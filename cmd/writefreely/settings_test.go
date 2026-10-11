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

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
	"github.com/writeas/web-core/log"
	"github.com/writefreely/writefreely"
)

// runSettings runs `writefreely -c ini settings args...` and returns what
// it wrote to stdout. The info logger is pointed at stdout first, as it is
// by default, so a command that fails to move it would leak into the result.
func runSettings(t *testing.T, ini string, args ...string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldW := os.Stdout, log.InfoLog.Writer()
	os.Stdout = w
	log.InfoLog.SetOutput(w)
	defer func() { os.Stdout = oldOut; log.InfoLog.SetOutput(oldW) }()

	a := &cli.App{
		Flags:    []cli.Flag{&cli.StringFlag{Name: "c"}},
		Commands: []*cli.Command{&cmdSettings},
	}
	runErr := a.Run(append([]string{"writefreely", "-c", ini, "settings"}, args...))
	w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatal(runErr)
	}
	return string(out)
}

func TestSettingsStdoutIsOnlyData(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "config.ini")
	body := "[server]\nport = 8080\n[database]\ntype = sqlite3\nfilename = " + filepath.Join(dir, "wf.db") +
		"\n[app]\nhost = https://blog.example\nsite_name = CLI Site\n"
	if err := os.WriteFile(ini, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writefreely.CreateSchema(writefreely.NewApp(ini)); err != nil {
		t.Fatal(err)
	}

	if got := runSettings(t, ini, "get", "app.site_name"); got != "CLI Site\n" {
		t.Errorf("get stdout = %q", got)
	}
	out := runSettings(t, ini, "export")
	if !strings.HasPrefix(out, "[") && !strings.HasPrefix(out, ";") && !strings.HasPrefix(out, "#") {
		t.Errorf("export stdout does not start like an ini: %q", out)
	}
	for _, noise := range []string{"Loading", "Connecting", "Closing", "Moved"} {
		if strings.Contains(out, noise) {
			t.Errorf("export stdout contains log line %q: %q", noise, out)
		}
	}
}
