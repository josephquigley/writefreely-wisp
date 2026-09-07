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
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/writefreely/writefreely/config"
)

// isPublicAddr blocks every address a webfinger lookup has no business
// reaching, CGNAT (100.64.0.0/10) included. That is right for a handle a
// stranger typed into a post — the lookup target is user-supplied, so an
// unguarded RemoteLookup is an SSRF primitive.
//
// It is also fatal to a private deployment whose peers sit on a Tailscale
// network, where every peer resolves into CGNAT: no handle on any peer can
// ever be resolved, so a reply delegate cannot be configured at all.
//
// The one thing that separates "a peer this operator federates with" from
// "an internal address an attacker named" is the federation allowlist, which
// the operator writes by hand and which an instance may only run while
// private. So an allowlisted host may be dialled at an address that would
// otherwise be refused — and nothing else changes.
//
// Loopback, link-local (where the cloud metadata endpoint lives), multicast
// and the unspecified address stay refused for every host, allowlisted or
// not. A named peer may live on a private network; it has no business being
// 127.0.0.1 or 169.254.169.254.

func allowlistedApp(t *testing.T, allowlist string) *App {
	t.Helper()
	cfg := config.New()
	cfg.App.Private = true
	cfg.App.Federation = true
	cfg.App.FederationAllowlist = allowlist
	app := &App{cfg: cfg}
	if err := app.initFederationAllowlist(); err != nil {
		t.Fatalf("initFederationAllowlist: %v", err)
	}
	return app
}

func TestDialAllowedPublicAddressNeedsNoAllowlist(t *testing.T) {
	app := allowlistedApp(t, "")
	assert.True(t, app.dialAllowed("example.org", net.ParseIP("93.184.216.34")))
}

func TestDialAllowedCGNATRefusedForUnlistedHost(t *testing.T) {
	app := allowlistedApp(t, "peer.example")
	assert.False(t, app.dialAllowed("stranger.example", net.ParseIP("100.84.155.115")),
		"a host nobody allowlisted must not reach a CGNAT address")
}

func TestDialAllowedCGNATPermittedForAllowlistedHost(t *testing.T) {
	app := allowlistedApp(t, "peer.example")
	assert.True(t, app.dialAllowed("peer.example", net.ParseIP("100.84.155.115")),
		"an allowlisted peer on a tailnet must be reachable")
}

func TestDialAllowedPrivateRangePermittedForAllowlistedHost(t *testing.T) {
	app := allowlistedApp(t, "peer.example")
	assert.True(t, app.dialAllowed("peer.example", net.ParseIP("10.1.2.3")))
	assert.True(t, app.dialAllowed("peer.example", net.ParseIP("172.20.0.5")))
}

func TestDialAllowedWildcardEntryPermitsSubdomain(t *testing.T) {
	app := allowlistedApp(t, "*.beta.example")
	assert.True(t, app.dialAllowed("mbin.beta.example", net.ParseIP("100.84.155.115")))
	assert.False(t, app.dialAllowed("beta.example", net.ParseIP("100.84.155.115")),
		"a wildcard never matches its own apex")
}

// The addresses no allowlist may unlock.
func TestDialAllowedNeverPermitsLoopbackOrMetadata(t *testing.T) {
	app := allowlistedApp(t, "peer.example")
	for _, ip := range []string{"127.0.0.1", "::1", "169.254.169.254", "224.0.0.1", "0.0.0.0"} {
		assert.False(t, app.dialAllowed("peer.example", net.ParseIP(ip)),
			"allowlisting a host must not unlock %s", ip)
	}
}

// With no allowlist configured the guard must behave exactly as it did
// before this change: federationAllowed returns true for every host when the
// allowlist is empty, and that must not be read as permission to dial.
func TestDialAllowedEmptyAllowlistBlocksPrivateAddresses(t *testing.T) {
	app := allowlistedApp(t, "")
	assert.False(t, app.dialAllowed("anything.example", net.ParseIP("100.84.155.115")))
	assert.False(t, app.dialAllowed("anything.example", net.ParseIP("10.0.0.1")))
}

// A nil App is what a caller with no instance context has. It must get the
// strict behaviour rather than a panic.
func TestDialAllowedNilAppIsStrict(t *testing.T) {
	var app *App
	assert.True(t, app.dialAllowed("example.org", net.ParseIP("93.184.216.34")))
	assert.False(t, app.dialAllowed("peer.example", net.ParseIP("100.84.155.115")))
}
