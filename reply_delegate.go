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
	"strings"

	"github.com/writeas/web-core/activitystreams"
	"github.com/writeas/web-core/log"
)

// A blog's reply delegate is a fediverse account, on some other instance,
// that every federated post from that blog addresses as a mention.
//
// WriteFreely federates outward only: it delivers Create activities and
// handles Follow, but it has no inbox handling for replies and no interface
// that could display one. A reply to a blog post is delivered, stored by
// nobody, and seen by no one. There is no ActivityPub mechanism for
// redirecting inbound activity to another actor — an activity goes to the
// inboxes of the actors named in `to`, `cc` and the `tag` mentions, and
// nowhere else.
//
// So the delegate works on the only lever that exists: addressing. Naming an
// account in the post's mentions puts that account in the conversation from
// the start, and every major implementation (Mastodon, GoToSocial, Mbin)
// pre-fills a reply's addressing from the parent post's author plus the
// parent's mentions. A reply to a blog post therefore reaches the delegate
// through the replier's own instance, by ordinary means, with no cooperation
// from us beyond having addressed the post correctly in the first place.
//
// The mention is carried in `tag` and `cc` only; nothing is added to the
// rendered content. Receivers build their mention records from the `tag`
// array on ingest rather than by scraping content, so notification and
// reply pre-fill both work without a handle appearing at the foot of every
// article.

// normalizeReplyDelegate cleans up a submitted delegate handle and reports
// whether it is usable. The empty string is valid and means "no delegate".
//
// The accepted shape is a full fediverse handle, `user@host`, with or without
// the leading `@`; a stored value always keeps the leading `@` so it matches
// what mention resolution elsewhere expects. A local-only `@user` is rejected
// rather than silently stored: the whole point of a delegate is that it lives
// on an instance that can hold a conversation, which this one cannot.
func normalizeReplyDelegate(handle string) (string, bool) {
	trimmed := strings.TrimSpace(handle)
	if trimmed == "" {
		return "", true
	}
	if !strings.HasPrefix(trimmed, "@") {
		trimmed = "@" + trimmed
	}
	// mentionReg is what the post renderer uses to find mentions in content,
	// so matching against it is what keeps a configured delegate and a typed
	// mention the same kind of thing.
	if mentionReg.FindString(trimmed) != trimmed {
		return "", false
	}
	return trimmed, true
}

// addReplyDelegateMention adds the delegate to an object's addressing, and
// reports whether it changed anything.
//
// Deduplication is the reason this is separate from the lookup: an author who
// also writes the delegate's handle into a post would otherwise get the actor
// twice in `cc` and two identical Mention tags, and federatePost walks the tag
// list delivering one copy of the activity per Mention — so a duplicate tag is
// a duplicate delivery, not just untidy JSON.
func addReplyDelegateMention(o *activitystreams.Object, handle, actorIRI string) bool {
	if o == nil || handle == "" || actorIRI == "" {
		return false
	}
	for _, t := range o.Tag {
		if t.Type == "Mention" && t.HRef == actorIRI {
			return false
		}
	}
	for _, cc := range o.CC {
		if cc == actorIRI {
			// Addressed already but not tagged: still tag it, since the tag
			// is what receivers build their mention records from.
			o.Tag = append(o.Tag, activitystreams.Tag{Type: "Mention", HRef: actorIRI, Name: handle})
			return true
		}
	}
	o.CC = append(o.CC, actorIRI)
	o.Tag = append(o.Tag, activitystreams.Tag{Type: "Mention", HRef: actorIRI, Name: handle})
	return true
}

// applyReplyDelegate resolves the collection's configured delegate and adds it
// to the object's addressing. A missing delegate, an unresolvable one, or a
// call with no datastore behind it all leave the object untouched: a blog post
// that cannot name its delegate is still a perfectly good blog post, and
// federating it without the mention is better than not federating it.
func applyReplyDelegate(app *App, c *Collection, o *activitystreams.Object) {
	if app == nil || c == nil || o == nil || c.ReplyDelegate == "" {
		return
	}
	if app.db == nil {
		return
	}
	// Resolution is a database read for any handle seen before, and a
	// webfinger lookup only the first time — the same path a mention typed
	// into a post takes.
	actorIRI, err := app.db.GetProfilePageFromHandle(app, c.ReplyDelegate)
	if err != nil || actorIRI == "" {
		log.Info("Couldn't resolve reply delegate '%s' for blog '%s'", c.ReplyDelegate, c.Alias)
		return
	}
	addReplyDelegateMention(o, c.ReplyDelegate, actorIRI)
}
