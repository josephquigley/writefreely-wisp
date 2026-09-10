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
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/writefreely/writefreely/config"
)

// The strict ruleset in ssrf_guard.go refuses 100.64.0.0/10, which is where
// a tailnet's hosts live. On an instance whose peers are all on a tailnet
// that means no peer handle resolves at all: every webfinger lookup for a
// sibling community is refused before it is sent.
//
// The exemption these tests pin is deliberately narrow. It is keyed on the
// *hostname* being connected to, it is only reachable for a host the
// operator named in federation_allowlist, and it reopens only the two
// ranges a private peer plausibly sits in. Loopback, link-local and the
// cloud metadata address stay refused for every host, allowlisted or not.

// withPeerExemption installs an exemption for one test and restores whatever
// was there before. The exemption is process-global because the guard is
// reached from a package-level HTTP client that has no App to consult.
func withPeerExemption(t *testing.T, fn func(host string) bool) {
	t.Helper()
	orig := currentPeerAddressExemption()
	setPeerAddressExemption(fn)
	t.Cleanup(func() { setPeerAddressExemption(orig) })
}

func exemptOnly(hosts ...string) func(string) bool {
	allowed := map[string]bool{}
	for _, h := range hosts {
		allowed[h] = true
	}
	return func(host string) bool { return allowed[host] }
}

func TestAllowlistedHostMayResolveIntoPrivateSpace(t *testing.T) {
	withPeerExemption(t, exemptOnly("talk.paisans.community"))

	tests := []struct {
		name string
		ip   string
		host string
		want bool
	}{
		{"CGNAT for an allowlisted host", "100.84.155.115", "talk.paisans.community", true},
		{"RFC1918 for an allowlisted host", "10.0.0.5", "talk.paisans.community", true},
		{"IPv6 ULA for an allowlisted host", "fd00::1", "talk.paisans.community", true},
		{"CGNAT for a host nobody allowlisted", "100.84.155.115", "evil.example", false},
		{"RFC1918 for a host nobody allowlisted", "10.0.0.5", "evil.example", false},
		{"CGNAT with no hostname at all", "100.84.155.115", "", false},
		{"a public address is unaffected", "93.184.216.34", "evil.example", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPublicAddr(net.ParseIP(tt.ip), tt.host, guardOptions())
			assert.Equal(t, tt.want, got)
		})
	}
}

// The half that matters more. An exemption says "this peer is reachable on a
// private network", not "this peer may point me anywhere".
func TestAllowlistedHostIsStillRefusedTheDangerousRanges(t *testing.T) {
	// The most permissive exemption there is: every host is allowlisted.
	withPeerExemption(t, func(string) bool { return true })

	for _, ip := range []string{
		"127.0.0.1",
		"::1",
		"169.254.169.254", // the cloud metadata endpoint
		"169.254.1.1",
		"fe80::1",
		"ff02::1",
		"ff01::1",
		"239.1.2.3",
		"0.0.0.0",
		"::",
	} {
		t.Run(ip, func(t *testing.T) {
			assert.False(t, isPublicAddr(net.ParseIP(ip), "talk.paisans.community", guardOptions()),
				"%s must stay refused even for an allowlisted host", ip)
		})
	}
}

// Both guard paths read the same exemption, so a mention resolves and the
// actor fetch that follows it is not refused a moment later.
func TestBothGuardPathsHonourTheExemption(t *testing.T) {
	// safeDialContext and isPublicIRI both take the hostname from the
	// address they were handed, which for a literal is the literal itself.
	// Exempting it keeps this test off the network.
	withPeerExemption(t, exemptOnly("100.84.155.115"))

	assert.NoError(t, isPublicIRI("https://100.84.155.115/users/quigs"),
		"isPublicIRI must honour the exemption")

	_, err := safeDialContext(t.Context(), "tcp", "100.84.155.115:9")
	assert.NotErrorIs(t, err, errBlockedRemoteAddr,
		"the dial-time guard must honour the exemption; any error here should be the connection failing, not the address being refused")
}

