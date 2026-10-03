//go:build sqlite && !wflib
// +build sqlite,!wflib

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

// Tests for `db copy` (dbcopy.go). The Postgres ones need the sqlite tag as
// well as a server:
//
//	make test-postgres GOTESTFLAGS='-tags sqlite -run DBCopy -v'
//
// No database file is committed: buildDBCopyFixture generates the source
// from the real schema every run, mostly through the application's own
// datastore methods, plus raw rows for what older versions of the
// application (or other engines' habits) left behind.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/writeas/web-core/auth"
	"github.com/writeas/web-core/converter"
	"github.com/writefreely/writefreely/migrations"
)

// dbCopyFixture is what buildDBCopyFixture made, for the assertions.
type dbCopyFixture struct {
	app  *App // the SQLite app; its keys decrypt the copied emails
	path string

	alice        *User
	aliceColl    *Collection
	oauthUser    *User
	postIDs      []string // every post, for "view each post"
	collPostIDs  map[string]bool
	rtlPost      string
	nullRTLPost  string
	longTitle    string // post whose title is longer than varchar(160)
	maxTitle     string // post whose title is exactly 160 multi-byte runes
	dirtyPost    string // post whose title holds a NUL and invalid UTF-8
	legacyTokStr string // UUID form of an access token stored as TEXT
	legacyTok    []byte
	keptSubID    string // the confirmed one of the case-duplicate pair
	droppedSubID string
	maxUsername  string
	secrets      []string // values that must never appear in the output
}

// dbCopyLegacyToken is 16 bytes that are not valid UTF-8 and hold a NUL:
// stored as TEXT, the way access tokens were written before WFPG-05.
var dbCopyLegacyToken = []byte{0xff, 0x00, 0x80, 'a', 'B', 0xc3, 0x28, 1, 2, 3, 4, 5, 6, 7, 8, 9}

