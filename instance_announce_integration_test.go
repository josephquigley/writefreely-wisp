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
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	fedsig "github.com/go-fed/httpsig"
	"github.com/gorilla/mux"
	"github.com/guregu/null"
	"github.com/guregu/null/zero"
	"github.com/stretchr/testify/assert"
	"github.com/writeas/web-core/activitystreams"
)

// End-to-end tests for the instance announce actor, against a dummy remote
// ActivityPub server.
//
// These run the real handlers, over real HTTP, against a real sqlite
// database. The dummy peer is a full participant: it serves an actor
// document, receives deliveries at an inbox, and verifies the HTTP signature
// on everything it is sent against the key the instance actor publishes. A
// test that only asserted on Go values would not catch an activity that is
// well-formed in memory and unusable on the wire, which is the failure mode
// that matters here.
//
// One thing is faked, deliberately. The peer is on 127.0.0.1, and
// isPublicIRI refuses to fetch loopback addresses — an SSRF guard, and not
// one to punch a test hole through. So each test pre-registers the peer in
// remoteusers, which is exactly what getActor consults before it would fetch.
// Actor discovery is upstream behaviour this change does not touch; delivery,
// signing and the follower bookkeeping are what is under test, and all three
// are real here.

// dummyPeer is a remote ActivityPub server: it serves one actor and records
// everything delivered to its inbox.
type dummyPeer struct {
	server  *httptest.Server
	actorID string

	// app is the instance under test, so the peer can look up the published
	// key for whichever actor signed a delivery. A real peer resolves the
	// keyId to an actor and fetches its key; this does the same lookup
	// locally, because the actors are not fetchable over loopback.
	app *App

	mu       sync.Mutex
	received []deliveredActivity
}

// deliveredActivity is one thing the peer was sent, as it arrived.
type deliveredActivity struct {
	Type     string
	Object   interface{}
	Actor    string
	KeyID    string
	SigError error
	Raw      map[string]interface{}
}

func (p *dummyPeer) inbox() string { return p.server.URL + "/inbox" }

// activities returns a copy of what has been delivered so far.
func (p *dummyPeer) activities() []deliveredActivity {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]deliveredActivity{}, p.received...)
}

// awaitActivity waits for a delivery of the given type and returns it.
//
// Delivery is asynchronous by design — the Accept for a Follow is sent from a
// goroutine that pauses first, because some peers are not ready to receive it
// immediately — so a test has to wait rather than assert straight away.
func (p *dummyPeer) awaitActivity(t *testing.T, typ string) deliveredActivity {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, a := range p.activities() {
			if a.Type == typ {
				return a
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no %s delivered within the deadline; got %+v", typ, p.activities())
	return deliveredActivity{}
}

// assertNothingDelivered gives delivery a fair chance to happen and then
// asserts that it did not. Used for the cases where announcing would be a
// privacy failure, so a false pass here is the expensive kind.
func (p *dummyPeer) assertNothingDelivered(t *testing.T, why string) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	assert.Empty(t, p.activities(), why)
}

// newDummyPeer starts a remote instance that will verify every delivery
// against the given instance actor's published key.
func newDummyPeer(t *testing.T, app *App) *dummyPeer {
	t.Helper()

	p := &dummyPeer{app: app}

	mux := http.NewServeMux()
	mux.HandleFunc("/inbox", func(w http.ResponseWriter, r *http.Request) {
		p.record(r)
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/users/bob", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/activity+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"@context":          activitystreams.Namespace,
			"type":              "Person",
			"id":                p.actorID,
			"preferredUsername": "bob",
			"inbox":             p.inbox(),
		})
	})

	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	p.actorID = p.server.URL + "/users/bob"

	return p
}

