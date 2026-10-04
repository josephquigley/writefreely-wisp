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
	"strings"
	"testing"
)

const storageTestHost = "[app]\nhost = http://localhost:8080\n"

func TestLoadWithoutStorageSectionKeepsImagesLocal(t *testing.T) {
	cfg, err := Load(writeConfig(t, storageTestHost))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Storage.UsesS3() {
		t.Error("no [storage] section must mean local storage")
	}
}

func TestLoadS3StorageWithEnvCredentials(t *testing.T) {
	t.Setenv("WF_TEST_S3_KEY", "GK0123456789abcdef01234567")
	t.Setenv("WF_TEST_S3_SECRET", "p@ss$word")
	cfg, err := Load(writeConfig(t, storageTestHost+`
[storage]
type = s3
s3_endpoint = http://localhost:3900
s3_region = garage
s3_bucket = blog
s3_prefix = /uploads/
s3_access_key_id = ${WF_TEST_S3_KEY}
s3_secret_access_key = ${WF_TEST_S3_SECRET}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.Storage
	if !s.UsesS3() || s.S3Bucket != "blog" || s.S3Region != "garage" || s.S3VirtualHost {
		t.Errorf("unexpected storage config: type=%q bucket=%q region=%q vhost=%v", s.Type, s.S3Bucket, s.S3Region, s.S3VirtualHost)
	}
	if s.S3AccessKeyID != "GK0123456789abcdef01234567" || s.S3SecretAccessKey != "p@ss$word" {
		t.Error("credentials were not read from the environment")
	}
	if s.S3Prefix != "uploads" {
		t.Errorf("prefix = %q, want slashes trimmed", s.S3Prefix)
	}
}

func TestStorageValidationRefusesBrokenS3WithoutLeakingSecrets(t *testing.T) {
	const secret = "do-not-print-this-secret"
	good := StorageCfg{
		Type:              "s3",
		S3Endpoint:        "https://s3.example.org",
		S3Bucket:          "blog",
		S3AccessKeyID:     "GK0123456789abcdef01234567",
		S3SecretAccessKey: secret,
	}
	if err := good.validate(""); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}

	for name, mutate := range map[string]func(*StorageCfg){
		"unknown type":     func(s *StorageCfg) { s.Type = "gcs" },
		"no scheme":        func(s *StorageCfg) { s.S3Endpoint = "s3.example.org" },
		"ftp scheme":       func(s *StorageCfg) { s.S3Endpoint = "ftp://s3.example.org" },
		"endpoint path":    func(s *StorageCfg) { s.S3Endpoint = "https://s3.example.org/blog" },
		"endpoint creds":   func(s *StorageCfg) { s.S3Endpoint = "https://GK0:" + secret + "@s3.example.org" },
		"no bucket":        func(s *StorageCfg) { s.S3Bucket = "" },
		"no key":           func(s *StorageCfg) { s.S3AccessKeyID = "" },
		"no secret":        func(s *StorageCfg) { s.S3SecretAccessKey = " " },
		"prefix traversal": func(s *StorageCfg) { s.S3Prefix = "a/../b" },
		"prefix double /":  func(s *StorageCfg) { s.S3Prefix = "a//b" },
	} {
		s := good
		mutate(&s)
		err := s.validate("")
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error contains the secret", name)
		}
	}

	for _, typ := range []string{"", "local", "LOCAL", " s3 "} {
		s := good
		s.Type = typ
		if err := s.validate(""); err != nil {
			t.Errorf("type %q refused: %v", typ, err)
		}
	}
}

// A settings save must not write a secret over the ${VAR} that names it, and
// must not invent a [storage] section full of empty keys.
func TestSaveKeepsStorageEnvRefs(t *testing.T) {
	t.Setenv("WF_TEST_S3_KEY", "GK0123456789abcdef01234567")
	t.Setenv("WF_TEST_S3_SECRET", "real-secret")
	p := writeConfig(t, storageTestHost+`
[storage]
type = s3
s3_endpoint = http://localhost:3900
s3_bucket = blog
s3_access_key_id = ${WF_TEST_S3_KEY}
s3_secret_access_key = ${WF_TEST_S3_SECRET}
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := Save(cfg, p); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "real-secret") || !strings.Contains(string(b), "${WF_TEST_S3_SECRET}") {
		t.Errorf("secret written into config.ini:\n%s", b)
	}

	p = writeConfig(t, storageTestHost)
	cfg, err = Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := Save(cfg, p); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), "s3_") {
		t.Errorf("save invented storage keys:\n%s", b)
	}
}

