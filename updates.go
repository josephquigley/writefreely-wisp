/*
 * Copyright © 2019-2020 Musing Studio LLC.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/writeas/web-core/log"
)

// updatesCacheTime is the default interval between cache updates for new
// software versions
const defaultUpdatesCacheTime = 12 * time.Hour

// updatesCache holds data about current and new releases of the writefreely
// software
type updatesCache struct {
	mu             sync.Mutex
	frequency      time.Duration
	lastCheck      time.Time
	latestVersion  string
	currentVersion string
	checkError     error
}

// CheckNow asks for the latest released version of writefreely and updates
// the cache last checked time. If the version postdates the current 'latest'
// the version value is replaced.
func (uc *updatesCache) CheckNow() error {
	if debugging {
		log.Info("[update check] Checking for update now.")
	}
	uc.mu.Lock()
	defer uc.mu.Unlock()
	uc.lastCheck = time.Now()
	latestRemote, err := newVersionCheck()
	if err != nil {
		log.Error("[update check] Failed: %v", err)
		uc.checkError = err
		return err
	}
	if CompareSemver(latestRemote, uc.latestVersion) == 1 {
		uc.latestVersion = latestRemote
	}
	return nil
}

// AreAvailable updates the cache if the frequency duration has passed
// then returns if the latest release is newer than the current running version.
func (uc *updatesCache) AreAvailable() bool {
	uc.mu.Lock()
	stale := time.Since(uc.lastCheck) > uc.frequency
	uc.mu.Unlock()

	if stale {
		uc.CheckNow()
	}
	return uc.AreAvailableNoCheck()
}

// AreAvailableNoCheck returns if the latest release is newer than the current
// running version.
func (uc *updatesCache) AreAvailableNoCheck() bool {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	return CompareSemver(uc.latestVersion, uc.currentVersion) == 1
}

// LatestVersion returns the latest stored version available.
func (uc *updatesCache) LatestVersion() string {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	return uc.latestVersion
}

// LastChecked returns the time of the last completed check.
func (uc *updatesCache) LastChecked() time.Time {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	return uc.lastCheck
}

// CheckFailed returns whether the last check ended in an error.
func (uc *updatesCache) CheckFailed() bool {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	return uc.checkError != nil
}

// ReleaseURL returns the full URL to the release page for the latest version
// as stored in the cache.
func (uc *updatesCache) ReleaseURL() string {
	return "https://writefreely.org/releases/" + uc.LatestVersion()
}

// ReleaseNotesURL returns the full URL to the blog.writefreely.org release notes
// for the latest version as stored in the cache.
func (uc *updatesCache) ReleaseNotesURL() string {
	return wfReleaseNotesURL(uc.LatestVersion())
}

func wfReleaseNotesURL(v string) string {
	ver := strings.TrimPrefix(v, "v")
	ver = strings.TrimSuffix(ver, ".0")
	// hack until go 1.12 in build/travis
	seg := strings.Split(ver, ".")
	return "https://blog.writefreely.org/version-" + strings.Join(seg, "-")
}

// newUpdatesCache returns an initialized updates cache
func newUpdatesCache(expiry time.Duration) *updatesCache {
	cache := &updatesCache{
		frequency:      expiry,
		currentVersion: "v" + softwareVer,
	}
	go cache.CheckNow()
	return cache
}

// updateChecksSupported reports whether this build can check for updates in a
// way that tells the admin something true. It is false in Wisp Edition.
//
// The check asks version.writefreely.org for the latest upstream WriteFreely
// release and compares it against softwareVer, which is the upstream version
// this fork is based on. Wisp Edition carries changes upstream does not have
// and cuts its own releases, so an upstream version number says nothing about
// whether this install is current. Left enabled, the admin page would report
// "up to date" on a stale Wisp Edition, or offer an upstream download that
// would drop the features this fork exists to provide.
//
// Re-enable this once the check points at this fork's own releases and
// compares against a fork version separate from softwareVer.
const updateChecksSupported = false

// InitUpdates initializes the updates cache, if the config value is set
// It uses the defaultUpdatesCacheTime for the cache expiry
func (app *App) InitUpdates() {
	if !updateChecksSupported {
		// See updateChecksSupported. Forcing the config value off keeps the
		// admin nav link and the Updates page consistent with the fact that
		// nothing is being checked, whatever the config file asks for.
		app.cfg.App.UpdateChecks = false
		return
	}
	if app.Config().App.UpdateChecks {
		app.updates = newUpdatesCache(defaultUpdatesCacheTime)
	}
}

func newVersionCheck() (string, error) {
	res, err := http.Get("https://version.writefreely.org")
	if debugging {
		log.Info("[update check] GET https://version.writefreely.org")
	}
	// TODO: return error if statusCode != OK
	if err == nil && res.StatusCode == http.StatusOK {
		defer res.Body.Close()

		body, err := io.ReadAll(res.Body)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	return "", err
}
