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
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// settingsReadBack lets a test alter what ClaimSettingsImport reads back,
// to prove a mismatch aborts the import. It is the identity otherwise.
var settingsReadBack = func(m map[string]string) map[string]string { return m }

// settingsTableExists reports whether migration V20 has run. A server
// started before `db migrate` runs on its config.ini alone.
func (db *datastore) settingsTableExists(ctx context.Context) (bool, error) {
	return db.dialectOrDefault().TableExists(ctx, db.DB, "app_settings_version")
}

// SettingsVersion returns the settings counter. Every node runs this once
// per request, so it is a primary-key read and nothing else.
func (db *datastore) SettingsVersion(ctx context.Context) (int64, error) {
	var v int64
	err := db.QueryRowContext(ctx, "SELECT version FROM app_settings_version WHERE id = 1").Scan(&v)
	return v, err
}

// LoadSettings returns every stored setting and the version they belong
// to. The version is read before and after the rows and the read retried
// if a save landed in between, which gives a consistent pair on every
// engine without depending on its default isolation level.
func (db *datastore) LoadSettings(ctx context.Context) (map[string]string, int64, error) {
	for attempt := 0; attempt < 5; attempt++ {
		before, err := db.SettingsVersion(ctx)
		if err != nil {
			return nil, 0, err
		}
		rows, err := db.readSettingRows(ctx, db.DB)
		if err != nil {
			return nil, 0, err
		}
		after, err := db.SettingsVersion(ctx)
		if err != nil {
			return nil, 0, err
		}
		if before == after {
			return rows, after, nil
		}
	}
	return nil, 0, fmt.Errorf("settings kept changing while being read")
}

type settingsQueryer interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
}

func (db *datastore) readSettingRows(ctx context.Context, q settingsQueryer) (map[string]string, error) {
	rs, err := q.QueryContext(ctx, "SELECT name, value FROM app_settings")
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	m := map[string]string{}
	for rs.Next() {
		var name, value string
		if err := rs.Scan(&name, &value); err != nil {
			return nil, err
		}
		m[name] = value
	}
	return m, rs.Err()
}

func (db *datastore) upsertSetting(ctx context.Context, t *sql.Tx, name, value string) error {
	var err error
	switch db.driverName {
	case driverSQLite:
		_, err = t.ExecContext(ctx, "INSERT OR REPLACE INTO app_settings (name, value) VALUES (?, ?)", name, value)
	case driverMySQL, driverPostgres:
		_, err = t.ExecContext(ctx, "INSERT INTO app_settings (name, value) VALUES (?, ?) "+db.upsert("name")+" value = ?", name, value, value)
	default:
		unsupportedDriver("upsertSetting", db.driverName)
	}
	return err
}

func sortedSettingNames(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// SaveSettings writes values and bumps the version in one transaction,
// and returns the new version. Callers validate first; this stores text.
func (db *datastore) SaveSettings(ctx context.Context, values map[string]string) (int64, error) {
	t, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer t.Rollback()
	for _, name := range sortedSettingNames(values) {
		if err := db.upsertSetting(ctx, t, name, values[name]); err != nil {
			return 0, fmt.Errorf("save %s: %v", name, err)
		}
	}
	if _, err := t.ExecContext(ctx, "UPDATE app_settings_version SET version = version + 1 WHERE id = 1"); err != nil {
		return 0, err
	}
	var v int64
	if err := t.QueryRowContext(ctx, "SELECT version FROM app_settings_version WHERE id = 1").Scan(&v); err != nil {
		return 0, err
	}
	return v, t.Commit()
}

// ClaimSettingsImport imports values if no node has yet: it moves the
// version from 0 to 1, writes values, reads them back, and commits only if
// every one matches. It returns false, writing nothing, when the version
// was already past 0 (an earlier import or a save, or another node that
// won the race; on MySQL and Postgres the losing UPDATE waits for the
// winner and then matches no row).
func (db *datastore) ClaimSettingsImport(ctx context.Context, values map[string]string) (bool, error) {
	t, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer t.Rollback()
	res, err := t.ExecContext(ctx, "UPDATE app_settings_version SET version = 1 WHERE id = 1 AND version = 0")
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, err
	} else if n == 0 {
		return false, nil
	}
	for _, name := range sortedSettingNames(values) {
		if err := db.upsertSetting(ctx, t, name, values[name]); err != nil {
			return false, fmt.Errorf("import %s: %v", name, err)
		}
	}
	got, err := db.readSettingRows(ctx, t)
	if err != nil {
		return false, err
	}
	got = settingsReadBack(got)
	var bad []string
	for _, name := range sortedSettingNames(values) {
		if got[name] != values[name] {
			bad = append(bad, name)
		}
	}
	if len(bad) > 0 {
		return false, fmt.Errorf("settings read back differently from what was written: %s; config.ini left untouched", strings.Join(bad, ", "))
	}
	return true, t.Commit()
}
