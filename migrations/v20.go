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
// typeInt is wide enough for the counter: it moves once per admin save.
func supportAppSettings(db *datastore) error {
	t, err := db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`CREATE TABLE app_settings (
    name  ` + db.typeVarChar(64) + ` NOT NULL,
    value ` + db.typeText() + ` NOT NULL,
    PRIMARY KEY (name)
)` + db.engine(),
		`CREATE TABLE app_settings_version (
    id      ` + db.typeInt() + ` NOT NULL,
    version ` + db.typeInt() + ` NOT NULL,
    PRIMARY KEY (id)
)` + db.engine(),
		`INSERT INTO app_settings_version (id, version) VALUES (1, 0)`,
	} {
		if _, err = t.Exec(q); err != nil {
			t.Rollback()
			return err
		}
	}
	return t.Commit()
}
