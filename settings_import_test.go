//go:build sqlite

/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/writefreely/writefreely/config"
)

const importFixture = `[server]
port = 8080

[database]
type     = sqlite3
filename = writefreely.db

[app]
host      = https://blog.example
; header text
site_name = Paisans
private   = true
max_blogs = 3
`

// loadINIInto gives app the bootstrap Config that config.Load produces
// from its ini, which is what an upgraded node starts with.
func loadINIInto(t *testing.T, app *App) {
	t.Helper()
	cfg, err := config.Load(app.cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = app.cfg.Database // keep the test database
	app.cfg = cfg
}

func TestImportStoresEffectiveValues(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	res, err := a.importSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Imported {
		t.Fatal("first node did not import")
	}
	rows, _, _ := a.db.LoadSettings(context.Background())
	want := map[string]string{
		"app.site_name":  "Paisans",
		"app.private":    "true",
		"app.max_blogs":  "3",
		"app.federation": "false", // absent from the ini: the old load gave false, not New()'s true
	}
	for k, v := range want {
		if rows[k] != v {
			t.Errorf("%s = %q, want %q", k, rows[k], v)
		}
	}
	if len(rows) != len(config.DBSettingNames()) {
		t.Errorf("imported %d rows, want every setting (%d)", len(rows), len(config.DBSettingNames()))
	}
}

func TestImportStripsINI(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	res, err := a.importSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app.site_name", "app.max_blogs", "app.private"}; !reflect.DeepEqual(res.Stripped, want) {
		t.Errorf("stripped %v, want %v", res.Stripped, want)
	}
	b, _ := os.ReadFile(a.cfgFile)
	if strings.Contains(string(b), "site_name") || !strings.Contains(string(b), "host") {
		t.Errorf("ini after strip:\n%s", b)
	}
}

func TestImportRerunIsNoOp(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	ctx := context.Background()
	a.importSettings(ctx)
	_, v1, _ := a.db.LoadSettings(ctx)
	res, err := a.importSettings(ctx)
	if err != nil || res.Imported || len(res.Stripped) != 0 {
		t.Fatalf("rerun res %+v err %v", res, err)
	}
	_, v2, _ := a.db.LoadSettings(ctx)
	if v1 != v2 {
		t.Errorf("version moved %d -> %d", v1, v2)
	}
}

func TestImportLoserReportsDrift(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	ctx := context.Background()
	if _, err := a.importSettings(ctx); err != nil {
		t.Fatal(err)
	}
	// Node B has its own ini that disagreed with A's before the upgrade.
	bFile := filepath.Join(t.TempDir(), "config.ini")
	writeTestINI(t, bFile, strings.Replace(importFixture, "site_name = Paisans", "site_name = Other", 1))
	b := &App{cfgFile: bFile, db: a.db}
	cfg, _ := config.Load(bFile)
	b.cfg = cfg
	res, err := b.importSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported {
		t.Error("second node imported")
	}
	if !reflect.DeepEqual(res.Drift, []string{"app.site_name"}) {
		t.Errorf("drift %v", res.Drift)
	}
	rows, _, _ := a.db.LoadSettings(ctx)
	if rows["app.site_name"] != "Paisans" {
		t.Errorf("loser overwrote: %q", rows["app.site_name"])
	}
	bb, _ := os.ReadFile(bFile)
	if strings.Contains(string(bb), "site_name") {
		t.Error("loser did not strip its own ini")
	}
}

func TestImportReadOnlyINIWarns(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	dir := filepath.Dir(a.cfgFile)
	os.Chmod(dir, 0500)
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	res, err := a.importSettings(context.Background())
	if err != nil {
		t.Fatalf("a read-only ini must not be fatal: %v", err)
	}
	if !res.Imported || res.StripErr == nil || len(res.Left) != 3 {
		t.Errorf("res %+v", res)
	}
	// The stale keys stay in the file; the database still wins.
	a.cfg.App.SiteName = "stale"
	a.db.SaveSettings(context.Background(), map[string]string{"app.site_name": "Fresh"})
	a.loadSettings(context.Background())
	if a.Config().App.SiteName != "Fresh" {
		t.Error("stale ini value won")
	}
}

