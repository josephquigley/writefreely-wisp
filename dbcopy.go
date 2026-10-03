//go:build sqlite && !wflib
// +build sqlite,!wflib

/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

// `writefreely db copy --from sqlite:<path>` copies a SQLite database into
// a freshly initialised, empty Postgres database, converting each value to
// the type postgres.sql gives its column, and then proves the copy by
// comparing per-table row counts and order-independent checksums.
//
// It lives behind the sqlite build tag: only a build that can read SQLite
// can copy from it.
//
// What it converts, and why:
//
//   - Flags stored as 0/1 become booleans; other integers are range-checked
//     for smallint and integer.
//   - Datetimes are read as text and parsed here, explicitly, as UTC unless
//     the text carries an offset. The SQLite driver turns text it cannot
//     parse into the zero time silently; this refuses instead. Postgres keeps
//     microseconds, so values are truncated to the microsecond (pgx would
//     truncate them the same way).
//   - Binary columns (bytea in Postgres) are read with CAST(… AS BLOB), so
//     the bytes arrive as []byte whether the row stored TEXT (access tokens
//     written before WFPG-05) or BLOB.
//   - Text has NUL bytes removed and invalid UTF-8 replaced, as
//     sanitizeDBText does for new writes (WFPG-10). Values from columns that
//     were char(n) on MySQL lose trailing spaces. Free-text columns that the
//     application already truncates (boundedDBText) are truncated to their
//     Postgres width; an over-long value anywhere else refuses the copy.
//   - Subscriber emails and remote handles take the form the application
//     now stores (normalizeSubscriberEmail, normalizeRemoteHandle; WFPG-09).
//     Two subscribers of one blog whose addresses differ only in case
//     collapse into one: the confirmed row wins, then the earliest.
//   - userinvites, usersinvited and remoteuserkeys have primary keys in
//     Postgres that SQLite never had. Rows that are exact duplicates collapse
//     into one; duplicates that differ refuse the copy.
//
// Output names tables, counts and row IDs only. It never prints a value from
// a row: there are emails, tokens and post bodies in there.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"github.com/writefreely/writefreely/migrations"
)

// dbCopySchemaVersion is the only migration version this command knows how
// to copy. Source, target and binary must all be at it. A new migration
// means reviewing this command (its column rules below) and raising it.
const dbCopySchemaVersion = 20

// dbCopyBatchRows is the most rows sent in one INSERT.
const dbCopyBatchRows = 500

// dbCopyCharColumns were char(n) on MySQL. A value from one may carry
// trailing spaces, which are padding, not data.
var dbCopyCharColumns = map[string]bool{
	"collectionpasswords.password": true,
	"emailsubscribers.id":          true,
	"emailsubscribers.token":       true,
	"password_resets.token":        true,
	"post_images.post_id":          true,
	"post_images.sha256":           true,
	"posts.id":                     true,
	"posts.language":               true,
	"posts.modify_token":           true,
	"posts.text_appearance":        true,
	"remote_likes.post_id":         true,
	"userinvites.id":               true,
	"users.password":               true,
	"usersinvited.invite_id":       true,
}

// dbCopyTruncatable are the bounded columns the application itself
// truncates to fit (boundedDBText). Any other bounded column holding a value
// too long for Postgres refuses the copy: it is an ID, a key or an address,
// and cutting it would change what it refers to.
var dbCopyTruncatable = map[string]bool{
	"accesstokens.user_agent": true,
	"appcontent.title":        true,
	"collections.description": true,
	"collections.title":       true,
	"post_images.filename":    true,
	"posts.language":          true,
	"posts.title":             true,
	"remoteuserkeys.id":       true,
	"userattributes.value":    true,
}

// dbCopyNormalize maps a column to the function that gives its stored form.
var dbCopyNormalize = map[string]func(string) string{
	"emailsubscribers.email": normalizeSubscriberEmail,
	"remoteusers.handle":     normalizeRemoteHandle,
}

// dbCopyPostgresOnlyKeys are primary keys Postgres has and SQLite lacks.
var dbCopyPostgresOnlyKeys = map[string][]string{
	"remoteuserkeys": {"id"},
	"userinvites":    {"id"},
	"usersinvited":   {"invite_id", "user_id"},
}

