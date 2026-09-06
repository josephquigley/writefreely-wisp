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

	// instanceKey is the public key deliveries are verified against.
	instanceKey *rsa.PublicKey

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

	p := &dummyPeer{instanceKey: instancePublicKey(t, app)}

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
		if err := v.Verify(p.instanceKey, fedsig.RSA_SHA256); err != nil {
			d.SigError = fmt.Errorf("bad signature: %v", err)
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.received = append(p.received, d)
}

// instancePublicKey returns the instance actor's published public key, parsed.
func instancePublicKey(t *testing.T, app *App) *rsa.PublicKey {
	t.Helper()
	actor := newInstanceColl(app).PersonObject()
	block, _ := pem.Decode([]byte(actor.PublicKey.PublicKeyPEM))
	if block == nil {
		t.Fatal("instance actor published no parsable public key")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse instance public key: %v", err)
	}
	rsaKey, ok := k.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("instance actor key is %T, not RSA", k)
	}
	return rsaKey
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

// An edit is not re-announced. The Announce points at the post's IRI, so what
// it resolves to changes on its own; re-announcing would push the post back to
// the top of a follower's timeline on every typo fix.
func TestEditingAPostDoesNotReAnnounceIt(t *testing.T) {
	app := newAnnounceTestApp(t)
	peer := newDummyPeer(t, app)
	followInstance(t, app, peer)

	pp, collID := newFederatablePost(t, app, "public0001")

	federatePost(app, pp, collID, true)

	peer.assertNothingDelivered(t, "an update must not produce a second Announce")
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
