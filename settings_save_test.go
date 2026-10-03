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
