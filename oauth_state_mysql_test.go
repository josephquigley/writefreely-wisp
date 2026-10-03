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
	"fmt"
	"os"
	"sync"
	"testing"
)

// TestValidateOAuthStateConcurrentMySQL races many validations of one state
// against MySQL. It connects with clientFoundRows=true, under which an UPDATE
// reports the rows it matched rather than the rows it changed — the same
// thing PostgreSQL reports. With the used = FALSE condition only in the
// SELECT, every caller that passed the SELECT before the first commit then
// matched the row in its UPDATE and "won". The condition must be in the UPDATE.
func TestValidateOAuthStateConcurrentMySQL(t *testing.T) {
	if !runMySQLTests() {
		t.Skip("skipping mysql tests")
	}
	host := os.Getenv("WF_HOST")
	if host == "" {
		host = "localhost"
	}
	name := os.Getenv("WF_DB")
	if name == "" {
		name = "writefreely"
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:3306)/%s?charset=utf8mb4&parseTime=true&clientFoundRows=true",
		os.Getenv("WF_USER"), os.Getenv("WF_PASSWORD"), host, name)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	ds := &datastore{DB: db, driverName: driverMySQL}

	const rounds, callers = 20, 32
	for round := 0; round < rounds; round++ {
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