func dbCopyUUID(b []byte) string {
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func buildDBCopyFixture(t *testing.T) *dbCopyFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite3_with_regex", path+"?parseTime=true&cached=shared")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := valueTypesConfig()
	cfg.UseSQLite(true)
	cfg.Database.FileName = path
	app := &App{db: newDatastore(db, driverSQLite), cfg: cfg}
	if err := adminInitDatabase(app); err != nil {
		t.Fatalf("init fixture schema: %v", err)
	}
	prepareValueTypesApp(t, app)
	fx := &dbCopyFixture{app: app, path: path, collPostIDs: map[string]bool{}}
	ds := app.db
	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := ds.Exec(q, args...); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	str := func(s string) *string { return &s }

	// Users, through CreateUser: one with a password and an email, one
	// OAuth user with neither, and one whose username is the full 100.
	fx.alice = vtCreateUser(t, app, "alice", "alice pass", "alice@example.com")
	fx.oauthUser = vtCreateUser(t, app, "oauthbob", "", "")
	fx.maxUsername = strings.Repeat("m", 100)
	vtCreateUser(t, app, fx.maxUsername, "max pass", "")
	fx.secrets = append(fx.secrets, "alice@example.com")
	if fx.aliceColl, err = ds.GetCollection("alice"); err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	ctx := context.Background()
	if err := ds.RecordRemoteUserID(ctx, fx.oauthUser.ID, "4242", "generic", "client-1", "oauth-access-secret"); err != nil {
		t.Fatalf("RecordRemoteUserID: %v", err)
	}
	fx.secrets = append(fx.secrets, "oauth-access-secret")
	if _, err := ds.GenerateOAuthState(ctx, "generic", "client-1", 0, ""); err != nil {
		t.Fatalf("GenerateOAuthState: %v", err)
	}
	tok, err := ds.GetTemporaryOneTimeAccessToken(fx.alice.ID, 0, false)
	if err != nil {
		t.Fatalf("access token: %v", err)
	}
	fx.secrets = append(fx.secrets, tok)
	if _, err := ds.CreatePasswordResetToken(fx.alice.ID); err != nil {
		t.Fatalf("password reset: %v", err)
	}
	// A reset token from char(32) days, with its padding still on.
	exec("INSERT INTO password_resets (user_id, token, used, created) VALUES (?, ?, 1, '2024-01-02 03:04:05')", fx.alice.ID, "paddedtoken"+strings.Repeat(" ", 21))
	ds.GetAPActorKeys(fx.aliceColl.ID) // collectionkeys
	if err := ds.SetCollectionAttribute(fx.aliceColl.ID, "verification_link", "https://a.example/1\nhttps://b.example/2"); err != nil {
		t.Fatalf("collection attribute: %v", err)
	}
	exec("INSERT INTO userattributes (user_id, attribute, value) VALUES (?, 'theme', 'dark')", fx.alice.ID)
	exec("INSERT INTO collectionpasswords (collection_id, password) VALUES (?, ?)", fx.aliceColl.ID, "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0")
	exec("INSERT INTO collectionredirects (prev_alias, new_alias) VALUES ('oldalice', 'alice')")
	if err := ds.UpdateDynamicContent("about", "About", "About this instance.", "page"); err != nil {
		t.Fatalf("appcontent: %v", err)
	}

	// Posts, through CreatePost.
	mk := func(collID int64, title, body string, rtl converter.NullJSONBool, lang string, created string) string {
		t.Helper()
		p := &SubmittedPost{Title: str(title), Content: str(body), IsRTL: rtl}
		if lang != "" {
			p.Language = converter.NullJSONString{NullString: sql.NullString{String: lang, Valid: true}}
		}
		if created != "" {
			p.Created = &created
		}
		owner := fx.alice.ID
		post, err := ds.CreatePost(owner, collID, p)
		if err != nil {
			t.Fatalf("CreatePost(%q): %v", title, err)
		}
		fx.postIDs = append(fx.postIDs, post.ID)
		if collID > 0 {
			fx.collPostIDs[post.ID] = true
		}
		return post.ID
	}
	fx.rtlPost = mk(fx.aliceColl.ID, "Right to left", "שלום עולם #hebrew", converter.NullJSONBool{NullBool: sql.NullBool{Bool: true, Valid: true}}, "he", "")
	fx.nullRTLPost = mk(fx.aliceColl.ID, "No direction", "Plain words, with a #tag.", converter.NullJSONBool{}, "", "")
	scheduled := mk(fx.aliceColl.ID, "Scheduled", "From the future.", converter.NullJSONBool{}, "en", time.Now().Add(72*time.Hour).UTC().Format("2006-01-02T15:04:05Z"))
	pinned := mk(fx.aliceColl.ID, "Pinned", "Stays on top.", converter.NullJSONBool{}, "en", "")
	if err := ds.UpdatePostPinState(true, pinned, fx.aliceColl.ID, fx.alice.ID, 1); err != nil {
		t.Fatalf("pin: %v", err)
	}
	maxTitle := strings.Repeat("é", 160)
	fx.maxTitle = mk(fx.aliceColl.ID, maxTitle, "Title at the limit.", converter.NullJSONBool{}, "", "")
	anon := mk(0, "Anonymous", "Nobody's blog. Secret body text 7f3a.", converter.NullJSONBool{}, "", "")
	fx.secrets = append(fx.secrets, "Secret body text 7f3a")

	// Legacy and hostile rows that only raw SQL can write now.
	fx.longTitle = "longtitle1"
	exec("INSERT INTO posts (id, slug, title, content, text_appearance, privacy, owner_id, collection_id, created, updated, view_count) VALUES (?, 'long', ?, 'Long title.', 'norm', 0, ?, ?, '2020-05-06 07:08:09', '2020-05-06 07:08:09', 3)",
		fx.longTitle, strings.Repeat("L", 200), fx.alice.ID, fx.aliceColl.ID)
	fx.dirtyPost = "dirtypost1"
	exec("INSERT INTO posts (id, slug, title, content, text_appearance, privacy, owner_id, collection_id, created, updated, view_count) VALUES (?, 'dirty', ?, ?, 'norm', 0, ?, ?, '2021-01-01 12:00:00.123456789+02:00', '2021-01-01T10:00:00Z', 0)",
		fx.dirtyPost, "bad\x00title\xff", "body\x00with nul", fx.alice.ID, fx.aliceColl.ID)
	fx.postIDs = append(fx.postIDs, fx.longTitle, fx.dirtyPost)
	fx.collPostIDs[fx.longTitle], fx.collPostIDs[fx.dirtyPost] = true, true
	exec("INSERT INTO accesstokens (token, user_id, one_time) VALUES (?, ?, 0)", string(dbCopyLegacyToken), fx.alice.ID)
	fx.legacyTok = dbCopyLegacyToken
	fx.legacyTokStr = dbCopyUUID(dbCopyLegacyToken)
	fx.secrets = append(fx.secrets, fx.legacyTokStr, hex.EncodeToString(dbCopyLegacyToken))

	// Images.
	img, err := ds.CreatePostImage(fx.alice.ID, strings.Repeat("ab", 32), "alice/one.png", "one.png", "image/png", 1234)
	if err != nil {
		t.Fatalf("CreatePostImage: %v", err)
	}
	exec("UPDATE post_images SET post_id = ? WHERE id = ?", fx.rtlPost, img.ID)

	// Invites, with exact duplicates of the rows Postgres keys.
	if err := ds.CreateUserInvite("inv001", fx.alice.ID, 5, nil); err != nil {
		t.Fatalf("CreateUserInvite: %v", err)
	}
	exec("INSERT INTO userinvites (id, owner_id, max_uses, created, expires, inactive) VALUES ('inv002', ?, NULL, '2025-02-03 04:05:06', '2025-03-03 04:05:06', 1)", fx.alice.ID)
	exec("INSERT INTO userinvites (id, owner_id, max_uses, created, expires, inactive) VALUES ('inv002', ?, NULL, '2025-02-03 04:05:06', '2025-03-03 04:05:06', 1)", fx.alice.ID)
	exec("INSERT INTO usersinvited (invite_id, user_id) VALUES ('inv001', ?)", fx.oauthUser.ID)
	exec("INSERT INTO usersinvited (invite_id, user_id) VALUES ('inv001', ?)", fx.oauthUser.ID)

	// Email subscribers: a pair that differ only in case, the later one
	// confirmed (it must win), and a user subscription with no address.
	exec("INSERT INTO emailsubscribers (id, collection_id, user_id, email, subscribed, token, confirmed, allow_export) VALUES ('SUBold01', ?, NULL, 'Case@Example.COM', '2023-01-01 00:00:00', 'tokentokentoken1', 0, 0)", fx.aliceColl.ID)
	exec("INSERT INTO emailsubscribers (id, collection_id, user_id, email, subscribed, token, confirmed, allow_export) VALUES ('SUBnew01', ?, NULL, 'case@example.com', '2023-06-01 00:00:00', 'tokentokentoken2', 1, 1)", fx.aliceColl.ID)
	fx.droppedSubID, fx.keptSubID = "SUBold01", "SUBnew01"
	fx.secrets = append(fx.secrets, "Case@Example.COM", "case@example.com", "tokentokentoken1")
	if _, err := ds.AddEmailSubscription(fx.aliceColl.ID, 0, "other@example.com", true); err != nil {
		t.Fatalf("AddEmailSubscription: %v", err)
	}
	if _, err := ds.AddEmailSubscription(fx.aliceColl.ID, fx.oauthUser.ID, "", false); err != nil {
		t.Fatalf("AddEmailSubscription(user): %v", err)
	}

	// Federation: remote users (one with a mixed-case handle), keys, a
	// follow and a like.
	exec("INSERT INTO remoteusers (id, actor_id, inbox, shared_inbox, url, handle) VALUES (7, 'https://remote.example/users/Zed', 'https://remote.example/users/Zed/inbox', 'https://remote.example/inbox', 'https://remote.example/@Zed', 'Zed@Remote.Example')")
	exec("INSERT INTO remoteusers (id, actor_id, inbox, shared_inbox, url, handle) VALUES (9, 'https://other.example/u/amy', 'https://other.example/u/amy/inbox', '', NULL, NULL)")
	exec("INSERT INTO remoteuserkeys (id, remote_user_id, public_key) VALUES ('https://remote.example/users/Zed#main-key', 7, ?)", []byte("-----BEGIN PUBLIC KEY-----\nzed\n-----END PUBLIC KEY-----\n"))
	// V21: a saved setting and its version, so the copy replaces the seed row.
	exec("INSERT INTO app_settings (name, value) VALUES (?, ?)", "app.site_name", "Fixture")
	exec("UPDATE app_settings_version SET version = 3 WHERE id = 1")
	exec("INSERT INTO remotefollows (collection_id, remote_user_id, created) VALUES (?, 7, 1700000000)", fx.aliceColl.ID)
	exec("INSERT INTO remote_likes (post_id, remote_user_id, created) VALUES (?, 9, CURRENT_TIMESTAMP)", fx.rtlPost)

	// Publish jobs.
	if err := ds.InsertJob(&PostJob{PostID: scheduled, Action: "email", Delay: 5}); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}
	_ = anon
	return fx
}

