#!/bin/sh
###############################################################################
##         WriteFreely (Wisp Edition) switch script                          ##
##                                                                           ##
##     Moves an existing upstream WriteFreely instance onto this fork.       ##
##     It does not download anything and it does not touch the database      ##
##     beyond running the migration the fork adds.                           ##
##                                                                           ##
##     usage: switch-from-writefreely.sh [--docker] [options]                ##
###############################################################################
#
#	Copyright © 2026 Joseph Quigley.
#
#	This file is part of WriteFreely.
#
#	WriteFreely is free software: you can redistribute it and/or modify
#	it under the terms of the GNU Affero General Public License, included
#	in the LICENSE file in this source code package.
#
# The switch is a drop-in: same binary name, same config file, and this
# edition's own migrations (wisp_v1 on, starting with post_images). What
# differs is where a container keeps its state. Upstream's image works out
# of /go, with the config bind mounted as a single file and the keys in a
# named volume. This image works out of /data, where one directory holds
# the config, the keys, the SQLite database if there is one, and uploads.
#
# So the docker half of this script is a file move, and the bare metal half
# is a migration and a restart.

set -eu

mode=bare
config=""
state_dir="./data"
keys_dir=""
keys_volume=""
uploads=ask
uploads_dir=""
assume_yes=false
keep_originals=false

# Paths the run superseded, reported at the end so nobody has to guess
# which config or which database is the live one.
leftovers=""

usage() {
	cat <<'EOF'
Switch an upstream WriteFreely instance to WriteFreely (Wisp Edition).

usage: switch-from-writefreely.sh [--docker] [options]

Bare metal (the default) runs from the installation root, the directory
holding the writefreely binary, config.ini and keys. Install this fork's
binary, templates, static and pages over the old ones first, then run this
to migrate the database and enable what the fork adds.

Docker (--docker) runs from the directory holding your compose file. It
gathers the config, the keys and any SQLite database into one state
directory for the new image to bind mount, then prints the compose changes
to make. It never edits your compose file or starts containers.

Nothing is deleted. What the run supersedes is renamed with a .pre-wisp
suffix and listed at the end, so the old copies remain available to roll
back to without leaving a second file that answers to config.ini.

Options:
  --docker              Container install rather than bare metal
  --config FILE         Config file (default: ./config.ini)
  --state-dir DIR       Docker only: state directory to build
                        (default: ./data)
  --keys-dir DIR        Docker only: read the existing keys from this
                        directory rather than a named volume
  --keys-volume NAME    Docker only: read the existing keys from this
                        named volume. Needs the docker CLI. Autodetected
                        when only one volume looks like a keys volume
  --uploads             Enable image uploads without asking
  --no-uploads          Leave image uploads alone without asking
  --uploads-dir DIR     Directory uploads are written to. Defaults to
                        /data/uploads under --docker, and to the built-in
                        location otherwise
  --keep-originals      Docker only: leave the superseded config and
                        database under their own names. By default they
                        are renamed with a .pre-wisp suffix, so that only
                        the live copy answers to config.ini
  --yes, -y             Do not ask for confirmation before making changes
  --help, -h            This text
EOF
}

say() {
	echo "==> $*"
}

warn() {
	echo "    $*" >&2
}

die() {
	echo "switch-from-writefreely: $*" >&2
	exit 1
}

# Ask a yes/no question. Answers yes when --yes was passed, and no when
# there is no terminal to ask at, so an unattended run never blocks and
# never guesses in the affirmative.
confirm() {
	local reply

	if [ "$assume_yes" = true ]; then
		return 0
	fi

	if [ ! -t 0 ]; then
		warn "not a terminal, assuming no: $1"
		return 1
	fi

	printf '%s [y/N] ' "$1"
	read -r reply
	case "$reply" in
		[Yy] | [Yy][Ee][Ss]) return 0 ;;
		*) return 1 ;;
	esac
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--docker) mode=docker ;;
		--config) config="${2:?--config needs a path}"; shift ;;
		--state-dir) state_dir="${2:?--state-dir needs a path}"; shift ;;
		--keys-dir) keys_dir="${2:?--keys-dir needs a path}"; shift ;;
		--keys-volume) keys_volume="${2:?--keys-volume needs a name}"; shift ;;
		--uploads) uploads=yes ;;
		--no-uploads) uploads=no ;;
		--uploads-dir) uploads_dir="${2:?--uploads-dir needs a path}"; shift ;;
		--keep-originals) keep_originals=true ;;
		-y | --yes) assume_yes=true ;;
		-h | --help) usage; exit 0 ;;
		*) die "unknown option: $1. Try --help" ;;
	esac
	shift
