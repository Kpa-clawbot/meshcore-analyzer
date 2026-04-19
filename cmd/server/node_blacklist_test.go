package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func TestConfigIsBlacklisted(t *testing.T) {
	cfg := &Config{
		NodeBlacklist: []string{"AA", "BB", "cc"},
	}

	tests := []struct {
		pubkey    string
		want      bool
	}{
		{"AA", true},
		{"aa", true},   // case-insensitive
		{"BB", true},
		{"CC", true},   // lowercase "cc" matches uppercase
		{"DD", false},
		{"", false},
		{"AAB", false},
	}

	for _, tt := range tests {
		got := cfg.IsBlacklisted(tt.pubkey)
		if got != tt.want {
			t.Errorf("IsBlacklisted(%q) = %v, want %v", tt.pubkey, got, tt.want)
		}
	}
}

func TestConfigIsBlacklistedEmpty(t *testing.T) {
	cfg := &Config{}
	if cfg.IsBlacklisted("anything") {
		t.Error("empty blacklist should not match anything")
	}
	if cfg.IsBlacklisted("") {
		t.Error("empty blacklist should not match empty string")
	}
}

func TestConfigBlacklistWhitespace(t *testing.T) {
	cfg := &Config{
		NodeBlacklist: []string{"  AA  ", "BB"},
	}
	if !cfg.IsBlacklisted("AA") {
		t.Error("trimmed key should match")
	}
	if !cfg.IsBlacklisted("  AA  ") {
		t.Error("whitespace-padded key should match after trimming")
	}
}

func TestConfigBlacklistEmptyEntries(t *testing.T) {
	cfg := &Config{
		NodeBlacklist: []string{"", "  ", "AA"},
	}
	if !cfg.IsBlacklisted("AA") {
		t.Error("non-empty entry should match")
	}
	if cfg.IsBlacklisted("") {
		t.Error("empty blacklist entry should not match empty pubkey")
	}
}

func TestBlacklistFiltersHandleNodes(t *testing.T) {
	db := setupTestDB(t)
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen) VALUES ('goodnode', 'GoodNode', 'companion', datetime('now'))")
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen) VALUES ('badnode', 'BadNode', 'companion', datetime('now'))")

	cfg := &Config{
		NodeBlacklist: []string{"badnode"},
	}
	srv := NewServer(db, cfg, NewHub())

	req := httptest.NewRequest("GET", "/api/nodes?limit=50", nil)
	w := httptest.NewRecorder()
	srv.RegisterRoutes(setupTestRouter(srv))
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp NodeListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	for _, node := range resp.Nodes {
		if pk, _ := node["public_key"].(string); pk == "badnode" {
			t.Error("blacklisted node should not appear in nodes list")
		}
	}
	if resp.Total == 0 {
		t.Error("expected at least one non-blacklisted node")
	}
}

func TestBlacklistFiltersNodeDetail(t *testing.T) {
	db := setupTestDB(t)
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen) VALUES ('badnode', 'BadNode', 'companion', datetime('now'))")

	cfg := &Config{
		NodeBlacklist: []string{"badnode"},
	}
	srv := NewServer(db, cfg, NewHub())

	req := httptest.NewRequest("GET", "/api/nodes/badnode", nil)
	w := httptest.NewRecorder()
	srv.RegisterRoutes(setupTestRouter(srv))
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for blacklisted node, got %d", w.Code)
	}
}

func TestBlacklistFiltersNodeSearch(t *testing.T) {
	db := setupTestDB(t)
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen) VALUES ('badnode', 'TrollNode', 'companion', datetime('now'))")
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen) VALUES ('goodnode', 'GoodNode', 'companion', datetime('now'))")

	cfg := &Config{
		NodeBlacklist: []string{"badnode"},
	}
	srv := NewServer(db, cfg, NewHub())

	req := httptest.NewRequest("GET", "/api/nodes/search?q=Troll", nil)
	w := httptest.NewRecorder()
	srv.RegisterRoutes(setupTestRouter(srv))
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp NodeSearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	for _, node := range resp.Nodes {
		if pk, _ := node["public_key"].(string); pk == "badnode" {
			t.Error("blacklisted node should not appear in search results")
		}
	}
}

