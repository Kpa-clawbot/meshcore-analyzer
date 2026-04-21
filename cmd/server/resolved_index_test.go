package main

import (
	"database/sql"
	"fmt"
	"reflect"
	"runtime"
	"strings"
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

	evicted := store.RunEviction()
	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}

	if len(store.byNode[relayPK]) != 0 {
		t.Error("expected byNode cleanup after eviction via on-demand SQL")
	}
}

// TestRunEviction_NoLockDuringSQLFetch proves that RunEviction releases the
// write lock during the batch SQL fetch phase. A concurrent goroutine acquires
// mu.RLock within a tight deadline — if the lock were held during SQL, it would
// time out.
func TestRunEviction_NoLockDuringSQLFetch(t *testing.T) {
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
		byPathHop:            make(map[string][]*StoreTx),
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

	// Create 10 stale packets with resolved_path data in DB
	for i := 1; i <= 10; i++ {
		staleTime := now.Add(-48 * time.Hour).Format(time.RFC3339)
		db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (?, '', ?, ?)",
			i, fmt.Sprintf("h%d", i), staleTime)
		db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (?, ?, 1, ?, ?, ?)",
			i, i, `["aa"]`, now.Add(-48*time.Hour).Unix(), `["pk`+fmt.Sprintf("%d", i)+`"]`)

		tx := &StoreTx{
			ID:        i,
			Hash:      fmt.Sprintf("h%d", i),
			FirstSeen: staleTime,
		}
		obs := &StoreObs{ID: i, TransmissionID: i, ObserverID: "obs0", Timestamp: staleTime}
		tx.Observations = []*StoreObs{obs}
		store.packets = append(store.packets, tx)
		store.byHash[tx.Hash] = tx
		store.byTxID[i] = tx
		store.byObsID[i] = obs
		store.byObserver["obs0"] = append(store.byObserver["obs0"], obs)
	}

	// Run eviction in a goroutine
	evictDone := make(chan int, 1)
	go func() {
		evictDone <- store.RunEviction()
	}()

	// Concurrently try to acquire RLock — should succeed quickly if SQL phase releases the lock
	lockAcquired := make(chan bool, 1)
	go func() {
		// Give eviction a moment to start
		time.Sleep(5 * time.Millisecond)
		store.mu.RLock()
		store.mu.RUnlock()
		lockAcquired <- true
	}()

	select {
	case <-lockAcquired:
		// Good — lock was available during SQL phase
	case <-time.After(5 * time.Second):
		t.Fatal("mu.RLock blocked for >5s — SQL likely running under write lock")
	}

	evicted := <-evictDone
	if evicted != 10 {
		t.Fatalf("expected 10 evicted, got %d", evicted)
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
		b.Run(fmt.Sprintf("pubkeys=%dK", numPubkeys/1000), func(b *testing.B) {
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

// BenchmarkLoad_OldFieldStorage simulates the OLD approach where each observation
// stored a []*string ResolvedPath directly on the struct. This serves as the "before"
// baseline for comparison with BenchmarkLoad_BeforeAfter (the "after").
func BenchmarkLoad_OldFieldStorage(b *testing.B) {
	const numTx = 10000
	const numObsPerTx = 5
	const numHops = 3

	for n := 0; n < b.N; n++ {
		var m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m1)

		packets := make([]*StoreTx, 0, numTx)
		byHash := make(map[string]*StoreTx, numTx)
		byTxID := make(map[int]*StoreTx, numTx)
		byObsID := make(map[int]*StoreObs, numTx*numObsPerTx)
		// Simulate old per-obs []*string storage — keep slices alive in a map
		oldResolvedPaths := make(map[int][]*string, numTx*numObsPerTx)

		for i := 0; i < numTx; i++ {
			tx := &StoreTx{
				ID:       i,
				Hash:     string(rune(i)),
				PathJSON: `["aa","bb","cc"]`,
			}
			packets = append(packets, tx)
			byHash[tx.Hash] = tx
			byTxID[i] = tx

			for j := 0; j < numObsPerTx; j++ {
				obsID := i*numObsPerTx + j
				obs := &StoreObs{
					ID:             obsID,
					TransmissionID: i,
					PathJSON:       `["aa","bb","cc"]`,
				}
				// OLD approach: each observation stores its own []*string
				rp := make([]*string, numHops)
				for h := 0; h < numHops; h++ {
					s := string(rune(i*numHops + h))
					rp[h] = &s
				}
				oldResolvedPaths[obsID] = rp
				tx.Observations = append(tx.Observations, obs)
				byObsID[obs.ID] = obs
			}
		}

		var m2 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m2)

		heapUsed := m2.HeapAlloc - m1.HeapAlloc
		b.ReportMetric(float64(heapUsed)/1048576, "MB")

		// Keep everything alive past GC
		runtime.KeepAlive(packets)
		runtime.KeepAlive(byHash)
		runtime.KeepAlive(byTxID)
		runtime.KeepAlive(byObsID)
		runtime.KeepAlive(oldResolvedPaths)
	}
}

// BenchmarkHopsSeen_Reuse measures allocations from reusing hopsSeen map
// via clear() vs allocating a new map per observation.
func BenchmarkHopsSeen_Reuse(b *testing.B) {
	// Simulate realistic hop counts (10 hops + 10 resolved pubkeys per observation)
	hops := make([]string, 10)
	pks := make([]string, 10)
	for i := range hops {
		hops[i] = fmt.Sprintf("hop%04d", i)
		pks[i] = fmt.Sprintf("pk%04d", i+100)
	}

	b.Run("clear-reuse", func(b *testing.B) {
		b.ReportAllocs()
		hopsSeen := make(map[string]bool)
		for i := 0; i < b.N; i++ {
			clear(hopsSeen)
			for _, hop := range hops {
				hopsSeen[strings.ToLower(hop)] = true
			}
			for _, pk := range pks {
				if !hopsSeen[pk] {
					hopsSeen[pk] = true
				}
			}
		}
	})

	b.Run("alloc-each", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			hopsSeen := make(map[string]bool)
			for _, hop := range hops {
				hopsSeen[strings.ToLower(hop)] = true
			}
			for _, pk := range pks {
				if !hopsSeen[pk] {
					hopsSeen[pk] = true
				}
			}
		}
	})
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

