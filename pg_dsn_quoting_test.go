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

	"github.com/lib/pq"
)

// TestPostgresConnectPasswordQuoting connects through connectToDatabase as a
// role whose password lib/pq can only read if the value is quoted in the
// connection string. app.go builds that string with an unquoted
// "password=%s", so a space ends the value early and a backslash is taken
// as an escape.
func TestPostgresConnectPasswordQuoting(t *testing.T) {
	for _, tc := range []struct{ name, role, password string }{
		{"space", "wf_space", "pass word"},
		{"backslash", "wf_backslash", `back\slash`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withPostgresTestApp(t, func(app *App) {
				if _, err := app.db.Exec("CREATE ROLE " + pq.QuoteIdentifier(tc.role) + " LOGIN PASSWORD " + pq.QuoteLiteral(tc.password)); err != nil {
					t.Fatalf("create role: %v", err)
				}
				defer app.db.Exec("DROP ROLE IF EXISTS " + pq.QuoteIdentifier(tc.role))

				restore := connectLikeProduction(t, app, tc.role, tc.password, false)
				err := app.db.Ping()
				restore()
				if err != nil {
					t.Fatalf("connect as %s with password %q: %v", tc.role, tc.password, err)
				}
			})
		})
	}
}
