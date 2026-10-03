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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/config"
	"github.com/writefreely/writefreely/key"
)

// Case sensitivity (WFPG-09).
//
// MySQL compared most of WriteFreely's string columns case-insensitively, by
// accident of a _ci collation. SQLite and Postgres compare exactly. The
// lookups that relied on the accident now normalise in Go, and these tests
// say which ones: subscriber emails, post IDs and slugs at the API, language
// codes from the URL, and remote handles.
//
// The rest stay exact on purpose, and the second half of this file pins that
// down: tokens, codes and IRIs are case-sensitive by construction or by
// spec, and a test that a differently cased value does NOT match turns that
// decision into a guard.
//
// Every test runs on SQLite, on Postgres too under `make test-postgres`, and
// on MySQL too under `make test-mysql`.

// forEachCaseEngine runs fn against a freshly initialised SQLite app and,
// on a Postgres or MySQL run, a freshly initialised app on that engine.
func forEachCaseEngine(t *testing.T, fn func(t *testing.T, app *App)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, newHandleTestAppOn(t, openSQLiteAppTestDB)) })
	t.Run("postgres", func(t *testing.T) { fn(t, newPostgresTestApp(t, nil)) })
	t.Run("mysql", func(t *testing.T) { fn(t, newMySQLTestApp(t, nil)) })
}

func caseInsertUser(t *testing.T, app *App, username string) int64 {
	t.Helper()
	id, err := app.db.insertReturningID(app.db, "INSERT INTO users (username, password) VALUES (?, ?)", username, strings.Repeat("x", 60))
	require.NoError(t, err)
	return id
}

func caseInsertCollection(t *testing.T, app *App, alias string, ownerID int64) int64 {
	t.Helper()
	id, err := app.db.insertReturningID(app.db, "INSERT INTO collections (alias, title, description, privacy, owner_id, view_count) VALUES (?, ?, '', 1, ?, 0)", alias, alias, ownerID)
	require.NoError(t, err)
	return id
}

// caseInsertPost inserts a post directly, so the test controls the stored
// case of every value. collID 0 is an anonymous post.
func caseInsertPost(t *testing.T, app *App, id, slug, lang, modifyToken string, ownerID, collID int64) {
	t.Helper()
	nullable := func(s string) interface{} {
		if s == "" {
			return nil
		}
		return s
	}
	var coll interface{}
	if collID > 0 {
		coll = collID
	}
	_, err := app.db.Exec("INSERT INTO posts (id, slug, modify_token, language, privacy, owner_id, collection_id, view_count, title, content) VALUES (?, ?, ?, ?, 0, ?, ?, 0, ?, ?)",
		id, nullable(slug), nullable(modifyToken), nullable(lang), ownerID, coll, "Title "+id, "Content of "+id)
	require.NoError(t, err)
}

// swapCase flips the case of every letter, so the result differs from s in
// case only.
func swapCase(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		}
		return r
	}, s)
}

func countSubscribers(t *testing.T, app *App, collID int64) int {
	t.Helper()
	var n int
	require.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM emailsubscribers WHERE collection_id = ?", collID).Scan(&n))
	return n
}

// --- Normalised: lower-cased on write and on lookup ---

