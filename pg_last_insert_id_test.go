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
	"testing"

	"github.com/writeas/web-core/activitystreams"
)

// lib/pq never implements sql.Result.LastInsertId, so every INSERT whose new
// row id is needed must get it some other way on Postgres.

func createPostgresTestUser(t *testing.T, app *App, username string) *User {
	t.Helper()
	u := &User{Username: username, HashedPass: []byte("x")}
	if err := app.db.CreateUser(app.cfg, u, "", ""); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func TestPostgresCreateCollectionID(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		u := createPostgresTestUser(t, app, "owner")

		c, err := app.db.CreateCollection(app.cfg, "second", "Second", u.ID)
		if err != nil {
			t.Fatalf("CreateCollection: %v", err)
		}
		if c.ID <= 0 {
			t.Fatalf("CreateCollection returned ID %d, want > 0", c.ID)
		}

		var id int64
		if err := app.db.QueryRow("SELECT id FROM collections WHERE alias = ?", "second").Scan(&id); err != nil {
			t.Fatalf("select collection: %v", err)
		}
		if c.ID != id {
			t.Fatalf("CreateCollection returned ID %d, row has %d", c.ID, id)
		}
	})
}

func TestPostgresInsertJobID(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		j := &PostJob{PostID: "abcdefghijklmnop", Action: "email", Delay: 5}
		if err := app.db.InsertJob(j); err != nil {
			t.Fatalf("InsertJob: %v", err)
		}
		if j.ID <= 0 {
			t.Fatalf("InsertJob set ID %d, want > 0", j.ID)
		}

		var id int64
		if err := app.db.QueryRow("SELECT id FROM publishjobs WHERE post_id = ?", j.PostID).Scan(&id); err != nil {
			t.Fatalf("select job: %v", err)
		}
		if j.ID != id {
			t.Fatalf("InsertJob set ID %d, row has %d", j.ID, id)
		}
	})
}

func TestPostgresAPAddRemoteUser(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		actor := &activitystreams.Person{
			Inbox: "https://remote.example/users/alice/inbox",
			URL:   "https://remote.example/@alice",
		}
		actor.ID = "https://remote.example/users/alice"
		actor.Endpoints.SharedInbox = "https://remote.example/inbox"
		actor.PublicKey.ID = "https://remote.example/users/alice#main-key"
		actor.PublicKey.PublicKeyPEM = "-----BEGIN PUBLIC KEY-----\ntest\n-----END PUBLIC KEY-----"

		tx, err := app.db.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		remoteUserID, err := apAddRemoteUser(app, tx, actor)
		if err != nil {
			t.Fatalf("apAddRemoteUser: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if remoteUserID <= 0 {
			t.Fatalf("apAddRemoteUser returned ID %d, want > 0", remoteUserID)
		}

		var id, keyOwner int64
		if err := app.db.QueryRow("SELECT id FROM remoteusers WHERE actor_id = ?", actor.ID).Scan(&id); err != nil {
			t.Fatalf("select remoteuser: %v", err)
		}
		if remoteUserID != id {
			t.Fatalf("apAddRemoteUser returned ID %d, row has %d", remoteUserID, id)
		}
		if err := app.db.QueryRow("SELECT remote_user_id FROM remoteuserkeys WHERE id = ?", actor.PublicKey.ID).Scan(&keyOwner); err != nil {
			t.Fatalf("select remoteuserkey: %v", err)
		}
		if keyOwner != id {
			t.Fatalf("remoteuserkeys.remote_user_id = %d, want %d", keyOwner, id)
		}
	})
}
