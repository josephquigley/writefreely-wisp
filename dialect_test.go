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
	"reflect"
	"strings"
	"testing"

	"github.com/lib/pq"
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
		{"native placeholders only", "SELECT a FROM t WHERE b = $1 AND c = $2", "SELECT a FROM t WHERE b = $1 AND c = $2"},
		{"$1 in single quotes", "SELECT '$1', ?", "SELECT '$1', $1"},
		{"$1 in E-string", `SELECT E'\'$1', ?`, `SELECT E'\'$1', $1`},
		{"$1 in double quotes", `SELECT "a$1" FROM t WHERE a = ?`, `SELECT "a$1" FROM t WHERE a = $1`},
		{"$1 in line comment", "SELECT ? -- not $1\nFROM t", "SELECT $1 -- not $1\nFROM t"},
		{"$1 in block comment", "SELECT /* $1 /* $2 */ */ ?", "SELECT /* $1 /* $2 */ */ $1"},
		{"$1 in dollar quoted", "SELECT $$ $1 $$, ?", "SELECT $$ $1 $$, $1"},
		{"$1 in tagged dollar quoted", "SELECT $x$ $1 $x$, ?", "SELECT $x$ $1 $x$, $1"},
		{"$ inside identifier", "SELECT a$1 FROM t WHERE b = ?", "SELECT a$1 FROM t WHERE b = $1"},
		{"bare $ not followed by a digit", "SELECT $ , ?", "SELECT $ , $1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rebindPostgres(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestRebindPostgresRefusesMixedPlaceholders: a `?` would be numbered $1
// whatever $n the query already holds, so the two would silently collide.
func TestRebindPostgresRefusesMixedPlaceholders(t *testing.T) {
	tests := []struct {
		name, in string
	}{
		{"$1 then ?", "SELECT a FROM t WHERE b = $1 AND c = ?"},
		{"? then $1", "SELECT a FROM t WHERE b = ? AND c = $1"},
		{"? then $2", "SELECT a FROM t WHERE b = ? AND c = $2"},
		{"multi-digit $n", "SELECT ? , $12"},
		{"adjacent", "VALUES ($1,?)"},
		{"$1 after a quoted ?", "SELECT '?', $1, ?"},
		{"$1 after a comment", "SELECT ? /* x */ $1"},
		{"cast after $1", "SELECT $1::int, ?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rebindPostgres(tt.in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), driverPostgresRebind)
			assert.Empty(t, got)
		})
	}

	pg := newDatastore(nil, driverPostgres)
	assert.Panics(t, func() { pg.dialect.Rebind("SELECT $1, ?") })

	// rebindConn refuses before it reaches lib/pq: inner is nil, so any
	// call through to it would panic.
	c := &rebindConn{}
	const mixed = "SELECT $1, ?"
	ctx := context.Background()
	_, err := c.Prepare(mixed)
	assert.Error(t, err)
	_, err = c.PrepareContext(ctx, mixed)
	assert.Error(t, err)
	_, err = c.ExecContext(ctx, mixed, nil)
	assert.Error(t, err)
	_, err = c.QueryContext(ctx, mixed, nil)
	assert.Error(t, err)
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
	assert.Equal(t, "SUBSTR(content, 1, 80)", lite.clip("content", 80))
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

	// lib/pq must read it back the same way.
	pc, err := pq.NewConfig(dsn)
	require.NoError(t, err)
	assert.Equal(t, "db.internal", pc.Host)
	assert.Equal(t, uint16(6543), pc.Port)
	assert.Equal(t, "wf", pc.User)
	assert.Equal(t, "p@ss/w:rd?#", pc.Password)
	assert.Equal(t, "require", string(pc.SSLMode))
	assert.Equal(t, "UTC", pc.Runtime["timezone"])

	dsn = postgresDSN(config.DatabaseCfg{Host: "::1", Database: "wf"})
	u, err = url.Parse(dsn)
	require.NoError(t, err)
	assert.Equal(t, "[::1]:5432", u.Host)
	assert.Equal(t, "disable", u.Query().Get("sslmode"))
	assert.Nil(t, u.User)
}

