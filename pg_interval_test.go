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
)

func TestPostgresDateAddSub(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		var ok bool
		q := "SELECT " + app.db.dateAdd(1, "HOUR") + " > NOW() AND " + app.db.dateSub(1, "HOUR") + " < NOW()"
		if err := app.db.QueryRow(q).Scan(&ok); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if !ok {
			t.Errorf("%s: want true", q)
		}
	})
}

func TestPostgresTemporaryAccessToken(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		u := &User{Username: "tok", HashedPass: []byte("x")}
		if err := app.db.CreateUser(app.cfg, u, "tok", ""); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		if _, err := app.db.GetTemporaryAccessToken(u.ID, 60); err != nil {
			t.Fatalf("GetTemporaryAccessToken: %v", err)
		}
	})
}

func TestPostgresPasswordResetLookup(t *testing.T) {
	withPostgresTestApp(t, func(app *App) {
		u := &User{Username: "reset", HashedPass: []byte("x")}
		if err := app.db.CreateUser(app.cfg, u, "reset", ""); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		tok, err := app.db.CreatePasswordResetToken(u.ID)
		if err != nil {
			t.Fatalf("CreatePasswordResetToken: %v", err)
		}
		if got := app.db.GetUserFromPasswordReset(tok); got != u.ID {
			t.Errorf("GetUserFromPasswordReset = %d, want %d", got, u.ID)
		}
	})
}
