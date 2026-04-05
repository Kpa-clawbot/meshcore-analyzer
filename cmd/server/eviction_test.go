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
	// Inject deterministic estimator: simulates 6MB (over 3MB limit).
	// Uses packet count so it scales correctly after eviction.
	store.memoryEstimator = func() float64 {
		return float64(len(store.packets)*5120+store.totalObs*500) / 1048576.0
	}

	evicted := store.EvictStale()
	if evicted == 0 {
		t.Fatal("expected some evictions for memory cap")
	}
	estMB := store.estimatedMemoryMB()
	if estMB > 3.5 {
		t.Fatalf("expected <=3.5MB after eviction, got %.1fMB", estMB)
	}
}

// TestEvictStale_MemoryBasedEviction_UnderestimatedHeap verifies that eviction
// fires correctly when actual heap is much larger than a formula-based estimate
// would report — the scenario that caused OOM kills in production.
func TestEvictStale_MemoryBasedEviction_UnderestimatedHeap(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(1000, now.Add(-1*time.Hour), 0)
	store.retentionHours = 24
	store.maxMemoryMB = 500
	// Simulate actual heap 5x over budget (like production: ~5GB actual vs ~1GB limit).
	store.memoryEstimator = func() float64 {
		return 2500.0 // 2500MB actual vs 500MB limit
	}

	evicted := store.EvictStale()
	if evicted == 0 {
		t.Fatal("expected evictions when heap is 5x over limit")
	}
	// Should keep roughly 500/2500 * 0.9 = 18% of packets → ~180 of 1000.
	remaining := len(store.packets)
	if remaining > 250 {
		t.Fatalf("expected most packets evicted (heap 5x over), but %d of 1000 remain", remaining)
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
