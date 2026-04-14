package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func setupTestServer(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return srv, router
}

func setupTestServerWithAPIKey(t *testing.T, apiKey string) (*Server, *mux.Router) {
	t.Helper()
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000, APIKey: apiKey}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return srv, router
}

func TestWriteEndpointsRequireAPIKey(t *testing.T) {
	_, router := setupTestServerWithAPIKey(t, "test-secret-key-strong-enough")

	t.Run("missing key returns 401", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/perf/reset", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
		var body map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body["error"] != "unauthorized" {
			t.Fatalf("expected unauthorized error, got %v", body["error"])
		}
	})

	t.Run("wrong key returns 401", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/perf/reset", nil)
		req.Header.Set("X-API-Key", "wrong-secret-key-strong-enough")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	})

	t.Run("correct key passes", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/perf/reset", nil)
		req.Header.Set("X-API-Key", "test-secret-key-strong-enough")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
		}
	})

	t.Run("decode works without key", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/decode", bytes.NewBufferString(`{"hex":"0200"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for decode without key, got %d (body: %s)", w.Code, w.Body.String())
		}
	})
}

func TestWriteEndpointsBlockWhenAPIKeyEmpty(t *testing.T) {
	_, router := setupTestServerWithAPIKey(t, "")

	req := httptest.NewRequest("POST", "/api/perf/reset", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with empty apiKey, got %d (body: %s)", w.Code, w.Body.String())
	}

	// decode should still work even with empty apiKey
	req2 := httptest.NewRequest("POST", "/api/decode", bytes.NewBufferString(`{"hex":"0200"}`))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 for decode with empty apiKey, got %d (body: %s)", w2.Code, w2.Body.String())
	}
}

func TestHealthEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
	if body["engine"] != "go" {
		t.Errorf("expected engine go, got %v", body["engine"])
	}
	if _, ok := body["version"]; !ok {
		t.Error("expected version field in health response")
	}
	if _, ok := body["commit"]; !ok {
		t.Error("expected commit field in health response")
	}
	if bt, ok := body["buildTime"]; !ok || bt == nil {
		t.Error("expected non-nil buildTime field in health response")
	}

	// Verify memory has spec-defined fields (no heapMB or goRuntime per api-spec.md)
	mem, ok := body["memory"].(map[string]interface{})
	if !ok {
		t.Fatal("expected memory object in health response")
	}
	for _, field := range []string{"rss", "heapUsed", "heapTotal", "external"} {
		if _, ok := mem[field]; !ok {
			t.Errorf("expected %s in memory", field)
		}
	}
	if _, ok := mem["heapMB"]; ok {
		t.Error("heapMB should not be in memory (removed per api-spec.md)")
	}
	if _, ok := body["goRuntime"]; ok {
		t.Error("goRuntime should not be in health response (removed per api-spec.md)")
	}

	// Verify real packetStore stats (not zeros)
	pktStore, ok := body["packetStore"].(map[string]interface{})
	if !ok {
		t.Fatal("expected packetStore object in health response")
	}
	if _, ok := pktStore["packets"]; !ok {
		t.Error("expected packets in packetStore")
	}
	if _, ok := pktStore["estimatedMB"]; !ok {
		t.Error("expected estimatedMB in packetStore")
	}
	if _, ok := pktStore["trackedMB"]; !ok {
		t.Error("expected trackedMB in packetStore")
	}

	// Verify eventLoop (GC pause metrics matching Node.js shape)
	el, ok := body["eventLoop"].(map[string]interface{})
	if !ok {
		t.Fatal("expected eventLoop object in health response")
	}
	for _, field := range []string{"currentLagMs", "maxLagMs", "p50Ms", "p95Ms", "p99Ms"} {
		if _, ok := el[field]; !ok {
			t.Errorf("expected %s in eventLoop", field)
		}
	}

	// Verify cache has real structure
	cache, ok := body["cache"].(map[string]interface{})
	if !ok {
		t.Fatal("expected cache object in health response")
	}
	if _, ok := cache["entries"]; !ok {
		t.Error("expected entries in cache")
	}
	if _, ok := cache["hitRate"]; !ok {
		t.Error("expected hitRate in cache")
	}
}

func TestStatsEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["totalTransmissions"] != float64(3) {
		t.Errorf("expected 3 transmissions, got %v", body["totalTransmissions"])
	}
	if body["totalNodes"] != float64(3) {
		t.Errorf("expected 3 nodes, got %v", body["totalNodes"])
	}
	if body["engine"] != "go" {
		t.Errorf("expected engine go, got %v", body["engine"])
	}
	if _, ok := body["version"]; !ok {
		t.Error("expected version field in stats response")
	}
	if _, ok := body["commit"]; !ok {
		t.Error("expected commit field in stats response")
	}
	if bt, ok := body["buildTime"]; !ok || bt == nil {
		t.Error("expected non-nil buildTime field in stats response")
	}
}

func TestPacketsEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	packets, ok := body["packets"].([]interface{})
	if !ok {
		t.Fatal("expected packets array")
	}
	if len(packets) != 3 {
		t.Errorf("expected 3 packets (transmissions), got %d", len(packets))
	}
}

func TestPacketsGrouped(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?groupByHash=true&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	packets, ok := body["packets"].([]interface{})
	if !ok {
		t.Fatal("expected packets array")
	}
	if len(packets) != 3 {
		t.Errorf("expected 3 grouped packets, got %d", len(packets))
	}
}

func TestNodesEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes?limit=50", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	nodes, ok := body["nodes"].([]interface{})
	if !ok {
		t.Fatal("expected nodes array")
	}
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(nodes))
	}
	total := body["total"].(float64)
	if total != 3 {
		t.Errorf("expected total 3, got %v", total)
	}
}

func TestNodeDetailEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	node, ok := body["node"].(map[string]interface{})
	if !ok {
		t.Fatal("expected node object")
	}
	if node["name"] != "TestRepeater" {
		t.Errorf("expected TestRepeater, got %v", node["name"])
	}
}

func TestNodeDetail404(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestNodeSearchEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/search?q=Repeater", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	nodes, ok := body["nodes"].([]interface{})
	if !ok {
		t.Fatal("expected nodes array")
	}
	if len(nodes) != 1 {
		t.Errorf("expected 1 node matching 'Repeater', got %d", len(nodes))
	}
}

func TestNetworkStatusEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/network-status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["total"] != float64(3) {
		t.Errorf("expected 3 total, got %v", body["total"])
	}
}

func TestObserversEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	observers, ok := body["observers"].([]interface{})
	if !ok {
		t.Fatal("expected observers array")
	}
	if len(observers) != 2 {
		t.Errorf("expected 2 observers, got %d", len(observers))
	}
}

func TestObserverDetail404(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/observers/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestChannelsEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	channels, ok := body["channels"].([]interface{})
	if !ok {
		t.Fatal("expected channels array")
	}
	if len(channels) != 1 {
		t.Errorf("expected 1 channel, got %d", len(channels))
	}
}

func TestTracesEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/traces/abc123def4567890", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	traces, ok := body["traces"].([]interface{})
	if !ok {
		t.Fatal("expected traces array")
	}
	if len(traces) != 2 {
		t.Errorf("expected 2 traces, got %d", len(traces))
	}
}

func TestConfigCacheEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/config/cache", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestConfigThemeEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/config/theme", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["branding"] == nil {
		t.Error("expected branding in theme response")
	}
}

func TestConfigMapEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/config/map", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["zoom"] == nil {
		t.Error("expected zoom in map response")
	}
}

func TestPerfEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	// Make a request first to generate perf data
	req1 := httptest.NewRequest("GET", "/api/health", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	req := httptest.NewRequest("GET", "/api/perf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)

	// Verify goRuntime IS present with expected fields
	goRuntime, ok := body["goRuntime"].(map[string]interface{})
	if !ok {
		t.Fatal("expected goRuntime object in perf response")
	}
	for _, field := range []string{"goroutines", "numGC", "pauseTotalMs", "lastPauseMs", "heapAllocMB", "heapSysMB", "heapInuseMB", "heapIdleMB", "numCPU"} {
		if _, ok := goRuntime[field]; !ok {
			t.Errorf("expected %s in goRuntime", field)
		}
	}
	// Verify status, uptimeHuman, websocket are NOT present
	for _, removed := range []string{"status", "uptimeHuman", "websocket"} {
		if _, ok := body[removed]; ok {
			t.Errorf("%s should not be in perf response (removed per api-spec.md)", removed)
		}
	}

	// Verify cache stats (real, not hardcoded zeros)
	cache, ok := body["cache"].(map[string]interface{})
	if !ok {
		t.Fatal("expected cache object in perf response")
	}
	for _, field := range []string{"size", "hits", "misses", "hitRate"} {
		if _, ok := cache[field]; !ok {
			t.Errorf("expected %s in cache", field)
		}
	}

	// Verify packetStore stats
	if _, ok := body["packetStore"]; !ok {
		t.Error("expected packetStore in perf response")
	}

	// Verify sqlite stats
	sqliteStats, ok := body["sqlite"].(map[string]interface{})
	if !ok {
		t.Fatal("expected sqlite object in perf response")
	}
	if _, ok := sqliteStats["dbSizeMB"]; !ok {
		t.Error("expected dbSizeMB in sqlite")
	}
	if _, ok := sqliteStats["rows"]; !ok {
		t.Error("expected rows in sqlite")
	}

	// Verify standard fields still present
	if _, ok := body["uptime"]; !ok {
		t.Error("expected uptime in perf response")
	}
	if _, ok := body["endpoints"]; !ok {
		t.Error("expected endpoints in perf response")
	}
}

func TestAnalyticsRFEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/rf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestResolveHopsEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/resolve-hops?hops=aabb,eeff", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	resolved, ok := body["resolved"].(map[string]interface{})
	if !ok {
		t.Fatal("expected resolved map")
	}
	// aabb should resolve to TestRepeater
	aabb, ok := resolved["aabb"].(map[string]interface{})
	if !ok {
		t.Fatal("expected aabb in resolved")
	}
	if aabb["name"] != "TestRepeater" {
		t.Errorf("expected TestRepeater for aabb, got %v", aabb["name"])
	}
}

func TestPacketTimestampsRequiresSince(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets/timestamps", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestContentTypeJSON(t *testing.T) {
	_, router := setupTestServer(t)
	endpoints := []string{
		"/api/health", "/api/stats", "/api/nodes", "/api/packets",
		"/api/observers", "/api/channels",
	}
	for _, ep := range endpoints {
		req := httptest.NewRequest("GET", ep, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		ct := w.Header().Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("%s: expected application/json, got %s", ep, ct)
		}
	}
}

