package main

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// makeTestStore creates a PacketStore with fake packets for eviction testing.
// It does NOT use a DB — indexes are populated manually.
func makeTestStore(count int, startTime time.Time, intervalMin int) *PacketStore {
	store := &PacketStore{
		packets:       make([]*StoreTx, 0, count),
		byHash:        make(map[string]*StoreTx, count),
		byTxID:        make(map[int]*StoreTx, count),
		byObsID:       make(map[int]*StoreObs, count*2),
		byObserver:    make(map[string][]*StoreObs),
		byNode:        make(map[string][]*StoreTx),
		nodeHashes:    make(map[string]map[string]bool),
		byPayloadType: make(map[int][]*StoreTx),
		spIndex:       make(map[string]int),
		distHops:      make([]distHopRecord, 0),
		distPaths:     make([]distPathRecord, 0),
		rfCache:       make(map[string]*cachedResult),
		topoCache:     make(map[string]*cachedResult),
		hashCache:     make(map[string]*cachedResult),
		chanCache:     make(map[string]*cachedResult),
		distCache:     make(map[string]*cachedResult),
		subpathCache:  make(map[string]*cachedResult),
		rfCacheTTL:    15 * time.Second,
	}

	obsID := 1000
	for i := 0; i < count; i++ {
		ts := startTime.Add(time.Duration(i*intervalMin) * time.Minute)
		hash := fmt.Sprintf("hash%04d", i)
		txID := i + 1
		pt := 4 // ADVERT
		decodedJSON := fmt.Sprintf(`{"pubKey":"pk%04d"}`, i)

		tx := &StoreTx{
			ID:          txID,
			Hash:        hash,
			FirstSeen:   ts.UTC().Format(time.RFC3339),
			PayloadType: &pt,
			DecodedJSON: decodedJSON,
			PathJSON:    `["aa","bb","cc"]`,
		}

		// Add 2 observations per tx
		for j := 0; j < 2; j++ {
			obsID++
			obsIDStr := fmt.Sprintf("obs%d", j)
			obs := &StoreObs{
				ID:             obsID,
				TransmissionID: txID,
				ObserverID:     obsIDStr,
				ObserverName:   fmt.Sprintf("Observer%d", j),
				Timestamp:      ts.UTC().Format(time.RFC3339),
			}
			tx.Observations = append(tx.Observations, obs)
			tx.ObservationCount++
			store.byObsID[obsID] = obs
			store.byObserver[obsIDStr] = append(store.byObserver[obsIDStr], obs)
			store.totalObs++
		}

		store.packets = append(store.packets, tx)
		store.byHash[hash] = tx
		store.byTxID[txID] = tx
		store.byPayloadType[pt] = append(store.byPayloadType[pt], tx)

		// Index by node
		pk := fmt.Sprintf("pk%04d", i)
		if store.nodeHashes[pk] == nil {
			store.nodeHashes[pk] = make(map[string]bool)
		}
		store.nodeHashes[pk][hash] = true
		store.byNode[pk] = append(store.byNode[pk], tx)

		// Add to distance index
		store.distHops = append(store.distHops, distHopRecord{tx: tx, Hash: hash})
		store.distPaths = append(store.distPaths, distPathRecord{tx: tx, Hash: hash})

		// Subpath index
		addTxToSubpathIndex(store.spIndex, tx)

		// Track bytes for self-accounting
		store.trackedBytes += estimateStoreTxBytes(tx)
		for _, obs := range tx.Observations {
			store.trackedBytes += estimateStoreObsBytes(obs)
		}
	}

	return store
}