func TestEmailSubscriberCaseInsensitive(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "newsletter")
		coll := caseInsertCollection(t, app, "newsletter", owner)

		first, err := app.db.AddEmailSubscription(coll, 0, "  Foo@Example.com ", false)
		require.NoError(t, err)
		assert.Equal(t, "foo@example.com", first.Email.String, "stored lower-cased and trimmed")

		var stored string
		require.NoError(t, app.db.QueryRow("SELECT email FROM emailsubscribers WHERE id = ?", first.ID).Scan(&stored))
		assert.Equal(t, "foo@example.com", stored)

		assert.True(t, app.db.IsEmailSubscriber("FOO@example.COM", 0, coll))
		found, err := app.db.FetchEmailSubscriber("foo@EXAMPLE.com", 0, coll)
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, first.ID, found.ID)

		// Subscribing again in another case is the same subscriber. The
		// duplicate-key path needs the sqlite build tag on SQLite, because
		// isDuplicateKeyErr only recognises SQLite errors there.
		if app.db.driverName != driverSQLite || SQLiteEnabled {
			again, err := app.db.AddEmailSubscription(coll, 0, "foo@example.com", false)
			require.NoError(t, err)
			require.NotNil(t, again)
			assert.Equal(t, first.ID, again.ID)
		}
		assert.Equal(t, 1, countSubscribers(t, app, coll), "one row, whatever case the address arrived in")

		// Unsubscribing in yet another case removes it.
		require.NoError(t, app.db.DeleteEmailSubscriberByUser("FOO@Example.Com", 0, coll))
		assert.Equal(t, 0, countSubscribers(t, app, coll))
	})
}

// A row written before normalisation existed (MySQL kept whatever case the
// reader typed) is still found by a lower-cased lookup.
func TestEmailSubscriberLegacyMixedCaseRow(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "legacy")
		coll := caseInsertCollection(t, app, "legacy", owner)
		_, err := app.db.Exec("INSERT INTO emailsubscribers (id, collection_id, email, subscribed, token, confirmed, allow_export) VALUES ('Legacy01', ?, 'Old@Example.com', CURRENT_TIMESTAMP, 'LegacyToken00001', FALSE, FALSE)", coll)
		require.NoError(t, err)

		found, err := app.db.FetchEmailSubscriber("old@example.com", 0, coll)
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, "Legacy01", found.ID)
		require.NoError(t, app.db.DeleteEmailSubscriberByUser("OLD@EXAMPLE.COM", 0, coll))
		assert.Equal(t, 0, countSubscribers(t, app, coll))
	})
}

// On Postgres, V19 makes the database refuse a second row for the same
// address in another case, even from a writer that skips the Go
// normalisation.
func TestPostgresEmailSubscriberUniqueIgnoresCase(t *testing.T) {
	app := newPostgresTestApp(t, nil)
	owner := caseInsertUser(t, app, "unique")
	coll := caseInsertCollection(t, app, "unique", owner)
	insert := "INSERT INTO emailsubscribers (id, collection_id, email, subscribed, token, confirmed, allow_export) VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?, FALSE, FALSE)"

	_, err := app.db.Exec(insert, "Unique01", coll, "reader@example.com", "UniqueToken00001")
	require.NoError(t, err)
	_, err = app.db.Exec(insert, "Unique02", coll, "Reader@Example.com", "UniqueToken00002")
	require.Error(t, err)
	assert.True(t, app.db.isDuplicateKeyErr(err), "got %v", err)

	var indexDef string
	require.NoError(t, app.db.QueryRow("SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'eu_coll_lower_email'").Scan(&indexDef))
	assert.Contains(t, indexDef, "lower(")
}

func TestPostIDCaseAtAPI(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "author")
		coll := caseInsertCollection(t, app, "author", owner)
		caseInsertPost(t, app, "abcdefghij", "", "", "", owner, 0)
		caseInsertPost(t, app, "klmnopqrst", "my-first-post", "en", "", owner, coll)

		fetch := func(vars map[string]string) (*httptest.ResponseRecorder, error) {
			r := mux.SetURLVars(httptest.NewRequest("GET", "/api/posts/x", nil), vars)
			w := httptest.NewRecorder()
			return w, fetchPost(app, w, r)
		}
		postID := func(w *httptest.ResponseRecorder) string {
			var body struct {
				Data struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
			return body.Data.ID
		}

		w, err := fetch(map[string]string{"post": "ABCDEFGHIJ"})
		require.NoError(t, err)
		assert.Equal(t, "abcdefghij", postID(w))

		w, err = fetch(map[string]string{"alias": "author", "post": "My-First-Post"})
		require.NoError(t, err)
		assert.Equal(t, "klmnopqrst", postID(w))
	})
}

