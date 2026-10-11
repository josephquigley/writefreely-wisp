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
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
)

// imageMetaReport counts what one metadata refresh found.
type imageMetaReport struct {
	Updated int // metadata replaced, or would be on a dry run
	Current int // already carried what Put now writes; left alone
	Missing int // recorded in post_images but not in the bucket
	Failed  int // could not be checked or updated; each is named
}

// RefreshImageMetadata gives every image already in the bucket the metadata
// Put now stores with a new one: Cache-Control, and for SVG,
// Content-Disposition: attachment. Images uploaded before that change carry
// only a type, which was all a bucket that only this app read needed.
// Anything serving the bucket directly for [storage] image_url_base needs
// the rest.
//
// Each object is copied onto itself with its metadata replaced, which is the
// only way S3 changes metadata. The bytes and Content-Type are kept, as is any
// user metadata. An object that already carries the right values is left
// alone, so running it again does nothing. With dryRun it only reports.
func RefreshImageMetadata(apper Apper, dryRun bool, out io.Writer) error {
	apper.LoadConfig()
	app := apper.App()
	if !app.Config().Storage.UsesS3() {
		return errNoS3Storage
	}
	connectToDatabase(app)
	defer shutdown(app)
	return refreshImageMetadataIn(app, dryRun, out)
}

// refreshImageMetadataIn is RefreshImageMetadata on an App whose
// configuration is loaded and whose database is connected.
func refreshImageMetadataIn(app *App, dryRun bool, out io.Writer) error {
	if !app.Config().Storage.UsesS3() {
		return errNoS3Storage
	}
	s, err := newS3ImageStore(app.Config().Storage)
	if err != nil {
		return err
	}
	refs, err := app.db.allImageRefs()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Checking %d images in bucket %s...\n", len(refs), app.Config().Storage.S3Bucket)
	r := refreshImageMetadata(context.Background(), refs, s, dryRun, out)
	verb := "Updated"
	if dryRun {
		verb = "Would update"
	}
	fmt.Fprintf(out, "%s %d, already current %d, missing %d, failed %d.\n", verb, r.Updated, r.Current, r.Missing, r.Failed)
	if dryRun {
		fmt.Fprintln(out, "Dry run: nothing was written.")
	}
	if r.Failed > 0 {
		return fmt.Errorf("%d images were not updated; see above", r.Failed)
	}
	return nil
}

// refreshImageMetadata brings each image's metadata up to what Put writes.
// Problems with one image are reported and counted, and the rest still run.
func refreshImageMetadata(ctx context.Context, refs []imageRef, s *s3ImageStore, dryRun bool, out io.Writer) imageMetaReport {
	var r imageMetaReport
	for _, ref := range refs {
		key := s.key(ref.Path)
		statCtx, cancel := context.WithTimeout(ctx, s3WriteTimeout)
		info, err := s.client.StatObject(statCtx, s.bucket, key, minio.StatObjectOptions{})
		cancel()
		if isNoSuchKey(err) {
			r.Missing++
			fmt.Fprintf(out, "  missing %s: not in the bucket\n", ref.Path)
			continue
		}
		if err != nil {
			r.Failed++
			fmt.Fprintf(out, "  FAILED %s: %v\n", ref.Path, err)
			continue
		}

		// The type the object has is kept, whatever it is: this changes
		// how images are cached, not what they are.
		contentType := info.ContentType
		if contentType == "" {
			contentType = mimeForPath(ref.Path)
		}
		want := imageObjectMetaFor(ref.Path, contentType)
		disposition := info.Metadata.Get("Content-Disposition")
		if want.ContentDisposition == "" {
			// Only SVG needs one; leave whatever else has.
			want.ContentDisposition = disposition
		}
		if info.Metadata.Get("Cache-Control") == want.CacheControl && disposition == want.ContentDisposition {
			r.Current++
			continue
		}

		if dryRun {
			r.Updated++
			fmt.Fprintf(out, "  would update %s\n", ref.Path)
			continue
		}
		copyCtx, cancel := context.WithTimeout(ctx, s3WriteTimeout)
		_, err = s.client.CopyObject(copyCtx, minio.CopyDestOptions{
			Bucket:             s.bucket,
			Object:             key,
			ReplaceMetadata:    true,
			UserMetadata:       info.UserMetadata,
			ContentType:        contentType,
			CacheControl:       want.CacheControl,
			ContentDisposition: want.ContentDisposition,
		}, minio.CopySrcOptions{Bucket: s.bucket, Object: key})
		cancel()
		if err != nil {
			r.Failed++
			fmt.Fprintf(out, "  FAILED %s: %v\n", ref.Path, err)
			continue
		}
		r.Updated++
		fmt.Fprintf(out, "  updated %s\n", ref.Path)
	}
	return r
}
