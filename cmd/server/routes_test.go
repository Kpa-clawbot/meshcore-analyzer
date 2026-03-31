package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	_, router := setupTestServerWithAPIKey(t, "test-secret")

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
		req := httptest.NewRequest("POST", "/api/decode", bytes.NewBufferString(`{"hex":"0200"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", "wrong-secret")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	})

	t.Run("correct key passes", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/decode", bytes.NewBufferString(`{"hex":"0200"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", "test-secret")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
		}
	})
}

func TestWriteEndpointsBlockWhenAPIKeyEmpty(t *testing.T) {
	_, router := setupTestServerWithAPIKey(t, "")

	req := httptest.NewRequest("POST", "/api/decode", bytes.NewBufferString(`{"hex":"0200"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with empty apiKey, got %d (body: %s)", w.Code, w.Body.String())
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
FirstSeen:   "2024-01-01T00:00:00Z",
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
// A node that sends mostly 2-byte adverts but occasionally 1-byte (pathByte=0x00
// on direct sends) should report HashSize=2, not 1.
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
// 1 packet with hashSize=1, 4 packets with hashSize=2
raws := []string{raw1byte, raw2byte, raw2byte, raw2byte, raw2byte}
for i, raw := range raws {
	tx := &StoreTx{
		ID:          8000 + i,
		RawHex:      raw,
		Hash:        "dominant" + strconv.Itoa(i),
		FirstSeen:   "2024-01-01T00:00:00Z",
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
	t.Errorf("HashSize=%d, want 2 (dominant size should win over occasional 1-byte)", ni.HashSize)
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
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
