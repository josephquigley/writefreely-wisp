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
	"testing"
)

// writeConfig puts a minimal config.ini in a temp dir and returns its path.
// Only the keys a test cares about are set: the point of these tests is what
// happens to keys that are ABSENT.
func writeLocalTimelineConfig(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.ini")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	return path
}

const minimalLocalTimelineConfig = `[server]
port = 8080

[app]
host = https://example.com
single_user = false
`

// A hand-written config that never mentions local_timeline must still get the
// documented default of true.
//
// This is the regression test for a real failure: upstream documents the
// default as true, but that default is written by the interactive generator,
// and Load maps the file onto a zero-valued Config. A hand-written file that
// omits the key therefore resolved it to false, which disables the reader AND
// removes the Public option from every blog's settings. The only symptom a
// blog owner sees is "The public reader is currently turned off for this
// community", with nothing in their configuration mentioning it.
func TestLocalTimelineDefaultsToTrueWhenAbsent(t *testing.T) {
	cfg, err := Load(writeLocalTimelineConfig(t, minimalLocalTimelineConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.App.LocalTimeline {
		t.Error("local_timeline absent from the file should default to true, got false")
	}
}

// A deliberate `local_timeline = false` must be honoured. The fix asks the
// file whether the key is present, so it must not override an explicit value.
func TestLocalTimelineHonoursAnExplicitFalse(t *testing.T) {
	cfg, err := Load(writeLocalTimelineConfig(t, minimalLocalTimelineConfig+"local_timeline = false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.App.LocalTimeline {
		t.Error("an explicit local_timeline = false should be honoured, got true")
	}
}

// An explicit true is obviously fine, but pin it so a future change to the
// presence check cannot invert the meaning without a test noticing.
func TestLocalTimelineHonoursAnExplicitTrue(t *testing.T) {
	cfg, err := Load(writeLocalTimelineConfig(t, minimalLocalTimelineConfig+"local_timeline = true\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.App.LocalTimeline {
		t.Error("an explicit local_timeline = true should be honoured, got false")
	}
}
