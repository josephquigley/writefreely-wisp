/*
 * Copyright © 2026 Musing Studio LLC.
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
	"strings"
	"testing"
)

// There used to be two SSRF rulesets in this package with two different
// answers. The webfinger client's dial-time check refused CGNAT and plain
// multicast; isPublicIRI, which guards every attacker-supplied ActivityPub
// IRI, refused neither. An IRI pointing at 100.64.0.0/10 — the range a
// tailnet or a CGNAT'd host sits in — sailed straight through.
//
// isPublicAddr is now the only place the ruleset is written down, and both
// call paths ask it. These tests pin the union, and TestBothGuardPathsRefuse
// pins the specific hole that the unification closes.

func TestIsPublicAddr(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"nil address", "", false},

		{"IPv4 loopback", "127.0.0.1", false},
		{"IPv6 loopback", "::1", false},

		{"RFC1918 10/8", "10.0.0.5", false},
		{"RFC1918 172.16/12", "172.16.3.4", false},
		{"RFC1918 192.168/16", "192.168.1.1", false},
		{"IPv6 unique local", "fd00::1", false},

		// 169.254.0.0/16 is link-local unicast, and the cloud metadata
		// endpoint lives inside it. A later change adds a per-host
		// allowlist exemption to this checker; this range must never be
		// exempted, so it gets its own case rather than riding along on
		// the generic link-local one.
		{"link-local unicast", "169.254.10.1", false},
		{"cloud metadata endpoint", "169.254.169.254", false},
		{"IPv6 link-local unicast", "fe80::1", false},

		{"link-local multicast", "224.0.0.251", false},
		{"IPv6 link-local multicast", "ff02::1", false},
		{"IPv6 interface-local multicast", "ff01::1", false},
		{"global multicast", "239.1.2.3", false},

		{"unspecified IPv4", "0.0.0.0", false},
		{"unspecified IPv6", "::", false},

		{"CGNAT, this community's own range", "100.84.155.115", false},
		{"CGNAT lower bound", "100.64.0.0", false},
		{"CGNAT upper bound", "100.127.255.255", false},
		{"just below CGNAT is public", "100.63.255.255", true},
		{"just above CGNAT is public", "100.128.0.0", true},

		{"ordinary public IPv4", "93.184.216.34", true},
		{"ordinary public IPv6", "2606:2800:220:1:248:1893:25c8:1946", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ip net.IP
			if tc.ip != "" {
				ip = net.ParseIP(tc.ip)
				if ip == nil {
					t.Fatalf("test case has an unparseable address %q", tc.ip)
				}
			}
			if got := isPublicAddr(ip, "host.example", nil); got != tc.want {
				t.Errorf("isPublicAddr(%q) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

// TestBothGuardPathsRefuseCGNAT is the regression this unification fixes.
// The two paths keep different shapes — the webfinger client checks at dial
// time so that DNS rebinding cannot slip past it, isPublicIRI checks ahead
// of the request — but they must reach the same verdict about one address.
//
// A literal address needs no DNS: both net.LookupIP and the default
// resolver hand a numeric host straight back, so neither of these tests
// touches the network.
func TestBothGuardPathsRefuseCGNAT(t *testing.T) {
	const addr = "100.84.155.115"

	t.Run("webfinger dial", func(t *testing.T) {
		conn, err := safeDialContext(context.Background(), "tcp", net.JoinHostPort(addr, "443"))
		if err == nil {
			conn.Close()
			t.Fatalf("safeDialContext dialled %s; it must refuse CGNAT", addr)
		}
		if !errors.Is(err, errBlockedRemoteAddr) {
			t.Errorf("safeDialContext error = %v, want one wrapping errBlockedRemoteAddr", err)
		}
	})

	t.Run("isPublicIRI", func(t *testing.T) {
		err := isPublicIRI("https://" + addr + "/api/collections/blog")
		if err == nil {
			t.Fatalf("isPublicIRI allowed %s; it must refuse CGNAT", addr)
		}
		if !strings.Contains(err.Error(), addr) {
			t.Errorf("isPublicIRI error = %v, want it to name the disallowed address", err)
		}
	})
}
