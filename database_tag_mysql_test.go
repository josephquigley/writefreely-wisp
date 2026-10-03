package writefreely

import (
	"database/sql"
	"sort"
	"strings"
	"testing"

	"github.com/writefreely/writefreely/config"
)

// TestTaggedPostQueriesOnMySQL runs both tag queries against the MySQL test
// database. MySQL 8.0.4+ uses ICU regular expressions, which reject the
// Spencer word boundary "[[:>:]]" with ERROR 3685, so each query must pick its
// boundary from the server version.
func TestTaggedPostQueriesOnMySQL(t *testing.T) {
	// MySQL-only: it is about MySQL's two regex engines.
	if !runAnyMySQLTests() {
		t.Skip("skipping mysql tests")
	}
	withTestDB(t, func(db *sql.DB) {
		ds := &datastore{DB: db, driverName: driverMySQL}
		ver, err := ds.version()
		if err != nil {
			t.Fatalf("version: %v", err)
		}
		// The same rule connectToDatabase's caller applies in app.go.
		ds.useSpencerRegex = strings.HasPrefix(ver, "5.")

		posts := map[string]string{
			"gopost":  "about #go today",
			"goxpost": "about #gox today",
		}
		for id, content := range posts {
			_, err := db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES (?, ?, 0, 1, 1, '2020-01-01 00:00:00', '2020-01-01 00:00:00', 0, '', ?)", id, id, content)
			if err != nil {
				t.Fatalf("insert %s: %v", id, err)
			}
		}

		cfg := config.New()
		c := &Collection{ID: 1}
		want := []string{"gopost"}

		ids, err := ds.GetAllPostsTaggedIDs(c, "go", true)
		if err != nil {
			t.Errorf("GetAllPostsTaggedIDs on %s: %v", ver, err)
		}
		sort.Strings(ids)
		if !equalTagIDs(ids, want) {
			t.Errorf("GetAllPostsTaggedIDs on %s = %v, want %v", ver, ids, want)
		}

		pp, err := ds.GetPostsTagged(cfg, c, "go", 1, true)
		if err != nil {
			t.Fatalf("GetPostsTagged on %s: %v", ver, err)
		}
		var got []string
		for _, p := range *pp {
			got = append(got, p.ID)
		}
		sort.Strings(got)
		if !equalTagIDs(got, want) {
			t.Errorf("GetPostsTagged on %s = %v, want %v", ver, got, want)
		}
	})
}

// TestTagWordBoundary checks the boundary each MySQL tag query uses: "\b" for
// ICU (MySQL 8.0.4+, and MariaDB's PCRE) and "[[:>:]]" only for MySQL 5.x.
func TestTagWordBoundary(t *testing.T) {
	if got := (&datastore{}).tagWordBoundary(); got != `\b` {
		t.Errorf("ICU boundary = %q, want %q", got, `\b`)
	}
	if got := (&datastore{useSpencerRegex: true}).tagWordBoundary(); got != "[[:>:]]" {
		t.Errorf("Spencer boundary = %q, want %q", got, "[[:>:]]")
	}
}

func equalTagIDs(a, b []string) bool {
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
