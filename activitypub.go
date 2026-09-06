/*
 * Copyright © 2018-2021 Musing Studio LLC.
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
	"crypto"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/writeas/activity/streams"
	"github.com/writeas/activityserve"
	"github.com/writeas/httpsig"
	"github.com/writeas/impart"
	"github.com/writeas/web-core/activitypub"
	"github.com/writeas/web-core/activitystreams"
	"github.com/writeas/web-core/id"
	"github.com/writeas/web-core/log"
	"github.com/writeas/web-core/silobridge"

	"github.com/writefreely/writefreely/config"
)

const (
	// TODO: delete. don't use this!
	apCustomHandleDefault = "blog"

	apCacheTime = time.Minute
)

var (
	apCollectionPostIRIRegex = regexp.MustCompile("/api/collections/([a-z0-9\\-]+)/posts/([a-z0-9\\-]+)$")
	apDraftPostIRIRegex      = regexp.MustCompile("/api/posts/([a-z0-9\\-]+)$")
)

var instanceColl *Collection

func initActivityPub(app *App) {
	instanceColl = newInstanceColl(app)

	// Say so at startup rather than leaving an operator to wonder why the
	// actor they enabled announces nothing. Same shape as the allowlist's
	// inert warning: configuration that cannot take effect is reported, not
	// treated as an error.
	if app.cfg.App.InstanceAnnounce && !app.cfg.App.Federation {
		log.Info(instanceAnnounceInertWarning)
	}
}

const instanceAnnounceInertWarning = "WARNING: instance_announce is enabled but federation = false: the instance actor will announce nothing until federation is enabled."

// newInstanceColl builds the pseudo-collection standing behind the
// instance-wide actor: the server's own ActivityPub identity, at
// <host>/api/collections/<host>.
//
// It is collection id 0, which is not a value the collections table ever
// issues, so it collides with no blog. That id is what lets the actor reuse
// the per-collection machinery unchanged: its keypair lives in collectionkeys
// under collection_id 0 (GetAPActorKeys generates it on first use), and its
// followers live in remotefollows under the same id, which works because that
// table carries no foreign key on collection_id.
//
// It is deliberately not persisted. The row would carry an owner id no user
// has, and every caller that loads a collection would then have to know to
// skip it. Building it from config instead keeps it absent from every query
// that enumerates blogs.
func newInstanceColl(app *App) *Collection {
	ur, _ := url.Parse(app.cfg.App.Host)
	return &Collection{
		ID:       0,
		Alias:    ur.Host,
		Title:    ur.Host,
		db:       app.db,
		hostName: app.cfg.App.Host,
	}
}

// instanceActorAlias is the alias that addresses the instance-wide actor: the
// host part of the configured App.Host.
//
// It comes from configuration rather than from the request's Host header.
// The actor id published in every signature and every activity is built from
// App.Host, so config is the only thing that decides which alias is really
// the instance actor; trusting the header would let a request served under
// some other name reach it.
func instanceActorAlias(cfg *config.Config) string {
	ur, err := url.Parse(cfg.App.Host)
	if err != nil {
		return ""
	}
	return ur.Host
}

// collectionForAPRequest resolves the collection an ActivityPub request
// addresses, mapping the instance actor's alias onto the pseudo-collection
// above and everything else onto a real blog.
//
// Every ActivityPub handler resolves its collection through here, so the
// instance actor cannot be reachable at one endpoint and missing at another
// — which is exactly the state the actor was in before: served as a document,
// but with an inbox, outbox, followers and following that all 404'd because
// no blog is named after the host.
func collectionForAPRequest(app *App, alias string) (*Collection, error) {
	if alias != "" && alias == instanceActorAlias(app.cfg) {
		return newInstanceColl(app), nil
	}
	if app.cfg.App.SingleUser {
		return app.db.GetCollectionByID(1)
	}
	return app.db.GetCollection(alias)
}

type RemoteUser struct {
	ID          int64
	ActorID     string
	Inbox       string
	SharedInbox string
	URL         string
	Handle      string
	Created     time.Time
}

func (ru *RemoteUser) CreatedFriendly() string {
	return ru.Created.Format("January 2, 2006")
}

func (ru *RemoteUser) EstimatedHandle() string {
	if ru.Handle != "" {
		return ru.Handle
	}
	username := filepath.Base(ru.ActorID)
	host, _ := url.Parse(ru.ActorID)
	return username + "@" + host.Host
}

func (ru *RemoteUser) AsPerson() *activitystreams.Person {
	return &activitystreams.Person{
		BaseObject: activitystreams.BaseObject{
			Type: "Person",
			Context: []interface{}{
				activitystreams.Namespace,
			},
			ID: ru.ActorID,
		},
		Inbox: ru.Inbox,
		Endpoints: activitystreams.Endpoints{
			SharedInbox: ru.SharedInbox,
		},
	}
}

func activityPubClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
	}
}

func handleFetchCollectionActivities(app *App, w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Server", serverSoftware)

	vars := mux.Vars(r)
	alias := vars["alias"]
	if alias == "" {
		alias = filepath.Base(r.RequestURI)
	}

	// Get base Collection data
	c, err := collectionForAPRequest(app, alias)
	if err != nil {
		return err
	}
	c.hostName = app.cfg.App.Host

	if !c.IsInstanceColl() {
		if c.IsPrivate() || c.IsProtected() {
			return ErrCollectionNotFound
		}
		silenced, err := app.db.IsUserSilenced(c.OwnerID)
		if err != nil {
			log.Error("fetch collection activities: %v", err)
			return ErrInternalGeneral
		}
		if silenced {
			return ErrCollectionNotFound
		}
	}

	p := c.PersonObject()

	setCacheControl(w, apCacheTime)
	return impart.RenderActivityJSON(w, p, http.StatusOK)
}

func handleFetchCollectionOutbox(app *App, w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Server", serverSoftware)

	vars := mux.Vars(r)
	alias := vars["alias"]

	// Get base Collection data
	c, err := collectionForAPRequest(app, alias)
	if err != nil {
		return err
	}
	c.hostName = app.cfg.App.Host

	if c.IsInstanceColl() {
		return handleFetchInstanceOutbox(app, w, r, c)
	}

	if c.IsPrivate() || c.IsProtected() {
		return ErrCollectionNotFound
	}
	silenced, err := app.db.IsUserSilenced(c.OwnerID)
	if err != nil {
		log.Error("fetch collection outbox: %v", err)
		return ErrInternalGeneral
	}
	if silenced {
		return ErrCollectionNotFound
	}

	if app.cfg.App.SingleUser {
		if alias != c.Alias {
			return ErrCollectionNotFound
		}
	}

	res := &CollectionObj{Collection: *c}
	app.db.GetPostsCount(res, false)
	accountRoot := c.FederatedAccount()

	page := r.FormValue("page")
	p, err := strconv.Atoi(page)
	if err != nil || p < 1 {
		// Return outbox
		oc := activitystreams.NewOrderedCollection(accountRoot, "outbox", res.TotalPosts)
		return impart.RenderActivityJSON(w, oc, http.StatusOK)
	}

	// Return outbox page
	ocp := activitystreams.NewOrderedCollectionPage(accountRoot, "outbox", res.TotalPosts, p)
	ocp.OrderedItems = []interface{}{}

	posts, err := app.db.GetPosts(app.cfg, c, p, false, true, false, "")
	for _, pp := range *posts {
		pp.Collection = res
		o := pp.ActivityObject(app)
		a := activitystreams.NewCreateActivity(o)
		// ActivityStreams 2.0 requires an id to identify exactly one
		// object, and an activity is a distinct object from the one it
		// wraps, so the two must not share an id.
		a.ID += "#Create"
		a.Context = nil
		ocp.OrderedItems = append(ocp.OrderedItems, *a)
	}

	setCacheControl(w, apCacheTime)
	return impart.RenderActivityJSON(w, ocp, http.StatusOK)
}

func handleFetchCollectionFollowers(app *App, w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Server", serverSoftware)

	vars := mux.Vars(r)
	alias := vars["alias"]

	// Get base Collection data
	c, err := collectionForAPRequest(app, alias)
	if err != nil {
		return err
	}
	c.hostName = app.cfg.App.Host

	// The instance actor has no owner to silence and no visibility of its
	// own: it is the server, and it is reachable exactly when federation is.
	if !c.IsInstanceColl() {
		if c.IsPrivate() || c.IsProtected() {
			return ErrCollectionNotFound
		}
		silenced, err := app.db.IsUserSilenced(c.OwnerID)
		if err != nil {
			log.Error("fetch collection followers: %v", err)
			return ErrInternalGeneral
		}
		if silenced {
			return ErrCollectionNotFound
		}
	}

	accountRoot := c.FederatedAccount()

	folls, err := app.db.GetAPFollowers(c)
	if err != nil {
		return err
	}

	page := r.FormValue("page")
	p, err := strconv.Atoi(page)
	if err != nil || p < 1 {
		// Return outbox
		oc := activitystreams.NewOrderedCollection(accountRoot, "followers", len(*folls))
		return impart.RenderActivityJSON(w, oc, http.StatusOK)
	}

	// Return outbox page
	ocp := activitystreams.NewOrderedCollectionPage(accountRoot, "followers", len(*folls), p)
	ocp.OrderedItems = []interface{}{}
	/*
		for _, f := range *folls {
			ocp.OrderedItems = append(ocp.OrderedItems, f.ActorID)
		}
	*/
	setCacheControl(w, apCacheTime)
	return impart.RenderActivityJSON(w, ocp, http.StatusOK)
}

