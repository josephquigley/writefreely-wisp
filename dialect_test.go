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
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/config"
)

func TestRebindPostgres(t *testing.T) {
	twelveIn := "INSERT INTO t VALUES (" + strings.TrimSuffix(strings.Repeat("?, ", 12), ", ") + ")"
	var twelveOut []string
	for i := 1; i <= 12; i++ {
		twelveOut = append(twelveOut, fmt.Sprintf("$%d", i))
	}

	tests := []struct {
		name, in, want string
	}{
		{"no parameters", "SELECT 1", "SELECT 1"},
		{"no parameters, with $1 already", "SELECT $1", "SELECT $1"},
		{"one parameter", "SELECT id FROM users WHERE username = ?", "SELECT id FROM users WHERE username = $1"},
		{"twelve parameters", twelveIn, "INSERT INTO t VALUES (" + strings.Join(twelveOut, ", ") + ")"},
		{"adjacent", "VALUES (?,?)", "VALUES ($1,$2)"},
		{"in single quotes", "SELECT '?' , ?", "SELECT '?' , $1"},
		{"in double quotes", `SELECT "wh?t" FROM t WHERE a = ?`, `SELECT "wh?t" FROM t WHERE a = $1`},
		{"doubled double quote", `SELECT "a""?" , ?`, `SELECT "a""?" , $1`},
		{"in line comment", "SELECT ? -- why?\nFROM t WHERE b = ?", "SELECT $1 -- why?\nFROM t WHERE b = $2"},
		{"line comment at end", "SELECT ? -- what?", "SELECT $1 -- what?"},
		{"escaped quote in literal", "SELECT 'it''s ? here', ?", "SELECT 'it''s ? here', $1"},
		{"literal ending in escaped quote", "SELECT '?''', ?", "SELECT '?''', $1"},
		{"E-string backslash escape", `SELECT E'\'?', ?`, `SELECT E'\'?', $1`},
		{"backslash in standard string", `SELECT '\', ?`, `SELECT '\', $1`},
		{"block comment", "SELECT /* ? /* nested ? */ ? */ ?", "SELECT /* ? /* nested ? */ ? */ $1"},
		{"dollar quoted", "SELECT $$ ? $$, ?", "SELECT $$ ? $$, $1"},
		{"tagged dollar quoted", "SELECT $x$ ? $$ ? $x$, ?", "SELECT $x$ ? $$ ? $x$, $1"},
		{"unterminated literal", "SELECT ?, 'oops ?", "SELECT $1, 'oops ?"},
		{"multibyte text", "SELECT 'ünï ?', ? -- ✓", "SELECT 'ünï ?', $1 -- ✓"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, rebindPostgres(tt.in))
		})
	}
}

func TestDialectFor(t *testing.T) {
	assert.Equal(t, driverMySQL, dialectFor(driverMySQL).DriverName())
	assert.Equal(t, driverSQLite, dialectFor(driverSQLite).DriverName())
	assert.Equal(t, driverPostgres, dialectFor(driverPostgres).DriverName())

	assert.PanicsWithValue(t,
		`dialectFor: unsupported database driver "oracle" (want "mysql", "sqlite3" or "postgres")`,
		func() { dialectFor("oracle") })
	assert.Panics(t, func() { newDatastore(nil, "") })
	assert.Panics(t, func() { (&datastore{driverName: "oracle"}).now() })
	assert.PanicsWithValue(t, `CreatePost: not implemented for database driver "oracle"`,
		func() { unsupportedDriver("CreatePost", "oracle") })
}

// TestDialectHelpersUnchanged pins the MySQL and SQLite fragments to what
// the datastore helpers returned before they moved into dialect.go.
func TestDialectHelpersUnchanged(t *testing.T) {
	my := &datastore{driverName: driverMySQL}
	assert.Equal(t, "NOW()", my.now())
	assert.Equal(t, "LEFT(content, 80)", my.clip("content", 80))
	assert.Equal(t, "ON DUPLICATE KEY UPDATE", my.upsert("id"))
	assert.Equal(t, "DATE_ADD(NOW(), INTERVAL 5 SECOND)", my.dateAdd(5, "SECOND"))
	assert.Equal(t, "DATE_SUB(NOW(), INTERVAL 3 HOUR)", my.dateSub(3, "HOUR"))

	lite := &datastore{driverName: driverSQLite}
	assert.Equal(t, "strftime('%Y-%m-%d %H:%M:%S','now')", lite.now())
	assert.Equal(t, "SUBSTR(content, 0, 80)", lite.clip("content", 80))
	assert.Equal(t, "ON CONFLICT(collection_id, attribute) DO UPDATE SET", lite.upsert("collection_id", "attribute"))
	assert.Equal(t, "DATETIME('now', '-24 HOUR')", lite.dateAdd(-24, "HOUR"))
	assert.Equal(t, "DATETIME('now', '-6 MONTH')", lite.dateSub(6, "MONTH"))
}

