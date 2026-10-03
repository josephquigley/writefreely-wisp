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

// exactMatchCollations makes MySQL compare tokens, codes and remote IRIs
// exactly, as SQLite already does.
//
// schema.sql creates its tables as latin1, whose default collation
// (latin1_swedish_ci) ignores case, and tables added by later migrations
// inherit the database's default, which is case-insensitive too on every
// stock MySQL and MariaDB. So on MySQL:
//
//   - a token, invite code, OAuth state or subscriber ID matched in any case;
//   - an OAuth provider's user ID matched another user's that differed only
//     in case;
//   - two remote actors whose IRIs differed only in case collided on the
//     remoteusers unique key;
//   - remoteusers could not store any character outside latin1, so an actor
//     with one in its inbox or profile URL failed to save under strict mode
//     and was mangled to '?' without it.
//
// Each column below keeps its type, nullability and default and gets a
// binary collation. remoteusers and remoteuserkeys hold IRIs and are
// converted to utf8mb4 as well. Converting latin1 to utf8mb4 is lossless,
// and a binary collation is stricter than the case-insensitive one it
// replaces, so no existing row can collide under a unique key afterwards.
//
// emailsubscribers.email and posts.slug keep their case-insensitive
// collation.
//
// MySQL commits each ALTER on its own, so there is no transaction to roll
// back. Every statement states the column's whole definition and can run
// again, so a migration that stops part way through is finished by running
// it again.
//
// The utf8mb4 key on remoteusers.actor_id is 1020 bytes, which needs the
// DYNAMIC row format (the default since MySQL 5.7 and MariaDB 10.2).
func exactMatchCollations(db *datastore) error {
	// Only run this migration on MySQL databases
	if db.driverName != driverMySQL {
		return nil
	}

	for _, q := range []string{
		`ALTER TABLE userinvites MODIFY id CHAR(6) CHARACTER SET latin1 COLLATE latin1_bin NOT NULL`,
		`ALTER TABLE usersinvited MODIFY invite_id CHAR(6) CHARACTER SET latin1 COLLATE latin1_bin NOT NULL`,
		`ALTER TABLE posts MODIFY modify_token CHAR(32) CHARACTER SET latin1 COLLATE latin1_bin NULL DEFAULT NULL`,
		`ALTER TABLE password_resets MODIFY token CHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL`,
		`ALTER TABLE emailsubscribers
			MODIFY id CHAR(8) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
			MODIFY token CHAR(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL`,
		`ALTER TABLE oauth_client_states
			MODIFY state VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
			MODIFY invite_code CHAR(6) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL DEFAULT NULL`,
		`ALTER TABLE oauth_users MODIFY remote_user_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL`,
		`ALTER TABLE remoteusers CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
		`ALTER TABLE remoteuserkeys CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
