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
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func ifEmptyTestApp(t *testing.T) *App {
	sdb, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "ifempty.db")+"?parseTime=true")
	require.NoError(t, err)
	t.Cleanup(func() { sdb.Close() })
	cfg := txTestConfig()
	cfg.Database.Type = driverSQLite
	return &App{cfg: cfg, db: newDatastore(sdb, driverSQLite)}
}

// An empty database gets the schema, and a second run leaves it alone
// instead of failing on tables that already exist. This is what lets the
// entrypoint run it on every start.
func TestInitDatabaseIfEmptyCreatesTheSchemaOnce(t *testing.T) {
	app := ifEmptyTestApp(t)

	require.NoError(t, initDatabaseIfEmpty(app))
	require.True(t, app.db.DatabaseInitialized(), "no users table after initializing an empty database")
	_, err := app.db.Exec("INSERT INTO appcontent (id, content, updated) VALUES ('kept', 'x', CURRENT_TIMESTAMP)")
	require.NoError(t, err)

	require.NoError(t, initDatabaseIfEmpty(app))
	var n int
	require.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM appcontent WHERE id = 'kept'").Scan(&n))
	require.Equal(t, 1, n, "a second run changed a database that already had a schema")
}

// A database holding appmigrations but no users is not empty: an old MySQL
// or SQLite database may be part way there, and initializing it would stamp
// it at the latest migration it never ran.
func TestInitDatabaseIfEmptyLeavesAPartialSchemaAlone(t *testing.T) {
	app := ifEmptyTestApp(t)
	_, err := app.db.Exec("CREATE TABLE appmigrations (version INTEGER NOT NULL, migrated DATETIME NOT NULL, result TEXT NOT NULL)")
	require.NoError(t, err)

	require.NoError(t, initDatabaseIfEmpty(app))
	exists, err := app.db.dialectOrDefault().TableExists(context.Background(), app.db.DB, "users")
	require.NoError(t, err)
	require.False(t, exists, "a database that already had appmigrations was initialized")
}
