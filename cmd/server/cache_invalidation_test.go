package main

import (
	"testing"
	"time"
)

// newTestStore creates a minimal PacketStore for cache invalidation testing.
func newTestStore(t *testing.T) *PacketStore {
	t.Helper()
	return &PacketStore{
		rfCache:        make(map[string]*cachedResult),
		topoCache:      make(map[string]*cachedResult),
		hashCache:      make(map[string]*cachedResult),
		collisionCache: make(map[string]*cachedResult),
		chanCache:      make(map[string]*cachedResult),
		distCache:      make(map[string]*cachedResult),
		subpathCache:   make(map[string]*cachedResult),
		rfCacheTTL:     15 * time.Second,
		invCooldown:    10 * time.Second,
	}
}

// populateAllCaches fills every analytics cache with a dummy entry so tests
// can verify which caches are cleared and which are preserved.
func populateAllCaches(s *PacketStore) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	dummy := &cachedResult{data: map[string]interface{}{"test": true}, expiresAt: time.Now().Add(time.Hour)}
	s.rfCache["global"] = dummy
	s.topoCache["global"] = dummy
	s.hashCache["global"] = dummy
	s.collisionCache["global"] = dummy
	s.chanCache["global"] = dummy
	s.distCache["global"] = dummy
	s.subpathCache["global"] = dummy
}

// cachePopulated returns which caches still have their "global" entry.
func cachePopulated(s *PacketStore) map[string]bool {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return map[string]bool{
		"rf":        len(s.rfCache) > 0,
		"topo":      len(s.topoCache) > 0,
		"hash":      len(s.hashCache) > 0,
		"collision": len(s.collisionCache) > 0,
		"chan":       len(s.chanCache) > 0,
		"dist":      len(s.distCache) > 0,
		"subpath":   len(s.subpathCache) > 0,
	}
}

func TestInvalidateCachesFor_Eviction(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{eviction: true})

	pop := cachePopulated(s)
	for name, has := range pop {
		if has {
			t.Errorf("eviction should clear %s cache", name)
		}
	}
}

func TestInvalidateCachesFor_NewObservationsOnly(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})

	pop := cachePopulated(s)
	if pop["rf"] {
		t.Error("rf cache should be cleared on new observations")
	}
	// These should be preserved
	for _, name := range []string{"topo", "hash", "chan", "dist", "subpath"} {
		if !pop[name] {
			t.Errorf("%s cache should NOT be cleared on observation-only ingest", name)
		}
	}
}

func TestInvalidateCachesFor_NewTransmissionsOnly(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasNewTransmissions: true})

	pop := cachePopulated(s)
	if pop["hash"] {
		t.Error("hash cache should be cleared on new transmissions")
	}
	// collisionCache should NOT be cleared by transmissions alone (only by hasNewNodes)
	for _, name := range []string{"rf", "topo", "collision", "chan", "dist", "subpath"} {
		if !pop[name] {
			t.Errorf("%s cache should NOT be cleared on transmission-only ingest", name)
		}
	}
}

func TestInvalidateCachesFor_ChannelDataOnly(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasChannelData: true})

	pop := cachePopulated(s)
	if pop["chan"] {
		t.Error("chan cache should be cleared on channel data")
	}
	for _, name := range []string{"rf", "topo", "hash", "dist", "subpath"} {
		if !pop[name] {
			t.Errorf("%s cache should NOT be cleared on channel-data-only ingest", name)
		}
	}
}

func TestInvalidateCachesFor_NewPaths(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasNewPaths: true})

	pop := cachePopulated(s)
	for _, name := range []string{"topo", "dist", "subpath"} {
		if pop[name] {
			t.Errorf("%s cache should be cleared on new paths", name)
		}
	}
	for _, name := range []string{"rf", "hash", "chan"} {
		if !pop[name] {
			t.Errorf("%s cache should NOT be cleared on path-only ingest", name)
		}
	}
}

