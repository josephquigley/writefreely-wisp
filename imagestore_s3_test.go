/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

// The S3 tests run against a real S3-compatible store and skip without one.
// scripts/test-s3-garage.sh starts a throwaway Garage and prints the
// variables:
//
//	eval "$(scripts/test-s3-garage.sh)"
//	go test -count=1 -run 'S3|ImageStore|ImageSync|StreamImages' .
//
// Each test writes under a prefix of its own, so runs do not see each other.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/writefreely/writefreely/config"
)

// s3TestConfig returns a [storage] section pointing at the test store, under
// a prefix unique to this test, or skips.
func s3TestConfig(t *testing.T) config.StorageCfg {
	t.Helper()
	endpoint := os.Getenv("WF_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("WF_TEST_S3_ENDPOINT not set; see scripts/test-s3-garage.sh")
	}
	nonce := make([]byte, 4)
	rand.Read(nonce)
	cfg := config.StorageCfg{
		Type:              config.StorageS3,
		S3Endpoint:        endpoint,
		S3Region:          os.Getenv("WF_TEST_S3_REGION"),
		S3Bucket:          os.Getenv("WF_TEST_S3_BUCKET"),
		S3Prefix:          "test/" + strings.ToLower(t.Name()) + "-" + hex.EncodeToString(nonce),
		S3AccessKeyID:     os.Getenv("WF_TEST_S3_ACCESS_KEY_ID"),
		S3SecretAccessKey: os.Getenv("WF_TEST_S3_SECRET_ACCESS_KEY"),
	}
	return cfg
}

func newTestS3Store(t *testing.T) *s3ImageStore {
	t.Helper()
	cfg := s3TestConfig(t)
	s, err := newS3ImageStore(cfg)
	require.NoError(t, err)
	return s
}

// countS3Objects counts what is under the store's prefix.
func countS3Objects(t *testing.T, s *s3ImageStore) int {
	t.Helper()
	n := 0
	for obj := range s.client.ListObjects(context.Background(), s.bucket, minio.ListObjectsOptions{Prefix: s.prefix, Recursive: true}) {
		require.NoError(t, obj.Err)
		n++
	}
	return n
}

// imageStores returns every store this machine can test: always the local
// one, and S3 when a test store is configured.
func imageStores(t *testing.T) map[string]ImageStore {
	t.Helper()
	dir := t.TempDir()
	stores := map[string]ImageStore{"local": &localImageStore{root: func() string { return dir }}}
	if os.Getenv("WF_TEST_S3_ENDPOINT") != "" {
		stores["s3"] = newTestS3Store(t)
	}
	return stores
}

func TestImageStoreContract(t *testing.T) {
	for name, s := range imageStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			const p = "2026/10/03/photo.png"

			require.NoError(t, s.Probe(ctx))

			ok, err := s.Exists(ctx, p)
			require.NoError(t, err)
			assert.False(t, ok)
			_, err = s.Get(ctx, p)
			assert.ErrorIs(t, err, errImageNotFound)
			assert.NoError(t, s.Delete(ctx, p), "deleting a missing image is not an error")

			body := []byte("not really a png, but the store does not care")
			require.NoError(t, s.Put(ctx, p, body, "image/png"))
			ok, err = s.Exists(ctx, p)
			require.NoError(t, err)
			assert.True(t, ok)

			img, err := s.Get(ctx, p)
			require.NoError(t, err)
			assert.Equal(t, int64(len(body)), img.Size)
			assert.Equal(t, "image/png", img.MIME)
			got, err := io.ReadAll(img)
			require.NoError(t, err)
			img.Close()
			assert.Equal(t, body, got)

			// Seeking is what Range requests are served with.
			img, err = s.Get(ctx, p)
			require.NoError(t, err)
			_, err = img.Seek(4, io.SeekStart)
			require.NoError(t, err)
			tail, err := io.ReadAll(img)
			require.NoError(t, err)
			img.Close()
			assert.Equal(t, body[4:], tail)

			require.NoError(t, s.Put(ctx, p, []byte("replaced"), "image/png"))
			got, err = readImage(ctx, s, p)
			require.NoError(t, err)
			assert.Equal(t, "replaced", string(got))

			require.NoError(t, s.Delete(ctx, p))
			ok, err = s.Exists(ctx, p)
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}
}

