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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gorilla/mux"
	"github.com/writeas/impart"
	"github.com/writeas/web-core/activitystreams"
	"github.com/writefreely/writefreely/config"
	"github.com/writefreely/writefreely/key"
)

func TestSanitizeDBText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"nul\x00byte", "nulbyte"},
		{"bad\xffbyte", "bad�byte"},
		{"\x00\xff\xfe\x00", "�"}, // a run of invalid bytes is one replacement
		{"café ☕", "café ☕"},
	}
	for _, c := range cases {
		if got := sanitizeDBText(c.in); got != c.want {
			t.Errorf("sanitizeDBText(%q) = %q, want %q", c.in, got, c.want)
		}
		if !isValidDBText(sanitizeDBText(c.in)) {
			t.Errorf("sanitizeDBText(%q) is not itself valid", c.in)
		}
	}
	if isValidDBText("a\x00b") || isValidDBText("a\xffb") {
		t.Error("isValidDBText accepted a NUL byte or invalid UTF-8")
	}
}

func TestBoundedDBTextCutsOnRuneBoundary(t *testing.T) {
	// 159 single-byte runes then a three-byte one: a byte-wise cut at 160
	// would split the euro sign.
	s := strings.Repeat("a", 159) + "€" + strings.Repeat("b", 240)
	got := boundedDBText(s, postMaxLengthTitle)
	if n := utf8.RuneCountInString(got); n != postMaxLengthTitle {
		t.Errorf("got %d runes, want %d", n, postMaxLengthTitle)
	}
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "€") {
		t.Errorf("cut split or dropped the multi-byte rune: ...%q", got[len(got)-6:])
	}
	if got := boundedDBText("short", 160); got != "short" {
		t.Errorf("short value changed: %q", got)
	}
}

func TestSubmittedPostSanitizeForStorage(t *testing.T) {
	title := strings.Repeat("t", 400)
	content := "body\x00with\xffjunk"
	slug := "sl\x00ug"
	p := &SubmittedPost{Title: &title, Content: &content, Slug: &slug}
	p.Language.String, p.Language.Valid = "en-US", true
	p.sanitizeForStorage()

	if n := utf8.RuneCountInString(*p.Title); n != postMaxLengthTitle {
		t.Errorf("title has %d runes, want %d", n, postMaxLengthTitle)
	}
	if *p.Content != "bodywith�junk" {
		t.Errorf("content = %q", *p.Content)
	}
	if *p.Slug != "slug" {
		t.Errorf("slug = %q", *p.Slug)
	}
	if p.Language.String != "en" {
		t.Errorf("language = %q, want %q", p.Language.String, "en")
	}

	var nilPost *SubmittedPost
	nilPost.sanitizeForStorage() // must not panic
}

func TestUnmarshalActorSanitizes(t *testing.T) {
	keyID := "https://remote.example/users/x#" + strings.Repeat("k", 300)
	body := fmt.Sprintf(`{"id":"https://remote.example/users/x","type":"Person",`+
		`"name":"Na\u0000me","summary":"sum\u0000mary",`+
		`"inbox":"https://remote.example/users/x/in\u0000box",`+
		`"publicKey":{"id":%q,"owner":"https://remote.example/users/x","publicKeyPem":"PEM\u0000"}}`, keyID)
	a := &activitystreams.Person{}
	if err := unmarshalActor([]byte(body), a); err != nil {
		t.Fatalf("unmarshalActor: %v", err)
	}
	for name, v := range map[string]string{"name": a.Name, "summary": a.Summary, "inbox": a.Inbox, "pem": a.PublicKey.PublicKeyPEM} {
		if strings.ContainsRune(v, 0) {
			t.Errorf("%s still contains a NUL byte: %q", name, v)
		}
	}
	// The key ID is compared against a signature's keyId by the allowlist
	// check, so it must come through whole; only the remoteuserkeys inserts
	// bound it to the column.
	if a.PublicKey.ID != keyID {
		t.Errorf("key id was changed: has %d runes, want %d", utf8.RuneCountInString(a.PublicKey.ID), utf8.RuneCountInString(keyID))
	}
}

