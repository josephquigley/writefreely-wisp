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
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/writeas/web-core/activitystreams"

	"github.com/writefreely/writefreely/config"
)

const announceTestHost = "https://local.example"

// newAnnounceTestApp builds a real, sqlite-backed App with the instance
// announce actor turned on, one user, and no collections yet.
//
// It is deliberately not single-user: single-user mode resolves every alias
// to collection 1, which would hide whether the instance actor is being
// resolved by alias or by accident.
func newAnnounceTestApp(t *testing.T) *App {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "writefreely.db")
	db, err := sql.Open("sqlite3", dbPath+"?parseTime=true&cached=shared")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := config.New()
	cfg.UseSQLite(true)
	cfg.Database.FileName = dbPath
	cfg.App.Host = announceTestHost
	cfg.App.SingleUser = false
	cfg.App.Federation = true
	cfg.App.InstanceAnnounce = true

	app := &App{
		db:  &datastore{DB: db, driverName: driverSQLite},
		cfg: cfg,
	}
	if err := adminInitDatabase(app); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if err := app.initFederationAllowlist(); err != nil {
		t.Fatalf("initFederationAllowlist: %v", err)
	}
	initActivityPub(app)

	u := &User{Username: "alice", HashedPass: []byte("x")}
	if err := app.db.CreateUser(cfg, u, "", ""); err != nil {
		t.Fatalf("create user: %v", err)
	}

	return app
}

// announceTestUserID returns the id of the user newAnnounceTestApp created.
func announceTestUserID(t *testing.T, app *App) int64 {
	t.Helper()
	var id int64
	if err := app.db.QueryRow("SELECT id FROM users WHERE username = ?", "alice").Scan(&id); err != nil {
		t.Fatalf("look up user: %v", err)
	}
	return id
}

// newTestCollection creates a blog at the given visibility and returns its id.
func newTestCollection(t *testing.T, app *App, alias string, vis collVisibility) int64 {
	t.Helper()
	userID := announceTestUserID(t, app)
	c, err := app.db.CreateCollection(app.cfg, alias, alias, userID)
	if err != nil {
		t.Fatalf("create collection %s: %v", alias, err)
	}
	if _, err := app.db.Exec("UPDATE collections SET privacy = ? WHERE id = ?", vis, c.ID); err != nil {
		t.Fatalf("set visibility on %s: %v", alias, err)
	}
	return c.ID
}

// newTestPost inserts a published post directly, so the test controls the id
// and the publication time.
func newTestPost(t *testing.T, app *App, collID int64, postID string, created time.Time) announceablePost {
	t.Helper()
	userID := announceTestUserID(t, app)
	_, err := app.db.Exec(`INSERT INTO posts
(id, slug, text_appearance, language, rtl, privacy, owner_id, collection_id, created, updated, view_count, title, content)
VALUES (?, ?, 'norm', 'en', 0, 1, ?, ?, ?, ?, 0, ?, ?)`,
		postID, postID, userID, collID, created, created, "Title "+postID, "Body of "+postID)
	if err != nil {
		t.Fatalf("insert post %s: %v", postID, err)
	}
	return announceablePost{ID: postID, Created: created}
}

// silenceTestUser suspends the user, which must stop their posts being
// announced instance-wide.
func silenceTestUser(t *testing.T, app *App) {
	t.Helper()
	if _, err := app.db.Exec("UPDATE users SET status = ? WHERE username = ?", UserSilenced, "alice"); err != nil {
		t.Fatalf("silence user: %v", err)
	}
}

// The instance actor must present as an Application. A server actor typed as
// a Person misrepresents what is behind it, and peers filter on the type.
func TestInstanceActorIsAnApplication(t *testing.T) {
	app := newAnnounceTestApp(t)

	p := newInstanceColl(app).PersonObject()

	assert.Equal(t, "Application", p.Type)
	assert.Equal(t, announceTestHost+"/api/collections/local.example", p.ID)
	assert.Equal(t, p.ID+"/inbox", p.Inbox)
	assert.Equal(t, p.ID+"/outbox", p.Outbox)
	assert.Equal(t, p.ID+"/followers", p.Followers)
	assert.Equal(t, p.ID+"/following", p.Following)
	assert.NotEmpty(t, p.PublicKey.PublicKeyPEM, "the instance actor must publish a key, or nothing it signs can be verified")
}

// An ordinary blog stays a Person. The type change is for the server actor
// alone, and a blog reading as an Application would break how clients present
// it.
func TestBlogActorIsStillAPerson(t *testing.T) {
	app := newAnnounceTestApp(t)
	newTestCollection(t, app, "quigs", CollPublic)

	c, err := app.db.GetCollection("quigs")
	assert.NoError(t, err)
	c.hostName = app.cfg.App.Host

	assert.Equal(t, "Person", c.PersonObject().Type)
}

