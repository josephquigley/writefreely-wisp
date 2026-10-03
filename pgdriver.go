/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/writefreely/writefreely/config"
)

// driverPostgresRebind is the database/sql driver name to open Postgres
// with: sql.Open(driverPostgresRebind, postgresDSN(cfg)).
//
// It is lib/pq's driver with one change: every query text passed to
// Prepare, Exec or Query has its `?` placeholders rewritten to `$1…$n` (see
// rebindPostgres). Doing this at the driver, rather than on datastore,
// covers every path to the database: datastore methods, *sql.Tx from
// Begin, wf_db.RunTransactionWithOptions, and the migrations package.
//
// datastore.driverName stays driverPostgres ("postgres"); this name is only
// what sql.Open is given.
const driverPostgresRebind = "postgres-rebind"

func init() {
	sql.Register(driverPostgresRebind, rebindDriver{})
}

// postgresDSN builds a lib/pq connection URL from the [database] section.
// It uses host, port, username, password, database and tls; tls = true maps
// to sslmode=require and false to sslmode=disable. The session time zone is
// pinned to UTC (WFPG-06 relies on it).
func postgresDSN(c config.DatabaseCfg) string {
	port := c.Port
	if port == 0 {
		port = 5432
	}
	sslmode := "disable"
	if c.TLS {
		sslmode = "require"
	}
	q := url.Values{}
	q.Set("sslmode", sslmode)
	q.Set("timezone", "UTC")
	q.Set("application_name", "writefreely")
	u := url.URL{
		Scheme:   "postgres",
		Host:     net.JoinHostPort(c.Host, strconv.Itoa(port)),
		Path:     "/" + c.Database,
		RawQuery: q.Encode(),
	}
	if c.User != "" {
		u.User = url.UserPassword(c.User, c.Password)
	}
	return u.String()
}

