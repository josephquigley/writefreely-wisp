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

// Text that reaches the database from a member or from a remote server is
// made storable here, once, at the point it enters the application.
//
// MySQL outside strict mode silently truncated an over-long value and mangled
// bytes it could not store. Postgres refuses both: a value longer than its
// varchar(n) column fails with 22001, and a NUL byte or an invalid UTF-8
// sequence fails with 22021. Either way the request used to end in a 500.
// SQLite stores anything, so this is a no-op there in practice, and the same
// code runs on every engine.

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/writeas/impart"
	"github.com/writeas/web-core/activitystreams"
	"github.com/writefreely/writefreely/parse"
)

const (
	// Column widths from the schema (postgres.sql, and the same widths in the
	// MySQL and SQLite schemas). Counted in characters, which is what
	// varchar(n) counts on both MySQL and Postgres.
	// collections.title and collections.description are collMaxLengthTitle
	// and collMaxLengthDescription, in collections.go.
	postMaxLengthTitle         = 160 // posts.title
	postMaxLengthLanguage      = 2   // posts.language
	postImageMaxLengthFilename = 255 // post_images.filename
	appContentMaxLengthTitle   = 255 // appcontent.title
	remoteUserKeyMaxLengthID   = 255 // remoteuserkeys.id
	oauthRemoteUserIDMaxLength = 128 // oauth_users.remote_user_id

	// emailMaxLength is the longest address that can be valid (RFC 5321's
	// 256-octet path, less its angle brackets). emailsubscribers.email is
	// varchar(255), so anything longer is refused rather than truncated.
	emailMaxLength = 254
)

// sanitizeDBText returns s as text every supported database will store:
// invalid UTF-8 sequences become U+FFFD and NUL bytes are removed.
func sanitizeDBText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.ReplaceAll(s, "\x00", "")
}

// sanitizeDBTextPtr applies sanitizeDBText in place. A nil pointer is left
// alone, so optional fields keep meaning "not supplied".
func sanitizeDBTextPtr(s *string) {
	if s != nil {
		*s = sanitizeDBText(*s)
	}
}

// boundedDBText sanitizes s and truncates it to at most n characters, on a
// rune boundary, for a varchar(n) column.
func boundedDBText(s string, n int) string {
	return parse.Truncate(sanitizeDBText(s), n)
}

// isValidDBText reports whether s would come back from sanitizeDBText
// unchanged. Use it where a value must be refused rather than repaired.
func isValidDBText(s string) bool {
	return sanitizeDBText(s) == s
}

// sanitizeForStorage makes every free-text field of a submitted post
// storable, and truncates the title to its column. It is called at the top of
// CreatePost and UpdateOwnedPost, which every way of writing a post goes
// through: the API, the web editor, and account import.
func (p *SubmittedPost) sanitizeForStorage() {
	if p == nil {
		return
	}
	if p.Title != nil {
		t := boundedDBText(*p.Title, postMaxLengthTitle)
		p.Title = &t
	}
	if p.Content != nil {
		c := sanitizeDBText(*p.Content)
		p.Content = &c
	}
	if p.Slug != nil {
		s := sanitizeDBText(*p.Slug)
		p.Slug = &s
	}
	if p.Language.Valid {
		p.Language.String = boundedDBText(p.Language.String, postMaxLengthLanguage)
	}
}

// sanitizeForStorage makes the free-text fields of a collection update
// storable. Title and description are truncated to their columns by
// UpdateCollection itself; the others are text columns.
func (c *SubmittedCollection) sanitizeForStorage() {
	if c == nil {
		return
	}
	for _, s := range []*string{
		c.Title, c.Description, c.StyleSheet, c.Script, c.Signature,
		c.Monetization, c.Verification, c.LetterReply, c.ReplyDelegate,
	} {
		sanitizeDBTextPtr(s)
	}
}

// sanitizeRemoteActor makes the strings of an actor fetched from another
// server storable. Of these, remoteusers stores the ID, inboxes and URL, and
// remoteuserkeys the key ID and PEM; the display name and summary are not
// stored today but are cleaned too, so a future column for them starts safe.
func sanitizeRemoteActor(a *activitystreams.Person) {
	if a == nil {
		return
	}
	for _, s := range []*string{
		&a.ID, &a.Type, &a.Inbox, &a.Outbox, &a.PreferredUsername, &a.URL,
		&a.Name, &a.Following, &a.Followers, &a.Summary,
		&a.PublicKey.Owner, &a.PublicKey.PublicKeyPEM, &a.Endpoints.SharedInbox,
		&a.Icon.Type, &a.Icon.MediaType, &a.Icon.URL, &a.Monetization,
	} {
		sanitizeDBTextPtr(s)
	}
	// remoteuserkeys.id is varchar(255) and is never read back, only written
	// as the row's key, so truncating an over-long one loses nothing.
	a.PublicKey.ID = boundedDBText(a.PublicKey.ID, remoteUserKeyMaxLengthID)
}

// fetchRemoteActorForStorage fetches a remote actor, as newRemoteActor does,
// and makes the strings that handle resolution writes to remoteusers
// storable. Those callers insert the fetched inboxes and URL directly rather
// than going through unmarshalActor, so they need the same cleaning here.
// remoteusers' string columns are text on Postgres, so nothing is truncated.
func fetchRemoteActorForStorage(app *App, actorIRI string) (remoteActorInfo, error) {
	a, err := newRemoteActor(app, actorIRI)
	if err != nil {
		return a, err
	}
	return remoteActorInfo{
		iri:         sanitizeDBText(a.iri),
		inbox:       sanitizeDBText(a.inbox),
		sharedInbox: sanitizeDBText(a.sharedInbox),
		url:         sanitizeDBText(a.url),
	}, nil
}

// errOAuthRemoteUserIDUnstorable is returned for an OAuth account ID that
// oauth_users.remote_user_id cannot hold exactly.
var errOAuthRemoteUserIDUnstorable = impart.HTTPError{Status: http.StatusBadRequest, Message: "The sign-in provider returned an account ID this site cannot store."}

// isStorableOAuthRemoteUserID reports whether id fits oauth_users.remote_user_id
// unchanged. The column is the key a returning OAuth user is found by, so an
// ID that does not fit is refused, never repaired: truncating it (as MySQL
// outside strict mode would) could map two provider accounts to one local
// user, and Postgres would fail the insert with a 500.
func isStorableOAuthRemoteUserID(id string) bool {
	return isValidDBText(id) && utf8.RuneCountInString(id) <= oauthRemoteUserIDMaxLength
}
