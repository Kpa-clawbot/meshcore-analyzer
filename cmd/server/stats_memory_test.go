package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStatsMemoryFields verifies that /api/stats exposes the new memory
// breakdown introduced for issue #832: storeDataMB, processRSSMB,
// goHeapInuseMB, goSysMB, plus the deprecated trackedMB alias.
//
// We assert presence, type, sign, and ordering invariants — but NOT
// "RSS within X% of true RSS" because that is flaky in CI under cgo,
// containerization, and shared-runner load.
func TestStatsMemoryFields(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	required := []string{"trackedMB", "storeDataMB", "processRSSMB", "goHeapInuseMB", "goSysMB"}
	values := make(map[string]float64, len(required))
	for _, k := range required {
		v, ok := body[k]
		if !ok {
			t.Fatalf("missing field %q in /api/stats response", k)
		}
		f, ok := v.(float64)
		if !ok {
			t.Fatalf("field %q is %T, expected float64", k, v)
		}
		if f < 0 {
			t.Errorf("field %q is negative: %v", k, f)
		}
		values[k] = f
	}

	// trackedMB is a deprecated alias for storeDataMB; they must match.
	if values["trackedMB"] != values["storeDataMB"] {
		t.Errorf("trackedMB (%v) != storeDataMB (%v); they must remain aliased",
			values["trackedMB"], values["storeDataMB"])
	}

	// Ordering invariants. goSys is the runtime's view of total OS memory;
	// HeapInuse is a subset of it. storeData is a subset of HeapInuse.
	// processRSS may be 0 in environments without /proc — treat 0 as
	// "unknown" rather than a failure.
	if values["goHeapInuseMB"] > values["goSysMB"]+0.5 {
		t.Errorf("invariant violated: goHeapInuseMB (%v) > goSysMB (%v)",
			values["goHeapInuseMB"], values["goSysMB"])
	}
	if values["storeDataMB"] > values["goHeapInuseMB"]+0.5 && values["storeDataMB"] > 0 {
		// In the test fixture storeDataMB is typically 0 (no packets in
		// store); only enforce the bound when both are nonzero.
		t.Errorf("invariant violated: storeDataMB (%v) > goHeapInuseMB (%v)",
			values["storeDataMB"], values["goHeapInuseMB"])
	}
	if values["processRSSMB"] > 0 && values["goSysMB"] > 0 {
		// goSys can briefly exceed RSS if pages are reserved-but-not-touched,
		// so allow some slack.
		if values["goSysMB"] > values["processRSSMB"]*4 {
			t.Errorf("suspicious: goSysMB (%v) >> processRSSMB (%v)",
				values["goSysMB"], values["processRSSMB"])
		}
	}
}

// TestStatsMemoryFieldsRawJSON spot-checks that the JSON wire format uses
// the documented camelCase names (no accidental rename through struct tags).
func TestStatsMemoryFieldsRawJSON(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	body := w.Body.String()
	for _, key := range []string{
		`"trackedMB":`, `"storeDataMB":`,
		`"processRSSMB":`, `"goHeapInuseMB":`, `"goSysMB":`,
	} {
		if !strings.Contains(body, key) {
			t.Errorf("missing %s in raw response: %s", key, body)
		}
	}
}
