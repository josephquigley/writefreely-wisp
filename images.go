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

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/writeas/impart"
	"github.com/writeas/web-core/log"
)

// orphanImageHours is how long an uploaded image may go unattached to a post
// before the sweep removes it.
const orphanImageHours = 24

// uploadedImage is the JSON representation of a stored image, as returned to
// the editor.
type uploadedImage struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	Name string `json:"name"`
}

// handleUploadImage stores an image uploaded from the editor and returns the
// URL the instance serves it from.
//
// The order of operations here is deliberate: the body is bounded before it is
// parsed, the type is decided by sniffing the content rather than by trusting
// the submitted filename or Content-Type, the image is re-encoded to discard
// metadata, and the path it is stored at is derived entirely from the owner's
// ID and the hash of the stored bytes.
func handleUploadImage(app *App, u *User, w http.ResponseWriter, r *http.Request) error {
	if !app.Config().Uploads.Enabled {
		// A disabled feature shouldn't advertise itself.
		return impart.HTTPError{http.StatusNotFound, "Not found."}
	}
	if u.IsSilenced() {
		return ErrUserSilenced
	}

	maxBytes := app.Config().MaxUploadBytes()

	// Bound the body before parsing it, so an oversized upload is refused
	// without ever being buffered.
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return impart.HTTPError{http.StatusRequestEntityTooLarge, "That image is too large."}
		}
		return impart.HTTPError{http.StatusBadRequest, "Couldn't read the uploaded file."}
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		return impart.HTTPError{http.StatusBadRequest, "No file was uploaded."}
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return impart.HTTPError{http.StatusRequestEntityTooLarge, "That image is too large."}
		}
		return impart.HTTPError{http.StatusBadRequest, "Couldn't read the uploaded file."}
	}

	stored, mime, ext, err := prepareUpload(data)
	if err != nil {
		if err == errUnsupportedImageType {
			return impart.HTTPError{http.StatusUnsupportedMediaType, "That file type isn't supported."}
		}
		log.Error("Failed re-encoding uploaded image: %v", err)
		return impart.HTTPError{http.StatusInternalServerError, "Couldn't process that image."}
	}

	// Identify the image by what we actually store, not by what was sent.
	sum := sha256Hex(stored)

	// The same bytes from the same user are the image already stored, so
	// the upload resolves to it rather than to a second copy under today's
	// date.
	if existing, err := app.db.GetPostImageBySum(u.ID, sum); err == nil {
		return impart.WriteSuccess(w, uploadedImage{existing.ID, existing.URL(), existing.Filename}, http.StatusOK)
	}

	// The row is written first, because its path is what reserves the name:
	// the unique constraint settles which of two uploads racing for the same
	// name that day gets it, and the loser tries the next one.
	// The client's name for the file is kept for display. It is free text
	// from the client, so it is made storable and fitted to its column; the
	// stored path is derived from it separately and is already bounded.
	filename := boundedDBText(header.Filename, postImageMaxLengthFilename)
	img, err := app.createImageRow(u.ID, sum, filename, mime, ext, len(stored))
	if err != nil {
		return err
	}

	if err = app.writeUploadedImage(r.Context(), img.RelPath(), stored); err != nil {
		log.Error("Failed writing uploaded image: %v", err)
		if rmErr := app.db.DeletePostImage(img.ID); rmErr != nil {
			log.Error("Failed removing image row after a failed write: %v", rmErr)
		}
		if s3Unreachable(err) {
			return impart.HTTPError{http.StatusServiceUnavailable, "Image storage isn't answering right now, so that image wasn't saved. Try again in a few minutes."}
		}
		return impart.HTTPError{http.StatusInsufficientStorage, "Couldn't store that image."}
	}

	return impart.WriteSuccess(w, uploadedImage{img.ID, img.URL(), img.Filename}, http.StatusOK)
}

