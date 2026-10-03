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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writeas/web-core/activitystreams"
	"github.com/writefreely/writefreely/config"
)

// TestAddOrGetRemoteUserConcurrently: two Follows from an actor this instance
// has never seen both reach the inbox goroutine with no remote user, and both
// try to add one. The one that loses the insert must get the existing row's
// id, so that its follow is still stored, rather than an error.
func TestAddOrGetRemoteUserConcurrently(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	db, err := sql.Open("sqlite3", dbPath+"?parseTime=true")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	cfg := config.New()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	app := &App{db: &datastore{DB: db, driverName: driverSQLite}, cfg: cfg}
	require.NoError(t, adminInitDatabase(app))

	actor := activitystreams.NewPerson("https://remote.example/users/bob")
	actor.Inbox = "https://remote.example/users/bob/inbox"
	actor.Endpoints.SharedInbox = "https://remote.example/inbox"
	actor.URL = "https://remote.example/@bob"

	ids := make([]int64, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ids[i], errs[i] = addOrGetRemoteUser(app, actor)
		}(i)
	}
	close(start)
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.NotZero(t, ids[0])
	assert.Equal(t, ids[0], ids[1], "both calls must resolve to the same remote user")

	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM remoteusers WHERE actor_id = ?", actor.ID).Scan(&n))
	assert.Equal(t, 1, n)
}
