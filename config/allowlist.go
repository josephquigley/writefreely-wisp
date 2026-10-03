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
	"strings"
)

// WildcardPrefix marks a federation allowlist entry that matches subdomains
// rather than one exact host.
const WildcardPrefix = "*."

// ParseFederationAllowlist turns a comma-separated list of hostnames into a
// set. Entries are trimmed and lowercased, and empty entries are dropped, so
// a value of only whitespace or commas yields an empty set. A wildcard entry
// ("*.example.org") is kept verbatim; ValidateFederationAllowlist checks it.
func ParseFederationAllowlist(s string) map[string]bool {
	allowed := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		host := strings.ToLower(strings.TrimSpace(part))
		if host != "" {
			allowed[host] = true
		}
	}
	return allowed
}

// ValidateFederationAllowlist rejects entries whose "*" cannot be honoured
// as written. An admin finds out when saving: a malformed wildcard that
// survived to match time would simply match nothing, and an allowlist that
// quietly matches nothing looks exactly like one that is working until the
// day someone expects it to admit a peer.
//
// A bare "*" is refused for the opposite reason. It would parse as an exact
// host named "*", which no hostname is, so it too matches nothing — but an
// operator who wrote it plainly meant "everything", and the honest way to say
// that is to leave the allowlist empty.
//
// This is syntax only. That an allowlist needs a private instance is a rule
// across settings and is checked where the whole configuration is known.
func ValidateFederationAllowlist(allowed map[string]bool) error {
	for entry := range allowed {
		suffix, isWildcard := strings.CutPrefix(entry, WildcardPrefix)
		if !isWildcard {
			if strings.Contains(entry, "*") {
				return fmt.Errorf("federation_allowlist entry %q: a wildcard is only meaningful as a leading %q", entry, WildcardPrefix)
			}
			continue
		}
		if suffix == "" {
			return fmt.Errorf("federation_allowlist entry %q has no domain after the wildcard", entry)
		}
		if strings.Contains(suffix, "*") {
			return fmt.Errorf("federation_allowlist entry %q: only one leading wildcard is supported", entry)
		}
	}
	return nil
}

func validateFederationAllowlistText(s string) error {
	return ValidateFederationAllowlist(ParseFederationAllowlist(s))
}
