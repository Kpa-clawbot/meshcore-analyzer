package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// ─── resolveWithContext unit tests ─────────────────────────────────────────────

func TestResolveWithContext_UniquePrefix(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a1b2c3d4", Name: "Node-A", HasGPS: true, Lat: 1, Lon: 2},
	})
	ni, confidence, _ := pm.resolveWithContext("a1b2c3d4", nil, nil)
	if ni == nil || ni.Name != "Node-A" {
		t.Fatal("expected Node-A")
	}
	if confidence != "unique_prefix" {
		t.Fatalf("expected unique_prefix, got %s", confidence)
	}
}

func TestResolveWithContext_NoMatch(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a1b2c3d4", Name: "Node-A"},
	})
	ni, confidence, _ := pm.resolveWithContext("ff", nil, nil)
	if ni != nil {
		t.Fatal("expected nil")
	}
	if confidence != "no_match" {
		t.Fatalf("expected no_match, got %s", confidence)
	}
}

func TestResolveWithContext_AffinityWins(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a1aaaaaa", Name: "Node-A1"},
		{PublicKey: "a1bbbbbb", Name: "Node-A2"},
	})

	graph := NewNeighborGraph()
	for i := 0; i < 100; i++ {
		graph.upsertEdge("c0c0c0c0", "a1aaaaaa", "a1", "obs1", nil, time.Now())
	}

	ni, confidence, score := pm.resolveWithContext("a1", []string{"c0c0c0c0"}, graph)
	if ni == nil || ni.Name != "Node-A1" {
		t.Fatalf("expected Node-A1, got %v", ni)
	}
	if confidence != "neighbor_affinity" {
		t.Fatalf("expected neighbor_affinity, got %s", confidence)
	}
	if score <= 0 {
		t.Fatalf("expected positive score, got %f", score)
	}
}

func TestResolveWithContext_AffinityTooClose_FallsToGeo(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a1aaaaaa", Name: "Node-A1", HasGPS: true, Lat: 10, Lon: 20},
		{PublicKey: "a1bbbbbb", Name: "Node-A2", HasGPS: true, Lat: 11, Lon: 21},
		{PublicKey: "c0c0c0c0", Name: "Ctx", HasGPS: true, Lat: 10.1, Lon: 20.1},
	})

	graph := NewNeighborGraph()
	for i := 0; i < 50; i++ {
		graph.upsertEdge("c0c0c0c0", "a1aaaaaa", "a1", "obs1", nil, time.Now())
		graph.upsertEdge("c0c0c0c0", "a1bbbbbb", "a1", "obs1", nil, time.Now())
	}

	ni, confidence, _ := pm.resolveWithContext("a1", []string{"c0c0c0c0"}, graph)
	if ni == nil {
		t.Fatal("expected a result")
	}
	if confidence != "geo_proximity" {
		t.Fatalf("expected geo_proximity, got %s", confidence)
	}
	if ni.Name != "Node-A1" {
		t.Fatalf("expected Node-A1 (closer to context), got %s", ni.Name)
	}
}

func TestResolveWithContext_GPSPreference(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a1aaaaaa", Name: "NoGPS"},
		{PublicKey: "a1bbbbbb", Name: "HasGPS", HasGPS: true, Lat: 1, Lon: 2},
	})

	ni, confidence, _ := pm.resolveWithContext("a1", nil, nil)
	if ni == nil || ni.Name != "HasGPS" {
		t.Fatalf("expected HasGPS, got %v", ni)
	}
	if confidence != "gps_preference" {
		t.Fatalf("expected gps_preference, got %s", confidence)
	}
}

func TestResolveWithContext_FirstMatchFallback(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a1aaaaaa", Name: "First"},
		{PublicKey: "a1bbbbbb", Name: "Second"},
	})

	ni, confidence, _ := pm.resolveWithContext("a1", nil, nil)
	if ni == nil || ni.Name != "First" {
		t.Fatalf("expected First, got %v", ni)
	}
	if confidence != "first_match" {
		t.Fatalf("expected first_match, got %s", confidence)
	}
}

func TestResolveWithContext_NilGraphFallsToGPS(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a1aaaaaa", Name: "NoGPS"},
		{PublicKey: "a1bbbbbb", Name: "HasGPS", HasGPS: true, Lat: 1, Lon: 2},
	})

	ni, confidence, _ := pm.resolveWithContext("a1", []string{"someone"}, nil)
	if ni == nil || ni.Name != "HasGPS" {
		t.Fatalf("expected HasGPS, got %v", ni)
	}
	if confidence != "gps_preference" {
		t.Fatalf("expected gps_preference, got %s", confidence)
	}
}

func TestResolveWithContext_BackwardCompatResolve(t *testing.T) {
	// Verify original resolve() still works unchanged
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a1aaaaaa", Name: "NoGPS"},
		{PublicKey: "a1bbbbbb", Name: "HasGPS", HasGPS: true, Lat: 1, Lon: 2},
	})
	ni := pm.resolve("a1")
	if ni == nil || ni.Name != "HasGPS" {
		t.Fatalf("expected HasGPS from resolve(), got %v", ni)
	}
}

// ─── geoDistApprox ─────────────────────────────────────────────────────────────

func TestGeoDistApprox_SamePoint(t *testing.T) {
	d := geoDistApprox(37.0, -122.0, 37.0, -122.0)
	if d != 0 {
		t.Fatalf("expected 0, got %f", d)
	}
}

