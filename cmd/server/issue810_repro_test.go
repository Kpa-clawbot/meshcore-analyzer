package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestRepro810 reproduces #810: when the longest-path observation has NULL
// resolved_path but a shorter-path observation has one, fetchResolvedPathForTxBest
// returns nil → /api/nodes/{pk}/health.recentPackets[].resolved_path is missing
// while /api/packets shows it.
func TestRepro810(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count) VALUES ('obs1','O1',?, '2026-01-01T00:00:00Z', 100)`, recent)
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count) VALUES ('obs2','O2',?, '2026-01-01T00:00:00Z', 100)`, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count) VALUES ('aabbccdd11223344','R','repeater',?, '2026-01-01T00:00:00Z', 1)`, recent)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json) VALUES ('AABB','testhash00000001',?,1,4,'{"pubKey":"aabbccdd11223344","type":"ADVERT"}')`, recent)
	// Longest-path obs WITHOUT resolved_path
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (1,1,12.5,-90,'["aa","bb","cc"]',?)`, recentEpoch)
	// Shorter-path obs WITH resolved_path
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp, resolved_path) VALUES (1,2,8.0,-95,'["aa","bb"]',?,'["aabbccdd11223344","eeff00112233aabb"]')`, recentEpoch-100)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// Sanity: /api/packets should show resolved_path for this tx.
	reqP := httptest.NewRequest("GET", "/api/packets?limit=10", nil)
	wP := httptest.NewRecorder()
	router.ServeHTTP(wP, reqP)
	var pktsBody map[string]interface{}
	json.Unmarshal(wP.Body.Bytes(), &pktsBody)
	pkts, _ := pktsBody["packets"].([]interface{})
	hasOnPackets := false
	for _, p := range pkts {
		pm := p.(map[string]interface{})
		if pm["hash"] == "testhash00000001" && pm["resolved_path"] != nil {
			hasOnPackets = true
		}
	}
	if !hasOnPackets {
		t.Fatal("precondition: /api/packets must report resolved_path for tx")
	}

	req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	rp, _ := body["recentPackets"].([]interface{})
	if len(rp) == 0 {
		t.Fatal("no recentPackets")
	}
	for _, p := range rp {
		pm := p.(map[string]interface{})
		if pm["hash"] == "testhash00000001" {
			if pm["resolved_path"] == nil {
				t.Fatal("BUG #810: /health.recentPackets resolved_path is nil despite /api/packets reporting it")
			}
			return
		}
	}
	t.Fatal("tx not found in recentPackets")
}
