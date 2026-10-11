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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gosimple/slug"
	"github.com/writeas/web-core/log"
)

const (
	// uploadsDir is the directory, under the static directory, that uploaded
	// images are stored in and served from.
	uploadsDir = "uploads"

	// jpegQuality is the quality uploaded JPEGs are re-encoded at.
	jpegQuality = 90
)

// errUnsupportedImageType is returned for anything we won't store: a type
// that isn't on the allow-list, and anything that sniffs as an image but
// won't actually decode.
var errUnsupportedImageType = errors.New("unsupported image type")

// sniffImageType returns the MIME type of b, determined from its content.
// The client's Content-Type header and filename are deliberately ignored:
// neither is trustworthy, and both are attacker-controlled.
func sniffImageType(b []byte) string {
	if len(b) > 512 {
		b = b[:512]
	}
	return strings.Split(http.DetectContentType(b), ";")[0]
}

// decodeAndReencode decodes an image and re-encodes it in the same format,
// discarding all metadata. Returns the new bytes, the MIME type, and the file
// extension to store it under.
//
// Re-encoding is what strips Exif, which routinely carries GPS coordinates:
// decoding to an image.Image and encoding again keeps only the pixels, so no
// Exif library is involved. The type is decided by sniffing the content
// against allowed; SVG is not decodable here and so can never be stored.
// svgMIME is the type SVG uploads are stored and served as. Go's content
// sniffer never reports it -- an SVG comes back as text/xml or text/plain
// -- so SVG is recognised separately, by looksLikeSVG.
const svgMIME = "image/svg+xml"

// looksLikeSVG reports whether b is an SVG document, allowing for a byte
// order mark, an XML declaration, comments and a doctype before the root
// element. It is deliberately conservative: anything it does not
// positively recognise falls through to the raster path and is rejected.
func looksLikeSVG(b []byte) bool {
	head := b
	if len(head) > 1024 {
		head = head[:1024]
	}
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	lower := bytes.ToLower(bytes.TrimSpace(head))
	if bytes.HasPrefix(lower, []byte("<svg")) {
		return true
	}
	// Skip an XML declaration, doctype or comments before the root element.
	if !bytes.HasPrefix(lower, []byte("<?xml")) && !bytes.HasPrefix(lower, []byte("<!")) {
		return false
	}
	return bytes.Contains(lower, []byte("<svg"))
}

// extForMIME returns the file extension an accepted upload is stored
// under. It is the single source of truth for that mapping: the stored
// path and the public URL are built independently, and when they disagreed
// the URL pointed at a file that was never written.
func extForMIME(mime string) string {
	switch mime {
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case svgMIME:
		return "svg"
	}
	return "png"
}

// prepareUpload validates an uploaded file and returns the bytes to store,
// its MIME type and the extension to store it under.
//
// Raster images are decoded and re-encoded, which discards every byte that
// is not pixel data -- metadata, trailing payloads, anything hostile.
// SVG cannot be treated that way: it is a document, not a bitmap, and Go
// has no rasteriser. Its bytes are therefore stored verbatim, and the
// safety of serving them is enforced at request time instead. See
// uploadHeaders.
func prepareUpload(b []byte) ([]byte, string, string, error) {
	if looksLikeSVG(b) {
		return b, svgMIME, extForMIME(svgMIME), nil
	}

	mime := sniffImageType(b)

	var buf bytes.Buffer
	switch mime {
	case "image/png":
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			return nil, "", "", errUnsupportedImageType
		}
		if err = png.Encode(&buf, img); err != nil {
			return nil, "", "", err
		}
		return buf.Bytes(), mime, extForMIME(mime), nil
	case "image/jpeg":
		img, err := jpeg.Decode(bytes.NewReader(b))
		if err != nil {
			return nil, "", "", errUnsupportedImageType
		}
		if err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return nil, "", "", err
		}
		return buf.Bytes(), mime, extForMIME(mime), nil
	case "image/gif":
		// DecodeAll, not Decode: the latter silently reduces an animated
		// GIF to its first frame.
		g, err := gif.DecodeAll(bytes.NewReader(b))
		if err != nil {
			return nil, "", "", errUnsupportedImageType
		}
		if err = gif.EncodeAll(&buf, g); err != nil {
			return nil, "", "", err
		}
		return buf.Bytes(), mime, extForMIME(mime), nil
	}
	return nil, "", "", errUnsupportedImageType
}

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// imageDirFormat groups uploads by the day they arrived, which keeps any one
// directory small without the path having to encode anything.
const imageDirFormat = "2006/01/02"