// TestLengthBounds_SQLite runs the input-bound checks against SQLite.
func TestLengthBounds_SQLite(t *testing.T) {
	app, _ := newTemplateTestApp(t, func(cfg *config.Config) {
		cfg.Server.StaticParentDir = t.TempDir()
		cfg.Uploads.Enabled = true
		cfg.Uploads.MaxSizeMB = 1
	})
	u, coll, _ := createTemplateTestUser(t, app, "bounds")
	exerciseLengthBounds(t, app, u, coll)
}

// TestLengthBounds_Postgres runs the same checks against Postgres, where an
// over-long value, a NUL byte or invalid UTF-8 is an error rather than a
// silent truncation.
func TestLengthBounds_Postgres(t *testing.T) {
	cfg := config.New()
	cfg.App.Host = "http://localhost:8080"
	cfg.Server.StaticParentDir = t.TempDir()
	cfg.Uploads.Enabled = true
	cfg.Uploads.MaxSizeMB = 1
	app := newPostgresTestApp(t, cfg)
	app.keys = &key.Keychain{}
	if err := app.keys.GenerateKeys(); err != nil {
		t.Fatalf("generate keys: %v", err)
	}
	app.InitSession()

	u := &User{Username: "bounds", HashedPass: []byte("x")}
	if err := app.db.CreateUser(app.cfg, u, "", ""); err != nil {
		t.Fatalf("create user: %v", err)
	}
	coll, err := app.db.GetCollection(u.Username)
	if err != nil {
		t.Fatalf("get collection: %v", err)
	}
	exerciseLengthBounds(t, app, u, coll)
}

