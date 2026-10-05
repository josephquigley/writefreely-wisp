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

// WFPG-05: every value the code writes and reads has a Go type that suits
// its column on every engine, and every SQL literal is valid on all three.
// Booleans are `boolean` on Postgres and tinyint/INTEGER elsewhere; binary
// columns are bytea, binary/varbinary, and TEXT on SQLite.
//
// runValueTypesSuite holds the checks. It is run against Postgres here
// (TestValueTypesPostgres), against MySQL here (TestValueTypesMySQL), and
// against SQLite in value_types_sqlite_test.go.

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/schema"
	uuid "github.com/nu7hatch/gouuid"
	"github.com/writeas/impart"
	"github.com/writeas/web-core/auth"
	"github.com/writeas/web-core/converter"
	"github.com/writefreely/writefreely/config"
	"github.com/writefreely/writefreely/key"
)

var valueTypesTemplatesOnce sync.Once

func valueTypesConfig() *config.Config {
	cfg := config.New()
	cfg.App.Host = "http://localhost:0"
	cfg.App.SingleUser = false
	cfg.App.MinUsernameLen = 3
	cfg.Server.HashSeed = "test-hash-seed"
	return cfg
}

// prepareValueTypesApp gives a schema-loaded app what the HTTP handlers in
// the suite touch: keys, a session store, a form decoder and templates.
func prepareValueTypesApp(t *testing.T, app *App) {
	t.Helper()
	app.keys = &key.Keychain{}
	if err := app.keys.GenerateKeys(); err != nil {
		t.Fatalf("generate keys: %v", err)
	}
	app.formDecoder = schema.NewDecoder()
	app.InitSession()
	var err error
	valueTypesTemplatesOnce.Do(func() {
		err = InitTemplates(app.cfg)
	})
	if err != nil {
		t.Fatalf("init templates: %v", err)
	}
}

func TestValueTypesPostgres(t *testing.T) {
	if !runPostgresTests() {
		t.Skipf("skipping postgres test: %s is not %q", envTestDBType, driverPostgres)
	}
	runValueTypesSuite(t, func(t *testing.T) *App {
		app := newPostgresTestApp(t, valueTypesConfig())
		prepareValueTypesApp(t, app)
		return app
	})
}

// TestValueTypesMySQL runs the suite on a fresh MySQL database with the
// real schema: from the harness under WF_TEST_DB_TYPE=mysql, otherwise
// created on the TEST_MYSQL server and dropped afterwards.
func TestValueTypesMySQL(t *testing.T) {
	if !runAnyMySQLTests() {
		t.Skip("skipping mysql tests")
	}
	runValueTypesSuite(t, func(t *testing.T) *App {
		if runMySQLHarnessTests() {
			app := newMySQLTestApp(t, valueTypesConfig())
			prepareValueTypesApp(t, app)
			return app
		}
		u, _ := uuid.NewV4()
		name := "wf_vt_" + strings.Replace(u.String(), "-", "", -1)
		if _, err := testDB.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4"); err != nil {
			t.Fatalf("create database: %v", err)
		}
		host := os.Getenv("WF_HOST")
		if host == "" {
			host = "localhost"
		}
		db, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s:3306)/%s?charset=utf8mb4&parseTime=true", os.Getenv("WF_USER"), os.Getenv("WF_PASSWORD"), host, name))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() {
			db.Close()
			testDB.Exec("DROP DATABASE " + name)
		})
		cfg := valueTypesConfig()
		cfg.Database.Type = driverMySQL
		app := &App{db: newDatastore(db, driverMySQL), cfg: cfg}
		if err := adminInitDatabase(app); err != nil {
			t.Fatalf("init schema: %v", err)
		}
		prepareValueTypesApp(t, app)
		return app
	})
}

// vtCreateUser creates a user the way signup does: a bcrypt hash (empty for
// an OAuth user without a password) and an encrypted email, if any. The
// hash is at bcrypt.MinCost (testHashPass) rather than signup's cost 12:
// what these suites check is how the hash is stored and read back, and
// auth.Authenticated verifies it either way.
func vtCreateUser(t *testing.T, app *App, username, pass, email string) *User {
	t.Helper()
	hashed := []byte{}
	if pass != "" {
		hashed = testHashPass(t, pass)
	}
	u := &User{
		Username:   username,
		HashedPass: hashed,
		HasPass:    pass != "",
		Email:      prepareUserEmail(email, app.keys.EmailKey),
		Created:    time.Now().Truncate(time.Second).UTC(),
	}
	if err := app.db.CreateUser(app.cfg, u, username, ""); err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	return u
}