// imageSlugMaxLen bounds the name part of a stored path, so a long filename
// cannot push the whole path past what the column and the filesystem hold.
const imageSlugMaxLen = 60

// imagePathAttempts bounds how many names are tried before an upload gives up
// on finding a free one for its day.
const imagePathAttempts = 50

// imagePath returns the storage path for an image, relative to the uploads
// root: the day it arrived, then the name the writer gave it.
//
// The directory and the extension are server-derived. The name is not, so it
// is reduced to a slug, which leaves lowercase ASCII words and hyphens and
// nothing that could climb out of the directory it lands in. n disambiguates
// a name already taken that day.
func imagePath(filename, ext string, at time.Time, n int) string {
	base := imageSlug(filename)
	if n > 1 {
		base += "-" + strconv.Itoa(n)
	}
	return at.UTC().Format(imageDirFormat) + "/" + base + "." + ext
}

// imageSlug reduces a client-supplied filename to the name part of a stored
// path, using the slugifier post URLs already go through.
func imageSlug(filename string) string {
	name := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	s := slug.Make(name)
	if len(s) > imageSlugMaxLen {
		s = strings.Trim(s[:imageSlugMaxLen], "-")
	}
	if s == "" {
		// Nothing usable survived, or there was no name to begin with.
		return "image"
	}
	return s
}

// uploadsRoot returns the directory uploaded images are written to.
func (app *App) uploadsRoot() string {
	if dir := strings.TrimSpace(app.Config().Uploads.Dir); dir != "" {
		return dir
	}
	return filepath.Join(app.Config().Server.StaticParentDir, staticDir, uploadsDir)
}

// ImageStore keeps the bytes of uploaded images. Every path is relative to
// the uploads root and is what post_images.path holds, so the same path names
// the same image in every store, and /uploads/<path> is its URL in post
// bodies whichever store holds it.
//
// Nothing here reads an image back to serve it. The local store is served
// from its directory by http.FileServer, and an S3 bucket by whatever answers
// at [storage] image_url_base; see uploadsHandlerFor.
type ImageStore interface {
	// Put stores b at path, replacing anything already there.
	Put(ctx context.Context, path string, b []byte, mime string) error
	// Delete removes the image at path. One that is already gone is not an
	// error.
	Delete(ctx context.Context, path string) error
	// Exists reports whether there is an image at path.
	Exists(ctx context.Context, path string) (bool, error)
	// Probe checks that the store can be written to, so that a store that
	// cannot is reported at startup rather than at the first upload.
	Probe(ctx context.Context) error
}

var errImageNotFound = errors.New("image not found")

// imageStore returns the store uploads go to, choosing it from [storage]
// the first time it is asked for.
func (app *App) imageStore() ImageStore {
	if err := app.initImageStore(); err != nil {
		// Initialize has already refused to start on this error, so only
		// an App built some other way gets here. Falling back to the
		// local directory would put images where no other node can see
		// them, so every call fails instead.
		return brokenImageStore{err}
	}
	return app.images
}

// initImageStore picks the image store from the configuration, once. With no
// [storage] section, or type = local, it is the directory images have always
// been written to.
func (app *App) initImageStore() error {
	app.imagesOnce.Do(func() {
		if !app.Config().Storage.UsesS3() {
			app.images = &localImageStore{root: app.uploadsRoot}
			return
		}
		app.images, app.imagesErr = newS3ImageStore(app.Config().Storage)
	})
	return app.imagesErr
}

