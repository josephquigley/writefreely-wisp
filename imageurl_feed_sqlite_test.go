//go:build sqlite

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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/config"
)

func TestFeedUsesImageURLBase(t *testing.T) {
	for _, c := range []struct {
		name, base, want string
	}{
		{"no base", "", "http://localhost:0/uploads/2026/09/02/pic.png"},
		{"absolute base", "https://media.example.com", "https://media.example.com/feeds/2026/09/02/pic.png"},
	} {
		t.Run(c.name, func(t *testing.T) {
			app := newFeedTestApp(t)
			if c.base != "" {
				app.cfg.Storage = config.StorageCfg{Type: config.StorageS3, S3Prefix: "feeds", ImageURLBase: c.base}
			}
			_, err := app.db.Exec("UPDATE posts SET content = ? WHERE id = 'p1'", "about #go today\n\n![pic](/uploads/2026/09/02/pic.png)")
			require.NoError(t, err)

			w, err := feedRequest(app, "")
			require.NoError(t, err)
			body := w.Body.String()
			assert.Contains(t, body, c.want)
			if c.base != "" {
				assert.NotContains(t, body, "localhost:0/uploads/")
			}
		})
	}
}