func exerciseLengthBounds(t *testing.T, app *App, u *User, coll *Collection) {
	longTitle := strings.Repeat("a", 159) + "€" + strings.Repeat("b", 240)
	wantTitle := strings.Repeat("a", 159) + "€"
	dirtyBody := "before\x00middle\xffafter"
	wantBody := "beforemiddle�after"

	readPost := func(t *testing.T, id string) (title, content string) {
		t.Helper()
		if err := app.db.QueryRow("SELECT title, content FROM posts WHERE id = ?", id).Scan(&title, &content); err != nil {
			t.Fatalf("read post %s: %v", id, err)
		}
		return
	}

	t.Run("create post truncates title and cleans body", func(t *testing.T) {
		title, body := longTitle, dirtyBody
		p := createPostOrSkip(t, app, u.ID, coll.ID, &SubmittedPost{Title: &title, Content: &body})
		gotTitle, gotBody := readPost(t, p.ID)
		if gotTitle != wantTitle {
			t.Errorf("stored title is %d runes ending %q, want %d ending %q",
				utf8.RuneCountInString(gotTitle), lastRunes(gotTitle, 3), postMaxLengthTitle, lastRunes(wantTitle, 3))
		}
		if gotBody != wantBody {
			t.Errorf("stored body = %q, want %q", gotBody, wantBody)
		}
	})

	t.Run("update post truncates title and cleans body", func(t *testing.T) {
		id := insertTestPost(t, app, u.ID, coll.ID)
		title, body := longTitle, dirtyBody
		err := app.db.UpdateOwnedPost(&AuthenticatedPost{ID: id, SubmittedPost: &SubmittedPost{Title: &title, Content: &body}}, u.ID)
		if err != nil {
			t.Fatalf("UpdateOwnedPost: %v", err)
		}
		gotTitle, gotBody := readPost(t, id)
		if gotTitle != wantTitle {
			t.Errorf("stored title is %d runes ending %q, want %q", utf8.RuneCountInString(gotTitle), lastRunes(gotTitle, 3), lastRunes(wantTitle, 3))
		}
		if gotBody != wantBody {
			t.Errorf("stored body = %q, want %q", gotBody, wantBody)
		}
	})

	t.Run("update post ignores an unknown font", func(t *testing.T) {
		id := insertTestPost(t, app, u.ID, coll.ID)
		body := "still updates"
		err := app.db.UpdateOwnedPost(&AuthenticatedPost{ID: id, SubmittedPost: &SubmittedPost{Content: &body, Font: "not-a-font"}}, u.ID)
		if err != nil {
			t.Fatalf("UpdateOwnedPost: %v", err)
		}
		var font string
		if err := app.db.QueryRow("SELECT text_appearance FROM posts WHERE id = ?", id).Scan(&font); err != nil {
			t.Fatal(err)
		}
		if font != "norm" {
			t.Errorf("text_appearance = %q, want norm", font)
		}
	})

	t.Run("create collection truncates title", func(t *testing.T) {
		c, err := app.db.CreateCollection(app.cfg, "boundscoll", strings.Repeat("c", 300)+"\x00", u.ID)
		if err != nil {
			t.Fatalf("CreateCollection: %v", err)
		}
		var got string
		if err := app.db.QueryRow("SELECT title FROM collections WHERE id = ?", c.ID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != strings.Repeat("c", collMaxLengthTitle) {
			t.Errorf("stored title has %d runes, want %d", utf8.RuneCountInString(got), collMaxLengthTitle)
		}
	})

	t.Run("update collection cleans and truncates", func(t *testing.T) {
		title := strings.Repeat("d", 300)
		desc := strings.Repeat("e", 200) + "\x00"
		sig := "sig\x00\xff"
		err := app.db.UpdateCollection(app, &SubmittedCollection{OwnerID: uint64(u.ID), Title: &title, Description: &desc, Signature: &sig}, coll.Alias)
		if err != nil {
			t.Fatalf("UpdateCollection: %v", err)
		}
		var gotTitle, gotDesc, gotSig string
		if err := app.db.QueryRow("SELECT title, description, post_signature FROM collections WHERE id = ?", coll.ID).Scan(&gotTitle, &gotDesc, &gotSig); err != nil {
			t.Fatal(err)
		}
		if utf8.RuneCountInString(gotTitle) != collMaxLengthTitle || utf8.RuneCountInString(gotDesc) != collMaxLengthDescription {
			t.Errorf("title %d runes, description %d runes", utf8.RuneCountInString(gotTitle), utf8.RuneCountInString(gotDesc))
		}
		if gotSig != "sig�" {
			t.Errorf("signature = %q", gotSig)
		}
	})

	t.Run("overlong email is refused with 400", func(t *testing.T) {
		for name, email := range map[string]string{
			"300 characters": strings.Repeat("x", 288) + "@example.com",
			"NUL byte":       "a\x00b@example.com",
		} {
			body := fmt.Sprintf(`{"email":%q}`, email)
			r := httptest.NewRequest("POST", "/api/collections/"+coll.Alias+"/email/subscribe", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r = mux.SetURLVars(r, map[string]string{"alias": coll.Alias})
			err := handleCreateEmailSubscription(app, httptest.NewRecorder(), r)
			he, ok := err.(impart.HTTPError)
			if !ok || he.Status != http.StatusBadRequest {
				t.Errorf("%s: got %v, want a 400", name, err)
			}
		}
		var n int
		if err := app.db.QueryRow("SELECT COUNT(*) FROM emailsubscribers").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d subscribers stored, want 0", n)
		}
	})

	t.Run("300-character file name uploads", func(t *testing.T) {
		name := strings.Repeat("f", 296) + ".png"
		rec, status := doUpload(t, app, u, uploadRequest(t, name, "image/png", tinyPNG(t)))
		if status != http.StatusOK {
			t.Fatalf("upload status %d: %s", status, rec.Body.String())
		}
		var got string
		if err := app.db.QueryRow("SELECT filename FROM post_images WHERE owner_id = ?", u.ID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if utf8.RuneCountInString(got) != postImageMaxLengthFilename {
			t.Errorf("stored filename has %d runes, want %d", utf8.RuneCountInString(got), postImageMaxLengthFilename)
		}
	})

	// The inbox stores nothing of a remote Note: Like and Follow are the
	// activities that write, and what they write is the remote actor. So
	// this is the remote content that reaches the database.
	t.Run("remote actor with NUL bytes is stored", func(t *testing.T) {
		body := `{"id":"https://remote.example/users/nul","type":"Person","name":"N\u0000","summary":"S\u0000",` +
			`"inbox":"https://remote.example/users/nul/in\u0000box","url":"https://remote.example/@n\u0000ul",` +
			`"endpoints":{"sharedInbox":"https://remote.example/in\u0000box"},` +
			`"publicKey":{"id":"https://remote.example/users/nul#` + strings.Repeat("k", 300) + `","owner":"https://remote.example/users/nul","publicKeyPem":"PEM\u0000"}}`
		a := &activitystreams.Person{}
		if err := unmarshalActor([]byte(body), a); err != nil {
			t.Fatalf("unmarshalActor: %v", err)
		}
		tx, err := app.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := apAddRemoteUser(app, tx, a); err != nil {
			t.Fatalf("apAddRemoteUser: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var inbox string
		if err := app.db.QueryRow("SELECT inbox FROM remoteusers WHERE actor_id = ?", "https://remote.example/users/nul").Scan(&inbox); err != nil {
			t.Fatal(err)
		}
		if inbox != "https://remote.example/users/nul/inbox" {
			t.Errorf("inbox = %q", inbox)
		}
		var keyID string
		if err := app.db.QueryRow("SELECT k.id FROM remoteuserkeys k JOIN remoteusers u ON u.id = k.remote_user_id WHERE u.actor_id = ?", "https://remote.example/users/nul").Scan(&keyID); err != nil {
			t.Fatalf("read key: %v", err)
		}
		if want := boundedDBText(a.PublicKey.ID, remoteUserKeyMaxLengthID); keyID != want {
			t.Errorf("stored key id has %d runes, want the %d-rune prefix", utf8.RuneCountInString(keyID), remoteUserKeyMaxLengthID)
		}
	})

	// Handle resolution writes remoteusers from webfinger and a fetched actor
	// directly, not through unmarshalActor, so it needs its own cleaning.
	t.Run("handle resolution stores a cleaned remote actor", func(t *testing.T) {
		stubRemoteLookup(t, func(handle string) string {
			return "https://remote.example/users/" + strings.SplitN(handle, "@", 2)[0] + "\x00"
		})
		stubNewRemoteActor(t, func(_ *App, iri string) (remoteActorInfo, error) {
			return remoteActorInfo{iri: iri, inbox: iri + "/in\x00box", sharedInbox: "https://remote.example/sh\xffared", url: iri + "/pro\x00file"}, nil
		})

		for _, c := range []struct {
			name    string
			resolve func(handle string) (string, error)
			handle  string
		}{
			{"GetProfileURLFromHandle", func(h string) (string, error) { return GetProfileURLFromHandle(app, h) }, "url\x00h@remote.example"},
			{"GetProfilePageFromHandle", func(h string) (string, error) { return app.db.GetProfilePageFromHandle(app, h) }, "page\x00h@remote.example"},
		} {
			got, err := c.resolve(c.handle)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if strings.ContainsRune(got, 0) {
				t.Errorf("%s returned %q, still carrying a NUL byte", c.name, got)
			}
			handle := strings.ReplaceAll(c.handle, "\x00", "")
			wantIRI := "https://remote.example/users/" + strings.SplitN(handle, "@", 2)[0]
			var actorID, inbox, shared string
			if err := app.db.QueryRow("SELECT actor_id, inbox, shared_inbox FROM remoteusers WHERE handle = ?", handle).Scan(&actorID, &inbox, &shared); err != nil {
				t.Fatalf("%s: read remoteusers row for %q: %v", c.name, handle, err)
			}
			if actorID != wantIRI || inbox != wantIRI+"/inbox" || shared != "https://remote.example/sh�ared" {
				t.Errorf("%s stored actor_id=%q inbox=%q shared_inbox=%q", c.name, actorID, inbox, shared)
			}
		}
	})

	t.Run("OAuth remote user ID that does not fit is refused", func(t *testing.T) {
		ctx := context.Background()
		fits := strings.Repeat("r", oauthRemoteUserIDMaxLength-1) + "€"
		if err := app.db.RecordRemoteUserID(ctx, u.ID, fits, "generic", "client", "tok"); err != nil {
			t.Fatalf("RecordRemoteUserID with a %d-character ID: %v", oauthRemoteUserIDMaxLength, err)
		}
		if id, err := app.db.GetIDForRemoteUser(ctx, fits, "generic", "client"); err != nil || id != u.ID {
			t.Errorf("GetIDForRemoteUser = %d, %v; want %d", id, err, u.ID)
		}
		for name, id := range map[string]string{
			"too long":     strings.Repeat("r", oauthRemoteUserIDMaxLength+1),
			"NUL byte":     "r\x00r",
			"invalid UTF8": "r\xffr",
		} {
			err := app.db.RecordRemoteUserID(ctx, u.ID, id, "other", "client", "tok")
			if err != errOAuthRemoteUserIDUnstorable {
				t.Errorf("%s: RecordRemoteUserID returned %v, want errOAuthRemoteUserIDUnstorable", name, err)
			}
		}
		var n int
		if err := app.db.QueryRow("SELECT COUNT(*) FROM oauth_users WHERE provider = 'other'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d unstorable OAuth IDs were stored, want 0", n)
		}
	})
}

// createPostOrSkip calls CreatePost, skipping the test if this engine's
// CreatePost is not ported yet (on Postgres it panics until WFPG-06 lands).
func createPostOrSkip(t *testing.T, app *App, userID, collID int64, p *SubmittedPost) (post *Post) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			if msg := fmt.Sprint(r); strings.Contains(msg, "not implemented for database driver") {
				t.Skipf("CreatePost is not ported to this engine yet (WFPG-06): %s", msg)
			}
			panic(r)
		}
	}()
	post, err := app.db.CreatePost(userID, collID, p)
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	return post
}

