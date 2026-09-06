&nbsp;
<p align="center">
	<a href="https://github.com/josephquigley/writefreely-wisp"><img src="https://writefreely.org/img/writefreely.svg" width="350px" alt="WriteFreely (Wisp Edition)" /></a>
</p>
<hr />
<p align="center">
	<strong>WriteFreely (Wisp Edition)</strong>
</p>
<p align="center">
	<a href="https://github.com/writefreely/writefreely">
		<img src="https://img.shields.io/badge/fork%20of-writefreely%2Fwritefreely-blue" alt="Fork of writefreely/writefreely" />
	</a>
	<a href="https://github.com/josephquigley/writefreely-wisp/blob/develop/LICENSE">
		<img src="https://img.shields.io/badge/license-AGPL--3.0-green" alt="AGPL-3.0" />
	</a>
</p>
&nbsp;

> **This is a fork.** WriteFreely (Wisp Edition) is an unofficial fork of
> [WriteFreely](https://github.com/writefreely/writefreely) by
> [Musing Studio LLC](https://musing.studio). It is not affiliated with or
> endorsed by the WriteFreely project. For the official software, go to
> [writefreely.org](https://writefreely.org).

WriteFreely is a clean, minimalist publishing platform made for writers. Start a blog, share knowledge within your organization, or build a community around the shared act of writing.

This edition keeps that intact and adds a small set of blog management features, aiming at something closer to what Ghost offers while staying lighter and simpler.

## What this edition adds

* **Post management list.** An admin view for browsing, filtering and acting on posts in bulk.
* **Multiple verification links.** More than one `rel="me"` link per blog, for verifying several profiles.
* **Subscribe button options.** Control over whether and how the subscribe button appears.
* **Reorderable pinned posts.** Drag pinned posts into the order you want, instead of taking the default.
* **Image uploads.** Upload images from the editor rather than hosting them elsewhere.
* **Instance-wide announce actor.** One actor for the whole instance that anyone can follow to receive every new post from every public blog, instead of following each blog separately. Off unless `instance_announce` is set. Public blogs only, and delivery still obeys `federation_allowlist`.
* **Reply delegate.** Route replies to a blog's posts to a configured account on another fediverse instance, so a reply lands somewhere a reader can actually see it.

Plus assorted fixes carried on top of upstream. Each feature is developed on its own branch so it can be offered upstream independently.

### Update checks are turned off

WriteFreely's built-in update check asks `version.writefreely.org` for the
latest upstream release and compares it against the upstream version this fork
is based on. Because this edition carries changes upstream does not have, that
comparison cannot say whether your install is current: it would report "up to
date" on a stale Wisp Edition, and offer an upstream download that would drop
the features this fork exists to provide.

Rather than report something untrue, the check is disabled and the admin
Updates page explains why. Watch
[releases](https://github.com/josephquigley/writefreely-wisp/releases) instead. See
`updateChecksSupported` in `updates.go` to re-enable it once the check points
at this fork's own releases.

Everything below describes WriteFreely itself and applies to this edition too.

## Features

### Made for writing

Built on a plain, auto-saving editor, WriteFreely gives you a distraction-free writing environment. Once published, your words are front and center, and easy to read.

### A connected community

Start writing together, publicly or privately. Connect with other communities, whether running WriteFreely, [Plume](https://joinplu.me/), or other ActivityPub-powered software. And bring members on board from your existing platforms, thanks to our OAuth 2.0 support.

### Intuitive organization

Categorize articles [with hashtags](https://writefreely.org/docs/latest/writer/hashtags), and create static pages from normal posts by [_pinning_ them](https://writefreely.org/docs/latest/writer/static) to your blog. Create draft posts and publish to multiple blogs from one account.

### International

Blog elements are localized in 20+ languages, and WriteFreely includes first-class support for non-Latin and right-to-left (RTL) script languages.

### Private by default

WriteFreely collects minimal data, and never publicizes more than a writer consents to. Writers can seamlessly create multiple blogs from a single account for different pen names or purposes without publicly revealing their association.

<h2><a href="https://write.as/writefreely"><img src="https://writefreely.org/img/writeas-readme.png" height="32px" alt="Write.as" /></a></h2>

The quickest way to deploy WriteFreely is with [Write.as](https://write.as/writefreely), a hosted service from the team behind WriteFreely. You'll get fully-managed installation, backup, upgrades, and maintenance — and directly fund our free software work ❤️

[**Learn more on Write.as**](https://write.as/writefreely).

## Quick start

WriteFreely deploys as a static binary on any platform and architecture that Go supports. Just use our built-in SQLite support, or add a MySQL or MariaDB database, and you'll be up and running!

This edition has no pre-built binaries yet. Build from source with `make build`, or run the published container image:

```
ghcr.io/josephquigley/writefreely-wisp:latest
```

The repository and the image were previously named `wispwriter`. GitHub redirects the old repository URL, but the container registry does not move a package: images published before the rename stay at `ghcr.io/josephquigley/wispwriter` and receive no new tags. Point existing deployments at the new path.

See [docs/docker.md](docs/docker.md) for the container setup. Upstream's
[installation guide](https://writefreely.org/start) covers configuration, which
is unchanged here.

Already running upstream WriteFreely? It switches over in place, keeping the
same database, config and keys. See
[docs/switching-from-writefreely.md](docs/switching-from-writefreely.md).

### Packages

WriteFreely is packaged in several repositories, thanks to upstream's community.
These ship **upstream WriteFreely, not this edition**, so they do not include the
features listed above:

* [Arch User Repository](https://aur.archlinux.org/packages/writefreely/)
* [Nanos Repository](https://repo.ops.city/v2/packages/eyberg/writefreely/show)

Upstream's [pre-built binaries](https://github.com/writefreely/writefreely/releases/)
are likewise upstream's software. Installing one over a Wisp Edition instance
replaces it with upstream WriteFreely.

## Documentation

Read our full [documentation on WriteFreely.org](https://writefreely.org/docs) &mdash;️ and help us improve by contributing to the [writefreely/documentation](https://github.com/writefreely/documentation) repo.

## Development

Start hacking on WriteFreely with our [developer setup guide](https://writefreely.org/docs/latest/developer/setup). For Docker support, see [docs/docker.md](docs/docker.md) in this repository, or our [Docker guide](https://writefreely.org/docs/latest/admin/docker).

## Releases

Releases are cut by pull request, so the same CI that gates feature work gates them.

```
make bump-patch                                   # edits app.go, commits
git push                                          # you push it
gh pr create --base main --title 'Release <version>'   # you open the pull request
```

`bump` does the first line only: it writes the version into `app.go` and commits it on the branch you are standing on. It stops before pushing, because the diff and the pull request into `main` are worth a look first. Nothing is tagged locally, and the branch does not matter: what is released is whatever version reaches `main`.

Merging that pull request into `main` is what releases: a version on `main` with no matching tag is a release, so the workflow tags it, publishes the GitHub Release, and retags the image the branch build already published. That check makes it idempotent, so a merge that does not change the version publishes nothing.

Put anything an operator has to *do* (migrations, new config keys, rebuild steps) in the pull request body. It becomes the top of the release notes, with the generated list of changes underneath it.

Versions carry `+wisp` as SemVer build metadata, so `0.17.3+wisp` is this fork's build of an upstream `0.17.3` line and compares equal to it rather than older. Tags read `v0.17.3+wisp`, which is also why they never collide with the upstream tags this repository inherits. Image tags cannot contain `+`, so the image for that release is tagged `0.17.3-wisp`.

## Contributing

Issues and pull requests specific to this edition belong on [this repo](https://github.com/josephquigley/writefreely-wisp/issues). Anything that is not specific to the fork is better raised upstream, where it helps everyone.

Code here follows the upstream [Contributing Guide](https://github.com/writefreely/writefreely/blob/develop/CONTRIBUTING.md#contributing-to-writefreely), since features from this fork are offered upstream where they fit.

Upstream welcomes contributions as [code](https://github.com/writefreely/writefreely/blob/develop/CONTRIBUTING.md#contributing-to-writefreely), [bug reports](https://github.com/writefreely/writefreely/issues/new?template=bug_report.md), [feature requests](https://discuss.write.as/c/feedback/feature-requests), [translations](https://poeditor.com/join/project/TIZ6HFRFdE), or [documentation](https://github.com/writefreely/documentation) improvements.

## License

Copyright © 2018-2026 [Musing Studio LLC](https://musing.studio) and contributing authors. Licensed under the [AGPL](https://github.com/writefreely/writefreely/blob/develop/LICENSE).

Changes made in this fork are released under the same license.