// The Announce must reference the post by IRI and must not reuse the post's
// own id as the activity id.
func TestNewAnnounceAddressing(t *testing.T) {
	app := newAnnounceTestApp(t)
	actor := newInstanceColl(app).PersonObject()
	created := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	a := newAnnounce(app, actor, announceablePost{ID: "abc1234567", Created: created})

	iri := announceTestHost + "/api/posts/abc1234567"
	assert.Equal(t, "Announce", a.Type)
	assert.Equal(t, iri, a.Object, "the Announce must reference the post, not inline a copy of it under the server's name")
	assert.NotEqual(t, a.Object, a.ID, "an activity and the object it wraps are two objects and must not share an id")
	assert.Equal(t, iri+"#Announce", a.ID)
	assert.Equal(t, actor.ID, a.Actor)
	assert.Equal(t, []string{activitystreams.ToPublic}, a.To)
	assert.Equal(t, []string{actor.ID + "/followers"}, a.CC)
	assert.Equal(t, created, a.Published)
}

// The Announce object must serialise as a bare IRI string. Inlining the post
// would republish it attributed to the server rather than to its author.
func TestAnnounceSerialisesObjectAsIRI(t *testing.T) {
	app := newAnnounceTestApp(t)
	actor := newInstanceColl(app).PersonObject()

	b, err := json.Marshal(newAnnounce(app, actor, announceablePost{ID: "abc1234567"}))
	assert.NoError(t, err)

	var m map[string]interface{}
	assert.NoError(t, json.Unmarshal(b, &m))
	_, isString := m["object"].(string)
	assert.True(t, isString, "object must be an IRI string, got %T", m["object"])
}

// The eligibility rule, which is the whole privacy surface of this feature.
func TestInstanceAnnounceEligible(t *testing.T) {
	for _, tc := range []struct {
		name    string
		vis     collVisibility
		silence bool
		tweak   func(*config.AppCfg)
		want    bool
	}{
		{name: "public blog", vis: CollPublic, want: true},
		{name: "unlisted blog", vis: CollUnlisted, want: false},
		{name: "private blog", vis: CollPrivate, want: false},
		{name: "protected blog", vis: CollProtected, want: false},
		{name: "public but also protected", vis: CollPublic | CollProtected, want: false},
		{name: "silenced author", vis: CollPublic, silence: true, want: false},
		{
			name: "feature off",
			vis:  CollPublic,
			// The compatibility guarantee: with the flag off nothing is
			// announced, whatever else is configured.
			tweak: func(c *config.AppCfg) { c.InstanceAnnounce = false },
			want:  false,
		},
		{
			name:  "federation off",
			vis:   CollPublic,
			tweak: func(c *config.AppCfg) { c.Federation = false },
			want:  false,
		},
		{
			name: "private instance with no allowlist",
			vis:  CollPublic,
			// Same gate federatePost applies: a private instance with no
			// allowlist federates to nobody, and the relay is not an
			// exception to that.
			tweak: func(c *config.AppCfg) { c.Private = true },
			want:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newAnnounceTestApp(t)
			collID := newTestCollection(t, app, "quigs", tc.vis)
			if tc.silence {
				silenceTestUser(t, app)
			}
			if tc.tweak != nil {
				tc.tweak(&app.cfg.App)
			}

			assert.Equal(t, tc.want, instanceAnnounceEligible(app, collID))
		})
	}
}

// A collection id that does not resolve must not be announced. Failing open
// here would broadcast a post whose visibility could not be established.
func TestInstanceAnnounceEligibleRefusesUnknownCollection(t *testing.T) {
	app := newAnnounceTestApp(t)

	assert.False(t, instanceAnnounceEligible(app, 4242))
	assert.False(t, instanceAnnounceEligible(app, 0), "collection 0 is the instance actor itself, which writes no posts")
}

// A private instance WITH an allowlist does federate, to the allowlist. The
// relay must follow that rule rather than refuse outright.
func TestInstanceAnnounceEligibleOnPrivateInstanceWithAllowlist(t *testing.T) {
	app := newAnnounceTestApp(t)
	collID := newTestCollection(t, app, "quigs", CollPublic)
	app.cfg.App.Private = true
	app.cfg.App.FederationAllowlist = "example.org"
	if err := app.initFederationAllowlist(); err != nil {
		t.Fatalf("initFederationAllowlist: %v", err)
	}

	assert.True(t, instanceAnnounceEligible(app, collID))
}