// rebindPostgres rewrites `?` placeholders to Postgres' `$1…$n`, in order.
//
// A `?` is left alone inside a single-quoted string ('…', where a doubled
// quote is an escaped quote, and backslash escapes in E'…' strings), a double-quoted
// identifier ("…"), a dollar-quoted string ($$…$$ or $tag$…$tag$), a `--`
// line comment, or a /* … */ block comment (which Postgres lets nest).
//
// Postgres' jsonb operators `?`, `?|` and `?&` cannot be written through
// this driver; use jsonb_exists() and friends instead.
func rebindPostgres(query string) string {
	if strings.IndexByte(query, '?') < 0 {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	for i := 0; i < len(query); {
		c := query[i]
		switch {
		case c == '\'':
			escapes := i > 0 && (query[i-1] == 'e' || query[i-1] == 'E') && (i == 1 || !isSQLIdentByte(query[i-2]))
			j := skipQuoted(query, i, '\'', escapes)
			b.WriteString(query[i:j])
			i = j
		case c == '"':
			j := skipQuoted(query, i, '"', false)
			b.WriteString(query[i:j])
			i = j
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			j := strings.IndexByte(query[i:], '\n')
			if j < 0 {
				j = len(query)
			} else {
				j += i + 1
			}
			b.WriteString(query[i:j])
			i = j
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			j := skipBlockComment(query, i)
			b.WriteString(query[i:j])
			i = j
		case c == '$' && (i == 0 || !isSQLIdentByte(query[i-1])):
			tag := dollarQuoteTag(query, i)
			if tag == "" {
				b.WriteByte(c)
				i++
				break
			}
			j := strings.Index(query[i+len(tag):], tag)
			if j < 0 {
				j = len(query)
			} else {
				j = i + len(tag) + j + len(tag)
			}
			b.WriteString(query[i:j])
			i = j
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// skipQuoted returns the index just past the quoted run that starts at
// query[start] == quote. A doubled quote is an escaped quote. With
// backslashEscapes, a backslash escapes the next byte. An unterminated run
// extends to the end of the query.
func skipQuoted(query string, start int, quote byte, backslashEscapes bool) int {
	for j := start + 1; j < len(query); j++ {
		switch query[j] {
		case '\\':
			if backslashEscapes {
				j++
			}
		case quote:
			if j+1 < len(query) && query[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(query)
}

// skipBlockComment returns the index just past the (possibly nested)
// /* … */ comment starting at query[start].
func skipBlockComment(query string, start int) int {
	depth := 0
	for j := start; j < len(query)-1; j++ {
		switch {
		case query[j] == '/' && query[j+1] == '*':
			depth++
			j++
		case query[j] == '*' && query[j+1] == '/':
			depth--
			j++
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(query)
}

// dollarQuoteTag returns the opening tag ("$$" or "$name$") of a
// dollar-quoted string starting at query[start], or "" if there is none
// (for example, a positional parameter like $1).
func dollarQuoteTag(query string, start int) string {
	j := start + 1
	if j < len(query) && query[j] == '$' {
		return "$$"
	}
	if j >= len(query) || !(query[j] == '_' || isASCIILetter(query[j]) || query[j] >= 0x80) {
		return ""
	}
	for j < len(query) && (isSQLIdentByte(query[j]) && query[j] != '$') {
		j++
	}
	if j < len(query) && query[j] == '$' {
		return query[start : j+1]
	}
	return ""
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isSQLIdentByte(c byte) bool {
	return isASCIILetter(c) || (c >= '0' && c <= '9') || c == '_' || c == '$' || c >= 0x80
}

// rebindDriver is registered as driverPostgresRebind. It opens connections
// through lib/pq's connector and wraps each one in a rebindConn.
type rebindDriver struct{}

var (
	_ driver.Driver        = rebindDriver{}
	_ driver.DriverContext = rebindDriver{}
)

func (d rebindDriver) Open(name string) (driver.Conn, error) {
	c, err := d.OpenConnector(name)
	if err != nil {
		return nil, err
	}
	return c.Connect(context.Background())
}

// OpenConnector parses name with pqConfigFromDSN and opens it with
// pq.NewConnectorConfig. pq.NewConnector is not used: it also reads the PG*
// environment variables and refuses to start on some of them (PGGSSENCMODE,
// PGREQUIRESSL, PGCHANNELBINDING, PGSSLCRL, or a PGSERVICE without a service
// file), which a host may export for psql. The [database] section is the
// whole configuration.
func (rebindDriver) OpenConnector(name string) (driver.Connector, error) {
	cfg, err := pqConfigFromDSN(name)
	if err != nil {
		return nil, err
	}
	inner, err := pq.NewConnectorConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &rebindConnector{inner: inner}, nil
}

// pqConfigFromDSN parses a postgres:// URL, as postgresDSN builds, into a
// pq.Config the way pq.NewConfig would, but without the environment.
// sslmode, application_name and connect_timeout (seconds) map to their
// fields; every other query parameter, such as timezone, is sent as a
// session setting. A missing sslmode is lib/pq's default, require.
func pqConfigFromDSN(dsn string) (pq.Config, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return pq.Config{}, fmt.Errorf("%s: the DSN must be a postgres:// URL", driverPostgresRebind)
	}
	cfg := pq.Config{
		Host: "localhost", Port: 5432, SSLSNI: true,
		// NewConfig's defaults; lib/pq parses timestamps as ISO text in UTF-8.
		ClientEncoding: "UTF8", Datestyle: "ISO, MDY",
		Runtime: map[string]string{},
	}
	if h := u.Hostname(); h != "" {
		cfg.Host = h
	}
	if p := u.Port(); p != "" {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return pq.Config{}, fmt.Errorf("%s: invalid port %q", driverPostgresRebind, p)
		}
		cfg.Port = uint16(n)
	}
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Password, _ = u.User.Password()
	}
	cfg.Database = strings.TrimPrefix(u.Path, "/")
	for k, vs := range u.Query() {
		v := vs[len(vs)-1]
		switch k {
		case "sslmode":
			cfg.SSLMode = pq.SSLMode(v)
		case "application_name":
			cfg.ApplicationName = v
		case "connect_timeout":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return pq.Config{}, fmt.Errorf("%s: invalid connect_timeout %q", driverPostgresRebind, v)
			}
			cfg.ConnectTimeout = time.Duration(n) * time.Second
		default:
			cfg.Runtime[k] = v
		}
	}
	return cfg, nil
}

type rebindConnector struct {
	inner driver.Connector
}

var _ driver.Connector = (*rebindConnector)(nil)

// pqConn is every driver interface rebindConn forwards. lib/pq's connection
// type is unexported, so it is held through this interface; Connect refuses
// a connection that lacks any of them rather than silently losing it.
type pqConn interface {
	driver.Conn
	driver.QueryerContext
	driver.ExecerContext
	driver.ConnPrepareContext
	driver.ConnBeginTx
	driver.NamedValueChecker
	driver.Pinger
	driver.SessionResetter
	driver.Validator
}

// Connect opens a lib/pq connection. Timestamps need nothing here: lib/pq
// scans timestamptz into the session TimeZone, and postgresDSN pins that to
// UTC, so values arrive in time.UTC (WFPG-06), as SQLite values already do.
// Post.Created8601 and friends format with a literal "Z", so a non-UTC value
// would render the wrong instant. TestPostgresTimeIsUTCWhateverTheEnvironment
// holds this.
func (c *rebindConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	pc, ok := conn.(pqConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("%s: lib/pq returned %T, which lacks a driver interface rebindConn forwards", driverPostgresRebind, conn)
	}
	return &rebindConn{inner: pc}, nil
}

func (c *rebindConnector) Driver() driver.Driver { return rebindDriver{} }

// rebindConn wraps a lib/pq connection, rebinding placeholders on the way
// in. database/sql discovers a connection's capabilities by type assertion,
// so every optional interface lib/pq's connection implements must be
// forwarded here, or its behaviour is silently lost.
// TestPostgresRebindConnForwards checks that list against lib/pq.
//
// Code that needs the raw lib/pq connection through sql.Conn.Raw gets a
// *rebindConn; call Unwrap on it.
type rebindConn struct {
	inner pqConn
}

var (
	_ driver.Conn               = (*rebindConn)(nil)
	_ driver.QueryerContext     = (*rebindConn)(nil)
	_ driver.ExecerContext      = (*rebindConn)(nil)
	_ driver.ConnPrepareContext = (*rebindConn)(nil)
	_ driver.ConnBeginTx        = (*rebindConn)(nil)
	_ driver.NamedValueChecker  = (*rebindConn)(nil)
	_ driver.Pinger             = (*rebindConn)(nil)
	_ driver.SessionResetter    = (*rebindConn)(nil)
	_ driver.Validator          = (*rebindConn)(nil)
)

// Unwrap returns the wrapped lib/pq connection.
func (c *rebindConn) Unwrap() driver.Conn { return c.inner }

func (c *rebindConn) Prepare(query string) (driver.Stmt, error) {
	return c.inner.Prepare(rebindPostgres(query))
}

func (c *rebindConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.inner.PrepareContext(ctx, rebindPostgres(query))
}

func (c *rebindConn) Close() error { return c.inner.Close() }

// Begin is required by driver.Conn; database/sql calls BeginTx instead.
func (c *rebindConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *rebindConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.inner.BeginTx(ctx, opts)
}

func (c *rebindConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.inner.ExecContext(ctx, rebindPostgres(query), args)
}

func (c *rebindConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.inner.QueryContext(ctx, rebindPostgres(query), args)
}

func (c *rebindConn) Ping(ctx context.Context) error { return c.inner.Ping(ctx) }

func (c *rebindConn) CheckNamedValue(nv *driver.NamedValue) error {
	return c.inner.CheckNamedValue(nv)
}

func (c *rebindConn) ResetSession(ctx context.Context) error {
	return c.inner.ResetSession(ctx)
}

// IsValid forwards to lib/pq, which reports false once the connection has
// seen a fatal error, so database/sql drops it instead of reusing it.
func (c *rebindConn) IsValid() bool { return c.inner.IsValid() }
