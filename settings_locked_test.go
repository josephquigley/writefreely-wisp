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
	"bytes"
	"context"
	stdlog "log"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/writeas/impart"
	"github.com/writeas/web-core/log"

	"github.com/writefreely/writefreely/config"
)

// lockedFixture locks private, the allowlist and authorized fetch, the way
// a deployment rendering config.ini from its own source would, and carries
// one unlocked DB setting beside them.
const lockedFixture = `locked_settings = app.private, app.federation_allowlist, app.authorized_fetch

[server]
port = 8080

[database]
type     = sqlite3
filename = writefreely.db

[app]
host                 = https://blog.example
site_name            = Paisans
private              = true
federation_allowlist = peer.example
authorized_fetch     = true
`

func lockedApp(t *testing.T, body string) *App {
	t.Helper()
	a := newSettingsTestApp(t, body)
	loadINIInto(t, a)
	return a
}

// restartedNode is a's node started again: a fresh App on the same database
// and config.ini, as a restart gives it.
func restartedNode(t *testing.T, a *App) *App {
	t.Helper()
	b := &App{cfgFile: a.cfgFile, db: a.db}
	cfg, err := config.Load(a.cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = a.cfg.Database
	b.cfg = cfg
	return b
}

func TestLockedValueWinsOverDatabaseRow(t *testing.T) {
	a := lockedApp(t, lockedFixture)
	ctx := context.Background()
	if _, err := a.importSettings(ctx); err != nil {
		t.Fatal(err)
	}
	// A row written before the lock, or by a node without it.
	if _, err := a.db.SaveSettings(ctx, map[string]string{"app.private": "false", "app.federation_allowlist": "", "app.authorized_fetch": "false"}); err != nil {
		t.Fatal(err)
	}
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	c := a.Config().App
	if !c.Private || c.FederationAllowlist != "peer.example" || !c.AuthorizedFetch {
		t.Errorf("at load: private=%v allowlist=%q authorized_fetch=%v", c.Private, c.FederationAllowlist, c.AuthorizedFetch)
	}
	if !a.settings.Load().fedAllowlist["peer.example"] {
		t.Errorf("allowlist not rebuilt from the locked value: %v", a.settings.Load().fedAllowlist)
	}

	// Another node saves an unrelated setting; this node reloads.
	b := secondNode(t, a)
	if err := b.saveSettings(ctx, map[string]string{"app.max_blogs": "4"}); err != nil {
		t.Fatal(err)
	}
	a.db.SaveSettings(ctx, map[string]string{"app.private": "false"}) // and a stray write
	a.refreshSettings(ctx)
	c = a.Config().App
	if c.MaxBlogs != 4 {
		t.Fatalf("reload did not happen: max_blogs %d", c.MaxBlogs)
	}
	if !c.Private || c.FederationAllowlist != "peer.example" || !c.AuthorizedFetch {
		t.Errorf("after reload: private=%v allowlist=%q authorized_fetch=%v", c.Private, c.FederationAllowlist, c.AuthorizedFetch)
	}
	if !a.settings.Load().fedAllowlist["peer.example"] {
		t.Error("allowlist lost on reload")
	}
}

func TestImportSkipsAndLeavesLockedKeys(t *testing.T) {
	a := lockedApp(t, lockedFixture)
	ctx := context.Background()
	res, err := a.importSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Imported {
		t.Fatal("did not import")
	}
	rows, _, _ := a.db.LoadSettings(ctx)
	for _, n := range []string{"app.private", "app.federation_allowlist", "app.authorized_fetch"} {
		if v, ok := rows[n]; ok {
			t.Errorf("locked %s imported as %q", n, v)
		}
	}
	if rows["app.site_name"] != "Paisans" {
		t.Errorf("unlocked site_name = %q, want it imported", rows["app.site_name"])
	}
	if len(rows) != len(config.DBSettingNames())-3 {
		t.Errorf("imported %d rows, want every unlocked setting (%d)", len(rows), len(config.DBSettingNames())-3)
	}
	if !reflect.DeepEqual(res.Stripped, []string{"app.site_name"}) {
		t.Errorf("stripped %v, want only app.site_name", res.Stripped)
	}
	b, _ := os.ReadFile(a.cfgFile)
	for _, k := range []string{"locked_settings", "private", "federation_allowlist", "authorized_fetch"} {
		if !strings.Contains(string(b), k) {
			t.Errorf("%s gone from config.ini:\n%s", k, b)
		}
	}
	if strings.Contains(string(b), "site_name") {
		t.Errorf("unlocked site_name not stripped:\n%s", b)
	}
}

func TestLockedKeysAreNotDrift(t *testing.T) {
	a := lockedApp(t, lockedFixture)
	ctx := context.Background()
	if _, err := a.importSettings(ctx); err != nil {
		t.Fatal(err)
	}
	// A row that disagrees with the locked value, and config.ini rendered
	// again from its source, as a deployment does on every start.
	a.db.SaveSettings(ctx, map[string]string{"app.private": "false"})
	writeTestINI(t, a.cfgFile, lockedFixture)

	var logged bytes.Buffer
	orig := log.ErrorLog
	log.ErrorLog = stdlog.New(&logged, "", 0)
	defer func() { log.ErrorLog = orig }()

	b := restartedNode(t, a)
	res, err := b.importSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drift) != 0 {
		t.Errorf("drift %v", res.Drift)
	}
	if strings.Contains(logged.String(), "disagrees") {
		t.Errorf("drift logged: %s", logged.String())
	}
}

