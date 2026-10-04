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

// wispV2 changes columns that already hold data. Develop numbered its two
// parts V19 and V20 before any release carried them; they were collapsed
// into one migration when wisp moved to its own version table.
//
// Each part can run again, so a start that stops partway through finishes
// on the next one.
func wispV2(db *datastore) error {
	return runEach(db, subscriberEmailCase, exactMatchCollations)
}

// subscriberEmailCase makes an email subscriber unique per blog
// regardless of the case of the address.
//
// The application stores subscriber addresses lower-cased and compares
// LOWER(email) when it looks one up (normalizeSubscriberEmail in the
// writefreely package). This is the database's half of that, belt and
// braces against a writer that forgets:
//
//   - MySQL: nothing to do. emailsubscribers.email has a case-insensitive
//     collation, so the existing eu_coll_email key already treats Foo@x and
//     foo@x as one address.
//   - SQLite: nothing is changed. A unique index on lower(email) would fail
//     to build on an existing database that already holds two rows differing
//     only in case, and that would stop the instance from starting. Those
//     rows stay as they are; the application's lower-cased lookups find them,
//     and it no longer writes new mixed-case ones.
//   - Postgres: eu_coll_email is replaced with a unique index on
//     (collection_id, lower(email)). A Postgres database is new — created by
//     `db init`, or filled by the data copy, which lower-cases addresses and
//     collapses case duplicates — so there is nothing for the index to trip
//     over. It also serves the application's LOWER(email) lookups.
func subscriberEmailCase(db *datastore) error {
	switch db.driverName {
	case driverMySQL, driverSQLite:
		return nil
	case driverPostgres:
		t, err := db.Begin()
		if err != nil {
			return err
		}
		for _, q := range []string{
			`ALTER TABLE emailsubscribers DROP CONSTRAINT IF EXISTS eu_coll_email`,
			`CREATE UNIQUE INDEX IF NOT EXISTS eu_coll_lower_email ON emailsubscribers (collection_id, lower(email))`,
		} {
			if _, err = t.Exec(q); err != nil {
				t.Rollback()
				return err
			}
		}
		return t.Commit()
	default:
		unsupportedDriver("subscriberEmailCase", db.driverName)
	}
	return nil
}

// exactMatchCollations makes MySQL compare tokens, codes and remote IRIs
// exactly, as SQLite and Postgres already do.
//
// schema.sql creates its tables as latin1, whose default collation
// (latin1_swedish_ci) ignores case, and tables added by later migrations
// inherit the database's default, which is case-insensitive too on every
// stock MySQL and MariaDB. So on MySQL alone:
//
//   - a token, invite code, OAuth state or subscriber ID matched in any case;
//   - an OAuth provider's user ID matched another user's that differed only
//     in case;
//   - two remote actors whose IRIs differed only in case collided on the
//     remoteusers unique key, and two image IDs on the post_images key;
//   - remoteusers could not store any character outside latin1, so an actor
//     with one in its inbox or profile URL failed to save under strict mode
//     and was mangled to '?' without it.
//
// Each column below keeps its type, nullability and default and gets a
// binary collation. remoteusers and remoteuserkeys hold IRIs and are
// converted to utf8mb4 as well. Converting latin1 to utf8mb4 is lossless,
// and a binary collation is stricter than the case-insensitive one it
// replaces, so no existing row can collide under a unique key afterwards.
//
// Columns that are meant to ignore case keep their collation:
// emailsubscribers.email (see subscriberEmailCase) and posts.slug, which the application
// normalises itself. remoteusers.handle does not keep it: CONVERT TO changes
// every string column of the table, so the handle becomes binary along with
// the rest. That is safe because handles are stored lower-cased and looked up
// as WHERE LOWER(handle) = ? (on MySQL, through the column remoteHandleIndex in wisp_v3 stores that
// expression in), which matches either way. A new query that
// compares it as plain handle = ? would be case-sensitive, so lower-case the
// argument or keep using LOWER().
//
// MySQL commits each ALTER on its own, so there is no transaction to roll
// back. Every statement states the column's whole definition and can run
// again, so a migration that stops part way through is finished by running
// it again.
//
// Three of those columns carry a unique or primary key of VARCHAR(255):
// oauth_client_states.state, remoteusers.actor_id and remoteuserkeys.id. In
// utf8mb4 each key is 1020 bytes, past the 767 a COMPACT or REDUNDANT table
// allows. A database whose default charset is latin1 (older installs, and
// anything created before tables were utf8mb4) holds them as 255-byte keys
// and would fail partway with error 1071 or 1709, so the three statements
// that widen them also set ROW_FORMAT=DYNAMIC, which allows keys up to 3072
// bytes. Setting a table option beside the column changes is one ALTER, and
// setting it on a table that already has it changes nothing, so a re-run
// stays harmless.
//
// DYNAMIC with long keys needs MySQL 5.7.9 or MariaDB 10.2.2 or later, where
// innodb_large_prefix and the Barracuda file format are on (they are the
// defaults there, and MySQL 8.0 removed the settings). MySQL 5.6 and MariaDB
// 10.1 only qualify with innodb_file_format=Barracuda, innodb_file_per_table
// and innodb_large_prefix all turned on, and are not supported otherwise.
//
// SQLite and Postgres already compare exactly, and have nothing to change.
func exactMatchCollations(db *datastore) error {
	switch db.driverName {
	case driverSQLite, driverPostgres:
		return nil
	case driverMySQL:
	default:
		unsupportedDriver("exactMatchCollations", db.driverName)
	}

	for _, q := range []string{
		`ALTER TABLE userinvites MODIFY id CHAR(6) CHARACTER SET latin1 COLLATE latin1_bin NOT NULL`,
		`ALTER TABLE usersinvited MODIFY invite_id CHAR(6) CHARACTER SET latin1 COLLATE latin1_bin NOT NULL`,
		`ALTER TABLE posts MODIFY modify_token CHAR(32) CHARACTER SET latin1 COLLATE latin1_bin NULL DEFAULT NULL`,
		`ALTER TABLE password_resets MODIFY token CHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL`,
		`ALTER TABLE emailsubscribers
			MODIFY id CHAR(8) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
			MODIFY token CHAR(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL`,
		`ALTER TABLE oauth_client_states
			ROW_FORMAT=DYNAMIC,
			MODIFY state VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
			MODIFY invite_code CHAR(6) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL DEFAULT NULL`,
		`ALTER TABLE oauth_users MODIFY remote_user_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL`,
		`ALTER TABLE post_images MODIFY id VARCHAR(6) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL`,
		`ALTER TABLE remoteusers CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_bin, ROW_FORMAT=DYNAMIC`,
		`ALTER TABLE remoteuserkeys CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_bin, ROW_FORMAT=DYNAMIC`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