// record parses one delivery and verifies its signature.
func (p *dummyPeer) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	d := deliveredActivity{}
	if err := json.Unmarshal(body, &d.Raw); err != nil {
		d.SigError = fmt.Errorf("undecodable body: %v", err)
	}
	if s, ok := d.Raw["type"].(string); ok {
		d.Type = s
	}
	if s, ok := d.Raw["actor"].(string); ok {
		d.Actor = s
	}
	d.Object = d.Raw["object"]

	// Verify exactly as a real peer would: the signature over the request,
	// against the key the actor publishes. The body has to be restored first
	// because the verifier reads it to check the digest.
	r.Body = io.NopCloser(bytes.NewReader(body))
	v, err := fedsig.NewVerifier(r)
	if err != nil {
		d.SigError = fmt.Errorf("no usable signature: %v", err)
	} else {
		d.KeyID = v.KeyId()
		key, err := p.keyFor(d.KeyID)
		if err != nil {
			d.SigError = err
		} else if err := v.Verify(key, fedsig.RSA_SHA256); err != nil {
			d.SigError = fmt.Errorf("bad signature: %v", err)
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.received = append(p.received, d)
}

// keyFor resolves a keyId to the public key its actor publishes, the way a
// real peer would: the instance actor for the server's own activities, the
// blog actor for a post's Create, Update or Delete.
//
// A mismatch is an error rather than a fallback. An activity signed by the
// wrong actor is precisely the bug worth catching here: a receiver honours an
// Update or a Delete only from the object's owner, so one signed by the
// instance actor would be dropped in the field and pass in a test that shrugged
// and tried the other key.
func (p *dummyPeer) keyFor(keyID string) (*rsa.PublicKey, error) {
	actorID := strings.TrimSuffix(keyID, "#main-key")

	var c *Collection
	if actorID == newInstanceColl(p.app).FederatedAccount() {
		c = newInstanceColl(p.app)
	} else {
		alias := path.Base(actorID)
		var err error
		c, err = p.app.db.GetCollection(alias)
		if err != nil {
			return nil, fmt.Errorf("unknown signing actor %q: %v", actorID, err)
		}
		c.hostName = p.app.cfg.App.Host
	}

	pemBytes := c.PersonObject().PublicKey.PublicKeyPEM
	block, _ := pem.Decode([]byte(pemBytes))
	if block == nil {
		return nil, fmt.Errorf("actor %q publishes no parsable key", actorID)
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse key for %q: %v", actorID, err)
	}
	rsaKey, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key for %q is %T, not RSA", actorID, k)
	}
	return rsaKey, nil
}

