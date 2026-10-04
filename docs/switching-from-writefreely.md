# Switching from upstream WriteFreely

An existing upstream WriteFreely instance can move to this edition in
place. There is no export, no import and no data conversion. The same
database, the same `config.ini` and the same keys carry over.

The application is a drop-in. What is not is where a container keeps its
state and its assets, so a container switch is mostly a matter of moving
files and telling the config where they went.

`scripts/switch-from-writefreely.sh` performs the switch. Read this page
first anyway, because the one step that cannot be undone is the database
migration, and the one step that must not be skipped is the backup.

## What carries over, and what changes

| | Upstream | This edition |
|---|---|---|
| Binary name | `writefreely` | `writefreely` |
| Config file | `config.ini` | `config.ini`, plus an optional `[uploads]` section |
| Database schema | through migration V17 | V17, then this edition's own migrations, recorded in `wisp_migrations` |
| Software name in nodeinfo, the `Server` header and the outbound user agent | `WriteFreely` | `WriteFreely`, deliberately unchanged |
| Name shown to people | WriteFreely | WriteFreely (Wisp Edition) |
| Update checks | on | off, see the README |

This edition is based on upstream 0.17.2 and carries upstream's migrations
V1 through V17 unmodified, so an instance running an older release
migrates straight through in one step, exactly as an upstream upgrade
would.

This edition's own migrations are numbered separately, wisp_v1 onwards,
and recorded in their own `wisp_migrations` table; `appmigrations` keeps
upstream's numbering. wisp_v1 only creates a table, `post_images`. wisp_v2
gives tokens, codes and remote IRIs exact-match collations on MySQL, and
wisp_v3 adds the settings tables, lookup indexes and a `publishjobs`
column. Nothing existing is dropped. The reordering of pinned posts uses `posts.pinned_position`,
which upstream already has, and multiple verification links are stored in
the `verification_link` attribute upstream already has, one per line.

Because the machine-readable software name is untouched, federation sees
no change: remote instances that already follow your blogs keep working,
and no re-verification is needed.

## Bare metal

The switch replaces the program and its assets, then migrates. Assets are
not optional: this edition's templates reference JavaScript and styles
that upstream's do not ship.

1. Stop the service and back up the database and the `keys` directory.
   Losing the keys invalidates every session and every encrypted value,
   and no backup of them can be reconstructed.

2. Install this edition over the old install, from the installation root
   (`/var/www/writefreely` on a typical setup): the `writefreely` binary,
   and the `templates`, `static` and `pages` directories. Leave
   `config.ini`, `keys` and any SQLite database where they are.

3. Run the script from that same directory:

   ```sh
   ./scripts/switch-from-writefreely.sh
   ```

   It checks that the binary in place really is this edition, confirms the
   backup with you, applies the migration, and offers to enable image
   uploads. It is equivalent to running `writefreely --migrate` yourself
   and editing `config.ini` by hand.

4. Start the service again.

## Docker

The application is a drop-in, but the image layout is not. Upstream's
image works out of `/go`, with `config.ini` bind mounted as a single file
and the keys in a named volume. This image works out of `/data`, where
one directory holds the config, the keys, uploads, and the SQLite
database if there is one.

| | `writeas/writefreely` | `ghcr.io/josephquigley/writefreely-wisp` |
|---|---|---|
| Working directory | `/go` | `/data` |
| Config | `/go/config.ini`, bind mounted file | `<state>/config.ini` |
| Keys | named volume at `/go/keys` | `<state>/keys` |
| Assets | `/go/{templates,static,pages}`, writable | `/usr/share/writefreely`, read only |
| Runs as | `daemon` | `PUID`:`PGID`, 1000 by default |
| Entrypoint | the binary | a script that generates keys when absent, creates the schema on a first run, and migrates |

So the work is gathering the state into one directory. The script does
that, and it is carried inside the image, so there is nothing to clone:

