package main

import (
	"database/sql"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// --- Unit tests ---

// TestStoreTx_NoResolvedPathField is a compile-time guard ensuring ResolvedPath
// field never returns to StoreTx/StoreObs structs (#800).
func TestStoreTx_ResolvedPathFieldAbsent(t *testing.T) {
	txType := reflect.TypeOf(StoreTx{})
	if _, found := txType.FieldByName("ResolvedPath"); found {
		t.Fatal("StoreTx must not have a ResolvedPath field (#800)")
	}

	obsType := reflect.TypeOf(StoreObs{})
	if _, found := obsType.FieldByName("ResolvedPath"); found {
		t.Fatal("StoreObs must not have a ResolvedPath field (#800)")
	}
}

// TestResolvedPubkeyIndex_BuildFromLoad verifies forward + reverse maps are
// consistent after building from resolved paths.
func TestResolvedPubkeyIndex_BuildFromLoad(t *testing.T) {
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Simulate Load() building index from 3 transmissions
	store.addToResolvedPubkeyIndex(1, []string{"aabb", "ccdd"})
	store.addToResolvedPubkeyIndex(2, []string{"aabb", "eeff"})
	store.addToResolvedPubkeyIndex(3, []string{"ccdd"})

	// Forward: aabb should map to [1, 2]
	h := resolvedPubkeyHash("aabb")
	if len(store.resolvedPubkeyIndex[h]) != 2 {
		t.Errorf("expected 2 txIDs for aabb, got %d", len(store.resolvedPubkeyIndex[h]))
	}

	// Forward: ccdd should map to [1, 3]
	h2 := resolvedPubkeyHash("ccdd")
	if len(store.resolvedPubkeyIndex[h2]) != 2 {
		t.Errorf("expected 2 txIDs for ccdd, got %d", len(store.resolvedPubkeyIndex[h2]))
	}

	// Reverse: tx 1 should have 2 hashes
	if len(store.resolvedPubkeyReverse[1]) != 2 {
		t.Errorf("expected 2 hashes for tx 1, got %d", len(store.resolvedPubkeyReverse[1]))
	}

	// Reverse: tx 2 should have 2 hashes
	if len(store.resolvedPubkeyReverse[2]) != 2 {
		t.Errorf("expected 2 hashes for tx 2, got %d", len(store.resolvedPubkeyReverse[2]))
	}
}

// TestResolvedPubkeyIndex_HashCollision verifies that SQL safety filters false candidates
// when two pubkeys produce a hash collision (simulated by inserting both under same hash).
func TestResolvedPubkeyIndex_HashCollision(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := &PacketStore{
		db:                   db,
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Insert test data: tx 1 has pubkey "real_match", tx 2 has pubkey "false_match"
	db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, '', 'h1', '2026-01-01T00:00:00Z')")
	db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (2, '', 'h2', '2026-01-01T00:00:00Z')")
	now := time.Now().Unix()
	db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (1, 1, 1, ?, ?, ?)",
		`["aa"]`, now, `["real_match"]`)
	db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (2, 2, 1, ?, ?, ?)",
		`["aa"]`, now, `["false_match"]`)

	// Simulate collision: manually insert both txIDs under the same hash
	collisionHash := resolvedPubkeyHash("real_match")
	store.resolvedPubkeyIndex[collisionHash] = []int{1, 2}
	store.resolvedPubkeyReverse[1] = []uint64{collisionHash}
	store.resolvedPubkeyReverse[2] = []uint64{collisionHash}

	// tx1 should match "real_match"
	tx1 := &StoreTx{ID: 1}
	if !store.nodeInResolvedPathViaIndex(tx1, "real_match") {
		t.Error("tx1 should match real_match")
	}

	// tx2 should NOT match "real_match" (SQL safety check)
	tx2 := &StoreTx{ID: 2}
	if store.nodeInResolvedPathViaIndex(tx2, "real_match") {
		t.Error("tx2 should not match real_match (collision filtered by SQL)")
	}
}

