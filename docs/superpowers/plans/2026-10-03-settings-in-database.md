# Settings in the Database Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move WriteFreely-wisp's community-policy settings out of `config.ini` into the database so several nodes share them, keep `config.ini` as a read-only bootstrap file, and import-then-strip existing files on upgrade.

**Architecture:** A registry in `config/settings.go` names every DB-bound setting and maps it onto the existing `config.Config` struct by ini tag. Settings live as name/value rows in `app_settings`, with a counter in `app_settings_version`. Each node caches an immutable snapshot (`config.Config` plus derived state) behind an `atomic.Pointer`, checks the counter on every request, and rebuilds the snapshot when the counter moved. On startup and on `db migrate`, the first node to claim `version 0 → 1` imports its effective settings, and every node removes the DB-bound keys from its own `config.ini`.

**Tech Stack:** Go 1.25, `database/sql` over MySQL / SQLite (`mattn/go-sqlite3`, build tag `sqlite`) / Postgres (pgx v5 behind the fork's `?`→`$n` rebinding driver), `github.com/go-ini/ini`, `gorilla/mux`, `urfave/cli/v2`.

**Spec:** `docs/superpowers/specs/2026-10-03-settings-in-database-design.md` — read it before starting any task.

## Global Constraints

- Every new migration (V19 on) runs on MySQL, SQLite and Postgres. Column types come from the helpers in `migrations/drivers.go`; anything not portable goes in a `switch db.driverName` whose `default` calls `unsupportedDriver`.
- Every new driver-specific branch in the `writefreely` package is a `switch db.driverName` with a case per engine and `default: unsupportedDriver(...)` (`dialect_guard_test.go` enforces this).
- Never edit `postgres.sql`; migrations reach fresh Postgres databases after `db init`.
- `config.ini` holds: all of `[server]`, `[database]`, `[email]`, every `[oauth.*]`, plus `app.host`, `app.single_user`, `app.disable_password_auth`, `uploads.dir`. Everything else in `[app]` plus `uploads.enabled` and `uploads.max_size_mb` lives in the database.
- No code path reachable from a running server calls `config.Save`. It stays only for `config generate` and `config start`.
- When a key is in both places, the database wins and the node logs a warning naming the key.
- No backup copy of `config.ini` is ever written.
- Before every commit run: `gofmt -l .` (prints nothing), `go vet -composites=false ./...`, `go build ./...`, `go build -tags sqlite ./...`, `go test -count=1 -tags sqlite ./...`. Before the final task also run `make test-postgres GOTESTFLAGS='-tags sqlite'` and `make test-mysql GOTESTFLAGS='-tags sqlite'` (both use throwaway local Docker containers; see `docs/database-testing.md`).
- Commit messages: one imperative sentence, as in `git log`, ending with the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never `git stash` (the stash is shared with other worktrees and sessions). Set work aside with a WIP commit.
- Never push to `develop` or `main`. Never post comments on pull requests or issues.
- No member data. Tests use fixtures only.

## Review Focus

1. **An upgrade from an ini that omits a key keeps the value the instance was actually running with.** Example: `federation` is absent, so the old load path gave `false`, while `config.New()` says `true`. The import stores effective values from the loaded `Config`, not registry defaults. Pinned in Task 6 (`TestImportStoresEffectiveValues`).
2. **A DB-bound key written as a `${VAR}` reference** (`site_name = ${SITE_NAME}`) is imported as its expanded value and stripped. `${VAR}` references in bootstrap keys stay byte-for-byte. Pinned in Task 2 (`TestStripKeysKeepsEnvRefs`) and Task 6 (`TestImportExpandsEnvRefs`).
3. **A stale DB-bound key left in a read-only ini never overrides the database**, including after a reload. Pinned in Task 5 (`TestLoadSettingsIgnoresINIValues`) and Task 6 (`TestImportReadOnlyINIWarns`).
4. **A server started before `db migrate` has created the tables runs on its ini values** and does not crash or import. Pinned in Task 5 (`TestLoadSettingsWithoutTable`).
5. **An admin save that breaks a cross-field rule writes nothing.** Example: setting an allowlist while `private` is off. Pinned in Task 7 (`TestSaveSettingsRejectsAllowlistOnPublic`).

---

## File structure

| File | Status | Responsibility |
|------|--------|----------------|
| `config/settings.go` | create | Registry: which names are DB-bound or bootstrap; get/set by name on `Config`; defaults; `ApplySettings`; `ExportINI`; name suggestions |
| `config/settings_test.go` | create | Registry unit tests, including the "every field classified" test |
| `config/strip.go` | create | `KeysPresent`, `StripKeys`: atomic removal of keys from `config.ini` |
| `config/strip_test.go` | create | Strip tests |
| `migrations/v20.go` | create | `app_settings` and `app_settings_version` tables |
| `migrations/migrations.go` | modify | Register V20 |
| `settings_store.go` | create | Datastore methods: version, load, save, claim-import |
| `settings_runtime.go` | create | Snapshot type, `loadSettings`, `refreshSettings`, middleware, `importSettings`, `saveSettings` |
| `settings_cli.go` | create | Exported functions behind `writefreely settings` |
| `settings_store_test.go`, `settings_runtime_test.go`, `settings_import_test.go`, `settings_save_test.go`, `settings_cli_test.go` | create | Tests (build tag `sqlite`) |
| `settings_guard_test.go` | create | AST guard: `.cfg` is read only inside allowed functions |
| `app.go` | modify | `App` gets the snapshot pointer; `Config()` reads it; startup order |
| `federation_allowlist.go` | modify | `buildFederationAllowlist` (pure) and an allowlist accessor |
| every other root-package `.go` file reading `app.cfg` | modify | Mechanical `.cfg` → `.Config()` |
| `admin.go`, `templates/user/admin/app-settings.tmpl` | modify | Save through `saveSettings`; expanded form |
| `routes.go`, `jobs.go`, `handle.go`, `gopher.go` | modify | Uploads route always registered; sweep checks per tick; Gopher refresh and refusal |
| `cmd/writefreely/settings.go`, `cmd/writefreely/main.go` | create/modify | CLI |
| `docs/settings.md`, `docs/docker.md`, `docker-entrypoint.sh`, `CHANGELOG.md` | create/modify | Docs |

---

### Task 1: Settings registry

**Files:**
- Create: `config/settings.go`
- Test: `config/settings_test.go`

**Interfaces:**
- Consumes: `config.Config`, `config.New()` from `config/config.go`; `validateNonEmpty` from `config/validation.go`.
- Produces:
  - `type SettingKind int` with `KindString`, `KindBool`, `KindInt`
  - `type Setting struct { Name string; Kind SettingKind; Validate func(string) error }`
  - `func (s Setting) Get(c *Config) string`
  - `func (s Setting) Set(c *Config, raw string) error`
  - `func LookupSetting(name string) (Setting, bool)`
  - `func IsBootstrap(name string) bool`
  - `func DBSettingNames() []string` (registry order)
  - `func SettingDefaults() map[string]string`
  - `func SettingsFrom(c *Config) map[string]string`
  - `func ApplySettings(base *Config, rows map[string]string) (*Config, []string)`
  - `func ExportINI(rows map[string]string) (string, error)`
  - `func SuggestSettings(name string) []string`
  - `func splitSettingName(name string) (section, key string)`

- [ ] **Step 1: Write the failing tests**

`config/settings_test.go`:

```go
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
		sec := ct.Field(i).Tag.Get("ini")
		st := ct.Field(i).Type
		for j := 0; j < st.NumField(); j++ {
			key := st.Field(j).Tag.Get("ini")
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./config/ -run 'Classified|Registry|Setting|Lookup|Apply|Export|Suggest' -count=1`
Expected: FAIL to compile with `undefined: LookupSetting`.

- [ ] **Step 3: Implement `config/settings.go`**

```go
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
	{Name: "app.federation_allowlist", Kind: KindString},
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
	"oauth.slack", "oauth.writeas", "oauth.gitlab", "oauth.gitea", "oauth.generic",
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

// field finds the struct field that config.ini's name maps to.
func field(c *Config, name string) (reflect.Value, error) {
	sec, key := splitSettingName(name)
	v := reflect.ValueOf(c).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Type().Field(i).Tag.Get("ini") != sec {
			continue
		}
		sv := v.Field(i)
		for j := 0; j < sv.NumField(); j++ {
			if sv.Type().Field(j).Tag.Get("ini") == key {
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

// Set validates raw and stores it in c. c is unchanged on error.
func (s Setting) Set(c *Config, raw string) error {
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
	f := ini.Empty()
	for _, s := range dbSettings {
		v, ok := rows[s.Name]
		if !ok {
			continue
		}
		sec, key := splitSettingName(s.Name)
		if _, err := f.Section(sec).NewKey(key, v); err != nil {
			return "", err
		}
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		return "", err
	}
	return buf.String(), nil
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./config/ -count=1`
Expected: PASS. If `TestEveryFieldClassified` names a field this plan did not list, stop and ask the human where it lives. Do not guess.

- [ ] **Step 5: Commit**

```bash
git add config/settings.go config/settings_test.go
git commit -m "Add a registry of the settings that live in the database

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Stripping keys from config.ini

**Files:**
- Create: `config/strip.go`
- Test: `config/strip_test.go`

**Interfaces:**
- Consumes: `splitSettingName` (Task 1).
- Produces:
  - `func KeysPresent(fname string, names []string) ([]string, error)`: which of `names` appear in the file, in the order given, read without env expansion.
  - `func StripKeys(fname string, names []string) ([]string, error)`: removes those present and returns them. On error the file is unchanged and the returned slice is the keys still present.

- [ ] **Step 1: Write the failing tests**

`config/strip_test.go`:

```go
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
	"reflect"
	"strings"
	"testing"
)

