# Settings

WriteFreely keeps its configuration in two places.

**`config.ini` holds infrastructure and credentials**: everything in
`[server]`, `[database]`, `[email]` and every `[oauth.*]` section, plus
`[app] host`, `[app] single_user`, `[app] disable_password_auth` and
`[uploads] dir`. A running server only reads this file. Change it, then
restart. Any value may be a `${VARIABLE}` reference to the environment.

**The database holds everything else**: site name and description,
appearance, registration, federation (including `federation_allowlist` and
`instance_announce`), the reader, invites, default visibility, update
checks, and whether image uploads are on and how large they may be. Every
server sharing the database uses the same values. A change made on one
takes effect on the others with their next request. Turning image uploads off
also stops serving images already uploaded, until they are turned back on.

## Changing settings

From the browser: **Admin → Settings**.

From a shell, e.g. on a headless install or with `docker compose exec`:

    writefreely settings list
    writefreely settings get  app.private
    writefreely settings set  app.private true
    writefreely settings export

`set` refuses a `config.ini` key and says so. `list`, `get` and `export` only
read: before the first import they show what `config.ini` says and leave it
alone.

The `settings` subcommands print only data on stdout (log lines go to
stderr), so their output can be piped or redirected. The `-c` flag must
come before the subcommand: `writefreely -c path/to/config.ini settings get app.site_name`.

## Upgrading

The first time a new version starts (or runs `writefreely db migrate`), it
copies every database setting your instance was running with out of
`config.ini` into the database, checks the copy, and removes those keys
from `config.ini`. No backup of the old file is kept. Every other server
sharing the database does the same with its own file. If its values
disagreed with the database, it logs which ones, and the database's
values win. An imported value that fails validation (a key that was absent,
say, and so read as zero) is stored as the default instead, with one logged
line per key.

If `config.ini` is read-only (a Docker `:ro` mount, a file owned by
configuration management), WriteFreely logs the keys to remove by hand and
starts anyway. Values left in the file are ignored. If `config.ini` is a
symlink, the file it points to is the one rewritten.

## Downgrading

An older version reads only `config.ini`. So does an old node that is still
running against the same `config.ini` during a rolling upgrade. Neither reads
any of the moved settings; they run with Go's zero values, not your values.
A private instance comes back **public** until the settings are back in the
file. Before downgrading, run

    writefreely settings export >> config.ini

and check the result.

To stop a new version from running on defaults by mistake, the first import
adds a line to the top of `config.ini`:

    ; Community settings live in the database. Do not remove this line; see docs/settings.md.
    settings_location = database

Older versions ignore it. A new version that finds it, but a database holding
no settings (say, one restored from a backup taken before the upgrade), stops
with an error instead of importing the file. Restore the database, or paste
`settings export` output from a current database into `config.ini` and delete
the `settings_location` line, and it imports again.
