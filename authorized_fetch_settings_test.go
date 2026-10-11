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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthorizedFetchIsImportedFromConfigINI(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "config.ini")
	writeTestINI(t, ini, `[server]
port = 8080
[database]
type     = sqlite3
filename = `+filepath.Join(dir, "wf.db")+`
[app]
host             = https://blog.example
authorized_fetch = true
`)
	require.NoError(t, CreateSchema(NewApp(ini)))

	row, err := SettingGet(NewApp(ini), "app.authorized_fetch")
	require.NoError(t, err)
	assert.Equal(t, "true", row.Value)
}

func TestAuthorizedFetchSettingsSet(t *testing.T) {
	app := cliTestApp(t)
	row, err := SettingGet(app, "app.authorized_fetch")
	require.NoError(t, err)
	assert.Equal(t, "false", row.Value)

	require.NoError(t, SettingSet(NewApp(app.cfgFile), "app.authorized_fetch", "true"))
	row, _ = SettingGet(NewApp(app.cfgFile), "app.authorized_fetch")
	assert.Equal(t, "true", row.Value)

	assert.Error(t, SettingSet(NewApp(app.cfgFile), "app.authorized_fetch", "sometimes"))
}

func TestAuthorizedFetchReadsTheLiveSetting(t *testing.T) {
	// A save on one node takes effect on another with its next request,
	// without a restart.
	a := loadedSettingsApp(t)
	b := secondNode(t, a)
	r := apRequest("GET", "https://blog.example/api/collections/x", nil)
	assert.NoError(t, b.requireAuthorizedFetch(r))

	require.NoError(t, a.saveSettings(context.Background(), map[string]string{"app.authorized_fetch": "true"}))
	b.refreshSettings(context.Background())
	assert.Equal(t, ErrFederationNotAllowed, b.requireAuthorizedFetch(r))
}

func TestAdminSettingsPageSavesAuthorizedFetch(t *testing.T) {
	a := loadedSettingsApp(t)
	save := func(on bool) {
		form := url.Values{
			"settings_version":    {settingsFormVersion(a)},
			"site_name":           {"X"},
			"min_username_len":    {"3"},
			"max_blogs":           {"2"},
			"user_invites":        {"none"},
			"default_visibility":  {"public"},
			"theme":               {"write"},
			"uploads_max_size_mb": {"10"},
		}
		if on {
			form.Set("authorized_fetch", "on")
		}
		r := httptest.NewRequest("POST", "/admin/settings", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		err := handleAdminUpdateConfig(a, nil, httptest.NewRecorder(), r)
		require.Contains(t, err.Error(), "Configuration+saved")
	}
	save(true)
	assert.True(t, a.Config().App.AuthorizedFetch)
	save(false)
	assert.False(t, a.Config().App.AuthorizedFetch)
}