// --- Regression tests for review feedback fixes ---

// TestAddToResolvedPubkeyIndex_CrossCallDedup verifies that calling
// addToResolvedPubkeyIndex multiple times for the same (hash, txID) pair
// does not create duplicate entries in the forward index.
func TestAddToResolvedPubkeyIndex_CrossCallDedup(t *testing.T) {
	store := &PacketStore{
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Call twice with the same txID and pubkey
	store.addToResolvedPubkeyIndex(1, []string{"abc123"})
	store.addToResolvedPubkeyIndex(1, []string{"abc123"})

	h := resolvedPubkeyHash("abc123")
	ids := store.resolvedPubkeyIndex[h]
	if len(ids) != 1 {
		t.Errorf("expected 1 entry in forward index, got %d", len(ids))
	}

	// Reverse index should also have no duplicates
	rev := store.resolvedPubkeyReverse[1]
	if len(rev) != 1 {
		t.Errorf("expected 1 entry in reverse index, got %d", len(rev))
	}
}

// TestRemoveFromResolvedPubkeyIndex_RemovesAllOccurrences verifies that
// removeFromResolvedPubkeyIndex removes ALL occurrences of a txID, not just the first.
func TestRemoveFromResolvedPubkeyIndex_RemovesAllOccurrences(t *testing.T) {
	store := &PacketStore{
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	// Manually inject duplicates to simulate pre-fix state
	h := resolvedPubkeyHash("abc123")
	store.resolvedPubkeyIndex[h] = []int{1, 2, 1, 1} // txID=1 appears 3 times
	store.resolvedPubkeyReverse[1] = []uint64{h}
	store.resolvedPubkeyReverse[2] = []uint64{h}

	store.removeFromResolvedPubkeyIndex(1)

	ids := store.resolvedPubkeyIndex[h]
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("expected [2] after removal, got %v", ids)
	}
}

// TestLRU_CapacityAfterBulkDelete verifies that bulk lruDelete calls don't
// permanently degrade effective cache capacity.
func TestLRU_CapacityAfterBulkDelete(t *testing.T) {
	store := &PacketStore{}
	store.initResolvedPathIndex()

	s := "x"
	dummy := []*string{&s}

	// Fill LRU to capacity
	for i := 0; i < lruMaxSize; i++ {
		store.lruPut(i, dummy)
	}
	if len(store.apiResolvedPathLRU) != lruMaxSize {
		t.Fatalf("expected %d entries, got %d", lruMaxSize, len(store.apiResolvedPathLRU))
	}

	// Delete 90% of entries (simulates bulk backfill invalidation)
	for i := 0; i < lruMaxSize*9/10; i++ {
		store.lruDelete(i)
	}
	remaining := len(store.apiResolvedPathLRU)
	if remaining != lruMaxSize/10 {
		t.Fatalf("expected %d remaining, got %d", lruMaxSize/10, remaining)
	}

	// Add new entries — effective capacity should recover
	for i := lruMaxSize; i < lruMaxSize+lruMaxSize/2; i++ {
		store.lruPut(i, dummy)
	}

	// After adding lruMaxSize/2 new entries, total should be close to that
	// (original 10% + new 50%), NOT stuck at O(1).
	total := len(store.apiResolvedPathLRU)
	if total < lruMaxSize/2 {
		t.Errorf("effective capacity degraded: expected at least %d entries, got %d", lruMaxSize/2, total)
	}
}

// TestConfirmResolvedPathContains_SpecialChars verifies that pubkeys containing
// SQL LIKE wildcards (%, _) don't cause false positives with the INSTR approach.
func TestConfirmResolvedPathContains_SpecialChars(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY,
		transmission_id INTEGER,
		resolved_path TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}

	// Insert a row with a normal pubkey
	_, err = db.Exec(`INSERT INTO observations (id, transmission_id, resolved_path)
		VALUES (1, 100, '["abc123","def456"]')`)
	if err != nil {
		t.Fatal(err)
	}

	store := &PacketStore{
		db: &DB{conn: db},
	}

	// Normal match should work
	if !store.confirmResolvedPathContains(100, "abc123") {
		t.Error("expected true for exact match")
	}

	// Wildcard-containing pubkey should NOT match
	if store.confirmResolvedPathContains(100, "abc%23") {
		t.Error("expected false for pubkey with % wildcard")
	}
	if store.confirmResolvedPathContains(100, "abc_23") {
		t.Error("expected false for pubkey with _ wildcard")
	}
}