func TestImportExpandsEnvRefs(t *testing.T) {
	t.Setenv("WF_TEST_SITE", "From Env")
	a := newSettingsTestApp(t, strings.Replace(importFixture, "site_name = Paisans", "site_name = ${WF_TEST_SITE}", 1))
	loadINIInto(t, a)
	if _, err := a.importSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := a.db.LoadSettings(context.Background())
	if rows["app.site_name"] != "From Env" {
		t.Errorf("site_name %q", rows["app.site_name"])
	}
	b, _ := os.ReadFile(a.cfgFile)
	if strings.Contains(string(b), "WF_TEST_SITE") {
		t.Error("DB-bound env reference not stripped")
	}
}

func TestImportMismatchLeavesINI(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	orig := settingsReadBack
	settingsReadBack = func(m map[string]string) map[string]string { m["app.private"] = "false"; return m }
	t.Cleanup(func() { settingsReadBack = orig })
	before, _ := os.ReadFile(a.cfgFile)
	if _, err := a.importSettings(context.Background()); err == nil {
		t.Fatal("mismatch did not fail")
	}
	after, _ := os.ReadFile(a.cfgFile)
	if string(before) != string(after) {
		t.Error("ini changed after a failed import")
	}
}

func TestImportNormalisesInvalidZeroValues(t *testing.T) {
	// importFixture omits min_username_len and [uploads] max_size_mb, so
	// the loaded Config holds zeros the registry rejects.
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	ctx := context.Background()
	if _, err := a.importSettings(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := a.db.LoadSettings(ctx)
	if rows["app.min_username_len"] != "3" || rows["uploads.max_size_mb"] != "10" {
		t.Errorf("min_username_len %q, max_size_mb %q", rows["app.min_username_len"], rows["uploads.max_size_mb"])
	}
	if _, warns := config.ApplySettings(a.cfg, rows); len(warns) != 0 {
		t.Errorf("imported rows draw warnings: %v", warns)
	}
	// A second node with the same omissions must not see drift.
	bFile := filepath.Join(t.TempDir(), "config.ini")
	writeTestINI(t, bFile, importFixture)
	cfg, _ := config.Load(bFile)
	b := &App{cfgFile: bFile, db: a.db, cfg: cfg}
	res, err := b.importSettings(ctx)
	if err != nil || len(res.Drift) != 0 {
		t.Errorf("drift %v err %v", res.Drift, err)
	}
}

func TestImportWritesMarker(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	if _, err := a.importSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, err := config.HasSettingsMarker(a.cfgFile); err != nil || !ok {
		t.Errorf("marker after import: %v %v", ok, err)
	}
}

// An ini stripped by an earlier build, or rewritten by `config start`,
// gets the marker on the next start because the database holds settings.
func TestImportAddsMarkerWhenDBHoldsSettings(t *testing.T) {
	a := newSettingsTestApp(t, "[app]\nhost = https://blog.example\n")
	loadINIInto(t, a)
	ctx := context.Background()
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "X"})
	res, err := a.importSettings(ctx)
	if err != nil || res.Imported {
		t.Fatalf("res %+v err %v", res, err)
	}
	if ok, _ := config.HasSettingsMarker(a.cfgFile); !ok {
		t.Error("marker not added")
	}
}

func TestImportRefusesMarkerWithEmptyDatabase(t *testing.T) {
	a := newSettingsTestApp(t, "settings_location = database\n"+importFixture)
	loadINIInto(t, a)
	ctx := context.Background()
	before, _ := os.ReadFile(a.cfgFile)
	res, err := a.importSettings(ctx)
	if err == nil || res.Imported || !strings.Contains(err.Error(), "restored from a backup") {
		t.Fatalf("res %+v err %v", res, err)
	}
	if _, ver, _ := a.db.LoadSettings(ctx); ver != 0 {
		t.Errorf("version %d, want 0", ver)
	}
	after, _ := os.ReadFile(a.cfgFile)
	if string(before) != string(after) {
		t.Error("config.ini was changed by a refused import")
	}
}

