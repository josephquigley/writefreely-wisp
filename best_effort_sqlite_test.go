//go:build sqlite

/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bestEffortTestApp(t *testing.T) *App {
	sdb, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "be.db")+"?parseTime=true")
	require.NoError(t, err)
	t.Cleanup(func() { sdb.Close() })
	cfg := txTestConfig()
	cfg.Database.Type = driverSQLite
	app := &App{cfg: cfg, db: newDatastore(sdb, driverSQLite)}
	require.NoError(t, adminInitDatabase(app))
	return app
}

// A failing best-effort statement is swallowed: execBestEffort returns nil,
// its effects are undone, and the transaction carries on and commits.
func TestExecBestEffortSwallowsStatementError(t *testing.T) {
	app := bestEffortTestApp(t)

	tx, err := app.db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO collectionredirects (prev_alias, new_alias) VALUES (?, ?)", "kept", "x")
	require.NoError(t, err)
	assert.NoError(t, execBestEffort(tx, "be_fail", "INSERT INTO no_such_table (a) VALUES (1)"))
	require.NoError(t, tx.Commit())

	assert.Equal(t, "x", app.db.GetCollectionRedirect("kept"))
}

// When a best-effort statement takes the whole transaction down with it
// (MySQL does this on a deadlock; here a trigger's RAISE(ROLLBACK) does the
// same on SQLite), ROLLBACK TO SAVEPOINT fails and the rename must be
// reported as failed, with nothing left behind by later statements running
// in autocommit.
func TestChangeSettingsAbortsWhenBestEffortBreaksTx(t *testing.T) {
	app := bestEffortTestApp(t)
	u, _ := txTestUser(t, app, "oldname")

	_, err := app.db.Exec("CREATE TRIGGER be_break BEFORE DELETE ON collectionredirects BEGIN SELECT RAISE(ROLLBACK, 'forced'); END")
	require.NoError(t, err)
	// The DELETE only fires the trigger if a row matches.
	_, err = app.db.Exec("INSERT INTO collectionredirects (prev_alias, new_alias) VALUES (?, ?)", "newname", "elsewhere")
	require.NoError(t, err)

	assert.Error(t, app.db.ChangeSettings(app, u, &userSettings{Username: "newname"}))
	assert.Equal(t, "oldname", u.Username)

	var name string
	require.NoError(t, app.db.QueryRow("SELECT username FROM users WHERE id = ?", u.ID).Scan(&name))
	assert.Equal(t, "oldname", name)
	assert.Equal(t, "", app.db.GetCollectionRedirect("oldname"), "no redirect may be written outside the rolled-back rename")
}
