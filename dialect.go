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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// dialect is the single home for SQL that differs between database engines.
//
// One implementation exists per supported driver (mysqlDialect, sqliteDialect,
// postgresDialect). A datastore resolves its dialect once, from its driver
// name, via dialectFor; an unknown driver name panics there rather than
// falling through to MySQL SQL, which is what the inline
// `if sqlite {…} else {mysql}` branches used to do.
//
// Rules for adding to it:
//
//   - A method returns a SQL fragment (Now, Clip, …) or runs a small,
//     self-contained statement through the sqlQueryer it is given
//     (Version, InsertReturningID, TableExists). It never holds a connection.
//   - Every implementation must implement every method. If an engine has no
//     equivalent, return the closest no-op and say so in a comment, or panic
//     with a message naming the method; never return MySQL SQL by default.
//   - Placeholders are written as `?` everywhere, including inside dialect
//     methods. On Postgres the rebinding driver (pgdriver.go) rewrites them to
//     `$n` for every statement, whether it goes through datastore, a *sql.Tx,
//     or the migrations package. Rebind exists only for code that must see
//     the final text (tests, logging, or a raw pgx connection).
type dialect interface {
	// DriverName is the database/sql driver name this dialect serves, i.e.
	// one of driverMySQL, driverSQLite or driverPostgres. It is the value of
	// datastore.driverName and of the config file's [database] type.
	DriverName() string

	// Now returns an SQL expression for the current timestamp.
	Now() string
	// NowForInsert returns the current time as a Go value to bind to a
	// datetime parameter, in the zone this engine's stored values use:
	// server-local on MySQL (its connection uses loc=Local, so existing
	// installs hold local times), UTC on SQLite and Postgres.
	NowForInsert() time.Time
	// TimeArg returns t as it should be bound to a datetime parameter. It is
	// t.UTC() on Postgres, so every value it is sent is UTC (the columns are
	// timestamptz, so the instant is the same either way). MySQL and SQLite
	// return t unchanged, preserving their behaviour.
	TimeArg(t time.Time) time.Time
	// Clip returns an SQL expression for the first l characters of field.
	Clip(field string, l int) string
	// Upsert returns the clause that follows an INSERT … VALUES (…) to turn
	// it into an upsert, ending just before the first `col = ?` assignment.
	// indexedCols is the unique key that conflicts; MySQL ignores it, but
	// SQLite and Postgres require it, so always pass it.
	Upsert(indexedCols ...string) string
	// DateAdd returns an SQL expression for now + l units. DateSub returns
	// now - l units. unit is one of SECOND, MINUTE, HOUR, DAY, WEEK, MONTH,
	// YEAR (any case); it is concatenated into SQL, so it must be a constant
	// at the call site. Postgres panics on any other unit.
	DateAdd(l int, unit string) string
	DateSub(l int, unit string) string

	// Version returns the database server's version string.
	Version(ctx context.Context, q sqlQueryer) (string, error)
	// Rebind returns query with its `?` placeholders in this dialect's
	// native form: unchanged for MySQL and SQLite, `$1…$n` for Postgres.
	// Normal code does NOT need to call it; see the type comment.
	Rebind(query string) string
	// InsertReturningID runs an INSERT and returns the generated `id`
	// column. MySQL and SQLite use Exec + LastInsertId. Postgres appends
	// " RETURNING id" to query and scans the result, so query must be a
	// plain INSERT into a table whose key column is named `id`, with no
	// RETURNING clause and no trailing semicolon. An INSERT that inserts no
	// row (e.g. ON CONFLICT DO NOTHING) returns sql.ErrNoRows on Postgres.
	InsertReturningID(ctx context.Context, q sqlQueryer, query string, args ...interface{}) (int64, error)
	// TableExists reports whether a table called name exists in the current
	// database (MySQL), the main schema (SQLite) or the search_path
	// (Postgres).
	TableExists(ctx context.Context, q sqlQueryer, name string) (bool, error)
	// BinaryEquals returns a condition matching the binary column col
	// (binary/bytea, or TEXT on SQLite) exactly against b, and the arguments
	// it consumes, in order. It exists for SQLite, whose databases hold
	// values written both as TEXT (older code bound a Go string) and as BLOB
	// (a Go []byte), which `=` never considers equal.
	BinaryEquals(col string, b []byte) (string, []interface{})
}

// sqlQueryer is satisfied by *sql.DB, *sql.Tx and *sql.Conn, so dialect
// methods work equally inside and outside a transaction.
type sqlQueryer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