func TestSaveRefusesLockedSetting(t *testing.T) {
	a := lockedApp(t, lockedFixture)
	ctx := context.Background()
	a.importSettings(ctx)
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	_, v1, _ := a.db.LoadSettings(ctx)
	for _, change := range []map[string]string{
		{"app.private": "false"},
		{"app.private": "true"}, // the same value is still a write to a locked name
		{"app.site_name": "Fine", "app.authorized_fetch": "false"},
	} {
		err := a.saveSettings(ctx, change)
		if err == nil || !strings.Contains(err.Error(), "locked") {
			t.Errorf("save %v: err %v", change, err)
		}
	}
	err := a.saveSettings(ctx, map[string]string{"app.federation_allowlist": "x.example"})
	if err == nil || !strings.Contains(err.Error(), "app.federation_allowlist") {
		t.Errorf("error does not name the setting: %v", err)
	}
	rows, v2, _ := a.db.LoadSettings(ctx)
	if v1 != v2 || rows["app.site_name"] != "Paisans" {
		t.Errorf("a refused save wrote: version %d -> %d, site_name %q", v1, v2, rows["app.site_name"])
	}
	if err := a.saveSettings(ctx, map[string]string{"app.site_name": "Unlocked"}); err != nil {
		t.Errorf("unlocked save refused: %v", err)
	}
}

// postLockedForm posts the admin settings form as the page renders it with
// app.private locked: the disabled checkbox is not sent at all.
func postLockedForm(t *testing.T, a *App, extra url.Values) string {
	t.Helper()
	form := url.Values{
		"settings_version":    {settingsFormVersion(a)},
		"site_name":           {"From form"},
		"min_username_len":    {"3"},
		"max_blogs":           {"2"},
		"user_invites":        {"none"},
		"default_visibility":  {"public"},
		"theme":               {"write"},
		"uploads_max_size_mb": {"10"},
	}
	for k, v := range extra {
		form[k] = v
	}
	r := httptest.NewRequest("POST", "/admin/settings", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	err := handleAdminUpdateConfig(a, nil, httptest.NewRecorder(), r)
	he, ok := err.(impart.HTTPError)
	if !ok {
		t.Fatalf("handler returned %v", err)
	}
	msg, _ := url.QueryUnescape(he.Message)
	return msg
}

func TestAdminSaveWithLockedFields(t *testing.T) {
	a := lockedApp(t, lockedFixture)
	ctx := context.Background()
	a.importSettings(ctx)
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}

	// As rendered: locked fields absent. The save lands, the locks hold.
	if m := postLockedForm(t, a, nil); !strings.Contains(m, "Configuration saved.") {
		t.Fatalf("ordinary save: %s", m)
	}
	c := a.Config().App
	if c.SiteName != "From form" || !c.Private || c.FederationAllowlist != "peer.example" || !c.AuthorizedFetch {
		t.Errorf("after save: %+v", c)
	}

	// A crafted POST sending a locked field at its locked value is harmless.
	if m := postLockedForm(t, a, url.Values{"private": {"on"}, "federation_allowlist": {"peer.example"}}); !strings.Contains(m, "Configuration saved.") {
		t.Errorf("same-value post: %s", m)
	}

	// A crafted POST changing a locked field is refused, naming it.
	_, v1, _ := a.db.LoadSettings(ctx)
	m := postLockedForm(t, a, url.Values{"site_name": {"Crafted"}, "federation_allowlist": {"evil.example"}})
	if !strings.Contains(m, "app.federation_allowlist") || !strings.Contains(m, "locked") {
		t.Errorf("crafted post: %s", m)
	}
	rows, v2, _ := a.db.LoadSettings(ctx)
	if v1 != v2 || rows["app.site_name"] == "Crafted" || a.Config().App.FederationAllowlist != "peer.example" {
		t.Error("a crafted post changed something")
	}
}

func TestLockedSettingsRefuseStartup(t *testing.T) {
	for _, tc := range []struct{ list, want string }{
		{"app.private, app.privte", "app.privte"},
		{"app.private, app.host", "app.host"},
		{"database.password", "database.password"},
	} {
		body := strings.Replace(lockedFixture, "app.private, app.federation_allowlist, app.authorized_fetch", tc.list, 1)
		a := lockedApp(t, body)
		_, err := a.importSettings(context.Background())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: import err %v", tc.list, err)
		}
		if err := a.loadSettings(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: load err %v", tc.list, err)
		}
		if _, err := a.lockedSettings(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: lockedSettings err %v", tc.list, err)
		}
	}
}

