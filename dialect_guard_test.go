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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// unportedDriverSites lists every function that still branches on the
// database driver without handling Postgres, keyed "file:Func" (methods as
// "file:Recv.Func"), with the number of such branches in it.
//
// A branch is either a `==`/`!=` comparison against a driver name
// (x.driverName or cfg.Database.Type), or a `switch` on one that has no
// `case driverPostgres`. Since WFPG-01 every such switch ends in
// `default: unsupportedDriver(...)`, so on Postgres these functions panic
// with their own name rather than run MySQL SQL.
//
// When you port a function, delete its entry (or lower its count). The
// guard fails both on a branch that is not listed and on an entry that no
// longer matches anything, so this list is always exactly the work left.
// WFPG-17 requires it to be empty. Do not add to it: new code puts
// dialect-specific SQL in dialect.go.
var unportedDriverSites = map[string]int{
	// Seeded by WFPG-01 from develop 04bf560. The trailing ticket is a best
	// guess at the owner; the ticket texts in Outline win.
	"app.go:adminInitDatabase":                       1, // WFPG-03
	"database.go:datastore.CreatePost":               2, // WFPG-06
	"database.go:datastore.UpdateCollection":         2, // WFPG-04
	"database.go:datastore.GetAllPostsTaggedIDs":     1, // WFPG-04
	"database.go:datastore.GetPostsTagged":           1, // WFPG-04
	"database.go:datastore.UpdateDynamicContent":     1, // WFPG-04
	"database.go:datastore.RecordRemoteUserID":       1, // WFPG-04
	"database.go:datastore.DatabaseInitialized":      1, // WFPG-03
	"database.go:datastore.GetJobsToRun":             1, // WFPG-04
	"migrations/migrations.go:datastore.tableExists": 1, // WFPG-03
	"migrations/v4.go:oauth":                         1, // WFPG-03
	"migrations/v5.go:oauthSlack":                    1, // WFPG-03
	"migrations/v7.go:oauthAttach":                   1, // WFPG-03
	"migrations/v8.go:oauthInvites":                  1, // WFPG-03
	"migrations/v9.go:optimizeDrafts":                1, // WFPG-03
	"migrations/v11.go:widenOauthAcceesToken":        1, // WFPG-03
	"migrations/v17.go:fixPostSignatureCharset":      1, // WFPG-03
}

// reviewedDriverSites lists driver comparisons that have been read and are
// correct on every engine as written, with the reason. Keep it short; each
// entry needs a reason a reviewer can check.
var reviewedDriverSites = map[string]struct {
	count  int
	reason string
}{
	"app.go:ConnectToDatabase": {2, "MySQL-only checks (empty username; v5.x regex compat) that correctly do nothing on SQLite and Postgres"},
}

// driverSiteExemptFiles hold the dialect helpers themselves.
var driverSiteExemptFiles = map[string]bool{
	"dialect.go":            true,
	"migrations/drivers.go": true,
}

func TestDialectGuard(t *testing.T) {
	found := map[string]int{}
	fset := token.NewFileSet()

	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if path != "." && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(path)
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || driverSiteExemptFiles[rel] {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			key := rel + ":" + funcKey(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.BinaryExpr:
					if (n.Op == token.EQL || n.Op == token.NEQ) && (isDriverNameExpr(n.X) || isDriverNameExpr(n.Y)) {
						found[key]++
					}
				case *ast.SwitchStmt:
					if n.Tag != nil && isDriverNameExpr(n.Tag) && !switchHandlesPostgres(n) {
						found[key]++
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]int{}
	for k, v := range unportedDriverSites {
		expected[k] += v
	}
	for k, v := range reviewedDriverSites {
		expected[k] += v.count
	}

	keys := map[string]bool{}
	for k := range found {
		keys[k] = true
	}
	for k := range expected {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, k := range sorted {
		switch got, want := found[k], expected[k]; {
		case got > want:
			t.Errorf("%s: %d driver branch(es) without a Postgres case, %d allowed. Put the dialect-specific SQL in dialect.go, or add `case driverPostgres:`.", k, got, want)
		case got < want:
			t.Errorf("%s: allowlist expects %d driver branch(es), found %d. Lower or delete its entry in dialect_guard_test.go.", k, want, got)
		}
	}
}

// funcKey names a function "Func", or a method "Recv.Func".
func funcKey(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	rt := fd.Recv.List[0].Type
	if s, ok := rt.(*ast.StarExpr); ok {
		rt = s.X
	}
	if id, ok := rt.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// isDriverNameExpr matches x.driverName and x.Database.Type.
func isDriverNameExpr(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name == "driverName" {
		return true
	}
	if sel.Sel.Name == "Type" {
		if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Database" {
			return true
		}
	}
	return false
}

func switchHandlesPostgres(s *ast.SwitchStmt) bool {
	for _, stmt := range s.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, e := range cc.List {
			if id, ok := e.(*ast.Ident); ok && id.Name == "driverPostgres" {
				return true
			}
		}
	}
	return false
}