done

: "${config:=./config.ini}"

# Append an [uploads] section. Sections are appended rather than edited,
# because rewriting an existing one would discard whatever the operator
# put in it. An [uploads] section already present is left as it is.
enable_uploads() {
	local file=$1
	local dir=$2

	if grep -q '^[[:space:]]*\[uploads\]' "$file"; then
		say "[uploads] already in $file, leaving it alone"
		return 0
	fi

	{
		echo ""
		echo "[uploads]"
		echo "enabled     = true"
		echo "max_size_mb = 10"
		if [ -n "$dir" ]; then
			echo "dir         = $dir"
		fi
	} >>"$file"

	say "enabled image uploads in $file"
	if [ -z "$dir" ]; then
		warn "uploads land under the static asset tree. Set [uploads] dir"
		warn "to somewhere outside it if your assets are replaced on upgrade."
	fi
}

# Ask about uploads unless a flag already answered. Uploads are off by
# default in a fresh config, so an instance that switches over keeps them
# off until someone says otherwise.
maybe_enable_uploads() {
	local file=$1
	local dir=$2

	case "$uploads" in
		no) return 0 ;;
		yes) enable_uploads "$file" "$dir" ;;
		ask)
			echo ""
			echo "This edition can accept image uploads from the editor."
			echo "Files are stored on disk under the day they were uploaded,"
			echo "and a size limit of 10 MB per file applies."
			if confirm "Enable image uploads?"; then
				enable_uploads "$file" "$dir"
			else
				say "leaving image uploads disabled"
			fi
			;;
	esac
}

###############################################################################
# Bare metal
###############################################################################

switch_bare_metal() {
	local version

	[ -f "$config" ] || die "no $config here. Run this from the installation root, or pass --config"
	[ -x ./writefreely ] || die "no writefreely binary here. Run this from the installation root"
	[ -f keys/email.aes256 ] || die "no keys/email.aes256 here. This does not look like an existing instance"

	# The migration this script runs is the fork's, so the fork's binary
	# has to be in place already. Running upstream's --migrate would be a
	# no-op that looks like success.
	version=$(./writefreely -v 2>/dev/null || true)
	case "$version" in
		*"Wisp Edition"*) say "found $version" ;;
		"") die "./writefreely -v printed nothing. Is the binary runnable?" ;;
		*)
			warn "the binary here reports: $version"
			die "install this fork's writefreely, templates, static and pages first, then run this again"
			;;
	esac

	echo ""
	echo "About to migrate the database this instance is configured to use."
	echo "Migrations are not reversible. Back up the database and the keys"
	echo "directory before continuing."
	echo ""
	confirm "Database and keys backed up, and the service stopped?" ||
		die "nothing changed"

	say "applying migrations"
	./writefreely --migrate

	maybe_enable_uploads "$config" "$uploads_dir"

	echo ""
	say "done. Start the service again, for example:"
	echo "      systemctl start writefreely"
}

###############################################################################
# Docker
###############################################################################

# Path of a database left behind by move_sqlite_database, if any.
superseded_db=""

# Say plainly which files are live and which are not. Two files named
# config.ini, one directory apart, is a coin flip six months from now, and
# the losing side of that coin flip fails silently.
report_leftovers() {
	[ -n "$leftovers" ] || return 0

	echo ""
	echo "==> live from here on:"
	echo ""
	echo "      $state_dir/config.ini"
	if grep -qi '^[[:space:]]*type[[:space:]]*=[[:space:]]*sqlite3' "$state_dir/config.ini"; then
		echo "      $(sed -n 's/^[[:space:]]*filename[[:space:]]*=[[:space:]]*//p' "$state_dir/config.ini" | head -n 1) (inside the container)"
	fi
	echo ""
	echo "    superseded, read by nothing, kept so you can roll back:"
	echo ""
	printf '%s' "$leftovers" | while IFS='	' read -r path why; do
		echo "      $path"
		echo "          $why"
	done
	echo ""
	echo "    The superseded config still contains whatever credentials it"
	echo "    always did. Rotating them means editing the live copy, and"
	echo "    deleting this one once you no longer want the rollback."
}