// DBCopyOptions configures CopySQLiteDatabase.
type DBCopyOptions struct {
	// From is the source, as sqlite:<path>.
	From string
	// DryRun reads and converts everything and writes nothing.
	DryRun bool
	// VerifyOnly compares an existing copy with the source.
	VerifyOnly bool
	// Out receives the report. nil means os.Stdout.
	Out io.Writer
}

// CopySQLiteDatabase copies the SQLite database named by opts.From into the
// Postgres database the configuration names. See dbcopy.go.
func CopySQLiteDatabase(apper Apper, opts DBCopyOptions) error {
	path, err := dbCopySourcePath(opts.From)
	if err != nil {
		return err
	}
	if opts.DryRun && opts.VerifyOnly {
		return errors.New("--dry-run and --verify-only cannot be used together")
	}
	if fi, err := os.Stat(path); err != nil {
		return fmt.Errorf("source database: %v", err)
	} else if fi.IsDir() {
		return fmt.Errorf("source database %s is a directory", path)
	}

	apper.LoadConfig()
	app := apper.App()
	switch app.cfg.Database.Type {
	case driverPostgres:
	default:
		return fmt.Errorf("the configured database is %q; db copy writes to a Postgres database, so set [database] type = %s", app.cfg.Database.Type, driverPostgres)
	}

	// mode=ro: the source is only ever read, and a mistyped path fails
	// instead of creating an empty database.
	src, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open source: %v", err)
	}
	defer src.Close()
	if err := src.Ping(); err != nil {
		return fmt.Errorf("open source: %v", err)
	}

	connectToDatabase(app)
	defer shutdown(app)
	if err := app.db.Ping(); err != nil {
		return fmt.Errorf("connect to target: %v", err)
	}

	return copySQLiteToPostgres(context.Background(), src, app.db.DB, opts)
}

func dbCopySourcePath(from string) (string, error) {
	if !strings.HasPrefix(from, "sqlite:") || len(from) == len("sqlite:") {
		return "", fmt.Errorf("--from must be sqlite:<path to the SQLite database>")
	}
	return strings.TrimPrefix(from, "sqlite:"), nil
}

type dbCopyQuerier interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

type dbCopyColumn struct {
	name     string
	pgType   string // information_schema data_type
	maxLen   int    // character_maximum_length; 0 when unbounded
	nullable bool
	identity bool
}

type dbCopyTable struct {
	name string
	cols []dbCopyColumn

	// Filled by readSource.
	srcRows      int
	rowHashes    [][]byte
	sanitized    int
	trimmed      int
	normalized   int
	truncated    int
	collapsed    int
	collapseNote []string

	// Filled by verify.
	dstRows   int
	dstHashes [][]byte
}

type dbCopyRow struct {
	rowid int64
	vals  []interface{}
}

func (t *dbCopyTable) expectedRows() int { return t.srcRows - t.collapsed }

func (t *dbCopyTable) colKey(c dbCopyColumn) string { return t.name + "." + c.name }

