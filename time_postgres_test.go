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
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WFPG-06: on Postgres every timestamp is a timestamptz instant and Go sends
// UTC. These tests must pass whatever the process time zone is; run them
// with both TZ=UTC and TZ=America/Detroit:
//
//	TZ=America/Detroit make test-postgres GOTESTFLAGS='-run TestPostgres -v'

func timeTestUserAndBlog(t *testing.T, db *datastore, name string) (int64, int64) {
	t.Helper()
	var uid, cid int64
	require.NoError(t, db.QueryRow("INSERT INTO users (username, password, email) VALUES (?, ?, NULL) RETURNING id", name, strings.Repeat("x", 60)).Scan(&uid))
	require.NoError(t, db.QueryRow("INSERT INTO collections (alias, title, description, privacy, owner_id, view_count) VALUES (?, ?, '', 1, ?, 0) RETURNING id", name, name, uid).Scan(&cid))
	return uid, cid
}

func TestPostgresDialectTimeIsUTC(t *testing.T) {
	// No database needed: the dialect contract on its own.
	pg := postgresDialect{}
	assert.Equal(t, time.UTC, pg.NowForInsert().Location())
	detroit, err := time.LoadLocation("America/Detroit")
	require.NoError(t, err)
	in := time.Date(2026, 10, 2, 8, 0, 0, 0, detroit)
	out := pg.TimeArg(in)
	assert.Equal(t, time.UTC, out.Location())
	assert.True(t, in.Equal(out))

	// MySQL and SQLite are unchanged.
	assert.Equal(t, time.Local, mysqlDialect{}.NowForInsert().Location())
	assert.Equal(t, time.UTC, sqliteDialect{}.NowForInsert().Location())
	assert.Equal(t, in.Location(), mysqlDialect{}.TimeArg(in).Location())
	assert.Equal(t, in.Location(), sqliteDialect{}.TimeArg(in).Location())
}

func TestPostgresScheduledPostVisibility(t *testing.T) {
	db := newPostgresTestDatastore(t)
	t.Logf("process time zone: %s", time.Local)
	owner, coll := timeTestUserAndBlog(t, db, "scheduler")

	title, content := "", "x"
	past := time.Now().UTC().Add(-10 * time.Minute).Format(postMetaDateFormat)
	future := time.Now().UTC().Add(10 * time.Minute).Format(postMetaDateFormat)

	// A post with no explicit time is visible at once. If created were
	// stored as local wall-clock time read back as UTC, it would be hidden
	// (or shown early) by the process's UTC offset.
	_, err := db.CreatePost(owner, coll, &SubmittedPost{Title: &title, Content: &content})
	require.NoError(t, err)
	_, err = db.CreatePost(owner, coll, &SubmittedPost{Title: &title, Content: &content, Created: &past})
	require.NoError(t, err)
	scheduled, err := db.CreatePost(owner, coll, &SubmittedPost{Title: &title, Content: &content, Created: &future})
	require.NoError(t, err)

	c := &CollectionObj{Collection: Collection{ID: coll}}
	require.NoError(t, db.GetPostsCount(c, false))
	assert.Equal(t, 2, c.TotalPosts, "the post scheduled 10 minutes ahead must be hidden")
	require.NoError(t, db.GetPostsCount(c, true))
	assert.Equal(t, 3, c.TotalPosts)

	// Its time passes: move it 20 minutes earlier, as the clock would.
	_, err = db.Exec("UPDATE posts SET created = created - INTERVAL '20 minutes' WHERE id = ?", scheduled.ID)
	require.NoError(t, err)
	require.NoError(t, db.GetPostsCount(c, false))
	assert.Equal(t, 3, c.TotalPosts, "the scheduled post must be visible once its time has passed")
}

func TestPostgresPostCreatedRoundTrip(t *testing.T) {
	db := newPostgresTestDatastore(t)
	owner, coll := timeTestUserAndBlog(t, db, "roundtrip")

	title, content := "", "x"
	created := "2024-03-10T06:30:00Z" // the morning US DST starts
	p, err := db.CreatePost(owner, coll, &SubmittedPost{Title: &title, Content: &content, Created: &created})
	require.NoError(t, err)

	got, err := db.GetPost(p.ID, 0)
	require.NoError(t, err)
	want := time.Date(2024, 3, 10, 6, 30, 0, 0, time.UTC)
	assert.Truef(t, want.Equal(got.Created), "created: want %s, got %s", want, got.Created)
	// What the API, templates and ActivityPub render.
	assert.Equal(t, created, got.Created.UTC().Format(postMetaDateFormat))
	// Read back in UTC whatever the process zone, so the formatters with a
	// literal "Z" (Created8601, export, ActivityPub) render the right instant.
	assert.Equal(t, time.UTC, got.Created.Location())
	assert.Equal(t, created, got.Created8601())

	// UpdateOwnedPost's created parameter round-trips too.
	updated := "2025-11-02T05:30:00Z" // the morning US DST ends
	require.NoError(t, db.UpdateOwnedPost(&AuthenticatedPost{ID: p.ID, SubmittedPost: &SubmittedPost{Created: &updated}}, owner))
	got, err = db.GetPost(p.ID, 0)
	require.NoError(t, err)
	want = time.Date(2025, 11, 2, 5, 30, 0, 0, time.UTC)
	assert.Truef(t, want.Equal(got.Created), "updated created: want %s, got %s", want, got.Created)
}

