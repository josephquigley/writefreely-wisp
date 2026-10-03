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
	"strings"

	"github.com/writefreely/writefreely/config"
)

// pqValueEscaper escapes the two characters that are special inside a
// single-quoted lib/pq connection string value.
var pqValueEscaper = strings.NewReplacer(`\`, `\\`, `'`, `\'`)

// pqQuote renders v as a single-quoted lib/pq key=value value. Quoting every
// value keeps empty strings, spaces, quotes, backslashes and '=' intact.
func pqQuote(v string) string {
	return "'" + pqValueEscaper.Replace(v) + "'"
}

// postgresDSN builds a lib/pq key=value connection string from the database
// config, quoting each value so that none of them can be misparsed or spill
// into another key.
//
// It also pins the session TimeZone to UTC (lib/pq sends unrecognised keys as
// startup parameters, which override the server's and database's defaults).
// The schema's time columns are TIMESTAMP without time zone, which store
// NOW() as the session's wall clock and which lib/pq reads back as UTC, so any
// other session zone would shift every SQL-stamped time. See pgTimeArgs for
// the matching rule on times bound from Go.
func postgresDSN(cfg config.DatabaseCfg, sslmode string) string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s timezone='UTC'",
		pqQuote(cfg.Host), cfg.Port, pqQuote(cfg.User), pqQuote(cfg.Password), pqQuote(cfg.Database), pqQuote(sslmode))
}