func dbCopyCount(t *testing.T, q interface {
	QueryRow(string, ...interface{}) *sql.Row
}, table string) int {
	t.Helper()
	var n int
	if err := q.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// dbCopyRun copies fx into pg's database and returns the report.
func dbCopyRun(t *testing.T, fx *dbCopyFixture, dst *sql.DB, opts DBCopyOptions) (string, error) {
	t.Helper()
	src, err := sql.Open("sqlite3", "file:"+fx.path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var out bytes.Buffer
	opts.Out = &out
	err = copySQLiteToPostgres(context.Background(), src, dst, opts)
	return out.String(), err
}

func TestDBCopy_Postgres(t *testing.T) {
	fx := buildDBCopyFixture(t)
	pg := newPostgresTestApp(t, valueTypesConfig())
	prepareValueTypesApp(t, pg)
	pg.keys = fx.app.keys // the copied emails were encrypted with these

	out, err := dbCopyRun(t, fx, pg.db.DB, DBCopyOptions{})
	t.Logf("report:\n%s", out)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	for _, s := range fx.secrets {
		if strings.Contains(out, s) {
			t.Errorf("report contains a row value it must not print (%d bytes)", len(s))
		}
	}
	if !strings.Contains(out, "Verification passed.") || !strings.Contains(out, "Copy committed.") {
		t.Errorf("report does not say the copy verified and committed")
	}
	if !strings.Contains(out, fx.droppedSubID) || !strings.Contains(out, fx.keptSubID) {
		t.Errorf("report does not name the collapsed subscriber rows")
	}

	t.Run("row counts", func(t *testing.T) {
		collapsed := map[string]int{"emailsubscribers": 1, "userinvites": 1, "usersinvited": 1}
		tables, err := dbCopyTables(context.Background(), fx.app.db.DB, pg.db.DB)
		if err != nil {
			t.Fatal(err)
		}
		for _, tb := range tables {
			want := dbCopyCount(t, fx.app.db, tb.name) - collapsed[tb.name]
			if got := dbCopyCount(t, pg.db, tb.name); got != want {
				t.Errorf("%s: %d rows, want %d", tb.name, got, want)
			}
			if dbCopyCount(t, fx.app.db, tb.name) == 0 {
				t.Errorf("fixture has no rows in %s; it must cover every table", tb.name)
			}
		}
	})

	t.Run("values", func(t *testing.T) {
		var tok []byte
		var sudo bool
		if err := pg.db.QueryRow("SELECT token, sudo FROM accesstokens WHERE token = ?", fx.legacyTok).Scan(&tok, &sudo); err != nil {
			t.Fatalf("legacy TEXT token not copied byte for byte: %v", err)
		}
		var rtl sql.NullBool
		pg.db.QueryRow("SELECT rtl FROM posts WHERE id = ?", fx.rtlPost).Scan(&rtl)
		if !rtl.Valid || !rtl.Bool {
			t.Errorf("rtl post: rtl = %v, want true", rtl)
		}
		pg.db.QueryRow("SELECT rtl FROM posts WHERE id = ?", fx.nullRTLPost).Scan(&rtl)
		if rtl.Valid {
			t.Errorf("rtl NULL became %v", rtl.Bool)
		}
		var title string
		pg.db.QueryRow("SELECT title FROM posts WHERE id = ?", fx.longTitle).Scan(&title)
		if title != strings.Repeat("L", 160) {
			t.Errorf("over-long title not truncated to 160 (got %d)", utf8.RuneCountInString(title))
		}
		pg.db.QueryRow("SELECT title FROM posts WHERE id = ?", fx.maxTitle).Scan(&title)
		if title != strings.Repeat("é", 160) {
			t.Errorf("160-character title changed")
		}
		var content string
		var created, updated time.Time
		pg.db.QueryRow("SELECT title, content, created, updated FROM posts WHERE id = ?", fx.dirtyPost).Scan(&title, &content, &created, &updated)
		if title != "badtitle�" || content != "bodywith nul" {
			t.Errorf("NUL / invalid UTF-8 not cleaned: %q %q", title, content)
		}
		if want := time.Date(2021, 1, 1, 10, 0, 0, 123456000, time.UTC); !created.Equal(want) {
			t.Errorf("created with +02:00 offset = %v, want %v", created.UTC(), want)
		}
		if want := time.Date(2021, 1, 1, 10, 0, 0, 0, time.UTC); !updated.Equal(want) {
			t.Errorf("updated with Z = %v, want %v", updated.UTC(), want)
		}
		var followed time.Time
		pg.db.QueryRow("SELECT created FROM remotefollows").Scan(&followed)
		if !followed.Equal(time.Unix(1700000000, 0)) {
			t.Errorf("integer datetime = %v", followed)
		}
		var n int
		pg.db.QueryRow("SELECT COUNT(*) FROM password_resets WHERE token = 'paddedtoken'").Scan(&n)
		if n != 1 {
			t.Error("char(32) padding not trimmed from password_resets.token")
		}
		var handle string
		pg.db.QueryRow("SELECT handle FROM remoteusers WHERE id = 7").Scan(&handle)
		if handle != "zed@remote.example" {
			t.Errorf("handle = %q, want lower case", handle)
		}
		var email string
		var confirmed bool
		if err := pg.db.QueryRow("SELECT email, confirmed FROM emailsubscribers WHERE id = ?", fx.keptSubID).Scan(&email, &confirmed); err != nil {
			t.Fatalf("kept subscriber: %v", err)
		}
		if email != "case@example.com" || !confirmed {
			t.Errorf("kept subscriber = %q confirmed=%v", email, confirmed)
		}
		var remoteID string
		pg.db.QueryRow("SELECT remote_user_id FROM oauth_users WHERE user_id = ?", fx.oauthUser.ID).Scan(&remoteID)
		if remoteID != "4242" {
			t.Errorf("oauth_users.remote_user_id = %q", remoteID)
		}
	})

	t.Run("app reads the copy", func(t *testing.T) {
		u, err := pg.db.GetUserForAuth("alice")
		if err != nil {
			t.Fatalf("GetUserForAuth: %v", err)
		}
		if !auth.Authenticated(u.HashedPass, []byte("alice pass")) {
			t.Error("alice's password does not authenticate on the copy")
		}
		full, err := pg.db.GetUserByID(u.ID)
		if err != nil {
			t.Fatalf("GetUserByID: %v", err)
		}
		if e := full.EmailClear(pg.keys); e != "alice@example.com" {
			t.Errorf("email ciphertext did not survive (decrypts to %d bytes)", len(e))
		}
		if set, _ := pg.db.IsUserPassSet(fx.oauthUser.ID); set {
			t.Error("OAuth user without a password now has one")
		}
		if name, err := pg.db.GetUserNameFromToken(fx.legacyTokStr); err != nil || name != "alice" {
			t.Errorf("legacy token lookup = %q, %v", name, err)
		}
		if _, err := pg.db.GetUserForAuth(fx.maxUsername); err != nil {
			t.Errorf("100-character username: %v", err)
		}
		for _, id := range fx.postIDs {
			if fx.collPostIDs[id] {
				var slug string
				if err := pg.db.QueryRow("SELECT slug FROM posts WHERE id = ?", id).Scan(&slug); err != nil {
					t.Errorf("post %s: %v", id, err)
					continue
				}
				if _, err := pg.db.GetPost(slug, fx.aliceColl.ID); err != nil {
					t.Errorf("GetPost(%s): %v", id, err)
				}
			} else if _, err := vtViewPost(pg, id); err != nil {
				t.Errorf("view anonymous post %s: %v", id, err)
			}
		}
		coll, err := pg.db.GetCollection("alice")
		if err != nil {
			t.Fatalf("GetCollection: %v", err)
		}
		posts, err := pg.db.GetPosts(pg.cfg, coll, 1, false, false, true, postArch)
		if err != nil || len(*posts) == 0 {
			t.Errorf("GetPosts = %v, %v", posts, err)
		}
		if tagged, err := pg.db.GetPostsTagged(pg.cfg, coll, "hebrew", 1, false); err != nil || len(*tagged) != 1 {
			t.Errorf("GetPostsTagged(hebrew) = %v, %v", tagged, err)
		}

		// A new signup proves the users and collections sequences moved on.
		nu := vtCreateUser(t, pg, "newcomer", "new pass", "")
		var maxBefore int64
		fx.app.db.QueryRow("SELECT MAX(id) FROM users").Scan(&maxBefore)
		if nu.ID <= maxBefore {
			t.Errorf("new user got id %d, not above the copied %d", nu.ID, maxBefore)
		}
		if _, err := pg.db.GetCollection("newcomer"); err != nil {
			t.Errorf("new user's collection: %v", err)
		}
		if err := pg.db.InsertJob(&PostJob{PostID: fx.rtlPost, Action: "email", Delay: 1}); err != nil {
			t.Errorf("new publish job: %v", err)
		}
		if _, err := pg.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox) VALUES ('https://n.example/a', 'https://n.example/a/i', '')"); err != nil {
			t.Errorf("new remote user: %v", err)
		}

		// Resubscribing the case-duplicate address finds the kept row.
		s, err := pg.db.AddEmailSubscription(fx.aliceColl.ID, 0, "CASE@example.com", false)
		if err != nil {
			t.Fatalf("resubscribe: %v", err)
		}
		if s.ID != fx.keptSubID {
			t.Errorf("resubscribe returned subscriber %s, want %s", s.ID, fx.keptSubID)
		}
	})

	t.Run("second run refused", func(t *testing.T) {
		_, err := dbCopyRun(t, fx, pg.db.DB, DBCopyOptions{})
		if err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("second copy: err = %v, want refusal for a non-empty target", err)
		}
	})

	t.Run("verify-only", func(t *testing.T) {
		// The subtests above wrote new rows; take them out again so the
		// copy matches its source.
		for _, q := range []string{
			"DELETE FROM collections WHERE alias = 'newcomer'",
			"DELETE FROM users WHERE username = 'newcomer'",
			"DELETE FROM publishjobs WHERE delay = 1",
			"DELETE FROM remoteusers WHERE actor_id = 'https://n.example/a'",
		} {
			if _, err := pg.db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		out, err := dbCopyRun(t, fx, pg.db.DB, DBCopyOptions{VerifyOnly: true})
		if err != nil {
			t.Fatalf("verify-only on an intact copy: %v\n%s", err, out)
		}
		if _, err := pg.db.Exec("UPDATE posts SET view_count = view_count + 1 WHERE id = ?", fx.nullRTLPost); err != nil {
			t.Fatal(err)
		}
		out, err = dbCopyRun(t, fx, pg.db.DB, DBCopyOptions{VerifyOnly: true})
		if err == nil {
			t.Fatalf("verify-only passed with a changed row:\n%s", out)
		}
		if !strings.Contains(err.Error(), "posts") || strings.Contains(err.Error(), "users") {
			t.Errorf("verify-only error does not name just the posts table: %v", err)
		}
	})
}

