# Running WriteFreely in Docker

One image, one layout. The binary goes where binaries go, the assets
where read-only shared data goes, and everything the instance writes into
a single state directory.

| Path | Holds |
|---|---|
| `/usr/bin/writefreely` | the binary |
| `/usr/share/writefreely/` | `templates/`, `static/`, `pages/`, read-only |
| `/data/` | `config.ini`, `keys/`, the SQLite database if used, `uploads/` |

That state directory is the only thing to back up, and the only mount
either compose file gives the application. The container's working
directory is the state directory, so the binary finds `config.ini`
without a `-c` flag.

Point uploads there as well. Without `dir` they default to a directory
inside the asset tree, which ships with the image, so an upgrade would
discard them:

```ini
[uploads]
enabled = true
dir = /data/uploads
```

The entrypoint generates encryption keys when they are absent, creates
the schema on a first run and applies pending migrations, then runs the
server.

## First run, development stack

```sh
cp .env.example .env
$EDITOR .env                 # set MYSQL_PASSWORD and MYSQL_ROOT_PASSWORD
mkdir -p data dbdata
docker compose up -d --build
```

The app container will exit on its first start, reporting that it has no
configuration. That is expected. Generate one:

```sh
docker compose run --rm app writefreely --config
```

Answer `Production, behind reverse proxy` unless you know otherwise, and
give the database section these values, matching your `.env`:

```
Host      db
Port      3306
Database  writefreely
Username  writefreely
Password  <MYSQL_PASSWORD>
```

Then create your admin account and start the stack. The schema is created
automatically on the first start, so there is no separate init step:

```sh
docker compose up -d
docker compose exec app writefreely --create-admin youruser:yourpassword
```

WriteFreely is now on <http://localhost:8080>, and Mailpit is on
<http://localhost:8025>.

## First run, production stack

```sh
cp .env.example .env
$EDITOR .env
mkdir -p data dbdata
docker compose -f docker-compose.prod.yml up -d
docker compose -f docker-compose.prod.yml run --rm app writefreely --config
docker compose -f docker-compose.prod.yml up -d
docker compose -f docker-compose.prod.yml exec app \
    writefreely --create-admin youruser:yourpassword
```

The database host is `db`, and everything the instance owns lives in
`./data`. The development stack builds the image from this checkout; this
one pulls the published one, so nothing is compiled on the server. To pin
a release instead of tracking `latest`, set `WRITEFREELY_IMAGE` in `.env`.

Three kinds of image tag are published:

| Tag | Moves? | Use it when |
|---|---|---|
| `latest` | every release | you want whatever is newest |
| `0.18-wisp` | every patch in that series | you want fixes but not a minor bump |
| `0.18.1-wisp` | never | you want exactly one build |

A series tag such as `0.18-wisp` is the usual choice for a deployment: patch
releases arrive on a `docker compose pull`, while a `0.19` would not.

Putting the instance behind Cloudflare needs cache rules that account for the
session cookie the index sets on every request; see [cloudflare.md](cloudflare.md).

## File ownership

Both containers run as `PUID`:`PGID` from `.env`, which default to
1000:1000. Every piece of state is a bind mount (`./data` and `./dbdata`),
and a bind mount keeps whatever ownership it has on the host, so those ids
are what decide whether the containers can write.

On most Linux hosts the first human user is uid 1000 and the defaults are
already correct. If yours is not, say so rather than chowning the
directories to a user that is not you:

```sh
echo "PUID=$(id -u)" >> .env
echo "PGID=$(id -g)" >> .env
```

On macOS, Docker Desktop remaps bind-mount ownership and these values make
no difference either way.

The database service is LinuxServer's MariaDB image, which reads the same
two variables and adopts that ownership for the files it writes. Its data
directory is `/config/databases`, not `/var/lib/mysql`, so if you ever
swap it for the official `mariadb` image, the existing files are not where
that image looks and it will not migrate them for you.

That image boots through s6 init and expects to start the server itself,
so do not add a `command:` override to either database service. It
already defaults to `utf8mb4` / `utf8mb4_general_ci`; anything else goes
in `/config/custom.cnf`, which it writes on first start.

Note that `schema.sql` declares `DEFAULT CHARSET=latin1` on every table
it creates, so the server-level character set matters less than it looks:
the application connects with `charset=utf8mb4` regardless.

The production stack binds to `127.0.0.1:8080`, so put a reverse proxy in
front of it and terminate TLS there.

## Sending mail

WriteFreely reads its mail settings from `config.ini` and sends through
either SMTP or the Mailgun API. Password resets and invites are silently
unavailable until one is configured. The credentials themselves can live in
`.env` -- see "Keeping secrets out of config.ini" below.

