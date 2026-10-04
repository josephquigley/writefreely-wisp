package writefreely

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wflog "github.com/writeas/web-core/log"
)

// captureLogs redirects the shared info and error loggers into a buffer for
// the duration of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldInfo, oldErr := wflog.InfoLog.Writer(), wflog.ErrorLog.Writer()
	wflog.InfoLog.SetOutput(&buf)
	wflog.ErrorLog.SetOutput(&buf)
	t.Cleanup(func() {
		wflog.InfoLog.SetOutput(oldInfo)
		wflog.ErrorLog.SetOutput(oldErr)
	})
	return &buf
}

// Re-subscribing finds the existing subscriber before inserting (or, under a
// concurrent insert, takes the duplicate-key path). Both used to log the
// subscriber's address in plain text.
func TestResubscribeDoesNotLogEmailAddress(t *testing.T) {
	forEachCaseEngine(t, func(t *testing.T, app *App) {
		if app.db.driverName == driverSQLite && !SQLiteEnabled {
			t.Skip("duplicate-key detection on SQLite needs the sqlite build tag")
		}
		owner := caseInsertUser(t, app, "resublog")
		coll := caseInsertCollection(t, app, "resublog", owner)
		const addr = "private.reader@example.com"

		first, err := app.db.AddEmailSubscription(coll, 0, addr, false)
		require.NoError(t, err)

		buf := captureLogs(t)
		again, err := app.db.AddEmailSubscription(coll, 0, addr, false)
		require.NoError(t, err)
		require.Equal(t, first.ID, again.ID)

		out := buf.String()
		assert.Contains(t, out, "subscriber", "the re-subscribe should still be logged")
		assert.NotContains(t, out, addr)
		assert.NotContains(t, out, "example.com")
	})
}
