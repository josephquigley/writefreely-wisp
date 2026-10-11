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
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/writeas/impart"
	"github.com/writeas/web-core/log"
)

// activityPubOnlyRoutes are the route templates that answer in ActivityPub
// whatever the request's Accept header says. Every other route serves
// ActivityPub only when IsActivityPubRequest, which is the same test its
// handler applies, so the gate and the handler cannot disagree about what is
// an ActivityPub request.
var activityPubOnlyRoutes = map[string]bool{
	"/api/collections/{alias}/inbox":     true,
	"/api/collections/{alias}/outbox":    true,
	"/api/collections/{alias}/followers": true,
	"/api/collections/{alias}/following": true,
}

// authorizedFetchOpen reports whether r is for something a peer must be able
// to read before it can verify this instance's own signatures, and which
// authorized_fetch therefore leaves open.
//
// That is discovery (webfinger, host-meta and nodeinfo) and the instance
// actor. resolveIRI signs every outbound fetch with the instance actor's
// key, so a peer that itself enforces signatures fetches that actor to check
// ours; gating it would leave two such servers each waiting on the other.
// Mastodon and Mbin leave their instance actor open for the same reason.
func (app *App) authorizedFetchOpen(r *http.Request) bool {
	p := r.URL.Path
	if strings.HasPrefix(p, "/.well-known/") || p == nodeInfoPath {
		return true
	}
	return r.Method == http.MethodGet && p == "/api/collections/"+instanceActorAlias(app.Config())
}

// isActivityPubRoute reports whether r reaches a route that serves
// ActivityPub, or asks for ActivityPub from a route that can serve either.
func isActivityPubRoute(r *http.Request) bool {
	if route := mux.CurrentRoute(r); route != nil {
		if tpl, err := route.GetPathTemplate(); err == nil && activityPubOnlyRoutes[tpl] {
			return true
		}
	}
	return IsActivityPubRequest(r)
}

// requireAuthorizedFetch enforces the authorized_fetch setting on r. It
// returns nil when the setting is off, when r is not an ActivityPub request,
// and when r is for something authorizedFetchOpen keeps open; otherwise it
// returns verifyPeerSignature's verdict.
//
// It never admits anything: a request it passes still faces private mode
// and every handler's own checks. An API token or a web session does not
// stand in for a signature here, but only ActivityPub requests are gated, so
// neither is affected anywhere else.
func (app *App) requireAuthorizedFetch(r *http.Request) error {
	if !app.Config().App.AuthorizedFetch {
		return nil
	}
	if !isActivityPubRoute(r) || app.authorizedFetchOpen(r) {
		return nil
	}
	return app.verifyPeerSignature(r)
}

// authorizedFetch is the router middleware that applies
// requireAuthorizedFetch. It runs once a route has matched and before its
// handler, so a refusal comes before any lookup: an unsigned request for a
// blog that does not exist gets the same 401 as one for a blog that does,
// and so learns nothing about which blogs exist.
func (h *Handler) authorizedFetch(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app := h.app.App()
		start := time.Now()
		if err := app.requireAuthorizedFetch(r); err != nil {
			impart.WriteError(w, ErrFederationNotAllowed)
			log.Info("%s", h.app.ReqLog(r, ErrFederationNotAllowed.Status, time.Since(start)))
			return
		}
		next.ServeHTTP(w, r)
	})
}