func TestAllEndpointsReturn200(t *testing.T) {
	_, router := setupTestServer(t)
	endpoints := []struct {
		path   string
		status int
	}{
		{"/api/health", http.StatusOK},
		{"/api/stats", http.StatusOK},
		{"/api/perf", http.StatusOK},
		{"/api/config/cache", http.StatusOK},
		{"/api/config/client", http.StatusOK},
		{"/api/config/regions", http.StatusOK},
		{"/api/config/theme", http.StatusOK},
		{"/api/config/map", http.StatusOK},
		{"/api/packets?limit=5", http.StatusOK},
		{"/api/nodes?limit=5", http.StatusOK},
		{"/api/nodes/search?q=test", http.StatusOK},
		{"/api/nodes/bulk-health", http.StatusOK},
		{"/api/nodes/network-status", http.StatusOK},
		{"/api/observers", http.StatusOK},
		{"/api/channels", http.StatusOK},
		{"/api/analytics/rf", http.StatusOK},
		{"/api/analytics/topology", http.StatusOK},
		{"/api/analytics/channels", http.StatusOK},
		{"/api/analytics/distance", http.StatusOK},
		{"/api/analytics/hash-sizes", http.StatusOK},
		{"/api/analytics/subpaths", http.StatusOK},
		{"/api/analytics/subpath-detail?hops=aa,bb", http.StatusOK},
		{"/api/resolve-hops?hops=aabb", http.StatusOK},
		{"/api/iata-coords", http.StatusOK},
		{"/api/traces/abc123def4567890", http.StatusOK},
	}
	for _, tc := range endpoints {
		req := httptest.NewRequest("GET", tc.path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Errorf("%s: expected %d, got %d (body: %s)", tc.path, tc.status, w.Code, w.Body.String()[:min(200, w.Body.Len())])
		}
	}
}

func TestPacketDetailByHash(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets/abc123def4567890", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	pkt, ok := body["packet"].(map[string]interface{})
	if !ok {
		t.Fatal("expected packet object")
	}
	if pkt["hash"] != "abc123def4567890" {
		t.Errorf("expected hash abc123def4567890, got %v", pkt["hash"])
	}
	if body["observation_count"] == nil {
		t.Error("expected observation_count")
	}
}

func TestPacketDetailByNumericID(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets/1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["packet"] == nil {
		t.Error("expected packet object")
	}
}

func TestPacketDetailNotFound(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets/notahash12345678", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// "notahash12345678" is 16 hex chars, will try hash lookup first, then fail
	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestPacketDetailNumericNotFound(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets/99999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestPacketTimestampsWithSince(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets/timestamps?since=2020-01-01", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestNodeDetailWithRecentAdverts(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["recentAdverts"] == nil {
		t.Error("expected recentAdverts in response")
	}
	node, ok := body["node"].(map[string]interface{})
	if !ok {
		t.Fatal("expected node object")
	}
	if node["name"] != "TestRepeater" {
		t.Errorf("expected TestRepeater, got %v", node["name"])
	}
}

func TestNodeHealthFound(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["node"] == nil {
		t.Error("expected node in response")
	}
	if body["stats"] == nil {
		t.Error("expected stats in response")
	}
}

func TestNodeHealthNotFound(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/nonexistent/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestNodeHealthPartialFromPackets verifies that a node with packets in the
// in-memory store but no DB entry returns a partial 200 response instead of 404.
// This is the fix for issue #665 (companion nodes without adverts).
func TestNodeHealthPartialFromPackets(t *testing.T) {
	srv, router := setupTestServer(t)

	// Inject a packet into byNode for a pubkey that doesn't exist in the nodes table
	ghostPubkey := "ghost_companion_no_advert"
	now := time.Now().UTC().Format(time.RFC3339)
	snr := 5.0
	srv.store.mu.Lock()
	if srv.store.byNode == nil {
		srv.store.byNode = make(map[string][]*StoreTx)
	}
	if srv.store.nodeHashes == nil {
		srv.store.nodeHashes = make(map[string]map[string]bool)
	}
	srv.store.byNode[ghostPubkey] = []*StoreTx{
		{Hash: "abc123", FirstSeen: now, SNR: &snr, ObservationCount: 1},
	}
	srv.store.nodeHashes[ghostPubkey] = map[string]bool{"abc123": true}
	srv.store.mu.Unlock()

	req := httptest.NewRequest("GET", "/api/nodes/"+ghostPubkey+"/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200 for ghost companion, got %d (body: %s)", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	// Should have a synthetic node stub
	node, ok := body["node"].(map[string]interface{})
	if !ok || node == nil {
		t.Fatal("expected node in response")
	}
	if node["role"] != "unknown" {
		t.Errorf("expected role=unknown, got %v", node["role"])
	}
	if node["public_key"] != ghostPubkey {
		t.Errorf("expected public_key=%s, got %v", ghostPubkey, node["public_key"])
	}

	// Should have stats from the packet
	stats, ok := body["stats"].(map[string]interface{})
	if !ok || stats == nil {
		t.Fatal("expected stats in response")
	}
	if stats["totalPackets"] != 1.0 { // JSON numbers are float64
		t.Errorf("expected totalPackets=1, got %v", stats["totalPackets"])
	}
	if stats["lastHeard"] == nil {
		t.Error("expected lastHeard to be set")
	}
}

func TestBulkHealthEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/bulk-health?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body []interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if len(body) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(body))
	}
}

func TestBulkHealthLimitCap(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/bulk-health?limit=999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestNodePathsFound(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["node"] == nil {
		t.Error("expected node in response")
	}
	if body["paths"] == nil {
		t.Error("expected paths in response")
	}
	if got, ok := body["totalTransmissions"].(float64); !ok || got < 1 {
		t.Errorf("expected totalTransmissions >= 1, got %v", body["totalTransmissions"])
	}
}

func TestNodePathsNotFound(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/nonexistent/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestNodeAnalytics(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("default days", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/analytics", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
		}
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["timeRange"] == nil {
			t.Error("expected timeRange")
		}
		if body["activityTimeline"] == nil {
			t.Error("expected activityTimeline")
		}
	})

	t.Run("custom days", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/analytics?days=30", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("clamp days below 1", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/analytics?days=0", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("clamp days above 365", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/analytics?days=999", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/nonexistent/analytics", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 404 {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})
}

func TestObserverDetailFound(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/observers/obs1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["id"] != "obs1" {
		t.Errorf("expected obs1, got %v", body["id"])
	}
}

func TestObserverAnalytics(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("default", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/observers/obs1/analytics", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["packetTypes"] == nil {
			t.Error("expected packetTypes")
		}
		if body["recentPackets"] == nil {
			t.Error("expected recentPackets")
		}
		if recent, ok := body["recentPackets"].([]interface{}); !ok || len(recent) == 0 {
			t.Errorf("expected non-empty recentPackets, got %v", body["recentPackets"])
		}
	})

	t.Run("custom days", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/observers/obs1/analytics?days=1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("days greater than 7", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/observers/obs1/analytics?days=30", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

func TestChannelMessages(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/channels/%23test/messages", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["messages"] == nil {
		t.Error("expected messages")
	}
}

func TestChannelMessagesWithRegion(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('EEFF', 'chanextra000001', ?, 1, 5, '{"type":"CHAN","channel":"#test","text":"OtherUser: Cross region","sender":"OtherUser"}')`,
		time.Now().UTC().Add(-30*time.Minute).Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (4, 2, 11.0, -89, '[]', ?)`, time.Now().UTC().Add(-30*time.Minute).Unix())

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/channels/%23test/messages?region=SJC", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	msgs, ok := body["messages"].([]interface{})
	if !ok {
		t.Fatalf("expected messages array, got %T", body["messages"])
	}
	if len(msgs) == 0 {
		t.Fatalf("expected at least one regional message")
	}
	for _, raw := range msgs {
		msg, _ := raw.(map[string]interface{})
		if msg["sender"] == "OtherUser" {
			t.Fatalf("cross-region message should be excluded")
		}
	}
}

func TestAnalyticsRFWithRegion(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/rf?region=SJC", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["snr"] == nil {
		t.Error("expected snr in response")
	}
	if body["payloadTypes"] == nil {
		t.Error("expected payloadTypes")
	}
}

func TestAnalyticsTopology(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/topology", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["uniqueNodes"] == nil {
		t.Error("expected uniqueNodes")
	}
}

func TestAnalyticsChannels(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["channels"] == nil {
		t.Error("expected channels")
	}
	if body["activeChannels"] == nil {
		t.Error("expected activeChannels")
	}
}

func TestAnalyticsDistance(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/distance", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestAnalyticsHashSizes(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/hash-sizes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestAnalyticsSubpaths(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/subpaths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestAnalyticsSubpathsBulk(t *testing.T) {
	_, router := setupTestServer(t)

	// Valid request with multiple groups.
	req := httptest.NewRequest("GET", "/api/analytics/subpaths-bulk?groups=2-2:50,3-3:30,5-8:15", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	results, ok := body["results"].([]interface{})
	if !ok {
		t.Fatal("expected results array")
	}
	if len(results) != 3 {
		t.Errorf("expected 3 result groups, got %d", len(results))
	}
	// Each result should have subpaths and totalPaths.
	for i, r := range results {
		rm, ok := r.(map[string]interface{})
		if !ok {
			t.Fatalf("result %d not a map", i)
		}
		if _, ok := rm["subpaths"]; !ok {
			t.Errorf("result %d missing subpaths", i)
		}
		if _, ok := rm["totalPaths"]; !ok {
			t.Errorf("result %d missing totalPaths", i)
		}
	}

	// Missing groups param → error.
	req2 := httptest.NewRequest("GET", "/api/analytics/subpaths-bulk", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("expected 200 with error body, got %d", w2.Code)
	}
	var errBody map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &errBody)
	if _, ok := errBody["error"]; !ok {
		t.Error("expected error field for missing groups param")
	}

	// Invalid group format.
	req3 := httptest.NewRequest("GET", "/api/analytics/subpaths-bulk?groups=bad", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)
	var errBody3 map[string]interface{}
	json.Unmarshal(w3.Body.Bytes(), &errBody3)
	if _, ok := errBody3["error"]; !ok {
		t.Error("expected error for invalid group format")
	}
}

func TestAnalyticsSubpathDetailWithHops(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/subpath-detail?hops=aa,bb", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	hops, ok := body["hops"].([]interface{})
	if !ok {
		t.Fatal("expected hops array")
	}
	if len(hops) != 2 {
		t.Errorf("expected 2 hops, got %d", len(hops))
	}
}

func TestAnalyticsSubpathDetailNoHops(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/subpath-detail", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] == nil {
		t.Error("expected error message when no hops provided")
	}
}

func TestResolveHopsEmpty(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/resolve-hops", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	resolved, ok := body["resolved"].(map[string]interface{})
	if !ok {
		t.Fatal("expected resolved map")
	}
	if len(resolved) != 0 {
		t.Error("expected empty resolved map for no hops")
	}
}

