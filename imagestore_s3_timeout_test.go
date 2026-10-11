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

// These tests need no real S3: they point the store at an endpoint that never
// answers, or that refuses connections.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/writefreely/writefreely/config"
)

// shortS3Timeouts makes the S3 deadlines short enough to wait for.
func shortS3Timeouts(t *testing.T) time.Duration {
	t.Helper()
	d := 300 * time.Millisecond
	oldProbe, oldWrite := s3ProbeTimeout, s3WriteTimeout
	s3ProbeTimeout, s3WriteTimeout = d, d
	t.Cleanup(func() { s3ProbeTimeout, s3WriteTimeout = oldProbe, oldWrite })
	return d
}

func s3CfgFor(endpoint string) config.StorageCfg {
	return config.StorageCfg{
		Type: config.StorageS3, S3Endpoint: endpoint, S3Bucket: "b",
		S3AccessKeyID: "AKIDSECRETSECRET", S3SecretAccessKey: "supersecretvalue",
	}
}

// hangingS3 accepts requests and never answers them.
func hangingS3(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv.URL
}

// refusingS3 is an address nothing listens on.
func refusingS3(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

func appWithS3(t *testing.T, endpoint string) *App {
	t.Helper()
	cfg := config.New()
	cfg.Storage = s3CfgFor(endpoint)
	cfg.Uploads.Enabled = true
	return &App{cfg: cfg}
}

func TestUnreachableS3DoesNotStopStartup(t *testing.T) {
	d := shortS3Timeouts(t)
	for name, endpoint := range map[string]string{"hangs": hangingS3(t), "refuses": refusingS3(t)} {
		t.Run(name, func(t *testing.T) {
			app := appWithS3(t, endpoint)
			start := time.Now()
			err := app.checkUploadsAtStartup()
			assert.NoError(t, err)
			assert.Less(t, time.Since(start), 5*d)
		})
	}
}

func TestUnreachableS3StartupLogDoesNotLeakCredentials(t *testing.T) {
	shortS3Timeouts(t)
	app := appWithS3(t, hangingS3(t))
	s, err := newS3ImageStore(app.cfg.Storage)
	require.NoError(t, err)
	perr := s.Probe(context.Background())
	require.Error(t, perr)
	assert.True(t, s3Unreachable(perr))
	assert.False(t, strings.Contains(perr.Error(), "supersecretvalue"))
}

func TestS3PutAndDeleteAreBounded(t *testing.T) {
	d := shortS3Timeouts(t)
	s, err := newS3ImageStore(s3CfgFor(hangingS3(t)))
	require.NoError(t, err)

	start := time.Now()
	assert.Error(t, s.Put(context.Background(), "a/b.png", []byte("x"), "image/png"))
	assert.Less(t, time.Since(start), 5*d)

	start = time.Now()
	assert.Error(t, s.Delete(context.Background(), "a/b.png"))
	assert.Less(t, time.Since(start), 5*d)

	start = time.Now()
	_, err = s.ReadAll(context.Background(), "a/b.png")
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 5*d)
}

func TestS3PutHonoursCancelledRequest(t *testing.T) {
	shortS3Timeouts(t)
	s3PutLong := 30 * time.Second
	s3WriteTimeout = s3PutLong
	s, err := newS3ImageStore(s3CfgFor(hangingS3(t)))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	assert.Error(t, s.Put(ctx, "a.png", []byte("x"), "image/png"))
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestWriteUploadedImageFailsPromptlyWhenS3IsDown(t *testing.T) {
	d := shortS3Timeouts(t)
	app := appWithS3(t, hangingS3(t))
	start := time.Now()
	err := app.writeUploadedImage(context.Background(), "a/b.png", []byte("x"))
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 5*d)
}

func TestMisconfiguredStorageStillRefusesToStart(t *testing.T) {
	cfg := config.New()
	cfg.Storage = config.StorageCfg{Type: config.StorageS3, S3Endpoint: "http://[::1", S3Bucket: "b"}
	cfg.Uploads.Enabled = true
	app := &App{cfg: cfg}
	assert.Error(t, app.initImageStore())
}
