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
	"html/template"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/writefreely/writefreely/config"
)

// imageURLBaseConfig is a configuration with images in S3 under prefix and
// served from base, on the instance at https://quigs.blog.
func imageURLBaseConfig(base, prefix string) *config.Config {
	cfg := config.New()
	cfg.App.Host = "https://quigs.blog"
	cfg.App.SingleUser = true
	cfg.Storage = config.StorageCfg{
		Type:         config.StorageS3,
		S3Endpoint:   "http://garage:3900",
		S3Bucket:     "blog",
		S3Prefix:     prefix,
		ImageURLBase: base,
	}
	return cfg
}

func TestImageURLForPathIncludesTheKeyPrefix(t *testing.T) {
	u := newImageURLs(imageURLBaseConfig("https://media.quigs.blog", ""))
	assert.Equal(t, "https://media.quigs.blog/2026/09/02/pic.png", u.forPath("2026/09/02/pic.png"))

	// The path after the base is the object's key, so the URL and the
	// object cannot drift apart.
	cfg := imageURLBaseConfig("https://media.quigs.blog", "blog/uploads")
	u = newImageURLs(cfg)
	store, err := newS3ImageStore(cfg.Storage)
	require.NoError(t, err)
	assert.Equal(t, "https://media.quigs.blog/"+store.key("2026/09/02/pic.png"), u.forPath("2026/09/02/pic.png"))
	assert.Equal(t, "https://media.quigs.blog/blog/uploads/2026/09/02/pic.png", u.forPath("2026/09/02/pic.png"))
}

func TestRewriteImageURL(t *testing.T) {
	u := newImageURLs(imageURLBaseConfig("https://media.quigs.blog", "up"))
	for in, want := range map[string]string{
		"/uploads/2026/09/02/pic.png":                    "https://media.quigs.blog/up/2026/09/02/pic.png",
		"https://quigs.blog/uploads/2026/09/02/pic.png":  "https://media.quigs.blog/up/2026/09/02/pic.png",
		"http://QUIGS.blog/uploads/2026/09/02/IMG_1.jpg": "http://QUIGS.blog/uploads/2026/09/02/IMG_1.jpg", // not the grammar: upper case
		"//quigs.blog/uploads/2026/09/02/a_b-c.d.gif":    "https://media.quigs.blog/up/2026/09/02/a_b-c.d.gif",
		" /uploads/2026/09/02/pic.png ":                  "https://media.quigs.blog/up/2026/09/02/pic.png",
		"https://QUIGS.blog/uploads/2026/09/02/pic.png":  "https://media.quigs.blog/up/2026/09/02/pic.png",

		// Not ours, or not an upload: left exactly as written.
		"https://other.example/uploads/2026/09/02/pic.png": "https://other.example/uploads/2026/09/02/pic.png",
		"/uploads/../2026/09/02/pic.png":                   "/uploads/../2026/09/02/pic.png",
		"/uploads/2026/9/2/pic.png":                        "/uploads/2026/9/2/pic.png",
		"/uploads/2026/09/02/pic.png?w=10":                 "/uploads/2026/09/02/pic.png?w=10",
		"/uploads/2026/09/02/pic.png#x":                    "/uploads/2026/09/02/pic.png#x",
		"/static/uploads/2026/09/02/pic.png":               "/static/uploads/2026/09/02/pic.png",
		"uploads/2026/09/02/pic.png":                       "uploads/2026/09/02/pic.png",
		"ftp://quigs.blog/uploads/2026/09/02/pic.png":      "ftp://quigs.blog/uploads/2026/09/02/pic.png",
		"https://quigs.blog/about":                         "https://quigs.blog/about",
		"":                                                 "",
	} {
		assert.Equal(t, want, u.rewriteURL(in), in)
	}
}

// Every path imageURLPattern accepts is rewritten, and nothing it rejects is,
// since the orphan sweep and the rewrite must agree on which URLs are images.
func TestRewriteImageURLMatchesImageURLPattern(t *testing.T) {
	u := newImageURLs(imageURLBaseConfig("/media", ""))
	for _, p := range []string{
		"/uploads/2026/09/02/pic.png",
		"/uploads/2026/09/02/img_1234.jpg",
		"/uploads/2026/09/02/a.b.svg",
		"/uploads/2026/09/02/Pic.png",
		"/uploads/26/09/02/pic.png",
		"/uploads/2026/09/02/pic",
		"/uploads/2026/09/02/sub/pic.png",
	} {
		matched := imageURLPattern.FindString(p) == p
		rewritten := u.rewriteURL(p) != p
		assert.Equal(t, matched, rewritten, p)
	}
}

