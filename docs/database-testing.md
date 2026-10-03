# Testing against each database engine

WriteFreely supports SQLite, MySQL/MariaDB and Postgres. The test suite can
run against any of them, so a change can be checked on every engine it
touches. This page covers choosing the engine; the Postgres specifics are in
[postgres-testing.md](postgres-testing.md).

## The short version

```sh
go test -tags sqlite ./...                              # SQLite (the default)
make test-mysql GOTESTFLAGS='-tags sqlite'              # MariaDB 11
make test-mysql MYSQL_IMAGE=mysql:8.4 GOTESTFLAGS='-tags sqlite'
make test-postgres GOTESTFLAGS='-tags sqlite'           # Postgres 18
```

Each `make` target starts a throwaway container in local Docker on a random
loopback port, runs the suite against it, and removes the container on every
exit path. Never point them at a Docker that reaches a server.

Pass `-tags sqlite` on every engine: several app-level test files (signup,
OAuth state, access tokens, tag queries) sit behind that build tag, and
without it they are not compiled at all, whichever engine is selected.

## Choosing the engine

`WF_TEST_DB_TYPE` selects it:

| Value | Engine | Also needs |
|---|---|---|
| unset, `sqlite` or `sqlite3` | A temporary SQLite file per test, as the suite always did. | nothing |
| `mysql` | A fresh MySQL or MariaDB database per test. | `WF_TEST_MYSQL_DSN` |
| `postgres` | A fresh Postgres database per test. | `WF_TEST_PG_DSN` |

Any other value fails the run before a test starts, as does selecting an
engine whose server cannot be reached: asking for an engine and not getting
it must not look like a green run.

`WF_TEST_MYSQL_DSN` is a Go MySQL driver DSN with no database name, for
example `root:writefreely@tcp(127.0.0.1:3306)/`. The account must be able to
create and drop databases. Each test gets a database named
`wf_test_<16 hex digits>` (utf8mb4), opened with the same parameters
`writefreely` itself uses (`charset=utf8mb4`, `parseTime=true`, local time
zone), loaded with the schema by `adminInitDatabase` exactly as
`writefreely db init` would, and dropped when the test ends.

## How the tests pick it up

The app-level tests build a real `App` with a database behind it. Their setup
helpers (`newInboxTestApp`, `newAnnounceTestApp`, `newHandleTestApp`,
`newSignupTestApp`, `newTemplateTestApp` and a few others) go through two
functions in `harness_app_test.go`:

| Helper | Does |
|---|---|
| `openAppTestDB(t, app, sqliteDriver, sqliteDSN) *sql.DB` | Gives `app` a schema-loaded database on the selected engine. On SQLite it opens `sqliteDSN` through `sqliteDriver`, as the helpers always did. |
| `engineTestApp(t, cfg) *App` | An `App` on a fresh MySQL or Postgres database, or `nil` on SQLite so the caller keeps its own SQLite setup. |

The engine-specific helpers are `newMySQLTestApp` / `newMySQLTestDatastore`
(`harness_mysql_test.go`) and their Postgres twins (`harness_pg_test.go`).
A test written against one of them skips unless that engine is selected.

Some tests are about one engine and stay that way, with a comment saying
why: `TestClipSQLite`, the `*SQLite` twins of the query, value-type and
transaction suites, `TestTaggedPostQueriesOnMySQL`, and so on. They run in
every engine's pass when their own engine is available (SQLite always is).

## The older `TEST_MYSQL` mechanism

`TEST_MYSQL=1` with `WF_USER`, `WF_PASSWORD`, `WF_DB` and `WF_HOST` still
works: tests gated on it copy the tables of an existing reference database.
Under `WF_TEST_DB_TYPE=mysql` the same tests run against a harness database
instead, so no reference database is needed.

## In CI

`.github/workflows/test.yml` has `test-mysql` (a `mariadb:11` service) and
`test-postgres` (a `postgres:18` service) jobs beside the SQLite ones. Both
run `go test -count=1 -tags sqlite ./...`. `test-postgres` is required;
`test-mysql` stays `continue-on-error` until it has been seen green.
