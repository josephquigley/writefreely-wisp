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
//     own binary collation (V20), so the comparison means exactly what
//     LOWER(handle) = ? meant. It is STORED rather than VIRTUAL because
//     indexes on virtual columns need a newer MariaDB than the 10.2.2 this
//     edition supports.
//
// Emails and language codes were left out here and are indexed by V23, the
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
