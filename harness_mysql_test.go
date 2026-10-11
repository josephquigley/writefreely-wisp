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

// The MySQL/MariaDB test harness, the twin of harness_pg_test.go.
// docs/database-testing.md is the user-facing description.
//
// With WF_TEST_DB_TYPE=mysql and WF_TEST_MYSQL_DSN pointing at a server
// (user:password@tcp(host:port)/), every test that asks for a MySQL database
// gets its own, named wf_test_<random>, created from the admin connection and
// dropped when the test ends. Without those variables every helper here
// skips the calling test.
//
// This is separate from the older TEST_MYSQL mechanism (initMySQL,
// withTestDB's newTestDatabase), which copies tables from a reference
// database that must already exist. Tests gated on TEST_MYSQL also run under
// WF_TEST_DB_TYPE=mysql, against a harness database.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/writefreely/writefreely/config"
)

const envTestMySQLDSN = "WF_TEST_MYSQL_DSN"

// testMySQLAdmin is the admin connection opened by TestMain when
// runMySQLHarnessTests() is true. It is used only for CREATE/DROP DATABASE.
var testMySQLAdmin *sql.DB

// runMySQLHarnessTests reports whether the suite was asked to run against
// MySQL or MariaDB through this harness.
func runMySQLHarnessTests() bool {
	return os.Getenv(envTestDBType) == driverMySQL
}

// runAnyMySQLTests reports whether MySQL is available by either mechanism,
// the TEST_MYSQL reference database or this harness.
func runAnyMySQLTests() bool {
	return runMySQLTests() || runMySQLHarnessTests()
}

// testMySQLConfig parses WF_TEST_MYSQL_DSN and points it at dbName, with the
// connection parameters connectToDatabase uses (utf8mb4, parseTime, local
// time zone).
func testMySQLConfig(dbName string) (*mysql.Config, error) {
	dsn := os.Getenv(envTestMySQLDSN)
	if dsn == "" {
		return nil, fmt.Errorf("%s=mysql but %s is not set", envTestDBType, envTestMySQLDSN)
	}
	c, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", envTestMySQLDSN, err)
	}
	c.DBName = dbName
	c.ParseTime = true
	c.Loc = time.Local
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	c.Params["charset"] = "utf8mb4"
	return c, nil
}

// initMySQLHarness opens and checks the admin connection. TestMain calls it
// when runMySQLHarnessTests() is true, and exits non-zero on error.
func initMySQLHarness() error {
	c, err := testMySQLConfig("")
	if err != nil {
		return err
	}
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		return fmt.Errorf("open %s: %v", envTestMySQLDSN, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return fmt.Errorf("connect to %s: %v", envTestMySQLDSN, err)
	}
	db.SetMaxOpenConns(4)
	testMySQLAdmin = db
	return nil
}

// newMySQLTestDB creates an empty utf8mb4 database named wf_test_<random>,
// opens it, and registers a cleanup that closes the pool and drops the
// database. params are added to the connection's DSN parameters (for
// example clientFoundRows). It skips the test unless WF_TEST_DB_TYPE=mysql.
func newMySQLTestDB(t testing.TB, params map[string]string) *sql.DB {
	t.Helper()
	if !runMySQLHarnessTests() {
		t.Skipf("skipping mysql test: %s is not %q", envTestDBType, driverMySQL)
	}
	if testMySQLAdmin == nil {
		t.Fatalf("mysql harness not initialised (TestMain did not run initMySQLHarness)")
	}

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random database name: %v", err)
	}
	name := testPGPrefix + hex.EncodeToString(b)
	if _, err := testMySQLAdmin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := testMySQLAdmin.Exec("DROP DATABASE IF EXISTS " + name); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	c, err := testMySQLConfig(name)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range params {
		c.Params[k] = v
	}
	db, err := sql.Open("mysql", c.FormatDSN())
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

// newMySQLTestAppWith is newMySQLTestApp over a database opened with extra
// DSN parameters.
func newMySQLTestAppWith(t testing.TB, cfg *config.Config, params map[string]string) *App {
	t.Helper()
	sdb := newMySQLTestDB(t, params)
	if cfg == nil {
		cfg = config.New()
	}
	cfg.Database.Type = driverMySQL
	app := &App{db: newDatastore(sdb, driverMySQL), cfg: cfg}
	if err := adminInitDatabase(app); err != nil {
		t.Fatalf("mysql schema load: %v", err)
	}
	return app
}

// newMySQLTestApp returns an App whose db is a fresh MySQL database (see
// newMySQLTestDB) with the full schema loaded by adminInitDatabase, exactly
// as `writefreely db init` would. cfg may be nil for config.New(); its
// Database.Type is set to mysql either way.
func newMySQLTestApp(t testing.TB, cfg *config.Config) *App {
	t.Helper()
	return newMySQLTestAppWith(t, cfg, nil)
}

// newMySQLTestDatastore is newMySQLTestApp(t, nil).db.
func newMySQLTestDatastore(t testing.TB) *datastore {
	t.Helper()
	return newMySQLTestApp(t, nil).db
}

// TestMySQLHarness checks the harness itself: a test's database exists while
// it runs and is gone once the test has finished.
func TestMySQLHarness(t *testing.T) {
	if !runMySQLHarnessTests() {
		t.Skipf("skipping mysql test: %s is not %q", envTestDBType, driverMySQL)
	}
	exists := func(name string) bool {
		var n int
		if err := testMySQLAdmin.QueryRow("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", name).Scan(&n); err != nil {
			t.Fatalf("look up %s: %v", name, err)
		}
		return n == 1
	}

	var name string
	t.Run("inner", func(t *testing.T) {
		ds := newMySQLTestDatastore(t)
		if err := ds.QueryRow("SELECT DATABASE()").Scan(&name); err != nil {
			t.Fatalf("DATABASE(): %v", err)
		}
		if !exists(name) {
			t.Errorf("database %q not found while the test runs", name)
		}
		var n int
		if err := ds.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil {
			t.Errorf("schema not loaded: %v", err)
		}
	})
	if name == "" {
		t.Fatal("inner test did not report its database")
	}
	if exists(name) {
		t.Errorf("database %q still exists after its test finished", name)
	}
}
