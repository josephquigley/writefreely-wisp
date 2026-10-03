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
