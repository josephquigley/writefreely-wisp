//go:build sqlite && !wflib
// +build sqlite,!wflib

package writefreely

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/writeas/impart"
)

var testPostSeq int

// insertTestPost inserts a post in the given blog whose slug is its ID.
func insertTestPost(t *testing.T, app *App, userID, collID int64) string {
	t.Helper()
	testPostSeq++
	id := "slugretry" + string(rune('a'+testPostSeq%26)) + "0000" + string(rune('a'+testPostSeq/26%26)) + "x"
	_, err := app.db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES (?, ?, 0, ?, ?, "+app.db.now()+", "+app.db.now()+", 0, '', 'x')", id, id, userID, collID)
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}
	return id
}

// collidingSeam installs a genSafeUniqueSlug that returns its input unchanged
// (so the write collides again) for the first collisions calls, then a fresh
// suffix. It records every input, so tests can check each attempt suffixes the
// original value rather than the previous attempt's.
func collidingSeam(collisions int) (inputs *[]string, restore func()) {
	orig := genSafeUniqueSlug
	var seen []string
	genSafeUniqueSlug = func(slug string) string {
		seen = append(seen, slug)
		if len(seen) <= collisions {
			return slug
		}
		return slug + "-fresh"
	}
	return &seen, func() { genSafeUniqueSlug = orig }
}

func TestAttemptClaimSlugRetry(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, coll, _ := createTemplateTestUser(t, app, "claimretry")
	insertTestPostSlug := func(slug string) {
		t.Helper()
		_, err := app.db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES (?, ?, 0, ?, ?, "+app.db.now()+", "+app.db.now()+", 0, '', 'x')", "taken"+slug, slug, u.ID, coll.ID)
		if err != nil {
			t.Fatalf("insert post: %v", err)
		}
	}
	insertTestPostSlug("claimed")

	claim := func(t *testing.T, postID string) (*ClaimPostRequest, error) {
		t.Helper()
		_, err := app.db.Exec("INSERT INTO posts (id, privacy, owner_id, created, updated, view_count, title, content) VALUES (?, 0, ?, "+app.db.now()+", "+app.db.now()+", 0, '', 'x')", postID, u.ID)
		if err != nil {
			t.Fatalf("insert loose post: %v", err)
		}
		p := &ClaimPostRequest{AnonymousAuthPost: &AnonymousAuthPost{ID: postID}, Slug: "claimed"}
		params := []interface{}{coll.ID, p.Slug, p.ID, u.ID}
		_, err = app.db.AttemptClaim(p, "UPDATE posts SET collection_id = ?, slug = ? WHERE id = ? AND owner_id = ?", params, 1)
		return p, err
	}

	t.Run("suffixes the original slug, not the previous attempt", func(t *testing.T) {
		inputs, restore := collidingSeam(3)
		defer restore()
		p, err := claim(t, "claimloose000001")
		assert.NoError(t, err)
		assert.Equal(t, "claimed-fresh", p.Slug)
		assert.Equal(t, []string{"claimed", "claimed", "claimed", "claimed"}, *inputs)
	})

	t.Run("gives up after the bound", func(t *testing.T) {
		inputs, restore := collidingSeam(1 << 30)
		defer restore()
		_, err := claim(t, "claimloose000002")
		assert.Error(t, err)
		assert.Len(t, *inputs, maxSlugRetries)
		if err != nil {
			assert.Contains(t, err.Error(), "retried slug generation")
		}
	})
}

func TestUpdateOwnedPostSlugRetry(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, coll, _ := createTemplateTestUser(t, app, "updateretry")
	// "hello-world" is already taken by the fixture post.
	slugOf := func(id string) string {
		var s string
		if err := app.db.QueryRow("SELECT slug FROM posts WHERE id = ?", id).Scan(&s); err != nil {
			t.Fatalf("read slug: %v", err)
		}
		return s
	}
	update := func(id string) error {
		slug := "Hello World"
		return app.db.UpdateOwnedPost(&AuthenticatedPost{ID: id, SubmittedPost: &SubmittedPost{Slug: &slug}}, u.ID)
	}

	t.Run("succeeds after colliding suffixes", func(t *testing.T) {
		id := insertTestPost(t, app, u.ID, coll.ID)
		inputs, restore := collidingSeam(3)
		defer restore()
		assert.NoError(t, update(id))
		assert.Equal(t, "hello-world-fresh", slugOf(id))
		assert.Equal(t, []string{"hello-world", "hello-world", "hello-world", "hello-world"}, *inputs)
	})

	t.Run("gives up after the bound and stays usable", func(t *testing.T) {
		id := insertTestPost(t, app, u.ID, coll.ID)
		inputs, restore := collidingSeam(1 << 30)
		err := update(id)
		restore()
		assert.Error(t, err)
		assert.Len(t, *inputs, maxSlugRetries)
		assert.Equal(t, id, slugOf(id), "slug is unchanged after a failed update")
		// A later write on the same connection pool still works.
		assert.NoError(t, update(id))
	})
}

func TestCreateCollectionAliasRetry(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, _, _ := createTemplateTestUser(t, app, "aliasretry") // owns alias "aliasretry"

	t.Run("generated alias succeeds after colliding suffixes", func(t *testing.T) {
		inputs, restore := collidingSeam(3)
		defer restore()
		c, err := app.db.CreateCollection(app.cfg, "aliasretry", "Title", u.ID, true)
		if assert.NoError(t, err) {
			assert.Equal(t, "aliasretry-fresh", c.Alias)
		}
		assert.Equal(t, []string{"aliasretry", "aliasretry", "aliasretry", "aliasretry"}, *inputs)
	})

	t.Run("generated alias gives up after the bound", func(t *testing.T) {
		inputs, restore := collidingSeam(1 << 30)
		defer restore()
		_, err := app.db.CreateCollection(app.cfg, "aliasretry", "Title", u.ID, true)
		assert.Error(t, err)
		assert.Len(t, *inputs, maxSlugRetries)
		assert.Equal(t, http.StatusConflict, httpStatus(err))
	})

	t.Run("user-chosen alias is never suffixed", func(t *testing.T) {
		inputs, restore := collidingSeam(0)
		defer restore()
		_, err := app.db.CreateCollection(app.cfg, "aliasretry", "Title", u.ID, false)
		assert.Error(t, err)
		assert.Equal(t, http.StatusConflict, httpStatus(err))
		assert.Empty(t, *inputs, "the suffix generator must not run")
		var n int
		assert.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM collections WHERE alias LIKE 'aliasretry-%'").Scan(&n))
		assert.Equal(t, 1, n, "only the earlier subtest's generated alias exists")
	})

	t.Run("signup keeps the username as the alias", func(t *testing.T) {
		inputs, restore := collidingSeam(0)
		defer restore()
		// Another user's blog already has the alias; the username is free.
		other := &User{Username: "somebodyelse", HashedPass: []byte("x")}
		assert.NoError(t, app.db.CreateUser(app.cfg, other, "", ""))
		_, err := app.db.Exec("UPDATE collections SET alias = ? WHERE owner_id = ?", "takenalias", other.ID)
		assert.NoError(t, err)
		err = app.db.CreateUser(app.cfg, &User{Username: "takenalias", HashedPass: []byte("x")}, "", "")
		assert.Equal(t, http.StatusConflict, httpStatus(err))
		assert.Empty(t, *inputs)
	})
}

func httpStatus(err error) int {
	if he, ok := err.(impart.HTTPError); ok {
		return he.Status
	}
	return 0
}
