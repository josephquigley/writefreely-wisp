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
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/writeas/web-core/activitystreams"
	"github.com/writeas/web-core/auth"
	"github.com/writefreely/writefreely/config"
)

// The tests in this file cover WFPG-04: queries that were a syntax error on
// Postgres (MySQL's LIMIT offset, count; INSERT OR REPLACE; DATE_SUB with a
// column interval; RLIKE; LastInsertId). querySyntaxSuite runs the real
// datastore methods and is shared by a Postgres runner (here) and a SQLite
// runner (database_query_syntax_sqlite_test.go), so both dialects are held
// to the same behaviour.

func TestPostgresTagPattern(t *testing.T) {
	for tag, want := range map[string]string{
		"Tag": `#tag\y`,
		"g.x": `#g\.x\y`,
		"a(b": `#a\(b\y`,
	} {
		if got := postgresTagPattern(tag); got != want {
			t.Errorf("postgresTagPattern(%q) = %q, want %q", tag, got, want)
		}
	}
	if strings.Contains(postgresTagPattern("x"), `\b`) {
		t.Errorf(`postgresTagPattern uses \b, which is a backspace in a Postgres regex`)
	}
}

func TestPostgresOAuthUpsertConflictTarget(t *testing.T) {
	db := &datastore{driverName: driverPostgres}
	// RecordRemoteUserID used to pass "user", a reserved word and not a
	// column. The conflict target must be oauth_users_uk's columns.
	got := db.upsert("user_id", "provider", "client_id")
	want := "ON CONFLICT (user_id, provider, client_id) DO UPDATE SET"
	if got != want {
		t.Errorf("upsert = %q, want %q", got, want)
	}
}

// wfpg04PostgresDDL is a hand-made minimum of the tables the suite touches,
// in Postgres types, until WFPG-03's schema lands. Column names match the
// real schema; types are a best guess and not a statement about WFPG-03.
var wfpg04PostgresDDL = []string{
	`CREATE TABLE users (id SERIAL PRIMARY KEY, username VARCHAR(100) NOT NULL UNIQUE, password BYTEA NOT NULL, email BYTEA NULL, created TIMESTAMP NOT NULL DEFAULT NOW(), status INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE collections (id SERIAL PRIMARY KEY, alias VARCHAR(100) NULL UNIQUE, title VARCHAR(255) NOT NULL, description VARCHAR(160) NOT NULL, style_sheet TEXT NULL, script TEXT NULL, post_signature TEXT NULL, format VARCHAR(8) NULL, privacy SMALLINT NOT NULL, owner_id INTEGER NOT NULL, view_count INTEGER NOT NULL)`,
	`CREATE TABLE collectionredirects (prev_alias VARCHAR(100) PRIMARY KEY, new_alias VARCHAR(100) NOT NULL)`,
	`CREATE TABLE collectionattributes (collection_id INTEGER NOT NULL, attribute VARCHAR(128) NOT NULL, value VARCHAR(255) NOT NULL, PRIMARY KEY (collection_id, attribute))`,
	`CREATE TABLE collectionpasswords (collection_id INTEGER PRIMARY KEY, password TEXT NOT NULL)`,
	`CREATE TABLE posts (id VARCHAR(16) PRIMARY KEY, slug VARCHAR(100) NULL, text_appearance VARCHAR(4) NOT NULL DEFAULT 'norm', language CHAR(2) NULL, rtl BOOLEAN NULL, privacy SMALLINT NOT NULL, owner_id INTEGER NULL, collection_id INTEGER NULL, pinned_position SMALLINT NULL, created TIMESTAMP NOT NULL DEFAULT NOW(), updated TIMESTAMP NOT NULL DEFAULT NOW(), view_count INTEGER NOT NULL, title VARCHAR(160) NOT NULL, content TEXT NOT NULL)`,
	`CREATE TABLE appcontent (id VARCHAR(36) PRIMARY KEY, content TEXT NOT NULL, updated TIMESTAMP NOT NULL, title VARCHAR(255) NULL, content_type VARCHAR(36) NOT NULL DEFAULT 'page')`,
	`CREATE TABLE oauth_users (user_id INTEGER NOT NULL, remote_user_id VARCHAR(128) NOT NULL, provider VARCHAR(24) NOT NULL, client_id VARCHAR(128) NOT NULL, access_token VARCHAR(512) NOT NULL)`,
	`CREATE UNIQUE INDEX oauth_users_uk ON oauth_users (user_id, provider, client_id)`,
	`CREATE TABLE publishjobs (id SERIAL PRIMARY KEY, post_id VARCHAR(16) NOT NULL, action VARCHAR(16) NOT NULL, delay SMALLINT NOT NULL)`,
	`CREATE TABLE remoteusers (id SERIAL PRIMARY KEY, actor_id VARCHAR(255) NOT NULL UNIQUE, inbox VARCHAR(255) NOT NULL, shared_inbox VARCHAR(255) NOT NULL, url VARCHAR(255) NULL)`,
	`CREATE TABLE remoteuserkeys (id VARCHAR(255) PRIMARY KEY, remote_user_id INTEGER NOT NULL, public_key BYTEA NOT NULL)`,
}

