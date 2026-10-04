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

import "net"

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

// ssrfGuardOptions carries the per-call knobs of the address check. There
// are none in use yet: every caller passes nil, which means "the strict
// ruleset, no exceptions".
//
// It exists so that a caller can later hand in a per-host exemption — a
// federation allowlist, say, whose peers legitimately live on addresses the
// strict ruleset refuses — without reshaping the call sites again.
type ssrfGuardOptions struct {
	// hostAllowed, when non-nil, is consulted for the hostname an address
	// was resolved from. It is a seam and nothing more today: no caller
	// sets it, and isPublicAddr's verdict does not yet depend on it.
	//
	// Whatever eventually reads it must keep 169.254.0.0/16 refused
	// unconditionally. That range carries the cloud metadata endpoint at
	// 169.254.169.254, which hands out instance credentials to anything
	// that asks, and no allowlist entry is worth reaching it — an
	// exemption is a statement about a *peer*, and any host at all can
	// name that address.
	hostAllowed func(host string) bool
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
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		// IsLinkLocalUnicast covers 169.254.0.0/16, and so covers the
		// cloud metadata address 169.254.169.254. Keep it refused: see
		// ssrfGuardOptions.hostAllowed for why no exemption may ever
		// reach it.
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 100.64.0.0/10, Carrier-Grade NAT. Nothing in net.IP reports
		// this range, and it is where a tailnet or a CGNAT'd private
		// host sits, so it has to be spelled out.
		if ip4[0] == 100 && ip4[1]&0xc0 == 64 {
			return false
		}
	}
	// An IPv6 transition address (6to4, Teredo, NAT64, IPv4-compatible)
	// carries an IPv4 address that the checks above never see; run them
	// again on it. The recursion ends because embeddedIPv4 returns nil for
	// an IPv4 address.
	if embedded := embeddedIPv4(ip); embedded != nil {
		if !isPublicAddr(embedded, host, opts) {
			return false
		}
	}
	return true
}

// embeddedIPv4 returns the IPv4 address encoded inside an IPv6 transition
// address (6to4, NAT64, Teredo, or the deprecated IPv4-compatible format), or
// nil if ip embeds none.
func embeddedIPv4(ip net.IP) net.IP {
	// Already an IPv4 address: nothing embedded to extract.
	if ip.To4() != nil {
		return nil
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return nil
	}
	switch {
	case ip16[0] == 0x20 && ip16[1] == 0x02:
		// 6to4: 2002:AABB:CCDD::/16 -> A.B.C.D
		return net.IPv4(ip16[2], ip16[3], ip16[4], ip16[5]).To4()
	case ip16[0] == 0x20 && ip16[1] == 0x01 && ip16[2] == 0x00 && ip16[3] == 0x00:
		// Teredo: 2001:0000::/32. The client IPv4 is the last 32 bits, stored
		// bitwise-inverted.
		return net.IPv4(^ip16[12], ^ip16[13], ^ip16[14], ^ip16[15]).To4()
	case ip16[0] == 0x00 && ip16[1] == 0x64 && ip16[2] == 0xff && ip16[3] == 0x9b:
		// NAT64 well-known prefix 64:ff9b::/96 and local prefix
		// 64:ff9b:1::/48. Both carry the embedded IPv4 in the last 32 bits.
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]).To4()
	}
	// IPv4-compatible IPv6 (deprecated): ::/96, e.g. ::127.0.0.1 == ::7f00:1.
	// The first 12 bytes are zero. Loopback (::1) and unspecified (::) are
	// already rejected by the callers before extraction, so treating them as
	// embedding 0.0.0.1 / 0.0.0.0 here is harmless.
	for _, b := range ip16[:12] {
		if b != 0 {
			return nil
		}
	}
	return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]).To4()
}