```sh
docker compose down
docker run --rm -it \
    --user "$(id -u):$(id -g)" \
    -v "$PWD:/workspace" -w /workspace \
    -v writefreely_web-keys:/keys-src:ro \
    --entrypoint switch-from-writefreely \
    ghcr.io/josephquigley/writefreely-wisp:latest --docker --keys-dir /keys-src
```

Replace `writefreely_web-keys` with your own volume, which
`docker volume ls` will show. Compose names volumes after the project, so
the prefix is usually the directory the stack lives in. Running the script
from a clone instead works the same way, and finds a single keys volume on
its own through the docker CLI:

```sh
./scripts/switch-from-writefreely.sh --docker
```

Either way it copies `config.ini` and the keys into `./data`, fills in the
asset directories, points `filename` at the SQLite database where the
container will find it, offers to enable image uploads, and prints the
compose changes to make. It does not edit your compose file, does not
touch the database service, and deletes nothing.

Three of those need explaining, because each one bites an install that
switches over by hand.

**The asset directories.** A configuration written for upstream's image
leaves them empty:

```ini
templates_parent_dir =
static_parent_dir    =
pages_parent_dir     =
```

That is correct there, where the assets sit in the working directory
beside the binary. Here the working directory is the state directory and
the assets are at `/usr/share/writefreely`, so an empty value resolves to
a directory holding no templates and the server exits on start with
`load templates`. The script writes the three keys, and this image also
fills them in at runtime when they are empty, so an instance that was
switched over by hand and is crash-looping recovers on the next restart
without the config being touched.

**What the switch leaves behind.** Copying rather than moving means the
old files are still there when it finishes: the `config.ini` the old
compose service bind mounted, and, if the database lived outside the state
directory, the database as it was before the migration. Both are worth
keeping. They are the rollback path, and deleting your data is not this
script's business.

Two files named `config.ini`, one directory apart, is the problem. Six
months on, an edit to the wrong one changes nothing and reports nothing.
Worse, the stale one is exactly what the old compose service mounted, so
rolling back the compose file without rolling back the data directory
starts an instance on a config with no `[uploads]` and a `filename`
pointing at a database frozen at the switch.

So the script renames what it supersedes, with a `.pre-wisp` suffix, and
lists the results at the end:

```
==> live from here on:

      ./data/config.ini
      /data/db/writefreely.db (inside the container)

    superseded, read by nothing, kept so you can roll back:

      ./config.ini.pre-wisp
          the config the old image bind mounted, still holding your credentials
```

Nothing is deleted, and everything is still recoverable by renaming it
back. `--keep-originals` skips the renaming if you have tooling that
expects the old paths, at the cost of the ambiguity above.

The superseded config still holds whatever credentials it always did:
`mailgun_private`, `smtp_password`, OAuth client secrets. Rotating any of
them means editing the live copy under `./data`, and deleting the
superseded one once you no longer want the rollback. The script sets both
to `0600`, which is what the keys beside them use. Some bind mounts,
Docker Desktop's among them, report a fixed mode and ignore that; the
script says so rather than claiming a permission it did not get.

**A relative SQLite path.** `filename` is resolved by whatever container
wrote it, so `filename = db/writefreely.db` in an upstream config means
`/go/db/writefreely.db` inside upstream's container, which is some
directory on the host that has nothing to do with where you run this
script. The script looks for the file next to the config, then inside the
state directory, and when it finds it already inside the state directory
it leaves it there and points `filename` at the path it will have in the
container. A database that lives elsewhere is copied in, and the original
is left alone.

Then point the app service at the new image and the new mount:

```yaml
  writefreely-web:
    image: ghcr.io/josephquigley/writefreely-wisp:latest
    user: "${PUID:-1000}:${PGID:-1000}"
    volumes:
      - ./data:/data
```

Remove the old `config.ini` file mount and the keys volume from that
service. Leave the database service alone: its data is untouched by any of
this, and the app reaches it by service name as before. Add `PUID` and
`PGID` to `.env` if your host user is not uid 1000, as described in
[docker.md](docker.md).

