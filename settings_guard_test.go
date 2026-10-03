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

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cfgFieldAllowed lists the only functions that may touch App.cfg, the
// bootstrap configuration from config.ini. Everything else reads
// App.Config(), which returns the current settings snapshot; a direct
// read of .cfg would see bootstrap values and miss every DB setting.
var cfgFieldAllowed = map[string]bool{
	"Config": true, "SetConfig": true, "LoadConfig": true, "NewApp": true,
	"Initialize": true, "ConnectToDatabase": true, "connectToDatabase": true,
	"DoConfig": true, "loadSettingsLocked": true, "importSettings": true, "normalisedSettings": true, "effectiveSettings": true,
	"initFederationAllowlist": true,
	// InitUpdates runs at bootstrap, before settings load, and may only
	// write the bootstrap config; loadSettingsLocked forces the same value
	// on every snapshot.
	"InitUpdates": true,
}

func TestCfgFieldOnlyReadViaConfig(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	for _, fn := range files {
		if strings.HasSuffix(fn, "_test.go") {
			continue
		}
		src, err := os.ReadFile(fn)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, fn, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || cfgFieldAllowed[fd.Name.Name] {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if se, ok := n.(*ast.SelectorExpr); ok && se.Sel.Name == "cfg" {
					t.Errorf("%s: %s reads .cfg directly; use .Config()", fset.Position(se.Pos()), fd.Name.Name)
				}
				return true
			})
		}
	}
}