// TestPostgresQuerySyntax runs the suite against a throwaway database on the
// server named by WF_TEST_PG_DSN (see `make test-postgres`). It creates its
// own database and tables, because the WFPG-02 per-test helper and the
// WFPG-03 schema were not available when it was written.
func TestPostgresQuerySyntax(t *testing.T) {
	dsn := os.Getenv("WF_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("WF_TEST_PG_DSN not set; run with `make test-postgres`")
	}

	maint, err := sql.Open(driverPostgresRebind, dsn)
	if err != nil {
		t.Fatalf("open maintenance db: %v", err)
	}
	defer maint.Close()
	name := fmt.Sprintf("wfpg04_%d", time.Now().UnixNano())
	if _, err := maint.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	defer func() {
		if _, err := maint.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	}()

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse WF_TEST_PG_DSN: %v", err)
	}
	u.Path = "/" + name
	sdb, err := sql.Open(driverPostgresRebind, u.String())
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer sdb.Close()
	for _, ddl := range wfpg04PostgresDDL {
		if _, err := sdb.Exec(ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	querySyntaxSuite(t, newDatastore(sdb, driverPostgres))
}

func querySyntaxSuite(t *testing.T, db *datastore) {
	cfg := config.New()
	cfg.App.MaxBlogs = testMaxBlogs
	cfg.App.Host = "http://localhost:0"
	cfg.App.DefaultVisibility = "public"
	app := &App{db: db, cfg: cfg}

	// ---- 5. Inserted IDs -------------------------------------------------
	var owner *User
	t.Run("CreateUser returns the new IDs", func(t *testing.T) {
		var prev int64
		for _, name := range []string{"alice", "bob"} {
			u := &User{Username: name, HashedPass: []byte("x")}
			if err := db.CreateUser(cfg, u, "", ""); err != nil {
				t.Fatalf("CreateUser(%s): %v", name, err)
			}
			var want int64
			if err := db.QueryRow("SELECT id FROM users WHERE username = ?", name).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if u.ID != want || u.ID == prev {
				t.Errorf("CreateUser(%s) set ID %d, row has %d (previous user %d)", name, u.ID, want, prev)
			}
			prev = u.ID
			owner = u
		}
	})
	if owner == nil {
		t.Fatal("no user; cannot continue")
	}

	var coll *Collection
	t.Run("CreateCollection returns the new ID", func(t *testing.T) {
		c, err := db.CreateCollection(cfg, "second", "Second", owner.ID)
		if err != nil {
			t.Fatalf("CreateCollection: %v", err)
		}
		var want int64
		if err := db.QueryRow("SELECT id FROM collections WHERE alias = ?", "second").Scan(&want); err != nil {
			t.Fatal(err)
		}
		if c.ID != want || c.ID == 0 {
			t.Errorf("CreateCollection ID = %d, row has %d", c.ID, want)
		}
		c.hostName = cfg.App.Host
		coll = c
	})
	if coll == nil {
		t.Fatal("no collection; cannot continue")
	}

	t.Run("apAddRemoteUser returns the new ID", func(t *testing.T) {
		var prev int64
		for i := 0; i < 2; i++ {
			actor := fmt.Sprintf("https://remote.example/users/r%d", i)
			p := &activitystreams.Person{}
			p.ID = actor
			p.Inbox = actor + "/inbox"
			p.Endpoints.SharedInbox = "https://remote.example/inbox"
			p.URL = actor
			p.PublicKey.ID = actor + "#main-key"
			p.PublicKey.PublicKeyPEM = "PEM"
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			id, err := apAddRemoteUser(app, tx, p)
			if err != nil {
				t.Fatalf("apAddRemoteUser: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var want int64
			if err := db.QueryRow("SELECT id FROM remoteusers WHERE actor_id = ?", actor).Scan(&want); err != nil {
				t.Fatal(err)
			}
			if id != want || id == prev {
				t.Errorf("apAddRemoteUser = %d, row has %d (previous %d)", id, want, prev)
			}
			prev = id
		}
	})

	// ---- 1. LIMIT n OFFSET m ---------------------------------------------
	// postsPerPage+3 posts with distinct created times: page 2 must be the
	// three oldest, newest first.
	n := postsPerPage + 3
	var newestFirst []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("pg%02d", i)
		// i minutes in the past, so pg00 is the newest.
		_, err := db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, language, created, updated, view_count, title, content) VALUES (?, ?, 0, ?, ?, 'en', "+db.dateSub(i+1, "MINUTE")+", "+db.now()+", 0, '', ?)", id, id, owner.ID, coll.ID, "page post #paged")
		if err != nil {
			t.Fatalf("insert post: %v", err)
		}
		// The same again with no collection, for GetAnonymousPosts.
		_, err = db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES (?, NULL, 0, ?, NULL, "+db.dateSub(i+1, "MINUTE")+", "+db.now()+", 0, '', 'anon')", "an"+id[2:], owner.ID)
		if err != nil {
			t.Fatalf("insert anonymous post: %v", err)
		}
		newestFirst = append(newestFirst, id)
	}
	wantPage2 := newestFirst[postsPerPage:]
	t.Run("pagination page 2", func(t *testing.T) {
		check := func(name string, posts *[]PublicPost, err error, want []string) {
			t.Helper()
			if err != nil {
				t.Errorf("%s: %v", name, err)
				return
			}
			if got := postIDs(posts); !sameStrings(got, want) {
				t.Errorf("%s page 2 = %v, want %v", name, got, want)
			}
		}
		pp, err := db.GetPosts(cfg, coll, 2, false, true, false, "")
		check("GetPosts", pp, err, wantPage2)
		pp, err = db.GetPostsTagged(cfg, coll, "paged", 2, false)
		check("GetPostsTagged", pp, err, wantPage2)
		pp, err = db.GetLangPosts(cfg, coll, "en", 2, false)
		check("GetLangPosts", pp, err, wantPage2)

		var anonWant []string
		for _, id := range wantPage2 {
			anonWant = append(anonWant, "an"+id[2:])
		}
		pp, err = db.GetAnonymousPosts(owner, 2)
		check("GetAnonymousPosts", pp, err, anonWant)
	})

	t.Run("GetAllUsers page 2", func(t *testing.T) {
		for i := 0; i < adminUsersPerPage; i++ {
			if _, err := db.Exec("INSERT INTO users (username, password, created) VALUES (?, ?, "+db.dateSub(i+10, "MINUTE")+")", fmt.Sprintf("pageuser%02d", i), []byte("x")); err != nil {
				t.Fatalf("insert user: %v", err)
			}
		}
		// alice and bob (created now) fill the top of page 1, so page 2
		// holds the two oldest page users.
		users, err := db.GetAllUsers(2)
		if err != nil {
			t.Fatalf("GetAllUsers: %v", err)
		}
		var got []string
		for _, u := range *users {
			got = append(got, u.Username)
		}
		if want := []string{"pageuser28", "pageuser29"}; !sameStrings(got, want) {
			t.Errorf("GetAllUsers(2) = %v, want %v", got, want)
		}
	})

	// ---- 2. Upserts ------------------------------------------------------
	t.Run("UpdateDynamicContent inserts then updates", func(t *testing.T) {
		for _, body := range []string{"first", "second"} {
			if err := db.UpdateDynamicContent("about", "About", body, "page"); err != nil {
				t.Fatalf("UpdateDynamicContent(%s): %v", body, err)
			}
			assertOneValue(t, db, body, "SELECT content FROM appcontent WHERE id = ?", "about")
		}
	})

	t.Run("RecordRemoteUserID inserts then updates", func(t *testing.T) {
		ctx := context.Background()
		for _, tok := range []string{"tok1", "tok2"} {
			if err := db.RecordRemoteUserID(ctx, owner.ID, "remote-1", "generic", "client", tok); err != nil {
				t.Fatalf("RecordRemoteUserID(%s): %v", tok, err)
			}
			assertOneValue(t, db, tok, "SELECT access_token FROM oauth_users WHERE user_id = ? AND provider = ? AND client_id = ?", owner.ID, "generic", "client")
		}
	})

	t.Run("SetCollectionAttribute inserts then updates", func(t *testing.T) {
		for _, v := range []string{"one", "two"} {
			if err := db.SetCollectionAttribute(coll.ID, "wfpg04", v); err != nil {
				t.Fatalf("SetCollectionAttribute(%s): %v", v, err)
			}
			assertOneValue(t, db, v, "SELECT value FROM collectionattributes WHERE collection_id = ? AND attribute = ?", coll.ID, "wfpg04")
		}
	})

	t.Run("UpdateCollection upserts inserts then updates", func(t *testing.T) {
		protected := int(CollProtected)
		for i, pointer := range []string{"$one.example", "$two.example"} {
			title := fmt.Sprintf("Second %d", i)
			sc := &SubmittedCollection{
				OwnerID:      uint64(owner.ID),
				MathJax:      true,
				Title:        &title,
				Monetization: &pointer,
				Visibility:   &protected,
				Pass:         fmt.Sprintf("pass%d", i),
			}
			if err := db.UpdateCollection(app, sc, coll.Alias); err != nil {
				t.Fatalf("UpdateCollection #%d: %v", i, err)
			}
			assertOneValue(t, db, "1", "SELECT value FROM collectionattributes WHERE collection_id = ? AND attribute = ?", coll.ID, "render_mathjax")
			assertOneValue(t, db, pointer, "SELECT value FROM collectionattributes WHERE collection_id = ? AND attribute = ?", coll.ID, "monetization_pointer")
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM collectionpasswords WHERE collection_id = ?", coll.ID).Scan(&n); err != nil || n != 1 {
				t.Errorf("collectionpasswords rows = %d (%v), want 1", n, err)
			}
			var hash []byte
			if err := db.QueryRow("SELECT password FROM collectionpasswords WHERE collection_id = ?", coll.ID).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			if !auth.Authenticated(hash, []byte(fmt.Sprintf("pass%d", i))) {
				t.Errorf("UpdateCollection #%d did not store the new password", i)
			}
		}
	})

	// ---- 3. Date arithmetic ----------------------------------------------
	t.Run("dateAdd and dateSub call sites", func(t *testing.T) {
		for _, expr := range []string{
			db.dateSub(6, "MONTH"),     // nodeinfo
			db.dateSub(1, "MONTH"),     // nodeinfo
			db.dateSub(3, "HOUR"),      // password reset
			db.dateAdd(3600, "SECOND"), // access token expiry
			db.dateAdd(-24, "HOUR"),    // orphaned images
		} {
			var c int
			if err := db.QueryRow("SELECT COUNT(*) FROM posts WHERE updated > " + expr).Scan(&c); err != nil {
				t.Errorf("%s: %v", expr, err)
			}
		}
	})

	t.Run("GetJobsToRun selects only jobs inside their window", func(t *testing.T) {
		// A job runs once its post is delay minutes old, for 5 minutes.
		for id, ago := range map[string]int{"jobin": 10, "jobold": 30, "jobnew": 3} {
			_, err := db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES (?, ?, 0, ?, NULL, "+db.dateSub(ago, "MINUTE")+", "+db.now()+", 0, '', 'job')", id, id, owner.ID)
			if err != nil {
				t.Fatalf("insert %s: %v", id, err)
			}
			if err := db.InsertJob(&PostJob{PostID: id, Action: "wfpg04", Delay: 7}); err != nil {
				t.Fatalf("InsertJob(%s): %v", id, err)
			}
		}
		jobs, err := db.GetJobsToRun("wfpg04")
		if err != nil {
			t.Fatalf("GetJobsToRun: %v", err)
		}
		var got []string
		for _, j := range jobs {
			got = append(got, j.PostID)
		}
		if want := []string{"jobin"}; !sameStrings(got, want) {
			t.Errorf("GetJobsToRun = %v, want %v", got, want)
		}
	})

	// ---- 4. Tag regex ----------------------------------------------------
	t.Run("tag queries find #tag but not #tagging", func(t *testing.T) {
		for id, content := range map[string]string{
			"tagplain": "about #tag today",
			"tagend":   "ends with #Tag",
			"tagdot":   "about #tag. today",
			"tagging":  "about #tagging today",
			"tagdotx":  "about #g.x today",
			"tagox":    "about #gox today",
		} {
			_, err := db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES (?, ?, 0, ?, ?, "+db.dateSub(1, "DAY")+", "+db.now()+", 0, '', ?)", id, id, owner.ID, coll.ID, content)
			if err != nil {
				t.Fatalf("insert %s: %v", id, err)
			}
		}
		for tag, want := range map[string][]string{
			"tag":  {"tagdot", "tagend", "tagplain"},
			"TAG":  {"tagdot", "tagend", "tagplain"},
			"g.x":  {"tagdotx"},
			"gox":  {"tagox"},
			"a(b":  nil,
			"g.*":  nil,
			"tagg": nil,
		} {
			ids, err := db.GetAllPostsTaggedIDs(coll, tag, false)
			if err != nil {
				t.Errorf("GetAllPostsTaggedIDs(%q): %v", tag, err)
			}
			if !sameStrings(sortedCopy(ids), want) {
				t.Errorf("GetAllPostsTaggedIDs(%q) = %v, want %v", tag, ids, want)
			}
			pp, err := db.GetPostsTagged(cfg, coll, tag, 1, false)
			if err != nil {
				t.Errorf("GetPostsTagged(%q): %v", tag, err)
				continue
			}
			if got := sortedCopy(postIDs(pp)); !sameStrings(got, want) {
				t.Errorf("GetPostsTagged(%q) = %v, want %v", tag, got, want)
			}
		}
	})
}

func assertOneValue(t *testing.T, db *datastore, want, query string, args ...interface{}) {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("%s = %v, want exactly [%s]", query, got, want)
	}
}

func sortedCopy(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
