package writefreely

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// slugRetryTitle is the shared title every test post uses, so each insert after
// the first collides on (collection_id, slug).
const slugRetryTitle = "same title"

func createSlugRetryPost(ds *datastore, userID, collID int64) (*Post, error) {
	title := slugRetryTitle
	content := "body"
	return ds.CreatePost(userID, collID, &SubmittedPost{Title: &title, Content: &content})
}

// runSlugRetryTests exercises CreatePost's handling of duplicate slugs against
// a datastore with an empty schema.
func runSlugRetryTests(t *testing.T, ds *datastore) {
	const userID, collID = 1, 1

	orig := genSafeUniqueSlug
	defer func() { genSafeUniqueSlug = orig }()

	first, err := createSlugRetryPost(ds, userID, collID)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, "same-title", first.Slug.String)

	t.Run("succeeds after colliding suffixes", func(t *testing.T) {
		const collisions = 3
		calls := 0
		genSafeUniqueSlug = func(slug string) string {
			calls++
			if calls <= collisions {
				// Collides with the first post's slug every time.
				return slug
			}
			return fmt.Sprintf("%s-fresh", slug)
		}
		p, err := createSlugRetryPost(ds, userID, collID)
		assert.NoError(t, err)
		assert.Equal(t, collisions+1, calls)
		if p != nil {
			assert.Equal(t, "same-title-fresh", p.Slug.String)
		}
	})

	t.Run("gives up after the bound", func(t *testing.T) {
		calls := 0
		genSafeUniqueSlug = func(slug string) string {
			calls++
			return slug
		}
		_, err := createSlugRetryPost(ds, userID, collID)
		assert.Error(t, err)
		assert.Equal(t, maxSlugRetries, calls)
		if err != nil {
			assert.True(t, strings.Contains(err.Error(), "Retried slug generation"), err.Error())
		}
	})

	t.Run("identical titles all save", func(t *testing.T) {
		genSafeUniqueSlug = orig
		for i := 0; i < 200; i++ {
			_, err := createSlugRetryPost(ds, userID, collID)
			if !assert.NoError(t, err, "post %d", i) {
				return
			}
		}
	})
}

func TestCreatePostSlugRetryMySQL(t *testing.T) {
	if !runMySQLTests() {
		t.Skip("skipping mysql tests")
	}
	withTestDB(t, func(db *sql.DB) {
		runSlugRetryTests(t, &datastore{DB: db, driverName: driverMySQL})
	})
}
