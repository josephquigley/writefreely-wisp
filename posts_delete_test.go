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
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	_ "github.com/mattn/go-sqlite3"

	"github.com/writefreely/writefreely/config"
)

// staticSessionStore hands every request a session already logged in as u,
// so a handler can be driven without a cookie round trip.
type staticSessionStore struct{ u *User }

func (s staticSessionStore) Get(r *http.Request, name string) (*sessions.Session, error) {
	return s.New(r, name)
}

func (s staticSessionStore) New(r *http.Request, name string) (*sessions.Session, error) {
	sess := sessions.NewSession(s, name)
	sess.Values[cookieUserVal] = s.u
	return sess, nil
}

func (s staticSessionStore) Save(*http.Request, http.ResponseWriter, *sessions.Session) error {
	return nil
}

// A failed DELETE inside deletePost's transaction must not leave that
// transaction open. A leaked transaction pins its connection ("idle in
// transaction") and holds its locks until the process exits.
func TestDeletePostRollsBackOnFailedDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	cfg := config.New()
	// No blog cap: config.New() allows one, CreateUser makes it, and
	// CreateCollection enforces the cap since upstream GHSA fixes.
	cfg.App.MaxBlogs = 0
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	cfg.App.SingleUser = false
	cfg.App.Federation = false

	app := &App{cfg: cfg}
	db := openAppTestDB(t, app, "sqlite3", dbPath+"?parseTime=true&cached=shared")

	u := &User{Username: "alice", HashedPass: []byte("x")}
	if err := app.db.CreateUser(cfg, u, "", ""); err != nil {
		t.Fatalf("create user: %v", err)
	}
	coll, err := app.db.CreateCollection(cfg, "alice-blog", "alice-blog", u.ID)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	_, err = db.Exec(`INSERT INTO posts
(id, slug, text_appearance, language, rtl, privacy, owner_id, collection_id, created, updated, view_count, title, content)
VALUES ('p1', 'p1', 'norm', 'en', ?, 0, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0, 'T', 'B')`, false, u.ID, coll.ID)
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}
	// Make the DELETE inside deletePost's transaction fail. Trigger syntax
	// is per engine; each raises an error from a BEFORE DELETE trigger.
	failDelete := []string{`CREATE TRIGGER fail_post_delete BEFORE DELETE ON posts
BEGIN SELECT RAISE(ABORT, 'forced delete failure'); END`}
	switch engine, _ := testDBEngine(); engine {
	case driverMySQL:
		failDelete = []string{`CREATE TRIGGER fail_post_delete BEFORE DELETE ON posts
FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'forced delete failure'`}
	case driverPostgres:
		failDelete = []string{
			`CREATE FUNCTION fail_post_delete() RETURNS trigger LANGUAGE plpgsql AS
$$ BEGIN RAISE EXCEPTION 'forced delete failure'; END $$`,
			`CREATE TRIGGER fail_post_delete BEFORE DELETE ON posts
FOR EACH ROW EXECUTE FUNCTION fail_post_delete()`,
		}
	}
	for _, q := range failDelete {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
	}

	app.sessionStore = staticSessionStore{u: &User{ID: u.ID, Username: u.Username}}

	req := httptest.NewRequest(http.MethodDelete, "/api/posts/p1", nil)
	req = mux.SetURLVars(req, map[string]string{"post": "p1"})
	if err := deletePost(app, httptest.NewRecorder(), req); err == nil {
		t.Fatal("deletePost succeeded despite the forced failure")
	}

	if inUse := db.Stats().InUse; inUse != 0 {
		t.Fatalf("deletePost left %d connection(s) in use: its transaction was not rolled back", inUse)
	}
}