// registerPeerLocally inserts the peer into remoteusers, which is what lets
// getActor resolve it without an outbound fetch. It does NOT make the peer a
// follower.
func registerPeerLocally(t *testing.T, app *App, p *dummyPeer) int64 {
	t.Helper()
	res, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, url) VALUES (?, ?, ?, ?)",
		p.actorID, p.inbox(), "", p.actorID)
	if err != nil {
		t.Fatalf("register peer: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}
	return id
}

// followInstance makes the peer a follower of the instance actor directly,
// for the tests whose subject is delivery rather than the follow handshake.
func followInstance(t *testing.T, app *App, p *dummyPeer) {
	t.Helper()
	id := registerPeerLocally(t, app, p)
	_, err := app.db.Exec("INSERT INTO remotefollows (collection_id, remote_user_id, created) VALUES (0, ?, "+app.db.now()+")", id)
	if err != nil {
		t.Fatalf("follow instance: %v", err)
	}
}

// postToInstanceInbox delivers an activity to the instance actor's inbox
// through the real handler.
func postToInstanceInbox(t *testing.T, app *App, activity map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(activity)
	if err != nil {
		t.Fatalf("marshal activity: %v", err)
	}
	r := httptest.NewRequest("POST", announceTestHost+"/api/collections/local.example/inbox", bytes.NewReader(body))
	r = mux.SetURLVars(r, map[string]string{"alias": "local.example"})
	w := httptest.NewRecorder()

	if err := handleFetchCollectionInbox(app, w, r); err != nil {
		t.Fatalf("inbox: %v", err)
	}
	assert.Equal(t, http.StatusOK, w.Code)
}

// instanceActorID is the id a remote peer follows.
func instanceActorID(app *App) string {
	return newInstanceColl(app).FederatedAccount()
}

// countInstanceFollows returns how many followers the instance actor has.
func countInstanceFollows(t *testing.T, app *App) int {
	t.Helper()
	var n int
	if err := app.db.QueryRow("SELECT COUNT(*) FROM remotefollows WHERE collection_id = 0").Scan(&n); err != nil {
		t.Fatalf("count follows: %v", err)
	}
	return n
}

// A remote server follows the instance actor and is accepted, and the follow
// is recorded against the instance rather than against any blog.
func TestPeerCanFollowTheInstanceActor(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	registerPeerLocally(t, app, peer)

	postToInstanceInbox(t, app, map[string]interface{}{
		"@context": activitystreams.Namespace,
		"type":     "Follow",
		"id":       peer.actorID + "/follows/1",
		"actor":    peer.actorID,
		"object":   instanceActorID(app),
	})

	accept := peer.awaitActivity(t, "Accept")
	assert.NoError(t, accept.SigError, "the Accept must be signed by a key the peer can verify")
	assert.Equal(t, instanceActorID(app)+"#main-key", accept.KeyID,
		"the Accept must be signed as the instance actor, not as some blog")
	assert.NotEmpty(t, accept.Raw["id"], "the Accept must carry an id; the Undo path lost its own by leaving this to the Follow callback")

	assert.Equal(t, 1, countInstanceFollows(t, app),
		"the follow must be recorded against collection 0, the instance actor")
}

// The followers collection reports what the follow handshake recorded.
func TestInstanceFollowersCollectionListsFollowers(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	r := httptest.NewRequest("GET", announceTestHost+"/api/collections/local.example/followers", nil)
	r = mux.SetURLVars(r, map[string]string{"alias": "local.example"})
	w := httptest.NewRecorder()

	err := handleFetchCollectionFollowers(app, w, r)

	assert.NoError(t, err, "the instance actor's followers endpoint must resolve; before this change it 404'd")
	assert.Equal(t, http.StatusOK, w.Code)

	oc := map[string]interface{}{}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &oc))
	assert.Equal(t, float64(1), oc["totalItems"])
}

// The whole point: a post on a public blog reaches someone who follows only
// the instance actor.
func TestPublicPostIsAnnouncedToInstanceFollowers(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	collID := newTestCollection(t, app, "quigs", CollPublic)
	post := newTestPost(t, app, collID, "public0001", time.Now().UTC().Add(-time.Minute))

	announceToInstanceFollowers(app, post, collID)

	a := peer.awaitActivity(t, "Announce")
	assert.NoError(t, a.SigError, "the Announce must be signed by a key the peer can verify")
	assert.Equal(t, instanceActorID(app)+"#main-key", a.KeyID)
	assert.Equal(t, instanceActorID(app), a.Actor, "the boost is the server's; the post stays the author's")
	assert.Equal(t, announceTestHost+"/api/posts/public0001", a.Object,
		"the object must be the post's IRI, so the peer fetches the original attributed to its author")
	assert.Equal(t, []interface{}{activitystreams.ToPublic}, a.Raw["to"])
	assert.NotEqual(t, a.Object, a.Raw["id"], "activity and object must not share an id")
}

// Deleting an announced post retracts the boost, so a follower of the
// instance actor is not left holding a reference to a post that is gone.
func TestDeletedPostIsUnannounced(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	collID := newTestCollection(t, app, "quigs", CollPublic)
	post := newTestPost(t, app, collID, "public0001", time.Now().UTC().Add(-time.Minute))

	undoAnnounceToInstanceFollowers(app, post, collID)

	u := peer.awaitActivity(t, "Undo")
	assert.NoError(t, u.SigError)
	obj, ok := u.Object.(map[string]interface{})
	assert.True(t, ok, "the Undo must wrap the Announce it retracts, got %T", u.Object)
	assert.Equal(t, "Announce", obj["type"])
	assert.Equal(t, announceTestHost+"/api/posts/public0001", obj["object"])
}

