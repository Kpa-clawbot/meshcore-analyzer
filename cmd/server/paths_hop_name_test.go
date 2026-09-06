package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestHandleNodePaths_HopName_CanonicalPathShowsTarget_1144 is the regression
// test for issue #1144.
//
// Bug: the biased hop resolver picked a GPS-having sibling over the actual target
// node when the target had no GPS coordinates, causing the wrong name in hop slots.
//
// Fix: the canonical-path branch (Option A) uses lookupNode(resolvedPK) with the
// full pubkey stored in resolved_path, bypassing the biased resolver entirely.
// This test verifies that when two nodes share a short prefix ("37"), the hop
// display uses the stored resolved_path pubkey and shows the correct target name.
func TestHandleNodePaths_HopName_CanonicalPathShowsTarget_1144(t *testing.T) {
	db := setupTestDB(t)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()

	targetPK := "37cf0832aaaabbbb"  // no GPS
	siblingPK := "37bb000011112222" // has GPS — biased resolver picks this without fix

	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'CJS SF Mission', 'repeater', 0, 0, ?, '2026-01-01', 1)`, targetPK, recent)
	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'Templeton Hills', 'repeater', 35.5, -120.7, ?, '2026-01-01', 1)`, siblingPK, recent)

	// TX: resolved_path = [targetPK] → canonical path (Option A) → lookupNode(targetPK)
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, 'AA', 'hash1144', ?)`, recent)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (1, NULL, '["37"]', ?, ?)`, recentEpoch, `["`+targetPK+`"]`)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/"+targetPK+"/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /paths: code=%d body=%s", w.Code, w.Body.String())
	}
	var resp NodePathsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Paths) != 1 {
		t.Fatalf("expected 1 path, got %d", len(resp.Paths))
	}
	if len(resp.Paths[0].Hops) != 1 {
		t.Fatalf("expected 1 hop, got %d", len(resp.Paths[0].Hops))
	}
	hop := resp.Paths[0].Hops[0]
	// The "37" prefix resolves to TWO candidates; the canonical path must use
	// the stored resolved_path pubkey (targetPK) and display the target's name,
	// NOT the GPS-having sibling.
	if hop.Name != "CJS SF Mission" {
		if hop.Name == "Templeton Hills" {
			t.Errorf("hop name = %q (sibling mis-resolution #1144): canonical path must show target name %q", hop.Name, "CJS SF Mission")
		} else {
			t.Errorf("hop name = %q, want %q", hop.Name, "CJS SF Mission")
		}
	}
	if hop.Pubkey != targetPK {
		t.Errorf("hop pubkey = %q, want %q", hop.Pubkey, targetPK)
	}
}
