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
	"testing"
)

// TestPaginationTiebreakerForkQueries covers the fork-only paginated
// queries: the owner's post list, the admin post list and the instance
// actor's outbox. See pagination_tiebreaker_test.go for the shared ones.
func TestPaginationTiebreakerForkQueries(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, coll, first := createTemplateTestUser(t, app, "forktiebreak")

	all := []string{first.ID}
	title := "tied"
	for i := 0; i < 2*postListPageSize+3; i++ {
		content := fmt.Sprintf("post %d", i)
		p, err := app.db.CreatePost(u.ID, coll.ID, &SubmittedPost{Title: &title, Content: &content})
		if err != nil {
			t.Fatalf("create post: %v", err)
		}
		all = append(all, p.ID)
	}
	if _, err := app.db.Exec("UPDATE posts SET created = ? WHERE collection_id = ?", tiedCreated, coll.ID); err != nil {
		t.Fatalf("tie created: %v", err)
	}

	collect := func(name string, fetch func(page int) ([]string, error)) {
		t.Run(name, func(t *testing.T) {
			var pages [][]string
			for page := 1; page <= 3; page++ {
				ids, err := fetch(page)
				if err != nil {
					t.Fatalf("%s page %d: %v", name, page, err)
				}
				pages = append(pages, ids)
			}
			assertPagedInIDOrder(t, name, pages, all, true)
		})
	}

	collect("GetCollectionPostsForOwner", func(page int) ([]string, error) {
		posts, _, err := app.db.GetCollectionPostsForOwner(coll.ID, page)
		if err != nil {
			return nil, err
		}
		return postIDs(posts), nil
	})
	collect("GetAllPostsForAdmin", func(page int) ([]string, error) {
		posts, _, err := app.db.GetAllPostsForAdmin(page)
		if err != nil {
			return nil, err
		}
		return postIDs(posts), nil
	})
	collect("GetPublicPostsToAnnounce", func(page int) ([]string, error) {
		posts, err := app.db.GetPublicPostsToAnnounce(page, postListPageSize)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for _, p := range *posts {
			ids = append(ids, p.ID)
		}
		return ids, nil
	})
}
