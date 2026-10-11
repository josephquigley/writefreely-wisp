# Settings in the database — design

Status: approved in conversation 2026-10-03, awaiting review of this written spec.
Branch: `config-in-database`, cut from `develop` at `d0c4f4e`.

## Problem

With PostgreSQL support, several WriteFreely nodes can share one database. The
nodes still do not share configuration. `/admin/settings` writes its changes to
`config.ini` on whichever node served the request (`handleAdminUpdateConfig` →
`App.SaveConfig` → `config.Save`), so the other nodes keep their old values, and
after a failover the settings quietly revert.

Context: founder decision 2026-10-03, wisp goes multi-node. This reverses
the HA design's 2026-09-01 decision that wisp stays a pinned single app.

## Goal

* Every node reads the same community-policy settings, and a change made on one
  node is in effect on every node from the next request onward.
* The application never writes `config.ini` while it runs.
* Settings can be changed at runtime from the admin page or from a CLI, with no
  file to edit.
* Upgrading an existing install moves its settings into the database without the
  operator having to do anything.
* One code path for Docker and for bare-metal installs.
* MySQL, SQLite and PostgreSQL all keep working.

## Non-goals

* The AES keys under `keys/` (WFPG-14 part 1). They are per-node disk state too
  and full HA needs them handled, but that is separate work. They stay out
  of the database: the email key beside the ciphertext it protects would
  defeat the encryption, and the cookie and CSRF keys would let anyone who
  reads a backup forge sessions. Founder decision, 2026-10-03: keeping
  identical keys on every node, and redistributing them in the rare event
  they change, is the sysadmin's job, including having them in place
  before a node first starts. No guard against a node generating keys
  into an empty directory is planned.
* Moving images off local disk (WFPG-13).
* Updating any particular deployment's copy of `config.ini`, e.g. a
  config-management tree that would re-deliver stripped keys. That is deploy
  work for whoever runs that deployment.

## Decision summary

| Decision | Choice |
|----------|--------|
| Where bootstrap values live | `config.ini`, kept, read-only, holding literals or `${VAR}` references |
| What happens to migrated keys in `config.ini` | removed from the file, with no backup copy |
| How other nodes see a change | a version counter checked on every request; settings are cached |
| How settings are changed at runtime | `/admin/settings` (expanded) and `writefreely settings` CLI |

The interactive design discussion also rejected:

* **Environment-only bootstrap with a `.env` loader.** It works, but needs an
  env-to-key mapping layer that every new upstream bootstrap field must be added
  to. `config.ini` with `${VAR}` references (already in this fork,
  `config/env.go`) does the same job with no new code, and keeps upstream merges
  of `ServerCfg`/`DatabaseCfg` trivial.
* **Polling for changes on a timer.** It allows a staleness window, and nothing
  makes the window necessary.
* **Querying the database on every read of a setting.** About 200 call sites
  read `app.cfg` as a struct. Each would have to become a call that can fail,
  a page render would make 10–30 queries, and derived state (the compiled
  allowlist, the local timeline) would still need something to notice a change.
* **Postgres `LISTEN/NOTIFY`.** Instant, but Postgres-only, so MySQL and SQLite
  would need a second mechanism anyway.

## 1. Where each setting lives

The rule: **`config.ini` holds infrastructure and credentials** — anything that
connects to something or that whoever deploys the instance sets. **The database
holds community policy**, the knobs an admin turns from `/admin/settings`.

### `config.ini` (bootstrap; read-only at runtime)

| Section | Keys |
|---------|------|
| `[server]` | all keys, including `hash_seed` |
| `[database]` | all keys |
| `[email]` | all keys |
| `[oauth.*]` | all keys, in every provider section |
| `[app]` | `host`, `single_user`, `disable_password_auth` |
| `[uploads]` | `dir` |

Reasons for the `[app]` exceptions:

* `host` is the instance's identity. Changing it breaks federation, so it is
  never a click.
* `single_user` decides which routes are registered (`routes.go`) and sets the
  global `isSingleUser`. It is a mode of the deployment, not a runtime toggle,
  and the upstream admin page does not expose it either.
* `disable_password_auth` can lock every admin out if OAuth is misconfigured. It
  sits with the OAuth configuration it depends on.

### Database (`app_settings`)

Every other key of `[app]`:

`site_name`, `site_description`, `theme`, `editor`, `disable_js`, `webfonts`,
`landing`, `simple_nav`, `wf_modesty`, `chorus`, `forest`, `disable_drafts`,
`open_registration`, `open_deletion`, `min_username_len`, `max_blogs`,
`federation`, `public_stats`, `monetization`, `notes_only`, `private`,
`federation_allowlist`, `instance_announce`, `local_timeline`, `user_invites`,
`default_visibility`, `update_checks`

and from `[uploads]`: `enabled`, `max_size_mb`.

### Settings whose effect was fixed at startup