func TestInvalidateCachesFor_CombinedFlags(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	// Simulate a typical ingest: new transmissions with observations but no GRP_TXT
	s.invalidateCachesFor(cacheInvalidation{
		hasNewObservations:  true,
		hasNewTransmissions: true,
		hasNewPaths:         true,
	})

	pop := cachePopulated(s)
	// rf, topo, hash, dist, subpath should all be cleared
	for _, name := range []string{"rf", "topo", "hash", "dist", "subpath"} {
		if pop[name] {
			t.Errorf("%s cache should be cleared with combined flags", name)
		}
	}
	// chan should be preserved (no GRP_TXT)
	if !pop["chan"] {
		t.Error("chan cache should NOT be cleared without hasChannelData flag")
	}
}

func TestInvalidateCachesFor_NoFlags(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{})

	pop := cachePopulated(s)
	for name, has := range pop {
		if !has {
			t.Errorf("%s cache should be preserved when no flags are set", name)
		}
	}
}

// TestInvalidationRateLimited verifies that rapid ingest cycles don't clear
// caches immediately — they accumulate dirty flags during the cooldown period
// and apply them on the next call after cooldown expires (fixes #533).
func TestInvalidationRateLimited(t *testing.T) {
	s := newTestStore(t)
	s.invCooldown = 100 * time.Millisecond // short cooldown for testing

	// First invalidation should go through immediately
	populateAllCaches(s)
	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})
	state := cachePopulated(s)
	if state["rf"] {
		t.Error("rf cache should be cleared on first invalidation")
	}
	if !state["topo"] {
		t.Error("topo cache should survive (no path changes)")
	}

	// Repopulate and call again within cooldown — should NOT clear
	populateAllCaches(s)
	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})
	state = cachePopulated(s)
	if !state["rf"] {
		t.Error("rf cache should survive during cooldown period")
	}

	// Wait for cooldown to expire
	time.Sleep(150 * time.Millisecond)

	// Next call should apply accumulated + current flags
	populateAllCaches(s)
	s.invalidateCachesFor(cacheInvalidation{hasNewPaths: true})
	state = cachePopulated(s)
	if state["rf"] {
		t.Error("rf cache should be cleared (pending from cooldown)")
	}
	if state["topo"] {
		t.Error("topo cache should be cleared (current call has hasNewPaths)")
	}
	if !state["hash"] {
		t.Error("hash cache should survive (no transmission changes)")
	}
}

// TestInvalidationCooldownAccumulatesFlags verifies that multiple calls during
// cooldown merge their flags correctly.
func TestInvalidationCooldownAccumulatesFlags(t *testing.T) {
	s := newTestStore(t)
	s.invCooldown = 200 * time.Millisecond

	// Initial invalidation (goes through, starts cooldown)
	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})

	// Several calls during cooldown with different flags
	s.invalidateCachesFor(cacheInvalidation{hasNewPaths: true})
	s.invalidateCachesFor(cacheInvalidation{hasNewTransmissions: true})
	s.invalidateCachesFor(cacheInvalidation{hasChannelData: true})

	// Verify pending has all flags
	s.cacheMu.Lock()
	if s.pendingInv == nil {
		t.Fatal("pendingInv should not be nil during cooldown")
	}
	if !s.pendingInv.hasNewPaths || !s.pendingInv.hasNewTransmissions || !s.pendingInv.hasChannelData {
		t.Error("all flags should be accumulated in pendingInv")
	}
	// hasNewObservations was applied immediately, not accumulated
	if s.pendingInv.hasNewObservations {
		t.Error("hasNewObservations was already applied, should not be in pending")
	}
	s.cacheMu.Unlock()

	// Wait for cooldown, then trigger — all accumulated flags should apply
	time.Sleep(250 * time.Millisecond)
	populateAllCaches(s)
	s.invalidateCachesFor(cacheInvalidation{}) // empty trigger
	state := cachePopulated(s)

	// Pending had paths, transmissions, channels — all those caches should clear
	if state["topo"] {
		t.Error("topo should be cleared (pending hasNewPaths)")
	}
	if state["hash"] {
		t.Error("hash should be cleared (pending hasNewTransmissions)")
	}
	if state["chan"] {
		t.Error("chan should be cleared (pending hasChannelData)")
	}
}

