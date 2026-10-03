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
	"errors"
	"fmt"
	"io"

	"github.com/writefreely/writefreely/config"
)

// imageRef is what the sync needs from a post_images row: where the image is,
// and the hash of the bytes that were stored there.
type imageRef struct {
	Path string
	Sum  string
}

// imageSyncReport counts what one sync did with each image.
type imageSyncReport struct {
	Copied   int // absent from the destination, now copied
	Present  int // already there with the right bytes; left alone
	Repaired int // there with the wrong bytes; replaced
	Failed   int // could not be copied; each is named in the output
}

// SyncImages copies every image recorded in post_images from this node's
// uploads directory into the store [storage] names. It is meant to be run
// once, before switching an instance from local storage to S3, and is safe
// to run again: an image already in the bucket with the right bytes is not
// copied twice.
//
// Every image is checked against the SHA-256 recorded when it was uploaded,
// on both sides. A local file that does not match is reported and not
// copied, so a damaged file is never made the only copy; an object that does
// not match is replaced from the local file.
//
// It never writes to or deletes from the uploads directory.
func SyncImages(apper Apper, to string, out io.Writer) error {
	if to != config.StorageS3 {
		return fmt.Errorf("unknown destination %q: the only one is %q", to, config.StorageS3)
	}
	apper.LoadConfig()
	app := apper.App()
	if !app.Config().Storage.UsesS3() {
		return errNoS3Storage
	}
	connectToDatabase(app)
	defer shutdown(app)
	return syncImagesToS3(app, out)
}

var errNoS3Storage = errors.New("config.ini has no [storage] section with type = s3 to copy into")

// syncImagesToS3 is SyncImages on an App whose configuration is loaded and
// whose database is connected.
func syncImagesToS3(app *App, out io.Writer) error {
	if !app.Config().Storage.UsesS3() {
		return errNoS3Storage
	}
	dst, err := newS3ImageStore(app.Config().Storage)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := dst.Probe(ctx); err != nil {
		return err
	}
	refs, err := app.db.allImageRefs()
	if err != nil {
		return err
	}

	// Always the directory, whatever store the app itself would use: this
	// copies off this node's disk.
	src := &localImageStore{root: app.uploadsRoot}
	fmt.Fprintf(out, "Copying %d images from %s into bucket %s...\n", len(refs), app.uploadsRoot(), app.Config().Storage.S3Bucket)
	r := syncImages(ctx, refs, src, dst, out)
	fmt.Fprintf(out, "Copied %d, already present %d, replaced %d, failed %d.\n", r.Copied, r.Present, r.Repaired, r.Failed)
	if r.Failed > 0 {
		return fmt.Errorf("%d images were not copied; see above", r.Failed)
	}
	return nil
}

// allImageRefs lists every stored image, in a stable order so that two runs
// print the same thing.
func (db *datastore) allImageRefs() ([]imageRef, error) {
	rows, err := db.Query("SELECT path, sha256 FROM post_images ORDER BY path")
	if err != nil {
		return nil, fmt.Errorf("list post_images: %v", err)
	}
	defer rows.Close()
	var refs []imageRef
	for rows.Next() {
		var r imageRef
		if err := rows.Scan(&r.Path, &r.Sum); err != nil {
			return nil, fmt.Errorf("list post_images: %v", err)
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list post_images: %v", err)
	}
	return refs, nil
}

// syncImages copies each image from src to dst, verifying both ends. Problems
// with one image are reported and counted, and the rest still run.
func syncImages(ctx context.Context, refs []imageRef, src, dst ImageStore, out io.Writer) imageSyncReport {
	var r imageSyncReport
	fail := func(p, why string) {
		r.Failed++
		fmt.Fprintf(out, "  FAILED %s: %s\n", p, why)
	}
	for _, ref := range refs {
		b, err := readImage(ctx, src, ref.Path)
		if errors.Is(err, errImageNotFound) {
			fail(ref.Path, "not in the uploads directory")
			continue
		}
		if err != nil {
			fail(ref.Path, err.Error())
			continue
		}
		if sha256Hex(b) != ref.Sum {
			fail(ref.Path, "the local file does not match its recorded SHA-256; not copied")
			continue
		}

		existing, err := readImage(ctx, dst, ref.Path)
		switch {
		case err == nil && sha256Hex(existing) == ref.Sum:
			r.Present++
			continue
		case err != nil && !errors.Is(err, errImageNotFound):
			fail(ref.Path, "reading the destination: "+err.Error())
			continue
		}
		repairing := err == nil

		if err := dst.Put(ctx, ref.Path, b, mimeForPath(ref.Path)); err != nil {
			fail(ref.Path, "writing: "+err.Error())
			continue
		}
		// Read it back: a store that accepted the write is not proof it
		// kept the bytes.
		if got, err := readImage(ctx, dst, ref.Path); err != nil || sha256Hex(got) != ref.Sum {
			fail(ref.Path, "the copy does not match its recorded SHA-256 after writing")
			continue
		}
		if repairing {
			r.Repaired++
			fmt.Fprintf(out, "  replaced %s: the copy in the bucket did not match its SHA-256\n", ref.Path)
		} else {
			r.Copied++
		}
	}
	return r
}

func readImage(ctx context.Context, s ImageStore, p string) ([]byte, error) {
	img, err := s.Get(ctx, p)
	if err != nil {
		return nil, err
	}
	defer img.Close()
	return io.ReadAll(img)
}
