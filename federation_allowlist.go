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

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	fedsig "github.com/go-fed/httpsig"
	"github.com/writeas/impart"
	"github.com/writeas/web-core/activitypub"
	"github.com/writeas/web-core/activitystreams"
	"github.com/writeas/web-core/log"
)

// parseFederationAllowlist turns a comma-separated list of hostnames into a
// set. Entries are trimmed and lowercased, and empty entries are dropped, so
// a value of only whitespace or commas yields an empty set.
//
// An entry may be a wildcard of the form "*.example.org", which is kept
// verbatim: federationAllowed looks a wildcard up by the key it builds from
// the hostname it is checking, so no separate list is needed. Wildcards are
// only checked for shape here — validateFederationAllowlist rejects the
// unusable ones, and does so at startup rather than silently at match time.
func parseFederationAllowlist(s string) map[string]bool {
	allowed := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		host := strings.ToLower(strings.TrimSpace(part))
		if host != "" {
			allowed[host] = true
		}
	}
	return allowed
}

// wildcardPrefix marks an allowlist entry that matches subdomains rather
// than one exact host.
const wildcardPrefix = "*."

// validateFederationAllowlist rejects entries whose "*" cannot be honoured as
// written. Startup is the only place an operator finds out: a malformed
// wildcard that survived to match time would simply match nothing, and an
// allowlist that quietly matches nothing looks exactly like one that is
// working until the day someone expects it to admit a peer.
//
// A bare "*" is refused for the opposite reason. It would parse as an exact
// host named "*", which no hostname is, so it too matches nothing — but an
// operator who wrote it plainly meant "everything", and the honest way to say
// that is to leave the allowlist empty.
func validateFederationAllowlist(allowed map[string]bool) error {
	for entry := range allowed {
		suffix, isWildcard := strings.CutPrefix(entry, wildcardPrefix)
		if !isWildcard {
			if strings.Contains(entry, "*") {
				return fmt.Errorf("federation_allowlist entry %q: a wildcard is only meaningful as a leading %q", entry, wildcardPrefix)
			}
			continue
		}
		if suffix == "" {
			return fmt.Errorf("federation_allowlist entry %q has no domain after the wildcard", entry)
		}
		if strings.Contains(suffix, "*") {
			return fmt.Errorf("federation_allowlist entry %q: only one leading wildcard is supported", entry)
		}
	}
	return nil
}

// federationAllowlistInertWarning is logged when a federation allowlist is
// configured but federation itself is switched off. It names both options
// because either one, read alone, leaves an operator to guess the other.
const federationAllowlistInertWarning = "WARNING: federation_allowlist is configured but federation = false: the allowlist will have no effect until federation is enabled."

// initFederationAllowlist parses the configured allowlist into the App and
// prepares the key cache. It fails when an allowlist is configured on an
// instance that is not private, so an operator never ends up with an
// allowlist they believe is in force over content that is in fact public.
//
// An allowlist configured with federation disabled is not refused: it is
// merely inert, since nothing is ever sent or exposed, so this only logs a
// warning rather than failing startup. That differs from the private-mode
// case above, where booting anyway would be dangerous rather than inert.
func (app *App) initFederationAllowlist() error {
	app.fedAllowlist = parseFederationAllowlist(app.cfg.App.FederationAllowlist)
	if err := validateFederationAllowlist(app.fedAllowlist); err != nil {
		return err
	}
	if len(app.fedAllowlist) > 0 && !app.cfg.App.Private {
		return fmt.Errorf("federation_allowlist requires private = true: refusing to run an allowlist on a public instance")
	}
	if len(app.fedAllowlist) > 0 && !app.cfg.App.Federation {
		log.Info(federationAllowlistInertWarning)
	}
	if app.fedKeys == nil {
		app.fedKeys = newKeyCache()
	}
	// Installed only once the allowlist has been accepted, so a refused
	// configuration never leaves a half-applied exemption behind it, and
	// only when there is an allowlist to consult: an instance without one
	// keeps the strict ruleset and the nil-options fast path with it.
	if app.federationAllowlistActive() {
		setPeerAddressExemption(app.peerAddressExempt)
	} else {
		setPeerAddressExemption(nil)
	}
	return nil
}