func dbCopyQuote(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func copySQLiteToPostgres(ctx context.Context, src, dst *sql.DB, opts DBCopyOptions) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	if v := migrations.CurrentVer(); v != dbCopySchemaVersion {
		return fmt.Errorf("this binary's schema is V%d, but db copy only knows V%d: update the copy command's column rules for the new migrations before using it", v, dbCopySchemaVersion)
	}
	if err := dbCopyCheckVersion(ctx, src, "source", dbCopySchemaVersion); err != nil {
		return err
	}
	if err := dbCopyCheckVersion(ctx, dst, "target", dbCopySchemaVersion); err != nil {
		return err
	}

	tables, err := dbCopyTables(ctx, src, dst)
	if err != nil {
		return err
	}

	switch {
	case opts.VerifyOnly:
		for _, t := range tables {
			if err := dbCopyReadSource(ctx, src, t, nil); err != nil {
				return err
			}
		}
		return dbCopyVerify(ctx, dst, tables, out)

	case opts.DryRun:
		if err := dbCopyCheckEmpty(ctx, dst, tables); err != nil {
			return err
		}
		for _, t := range tables {
			if err := dbCopyReadSource(ctx, src, t, nil); err != nil {
				return err
			}
		}
		fmt.Fprintln(out, "Dry run: nothing was written.")
		dbCopyPrintReport(out, tables, false)
		return nil
	}

	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin target transaction: %v", err)
	}
	defer tx.Rollback()

	if err := dbCopyCheckEmpty(ctx, tx, tables); err != nil {
		return err
	}
	for _, t := range tables {
		w := &dbCopyWriter{ctx: ctx, tx: tx, t: t}
		if err := dbCopyReadSource(ctx, src, t, w.add); err != nil {
			return err
		}
		if err := w.flush(); err != nil {
			return err
		}
	}
	for _, t := range tables {
		for _, c := range t.cols {
			if !c.identity {
				continue
			}
			q := fmt.Sprintf("SELECT setval(pg_get_serial_sequence(?, ?), COALESCE(MAX(%[1]s), 1), MAX(%[1]s) IS NOT NULL) FROM %[2]s", dbCopyQuote(c.name), dbCopyQuote(t.name))
			if _, err := tx.ExecContext(ctx, q, t.name, c.name); err != nil {
				return fmt.Errorf("advance the %s.%s sequence: %v", t.name, c.name, dbCopyErr(err))
			}
		}
	}

	// Verify inside the transaction, so a copy that does not verify is
	// never committed.
	if err := dbCopyVerify(ctx, tx, tables, out); err != nil {
		return fmt.Errorf("%v; the copy was rolled back", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %v", dbCopyErr(err))
	}
	fmt.Fprintln(out, "Copy committed.")
	return nil
}

func dbCopyCheckVersion(ctx context.Context, q dbCopyQuerier, which string, want int) error {
	var v int
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM appmigrations").Scan(&v); err != nil {
		return fmt.Errorf("read the %s's migration version: %v", which, err)
	}
	if v == want {
		return nil
	}
	if which == "source" {
		if v < want {
			return fmt.Errorf("the source database is at migration V%d, not V%d: run `writefreely db migrate` against it first, with a configuration that points at the SQLite file (db copy does not migrate its source)", v, want)
		}
		return fmt.Errorf("the source database is at migration V%d, newer than the V%d this binary copies", v, want)
	}
	return fmt.Errorf("the target database is at migration V%d, not V%d: initialise an empty Postgres database with `writefreely db init` using this binary", v, want)
}