func TestPostgresDialectFragments(t *testing.T) {
	pg := newDatastore(nil, driverPostgres)
	assert.Equal(t, "NOW()", pg.now())
	assert.Equal(t, "LEFT(content, 80)", pg.clip("content", 80))
	assert.Equal(t, "ON CONFLICT (collection_id, attribute) DO UPDATE SET", pg.upsert("collection_id", "attribute"))
	assert.Equal(t, "NOW() + INTERVAL '5 SECOND'", pg.dateAdd(5, "SECOND"))
	assert.Equal(t, "NOW() + INTERVAL '-24 HOUR'", pg.dateAdd(-24, "HOUR"))
	assert.Equal(t, "NOW() - INTERVAL '6 MONTH'", pg.dateSub(6, "month"))
	assert.Equal(t, "SELECT $1, $2", pg.dialect.Rebind("SELECT ?, ?"))

	assert.Panics(t, func() { pg.upsert() })
	assert.PanicsWithValue(t, `postgresDialect.DateAdd: interval unit "HOUR'; DROP TABLE users; --" is not allowed`,
		func() { pg.dateAdd(1, "HOUR'; DROP TABLE users; --") })
	assert.Panics(t, func() { pg.dateSub(1, "fortnight") })
}

func TestPostgresDSN(t *testing.T) {
	dsn := postgresDSN(config.DatabaseCfg{
		Type:     "postgres",
		User:     "wf",
		Password: "p@ss/w:rd?#",
		Database: "writefreely",
		Host:     "db.internal",
		Port:     6543,
		TLS:      true,
	})
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	assert.Equal(t, "postgres", u.Scheme)
	assert.Equal(t, "db.internal:6543", u.Host)
	assert.Equal(t, "/writefreely", u.Path)
	pw, _ := u.User.Password()
	assert.Equal(t, "wf", u.User.Username())
	assert.Equal(t, "p@ss/w:rd?#", pw)
	assert.Equal(t, "require", u.Query().Get("sslmode"))
	assert.Equal(t, "UTC", u.Query().Get("timezone"))

	// pgx must read it back the same way.
	pc, err := pgconn.ParseConfig(dsn)
	require.NoError(t, err)
	assert.Equal(t, "p@ss/w:rd?#", pc.Password)
	assert.Equal(t, "UTC", pc.RuntimeParams["timezone"])

	dsn = postgresDSN(config.DatabaseCfg{Host: "::1", Database: "wf"})
	u, err = url.Parse(dsn)
	require.NoError(t, err)
	assert.Equal(t, "[::1]:5432", u.Host)
	assert.Equal(t, "disable", u.Query().Get("sslmode"))
	assert.Nil(t, u.User)
}

// TestRebindConnForwards checks, against the pgx version in go.mod, that
// rebindConn implements every optional database/sql/driver connection
// interface that *stdlib.Conn implements. If a pgx upgrade adds one, this
// fails until rebindConn forwards it too.
func TestRebindConnForwards(t *testing.T) {
	ifaces := map[string]reflect.Type{
		"Execer":             reflect.TypeOf((*driver.Execer)(nil)).Elem(), //nolint:staticcheck
		"ExecerContext":      reflect.TypeOf((*driver.ExecerContext)(nil)).Elem(),
		"Queryer":            reflect.TypeOf((*driver.Queryer)(nil)).Elem(), //nolint:staticcheck
		"QueryerContext":     reflect.TypeOf((*driver.QueryerContext)(nil)).Elem(),
		"ConnPrepareContext": reflect.TypeOf((*driver.ConnPrepareContext)(nil)).Elem(),
		"ConnBeginTx":        reflect.TypeOf((*driver.ConnBeginTx)(nil)).Elem(),
		"NamedValueChecker":  reflect.TypeOf((*driver.NamedValueChecker)(nil)).Elem(),
		"Pinger":             reflect.TypeOf((*driver.Pinger)(nil)).Elem(),
		"SessionResetter":    reflect.TypeOf((*driver.SessionResetter)(nil)).Elem(),
		"Validator":          reflect.TypeOf((*driver.Validator)(nil)).Elem(),
	}
	inner := reflect.TypeOf((*stdlib.Conn)(nil))
	outer := reflect.TypeOf((*rebindConn)(nil))
	for name, it := range ifaces {
		if inner.Implements(it) {
			assert.Truef(t, outer.Implements(it), "*stdlib.Conn implements driver.%s but rebindConn does not forward it", name)
		}
	}
}