func TestResolveHopsAmbiguous(t *testing.T) {
	// Set up server with nodes that share a prefix
	db := setupTestDB(t)
	seedTestData(t, db)
	// Add another node with same "aabb" prefix
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES ('aabb000000000000', 'AnotherNode', 'repeater')`)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/resolve-hops?hops=aabb", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	resolved := body["resolved"].(map[string]interface{})
	aabb := resolved["aabb"].(map[string]interface{})
	if aabb["ambiguous"] != true {
		t.Error("expected ambiguous=true when multiple candidates")
	}
	candidates, ok := aabb["candidates"].([]interface{})
	if !ok {
		t.Fatal("expected candidates array")
	}
	if len(candidates) < 2 {
		t.Errorf("expected at least 2 candidates, got %d", len(candidates))
	}
}

func TestResolveHopsNoMatch(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/resolve-hops?hops=zzzz", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	resolved := body["resolved"].(map[string]interface{})
	zzzz := resolved["zzzz"].(map[string]interface{})
	if zzzz["name"] != nil {
		t.Error("expected nil name for unresolved hop")
	}
}

func TestAudioLabBuckets(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/audio-lab/buckets", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["buckets"] == nil {
		t.Error("expected buckets")
	}
}

func TestIATACoords(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/iata-coords", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["coords"] == nil {
		t.Error("expected coords")
	}
}

func TestPerfMiddlewareRecording(t *testing.T) {
	_, router := setupTestServer(t)

	// Make several requests to generate perf data
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/api/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	// Check perf endpoint
	req := httptest.NewRequest("GET", "/api/perf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	totalReqs := body["totalRequests"].(float64)
	// At least 5 health requests + 1 perf request (but perf is also counted)
	if totalReqs < 5 {
		t.Errorf("expected at least 5 total requests, got %v", totalReqs)
	}
}

func TestPerfMiddlewareNonAPI(t *testing.T) {
	// Non-API paths should not be recorded
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/some/non/api/path", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// No panic, no error — middleware just passes through
}

func TestPacketsWithOrderAsc(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10&order=asc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPacketsWithTypeAndRouteFilter(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10&type=4&route=1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPacketsWithExpandObservations(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10&expand=observations", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestConfigClientEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/config/client", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["propagationBufferMs"] == nil {
		t.Error("expected propagationBufferMs")
	}
	tsRaw, ok := body["timestamps"].(map[string]interface{})
	if !ok {
		t.Fatal("expected timestamps object")
	}
	if tsRaw["defaultMode"] != "ago" {
		t.Errorf("expected timestamps.defaultMode=ago, got %v", tsRaw["defaultMode"])
	}
	if tsRaw["timezone"] != "local" {
		t.Errorf("expected timestamps.timezone=local, got %v", tsRaw["timezone"])
	}
	if tsRaw["formatPreset"] != "iso" {
		t.Errorf("expected timestamps.formatPreset=iso, got %v", tsRaw["formatPreset"])
	}
}

func TestConfigRegionsEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/config/regions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	// Should have at least the IATA codes from seed data
	if body["SJC"] == nil {
		t.Error("expected SJC region")
	}
	if body["SFO"] == nil {
		t.Error("expected SFO region")
	}
}

func TestNodeSearchEmpty(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/search?q=", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	nodes := body["nodes"].([]interface{})
	if len(nodes) != 0 {
		t.Error("expected empty nodes for empty search")
	}
}

func TestNodeSearchWhitespace(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/search?q=%20%20", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	nodes := body["nodes"].([]interface{})
	if len(nodes) != 0 {
		t.Error("expected empty nodes for whitespace search")
	}
}

func TestNodeAnalyticsNoNameNode(t *testing.T) {
	// Test with a node that has no name to cover the name="" branch
	db := setupTestDB(t)
	seedTestData(t, db)
	// Insert a node without a name
	db.conn.Exec(`INSERT INTO nodes (public_key, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('deadbeef12345678', NULL, 37.5, -122.0, '2026-01-15T10:00:00Z', '2026-01-01T00:00:00Z', 5)`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DDEE', 'deadbeefhash1234', '2026-01-15T10:05:00Z', 1, 4,
		'{"pubKey":"deadbeef12345678","type":"ADVERT"}')`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 11.0, -91, '["dd"]', 1736935500)`)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/deadbeef12345678/analytics?days=30", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["node"] == nil {
		t.Error("expected node in response")
	}
}

func TestNodeHealthForNoNameNode(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	db.conn.Exec(`INSERT INTO nodes (public_key, role, last_seen, first_seen, advert_count)
		VALUES ('deadbeef12345678', 'repeater', '2026-01-15T10:00:00Z', '2026-01-01T00:00:00Z', 5)`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DDEE', 'deadbeefhash1234', '2026-01-15T10:05:00Z', 1, 4,
		'{"pubKey":"deadbeef12345678","type":"ADVERT"}')`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 11.0, -91, '["dd"]', 1736935500)`)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/deadbeef12345678/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestPacketsWithNodeFilter(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10&node=TestRepeater", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPacketsWithRegionFilter(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10&region=SJC", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPacketsWithHashFilter(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10&hash=abc123def4567890", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPacketsWithObserverFilter(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10&observer=obs1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPacketsWithSinceUntil(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10&since=2020-01-01&until=2099-01-01", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestNodesWithRoleFilter(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes?role=repeater&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	total := body["total"].(float64)
	if total != 1 {
		t.Errorf("expected 1 repeater, got %v", total)
	}
}

func TestNodesWithSortAndSearch(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes?search=Test&sortBy=name&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestGroupedPacketsWithFilters(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?groupByHash=true&limit=10&type=4", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestConfigThemeWithCustomConfig(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{
		Port: 3000,
		Branding: map[string]interface{}{
			"siteName": "CustomSite",
		},
		Theme: map[string]interface{}{
			"accent": "#ff0000",
		},
		Home: map[string]interface{}{
			"title": "Welcome",
		},
	}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/config/theme", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	branding := body["branding"].(map[string]interface{})
	if branding["siteName"] != "CustomSite" {
		t.Errorf("expected CustomSite, got %v", branding["siteName"])
	}
	if body["home"] == nil {
		t.Error("expected home in response")
	}
}

func TestConfigThemeHomeDefaults(t *testing.T) {
	// When no home config is set, server should return built-in defaults
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000} // no Home set
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/config/theme", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	home, ok := body["home"].(map[string]interface{})
	if !ok || home == nil {
		t.Fatal("expected non-null home object in theme response")
	}
	if home["heroTitle"] != "CoreScope" {
		t.Errorf("expected heroTitle=CoreScope, got %v", home["heroTitle"])
	}
	if home["heroSubtitle"] == nil {
		t.Error("expected heroSubtitle in home defaults")
	}
	steps, ok := home["steps"].([]interface{})
	if !ok || len(steps) == 0 {
		t.Error("expected non-empty steps array in home defaults")
	}
	footerLinks, ok := home["footerLinks"].([]interface{})
	if !ok || len(footerLinks) == 0 {
		t.Error("expected non-empty footerLinks array in home defaults")
	}
}

func TestConfigCacheWithCustomTTL(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{
		Port: 3000,
		CacheTTL: map[string]interface{}{
			"nodes": 60000,
		},
	}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/config/cache", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["nodes"] != float64(60000) {
		t.Errorf("expected 60000, got %v", body["nodes"])
	}
}

func TestConfigRegionsWithCustomRegions(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{
		Port: 3000,
		Regions: map[string]string{
			"LAX": "Los Angeles",
		},
	}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/config/regions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["LAX"] != "Los Angeles" {
		t.Errorf("expected 'Los Angeles', got %v", body["LAX"])
	}
	// DB-sourced IATA codes should also appear
	if body["SJC"] == nil {
		t.Error("expected SJC from DB")
	}
}

func TestConfigMapWithCustomDefaults(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	cfg.MapDefaults.Center = []float64{40.0, -74.0}
	cfg.MapDefaults.Zoom = 12
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/config/map", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["zoom"] != float64(12) {
		t.Errorf("expected zoom 12, got %v", body["zoom"])
	}
}

func TestHandlerErrorPaths(t *testing.T) {
	// Create a DB that will error on queries by dropping the view/tables
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)


	t.Run("stats error", func(t *testing.T) {
		db.conn.Exec("DROP TABLE IF EXISTS transmissions")
		req := httptest.NewRequest("GET", "/api/stats", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 500 {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})
}

func TestHandlerErrorChannels(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	db.conn.Exec("DROP TABLE IF EXISTS transmissions")

	req := httptest.NewRequest("GET", "/api/channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for channels error, got %d", w.Code)
	}
}

func TestHandlerErrorTraces(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	db.conn.Exec("DROP TABLE IF EXISTS observations")

	req := httptest.NewRequest("GET", "/api/traces/abc123def4567890", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for traces error, got %d", w.Code)
	}
}

func TestHandlerErrorObservers(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	db.conn.Exec("DROP TABLE IF EXISTS observers")

	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for observers error, got %d", w.Code)
	}
}

func TestHandlerErrorNodes(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	db.conn.Exec("DROP TABLE IF EXISTS nodes")

	req := httptest.NewRequest("GET", "/api/nodes?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for nodes error, got %d", w.Code)
	}
}

func TestHandlerErrorNetworkStatus(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	db.conn.Exec("DROP TABLE IF EXISTS nodes")

	req := httptest.NewRequest("GET", "/api/nodes/network-status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for network-status error, got %d", w.Code)
	}
}

func TestHandlerErrorPackets(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// Drop transmissions table to trigger error in transmission-centric query
	db.conn.Exec("DROP TABLE IF EXISTS transmissions")

	req := httptest.NewRequest("GET", "/api/packets?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for packets error, got %d", w.Code)
	}
}

func TestHandlerErrorPacketsGrouped(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	db.conn.Exec("DROP TABLE IF EXISTS observations")

	req := httptest.NewRequest("GET", "/api/packets?limit=10&groupByHash=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for grouped packets error, got %d", w.Code)
	}
}

func TestHandlerErrorNodeSearch(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	db.conn.Exec("DROP TABLE IF EXISTS nodes")

	req := httptest.NewRequest("GET", "/api/nodes/search?q=test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for node search error, got %d", w.Code)
	}
}

func TestHandlerErrorTimestamps(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// Without a store, timestamps returns empty 200
	req := httptest.NewRequest("GET", "/api/packets/timestamps?since=2020-01-01", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 for timestamps without store, got %d", w.Code)
	}
}

func TestHandlerErrorChannelMessages(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	db.conn.Exec("DROP TABLE IF EXISTS observations")

	req := httptest.NewRequest("GET", "/api/channels/%23test/messages", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for channel messages error, got %d", w.Code)
	}
}

func TestHandlerErrorBulkHealth(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	db.conn.Exec("DROP TABLE IF EXISTS nodes")

	req := httptest.NewRequest("GET", "/api/nodes/bulk-health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}


func TestAnalyticsChannelsNoNullArrays(t *testing.T) {
_, router := setupTestServer(t)
req := httptest.NewRequest("GET", "/api/analytics/channels", nil)
w := httptest.NewRecorder()
router.ServeHTTP(w, req)

if w.Code != 200 {
t.Fatalf("expected 200, got %d", w.Code)
}

raw := w.Body.String()
var body map[string]interface{}
if err := json.Unmarshal([]byte(raw), &body); err != nil {
t.Fatalf("invalid JSON: %v", err)
}

arrayFields := []string{"channels", "topSenders", "channelTimeline", "msgLengths"}
for _, field := range arrayFields {
val, exists := body[field]
if !exists {
t.Errorf("missing field %q", field)
continue
}
if val == nil {
t.Errorf("field %q is null, expected empty array []", field)
continue
}
if _, ok := val.([]interface{}); !ok {
t.Errorf("field %q is not an array, got %T", field, val)
}
}
}

func TestAnalyticsChannelsNoStoreFallbackNoNulls(t *testing.T) {
db := setupTestDB(t)
seedTestData(t, db)
cfg := &Config{Port: 3000}
hub := NewHub()
srv := NewServer(db, cfg, hub)
router := mux.NewRouter()
srv.RegisterRoutes(router)

req := httptest.NewRequest("GET", "/api/analytics/channels", nil)
w := httptest.NewRecorder()
router.ServeHTTP(w, req)

if w.Code != 200 {
t.Fatalf("expected 200, got %d", w.Code)
}

var body map[string]interface{}
json.Unmarshal(w.Body.Bytes(), &body)

arrayFields := []string{"channels", "topSenders", "channelTimeline", "msgLengths"}
for _, field := range arrayFields {
if body[field] == nil {
t.Errorf("field %q is null in DB fallback, expected []", field)
}
}
}

func TestNodeHashSizeEnrichment(t *testing.T) {
t.Run("nil info leaves defaults", func(t *testing.T) {
node := map[string]interface{}{
"public_key":             "abc123",
"hash_size":              nil,
"hash_size_inconsistent": false,
}
EnrichNodeWithHashSize(node, nil)
if node["hash_size"] != nil {
t.Error("expected hash_size to remain nil with nil info")
}
})

t.Run("enriches with computed data", func(t *testing.T) {
node := map[string]interface{}{
"public_key":             "abc123",
"hash_size":              nil,
"hash_size_inconsistent": false,
}
info := &hashSizeNodeInfo{
HashSize:     2,
AllSizes:     map[int]bool{1: true, 2: true},
Seq:          []int{1, 2, 1, 2},
Inconsistent: true,
}
EnrichNodeWithHashSize(node, info)
if node["hash_size"] != 2 {
t.Errorf("expected hash_size 2, got %v", node["hash_size"])
}
if node["hash_size_inconsistent"] != true {
t.Error("expected hash_size_inconsistent true")
}
sizes, ok := node["hash_sizes_seen"].([]int)
if !ok {
t.Fatal("expected hash_sizes_seen to be []int")
}
if len(sizes) != 2 || sizes[0] != 1 || sizes[1] != 2 {
t.Errorf("expected [1,2], got %v", sizes)
}
})

t.Run("single size omits sizes_seen", func(t *testing.T) {
node := map[string]interface{}{
"public_key":             "abc123",
"hash_size":              nil,
"hash_size_inconsistent": false,
}
info := &hashSizeNodeInfo{
HashSize: 3,
AllSizes: map[int]bool{3: true},
Seq:      []int{3, 3, 3},
}
EnrichNodeWithHashSize(node, info)
if node["hash_size"] != 3 {
t.Errorf("expected hash_size 3, got %v", node["hash_size"])
}
if node["hash_size_inconsistent"] != false {
t.Error("expected hash_size_inconsistent false")
}
if _, exists := node["hash_sizes_seen"]; exists {
t.Error("hash_sizes_seen should not be set for single size")
}
})
}

func TestGetNodeHashSizeInfoFlipFlop(t *testing.T) {
db := setupTestDB(t)
seedTestData(t, db)
store := NewPacketStore(db, nil)
if err := store.Load(); err != nil {
	t.Fatalf("store.Load failed: %v", err)
}

pk := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'TestNode', 'repeater')", pk)

decoded := `{"name":"TestNode","pubKey":"` + pk + `"}`
raw1 := "04" + "00" + "aabb"
raw2 := "04" + "40" + "aabb"

payloadType := 4
for i := 0; i < 3; i++ {
rawHex := raw1
if i%2 == 1 {
rawHex = raw2
}
tx := &StoreTx{
ID:          9000 + i,
RawHex:      rawHex,
Hash:        "testhash" + strconv.Itoa(i),
FirstSeen:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
PayloadType: &payloadType,
DecodedJSON: decoded,
}
store.packets = append(store.packets, tx)
store.byPayloadType[4] = append(store.byPayloadType[4], tx)
}

info := store.GetNodeHashSizeInfo()
ni := info[pk]
if ni == nil {
t.Fatal("expected hash info for test node")
}
if len(ni.AllSizes) != 2 {
t.Errorf("expected 2 unique sizes, got %d", len(ni.AllSizes))
}
if !ni.Inconsistent {
t.Error("expected inconsistent flag to be true for flip-flop pattern")
}
}

func TestGetNodeHashSizeInfoDominant(t *testing.T) {
// A node with mostly 2-byte adverts and an occasional 1-byte advert; the
// latest advert (2-byte) determines the reported hash size.
db := setupTestDB(t)
seedTestData(t, db)
store := NewPacketStore(db, nil)
if err := store.Load(); err != nil {
	t.Fatalf("store.Load failed: %v", err)
}

pk := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'Repeater2B', 'repeater')", pk)

decoded := `{"name":"Repeater2B","pubKey":"` + pk + `"}`
raw1byte := "04" + "00" + "aabb" // pathByte=0x00 → hashSize=1 (direct send, no hops)
raw2byte := "04" + "40" + "aabb" // pathByte=0x40 → hashSize=2

payloadType := 4
// 1 packet with hashSize=1, 4 packets with hashSize=2 (latest is 2-byte)
raws := []string{raw1byte, raw2byte, raw2byte, raw2byte, raw2byte}
for i, raw := range raws {
	tx := &StoreTx{
		ID:          8000 + i,
		RawHex:      raw,
		Hash:        "dominant" + strconv.Itoa(i),
		FirstSeen:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		PayloadType: &payloadType,
		DecodedJSON: decoded,
	}
	store.packets = append(store.packets, tx)
	store.byPayloadType[4] = append(store.byPayloadType[4], tx)
}

info := store.GetNodeHashSizeInfo()
ni := info[pk]
if ni == nil {
	t.Fatal("expected hash info for test node")
}
if ni.HashSize != 2 {
	t.Errorf("HashSize=%d, want 2 (latest advert should determine hash size)", ni.HashSize)
}
}

func TestGetNodeHashSizeInfoLatestWins(t *testing.T) {
	// A node reconfigured from 1-byte to 2-byte hash should show 2-byte
	// even when it has many more historical 1-byte adverts (issue #303).
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	pk := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'LatestWins', 'repeater')", pk)

	decoded := `{"name":"LatestWins","pubKey":"` + pk + `"}`
	raw1byte := "04" + "00" + "aabb" // pathByte=0x00 → hashSize=1
	raw2byte := "04" + "40" + "aabb" // pathByte=0x40 → hashSize=2

	payloadType := 4
	// 4 historical 1-byte adverts, then 1 recent 2-byte advert (latest).
	// Mode would pick 1 (majority), but latest-wins should pick 2.
	raws := []string{raw1byte, raw1byte, raw1byte, raw1byte, raw2byte}
	baseTime := time.Now().UTC().Add(-1 * time.Hour)
	for i, raw := range raws {
		tx := &StoreTx{
			ID:          7000 + i,
			RawHex:      raw,
			Hash:        "latest" + strconv.Itoa(i),
			FirstSeen:   baseTime.Add(time.Duration(i) * time.Minute).Format("2006-01-02T15:04:05.000Z"),
			PayloadType: &payloadType,
			DecodedJSON: decoded,
		}
		store.packets = append(store.packets, tx)
		store.byPayloadType[4] = append(store.byPayloadType[4], tx)
	}

	info := store.GetNodeHashSizeInfo()
	ni := info[pk]
	if ni == nil {
		t.Fatal("expected hash info for test node")
	}
	if ni.HashSize != 2 {
		t.Errorf("HashSize=%d, want 2 (latest advert should win over historical mode)", ni.HashSize)
	}
	if len(ni.AllSizes) != 2 {
		t.Errorf("AllSizes count=%d, want 2", len(ni.AllSizes))
	}
	if !ni.AllSizes[1] || !ni.AllSizes[2] {
		t.Error("AllSizes should contain both 1 and 2")
	}
}

func TestGetNodeHashSizeInfoIgnoreDirectZeroHop(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	pk := "dddd111122223333444455556666777788889999aaaabbbbccccddddeeee3333"
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'DirIgnore', 'repeater')", pk)

	decoded := `{"name":"DirIgnore","pubKey":"` + pk + `"}`
	rawFlood2B := "11" + "40" + "aabb" // FLOOD advert, hashSize=2
	rawDirect0 := "12" + "00" + "aabb" // DIRECT advert, zero-hop (should be ignored)

	payloadType := 4
	raws := []string{rawFlood2B, rawDirect0, rawFlood2B, rawDirect0, rawFlood2B}
	baseTime2 := time.Now().UTC().Add(-1 * time.Hour)
	for i, raw := range raws {
		tx := &StoreTx{
			ID:          9150 + i,
			RawHex:      raw,
			Hash:        "dirignore" + strconv.Itoa(i),
			FirstSeen:   baseTime2.Add(time.Duration(i) * time.Minute).Format("2006-01-02T15:04:05.000Z"),
			PayloadType: &payloadType,
			DecodedJSON: decoded,
		}
		store.packets = append(store.packets, tx)
		store.byPayloadType[4] = append(store.byPayloadType[4], tx)
	}

	info := store.GetNodeHashSizeInfo()
	ni := info[pk]
	if ni == nil {
		t.Fatal("expected hash info for test node")
	}
	if ni.HashSize != 2 {
		t.Errorf("HashSize=%d, want 2 (direct zero-hop adverts should be ignored)", ni.HashSize)
	}
	if ni.Inconsistent {
		t.Error("expected hash_size_inconsistent=false when direct zero-hop adverts are ignored")
	}
	if len(ni.AllSizes) != 1 || !ni.AllSizes[2] {
		t.Errorf("expected only 2-byte size in AllSizes, got %#v", ni.AllSizes)
	}
}

func TestGetNodeHashSizeInfoOnlyDirectZeroHopIgnored(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	pk := "eeee111122223333444455556666777788889999aaaabbbbccccddddeeee4444"
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'OnlyDirect', 'repeater')", pk)

	decoded := `{"name":"OnlyDirect","pubKey":"` + pk + `"}`
	rawDirect0 := "12" + "00" + "aabb"
	payloadType := 4

	tx := &StoreTx{
		ID:          9160,
		RawHex:      rawDirect0,
		Hash:        "onlydirect0",
		FirstSeen:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		PayloadType: &payloadType,
		DecodedJSON: decoded,
	}
	store.packets = append(store.packets, tx)
	store.byPayloadType[4] = append(store.byPayloadType[4], tx)

	info := store.GetNodeHashSizeInfo()
	if ni := info[pk]; ni != nil {
		t.Errorf("expected nil hash info for direct zero-hop only node, got HashSize=%d", ni.HashSize)
	}
}

func TestGetNodeHashSizeInfoDirectNonZeroHopCounted(t *testing.T) {
	// A DIRECT advert with non-zero hop count should NOT be skipped —
	// only zero-hop DIRECT adverts misreport hash size.
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	pk := "ffff111122223333444455556666777788889999aaaabbbbccccddddeeee5555"
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'DirNonZero', 'repeater')", pk)

	decoded := `{"name":"DirNonZero","pubKey":"` + pk + `"}`
	// DIRECT advert (route type 2 = 0x02 in bits 0-1), path byte 0x41:
	//   upper 2 bits = 01 → hash_size = 2, lower 6 bits = 0x01 → hop count 1 (non-zero)
	rawDirectNonZero := "12" + "41" + "aabb" // header=0x12 (ADVERT|DIRECT), path=0x41
	payloadType := 4

	tx := &StoreTx{
		ID:          9170,
		RawHex:      rawDirectNonZero,
		Hash:        "dirnonzero0",
		FirstSeen:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		PayloadType: &payloadType,
		DecodedJSON: decoded,
	}
	store.packets = append(store.packets, tx)
	store.byPayloadType[4] = append(store.byPayloadType[4], tx)

	info := store.GetNodeHashSizeInfo()
	ni := info[pk]
	if ni == nil {
		t.Fatal("expected hash info for DIRECT non-zero-hop node — it should NOT be skipped")
	}
	if ni.HashSize != 2 {
		t.Errorf("HashSize=%d, want 2 (DIRECT with hop count > 0 should be counted)", ni.HashSize)
	}
}

func TestGetNodeHashSizeInfoNoAdverts(t *testing.T) {
	// A node with no ADVERT packets should not appear in hash size info.
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	pk := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'NoAdverts', 'repeater')", pk)

	// Add a non-advert packet (payload_type=2 = TXT_MSG)
	payloadType := 2
	tx := &StoreTx{
		ID:          6000,
		RawHex:      "0440aabb",
		Hash:        "noadverts0",
		FirstSeen:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		PayloadType: &payloadType,
		DecodedJSON: `{"pubKey":"` + pk + `"}`,
	}
	store.packets = append(store.packets, tx)
	store.byPayloadType[2] = append(store.byPayloadType[2], tx)

	info := store.GetNodeHashSizeInfo()
	if ni := info[pk]; ni != nil {
		t.Errorf("expected nil hash info for node with no adverts, got HashSize=%d", ni.HashSize)
	}
}

func TestHashAnalyticsZeroHopAdvert(t *testing.T) {
	// A zero-hop advert (pathByte=0x00, no relay path) should contribute to
	// distributionByRepeaters (per-node tracking) but NOT inflate total or
	// distribution (which only count relayed packets).
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	// Capture baseline from seed data (bypass cache via computeAnalyticsHashSizes)
	baseline := store.computeAnalyticsHashSizes("")
	baseTotal, _ := baseline["total"].(int)
	baseDist, _ := baseline["distribution"].(map[string]int)
	baseDist1 := baseDist["1"]

	pk := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'ZeroHop', 'repeater')", pk)
	store.InvalidateNodeCache()

	decoded := `{"name":"ZeroHop","pubKey":"` + pk + `"}`
	// header 0x05 → routeType=1 (FLOOD), pathByte=0x00 → hashSize=1
	raw := "05" + "00" + "aabb"
	payloadType := 4

	tx := &StoreTx{
		ID:          8000,
		RawHex:      raw,
		Hash:        "zerohop0",
		FirstSeen:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		PayloadType: &payloadType,
		DecodedJSON: decoded,
		// No PathJSON → txGetParsedPath returns nil (zero hops)
	}
	store.packets = append(store.packets, tx)
	store.byPayloadType[4] = append(store.byPayloadType[4], tx)

	result := store.computeAnalyticsHashSizes("")

	// distributionByRepeaters should include the zero-hop advert's node
	distByRepeaters, ok := result["distributionByRepeaters"].(map[string]int)
	if !ok {
		t.Fatal("distributionByRepeaters missing or wrong type")
	}
	if distByRepeaters["1"] < 1 {
		t.Errorf("distributionByRepeaters[\"1\"]=%d, want >=1 (zero-hop advert should be tracked per-node)", distByRepeaters["1"])
	}

	// total and distribution must NOT have increased from the baseline
	total, _ := result["total"].(int)
	dist, _ := result["distribution"].(map[string]int)
	if total != baseTotal {
		t.Errorf("total=%d, want %d (zero-hop adverts must not inflate total)", total, baseTotal)
	}
	if dist["1"] != baseDist1 {
		t.Errorf("distribution[\"1\"]=%d, want %d (zero-hop adverts must not inflate distribution)", dist["1"], baseDist1)
	}
}

func TestAnalyticsHashSizeSameNameDifferentPubkey(t *testing.T) {
	// Two nodes named "SameName" with different pubkeys should be counted
	// separately in distributionByRepeaters (issue #303, byNode keying fix).
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	pk1 := "aaaa111122223333444455556666777788889999aaaabbbbccccddddeeee1111"
	pk2 := "aaaa111122223333444455556666777788889999aaaabbbbccccddddeeee2222"

	// Insert both nodes as repeaters so they appear in distributionByRepeaters.
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'SameName', 'repeater')", pk1)
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'SameName', 'repeater')", pk2)
	store.InvalidateNodeCache()

	decoded1 := `{"name":"SameName","pubKey":"` + pk1 + `"}`
	decoded2 := `{"name":"SameName","pubKey":"` + pk2 + `"}`

	raw2byte := "05" + "40" + "aabb" // header routeType=1 (FLOOD), pathByte=0x40 → hashSize=2
	payloadType := 4

	for i, decoded := range []string{decoded1, decoded2} {
		tx := &StoreTx{
			ID:          6100 + i,
			RawHex:      raw2byte,
			Hash:        "samename" + strconv.Itoa(i),
			FirstSeen:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			PayloadType: &payloadType,
			DecodedJSON: decoded,
			PathJSON:    `["AABB"]`,
		}
		store.packets = append(store.packets, tx)
		store.byPayloadType[4] = append(store.byPayloadType[4], tx)
	}

	result := store.GetAnalyticsHashSizes("")

	distByRepeaters, ok := result["distributionByRepeaters"].(map[string]int)
	if !ok {
		t.Fatal("distributionByRepeaters missing or wrong type")
	}
	if distByRepeaters["2"] < 2 {
		t.Errorf("distributionByRepeaters[\"2\"]=%d, want >=2 (same-name nodes with different pubkeys should be counted separately)", distByRepeaters["2"])
	}
}

func TestAnalyticsHashSizesNoNullArrays(t *testing.T) {
_, router := setupTestServer(t)
req := httptest.NewRequest("GET", "/api/analytics/hash-sizes", nil)
w := httptest.NewRecorder()
router.ServeHTTP(w, req)

if w.Code != 200 {
t.Fatalf("expected 200, got %d", w.Code)
}

var body map[string]interface{}
json.Unmarshal(w.Body.Bytes(), &body)

arrayFields := []string{"hourly", "topHops", "multiByteNodes"}
for _, field := range arrayFields {
if body[field] == nil {
t.Errorf("field %q is null, expected []", field)
}
	}
}
func TestInconsistentNodesExcludesCompanions(t *testing.T) {
	// Issue #566: inconsistentNodes should only include repeaters and room servers.
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	payloadType := 4

	// Create three nodes: repeater, room_server, companion — all with inconsistent hash sizes
	nodes := []struct {
		pk   string
		role string
	}{
		{"aa11111111111111111111111111111111111111111111111111111111111111", "repeater"},
		{"bb22222222222222222222222222222222222222222222222222222222222222", "room_server"},
		{"cc33333333333333333333333333333333333333333333333333333333333333", "companion"},
	}

	for ni, n := range nodes {
		db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, ?, ?)", n.pk, "Node-"+n.role, n.role)
		decoded := `{"name":"Node-` + n.role + `","pubKey":"` + n.pk + `"}`
		// Create flip-flop pattern: 1-byte, 2-byte, 1-byte (transitions=2 → inconsistent)
		// Use header 0x11 (routeType=FLOOD, payloadType=4) and pathByte 0x41/0x81
		// (non-zero hop count) so packets aren't skipped by direct zero-hop filter.
		raws := []string{"11" + "41" + "aabb", "11" + "81" + "aabb", "11" + "41" + "aabb"}
		for i, raw := range raws {
			tx := &StoreTx{
				ID:          7000 + ni*10 + i,
				RawHex:      raw,
				Hash:        "incon-" + n.role + strconv.Itoa(i),
				FirstSeen:   now,
				PayloadType: &payloadType,
				DecodedJSON: decoded,
			}
			store.packets = append(store.packets, tx)
			store.byPayloadType[4] = append(store.byPayloadType[4], tx)
		}
	}

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)

	incon := body["inconsistent_nodes"].([]interface{})
	for _, item := range incon {
		node := item.(map[string]interface{})
		role := node["role"].(string)
		if role == "companion" {
			t.Error("companion node should be excluded from inconsistent_nodes")
		}
	}

	// Repeater and room_server should be present
	roles := make(map[string]bool)
	for _, item := range incon {
		node := item.(map[string]interface{})
		roles[node["role"].(string)] = true
	}
	if !roles["repeater"] {
		t.Error("expected repeater in inconsistent_nodes")
	}
	if !roles["room_server"] {
		t.Error("expected room_server in inconsistent_nodes")
	}
}

func TestHashSizeInfoTimeWindow(t *testing.T) {
	// Issue #566: adverts older than 7 days should be excluded from hash size computation.
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	pk := "dd44444444444444444444444444444444444444444444444444444444444444"
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'OldNode', 'repeater')", pk)

	decoded := `{"name":"OldNode","pubKey":"` + pk + `"}`
	payloadType := 4

	// Old adverts (>7 days ago) with flip-flop pattern
	// Use header 0x11 (routeType=FLOOD) and pathByte 0x41/0x81 (non-zero hop count)
	// so packets aren't skipped by direct zero-hop filter.
	oldTime := time.Now().UTC().Add(-10 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	oldRaws := []string{"11" + "41" + "aabb", "11" + "81" + "aabb", "11" + "41" + "aabb"}
	for i, raw := range oldRaws {
		tx := &StoreTx{
			ID:          6000 + i,
			RawHex:      raw,
			Hash:        "old-" + strconv.Itoa(i),
			FirstSeen:   oldTime,
			PayloadType: &payloadType,
			DecodedJSON: decoded,
		}
		store.packets = append(store.packets, tx)
		store.byPayloadType[4] = append(store.byPayloadType[4], tx)
	}

	info := store.GetNodeHashSizeInfo()
	ni := info[pk]
	if ni != nil && ni.Inconsistent {
		t.Error("old adverts (>7 days) should be excluded; node should not be flagged as inconsistent")
	}

	// Now add recent adverts with consistent hash size — should appear in info
	pk2 := "ee55555555555555555555555555555555555555555555555555555555555555"
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role) VALUES (?, 'NewNode', 'repeater')", pk2)
	decoded2 := `{"name":"NewNode","pubKey":"` + pk2 + `"}`
	recentTime := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	for i := 0; i < 3; i++ {
		tx := &StoreTx{
			ID:          6100 + i,
			RawHex:      "11" + "41" + "aabb",
			Hash:        "new-" + strconv.Itoa(i),
			FirstSeen:   recentTime,
			PayloadType: &payloadType,
			DecodedJSON: decoded2,
		}
		store.packets = append(store.packets, tx)
		store.byPayloadType[4] = append(store.byPayloadType[4], tx)
	}

	// Invalidate cache before second call
	store.hashSizeInfoMu.Lock()
	store.hashSizeInfoCache = nil
	store.hashSizeInfoMu.Unlock()

	info2 := store.GetNodeHashSizeInfo()
	ni2 := info2[pk2]
	if ni2 == nil {
		t.Error("recent adverts should be included in hash size info")
	}
}

func TestObserverAnalyticsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/observers/obs1/analytics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 503 {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
func TestConfigGeoFilterEndpoint(t *testing.T) {
	t.Run("no geo filter configured", func(t *testing.T) {
		_, router := setupTestServer(t)
		req := httptest.NewRequest("GET", "/api/config/geo-filter", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["polygon"] != nil {
			t.Errorf("expected polygon to be nil when no geo filter configured, got %v", body["polygon"])
		}
	})

	t.Run("with polygon configured", func(t *testing.T) {
		db := setupTestDB(t)
		seedTestData(t, db)
		lat0, lat1 := 50.0, 51.5
		lon0, lon1 := 3.0, 5.5
		cfg := &Config{
			Port: 3000,
			GeoFilter: &GeoFilterConfig{
				Polygon:  [][2]float64{{lat0, lon0}, {lat1, lon0}, {lat1, lon1}, {lat0, lon1}},
				BufferKm: 20,
			},
		}
		hub := NewHub()
		srv := NewServer(db, cfg, hub)
		srv.store = NewPacketStore(db, nil)
		srv.store.Load()
		router := mux.NewRouter()
		srv.RegisterRoutes(router)

		req := httptest.NewRequest("GET", "/api/config/geo-filter", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["polygon"] == nil {
			t.Error("expected polygon in response when geo filter is configured")
		}
		if body["bufferKm"] == nil {
			t.Error("expected bufferKm in response")
		}
		if _, ok := body["writeEnabled"]; !ok {
			t.Error("expected writeEnabled field in response")
		}
		// No apiKey configured → writeEnabled should be false
		if body["writeEnabled"] != false {
			t.Errorf("expected writeEnabled=false when no apiKey, got %v", body["writeEnabled"])
		}
	})

	t.Run("writeEnabled true when strong apiKey configured", func(t *testing.T) {
		db := setupTestDB(t)
		cfg := &Config{Port: 3000, APIKey: "a-strong-api-key-1234"}
		hub := NewHub()
		srv := NewServer(db, cfg, hub)
		srv.store = NewPacketStore(db, nil)
		srv.store.Load()
		router := mux.NewRouter()
		srv.RegisterRoutes(router)

		req := httptest.NewRequest("GET", "/api/config/geo-filter", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["writeEnabled"] != true {
			t.Errorf("expected writeEnabled=true when strong apiKey configured, got %v", body["writeEnabled"])
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestLatestSeenMaintained verifies that StoreTx.LatestSeen is populated after Load()
// and is >= FirstSeen for packets that have observations.
func TestLatestSeenMaintained(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	if len(store.packets) == 0 {
		t.Fatal("expected packets in store after Load")
	}

	for _, tx := range store.packets {
		if tx.LatestSeen == "" {
			t.Errorf("packet %s has empty LatestSeen (FirstSeen=%s)", tx.Hash, tx.FirstSeen)
			continue
		}
		// LatestSeen must be >= FirstSeen (string comparison works for RFC3339/ISO8601)
		if tx.LatestSeen < tx.FirstSeen {
			t.Errorf("packet %s: LatestSeen %q < FirstSeen %q", tx.Hash, tx.LatestSeen, tx.FirstSeen)
		}
		// For packets with observations, LatestSeen must be >= all observation timestamps.
		for _, obs := range tx.Observations {
			if obs.Timestamp != "" && obs.Timestamp > tx.LatestSeen {
				t.Errorf("packet %s: obs.Timestamp %q > LatestSeen %q", tx.Hash, obs.Timestamp, tx.LatestSeen)
			}
		}
	}
}

// TestQueryGroupedPacketsSortedByLatest verifies that QueryGroupedPackets returns packets
// sorted by LatestSeen DESC — i.e. the packet whose most-recent observation is newest
// comes first, even if its first_seen is older.
func TestQueryGroupedPacketsSortedByLatest(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	// oldFirst: first_seen is old, but observation is very recent.
	oldFirst := now.Add(-48 * time.Hour).Format(time.RFC3339)
	// newFirst: first_seen is recent, but observation is old.
	newFirst := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-5 * time.Minute).Unix()
	oldEpoch := now.Add(-72 * time.Hour).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('sortobs', 'Sort Observer', 'TST', ?, '2026-01-01T00:00:00Z', 1)`, now.Format(time.RFC3339))

	// Packet A: old first_seen, but a very recent observation — should sort first.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AA01', 'sort_old_first_recent_obs', ?, 1, 2, '{"type":"TXT_MSG","text":"old first"}')`, oldFirst)
	var idA int64
	db.conn.QueryRow(`SELECT id FROM transmissions WHERE hash='sort_old_first_recent_obs'`).Scan(&idA)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 10.0, -90, '[]', ?)`, idA, recentEpoch)

	// Packet B: newer first_seen, but an old observation — should sort second.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('BB02', 'sort_new_first_old_obs', ?, 1, 2, '{"type":"TXT_MSG","text":"new first"}')`, newFirst)
	var idB int64
	db.conn.QueryRow(`SELECT id FROM transmissions WHERE hash='sort_new_first_old_obs'`).Scan(&idB)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 10.0, -90, '[]', ?)`, idB, oldEpoch)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	result := store.QueryGroupedPackets(PacketQuery{Limit: 50})
	if result.Total < 2 {
		t.Fatalf("expected at least 2 packets, got %d", result.Total)
	}

	// Find the two test packets in the result (may be mixed with other entries).
	firstHash := ""
	secondHash := ""
	for _, p := range result.Packets {
		h, _ := p["hash"].(string)
		if h == "sort_old_first_recent_obs" || h == "sort_new_first_old_obs" {
			if firstHash == "" {
				firstHash = h
			} else {
				secondHash = h
				break
			}
		}
	}

	if firstHash != "sort_old_first_recent_obs" {
		t.Errorf("expected sort_old_first_recent_obs to appear before sort_new_first_old_obs in sorted results; got first=%q second=%q", firstHash, secondHash)
	}
}

// TestQueryGroupedPacketsCacheReturnsConsistentResult verifies that two rapid successive
// calls to QueryGroupedPackets return the same total count and first packet hash.
func TestQueryGroupedPacketsCacheReturnsConsistentResult(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	q := PacketQuery{Limit: 50}
	r1 := store.QueryGroupedPackets(q)
	r2 := store.QueryGroupedPackets(q)

	if r1.Total != r2.Total {
		t.Errorf("cache inconsistency: first call total=%d, second call total=%d", r1.Total, r2.Total)
	}
	if r1.Total == 0 {
		t.Fatal("expected non-zero results from QueryGroupedPackets")
	}
	h1, _ := r1.Packets[0]["hash"].(string)
	h2, _ := r2.Packets[0]["hash"].(string)
	if h1 != h2 {
		t.Errorf("cache inconsistency: first call first hash=%q, second call first hash=%q", h1, h2)
	}
}

// TestGetChannelsCacheReturnsConsistentResult verifies that two rapid successive calls
// to GetChannels return the same number of channels with the same names.
func TestGetChannelsCacheReturnsConsistentResult(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	r1 := store.GetChannels("")
	r2 := store.GetChannels("")

	if len(r1) != len(r2) {
		t.Errorf("cache inconsistency: first call len=%d, second call len=%d", len(r1), len(r2))
	}
	if len(r1) == 0 {
		t.Fatal("expected at least one channel from seedTestData")
	}

	names1 := make(map[string]bool)
	for _, ch := range r1 {
		if n, ok := ch["name"].(string); ok {
			names1[n] = true
		}
	}
	for _, ch := range r2 {
		if n, ok := ch["name"].(string); ok {
			if !names1[n] {
				t.Errorf("cache inconsistency: channel %q in second result but not first", n)
			}
		}
	}
}

// TestGetChannelsNotBlockedByLargeLock verifies that GetChannels returns correct channel
// data (count and messageCount) after observations have been added — i.e. the lock-copy
// pattern works correctly and the JSON unmarshal outside the lock produces valid results.
func TestGetChannelsNotBlockedByLargeLock(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	channels := store.GetChannels("")

	// seedTestData inserts one GRP_TXT (payload_type=5) packet with channel "#test".
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}

	ch := channels[0]
	name, ok := ch["name"].(string)
	if !ok || name != "#test" {
		t.Errorf("expected channel name '#test', got %v", ch["name"])
	}

	// messageCount should be 1 (one CHAN packet for #test).
	msgCount, ok := ch["messageCount"].(int)
	if !ok {
		// JSON numbers may unmarshal as float64 — but GetChannels returns native Go values.
		t.Errorf("expected messageCount to be int, got %T (%v)", ch["messageCount"], ch["messageCount"])
	} else if msgCount != 1 {
		t.Errorf("expected messageCount=1, got %d", msgCount)
	}
}

// --- Tests for computeHashCollisions (Issue #416) ---

func TestAnalyticsHashCollisionsEndpoint(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Must have top-level keys
	if _, ok := body["inconsistent_nodes"]; !ok {
		t.Error("missing inconsistent_nodes key")
	}
	if _, ok := body["by_size"]; !ok {
		t.Error("missing by_size key")
	}

	bySize, ok := body["by_size"].(map[string]interface{})
	if !ok {
		t.Fatal("by_size is not a map")
	}
	// Must have entries for 1, 2, 3 byte sizes
	for _, sz := range []string{"1", "2", "3"} {
		sizeData, ok := bySize[sz].(map[string]interface{})
		if !ok {
			t.Errorf("by_size[%s] is not a map", sz)
			continue
		}
		stats, ok := sizeData["stats"].(map[string]interface{})
		if !ok {
			t.Errorf("by_size[%s].stats is not a map", sz)
			continue
		}
		if _, ok := stats["total_nodes"]; !ok {
			t.Errorf("by_size[%s].stats missing total_nodes", sz)
		}
		if _, ok := stats["collision_count"]; !ok {
			t.Errorf("by_size[%s].stats missing collision_count", sz)
		}
		// collisions must be an array, not null
		collisions, ok := sizeData["collisions"].([]interface{})
		if !ok {
			t.Errorf("by_size[%s].collisions is not an array", sz)
		}
		_ = collisions
	}
}

func TestHashCollisionsNoNullArrays(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// JSON must not contain "null" for arrays
	bodyStr := w.Body.String()
	if bodyStr == "" {
		t.Fatal("empty response body")
	}
	// inconsistent_nodes should be [] not null
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["inconsistent_nodes"] == nil {
		t.Error("inconsistent_nodes is null, should be empty array")
	}
}

func TestHashCollisionsRegionParam(t *testing.T) {
	// Issue #438: region param should be accepted and used for filtering.
	// With no region observers configured, results should be identical to global.
	_, router := setupTestServer(t)

	// Request without region
	req1 := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	if w1.Code != 200 {
		t.Fatalf("expected 200, got %d", w1.Code)
	}

	// Request with region param (no observers for this region, so falls back to global)
	req2 := httptest.NewRequest("GET", "/api/analytics/hash-collisions?region=us-west", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("expected 200, got %d", w2.Code)
	}

	// With no region observers configured, both should return identical results
	if w1.Body.String() != w2.Body.String() {
		t.Error("responses differ with/without region param when no region observers configured")
	}
}

func TestHashCollisionsOneByteCells(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	bySize := body["by_size"].(map[string]interface{})
	oneByteData := bySize["1"].(map[string]interface{})

	// 1-byte data should include one_byte_cells for matrix rendering
	cells, ok := oneByteData["one_byte_cells"].(map[string]interface{})
	if !ok {
		t.Fatal("1-byte data missing one_byte_cells")
	}
	// Should have 256 entries (00-FF)
	if len(cells) != 256 {
		t.Errorf("expected 256 one_byte_cells entries, got %d", len(cells))
	}
}

func TestHashCollisionsTwoByteCells(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	bySize := body["by_size"].(map[string]interface{})
	twoByteData := bySize["2"].(map[string]interface{})

	// 2-byte data should include two_byte_cells for matrix rendering
	cells, ok := twoByteData["two_byte_cells"].(map[string]interface{})
	if !ok {
		t.Fatal("2-byte data missing two_byte_cells")
	}
	// Should have 256 entries (00-FF first-byte groups)
	if len(cells) != 256 {
		t.Errorf("expected 256 two_byte_cells entries, got %d", len(cells))
	}
}

func TestHashCollisionsThreeByteNoMatrix(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	bySize := body["by_size"].(map[string]interface{})
	threeByteData := bySize["3"].(map[string]interface{})

	// 3-byte data should NOT have one_byte_cells or two_byte_cells
	if _, ok := threeByteData["one_byte_cells"]; ok {
		t.Error("3-byte data should not have one_byte_cells")
	}
	if _, ok := threeByteData["two_byte_cells"]; ok {
		t.Error("3-byte data should not have two_byte_cells")
	}
}

func TestHashCollisionsClassification(t *testing.T) {
	// Test with seed data — nodes have coordinates, so distance classification should work
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	bySize := body["by_size"].(map[string]interface{})

	// Check that collision entries have required fields
	for _, sz := range []string{"1", "2", "3"} {
		sizeData := bySize[sz].(map[string]interface{})
		collisions := sizeData["collisions"].([]interface{})
		for i, c := range collisions {
			entry := c.(map[string]interface{})
			if _, ok := entry["prefix"]; !ok {
				t.Errorf("by_size[%s].collisions[%d] missing prefix", sz, i)
			}
			if _, ok := entry["classification"]; !ok {
				t.Errorf("by_size[%s].collisions[%d] missing classification", sz, i)
			}
			class := entry["classification"].(string)
			validClasses := map[string]bool{"local": true, "regional": true, "distant": true, "incomplete": true, "unknown": true}
			if !validClasses[class] {
				t.Errorf("by_size[%s].collisions[%d] invalid classification: %s", sz, i, class)
			}
			nodes, ok := entry["nodes"].([]interface{})
			if !ok {
				t.Errorf("by_size[%s].collisions[%d] missing nodes array", sz, i)
			}
			if len(nodes) < 2 {
				t.Errorf("by_size[%s].collisions[%d] has %d nodes, expected >=2", sz, i, len(nodes))
			}
		}
	}
}

func TestHashCollisionsCacheTTL(t *testing.T) {
	// Issue #420: collision cache should use dedicated TTL, default 3600s (1 hour)
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	if store.collisionCacheTTL != 3600*time.Second {
		t.Errorf("expected collisionCacheTTL=3600s, got %v", store.collisionCacheTTL)
	}
	if store.rfCacheTTL != 15*time.Second {
		t.Errorf("expected rfCacheTTL=15s, got %v", store.rfCacheTTL)
	}
}

func TestHashCollisionsStatsFields(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	bySize := body["by_size"].(map[string]interface{})

	for _, sz := range []string{"1", "2", "3"} {
		sizeData := bySize[sz].(map[string]interface{})
		stats := sizeData["stats"].(map[string]interface{})

		requiredFields := []string{"total_nodes", "nodes_for_byte", "using_this_size", "unique_prefixes", "collision_count", "space_size", "pct_used"}
		for _, f := range requiredFields {
			if _, ok := stats[f]; !ok {
				t.Errorf("by_size[%s].stats missing field: %s", sz, f)
			}
		}
	}
}

func TestHashCollisionsEmptyStore(t *testing.T) {
	// Test with no nodes seeded
	db := setupTestDB(t)
	// Don't call seedTestData — empty store
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)

	// With no nodes, inconsistent_nodes should be empty array
	incon := body["inconsistent_nodes"].([]interface{})
	if len(incon) != 0 {
		t.Errorf("expected 0 inconsistent nodes, got %d", len(incon))
	}

	// All collision lists should be empty
	bySize := body["by_size"].(map[string]interface{})
	for _, sz := range []string{"1", "2", "3"} {
		sizeData := bySize[sz].(map[string]interface{})
		collisions := sizeData["collisions"].([]interface{})
		if len(collisions) != 0 {
			t.Errorf("by_size[%s] expected 0 collisions with empty store, got %d", sz, len(collisions))
		}
	}
}

func TestHashCollisionsWithCollision(t *testing.T) {
	// Seed two nodes with the same 1-byte prefix to verify collision detection
	db := setupTestDB(t)
	// Don't use seedTestData — create minimal data to control hash sizes
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)

	// Two repeater nodes with same first byte 'CC' and hash_size=1
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('CC11223344556677', 'Node1', 'repeater', 37.5, -122.0, ?, '2026-01-01T00:00:00Z', 5)`, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('CC99887766554433', 'Node2', 'repeater', 37.51, -122.01, ?, '2026-01-01T00:00:00Z', 5)`, recent)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	// Inject hash_size=1 for both nodes so they appear in the 1-byte bucket
	store.hashSizeInfoMu.Lock()
	store.hashSizeInfoCache = map[string]*hashSizeNodeInfo{
		"CC11223344556677": {HashSize: 1, AllSizes: map[int]bool{1: true}},
		"CC99887766554433": {HashSize: 1, AllSizes: map[int]bool{1: true}},
	}
	store.hashSizeInfoAt = time.Now()
	store.hashSizeInfoMu.Unlock()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	bySize := body["by_size"].(map[string]interface{})
	oneByteData := bySize["1"].(map[string]interface{})
	stats := oneByteData["stats"].(map[string]interface{})

	collisionCount := int(stats["collision_count"].(float64))
	if collisionCount < 1 {
		t.Errorf("expected at least 1 collision (CC prefix), got %d", collisionCount)
	}

	// Check the collision entry
	collisions := oneByteData["collisions"].([]interface{})
	found := false
	for _, c := range collisions {
		entry := c.(map[string]interface{})
		if entry["prefix"] == "CC" {
			found = true
			nodes := entry["nodes"].([]interface{})
			if len(nodes) < 2 {
				t.Errorf("expected >=2 nodes for AA collision, got %d", len(nodes))
			}
			// Both nodes have coords close together, so classification should be "local"
			class := entry["classification"].(string)
			if class != "local" {
				t.Errorf("expected 'local' classification for nearby nodes, got %s", class)
			}
		}
	}
	if !found {
		t.Error("expected collision entry with prefix 'CC'")
	}
}

func TestHashCollisionsShortPublicKey(t *testing.T) {
	// Nodes with very short public keys should not crash
	db := setupTestDB(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)

	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('A', 'ShortKey', 'repeater', 0, 0, ?, '2026-01-01T00:00:00Z', 1)`, recent)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200 even with short public key, got %d", w.Code)
	}
}

