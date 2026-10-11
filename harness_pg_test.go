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

// The Postgres test harness. docs/postgres-testing.md is the user-facing
// description; this file is the mechanism.
//
// With WF_TEST_DB_TYPE=postgres and WF_TEST_PG_DSN pointing at a maintenance
// database, every test that asks for a Postgres database gets its own,
// named wf_test_<random>, created from the maintenance connection and dropped
// WITH (FORCE) when the test ends. Without those variables every helper here
// skips the calling test.
//
// A schema-loaded database (newPostgresTestApp, newPostgresTestDatastore) is
// a clone of a template database, wf_test_template_<random>, which the first
// such test builds with adminInitDatabase and TestMain drops at the end of
// the run. Cloning takes a few milliseconds where loading the schema and
// running every migration took about 50. TestPostgresTemplateParity checks
// that a clone is the same as a database built from scratch, and a test that
// is about init or migrations itself calls buildPostgresFromScratch to get
// one built the old way.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/config"
)

const (
	envTestDBType = "WF_TEST_DB_TYPE"
	envTestPGDSN  = "WF_TEST_PG_DSN"

	// testPGPrefix names every database the harness creates, so a leak is
	// easy to spot (\l wf_test_*) and to clean up by hand.
	testPGPrefix = "wf_test_"

	// testPGTemplatePrefix names the migrated template the schema-loaded
	// databases are cloned from. It is unique per test binary, so two runs
	// against one server never share or drop each other's, and one a
	// crashed run left behind is never picked up again (it still matches
	// wf_test_*, so the clean-up query in docs/postgres-testing.md finds it).
	testPGTemplatePrefix = testPGPrefix + "template_"
)

// testPGAdmin is the maintenance connection opened by TestMain when
// runPostgresTests() is true. It is used only for CREATE/DROP DATABASE.
var testPGAdmin *sql.DB

// runPostgresTests reports whether the suite was asked to run against
// Postgres.
func runPostgresTests() bool {
	return os.Getenv(envTestDBType) == driverPostgres
}

// initPostgresTests opens and checks the maintenance connection. TestMain
// calls it when runPostgresTests() is true, and exits non-zero on error:
// asking for Postgres and not getting it must not look like a green run.
func initPostgresTests() error {
	dsn := os.Getenv(envTestPGDSN)
	if dsn == "" {
		return fmt.Errorf("%s=postgres but %s is not set", envTestDBType, envTestPGDSN)
	}
	if _, err := testPGDSN(dsn, "x"); err != nil {
		return err
	}
	db, err := sql.Open(driverPostgresRebind, dsn)
	if err != nil {
		return fmt.Errorf("open %s: %v", envTestPGDSN, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return fmt.Errorf("connect to %s: %v", envTestPGDSN, err)
	}
	db.SetMaxOpenConns(4)
	// STRATEGY arrived in Postgres 15. Older servers are not tested
	// (docs/postgres-testing.md), but on one the clone still works, as a
	// plain TEMPLATE copy.
	var ver int
	if err := db.QueryRow("SELECT current_setting('server_version_num')::int").Scan(&ver); err != nil {
		db.Close()
		return fmt.Errorf("server version: %v", err)
	}
	if ver < 150000 {
		testPGCloneStrategy = ""
	}
	testPGAdmin = db
	return nil
}

// testPGDSN returns base with its database replaced by dbName and the
// session time zone pinned to UTC, matching
// postgresDSN. Only URL-form DSNs (postgres://…) are accepted.
func testPGDSN(base, dbName string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("%s must be a postgres:// URL", envTestPGDSN)
	}
	u.Path = "/" + dbName
	u.RawPath = ""
	q := u.Query()
	// Always UTC, as postgresDSN does: lib/pq scans timestamptz in the
	// session zone, so a DSN naming another zone would test something the
	// application never runs.
	q.Set("timezone", "UTC")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// randomPGName returns prefix followed by 16 random hex digits.
func randomPGName(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random database name: %v", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

// openPostgresTestPool opens name through the postgres-rebind driver, with
// the session zone pinned to UTC (testPGDSN), and checks it answers.
func openPostgresTestPool(name string) (*sql.DB, error) {
	dsn, err := testPGDSN(os.Getenv(envTestPGDSN), name)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverPostgresRebind, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %v", name, err)
	}
	db.SetMaxOpenConns(10)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to %s: %v", name, err)
	}
	return db, nil
}

