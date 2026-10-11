//go:build sqlite

package writefreely

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/writefreely/writefreely/config"
)

var registerFailingRegexDriver sync.Once

// TestGetAllPostsTaggedIDsReturnsIterationError makes the tag query fail
// while its rows are being read, not when it is prepared: SQLite only calls
// regexp() as it steps through rows, so an error from it surfaces through
// rows.Err(). GetAllPostsTaggedIDs used to log that and return an empty list
// with a nil error, which a tag page renders as "no posts".
func TestGetAllPostsTaggedIDsReturnsIterationError(t *testing.T) {
	registerFailingRegexDriver.Do(func() {
		sql.Register("sqlite3_with_failing_regex", &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				return conn.RegisterFunc("regexp", func(re, s string) (bool, error) {
					return false, errors.New("regexp failed")
				}, true)
			},
		})
	})

	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	db, err := sql.Open("sqlite3_with_failing_regex", dbPath+"?parseTime=true")
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

	_, err = db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, created, updated, view_count, title, content) VALUES ('p1', 'p1', 0, 1, 1, '2020-01-01 00:00:00', '2020-01-01 00:00:00', 0, '', 'about #go today')")
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}

	ids, err := app.db.GetAllPostsTaggedIDs(&Collection{ID: 1}, "go", true)
	if err == nil {
		t.Fatalf("GetAllPostsTaggedIDs returned %v with a nil error; want the iteration error", ids)
	}
}