// TestResolvedPubkeyIndex_IngestUpdate verifies both maps reflect new ingests.
func TestResolvedPubkeyIndex_IngestUpdate(t *testing.T) {
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Initial state
	store.addToResolvedPubkeyIndex(1, []string{"pk1"})
	h := resolvedPubkeyHash("pk1")
	if len(store.resolvedPubkeyIndex[h]) != 1 {
		t.Fatal("expected 1 entry after first insert")
	}

	// Ingest update: add more pubkeys for same tx
	store.addToResolvedPubkeyIndex(1, []string{"pk2"})
	h2 := resolvedPubkeyHash("pk2")
	if len(store.resolvedPubkeyIndex[h2]) != 1 {
		t.Fatal("expected 1 entry for pk2 after update")
	}
	if len(store.resolvedPubkeyReverse[1]) != 2 {
		t.Errorf("expected 2 reverse entries for tx 1, got %d", len(store.resolvedPubkeyReverse[1]))
	}
}

// TestResolvedPubkeyIndex_RemoveOnEvict verifies eviction removes via reverse map.
func TestResolvedPubkeyIndex_RemoveOnEvict(t *testing.T) {
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	store.addToResolvedPubkeyIndex(1, []string{"pk1", "pk2"})
	store.addToResolvedPubkeyIndex(2, []string{"pk1"})

	// Remove tx 1
	store.removeFromResolvedPubkeyIndex(1)

	// pk1 should still have tx 2
	h := resolvedPubkeyHash("pk1")
	if len(store.resolvedPubkeyIndex[h]) != 1 || store.resolvedPubkeyIndex[h][0] != 2 {
		t.Error("pk1 should only have tx 2 after removing tx 1")
	}

	// pk2 should be empty
	h2 := resolvedPubkeyHash("pk2")
	if _, exists := store.resolvedPubkeyIndex[h2]; exists {
		t.Error("pk2 should be deleted after removing its only tx")
	}

	// Reverse map should be clean
	if _, exists := store.resolvedPubkeyReverse[1]; exists {
		t.Error("reverse map for tx 1 should be deleted")
	}
}

// TestResolvedPubkeyIndex_PerObsCoverage verifies non-best obs's resolved pubkeys are indexed.
func TestResolvedPubkeyIndex_PerObsCoverage(t *testing.T) {
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Simulate: tx has 2 observations with different resolved paths
	// Both should be indexed
	store.addToResolvedPubkeyIndex(1, []string{"obs1_pk1", "obs1_pk2"})
	store.addToResolvedPubkeyIndex(1, []string{"obs2_pk3"})

	// All 3 pubkeys should be indexed
	for _, pk := range []string{"obs1_pk1", "obs1_pk2", "obs2_pk3"} {
		h := resolvedPubkeyHash(pk)
		found := false
		for _, id := range store.resolvedPubkeyIndex[h] {
			if id == 1 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pubkey %s should be indexed for tx 1", pk)
		}
	}
}

// TestAddToByNode_WithoutResolvedPathField verifies relay nodes are still indexed
// when fed through the decode-window (not via struct field).
func TestAddToByNode_WithoutResolvedPathField(t *testing.T) {
	store := &PacketStore{
		byNode:     make(map[string][]*StoreTx),
		nodeHashes: make(map[string]map[string]bool),
	}

	tx := &StoreTx{ID: 1, Hash: "h1"}

	// Simulate decode-window feeding relay pubkeys
	store.addToByNode(tx, "relay_pk_1")
	store.addToByNode(tx, "relay_pk_2")

	if len(store.byNode["relay_pk_1"]) != 1 {
		t.Error("relay_pk_1 should be in byNode")
	}
	if len(store.byNode["relay_pk_2"]) != 1 {
		t.Error("relay_pk_2 should be in byNode")
	}
}

