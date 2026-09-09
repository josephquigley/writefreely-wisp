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
	"os"
	"strings"
	"testing"

	"github.com/go-ini/ini"
)

// unsetEnv clears a variable for the duration of the test. t.Setenv registers
// the cleanup that restores it; the Unsetenv that follows is what the test
// actually wants.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetenv %s: %v", name, err)
	}
}

func TestExpandEnvRefSubstitutesWholeValueReference(t *testing.T) {
	t.Setenv("WF_TEST_SECRET", "s3cret")
	if got := expandEnvRef("${WF_TEST_SECRET}"); got != "s3cret" {
		t.Errorf("expandEnvRef = %q, want s3cret", got)
	}
}

// A value is either entirely a reference or entirely a literal. os.ExpandEnv
// would rewrite the middle of several of these.
func TestExpandEnvRefLeavesLiteralsAlone(t *testing.T) {
	t.Setenv("HOME", "/root")
	t.Setenv("WF_TEST_SECRET", "s3cret")
	for _, val := range []string{
		"p@ss$word",
		"$HOME",
		"a${WF_TEST_SECRET}b",
		"${not-a-legal-name}",
		"50%pct",
		"${WF_TEST_SECRET} ",
	} {
		if got := expandEnvRef(val); got != val {
			t.Errorf("expandEnvRef(%q) = %q, want it unchanged", val, got)
		}
	}
}

func TestCheckEnvRefsPassesWhenEveryVariableIsSet(t *testing.T) {
	t.Setenv("WF_TEST_SECRET", "s3cret")
	f, err := ini.Load([]byte("[email]\nmailgun_private = ${WF_TEST_SECRET}\n"))
	if err != nil {
		t.Fatalf("ini.Load: %v", err)
	}
	if err := checkEnvRefs(f); err != nil {
		t.Errorf("checkEnvRefs: %v", err)
	}
}

// Every miss is reported at once. Finding one typo per restart, on a container
// that exits between attempts, is a bad way to fix a .env file.
func TestCheckEnvRefsReportsEveryMissingVariable(t *testing.T) {
	unsetEnv(t, "WF_TEST_MISSING_ONE")
	unsetEnv(t, "WF_TEST_MISSING_TWO")
	f, err := ini.Load([]byte(
		"[email]\nmailgun_private = ${WF_TEST_MISSING_ONE}\n" +
			"[oauth.generic]\nclient_secret = ${WF_TEST_MISSING_TWO}\n"))
	if err != nil {
		t.Fatalf("ini.Load: %v", err)
	}
	err = checkEnvRefs(f)
	if err == nil {
		t.Fatal("checkEnvRefs: want an error, got nil")
	}
	for _, want := range []string{
		"[email] mailgun_private references WF_TEST_MISSING_ONE",
		"[oauth.generic] client_secret references WF_TEST_MISSING_TWO",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestEnvRefsInFindsReferencesAndNothingElse(t *testing.T) {
	f, err := ini.Load([]byte(
		"[email]\nmailgun_private = ${WF_TEST_SECRET}\ndomain = mg.example.org\n"))
	if err != nil {
		t.Fatalf("ini.Load: %v", err)
	}
	refs := envRefsIn(f)
	if len(refs) != 1 {
		t.Fatalf("envRefsIn returned %d refs, want 1: %+v", len(refs), refs)
	}
	want := envRefKey{section: "email", key: "mailgun_private", raw: "${WF_TEST_SECRET}"}
	if refs[0] != want {
		t.Errorf("envRefsIn = %+v, want %+v", refs[0], want)
	}
}
