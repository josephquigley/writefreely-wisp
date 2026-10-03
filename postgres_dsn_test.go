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
	"testing"

	"github.com/lib/pq"
	"github.com/writefreely/writefreely/config"
)

// TestPostgresDSNRoundTrip feeds awkward values through postgresDSN and back
// out through lib/pq's own parser: whatever the config says must be exactly
// what the driver ends up using.
func TestPostgresDSNRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.DatabaseCfg
	}{
		{"plain", config.DatabaseCfg{Host: "localhost", Port: 5432, User: "wf", Password: "secret", Database: "writefreely"}},
		{"empty password", config.DatabaseCfg{Host: "localhost", Port: 5432, User: "wf", Password: "", Database: "writefreely"}},
		{"space in password", config.DatabaseCfg{Host: "localhost", Port: 5432, User: "wf", Password: "a b", Database: "writefreely"}},
		{"key injection", config.DatabaseCfg{Host: "localhost", Port: 5432, User: "wf", Password: "x sslmode=require", Database: "writefreely"}},
		{"single quote", config.DatabaseCfg{Host: "localhost", Port: 5432, User: "o'brien", Password: "it's", Database: "wf'db"}},
		{"backslash", config.DatabaseCfg{Host: "localhost", Port: 5432, User: `dom\user`, Password: `back\slash\`, Database: "writefreely"}},
		{"equals", config.DatabaseCfg{Host: "localhost", Port: 5432, User: "wf", Password: "a=b==", Database: "db=x"}},
		{"everything", config.DatabaseCfg{Host: "db host", Port: 5432, User: "w f", Password: ` '\ = '' \\ `, Database: "my db"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := postgresDSN(tc.cfg, "disable")
			got, err := pq.NewConfig(dsn)
			if err != nil {
				t.Fatalf("pq.NewConfig(%q): %v", dsn, err)
			}
			if got.Host != tc.cfg.Host {
				t.Errorf("host: got %q, want %q (dsn %q)", got.Host, tc.cfg.Host, dsn)
			}
			if int(got.Port) != tc.cfg.Port {
				t.Errorf("port: got %d, want %d (dsn %q)", got.Port, tc.cfg.Port, dsn)
			}
			if got.User != tc.cfg.User {
				t.Errorf("user: got %q, want %q (dsn %q)", got.User, tc.cfg.User, dsn)
			}
			if got.Password != tc.cfg.Password {
				t.Errorf("password: got %q, want %q (dsn %q)", got.Password, tc.cfg.Password, dsn)
			}
			if got.Database != tc.cfg.Database {
				t.Errorf("dbname: got %q, want %q (dsn %q)", got.Database, tc.cfg.Database, dsn)
			}
			if string(got.SSLMode) != "disable" {
				t.Errorf("sslmode: got %q, want %q (dsn %q)", got.SSLMode, "disable", dsn)
			}
		})
	}
}

// TestPostgresDSNConnect logs in as a role whose password needs quoting,
// using a connection string built by postgresDSN. Requires WF_TEST_PG_DSN.
func TestPostgresDSNConnect(t *testing.T) {
	adminDSN := postgresTestDSN(t)
	admin, err := sql.Open(driverPostgres, adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	adminCfg, err := pq.NewConfig(adminDSN)
	if err != nil {
		t.Fatalf("parse WF_TEST_PG_DSN: %v", err)
	}

	role := fmt.Sprintf("wf_dsn_%d", rand.Int63())
	password := `p a's\s w`
	if _, err := admin.Exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s", pq.QuoteIdentifier(role), pq.QuoteLiteral(password))); err != nil {
		t.Fatalf("create role: %v", err)
	}
	defer func() {
		if _, err := admin.Exec("DROP ROLE IF EXISTS " + pq.QuoteIdentifier(role)); err != nil {
			t.Logf("drop role %s: %v", role, err)
		}
	}()

	cfg := config.DatabaseCfg{
		Host:     adminCfg.Host,
		Port:     int(adminCfg.Port),
		User:     role,
		Password: password,
		Database: adminCfg.Database,
	}
	db, err := sql.Open(driverPostgres, postgresDSN(cfg, "disable"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("ping as %s: %v", role, err)
	}
}