func TestLanguageCodeCase(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "polyglot")
		collID := caseInsertCollection(t, app, "polyglot", owner)
		caseInsertPost(t, app, "lang000001", "english-one", "en", "", owner, collID)
		// A client may have sent the code upper-cased; MySQL matched it.
		caseInsertPost(t, app, "lang000002", "english-two", "EN", "", owner, collID)
		caseInsertPost(t, app, "lang000003", "en-francais", "fr", "", owner, collID)

		n, err := app.db.GetCollLangTotalPosts(collID, "EN")
		require.NoError(t, err)
		assert.EqualValues(t, 2, n)

		c, err := app.db.GetCollectionByID(collID)
		require.NoError(t, err)
		posts, err := app.db.GetLangPosts(app.cfg, c, "EN", 1, true)
		require.NoError(t, err)
		ids := []string{}
		for _, p := range *posts {
			ids = append(ids, p.ID)
		}
		assert.ElementsMatch(t, []string{"lang000001", "lang000002"}, ids)
	})
}

// The /lang: route accepts an upper-case code, so /lang:EN reaches the
// handler instead of 404ing at the router.
func TestLanguageRouteAcceptsUpperCase(t *testing.T) {
	router := mux.NewRouter()
	RouteCollections(&Handler{app: &App{cfg: config.New(), keys: &key.Keychain{CSRFKey: make([]byte, 32)}}}, router)
	for _, path := range []string{"/lang:en", "/lang:EN", "/lang:En/page/2"} {
		var match mux.RouteMatch
		require.True(t, router.Match(httptest.NewRequest("GET", path, nil), &match), path)
		assert.Equal(t, "en", normalizeLangCode(match.Vars["lang"]), path)
	}
}

func TestRemoteHandleCase(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		stubRemoteLookup(t, func(h string) string {
			t.Errorf("webfinger called for %q; the handle is cached", h)
			return ""
		})
		_, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, handle) VALUES (?, ?, ?, ?)",
			"https://social.example/users/alice", "https://social.example/users/alice/inbox", "https://social.example/inbox", "alice@social.example")
		require.NoError(t, err)
		// Cached before normalisation, in the case the owner typed.
		_, err = app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, handle) VALUES (?, ?, ?, ?)",
			"https://social.example/users/Bob", "https://social.example/users/Bob/inbox", "https://social.example/inbox", "Bob@Social.Example")
		require.NoError(t, err)

		iri, err := app.db.GetProfilePageFromHandle(app, "@Alice@Social.Example")
		require.NoError(t, err)
		assert.Equal(t, "https://social.example/users/alice", iri)

		iri, err = app.db.GetProfilePageFromHandle(app, "@bob@social.example")
		require.NoError(t, err)
		assert.Equal(t, "https://social.example/users/Bob", iri)

		ru, err := getRemoteUserFromHandle(app, "ALICE@social.example")
		require.NoError(t, err)
		assert.Equal(t, "https://social.example/users/alice", ru.ActorID)

		// The reply delegate's cached-only path, which used to pass the
		// handle through without lower-casing it.
		_, actor := replyDelegateState(app, &Collection{ID: 1, Alias: "x", ReplyDelegate: "@Alice@Social.Example"}, false)
		assert.Equal(t, "https://social.example/users/alice", actor)
	})
}

// A handle resolved by webfinger is cached lower-cased.
func TestRemoteHandleStoredLowerCase(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		const actor = "https://x.example/users/carol"
		stubRemoteLookup(t, func(h string) string {
			assert.Equal(t, "carol@x.example", h)
			return actor
		})
		_, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox) VALUES (?, ?, ?)", actor, actor+"/inbox", "https://x.example/inbox")
		require.NoError(t, err)

		iri, err := app.db.GetProfilePageFromHandle(app, "@Carol@X.Example")
		require.NoError(t, err)
		assert.Equal(t, actor, iri)

		var handle string
		require.NoError(t, app.db.QueryRow("SELECT handle FROM remoteusers WHERE actor_id = ?", actor).Scan(&handle))
		assert.Equal(t, "carol@x.example", handle)
	})
}