func handleFetchCollectionFollowing(app *App, w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Server", serverSoftware)

	vars := mux.Vars(r)
	alias := vars["alias"]

	// Get base Collection data
	c, err := collectionForAPRequest(app, alias)
	if err != nil {
		return err
	}
	c.hostName = app.cfg.App.Host

	// The instance actor has no owner to silence and no visibility of its
	// own: it is the server, and it is reachable exactly when federation is.
	if !c.IsInstanceColl() {
		if c.IsPrivate() || c.IsProtected() {
			return ErrCollectionNotFound
		}
		silenced, err := app.db.IsUserSilenced(c.OwnerID)
		if err != nil {
			log.Error("fetch collection following: %v", err)
			return ErrInternalGeneral
		}
		if silenced {
			return ErrCollectionNotFound
		}
	}

	accountRoot := c.FederatedAccount()

	page := r.FormValue("page")
	p, err := strconv.Atoi(page)
	if err != nil || p < 1 {
		// Return outbox
		oc := activitystreams.NewOrderedCollection(accountRoot, "following", 0)
		return impart.RenderActivityJSON(w, oc, http.StatusOK)
	}

	// Return outbox page
	ocp := activitystreams.NewOrderedCollectionPage(accountRoot, "following", 0, p)
	ocp.OrderedItems = []interface{}{}
	setCacheControl(w, apCacheTime)
	return impart.RenderActivityJSON(w, ocp, http.StatusOK)
}