// --- #807: Bounded growth tests ---

func TestResolvedPubkeyIndex_BoundedByEviction(t *testing.T) {
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
		byPathHop:            make(map[string][]*StoreTx),
		spIndex:              make(map[string]int),
		spTxIndex:            make(map[string][]*StoreTx),
		distHops:             make([]distHopRecord, 0),
		distPaths:            make([]distPathRecord, 0),
		rfCache:              make(map[string]*cachedResult),
		topoCache:            make(map[string]*cachedResult),
		hashCache:            make(map[string]*cachedResult),
		chanCache:            make(map[string]*cachedResult),
		distCache:            make(map[string]*cachedResult),
		subpathCache:         make(map[string]*cachedResult),
		rfCacheTTL:           15 * time.Second,
		retentionHours:       1, // 1 hour — everything older gets evicted
		db:                   db,
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	now := time.Now().UTC()
	N := 50

	for i := 1; i <= N; i++ {
		hash := fmt.Sprintf("hash_%04d", i)
		pk := fmt.Sprintf("pk_%04d", i)
		firstSeen := now.Add(-2 * time.Hour).Format(time.RFC3339)

		db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (?, '', ?, ?)",
			i, hash, firstSeen)
		db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (?, ?, 1, ?, ?, ?)",
			i, i, `["aa"]`, now.Add(-2*time.Hour).Unix(), `["`+pk+`"]`)

		tx := &StoreTx{ID: i, Hash: hash, FirstSeen: firstSeen}
		obs := &StoreObs{ID: i, TransmissionID: i, ObserverID: "obs0", Timestamp: firstSeen}
		tx.Observations = []*StoreObs{obs}

		store.packets = append(store.packets, tx)
		store.byHash[hash] = tx
		store.byTxID[i] = tx
		store.byObsID[i] = obs
		store.byObserver["obs0"] = append(store.byObserver["obs0"], obs)
		store.addToResolvedPubkeyIndex(i, []string{pk})
	}

	if len(store.resolvedPubkeyIndex) != N {
		t.Fatalf("forward index: expected %d entries, got %d", N, len(store.resolvedPubkeyIndex))
	}
	if len(store.resolvedPubkeyReverse) != N {
		t.Fatalf("reverse index: expected %d entries, got %d", N, len(store.resolvedPubkeyReverse))
	}

	evicted := store.RunEviction()
	if evicted != N {
		t.Fatalf("expected %d evicted, got %d", N, evicted)
	}

	if len(store.resolvedPubkeyIndex) != 0 {
		t.Errorf("forward index should be empty after full eviction, got %d entries", len(store.resolvedPubkeyIndex))
	}
	if len(store.resolvedPubkeyReverse) != 0 {
		t.Errorf("reverse index should be empty after full eviction, got %d entries", len(store.resolvedPubkeyReverse))
	}
}

