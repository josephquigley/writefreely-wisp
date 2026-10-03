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
	"errors"
	"fmt"
	"net/http"
	"strings"

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

// settingsImport is what one importSettings run did, for logging and tests.
type settingsImport struct {
	Imported bool     // this node moved the settings into the database
	Drift    []string // DB-bound keys in this node's ini that disagree with the database
	Stripped []string // keys removed from this node's ini
	Left     []string // keys still in the ini because it could not be written
	StripErr error
}

// importSettings moves this node's settings into the database if no node
// has yet, then removes the DB-bound keys from its own config.ini.
//
// It imports the values the instance was running with (app.cfg as loaded),
// not the keys as written: a key absent from the file was in force as its
// zero value, and storing config.New()'s default instead would quietly
// change the instance on upgrade.
//
// It is idempotent and runs at every start and in `db migrate`.
func (app *App) importSettings(ctx context.Context) (settingsImport, error) {
	var res settingsImport
	ok, err := app.db.settingsTableExists(ctx)
	if err != nil || !ok {
		return res, err
	}
	present, err := config.KeysPresent(app.configPath(), config.DBSettingNames())
	if err != nil {
		return res, err
	}
	effective := config.SettingsFrom(app.cfg)

	res.Imported, err = app.db.ClaimSettingsImport(ctx, effective)
	if err != nil {
		return res, err
	}
	if res.Imported {
		log.Info("Moved %d settings from %s into the database.", len(effective), app.configPath())
	} else if len(present) > 0 {
		rows, _, err := app.db.LoadSettings(ctx)
		if err != nil {
			return res, err
		}
		defs := config.SettingDefaults()
		for _, name := range present {
			dbv, ok := rows[name]
			if !ok {
				dbv = defs[name]
			}
			if effective[name] != dbv {
				res.Drift = append(res.Drift, name)
			}
		}
		if len(res.Drift) > 0 {
			log.Error("settings: %s disagrees with the database on %s; the database value is in force.", app.configPath(), strings.Join(res.Drift, ", "))
		}
	}

	if len(present) == 0 {
		return res, nil
	}
	res.Stripped, res.StripErr = config.StripKeys(app.configPath(), present)
	if res.StripErr != nil {
		res.Left, res.Stripped = res.Stripped, nil
		log.Error("settings: could not remove %s from %s (%v). They are ignored; delete them by hand.", strings.Join(res.Left, ", "), app.configPath(), res.StripErr)
	} else {
		log.Info("Removed %s from %s; they now live in the database.", strings.Join(res.Stripped, ", "), app.configPath())
	}
	return res, nil
}

// settingNameError explains why name cannot be set at runtime.
func settingNameError(name, cfgPath string) error {
	if config.IsBootstrap(name) {
		return fmt.Errorf("%s lives in %s (bootstrap); edit it there and restart", name, cfgPath)
	}
	if s := config.SuggestSettings(name); len(s) > 0 {
		return fmt.Errorf("no setting called %s; did you mean %s?", name, strings.Join(s, " or "))
	}
	return fmt.Errorf("no setting called %s", name)
}

// saveSettings validates changes against the settings in force, writes
// them, and reloads this node at once. Other nodes pick them up on their
// next request. Nothing is written unless every value, and the
// combination, is valid.
func (app *App) saveSettings(ctx context.Context, changes map[string]string) error {
	if app.settings.Load() == nil {
		return errors.New("settings are not in the database yet: run `writefreely db migrate` and restart")
	}
	cur := app.Config()
	cand := *cur
	stored := map[string]string{}
	for _, name := range sortedSettingNames(changes) {
		s, ok := config.LookupSetting(name)
		if !ok {
			return settingNameError(name, app.configPath())
		}
		if err := s.Set(&cand, changes[name]); err != nil {
			return err
		}
		stored[name] = s.Get(&cand) // normalised text: "TRUE" -> "true"
	}
	if _, err := buildFederationAllowlist(&cand); err != nil {
		return err
	}
	if cand.Uploads.Enabled && !cur.Uploads.Enabled {
		if err := app.ensureUploadsWritable(); err != nil {
			return fmt.Errorf("cannot enable uploads on this node: %v", err)
		}
	}
	if _, err := app.db.SaveSettings(ctx, stored); err != nil {
		return err
	}
	app.settingsMu.Lock()
	defer app.settingsMu.Unlock()
	return app.loadSettingsLocked(ctx)
}
