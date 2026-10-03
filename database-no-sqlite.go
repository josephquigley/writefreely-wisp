//go:build !sqlite && !wflib
// +build !sqlite,!wflib

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
	"github.com/go-sql-driver/mysql"
	"github.com/writeas/web-core/log"
)

func (db *datastore) isDuplicateKeyErr(err error) bool {
	switch db.driverName {
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
	case driverPostgres:
		// MySQL's 1267 (illegal mix of collations) has no Postgres analogue.
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
		return isPostgresErrCode(err, pgErrTooManyConnections)
	}

	return false
}
