package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHandleNodesPerfLargeFleet asserts the /api/nodes endpoint (no `limit`
// param — relying on the server-side default) returns in well under 2s on a
// realistic-shape fleet: 600 nodes, ~50 of them repeaters/rooms with rich
// path-hop activity, and a non-trivial byPayloadType + byPathHop index.
//
// Regression guard for issue #1257:
//
//	/api/nodes (no limit) → 32.9s, 30KB on staging (637 nodes)
//	/api/nodes?limit=2000 → 4.9s,  360KB
//
// Root cause class: per-repeater enrichment in handleNodes calls
// store.GetRepeaterRelayInfo + GetRepeaterUsefulnessScore separately for
// each node in the page. Each call takes its own RLock and walks
// byPathHop[pk] / byPayloadType, doing expensive timestamp parsing.
// For the default-page case (top-50 by last_seen, mostly hot repeaters)
// that is hundreds of thousands of timestamp parses per request.
//
// Budget: 2s. On the broken implementation with this fixture the
// endpoint blows the budget; with batched/cached per-page enrichment it
// completes in well under 500ms.
func TestHandleNodesPerfLargeFleet(t *testing.T) {
	if testing.Short() {
		t.Skip("perf test")
	}
	srv, router := setupTestServer(t)
	conn := srv.db.conn

	// Seed 600 nodes — 50 repeaters/rooms with most-recent last_seen so
	// they land on the default page, plus 550 stale companions.
	tx, err := conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, foreign_advert)
		VALUES (?, ?, ?, 0, 0, ?, '2026-01-01T00:00:00Z', 1, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 50; i++ {
		pk := fmt.Sprintf("pkrepeat%056x", i)
		ts := now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		if _, err := stmt.Exec(pk, fmt.Sprintf("rep%d", i), "repeater", ts); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 550; i++ {
		pk := fmt.Sprintf("pkcompan%056x", i)
		ts := now.Add(-time.Duration(60+i) * time.Minute).Format(time.RFC3339Nano)
		if _, err := stmt.Exec(pk, fmt.Sprintf("comp%d", i), "companion", ts); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Seed the in-memory packet store: a body of non-advert traffic with
	// each repeater appearing as a path hop on many of them. This is what
	// makes the per-node GetRepeaterRelayInfo / GetRepeaterUsefulnessScore
	// calls expensive on the broken impl.
	const numTx = 150000
	const hopsPerTx = 6
	pt2 := 2 // non-advert payload type
	store := srv.store
	// Also index every tx under a single shared 1-byte prefix so the
	// GetRepeaterRelayInfo prefix-collision branch fans every per-node
	// call through the full non-advert tx set (matches production where
	// many repeaters share a 1-byte hop prefix).
	for i := 0; i < numTx; i++ {
		txID := 100000 + i
		ts := now.Add(-time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		stx := &StoreTx{
			ID:          txID,
			Hash:        fmt.Sprintf("h%d", txID),
			FirstSeen:   ts,
			PayloadType: &pt2,
		}
		store.byPayloadType[pt2] = append(store.byPayloadType[pt2], stx)
		store.byPathHop["pk"] = append(store.byPathHop["pk"], stx)
		// Index each repeater under byPathHop so per-node enrichment walks
		// a non-trivial slice.
		for h := 0; h < hopsPerTx; h++ {
			repIdx := (i + h) % 50
			pk := fmt.Sprintf("pkrepeat%056x", repIdx)
			store.byPathHop[pk] = append(store.byPathHop[pk], stx)
		}
	}

	// Warm-up to amortize first-call costs (cache misses, prepare). Note:
	// the per-node Repeater* enrichment is NOT cached, so this warmup
	// does not hide the perf bug — it only amortizes one-shot prep.
	{
		req := httptest.NewRequest("GET", "/api/nodes?limit=1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("warmup status=%d body=%s", w.Code, w.Body.String())
		}
	}

	start := time.Now()
	req := httptest.NewRequest("GET", "/api/nodes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	elapsed := time.Since(start)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	const budget = 2 * time.Second
	t.Logf("/api/nodes (no limit) elapsed=%v on %d nodes, %d tx", elapsed, 600, numTx)
	if elapsed > budget {
		t.Fatalf("/api/nodes (no limit) too slow for #1257: %v (budget %v) on %d nodes, %d tx",
			elapsed, budget, 600, numTx)
	}
}
