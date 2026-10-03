package writefreely

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestClipSQLite runs the expression clip() renders for SQLite against a real
// SQLite engine. SQLite's SUBSTR is 1-indexed, so a start of 0 returns one
// character fewer than asked for.
func TestClipSQLite(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	ds := &datastore{DB: db, driverName: driverSQLite}

	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"abcdef", 3, "abc"},
		{"abcdef", 1, "a"},
		{"abcdef", 6, "abcdef"},
		{"abc", 80, "abc"},
		{"héllo", 2, "hé"},
	}
	for _, tt := range tests {
		var got string
		q := "SELECT " + ds.clip("?", tt.n)
		if err := db.QueryRow(q, tt.in).Scan(&got); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if got != tt.want {
			t.Errorf("clip(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