func TestRewriteImageURLsInHTML(t *testing.T) {
	u := newImageURLs(imageURLBaseConfig("https://media.quigs.blog", ""))
	in := `<p>See /uploads/2026/09/02/pic.png and <code>src="/uploads/2026/09/02/pic.png"</code></p>` +
		`<p><a href="https://quigs.blog/uploads/2026/09/02/pic.png"><img src="/uploads/2026/09/02/pic.png" alt="/uploads/2026/09/02/pic.png"></a></p>` +
		`<p><img src="https://other.example/uploads/2026/09/02/pic.png"></p>`
	got := u.rewriteHTML(in)

	assert.Contains(t, got, `<a href="https://media.quigs.blog/2026/09/02/pic.png">`)
	assert.Contains(t, got, `<img src="https://media.quigs.blog/2026/09/02/pic.png" alt="/uploads/2026/09/02/pic.png"/>`)
	assert.Contains(t, got, `<p>See /uploads/2026/09/02/pic.png and`, "text is not touched")
	assert.Contains(t, got, `<code>src=&#34;/uploads/2026/09/02/pic.png&#34;</code>`, "code is text, not an attribute")
	assert.Contains(t, got, `src="https://other.example/uploads/2026/09/02/pic.png"`, "another host's uploads are not ours")
}

func TestRewriteImageURLsLeavesHTMLAloneWithoutWork(t *testing.T) {
	in := `<p><img src="/uploads/2026/09/02/pic.png"><br></p>`

	// No base: byte for byte what it was, everywhere.
	off := newImageURLs(config.New())
	assert.False(t, off.on())
	assert.Equal(t, in, off.rewriteHTML(in))
	assert.Equal(t, "/uploads/2026/09/02/pic.png", off.rewriteURL("/uploads/2026/09/02/pic.png"))
	assert.False(t, newImageURLs(nil).on())

	// A base, but nothing to rewrite: not even re-serialised.
	on := newImageURLs(imageURLBaseConfig("/media", ""))
	plain := `<p>Just words<br></p>`
	assert.Equal(t, plain, on.rewriteHTML(plain))
	other := `<p><img src="https://other.example/uploads/2026/09/02/pic.png"><br></p>`
	assert.Equal(t, other, on.rewriteHTML(other))
}

// The rewrite sits in applyMarkdownSpecial, after sanitising, so every page
// that renders a post body gets it.
func TestRenderedMarkdownUsesImageURLBase(t *testing.T) {
	md := []byte("![pic](/uploads/2026/09/02/pic.png)\n\n[full size](https://quigs.blog/uploads/2026/09/02/pic.png)\n")

	off := imageURLBaseConfig("", "")
	got := applyMarkdown(md, "", off)
	assert.Contains(t, got, `src="/uploads/2026/09/02/pic.png"`)
	assert.Contains(t, got, `href="https://quigs.blog/uploads/2026/09/02/pic.png"`)

	on := imageURLBaseConfig("https://media.quigs.blog", "blog")
	got = applyMarkdown(md, "https://quigs.blog/", on)
	assert.Contains(t, got, `src="https://media.quigs.blog/blog/2026/09/02/pic.png"`)
	assert.Contains(t, got, `href="https://media.quigs.blog/blog/2026/09/02/pic.png"`)
	assert.NotContains(t, got, "/uploads/")

	// The post page, the blog index and the excerpt all come from
	// formatContent.
	_, p := syndicatedPost("https://quigs.blog")
	p.HTMLContent = ""
	p.Content = "Intro.<!--more-->\n\n![pic](/uploads/2026/09/02/pic.png)\n"
	p.formatContent(on, false, true)
	assert.Contains(t, string(p.HTMLContent), `src="https://media.quigs.blog/blog/2026/09/02/pic.png"`)
}

