/*
 * Copyright © 2018-2021 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

// Package config holds and assists in the configuration of a writefreely instance.
package config

import (
	"net/url"
	"os"
	"strings"

	"github.com/go-ini/ini"
	"github.com/writeas/web-core/log"
	"golang.org/x/net/idna"
)

const (
	// FileName is the default configuration file name
	FileName = "config.ini"

	// DefaultTheme is the stylesheet served when a configuration does not
	// name one.
	DefaultTheme = "write"

	UserNormal UserType = "user"
	UserAdmin           = "admin"
)

type (
	UserType string

	// ServerCfg holds values that affect how the HTTP server runs
	ServerCfg struct {
		HiddenHost string `ini:"hidden_host"`
		Port       int    `ini:"port"`
		Bind       string `ini:"bind"`

		TLSCertPath string `ini:"tls_cert_path"`
		TLSKeyPath  string `ini:"tls_key_path"`
		Autocert    bool   `ini:"autocert"`

		TemplatesParentDir string `ini:"templates_parent_dir"`
		StaticParentDir    string `ini:"static_parent_dir"`
		PagesParentDir     string `ini:"pages_parent_dir"`
		KeysParentDir      string `ini:"keys_parent_dir"`

		HashSeed string `ini:"hash_seed"`

		GopherPort int `ini:"gopher_port"`

		Dev bool `ini:"-"`
	}

	// DatabaseCfg holds values that determine how the application connects to a datastore
	DatabaseCfg struct {
		Type     string `ini:"type"`
		FileName string `ini:"filename"`
		User     string `ini:"username"`
		Password string `ini:"password"`
		Database string `ini:"database"`
		Host     string `ini:"host"`
		Port     int    `ini:"port"`
		TLS      bool   `ini:"tls"`
	}

	WriteAsOauthCfg struct {
		ClientID         string `ini:"client_id"`
		ClientSecret     string `ini:"client_secret"`
		AuthLocation     string `ini:"auth_location"`
		TokenLocation    string `ini:"token_location"`
		InspectLocation  string `ini:"inspect_location"`
		CallbackProxy    string `ini:"callback_proxy"`
		CallbackProxyAPI string `ini:"callback_proxy_api"`
	}

	GitlabOauthCfg struct {
		ClientID         string `ini:"client_id"`
		ClientSecret     string `ini:"client_secret"`
		Host             string `ini:"host"`
		DisplayName      string `ini:"display_name"`
		CallbackProxy    string `ini:"callback_proxy"`
		CallbackProxyAPI string `ini:"callback_proxy_api"`
	}

	GiteaOauthCfg struct {
		ClientID         string `ini:"client_id"`
		ClientSecret     string `ini:"client_secret"`
		Host             string `ini:"host"`
		DisplayName      string `ini:"display_name"`
		CallbackProxy    string `ini:"callback_proxy"`
		CallbackProxyAPI string `ini:"callback_proxy_api"`
	}

	SlackOauthCfg struct {
		ClientID         string `ini:"client_id"`
		ClientSecret     string `ini:"client_secret"`
		TeamID           string `ini:"team_id"`
		CallbackProxy    string `ini:"callback_proxy"`
		CallbackProxyAPI string `ini:"callback_proxy_api"`
	}

	GenericOauthCfg struct {
		ClientID           string `ini:"client_id"`
		ClientSecret       string `ini:"client_secret"`
		Host               string `ini:"host"`
		DisplayName        string `ini:"display_name"`
		CallbackProxy      string `ini:"callback_proxy"`
		CallbackProxyAPI   string `ini:"callback_proxy_api"`
		TokenEndpoint      string `ini:"token_endpoint"`
		InspectEndpoint    string `ini:"inspect_endpoint"`
		AuthEndpoint       string `ini:"auth_endpoint"`
		AuthUseRequestBody bool   `ini:"auth_use_basic_auth"`
		Scope              string `ini:"scope"`
		AllowDisconnect    bool   `ini:"allow_disconnect"`
		MapUserID          string `ini:"map_user_id"`
		MapUsername        string `ini:"map_username"`
		MapDisplayName     string `ini:"map_display_name"`
		MapEmail           string `ini:"map_email"`
	}

	// AppCfg holds values that affect how the application functions
	AppCfg struct {
		SiteName string `ini:"site_name"`
		SiteDesc string `ini:"site_description"`
		Host     string `ini:"host"`

		// Site appearance
		Theme      string `ini:"theme"`
		Editor     string `ini:"editor"`
		JSDisabled bool   `ini:"disable_js"`
		WebFonts   bool   `ini:"webfonts"`
		Landing    string `ini:"landing"`
		SimpleNav  bool   `ini:"simple_nav"`
		WFModesty  bool   `ini:"wf_modesty"`

		// Site functionality
		Chorus        bool `ini:"chorus"`
		Forest        bool `ini:"forest"` // The admin cares about the forest, not the trees. Hide unnecessary technical info.
		DisableDrafts bool `ini:"disable_drafts"`

		// Users
		SingleUser       bool `ini:"single_user"`
		OpenRegistration bool `ini:"open_registration"`
		OpenDeletion     bool `ini:"open_deletion"`
		MinUsernameLen   int  `ini:"min_username_len"`
		MaxBlogs         int  `ini:"max_blogs"`

		// Options for public instances
		// Federation
		Federation   bool `ini:"federation"`
		PublicStats  bool `ini:"public_stats"`
		Monetization bool `ini:"monetization"`
		NotesOnly    bool `ini:"notes_only"`

		// Access
		Private bool `ini:"private"`

		// FederationAllowlist is a comma-separated list of hostnames allowed
		// to read this instance's ActivityPub surface by signing their
		// requests. It requires Private. An empty value leaves the instance
		// behaving exactly as it did before this option existed.
		//
		// An entry may be a wildcard: "*.example.org" admits subdomains at
		// any depth, but not the apex "example.org", which must be listed
		// separately if it is also wanted. A wildcard trusts whoever controls
		// the zone, so use one only on a zone you control.
		FederationAllowlist string `ini:"federation_allowlist"`

		// Additional functions
		LocalTimeline bool   `ini:"local_timeline"`
		UserInvites   string `ini:"user_invites"`

		// Defaults
		DefaultVisibility string `ini:"default_visibility"`

		// Check for Updates
		UpdateChecks bool `ini:"update_checks"`

		// Disable password authentication if use only Oauth
		DisablePasswordAuth bool `ini:"disable_password_auth"`
	}

	EmailCfg struct {
		// SMTP configuration values
		Host           string `ini:"smtp_host"`
		Port           int    `ini:"smtp_port"`
		Username       string `ini:"smtp_username"`
		Password       string `ini:"smtp_password"`
		EnableStartTLS bool   `ini:"smtp_enable_start_tls"`

		// Mailgun configuration values
		Domain         string `ini:"domain"`
		MailgunPrivate string `ini:"mailgun_private"`
		MailgunEurope  bool   `ini:"mailgun_europe"`
	}

	// UploadsCfg holds values that control user image uploads, configured
	// under [uploads]:
	//
	//	[uploads]
	//	enabled = true
	//	max_size_mb = 10
	//
	// enabled defaults to false, so an instance operator opts in
	// deliberately. max_size_mb bounds a single file, not a user's total
	// storage.
	//
	// dir is where uploaded files are written. Left empty they go under
	// the static asset tree, which keeps a plain install self-contained.
	// A packaged install wants them somewhere writable and separate from
	// the read-only assets, such as /data/uploads.
	//
	// Files are stored under the day they were uploaded and named after
	// the file the writer sent, reduced to a slug: 2026/03/09/holiday.png.
	// A name already used that day gains a suffix rather than overwriting
	// what is there.
	//
	// Which image types are accepted is not configurable: it is fixed by
	// the decoders compiled in, in decodeAndReencode. WebP is excluded
	// because decoding it would require a dependency.
	UploadsCfg struct {
		Enabled   bool   `ini:"enabled"`
		MaxSizeMB int    `ini:"max_size_mb"`
		Dir       string `ini:"dir"`
	}

	// Config holds the complete configuration for running a writefreely instance
	Config struct {
		Server       ServerCfg       `ini:"server"`
		Database     DatabaseCfg     `ini:"database"`
		App          AppCfg          `ini:"app"`
		Email        EmailCfg        `ini:"email"`
		Uploads      UploadsCfg      `ini:"uploads"`
		SlackOauth   SlackOauthCfg   `ini:"oauth.slack"`
		WriteAsOauth WriteAsOauthCfg `ini:"oauth.writeas"`
		GitlabOauth  GitlabOauthCfg  `ini:"oauth.gitlab"`
		GiteaOauth   GiteaOauthCfg   `ini:"oauth.gitea"`
		GenericOauth GenericOauthCfg `ini:"oauth.generic"`
	}
)

// New creates a new Config with sane defaults
func New() *Config {
	c := &Config{
		Server: ServerCfg{
			Port: 8080,
			Bind: "localhost", /* IPV6 support when not using localhost? */
		},
		App: AppCfg{
			Host:           "http://localhost:8080",
			Theme:          DefaultTheme,
			WebFonts:       true,
			SingleUser:     true,
			MinUsernameLen: 3,
			MaxBlogs:       1,
			Federation:     true,
			PublicStats:    true,
		},
		Uploads: UploadsCfg{
			MaxSizeMB: 10,
		},
	}
	c.UseMySQL(true)
	return c
}

