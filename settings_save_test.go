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
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/writeas/impart"
)

func loadedSettingsApp(t *testing.T) *App {
	t.Helper()
	a := newSettingsTestApp(t, "")
	if err := a.loadSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSaveSettingsAppliesAndBumps(t *testing.T) {
	a := loadedSettingsApp(t)
	ctx := context.Background()
	if err := a.saveSettings(ctx, map[string]string{"app.site_name": "Saved"}); err != nil {
		t.Fatal(err)
	}
	if a.Config().App.SiteName != "Saved" {
		t.Error("saving node did not reload")
	}
	b := secondNode(t, a)
	if b.Config().App.SiteName != "Saved" {
		t.Error("other node does not see the save")
	}
}

func TestSaveSettingsRejectsAllowlistOnPublic(t *testing.T) {
	a := loadedSettingsApp(t)
	ctx := context.Background()
	_, before, _ := a.db.LoadSettings(ctx)
	err := a.saveSettings(ctx, map[string]string{
		"app.private":              "false",
		"app.federation_allowlist": "peer.example",
		"app.site_name":            "must not land",
	})
	if err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("err %v", err)
	}
	rows, after, _ := a.db.LoadSettings(ctx)
	if after != before || rows["app.site_name"] == "must not land" {
		t.Error("a rejected save wrote something")
	}
}

func TestSaveSettingsRefusesBootstrapAndUnknown(t *testing.T) {
	a := loadedSettingsApp(t)
	err := a.saveSettings(context.Background(), map[string]string{"app.host": "https://x.example"})
	if err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("bootstrap err %v", err)
	}
	err = a.saveSettings(context.Background(), map[string]string{"app.privat": "true"})
	if err == nil || !strings.Contains(err.Error(), "app.private") {
		t.Errorf("unknown err %v (want a suggestion)", err)
	}
}

func TestSaveSettingsBeforeMigration(t *testing.T) {
	a := newSettingsTestApp(t, "") // never loaded: no snapshot
	err := a.saveSettings(context.Background(), map[string]string{"app.site_name": "x"})
	if err == nil || !strings.Contains(err.Error(), "db migrate") {
		t.Errorf("err %v", err)
	}
}