func TestHashCollisionsMissingCoordinates(t *testing.T) {
	// Nodes without coordinates should get "incomplete" classification
	db := setupTestDB(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)

	// Two nodes same prefix, no coordinates
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('BB11223344556677', 'NoCoords1', 'repeater', 0, 0, ?, '2026-01-01T00:00:00Z', 1)`, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('BB99887766554433', 'NoCoords2', 'repeater', 0, 0, ?, '2026-01-01T00:00:00Z', 1)`, recent)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/hash-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	bySize := body["by_size"].(map[string]interface{})
	oneByteData := bySize["1"].(map[string]interface{})
	collisions := oneByteData["collisions"].([]interface{})

	for _, c := range collisions {
		entry := c.(map[string]interface{})
		if entry["prefix"] == "BB" {
			class := entry["classification"].(string)
			if class != "incomplete" {
				t.Errorf("expected 'incomplete' for nodes without coords, got %s", class)
			}
		}
	}
}

// TestHashCollisionsOnlyRepeaters verifies that only repeater nodes
// are included in collision analysis. Companions, rooms, sensors, and
// hash_size==0 nodes are excluded — per firmware analysis, only repeaters
// forward packets and appear in path[] arrays. (#441)
func TestHashCollisionsOnlyRepeaters(t *testing.T) {
	db := setupTestDB(t)

	// Insert nodes sharing the same 1-byte prefix "AA":
	//   1. repeater with hash_size=1 → should be counted
	//   2. repeater with hash_size=0 (unknown) → should be excluded
	//   3. companion with hash_size=1 → should be excluded
	//   4. room with hash_size=1 → should be excluded
	//   5. sensor with hash_size=1 → should be excluded
	now := time.Now().Format("2006-01-02 15:04:05")
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen) VALUES
		('aa11223344556677', 'Repeater1', 'repeater', ?),
		('aa99887766554433', 'UnknownNode', 'repeater', ?),
		('aadeadbeefcafe01', 'Companion1', 'companion', ?),
		('aabbcc1122334455', 'Room1', 'room', ?),
		('aabbcc9988776655', 'Sensor1', 'sensor', ?)`, now, now, now, now, now)

	// We also need a second repeater with hash_size=1 and same prefix to
	// confirm that genuine collisions ARE still detected.
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen) VALUES
		('aa00112233445566', 'Repeater2', 'repeater', ?)`, now)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store

	// Inject hash size info directly into the cache
	store.hashSizeInfoMu.Lock()
	store.hashSizeInfoCache = map[string]*hashSizeNodeInfo{
		"aa11223344556677": {HashSize: 1, AllSizes: map[int]bool{1: true}},
		"aa00112233445566": {HashSize: 1, AllSizes: map[int]bool{1: true}},
		"aa99887766554433": {HashSize: 0, AllSizes: map[int]bool{}},       // unknown
		"aadeadbeefcafe01": {HashSize: 1, AllSizes: map[int]bool{1: true}}, // companion
		"aabbcc1122334455": {HashSize: 1, AllSizes: map[int]bool{1: true}}, // room
		"aabbcc9988776655": {HashSize: 1, AllSizes: map[int]bool{1: true}}, // sensor
	}
	store.hashSizeInfoAt = time.Now()
	store.hashSizeInfoMu.Unlock()

	result := store.computeHashCollisions("")

	bySize, ok := result["by_size"].(map[string]interface{})
	if !ok {
		t.Fatal("missing by_size")
	}

	size1, ok := bySize["1"].(map[string]interface{})
	if !ok {
		t.Fatal("missing by_size[1]")
	}

	stats, ok := size1["stats"].(map[string]interface{})
	if !ok {
		t.Fatal("missing stats")
	}

	// Only Repeater1 and Repeater2 should be in nodesForByte (hash_size=1, role=repeater).
	// UnknownNode (hash_size=0), Companion1, Room1, Sensor1 must all be excluded.
	nodesForByte := stats["nodes_for_byte"]
	if nodesForByte != 2 {
		t.Errorf("expected nodes_for_byte=2 (only repeaters with hash_size=1), got %v", nodesForByte)
	}

	// They share prefix "AA", so there should be exactly 1 collision entry.
	collisions, ok := size1["collisions"].([]collisionEntry)
	if !ok {
		t.Fatalf("collisions is not []collisionEntry")
	}
	if len(collisions) != 1 {
		t.Errorf("expected 1 collision entry, got %d", len(collisions))
	}
	if len(collisions) == 1 && len(collisions[0].Nodes) != 2 {
		t.Errorf("expected 2 nodes in collision, got %d", len(collisions[0].Nodes))
	}
}

