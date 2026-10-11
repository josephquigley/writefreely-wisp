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
	"fmt"
	"net/http"
	"testing"

	"github.com/writeas/impart"
)

// TestCreateCollectionEnforcesMaxBlogs pins the cap upstream moved into
// CreateCollection, at the testMaxBlogs value the other fixtures rely on: a
// user may fill the cap, and the next blog is refused.
func TestCreateCollectionEnforcesMaxBlogs(t *testing.T) {
	app := newAnnounceTestApp(t)
	userID := announceTestUserID(t, app)

	n, err := app.db.GetUserCollectionCount(userID)
	if err != nil {
		t.Fatalf("count blogs: %v", err)
	}
	for ; n < testMaxBlogs; n++ {
		alias := fmt.Sprintf("blog%d", n)
		if _, err := app.db.CreateCollection(app.cfg, alias, alias, userID); err != nil {
			t.Fatalf("blog %d of %d refused: %v", n+1, testMaxBlogs, err)
		}
	}

	_, err = app.db.CreateCollection(app.cfg, "onetoomany", "onetoomany", userID)
	herr, ok := err.(impart.HTTPError)
	if !ok || herr.Status != http.StatusForbidden {
		t.Fatalf("blog %d: err = %v, want a 403 for the blog cap", testMaxBlogs+1, err)
	}
}
