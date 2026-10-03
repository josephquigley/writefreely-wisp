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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func createSlugRetryPost(app *App, userID, collID int64) (*Post, error) {
	title := "same title"
	content := "body"
	return app.db.CreatePost(userID, collID, &SubmittedPost{Title: &title, Content: &content})
}

func TestCreatePostSlugRetry(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, coll, _ := createTemplateTestUser(t, app, "slugretry")

	orig := genSafeUniqueSlug
	defer func() { genSafeUniqueSlug = orig }()

	first, err := createSlugRetryPost(app, u.ID, coll.ID)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, "same-title", first.Slug.String)

	t.Run("succeeds after colliding suffixes", func(t *testing.T) {
		const collisions = 3
		calls := 0
		genSafeUniqueSlug = func(slug string) string {
			calls++
			if calls <= collisions {
				// Collides with the first post's slug every time.
				return slug
			}
			return slug + "-fresh"
		}
		p, err := createSlugRetryPost(app, u.ID, coll.ID)
		assert.NoError(t, err)
		assert.Equal(t, collisions+1, calls)
		if p != nil {
			assert.Equal(t, "same-title-fresh", p.Slug.String)
		}
	})

	t.Run("gives up after the bound", func(t *testing.T) {
		calls := 0
		genSafeUniqueSlug = func(slug string) string {
			calls++
			return slug
		}
		_, err := createSlugRetryPost(app, u.ID, coll.ID)
		assert.Error(t, err)
		assert.Equal(t, maxSlugRetries, calls)
		if err != nil {
			assert.True(t, strings.Contains(err.Error(), "Retried slug generation"), err.Error())
		}
	})

	t.Run("identical titles all save", func(t *testing.T) {
		genSafeUniqueSlug = orig
		for i := 0; i < 200; i++ {
			_, err := createSlugRetryPost(app, u.ID, coll.ID)
			if !assert.NoError(t, err, fmt.Sprintf("post %d", i)) {
				return
			}
		}
	})

	t.Run("a failed insert leaves later inserts working", func(t *testing.T) {
		// On Postgres a unique violation aborts a transaction; CreatePost must
		// leave the connection usable after exhausting its retries.
		genSafeUniqueSlug = func(slug string) string { return slug }
		_, err := createSlugRetryPost(app, u.ID, coll.ID)
		assert.Error(t, err)
		genSafeUniqueSlug = orig
		_, err = createSlugRetryPost(app, u.ID, coll.ID)
		assert.NoError(t, err)
	})
}
