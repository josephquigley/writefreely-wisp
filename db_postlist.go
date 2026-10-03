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
	"github.com/writeas/web-core/log"
)

// postListPageSize is the number of posts shown per page in the post
// management views.
const postListPageSize = 50

// postListCols are the columns loaded for post management views. Post
// content is deliberately excluded: these views never render bodies, and
// loading them makes listing a long-form blog needlessly expensive.
const postListCols = "id, slug, title, created, updated, view_count, pinned_position, collection_id, owner_id"

// prefixedPostListCols is postListCols qualified with the posts table
// alias, for queries that join another table.
const prefixedPostListCols = "p.id, p.slug, p.title, p.created, p.updated, p.view_count, p.pinned_position, p.collection_id, p.owner_id"

// GetCollectionPostsForOwner returns one page of posts belonging to the
// given collection, along with the total number of posts in it. Scheduled
// and future-dated posts are included, since this is a management view.
// page is 1-indexed.
func (db *datastore) GetCollectionPostsForOwner(collID int64, page int) (*[]PublicPost, int, error) {
	if page < 1 {
		page = 1
	}

	var total int
	err := db.QueryRow("SELECT COUNT(*) FROM posts WHERE collection_id = ?", collID).Scan(&total)
	if err != nil {
		log.Error("get collection posts for owner: count: %v", err)
		return nil, 0, err
	}

	rows, err := db.Query("SELECT "+postListCols+" FROM posts WHERE collection_id = ? ORDER BY created DESC, id DESC LIMIT ? OFFSET ?",
		collID, postListPageSize, (page-1)*postListPageSize)
	if err != nil {
		log.Error("get collection posts for owner: %v", err)
		return nil, 0, err
	}
	defer rows.Close()

	posts := []PublicPost{}
	for rows.Next() {
		p := &Post{}
		err = rows.Scan(&p.ID, &p.Slug, &p.Title, &p.Created, &p.Updated, &p.ViewCount,
			&p.PinnedPosition, &p.CollectionID, &p.OwnerID)
		if err != nil {
			log.Error("scan post list row: %v", err)
			return nil, 0, err
		}
		posts = append(posts, p.processPost())
	}
	if err = rows.Err(); err != nil {
		log.Error("post list rows: %v", err)
		return nil, 0, err
	}
	return &posts, total, nil
}

// GetAllPostsForAdmin returns one page of posts across every collection on
// the instance, newest first, along with the total post count. It is used
// only by the admin post management view. page is 1-indexed.
func (db *datastore) GetAllPostsForAdmin(page int) (*[]PublicPost, int, error) {
	if page < 1 {
		page = 1
	}

	var total int
	err := db.QueryRow("SELECT COUNT(*) FROM posts WHERE collection_id IS NOT NULL").Scan(&total)
	if err != nil {
		log.Error("get all posts for admin: count: %v", err)
		return nil, 0, err
	}

	rows, err := db.Query("SELECT "+prefixedPostListCols+", c.alias FROM posts p "+
		"INNER JOIN collections c ON c.id = p.collection_id "+
		"ORDER BY p.created DESC, p.id DESC LIMIT ? OFFSET ?",
		postListPageSize, (page-1)*postListPageSize)
	if err != nil {
		log.Error("get all posts for admin: %v", err)
		return nil, 0, err
	}
	defer rows.Close()

	posts := []PublicPost{}
	for rows.Next() {
		p := &Post{}
		var alias string
		err = rows.Scan(&p.ID, &p.Slug, &p.Title, &p.Created, &p.Updated, &p.ViewCount,
			&p.PinnedPosition, &p.CollectionID, &p.OwnerID, &alias)
		if err != nil {
			log.Error("scan admin post row: %v", err)
			return nil, 0, err
		}
		pp := p.processPost()
		pp.Collection = &CollectionObj{Collection: Collection{Alias: alias}}
		posts = append(posts, pp)
	}
	if err = rows.Err(); err != nil {
		log.Error("admin post rows: %v", err)
		return nil, 0, err
	}
	return &posts, total, nil
}
