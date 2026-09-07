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
	"database/sql"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/mux"
	"github.com/writeas/impart"

	"github.com/writeas/web-core/activitystreams"
	"github.com/writeas/web-core/log"
)

// A blog's reply delegate is a fediverse account, on some other instance,
// that every federated post from that blog addresses as a mention — provided
// that account follows the blog. See "Consent" below for why the follow is
// required and what happens when it is missing.
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

// applyReplyDelegate resolves the collection's configured delegate, checks
// that it follows the blog, and adds it to the object's addressing. A missing
// delegate, an unresolvable one, one that does not follow the blog, or a call
// with no datastore behind it all leave the object untouched: a blog post that
// cannot name its delegate is still a perfectly good blog post, and federating
// it without the mention is better than not federating it.
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
	// Consent gate. Mentioning an account that has not asked for this blog's
	// posts is unsolicited delivery to somebody else's inbox, so a delegate
	// that does not follow the blog is treated exactly like no delegate at
	// all: the post federates, unaddressed, as it did before the setting
	// existed. A read that fails is treated as "not following" for the same
	// reason — an error is not consent.
	follows, err := replyDelegateFollowsCollection(app, c, actorIRI)
	if err != nil || !follows {
		log.Info("Reply delegate '%s' does not follow blog '%s'; federating without the mention", c.ReplyDelegate, c.Alias)
		return
	}
	addReplyDelegateMention(o, c.ReplyDelegate, actorIRI)
}

// Consent: a delegate must follow the blog before it is mentioned
//
// Addressing is delivery. Every handle put in `cc` and `tag` gets a copy of
// the activity pushed to its inbox, which means a setting that accepts any
// handle its owner types is a setting that lets one blog send unsolicited
// mail to a stranger, on every post, forever, with the stranger's own
// instance rendering it as a mention from someone they never heard of. The
// delegate was written for the case where the blog's owner also owns the
// other account, but nothing in it said so, and nothing checked.
//
// Following is the check. It is the one signal ActivityPub already carries
// that means "I want this blog's posts", it is recorded locally in
// `remotefollows` when the Follow is accepted, and the account can withdraw
// it at any time by unfollowing — at which point the mentions stop, without
// the blog's owner being involved. An owner delegating to their own account
// follows their own blog once and is done; an owner naming somebody else's
// account gets nothing until that person agrees.
//
// The check is a local database read, never a network call: whether an actor
// follows this collection is a fact this instance already holds.

// replyDelegateStatus is what the blog's Customize page reports about the
// configured delegate. The zero value is deliberately the "nothing set" case.
type replyDelegateStatus string

const (
	// replyDelegateUnset: no delegate configured. Nothing is added to posts.
	replyDelegateUnset replyDelegateStatus = "unset"
	// replyDelegateNotFollowing: this blog has no evidence that the delegate
	// follows it, so posts are federating without the mention.
	//
	// "No evidence" deliberately covers the unresolved case too — a handle
	// this instance has never turned into an actor cannot be shown to follow
	// anything, and reporting that separately would be a distinction without
	// a difference to the owner: either way the mention is not being sent.
	// Saving the settings resolves the handle (see warmReplyDelegate), so a
	// delegate normally arrives here already resolved.
	replyDelegateNotFollowing replyDelegateStatus = "not_following"
	// replyDelegateFollowing: resolved and following. Posts carry the mention.
	replyDelegateFollowing replyDelegateStatus = "following"
	// replyDelegateUnreachable: a lookup was attempted and failed. Distinct
	// from replyDelegateUnresolved, which only means nobody has tried yet:
	// one is a wrong handle or an unreachable instance and needs the owner to
	// act, the other is the ordinary state of a delegate set a minute ago.
	replyDelegateUnreachable replyDelegateStatus = "unreachable"
	// replyDelegateInvalid: what was typed is not a full fediverse handle.
	// Only the Check button can produce this, since a saved delegate has
	// already been through normalizeReplyDelegate.
	replyDelegateInvalid replyDelegateStatus = "invalid"
)