* **`uploads.enabled`.** Today the upload routes are registered only when it is
  true (`routes.go`). They become always registered, and each upload handler
  returns 404 when `Uploads.Enabled` is false at request time. This makes it a
  true runtime setting.
* **Gopher.** The server starts at boot only if `server.gopher_port > 0` and the
  instance is not private. That start condition stays. In addition, when
  `app.private` becomes true at runtime, the running Gopher handler refuses every
  request, so a now-private instance is not served over Gopher.

### When a key appears in both places

This only happens mid-upgrade, on a node whose `config.ini` could not be
stripped, or after a hand edit. **The database wins.** At load, a DB-bound key
found in `config.ini` is ignored, and one warning per key names it.

## 2. Storage and reload

### Schema

One migration, appended after the current last one (V20 → V21), for all three
dialects:

```sql
app_settings         (name VARCHAR(64) PRIMARY KEY, value TEXT NOT NULL)
app_settings_version (id INT PRIMARY KEY, version BIGINT NOT NULL)
-- the migration inserts the single row (1, 0)
```

* One row per setting. `name` is `section.key` as the ini tag spells it, e.g.
  `app.site_name`, `uploads.max_size_mb`. Adding a setting needs no schema
  change.
* Values are text and are parsed into the existing `config.Config` fields by
  type (string, bool, int).
* A setting with no row takes its default from `config.New()`, including the
  existing rule that `app.local_timeline` defaults to true.
* A row whose name is not in the registry is ignored with a warning. This is
  what lets an older binary read a newer database.
* `version` is a counter, not a timestamp. Node clocks differ, and two saves can
  land in the same second.
* `version = 0` means "nothing imported or saved yet". Section 3 uses that.

### Saving

From the admin page or the CLI, in one transaction:

1. validate every submitted value through the registry (Section 4);
2. upsert the changed rows;
3. `UPDATE app_settings_version SET version = version + 1 WHERE id = 1`;
4. commit.

After the commit, the saving process reloads right away rather than waiting for
its next check. If validation fails, nothing is written.

### Startup order

Today the whole configuration is loaded before the database is opened. The new
order:

1. load `config.ini` (bootstrap);
2. connect to the database;
3. run migrations (when the start does so);
4. import from `config.ini` if this is an upgrade (Section 3);
5. load the settings from the database and merge them over the bootstrap values;
6. build derived state: the federation allowlist and the local timeline.

### Noticing a change

* The handler wrapper in `handle.go` runs, before each request,
  `SELECT version FROM app_settings_version WHERE id = 1`.
* If the version differs from the one this node holds, the node reads every row,
  builds a **new** `config.Config` (bootstrap fields copied plus database
  fields), swaps it in, and rebuilds derived state.
* Background jobs that read settings run the same check at each tick.
* If the version query fails (for instance, the database is unreachable during a
  failover), the node keeps its cached configuration, logs the error, and
  carries on with the request. The request will usually fail on its own content
  query; static assets still work.
* If a reload fails partway, the node keeps the old snapshot and its old version,
  so it tries again on the next request.

### Concurrency

`App.cfg` becomes an `atomic.Pointer[config.Config]`, read through an accessor.
A reload swaps in a complete new snapshot, so a request that started under the
old snapshot finishes under it, never with half old and half new values.
Snapshots are never mutated after they are published.

This is a mechanical rewrite of the roughly 200 `app.cfg` call sites. It also
removes an existing data race: `handleAdminUpdateConfig` currently mutates the
shared `Config` in place while other requests read it.

## 3. Upgrading from `config.ini`

### When the import runs

After migrations, at every server start and inside `--migrate`. It is
idempotent, so the Docker entrypoint's separate `--migrate` step and a plain
bare-metal start both reach it safely.

### Who imports

In one transaction the node runs
`UPDATE app_settings_version SET version = 1 WHERE id = 1 AND version = 0`.

* **One row updated: this node imports.** It writes every DB-bound key present in
  its `config.ini`, reads the rows back, and compares each value with what it
  wrote. It commits only if they all match. On any mismatch it rolls back, leaves
  `config.ini` untouched, and refuses to start with an error naming the keys.
* **Zero rows updated: the database already has settings**, imported earlier or
  by another node that won the race. This node imports nothing. For each
  DB-bound key still in its `config.ini` whose value differs from the database,
  it logs a warning naming the key. That is drift the nodes already had before
  the upgrade, and the database value stands.

### Stripping the file

In both cases, once the database is known to hold the settings, the node removes
the DB-bound keys from its own `config.ini`:

* Edit the loaded file with go-ini, as `config.Save` does today, so other
  comments survive. A comment attached to a removed key is removed with it.
* Leave `${VAR}` references in bootstrap keys exactly as they are on disk.
* Write a temporary file in the same directory, then `rename` it over
  `config.ini`, then `chmod 0600`. A crash leaves the old file or the new one,
  never a partial one.
