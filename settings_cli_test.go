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
	"github.com/writefreely/writefreely/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cliTestApp is a NewApp on a fresh SQLite file, set up as an operator
// would have it: a full ini, then `db init`.
func cliTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	ini := filepath.Join(dir, "config.ini")
	writeTestINI(t, ini, `[server]
port = 8080
[database]
type     = sqlite3
filename = `+filepath.Join(dir, "wf.db")+`
[app]
host      = https://blog.example
site_name = CLI Site
`)
	app := NewApp(ini)
	if err := CreateSchema(app); err != nil {
		t.Fatal(err)
	}
	return NewApp(ini)
}

func TestSettingsCLI(t *testing.T) {
	app := cliTestApp(t)
	row, err := SettingGet(app, "app.site_name")
	if err != nil || row.Value != "CLI Site" || row.Default {
		t.Fatalf("get %+v err %v (first use must import the ini)", row, err)
	}
	if err := SettingSet(NewApp(app.cfgFile), "app.private", "true"); err != nil {
		t.Fatal(err)
	}
	row, _ = SettingGet(NewApp(app.cfgFile), "app.private")
	if row.Value != "true" {
		t.Errorf("private %q", row.Value)
	}
	rows, err := SettingsList(NewApp(app.cfgFile))
	if err != nil || len(rows) == 0 {
		t.Fatalf("list %v err %v", rows, err)
	}
	out, err := SettingsExport(NewApp(app.cfgFile))
	if err != nil || !strings.Contains(out, "site_name") || !strings.Contains(out, "CLI Site") {
		t.Errorf("export %q err %v", out, err)
	}
}

func TestSettingsCLIRefusals(t *testing.T) {
	app := cliTestApp(t)
	if _, err := SettingGet(app, "app.host"); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("get bootstrap: %v", err)
	}
	if err := SettingSet(NewApp(app.cfgFile), "database.password", "x"); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("set bootstrap: %v", err)
	}
	if err := SettingSet(NewApp(app.cfgFile), "app.max_blogs", "lots"); err == nil {
		t.Error("invalid value accepted")
	}
}

func TestSettingsCLIBeforeMigrate(t *testing.T) {
	app := cliTestApp(t)
	app.LoadConfig()
	connectToDatabase(app)
	for _, tbl := range []string{"app_settings", "app_settings_version"} {
		if _, err := app.db.ExecContext(context.Background(), "DROP TABLE "+tbl); err != nil {
			t.Fatal(err)
		}
	}
	shutdown(app)
	_, err := SettingsList(NewApp(app.cfgFile))
	if err == nil || !strings.Contains(err.Error(), "db migrate") {
		t.Errorf("list: %v", err)
	}
	_, err = SettingGet(NewApp(app.cfgFile), "app.site_name")
	if err == nil || !strings.Contains(err.Error(), "db migrate") {
		t.Errorf("get: %v", err)
	}
}

func TestSettingsCLIDefaultMarker(t *testing.T) {
	app := cliTestApp(t)
	def := config.SettingDefaults()["app.site_name"]
	if err := SettingSet(NewApp(app.cfgFile), "app.site_name", def); err != nil {
		t.Fatal(err)
	}
	row, _ := SettingGet(NewApp(app.cfgFile), "app.site_name")
	if !row.Default {
		t.Errorf("saved default: %+v", row)
	}
	if err := SettingSet(NewApp(app.cfgFile), "app.site_name", "Other"); err != nil {
		t.Fatal(err)
	}
	row, _ = SettingGet(NewApp(app.cfgFile), "app.site_name")
	if row.Default || row.Value != "Other" {
		t.Errorf("saved other: %+v", row)
	}
}

// Read-only commands on a fresh install must not claim the import or
// touch config.ini; they show what config.ini says.
func TestSettingsCLIReadOnlyDoesNotWrite(t *testing.T) {
	app := cliTestApp(t)
	before, err := os.ReadFile(app.cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := SettingsList(NewApp(app.cfgFile))
	if err != nil || len(rows) == 0 {
		t.Fatalf("list %v err %v", rows, err)
	}
	for _, r := range rows {
		if r.Imported {
			t.Fatalf("%s marked imported on a fresh install", r.Name)
		}
	}
	row, err := SettingGet(NewApp(app.cfgFile), "app.site_name")
	if err != nil || row.Value != "CLI Site" || row.Imported {
		t.Fatalf("get %+v err %v", row, err)
	}
	out, err := SettingsExport(NewApp(app.cfgFile))
	if err != nil || !strings.Contains(out, "CLI Site") {
		t.Fatalf("export %q err %v", out, err)
	}
	after, _ := os.ReadFile(app.cfgFile)
	if string(before) != string(after) {
		t.Errorf("config.ini changed:\n%s", after)
	}
	chk := NewApp(app.cfgFile)
	chk.LoadConfig()
	connectToDatabase(chk)
	defer shutdown(chk)
	if v, err := chk.db.SettingsVersion(context.Background()); err != nil || v != 0 {
		t.Errorf("version %d err %v", v, err)
	}
}

// A restored backup: config.ini says the settings were moved, but the
// database is at version 0. The read-only commands must refuse as the
// import does, not show the stripped file's zero values.
func TestSettingsCLIReadOnlyRefusesMarkerAtVersionZero(t *testing.T) {
	app := cliTestApp(t)
	if _, err := config.MarkSettingsInDatabase(app.cfgFile); err != nil {
		t.Fatal(err)
	}
	if _, err := SettingsList(NewApp(app.cfgFile)); err == nil || err.Error() != emptyDatabaseRefusal {
		t.Errorf("list: %v", err)
	}
	if _, err := SettingGet(NewApp(app.cfgFile), "app.site_name"); err == nil || err.Error() != emptyDatabaseRefusal {
		t.Errorf("get: %v", err)
	}
	out, err := SettingsExport(NewApp(app.cfgFile))
	if err == nil || err.Error() != emptyDatabaseRefusal || out != "" {
		t.Errorf("export %q: %v", out, err)
	}
}

func TestSettingsCLISetRefusesEnvReference(t *testing.T) {
	app := cliTestApp(t)
	err := SettingSet(NewApp(app.cfgFile), "app.site_name", "${NAME}")
	if err == nil || !strings.Contains(err.Error(), "would be read as an environment reference") {
		t.Errorf("set: %v", err)
	}
}
