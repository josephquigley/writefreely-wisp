# Testing against Postgres

Postgres support is being added alongside MySQL and SQLite. This page is how
to run the test suite against a real Postgres server, on your machine and in
CI.

**Status:** the scaffold is in place, the harness is not yet. The variables
below are set by every runner described here, but nothing in the test code
reads them yet, so for now `make test-postgres` runs the same unit tests as
`go test ./...` with a Postgres server standing by. The harness that creates a
database per test arrives with the Postgres driver; this page will be updated
when it does.

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
| `WF_TEST_DB_TYPE` | Which database the database-backed tests use. `postgres` selects Postgres. Unset, the tests keep their existing behaviour: MySQL when `TEST_MYSQL` is set, otherwise database-backed tests are skipped. |
| `WF_TEST_PG_DSN` | A Postgres connection URL, for example `postgres://writefreely:writefreely@127.0.0.1:5432/writefreely?sslmode=disable`. Read only when `WF_TEST_DB_TYPE=postgres`. |

The role in `WF_TEST_PG_DSN` must be allowed to create and drop databases.
The harness connects to the database named in the URL only to create a fresh
database for each test, works in that one, and drops it afterwards, so the
named database itself is never written to. The official image's
`POSTGRES_USER` is a superuser, which is enough.

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
service container and the same two variables. It is marked
`continue-on-error` while Postgres support is incomplete, so its failures are
visible on a pull request without blocking it. It becomes a required check
once the dialect work is finished.

## Version

Tests target **Postgres 18**. Older majors are not tested.
