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
	"crypto/rsa"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"

	fedsig "github.com/go-fed/httpsig"
	"github.com/stretchr/testify/assert"
	"github.com/writeas/web-core/activitypub"
)

// The actor fetch behind handle resolution used to be
// activityserve.NewRemoteActor, a plain unsigned GET.
//
// An instance running Mastodon's authorized fetch ("secure mode") answers an
// unsigned actor GET with 401 and a JSON error body:
//
//	$ curl -H 'Accept: application/activity+json' https://hachyderm.io/users/quigs
//	401 {"error":"Request not signed"}
//
// That body unmarshals into an actor perfectly happily — one with no inbox.
// The row that landed in remoteusers therefore carried a handle and an actor
// id and nothing else, and because getActor answers from that row, every
// later delivery to the actor died in makeActivityPost with "target POST URL
// is empty". The Accept for a follow was never sent and the remote side sat
// on "pending" forever.
//
// Two things are tested here: that the fetch is signed, and that an actor
// with no inbox is a failed fetch rather than a row.

// hachydermUnsignedResponse is the body hachyderm.io returns for an unsigned
// actor GET, verbatim.
const hachydermUnsignedResponse = `{"error":"Request not signed"}`

// stubFetchActorIRI replaces the signed HTTP GET for one test.
func stubFetchActorIRI(t *testing.T, fn func(hostName, url string) ([]byte, error)) {
	t.Helper()
	orig := fetchActorIRI
	fetchActorIRI = fn
	t.Cleanup(func() { fetchActorIRI = orig })
}

func funcName(f interface{}) string {
	return runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
}

// TestActorFetchIsWiredToTheSignedFetch pins the wiring. Nothing else in the
// suite would notice a return to activityserve.NewRemoteActor or to any other
// unsigned GET, because a test peer answers an unsigned request as readily as
// a signed one — only the real fediverse does not.
func TestActorFetchIsWiredToTheSignedFetch(t *testing.T) {
	assert.Equal(t, funcName(fetchRemoteActor), funcName(newRemoteActor),
		"handle resolution must fetch actors through fetchRemoteActor")
	assert.Equal(t, funcName(resolveIRI), funcName(fetchActorIRI),
		"fetchRemoteActor must fetch through resolveIRI, which signs the request")
}