The development stack runs [Mailpit](https://mailpit.axllent.org/), which
accepts everything and delivers nothing. Read what the app sent at
<http://localhost:8025>. It authenticates any credentials, but WriteFreely
only builds an SMTP mailer when a username and a password are both set, so
give it something:

```ini
[email]
smtp_host             = mailpit
smtp_port             = 1025
smtp_username         = dev
smtp_password         = dev
smtp_enable_start_tls = false
```

In production, point the same section at a real relay:

```ini
[email]
smtp_host             = smtp.example.com
smtp_port             = 587
smtp_username         = writefreely@example.com
smtp_password         = <password>
smtp_enable_start_tls = true
```

Or use the Mailgun API instead, which takes precedence when both are
present:

```ini
[email]
domain          = mail.example.com
mailgun_private = <private API key>
mailgun_europe  = false
```

Mailpit is deliberately absent from the production stack. Nothing stops
you adding it for a smoke test, but a catcher left in front of a live
instance swallows every password reset.

## What persists, and what does not

Anything not on this list lives in the container's writable layer and is
destroyed when the container is recreated, which happens on every image
upgrade. Both stacks use the same two bind mounts:

| Data | Where |
|---|---|
| Config, keys, uploads, SQLite database if used | `./data`, mounted at `/data` |
| MariaDB database | `./dbdata` (LinuxServer layout: files sit under `./dbdata/databases`) |

Uploaded images live in the state directory only if `[uploads] dir` points
there, as above. Left unset they default inside the asset tree that ships
with the image, where the next `docker compose pull && up -d` discards
them.

## Custom CSS

The instance-wide stylesheet at `/local/custom.css` is the only one that reaches the post editor and the admin pages. See [custom CSS](custom-css.md) for what it is and how it differs from the per-blog setting.

It lives in the image's asset tree, so mount it in. Create the file first: Docker binds it by inode at container start, and if it does not exist it creates a directory there instead.

```sh
touch ./data/custom.css
```

```yaml
  app:
    volumes:
      - ./data:/data
      - ./data/custom.css:/usr/share/writefreely/static/local/custom.css:ro
```

Editing in place (`cat >>`, `cat >`) is picked up on the next page load. An editor that replaces the file, which is vim by default and most GUI editors, leaves the container serving the old inode until you run `docker compose up -d --force-recreate app`.

Do not mount over `/usr/share/writefreely` or its `static` directory. That masks the templates and pages the binary needs.

## Keeping secrets out of config.ini

Any value in `config.ini` may be written as a reference to an environment
variable instead of the value itself:

```ini
[email]
mailgun_private = ${WRITEFREELY_EMAIL_MAILGUN_PRIVATE}

[oauth.generic]
client_secret = ${WRITEFREELY_OAUTH_GENERIC_CLIENT_SECRET}
```

Put the values in `.env`, which both compose files pass to the container:

```
WRITEFREELY_EMAIL_MAILGUN_PRIVATE=key-...
WRITEFREELY_OAUTH_GENERIC_CLIENT_SECRET=...
```

`config.ini` can then be committed, mirrored or backed up without carrying a
secret, while `.env` stays on the host.

The variable names are yours to choose. Nothing derives a name from the
section and key, and an environment variable on its own configures nothing:
the reference in the file is what makes it apply.

**A value is substituted only when it is entirely a reference.** `${NAME}`
alone is replaced; `p@ss$word`, `$HOME` and `a${NAME}b` are literals and are
left exactly as written. There is no escape sequence, so a value whose literal
content is `${SOME_NAME}` cannot be expressed -- pick a different value or a
different variable name.

**A referenced variable that is not set stops the instance from starting**,
naming every one it could not resolve:

```
configuration references environment variables that are not set:
  [email] mailgun_private references WRITEFREELY_EMAIL_MAILGUN_PRIVATE, which is not set
```

That is deliberate. The alternative is passing the literal `${...}` on as a
credential, which fails much later and much less clearly.

Saving settings from `/admin/settings` leaves references intact, so the UI
cannot write an expanded secret back into the file.

## Environment variables

Read from `.env` by both compose files:

| Variable | Default | Meaning |
|---|---|---|
| `MYSQL_PASSWORD` | none, required | Password for the `writefreely` database user. Reference it from `config.ini` as `password = ${MYSQL_PASSWORD}` rather than copying the value |
| `MYSQL_ROOT_PASSWORD` | none, required | MariaDB root password, for administration only |
| `PUID` / `PGID` | `1000` | User and group both containers run as, and the ownership MariaDB writes with |
| `TZ` | `Etc/UTC` | Container timezone |
| `WRITEFREELY_IMAGE` | ghcr.io `:latest` | Production stack only: the published image to run |
| `WRITEFREELY_VERSION` | empty | Development stack only: version string compiled into the binary |

Neither stack starts without the two passwords. That is deliberate, so
that no deployment inherits a password published in this repository.

Read by the image itself:

| Variable | Default | Meaning |
|---|---|---|
| `WRITEFREELY_DOCKER` | set in the image | Tells `--config` it is running in a container: bind `0.0.0.0`, use container asset paths |
| `WRITEFREELY_DOCKER_PARENT_DIR` | `/usr/share/writefreely` | Asset root the configurator writes into the config |
| `WRITEFREELY_AUTO_MIGRATE` | `true` | Apply pending migrations on start |
| `WRITEFREELY_INIT_DB` | `auto` | Create the schema on start. `auto` initializes when the keys also had to be generated, i.e. on a first run. `true` forces it, `false` disables it |
| `WRITEFREELY_CONFIG` | `config.ini` | Config file the entrypoint looks for |
| `WRITEFREELY_KEYS_DIR` | `keys` | Directory the entrypoint checks for keys |

## Stamping a version into the image

The build accepts an argument:

```sh
docker build --build-arg WRITEFREELY_VERSION=0.18.0 -t writefreely:0.18.0 .
```

Without it the build falls back to `git describe`, and then to the version
compiled into `app.go`. The argument matters when the build context has no
usable git repository: building from a git worktree, for instance, where
`.git` is a file pointing outside the context, or from a shallow CI
checkout that carries no tags. Without it those builds injected an empty
version and produced binaries whose footer read a bare "v". CI resolves
the version in a step of its own and passes it in.

## Switching from an upstream WriteFreely container

An existing upstream instance moves onto this image in place, but the two
images keep their state in different places: upstream works out of `/go`,
with the config bind mounted as a file and the keys in a named volume,
and this one works out of `/data`. Gathering that state into one
directory is what the switch consists of, and
`scripts/switch-from-writefreely.sh` does it. See
[switching-from-writefreely.md](switching-from-writefreely.md).

## Upgrading

```sh
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

The entrypoint applies pending migrations on start, so an upgrade that
includes a schema change needs no separate step. **Back the database up
first.** Migrations are not reversible, and downgrading the image after
one has run will not work.

To upgrade without automatic migrations, set `WRITEFREELY_AUTO_MIGRATE`
to `false` and run `writefreely --migrate` yourself when ready.

## Troubleshooting

**The app container exits immediately on a fresh install.** It has no
`config.ini`. The log says so and prints the command to create one.

**The app cannot reach the database.** The database host is the *service*
name, `db`, not `localhost`. A container's `localhost` is itself.

**Permission denied writing to the state directory.** The containers run
as `PUID`:`PGID` and your `./data` is owned by someone else. Set both to
your own ids in `.env` and recreate the containers. See "File ownership"
above.

**The container reports unhealthy but the site works.** Please report it.
The healthcheck accepts any HTTP response as proof of life, so it should
not false-negative on a password-protected or unconfigured instance.

**Uploaded images disappeared after an upgrade.** `[uploads] dir` was not
pointing at the state directory. See the note above.

**`load templates: ... no such file or directory`, and the container
restarts forever.** The asset directories in `config.ini` are empty or
point somewhere with no assets. In this image they belong at
`/usr/share/writefreely`:

```ini
[server]
templates_parent_dir = /usr/share/writefreely
static_parent_dir    = /usr/share/writefreely
pages_parent_dir     = /usr/share/writefreely
```

Empty values are filled in with that path on start, so this only happens
on an older image, or when the keys name a directory that really is wrong.
A configuration carried over from upstream's image, where the assets sat
in the working directory, is the usual source. See
[switching-from-writefreely.md](switching-from-writefreely.md).

**Password resets and invites never arrive.** No `[email]` section, or an
incomplete one. WriteFreely needs `smtp_host`, `smtp_port`,
`smtp_username` and `smtp_password` together, or the two Mailgun keys.

**`migrate: no such table: appcontent`, or `Table 'writefreely.appcontent'
doesn't exist`.** Migrations ran against a database with no schema, which
leaves it stamped at a version it never reached. Recreate the database
from empty and start again; the entrypoint creates the schema on a first
run, so this should only happen if `WRITEFREELY_INIT_DB=false` was set on
a fresh instance.

**`Invalid database type 'sqlite'. Only 'mysql' and 'sqlite3' are
supported right now.`** The driver name in `config.ini` is `sqlite3`, not
`sqlite`. The interactive configurator writes the right value; this bites
when the file is written by hand.

## Running on SQLite instead of MariaDB

Neither compose stack uses SQLite, but the image supports it and it is a
reasonable choice for a small single-user instance. Drop the database
service and point the config at a file in the state directory:

```ini
[database]
type     = sqlite3
filename = /data/writefreely.db
```

`./data` is a bind mount, so the file survives the container. The binary
is built with the `sqlite` build tag, so no extra image is needed.
