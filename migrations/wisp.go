/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package migrations

import (
	"fmt"

	"github.com/writeas/web-core/log"
)

// WispTable records which of wispMigrations a database has applied.
//
// Upstream numbers its migrations by their position in its own list and
// records them in appmigrations. Had wisp kept appending to that list, the
// first migration upstream added after V17 would have collided with wisp's
// V18 on every merge and, worse, would never have run on a database already
// past V18. So upstream's list and its vN.go files stay exactly as upstream
// has them, and the two are versioned apart:
//
//   - Upstream's V1 to V17 (upstreamBaseVersion) run first, as upstream runs
//     them, recorded in appmigrations alone. That stretch never grows.
//   - Then wispMigrations, recorded in WispTable as wisp_v1, wisp_v2, … It
//     holds wisp's own migrations (wisp_vN.go) and, from now on, an entry
//     for each upstream migration after V17.
//
// appmigrations stays at upstream's version number: an entry that is an
// upstream migration records its upstream version there too, and wisp's own
// record nothing. Upstream WriteFreely installed over this edition therefore
// reads the version it would have reached itself.
const WispTable = "wisp_migrations"

// upstreamBaseVersion is the last upstream migration that runs before
// wispMigrations. It is fixed: upstream migrations after it are entries in
// wispMigrations.
const upstreamBaseVersion = 17

// lastSharedVersion is the newest version any release recorded in
// appmigrations before WispTable existed: V18, which is wisp_v1. V19 to V24
// were only ever recorded by unreleased develop builds.
//
// appmigrations' V18 is not always wisp's: once upstream has a V18 of its
// own, a database coming from upstream can record it too. wispVersion tells
// them apart by what wisp's V18 did, creating wispV1Table, and refuses
// upstream's, because nothing yet records an upstream migration past V17
// as already run. The first wispMigrations entry for one has to.
const lastSharedVersion = 18

// wispV1Table is the table wisp_v1, formerly appmigrations' V18, creates.
const wispV1Table = "post_images"

// PostgresBaseVersion is the wisp version postgres.sql describes, with
// upstream's V1 to V17 beneath it. A Postgres database is created at it by
// `db init` and never runs anything older.
const PostgresBaseVersion = 1

type wispMigration struct {
	Migration
	// upstream is this migration's version in upstream's list, or 0 for one
	// of wisp's own.
	upstream int
}

func wispOnly(description string, fn func(db *datastore) error) wispMigration {
	return wispMigration{New(description, fn), 0}
}

// wispMigrations is the list Migrate runs after upstream's V1 to V17; a
// migration's version is its position in it.
//
// When an upstream merge adds to upstream's list, add an entry at the end
// for each new migration: wispMigration{migrations[n-1], n} if it is
// correct on MySQL, SQLite and Postgres as written, or
// wispMigration{New(...), n} with a wisp_vN.go version of it if it is not,
// which is usual, as upstream has no Postgres. An upstream migration wisp
// must not run still gets an entry, with a function that does nothing and
// says why, so that appmigrations stays in step.
// TestEveryUpstreamMigrationHasAWispEntry fails until each has one.
var wispMigrations = []wispMigration{
	wispOnly("support post images", supportPostImages),                             // wisp_v1 (v0.18.5+wisp)
	wispOnly("change columns that hold data", wispV2),                              // wisp_v2
	wispOnly("add settings tables, lookup indexes and publish job claims", wispV3), // wisp_v3
}

// runEach runs fns in order and stops at the first error.
func runEach(db *datastore, fns ...func(db *datastore) error) error {
	for _, fn := range fns {
		if err := fn(db); err != nil {
			return err
		}
	}
	return nil
}

// UpstreamVer returns the upstream version a fully migrated database has
// reached, which is what its appmigrations records.
func UpstreamVer() int {
	for i := len(wispMigrations) - 1; i >= 0; i-- {
		if u := wispMigrations[i].upstream; u > 0 {
			return u
		}
	}
	return upstreamBaseVersion
}

// DatabaseVersion returns the wisp version db is at. A database no release
// with WispTable has migrated yet is read from appmigrations.
func DatabaseVersion(db *datastore) (int, error) {
	up, err := db.upstreamVersion()
	if err != nil {
		return 0, err
	}
	w, _, err := db.wispVersion(up)
	return w, err
}

// PendingMigrations returns how many migrations Migrate would run on db.
func PendingMigrations(db *datastore) (int, error) {
	up, err := db.upstreamVersion()
	if err != nil {
		return 0, err
	}
	w, _, err := db.wispVersion(up)
	if err != nil {
		return 0, err
	}
	n := len(wispMigrations) - w
	if up < upstreamBaseVersion {
		n += upstreamBaseVersion - up
	}
	return n, nil
}

// upstreamVersion returns the version appmigrations records, 0 if it does
// not exist.
func (db *datastore) upstreamVersion() (int, error) {
	if !db.tableExists("appmigrations") {
		return 0, nil
	}
	return db.maxVersion("appmigrations")
}

