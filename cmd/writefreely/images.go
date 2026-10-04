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
			&cmdImagesMetadata,
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

	cmdImagesMetadata cli.Command = cli.Command{
		Name:  "metadata",
		Usage: "Give images already in the object store the caching headers new uploads get",
		Description: "Reads post_images and, for each image in the bucket named under [storage], sets\n" +
			"Cache-Control and, for SVG, Content-Disposition: attachment, by copying the object\n" +
			"onto itself. Content-Type and the bytes are kept. Run it once before serving the\n" +
			"bucket directly with image_url_base. Safe to run again: images that already\n" +
			"carry the headers are left alone.",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "list what would change, and change nothing",
			},
		},
		Action: imagesMetadataAction,
	}
)

func imagesSyncAction(c *cli.Context) error {
	app := writefreely.NewApp(c.String("c"))
	return writefreely.SyncImages(app, c.String("to"), os.Stdout)
}

func imagesMetadataAction(c *cli.Context) error {
	app := writefreely.NewApp(c.String("c"))
	return writefreely.RefreshImageMetadata(app, c.Bool("dry-run"), os.Stdout)
}
