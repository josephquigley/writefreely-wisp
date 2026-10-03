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

	"github.com/writeas/web-core/log"
	"github.com/writefreely/go-nodeinfo"
	"github.com/writefreely/writefreely/config"
)

// configPath is the bootstrap file this App reads.
func (app *App) configPath() string {
	if app.cfgFile == "" {
		return config.FileName
	}
	return app.cfgFile
}

// loadSettings builds a settings snapshot from the bootstrap
// configuration and the database, and puts it in force.
func (app *App) loadSettings(ctx context.Context) error {
	app.settingsMu.Lock()
	defer app.settingsMu.Unlock()
	return app.loadSettingsLocked(ctx)
}

// loadSettingsLocked is loadSettings for a caller holding settingsMu.
//
// Before `db migrate` has created the tables there is nothing to load:
// the instance runs on config.ini as it always did, and Config() keeps
// returning the bootstrap configuration.
func (app *App) loadSettingsLocked(ctx context.Context) error {
	ok, err := app.db.settingsTableExists(ctx)
	if err != nil {
		return err
	}
	if !ok {
		log.Info("Settings are still read from %s: run `writefreely db migrate`, then restart, to move them into the database.", app.configPath())
		return nil
	}
	rows, ver, err := app.db.LoadSettings(ctx)
	if err != nil {
		return err
	}
	cfg, warnings := config.ApplySettings(app.cfg, rows)
	for _, w := range warnings {
		log.Error("settings: %s", w)
	}
	if !updateChecksSupported {
		// See updateChecksSupported: whatever a row says, nothing is checked.
		// cfg is a fresh Config no one else holds yet, so this is safe.
		cfg.App.UpdateChecks = false
	}
	allow, err := buildFederationAllowlist(cfg)
	if err != nil {
		return err
	}
	if app.fedKeys == nil {
		app.fedKeys = newKeyCache()
	}
	app.settings.Store(&settingsSnapshot{cfg: cfg, fedAllowlist: allow, version: ver})
	return nil
}

// refreshSettings reloads the settings if another node (or the CLI) has
// saved since this node last loaded them. It costs one primary-key read.
// On any failure the cached snapshot stays in force: a database that is
// unreachable mid-failover must not take the configuration with it.
func (app *App) refreshSettings(ctx context.Context) {
	cur := app.settings.Load()
	if cur == nil {
		return // settings are not in the database (yet); nothing to refresh
	}
	ver, err := app.db.SettingsVersion(ctx)
	if err != nil {
		log.Error("settings: version check failed, keeping cached settings: %v", err)
		return
	}
	if ver == cur.version {
		return
	}
	app.settingsMu.Lock()
	defer app.settingsMu.Unlock()
	if s := app.settings.Load(); s != nil && s.version == ver {
		return // another request reloaded while this one waited
	}
	if err := app.loadSettingsLocked(ctx); err != nil {
		log.Error("settings: reload failed, keeping cached settings: %v", err)
	}
}

// settingsMiddleware checks for changed settings before every request.
func (app *App) settingsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.refreshSettings(r.Context())
		next.ServeHTTP(w, r)
	})
}

// nodeInfoHandler serves NodeInfo (or its discovery document) from the
// settings in force when the request arrives. The service is rebuilt per
// request: NewService only copies a config, and the site name, privacy,
// blog limit and the like are settings that can change at any time.
func (app *App) nodeInfoHandler(discover bool) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ni := nodeinfo.NewService(*nodeInfoConfig(app.db, app.Config()), nodeInfoResolver{app, app.db})
		if discover {
			ni.NodeInfoDiscover(w, r)
			return
		}
		ni.NodeInfo(w, r)
	})
}
