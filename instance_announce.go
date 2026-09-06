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
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/writeas/impart"
	"github.com/writeas/web-core/activitystreams"
	"github.com/writeas/web-core/log"
)

// The instance-wide announce actor.
//
// WriteFreely federates one actor per blog: a reader who wants everything
// written on an instance has to find and follow every blog on it, and has no
// way to learn about a blog created after they subscribed. This gives the
// instance one actor of its own — the server actor that already exists at
// /api/collections/<host> — which anyone can follow once to receive every new
// post from every public blog, as an Announce.
//
// Three properties are deliberate, and each is load-bearing:
//
//   - It is off unless App.InstanceAnnounce is set. See the config comment.
//   - It announces PUBLIC blogs only. Unlisted blogs are followers-only on
//     the wire (see ActivityObject), and relaying one would republish it to
//     everyone following the instance — undoing that guarantee through a side
//     door. Private and protected blogs never federate at all.
//   - It delivers through makeActivityPost, so the federation allowlist
//     governs every destination exactly as it does for per-blog delivery.
//
// A reader following both a blog and the instance actor receives that blog's
// posts twice, once as a Create and once as an Announce. That is what a boost
// is, and every fediverse client already deduplicates it.

// announcePerPage is the number of Announce activities in one page of the
// instance actor's outbox. It matches the per-blog outbox, which pages
// through db.GetPosts at the same size.
const announcePerPage = 10

// announceActivity is an Announce whose object is an IRI rather than an
// inlined object.
//
// web-core's Activity carries `Object *Object`, which always serialises the
// whole object. An Announce must reference the post by IRI: the post already
// exists at that address, attributed to the blog that wrote it, and inlining
// a second copy under the instance actor would present the server as the
// author. ActivityStreams allows either form; only one of them is honest
// here.
type announceActivity struct {
	activitystreams.BaseObject
	Actor     string    `json:"actor"`
	Published time.Time `json:"published,omitempty"`
	To        []string  `json:"to,omitempty"`
	CC        []string  `json:"cc,omitempty"`
	Object    string    `json:"object"`
}

// undoAnnounceActivity retracts a previously delivered Announce.
type undoAnnounceActivity struct {
	activitystreams.BaseObject
	Actor  string           `json:"actor"`
	To     []string         `json:"to,omitempty"`
	Object announceActivity `json:"object"`
}

// announceablePost is the whole of what an Announce needs from a post: an id
// to build the IRI from, and a publication time. The post's content, title
// and tags belong to the blog's own Create and are never restated here.
type announceablePost struct {
	ID      string
	Created time.Time
}

// postIRI returns the ActivityPub id of a post, which is the same address
// ActivityObject gives it.
func postIRI(app *App, postID string) string {
	return app.cfg.App.Host + "/api/posts/" + postID
}

// newAnnounce builds the Announce the instance actor delivers for one post.
//
// The activity id is derived from the post IRI but distinct from it:
// ActivityStreams 2.0 requires an id to identify exactly one object, and an
// activity is a distinct object from the one it wraps. Sharing them is the
// bug that makes GoToSocial silently drop delivered posts, which is why the
// per-blog Create suffixes its id the same way.
func newAnnounce(app *App, actor *activitystreams.Person, p announceablePost) *announceActivity {
	iri := postIRI(app, p.ID)
	return &announceActivity{
		BaseObject: activitystreams.BaseObject{
			Context: []interface{}{activitystreams.Namespace},
			Type:    "Announce",
			ID:      iri + "#Announce",
		},
		Actor:     actor.ID,
		Published: p.Created,
		To:        []string{activitystreams.ToPublic},
		CC:        []string{actor.ID + "/followers"},
		Object:    iri,
	}
}

// instanceAnnounceEnabled reports whether this instance relays at all: the
// feature is on, federation is on, and the instance is not private without an
// allowlist — the same gate federatePost applies before it delivers anything.
func instanceAnnounceEnabled(app *App) bool {
	if !app.cfg.App.InstanceAnnounce || !app.cfg.App.Federation {
		return false
	}
	return !app.cfg.App.Private || app.federationAllowlistActive()
}

