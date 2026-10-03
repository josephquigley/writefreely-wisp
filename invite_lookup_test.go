//go:build sqlite

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
	stdlog "log"
	"net/http"
	"testing"

	"github.com/writeas/impart"
	"github.com/writeas/web-core/log"
)

// captureErrorLog redirects web-core's error logger into a buffer for the
// duration of the test.
func captureErrorLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.ErrorLog
	log.ErrorLog = stdlog.New(&buf, "ERROR: ", 0)
	t.Cleanup(func() { log.ErrorLog = prev })
	return &buf
}

// A successful invite lookup must not log anything. It used to consult
// isIgnorableError with a nil error, which on any non-MySQL driver logs
// "unrecognized driver" on every lookup.
func TestGetUserInviteSuccessLogsNothing(t *testing.T) {
	app := newSignupTestApp(t)
	seedInvite(t, app, "invitelookup1", 1)

	buf := captureErrorLog(t)
	inv, err := app.db.GetUserInvite("invitelookup1")
	if err != nil {
		t.Fatalf("GetUserInvite: %v", err)
	}
	if inv == nil || inv.ID != "invitelookup1" {
		t.Fatalf("GetUserInvite returned %+v, want invite invitelookup1", inv)
	}
	if buf.Len() != 0 {
		t.Errorf("successful lookup logged an error: %q", buf.String())
	}
}

// A missing invite is still reported as 404.
func TestGetUserInviteMissingIsNotFound(t *testing.T) {
	app := newSignupTestApp(t)

	_, err := app.db.GetUserInvite("no-such-invite")
	herr, ok := err.(impart.HTTPError)
	if !ok || herr.Status != http.StatusNotFound {
		t.Fatalf("GetUserInvite(missing) = %v, want 404 HTTPError", err)
	}
}
