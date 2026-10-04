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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/writeas/web-core/log"
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
//     (Version, InsertReturningID, TableExists). It never holds a connection,
//     with one exception: TryJobLock, whose lock lives on a connection and
//     which therefore pins one out of the pool until it is released.
//   - Every implementation must implement every method. If an engine has no
//     equivalent, return the closest no-op and say so in a comment, or panic
//     with a message naming the method; never return MySQL SQL by default.
//   - Placeholders are written as `?` everywhere, including inside dialect
//     methods. On Postgres the rebinding driver (pgdriver.go) rewrites them to
//     `$n` for every statement, whether it goes through datastore, a *sql.Tx,
//     or the migrations package. Rebind exists only for code that must see
//     the final text (tests, logging, or a raw driver connection).
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
	// Normal code does NOT need to call it; see the type comment. Postgres
	// panics on a query that mixes `?` with native `$n` placeholders, which
	// the rebinding driver refuses (see rebindPostgres).
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
	// InsertIgnore rewrites a plain `INSERT INTO …` statement so that a row
	// conflicting with any unique key is skipped instead of failing:
	// `INSERT IGNORE INTO` on MySQL, `INSERT OR IGNORE INTO` on SQLite, and
	// a trailing `ON CONFLICT DO NOTHING` on Postgres. Use it instead of
	// running the INSERT and then forgiving isDuplicateKeyErr: on Postgres
	// that error has already aborted the enclosing transaction, so every
	// later statement fails and Commit rolls back. insert must begin with
	// "INSERT INTO " and have no trailing clause or semicolon; anything
	// else panics. (MySQL's IGNORE also downgrades some other errors, such
	// as truncation, to warnings; that is the existing MySQL behaviour.)
	InsertIgnore(insert string) string

	// TryJobLock takes the cross-process lock called name without waiting,
	// so that a periodic job runs in at most one app process at a time
	// against this database. ok is false, with a nil error, when another
	// process holds it. When ok is true the caller must call unlock exactly
	// once, when the job is done.
	//
	// The lock lives on a connection taken from db and kept out of the pool
	// until unlock, so the job itself must not need every connection the
	// pool allows. If the process dies, the server releases the lock with
	// the connection. A job that cannot take the lock skips its run rather
	// than waiting for the other process to finish.
	TryJobLock(ctx context.Context, db *sql.DB, name string) (unlock func(), ok bool, err error)
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

func (mysqlDialect) InsertIgnore(insert string) string {
	return "INSERT IGNORE INTO " + insertIntoRest("mysqlDialect", insert)
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

// TryJobLock on MySQL is GET_LOCK with a zero timeout on a dedicated
// connection, released with RELEASE_LOCK on that same connection. A
// GET_LOCK name is global to the server, not to one database, so the name
// is qualified with DATABASE() and hashed: two installs sharing a server
// must not block each other, and SHA1's 40 characters fit the 64-character
// limit whatever the database is called.
func (mysqlDialect) TryJobLock(ctx context.Context, db *sql.DB, name string) (func(), bool, error) {
	const lockName = "SHA1(CONCAT(DATABASE(), ':', ?))"
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK("+lockName+", 0)", jobLockName(name)).Scan(&got); err != nil {
		conn.Close()
		return nil, false, err
	}
	if !got.Valid {
		conn.Close()
		return nil, false, fmt.Errorf("GET_LOCK for job lock %q returned NULL", name)
	}
	if got.Int64 != 1 {
		conn.Close()
		return nil, false, nil
	}
	return func() {
		var released sql.NullInt64
		err := conn.QueryRowContext(context.Background(), "SELECT RELEASE_LOCK("+lockName+")", jobLockName(name)).Scan(&released)
		if err != nil || released.Int64 != 1 {
			// The lock may still be held by this connection. Returning it
			// to the pool would leave the job locked for as long as the
			// pool keeps it, so close it instead: the server then
			// releases the lock.
			log.Error("[jobs] Unable to release job lock %q (released=%v): %v; discarding its connection", name, released, err)
			discardConn(conn)
			return
		}
		conn.Close()
	}, true, nil
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

func (sqliteDialect) InsertIgnore(insert string) string {
	return "INSERT OR IGNORE INTO " + insertIntoRest("sqliteDialect", insert)
}

// TryJobLock on SQLite always succeeds and holds nothing. A SQLite database
// is a file on one host, used by one app process, so there is no second
// process to exclude.
func (sqliteDialect) TryJobLock(ctx context.Context, db *sql.DB, name string) (func(), bool, error) {
	return func() {}, true, nil
}

// ------------------------------------------------------------- Postgres --

type postgresDialect struct{}

// SQLSTATE codes the error classifiers recognise. See
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	pgErrUniqueViolation    = "23505"
	pgErrTooManyConnections = "53300"
	// pgErrInsufficientResources is the generic "out of a resource that
	// frees up again" error. 53100 (disk_full) and 53200 (out_of_memory) are
	// deliberately not treated as high load: they last until an operator
	// intervenes, so a 503 would invite retries that cannot succeed.
	pgErrInsufficientResources = "53000"
	// pgErrConfigLimitExceeded is a per-role or per-database connection
	// limit being reached.
	pgErrConfigLimitExceeded = "53400"
	// pgErrCannotConnectNow is returned while the server is starting up,
	// shutting down or in recovery, such as during a failover.
	pgErrCannotConnectNow = "57P03"
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
func (postgresDialect) NowForInsert() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// TimeArg truncates to microseconds, Postgres' precision, so the stored
// instant does not depend on how the driver rounds the rest.
func (postgresDialect) TimeArg(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

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

func (postgresDialect) Rebind(query string) string {
	q, err := rebindPostgres(query)
	if err != nil {
		panic("postgresDialect.Rebind: " + err.Error())
	}
	return q
}

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

func (postgresDialect) InsertIgnore(insert string) string {
	return "INSERT INTO " + insertIntoRest("postgresDialect", insert) + " ON CONFLICT DO NOTHING"
}

// TryJobLock on Postgres is a transaction-level advisory lock,
// pg_try_advisory_xact_lock, taken in a transaction that stays open until
// unlock rolls it back. Advisory locks are scoped to the current database,
// so two installs sharing a server do not block each other.
//
// The transaction-level lock is used rather than the session-level
// pg_try_advisory_lock because its lifetime is the transaction's, and
// database/sql always ends a transaction before the connection can go back
// to the pool. A session-level lock outlives anything database/sql tracks:
// a failed pg_advisory_unlock would return a connection still holding it to
// the pool, and the job would stay locked for as long as the pool kept that
// connection. It also keeps working behind a transaction-pooling proxy such
// as PgBouncer, where a session-level lock can land on a different backend
// from its unlock. The cost is an idle transaction for the length of a run;
// it writes nothing, and the job's own queries go through the pool, not
// through it. If the server ends it early (idle_in_transaction_session_timeout),
// the lock is released before the run finishes, which only weakens this
// guard back to having none.
func (postgresDialect) TryJobLock(ctx context.Context, db *sql.DB, name string) (func(), bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock(hashtext(?))", jobLockName(name)).Scan(&got); err != nil {
		tx.Rollback()
		return nil, false, err
	}
	if !got {
		tx.Rollback()
		return nil, false, nil
	}
	return func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			log.Error("[jobs] Unable to end the transaction holding job lock %q: %v", name, err)
		}
	}, true, nil
}