// peerAddressExempt reports whether host may be reached on an address the
// SSRF guard would otherwise refuse. It is what ssrfGuardOptions.hostAllowed
// is wired to, and it exempts nothing beyond RFC1918/ULA and CGNAT — see
// isPublicAddr for the ranges no exemption can reopen.
//
// The exemption exists because this software is deployed on private
// networks. A community whose peers all sit on a tailnet has every peer on
// 100.64.0.0/10, so the strict ruleset refuses every webfinger lookup for a
// sibling instance and no peer handle in a post can resolve.
//
// It deliberately does NOT reuse federationAllowed's answer directly.
// federationAllowed treats an unconfigured allowlist as "federate with
// anyone" and returns true for every host; read straight, that would exempt
// every host on the internet from the address guard on an instance that has
// simply not been configured. An exemption has to be something the operator
// asked for, so an empty allowlist exempts nobody.
func (app *App) peerAddressExempt(host string) bool {
	if app == nil || !app.federationAllowlistActive() {
		return false
	}
	return app.federationAllowed(host)
}

const (
	// allowlistKeyTTL is how long a fetched remote public key is reused.
	allowlistKeyTTL = time.Hour
	// allowlistKeyErrorTTL is how long a failed key fetch is remembered, so
	// an unreachable peer cannot make every request wait on a timeout.
	allowlistKeyErrorTTL = time.Minute
	// allowlistClockSkew is how far a request's Date header may sit from the
	// current time before the request is treated as a replay.
	allowlistClockSkew = 5 * time.Minute
)

// cachedKey is one key cache entry. A nil key records a failed fetch.
type cachedKey struct {
	key     *rsa.PublicKey
	expires time.Time
}

// keyCache holds remote public keys used to verify incoming signatures. It is
// safe for concurrent use.
type keyCache struct {
	mu   sync.RWMutex
	keys map[string]cachedKey
}

// newKeyCache returns an empty key cache.
func newKeyCache() *keyCache {
	return &keyCache{keys: map[string]cachedKey{}}
}

// keyCacheLimit caps the number of entries a keyCache holds. allowlistedKey
// is reached before a caller has proved possession of any private key: the
// allowlist, date-window and signed-header checks that come before it in
// verifyAllowlistedSignature all pass without a valid signature. An
// unauthenticated caller who knows one allowlisted hostname can therefore
// present an unbounded number of distinct keyIds on that host, so the cache
// must not grow without bound in response.
const keyCacheLimit = 1024

// get returns the cached key for keyID. The second return value reports
// whether a live entry exists: an entry that exists with a nil key records an
// earlier failure and should not be retried yet. An entry found to be
// expired is removed, so expiry actually reclaims memory rather than merely
// being reported as a miss forever.
func (c *keyCache) get(keyID string) (*rsa.PublicKey, bool) {
	c.mu.RLock()
	e, ok := c.keys[keyID]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		c.mu.Lock()
		// Re-check under the write lock: another goroutine may have
		// already deleted or replaced this entry.
		if cur, ok := c.keys[keyID]; ok && cur.expires.Equal(e.expires) {
			delete(c.keys, keyID)
		}
		c.mu.Unlock()
		return nil, false
	}
	return e.key, true
}

// set stores a key, or a nil key to record a failure, for the given duration.
// If the cache is at keyCacheLimit, expired entries are evicted first to
// make room; if it is still full after that, the new entry is dropped rather
// than added, since keyCacheLimit exists specifically to bound the cost an
// unauthenticated caller can impose.
func (c *keyCache) set(keyID string, k *rsa.PublicKey, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.keys[keyID]; !exists && len(c.keys) >= keyCacheLimit {
		now := time.Now()
		for id, e := range c.keys {
			if now.After(e.expires) {
				delete(c.keys, id)
			}
		}
		if len(c.keys) >= keyCacheLimit {
			return
		}
	}
	c.keys[keyID] = cachedKey{key: k, expires: time.Now().Add(ttl)}
}

// federationAllowlistActive reports whether a federation allowlist is
// configured.
func (app *App) federationAllowlistActive() bool {
	return len(app.fedAllowlist) > 0
}

// federationOutboundEnabled reports whether this instance may send activities
// to remote servers. A private instance may, but only when a federation
// allowlist names the hosts it is allowed to reach.
func (app *App) federationOutboundEnabled() bool {
	return !app.cfg.App.Private || app.federationAllowlistActive()
}

