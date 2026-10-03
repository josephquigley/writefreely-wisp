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
	"sort"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/config"
	"github.com/writefreely/writefreely/migrations"
)

// TestSchemaParity checks that postgres.sql describes the same tables and
// columns as sqlite.sql plus every migration, so a table or column added by
// a migration but missed in postgres.sql (or the reverse) fails here. Names
// only: types legitimately differ between engines.
//
// It runs under `make test-postgres`, or with WF_TEST_PG_DSN set by hand; see
// docs/postgres-testing.md.
func TestSchemaParity(t *testing.T) {
	pg := newInitializedPostgresApp(t)
	lite := newInitializedSQLiteApp(t)

	pgCols := postgresColumns(t, pg.db.DB)
	liteCols := sqliteColumns(t, lite.db.DB)

	assert.Equal(t, sortedKeys(liteCols), sortedKeys(pgCols), "tables differ between postgres.sql and sqlite.sql + migrations")
	for table, want := range liteCols {
		got, ok := pgCols[table]
		if !ok {
			continue
		}
		assert.Equal(t, want, got, "columns of %s differ between postgres.sql and sqlite.sql + migrations", table)
	}
}

// TestPostgresInit checks what `db init` leaves behind on Postgres: the
// version, a no-op migrate, and the column types other code relies on.
func TestPostgresInit(t *testing.T) {
	app := newInitializedPostgresApp(t)
	db := app.db

	assert.True(t, db.DatabaseInitialized())

	var ver, rows int
	require.NoError(t, db.QueryRow("SELECT COALESCE(MAX(version), 0), COUNT(*) FROM appmigrations").Scan(&ver, &rows))
	assert.Equal(t, migrations.CurrentVer(), ver, "a fresh Postgres database is at the current version")
	assert.Equal(t, 1, rows, "init records one row; V1 to V18 never run on Postgres")

	// db migrate is a no-op.
	require.NoError(t, migrations.Migrate(migrations.NewDatastore(db.DB, driverPostgres)))
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM appmigrations").Scan(&rows))
	assert.Equal(t, 1, rows)

	types := map[string]string{}
	r, err := db.Query(`SELECT table_name || '.' || column_name, format_type(a.atttypid, a.atttypmod)
		FROM information_schema.columns c
		JOIN pg_attribute a ON a.attrelid = (quote_ident(c.table_schema) || '.' || quote_ident(c.table_name))::regclass AND a.attname = c.column_name
		WHERE c.table_schema = current_schema()`)
	require.NoError(t, err)
	defer r.Close()
	for r.Next() {
		var col, typ string
		require.NoError(t, r.Scan(&col, &typ))
		types[col] = typ
	}
	require.NoError(t, r.Err())

	for col, want := range map[string]string{
		"posts.id":                   "character varying(16)",
		"posts.rtl":                  "boolean",
		"posts.created":              "timestamp with time zone",
		"posts.privacy":              "smallint",
		"posts.pinned_position":      "smallint",
		"collections.privacy":        "smallint",
		"users.id":                   "integer",
		"users.password":             "character varying(60)",
		"users.email":                "bytea",
		"accesstokens.token":         "bytea",
		"accesstokens.sudo":          "boolean",
		"remote_likes.post_id":       "character varying(16)",
		"post_images.post_id":        "character varying(16)",
		"oauth_users.access_token":   "text",
		"collectionattributes.value": "text",
		"remoteusers.actor_id":       "text",
		"remoteusers.inbox":          "text",
		"remoteusers.shared_inbox":   "text",
		"remoteusers.url":            "text",
		"remoteusers.handle":         "text",
		"publishjobs.delay":          "smallint",
		"appmigrations.migrated":     "timestamp with time zone",
	} {
		assert.Equal(t, want, types[col], col)
	}
	for col, typ := range types {
		assert.NotEqual(t, "character", strings.SplitN(typ, "(", 2)[0], "%s is char(n), which Postgres pads with spaces", col)
	}

	// Every serial key is an identity column that accepts explicit values,
	// so a data copy can insert ids and then setval.
	for _, table := range []string{"collections", "users", "remoteusers", "publishjobs"} {
		var identity string
		require.NoError(t, db.QueryRow("SELECT is_identity || ':' || COALESCE(identity_generation, '') FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'id'", table).Scan(&identity))
		assert.Equal(t, "YES:BY DEFAULT", identity, table)
	}
}

// TestPostgresInitIsAtomic breaks the last statement of postgres.sql and
// checks that init fails and leaves nothing behind.
func TestPostgresInitIsAtomic(t *testing.T) {
	db := newEmptyPostgresDatastore(t)
	cfg := config.New()
	cfg.Database.Type = driverPostgres
	app := &App{cfg: cfg, db: db}

	orig := postgresSql
	postgresSql = orig + "\nCREATE TABLE wfpg_broken (x no_such_type);\n"
	defer func() { postgresSql = orig }()

	err := adminInitDatabase(app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wfpg_broken")
	assert.False(t, db.DatabaseInitialized(), "a failed init must not leave tables behind")
}

// TestMigrateRefusesPostgresBeforeBase checks that migrations V1 to V18
// cannot run on Postgres, whether appmigrations is missing or behind.
func TestMigrateRefusesPostgresBeforeBase(t *testing.T) {
	db := newEmptyPostgresDatastore(t)
	mdb := migrations.NewDatastore(db.DB, driverPostgres)

	err := migrations.Migrate(mdb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db init")

	_, err = db.Exec("CREATE TABLE appmigrations (version integer NOT NULL, migrated timestamptz NOT NULL, result text NOT NULL)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO appmigrations VALUES (1, NOW(), '')")
	require.NoError(t, err)
	err = migrations.Migrate(mdb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start at V18")
}

func newInitializedPostgresApp(t *testing.T) *App {
	t.Helper()
	cfg := config.New()
	cfg.Database.Type = driverPostgres
	app := &App{cfg: cfg, db: newEmptyPostgresDatastore(t)}
	require.NoError(t, adminInitDatabase(app))
	return app
}

func newInitializedSQLiteApp(t *testing.T) *App {
	t.Helper()
	// A file, not :memory:, so that every pooled connection sees one database.
	sdb, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "parity.db"))
	require.NoError(t, err)
	t.Cleanup(func() { sdb.Close() })
	cfg := config.New()
	cfg.Database.Type = driverSQLite
	app := &App{cfg: cfg, db: newDatastore(sdb, driverSQLite)}
	require.NoError(t, adminInitDatabase(app))
	return app
}

// newEmptyPostgresDatastore returns a datastore on a fresh, empty database
// from the WFPG-02 harness (harness_pg_test.go).
func newEmptyPostgresDatastore(t *testing.T) *datastore {
	t.Helper()
	return newDatastore(newPostgresTestDB(t), driverPostgres)
}

func postgresColumns(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query("SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema()")
	require.NoError(t, err)
	return collectColumns(t, rows)
}

func sqliteColumns(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query("SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'")
	require.NoError(t, err)
	return collectColumns(t, rows)
}

func collectColumns(t *testing.T, rows *sql.Rows) map[string][]string {
	t.Helper()
	defer rows.Close()
	cols := map[string][]string{}
	for rows.Next() {
		var table, col string
		require.NoError(t, rows.Scan(&table, &col))
		cols[table] = append(cols[table], col)
	}
	require.NoError(t, rows.Err())
	for _, c := range cols {
		sort.Strings(c)
	}
	return cols
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