// wispVersion returns the wisp version db is at, and whether WispTable
// records it. When it does not, the version comes from up, appmigrations'
// version, and Migrate converts it before going on.
func (db *datastore) wispVersion(up int) (int, bool, error) {
	if db.tableExists(WispTable) {
		v, err := db.maxVersion(WispTable)
		if err != nil || v > 0 {
			return v, true, err
		}
		// Empty: either nothing of wisp's has run yet, or a conversion
		// created the table and stopped before filling it. appmigrations
		// still says which.
	}
	switch {
	case up <= upstreamBaseVersion:
		return 0, false, nil
	case !db.tableExists(wispV1Table):
		// Past V17 without wisp's V18 having run: upstream wrote this.
		return 0, false, errUpstreamPastBase(up)
	case up > lastSharedVersion:
		return 0, false, errUnreleasedVersion(up)
	}
	return 1, false, nil
}

func (db *datastore) maxVersion(table string) (int, error) {
	var v int
	// MAX is NULL on an empty table.
	err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM " + table).Scan(&v)
	return v, err
}

// createWispTable creates WispTable, with appmigrations' columns, unless it
// exists.
func createWispTable(ex Execer, driverName string) error {
	d := &datastore{driverName: driverName}
	_, err := ex.Exec(`CREATE TABLE IF NOT EXISTS ` + WispTable + ` (
		version ` + d.typeInt() + ` NOT NULL,
		migrated ` + d.typeDateTime() + ` NOT NULL,
		result ` + d.typeText() + ` NOT NULL
	)` + d.engine())
	return err
}

// convertToWispTable gives a database that appmigrations alone versions
// its WispTable. At w 1, appmigrations' V18 becomes wisp_v1, and
// appmigrations goes back to upstream's V17.
//
// MySQL commits CREATE TABLE on its own, so the table can exist empty if
// this stops part way; wispVersion reads an empty table from
// appmigrations, and the next start converts again. The row moves in one
// transaction.
func convertToWispTable(db *datastore, w int) error {
	if err := createWispTable(db, db.driverName); err != nil {
		return err
	}
	if w == 0 {
		return nil
	}
	log.Info("Moving appmigrations V%d to %s as wisp_v1...", lastSharedVersion, WispTable)
	t, err := db.Begin()
	if err != nil {
		return err
	}
	defer t.Rollback()
	if _, err = t.Exec("INSERT INTO "+WispTable+" (version, migrated, result) SELECT 1, migrated, result FROM appmigrations WHERE version = ?", lastSharedVersion); err != nil {
		return err
	}
	if _, err = t.Exec("DELETE FROM appmigrations WHERE version > ?", upstreamBaseVersion); err != nil {
		return err
	}
	// A Postgres database recorded only its V18 base, which is now gone, so
	// it records upstream's V17 for the first time.
	var have int
	if err = t.QueryRow("SELECT COALESCE(MAX(version), 0) FROM appmigrations").Scan(&have); err != nil {
		return err
	}
	if have < upstreamBaseVersion {
		if err = insertVersion(t, "appmigrations", db.driverName, upstreamBaseVersion); err != nil {
			return err
		}
	}
	return t.Commit()
}

func insertVersion(ex Execer, table, driverName string, v int) error {
	d := &datastore{driverName: driverName}
	_, err := ex.Exec("INSERT INTO "+table+" (version, migrated, result) VALUES (?, "+d.now()+", ?)", v, "")
	return err
}

// recordWispVersion records that wisp_v(v) has run, in WispTable, and in
// appmigrations under its upstream version if it has one.
func recordWispVersion(db *datastore, v int) error {
	t, err := db.Begin()
	if err != nil {
		return err
	}
	defer t.Rollback()
	if err = insertVersion(t, WispTable, db.driverName, v); err != nil {
		return err
	}
	if up := wispMigrations[v-1].upstream; up > 0 {
		if err = insertVersion(t, "appmigrations", db.driverName, up); err != nil {
			return err
		}
	}
	return t.Commit()
}

// errUnreleasedVersion refuses a database whose appmigrations records a
// version no release has: one a develop build migrated before WispTable.
func errUnreleasedVersion(v int) error {
	return fmt.Errorf("appmigrations records V%d, which no release of this edition has ever had: the database was migrated by a develop build before migrations moved to %s, and its version cannot be read. Recreate it with `writefreely db init`, or restore one a release migrated", v, WispTable)
}

// errUpstreamPastBase refuses a database upstream WriteFreely migrated past
// V17, which this edition cannot yet take over.
func errUpstreamPastBase(v int) error {
	return fmt.Errorf("appmigrations records upstream's V%d, but this edition takes over from upstream WriteFreely only at V%d or earlier: %s is missing, so V%d is upstream's own migration, not this edition's former V%d", v, upstreamBaseVersion, wispV1Table, v, lastSharedVersion)
}