func TestNodePathsEndpointUsesIndex(t *testing.T) {
	srv, router := setupTestServer(t)

	// Verify byPathHop index was built during Load
	srv.store.mu.RLock()
	hopKeys := len(srv.store.byPathHop)
	srv.store.mu.RUnlock()
	if hopKeys == 0 {
		t.Fatal("byPathHop index is empty after Load")
	}

	// Query paths for TestRepeater (pubkey aabbccdd11223344, prefix "aa")
	// Should find transmissions with hop "aa" in path
	req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Paths              []json.RawMessage `json:"paths"`
		TotalTransmissions int               `json:"totalTransmissions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}

	// Transmission 1 has path ["aa","bb"] which contains "aa" matching prefix of aabbccdd11223344
	if resp.TotalTransmissions == 0 {
		t.Error("expected at least 1 transmission matching node paths")
	}
	if len(resp.Paths) == 0 {
		t.Error("expected at least 1 path group")
	}
}

func TestNodePathsPrefixCollisionFilter(t *testing.T) {
	// Two nodes share the "aa" prefix: TestRepeater (aabbccdd11223344) and a
	// second node (aacafe0000000000). Packets whose resolved_path points to
	// the second node must NOT appear when querying TestRepeater's paths.
	srv, router := setupTestServer(t)

	// Manually inject a transmission whose raw path contains "aa" but whose
	// resolved_path points to the other node (aacafe0000000000).
	now := time.Now().UTC()
	recent := now.Add(-30 * time.Minute).Format(time.RFC3339)
	recentEpoch := now.Add(-30 * time.Minute).Unix()

	// Insert a second node with the same 2-char prefix
	srv.db.conn.Exec(`INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('aacafe0000000000', 'CollisionNode', 'repeater', ?, '2026-01-01T00:00:00Z', 5)`, recent)

	// Insert a transmission with path hop "aa" that resolves to the OTHER node
	srv.db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('FF01', 'collision_test_hash', ?, 1, 4, '{}')`, recent)
	// Get its ID
	var collisionTxID int
	srv.db.conn.QueryRow(`SELECT id FROM transmissions WHERE hash='collision_test_hash'`).Scan(&collisionTxID)

	srv.db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp, resolved_path)
		VALUES (?, 1, 10.0, -90, '["aa","bb"]', ?, '["aacafe0000000000","eeff00112233aabb"]')`,
		collisionTxID, recentEpoch)

	// Reload store to pick up new data
	store := NewPacketStore(srv.db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store

	// Query paths for TestRepeater — should NOT include the collision packet
	req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Paths              []json.RawMessage `json:"paths"`
		TotalTransmissions int               `json:"totalTransmissions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}

	// The collision packet should be filtered out. Only transmission 1 (and 3
	// if prefix matches) should remain — but transmission 3 has path "cc" and
	// resolved_path pointing to TestRoom, so only tx 1 should match.
	// Check that collision_test_hash is not in any path group.
	bodyStr := w.Body.String()
	if strings.Contains(bodyStr, "collision_test_hash") {
		t.Error("collision packet should have been filtered out but appeared in response")
	}

	// Query paths for CollisionNode — should include the collision packet
	req2 := httptest.NewRequest("GET", "/api/nodes/aacafe0000000000/paths", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != 200 {
		t.Fatalf("expected 200 for CollisionNode, got %d: %s", w2.Code, w2.Body.String())
	}

	body2 := w2.Body.String()
	if !strings.Contains(body2, "collision_test_hash") {
		t.Error("collision packet should appear for CollisionNode but was missing")
	}
}