var (
	_ sqlQueryer = (*sql.DB)(nil)
	_ sqlQueryer = (*sql.Tx)(nil)
	_ sqlQueryer = (*sql.Conn)(nil)

	_ dialect = mysqlDialect{}
	_ dialect = sqliteDialect{}
	_ dialect = postgresDialect{}
)

// dialectFor returns the dialect for a driver name, and panics with a
// message on any name it does not know. It is the one place a new engine is
// wired in.
func dialectFor(driverName string) dialect {
	switch driverName {
	case driverMySQL:
		return mysqlDialect{}
	case driverSQLite:
		return sqliteDialect{}
	case driverPostgres:
		return postgresDialect{}
	default:
		panic(fmt.Sprintf("dialectFor: unsupported database driver %q (want %q, %q or %q)", driverName, driverMySQL, driverSQLite, driverPostgres))
	}
}

// newDatastore wraps an open *sql.DB with the dialect for driverName. It
// panics on an unknown driver name. For Postgres, db must have been opened
// with sql.Open(driverPostgresRebind, dsn) so that `?` placeholders work.
func newDatastore(db *sql.DB, driverName string) *datastore {
	return &datastore{DB: db, driverName: driverName, dialect: dialectFor(driverName)}
}

// unsupportedDriver panics with a message naming the function that has not
// been ported to the datastore's driver. Inline driver branches outside this
// file end in `default: unsupportedDriver("Func", db.driverName)`.
func unsupportedDriver(fn, driverName string) {
	panic(fmt.Sprintf("%s: not implemented for database driver %q", fn, driverName))
}

// ---------------------------------------------------------------- MySQL --

type mysqlDialect struct{}

func (mysqlDialect) DriverName() string { return driverMySQL }

func (mysqlDialect) Now() string { return "NOW()" }

func (mysqlDialect) NowForInsert() time.Time { return time.Now() }

func (mysqlDialect) TimeArg(t time.Time) time.Time { return t }

func (mysqlDialect) Clip(field string, l int) string {
	return fmt.Sprintf("LEFT(%s, %d)", field, l)
}

func (mysqlDialect) Upsert(indexedCols ...string) string {
	return "ON DUPLICATE KEY UPDATE"
}

func (mysqlDialect) DateAdd(l int, unit string) string {
	return fmt.Sprintf("DATE_ADD(NOW(), INTERVAL %d %s)", l, unit)
}

func (mysqlDialect) DateSub(l int, unit string) string {
	return fmt.Sprintf("DATE_SUB(NOW(), INTERVAL %d %s)", l, unit)
}

func (mysqlDialect) Version(ctx context.Context, q sqlQueryer) (string, error) {
	return queryVersion(ctx, q, "SELECT version()")
}

func (mysqlDialect) Rebind(query string) string { return query }

func (mysqlDialect) InsertReturningID(ctx context.Context, q sqlQueryer, query string, args ...interface{}) (int64, error) {
	return execLastInsertID(ctx, q, query, args...)
}

func (mysqlDialect) TableExists(ctx context.Context, q sqlQueryer, name string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", name).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (mysqlDialect) BinaryEquals(col string, b []byte) (string, []interface{}) {
	return col + " = ?", []interface{}{b}
}

// --------------------------------------------------------------- SQLite --

type sqliteDialect struct{}

func (sqliteDialect) DriverName() string { return driverSQLite }

func (sqliteDialect) Now() string { return "strftime('%Y-%m-%d %H:%M:%S','now')" }

// SQLite stores datetimes in UTC.
func (sqliteDialect) NowForInsert() time.Time { return time.Now().UTC() }

func (sqliteDialect) TimeArg(t time.Time) time.Time { return t }

// SQLite strings are 1-indexed (WFPG-11 defect B).
func (sqliteDialect) Clip(field string, l int) string {
	return fmt.Sprintf("SUBSTR(%s, 1, %d)", field, l)
}

func (sqliteDialect) Upsert(indexedCols ...string) string {
	// NOTE: SQLite UPSERT syntax only works in v3.24.0 (2018-06-04) or later
	// Leaving this for whenever we can upgrade and include it in our binary
	cc := strings.Join(indexedCols, ", ")
	return "ON CONFLICT(" + cc + ") DO UPDATE SET"
}

func (sqliteDialect) DateAdd(l int, unit string) string {
	return fmt.Sprintf("DATETIME('now', '%d %s')", l, unit)
}

func (sqliteDialect) DateSub(l int, unit string) string {
	return fmt.Sprintf("DATETIME('now', '-%d %s')", l, unit)
}