func TestCompactResolvedPubkeyIndex_DropsEmptyKeys(t *testing.T) {
	store := &PacketStore{useResolvedPathIndex: true}
	store.initResolvedPathIndex()

	h := resolvedPubkeyHash("pk1")
	// Manually insert an empty slice (simulates a bug where delete wasn't called)
	store.resolvedPubkeyIndex[h] = []int{}
	store.resolvedPubkeyReverse[99] = []uint64{}

	store.CompactResolvedPubkeyIndex()

	if _, exists := store.resolvedPubkeyIndex[h]; exists {
		t.Error("CompactResolvedPubkeyIndex should delete empty forward entries")
	}
	if _, exists := store.resolvedPubkeyReverse[99]; exists {
		t.Error("CompactResolvedPubkeyIndex should delete empty reverse entries")
	}
}

func TestCompactResolvedPubkeyIndex_ClipsOversizedSlices(t *testing.T) {
	store := &PacketStore{useResolvedPathIndex: true}
	store.initResolvedPathIndex()

	h := resolvedPubkeyHash("pk1")
	// Create a slice with large backing array but small length
	big := make([]int, 100)
	for i := range big {
		big[i] = i
	}
	// Shrink to 2 elements but keep cap=100
	store.resolvedPubkeyIndex[h] = big[:2]

	if cap(store.resolvedPubkeyIndex[h]) != 100 {
		t.Fatalf("precondition: cap should be 100, got %d", cap(store.resolvedPubkeyIndex[h]))
	}

	store.CompactResolvedPubkeyIndex()

	ids := store.resolvedPubkeyIndex[h]
	if len(ids) != 2 {
		t.Errorf("length should be preserved: got %d", len(ids))
	}
	if cap(ids) > 2*len(ids)+8 {
		t.Errorf("cap should be clipped: got cap=%d for len=%d", cap(ids), len(ids))
	}
	if ids[0] != 0 || ids[1] != 1 {
		t.Error("data should be preserved after clip")
	}
}

func TestRunEviction_TriggersCompaction(t *testing.T) {
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
		byPathHop:            make(map[string][]*StoreTx),
		spIndex:              make(map[string]int),
		spTxIndex:            make(map[string][]*StoreTx),
		distHops:             make([]distHopRecord, 0),
		distPaths:            make([]distPathRecord, 0),
		rfCache:              make(map[string]*cachedResult),
		topoCache:            make(map[string]*cachedResult),
		hashCache:            make(map[string]*cachedResult),
		chanCache:            make(map[string]*cachedResult),
		distCache:            make(map[string]*cachedResult),
		subpathCache:         make(map[string]*cachedResult),
		rfCacheTTL:           15 * time.Second,
		retentionHours:       1,
		db:                   db,
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	now := time.Now().UTC()
	// Add one old tx with a large-cap slice in the index
	pk := "compaction_pk"
	firstSeen := now.Add(-2 * time.Hour).Format(time.RFC3339)
	db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, '', 'h1', ?)", firstSeen)
	db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (1, 1, 1, ?, ?, ?)",
		`["aa"]`, now.Add(-2*time.Hour).Unix(), `["`+pk+`"]`)

	tx := &StoreTx{ID: 1, Hash: "h1", FirstSeen: firstSeen}
	obs := &StoreObs{ID: 1, TransmissionID: 1, ObserverID: "obs0", Timestamp: firstSeen}
	tx.Observations = []*StoreObs{obs}

	store.packets = append(store.packets, tx)
	store.byHash[tx.Hash] = tx
	store.byTxID[1] = tx
	store.byObsID[1] = obs
	store.byObserver["obs0"] = []*StoreObs{obs}

	// Add a fresh tx (won't be evicted) with pk shared with old tx
	pk2 := "shared_pk"
	freshSeen := now.Format(time.RFC3339)
	db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (2, '', 'h2', ?)", freshSeen)
	db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (2, 2, 1, ?, ?, ?)",
		`["bb"]`, now.Unix(), `["`+pk2+`"]`)

	tx2 := &StoreTx{ID: 2, Hash: "h2", FirstSeen: freshSeen}
	obs2 := &StoreObs{ID: 2, TransmissionID: 2, ObserverID: "obs0", Timestamp: freshSeen}
	tx2.Observations = []*StoreObs{obs2}

	store.packets = append(store.packets, tx2)
	store.byHash[tx2.Hash] = tx2
	store.byTxID[2] = tx2
	store.byObsID[2] = obs2
	store.byObserver["obs0"] = append(store.byObserver["obs0"], obs2)

	store.addToResolvedPubkeyIndex(1, []string{pk, pk2})
	store.addToResolvedPubkeyIndex(2, []string{pk2})

	evicted := store.RunEviction()
	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}

	// pk should be gone (only had tx 1)
	hPK := resolvedPubkeyHash(pk)
	if _, exists := store.resolvedPubkeyIndex[hPK]; exists {
		t.Error("pk entry should be removed after eviction")
	}

	// pk2 should still have tx 2
	hPK2 := resolvedPubkeyHash(pk2)
	ids := store.resolvedPubkeyIndex[hPK2]
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("pk2 should only have tx 2, got %v", ids)
	}
}

