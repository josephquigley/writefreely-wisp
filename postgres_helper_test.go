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
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"testing"

	"github.com/writefreely/writefreely/config"
)

// Postgres-backed tests run only when WF_TEST_PG_DSN is set to a lib/pq
// key=value connection string for a role that may create databases, e.g.
//
//	WF_TEST_PG_DSN="host=localhost port=5432 user=postgres password=postgres dbname=postgres sslmode=disable"
//
// Each test gets a fresh database, built the way `writefreely db init` builds
// one (postgres.sql, then the migrations), and dropped when the test ends.

func postgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("WF_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("WF_TEST_PG_DSN not set; skipping Postgres test")
	}
	return dsn
}

// withPostgresTestApp runs fn with an App whose datastore points at a fresh,
// fully migrated Postgres database.
func withPostgresTestApp(t *testing.T, fn func(app *App)) {
	t.Helper()
	adminDSN := postgresTestDSN(t)

	admin, err := sql.Open(driverPostgres, adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	name := fmt.Sprintf("wf_test_%d", rand.Int63())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	defer func() {
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
	}()

	// lib/pq lets a later key override an earlier one.
	db, err := sql.Open(driverPostgres, adminDSN+" dbname="+name)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer db.Close()

	app := &App{
		cfg: &config.Config{},
		db:  &datastore{DB: db, driverName: driverPostgres},
	}
	app.cfg.Database.Type = driverPostgres
	if err := adminInitDatabase(app); err != nil {
		t.Fatalf("init test database: %v", err)
	}

	fn(app)
}
