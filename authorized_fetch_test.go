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
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	"github.com/writefreely/writefreely/config"
	"github.com/writefreely/writefreely/key"
)

const apAccept = "application/activity+json"

// authorizedFetchApp is an App with no database, enough to build the real
// router. Every request these tests send through it must be refused before a
// handler runs, which is the property under test: a handler would need the
// database and fail.
func authorizedFetchApp(t *testing.T, private bool, allowlist string, on bool) *App {
	t.Helper()
	cfg := config.New()
	cfg.App.Host = "https://local.example"
	cfg.App.SingleUser = false
	cfg.App.Private = private
	cfg.App.FederationAllowlist = allowlist
	cfg.App.AuthorizedFetch = on
	app := &App{cfg: cfg, keys: &key.Keychain{CSRFKey: make([]byte, 32)}}
	if err := app.initFederationAllowlist(); err != nil {
		t.Fatalf("initFederationAllowlist: %v", err)
	}
	return app
}

func apRequest(method, target string, body []byte) *http.Request {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Header.Set("Accept", apAccept)
	return r
}

// Every ActivityPub surface, including AP renders of web routes. None of the
// blogs or posts named exist: the refusal must not depend on content.
var authorizedFetchGatedRequests = []struct {
	name   string
	method string
	path   string
	accept string
}{
	{"blog actor", "GET", "/api/collections/nosuchblog", apAccept},
	{"outbox, no Accept", "GET", "/api/collections/nosuchblog/outbox", ""},
	{"followers, no Accept", "GET", "/api/collections/nosuchblog/followers", ""},
	{"following, no Accept", "GET", "/api/collections/nosuchblog/following", ""},
	{"instance actor outbox", "GET", "/api/collections/local.example/outbox", ""},
	{"blog post", "GET", "/api/collections/nosuchblog/posts/nosuchpost", apAccept},
	{"post", "GET", "/api/posts/nosuchpost", apAccept},
	{"web blog page", "GET", "/nosuchblog/", apAccept},
	{"web post page", "GET", "/nosuchblog/nosuchpost", apAccept},
	{"inbox, no Accept", "POST", "/api/collections/nosuchblog/inbox", ""},
	{"instance actor inbox", "POST", "/api/collections/local.example/inbox", ""},
}

func TestAuthorizedFetchRefusesUnsignedRequestsBeforeAnyLookup(t *testing.T) {
	for _, inst := range []struct {
		name      string
		private   bool
		allowlist string
	}{
		{"public", false, ""},
		{"private", true, ""},
		{"private with allowlist", true, "example.org"},
	} {
		app := authorizedFetchApp(t, inst.private, inst.allowlist, true)
		router := InitRoutes(app, mux.NewRouter())
		for _, tc := range authorizedFetchGatedRequests {
			t.Run(inst.name+"/"+tc.name, func(t *testing.T) {
				r := httptest.NewRequest(tc.method, "https://local.example"+tc.path, bytes.NewReader([]byte(`{}`)))
				if tc.accept != "" {
					r.Header.Set("Accept", tc.accept)
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				assert.Equal(t, http.StatusUnauthorized, w.Code)
				assert.Contains(t, w.Body.String(), "Unauthorized.")
			})
		}
	}
}

func TestAuthorizedFetchRefusesSignatureFromHostOffTheAllowlist(t *testing.T) {
	app := authorizedFetchApp(t, true, "example.org", true)
	router := InitRoutes(app, mux.NewRouter())
	k := testKey(t)
	keyID := "https://evil.example/users/a#main-key"
	// A cached key would let the signature verify; the allowlist must still
	// refuse it.
	app.fedKeys.set(keyID, &k.PublicKey, time.Minute)

	for _, target := range []string{
		"https://local.example/api/collections/nosuchblog/outbox",
		"https://local.example/api/collections/nosuchblog",
	} {
		r := signedRequest(t, k, keyID, "GET", target, nil)
		r.Header.Set("Accept", apAccept)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		assert.Equal(t, http.StatusUnauthorized, w.Code, target)
	}

	body := []byte(inboxActivity)
	r := signedRequest(t, k, keyID, "POST", "https://local.example/api/collections/nosuchblog/inbox", body)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "inbox")
}

func TestRequireAuthorizedFetchIsOffByDefault(t *testing.T) {
	assert.False(t, config.New().App.AuthorizedFetch)
	app := authorizedFetchApp(t, false, "", false)
	assert.NoError(t, app.requireAuthorizedFetch(apRequest("GET", "https://local.example/api/collections/x", nil)))
}

func TestRequireAuthorizedFetchAdmitsAnyValidSignerWithNoAllowlist(t *testing.T) {
	app := authorizedFetchApp(t, false, "", true)
	k := testKey(t)
	keyID := "https://anywhere.example/users/a#main-key"
	app.fedKeys.set(keyID, &k.PublicKey, time.Minute)

	r := signedRequest(t, k, keyID, "GET", "https://local.example/api/collections/x", nil)
	r.Header.Set("Accept", apAccept)
	assert.NoError(t, app.requireAuthorizedFetch(r))

	// The same request, altered after signing, is refused.
	r = signedRequest(t, k, keyID, "GET", "https://local.example/api/collections/x", nil)
	r.Header.Set("Accept", apAccept)
	r.URL.Path = "/api/collections/y"
	assert.Equal(t, ErrFederationNotAllowed, app.requireAuthorizedFetch(r))
}

func TestRequireAuthorizedFetchAdmitsAllowlistedSigner(t *testing.T) {
	app := authorizedFetchApp(t, true, "example.org", true)
	k := testKey(t)
	keyID := "https://example.org/users/a#main-key"
	app.fedKeys.set(keyID, &k.PublicKey, time.Minute)

	r := signedRequest(t, k, keyID, "GET", "https://local.example/api/collections/x", nil)
	r.Header.Set("Accept", apAccept)
	assert.NoError(t, app.requireAuthorizedFetch(r))
}

func TestRequireAuthorizedFetchLeavesWhatPeersNeedFirstOpen(t *testing.T) {
	app := authorizedFetchApp(t, false, "", true)
	for _, path := range []string{
		"/.well-known/webfinger?resource=acct:alice@local.example",
		"/.well-known/host-meta",
		"/.well-known/nodeinfo",
		"/api/nodeinfo",
		"/api/collections/local.example",
	} {
		assert.NoError(t, app.requireAuthorizedFetch(apRequest("GET", "https://local.example"+path, nil)), path)
	}
}

func TestRequireAuthorizedFetchLeavesNonActivityPubRequestsAlone(t *testing.T) {
	// API tokens and web sessions are judged by the handlers, as before:
	// only ActivityPub requests need a signature.
	app := authorizedFetchApp(t, false, "", true)
	for _, accept := range []string{"", "application/json", "text/html"} {
		r := httptest.NewRequest("GET", "https://local.example/api/collections/x", nil)
		r.Header.Set("Accept", accept)
		r.Header.Set("Authorization", "some-token")
		assert.NoError(t, app.requireAuthorizedFetch(r), accept)
	}
}

func TestAuthorizedFetchIsIndependentOfPrivateMode(t *testing.T) {
	// Unlike federation_allowlist, the setting is accepted on a public
	// instance.
	cfg := config.New()
	cfg.App.Private = false
	cfg.App.AuthorizedFetch = true
	_, err := buildFederationAllowlist(cfg)
	assert.NoError(t, err)
}