// dbCopyTables lists every table to copy, with its Postgres columns, and
// checks the two schemas have the same tables and columns.
func dbCopyTables(ctx context.Context, src, dst *sql.DB) ([]*dbCopyTable, error) {
	rows, err := dst.QueryContext(ctx, `SELECT c.table_name, c.column_name, c.data_type, COALESCE(c.character_maximum_length, 0), c.is_nullable = 'YES', c.is_identity = 'YES'
FROM information_schema.columns c
JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema = current_schema() AND t.table_type = 'BASE TABLE' AND c.table_name <> 'appmigrations'
ORDER BY c.table_name, c.ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("read target schema: %v", err)
	}
	defer rows.Close()
	var tables []*dbCopyTable
	byName := map[string]*dbCopyTable{}
	for rows.Next() {
		var tn string
		var c dbCopyColumn
		if err := rows.Scan(&tn, &c.name, &c.pgType, &c.maxLen, &c.nullable, &c.identity); err != nil {
			return nil, err
		}
		t := byName[tn]
		if t == nil {
			t = &dbCopyTable{name: tn}
			byName[tn] = t
			tables = append(tables, t)
		}
		t.cols = append(t.cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, errors.New("the target database has no tables: run `writefreely db init` on it first")
	}

	srcTables := map[string]bool{}
	srows, err := src.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\' AND name <> 'appmigrations'`)
	if err != nil {
		return nil, fmt.Errorf("read source schema: %v", err)
	}
	for srows.Next() {
		var n string
		if err := srows.Scan(&n); err != nil {
			srows.Close()
			return nil, err
		}
		srcTables[n] = true
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, err
	}

	var problems []string
	for n := range srcTables {
		if byName[n] == nil {
			problems = append(problems, fmt.Sprintf("table %s is in the source but not the target", n))
		}
	}
	for _, t := range tables {
		if !srcTables[t.name] {
			problems = append(problems, fmt.Sprintf("table %s is in the target but not the source", t.name))
			continue
		}
		srcCols := map[string]bool{}
		crows, err := src.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", t.name)
		if err != nil {
			return nil, fmt.Errorf("read source columns of %s: %v", t.name, err)
		}
		for crows.Next() {
			var n string
			if err := crows.Scan(&n); err != nil {
				crows.Close()
				return nil, err
			}
			srcCols[n] = true
		}
		crows.Close()
		for _, c := range t.cols {
			if !srcCols[c.name] {
				problems = append(problems, fmt.Sprintf("column %s.%s is in the target but not the source", t.name, c.name))
			}
			delete(srcCols, c.name)
		}
		for n := range srcCols {
			problems = append(problems, fmt.Sprintf("column %s.%s is in the source but not the target", t.name, n))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("the source and target schemas differ:\n  %s", strings.Join(problems, "\n  "))
	}
	return tables, nil
}

func dbCopyCheckEmpty(ctx context.Context, q dbCopyQuerier, tables []*dbCopyTable) error {
	var full []string
	for _, t := range tables {
		var exists bool
		if err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+dbCopyQuote(t.name)+")").Scan(&exists); err != nil {
			return fmt.Errorf("check target table %s: %v", t.name, dbCopyErr(err))
		}
		if exists {
			full = append(full, t.name)
		}
	}
	if len(full) > 0 {
		return fmt.Errorf("the target database is not empty (rows in: %s). db copy only writes into a freshly initialised database; use --verify-only to check an existing copy", strings.Join(full, ", "))
	}
	return nil
}

// dbCopyReadSource reads table t from the source in rowid order, converts
// every row, applies the collapses, records the row hashes, and hands each
// kept row to emit (when it is not nil).
func dbCopyReadSource(ctx context.Context, src *sql.DB, t *dbCopyTable, emit func(dbCopyRow) error) error {
	t.srcRows, t.sanitized, t.trimmed, t.normalized, t.truncated, t.collapsed = 0, 0, 0, 0, 0, 0
	t.rowHashes, t.collapseNote = nil, nil

	exprs := []string{"rowid"}
	for _, c := range t.cols {
		q := dbCopyQuote(c.name)
		switch c.pgType {
		case "bytea":
			q = "CAST(" + q + " AS BLOB)"
		case "text", "character varying", "timestamp with time zone":
			q = "CAST(" + q + " AS TEXT)"
		}
		exprs = append(exprs, q)
	}
	rows, err := src.QueryContext(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM "+dbCopyQuote(t.name)+" ORDER BY rowid")
	if err != nil {
		return fmt.Errorf("read source table %s: %v", t.name, err)
	}
	defer rows.Close()

	// Tables that may collapse rows are small; hold them to decide.
	_, pgOnly := dbCopyPostgresOnlyKeys[t.name]
	buffer := pgOnly || t.name == "emailsubscribers"
	var held []dbCopyRow

	raw := make([]interface{}, len(exprs))
	ptrs := make([]interface{}, len(exprs))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("read source table %s: %v", t.name, err)
		}
		rowid, ok := raw[0].(int64)
		if !ok {
			return fmt.Errorf("source table %s: rowid is not an integer", t.name)
		}
		t.srcRows++
		r := dbCopyRow{rowid: rowid, vals: make([]interface{}, len(t.cols))}
		for i, c := range t.cols {
			v, err := t.convert(c, raw[i+1])
			if err != nil {
				return fmt.Errorf("source table %s, column %s, rowid %d: %v", t.name, c.name, rowid, err)
			}
			r.vals[i] = v
		}
		if buffer {
			held = append(held, r)
			continue
		}
		if err := t.keep(r, emit); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read source table %s: %v", t.name, err)
	}
	if !buffer {
		return nil
	}

	if t.name == "emailsubscribers" {
		held = t.collapseSubscribers(held)
	}
	if pgOnly {
		if held, err = t.collapseKeyDuplicates(held); err != nil {
			return err
		}
	}
	for _, r := range held {
		if err := t.keep(r, emit); err != nil {
			return err
		}
	}
	return nil
}

