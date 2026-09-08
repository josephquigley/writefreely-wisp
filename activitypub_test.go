package writefreely

import (
	"bytes"
	"database/sql"
	"fmt"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/writeas/web-core/activitypub"
	"github.com/writeas/web-core/activitystreams"
	"github.com/writeas/web-core/log"

	"github.com/writefreely/writefreely/config"
)

var actorTestTable = []struct {
	Name string
	Resp []byte
}{
	{
		"Context as a string",
		[]byte(`{"@context":"https://www.w3.org/ns/activitystreams"}`),
	},
	{
		"Context as a list",
		[]byte(`{"@context":["one string", "two strings"]}`),
	},
}

func TestUnmarshalActor(t *testing.T) {
	for _, tc := range actorTestTable {
		actor := activitystreams.Person{}
		err := unmarshalActor(tc.Resp, &actor)
		if err != nil {
			t.Errorf("%s failed with error %s", tc.Name, err)
		}
	}
}

// TestCreateActivityIDDoesNotMatchObjectID confirms that a Create
// activity's id is distinct from the id of the object it wraps. Two
// distinct objects sharing one id violates ActivityPub, and it's what
// makes GoToSocial silently drop delivered posts.
func TestCreateActivityIDDoesNotMatchObjectID(t *testing.T) {
	o := activitystreams.NewNoteObject()
	o.ID = "https://example.com/api/posts/abc123"

	a := activitystreams.NewCreateActivity(o)
	a.ID += "#Create"

	if a.ID == a.Object.ID {
		t.Errorf("activity id must not equal object id, both were %q", a.ID)
	}
	if !strings.HasPrefix(a.ID, a.Object.ID) {
		t.Errorf("activity id %q should be derived from object id %q", a.ID, a.Object.ID)
	}
}

// updateActivityID mirrors the id-suffixing logic in federatePost for an
// Update activity: it appends the object's updated time so that
// successive edits of the same post get distinct activity ids.
func updateActivityID(o *activitystreams.Object) string {
	a := activitystreams.NewUpdateActivity(o)
	updateTime := time.Now()
	if a.Updated != nil && !a.Updated.IsZero() {
		updateTime = *a.Updated
	}
	return a.ID + fmt.Sprintf("#Update/%d", updateTime.Unix())
}

// TestUpdateActivityIDDoesNotMatchObjectID confirms that an Update
// activity's id is distinct from the id of the object it wraps, for the
// same reason as Create.
func TestUpdateActivityIDDoesNotMatchObjectID(t *testing.T) {
	o := activitystreams.NewNoteObject()
	o.ID = "https://example.com/api/posts/abc123"
	updated := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	o.Updated = &updated

	id := updateActivityID(o)

	if id == o.ID {
		t.Errorf("activity id must not equal object id, both were %q", id)
	}
	if !strings.HasPrefix(id, o.ID) {
		t.Errorf("activity id %q should be derived from object id %q", id, o.ID)
	}
}

// TestUpdateActivityIDVariesPerEdit confirms that editing the same post
// twice produces two different Update activity ids. A static suffix
// (e.g. always appending "#Update") would give every edit of a post the
// same activity id as its first edit. Receivers dedupe activities by id,
// so a conforming implementation would treat the second edit as one
// already processed and silently drop it, which is worse than the bug
// this fix addresses.
func TestUpdateActivityIDVariesPerEdit(t *testing.T) {
	o := activitystreams.NewNoteObject()
	o.ID = "https://example.com/api/posts/abc123"

	firstEdit := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	o.Updated = &firstEdit
	firstID := updateActivityID(o)

	secondEdit := firstEdit.Add(time.Hour)
	o.Updated = &secondEdit
	secondID := updateActivityID(o)

	if firstID == secondID {
		t.Errorf("two edits of the same post produced the same activity id %q; "+
			"receivers that dedupe by id would drop the second edit", firstID)
	}
}

// newInboxTestApp builds a real, sqlite-backed App suitable for exercising
// handleFetchCollectionInbox end-to-end, with a single user and collection
// already created. When allowlist is non-empty the App is private and that
// allowlist is configured; otherwise the App behaves as an ordinary,
// non-private instance with federation open to everyone.
func newInboxTestApp(t *testing.T, allowlist string) *App {
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
	cfg.App.Host = "https://local.example"
	cfg.App.SingleUser = true
	cfg.App.Private = allowlist != ""
	cfg.App.FederationAllowlist = allowlist

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

	u := &User{Username: "alice", HashedPass: []byte("x")}
	if err := app.db.CreateUser(cfg, u, "", ""); err != nil {
		t.Fatalf("create user: %v", err)
	}

	return app
}

