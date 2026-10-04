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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const immutableWeek = "public, max-age=604800, immutable"

func serveUploads(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
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