// TestDBCopyRefusals_Postgres covers what the command must refuse, and that
// a refused copy leaves the target as it was.
func TestDBCopyRefusals_Postgres(t *testing.T) {
	t.Run("source behind", func(t *testing.T) {
		fx := buildDBCopyFixture(t)
		pg := newPostgresTestApp(t, nil)
		if _, err := fx.app.db.Exec("DELETE FROM appmigrations WHERE version > 17"); err != nil {
			t.Fatal(err)
		}
		_, err := dbCopyRun(t, fx, pg.db.DB, DBCopyOptions{})
		if err == nil || !strings.Contains(err.Error(), "V17") || !strings.Contains(err.Error(), "db migrate") {
			t.Fatalf("V17 source: err = %v", err)
		}
	})

	t.Run("target not initialised", func(t *testing.T) {
		fx := buildDBCopyFixture(t)
		empty := newPostgresTestDB(t)
		if _, err := dbCopyRun(t, fx, empty, DBCopyOptions{}); err == nil {
			t.Fatal("copy into a database without the schema succeeded")
		}
	})

	t.Run("dry run writes nothing", func(t *testing.T) {
		fx := buildDBCopyFixture(t)
		pg := newPostgresTestApp(t, nil)
		out, err := dbCopyRun(t, fx, pg.db.DB, DBCopyOptions{DryRun: true})
		if err != nil {
			t.Fatalf("dry run: %v", err)
		}
		if !strings.Contains(out, "Dry run") || !strings.Contains(out, "emailsubscribers") {
			t.Errorf("dry-run report:\n%s", out)
		}
		if n := dbCopyCount(t, pg.db, "users"); n != 0 {
			t.Errorf("dry run wrote %d users", n)
		}
	})

	t.Run("conflicting duplicate key rolls back", func(t *testing.T) {
		fx := buildDBCopyFixture(t)
		pg := newPostgresTestApp(t, nil)
		if _, err := fx.app.db.Exec("INSERT INTO remoteuserkeys (id, remote_user_id, public_key) VALUES ('https://remote.example/users/Zed#main-key', 9, ?)", []byte("other key")); err != nil {
			t.Fatal(err)
		}
		_, err := dbCopyRun(t, fx, pg.db.DB, DBCopyOptions{})
		if err == nil || !strings.Contains(err.Error(), "remoteuserkeys") {
			t.Fatalf("conflicting remoteuserkeys: err = %v", err)
		}
		if strings.Contains(err.Error(), "remote.example") {
			t.Errorf("error prints a key value: %v", err)
		}
		if n := dbCopyCount(t, pg.db, "users"); n != 0 {
			t.Errorf("a refused copy left %d users behind", n)
		}
	})

	t.Run("over-long key column refused", func(t *testing.T) {
		fx := buildDBCopyFixture(t)
		pg := newPostgresTestApp(t, nil)
		if _, err := fx.app.db.Exec("UPDATE collectionredirects SET prev_alias = ?", strings.Repeat("a", 101)); err != nil {
			t.Fatal(err)
		}
		_, err := dbCopyRun(t, fx, pg.db.DB, DBCopyOptions{})
		if err == nil || !strings.Contains(err.Error(), "collectionredirects") || !strings.Contains(err.Error(), "prev_alias") {
			t.Fatalf("over-long prev_alias: err = %v", err)
		}
	})
}

