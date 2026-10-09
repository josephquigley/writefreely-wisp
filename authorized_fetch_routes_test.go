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
	"github.com/stretchr/testify/require"

	"github.com/writefreely/writefreely/config"
)

// These run the whole router against a real database, so a request that the
// gate passes reaches its handler and has to come back as it always did.

const afPeerKeyID = "https://peer.example/users/a#main-key"

type afInstance struct {
	name      string
	private   bool
	allowlist string
}

func newAuthorizedFetchRouteApp(t *testing.T, inst afInstance, on bool) (*App, *mux.Router, *User) {
	t.Helper()
	app, router := newTemplateTestApp(t, func(cfg *config.Config) {
		cfg.App.Host = "https://local.example"
		cfg.App.SingleUser = false
		cfg.App.Private = inst.private
		cfg.App.FederationAllowlist = inst.allowlist
		cfg.App.AuthorizedFetch = on
	})
	require.NoError(t, app.initFederationAllowlist())
	u, _, _ := createTemplateTestUser(t, app, "alice")
	return app, router, u
}

func afServe(router *mux.Router, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func afUnsigned(method, path, accept string, body []byte) *http.Request {
	r := httptest.NewRequest(method, "https://local.example"+path, bytes.NewReader(body))
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	return r
}

func TestAuthorizedFetchOffKeepsPublicInstanceOpen(t *testing.T) {
	_, router, _ := newAuthorizedFetchRouteApp(t, afInstance{name: "public"}, false)

	assert.Equal(t, http.StatusOK, afServe(router, afUnsigned("GET", "/api/collections/alice", apAccept, nil)).Code)
	assert.Equal(t, http.StatusOK, afServe(router, afUnsigned("GET", "/api/collections/alice/outbox", "", nil)).Code)
	assert.Equal(t, http.StatusOK, afServe(router, afUnsigned("GET", "/alice/", apAccept, nil)).Code)
	assert.Equal(t, http.StatusNotFound, afServe(router, afUnsigned("GET", "/api/collections/nosuchblog/outbox", apAccept, nil)).Code)
	assert.Equal(t, http.StatusOK, afServe(router, afUnsigned("POST", "/api/collections/alice/inbox", "", []byte(inboxActivity))).Code)
}

func TestAuthorizedFetchOffKeepsUnsignedInboxOnPrivateInstanceWithNoAllowlist(t *testing.T) {
	// Upstream behaviour, kept while the setting is off.
	_, router, _ := newAuthorizedFetchRouteApp(t, afInstance{name: "private", private: true}, false)
	assert.Equal(t, http.StatusOK, afServe(router, afUnsigned("POST", "/api/collections/alice/inbox", "", []byte(inboxActivity))).Code)
}

func TestAuthorizedFetchOnEndToEnd(t *testing.T) {
	for _, inst := range []afInstance{
		{name: "public"},
		{name: "private with allowlist", private: true, allowlist: "peer.example"},
	} {
		t.Run(inst.name, func(t *testing.T) {
			app, router, u := newAuthorizedFetchRouteApp(t, inst, true)
			k := testKey(t)
			app.fedKeys.set(afPeerKeyID, &k.PublicKey, time.Minute)
			signed := func(method, path string, body []byte) *http.Request {
				r := signedRequest(t, k, afPeerKeyID, method, "https://local.example"+path, body)
				r.Header.Set("Accept", apAccept)
				return r
			}

			// Unsigned ActivityPub is refused, whether or not the blog exists.
			for _, p := range []string{"/api/collections/alice", "/api/collections/alice/outbox", "/api/collections/nosuchblog/outbox", "/alice/"} {
				assert.Equal(t, http.StatusUnauthorized, afServe(router, afUnsigned("GET", p, apAccept, nil)).Code, p)
			}
			assert.Equal(t, http.StatusUnauthorized, afServe(router, afUnsigned("POST", "/api/collections/alice/inbox", "", []byte(inboxActivity))).Code, "inbox")

			// A signed request reaches its handler and is answered normally.
			assert.Equal(t, http.StatusOK, afServe(router, signed("GET", "/api/collections/alice", nil)).Code)
			assert.Equal(t, http.StatusOK, afServe(router, signed("GET", "/api/collections/alice/outbox", nil)).Code)
			assert.Equal(t, http.StatusNotFound, afServe(router, signed("GET", "/api/collections/nosuchblog/outbox", nil)).Code)
			assert.Equal(t, http.StatusOK, afServe(router, signed("POST", "/api/collections/alice/inbox", []byte(inboxActivity))).Code)

			// A web session still reads the HTML blog, which is not ActivityPub.
			r := afUnsigned("GET", "/alice/", "text/html", nil)
			r.AddCookie(loginCookie(t, app, u))
			assert.Equal(t, http.StatusOK, afServe(router, r).Code)
		})
	}
}

func TestAuthorizedFetchOnRefusesUnsignedInboxOnPrivateInstanceWithNoAllowlist(t *testing.T) {
	_, router, _ := newAuthorizedFetchRouteApp(t, afInstance{name: "private", private: true}, true)
	assert.Equal(t, http.StatusUnauthorized, afServe(router, afUnsigned("POST", "/api/collections/alice/inbox", "", []byte(inboxActivity))).Code)
}

func TestAuthorizedFetchOnLeavesInstanceActorAndDiscoveryOpen(t *testing.T) {
	_, router, _ := newAuthorizedFetchRouteApp(t, afInstance{name: "public"}, true)

	w := afServe(router, afUnsigned("GET", "/api/collections/local.example", apAccept, nil))
	assert.Equal(t, http.StatusOK, w.Code)
	// The key a peer needs to verify our signed fetches.
	assert.Contains(t, w.Body.String(), "https://local.example/api/collections/local.example#main-key")

	w = afServe(router, afUnsigned("GET", "/.well-known/webfinger?resource=acct:alice@local.example", apAccept, nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "https://local.example/api/collections/alice")

	assert.Equal(t, http.StatusOK, afServe(router, afUnsigned("GET", "/.well-known/nodeinfo", "", nil)).Code)
	assert.Equal(t, http.StatusOK, afServe(router, afUnsigned("GET", "/api/nodeinfo", "", nil)).Code)
}

func TestInstanceActorKeyIsWhatOutboundFetchesSignWith(t *testing.T) {
	// The instance actor stays open because it is the keyId on every signed
	// fetch this instance makes. If that ever changes, so can the exemption.
	app, _, _ := newAuthorizedFetchRouteApp(t, afInstance{name: "public"}, true)
	r, err := signedIRIRequest(app.Config().App.Host, "https://peer.example/users/a")
	require.NoError(t, err)
	assert.Contains(t, r.Header.Get("Signature"), `keyId="https://local.example/api/collections/local.example#main-key"`)
}
