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
	"html/template"
	"net/http"

	"github.com/gorilla/csrf"
	"github.com/gorilla/mux"
	"github.com/writeas/impart"
)

// pinnedActionResult is what the page's script receives after a reorder or
// an unpin. Message is empty for actions that have nothing to report.
type pinnedActionResult struct {
	Message string `json:"message"`
}

// pinnedPostsPage is the view data for the pinned post management page.
type pinnedPostsPage struct {
	*UserPage
	Collection  *Collection
	Alias       string
	SingleUser  bool
	CSRFField   template.HTML
	PinnedPosts []PublicPost

	// LastIndex is the index of the final pinned post, so the template can
	// tell which row should omit its "move down" control without doing
	// arithmetic.
	LastIndex int
}

// ownedPinnedCollection resolves the {collection} route variable and returns
// it only if the given user owns it. Someone else's blog gets the same "not
// found" error a nonexistent one does, so the response doesn't confirm that
// the blog exists.
func ownedPinnedCollection(app *App, u *User, r *http.Request) (*Collection, error) {
	c, err := app.db.GetCollection(mux.Vars(r)["collection"])
	if err != nil {
		return nil, err
	}
	if c.OwnerID != u.ID {
		return nil, ErrCollectionNotFound
	}
	c.hostName = app.Config().App.Host
	return c, nil
}

// unpinnedMessage confirms a post was removed from the navigation. Both
// the redirect path and the scripted path report it, so it lives in one
// place rather than being repeated in the template.
const unpinnedMessage = "Removed that post from your blog's navigation."

// isXHR reports whether the request came from the page's own script
// rather than from a plain form submission.
func isXHR(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// viewPinnedPosts renders the pinned posts of one of the user's blogs, in
// navigation order, with controls to reorder and unpin them.
func viewPinnedPosts(app *App, u *User, w http.ResponseWriter, r *http.Request) error {
	c, err := ownedPinnedCollection(app, u, r)
	if err != nil {
		return err
	}

	// Repair any drift before reading, so the order shown matches the order
	// the move controls will operate on.
	if err := app.db.NormalizePinnedPositions(c.ID); err != nil {
		return err
	}

	posts, err := app.db.GetPinnedPosts(&CollectionObj{Collection: *c}, true)
	if err != nil {
		return err
	}

	flashes, _ := getSessionFlashes(app, w, r, nil)

	p := pinnedPostsPage{
		UserPage:    NewUserPage(app, r, u, c.DisplayTitle()+" Pinned Posts", flashes),
		Collection:  c,
		Alias:       c.Alias,
		SingleUser:  app.Config().App.SingleUser,
		CSRFField:   csrf.TemplateField(r),
		PinnedPosts: *posts,
		LastIndex:   len(*posts) - 1,
	}
	showUserPage(w, "collection-pinned", p)
	return nil
}

// handlePinnedPostAction moves a pinned post up or down in the blog's
// navigation, or unpins it. The action comes from the {action} route
// variable. Every action redirects back to the management page.
func handlePinnedPostAction(app *App, u *User, w http.ResponseWriter, r *http.Request) error {
	c, err := ownedPinnedCollection(app, u, r)
	if err != nil {
		return err
	}

	vars := mux.Vars(r)
	postID := normalizePostID(vars["post"])
	action := vars["action"]

	if action != "up" && action != "down" && action != "remove" {
		return impart.HTTPError{http.StatusNotFound, "No such action."}
	}

	// Repair any drift first, so the positions this acts on are dense, and
	// reject anything that isn't a pinned post of this user's blog before
	// writing at all.
	if err := app.db.NormalizePinnedPositions(c.ID); err != nil {
		return err
	}
	if _, err := app.db.pinnedPosition(c.ID, u.ID, postID); err != nil {
		return err
	}

	if action == "remove" {
		if err := app.db.UpdatePostPinState(false, postID, c.ID, u.ID, 0); err != nil {
			return err
		}
		// A flash is only shown by the next page render. The scripted
		// path never reloads, so stashing one there would leave it to
		// appear later on an unrelated page; that request carries the
		// message in its response instead.
		if !isXHR(r) {
			_ = addSessionFlash(app, w, r, unpinnedMessage, nil)
		}
	} else {
		neighbor, err := app.db.GetAdjacentPinnedPost(c.ID, u.ID, postID, action == "up")
		if err != nil {
			return err
		}
		// An empty neighbor means the post is already at the end it was
		// asked to move toward. That isn't an error -- the control simply
		// had nothing to do.
		if neighbor != "" {
			if err := app.db.SwapPinnedPositions(c.ID, u.ID, postID, neighbor); err != nil {
				return err
			}
		}
	}

	if err := app.db.NormalizePinnedPositions(c.ID); err != nil {
		return err
	}

	// The page enhances these forms with a background request when
	// scripting is available. Answering those with a redirect would have
	// the browser re-render the whole page just to discard it, so return
	// the outcome and let the script update the list in place. Without
	// scripting the form submits normally and the redirect reloads the
	// page.
	if isXHR(r) {
		msg := ""
		if action == "remove" {
			msg = unpinnedMessage
		}
		return impart.WriteSuccess(w, pinnedActionResult{Message: msg}, http.StatusOK)
	}

	return impart.HTTPError{http.StatusFound, "/me/c/" + c.Alias + "/pinned"}
}