func TestBothGuardPathsRefuseAnUnexemptedPrivateAddress(t *testing.T) {
	withPeerExemption(t, exemptOnly("talk.paisans.community"))

	assert.Error(t, isPublicIRI("https://100.84.155.115/users/quigs"))

	_, err := safeDialContext(t.Context(), "tcp", "100.84.155.115:9")
	assert.ErrorIs(t, err, errBlockedRemoteAddr)
}

// federationAllowed answers true for every host when no allowlist is
// configured — "no allowlist" means "federate with anyone". Read straight,
// that would turn an unconfigured instance into one that exempts every host
// from the address guard, which is the opposite of what an empty
// configuration should mean.
func TestNoAllowlistExemptsNothing(t *testing.T) {
	withPeerExemption(t, nil)

	cfg := config.New()
	cfg.App.Private = true
	app := &App{cfg: cfg}
	assert.NoError(t, app.initFederationAllowlist())

	assert.True(t, app.federationAllowed("evil.example"),
		"precondition: with no allowlist, federationAllowed admits everyone")
	assert.False(t, app.peerAddressExempt("evil.example"),
		"an unconfigured allowlist must exempt no host from the address guard")
	assert.Nil(t, guardOptions(),
		"with no allowlist there is no exemption to install")
}

func TestPeerAddressExemptMatchesTheAllowlist(t *testing.T) {
	// initFederationAllowlist installs the process-global exemption as a
	// side effect, so put back whatever was there when this test is done.
	withPeerExemption(t, currentPeerAddressExemption())

	cfg := config.New()
	cfg.App.Private = true
	cfg.App.FederationAllowlist = "talk.paisans.community, *.beta.paisans.community"
	app := &App{cfg: cfg}
	assert.NoError(t, app.initFederationAllowlist())

	assert.True(t, app.peerAddressExempt("talk.paisans.community"), "exact entry")
	assert.True(t, app.peerAddressExempt("TALK.paisans.community"), "matching is case-insensitive")
	assert.True(t, app.peerAddressExempt("gts.beta.paisans.community"), "wildcard entry")
	assert.False(t, app.peerAddressExempt("beta.paisans.community"),
		"a wildcard does not cover its own apex, exactly as federationAllowed has it")
	assert.False(t, app.peerAddressExempt("evilbeta.paisans.community"),
		"wildcards match on label boundaries")
	assert.False(t, app.peerAddressExempt("blog.paisans.community"), "not on the allowlist")
	assert.False(t, app.peerAddressExempt(""), "no hostname, no exemption")
}

func TestInitFederationAllowlistInstallsTheExemption(t *testing.T) {
	withPeerExemption(t, nil)

	cfg := config.New()
	cfg.App.Private = true
	cfg.App.FederationAllowlist = "talk.paisans.community"
	app := &App{cfg: cfg}
	assert.NoError(t, app.initFederationAllowlist())

	assert.NotNil(t, guardOptions(), "startup must install the exemption")
	assert.True(t, isPublicAddr(net.ParseIP("100.84.155.115"), "talk.paisans.community", guardOptions()))
	assert.False(t, isPublicAddr(net.ParseIP("100.84.155.115"), "blog.example", guardOptions()))
}

// A rejected allowlist must not leave an exemption installed behind it: the
// process is about to fail startup, but a test — or any later caller — must
// not see a half-applied configuration.
func TestARejectedAllowlistInstallsNoExemption(t *testing.T) {
	withPeerExemption(t, nil)

	cfg := config.New()
	cfg.App.Private = true
	cfg.App.FederationAllowlist = "*"
	app := &App{cfg: cfg}

	assert.Error(t, app.initFederationAllowlist())
	assert.Nil(t, guardOptions(), "a refused allowlist must install nothing")
}
