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

// wispV3 only adds: the settings tables, the case-insensitive lookup
// indexes and the publish-job claim column. Develop numbered its four parts
// V21 to V24 before any release carried them; they were collapsed into one
// migration when wisp moved to its own version table.
//
// Each part can run again, so a start that stops partway through finishes
// on the next one.
func wispV3(db *datastore) error {
	return runEach(db, supportAppSettings, remoteHandleIndex, lowerEmailLanguageIndex, publishJobClaims)
}

// supportAppSettings adds the tables that hold community-policy settings,
// so that every node sharing a database reads the same ones. config.ini
// keeps only bootstrap values; see config/settings.go for which is which.
//
// app_settings_version holds one row, id 1. Its version is bumped in the
// same transaction as every write to app_settings, and each node compares
// it with the version of its cached settings before every request. 0
// means nothing has been imported or saved yet; the first node to move it
// to 1 imports its config.ini.
//
// It is safe to run again. MySQL commits each DDL statement as it goes, so
// an interrupted run can leave the tables, with or without the version row,
// and no record that wisp_v3 ran; the tables are created only if absent and the
// row is inserted only if absent, so a version already moved past 0 is never
// reset.
//
// typeInt is wide enough for the counter: it moves once per admin save.
//
// On MySQL both tables are created utf8mb4 with a binary collation, rather
// than inheriting the database default, which may be latin1. A value can be
// any Unicode (a site name, a description), and a setting's name is a key
// that must match exactly. The VARCHAR(64) key is 256 bytes in utf8mb4.
func supportAppSettings(db *datastore) error {
	var opts, seed string
	switch db.driverName {
	case driverSQLite:
		seed = `INSERT OR IGNORE INTO app_settings_version (id, version) VALUES (1, 0)`
	case driverPostgres:
		seed = `INSERT INTO app_settings_version (id, version) VALUES (1, 0) ON CONFLICT (id) DO NOTHING`
	case driverMySQL:
		opts = " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
		seed = `INSERT IGNORE INTO app_settings_version (id, version) VALUES (1, 0)`
	default:
		unsupportedDriver("supportAppSettings", db.driverName)
	}
	t, err := db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS app_settings (
    name  ` + db.typeVarChar(64) + ` NOT NULL,
    value ` + db.typeText() + ` NOT NULL,
    PRIMARY KEY (name)
)` + db.engine() + opts,
		`CREATE TABLE IF NOT EXISTS app_settings_version (
    id      ` + db.typeInt() + ` NOT NULL,
    version ` + db.typeInt() + ` NOT NULL,
    PRIMARY KEY (id)
)` + db.engine() + opts,
		seed,
	} {
		if _, err = t.Exec(q); err != nil {
			t.Rollback()
			return err
		}
	}
	return t.Commit()
}

// remoteHandleIndex indexes the remote-handle lookup.
//
// Every @mention in a post and every blog's reply delegate is resolved
// through getRemoteUserFromHandle each time the post's ActivityPub object is
// built, which is on every federated delivery and every fetch of it. That
// query compares LOWER(handle) (remoteUserByHandleQuery in the writefreely
// package), and remoteusers, which holds every remote actor the instance has
// seen, had no index on handle at all, so each lookup read the whole table.
//
//   - SQLite and Postgres: an index on the expression lower(handle), which
//     the planner matches against the query's LOWER(handle) as written.
//   - MySQL: MariaDB cannot index an expression, and MySQL only can from
//     8.0.13. Both index a generated column, so remoteusers gains
//     handle_lower, stored as LOWER(handle), with an index on it, and the
//     MySQL lookup compares handle_lower instead. The column has handle's
//     own binary collation (wisp_v2), so the comparison means exactly what
//     LOWER(handle) = ? meant. It is STORED rather than VIRTUAL because
//     indexes on virtual columns need a newer MariaDB than the 10.2.2 this
//     edition supports.
//
// Emails and language codes were left out here and are indexed by lowerEmailLanguageIndex, the
// same way.
//
// Each statement can run again, so a start interrupted before the version is
// recorded finishes on the next one.
func remoteHandleIndex(db *datastore) error {
	switch db.driverName {
	case driverSQLite, driverPostgres:
		_, err := db.Exec(`CREATE INDEX IF NOT EXISTS remoteusers_lower_handle ON remoteusers (lower(handle))`)
		return err
	case driverMySQL:
		// MySQL has no ADD COLUMN IF NOT EXISTS (MariaDB does). The column
		// and its index arrive in one ALTER, so the column alone says
		// whether it has run.
		var n int
		err := db.QueryRow("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'remoteusers' AND COLUMN_NAME = 'handle_lower'").Scan(&n)
		if err != nil || n > 0 {
			return err
		}
		_, err = db.Exec(`ALTER TABLE remoteusers
			ADD COLUMN handle_lower ` + db.typeVarChar(255) + ` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin GENERATED ALWAYS AS (LOWER(handle)) STORED,
			ADD INDEX remoteusers_handle_lower (handle_lower)`)
		return err
	default:
		unsupportedDriver("remoteHandleIndex", db.driverName)
	}
	return nil
}

// lowerEmailLanguageIndex indexes the case-insensitive subscriber-email and
// language-code lookups, as remoteHandleIndex does the remote-handle one.
//
//   - Confirming a subscriber, and checking whether an address is confirmed
//     before a newsletter goes to it, compare LOWER(email) across every blog
//     (subscriberEmailLower in the writefreely package). The only index on
//     email led with collection_id, so both read the whole emailsubscribers
//     table.
//   - A blog's /lang: page and its count compare LOWER(language) within one
//     blog (postLanguageLower). The collection_id index narrowed them to that
//     blog, but every one of its posts was then read to test the language.
//     The new index carries created as well, which those queries filter and
//     order on.
//
// Per engine, as remoteHandleIndex:
//
//   - SQLite and Postgres: indexes on the expressions lower(email) and
//     (collection_id, lower(language), created), which the planner matches
//     against the queries' LOWER(email) and LOWER(language) as written.
//   - MySQL: neither MariaDB nor MySQL before 8.0.13 can index an
//     expression, so each table gains a STORED generated column holding the
//     lower-cased value, with an index on it, and the MySQL queries compare
//     that column instead: emailsubscribers.email_lower and
//     posts.language_lower. Both are utf8mb4 with a binary collation, so the
//     comparison is exact on a value both sides have already lower-cased.
//     language is CHAR(2), and MariaDB refuses a generated column over a
//     CHAR value whose result could depend on PAD_CHAR_TO_FULL_LENGTH, so
//     it is RTRIM()med first; CHAR values come back trimmed anyway.
//     Adding a stored column rebuilds the table, posts included, once.
//     emailsubscribers also gets ROW_FORMAT=DYNAMIC: a VARCHAR(255) utf8mb4
//     key is 1020 bytes, past what a COMPACT table allows (see wisp_v2).
//
// Each statement can run again, so a start interrupted before the version is
// recorded finishes on the next one.
func lowerEmailLanguageIndex(db *datastore) error {
	switch db.driverName {
	case driverSQLite, driverPostgres:
		for _, q := range []string{
			`CREATE INDEX IF NOT EXISTS emailsubscribers_lower_email ON emailsubscribers (lower(email))`,
			`CREATE INDEX IF NOT EXISTS posts_coll_lower_language ON posts (collection_id, lower(language), created)`,
		} {
			if _, err := db.Exec(q); err != nil {
				return err
			}
		}
		return nil
	case driverMySQL:
		// MySQL has no ADD COLUMN IF NOT EXISTS (MariaDB does). Each column
		// and its index arrive in one ALTER, so the column alone says
		// whether that ALTER has run.
		for _, a := range []struct{ table, column, alter string }{
			{"emailsubscribers", "email_lower", `ALTER TABLE emailsubscribers
				ROW_FORMAT=DYNAMIC,
				ADD COLUMN email_lower ` + db.typeVarChar(255) + ` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin GENERATED ALWAYS AS (LOWER(email)) STORED,
				ADD INDEX emailsubscribers_email_lower (email_lower)`},
			{"posts", "language_lower", `ALTER TABLE posts
				ADD COLUMN language_lower ` + db.typeChar(2) + ` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin GENERATED ALWAYS AS (LOWER(RTRIM(language))) STORED,
				ADD INDEX posts_coll_language_lower (collection_id, language_lower, created)`},
		} {
			var n int
			err := db.QueryRow("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?", a.table, a.column).Scan(&n)
			if err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			if _, err = db.Exec(a.alter); err != nil {
				return err
			}
		}
		return nil
	default:
		unsupportedDriver("lowerEmailLanguageIndex", db.driverName)
	}
	return nil
}

// publishJobClaims adds publishjobs.claimed_at, so that a scheduled email
// is sent by exactly one worker.
//
// The publish queue selects its due jobs, sends each and then deletes it.
// The job lock (dialect.TryJobLock in the writefreely package) stops two
// processes running the same tick on MySQL and Postgres, but it is a
// no-op on SQLite, and it is all that stood between two workers that both
// selected a job and both sent it. Now a worker claims each job first, with
// one UPDATE that sets claimed_at only where it is still NULL, and only the
// worker whose UPDATE changed the row sends (ClaimJob). A failed send clears
// the claim, so the next tick retries it.
//
// The column is nullable with no default: every existing job is unclaimed.
//
// SQLite and MySQL have no ADD COLUMN IF NOT EXISTS (MariaDB and Postgres
// do), so the column is looked for first, and a start interrupted before the
// version is recorded finishes on the next one.
func publishJobClaims(db *datastore) error {
	var n int
	var err error
	switch db.driverName {
	case driverSQLite:
		err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('publishjobs') WHERE name = 'claimed_at'").Scan(&n)
	case driverMySQL:
		err = db.QueryRow("SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'publishjobs' AND COLUMN_NAME = 'claimed_at'").Scan(&n)
	case driverPostgres:
		err = db.QueryRow("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'publishjobs' AND column_name = 'claimed_at'").Scan(&n)
	default:
		unsupportedDriver("publishJobClaims", db.driverName)
	}
	if err != nil || n > 0 {
		return err
	}
	_, err = db.Exec(`ALTER TABLE publishjobs ADD COLUMN claimed_at ` + db.typeDateTime() + ` NULL`)
	return err
}