// replyDelegateStatusOf derives the reported status from the three facts the
// caller has gathered. Kept separate from the lookups so the state machine is
// testable without a datastore or a network.
func replyDelegateStatusOf(handle, actorIRI string, follows bool) replyDelegateStatus {
	if handle == "" {
		return replyDelegateUnset
	}
	if actorIRI == "" || !follows {
		return replyDelegateNotFollowing
	}
	return replyDelegateFollowing
}

// collectionFediverseHandle is the blog's own handle, the thing a delegate has
// to follow. It is only ever used in text shown to the blog's owner.
func collectionFediverseHandle(c *Collection) string {
	if c == nil {
		return ""
	}
	host := c.hostName
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		host = u.Host
	}
	return "@" + c.Alias + "@" + host
}

// replyDelegateStatusMessage is the sentence shown under the delegate box. It
// lives here rather than in the template because the Customize page renders it
// on load and the Check button replaces it over XHR, and two copies of this
// wording would drift apart.
func replyDelegateStatusMessage(status replyDelegateStatus, handle, blogHandle string) string {
	switch status {
	case replyDelegateFollowing:
		return handle + " follows this blog. New posts will mention it."
	case replyDelegateNotFollowing:
		return handle + " does not follow this blog yet, so posts are not mentioning it. Sign in to that account, follow " + blogHandle + ", then check again."
	case replyDelegateUnreachable:
		return "Couldn't look up " + handle + ". Check the spelling, and that its instance is reachable from here."
	case replyDelegateInvalid:
		return "That is not a full fediverse handle. It needs the instance too, like @you@social.example."
	default:
		return ""
	}
}

// replyDelegateFollowsCollection reports whether an actor follows this blog.
// A follow this instance accepted is a row in remotefollows, so the answer is
// local; an actor we have never heard of simply has no row.
func replyDelegateFollowsCollection(app *App, c *Collection, actorIRI string) (bool, error) {
	if app == nil || app.db == nil || c == nil || actorIRI == "" {
		return false, nil
	}
	var one int64
	err := app.db.QueryRow(`SELECT 1
FROM remotefollows f
INNER JOIN remoteusers u
  ON f.remote_user_id = u.id
WHERE f.collection_id = ? AND u.actor_id = ?`, c.ID, actorIRI).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		log.Error("Couldn't check whether reply delegate '%s' follows blog '%s': %v", actorIRI, c.Alias, err)
		return false, err
	}
	return true, nil
}

// replyDelegateState answers "what should the owner be told about this
// delegate", and is the only place the two lookups are sequenced.
//
// `resolve` picks how hard it tries to turn the handle into an actor. Rendering
// the settings page passes false and reads only what is already cached, because
// a page load must not wait on somebody else's server. The Check button passes
// true, which permits the webfinger call — that is the entire point of a button
// the owner presses on purpose.
func replyDelegateState(app *App, c *Collection, resolve bool) (replyDelegateStatus, string) {
	if app == nil || c == nil || c.ReplyDelegate == "" {
		return replyDelegateUnset, ""
	}
	actorIRI := ""
	if resolve {
		if app.db != nil {
			iri, err := app.db.GetProfilePageFromHandle(app, c.ReplyDelegate)
			if err != nil {
				log.Info("Couldn't resolve reply delegate '%s' for blog '%s': %v", c.ReplyDelegate, c.Alias, err)
			} else {
				actorIRI = iri
			}
		}
		if actorIRI == "" {
			// A lookup happened and came back empty, which is a different
			// thing to say than "nobody has looked yet".
			return replyDelegateUnreachable, ""
		}
	} else if app.db != nil {
		// The cached-only path. remoteusers stores handles without the
		// leading '@', the same trimming GetProfilePageFromHandle does.
		if ru, err := getRemoteUserFromHandle(app, strings.TrimLeft(c.ReplyDelegate, "@")); err == nil && ru != nil {
			actorIRI = ru.ActorID
		}
	}
	if actorIRI == "" {
		// The cached-only path found nothing. Say "not following" rather than
		// inventing a third answer: the mention is not being sent either way.
		return replyDelegateNotFollowing, ""
	}
	follows, err := replyDelegateFollowsCollection(app, c, actorIRI)
	if err != nil {
		// A failed read is not evidence of consent. Report the conservative
		// answer, which is also what applyReplyDelegate will act on.
		return replyDelegateNotFollowing, actorIRI
	}
	return replyDelegateStatusOf(c.ReplyDelegate, actorIRI, follows), actorIRI
}