* No backup copy is written. After a verified import, the database is the only
  copy of those values.
* **If the file cannot be written** (a Docker `:ro` mount, or a file owned by
  config management), do not fail. Log one warning listing the keys to delete by
  hand, and continue; the database wins anyway.

### Fresh installs

* `writefreely config start` (interactive) writes the bootstrap answers to
  `config.ini` and the policy answers (site name, registration, federation and
  the rest) to the database. It already connects to the database to create the
  schema.
* `writefreely config generate` writes a bootstrap-only `config.ini`. Policy
  settings start at their defaults.
* `config.Save` remains for those two commands only. Nothing reachable from a
  running server calls it.

### Docker and bare metal

The same code runs in both. The differences come only from the environment:

* Docker typically has `${VAR}` references in `config.ini` and may mount it
  `:ro`, which takes the warning path above.
* Bare metal typically has literal values and a writable file, which takes the
  strip path.
* `docker-entrypoint.sh` needs no change in logic. Its comments are updated to
  say where settings now live.

### Downgrading

An older binary reads only `config.ini`, so after the strip it would run with
default policy settings. `writefreely settings export` prints the database
settings as an ini fragment to paste back. The changelog entry and
`docs/settings.md` say so.

## 4. Runtime configuration surface

### The registry

`config/settings.go` lists every DB-bound setting once: its name, the
`config.Config` field it fills (found through the ini tag), its type, its
default, and a validator. The admin page, the CLI, the import and the export all
read it, so there is no second list to drift.

A test walks every field of `config.Config` and fails unless the field is
classified as either bootstrap or DB-bound. When an upstream merge adds a field
to `AppCfg`, CI forces a decision instead of letting it land in `config.ini` by
default.

Validators reuse `config/validation.go` where it already covers a field, for
example the federation allowlist parser.

### `/admin/settings`

* Keeps its current fields.
* Adds the DB-bound settings it does not expose today: `theme`, `editor`,
  `disable_js`, `webfonts`, `simple_nav`, `wf_modesty`, `chorus`, `forest`,
  `disable_drafts`, `notes_only`, `federation_allowlist`, `instance_announce`,
  `update_checks`, `uploads.enabled`, `uploads.max_size_mb`.
* Groups `federation_allowlist` and `instance_announce` under **Federation**,
  each with a one-line warning drawn from their existing code comments: these
  change who receives what.
* A save goes through the registry and the transaction in Section 2. On a
  validation failure the form re-renders with the error and nothing is saved.
* Keeps the existing rule that private mode cannot be turned off when
  `canDisablePrivateMode()` says so.

### CLI

```
writefreely settings list              # every setting: name, value, "(default)" when no row
writefreely settings get  app.private
writefreely settings set  app.private true
writefreely settings export            # ini fragment of the DB settings
```

* `set` uses the same validation and save path as the admin page and bumps the
  version, so running nodes pick the change up on their next request.
* `set` or `get` on a bootstrap key is refused with a message saying where it
  lives, e.g.
  `app.host lives in config.ini (bootstrap); edit it there and restart`.
* An unknown name is refused, with the closest registered names suggested.
* Works with no admin account, so a fresh headless install can be configured
  with `docker compose exec`.
* `export` writes to stdout. It holds no secrets, since secrets never reach the
  database.

## 5. Testing

* **Registry:** every `config.Config` field is classified; every setting
  survives save then load unchanged; each validator rejects bad values.
* **Import:**
  * a fixture ini is imported, read back and stripped;
  * comments and `${VAR}` references survive the strip;
  * a read-only ini takes the warning path and the node still starts;
  * a forced read-back mismatch aborts and leaves the ini byte-for-byte
    unchanged;
  * a re-run is a no-op;
  * two importers at once leave exactly one winner, and the loser warns about
    each key that differs.
* **Two nodes:** two `App` instances on one database. A save on A is seen by B's
  next request, and B's derived state (the federation allowlist) is rebuilt.
* **Startup-fixed settings:** upload routes return 404 when disabled and work
  after enabling at runtime; Gopher refuses while private.
* **Race:** `go test -race` over concurrent requests during repeated reloads.
* **Engines:** SQLite and MySQL, plus PostgreSQL through the harness in
  `docs/postgres-testing.md`, alongside the fork's standard checks (`gofmt -l .`,
  `go vet -composites=false ./...`, `go build ./...`, `go build -tags sqlite ./...`,
  `go test -count=1 ./...`).
* **Docker:** the entrypoint starts the image against a `:ro` ini and the
  instance comes up with settings imported.

## Documentation and changelog

* New `docs/settings.md`: what lives where, the CLI, the upgrade behaviour, and
  downgrading.
* `docs/docker.md`: the first-run walkthrough no longer edits policy settings in
  the ini.
* `CHANGELOG.md` `[Unreleased]`, per the fork's rule: separate bullets for the
  settings move (`Changed`) and the new `writefreely settings` command (`Added`).