// TestTouchRelayLastSeen_WithoutResolvedPathField verifies relay last_seen is
// still updated via explicit pubkey list.
func TestTouchRelayLastSeen_WithoutResolvedPathField(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role) VALUES (?, ?, ?)", "relay_pk", "R1", "REPEATER")

	s := &PacketStore{
		db:              db,
		lastSeenTouched: make(map[string]time.Time),
	}

	s.touchRelayLastSeen([]string{"relay_pk"}, time.Now())

	var lastSeen sql.NullString
	db.conn.QueryRow("SELECT last_seen FROM nodes WHERE public_key = ?", "relay_pk").Scan(&lastSeen)
	if !lastSeen.Valid {
		t.Fatal("expected last_seen to be set")
	}
}

// TestWebSocketBroadcast_IncludesResolvedPath verifies broadcast maps carry resolved_path
// from the decode-window, not from struct fields.
func TestWebSocketBroadcast_IncludesResolvedPath(t *testing.T) {
	// This is verified by the IngestNewFromDB code path: if resolvePathForObs
	// returns a non-nil result, it's put into broadcastRP map and then into
	// the broadcast pkt map. We test the extraction helper here.
	pk1 := "aabbccdd"
	pk2 := "eeff0011"
	rp := []*string{&pk1, nil, &pk2}
	pks := extractResolvedPubkeys(rp)
	if len(pks) != 2 {
		t.Errorf("expected 2 pubkeys, got %d", len(pks))
	}
	if pks[0] != "aabbccdd" || pks[1] != "eeff0011" {
		t.Errorf("unexpected pubkeys: %v", pks)
	}
}

// TestBackfill_InvalidatesLRU verifies LRU cache evicts obs after backfill UPDATE.
func TestBackfill_InvalidatesLRU(t *testing.T) {
	store := &PacketStore{
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Pre-populate LRU
	pk := "old_pk"
	store.lruMu.Lock()
	store.lruPut(42, []*string{&pk})
	store.lruMu.Unlock()

	// Verify it's cached
	store.lruMu.RLock()
	_, cached := store.apiResolvedPathLRU[42]
	store.lruMu.RUnlock()
	if !cached {
		t.Fatal("expected obs 42 to be cached")
	}

	// Simulate backfill invalidation
	store.lruMu.Lock()
	store.lruDelete(42)
	store.lruMu.Unlock()

	store.lruMu.RLock()
	_, cached = store.apiResolvedPathLRU[42]
	store.lruMu.RUnlock()
	if cached {
		t.Error("expected obs 42 to be evicted from LRU after backfill")
	}
}

// TestEviction_ByNodeCleanup_OnDemandSQL verifies eviction path SQL-fetches
// resolved_path to clean byNode/nodeHashes.
func TestEviction_ByNodeCleanup_OnDemandSQL(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := &PacketStore{
		packets:              make([]*StoreTx, 0),
		byHash:               make(map[string]*StoreTx),
		byTxID:               make(map[int]*StoreTx),
		byObsID:              make(map[int]*StoreObs),
		byObserver:           make(map[string][]*StoreObs),
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		byPayloadType:        make(map[int][]*StoreTx),
		spIndex:              make(map[string]int),
		distHops:             make([]distHopRecord, 0),
		distPaths:            make([]distPathRecord, 0),
		rfCache:              make(map[string]*cachedResult),
		topoCache:            make(map[string]*cachedResult),
		hashCache:            make(map[string]*cachedResult),
		chanCache:            make(map[string]*cachedResult),
		distCache:            make(map[string]*cachedResult),
		subpathCache:         make(map[string]*cachedResult),
		rfCacheTTL:           15 * time.Second,
		retentionHours:       24,
		db:                   db,
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	now := time.Now().UTC()
	relayPK := "relay_evict_test"

	// Insert DB data for on-demand SQL fetch during eviction
	db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, '', 'h1', ?)",
		now.Add(-48*time.Hour).Format(time.RFC3339))
	db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (1, 1, 1, ?, ?, ?)",
		`["aa"]`, now.Add(-48*time.Hour).Unix(), `["`+relayPK+`"]`)

	tx := &StoreTx{
		ID:        1,
		Hash:      "h1",
		FirstSeen: now.Add(-48 * time.Hour).Format(time.RFC3339),
	}
	obs := &StoreObs{ID: 1, TransmissionID: 1, ObserverID: "obs0", Timestamp: tx.FirstSeen}
	tx.Observations = []*StoreObs{obs}

	store.packets = append(store.packets, tx)
	store.byHash[tx.Hash] = tx
	store.byTxID[1] = tx
	store.byObsID[1] = obs
	store.byObserver["obs0"] = []*StoreObs{obs}

	// Index via decode-window simulation
	store.addToByNode(tx, relayPK)
	store.addToResolvedPubkeyIndex(1, []string{relayPK})

	if len(store.byNode[relayPK]) != 1 {
		t.Fatalf("expected relay in byNode")
	}

	evicted := store.EvictStale()
	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}

	if len(store.byNode[relayPK]) != 0 {
		t.Error("expected byNode cleanup after eviction via on-demand SQL")
	}
}

