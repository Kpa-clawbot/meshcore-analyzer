package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPacketsChannelFilter verifies /api/packets?channel=... actually filters
// (regression test for #812).
func TestPacketsChannelFilter(t *testing.T) {
	_, router := setupTestServer(t)

	get := func(url string) map[string]interface{} {
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: expected 200, got %d", url, w.Code)
		}
		var body map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
		return body
	}

	all := get("/api/packets?limit=50")
	allTotal := int(all["total"].(float64))
	if allTotal < 2 {
		t.Fatalf("expected baseline >= 2 packets, got %d", allTotal)
	}

	test := get("/api/packets?limit=50&channel=%23test")
	testTotal := int(test["total"].(float64))
	if testTotal == 0 {
		t.Fatalf("channel=#test: expected >= 1 match, got 0 (filter ignored?)")
	}
	if testTotal >= allTotal {
		t.Fatalf("channel=#test: expected fewer packets than baseline (%d), got %d", allTotal, testTotal)
	}

	// Every returned packet must be a CHAN/GRP_TXT (payload_type=5) on #test.
	pkts, _ := test["packets"].([]interface{})
	for _, p := range pkts {
		m := p.(map[string]interface{})
		if pt, _ := m["payload_type"].(float64); int(pt) != 5 {
			t.Errorf("channel=#test: returned non-GRP_TXT packet (payload_type=%v)", m["payload_type"])
		}
	}

	none := get("/api/packets?limit=50&channel=nonexistentchannel")
	if int(none["total"].(float64)) != 0 {
		t.Fatalf("channel=nonexistentchannel: expected total=0, got %v", none["total"])
	}
}
