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

func hasDBSubcommand(name string) bool {
	for _, c := range cmdDB.Subcommands {
		if c.Name == name {
			return true
		}
	}
	return false
}
