/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

// Postgres tests for the call sites that branch on a database error code
// (WFPG-07). Each one checks that a duplicate, or a high-load refusal, takes
// the same path on Postgres that it takes on MySQL.
//
// They run only with WF_TEST_DB_TYPE=postgres and WF_TEST_PG_DSN set, which
// `make test-postgres` does. Until the schema port lands, each test creates
// the few tables it needs by hand, with the unique constraints the real
// schema has, in a database of its own that is dropped afterwards.

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writeas/impart"
	"github.com/writeas/web-core/id"
	"github.com/writefreely/writefreely/config"
)

// errCodeTables mirrors the parts of the schema these tests reach: every
// column the code under test reads or writes, and every unique constraint
// that can turn an INSERT or UPDATE into a duplicate.
var errCodeTables = []string{
	`CREATE TABLE users (
		id       SERIAL PRIMARY KEY,
		username VARCHAR(100) NOT NULL UNIQUE,
		password CHAR(60) NOT NULL,
		email    BYTEA NULL,
		created  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE collections (
		id             SERIAL PRIMARY KEY,
		alias          VARCHAR(100) NULL UNIQUE,
		title          VARCHAR(255) NOT NULL,
		description    VARCHAR(160) NOT NULL,
		style_sheet    TEXT NULL,
		script         TEXT NULL,
		post_signature TEXT NULL,
		format         VARCHAR(8) NULL,
		privacy        SMALLINT NOT NULL,
		owner_id       INT NOT NULL,
		view_count     INT NOT NULL
	)`,
	`CREATE TABLE collectionredirects (
		prev_alias VARCHAR(100) NOT NULL PRIMARY KEY,
		new_alias  VARCHAR(100) NOT NULL
	)`,
	`CREATE TABLE posts (
		id              CHAR(16) NOT NULL PRIMARY KEY,
		slug            VARCHAR(100) NULL,
		modify_token    CHAR(32) NULL,
		text_appearance CHAR(4) NOT NULL DEFAULT 'norm',
		language        CHAR(2) NULL,
		rtl             BOOLEAN NULL,
		privacy         SMALLINT NOT NULL,
		owner_id        INT NULL,
		collection_id   INT NULL,
		pinned_position SMALLINT NULL,
		created         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		view_count      INT NOT NULL,
		title           VARCHAR(160) NOT NULL,
		content         TEXT NOT NULL,
		CONSTRAINT id_slug UNIQUE (collection_id, slug),
		CONSTRAINT owner_id UNIQUE (owner_id, id)
	)`,
	`CREATE TABLE emailsubscribers (
		id            CHAR(8) NOT NULL PRIMARY KEY,
		collection_id INT NOT NULL,
		user_id       INT NULL,
		email         VARCHAR(255) NULL,
		subscribed    TIMESTAMP NOT NULL,
		token         CHAR(16) NOT NULL,
		confirmed     BOOLEAN NOT NULL DEFAULT FALSE,
		allow_export  BOOLEAN NOT NULL DEFAULT FALSE,
		CONSTRAINT eu_coll_email UNIQUE (collection_id, email),
		CONSTRAINT eu_coll_user UNIQUE (collection_id, user_id)
	)`,
	`CREATE TABLE post_images (
		id       VARCHAR(6) NOT NULL PRIMARY KEY,
		owner_id INT NOT NULL,
		post_id  CHAR(16) NULL,
		sha256   CHAR(64) NOT NULL,
		path     VARCHAR(255) NOT NULL,
		filename VARCHAR(255) NOT NULL,
		mime     VARCHAR(64) NOT NULL,
		size     INT NOT NULL,
		created  TIMESTAMP NOT NULL,
		CONSTRAINT pi_owner_sum UNIQUE (owner_id, sha256),
		CONSTRAINT pi_path UNIQUE (path)
	)`,
}

