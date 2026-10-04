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

// Two Follows from an actor this instance has never seen arrive close
// together. The inbox handler looks the actor up synchronously and persists
// the follower about two seconds later, so both deliveries carry
// remoteUser == nil and both try to add the same remoteusers row. The one
// that loses that insert must still store its follow and send its Accept, not
// roll back and drop them.
//
// Passing nil to both calls is what makes this deterministic on every engine:
// whichever call inserts second meets the unique key, whether the two really
// overlap (MySQL and Postgres, where the second insert waits on the first's
// row lock) or run one after the other (SQLite, which serialises writers).

// TestConcurrentFollowsFromNewActor: the same Follow delivered twice (peers
// retry) still ends in one follower and two Accepts.
func TestConcurrentFollowsFromNewActor(t *testing.T) {
	f := newFollowRaceFixture(t)
	f.followConcurrently(t, f.coll, f.coll)

	assert.Equal(t, 1, f.remoteUsers(t), "both Follows must resolve to one remote user")
	assert.Equal(t, 1, f.followers(t), "the follow must be stored once")
	assert.Equal(t, 2, f.inbox.count(), "both Follows must be accepted")
	assert.Equal(t, 1, f.keys(t))
}

// TestConcurrentFollowsOfTwoBlogsFromNewActor: a new actor following two
// blogs on this instance at once ends up following both.
func TestConcurrentFollowsOfTwoBlogsFromNewActor(t *testing.T) {
	f := newFollowRaceFixture(t)
	_, other := txTestUser(t, f.app, "bea")
	f.followConcurrently(t, f.coll, other)

	assert.Equal(t, 1, f.remoteUsers(t), "both Follows must resolve to one remote user")
	assert.Equal(t, 1, f.followers(t), "the follow of alice's blog must be stored")
	var n int
	require.NoError(t, f.app.db.QueryRow("SELECT COUNT(*) FROM remotefollows WHERE collection_id = ?", other.ID).Scan(&n))
	assert.Equal(t, 1, n, "the follow of bea's blog must be stored")
	assert.Equal(t, 2, f.inbox.count(), "both Follows must be accepted")
	assert.Equal(t, 1, f.keys(t))
}

type followRaceFixture struct{ *followFixture }

func newFollowRaceFixture(t *testing.T) followRaceFixture {
	t.Helper()
	// A build without `-tags sqlite` still links the SQLite driver (db copy
	// reads SQLite files), so the harness can open one, but the datastore
	// cannot classify SQLite errors there and the app itself refuses to run
	// on SQLite. That configuration never serves a Follow.
	if engine, _ := testDBEngine(); engine == driverSQLite && !SQLiteEnabled {
		t.Skip("SQLite support not compiled in; run with `go test -tags sqlite` to run this test")
	}
	cfg := txTestConfig()
	cfg.Database.Type = driverSQLite // replaced when WF_TEST_DB_TYPE selects another engine
	app := &App{cfg: cfg}
	openAppTestDB(t, app, "sqlite3", filepath.Join(t.TempDir(), "follow.db")+"?parseTime=true")
	f := followRaceFixture{newFollowFixture(t, app)}

	_, err := getRemoteUser(app, f.remote.ID)
	require.Error(t, err, "the actor must be unknown before the Follows arrive")
	return f
}

// followConcurrently runs one Follow of each collection at the same moment,
// both carrying the nil remote user the inbox handler saw.
func (f followRaceFixture) followConcurrently(t *testing.T, colls ...*Collection) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, c := range colls {
		wg.Add(1)
		go func(c *Collection) {
			defer wg.Done()
			<-start
			acceptAndPersistFollow(f.app, c, f.blog, streams.NewAccept(), f.remoteIRI, f.remote, nil, true, false)
		}(c)
	}
	close(start)
	wg.Wait()
}

func (f followRaceFixture) remoteUsers(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.app.db.QueryRow("SELECT COUNT(*) FROM remoteusers WHERE actor_id = ?", f.remote.ID).Scan(&n))
	return n
}

func (f followRaceFixture) keys(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.app.db.QueryRow("SELECT COUNT(*) FROM remoteuserkeys WHERE id = ?", f.remote.PublicKey.ID).Scan(&n))
	return n
}
