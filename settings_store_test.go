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
	"github.com/writefreely/writefreely/migrations"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/writefreely/writefreely/config"
)

// newSettingsTestApp is an App with a migrated database on the selected
// engine and a config.ini at cfgFile holding body (when body is not "").
func newSettingsTestApp(t *testing.T, body string) *App {
	t.Helper()
	// The database and config.ini sit in different directories, so a test
	// can make the ini's directory read-only without breaking SQLite.
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	cfg := config.New()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	cfg.App.Host = "https://blog.example"
	app := &App{cfg: cfg, cfgFile: filepath.Join(t.TempDir(), "config.ini")}
	openAppTestDB(t, app, "sqlite3", dbPath+"?parseTime=true&cached=shared")
	if body != "" {
		writeTestINI(t, app.cfgFile, body)
	}
	return app
}

func TestSettingsStoreEmpty(t *testing.T) {
	app := newSettingsTestApp(t, "")
	ctx := context.Background()
	ok, err := app.db.settingsTableExists(ctx)
	if err != nil || !ok {
		t.Fatalf("table exists %v err %v", ok, err)
	}
	rows, ver, err := app.db.LoadSettings(ctx)
	if err != nil || ver != 0 || len(rows) != 0 {
		t.Fatalf("rows %v ver %d err %v", rows, ver, err)
	}
}

func TestSettingsStoreSave(t *testing.T) {
	app := newSettingsTestApp(t, "")
	ctx := context.Background()
	v1, err := app.db.SaveSettings(ctx, map[string]string{"app.site_name": "One", "app.private": "true"})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := app.db.SaveSettings(ctx, map[string]string{"app.site_name": "Two"})
	if err != nil {
		t.Fatal(err)
	}
	if v2 != v1+1 {
		t.Errorf("versions %d then %d", v1, v2)
	}
	rows, ver, err := app.db.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ver != v2 || rows["app.site_name"] != "Two" || rows["app.private"] != "true" {
		t.Errorf("rows %v ver %d", rows, ver)
	}
	if got, _ := app.db.SettingsVersion(ctx); got != v2 {
		t.Errorf("SettingsVersion %d", got)
	}
}

func TestSettingsClaimOnce(t *testing.T) {
	app := newSettingsTestApp(t, "")
	ctx := context.Background()
	won, err := app.db.ClaimSettingsImport(ctx, map[string]string{"app.site_name": "First"})
	if err != nil || !won {
		t.Fatalf("first claim won=%v err=%v", won, err)
	}
	won, err = app.db.ClaimSettingsImport(ctx, map[string]string{"app.site_name": "Second"})
	if err != nil || won {
		t.Fatalf("second claim won=%v err=%v", won, err)
	}
	rows, ver, _ := app.db.LoadSettings(ctx)
	if rows["app.site_name"] != "First" || ver != 1 {
		t.Errorf("rows %v ver %d", rows, ver)
	}
}

func TestSettingsClaimReadBackMismatch(t *testing.T) {
	app := newSettingsTestApp(t, "")
	ctx := context.Background()
	orig := settingsReadBack
	settingsReadBack = func(m map[string]string) map[string]string {
		m["app.site_name"] = "mangled"
		return m
	}
	t.Cleanup(func() { settingsReadBack = orig })
	_, err := app.db.ClaimSettingsImport(ctx, map[string]string{"app.site_name": "Paisans"})
	if err == nil || !strings.Contains(err.Error(), "app.site_name") {
		t.Fatalf("err %v", err)
	}
	rows, ver, _ := app.db.LoadSettings(ctx)
	if ver != 0 || len(rows) != 0 {
		t.Errorf("claim not rolled back: rows %v ver %d", rows, ver)
	}
}

func TestSettingsClaimConcurrent(t *testing.T) {
	app := newSettingsTestApp(t, "")
	if app.db.driverName == driverSQLite {
		t.Skip("SQLite has one writer per file; two nodes never share one")
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := app.db.ClaimSettingsImport(ctx, map[string]string{"app.site_name": "x"})
			if err != nil {
				t.Error(err)
			}
			wins <- won
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d winners, want 1", n)
	}
}

func writeTestINI(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

// V21 must survive being run again over what an earlier, interrupted run
// left: MySQL commits each DDL statement as it goes, so a crash can leave
// the tables, with or without the version row, and no appmigrations entry.
func TestSettingsMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	run := func(a *App) {
		t.Helper()
		if _, err := a.db.ExecContext(ctx, "DELETE FROM appmigrations WHERE version >= 21"); err != nil {
			t.Fatal(err)
		}
		if err := migrations.Migrate(migrations.NewDatastore(a.db.DB, a.db.driverName)); err != nil {
			t.Fatalf("rerun: %v", err)
		}
	}
	a := newSettingsTestApp(t, "")
	v, err := a.db.SaveSettings(ctx, map[string]string{"app.site_name": "Kept"})
	if err != nil {
		t.Fatal(err)
	}
	run(a) // tables, row and data all present
	run(a)
	rows, ver, err := a.db.LoadSettings(ctx)
	if err != nil || ver != v || rows["app.site_name"] != "Kept" {
		t.Fatalf("after rerun: rows %v ver %d (want %d) err %v", rows, ver, v, err)
	}
	// Crash between CREATE TABLE and the INSERT: tables, no version row.
	if _, err := a.db.ExecContext(ctx, "DELETE FROM app_settings_version"); err != nil {
		t.Fatal(err)
	}
	run(a)
	if ver, err := a.db.SettingsVersion(ctx); err != nil || ver != 0 {
		t.Fatalf("version row after repair: %d %v", ver, err)
	}
}