# Find the volume holding the keys. Upstream's compose mounts a named
# volume at /go/keys, prefixed with the compose project name, so the name
# varies by install and is only guessable when exactly one candidate
# exists.
detect_keys_volume() {
	local candidates

	command -v docker >/dev/null 2>&1 || return 1

	candidates=$(docker volume ls --format '{{.Name}}' 2>/dev/null | grep -i 'keys' || true)
	[ "$(echo "$candidates" | grep -c .)" -eq 1 ] || return 1

	echo "$candidates"
}

# Copy the keys into the state directory. Regenerating them instead would
# invalidate every session and every value encrypted with them, so this is
# the step that must not be skipped.
copy_keys() {
	local dest="$state_dir/keys"

	if [ -f "$dest/email.aes256" ]; then
		say "keys already in $dest"
		return 0
	fi

	if [ -n "$keys_dir" ]; then
		[ -f "$keys_dir/email.aes256" ] ||
			die "no email.aes256 in $keys_dir. Is that the keys directory?"
		mkdir -p "$dest"
		cp -a "$keys_dir/." "$dest/"
		say "copied keys from $keys_dir"
		return 0
	fi

	if [ -z "$keys_volume" ]; then
		keys_volume=$(detect_keys_volume || true)
	fi

	if [ -z "$keys_volume" ]; then
		die "cannot find the existing keys. Pass --keys-volume NAME (docker volume ls), or mount them and pass --keys-dir"
	fi

	command -v docker >/dev/null 2>&1 ||
		die "no docker CLI here to read volume $keys_volume. Mount the volume and pass --keys-dir instead"

	say "copying keys from volume $keys_volume"
	mkdir -p "$dest"
	docker run --rm \
		-v "$keys_volume":/from:ro \
		-v "$(cd "$dest" && pwd)":/to \
		alpine:3.22 sh -c 'cp -a /from/. /to/' ||
		die "could not read volume $keys_volume"
}

# Rename a file the run has superseded, so that only the live copy answers
# to its name. The copy stays on disk under the new name: it is the
# rollback path, and in the config's case it still holds credentials.
#
# Renaming rather than leaving it is what stops an operator editing a dead
# config for an hour, and what stops a rolled-back compose file quietly
# starting on stale data. Both files being called config.ini, one directory
# apart, is the whole problem.
supersede() {
	local file=$1
	local why=$2
	local dest="$file.pre-wisp"

	[ -e "$file" ] || return 0

	if [ "$keep_originals" = true ]; then
		note_leftover "$file" "$why, left under its own name"
		return 0
	fi

	if [ -e "$dest" ]; then
		note_leftover "$file" "$why, and $dest already exists so this was left alone"
		return 0
	fi

	mv "$file" "$dest"
	say "renamed $file to $dest"
	note_leftover "$dest" "$why"
}

# Record a path for the closing summary.
note_leftover() {
	leftovers="$leftovers$1	$2
"
}

# Move a rewritten "$file.new" back over "$file", keeping the original
# file's ownership and mode. A plain mv would install the temporary file
# instead, and with it the umask's mode, which is how a 0600 config quietly
# becomes 0644 halfway through a run.
replace_contents() {
	local file=$1

	cat "$file.new" >"$file"
	rm -f "$file.new"
}

# Set a configuration key that has no value, leaving one that does alone.
set_config_value() {
	local file=$1
	local key=$2
	local value=$3

	grep -q "^[[:space:]]*$key[[:space:]]*=[[:space:]]*[^[:space:]]" "$file" && return 0

	if grep -q "^[[:space:]]*$key[[:space:]]*=" "$file"; then
		sed "s|^\([[:space:]]*$key[[:space:]]*=[[:space:]]*\).*|\1$value|" \
			"$file" >"$file.new"
		replace_contents "$file"
	else
		# No key to rewrite. It belongs to [server], which every config
		# opens with, so append it under that heading.
		awk -v line="$key = $value" '
			/^[[:space:]]*\[server\][[:space:]]*$/ && !done { print; print line; done = 1; next }
			{ print }
			END { if (!done) { print "[server]"; print line } }
		' "$file" >"$file.new"
		replace_contents "$file"
	fi

	say "set $key to $value"
}

