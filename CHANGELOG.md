# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions carry a `+wisp` build suffix to distinguish them from upstream releases.

This edition diverged from upstream WriteFreely. See its [releases](https://github.com/writefreely/writefreely/releases) for further context.

Each change gets one bullet, a single sentence of at most 35 words; related
changes share a bullet. Detailed notes are on the corresponding
[GitHub release](https://github.com/josephquigley/writefreely-wisp/releases).

## [Unreleased]

### Security

- Access-token lookups and deletion now match with `=` instead of `LIKE`, closing a pre-auth bypass where a crafted wildcard token matched any user's token.
- OAuth login states are strictly single-use, and replayed or unknown states are refused instead of accepted.
- OAuth login states are strictly single-use, and replayed or unknown states are refused instead of accepted.
- OAuth login states are strictly single-use, and replayed or unknown states are refused instead of accepted.

### Fixed

- `db init` stops with an error at the first table it cannot create, instead of reporting success with tables missing.

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
