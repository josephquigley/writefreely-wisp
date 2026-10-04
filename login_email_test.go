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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/writeas/web-core/auth"
)

// apiLoginEmail logs in over the JSON API and returns the raw "user" object
// of the response.
func apiLoginEmail(t *testing.T, app *App, body string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "login-email-test")
	w := httptest.NewRecorder()
	if err := login(app, w, req); err != nil {
		t.Fatalf("login: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var res struct {
		Data struct {
			User map[string]interface{} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body.String())
	}
	return res.Data.User
}

func TestAPILoginReturnsPlainEmail(t *testing.T) {
	app := newSignupTestApp(t)
	app.cfg.Server.Dev = true // skip the per-account login throttle

	hash, err := auth.HashPass([]byte("sup3rSecret!"))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name, email string) *User {
		u := &User{
			Username:   name,
			HashedPass: hash,
			HasPass:    true,
			Email:      prepareUserEmail(email, app.keys.EmailKey),
			Created:    time.Now().Truncate(time.Second).UTC(),
		}
		if err := app.db.CreateUser(app.Config(), u, name, ""); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return u
	}
	withEmail := mk("with-email", "me@example.com")
	mk("no-email", "")

	if enc := prepareUserEmail("me@example.com", app.keys.EmailKey).String; enc == "me@example.com" {
		t.Fatal("test setup: email was not encrypted")
	}

	for _, verbose := range []string{"", "?all=true"} {
		t.Run("with email"+verbose, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/auth/login"+verbose, strings.NewReader(`{"alias":"with-email","pass":"sup3rSecret!"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "login-email-test")
			w := httptest.NewRecorder()
			if err := login(app, w, req); err != nil {
				t.Fatalf("login: %v", err)
			}
			if !strings.Contains(w.Body.String(), `"email":"me@example.com"`) {
				t.Errorf("response lacks plaintext email: %s", w.Body.String())
			}
			var stored string
			if err := app.db.QueryRow("SELECT email FROM users WHERE id = ?", withEmail.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored == "" || strings.Contains(w.Body.String(), stored) {
				t.Errorf("response leaks the stored ciphertext")
			}
		})
	}

	t.Run("one-time token", func(t *testing.T) {
		tok, err := app.db.GetAccessToken(withEmail.ID)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/auth/login?with="+tok, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "login-email-test")
		w := httptest.NewRecorder()
		if err := login(app, w, req); err != nil {
			t.Fatalf("login: %v", err)
		}
		if !strings.Contains(w.Body.String(), `"email":"me@example.com"`) {
			t.Errorf("response lacks plaintext email: %s", w.Body.String())
		}
	})

	t.Run("no email", func(t *testing.T) {
		user := apiLoginEmail(t, app, `{"alias":"no-email","pass":"sup3rSecret!"}`)
		if e, ok := user["email"]; ok && e != nil && e != "" {
			t.Errorf("email = %v, want null or empty", e)
		}
	})
}

func TestUserForAPIEmail(t *testing.T) {
	app := newSignupTestApp(t)
	enc := prepareUserEmail("a@example.com", app.keys.EmailKey)

	u := &User{Username: "x", Email: enc}
	got := u.forAPI(app.keys)
	if got.Email.String != "a@example.com" || !got.Email.Valid {
		t.Errorf("email = %+v, want plaintext", got.Email)
	}
	if u.Email != enc {
		t.Error("forAPI modified the receiver")
	}

	// Undecryptable data must be dropped, never echoed.
	bad := &User{Username: "y", Email: prepareUserEmail("zzz", []byte("0123456789abcdef0123456789abcdef"))}
	if got := bad.forAPI(app.keys); got.Email.Valid || got.Email.String != "" {
		t.Errorf("undecryptable email = %+v, want null", got.Email)
	}

	none := &User{Username: "z"}
	if got := none.forAPI(app.keys); got.Email.Valid || got.Email.String != "" {
		t.Errorf("no email = %+v, want null", got.Email)
	}
}
