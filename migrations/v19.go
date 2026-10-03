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
			`ALTER TABLE emailsubscribers DROP CONSTRAINT eu_coll_email`,
			`CREATE UNIQUE INDEX eu_coll_lower_email ON emailsubscribers (collection_id, lower(email))`,
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