// The wiring, end to end: publishing a post on a public blog announces it
// instance-wide, without any caller having to know the relay exists.
//
// This is the test that would fail if the call were removed from federatePost
// while everything else kept working, which is the likeliest way this feature
// silently dies.
func TestFederatingANewPostAnnouncesItInstanceWide(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	pp, collID := newFederatablePost(t, app, "public0001")

	federatePost(app, pp, collID, false)

	a := peer.awaitActivity(t, "Announce")
	assert.Equal(t, announceTestHost+"/api/posts/public0001", a.Object)
}

// An edit reaches instance followers as the blog's own Update, signed by the
// blog.
//
// This is the case a boost alone gets wrong. A receiver caches the object it
// fetched after the Announce and does not re-fetch it, so without an Update a
// follower of the instance actor renders the original text for good. The
// signature has to be the blog's, because a receiver accepts an Update only
// from the object's owner.
func TestEditingAPostSendsAnUpdateToInstanceFollowers(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	pp, collID := newFederatablePost(t, app, "public0001")
	pp.Content = "Edited body"

	federatePost(app, pp, collID, true)

	u := peer.awaitActivity(t, "Update")
	assert.Equal(t, blogActorID(app, "quigs")+"#main-key", u.KeyID,
		"an Update must be signed by the blog that owns the post, not by the instance actor")
	assert.NoError(t, u.SigError)

	obj, ok := u.Object.(map[string]interface{})
	assert.True(t, ok, "the Update must carry the post object, got %T", u.Object)
	assert.Contains(t, obj["content"], "Edited body", "the edited text is the point of sending it")
	assert.Equal(t, announceTestHost+"/api/posts/public0001", obj["id"])

	// Not re-announced: the Update refreshes what the follower already holds,
	// and a second Announce would push the post back to the top of their
	// timeline on every typo fix.
	for _, a := range peer.activities() {
		assert.NotEqual(t, "Announce", a.Type, "an edit must not be announced again")
	}
}

// An edit carries the same activity id every recipient of that edit gets, so
// someone following both the blog and the instance actor sees one edit rather
// than two. Receivers dedupe by id.
func TestUpdateIdMatchesThePerBlogUpdateId(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	pp, collID := newFederatablePost(t, app, "public0001")
	pp.Updated = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	federatePost(app, pp, collID, true)

	u := peer.awaitActivity(t, "Update")
	assert.Equal(t,
		fmt.Sprintf("%s/api/posts/public0001#Update/%d", announceTestHost, pp.Updated.Unix()),
		u.Raw["id"],
		"the id must be derived from the post's updated time, which is what the per-blog Update uses too")
}

// blogActorID is the ActivityPub id of a blog on this instance.
func blogActorID(app *App, alias string) string {
	return app.cfg.App.Host + "/api/collections/" + alias
}

// newFederatablePost creates a public blog with one post and returns the post
// in the shape federatePost expects.
func newFederatablePost(t *testing.T, app *App, postID string) (*PublicPost, int64) {
	t.Helper()
	collID := newTestCollection(t, app, "quigs", CollPublic)
	created := time.Now().UTC().Add(-time.Minute)
	newTestPost(t, app, collID, postID, created)

	coll, err := app.db.GetCollectionByID(collID)
	if err != nil {
		t.Fatalf("load collection: %v", err)
	}
	coll.hostName = app.cfg.App.Host

	return &PublicPost{
		Post: &Post{
			ID:      postID,
			Slug:    null.NewString(postID, true),
			Title:   zero.NewString("Title "+postID, true),
			Content: "Body of " + postID,
			Created: created,
		},
		Collection: &CollectionObj{Collection: *coll},
	}, collID
}

// A post whose blog turned private after it was announced must still be
// retracted. This is the one place the undo deliberately does not ask the
// eligibility question: refusing to withdraw a post because it has become
// more private is backwards.
func TestUndoIsSentEvenWhenThePostIsNoLongerEligible(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	collID := newTestCollection(t, app, "quigs", CollPublic)
	post := newTestPost(t, app, collID, "public0001", time.Now().UTC().Add(-time.Minute))
	if _, err := app.db.Exec("UPDATE collections SET privacy = ? WHERE id = ?", CollPrivate, collID); err != nil {
		t.Fatalf("make collection private: %v", err)
	}

	undoAnnounceToInstanceFollowers(app, post, collID)

	u := peer.awaitActivity(t, "Undo")
	assert.NoError(t, u.SigError)
}

