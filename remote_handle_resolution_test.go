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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/writefreely/writefreely/config"
)

// Handle resolution caches what it learns in remoteusers, and both entry
// points — GetProfilePageFromHandle and GetProfileURLFromHandle — used to
// cache a *failure* as readily as a success. A webfinger lookup that came
// back empty, or a remote actor that could not be fetched, still reached the
// INSERT, which wrote a row carrying the handle and an empty actor_id and
// inbox.
//
// That row is worse than no row. Every later lookup for the same handle
// finds it, reads an empty actor_id back, and returns empty without ever
// retrying the network, so one bad minute disables that handle permanently.
// Observed on a live instance: a blog's reply delegate resolved once while
// its peer was unreachable and never resolved again, and no post carried the
// mention afterwards.
//
// These tests drive both network calls from the outside, which is what the
// remoteLookup and newRemoteActor indirections exist for.

// newHandleTestApp builds a real sqlite-backed App with the schema in place
// and nothing in remoteusers.
func newHandleTestApp(t *testing.T) *App {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	db, err := sql.Open("sqlite3", dbPath+"?parseTime=true&cached=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := config.New()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	cfg.App.Host = "https://local.example"
	cfg.App.SingleUser = true

	app := &App{
		db:  &datastore{DB: db, driverName: driverSQLite},
		cfg: cfg,
	}
	if err := adminInitDatabase(app); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return app
}

func countRemoteUsers(t *testing.T, app *App) int {
	t.Helper()
	var n int
	if err := app.db.QueryRow("SELECT COUNT(*) FROM remoteusers").Scan(&n); err != nil {
		t.Fatalf("count remoteusers: %v", err)
	}
	return n
}

// stubRemoteLookup replaces the webfinger call for one test.
func stubRemoteLookup(t *testing.T, fn func(string) string) {
	t.Helper()
	orig := remoteLookup
	remoteLookup = fn
	t.Cleanup(func() { remoteLookup = orig })
}

// stubNewRemoteActor replaces the actor fetch for one test.
func stubNewRemoteActor(t *testing.T, fn func(*App, string) (remoteActorInfo, error)) {
	t.Helper()
	orig := newRemoteActor
	newRemoteActor = fn
	t.Cleanup(func() { newRemoteActor = orig })
}

// failingActorFetch fails, and records whether it was called at all.
func failingActorFetch(called *bool) func(*App, string) (remoteActorInfo, error) {
	return func(*App, string) (remoteActorInfo, error) {
		*called = true
		return remoteActorInfo{}, assert.AnError
	}
}

func TestGetProfilePageFromHandleFailedLookupIsNotCached(t *testing.T) {
	app := newHandleTestApp(t)
	stubRemoteLookup(t, func(string) string { return "" })
	fetched := false
	stubNewRemoteActor(t, failingActorFetch(&fetched))

	actorIRI, err := app.db.GetProfilePageFromHandle(app, "@delegate@peer.example")

	assert.Error(t, err, "an unresolvable handle must be an error, not an empty string with no error")
	assert.Empty(t, actorIRI)
	assert.False(t, fetched, "no actor should be fetched for an empty lookup result")
	assert.Equal(t, 0, countRemoteUsers(t, app), "a failed lookup must not be cached in remoteusers")
}

func TestGetProfileURLFromHandleFailedLookupIsNotCached(t *testing.T) {
	app := newHandleTestApp(t)
	stubRemoteLookup(t, func(string) string { return "" })
	fetched := false
	stubNewRemoteActor(t, failingActorFetch(&fetched))

	profileURL, err := GetProfileURLFromHandle(app, "@delegate@peer.example")

	assert.Error(t, err)
	assert.Empty(t, profileURL)
	assert.False(t, fetched)
	assert.Equal(t, 0, countRemoteUsers(t, app))
}

func TestGetProfilePageFromHandleFailedActorFetchIsNotCached(t *testing.T) {
	app := newHandleTestApp(t)
	stubRemoteLookup(t, func(string) string { return "https://peer.example/u/delegate" })
	fetched := false
	stubNewRemoteActor(t, failingActorFetch(&fetched))

	actorIRI, err := app.db.GetProfilePageFromHandle(app, "@delegate@peer.example")

	assert.Error(t, err, "an actor that could not be fetched must be an error")
	assert.Empty(t, actorIRI)
	assert.True(t, fetched)
	assert.Equal(t, 0, countRemoteUsers(t, app), "an unfetchable actor must not be cached with an empty inbox")
}

func TestGetProfileURLFromHandleFailedActorFetchIsNotCached(t *testing.T) {
	app := newHandleTestApp(t)
	stubRemoteLookup(t, func(string) string { return "https://peer.example/u/delegate" })
	fetched := false
	stubNewRemoteActor(t, failingActorFetch(&fetched))

	profileURL, err := GetProfileURLFromHandle(app, "@delegate@peer.example")

	assert.Error(t, err)
	assert.Empty(t, profileURL)
	assert.True(t, fetched)
	assert.Equal(t, 0, countRemoteUsers(t, app))
}

// The recovery path. A row for the actor already exists from some other
// route — a Follow, say — with no handle on it, which is why the handle
// lookup missed. Resolution should back-fill the handle onto that row and
// answer from it, without fetching the actor again.
func TestGetProfilePageFromHandleBackfillsHandleOnExistingActor(t *testing.T) {
	app := newHandleTestApp(t)
	const actorIRI = "https://peer.example/u/delegate"
	_, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox) VALUES (?, ?, ?)",
		actorIRI, actorIRI+"/inbox", "https://peer.example/f/inbox")
	assert.NoError(t, err)

	stubRemoteLookup(t, func(string) string { return actorIRI })
	fetched := false
	stubNewRemoteActor(t, failingActorFetch(&fetched))

	got, err := app.db.GetProfilePageFromHandle(app, "@delegate@peer.example")

	assert.NoError(t, err)
	assert.Equal(t, actorIRI, got)
	assert.False(t, fetched, "an actor already in remoteusers should not be fetched again")
	assert.Equal(t, 1, countRemoteUsers(t, app))

	var handle sql.NullString
	assert.NoError(t, app.db.QueryRow("SELECT handle FROM remoteusers WHERE actor_id = ?", actorIRI).Scan(&handle))
	assert.Equal(t, "delegate@peer.example", handle.String, "the handle should be back-filled onto the existing row")
}