// brokenImageStore stands in for a store that could not be set up.
type brokenImageStore struct{ err error }

func (b brokenImageStore) Put(context.Context, string, []byte, string) error { return b.err }
func (b brokenImageStore) Delete(context.Context, string) error              { return b.err }
func (b brokenImageStore) Exists(context.Context, string) (bool, error)      { return false, b.err }
func (b brokenImageStore) Probe(context.Context) error                       { return b.err }

// writeUploadedImage stores b at the given uploads-relative path.
// ctx is the request's, so a writer who goes away stops the write; the store
// bounds it either way.
func (app *App) writeUploadedImage(ctx context.Context, relPath string, b []byte) error {
	return app.imageStore().Put(ctx, relPath, b, mimeForPath(relPath))
}

// removeUploadedImage deletes the image at the given uploads-relative path.
// One that is already gone is not an error.
func (app *App) removeUploadedImage(ctx context.Context, relPath string) error {
	return app.imageStore().Delete(ctx, relPath)
}

// ensureUploadsWritable verifies the image store can be written to.
//
// Without this an instance starts happily and only fails when a writer
// first drags in an image, with a 507 and a log line nobody is watching.
// The two ways it goes wrong in practice -- a container image whose
// uploads directory is owned by root while the process runs unprivileged,
// and a deployment that forgot to mount a volume for it -- are both
// present from the moment the process starts, so they are worth reporting
// then. With S3 the same holds for a wrong endpoint, bucket or key.
func (app *App) ensureUploadsWritable() error {
	return app.imageStore().Probe(context.Background())
}

// checkUploadsAtStartup is the startup check of the image store. It returns an
// error only when the instance should refuse to start: a local directory it
// cannot write to, or an S3 store that answered and said no (a missing bucket,
// a refused key), both of which are configuration. An S3 store that does not
// answer at all is an outage, and images are all it takes down, so it is
// logged and startup goes on; the store is used again on the next request.
func (app *App) checkUploadsAtStartup() error {
	err := app.ensureUploadsWritable()
	if err == nil {
		return nil
	}
	if s3Unreachable(err) {
		st := app.Config().Storage
		log.Error("image uploads are unavailable: S3 at %s/%s did not answer: %v; the blog is starting without them", st.S3Endpoint, st.S3Bucket, err)
		return nil
	}
	return err
}

// mimeForPath returns the type an image is served as, from its extension.
// Paths are server-derived (see imagePath), so the extension is one this
// server chose for the bytes it stored.
func mimeForPath(relPath string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(relPath), "."))
	for _, m := range []string{"image/png", "image/jpeg", "image/gif", svgMIME} {
		if extForMIME(m) == ext {
			return m
		}
	}
	if ext == "jpeg" {
		return "image/jpeg"
	}
	return "application/octet-stream"
}