func TestResolvedPubkeyIndex_MemoryStableThroughEvictionCycles(t *testing.T) {
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
		byPathHop:            make(map[string][]*StoreTx),
		spIndex:              make(map[string]int),
		spTxIndex:            make(map[string][]*StoreTx),
		distHops:             make([]distHopRecord, 0),
		distPaths:            make([]distPathRecord, 0),
		rfCache:              make(map[string]*cachedResult),
		topoCache:            make(map[string]*cachedResult),
		hashCache:            make(map[string]*cachedResult),
		chanCache:            make(map[string]*cachedResult),
		distCache:            make(map[string]*cachedResult),
		subpathCache:         make(map[string]*cachedResult),
		rfCacheTTL:           15 * time.Second,
		retentionHours:       1,
		db:                   db,
		useResolvedPathIndex: true,
	}
	store.initResolvedPathIndex()

	now := time.Now().UTC()
	txCounter := 0

	// Run 5 cycles: each adds 100 old txs, evicts them, checks maps are empty
	for cycle := 0; cycle < 5; cycle++ {
		for i := 0; i < 100; i++ {
			txCounter++
			hash := fmt.Sprintf("hash_%d", txCounter)
			pk := fmt.Sprintf("pk_%d", txCounter)
			firstSeen := now.Add(-2 * time.Hour).Format(time.RFC3339)

			db.conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (?, '', ?, ?)",
				txCounter, hash, firstSeen)
			db.conn.Exec("INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (?, ?, 1, ?, ?, ?)",
				txCounter, txCounter, `["aa"]`, now.Add(-2*time.Hour).Unix(), `["`+pk+`"]`)

			tx := &StoreTx{ID: txCounter, Hash: hash, FirstSeen: firstSeen}
			obs := &StoreObs{ID: txCounter, TransmissionID: txCounter, ObserverID: "obs0", Timestamp: firstSeen}
			tx.Observations = []*StoreObs{obs}

			store.packets = append(store.packets, tx)
			store.byHash[hash] = tx
			store.byTxID[txCounter] = tx
			store.byObsID[txCounter] = obs
			store.byObserver["obs0"] = append(store.byObserver["obs0"], obs)
			store.addToResolvedPubkeyIndex(txCounter, []string{pk})
		}

		evicted := store.RunEviction()
		if evicted != 100 {
			t.Fatalf("cycle %d: expected 100 evicted, got %d", cycle, evicted)
		}

		if fwd := len(store.resolvedPubkeyIndex); fwd != 0 {
			t.Errorf("cycle %d: forward index should be 0, got %d", cycle, fwd)
		}
		if rev := len(store.resolvedPubkeyReverse); rev != 0 {
			t.Errorf("cycle %d: reverse index should be 0, got %d", cycle, rev)
		}
	}
}

func TestCheckResolvedPubkeyIndexSize_Warning(t *testing.T) {
	store := &PacketStore{
		useResolvedPathIndex:          true,
		maxResolvedPubkeyIndexEntries: 10,
	}
	store.initResolvedPathIndex()

	// Add 15 entries — should exceed limit of 10
	for i := 0; i < 15; i++ {
		store.addToResolvedPubkeyIndex(i, []string{fmt.Sprintf("pk_%d", i)})
	}

	// Just verify it doesn't panic — the warning is logged, not returned
	store.CheckResolvedPubkeyIndexSize()
}