// federationAllowed reports whether the given hostname may federate with this
// instance. With no allowlist configured every host is allowed, which
// preserves the behaviour of an instance that has not opted in.
//
// Matching is case-insensitive. A plain entry matches exactly and never
// admits its subdomains: example.org does not allow evil.example.org. A
// wildcard entry "*.example.org" matches subdomains at any depth —
// blog.example.org and bar.beta.example.org alike — but never the apex
// example.org itself, which is a separate instance with its own keys and its
// own operator. An operator who wants both lists both.
//
// A wildcard delegates trust to whoever controls the zone, so it belongs only
// on a zone this operator controls.
func (app *App) federationAllowed(host string) bool {
	if !app.federationAllowlistActive() {
		return true
	}
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	// Entries are stored verbatim, so a caller presenting the literal text of
	// a wildcard entry as its hostname would otherwise be admitted by the
	// exact-match lookup below. No real hostname contains a "*".
	if strings.Contains(host, "*") {
		return false
	}
	if app.fedAllowlist[host] {
		return true
	}
	// Walk off one label at a time and ask whether a wildcard was configured
	// for what remains. Starting one label in is what excludes the apex, and
	// cutting only at a "." is what keeps the match on label boundaries, so
	// evilbeta.example.org is not a subdomain of beta.example.org.
	for rest := host; ; {
		_, after, found := strings.Cut(rest, ".")
		if !found || after == "" {
			return false
		}
		if app.fedAllowlist[wildcardPrefix+after] {
			return true
		}
		rest = after
	}
}

// inboxAllowed reports whether an activity may be delivered to the given
// inbox URL. A URL that cannot be parsed is never allowed.
func (app *App) inboxAllowed(inboxURL string) bool {
	if !app.federationAllowlistActive() {
		return true
	}
	u, err := url.Parse(inboxURL)
	if err != nil {
		log.Error("Federation allowlist: can't parse inbox %q: %v", inboxURL, err)
		return false
	}
	return app.federationAllowed(u.Hostname())
}

// allowlistedKey returns the RSA public key named by keyID, fetching the
// owning actor if the key is not cached.
//
// The caller must already have checked keyID's host against the allowlist:
// this function makes an outbound request.
//
// The actor is fetched directly rather than through getActor, because a
// RemoteUser cached in the database carries no public key and AsPerson would
// return an actor with an empty one.
func (app *App) allowlistedKey(keyID string) (*rsa.PublicKey, error) {
	if k, found := app.fedKeys.get(keyID); found {
		if k == nil {
			return nil, fmt.Errorf("key %s was recently unreachable", keyID)
		}
		return k, nil
	}

	u, err := url.Parse(keyID)
	if err != nil {
		return nil, err
	}
	keyHost := u.Hostname()
	u.Fragment = ""
	actorIRI := u.String()

	resp, err := resolveIRI(app.cfg.App.Host, actorIRI)
	if err != nil {
		app.fedKeys.set(keyID, nil, allowlistKeyErrorTTL)
		return nil, err
	}
	actor := &activitystreams.Person{}
	if err := unmarshalActor(resp, actor); err != nil {
		app.fedKeys.set(keyID, nil, allowlistKeyErrorTTL)
		return nil, err
	}

	// The fetched actor must actually own the key it was asked about, and
	// must still be on the host the allowlist was checked against. Without
	// the host check a redirect could hand back a key from elsewhere.
	if actor.PublicKey.ID != keyID {
		app.fedKeys.set(keyID, nil, allowlistKeyErrorTTL)
		return nil, fmt.Errorf("actor %s does not own key %s", actorIRI, keyID)
	}
	if actor.PublicKey.Owner != actor.ID {
		app.fedKeys.set(keyID, nil, allowlistKeyErrorTTL)
		return nil, fmt.Errorf("key %s names owner %s, but the actor is %s", keyID, actor.PublicKey.Owner, actor.ID)
	}
	actorURL, err := url.Parse(actor.ID)
	if err != nil {
		app.fedKeys.set(keyID, nil, allowlistKeyErrorTTL)
		return nil, err
	}
	if !strings.EqualFold(actorURL.Hostname(), keyHost) {
		app.fedKeys.set(keyID, nil, allowlistKeyErrorTTL)
		return nil, fmt.Errorf("actor %s is not on key host %s", actor.ID, keyHost)
	}

	pub, err := activitypub.DecodePublicKey([]byte(actor.PublicKey.PublicKeyPEM))
	if err != nil {
		app.fedKeys.set(keyID, nil, allowlistKeyErrorTTL)
		return nil, err
	}
	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		app.fedKeys.set(keyID, nil, allowlistKeyErrorTTL)
		return nil, fmt.Errorf("key %s is not an RSA key", keyID)
	}

	app.fedKeys.set(keyID, rsaKey, allowlistKeyTTL)
	return rsaKey, nil
}

