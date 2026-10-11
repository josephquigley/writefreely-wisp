/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package author

import (
	"testing"

	"github.com/writefreely/writefreely/config"
)

// In multi-user mode blogs live at top-level paths, so a username that
// matches a route would shadow it. IsValidUsername returns false for every
// name when it cannot read the pages directory, so the test points it at
// the real one: otherwise a missing reservation would pass unnoticed.
func TestIsValidUsernameReservesRoutes(t *testing.T) {
	cfg := config.New()
	cfg.App.MinUsernameLen = 3
	cfg.Server.PagesParentDir = ".."

	if !IsValidUsername(cfg, "someone") {
		t.Fatal("an ordinary username must be valid, or this test proves nothing")
	}
	for _, name := range []string{"healthz", "login", "oauth", "api"} {
		if IsValidUsername(cfg, name) {
			t.Errorf("%q must be reserved", name)
		}
	}
}
