package writefreely

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	wflog "github.com/writeas/web-core/log"
)

// dupKeyDriver is a database driver whose every write fails with MySQL's
// duplicate-key error and whose every read fails, so AddEmailSubscription
// takes its re-subscribe path without a live database.
type dupKeyDriver struct{}

type dupKeyConn struct{}

var errDupKeyRead = errors.New("fake read failure")

func (dupKeyDriver) Open(string) (driver.Conn, error) { return dupKeyConn{}, nil }
func (dupKeyConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (dupKeyConn) Close() error              { return nil }
func (dupKeyConn) Begin() (driver.Tx, error) { return nil, errors.New("not implemented") }
func (dupKeyConn) ExecContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	return nil, &mysql.MySQLError{Number: mySQLErrDuplicateKey, Message: "Duplicate entry"}
}
func (dupKeyConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return nil, errDupKeyRead
}

func init() { sql.Register("wf-dupkey-test", dupKeyDriver{}) }

// Re-subscribing takes the duplicate-key path, which used to log the
// subscriber's address in plain text.
func TestResubscribeDoesNotLogEmailAddress(t *testing.T) {
	sdb, err := sql.Open("wf-dupkey-test", "")
	assert.NoError(t, err)
	defer sdb.Close()
	ds := &datastore{DB: sdb}

	var buf bytes.Buffer
	oldInfo, oldErr := wflog.InfoLog.Writer(), wflog.ErrorLog.Writer()
	wflog.InfoLog.SetOutput(&buf)
	wflog.ErrorLog.SetOutput(&buf)
	defer func() {
		wflog.InfoLog.SetOutput(oldInfo)
		wflog.ErrorLog.SetOutput(oldErr)
	}()

	const addr = "private.reader@example.com"
	_, _ = ds.AddEmailSubscription(1, 0, addr, false)

	out := buf.String()
	assert.Contains(t, out, "Duplicate subscriber", "the duplicate path should still be logged")
	assert.NotContains(t, out, addr)
	assert.NotContains(t, out, "example.com")
}
