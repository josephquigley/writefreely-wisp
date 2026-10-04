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
	"strings"
	"testing"
)

// TestPostgresTLSConnectionString connects through connectToDatabase with
// `tls = true`. lib/pq checks the sslmode only after reaching the server, so
// this needs a real one. The test server may or may not offer TLS; either
// way the connection must not fail because lib/pq rejected the sslmode
// itself, which is what app.go's `sslmode=enable` causes.
func TestPostgresTLSConnectionString(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		restore := connectLikeProduction(t, app, adminUser(t), adminPassword(t), true)
		defer restore()
		if err := app.db.Ping(); err != nil && strings.Contains(err.Error(), "sslmode") {
			t.Fatalf("lib/pq rejected the sslmode connectToDatabase chose: %v", err)
		}
	})
}