// errCodePostgres returns the maintenance DSN when the Postgres suite is
// selected, and skips the test otherwise.
func errCodePostgres(t *testing.T) string {
	t.Helper()
	if os.Getenv("WF_TEST_DB_TYPE") != "postgres" {
		t.Skip("WF_TEST_DB_TYPE is not postgres")
	}
	dsn := os.Getenv("WF_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("WF_TEST_PG_DSN not set")
	}
	return dsn
}

// errCodeDSN returns dsn with its database (and, if user is non-nil, its
// credentials) replaced.
func errCodeDSN(t *testing.T, dsn, dbName string, user *url.Userinfo) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	u.Path = "/" + dbName
	if user != nil {
		u.User = user
	}
	return u.String()
}

// newErrCodeDB creates a fresh database with errCodeTables in it, and drops
// it when the test ends. It returns the datastore and the database's name.
func newErrCodeDB(t *testing.T) (*datastore, string) {
	t.Helper()
	dsn := errCodePostgres(t)

	admin, err := sql.Open(driverPostgresRebind, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { admin.Close() })

	name := strings.ToLower("wfpg07_" + id.GenerateRandomString("abcdefghijklmnopqrstuvwxyz0123456789", 10))
	_, err = admin.Exec("CREATE DATABASE " + name)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Logf("dropping %s: %v", name, err)
		}
	})

	sdb, err := sql.Open(driverPostgresRebind, errCodeDSN(t, dsn, name, nil))
	require.NoError(t, err)
	t.Cleanup(func() { sdb.Close() })
	db := newDatastore(sdb, driverPostgres)

	for _, ddl := range errCodeTables {
		_, err = db.Exec(ddl)
		require.NoError(t, err, ddl)
	}
	return db, name
}

func errCodeStatus(t *testing.T, err error) int {
	t.Helper()
	require.Error(t, err)
	herr, ok := err.(impart.HTTPError)
	require.Truef(t, ok, "want impart.HTTPError, got %T: %v", err, err)
	return herr.Status
}

func errCodeInsertUser(t *testing.T, db *datastore, username string) int64 {
	t.Helper()
	var uid int64
	require.NoError(t, db.QueryRow("INSERT INTO users (username, password) VALUES (?, ?) RETURNING id", username, strings.Repeat("x", 60)).Scan(&uid))
	return uid
}

func errCodeInsertCollection(t *testing.T, db *datastore, alias string, ownerID int64) int64 {
	t.Helper()
	var cid int64
	require.NoError(t, db.QueryRow("INSERT INTO collections (alias, title, description, privacy, owner_id, view_count) VALUES (?, ?, '', 1, ?, 0) RETURNING id", alias, alias, ownerID).Scan(&cid))
	return cid
}

func TestPostgresCreateUserDuplicate(t *testing.T) {
	db, _ := newErrCodeDB(t)
	cfg := config.New()

	t.Run("username taken", func(t *testing.T) {
		errCodeInsertUser(t, db, "taken")
		u := &User{Username: "taken", HashedPass: []byte(strings.Repeat("y", 60))}
		err := db.CreateUser(cfg, u, "", "")
		assert.Equal(t, http.StatusConflict, errCodeStatus(t, err))
		assert.Equal(t, "Username is already taken.", err.(impart.HTTPError).Message)
	})

	t.Run("collection alias taken", func(t *testing.T) {
		// The username is free but a blog already has the alias, so the
		// second INSERT in the transaction is the duplicate.
		owner := errCodeInsertUser(t, db, "someoneelse")
		errCodeInsertCollection(t, db, "aliastaken", owner)
		u := &User{Username: "aliastaken", HashedPass: []byte(strings.Repeat("y", 60))}
		err := db.CreateUser(cfg, u, "", "")
		if err != nil && strings.Contains(err.Error(), "LastInsertId") {
			// CreateUser reads the new user's ID with LastInsertId between
			// the two INSERTs, which pgx does not support. That is a
			// separate port (InsertReturningID); this path is reachable on
			// Postgres only once it lands.
			t.Skipf("CreateUser still uses LastInsertId on Postgres: %v", err)
		}
		assert.Equal(t, http.StatusConflict, errCodeStatus(t, err))
		assert.Equal(t, "Username is already taken.", err.(impart.HTTPError).Message)

		// The transaction rolled back: no orphan users row.
		var n int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", "aliastaken").Scan(&n))
		assert.Equal(t, 0, n)
	})
}

