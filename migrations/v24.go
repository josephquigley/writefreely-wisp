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
