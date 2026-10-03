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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// secondNode is another App on the same database: its own snapshot, the
// same rows. That is all a second node is, as far as settings go.
func secondNode(t *testing.T, a *App) *App {
	t.Helper()
	cfg := *a.cfg
	b := &App{cfg: &cfg, cfgFile: a.cfgFile, db: a.db}
	if err := b.loadSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLoadSettingsAppliesRows(t *testing.T) {
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	if _, err := a.db.SaveSettings(ctx, map[string]string{"app.site_name": "From DB"}); err != nil {
		t.Fatal(err)
	}
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.Config().App.SiteName; got != "From DB" {
		t.Errorf("site_name %q", got)
	}
	if a.Config().App.Host != "https://blog.example" {
		t.Error("bootstrap host lost")
	}
}

func TestLoadSettingsIgnoresINIValues(t *testing.T) {
	a := newSettingsTestApp(t, "")
	a.cfg.App.SiteName = "stale ini value"
	ctx := context.Background()
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "DB value"})
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.Config().App.SiteName; got != "DB value" {
		t.Errorf("ini value won: %q", got)
	}
	// Still true after a reload triggered by an unrelated save.
	a.db.SaveSettings(ctx, map[string]string{"app.max_blogs": "3"})
	a.refreshSettings(ctx)
	if got := a.Config().App.SiteName; got != "DB value" {
		t.Errorf("ini value won after reload: %q", got)
	}
}

func TestLoadSettingsWithoutTable(t *testing.T) {
	a := newSettingsTestApp(t, "")
	for _, q := range []string{"DROP TABLE app_settings", "DROP TABLE app_settings_version"} {
		if _, err := a.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a.cfg.App.SiteName = "ini only"
	if err := a.loadSettings(context.Background()); err != nil {
		t.Fatalf("missing table must not be fatal: %v", err)
	}
	if got := a.Config().App.SiteName; got != "ini only" {
		t.Errorf("site_name %q", got)
	}
	a.refreshSettings(context.Background()) // must not panic
}

func TestSecondNodeSeesSaveOnNextRequest(t *testing.T) {
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	b := secondNode(t, a)

	a.db.SaveSettings(ctx, map[string]string{
		"app.private":              "true",
		"app.federation_allowlist": "peer.example",
	})

	var seen string
	h := b.settingsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = b.Config().App.FederationAllowlist
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if seen != "peer.example" {
		t.Errorf("node B saw %q", seen)
	}
	if !b.federationAllowlist()["peer.example"] {
		t.Error("node B's derived allowlist was not rebuilt")
	}
}

func TestRefreshKeepsSnapshotOnError(t *testing.T) {
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "Kept"})
	a.loadSettings(ctx)
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "Not yet seen"})
	dead, cancel := context.WithCancel(ctx)
	cancel()
	a.refreshSettings(dead) // the version check fails: logs, keeps the cache
	if got := a.Config().App.SiteName; got != "Kept" {
		t.Errorf("site_name %q", got)
	}
}

func TestReloadIsRaceFree(t *testing.T) {
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	a.loadSettings(ctx)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				a.refreshSettings(ctx)
				_ = a.Config().App.SiteName
			}
		}()
	}
	for j := 0; j < 20; j++ {
		a.db.SaveSettings(ctx, map[string]string{"app.max_blogs": "2"})
	}
	wg.Wait()
}

// This fork hard-disables update checks; a database row must not turn them on.
func TestLoadSettingsKeepsUpdateChecksOff(t *testing.T) {
	if updateChecksSupported {
		t.Skip("update checks are supported in this build")
	}
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	a.db.SaveSettings(ctx, map[string]string{"app.update_checks": "true"})
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Config().App.UpdateChecks {
		t.Error("update_checks row enabled update checks")
	}
}

func TestNodeInfoUsesSettingsInForce(t *testing.T) {
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "Before"})
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	h := a.settingsMiddleware(a.nodeInfoHandler(false))
	get := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/nodeinfo", nil))
		return rec.Body.String()
	}
	if body := get(); !strings.Contains(body, "Before") {
		t.Fatalf("nodeinfo missing site name: %s", body)
	}
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "After"})
	if body := get(); !strings.Contains(body, "After") || strings.Contains(body, "Before") {
		t.Errorf("nodeinfo stale after settings change: %s", body)
	}
}