// newPostgresTestDB creates an empty database named wf_test_<random>, opens
// it through the postgres-rebind driver, and registers a cleanup that closes
// the pool and drops the database WITH (FORCE), so a leaked connection cannot
// wedge the drop. It skips the test unless WF_TEST_DB_TYPE=postgres.
//
// Use it for tests that build their own tables. Most tests want
// newPostgresTestApp or newPostgresTestDatastore, which also load the schema.
func newPostgresTestDB(t testing.TB) *sql.DB {
	t.Helper()
	return createPostgresTestDB(t, "")
}

// createPostgresTestDB is newPostgresTestDB, with the new database cloned
// from template when that is not "" (see postgresTestTemplate), or created
// empty from template1, as CREATE DATABASE does by default, when it is.
func createPostgresTestDB(t testing.TB, template string) *sql.DB {
	t.Helper()
	if !runPostgresTests() {
		t.Skipf("skipping postgres test: %s is not %q", envTestDBType, driverPostgres)
	}
	if testPGAdmin == nil {
		t.Fatalf("postgres harness not initialised (TestMain did not run initPostgresTests)")
	}

	name, err := randomPGName(testPGPrefix)
	if err != nil {
		t.Fatal(err)
	}
	q := "CREATE DATABASE " + name
	if template != "" {
		q += " TEMPLATE " + template + testPGCloneStrategy
	}
	if _, err := testPGAdmin.Exec(q); err != nil {
		if template != "" && strings.Contains(err.Error(), "is being accessed by other users") {
			t.Fatalf("clone %s into %s: %v\n"+
				"Postgres refuses to copy a template while any session is connected to it. "+
				"Nothing may open %s once postgresTestTemplate has built it; look for a "+
				"connection to it in pg_stat_activity.", template, name, err, template)
		}
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := testPGAdmin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	db, err := openPostgresTestPool(name)
	if err != nil {
		t.Fatal(err)
	}
	// Registered after the drop, so it runs before it (cleanups are LIFO).
	t.Cleanup(func() { db.Close() })
	return db
}

// The migrated template, built once per test binary by the first test that
// asks for a schema-loaded database.
var (
	testPGTemplateOnce sync.Once
	testPGTemplateName string // set once the template exists, even half-built
	testPGTemplateErr  error

	// testPGCloneStrategy is appended to CREATE DATABASE … TEMPLATE. WAL_LOG
	// (the default since Postgres 15) copies the template block by block
	// through the WAL; FILE_COPY copies its files and forces a checkpoint
	// before and after, which costs more on a real disk as the run's
	// databases accumulate dirty pages. initPostgresTests leaves it empty on
	// a server older than 15, which has no STRATEGY clause and only ever
	// copies files.
	testPGCloneStrategy = " STRATEGY WAL_LOG"
)

// postgresTestTemplate returns the name of the template database that
// schema-loaded test databases are cloned from, building it on first use.
//
// It is built exactly as a test database used to be: an empty database from
// template1, then adminInitDatabase, so it holds postgres.sql, the initial
// migration rows and every migration since. Then every connection to it is
// closed and the harness waits until the server shows none, because
// CREATE DATABASE … TEMPLATE fails while any session is connected to the
// template, and connections to it are disallowed so that nothing can open it
// again before dropPostgresTestTemplate.
func postgresTestTemplate() (string, error) {
	testPGTemplateOnce.Do(func() {
		testPGTemplateErr = buildPostgresTestTemplate()
	})
	return testPGTemplateName, testPGTemplateErr
}

func buildPostgresTestTemplate() error {
	name, err := randomPGName(testPGTemplatePrefix)
	if err != nil {
		return err
	}
	if _, err := testPGAdmin.Exec("CREATE DATABASE " + name); err != nil {
		return fmt.Errorf("create template %s: %v", name, err)
	}
	// From here on TestMain drops it, whatever happens next.
	testPGTemplateName = name

	db, err := openPostgresTestPool(name)
	if err != nil {
		return fmt.Errorf("template: %v", err)
	}
	cfg := config.New()
	cfg.Database.Type = driverPostgres
	app := &App{db: newDatastore(db, driverPostgres), cfg: cfg}
	loadErr := loadPostgresTestSchema(app)
	if err := db.Close(); err != nil {
		return fmt.Errorf("close template %s: %v", name, err)
	}
	if loadErr != nil {
		return fmt.Errorf("postgres schema load into template %s: %v", name, loadErr)
	}

	// db.Close returns once the connections are closed on this side; the
	// server ends each backend a moment later. Wait for that here rather
	// than let the first clone race it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := testPGAdmin.QueryRow("SELECT COUNT(*) FROM pg_stat_activity WHERE datname = ?", name).Scan(&n); err != nil {
			return fmt.Errorf("count connections to template %s: %v", name, err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("template %s still has %d connection(s) 10s after the harness closed its own; "+
				"Postgres cannot clone a template anything is connected to", name, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// And keep it that way: with connections disallowed, nothing (a stray
	// test, a psql session, autovacuum outside wraparound) can connect to the
	// template while the run clones it. A clone does not inherit the setting.
	if _, err := testPGAdmin.Exec("ALTER DATABASE " + name + " WITH ALLOW_CONNECTIONS false"); err != nil {
		return fmt.Errorf("close template %s to connections: %v", name, err)
	}
	return nil
}

// dropPostgresTestTemplate drops the template, if one was built. TestMain
// calls it after the tests have run. WITH (FORCE), so a stray connection
// cannot leave it behind.
func dropPostgresTestTemplate() error {
	if testPGAdmin == nil || testPGTemplateName == "" {
		return nil
	}
	if _, err := testPGAdmin.Exec("DROP DATABASE IF EXISTS " + testPGTemplateName + " WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop template %s: %v", testPGTemplateName, err)
	}
	return nil
}

// pgFromScratch holds the names of the tests that called
// buildPostgresFromScratch.
var pgFromScratch sync.Map

// buildPostgresFromScratch makes every schema-loaded Postgres database that
// t and its subtests get from newPostgresTestApp (directly, or through
// openAppTestDB, engineTestApp, forEachCaseEngine and the like) be built the
// way `db init` builds one, on an empty database, rather than cloned from the
// template.
//
// A test that is about init, the migrations or db copy calls it first, so
// that what it checks is a real run of them on this database and not a copy
// of one made earlier. It does nothing on another engine.
func buildPostgresFromScratch(t testing.TB) {
	t.Helper()
	name := t.Name()
	pgFromScratch.Store(name, true)
	t.Cleanup(func() { pgFromScratch.Delete(name) })
}

// wantsPostgresFromScratch reports whether t or a test it is a subtest of
// called buildPostgresFromScratch.
func wantsPostgresFromScratch(t testing.TB) bool {
	name := t.Name()
	for {
		if _, ok := pgFromScratch.Load(name); ok {
			return true
		}
		i := strings.LastIndex(name, "/")
		if i < 0 {
			return false
		}
		name = name[:i]
	}
}

// newPostgresTestApp returns an App whose db is a fresh Postgres database
// with the full schema, as `writefreely db init` would leave it. cfg may be
// nil for config.New(); its Database.Type is set to postgres either way.
//
// The database is a clone of the migrated template (postgresTestTemplate),
// unless the test called buildPostgresFromScratch, in which case it is an
// empty database (newPostgresTestDB) with adminInitDatabase run on it.
//
// A schema-load error fails the test. So does an unsupportedDriver panic: a
// Postgres case that was forgotten must not turn into a skip while the
// required test-postgres job stays green.
func newPostgresTestApp(t testing.TB, cfg *config.Config) *App {
	t.Helper()
	if wantsPostgresFromScratch(t) {
		return newPostgresTestAppFromScratch(t, cfg)
	}
	return newPostgresTestAppFromTemplate(t, cfg)
}

// newPostgresTestAppFromTemplate is newPostgresTestApp's default path: a
// clone of the migrated template.
func newPostgresTestAppFromTemplate(t testing.TB, cfg *config.Config) *App {
	t.Helper()
	if !runPostgresTests() {
		t.Skipf("skipping postgres test: %s is not %q", envTestDBType, driverPostgres)
	}
	if testPGAdmin == nil {
		t.Fatalf("postgres harness not initialised (TestMain did not run initPostgresTests)")
	}
	template, err := postgresTestTemplate()
	if err != nil {
		t.Fatalf("postgres test template: %v", err)
	}
	return postgresTestAppOn(createPostgresTestDB(t, template), cfg)
}

// newPostgresTestAppFromScratch is newPostgresTestApp's path for a test that
// called buildPostgresFromScratch: an empty database, then adminInitDatabase.
func newPostgresTestAppFromScratch(t testing.TB, cfg *config.Config) *App {
	t.Helper()
	app := postgresTestAppOn(newPostgresTestDB(t), cfg)
	if err := loadPostgresTestSchema(app); err != nil {
		t.Fatalf("postgres schema load: %v", err)
	}
	return app
}

func postgresTestAppOn(sdb *sql.DB, cfg *config.Config) *App {
	if cfg == nil {
		cfg = config.New()
	}
	cfg.Database.Type = driverPostgres
	return &App{db: newDatastore(sdb, driverPostgres), cfg: cfg}
}

// newPostgresTestDatastore is newPostgresTestApp(t, nil).db: a *datastore
// with driverName postgres over a fresh, schema-loaded database.
func newPostgresTestDatastore(t testing.TB) *datastore {
	t.Helper()
	return newPostgresTestApp(t, nil).db
}

// loadPostgresTestSchema runs adminInitDatabase. It deliberately does not
// recover: an unsupportedDriver panic must fail the test, not skip it.
func loadPostgresTestSchema(app *App) error {
	return adminInitDatabase(app)
}

// TestPostgresHarness checks the harness itself: a test's database exists
// while it runs, is named wf_test_*, and is gone once the test has finished.
func TestPostgresHarness(t *testing.T) {
	if !runPostgresTests() {
		t.Skipf("skipping postgres test: %s is not %q", envTestDBType, driverPostgres)
	}
	exists := func(name string) bool {
		var n int
		if err := testPGAdmin.QueryRow("SELECT COUNT(*) FROM pg_database WHERE datname = ?", name).Scan(&n); err != nil {
			t.Fatalf("look up %s: %v", name, err)
		}
		return n == 1
	}

	var name string
	t.Run("inner", func(t *testing.T) {
		db := newPostgresTestDB(t)
		if err := db.QueryRow("SELECT current_database()").Scan(&name); err != nil {
			t.Fatalf("current_database: %v", err)
		}
		if !strings.HasPrefix(name, testPGPrefix) {
			t.Errorf("database %q is not named %s*", name, testPGPrefix)
		}
		if !exists(name) {
			t.Errorf("database %q not found while the test runs", name)
		}
		// A connection left open must not stop the drop.
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("hold a connection: %v", err)
		}
		_ = conn
	})
	if name == "" {
		t.Fatal("inner test did not report its database")
	}
	if exists(name) {
		t.Errorf("database %q still exists after its test finished", name)
	}
}

// TestPostgresSchemaLoads proves a full `db init` works against a real
// server, so it builds from scratch rather than cloning the template.
func TestPostgresSchemaLoads(t *testing.T) {
	buildPostgresFromScratch(t)
	ds := newPostgresTestDatastore(t)
	for _, table := range []string{"users", "collections", "posts", "appmigrations"} {
		ok, err := ds.dialect.TableExists(context.Background(), ds.DB, table)
		if err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
		if !ok {
			t.Errorf("table %s missing after schema load", table)
		}
	}
}

// TestPostgresSchemaLoadDoesNotSwallowUnportedPanic guards against the
// harness turning an unsupportedDriver panic into a skip, which would let a
// migration or query that forgot its Postgres case pass the required
// test-postgres job unnoticed. It needs no database: the driver is one
// adminInitDatabase has no branch for, so it panics before touching app.db.
func TestPostgresSchemaLoadDoesNotSwallowUnportedPanic(t *testing.T) {
	cfg := config.New()
	cfg.Database.Type = "oracle"
	app := &App{cfg: cfg}
	assert.PanicsWithValue(t, `adminInitDatabase: not implemented for database driver "oracle"`,
		func() { _ = loadPostgresTestSchema(app) })
}

// TestPostgresTemplateParity checks that a database cloned from the
// template is the same as one built from scratch by adminInitDatabase, the
// way every Postgres test database was built before the template: the same
// schema objects, owners, privileges and database settings, the same rows in
// every table (the migration records and anything a migration seeds), and
// the same sequence positions. Only timestamps are left out, reduced to
// whether each is NULL, because the template was migrated earlier in the
// run.
func TestPostgresTemplateParity(t *testing.T) {
	clone := newPostgresTestAppFromTemplate(t, nil)
	fresh := newPostgresTestAppFromScratch(t, nil)
	if !strings.HasPrefix(testPGTemplateName, testPGTemplatePrefix) {
		t.Fatalf("no template was built (name %q)", testPGTemplateName)
	}

	want := postgresSnapshot(t, fresh.db.DB)
	got := postgresSnapshot(t, clone.db.DB)
	assert.Equal(t, sortedSnapshotKeys(want), sortedSnapshotKeys(got), "sections differ")
	for k, w := range want {
		assert.Equal(t, w, got[k], "%s differs between a template clone and a fresh db init", k)
	}
	// Guard against a snapshot that compares nothing.
	for _, k := range []string{"columns", "indexes", "constraints", "relations", "sequences", "database", "rows:appmigrations", "rows:wisp_migrations"} {
		assert.NotEmpty(t, want[k], "snapshot section %s is empty", k)
	}

	// The template stays closed to connections; the clone does not inherit
	// that.
	var allow bool
	require.NoError(t, testPGAdmin.QueryRow("SELECT datallowconn FROM pg_database WHERE datname = ?", testPGTemplateName).Scan(&allow))
	assert.False(t, allow, "the template accepts connections")
}

// postgresSnapshot describes everything in db's current schema that a
// template clone could get wrong, as sorted lines per section.
func postgresSnapshot(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	snap := map[string][]string{}
	lines := func(section, q string) {
		t.Helper()
		rows, err := db.Query(q)
		require.NoError(t, err, section)
		defer rows.Close()
		for rows.Next() {
			var s sql.NullString
			require.NoError(t, rows.Scan(&s), section)
			snap[section] = append(snap[section], s.String)
		}
		require.NoError(t, rows.Err(), section)
		sort.Strings(snap[section])
	}

	lines("database", `SELECT concat_ws('|', pg_get_userbyid(datdba), pg_encoding_to_char(encoding), datcollate, datctype,
		datlocprovider, datallowconn, datistemplate, datconnlimit, datacl::text)
		FROM pg_database WHERE datname = current_database()`)
	lines("database settings", `SELECT array_to_string(s.setconfig, ',') FROM pg_db_role_setting s
		JOIN pg_database d ON d.oid = s.setdatabase WHERE d.datname = current_database()`)
	lines("schemas", `SELECT concat_ws('|', nspname, pg_get_userbyid(nspowner), nspacl::text) FROM pg_namespace
		WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'`)
	lines("extensions", `SELECT concat_ws('|', extname, extversion) FROM pg_extension`)
	lines("columns", `SELECT concat_ws('|', table_name, column_name, ordinal_position, data_type, udt_name,
		character_maximum_length, numeric_precision, numeric_scale, is_nullable, column_default, collation_name,
		is_identity, identity_generation, identity_start, identity_increment, identity_maximum, is_generated, generation_expression)
		FROM information_schema.columns WHERE table_schema = current_schema()`)
	lines("indexes", `SELECT concat_ws('|', tablename, indexname, indexdef) FROM pg_indexes WHERE schemaname = current_schema()`)
	lines("constraints", `SELECT concat_ws('|', c.conrelid::regclass::text, c.conname, c.contype, pg_get_constraintdef(c.oid))
		FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace WHERE n.nspname = current_schema()`)
	lines("relations", `SELECT concat_ws('|', c.relname, c.relkind, pg_get_userbyid(c.relowner), c.relpersistence,
		c.relacl::text, array_to_string(c.reloptions, ','), obj_description(c.oid, 'pg_class'))
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema()`)
	lines("sequences", `SELECT concat_ws('|', sequencename, sequenceowner, data_type, start_value, min_value, max_value,
		increment_by, cycle, last_value) FROM pg_sequences WHERE schemaname = current_schema()`)
	lines("functions", `SELECT concat_ws('|', p.proname, pg_get_userbyid(p.proowner), pg_get_function_identity_arguments(p.oid))
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = current_schema()`)
	lines("triggers", `SELECT concat_ws('|', tgrelid::regclass::text, tgname, pg_get_triggerdef(oid)) FROM pg_trigger WHERE NOT tgisinternal`)
	lines("types", `SELECT concat_ws('|', t.typname, t.typtype, pg_get_userbyid(t.typowner)) FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = current_schema() AND t.typelem = 0
		AND (t.typrelid = 0 OR (SELECT relkind FROM pg_class WHERE oid = t.typrelid) = 'c')`)

	// Every table's rows, as text, with timestamps reduced to whether they
	// are set.
	cols := map[string][]string{}
	r, err := db.Query(`SELECT c.table_name, c.column_name, c.data_type FROM information_schema.columns c
		JOIN information_schema.tables tb ON tb.table_schema = c.table_schema AND tb.table_name = c.table_name
		WHERE c.table_schema = current_schema() AND tb.table_type = 'BASE TABLE' ORDER BY c.table_name, c.ordinal_position`)
	require.NoError(t, err)
	for r.Next() {
		var table, col, typ string
		require.NoError(t, r.Scan(&table, &col, &typ))
		expr := quotePGIdent(col) + "::text"
		if strings.HasPrefix(typ, "timestamp") || typ == "date" || strings.HasPrefix(typ, "time ") {
			expr = "(" + quotePGIdent(col) + " IS NULL)::text"
		}
		cols[table] = append(cols[table], expr)
	}
	require.NoError(t, r.Err())
	r.Close()
	for table, exprs := range cols {
		section := "rows:" + table
		lines(section, "SELECT concat_ws('|', "+strings.Join(exprs, ", ")+") FROM "+quotePGIdent(table))
		if snap[section] == nil {
			snap[section] = []string{}
		}
	}
	return snap
}

func quotePGIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func sortedSnapshotKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
