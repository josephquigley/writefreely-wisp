/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/migrations"
)

// V20 (migrations/v20.go) on MySQL. The behaviour it buys is pinned by the
// TestExactMatch* tests in case_normalisation_test.go, which run on every
// engine; these tests pin the schema itself, and that an existing database
// comes through the migration with its rows intact.

// exactMatchColumns is every column V20 gives a binary collation, with the
// collation it should end up with.
var exactMatchColumns = []struct{ table, column, collation string }{
	{"userinvites", "id", "latin1_bin"},
	{"usersinvited", "invite_id", "latin1_bin"},
	{"posts", "modify_token", "latin1_bin"},
	{"password_resets", "token", "utf8mb4_bin"},
	{"emailsubscribers", "id", "utf8mb4_bin"},
	{"emailsubscribers", "token", "utf8mb4_bin"},
	{"oauth_client_states", "state", "utf8mb4_bin"},
	{"oauth_client_states", "invite_code", "utf8mb4_bin"},
	{"oauth_users", "remote_user_id", "utf8mb4_bin"},
	{"post_images", "id", "utf8mb4_bin"},
	{"remoteusers", "actor_id", "utf8mb4_bin"},
	{"remoteusers", "inbox", "utf8mb4_bin"},
	{"remoteusers", "shared_inbox", "utf8mb4_bin"},
	{"remoteusers", "url", "utf8mb4_bin"},
	{"remoteusers", "handle", "utf8mb4_bin"},
	{"remoteuserkeys", "id", "utf8mb4_bin"},
}

func mysqlColumnCollation(t *testing.T, app *App, table, column string) string {
	t.Helper()
	var c string
	require.NoError(t, app.db.QueryRow("SELECT COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?", table, column).Scan(&c), "%s.%s", table, column)
	return c
}

// wideKeyTables are the tables V20 gives a 1020-byte utf8mb4 key
// (oauth_client_states.state, remoteuserkeys.id, remoteusers.actor_id).
var wideKeyTables = []string{"oauth_client_states", "remoteusers", "remoteuserkeys"}