// isPostgresErrCode reports whether err is (or wraps) a Postgres error with
// the given SQLSTATE code.
func isPostgresErrCode(err error, code string) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && string(pqErr.Code) == code
}

// isPostgresHighLoadErr reports whether err is a Postgres error meaning the
// database is temporarily unavailable or overloaded, so the caller should
// answer 503 rather than 500.
func isPostgresHighLoadErr(err error) bool {
	return isPostgresErrCode(err, pgErrTooManyConnections) ||
		isPostgresErrCode(err, pgErrConfigLimitExceeded) ||
		isPostgresErrCode(err, pgErrCannotConnectNow) ||
		isPostgresErrCode(err, pgErrInsufficientResources)
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

// jobLockName qualifies a TryJobLock name, so that it cannot collide with a
// lock some other software takes on the same database or server.
func jobLockName(name string) string {
	return "writefreely:jobs:" + name
}

// discardConn closes conn without returning its underlying connection to
// the pool: reporting driver.ErrBadConn from Raw makes database/sql close
// it. Use it for a connection left in a state the next user must not
// inherit, such as one that may still hold a lock.
func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
	conn.Close()
}

// insertIntoRest returns what follows "INSERT INTO " in insert, panicking
// (with the calling dialect's name) if insert is not a plain INSERT INTO or
// ends in a semicolon.
func insertIntoRest(d, insert string) string {
	const prefix = "INSERT INTO "
	if !strings.HasPrefix(insert, prefix) {
		panic(fmt.Sprintf("%s.InsertIgnore: want a statement beginning %q, got %q", d, prefix, insert))
	}
	rest := strings.TrimRight(insert[len(prefix):], " \t\r\n")
	if strings.HasSuffix(rest, ";") {
		panic(fmt.Sprintf("%s.InsertIgnore: statement must not end in a semicolon: %q", d, insert))
	}
	return rest
}

// execBestEffort runs one statement inside t that is allowed to fail without
// failing the transaction, by fencing it in a savepoint. On error the
// statement's effects are rolled back to the savepoint, the error is logged
// and returned, and t remains usable.
//
// On MySQL and SQLite a failed statement leaves the transaction usable
// anyway, so this only makes that explicit; on Postgres any error aborts the
// whole transaction (SQLSTATE 25P02 on every later statement, and Commit
// rolls back), and the savepoint is what lets the caller carry on. All three
// engines support SAVEPOINT inside a transaction. name must be a constant SQL
// identifier: it is concatenated into the statement.
func execBestEffort(t *sql.Tx, name, query string, args ...interface{}) error {
	if _, err := t.Exec("SAVEPOINT " + name); err != nil {
		log.Error("Unable to set savepoint %s: %v", name, err)
		return err
	}
	_, err := t.Exec(query, args...)
	if err != nil {
		log.Error("Best-effort statement failed (rolled back to savepoint %s): %v", name, err)
		if _, rbErr := t.Exec("ROLLBACK TO SAVEPOINT " + name); rbErr != nil {
			log.Error("Unable to roll back to savepoint %s: %v", name, rbErr)
			return rbErr
		}
	}
	if _, relErr := t.Exec("RELEASE SAVEPOINT " + name); relErr != nil {
		log.Error("Unable to release savepoint %s: %v", name, relErr)
		return relErr
	}
	return err
}
