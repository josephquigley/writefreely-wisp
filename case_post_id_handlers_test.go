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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writeas/impart"
)

// Post IDs are only ever generated lower-case, so the handlers that take one
// from the URL lower-case it at the boundary (see normalizePostID). These
// cover the three that did not: the split-content API, the /a/{post}
// redirect and the pin-move action. Each runs the handler directly, so it
// answers on every engine the harness has.

func TestSplitContentPostIDCase(t *testing.T) {
	receipts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receipts.Close()
	t.Setenv("RECEIPTS_HOST", receipts.URL)

	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "splitter")
		coll := caseInsertCollection(t, app, "splitter", owner)
		caseInsertPost(t, app, "splitpost1", "split-slug", "", "", owner, coll)
		caseInsertPost(t, app, "splitpost2", "", "", "", owner, 0)
		for _, id := range []string{"splitpost1", "splitpost2"} {
			_, err := app.db.Exec("UPDATE posts SET content = ? WHERE id = ?", "Free part"+shortCodePaid+"Paid part", id)
			require.NoError(t, err)
		}

		split := func(vars map[string]string) *httptest.ResponseRecorder {
			r := mux.SetURLVars(httptest.NewRequest("POST", "/api/collections/x/posts/x/splitcontent", strings.NewReader(url.Values{"receipt": {"r"}}.Encode())), vars)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			require.NoError(t, handleGetSplitContent(app, w, r))
			return w
		}
		// In a collection the post is addressed by slug; without one, by ID.
		assert.Contains(t, split(map[string]string{"alias": "splitter", "post": "Split-Slug"}).Body.String(), "Paid part")
		assert.Contains(t, split(map[string]string{"post": "SplitPost2"}).Body.String(), "Paid part")
	})
}

func TestPostIDRedirectCase(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "redirector")
		coll := caseInsertCollection(t, app, "redirector", owner)
		caseInsertPost(t, app, "redirpost1", "the-slug", "", "", owner, coll)

		r := mux.SetURLVars(httptest.NewRequest("GET", "/a/x", nil), map[string]string{"post": "RedirPost1"})
		err := handlePostIDRedirect(app, httptest.NewRecorder(), r)
		require.Error(t, err, "the redirect is returned as an HTTPError")
		assert.Contains(t, err.Error(), "the-slug")
	})
}

func TestPinnedActionPostIDCase(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		owner := caseInsertUser(t, app, "pinner")
		coll := caseInsertCollection(t, app, "pinner", owner)
		caseInsertPost(t, app, "pinpost001", "", "", "", owner, coll)
		caseInsertPost(t, app, "pinpost002", "", "", "", owner, coll)
		_, err := app.db.Exec("UPDATE posts SET pinned_position = 1 WHERE id = 'pinpost001'")
		require.NoError(t, err)
		_, err = app.db.Exec("UPDATE posts SET pinned_position = 2 WHERE id = 'pinpost002'")
		require.NoError(t, err)

		r := mux.SetURLVars(httptest.NewRequest("POST", "/me/c/pinner/pinned/x/down", nil),
			map[string]string{"collection": "pinner", "post": "PinPost001", "action": "down"})
		w := httptest.NewRecorder()
		// A successful action answers with a redirect back to the page.
		err = handlePinnedPostAction(app, &User{ID: owner}, w, r)
		var he impart.HTTPError
		require.ErrorAs(t, err, &he)
		assert.Equal(t, http.StatusFound, he.Status)

		var first, second int64
		require.NoError(t, app.db.QueryRow("SELECT pinned_position FROM posts WHERE id = 'pinpost001'").Scan(&first))
		require.NoError(t, app.db.QueryRow("SELECT pinned_position FROM posts WHERE id = 'pinpost002'").Scan(&second))
		assert.EqualValues(t, 2, first)
		assert.EqualValues(t, 1, second)
	})
}