// handleDeleteImage takes one of the current user's images out of the editor
// it was removed from. The file and row go with it only when no post still
// references the image; otherwise they stay and the removal is local to the
// post being edited.
func handleDeleteImage(app *App, u *User, w http.ResponseWriter, r *http.Request) error {
	if !app.Config().Uploads.Enabled {
		return impart.HTTPError{http.StatusNotFound, "Not found."}
	}

	imgID := mux.Vars(r)["image"]
	img, err := app.db.GetPostImage(imgID)
	if err != nil {
		return err
	}
	if img.OwnerID != u.ID {
		// Don't confirm that someone else's image exists.
		return impart.HTTPError{http.StatusNotFound, "Image doesn't exist."}
	}

	// A post other than the one being edited may use this image too. Taking
	// it out of this post is still what was asked for, so report success and
	// leave the file and row for the posts that still reference them.
	refs, err := app.db.CountPostsReferencingImage(img.URL(), "")
	if err != nil {
		return err
	}
	if refs > 0 {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}

	if err = app.deleteImage(r.Context(), img); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

// createImageRow records an upload under the first free name for today,
// which is where its file then goes.
func (app *App) createImageRow(ownerID int64, sum, filename, mime, ext string, size int) (*PostImage, error) {
	now := time.Now()
	for n := 1; n <= imagePathAttempts; n++ {
		img, err := app.db.CreatePostImage(ownerID, sum, imagePath(filename, ext, now, n), filename, mime, size)
		if err == nil {
			return img, nil
		}
		if err != ErrImagePathTaken {
			return nil, err
		}
	}
	log.Error("Gave up finding a free path for an upload named %s", filename)
	return nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't store that image."}
}

// uploadHeaders hardens the responses served for uploaded files. This is
// defense in depth behind the type allow-list, not a substitute for it.
// uploadHeaders serves uploaded files with the headers that make hosting
// them from the instance's own origin safe.
//
// The sandbox directive is the primary control: it loads the response into
// an opaque origin with scripting disabled, so script embedded in an
// uploaded file cannot act as the signed-in user. It is enforced by the
// browser and does not depend on this server parsing the file correctly.
//
// SVG additionally serves as an attachment. Content-Disposition applies to
// navigations but not to subresources, so an SVG referenced from a post
// with <img> still renders, while opening its URL directly downloads it
// rather than loading it as a document.
func uploadHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		if strings.HasSuffix(strings.ToLower(r.URL.Path), ".svg") {
			w.Header().Set("Content-Type", svgMIME)
			w.Header().Set("Content-Disposition", "attachment")
		} else {
			w.Header().Set("Content-Disposition", "inline")
		}
		next.ServeHTTP(w, r)
	})
}

// imageURLPattern matches the URLs of images this instance hosts, so a post's
// body can be scanned for the ones it uses. It captures the stored path,
// which is what identifies the image now that the URL no longer carries its
// hash.
// The filename character class must cover everything imageSlug can emit.
// slug.Make keeps underscores, so a name like IMG_1234.jpg produced a stored
// path this pattern could not match: the upload then looked unreferenced,
// was never attached to its post, and the orphan sweep deleted it a day
// later while the post still displayed it. Dots are allowed for the same
// reason -- to fail safe if the slugifier ever stops stripping them.
var imageURLPattern = regexp.MustCompile(`/` + uploadsDir + `/([0-9]{4}/[0-9]{2}/[0-9]{2}/[a-z0-9._-]+\.[a-z0-9]+)`)

// attachPostImages records which of the user's uploaded images the given post
// body uses, so the images stop being orphans and can be cleaned up with the
// post. Failing to attach must never fail the save, so problems are logged
// rather than returned.
func attachPostImages(app *App, ownerID int64, postID, content string) {
	if !app.Config().Uploads.Enabled || postID == "" {
		return
	}

	seen := map[string]bool{}
	imageIDs := []string{}
	referenced := map[string]bool{}
	for _, m := range imageURLPattern.FindAllStringSubmatch(content, -1) {
		path := m[1]
		if seen[path] {
			continue
		}
		seen[path] = true

		img, err := app.db.GetPostImageByPath(ownerID, path)
		if err != nil {
			// Someone else's image, or one that's already gone.
			continue
		}
		imageIDs = append(imageIDs, img.ID)
		referenced[img.ID] = true
	}

	// Run even when the body has no images left, since emptying a post of
	// them is exactly when its last attachment needs releasing.
	detachUnusedPostImages(app, postID, referenced)

	if err := app.db.AttachImagesToPost(ownerID, postID, imageIDs); err != nil {
		log.Error("Unable to attach images to post %s: %v", postID, err)
	}
}