// UseMySQL resets the Config's Database to use default values for a MySQL setup.
func (cfg *Config) UseMySQL(fresh bool) {
	cfg.Database.Type = "mysql"
	if fresh {
		cfg.Database.Host = "localhost"
		cfg.Database.Port = 3306
	}
}

// UseSQLite resets the Config's Database to use default values for a SQLite setup.
func (cfg *Config) UseSQLite(fresh bool) {
	cfg.Database.Type = "sqlite3"
	if fresh {
		cfg.Database.FileName = "writefreely.db"
	}
}

// IsSecureStandalone returns whether or not the application is running as a
// standalone server with TLS enabled.
func (cfg *Config) IsSecureStandalone() bool {
	return cfg.Server.Port == 443 && cfg.Server.TLSCertPath != "" && cfg.Server.TLSKeyPath != ""
}

// MaxUploadBytes returns the largest accepted size of a single uploaded file,
// in bytes.
func (cfg *Config) MaxUploadBytes() int64 {
	mb := cfg.Uploads.MaxSizeMB
	if mb <= 0 {
		mb = 10
	}
	return int64(mb) * 1024 * 1024
}

func (ac *AppCfg) LandingPath() string {
	if !strings.HasPrefix(ac.Landing, "/") {
		return "/" + ac.Landing
	}
	return ac.Landing
}

