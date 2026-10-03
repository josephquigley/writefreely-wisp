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
	"fmt"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/writefreely/writefreely/config"
)

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
	prefix := strings.Trim(cfg.S3Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &s3ImageStore{client: client, bucket: cfg.S3Bucket, prefix: prefix}, nil
}

func (s *s3ImageStore) key(relPath string) string {
	return s.prefix + strings.TrimPrefix(relPath, "/")
}

func isNoSuchKey(err error) bool {
	return minio.ToErrorResponse(err).Code == "NoSuchKey"
}

func (s *s3ImageStore) Put(ctx context.Context, relPath string, b []byte, mime string) error {
	_, err := s.client.PutObject(ctx, s.bucket, s.key(relPath), bytes.NewReader(b), int64(len(b)),
		minio.PutObjectOptions{ContentType: mime})
	return err
}

func (s *s3ImageStore) Get(ctx context.Context, relPath string) (*storedImage, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, s.key(relPath), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// GetObject is lazy; Stat is what finds out whether there is anything
	// there.
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		if isNoSuchKey(err) {
			return nil, errImageNotFound
		}
		return nil, err
	}
	return &storedImage{ReadSeekCloser: obj, Size: info.Size, ModTime: info.LastModified, MIME: mimeForPath(relPath)}, nil
}

// Delete removes the object. S3 does not treat a missing key as an error on
// delete, which is the contract here already.
func (s *s3ImageStore) Delete(ctx context.Context, relPath string) error {
	return s.client.RemoveObject(ctx, s.bucket, s.key(relPath), minio.RemoveObjectOptions{})
}

func (s *s3ImageStore) Exists(ctx context.Context, relPath string) (bool, error) {
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
func (s *s3ImageStore) Probe(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("S3 bucket %s cannot be reached: %v", s.bucket, err)
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
		return fmt.Errorf("S3 bucket %s is not writable: %v", s.bucket, err)
	}
	if err := s.Delete(ctx, probe); err != nil {
		return fmt.Errorf("S3 bucket %s does not allow deletes: %v", s.bucket, err)
	}
	return nil
}