func (t *dbCopyTable) keep(r dbCopyRow, emit func(dbCopyRow) error) error {
	t.rowHashes = append(t.rowHashes, dbCopyRowHash(t.cols, r.vals))
	if emit != nil {
		return emit(r)
	}
	return nil
}

func (t *dbCopyTable) colIndex(name string) int {
	for i, c := range t.cols {
		if c.name == name {
			return i
		}
	}
	panic("dbcopy: no column " + t.name + "." + name)
}

// collapseSubscribers keeps one row per blog and address. Addresses are
// already lower-cased by convert. The confirmed row wins, then the earliest
// subscription, then the lowest rowid (WFPG-09's rule).
func (t *dbCopyTable) collapseSubscribers(rows []dbCopyRow) []dbCopyRow {
	iColl, iEmail := t.colIndex("collection_id"), t.colIndex("email")
	iConf, iSub, iID := t.colIndex("confirmed"), t.colIndex("subscribed"), t.colIndex("id")
	better := func(a, b dbCopyRow) bool {
		ca, cb := a.vals[iConf].(bool), b.vals[iConf].(bool)
		if ca != cb {
			return ca
		}
		sa, sb := a.vals[iSub].(time.Time), b.vals[iSub].(time.Time)
		if !sa.Equal(sb) {
			return sa.Before(sb)
		}
		return a.rowid < b.rowid
	}
	type key struct {
		coll  interface{}
		email string
	}
	groups := map[key][]int{}
	var order []key
	for i, r := range rows {
		e, ok := r.vals[iEmail].(string)
		if !ok {
			continue // NULL email: a user subscription, keyed by user_id.
		}
		k := key{r.vals[iColl], e}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], i)
	}
	drop := map[int]bool{}
	for _, k := range order {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		win := g[0]
		for _, i := range g[1:] {
			if better(rows[i], rows[win]) {
				win = i
			}
		}
		for _, i := range g {
			if i == win {
				continue
			}
			drop[i] = true
			t.collapsed++
			t.collapseNote = append(t.collapseNote, fmt.Sprintf("subscriber %v merged into %v (same blog, address differs only in case)", rows[i].vals[iID], rows[win].vals[iID]))
		}
	}
	kept := rows[:0:0]
	for i, r := range rows {
		if !drop[i] {
			kept = append(kept, r)
		}
	}
	return kept
}

// collapseKeyDuplicates enforces a primary key Postgres has and SQLite did
// not. An exact duplicate is dropped; a duplicate that differs anywhere else
// is an error, because choosing between them would lose data.
func (t *dbCopyTable) collapseKeyDuplicates(rows []dbCopyRow) ([]dbCopyRow, error) {
	var idx []int
	for _, n := range dbCopyPostgresOnlyKeys[t.name] {
		idx = append(idx, t.colIndex(n))
	}
	first := map[string]int{}
	var kept []dbCopyRow
	var conflicts []string
	for _, r := range rows {
		var kb strings.Builder
		for _, i := range idx {
			dbCopyRender(&kb, r.vals[i])
			kb.WriteByte(0x1f)
		}
		k := kb.String()
		j, dup := first[k]
		if !dup {
			first[k] = len(kept)
			kept = append(kept, r)
			continue
		}
		if bytes.Equal(dbCopyRowHash(t.cols, r.vals), dbCopyRowHash(t.cols, kept[j].vals)) {
			t.collapsed++
			t.collapseNote = append(t.collapseNote, fmt.Sprintf("rowid %d dropped: exact duplicate of rowid %d", r.rowid, kept[j].rowid))
			continue
		}
		conflicts = append(conflicts, fmt.Sprintf("rowids %d and %d", kept[j].rowid, r.rowid))
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("source table %s has rows that share a primary key (%s) but differ: %s. Postgres cannot hold both; decide which to keep and delete the other from the SQLite file, then run db copy again",
			t.name, strings.Join(dbCopyPostgresOnlyKeys[t.name], ", "), strings.Join(conflicts, "; "))
	}
	return kept, nil
}

