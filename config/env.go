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
	"os"
	"regexp"
	"strings"

	"github.com/go-ini/ini"
)

// envRef matches a value that is entirely a reference to an environment
// variable.
//
// The anchors are the point. os.ExpandEnv substitutes positionally, rewriting
// $NAME and ${NAME} anywhere inside a value, which mangles any password
// containing a dollar sign -- and a password is exactly what this feature
// exists to carry. Matching the whole value makes substitution
// all-or-nothing: "p@ss$word" and "$HOME" are literals and stay literals.
//
// The name pattern is the shell's, so "${not-a-legal-name}" is a literal too.
//
// There is no escape sequence, so a value whose literal content is exactly
// ${SOME_NAME} cannot be written. That is accepted: with an unset variable
// refusing to start, the only way to be silently wrong is for a variable of
// that exact name to be set.
var envRef = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// envRefKey locates one environment reference in a file, with the reference
// as it is written on disk.
type envRefKey struct {
	section string
	key     string
	raw     string
}

// envRefsIn lists every environment reference in the file.
//
// It reads Key.Value, which is the raw stored string, so it sees the
// reference itself even on a file that has a ValueMapper installed.
func envRefsIn(f *ini.File) []envRefKey {
	var refs []envRefKey
	for _, s := range f.Sections() {
		for _, k := range s.Keys() {
			if envRef.MatchString(k.Value()) {
				refs = append(refs, envRefKey{section: s.Name(), key: k.Name(), raw: k.Value()})
			}
		}
	}
	return refs
}

// checkEnvRefs reports every environment reference in the file whose variable
// is not set.
//
// This exists as a pass of its own because ini.ValueMapper is
// func(string) string: it cannot fail, and it is not told which key it is
// mapping. Refusing here is what turns a typo in .env into a named error at
// startup rather than the literal string "${WF_MAILGUN_PRIVATE}" being handed
// to Mailgun as an API key -- which surfaces as a newsletter that fails to
// send fifteen minutes after a publish, inside a five-minute pickup window
// that is never retried.
func checkEnvRefs(f *ini.File) error {
	var missing []string
	for _, ref := range envRefsIn(f) {
		name := envRef.FindStringSubmatch(ref.raw)[1]
		if _, ok := os.LookupEnv(name); !ok {
			missing = append(missing, fmt.Sprintf("[%s] %s references %s, which is not set",
				ref.section, ref.key, name))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("configuration references environment variables that are not set:\n  %s",
		strings.Join(missing, "\n  "))
}

// expandEnvRef is the ini.ValueMapper. A whole-value reference becomes the
// variable's value; everything else is returned untouched.
//
// os.Getenv's empty-string-for-unset is unreachable in normal use, because
// checkEnvRefs has already refused the load.
func expandEnvRef(val string) string {
	m := envRef.FindStringSubmatch(val)
	if m == nil {
		return val
	}
	return os.Getenv(m[1])
}
