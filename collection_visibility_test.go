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
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/writefreely/writefreely/config"
)

// TestSingleUserCanChoosePublic covers the Publicity section of a blog's
// settings. Visibility is no longer only about the local reader: since the
// federated addressing change, an unlisted blog addresses its posts to
// followers only, so an instance that can never choose Public can never
// reach a remote public timeline. A single-user instance has no reader, but
// it does federate, so the option has to be offered there.
func TestSingleUserCanChoosePublic(t *testing.T) {
	app, router := newTemplateTestApp(t, func(cfg *config.Config) {
		cfg.App.SingleUser = true
	})
	u, coll, _ := createTemplateTestUser(t, app, "solopublic")

	cookies := []*http.Cookie{loginCookie(t, app, u)}
	rec := assertRendersCleanly(t, router, "GET", "/me/c/"+coll.Alias, cookies, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, `id="visibility-public"`, "a single-user instance must be offered Public")
	assert.NotContains(t, body, `id="visibility-public" value="1" disabled="disabled"`)
	assert.NotContains(t, body, "The public reader is currently turned off",
		"a single-user instance has no reader, so it must not be blamed for one being off")
}

// TestSingleUserPublicIsNotGatedOnLocalTimeline checks that the reader
// setting, which a single-user instance doesn't serve anyway, doesn't
// disable the option there.
func TestSingleUserPublicIsNotGatedOnLocalTimeline(t *testing.T) {
	app, router := newTemplateTestApp(t, func(cfg *config.Config) {
		cfg.App.SingleUser = true
		cfg.App.LocalTimeline = false
	})
	u, coll, _ := createTemplateTestUser(t, app, "solonotimeline")

	cookies := []*http.Cookie{loginCookie(t, app, u)}
	rec := assertRendersCleanly(t, router, "GET", "/me/c/"+coll.Alias, cookies, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, `id="visibility-public"`)
	assert.NotContains(t, body, `disabled="disabled"`)
}

// TestMultiUserPublicStillFollowsTheLocalTimeline pins the behaviour that
// must not change: on a multi-user instance Public still depends on the
// reader being on, and still says so when it isn't.
func TestMultiUserPublicStillFollowsTheLocalTimeline(t *testing.T) {
	for _, timeline := range []bool{true, false} {
		timeline := timeline
		name := "TimelineOn"
		if !timeline {
			name = "TimelineOff"
		}
		t.Run(name, func(t *testing.T) {
			app, router := newTemplateTestApp(t, func(cfg *config.Config) {
				cfg.App.SingleUser = false
				cfg.App.LocalTimeline = timeline
			})
			u, coll, _ := createTemplateTestUser(t, app, "multi"+name)

			cookies := []*http.Cookie{loginCookie(t, app, u)}
			rec := assertRendersCleanly(t, router, "GET", "/me/c/"+coll.Alias, cookies, http.StatusOK)
			body := rec.Body.String()

			assert.Contains(t, body, `id="visibility-public"`)
			if timeline {
				assert.Contains(t, body, `<a href="/read">reader</a>`)
				assert.NotContains(t, body, `disabled="disabled"`)
			} else {
				assert.Contains(t, body, "The public reader is currently turned off")
				assert.Contains(t, body, `disabled="disabled"`)
			}
		})
	}
}

// TestSingleUserPublicVisibilityPersists checks the setting is more than
// cosmetic: the server accepts visibility=1 from a single-user instance.
func TestSingleUserPublicVisibilityPersists(t *testing.T) {
	app, router := newTemplateTestApp(t, func(cfg *config.Config) {
		cfg.App.SingleUser = true
		// The real-world starting point: no default_visibility, so the
		// blog is created unlisted.
		cfg.App.DefaultVisibility = ""
	})
	u, coll, _ := createTemplateTestUser(t, app, "solopersist")
	assert.True(t, coll.IsUnlisted(), "a blog starts unlisted without default_visibility")

	cookies := []*http.Cookie{loginCookie(t, app, u)}
	rec := postForm(t, router, "/api/collections/"+coll.Alias, cookies, url.Values{
		"web":        {"1"},
		"title":      {coll.Title},
		"visibility": {"1"},
	})
	assert.Less(t, rec.Code, 400, "form submission succeeded")

	loaded, err := app.db.GetCollection(coll.Alias)
	assert.NoError(t, err)
	assert.True(t, loaded.IsPublic(), "a single-user blog must be able to become public")
}