// TestPostgresRebindConnForwards checks, against the lib/pq version in
// go.mod, that rebindConn implements every optional database/sql/driver
// connection interface lib/pq's connection implements. If a lib/pq upgrade
// adds one, this fails until rebindConn forwards it too. The deprecated
// Execer and Queryer are left out: database/sql calls them only when the
// Context variants are missing, and rebindConn has those.
func TestPostgresRebindConnForwards(t *testing.T) {
	sdb := newPostgresTestDB(t)
	conn, err := sdb.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	var inner reflect.Type
	require.NoError(t, conn.Raw(func(dc interface{}) error {
		rc, ok := dc.(*rebindConn)
		require.True(t, ok, "%T", dc)
		inner = reflect.TypeOf(rc.Unwrap())
		return nil
	}))

	ifaces := map[string]reflect.Type{
		"ExecerContext":      reflect.TypeOf((*driver.ExecerContext)(nil)).Elem(),
		"QueryerContext":     reflect.TypeOf((*driver.QueryerContext)(nil)).Elem(),
		"ConnPrepareContext": reflect.TypeOf((*driver.ConnPrepareContext)(nil)).Elem(),
		"ConnBeginTx":        reflect.TypeOf((*driver.ConnBeginTx)(nil)).Elem(),
		"NamedValueChecker":  reflect.TypeOf((*driver.NamedValueChecker)(nil)).Elem(),
		"Pinger":             reflect.TypeOf((*driver.Pinger)(nil)).Elem(),
		"SessionResetter":    reflect.TypeOf((*driver.SessionResetter)(nil)).Elem(),
		"Validator":          reflect.TypeOf((*driver.Validator)(nil)).Elem(),
	}
	outer := reflect.TypeOf((*rebindConn)(nil))
	for name, it := range ifaces {
		if inner.Implements(it) {
			assert.Truef(t, outer.Implements(it), "%v implements driver.%s but rebindConn does not forward it", inner, name)
		}
	}
}

// TestPostgresPoolDropsTerminatedBackend: after the server kills a pooled
// connection (failover, restart, pg_terminate_backend), at most one call may
// fail; the next must run on a fresh connection.
func TestPostgresPoolDropsTerminatedBackend(t *testing.T) {
	sdb := newPostgresTestDB(t)
	ctx := context.Background()
	sdb.SetMaxOpenConns(1)
	sdb.SetMaxIdleConns(1)

	var pid int
	require.NoError(t, sdb.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	var killed bool
	require.NoError(t, testPGAdmin.QueryRowContext(ctx, "SELECT pg_terminate_backend(?, 5000)", pid).Scan(&killed))
	require.True(t, killed)

	_, _ = sdb.ExecContext(ctx, "SELECT 1") // may surface the dead connection once
	var newPID int
	require.NoError(t, sdb.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&newPID))
	assert.NotEqual(t, pid, newPID)
}

func TestPostgresErrorClassifiers(t *testing.T) {
	pg := newDatastore(nil, driverPostgres)
	dup := fmt.Errorf("wrapped: %w", &pq.Error{Code: "23505"})
	load := &pq.Error{Code: "53300"}
	other := &pq.Error{Code: "42P01"}

	assert.True(t, pg.isDuplicateKeyErr(dup))
	assert.False(t, pg.isDuplicateKeyErr(other))
	assert.False(t, pg.isDuplicateKeyErr(sql.ErrNoRows))
	assert.True(t, pg.isHighLoadError(load))
	assert.False(t, pg.isHighLoadError(dup))
	assert.False(t, pg.isHighLoadError(other))
	assert.False(t, pg.isHighLoadError(nil))
	assert.False(t, pg.isIgnorableError(dup))
	assert.False(t, pg.isIgnorableError(other))
}

// pgHighLoadCodes are the SQLSTATEs that mean "temporarily unavailable, try
// again shortly".
var pgHighLoadCodes = map[string]string{
	"53300": "too_many_connections",
	"53400": "configuration_limit_exceeded",
	"57P03": "cannot_connect_now",
	"53000": "insufficient_resources",
}

func TestPostgresHighLoadCodes(t *testing.T) {
	pg := newDatastore(nil, driverPostgres)
	for code, name := range pgHighLoadCodes {
		t.Run(name, func(t *testing.T) {
			bare := &pq.Error{Code: pq.ErrorCode(code)}
			assert.True(t, pg.isHighLoadError(bare))
			assert.True(t, pg.isHighLoadError(fmt.Errorf("wrapped: %w", bare)))
		})
	}
	// Disk full and out of memory persist until an operator steps in, and a
	// 503 would tell clients to retry something that will not clear.
	for _, code := range []string{"53100", "53200", "42P01", "23505", "57014"} {
		assert.Falsef(t, pg.isHighLoadError(&pq.Error{Code: pq.ErrorCode(code)}), code)
	}
}

// pqErrDriver is a database/sql driver whose every query fails with a
// *pq.Error carrying code.
type pqErrDriver struct{ code string }

func (d pqErrDriver) Open(string) (driver.Conn, error) { return pqErrConn(d), nil }

type pqErrConn struct{ code string }

func (c pqErrConn) Prepare(string) (driver.Stmt, error) {
	return nil, &pq.Error{Code: pq.ErrorCode(c.code)}
}
func (c pqErrConn) Close() error              { return nil }
func (c pqErrConn) Begin() (driver.Tx, error) { return nil, &pq.Error{Code: pq.ErrorCode(c.code)} }

