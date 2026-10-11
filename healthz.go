/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"context"
	"net/http"
	"time"
)

// healthzPath is the status-only health route. "healthz" is a reserved
// username (see author.reservedUsernames), so no blog can claim it in
// multi-user mode, where blogs live at top-level paths.
const healthzPath = "/healthz"

// healthzTimeout bounds the database check, so a hung connection reports
// unhealthy instead of holding the monitor's request open.
const healthzTimeout = 3 * time.Second

// handleHealthz answers 200 when the database answers a query and 503 when
// it does not, both with an empty body.
//
// It deliberately says nothing else: no version, no error text, and no
// session cookie, redirect or authentication check, so it answers a
// monitor the same way on a public or a private instance. It is a plain
// http.HandlerFunc rather than one of Handler's wrappers because each of
// those either touches the session or enforces private mode.
func (app *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), healthzTimeout)
	defer cancel()
	if !app.databaseHealthy(ctx) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// databaseHealthy reports whether the database answers SELECT 1. A ping
// alone is not enough: database/sql only reaches the server on a ping when
// the driver implements driver.Pinger, and otherwise reports a pooled
// connection healthy without using it.
func (app *App) databaseHealthy(ctx context.Context) bool {
	if app.db == nil || app.db.DB == nil {
		return false
	}
	var one int
	if err := app.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return false
	}
	return one == 1
}