func TestPostgresInviteExpiry(t *testing.T) {
	db := newPostgresTestDatastore(t)
	owner, _ := timeTestUserAndBlog(t, db, "inviter")

	// CreateUserInvite cannot run on Postgres yet: it inserts the boolean
	// userinvites.inactive as the integer literal 0 (SQLSTATE 42804), which
	// is the boolean-flag work, not this ticket's. Insert the rows the way it
	// does, binding expires through the same dialect.TimeArg it now uses.
	// Once that is fixed this should call db.CreateUserInvite directly.
	insert := func(id string, expires time.Time) {
		_, err := db.Exec("INSERT INTO userinvites (id, owner_id, max_uses, created, expires, inactive) VALUES (?, ?, 0, "+db.now()+", ?, false)", id, owner, db.dialectOrDefault().TimeArg(expires))
		require.NoError(t, err)
	}
	past := time.Now().Add(-10 * time.Minute)
	future := time.Now().Add(10 * time.Minute)
	insert("expird", past)
	insert("liveiv", future)

	i, err := db.GetUserInvite("expird")
	require.NoError(t, err)
	assert.True(t, i.Expired(), "an invite whose expiry has passed must be rejected")
	assert.Lessf(t, past.Sub(*i.Expires).Abs(), time.Millisecond, "expires: want %s, got %s", past, i.Expires)

	i, err = db.GetUserInvite("liveiv")
	require.NoError(t, err)
	assert.False(t, i.Expired())
}

func TestPostgresUsersFilteredWindow(t *testing.T) {
	db := newPostgresTestDatastore(t)
	timeTestUserAndBlog(t, db, "admin") // id 1 is excluded by default
	_, _ = timeTestUserAndBlog(t, db, "older")
	_, _ = timeTestUserAndBlog(t, db, "newer")
	_, err := db.Exec("UPDATE users SET created = ? WHERE username = 'older'", time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_, err = db.Exec("UPDATE users SET created = ? WHERE username = 'newer'", time.Date(2025, 1, 1, 14, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	// A window given in Detroit time (UTC-5 in January) selects by instant.
	detroit, err := time.LoadLocation("America/Detroit")
	require.NoError(t, err)
	since := time.Date(2025, 1, 1, 8, 0, 0, 0, detroit)  // 13:00 UTC
	until := time.Date(2025, 1, 1, 10, 0, 0, 0, detroit) // 15:00 UTC
	users, err := db.GetUsersFiltered(UserFilter{Since: &since, Until: &until, MaxPosts: -1})
	require.NoError(t, err)
	var names []string
	for _, u := range users {
		names = append(names, u.Username)
	}
	assert.Equal(t, []string{"newer"}, names)
}

// TestPostgresTimeIsUTCWhateverTheEnvironment: lib/pq reads timestamptz in
// the session TimeZone. postgresDSN pins it to UTC, and that must beat a
// host's PGTZ or PGOPTIONS, or Created8601 renders the wrong instant.
func TestPostgresTimeIsUTCWhateverTheEnvironment(t *testing.T) {
	newPostgresTestDB(t) // skips unless WF_TEST_DB_TYPE=postgres
	t.Setenv("PGTZ", "America/Detroit")
	t.Setenv("PGOPTIONS", "-c timezone=Asia/Tokyo")

	dsn, err := testPGDSN(os.Getenv(envTestPGDSN), "postgres")
	require.NoError(t, err)
	db, err := sql.Open(driverPostgresRebind, dsn)
	require.NoError(t, err)
	defer db.Close()

	var zone string
	var now time.Time
	require.NoError(t, db.QueryRow("SELECT current_setting('TimeZone'), now()").Scan(&zone, &now))
	assert.Equal(t, "UTC", zone)
	assert.Same(t, time.UTC, now.Location())
}

// TestPostgresTestDSNAlwaysUTC: a WF_TEST_PG_DSN that names another zone
// must not change what the suite tests.
func TestPostgresTestDSNAlwaysUTC(t *testing.T) {
	dsn, err := testPGDSN("postgres://u@h/db?timezone=America/Detroit", "x")
	require.NoError(t, err)
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	assert.Equal(t, "UTC", u.Query().Get("timezone"))
}
