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

// These tests need no real S3: they run against stubS3, an in-memory server
// that speaks just enough of the protocol for PutObject, StatObject and
// CopyObject, and records the headers each object was written with. The same
// behaviour against Garage is covered by TestS3ObjectMetadataEndToEnd in
// imagemeta_test.go, which skips without one.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubObject is what stubS3 keeps for one key.
type stubObject struct {
	body   []byte
	header http.Header
}

// stubS3 is an in-memory S3 endpoint with one bucket.
type stubS3 struct {
	mu      sync.Mutex
	objects map[string]stubObject
	copies  int
}

// storedHeaders are the request headers an object keeps and returns.
func storedHeaders(r *http.Request) http.Header {
	h := http.Header{}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || lk == "cache-control" || lk == "content-disposition" || strings.HasPrefix(lk, "x-amz-meta-") {
			h[k] = v
		}
	}
	return h
}

func (s *stubS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Path style: /bucket/key.
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) != 2 {
		w.WriteHeader(http.StatusOK)
		return
	}
	key := parts[1]
	switch r.Method {
	case http.MethodPut:
		if src := r.Header.Get("X-Amz-Copy-Source"); src != "" {
			src, _ = url.PathUnescape(src)
			srcParts := strings.SplitN(strings.TrimPrefix(src, "/"), "/", 2)
			from, ok := s.objects[srcParts[1]]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			h := from.header
			if r.Header.Get("X-Amz-Metadata-Directive") == "REPLACE" {
				h = storedHeaders(r)
			}
			s.objects[key] = stubObject{body: from.body, header: h}
			s.copies++
			io.WriteString(w, `<CopyObjectResult><ETag>"e"</ETag><LastModified>2026-01-01T00:00:00.000Z</LastModified></CopyObjectResult>`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		s.objects[key] = stubObject{body: b, header: storedHeaders(r)}
		w.Header().Set("ETag", `"e"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead, http.MethodGet:
		o, ok := s.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodGet {
				io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
			}
			return
		}
		for k, v := range o.header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(o.body)))
		w.Header().Set("ETag", `"e"`)
		w.Header().Set("Last-Modified", "Thu, 01 Jan 2026 00:00:00 GMT")
		if r.Method == http.MethodGet {
			w.Write(o.body)
		}
	case http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *stubS3) header(key string) http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[key].header
}

func newStubS3(t *testing.T, prefix string) (*stubS3, *s3ImageStore) {
	t.Helper()
	stub := &stubS3{objects: map[string]stubObject{}}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	cfg := s3CfgFor(srv.URL)
	// A region, so minio-go does not ask the stub for the bucket's location.
	cfg.S3Region = "us-east-1"
	cfg.S3Bucket = "blog"
	cfg.S3Prefix = prefix
	s, err := newS3ImageStore(cfg)
	require.NoError(t, err)
	return stub, s
}

func TestS3PutStoresServingMetadata(t *testing.T) {
	stub, s := newStubS3(t, "blog")
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, "2026/10/04/a.png", []byte("png"), "image/png"))
	h := stub.header("blog/2026/10/04/a.png")
	assert.Equal(t, "image/png", h.Get("Content-Type"))
	assert.Equal(t, imageCacheControl, h.Get("Cache-Control"))
	assert.Empty(t, h.Get("Content-Disposition"), "a raster image displays inline")

	require.NoError(t, s.Put(ctx, "2026/10/04/b.svg", []byte("<svg/>"), svgMIME))
	h = stub.header("blog/2026/10/04/b.svg")
	assert.Equal(t, svgMIME, h.Get("Content-Type"))
	assert.Equal(t, imageCacheControl, h.Get("Cache-Control"))
	assert.Equal(t, "attachment", h.Get("Content-Disposition"))
}

func TestImageObjectMetaFor(t *testing.T) {
	assert.Equal(t, imageObjectMeta{ContentType: "image/jpeg", CacheControl: imageCacheControl}, imageObjectMetaFor("a.jpg", "image/jpeg"))
	assert.Equal(t, "attachment", imageObjectMetaFor("a.svg", svgMIME).ContentDisposition)
	// Either the type or the name is enough to mark it.
	assert.Equal(t, "attachment", imageObjectMetaFor("a.SVG", "application/octet-stream").ContentDisposition)
	assert.Equal(t, "attachment", imageObjectMetaFor("a", svgMIME).ContentDisposition)
}