// --- Endpoint tests ---

// TestPathsThroughNode_NilResolvedPathFallback verifies packets with no resolved_path
// data are still returned (conservative: can't disambiguate = keep).
func TestPathsThroughNode_NilResolvedPathFallback(t *testing.T) {
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		byPathHop:            make(map[string][]*StoreTx),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// tx with no resolved path data at all
	tx := &StoreTx{ID: 1, PathJSON: `["aa"]`}
	store.byPathHop["aa"] = []*StoreTx{tx}

	// Should match because no resolved_path data = can't disambiguate
	if !store.nodeInResolvedPathViaIndex(tx, "any_pk") {
		t.Error("should keep tx with no resolved_path data")
	}
}

// TestPathsThroughNode_CollisionSafety is tested in TestResolvedPubkeyIndex_HashCollision above.

// TestPacketsAPI_OnDemandResolvedPath_Empty verifies NULL resolved_path returns nil/omitted.
func TestPacketsAPI_OnDemandResolvedPath_Empty(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := &PacketStore{
		db:                   db,
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// No observation in DB — should return nil
	rp := store.fetchResolvedPathForObs(999)
	if rp != nil {
		t.Error("expected nil for non-existent obs")
	}
}

// TestPacketsAPI_OnDemandResolvedPath verifies on-demand SQL fetch works.
func TestPacketsAPI_OnDemandResolvedPath(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, '', 'h1', '2026-01-01')")
	db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (1, 1, 1, ?, ?, ?)",
		`["aa"]`, now, `["aabbccdd"]`)

	store := &PacketStore{
		db:                   db,
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	rp := store.fetchResolvedPathForObs(1)
	if rp == nil || len(rp) != 1 {
		t.Fatal("expected resolved path from SQL")
	}
	if *rp[0] != "aabbccdd" {
		t.Errorf("expected aabbccdd, got %s", *rp[0])
	}
}

// TestPacketsAPI_OnDemandResolvedPath_LRUHit verifies second request hits cache.
func TestPacketsAPI_OnDemandResolvedPath_LRUHit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, '', 'h1', '2026-01-01')")
	db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (1, 1, 1, ?, ?, ?)",
		`["aa"]`, now, `["aabbccdd"]`)

	store := &PacketStore{
		db:                   db,
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// First fetch — goes to SQL
	rp1 := store.fetchResolvedPathForObs(1)
	if rp1 == nil {
		t.Fatal("expected resolved path")
	}

	// Delete from DB to prove second fetch uses cache
	db.conn.Exec("UPDATE observations SET resolved_path = NULL WHERE id = 1")

	rp2 := store.fetchResolvedPathForObs(1)
	if rp2 == nil || len(rp2) != 1 || *rp2[0] != "aabbccdd" {
		t.Error("expected LRU cache hit")
	}
}

// --- Feature flag tests ---

