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
	"os"
	"path/filepath"
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

// An image already in the store becomes servable the moment uploads are
// enabled, with the route registered at startup and no restart.
func TestUploadsServedAfterRuntimeEnable(t *testing.T) {
	a := loadedSettingsApp(t)
	dir := t.TempDir()
	a.cfg.Uploads.Dir = dir
	if err := a.loadSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "ab"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ab", "x.png"), []byte("png-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	h := uploadsGate(a, a.uploadsHandler())
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/"+uploadsDir+"/ab/x.png", nil))
		return rec
	}
	if rec := get(); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled: %d", rec.Code)
	}
	if err := a.saveSettings(context.Background(), map[string]string{"uploads.enabled": "true"}); err != nil {
		t.Fatal(err)
	}
	rec := get()
	if rec.Code != http.StatusOK || rec.Body.String() != "png-bytes" {
		t.Fatalf("enabled: %d %q", rec.Code, rec.Body.String())
	}
	if err := a.saveSettings(context.Background(), map[string]string{"uploads.enabled": "false"}); err != nil {
		t.Fatal(err)
	}
	if rec := get(); rec.Code != http.StatusNotFound {
		t.Errorf("disabled again: %d", rec.Code)
	}
}
