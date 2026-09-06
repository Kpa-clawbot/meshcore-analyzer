package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Issue #1290 (MAJOR-2, adversarial review of PR #1624) — tri-state badge.
//
// The badge surface needs to distinguish three states:
//  1. legacy observer (never sent `repeat` field) → unknown → no badge
//  2. firmware confirmed `repeat:on`              → "Repeater"
//  3. firmware confirmed `repeat:off`             → "Listener"
//
// Previously `CanRelay bool` defaulted to false in Go even when the row
// was the legacy DEFAULT 1, conflating "confirmed repeater" with
// "unknown". This pins the API surface to *bool + JSON omitempty so the
// frontend tri-state render works.
func TestObservers_CanRelayTriState_Issue1290(t *testing.T) {
	srv, router := setupTestServer(t)

	// Add the can_relay column (matches dbschema migration) PLUS the
	// can_relay_seen tracking column so the read layer can distinguish
	// "ingestor explicitly wrote a value" from "default sentinel".
	for _, ddl := range []string{
		`ALTER TABLE observers ADD COLUMN can_relay INTEGER DEFAULT 1`,
		`ALTER TABLE observers ADD COLUMN can_relay_seen INTEGER DEFAULT 0`,
	} {
		if _, err := srv.store.db.conn.Exec(ddl); err != nil {
			t.Fatalf("alter: %v", err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	// Legacy: never received repeat field. can_relay=DEFAULT 1, seen=0.
	if _, err := srv.store.db.conn.Exec(
		`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		 VALUES ('legacy-obs', 'Legacy', 'SJC', ?, '2026-01-01T00:00:00Z', 1)`, now); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	// Repeater: ingestor wrote can_relay=1, seen=1.
	if _, err := srv.store.db.conn.Exec(
		`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, can_relay, can_relay_seen)
		 VALUES ('rep-obs', 'Repeater', 'SFO', ?, '2026-01-01T00:00:00Z', 1, 1, 1)`, now); err != nil {
		t.Fatalf("seed repeater: %v", err)
	}
	// Listener: ingestor wrote can_relay=0, seen=1.
	if _, err := srv.store.db.conn.Exec(
		`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, can_relay, can_relay_seen)
		 VALUES ('lst-obs', 'Listener', 'OAK', ?, '2026-01-01T00:00:00Z', 1, 0, 1)`, now); err != nil {
		t.Fatalf("seed listener: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/observers?nocache=1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var body struct {
		Observers []map[string]interface{} `json:"observers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}

	rows := map[string]map[string]interface{}{}
	for _, o := range body.Observers {
		if id, _ := o["id"].(string); id != "" {
			rows[id] = o
		}
	}

	// Legacy: can_relay key must be absent (JSON omitempty for nil *bool).
	legacy, ok := rows["legacy-obs"]
	if !ok {
		ids := make([]string, 0, len(rows))
		for k := range rows {
			ids = append(ids, k)
		}
		t.Fatalf("legacy-obs missing from response; got ids: %v", ids)
	}
	if _, has := legacy["can_relay"]; has {
		t.Errorf("legacy observer (never sent repeat) should have can_relay omitted (unknown); got can_relay=%v", legacy["can_relay"])
	}

	// Repeater: can_relay must be true.
	if v := rows["rep-obs"]["can_relay"]; v != true {
		t.Errorf("repeater observer: expected can_relay=true, got %v", v)
	}
	// Listener: can_relay must be false.
	if v, has := rows["lst-obs"]["can_relay"]; !has || v != false {
		t.Errorf("listener observer: expected can_relay=false, got %v (present=%v)", v, has)
	}

	// And the raw JSON must not contain the legacy observer's can_relay key
	// (defense against a future ObserverResp change that hardcodes false).
	raw := w.Body.String()
	if idx := strings.Index(raw, `"id":"legacy-obs"`); idx >= 0 {
		// scan its row only — observers are JSON-array-ordered objects.
		end := strings.Index(raw[idx:], "}")
		if end > 0 {
			rowStr := raw[idx : idx+end]
			if strings.Contains(rowStr, `"can_relay"`) {
				t.Errorf("legacy observer raw JSON unexpectedly contains can_relay key: %s", rowStr)
			}
		}
	}
}
