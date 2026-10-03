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

	"github.com/writeas/web-core/log"

	"github.com/writefreely/writefreely/config"
)

// SettingRow is one setting as the CLI shows it. Default is true when the
// value in force equals the built-in default, whether or not the database
// has a row for it. Imported is false while the settings are still only in
// config.ini (nothing has been moved into the database), in which case
// Value is what config.ini says.
type SettingRow struct {
	Name     string
	Value    string
	Default  bool
	Imported bool
}

const notImportedNote = "settings have not been moved into the database yet; showing config.ini"

// openSettings connects apper's App for a settings command. Only a command
// that writes (`set`) claims the import and strips config.ini; the read-only
// ones leave both alone. The caller must call shutdown.
func openSettings(apper Apper, importFirst bool) (*App, error) {
	app := apper.App()
	apper.LoadConfig()
	connectToDatabase(app)
	ctx := context.Background()
	if !importFirst {
		return app, nil
	}
	if _, err := app.importSettings(ctx); err != nil {
		shutdown(app)
		return nil, err
	}
	if err := app.loadSettings(ctx); err != nil {
		shutdown(app)
		return nil, err
	}
	return app, nil
}

// effectiveSettings returns every DB setting's value in force, and whether
// the database holds them. Before the first import it is what config.ini
// says (what an import would store).
func effectiveSettings(app *App) (map[string]string, bool, error) {
	ctx := context.Background()
	ok, err := app.db.settingsTableExists(ctx)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, errors.New("settings are not in the database yet: run `writefreely db migrate`, then try again")
	}
	rows, ver, err := app.db.LoadSettings(ctx)
	if err != nil {
		return nil, false, err
	}
	if ver == 0 {
		log.Info(notImportedNote)
		return app.normalisedSettings(), false, nil
	}
	cur, _ := config.ApplySettings(app.cfg, rows)
	return config.SettingsFrom(cur), true, nil
}

func settingRows(app *App) ([]SettingRow, error) {
	vals, imported, err := effectiveSettings(app)
	if err != nil {
		return nil, err
	}
	defs := config.SettingDefaults()
	var out []SettingRow
	for _, name := range config.DBSettingNames() {
		v := vals[name]
		out = append(out, SettingRow{Name: name, Value: v, Default: v == defs[name], Imported: imported})
	}
	return out, nil
}

// SettingsList returns every DB setting with its value in force.
func SettingsList(apper Apper) ([]SettingRow, error) {
	app, err := openSettings(apper, false)
	if err != nil {
		return nil, err
	}
	defer shutdown(app)
	return settingRows(app)
}

// SettingGet returns one setting. A bootstrap or unknown name is an error
// saying where to look instead.
func SettingGet(apper Apper, name string) (SettingRow, error) {
	if _, ok := config.LookupSetting(name); !ok {
		return SettingRow{}, settingNameError(name, apper.App().configPath())
	}
	app, err := openSettings(apper, false)
	if err != nil {
		return SettingRow{}, err
	}
	defer shutdown(app)
	rows, err := settingRows(app)
	if err != nil {
		return SettingRow{}, err
	}
	for _, r := range rows {
		if r.Name == name {
			return r, nil
		}
	}
	return SettingRow{}, settingNameError(name, app.configPath())
}

// SettingSet validates and saves one setting. Running servers pick it up
// on their next request.
func SettingSet(apper Apper, name, value string) error {
	if _, ok := config.LookupSetting(name); !ok {
		return settingNameError(name, apper.App().configPath())
	}
	app, err := openSettings(apper, true)
	if err != nil {
		return err
	}
	defer shutdown(app)
	return app.saveSettings(context.Background(), map[string]string{name: value})
}

// SettingsExport renders every setting in force as a config.ini fragment,
// for pasting back before a downgrade. It contains no secrets.
func SettingsExport(apper Apper) (string, error) {
	app, err := openSettings(apper, false)
	if err != nil {
		return "", err
	}
	defer shutdown(app)
	vals, _, err := effectiveSettings(app)
	if err != nil {
		return "", err
	}
	return config.ExportINI(vals)
}
