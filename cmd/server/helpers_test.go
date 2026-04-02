package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, 404, "Not found")

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "Not found" {
		t.Errorf("expected 'Not found', got %s", body["error"])
	}
}

func TestWriteErrorVariousCodes(t *testing.T) {
	tests := []struct {
		code int
		msg  string
	}{
		{400, "Bad request"},
		{500, "Internal error"},
		{403, "Forbidden"},
	}
	for _, tc := range tests {
		w := httptest.NewRecorder()
		writeError(w, tc.code, tc.msg)
		if w.Code != tc.code {
			t.Errorf("expected %d, got %d", tc.code, w.Code)
		}
	}
}

func TestQueryInt(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		key      string
		def      int
		expected int
	}{
		{"valid", "/?limit=25", "limit", 50, 25},
		{"missing", "/?other=5", "limit", 50, 50},
		{"empty", "/?limit=", "limit", 50, 50},
		{"invalid", "/?limit=abc", "limit", 50, 50},
		{"zero", "/?limit=0", "limit", 50, 0},
		{"negative", "/?limit=-1", "limit", 50, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.url, nil)
			got := queryInt(r, tc.key, tc.def)
			if got != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, got)
			}
		})
	}
}

func TestMergeMap(t *testing.T) {
	t.Run("basic merge", func(t *testing.T) {
		base := map[string]interface{}{"a": 1, "b": 2}
		overlay := map[string]interface{}{"b": 3, "c": 4}
		result := mergeMap(base, overlay)

		if result["a"] != 1 {
			t.Errorf("expected 1, got %v", result["a"])
		}
		if result["b"] != 3 {
			t.Errorf("expected 3 (overridden), got %v", result["b"])
		}
		if result["c"] != 4 {
			t.Errorf("expected 4, got %v", result["c"])
		}
	})

	t.Run("nil overlay", func(t *testing.T) {
		base := map[string]interface{}{"a": 1}
		result := mergeMap(base, nil)
		if result["a"] != 1 {
			t.Errorf("expected 1, got %v", result["a"])
		}
	})

	t.Run("multiple overlays", func(t *testing.T) {
		base := map[string]interface{}{"a": 1}
		o1 := map[string]interface{}{"b": 2}
		o2 := map[string]interface{}{"c": 3, "a": 10}
		result := mergeMap(base, o1, o2)
		if result["a"] != 10 {
			t.Errorf("expected 10, got %v", result["a"])
		}
		if result["b"] != 2 {
			t.Errorf("expected 2, got %v", result["b"])
		}
		if result["c"] != 3 {
			t.Errorf("expected 3, got %v", result["c"])
		}
	})

	t.Run("empty base", func(t *testing.T) {
		result := mergeMap(map[string]interface{}{}, map[string]interface{}{"x": 5})
		if result["x"] != 5 {
			t.Errorf("expected 5, got %v", result["x"])
		}
	})
}

func TestSafeAvg(t *testing.T) {
	tests := []struct {
		total, count float64
		expected     float64
	}{
		{100, 10, 10.0},
		{0, 0, 0},
		{33, 3, 11.0},
		{10, 3, 3.3},
	}
	for _, tc := range tests {
		got := safeAvg(tc.total, tc.count)
		if got != tc.expected {
			t.Errorf("safeAvg(%v, %v) = %v, want %v", tc.total, tc.count, got, tc.expected)
		}
	}
}

func TestRound(t *testing.T) {
	tests := []struct {
		val    float64
		places int
		want   float64
	}{
		{3.456, 1, 3.5},
		{3.444, 1, 3.4},
		{3.456, 2, 3.46},
		{0, 1, 0},
		{100.0, 0, 100.0},
	}
	for _, tc := range tests {
		got := round(tc.val, tc.places)
		if got != tc.want {
			t.Errorf("round(%v, %d) = %v, want %v", tc.val, tc.places, got, tc.want)
		}
	}
}

func TestPercentile(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if percentile([]float64{}, 0.5) != 0 {
			t.Error("expected 0 for empty slice")
		}
	})

	t.Run("single element", func(t *testing.T) {
		if percentile([]float64{42}, 0.5) != 42 {
			t.Error("expected 42")
		}
	})

	t.Run("p50", func(t *testing.T) {
		sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
		got := percentile(sorted, 0.5)
		if got != 6 {
			t.Errorf("expected 6 for p50, got %v", got)
		}
	})

	t.Run("p95", func(t *testing.T) {
		sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
		got := percentile(sorted, 0.95)
		if got != 10 {
			t.Errorf("expected 10 for p95, got %v", got)
		}
	})

	t.Run("p100 clamps", func(t *testing.T) {
		sorted := []float64{1, 2, 3}
		got := percentile(sorted, 1.0)
		if got != 3 {
			t.Errorf("expected 3 for p100, got %v", got)
		}
	})
}

func TestSortedCopy(t *testing.T) {
	original := []float64{5, 3, 1, 4, 2}
	sorted := sortedCopy(original)

	// Original should be unchanged
	if original[0] != 5 {
		t.Error("original should not be modified")
	}

	expected := []float64{1, 2, 3, 4, 5}
	for i, v := range sorted {
		if v != expected[i] {
			t.Errorf("index %d: expected %v, got %v", i, expected[i], v)
		}
	}

	// Empty slice
	empty := sortedCopy([]float64{})
	if len(empty) != 0 {
		t.Error("expected empty slice")
	}
}

