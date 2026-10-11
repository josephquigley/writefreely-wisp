//go:build sqlite

package writefreely

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/writefreely/writefreely/config"
)

// openPostsTaggedTestDB opens a SQLite database through driverName and
// creates a minimal posts table holding the columns GetPostsTagged selects.
// The id column allows NULL, which the real schema forbids, so a row can be
// made unscannable.
func openPostsTaggedTestDB(t *testing.T, driverName string, rows ...string) *datastore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	db, err := sql.Open(driverName, dbPath+"?parseTime=true")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	qs := append([]string{"CREATE TABLE posts (id TEXT, slug TEXT, text_appearance TEXT NOT NULL DEFAULT 'norm', language TEXT, rtl INTEGER, privacy INTEGER NOT NULL DEFAULT 0, owner_id INTEGER, collection_id INTEGER, pinned_position INTEGER, created DATETIME, updated DATETIME, view_count INTEGER NOT NULL DEFAULT 0, title TEXT NOT NULL DEFAULT '', content TEXT NOT NULL)"}, rows...)
	for _, q := range qs {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return &datastore{DB: db, driverName: driverSQLite}
}

// TestGetPostsTaggedReturnsScanError gives the tag query a row whose id
// cannot be scanned into a string. GetPostsTagged used to log the scan error,
// break out of the loop and return the posts read so far with a nil error, so
// a tag page silently lost posts.
func TestGetPostsTaggedReturnsScanError(t *testing.T) {
	ds := openPostsTaggedTestDB(t, "sqlite3_with_regex",
		"INSERT INTO posts (id, slug, owner_id, collection_id, created, updated, content) VALUES ('p1', 'p1', 1, 1, '2020-01-02 00:00:00', '2020-01-02 00:00:00', 'about #go today')",
		"INSERT INTO posts (id, slug, owner_id, collection_id, created, updated, content) VALUES (NULL, 'p2', 1, 1, '2020-01-01 00:00:00', '2020-01-01 00:00:00', 'more #go')",
	)

	posts, err := ds.GetPostsTagged(config.New(), &Collection{ID: 1}, "go", 1, true)
	if err == nil {
		t.Fatalf("GetPostsTagged returned %d posts with a nil error; want the scan error", len(*posts))
	}
	if posts != nil {
		t.Errorf("GetPostsTagged returned %d partial posts alongside the error; want nil", len(*posts))
	}
}

// TestGetPostsTaggedReturnsIterationError makes the tag query fail while its
// rows are being read: SQLite only calls regexp() as it steps through rows,
// so an error from it surfaces through rows.Err(). GetPostsTagged used to log
// that and return an empty list with a nil error.
func TestGetPostsTaggedReturnsIterationError(t *testing.T) {
	registerFailingRegexDriver.Do(func() {
		sql.Register("sqlite3_with_failing_regex", &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				return conn.RegisterFunc("regexp", func(re, s string) (bool, error) {
					return false, errors.New("regexp failed")
				}, true)
			},
		})
	})
	ds := openPostsTaggedTestDB(t, "sqlite3_with_failing_regex",
		"INSERT INTO posts (id, slug, owner_id, collection_id, created, updated, content) VALUES ('p1', 'p1', 1, 1, '2020-01-01 00:00:00', '2020-01-01 00:00:00', 'about #go today')",
	)

	posts, err := ds.GetPostsTagged(config.New(), &Collection{ID: 1}, "go", 1, true)
	if err == nil {
		t.Fatalf("GetPostsTagged returned %d posts with a nil error; want the iteration error", len(*posts))
	}
}
