package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func setupTestServer(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return srv, router
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
	if body["totalTransmissions"] != float64(2) {
		t.Errorf("expected 2 transmissions, got %v", body["totalTransmissions"])
	}
	if body["totalNodes"] != float64(3) {
		t.Errorf("expected 3 nodes, got %v", body["totalNodes"])
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
		t.Errorf("expected 3 packets, got %d", len(packets))
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
	if len(packets) != 2 {
		t.Errorf("expected 2 grouped packets, got %d", len(packets))
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

	// Drop the view to force query errors
	db.conn.Exec("DROP VIEW IF EXISTS packets_v")

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

	db.conn.Exec("DROP VIEW IF EXISTS packets_v")

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

	db.conn.Exec("DROP VIEW IF EXISTS packets_v")

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

	db.conn.Exec("DROP VIEW IF EXISTS packets_v")

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

	db.conn.Exec("DROP VIEW IF EXISTS packets_v")

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

	db.conn.Exec("DROP VIEW IF EXISTS packets_v")

	req := httptest.NewRequest("GET", "/api/packets/timestamps?since=2020-01-01", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for timestamps error, got %d", w.Code)
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

	db.conn.Exec("DROP VIEW IF EXISTS packets_v")

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
	if w.Code != 500 {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
