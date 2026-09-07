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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/writeas/activityserve"
	"github.com/writeas/go-webfinger"
	"github.com/writeas/impart"
	"github.com/writeas/web-core/log"
	"github.com/writefreely/writefreely/config"
)

type wfResolver struct {
	db  *datastore
	cfg *config.Config
}

var wfUserNotFoundErr = impart.HTTPError{http.StatusNotFound, "User not found."}

func (wfr wfResolver) FindUser(username string, host, requestHost string, r []webfinger.Rel) (*webfinger.Resource, error) {
	var c *Collection
	var err error
	if username == host {
		c = instanceColl
	} else if wfr.cfg.App.SingleUser {
		c, err = wfr.db.GetCollectionByID(1)
	} else {
		c, err = wfr.db.GetCollection(username)
	}
	if err != nil {
		log.Error("Unable to get blog: %v", err)
		return nil, err
	}
	c.hostName = wfr.cfg.App.Host

	if !c.IsInstanceColl() {
		silenced, err := wfr.db.IsUserSilenced(c.OwnerID)
		if err != nil {
			log.Error("webfinger find user: check is silenced: %v", err)
			return nil, err
		}
		if silenced {
			return nil, wfUserNotFoundErr
		}
	}
	if wfr.cfg.App.SingleUser {
		// Ensure handle matches user-chosen one on single-user blogs
		if username != c.Alias {
			log.Info("Username '%s' is not handle '%s'", username, c.Alias)
			return nil, wfUserNotFoundErr
		}
	}
	// Only return information if site has federation enabled.
	// TODO: enable two levels of federation? Unlisted or Public on timelines?
	if !wfr.cfg.App.Federation {
		return nil, wfUserNotFoundErr
	}

	res := webfinger.Resource{
		Subject: "acct:" + username + "@" + host,
		Aliases: []string{
			c.CanonicalURL(),
			c.FederatedAccount(),
		},
		Links: []webfinger.Link{
			{
				HRef: c.CanonicalURL(),
				Type: "text/html",
				Rel:  "https://webfinger.net/rel/profile-page",
			},
			{
				HRef: c.FederatedAccount(),
				Type: "application/activity+json",
				Rel:  "self",
			},
		},
	}
	return &res, nil
}

func (wfr wfResolver) DummyUser(username string, hostname string, r []webfinger.Rel) (*webfinger.Resource, error) {
	return nil, wfUserNotFoundErr
}

func (wfr wfResolver) IsNotFoundError(err error) bool {
	return err == wfUserNotFoundErr
}

// errBlockedRemoteAddr is returned when a webfinger lookup would connect to
// a private, loopback, link-local, or otherwise disallowed address.
var errBlockedRemoteAddr = errors.New("refusing to connect to a private or internal address")

// newSafeWebfingerHTTPClient returns a hardened HTTP client for fetching
// remote webfinger documents. It validates the resolved IP address of every
// connection (including redirects) at dial time, so DNS rebinding can't be
// used to bypass the check.
//
// The App is what supplies the federation allowlist, which is the only thing
// that can distinguish a peer this operator federates with from an internal
// address someone named in a handle. A nil App gets the strict behaviour.
// The decision is per-connection, so each redirect hop is judged on the host
// it actually dials rather than on the host the lookup started with.
func newSafeWebfingerHTTPClient(app *App) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: app.safeDialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing to follow redirect to non-https scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
}

func (app *App) safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ip := range ips {
		if !app.dialAllowed(host, ip.IP) {
			lastErr = fmt.Errorf("%w: %s", errBlockedRemoteAddr, ip.IP)
			continue
		}
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err != nil {
			lastErr = err
			continue
		}
		return conn, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no addresses found for %s", host)
	}
	return nil, lastErr
}

// dialAllowed reports whether host may be dialled at ip.
//
// A public address is always allowed. An address that isPublicAddr refuses —
// RFC 1918, CGNAT — is allowed only when the operator has named this host in
// federation_allowlist, which they may only do on a private instance and only
// by writing the hostname out by hand. That is the sole signal available here
// that separates a peer from an SSRF target: the guard sees an IP, and every
// tailnet peer looks exactly like an internal service at that level.
//
// The addresses in neverDialableAddr stay refused whatever the allowlist
// says. A peer may live on a private network; it has no business being
// loopback or the link-local cloud metadata endpoint.
func (app *App) dialAllowed(host string, ip net.IP) bool {
	if isPublicAddr(ip) {
		return true
	}
	if neverDialableAddr(ip) {
		return false
	}
	// federationAllowed answers true for every host when no allowlist is
	// configured, which is right for federation and wrong here: no allowlist
	// means no operator declaration, so nothing unlocks a private address.
	if app == nil || !app.federationAllowlistActive() {
		return false
	}
	return app.federationAllowed(host)
}

// neverDialableAddr reports whether ip is refused no matter which host asked
// for it: loopback, link-local (including 169.254.169.254), multicast, and
// the unspecified address.
func neverDialableAddr(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

// isPublicAddr reports whether ip is safe to connect to for any host, i.e.
// not a loopback, private, link-local, multicast, or otherwise
// special-purpose address that could be used to reach internal services or
// cloud metadata endpoints via SSRF.
func isPublicAddr(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 100.64.0.0/10 (Carrier-Grade NAT) and 169.254.169.254-style
		// cloud metadata addresses are covered by IsLinkLocalUnicast above,
		// but explicitly block the CGNAT range too.
		if ip4[0] == 100 && ip4[1]&0xc0 == 64 {
			return false
		}
	}
	return true
}

// remoteLookup and newRemoteActor are the two network calls that handle
// resolution makes. They are indirected through variables so that a test can
// drive the failure of either one: both failures used to be cached in
// remoteusers as a row with an empty actor_id, which disabled that handle for
// good, and a regression there is invisible without being able to fail them
// on demand.
var (
	remoteLookup   = RemoteLookup
	newRemoteActor = activityserve.NewRemoteActor
)

// RemoteLookup looks up a user by handle at a remote server
// and returns the actor URL. The App carries the federation allowlist, which
// decides whether a peer on a private network may be dialled at all; passing
// nil is safe and gets the strict guard.
func RemoteLookup(app *App, handle string) string {
	handle = strings.TrimLeft(handle, "@")
	// let's take the server part of the handle
	parts := strings.Split(handle, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		log.Error("Invalid webfinger handle: %s", handle)
		return ""
	}
	domain := parts[1]

	resp, err := newSafeWebfingerHTTPClient(app).Get("https://" + domain + "/.well-known/webfinger?resource=acct:" + handle)
	if err != nil {
		log.Error("Error on webfinger request: %v", err)
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("Error on webfinger response: %v", err)
		return ""
	}

	var result webfinger.Resource
	err = json.Unmarshal(body, &result)
	if err != nil {
		log.Error("Unable to parse webfinger response: %v", err)
		return ""
	}

	var href string
	// iterate over webfinger links and find the one with
	// a self "rel"
	for _, link := range result.Links {
		if link.Rel == "self" {
			href = link.HRef
		}
	}

	// if we didn't find it with the above then
	// try using aliases
	if href == "" {
		// take the last alias because mastodon has the
		// https://instance.tld/@user first which
		// doesn't work as an href
		href = result.Aliases[len(result.Aliases)-1]
	}

	return href
}
