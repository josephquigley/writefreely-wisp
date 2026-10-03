//go:build sqlite

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
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-ini/ini"
	"github.com/gorilla/mux"
	"github.com/writefreely/go-gopher"
	"github.com/writefreely/writefreely/config"
)

// stubGopherWriter records what a Gopher handler wrote.
type stubGopherWriter struct {
	gopher.ResponseWriter // nil: any method the test does not expect panics
	errs                  []string
}

func (s *stubGopherWriter) WriteError(e string) error { s.errs = append(s.errs, e); return nil }

func TestGopherRefusesWhilePrivate(t *testing.T) {
	a := loadedSettingsApp(t)
	h := &Handler{app: a}
	called := 0
	wrapped := h.Gopher(func(app *App, w gopher.ResponseWriter, r *gopher.Request) error {
		called++
		return nil
	})
	serve := func() *stubGopherWriter {
		w := &stubGopherWriter{}
		wrapped(w, &gopher.Request{Selector: "/"})
		return w
	}

	if w := serve(); called != 1 || len(w.errs) != 0 {
		t.Fatalf("public: called %d errs %v", called, w.errs)
	}
	// Another node makes the instance private; this node learns of it from
	// the wrapper's own refresh.
	if _, err := a.db.SaveSettings(context.Background(), map[string]string{"app.private": "true"}); err != nil {
		t.Fatal(err)
	}
	w := serve()
	if called != 1 {
		t.Error("handler ran on a private instance")
	}
	if len(w.errs) != 1 || w.errs[0] != "This instance is private." {
		t.Errorf("errs %v", w.errs)
	}
}

func TestMiddlewareInstalledOnRouter(t *testing.T) {
	a := loadedSettingsApp(t)
	b := secondNode(t, a)
	r := mux.NewRouter()
	var seen string
	r.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) { seen = b.Config().App.SiteName })
	b.useMiddleware(r)
	a.db.SaveSettings(context.Background(), map[string]string{"app.site_name": "Via router"})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if seen != "Via router" {
		t.Errorf("router did not refresh settings: %q", seen)
	}
}

// Serve blocks, so a test cannot run it; check instead that it installs the
// middleware, before any other statement.
func TestServeInstallsMiddleware(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "Serve" {
			continue
		}
		first, ok := fd.Body.List[0].(*ast.ExprStmt)
		if !ok {
			t.Fatal("Serve's first statement is not a call")
		}
		call, ok := first.X.(*ast.CallExpr)
		if !ok {
			t.Fatal("Serve's first statement is not a call")
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "useMiddleware" {
			t.Errorf("Serve must begin with app.useMiddleware(r)")
		}
		return
	}
	t.Fatal("no Serve in app.go")
}

func TestExportINIRoundTrip(t *testing.T) {
	tricky := map[string]string{
		"app.site_name":        `He said "hi" ; not a comment # nor this`,
		"app.site_description": "back`tick `and` ; # \"both\" 'single'",
		"app.landing":          `/a;b#c`,
		"app.editor":           "`",
		"app.theme":            `"quoted"`,
		"app.webfonts":         `'x'`,
		"app.simple_nav":       `a"`,
		"app.chorus":           "line one\nline two ; x",
		"app.forest":           `a\`,
		"app.disable_drafts":   `\`,
		"app.notes_only":       `x\\`,
	}
	vals := config.SettingDefaults()
	for k, v := range tricky {
		vals[k] = v
	}
	out, err := config.ExportINI(vals)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ini.Load([]byte(out))
	if err != nil {
		t.Fatalf("export does not parse: %v\n%s", err, out)
	}
	for k, want := range vals {
		sec, key, _ := strings.Cut(k, ".")
		if got := f.Section(sec).Key(key).String(); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
}
