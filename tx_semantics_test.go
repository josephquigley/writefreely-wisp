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

// WFPG-08: no code path may carry on in a transaction after a statement in it
// failed, because on Postgres any error aborts the whole transaction. Each
// scenario below is a plain function taking an initialised *App, run on
// Postgres here and on SQLite in tx_semantics_sqlite_test.go, so that both
// engines are held to the same outcome.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writeas/activity/streams"
	"github.com/writeas/web-core/activitystreams"
	"github.com/writefreely/writefreely/config"
)

func TestInsertIgnoreRendering(t *testing.T) {
	const ins = "INSERT INTO remotefollows (collection_id, remote_user_id) VALUES (?, ?)"
	assert.Equal(t, "INSERT IGNORE INTO remotefollows (collection_id, remote_user_id) VALUES (?, ?)", mysqlDialect{}.InsertIgnore(ins))
	assert.Equal(t, "INSERT OR IGNORE INTO remotefollows (collection_id, remote_user_id) VALUES (?, ?)", sqliteDialect{}.InsertIgnore(ins))
	assert.Equal(t, "INSERT INTO remotefollows (collection_id, remote_user_id) VALUES (?, ?) ON CONFLICT DO NOTHING", postgresDialect{}.InsertIgnore(ins+" \n"))

	for _, d := range []dialect{mysqlDialect{}, sqliteDialect{}, postgresDialect{}} {
		assert.Panics(t, func() { d.InsertIgnore("UPDATE posts SET x = 1") }, d.DriverName())
		assert.Panics(t, func() { d.InsertIgnore("INSERT IGNORE INTO t (a) VALUES (?)") }, d.DriverName())
		assert.Panics(t, func() { d.InsertIgnore(ins + ";") }, d.DriverName())
	}
}

// txTestUser creates a user and returns it with its blog.
func txTestUser(t *testing.T, app *App, username string) (*User, *Collection) {
	t.Helper()
	u := &User{Username: username, HashedPass: []byte("x")}
	require.NoError(t, app.db.CreateUser(app.cfg, u, "", ""))
	c, err := app.db.GetCollection(username)
	require.NoError(t, err)
	return u, c
}

// acceptInbox is a stub remote inbox that counts the Accepts delivered to it.
type acceptInbox struct {
	srv     *httptest.Server
	mu      sync.Mutex
	accepts int
}

func newAcceptInbox(t *testing.T) *acceptInbox {
	t.Helper()
	ib := &acceptInbox{}
	ib.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]interface{}
		if json.Unmarshal(body, &m) == nil && m["type"] == "Accept" {
			ib.mu.Lock()
			ib.accepts++
			ib.mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ib.srv.Close)
	return ib
}

func (ib *acceptInbox) count() int {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	return ib.accepts
}

// followFixture is everything acceptAndPersistFollow needs for one remote
// actor following one local blog.
type followFixture struct {
	app       *App
	coll      *Collection
	blog      *activitystreams.Person
	remote    *activitystreams.Person
	remoteIRI *url.URL
	inbox     *acceptInbox
}

func newFollowFixture(t *testing.T, app *App) *followFixture {
	t.Helper()
	_, c := txTestUser(t, app, "alice")

	blog := activitystreams.NewPerson("https://local.example/api/collections/alice")
	blog.PublicKey.ID = blog.ID + "#main-key"
	blog.SetPrivKey(pemEncodePrivateKey(t, testKey(t)))

	ib := newAcceptInbox(t)
	remote := activitystreams.NewPerson("https://remote.example/users/bob")
	remote.Inbox = ib.srv.URL + "/inbox"
	remote.Endpoints.SharedInbox = ib.srv.URL + "/inbox"
	remote.URL = "https://remote.example/@bob"
	remote.PublicKey.ID = remote.ID + "#main-key"
	remote.PublicKey.PublicKeyPEM = "-----BEGIN PUBLIC KEY-----\nnot checked here\n-----END PUBLIC KEY-----\n"

	iri, err := url.Parse(remote.ID)
	require.NoError(t, err)
	return &followFixture{app: app, coll: c, blog: blog, remote: remote, remoteIRI: iri, inbox: ib}
}