// convert turns one raw source value into the Go value to bind for column
// c: nil, bool, int64, time.Time, []byte or string.
func (t *dbCopyTable) convert(c dbCopyColumn, v interface{}) (interface{}, error) {
	if v == nil {
		if !c.nullable {
			return nil, errors.New("NULL in a NOT NULL column")
		}
		return nil, nil
	}
	switch c.pgType {
	case "boolean":
		n, err := dbCopyInt(v)
		if err != nil {
			return nil, err
		}
		switch n {
		case 0:
			return false, nil
		case 1:
			return true, nil
		}
		return nil, fmt.Errorf("flag holds %d, not 0 or 1", n)

	case "smallint", "integer", "bigint":
		n, err := dbCopyInt(v)
		if err != nil {
			return nil, err
		}
		lo, hi := int64(-1<<63), int64(1<<63-1)
		switch c.pgType {
		case "smallint":
			lo, hi = -1<<15, 1<<15-1
		case "integer":
			lo, hi = -1<<31, 1<<31-1
		}
		if n < lo || n > hi {
			return nil, fmt.Errorf("%d is out of range for %s", n, c.pgType)
		}
		return n, nil

	case "timestamp with time zone":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("datetime read as %T", v)
		}
		return parseSQLiteTime(s)

	case "bytea":
		b, ok := v.([]byte)
		if !ok {
			return nil, fmt.Errorf("binary value read as %T", v)
		}
		// Copy: the driver may reuse the buffer.
		return append([]byte{}, b...), nil

	case "text", "character varying":
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case []byte:
			s = string(x)
		default:
			return nil, fmt.Errorf("text read as %T", v)
		}
		key := t.colKey(c)
		if clean := sanitizeDBText(s); clean != s {
			t.sanitized++
			s = clean
		}
		if dbCopyCharColumns[key] {
			if trimmed := strings.TrimRight(s, " "); trimmed != s {
				t.trimmed++
				s = trimmed
			}
		}
		if norm := dbCopyNormalize[key]; norm != nil {
			if n := norm(s); n != s {
				t.normalized++
				s = n
			}
		}
		if c.maxLen > 0 && utf8.RuneCountInString(s) > c.maxLen {
			if !dbCopyTruncatable[key] {
				return nil, fmt.Errorf("value is %d characters, longer than the %d this column holds in Postgres", utf8.RuneCountInString(s), c.maxLen)
			}
			t.truncated++
			s = boundedDBText(s, c.maxLen)
		}
		return s, nil
	}
	return nil, fmt.Errorf("db copy has no rule for Postgres type %q", c.pgType)
}

func dbCopyInt(v interface{}) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case float64:
		if x == float64(int64(x)) {
			return int64(x), nil
		}
		return 0, errors.New("number is not an integer")
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(x), 10, 64)
	case []byte:
		return strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64)
	}
	return 0, fmt.Errorf("integer read as %T", v)
}

// parseSQLiteTime parses a datetime as SQLite holds it: text written by
// CURRENT_TIMESTAMP (UTC, no offset) or by the Go driver (with an offset),
// or an integer number of seconds since the epoch. Text without an offset is
// UTC. The result is UTC, truncated to the microsecond Postgres keeps.
func parseSQLiteTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty datetime")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	// time.Time.String() output, possibly with its monotonic clock reading.
	if i := strings.Index(s, " m="); i > 0 {
		s = s[:i]
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", s); err == nil {
		return t.UTC().Truncate(time.Microsecond), nil
	}
	s = strings.TrimSuffix(s, "Z")
	for _, f := range sqlite3.SQLiteTimestampFormats {
		if t, err := time.ParseInLocation(f, s, time.UTC); err == nil {
			return t.UTC().Truncate(time.Microsecond), nil
		}
	}
	return time.Time{}, errors.New("unrecognised datetime format")
}

