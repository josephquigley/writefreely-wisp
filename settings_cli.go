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

	"github.com/writefreely/writefreely/config"
)

// SettingRow is one setting as the CLI shows it. Default is true when the
// database has no row and the value is the built-in default.
type SettingRow struct {
	Name    string
	Value   string
	Default bool
}

// openSettings connects apper's App for a settings command. It imports
// config.ini first, so the CLI works on a fresh install that has never
// served a request. The caller must call shutdown.
func openSettings(apper Apper) (*App, error) {
	app := apper.App()
	apper.LoadConfig()
	connectToDatabase(app)
	ctx := context.Background()
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

func settingRows(app *App) ([]SettingRow, error) {
	rows, _, err := app.db.LoadSettings(context.Background())
	if err != nil {
		return nil, err
	}
	cur := app.Config()
	var out []SettingRow
	for _, name := range config.DBSettingNames() {
		s, _ := config.LookupSetting(name)
		_, stored := rows[name]
		out = append(out, SettingRow{Name: name, Value: s.Get(cur), Default: !stored})
	}
	return out, nil
}

// SettingsList returns every DB setting with its value in force.
func SettingsList(apper Apper) ([]SettingRow, error) {
	app, err := openSettings(apper)
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
	app, err := openSettings(apper)
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
	app, err := openSettings(apper)
	if err != nil {
		return err
	}
	defer shutdown(app)
	return app.saveSettings(context.Background(), map[string]string{name: value})
}

// SettingsExport renders every setting in force as a config.ini fragment,
// for pasting back before a downgrade. It contains no secrets.
func SettingsExport(apper Apper) (string, error) {
	app, err := openSettings(apper)
	if err != nil {
		return "", err
	}
	defer shutdown(app)
	return config.ExportINI(config.SettingsFrom(app.Config()))
}
