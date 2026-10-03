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
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/go-ini/ini"
)

// SettingKind is the Go type a setting's text value parses into.
type SettingKind int

const (
	KindString SettingKind = iota
	KindBool
	KindInt
)

// Setting is one community-policy setting that lives in the database.
// Name is "section.key" as config.ini spells it. Validate, when set, sees
// the raw text before it is parsed.
type Setting struct {
	Name     string
	Kind     SettingKind
	Validate func(string) error
}

// dbSettings is every setting stored in the database, in the order the
// CLI lists them. config.ini holds everything else; see bootstrapSections
// and bootstrapKeys. TestEveryFieldClassified fails on a Config field that
// is in neither, so a new upstream field cannot land unclassified.
var dbSettings = []Setting{
	{Name: "app.site_name", Kind: KindString},
	{Name: "app.site_description", Kind: KindString},
	{Name: "app.theme", Kind: KindString, Validate: validateNonEmpty},
	{Name: "app.editor", Kind: KindString},
	{Name: "app.disable_js", Kind: KindBool},
	{Name: "app.webfonts", Kind: KindBool},
	{Name: "app.landing", Kind: KindString},
	{Name: "app.simple_nav", Kind: KindBool},
	{Name: "app.wf_modesty", Kind: KindBool},
	{Name: "app.chorus", Kind: KindBool},
	{Name: "app.forest", Kind: KindBool},
	{Name: "app.disable_drafts", Kind: KindBool},
	{Name: "app.open_registration", Kind: KindBool},
	{Name: "app.open_deletion", Kind: KindBool},
	{Name: "app.min_username_len", Kind: KindInt, Validate: intRange(1, 100)},
	{Name: "app.max_blogs", Kind: KindInt, Validate: intRange(0, 1<<30)},
	{Name: "app.federation", Kind: KindBool},
	{Name: "app.public_stats", Kind: KindBool},
	{Name: "app.monetization", Kind: KindBool},
	{Name: "app.notes_only", Kind: KindBool},
	{Name: "app.private", Kind: KindBool},
	{Name: "app.federation_allowlist", Kind: KindString, Validate: validateFederationAllowlistText},
	{Name: "app.instance_announce", Kind: KindBool},
	{Name: "app.local_timeline", Kind: KindBool},
	{Name: "app.user_invites", Kind: KindString, Validate: oneOf("", "admin", "user")},
	{Name: "app.default_visibility", Kind: KindString, Validate: oneOf("", "unlisted", "public", "private")},
	{Name: "app.update_checks", Kind: KindBool},
	{Name: "uploads.enabled", Kind: KindBool},
	{Name: "uploads.max_size_mb", Kind: KindInt, Validate: intRange(1, 1<<20)},
}

// Every key in these sections is bootstrap: infrastructure and credentials,
// read from config.ini and never written by a running server.
var bootstrapSections = []string{
	"server", "database", "email",
	"oauth.slack", "oauth.writeas", "oauth.gitlab", "oauth.gitea", "oauth.generic", "storage",
}

// Bootstrap keys in sections that otherwise hold DB settings.
var bootstrapKeys = []string{
	"app.host",                  // instance identity; changing it breaks federation
	"app.single_user",           // decides which routes exist
	"app.disable_password_auth", // depends on the OAuth configuration beside it
	"uploads.dir",               // a path on this node
}

func intRange(min, max int) func(string) error {
	return func(s string) error {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("%q is not a whole number", s)
		}
		if n < min || n > max {
			return fmt.Errorf("%d is outside %d–%d", n, min, max)
		}
		return nil
	}
}

func oneOf(allowed ...string) func(string) error {
	return func(s string) error {
		for _, a := range allowed {
			if s == a {
				return nil
			}
		}
		return fmt.Errorf("%q is not one of %q", s, allowed)
	}
}

// splitSettingName splits "oauth.generic.client_id" into "oauth.generic"
// and "client_id": sections may contain dots, keys never do.
func splitSettingName(name string) (section, key string) {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return "", name
	}
	return name[:i], name[i+1:]
}

// LookupSetting returns the DB setting called name.
func LookupSetting(name string) (Setting, bool) {
	for _, s := range dbSettings {
		if s.Name == name {
			return s, true
		}
	}
	return Setting{}, false
}

// IsBootstrap reports whether name lives in config.ini.
func IsBootstrap(name string) bool {
	sec, _ := splitSettingName(name)
	for _, s := range bootstrapSections {
		if sec == s {
			return true
		}
	}
	for _, k := range bootstrapKeys {
		if name == k {
			return true
		}
	}
	return false
}

// DBSettingNames returns every DB setting name, in registry order.
func DBSettingNames() []string {
	names := make([]string, len(dbSettings))
	for i, s := range dbSettings {
		names[i] = s.Name
	}
	return names
}

// iniName is a field's name in config.ini: its ini tag with any options
// after a comma (as in "type,omitempty") removed.
func iniName(f reflect.StructField) string {
	n, _, _ := strings.Cut(f.Tag.Get("ini"), ",")
	return n
}

// field finds the struct field that config.ini's name maps to.
func field(c *Config, name string) (reflect.Value, error) {
	sec, key := splitSettingName(name)
	v := reflect.ValueOf(c).Elem()
	for i := 0; i < v.NumField(); i++ {
		if iniName(v.Type().Field(i)) != sec {
			continue
		}
		sv := v.Field(i)
		for j := 0; j < sv.NumField(); j++ {
			if iniName(sv.Type().Field(j)) == key {
				return sv.Field(j), nil
			}
		}
	}
	return reflect.Value{}, fmt.Errorf("no Config field for %s", name)
}