var testPostSeq int

// insertTestPost writes a minimal post directly, so the update path can be
// tested on an engine whose CreatePost is not ported yet.
func insertTestPost(t *testing.T, app *App, userID, collID int64) string {
	t.Helper()
	testPostSeq++
	id := fmt.Sprintf("bounds%010d", testPostSeq)
	_, err := app.db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES (?, ?, 0, ?, ?, "+app.db.now()+", "+app.db.now()+", 0, '', 'x')", id, id, userID, collID)
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}
	return id
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) < n {
		return s
	}
	return string(r[len(r)-n:])
}

// TestOAuthCallbackRefusesUnstorableRemoteUserID checks the input boundary:
// an account ID oauth_users.remote_user_id cannot hold exactly is a 400 from
// the callback, before it is looked up, attached or carried into signup.
func TestOAuthCallbackRefusesUnstorableRemoteUserID(t *testing.T) {
	looked := false
	app := &MockOAuthDatastoreProvider{
		DoDB: func() OAuthDatastore {
			return &MockOAuthDatastore{
				DoGetIDForRemoteUser: func(ctx context.Context, remoteUserID, provider, clientID string) (int64, error) {
					looked = true
					return 1, nil
				},
			}
		},
	}
	longID := strings.Repeat("9", oauthRemoteUserIDMaxLength+1)
	h := oauthHandler{
		Config:   app.Config(),
		DB:       app.DB(),
		Store:    app.SessionStore(),
		EmailKey: []byte{0xd, 0xe, 0xc, 0xa, 0xf, 0xf, 0xb, 0xa, 0xd},
		oauthClient: writeAsOauthClient{
			ClientID:         app.Config().WriteAsOauth.ClientID,
			ClientSecret:     app.Config().WriteAsOauth.ClientSecret,
			ExchangeLocation: app.Config().WriteAsOauth.TokenLocation,
			InspectLocation:  app.Config().WriteAsOauth.InspectLocation,
			AuthLocation:     app.Config().WriteAsOauth.AuthLocation,
			CallbackLocation: "http://localhost/oauth/callback",
			HttpClient: &MockHTTPClient{
				DoDo: func(req *http.Request) (*http.Response, error) {
					switch req.URL.String() {
					case "https://write.as/oauth/token":
						return &http.Response{StatusCode: 200, Body: &StringReadCloser{strings.NewReader(`{"access_token": "access_token", "expires_in": 1000, "refresh_token": "refresh_token", "token_type": "access"}`)}}, nil
					case "https://write.as/oauth/inspect":
						return &http.Response{StatusCode: 200, Body: &StringReadCloser{strings.NewReader(`{"client_id": "development", "user_id": "` + longID + `", "expires_at": "2019-12-19T11:42:01Z", "username": "nick", "email": "nick@testing.write.as"}`)}}, nil
					}
					return &http.Response{StatusCode: http.StatusNotFound}, nil
				},
			},
		},
	}
	req := httptest.NewRequest("GET", "/oauth/callback", nil)
	err := h.viewOauthCallback(&App{cfg: app.Config(), sessionStore: app.SessionStore()}, httptest.NewRecorder(), req)
	he, ok := err.(impart.HTTPError)
	if !ok || he.Status != http.StatusBadRequest {
		t.Fatalf("viewOauthCallback returned %v, want a 400", err)
	}
	if looked {
		t.Error("the unstorable ID was looked up anyway")
	}
}
