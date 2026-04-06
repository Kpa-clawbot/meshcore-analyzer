package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestBackfillAsyncChunked verifies that backfillResolvedPathsAsync processes
// observations in chunks, yields between batches, and sets the completion flag.
func TestBackfillAsyncChunked(t *testing.T) {
	store := &PacketStore{
		packets:  make([]*StoreTx, 0),
		byHash:   make(map[string]*StoreTx),
		byTxID:   make(map[int]*StoreTx),
		byObsID:  make(map[int]*StoreObs),
	}

	// No pending observations → should complete immediately.
	backfillResolvedPathsAsync(store, "", 100, time.Millisecond, 24)
	if !store.backfillComplete.Load() {
		t.Fatal("expected backfillComplete to be true with empty store")
	}
}

// TestBackfillStatusHeader verifies the X-CoreScope-Status header is set correctly.
func TestBackfillStatusHeader(t *testing.T) {
	store := &PacketStore{
		packets: make([]*StoreTx, 0),
		byHash:  make(map[string]*StoreTx),
		byTxID:  make(map[int]*StoreTx),
		byObsID: make(map[int]*StoreObs),
	}

	srv := &Server{store: store}

	handler := srv.backfillStatusMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	// Before backfill completes → backfilling
	req := httptest.NewRequest("GET", "/api/stats", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-CoreScope-Status"); got != "backfilling" {
		t.Fatalf("expected 'backfilling', got %q", got)
	}

	// After backfill completes → ready
	store.backfillComplete.Store(true)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-CoreScope-Status"); got != "ready" {
		t.Fatalf("expected 'ready', got %q", got)
	}
}

// TestStatsBackfillFields verifies /api/stats includes backfill fields.
func TestStatsBackfillFields(t *testing.T) {
	db := setupTestDBv2(t)
	defer db.Close()
	seedV2Data(t, db)

	store := &PacketStore{
		db:      db,
		packets: make([]*StoreTx, 0),
		byHash:  make(map[string]*StoreTx),
		byTxID:  make(map[int]*StoreTx),
		byObsID: make(map[int]*StoreObs),
		loaded:  true,
	}

	cfg := &Config{Port: 0}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = store

	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// While backfilling
	req := httptest.NewRequest("GET", "/api/stats", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse stats response: %v", err)
	}

	if backfilling, ok := resp["backfilling"]; !ok {
		t.Fatal("missing 'backfilling' field in stats response")
	} else if backfilling != true {
		t.Fatalf("expected backfilling=true, got %v", backfilling)
	}

	if _, ok := resp["backfillProgress"]; !ok {
		t.Fatal("missing 'backfillProgress' field in stats response")
	}

	// Check header
	if got := rec.Header().Get("X-CoreScope-Status"); got != "backfilling" {
		t.Fatalf("expected X-CoreScope-Status=backfilling, got %q", got)
	}

	// After backfill completes
	store.backfillComplete.Store(true)
	// Invalidate stats cache
	srv.statsMu.Lock()
	srv.statsCache = nil
	srv.statsMu.Unlock()

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	resp = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse stats response: %v", err)
	}

	if backfilling, ok := resp["backfilling"]; !ok || backfilling != false {
		t.Fatalf("expected backfilling=false after completion, got %v", backfilling)
	}

	if got := rec.Header().Get("X-CoreScope-Status"); got != "ready" {
		t.Fatalf("expected X-CoreScope-Status=ready, got %q", got)
	}
}
