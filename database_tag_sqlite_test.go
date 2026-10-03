//go:build sqlite

package writefreely

import (
	"database/sql"
	"path/filepath"
	"sort"
	"testing"

	"github.com/writefreely/writefreely/config"
)

// TestTaggedPostQueriesMatchTagLiterally runs the tag queries against a real
// SQLite database, whose regexp() is Go's regexp package. A tag is taken from
// the request URL; its regex metacharacters must match only themselves.
func TestTaggedPostQueriesMatchTagLiterally(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	db, err := sql.Open("sqlite3_with_regex", dbPath+"?parseTime=true")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := config.New()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	cfg.App.Host = "http://localhost:0"
	app := &App{db: &datastore{DB: db, driverName: driverSQLite}, cfg: cfg}
	if err := adminInitDatabase(app); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	posts := map[string]string{
		"dotpost": "about #g.x today",
		"oxpost":  "about #gox today",
		"parpost": "about #a(b today",
	}
	for id, content := range posts {
		_, err := db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES (?, ?, 0, 1, 1, '2020-01-01 00:00:00', '2020-01-01 00:00:00', 0, '', ?)", id, id, content)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	c := &Collection{ID: 1}
	cases := []struct {
		tag  string
		want []string
	}{
		{"g.x", []string{"dotpost"}}, // "." must not match the "o" in #gox
		{"gox", []string{"oxpost"}},
		{"a(b", []string{"parpost"}}, // unescaped, "(" is a regex syntax error
		{"g.*", nil},
		{".*", nil},
	}
	for _, tc := range cases {
		ids, err := app.db.GetAllPostsTaggedIDs(c, tc.tag, true)
		if err != nil {
			t.Errorf("GetAllPostsTaggedIDs(%q): %v", tc.tag, err)
		}
		sort.Strings(ids)
		if !equalIDs(ids, tc.want) {
			t.Errorf("GetAllPostsTaggedIDs(%q) = %v, want %v", tc.tag, ids, tc.want)
		}

		pp, err := app.db.GetPostsTagged(cfg, c, tc.tag, 1, true)
		if err != nil {
			t.Errorf("GetPostsTagged(%q): %v", tc.tag, err)
			continue
		}
		var got []string
		for _, p := range *pp {
			got = append(got, p.ID)
		}
		sort.Strings(got)
		if !equalIDs(got, tc.want) {
			t.Errorf("GetPostsTagged(%q) = %v, want %v", tc.tag, got, tc.want)
		}
	}
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