// TestEvictionBypassesCooldown verifies eviction always clears immediately.
func TestEvictionBypassesCooldown(t *testing.T) {
	s := newTestStore(t)
	s.invCooldown = 10 * time.Second // long cooldown

	// Start cooldown
	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})

	// Eviction during cooldown should still clear everything
	populateAllCaches(s)
	s.invalidateCachesFor(cacheInvalidation{eviction: true})
	state := cachePopulated(s)
	for name, has := range state {
		if has {
			t.Errorf("%s cache should be cleared on eviction even during cooldown", name)
		}
	}
	// pendingInv should be cleared
	s.cacheMu.Lock()
	if s.pendingInv != nil {
		t.Error("pendingInv should be nil after eviction")
	}
	s.cacheMu.Unlock()
}

// BenchmarkCacheHitDuringIngestion simulates rapid ingestion and verifies
// that cache hits now occur thanks to rate-limited invalidation.
func BenchmarkCacheHitDuringIngestion(b *testing.B) {
	s := &PacketStore{
		rfCache:      make(map[string]*cachedResult),
		topoCache:    make(map[string]*cachedResult),
		hashCache:    make(map[string]*cachedResult),
		chanCache:    make(map[string]*cachedResult),
		distCache:    make(map[string]*cachedResult),
		subpathCache: make(map[string]*cachedResult),
		rfCacheTTL:   15 * time.Second,
		invCooldown:  50 * time.Millisecond,
	}

	// Trigger first invalidation to start cooldown timer
	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})

	var hits, misses int64
	for i := 0; i < b.N; i++ {
		// Populate cache (simulates an analytics query filling the cache)
		s.cacheMu.Lock()
		s.rfCache["global"] = &cachedResult{
			data:      map[string]interface{}{"test": true},
			expiresAt: time.Now().Add(time.Hour),
		}
		s.cacheMu.Unlock()

		// Simulate rapid ingest invalidation (should be rate-limited)
		s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})

		// Check if cache survived the invalidation
		s.cacheMu.Lock()
		if len(s.rfCache) > 0 {
			hits++
		} else {
			misses++
		}
		s.cacheMu.Unlock()
	}

	if hits == 0 {
		b.Errorf("expected cache hits > 0 with rate-limited invalidation, got 0 hits / %d misses", misses)
	}
	b.ReportMetric(float64(hits)/float64(hits+misses)*100, "hit%")
}

// TestInvCooldownFromConfig verifies that invalidationDebounce from config
// is wired to invCooldown on PacketStore.
func TestInvCooldownFromConfig(t *testing.T) {
	// Default without config
	ps := NewPacketStore(nil, nil)
	if ps.invCooldown != 300*time.Second {
		t.Errorf("default invCooldown = %v, want 300s", ps.invCooldown)
	}

	// With config override
	ct := map[string]interface{}{"invalidationDebounce": float64(60)}
	ps2 := NewPacketStore(nil, nil, ct)
	if ps2.invCooldown != 60*time.Second {
		t.Errorf("configured invCooldown = %v, want 60s", ps2.invCooldown)
	}
}

// TestCollisionCacheNotClearedByTransmissions verifies that collisionCache
// is only cleared by hasNewNodes, not hasNewTransmissions (fixes #720).
func TestCollisionCacheNotClearedByTransmissions(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasNewTransmissions: true})

	pop := cachePopulated(s)
	if !pop["collision"] {
		t.Error("collisionCache should NOT be cleared by hasNewTransmissions alone")
	}
	if pop["hash"] {
		t.Error("hashCache should be cleared by hasNewTransmissions")
	}
}

// TestCollisionCacheClearedByNewNodes verifies that collisionCache IS cleared
// when genuinely new nodes are discovered.
func TestCollisionCacheClearedByNewNodes(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasNewNodes: true})

	pop := cachePopulated(s)
	if pop["collision"] {
		t.Error("collisionCache should be cleared by hasNewNodes")
	}
	// Other caches should survive
	for _, name := range []string{"rf", "topo", "hash", "chan", "dist", "subpath"} {
		if !pop[name] {
			t.Errorf("%s cache should NOT be cleared on new-nodes-only ingest", name)
		}
	}
}

