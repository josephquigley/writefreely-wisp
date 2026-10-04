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
// # Rule for V19 and later
//
// Every migration added from V19 on runs on MySQL, SQLite and Postgres, and
// must be correct on all three. Write column types with the helpers in
// drivers.go (typeInt, typeVarChar, typeBool, typeDateTime, …), never as a
// literal type, and put any statement that cannot be written portably in a
// `switch db.driverName` with a case for each engine and a default that
// panics. Test it against all three before merging.
//
// Migrations V1 to V18 predate Postgres support and are MySQL and SQLite
// only. A Postgres database is created by `writefreely db init` from
// postgres.sql, which already describes the V18 schema, and starts at
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

// errBeforePostgresBase is returned by a migration from before
// PostgresBaseVersion if it is ever asked to run on Postgres. Migrate refuses
// before it gets that far; this is the second line of defence.
func errBeforePostgresBase(fn string) error {
	return fmt.Errorf("migrations.%s: predates V%d and must not run on Postgres", fn, PostgresBaseVersion)
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
	New("support post images", supportPostImages),                    // V17 -> V18
	New("case-insensitive subscriber email", subscriberEmailCase),    // V18 -> V19
	New("exact-match collations on MySQL", exactMatchCollations),     // V19 -> V20
	New("store settings in the database", supportAppSettings),        // V20 -> V21
	New("index remote handle lookups", remoteHandleIndex),            // V21 -> V22
	New("index email and language lookups", lowerEmailLanguageIndex), // V22 -> V23
}

// CurrentVer returns the current migration version the application is on
func CurrentVer() int {
	return len(migrations)
}

// PostgresBaseVersion is the migration version postgres.sql describes. A
// Postgres database starts here: migrations up to and including it never
// run on Postgres. It never changes once Postgres databases exist.
const PostgresBaseVersion = 18

// InitialVersion returns the migration version that the schema file `db
// init` loads for driverName describes: V1 for schema.sql and sqlite.sql,
// PostgresBaseVersion for postgres.sql.
func InitialVersion(driverName string) int {
	switch driverName {
	case driverMySQL, driverSQLite:
		return 1
	case driverPostgres:
		return PostgresBaseVersion
	}
	panic(unknownDriver("InitialVersion", driverName))
}

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// SetInitialMigrations records, in a freshly created schema, the migration
// version that schema is at; see InitialVersion.
func SetInitialMigrations(db *datastore) error {
	return SetInitialMigrationsOn(db, db.driverName)
}

// SetInitialMigrationsOn is SetInitialMigrations through ex, which may be a
// transaction that also created the schema.
func SetInitialMigrationsOn(ex Execer, driverName string) error {
	d := &datastore{driverName: driverName}
	_, err := ex.Exec("INSERT INTO appmigrations (version, migrated, result) VALUES (?, "+d.now()+", ?)", InitialVersion(driverName), "")
	return err
}

func Migrate(db *datastore) error {
	var version int
	var err error
	isPostgres := false
	switch db.driverName {
	case driverMySQL, driverSQLite:
	case driverPostgres:
		isPostgres = true
	default:
		unsupportedDriver("Migrate", db.driverName)
	}

	if db.tableExists("appmigrations") {
		// MAX is NULL on an empty table.
		err = db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM appmigrations").Scan(&version)
		if err != nil {
			return err
		}
	} else if isPostgres {
		return fmt.Errorf("no appmigrations table: a Postgres database must be created with `writefreely db init`, which starts it at V%d", PostgresBaseVersion)
	} else {
		log.Info("Initializing appmigrations table...")
		version = 0
		_, err = db.Exec(`CREATE TABLE appmigrations (
			version ` + db.typeInt() + ` NOT NULL,
			migrated ` + db.typeDateTime() + ` NOT NULL,
			result ` + db.typeText() + ` NOT NULL
		) ` + db.engine() + `;`)
		if err != nil {
			return err
		}
	}

	if isPostgres && version < PostgresBaseVersion {
		return fmt.Errorf("database is at V%d, but Postgres databases start at V%d: migrations before V%d are MySQL and SQLite only. Create the database with `writefreely db init`", version, PostgresBaseVersion, PostgresBaseVersion+1)
	}

	if len(migrations[version:]) > 0 {
		for i, m := range migrations[version:] {
			curVer := version + i + 1
			log.Info("Migrating to V%d: %s", curVer, m.Description())
			err = m.Migrate(db)
			if err != nil {
				return err
			}

			// Update migrations table
			_, err = db.Exec("INSERT INTO appmigrations (version, migrated, result) VALUES (?, "+db.now()+", ?)", curVer, "")
			if err != nil {
				return err
			}
		}
	} else {
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
