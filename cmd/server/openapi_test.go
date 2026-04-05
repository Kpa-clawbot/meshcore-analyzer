package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAPISpecEndpoint(t *testing.T) {
	_, r := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/spec", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json; charset=utf-8" {
		t.Errorf("unexpected content-type: %s", ct)
	}

	var spec map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Check required OpenAPI fields
	if spec["openapi"] != "3.0.3" {
		t.Errorf("expected openapi 3.0.3, got %v", spec["openapi"])
	}

	info, ok := spec["info"].(map[string]interface{})
	if !ok {
		t.Fatal("missing info object")
	}
	if info["title"] != "CoreScope API" {
		t.Errorf("unexpected title: %v", info["title"])
	}

	paths, ok := spec["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("missing paths object")
	}

	// Should have at least 20 paths
	if len(paths) < 20 {
		t.Errorf("expected at least 20 paths, got %d", len(paths))
	}

	// Check a known path exists
	if _, ok := paths["/api/nodes"]; !ok {
		t.Error("missing /api/nodes path")
	}
	if _, ok := paths["/api/packets"]; !ok {
		t.Error("missing /api/packets path")
	}

	// Check tags exist
	tags, ok := spec["tags"].([]interface{})
	if !ok || len(tags) == 0 {
		t.Error("missing or empty tags")
	}

	// Check security schemes
	components, ok := spec["components"].(map[string]interface{})
	if !ok {
		t.Fatal("missing components")
	}
	schemes, ok := components["securitySchemes"].(map[string]interface{})
	if !ok {
		t.Fatal("missing securitySchemes")
	}
	if _, ok := schemes["ApiKeyAuth"]; !ok {
		t.Error("missing ApiKeyAuth security scheme")
	}

	// Spec should NOT contain /api/spec or /api/docs (self-referencing)
	if _, ok := paths["/api/spec"]; ok {
		t.Error("/api/spec should not appear in the spec")
	}
	if _, ok := paths["/api/docs"]; ok {
		t.Error("/api/docs should not appear in the spec")
	}
}

func TestSwaggerUIEndpoint(t *testing.T) {
	_, r := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/docs", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "text/html; charset=utf-8" {
		t.Errorf("unexpected content-type: %s", ct)
	}

	body := w.Body.String()
	if len(body) < 100 {
		t.Error("response too short for Swagger UI HTML")
	}
	if !strings.Contains(body, "swagger-ui") {
		t.Error("response doesn't contain swagger-ui reference")
	}
	if !strings.Contains(body, "/api/spec") {
		t.Error("response doesn't point to /api/spec")
	}
}

func TestExtractPathParams(t *testing.T) {
	tests := []struct {
		path   string
		expect []string
	}{
		{"/api/nodes", nil},
		{"/api/nodes/{pubkey}", []string{"pubkey"}},
		{"/api/channels/{hash}/messages", []string{"hash"}},
	}
	for _, tt := range tests {
		got := extractPathParams(tt.path)
		if len(got) != len(tt.expect) {
			t.Errorf("extractPathParams(%q) = %v, want %v", tt.path, got, tt.expect)
			continue
		}
		for i := range got {
			if got[i] != tt.expect[i] {
				t.Errorf("extractPathParams(%q)[%d] = %q, want %q", tt.path, i, got[i], tt.expect[i])
			}
		}
	}
}


