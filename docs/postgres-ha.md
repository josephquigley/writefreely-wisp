# Running on a highly available Postgres

This guide covers running WriteFreely against a Postgres cluster that fails
over between nodes, such as one managed by [Patroni](https://patroni.readthedocs.io/).
It assumes WriteFreely already runs on Postgres (`type = postgres` under
`[database]` in `config.ini`).

WriteFreely knows nothing about the cluster. It connects to one address, a
TCP proxy on the same machine, and the proxy sends every connection to
whichever node is currently the primary.

```
WriteFreely ──► 127.0.0.1:5000 (HAProxy) ──► current primary :5432
                                         ╲─► standby :5432 (no traffic)
```

## Connection settings

Point `[database]` at the proxy, not at a database node:

```ini
[database]
type     = postgres
host     = 127.0.0.1
port     = 5000
username = writefreely
password = ${WF_DB_PASSWORD}
database = writefreely
tls      = false
```

`tls = true` makes WriteFreely require TLS (`sslmode=require`). HAProxy in
TCP mode passes TLS through untouched, so the setting behaves the same with
or without the proxy. On a loopback proxy it protects only the hop between
the proxy and the database node, so enable it when that hop crosses a
network you do not trust.

WriteFreely ignores `PG*` environment variables such as `PGHOST`,
`PGPASSWORD` and `PGSSLMODE`: `[database]` is the whole connection
configuration.

## HAProxy

```
global
    maxconn 200

defaults
    mode tcp
    timeout connect 4s
    timeout client  30m
    timeout server  30m
    timeout check   5s

listen postgres_primary
    bind 127.0.0.1:5000
    option httpchk GET /primary
    http-check expect status 200
    default-server inter 3s fall 3 rise 2 on-marked-down shutdown-sessions
    server node1 <node1-address>:5432 maxconn 100 check port 8008
    server node2 <node2-address>:5432 maxconn 100 check port 8008
    server node3 <node3-address>:5432 maxconn 100 check port 8008
```

Two lines do the work:

- **`option httpchk GET /primary`** asks each node's Patroni REST API
  (port 8008 by default) whether it is the primary. Patroni answers `200`
  only on the node that holds the leader lock, so HAProxy routes new
  connections to exactly one node.
- **`on-marked-down shutdown-sessions`** closes every open session to a node
  as soon as it fails that check. **Do not leave it out.** Without it,
  connections that WriteFreely opened before a failover stay attached to the
  old primary. If that node has been demoted rather than stopped, it is
  still up but read-only, and every write on those connections fails with
  `cannot execute … in a read-only transaction` (SQLSTATE `25006`) until
  they close.

`inter 3s fall 3` marks a failed primary down after about nine seconds.
Shorten it to switch over sooner, at the cost of more false alarms on a
busy network.

## What WriteFreely does during a failover

- **Requests in flight fail.** A request that was writing when the primary
  went away returns an error. Expect a short window of failed writes, about
  as long as the cluster takes to elect a new primary plus the proxy's check
  interval.
- **Closed connections are replaced.** When the proxy closes a session, or
  the server ends it (for example `terminating connection due to
  administrator command`), WriteFreely discards that connection and opens a
  new one on the next request. The new connection reaches the new primary.
  No restart is needed.
- **A connection still open to a demoted node keeps failing.** WriteFreely
  does not yet drop a connection because a write was refused as read-only,
  and it sets no maximum lifetime on pooled connections. Such connections
  are only cleared by something closing them, which is why
  `on-marked-down shutdown-sessions` matters.
- **Startup does not wait for the database.** If the database is
  unreachable when WriteFreely starts, it logs `connect to DB: …` and
  exits. Run it under a supervisor that restarts it, such as a container
  restart policy or `Restart=on-failure` in systemd.

Each WriteFreely process opens at most 20 connections. Size `maxconn` on the
proxy, and Postgres' `max_connections`, for 20 times the number of
processes, plus whatever else uses the database.

## Running more than one WriteFreely process

Several processes can share one database, for example one per site in a
multi-site cluster. They must agree on everything that is not in the
database:

- **Keys.** Every process needs the same files in `keys/`. They encrypt
  sessions and stored email addresses. Copy the directory from the first
  install to every other node before that node's first start, and compare
  checksums. The Docker entrypoint generates keys when the directory is
  empty. A node started without the shared keys therefore runs with new
  ones: everyone it serves is logged out, and it cannot decrypt the email
  addresses stored by the others.
- **Uploaded images.** Images on local disk exist only on the node that
  received them. Use `[storage] type = s3` so that every node serves the
  same images; `writefreely images sync --to s3` copies existing ones.
- **Settings** live in the database (see [settings.md](settings.md)), so
  every process already sees the same values. `config.ini` holds only
  infrastructure and credentials; keep it the same on every node, except
  where a value really differs per node.
- **Background jobs.** Scheduled email publishing and the orphaned-image
  sweep take a database lock on each run, so only one process does each
  run, whichever gets there first.

## Checking a failover

On a test cluster, before relying on this setup:

1. Start WriteFreely, sign in, and publish a post.
2. Switch the primary, for example with `patronictl switchover`.
3. Within the proxy's check interval, publishing works again, with no
   restart. A few requests during the switch may fail.
4. Check the WriteFreely log: there should be no run of
   `read-only transaction` errors after the switch. If there is,
   `on-marked-down shutdown-sessions` is not in effect.