// instanceAnnounceEligible reports whether a post in the given collection may
// be announced instance-wide, and is the single place that decision is made.
//
// It reads the collection from the database rather than trusting the one
// hanging off the post. Callers assemble that struct along several different
// paths — a new post, an edit, an import — and a visibility field that
// arrives zero from one of them means CollUnlisted, which is a value this
// function must never mistake for "not set".
func instanceAnnounceEligible(app *App, collID int64) bool {
	if !instanceAnnounceEnabled(app) {
		return false
	}
	if collID == 0 {
		return false
	}

	c, err := app.db.GetCollectionByID(collID)
	if err != nil {
		log.Error("instance announce: couldn't load collection %d: %v", collID, err)
		return false
	}
	// Strict equality, not IsPublic(). A collection carrying the public bit
	// alongside private or protected is not something to relay to the world
	// on the strength of one bit being set.
	if c.Visibility != CollPublic {
		return false
	}

	silenced, err := app.db.IsUserSilenced(c.OwnerID)
	if err != nil {
		// Fail closed: an unreadable user status is not permission to
		// broadcast that user's writing.
		log.Error("instance announce: couldn't check silenced status for user %d: %v", c.OwnerID, err)
		return false
	}
	return !silenced
}

// fanOutPostToInstanceFollowers gives the instance actor's followers whichever
// activity a publish or an edit calls for.
//
// A new post is Announced by the instance actor: a boost, referencing the
// post's IRI, which is how a relay introduces writing it did not author.
//
// An edit is different, and the difference is the whole reason this function
// exists rather than an `if !isUpdate` at the call site. A receiver caches the
// object it fetched after the Announce and does not re-fetch it; without an
// Update it renders the original text for good. So the edit is delivered as
// the blog's own Update activity, signed by the BLOG actor rather than by the
// instance actor, because a receiver accepts an Update only from the object's
// owner. That is the same forwarding a fediverse relay does, and it is why the
// blog actor is threaded down here.
//
// An edit is not re-Announced. The Update refreshes what a follower already
// holds; a second Announce would push the post back to the top of their
// timeline on every typo fix.
//
// It runs synchronously up to the point where the activity is serialised, and
// only then hands delivery to a goroutine. That ordering is load-bearing: the
// caller rewrites na.CC per shared inbox immediately afterwards, so the
// snapshot has to be taken before this function returns.
func fanOutPostToInstanceFollowers(app *App, p *PublicPost, na *activitystreams.Object, blogActor *activitystreams.Person, collID int64, isUpdate bool, updateTime time.Time) {
	if !instanceAnnounceEligible(app, collID) {
		return
	}

	if !isUpdate {
		go announceToInstanceFollowers(app, announceablePost{ID: p.ID, Created: p.Created}, collID)
		return
	}

	obj := instanceCopyOfObject(na)
	obj.Updated = &p.Updated
	activity := activitystreams.NewUpdateActivity(obj)
	// The same id the blog's own followers get for this edit, so anyone
	// following both the blog and the instance actor sees one edit rather
	// than two. Receivers dedupe activities by id.
	activity.ID += fmt.Sprintf("#Update/%d", updateTime.Unix())

	raw, err := snapshotActivity(activity)
	if err != nil {
		log.Error("instance announce: couldn't serialise Update: %v", err)
		return
	}
	if debugging {
		logOutgoingActivity("Update", activity)
	}
	go deliverToInstanceFollowers(app, blogActor, raw)
}

// fanOutDeleteToInstanceFollowers delivers a post's Delete to the instance
// actor's followers, signed by the blog that owns the post.
//
// It goes alongside the Undo of the Announce, and the two do different jobs:
// the Undo retracts the boost, this removes the cached object the receiver
// actually rendered. A receiver honours a Delete only from the object's owner,
// which is why this is signed by the blog and the Undo by the instance.
//
// Unlike the Update path it does not ask whether the post is still eligible to
// be announced. A post whose blog turned private after it was announced is
// exactly the post most in need of deleting.
func fanOutDeleteToInstanceFollowers(app *App, na *activitystreams.Object, blogActor *activitystreams.Person, collID int64) {
	if !instanceAnnounceEnabled(app) {
		return
	}

	da := activitystreams.NewDeleteActivity(instanceCopyOfObject(na))
	// ActivityStreams 2.0 requires an id to identify exactly one object, and
	// an activity is a distinct object from the one it wraps. Same suffix the
	// blog's own Delete uses, so a dual follower dedupes the two.
	da.ID += "#Delete"

	raw, err := snapshotActivity(da)
	if err != nil {
		log.Error("instance announce: couldn't serialise Delete: %v", err)
		return
	}
	if debugging {
		logOutgoingActivity("Delete", da)
	}
	go deliverToInstanceFollowers(app, blogActor, raw)
}

