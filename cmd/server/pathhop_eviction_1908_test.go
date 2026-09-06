package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPathHopEviction1908(t *testing.T) {
	for _, mode := range []string{"time-direct", "time-run", "memory-empty-batch", "memory-index-disabled"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC()
			store := makeTestStore(8, now.Add(-30*time.Minute), 0)
			store.byPayloadType[5] = store.byPayloadType[4]
			delete(store.byPayloadType, 4)
			store.byPathHop = make(map[string][]*StoreTx)
			store.useResolvedPathIndex = mode != "memory-index-disabled"
			store.initResolvedPathIndex()
			store.retentionHours = 1
			if strings.HasPrefix(mode, "memory-") {
				store.maxMemoryMB = 1
				store.trackedBytes = 8 * 1048576 // The 25% cap evicts the first two.
			} else {
				for _, tx := range store.packets[:2] {
					tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
				}
			}

			shared := strings.Repeat("a", 64)
			expiredOnly := strings.Repeat("b", 64)
			retainedOnly := strings.Repeat("c", 64)
			expiredScope, retainedScope := "expired-scope", "retained-scope"
			hopsSeen := make(map[string]bool)
			for i, tx := range store.packets {
				*tx.PayloadType = 5 // Non-advert traffic contributes to relay stats.
				pk := retainedOnly
				tx.ScopeName = &retainedScope
				if i < 2 {
					pk = expiredOnly
					tx.ScopeName = &expiredScope
				}
				addTxToPathHopIndex(store.byPathHop, tx)
				store.indexResolvedPathHops(tx, []string{shared, pk}, hopsSeen)
			}
			// Repeated observations can append the same resolved association again.
			store.indexResolvedPathHops(store.packets[0], []string{shared, expiredOnly}, hopsSeen)
			store.indexResolvedPathHops(store.packets[7], []string{shared}, hopsSeen)
			addTxToPathHopIndex(store.byPathHop, store.packets[0])

			// Keep the original backing arrays observable: a shortened slice must
			// not hide pointers that keep evicted transmissions alive for the GC.
			backingArrays := make(map[string][]*StoreTx, len(store.byPathHop))
			for key, list := range store.byPathHop {
				backingArrays[key] = list[:cap(list)]
			}
			before := store.GetRepeaterNodeStatsBatchCached([]string{shared}, 24)[shared].Info
			if before.RelayCount24h != 8 || len(before.TransportedScopes) != 2 {
				t.Fatalf("fixture must index all transmissions and scopes: %+v", before)
			}

			var evicted int
			if mode == "time-run" {
				evicted = store.RunEviction() // No DB: cleanup cannot depend on SQL.
			} else {
				store.mu.Lock()
				if strings.HasPrefix(mode, "memory-") {
					evicted = store.EvictStaleWithRP(map[int][]string{})
				} else {
					evicted = store.EvictStale()
				}
				store.mu.Unlock()
			}
			if evicted != 2 {
				t.Fatalf("evicted %d transmissions, want 2", evicted)
			}

			expected := map[string][]int{
				"aa":         {3, 4, 5, 6, 7, 8},
				"bb":         {3, 4, 5, 6, 7, 8},
				"cc":         {3, 4, 5, 6, 7, 8},
				shared:       {3, 4, 5, 6, 7, 8, 8},
				retainedOnly: {3, 4, 5, 6, 7, 8},
			}
			if len(store.byPathHop) != len(expected) {
				t.Errorf("path-hop index has %d keys, want %d (expired-only keys must disappear)", len(store.byPathHop), len(expected))
			}
			for key, want := range expected {
				var ids []int
				for _, tx := range store.byPathHop[key] {
					ids = append(ids, tx.ID)
				}
				if !reflect.DeepEqual(ids, want) {
					t.Errorf("path-hop bucket %q = %v, want %v", key, ids, want)
				}
			}
			for key, list := range backingArrays {
				for _, tx := range list {
					if tx != nil && tx.ID <= 2 {
						t.Errorf("backing array for %q still retains evicted tx %d", key, tx.ID)
						break
					}
				}
			}
			if store.relayStatsCache != nil {
				t.Error("eviction must invalidate the relay-stats cache")
			}
			after := store.GetRepeaterNodeStatsBatchCached([]string{shared}, 24)[shared].Info
			if after.RelayCount24h != 6 {
				t.Errorf("relay count after eviction = %d, want 6", after.RelayCount24h)
			}
			if !reflect.DeepEqual(after.TransportedScopes, []string{retainedScope}) {
				t.Errorf("transported scopes after eviction = %v, want only retained scope", after.TransportedScopes)
			}

			// A later sweep that removes every survivor must also remove every key.
			for _, tx := range store.packets {
				tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
			}
			if got := store.RunEviction(); got != 6 {
				t.Fatalf("final eviction removed %d transmissions, want 6", got)
			}
			if len(store.byPathHop) != 0 {
				t.Errorf("empty store retains %d path-hop keys", len(store.byPathHop))
			}
		})
	}
}

// Benchmark the eviction path with 8 raw and 8 resolved hops per transmission,
// 2 observations each, and 2,048 resolved relays sharing 256 raw-prefix buckets.
// Other secondary indexes are omitted to isolate path-hop cleanup cost. Setup
// is outside the timer; the timed operation includes acquiring the store lock.
func BenchmarkPathHopEviction1908(b *testing.B) {
	const hops = 8
	pubkeys := make([]string, 2048)
	for i := range pubkeys {
		pubkeys[i] = fmt.Sprintf("%02x%062x", i%256, i)
	}
	for _, count := range []int{30000, 100000} {
		for _, evictCount := range []int{1, count / 10, count / 4} {
			b.Run(fmt.Sprintf("tx=%d/evict=%d", count, evictCount), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					now := time.Now().UTC()
					old := now.Add(-2 * time.Hour).Format(time.RFC3339)
					recent := now.Add(-30 * time.Minute).Format(time.RFC3339)
					store := &PacketStore{
						packets:        make([]*StoreTx, count),
						byPathHop:      make(map[string][]*StoreTx, len(pubkeys)+256),
						spIndex:        make(map[string]int),
						retentionHours: 1,
					}
					hopsSeen := make(map[string]bool, hops*2)
					for j := range store.packets {
						tx := &StoreTx{ID: j + 1, FirstSeen: recent, parsedPath: make([]string, hops), pathParsed: true}
						if j < evictCount {
							tx.FirstSeen = old
						}
						tx.Observations = []*StoreObs{{ID: j * 2}, {ID: j*2 + 1}}
						resolved := make([]string, hops)
						for k := range resolved {
							resolved[k] = pubkeys[(j*17+k*13)%len(pubkeys)]
							tx.parsedPath[k] = resolved[k][:2]
						}
						store.packets[j] = tx
						addTxToPathHopIndex(store.byPathHop, tx)
						store.addResolvedPubkeysToPathHopIndex(tx, resolved, hopsSeen)
					}
					b.StartTimer()
					store.mu.Lock()
					got := store.EvictStale()
					store.mu.Unlock()
					b.StopTimer()
					if got != evictCount {
						b.Fatalf("evicted %d transmissions, want %d", got, evictCount)
					}
				}
				b.ReportMetric(float64(count*hops*2), "entries/batch")
			})
		}
	}
}
