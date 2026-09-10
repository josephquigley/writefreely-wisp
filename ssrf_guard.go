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
	"sync"
)

// This file holds the one SSRF ruleset in the package. It used to be two:
// the webfinger client's dial-time check refused CGNAT and plain multicast,
// while isPublicIRI — which guards every attacker-supplied ActivityPub IRI,
// the more exposed of the two paths — refused neither. Two rulesets meant
// two answers for the same address, and the weaker one guarded the wider
// door.
//
// What stays different is the *shape* of the two checks, because they are
// not the same kind of check:
//
//   - safeDialContext checks at dial time, per connection, after the
//     resolver has run, so a DNS rebind cannot land a request on an address
//     that was public when it was first looked up.
//   - isPublicIRI checks ahead of the request, so an IRI is refused before
//     anything is signed or sent.
//
// Only the per-address verdict is shared. Both ask isPublicAddr.

// ssrfGuardOptions carries the per-call knobs of the address check. The
// only knob is a per-host exemption, and callers get it from guardOptions
// rather than building one: the guard is reached from a package-level HTTP
// client that has no App to ask.
//
// A nil *ssrfGuardOptions means "the strict ruleset, no exceptions", which
// is what every caller gets until an allowlist is configured.
type ssrfGuardOptions struct {
	// hostAllowed, when non-nil, is consulted for the hostname an address
	// was resolved from. It answers one question only: may THIS peer be
	// reached on an address the strict ruleset refuses?
	//
	// It is asked per hostname, not per request, so a redirect earns the
	// exemption on the name of the hop actually being dialled — an
	// allowlisted peer cannot redirect the client onto a private address
	// under some other name.
	//
	// It can only ever reopen the two ranges a private peer plausibly
	// sits on: RFC1918/ULA, and 100.64.0.0/10. It is never asked about
	// loopback, link-local, multicast or the unspecified address. That
	// matters most for 169.254.0.0/16, which carries the cloud metadata
	// endpoint at 169.254.169.254: it hands out instance credentials to
	// anything that asks, and no allowlist entry is worth reaching it. An
	// exemption is a statement about a peer's *network*, and any host at
	// all can name that address.
	hostAllowed func(host string) bool
}

// peerAddressExemption is the exemption installed at startup, and is nil
// until then — and on any instance with no federation allowlist, forever.
// It is process-global because safeWebfingerHTTPClient is: the dial-time
// guard runs inside an http.Transport built once at package scope, with no
// App in reach.
var (
	peerAddressExemptionMu sync.RWMutex
	peerAddressExemption   func(host string) bool
)

// setPeerAddressExemption installs the exemption consulted by every guarded
// path. Passing nil removes it, restoring the strict ruleset.
func setPeerAddressExemption(fn func(host string) bool) {
	peerAddressExemptionMu.Lock()
	defer peerAddressExemptionMu.Unlock()
	peerAddressExemption = fn
}

// currentPeerAddressExemption returns the installed exemption, or nil.
func currentPeerAddressExemption() func(host string) bool {
	peerAddressExemptionMu.RLock()
	defer peerAddressExemptionMu.RUnlock()
	return peerAddressExemption
}

// guardOptions is what every guarded call site passes to isPublicAddr. It
// returns nil when no exemption is installed, so an instance without a
// federation allowlist behaves exactly as it did before there was one.
func guardOptions() *ssrfGuardOptions {
	fn := currentPeerAddressExemption()
	if fn == nil {
		return nil
	}
	return &ssrfGuardOptions{hostAllowed: fn}
}

// hostExempt reports whether host has been exempted from the private-range
// half of the ruleset. An empty hostname is never exempt: there is nothing
// to have allowlisted.
func hostExempt(host string, opts *ssrfGuardOptions) bool {
	if opts == nil || opts.hostAllowed == nil || host == "" {
		return false
	}
	return opts.hostAllowed(host)
}

// isPublicAddr reports whether ip is safe to connect to, i.e. not a
// loopback, private, link-local, CGNAT, multicast, or otherwise
// special-purpose address that could be used to reach internal services or
// cloud metadata endpoints via SSRF.
//
// host is the hostname ip was resolved from, or "" when there isn't one; it
// is only here for opts. opts may be nil, and is nil everywhere today.
func isPublicAddr(ip net.IP, host string, opts *ssrfGuardOptions) bool {
	if ip == nil {
		return false
	}
	// Refused for every host, exemption or not. IsLinkLocalUnicast covers
	// 169.254.0.0/16 and so covers the cloud metadata address
	// 169.254.169.254: see ssrfGuardOptions.hostAllowed for why nothing
	// may reopen it.
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// The two ranges a peer on a private network plausibly sits on: an
	// RFC1918 address or an IPv6 ULA, and 100.64.0.0/10, which is where a
	// tailnet puts its hosts. Nothing in net.IP reports CGNAT, so it has
	// to be spelled out. Refused unless this particular host has been
	// exempted.
	if ip.IsPrivate() || isCGNAT(ip) {
		return hostExempt(host, opts)
	}
	return true
}

// isCGNAT reports whether ip is in 100.64.0.0/10, the Carrier-Grade NAT
// range. Tailscale hands its nodes addresses out of it, which is why an
// instance whose peers are on a tailnet needs the exemption above at all.
func isCGNAT(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64
}