func (lc EmailCfg) Enabled() bool {
	return (lc.Domain != "" && lc.MailgunPrivate != "") ||
		lc.Username != "" && lc.Password != "" && lc.Host != "" && lc.Port > 0
}

func (ac AppCfg) SignupPath() string {
	if !ac.OpenRegistration {
		return ""
	}
	if ac.Chorus || ac.Private || (ac.Landing != "" && ac.Landing != "/") {
		return "/signup"
	}
	return "/"
}

// Load reads the given configuration file, then parses and returns it as a Config.
func Load(fname string) (*Config, error) {
	if fname == "" {
		fname = FileName
	}
	cfg, err := ini.Load(fname)
	if err != nil {
		return nil, err
	}

	// Parse INI file
	uc := &Config{}
	err = cfg.MapTo(uc)
	if err != nil {
		return nil, err
	}

	// Values are mapped onto a zero-valued Config, so any key absent from
	// the file stays empty rather than picking up New()'s default. An empty
	// theme is not a usable state: the base template builds the stylesheet
	// URL from it, so a configuration that omits the key requests
	// "/css/.css" and the instance renders with no styling at all.
	if uc.App.Theme == "" {
		uc.App.Theme = DefaultTheme
	}

	// The asset directories are empty in every configuration written for
	// upstream's container image, which keeps templates, static and pages
	// beside the binary in the working directory. This image keeps them at
	// /usr/share/writefreely and works out of the state directory, so an
	// empty value resolves against a directory holding no assets and the
	// server exits loading templates.
	//
	// Filling them in here rather than at configuration time is what heals
	// an instance switched over from another image: it never runs the
	// interactive generator, so nothing else would ever set them. A value
	// already in the file is left alone, including a deliberate one.
	if _, isDocker := os.LookupEnv("WRITEFREELY_DOCKER"); isDocker {
		parentDir := dockerAssetParentDir()
		if uc.Server.TemplatesParentDir == "" {
			uc.Server.TemplatesParentDir = parentDir
		}
		if uc.Server.StaticParentDir == "" {
			uc.Server.StaticParentDir = parentDir
		}
		if uc.Server.PagesParentDir == "" {
			uc.Server.PagesParentDir = parentDir
		}
	}

	// Do any transformations
	u, err := url.Parse(uc.App.Host)
	if err != nil {
		return nil, err
	}
	d, err := idna.ToASCII(u.Hostname())
	if err != nil {
		log.Error("idna.ToASCII for %s: %s", u.Hostname(), err)
		return nil, err
	}
	uc.App.Host = u.Scheme + "://" + d
	if u.Port() != "" {
		uc.App.Host += ":" + u.Port()
	}

	return uc, nil
}

// Save writes the given Config to the given file.
func Save(uc *Config, fname string) error {
	cfg := ini.Empty()
	err := ini.ReflectFrom(cfg, uc)
	if err != nil {
		return err
	}

	if fname == "" {
		fname = FileName
	}
	err = cfg.SaveTo(fname)
	if err != nil {
		return err
	}
	return os.Chmod(fname, 0600)
}
