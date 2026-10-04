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
	"strings"
	"testing"
)

func TestParsePrivateAddressAllowlistAccepts(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{"empty", "", nil},
		{"only separators", " , ,", nil},
		{"tailnet", "100.64.0.0/10", []string{"100.64.0.0/10"}},
		{"one tailnet host", "100.84.155.115", []string{"100.84.155.115/32"}},
		{"RFC 1918, trimmed", " 10.0.0.0/8 ,192.168.1.0/24", []string{"10.0.0.0/8", "192.168.1.0/24"}},
		{"172.16/12 subnet", "172.20.0.0/16", []string{"172.20.0.0/16"}},
		{"loopback for a local test server", "127.0.0.1", []string{"127.0.0.1/32"}},
		{"IPv6 unique local", "fd7a:115c:a1e0::/48", []string{"fd7a:115c:a1e0::/48"}},
		{"IPv6 loopback", "::1", []string{"::1/128"}},
		{"host bits are masked off", "100.84.155.115/10", []string{"100.64.0.0/10"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nets, err := ParsePrivateAddressAllowlist(tc.value)
			if err != nil {
				t.Fatalf("ParsePrivateAddressAllowlist(%q) = %v", tc.value, err)
			}
			var got []string
			for _, n := range nets {
				got = append(got, n.String())
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("ParsePrivateAddressAllowlist(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// Every refusal fails the whole value, so a typo cannot leave an operator
// with part of the allowlist they wrote. The dangerous entries each get their
// own case: these are the ones that must never parse.
func TestParsePrivateAddressAllowlistRefuses(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"cloud metadata endpoint", "169.254.169.254"},
		{"link-local range", "169.254.0.0/16"},
		{"IPv6 link-local", "fe80::/10"},
		{"everything", "0.0.0.0/0"},
		{"everything, IPv6", "::/0"},
		{"unspecified", "0.0.0.0"},
		{"multicast", "224.0.0.0/4"},
		{"wider than CGNAT", "100.0.0.0/8"},
		{"wider than RFC 1918", "10.0.0.0/7"},
		{"a public address", "93.184.216.34"},
		{"IPv4 in IPv6 form", "::ffff:10.0.0.0/104"},
		{"hostname", "talk.example.org"},
		{"bad prefix", "10.0.0.0/33"},
		{"one bad entry spoils the rest", "100.64.0.0/10, 169.254.169.254"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nets, err := ParsePrivateAddressAllowlist(tc.value)
			if err == nil {
				t.Fatalf("ParsePrivateAddressAllowlist(%q) = %v, want an error", tc.value, nets)
			}
			if !strings.Contains(err.Error(), "private_address_allowlist") {
				t.Errorf("error %q does not name the setting", err)
			}
		})
	}
}
