//go:build sqlite && !wflib
// +build sqlite,!wflib

/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package main

import (
	"github.com/urfave/cli/v2"
	"github.com/writefreely/writefreely"
)

// `db copy` exists only in builds that can read SQLite.
func init() {
	cmdDB.Subcommands = append(cmdDB.Subcommands, &cmdDBCopy)
}

var cmdDBCopy = cli.Command{
	Name:  "copy",
	Usage: "Copy a SQLite database into the configured, freshly initialised Postgres database",
	Description: "Copies every table of a SQLite database at the current migration version into the\n" +
		"empty Postgres database named in the configuration (run `db init` on it first), in one\n" +
		"transaction, then verifies row counts and checksums before committing. The report\n" +
		"names tables, counts and row IDs only.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "from",
			Usage:    "Source database, as sqlite:`PATH`",
			Required: true,
		},
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "Read and convert everything, write nothing, and print the per-table report",
		},
		&cli.BoolFlag{
			Name:  "verify-only",
			Usage: "Compare an existing copy with the source, and exit non-zero on any difference",
		},
	},
	Action: copyDBAction,
}

func copyDBAction(c *cli.Context) error {
	app := writefreely.NewApp(c.String("c"))
	return writefreely.CopySQLiteDatabase(app, writefreely.DBCopyOptions{
		From:       c.String("from"),
		DryRun:     c.Bool("dry-run"),
		VerifyOnly: c.Bool("verify-only"),
	})
}