func TestDBCopyVersionPin(t *testing.T) {
	if v := migrations.CurrentVer(); v != dbCopySchemaVersion {
		t.Fatalf("migrations are at V%d but db copy knows V%d: review dbcopy.go's column rules for the new migrations, then raise dbCopySchemaVersion", v, dbCopySchemaVersion)
	}
}

func TestParseSQLiteTime(t *testing.T) {
	utc := func(y int, mo time.Month, d, h, mi, s, ns int) time.Time {
		return time.Date(y, mo, d, h, mi, s, ns, time.UTC)
	}
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"2024-01-02 03:04:05", utc(2024, 1, 2, 3, 4, 5, 0)},
		{"2024-01-02T03:04:05Z", utc(2024, 1, 2, 3, 4, 5, 0)},
		{"2024-01-02 03:04:05.123456789+00:00", utc(2024, 1, 2, 3, 4, 5, 123456000)},
		{"2024-01-02 03:04:05-05:00", utc(2024, 1, 2, 8, 4, 5, 0)},
		{"2024-01-02", utc(2024, 1, 2, 0, 0, 0, 0)},
		{"1700000000", time.Unix(1700000000, 0).UTC()},
		{"2024-01-02 03:04:05.5 +0100 CET m=+0.001", utc(2024, 1, 2, 2, 4, 5, 500000000)},
	} {
		got, err := parseSQLiteTime(c.in)
		if err != nil || !got.Equal(c.want) || got.Location() != time.UTC {
			t.Errorf("parseSQLiteTime(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "yesterday", "2024-13-45 99:99:99"} {
		if _, err := parseSQLiteTime(bad); err == nil {
			t.Errorf("parseSQLiteTime(%q) accepted", bad)
		}
	}
}

func TestDBCopySourcePath(t *testing.T) {
	if p, err := dbCopySourcePath("sqlite:/data/writefreely.db"); err != nil || p != "/data/writefreely.db" {
		t.Errorf("got %q, %v", p, err)
	}
	for _, bad := range []string{"", "sqlite:", "/data/writefreely.db", "mysql:x"} {
		if _, err := dbCopySourcePath(bad); err == nil {
			t.Errorf("dbCopySourcePath(%q) accepted", bad)
		}
	}
}