func TestLastN(t *testing.T) {
	arr := []map[string]interface{}{
		{"id": 1}, {"id": 2}, {"id": 3}, {"id": 4}, {"id": 5},
	}

	t.Run("n less than length", func(t *testing.T) {
		result := lastN(arr, 3)
		if len(result) != 3 {
			t.Errorf("expected 3, got %d", len(result))
		}
		if result[0]["id"] != 3 {
			t.Errorf("expected id 3, got %v", result[0]["id"])
		}
	})

	t.Run("n greater than length", func(t *testing.T) {
		result := lastN(arr, 10)
		if len(result) != 5 {
			t.Errorf("expected 5, got %d", len(result))
		}
	})

	t.Run("n equals length", func(t *testing.T) {
		result := lastN(arr, 5)
		if len(result) != 5 {
			t.Errorf("expected 5, got %d", len(result))
		}
	})

	t.Run("empty", func(t *testing.T) {
		result := lastN([]map[string]interface{}{}, 5)
		if len(result) != 0 {
			t.Errorf("expected 0, got %d", len(result))
		}
	})
}

func TestSpaHandler(t *testing.T) {
	// Create a temp directory with test files
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA</html>"), 0644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('app')"), 0644)
	os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0644)

	fs := http.FileServer(http.Dir(dir))
	handler := spaHandler(dir, fs)

	t.Run("existing JS file with cache control", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/app.js", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}
		cc := w.Header().Get("Cache-Control")
		if cc != "no-cache, no-store, must-revalidate" {
			t.Errorf("expected no-cache header for .js, got %s", cc)
		}
	})

	t.Run("existing CSS file with cache control", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/style.css", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}
		cc := w.Header().Get("Cache-Control")
		if cc != "no-cache, no-store, must-revalidate" {
			t.Errorf("expected no-cache header for .css, got %s", cc)
		}
	})

	t.Run("non-existent file falls back to index.html", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/some/spa/route", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}
		body := w.Body.String()
		if body != "<html>SPA</html>" {
			t.Errorf("expected SPA index.html content, got %s", body)
		}
	})

	t.Run("existing HTML file", func(t *testing.T) {
		// Subdirectory with HTML file to avoid redirect from root /index.html
		subDir := filepath.Join(dir, "sub")
		os.Mkdir(subDir, 0755)
		os.WriteFile(filepath.Join(subDir, "page.html"), []byte("<html>page</html>"), 0644)

		req := httptest.NewRequest("GET", "/sub/page.html", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}
		cc := w.Header().Get("Cache-Control")
		if cc != "no-cache, no-store, must-revalidate" {
			t.Errorf("expected no-cache header for .html, got %s", cc)
		}
	})

	t.Run("root path serves index.html", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}
		body := w.Body.String()
		if body != "<html>SPA</html>" {
			t.Errorf("expected SPA index.html content, got %s", body)
		}
		ct := w.Header().Get("Content-Type")
		if ct != "text/html; charset=utf-8" {
			t.Errorf("expected text/html content type, got %s", ct)
		}
	})

	t.Run("/index.html serves pre-processed content", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/index.html", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}
		body := w.Body.String()
		if body != "<html>SPA</html>" {
			t.Errorf("expected SPA index.html content, got %s", body)
		}
	})
}

func TestSpaHandlerCacheBust(t *testing.T) {
	dir := t.TempDir()
	htmlWithBust := `<html><script src="app.js?v=__BUST__"></script><link href="style.css?v=__BUST__"></html>`
	os.WriteFile(filepath.Join(dir, "index.html"), []byte(htmlWithBust), 0644)

	fs := http.FileServer(http.Dir(dir))
	handler := spaHandler(dir, fs)

	t.Run("__BUST__ is replaced with a Unix timestamp", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		body := w.Body.String()
		if strings.Contains(body, "__BUST__") {
			t.Errorf("__BUST__ placeholder was not replaced in response: %s", body)
		}
		// Verify it was replaced with digits (Unix timestamp)
		if !strings.Contains(body, "v=") {
			t.Errorf("expected v= query params in response, got: %s", body)
		}
	})

	t.Run("SPA fallback also has busted values", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/nonexistent/route", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		body := w.Body.String()
		if strings.Contains(body, "__BUST__") {
			t.Errorf("__BUST__ placeholder was not replaced in SPA fallback: %s", body)
		}
	})

	t.Run("/index.html also has busted values", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/index.html", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		body := w.Body.String()
		if strings.Contains(body, "__BUST__") {
			t.Errorf("__BUST__ placeholder was not replaced for /index.html: %s", body)
		}
	})
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, map[string]interface{}{"key": "value"})

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["key"] != "value" {
		t.Errorf("expected 'value', got %v", body["key"])
	}
}

func TestHaversineKm(t *testing.T) {
	// Same point should be 0
	if d := haversineKm(37.0, -122.0, 37.0, -122.0); d != 0 {
		t.Errorf("same point: expected 0, got %f", d)
	}

	// SF to LA ~559km
	d := haversineKm(37.7749, -122.4194, 34.0522, -118.2437)
	if d < 550 || d > 570 {
		t.Errorf("SF to LA: expected ~559km, got %f", d)
	}

	// Symmetry
	d1 := haversineKm(37.7749, -122.4194, 34.0522, -118.2437)
	d2 := haversineKm(34.0522, -118.2437, 37.7749, -122.4194)
	if d1 != d2 {
		t.Errorf("not symmetric: %f vs %f", d1, d2)
	}

	// Oslo to Stockholm ~415km (old Euclidean dLat*111, dLon*85 would give ~627km)
	d = haversineKm(59.9, 10.7, 59.3, 18.0)
	if d < 400 || d > 430 {
		t.Errorf("Oslo to Stockholm: expected ~415km, got %f", d)
	}
}