// cleanImagePath turns the path of a request under /uploads/ into a store
// path, or reports that it cannot name an image. The local store has
// http.Dir to refuse a path that climbs out of its root; an object store has
// nothing like it, so this is that refusal.
func cleanImagePath(p string) (string, bool) {
	p = strings.TrimPrefix(p, "/")
	if p == "" || strings.HasSuffix(p, "/") || strings.Contains(p, "\\") {
		return "", false
	}
	if c := path.Clean("/" + p); c != "/"+p {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	return p, true
}

// uploadsHandler serves /uploads/<path>. The URL is the same whichever store
// holds the image, because post bodies and the copies remote servers have
// cached carry it. The local store is served by http.FileServer, exactly as
// before S3 existed. An S3 bucket is never read back by this app: /uploads/
// redirects to the image under [storage] image_url_base, which is where
// rendered and federated copies already point; see redirectImages.
func (app *App) uploadsHandler() http.Handler {
	return uploadsHandlerFor(app.imageStore(), newImageURLs(app.Config()))
}

// uploadsHandlerFor is uploadsHandler for a given store. Cache-Control is
// set only on a response that carries an image: http.FileServer's errors
// strip it, so a missing file is not kept for a week, immutable.
//
// Which branch runs follows the store, not the setting, so an S3 store is
// never streamed. Without a base, which a loaded configuration cannot have
// with S3, there is nowhere to send readers, and every request is a 404.
func uploadsHandlerFor(store ImageStore, urls imageURLs) http.Handler {
	var h http.Handler
	switch ls, isLocal := store.(*localImageStore); {
	case isLocal:
		h = cacheControl(http.FileServer(http.Dir(ls.root())))
	case urls.on():
		h = redirectImages(urls)
	default:
		h = http.NotFoundHandler()
	}
	return uploadHeaders(http.StripPrefix("/"+uploadsDir+"/", h))
}

// imageRedirectCacheControl is how long the redirect from /uploads/ to
// image_url_base may be kept. Unlike the image behind it, it is not
// immutable: it says where the images are, which the operator may change,
// and a week-long immutable redirect would keep sending readers to the old
// place long after. A few minutes still lets a cache in front of this app
// absorb a burst, such as a post's images being fetched by every server
// that just received it.
const imageRedirectCacheControl = "public, max-age=300"

// redirectImages answers /uploads/<path> with a redirect to the image's URL
// under image_url_base. Rendered pages and federated copies already use the
// base, so this serves what still carries /uploads/: copies of posts that
// left while their images were on local disk, before they were moved with
// `writefreely images sync`, and anything else that kept a body's own URL.
//
// The store is not asked whether the image exists. That is the point: the
// redirect costs nothing, and a missing image is the base's 404 to give.
// The path is still checked with cleanImagePath, so a request that could
// not name an image is refused here, not passed along.
func redirectImages(urls imageURLs) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		p, ok := cleanImagePath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		// uploadHeaders typed the response as the image it names; a
		// redirect carries no image, and no body at all.
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Disposition")
		w.Header().Set("Cache-Control", imageRedirectCacheControl)
		w.Header().Set("Location", urls.forPath(p))
		w.WriteHeader(http.StatusFound)
	})
}

// localImageStore keeps images in a directory on this node. root is a func
// because the directory comes from the configuration, which a test may change
// after the store is made.
type localImageStore struct {
	root func() string
}

func (s *localImageStore) full(relPath string) string {
	return filepath.Join(s.root(), filepath.FromSlash(relPath))
}

func (s *localImageStore) Put(_ context.Context, relPath string, b []byte, _ string) error {
	full := s.full(relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	return os.WriteFile(full, b, 0644)
}

// ReadAll returns the bytes of the image at relPath, or errImageNotFound.
func (s *localImageStore) ReadAll(_ context.Context, relPath string) ([]byte, error) {
	fi, err := os.Stat(s.full(relPath))
	if os.IsNotExist(err) || (err == nil && fi.IsDir()) {
		return nil, errImageNotFound
	}
	if err != nil {
		return nil, err
	}
	return os.ReadFile(s.full(relPath))
}

func (s *localImageStore) Delete(_ context.Context, relPath string) error {
	err := os.Remove(s.full(relPath))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *localImageStore) Exists(_ context.Context, relPath string) (bool, error) {
	fi, err := os.Stat(s.full(relPath))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !fi.IsDir(), nil
}

// Probe verifies the uploads directory exists and can be written to,
// creating it if necessary.
func (s *localImageStore) Probe(_ context.Context) error {
	root := s.root()
	if err := os.MkdirAll(root, 0755); err != nil {
		return fmt.Errorf("uploads directory %s cannot be created: %s", root, err)
	}

	probe := filepath.Join(root, ".writable")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("uploads directory %s is not writable: %s", root, err)
	}
	f.Close()
	os.Remove(probe)
	return nil
}