func TestCollectionAliasTrimmed(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "padded")
		caseInsertCollection(t, app, "padded", owner)
		c, err := app.db.GetCollection("padded ")
		require.NoError(t, err)
		assert.Equal(t, "padded", c.Alias)
		c, err = app.db.GetCollectionForPad(" padded")
		require.NoError(t, err)
		assert.Equal(t, "padded", c.Alias)
	})
}

// --- Exact on purpose: each of these must match only in its exact case ---

func TestExactMatchInviteCode(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "inviter")
		// Invite codes draw from a mixed-case alphabet (invites.go), so case
		// carries information.
		_, err := app.db.Exec("INSERT INTO userinvites (id, owner_id, max_uses, created, expires, inactive) VALUES ('BcDfGh', ?, 0, CURRENT_TIMESTAMP, NULL, FALSE)", owner)
		require.NoError(t, err)

		i, err := app.db.GetUserInvite("BcDfGh")
		require.NoError(t, err)
		assert.Equal(t, "BcDfGh", i.ID)
		_, err = app.db.GetUserInvite(swapCase("BcDfGh"))
		assert.Error(t, err)
	})
}

func TestExactMatchPasswordResetToken(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "forgetful")
		const token = "AbCdEfGhIjKlMnOpQrStUvWxYz012345"
		_, err := app.db.Exec("INSERT INTO password_resets (user_id, token, used, created) VALUES (?, ?, FALSE, CURRENT_TIMESTAMP)", owner, token)
		require.NoError(t, err)

		// The comparison GetUserFromPasswordReset and
		// ConsumePasswordResetToken make. Those functions are not called
		// directly because their `used` literals are not yet portable to a
		// Postgres boolean (WFPG-05).
		lookup := func(tok string) error {
			var uid int64
			return app.db.QueryRow("SELECT user_id FROM password_resets WHERE token = ?", tok).Scan(&uid)
		}
		assert.NoError(t, lookup(token))
		assert.Error(t, lookup(swapCase(token)))
	})
}

func TestExactMatchModifyToken(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		const token = "ModifyTokenAbCdEf"
		caseInsertPost(t, app, "anonpost01", "", "", token, 0, 0)

		// The check deletePost and the anonymous-post paths make.
		lookup := func(tok string) error {
			var dummy int
			return app.db.QueryRow("SELECT 1 FROM posts WHERE id = ? AND modify_token = ?", "anonpost01", tok).Scan(&dummy)
		}
		assert.NoError(t, lookup(token))
		assert.Error(t, lookup(swapCase(token)))
	})
}

func TestExactMatchEmailSubscriberToken(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "tokens")
		coll := caseInsertCollection(t, app, "tokens", owner)
		_, err := app.db.Exec("INSERT INTO emailsubscribers (id, collection_id, email, subscribed, token, confirmed, allow_export) VALUES ('SubIdAb1', ?, 'reader@example.com', CURRENT_TIMESTAMP, 'TokenBcDfGhJkLmN', FALSE, FALSE)", coll)
		require.NoError(t, err)

		email, err := app.db.FetchEmailSubscriberEmail("SubIdAb1", "TokenBcDfGhJkLmN")
		require.NoError(t, err)
		assert.Equal(t, "reader@example.com", email)
		_, err = app.db.FetchEmailSubscriberEmail("SubIdAb1", swapCase("TokenBcDfGhJkLmN"))
		assert.Error(t, err)
		_, err = app.db.FetchEmailSubscriberEmail(swapCase("SubIdAb1"), "TokenBcDfGhJkLmN")
		assert.Error(t, err)
		assert.Error(t, app.db.DeleteEmailSubscriber("SubIdAb1", swapCase("TokenBcDfGhJkLmN")))
		assert.NoError(t, app.db.DeleteEmailSubscriber("SubIdAb1", "TokenBcDfGhJkLmN"))
	})
}

