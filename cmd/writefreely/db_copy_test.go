//go:build sqlite && !wflib
// +build sqlite,!wflib

/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package main

import "testing"

func TestDBCopyCommandPresent(t *testing.T) {
	if !hasDBSubcommand("copy") {
		t.Error("`db copy` is missing from a build with the sqlite tag")
	}
}