func runValueTypesSuite(t *testing.T, newApp func(t *testing.T) *App) {
	t.Run("signup with email", func(t *testing.T) {
		app := newApp(t)
		u := vtCreateUser(t, app, "emailuser", "correct horse", "someone@example.com")

		var stored []byte
		if err := app.db.QueryRow("SELECT email FROM users WHERE id = ?", u.ID).Scan(&stored); err != nil {
			t.Fatalf("select email: %v", err)
		}
		if string(stored) != u.Email.String {
			t.Errorf("stored ciphertext differs from what was written (%d bytes vs %d)", len(stored), len(u.Email.String))
		}
		got, err := app.db.GetUserByID(u.ID)
		if err != nil {
			t.Fatalf("GetUserByID: %v", err)
		}
		if e := got.EmailClear(app.keys); e != "someone@example.com" {
			t.Errorf("EmailClear = %q, want someone@example.com", e)
		}
		if set, err := app.db.IsUserPassSet(u.ID); err != nil || !set {
			t.Errorf("IsUserPassSet = %v, %v; want true", set, err)
		}
		authUser, err := app.db.GetUserForAuth("emailuser")
		if err != nil {
			t.Fatalf("GetUserForAuth: %v", err)
		}
		if !auth.Authenticated(authUser.HashedPass, []byte("correct horse")) {
			t.Error("stored password hash does not authenticate the password")
		}

		// ChangePassphrase and ChangeSettings write the hash again.
		newHash, _ := auth.HashPass([]byte("battery staple"))
		if err := app.db.ChangePassphrase(u.ID, true, "", newHash); err != nil {
			t.Fatalf("ChangePassphrase: %v", err)
		}
		authUser, _ = app.db.GetUserForAuth("emailuser")
		if !auth.Authenticated(authUser.HashedPass, []byte("battery staple")) {
			t.Error("password set by ChangePassphrase does not authenticate")
		}
		u.HashedPass = authUser.HashedPass
		if err := app.db.ChangeSettings(app, u, &userSettings{OldPass: "battery staple", NewPass: "third pass"}); err != nil {
			t.Fatalf("ChangeSettings: %v", err)
		}
		authUser, _ = app.db.GetUserForAuth("emailuser")
		if !auth.Authenticated(authUser.HashedPass, []byte("third pass")) {
			t.Error("password set by ChangeSettings does not authenticate")
		}
	})

	t.Run("oauth user without password", func(t *testing.T) {
		app := newApp(t)
		u := vtCreateUser(t, app, "oauthuser", "", "")
		if set, err := app.db.IsUserPassSet(u.ID); err != nil || set {
			t.Errorf("IsUserPassSet = %v, %v; want false (a CHAR column would pad the empty password)", set, err)
		}
		if !app.db.DoesUserNeedAuth(u.ID) {
			t.Error("DoesUserNeedAuth = false for a user with no password and no email")
		}
	})

	t.Run("password reset", func(t *testing.T) {
		app := newApp(t)
		u := vtCreateUser(t, app, "resetuser", "pass1234", "")
		tok, err := app.db.CreatePasswordResetToken(u.ID)
		if err != nil {
			t.Fatalf("CreatePasswordResetToken: %v", err)
		}
		if got := app.db.GetUserFromPasswordReset(tok); got != u.ID {
			t.Fatalf("GetUserFromPasswordReset = %d, want %d", got, u.ID)
		}
		if err := app.db.ConsumePasswordResetToken(tok); err != nil {
			t.Fatalf("ConsumePasswordResetToken: %v", err)
		}
		if got := app.db.GetUserFromPasswordReset(tok); got != 0 {
			t.Errorf("GetUserFromPasswordReset after use = %d, want 0", got)
		}
	})

	t.Run("invite creation", func(t *testing.T) {
		app := newApp(t)
		u := vtCreateUser(t, app, "inviter", "pass1234", "")
		if err := app.db.CreateUserInvite("vtinv1", u.ID, 5, nil); err != nil {
			t.Fatalf("CreateUserInvite: %v", err)
		}
		i, err := app.db.GetUserInvite("vtinv1")
		if err != nil {
			t.Fatalf("GetUserInvite: %v", err)
		}
		if i.Inactive {
			t.Error("new invite is inactive")
		}
		if !i.MaxUses.Valid || i.MaxUses.Int64 != 5 {
			t.Errorf("MaxUses = %v, want 5", i.MaxUses)
		}
		is, err := app.db.GetUserInvites(u.ID)
		if err != nil {
			t.Fatalf("GetUserInvites: %v", err)
		}
		if len(*is) != 1 || (*is)[0].ID != "vtinv1" || (*is)[0].Inactive {
			t.Errorf("GetUserInvites = %+v", *is)
		}
	})

	t.Run("confirmed subscribers", func(t *testing.T) {
		app := newApp(t)
		u := vtCreateUser(t, app, "subowner", "pass1234", "owner@example.com")
		c, err := app.db.GetCollection("subowner")
		if err != nil {
			t.Fatalf("GetCollection: %v", err)
		}
		a, err := app.db.AddEmailSubscription(c.ID, 0, "a@example.com", false)
		if err != nil {
			t.Fatalf("AddEmailSubscription(a): %v", err)
		}
		if _, err := app.db.AddEmailSubscription(c.ID, 0, "b@example.com", true); err != nil {
			t.Fatalf("AddEmailSubscription(b): %v", err)
		}
		if _, err := app.db.AddEmailSubscription(c.ID, u.ID, "", true); err != nil {
			t.Fatalf("AddEmailSubscription(user): %v", err)
		}

		subs, err := app.db.GetEmailSubscribers(c.ID, true)
		if err != nil {
			t.Fatalf("GetEmailSubscribers(confirmed): %v", err)
		}
		if len(subs) != 2 {
			t.Fatalf("confirmed subscribers = %d, want 2", len(subs))
		}
		for _, s := range subs {
			if !s.Confirmed {
				t.Errorf("subscriber %s listed as confirmed has Confirmed = false", s.ID)
			}
			if s.UserID.Valid {
				if e := s.FinalEmail(app.keys); e != "owner@example.com" {
					t.Errorf("user subscriber email = %q, want owner@example.com", e)
				}
			}
		}
		if all, _ := app.db.GetEmailSubscribers(c.ID, false); len(all) != 3 {
			t.Errorf("all subscribers = %d, want 3", len(all))
		}

		if app.db.IsSubscriberConfirmed("a@example.com") {
			t.Error("IsSubscriberConfirmed(a) before confirming = true")
		}
		if !app.db.IsSubscriberConfirmed("b@example.com") {
			t.Error("IsSubscriberConfirmed(b) = false")
		}
		if err := app.db.UpdateSubscriberConfirmed(a.ID, a.Token); err != nil {
			t.Fatalf("UpdateSubscriberConfirmed: %v", err)
		}
		if !app.db.IsSubscriberConfirmed("a@example.com") {
			t.Error("IsSubscriberConfirmed(a) after confirming = false")
		}
		if subs, _ := app.db.GetEmailSubscribers(c.ID, true); len(subs) != 3 {
			t.Errorf("confirmed subscribers after confirming a = %d, want 3", len(subs))
		}
		s, err := app.db.FetchEmailSubscriber("a@example.com", 0, c.ID)
		if err != nil || s == nil || !s.Confirmed || s.AllowExport {
			t.Errorf("FetchEmailSubscriber(a) = %+v, %v; want confirmed, not allow_export", s, err)
		}
	})

	t.Run("access tokens", func(t *testing.T) {
		app := newApp(t)
		u := vtCreateUser(t, app, "tokenuser", "pass1234", "")

		// UUID bytes are random, so most of these are not valid UTF-8,
		// which Postgres refuses as text.
		for i := 0; i < 32; i++ {
			tok, err := app.db.GetAccessToken(u.ID)
			if err != nil {
				t.Fatalf("GetAccessToken: %v", err)
			}
			if got := app.db.GetUserID(tok); got != u.ID {
				t.Fatalf("GetUserID(%s) = %d, want %d", tok, got, u.ID)
			}
			if name, err := app.db.GetUserNameFromToken(tok); err != nil || name != "tokenuser" {
				t.Fatalf("GetUserNameFromToken = %q, %v", name, err)
			}
			if id, name, err := app.db.GetUserDataFromToken(tok); err != nil || id != u.ID || name != "tokenuser" {
				t.Fatalf("GetUserDataFromToken = %d, %q, %v", id, name, err)
			}
		}
		if last := app.db.FetchLastAccessToken(u.ID); app.db.GetUserID(last) != u.ID {
			t.Errorf("FetchLastAccessToken returned %q, which does not log in", last)
		}

		// one_time is written from a Go bool and read back as one.
		once, err := app.db.GetTemporaryOneTimeAccessToken(u.ID, 60, true)
		if err != nil {
			t.Fatalf("GetTemporaryOneTimeAccessToken: %v", err)
		}
		if got := app.db.GetUserID(once); got != u.ID {
			t.Fatalf("one-time token first use = %d, want %d", got, u.ID)
		}
		if got := app.db.GetUserID(once); got != -1 {
			t.Errorf("one-time token second use = %d, want -1", got)
		}

		tok, _ := app.db.GetAccessToken(u.ID)
		bin := auth.GetToken(tok)
		if err := app.db.DeleteToken(bin); err != nil {
			t.Fatalf("DeleteToken: %v", err)
		}
		if got := app.db.GetUserID(tok); got != -1 {
			t.Errorf("deleted token still logs in as %d", got)
		}
	})

	t.Run("collection password", func(t *testing.T) {
		app := newApp(t)
		u := vtCreateUser(t, app, "lockedblog", "pass1234", "")
		vis := int(CollProtected)
		if err := app.db.UpdateCollection(app, &SubmittedCollection{OwnerID: uint64(u.ID), Visibility: &vis, Pass: "open sesame"}, "lockedblog"); err != nil {
			t.Fatalf("UpdateCollection: %v", err)
		}
		unlock := func(pass string) error {
			form := url.Values{"alias": {"lockedblog"}, "password": {pass}}
			r := httptest.NewRequest("POST", "/api/auth/read", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return handleWebCollectionUnlock(app, httptest.NewRecorder(), r)
		}
		if err := unlock("open sesame"); !isHTTPStatus(err, http.StatusFound) {
			t.Errorf("unlock with the right password: %v, want a redirect", err)
		}
		if err := unlock("wrong"); !isHTTPStatus(err, http.StatusUnauthorized) {
			t.Errorf("unlock with a wrong password: %v, want 401", err)
		}

		// A second update takes the upsert path.
		if err := app.db.UpdateCollection(app, &SubmittedCollection{OwnerID: uint64(u.ID), Visibility: &vis, Pass: "new sesame"}, "lockedblog"); err != nil {
			t.Fatalf("UpdateCollection again: %v", err)
		}
		if err := unlock("new sesame"); !isHTTPStatus(err, http.StatusFound) {
			t.Errorf("unlock with the changed password: %v, want a redirect", err)
		}
	})

	t.Run("anonymous post rtl", func(t *testing.T) {
		app := newApp(t)
		for _, c := range vtRTLCases {
			// Inserted directly, binding rtl as the sql.NullBool CreatePost
			// binds, so this runs before CreatePost is ported (WFPG-06).
			postID := "vtrtl" + c.name
			for len(postID) < postIDLen {
				postID += "x"
			}
			content := "Body of the " + c.name + " post."
			_, err := app.db.Exec("INSERT INTO posts (id, slug, title, content, text_appearance, language, rtl, privacy, owner_id, collection_id, created, updated, view_count) VALUES (?, NULL, ?, ?, 'norm', NULL, ?, 0, NULL, NULL, "+app.db.now()+", "+app.db.now()+", 0)", postID, "Title "+c.name, content, c.rtl)
			if err != nil {
				t.Fatalf("insert post (%s): %v", c.name, err)
			}
			body, err := vtViewPost(app, postID)
			if err != nil {
				t.Fatalf("view anonymous post (%s): %v", c.name, err)
			}
			if !vtHTMLDir(body, c.dir) {
				t.Errorf("view anonymous post (%s): no dir=%q on <html>", c.name, c.dir)
			}
			if !strings.Contains(body, content) {
				t.Errorf("view anonymous post (%s): content missing", c.name)
			}
		}
	})

	t.Run("CreatePost and rtl", func(t *testing.T) {
		app := newApp(t)
		skipIfUnported(t, func() {
			title, content := "probe", "probe"
			app.db.CreatePost(-1, -1, &SubmittedPost{Title: &title, Content: &content})
		})
		for _, c := range vtRTLCases {
			title, content := "Title "+c.name, "Body of the "+c.name+" post."
			p, err := app.db.CreatePost(-1, -1, &SubmittedPost{Title: &title, Content: &content, IsRTL: converter.NullJSONBool{NullBool: c.rtl}})
			if err != nil {
				t.Fatalf("CreatePost(%s): %v", c.name, err)
			}

			// posts.id was CHAR(16) on MySQL; it must come back unpadded.
			var storedID string
			var storedRTL sql.NullBool
			if err := app.db.QueryRow("SELECT id, rtl FROM posts WHERE id = ?", p.ID).Scan(&storedID, &storedRTL); err != nil {
				t.Fatalf("select post: %v", err)
			}
			if len(storedID) != postIDLen || storedID != p.ID {
				t.Errorf("stored post id %q (%d chars), want %q (%d chars)", storedID, len(storedID), p.ID, postIDLen)
			}
			if storedRTL != c.rtl {
				t.Errorf("%s: stored rtl %+v, want %+v", c.name, storedRTL, c.rtl)
			}
			body, err := vtViewPost(app, p.ID)
			if err != nil {
				t.Fatalf("view anonymous post (%s): %v", c.name, err)
			}
			if !vtHTMLDir(body, c.dir) {
				t.Errorf("view anonymous post (%s): no dir=%q on <html>", c.name, c.dir)
			}
		}

		// An owned post: UpdateOwnedPost writes rtl from a Go bool, and
		// GetUserPosts reads it back.
		u := vtCreateUser(t, app, "rtlowner", "pass1234", "")
		title, content := "Owned", "Owned post body."
		p, err := app.db.CreatePost(u.ID, -1, &SubmittedPost{Title: &title, Content: &content})
		if err != nil {
			t.Fatalf("CreatePost(owned): %v", err)
		}
		err = app.db.UpdateOwnedPost(&AuthenticatedPost{ID: p.ID, SubmittedPost: &SubmittedPost{IsRTL: converter.NullJSONBool{NullBool: sql.NullBool{Bool: true, Valid: true}}}}, u.ID)
		if err != nil {
			t.Fatalf("UpdateOwnedPost: %v", err)
		}
		posts, err := app.db.GetUserPosts(u)
		if err != nil {
			t.Fatalf("GetUserPosts: %v", err)
		}
		if len(*posts) != 1 || !(*posts)[0].RTL.Valid || !(*posts)[0].RTL.Bool {
			t.Errorf("GetUserPosts = %+v, want one post with rtl true", *posts)
		}
	})
}

var vtRTLCases = []struct {
	name string
	rtl  sql.NullBool
	dir  string
}{
	{"null", sql.NullBool{}, "auto"},
	{"true", sql.NullBool{Bool: true, Valid: true}, "rtl"},
	{"false", sql.NullBool{Bool: false, Valid: true}, "ltr"},
}

// vtHTMLDir reports whether the page's <html> element has dir="dir".
func vtHTMLDir(body, dir string) bool {
	i := strings.Index(body, "<html")
	if i < 0 {
		return false
	}
	j := strings.Index(body[i:], ">")
	return j > 0 && strings.Contains(body[i:i+j], `dir="`+dir+`"`)
}

// skipIfUnported runs probe and skips the test if it panics because the
// function it calls has not been ported to this driver yet.
func skipIfUnported(t *testing.T, probe func()) {
	t.Helper()
	msg := func() (msg string) {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
				if !strings.Contains(msg, "not implemented for database driver") {
					panic(r)
				}
			}
		}()
		probe()
		return ""
	}()
	if msg != "" {
		t.Skipf("skipping until ported: %s", msg)
	}
}

// vtViewPost renders an anonymous post's page through handleViewPost.
func vtViewPost(app *App, id string) (string, error) {
	r := httptest.NewRequest("GET", "/"+id, nil)
	r = mux.SetURLVars(r, map[string]string{"post": id})
	w := httptest.NewRecorder()
	if err := handleViewPost(app, w, r); err != nil {
		return "", err
	}
	return w.Body.String(), nil
}

func isHTTPStatus(err error, status int) bool {
	if he, ok := err.(impart.HTTPError); ok {
		return he.Status == status
	}
	return false
}
