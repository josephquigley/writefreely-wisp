/*
 * Copyright © 2019 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

// Package migrations contains database migrations for WriteFreely.
//
// Upstream's migrations (vN.go, listed in migrations) are left exactly as
// upstream has them. Migrate runs upstream's V1 to V17, then
// wispMigrations, which this edition versions in its own table; see
// WispTable in wisp.go, which also says what to do when an upstream merge
// adds a migration.
//
// # Rule for wisp migrations
//
// Every migration in wispMigrations runs on MySQL, SQLite and Postgres, and
// must be correct on all three. Write column types with the helpers in
// drivers.go (typeInt, typeVarChar, typeBool, typeDateTime, …), never as a
// literal type, and put any statement that cannot be written portably in a
// `switch db.driverName` with a case for each engine and a default that
// panics. Make each statement safe to run again: MySQL commits DDL as it
// goes, so a migration that stops part way runs again from the start. Test
// it against all three before merging.
//
// Upstream's V1 to V17 and wisp_v1 predate Postgres support and are MySQL
// and SQLite only. A Postgres database is created by `writefreely db init`
// from postgres.sql, which already describes that schema, and starts at
// PostgresBaseVersion; Migrate refuses to run anything older on Postgres.
// When you add a migration, do not also edit postgres.sql: Migrate applies
// the new migration to fresh Postgres databases right after init.
package migrations

import (
	"database/sql"
	"fmt"

	"github.com/writeas/web-core/log"
)

// TODO: refactor to use the datastore struct from writefreely pkg
type datastore struct {
	*sql.DB
	driverName string
}

// NewDatastore wraps db for running migrations. dn is the driver name
// (driverMySQL, driverSQLite or driverPostgres); any other name panics, so
// that an unsupported engine never falls through to MySQL SQL.
func NewDatastore(db *sql.DB, dn string) *datastore {
	switch dn {
	case driverMySQL, driverSQLite, driverPostgres:
	default:
		panic(fmt.Sprintf("migrations.NewDatastore: unsupported database driver %q", dn))
	}
	return &datastore{db, dn}
}

// unsupportedDriver panics with a message naming the migration function
// that has not been ported to driverName.
func unsupportedDriver(fn, driverName string) {
	panic(fmt.Sprintf("migrations.%s: not implemented for database driver %q", fn, driverName))
}

// errBeforePostgresBase is returned by an upstream migration if it is ever
// asked to run on Postgres. Migrate refuses before it gets that far; this is
// the second line of defence.
func errBeforePostgresBase(fn string) error {
	return fmt.Errorf("migrations.%s: predates Postgres support and must not run on Postgres", fn)
}

// TODO: use these consts from writefreely pkg
const (
	driverMySQL    = "mysql"
	driverSQLite   = "sqlite3"
	driverPostgres = "postgres"
)

type Migration interface {
	Description() string
	Migrate(db *datastore) error
}

type migration struct {
	description string
	migrate     func(db *datastore) error
}

func New(d string, fn func(db *datastore) error) Migration {
	return &migration{d, fn}
}

func (m *migration) Description() string {
	return m.description
}

func (m *migration) Migrate(db *datastore) error {
	return m.migrate(db)
}

var migrations = []Migration{
	New("support user invites", supportUserInvites),                  // -> V1 (v0.8.0)
	New("support dynamic instance pages", supportInstancePages),      // V1 -> V2 (v0.9.0)
	New("support users suspension", supportUserStatus),               // V2 -> V3 (v0.11.0)
	New("support oauth", oauth),                                      // V3 -> V4
	New("support slack oauth", oauthSlack),                           // V4 -> v5
	New("support ActivityPub mentions", supportActivityPubMentions),  // V5 -> V6
	New("support oauth attach", oauthAttach),                         // V6 -> V7
	New("support oauth via invite", oauthInvites),                    // V7 -> V8 (v0.12.0)
	New("optimize drafts retrieval", optimizeDrafts),                 // V8 -> V9
	New("support post signatures", supportPostSignatures),            // V9 -> V10 (v0.13.0)
	New("Widen oauth_users.access_token", widenOauthAcceesToken),     // V10 -> V11
	New("support verifying fedi profile", fediverseVerifyProfile),    // V11 -> V12 (v0.14.0)
	New("support newsletters", supportLetters),                       // V12 -> V13
	New("support password resetting", supportPassReset),              // V13 -> V14
	New("speed up blog post retrieval", addPostRetrievalIndex),       // V14 -> V15
	New("support ActivityPub likes", supportRemoteLikes),             // V15 -> V16 (v0.16.0)
	New("fix post signature character set", fixPostSignatureCharset), // V16 -> V17 (v0.17.0)
}

// CurrentVer returns the wisp version the application is on: the number of
// wispMigrations. See WispTable.
func CurrentVer() int {
	return len(wispMigrations)
}

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// SetInitialMigrations records, in a freshly created schema, the versions
// that schema is at.
func SetInitialMigrations(db *datastore) error {
	return SetInitialMigrationsOn(db, db.driverName)
}

// SetInitialMigrationsOn is SetInitialMigrations through ex, which may be a
// transaction that also created the schema. schema.sql and sqlite.sql
// describe upstream's V1; postgres.sql describes upstream's V17 and
// PostgresBaseVersion.
func SetInitialMigrationsOn(ex Execer, driverName string) error {
	up, w := 1, 0
	switch driverName {
	case driverMySQL, driverSQLite:
	case driverPostgres:
		up, w = upstreamBaseVersion, PostgresBaseVersion
	default:
		panic(unknownDriver("SetInitialMigrationsOn", driverName))
	}
	if err := insertVersion(ex, "appmigrations", driverName, up); err != nil {
		return err
	}
	if err := createWispTable(ex, driverName); err != nil {
		return err
	}
	if w > 0 {
		return insertVersion(ex, WispTable, driverName, w)
	}
	return nil
}

// Migrate runs upstream's V1 to V17 that db has not, recording each in
// appmigrations, then the wispMigrations it has not; see WispTable.
func Migrate(db *datastore) error {
	var err error
	isPostgres := false
	switch db.driverName {
	case driverMySQL, driverSQLite:
	case driverPostgres:
		isPostgres = true
	default:
		unsupportedDriver("Migrate", db.driverName)
	}

	if !db.tableExists("appmigrations") {
		if isPostgres {
			return fmt.Errorf("no appmigrations table: a Postgres database must be created with `writefreely db init`, which starts it at wisp_v%d", PostgresBaseVersion)
		}
		log.Info("Initializing appmigrations table...")
		_, err = db.Exec(`CREATE TABLE appmigrations (
			version ` + db.typeInt() + ` NOT NULL,
			migrated ` + db.typeDateTime() + ` NOT NULL,
			result ` + db.typeText() + ` NOT NULL
		) ` + db.engine() + `;`)
		if err != nil {
			return err
		}
	}
	up, err := db.maxVersion("appmigrations")
	if err != nil {
		return err
	}
	w, converted, err := db.wispVersion(up)
	if err != nil {
		return err
	}

	if isPostgres && (up < upstreamBaseVersion || w < PostgresBaseVersion) {
		return fmt.Errorf("database is at V%d and wisp_v%d, but Postgres databases start at V%d and wisp_v%d: migrations before those are MySQL and SQLite only. Create the database with `writefreely db init`", up, w, upstreamBaseVersion, PostgresBaseVersion)
	}

	if !converted {
		if err = convertToWispTable(db, w); err != nil {
			return err
		}
	}

	ran := false
	for v := up + 1; v <= upstreamBaseVersion; v++ {
		m := migrations[v-1]
		log.Info("Migrating to V%d: %s", v, m.Description())
		if err = m.Migrate(db); err != nil {
			return err
		}
		if err = insertVersion(db, "appmigrations", db.driverName, v); err != nil {
			return err
		}
		ran = true
	}
	for v := w + 1; v <= len(wispMigrations); v++ {
		m := wispMigrations[v-1]
		log.Info("Migrating to wisp_v%d: %s", v, m.Description())
		if err = m.Migrate(db); err != nil {
			return err
		}
		if err = recordWispVersion(db, v); err != nil {
			return err
		}
		ran = true
	}
	if !ran {
		log.Info("Database up-to-date. No migrations to run.")
	}

	return nil
}

func (db *datastore) tableExists(t string) bool {
	var exists bool
	var err error
	switch db.driverName {
	case driverSQLite:
		var dummy string
		err = db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", t).Scan(&dummy)
		exists = err == nil
	case driverMySQL:
		var dummy string
		err = db.QueryRow("SHOW TABLES LIKE '" + t + "'").Scan(&dummy)
		exists = err == nil
	case driverPostgres:
		// The same query as postgresDialect.TableExists in the writefreely
		// package, which this package cannot import.
		err = db.QueryRow("SELECT to_regclass(?) IS NOT NULL", t).Scan(&exists)
	default:
		unsupportedDriver("tableExists", db.driverName)
	}
	switch {
	case err == sql.ErrNoRows:
		return false
	case err != nil:
		log.Error("Couldn't check whether table %s exists: %v", t, err)
		return false
	}
	return exists
}
