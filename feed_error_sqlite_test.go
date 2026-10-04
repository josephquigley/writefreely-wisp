//go:build sqlite

package writefreely

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/writeas/impart"
)

func newFeedTestApp(t *testing.T) *App {
	t.Helper()
	app := newSignupTestApp(t)
	for _, q := range []string{
		"INSERT INTO users (id, username, password) VALUES (1, 'alice', 'x')",
		"INSERT INTO collections (id, alias, title, description, privacy, owner_id, view_count) VALUES (1, 'alice', 'Alice', '', 1, 1, 0)",
		"INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES ('p1', 'p1', 0, 1, 1, '2020-01-01 00:00:00', '2020-01-01 00:00:00', 0, '', 'about #go today')",
	} {
		if _, err := app.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return app
}

func feedRequest(app *App, tag string) (*httptest.ResponseRecorder, error) {
	vars := map[string]string{"collection": "alice"}
	path := "/alice/feed/"
	if tag != "" {
		vars["tag"] = tag
		path = "/alice/tag:" + tag + "/feed/"
	}
	r := httptest.NewRequest("GET", path, nil)
	r = mux.SetURLVars(r, vars)
	w := httptest.NewRecorder()
	return w, ViewFeed(app, w, r)
}

// TestViewFeedReturnsPostsError breaks the posts query (by renaming a column
// it selects). ViewFeed used to discard the error and dereference the nil
// result, panicking, for both the tag feed and the plain feed.
func TestViewFeedReturnsPostsError(t *testing.T) {
	app := newFeedTestApp(t)
	if _, err := app.db.Exec("ALTER TABLE posts RENAME COLUMN text_appearance TO text_appearance_gone"); err != nil {
		t.Fatal(err)
	}

	for _, tag := range []string{"go", ""} {
		name := "tag"
		if tag == "" {
			name = "plain"
		}
		t.Run(name, func(t *testing.T) {
			_, err := feedRequest(app, tag)
			herr, ok := err.(impart.HTTPError)
			if !ok || herr.Status != http.StatusInternalServerError {
				t.Fatalf("ViewFeed returned %#v; want a 500", err)
			}
		})
	}
}

func TestViewFeedTagWorks(t *testing.T) {
	app := newFeedTestApp(t)
	w, err := feedRequest(app, "go")
	if err != nil {
		t.Fatalf("ViewFeed: %v", err)
	}
	if body := w.Body.String(); !strings.Contains(body, "about #go today") {
		t.Fatalf("tag feed missing post: %s", body)
	}
}