func TestEvictStale_TimeBasedEviction(t *testing.T) {
	now := time.Now().UTC()
	// 100 packets: first 50 are 48h old, last 50 are 1h old
	store := makeTestStore(100, now.Add(-48*time.Hour), 0)
	// Override: set first 50 to 48h ago, last 50 to 1h ago
	for i := 0; i < 50; i++ {
		store.packets[i].FirstSeen = now.Add(-48 * time.Hour).Format(time.RFC3339)
	}
	for i := 50; i < 100; i++ {
		store.packets[i].FirstSeen = now.Add(-1 * time.Hour).Format(time.RFC3339)
	}

	store.retentionHours = 24

	evicted := store.EvictStale()
	if evicted != 50 {
		t.Fatalf("expected 50 evicted, got %d", evicted)
	}
	if len(store.packets) != 50 {
		t.Fatalf("expected 50 remaining, got %d", len(store.packets))
	}
	if len(store.byHash) != 50 {
		t.Fatalf("expected 50 in byHash, got %d", len(store.byHash))
	}
	if len(store.byTxID) != 50 {
		t.Fatalf("expected 50 in byTxID, got %d", len(store.byTxID))
	}
	// 50 remaining * 2 obs each = 100 obs
	if store.totalObs != 100 {
		t.Fatalf("expected 100 obs remaining, got %d", store.totalObs)
	}
	if len(store.byObsID) != 100 {
		t.Fatalf("expected 100 in byObsID, got %d", len(store.byObsID))
	}
	if atomic.LoadInt64(&store.evicted) != 50 {
		t.Fatalf("expected evicted counter=50, got %d", atomic.LoadInt64(&store.evicted))
	}

	// Verify evicted hashes are gone
	if _, ok := store.byHash["hash0000"]; ok {
		t.Fatal("hash0000 should have been evicted")
	}
	// Verify remaining hashes exist
	if _, ok := store.byHash["hash0050"]; !ok {
		t.Fatal("hash0050 should still exist")
	}

	// Verify distance indexes cleaned
	if len(store.distHops) != 50 {
		t.Fatalf("expected 50 distHops, got %d", len(store.distHops))
	}
	if len(store.distPaths) != 50 {
		t.Fatalf("expected 50 distPaths, got %d", len(store.distPaths))
	}
}

func TestEvictStale_NoEvictionWhenDisabled(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(10, now.Add(-48*time.Hour), 60)
	// No retention set (defaults to 0)

	evicted := store.EvictStale()
	if evicted != 0 {
		t.Fatalf("expected 0 evicted, got %d", evicted)
	}
	if len(store.packets) != 10 {
		t.Fatalf("expected 10 remaining, got %d", len(store.packets))
	}
}

func TestEvictStale_MemoryBasedEviction(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(1000, now.Add(-1*time.Hour), 0)
	// All packets are recent (1h old) so time-based won't trigger.
	store.retentionHours = 24
	store.maxMemoryMB = 3
	// Set trackedBytes to simulate 6MB (over 3MB limit).
	store.trackedBytes = 6 * 1048576

	evicted := store.EvictStale()
	if evicted == 0 {
		t.Fatal("expected some evictions for memory cap")
	}
	// 25% safety cap should limit to 250 per pass
	if evicted > 250 {
		t.Fatalf("25%% safety cap violated: evicted %d", evicted)
	}
	// trackedBytes should have decreased
	if store.trackedBytes >= 6*1048576 {
		t.Fatal("trackedBytes should have decreased after eviction")
	}
}

// TestEvictStale_MemoryBasedEviction_UnderestimatedHeap verifies that the 25%
// safety cap prevents cascading eviction even when trackedBytes is very high.
func TestEvictStale_MemoryBasedEviction_UnderestimatedHeap(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(1000, now.Add(-1*time.Hour), 0)
	store.retentionHours = 24
	store.maxMemoryMB = 500
	// Simulate trackedBytes 5x over budget.
	store.trackedBytes = 2500 * 1048576

	evicted := store.EvictStale()
	if evicted == 0 {
		t.Fatal("expected evictions when tracked is 5x over limit")
	}
	// Safety cap: max 25% per pass = 250
	if evicted > 250 {
		t.Fatalf("25%% safety cap violated: evicted %d of 1000", evicted)
	}
	if evicted != 250 {
		t.Fatalf("expected exactly 250 evicted (25%% cap), got %d", evicted)
	}
}

func TestEvictStale_CleansNodeIndexes(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(10, now.Add(-48*time.Hour), 0)
	store.retentionHours = 24

	// Verify node indexes exist before eviction
	if len(store.byNode) != 10 {
		t.Fatalf("expected 10 nodes indexed, got %d", len(store.byNode))
	}
	if len(store.nodeHashes) != 10 {
		t.Fatalf("expected 10 nodeHashes, got %d", len(store.nodeHashes))
	}

	evicted := store.EvictStale()
	if evicted != 10 {
		t.Fatalf("expected 10 evicted, got %d", evicted)
	}

	// All should be cleaned
	if len(store.byNode) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(store.byNode))
	}
	if len(store.nodeHashes) != 0 {
		t.Fatalf("expected 0 nodeHashes, got %d", len(store.nodeHashes))
	}
	if len(store.byPayloadType) != 0 {
		t.Fatalf("expected 0 payload types, got %d", len(store.byPayloadType))
	}
	if len(store.byObserver) != 0 {
		t.Fatalf("expected 0 observers, got %d", len(store.byObserver))
	}
}