// follow runs the Follow's persistence and Accept exactly as the inbox
// goroutine does, looking the remote user up first as getActor would.
func (f *followFixture) follow(t *testing.T) {
	t.Helper()
	ru, err := getRemoteUser(f.app, f.remote.ID)
	if err != nil {
		ru = nil // not stored yet: acceptAndPersistFollow adds it
	}
	acceptAndPersistFollow(f.app, f.coll, f.blog, streams.NewAccept(), f.remoteIRI, f.remote, ru, true, false)
}

func (f *followFixture) followers(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.app.db.QueryRow("SELECT COUNT(*) FROM remotefollows WHERE collection_id = ?", f.coll.ID).Scan(&n))
	return n
}

// testFollowTwiceAcceptedTwice: a remote that re-sends its Follow ends up as
// one follower and gets an Accept both times.
func testFollowTwiceAcceptedTwice(t *testing.T, app *App) {
	f := newFollowFixture(t, app)

	f.follow(t)
	assert.Equal(t, 1, f.followers(t))
	assert.Equal(t, 1, f.inbox.count())

	f.follow(t)
	assert.Equal(t, 1, f.followers(t), "a repeated Follow must not add a second follower row")
	assert.Equal(t, 2, f.inbox.count(), "a repeated Follow must still be accepted")

	var keys int
	require.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM remoteuserkeys WHERE id = ?", f.remote.PublicKey.ID).Scan(&keys))
	assert.Equal(t, 1, keys)
}

// testFollowWithStaleKeyStored: a key row already stored under the actor's
// key id (left by a remoteusers row that no longer exists) does not stop the
// new follower from being stored and accepted.
func testFollowWithStaleKeyStored(t *testing.T, app *App) {
	f := newFollowFixture(t, app)
	_, err := app.db.Exec("INSERT INTO remoteuserkeys (id, remote_user_id, public_key) VALUES (?, ?, ?)", f.remote.PublicKey.ID, 999999, []byte("stale"))
	require.NoError(t, err)

	f.follow(t)
	assert.Equal(t, 1, f.followers(t), "the follower must be stored despite the existing key row")
	assert.Equal(t, 1, f.inbox.count())
}

// testFollowWithLongKeyID: an actor whose key ID is longer than
// remoteuserkeys.id (varchar(255)) is still stored and accepted. The ID is
// truncated only at the insert, so the actor keeps its full key ID.
func testFollowWithLongKeyID(t *testing.T, app *App) {
	f := newFollowFixture(t, app)
	fullKeyID := f.remote.ID + "#" + strings.Repeat("k", 300-len(f.remote.ID)-1)
	f.remote.PublicKey.ID = fullKeyID

	f.follow(t)
	assert.Equal(t, 1, f.followers(t), "the follower must be stored despite the long key id")
	assert.Equal(t, 1, f.inbox.count())
	assert.Equal(t, fullKeyID, f.remote.PublicKey.ID, "storing the key must not shorten the actor's key id")

	var keys int
	require.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM remoteuserkeys WHERE id = ?", fullKeyID[:remoteUserKeyMaxLengthID]).Scan(&keys))
	assert.Equal(t, 1, keys)
}

// testRenameWithConflictingRedirect: a redirect already stored from the old
// username (left by an earlier owner of it) does not undo the rename, and is
// replaced so the old name points at the new one.
func testRenameWithConflictingRedirect(t *testing.T, app *App) {
	u, _ := txTestUser(t, app, "oldname")
	_, err := app.db.Exec("INSERT INTO collectionredirects (prev_alias, new_alias) VALUES (?, ?)", "oldname", "someoneelse")
	require.NoError(t, err)

	require.NoError(t, app.db.ChangeSettings(app, u, &userSettings{Username: "newname"}))
	assert.Equal(t, "newname", u.Username)

	var name string
	require.NoError(t, app.db.QueryRow("SELECT username FROM users WHERE id = ?", u.ID).Scan(&name))
	assert.Equal(t, "newname", name, "the rename must be committed")
	var n int
	require.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM collections WHERE alias = ? AND owner_id = ?", "newname", u.ID).Scan(&n))
	assert.Equal(t, 1, n, "the blog alias must be committed with the rename")
	assert.Equal(t, "newname", app.db.GetCollectionRedirect("oldname"))
}

