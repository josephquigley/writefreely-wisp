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

	"github.com/writefreely/writefreely/config"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// imageURLPathPattern is imageURLPattern anchored to a whole URL path, so
// that the rewrite below matches exactly the URLs attachPostImages and the
// orphan sweep count as this instance's images, and no others.
var imageURLPathPattern = regexp.MustCompile(`^` + imageURLPattern.String() + `$`)

// imageURLs maps uploaded images to the URL readers fetch them from when
// [storage] image_url_base is set: something other than this app, such as a
// CDN or a reverse proxy, serves the bucket there.
//
// Post bodies are never changed. They keep the /uploads/<path> URLs the
// editor inserted, which is what attachPostImages, the reference counts and
// the orphan sweep all look for. The base is applied to copies on their way
// out: rendered pages, feeds and ActivityPub objects. Turning it off, or
// moving it, therefore needs no migration, and /uploads/ redirects to the
// base for every copy that has already left.
//
// The zero value is off, and everything it does is then a no-op.
type imageURLs struct {
	base   string // image_url_base, validated, no trailing slash
	prefix string // the bucket's key prefix, as s3KeyPrefix gives it
	host   string // [app] host's host[:port], to recognise absolute URLs
}

func newImageURLs(cfg *config.Config) imageURLs {
	if cfg == nil || cfg.Storage.ImageURLBase == "" {
		return imageURLs{}
	}
	u := imageURLs{base: cfg.Storage.ImageURLBase, prefix: s3KeyPrefix(cfg.Storage)}
	if h, err := url.Parse(cfg.App.Host); err == nil {
		u.host = h.Host
	}
	return u
}

func (u imageURLs) on() bool {
	return u.base != ""
}

// forPath returns the URL of the image stored at relPath. What follows the
// base is the object's key in the bucket, prefix and all, built by the same
// function the store writes with, so the two cannot drift apart.
func (u imageURLs) forPath(relPath string) string {
	return u.base + "/" + s3ObjectKey(u.prefix, relPath)
}

// rewriteURL returns the base URL for v if v is one of this instance's
// uploads, relative or absolute on its own host, and v unchanged otherwise.
// A URL with a query or fragment is left alone: nothing this instance writes
// has one, and there is no saying what the base would make of it.
func (u imageURLs) rewriteURL(v string) string {
	if !u.on() {
		return v
	}
	ref, err := url.Parse(strings.TrimSpace(v))
	if err != nil || ref.User != nil || ref.RawQuery != "" || ref.ForceQuery || ref.Fragment != "" {
		return v
	}
	switch {
	case ref.Host != "":
		if ref.Scheme != "" && ref.Scheme != "http" && ref.Scheme != "https" {
			return v
		}
		if !strings.EqualFold(ref.Host, u.host) {
			return v
		}
	case ref.Scheme != "":
		return v
	}
	m := imageURLPathPattern.FindStringSubmatch(ref.EscapedPath())
	if m == nil {
		return v
	}
	return u.forPath(m[1])
}

// rewriteHTML rewrites the URL attributes in content (src and href; see
// urlAttrs) that point at
// this instance's uploads, and leaves everything else, text
// included, as it was. It runs on HTML that has already been sanitised, so the
// sanitiser never has to know about the base's host.
//
// Content with nothing to rewrite is returned untouched rather than parsed
// and re-serialised, so an instance without a base, or a post without
// images, renders byte for byte as it did before.
func (u imageURLs) rewriteHTML(content string) string {
	if !u.on() || !strings.Contains(content, "/"+uploadsDir+"/") {
		return content
	}
	body := &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := xhtml.ParseFragment(strings.NewReader(content), body)
	if err != nil {
		return content
	}
	changed := false
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			for i, attr := range n.Attr {
				if attr.Namespace != "" || !urlAttrs[attr.Key] {
					continue
				}
				if r := u.rewriteURL(attr.Val); r != attr.Val {
					n.Attr[i].Val = r
					changed = true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	if !changed {
		return content
	}
	var out strings.Builder
	for _, n := range nodes {
		if err := xhtml.Render(&out, n); err != nil {
			return content
		}
	}
	return out.String()
}

// objectStoreProviderSuffixes are the hostnames object store providers serve
// buckets from. A base on one of them works, but it is the provider's name,
// not the operator's.
var objectStoreProviderSuffixes = []string{
	".amazonaws.com",
	".r2.dev",
	".r2.cloudflarestorage.com",
	".digitaloceanspaces.com",
	".backblazeb2.com",
	".linodeobjects.com",
	".wasabisys.com",
}

// imageURLBaseWarning returns a warning to log at startup when base is on an
// object store provider's own hostname, or "" when it is not.
//
// Federated copies of a post carry the image URLs it had when it was sent, and
// remote servers keep them. A base on a hostname the operator owns can be
// pointed at a different store later; one on the provider's hostname cannot,
// so leaving the provider would break every image already federated.
func imageURLBaseWarning(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	provider := host == "storage.googleapis.com"
	for _, s := range objectStoreProviderSuffixes {
		if strings.HasSuffix(host, s) {
			provider = true
		}
	}
	if !provider {
		return ""
	}
	return "[storage] image_url_base is on " + host + ", an object store provider's hostname. " +
		"Image URLs are copied permanently into federated posts, so they will break if the images ever move to another provider. " +
		"Use a hostname you own, such as media.example.org, pointed at the bucket."
}
