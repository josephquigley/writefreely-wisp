//go:build sqlite && !wflib
// +build sqlite,!wflib

/*
 * Copyright © 2019-2020 Musing Studio LLC.
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
	"regexp"

	"github.com/go-sql-driver/mysql"
	"github.com/mattn/go-sqlite3"
	"github.com/writeas/web-core/log"
)

func init() {
	SQLiteEnabled = true

	regex := func(re, s string) (bool, error) {
		return regexp.MatchString(re, s)
	}
	sql.Register("sqlite3_with_regex", &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("regexp", regex, true)
		},
	})
}

func (db *datastore) isDuplicateKeyErr(err error) bool {
	switch db.driverName {
	case driverSQLite:
		if err, ok := err.(sqlite3.Error); ok {
			return err.Code == sqlite3.ErrConstraint
		}
	case driverMySQL:
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			return mysqlErr.Number == mySQLErrDuplicateKey
		}
	case driverPostgres:
		return isPostgresErrCode(err, pgErrUniqueViolation)
	default:
		log.Error("isDuplicateKeyErr: failed check for unrecognized driver '%s'", db.driverName)
	}

	return false
}

func (db *datastore) isIgnorableError(err error) bool {
	switch db.driverName {
	case driverMySQL:
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			return mysqlErr.Number == mySQLErrCollationMix
		}
	case driverSQLite, driverPostgres:
		// MySQL's 1267 (illegal mix of collations) has no analogue on
		// either. Naming them here keeps a real error on these engines from
		// also logging a spurious "unrecognized driver" line.
		return false
	default:
		log.Error("isIgnorableError: failed check for unrecognized driver '%s'", db.driverName)
	}

	return false
}

func (db *datastore) isHighLoadError(err error) bool {
	switch db.driverName {
	case driverMySQL:
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			return mysqlErr.Number == mySQLErrMaxUserConns || mysqlErr.Number == mySQLErrTooManyConns
		}
	case driverPostgres:
		return isPostgresHighLoadErr(err)
	}

	return false
}