// testRenameChainsRedirects: renaming twice points every earlier name at the
// latest one.
func testRenameChainsRedirects(t *testing.T, app *App) {
	u, _ := txTestUser(t, app, "first")
	require.NoError(t, app.db.ChangeSettings(app, u, &userSettings{Username: "second"}))
	require.NoError(t, app.db.ChangeSettings(app, u, &userSettings{Username: "third"}))
	assert.Equal(t, "third", app.db.GetCollectionRedirect("first"))
	assert.Equal(t, "third", app.db.GetCollectionRedirect("second"))
}

// testSignupClearsRedirect: a new user taking a name that has a redirect
// clears it, and signup still commits.
func testSignupClearsRedirect(t *testing.T, app *App) {
	_, err := app.db.Exec("INSERT INTO collectionredirects (prev_alias, new_alias) VALUES (?, ?)", "carol", "elsewhere")
	require.NoError(t, err)
	u, _ := txTestUser(t, app, "carol")
	assert.NotZero(t, u.ID)
	assert.Equal(t, "", app.db.GetCollectionRedirect("carol"))
}

// testRepinSamePosition: pinning a post at the position it already has is
// not forbidden on any engine (MySQL reports 0 rows changed, Postgres and
// SQLite 1 row matched); someone else's post still is.
func testRepinSamePosition(t *testing.T, app *App) {
	u, c := txTestUser(t, app, "pinner")
	const postID = "repinsamepos0001"
	_, err := app.db.Exec("INSERT INTO posts (id, privacy, owner_id, collection_id, view_count, title, content) VALUES (?, 0, ?, ?, 0, '', '')", postID, u.ID, c.ID)
	require.NoError(t, err)

	assert.NoError(t, app.db.UpdatePostPinState(true, postID, c.ID, u.ID, 1))
	assert.NoError(t, app.db.UpdatePostPinState(true, postID, c.ID, u.ID, 1))
	assert.NoError(t, app.db.UpdatePostPinState(false, postID, c.ID, u.ID, 0))
	assert.NoError(t, app.db.UpdatePostPinState(false, postID, c.ID, u.ID, 0))

	assert.Equal(t, ErrForbiddenCollection, app.db.UpdatePostPinState(true, postID, c.ID, u.ID+1, 1))
	assert.Equal(t, ErrForbiddenCollection, app.db.UpdatePostPinState(true, postID, c.ID+1, u.ID, 1))
}

var txSemanticsScenarios = []struct {
	name string
	run  func(t *testing.T, app *App)
}{
	{"FollowTwiceAcceptedTwice", testFollowTwiceAcceptedTwice},
	{"FollowWithStaleKeyStored", testFollowWithStaleKeyStored},
	{"FollowWithLongKeyID", testFollowWithLongKeyID},
	{"RenameWithConflictingRedirect", testRenameWithConflictingRedirect},
	{"RenameChainsRedirects", testRenameChainsRedirects},
	{"SignupClearsRedirect", testSignupClearsRedirect},
	{"RepinSamePosition", testRepinSamePosition},
}

func txTestConfig() *config.Config {
	cfg := config.New()
	cfg.App.MaxBlogs = testMaxBlogs
	cfg.App.Host = "https://local.example"
	return cfg
}

func TestTxSemantics_Postgres(t *testing.T) {
	for _, sc := range txSemanticsScenarios {
		t.Run(sc.name, func(t *testing.T) {
			sc.run(t, newPostgresTestApp(t, txTestConfig()))
		})
	}
}
