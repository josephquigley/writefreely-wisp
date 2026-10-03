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
	"path/filepath"
	"testing"

	"github.com/writefreely/writefreely/config"
)

// TestSQLiteQuerySyntax runs the WFPG-04 suite (database_query_syntax_test.go)
// against SQLite with the real schema, so the Postgres port is held to the
// behaviour SQLite already has.
func TestSQLiteQuerySyntax(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	sdb, err := sql.Open("sqlite3_with_regex", dbPath+"?parseTime=true")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { sdb.Close() })

	cfg := config.New()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	cfg.App.Host = "http://localhost:0"
	db := newDatastore(sdb, driverSQLite)
	if err := adminInitDatabase(&App{db: db, cfg: cfg}); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	querySyntaxSuite(t, db)
}
