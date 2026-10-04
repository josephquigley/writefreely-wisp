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

// lowerEmailLanguageIndex indexes the case-insensitive subscriber-email and
// language-code lookups, as V22 did the remote-handle one.
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
// Per engine, as V22:
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
//     key is 1020 bytes, past what a COMPACT table allows (see V20).
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