func TestActivityObjectUsesImageURLBase(t *testing.T) {
	// A .com host: the URL extractor behind p.Images does not know .blog.
	const host = "https://blog.example.com"
	for _, c := range []struct {
		name, base, want string
	}{
		{"no base", "", host + "/uploads/2026/09/02/pic.png"},
		{"absolute base", "https://media.example.com", "https://media.example.com/2026/09/02/pic.png"},
		{"path base", "/media", host + "/media/2026/09/02/pic.png"},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, p := syndicatedPost(host)
			app.cfg = imageURLBaseConfig(c.base, "")
			app.cfg.App.Host = host
			// Rendered the way the database layer renders it.
			p.HTMLContent = ""
			// A bare link to the image too, which reaches the attachment
			// list through p.Images rather than an <img>.
			p.Content += "\nFull size: " + host + "/uploads/2026/09/02/pic.png\n"
			p.extractImages()
			require.Equal(t, []string{host + "/uploads/2026/09/02/pic.png"}, p.Images)
			p.formatContent(app.cfg, false, false)

			o := p.ActivityObject(app)
			assert.Contains(t, o.Content, `src="`+c.want+`"`)
			require.Len(t, o.Attachment, 1, "one image, one attachment: %+v", o.Attachment)
			assert.Equal(t, c.want, o.Attachment[0].URL)
			assert.Equal(t, "pic", o.Attachment[0].Name)
		})
	}
}

func TestImageAttachmentsRewriteExtractedImages(t *testing.T) {
	u := newImageURLs(imageURLBaseConfig("https://media.quigs.blog", ""))
	extracted := []string{"https://quigs.blog/uploads/2026/09/02/pic.png", "https://example.com/c.jpg"}
	alts := map[string]string{"https://quigs.blog/uploads/2026/09/02/pic.png": "a pic"}

	got := imageAttachments("", extracted, alts, attachBase, u)
	require.Len(t, got, 2)
	assert.Equal(t, "https://media.quigs.blog/2026/09/02/pic.png", got[0].URL)
	assert.Equal(t, "a pic", got[0].Name)
	assert.Equal(t, "https://example.com/c.jpg", got[1].URL)
}

func TestSyndicatedContentKeepsBaseAbsolute(t *testing.T) {
	_, p := syndicatedPost("https://quigs.blog")
	p.HTMLContent = template.HTML(applyMarkdown([]byte("![pic](/uploads/2026/09/02/pic.png)"), "", imageURLBaseConfig("https://media.quigs.blog", "")))
	got := syndicatedContent(p, "https://quigs.blog/rel-img-test")
	assert.Contains(t, got, `src="https://media.quigs.blog/2026/09/02/pic.png"`)
}

func TestUploadsRedirectToImageURLBase(t *testing.T) {
	store := &fakeImageStore{objects: map[string][]byte{"2026/01/01/a.png": tinyPNG(t)}}
	u := newImageURLs(imageURLBaseConfig("https://media.quigs.blog", "blog"))
	h := uploadsHandlerFor(store, u)

	for _, method := range []string{"GET", "HEAD"} {
		rec := serveUploads(h, method, "/uploads/2026/01/01/a.png")
		assert.Equal(t, http.StatusFound, rec.Code, method)
		assert.Equal(t, "https://media.quigs.blog/blog/2026/01/01/a.png", rec.Header().Get("Location"), method)
		assert.NotContains(t, rec.Header().Get("Cache-Control"), "immutable", method)
		assert.NotEmpty(t, rec.Header().Get("Cache-Control"), method)
		assert.Empty(t, rec.Body.String(), method)
	}

	// The store is never asked: the redirect is the whole answer, so even an
	// image this node cannot see is sent to the base.
	store.getErr = assert.AnError
	rec := serveUploads(h, "GET", "/uploads/2026/01/01/a.png")
	assert.Equal(t, http.StatusFound, rec.Code)

	for _, bad := range []string{
		"/uploads/2026/01/01/",
		"/uploads/2026/01/01/.hidden.png",
		"/uploads/2026//01/01/a.png",
		"/uploads/.writable-x",
	} {
		rec := serveUploads(h, "GET", bad)
		assert.Equal(t, http.StatusNotFound, rec.Code, bad)
		assert.Empty(t, rec.Header().Get("Location"), bad)
	}

	rec = serveUploads(h, "POST", "/uploads/2026/01/01/a.png")
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
	assert.Empty(t, rec.Header().Get("Location"))

	// A path base stays a path.
	h = uploadsHandlerFor(store, newImageURLs(imageURLBaseConfig("/media", "")))
	rec = serveUploads(h, "GET", "/uploads/2026/01/01/a.png")
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/media/2026/01/01/a.png", rec.Header().Get("Location"))
}

