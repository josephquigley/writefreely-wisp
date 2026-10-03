//go:build sqlite

package writefreely

import (
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/writeas/web-core/auth"
)

// newTokenTestDB opens a throwaway SQLite database holding only the two
// tables the access-token lookups read, with one user (id 1, "victim") and,
// if victimToken is not nil, one token for that user. The accesstokens
// table matches sqlite.sql, so its token column is TEXT, and the token is
// stored as TEXT the way older code wrote it. Under WF_TEST_DB_TYPE=mysql or
// postgres it is a fresh database on that engine with the real schema
// instead, holding the same user and token.
func newTokenTestDB(t *testing.T, victimToken []byte) *datastore {
	t.Helper()
	if e := engineTestApp(t, nil); e != nil {
		ds := e.db
		if _, err := ds.Exec("INSERT INTO users (id, username, password) VALUES (1, 'victim', 'x')"); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		if victimToken != nil {
			if _, err := ds.Exec("INSERT INTO accesstokens (token, user_id) VALUES (?, 1)", victimToken); err != nil {
				t.Fatalf("insert token: %v", err)
			}
		}
		return ds
	}
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
	if victimToken != nil {
		if _, err := db.Exec("INSERT INTO accesstokens (token, user_id) VALUES (?, 1)", string(victimToken)); err != nil {
			t.Fatalf("insert token: %v", err)
		}
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

// TestAccessTokenRoundTrip checks that a token issued by GetAccessToken is
// found again by every lookup and then deleted. GetAccessToken now writes the
// token as bytes; older SQLite databases hold TEXT tokens instead, which
// TestLegacyTextAccessTokensSQLite covers. On SQLite a TEXT value never
// equals a BLOB, so a mismatch between the write and the lookups would make
// every token unusable.
func TestAccessTokenRoundTrip(t *testing.T) {
	db := newTokenTestDB(t, nil)

	tok, err := db.GetAccessToken(1)
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}
	if id := db.GetUserID(tok); id != 1 {
		t.Errorf("GetUserID: got user %d, want 1", id)
	}
	if name, err := db.GetUserNameFromToken(tok); err != nil || name != "victim" {
		t.Errorf("GetUserNameFromToken: got %q, %v", name, err)
	}
	if id, name, err := db.GetUserDataFromToken(tok); err != nil || id != 1 || name != "victim" {
		t.Errorf("GetUserDataFromToken: got %d, %q, %v", id, name, err)
	}
	if err := db.DeleteToken(auth.GetToken(tok)); err != nil {
		t.Errorf("DeleteToken: %v", err)
	}
	if id := db.GetUserID(tok); id != -1 {
		t.Errorf("GetUserID after DeleteToken: got user %d, want -1", id)
	}
}
