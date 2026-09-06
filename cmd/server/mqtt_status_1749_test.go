package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Issue #1749 (root-cause fix): /api/mqtt/status must surface
// WatchdogLogDropCount alongside WatchdogLastTickUnix /
// WatchdogPanicCount so external monitoring can distinguish "watchdog
// dead" (stale tick) from "watchdog alive, but its log sink is stuck"
// (ticking normally, drop count climbing) — the actual root cause
// behind the original #1749 production incident, which the #1810
// panic-recovery fix did not address (a blocked write() hangs, it
// does not panic).
func TestMqttStatus_ExposesWatchdogLogDropCount_1749(t *testing.T) {
	tmp := t.TempDir()
	statsPath := filepath.Join(tmp, "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	stub := map[string]any{
		"sampledAt":            "2026-07-18T12:30:00Z",
		"source_statuses":      []map[string]any{},
		"watchdogLastTickUnix": int64(1752841800),
		"watchdogPanicCount":   int64(0),
		"watchdogLogDropCount": int64(413),
	}
	data, err := json.Marshal(stub)
	if err != nil {
		t.Fatalf("marshal stub: %v", err)
	}
	if err := os.WriteFile(statsPath, data, 0o600); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/mqtt/status", nil)
	rec := httptest.NewRecorder()
	srv.handleMqttStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp MqttStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.WatchdogLogDropCount != 413 {
		t.Errorf("WatchdogLogDropCount = %d, want 413; body=%s", resp.WatchdogLogDropCount, rec.Body.String())
	}
	// Sanity: the two pre-existing watchdog fields must still round-trip
	// alongside the new one (no accidental field clobbering).
	if resp.WatchdogLastTickUnix != 1752841800 {
		t.Errorf("WatchdogLastTickUnix = %d, want 1752841800; body=%s", resp.WatchdogLastTickUnix, rec.Body.String())
	}
}
