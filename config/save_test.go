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
	"path/filepath"
	"strings"
	"testing"
)

// hasSetting reports whether the saved file carries the given "key = value"
// line. go-ini aligns the "=" to the longest key in a section on write, and
// Save fills in every key the struct has, so the padding a line ends up with
// is not something a test should pin down.
func hasSetting(body, setting string) bool {
	want := strings.Join(strings.Fields(setting), " ")
	for _, line := range strings.Split(body, "\n") {
		if strings.Join(strings.Fields(line), " ") == want {
			return true
		}
	}
	return false
}

const savableConfig = `[app]
; the Pocket ID notes and the restore instructions live in comments like this
site_name = old name
host = http://localhost:8080

[email]
domain = mg.example.org
future_key = a key this version of the struct does not know about
`

// An admin saving settings must not cost the operator the comments that
// explain the file, nor the keys a newer version added. ini.Empty() plus
// ReflectFrom, which is what Save used to do, loses both.
func TestSavePreservesCommentsAndUnmappedKeys(t *testing.T) {
	p := writeConfig(t, savableConfig)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.App.SiteName = "new name"
	if err := Save(cfg, p); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		"; the Pocket ID notes and the restore instructions live in comments like this",
		"future_key",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("saved config is missing %q:\n%s", want, got)
		}
	}
	if !hasSetting(got, "site_name = new name") {
		t.Errorf("saved config did not take the edited site_name:\n%s", got)
	}
}

// The whole point of the feature: a UI save of an unrelated setting must not
// write the expanded secret back into the file.
func TestSaveKeepsEnvReferenceUnexpanded(t *testing.T) {
	t.Setenv("WF_TEST_MAILGUN", "mg-secret")
	p := writeConfig(t, "[app]\nsite_name = old name\nhost = http://localhost:8080\n\n"+
		"[email]\nmailgun_private = ${WF_TEST_MAILGUN}\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Email.MailgunPrivate != "mg-secret" {
		t.Fatalf("precondition: mailgun_private = %q, want the expanded value", cfg.Email.MailgunPrivate)
	}
	cfg.App.SiteName = "new name"
	if err := Save(cfg, p); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	got := string(b)
	if !hasSetting(got, "mailgun_private = ${WF_TEST_MAILGUN}") {
		t.Errorf("saved config lost the reference:\n%s", got)
	}
	if strings.Contains(got, "mg-secret") {
		t.Errorf("saved config leaked the expanded secret:\n%s", got)
	}
}

// CreateConfig and --config write to a path that does not exist yet.
func TestSaveWritesWholeConfigWhenFileMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.ini")
	if err := Save(New(), p); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(b), "[app]") {
		t.Errorf("saved config has no [app] section:\n%s", b)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("mode = %v, want -rw-------", fi.Mode().Perm())
	}
}