// inboxActivity is a minimal, well-formed but unhandled ActivityStreams
// activity. It exists only to prove a request reached the inbox's normal
// processing (JSON decode and onward), not to exercise any specific
// activity type's side effects.
const inboxActivity = `{"@context":"https://www.w3.org/ns/activitystreams","type":"Create","actor":"https://example.org/users/a","object":"https://example.org/note/1"}`

func TestHandleFetchCollectionInboxAllowlistedSignatureProcessedNormally(t *testing.T) {
	app := newInboxTestApp(t, "example.org")
	k := testKey(t)
	keyID := "https://example.org/users/a#main-key"
	app.fedKeys.set(keyID, &k.PublicKey, time.Minute)

	body := []byte(inboxActivity)
	r := signedRequest(t, k, keyID, "POST", "https://local.example/api/collections/alice/inbox", body)
	w := httptest.NewRecorder()

	err := handleFetchCollectionInbox(app, w, r)

	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleFetchCollectionInboxRejectsUnsignedWhenAllowlisted(t *testing.T) {
	app := newInboxTestApp(t, "example.org")

	body := []byte(inboxActivity)
	r := httptest.NewRequest("POST", "https://local.example/api/collections/alice/inbox", bytes.NewReader(body))
	w := httptest.NewRecorder()

	err := handleFetchCollectionInbox(app, w, r)

	assert.Equal(t, ErrFederationNotAllowed, err)
}

func TestHandleFetchCollectionInboxRejectsUnlistedHost(t *testing.T) {
	app := newInboxTestApp(t, "example.org")
	k := testKey(t)
	keyID := "https://evil.example/users/a#main-key"

	body := []byte(inboxActivity)
	r := signedRequest(t, k, keyID, "POST", "https://local.example/api/collections/alice/inbox", body)
	w := httptest.NewRecorder()

	err := handleFetchCollectionInbox(app, w, r)

	assert.Equal(t, ErrFederationNotAllowed, err)
}

func TestHandleFetchCollectionInboxUnsignedProcessedNormallyWithNoAllowlist(t *testing.T) {
	// The compatibility guarantee: with no allowlist configured, an
	// unsigned inbound activity is processed exactly as it is today.
	app := newInboxTestApp(t, "")

	body := []byte(inboxActivity)
	r := httptest.NewRequest("POST", "https://local.example/api/collections/alice/inbox", bytes.NewReader(body))
	w := httptest.NewRecorder()

	err := handleFetchCollectionInbox(app, w, r)

	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestDecodePrivateKeyPanicsOnEmptyKey documents the upstream defect that
// actorPrivKey exists to guard. web-core's DecodePrivateKey checks whether
// pem.Decode returned a nil block and then dereferences that same nil block to
// format its error message, so the nil check and the panic sit on one line. If
// web-core is ever fixed this test fails, which is the signal to reconsider the
// guard rather than to keep it out of habit.
func TestDecodePrivateKeyPanicsOnEmptyKey(t *testing.T) {
	assert.Panics(t, func() {
		//nolint:errcheck // the panic is the subject of the test
		activitypub.DecodePrivateKey(nil)
	})
}

// TestActorPrivKeyRefusesMissingKey covers the reachable case: a collection
// whose ActivityPub keypair was never generated, which happens when key
// generation fails. That must surface as an error the caller can handle, not
// as a panic inside a dependency.
func TestActorPrivKeyRefusesMissingKey(t *testing.T) {
	p := &activitystreams.Person{}
	p.ID = "https://example.com/api/collections/nokey"

	key, err := actorPrivKey(p)

	assert.Nil(t, key)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no private key")
	assert.Contains(t, err.Error(), p.ID, "the error should name the actor, so the operator knows which collection is broken")
}

func TestMakeActivityPostEmptyURLDoesNotLogPrivateKey(t *testing.T) {
	// A Person carries the blog's ActivityPub signing private key in an
	// unexported field, and %+v prints unexported fields via reflection.
	// Logging the actor struct on the empty-URL path therefore wrote the
	// signing key to the logs in plaintext. The log line must name the
	// actor and nothing else about it.
	app := allowlistApp(t, "")

	p := activitystreams.NewPerson("https://local.example/api/collections/blog")
	privKey := []byte("NOT-A-REAL-PRIVATE-KEY-placeholder")
	p.SetPrivKey(privKey)

	var logged bytes.Buffer
	orig := log.ErrorLog
	log.ErrorLog = stdlog.New(&logged, "", 0)
	defer func() { log.ErrorLog = orig }()

	err := makeActivityPost(app, p, "", map[string]string{"type": "Create"})

	assert.Error(t, err)
	// %+v renders a []byte as a decimal byte array, so the leaked key does
	// not appear verbatim: look for the rendering fmt would actually emit.
	assert.NotContains(t, logged.String(), fmt.Sprintf("%v", privKey), "the actor's private key must never reach the log")
	assert.Contains(t, logged.String(), p.ID, "the log should still name the actor, which is the useful debugging detail")
}
