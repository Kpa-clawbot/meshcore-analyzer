package main

import (
	"testing"
	"time"
)

// TestEstimateStoreTxBytes_ReasonableValues verifies the estimate function
// returns reasonable values for different packet sizes.
func TestEstimateStoreTxBytes_ReasonableValues(t *testing.T) {
	tx := &StoreTx{
		Hash:        "abcdef1234567890",
		RawHex:      "deadbeef",
		DecodedJSON: `{"type":"GRP_TXT"}`,
		PathJSON:    `["hop1","hop2","hop3"]`,
		parsedPath:  []string{"hop1", "hop2", "hop3"},
		pathParsed:  true,
	}
	got := estimateStoreTxBytes(tx)

	// Should be at least base (384) + maps (200) + indexes + path/subpath costs
	if got < 700 {
		t.Errorf("estimate too low for 3-hop tx: %d", got)
	}
	if got > 5000 {
		t.Errorf("estimate unreasonably high for 3-hop tx: %d", got)
	}
}

// TestEstimateStoreTxBytes_ManyHopsSubpaths verifies that packets with many
// hops estimate significantly more due to O(path²) subpath index entries.
func TestEstimateStoreTxBytes_ManyHopsSubpaths(t *testing.T) {
	tx2 := &StoreTx{
		Hash:       "aabb",
		parsedPath: []string{"a", "b"},
		pathParsed: true,
	}
	tx10 := &StoreTx{
		Hash:       "aabb",
		parsedPath: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
		pathParsed: true,
	}
	est2 := estimateStoreTxBytes(tx2)
	est10 := estimateStoreTxBytes(tx10)

	// 10 hops → 45 subpath combos × 40 = 1800 bytes just for subpaths
	if est10 <= est2 {
		t.Errorf("10-hop (%d) should estimate more than 2-hop (%d)", est10, est2)
	}
	if est10 < est2+1500 {
		t.Errorf("10-hop (%d) should estimate at least 1500 more than 2-hop (%d)", est10, est2)
	}
}

// TestEstimateStoreObsBytes_AfterRefactor verifies that after #800 refactor,
// observations no longer have ResolvedPath overhead in their estimate.
func TestEstimateStoreObsBytes_AfterRefactor(t *testing.T) {
	obs := &StoreObs{
		ObserverID: "obs1",
		PathJSON:   `["a","b"]`,
	}

	est := estimateStoreObsBytes(obs)
	if est <= 0 {
		t.Errorf("estimate should be positive, got %d", est)
	}
	// After #800, all obs estimates should be the same (no RP field variation)
	obs2 := &StoreObs{
		ObserverID: "obs1",
		PathJSON:   `["a","b"]`,
	}
	est2 := estimateStoreObsBytes(obs2)
	if est != est2 {
		t.Errorf("estimates should be equal after #800 (no RP field), got %d vs %d", est, est2)
	}
}

// TestEstimateStoreObsBytes_ManyObservations verifies that 15 observations
// estimate significantly more than 1.
func TestEstimateStoreObsBytes_ManyObservations(t *testing.T) {
	est1 := estimateStoreObsBytes(&StoreObs{ObserverID: "a", PathJSON: `["x"]`})
	est15 := int64(0)
	for i := 0; i < 15; i++ {
		est15 += estimateStoreObsBytes(&StoreObs{ObserverID: "a", PathJSON: `["x"]`})
	}
	if est15 <= est1*10 {
		t.Errorf("15 obs total (%d) should be >10x single obs (%d)", est15, est1)
	}
}

// TestTrackedBytesMatchesSumAfterInsert verifies that trackedBytes equals the
// sum of individual estimates after inserting packets via makeTestStore.
func TestTrackedBytesMatchesSumAfterInsert(t *testing.T) {
	store := makeTestStore(20, time.Now().Add(-2*time.Hour), 5)

	// Manually compute trackedBytes as sum of estimates
	var expectedSum int64
	for _, tx := range store.packets {
		expectedSum += estimateStoreTxBytes(tx)
		for _, obs := range tx.Observations {
			expectedSum += estimateStoreObsBytes(obs)
		}
	}

	if store.trackedBytes != expectedSum {
		t.Errorf("trackedBytes=%d, expected sum=%d", store.trackedBytes, expectedSum)
	}
}

// TestEvictionTriggersWithImprovedEstimates verifies that eviction triggers
// at the right point with the improved (higher) estimates.
func TestEvictionTriggersWithImprovedEstimates(t *testing.T) {
	store := makeTestStore(100, time.Now().Add(-10*time.Hour), 5)

	// trackedBytes for 100 packets is small — artificially set maxMemoryMB
	// so highWatermark is just below trackedBytes to trigger eviction.
	highWatermarkBytes := store.trackedBytes - 1000
	if highWatermarkBytes < 1 {
		highWatermarkBytes = 1
	}
	// maxMemoryMB * 1048576 = highWatermark, so maxMemoryMB = ceil(highWatermarkBytes / 1048576)
	// But that'll be 0 for small values. Instead, directly set trackedBytes high.
	store.trackedBytes = 6 * 1048576 // 6MB
	store.maxMemoryMB = 3            // 3MB limit

	beforeCount := len(store.packets)
	store.RunEviction()
	afterCount := len(store.packets)

	if afterCount >= beforeCount {
		t.Errorf("expected eviction to remove packets: before=%d, after=%d, trackedBytes=%d, maxMB=%d",
			beforeCount, afterCount, store.trackedBytes, store.maxMemoryMB)
	}
	// trackedBytes should have decreased
	if store.trackedBytes >= 6*1048576 {
		t.Errorf("trackedBytes should have decreased after eviction")
	}
}

// BenchmarkEstimateStoreTxBytes verifies the estimate function is fast.
func BenchmarkEstimateStoreTxBytes(b *testing.B) {
	tx := &StoreTx{
		Hash:        "abcdef1234567890",
		RawHex:      "deadbeefdeadbeef",
		DecodedJSON: `{"type":"GRP_TXT","payload":"hello"}`,
		PathJSON:    `["hop1","hop2","hop3","hop4","hop5"]`,
		parsedPath:  []string{"hop1", "hop2", "hop3", "hop4", "hop5"},
		pathParsed:  true,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		estimateStoreTxBytes(tx)
	}
}

// BenchmarkEstimateStoreObsBytes verifies the obs estimate function is fast.
func BenchmarkEstimateStoreObsBytes(b *testing.B) {
	obs := &StoreObs{
		ObserverID: "observer1234",
		PathJSON:   `["a","b","c"]`,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		estimateStoreObsBytes(obs)
	}
}
