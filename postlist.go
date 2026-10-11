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
	"strconv"

	"github.com/gorilla/mux"
	"github.com/writeas/web-core/log"
)

// postListPage is the view data shared by the post management pages.
type postListPage struct {
	*UserPage
	Collection  *Collection
	Posts       *[]PublicPost
	Collections *[]Collection
	Alias       string
	ShowBlog    bool
	SingleUser  bool
	Silenced    bool
	CurrentPage int
	TotalPages  []int
}

// pageParam reads a 1-indexed page number from the request's p query
// parameter, defaulting to 1 when absent or unparseable. It matches the
// parameter name used by the admin users list.
func pageParam(r *http.Request) int {
	p, err := strconv.Atoi(r.FormValue("p"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

// pageNumbers returns the 1-indexed page numbers needed to show total
// items at postListPageSize per page, always at least one page.
func pageNumbers(total int) []int {
	n := 1
	if total > 0 {
		n = (total-1)/postListPageSize + 1
	}
	pages := []int{}
	for i := 1; i <= n; i++ {
		pages = append(pages, i)
	}
	return pages
}

// viewCollectionPosts renders a compact, management-oriented list of one of
// the user's blogs, with edit, pin and delete actions on every row.
func viewCollectionPosts(app *App, u *User, w http.ResponseWriter, r *http.Request) error {
	c, err := app.db.GetCollection(mux.Vars(r)["collection"])
	if err != nil {
		return err
	}
	if c.OwnerID != u.ID {
		// 404 rather than 403: don't confirm that someone else's blog exists.
		return ErrCollectionNotFound
	}
	c.hostName = app.Config().App.Host

	page := pageParam(r)
	posts, total, err := app.db.GetCollectionPostsForOwner(c.ID, page)
	if err != nil {
		return err
	}

	colls, err := app.db.GetPublishableCollections(u, app.Config().App.Host)
	if err != nil {
		log.Error("view collection posts: get collections: %v", err)
		return err
	}

	flashes, _ := getSessionFlashes(app, w, r, nil)

	obj := &postListPage{
		UserPage:    NewUserPage(app, r, u, c.DisplayTitle()+" Posts", flashes),
		Collection:  c,
		Posts:       posts,
		Collections: colls,
		Alias:       c.Alias,
		ShowBlog:    false,
		SingleUser:  app.Config().App.SingleUser,
		Silenced:    u.IsSilenced(),
		CurrentPage: page,
		TotalPages:  pageNumbers(total),
	}
	obj.UserPage.CollAlias = c.Alias

	showUserPage(w, "collection-posts", obj)
	return nil
}

// handleViewAdminPosts renders a paginated list of every post on the
// instance, with the same per-row actions as the owner-facing view.
func handleViewAdminPosts(app *App, u *User, w http.ResponseWriter, r *http.Request) error {
	page := pageParam(r)
	posts, total, err := app.db.GetAllPostsForAdmin(page)
	if err != nil {
		return err
	}

	flashes, _ := getSessionFlashes(app, w, r, nil)

	obj := &postListPage{
		UserPage:    NewUserPage(app, r, u, "Posts", flashes),
		Posts:       posts,
		ShowBlog:    true,
		SingleUser:  app.Config().App.SingleUser,
		Silenced:    u.IsSilenced(),
		CurrentPage: page,
		TotalPages:  pageNumbers(total),
	}

	showUserPage(w, "posts", obj)
	return nil
}
