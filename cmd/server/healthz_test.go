package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzNotReady(t *testing.T) {
	// Ensure readiness is 0 (not ready)
	readiness.Store(0)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ready"] != false {
		t.Fatalf("expected ready=false, got %v", resp["ready"])
	}
	if resp["reason"] != "loading" {
		t.Fatalf("expected reason=loading, got %v", resp["reason"])
	}
}

func TestHealthzReady(t *testing.T) {
	readiness.Store(1)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ready"] != true {
		t.Fatalf("expected ready=true, got %v", resp["ready"])
	}
	if _, ok := resp["loadedTx"]; !ok {
		t.Fatal("missing loadedTx field")
	}
	if _, ok := resp["loadedObs"]; !ok {
		t.Fatal("missing loadedObs field")
	}
}

func TestHealthzAntiTautology(t *testing.T) {
	// When readiness is 0, must NOT return 200
	readiness.Store(0)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("anti-tautology: handler returned 200 when readiness=0; gating is broken")
	}
}
