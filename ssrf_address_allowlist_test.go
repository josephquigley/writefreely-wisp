/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/writefreely/writefreely/config"
)

// [server] private_address_allowlist reopens operator-chosen private ranges
// to both SSRF guards: peers on a tailnet, and test servers on a LAN or this
// machine. These tests pin what it reopens, what it never reopens, and that
// both guard paths and the ActivityPub client honour it.

// setPrivateAddressAllowlist installs value as the allowlist for one test.
func setPrivateAddressAllowlist(t *testing.T, value string) {
	t.Helper()
	prev := ssrfAllowlist.Load()
	t.Cleanup(func() { ssrfAllowlist.Store(prev) })
	cfg := config.New()
	cfg.Server.PrivateAddressAllowlist = value
	if err := initPrivateAddressAllowlist(cfg); err != nil {
		t.Fatalf("initPrivateAddressAllowlist(%q) = %v", value, err)
	}
}

func mustCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var nets []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		nets = append(nets, n)
	}
	return nets
}

func TestIsPublicAddrHonoursAllowlist(t *testing.T) {
	allowed := mustCIDRs(t, "100.64.0.0/10", "192.168.1.0/24", "127.0.0.0/8", "fd7a:115c:a1e0::/48")
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"listed CGNAT host", "100.84.155.115", true},
		{"listed LAN host", "192.168.1.20", true},
		{"listed loopback", "127.0.0.1", true},
		{"listed tailnet IPv6", "fd7a:115c:a1e0::1", true},
		{"IPv4-mapped form of a listed host", "::ffff:100.84.155.115", true},

		{"unlisted LAN", "192.168.2.20", false},
		{"unlisted RFC 1918", "10.0.0.5", false},
		{"unlisted IPv6 loopback", "::1", false},
		{"unlisted ULA", "fd00::1", false},

		// An embedded address faces the strict rules even when the
		// address it embeds is listed: the allowlist names addresses to
		// dial, not addresses to tunnel to.
		{"6to4 wrapping a listed CGNAT host", "2002:6454:9b73::", false},
		{"NAT64 wrapping listed loopback", "64:ff9b::7f00:1", false},

		{"public stays public", "93.184.216.34", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPublicAddr(net.ParseIP(tc.ip), allowed); got != tc.want {
				t.Errorf("isPublicAddr(%q) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

// The parser refuses link-local, multicast and unspecified entries, and
// isPublicAddr refuses those addresses before it consults the allowlist at
// all. This test builds options the parser would never produce, to show the
// second check holds without the first.
func TestAllowlistNeverReopensMetadataOrMulticast(t *testing.T) {
	allowed := mustCIDRs(t, "0.0.0.0/0", "::/0")
	for _, ip := range []string{
		"169.254.169.254", "169.254.10.1", "fe80::1",
		"224.0.0.251", "239.1.2.3", "ff02::1", "ff01::1",
		"0.0.0.0", "::",
	} {
		if isPublicAddr(net.ParseIP(ip), allowed) {
			t.Errorf("isPublicAddr(%q) = true with everything listed; it must stay refused", ip)
		}
	}
}

func TestBothGuardPathsHonourAllowlist(t *testing.T) {
	const addr = "100.84.155.115"
	setPrivateAddressAllowlist(t, "100.64.0.0/10")

	t.Run("isPublicIRI", func(t *testing.T) {
		if err := isPublicIRI("https://" + addr + "/api/collections/blog"); err != nil {
			t.Errorf("isPublicIRI refused a listed address: %v", err)
		}
		if err := isPublicIRI("https://10.0.0.5/api/collections/blog"); err == nil {
			t.Error("isPublicIRI allowed an unlisted private address")
		}
	})

	t.Run("dial", func(t *testing.T) {
		// A canceled context makes the dial fail without touching the
		// network, so the error says which side of the guard it failed on.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := safeDialContext(ctx, "tcp", net.JoinHostPort(addr, "443"))
		if errors.Is(err, errBlockedRemoteAddr) {
			t.Errorf("safeDialContext refused a listed address: %v", err)
		}
		_, err = safeDialContext(ctx, "tcp", net.JoinHostPort("10.0.0.5", "443"))
		if !errors.Is(err, errBlockedRemoteAddr) {
			t.Errorf("safeDialContext error for an unlisted address = %v, want errBlockedRemoteAddr", err)
		}
	})
}

// TestActivityPubClientReachesListedLoopback is the local test server case,
// end to end through the real dialer: listed, the request arrives; unlisted,
// TestActivityPubClientRefusesLoopback already shows it does not.
func TestActivityPubClientReachesListedLoopback(t *testing.T) {
	prev := activityPubDialContext
	activityPubDialContext = safeDialContext
	t.Cleanup(func() { activityPubDialContext = prev })
	setPrivateAddressAllowlist(t, "127.0.0.0/8")

	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer srv.Close()

	resp, err := activityPubClient().Get(srv.URL + "/inbox")
	if err != nil {
		t.Fatalf("activityPubClient could not reach a listed loopback server: %v", err)
	}
	resp.Body.Close()
	if !reached {
		t.Error("the request did not reach the server")
	}
}

// A value that fails to parse installs nothing, so the guard is never left
// half-configured.
func TestInitPrivateAddressAllowlistRefusesBadValue(t *testing.T) {
	setPrivateAddressAllowlist(t, "100.64.0.0/10")

	cfg := config.New()
	cfg.Server.PrivateAddressAllowlist = "192.168.0.0/16, 169.254.169.254"
	if err := initPrivateAddressAllowlist(cfg); err == nil {
		t.Fatal("initPrivateAddressAllowlist accepted the metadata endpoint")
	}
	if isPublicAddr(net.ParseIP("192.168.1.1"), privateAddressAllowlist()) {
		t.Error("a refused allowlist installed part of itself")
	}
	if !isPublicAddr(net.ParseIP("100.84.155.115"), privateAddressAllowlist()) {
		t.Error("a refused allowlist removed the one already in force")
	}
}

func TestEmptyAllowlistIsStrict(t *testing.T) {
	setPrivateAddressAllowlist(t, "")
	if len(privateAddressAllowlist()) != 0 {
		t.Error("an empty allowlist installed ranges")
	}
	if isPublicAddr(net.ParseIP("100.84.155.115"), privateAddressAllowlist()) {
		t.Error("an empty allowlist reopened CGNAT")
	}
}
