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
	"io"
	"net/http"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshImageMetadata(t *testing.T) {
	stub, s := newStubS3(t, "")
	ctx := context.Background()

	// Objects as an older version wrote them: a type and nothing else.
	// The user metadata stands in for anything else on an object, which
	// REPLACE would otherwise drop.
	stub.objects["2026/10/01/a.png"] = stubObject{body: []byte("a"), header: http.Header{
		"Content-Type":    {"image/png"},
		"X-Amz-Meta-Note": {"kept"},
	}}
	stub.objects["2026/10/01/b.svg"] = stubObject{body: []byte("<svg/>"), header: http.Header{"Content-Type": {svgMIME}}}
	// The type the store has is kept even when it is not what the name
	// suggests; this command changes caching, not types.
	stub.objects["2026/10/01/c.jpg"] = stubObject{body: []byte("c"), header: http.Header{"Content-Type": {"image/pjpeg"}}}
	require.NoError(t, s.Put(ctx, "2026/10/01/d.gif", []byte("d"), "image/gif"))
	refs := []imageRef{
		{Path: "2026/10/01/a.png"}, {Path: "2026/10/01/b.svg"}, {Path: "2026/10/01/c.jpg"},
		{Path: "2026/10/01/d.gif"}, {Path: "2026/10/01/gone.png"},
	}

	var out bytes.Buffer
	r := refreshImageMetadata(ctx, refs, s, true, &out)
	assert.Equal(t, imageMetaReport{Updated: 3, Current: 1, Missing: 1}, r, out.String())
	assert.Equal(t, 0, stub.copies, "a dry run writes nothing")
	assert.Contains(t, out.String(), "2026/10/01/a.png")
	assert.Contains(t, out.String(), "2026/10/01/gone.png")

	out.Reset()
	r = refreshImageMetadata(ctx, refs, s, false, &out)
	assert.Equal(t, imageMetaReport{Updated: 3, Current: 1, Missing: 1}, r, out.String())
	assert.Equal(t, 3, stub.copies)

	h := stub.header("2026/10/01/a.png")
	assert.Equal(t, "image/png", h.Get("Content-Type"))
	assert.Equal(t, imageCacheControl, h.Get("Cache-Control"))
	assert.Empty(t, h.Get("Content-Disposition"))
	assert.Equal(t, "kept", h.Get("X-Amz-Meta-Note"))
	h = stub.header("2026/10/01/b.svg")
	assert.Equal(t, svgMIME, h.Get("Content-Type"))
	assert.Equal(t, "attachment", h.Get("Content-Disposition"))
	assert.Equal(t, "image/pjpeg", stub.header("2026/10/01/c.jpg").Get("Content-Type"))
	assert.Equal(t, []byte("<svg/>"), stub.objects["2026/10/01/b.svg"].body)

	// Idempotent: a second run finds nothing to do.
	out.Reset()
	r = refreshImageMetadata(ctx, refs, s, false, &out)
	assert.Equal(t, imageMetaReport{Current: 4, Missing: 1}, r, out.String())
	assert.Equal(t, 3, stub.copies)
}

func TestRefreshImageMetadataRefusesLocalStorage(t *testing.T) {
	app, _, _, _ := newImageTestApp(t)
	assert.ErrorIs(t, refreshImageMetadataIn(app, false, io.Discard), errNoS3Storage)
}

// TestS3ObjectMetadataEndToEnd checks the same against a real store, since a
// stub only knows what its author thought S3 does.
func TestS3ObjectMetadataEndToEnd(t *testing.T) {
	cfg := s3TestConfig(t)
	s, err := newS3ImageStore(cfg)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, "2026/10/04/a.svg", []byte("<svg/>"), svgMIME))
	info, err := s.client.StatObject(ctx, s.bucket, s.key("2026/10/04/a.svg"), minio.StatObjectOptions{})
	require.NoError(t, err)
	assert.Equal(t, imageCacheControl, info.Metadata.Get("Cache-Control"))
	assert.Equal(t, "attachment", info.Metadata.Get("Content-Disposition"))
	assert.Equal(t, svgMIME, info.ContentType)

	// An object from before: no Cache-Control.
	_, err = s.client.PutObject(ctx, s.bucket, s.key("2026/10/04/b.png"), bytes.NewReader([]byte("b")), 1, minio.PutObjectOptions{ContentType: "image/png"})
	require.NoError(t, err)
	refs := []imageRef{{Path: "2026/10/04/a.svg"}, {Path: "2026/10/04/b.png"}}
	var out bytes.Buffer
	assert.Equal(t, imageMetaReport{Updated: 1, Current: 1}, refreshImageMetadata(ctx, refs, s, false, &out), out.String())
	info, err = s.client.StatObject(ctx, s.bucket, s.key("2026/10/04/b.png"), minio.StatObjectOptions{})
	require.NoError(t, err)
	assert.Equal(t, imageCacheControl, info.Metadata.Get("Cache-Control"))
	assert.Equal(t, "image/png", info.ContentType)
	got, err := readImage(ctx, s, "2026/10/04/b.png")
	require.NoError(t, err)
	assert.Equal(t, "b", string(got))
	assert.Equal(t, imageMetaReport{Current: 2}, refreshImageMetadata(ctx, refs, s, false, &out))
}