func handleFetchCollectionInbox(app *App, w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Server", serverSoftware)

	if app.federationAllowlistActive() {
		if err := app.verifyAllowlistedSignature(r); err != nil {
			return err
		}
	}

	vars := mux.Vars(r)
	alias := vars["alias"]
	c, err := collectionForAPRequest(app, alias)
	if err != nil {
		// TODO: return Reject?
		return err
	}
	c.hostName = app.cfg.App.Host

	// The instance actor has no owner, so there is no user to silence. Every
	// other check below applies to it unchanged.
	if !c.IsInstanceColl() {
		silenced, err := app.db.IsUserSilenced(c.OwnerID)
		if err != nil {
			log.Error("fetch collection inbox: %v", err)
			return ErrInternalGeneral
		}
		if silenced {
			return ErrCollectionNotFound
		}
	}

	if debugging {
		dump, err := httputil.DumpRequest(r, true)
		if err != nil {
			log.Error("Can't dump: %v", err)
		} else {
			log.Info("Rec'd! %q", dump)
		}
	}

	// Only call impart.RenderActivityJSON here if NO callback has already written a response.
	// Track whether a callback has written a response
	var responseWritten bool

	// Read raw body for debugging before decoding
	var rawBody bytes.Buffer
	tee := io.TeeReader(r.Body, &rawBody)

	var m map[string]any
	if err := json.NewDecoder(tee).Decode(&m); err != nil {
		log.Error("Failed decoding JSON: %v", err)
		log.Error("Raw body: %s", rawBody.String())
		return err
	}
	if debugging {
		log.Info("Decoded JSON: %v", m)
	}

	a := streams.NewAccept()
	// Give the Accept an id before any callback runs. ActivityStreams 2.0
	// requires every activity to have one, and a receiver that enforces it
	// refuses the delivery rather than ignoring the missing field: Mbin
	// answers 401 with `Missing required "id" field in the payload`. Setting
	// it here rather than inside a callback is what keeps every path
	// covered — the id belonged to the Follow callback, so the Accept sent
	// for an Undo Follow went out without one and was refused, and the
	// unfollow was never acknowledged.
	aID := c.FederatedAccount() + "#accept-" + id.GenerateFriendlyRandomString(20)
	acceptID, err := url.Parse(aID)
	if err != nil {
		log.Error("Couldn't parse generated Accept URL '%s': %v", aID, err)
	}
	a.SetId(acceptID)

	p := c.PersonObject()
	var to *url.URL
	var isFollow, isUnfollow, isLike, isUnlike bool
	var likePostID, unlikePostID string
	fullActor := &activitystreams.Person{}
	var remoteUser *RemoteUser

	res := &streams.Resolver{
		LikeCallback: func(l *streams.Like) error {
			isLike = true

			// 1) Use the Like concrete type here
			// 2) Errors are propagated to res.Deserialize call below
			m["@context"] = []string{activitystreams.Namespace}
			b, _ := json.Marshal(m)
			if debugging {
				log.Info("Like: %s", b)
			}

			_, likeID := l.GetId()
			if likeID == nil {
				log.Error("Didn't resolve Like ID")
			}
			if p := l.HasObject(0); p == streams.NoPresence {
				return fmt.Errorf("no object for Like activity at index 0")
			}

			obj := l.Raw().GetObjectIRI(0)
			/*
			   // TODO: handle this more robustly
			   l.ResolveObject(&streams.Resolver{
			     LinkCallback: func(link *streams.Link) error {
			       return nil
			     },
			   }, 0)
			*/

			if obj == nil {
				return fmt.Errorf("didn't get ObjectIRI to Like")
			}
			likePostID, err = parsePostIDFromURL(app, obj)
			if err != nil {
				return err
			}

			// Finally, get actor information
			_, from := l.GetActor(0)
			if from == nil {
				return fmt.Errorf("No valid actor string")
			}
			fullActor, remoteUser, err = getActor(app, from.String())
			if err != nil {
				return err
			}
			responseWritten = true
			return nil
		},
		FollowCallback: func(f *streams.Follow) error {
			isFollow = true

			// 1) Use the Follow concrete type here
			// 2) Errors are propagated to res.Deserialize call below
			m["@context"] = []string{activitystreams.Namespace}
			b, _ := json.Marshal(m)
			if debugging {
				log.Info("Follow: %s", b)
			}

			_, followID := f.GetId()
			if followID == nil {
				log.Error("Didn't resolve follow ID")
			}
			a.AppendObject(f.Raw())
			_, to = f.GetActor(0)
			obj := f.Raw().GetObjectIRI(0)
			if obj == nil {
				if debugging {
					log.Error("GetObjectIRI on Follow for actor is empty; trying object")
				}
				ao := f.Raw().GetObject(0)
				if ao == nil {
					log.Error("Fell back to GetObject and none parsed, so no actor ID! Follow request probably FAILED!")
				} else {
					obj = ao.GetId()
				}
			}
			a.AppendActor(obj)

			// First get actor information
			if to == nil {
				return fmt.Errorf("No valid `to` string")
			}
			fullActor, remoteUser, err = getActor(app, to.String())
			if err != nil {
				return err
			}
			responseWritten = true
			return impart.RenderActivityJSON(w, m, http.StatusOK)
		},
		UndoCallback: func(u *streams.Undo) error {
			m["@context"] = []string{activitystreams.Namespace}
			b, _ := json.Marshal(m)
			if debugging {
				log.Info("Undo: %s", b)
			}

			a.AppendObject(u.Raw())

			// Check type -- we handle Undo:Like and Undo:Follow
			_, err := u.ResolveObject(&streams.Resolver{
				LikeCallback: func(like *streams.Like) error {
					isUnlike = true

					_, from := like.GetActor(0)
					obj := like.Raw().GetObjectIRI(0)
					if obj == nil {
						return fmt.Errorf("didn't get ObjectIRI for Undo Like")
					}
					unlikePostID, err = parsePostIDFromURL(app, obj)
					if err != nil {
						return err
					}
					fullActor, remoteUser, err = getActor(app, from.String())
					if err != nil {
						return err
					}
					return nil
				},
				// TODO: add FollowCallback for more robust handling
			}, 0)
			if err != nil {
				return err
			}
			if isUnlike {
				return nil
			}

			isUnfollow = true
			_, to = u.GetActor(0)
			// TODO: get actor from object.object, not object
			obj := u.Raw().GetObjectIRI(0)
			a.AppendActor(obj)
			if to != nil {
				// Populate fullActor from DB?
				remoteUser, err = getRemoteUser(app, to.String())
				if err != nil {
					if iErr, ok := err.(*impart.HTTPError); ok {
						if iErr.Status == http.StatusNotFound {
							log.Error("No remoteuser info for Undo event!")
						}
					}
					return err
				} else {
					fullActor = remoteUser.AsPerson()
				}
			} else {
				log.Error("No to on Undo!")
			}
			responseWritten = true
			return impart.RenderActivityJSON(w, m, http.StatusOK)
		},
		DeleteCallback: func(d *streams.Delete) error {
			if debugging {
				b, _ := json.Marshal(m)
				log.Info("Delete: %s", b)
			}
			impart.RenderActivityJSON(w, m, http.StatusOK)
			responseWritten = true
			return nil
		},
	}
	if err := res.Deserialize(m); err != nil {
		// 3) Any errors from #2 can be handled, or the payload is an unknown type.
		log.Error("Unable to resolve Activity: %v", err)
		if debugging {
			log.Error("Map: %s", m)
		}
		if t, ok := m["type"]; ok {
			log.Error("Unhandled activity type: %v", t)
		}
		impart.RenderActivityJSON(w, "", http.StatusOK)
		return nil
	}

	// Handle synchronous activities
	if isLike {
		t, err := app.db.Begin()
		if err != nil {
			log.Error("Unable to start transaction: %v", err)
			return fmt.Errorf("unable to start transaction: %v", err)
		}

		var remoteUserID int64
		if remoteUser != nil {
			remoteUserID = remoteUser.ID
		} else {
			remoteUserID, err = apAddRemoteUser(app, t, fullActor)
		}

		// Add like
		_, err = t.Exec("INSERT INTO remote_likes (post_id, remote_user_id, created) VALUES (?, ?, "+app.db.now()+")", likePostID, remoteUserID)
		if err != nil {
			if !app.db.isDuplicateKeyErr(err) {
				t.Rollback()
				log.Error("Couldn't add like in DB: %v\n", err)
				return fmt.Errorf("Couldn't add like in DB: %v", err)
			} else {
				t.Rollback()
				log.Error("Couldn't add like in DB: %v\n", err)
				return fmt.Errorf("Couldn't add like in DB: %v", err)
			}
		}

		err = t.Commit()
		if err != nil {
			t.Rollback()
			log.Error("Rolling back after Commit(): %v\n", err)
			return fmt.Errorf("Rolling back after Commit(): %v\n", err)
		}

		if debugging {
			log.Info("Successfully liked post %s by remote user %s", likePostID, remoteUser.URL)
		}
		impart.RenderActivityJSON(w, "", http.StatusOK)
		return nil
	} else if isUnlike {
		t, err := app.db.Begin()
		if err != nil {
			log.Error("Unable to start transaction: %v", err)
			return fmt.Errorf("unable to start transaction: %v", err)
		}

		var remoteUserID int64
		if remoteUser != nil {
			remoteUserID = remoteUser.ID
		} else {
			remoteUserID, err = apAddRemoteUser(app, t, fullActor)
		}

		// Remove like
		_, err = t.Exec("DELETE FROM remote_likes WHERE post_id = ? AND remote_user_id = ?", unlikePostID, remoteUserID)
		if err != nil {
			t.Rollback()
			log.Error("Couldn't delete Like from DB: %v\n", err)
			return fmt.Errorf("Couldn't delete Like from DB: %v", err)
		}

		err = t.Commit()
		if err != nil {
			t.Rollback()
			log.Error("Rolling back after Commit(): %v\n", err)
			return fmt.Errorf("Rolling back after Commit(): %v\n", err)
		}

		if debugging {
			log.Info("Successfully un-liked post %s by remote user %s", unlikePostID, remoteUser.URL)
		}
		impart.RenderActivityJSON(w, "", http.StatusOK)
		return nil
	}

	go func() {
		// The 2s pause is the historical behaviour, and it is why this runs
		// in a goroutine at all: some peers are not ready to receive the
		// Accept the instant they sent the Follow.
		time.Sleep(2 * time.Second)
		acceptAndPersistFollow(app, c, p, a, to, fullActor, remoteUser, isFollow, isUnfollow)
	}()

	if !responseWritten {
		if debugging {
			log.Info("Received unhandled activity type, returning OK")
		}
		impart.RenderActivityJSON(w, "", http.StatusOK)
	}

	return nil
}

