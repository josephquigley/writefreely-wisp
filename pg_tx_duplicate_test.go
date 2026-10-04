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
)

// The follow handler in handleFetchCollectionInbox (activitypub.go, the
// remoteuserkeys insert at line 704 and the remotefollows insert at line 715)
// runs inside one transaction and forgives a duplicate-key error on each
// insert before calling Commit. That logic is inline in the HTTP handler, so
// this test runs the SAME statements, with the same QueryWrap, now() and
// isDuplicateKeyErr handling, rather than the handler itself.
//
// A remote server that delivers the same Follow twice takes this path a
// second time. On Postgres, the forgiven duplicate has already aborted the
// transaction, so the second Commit fails and the follow is lost.

func seedFollowRows(t *testing.T, app *App) (collID, remoteUserID int64) {
	t.Helper()
	var userID int64
	if err := app.db.QueryRow("INSERT INTO users (username, password) VALUES ('alice', 'x') RETURNING id").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := app.db.QueryRow("INSERT INTO collections (alias, title, description, privacy, owner_id, view_count) VALUES ('alice', 'Alice', '', 0, ?, 0) RETURNING id", userID).Scan(&collID); err != nil {
		t.Fatalf("insert collection: %v", err)
	}
	if err := app.db.QueryRow("INSERT INTO remoteusers (actor_id, inbox, shared_inbox) VALUES ('https://remote.example/u/bob', 'https://remote.example/u/bob/inbox', '') RETURNING id").Scan(&remoteUserID); err != nil {
		t.Fatalf("insert remote user: %v", err)
	}
	return collID, remoteUserID
}

// followHandlerInserts mirrors activitypub.go:703-722 for a follower whose
// key is being stored.
func followHandlerInserts(app *App, t *sql.Tx, collID, followerID int64, keyID, keyPEM string) error {
	_, err := t.Exec(app.db.QueryWrap("INSERT INTO remoteuserkeys (id, remote_user_id, public_key) VALUES (?, ?, ?)"), keyID, followerID, keyPEM)
	if err != nil {
		if !app.db.isDuplicateKeyErr(err) {
			return err
		}
	}
	_, err = t.Exec(app.db.QueryWrap("INSERT INTO remotefollows (collection_id, remote_user_id, created) VALUES (?, ?, "+app.db.now()+")"), collID, followerID)
	if err != nil {
		if !app.db.isDuplicateKeyErr(err) {
			return err
		}
	}
	return nil
}

func TestPostgresRepeatedFollowCommits(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		collID, followerID := seedFollowRows(t, app)
		for i := 1; i <= 2; i++ {
			tx, err := app.db.Begin()
			if err != nil {
				t.Fatalf("begin %d: %v", i, err)
			}
			if err := followHandlerInserts(app, tx, collID, followerID, "https://remote.example/u/bob#main-key", "pem"); err != nil {
				tx.Rollback()
				t.Fatalf("follow %d: %v", i, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit of follow %d: %v", i, err)
			}
		}
	})
}