// An edit on a blog that may not be relayed is not forwarded either. The
// Update path carries the post's full content, so it is a second way to leak
// an unlisted blog and needs its own guard, not just the announce path's.
func TestEditingAnUnlistedPostSendsNothingInstanceWide(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	collID := newTestCollection(t, app, "quiet", CollUnlisted)
	created := time.Now().UTC().Add(-time.Minute)
	newTestPost(t, app, collID, "unlisted001", created)
	coll, err := app.db.GetCollectionByID(collID)
	if err != nil {
		t.Fatalf("load collection: %v", err)
	}
	coll.hostName = app.cfg.App.Host
	pp := &PublicPost{
		Post: &Post{
			ID:      "unlisted001",
			Slug:    null.NewString("unlisted001", true),
			Title:   zero.NewString("Title", true),
			Content: "Edited body",
			Created: created,
		},
		Collection: &CollectionObj{Collection: *coll},
	}

	federatePost(app, pp, collID, true)

	peer.assertNothingDelivered(t, "an unlisted blog's edit must not reach instance followers")
}

// Deleting a post sends instance followers the blog's own Delete as well as
// the Undo of the boost.
//
// The two do different jobs and both are needed: the Undo retracts the boost,
// the Delete removes the cached object the receiver actually rendered. A
// receiver honours a Delete only from the object's owner, so that one is
// signed by the blog.
func TestDeletingAPostSendsDeleteAndUndoToInstanceFollowers(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	pp, collID := newFederatablePost(t, app, "public0001")

	if err := deleteFederatedPost(app, pp, collID); err != nil {
		t.Fatalf("deleteFederatedPost: %v", err)
	}

	del := peer.awaitActivity(t, "Delete")
	assert.NoError(t, del.SigError)
	assert.Equal(t, blogActorID(app, "quigs")+"#main-key", del.KeyID,
		"a Delete must be signed by the blog that owns the post")

	undo := peer.awaitActivity(t, "Undo")
	assert.NoError(t, undo.SigError)
	assert.Equal(t, instanceActorID(app)+"#main-key", undo.KeyID,
		"the Undo retracts the instance actor's own boost, so it is signed by the instance actor")
}

// The privacy cases. Each of these delivering would be a leak, so they are
// asserted against a live peer rather than only against the eligibility
// function.
func TestPostsThatMustNotBeAnnounced(t *testing.T) {
	for _, tc := range []struct {
		name  string
		vis   collVisibility
		tweak func(*testing.T, *App)
		why   string
	}{
		{
			name: "unlisted blog",
			vis:  CollUnlisted,
			why:  "an unlisted blog is followers-only on the wire; relaying it instance-wide would republish it to everyone",
		},
		{
			name: "private blog",
			vis:  CollPrivate,
			why:  "a private blog never federates at all",
		},
		{
			name: "protected blog",
			vis:  CollProtected,
			why:  "a protected blog is behind a password and must not be broadcast",
		},
		{
			name:  "silenced author",
			vis:   CollPublic,
			tweak: func(t *testing.T, app *App) { silenceTestUser(t, app) },
			why:   "a silenced author's posts must not be relayed",
		},
		{
			name:  "feature disabled",
			vis:   CollPublic,
			tweak: func(t *testing.T, app *App) { app.cfg.App.InstanceAnnounce = false },
			why:   "with the flag off the instance announces nothing, which is what makes this change safe to merge",
		},
		{
			name:  "federation disabled",
			vis:   CollPublic,
			tweak: func(t *testing.T, app *App) { app.cfg.App.Federation = false },
			why:   "an instance with federation off sends nothing anywhere",
		},
		{
			name:  "private instance with no allowlist",
			vis:   CollPublic,
			tweak: func(t *testing.T, app *App) { app.cfg.App.Private = true },
			why:   "a private instance with no allowlist federates to nobody",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newAnnounceTestApp(t)
			peer := newDummyPeer(t, app)
			followInstance(t, app, peer)

			collID := newTestCollection(t, app, "quigs", tc.vis)
			post := newTestPost(t, app, collID, "public0001", time.Now().UTC().Add(-time.Minute))
			if tc.tweak != nil {
				tc.tweak(t, app)
			}

			announceToInstanceFollowers(app, post, collID)

			peer.assertNothingDelivered(t, tc.why)
		})
	}
}

