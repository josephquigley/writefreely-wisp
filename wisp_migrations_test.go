/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/migrations"
)

// Upstream's V1 to V17 are versioned in appmigrations, and wisp's
// migrations after them in wisp_migrations (migrations/wisp.go).
// appmigrations is kept at upstream's own version number, so that upstream
// WriteFreely reads a database this edition migrated correctly.

func maxMigrationVersion(t *testing.T, app *App, table string) int {
	t.Helper()
	var v int
	require.NoError(t, app.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM "+table).Scan(&v))
	return v
}

func countMigrationRows(t *testing.T, app *App, table string) int {
	t.Helper()
	var n int
	require.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

// rewindToAppMigrations puts a database back to how a release before the
// wisp_migrations table left it: no wisp_migrations, and appmigrations
// holding versions up to ver in the single numbering wisp then shared with
// upstream. That was every version from V1 on MySQL and SQLite, and V18
// alone on Postgres, which `db init` started there.
func rewindToAppMigrations(t *testing.T, app *App, ver int) {
	t.Helper()
	for _, q := range []string{"DROP TABLE " + migrations.WispTable, "DELETE FROM appmigrations"} {
		_, err := app.db.Exec(q)
		require.NoError(t, err, q)
	}
	first := 1
	if app.db.driverName == driverPostgres {
		first = 18
	}
	for v := first; v <= ver; v++ {
		_, err := app.db.Exec("INSERT INTO appmigrations (version, migrated, result) VALUES (?, CURRENT_TIMESTAMP, '')", v)
		require.NoError(t, err)
	}
}

func TestWispMigrationsRecordedOnInit(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		assert.Equal(t, migrations.CurrentVer(), maxMigrationVersion(t, app, migrations.WispTable))
		assert.Equal(t, migrations.UpstreamVer(), maxMigrationVersion(t, app, "appmigrations"),
			"appmigrations records upstream's version, not wisp's")

		v, err := migrations.DatabaseVersion(migrations.NewDatastore(app.db.DB, app.db.driverName))
		require.NoError(t, err)
		assert.Equal(t, migrations.CurrentVer(), v)
	})
}

func TestWispMigrationsConvertFromAppMigrations(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		// The released V18, as the blog's database holds it. Its schema is
		// already past V18, so the later migrations also show they can run
		// again.
		rewindToAppMigrations(t, app, 18)

		mdb := migrations.NewDatastore(app.db.DB, app.db.driverName)
		v, err := migrations.DatabaseVersion(mdb)
		require.NoError(t, err)
		assert.Equal(t, 1, v, "before conversion, appmigrations' V18 reads as wisp_v1")

		require.NoError(t, migrations.Migrate(mdb))

		assert.Equal(t, migrations.CurrentVer(), maxMigrationVersion(t, app, migrations.WispTable))
		assert.Equal(t, migrations.CurrentVer(), countMigrationRows(t, app, migrations.WispTable),
			"V18 is carried over as wisp_v1, then one row per migration run")
		assert.Equal(t, migrations.UpstreamVer(), maxMigrationVersion(t, app, "appmigrations"),
			"V18 was wisp's own, so appmigrations goes back to upstream's V17")

		// A second run changes nothing.
		require.NoError(t, migrations.Migrate(mdb))
		assert.Equal(t, migrations.CurrentVer(), countMigrationRows(t, app, migrations.WispTable))
	})
}

// A start interrupted after creating wisp_migrations but before filling it
// converts on the next one, rather than reading the empty table as V0.
func TestWispConversionFinishesAfterInterruption(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		rewindToAppMigrations(t, app, 18)
		dt := "DATETIME"
		if app.db.driverName == driverPostgres {
			dt = "timestamptz"
		}
		_, err := app.db.Exec("CREATE TABLE " + migrations.WispTable + " (version INT NOT NULL, migrated " + dt + " NOT NULL, result TEXT NOT NULL)")
		require.NoError(t, err)

		require.NoError(t, migrations.Migrate(migrations.NewDatastore(app.db.DB, app.db.driverName)))
		assert.Equal(t, migrations.CurrentVer(), maxMigrationVersion(t, app, migrations.WispTable))
		assert.Equal(t, migrations.CurrentVer(), countMigrationRows(t, app, migrations.WispTable))
		assert.Equal(t, migrations.UpstreamVer(), maxMigrationVersion(t, app, "appmigrations"))
	})
}

// V19 to V24 in appmigrations came from develop builds that were never
// released, and their numbers now mean other migrations. Such a database is
// refused rather than guessed at.
func TestWispConversionRefusesUnreleasedVersions(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		rewindToAppMigrations(t, app, 19)
		err := migrations.Migrate(migrations.NewDatastore(app.db.DB, app.db.driverName))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no release")
		exists, err := app.db.dialectOrDefault().TableExists(context.Background(), app.db.DB, migrations.WispTable)
		require.NoError(t, err)
		assert.False(t, exists, "a refused conversion leaves nothing behind")
	})
}

// A database upstream WriteFreely migrated to its V17 runs every wisp
// migration from wisp_v1, and upstream's versions stay where they are.
func TestWispMigrationsFromUpstreamDatabase(t *testing.T) {
	for _, tc := range []struct {
		name string
		app  func(t *testing.T) *App
	}{
		{"sqlite", func(t *testing.T) *App { return newHandleTestAppOn(t, openSQLiteAppTestDB) }},
		{"mysql", func(t *testing.T) *App { return newMySQLTestApp(t, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.app(t)
			rewindToAppMigrations(t, app, 17)
			_, err := app.db.Exec("DROP TABLE post_images")
			require.NoError(t, err)

			require.NoError(t, migrations.Migrate(migrations.NewDatastore(app.db.DB, app.db.driverName)))
			assert.Equal(t, migrations.CurrentVer(), countMigrationRows(t, app, migrations.WispTable))
			assert.Equal(t, 17, countMigrationRows(t, app, "appmigrations"))
			exists, err := app.db.dialectOrDefault().TableExists(context.Background(), app.db.DB, "post_images")
			require.NoError(t, err)
			assert.True(t, exists, "wisp_v1 ran")
		})
	}
}

// appmigrations' V18 is wisp's own only where wisp's V18 ran, which is
// where post_images exists. Without it, V18 was written by upstream
// WriteFreely, whose own V18 is a different migration: it must not be taken
// for wisp_v1, and this edition does not yet handle upstream past V17.
func TestUpstreamV18IsNotTakenForWispV1(t *testing.T) {
	for _, tc := range []struct {
		name string
		app  func(t *testing.T) *App
	}{
		{"sqlite", func(t *testing.T) *App { return newHandleTestAppOn(t, openSQLiteAppTestDB) }},
		{"mysql", func(t *testing.T) *App { return newMySQLTestApp(t, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.app(t)
			rewindToAppMigrations(t, app, 18)
			_, err := app.db.Exec("DROP TABLE post_images")
			require.NoError(t, err)

			mdb := migrations.NewDatastore(app.db.DB, app.db.driverName)
			err = migrations.Migrate(mdb)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "upstream")
			exists, err := app.db.dialectOrDefault().TableExists(context.Background(), app.db.DB, migrations.WispTable)
			require.NoError(t, err)
			assert.False(t, exists, "nothing is converted")
			assert.Equal(t, 18, maxMigrationVersion(t, app, "appmigrations"), "upstream's version is left alone")
		})
	}
}
