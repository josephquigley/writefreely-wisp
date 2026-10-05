package writefreely

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"
)

// serveLatestVersion points versionCheckURL at a local server answering with
// version, for the length of the test, so the update check never leaves the
// machine.
func serveLatestVersion(t *testing.T, version string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, version)
	}))
	t.Cleanup(srv.Close)
	prev := versionCheckURL
	versionCheckURL = srv.URL
	t.Cleanup(func() { versionCheckURL = prev })
}

func TestUpdatesRoundTrip(t *testing.T) {
	// Set before the cache is built: newUpdatesCache starts a check in a
	// goroutine straight away.
	serveLatestVersion(t, "v99.0.0")
	cache := newUpdatesCache(defaultUpdatesCacheTime)
	t.Run("New Updates Cache", func(t *testing.T) {

		if cache == nil {
			t.Fatal("Returned nil cache")
		}

		if cache.frequency != defaultUpdatesCacheTime {
			t.Fatalf("Got cache expiry frequency: %s but expected: %s", cache.frequency, defaultUpdatesCacheTime)
		}

		if cache.currentVersion != "v"+softwareVer {
			t.Fatalf("Got current version: %s but expected: %s", cache.currentVersion, "v"+softwareVer)
		}
	})

	t.Run("Release URL", func(t *testing.T) {
		// newUpdatesCache looks up the latest version in a goroutine, so the
		// cache's latest version is still empty by the time this subtest
		// runs. Build the URL from a known version instead: what is under
		// test here is the URL format, not the version lookup.
		url := wfReleaseNotesURL("v0.15.0")

		reg, err := regexp.Compile(`^https:\/\/blog.writefreely.org\/version(-\d+){1,}$`)
		if err != nil {
			t.Fatalf("Test Case Error: Failed to compile regex: %v", err)
		}
		match := reg.MatchString(url)

		if !match {
			t.Fatalf("Malformed Release URL: %s", url)
		}
	})

	t.Run("Check Now", func(t *testing.T) {
		// ensure time between init and next check
		time.Sleep(1 * time.Second)

		prevLastCheck := cache.LastChecked()

		// force to known older version for latest and current. The check
		// the constructor started runs in a goroutine and writes these
		// same fields, so take the lock the accessors take.
		prevLatestVer := "v0.8.1"
		cache.mu.Lock()
		cache.latestVersion = prevLatestVer
		cache.currentVersion = "v0.8.0"
		cache.mu.Unlock()

		err := cache.CheckNow()
		if err != nil {
			t.Fatalf("Error should be nil, got: %v", err)
		}

		if prevLastCheck == cache.LastChecked() {
			t.Fatal("Expected lastCheck to update")
		}

		if cache.LastChecked().Before(prevLastCheck) {
			t.Fatal("Last check should be newer than previous")
		}

		if prevLatestVer == cache.LatestVersion() {
			t.Fatal("expected latestVersion to update")
		}

	})

	t.Run("Are Available", func(t *testing.T) {
		if !cache.AreAvailable() {
			t.Fatalf("Cache reports no updates but Latest is %s", cache.LatestVersion())
		}
	})

	t.Run("Latest Version", func(t *testing.T) {
		if cache.LatestVersion() == "" {
			t.Fatal("Latest version is empty after a completed check")
		}
	})
}

// TestUpdatesCacheConcurrentAccess covers the cache being read while the
// background check writes to it, which is what newUpdatesCache sets up on
// every start: it launches CheckNow in a goroutine and hands the cache
// straight to callers. Run with -race, this fails when the readers take a
// copy of the cache instead of locking it.
func TestUpdatesCacheConcurrentAccess(t *testing.T) {
	cache := &updatesCache{
		frequency:      time.Hour,
		currentVersion: "v0.1.0",
		lastCheck:      time.Now(),
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.AreAvailableNoCheck()
			cache.LatestVersion()
			cache.LastChecked()
			cache.CheckFailed()
			cache.ReleaseNotesURL()
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 8; i++ {
			cache.mu.Lock()
			cache.latestVersion = "v0.2.0"
			cache.lastCheck = time.Now()
			cache.mu.Unlock()
		}
	}()

	wg.Wait()
}
