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
	"testing"
	"time"

	"github.com/writeas/impart"
	"github.com/writeas/web-core/memo"
	"github.com/writefreely/writefreely/config"
)

func TestViewLocalTimelineAPIHonoursSetting(t *testing.T) {
	app := &App{cfg: &config.Config{}}
	// The timeline is always built now, so the handler must check the
	// setting itself. A stub fetch keeps this off the database.
	app.timeline = &localTimeline{
		postsPerPage: tlPostsPerPage,
		m: memo.New(func() (interface{}, error) {
			return []PublicPost{}, nil
		}, time.Minute),
	}

	req := httptest.NewRequest("GET", "/read/api/posts", nil)

	app.cfg.App.LocalTimeline = false
	err := viewLocalTimelineAPI(app, httptest.NewRecorder(), req)
	he, ok := err.(impart.HTTPError)
	if !ok || he.Status != http.StatusNotFound {
		t.Fatalf("disabled: want 404 HTTPError, got %v", err)
	}

	app.cfg.App.LocalTimeline = true
	w := httptest.NewRecorder()
	if err := viewLocalTimelineAPI(app, w, req); err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("enabled: want 200, got %d", w.Code)
	}
}
