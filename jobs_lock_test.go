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
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/writefreely/writefreely/config"
)

// newJobsTestApp returns an App over a schema-loaded database on the engine
// WF_TEST_DB_TYPE selects (SQLite by default), and that database's pool.
func newJobsTestApp(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	cfg := config.New()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	cfg.App.SingleUser = false
	cfg.App.Federation = false
	app := &App{cfg: cfg}
	db := openAppTestDB(t, app, "sqlite3", dbPath+"?parseTime=true&cached=shared")
	return app, db
}

// secondProcessDB opens a second, independent pool on app's MySQL or
// Postgres database, standing in for another app process. It is closed
// when the test ends, before the harness drops the database.
func secondProcessDB(t *testing.T, app *App) *sql.DB {
	t.Helper()
	var (
		db  *sql.DB
		err error
	)
	switch app.db.driverName {
	case driverPostgres:
		var name, dsn string
		if err = app.db.QueryRow("SELECT current_database()").Scan(&name); err != nil {
			t.Fatalf("current_database: %v", err)
		}
		if dsn, err = testPGDSN(os.Getenv(envTestPGDSN), name); err != nil {
			t.Fatal(err)
		}
		db, err = sql.Open(driverPostgresRebind, dsn)
	case driverMySQL:
		var name string
		if err = app.db.QueryRow("SELECT DATABASE()").Scan(&name); err != nil {
			t.Fatalf("DATABASE(): %v", err)
		}
		c, cerr := testMySQLConfig(name)
		if cerr != nil {
			t.Fatal(cerr)
		}
		db, err = sql.Open("mysql", c.FormatDSN())
	default:
		t.Fatalf("secondProcessDB: no second process on %s", app.db.driverName)
	}
	if err != nil {
		t.Fatalf("open second pool: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("connect second pool: %v", err)
	}
	return db
}

// seedDueEmailJob creates a user, a collection and a post created a minute
// ago, with an email publish job for it that is due now.
func seedDueEmailJob(t *testing.T, app *App, db *sql.DB) {
	t.Helper()
	u := &User{Username: "alice", HashedPass: []byte("x")}
	if err := app.db.CreateUser(app.cfg, u, "", ""); err != nil {
		t.Fatalf("create user: %v", err)
	}
	coll, err := app.db.CreateCollection(app.cfg, "alice-blog", "alice-blog", u.ID)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	created := app.db.dialectOrDefault().DateSub(1, "MINUTE")
	_, err = db.Exec(`INSERT INTO posts
(id, slug, text_appearance, language, rtl, privacy, owner_id, collection_id, created, updated, view_count, title, content)
VALUES ('p1', 'p1', 'norm', 'en', ?, 0, ?, ?, `+created+`, `+created+`, 0, 'T', 'B')`, false, u.ID, coll.ID)
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}
	if err := app.db.InsertJob(&PostJob{PostID: "p1", Action: "email", Delay: 0}); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	jobs, err := app.db.GetJobsToRun("email")
	if err != nil {
		t.Fatalf("get jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("fixture: %d due jobs, want 1", len(jobs))
	}
}

func countPublishJobs(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM publishjobs").Scan(&n); err != nil {
		t.Fatalf("count publishjobs: %v", err)
	}
	return n
}

// TestPublishJobRunsOnceAcrossProcesses runs the publish queue in two
// "processes" (separate pools) against one database holding one due job.
// The first is held inside its send, before it deletes the job, while the
// second ticks: without the lock the second would select the same job and
// send it again. On SQLite there is no second process, and the test checks
// that the queue still sends once and then finds nothing.
func TestPublishJobRunsOnceAcrossProcesses(t *testing.T) {
	appA, db := newJobsTestApp(t)
	seedDueEmailJob(t, appA, db)

	var (
		mu    sync.Mutex
		sends int
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	orig := jobEmailPost
	t.Cleanup(func() { jobEmailPost = orig })
	jobEmailPost = func(app *App, p *PublicPost, collID int64) error {
		mu.Lock()
		sends++
		first := sends == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
		return nil
	}
	tick := func(app *App) {
		withJobLock(app, jobLockPublish, func() { runPublishJobs(app) })
	}

	if appA.db.driverName == driverSQLite {
		close(release)
		tick(appA)
		tick(appA)
	} else {
		appB := &App{cfg: appA.cfg, db: newDatastore(secondProcessDB(t, appA), appA.db.driverName)}
		done := make(chan struct{})
		go func() {
			defer close(done)
			tick(appA)
		}()
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("process A never reached its send")
		}
		tick(appB)
		close(release)
		<-done
	}

	if sends != 1 {
		t.Errorf("post sent %d times, want 1", sends)
	}
	if n := countPublishJobs(t, db); n != 0 {
		t.Errorf("%d publish jobs left, want 0", n)
	}
}

// TestTryJobLock checks the lock itself: a second process cannot take a
// held lock, can once it is released, and locks with different names do
// not exclude each other. SQLite has no second process; its lock always
// succeeds, including while "held".
func TestTryJobLock(t *testing.T) {
	app, db := newJobsTestApp(t)
	d := app.db.dialectOrDefault()
	ctx := context.Background()

	unlockA, ok, err := d.TryJobLock(ctx, db, jobLockPublish)
	if err != nil || !ok {
		t.Fatalf("A: ok=%v err=%v, want the lock", ok, err)
	}

	if app.db.driverName == driverSQLite {
		unlock, ok, err := d.TryJobLock(ctx, db, jobLockPublish)
		if err != nil || !ok {
			t.Fatalf("sqlite second take: ok=%v err=%v, want ok", ok, err)
		}
		unlock()
		unlockA()
		return
	}

	other := secondProcessDB(t, app)
	if _, ok, err := d.TryJobLock(ctx, other, jobLockPublish); err != nil || ok {
		t.Fatalf("B while A holds it: ok=%v err=%v, want not ok and no error", ok, err)
	}
	unlockSweep, ok, err := d.TryJobLock(ctx, other, jobLockOrphanSweep)
	if err != nil || !ok {
		t.Fatalf("B, another name: ok=%v err=%v, want the lock", ok, err)
	}
	unlockSweep()

	unlockA()
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Errorf("unlock left %d connection(s) in use", inUse)
	}
	unlockB, ok, err := d.TryJobLock(ctx, other, jobLockPublish)
	if err != nil || !ok {
		t.Fatalf("B after A released: ok=%v err=%v, want the lock", ok, err)
	}
	unlockB()
}