func TestImportMarkerAbsentVersionZeroImports(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	res, err := a.importSettings(context.Background())
	if err != nil || !res.Imported {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestImportMarkerWithPopulatedDatabaseIsNormal(t *testing.T) {
	a := newSettingsTestApp(t, "settings_location = database\n[app]\nhost = https://blog.example\n")
	loadINIInto(t, a)
	ctx := context.Background()
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "X"})
	res, err := a.importSettings(ctx)
	if err != nil || res.Imported {
		t.Fatalf("res %+v err %v", res, err)
	}
}

// A read-only ini cannot take the marker; the refusal still applies when
// it already has one, and a populated database still starts.
func TestImportReadOnlyINIWithMarker(t *testing.T) {
	a := newSettingsTestApp(t, "settings_location = database\n[app]\nhost = https://blog.example\nsite_name = Old\n")
	loadINIInto(t, a)
	dir := filepath.Dir(a.cfgFile)
	os.Chmod(dir, 0555)
	defer os.Chmod(dir, 0755)
	ctx := context.Background()
	if _, err := a.importSettings(ctx); err == nil {
		t.Fatal("empty database accepted with marker on a read-only ini")
	}
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "X"})
	res, err := a.importSettings(ctx)
	if err != nil || res.StripErr == nil || len(res.Left) == 0 {
		t.Errorf("res %+v err %v", res, err)
	}
}

func TestConfigStartNote(t *testing.T) {
	if n := configStartNote(settingsImport{Imported: true, Stripped: []string{"app.site_name"}}); n != "" {
		t.Errorf("note after a real import: %q", n)
	}
	if n := configStartNote(settingsImport{}); n != "" {
		t.Errorf("note with nothing to say: %q", n)
	}
	n := configStartNote(settingsImport{Stripped: []string{"app.site_name", "app.private"}, Drift: []string{"app.private"}})
	want := "The database already holds this instance's settings; the answers just given for app.site_name, app.private were not applied. Change them with `writefreely settings set` or the admin page."
	if n != want {
		t.Errorf("note %q", n)
	}
}

// A malformed allowlist in the ini must stop the import, as it stopped
// startup before; normalising it would silently drop the operator's
// allowlist and cut the instance off from its peers.
func TestImportRefusesMalformedAllowlist(t *testing.T) {
	a := newSettingsTestApp(t, "[app]\nhost = https://blog.example\nprivate = true\nfederation_allowlist = a.*.b\n")
	loadINIInto(t, a)
	ctx := context.Background()
	if _, err := a.importSettings(ctx); err == nil || !strings.Contains(err.Error(), "federation_allowlist") {
		t.Fatalf("err %v", err)
	}
	if _, ver, _ := a.db.LoadSettings(ctx); ver != 0 {
		t.Errorf("version %d", ver)
	}
}

// A value that is exactly ${NAME} is invalid like any other: the import
// stores the default and moves on. The allowlist is the exception; dropping
// it would cut the instance off from its peers, so it stops the import.
func TestImportNormalisesEnvReference(t *testing.T) {
	a := newSettingsTestApp(t, "[app]\nhost = https://blog.example\n")
	a.cfg.App.SiteName = "${NAME}"
	ctx := context.Background()
	if _, err := a.importSettings(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := a.db.LoadSettings(ctx)
	if rows["app.site_name"] != config.SettingDefaults()["app.site_name"] {
		t.Errorf("site_name stored as %q", rows["app.site_name"])
	}
}

func TestImportRefusesEnvReferenceAllowlist(t *testing.T) {
	a := newSettingsTestApp(t, "[app]\nhost = https://blog.example\n")
	a.cfg.App.Private = true
	a.cfg.App.FederationAllowlist = "${PEERS}"
	ctx := context.Background()
	if _, err := a.importSettings(ctx); err == nil || !strings.Contains(err.Error(), "federation_allowlist") {
		t.Fatalf("err %v", err)
	}
	if _, ver, _ := a.db.LoadSettings(ctx); ver != 0 {
		t.Errorf("version %d", ver)
	}
}
