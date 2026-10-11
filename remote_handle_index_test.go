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
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/migrations"
)

// wisp_v3 (migrations/wisp_v3.go): the remote-handle lookup that every mention and
// reply delegate goes through is served by an index on every engine, instead
// of reading the whole remoteusers table.

// insertRemoteHandles fills remoteusers with n actors, so that a planner
// has a table worth using an index on.
func insertRemoteHandles(t *testing.T, app *App, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		actor := fmt.Sprintf("https://peer%d.example/users/u%d", i, i)
		_, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, handle) VALUES (?, ?, ?, ?)",
			actor, actor+"/inbox", fmt.Sprintf("https://peer%d.example/inbox", i), fmt.Sprintf("u%d@peer%d.example", i, i))
		require.NoError(t, err)
	}
}

// remoteHandlePlan returns the plan the engine chooses for the query
// getRemoteUserFromHandle runs, flattened to one string.
func remoteHandlePlan(t *testing.T, app *App) string {
	t.Helper()
	return explainPlan(t, app, app.db.remoteUserByHandleQuery(), "u7@peer7.example")
}

// explainPlan returns the plan the engine chooses for q, flattened to one
// string.
func explainPlan(t *testing.T, app *App, q string, args ...interface{}) string {
	t.Helper()
	switch app.db.driverName {
	case driverSQLite:
		return joinPlanRows(t, app.db, "EXPLAIN QUERY PLAN "+q, args...)
	case driverPostgres:
		// A few dozen rows fit in one page, where a sequential scan is
		// honestly cheaper; with it priced out, what is left shows whether
		// an index can serve the expression at all.
		tx, err := app.db.Begin()
		require.NoError(t, err)
		defer tx.Rollback()
		_, err = tx.Exec("SET LOCAL enable_seqscan = off")
		require.NoError(t, err)
		return joinPlanRows(t, tx, "EXPLAIN "+q, args...)
	case driverMySQL:
		return joinPlanRows(t, app.db, "EXPLAIN "+q, args...)
	}
	t.Fatalf("no plan query for %s", app.db.driverName)
	return ""
}

// joinPlanRows reads every column of every row as text, labelled with its
// column name, so MySQL's and MariaDB's differing EXPLAIN layouts read alike.
func joinPlanRows(t *testing.T, q interface {
	Query(string, ...interface{}) (*sql.Rows, error)
}, query string, args ...interface{}) string {
	t.Helper()
	rows, err := q.Query(query, args...)
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	var out []string
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		for i, c := range cols {
			out = append(out, c+"="+string(vals[i]))
		}
	}
	require.NoError(t, rows.Err())
	return strings.Join(out, " ")
}

// remoteHandleIndexName is the index wisp_v3 creates on each engine.
func remoteHandleIndexName(driverName string) string {
	if driverName == driverMySQL {
		return "remoteusers_handle_lower"
	}
	return "remoteusers_lower_handle"
}

func TestRemoteHandleLookupUsesIndex(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		insertRemoteHandles(t, app, 50)
		plan := remoteHandlePlan(t, app)
		assert.Contains(t, plan, remoteHandleIndexName(app.db.driverName), "the handle lookup should use wisp_v3's index; plan: %s", plan)
		if app.db.driverName == driverMySQL {
			assert.Contains(t, plan, "key="+remoteHandleIndexName(driverMySQL), plan)
		}
	})
}

// undoRemoteHandleIndex takes a database back to before wisp_v3, as an upgrade finds it.
func undoRemoteHandleIndex(t *testing.T, app *App) {
	t.Helper()
	var qs []string
	switch app.db.driverName {
	case driverSQLite, driverPostgres:
		qs = []string{"DROP INDEX remoteusers_lower_handle"}
	case driverMySQL:
		qs = []string{"ALTER TABLE remoteusers DROP COLUMN handle_lower"}
	}
	qs = append(qs, "DELETE FROM wisp_migrations WHERE version >= 3")
	for _, q := range qs {
		_, err := app.db.Exec(q)
		require.NoError(t, err, q)
	}
}

func TestRemoteHandleIndexMigration(t *testing.T) {
	// About the migrations themselves: not a template clone.
	buildPostgresFromScratch(t)
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		undoRemoteHandleIndex(t, app)
		// On MySQL the query names handle_lower, which is gone now.
		if app.db.driverName != driverMySQL {
			require.NotContains(t, remoteHandlePlan(t, app), remoteHandleIndexName(app.db.driverName))
		}

		// Rows cached before wisp_v3, one in the case its owner typed.
		insertRemoteHandles(t, app, 50)
		_, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, handle) VALUES (?, ?, ?, ?)",
			"https://social.example/users/Bob", "https://social.example/users/Bob/inbox", "https://social.example/inbox", "Bob@Social.Example")
		require.NoError(t, err)

		mdb := migrations.NewDatastore(app.db.DB, app.db.driverName)
		require.NoError(t, migrations.Migrate(mdb))
		plan := remoteHandlePlan(t, app)
		assert.Contains(t, plan, remoteHandleIndexName(app.db.driverName), plan)

		ru, err := getRemoteUserFromHandle(app, "@BOB@social.example")
		require.NoError(t, err)
		assert.Equal(t, "https://social.example/users/Bob", ru.ActorID)
		ru, err = getRemoteUserFromHandle(app, "u7@peer7.example")
		require.NoError(t, err)
		assert.Equal(t, "https://peer7.example/users/u7", ru.ActorID)

		// A migration that stopped after its schema change but before
		// recording itself runs again on the next start.
		_, err = app.db.Exec("DELETE FROM wisp_migrations WHERE version >= 3")
		require.NoError(t, err)
		require.NoError(t, migrations.Migrate(mdb))
		assert.Contains(t, remoteHandlePlan(t, app), remoteHandleIndexName(app.db.driverName))
	})
}