// Get returns s's value in c as stored text.
func (s Setting) Get(c *Config) string {
	f, err := field(c, s.Name)
	if err != nil {
		panic(err) // TestRegistryKindsMatchFields rules this out
	}
	switch s.Kind {
	case KindBool:
		return strconv.FormatBool(f.Bool())
	case KindInt:
		return strconv.FormatInt(f.Int(), 10)
	default:
		return f.String()
	}
}

// errEnvRef is why a string setting cannot be exactly ${NAME}: config.ini
// reads such a value as a reference to an environment variable (see envRef),
// so an export pasted back would not round-trip.
var errEnvRef = errors.New("a value of exactly ${NAME} would be read as an environment reference in config.ini")

// Set validates raw and stores it in c. c is unchanged on error.
func (s Setting) Set(c *Config, raw string) error {
	if s.Kind == KindString && envRef.MatchString(raw) {
		return fmt.Errorf("%s: %v", s.Name, errEnvRef)
	}
	if s.Validate != nil {
		if err := s.Validate(raw); err != nil {
			return fmt.Errorf("%s: %v", s.Name, err)
		}
	}
	f, err := field(c, s.Name)
	if err != nil {
		return err
	}
	switch s.Kind {
	case KindBool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("%s: %q is not true or false", s.Name, raw)
		}
		f.SetBool(b)
	case KindInt:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("%s: %q is not a whole number", s.Name, raw)
		}
		f.SetInt(int64(n))
	default:
		f.SetString(raw)
	}
	return nil
}

// SettingsFrom returns every DB setting's value in c, as stored text.
func SettingsFrom(c *Config) map[string]string {
	m := make(map[string]string, len(dbSettings))
	for _, s := range dbSettings {
		m[s.Name] = s.Get(c)
	}
	return m
}

// SettingDefaults returns the value each DB setting takes when the
// database has no row for it: config.New()'s, except local_timeline,
// whose documented default is true (see Load).
func SettingDefaults() map[string]string {
	d := New()
	d.App.LocalTimeline = true
	return SettingsFrom(d)
}

// ApplySettings returns a copy of base with every DB setting taken from
// rows, or from SettingDefaults when rows has none. Values for DB settings
// that base got from config.ini are ignored: the database wins. A stored
// value that does not validate falls back to the default, and a row whose
// name is not registered (a newer version wrote it) is skipped; both are
// reported in the returned warnings rather than failing, so an instance
// never refuses to start over one bad row.
func ApplySettings(base *Config, rows map[string]string) (*Config, []string) {
	c := *base // Config holds only value fields, so this copy is deep
	defs := SettingDefaults()
	var warnings []string
	for _, s := range dbSettings {
		raw, ok := rows[s.Name]
		if !ok {
			raw = defs[s.Name]
		}
		if err := s.Set(&c, raw); err != nil {
			warnings = append(warnings, fmt.Sprintf("ignoring stored value: %v; using default %q", err, defs[s.Name]))
			_ = s.Set(&c, defs[s.Name])
		}
	}
	var unknown []string
	for name := range rows {
		if _, ok := LookupSetting(name); !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		warnings = append(warnings, fmt.Sprintf("ignoring unknown setting %q in the database", name))
	}
	return &c, warnings
}

// ExportINI renders rows as a config.ini fragment, in registry order, for
// pasting back into config.ini before downgrading. rows never holds a
// secret: secrets are bootstrap.
func ExportINI(rows map[string]string) (string, error) {
	var buf bytes.Buffer
	section := ""
	for _, s := range dbSettings {
		v, ok := rows[s.Name]
		if !ok {
			continue
		}
		if s.Kind == KindString && envRef.MatchString(v) {
			return "", fmt.Errorf("%s: %v", s.Name, errEnvRef)
		}
		sec, key := splitSettingName(s.Name)
		if sec != section {
			if section != "" {
				buf.WriteString("\n")
			}
			fmt.Fprintf(&buf, "[%s]\n", sec)
			section = sec
		}
		line, err := iniValue(key, v)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&buf, "%s = %s\n", key, line)
	}
	return buf.String(), nil
}

// iniValue renders v so that reading it back gives v. go-ini does the
// quoting for ordinary values (`;`, `#`, backticks). It leaves a value that
// starts or ends in a quote character bare, and the reader then strips the
// quotes, so those are wrapped in triple quotes, which it keeps whole. A
// value ending in a backslash is wrapped too: bare, the reader takes it as a
// line continuation and swallows the next key.
func iniValue(key, v string) (string, error) {
	if v != "" && (strings.ContainsAny(v[:1], `"'`) || strings.ContainsAny(v[len(v)-1:], `"'\`)) {
		return `"""` + v + `"""`, nil
	}
	f := ini.Empty()
	if _, err := f.Section("x").NewKey(key, v); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		return "", err
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	return strings.TrimPrefix(out, "[x]\n"+key+" = "), nil
}

// SuggestSettings returns up to three registered names close to name,
// closest first.
func SuggestSettings(name string) []string {
	type cand struct {
		name string
		d    int
	}
	var cs []cand
	for _, s := range dbSettings {
		if d := levenshtein(name, s.Name); d <= 4 {
			cs = append(cs, cand{s.Name, d})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	var out []string
	for i := 0; i < len(cs) && i < 3; i++ {
		out = append(out, cs[i].name)
	}
	return out
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
