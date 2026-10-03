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
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writeas/activity/streams"
)

// TestConcurrentFollowsFromNewActor: two Follows from an actor this instance
// has never seen arrive close together. The inbox handler looks the actor up
// synchronously and persists the follower about two seconds later, so both
// deliveries carry remoteUser == nil and both try to add the same remoteusers
// row. The one that loses that insert must still store the follow and send
// its Accept, not roll back and drop it.
//
// Passing nil to both calls is what makes this deterministic on every engine:
// whichever call inserts second meets the unique key, whether the two really
// overlap (MySQL and Postgres, where the second insert waits on the first's
// row lock) or run one after the other (SQLite, which serialises writers).
func TestConcurrentFollowsFromNewActor(t *testing.T) {
	cfg := txTestConfig()
	cfg.Database.Type = driverSQLite // replaced when WF_TEST_DB_TYPE selects another engine
	app := &App{cfg: cfg}
	openAppTestDB(t, app, "sqlite3", filepath.Join(t.TempDir(), "follow.db")+"?parseTime=true")
	f := newFollowFixture(t, app)

	_, err := getRemoteUser(app, f.remote.ID)
	require.Error(t, err, "the actor must be unknown before the Follows arrive")

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			acceptAndPersistFollow(app, f.coll, f.blog, streams.NewAccept(), f.remoteIRI, f.remote, nil, true, false)
		}()
	}
	close(start)
	wg.Wait()

	var users int
	require.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM remoteusers WHERE actor_id = ?", f.remote.ID).Scan(&users))
	assert.Equal(t, 1, users, "both Follows must resolve to one remote user")
	assert.Equal(t, 1, f.followers(t), "the follow must be stored once")
	assert.Equal(t, 2, f.inbox.count(), "both Follows must be accepted")

	var keys int
	require.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM remoteuserkeys WHERE id = ?", f.remote.PublicKey.ID).Scan(&keys))
	assert.Equal(t, 1, keys)
}
