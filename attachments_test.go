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
	"testing"
)

const attachBase = "https://quigs.blog/rel-img-test"

func TestImageAttachmentsFromUploadedImage(t *testing.T) {
	html := `<p><img src="https://quigs.blog/uploads/2026/09/02/pic.png" alt="a picture"></p>`

	got := imageAttachments(html, nil, nil, attachBase, imageURLs{})

	if len(got) != 1 {
		t.Fatalf("wanted 1 attachment, got %d: %+v", len(got), got)
	}
	if got[0].URL != "https://quigs.blog/uploads/2026/09/02/pic.png" {
		t.Errorf("wrong url: %s", got[0].URL)
	}
	if got[0].Type != "Image" {
		t.Errorf("wrong type: %s", got[0].Type)
	}
	if got[0].MediaType != "image/png" {
		t.Errorf("wrong media type: %s", got[0].MediaType)
	}
	if got[0].Name != "a picture" {
		t.Errorf("alt text should become the attachment name, got %q", got[0].Name)
	}
}

// A relative src should never reach this function post-absolutization, but
// resolving defensively costs nothing and keeps the attachment valid if some
// other caller passes raw rendered HTML.
func TestImageAttachmentsResolveRelativeSrc(t *testing.T) {
	html := `<img src="/uploads/2026/09/02/pic.png">`

	got := imageAttachments(html, nil, nil, attachBase, imageURLs{})

	if len(got) != 1 || got[0].URL != "https://quigs.blog/uploads/2026/09/02/pic.png" {
		t.Fatalf("wanted absolute upload url, got %+v", got)
	}
}

// Bare image URLs sitting in the post text are attached today via p.Images,
// even though they are not <img> tags. That behavior must survive.
func TestImageAttachmentsKeepsTextExtractedImages(t *testing.T) {
	extracted := []string{"https://example.com/photo.jpg"}
	alts := map[string]string{"https://example.com/photo.jpg": "a photo"}

	got := imageAttachments("<p>no img tags here</p>", extracted, alts, attachBase, imageURLs{})

	if len(got) != 1 {
		t.Fatalf("wanted 1 attachment, got %d: %+v", len(got), got)
	}
	if got[0].URL != "https://example.com/photo.jpg" {
		t.Errorf("wrong url: %s", got[0].URL)
	}
	if got[0].Name != "a photo" {
		t.Errorf("wanted alt text from the markdown map, got %q", got[0].Name)
	}
}

func TestImageAttachmentsDeduplicates(t *testing.T) {
	html := `<img src="https://example.com/photo.jpg" alt="from html">`
	extracted := []string{"https://example.com/photo.jpg"}

	got := imageAttachments(html, extracted, nil, attachBase, imageURLs{})

	if len(got) != 1 {
		t.Fatalf("wanted 1 attachment, got %d: %+v", len(got), got)
	}
	if got[0].Name != "from html" {
		t.Errorf("the html alt should win, got %q", got[0].Name)
	}
}

func TestImageAttachmentsPreservesOrder(t *testing.T) {
	html := `<img src="https://quigs.blog/a.png"><img src="https://quigs.blog/b.png">`

	got := imageAttachments(html, []string{"https://example.com/c.jpg"}, nil, attachBase, imageURLs{})

	wantOrder := []string{
		"https://quigs.blog/a.png",
		"https://quigs.blog/b.png",
		"https://example.com/c.jpg",
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("wanted %d attachments, got %d: %+v", len(wantOrder), len(got), got)
	}
	for i, want := range wantOrder {
		if got[i].URL != want {
			t.Errorf("position %d: wanted %s, got %s", i, want, got[i].URL)
		}
	}
}

func TestImageAttachmentsWithNoImages(t *testing.T) {
	if got := imageAttachments("<p>Just words.</p>", nil, nil, attachBase, imageURLs{}); len(got) != 0 {
		t.Errorf("wanted no attachments, got %+v", got)
	}
}

func TestImageAttachmentsOmitsEmptyAltText(t *testing.T) {
	got := imageAttachments(`<img src="https://quigs.blog/a.png" alt="">`, nil, nil, attachBase, imageURLs{})

	if len(got) != 1 {
		t.Fatalf("wanted 1 attachment, got %d", len(got))
	}
	if got[0].Name != "" {
		t.Errorf("wanted empty name, got %q", got[0].Name)
	}
}

// TestActivityObjectAttachesUploadedImages is the wiring test: an uploaded
// image referenced relatively in the body must reach the ActivityPub object as
// an attachment, because Mastodon strips <img> from content and renders media
// only from the attachment array.
func TestActivityObjectAttachesUploadedImages(t *testing.T) {
	app, p := syndicatedPost("https://quigs.blog")
	p.HTMLContent = template.HTML(`<p><img src="/uploads/2026/09/02/pic.png" alt="pic"></p>`)

	o := p.ActivityObject(app)

	if len(o.Attachment) != 1 {
		t.Fatalf("wanted 1 attachment, got %d: %+v", len(o.Attachment), o.Attachment)
	}
	if o.Attachment[0].URL != "https://quigs.blog/uploads/2026/09/02/pic.png" {
		t.Errorf("wanted an absolute upload url, got %s", o.Attachment[0].URL)
	}
	if o.Attachment[0].Name != "pic" {
		t.Errorf("wanted alt text carried through, got %q", o.Attachment[0].Name)
	}
}