func TestPostgresCreateCollectionDuplicate(t *testing.T) {
	db, _ := newErrCodeDB(t)
	owner := errCodeInsertUser(t, db, "owner")
	errCodeInsertCollection(t, db, "blog", owner)

	_, err := db.CreateCollection(config.New(), "blog", "Blog", owner)
	assert.Equal(t, http.StatusConflict, errCodeStatus(t, err))
	assert.Equal(t, "Collection already exists.", err.(impart.HTTPError).Message)
}

func TestPostgresCreatePostDuplicateSlug(t *testing.T) {
	if n := unportedDriverSites["database.go:datastore.CreatePost"]; n > 0 {
		t.Skip("CreatePost still panics on Postgres in its created-time branches (WFPG-06)")
	}
	db, _ := newErrCodeDB(t)
	owner := errCodeInsertUser(t, db, "writer")
	coll := errCodeInsertCollection(t, db, "writer", owner)

	title, content := "Hello world", "First."
	first, err := db.CreatePost(owner, coll, &SubmittedPost{Title: &title, Content: &content})
	require.NoError(t, err)
	require.Equal(t, "hello-world", first.Slug.String)

	// Same title, same blog: the slug collides, CreatePost regenerates it
	// and retries once. On Postgres this used to be a 500.
	second, err := db.CreatePost(owner, coll, &SubmittedPost{Title: &title, Content: &content})
	require.NoError(t, err)
	assert.NotEqual(t, first.Slug.String, second.Slug.String)
	assert.True(t, strings.HasPrefix(second.Slug.String, "hello-world-"), second.Slug.String)

	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM posts WHERE collection_id = ?", coll).Scan(&n))
	assert.Equal(t, 2, n)
}

func TestPostgresAttemptClaimDuplicateSlug(t *testing.T) {
	// AttemptClaim runs each attempt as its own statement, not inside a
	// transaction, so a duplicate does not poison the retry on Postgres and
	// no SAVEPOINT is needed.
	db, _ := newErrCodeDB(t)
	owner := errCodeInsertUser(t, db, "claimer")
	coll := errCodeInsertCollection(t, db, "claimer", owner)

	insertPost := func(postID string, slug, collID interface{}) {
		_, err := db.Exec("INSERT INTO posts (id, slug, privacy, owner_id, collection_id, view_count, title, content) VALUES (?, ?, 0, ?, ?, 0, '', 'x')", postID, slug, owner, collID)
		require.NoError(t, err)
	}
	insertPost("existingpost0000", "my-post", coll)
	insertPost("loosepost0000000", nil, nil)

	// The query and parameters DispersePosts/ClaimPosts build when the user
	// already owns the post.
	p := &ClaimPostRequest{AnonymousAuthPost: &AnonymousAuthPost{ID: "loosepost0000000"}, Slug: "my-post"}
	query := "UPDATE posts SET collection_id = ?, slug = ? WHERE id = ? AND owner_id = ?"
	params := []interface{}{coll, p.Slug, p.ID, owner}
	res, err := db.AttemptClaim(p, query, params, 1)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	assert.NotEqual(t, "my-post", p.Slug)
	assert.True(t, strings.HasPrefix(p.Slug, "my-post-"), p.Slug)

	var slug string
	require.NoError(t, db.QueryRow("SELECT slug FROM posts WHERE id = ?", p.ID).Scan(&slug))
	assert.Equal(t, p.Slug, slug)
}