func TestEvictStale_CleansResolvedPathNodeIndexes(t *testing.T) {
	now := time.Now().UTC()
	store := &PacketStore{
		packets:       make([]*StoreTx, 0),
		byHash:        make(map[string]*StoreTx),
		byTxID:        make(map[int]*StoreTx),
		byObsID:       make(map[int]*StoreObs),
		byObserver:    make(map[string][]*StoreObs),
		byNode:        make(map[string][]*StoreTx),
		nodeHashes:    make(map[string]map[string]bool),
		byPayloadType: make(map[int][]*StoreTx),
		spIndex:       make(map[string]int),
		distHops:      make([]distHopRecord, 0),
		distPaths:     make([]distPathRecord, 0),
		rfCache:       make(map[string]*cachedResult),
		topoCache:     make(map[string]*cachedResult),
		hashCache:     make(map[string]*cachedResult),
		chanCache:     make(map[string]*cachedResult),
		distCache:     make(map[string]*cachedResult),
		subpathCache:  make(map[string]*cachedResult),
		rfCacheTTL:    15 * time.Second,
		retentionHours: 24,
	}

	// Create a packet indexed only via resolved_path (no decoded JSON pubkeys)
	relayPK := "relay0001abcdef"
	tx := &StoreTx{
		ID:        1,
		Hash:      "hash_rp_001",
		FirstSeen: now.Add(-48 * time.Hour).UTC().Format(time.RFC3339),
	}
	rpPtr := &relayPK
	obs := &StoreObs{
		ID:             100,
		TransmissionID: 1,
		ObserverID:     "obs0",
		Timestamp:      tx.FirstSeen,
		ResolvedPath:   []*string{rpPtr},
	}
	tx.Observations = append(tx.Observations, obs)
	tx.ResolvedPath = []*string{rpPtr}

	store.packets = append(store.packets, tx)
	store.byHash[tx.Hash] = tx
	store.byTxID[tx.ID] = tx
	store.byObsID[obs.ID] = obs
	store.byObserver["obs0"] = append(store.byObserver["obs0"], obs)

	// Index via resolved_path
	store.indexByNode(tx)

	// Verify indexed
	if len(store.byNode[relayPK]) != 1 {
		t.Fatalf("expected 1 entry in byNode[%s], got %d", relayPK, len(store.byNode[relayPK]))
	}
	if !store.nodeHashes[relayPK][tx.Hash] {
		t.Fatalf("expected nodeHashes[%s] to contain %s", relayPK, tx.Hash)
	}

	evicted := store.EvictStale()
	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}

	// Verify resolved_path entries are cleaned up
	if len(store.byNode[relayPK]) != 0 {
		t.Fatalf("expected byNode[%s] to be empty after eviction, got %d", relayPK, len(store.byNode[relayPK]))
	}
	if _, exists := store.nodeHashes[relayPK]; exists {
		t.Fatalf("expected nodeHashes[%s] to be deleted after eviction", relayPK)
	}
}

func TestEvictStale_RunEvictionThreadSafe(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(20, now.Add(-48*time.Hour), 0)
	store.retentionHours = 24

	evicted := store.RunEviction()
	if evicted != 20 {
		t.Fatalf("expected 20 evicted, got %d", evicted)
	}
}

func TestStartEvictionTicker_NoopWhenDisabled(t *testing.T) {
	store := &PacketStore{}
	stop := store.StartEvictionTicker()
	stop() // should not panic
}

func TestNewPacketStoreWithConfig(t *testing.T) {
	cfg := &PacketStoreConfig{
		RetentionHours: 48,
		MaxMemoryMB:    512,
	}
	store := NewPacketStore(nil, cfg)
	if store.retentionHours != 48 {
		t.Fatalf("expected retentionHours=48, got %f", store.retentionHours)
	}
	if store.maxMemoryMB != 512 {
		t.Fatalf("expected maxMemoryMB=512, got %d", store.maxMemoryMB)
	}
}

