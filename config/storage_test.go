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
	if err := good.validate(); err != nil {
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
		err := s.validate()
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
		if err := s.validate(); err != nil {
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