// TestPostgresGetCollectionByHighLoadCodes checks that each high-load code
// reaches the caller as ErrUnavailable (a 503) rather than a logged 500.
func TestPostgresGetCollectionByHighLoadCodes(t *testing.T) {
	for code, name := range pgHighLoadCodes {
		t.Run(name, func(t *testing.T) {
			sdb := sql.OpenDB(pqErrConnector{code})
			defer sdb.Close()
			db := newDatastore(sdb, driverPostgres)
			c, err := db.GetCollectionBy("alias = ?", "anything")
			assert.Nil(t, c)
			assert.Equal(t, ErrUnavailable, err)
		})
	}

	sdb := sql.OpenDB(pqErrConnector{"42P01"})
	defer sdb.Close()
	_, err := newDatastore(sdb, driverPostgres).GetCollectionBy("alias = ?", "anything")
	require.Error(t, err)
	assert.NotEqual(t, ErrUnavailable, err)
}

type pqErrConnector struct{ code string }

func (c pqErrConnector) Connect(context.Context) (driver.Conn, error) { return pqErrConn(c), nil }
func (c pqErrConnector) Driver() driver.Driver                        { return pqErrDriver(c) }

// TestPostgresConnection exercises the driver against a real server, in a
// fresh database from the Postgres test harness (harness_pg_test.go). It runs
// only under WF_TEST_DB_TYPE=postgres, e.g. `make test-postgres
// GOTESTFLAGS='-run Postgres -v'`.
func TestPostgresConnection(t *testing.T) {
	sdb := newPostgresTestDB(t)
	ctx := context.Background()
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

	// sql.Conn.Raw still reaches lib/pq through the wrapper.
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.Raw(func(dc interface{}) error {
		rc, ok := dc.(*rebindConn)
		require.True(t, ok, "%T", dc)
		p, ok := rc.Unwrap().(driver.Pinger)
		require.True(t, ok, "%T", rc.Unwrap())
		return p.Ping(ctx)
	}))
}

// TestPostgresIgnoresPGEnvironment: lib/pq's NewConnector reads PG*
// variables and refuses some outright (PGGSSENCMODE, PGCHANNELBINDING, a
// PGSERVICE with no service file). The [database] section is the whole
// configuration, so a host that exports them for psql must still start.
func TestPostgresIgnoresPGEnvironment(t *testing.T) {
	t.Setenv("PGSSLMODE", "prefer")
	t.Setenv("PGGSSENCMODE", "disable")
	t.Setenv("PGCHANNELBINDING", "prefer")
	t.Setenv("PGSERVICE", "elsewhere")
	t.Setenv("PGDATABASE", "not_this_one")
	dsn := postgresDSN(config.DatabaseCfg{User: "wf", Password: "pw", Database: "writefreely", Host: "db.internal", TLS: true})
	_, err := rebindDriver{}.OpenConnector(dsn)
	require.NoError(t, err)
}

// TestPqConfigFromDSN: the DSN is parsed the way lib/pq parses it, minus
// the environment.
func TestPqConfigFromDSN(t *testing.T) {
	for _, dsn := range []string{
		postgresDSN(config.DatabaseCfg{User: "wf", Password: "p@ss/w:rd?# '\\", Database: "writefreely", Host: "db.internal", Port: 6543, TLS: true}),
		postgresDSN(config.DatabaseCfg{User: "wf", Database: "wf", Host: "::1"}),
		"postgres://writefreely:writefreely@127.0.0.1:5432/wf_test_00?sslmode=disable&timezone=UTC&connect_timeout=5",
	} {
		want, err := pq.NewConfig(dsn)
		require.NoError(t, err, dsn)
		got, err := pqConfigFromDSN(dsn)
		require.NoError(t, err, dsn)
		assert.Equal(t, want.Host, got.Host, dsn)
		assert.Equal(t, want.Port, got.Port, dsn)
		assert.Equal(t, want.User, got.User, dsn)
		assert.Equal(t, want.Password, got.Password, dsn)
		assert.Equal(t, want.Database, got.Database, dsn)
		assert.Equal(t, want.SSLMode, got.SSLMode, dsn)
		assert.Equal(t, want.ApplicationName, got.ApplicationName, dsn)
		assert.Equal(t, want.ConnectTimeout, got.ConnectTimeout, dsn)
		assert.Equal(t, want.Runtime, got.Runtime, dsn)
		// lib/pq parses timestamps assuming ISO output in UTF-8.
		assert.Equal(t, want.ClientEncoding, got.ClientEncoding, dsn)
		assert.Equal(t, want.Datestyle, got.Datestyle, dsn)
	}

	_, err := pqConfigFromDSN("host=x dbname=y")
	assert.Error(t, err, "only URL DSNs are accepted")
}
