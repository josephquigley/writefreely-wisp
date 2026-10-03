# Testing against Postgres

Postgres support is being added alongside MySQL and SQLite. This page is how
to run the test suite against a real Postgres server, on your machine and in
CI.

**Status:** the harness is in place. Under `WF_TEST_DB_TYPE=postgres`, a
test that asks for a Postgres database gets its own, created for it and
dropped when it ends, with the real schema loaded. The app-level tests
(signup, inbox, announce, template rendering and so on) run on Postgres too:
their setup goes through an engine switch described in
[database-testing.md](database-testing.md), which also covers MySQL and
MariaDB (`make test-mysql`).

## The short version

With Docker running locally:

```sh
make test-postgres
```

That starts a throwaway `postgres:18` container on a random loopback port,
waits until it accepts connections, runs `go test -count=1 ./...` with the
variables below pointing at it, and removes the container afterwards, whether
the tests passed, failed or were interrupted.

Pass extra `go test` flags through `GOTESTFLAGS`, and choose another image with
`PG_IMAGE`:

```sh
make test-postgres GOTESTFLAGS='-run TestCreatePost -v'
make test-postgres PG_IMAGE=postgres:18.1
```

The target uses whatever Docker the `docker` command reaches. Run it against
local Docker, never with `DOCKER_HOST` or a Docker context pointed at a server.

## The variables

| Variable | Meaning |
|---|---|
| `WF_TEST_DB_TYPE` | Which database the database-backed tests use: `sqlite` (the default when unset), `mysql` or `postgres`. Anything else fails the run. See [database-testing.md](database-testing.md). |
| `WF_TEST_PG_DSN` | A Postgres connection URL (`postgres://…`; the keyword=value form is not accepted), for example `postgres://writefreely:writefreely@127.0.0.1:5432/writefreely?sslmode=disable`. Read only when `WF_TEST_DB_TYPE=postgres`, and then required: a run that asks for Postgres and cannot reach it exits non-zero before any test runs, rather than passing with everything skipped. |

The role in `WF_TEST_PG_DSN` must be allowed to create and drop databases.
The harness connects to the database named in the URL only to create a fresh
database for each test, works in that one, and drops it afterwards, so the
named database itself is never written to. The official image's
`POSTGRES_USER` is a superuser, which is enough.

## Writing a Postgres test

The helpers live in `harness_pg_test.go`. Each skips the calling test unless
`WF_TEST_DB_TYPE=postgres`, so a Postgres test sits in the ordinary suite and
costs nothing on a run without Postgres.

| Helper | Gives you |
|---|---|
| `newPostgresTestDatastore(t testing.TB) *datastore` | A `*datastore` with `driverName` `postgres` over a fresh database with the full schema loaded, as `writefreely db init` would load it. The usual choice. |
| `newPostgresTestApp(t testing.TB, cfg *config.Config) *App` | The same, wrapped in an `*App` whose `cfg` is yours (`nil` means `config.New()`). Its `Database.Type` is set to `postgres`. Use it for code that takes an `*App`. |
| `newPostgresTestDB(t testing.TB) *sql.DB` | A fresh, **empty** database, for a test that builds its own tables. |
| `runPostgresTests() bool` | Whether the run is a Postgres run, for gating a test by hand. |

```go
func TestCreateUser_Postgres(t *testing.T) {
	ds := newPostgresTestDatastore(t)
	// ... exercise ds exactly as the code under test would
}
```

Every database is named `wf_test_<16 hex digits>`. It is created from the
maintenance connection in `WF_TEST_PG_DSN`, opened through the
`postgres-rebind` driver (so `?` placeholders work, and the session time zone
is UTC), and dropped with `DROP DATABASE … WITH (FORCE)` in the test's
cleanup, so a connection the test leaked cannot stop the drop. Tests are
isolated from one another and need no teardown of their own.

The schema is loaded by `adminInitDatabase`, the same function `db init`
uses, so these tests exercise the real schema and migrations rather than a
copy. The helpers still turn one specific panic, "not implemented for
database driver", into a skip naming the schema ticket; since the Postgres
schema landed (WFPG-03) that panic no longer happens, and any schema-load
error fails the test. `TestPostgresSchemaLoads` is the probe that a full
`db init` works against a real server.

`withTestDB`, the older helper used by MySQL-only tests, also hands out a
fresh schema-loaded database under Postgres. `TestOAuthDatastore` and
`TestUpdatePostPinStateUnchanged` run on Postgres; `TestTaggedPostQueriesOnMySQL`
stays MySQL-only, because it is about MySQL's two regex engines.

If a run is killed hard (`kill -9`, a crashed container), a database can be
left behind. They are easy to spot and drop by hand:

```sh
psql "$WF_TEST_PG_DSN" -Atc "SELECT datname FROM pg_database WHERE datname LIKE 'wf\_test\_%'"
```

`make test-postgres` never leaves any, because the whole server is thrown
away.

## Running against a Postgres that stays up

`make test-postgres` is the normal path. When you want a server that persists
between runs, for poking at with `psql` or for running `go test` repeatedly,
the development compose file has an opt-in profile:

```sh
docker compose --profile postgres up -d postgres
WF_TEST_DB_TYPE=postgres \
WF_TEST_PG_DSN='postgres://writefreely:writefreely@127.0.0.1:5432/writefreely?sslmode=disable' \
  go test -count=1 ./...
```

It listens on loopback only, on `POSTGRES_PORT` (default 5432), with the
password from `POSTGRES_PASSWORD` in `.env` (default `writefreely`). Its data
lives in the `pgdata` volume; `docker compose --profile postgres down -v`
removes it. The profile does not change the default stack, which still runs
on MariaDB.

Any other Postgres works the same way: set the two variables and run
`go test`.

## In CI

`.github/workflows/test.yml` has a `test-postgres` job with a `postgres:18`
service container and the same two variables. It is required: a failure
there fails the run, the same as the SQLite job.

## Version

Tests target **Postgres 18**. Older majors are not tested.
