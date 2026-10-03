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
