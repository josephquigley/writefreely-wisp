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
	f, err := ini.Load(fname)
	if err != nil {
		return nil, err
	}
	present := keysPresentIn(f, names)
	if len(present) == 0 {
		return nil, nil
	}
	for _, name := range present {
		sec, key := splitSettingName(name)
		f.Section(sec).DeleteKey(key)
	}

	tmp, err := os.CreateTemp(filepath.Dir(fname), ".config.ini.*")
	if err != nil {
		return present, err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if _, err := f.WriteTo(tmp); err != nil {
		tmp.Close()
		return present, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return present, err
	}
	if err := tmp.Close(); err != nil {
		return present, err
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		return present, err
	}
	if err := os.Rename(tmp.Name(), fname); err != nil {
		return present, err
	}
	// Sync the directory to ensure the rename is durable.
	// This is best-effort; if the directory is read-only, let the
	// caller decide whether to treat that as an error (they have the
	// keys back in present to report what changed).
	if d, err := os.Open(filepath.Dir(fname)); err == nil {
		d.Sync()
		d.Close()
	}
	return present, nil
}