func TestAdminUpdateConfigHandler(t *testing.T) {
	a := loadedSettingsApp(t)
	form := url.Values{
		"settings_version":    {settingsFormVersion(a)},
		"site_name":           {"Handled"},
		"min_username_len":    {"3"},
		"max_blogs":           {"2"},
		"user_invites":        {"none"},
		"default_visibility":  {"public"},
		"theme":               {"write"},
		"federation":          {"on"},
		"uploads_max_size_mb": {"10"},
	}
	r := httptest.NewRequest("POST", "/admin/settings", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	err := handleAdminUpdateConfig(a, nil, httptest.NewRecorder(), r)
	he, ok := err.(impart.HTTPError)
	if !ok || !strings.Contains(he.Message, "Configuration+saved") {
		t.Fatalf("handler returned %v, want a redirect carrying ?cm=Configuration+saved.", err)
	}
	if a.Config().App.SiteName != "Handled" || a.Config().App.UserInvites != "" || !a.Config().App.Federation {
		t.Errorf("config after handler: %+v", a.Config().App)
	}
}

func TestAdminUpdateConfigSkipsUnsupportedUpdateChecks(t *testing.T) {
	if updateChecksSupported {
		t.Skip("update checks are supported in this build")
	}
	a := loadedSettingsApp(t)
	form := url.Values{
		"settings_version":    {settingsFormVersion(a)},
		"site_name":           {"X"},
		"min_username_len":    {"3"},
		"max_blogs":           {"2"},
		"user_invites":        {"none"},
		"default_visibility":  {"public"},
		"theme":               {"write"},
		"update_checks":       {"on"},
		"uploads_max_size_mb": {"10"},
	}
	r := httptest.NewRequest("POST", "/admin/settings", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	err := handleAdminUpdateConfig(a, nil, httptest.NewRecorder(), r)
	he, ok := err.(impart.HTTPError)
	if !ok || !strings.Contains(he.Message, "Configuration+saved") {
		t.Fatalf("handler returned %v", err)
	}
	rows, _, _ := a.db.LoadSettings(context.Background())
	if _, found := rows["app.update_checks"]; found {
		t.Error("an unsupported update_checks was written")
	}
	if rows["app.site_name"] != "X" {
		t.Error("save did not land")
	}
}

func TestSaveSettingsRefusesEnvReference(t *testing.T) {
	a := loadedSettingsApp(t)
	ctx := context.Background()
	_, before, _ := a.db.LoadSettings(ctx)
	err := a.saveSettings(ctx, map[string]string{"app.site_name": "${NAME}", "app.max_blogs": "9"})
	if err == nil || !strings.Contains(err.Error(), "would be read as an environment reference") {
		t.Fatalf("err %v", err)
	}
	if _, after, _ := a.db.LoadSettings(ctx); after != before {
		t.Error("a refused save wrote something")
	}
}

const staleSettingsMessage = "Settings were changed elsewhere since this page was loaded. Nothing was saved; reload the page and try again."

// settingsFormVersion is what the settings page would put in the hidden
// settings_version field when rendered now.
func settingsFormVersion(a *App) string {
	return strconv.FormatInt(a.settings.Load().version, 10)
}

func postSettingsForm(t *testing.T, a *App, version, siteName string) string {
	t.Helper()
	form := url.Values{
		"site_name":           {siteName},
		"min_username_len":    {"3"},
		"max_blogs":           {"2"},
		"user_invites":        {"none"},
		"default_visibility":  {"public"},
		"theme":               {"write"},
		"uploads_max_size_mb": {"10"},
	}
	if version != "" {
		form.Set("settings_version", version)
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

func TestAdminSaveRefusesStaleForm(t *testing.T) {
	a := loadedSettingsApp(t)
	ctx := context.Background()
	// Two admins load the page at the same version.
	v1, v2 := settingsFormVersion(a), settingsFormVersion(a)
	if m := postSettingsForm(t, a, v1, "First"); !strings.Contains(m, "Configuration saved.") {
		t.Fatalf("first save: %s", m)
	}
	rows, ver, _ := a.db.LoadSettings(ctx)
	m := postSettingsForm(t, a, v2, "Second")
	if !strings.Contains(m, staleSettingsMessage) {
		t.Fatalf("second save: %s", m)
	}
	rows2, ver2, _ := a.db.LoadSettings(ctx)
	if ver2 != ver || rows2["app.site_name"] != "First" || len(rows2) != len(rows) {
		t.Errorf("stale save changed the database: version %d -> %d, site_name %q", ver, ver2, rows2["app.site_name"])
	}
	if a.Config().App.SiteName != "First" {
		t.Errorf("site name %q", a.Config().App.SiteName)
	}
	// Reloaded, the form works again.
	if m := postSettingsForm(t, a, settingsFormVersion(a), "Third"); !strings.Contains(m, "Configuration saved.") {
		t.Errorf("fresh form: %s", m)
	}
}

func TestAdminSaveRefusesMissingOrBadVersion(t *testing.T) {
	a := loadedSettingsApp(t)
	for _, v := range []string{"", "abc", "-1", "99999999999999999999"} {
		if m := postSettingsForm(t, a, v, "Nope"); !strings.Contains(m, staleSettingsMessage) {
			t.Errorf("version %q: %s", v, m)
		}
	}
	if a.Config().App.SiteName == "Nope" {
		t.Error("a refused save landed")
	}
}

// The CLI has no form to be stale: it saves unconditionally, even after
// another save has moved the version.
func TestSaveSettingsWithoutExpectedVersionIsUnconditional(t *testing.T) {
	a := loadedSettingsApp(t)
	ctx := context.Background()
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "Elsewhere"}) // a.settings is now behind
	if err := a.saveSettings(ctx, map[string]string{"app.max_blogs": "4"}); err != nil {
		t.Fatal(err)
	}
	if a.Config().App.MaxBlogs != 4 {
		t.Error("save did not land")
	}
}

func TestSettingsTemplateCarriesVersion(t *testing.T) {
	b, err := os.ReadFile("templates/user/admin/app-settings.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `name="settings_version"`) {
		t.Error("the settings form has no settings_version field")
	}
}