func TestLoadImageURLBase(t *testing.T) {
	t.Setenv("WF_TEST_S3_KEY", "GK0123456789abcdef01234567")
	t.Setenv("WF_TEST_S3_SECRET", "secret")
	t.Setenv("WF_TEST_MEDIA", "https://media.example.org/")
	cfg, err := Load(writeConfig(t, storageTestHost+`
[storage]
type = s3
s3_endpoint = http://localhost:3900
s3_bucket = blog
s3_access_key_id = ${WF_TEST_S3_KEY}
s3_secret_access_key = ${WF_TEST_S3_SECRET}
image_url_base = ${WF_TEST_MEDIA}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Storage.ImageURLBase; got != "https://media.example.org" {
		t.Errorf("image_url_base = %q, want it read from the environment with the trailing slash stripped", got)
	}

	cfg, err = Load(writeConfig(t, storageTestHost))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Storage.ImageURLBase != "" {
		t.Errorf("image_url_base = %q with no [storage] section, want empty", cfg.Storage.ImageURLBase)
	}
}

func TestStorageValidationOfImageURLBase(t *testing.T) {
	const host = "https://blog.example.org"
	s3 := StorageCfg{
		Type:              "s3",
		S3Endpoint:        "https://s3.example.org",
		S3Bucket:          "blog",
		S3AccessKeyID:     "GK0123456789abcdef01234567",
		S3SecretAccessKey: "secret",
	}

	for in, want := range map[string]string{
		"":                                   "",
		"  ":                                 "",
		"https://media.example.org":          "https://media.example.org",
		"https://media.example.org/":         "https://media.example.org",
		"http://localhost:3902":              "http://localhost:3902",
		"https://cdn.example.org/blog/":      "https://cdn.example.org/blog",
		"/media":                             "/media",
		"/media/":                            "/media",
		" /media/blog ":                      "/media/blog",
		"https://blog.example.org/media":     "https://blog.example.org/media",
		"https://media.example.org/uploads/": "https://media.example.org/uploads",
	} {
		s := s3
		s.ImageURLBase = in
		if err := s.validate(host); err != nil {
			t.Errorf("%q refused: %v", in, err)
			continue
		}
		if s.ImageURLBase != want {
			t.Errorf("%q normalised to %q, want %q", in, s.ImageURLBase, want)
		}
	}

	for _, in := range []string{
		"media.example.org",
		"ftp://media.example.org",
		"https://",
		"//media.example.org",
		"/",
		"media",
		"https://user:pw@media.example.org",
		"https://media.example.org/?v=1",
		"https://media.example.org/#x",
		"/media?x=1",
		"/media/../uploads",
		"/media//blog",
		// /uploads/ on this host is what redirects to the base, so a base
		// there would redirect to itself.
		"/uploads",
		"/uploads/media",
		"https://blog.example.org/uploads",
		"https://BLOG.example.org/uploads/x",
	} {
		s := s3
		s.ImageURLBase = in
		if err := s.validate(host); err == nil {
			t.Errorf("%q accepted", in)
		} else if !strings.Contains(err.Error(), "image_url_base") {
			t.Errorf("%q: error %q does not name the key", in, err)
		}
	}

	// Nothing but S3 can serve the files at the base.
	for _, typ := range []string{"", "local"} {
		s := StorageCfg{Type: typ, ImageURLBase: "https://media.example.org"}
		if err := s.validate(host); err == nil || !strings.Contains(err.Error(), "type = s3") {
			t.Errorf("type %q with image_url_base: got %v, want a refusal naming type = s3", typ, err)
		}
	}
}