func TestExactMatchOAuthState(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		ctx := context.Background()
		const state = "StateAbCdEfGhIjKlMnOpQrSt"
		_, err := app.db.Exec("INSERT INTO oauth_client_states (state, provider, client_id, used, created_at) VALUES (?, 'gitlab', 'client', FALSE, CURRENT_TIMESTAMP)", state)
		require.NoError(t, err)

		// The wrong case is rejected, and does not consume the state.
		_, _, _, _, err = app.db.ValidateOAuthState(ctx, swapCase(state))
		assert.Error(t, err)
		provider, clientID, _, _, err := app.db.ValidateOAuthState(ctx, state)
		require.NoError(t, err)
		assert.Equal(t, "gitlab", provider)
		assert.Equal(t, "client", clientID)
	})
}

func TestExactMatchOAuthRemoteUserID(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		ctx := context.Background()
		uid := caseInsertUser(t, app, "oauthed")
		require.NoError(t, app.db.RecordRemoteUserID(ctx, uid, "RemoteUserAbC", "gitlab", "client", "access"))

		got, err := app.db.GetIDForRemoteUser(ctx, "RemoteUserAbC", "gitlab", "client")
		require.NoError(t, err)
		assert.Equal(t, uid, got)
		got, err = app.db.GetIDForRemoteUser(ctx, swapCase("RemoteUserAbC"), "gitlab", "client")
		require.NoError(t, err)
		assert.EqualValues(t, -1, got, "a provider's user ID is opaque; another case is another user")
	})
}

func TestExactMatchRemoteActorIRIAndURL(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		// IRIs are case-sensitive past the host (RFC 3986), and AP servers
		// do mint paths that differ only in case.
		const actor = "https://social.example/users/MixedCase"
		const profile = "https://social.example/@MixedCase"
		_, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, url) VALUES (?, ?, ?, ?)", actor, actor+"/inbox", "https://social.example/inbox", profile)
		require.NoError(t, err)

		ru, err := getRemoteUser(app, actor)
		require.NoError(t, err)
		assert.Equal(t, profile, ru.URL)
		_, err = getRemoteUser(app, strings.ToLower(actor))
		assert.Error(t, err)

		ru, err = getRemoteUserFromURL(app, profile)
		require.NoError(t, err)
		assert.Equal(t, actor, ru.ActorID)
		_, err = getRemoteUserFromURL(app, strings.ToLower(profile))
		assert.Error(t, err)
	})
}

func TestExactMatchPostImageID(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "photographer")
		insert := "INSERT INTO post_images (id, owner_id, sha256, path, filename, mime, size, created) VALUES (?, ?, ?, ?, 'a.png', 'image/png', 1, CURRENT_TIMESTAMP)"
		// Two IDs differing only in case are two images. On MySQL these
		// collided on the primary key until V20 made the key binary.
		_, err := app.db.Exec(insert, "AbC123", owner, strings.Repeat("a", 64), "u/a.png")
		require.NoError(t, err)
		_, err = app.db.Exec(insert, "abc123", owner, strings.Repeat("b", 64), "u/b.png")
		require.NoError(t, err)

		img, err := app.db.GetPostImage("AbC123")
		require.NoError(t, err)
		assert.Equal(t, "u/a.png", img.Path)
		img, err = app.db.GetPostImage("abc123")
		require.NoError(t, err)
		assert.Equal(t, "u/b.png", img.Path)
		_, err = app.db.GetPostImage("ABC123")
		assert.Error(t, err)
		if herr, ok := err.(interface{ Status() int }); ok {
			assert.Equal(t, http.StatusNotFound, herr.Status())
		}
	})
}