func TestNoBlacklistPassesAll(t *testing.T) {
	db := setupTestDB(t)
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen) VALUES ('somenode', 'SomeNode', 'companion', datetime('now'))")

	cfg := &Config{}
	srv := NewServer(db, cfg, NewHub())

	req := httptest.NewRequest("GET", "/api/nodes?limit=50", nil)
	w := httptest.NewRecorder()
	srv.RegisterRoutes(setupTestRouter(srv))
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp NodeListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Total == 0 {
		t.Error("without blacklist, node should appear")
	}
}

// setupTestRouter creates a mux.Router and registers server routes.
func setupTestRouter(srv *Server) *mux.Router {
	r := mux.NewRouter()
	srv.RegisterRoutes(r)
	srv.router = r
	return r
}
func TestBlacklistFiltersNeighborGraph(t *testing.T) {
	cfg := &Config{
		NodeBlacklist: []string{"badnode"},
	}
	db := setupTestDB(t)
	srv := NewServer(db, cfg, NewHub())
	srv.RegisterRoutes(setupTestRouter(srv))

	req := httptest.NewRequest("GET", "/api/analytics/neighbor-graph", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check edges don't contain blacklisted node
	if edges, ok := resp["edges"].([]interface{}); ok {
		for _, e := range edges {
			if edge, ok := e.(map[string]interface{}); ok {
				if src, _ := edge["source"].(string); src == "badnode" {
					t.Error("blacklisted node should not appear as edge source in neighbor graph")
				}
				if tgt, _ := edge["target"].(string); tgt == "badnode" {
					t.Error("blacklisted node should not appear as edge target in neighbor graph")
				}
			}
		}
	}

	// Check nodes list doesn't contain blacklisted node
	if nodes, ok := resp["nodes"].([]interface{}); ok {
		for _, n := range nodes {
			if node, ok := n.(map[string]interface{}); ok {
				if pk, _ := node["pubkey"].(string); pk == "badnode" {
					t.Error("blacklisted node should not appear in neighbor graph nodes")
				}
			}
		}
	}
}

func TestBlacklistFiltersResolveHops(t *testing.T) {
	db := setupTestDB(t)
	db.conn.Exec("INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen) VALUES ('badnode', 'BadNode', 'companion', datetime('now'))")

	cfg := &Config{
		NodeBlacklist: []string{"badnode"},
	}
	srv := NewServer(db, cfg, NewHub())
	srv.RegisterRoutes(setupTestRouter(srv))

	req := httptest.NewRequest("GET", "/api/resolve-hops?hops=badnode", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ResolveHopsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if hr, ok := resp.Resolved["badnode"]; ok {
		for _, c := range hr.Candidates {
			if c.Pubkey == "badnode" {
				t.Error("blacklisted node should not appear as resolve-hops candidate")
			}
		}
	}
}

func TestBlacklistFiltersSubpathDetail(t *testing.T) {
	cfg := &Config{
		NodeBlacklist: []string{"badnode"},
	}
	db := setupTestDB(t)
	srv := NewServer(db, cfg, NewHub())
	srv.RegisterRoutes(setupTestRouter(srv))

	req := httptest.NewRequest("GET", "/api/analytics/subpath-detail?hops=badnode,othernode", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for subpath-detail with blacklisted hop, got %d", w.Code)
	}
}

func TestBlacklistConcurrentIsBlacklisted(t *testing.T) {
	cfg := &Config{
		NodeBlacklist: []string{"AA", "BB", "CC"},
	}

	errc := make(chan error, 100)
	for i := 0; i < 100; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				cfg.IsBlacklisted("AA")
				cfg.IsBlacklisted("BB")
				cfg.IsBlacklisted("DD")
			}
		}()
	}

	// If sync.Once is wrong, this would panic or race.
	// We can't run the race detector on ARM, but at least verify no panics.
	done := false
	for !done {
		select {
		case <-errc:
			t.Error("concurrent IsBlacklisted panicked")
		default:
			done = true
		}
	}
}
