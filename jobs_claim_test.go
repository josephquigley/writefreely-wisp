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
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/migrations"
)

// wisp_v3 (migrations/wisp_v3.go): a publish job is claimed with one atomic UPDATE
// before it is sent, so two workers that both selected it cannot both send
// it, and a failed send gives the claim back.

// seedDueEmailJobs creates a blog with n posts created a minute ago, each
// with an email publish job that is due now, and returns the job IDs.
func seedDueEmailJobs(t *testing.T, app *App, db *sql.DB, n int) []int64 {
	t.Helper()
	u := &User{Username: "claimer", HashedPass: []byte("x")}
	require.NoError(t, app.db.CreateUser(app.cfg, u, "", ""))
	coll, err := app.db.CreateCollection(app.cfg, "claimer-blog", "claimer-blog", u.ID)
	require.NoError(t, err)
	created := app.db.dialectOrDefault().DateSub(1, "MINUTE")
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("claim%d", i)
		_, err = db.Exec(`INSERT INTO posts
(id, slug, text_appearance, language, rtl, privacy, owner_id, collection_id, created, updated, view_count, title, content)
VALUES (?, ?, 'norm', 'en', ?, 0, ?, ?, `+created+`, `+created+`, 0, 'T', 'B')`, id, id, false, u.ID, coll.ID)
		require.NoError(t, err)
		require.NoError(t, app.db.InsertJob(&PostJob{PostID: id, Action: "email", Delay: 0}))
	}
	jobs, err := app.db.GetJobsToRun("email")
	require.NoError(t, err)
	require.Len(t, jobs, n, "fixture: due jobs")
	ids := make([]int64, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	return ids
}

// claimWorkers returns two datastores on app's database with separate
// connection pools, standing in for two app processes.
func claimWorkers(t *testing.T, app *App, db *sql.DB) (*datastore, *datastore) {
	t.Helper()
	if app.db.driverName != driverSQLite {
		return app.db, newDatastore(secondProcessDB(t, app), app.db.driverName)
	}
	var seq int
	var name, path string
	require.NoError(t, db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path))
	open := func() *datastore {
		p, err := sql.Open("sqlite3", path+"?parseTime=true&_busy_timeout=10000")
		require.NoError(t, err)
		t.Cleanup(func() { p.Close() })
		return newDatastore(p, driverSQLite)
	}
	return open(), open()
}

func TestPublishJobClaimsAreDisjoint(t *testing.T) {
	app, db := newJobsTestApp(t)
	ids := seedDueEmailJobs(t, app, db, 40)
	a, b := claimWorkers(t, app, db)

	// Both workers selected every job before either claims one, as two
	// ticks that overlap do. They then claim each job at the same moment,
	// so every claim is a real race on one row.
	claimed := make([][]int64, 2)
	for _, id := range ids {
		var wg sync.WaitGroup
		start := make(chan struct{})
		got := make([]bool, 2)
		errs := make([]error, 2)
		for w, ds := range []*datastore{a, b} {
			wg.Add(1)
			go func(w int, ds *datastore) {
				defer wg.Done()
				<-start
				got[w], errs[w] = ds.ClaimJob(id)
			}(w, ds)
		}
		close(start)
		wg.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])
		for w := range got {
			if got[w] {
				claimed[w] = append(claimed[w], id)
			}
		}
	}

	seen := map[int64]int{}
	for _, c := range claimed {
		for _, id := range c {
			seen[id]++
		}
	}
	for _, id := range ids {
		assert.Equal(t, 1, seen[id], "job %d claimed %d times", id, seen[id])
	}
	t.Logf("worker A claimed %d, worker B %d", len(claimed[0]), len(claimed[1]))

	// A claimed job is no longer due, and cannot be claimed again.
	jobs, err := app.db.GetJobsToRun("email")
	require.NoError(t, err)
	assert.Empty(t, jobs)
	ok, err := b.ClaimJob(ids[0])
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestPublishJobClaimedElsewhereIsNotSent: a job another worker has claimed
// is skipped, even by a run that selected it before the claim.
func TestPublishJobClaimedElsewhereIsNotSent(t *testing.T) {
	app, db := newJobsTestApp(t)
	ids := seedDueEmailJobs(t, app, db, 1)
	jobs, err := app.db.GetJobsToRun("email")
	require.NoError(t, err)

	_, other := claimWorkers(t, app, db)
	ok, err := other.ClaimJob(ids[0])
	require.NoError(t, err)
	require.True(t, ok)

	sends := 0
	orig := jobEmailPost
	t.Cleanup(func() { jobEmailPost = orig })
	jobEmailPost = func(app *App, p *PublicPost, collID int64) error {
		sends++
		return nil
	}
	require.NoError(t, runJobs(app, jobs, true))
	assert.Equal(t, 0, sends)
	assert.Equal(t, 1, countPublishJobs(t, db), "the claiming worker deletes the job, not this one")
}

func TestFailedPublishJobIsClaimableAgain(t *testing.T) {
	app, db := newJobsTestApp(t)
	ids := seedDueEmailJobs(t, app, db, 1)

	sends := 0
	fail := true
	orig := jobEmailPost
	t.Cleanup(func() { jobEmailPost = orig })
	jobEmailPost = func(app *App, p *PublicPost, collID int64) error {
		sends++
		if fail {
			return errors.New("mail provider down")
		}
		return nil
	}

	runPublishJobs(app)
	assert.Equal(t, 1, sends)
	assert.Equal(t, 1, countPublishJobs(t, db), "a failed job stays queued")
	var claimedAt sql.NullString
	require.NoError(t, db.QueryRow("SELECT claimed_at FROM publishjobs WHERE id = ?", ids[0]).Scan(&claimedAt))
	assert.False(t, claimedAt.Valid, "a failed send gives its claim back")

	// The next tick retries it, and sends it once.
	fail = false
	runPublishJobs(app)
	assert.Equal(t, 2, sends)
	assert.Equal(t, 0, countPublishJobs(t, db))
	runPublishJobs(app)
	assert.Equal(t, 2, sends)
}

func TestPublishJobClaimsMigration(t *testing.T) {
	// About the migrations themselves: not a template clone.
	buildPostgresFromScratch(t)
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		// Back to before wisp_v3, as an upgrade finds it, with a job queued.
		for _, q := range []string{
			"ALTER TABLE publishjobs DROP COLUMN claimed_at",
			"DELETE FROM wisp_migrations WHERE version >= 3",
		} {
			_, err := app.db.Exec(q)
			require.NoError(t, err, q)
		}
		require.NoError(t, app.db.InsertJob(&PostJob{PostID: "oldjob", Action: "email", Delay: 5}))

		mdb := migrations.NewDatastore(app.db.DB, app.db.driverName)
		require.NoError(t, migrations.Migrate(mdb))
		var claimedAt sql.NullString
		require.NoError(t, app.db.QueryRow("SELECT claimed_at FROM publishjobs WHERE post_id = 'oldjob'").Scan(&claimedAt))
		assert.False(t, claimedAt.Valid, "an existing job starts unclaimed")

		// A migration that stopped after adding the column but before
		// recording itself runs again on the next start.
		_, err := app.db.Exec("DELETE FROM wisp_migrations WHERE version >= 3")
		require.NoError(t, err)
		require.NoError(t, migrations.Migrate(mdb))
	})
}
