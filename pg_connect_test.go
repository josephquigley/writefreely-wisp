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
	"os"
	"strconv"
	"strings"
	"testing"
)

// connectLikeProduction swaps app's connection pool for one opened by
// connectToDatabase, the function production uses, pointed at the harness's
// test database as user/password. Host and port come from
// WF_TEST_PG_DSN, which must use plain key=value pairs without quoting; tls
// is the config file's [database] tls. The returned func closes the new pool.
func connectLikeProduction(t *testing.T, app *App, user, password string, tls bool) func() {
	t.Helper()
	opts := map[string]string{}
	for _, kv := range strings.Fields(os.Getenv("WF_TEST_PG_DSN")) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			opts[k] = v
		}
	}
	port, err := strconv.Atoi(opts["port"])
	if err != nil {
		port = 5432
	}
	var dbName string
	if err := app.db.QueryRow("SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatalf("current_database: %v", err)
	}

	cfg := *app.cfg
	prod := &App{cfg: &cfg}
	prod.cfg.Database.Type = driverPostgres
	prod.cfg.Database.Host = opts["host"]
	prod.cfg.Database.Port = port
	prod.cfg.Database.User = user
	prod.cfg.Database.Password = password
	prod.cfg.Database.Database = dbName
	prod.cfg.Database.TLS = tls
	connectToDatabase(prod)

	old := app.db
	app.db = prod.db
	return func() {
		prod.db.Close()
		app.db = old
	}
}