func TestUploadsStreamWithoutImageURLBase(t *testing.T) {
	store := &fakeImageStore{objects: map[string][]byte{"2026/01/01/a.png": tinyPNG(t)}}
	h := uploadsHandlerFor(store, newImageURLs(imageURLBaseConfig("", "")))
	rec := serveUploads(h, "GET", "/uploads/2026/01/01/a.png")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"))
	assert.Equal(t, tinyPNG(t), rec.Body.Bytes())
}

func TestObjectStoreProviderHost(t *testing.T) {
	for _, base := range []string{
		"https://blog.s3.amazonaws.com",
		"https://s3.eu-west-1.amazonaws.com/blog",
		"https://pub-0123.r2.dev",
		"https://acct.r2.cloudflarestorage.com/blog",
		"https://blog.nyc3.digitaloceanspaces.com",
		"https://f000.backblazeb2.com/file/blog",
		"https://blog.us-east-1.linodeobjects.com",
		"https://s3.wasabisys.com/blog",
		"https://storage.googleapis.com/blog",
		"https://STORAGE.googleapis.com/blog",
	} {
		assert.NotEmpty(t, imageURLBaseWarning(base), base)
		assert.Contains(t, imageURLBaseWarning(base), "federated", base)
	}
	for _, base := range []string{
		"",
		"/media",
		"https://media.quigs.blog",
		"https://notamazonaws.com",
		"https://r2.dev.example.org",
		"https://googleapis.com",
		"https://evil-storage.googleapis.com.example.org",
	} {
		assert.Empty(t, imageURLBaseWarning(base), base)
	}
}

func TestImageURLBaseNeverChangesWhatIsStored(t *testing.T) {
	img := &PostImage{Path: "2026/09/02/pic.png"}
	assert.Equal(t, "/uploads/2026/09/02/pic.png", img.URL(), "the editor and the database keep /uploads/ paths")
	assert.True(t, strings.HasPrefix(img.URL(), "/"+uploadsDir+"/"))
}

// TestImageURLBaseThroughTheRouter renders a real post page, which shows the
// base, and its editor, which still gets the body as stored; and /uploads/
// redirects.
func TestImageURLBaseThroughTheRouter(t *testing.T) {
	app, router := newTemplateTestApp(t, func(cfg *config.Config) {
		cfg.App.SingleUser = false
		cfg.Uploads.Enabled = true
		cfg.Storage = config.StorageCfg{
			Type:         config.StorageS3,
			S3Endpoint:   "http://127.0.0.1:1",
			S3Bucket:     "blog",
			S3Prefix:     "up",
			ImageURLBase: "https://media.example.com",
		}
	})
	u, coll, _ := createTemplateTestUser(t, app, "pics")
	cookie := loginCookie(t, app, u)
	title := "Pictures"
	body := "A picture.\n\n![pic](/uploads/2026/09/02/pic.png)\n"
	post, err := app.db.CreatePost(u.ID, coll.ID, &SubmittedPost{Title: &title, Content: &body})
	require.NoError(t, err)

	const want = `src="https://media.example.com/up/2026/09/02/pic.png"`
	rec := assertRendersCleanly(t, router, "GET", "/"+coll.Alias+"/"+post.Slug.String, nil, http.StatusOK)
	assert.Contains(t, rec.Body.String(), want)
	assert.NotContains(t, rec.Body.String(), `src="/uploads/`)

	rec = assertRendersCleanly(t, router, "GET", "/"+coll.Alias+"/"+post.Slug.String+"/edit", []*http.Cookie{cookie}, http.StatusOK)
	assert.Contains(t, rec.Body.String(), "/uploads/2026/09/02/pic.png", "the editor gets the body as stored")
	var stored string
	require.NoError(t, app.db.QueryRow("SELECT content FROM posts WHERE id = ?", post.ID).Scan(&stored))
	assert.Equal(t, body, stored)

	rec, _ = renderedRequest(t, router, "GET", "/uploads/2026/09/02/pic.png", nil)
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "https://media.example.com/up/2026/09/02/pic.png", rec.Header().Get("Location"))
}
