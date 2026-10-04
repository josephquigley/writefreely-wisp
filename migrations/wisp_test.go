/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package migrations

import "testing"

// Every upstream migration after V17 must have exactly one entry in
// wispMigrations, in upstream's order. This is the test that fails after an
// upstream merge brings in a migration nobody has reviewed for wisp yet:
// until it has an entry, Migrate would never run it. Read lastSharedVersion
// before adding the first.
func TestEveryUpstreamMigrationHasAWispEntry(t *testing.T) {
	next := upstreamBaseVersion + 1
	for i, m := range wispMigrations {
		if m.upstream == 0 {
			continue
		}
		if m.upstream != next {
			t.Errorf("wisp_v%d maps upstream V%d, want V%d: upstream migrations after V%d must each appear once, in order", i+1, m.upstream, next, upstreamBaseVersion)
		}
		next = m.upstream + 1
	}
	if got := next - 1; got != len(migrations) {
		t.Errorf("upstream has %d migrations but wispMigrations maps them through V%d: add a wisp entry for each upstream migration merged since", len(migrations), got)
	}
}

// Upstream's V1 to V17 run before wispMigrations, so none of them may also
// be an entry in it, and upstream's list must still reach V17.
func TestUpstreamBaseRunsBeforeWispMigrations(t *testing.T) {
	if len(migrations) < upstreamBaseVersion {
		t.Fatalf("upstream's list has %d migrations, fewer than the V%d Migrate runs first", len(migrations), upstreamBaseVersion)
	}
	for i, m := range wispMigrations {
		if m.upstream != 0 && m.upstream <= upstreamBaseVersion {
			t.Errorf("wisp_v%d is upstream's V%d, which Migrate already runs before wispMigrations", i+1, m.upstream)
		}
	}
	// appmigrations' V18 converts to wisp_v1, so wisp_v1 is post images.
	if m := wispMigrations[0]; m.upstream != 0 || m.Description() != "support post images" {
		t.Errorf("wisp_v1 is %q, want wisp's own \"support post images\"", m.Description())
	}
}
