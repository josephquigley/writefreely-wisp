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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/writefreely/writefreely/config"
)

const (
	piaActivityJSON  = "application/activity+json"
	piaInstanceActor = "/api/collections/local.example"
	piaKeyID         = "https://local.example/api/collections/local.example#main-key"
)

// newPrivateInstanceActorApp runs the whole router against a real database,
// so a request private mode passes reaches its handler.
func newPrivateInstanceActorApp(t *testing.T, private bool, allowlist string) (*mux.Router, *Collection, *Post) {
	t.Helper()
	app, router := newTemplateTestApp(t, func(cfg *config.Config) {
		cfg.App.Host = "https://local.example"
		cfg.App.SingleUser = false
		cfg.App.Private = private
		cfg.App.FederationAllowlist = allowlist
	})
	require.NoError(t, app.initFederationAllowlist())
	_, coll, post := createTemplateTestUser(t, app, "alice")
	return router, coll, post
}

func piaServe(router *mux.Router, method, path, accept string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://local.example"+path, nil)
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

// assertInstanceActorDocument checks that w is the instance actor, and that
// it serves nothing beyond the server's name, its public key and the
// standard actor endpoints, all of which derive from the configured host.
// A new field here is readable by anyone on a private instance, so it
// should fail this test until someone has decided it may be.
func assertInstanceActorDocument(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))

	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{
		"@context", "endpoints", "followers", "following", "icon", "id",
		"inbox", "name", "outbox", "preferredUsername", "publicKey",
		"summary", "type", "url",
	}, keys)

	assert.Equal(t, "Application", doc["type"])
	assert.Equal(t, "https://local.example/api/collections/local.example", doc["id"])
	assert.Equal(t, "local.example", doc["name"])
	assert.Equal(t, "local.example", doc["preferredUsername"])
	assert.Equal(t, "", doc["summary"])

	pk, ok := doc["publicKey"].(map[string]interface{})
	require.True(t, ok, "publicKey")
	assert.Equal(t, piaKeyID, pk["id"])
	assert.Contains(t, pk["publicKeyPem"], "PUBLIC KEY")
}

func TestPrivateModeServesInstanceActorUnsigned(t *testing.T) {
	for _, allowlist := range []string{"", "peer.example"} {
		t.Run(fmt.Sprintf("allowlist=%q", allowlist), func(t *testing.T) {
			router, _, _ := newPrivateInstanceActorApp(t, true, allowlist)

			assertInstanceActorDocument(t, piaServe(router, "GET", piaInstanceActor, piaActivityJSON))
			assertInstanceActorDocument(t, piaServe(router, "GET", piaInstanceActor,
				`application/ld+json; profile="https://www.w3.org/ns/activitystreams"`))
		})
	}
}

func TestPrivateModeStillRefusesEverythingElseUnsigned(t *testing.T) {
	for _, allowlist := range []string{"", "peer.example"} {
		t.Run(fmt.Sprintf("allowlist=%q", allowlist), func(t *testing.T) {
			router, coll, post := newPrivateInstanceActorApp(t, true, allowlist)

			for _, tc := range []struct {
				path, accept string
			}{
				// The instance actor's path, but not as ActivityPub.
				{piaInstanceActor, ""},
				{piaInstanceActor, "application/json"},
				// The instance actor's other documents.
				{piaInstanceActor + "/outbox", piaActivityJSON},
				{piaInstanceActor + "/followers", piaActivityJSON},
				{piaInstanceActor + "/following", piaActivityJSON},
				// A blog actor and its documents.
				{"/api/collections/" + coll.Alias, piaActivityJSON},
				{"/api/collections/" + coll.Alias, ""},
				{"/api/collections/" + coll.Alias + "/outbox", piaActivityJSON},
				{"/api/collections/" + coll.Alias + "/posts", ""},
				// A post, by slug and by id.
				{"/api/collections/" + coll.Alias + "/posts/" + post.Slug.String, piaActivityJSON},
				{"/api/posts/" + post.ID, piaActivityJSON},
				{"/api/posts/" + post.ID, ""},
			} {
				w := piaServe(router, "GET", tc.path, tc.accept)
				assert.Equal(t, http.StatusUnauthorized, w.Code, "%s (Accept %q)", tc.path, tc.accept)
			}
		})
	}
}

func TestPrivateModeInstanceActorOtherMethodsUnchanged(t *testing.T) {
	// The route serves GET only, so other methods are answered by the
	// router's not-found handler, as they were before the exemption.
	router, _, _ := newPrivateInstanceActorApp(t, true, "peer.example")
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		w := piaServe(router, m, piaInstanceActor, piaActivityJSON)
		assert.Equal(t, http.StatusNotFound, w.Code, m)
	}

	// And private mode itself still refuses them, so the exemption would
	// not reach another method even if a route for one were added.
	app := allowlistApp(t, "peer.example")
	app.sessionStore = sessions.NewCookieStore([]byte("test-session-key"))
	h := &Handler{app: app}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD"} {
		r := httptest.NewRequest(m, "https://local.example"+piaInstanceActor, nil)
		r.Header.Set("Accept", piaActivityJSON)
		assert.Error(t, h.requirePrivateModeAccess(r), m)
	}
	r := httptest.NewRequest("GET", "https://local.example"+piaInstanceActor, nil)
	r.Header.Set("Accept", piaActivityJSON)
	assert.NoError(t, h.requirePrivateModeAccess(r), "GET")
}

func TestPublicModeInstanceActorUnchanged(t *testing.T) {
	router, coll, _ := newPrivateInstanceActorApp(t, false, "")

	assertInstanceActorDocument(t, piaServe(router, "GET", piaInstanceActor, piaActivityJSON))
	assert.Equal(t, http.StatusOK, piaServe(router, "GET", "/api/collections/"+coll.Alias, piaActivityJSON).Code)
	assert.Equal(t, http.StatusOK, piaServe(router, "GET", "/api/collections/"+coll.Alias+"/outbox", piaActivityJSON).Code)
}

func TestIsInstanceActorRead(t *testing.T) {
	cfg := config.New()
	cfg.App.Host = "https://local.example"

	for _, tc := range []struct {
		method, path, accept string
		want                 bool
	}{
		{"GET", piaInstanceActor, piaActivityJSON, true},
		{"GET", piaInstanceActor, `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`, true},
		{"GET", piaInstanceActor, "", false},
		{"GET", piaInstanceActor, "application/json", false},
		{"HEAD", piaInstanceActor, piaActivityJSON, false},
		{"POST", piaInstanceActor, piaActivityJSON, false},
		{"GET", piaInstanceActor + "/", piaActivityJSON, false},
		{"GET", piaInstanceActor + "/outbox", piaActivityJSON, false},
		{"GET", piaInstanceActor + "/posts/x", piaActivityJSON, false},
		{"GET", "/api/collections/alice", piaActivityJSON, false},
		{"GET", "/api/collections/other.example", piaActivityJSON, false},
		{"GET", "/local.example", piaActivityJSON, false},
	} {
		r := httptest.NewRequest(tc.method, "https://local.example"+tc.path, nil)
		if tc.accept != "" {
			r.Header.Set("Accept", tc.accept)
		}
		assert.Equal(t, tc.want, isInstanceActorRead(cfg, r), "%s %s (Accept %q)", tc.method, tc.path, tc.accept)
	}

	// The alias comes from configuration, never from the request's Host.
	r := httptest.NewRequest("GET", "https://other.example/api/collections/other.example", nil)
	r.Header.Set("Accept", piaActivityJSON)
	assert.False(t, isInstanceActorRead(cfg, r))
}
