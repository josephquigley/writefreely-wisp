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

	"github.com/writefreely/writefreely/config"
)

// TestPostgresTLSConnectionString checks that `tls = true` produces a
// connection string lib/pq accepts. Nothing listens on port 1, so the ping
// must fail on the connection, not on parsing the sslmode.
func TestPostgresTLSConnectionString(t *testing.T) {
	app := &App{cfg: &config.Config{}}
	app.cfg.Database.Type = driverPostgres
	app.cfg.Database.User = "wf"
	app.cfg.Database.Password = "wf"
	app.cfg.Database.Database = "wf"
	app.cfg.Database.Host = "127.0.0.1"
	app.cfg.Database.Port = 1
	app.cfg.Database.TLS = true
	connectToDatabase(app)
	defer app.db.Close()

	err := app.db.Ping()
	if err == nil {
		t.Fatal("Ping: want a connection error, got nil")
	}
	if strings.Contains(err.Error(), "sslmode") {
		t.Fatalf("Ping: lib/pq rejected the sslmode: %v", err)
	}
}
