package writefreely

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
)

// withTestDatastore runs testBody against withTestDB's database, as a
// datastore for the engine in use: the WF_TEST_DB_TYPE engine under the
// mysql or postgres harness, and refDriver (the driver these tests always
// used) on the TEST_MYSQL reference database.
func withTestDatastore(t *testing.T, refDriver string, testBody func(db *sql.DB, ds *datastore)) {
	withTestDB(t, func(db *sql.DB) {
		ds := &datastore{DB: db, driverName: refDriver}
		if engine, _ := testDBEngine(); engine != driverSQLite {
			ds = newDatastore(db, engine)
		}
		testBody(db, ds)
	})
}

func TestOAuthDatastore(t *testing.T) {
	if !runAnyMySQLTests() && !runPostgresTests() {
		t.Skip("skipping mysql and postgres tests")
	}
	withTestDatastore(t, driverMySQL, func(db *sql.DB, ds *datastore) {
		ctx := context.Background()

		state, err := ds.GenerateOAuthState(ctx, "test", "development", 0, "")
		assert.NoError(t, err)
		assert.Len(t, state, 24)

		countRows(t, ctx, db, 1, "SELECT COUNT(*) FROM oauth_client_states WHERE state = ? AND used = false", state)

		_, _, _, _, err = ds.ValidateOAuthState(ctx, state)
		assert.NoError(t, err)

		countRows(t, ctx, db, 1, "SELECT COUNT(*) FROM oauth_client_states WHERE state = ? AND used = true", state)

		var localUserID int64 = 99
		var remoteUserID = "100"
		err = ds.RecordRemoteUserID(ctx, localUserID, remoteUserID, "test", "test", "access_token_a")
		assert.NoError(t, err)

		countRows(t, ctx, db, 1, "SELECT COUNT(*) FROM oauth_users WHERE user_id = ? AND remote_user_id = ? AND access_token = 'access_token_a'", localUserID, remoteUserID)

		err = ds.RecordRemoteUserID(ctx, localUserID, remoteUserID, "test", "test", "access_token_b")
		assert.NoError(t, err)

		countRows(t, ctx, db, 1, "SELECT COUNT(*) FROM oauth_users WHERE user_id = ? AND remote_user_id = ? AND access_token = 'access_token_b'", localUserID, remoteUserID)

		countRows(t, ctx, db, 1, "SELECT COUNT(*) FROM oauth_users")

		foundUserID, err := ds.GetIDForRemoteUser(ctx, remoteUserID, "test", "test")
		assert.NoError(t, err)
		assert.Equal(t, localUserID, foundUserID)
	})
}

func TestUpdatePostPinStateUnchanged(t *testing.T) {
	if !runAnyMySQLTests() && !runPostgresTests() {
		t.Skip("skipping mysql and postgres tests")
	}
	withTestDatastore(t, "", func(db *sql.DB, ds *datastore) {

		const postID = "repinsamepos0001"
		var collID, ownerID int64 = 7, 3
		_, err := db.Exec("INSERT INTO posts (id, privacy, owner_id, collection_id, view_count, title, content) VALUES (?, 0, ?, ?, 0, '', '')", postID, ownerID, collID)
		assert.NoError(t, err)

		// Pinning, then pinning again at the same position, leaves the row
		// unchanged the second time. That is still the owner's own post.
		assert.NoError(t, ds.UpdatePostPinState(true, postID, collID, ownerID, 1))
		assert.NoError(t, ds.UpdatePostPinState(true, postID, collID, ownerID, 1))

		// Likewise unpinning a post that is already unpinned.
		assert.NoError(t, ds.UpdatePostPinState(false, postID, collID, ownerID, 0))
		assert.NoError(t, ds.UpdatePostPinState(false, postID, collID, ownerID, 0))

		// Someone else's post, or a post in another collection, is still
		// forbidden.
		assert.Equal(t, ErrForbiddenCollection, ds.UpdatePostPinState(true, postID, collID, ownerID+1, 1))
		assert.Equal(t, ErrForbiddenCollection, ds.UpdatePostPinState(true, postID, collID+1, ownerID, 1))
		assert.Equal(t, ErrForbiddenCollection, ds.UpdatePostPinState(false, postID, collID, ownerID+1, 0))
	})
}
