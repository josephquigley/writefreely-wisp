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
	"reflect"
	"strings"
	"testing"

	"github.com/go-ini/ini"
)

const stripFixture = `; deployment notes stay
[server]
port = 8080

[database]
; password comes from the environment
password = ${WF_DB_PASSWORD}

[app]
host      = https://blog.example
; shown in the header
site_name = Paisans
private   = true

[uploads]
dir     = /data/uploads
enabled = true
`

func writeINI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKeysPresent(t *testing.T) {
	p := writeINI(t, stripFixture)
	got, err := KeysPresent(p, []string{"app.site_name", "app.theme", "uploads.enabled"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app.site_name", "uploads.enabled"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripKeys(t *testing.T) {
	p := writeINI(t, stripFixture)
	removed, err := StripKeys(p, []string{"app.site_name", "app.private", "uploads.enabled", "app.theme"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app.site_name", "app.private", "uploads.enabled"}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	b, _ := os.ReadFile(p)
	out := string(b)
	for _, gone := range []string{"site_name", "private", "enabled", "shown in the header"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q still present:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"deployment notes stay", "password comes from the environment", "host", "/data/uploads"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q lost:\n%s", kept, out)
		}
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestStripKeysKeepsEnvRefs(t *testing.T) {
	p := writeINI(t, stripFixture)
	if _, err := StripKeys(p, []string{"app.site_name"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "${WF_DB_PASSWORD}") {
		t.Errorf("env reference expanded or lost:\n%s", b)
	}
}

func TestStripKeysNothingToDo(t *testing.T) {
	p := writeINI(t, stripFixture)
	before, _ := os.ReadFile(p)
	removed, err := StripKeys(p, []string{"app.theme"})
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed %v err %v", removed, err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("file rewritten with nothing to strip")
	}
}

func TestStripKeysReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	p := writeINI(t, stripFixture)
	dir := filepath.Dir(p)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	before, _ := os.ReadFile(p)
	left, err := StripKeys(p, []string{"app.site_name"})
	if err == nil {
		t.Fatal("expected an error writing into a read-only directory")
	}
	if !reflect.DeepEqual(left, []string{"app.site_name"}) {
		t.Errorf("left %v", left)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("file changed despite the error")
	}
}

func TestStripKeysPreservesRemainingValues(t *testing.T) {
	// Fixture with tricky values that must survive the rewrite
	fixture := `[database]
password = ${WF_DB_PASSWORD}
user = "admin user "
query = value with # hash

[oauth.generic]
client_secret = "secret; with semicolon"
scope = "read write"
token_endpoint = https://oauth.example/token?param=value&other=stuff
custom_value = """triple quoted"""
backtick_value = ` + "`test`" + `

[app]
site_name = Paisans
`
	p := writeINI(t, fixture)

	// Load before stripping
	beforeFile, err := ini.Load(p)
	if err != nil {
		t.Fatalf("failed to load before: %v", err)
	}

	// Strip an unrelated key
	removed, err := StripKeys(p, []string{"app.site_name"})
	if err != nil {
		t.Fatalf("StripKeys failed: %v", err)
	}
	if !reflect.DeepEqual(removed, []string{"app.site_name"}) {
		t.Errorf("removed %v, want [app.site_name]", removed)
	}

	// Load after stripping
	afterFile, err := ini.Load(p)
	if err != nil {
		t.Fatalf("failed to load after: %v", err)
	}

	// Verify all remaining keys have identical values
	sections := []string{"database", "oauth.generic"}
	expectedKeys := map[string]map[string]bool{
		"database": {
			"password": true,
			"user":     true,
			"query":    true,
		},
		"oauth.generic": {
			"client_secret":  true,
			"scope":          true,
			"token_endpoint": true,
			"custom_value":   true,
			"backtick_value": true,
		},
	}

	for _, section := range sections {
		sec, _ := afterFile.GetSection(section)
		if sec == nil {
			t.Fatalf("section [%s] not found after strip", section)
		}
		for key := range expectedKeys[section] {
			beforeVal := beforeFile.Section(section).Key(key).Value()
			afterVal := sec.Key(key).Value()
			if beforeVal != afterVal {
				t.Errorf("[%s].%s changed: before %q, after %q", section, key, beforeVal, afterVal)
			}
		}
	}

	// Verify app.site_name is actually gone
	appSec, _ := afterFile.GetSection("app")
	if appSec != nil && appSec.HasKey("site_name") {
		t.Error("app.site_name still present after strip")
	}
}

func TestStripKeysFollowsSymlink(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real.ini")
	if err := os.WriteFile(real, []byte(stripFixture), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "config.ini")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := StripKeys(link, []string{"app.site_name"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v %v", fi, err)
	}
	b, _ := os.ReadFile(real)
	if strings.Contains(string(b), "site_name") || !strings.Contains(string(b), "private") {
		t.Errorf("target:\n%s", b)
	}
	// no temp files left in either directory
	for _, d := range []string{filepath.Dir(real), filepath.Dir(link)} {
		m, _ := filepath.Glob(filepath.Join(d, ".config.ini.*"))
		if len(m) != 0 {
			t.Errorf("leftover %v", m)
		}
	}
}

func TestMarkSettingsInDatabase(t *testing.T) {
	fname := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(fname, []byte(stripFixture), 0600); err != nil {
		t.Fatal(err)
	}
	if ok, _ := HasSettingsMarker(fname); ok {
		t.Fatal("marker before writing")
	}
	wrote, err := MarkSettingsInDatabase(fname)
	if err != nil || !wrote {
		t.Fatalf("wrote %v err %v", wrote, err)
	}
	b, _ := os.ReadFile(fname)
	s := string(b)
	want := "; Community settings live in the database. Do not remove this line; see docs/settings.md.\nsettings_location = database\n"
	if !strings.Contains(s, want) {
		t.Fatalf("marker missing:\n%s", s)
	}
	if strings.Index(s, "settings_location") > strings.Index(s, "[server]") {
		t.Errorf("marker not in the top-of-file section:\n%s", s)
	}
	if ok, err := HasSettingsMarker(fname); err != nil || !ok {
		t.Errorf("has marker %v %v", ok, err)
	}
	if wrote, err := MarkSettingsInDatabase(fname); err != nil || wrote {
		t.Errorf("second call wrote %v err %v", wrote, err)
	}
	t.Setenv("WF_DB_PASSWORD", "x")
	// An older binary maps the file onto its Config and ignores the key.
	if _, err := Load(fname); err != nil {
		t.Errorf("Load with marker: %v", err)
	}
	// Marker does not look like a DB-bound or bootstrap key.
	present, err := KeysPresent(fname, DBSettingNames())
	if err != nil || !reflect.DeepEqual(present, []string{"app.site_name", "app.private", "uploads.enabled"}) {
		t.Errorf("present %v err %v", present, err)
	}
	// And stripping keeps it.
	if _, err := StripKeys(fname, present); err != nil {
		t.Fatal(err)
	}
	if ok, _ := HasSettingsMarker(fname); !ok {
		t.Error("strip dropped the marker")
	}
}