// actorPrivKey returns the decoded private key for an actor, refusing an empty
// key rather than handing it to DecodePrivateKey.
//
// web-core's DecodePrivateKey tests whether pem.Decode returned a nil block and
// then dereferences that same nil block while formatting the error, so an actor
// whose keypair was never generated panics its caller instead of receiving an
// error. Key generation can still fail, and an instance that ran a build whose
// generation was broken carries actors with no stored keypair, so an actor
// without a keypair is a reachable state rather than a theoretical one.
func actorPrivKey(p *activitystreams.Person) (crypto.PrivateKey, error) {
	k := p.GetPrivKey()
	if len(k) == 0 {
		return nil, fmt.Errorf("actor %s has no private key: its ActivityPub keypair was never generated", p.ID)
	}
	return activitypub.DecodePrivateKey(k)
}

// makeActivityPost delivers an activity to a remote inbox. It is the single
// point every outbound activity passes through, which is where the federation
// allowlist is enforced: no delivery path can reach a host that is not on the
// list.
func makeActivityPost(app *App, p *activitystreams.Person, url string, m interface{}) error {
	hostName := app.cfg.App.Host

	if !app.inboxAllowed(url) {
		return fmt.Errorf("refusing to post to %s: not on the federation allowlist", url)
	}

	if url == "" {
		log.Error("Target POST URL is empty! Person: %+v, Activity: %+v", p, m)
		return fmt.Errorf("target POST URL is empty")
	}

	log.Info("POST %s", url)
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}

	r, _ := http.NewRequest("POST", url, bytes.NewBuffer(b))
	r.Header.Add("Content-Type", "application/activity+json")
	r.Header.Set("User-Agent", ServerUserAgent(hostName))
	h := sha256.New()
	h.Write(b)
	r.Header.Add("Digest", "SHA-256="+base64.StdEncoding.EncodeToString(h.Sum(nil)))

	// Sign using the 'Signature' header
	privKey, err := actorPrivKey(p)
	if err != nil {
		return err
	}
	signer := httpsig.NewSigner(p.PublicKey.ID, privKey, httpsig.RSASHA256, []string{"(request-target)", "date", "host", "digest"})
	err = signer.SignSigHeader(r)
	if err != nil {
		log.Error("Can't sign: %v", err)
	}

	if debugging {
		dump, err := httputil.DumpRequestOut(r, true)
		if err != nil {
			log.Error("Can't dump: %v", err)
		} else {
			log.Info("%s", dump)
		}
	}

	resp, err := activityPubClient().Do(r)
	if err != nil {
		return err
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if debugging {
		log.Info("Status  : %s", resp.Status)
		log.Info("Response: %s", body)
	}

	return nil
}

