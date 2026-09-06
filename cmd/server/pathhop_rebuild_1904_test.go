package main

import (
	"sync"
	"testing"
)

// Issue #1904: buildPathHopIndex() rebuilds byPathHop from raw path_json
// hops and opens by discarding the map. But byPathHop also holds resolved
// full-pubkey keys fed by indexResolvedPathHops, and those pubkey strings
// are retained nowhere (#800 dropped the per-StoreTx ResolvedPath field in
// favour of a hash-only index), so a plain rebuild silently drops every
// resolved attribution. On a cold start that leaves relay_count_*,
// last_relayed and transported_scopes empty for every node until live
// ingestion refills the index.
//
// The rebuild must therefore RETAIN resolved entries — but only for
// transmissions still in s.packets. Eviction's removeTxFromPathHopIndex
// only strips raw hops (it derives them from txGetParsedPath), so evicted
// transmissions linger under their resolved keys; the rebuild is where
// they get dropped.

const (
	hop1904Raw      = "a3"
	hop1904Resolved = "a3f19c2b7d4e5081aa22bb33cc44dd55ee66ff778899000112233445566778899"
	hop1904Evicted  = "b7002211ffeeddccbbaa99887766554433221100aabbccddeeff001122334455"
)

// pathHopTx builds a transmission whose wire path is a single raw hop.
func pathHopTx(id int, path string) *StoreTx {
	return &StoreTx{ID: id, Hash: "pathhop-tx-" + path, PathJSON: `["` + hop1904Raw + `"]`}
}

func newPathHopStore(packets []*StoreTx, idx map[string][]*StoreTx) *PacketStore {
	return &PacketStore{packets: packets, byPathHop: idx, mu: sync.RWMutex{}}
}

func hopKeyHas(idx map[string][]*StoreTx, key string, tx *StoreTx) bool {
	for _, t := range idx[key] {
		if t == tx {
			return true
		}
	}
	return false
}

func TestBuildPathHopIndex_RetainsResolvedHops_1904(t *testing.T) {
	tx := pathHopTx(1, hop1904Raw)
	// Shape produced by a chunk scan: raw hop from path_json PLUS the
	// resolved full pubkey that indexResolvedPathHops added.
	store := newPathHopStore([]*StoreTx{tx}, map[string][]*StoreTx{
		hop1904Raw:      {tx},
		hop1904Resolved: {tx},
	})

	store.buildPathHopIndex()

	if !hopKeyHas(store.byPathHop, hop1904Raw, tx) {
		t.Fatalf("raw hop %q lost by rebuild", hop1904Raw)
	}
	if !hopKeyHas(store.byPathHop, hop1904Resolved, tx) {
		t.Fatalf("resolved pubkey hop %q dropped by rebuild — #1904", hop1904Resolved)
	}
}

func TestBuildPathHopIndex_DropsResolvedHopsOfEvictedTx_1904(t *testing.T) {
	live := pathHopTx(1, hop1904Raw)
	evicted := pathHopTx(2, hop1904Raw) // NOT in s.packets any more

	store := newPathHopStore([]*StoreTx{live}, map[string][]*StoreTx{
		hop1904Raw:      {live},
		hop1904Resolved: {live},
		hop1904Evicted:  {evicted},
	})

	store.buildPathHopIndex()

	if hopKeyHas(store.byPathHop, hop1904Evicted, evicted) {
		t.Fatal("evicted transmission retained under its resolved key — the rebuild must not turn eviction's known gap into a permanent leak")
	}
	if _, ok := store.byPathHop[hop1904Evicted]; ok {
		t.Fatalf("empty key %q left behind after dropping its only entry", hop1904Evicted)
	}
	if !hopKeyHas(store.byPathHop, hop1904Resolved, live) {
		t.Fatal("live resolved hop dropped while pruning evicted ones")
	}
}

func TestBuildPathHopIndex_NoDuplicateOnRepeatedBuild_1904(t *testing.T) {
	tx := pathHopTx(1, hop1904Raw)
	store := newPathHopStore([]*StoreTx{tx}, map[string][]*StoreTx{
		hop1904Raw:      {tx},
		hop1904Resolved: {tx},
	})

	store.buildPathHopIndex()
	store.buildPathHopIndex()

	if got := len(store.byPathHop[hop1904Raw]); got != 1 {
		t.Fatalf("raw hop bucket has %d entries after two builds, want 1", got)
	}
	if got := len(store.byPathHop[hop1904Resolved]); got != 1 {
		t.Fatalf("resolved hop bucket has %d entries after two builds, want 1", got)
	}
}
