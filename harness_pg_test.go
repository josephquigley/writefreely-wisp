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

// The Postgres test harness. docs/postgres-testing.md is the user-facing
// description; this file is the mechanism.
//
// With WF_TEST_DB_TYPE=postgres and WF_TEST_PG_DSN pointing at a maintenance
// database, every test that asks for a Postgres database gets its own,
// named wf_test_<random>, created from the maintenance connection and dropped
// WITH (FORCE) when the test ends. Without those variables every helper here
// skips the calling test.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/writefreely/writefreely/config"
)

const (
	envTestDBType = "WF_TEST_DB_TYPE"
	envTestPGDSN  = "WF_TEST_PG_DSN"

	// testPGPrefix names every database the harness creates, so a leak is
	// easy to spot (\l wf_test_*) and to clean up by hand.
	testPGPrefix = "wf_test_"
)

// testPGAdmin is the maintenance connection opened by TestMain when
// runPostgresTests() is true. It is used only for CREATE/DROP DATABASE.
var testPGAdmin *sql.DB

// runPostgresTests reports whether the suite was asked to run against
// Postgres.
func runPostgresTests() bool {
	return os.Getenv(envTestDBType) == driverPostgres
}

// initPostgresTests opens and checks the maintenance connection. TestMain
// calls it when runPostgresTests() is true, and exits non-zero on error:
// asking for Postgres and not getting it must not look like a green run.
func initPostgresTests() error {
	dsn := os.Getenv(envTestPGDSN)
	if dsn == "" {
		return fmt.Errorf("%s=postgres but %s is not set", envTestDBType, envTestPGDSN)
	}
	if _, err := testPGDSN(dsn, "x"); err != nil {
		return err
	}
	db, err := sql.Open(driverPostgresRebind, dsn)
	if err != nil {
		return fmt.Errorf("open %s: %v", envTestPGDSN, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return fmt.Errorf("connect to %s: %v", envTestPGDSN, err)
	}
	db.SetMaxOpenConns(4)
	testPGAdmin = db
	return nil
}

// testPGDSN returns base with its database replaced by dbName and the
// session time zone pinned to UTC unless base already sets one, matching
// postgresDSN. Only URL-form DSNs (postgres://…) are accepted.
func testPGDSN(base, dbName string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("%s must be a postgres:// URL", envTestPGDSN)
	}
	u.Path = "/" + dbName
	u.RawPath = ""
	q := u.Query()
	if q.Get("timezone") == "" {
		q.Set("timezone", "UTC")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// newPostgresTestDB creates an empty database named wf_test_<random>, opens
// it through the postgres-rebind driver, and registers a cleanup that closes
// the pool and drops the database WITH (FORCE), so a leaked connection cannot
// wedge the drop. It skips the test unless WF_TEST_DB_TYPE=postgres.
//
// Use it for tests that build their own tables. Most tests want
// newPostgresTestApp or newPostgresTestDatastore, which also load the schema.
func newPostgresTestDB(t testing.TB) *sql.DB {
	t.Helper()
	if !runPostgresTests() {
		t.Skipf("skipping postgres test: %s is not %q", envTestDBType, driverPostgres)
	}
	if testPGAdmin == nil {
		t.Fatalf("postgres harness not initialised (TestMain did not run initPostgresTests)")
	}

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random database name: %v", err)
	}
	name := testPGPrefix + hex.EncodeToString(b)
	if _, err := testPGAdmin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := testPGAdmin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	dsn, err := testPGDSN(os.Getenv(envTestPGDSN), name)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverPostgresRebind, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	// Registered after the drop, so it runs before it (cleanups are LIFO).
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(10)
	if err := db.Ping(); err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	return db
}

// newPostgresTestApp returns an App whose db is a fresh Postgres database
// (see newPostgresTestDB) with the full schema loaded by adminInitDatabase,
// exactly as `writefreely db init` would. cfg may be nil for config.New();
// its Database.Type is set to postgres either way.
//
// While the Postgres schema is not ported (WFPG-03), adminInitDatabase panics
// with "not implemented for database driver"; the test is then skipped with a
// message saying so. Any other schema-load error, including one from a
// partial port, fails the test.
func newPostgresTestApp(t testing.TB, cfg *config.Config) *App {
	t.Helper()
	sdb := newPostgresTestDB(t)
	if cfg == nil {
		cfg = config.New()
	}
	cfg.Database.Type = driverPostgres
	app := &App{db: newDatastore(sdb, driverPostgres), cfg: cfg}

	unported, err := loadPostgresTestSchema(app)
	if unported != "" {
		t.Skipf("skipping postgres test: schema load is not ported yet (WFPG-03): %s", unported)
	}
	if err != nil {
		t.Fatalf("postgres schema load: %v", err)
	}
	return app
}

// newPostgresTestDatastore is newPostgresTestApp(t, nil).db: a *datastore
// with driverName postgres over a fresh, schema-loaded database.
func newPostgresTestDatastore(t testing.TB) *datastore {
	t.Helper()
	return newPostgresTestApp(t, nil).db
}

// loadPostgresTestSchema runs adminInitDatabase, turning an
// unsupportedDriver panic into a non-empty unported message.
func loadPostgresTestSchema(app *App) (unported string, err error) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			if !strings.Contains(msg, "not implemented for database driver") {
				panic(r)
			}
			unported = msg
		}
	}()
	return "", adminInitDatabase(app)
}

// TestPostgresHarness checks the harness itself: a test's database exists
// while it runs, is named wf_test_*, and is gone once the test has finished.
func TestPostgresHarness(t *testing.T) {
	if !runPostgresTests() {
		t.Skipf("skipping postgres test: %s is not %q", envTestDBType, driverPostgres)
	}
	exists := func(name string) bool {
		var n int
		if err := testPGAdmin.QueryRow("SELECT COUNT(*) FROM pg_database WHERE datname = ?", name).Scan(&n); err != nil {
			t.Fatalf("look up %s: %v", name, err)
		}
		return n == 1
	}

	var name string
	t.Run("inner", func(t *testing.T) {
		db := newPostgresTestDB(t)
		if err := db.QueryRow("SELECT current_database()").Scan(&name); err != nil {
			t.Fatalf("current_database: %v", err)
		}
		if !strings.HasPrefix(name, testPGPrefix) {
			t.Errorf("database %q is not named %s*", name, testPGPrefix)
		}
		if !exists(name) {
			t.Errorf("database %q not found while the test runs", name)
		}
		// A connection left open must not stop the drop.
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("hold a connection: %v", err)
		}
		_ = conn
	})
	if name == "" {
		t.Fatal("inner test did not report its database")
	}
	if exists(name) {
		t.Errorf("database %q still exists after its test finished", name)
	}
}

// TestPostgresSchemaLoads is the probe for the Postgres schema: it skips,
// naming WFPG-03, until adminInitDatabase supports Postgres, and from then
// on proves a full `db init` works against a real server.
func TestPostgresSchemaLoads(t *testing.T) {
	ds := newPostgresTestDatastore(t)
	for _, table := range []string{"users", "collections", "posts", "appmigrations"} {
		ok, err := ds.dialect.TableExists(context.Background(), ds.DB, table)
		if err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
		if !ok {
			t.Errorf("table %s missing after schema load", table)
		}
	}
}