// isPublicIRI reports whether iri is an http(s) URL whose host resolves
// exclusively to public, routable IP addresses. It rejects loopback,
// private, link-local (including cloud metadata endpoints like
// 169.254.169.254), and unspecified addresses to mitigate SSRF via
// attacker-supplied ActivityPub IRIs (e.g. inbox actor/object fields).
func isPublicIRI(iri string) error {
	u, err := url.Parse(iri)
	if err != nil {
		return fmt.Errorf("invalid IRI: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported IRI scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host in IRI")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("unable to resolve host %q: %v", host, err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("host %q resolves to disallowed address %s", host, ip)
		}
	}
	return nil
}

func resolveIRI(hostName, url string) ([]byte, error) {
	log.Info("GET %s", url)

	if err := isPublicIRI(url); err != nil {
		return nil, fmt.Errorf("refusing to fetch IRI: %v", err)
	}

	r, _ := http.NewRequest("GET", url, nil)
	r.Header.Add("Accept", "application/activity+json")
	r.Header.Set("User-Agent", ServerUserAgent(hostName))

	p := instanceColl.PersonObject()
	h := sha256.New()
	h.Write([]byte{})
	r.Header.Add("Digest", "SHA-256="+base64.StdEncoding.EncodeToString(h.Sum(nil)))

	// Sign using the 'Signature' header
	privKey, err := actorPrivKey(p)
	if err != nil {
		return nil, err
	}
	signer := httpsig.NewSigner(p.PublicKey.ID, privKey, httpsig.RSASHA256, []string{"(request-target)", "date", "host", "digest"})
	err = signer.SignSigHeader(r)
	if err != nil {
		log.Error("Can't sign: %v", err)
	}

	if debugging {
		dump, err := httputil.DumpRequestOut(r, true)
		if err != nil {
			log.Error("Can't dump: %v", err)
		} else {
			log.Info("%s", dump)
		}
	}

	resp, err := activityPubClient().Do(r)
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if debugging {
		log.Info("Status  : %s", resp.Status)
		log.Info("Response: %s", body)
	}

	return body, nil
}

func deleteFederatedPost(app *App, p *PublicPost, collID int64) error {
	if debugging {
		log.Info("Deleting federated post!")
	}

	// Retract the instance-wide Announce. The Delete below reaches the blog's
	// followers; anyone who followed only the instance actor never sees it,
	// and would keep a boost of a post that no longer exists.
	go undoAnnounceToInstanceFollowers(app, announceablePost{ID: p.ID, Created: p.Created}, collID)

	p.Collection.hostName = app.cfg.App.Host
	actor := p.Collection.PersonObject(collID)
	na := p.ActivityObject(app)

	// Send the Delete to instance followers too, before the loop below
	// rewrites na.CC. The Undo above retracts the boost; this removes the
	// cached object, which is the thing a receiver actually rendered.
	fanOutDeleteToInstanceFollowers(app, na, actor, collID)

	// Add followers
	p.Collection.ID = collID
	followers, err := app.db.GetAPFollowers(&p.Collection.Collection)
	if err != nil {
		log.Error("Couldn't delete post (get followers)! %v", err)
		return err
	}

	inboxes := map[string][]string{}
	for _, f := range *followers {
		inbox := f.SharedInbox
		if inbox == "" {
			inbox = f.Inbox
		}
		if _, ok := inboxes[inbox]; ok {
			inboxes[inbox] = append(inboxes[inbox], f.ActorID)
		} else {
			inboxes[inbox] = []string{f.ActorID}
		}
	}

	for si, instFolls := range inboxes {
		na.CC = []string{}
		na.CC = append(na.CC, instFolls...)
		da := activitystreams.NewDeleteActivity(na)
		// ActivityStreams 2.0 requires an id to identify exactly one
		// object, and an activity is a distinct object from the one it
		// wraps, so the two must not share an id.
		// See: https://git.pleroma.social/pleroma/pleroma/issues/1481
		da.ID += "#Delete"

		err = makeActivityPost(app, actor, si, da)
		if err != nil {
			log.Error("Couldn't delete post! %v", err)
		}
	}
	return nil
}

