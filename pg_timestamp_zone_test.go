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
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

// The Postgres schema declares every time column TIMESTAMP (without time
// zone). Values reach those columns from two places: SQL NOW(), which stores
// the wall clock of the session TimeZone, and Go time.Time arguments, whose
// offset Postgres discards, keeping the Go value's own wall clock. lib/pq
// reads a TIMESTAMP back as UTC. The stored instants are therefore only
// right when both the session and the bound Go values are UTC, whatever zone
// the server and the app process happen to run in.
//
// The tests reconnect through connectToDatabase, so they see whatever
// production's connection string does or does not set for the session.
//
// These tests put the server's database default and the process zone in
// America/Detroit, in each combination, and check that times written through
// production code paths come back as the instants that were written.

// setLocal makes Go treat zone as the process's local time zone, as if the
// app had been started with TZ=zone, and returns a func restoring the old one.
func setLocal(t *testing.T, zone string) func() {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatalf("load %s: %v", zone, err)
	}
	old := time.Local
	time.Local = loc
	return func() { time.Local = old }
}

func TestPostgresTimestampsIgnoreZones(t *testing.T) {
	const detroit = "America/Detroit"
	cases := []struct {
		name      string
		processTZ string // "" leaves the process zone as the environment set it
		dbTZ      string // server-side default TimeZone for the database
	}{
		{"database default non-UTC", "UTC", detroit},
		{"process zone non-UTC", detroit, "UTC"},
		{"both non-UTC", detroit, detroit},
		{"environment process zone, database non-UTC", "", detroit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withPostgresTestApp(t, func(app *App) {
				// Stand-in for a server whose postgresql.conf timezone is not
				// UTC (initdb copies the host's zone): new sessions on this
				// database start in dbTZ unless the client says otherwise.
				var dbName string
				if err := app.db.QueryRow("SELECT current_database()").Scan(&dbName); err != nil {
					t.Fatalf("current_database: %v", err)
				}
				if _, err := app.db.Exec("ALTER DATABASE " + pq.QuoteIdentifier(dbName) + " SET timezone TO " + pq.QuoteLiteral(tc.dbTZ)); err != nil {
					t.Fatalf("alter database timezone: %v", err)
				}
				defer connectLikeProduction(t, app, adminUser(t), adminPassword(t), false)()
				if tc.processTZ != "" {
					defer setLocal(t, tc.processTZ)()
				}

				var sessionTZ string
				if err := app.db.QueryRow("SHOW timezone").Scan(&sessionTZ); err != nil {
					t.Fatalf("show timezone: %v", err)
				}
				t.Logf("process zone %s, session TimeZone %s", time.Local, sessionTZ)

				checkTimestampRoundTrips(t, app)
			})
		})
	}
}

func checkTimestampRoundTrips(t *testing.T, app *App) {
	const slack = time.Minute

	var uid, cid int64
	if err := app.db.QueryRow("INSERT INTO users (username, password, email) VALUES (?, ?, NULL) RETURNING id", "tzuser", strings.Repeat("x", 60)).Scan(&uid); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := app.db.QueryRow("INSERT INTO collections (alias, title, description, privacy, owner_id, view_count) VALUES (?, ?, '', 1, ?, 0) RETURNING id", "tzuser", "tzuser", uid).Scan(&cid); err != nil {
		t.Fatalf("insert collection: %v", err)
	}

	// users.created comes from the column default, CURRENT_TIMESTAMP.
	u, err := app.db.GetUserByID(uid)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if d := time.Since(u.Created); d < -slack || d > slack {
		t.Errorf("users.created (SQL default) read back as %s, %s away from now", u.Created.UTC(), d)
	}

	// userinvites: created from NOW(), expires bound as a Go time.Time.
	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := app.db.CreateUserInvite("tzinvite", uid, 1, &expires); err != nil {
		t.Fatalf("CreateUserInvite: %v", err)
	}
	inv, err := app.db.GetUserInvite("tzinvite")
	if err != nil {
		t.Fatalf("GetUserInvite: %v", err)
	}
	if d := time.Since(inv.Created); d < -slack || d > slack {
		t.Errorf("userinvites.created (NOW()) read back as %s, %s away from now", inv.Created.UTC(), d)
	}
	if inv.Expires == nil || !inv.Expires.Equal(expires) {
		t.Errorf("userinvites.expires (Go value) wrote %s, read back %v", expires.UTC(), inv.Expires)
	}
	if inv.Expired() {
		t.Errorf("invite expiring in an hour reads as already expired (expires %v)", inv.Expires)
	}

	// posts: created bound as Go time.Now(), updated from NOW().
	title, content := "tz", "timestamps"
	created, err := app.db.CreatePost(uid, cid, &SubmittedPost{Title: &title, Content: &content})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	p, err := app.db.GetPost(created.ID, 0)
	if err != nil {
		t.Fatalf("GetPost: %v", err)
	}
	if d := time.Since(p.Created); d < -slack || d > slack {
		t.Errorf("posts.created (Go value) read back as %s, %s away from now", p.Created.UTC(), d)
	}
	if d := time.Since(p.Updated); d < -slack || d > slack {
		t.Errorf("posts.updated (NOW()) read back as %s, %s away from now", p.Updated.UTC(), d)
	}
	if d := p.Updated.Sub(p.Created); d < -slack || d > slack {
		t.Errorf("posts.created and posts.updated were stamped together but are %s apart", d)
	}
}

// adminUser and adminPassword return the role WF_TEST_PG_DSN connects as.
func adminUser(t *testing.T) string     { return testDSNValue(t, "user") }
func adminPassword(t *testing.T) string { return testDSNValue(t, "password") }

func testDSNValue(t *testing.T, key string) string {
	t.Helper()
	for _, kv := range strings.Fields(postgresTestDSN(t)) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}