// ErrFederationNotAllowed is the single error every allowlist rejection
// produces. The real cause is logged but never returned, because a response
// that varies by cause lets a caller probe the allowlist.
var ErrFederationNotAllowed = impart.HTTPError{Status: http.StatusUnauthorized, Message: "Unauthorized."}

// checkRequestDate rejects a request whose Date header sits too far from now.
// A signature stays valid forever on its own, so the date window is what stops
// a captured request being replayed later.
func checkRequestDate(r *http.Request, now time.Time) error {
	d := r.Header.Get("Date")
	if d == "" {
		return fmt.Errorf("request has no Date header")
	}
	t, err := http.ParseTime(d)
	if err != nil {
		return fmt.Errorf("unparseable Date header %q: %v", d, err)
	}
	diff := now.Sub(t)
	if diff > allowlistClockSkew || diff < -allowlistClockSkew {
		return fmt.Errorf("Date header %q is outside the accepted window", d)
	}
	return nil
}

// requestHasBody reports whether r carries a message body. A signature that
// omits the Digest header from its covered set leaves that body wholly
// unbound to the signature, so this decides both whether Digest must be
// signed and whether it is checked. Both call sites must use this same
// predicate: they must never be able to disagree about whether a body is
// present.
//
// ContentLength == 0 is the only value that means "definitely no body".
// net/http reports ContentLength == -1 for a request of unknown length,
// which is what a chunked request looks like, and that unknown case must be
// treated as carrying a body. Otherwise an attacker could dodge the digest
// requirement entirely by sending a chunked request instead of a request
// with a Content-Length header: do not simplify this back to "> 0".
func requestHasBody(r *http.Request) bool {
	return r.ContentLength != 0 || len(r.TransferEncoding) > 0
}

// parseSignatureParams splits a raw Signature header value into its
// key=value parameters the same way go-fed's own parser does: split on ",",
// then split each parameter on its first "=", trimming surrounding double
// quotes from the value. Keys are compared case-SENSITIVELY, on purpose:
// go-fed's getSignatureComponents matches keys with an exact-case switch
// against the literal strings "keyId", "algorithm", "headers" and
// "signature", and silently ignores anything spelled differently. If this
// parser lowercased keys before comparing them, a fabricated "Headers="
// would be read as the authoritative headers parameter here while go-fed
// ignored it and fell back to whatever the genuine, differently-cased (or
// entirely absent) parameter actually said. Do not add a ToLower here: it
// would make a wrongly-cased key authoritative on this side and invisible
// on go-fed's, exactly the gap this parser exists to close.
//
// This exists so the signed header set is never decided by a parser that
// could read the same bytes differently than the verifying library does. A
// naive marker search (an earlier version of this function used
// strings.Index to find "headers=") finds the FIRST occurrence of a
// parameter, while go-fed's parser assigns on every match it sees and so
// ends up holding the LAST one. An attacker who inserts a fabricated,
// broader headers= parameter ahead of the real one can satisfy a
// first-occurrence check while go-fed verifies against the real, narrower
// set the signature was actually computed over, leaving the body (or the
// request-target, or the host) unbound to what was verified. Rather than
// switch to last-occurrence-wins and rest correctness on that reasoning
// staying right forever, any Signature header with a duplicated parameter
// key is refused outright: with duplicates refused, which occurrence would
// "win" cannot matter.
func parseSignatureParams(sigHeader string) (map[string]string, error) {
	params := map[string]string{}
	for _, p := range strings.Split(sigHeader, ",") {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("malformed Signature header parameter")
		}
		k := kv[0]
		v := strings.Trim(kv[1], `"`)
		if _, dup := params[k]; dup {
			return nil, fmt.Errorf("Signature header has a duplicated %q parameter", k)
		}
		params[k] = v
	}
	return params, nil
}