func federatePost(app *App, p *PublicPost, collID int64, isUpdate bool) error {
	// A private instance does not federate. With a federation allowlist
	// configured it does, but only to the hosts on that list, which
	// makeActivityPost enforces.
	if app.cfg.App.Private && !app.federationAllowlistActive() {
		return nil
	}

	// Do not federate posts from private or protected blogs
	if p.Collection.Visibility == CollPrivate || p.Collection.Visibility == CollProtected {
		return nil
	}

	if debugging {
		if isUpdate {
			log.Info("Federating updated post!")
		} else {
			log.Info("Federating new post!")
		}
	}

	actor := p.Collection.PersonObject(collID)
	na := p.ActivityObject(app)

	// An edit gets one activity id, shared by every recipient. Computing it
	// per shared inbox would hand each of them a different id whenever
	// p.Updated is zero, and a receiver that dedupes by id would then render
	// one edit once per instance it reached.
	updateTime := time.Now()
	if !p.Updated.IsZero() {
		updateTime = p.Updated
	}

	// Fan out to the instance actor's followers as well. This happens here,
	// before the loop below starts rewriting na.CC with one blog's follower
	// list, so what instance followers receive carries the post's own
	// addressing. It re-checks eligibility itself.
	fanOutPostToInstanceFollowers(app, p, na, actor, collID, isUpdate, updateTime)

	// Add followers
	p.Collection.ID = collID
	followers, err := app.db.GetAPFollowers(&p.Collection.Collection)
	if err != nil {
		log.Error("Couldn't post! %v", err)
		return err
	}
	log.Info("Followers for %d: %+v", collID, followers)

	inboxes := map[string][]string{}
	for _, f := range *followers {
		inbox := f.SharedInbox
		if inbox == "" {
			inbox = f.Inbox
		}
		if _, ok := inboxes[inbox]; ok {
			// check if we're already sending to this shared inbox
			inboxes[inbox] = append(inboxes[inbox], f.ActorID)
		} else {
			// add the new shared inbox to the list
			inboxes[inbox] = []string{f.ActorID}
		}
	}

	var activity *activitystreams.Activity
	// for each one of the shared inboxes
	for si, instFolls := range inboxes {
		// add all followers from that instance
		// to the CC field
		na.CC = []string{}
		na.CC = append(na.CC, instFolls...)
		// create a new "Create" activity
		// with our article as object
		label := "Create"
		if isUpdate {
			label = "Update"
			na.Updated = &p.Updated
			activity = activitystreams.NewUpdateActivity(na)
			// ActivityStreams 2.0 requires an id to identify exactly one
			// object, and an activity is a distinct object from the one
			// it wraps, so the two must not share an id. Unlike
			// Create, which happens once per post, Update happens on every
			// edit, so a static suffix would give every edit's activity the
			// same id as the first one. Receivers dedupe activities by id,
			// so that would silently drop every edit after the first
			// instead of fixing anything. Use the post's updated timestamp
			// so each edit gets a distinct id. It is computed once, above, so
			// that every recipient of one edit sees the same id.
			activity.ID += fmt.Sprintf("#Update/%d", updateTime.Unix())
		} else {
			activity = activitystreams.NewCreateActivity(na)
			// ActivityStreams 2.0 requires an id to identify exactly one
			// object, and an activity is a distinct object from the one it
			// wraps, so the two must not share an id.
			activity.ID += "#Create"
			activity.To = na.To
			activity.CC = na.CC
		}
		// and post it to that sharedInbox
		if debugging {
			logOutgoingActivity(label, activity)
		}
		err = makeActivityPost(app, actor, si, activity)
		if err != nil {
			log.Error("Couldn't post! %v", err)
		}
	}

	// re-create the object so that the CC list gets reset and has
	// the mentioned users. This might seem wasteful but the code is
	// cleaner than adding the mentioned users to CC here instead of
	// in p.ActivityObject()
	na = p.ActivityObject(app)
	for _, tag := range na.Tag {
		if tag.Type == "Mention" {
			activity = activitystreams.NewCreateActivity(na)
			// ActivityStreams 2.0 requires an id to identify exactly one
			// object, and an activity is a distinct object from the one it
			// wraps, so the two must not share an id.
			activity.ID += "#Create"
			activity.To = na.To
			activity.CC = na.CC
			// This here might be redundant in some cases as we might have already
			// sent this to the sharedInbox of this instance above, but we need too
			// much logic to catch this at the expense of the odd extra request.
			// I don't believe we'd ever have too many mentions in a single post that this
			// could become a burden.
			remoteUser, err := getRemoteUser(app, tag.HRef)
			if err != nil {
				log.Error("Unable to find remote user %s. Skipping: %v", tag.HRef, err)
				continue
			}
			err = makeActivityPost(app, actor, remoteUser.Inbox, activity)
			if err != nil {
				log.Error("Couldn't post! %v", err)
			}
		}
	}

	return nil
}

func getRemoteUser(app *App, actorID string) (*RemoteUser, error) {
	u := RemoteUser{ActorID: actorID}
	var urlVal, handle sql.NullString
	err := app.db.QueryRow("SELECT id, inbox, shared_inbox, url, handle FROM remoteusers WHERE actor_id = ?", actorID).Scan(&u.ID, &u.Inbox, &u.SharedInbox, &urlVal, &handle)
	switch {
	case err == sql.ErrNoRows:
		return nil, impart.HTTPError{http.StatusNotFound, "No remote user with that ID."}
	case err != nil:
		log.Error("Couldn't get remote user %s: %v", actorID, err)
		return nil, err
	}

	u.URL = urlVal.String
	u.Handle = handle.String

	return &u, nil
}

// getRemoteUserFromHandle retrieves the profile page of a remote user
// from the @user@server.tld handle
func getRemoteUserFromHandle(app *App, handle string) (*RemoteUser, error) {
	u := RemoteUser{Handle: handle}
	var urlVal sql.NullString
	err := app.db.QueryRow("SELECT id, actor_id, inbox, shared_inbox, url FROM remoteusers WHERE handle = ?", handle).Scan(&u.ID, &u.ActorID, &u.Inbox, &u.SharedInbox, &urlVal)
	switch {
	case err == sql.ErrNoRows:
		return nil, ErrRemoteUserNotFound
	case err != nil:
		log.Error("Couldn't get remote user %s: %v", handle, err)
		return nil, err
	}
	u.URL = urlVal.String
	return &u, nil
}

