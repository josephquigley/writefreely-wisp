/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package main

import (
	"os"

	"github.com/urfave/cli/v2"
	"github.com/writefreely/writefreely"
)

var (
	cmdImages cli.Command = cli.Command{
		Name:  "images",
		Usage: "uploaded image tools",
		Subcommands: []*cli.Command{
			&cmdImagesSync,
		},
	}

	cmdImagesSync cli.Command = cli.Command{
		Name:  "sync",
		Usage: "Copy every uploaded image from the uploads directory into the configured object store",
		Description: "Reads post_images and copies each image from this node's uploads directory into\n" +
			"the bucket named under [storage], verifying SHA-256 on both sides. Safe to run\n" +
			"again: images already in the bucket with the right bytes are not copied twice.\n" +
			"Nothing in the uploads directory is changed or deleted.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "to",
				Usage:    "destination store: s3",
				Required: true,
			},
		},
		Action: imagesSyncAction,
	}
)

func imagesSyncAction(c *cli.Context) error {
	app := writefreely.NewApp(c.String("c"))
	return writefreely.SyncImages(app, c.String("to"), os.Stdout)
}
