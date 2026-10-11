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
	"strings"
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
					var marks []string
					if r.Default {
						marks = append(marks, "(default)")
					}
					if r.Locked {
						marks = append(marks, "(locked in config.ini)")
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, r.Value, strings.Join(marks, " "))
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
				if r.Locked {
					log.Info("%s is locked in config.ini (locked_settings); edit it there and restart.", r.Name)
				}
				fmt.Println(r.Value)
				return nil
			},
		},
		{
			Name:      "set",
			Usage:     "change one setting; running servers apply it on their next request (refused for a setting config.ini locks)",
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
