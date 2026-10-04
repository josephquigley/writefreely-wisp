/*
 * Copyright © 2026 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package migrations

func supportPostImages(db *datastore) error {
	t, err := db.Begin()
	if err != nil {
		t.Rollback()
		return err
	}

	_, err = t.Exec(`CREATE TABLE post_images (
    id       ` + db.typeVarChar(6) + ` not null,
    owner_id ` + db.typeInt() + ` not null,
    post_id  ` + db.typeChar(16) + ` null,
    sha256   ` + db.typeChar(64) + ` not null,
    path     ` + db.typeVarChar(255) + ` not null,
    filename ` + db.typeVarChar(255) + ` not null,
    mime     ` + db.typeVarChar(64) + ` not null,
    size     ` + db.typeInt() + ` not null,
    created  ` + db.typeDateTime() + ` not null,
    constraint pi_owner_sum
        unique (owner_id, sha256),
    constraint pi_path
        unique (path),
    PRIMARY KEY (id)
)`)
	if err != nil {
		t.Rollback()
		return err
	}

	_, err = t.Exec(`CREATE INDEX post_images_post_id on post_images (post_id)`)
	if err != nil {
		t.Rollback()
		return err
	}

	err = t.Commit()
	if err != nil {
		t.Rollback()
		return err
	}

	return nil
}
