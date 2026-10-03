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

// supportAppSettings (V21) adds the tables that hold community-policy settings,
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
// and no record that V21 ran; the tables are created only if absent and the
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
