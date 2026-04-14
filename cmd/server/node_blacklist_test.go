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