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
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/writeas/impart"
	"github.com/writeas/web-core/id"
	"github.com/writeas/web-core/log"
)

// imageIDChars is the alphabet short image IDs are generated from, matching
// the scheme used for user invites.
const imageIDChars = "0123456789BCDFGHJKLMNPQRSTVWXYZbcdfghjklmnpqrstvwxyz"

// PostImage is an image uploaded by a user for use in a post.
type PostImage struct {
	ID       string
	OwnerID  int64
	PostID   sql.NullString
	Sum      string
	Path     string
	Filename string
	Mime     string
	Size     int
	Created  time.Time
}

// Ext returns the file extension the image is stored under, derived from its
// sniffed MIME type rather than from anything the client sent.
func (i *PostImage) Ext() string {
	return extForMIME(i.Mime)
}

// RelPath returns the image's path relative to the uploads directory. It is
// stored rather than derived, since a path naming the day and the file cannot
// be recomputed from the image's contents the way a hashed one could.
func (i *PostImage) RelPath() string {
	return i.Path
}

// URL returns the public path at which the image is served.
func (i *PostImage) URL() string {
	return "/" + uploadsDir + "/" + i.RelPath()
}

// ErrImagePathTaken reports that an upload's chosen storage path was claimed
// by another upload first. It is not returned to the client: the handler
// answers it by trying the next name.
var ErrImagePathTaken = errors.New("image path already taken")

// CreatePostImage records an uploaded image. Identical bytes uploaded again by
// the same user return the existing row instead of creating a second one, so a
// duplicate upload is a success rather than a conflict.
func (db *datastore) CreatePostImage(ownerID int64, sum, path, filename, mime string, size int) (*PostImage, error) {
	if existing, err := db.GetPostImageBySum(ownerID, sum); err == nil {
		return existing, nil
	}

	imgID := id.GenerateRandomString(imageIDChars, 6)
	_, err := db.Exec("INSERT INTO post_images (id, owner_id, post_id, sha256, path, filename, mime, size, created) VALUES (?, ?, NULL, ?, ?, ?, ?, ?, "+db.now()+")",
		imgID, ownerID, sum, path, filename, mime, size)
	if err != nil {
		if db.isDuplicateKeyErr(err) {
			// Two constraints can produce this: the same bytes, which is a
			// duplicate upload and a success, or the same path, which means
			// another upload claimed the name first and the caller should
			// try the next one.
			if existing, sumErr := db.GetPostImageBySum(ownerID, sum); sumErr == nil {
				return existing, nil
			}
			return nil, ErrImagePathTaken
		}
		log.Error("Failed inserting into post_images: %v", err)
		return nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't save the uploaded image."}
	}

	return db.GetPostImage(imgID)
}

// GetPostImage returns the image with the given ID.
func (db *datastore) GetPostImage(imgID string) (*PostImage, error) {
	return db.getPostImageBy("id = ?", imgID)
}

// GetPostImageBySum returns the given user's image with the given SHA-256 sum.
func (db *datastore) GetPostImageBySum(ownerID int64, sum string) (*PostImage, error) {
	return db.getPostImageBy("owner_id = ? AND sha256 = ?", ownerID, sum)
}

// GetPostImageByPath returns the given user's image stored at the given
// uploads-relative path.
func (db *datastore) GetPostImageByPath(ownerID int64, path string) (*PostImage, error) {
	return db.getPostImageBy("owner_id = ? AND path = ?", ownerID, path)
}

func (db *datastore) getPostImageBy(where string, args ...interface{}) (*PostImage, error) {
	var i PostImage
	err := db.QueryRow("SELECT id, owner_id, post_id, sha256, path, filename, mime, size, created FROM post_images WHERE "+where, args...).
		Scan(&i.ID, &i.OwnerID, &i.PostID, &i.Sum, &i.Path, &i.Filename, &i.Mime, &i.Size, &i.Created)
	switch {
	case err == sql.ErrNoRows:
		return nil, impart.HTTPError{http.StatusNotFound, "Image doesn't exist."}
	case err != nil:
		log.Error("Failed selecting from post_images: %v", err)
		return nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't retrieve the image."}
	}
	return &i, nil
}