// TestActorFetchRequestIsSigned drives the request resolveIRI issues against a
// server that verifies it the way an authorized-fetch instance does.
//
// The request is built rather than issued, because resolveIRI refuses to fetch
// a loopback address (its SSRF guard) and a loopback address is the only kind
// an httptest.Server has. Everything the peer checks — the signature, the key
// it names, the Accept header — is in the request itself.
func TestActorFetchRequestIsSigned(t *testing.T) {
	app := newHandleTestApp(t)
	instanceColl = newInstanceColl(app)
	t.Cleanup(func() { instanceColl = nil })

	var (
		gotSignature bool
		gotAccept    string
		verifyErr    error
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotSignature = r.Header.Get("Signature") != ""

		v, err := fedsig.NewVerifier(r)
		if err != nil {
			verifyErr = err
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// The key the request names is the instance actor's, which the peer
		// would fetch. Here it is read straight out of the database.
		pubPEM, _ := app.db.GetAPActorKeys(0)
		pub, err := activitypub.DecodePublicKey(pubPEM)
		if err != nil {
			verifyErr = err
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		verifyErr = v.Verify(pub.(*rsa.PublicKey), fedsig.RSA_SHA256)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r, err := signedIRIRequest(app.cfg.App.Host, srv.URL+"/users/quigs")
	assert.NoError(t, err)
	resp, err := http.DefaultClient.Do(r)
	assert.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	assert.True(t, gotSignature, "the actor fetch must carry a Signature header")
	assert.Equal(t, "application/activity+json", gotAccept)
	assert.NoError(t, verifyErr, "an authorized-fetch peer must be able to verify the signature")
}

// TestFetchRemoteActorPropagatesFetchError covers the other half of the silent
// failure. resolveIRI now rejects a non-2xx response instead of returning the
// error body, and that rejection has to reach the caller rather than being
// logged and stepped over.
func TestFetchRemoteActorPropagatesFetchError(t *testing.T) {
	app := newHandleTestApp(t)
	stubFetchActorIRI(t, func(string, string) ([]byte, error) {
		return nil, assert.AnError
	})

	_, err := fetchRemoteActor(app, "https://hachyderm.io/users/quigs")
	assert.Error(t, err)
}

func TestFetchRemoteActorRejectsAnActorWithNoInbox(t *testing.T) {
	app := newHandleTestApp(t)
	stubFetchActorIRI(t, func(string, string) ([]byte, error) {
		return []byte(hachydermUnsignedResponse), nil
	})

	_, err := fetchRemoteActor(app, "https://hachyderm.io/users/quigs")

	assert.Error(t, err, "a body with no inbox is a failed fetch, not an actor")
	assert.Contains(t, err.Error(), "no inbox")
}

func TestFetchRemoteActorReadsInboxesAndURL(t *testing.T) {
	app := newHandleTestApp(t)
	stubFetchActorIRI(t, func(string, string) ([]byte, error) {
		return []byte(`{
			"id": "https://hachyderm.io/users/quigs",
			"inbox": "https://hachyderm.io/users/quigs/inbox",
			"url": "https://hachyderm.io/@quigs",
			"endpoints": {"sharedInbox": "https://hachyderm.io/inbox"}
		}`), nil
	})

	a, err := fetchRemoteActor(app, "https://hachyderm.io/users/quigs")

	assert.NoError(t, err)
	assert.Equal(t, "https://hachyderm.io/users/quigs/inbox", a.GetInbox())
	assert.Equal(t, "https://hachyderm.io/inbox", a.GetSharedInbox())
	assert.Equal(t, "https://hachyderm.io/@quigs", a.URL())
}

// An actor that publishes no endpoints falls back to its personal inbox, which
// is what activityserve did and what the INSERT relies on.
func TestFetchRemoteActorFallsBackToPersonalInbox(t *testing.T) {
	app := newHandleTestApp(t)
	stubFetchActorIRI(t, func(string, string) ([]byte, error) {
		return []byte(`{"inbox": "https://peer.example/u/a/inbox", "url": 42, "endpoints": "nope"}`), nil
	})

	a, err := fetchRemoteActor(app, "https://peer.example/u/a")

	assert.NoError(t, err, "a non-string url or endpoints value must not fail the fetch")
	assert.Equal(t, "https://peer.example/u/a/inbox", a.GetSharedInbox())
	assert.Empty(t, a.URL())
}

// The regression tests proper: the 401 must not reach remoteusers by either
// entry point.

func TestGetProfilePageFromHandleDoesNotCacheAnInboxlessActor(t *testing.T) {
	app := newHandleTestApp(t)
	stubRemoteLookup(t, func(string) string { return "https://hachyderm.io/users/quigs" })
	stubFetchActorIRI(t, func(string, string) ([]byte, error) {
		return []byte(hachydermUnsignedResponse), nil
	})

	actorIRI, err := app.db.GetProfilePageFromHandle(app, "@quigs@hachyderm.io")

	assert.Error(t, err)
	assert.Empty(t, actorIRI)
	assert.Equal(t, 0, countRemoteUsers(t, app),
		"an actor with no inbox must not be cached: the row is sticky and kills every later delivery")
}

func TestGetProfileURLFromHandleDoesNotCacheAnInboxlessActor(t *testing.T) {
	app := newHandleTestApp(t)
	stubRemoteLookup(t, func(string) string { return "https://hachyderm.io/users/quigs" })
	stubFetchActorIRI(t, func(string, string) ([]byte, error) {
		return []byte(hachydermUnsignedResponse), nil
	})

	profileURL, err := GetProfileURLFromHandle(app, "@quigs@hachyderm.io")

	assert.Error(t, err)
	assert.Empty(t, profileURL)
	assert.Equal(t, 0, countRemoteUsers(t, app))
}

func TestGetProfileURLFromHandleCachesAFetchedActor(t *testing.T) {
	// The other side of the guard: a real actor still lands, inbox and all.
	app := newHandleTestApp(t)
	stubRemoteLookup(t, func(string) string { return "https://hachyderm.io/users/quigs" })
	stubFetchActorIRI(t, func(string, string) ([]byte, error) {
		return []byte(`{
			"inbox": "https://hachyderm.io/users/quigs/inbox",
			"url": "https://hachyderm.io/@quigs",
			"endpoints": {"sharedInbox": "https://hachyderm.io/inbox"}
		}`), nil
	})

	profileURL, err := GetProfileURLFromHandle(app, "@quigs@hachyderm.io")

	assert.NoError(t, err)
	assert.Equal(t, "https://hachyderm.io/@quigs", profileURL)

	var inbox string
	assert.NoError(t, app.db.QueryRow("SELECT inbox FROM remoteusers WHERE actor_id = ?",
		"https://hachyderm.io/users/quigs").Scan(&inbox))
	assert.Equal(t, "https://hachyderm.io/users/quigs/inbox", inbox)
}

// TestGetActorRepairsACachedRowWithNoInbox is the self-healing half. An
// instance that already carries a poisoned row — written by a build that
// cached the 401 — must fix it on the next activity rather than needing
// manual SQL.
func TestGetActorRepairsACachedRowWithNoInbox(t *testing.T) {
	app := newHandleTestApp(t)
	const actorIRI = "https://hachyderm.io/users/quigs"
	_, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, handle) VALUES (?, ?, ?, ?)",
		actorIRI, "", "", "quigs@hachyderm.io")
	assert.NoError(t, err)

	stubFetchActorIRI(t, func(string, string) ([]byte, error) {
		return []byte(`{
			"inbox": "https://hachyderm.io/users/quigs/inbox",
			"endpoints": {"sharedInbox": "https://hachyderm.io/inbox"}
		}`), nil
	})

	actor, remoteUser, err := getActor(app, actorIRI)

	assert.NoError(t, err)
	assert.Equal(t, "https://hachyderm.io/users/quigs/inbox", actor.Inbox,
		"the Accept has to have somewhere to go, or the follow stays pending forever")
	assert.Equal(t, "https://hachyderm.io/users/quigs/inbox", remoteUser.Inbox)

	var inbox, sharedInbox string
	assert.NoError(t, app.db.QueryRow("SELECT inbox, shared_inbox FROM remoteusers WHERE actor_id = ?", actorIRI).
		Scan(&inbox, &sharedInbox))
	assert.Equal(t, "https://hachyderm.io/users/quigs/inbox", inbox, "the repair must be persisted")
	assert.Equal(t, "https://hachyderm.io/inbox", sharedInbox)
	assert.Equal(t, 1, countRemoteUsers(t, app), "the row is repaired, not duplicated")
}

// A healthy cached row is answered from the cache, with no network call.
func TestGetActorDoesNotRefetchAHealthyRow(t *testing.T) {
	app := newHandleTestApp(t)
	const actorIRI = "https://peer.example/u/a"
	_, err := app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox) VALUES (?, ?, ?)",
		actorIRI, actorIRI+"/inbox", "https://peer.example/inbox")
	assert.NoError(t, err)

	fetched := false
	stubFetchActorIRI(t, func(string, string) ([]byte, error) {
		fetched = true
		return nil, assert.AnError
	})

	actor, _, err := getActor(app, actorIRI)

	assert.NoError(t, err)
	assert.False(t, fetched, "a cached actor with an inbox must not be fetched again")
	assert.Equal(t, actorIRI+"/inbox", actor.Inbox)
}