// getRemoteUserFromURL retrieves a RemoteUser from their public profile URL.
func getRemoteUserFromURL(app *App, urlStr string) (*RemoteUser, error) {
	u := RemoteUser{URL: urlStr}
	var urlVal, handle sql.NullString
	err := app.db.QueryRow("SELECT id, actor_id, inbox, shared_inbox, url, handle FROM remoteusers WHERE url = ?", urlStr).Scan(&u.ID, &u.ActorID, &u.Inbox, &u.SharedInbox, &urlVal, &handle)
	switch {
	case err == sql.ErrNoRows:
		return nil, ErrRemoteUserNotFound
	case err != nil:
		log.Error("Couldn't get remote user from URL %s: %v", urlStr, err)
		return nil, err
	}
	u.URL = urlVal.String
	u.Handle = handle.String
	return &u, nil
}

func getActor(app *App, actorIRI string) (*activitystreams.Person, *RemoteUser, error) {
	log.Info("Fetching actor %s locally", actorIRI)
	actor := &activitystreams.Person{}
	remoteUser, err := getRemoteUser(app, actorIRI)
	if err != nil {
		if iErr, ok := err.(impart.HTTPError); ok {
			if iErr.Status == http.StatusNotFound {
				// Fetch remote actor
				log.Info("Not found; fetching actor %s remotely", actorIRI)
				actorResp, err := resolveIRI(app.cfg.App.Host, actorIRI)
				if err != nil {
					log.Error("Unable to get base actor! %v", err)
					return nil, nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't fetch actor."}
				}
				if err := unmarshalActor(actorResp, actor); err != nil {
					log.Error("Unable to unmarshal base actor! %v", err)
					return nil, nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't parse actor."}
				}
				baseActor := &activitystreams.Person{}
				if err := unmarshalActor(actorResp, baseActor); err != nil {
					log.Error("Unable to unmarshal actual actor! %v", err)
					return nil, nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't parse actual actor."}
				}
				// Fetch the actual actor using the owner field from the publicKey object
				actualActorResp, err := resolveIRI(app.cfg.App.Host, baseActor.PublicKey.Owner)
				if err != nil {
					log.Error("Unable to get actual actor! %v", err)
					return nil, nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't fetch actual actor."}
				}
				if err := unmarshalActor(actualActorResp, actor); err != nil {
					log.Error("Unable to unmarshal actual actor! %v", err)
					return nil, nil, impart.HTTPError{http.StatusInternalServerError, "Couldn't parse actual actor."}
				}
			} else {
				return nil, nil, err
			}
		} else {
			return nil, nil, err
		}
	} else {
		actor = remoteUser.AsPerson()
	}
	return actor, remoteUser, nil
}

func GetProfileURLFromHandle(app *App, handle string) (string, error) {
	handle = strings.TrimLeft(handle, "@")
	actorIRI := ""
	parts := strings.Split(handle, "@")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid handle format")
	}
	domain := parts[1]

	// Check non-AP instances
	if siloProfileURL := silobridge.Profile(parts[0], domain); siloProfileURL != "" {
		return siloProfileURL, nil
	}

	remoteUser, err := getRemoteUserFromHandle(app, handle)
	if err != nil {
		// can't find using handle in the table but the table may already have this user without
		// handle from a previous version
		// TODO: Make this determination. We should know whether a user exists without a handle, or doesn't exist at all
		actorIRI = RemoteLookup(handle)
		_, errRemoteUser := getRemoteUser(app, actorIRI)
		// if it exists then we need to update the handle
		if errRemoteUser == nil {
			_, err := app.db.Exec("UPDATE remoteusers SET handle = ? WHERE actor_id = ?", handle, actorIRI)
			if err != nil {
				log.Error("Couldn't update handle '%s' for user %s", handle, actorIRI)
			}
		} else {
			// this probably means we don't have the user in the table so let's try to insert it
			// here we need to ask the server for the inboxes
			remoteActor, err := activityserve.NewRemoteActor(actorIRI)
			if err != nil {
				log.Error("Couldn't fetch remote actor: %v", err)
			}
			if debugging {
				log.Info("Got remote actor: %s %s %s %s %s", actorIRI, remoteActor.GetInbox(), remoteActor.GetSharedInbox(), remoteActor.URL(), handle)
			}
			_, err = app.db.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, url, handle) VALUES(?, ?, ?, ?, ?)", actorIRI, remoteActor.GetInbox(), remoteActor.GetSharedInbox(), remoteActor.URL(), handle)
			if err != nil {
				log.Error("Couldn't insert remote user: %v", err)
				return "", err
			}
			actorIRI = remoteActor.URL()
		}
	} else if remoteUser.URL == "" {
		log.Info("Remote user %s URL empty, fetching", remoteUser.ActorID)
		newRemoteActor, err := activityserve.NewRemoteActor(remoteUser.ActorID)
		if err != nil {
			log.Error("Couldn't fetch remote actor: %v", err)
		} else {
			_, err := app.db.Exec("UPDATE remoteusers SET url = ? WHERE actor_id = ?", newRemoteActor.URL(), remoteUser.ActorID)
			if err != nil {
				log.Error("Couldn't update handle '%s' for user %s", handle, actorIRI)
			} else {
				actorIRI = newRemoteActor.URL()
			}
		}
	} else {
		actorIRI = remoteUser.URL
	}
	return actorIRI, nil
}

// unmarshal actor normalizes the actor response to conform to
// the type Person from github.com/writeas/web-core/activitysteams
//
// some implementations return different context field types
// this converts any non-slice contexts into a slice
func unmarshalActor(actorResp []byte, actor *activitystreams.Person) error {
	// FIXME: Hubzilla has an object for the Actor's url: cannot unmarshal object into Go struct field Person.url of type string

	// flexActor overrides the Context field to allow
	// all valid representations during unmarshal
	flexActor := struct {
		activitystreams.Person
		Context json.RawMessage `json:"@context,omitempty"`
	}{}
	if err := json.Unmarshal(actorResp, &flexActor); err != nil {
		return err
	}

	actor.Endpoints = flexActor.Endpoints
	actor.Followers = flexActor.Followers
	actor.Following = flexActor.Following
	actor.ID = flexActor.ID
	actor.Icon = flexActor.Icon
	actor.Inbox = flexActor.Inbox
	actor.Name = flexActor.Name
	actor.Outbox = flexActor.Outbox
	actor.PreferredUsername = flexActor.PreferredUsername
	actor.PublicKey = flexActor.PublicKey
	actor.Summary = flexActor.Summary
	actor.Type = flexActor.Type
	actor.URL = flexActor.URL

	func(val interface{}) {
		switch val.(type) {
		case []interface{}:
			// already a slice, do nothing
			actor.Context = val.([]interface{})
		default:
			actor.Context = []interface{}{val}
		}
	}(flexActor.Context)

	return nil
}

