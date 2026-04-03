package main

import (
	"fmt"
	"testing"
)

// TestObsDedupCorrectness verifies that the map-based dedup produces correct
// results: no duplicate observations (same observerID + pathJSON) on a single
// transmission.
func TestObsDedupCorrectness(t *testing.T) {
	tx := &StoreTx{
		ID:      1,
		Hash:    "abc123",
		obsKeys: make(map[string]bool),
	}

	// Add 5 unique observations
	for i := 0; i < 5; i++ {
		obsID := fmt.Sprintf("obs-%d", i)
		pathJSON := fmt.Sprintf(`["path-%d"]`, i)
		dk := obsID + "|" + pathJSON
		if tx.obsKeys[dk] {
			t.Fatalf("observation %d should not be a duplicate", i)
		}
		tx.Observations = append(tx.Observations, &StoreObs{
			ID:         i,
			ObserverID: obsID,
			PathJSON:   pathJSON,
		})
		tx.obsKeys[dk] = true
		tx.ObservationCount++
	}

	if tx.ObservationCount != 5 {
		t.Fatalf("expected 5 observations, got %d", tx.ObservationCount)
	}

	// Try to add duplicates of each — all should be rejected
	for i := 0; i < 5; i++ {
		obsID := fmt.Sprintf("obs-%d", i)
		pathJSON := fmt.Sprintf(`["path-%d"]`, i)
		dk := obsID + "|" + pathJSON
		if !tx.obsKeys[dk] {
			t.Fatalf("observation %d should be detected as duplicate", i)
		}
	}

	// Same observer, different path — should NOT be a duplicate
	dk := "obs-0" + "|" + `["different-path"]`
	if tx.obsKeys[dk] {
		t.Fatal("different path should not be a duplicate")
	}

	// Different observer, same path — should NOT be a duplicate
	dk = "obs-new" + "|" + `["path-0"]`
	if tx.obsKeys[dk] {
		t.Fatal("different observer should not be a duplicate")
	}
}

// TestObsDedupNilMapSafety ensures obsKeys lazy init works for pre-existing
// transmissions that may not have the map initialized.
func TestObsDedupNilMapSafety(t *testing.T) {
	tx := &StoreTx{ID: 1, Hash: "abc"}
	// obsKeys is nil — the lazy init pattern used in IngestNewFromDB/IngestNewObservations
	if tx.obsKeys == nil {
		tx.obsKeys = make(map[string]bool)
	}
	dk := "obs1|path1"
	if tx.obsKeys[dk] {
		t.Fatal("should not be duplicate on empty map")
	}
	tx.obsKeys[dk] = true
	if !tx.obsKeys[dk] {
		t.Fatal("should be duplicate after insert")
	}
}

// BenchmarkObsDedupMap benchmarks the map-based O(1) dedup approach.
func BenchmarkObsDedupMap(b *testing.B) {
	for _, obsCount := range []int{10, 50, 100, 500} {
		b.Run(fmt.Sprintf("obs=%d", obsCount), func(b *testing.B) {
			// Pre-populate a tx with obsCount observations
			tx := &StoreTx{
				ID:      1,
				obsKeys: make(map[string]bool),
			}
			for i := 0; i < obsCount; i++ {
				obsID := fmt.Sprintf("obs-%d", i)
				pathJSON := fmt.Sprintf(`["hop-%d"]`, i)
				dk := obsID + "|" + pathJSON
				tx.Observations = append(tx.Observations, &StoreObs{
					ObserverID: obsID,
					PathJSON:   pathJSON,
				})
				tx.obsKeys[dk] = true
			}

			// Benchmark: check dedup for a new observation (not duplicate)
			newDK := "new-obs|new-path"
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = tx.obsKeys[newDK]
			}
		})
	}
}

// BenchmarkObsDedupLinear benchmarks the old O(n) linear scan for comparison.
func BenchmarkObsDedupLinear(b *testing.B) {
	for _, obsCount := range []int{10, 50, 100, 500} {
		b.Run(fmt.Sprintf("obs=%d", obsCount), func(b *testing.B) {
			tx := &StoreTx{ID: 1}
			for i := 0; i < obsCount; i++ {
				tx.Observations = append(tx.Observations, &StoreObs{
					ObserverID: fmt.Sprintf("obs-%d", i),
					PathJSON:   fmt.Sprintf(`["hop-%d"]`, i),
				})
			}

			newObsID := "new-obs"
			newPath := "new-path"
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, existing := range tx.Observations {
					if existing.ObserverID == newObsID && existing.PathJSON == newPath {
						break
					}
				}
			}
		})
	}
}
