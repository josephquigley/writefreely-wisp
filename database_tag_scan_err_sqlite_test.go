//go:build sqlite

package writefreely

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestGetAllPostsTaggedIDsReturnsScanError gives the tag query a row whose
// id cannot be scanned into a string (a NULL, which the real schema forbids,
// so the test uses a minimal posts table that allows it). GetAllPostsTaggedIDs
// used to log the scan error, break out of the loop and return the ids read
// so far with a nil error, so a tag page silently lost posts.
func TestGetAllPostsTaggedIDsReturnsScanError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	db, err := sql.Open("sqlite3_with_regex", dbPath+"?parseTime=true")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, q := range []string{
		"CREATE TABLE posts (id TEXT, collection_id INTEGER, content TEXT, created DATETIME)",
		"INSERT INTO posts VALUES ('p1', 1, 'about #go today', '2020-01-02 00:00:00')",
		"INSERT INTO posts VALUES (NULL, 1, 'more #go', '2020-01-01 00:00:00')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	ds := &datastore{DB: db, driverName: driverSQLite}
	ids, err := ds.GetAllPostsTaggedIDs(&Collection{ID: 1}, "go", true)
	if err == nil {
		t.Fatalf("GetAllPostsTaggedIDs returned %v with a nil error; want the scan error", ids)
	}
	if ids != nil {
		t.Errorf("GetAllPostsTaggedIDs returned partial ids %v alongside the error; want nil", ids)
	}
}