`docker compose up -d` then starts the new image, and the entrypoint
applies this edition's migrations.

The entrypoint migrates before the server starts, and it does not undo the
migration if the server then fails to boot. A container that crash-loops
after the switch is therefore an already-migrated database with an
application that will not run, which is recoverable (fix the config and
restart) but is not a state to keep trying blindly from. Read the first
lines of `docker compose logs app`: the migration is reported there, and
so is whatever the server objected to afterwards.

The keys must be in `./data/keys` **before** that first start. The
entrypoint treats absent keys as the signal that this is a new instance:
it would generate a fresh set, invalidating every session, and then run
`--init-db` against a database that already has a schema.

The compose files in this repository are a working example of the target
layout, but they are not a drop-in replacement for yours. They name the
database service `db`, use LinuxServer's MariaDB image, and expect
`./dbdata`, none of which matches an upstream stack.

## Enabling image uploads

Uploads are off until the config says otherwise, whether or not you use
the script:

```ini
[uploads]
enabled     = true
max_size_mb = 10
dir         = /data/uploads
```

`max_size_mb` bounds one file, not a user's total storage. `dir` is where
the files are written. Under Docker it has to point into the state
directory, because everything else in the container is discarded when the
container is recreated. On bare metal it can be left out, in which case
uploads go under the static asset tree, which is fine until an upgrade
replaces that tree. Which image types are accepted is fixed by the
decoders compiled in, and is not configurable.

### Keeping images in object storage

Images can live in an S3-compatible store such as Garage or MinIO instead of
`dir`. Every node pointed at the same bucket then serves the same images,
which a directory on one machine cannot do:

```ini
[storage]
type                 = s3
s3_endpoint          = http://garage:3900
s3_region            = garage
s3_bucket            = blog
s3_prefix            = uploads
s3_access_key_id     = ${WF_S3_ACCESS_KEY_ID}
s3_secret_access_key = ${WF_S3_SECRET_ACCESS_KEY}
```

Without the section, or with `type = local`, nothing changes. The endpoint's
scheme decides whether TLS is used. Addressing is path-style, which Garage
needs; set `s3_virtual_host = true` for a store that wants bucket subdomains.
The bucket can stay private: images are still served at `/uploads/...` and
streamed through the app, never redirected to the bucket, so URLs that other
servers have cached keep working. At startup the server checks that it can
write to and delete from the bucket, and refuses to start if the store answers
and says no (a missing bucket, a rejected key). If the store does not answer
within 15 seconds, the blog starts anyway and logs "uploaded images are
unavailable"; pages render, `/uploads/` returns 502 for images and uploads fail
with a 503 until the store is back. Each S3 call is bounded (30 seconds to
write or delete, 30 seconds to start fetching an image).

Existing images are copied in with:

```sh
writefreely images sync --to s3
```

It reads `post_images`, copies each file from `dir`, and checks every copy
against the SHA-256 recorded at upload. It changes nothing in `dir`, and is
safe to run again: images already in the bucket are left alone, and a damaged
one is replaced. It reads the bucket from the `[storage]` section, so the order
is: stop the server, add the section, run the sync, start the server. Nothing
is uploaded in between, so nothing is missed.

## Going back to upstream

Installing upstream over this edition mostly works. The tables this
edition adds (`post_images`, `app_settings`, `app_settings_version` and
`wisp_migrations`) are simply unused, and so are the columns and indexes
it adds to upstream's tables.

The migration counter needs nothing done by hand. `appmigrations` only
ever records upstream's own migrations, so upstream reads the version it
would have reached itself and runs its next migration as usual.

Uploaded images are files on disk, and upstream has nowhere to serve them
from. Posts referencing them will show broken images.

If the switch was recent, the `.pre-wisp` files are the shortest way back:
rename `config.ini.pre-wisp` to `config.ini`, restore the database
alongside it, and point the old compose service at them again. That
database stopped receiving writes at the moment of the switch, so
everything published since is only in the live one.