// warmReplyDelegate resolves a delegate handle as it is saved.
//
// Resolution is otherwise lazy: the handle becomes an actor the first time a
// post federates, and until then the settings page has nothing cached to
// report on. That left a blog whose delegate genuinely follows it reading as
// though it did not, which is the one thing this page must not get wrong.
// Saving is the natural moment to pay for the lookup — the owner has just
// asked for this handle, and a webfinger call is what the next page render
// refuses to make.
//
// Best effort by design. A failure is logged and nothing else: the delegate is
// already saved, the Check button can retry, and a peer that happens to be
// down must not turn "save your settings" into an error.
func warmReplyDelegate(app *App, submitted *string) {
	if app == nil || app.db == nil || submitted == nil {
		return
	}
	handle, ok := normalizeReplyDelegate(*submitted)
	if !ok || handle == "" {
		return
	}
	if _, err := app.db.GetProfilePageFromHandle(app, handle); err != nil {
		log.Info("Couldn't resolve reply delegate '%s' on save: %v", handle, err)
	}
}

// replyDelegateCheck is the JSON the Customize page's Check button gets back.
// Status and Message travel together so the page never has to reproduce the
// wording, and the actor IRI is included because it is the one fact that makes
// a "not following" answer diagnosable.
type replyDelegateCheck struct {
	Handle  string              `json:"handle"`
	ActorID string              `json:"actor_id,omitempty"`
	Status  replyDelegateStatus `json:"status"`
	Message string              `json:"message"`
}

// handleCheckReplyDelegate re-checks a blog's saved delegate on demand.
//
// It reads: nothing here writes the delegate, and the button deliberately
// checks the *saved* value rather than whatever is typed in the box, so that
// what it reports is what federation will actually do. It is allowed to make
// the webfinger call the page render refuses to make.
func handleCheckReplyDelegate(app *App, u *User, w http.ResponseWriter, r *http.Request) error {
	vars := mux.Vars(r)
	c, err := app.db.GetCollection(vars["collection"])
	if err != nil {
		return err
	}
	if c.OwnerID != u.ID {
		return ErrCollectionNotFound
	}
	c.hostName = app.cfg.App.Host

	// Check what is in the box, not what is in the database. The owner presses
	// this button while typing a handle, and answering about the saved value
	// looks like the button is broken: the handle changes, the verdict does
	// not. Nothing here writes, so checking an unsaved handle is a question,
	// not a change — it is still Save that sets the delegate.
	handle := c.ReplyDelegate
	if typed, ok := r.URL.Query()["handle"]; ok && len(typed) > 0 {
		normalized, valid := normalizeReplyDelegate(typed[0])
		if !valid {
			return impart.WriteSuccess(w, replyDelegateCheck{
				Handle:  strings.TrimSpace(typed[0]),
				Status:  replyDelegateInvalid,
				Message: replyDelegateStatusMessage(replyDelegateInvalid, "", collectionFediverseHandle(c)),
			}, http.StatusOK)
		}
		handle = normalized
	}

	// replyDelegateState reads the delegate off the collection, so ask it
	// about a copy carrying the handle under test. The copy is a value, so
	// nothing that follows can reach the stored collection.
	probe := *c
	probe.ReplyDelegate = handle

	status, actorID := replyDelegateState(app, &probe, true)
	return impart.WriteSuccess(w, replyDelegateCheck{
		Handle:  handle,
		ActorID: actorID,
		Status:  status,
		Message: replyDelegateStatusMessage(status, handle, collectionFediverseHandle(c)),
	}, http.StatusOK)
}