// instanceCopyOfObject copies a post's activity object for instance-wide
// delivery, replacing the addressing that the per-blog fan-out is about to
// overwrite.
//
// The caller's loop assigns na.CC a fresh slice holding one instance's
// follower actor ids, which is addressing meant for that instance and nobody
// else. Instance followers get the blog's followers collection instead: the
// same value ActivityObject puts there before delivery narrows it.
func instanceCopyOfObject(na *activitystreams.Object) *activitystreams.Object {
	obj := *na
	obj.CC = []string{na.AttributedTo + "/followers"}
	return &obj
}

// snapshotActivity serialises an activity immediately, so that later mutation
// of the object it was built from cannot change what gets delivered.
//
// Delivery happens in a goroutine while the caller keeps working on the same
// object, so handing that object to the goroutine would be a data race with a
// wrong-content failure mode rather than a crash: the activity would go out
// carrying whatever the caller had rewritten by the time it was marshalled.
func snapshotActivity(a interface{}) (json.RawMessage, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// announceToInstanceFollowers delivers an Announce for a newly published post
// to everyone following the instance actor.
//
// It runs as its own goroutine beside federatePost rather than inside it, so
// that a relay failure cannot interfere with per-blog delivery, and a slow
// per-blog fan-out cannot delay this one.
func announceToInstanceFollowers(app *App, p announceablePost, collID int64) {
	if !instanceAnnounceEligible(app, collID) {
		return
	}

	actor := newInstanceColl(app).PersonObject()
	a := newAnnounce(app, actor, p)

	if debugging {
		logOutgoingActivity("Announce", a)
	}
	deliverToInstanceFollowers(app, actor, a)
}

// undoAnnounceToInstanceFollowers retracts the Announce for a post that has
// been deleted.
//
// Without this, a follower of the instance actor who does not also follow the
// blog keeps a boost pointing at a post that no longer exists: the Delete
// goes to the blog's own followers, and they are a different set of people.
//
// It checks only whether the instance relays at all, not whether this
// particular post is still eligible to be announced. A post whose blog was
// made private, or whose author was silenced, after it was announced is
// exactly the post most in need of retracting; asking the eligibility
// question here would refuse to withdraw it.
func undoAnnounceToInstanceFollowers(app *App, p announceablePost, collID int64) {
	if !instanceAnnounceEnabled(app) {
		return
	}

	actor := newInstanceColl(app).PersonObject()
	announce := newAnnounce(app, actor, p)
	announce.Context = nil

	u := &undoAnnounceActivity{
		BaseObject: activitystreams.BaseObject{
			Context: []interface{}{activitystreams.Namespace},
			Type:    "Undo",
			ID:      announce.ID + "/undo",
		},
		Actor:  actor.ID,
		To:     []string{activitystreams.ToPublic},
		Object: *announce,
	}

	if debugging {
		logOutgoingActivity("Undo Announce", u)
	}
	deliverToInstanceFollowers(app, actor, u)
}

// deliverToInstanceFollowers POSTs an activity to every distinct inbox among
// the instance actor's followers, preferring a shared inbox where one is
// advertised.
//
// Delivery goes one activity per inbox, not per follower: the CC is the
// followers collection rather than a list of individual actors, so there is
// nothing to vary between recipients on one server. That is the difference
// from federatePost, which rewrites CC per shared inbox because a post's
// addressing names its followers individually.
func deliverToInstanceFollowers(app *App, actor *activitystreams.Person, activity interface{}) {
	followers, err := app.db.GetAPFollowers(newInstanceColl(app))
	if err != nil {
		log.Error("instance announce: couldn't get followers: %v", err)
		return
	}

	for _, inbox := range distinctInboxes(followers) {
		if err := makeActivityPost(app, actor, inbox, activity); err != nil {
			// One unreachable peer must not stop the others.
			log.Error("instance announce: couldn't post to %s: %v", inbox, err)
		}
	}
}

// distinctInboxes collapses a follower list to the addresses that must
// actually be POSTed to, so a server hosting fifty followers behind one
// shared inbox receives one delivery rather than fifty.
func distinctInboxes(followers *[]RemoteUser) []string {
	seen := map[string]bool{}
	inboxes := []string{}
	for _, f := range *followers {
		inbox := f.SharedInbox
		if inbox == "" {
			inbox = f.Inbox
		}
		if inbox == "" || seen[inbox] {
			continue
		}
		seen[inbox] = true
		inboxes = append(inboxes, inbox)
	}
	return inboxes
}

// handleFetchInstanceOutbox serves the instance actor's outbox: the same
// Announce activities its followers were delivered, so a peer that backfills
// on follow sees the history instead of a 404.
//
// It pages exactly as handleFetchCollectionOutbox does — a bare request
// returns the OrderedCollection, ?page=N returns a page — because peers that
// walk one already know how to walk the other.
func handleFetchInstanceOutbox(app *App, w http.ResponseWriter, r *http.Request, c *Collection) error {
	accountRoot := c.FederatedAccount()

	// With the feature off the actor has no announces to show. An empty
	// collection is the honest answer, and a peer reading it learns not to
	// expect a firehose.
	if !app.cfg.App.InstanceAnnounce {
		return impart.RenderActivityJSON(w, activitystreams.NewOrderedCollection(accountRoot, "outbox", 0), http.StatusOK)
	}

	total, err := app.db.CountPublicPostsToAnnounce()
	if err != nil {
		return err
	}

	page, err := strconv.Atoi(r.FormValue("page"))
	if err != nil || page < 1 {
		return impart.RenderActivityJSON(w, activitystreams.NewOrderedCollection(accountRoot, "outbox", total), http.StatusOK)
	}

	ocp := activitystreams.NewOrderedCollectionPage(accountRoot, "outbox", total, page)
	ocp.OrderedItems = []interface{}{}

	posts, err := app.db.GetPublicPostsToAnnounce(page, announcePerPage)
	if err != nil {
		return err
	}
	actor := c.PersonObject()
	for _, p := range *posts {
		a := newAnnounce(app, actor, p)
		// The page carries its own @context; repeating it on every item is
		// noise, and the per-blog outbox drops it for the same reason.
		a.Context = nil
		ocp.OrderedItems = append(ocp.OrderedItems, *a)
	}

	setCacheControl(w, apCacheTime)
	return impart.RenderActivityJSON(w, ocp, http.StatusOK)
}

// The two queries below live here rather than in database.go because they
// exist only for this feature and share its rule about what may be announced.
// Keeping them beside instanceAnnounceEligible is what makes it checkable
// that the outbox shows exactly what delivery sends.

// announceableCondition selects posts eligible for instance-wide announce:
// published, in a public collection, by a user who is not silenced.
//
// privacy = 1 is CollPublic. u.status = 0 is an active user; the local
// timeline query uses both of the same tests, and this must not drift from
// what instanceAnnounceEligible decides per post.
const announceableCondition = `
FROM posts p
INNER JOIN collections c ON p.collection_id = c.id
INNER JOIN users u ON u.id = p.owner_id
WHERE c.privacy = 1 AND u.status = 0 AND p.created <= `

// CountPublicPostsToAnnounce returns how many posts the instance actor's
// outbox has to show.
func (db *datastore) CountPublicPostsToAnnounce() (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) ` + announceableCondition + db.now()).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		log.Error("Failed counting announceable posts: %v", err)
		return 0, impart.HTTPError{http.StatusInternalServerError, "Couldn't retrieve instance outbox."}
	}
	return count, nil
}

// GetPublicPostsToAnnounce returns one page of announceable posts, newest
// first, matching the order they were delivered in.
func (db *datastore) GetPublicPostsToAnnounce(page, perPage int) (*[]announceablePost, error) {
	if page < 1 {
		page = 1
	}
	rows, err := db.Query(`SELECT p.id, p.created `+announceableCondition+db.now()+`
ORDER BY p.created DESC
LIMIT ? OFFSET ?`, perPage, (page-1)*perPage)
	if err != nil {
		log.Error("Failed selecting announceable posts: %v", err)
		return nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't retrieve instance outbox."}
	}
	defer rows.Close()

	posts := []announceablePost{}
	for rows.Next() {
		p := announceablePost{}
		if err := rows.Scan(&p.ID, &p.Created); err != nil {
			log.Error("Unable to scan announceable post, skipping: %v", err)
			continue
		}
		posts = append(posts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading announceable posts: %v", err)
	}
	return &posts, nil
}
