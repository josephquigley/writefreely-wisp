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
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/writeas/impart"
	"github.com/writefreely/writefreely/config"
)

const likeTestActor = "https://remote.example/users/bob"

// newLikeTestApp builds a sqlite-backed single-user App with one collection
// and one cached remote actor, enough to deliver a Like to its inbox without
// any network access.
func newLikeTestApp(t *testing.T) *App {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	db, err := sql.Open("sqlite3_with_regex", dbPath+"?parseTime=true&cached=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := config.New()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	cfg.App.Host = "https://local.example"
	cfg.App.SingleUser = true

	app := &App{db: &datastore{DB: db, driverName: driverSQLite}, cfg: cfg}
	if err := adminInitDatabase(app); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	u := &User{Username: "alice", HashedPass: []byte("x")}
	if err := app.db.CreateUser(cfg, u, "", ""); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, url) VALUES (?, ?, ?, ?)", likeTestActor, "https://remote.example/inbox", "", likeTestActor); err != nil {
		t.Fatalf("seed remote user: %v", err)
	}
	return app
}

// deliverLike posts a Like, or an Undo of one, from likeTestActor for postIRI
// to the collection inbox.
func deliverLike(app *App, activityType, postIRI string) (*httptest.ResponseRecorder, error) {
	like := fmt.Sprintf(`{"type":"Like","actor":%q,"object":%q}`, likeTestActor, postIRI)
	body := fmt.Sprintf(`{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/activities/1","type":"Like","actor":%q,"object":%q}`, likeTestActor, postIRI)
	if activityType == "Undo" {
		body = fmt.Sprintf(`{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/activities/2","type":"Undo","actor":%q,"object":%s}`, likeTestActor, like)
	}
	r := httptest.NewRequest("POST", "https://local.example/api/collections/alice/inbox", strings.NewReader(body))
	w := httptest.NewRecorder()
	err := handleFetchCollectionInbox(app, w, r)
	return w, err
}

func countLikes(t *testing.T, app *App, postID string) int {
	t.Helper()
	var n int
	if err := app.db.QueryRow("SELECT COUNT(*) FROM remote_likes WHERE post_id = ?", postID).Scan(&n); err != nil {
		t.Fatalf("count likes: %v", err)
	}
	return n
}

func TestInboxRepeatedLikeIsIdempotent(t *testing.T) {
	app := newLikeTestApp(t)
	post := "https://local.example/api/posts/abc123"

	for i := 1; i <= 2; i++ {
		w, err := deliverLike(app, "Like", post)
		assert.NoError(t, err, "delivery %d", i)
		assert.Equal(t, http.StatusOK, w.Code, "delivery %d", i)
		assert.Equal(t, 1, countLikes(t, app, "abc123"), "likes after delivery %d", i)
	}
}

func TestInboxRepeatedUndoLikeIsIdempotent(t *testing.T) {
	app := newLikeTestApp(t)
	post := "https://local.example/api/posts/abc123"

	_, err := deliverLike(app, "Like", post)
	assert.NoError(t, err)
	for i := 1; i <= 2; i++ {
		w, err := deliverLike(app, "Undo", post)
		assert.NoError(t, err, "undo %d", i)
		assert.Equal(t, http.StatusOK, w.Code, "undo %d", i)
		assert.Equal(t, 0, countLikes(t, app, "abc123"))
	}
}

func TestInboxLikeDBFailureIsAnHTTPError(t *testing.T) {
	// A database failure must reach the client as an error status, not as the
	// HTML "Server error" page the handler renders for a plain error.
	app := newLikeTestApp(t)
	if _, err := app.db.Exec("DROP TABLE remote_likes"); err != nil {
		t.Fatalf("drop remote_likes: %v", err)
	}

	_, err := deliverLike(app, "Like", "https://local.example/api/posts/abc123")

	herr, ok := err.(impart.HTTPError)
	if assert.True(t, ok, "want impart.HTTPError, got %T: %v", err, err) {
		assert.Equal(t, http.StatusInternalServerError, herr.Status)
	}
}
