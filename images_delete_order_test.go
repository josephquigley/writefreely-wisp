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
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingDeleteStore wraps a real store and makes Delete return deleteErr
// while it is set, to stand in for an object store that is down.
type failingDeleteStore struct {
	ImageStore
	deleteErr error
	deletes   int
}

func (s *failingDeleteStore) Delete(ctx context.Context, path string) error {
	s.deletes++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.ImageStore.Delete(ctx, path)
}

// withFailingDeletes swaps the app's image store for one whose Delete fails.
func withFailingDeletes(app *App, err error) *failingDeleteStore {
	real := app.imageStore()
	fs := &failingDeleteStore{ImageStore: real, deleteErr: err}
	app.images = fs
	return fs
}

func uploadTestImage(t *testing.T, app *App, u *User, name string) string {
	t.Helper()
	rec, status := doUpload(t, app, u, uploadRequest(t, name, "image/png", tinyPNG(t)))
	require.Equal(t, http.StatusOK, status, rec.Body.String())
	id, _ := uploadedURL(t, rec)
	return id
}

func TestImageDeleteKeepsRowWhenStoreFails(t *testing.T) {
	app, _, u, _ := newImageTestApp(t)
	id := uploadTestImage(t, app, u, "a.png")
	fs := withFailingDeletes(app, errors.New("s3 is down"))

	_, status := doDelete(t, app, u, id)
	assert.Equal(t, http.StatusInternalServerError, status, "the failure must be surfaced")
	_, err := app.db.GetPostImage(id)
	assert.NoError(t, err, "the row stays so the delete can be retried")
	assert.Equal(t, 1, countUploadedFiles(t, app))

	// The store comes back and the same delete is simply tried again.
	fs.deleteErr = nil
	_, status = doDelete(t, app, u, id)
	assert.Equal(t, http.StatusNoContent, status)
	_, err = app.db.GetPostImage(id)
	assert.Error(t, err)
	assert.Equal(t, 0, countUploadedFiles(t, app))
}

func TestImageDeleteTreatsMissingObjectAsDeleted(t *testing.T) {
	app, _, u, _ := newImageTestApp(t)
	id := uploadTestImage(t, app, u, "a.png")
	withFailingDeletes(app, errImageNotFound)

	_, status := doDelete(t, app, u, id)
	assert.Equal(t, http.StatusNoContent, status)
	_, err := app.db.GetPostImage(id)
	assert.Error(t, err, "an object that is already gone must not keep the row alive")
}

func TestImageDeleteRetrySucceedsAfterObjectAlreadyGone(t *testing.T) {
	// The reverse failure: the object went but the row delete did not. The
	// retry finds no object and must still remove the row.
	app, _, u, _ := newImageTestApp(t)
	id := uploadTestImage(t, app, u, "a.png")
	img, err := app.db.GetPostImage(id)
	require.NoError(t, err)
	require.NoError(t, app.removeUploadedImage(img.RelPath()))
	require.Equal(t, 0, countUploadedFiles(t, app))

	_, status := doDelete(t, app, u, id)
	assert.Equal(t, http.StatusNoContent, status)
	_, err = app.db.GetPostImage(id)
	assert.Error(t, err)
}

func TestOrphanSweepKeepsRowWhenStoreFails(t *testing.T) {
	app, _, u, _ := newImageTestApp(t)
	id := uploadTestImage(t, app, u, "old.png")
	ageImage(t, app, id, 25)
	fs := withFailingDeletes(app, errors.New("s3 is down"))

	sweepOrphanedImages(app)
	_, err := app.db.GetPostImage(id)
	assert.NoError(t, err, "the row must survive so the next tick finds the orphan again")
	assert.Equal(t, 1, countUploadedFiles(t, app))

	fs.deleteErr = nil
	sweepOrphanedImages(app)
	_, err = app.db.GetPostImage(id)
	assert.Error(t, err, "the next tick converges")
	assert.Equal(t, 0, countUploadedFiles(t, app))
}

func TestDeletingPostWhoseImageDeleteFailsLeavesItForTheSweep(t *testing.T) {
	app, _, u, _ := newImageTestApp(t)
	_, _, post := createTemplateTestUser(t, app, "poster")
	id := uploadTestImage(t, app, u, "a.png")
	require.NoError(t, app.db.AttachImagesToPost(u.ID, post.ID, []string{id}))
	fs := withFailingDeletes(app, errors.New("s3 is down"))

	cleanUpPostImages(app, post.ID)
	img, err := app.db.GetPostImage(id)
	require.NoError(t, err, "row kept after a failed object delete")
	assert.False(t, img.PostID.Valid, "released from the deleted post so the orphan sweep can find it")

	fs.deleteErr = nil
	ageImage(t, app, id, 25)
	sweepOrphanedImages(app)
	_, err = app.db.GetPostImage(id)
	assert.Error(t, err)
	assert.Equal(t, 0, countUploadedFiles(t, app))
}
