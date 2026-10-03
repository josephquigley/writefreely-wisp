/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package config

import (
	"os"
	"path/filepath"

	"github.com/go-ini/ini"
)

// KeysPresent returns which of names fname sets, in the order given. No
// ValueMapper is installed, so ${VAR} references are not resolved and an
// unset variable is not an error here.
func KeysPresent(fname string, names []string) ([]string, error) {
	f, err := ini.Load(fname)
	if err != nil {
		return nil, err
	}
	return keysPresentIn(f, names), nil
}

func keysPresentIn(f *ini.File, names []string) []string {
	var present []string
	for _, name := range names {
		sec, key := splitSettingName(name)
		if s, err := f.GetSection(sec); err == nil && s.HasKey(key) {
			present = append(present, name)
		}
	}
	return present
}

// SettingsLocationKey is the marker config.ini carries, in its unnamed
// top-of-file section, once the settings live in the database. An older
// binary ignores it. A newer one that finds it beside a database holding no
// settings refuses to start on defaults; see App.importSettings.
const (
	SettingsLocationKey   = "settings_location"
	SettingsLocationValue = "database"
	settingsMarkerComment = "Community settings live in the database. Do not remove this line; see docs/settings.md."
)

// HasSettingsMarker reports whether fname says its settings were moved into
// the database.
func HasSettingsMarker(fname string) (bool, error) {
	f, err := ini.Load(fname)
	if err != nil {
		return false, err
	}
	k, err := f.Section("").GetKey(SettingsLocationKey)
	return err == nil && k.String() == SettingsLocationValue, nil
}

// MarkSettingsInDatabase puts the settings_location marker at the top of
// fname if it is not there, by the same atomic write StripKeys uses. It
// reports whether it wrote.
func MarkSettingsInDatabase(fname string) (bool, error) {
	return rewriteINI(fname, func(f *ini.File) bool {
		def := f.Section("")
		if k, err := def.GetKey(SettingsLocationKey); err == nil && k.String() == SettingsLocationValue {
			return false
		}
		k := def.Key(SettingsLocationKey)
		k.SetValue(SettingsLocationValue)
		k.Comment = settingsMarkerComment
		return true
	})
}

// StripKeys removes names from fname once their values live in the
// database, and returns the names it removed. Comments on other keys and
// ${VAR} references survive; a comment attached to a removed key goes
// with it.
//
// The new file is written beside the old one and renamed over it, so a
// crash leaves one or the other, never a partial file. No backup is kept.
//
// On error nothing has changed, and the returned names are the keys still
// in the file, for the caller to report.
func StripKeys(fname string, names []string) ([]string, error) {
	var present []string
	wrote, err := rewriteINI(fname, func(f *ini.File) bool {
		present = keysPresentIn(f, names)
		for _, name := range present {
			sec, key := splitSettingName(name)
			f.Section(sec).DeleteKey(key)
		}
		return len(present) > 0
	})
	if err != nil {
		return present, err
	}
	if !wrote {
		return nil, nil
	}
	return present, nil
}

// rewriteINI loads fname, lets edit change it, and if edit reports a change
// writes the result atomically. A symlinked config.ini (a mounted secret,
// configuration management) is resolved first, so the temp file and rename
// happen beside the real file and the link stays a link.
func rewriteINI(fname string, edit func(*ini.File) bool) (bool, error) {
	f, err := ini.Load(fname)
	if err != nil {
		return false, err
	}
	if !edit(f) {
		return false, nil
	}
	target, err := filepath.EvalSymlinks(fname)
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".config.ini.*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if _, err := f.WriteTo(tmp); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return false, err
	}
	// Sync the directory to ensure the rename is durable.
	// This is best-effort; if the directory is read-only, let the
	// caller decide whether to treat that as an error.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return true, nil
}