func TestStreamImagesServesRangesAndRefusesTraversal(t *testing.T) {
	for name, s := range imageStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			body := []byte("0123456789abcdef")
			require.NoError(t, s.Put(ctx, "2026/10/03/a.png", body, "image/png"))
			h := http.StripPrefix("/uploads/", streamImages(s))

			get := func(method, target string, hdr map[string]string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, target, nil)
				for k, v := range hdr {
					req.Header.Set(k, v)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				return rec
			}

			rec := get("GET", "/uploads/2026/10/03/a.png", nil)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
			assert.Equal(t, "16", rec.Header().Get("Content-Length"))
			assert.Equal(t, body, rec.Body.Bytes())

			rec = get("GET", "/uploads/2026/10/03/a.png", map[string]string{"Range": "bytes=2-5"})
			assert.Equal(t, http.StatusPartialContent, rec.Code)
			assert.Equal(t, "2345", rec.Body.String())
			assert.Equal(t, "bytes 2-5/16", rec.Header().Get("Content-Range"))

			rec = get("HEAD", "/uploads/2026/10/03/a.png", nil)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "16", rec.Header().Get("Content-Length"))

			assert.Equal(t, http.StatusMethodNotAllowed, get("POST", "/uploads/2026/10/03/a.png", nil).Code)
			for _, bad := range []string{
				"/uploads/2026/10/03/missing.png",
				"/uploads/2026/10/03/",
				"/uploads/2026/10/03/../03/a.png",
				"/uploads/2026//10/03/a.png",
				"/uploads/.writable-x",
			} {
				assert.Equal(t, http.StatusNotFound, get("GET", bad, nil).Code, bad)
			}
		})
	}
}

func TestCleanImagePath(t *testing.T) {
	for in, want := range map[string]string{
		"2026/10/03/a.png":  "2026/10/03/a.png",
		"/2026/10/03/a.png": "2026/10/03/a.png",
	} {
		got, ok := cleanImagePath(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got)
	}
	for _, in := range []string{"", "/", "a/", "../a.png", "a/../b.png", "a/./b.png", "a//b.png", `a\b.png`, ".hidden", "a/.b.png"} {
		_, ok := cleanImagePath(in)
		assert.False(t, ok, in)
	}
}

func TestMimeForPath(t *testing.T) {
	assert.Equal(t, "image/png", mimeForPath("2026/10/03/a.png"))
	assert.Equal(t, "image/jpeg", mimeForPath("a.jpg"))
	assert.Equal(t, "image/jpeg", mimeForPath("a.JPEG"))
	assert.Equal(t, "image/gif", mimeForPath("a.gif"))
	assert.Equal(t, svgMIME, mimeForPath("a.svg"))
	assert.Equal(t, "application/octet-stream", mimeForPath("a.exe"))
}

// newS3ImageTestApp is newImageTestApp with images kept in the test store.
func newS3ImageTestApp(t *testing.T) (*App, http.Handler, *User, *s3ImageStore) {
	t.Helper()
	cfg := s3TestConfig(t)
	app, router, u, _ := newImageTestAppWith(t, func(c *config.Config) { c.Storage = cfg })
	s, ok := app.imageStore().(*s3ImageStore)
	require.True(t, ok, "configured for S3 but got %T", app.imageStore())
	return app, router, u, s
}

func TestS3UploadServeDedupeDelete(t *testing.T) {
	app, router, u, store := newS3ImageTestApp(t)
	png := tinyPNG(t)

	rec, status := doUpload(t, app, u, uploadRequest(t, "a.png", "image/png", png))
	require.Equal(t, http.StatusOK, status, rec.Body.String())
	imgID, url := uploadedURL(t, rec)
	assert.True(t, strings.HasPrefix(url, "/uploads/"), "the URL is the one local storage gives: %s", url)
	assert.Equal(t, 1, countS3Objects(t, store))
	assert.Equal(t, 0, countUploadedFiles(t, app), "nothing is written to local disk")

	// Served through this server, with the same hardening as local files.
	got := httptest.NewRecorder()
	router.ServeHTTP(got, httptest.NewRequest("GET", url, nil))
	assert.Equal(t, http.StatusOK, got.Code)
	assert.Equal(t, "nosniff", got.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "inline", got.Header().Get("Content-Disposition"))
	assert.Equal(t, "image/png", got.Header().Get("Content-Type"))
	assert.NotEmpty(t, got.Header().Get("Cache-Control"))
	assert.Empty(t, got.Header().Get("Location"), "never a redirect to the bucket")
	img, err := app.db.GetPostImage(imgID)
	require.NoError(t, err)
	assert.Equal(t, img.Sum, sha256Hex(got.Body.Bytes()))

	// The same bytes again resolve to the stored image.
	rec, _ = doUpload(t, app, u, uploadRequest(t, "again.png", "image/png", png))
	againID, againURL := uploadedURL(t, rec)
	assert.Equal(t, imgID, againID)
	assert.Equal(t, url, againURL)
	assert.Equal(t, 1, countS3Objects(t, store))

	_, status = doDelete(t, app, u, imgID)
	assert.Equal(t, http.StatusNoContent, status)
	assert.Equal(t, 0, countS3Objects(t, store))
	got = httptest.NewRecorder()
	router.ServeHTTP(got, httptest.NewRequest("GET", url, nil))
	assert.Equal(t, http.StatusNotFound, got.Code)
}

