# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions carry a `+wisp` build suffix to distinguish them from upstream releases.

This edition diverged from upstream WriteFreely. See its [releases](https://github.com/writefreely/writefreely/releases) for further context.

Each change gets one bullet, a single sentence of at most 35 words; related
changes share a bullet. Detailed notes are on the corresponding
[GitHub release](https://github.com/josephquigley/writefreely-wisp/releases).

## [Unreleased]

### Added

- PostgreSQL is supported alongside MySQL and SQLite (`type = postgres` under `[database]`), and `writefreely db copy --from sqlite:<path>` moves an existing SQLite database into Postgres, verified before committing.
- `writefreely settings list|get|set|export` changes settings without editing a file, and the admin page covers every database setting and refuses saves from a page older than the current settings.
- Uploaded images can be kept in S3-compatible storage such as Garage through a new `[storage]` section, served at unchanged `/uploads/` URLs, and `writefreely images sync --to s3` copies existing images there.
- Scheduled email publishing and the orphaned-image sweep take a database lock, so two app processes sharing one MySQL or Postgres database never email the same post twice.
- `docs/postgres-ha.md` explains running behind a proxy that follows a Patroni primary, the HAProxy settings failover depends on, and what several processes sharing one database must agree on.

### Changed

- Emails, slugs, post IDs, language codes and remote handles now match case-insensitively on every database, invalid or oversized text is cleaned before storage, and blog renames replace stale redirects.
- Settings now live in the database and leave config.ini on upgrade, which gains a `settings_location` marker; an older binary runs on zero values (a private instance turns public) until `settings export` is pasted back.

### Security

- Access-token lookups and deletion now match with `=` instead of `LIKE`, closing a pre-auth bypass where a crafted wildcard token matched any user's token.
- OAuth login states are strictly single-use, and replayed or unknown states are refused instead of accepted.

### Fixed

- `db init` stops with an error at the first table it cannot create, instead of reporting success with tables missing.
- Re-pinning a post at the position it already holds is no longer refused as forbidden on MySQL and MariaDB.
- A failed post deletion rolls back its transaction instead of leaving the connection holding row locks.
- Successful invite lookups on SQLite no longer log a spurious error.
- The Reader's tag filter matches tags literally instead of treating them as regular expressions.
- Tag pages escape regex characters in the tag, work on MySQL 8, and return an error when a tag query fails, on collection tag pages too.
- Paginated post and user lists break same-second ties by id, so pages no longer repeat or skip entries.
- On MySQL and MariaDB, tokens, invite codes and remote actor addresses now match case-sensitively, and remote actors with non-Latin characters in their URLs save; the upgrade migration may take a while on large instances.
- Saving a post whose title matches many others in a blog no longer fails when the random slug suffix collides too; it now retries several times before giving up.
- A repeated inbound ActivityPub Like is accepted as already recorded, and a failed Like or Undo now returns an error status instead of an HTML error page with HTTP 200.
- The login API, the account settings API and the full-account export return the user's plain email address instead of its stored ciphertext.
- RSS and Atom feeds return an error instead of crashing the request when the query for a blog's posts or tag fails.
- Logs no longer contain subscribers' email addresses: re-subscribing, newsletter sends and failed deliveries now log subscriber IDs or the collection instead of the address.

## [0.20.0+wisp] - 2026-09-09

### Added

- Config values may reference environment variables as `${VAR}` so secrets can live in `.env`, and admin settings now update `config.ini` in place instead of rewriting it.

## [0.19.2+wisp] - 2026-09-08

### Security

- Stops logging the blog's ActivityPub private key, signs remote actor fetches so authorized-fetch servers resolve, and lets single-user instances make their blog public.

## [0.19.1+wisp] - 2026-09-08

### Changed

- A reply delegate is mentioned only while it follows the blog, and Accept, failed handle lookup caching and pager wrapping bugs are fixed.

## [0.19.0+wisp] - 2026-09-06

### Added

- Adds an opt-in instance-wide announce actor and per-blog reply delegates, and fixes a failed Accept erasing the follower and an omitted `local_timeline` defaulting to false.

## [0.18.5+wisp] - 2026-09-06

### Added

- The federation allowlist accepts `*.example.org` wildcard entries, matching subdomains at any depth but never the apex.

## [0.18.4+wisp] - 2026-09-05

### Changed

- Unlisted blogs now federate to followers only, instead of reaching the public timelines of every allowlisted peer.

## [0.18.3+wisp] - 2026-09-05

### Fixed

- The Custom CSS editor on a blog's settings page now renders legibly in dark mode.

## [0.18.2+wisp] - 2026-09-05

### Changed

- The pad, editors, dashboard and sign-in pages follow the device color scheme, ActivityPub keys are generated in Go, and the database healthcheck no longer carries credentials.

## [0.18.1+wisp] - 2026-09-03

### Fixed

- Uploaded images and relative links reach feed readers and fediverse servers as absolute URLs, attach to ActivityPub objects, and appear in link previews and the sitemap.

## [0.18.0+wisp] - 2026-09-01

### Added

- First Wisp Edition release, adding image uploads, post and pinned-post management, multiple verification links, subscribe-form placement, allowlisted signed federation, and fixes for CSRF over HTTP, single-user links and Docker.

[Unreleased]: https://github.com/josephquigley/writefreely-wisp/compare/v0.20.0+wisp...develop
[0.20.0+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/v0.19.2+wisp...v0.20.0+wisp
[0.19.2+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/v0.19.1+wisp...v0.19.2+wisp
[0.19.1+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/v0.19.0+wisp...v0.19.1+wisp
[0.19.0+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/v0.18.5+wisp...v0.19.0+wisp
[0.18.5+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/v0.18.4+wisp...v0.18.5+wisp
[0.18.4+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/v0.18.3+wisp...v0.18.4+wisp
[0.18.3+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/v0.18.2+wisp...v0.18.3+wisp
[0.18.2+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/v0.18.1+wisp...v0.18.2+wisp
[0.18.1+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/v0.18.0+wisp...v0.18.1+wisp
[0.18.0+wisp]: https://github.com/josephquigley/writefreely-wisp/compare/71d6410...v0.18.0+wisp