func TestUnlockingRestoresStoredValue(t *testing.T) {
	a := lockedApp(t, lockedFixture)
	ctx := context.Background()
	a.importSettings(ctx)
	a.db.SaveSettings(ctx, map[string]string{"app.authorized_fetch": "false"}) // stored while locked
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if !a.Config().App.AuthorizedFetch {
		t.Fatal("lock did not hold")
	}
	// The operator drops app.authorized_fetch from the list and restarts.
	body, _ := os.ReadFile(a.cfgFile)
	writeTestINI(t, a.cfgFile, strings.Replace(string(body), ", app.authorized_fetch", "", 1))
	b := restartedNode(t, a)
	if _, err := b.importSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if b.Config().App.AuthorizedFetch {
		t.Error("the stored value did not come back")
	}
	if !b.Config().App.Private {
		t.Error("a still-locked setting lost its lock")
	}
}

// A deployment that mounts config.ini read-only and puts only locked DB
// keys in it has nothing to strip, so it gets no strip error.
func TestReadOnlyINIWithOnlyLockedKeys(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	body := strings.Replace(lockedFixture, "site_name            = Paisans\n", "", 1)
	a := lockedApp(t, body)
	dir := filepath.Dir(a.cfgFile)
	os.Chmod(a.cfgFile, 0400)
	os.Chmod(dir, 0500)
	t.Cleanup(func() { os.Chmod(dir, 0700); os.Chmod(a.cfgFile, 0600) })
	res, err := a.importSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Imported || res.StripErr != nil || len(res.Left) != 0 || len(res.Stripped) != 0 {
		t.Errorf("res %+v", res)
	}
	if err := a.loadSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.Config().App.Private {
		t.Error("locked private lost")
	}
}

func TestSettingsCLIShowsLocked(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "config.ini")
	body := strings.Replace(lockedFixture, "filename = writefreely.db", "filename = "+filepath.Join(dir, "wf.db"), 1)
	writeTestINI(t, ini, body)
	if err := CreateSchema(NewApp(ini)); err != nil {
		t.Fatal(err)
	}
	rows, err := SettingsList(NewApp(ini))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		want := r.Name == "app.private" || r.Name == "app.federation_allowlist" || r.Name == "app.authorized_fetch"
		if r.Locked != want {
			t.Errorf("%s locked=%v", r.Name, r.Locked)
		}
	}
	r, err := SettingGet(NewApp(ini), "app.private")
	if err != nil || !r.Locked || r.Value != "true" {
		t.Errorf("get %+v err %v", r, err)
	}

	err = SettingSet(NewApp(ini), "app.private", "false")
	if err == nil || !strings.Contains(err.Error(), "app.private") || !strings.Contains(err.Error(), "locked") {
		t.Errorf("set locked: %v", err)
	}
	if err := SettingSet(NewApp(ini), "app.site_name", "Set"); err != nil {
		t.Errorf("set unlocked: %v", err)
	}

	out, err := SettingsExport(NewApp(ini))
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?m)^(private|federation_allowlist|authorized_fetch)\s*=`).MatchString(out) {
		t.Errorf("export carries a locked key:\n%s", out)
	}
	if !strings.Contains(out, "site_name = Set") {
		t.Errorf("export lost an unlocked key:\n%s", out)
	}
	// Pasted back, as the downgrade instructions say, it still loads, and the
	// locked values are the ones config.ini already had.
	f, _ := os.OpenFile(ini, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("\n" + out)
	f.Close()
	cfg, err := config.Load(ini)
	if err != nil {
		t.Fatalf("export pasted back does not load: %v", err)
	}
	if !cfg.App.Private || cfg.App.FederationAllowlist != "peer.example" || cfg.App.SiteName != "Set" {
		t.Errorf("after paste-back: %+v", cfg.App)
	}
	if _, err := config.LockedSettings(ini, cfg); err != nil {
		t.Errorf("after paste-back: %v", err)
	}
}

func TestAdminPageRendersLockedFieldsDisabled(t *testing.T) {
	app, _ := newTemplateTestApp(t, nil)
	u, _, _ := createTemplateTestUser(t, app, "lockadmin")
	app.cfgFile = filepath.Join(t.TempDir(), "config.ini")
	writeTestINI(t, app.cfgFile, "locked_settings = app.private, app.federation_allowlist\n[app]\nhost = http://localhost:8080\nprivate = true\nfederation_allowlist = peer.example\n")

	rec := httptest.NewRecorder()
	if err := handleViewAdminSettings(app, u, rec, httptest.NewRequest("GET", "/admin/settings", nil)); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	disabled := func(name string) bool {
		return regexp.MustCompile(`<input[^>]*name="` + name + `"[^>]*disabled`).MatchString(body)
	}
	for _, n := range []string{"private", "federation_allowlist"} {
		if !disabled(n) {
			t.Errorf("%s is not disabled", n)
		}
	}
	for _, n := range []string{"site_name", "authorized_fetch", "max_blogs"} {
		if disabled(n) {
			t.Errorf("unlocked %s is disabled", n)
		}
	}
	if strings.Count(body, "Set in config.ini") != 2 {
		t.Errorf("want one note per locked field, got %d", strings.Count(body, "Set in config.ini"))
	}
}
