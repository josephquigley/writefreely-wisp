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
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/writefreely/writefreely/config"
)

func TestGetCollectionPostsForOwner(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, coll, _ := createTemplateTestUser(t, app, "listowner")

	// createTemplateTestUser leaves one post on the collection.
	posts, total, err := app.db.GetCollectionPostsForOwner(coll.ID, 1)
	assert.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, *posts, 1)
	assert.NotZero(t, u.ID)

	// Bodies must not be loaded -- this view never renders content.
	assert.Empty(t, (*posts)[0].Content)
}

func TestGetCollectionPostsForOwnerPaging(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, coll, _ := createTemplateTestUser(t, app, "pager")

	content := "body"
	for i := 0; i < postListPageSize+5; i++ {
		// Distinct titles give distinct slugs. A shared title makes every
		// insert after the first collide on (collection_id, slug) and fall
		// back to a random suffix, which can itself collide.
		title := fmt.Sprintf("post %d", i)
		_, err := app.db.CreatePost(u.ID, coll.ID, &SubmittedPost{Title: &title, Content: &content})
		assert.NoError(t, err)
	}

	first, total, err := app.db.GetCollectionPostsForOwner(coll.ID, 1)
	assert.NoError(t, err)
	assert.Equal(t, postListPageSize+6, total) // +1 for the user's initial post
	assert.Len(t, *first, postListPageSize)

	second, _, err := app.db.GetCollectionPostsForOwner(coll.ID, 2)
	assert.NoError(t, err)
	assert.Len(t, *second, 6)
}

func TestGetAllPostsForAdmin(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	_, collA, _ := createTemplateTestUser(t, app, "adminlista")
	_, collB, _ := createTemplateTestUser(t, app, "adminlistb")

	posts, total, err := app.db.GetAllPostsForAdmin(1)
	assert.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, *posts, 2)

	aliases := map[string]bool{}
	for _, p := range *posts {
		aliases[p.Collection.Alias] = true
	}
	assert.True(t, aliases[collA.Alias])
	assert.True(t, aliases[collB.Alias])
}

func TestPostRowPartialIsParsed(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	assert.NotNil(t, app)

	tmpl, ok := userPages["user/collection-posts.tmpl"]
	assert.True(t, ok, "collection-posts template should be registered")
	if !ok {
		return
	}
	assert.NotNil(t, tmpl.Lookup("post-row"), "post-row partial should be parsed into user pages")
}

func TestViewCollectionPostsOwner(t *testing.T) {
	for _, singleUser := range []bool{true, false} {
		singleUser := singleUser
		name := "MultiUser"
		if singleUser {
			name = "SingleUser"
		}
		t.Run(name, func(t *testing.T) {
			app, router := newTemplateTestApp(t, func(cfg *config.Config) {
				cfg.App.SingleUser = singleUser
			})
			u, coll, post := createTemplateTestUser(t, app, "listviewer")

			cookies := []*http.Cookie{loginCookie(t, app, u)}
			rec := assertRendersCleanly(t, router, "GET", "/me/c/"+coll.Alias+"/posts", cookies, http.StatusOK)

			body := rec.Body.String()
			assert.Contains(t, body, "post-"+post.ID)
			assert.Contains(t, body, "Hello World")
			assert.Contains(t, body, post.Slug.String+"/edit")
			// The list must never render post bodies.
			assert.NotContains(t, body, "This is a **test** post")
		})
	}
}

func TestViewCollectionPostsRejectsNonOwner(t *testing.T) {
	app, router := newTemplateTestApp(t, nil)
	_, coll, _ := createTemplateTestUser(t, app, "listowner2")
	other, _, _ := createTemplateTestUser(t, app, "listintruder")

	cookies := []*http.Cookie{loginCookie(t, app, other)}
	rec, _ := renderedRequest(t, router, "GET", "/me/c/"+coll.Alias+"/posts", cookies)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestViewAdminPosts(t *testing.T) {
	app, router := newTemplateTestApp(t, nil)
	// IsAdmin is true for the instance's first user, so create the admin
	// before anyone else.
	admin, _, adminPost := createTemplateTestUser(t, app, "postsadmin")
	assert.True(t, admin.IsAdmin())
	_, otherColl, otherPost := createTemplateTestUser(t, app, "postsauthor")

	cookies := []*http.Cookie{loginCookie(t, app, admin)}
	rec := assertRendersCleanly(t, router, "GET", "/admin/posts", cookies, http.StatusOK)

	body := rec.Body.String()
	assert.Contains(t, body, "post-"+adminPost.ID)
	assert.Contains(t, body, "post-"+otherPost.ID)
	// The blog column names every post's own collection.
	assert.Contains(t, body, "/"+otherColl.Alias+"/")
}

func TestViewAdminPostsRejectsNonAdmin(t *testing.T) {
	app, router := newTemplateTestApp(t, nil)
	createTemplateTestUser(t, app, "theadmin")
	u, _, _ := createTemplateTestUser(t, app, "notanadmin")
	assert.False(t, u.IsAdmin())

	cookies := []*http.Cookie{loginCookie(t, app, u)}
	rec, _ := renderedRequest(t, router, "GET", "/admin/posts", cookies)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPostRowShowsPinnedState(t *testing.T) {
	app, router := newTemplateTestApp(t, nil)
	u, coll, post := createTemplateTestUser(t, app, "pinlister")

	err := app.db.UpdatePostPinState(true, post.ID, coll.ID, u.ID, 1)
	assert.NoError(t, err)

	cookies := []*http.Cookie{loginCookie(t, app, u)}
	rec := assertRendersCleanly(t, router, "GET", "/me/c/"+coll.Alias+"/posts", cookies, http.StatusOK)

	body := rec.Body.String()
	assert.Contains(t, body, `<span class="badge pinned-badge">Pinned</span>`)
	assert.Contains(t, body, ">unpin<")
}

func TestPostRowLabelsUntitledPostWithItsDate(t *testing.T) {
	app, router := newTemplateTestApp(t, nil)
	u, coll, _ := createTemplateTestUser(t, app, "untitledlister")

	title := ""
	content := "An untitled post, which has no title of its own."
	untitled, err := app.db.CreatePost(u.ID, coll.ID, &SubmittedPost{Title: &title, Content: &content})
	assert.NoError(t, err)

	cookies := []*http.Cookie{loginCookie(t, app, u)}
	rec := assertRendersCleanly(t, router, "GET", "/me/c/"+coll.Alias+"/posts", cookies, http.StatusOK)

	posts, _, err := app.db.GetCollectionPostsForOwner(coll.ID, 1)
	assert.NoError(t, err)
	var row *PublicPost
	for i := range *posts {
		if (*posts)[i].ID == untitled.ID {
			row = &(*posts)[i]
		}
	}
	assert.NotNil(t, row)
	if row == nil {
		return
	}

	assert.Contains(t, rec.Body.String(), ">"+row.DisplayDate+"</a>")
}
