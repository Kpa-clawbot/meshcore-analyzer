package main

import (
	"sync"
	"testing"
	"time"
)

// TestGetStoreStats_CacheHit verifies that a second call within 30s returns
// the cached observation counts without re-querying the database.
func TestGetStoreStats_CacheHit(t *testing.T) {
	srv, _ := setupTestServer(t)
	store := srv.store

	store.statsCacheMu.Lock()
	store.statsCacheTime = time.Now()
	store.statsLastHour = 42
	store.statsLast24h = 777
	store.statsCacheMu.Unlock()

	st, err := store.GetStoreStats()
	if err != nil {
		t.Fatalf("GetStoreStats: %v", err)
	}
	if st.PacketsLastHour != 42 {
		t.Errorf("cache hit: PacketsLastHour want 42 got %d", st.PacketsLastHour)
	}
	if st.PacketsLast24h != 777 {
		t.Errorf("cache hit: PacketsLast24h want 777 got %d", st.PacketsLast24h)
	}
}

// TestGetStoreStats_CacheExpiry verifies that a cache older than 30s is
// discarded and the database query re-runs to refresh the values.
func TestGetStoreStats_CacheExpiry(t *testing.T) {
	srv, _ := setupTestServer(t)
	store := srv.store

	store.statsCacheMu.Lock()
	store.statsCacheTime = time.Now().Add(-35 * time.Second)
	store.statsLastHour = 9999
	store.statsLast24h = 9999
	store.statsCacheMu.Unlock()

	if _, err := store.GetStoreStats(); err != nil {
		t.Fatalf("GetStoreStats: %v", err)
	}

	// #1910 CHANGED THE CONTRACT HERE. This used to assert that an expired cache
	// returns fresh DB values on the same call. It now returns the stale value and
	// refreshes in the background, because making the caller wait for a range scan
	// over 24h of observations is what produced 10-17s /stats responses under
	// mixed load. What still has to hold is that the refresh actually happens, so
	// that is what this asserts. The "served immediately while stale" half is
	// pinned by TestGetStoreStats_StaleCacheServedWithoutBlocking below.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		store.statsCacheMu.Lock()
		age := time.Since(store.statsCacheTime)
		refreshed := store.statsLastHour != 9999 && store.statsLast24h != 9999
		store.statsCacheMu.Unlock()
		if refreshed && age < 5*time.Second {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("cache not refreshed after expiry: the sentinel is still in place")
}

// TestGetStoreStats_CacheConcurrentReaders verifies that 100 concurrent
// callers produce no data race on the stats cache fields.
// Run with: go test -race ./... -run TestGetStoreStats_CacheConcurrentReaders
func TestGetStoreStats_CacheConcurrentReaders(t *testing.T) {
	srv, _ := setupTestServer(t)
	store := srv.store

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.GetStoreStats(); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent GetStoreStats: %v", err)
	}
}

// #1910: a stale observation-count cache must be served immediately, with the
// refresh happening in the background, instead of making the caller wait for a
// range scan over 24h of observations.
//
// Before this, an expired cache meant every concurrent request ran that scan
// itself, because the cache check releases statsCacheMu before doing the work.
// On a deployment with 18k observers /stats was measured at 10-17s under the
// mixed load an Observers page load produces, while staying under 70ms when it
// was the only endpoint being hit.
//
// The test seeds the cache with values the database cannot produce, then makes
// it stale. If the stale value comes back, the caller was not made to wait.
func TestGetStoreStats_StaleCacheServedWithoutBlocking(t *testing.T) {
	srv, _ := setupTestServer(t)
	store := srv.store

	const sentinelHour, sentinelDay = 424242, 434343

	store.statsCacheMu.Lock()
	store.statsLastHour = sentinelHour
	store.statsLast24h = sentinelDay
	store.statsCacheTime = time.Now().Add(-90 * time.Second) // well past the 30s TTL
	store.statsCacheMu.Unlock()

	st, err := store.GetStoreStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.PacketsLastHour != sentinelHour || st.PacketsLast24h != sentinelDay {
		t.Errorf("stale cache not served: got (%d, %d), want (%d, %d). "+
			"An expired cache must answer from the previous value and refresh in the "+
			"background, not block the request on the observations scan",
			st.PacketsLastHour, st.PacketsLast24h, sentinelHour, sentinelDay)
	}

	// The background refresh should then replace the sentinel with a real count.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		store.statsCacheMu.Lock()
		refreshed := store.statsLastHour != sentinelHour || store.statsLast24h != sentinelDay
		store.statsCacheMu.Unlock()
		if refreshed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("background refresh never replaced the stale value; serving stale is only " +
		"acceptable because a refresh follows it")
}

// #1910: concurrent cold-cache callers must agree, which they cannot do if each
// runs its own scan against a moving table.
func TestGetStoreStats_ConcurrentColdCallersAgree(t *testing.T) {
	srv, _ := setupTestServer(t)
	store := srv.store

	store.statsCacheMu.Lock()
	store.statsCacheTime = time.Time{} // cold
	store.statsCacheMu.Unlock()

	const n = 50
	var wg sync.WaitGroup
	results := make(chan [2]int, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := store.GetStoreStats()
			if err != nil {
				t.Error(err)
				return
			}
			results <- [2]int{st.PacketsLastHour, st.PacketsLast24h}
		}()
	}
	wg.Wait()
	close(results)

	var first [2]int
	seen := false
	for r := range results {
		if !seen {
			first, seen = r, true
			continue
		}
		if r != first {
			t.Errorf("concurrent callers disagreed: %v vs %v", r, first)
		}
	}
}
