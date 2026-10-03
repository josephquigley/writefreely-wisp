//go:build sqlite

package writefreely

import (
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newTokenTestDB opens a throwaway SQLite database holding only the two
// tables the access-token lookups read, with one user and one token.
func newTokenTestDB(t *testing.T, victimToken []byte) *datastore {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "tokens.db")+"?parseTime=true")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, stmt := range []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT NOT NULL)",
		"CREATE TABLE accesstokens (token TEXT NOT NULL PRIMARY KEY, user_id INTEGER NOT NULL, sudo INTEGER NOT NULL DEFAULT '0', one_time INTEGER NOT NULL DEFAULT '0', created DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, expires DATETIME DEFAULT NULL, user_agent TEXT DEFAULT NULL)",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	if _, err := db.Exec("INSERT INTO users (id, username) VALUES (1, 'victim')"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec("INSERT INTO accesstokens (token, user_id) VALUES (?, 1)", victimToken); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	return &datastore{DB: db, driverName: driverSQLite}
}

// TestAccessTokenLookupIsExact checks that a token whose bytes happen to
// contain a LIKE wildcard does not match a different stored token.
func TestAccessTokenLookupIsExact(t *testing.T) {
	victim := []byte("ABCDEFGHIJKLMNOP")
	// Same first 15 bytes as the victim's token, last byte '%'.
	other := append(append([]byte{}, victim[:15]...), '%')
	otherHex := hex.EncodeToString(other)
	victimHex := hex.EncodeToString(victim)

	db := newTokenTestDB(t, victim)

	if id, _ := db.GetUserIDPrivilege(otherHex); id != -1 {
		t.Errorf("GetUserIDPrivilege with a different token containing %%: got user %d, want -1", id)
	}
	if name, err := db.GetUserNameFromToken(otherHex); err == nil {
		t.Errorf("GetUserNameFromToken with a different token containing %%: got %q, want an error", name)
	}
	if id, _, err := db.GetUserDataFromToken(otherHex); err == nil {
		t.Errorf("GetUserDataFromToken with a different token containing %%: got user %d, want an error", id)
	}
	if err := db.DeleteToken(other); err == nil {
		t.Errorf("DeleteToken with a different token containing %%: deleted a row, want none")
	}

	// The real token still works, and was not deleted above.
	if id, _ := db.GetUserIDPrivilege(victimHex); id != 1 {
		t.Errorf("GetUserIDPrivilege with the real token: got user %d, want 1", id)
	}
	if name, err := db.GetUserNameFromToken(victimHex); err != nil || name != "victim" {
		t.Errorf("GetUserNameFromToken with the real token: got %q, %v", name, err)
	}
	if id, name, err := db.GetUserDataFromToken(victimHex); err != nil || id != 1 || name != "victim" {
		t.Errorf("GetUserDataFromToken with the real token: got %d, %q, %v", id, name, err)
	}
	if err := db.DeleteToken(victim); err != nil {
		t.Errorf("DeleteToken with the real token: %v", err)
	}
}
