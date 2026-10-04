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
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const immutableWeek = "public, max-age=604800, immutable"

// fakeImageStore is an in-memory ImageStore whose Get can be made to fail,
// standing in for an object store without needing one running.
type fakeImageStore struct {
	ImageStore
	objects map[string][]byte
	getErr  error
}

func (f *fakeImageStore) Get(_ context.Context, p string) (*storedImage, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	b, ok := f.objects[p]
	if !ok {
		return nil, errImageNotFound
	}
	return &storedImage{
		ReadSeekCloser: nopSeekCloser{bytes.NewReader(b)},
		Size:           int64(len(b)),
		ModTime:        time.Unix(1700000000, 0),
	}, nil
}

type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

func serveUploads(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestStreamedUploadsCacheOnlySuccess(t *testing.T) {
	store := &fakeImageStore{objects: map[string][]byte{"2026/01/01/a.png": tinyPNG(t)}}
	h := uploadsHandlerFor(store, imageURLs{})

	cases := []struct {
		name   string
		method string
		path   string
		err    error
		status int
		cached bool
	}{
		{"found", "GET", "/uploads/2026/01/01/a.png", nil, http.StatusOK, true},
		{"missing", "GET", "/uploads/2026/01/01/gone.png", nil, http.StatusNotFound, false},
		{"bad path", "GET", "/uploads/2026/01/01/.hidden.png", nil, http.StatusNotFound, false},
		{"store error", "GET", "/uploads/2026/01/01/a.png", errors.New("garage restarting"), http.StatusBadGateway, false},
		{"method", "POST", "/uploads/2026/01/01/a.png", nil, http.StatusMethodNotAllowed, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store.getErr = c.err
			rec := serveUploads(h, c.method, c.path)
			assert.Equal(t, c.status, rec.Code)
			if c.cached {
				assert.Equal(t, immutableWeek, rec.Header().Get("Cache-Control"))
			} else {
				assert.Empty(t, rec.Header().Get("Cache-Control"))
			}
			// Hardening applies to every response, error or not.
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.NotEmpty(t, rec.Header().Get("Content-Security-Policy"))
		})
	}
}

func TestLocalUploadsCacheOnlySuccess(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.png"), tinyPNG(t), 0o644))
	h := uploadsHandlerFor(&localImageStore{root: func() string { return dir }}, imageURLs{})

	rec := serveUploads(h, "GET", "/uploads/a.png")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, immutableWeek, rec.Header().Get("Cache-Control"))

	rec = serveUploads(h, "GET", "/uploads/missing.png")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, rec.Header().Get("Cache-Control"))
}