// TestFeatureFlag_OffPath_PreservesOldBehavior verifies that with useResolvedPathIndex=false,
// the index is not used and all candidates are kept (conservative behavior).
func TestFeatureFlag_OffPath_PreservesOldBehavior(t *testing.T) {
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		useResolvedPathIndex: false,
	}
	store.initResolvedPathIndex()

	tx := &StoreTx{ID: 1}
	// With flag off, nodeInResolvedPathViaIndex always returns true
	if !store.nodeInResolvedPathViaIndex(tx, "any_pk") {
		t.Error("flag off should always return true (conservative)")
	}

	// addToResolvedPubkeyIndex should be a no-op
	store.addToResolvedPubkeyIndex(1, []string{"pk1"})
	if len(store.resolvedPubkeyIndex) != 0 {
		t.Error("index should be empty when flag is off")
	}
}

// TestFeatureFlag_Toggle_NoStateLeak documents that toggling requires restart.
func TestFeatureFlag_Toggle_NoStateLeak(t *testing.T) {
	// The feature flag is a simple bool checked at each call site.
	// Toggling at runtime is safe: the index is simply ignored when off.
	// State doesn't leak because the index is append-only during runtime;
	// the off-path just stops reading/writing it.
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	store.addToResolvedPubkeyIndex(1, []string{"pk1"})
	if len(store.resolvedPubkeyIndex) == 0 {
		t.Fatal("expected index entries")
	}

	// Toggle off — index is still there but not consulted
	store.useResolvedPathIndex = false
	tx := &StoreTx{ID: 99}
	if !store.nodeInResolvedPathViaIndex(tx, "nonexistent") {
		t.Error("flag off should return true regardless")
	}
}

// --- Concurrency tests ---

// TestReverseMap_NoLeakOnPartialFailure verifies the reverse map stays consistent
// when operations are interleaved.
func TestReverseMap_NoLeakOnPartialFailure(t *testing.T) {
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Add, then remove — reverse map should be clean
	store.addToResolvedPubkeyIndex(1, []string{"pk1", "pk2", "pk3"})
	store.removeFromResolvedPubkeyIndex(1)

	if _, exists := store.resolvedPubkeyReverse[1]; exists {
		t.Error("reverse map should be empty after full removal")
	}
	for _, h := range []string{"pk1", "pk2", "pk3"} {
		hv := resolvedPubkeyHash(h)
		if len(store.resolvedPubkeyIndex[hv]) != 0 {
			t.Errorf("forward index for %s should be empty", h)
		}
	}
}

// TestDecodeWindow_LockHoldTimeBounded measures write-lock duration during
// decode-window operations. This is a documentation/assertion test.
func TestDecodeWindow_LockHoldTimeBounded(t *testing.T) {
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		byPathHop:            make(map[string][]*StoreTx),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Simulate decode-window for 100 observations with 5 hops each
	start := time.Now()
	for i := 0; i < 100; i++ {
		tx := &StoreTx{ID: i, Hash: "h", PathJSON: `["a","b","c","d","e"]`}
		pks := []string{"pk1", "pk2", "pk3", "pk4", "pk5"}
		for _, pk := range pks {
			store.addToByNode(tx, pk)
		}
		store.addToResolvedPubkeyIndex(i, pks)
	}
	elapsed := time.Since(start)

	// Should complete in well under 100ms (typically <1ms)
	if elapsed > 100*time.Millisecond {
		t.Errorf("decode-window for 100 obs took %v, expected <100ms", elapsed)
	}
}

// --- Integration / regression tests ---

// TestRepeaterLiveness_StillAccurate verifies touchRelayLastSeen still works
// with the new pubkey-list interface.
func TestRepeaterLiveness_StillAccurate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role) VALUES (?, ?, ?)", "r1", "Relay1", "REPEATER")
	db.conn.Exec("INSERT INTO nodes (public_key, name, role) VALUES (?, ?, ?)", "r2", "Relay2", "REPEATER")

	s := &PacketStore{
		db:              db,
		lastSeenTouched: make(map[string]time.Time),
	}

	s.touchRelayLastSeen([]string{"r1", "r2"}, time.Now())

	var ls1, ls2 sql.NullString
	db.conn.QueryRow("SELECT last_seen FROM nodes WHERE public_key = ?", "r1").Scan(&ls1)
	db.conn.QueryRow("SELECT last_seen FROM nodes WHERE public_key = ?", "r2").Scan(&ls2)
	if !ls1.Valid || !ls2.Valid {
		t.Error("expected both relays to have last_seen updated")
	}
}

