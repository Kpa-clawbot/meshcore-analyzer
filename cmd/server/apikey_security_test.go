package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsWeakAPIKey(t *testing.T) {
	// Known defaults must be detected
	for _, weak := range []string{
		"your-secret-api-key-here", "change-me", "example", "test",
		"password", "admin", "apikey", "api-key", "secret", "default",
	} {
		if !IsWeakAPIKey(weak) {
			t.Errorf("expected %q to be weak", weak)
		}
	}
	// Case-insensitive
	if !IsWeakAPIKey("Password") {
		t.Error("expected case-insensitive match for Password")
	}
	if !IsWeakAPIKey("YOUR-SECRET-API-KEY-HERE") {
		t.Error("expected case-insensitive match")
	}

	// Short keys (<16 chars) are weak
	if !IsWeakAPIKey("short") {
		t.Error("expected short key to be weak")
	}
	if !IsWeakAPIKey("exactly15chars!") { // 15 chars
		t.Error("expected 15-char key to be weak")
	}

	// Empty key is NOT weak (handled separately as "disabled")
	if IsWeakAPIKey("") {
		t.Error("empty key should not be flagged as weak")
	}

	// Strong keys pass
	if IsWeakAPIKey("a-very-strong-key-1234") {
		t.Error("expected strong key to pass")
	}
	if IsWeakAPIKey("xK9!mP2@nL5#qR8$") {
		t.Error("expected 17-char random key to pass")
	}
}

func TestRequireAPIKey_RejectsWeakKey(t *testing.T) {
	s := &Server{cfg: &Config{APIKey: "test"}}
	handler := s.requireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/packets", nil)
	req.Header.Set("X-API-Key", "test")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for weak key, got %d", rr.Code)
	}
}

func TestRequireAPIKey_AcceptsStrongKey(t *testing.T) {
	strongKey := "a-very-strong-key-1234"
	s := &Server{cfg: &Config{APIKey: strongKey}}
	handler := s.requireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/packets", nil)
	req.Header.Set("X-API-Key", strongKey)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for strong key, got %d", rr.Code)
	}
}

func TestRequireAPIKey_EmptyKeyDisablesEndpoints(t *testing.T) {
	s := &Server{cfg: &Config{APIKey: ""}}
	handler := s.requireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/packets", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for empty key, got %d", rr.Code)
	}
}

func TestRequireAPIKey_WrongKeyUnauthorized(t *testing.T) {
	s := &Server{cfg: &Config{APIKey: "a-very-strong-key-1234"}}
	handler := s.requireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/packets", nil)
	req.Header.Set("X-API-Key", "wrong-key-entirely-here")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong key, got %d", rr.Code)
	}
}