func (sqliteDialect) Version(ctx context.Context, q sqlQueryer) (string, error) {
	return queryVersion(ctx, q, "SELECT sqlite_version()")
}

func (sqliteDialect) Rebind(query string) string { return query }

func (sqliteDialect) InsertReturningID(ctx context.Context, q sqlQueryer, query string, args ...interface{}) (int64, error) {
	return execLastInsertID(ctx, q, query, args...)
}

func (sqliteDialect) TableExists(ctx context.Context, q sqlQueryer, name string) (bool, error) {
	var dummy string
	err := q.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&dummy)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// BinaryEquals on SQLite matches the value stored either as a BLOB or as
// TEXT holding the same bytes. A BLOB never equals TEXT under `=`, and
// access tokens were inserted as TEXT (a Go string) until WFPG-05, so
// existing databases hold TEXT tokens while new ones are BLOBs. CAST(? AS
// TEXT) reinterprets the bytes without changing them, and BINARY collation
// compares TEXT byte for byte, so this is still an exact match. An IN list
// on the column keeps the primary-key index usable.
func (sqliteDialect) BinaryEquals(col string, b []byte) (string, []interface{}) {
	return col + " IN (?, CAST(? AS TEXT))", []interface{}{b, b}
}

// ------------------------------------------------------------- Postgres --

type postgresDialect struct{}

// SQLSTATE codes the error classifiers recognise. See
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	pgErrUniqueViolation    = "23505"
	pgErrTooManyConnections = "53300"
)

// postgresIntervalUnits are the units DateAdd and DateSub accept on
// Postgres, upper-cased. Postgres accepts them in either case.
var postgresIntervalUnits = map[string]bool{
	"SECOND": true,
	"MINUTE": true,
	"HOUR":   true,
	"DAY":    true,
	"WEEK":   true,
	"MONTH":  true,
	"YEAR":   true,
}

func (postgresDialect) DriverName() string { return driverPostgres }

func (postgresDialect) Now() string { return "NOW()" }

// The columns are timestamptz and the session TimeZone is UTC (postgresDSN),
// so Go always sends UTC and the stored instant never depends on the
// process's time zone.
func (postgresDialect) NowForInsert() time.Time { return time.Now().UTC() }

func (postgresDialect) TimeArg(t time.Time) time.Time { return t.UTC() }

func (postgresDialect) Clip(field string, l int) string {
	return fmt.Sprintf("LEFT(%s, %d)", field, l)
}

func (postgresDialect) Upsert(indexedCols ...string) string {
	if len(indexedCols) == 0 {
		panic("postgresDialect.Upsert: Postgres needs the conflicting columns for ON CONFLICT")
	}
	return "ON CONFLICT (" + strings.Join(indexedCols, ", ") + ") DO UPDATE SET"
}

func (d postgresDialect) DateAdd(l int, unit string) string {
	return fmt.Sprintf("NOW() + INTERVAL '%d %s'", l, d.intervalUnit("DateAdd", unit))
}

func (d postgresDialect) DateSub(l int, unit string) string {
	return fmt.Sprintf("NOW() - INTERVAL '%d %s'", l, d.intervalUnit("DateSub", unit))
}

func (postgresDialect) intervalUnit(fn, unit string) string {
	u := strings.ToUpper(unit)
	if !postgresIntervalUnits[u] {
		panic(fmt.Sprintf("postgresDialect.%s: interval unit %q is not allowed", fn, unit))
	}
	return u
}

func (postgresDialect) Version(ctx context.Context, q sqlQueryer) (string, error) {
	return queryVersion(ctx, q, "SELECT version()")
}

func (postgresDialect) Rebind(query string) string { return rebindPostgres(query) }

func (postgresDialect) InsertReturningID(ctx context.Context, q sqlQueryer, query string, args ...interface{}) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, strings.TrimRight(query, " \t\r\n;")+" RETURNING id", args...).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (postgresDialect) TableExists(ctx context.Context, q sqlQueryer, name string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, "SELECT to_regclass(?) IS NOT NULL", name).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (postgresDialect) BinaryEquals(col string, b []byte) (string, []interface{}) {
	return col + " = ?", []interface{}{b}
}

// isPostgresErrCode reports whether err is (or wraps) a Postgres error with
// the given SQLSTATE code.
func isPostgresErrCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

// --------------------------------------------------------------- shared --

func queryVersion(ctx context.Context, q sqlQueryer, query string) (string, error) {
	var v string
	if err := q.QueryRowContext(ctx, query).Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

func execLastInsertID(ctx context.Context, q sqlQueryer, query string, args ...interface{}) (int64, error) {
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
