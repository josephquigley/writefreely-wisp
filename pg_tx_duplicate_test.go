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
	"testing"

	"github.com/writeas/web-core/activitystreams"
)

// seedFollowParents inserts the rows remotefollows and remoteuserkeys
// reference, returning a collection ID and a remote user ID.
func seedFollowParents(t *testing.T, app *App) (collID, remoteUserID int64) {
	t.Helper()
	var userID int64
	if err := app.db.QueryRow(`INSERT INTO users (username, password) VALUES ('alice', 'x') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := app.db.QueryRow(`INSERT INTO collections (alias, title, description, privacy, owner_id, view_count) VALUES ('alice', 'Alice', '', 0, $1, 0) RETURNING id`, userID).Scan(&collID); err != nil {
		t.Fatalf("insert collection: %v", err)
	}
	if err := app.db.QueryRow(`INSERT INTO remoteusers (actor_id, inbox, shared_inbox) VALUES ('https://remote.example/u/bob', 'https://remote.example/u/bob/inbox', '') RETURNING id`).Scan(&remoteUserID); err != nil {
		t.Fatalf("insert remote user: %v", err)
	}
	return collID, remoteUserID
}

// runTwiceInTx runs add once and commits, then runs it again for the same row
// in a new transaction and commits that. A re-delivered Follow does exactly
// this: the second insert hits an existing row, and the transaction must
// still commit.
func runTwiceInTx(t *testing.T, app *App, add func(*App, *sql.Tx) error) {
	t.Helper()
	for i := 1; i <= 2; i++ {
		tx, err := app.db.Begin()
		if err != nil {
			t.Fatalf("begin %d: %v", i, err)
		}
		if err := add(app, tx); err != nil {
			tx.Rollback()
			t.Fatalf("insert %d: %v", i, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %d after duplicate insert: %v", i, err)
		}
	}
}

func TestPostgresRemoteFollowDuplicateKeepsTx(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		collID, remoteUserID := seedFollowParents(t, app)
		runTwiceInTx(t, app, func(app *App, tx *sql.Tx) error {
			return apAddRemoteFollow(app, tx, collID, remoteUserID)
		})

		var n int
		if err := app.db.QueryRow(`SELECT COUNT(*) FROM remotefollows WHERE collection_id = $1 AND remote_user_id = $2`, collID, remoteUserID).Scan(&n); err != nil {
			t.Fatalf("count follows: %v", err)
		}
		if n != 1 {
			t.Fatalf("got %d follow rows, want 1", n)
		}
	})
}

func TestPostgresRemoteUserKeyDuplicateKeepsTx(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		_, remoteUserID := seedFollowParents(t, app)
		actor := &activitystreams.Person{}
		actor.PublicKey.ID = "https://remote.example/u/bob#main-key"
		actor.PublicKey.PublicKeyPEM = "-----BEGIN PUBLIC KEY-----\n-----END PUBLIC KEY-----"
		runTwiceInTx(t, app, func(app *App, tx *sql.Tx) error {
			return apAddRemoteUserKey(app, tx, actor, remoteUserID)
		})

		var n int
		if err := app.db.QueryRow(`SELECT COUNT(*) FROM remoteuserkeys WHERE remote_user_id = $1`, remoteUserID).Scan(&n); err != nil {
			t.Fatalf("count keys: %v", err)
		}
		if n != 1 {
			t.Fatalf("got %d key rows, want 1", n)
		}
	})
}
