//go:build sqlite

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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

// serveHealthz sends one request to /healthz through the full router.
func serveHealthz(t *testing.T, app *App, method string) *http.Response {
	t.Helper()
	router := InitRoutes(app, mux.NewRouter())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(method, healthzPath, nil))
	return rec.Result()
}

// assertBare checks that a /healthz answer says nothing beyond its status.
func assertBare(t *testing.T, res *http.Response) {
	t.Helper()
	assert.Empty(t, res.Cookies(), "no cookie, the wfu session cookie included")
	assert.Empty(t, res.Header.Get("Location"), "no redirect")
	body := httptest.NewRecorder().Body
	body.ReadFrom(res.Body)
	assert.Empty(t, body.String(), "empty body")
}

func TestHealthzHealthy(t *testing.T) {
	for _, singleUser := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			app := newSignupTestApp(t)
			app.cfg.App.SingleUser = singleUser
			res := serveHealthz(t, app, method)
			assert.Equal(t, http.StatusOK, res.StatusCode, "%s, single user %v", method, singleUser)
			assertBare(t, res)
		}
	}
}

// A private instance redirects anonymous visitors to the login page
// everywhere else; the health route must answer regardless.
func TestHealthzPrivateInstance(t *testing.T) {
	app := newSignupTestApp(t)
	app.cfg.App.Private = true
	res := serveHealthz(t, app, http.MethodGet)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assertBare(t, res)
}

func TestHealthzDatabaseDown(t *testing.T) {
	app := newSignupTestApp(t)
	if err := app.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		res := serveHealthz(t, app, method)
		assert.Equal(t, http.StatusServiceUnavailable, res.StatusCode, method)
		assertBare(t, res)
	}
}

// Any other method is refused here rather than falling through to the
// post or collection routes registered after it.
func TestHealthzOtherMethods(t *testing.T) {
	app := newSignupTestApp(t)
	res := serveHealthz(t, app, http.MethodPost)
	assert.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)
	assert.Equal(t, "GET, HEAD", res.Header.Get("Allow"))
	assertBare(t, res)
}