func TestPostgresErrorClassifiers(t *testing.T) {
	pg := newDatastore(nil, driverPostgres)
	dup := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505"})
	load := &pgconn.PgError{Code: "53300"}
	other := &pgconn.PgError{Code: "42P01"}

	assert.True(t, pg.isDuplicateKeyErr(dup))
	assert.False(t, pg.isDuplicateKeyErr(other))
	assert.False(t, pg.isDuplicateKeyErr(sql.ErrNoRows))
	assert.True(t, pg.isHighLoadError(load))
	assert.False(t, pg.isHighLoadError(dup))
	assert.False(t, pg.isIgnorableError(dup))
	assert.False(t, pg.isIgnorableError(other))
}

// TestPostgresConnection exercises the driver against a real server. It runs
// only when WF_TEST_POSTGRES_DSN is set, e.g.
//
//	docker run --rm -d --name wf-pg -e POSTGRES_PASSWORD=wf -p 55432:5432 postgres:18
//	WF_TEST_POSTGRES_DSN='postgres://postgres:wf@localhost:55432/postgres?sslmode=disable&timezone=UTC' go test -run Postgres ./
func TestPostgresConnection(t *testing.T) {
	dsn := os.Getenv("WF_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WF_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	sdb, err := sql.Open(driverPostgresRebind, dsn)
	require.NoError(t, err)
	defer sdb.Close()
	db := newDatastore(sdb, driverPostgres)

	require.NoError(t, db.Ping())
	v, err := db.version()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(v, "PostgreSQL "), v)
	t.Logf("version: %s", v)

	var tz string
	require.NoError(t, db.QueryRow("SHOW timezone").Scan(&tz))
	assert.Equal(t, "UTC", tz)

	// `?` placeholders through DB, Tx and a prepared statement.
	var got string
	require.NoError(t, db.QueryRow("SELECT ?::text || '?' || ?::text", "a", "b").Scan(&got))
	assert.Equal(t, "a?b", got)

	table := "wfpg01_dialect_test"
	_, err = db.Exec("DROP TABLE IF EXISTS " + table)
	require.NoError(t, err)
	exists, err := db.dialect.TableExists(ctx, db.DB, table)
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = db.Exec("CREATE TABLE " + table + " (id SERIAL PRIMARY KEY, name TEXT UNIQUE NOT NULL, hash BYTEA)")
	require.NoError(t, err)
	defer db.Exec("DROP TABLE IF EXISTS " + table)

	exists, err = db.dialect.TableExists(ctx, db.DB, table)
	require.NoError(t, err)
	assert.True(t, exists)

	tx, err := db.Begin()
	require.NoError(t, err)
	id1, err := db.dialect.InsertReturningID(ctx, tx, "INSERT INTO "+table+" (name, hash) VALUES (?, ?)", "one", []byte("$2a$10$hash"))
	require.NoError(t, err)
	stmt, err := tx.Prepare("INSERT INTO " + table + " (name) VALUES (?)")
	require.NoError(t, err)
	_, err = stmt.Exec("two")
	require.NoError(t, err)
	stmt.Close()
	require.NoError(t, tx.Commit())
	assert.Equal(t, int64(1), id1)

	var hash []byte
	require.NoError(t, db.QueryRow("SELECT hash FROM "+table+" WHERE id = ?", id1).Scan(&hash))
	assert.Equal(t, "$2a$10$hash", string(hash))

	_, err = db.Exec("INSERT INTO "+table+" (name) VALUES (?)", "one")
	require.Error(t, err)
	assert.True(t, db.isDuplicateKeyErr(err), "%v", err)

	_, err = db.Exec("INSERT INTO "+table+" (name, hash) VALUES (?, ?) "+db.upsert("name")+" hash = ?", "one", []byte("x"), []byte("y"))
	require.NoError(t, err)

	var later bool
	require.NoError(t, db.QueryRow("SELECT "+db.dateAdd(1, "HOUR")+" > "+db.now()+" AND "+db.dateSub(1, "DAY")+" < "+db.now()).Scan(&later))
	assert.True(t, later)

	// sql.Conn.Raw still reaches pgx through the wrapper.
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.Raw(func(dc interface{}) error {
		rc, ok := dc.(*rebindConn)
		require.True(t, ok, "%T", dc)
		return rc.Conn().Ping(ctx)
	}))
}
