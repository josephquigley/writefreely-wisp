/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/writefreely/writefreely/config"
)

// failingDriver is a minimal database/sql driver whose Exec fails for any
// statement containing failOn, and records every statement it was given.
type failingDriver struct {
	mu     sync.Mutex
	failOn string
	execs  []string
}

func (d *failingDriver) Open(string) (driver.Conn, error) { return &failingConn{d}, nil }

type failingConn struct{ d *failingDriver }

func (c *failingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (c *failingConn) Close() error              { return nil }
func (c *failingConn) Begin() (driver.Tx, error) { return nil, errors.New("tx not supported") }
func (c *failingConn) Exec(q string, _ []driver.Value) (driver.Result, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.execs = append(c.d.execs, q)
	if strings.Contains(q, c.d.failOn) {
		return nil, errors.New("injected failure")
	}
	return driver.RowsAffected(0), nil
}

var registerFailingDriver sync.Once
var theFailingDriver = &failingDriver{}

func TestAdminInitDatabaseStopsOnSchemaError(t *testing.T) {
	registerFailingDriver.Do(func() { sql.Register("wf_failing_initdb", theFailingDriver) })
	// Fail on a table that is not the first in schema.sql, so the test proves
	// a mid-schema failure is not swallowed.
	theFailingDriver.failOn = "`appcontent`"
	theFailingDriver.execs = nil

	db, err := sql.Open("wf_failing_initdb", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	cfg := config.New()
	cfg.Database.Type = driverMySQL
	app := &App{cfg: cfg, db: &datastore{DB: db, driverName: driverMySQL}}

	err = adminInitDatabase(app)
	if err == nil {
		t.Fatal("adminInitDatabase returned nil after a CREATE TABLE failed")
	}
	if !strings.Contains(err.Error(), "appcontent") || !strings.Contains(err.Error(), "injected failure") {
		t.Errorf("error should name the table and the cause, got: %v", err)
	}
	last := theFailingDriver.execs[len(theFailingDriver.execs)-1]
	if !strings.Contains(last, "`appcontent`") {
		t.Errorf("statements ran after the failure; last was: %.60q", last)
	}
}
