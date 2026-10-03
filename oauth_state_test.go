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
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

// newOAuthStateTestDB opens a file-backed SQLite database holding only the
// oauth_client_states table, with immediate transactions and a busy timeout
// so that concurrent validations queue for the write lock instead of failing
// with SQLITE_BUSY.
func newOAuthStateTestDB(t *testing.T) *datastore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "oauth.db")
	db, err := sql.Open("sqlite3_with_regex", dbPath+"?parseTime=true&_busy_timeout=10000&_txlock=immediate")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE oauth_client_states (
		state VARCHAR(255) NOT NULL UNIQUE,
		used BOOLEAN NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP NOT NULL,
		provider VARCHAR(24) NOT NULL DEFAULT '',
		client_id VARCHAR(128) NOT NULL DEFAULT '',
		attach_user_id INTEGER NULL,
		invite_code CHAR(6) NULL
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return &datastore{DB: db, driverName: driverSQLite}
}

func TestValidateOAuthStateIsSingleUse(t *testing.T) {
	ctx := context.Background()
	ds := newOAuthStateTestDB(t)

	state, err := ds.GenerateOAuthState(ctx, "generic", "client", 0, "")
	if err != nil {
		t.Fatalf("GenerateOAuthState: %v", err)
	}

	provider, clientID, _, _, err := ds.ValidateOAuthState(ctx, state)
	if err != nil {
		t.Fatalf("first validation: %v", err)
	}
	if provider != "generic" || clientID != "client" {
		t.Fatalf("first validation returned provider=%q clientID=%q", provider, clientID)
	}

	if _, _, _, _, err := ds.ValidateOAuthState(ctx, state); err == nil {
		t.Fatal("second validation of the same state succeeded; want an error")
	}
	if _, _, _, _, err := ds.ValidateOAuthState(ctx, "no-such-state"); err == nil {
		t.Fatal("validation of an unknown state succeeded; want an error")
	}
}

func TestValidateOAuthStateConcurrentCallersOneWins(t *testing.T) {
	ctx := context.Background()
	ds := newOAuthStateTestDB(t)

	const callers = 8
	for round := 0; round < 10; round++ {
		state, err := ds.GenerateOAuthState(ctx, "generic", "client", 0, "")
		if err != nil {
			t.Fatalf("GenerateOAuthState: %v", err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				provider, _, _, _, err := ds.ValidateOAuthState(ctx, state)
				if err == nil && provider == "generic" {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()

		if wins != 1 {
			t.Fatalf("round %d: %d of %d concurrent validations of one state succeeded; want exactly 1", round, wins, callers)
		}
	}
}
