//go:build sqlite

package writefreely

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/writeas/impart"
)

// TestViewCollectionTagReturnsPostsError breaks GetPostsTagged's query (by
// renaming a column it selects) while leaving GetAllPostsTaggedIDs's query
// working, so the handler gets past the tagged-ID count and then fails to
// fetch the posts. handleViewCollectionTag used to discard that error and
// render the tag page as though the collection had no posts for the tag.
func TestViewCollectionTagReturnsPostsError(t *testing.T) {
	app := newSignupTestApp(t)

	for _, q := range []string{
		"INSERT INTO users (id, username, password) VALUES (1, 'alice', 'x')",
		"INSERT INTO collections (id, alias, title, description, privacy, owner_id, view_count) VALUES (1, 'alice', 'Alice', '', 1, 1, 0)",
		"INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES ('p1', 'p1', 0, 1, 1, '2020-01-01 00:00:00', '2020-01-01 00:00:00', 0, '', 'about #go today')",
		"ALTER TABLE posts RENAME COLUMN text_appearance TO text_appearance_gone",
	} {
		if _, err := app.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	r := httptest.NewRequest("GET", "/alice/tag:go", nil)
	r = mux.SetURLVars(r, map[string]string{"collection": "alice", "tag": "go"})
	w := httptest.NewRecorder()

	err := handleViewCollectionTag(app, w, r)
	herr, ok := err.(impart.HTTPError)
	if !ok || herr.Status != http.StatusInternalServerError {
		t.Fatalf("handleViewCollectionTag returned %#v; want a 500 from GetPostsTagged", err)
	}
}
