# Settings

WriteFreely keeps its configuration in two places.

**`config.ini` holds infrastructure and credentials**: everything in
`[server]`, `[database]`, `[email]` and every `[oauth.*]` section, plus
`[app] host`, `[app] single_user`, `[app] disable_password_auth` and
`[uploads] dir`. A running server only reads this file. Change it, then
restart. Any value may be a `${VARIABLE}` reference to the environment.

**The database holds everything else**: site name and description,
appearance, registration, federation (including `federation_allowlist`,
`authorized_fetch` and `instance_announce`), the reader, invites, default
visibility, update checks, and whether image uploads are on and how large
they may be. Every
server sharing the database uses the same values. A change made on one
takes effect on the others with their next request. Turning image uploads off
also stops serving images already uploaded, until they are turned back on.

A deployment can instead pin chosen database settings to `config.ini`; see
[Locking settings in config.ini](#locking-settings-in-configini).

## Changing settings

From the browser: **Admin → Settings**.

From a shell, e.g. on a headless install or with `docker compose exec`:

    writefreely settings list
    writefreely settings get  app.private
    writefreely settings set  app.private true
    writefreely settings export

`set` refuses a `config.ini` key, or a locked setting, and says so. `list`, `get` and `export` only
read: before the first import they show what `config.ini` says and leave it
alone. If `config.ini` carries the `settings_location = database` marker but
the database holds no settings (see Upgrading), they refuse instead, the same
way the server does.

A database setting cannot be exactly a `${VARIABLE}` reference: such a value
would be read as one if the export were pasted back into `config.ini`. Text
that merely contains `${...}` is fine.

If two admins have the settings page open and one saves, the other's save is
refused with "Settings were changed elsewhere since this page was loaded".
Reload the page and save again. `writefreely settings set` always applies.

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
line per key. Settings named in `locked_settings` are left out of all of
this: never imported, never removed, never reported as disagreeing.

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

## Locking settings in config.ini

A deployment that writes `config.ini` from its own source (a YAML file,
configuration management) can make chosen database settings follow that
file on every start, out of reach of the admin page. Name them in
`locked_settings`, at the top of the file beside `settings_location`,
before any `[section]` header:

    locked_settings = app.private, app.federation_allowlist, app.authorized_fetch

    [app]
    host                 = https://blog.example
    private              = true
    federation_allowlist = talk.example, *.peers.example
    authorized_fetch     = true

Entries are the names `writefreely settings list` prints, separated by
commas. Spaces around an entry are ignored, an empty entry is skipped, and
names must match exactly, case included. `locked_settings` is itself a
`config.ini` key: a running server never writes it.

A locked setting:

* is read from `config.ini` every time WriteFreely starts. To change it,
  edit the file and restart.
* is never moved into the database, never removed from `config.ini`, and
  never reported as disagreeing with the database.
* wins over whatever the database holds. A value already stored for it stays
  in the database, unused. Take the name off the list and restart, and the
  stored value is in force again; the key left in `config.ini` is then an
  ordinary setting, removed from the file or reported as disagreeing like any
  other.
* cannot be changed while locked. `writefreely settings set` refuses it with
  `<name> is locked: config.ini sets it`. The admin page shows it greyed
  out, with "Set in config.ini; change it there.", and a submitted form that
  changes it anyway is refused with the same message and saves nothing.
* is marked `(locked in config.ini)` by `settings list`. `settings get`
  prints its value, and says on stderr that it is locked. `settings export`
  leaves it out, with a comment naming it, because `config.ini` already holds
  it; pasting the export back adds nothing that clashes.

A locked setting whose key is missing from `config.ini` takes the value the
instance would run with without that key, which is also what an import would
store: Go's zero value (false, empty, 0), except `local_timeline`, which is
true, and `theme`, which is `write`. Where the zero is not a valid value
(`min_username_len`, `uploads.max_size_mb`), it is the default. Write out
every key you lock rather than relying on this.

WriteFreely refuses to start, and `writefreely db migrate` and the `settings`
commands refuse to run, with an error naming the entry, when the list names:

* a setting that does not exist (`app.privat`), or
* a `config.ini` key such as `app.host` or anything in `[database]`, which
  lives only in the file already.

It also refuses when a locked key in the file holds a value that does not
parse or that `settings set` would refuse (`private = maybe`,
`min_username_len = 0`). Combinations are checked the same way as a save: a
locked `federation_allowlist` on an instance whose `private` is false stops
startup.

Every server reads its own `config.ini`. Servers sharing one database should
carry the same `locked_settings` line and the same values. Nothing compares
them across servers; each serves what its own file says.

A read-only `config.ini` that holds only locked database settings (plus
bootstrap keys) has nothing to remove, so the "could not remove" warning of
[Upgrading](#upgrading) does not appear. Until the file carries the
`settings_location = database` line, WriteFreely still logs at each start
that it could not add it. That is a warning only. Add the line once the
database holds the settings, not before: on an empty database it stops
startup, as described under Downgrading.
