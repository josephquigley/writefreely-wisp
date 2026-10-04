/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package config

import (
	"fmt"
	"net"
	"strings"
)

// reopenableRanges are the only address ranges a private address allowlist
// may name. Every one of them is refused by the SSRF guard by default, and
// every one of them is somewhere a peer the operator chose can legitimately
// live: a LAN, a tailnet or other CGNAT'd network, or the same machine.
//
// Link-local (169.254.0.0/16, fe80::/10) is deliberately absent. It carries
// the cloud metadata endpoint at 169.254.169.254, which hands out instance
// credentials to anything that asks, and no peer is worth reaching it.
// Multicast and the unspecified address are absent because nothing that
// answers an HTTP request lives there.
var reopenableRanges = mustParseCIDRs(
	"10.0.0.0/8",     // RFC 1918
	"172.16.0.0/12",  // RFC 1918
	"192.168.0.0/16", // RFC 1918
	"100.64.0.0/10",  // CGNAT, where a tailnet sits
	"127.0.0.0/8",    // loopback, for a test server on this machine
	"fc00::/7",       // IPv6 unique local
	"::1/128",        // IPv6 loopback
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		nets = append(nets, n)
	}
	return nets
}

// ParsePrivateAddressAllowlist turns a comma-separated list of CIDR ranges
// into networks. A bare address is read as a range holding only itself.
// Empty entries are dropped, so an empty value yields no networks and leaves
// the SSRF guard strict.
//
// Every entry must lie wholly inside one of reopenableRanges. Anything else
// is refused rather than ignored: "0.0.0.0/0" would otherwise reopen the
// metadata endpoint, and a public range is refused because it is never
// blocked in the first place, so listing one means the operator has
// misunderstood what the setting does.
func ParsePrivateAddressAllowlist(s string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		n, err := parseAddressEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("private_address_allowlist entry %q: %v", entry, err)
		}
		if !reopenable(n) {
			return nil, fmt.Errorf("private_address_allowlist entry %q: only private (RFC 1918, fc00::/7), CGNAT (100.64.0.0/10) and loopback ranges may be listed", entry)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

func parseAddressEntry(entry string) (*net.IPNet, error) {
	if strings.Contains(entry, "/") {
		ip, n, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("not a CIDR range")
		}
		if ip.To4() != nil && len(n.IP) == net.IPv6len {
			// "::ffff:10.0.0.0/104" and the like. Containment across the
			// two families is easy to get wrong, and the IPv4 spelling
			// says the same thing.
			return nil, fmt.Errorf("write an IPv4 range in IPv4 form")
		}
		return n, nil
	}
	ip := net.ParseIP(entry)
	if ip == nil {
		return nil, fmt.Errorf("not an IP address or CIDR range")
	}
	if ip4 := ip.To4(); ip4 != nil {
		return &net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}, nil
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
}

// reopenable reports whether n lies wholly inside one of reopenableRanges:
// same family, its first address inside the range, and a prefix at least as
// long as the range's.
func reopenable(n *net.IPNet) bool {
	ones, bits := n.Mask.Size()
	for _, r := range reopenableRanges {
		rOnes, rBits := r.Mask.Size()
		if bits == rBits && ones >= rOnes && r.Contains(n.IP) {
			return true
		}
	}
	return false
}