func TestS3ServesSVGAsAttachment(t *testing.T) {
	app, router, u, _ := newS3ImageTestApp(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"></svg>`)
	rec, status := doUpload(t, app, u, uploadRequest(t, "a.svg", "image/svg+xml", svg))
	require.Equal(t, http.StatusOK, status, rec.Body.String())
	_, url := uploadedURL(t, rec)

	got := httptest.NewRecorder()
	router.ServeHTTP(got, httptest.NewRequest("GET", url, nil))
	assert.Equal(t, http.StatusOK, got.Code)
	assert.Equal(t, svgMIME, got.Header().Get("Content-Type"))
	assert.Equal(t, "attachment", got.Header().Get("Content-Disposition"))
}

func TestS3OrphanSweepRemovesOldUnattachedImages(t *testing.T) {
	app, _, u, store := newS3ImageTestApp(t)
	_, _, post := createTemplateTestUser(t, app, "sweeper")

	rec, _ := doUpload(t, app, u, uploadRequest(t, "old.png", "image/png", tinyPNG(t)))
	oldID, _ := uploadedURL(t, rec)
	rec, _ = doUpload(t, app, u, uploadRequest(t, "new.jpg", "image/jpeg", tinyJPEG(t)))
	recentID, _ := uploadedURL(t, rec)
	rec, _ = doUpload(t, app, u, uploadRequest(t, "att.gif", "image/gif", animatedGIF(t)))
	attachedID, _ := uploadedURL(t, rec)
	assert.NoError(t, app.db.AttachImagesToPost(u.ID, post.ID, []string{attachedID}))

	assert.Equal(t, 3, countS3Objects(t, store))

	ageImage(t, app, oldID, 25)
	ageImage(t, app, recentID, 1)
	ageImage(t, app, attachedID, 25)

	sweepOrphanedImages(app)

	_, err := app.db.GetPostImage(oldID)
	assert.Error(t, err, "an abandoned draft upload must be swept")
	_, err = app.db.GetPostImage(recentID)
	assert.NoError(t, err, "a recent upload must be kept")
	_, err = app.db.GetPostImage(attachedID)
	assert.NoError(t, err, "an attached image must be kept")
	assert.Equal(t, 2, countS3Objects(t, store))
}

func TestS3ProbeRefusesMissingBucketAndBadKey(t *testing.T) {
	cfg := s3TestConfig(t)
	ctx := context.Background()

	missing := cfg
	missing.S3Bucket = "no-such-bucket-wisp-test"
	s, err := newS3ImageStore(missing)
	require.NoError(t, err)
	assert.Error(t, s.Probe(ctx))

	badKey := cfg
	badKey.S3SecretAccessKey = strings.Repeat("0", 64)
	s, err = newS3ImageStore(badKey)
	require.NoError(t, err)
	err = s.Probe(ctx)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), badKey.S3SecretAccessKey, "no credential in an error")
	assert.NotContains(t, err.Error(), cfg.S3SecretAccessKey, "no credential in an error")
}

// syncFixture writes images into a local store and returns their refs, as
// post_images would list them.
func syncFixture(t *testing.T, src ImageStore) []imageRef {
	t.Helper()
	ctx := context.Background()
	var refs []imageRef
	for i, b := range [][]byte{[]byte("first image"), []byte("second image"), []byte("third image")} {
		p := "2026/10/0" + string(rune('1'+i)) + "/img.png"
		require.NoError(t, src.Put(ctx, p, b, "image/png"))
		refs = append(refs, imageRef{Path: p, Sum: sha256Hex(b)})
	}
	return refs
}

