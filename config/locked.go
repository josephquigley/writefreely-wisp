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
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/go-ini/ini"
)

// LockedSettingsKey names, in config.ini's unnamed top-of-file section, the
// database settings that config.ini sets instead: a comma-separated list of
// registry names such as "app.private, app.federation_allowlist". A locked
// setting is read from the file on every start, never imported into the
// database or stripped from the file, overrides any database row, and
// cannot be changed from the admin page or `writefreely settings set`.
// It is for deployments that render config.ini from their own source of
// truth and want that source to win. See docs/settings.md.
const LockedSettingsKey = "locked_settings"

// ParseLockedNames splits a locked_settings value into setting names.
// Whitespace around each entry is trimmed, empty entries are skipped and a
// repeated name is kept once. Names are matched exactly. An unknown name, or
// a bootstrap one (which config.ini already holds), is an error naming it.
func ParseLockedNames(list string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(list, ",") {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		if _, ok := LookupSetting(name); !ok {
			if IsBootstrap(name) {
				return nil, fmt.Errorf("%s: %q is a config.ini setting already and cannot be locked; remove it from the list", LockedSettingsKey, name)
			}
			msg := fmt.Sprintf("%s: no setting called %q", LockedSettingsKey, name)
			if s := SuggestSettings(name); len(s) > 0 {
				msg += fmt.Sprintf("; did you mean %s?", strings.Join(s, " or "))
			}
			return nil, fmt.Errorf("%s", msg)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// LockedSettings returns each setting fname locks, mapped to its value as
// stored text. cfg must be what Load(fname) returned.
//
// A locked key present in the file takes its value from cfg, the same value
// an import would have stored. A locked key absent from the file takes the
// value the instance runs with without it: cfg's (Load's zero, or the fix-up
// Load applies for theme and local_timeline), or the default when the
// registry refuses that zero, exactly as the import does it. A present value
// that does not parse, or that the registry refuses, is an error: the
// operator wrote it and asked for it to win, so it is not replaced quietly.
func LockedSettings(fname string, cfg *Config) (map[string]string, error) {
	f, err := ini.Load(fname)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil // no file to lock anything
	}
	if err != nil {
		return nil, err
	}
	if err := checkEnvRefs(f); err != nil {
		return nil, err
	}
	f.ValueMapper = expandEnvRef
	k, err := f.Section("").GetKey(LockedSettingsKey)
	if err != nil {
		return map[string]string{}, nil // no list: nothing is locked
	}
	names, err := ParseLockedNames(k.String())
	if err != nil {
		return nil, fmt.Errorf("%s: %v", fname, err)
	}
	defs := SettingDefaults()
	out := make(map[string]string, len(names))
	for _, name := range names {
		s, _ := LookupSetting(name)
		sec, key := splitSettingName(name)
		v := s.Get(cfg)
		var scratch Config
		if fs, err := f.GetSection(sec); err == nil && fs.HasKey(key) {
			// MapTo, which built cfg, reads a value it cannot parse as zero
			// without a word. Ask the file directly.
			fk := fs.Key(key)
			var perr error
			switch s.Kind {
			case KindBool:
				_, perr = fk.Bool()
			case KindInt:
				_, perr = fk.Int()
			}
			if perr != nil {
				return nil, fmt.Errorf("%s: locked setting %s = %q does not parse: %v", fname, name, fk.String(), perr)
			}
			if err := s.Set(&scratch, v); err != nil {
				return nil, fmt.Errorf("%s: locked setting %v", fname, err)
			}
		} else if err := s.Set(&scratch, v); err != nil {
			v = defs[name]
		}
		out[name] = v
	}
	return out, nil
}

// UnlockedSettingNames is DBSettingNames without the names in locked, in
// registry order: the settings the database holds for this node.
func UnlockedSettingNames(locked map[string]string) []string {
	var names []string
	for _, n := range DBSettingNames() {
		if _, ok := locked[n]; !ok {
			names = append(names, n)
		}
	}
	return names
}

// OverlayLocked returns a copy of rows with every locked value put over the
// stored one. rows itself is left alone: the stored values stay in the
// database, ignored, and come back when a name leaves the list.
func OverlayLocked(rows, locked map[string]string) map[string]string {
	out := make(map[string]string, len(rows)+len(locked))
	for k, v := range rows {
		out[k] = v
	}
	for k, v := range locked {
		out[k] = v
	}
	return out
}
