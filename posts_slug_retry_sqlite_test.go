//go:build sqlite && !wflib
// +build sqlite,!wflib

package writefreely

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreatePostSlugRetrySQLite(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if !assert.NoError(t, err) {
		return
	}
	defer db.Close()
	// One connection, so every statement sees the same in-memory database.
	db.SetMaxOpenConns(1)
	_, err = db.Exec(sqliteSql)
	if !assert.NoError(t, err) {
		return
	}
	runSlugRetryTests(t, &datastore{DB: db, driverName: driverSQLite})
}