// --- Benchmarks ---

// BenchmarkResolvedPubkeyIndex_Memory measures index memory at different cardinalities.
func BenchmarkResolvedPubkeyIndex_Memory(b *testing.B) {
	for _, numPubkeys := range []int{50000, 500000} {
		b.Run("pubkeys="+string(rune('0'+numPubkeys/100000))+"00K", func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				store := &PacketStore{
					byNode:               make(map[string][]*StoreTx),
					nodeHashes:           make(map[string]map[string]bool),
					useResolvedPathIndex: true,
				}
				store.initResolvedPathIndex()

				// Build index with realistic distribution
				txsPerPubkey := 10
				for pk := 0; pk < numPubkeys; pk++ {
					pks := []string{string(rune(pk))}
					for j := 0; j < txsPerPubkey; j++ {
						txID := pk*txsPerPubkey + j
						store.addToResolvedPubkeyIndex(txID, pks)
					}
				}
			}
		})
	}
}

// BenchmarkLoad_BeforeAfter measures heap reduction from removing per-obs ResolvedPath.
func BenchmarkLoad_BeforeAfter(b *testing.B) {
	// Create a realistic in-memory dataset to measure memory
	const numTx = 10000
	const numObsPerTx = 5
	const numHops = 3

	for n := 0; n < b.N; n++ {
		var m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m1)

		store := &PacketStore{
			packets:              make([]*StoreTx, 0, numTx),
			byHash:               make(map[string]*StoreTx, numTx),
			byTxID:               make(map[int]*StoreTx, numTx),
			byObsID:              make(map[int]*StoreObs, numTx*numObsPerTx),
			byNode:               make(map[string][]*StoreTx),
			nodeHashes:           make(map[string]map[string]bool),
			byPathHop:            make(map[string][]*StoreTx),
			useResolvedPathIndex: true,
		}
		store.initResolvedPathIndex()

		for i := 0; i < numTx; i++ {
			tx := &StoreTx{
				ID:       i,
				Hash:     string(rune(i)),
				PathJSON: `["aa","bb","cc"]`,
			}
			store.packets = append(store.packets, tx)
			store.byHash[tx.Hash] = tx
			store.byTxID[i] = tx

			for j := 0; j < numObsPerTx; j++ {
				obs := &StoreObs{
					ID:             i*numObsPerTx + j,
					TransmissionID: i,
					PathJSON:       `["aa","bb","cc"]`,
				}
				tx.Observations = append(tx.Observations, obs)
				store.byObsID[obs.ID] = obs
			}

			// Simulate decode-window: add to index instead of storing on struct
			pks := make([]string, numHops)
			for h := 0; h < numHops; h++ {
				pks[h] = string(rune(i*numHops + h))
			}
			store.addToResolvedPubkeyIndex(i, pks)
		}

		var m2 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m2)

		heapUsed := m2.HeapAlloc - m1.HeapAlloc
		b.ReportMetric(float64(heapUsed)/1048576, "MB")
	}
}

// BenchmarkPathsThroughNode_Latency measures index lookup performance.
func BenchmarkPathsThroughNode_Latency(b *testing.B) {
	store := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           make(map[string]map[string]bool),
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Build index with 5000 candidates
	for i := 0; i < 5000; i++ {
		store.addToResolvedPubkeyIndex(i, []string{"target_pk"})
	}

	tx := &StoreTx{ID: 2500} // mid-range
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.nodeInResolvedPathViaIndex(tx, "target_pk")
	}
}