func TestPostgresGetCollectionByHighLoad(t *testing.T) {
	// A real 53300: a role limited to one connection, with that one
	// connection held open, so the next connect is refused by the server.
	db, name := newErrCodeDB(t)
	dsn := errCodePostgres(t)

	role := name + "_limited"
	pass := id.GenerateRandomString("abcdefghijklmnopqrstuvwxyz0123456789", 16)
	_, err := db.Exec("CREATE ROLE " + role + " LOGIN PASSWORD '" + pass + "' CONNECTION LIMIT 1")
	require.NoError(t, err)
	t.Cleanup(func() {
		// Runs before the database is dropped, and after this test's
		// deferred Closes, so the role has no sessions left.
		if _, err := db.Exec("DROP ROLE IF EXISTS " + role); err != nil {
			t.Logf("dropping role %s: %v", role, err)
		}
	})

	limitedDSN := errCodeDSN(t, dsn, name, url.UserPassword(role, pass))

	holder, err := sql.Open(driverPostgresRebind, limitedDSN)
	require.NoError(t, err)
	defer holder.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	held, err := holder.Conn(ctx)
	require.NoError(t, err)
	defer held.Close()

	sdb, err := sql.Open(driverPostgresRebind, limitedDSN)
	require.NoError(t, err)
	defer sdb.Close()
	limited := newDatastore(sdb, driverPostgres)

	c, err := limited.GetCollectionBy("alias = ?", "anything")
	assert.Nil(t, c)
	require.Error(t, err)
	assert.Equal(t, ErrUnavailable, err)
	assert.Equal(t, http.StatusServiceUnavailable, errCodeStatus(t, err))
}

func TestPostgresAddEmailSubscriptionDuplicate(t *testing.T) {
	db, _ := newErrCodeDB(t)
	owner := errCodeInsertUser(t, db, "newsletter")
	coll := errCodeInsertCollection(t, db, "newsletter", owner)

	t.Run("by email", func(t *testing.T) {
		first, err := db.AddEmailSubscription(coll, 0, "reader@example.com", false)
		require.NoError(t, err)
		// A re-subscribe returns the existing subscriber instead of a 500.
		again, err := db.AddEmailSubscription(coll, 0, "reader@example.com", false)
		require.NoError(t, err)
		require.NotNil(t, again)
		assert.Equal(t, first.ID, again.ID)
		assert.Equal(t, first.Token, again.Token)
	})

	t.Run("by user", func(t *testing.T) {
		reader := errCodeInsertUser(t, db, "reader")
		first, err := db.AddEmailSubscription(coll, reader, "", true)
		require.NoError(t, err)
		again, err := db.AddEmailSubscription(coll, reader, "", true)
		require.NoError(t, err)
		require.NotNil(t, again)
		assert.Equal(t, first.ID, again.ID)
	})
}

func TestPostgresCreatePostImageDuplicate(t *testing.T) {
	db, _ := newErrCodeDB(t)
	app := &App{db: db}
	owner := errCodeInsertUser(t, db, "photographer")
	sum := func(c byte) string { return strings.Repeat(string(c), 64) }
	day := time.Now()

	// The same bytes again is a successful duplicate upload: the existing row
	// comes back.
	first, err := app.createImageRow(owner, sum('a'), "cat.png", "image/png", "png", 10)
	require.NoError(t, err)
	again, err := app.createImageRow(owner, sum('a'), "cat.png", "image/png", "png", 10)
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)

	// Different bytes under a name already used today: the path collides,
	// CreatePostImage reports ErrImagePathTaken, and createImageRow moves on
	// to the next name. On Postgres this used to be a 500.
	_, err = db.CreatePostImage(owner, sum('b'), imagePath("cat.png", "png", day, 1), "cat.png", "image/png", 10)
	assert.Equal(t, ErrImagePathTaken, err)

	second, err := app.createImageRow(owner, sum('b'), "cat.png", "image/png", "png", 10)
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
	assert.NotEqual(t, first.Path, second.Path)
	assert.Equal(t, imagePath("cat.png", "png", day, 2), second.Path)
}
