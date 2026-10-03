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
// at the first upload.
//
// No message here may include a credential. They name the key that is wrong,
// never its value, since a value is exactly where a secret would be.
func (s *StorageCfg) validate() error {
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "", StorageLocal:
		return nil
	case StorageS3:
	default:
		return fmt.Errorf("[storage] type must be %q or %q", StorageLocal, StorageS3)
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