func TestImageSyncCopiesOnceAndRepairs(t *testing.T) {
	dstDir := t.TempDir()
	dests := map[string]ImageStore{"local": &localImageStore{root: func() string { return dstDir }}}
	if os.Getenv("WF_TEST_S3_ENDPOINT") != "" {
		dests["s3"] = newTestS3Store(t)
	}
	for name, dst := range dests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			srcDir := t.TempDir()
			src := &localImageStore{root: func() string { return srcDir }}
			refs := syncFixture(t, src)
			var out bytes.Buffer

			r := syncImages(ctx, refs, src, dst, &out)
			assert.Equal(t, imageSyncReport{Copied: 3}, r, out.String())
			for _, ref := range refs {
				got, err := readImage(ctx, dst, ref.Path)
				require.NoError(t, err)
				assert.Equal(t, ref.Sum, sha256Hex(got))
			}

			r = syncImages(ctx, refs, src, dst, &out)
			assert.Equal(t, imageSyncReport{Present: 3}, r, "a second run copies nothing")

			// A damaged copy in the destination is found by its hash and
			// replaced.
			require.NoError(t, dst.Put(ctx, refs[1].Path, []byte("bit rot"), "image/png"))
			out.Reset()
			r = syncImages(ctx, refs, src, dst, &out)
			assert.Equal(t, imageSyncReport{Present: 2, Repaired: 1}, r)
			assert.Contains(t, out.String(), refs[1].Path)
			got, err := readImage(ctx, dst, refs[1].Path)
			require.NoError(t, err)
			assert.Equal(t, refs[1].Sum, sha256Hex(got))

			// A damaged or missing source is reported and never copied
			// over a good copy.
			require.NoError(t, os.WriteFile(filepath.Join(srcDir, filepath.FromSlash(refs[0].Path)), []byte("damaged"), 0644))
			require.NoError(t, src.Delete(ctx, refs[2].Path))
			out.Reset()
			r = syncImages(ctx, refs, src, dst, &out)
			assert.Equal(t, imageSyncReport{Present: 1, Failed: 2}, r)
			assert.Contains(t, out.String(), "does not match its recorded SHA-256")
			assert.Contains(t, out.String(), "not in the uploads directory")
			got, err = readImage(ctx, dst, refs[0].Path)
			require.NoError(t, err)
			assert.Equal(t, refs[0].Sum, sha256Hex(got), "the good copy is untouched")
		})
	}
}

func TestImageSyncListsEveryRecordedImage(t *testing.T) {
	app, _, u, _ := newImageTestApp(t)
	rec, _ := doUpload(t, app, u, uploadRequest(t, "a.png", "image/png", tinyPNG(t)))
	idA, _ := uploadedURL(t, rec)
	rec, _ = doUpload(t, app, u, uploadRequest(t, "b.jpg", "image/jpeg", tinyJPEG(t)))
	idB, _ := uploadedURL(t, rec)

	refs, err := app.db.allImageRefs()
	require.NoError(t, err)
	require.Len(t, refs, 2)
	for _, id := range []string{idA, idB} {
		img, err := app.db.GetPostImage(id)
		require.NoError(t, err)
		assert.Contains(t, refs, imageRef{Path: img.Path, Sum: img.Sum})
	}

	// And what the upload handler stored is what the sync verifies against.
	dstDir := t.TempDir()
	dst := &localImageStore{root: func() string { return dstDir }}
	var out bytes.Buffer
	r := syncImages(context.Background(), refs, app.imageStore(), dst, &out)
	assert.Equal(t, imageSyncReport{Copied: 2}, r, out.String())
}

func TestSyncImagesRefusesUnknownDestination(t *testing.T) {
	app, _, _, _ := newImageTestApp(t)
	assert.Error(t, SyncImages(app, "gcs", io.Discard))
	assert.ErrorIs(t, syncImagesToS3(app, io.Discard), errNoS3Storage)
}

// TestS3ImageSyncEndToEnd runs the command's path from post_images on a
// node's disk into the bucket: uploads made with local storage, then copied.
func TestS3ImageSyncEndToEnd(t *testing.T) {
	cfg := s3TestConfig(t)
	app, _, u, _ := newImageTestApp(t)
	for _, f := range []struct {
		name, mime string
		b          []byte
	}{
		{"a.png", "image/png", tinyPNG(t)},
		{"b.jpg", "image/jpeg", tinyJPEG(t)},
		{"c.gif", "image/gif", animatedGIF(t)},
	} {
		rec, status := doUpload(t, app, u, uploadRequest(t, f.name, f.mime, f.b))
		require.Equal(t, http.StatusOK, status, rec.Body.String())
	}
	require.Equal(t, 3, countUploadedFiles(t, app))

	app.cfg.Storage = cfg
	var out bytes.Buffer
	require.NoError(t, syncImagesToS3(app, &out), out.String())
	assert.Contains(t, out.String(), "Copied 3, already present 0, replaced 0, failed 0.")
	out.Reset()
	require.NoError(t, syncImagesToS3(app, &out), out.String())
	assert.Contains(t, out.String(), "Copied 0, already present 3, replaced 0, failed 0.")
	assert.Equal(t, 3, countUploadedFiles(t, app), "the uploads directory is left as it was")

	store, err := newS3ImageStore(cfg)
	require.NoError(t, err)
	assert.Equal(t, 3, countS3Objects(t, store))
	assert.NotContains(t, out.String(), cfg.S3SecretAccessKey)
	assert.NotContains(t, out.String(), cfg.S3AccessKeyID)
}
