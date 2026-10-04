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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/writefreely/writefreely/migrations"
)

// wisp_v3 (migrations/wisp_v3.go): the case-insensitive subscriber-email lookups that
// span every blog, and a blog's language lookups, are served by an index on
// every engine.

// lowerLookupIndexes are the indexes wisp_v3 creates on each engine.
func lowerLookupIndexes(driverName string) (email, language string) {
	if driverName == driverMySQL {
		return "emailsubscribers_email_lower", "posts_coll_language_lower"
	}
	return "emailsubscribers_lower_email", "posts_coll_lower_language"
}

// insertLookupRows fills emailsubscribers and posts across several blogs and
// languages, so that a planner has tables worth using an index on. It
// returns one of the blogs.
func insertLookupRows(t *testing.T, app *App) int64 {
	t.Helper()
	owner := caseInsertUser(t, app, "indexed")
	langs := []string{"en", "fr", "de", "es", "it"}
	var first int64
	for c := 0; c < 5; c++ {
		collID := caseInsertCollection(t, app, fmt.Sprintf("indexed%d", c), owner)
		if c == 0 {
			first = collID
		}
		for i := 0; i < 20; i++ {
			_, err := app.db.Exec("INSERT INTO emailsubscribers (id, collection_id, email, subscribed, token, confirmed, allow_export) VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?, FALSE, FALSE)",
				fmt.Sprintf("s%d%06d", c, i), collID, fmt.Sprintf("reader%d@site%d.example", i, c), fmt.Sprintf("tok%d%012d", c, i))
			require.NoError(t, err)
			caseInsertPost(t, app, fmt.Sprintf("p%d%08d", c, i), fmt.Sprintf("post-%d", i), langs[i%len(langs)], "", owner, collID)
		}
	}
	analyzeLookupTables(t, app)
	return first
}

func analyzeLookupTables(t *testing.T, app *App) {
	t.Helper()
	var qs []string
	switch app.db.driverName {
	case driverSQLite, driverPostgres:
		qs = []string{"ANALYZE emailsubscribers", "ANALYZE posts"}
	case driverMySQL:
		qs = []string{"ANALYZE TABLE emailsubscribers, posts"}
	}
	for _, q := range qs {
		rows, err := app.db.Query(q)
		require.NoError(t, err, q)
		rows.Close()
	}
}

// lowerLookupPlans returns the plan of each query wisp_v3 serves, by name.
func lowerLookupPlans(t *testing.T, app *App, collID int64) map[string]string {
	t.Helper()
	const email = "reader7@site3.example"
	return map[string]string{
		"UpdateSubscriberConfirmed": explainPlan(t, app, app.db.confirmSubscriberEmailQuery(), email),
		"IsSubscriberConfirmed":     explainPlan(t, app, app.db.subscriberConfirmedQuery(), email),
		"GetCollLangTotalPosts":     explainPlan(t, app, app.db.collLangTotalPostsQuery(), collID, "fr"),
		"GetLangPosts":              explainPlan(t, app, app.db.langPostsQuery("AND created <= "+app.db.now(), "DESC", " LIMIT 10 OFFSET 0"), collID, "fr"),
	}
}

func assertLowerLookupsIndexed(t *testing.T, app *App, collID int64) {
	t.Helper()
	emailIdx, langIdx := lowerLookupIndexes(app.db.driverName)
	for name, plan := range lowerLookupPlans(t, app, collID) {
		want := langIdx
		if name == "UpdateSubscriberConfirmed" || name == "IsSubscriberConfirmed" {
			want = emailIdx
		}
		if app.db.driverName == driverMySQL {
			want = "key=" + want
		}
		assert.Contains(t, plan, want, "%s should use wisp_v3's index; plan: %s", name, plan)
	}
}

func TestLowerEmailLanguageLookupsUseIndex(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		collID := insertLookupRows(t, app)
		assertLowerLookupsIndexed(t, app, collID)
	})
}

// undoLowerEmailLanguageIndex takes a database back to before wisp_v3, as an upgrade
// finds it.
func undoLowerEmailLanguageIndex(t *testing.T, app *App) {
	t.Helper()
	var qs []string
	switch app.db.driverName {
	case driverSQLite, driverPostgres:
		qs = []string{"DROP INDEX emailsubscribers_lower_email", "DROP INDEX posts_coll_lower_language"}
	case driverMySQL:
		qs = []string{
			"ALTER TABLE emailsubscribers DROP COLUMN email_lower",
			// Dropping only the column would leave its index behind
			// on (collection_id, created), under the same name.
			"ALTER TABLE posts DROP INDEX posts_coll_language_lower, DROP COLUMN language_lower",
		}
	}
	qs = append(qs, "DELETE FROM wisp_migrations WHERE version >= 3")
	for _, q := range qs {
		_, err := app.db.Exec(q)
		require.NoError(t, err, q)
	}
}

func TestLowerEmailLanguageIndexMigration(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		undoLowerEmailLanguageIndex(t, app)

		// Rows written before wisp_v3, some in the case their writer sent.
		collID := insertLookupRows(t, app)
		_, err := app.db.Exec("INSERT INTO emailsubscribers (id, collection_id, email, subscribed, token, confirmed, allow_export) VALUES ('Legacy23', ?, 'Mixed@Example.COM', CURRENT_TIMESTAMP, 'LegacyToken00023', TRUE, FALSE)", collID)
		require.NoError(t, err)
		owner := caseInsertUser(t, app, "legacylang")
		caseInsertPost(t, app, "legacy00000023", "legacy-lang", "FR", "", owner, collID)

		mdb := migrations.NewDatastore(app.db.DB, app.db.driverName)
		require.NoError(t, migrations.Migrate(mdb))
		analyzeLookupTables(t, app)
		assertLowerLookupsIndexed(t, app, collID)

		assert.True(t, app.db.IsSubscriberConfirmed("mixed@example.com"))
		assert.False(t, app.db.IsSubscriberConfirmed("reader7@site3.example"))
		n, err := app.db.GetCollLangTotalPosts(collID, "FR")
		require.NoError(t, err)
		// Four of the blog's twenty posts are in fr, and the legacy FR one.
		assert.EqualValues(t, 5, n)

		// A migration that stopped after its schema change but before
		// recording itself runs again on the next start.
		_, err = app.db.Exec("DELETE FROM wisp_migrations WHERE version >= 3")
		require.NoError(t, err)
		require.NoError(t, migrations.Migrate(mdb))
		assertLowerLookupsIndexed(t, app, collID)
	})
}
