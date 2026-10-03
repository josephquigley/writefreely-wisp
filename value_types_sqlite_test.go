//go:build sqlite

/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func newValueTypesSQLiteApp(t *testing.T) *App {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	db, err := sql.Open("sqlite3_with_regex", dbPath+"?parseTime=true&cached=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := valueTypesConfig()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	app := &App{db: newDatastore(db, driverSQLite), cfg: cfg}
	if err := adminInitDatabase(app); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	prepareValueTypesApp(t, app)
	return app
}

func TestValueTypesSQLite(t *testing.T) {
	runValueTypesSuite(t, newValueTypesSQLiteApp)
}

// TestLegacyTextAccessTokensSQLite covers SQLite databases written before
// WFPG-05, when tokens were bound as a Go string and so stored as TEXT. New
// tokens are BLOBs, and SQLite never considers a BLOB equal to TEXT, so the
// lookups must match both, exactly.
func TestLegacyTextAccessTokensSQLite(t *testing.T) {
	app := newValueTypesSQLiteApp(t)
	u := vtCreateUser(t, app, "legacyuser", "pass1234", "")

	// Not valid UTF-8, with a NUL and LIKE wildcards in it.
	legacy := []byte{0xff, 0x00, '%', '_', 0x80, 'a', 'B', 0xc3, 0x28, 1, 2, 3, 4, 5, 6, 7}
	if _, err := app.db.Exec("INSERT INTO accesstokens (token, user_id, one_time) VALUES (?, ?, 0)", string(legacy), u.ID); err != nil {
		t.Fatalf("insert legacy token: %v", err)
	}
	var typ string
	if err := app.db.QueryRow("SELECT typeof(token) FROM accesstokens").Scan(&typ); err != nil || typ != "text" {
		t.Fatalf("legacy token stored as %q (%v), want text", typ, err)
	}

	legacyHex := hex.EncodeToString(legacy)
	if got := app.db.GetUserID(legacyHex); got != u.ID {
		t.Errorf("GetUserID(legacy TEXT token) = %d, want %d", got, u.ID)
	}
	if name, err := app.db.GetUserNameFromToken(legacyHex); err != nil || name != "legacyuser" {
		t.Errorf("GetUserNameFromToken(legacy) = %q, %v", name, err)
	}
	if id, _, err := app.db.GetUserDataFromToken(legacyHex); err != nil || id != u.ID {
		t.Errorf("GetUserDataFromToken(legacy) = %d, %v", id, err)
	}

	// A token differing in one byte, or only in ASCII case, matches nothing.
	other := append([]byte{}, legacy...)
	other[6] = 'A'
	if got := app.db.GetUserID(hex.EncodeToString(other)); got != -1 {
		t.Errorf("GetUserID(case-variant token) = %d, want -1", got)
	}
	other = append([]byte{}, legacy...)
	other[15] = 8
	if err := app.db.DeleteToken(other); err == nil {
		t.Error("DeleteToken with a different token deleted a row")
	}

	// New tokens are BLOBs and log in alongside the legacy one.
	tok, err := app.db.GetAccessToken(u.ID)
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}
	if err := app.db.QueryRow("SELECT typeof(token) FROM accesstokens WHERE token != ?", string(legacy)).Scan(&typ); err != nil || typ != "blob" {
		t.Errorf("new token stored as %q (%v), want blob", typ, err)
	}
	if got := app.db.GetUserID(tok); got != u.ID {
		t.Errorf("GetUserID(new token) = %d, want %d", got, u.ID)
	}

	if err := app.db.DeleteToken(legacy); err != nil {
		t.Fatalf("DeleteToken(legacy): %v", err)
	}
	if got := app.db.GetUserID(legacyHex); got != -1 {
		t.Errorf("deleted legacy token still logs in as %d", got)
	}
}
