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
	"net/url"
	"regexp"
	"strings"

	"github.com/writeas/web-core/activitystreams"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// imageAttachments builds a post's ActivityPub attachment list.
//
// Mastodon strips <img> out of an object's content and renders media only from
// the attachment array, so an image that appears solely inline is invisible
// there. Attaching it is what makes it display.
//
// The syndicated HTML is the source, with extracted supplying the bare image
// URLs that live in the post text rather than in a tag, and altText their
// names. An alt attribute in the HTML wins over that map.
//
// urls is image_url_base. The HTML has been through it already, but the
// extracted URLs come straight from the post body, so without it the same
// image would be attached twice: once at the base, once at /uploads/.
func imageAttachments(syndicatedHTML string, extracted []string, altText map[string]string, base string, urls imageURLs) []activitystreams.Attachment {
	images := postImages("", syndicatedHTML, base)

	b, err := url.Parse(base)
	baseUsable := err == nil && b.IsAbs()
	for i, img := range images {
		if resolved, ok := resolveImageURL(urls.rewriteURL(img.URL), b, baseUsable); ok {
			images[i].URL = resolved
		}
	}
	for _, raw := range extracted {
		if resolved, ok := resolveImageURL(urls.rewriteURL(raw), b, baseUsable); ok {
			images = append(images, postImage{URL: resolved, Alt: altText[raw]})
		}
	}

	var attachments []activitystreams.Attachment
	seen := map[string]bool{}
	for _, img := range images {
		if seen[img.URL] {
			continue
		}
		seen[img.URL] = true

		a := activitystreams.NewImageAttachment(img.URL)
		if img.Alt != "" {
			a.Name = img.Alt
		}
		attachments = append(attachments, a)
	}
	return attachments
}

type parsedImage struct {
	src string
	alt string
}

// parseImages returns the src and alt of every <img> in content, in document
// order. Content that will not parse yields no images rather than an error:
// a missing attachment is better than a dropped post.
func parseImages(content string) []parsedImage {
	if content == "" {
		return nil
	}

	body := &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := xhtml.ParseFragment(strings.NewReader(content), body)
	if err != nil {
		return nil
	}

	var images []parsedImage
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.DataAtom == atom.Img {
			var img parsedImage
			for _, attr := range n.Attr {
				switch attr.Key {
				case "src":
					img.src = attr.Val
				case "alt":
					img.alt = attr.Val
				}
			}
			if img.src != "" {
				images = append(images, img)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return images
}

// imageURLsOf flattens images to their URLs, appending any extra URLs that were
// found elsewhere and are not already present.
func imageURLsOf(images []postImage, extra []string, base string) []string {
	out := make([]string, 0, len(images))
	seen := map[string]bool{}
	for _, img := range images {
		if seen[img.URL] {
			continue
		}
		seen[img.URL] = true
		out = append(out, img.URL)
	}

	b, err := url.Parse(base)
	baseUsable := err == nil && b.IsAbs()
	for _, raw := range extra {
		resolved, ok := resolveImageURL(raw, b, baseUsable)
		if !ok || seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, resolved)
	}
	return out
}

// AbsoluteImages returns the post's images as absolute URLs, resolved against
// base — the collection's canonical URL at every call site.
func (p *PublicPost) AbsoluteImages(base string) []string {
	return imageURLsOf(postImages(p.Content, string(p.HTMLContent), base), nil, base)
}

// AbsoluteImages returns the post's images as absolute URLs, resolved against
// base — the site host for a standalone post, which has no collection.
func (p *AnonymousPost) AbsoluteImages(base string) []string {
	return imageURLsOf(postImages(p.Content, string(p.HTMLContent), base), nil, base)
}

// postImage is one image a post references, with its alt text where the post
// supplied one.
type postImage struct {
	URL string
	Alt string
}

// postImages returns every image a post references, as absolute URLs resolved
// against base, in order of first appearance and without duplicates.
//
// It is the single answer to "what images does this post use", shared by the
// ActivityPub attachment list, og:image, twitter:image and the image sitemap,
// which previously each scanned differently and disagreed.
//
// Images are found in three ways, because no one of them sees everything:
//
//   - <img> tags, in the rendered HTML when the caller has it, and otherwise in
//     the body itself, which catches raw HTML the Markdown renderer passes
//     through untouched.
//   - Markdown image syntax, filtered to image-looking paths.
//   - Bare image URLs sitting in the text, which are neither tags nor Markdown
//     images but have always been treated as the post's images.
//
// Rendered HTML is scanned as-is: the Markdown renderer has already escaped
// anything inside a code block, so an example cannot be mistaken for markup.
// The raw body has not been through that, so code is stripped from it first.
func postImages(content, renderedHTML, base string) []postImage {
	b, err := url.Parse(base)
	baseUsable := err == nil && b.IsAbs()

	var out []postImage
	seen := map[string]bool{}

	add := func(raw, alt string) {
		resolved, ok := resolveImageURL(raw, b, baseUsable)
		if !ok || seen[resolved] {
			return
		}
		seen[resolved] = true
		out = append(out, postImage{URL: resolved, Alt: strings.TrimSpace(alt)})
	}

	for _, img := range parseImages(renderedHTML) {
		add(img.src, img.alt)
	}

	body := stripCode(content)

	for _, img := range parseImages(body) {
		add(img.src, img.alt)
	}
	for _, m := range imageMarkdownRegex.FindAllStringSubmatch(body, -1) {
		target := strings.TrimSpace(m[2])
		u, err := url.Parse(target)
		if err != nil || !imageURLRegex.MatchString(u.Path) {
			continue
		}
		add(target, m[1])
	}
	for _, u := range extractImages(body) {
		add(u, "")
	}

	return out
}

// resolveImageURL makes one image reference absolute, reporting whether it is
// usable. A relative reference with no base to resolve against is not: emitting
// it raw would put an invalid URL where an absolute one is required.
func resolveImageURL(raw string, base *url.URL, baseUsable bool) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if !ref.IsAbs() {
		if !baseUsable {
			return "", false
		}
		ref = base.ResolveReference(ref)
	}
	return ref.String(), true
}

var (
	fencedCodeRegex = regexp.MustCompile("(?s)```.*?```|(?s)~~~.*?~~~")
	inlineCodeRegex = regexp.MustCompile("`[^`\n]*`")
)

// stripCode removes fenced code blocks and inline code spans from a Markdown
// body, so that an image written as an example is not mistaken for one the post
// displays. A post about HTML should not advertise its own sample as og:image.
//
// Indented code blocks are not handled: telling four-space indentation apart
// from list continuation needs a real Markdown parse, and the callers that scan
// a raw body are the ones without rendered HTML to hand.
func stripCode(content string) string {
	return inlineCodeRegex.ReplaceAllString(fencedCodeRegex.ReplaceAllString(content, "\n"), " ")
}