// GetImagesForPost returns the images attached to the given post.
func (db *datastore) GetImagesForPost(postID string) (*[]PostImage, error) {
	rows, err := db.Query("SELECT id, owner_id, post_id, sha256, path, filename, mime, size, created FROM post_images WHERE post_id = ? ORDER BY created ASC", postID)
	if err != nil {
		log.Error("Failed selecting post images: %v", err)
		return nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't retrieve the post's images."}
	}
	defer rows.Close()

	imgs := []PostImage{}
	for rows.Next() {
		var i PostImage
		if err = rows.Scan(&i.ID, &i.OwnerID, &i.PostID, &i.Sum, &i.Path, &i.Filename, &i.Mime, &i.Size, &i.Created); err != nil {
			log.Error("Failed scanning post image: %v", err)
			return nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't retrieve the post's images."}
		}
		imgs = append(imgs, i)
	}
	return &imgs, nil
}

// AttachImagesToPost records that the given images belong to the given post.
// Only the owner's own images are affected.
func (db *datastore) AttachImagesToPost(ownerID int64, postID string, imageIDs []string) error {
	if len(imageIDs) == 0 {
		return nil
	}

	args := []interface{}{postID, ownerID}
	placeholders := make([]string, 0, len(imageIDs))
	for _, imgID := range imageIDs {
		placeholders = append(placeholders, "?")
		args = append(args, imgID)
	}

	_, err := db.Exec("UPDATE post_images SET post_id = ? WHERE owner_id = ? AND id IN ("+strings.Join(placeholders, ", ")+")", args...)
	if err != nil {
		log.Error("Failed attaching images to post: %v", err)
		return impart.HTTPError{http.StatusInternalServerError, "Couldn't attach images to the post."}
	}
	return nil
}

// DetachImageFromPost releases an image from the post it was attached to. The
// row stays behind so a shared image keeps working for the posts that still
// use it, and so the orphan sweep can collect it if none do.
func (db *datastore) DetachImageFromPost(imgID string) error {
	_, err := db.Exec("UPDATE post_images SET post_id = NULL WHERE id = ?", imgID)
	if err != nil {
		log.Error("Failed detaching post image: %v", err)
		return impart.HTTPError{http.StatusInternalServerError, "Couldn't detach the image."}
	}
	return nil
}

// DeletePostImage removes the given image's row. The file itself is removed
// before the row, and only once nothing references it; see deleteImage.
func (db *datastore) DeletePostImage(imgID string) error {
	_, err := db.Exec("DELETE FROM post_images WHERE id = ?", imgID)
	if err != nil {
		log.Error("Failed deleting post image: %v", err)
		return impart.HTTPError{http.StatusInternalServerError, "Couldn't delete the image."}
	}
	return nil
}

// CountPostsReferencingImage returns how many posts contain the given image
// URL in their body, ignoring the post with the given ID (pass an empty string
// to ignore none).
//
// This is a LIKE scan over posts.content, which is not fast. It runs only when
// something is being deleted, and the markdown body is the actual source of
// truth for what a post references, so a faster but less honest check would be
// the wrong trade.
func (db *datastore) CountPostsReferencingImage(url, excludingPostID string) (int, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM posts WHERE content LIKE ? AND (? = '' OR id != ?)",
		"%"+url+"%", excludingPostID, excludingPostID).Scan(&count)
	if err != nil {
		log.Error("Failed counting posts referencing image: %v", err)
		return 0, impart.HTTPError{http.StatusInternalServerError, "Couldn't check image references."}
	}
	return count, nil
}

// GetOrphanedImages returns images that were uploaded more than the given
// number of hours ago and never attached to a post -- uploads from drafts that
// were abandoned.
func (db *datastore) GetOrphanedImages(olderThanHours int) (*[]PostImage, error) {
	rows, err := db.Query("SELECT id, owner_id, post_id, sha256, path, filename, mime, size, created FROM post_images WHERE post_id IS NULL AND created < " + db.dateAdd(-olderThanHours, "HOUR"))
	if err != nil {
		log.Error("Failed selecting orphaned images: %v", err)
		return nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't retrieve orphaned images."}
	}
	defer rows.Close()

	imgs := []PostImage{}
	for rows.Next() {
		var i PostImage
		if err = rows.Scan(&i.ID, &i.OwnerID, &i.PostID, &i.Sum, &i.Path, &i.Filename, &i.Mime, &i.Size, &i.Created); err != nil {
			log.Error("Failed scanning orphaned image: %v", err)
			return nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't retrieve orphaned images."}
		}
		imgs = append(imgs, i)
	}
	return &imgs, nil
}