// TestCacheSurvivesMultipleIngestCyclesWithinCooldown verifies that caches
// survive repeated ingest cycles during the cooldown period.
func TestCacheSurvivesMultipleIngestCyclesWithinCooldown(t *testing.T) {
	s := newTestStore(t)
	s.invCooldown = 200 * time.Millisecond

	// First invalidation goes through (starts cooldown)
	populateAllCaches(s)
	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})
	pop := cachePopulated(s)
	if pop["rf"] {
		t.Error("rf should be cleared on first invalidation")
	}

	// Repopulate and simulate 5 rapid ingest cycles
	populateAllCaches(s)
	for i := 0; i < 5; i++ {
		s.invalidateCachesFor(cacheInvalidation{
			hasNewObservations:  true,
			hasNewTransmissions: true,
			hasNewPaths:         true,
		})
	}

	// All caches should survive during cooldown
	pop = cachePopulated(s)
	for name, has := range pop {
		if !has {
			t.Errorf("%s cache should survive during cooldown period (ingest cycle %d)", name, 5)
		}
	}
}

// TestNewNodesAccumulatedDuringCooldown verifies that hasNewNodes flags
// accumulated during cooldown are applied when cooldown expires.
func TestNewNodesAccumulatedDuringCooldown(t *testing.T) {
	s := newTestStore(t)
	s.invCooldown = 100 * time.Millisecond

	// First call starts cooldown
	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})

	// During cooldown, accumulate hasNewNodes
	s.invalidateCachesFor(cacheInvalidation{hasNewNodes: true})

	// Verify accumulated
	s.cacheMu.Lock()
	if s.pendingInv == nil || !s.pendingInv.hasNewNodes {
		t.Error("hasNewNodes should be accumulated in pendingInv")
	}
	s.cacheMu.Unlock()

	// Wait for cooldown
	time.Sleep(150 * time.Millisecond)

	// Trigger flush
	populateAllCaches(s)
	s.invalidateCachesFor(cacheInvalidation{})

	pop := cachePopulated(s)
	if pop["collision"] {
		t.Error("collisionCache should be cleared after pending hasNewNodes is flushed")
	}
}

// BenchmarkAnalyticsLatencyCacheHitVsMiss benchmarks cache hit vs miss
// for analytics endpoints to demonstrate the performance impact.
func BenchmarkAnalyticsLatencyCacheHitVsMiss(b *testing.B) {
	s := &PacketStore{
		rfCache:        make(map[string]*cachedResult),
		topoCache:      make(map[string]*cachedResult),
		hashCache:      make(map[string]*cachedResult),
		collisionCache: make(map[string]*cachedResult),
		chanCache:      make(map[string]*cachedResult),
		distCache:      make(map[string]*cachedResult),
		subpathCache:   make(map[string]*cachedResult),
		rfCacheTTL:     1800 * time.Second,
		invCooldown:    300 * time.Second,
	}

	// Pre-populate cache
	s.cacheMu.Lock()
	s.rfCache["global"] = &cachedResult{
		data:      map[string]interface{}{"bins": make([]int, 100)},
		expiresAt: time.Now().Add(time.Hour),
	}
	s.cacheMu.Unlock()

	// Trigger initial invalidation to start cooldown
	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})

	var hits, misses int64
	for i := 0; i < b.N; i++ {
		// Re-populate (simulates query filling cache)
		s.cacheMu.Lock()
		if len(s.rfCache) == 0 {
			s.rfCache["global"] = &cachedResult{
				data:      map[string]interface{}{"bins": make([]int, 100)},
				expiresAt: time.Now().Add(time.Hour),
			}
		}
		s.cacheMu.Unlock()

		// Simulate ingest (rate-limited)
		s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})

		// Check hit
		s.cacheMu.Lock()
		if len(s.rfCache) > 0 {
			hits++
		} else {
			misses++
		}
		s.cacheMu.Unlock()
	}

	hitRate := float64(hits) / float64(hits+misses) * 100
	b.ReportMetric(hitRate, "hit%")
	if hitRate < 50 {
		b.Errorf("hit rate %.1f%% is below 50%% target", hitRate)
	}
}
