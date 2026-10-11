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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/writefreely/writefreely/config"
)

// Deadlines for talking to the object store. Without them a store that does
// not answer holds a request, or startup, through minio-go's retries, which
// run for minutes. minio-go stops retrying when the context is done, so
// these win over its own backoff. They are variables so tests can shorten
// them.
var (
	// s3ProbeTimeout bounds the whole startup check, which is several calls.
	s3ProbeTimeout = 15 * time.Second
	// s3WriteTimeout bounds one Put, Delete, Exists or ReadAll. Images are
	// capped at a few megabytes, so this is generous for a store that is
	// answering.
	s3WriteTimeout = 30 * time.Second
)

// s3UnreachableError marks a failure to get any answer from the store, as
// opposed to an answer saying no (a missing bucket, a refused key). The first
// is an outage that may pass; the second is configuration.
type s3UnreachableError struct{ err error }

func (e s3UnreachableError) Error() string { return e.err.Error() }
func (e s3UnreachableError) Unwrap() error { return e.err }

// s3Unreachable reports whether err is a failure to reach the store.
func s3Unreachable(err error) bool {
	var u s3UnreachableError
	return errors.As(err, &u)
}

// probeFailure describes a failed probe step, marking it unreachable when the
// store sent no S3 error response at all.
func probeFailure(what string, cause error) error {
	err := fmt.Errorf("%s: %w", what, cause)
	if minio.ToErrorResponse(cause).Code != "" {
		return err
	}
	return s3UnreachableError{err}
}

// s3ImageStore keeps images in a bucket of an S3-compatible object store,
// such as Garage or MinIO. Every node of a cluster pointed at the same bucket
// serves the same images, which is what a local directory cannot do.
//
// minio-go rather than aws-sdk-go-v2: it is one module instead of a dozen,
// speaks path-style addressing natively, and is tested against non-AWS
// stores, which is all this needs.
type s3ImageStore struct {
	client *minio.Client
	bucket string
	prefix string
}

func newS3ImageStore(cfg config.StorageCfg) (*s3ImageStore, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.S3Endpoint))
	if err != nil {
		return nil, fmt.Errorf("[storage] s3_endpoint is not a URL")
	}
	lookup := minio.BucketLookupPath
	if cfg.S3VirtualHost {
		lookup = minio.BucketLookupDNS
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, ""),
		Secure:       u.Scheme == "https",
		Region:       cfg.S3Region,
		BucketLookup: lookup,
	})
	if err != nil {
		// minio.New fails only on the endpoint's shape; the credentials are
		// not in its error.
		return nil, fmt.Errorf("[storage] s3: %v", err)
	}
	return &s3ImageStore{client: client, bucket: cfg.S3Bucket, prefix: s3KeyPrefix(cfg)}, nil
}

// s3KeyPrefix returns what every object key starts with: s3_prefix and a
// slash, or nothing. image_url_base URLs are built from it too.
func s3KeyPrefix(cfg config.StorageCfg) string {
	prefix := strings.Trim(cfg.S3Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return prefix
}

// s3ObjectKey returns the key the image at relPath is stored under. It is the
// one place a key is made, for the store and for image_url_base alike, so
// the URL readers are given and the object it names cannot drift apart.
func s3ObjectKey(prefix, relPath string) string {
	return prefix + strings.TrimPrefix(relPath, "/")
}

func (s *s3ImageStore) key(relPath string) string {
	return s3ObjectKey(s.prefix, relPath)
}

// imageObjectMeta is the HTTP metadata an image object is stored with.
// Something other than this app serves the bucket, at image_url_base, and the
// object's own metadata is all it has to go on, so the headers this app sends
// for a local image are kept on the object itself: the same Cache-Control,
// and for SVG, Content-Disposition: attachment, which stops a direct visit
// from loading it as a document (see uploadHeaders).
type imageObjectMeta struct {
	ContentType        string
	CacheControl       string
	ContentDisposition string
}

func imageObjectMetaFor(relPath, mime string) imageObjectMeta {
	m := imageObjectMeta{ContentType: mime, CacheControl: imageCacheControl}
	if mime == svgMIME || strings.HasSuffix(strings.ToLower(relPath), ".svg") {
		m.ContentDisposition = "attachment"
	}
	return m
}

func isNoSuchKey(err error) bool {
	return minio.ToErrorResponse(err).Code == "NoSuchKey"
}

func (s *s3ImageStore) Put(ctx context.Context, relPath string, b []byte, mime string) error {
	ctx, cancel := context.WithTimeout(ctx, s3WriteTimeout)
	defer cancel()
	meta := imageObjectMetaFor(relPath, mime)
	_, err := s.client.PutObject(ctx, s.bucket, s.key(relPath), bytes.NewReader(b), int64(len(b)),
		minio.PutObjectOptions{
			ContentType:        meta.ContentType,
			CacheControl:       meta.CacheControl,
			ContentDisposition: meta.ContentDisposition,
		})
	if err != nil && minio.ToErrorResponse(err).Code == "" {
		return s3UnreachableError{err}
	}
	return err
}

// ReadAll returns the bytes of the image at relPath, or errImageNotFound.
// Nothing serves images from here; `writefreely images sync` uses it to check
// each copy it makes against the SHA-256 recorded at upload.
func (s *s3ImageStore) ReadAll(ctx context.Context, relPath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, s3WriteTimeout)
	defer cancel()
	obj, err := s.client.GetObject(ctx, s.bucket, s.key(relPath), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	b, err := io.ReadAll(obj)
	if isNoSuchKey(err) {
		return nil, errImageNotFound
	}
	return b, err
}

// Delete removes the object. S3 does not treat a missing key as an error on
// delete, which is the contract here already.
func (s *s3ImageStore) Delete(ctx context.Context, relPath string) error {
	ctx, cancel := context.WithTimeout(ctx, s3WriteTimeout)
	defer cancel()
	return s.client.RemoveObject(ctx, s.bucket, s.key(relPath), minio.RemoveObjectOptions{})
}

func (s *s3ImageStore) Exists(ctx context.Context, relPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, s3WriteTimeout)
	defer cancel()
	_, err := s.client.StatObject(ctx, s.bucket, s.key(relPath), minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if isNoSuchKey(err) {
		return false, nil
	}
	return false, err
}

// Probe checks that the bucket exists and that these credentials can write to
// and delete from it, by doing both with a throwaway object.
//
// A failure to get any answer is returned as an s3UnreachableError, so that
// startup can tell an outage from a bucket or key that is wrong.
func (s *s3ImageStore) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s3ProbeTimeout)
	defer cancel()
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return probeFailure("S3 bucket "+s.bucket+" cannot be reached", err)
	}
	if !ok {
		return fmt.Errorf("S3 bucket %s does not exist", s.bucket)
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	probe := ".writable-" + hex.EncodeToString(nonce)
	if err := s.Put(ctx, probe, []byte{}, "application/octet-stream"); err != nil {
		return probeFailure("S3 bucket "+s.bucket+" is not writable", err)
	}
	if err := s.Delete(ctx, probe); err != nil {
		return probeFailure("S3 bucket "+s.bucket+" does not allow deletes", err)
	}
	return nil
}