func TestNodeInResolvedPath(t *testing.T) {
	target := "aabbccdd11223344"

	// Case 1: tx.ResolvedPath contains target
	pk := "aabbccdd11223344"
	tx1 := &StoreTx{ResolvedPath: []*string{&pk}}
	if !nodeInResolvedPath(tx1, target) {
		t.Error("should match when ResolvedPath contains target")
	}

	// Case 2: tx.ResolvedPath contains different node
	other := "aacafe0000000000"
	tx2 := &StoreTx{ResolvedPath: []*string{&other}}
	if nodeInResolvedPath(tx2, target) {
		t.Error("should not match when ResolvedPath contains different node")
	}

	// Case 3: nil ResolvedPath — should match (no data to disambiguate, keep it)
	tx3 := &StoreTx{}
	if !nodeInResolvedPath(tx3, target) {
		t.Error("should match when ResolvedPath is nil (no data to disambiguate)")
	}

	// Case 4: ResolvedPath with nil elements only — has data but no match
	tx4 := &StoreTx{ResolvedPath: []*string{nil, nil}}
	if nodeInResolvedPath(tx4, target) {
		t.Error("should not match when all ResolvedPath elements are nil")
	}

	// Case 5: target in observation but not in tx.ResolvedPath
	tx5 := &StoreTx{
		ResolvedPath: []*string{&other},
		Observations: []*StoreObs{
			{ResolvedPath: []*string{&pk}},
		},
	}
	if !nodeInResolvedPath(tx5, target) {
		t.Error("should match when observation's ResolvedPath contains target")
	}
}

