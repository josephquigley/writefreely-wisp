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
	"reflect"
	"strings"
	"testing"
)

// TestEveryFieldClassified fails when a Config field is neither a DB
// setting nor bootstrap, or is both. An upstream merge that adds a field
// lands here, which forces someone to decide where it lives.
func TestEveryFieldClassified(t *testing.T) {
	ct := reflect.TypeOf(Config{})
	for i := 0; i < ct.NumField(); i++ {
		sec := iniName(ct.Field(i))
		st := ct.Field(i).Type
		for j := 0; j < st.NumField(); j++ {
			key := iniName(st.Field(j))
			if key == "" || key == "-" {
				continue
			}
			name := sec + "." + key
			_, db := LookupSetting(name)
			boot := IsBootstrap(name)
			if db == boot {
				t.Errorf("%s: db=%v bootstrap=%v; exactly one must be true", name, db, boot)
			}
		}
	}
}

func TestRegistryKindsMatchFields(t *testing.T) {
	c := New()
	for _, name := range DBSettingNames() {
		s, _ := LookupSetting(name)
		f, err := field(c, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := map[SettingKind]reflect.Kind{KindString: reflect.String, KindBool: reflect.Bool, KindInt: reflect.Int}[s.Kind]
		if f.Kind() != want {
			t.Errorf("%s: registry kind %v, field kind %v", name, s.Kind, f.Kind())
		}
	}
}

func TestSettingRoundTrip(t *testing.T) {
	values := map[string]string{
		"app.site_name":            "Paisans, \"quoted\" = yes",
		"app.private":              "true",
		"app.max_blogs":            "4",
		"app.federation_allowlist": "a.example, *.b.example",
		"uploads.max_size_mb":      "25",
	}
	c := New()
	for name, v := range values {
		s, ok := LookupSetting(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if err := s.Set(c, v); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
		if got := s.Get(c); got != v {
			t.Errorf("%s: got %q, want %q", name, got, v)
		}
	}
}

func TestSettingValidation(t *testing.T) {
	bad := map[string]string{
		"app.private":            "maybe",
		"app.max_blogs":          "-1",
		"app.min_username_len":   "0",
		"app.user_invites":       "everyone",
		"app.default_visibility": "secret",
		"app.theme":              "",
		"uploads.max_size_mb":    "0",
	}
	c := New()
	for name, v := range bad {
		s, ok := LookupSetting(name)
		if !ok {
			continue // the unknown name is checked by TestLookupUnknown
		}
		if err := s.Set(c, v); err == nil {
			t.Errorf("%s = %q accepted", name, v)
		}
	}
}

func TestLookupUnknownAndBootstrap(t *testing.T) {
	if _, ok := LookupSetting("app.host"); ok {
		t.Error("app.host must not be a DB setting")
	}
	if !IsBootstrap("app.host") || !IsBootstrap("database.password") || !IsBootstrap("oauth.generic.client_secret") {
		t.Error("bootstrap classification wrong")
	}
	if _, ok := LookupSetting("app.nope"); ok {
		t.Error("unknown name found")
	}
}

func TestSettingDefaultsLocalTimelineTrue(t *testing.T) {
	if got := SettingDefaults()["app.local_timeline"]; got != "true" {
		t.Errorf("local_timeline default %q, want true", got)
	}
}

func TestApplySettings(t *testing.T) {
	base := New()
	base.App.Host = "https://blog.example"
	base.App.SiteName = "from ini"
	got, warnings := ApplySettings(base, map[string]string{
		"app.site_name": "from db",
		"app.max_blogs": "not a number",
		"app.retired":   "x",
	})
	if got.App.SiteName != "from db" {
		t.Errorf("site_name %q", got.App.SiteName)
	}
	if got.App.MaxBlogs != New().App.MaxBlogs {
		t.Errorf("invalid max_blogs did not fall back to default: %d", got.App.MaxBlogs)
	}
	if got.App.Host != "https://blog.example" {
		t.Errorf("bootstrap field lost: %q", got.App.Host)
	}
	if !got.App.LocalTimeline {
		t.Error("absent local_timeline must default to true")
	}
	if base.App.SiteName != "from ini" {
		t.Error("ApplySettings mutated base")
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "app.max_blogs") || !strings.Contains(joined, "app.retired") {
		t.Errorf("warnings missing: %v", warnings)
	}
}

func TestExportINI(t *testing.T) {
	out, err := ExportINI(map[string]string{"app.site_name": "A = B", "uploads.enabled": "true"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[app]", "site_name", "A = B", "[uploads]", "enabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q:\n%s", want, out)
		}
	}
}

func TestSuggestSettings(t *testing.T) {
	got := SuggestSettings("app.privat")
	if len(got) == 0 || got[0] != "app.private" {
		t.Errorf("suggestions %v", got)
	}
}

func TestIniNameStripsOptions(t *testing.T) {
	type s struct {
		A string `ini:"type,omitempty"`
		B string `ini:"plain"`
	}
	ty := reflect.TypeOf(s{})
	if got := iniName(ty.Field(0)); got != "type" {
		t.Errorf("omitempty: %q", got)
	}
	if got := iniName(ty.Field(1)); got != "plain" {
		t.Errorf("plain: %q", got)
	}
}

func TestFederationAllowlistSettingValidatesSyntax(t *testing.T) {
	s, _ := LookupSetting("app.federation_allowlist")
	var c Config
	for _, bad := range []string{"*", "a.*.b", "*.*.example.org", "*."} {
		if err := s.Set(&c, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"", "peer.example, *.example.org", " , "} {
		if err := s.Set(&c, ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}