const previewBase = "https://quigs.blog/"

func TestPreviewImagesIncludesUploads(t *testing.T) {
	content := "Intro.\n\n![pic](/uploads/2026/09/02/pic.png)\n"

	got := previewImages(content, nil, previewBase)

	want := []string{"https://quigs.blog/uploads/2026/09/02/pic.png"}
	assertURLs(t, want, got)
}

func TestPreviewImagesKeepsExternalImages(t *testing.T) {
	content := "![pic](https://example.com/photo.jpg)\n"
	extracted := []string{"https://example.com/photo.jpg"}

	got := previewImages(content, extracted, previewBase)

	assertURLs(t, []string{"https://example.com/photo.jpg"}, got)
}

func TestPreviewImagesSkipsNonImages(t *testing.T) {
	content := "[a page](/some-post)\n\n![notes](/uploads/notes.txt)\n"

	if got := previewImages(content, nil, previewBase); len(got) != 0 {
		t.Errorf("wanted no images, got %v", got)
	}
}

func TestPreviewImagesPreservesOrder(t *testing.T) {
	// Markdown images first, then bare URLs sitting in the text.
	content := "![a](/uploads/a.png)\n\n![b](/uploads/b.png)\n\nAlso https://example.com/c.jpg\n"
	extracted := []string{"https://example.com/c.jpg"}

	got := previewImages(content, extracted, previewBase)

	assertURLs(t, []string{
		"https://quigs.blog/uploads/a.png",
		"https://quigs.blog/uploads/b.png",
		"https://example.com/c.jpg",
	}, got)
}

// With no usable base a relative path cannot be made absolute, and emitting it
// raw would put an invalid URL in og:image — worse than omitting the image,
// which falls back to the blog avatar. Absolute images are still returned.
func TestPreviewImagesDropsRelativeWhenBaseUnusable(t *testing.T) {
	content := "![pic](/uploads/pic.png)\n\n![ext](https://example.com/photo.jpg)\n"
	extracted := []string{"https://example.com/photo.jpg"}

	got := previewImages(content, extracted, "")

	assertURLs(t, []string{"https://example.com/photo.jpg"}, got)
}

func TestPreviewImagesWithNoImages(t *testing.T) {
	if got := previewImages("Just words.", nil, previewBase); len(got) != 0 {
		t.Errorf("wanted no images, got %v", got)
	}
}

func assertURLs(t *testing.T, want, got []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("wanted %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: wanted %s, got %s", i, want[i], got[i])
		}
	}
}

// extractImages collected into a map and returned map iteration order, so a
// post with several images produced a different order on every call — and
// twitter:image, which takes only the first, picked one at random.
func TestExtractImagesIsInOrderOfAppearance(t *testing.T) {
	content := "![a](https://example.com/a.png)\n\n![b](https://example.com/b.png)\n\n![c](https://example.com/c.png)"
	want := []string{
		"https://example.com/a.png",
		"https://example.com/b.png",
		"https://example.com/c.png",
	}

	for i := 0; i < 20; i++ {
		assertURLs(t, want, extractImages(content))
	}
}

func TestAnonymousPostAbsoluteImages(t *testing.T) {
	p := &AnonymousPost{Content: "![pic](/uploads/pic.png)"}

	got := p.AbsoluteImages("https://quigs.blog")

	assertURLs(t, []string{"https://quigs.blog/uploads/pic.png"}, got)
}

// A post documenting HTML must not advertise its own examples as images.
// Fenced blocks and inline spans are code, not markup.
func TestPreviewImagesIgnoresCodeBlocks(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"backtick fence", "Here is how:\n\n```\n<img src=\"/uploads/example.png\">\n```\n"},
		{"tilde fence", "Here is how:\n\n~~~\n<img src=\"/uploads/example.png\">\n~~~\n"},
		{"fence with language", "Here is how:\n\n```html\n<img src=\"/uploads/example.png\">\n```\n"},
		{"inline span", "Write `<img src=\"/uploads/example.png\">` to embed it."},
		{"markdown image in fence", "```\n![x](/uploads/example.png)\n```\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := previewImages(test.content, nil, previewBase); len(got) != 0 {
				t.Errorf("code should yield no images, got %v", got)
			}
		})
	}
}

// Fencing one image must not hide a real one elsewhere in the post.
func TestPreviewImagesKeepsRealImagesAlongsideCode(t *testing.T) {
	content := "```\n<img src=\"/uploads/example.png\">\n```\n\n![real](/uploads/real.png)\n"

	got := previewImages(content, nil, previewBase)

	assertURLs(t, []string{"https://quigs.blog/uploads/real.png"}, got)
}

// previewImages exercises the path og:image, twitter:image and the sitemap
// take: a post with no rendered HTML to hand, as the sitemap has.
func previewImages(content string, extracted []string, base string) []string {
	p := &PublicPost{Post: &Post{Content: content, Images: extracted}}
	return p.AbsoluteImages(base)
}