func TestPathHopIndexIncrementalUpdate(t *testing.T) {
	// Test that addTxToPathHopIndex and removeTxFromPathHopIndex work correctly
	idx := make(map[string][]*StoreTx)

	pk1 := "fullpubkey1"
	tx1 := &StoreTx{
		ID:       1,
		PathJSON: `["ab","cd"]`,
		ResolvedPath: []*string{&pk1, nil},
	}

	addTxToPathHopIndex(idx, tx1)

	// Should be indexed under "ab", "cd", and "fullpubkey1"
	if len(idx["ab"]) != 1 {
		t.Errorf("expected 1 entry for 'ab', got %d", len(idx["ab"]))
	}
	if len(idx["cd"]) != 1 {
		t.Errorf("expected 1 entry for 'cd', got %d", len(idx["cd"]))
	}
	if len(idx["fullpubkey1"]) != 1 {
		t.Errorf("expected 1 entry for resolved pubkey, got %d", len(idx["fullpubkey1"]))
	}

	// Add another tx with overlapping hop
	tx2 := &StoreTx{
		ID:       2,
		PathJSON: `["ab","ef"]`,
	}
	addTxToPathHopIndex(idx, tx2)

	if len(idx["ab"]) != 2 {
		t.Errorf("expected 2 entries for 'ab', got %d", len(idx["ab"]))
	}
	if len(idx["ef"]) != 1 {
		t.Errorf("expected 1 entry for 'ef', got %d", len(idx["ef"]))
	}

	// Remove tx1
	removeTxFromPathHopIndex(idx, tx1)

	if len(idx["ab"]) != 1 {
		t.Errorf("expected 1 entry for 'ab' after removal, got %d", len(idx["ab"]))
	}
	if _, ok := idx["cd"]; ok {
		t.Error("expected 'cd' key to be deleted after removal")
	}
	if _, ok := idx["fullpubkey1"]; ok {
		t.Error("expected resolved pubkey key to be deleted after removal")
	}
}