func parsePostIDFromURL(app *App, u *url.URL) (string, error) {
	// Get post ID from URL
	var collAlias, slug, postID string
	if m := apCollectionPostIRIRegex.FindStringSubmatch(u.String()); len(m) == 3 {
		collAlias = m[1]
		slug = m[2]
	} else if m = apDraftPostIRIRegex.FindStringSubmatch(u.String()); len(m) == 2 {
		postID = m[1]
	} else {
		return "", fmt.Errorf("unable to match objectIRI: %s", u)
	}

	// Get postID if all we have is collection and slug
	if collAlias != "" && slug != "" {
		c, err := app.db.GetCollection(collAlias)
		if err != nil {
			return "", err
		}
		p, err := app.db.GetPost(slug, c.ID)
		if err != nil {
			return "", err
		}
		postID = p.ID
	}

	return postID, nil
}

func setCacheControl(w http.ResponseWriter, ttl time.Duration) {
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%.0f", ttl.Seconds()))
}

func logOutgoingActivity(label string, activity any) {
	b, err := json.MarshalIndent(activity, "", "  ")
	if err != nil {
		log.Error("Failed to marshal %s activity: %v", label, err)
		return
	}
	log.Info("%s outgoing ActivityPub payload:\n%s", label, string(b))
}

// acceptAndPersistFollow delivers the Accept for a Follow or an Undo Follow
// and records the resulting change to the follower list.
//
// It is a named function rather than the closure it used to be so that a test
// can run it directly. As a closure inside an anonymous goroutine it was
// unreachable except by sending a real Follow to a real inbox and then
// sleeping longer than the handler does, which is why the follow path had no
// test. The sleep stays at the call site: the delay is delivery politeness,
// not part of what this does.
//
// c may be the instance-wide collection, in which case the follow is recorded
// against collection id 0 and the follower becomes a follower of the whole
// instance. Nothing here needs to know the difference.
func acceptAndPersistFollow(app *App, c *Collection, p *activitystreams.Person, a *streams.Accept, to *url.URL, fullActor *activitystreams.Person, remoteUser *RemoteUser, isFollow, isUnfollow bool) {
	if to == nil {
		if debugging {
			log.Info("No `to` value: likely not needed for this activity type.")
		}
		return
	}

	// Persist the follow before attempting delivery of the Accept.
	// The remote already believes it is following once it gets a 200
	// on the inbox POST, so it must be recorded here regardless of
	// whether the Accept below is delivered successfully. Nothing in
	// this block depends on the Accept's serialization or delivery:
	// fullActor, remoteUser and c.ID were all populated synchronously
	// in the Follow callback, before this goroutine was even started.
	if isFollow {
		t, err := app.db.Begin()
		if err != nil {
			log.Error("Unable to start transaction: %v", err)
			return
		}

		var followerID int64

		if remoteUser != nil {
			followerID = remoteUser.ID
		} else {
			// TODO: use apAddRemoteUser() here, instead!
			// Add follower locally, since it wasn't found before
			res, err := t.Exec("INSERT INTO remoteusers (actor_id, inbox, shared_inbox, url) VALUES (?, ?, ?, ?)", fullActor.ID, fullActor.Inbox, fullActor.Endpoints.SharedInbox, fullActor.URL)
			if err != nil {
				// if duplicate key, res will be nil and panic on
				// res.LastInsertId below
				t.Rollback()
				log.Error("Couldn't add new remoteuser in DB: %v\n", err)
				return
			}

			followerID, err = res.LastInsertId()
			if err != nil {
				t.Rollback()
				log.Error("no lastinsertid for followers, rolling back: %v", err)
				return
			}

			// Add in key
			_, err = t.Exec("INSERT INTO remoteuserkeys (id, remote_user_id, public_key) VALUES (?, ?, ?)", fullActor.PublicKey.ID, followerID, fullActor.PublicKey.PublicKeyPEM)
			if err != nil {
				if !app.db.isDuplicateKeyErr(err) {
					t.Rollback()
					log.Error("Couldn't add follower keys in DB: %v\n", err)
					return
				}
			}
		}

		// Add follow
		_, err = t.Exec("INSERT INTO remotefollows (collection_id, remote_user_id, created) VALUES (?, ?, "+app.db.now()+")", c.ID, followerID)
		if err != nil {
			if !app.db.isDuplicateKeyErr(err) {
				t.Rollback()
				log.Error("Couldn't add follower in DB: %v\n", err)
				return
			}
		}

		err = t.Commit()
		if err != nil {
			t.Rollback()
			log.Error("Rolling back after Commit(): %v\n", err)
			return
		}
	}

	am, err := a.Serialize()
	if err != nil {
		log.Error("Unable to serialize Accept: %v", err)
		return
	}
	am["@context"] = []string{activitystreams.Namespace}
	if debugging {
		logOutgoingActivity("Accept", am)
	}

	err = makeActivityPost(app, p, fullActor.Inbox, am)
	if err != nil {
		log.Error("Unable to make activity POST: %v", err)
		return
	}

	if isUnfollow {
		// Remove follower locally
		_, err = app.db.Exec("DELETE FROM remotefollows WHERE collection_id = ? AND remote_user_id = (SELECT id FROM remoteusers WHERE actor_id = ?)", c.ID, to.String())
		if err != nil {
			log.Error("Couldn't remove follower from DB: %v\n", err)
		}
	}
}
