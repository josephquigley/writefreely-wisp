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

// Engine selection for the app-level tests (the inbox, announce,
// handle-resolution, signup, template-rendering and similar suites). Each of
// those used to open its own SQLite file. They now go through openAppTestDB
// or engineTestApp, which keep doing exactly that by default and switch to a
// fresh MySQL or Postgres database when WF_TEST_DB_TYPE asks for one, so the
// same tests give a signal on every engine. See docs/database-testing.md.

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/writefreely/writefreely/config"
)

// testDBEngine returns the engine WF_TEST_DB_TYPE selects: driverSQLite
// (unset, "sqlite" or "sqlite3"), driverMySQL or driverPostgres.
func testDBEngine() (string, error) {
	switch v := os.Getenv(envTestDBType); v {
	case "", "sqlite", driverSQLite:
		return driverSQLite, nil
	case driverMySQL, driverPostgres:
		return v, nil
	default:
		return "", fmt.Errorf("%s=%q: want sqlite, mysql or postgres", envTestDBType, v)
	}
}

// initTestDBEngine sets up the harness WF_TEST_DB_TYPE selects. TestMain
// calls it and exits non-zero on error: asking for an engine and not getting
// it must not look like a green run.
func initTestDBEngine() error {
	engine, err := testDBEngine()
	if err != nil {
		return err
	}
	switch engine {
	case driverPostgres:
		return initPostgresTests()
	case driverMySQL:
		return initMySQLHarness()
	}
	return nil
}

// engineTestApp returns an App over a fresh, schema-loaded MySQL or Postgres
// database when WF_TEST_DB_TYPE selects one, and nil on the default SQLite
// run, so the caller keeps its own SQLite setup. cfg may be nil.
func engineTestApp(t testing.TB, cfg *config.Config) *App {
	t.Helper()
	switch {
	case runPostgresTests():
		return newPostgresTestApp(t, cfg)
	case runMySQLHarnessTests():
		return newMySQLTestApp(t, cfg)
	}
	return nil
}

// openAppTestDB gives app a database with the full schema loaded and returns
// the underlying *sql.DB for fixtures that write to it directly.
//
// Under WF_TEST_DB_TYPE=mysql or postgres it is a fresh database from that
// engine's harness, app.cfg.Database.Type is set to match, and sqliteDriver
// and sqliteDSN are ignored. Otherwise it opens sqliteDSN through
// sqliteDriver, closes it when the test ends, and runs adminInitDatabase, as
// the per-file helpers did before.
//
// app.cfg must be set. app.db is replaced.
func openAppTestDB(t testing.TB, app *App, sqliteDriver, sqliteDSN string) *sql.DB {
	t.Helper()
	if e := engineTestApp(t, app.cfg); e != nil {
		app.db = e.db
		return app.db.DB
	}

	db, err := sql.Open(sqliteDriver, sqliteDSN)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	app.db = &datastore{DB: db, driverName: driverSQLite}
	if err := adminInitDatabase(app); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return db
}
