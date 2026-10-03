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
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
	"github.com/writeas/web-core/log"
	"github.com/writefreely/writefreely"
)

// infoToStderr sends informational logging to stderr, so that stdout
// carries only a settings command's data (`settings export > file`).
func infoToStderr() {
	log.InfoLog.SetOutput(os.Stderr)
}

var cmdSettings = cli.Command{
	Name: "settings",
	Before: func(c *cli.Context) error {
		infoToStderr()
		return nil
	},
	Usage: "show and change the settings stored in the database",
	Subcommands: []*cli.Command{
		{
			Name:  "list",
			Usage: "list every setting and its value",
			Action: func(c *cli.Context) error {
				rows, err := writefreely.SettingsList(writefreely.NewApp(c.String("c")))
				if err != nil {
					return err
				}
				tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				for _, r := range rows {
					mark := ""
					if r.Default {
						mark = "(default)"
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, r.Value, mark)
				}
				return tw.Flush()
			},
		},
		{
			Name:      "get",
			Usage:     "print one setting's value",
			ArgsUsage: "<name>",
			Action: func(c *cli.Context) error {
				if c.NArg() != 1 {
					return cli.Exit("usage: writefreely settings get <name>", 2)
				}
				r, err := writefreely.SettingGet(writefreely.NewApp(c.String("c")), c.Args().First())
				if err != nil {
					return err
				}
				fmt.Println(r.Value)
				return nil
			},
		},
		{
			Name:      "set",
			Usage:     "change one setting; running servers apply it on their next request",
			ArgsUsage: "<name> <value>",
			Action: func(c *cli.Context) error {
				if c.NArg() != 2 {
					return cli.Exit("usage: writefreely settings set <name> <value>", 2)
				}
				return writefreely.SettingSet(writefreely.NewApp(c.String("c")), c.Args().Get(0), c.Args().Get(1))
			},
		},
		{
			Name:  "export",
			Usage: "print the settings as a config.ini fragment (for downgrading)",
			Action: func(c *cli.Context) error {
				out, err := writefreely.SettingsExport(writefreely.NewApp(c.String("c")))
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			},
		},
	},
}