// dbCopyRender writes a canonical, type-tagged rendering of v.
func dbCopyRender(b *strings.Builder, v interface{}) {
	switch x := v.(type) {
	case nil:
		b.WriteString("N")
	case bool:
		if x {
			b.WriteString("b1")
		} else {
			b.WriteString("b0")
		}
	case int64:
		b.WriteString("i")
		b.WriteString(strconv.FormatInt(x, 10))
	case time.Time:
		b.WriteString("t")
		b.WriteString(x.UTC().Format(time.RFC3339Nano))
	case []byte:
		b.WriteString("x")
		b.WriteString(hex.EncodeToString(x))
	case string:
		b.WriteString("s")
		b.WriteString(strconv.Itoa(len(x)))
		b.WriteByte(':')
		b.WriteString(x)
	default:
		fmt.Fprintf(b, "?%T", v)
	}
}

func dbCopyRowHash(cols []dbCopyColumn, vals []interface{}) []byte {
	var b strings.Builder
	for i, c := range cols {
		b.WriteString(c.name)
		b.WriteByte('=')
		dbCopyRender(&b, vals[i])
		b.WriteByte(0x1e)
	}
	h := sha256.Sum256([]byte(b.String()))
	return h[:]
}

// dbCopyChecksum is order-independent: the hash of the sorted row hashes.
func dbCopyChecksum(hashes [][]byte) string {
	s := make([][]byte, len(hashes))
	copy(s, hashes)
	sort.Slice(s, func(i, j int) bool { return bytes.Compare(s[i], s[j]) < 0 })
	h := sha256.New()
	for _, x := range s {
		h.Write(x)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dbCopyWriter batches rows into multi-row INSERTs.
type dbCopyWriter struct {
	ctx     context.Context
	tx      *sql.Tx
	t       *dbCopyTable
	pending []dbCopyRow
}

func (w *dbCopyWriter) add(r dbCopyRow) error {
	w.pending = append(w.pending, r)
	if len(w.pending) >= dbCopyBatchRows {
		return w.flush()
	}
	return nil
}

func (w *dbCopyWriter) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	t := w.t
	names := make([]string, len(t.cols))
	for i, c := range t.cols {
		names[i] = dbCopyQuote(c.name)
	}
	one := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(t.cols)), ", ") + ")"
	var q strings.Builder
	q.WriteString("INSERT INTO " + dbCopyQuote(t.name) + " (" + strings.Join(names, ", ") + ") VALUES ")
	args := make([]interface{}, 0, len(w.pending)*len(t.cols))
	for i, r := range w.pending {
		if i > 0 {
			q.WriteString(", ")
		}
		q.WriteString(one)
		args = append(args, r.vals...)
	}
	if _, err := w.tx.ExecContext(w.ctx, q.String(), args...); err != nil {
		return fmt.Errorf("insert into %s (source rowids %d to %d): %v", t.name, w.pending[0].rowid, w.pending[len(w.pending)-1].rowid, dbCopyErr(err))
	}
	w.pending = w.pending[:0]
	return nil
}

// dbCopyErr reduces a Postgres error to its code, message and constraint.
// The detail line, which can quote the offending value, is left out.
func dbCopyErr(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		s := fmt.Sprintf("%s (SQLSTATE %s)", pe.Message, pe.Code)
		if pe.ConstraintName != "" {
			s += ", constraint " + pe.ConstraintName
		}
		return errors.New(s)
	}
	return err
}

