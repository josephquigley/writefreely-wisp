/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package config

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

const (
	StorageLocal = "local"
	StorageS3    = "s3"
)

// UsesS3 reports whether uploaded images are kept in an S3-compatible store.
func (s StorageCfg) UsesS3() bool {
	return strings.EqualFold(strings.TrimSpace(s.Type), StorageS3)
}

// validate refuses a [storage] section that cannot work, at load rather than
// at the first upload. appHost is [app] host, which image_url_base must not
// point back into.
//
// No message here may include a credential. They name the key that is wrong,
// never its value, since a value is exactly where a secret would be.
func (s *StorageCfg) validate(appHost string) error {
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "", StorageLocal:
		if strings.TrimSpace(s.ImageURLBase) != "" {
			// A local directory is served by this app and nothing else,
			// so a base would send readers somewhere with no images.
			return fmt.Errorf("[storage] image_url_base needs type = s3: with local storage nothing but this server can serve the images")
		}
		return nil
	case StorageS3:
	default:
		return fmt.Errorf("[storage] type must be %q or %q", StorageLocal, StorageS3)
	}
	if err := s.validateImageURLBase(appHost); err != nil {
		return err
	}

	u, err := url.Parse(strings.TrimSpace(s.S3Endpoint))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("[storage] s3_endpoint must be a URL such as https://s3.example.org")
	}
	if u.User != nil {
		// It would be printed wherever the endpoint is, so credentials
		// go in their own keys and nowhere else.
		return fmt.Errorf("[storage] s3_endpoint must not contain credentials; use s3_access_key_id and s3_secret_access_key")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("[storage] s3_endpoint must not have a path; the bucket goes in s3_bucket")
	}
	var missing []string
	for _, k := range []struct{ name, val string }{
		{"s3_bucket", s.S3Bucket},
		{"s3_access_key_id", s.S3AccessKeyID},
		{"s3_secret_access_key", s.S3SecretAccessKey},
	} {
		if strings.TrimSpace(k.val) == "" {
			missing = append(missing, k.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("[storage] type = s3 needs %s", strings.Join(missing, ", "))
	}

	// One leading or trailing slash is a typo, not a different layout.
	s.S3Prefix = strings.Trim(strings.TrimSpace(s.S3Prefix), "/")
	if strings.Contains(s.S3Prefix, "//") || strings.Contains("/"+s.S3Prefix+"/", "/../") {
		return fmt.Errorf("[storage] s3_prefix must be a plain path such as blog/uploads")
	}
	return nil
}

// validateImageURLBase normalises image_url_base and refuses one that cannot
// work. It is an absolute http(s) URL, or a path on this host such as /media
// for a reverse proxy in front of the app that serves the bucket there. A
// trailing slash is dropped, since the object key is joined on with one.
//
// A query or fragment is refused because the key is appended to the path,
// and a base inside /uploads/ on this host because that is where the app
// redirects to the base from: it would redirect to itself.
func (s *StorageCfg) validateImageURLBase(appHost string) error {
	base := strings.TrimSpace(s.ImageURLBase)
	if base == "" {
		s.ImageURLBase = ""
		return nil
	}
	bad := fmt.Errorf("[storage] image_url_base must be a URL such as https://media.example.org, or a path on this host such as /media")
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return bad
	}
	sameHost := false
	switch {
	case u.Scheme == "" && u.Host == "":
		// A path, so it must be absolute: a relative one would resolve
		// against each page's own address.
		if !strings.HasPrefix(u.Path, "/") {
			return bad
		}
		sameHost = true
	case (u.Scheme == "http" || u.Scheme == "https") && u.Host != "":
		if h, err := url.Parse(appHost); err == nil && strings.EqualFold(h.Host, u.Host) {
			sameHost = true
		}
	default:
		return bad
	}
	p := strings.TrimSuffix(u.Path, "/")
	if (u.Host == "" && p == "") || strings.HasSuffix(p, "/") {
		return bad
	}
	if p != "" && path.Clean(p) != p {
		return bad
	}
	if sameHost && (p == "/uploads" || strings.HasPrefix(p, "/uploads/")) {
		return fmt.Errorf("[storage] image_url_base must not be under /uploads/ on this host, which redirects to it")
	}
	s.ImageURLBase = strings.TrimSuffix(base, "/")
	return nil
}
