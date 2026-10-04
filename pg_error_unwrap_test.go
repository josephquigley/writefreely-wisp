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

	"github.com/lib/pq"
)

func TestPostgresErrorChecksUnwrap(t *testing.T) {
	db := &datastore{driverName: driverPostgres}
	wrap := func(code pq.ErrorCode) error {
		return fmt.Errorf("context: %w", &pq.Error{Code: code})
	}

	if !db.isDuplicateKeyErr(wrap(postgresErrDuplicateKey)) {
		t.Error("isDuplicateKeyErr: wrapped unique_violation not recognised")
	}
	if !db.isHighLoadError(wrap(postgresErrTooManyConns)) {
		t.Error("isHighLoadError: wrapped too_many_connections not recognised")
	}
	if db.isDuplicateKeyErr(wrap("42601")) {
		t.Error("isDuplicateKeyErr: syntax_error recognised as a duplicate")
	}
}