func TestMetricsAPIEndpoints(t *testing.T) {
	srv, router := setupTestServer(t)

	now := time.Now().UTC()
	t1 := now.Add(-1 * time.Hour).Format(time.RFC3339)

	srv.db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor) VALUES (?, ?, ?)",
		"obs1", t1, -112.0)

	// Test /api/observers/obs1/metrics
	req := httptest.NewRequest("GET", "/api/observers/obs1/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /api/observers/obs1/metrics = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	metrics, ok := resp["metrics"].([]interface{})
	if !ok || len(metrics) != 1 {
		t.Errorf("expected 1 metric in response, got %v", resp["metrics"])
	}

	// Test /api/observers/metrics/summary
	req2 := httptest.NewRequest("GET", "/api/observers/metrics/summary?window=24h", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("GET /api/observers/metrics/summary = %d, want 200", w2.Code)
	}
	var resp2 map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	observers, ok := resp2["observers"].([]interface{})
	if !ok || len(observers) != 1 {
		t.Errorf("expected 1 observer in summary, got %v", resp2["observers"])
	}
}

// --- geo-filter write-back tests ---

func setupGeoFilterServer(t *testing.T, apiKey string) (*Server, *mux.Router, string) {
	t.Helper()
	dir := t.TempDir()
	cfgJSON := `{"port":3000,"apiKey":"` + apiKey + `"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000, APIKey: apiKey}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.configDir = dir
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return srv, router, dir
}

func TestPutConfigGeoFilter(t *testing.T) {
	const apiKey = "a-strong-api-key-for-testing"

	t.Run("saves valid polygon and updates in-memory config", func(t *testing.T) {
		srv, router, dir := setupGeoFilterServer(t, apiKey)

		body := `{"polygon":[[51.0,4.0],[51.0,5.0],[50.5,5.0],[50.5,4.0]],"bufferKm":15}`
		req := httptest.NewRequest("PUT", "/api/config/geo-filter", strings.NewReader(body))
		req.Header.Set("X-API-Key", apiKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		// In-memory config updated
		if srv.cfg.GeoFilter == nil {
			t.Fatal("expected in-memory GeoFilter to be set")
		}
		if len(srv.cfg.GeoFilter.Polygon) != 4 {
			t.Errorf("expected 4 polygon points, got %d", len(srv.cfg.GeoFilter.Polygon))
		}
		if srv.cfg.GeoFilter.BufferKm != 15 {
			t.Errorf("expected bufferKm=15, got %v", srv.cfg.GeoFilter.BufferKm)
		}

		// config.json updated on disk
		data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		if !bytes.Contains(data, []byte("geo_filter")) {
			t.Error("expected geo_filter key in saved config.json")
		}
	})

	t.Run("clears filter when polygon is empty", func(t *testing.T) {
		srv, router, dir := setupGeoFilterServer(t, apiKey)
		// Pre-set a filter so we can clear it
		srv.cfg.GeoFilter = &GeoFilterConfig{Polygon: [][2]float64{{51.0, 4.0}, {51.0, 5.0}, {50.5, 4.0}}, BufferKm: 10}

		req := httptest.NewRequest("PUT", "/api/config/geo-filter", strings.NewReader(`{"polygon":null}`))
		req.Header.Set("X-API-Key", apiKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if srv.cfg.GeoFilter != nil {
			t.Error("expected in-memory GeoFilter to be cleared")
		}
		data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		if bytes.Contains(data, []byte("geo_filter")) {
			t.Error("expected geo_filter to be removed from config.json")
		}
	})

	t.Run("rejects polygon with fewer than 3 points", func(t *testing.T) {
		_, router, _ := setupGeoFilterServer(t, apiKey)

		body := `{"polygon":[[51.0,4.0],[51.0,5.0]],"bufferKm":0}`
		req := httptest.NewRequest("PUT", "/api/config/geo-filter", strings.NewReader(body))
		req.Header.Set("X-API-Key", apiKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", w.Code)
		}
	})

	t.Run("rejects missing API key", func(t *testing.T) {
		_, router, _ := setupGeoFilterServer(t, apiKey)

		body := `{"polygon":[[51.0,4.0],[51.0,5.0],[50.5,4.0]],"bufferKm":0}`
		req := httptest.NewRequest("PUT", "/api/config/geo-filter", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	})
}

func TestSaveGeoFilter(t *testing.T) {
	t.Run("saves and reads back", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"port":3000}`), 0644); err != nil {
			t.Fatal(err)
		}
		gf := &GeoFilterConfig{
			Polygon:  [][2]float64{{51.0, 4.0}, {51.0, 5.0}, {50.5, 4.0}},
			BufferKm: 20,
		}
		if err := SaveGeoFilter(dir, gf); err != nil {
			t.Fatalf("SaveGeoFilter: %v", err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		if !bytes.Contains(data, []byte("geo_filter")) {
			t.Error("expected geo_filter in saved config")
		}
		if !bytes.Contains(data, []byte(`"bufferKm"`)) {
			t.Error("expected bufferKm in saved config")
		}
	})

	t.Run("removes geo_filter key when gf is nil", func(t *testing.T) {
		dir := t.TempDir()
		initial := `{"port":3000,"geo_filter":{"polygon":[[1,2],[3,4],[5,6]],"bufferKm":5}}`
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(initial), 0644); err != nil {
			t.Fatal(err)
		}
		if err := SaveGeoFilter(dir, nil); err != nil {
			t.Fatalf("SaveGeoFilter: %v", err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
		if bytes.Contains(data, []byte("geo_filter")) {
			t.Error("expected geo_filter to be removed")
		}
	})

	t.Run("returns error when config.json not found", func(t *testing.T) {
		dir := t.TempDir()
		err := SaveGeoFilter(dir, nil)
		if err == nil {
			t.Error("expected error when config.json not found")
		}
	})
}