func TestNewPacketStoreNilConfig(t *testing.T) {
	store := NewPacketStore(nil, nil)
	if store.retentionHours != 0 {
		t.Fatalf("expected retentionHours=0, got %f", store.retentionHours)
	}
}

func TestCacheTTLFromConfig(t *testing.T) {
	// With config values: analyticsHashSizes and analyticsRF should override defaults.
	cacheTTL := map[string]interface{}{
		"analyticsHashSizes": float64(7200),
		"analyticsRF":        float64(300),
	}
	store := NewPacketStore(nil, nil, cacheTTL)
	if store.collisionCacheTTL != 7200*time.Second {
		t.Fatalf("expected collisionCacheTTL=7200s, got %v", store.collisionCacheTTL)
	}
	if store.rfCacheTTL != 300*time.Second {
		t.Fatalf("expected rfCacheTTL=300s, got %v", store.rfCacheTTL)
	}
}

func TestCacheTTLDefaults(t *testing.T) {
	// Without config, defaults should apply.
	store := NewPacketStore(nil, nil)
	if store.collisionCacheTTL != 3600*time.Second {
		t.Fatalf("expected default collisionCacheTTL=3600s, got %v", store.collisionCacheTTL)
	}
	if store.rfCacheTTL != 15*time.Second {
		t.Fatalf("expected default rfCacheTTL=15s, got %v", store.rfCacheTTL)
	}
}

// --- Self-accounting memory tracking tests ---

func TestTrackedBytes_IncreasesOnInsert(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(0, now, 0)
	if store.trackedBytes != 0 {
		t.Fatalf("expected 0 trackedBytes for empty store, got %d", store.trackedBytes)
	}

	store2 := makeTestStore(10, now, 1)
	if store2.trackedBytes <= 0 {
		t.Fatal("expected positive trackedBytes after inserting 10 packets")
	}
	// Each packet has 2 observations; should be roughly 10*(384+5*48) + 20*(192+2*48) = 10*624 + 20*288 = 12000
	expectedMin := int64(10*600 + 20*250) // rough lower bound
	if store2.trackedBytes < expectedMin {
		t.Fatalf("trackedBytes %d seems too low (expected > %d)", store2.trackedBytes, expectedMin)
	}
}

func TestTrackedBytes_DecreasesOnEvict(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(100, now.Add(-48*time.Hour), 0)
	store.retentionHours = 24

	beforeBytes := store.trackedBytes
	if beforeBytes <= 0 {
		t.Fatal("expected positive trackedBytes before eviction")
	}

	evicted := store.EvictStale()
	if evicted != 100 {
		t.Fatalf("expected 100 evicted, got %d", evicted)
	}
	if store.trackedBytes != 0 {
		t.Fatalf("expected 0 trackedBytes after evicting all, got %d", store.trackedBytes)
	}
}

func TestTrackedBytes_MatchesExpectedAfterMixedInsertEvict(t *testing.T) {
	now := time.Now().UTC()
	// Create 100 packets, 50 old + 50 recent
	store := makeTestStore(100, now.Add(-48*time.Hour), 0)
	for i := 50; i < 100; i++ {
		store.packets[i].FirstSeen = now.Add(-1 * time.Hour).Format(time.RFC3339)
	}
	store.retentionHours = 24

	totalBefore := store.trackedBytes

	// Calculate expected bytes for first 50 packets (to be evicted)
	var evictedBytes int64
	for i := 0; i < 50; i++ {
		tx := store.packets[i]
		evictedBytes += estimateStoreTxBytes(tx)
		for _, obs := range tx.Observations {
			evictedBytes += estimateStoreObsBytes(obs)
		}
	}

	store.EvictStale()

	expectedAfter := totalBefore - evictedBytes
	if store.trackedBytes != expectedAfter {
		t.Fatalf("trackedBytes %d != expected %d (before=%d, evicted=%d)",
			store.trackedBytes, expectedAfter, totalBefore, evictedBytes)
	}
}

