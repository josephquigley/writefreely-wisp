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

func writeLockedINI(t *testing.T, body string) (string, *Config) {
	t.Helper()
	fname := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(fname, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(fname)
	if err != nil {
		t.Fatal(err)
	}
	return fname, cfg
}

func TestLockedSettingsKeyIsBootstrap(t *testing.T) {
	if !IsBootstrap(LockedSettingsKey) {
		t.Errorf("%s must be bootstrap", LockedSettingsKey)
	}
	if !IsBootstrap(SettingsLocationKey) {
		t.Errorf("%s must be bootstrap", SettingsLocationKey)
	}
	if _, ok := LookupSetting(LockedSettingsKey); ok {
		t.Errorf("%s must not be a DB setting", LockedSettingsKey)
	}
}

func TestParseLockedNames(t *testing.T) {
	got, err := ParseLockedNames(" app.private ,, app.federation_allowlist,app.authorized_fetch , ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app.private", "app.federation_allowlist", "app.authorized_fetch"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, err := ParseLockedNames(""); err != nil || len(got) != 0 {
		t.Errorf("empty list: %v, %v", got, err)
	}
	if got, _ := ParseLockedNames("app.private, app.private"); len(got) != 1 {
		t.Errorf("duplicate kept: %v", got)
	}
}

func TestParseLockedNamesRefusals(t *testing.T) {
	for _, tc := range []struct{ list, want string }{
		{"app.private, app.privat", "app.privat"},
		{"App.Private", "App.Private"},
		{"app.host", "app.host"},
		{"database.password", "database.password"},
		{"locked_settings", "locked_settings"},
		{"settings_location", "settings_location"},
	} {
		_, err := ParseLockedNames(tc.list)
		if err == nil {
			t.Errorf("%q accepted", tc.list)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %q does not name %q", tc.list, err, tc.want)
		}
	}
}

func TestLockedSettingsValues(t *testing.T) {
	fname, cfg := writeLockedINI(t, `locked_settings = app.private, app.federation_allowlist, app.min_username_len, app.local_timeline, app.theme

[server]
port = 8080

[app]
host                 = https://blog.example
private              = true
federation_allowlist = a.example, *.b.example
`)
	got, err := LockedSettings(fname, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"app.private":              "true",
		"app.federation_allowlist": "a.example, *.b.example",
		// Absent from the file: the value the instance runs with without the
		// key, which is what an import would store. A zero that the registry
		// refuses becomes the default, exactly as the import does it.
		"app.min_username_len": "3",
		"app.local_timeline":   "true",
		"app.theme":            DefaultTheme,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestLockedSettingsNoneWhenAbsent(t *testing.T) {
	fname, cfg := writeLockedINI(t, "[app]\nhost = https://blog.example\nprivate = true\n")
	got, err := LockedSettings(fname, cfg)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestLockedSettingsRefusesInvalidValue(t *testing.T) {
	for _, body := range []string{
		"locked_settings = app.private\n[app]\nhost = https://blog.example\nprivate = maybe\n",
		"locked_settings = app.max_blogs\n[app]\nhost = https://blog.example\nmax_blogs = lots\n",
		"locked_settings = app.min_username_len\n[app]\nhost = https://blog.example\nmin_username_len = 0\n",
		"locked_settings = app.user_invites\n[app]\nhost = https://blog.example\nuser_invites = everyone\n",
	} {
		fname, cfg := writeLockedINI(t, body)
		_, err := LockedSettings(fname, cfg)
		if err == nil {
			t.Errorf("accepted:\n%s", body)
			continue
		}
		if !strings.Contains(err.Error(), "locked") {
			t.Errorf("error %q does not say the setting is locked", err)
		}
	}
}

func TestLockedSettingsRefusesUnknownName(t *testing.T) {
	fname, cfg := writeLockedINI(t, "locked_settings = app.private, app.nope\n[app]\nhost = https://blog.example\n")
	_, err := LockedSettings(fname, cfg)
	if err == nil || !strings.Contains(err.Error(), "app.nope") {
		t.Errorf("err %v", err)
	}
}

// A locked key is never stripped, and an unlocked one beside it still is.
func TestStripKeysLeavesLockedKeys(t *testing.T) {
	fname, cfg := writeLockedINI(t, "locked_settings = app.private\n[app]\nhost = https://blog.example\nprivate = true\nsite_name = X\n")
	locked, err := LockedSettings(fname, cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := UnlockedSettingNames(locked)
	for _, n := range names {
		if n == "app.private" {
			t.Fatal("UnlockedSettingNames kept a locked name")
		}
	}
	if len(names) != len(DBSettingNames())-1 {
		t.Errorf("%d unlocked names, want %d", len(names), len(DBSettingNames())-1)
	}
	stripped, err := StripKeys(fname, names)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stripped, []string{"app.site_name"}) {
		t.Errorf("stripped %v", stripped)
	}
	b, _ := os.ReadFile(fname)
	if !strings.Contains(string(b), "private") || !strings.Contains(string(b), "locked_settings") {
		t.Errorf("file after strip:\n%s", b)
	}
}

func TestOverlayLocked(t *testing.T) {
	rows := map[string]string{"app.private": "false", "app.site_name": "DB"}
	got := OverlayLocked(rows, map[string]string{"app.private": "true"})
	if got["app.private"] != "true" || got["app.site_name"] != "DB" {
		t.Errorf("overlay %v", got)
	}
	if rows["app.private"] != "false" {
		t.Error("overlay mutated its input")
	}
}