func TestGeoDistApprox_Ordering(t *testing.T) {
	d1 := geoDistApprox(37.0, -122.0, 37.01, -122.01)
	d2 := geoDistApprox(37.0, -122.0, 38.0, -121.0)
	if d1 >= d2 {
		t.Fatal("closer point should have smaller distance")
	}
}

// ─── handleResolveHops enhanced response (API tests) ───────────────────────────

func TestResolveHopsAPI_UniquePrefix(t *testing.T) {
	srv, router := setupTestServer(t)
	_ = srv

	// Insert a unique node
	srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon) VALUES (?, ?, ?, ?)",
		"ff11223344", "UniqueNode", 37.0, -122.0)

	req := httptest.NewRequest("GET", "/api/resolve-hops?hops=ff11223344", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	var result ResolveHopsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}

	hr, ok := result.Resolved["ff11223344"]
	if !ok {
		t.Fatal("expected hop in resolved map")
	}
	if hr.Confidence != "unique_prefix" {
		t.Fatalf("expected unique_prefix, got %s", hr.Confidence)
	}
}

func TestResolveHopsAPI_AmbiguousNoContext(t *testing.T) {
	srv, router := setupTestServer(t)

	srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon) VALUES (?, ?, ?, ?)",
		"ee1aaaaaaa", "Node-E1", 37.0, -122.0)
	srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon) VALUES (?, ?, ?, ?)",
		"ee1bbbbbbb", "Node-E2", 38.0, -121.0)

	req := httptest.NewRequest("GET", "/api/resolve-hops?hops=ee1", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	var result ResolveHopsResponse
	json.Unmarshal(rr.Body.Bytes(), &result)

	hr := result.Resolved["ee1"]
	if hr == nil {
		t.Fatal("expected hop in resolved map")
	}
	if hr.Confidence != "ambiguous" {
		t.Fatalf("expected ambiguous, got %s", hr.Confidence)
	}
	if len(hr.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(hr.Candidates))
	}
	for _, c := range hr.Candidates {
		if c.AffinityScore != nil {
			t.Fatal("expected nil affinity score without context")
		}
	}
}

func TestResolveHopsAPI_WithAffinityContext(t *testing.T) {
	srv, router := setupTestServer(t)

	srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon) VALUES (?, ?, ?, ?)",
		"dd1aaaaaaa", "Node-D1", 37.0, -122.0)
	srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon) VALUES (?, ?, ?, ?)",
		"dd1bbbbbbb", "Node-D2", 38.0, -121.0)
	srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon) VALUES (?, ?, ?, ?)",
		"c0c0c0c0c0", "Context", 37.1, -122.1)

	// Invalidate node cache so the PM includes newly inserted nodes.
	srv.store.cacheMu.Lock()
	srv.store.nodeCacheTime = time.Time{}
	srv.store.cacheMu.Unlock()

	// Build graph with strong affinity
	graph := NewNeighborGraph()
	for i := 0; i < 100; i++ {
		graph.upsertEdge("c0c0c0c0c0", "dd1aaaaaaa", "dd1", "obs1", nil, time.Now())
	}
	graph.builtAt = time.Now()
	srv.neighborMu.Lock()
	srv.neighborGraph = graph
	srv.neighborMu.Unlock()

	req := httptest.NewRequest("GET", "/api/resolve-hops?hops=dd1&from_node=c0c0c0c0c0", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	var result ResolveHopsResponse
	json.Unmarshal(rr.Body.Bytes(), &result)

	hr := result.Resolved["dd1"]
	if hr == nil {
		t.Fatal("expected hop in resolved map")
	}
	if hr.Confidence != "neighbor_affinity" {
		t.Fatalf("expected neighbor_affinity, got %s", hr.Confidence)
	}
	if hr.BestCandidate == nil || *hr.BestCandidate != "dd1aaaaaaa" {
		t.Fatalf("expected bestCandidate dd1aaaaaaa, got %v", hr.BestCandidate)
	}

	// Verify affinity scores present
	hasScore := false
	for _, c := range hr.Candidates {
		if c.AffinityScore != nil && *c.AffinityScore > 0 {
			hasScore = true
		}
	}
	if !hasScore {
		t.Fatal("expected at least one candidate with affinity score")
	}
}

func TestResolveHopsAPI_ResponseShape(t *testing.T) {
	srv, router := setupTestServer(t)

	srv.db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, lat, lon) VALUES (?, ?, ?, ?)",
		"bb1aaaaaaa", "Node-B1", 37.0, -122.0)

	req := httptest.NewRequest("GET", "/api/resolve-hops?hops=bb1a", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	var raw map[string]json.RawMessage
	json.Unmarshal(rr.Body.Bytes(), &raw)

	if _, ok := raw["resolved"]; !ok {
		t.Fatal("missing 'resolved' key")
	}

	var resolved map[string]map[string]interface{}
	json.Unmarshal(raw["resolved"], &resolved)

	for _, hr := range resolved {
		if _, ok := hr["confidence"]; !ok {
			t.Error("missing 'confidence' field in HopResolution")
		}
		if _, ok := hr["candidates"]; !ok {
			t.Error("missing 'candidates' field")
		}
	}
}

// ─── Helpers used only in this test file ───────────────────────────────────────