func TestWatermarkHysteresis(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(1000, now.Add(-1*time.Hour), 0)
	store.retentionHours = 0 // no time-based eviction
	store.maxMemoryMB = 1    // 1MB budget

	// Set trackedBytes to just above high watermark
	highWatermark := int64(1 * 1048576)
	lowWatermark := int64(float64(highWatermark) * 0.85)
	store.trackedBytes = highWatermark + 1

	evicted := store.EvictStale()
	if evicted == 0 {
		t.Fatal("expected eviction when above high watermark")
	}
	if store.trackedBytes > lowWatermark+1024 {
		t.Fatalf("expected trackedBytes near low watermark after eviction, got %d (low=%d)",
			store.trackedBytes, lowWatermark)
	}

	// Now set trackedBytes to just below high watermark — should NOT trigger
	store.trackedBytes = highWatermark - 1
	evicted2 := store.EvictStale()
	if evicted2 != 0 {
		t.Fatalf("expected no eviction below high watermark, got %d", evicted2)
	}
}

func TestSafetyCap25Percent(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(1000, now.Add(-1*time.Hour), 0)
	store.retentionHours = 0
	store.maxMemoryMB = 1

	// Set trackedBytes way over limit to force maximum eviction
	store.trackedBytes = 100 * 1048576 // 100MB vs 1MB limit

	evicted := store.EvictStale()
	// 25% of 1000 = 250
	if evicted > 250 {
		t.Fatalf("25%% safety cap violated: evicted %d of 1000 (max should be 250)", evicted)
	}
	if evicted != 250 {
		t.Fatalf("expected exactly 250 evicted (25%% cap), got %d", evicted)
	}
	if len(store.packets) != 750 {
		t.Fatalf("expected 750 remaining, got %d", len(store.packets))
	}
}

func TestMultiplePassesConverge(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(1000, now.Add(-1*time.Hour), 0)
	store.retentionHours = 0
	// Set budget to half the actual tracked bytes — requires ~2 passes
	actualBytes := store.trackedBytes
	store.maxMemoryMB = int(float64(actualBytes) / 1048576.0 / 2)
	if store.maxMemoryMB < 1 {
		store.maxMemoryMB = 1
	}

	totalEvicted := 0
	for pass := 0; pass < 20; pass++ {
		evicted := store.EvictStale()
		if evicted == 0 {
			break
		}
		totalEvicted += evicted
	}

	// After convergence, trackedBytes should be at or below high watermark
	// (may be between low and high due to hysteresis — that's fine)
	highWatermark := int64(store.maxMemoryMB) * 1048576
	if store.trackedBytes > highWatermark {
		t.Fatalf("did not converge: trackedBytes=%d (%.1fMB) > highWatermark=%d after multiple passes",
			store.trackedBytes, float64(store.trackedBytes)/1048576.0, highWatermark)
	}
	if totalEvicted == 0 {
		t.Fatal("expected some evictions across multiple passes")
	}
}

func TestEstimateStoreTxBytes(t *testing.T) {
	tx := &StoreTx{
		RawHex:      "aabbcc",
		Hash:        "hash1234",
		DecodedJSON: `{"pubKey":"pk1"}`,
		PathJSON:    `["aa","bb"]`,
	}
	est := estimateStoreTxBytes(tx)
	// Verify the function returns a reasonable value matching our manual calculation
	manualCalc := int64(storeTxBaseBytes) + int64(len(tx.RawHex)+len(tx.Hash)+len(tx.DecodedJSON)+len(tx.PathJSON)) + int64(numIndexesPerTx*indexEntryBytes)
	if est != manualCalc {
		t.Fatalf("estimateStoreTxBytes = %d, want %d (manual calc)", est, manualCalc)
	}
	if est < 600 || est > 800 {
		t.Fatalf("estimateStoreTxBytes = %d, expected in range [600, 800]", est)
	}
}

func TestEstimateStoreObsBytes(t *testing.T) {
	obs := &StoreObs{
		ObserverID: "obs123",
		PathJSON:   `["aa"]`,
	}
	est := estimateStoreObsBytes(obs)
	// storeObsBaseBytes(192) + len(ObserverID=6) + len(PathJSON=6) + 2*48(96) = 300
	expected := int64(192 + 6 + 6 + 2*48)
	if est != expected {
		t.Fatalf("estimateStoreObsBytes = %d, want %d", est, expected)
	}
}

func BenchmarkEviction100K(b *testing.B) {
	now := time.Now().UTC()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		store := makeTestStore(100000, now.Add(-48*time.Hour), 0)
		store.retentionHours = 24
		b.StartTimer()
		store.EvictStale()
	}
}