// signedHeaderSet parses the headers parameter out of a raw Signature
// header value and returns its elements, lowercased, as a set. go-fed
// treats a request with no headers parameter as covering only "date", so
// the absence of the parameter is reported as an error rather than an empty
// set: callers must not treat it as acceptable.
func signedHeaderSet(sigHeader string) (map[string]bool, error) {
	params, err := parseSignatureParams(sigHeader)
	if err != nil {
		return nil, err
	}
	headers, ok := params["headers"]
	if !ok {
		return nil, fmt.Errorf("Signature header has no headers parameter")
	}
	set := map[string]bool{}
	for _, h := range strings.Fields(headers) {
		set[strings.ToLower(h)] = true
	}
	return set, nil
}

// checkSignedHeaders rejects a signature that does not cover the headers
// this gate relies on. Verifying a signature only proves the remote signed
// whatever it chose to sign: without this check, a signature that leaves out
// (request-target), host or digest would let a captured signature be
// replayed against a different method, path, destination or body.
func checkSignedHeaders(r *http.Request, hasBody bool) error {
	set, err := signedHeaderSet(r.Header.Get("Signature"))
	if err != nil {
		return err
	}
	required := []string{"(request-target)", "host", "date"}
	if hasBody {
		required = append(required, "digest")
	}
	for _, h := range required {
		if !set[h] {
			return fmt.Errorf("signature does not cover required header %q", h)
		}
	}
	return nil
}

// verifyDigest recomputes the request body's SHA-256 and compares it to the
// Digest header. Signature verification covers the header's value, not the
// body that value claims to describe, so this is what ties the two together.
// The body is restored for the next reader.
func verifyDigest(r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("can't read body to check Digest: %v", err)
	}

	algo, want, found := strings.Cut(r.Header.Get("Digest"), "=")
	if !found || !strings.EqualFold(algo, "SHA-256") {
		return fmt.Errorf("missing or unsupported Digest header")
	}
	sum := sha256.Sum256(body)
	got := base64.StdEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return fmt.Errorf("Digest header does not match the body")
	}
	return nil
}

// verifyAllowlistedSignature admits a request carrying a valid HTTP signature
// from a host on the federation allowlist. Every failure returns
// ErrFederationNotAllowed.
//
// The order of these checks is a security property, not an implementation
// detail. The signing key's host is checked against the allowlist before the
// key is fetched, so a request from a host that is not on the list never
// causes an outbound connection to a URL the caller chose.
func (app *App) verifyAllowlistedSignature(r *http.Request) error {
	if !app.federationAllowlistActive() || r.Header.Get("Signature") == "" {
		return ErrFederationNotAllowed
	}

	v, err := fedsig.NewVerifier(r)
	if err != nil {
		log.Info("Federation allowlist: malformed signature: %v", err)
		return ErrFederationNotAllowed
	}

	keyID := v.KeyId()
	u, err := url.Parse(keyID)
	if err != nil || u.Hostname() == "" {
		log.Info("Federation allowlist: unusable keyId %q", keyID)
		return ErrFederationNotAllowed
	}
	if !app.federationAllowed(u.Hostname()) {
		log.Info("Federation allowlist: host %s is not on the allowlist", u.Hostname())
		return ErrFederationNotAllowed
	}

	if err := checkRequestDate(r, time.Now()); err != nil {
		log.Info("Federation allowlist: %v", err)
		return ErrFederationNotAllowed
	}

	hasBody := requestHasBody(r)
	if err := checkSignedHeaders(r, hasBody); err != nil {
		log.Info("Federation allowlist: %v", err)
		return ErrFederationNotAllowed
	}

	pubKey, err := app.allowlistedKey(keyID)
	if err != nil {
		log.Info("Federation allowlist: can't get key %s: %v", keyID, err)
		return ErrFederationNotAllowed
	}
	if err := v.Verify(pubKey, fedsig.RSA_SHA256); err != nil {
		log.Info("Federation allowlist: bad signature from %s: %v", keyID, err)
		return ErrFederationNotAllowed
	}

	if hasBody {
		if err := verifyDigest(r); err != nil {
			log.Info("Federation allowlist: %v", err)
			return ErrFederationNotAllowed
		}
	}

	return nil
}

// canDisablePrivateMode reports whether private mode may be turned off. It
// may not while a federation allowlist is configured, because the two are
// only meaningful together: an allowlist over public content protects
// nothing.
func (app *App) canDisablePrivateMode() bool {
	return !app.federationAllowlistActive()
}