// An unfollow stops the announces.
func TestUnfollowStopsAnnounces(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)
	assert.Equal(t, 1, countInstanceFollows(t, app))

	postToInstanceInbox(t, app, map[string]interface{}{
		"@context": activitystreams.Namespace,
		"type":     "Undo",
		"id":       peer.actorID + "/follows/1/undo",
		"actor":    peer.actorID,
		"object": map[string]interface{}{
			"type":   "Follow",
			"id":     peer.actorID + "/follows/1",
			"actor":  peer.actorID,
			"object": instanceActorID(app),
		},
	})

	// The Undo is acknowledged with an Accept, then the follow is removed.
	peer.awaitActivity(t, "Accept")
	deadline := time.Now().Add(5 * time.Second)
	for countInstanceFollows(t, app) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	assert.Equal(t, 0, countInstanceFollows(t, app), "the unfollow must remove the instance follow")

	collID := newTestCollection(t, app, "quigs", CollPublic)
	post := newTestPost(t, app, collID, "public0001", time.Now().UTC().Add(-time.Minute))
	before := len(peer.activities())

	announceToInstanceFollowers(app, post, collID)

	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, before, len(peer.activities()), "a former follower must receive nothing")
}

// Two followers behind one shared inbox are one delivery, not two.
func TestSharedInboxReceivesOneDelivery(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)

	for i, actor := range []string{peer.actorID, peer.server.URL + "/users/carol"} {
		res, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, url) VALUES (?, ?, ?, ?)",
			actor, fmt.Sprintf("%s/users/%d/inbox", peer.server.URL, i), peer.inbox(), actor)
		if err != nil {
			t.Fatalf("register follower: %v", err)
		}
		id, _ := res.LastInsertId()
		if _, err := app.db.Exec("INSERT INTO remotefollows (collection_id, remote_user_id, created) VALUES (0, ?, "+app.db.now()+")", id); err != nil {
			t.Fatalf("follow: %v", err)
		}
	}

	collID := newTestCollection(t, app, "quigs", CollPublic)
	post := newTestPost(t, app, collID, "public0001", time.Now().UTC().Add(-time.Minute))

	announceToInstanceFollowers(app, post, collID)

	peer.awaitActivity(t, "Announce")
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, peer.activities(), 1, "one shared inbox is one delivery, however many followers sit behind it")
}

// The Accept sent for an Undo Follow carries an id of its own.
//
// ActivityStreams 2.0 requires every activity to have an id, and a receiver
// that enforces it refuses the delivery outright rather than ignoring the
// missing field: Mbin answers 401 with "Missing required "id" field in the
// payload", so the unfollow is never acknowledged and this instance never
// drops the follower. The Accept for a Follow had an id and the Accept for an
// Undo did not, because the inbox handler builds one shared Accept and only
// the Follow callback ever set an id on it.
func TestAcceptOfAnUndoFollowCarriesAnID(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	postToInstanceInbox(t, app, map[string]interface{}{
		"@context": activitystreams.Namespace,
		"type":     "Undo",
		"id":       peer.actorID + "/follows/1/undo",
		"actor":    peer.actorID,
		"object": map[string]interface{}{
			"type":   "Follow",
			"id":     peer.actorID + "/follows/1",
			"actor":  peer.actorID,
			"object": instanceActorID(app),
		},
	})

	accept := peer.awaitActivity(t, "Accept")
	assert.NoError(t, accept.SigError, "the Accept must be signed by a key the peer can verify")
	assert.NotEmpty(t, accept.Raw["id"], "the Accept for an Undo Follow must carry an id; a peer that requires one refuses it with a 401")
}