// dbCopyVerify reads every table back from the target and compares it with
// what the source converts to, then checks the identity sequences.
func dbCopyVerify(ctx context.Context, q dbCopyQuerier, tables []*dbCopyTable, out io.Writer) error {
	var failed []string
	for _, t := range tables {
		names := make([]string, len(t.cols))
		for i, c := range t.cols {
			names[i] = dbCopyQuote(c.name)
		}
		rows, err := q.QueryContext(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+dbCopyQuote(t.name))
		if err != nil {
			return fmt.Errorf("read target table %s: %v", t.name, dbCopyErr(err))
		}
		t.dstRows, t.dstHashes = 0, nil
		vals := make([]interface{}, len(t.cols))
		ptrs := make([]interface{}, len(t.cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return fmt.Errorf("read target table %s: %v", t.name, err)
			}
			for i, v := range vals {
				switch x := v.(type) {
				case int32:
					vals[i] = int64(x)
				case int16:
					vals[i] = int64(x)
				case time.Time:
					vals[i] = x.UTC()
				}
			}
			t.dstRows++
			t.dstHashes = append(t.dstHashes, dbCopyRowHash(t.cols, vals))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("read target table %s: %v", t.name, err)
		}
	}

	type seqResult struct {
		table, col string
		max, next  int64
		ok         bool
	}
	var seqs []seqResult
	for _, t := range tables {
		for _, c := range t.cols {
			if !c.identity {
				continue
			}
			var seq string
			if err := q.QueryRowContext(ctx, "SELECT pg_get_serial_sequence(?, ?)", t.name, c.name).Scan(&seq); err != nil {
				return fmt.Errorf("find the %s.%s sequence: %v", t.name, c.name, dbCopyErr(err))
			}
			var last int64
			var called bool
			// seq comes from pg_get_serial_sequence, already quoted as needed.
			if err := q.QueryRowContext(ctx, "SELECT last_value, is_called FROM "+seq).Scan(&last, &called); err != nil {
				return fmt.Errorf("read the %s.%s sequence: %v", t.name, c.name, dbCopyErr(err))
			}
			var max int64
			if err := q.QueryRowContext(ctx, fmt.Sprintf("SELECT COALESCE(MAX(%s), 0) FROM %s", dbCopyQuote(c.name), dbCopyQuote(t.name))).Scan(&max); err != nil {
				return fmt.Errorf("read MAX(%s.%s): %v", t.name, c.name, dbCopyErr(err))
			}
			next := last
			if called {
				next++
			}
			r := seqResult{t.name, c.name, max, next, next > max}
			if !r.ok {
				failed = append(failed, fmt.Sprintf("sequence %s.%s", t.name, c.name))
			}
			seqs = append(seqs, r)
		}
	}

	dbCopyPrintReport(out, tables, true)
	for _, t := range tables {
		if t.dstRows != t.expectedRows() || dbCopyChecksum(t.dstHashes) != dbCopyChecksum(t.rowHashes) {
			failed = append(failed, t.name)
		}
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "sequence\tmax id\tnext value\tok")
	for _, s := range seqs {
		fmt.Fprintf(tw, "%s.%s\t%d\t%d\t%s\n", s.table, s.col, s.max, s.next, dbCopyOK(s.ok))
	}
	tw.Flush()

	if len(failed) > 0 {
		sort.Strings(failed)
		return fmt.Errorf("verification failed for: %s", strings.Join(failed, ", "))
	}
	fmt.Fprintln(out, "Verification passed.")
	return nil
}

func dbCopyOK(ok bool) string {
	if ok {
		return "ok"
	}
	return "MISMATCH"
}

// dbCopyPrintReport prints one line per table: counts and IDs only.
func dbCopyPrintReport(out io.Writer, tables []*dbCopyTable, verified bool) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	hdr := "table\tsource rows\tcollapsed\texpected\tsanitized\ttrimmed\tnormalized\ttruncated"
	if verified {
		hdr += "\ttarget rows\tchecksum"
	}
	fmt.Fprintln(tw, hdr)
	for _, t := range tables {
		line := fmt.Sprintf("%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d", t.name, t.srcRows, t.collapsed, t.expectedRows(), t.sanitized, t.trimmed, t.normalized, t.truncated)
		if verified {
			sum := dbCopyChecksum(t.dstHashes) == dbCopyChecksum(t.rowHashes)
			line += fmt.Sprintf("\t%d\t%s", t.dstRows, dbCopyOK(sum && t.dstRows == t.expectedRows()))
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()
	for _, t := range tables {
		for _, n := range t.collapseNote {
			fmt.Fprintf(out, "collapsed in %s: %s\n", t.name, n)
		}
	}
}