const stripFixture = `; deployment notes stay
[server]
port = 8080

[database]
; password comes from the environment
password = ${WF_DB_PASSWORD}

[app]
host      = https://blog.example
; shown in the header
site_name = Paisans
private   = true

[uploads]
dir     = /data/uploads
enabled = true
`

func writeINI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKeysPresent(t *testing.T) {
	p := writeINI(t, stripFixture)
	got, err := KeysPresent(p, []string{"app.site_name", "app.theme", "uploads.enabled"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app.site_name", "uploads.enabled"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripKeys(t *testing.T) {
	p := writeINI(t, stripFixture)
	removed, err := StripKeys(p, []string{"app.site_name", "app.private", "uploads.enabled", "app.theme"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app.site_name", "app.private", "uploads.enabled"}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	b, _ := os.ReadFile(p)
	out := string(b)
	for _, gone := range []string{"site_name", "private", "enabled", "shown in the header"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q still present:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"deployment notes stay", "password comes from the environment", "host", "/data/uploads"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q lost:\n%s", kept, out)
		}
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestStripKeysKeepsEnvRefs(t *testing.T) {
	p := writeINI(t, stripFixture)
	if _, err := StripKeys(p, []string{"app.site_name"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "${WF_DB_PASSWORD}") {
		t.Errorf("env reference expanded or lost:\n%s", b)
	}
}

func TestStripKeysNothingToDo(t *testing.T) {
	p := writeINI(t, stripFixture)
	before, _ := os.ReadFile(p)
	removed, err := StripKeys(p, []string{"app.theme"})
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed %v err %v", removed, err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("file rewritten with nothing to strip")
	}
}

func TestStripKeysReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	p := writeINI(t, stripFixture)
	dir := filepath.Dir(p)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	before, _ := os.ReadFile(p)
	left, err := StripKeys(p, []string{"app.site_name"})
	if err == nil {
		t.Fatal("expected an error writing into a read-only directory")
	}
	if !reflect.DeepEqual(left, []string{"app.site_name"}) {
		t.Errorf("left %v", left)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("file changed despite the error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./config/ -run 'KeysPresent|StripKeys' -count=1`
Expected: FAIL with `undefined: KeysPresent`.

- [ ] **Step 3: Implement `config/strip.go`**

```go
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
	if err := tmp.Close(); err != nil {
		return present, err
	}
	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		return present, err
	}
	if err := os.Rename(tmp.Name(), fname); err != nil {
		return present, err
	}
	return present, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./config/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add config/strip.go config/strip_test.go
git commit -m "Add atomic removal of keys from config.ini

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Migration V20 and the settings datastore

**Files:**
- Create: `migrations/v20.go`
- Modify: `migrations/migrations.go` (the `migrations` slice; add one line after the V18→V19 entry)
- Create: `settings_store.go`
- Test: `settings_store_test.go`

**Interfaces:**
- Consumes: `datastore`, `driverSQLite/MySQL/Postgres`, `unsupportedDriver`, `db.upsert`, `db.dialectOrDefault().TableExists` from the root package; `openAppTestDB` from `harness_app_test.go`.
- Produces (methods on `*datastore`):
  - `settingsTableExists(ctx context.Context) (bool, error)`
  - `SettingsVersion(ctx context.Context) (int64, error)`
  - `LoadSettings(ctx context.Context) (map[string]string, int64, error)`: a consistent rows+version pair.
  - `SaveSettings(ctx context.Context, values map[string]string) (int64, error)`: upserts and bumps the version in one transaction, returning the new version.
  - `ClaimSettingsImport(ctx context.Context, values map[string]string) (bool, error)`: claims version 0→1, writes, reads back and compares. It returns false with no error when another node already holds the settings.
  - `var settingsReadBack = func(m map[string]string) map[string]string { return m }` (a test seam).

- [ ] **Step 1: Write the failing tests**

`settings_store_test.go`:

```go
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
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/writefreely/writefreely/config"
)

// newSettingsTestApp is an App with a migrated database on the selected
// engine and a config.ini at cfgFile holding body (when body is not "").
func newSettingsTestApp(t *testing.T, body string) *App {
	t.Helper()
	// The database and config.ini sit in different directories, so a test
	// can make the ini's directory read-only without breaking SQLite.
	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	cfg := config.New()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	cfg.App.Host = "https://blog.example"
	app := &App{cfg: cfg, cfgFile: filepath.Join(t.TempDir(), "config.ini")}
	openAppTestDB(t, app, "sqlite3", dbPath+"?parseTime=true&cached=shared")
	if body != "" {
		writeTestINI(t, app.cfgFile, body)
	}
	return app
}

func TestSettingsStoreEmpty(t *testing.T) {
	app := newSettingsTestApp(t, "")
	ctx := context.Background()
	ok, err := app.db.settingsTableExists(ctx)
	if err != nil || !ok {
		t.Fatalf("table exists %v err %v", ok, err)
	}
	rows, ver, err := app.db.LoadSettings(ctx)
	if err != nil || ver != 0 || len(rows) != 0 {
		t.Fatalf("rows %v ver %d err %v", rows, ver, err)
	}
}

func TestSettingsStoreSave(t *testing.T) {
	app := newSettingsTestApp(t, "")
	ctx := context.Background()
	v1, err := app.db.SaveSettings(ctx, map[string]string{"app.site_name": "One", "app.private": "true"})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := app.db.SaveSettings(ctx, map[string]string{"app.site_name": "Two"})
	if err != nil {
		t.Fatal(err)
	}
	if v2 != v1+1 {
		t.Errorf("versions %d then %d", v1, v2)
	}
	rows, ver, err := app.db.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ver != v2 || rows["app.site_name"] != "Two" || rows["app.private"] != "true" {
		t.Errorf("rows %v ver %d", rows, ver)
	}
	if got, _ := app.db.SettingsVersion(ctx); got != v2 {
		t.Errorf("SettingsVersion %d", got)
	}
}

func TestSettingsClaimOnce(t *testing.T) {
	app := newSettingsTestApp(t, "")
	ctx := context.Background()
	won, err := app.db.ClaimSettingsImport(ctx, map[string]string{"app.site_name": "First"})
	if err != nil || !won {
		t.Fatalf("first claim won=%v err=%v", won, err)
	}
	won, err = app.db.ClaimSettingsImport(ctx, map[string]string{"app.site_name": "Second"})
	if err != nil || won {
		t.Fatalf("second claim won=%v err=%v", won, err)
	}
	rows, ver, _ := app.db.LoadSettings(ctx)
	if rows["app.site_name"] != "First" || ver != 1 {
		t.Errorf("rows %v ver %d", rows, ver)
	}
}

func TestSettingsClaimReadBackMismatch(t *testing.T) {
	app := newSettingsTestApp(t, "")
	ctx := context.Background()
	orig := settingsReadBack
	settingsReadBack = func(m map[string]string) map[string]string {
		m["app.site_name"] = "mangled"
		return m
	}
	t.Cleanup(func() { settingsReadBack = orig })
	_, err := app.db.ClaimSettingsImport(ctx, map[string]string{"app.site_name": "Paisans"})
	if err == nil || !strings.Contains(err.Error(), "app.site_name") {
		t.Fatalf("err %v", err)
	}
	rows, ver, _ := app.db.LoadSettings(ctx)
	if ver != 0 || len(rows) != 0 {
		t.Errorf("claim not rolled back: rows %v ver %d", rows, ver)
	}
}

func TestSettingsClaimConcurrent(t *testing.T) {
	app := newSettingsTestApp(t, "")
	if app.db.driverName == driverSQLite {
		t.Skip("SQLite has one writer per file; two nodes never share one")
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := app.db.ClaimSettingsImport(ctx, map[string]string{"app.site_name": "x"})
			if err != nil {
				t.Error(err)
			}
			wins <- won
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d winners, want 1", n)
	}
}
```

Add to the same file the helper the later tasks also use:

```go
func writeTestINI(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
```

(and add `"os"` to the imports).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -tags sqlite -run 'TestSettings' -count=1 .`
Expected: FAIL to compile with `app.db.settingsTableExists undefined`.

- [ ] **Step 3: Write the migration**

`migrations/v20.go`:

```go
/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package migrations

// supportAppSettings adds the tables that hold community-policy settings,
// so that every node sharing a database reads the same ones. config.ini
// keeps only bootstrap values; see config/settings.go for which is which.
//
// app_settings_version holds one row, id 1. Its version is bumped in the
// same transaction as every write to app_settings, and each node compares
// it with the version of its cached settings before every request. 0
// means nothing has been imported or saved yet; the first node to move it
// to 1 imports its config.ini.
//
// typeInt is wide enough for the counter: it moves once per admin save.
func supportAppSettings(db *datastore) error {
	t, err := db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`CREATE TABLE app_settings (
    name  ` + db.typeVarChar(64) + ` NOT NULL,
    value ` + db.typeText() + ` NOT NULL,
    PRIMARY KEY (name)
)` + db.engine(),
		`CREATE TABLE app_settings_version (
    id      ` + db.typeInt() + ` NOT NULL,
    version ` + db.typeInt() + ` NOT NULL,
    PRIMARY KEY (id)
)` + db.engine(),
		`INSERT INTO app_settings_version (id, version) VALUES (1, 0)`,
	} {
		if _, err = t.Exec(q); err != nil {
			t.Rollback()
			return err
		}
	}
	return t.Commit()
}
```

In `migrations/migrations.go`, append to the `migrations` slice after the V18→V19 line:

```go
	New("store settings in the database", supportAppSettings),        // V19 -> V20
```

(run `gofmt -w migrations/migrations.go` to re-align the comments).

- [ ] **Step 4: Write the datastore methods**

`settings_store.go`:

```go
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

func sortedKeys(m map[string]string) []string {
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
	for _, name := range sortedKeys(values) {
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
	for _, name := range sortedKeys(values) {
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
	for _, name := range sortedKeys(values) {
		if got[name] != values[name] {
			bad = append(bad, name)
		}
	}
	if len(bad) > 0 {
		return false, fmt.Errorf("settings read back differently from what was written: %s; config.ini left untouched", strings.Join(bad, ", "))
	}
	return true, t.Commit()
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -tags sqlite -run 'TestSettings|TestSchemaParity' -count=1 .`
Expected: PASS (`TestSettingsClaimConcurrent` skips on SQLite).

- [ ] **Step 6: Run the full suite and the engine checks**

Run: `go test -count=1 -tags sqlite ./...`, then `make test-postgres GOTESTFLAGS='-tags sqlite'`
Expected: PASS on both. On Postgres the concurrent-claim test runs and reports one winner.

- [ ] **Step 7: Commit**

```bash
git add migrations/v20.go migrations/migrations.go settings_store.go settings_store_test.go
git commit -m "Store settings and their version in the database

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Config snapshot accessor and the mechanical `.cfg` rewrite

This task changes no behaviour. It routes every read through `App.Config()` so that Task 5 can change what `Config()` returns.

**Files:**
- Modify: `app.go` (the `App` struct, `Config`, `SetConfig`, `Initialize`, `Serve`)
- Modify: `federation_allowlist.go` (`initFederationAllowlist` plus the three `app.fedAllowlist` readers)
- Modify: every non-test `.go` file in the repo root that reads `.cfg`
- Create: `settings_guard_test.go`

**Interfaces:**
- Produces:
  - `type settingsSnapshot struct { cfg *config.Config; fedAllowlist map[string]bool; version int64 }`
  - `App.settings atomic.Pointer[settingsSnapshot]`
  - `App.settingsMu sync.Mutex`
  - `func (app *App) Config() *config.Config`: the snapshot's config, or `app.cfg` when there is no snapshot (tests, legacy mode, startup before settings load).
  - `func (app *App) federationAllowlist() map[string]bool`: same fallback, to `app.fedAllowlist`.
  - `func buildFederationAllowlist(cfg *config.Config) (map[string]bool, error)`: pure, with the same checks `initFederationAllowlist` makes.
  - `app.cfg` from now on means **bootstrap configuration as loaded from config.ini**.

- [ ] **Step 1: Write the guard test (it fails until the rewrite is done)**

`settings_guard_test.go`:

```go
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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cfgFieldAllowed lists the only functions that may touch App.cfg, the
// bootstrap configuration from config.ini. Everything else reads
// App.Config(), which returns the current settings snapshot; a direct
// read of .cfg would see bootstrap values and miss every DB setting.
var cfgFieldAllowed = map[string]bool{
	"Config": true, "SetConfig": true, "LoadConfig": true, "NewApp": true,
	"Initialize": true, "ConnectToDatabase": true, "connectToDatabase": true,
	"DoConfig": true, "loadSettingsLocked": true, "importSettings": true,
}

func TestCfgFieldOnlyReadViaConfig(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	for _, fn := range files {
		if strings.HasSuffix(fn, "_test.go") {
			continue
		}
		src, err := os.ReadFile(fn)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, fn, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || cfgFieldAllowed[fd.Name.Name] {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if se, ok := n.(*ast.SelectorExpr); ok && se.Sel.Name == "cfg" {
					t.Errorf("%s: %s reads .cfg directly; use .Config()", fset.Position(se.Pos()), fd.Name.Name)
				}
				return true
			})
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -run TestCfgFieldOnlyReadViaConfig -count=1 .`
Expected: FAIL with many lines of the form `admin.go:NNN: handleViewAdminSettings reads .cfg directly`.

- [ ] **Step 3: Add the snapshot to `App` and rewrite the accessors**

In `app.go`, add `"sync"` and `"sync/atomic"` to the imports. In the `App` struct, after `timeline *localTimeline`, add:

```go
	// settings is the configuration in force: bootstrap values from cfg
	// with the database settings applied, plus state derived from them.
	// It is replaced whole, never mutated, so a request keeps one
	// consistent view. nil until settings are first loaded, and in tests
	// that build an App by hand; Config() then falls back to cfg.
	settings atomic.Pointer[settingsSnapshot]
	// settingsMu serialises reloads, so a burst of requests after a save
	// reloads once.
	settingsMu sync.Mutex
```

Change the `cfg` field comment to:

```go
	// cfg is the bootstrap configuration as loaded from config.ini. Read
	// the configuration in force through Config(); see settingsSnapshot.
	cfg          *config.Config
```

Replace `Config()`:

```go
// settingsSnapshot is one immutable view of the configuration in force.
type settingsSnapshot struct {
	cfg          *config.Config
	fedAllowlist map[string]bool
	version      int64
}

// Config returns the configuration in force. Never mutate what it
// returns: it is shared by every request running under it.
func (app *App) Config() *config.Config {
	if s := app.settings.Load(); s != nil {
		return s.cfg
	}
	return app.cfg
}
```

- [ ] **Step 4: Run the mechanical rewrite**

From the worktree root:

```bash
for f in $(ls *.go | grep -v _test.go); do
  perl -pi -e 's/\.cfg\b(?!\s*=[^=])/.Config()/g' "$f"
done
```

The negative lookahead leaves assignments (`app.cfg = cfg`) alone. Then restore the lines the rewrite must not change:

- `Config()` body: it must read `return app.cfg`, not `return app.Config()`.
- `SetConfig`: `app.cfg = cfg` (untouched by the lookahead; check it).
- `LoadConfig`: `app.cfg = cfg` stays; change its `initFederationAllowlist` call as in Step 5.
- `ConnectToDatabase` and `connectToDatabase`: change every `app.Config().` back to `app.cfg.`. These run before any settings load and write bootstrap defaults (`Database.Host`, `Database.Database`).
- `DoConfig`: `app.cfg = d.Config` stays.

Then run `go build ./... && go build -tags sqlite ./...`. Each compile error of the form `x.Config undefined (type T has no field or method Config)` means a non-`App` struct has its own `cfg` field. Restore that line to `.cfg`, because the guard test only flags functions; add the function to `cfgFieldAllowed` only if it really reads an `App`'s `cfg`.

- [ ] **Step 5: Make the allowlist pure and read it through an accessor**

In `federation_allowlist.go`, replace `initFederationAllowlist` with:

```go
// buildFederationAllowlist parses and checks cfg's allowlist. It is pure,
// so a candidate configuration can be checked before it is saved.
func buildFederationAllowlist(cfg *config.Config) (map[string]bool, error) {
	allow := parseFederationAllowlist(cfg.App.FederationAllowlist)
	if err := validateFederationAllowlist(allow); err != nil {
		return nil, err
	}
	if len(allow) > 0 && !cfg.App.Private {
		return nil, fmt.Errorf("federation_allowlist requires private = true: refusing to run an allowlist on a public instance")
	}
	if len(allow) > 0 && !cfg.App.Federation {
		log.Info(federationAllowlistInertWarning)
	}
	return allow, nil
}

// initFederationAllowlist builds the allowlist from the bootstrap
// configuration, for commands that never load database settings.
func (app *App) initFederationAllowlist() error {
	allow, err := buildFederationAllowlist(app.cfg)
	if err != nil {
		return err
	}
	app.fedAllowlist = allow
	if app.fedKeys == nil {
		app.fedKeys = newKeyCache()
	}
	return nil
}

// federationAllowlist returns the allowlist in force.
func (app *App) federationAllowlist() map[string]bool {
	if s := app.settings.Load(); s != nil {
		return s.fedAllowlist
	}
	return app.fedAllowlist
}
```

Add `"github.com/writefreely/writefreely/config"` to its imports if it is not there, and `initFederationAllowlist` to `cfgFieldAllowed`. Replace each remaining read of `app.fedAllowlist` in non-test files (in `federationAllowlistActive` and the two lookups in the host-match function) with `app.federationAllowlist()`. Read the allowlist once into a local at the top of the host-match function, so one call sees one snapshot:

```go
	allow := app.federationAllowlist()
	if allow[host] {
```

- [ ] **Step 6: Move the one runtime write out of `Serve`, and make the timeline unconditional**

In `Serve`, delete `app.Config().Server.Dev = debugging` (it was `app.cfg.Server.Dev = debugging`). In `Initialize`, directly after `apper.LoadConfig()`, add:

```go
	// Bootstrap, so every settings snapshot built from it inherits it.
	apper.App().cfg.Server.Dev = debugging
```

In `Initialize`, replace the `if ... LocalTimeline { initLocalTimeline }` block with:

```go
	// Always built: it only fetches when read, and the setting can now be
	// turned on at runtime on any node. Readers check LocalTimeline.
	initLocalTimeline(apper.App())
```

In `admin.go` `handleAdminUpdateConfig`, delete the block that calls `initLocalTimeline` when the timeline was nil. In `app.go` near line 1026, `app.timeline != nil` stays as it is (harmless).

- [ ] **Step 7: Run the guard and the whole suite, including the race detector**

Run: `go test -run TestCfgFieldOnlyReadViaConfig -count=1 . && go test -count=1 -tags sqlite ./... && go test -race -count=1 -tags sqlite .`
Expected: PASS. If the guard still lists a function, fix that read. Never widen the allowlist to make it pass.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "Read configuration through App.Config so it can be swapped whole

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Loading settings and refreshing them on every request

**Files:**
- Create: `settings_runtime.go`
- Modify: `app.go` (`Initialize` and `Serve`)
- Modify: `handle.go` (`Gopher`)
- Test: `settings_runtime_test.go`

**Interfaces:**
- Consumes: Task 1 (`config.ApplySettings`), Task 3 (datastore methods), Task 4 (`settingsSnapshot`, `buildFederationAllowlist`).
- Produces:
  - `func (app *App) loadSettings(ctx context.Context) error`
  - `func (app *App) loadSettingsLocked(ctx context.Context) error` (caller holds `settingsMu`)
  - `func (app *App) refreshSettings(ctx context.Context)`
  - `func (app *App) settingsMiddleware(next http.Handler) http.Handler`
  - `func (app *App) configPath() string`

- [ ] **Step 1: Write the failing tests**

`settings_runtime_test.go`:

```go
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
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// secondNode is another App on the same database: its own snapshot, the
// same rows. That is all a second node is, as far as settings go.
func secondNode(t *testing.T, a *App) *App {
	t.Helper()
	cfg := *a.cfg
	b := &App{cfg: &cfg, cfgFile: a.cfgFile, db: a.db}
	if err := b.loadSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLoadSettingsAppliesRows(t *testing.T) {
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	if _, err := a.db.SaveSettings(ctx, map[string]string{"app.site_name": "From DB"}); err != nil {
		t.Fatal(err)
	}
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.Config().App.SiteName; got != "From DB" {
		t.Errorf("site_name %q", got)
	}
	if a.Config().App.Host != "https://blog.example" {
		t.Error("bootstrap host lost")
	}
}

func TestLoadSettingsIgnoresINIValues(t *testing.T) {
	a := newSettingsTestApp(t, "")
	a.cfg.App.SiteName = "stale ini value"
	ctx := context.Background()
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "DB value"})
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.Config().App.SiteName; got != "DB value" {
		t.Errorf("ini value won: %q", got)
	}
	// Still true after a reload triggered by an unrelated save.
	a.db.SaveSettings(ctx, map[string]string{"app.max_blogs": "3"})
	a.refreshSettings(ctx)
	if got := a.Config().App.SiteName; got != "DB value" {
		t.Errorf("ini value won after reload: %q", got)
	}
}

func TestLoadSettingsWithoutTable(t *testing.T) {
	a := newSettingsTestApp(t, "")
	for _, q := range []string{"DROP TABLE app_settings", "DROP TABLE app_settings_version"} {
		if _, err := a.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a.cfg.App.SiteName = "ini only"
	if err := a.loadSettings(context.Background()); err != nil {
		t.Fatalf("missing table must not be fatal: %v", err)
	}
	if got := a.Config().App.SiteName; got != "ini only" {
		t.Errorf("site_name %q", got)
	}
	a.refreshSettings(context.Background()) // must not panic
}

func TestSecondNodeSeesSaveOnNextRequest(t *testing.T) {
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	if err := a.loadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	b := secondNode(t, a)

	a.db.SaveSettings(ctx, map[string]string{
		"app.private":              "true",
		"app.federation_allowlist": "peer.example",
	})

	var seen string
	h := b.settingsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = b.Config().App.FederationAllowlist
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if seen != "peer.example" {
		t.Errorf("node B saw %q", seen)
	}
	if !b.federationAllowlist()["peer.example"] {
		t.Error("node B's derived allowlist was not rebuilt")
	}
}

func TestRefreshKeepsSnapshotOnError(t *testing.T) {
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "Kept"})
	a.loadSettings(ctx)
	a.db.SaveSettings(ctx, map[string]string{"app.site_name": "Not yet seen"})
	dead, cancel := context.WithCancel(ctx)
	cancel()
	a.refreshSettings(dead) // the version check fails: logs, keeps the cache
	if got := a.Config().App.SiteName; got != "Kept" {
		t.Errorf("site_name %q", got)
	}
}

func TestReloadIsRaceFree(t *testing.T) {
	a := newSettingsTestApp(t, "")
	ctx := context.Background()
	a.loadSettings(ctx)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				a.refreshSettings(ctx)
				_ = a.Config().App.SiteName
			}
		}()
	}
	for j := 0; j < 20; j++ {
		a.db.SaveSettings(ctx, map[string]string{"app.max_blogs": "2"})
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -tags sqlite -run 'LoadSettings|SecondNode|Refresh|ReloadIsRace' -count=1 .`
Expected: FAIL to compile with `a.loadSettings undefined`.

- [ ] **Step 3: Implement `settings_runtime.go` (load and refresh)**

```go
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
	"net/http"

	"github.com/writeas/web-core/log"
	"github.com/writefreely/writefreely/config"
)

// configPath is the bootstrap file this App reads.
func (app *App) configPath() string {
	if app.cfgFile == "" {
		return config.FileName
	}
	return app.cfgFile
}

// loadSettings builds a settings snapshot from the bootstrap
// configuration and the database, and puts it in force.
func (app *App) loadSettings(ctx context.Context) error {
	app.settingsMu.Lock()
	defer app.settingsMu.Unlock()
	return app.loadSettingsLocked(ctx)
}

// loadSettingsLocked is loadSettings for a caller holding settingsMu.
//
// Before `db migrate` has created the tables there is nothing to load:
// the instance runs on config.ini as it always did, and Config() keeps
// returning the bootstrap configuration.
func (app *App) loadSettingsLocked(ctx context.Context) error {
	ok, err := app.db.settingsTableExists(ctx)
	if err != nil {
		return err
	}
	if !ok {
		log.Info("Settings are still read from %s: run `writefreely db migrate`, then restart, to move them into the database.", app.configPath())
		return nil
	}
	rows, ver, err := app.db.LoadSettings(ctx)
	if err != nil {
		return err
	}
	cfg, warnings := config.ApplySettings(app.cfg, rows)
	for _, w := range warnings {
		log.Error("settings: %s", w)
	}
	allow, err := buildFederationAllowlist(cfg)
	if err != nil {
		return err
	}
	if app.fedKeys == nil {
		app.fedKeys = newKeyCache()
	}
	app.settings.Store(&settingsSnapshot{cfg: cfg, fedAllowlist: allow, version: ver})
	return nil
}

// refreshSettings reloads the settings if another node (or the CLI) has
// saved since this node last loaded them. It costs one primary-key read.
// On any failure the cached snapshot stays in force: a database that is
// unreachable mid-failover must not take the configuration with it.
func (app *App) refreshSettings(ctx context.Context) {
	cur := app.settings.Load()
	if cur == nil {
		return // settings are not in the database (yet); nothing to refresh
	}
	ver, err := app.db.SettingsVersion(ctx)
	if err != nil {
		log.Error("settings: version check failed, keeping cached settings: %v", err)
		return
	}
	if ver == cur.version {
		return
	}
	app.settingsMu.Lock()
	defer app.settingsMu.Unlock()
	if s := app.settings.Load(); s != nil && s.version == ver {
		return // another request reloaded while this one waited
	}
	if err := app.loadSettingsLocked(ctx); err != nil {
		log.Error("settings: reload failed, keeping cached settings: %v", err)
	}
}

// settingsMiddleware checks for changed settings before every request.
func (app *App) settingsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.refreshSettings(r.Context())
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Wire it into startup, serving and Gopher**

In `Initialize` (`app.go`), directly after the `ConnectToDatabase` error check, add:

```go
	if err := apper.App().loadSettings(context.Background()); err != nil {
		return nil, fmt.Errorf("load settings: %s", err)
	}
```

(Task 6 inserts the import call immediately before this.) Add `"context"` to the imports if needed.

In `Serve`, as its first statement:

```go
	r.Use(app.settingsMiddleware)
```

In `handle.go` `Gopher`, inside the returned func and before `err := f(...)`:

```go
		app := h.app.App()
		app.refreshSettings(context.Background())
		if app.Config().App.Private {
			// The Gopher server starts only on a public instance; an
			// instance made private since must stop answering.
			w.WriteError("This instance is private.")
			return
		}
```

and change `f(h.app.App(), w, r)` to `f(app, w, r)`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -tags sqlite -run 'LoadSettings|SecondNode|Refresh|ReloadIsRace|CfgField' -count=1 . && go test -race -tags sqlite -run 'ReloadIsRace|SecondNode' -count=1 .`
Expected: PASS, with no `DATA RACE` reports.

- [ ] **Step 6: Run the full suite**

Run: `go test -count=1 -tags sqlite ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add settings_runtime.go settings_runtime_test.go app.go handle.go
git commit -m "Load settings from the database and reload them when another node saves

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Importing config.ini on upgrade

**Files:**
- Modify: `settings_runtime.go` (add `importSettings`)
- Modify: `app.go` (`Initialize`, `Migrate`, `DoConfig`)
- Test: `settings_import_test.go`

**Interfaces:**
- Consumes: `config.KeysPresent`, `config.StripKeys`, `config.SettingsFrom`, `config.SettingDefaults`, `config.DBSettingNames` (Tasks 1–2); `ClaimSettingsImport`, `LoadSettings` (Task 3).
- Produces:
  - `type settingsImport struct { Imported bool; Drift []string; Stripped []string; Left []string; StripErr error }`
  - `func (app *App) importSettings(ctx context.Context) (settingsImport, error)`

- [ ] **Step 1: Write the failing tests**

`settings_import_test.go`:

```go
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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/writefreely/writefreely/config"
)

const importFixture = `[server]
port = 8080

[database]
type     = sqlite3
filename = writefreely.db

[app]
host      = https://blog.example
; header text
site_name = Paisans
private   = true
max_blogs = 3
`

// loadINIInto gives app the bootstrap Config that config.Load produces
// from its ini, which is what an upgraded node starts with.
func loadINIInto(t *testing.T, app *App) {
	t.Helper()
	cfg, err := config.Load(app.cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = app.cfg.Database // keep the test database
	app.cfg = cfg
}

func TestImportStoresEffectiveValues(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	res, err := a.importSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Imported {
		t.Fatal("first node did not import")
	}
	rows, _, _ := a.db.LoadSettings(context.Background())
	want := map[string]string{
		"app.site_name":  "Paisans",
		"app.private":    "true",
		"app.max_blogs":  "3",
		"app.federation": "false", // absent from the ini: the old load gave false, not New()'s true
	}
	for k, v := range want {
		if rows[k] != v {
			t.Errorf("%s = %q, want %q", k, rows[k], v)
		}
	}
	if len(rows) != len(config.DBSettingNames()) {
		t.Errorf("imported %d rows, want every setting (%d)", len(rows), len(config.DBSettingNames()))
	}
}

func TestImportStripsINI(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	res, err := a.importSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app.site_name", "app.max_blogs", "app.private"}; !reflect.DeepEqual(res.Stripped, want) {
		t.Errorf("stripped %v, want %v", res.Stripped, want)
	}
	b, _ := os.ReadFile(a.cfgFile)
	if strings.Contains(string(b), "site_name") || !strings.Contains(string(b), "host") {
		t.Errorf("ini after strip:\n%s", b)
	}
}

func TestImportRerunIsNoOp(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	ctx := context.Background()
	a.importSettings(ctx)
	_, v1, _ := a.db.LoadSettings(ctx)
	res, err := a.importSettings(ctx)
	if err != nil || res.Imported || len(res.Stripped) != 0 {
		t.Fatalf("rerun res %+v err %v", res, err)
	}
	_, v2, _ := a.db.LoadSettings(ctx)
	if v1 != v2 {
		t.Errorf("version moved %d -> %d", v1, v2)
	}
}

func TestImportLoserReportsDrift(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	ctx := context.Background()
	if _, err := a.importSettings(ctx); err != nil {
		t.Fatal(err)
	}
	// Node B has its own ini that disagreed with A's before the upgrade.
	bFile := filepath.Join(t.TempDir(), "config.ini")
	writeTestINI(t, bFile, strings.Replace(importFixture, "site_name = Paisans", "site_name = Other", 1))
	b := &App{cfgFile: bFile, db: a.db}
	cfg, _ := config.Load(bFile)
	b.cfg = cfg
	res, err := b.importSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported {
		t.Error("second node imported")
	}
	if !reflect.DeepEqual(res.Drift, []string{"app.site_name"}) {
		t.Errorf("drift %v", res.Drift)
	}
	rows, _, _ := a.db.LoadSettings(ctx)
	if rows["app.site_name"] != "Paisans" {
		t.Errorf("loser overwrote: %q", rows["app.site_name"])
	}
	bb, _ := os.ReadFile(bFile)
	if strings.Contains(string(bb), "site_name") {
		t.Error("loser did not strip its own ini")
	}
}

func TestImportReadOnlyINIWarns(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	dir := filepath.Dir(a.cfgFile)
	os.Chmod(dir, 0500)
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	res, err := a.importSettings(context.Background())
	if err != nil {
		t.Fatalf("a read-only ini must not be fatal: %v", err)
	}
	if !res.Imported || res.StripErr == nil || len(res.Left) != 3 {
		t.Errorf("res %+v", res)
	}
	// The stale keys stay in the file; the database still wins.
	a.cfg.App.SiteName = "stale"
	a.db.SaveSettings(context.Background(), map[string]string{"app.site_name": "Fresh"})
	a.loadSettings(context.Background())
	if a.Config().App.SiteName != "Fresh" {
		t.Error("stale ini value won")
	}
}

func TestImportExpandsEnvRefs(t *testing.T) {
	t.Setenv("WF_TEST_SITE", "From Env")
	a := newSettingsTestApp(t, strings.Replace(importFixture, "site_name = Paisans", "site_name = ${WF_TEST_SITE}", 1))
	loadINIInto(t, a)
	if _, err := a.importSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := a.db.LoadSettings(context.Background())
	if rows["app.site_name"] != "From Env" {
		t.Errorf("site_name %q", rows["app.site_name"])
	}
	b, _ := os.ReadFile(a.cfgFile)
	if strings.Contains(string(b), "WF_TEST_SITE") {
		t.Error("DB-bound env reference not stripped")
	}
}

func TestImportMismatchLeavesINI(t *testing.T) {
	a := newSettingsTestApp(t, importFixture)
	loadINIInto(t, a)
	orig := settingsReadBack
	settingsReadBack = func(m map[string]string) map[string]string { m["app.private"] = "false"; return m }
	t.Cleanup(func() { settingsReadBack = orig })
	before, _ := os.ReadFile(a.cfgFile)
	if _, err := a.importSettings(context.Background()); err == nil {
		t.Fatal("mismatch did not fail")
	}
	after, _ := os.ReadFile(a.cfgFile)
	if string(before) != string(after) {
		t.Error("ini changed after a failed import")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -tags sqlite -run TestImport -count=1 .`
Expected: FAIL to compile with `a.importSettings undefined`.

- [ ] **Step 3: Implement `importSettings`**

Append to `settings_runtime.go` (add `"strings"` to its imports):

```go
// settingsImport is what one importSettings run did, for logging and tests.
type settingsImport struct {
	Imported bool     // this node moved the settings into the database
	Drift    []string // DB-bound keys in this node's ini that disagree with the database
	Stripped []string // keys removed from this node's ini
	Left     []string // keys still in the ini because it could not be written
	StripErr error
}

// importSettings moves this node's settings into the database if no node
// has yet, then removes the DB-bound keys from its own config.ini.
//
// It imports the values the instance was running with (app.cfg as loaded),
// not the keys as written: a key absent from the file was in force as its
// zero value, and storing config.New()'s default instead would quietly
// change the instance on upgrade.
//
// It is idempotent and runs at every start and in `db migrate`.
func (app *App) importSettings(ctx context.Context) (settingsImport, error) {
	var res settingsImport
	ok, err := app.db.settingsTableExists(ctx)
	if err != nil || !ok {
		return res, err
	}
	present, err := config.KeysPresent(app.configPath(), config.DBSettingNames())
	if err != nil {
		return res, err
	}
	effective := config.SettingsFrom(app.cfg)

	res.Imported, err = app.db.ClaimSettingsImport(ctx, effective)
	if err != nil {
		return res, err
	}
	if res.Imported {
		log.Info("Moved %d settings from %s into the database.", len(effective), app.configPath())
	} else if len(present) > 0 {
		rows, _, err := app.db.LoadSettings(ctx)
		if err != nil {
			return res, err
		}
		defs := config.SettingDefaults()
		for _, name := range present {
			dbv, ok := rows[name]
			if !ok {
				dbv = defs[name]
			}
			if effective[name] != dbv {
				res.Drift = append(res.Drift, name)
			}
		}
		if len(res.Drift) > 0 {
			log.Error("settings: %s disagrees with the database on %s; the database value is in force.", app.configPath(), strings.Join(res.Drift, ", "))
		}
	}

	if len(present) == 0 {
		return res, nil
	}
	res.Stripped, res.StripErr = config.StripKeys(app.configPath(), present)
	if res.StripErr != nil {
		res.Left, res.Stripped = res.Stripped, nil
		log.Error("settings: could not remove %s from %s (%v). They are ignored; delete them by hand.", strings.Join(res.Left, ", "), app.configPath(), res.StripErr)
	} else {
		log.Info("Removed %s from %s; they now live in the database.", strings.Join(res.Stripped, ", "), app.configPath())
	}
	return res, nil
}
```

- [ ] **Step 4: Wire the import in**

In `Initialize`, immediately before the `loadSettings` call added in Task 5:

```go
	if _, err := apper.App().importSettings(context.Background()); err != nil {
		return nil, fmt.Errorf("import settings from %s: %s", apper.App().configPath(), err)
	}
```

In `Migrate`, replace the final `return nil` with:

```go
	if _, err := apper.App().importSettings(context.Background()); err != nil {
		return fmt.Errorf("import settings: %s", err)
	}
	return nil
```

In `DoConfig`, after the `if !app.db.DatabaseInitialized() { ... } else { ... }` block, add:

```go
	if _, err := app.importSettings(context.Background()); err != nil {
		log.Error("Unable to move settings into the database: %v", err)
		os.Exit(1)
	}
```

This is how `config start` puts its policy answers into the database: `config.Configure` saves a full ini, then the import moves the DB-bound keys over and strips them. `config generate` needs nothing, because the first `serve` or `db migrate` does the same.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -tags sqlite -run 'TestImport|CfgField' -count=1 .`
Expected: PASS.

- [ ] **Step 6: Run the full suite**

Run: `go test -count=1 -tags sqlite ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add settings_runtime.go settings_import_test.go app.go
git commit -m "Move settings from config.ini into the database on upgrade

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Saving from the admin page

**Files:**
- Modify: `settings_runtime.go` (add `saveSettings`, `settingNameError`)
- Modify: `admin.go` (`handleAdminUpdateConfig`, `handleViewAdminSettings`)
- Modify: `templates/user/admin/app-settings.tmpl`
- Test: `settings_save_test.go`

**Interfaces:**
- Consumes: Tasks 1, 3, 4, 6.
- Produces:
  - `func (app *App) saveSettings(ctx context.Context, changes map[string]string) error`
  - `func settingNameError(name, cfgPath string) error`

- [ ] **Step 1: Write the failing tests**

`settings_save_test.go`:

```go
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
	"strings"
	"testing"

	"github.com/writeas/impart"
)

func loadedSettingsApp(t *testing.T) *App {
	t.Helper()
	a := newSettingsTestApp(t, "")
	if err := a.loadSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSaveSettingsAppliesAndBumps(t *testing.T) {
	a := loadedSettingsApp(t)
	ctx := context.Background()
	if err := a.saveSettings(ctx, map[string]string{"app.site_name": "Saved"}); err != nil {
		t.Fatal(err)
	}
	if a.Config().App.SiteName != "Saved" {
		t.Error("saving node did not reload")
	}
	b := secondNode(t, a)
	if b.Config().App.SiteName != "Saved" {
		t.Error("other node does not see the save")
	}
}

func TestSaveSettingsRejectsAllowlistOnPublic(t *testing.T) {
	a := loadedSettingsApp(t)
	ctx := context.Background()
	_, before, _ := a.db.LoadSettings(ctx)
	err := a.saveSettings(ctx, map[string]string{
		"app.private":              "false",
		"app.federation_allowlist": "peer.example",
		"app.site_name":            "must not land",
	})
	if err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("err %v", err)
	}
	rows, after, _ := a.db.LoadSettings(ctx)
	if after != before || rows["app.site_name"] == "must not land" {
		t.Error("a rejected save wrote something")
	}
}

func TestSaveSettingsRefusesBootstrapAndUnknown(t *testing.T) {
	a := loadedSettingsApp(t)
	err := a.saveSettings(context.Background(), map[string]string{"app.host": "https://x.example"})
	if err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("bootstrap err %v", err)
	}
	err = a.saveSettings(context.Background(), map[string]string{"app.privat": "true"})
	if err == nil || !strings.Contains(err.Error(), "app.private") {
		t.Errorf("unknown err %v (want a suggestion)", err)
	}
}

func TestSaveSettingsBeforeMigration(t *testing.T) {
	a := newSettingsTestApp(t, "") // never loaded: no snapshot
	err := a.saveSettings(context.Background(), map[string]string{"app.site_name": "x"})
	if err == nil || !strings.Contains(err.Error(), "db migrate") {
		t.Errorf("err %v", err)
	}
}

func TestAdminUpdateConfigHandler(t *testing.T) {
	a := loadedSettingsApp(t)
	form := url.Values{
		"site_name":          {"Handled"},
		"min_username_len":   {"3"},
		"max_blogs":          {"2"},
		"user_invites":       {"none"},
		"default_visibility": {"public"},
		"theme":              {"write"},
		"federation":         {"on"},
		"uploads_max_size_mb": {"10"},
	}
	r := httptest.NewRequest("POST", "/admin/settings", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	err := handleAdminUpdateConfig(a, nil, httptest.NewRecorder(), r)
	he, ok := err.(impart.HTTPError)
	if !ok || !strings.Contains(he.Message, "Configuration+saved") {
		t.Fatalf("handler returned %v, want a redirect carrying ?cm=Configuration+saved.", err)
	}
	if a.Config().App.SiteName != "Handled" || a.Config().App.UserInvites != "" || !a.Config().App.Federation {
		t.Errorf("config after handler: %+v", a.Config().App)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -tags sqlite -run 'TestSaveSettings|TestAdminUpdateConfig' -count=1 .`
Expected: FAIL to compile with `a.saveSettings undefined`.

- [ ] **Step 3: Implement `saveSettings`**

Append to `settings_runtime.go` (add `"errors"` and `"fmt"` to its imports):

```go
// settingNameError explains why name cannot be set at runtime.
func settingNameError(name, cfgPath string) error {
	if config.IsBootstrap(name) {
		return fmt.Errorf("%s lives in %s (bootstrap); edit it there and restart", name, cfgPath)
	}
	if s := config.SuggestSettings(name); len(s) > 0 {
		return fmt.Errorf("no setting called %s; did you mean %s?", name, strings.Join(s, " or "))
	}
	return fmt.Errorf("no setting called %s", name)
}

// saveSettings validates changes against the settings in force, writes
// them, and reloads this node at once. Other nodes pick them up on their
// next request. Nothing is written unless every value, and the
// combination, is valid.
func (app *App) saveSettings(ctx context.Context, changes map[string]string) error {
	if app.settings.Load() == nil {
		return errors.New("settings are not in the database yet: run `writefreely db migrate` and restart")
	}
	cur := app.Config()
	cand := *cur
	stored := map[string]string{}
	for _, name := range sortedKeys(changes) {
		s, ok := config.LookupSetting(name)
		if !ok {
			return settingNameError(name, app.configPath())
		}
		if err := s.Set(&cand, changes[name]); err != nil {
			return err
		}
		stored[name] = s.Get(&cand) // normalised text: "TRUE" -> "true"
	}
	if _, err := buildFederationAllowlist(&cand); err != nil {
		return err
	}
	if cand.Uploads.Enabled && !cur.Uploads.Enabled {
		if err := app.ensureUploadsWritable(); err != nil {
			return fmt.Errorf("cannot enable uploads on this node: %v", err)
		}
	}
	if _, err := app.db.SaveSettings(ctx, stored); err != nil {
		return err
	}
	app.settingsMu.Lock()
	defer app.settingsMu.Unlock()
	return app.loadSettingsLocked(ctx)
}
```

- [ ] **Step 4: Rewrite the admin handler**

Replace `handleAdminUpdateConfig` in `admin.go` with:

```go
func handleAdminUpdateConfig(apper Apper, u *User, w http.ResponseWriter, r *http.Request) error {
	check := func(field string) string {
		if r.FormValue(field) == "on" {
			return "true"
		}
		return "false"
	}
	invites := r.FormValue("user_invites")
	if invites == "none" {
		invites = ""
	}
	changes := map[string]string{
		"app.site_name":            r.FormValue("site_name"),
		"app.site_description":     r.FormValue("site_desc"),
		"app.landing":              r.FormValue("landing"),
		"app.open_registration":    check("open_registration"),
		"app.open_deletion":        check("open_deletion"),
		"app.min_username_len":     r.FormValue("min_username_len"),
		"app.max_blogs":            r.FormValue("max_blogs"),
		"app.federation":           check("federation"),
		"app.public_stats":         check("public_stats"),
		"app.monetization":         check("monetization"),
		"app.private":              check("private"),
		"app.local_timeline":       check("local_timeline"),
		"app.user_invites":         invites,
		"app.default_visibility":   r.FormValue("default_visibility"),
		"app.theme":                r.FormValue("theme"),
		"app.editor":               r.FormValue("editor"),
		"app.disable_js":           check("disable_js"),
		"app.webfonts":             check("webfonts"),
		"app.simple_nav":           check("simple_nav"),
		"app.wf_modesty":           check("wf_modesty"),
		"app.chorus":               check("chorus"),
		"app.forest":               check("forest"),
		"app.disable_drafts":       check("disable_drafts"),
		"app.notes_only":           check("notes_only"),
		"app.federation_allowlist": r.FormValue("federation_allowlist"),
		"app.instance_announce":    check("instance_announce"),
		"app.update_checks":        check("update_checks"),
		"uploads.enabled":          check("uploads_enabled"),
		"uploads.max_size_mb":      r.FormValue("uploads_max_size_mb"),
	}

	m := "?cm=Configuration+saved."
	if err := apper.App().saveSettings(r.Context(), changes); err != nil {
		m = "?cm=" + url.QueryEscape(err.Error())
	}
	return impart.HTTPError{http.StatusFound, "/admin/settings" + m + "#config"}
}
```

Add `"net/url"` to `admin.go`'s imports if it is not there, and remove `strconv` if it is now unused. The private-mode rule (`canDisablePrivateMode`) is now enforced by `buildFederationAllowlist` inside `saveSettings`. A save that turns `private` off while an allowlist is set fails with a message instead of being silently forced on.

In `handleViewAdminSettings`, add `Uploads config.UploadsCfg` to the page struct and set it to `app.Config().Uploads`.

- [ ] **Step 5: Expand the form**

In `templates/user/admin/app-settings.tmpl`, before the `Save Settings` row, add one row per new control, following the existing row markup exactly:

```html
		<div class="features row">
			<div><label for="theme">Theme<p>Stylesheet name served to every page.</p></label></div>
			<div><input type="text" name="theme" id="theme" class="inline" value="{{.Config.Theme}}" style="width: 14em;"/></div>
		</div>
		<div class="features row">
			<div><label for="editor">Editor<p>Default editor for new posts. Leave empty for the standard one.</p></label></div>
			<div><input type="text" name="editor" id="editor" class="inline" value="{{.Config.Editor}}" style="width: 14em;"/></div>
		</div>
		<div class="features row">
			<div><label for="disable_js">Disable JavaScript<p>Serve reader pages without scripts.</p></label></div>
			<div><input type="checkbox" name="disable_js" id="disable_js" {{if .Config.JSDisabled}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div><label for="webfonts">Web Fonts<p>Load the site's web fonts.</p></label></div>
			<div><input type="checkbox" name="webfonts" id="webfonts" {{if .Config.WebFonts}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div><label for="simple_nav">Simple Navigation<p>Show a reduced navigation bar.</p></label></div>
			<div><input type="checkbox" name="simple_nav" id="simple_nav" {{if .Config.SimpleNav}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div><label for="wf_modesty">WriteFreely Modesty<p>Hide "Powered by WriteFreely" links.</p></label></div>
			<div><input type="checkbox" name="wf_modesty" id="wf_modesty" {{if .Config.WFModesty}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div{{if .Config.SingleUser}} class="invisible"{{end}}><label for="chorus">Chorus<p>Present the instance as one shared publication.</p></label></div>
			<div{{if .Config.SingleUser}} class="invisible"{{end}}><input type="checkbox" name="chorus" id="chorus" {{if .Config.Chorus}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div><label for="forest">Forest<p>Hide technical details from writers.</p></label></div>
			<div><input type="checkbox" name="forest" id="forest" {{if .Config.Forest}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div><label for="disable_drafts">Disable Drafts<p>Posts must belong to a blog.</p></label></div>
			<div><input type="checkbox" name="disable_drafts" id="disable_drafts" {{if .Config.DisableDrafts}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div><label for="notes_only">Notes Only<p>Hide blog features; posts are notes.</p></label></div>
			<div><input type="checkbox" name="notes_only" id="notes_only" {{if .Config.NotesOnly}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div><label for="update_checks">Update Checks<p>Check for new WriteFreely releases.</p></label></div>
			<div><input type="checkbox" name="update_checks" id="update_checks" {{if .Config.UpdateChecks}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div><label for="uploads_enabled">Image Uploads<p>Let writers upload images. This node's upload directory must be writable.</p></label></div>
			<div><input type="checkbox" name="uploads_enabled" id="uploads_enabled" {{if .Uploads.Enabled}}checked="checked"{{end}} /></div>
		</div>
		<div class="features row">
			<div><label for="uploads_max_size_mb">Largest Upload (MB)<p>Per file, not per writer.</p></label></div>
			<div><input type="number" name="uploads_max_size_mb" id="uploads_max_size_mb" class="inline" min="1" value="{{.Uploads.MaxSizeMB}}"/></div>
		</div>
		<h3 id="federation-settings">Federation</h3>
		<div class="features row">
			<div><label for="federation_allowlist">Federation Allowlist
					<p>Comma-separated hosts allowed to read this instance over ActivityPub. Requires Private. <strong>Changes who receives what.</strong></p>
				</label></div>
			<div><input type="text" name="federation_allowlist" id="federation_allowlist" class="inline" value="{{.Config.FederationAllowlist}}" style="width: 20em;"/></div>
		</div>
		<div class="features row">
			<div><label for="instance_announce">Instance Announce
					<p>Announce every public post under one instance-wide actor. <strong>Publishes a feed of all public writing.</strong></p>
				</label></div>
			<div><input type="checkbox" name="instance_announce" id="instance_announce" {{if .Config.InstanceAnnounce}}checked="checked"{{end}} /></div>
		</div>
```

Before writing these, check the existing template. If any of these names already has a row, keep the existing row and skip the duplicate.

- [ ] **Step 6: Run the tests to verify they pass, and render the page**

Run: `go test -tags sqlite -run 'TestSaveSettings|TestAdminUpdateConfig|TestTemplate' -count=1 .`
Expected: PASS. Then run the template render test suite (`go test -tags sqlite -run Template -count=1 .`), which parses every template, and expect PASS.

- [ ] **Step 7: Run the full suite and commit**

Run: `go test -count=1 -tags sqlite ./...` and expect PASS.

```bash
git add settings_runtime.go settings_save_test.go admin.go templates/user/admin/app-settings.tmpl
git commit -m "Save admin settings to the database and expose every runtime setting

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Uploads as a runtime setting

**Files:**
- Modify: `routes.go` (`InitStaticRoutes`)
- Modify: `app.go` (`Initialize`, the uploads block)
- Modify: `jobs.go` (`startOrphanImageSweep`)
- Test: `settings_uploads_test.go`

**Interfaces:**
- Consumes: `app.Config()`, `refreshSettings`, `saveSettings`.
- Produces: `func uploadsGate(app *App, next http.Handler) http.Handler`.

- [ ] **Step 1: Write the failing test**

`settings_uploads_test.go`:

```go
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
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadsGate(t *testing.T) {
	a := loadedSettingsApp(t)
	a.cfg.Uploads.Dir = t.TempDir()
	a.loadSettings(context.Background())
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := uploadsGate(a, ok)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/"+uploadsDir+"/x.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("disabled: %d", rec.Code)
	}

	if err := a.saveSettings(context.Background(), map[string]string{"uploads.enabled": "true"}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/"+uploadsDir+"/x.png", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("enabled: %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -tags sqlite -run TestUploadsGate -count=1 .`
Expected: FAIL with `undefined: uploadsGate`.

- [ ] **Step 3: Implement**

In `routes.go`, replace the `if app.Config().Uploads.Enabled { ... }` block in `InitStaticRoutes` with:

```go
	// Registered whatever the setting says, because it can now be turned on
	// at runtime from any node; uploadsGate answers 404 while it is off.
	uploads := http.FileServer(http.Dir(app.uploadsRoot()))
	uploads = cacheControl(http.StripPrefix("/"+uploadsDir+"/", uploads))
	r.PathPrefix("/" + uploadsDir + "/").Handler(uploadsGate(app, uploadHeaders(uploads)))
```

and add to `routes.go`:

```go
// uploadsGate serves next only while uploads are enabled.
func uploadsGate(app *App, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !app.Config().Uploads.Enabled {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

In `Initialize`, replace the uploads block with:

```go
	if apper.App().Config().Uploads.Enabled {
		// Fail here rather than at the moment someone uploads: a missing
		// volume or a directory the process cannot write to is already
		// true at startup, and an operator is watching now. Enabling at
		// runtime makes the same check in saveSettings.
		if err := apper.App().ensureUploadsWritable(); err != nil {
			return nil, fmt.Errorf("uploads are enabled but unusable: %s", err)
		}
	}
	log.Info("Starting orphaned image sweep...")
	go startOrphanImageSweep(apper.App())
```

In `jobs.go`, change the sweep loop body to:

```go
		<-t.C
		app.refreshSettings(context.Background())
		if !app.Config().Uploads.Enabled {
			continue
		}
		log.Info("[jobs] Sweeping orphaned image uploads...")
		sweepOrphanedImages(app)
```

(add `"context"` to `jobs.go`'s imports).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -tags sqlite -run 'TestUploads|TestImage|Images' -count=1 .`
Expected: PASS.

- [ ] **Step 5: Run the full suite and commit**

Run: `go test -count=1 -tags sqlite ./...` and expect PASS.

```bash
git add routes.go app.go jobs.go settings_uploads_test.go
git commit -m "Let image uploads be turned on and off without a restart

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: `writefreely settings` CLI

**Files:**
- Create: `settings_cli.go`
- Create: `cmd/writefreely/settings.go`
- Modify: `cmd/writefreely/main.go` (`app.Commands`)
- Test: `settings_cli_test.go`

**Interfaces:**
- Consumes: `importSettings`, `loadSettings`, `saveSettings`, `settingNameError`, `config.*` registry functions.
- Produces (exported, package `writefreely`):
  - `type SettingRow struct { Name, Value string; Default bool }`
  - `func SettingsList(apper Apper) ([]SettingRow, error)`
  - `func SettingGet(apper Apper, name string) (SettingRow, error)`
  - `func SettingSet(apper Apper, name, value string) error`
  - `func SettingsExport(apper Apper) (string, error)`

- [ ] **Step 1: Write the failing tests**

`settings_cli_test.go`:

```go
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
	"path/filepath"
	"strings"
	"testing"
)

// cliTestApp is a NewApp on a fresh SQLite file, set up as an operator
// would have it: a full ini, then `db init`.
func cliTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	ini := filepath.Join(dir, "config.ini")
	writeTestINI(t, ini, `[server]
port = 8080
[database]
type     = sqlite3
filename = `+filepath.Join(dir, "wf.db")+`
[app]
host      = https://blog.example
site_name = CLI Site
`)
	app := NewApp(ini)
	if err := CreateSchema(app); err != nil {
		t.Fatal(err)
	}
	return NewApp(ini)
}

func TestSettingsCLI(t *testing.T) {
	app := cliTestApp(t)
	row, err := SettingGet(app, "app.site_name")
	if err != nil || row.Value != "CLI Site" || row.Default {
		t.Fatalf("get %+v err %v (first use must import the ini)", row, err)
	}
	if err := SettingSet(NewApp(app.cfgFile), "app.private", "true"); err != nil {
		t.Fatal(err)
	}
	row, _ = SettingGet(NewApp(app.cfgFile), "app.private")
	if row.Value != "true" {
		t.Errorf("private %q", row.Value)
	}
	rows, err := SettingsList(NewApp(app.cfgFile))
	if err != nil || len(rows) == 0 {
		t.Fatalf("list %v err %v", rows, err)
	}
	out, err := SettingsExport(NewApp(app.cfgFile))
	if err != nil || !strings.Contains(out, "site_name") || !strings.Contains(out, "CLI Site") {
		t.Errorf("export %q err %v", out, err)
	}
}

func TestSettingsCLIRefusals(t *testing.T) {
	app := cliTestApp(t)
	if _, err := SettingGet(app, "app.host"); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("get bootstrap: %v", err)
	}
	if err := SettingSet(NewApp(app.cfgFile), "database.password", "x"); err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("set bootstrap: %v", err)
	}
	if err := SettingSet(NewApp(app.cfgFile), "app.max_blogs", "lots"); err == nil {
		t.Error("invalid value accepted")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -tags sqlite -run TestSettingsCLI -count=1 .`
Expected: FAIL with `undefined: SettingGet`.

- [ ] **Step 3: Implement `settings_cli.go`**

```go
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
```

Note that `openSettings` calls `apper.LoadConfig()`, which reads `.cfg` from `LoadConfig` itself. `openSettings` reads no `.cfg`, so the guard test passes unchanged.

- [ ] **Step 4: Implement the command**

`cmd/writefreely/settings.go`:

```go
/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
	"github.com/writefreely/writefreely"
)

var cmdSettings = cli.Command{
	Name:  "settings",
	Usage: "show and change the settings stored in the database",
	Subcommands: []*cli.Command{
		{
			Name:  "list",
			Usage: "list every setting and its value",
			Action: func(c *cli.Context) error {
				rows, err := writefreely.SettingsList(writefreely.NewApp(c.String("c")))
				if err != nil {
					return err
				}
				tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				for _, r := range rows {
					mark := ""
					if r.Default {
						mark = "(default)"
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, r.Value, mark)
				}
				return tw.Flush()
			},
		},
		{
			Name:      "get",
			Usage:     "print one setting's value",
			ArgsUsage: "<name>",
			Action: func(c *cli.Context) error {
				if c.NArg() != 1 {
					return cli.Exit("usage: writefreely settings get <name>", 2)
				}
				r, err := writefreely.SettingGet(writefreely.NewApp(c.String("c")), c.Args().First())
				if err != nil {
					return err
				}
				fmt.Println(r.Value)
				return nil
			},
		},
		{
			Name:      "set",
			Usage:     "change one setting; running servers apply it on their next request",
			ArgsUsage: "<name> <value>",
			Action: func(c *cli.Context) error {
				if c.NArg() != 2 {
					return cli.Exit("usage: writefreely settings set <name> <value>", 2)
				}
				return writefreely.SettingSet(writefreely.NewApp(c.String("c")), c.Args().Get(0), c.Args().Get(1))
			},
		},
		{
			Name:  "export",
			Usage: "print the settings as a config.ini fragment (for downgrading)",
			Action: func(c *cli.Context) error {
				out, err := writefreely.SettingsExport(writefreely.NewApp(c.String("c")))
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			},
		},
	},
}
```

In `cmd/writefreely/main.go`, add `&cmdSettings,` to `app.Commands` after `&cmdConfig,`.

Check how the existing subcommands read the `-c` flag, e.g. `c.String("c")` in `cmd/writefreely/config.go`. If they use the parent context or `c.Lineage()`, match that.

- [ ] **Step 5: Run the tests and try the binary**

Run: `go test -tags sqlite -run TestSettingsCLI -count=1 . && go build -tags sqlite -o /tmp/wf-settings ./cmd/writefreely && /tmp/wf-settings settings --help`
Expected: tests PASS, and the help lists `list`, `get`, `set` and `export`.

- [ ] **Step 6: Run the full suite and commit**

Run: `go test -count=1 -tags sqlite ./...` and expect PASS.

```bash
git add settings_cli.go settings_cli_test.go cmd/writefreely/settings.go cmd/writefreely/main.go
git commit -m "Add writefreely settings list, get, set and export

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Docs, changelog and an end-to-end check on every engine

**Files:**
- Create: `docs/settings.md`
- Modify: `docs/docker.md`, `docker-entrypoint.sh` (comments only), `CHANGELOG.md`

- [ ] **Step 1: Write `docs/settings.md`**

```markdown
# Settings

WriteFreely keeps its configuration in two places.

**`config.ini` holds infrastructure and credentials**: everything in
`[server]`, `[database]`, `[email]` and every `[oauth.*]` section, plus
`[app] host`, `[app] single_user`, `[app] disable_password_auth` and
`[uploads] dir`. A running server only reads this file. Change it, then
restart. Any value may be a `${VARIABLE}` reference to the environment.

**The database holds everything else**: site name and description,
appearance, registration, federation (including `federation_allowlist` and
`instance_announce`), the reader, invites, default visibility, update
checks, and whether image uploads are on and how large they may be. Every
server sharing the database uses the same values. A change made on one
takes effect on the others with their next request.

## Changing settings

From the browser: **Admin → Settings**.

From a shell, e.g. on a headless install or with `docker compose exec`:

    writefreely settings list
    writefreely settings get  app.private
    writefreely settings set  app.private true
    writefreely settings export

`set` refuses a `config.ini` key and says so.

## Upgrading

The first time a new version starts (or runs `writefreely db migrate`), it
copies every database setting your instance was running with out of
`config.ini` into the database, checks the copy, and removes those keys
from `config.ini`. No backup of the old file is kept. Every other server
sharing the database does the same with its own file. If its values
disagreed with the database, it logs which ones, and the database's
values win.

If `config.ini` is read-only (a Docker `:ro` mount, a file owned by
configuration management), WriteFreely logs the keys to remove by hand and
starts anyway. Values left in the file are ignored.

## Downgrading

An older version reads only `config.ini`, so it would start with default
settings. Before downgrading, run

    writefreely settings export >> config.ini

and check the result.
```

- [ ] **Step 2: Update `docs/docker.md` and the entrypoint comments**

In `docs/docker.md`, find every instruction that edits a DB-bound key in `config.ini` (search for `site_name`, `federation`, `private`, `open_registration`, `local_timeline`, `[uploads]`). Replace each with the matching `docker compose exec app writefreely settings set <name> <value>` command, and link `settings.md`. Add one sentence near the top: "After the first start, only the keys listed in settings.md remain in `config.ini`."

In `docker-entrypoint.sh`, add to the header comment, after the variables list:

```sh
# `--migrate` also moves settings out of config.ini into the database on
# the first start of this version, and removes them from the file. A
# read-only config.ini is fine: the keys are logged and ignored.
```

- [ ] **Step 3: Add the changelog bullets**

Under `## [Unreleased]` in `CHANGELOG.md`, keep the existing headings and their order, and add one bullet under each of the following headings. Create a heading if it is missing:

```markdown
### Changed

- Instance settings now live in the database, so every server sharing it uses the same values; upgrading moves them out of config.ini and removes them from the file, without a backup.

### Added

- `writefreely settings list|get|set|export` changes settings without editing a file, and the admin page now covers every setting kept in the database.
```

Check that each bullet is at most 35 words (count them).

- [ ] **Step 4: Run every engine and the race detector**

```bash
gofmt -l .
go vet -composites=false ./...
go build ./... && go build -tags sqlite ./...
go test -count=1 -tags sqlite ./...
go test -race -count=1 -tags sqlite .
make test-postgres GOTESTFLAGS='-tags sqlite'
make test-mysql GOTESTFLAGS='-tags sqlite'
```

Expected: `gofmt` prints nothing, and every other command passes. `TestSettingsClaimConcurrent` runs (not skipped) on Postgres and MySQL.

- [ ] **Step 5: Docker check against a read-only ini (local Docker only)**

```bash
docker build -t wisp-settings-test .
mkdir -p /tmp/wisp-ro && cd /tmp/wisp-ro
cat > config.ini <<'EOF'
[server]
bind = 0.0.0.0
port = 8080
[database]
type     = sqlite3
filename = /data/wf.db
[app]
host      = http://localhost:8080
site_name = RO Check
EOF
docker run --rm -d --name wisp-ro -p 127.0.0.1:18080:8080 \
  -v "$PWD/config.ini:/data/config.ini:ro" -v wisp-ro-data:/data wisp-settings-test
sleep 5; docker logs wisp-ro 2>&1 | grep -E 'Moved|could not remove'
curl -s http://127.0.0.1:18080/ | grep -o 'RO Check' | head -1
docker exec wisp-ro writefreely settings get app.site_name
docker rm -f wisp-ro && docker volume rm wisp-ro-data
```

Expected: the logs show `Moved N settings` and `could not remove app.site_name`, the page shows `RO Check`, and `settings get` prints `RO Check`. Check the image's actual working directory and config path in `Dockerfile` and `docs/docker.md` first, and adjust the mount target to match. This runs on local Docker only and never on a server.

- [ ] **Step 6: Commit**

```bash
git add docs/settings.md docs/docker.md docker-entrypoint.sh CHANGELOG.md
git commit -m "Document where settings live and how they move on upgrade

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Out of scope (do not do)

- AES keys under `keys/` (WFPG-14 part 1).
- Any change to a deployment's own `config.ini` copy or compose files.
- Pushing, opening the pull request, or releasing. These are the human's call after review.
