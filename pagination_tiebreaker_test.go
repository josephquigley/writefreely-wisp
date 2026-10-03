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
	"sort"
	"testing"
)

// tiedCreated is the timestamp every row in these tests is forced to, so
// that "ORDER BY created" alone cannot order them.
const tiedCreated = "2020-01-01 00:00:00"

// assertPagedInIDOrder checks that pages, read in order, return every one of
// want exactly once and that rows sharing a created timestamp come back
// ordered by id in the given direction. Without an id tiebreaker the order
// among tied rows is unspecified, so pages can repeat or skip rows.
func assertPagedInIDOrder(t *testing.T, name string, pages [][]string, want []string, desc bool) {
	t.Helper()

	var got []string
	seen := map[string]bool{}
	for _, page := range pages {
		for _, id := range page {
			if seen[id] {
				t.Errorf("%s: id %s returned on more than one page", name, id)
			}
			seen[id] = true
			got = append(got, id)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("%s: got %d rows across pages, want %d", name, len(got), len(want))
	}

	expected := append([]string(nil), want...)
	sort.Strings(expected)
	if desc {
		sort.Sort(sort.Reverse(sort.StringSlice(expected)))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("%s: rows with a tied created are not ordered by id:\n got  %v\n want %v", name, got, expected)
		}
	}
}

func postIDs(posts *[]PublicPost) []string {
	ids := []string{}
	for _, p := range *posts {
		ids = append(ids, p.ID)
	}
	return ids
}

func TestPaginationTiebreakerCollectionPosts(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, coll, first := createTemplateTestUser(t, app, "tiebreak")

	all := []string{first.ID}
	title := "tied"
	for i := 0; i < 2*postsPerPage+3; i++ {
		content := fmt.Sprintf("post %d #tied", i)
		p, err := app.db.CreatePost(u.ID, coll.ID, &SubmittedPost{Title: &title, Content: &content})
		if err != nil {
			t.Fatalf("create post: %v", err)
		}
		all = append(all, p.ID)
	}
	// Give every post the same created time, and a language and tag to
	// filter on.
	// || is string concatenation on SQLite and Postgres, but OR on MySQL.
	appendTag := "content || ' #tied'"
	if app.db.driverName == driverMySQL {
		appendTag = "CONCAT(content, ' #tied')"
	}
	if _, err := app.db.Exec("UPDATE posts SET created = ?, language = 'en', content = "+appendTag+" WHERE collection_id = ?", tiedCreated, coll.ID); err != nil {
		t.Fatalf("tie created: %v", err)
	}

	collect := func(name string, fetch func(page int) (*[]PublicPost, error)) {
		t.Run(name, func(t *testing.T) {
			var pages [][]string
			for page := 1; page <= 3; page++ {
				posts, err := fetch(page)
				if err != nil {
					t.Fatalf("%s page %d: %v", name, page, err)
				}
				pages = append(pages, postIDs(posts))
			}
			assertPagedInIDOrder(t, name, pages, all, true)
		})
	}

	collect("GetPosts", func(page int) (*[]PublicPost, error) {
		return app.db.GetPosts(app.cfg, coll, page, false, true, false, "")
	})
	collect("GetPostsTagged", func(page int) (*[]PublicPost, error) {
		return app.db.GetPostsTagged(app.cfg, coll, "tied", page, false)
	})
	collect("GetLangPosts", func(page int) (*[]PublicPost, error) {
		return app.db.GetLangPosts(app.cfg, coll, "en", page, false)
	})
}

func TestPaginationTiebreakerAnonymousPosts(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, _, _ := createTemplateTestUser(t, app, "anontiebreak")

	var all []string
	title := "anon"
	content := "anonymous post"
	for i := 0; i < 23; i++ {
		p, err := app.db.CreatePost(u.ID, 0, &SubmittedPost{Title: &title, Content: &content})
		if err != nil {
			t.Fatalf("create anonymous post: %v", err)
		}
		all = append(all, p.ID)
	}
	if _, err := app.db.Exec("UPDATE posts SET created = ? WHERE owner_id = ? AND collection_id IS NULL", tiedCreated, u.ID); err != nil {
		t.Fatalf("tie created: %v", err)
	}

	var pages [][]string
	for page := 1; page <= 3; page++ {
		posts, err := app.db.GetAnonymousPosts(u, page)
		if err != nil {
			t.Fatalf("GetAnonymousPosts page %d: %v", page, err)
		}
		pages = append(pages, postIDs(posts))
	}
	assertPagedInIDOrder(t, "GetAnonymousPosts", pages, all, true)
}

func TestPaginationTiebreakerAllUsers(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)

	// Insert directly: creating users through CreateUser hashes a password
	// for each one, which is slow and irrelevant here.
	n := 2*adminUsersPerPage + 3
	for i := 0; i < n; i++ {
		if _, err := app.db.Exec("INSERT INTO users (username, password, email, created) VALUES (?, '', NULL, ?)", fmt.Sprintf("tieuser%02d", i), tiedCreated); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	var got []int64
	seen := map[int64]bool{}
	for page := uint(1); page <= 3; page++ {
		users, err := app.db.GetAllUsers(page)
		if err != nil {
			t.Fatalf("GetAllUsers page %d: %v", page, err)
		}
		for _, u := range *users {
			if seen[u.ID] {
				t.Errorf("GetAllUsers: user %d returned on more than one page", u.ID)
			}
			seen[u.ID] = true
			got = append(got, u.ID)
		}
	}
	if len(got) != n {
		t.Fatalf("GetAllUsers: got %d users across pages, want %d", len(got), n)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i] > got[j] }) {
		t.Fatalf("GetAllUsers: users with a tied created are not ordered by id DESC: %v", got)
	}
}
