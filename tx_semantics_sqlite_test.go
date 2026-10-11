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

	"github.com/stretchr/testify/require"
)

// TestTxSemantics_SQLite runs the WFPG-08 scenarios (tx_semantics_test.go)
// on SQLite, so both engines are held to the same outcome.
func TestTxSemantics_SQLite(t *testing.T) {
	for _, sc := range txSemanticsScenarios {
		t.Run(sc.name, func(t *testing.T) {
			sdb, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "tx.db")+"?parseTime=true")
			require.NoError(t, err)
			t.Cleanup(func() { sdb.Close() })
			cfg := txTestConfig()
			cfg.Database.Type = driverSQLite
			app := &App{cfg: cfg, db: newDatastore(sdb, driverSQLite)}
			require.NoError(t, adminInitDatabase(app))
			sc.run(t, app)
		})
	}
}