# Point the asset directories at the tree inside the image. A config
# written for upstream's image leaves them empty, because there the assets
# sit in the working directory beside the binary. Here the working
# directory is the state directory, so an empty value resolves to a
# directory holding no templates and the server exits on start.
#
# Recent images fill these in themselves when they are empty, but writing
# them makes the switch work on an image published before that, and makes
# the layout visible in the file rather than implied.
set_asset_dirs() {
	local file=$1
	local dir="${WRITEFREELY_DOCKER_PARENT_DIR:-/usr/share/writefreely}"

	set_config_value "$file" templates_parent_dir "$dir"
	set_config_value "$file" static_parent_dir "$dir"
	set_config_value "$file" pages_parent_dir "$dir"
}

# Find the SQLite database the config names. The path is whatever the old
# container resolved it against, so a relative one such as
# "db/writefreely.db" is relative to that container's working directory,
# not to the host directory this script runs in. The bind mount that
# supplied it is normally somewhere under the state directory, so look
# there too before giving up.
find_sqlite_file() {
	local file=$1
	local candidate

	for candidate in "$file" "$state_dir/$file" "$state_dir/$(basename "$file")"; do
		if [ -f "$candidate" ]; then
			echo "$candidate"
			return 0
		fi
	done

	return 1
}

# Point the config at the path the database will have inside the
# container, moving the file into the state directory when it is not
# already there. A MySQL config needs none of this: the database lives in
# its own service either way.
move_sqlite_database() {
	local file found found_abs state_abs rel

	grep -qi '^[[:space:]]*type[[:space:]]*=[[:space:]]*sqlite3' "$state_dir/config.ini" || return 0

	file=$(sed -n 's/^[[:space:]]*filename[[:space:]]*=[[:space:]]*//p' "$state_dir/config.ini" | head -n 1)
	[ -n "$file" ] || die "config says sqlite3 but names no filename"

	found=$(find_sqlite_file "$file") || die "cannot find the SQLite database that config.ini names as $file. Looked in $state_dir too. Copy it into $state_dir yourself and edit filename"

	state_abs=$(cd "$state_dir" && pwd)
	found_abs=$(cd "$(dirname "$found")" && pwd)/$(basename "$found")

	case "$found_abs" in
		"$state_abs"/*)
			# Already inside what becomes the bind mount, so it is
			# already where the container will look. Copying it again
			# would leave two databases and no way to tell which one
			# the instance is writing to.
			rel=${found_abs#"$state_abs"/}
			say "database already in $state_dir at $rel"
			;;
		*)
			rel=$(basename "$file")
			cp -a "$found_abs" "$state_abs/$rel"
			say "copied $found into $state_dir"
			superseded_db=$found_abs
			;;
	esac

	# Rewritten through a temporary file rather than sed -i, whose
	# spelling differs between GNU, BSD and busybox.
	sed "s|^\([[:space:]]*filename[[:space:]]*=[[:space:]]*\).*|\1/data/$rel|" \
		"$state_dir/config.ini" >"$state_dir/config.ini.new"
	replace_contents "$state_dir/config.ini"
	say "pointed filename at /data/$rel"
}

# Match the ownership the containers run as, so the bind mount does not
# need a chown later. Failing is not fatal: the operator may be on Docker
# Desktop, where it makes no difference, or running this inside a
# container that cannot chown a host directory.
own_state_dir() {
	local puid pgid

	puid=$(sed -n 's/^PUID=//p' .env 2>/dev/null | head -n 1)
	pgid=$(sed -n 's/^PGID=//p' .env 2>/dev/null | head -n 1)

	# Inside a container the current ids are the container's, not the
	# operator's, and root is the usual case. Handing a bind-mounted
	# directory to root is worse than leaving it alone, so only .env
	# answers the question here.
	if [ -z "$puid" ] && [ -f /.dockerenv ] && [ "$(id -u)" = 0 ]; then
		warn "no .env here and this is running as root in a container."
		warn "Leaving ownership of $state_dir alone. Re-run with"
		warn "--user \"\$(id -u):\$(id -g)\" if the files came out wrong."
		return 0
	fi

	: "${puid:=$(id -u)}"
	: "${pgid:=$(id -g)}"

	if chown -R "$puid:$pgid" "$state_dir" 2>/dev/null; then
		say "state directory owned by $puid:$pgid"
	else
		warn "could not chown $state_dir to $puid:$pgid. Do it yourself if"
		warn "the container reports permission denied on start."
	fi
}

switch_docker() {
	# A second run, after the first renamed the original out of the way.
	# The state directory is the config now, so work from it rather than
	# failing on a file this script itself moved.
	if [ ! -f "$config" ] && [ -f "$state_dir/config.ini" ]; then
		say "$config is gone and $state_dir/config.ini is here, so this looks switched already"
		config="$state_dir/config.ini"
	fi

	[ -f "$config" ] || die "no $config here. Run this from the directory holding your compose file, or pass --config"

	echo ""
	echo "This gathers $config, the keys and any SQLite database into"
	echo "$state_dir, which the new image bind mounts at /data."
	echo "Nothing is deleted and your compose file is not edited. What it"
	echo "supersedes is renamed with a .pre-wisp suffix and listed at the end."
	echo ""
	echo "Stop the stack and back up the database before continuing."
	echo ""
	confirm "Stack stopped, and database backed up?" || die "nothing changed"

	mkdir -p "$state_dir"

	if [ "$config" = "$state_dir/config.ini" ]; then
		say "working directly on $state_dir/config.ini"
	elif [ -f "$state_dir/config.ini" ]; then
		say "config already in $state_dir"
	else
		cp -a "$config" "$state_dir/config.ini"
		say "copied $config into $state_dir"
	fi


	copy_keys
	set_asset_dirs "$state_dir/config.ini"
	move_sqlite_database

	: "${uploads_dir:=/data/uploads}"
	maybe_enable_uploads "$state_dir/config.ini" "$uploads_dir"
	if grep -q '^[[:space:]]*\[uploads\]' "$state_dir/config.ini"; then
		mkdir -p "$state_dir/uploads"
	fi

	# cp -a preserves the source mode, and a config generated by other
	# tooling is often 0644. This file holds the mail and OAuth
	# credentials, so it should be no more readable than the keys beside
	# it, which are 0600.
	chmod 0600 "$state_dir/config.ini"
	case "$(ls -l "$state_dir/config.ini" | cut -c1-10)" in
		-rw-------) say "$state_dir/config.ini is 0600, matching the keys beside it" ;;
		*)
			# Some bind mounts, Docker Desktop's among them, report a
			# fixed mode and ignore chmod. Saying so is better than
			# claiming a permission that is not there.
			warn "could not restrict $state_dir/config.ini to 0600. It holds"
			warn "your mail and OAuth credentials, so check its permissions"
			warn "on the host if this is not a bind mount that fakes them."
			;;
	esac

	own_state_dir

	if [ "$config" != "$state_dir/config.ini" ]; then
		supersede "$config" "the config the old image bind mounted, still holding your credentials"
		[ -f "$config.pre-wisp" ] && chmod 0600 "$config.pre-wisp"
	fi
	if [ -n "$superseded_db" ]; then
		supersede "$superseded_db" "the database as it was before the switch, at schema V17"
	fi

	report_leftovers

	cat <<EOF

==> done. Point the app service at the new image and the new mount:

      writefreely-web:
        image: ghcr.io/josephquigley/writefreely-wisp:latest
        user: "\${PUID:-1000}:\${PGID:-1000}"
        volumes:
          - $state_dir:/data

    Remove the old keys volume and the config.ini file mount from that
    service. Leave the database service alone. Then:

      docker compose up -d

    The entrypoint applies the migration this edition adds. See
    docs/docker.md for the rest.
EOF
}

case "$mode" in
	bare) switch_bare_metal ;;
	docker) switch_docker ;;
esac
