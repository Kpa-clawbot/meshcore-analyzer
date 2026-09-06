// Issue #1008: subpath + pathHop index builds must move off the
// synchronous Load() critical path into a background goroutine.
//
// Contract:
//  1. Immediately after Load() returns, SubpathIndexReady() and
//     PathHopIndexReady() report false (the goroutine has not finished).
//  2. Analytics handlers that depend on those indices respond 503 with
//     Retry-After: 5 until the corresponding ready flag flips true.
//  3. After the background build completes (waitable via a helper),
//     both flags flip true and handlers respond 200.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestIssue1008_SubpathIndexReadyFalseImmediatelyAfterLoad asserts the
// subpath ready flag is false the instant Load() returns. Red commit: the
// stub returns true → assertion fires. Green commit: the flag is owned by
// the background goroutine, which has not yet run, so the assertion holds.
func TestIssue1008_SubpathIndexReadyFalseImmediatelyAfterLoad(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if store.SubpathIndexReady() {
		t.Fatal("expected SubpathIndexReady()==false immediately after Load(); want background-deferred build (#1008)")
	}
}

// TestIssue1008_PathHopIndexReadyFalseImmediatelyAfterLoad: same contract
// for the path-hop index.
func TestIssue1008_PathHopIndexReadyFalseImmediatelyAfterLoad(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if store.PathHopIndexReady() {
		t.Fatal("expected PathHopIndexReady()==false immediately after Load(); want background-deferred build (#1008)")
	}
}

// TestIssue1008_HandlerReturns503WhileSubpathIndexLoading asserts the
// analytics/subpaths handler returns 503 + Retry-After: 5 + a JSON body
// matching the triage spec while the subpath index is still building.
func TestIssue1008_HandlerReturns503WhileSubpathIndexLoading(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// Finish the background build before simulating not-ready, so it cannot
	// flip the flag back to true before the handler checks it.
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("background builds never finished")
	}
	store.subpathReady.Store(false)
	cfg := &Config{}
	cfg.applyListLimitsDefaults()
	srv := &Server{store: store, cfg: cfg}

	req := httptest.NewRequest("GET", "/api/analytics/subpaths?minLen=2&maxLen=4&limit=10", nil)
	rec := httptest.NewRecorder()
	srv.handleAnalyticsSubpaths(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (subpath index loading, #1008)", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After header = %q, want %q", got, "5")
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not valid JSON: %v (body=%s)", err, rec.Body.String())
	}
	if body["error"] != "index loading" {
		t.Errorf(`body["error"] = %v, want "index loading"`, body["error"])
	}
}

// TestIssue1008_HandlerRecoversAfterIndexReady asserts that, once the
// background build completes, the handler returns 200.
func TestIssue1008_HandlerRecoversAfterIndexReady(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Wait up to 5s for both background builds to finish on this small
	// fixture (rich test DB has ~3 packets; build is sub-millisecond).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if store.SubpathIndexReady() && store.PathHopIndexReady() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !store.SubpathIndexReady() {
		t.Fatal("SubpathIndexReady() never flipped true within 5s")
	}
	if !store.PathHopIndexReady() {
		t.Fatal("PathHopIndexReady() never flipped true within 5s")
	}

	cfg := &Config{}
	cfg.applyListLimitsDefaults()
	srv := &Server{store: store, cfg: cfg}
	req := httptest.NewRequest("GET", "/api/analytics/subpaths?minLen=2&maxLen=4&limit=10", nil)
	rec := httptest.NewRecorder()
	srv.handleAnalyticsSubpaths(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status after ready = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestIssue1008_m7_BothFlagsSetAfterParallelStart verifies that the
// parallel two-goroutine version of startBackgroundIndexBuilds (review
// m7) sets BOTH ready flags after a bounded wait, regardless of which
// goroutine wins the race to s.mu.Lock(). Sanity check that breaking
// the two builds apart didn't drop the pathHop flag flip.
func TestIssue1008_m7_BothFlagsSetAfterParallelStart(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("indexes never ready after parallel start (#1008 m7)")
	}
	if !store.SubpathIndexReady() {
		t.Error("subpath flag not set after WaitIndexesReady returned true")
	}
	if !store.PathHopIndexReady() {
		t.Error("pathHop flag not set after WaitIndexesReady returned true")
	}
}