// One delivery per inbox, not per follower.
func TestDistinctInboxes(t *testing.T) {
	followers := []RemoteUser{
		{ActorID: "https://a.example/users/1", Inbox: "https://a.example/users/1/inbox", SharedInbox: "https://a.example/inbox"},
		{ActorID: "https://a.example/users/2", Inbox: "https://a.example/users/2/inbox", SharedInbox: "https://a.example/inbox"},
		{ActorID: "https://b.example/users/1", Inbox: "https://b.example/users/1/inbox"},
		{ActorID: "https://c.example/users/1"},
	}

	got := distinctInboxes(&followers)

	assert.Equal(t, []string{"https://a.example/inbox", "https://b.example/users/1/inbox"}, got,
		"two followers behind one shared inbox are one delivery, and a follower with no inbox at all is not a destination")
}

// The outbox lists the same Announces delivery sends, and pages the way the
// per-blog outbox does.
func TestInstanceOutboxListsAnnounces(t *testing.T) {
	app := newAnnounceTestApp(t)
	pub := newTestCollection(t, app, "quigs", CollPublic)
	unlisted := newTestCollection(t, app, "quiet", CollUnlisted)

	// Backdated, because the outbox only lists posts already published:
	// the query filters on created <= now, as the local timeline does.
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		newTestPost(t, app, pub, fmt.Sprintf("public%04d", i), base.Add(time.Duration(i)*time.Minute))
	}
	newTestPost(t, app, unlisted, "unlisted001", base)

	// The bare collection reports the total.
	oc := map[string]interface{}{}
	fetchInstanceOutbox(t, app, "", &oc)
	assert.Equal(t, float64(3), oc["totalItems"], "an unlisted blog's post must not be counted in the instance outbox")
	assert.Equal(t, "OrderedCollection", oc["type"])

	// A page lists the Announces themselves, newest first.
	ocp := struct {
		Type         string             `json:"type"`
		TotalItems   int                `json:"totalItems"`
		OrderedItems []announceActivity `json:"orderedItems"`
	}{}
	fetchInstanceOutbox(t, app, "?page=1", &ocp)
	assert.Equal(t, "OrderedCollectionPage", ocp.Type)
	assert.Len(t, ocp.OrderedItems, 3)
	assert.Equal(t, announceTestHost+"/api/posts/public0002", ocp.OrderedItems[0].Object, "newest first")
	for _, item := range ocp.OrderedItems {
		assert.Equal(t, "Announce", item.Type)
		assert.NotContains(t, item.Object, "unlisted", "an unlisted blog's post must never appear in the instance outbox")
	}
}

// With the feature off the outbox is empty, so a peer is not told to expect a
// firehose that will never arrive.
func TestInstanceOutboxIsEmptyWhenFeatureOff(t *testing.T) {
	app := newAnnounceTestApp(t)
	pub := newTestCollection(t, app, "quigs", CollPublic)
	newTestPost(t, app, pub, "public0001", time.Now().UTC())
	app.cfg.App.InstanceAnnounce = false

	oc := map[string]interface{}{}
	fetchInstanceOutbox(t, app, "", &oc)

	assert.Equal(t, float64(0), oc["totalItems"])
}

// fetchInstanceOutbox calls the outbox handler for the instance actor and
// decodes the response into out.
func fetchInstanceOutbox(t *testing.T, app *App, query string, out interface{}) {
	t.Helper()
	c := newInstanceColl(app)
	r := httptest.NewRequest("GET", announceTestHost+"/api/collections/local.example/outbox"+query, nil)
	w := httptest.NewRecorder()

	if err := handleFetchInstanceOutbox(app, w, r, c); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	assert.Equal(t, http.StatusOK, w.Code)
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("decode outbox %q: %v", w.Body.String(), err)
	}
}

// Every ActivityPub endpoint must resolve the instance actor, not only the
// actor document. Before this change the actor advertised four endpoints and
// all four 404'd, because no blog is named after the host.
func TestCollectionForAPRequestResolvesInstanceActor(t *testing.T) {
	app := newAnnounceTestApp(t)
	newTestCollection(t, app, "quigs", CollPublic)

	c, err := collectionForAPRequest(app, "local.example")
	assert.NoError(t, err)
	assert.Equal(t, int64(0), c.ID)
	c.hostName = app.cfg.App.Host
	assert.True(t, c.IsInstanceColl())

	c, err = collectionForAPRequest(app, "quigs")
	assert.NoError(t, err)
	assert.NotEqual(t, int64(0), c.ID)

	_, err = collectionForAPRequest(app, "nope")
	assert.Error(t, err)
}

// The instance actor is decided by configuration, not by the Host header a
// request happens to carry.
func TestInstanceActorAliasComesFromConfig(t *testing.T) {
	cfg := config.New()
	cfg.App.Host = "https://blog.example.org"

	assert.Equal(t, "blog.example.org", instanceActorAlias(cfg))
}