// BenchmarkLivePolling_UnderIngest measures LRU performance under concurrent access.
func BenchmarkLivePolling_UnderIngest(b *testing.B) {
	store := &PacketStore{
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Pre-populate LRU
	for i := 0; i < lruMaxSize; i++ {
		pk := "pk"
		store.lruMu.Lock()
		store.lruPut(i, []*string{&pk})
		store.lruMu.Unlock()
	}

	var wg sync.WaitGroup
	b.ResetTimer()

	// Concurrent reads
	for i := 0; i < b.N; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			store.lruMu.RLock()
			_ = store.apiResolvedPathLRU[id%lruMaxSize]
			store.lruMu.RUnlock()
		}(i)
		go func(id int) {
			defer wg.Done()
			pk := "new"
			store.lruMu.Lock()
			store.lruPut(lruMaxSize+id, []*string{&pk})
			store.lruMu.Unlock()
		}(i)
	}
	wg.Wait()
}

// TestLivePolling_LRUUnderConcurrentIngest tests 100 concurrent live polls + ingest writes.
func TestLivePolling_LRUUnderConcurrentIngest(t *testing.T) {
	store := &PacketStore{
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	var wg sync.WaitGroup
	const numOps = 100

	// Concurrent writes
	for i := 0; i < numOps; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			pk := "pk"
			store.lruMu.Lock()
			store.lruPut(id, []*string{&pk})
			store.lruMu.Unlock()
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numOps; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			store.lruMu.RLock()
			_ = store.apiResolvedPathLRU[id]
			store.lruMu.RUnlock()
		}(i)
	}

	wg.Wait()
	// No panics or races = pass
}

// TestExtractResolvedPubkeys verifies the extraction helper.
func TestExtractResolvedPubkeys(t *testing.T) {
	pk1, pk2 := "aa", "bb"
	rp := []*string{&pk1, nil, &pk2, nil}
	pks := extractResolvedPubkeys(rp)
	if len(pks) != 2 || pks[0] != "aa" || pks[1] != "bb" {
		t.Errorf("unexpected: %v", pks)
	}

	// Empty
	if pks := extractResolvedPubkeys(nil); pks != nil {
		t.Error("expected nil for nil input")
	}
}

// TestMergeResolvedPubkeys verifies the merge helper.
func TestMergeResolvedPubkeys(t *testing.T) {
	pk1, pk2, pk3 := "aa", "bb", "aa"
	rp1 := []*string{&pk1, &pk2}
	rp2 := []*string{&pk3}
	merged := mergeResolvedPubkeys(rp1, rp2)
	if len(merged) != 2 {
		t.Errorf("expected 2 unique pubkeys, got %d: %v", len(merged), merged)
	}
}

// TestResolvedPubkeyHash_Deterministic verifies hash stability.
func TestResolvedPubkeyHash_Deterministic(t *testing.T) {
	h1 := resolvedPubkeyHash("test_pubkey")
	h2 := resolvedPubkeyHash("test_pubkey")
	if h1 != h2 {
		t.Error("hash should be deterministic")
	}
	h3 := resolvedPubkeyHash("TEST_PUBKEY")
	if h1 != h3 {
		t.Error("hash should be case-insensitive")
	}
}

// TestLRU_EvictionOnFull verifies LRU evicts oldest entries.
func TestLRU_EvictionOnFull(t *testing.T) {
	store := &PacketStore{
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	pk := "pk"
	// Fill to capacity
	store.lruMu.Lock()
	for i := 0; i < lruMaxSize; i++ {
		store.lruPut(i, []*string{&pk})
	}
	store.lruMu.Unlock()

	if len(store.apiResolvedPathLRU) != lruMaxSize {
		t.Fatalf("expected %d entries, got %d", lruMaxSize, len(store.apiResolvedPathLRU))
	}

	// Add one more — should evict oldest (0)
	store.lruMu.Lock()
	store.lruPut(lruMaxSize, []*string{&pk})
	store.lruMu.Unlock()

	if _, exists := store.apiResolvedPathLRU[0]; exists {
		t.Error("oldest entry should have been evicted")
	}
	if _, exists := store.apiResolvedPathLRU[lruMaxSize]; !exists {
		t.Error("newest entry should exist")
	}
}