func mysqlRowFormat(t *testing.T, app *App, table string) string {
	t.Helper()
	var f string
	require.NoError(t, app.db.QueryRow("SELECT ROW_FORMAT FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&f), table)
	return f
}

func TestMySQLExactMatchCollations(t *testing.T) {
	app := newMySQLTestApp(t, nil)
	for _, c := range exactMatchColumns {
		assert.Equal(t, c.collation, mysqlColumnCollation(t, app, c.table, c.column), "%s.%s", c.table, c.column)
	}
	// V21's tables hold any Unicode and are keyed by exact name.
	assert.Equal(t, "utf8mb4_bin", mysqlColumnCollation(t, app, "app_settings", "name"))
	assert.Equal(t, "utf8mb4_bin", mysqlColumnCollation(t, app, "app_settings", "value"))
	// Meant to ignore case, and left alone (see V19).
	assert.Equal(t, "utf8mb4_unicode_ci", mysqlColumnCollation(t, app, "emailsubscribers", "email"))
}

// TestMySQLExactMatchCollationsUpgrade puts the V19 definitions back, fills
// the tables, and runs V20 over them as an upgrade would.
func TestMySQLExactMatchCollationsUpgrade(t *testing.T) {
	app := newMySQLTestApp(t, nil)

	// The V19 schema on a fresh MySQL, as information_schema reports it.
	for _, q := range []string{
		`ALTER TABLE userinvites MODIFY id CHAR(6) CHARACTER SET latin1 COLLATE latin1_swedish_ci NOT NULL`,
		`ALTER TABLE usersinvited MODIFY invite_id CHAR(6) CHARACTER SET latin1 COLLATE latin1_swedish_ci NOT NULL`,
		`ALTER TABLE posts MODIFY modify_token CHAR(32) CHARACTER SET latin1 COLLATE latin1_swedish_ci NULL DEFAULT NULL`,
		`ALTER TABLE password_resets MODIFY token CHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL`,
		`ALTER TABLE emailsubscribers MODIFY id CHAR(8) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL, MODIFY token CHAR(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL`,
		`ALTER TABLE oauth_client_states MODIFY state VARCHAR(255) CHARACTER SET latin1 COLLATE latin1_swedish_ci NOT NULL, MODIFY invite_code CHAR(6) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NULL DEFAULT NULL`,
		`ALTER TABLE oauth_users MODIFY remote_user_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL`,
		`ALTER TABLE post_images MODIFY id VARCHAR(6) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL`,
		`ALTER TABLE remoteusers CONVERT TO CHARACTER SET latin1 COLLATE latin1_swedish_ci`,
		`ALTER TABLE remoteuserkeys CONVERT TO CHARACTER SET latin1 COLLATE latin1_swedish_ci`,
		// What an older install looks like: latin1 keys of 255 bytes in
		// tables whose row format caps an index prefix at 767 bytes. Widened
		// to utf8mb4 they are 1020 bytes, which only DYNAMIC can hold.
		`ALTER TABLE oauth_client_states ROW_FORMAT=COMPACT`,
		`ALTER TABLE remoteusers ROW_FORMAT=COMPACT`,
		`ALTER TABLE remoteuserkeys ROW_FORMAT=COMPACT`,
		// V21 (settings tables) is undone too, so Migrate runs V20 and then
		// V21 again, as it would on a V19 database.
		`DROP TABLE app_settings`,
		`DROP TABLE app_settings_version`,
		`DELETE FROM appmigrations WHERE version >= 20`,
	} {
		_, err := app.db.Exec(q)
		require.NoError(t, err, q)
	}
	require.Equal(t, "latin1_swedish_ci", mysqlColumnCollation(t, app, "remoteusers", "actor_id"))
	for _, table := range wideKeyTables {
		require.Equal(t, "Compact", mysqlRowFormat(t, app, table), table)
	}

	owner := caseInsertUser(t, app, "upgrader")
	_, err := app.db.Exec("INSERT INTO userinvites (id, owner_id, max_uses, created, expires, inactive) VALUES ('BcDfGh', ?, 0, CURRENT_TIMESTAMP, NULL, FALSE)", owner)
	require.NoError(t, err)
	// é is in latin1, so a V19 database can hold it; it must survive the
	// conversion to utf8mb4 unchanged.
	const profile = "https://social.example/@Café"
	_, err = app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, url, handle) VALUES (?, ?, ?, ?, ?)",
		"https://social.example/users/MixedCase", "https://social.example/users/MixedCase/inbox", "https://social.example/inbox", profile, "mixedcase@social.example")
	require.NoError(t, err)
	_, err = app.db.Exec("INSERT INTO remoteuserkeys (id, remote_user_id, public_key) VALUES (?, LAST_INSERT_ID(), ?)", "https://social.example/users/MixedCase#main-key", []byte("key"))
	require.NoError(t, err)

	require.NoError(t, migrations.Migrate(migrations.NewDatastore(app.db.DB, driverMySQL)))

	for _, c := range exactMatchColumns {
		assert.Equal(t, c.collation, mysqlColumnCollation(t, app, c.table, c.column), "%s.%s", c.table, c.column)
	}

	// The three tables that carry a 1020-byte key are DYNAMIC now.
	for _, table := range wideKeyTables {
		assert.Equal(t, "Dynamic", mysqlRowFormat(t, app, table), table)
	}

	// Rows survive, and now match only in their own case.
	i, err := app.db.GetUserInvite("BcDfGh")
	require.NoError(t, err)
	assert.Equal(t, "BcDfGh", i.ID)
	_, err = app.db.GetUserInvite("bcdfgh")
	assert.Error(t, err)

	ru, err := getRemoteUser(app, "https://social.example/users/MixedCase")
	require.NoError(t, err)
	assert.Equal(t, profile, ru.URL)
	_, err = getRemoteUser(app, "https://social.example/users/mixedcase")
	assert.Error(t, err)

	var keyID string
	require.NoError(t, app.db.QueryRow("SELECT id FROM remoteuserkeys WHERE id = ?", "https://social.example/users/MixedCase#main-key").Scan(&keyID))

	// remoteusers now takes characters outside latin1, and a second actor
	// whose IRI differs only in case.
	_, err = app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, url) VALUES (?, ?, ?, ?)",
		"https://social.example/users/mixedcase", "https://social.example/users/mixedcase/inbox", "https://social.example/sh�ared", "https://social.example/@日本")
	require.NoError(t, err)
	var n int
	require.NoError(t, app.db.QueryRow("SELECT COUNT(*) FROM remoteusers WHERE LOWER(actor_id) = ?", strings.ToLower("https://social.example/users/MixedCase")).Scan(&n))
	assert.Equal(t, 2, n)

	// Running it again is harmless: a migration interrupted part way is
	// finished by running it again.
	for _, q := range []string{"DROP TABLE app_settings", "DROP TABLE app_settings_version", "DELETE FROM appmigrations WHERE version >= 20"} {
		_, err = app.db.Exec(q)
		require.NoError(t, err, q)
	}
	require.NoError(t, migrations.Migrate(migrations.NewDatastore(app.db.DB, driverMySQL)))
	for _, table := range wideKeyTables {
		assert.Equal(t, "Dynamic", mysqlRowFormat(t, app, table), table)
	}
	for _, c := range exactMatchColumns {
		assert.Equal(t, c.collation, mysqlColumnCollation(t, app, c.table, c.column), "%s.%s", c.table, c.column)
	}
}
