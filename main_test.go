package writefreely

import (
	"context"
	"database/sql"
	"encoding/gob"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	uuid "github.com/nu7hatch/gouuid"
	"github.com/stretchr/testify/assert"
)

var testDB *sql.DB

type ScopedTestBody func(*sql.DB)

// TestMain provides testing infrastructure within this package.
// testMaxBlogs is the blog cap test configs use. config.New() allows one
// blog, which the one CreateUser makes already fills, and CreateCollection
// enforces the cap, so fixtures that create blogs need room. It stays finite
// so the cap is still in force in those tests.
const testMaxBlogs = 10

func TestMain(m *testing.M) {
	rand.Seed(time.Now().UTC().UnixNano())
	gob.Register(&User{})

	// Federation tests deliver to httptest servers on 127.0.0.1, which
	// safeDialContext refuses. The ruleset itself is tested directly in
	// ssrf_guard_test.go, and TestActivityPubClientRefusesLoopback checks
	// the production dialer is wired in.
	activityPubDialContext = (&net.Dialer{}).DialContext

	if runMySQLTests() {
		var err error

		testDB, err = initMySQL(os.Getenv("WF_USER"), os.Getenv("WF_PASSWORD"), os.Getenv("WF_DB"), os.Getenv("WF_HOST"))
		if err != nil {
			fmt.Println(err)
			return
		}
	}

	// See harness_app_test.go. Asking for MySQL or Postgres and not
	// reaching it is a failed run, not a skipped one.
	if err := initTestDBEngine(); err != nil {
		fmt.Println("test database harness:", err)
		os.Exit(1)
	}

	code := m.Run()
	if runMySQLTests() {
		if closeErr := testDB.Close(); closeErr != nil {
			fmt.Println(closeErr)
		}
	}
	if testPGAdmin != nil {
		if closeErr := testPGAdmin.Close(); closeErr != nil {
			fmt.Println(closeErr)
		}
	}
	if testMySQLAdmin != nil {
		if closeErr := testMySQLAdmin.Close(); closeErr != nil {
			fmt.Println(closeErr)
		}
	}
	os.Exit(code)
}

func runMySQLTests() bool {
	return len(os.Getenv("TEST_MYSQL")) > 0
}

func initMySQL(dbUser, dbPassword, dbName, dbHost string) (*sql.DB, error) {
	if dbUser == "" || dbPassword == "" {
		return nil, errors.New("database user or password not set")
	}
	if dbHost == "" {
		dbHost = "localhost"
	}
	if dbName == "" {
		dbName = "writefreely"
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:3306)/%s?charset=utf8mb4&parseTime=true", dbUser, dbPassword, dbHost, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	if err := ensureMySQL(db); err != nil {
		return nil, err
	}
	return db, nil
}

func ensureMySQL(db *sql.DB) error {
	if err := db.Ping(); err != nil {
		return err
	}
	db.SetMaxOpenConns(250)
	return nil
}

// withTestDB provides a scoped database connection. Under
// WF_TEST_DB_TYPE=postgres or mysql the connection is to a fresh wf_test_*
// database with the schema loaded (engineTestApp), dropped when the test
// ends; otherwise it is a copy of the MySQL reference database
// (newTestDatabase).
func withTestDB(t *testing.T, testBody ScopedTestBody) {
	if e := engineTestApp(t, nil); e != nil {
		testBody(e.db.DB)
		return
	}
	db, cleanup, err := newTestDatabase(testDB,
		os.Getenv("WF_USER"),
		os.Getenv("WF_PASSWORD"),
		os.Getenv("WF_DB"),
		os.Getenv("WF_HOST"),
	)
	assert.NoError(t, err)
	defer func() {
		assert.NoError(t, cleanup())
	}()

	testBody(db)
}

// newTestDatabase creates a new temporary test database. When a test
// database connection is returned, it will have created a new database and
// initialized it with tables from a reference database.
func newTestDatabase(base *sql.DB, dbUser, dbPassword, dbName, dbHost string) (*sql.DB, func() error, error) {
	var err error
	var baseName = dbName

	if baseName == "" {
		row := base.QueryRow("SELECT DATABASE()")
		err := row.Scan(&baseName)
		if err != nil {
			return nil, nil, err
		}
	}
	tUUID, _ := uuid.NewV4()
	suffix := strings.Replace(tUUID.String(), "-", "_", -1)
	newDBName := baseName + suffix
	_, err = base.Exec("CREATE DATABASE " + newDBName)
	if err != nil {
		return nil, nil, err
	}
	newDB, err := initMySQL(dbUser, dbPassword, newDBName, dbHost)
	if err != nil {
		return nil, nil, err
	}

	rows, err := base.Query("SHOW TABLES IN " + baseName)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, nil, err
		}
		query := fmt.Sprintf("CREATE TABLE %s LIKE %s.%s", tableName, baseName, tableName)
		if _, err := newDB.Exec(query); err != nil {
			return nil, nil, err
		}
	}

	cleanup := func() error {
		if closeErr := newDB.Close(); closeErr != nil {
			fmt.Println(closeErr)
		}

		_, err = base.Exec("DROP DATABASE " + newDBName)
		return err
	}
	return newDB, cleanup, nil
}

func countRows(t *testing.T, ctx context.Context, db *sql.DB, count int, query string, args ...interface{}) {
	var returned int
	err := db.QueryRowContext(ctx, query, args...).Scan(&returned)
	assert.NoError(t, err, "error executing query %s and args %s", query, args)
	assert.Equal(t, count, returned, "unexpected return count %d, expected %d from %s and args %s", returned, count, query, args)
}