// detachUnusedPostImages releases the images a post is attached to but no
// longer references in its body, so that taking an image out of a post is as
// complete when the link is edited away by hand as when the thumbnail's
// delete control is used. An image no post references at all goes entirely.
func detachUnusedPostImages(app *App, postID string, referenced map[string]bool) {
	imgs, err := app.db.GetImagesForPost(postID)
	if err != nil {
		log.Error("Unable to get images for post %s: %v", postID, err)
		return
	}

	for _, img := range *imgs {
		if referenced[img.ID] {
			continue
		}
		if err := app.db.DetachImageFromPost(img.ID); err != nil {
			continue
		}
		removeImageIfUnreferenced(app, &img, postID)
	}
}

// cleanUpPostImages removes the images that belonged to a just-deleted post,
// keeping any that another post still references. The post is already gone by
// the time this runs, so failures are logged rather than returned.
func cleanUpPostImages(app *App, postID string) {
	if !app.Config().Uploads.Enabled {
		return
	}

	imgs, err := app.db.GetImagesForPost(postID)
	if err != nil {
		log.Error("Unable to get images for deleted post %s: %v", postID, err)
		return
	}

	for _, img := range *imgs {
		removeImageIfUnreferenced(app, &img, postID)
	}
}

// removeImageIfUnreferenced deletes an image's row and file if no post other
// than excludingPostID still has its URL in the body.
func removeImageIfUnreferenced(app *App, img *PostImage, excludingPostID string) {
	refs, err := app.db.CountPostsReferencingImage(img.URL(), excludingPostID)
	if err != nil {
		log.Error("Unable to count references to image %s: %v", img.ID, err)
		return
	}
	if refs > 0 {
		return
	}
	if err = app.deleteImage(context.Background(), img); err != nil {
		log.Error("Unable to delete image %s: %v", img.ID, err)
		// The row is still here. If it still names a post, that post is gone
		// or has let go of it, so release it: the orphan sweep only looks at
		// images attached to nothing, and would never retry this one.
		if img.PostID.Valid {
			if derr := app.db.DetachImageFromPost(img.ID); derr != nil {
				log.Error("Unable to release image %s for the orphan sweep: %v", img.ID, derr)
			}
		}
	}
}

// deleteImage removes an image's stored file and then its row, in that order.
//
// The file goes first because the two failures are not equally bad. A row
// whose file is missing is a broken image that shows up and can be retried:
// the delete is idempotent, a missing file counts as deleted, so trying again
// goes straight on to the row. A file whose row is gone is invisible and
// permanent: the orphan sweep works from rows, so nothing would ever find the
// file again. If the file cannot be removed the row is kept and the error
// returned, so the user's retry or the sweep's next tick can finish the job.
//
// No transaction is held across the store call; the row delete is one
// statement of its own.
func (app *App) deleteImage(ctx context.Context, img *PostImage) error {
	if err := app.removeUploadedImage(ctx, img.RelPath()); err != nil && !errors.Is(err, errImageNotFound) {
		log.Error("Failed removing image file %s: %v", img.RelPath(), err)
		return impart.HTTPError{http.StatusInternalServerError, "Couldn't delete the image."}
	}
	return app.db.DeletePostImage(img.ID)
}

// sweepOrphanedImages removes uploads that were never attached to a post --
// what's left behind when a draft is abandoned after an image is dropped into
// it.
func sweepOrphanedImages(app *App) {
	imgs, err := app.db.GetOrphanedImages(orphanImageHours)
	if err != nil {
		log.Error("[jobs] Unable to get orphaned images: %v", err)
		return
	}
	for _, img := range *imgs {
		removeImageIfUnreferenced(app, &img, "")
	}
}
