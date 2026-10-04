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
	"sync/atomic"

	"github.com/writeas/web-core/log"
	"github.com/writefreely/writefreely/config"
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

// ssrfAllowlist holds the ranges from [server] private_address_allowlist.
// config.ParsePrivateAddressAllowlist has already confined them to private,
// CGNAT and loopback space, and isPublicAddr refuses link-local, multicast
// and the unspecified address before it looks at them, so no entry can reach
// the cloud metadata endpoint at 169.254.169.254.
//
// It is package state rather than App state because safeDialContext sits
// under package-level HTTP clients that have no App to ask.
var ssrfAllowlist atomic.Pointer[[]*net.IPNet]

// initPrivateAddressAllowlist installs cfg's private address allowlist. A
// value that fails to parse installs nothing and fails startup: an operator
// who wrote an allowlist must not find out it was ignored from a peer that
// silently stopped receiving posts.
func initPrivateAddressAllowlist(cfg *config.Config) error {
	nets, err := config.ParsePrivateAddressAllowlist(cfg.Server.PrivateAddressAllowlist)
	if err != nil {
		return err
	}
	ssrfAllowlist.Store(&nets)
	if len(nets) > 0 {
		log.Info("SSRF guard: outbound requests may reach %s (private_address_allowlist)", cfg.Server.PrivateAddressAllowlist)
	}
	return nil
}

// privateAddressAllowlist returns the ranges both guards let through, or
// nil when none are configured.
func privateAddressAllowlist() []*net.IPNet {
	if nets := ssrfAllowlist.Load(); nets != nil {
		return *nets
	}
	return nil
}

// isPublicAddr reports whether ip is safe to connect to, i.e. not a
// loopback, private, link-local, CGNAT, multicast, or otherwise
// special-purpose address that could be used to reach internal services or
// cloud metadata endpoints via SSRF. An address inside one of the allowed
// ranges is reported safe, unless it is link-local, multicast or
// unspecified, which nothing reopens. allowed may be nil.
func isPublicAddr(ip net.IP, allowed []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		// IsLinkLocalUnicast covers 169.254.0.0/16, and so covers the
		// cloud metadata address 169.254.169.254. It is checked before
		// the allowlist so that no entry can ever reopen it.
		return false
	}
	for _, n := range allowed {
		if n.Contains(ip) {
			return true
		}
	}
	return isStrictlyPublicAddr(ip)
}

// isStrictlyPublicAddr is the default ruleset, with no allowlist. The
// allowlist matches only the address actually dialled, never an IPv4
// address embedded in an IPv6 transition address, so the recursion below
// stays strict.
func isStrictlyPublicAddr(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
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
		if !isStrictlyPublicAddr(embedded) {
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
