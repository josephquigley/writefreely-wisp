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
)

// TestPostgresGetJobsToRun checks that a job is returned only once its post
// is between delay and delay+5 minutes old.
func TestPostgresGetJobsToRun(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		u := &User{Username: "jobs", HashedPass: []byte("x")}
		if err := app.db.CreateUser(app.cfg, u, "jobs", ""); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}

		// Posts stamped relative to the database clock, so the test does
		// not depend on the zone of the test process.
		for _, p := range []struct{ id, age string }{
			{"due", "12 minutes"},
			{"early", "1 minute"},
			{"late", "30 minutes"},
		} {
			_, err := app.db.Exec("INSERT INTO posts (id, title, content, privacy, owner_id, view_count, created, updated) VALUES (?, '', '', 0, ?, 0, NOW() - INTERVAL '"+p.age+"', NOW())", p.id, u.ID)
			if err != nil {
				t.Fatalf("insert post %s: %v", p.id, err)
			}
			if err := app.db.InsertJob(&PostJob{PostID: p.id, Action: "email", Delay: 10}); err != nil {
				t.Fatalf("InsertJob %s: %v", p.id, err)
			}
		}

		jobs, err := app.db.GetJobsToRun("email")
		if err != nil {
			t.Fatalf("GetJobsToRun: %v", err)
		}
		if len(jobs) != 1 || jobs[0].PostID != "due" {
			var got []string
			for _, j := range jobs {
				got = append(got, j.PostID)
			}
			t.Fatalf("GetJobsToRun returned posts %v, want [due]", got)
		}
	})
}
