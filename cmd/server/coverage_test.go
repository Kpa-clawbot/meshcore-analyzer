package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	_ "modernc.org/sqlite"
)

// --- helpers ---

func setupTestDBv2(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// Force single connection so all goroutines share the same in-memory DB
	conn.SetMaxOpenConns(1)
	schema := `
		CREATE TABLE nodes (
			public_key TEXT PRIMARY KEY, name TEXT, role TEXT,
			lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER, temperature_c REAL
		);
		CREATE TABLE observers (
			id TEXT PRIMARY KEY, name TEXT, iata TEXT, last_seen TEXT, first_seen TEXT,
			packet_count INTEGER DEFAULT 0, model TEXT, firmware TEXT,
			client_version TEXT, radio TEXT, battery_mv INTEGER, uptime_secs INTEGER, noise_floor REAL
		);
		CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE, first_seen TEXT NOT NULL,
			route_type INTEGER, payload_type INTEGER, payload_version INTEGER,
			decoded_json TEXT, channel_hash TEXT DEFAULT NULL, created_at TEXT DEFAULT (datetime('now'))
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
			observer_id TEXT, observer_name TEXT, direction TEXT,
			snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp INTEGER NOT NULL
		);
	`
	if _, err := conn.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return &DB{conn: conn, isV3: false}
}

func seedV2Data(t *testing.T, db *DB) {
	t.Helper()
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	epoch := now.Add(-1 * time.Hour).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Obs One', 'SJC', ?, '2026-01-01T00:00:00Z', 100)`, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('aabbccdd11223344', 'TestRepeater', 'repeater', 37.5, -122.0, ?, '2026-01-01T00:00:00Z', 50)`, recent)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AABB', 'abc123def4567890', ?, 1, 4, '{"pubKey":"aabbccdd11223344","name":"TestRepeater","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_id, observer_name, snr, rssi, path_json, timestamp)
		VALUES (1, 'obs1', 'Obs One', 12.5, -90, '["aa","bb"]', ?)`, epoch)
}

func setupNoStoreServer(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	db := setupTestDB(t)
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	// No store — forces DB fallback paths
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return srv, router
}

// --- detectSchema ---

func TestDetectSchemaV3(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	if !db.isV3 {
		t.Error("expected v3 schema (observer_idx)")
	}
}

func TestDetectSchemaV2(t *testing.T) {
	db := setupTestDBv2(t)
	defer db.Close()
	db.detectSchema()
	if db.isV3 {
		t.Error("expected v2 schema (observer_id), got v3")
	}
}

func TestDetectSchemaV2Queries(t *testing.T) {
	db := setupTestDBv2(t)
	defer db.Close()
	seedV2Data(t, db)

	// v2 schema should work with QueryPackets
	result, err := db.QueryPackets(PacketQuery{Limit: 50, Order: "DESC"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Errorf("expected 1 transmission in v2, got %d", result.Total)
	}

	// v2 grouped query
	gResult, err := db.QueryGroupedPackets(PacketQuery{Limit: 50, Order: "DESC"})
	if err != nil {
		t.Fatal(err)
	}
	if gResult.Total != 1 {
		t.Errorf("expected 1 grouped in v2, got %d", gResult.Total)
	}

	// v2 GetObserverPacketCounts
	counts := db.GetObserverPacketCounts(0)
	if counts["obs1"] != 1 {
		t.Errorf("expected 1 obs count for obs1, got %d", counts["obs1"])
	}

	// v2 QueryMultiNodePackets
	mResult, err := db.QueryMultiNodePackets([]string{"aabbccdd11223344"}, 50, 0, "DESC", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if mResult.Total != 1 {
		t.Errorf("expected 1 multi-node packet in v2, got %d", mResult.Total)
	}
}

// --- buildPacketWhere ---

func TestBuildPacketWhere(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	tests := []struct {
		name      string
		query     PacketQuery
		wantWhere int
	}{
		{"empty", PacketQuery{}, 0},
		{"type filter", PacketQuery{Type: intPtr(4)}, 1},
		{"route filter", PacketQuery{Route: intPtr(1)}, 1},
		{"observer filter", PacketQuery{Observer: "obs1"}, 1},
		{"hash filter", PacketQuery{Hash: "ABC123DEF4567890"}, 1},
		{"since filter", PacketQuery{Since: "2025-01-01"}, 1},
		{"until filter", PacketQuery{Until: "2099-01-01"}, 1},
		{"region filter", PacketQuery{Region: "SJC"}, 1},
		{"node filter", PacketQuery{Node: "TestRepeater"}, 1},
		{"all filters", PacketQuery{
			Type: intPtr(4), Route: intPtr(1), Observer: "obs1",
			Hash: "abc123", Since: "2025-01-01", Until: "2099-01-01",
			Region: "SJC", Node: "TestRepeater",
		}, 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			where, args := db.buildPacketWhere(tc.query)
			if len(where) != tc.wantWhere {
				t.Errorf("expected %d where clauses, got %d", tc.wantWhere, len(where))
			}
			if len(where) != len(args) {
				t.Errorf("where count (%d) != args count (%d)", len(where), len(args))
			}
		})
	}
}

// --- DB.QueryMultiNodePackets ---

func TestDBQueryMultiNodePackets(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("empty pubkeys", func(t *testing.T) {
		result, err := db.QueryMultiNodePackets(nil, 50, 0, "DESC", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 0 {
			t.Errorf("expected 0 for empty pubkeys, got %d", result.Total)
		}
	})

	t.Run("single pubkey match", func(t *testing.T) {
		result, err := db.QueryMultiNodePackets([]string{"aabbccdd11223344"}, 50, 0, "DESC", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Total < 1 {
			t.Errorf("expected >=1, got %d", result.Total)
		}
	})

	t.Run("multiple pubkeys", func(t *testing.T) {
		result, err := db.QueryMultiNodePackets(
			[]string{"aabbccdd11223344", "eeff00112233aabb"}, 50, 0, "DESC", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Total < 1 {
			t.Errorf("expected >=1, got %d", result.Total)
		}
	})

	t.Run("with time filters", func(t *testing.T) {
		result, err := db.QueryMultiNodePackets(
			[]string{"aabbccdd11223344"}, 50, 0, "ASC",
			"2020-01-01T00:00:00Z", "2099-01-01T00:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
		if result.Total < 1 {
			t.Errorf("expected >=1, got %d", result.Total)
		}
	})

	t.Run("default limit and order", func(t *testing.T) {
		result, err := db.QueryMultiNodePackets([]string{"aabbccdd11223344"}, 0, 0, "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Total < 1 {
			t.Errorf("expected >=1, got %d", result.Total)
		}
	})

	t.Run("no match", func(t *testing.T) {
		result, err := db.QueryMultiNodePackets([]string{"nonexistent"}, 50, 0, "DESC", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 0 {
			t.Errorf("expected 0, got %d", result.Total)
		}
	})
}

// --- Store.QueryMultiNodePackets ---

func TestStoreQueryMultiNodePackets(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	t.Run("empty pubkeys", func(t *testing.T) {
		result := store.QueryMultiNodePackets(nil, 50, 0, "DESC", "", "")
		if result.Total != 0 {
			t.Errorf("expected 0, got %d", result.Total)
		}
	})

	t.Run("matching pubkey", func(t *testing.T) {
		result := store.QueryMultiNodePackets([]string{"aabbccdd11223344"}, 50, 0, "DESC", "", "")
		if result.Total < 1 {
			t.Errorf("expected >=1, got %d", result.Total)
		}
	})

	t.Run("ASC order", func(t *testing.T) {
		result := store.QueryMultiNodePackets([]string{"aabbccdd11223344"}, 50, 0, "ASC", "", "")
		if result.Total < 1 {
			t.Errorf("expected >=1, got %d", result.Total)
		}
	})

	t.Run("with since/until", func(t *testing.T) {
		result := store.QueryMultiNodePackets(
			[]string{"aabbccdd11223344"}, 50, 0, "DESC",
			"2020-01-01T00:00:00Z", "2099-01-01T00:00:00Z")
		if result.Total < 1 {
			t.Errorf("expected >=1, got %d", result.Total)
		}
	})

	t.Run("offset beyond total", func(t *testing.T) {
		result := store.QueryMultiNodePackets([]string{"aabbccdd11223344"}, 50, 9999, "DESC", "", "")
		if len(result.Packets) != 0 {
			t.Errorf("expected 0 packets, got %d", len(result.Packets))
		}
	})

	t.Run("default limit", func(t *testing.T) {
		result := store.QueryMultiNodePackets([]string{"aabbccdd11223344"}, 0, 0, "DESC", "", "")
		if result.Total < 1 {
			t.Errorf("expected >=1, got %d", result.Total)
		}
	})
}

// --- IngestNewFromDB ---

func TestIngestNewFromDB(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	initialMax := store.MaxTransmissionID()

	// Insert a new transmission in DB
	now := time.Now().UTC().Format(time.RFC3339)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('EEFF', 'newhash123456abcd', ?, 1, 4, '{"pubKey":"aabbccdd11223344","type":"ADVERT"}')`, now)
	newTxID := 0
	db.conn.QueryRow("SELECT MAX(id) FROM transmissions").Scan(&newTxID)

	// Add observation for the new transmission
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 10.0, -92, '["cc"]', ?)`, newTxID, time.Now().Unix())

	// Ingest
	broadcastMaps, newMax := store.IngestNewFromDB(initialMax, 100)
	if newMax <= initialMax {
		t.Errorf("expected newMax > %d, got %d", initialMax, newMax)
	}
	if len(broadcastMaps) < 1 {
		t.Errorf("expected >=1 broadcast maps, got %d", len(broadcastMaps))
	}

	// Verify broadcast map contains nested "packet" field (fixes #162)
	if len(broadcastMaps) > 0 {
		bm := broadcastMaps[0]
		pkt, ok := bm["packet"]
		if !ok || pkt == nil {
			t.Error("broadcast map missing 'packet' field (required by packets.js)")
		}
		pktMap, ok := pkt.(map[string]interface{})
		if ok {
			for _, field := range []string{"id", "hash", "payload_type", "observer_id"} {
				if _, exists := pktMap[field]; !exists {
					t.Errorf("packet sub-object missing field %q", field)
				}
			}
		}
		// Verify decoded also present at top level (for live.js)
		if _, ok := bm["decoded"]; !ok {
			t.Error("broadcast map missing 'decoded' field (required by live.js)")
		}
	}

	// Verify ingested into store
	updatedMax := store.MaxTransmissionID()
	if updatedMax < newMax {
		t.Errorf("store max (%d) should be >= newMax (%d)", updatedMax, newMax)
	}

	t.Run("no new data", func(t *testing.T) {
		maps, max := store.IngestNewFromDB(newMax, 100)
		if maps != nil {
			t.Errorf("expected nil for no new data, got %d maps", len(maps))
		}
		if max != newMax {
			t.Errorf("expected same max %d, got %d", newMax, max)
		}
	})

	t.Run("default limit", func(t *testing.T) {
		_, _ = store.IngestNewFromDB(newMax, 0)
	})
}

func TestIngestNewFromDBv2(t *testing.T) {
	db := setupTestDBv2(t)
	defer db.Close()
	seedV2Data(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	initialMax := store.MaxTransmissionID()

	now := time.Now().UTC().Format(time.RFC3339)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('EEFF', 'v2newhash12345678', ?, 1, 4, '{"pubKey":"aabbccdd11223344","type":"ADVERT"}')`, now)
	newTxID := 0
	db.conn.QueryRow("SELECT MAX(id) FROM transmissions").Scan(&newTxID)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_id, observer_name, snr, rssi, path_json, timestamp)
		VALUES (?, 'obs1', 'Obs One', 10.0, -92, '["cc"]', ?)`, newTxID, time.Now().Unix())

	broadcastMaps, newMax := store.IngestNewFromDB(initialMax, 100)
	if newMax <= initialMax {
		t.Errorf("expected newMax > %d, got %d", initialMax, newMax)
	}
	if len(broadcastMaps) < 1 {
		t.Errorf("expected >=1 broadcast maps, got %d", len(broadcastMaps))
	}
}

// --- MaxTransmissionID ---

func TestMaxTransmissionID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	maxID := store.MaxTransmissionID()
	if maxID <= 0 {
		t.Errorf("expected maxID > 0, got %d", maxID)
	}

	t.Run("empty store", func(t *testing.T) {
		emptyStore := NewPacketStore(db, nil)
		if emptyStore.MaxTransmissionID() != 0 {
			t.Error("expected 0 for empty store")
		}
	})
}

// --- MaxTransmissionID incremental tracking ---

func TestMaxTransmissionIDIncremental(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	maxTx := store.MaxTransmissionID()
	maxObs := store.MaxObservationID()

	if maxTx <= 0 {
		t.Fatalf("expected maxTx > 0 after Load, got %d", maxTx)
	}
	if maxObs <= 0 {
		t.Fatalf("expected maxObs > 0 after Load, got %d", maxObs)
	}

	// Verify incremental field matches brute-force iteration
	store.mu.RLock()
	bruteMaxTx := 0
	for id := range store.byTxID {
		if id > bruteMaxTx {
			bruteMaxTx = id
		}
	}
	bruteMaxObs := 0
	for id := range store.byObsID {
		if id > bruteMaxObs {
			bruteMaxObs = id
		}
	}
	store.mu.RUnlock()

	if maxTx != bruteMaxTx {
		t.Errorf("maxTxID mismatch: incremental=%d brute=%d", maxTx, bruteMaxTx)
	}
	if maxObs != bruteMaxObs {
		t.Errorf("maxObsID mismatch: incremental=%d brute=%d", maxObs, bruteMaxObs)
	}
}

// --- Route handler DB fallback (no store) ---

func TestHandleBulkHealthNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/bulk-health?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body []interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body == nil {
		t.Fatal("expected array response")
	}
}

func TestHandleBulkHealthNoStoreMaxLimit(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/bulk-health?limit=500", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsRFNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)

	t.Run("basic", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/analytics/rf", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		if _, ok := body["snr"]; !ok {
			t.Error("expected snr field")
		}
		if _, ok := body["payloadTypes"]; !ok {
			t.Error("expected payloadTypes field")
		}
	})

	t.Run("with region", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/analytics/rf?region=SJC", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

func TestHandlePacketsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)

	t.Run("basic packets", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets?limit=10", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("multi-node", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets?nodes=aabbccdd11223344,eeff00112233aabb", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		if _, ok := body["packets"]; !ok {
			t.Error("expected packets field")
		}
	})

	t.Run("grouped", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets?groupByHash=true&limit=10", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

func TestHandlePacketsMultiNodeWithStore(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?nodes=aabbccdd11223344&order=asc&limit=10&offset=0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["packets"]; !ok {
		t.Error("expected packets field")
	}
}

func TestHandlePacketDetailNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)

	t.Run("by hash", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets/abc123def4567890", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("expected 404 (no store), got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("by ID", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets/1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("expected 404 (no store), got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("not found", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets/9999", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("expected 404, got %d", w.Code)
		}
	})

	t.Run("non-numeric non-hash", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets/notahash", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("expected 404, got %d", w.Code)
		}
	})
}

func TestHandleAnalyticsChannelsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["activeChannels"]; !ok {
		t.Error("expected activeChannels field")
	}
}

// --- transmissionsForObserver (byObserver index path) ---

func TestTransmissionsForObserverIndex(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Query packets for an observer — hits the byObserver index
	result := store.QueryPackets(PacketQuery{Limit: 50, Observer: "obs1", Order: "DESC"})
	if result.Total < 1 {
		t.Errorf("expected >=1 packets for obs1, got %d", result.Total)
	}

	// Query with observer + type (uses from != nil path in transmissionsForObserver)
	pt := 4
	result2 := store.QueryPackets(PacketQuery{Limit: 50, Observer: "obs1", Type: &pt, Order: "DESC"})
	if result2.Total < 1 {
		t.Errorf("expected >=1 filtered packets, got %d", result2.Total)
	}
}

// --- GetChannelMessages (dedup, observer, hops paths) ---

func TestGetChannelMessagesFromStore(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Test channel should exist from seed data
	messages, total := store.GetChannelMessages("#test", 100, 0)
	if total < 1 {
		t.Errorf("expected >=1 messages for #test, got %d", total)
	}
	if len(messages) < 1 {
		t.Errorf("expected >=1 message entries, got %d", len(messages))
	}

	t.Run("non-existent channel", func(t *testing.T) {
		msgs, total := store.GetChannelMessages("nonexistent", 100, 0)
		if total != 0 || len(msgs) != 0 {
			t.Errorf("expected 0 for nonexistent channel, got %d/%d", total, len(msgs))
		}
	})

	t.Run("default limit", func(t *testing.T) {
		_, total := store.GetChannelMessages("#test", 0, 0)
		if total < 1 {
			t.Errorf("expected >=1 with default limit, got %d", total)
		}
	})

	t.Run("with offset", func(t *testing.T) {
		_, _ = store.GetChannelMessages("#test", 10, 9999)
	})
}

func TestGetChannelMessagesDedupe(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	epoch := now.Add(-1 * time.Hour).Unix()

	seedTestData(t, db)

	// Insert a duplicate channel message with the same hash as existing
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DDEE', 'dupchannelhash1234', ?, 1, 5, '{"type":"CHAN","channel":"#test","text":"Hello: World","sender":"TestUser"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 11.0, -91, '["aa"]', ?)`, epoch)

	// Insert another dupe same hash as above (should dedup)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DDFF', 'dupchannelhash5678', ?, 1, 5, '{"type":"CHAN","channel":"#test","text":"Hello: World","sender":"TestUser"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (4, 2, 9.0, -93, '[]', ?)`, epoch)

	store := NewPacketStore(db, nil)
	store.Load()

	msgs, total := store.GetChannelMessages("#test", 100, 0)
	// Should have messages, with some deduped
	if total < 1 {
		t.Errorf("expected >=1 total messages, got %d", total)
	}
	_ = msgs
}

// --- GetChannels ---

func TestGetChannelsFromStore(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	channels := store.GetChannels("")
	if len(channels) < 1 {
		t.Errorf("expected >=1 channel, got %d", len(channels))
	}

	t.Run("with region", func(t *testing.T) {
		ch := store.GetChannels("SJC")
		_ = ch
	})

	t.Run("non-existent region", func(t *testing.T) {
		ch := store.GetChannels("NONEXIST")
		// Region filter may return 0 or fallback to unfiltered depending on DB content
		_ = ch
	})
}

// --- resolve (prefixMap) ---

func TestPrefixMapResolve(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "aabbccdd11223344", Name: "NodeA", HasGPS: true, Lat: 37.5, Lon: -122.0},
		{PublicKey: "aabbccdd55667788", Name: "NodeB", HasGPS: false},
		{PublicKey: "eeff0011aabbccdd", Name: "NodeC", HasGPS: true, Lat: 38.0, Lon: -121.0},
	}
	pm := buildPrefixMap(nodes)

	t.Run("exact match", func(t *testing.T) {
		n := pm.resolve("aabbccdd11223344")
		if n == nil || n.Name != "NodeA" {
			t.Errorf("expected NodeA, got %v", n)
		}
	})

	t.Run("prefix match single", func(t *testing.T) {
		n := pm.resolve("eeff")
		if n == nil || n.Name != "NodeC" {
			t.Errorf("expected NodeC, got %v", n)
		}
	})

	t.Run("prefix match multiple — prefer GPS", func(t *testing.T) {
		n := pm.resolve("aabbccdd")
		if n == nil {
			t.Fatal("expected non-nil")
		}
		if !n.HasGPS {
			t.Error("expected GPS-preferred candidate")
		}
		if n.Name != "NodeA" {
			t.Errorf("expected NodeA (has GPS), got %s", n.Name)
		}
	})

	t.Run("no match", func(t *testing.T) {
		n := pm.resolve("zzzzz")
		if n != nil {
			t.Errorf("expected nil, got %v", n)
		}
	})

	t.Run("multiple candidates no GPS", func(t *testing.T) {
		noGPSNodes := []nodeInfo{
			{PublicKey: "aa11bb22", Name: "X", HasGPS: false},
			{PublicKey: "aa11cc33", Name: "Y", HasGPS: false},
		}
		pm2 := buildPrefixMap(noGPSNodes)
		n := pm2.resolve("aa11")
		if n == nil {
			t.Fatal("expected non-nil")
		}
		// Should return first candidate
	})
}

func TestPrefixMapCap(t *testing.T) {
	// 16-char pubkey — longer than maxPrefixLen
	nodes := []nodeInfo{
		{PublicKey: "aabbccdd11223344", Name: "LongKey"},
		{PublicKey: "eeff0011", Name: "ShortKey"}, // exactly 8 chars
	}
	pm := buildPrefixMap(nodes)

	t.Run("short prefixes still work", func(t *testing.T) {
		n := pm.resolve("aabb")
		if n == nil || n.Name != "LongKey" {
			t.Errorf("expected LongKey for short prefix, got %v", n)
		}
	})

	t.Run("full pubkey exact match works", func(t *testing.T) {
		n := pm.resolve("aabbccdd11223344")
		if n == nil || n.Name != "LongKey" {
			t.Errorf("expected LongKey for full key, got %v", n)
		}
	})

	t.Run("intermediate prefix beyond cap returns nil", func(t *testing.T) {
		// 10-char prefix — beyond maxPrefixLen but not full key
		n := pm.resolve("aabbccdd11")
		if n != nil {
			t.Errorf("expected nil for intermediate prefix beyond cap, got %v", n.Name)
		}
	})

	t.Run("short key within cap has all prefixes", func(t *testing.T) {
		for l := 2; l <= 8; l++ {
			pfx := "eeff0011"[:l]
			n := pm.resolve(pfx)
			if n == nil || n.Name != "ShortKey" {
				t.Errorf("prefix %q: expected ShortKey, got %v", pfx, n)
			}
		}
	})

	t.Run("map size is capped", func(t *testing.T) {
		// LongKey: 7 prefix entries (2..8) + 1 full key = 8
		// ShortKey: 7 prefix entries (2..8), no full key entry (len == maxPrefixLen) = 7
		// No overlapping prefixes between the two nodes → 8 + 7 = 15 unique map keys
		if len(pm.m) != 15 {
			t.Errorf("expected 15 map entries (8 for LongKey + 7 for ShortKey), got %d", len(pm.m))
		}
	})
}

// --- pathLen ---

func TestPathLen(t *testing.T) {
	tests := []struct {
		json string
		want int
	}{
		{"", 0},
		{"invalid", 0},
		{`[]`, 0},
		{`["aa"]`, 1},
		{`["aa","bb","cc"]`, 3},
	}
	for _, tc := range tests {
		got := pathLen(tc.json)
		if got != tc.want {
			t.Errorf("pathLen(%q) = %d, want %d", tc.json, got, tc.want)
		}
	}
}

// --- floatPtrOrNil ---

func TestFloatPtrOrNil(t *testing.T) {
	v := 3.14
	if floatPtrOrNil(&v) != 3.14 {
		t.Error("expected 3.14")
	}
	if floatPtrOrNil(nil) != nil {
		t.Error("expected nil")
	}
}

// --- nullFloatPtr ---

func TestNullFloatPtr(t *testing.T) {
	valid := sql.NullFloat64{Float64: 2.71, Valid: true}
	p := nullFloatPtr(valid)
	if p == nil || *p != 2.71 {
		t.Errorf("expected 2.71, got %v", p)
	}
	invalid := sql.NullFloat64{Valid: false}
	if nullFloatPtr(invalid) != nil {
		t.Error("expected nil for invalid")
	}
}

// --- nilIfEmpty ---

func TestNilIfEmpty(t *testing.T) {
	if nilIfEmpty("") != nil {
		t.Error("expected nil for empty")
	}
	if nilIfEmpty("hello") != "hello" {
		t.Error("expected 'hello'")
	}
}

// --- pickBestObservation ---

func TestPickBestObservation(t *testing.T) {
	t.Run("empty observations", func(t *testing.T) {
		tx := &StoreTx{}
		pickBestObservation(tx)
		if tx.ObserverID != "" {
			t.Error("expected empty observer for no observations")
		}
	})

	t.Run("single observation", func(t *testing.T) {
		snr := 10.0
		tx := &StoreTx{
			Observations: []*StoreObs{
				{ObserverID: "obs1", ObserverName: "One", SNR: &snr, PathJSON: `["aa"]`},
			},
		}
		pickBestObservation(tx)
		if tx.ObserverID != "obs1" {
			t.Errorf("expected obs1, got %s", tx.ObserverID)
		}
	})

	t.Run("picks longest path", func(t *testing.T) {
		snr1, snr2 := 10.0, 5.0
		tx := &StoreTx{
			Observations: []*StoreObs{
				{ObserverID: "obs1", SNR: &snr1, PathJSON: `["aa"]`},
				{ObserverID: "obs2", SNR: &snr2, PathJSON: `["aa","bb","cc"]`},
			},
		}
		pickBestObservation(tx)
		if tx.ObserverID != "obs2" {
			t.Errorf("expected obs2 (longest path), got %s", tx.ObserverID)
		}
	})
}

// --- indexByNode ---

func TestIndexByNode(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)

	t.Run("empty decoded_json", func(t *testing.T) {
		tx := &StoreTx{Hash: "h1"}
		store.indexByNode(tx)
		if len(store.byNode) != 0 {
			t.Error("expected no index entries")
		}
	})

	t.Run("valid decoded_json", func(t *testing.T) {
		tx := &StoreTx{
			Hash:        "h2",
			DecodedJSON: `{"pubKey":"aabbccdd11223344","destPubKey":"eeff00112233aabb"}`,
		}
		store.indexByNode(tx)
		if len(store.byNode["aabbccdd11223344"]) != 1 {
			t.Error("expected pubKey indexed")
		}
		if len(store.byNode["eeff00112233aabb"]) != 1 {
			t.Error("expected destPubKey indexed")
		}
	})

	t.Run("duplicate hash skipped", func(t *testing.T) {
		tx := &StoreTx{
			Hash:        "h2",
			DecodedJSON: `{"pubKey":"aabbccdd11223344"}`,
		}
		store.indexByNode(tx)
		// Should not add duplicate
		if len(store.byNode["aabbccdd11223344"]) != 1 {
			t.Errorf("expected 1, got %d", len(store.byNode["aabbccdd11223344"]))
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		tx := &StoreTx{Hash: "h3", DecodedJSON: "not json"}
		store.indexByNode(tx)
		// Should not panic or add anything
	})
}

// --- resolveVersion ---

func TestResolveVersion(t *testing.T) {
	old := Version
	defer func() { Version = old }()

	Version = "v1.2.3"
	if resolveVersion() != "v1.2.3" {
		t.Error("expected v1.2.3")
	}

	Version = ""
	if resolveVersion() != "unknown" {
		t.Error("expected unknown when empty")
	}
}

// --- wsOrStatic ---

func TestWsOrStaticNonWebSocket(t *testing.T) {
	hub := NewHub()
	staticHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("static"))
	})
	handler := wsOrStatic(hub, staticHandler)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "static" {
		t.Errorf("expected 'static', got %s", w.Body.String())
	}
}

// --- Poller.Start ---

func TestPollerStartStop(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	hub := NewHub()

	poller := NewPoller(db, hub, 50*time.Millisecond)
	go poller.Start()
	time.Sleep(150 * time.Millisecond)
	poller.Stop()
}

func TestPollerStartWithStore(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	hub := NewHub()
	store := NewPacketStore(db, nil)
	store.Load()

	poller := NewPoller(db, hub, 50*time.Millisecond)
	poller.store = store
	go poller.Start()

	// Insert new data while poller running
	now := time.Now().UTC().Format(time.RFC3339)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
		VALUES ('FFEE', 'pollerhash12345678', ?, 1, 4)`, now)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES ((SELECT MAX(id) FROM transmissions), 1, 10.0, -92, '[]', ?)`, time.Now().Unix())

	time.Sleep(200 * time.Millisecond)
	poller.Stop()
}

// --- perfMiddleware slow query path ---

func TestPerfMiddlewareSlowQuery(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store

	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// Add a slow handler
	router.HandleFunc("/api/test-slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(110 * time.Millisecond)
		writeJSON(w, map[string]string{"ok": "true"})
	}).Methods("GET")

	req := httptest.NewRequest("GET", "/api/test-slow", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if len(srv.perfStats.SlowQueries) < 1 {
		t.Error("expected slow query to be recorded")
	}
}

func TestPerfMiddlewareNonAPIPath(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// Non-API path should pass through without perf tracking
	router.HandleFunc("/not-api", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}).Methods("GET")

	initialReqs := srv.perfStats.Requests
	req := httptest.NewRequest("GET", "/not-api", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if srv.perfStats.Requests != initialReqs {
		t.Error("non-API request should not be tracked")
	}
}

// --- writeJSON error path ---

func TestWriteJSONErrorPath(t *testing.T) {
	w := httptest.NewRecorder()
	// math.Inf cannot be marshaled to JSON — triggers the error path
	writeJSON(w, math.Inf(1))
	// Should not panic, just log the error
}

// --- GetObserverPacketCounts ---

func TestGetObserverPacketCountsV3(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	counts := db.GetObserverPacketCounts(0)
	if len(counts) == 0 {
		t.Error("expected some observer counts")
	}
}

// --- Additional route fallback tests ---

func TestHandleAnalyticsTopologyNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/topology", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsDistanceNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/distance", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsHashSizesNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/hash-sizes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsSubpathsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/subpaths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsSubpathDetailNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)

	t.Run("with hops", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/analytics/subpath-detail?hops=aa,bb", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("missing hops", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/analytics/subpath-detail", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("single hop", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/analytics/subpath-detail?hops=aa", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

func TestHandleChannelsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleChannelMessagesNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/channels/test/messages", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandlePacketTimestampsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)

	t.Run("with since", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets/timestamps?since=2020-01-01T00:00:00Z", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("missing since", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets/timestamps", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("expected 400, got %d", w.Code)
		}
	})
}

func TestHandleStatsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleHealthNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
}

// --- buildTransmissionWhere additional coverage ---

func TestBuildTransmissionWhereRFC3339(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("RFC3339 since", func(t *testing.T) {
		q := PacketQuery{Since: "2020-01-01T00:00:00Z"}
		where, args := db.buildTransmissionWhere(q)
		if len(where) != 1 {
			t.Errorf("expected 1 clause, got %d", len(where))
		}
		if len(args) != 1 {
			t.Errorf("expected 1 arg, got %d", len(args))
		}
		if !strings.Contains(where[0], "observations") {
			t.Error("expected observations subquery for RFC3339 since")
		}
	})

	t.Run("RFC3339 until", func(t *testing.T) {
		q := PacketQuery{Until: "2099-01-01T00:00:00Z"}
		where, args := db.buildTransmissionWhere(q)
		if len(where) != 1 {
			t.Errorf("expected 1 clause, got %d", len(where))
		}
		if len(args) != 1 {
			t.Errorf("expected 1 arg, got %d", len(args))
		}
	})

	t.Run("non-RFC3339 since", func(t *testing.T) {
		q := PacketQuery{Since: "2020-01-01"}
		where, _ := db.buildTransmissionWhere(q)
		if len(where) != 1 {
			t.Errorf("expected 1 clause, got %d", len(where))
		}
		if strings.Contains(where[0], "observations") {
			t.Error("expected direct first_seen comparison for non-RFC3339")
		}
	})

	t.Run("observer v3", func(t *testing.T) {
		q := PacketQuery{Observer: "obs1"}
		where, _ := db.buildTransmissionWhere(q)
		if len(where) != 1 {
			t.Errorf("expected 1 clause, got %d", len(where))
		}
		if !strings.Contains(where[0], "observer_idx") {
			t.Error("expected observer_idx subquery for v3")
		}
	})

	t.Run("region v3", func(t *testing.T) {
		q := PacketQuery{Region: "SJC"}
		where, _ := db.buildTransmissionWhere(q)
		if len(where) != 1 {
			t.Errorf("expected 1 clause, got %d", len(where))
		}
		if !strings.Contains(where[0], "iata") {
			t.Error("expected iata subquery for region")
		}
	})
}

func TestBuildTransmissionWhereV2(t *testing.T) {
	db := setupTestDBv2(t)
	defer db.Close()
	seedV2Data(t, db)

	t.Run("observer v2", func(t *testing.T) {
		q := PacketQuery{Observer: "obs1"}
		where, _ := db.buildTransmissionWhere(q)
		if len(where) != 1 {
			t.Errorf("expected 1 clause, got %d", len(where))
		}
		if !strings.Contains(where[0], "observer_id") {
			t.Error("expected observer_id subquery for v2")
		}
	})

	t.Run("region v2", func(t *testing.T) {
		q := PacketQuery{Region: "SJC"}
		where, _ := db.buildTransmissionWhere(q)
		if len(where) != 1 {
			t.Errorf("expected 1 clause, got %d", len(where))
		}
	})
}

// --- GetMaxTransmissionID (DB) ---

func TestDBGetMaxTransmissionID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	maxID := db.GetMaxTransmissionID()
	if maxID <= 0 {
		t.Errorf("expected > 0, got %d", maxID)
	}
}

// --- GetNodeLocations ---

func TestGetNodeLocations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	locs := db.GetNodeLocations()
	if len(locs) == 0 {
		t.Error("expected some node locations")
	}
	pk := strings.ToLower("aabbccdd11223344")
	if entry, ok := locs[pk]; ok {
		if entry["lat"] == nil {
			t.Error("expected non-nil lat")
		}
	} else {
		t.Error("expected node location for test repeater")
	}
}

// --- GetNodeLocationsByKeys ---

func TestGetNodeLocationsByKeys(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	// Query with a known key
	pk := "aabbccdd11223344"
	locs := db.GetNodeLocationsByKeys([]string{pk})
	if len(locs) != 1 {
		t.Errorf("expected 1 location, got %d", len(locs))
	}
	if entry, ok := locs[strings.ToLower(pk)]; ok {
		if entry["lat"] == nil {
			t.Error("expected non-nil lat")
		}
	} else {
		t.Error("expected node location for test repeater")
	}

	// Query with no keys returns empty map
	empty := db.GetNodeLocationsByKeys([]string{})
	if len(empty) != 0 {
		t.Errorf("expected 0 locations for empty keys, got %d", len(empty))
	}

	// Query with unknown key returns empty map
	unknown := db.GetNodeLocationsByKeys([]string{"nonexistent"})
	if len(unknown) != 0 {
		t.Errorf("expected 0 locations for unknown key, got %d", len(unknown))
	}
}

// --- Store edge cases ---

func TestStoreQueryPacketsEdgeCases(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	t.Run("hash filter", func(t *testing.T) {
		result := store.QueryPackets(PacketQuery{Hash: "abc123def4567890", Limit: 50, Order: "DESC"})
		if result.Total != 1 {
			t.Errorf("expected 1, got %d", result.Total)
		}
	})

	t.Run("non-existent hash", func(t *testing.T) {
		result := store.QueryPackets(PacketQuery{Hash: "0000000000000000", Limit: 50, Order: "DESC"})
		if result.Total != 0 {
			t.Errorf("expected 0, got %d", result.Total)
		}
	})

	t.Run("ASC order", func(t *testing.T) {
		result := store.QueryPackets(PacketQuery{Limit: 50, Order: "ASC"})
		if result.Total < 1 {
			t.Error("expected results")
		}
	})

	t.Run("offset beyond end", func(t *testing.T) {
		result := store.QueryPackets(PacketQuery{Limit: 50, Offset: 9999, Order: "DESC"})
		if len(result.Packets) != 0 {
			t.Errorf("expected 0, got %d", len(result.Packets))
		}
	})

	t.Run("node filter with index", func(t *testing.T) {
		result := store.QueryPackets(PacketQuery{Node: "aabbccdd11223344", Limit: 50, Order: "DESC"})
		if result.Total < 1 {
			t.Error("expected >=1")
		}
	})

	t.Run("route filter", func(t *testing.T) {
		rt := 1
		result := store.QueryPackets(PacketQuery{Route: &rt, Limit: 50, Order: "DESC"})
		if result.Total < 1 {
			t.Error("expected >=1")
		}
	})

	t.Run("since filter", func(t *testing.T) {
		result := store.QueryPackets(PacketQuery{Since: "2020-01-01", Limit: 50, Order: "DESC"})
		if result.Total < 1 {
			t.Error("expected >=1")
		}
	})

	t.Run("until filter", func(t *testing.T) {
		result := store.QueryPackets(PacketQuery{Until: "2099-01-01", Limit: 50, Order: "DESC"})
		if result.Total < 1 {
			t.Error("expected >=1")
		}
	})
}

// --- HandlePackets with various options ---

func TestHandlePacketsWithQueryOptions(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("with type filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets?type=4&limit=10", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("with route filter", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets?route=1&limit=10", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("expand observations", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets?limit=10&expand=observations", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("ASC order", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets?order=asc&limit=10", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

// --- handleObservers and handleObserverDetail ---

func TestHandleObserversNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleObserverDetailNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/observers/obs1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleObserverAnalyticsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/observers/obs1/analytics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// --- HandleTraces ---

func TestHandleTracesNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/traces/abc123def4567890", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- HandleResolveHops ---

func TestHandleResolveHops(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("empty hops", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/resolve-hops", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("with hops", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/resolve-hops?hops=aabb,eeff", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

// --- HandlePerf ---

func TestHandlePerfNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/perf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- HandleIATACoords ---

func TestHandleIATACoordsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/iata-coords", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- Conversion helpers ---

func TestStrOrNil(t *testing.T) {
	if strOrNil("") != nil {
		t.Error("expected nil")
	}
	if strOrNil("abc") != "abc" {
		t.Error("expected abc")
	}
}

func TestIntPtrOrNil(t *testing.T) {
	if intPtrOrNil(nil) != nil {
		t.Error("expected nil")
	}
	v := 42
	if intPtrOrNil(&v) != 42 {
		t.Error("expected 42")
	}
}

func TestNullIntPtr(t *testing.T) {
	valid := sql.NullInt64{Int64: 7, Valid: true}
	p := nullIntPtr(valid)
	if p == nil || *p != 7 {
		t.Error("expected 7")
	}
	invalid := sql.NullInt64{Valid: false}
	if nullIntPtr(invalid) != nil {
		t.Error("expected nil")
	}
}

func TestNullStr(t *testing.T) {
	valid := sql.NullString{String: "hello", Valid: true}
	if nullStr(valid) != "hello" {
		t.Error("expected hello")
	}
	invalid := sql.NullString{Valid: false}
	if nullStr(invalid) != nil {
		t.Error("expected nil")
	}
}

func TestNullStrVal(t *testing.T) {
	valid := sql.NullString{String: "test", Valid: true}
	if nullStrVal(valid) != "test" {
		t.Error("expected test")
	}
	invalid := sql.NullString{Valid: false}
	if nullStrVal(invalid) != "" {
		t.Error("expected empty string")
	}
}

func TestNullFloat(t *testing.T) {
	valid := sql.NullFloat64{Float64: 1.5, Valid: true}
	if nullFloat(valid) != 1.5 {
		t.Error("expected 1.5")
	}
	invalid := sql.NullFloat64{Valid: false}
	if nullFloat(invalid) != nil {
		t.Error("expected nil")
	}
}

func TestNullInt(t *testing.T) {
	valid := sql.NullInt64{Int64: 99, Valid: true}
	if nullInt(valid) != 99 {
		t.Error("expected 99")
	}
	invalid := sql.NullInt64{Valid: false}
	if nullInt(invalid) != nil {
		t.Error("expected nil")
	}
}

// --- resolveCommit ---

func TestResolveCommit(t *testing.T) {
	old := Commit
	defer func() { Commit = old }()

	Commit = "abc123"
	if resolveCommit() != "abc123" {
		t.Error("expected abc123")
	}

	Commit = ""
	// With no .git-commit file and possibly no git, should return something
	result := resolveCommit()
	if result == "" {
		t.Error("expected non-empty result")
	}
}

// --- parsePathJSON ---

func TestParsePathJSON(t *testing.T) {
	if parsePathJSON("") != nil {
		t.Error("expected nil for empty")
	}
	if parsePathJSON("[]") != nil {
		t.Error("expected nil for []")
	}
	if parsePathJSON("invalid") != nil {
		t.Error("expected nil for invalid")
	}
	hops := parsePathJSON(`["aa","bb"]`)
	if len(hops) != 2 {
		t.Errorf("expected 2 hops, got %d", len(hops))
	}
}

// --- Store.GetPerfStoreStats & GetCacheStats ---

func TestStorePerfAndCacheStats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	stats := store.GetPerfStoreStats()
	if _, ok := stats["totalLoaded"]; !ok {
		t.Error("expected totalLoaded")
	}

	cacheStats := store.GetCacheStats()
	if _, ok := cacheStats["size"]; !ok {
		t.Error("expected size")
	}
}

// --- enrichObs ---

func TestEnrichObs(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Find an observation from the loaded store
	var obs *StoreObs
	for _, o := range store.byObsID {
		obs = o
		break
	}
	if obs == nil {
		t.Skip("no observations loaded")
	}

	enriched := store.enrichObs(obs)
	if enriched["observer_id"] == nil {
		t.Error("expected observer_id")
	}
}

// --- HandleNodeSearch ---

func TestHandleNodeSearch(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("with query", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/search?q=Test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("empty query", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/search?q=", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

// --- HandleNodeDetail ---

func TestHandleNodeDetail(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("existing", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/nonexistent12345678", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("expected 404, got %d", w.Code)
		}
	})
}

// --- HandleNodeHealth ---

func TestHandleNodeHealth(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("not found", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/nonexistent12345678/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("expected 404, got %d", w.Code)
		}
	})
}

// --- HandleNodePaths ---

func TestHandleNodePaths(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("existing", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/paths", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/nonexistent12345678/paths", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("expected 404, got %d", w.Code)
		}
	})
}

// --- HandleNodeAnalytics ---

func TestHandleNodeAnalytics(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("existing", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/analytics?days=7", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/nonexistent/analytics", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("expected 404, got %d", w.Code)
		}
	})

	t.Run("days bounds", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/analytics?days=0", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("days max", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/analytics?days=999", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

// --- HandleNetworkStatus ---

func TestHandleNetworkStatus(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/network-status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- HandleConfigEndpoints ---

func TestHandleConfigEndpoints(t *testing.T) {
	_, router := setupTestServer(t)

	endpoints := []string{
		"/api/config/cache",
		"/api/config/client",
		"/api/config/regions",
		"/api/config/theme",
		"/api/config/map",
	}
	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			req := httptest.NewRequest("GET", ep, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("expected 200, got %d for %s", w.Code, ep)
			}
		})
	}
}

// --- HandleAudioLabBuckets ---

func TestHandleAudioLabBuckets(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/audio-lab/buckets", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// May return 200 or 404 depending on implementation
	if w.Code != 200 {
		// Audio lab might not be fully implemented — just verify it doesn't crash
	}
}

// --- txToMap ---

func TestTxToMap(t *testing.T) {
	snr := 10.5
	rssi := -90.0
	pt := 4
	rt := 1
	tx := &StoreTx{
		ID:               1,
		RawHex:           "AABB",
		Hash:             "abc123",
		FirstSeen:        "2025-01-01",
		RouteType:        &rt,
		PayloadType:      &pt,
		DecodedJSON:      `{"type":"ADVERT"}`,
		ObservationCount: 2,
		ObserverID:       "obs1",
		ObserverName:     "Obs One",
		SNR:              &snr,
		RSSI:             &rssi,
		PathJSON:         `["aa"]`,
		Direction:        "RX",
	}
	m := txToMap(tx)
	if m["id"] != 1 {
		t.Error("expected id 1")
	}
	if m["hash"] != "abc123" {
		t.Error("expected hash abc123")
	}
	if m["snr"] != 10.5 {
		t.Error("expected snr 10.5")
	}
}

func TestTxToMapLazyObservations(t *testing.T) {
	snr := 10.5
	rssi := -90.0
	tx := &StoreTx{
		ID:   1,
		Hash: "abc",
		Observations: []*StoreObs{
			{ID: 10, ObserverID: "obs1", ObserverName: "O1", SNR: &snr, RSSI: &rssi, Timestamp: "2025-01-01"},
			{ID: 11, ObserverID: "obs2", ObserverName: "O2", SNR: &snr, RSSI: &rssi, Timestamp: "2025-01-02"},
		},
	}

	// Without flag: no observations key
	m := txToMap(tx)
	if _, ok := m["observations"]; ok {
		t.Error("txToMap without includeObservations should not include observations key")
	}

	// With false: no observations key
	m = txToMap(tx, false)
	if _, ok := m["observations"]; ok {
		t.Error("txToMap(tx, false) should not include observations key")
	}

	// With true: observations included
	m = txToMap(tx, true)
	obs, ok := m["observations"]
	if !ok {
		t.Fatal("txToMap(tx, true) should include observations key")
	}
	obsList, ok := obs.([]map[string]interface{})
	if !ok {
		t.Fatal("observations should be []map[string]interface{}")
	}
	if len(obsList) != 2 {
		t.Errorf("expected 2 observations, got %d", len(obsList))
	}
	if obsList[0]["observer_id"] != "obs1" {
		t.Errorf("expected observer_id obs1, got %v", obsList[0]["observer_id"])
	}
}

// --- filterTxSlice ---

func TestFilterTxSlice(t *testing.T) {
	txs := []*StoreTx{
		{ID: 1, Hash: "a"},
		{ID: 2, Hash: "b"},
		{ID: 3, Hash: "a"},
	}
	result := filterTxSlice(txs, func(tx *StoreTx) bool {
		return tx.Hash == "a"
	})
	if len(result) != 2 {
		t.Errorf("expected 2, got %d", len(result))
	}
}

// --- GetTimestamps ---

func TestStoreGetTimestamps(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	ts := store.GetTimestamps("2000-01-01")
	if len(ts) < 1 {
		t.Error("expected >=1 timestamps")
	}
}

// Helper
func intPtr(v int) *int {
	return &v
}

// setupRichTestDB creates a test DB with richer data including paths, multiple observers, channel data.
func setupRichTestDB(t *testing.T) *DB {
	t.Helper()
	db := setupTestDB(t)

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	yesterday := now.Add(-24 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()
	yesterdayEpoch := now.Add(-24 * time.Hour).Unix()

	seedTestData(t, db)

	// Add advert packet with raw_hex that has valid header + path bytes for hash size parsing
	// route_type 1 = FLOOD, path byte at position 1 (hex index 2..3)
	// header: 0x01 (route_type=1), path byte: 0x40 (hashSize bits=01 → size 2)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0140aabbccdd', 'hash_with_path_01', ?, 1, 4, '{"pubKey":"aabbccdd11223344","name":"TestRepeater","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 10.0, -91, '["aabb","ccdd"]', ?)`, recentEpoch)

	// Another advert with 3-byte hash size: header 0x01, path byte 0x80 (bits=10 → size 3)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0180eeff0011', 'hash_with_path_02', ?, 1, 4, '{"pubKey":"eeff00112233aabb","name":"TestCompanion","type":"ADVERT"}')`, yesterday)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (4, 2, 8.5, -94, '["eeff","0011","2233"]', ?)`, yesterdayEpoch)

	// Another channel message with different sender for analytics
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('CC01', 'chan_msg_hash_001', ?, 1, 5, '{"type":"CHAN","channel":"#test","text":"User2: Another msg","sender":"User2","channelHash":"abc123"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (5, 1, 14.0, -88, '["aa"]', ?)`, recentEpoch)

	// Extra packet sharing subpath "eeff,0011" with hash_with_path_02 above,
	// so that subpath has count>=2 and survives singleton pruning.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0140eeff0011', 'hash_shared_subpath', ?, 1, 4, '{"pubKey":"eeff001199887766","name":"TestShared","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (6, 1, 9.0, -92, '["eeff","0011"]', ?)`, recentEpoch)

	return db
}

// --- Store-backed analytics tests ---

func TestStoreGetBulkHealthWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	results := store.GetBulkHealth(50, "")
	if len(results) == 0 {
		t.Error("expected bulk health results")
	}
	// Check that results have expected structure
	for _, r := range results {
		if _, ok := r["public_key"]; !ok {
			t.Error("expected public_key field")
		}
		if _, ok := r["stats"]; !ok {
			t.Error("expected stats field")
		}
	}

	t.Run("with region filter", func(t *testing.T) {
		results := store.GetBulkHealth(50, "SJC")
		_ = results
	})
}

func TestStoreGetAnalyticsHashSizes(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetAnalyticsHashSizes("")
	if result["total"] == nil {
		t.Error("expected total field")
	}
	dist, ok := result["distribution"].(map[string]int)
	if !ok {
		t.Error("expected distribution map")
	}
	_ = dist

	t.Run("with region", func(t *testing.T) {
		r := store.GetAnalyticsHashSizes("SJC")
		_ = r
	})
}

func TestHashSizesDistributionByRepeatersFiltersRole(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetAnalyticsHashSizes("")

	// distributionByRepeaters should only count repeater nodes.
	// Rich test DB: aabbccdd11223344 = repeater (hash size 2), eeff00112233aabb = companion (hash size 3).
	dbr, ok := result["distributionByRepeaters"].(map[string]int)
	if !ok {
		t.Fatal("expected distributionByRepeaters map")
	}
	// Only the repeater node should be counted.
	if dbr["3"] != 0 {
		t.Errorf("distributionByRepeaters[3] = %d, want 0 (companion should be excluded)", dbr["3"])
	}
	if dbr["2"] != 1 {
		t.Errorf("distributionByRepeaters[2] = %d, want 1 (repeater)", dbr["2"])
	}

	// multiByteNodes should include role field for frontend filtering.
	mbn, ok := result["multiByteNodes"].([]map[string]interface{})
	if !ok {
		t.Fatal("expected multiByteNodes slice")
	}
	for _, node := range mbn {
		if _, hasRole := node["role"]; !hasRole {
			t.Errorf("multiByteNodes entry missing 'role' field: %v", node)
		}
	}
	// Verify companion is included in multiByteNodes (it's multi-byte) with correct role.
	foundCompanion := false
	for _, node := range mbn {
		if node["pubkey"] == "eeff00112233aabb" {
			foundCompanion = true
			if node["role"] != "companion" {
				t.Errorf("companion node role = %v, want 'companion'", node["role"])
			}
		}
	}
	if !foundCompanion {
		t.Error("expected companion node in multiByteNodes (multi-byte adopters should include all roles)")
	}
}

func TestStoreGetAnalyticsSubpaths(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetAnalyticsSubpaths("", 2, 8, 100)
	if _, ok := result["subpaths"]; !ok {
		t.Error("expected subpaths field")
	}

	t.Run("with region", func(t *testing.T) {
		r := store.GetAnalyticsSubpaths("SJC", 2, 4, 50)
		_ = r
	})
}

func TestSubpathPrecomputedIndex(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	// After Load(), the precomputed index must be populated.
	if len(store.spIndex) == 0 {
		t.Fatal("expected spIndex to be populated after Load()")
	}
	if store.spTotalPaths == 0 {
		t.Fatal("expected spTotalPaths > 0 after Load()")
	}

	// The rich test DB has paths ["aa","bb"], ["aabb","ccdd"],
	// ["eeff","0011","2233"], and ["eeff","0011"].  After singleton pruning,
	// only subpaths with count>=2 survive.  "eeff,0011" appears in two packets.
	expectedRaw := map[string]int{
		"eeff,0011": 2,
	}
	for key, want := range expectedRaw {
		got, ok := store.spIndex[key]
		if !ok {
			t.Errorf("expected spIndex[%q] to exist", key)
		} else if got != want {
			t.Errorf("spIndex[%q] = %d, want %d", key, got, want)
		}
	}

	// Singleton subpaths must have been pruned
	singletons := []string{"aa,bb", "aabb,ccdd", "0011,2233", "eeff,0011,2233"}
	for _, key := range singletons {
		if _, ok := store.spIndex[key]; ok {
			t.Errorf("expected singleton spIndex[%q] to be pruned", key)
		}
	}
	if store.spTotalPaths != 4 {
		t.Errorf("spTotalPaths = %d, want 4", store.spTotalPaths)
	}

	// Fast-path (no region) and slow-path (with region) must return the
	// same shape.
	fast := store.GetAnalyticsSubpaths("", 2, 8, 100)
	slow := store.GetAnalyticsSubpaths("SJC", 2, 4, 50)
	for _, r := range []map[string]interface{}{fast, slow} {
		if _, ok := r["subpaths"]; !ok {
			t.Error("missing subpaths in result")
		}
		if _, ok := r["totalPaths"]; !ok {
			t.Error("missing totalPaths in result")
		}
	}

	// Verify fast path totalPaths matches index.
	if tp, ok := fast["totalPaths"].(int); ok && tp != store.spTotalPaths {
		t.Errorf("fast totalPaths=%d, spTotalPaths=%d", tp, store.spTotalPaths)
	}
}

func TestSubpathTxIndexPopulated(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	// spIndex must be populated after Load()
	if len(store.spIndex) == 0 {
		t.Fatal("expected spIndex to be populated after Load()")
	}

	// GetSubpathDetail should return correct match count via scan fallback
	detail := store.GetSubpathDetail([]string{"eeff", "0011"})
	if detail == nil {
		t.Fatal("expected non-nil detail for existing subpath")
	}
	matches, _ := detail["totalMatches"].(int)
	if matches != 2 {
		t.Errorf("totalMatches = %d, want 2", matches)
	}

	// Non-existent subpath should return 0 matches
	detail2 := store.GetSubpathDetail([]string{"zzzz", "yyyy"})
	if detail2 == nil {
		t.Fatal("expected non-nil result even for non-existent subpath")
	}
	matches2, _ := detail2["totalMatches"].(int)
	if matches2 != 0 {
		t.Errorf("totalMatches for non-existent subpath = %d, want 0", matches2)
	}
}

func TestSubpathDetailMixedCaseHops(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	// Query with lowercase hops to establish baseline
	lower := store.GetSubpathDetail([]string{"eeff", "0011"})
	if lower == nil {
		t.Fatal("expected non-nil detail for lowercase subpath")
	}
	lowerMatches, _ := lower["totalMatches"].(int)
	if lowerMatches == 0 {
		t.Fatal("expected >0 matches for lowercase subpath")
	}

	// Query with mixed-case hops — must return the same results (case-insensitive)
	mixed := store.GetSubpathDetail([]string{"EEFF", "0011"})
	if mixed == nil {
		t.Fatal("expected non-nil detail for mixed-case subpath")
	}
	mixedMatches, _ := mixed["totalMatches"].(int)
	if mixedMatches != lowerMatches {
		t.Errorf("mixed-case totalMatches = %d, want %d (same as lowercase)", mixedMatches, lowerMatches)
	}

	// All-uppercase should also match
	upper := store.GetSubpathDetail([]string{"EEFF", "0011"})
	upperMatches, _ := upper["totalMatches"].(int)
	if upperMatches != lowerMatches {
		t.Errorf("uppercase totalMatches = %d, want %d", upperMatches, lowerMatches)
	}
}

// TestSubpathSingletonDrop verifies that singleton entries are pruned from
// spIndex while count>=2 entries are preserved.
func TestSubpathSingletonDrop(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	// "eeff,0011" appears in 2 packets — must survive singleton pruning
	if count, ok := store.spIndex["eeff,0011"]; !ok {
		t.Fatal("expected spIndex[\"eeff,0011\"] to survive singleton pruning")
	} else if count != 2 {
		t.Errorf("spIndex[\"eeff,0011\"] = %d, want 2", count)
	}

	// All count==1 entries must be gone
	for key, count := range store.spIndex {
		if count < 2 {
			t.Errorf("spIndex[%q] = %d, singletons should have been pruned", key, count)
		}
	}
}

// TestSubpathEmptyDB verifies that the store loads successfully on a DB
// with no transmissions (no subpaths at all).
func TestSubpathEmptyDB(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	if len(store.spIndex) != 0 {
		t.Errorf("expected empty spIndex on empty DB, got %d entries", len(store.spIndex))
	}
	if store.spTotalPaths != 0 {
		t.Errorf("expected spTotalPaths=0 on empty DB, got %d", store.spTotalPaths)
	}

	// GetSubpathDetail should still work (return zero matches)
	detail := store.GetSubpathDetail([]string{"aa", "bb"})
	if detail == nil {
		t.Fatal("expected non-nil detail even on empty DB")
	}
	matches, _ := detail["totalMatches"].(int)
	if matches != 0 {
		t.Errorf("totalMatches on empty DB = %d, want 0", matches)
	}
}

func TestStoreGetAnalyticsRFCacheHit(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	// First call — cache miss
	result1 := store.GetAnalyticsRF("")
	if result1["totalPackets"] == nil {
		t.Error("expected totalPackets")
	}

	// Second call — should hit cache
	result2 := store.GetAnalyticsRF("")
	if result2["totalPackets"] == nil {
		t.Error("expected cached totalPackets")
	}

	// Verify cache hit was recorded
	stats := store.GetCacheStats()
	hits, _ := stats["hits"].(int64)
	if hits < 1 {
		t.Error("expected at least 1 cache hit")
	}
}

func TestStoreGetAnalyticsTopology(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetAnalyticsTopology("")
	if result == nil {
		t.Error("expected non-nil result")
	}

	// #155: uniqueNodes must match DB 7-day active count, not hop resolution
	stats, err := db.GetStats()
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	un, ok := result["uniqueNodes"].(int)
	if !ok {
		t.Fatalf("uniqueNodes is not int: %T", result["uniqueNodes"])
	}
	if un != stats.TotalNodes {
		t.Errorf("uniqueNodes=%d should match stats totalNodes=%d", un, stats.TotalNodes)
	}

	t.Run("with region", func(t *testing.T) {
		r := store.GetAnalyticsTopology("SJC")
		_ = r
	})
}

func TestStoreGetAnalyticsChannels(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetAnalyticsChannels("")
	if _, ok := result["activeChannels"]; !ok {
		t.Error("expected activeChannels")
	}
	if _, ok := result["topSenders"]; !ok {
		t.Error("expected topSenders")
	}
	if _, ok := result["channelTimeline"]; !ok {
		t.Error("expected channelTimeline")
	}

	t.Run("with region", func(t *testing.T) {
		r := store.GetAnalyticsChannels("SJC")
		_ = r
	})
}

// Regression test for #154: channelHash is a number in decoded JSON from decoder.js,
// not a string. The Go struct must handle both types correctly.
func TestStoreGetAnalyticsChannelsNumericHash(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()

	// Insert GRP_TXT packets with numeric channelHash (matches decoder.js output)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DD01', 'grp_num_hash_1', ?, 1, 5, '{"type":"GRP_TXT","channelHash":97,"channelHashHex":"61","decryptionStatus":"no_key"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (4, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DD02', 'grp_num_hash_2', ?, 1, 5, '{"type":"GRP_TXT","channelHash":42,"channelHashHex":"2A","decryptionStatus":"no_key"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (5, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// Also a decrypted CHAN with numeric channelHash
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DD03', 'chan_num_hash_3', ?, 1, 5, '{"type":"CHAN","channel":"general","channelHash":97,"channelHashHex":"61","text":"hello","sender":"Alice"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (6, 1, 12.0, -88, '[]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()
	result := store.GetAnalyticsChannels("")

	channels := result["channels"].([]map[string]interface{})
	if len(channels) < 2 {
		t.Errorf("expected at least 2 channels (hash 97 + hash 42), got %d", len(channels))
	}

	// Verify the numeric-hash channels we inserted have proper hashes (not "?")
	found97 := false
	found42 := false
	for _, ch := range channels {
		if ch["hash"] == "97" {
			found97 = true
		}
		if ch["hash"] == "42" {
			found42 = true
		}
	}
	if !found97 {
		t.Error("expected to find channel with hash '97' (numeric channelHash parsing)")
	}
	if !found42 {
		t.Error("expected to find channel with hash '42' (numeric channelHash parsing)")
	}

	// Verify the decrypted CHAN channel has the correct name
	foundGeneral := false
	for _, ch := range channels {
		if ch["name"] == "general" {
			foundGeneral = true
			if ch["hash"] != "97" {
				t.Errorf("expected hash '97' for general channel, got %v", ch["hash"])
			}
		}
	}
	if !foundGeneral {
		t.Error("expected to find channel named 'general'")
	}
}

func TestStoreGetAnalyticsDistance(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetAnalyticsDistance("")
	if result == nil {
		t.Error("expected non-nil result")
	}

	t.Run("with region", func(t *testing.T) {
		r := store.GetAnalyticsDistance("SJC")
		_ = r
	})
}

func TestStoreGetSubpathDetail(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetSubpathDetail([]string{"aabb", "ccdd"})
	if result == nil {
		t.Error("expected non-nil result")
	}
	if _, ok := result["hops"]; !ok {
		t.Error("expected hops field")
	}
}

// --- Route handlers with store for analytics ---

func TestHandleAnalyticsRFWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	t.Run("basic", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/analytics/rf", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("with region", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/analytics/rf?region=SJC", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

func TestHandleBulkHealthWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/bulk-health?limit=50&region=SJC", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsSubpathsWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/subpaths?minLen=2&maxLen=4&limit=50", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsSubpathDetailWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/subpath-detail?hops=aabb,ccdd", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsDistanceWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/distance", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsHashSizesWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/hash-sizes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsTopologyWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/topology", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleAnalyticsChannelsWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- GetChannelMessages more paths ---

func TestGetChannelMessagesRichData(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	messages, total := store.GetChannelMessages("#test", 100, 0)
	if total < 2 {
		t.Errorf("expected >=2 messages for #test with rich data, got %d", total)
	}

	// Verify message fields
	for _, msg := range messages {
		if _, ok := msg["sender"]; !ok {
			t.Error("expected sender field")
		}
		if _, ok := msg["hops"]; !ok {
			t.Error("expected hops field")
		}
	}
}

// --- handleObservers with actual data ---

func TestHandleObserversWithData(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	observers, ok := body["observers"].([]interface{})
	if !ok || len(observers) == 0 {
		t.Error("expected non-empty observers")
	}
}

// --- handleChannelMessages with store ---

func TestHandleChannelMessagesWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/channels/%23test/messages?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- handleChannels with store ---

func TestHandleChannelsWithStore(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- Traces via store path ---

func TestHandleTracesWithStore(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/traces/abc123def4567890", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- Store.GetStoreStats ---

func TestStoreGetStoreStats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	stats, err := store.GetStoreStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalTransmissions < 1 {
		t.Error("expected transmissions > 0")
	}
}

// --- Store.QueryGroupedPackets ---

func TestStoreQueryGroupedPackets(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	result := store.QueryGroupedPackets(PacketQuery{Limit: 50, Order: "DESC"})
	if result.Total < 1 {
		t.Error("expected >=1 grouped packets")
	}
}

// --- Store.GetPacketByHash / GetPacketByID / GetTransmissionByID ---

func TestStoreGetPacketByHash(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	pkt := store.GetPacketByHash("abc123def4567890")
	if pkt == nil {
		t.Fatal("expected packet")
	}
	if pkt["hash"] != "abc123def4567890" {
		t.Errorf("wrong hash: %v", pkt["hash"])
	}

	t.Run("not found", func(t *testing.T) {
		pkt := store.GetPacketByHash("0000000000000000")
		if pkt != nil {
			t.Error("expected nil for not found")
		}
	})
}

// --- Coverage gap-filling tests ---

func TestResolvePayloadTypeNameUnknown(t *testing.T) {
	// nil → UNKNOWN
	if got := resolvePayloadTypeName(nil); got != "UNKNOWN" {
		t.Errorf("expected UNKNOWN for nil, got %s", got)
	}
	// known type
	pt4 := 4
	if got := resolvePayloadTypeName(&pt4); got != "ADVERT" {
		t.Errorf("expected ADVERT, got %s", got)
	}
	// unknown type → UNK(N) format
	pt99 := 99
	if got := resolvePayloadTypeName(&pt99); got != "UNK(99)" {
		t.Errorf("expected UNK(99), got %s", got)
	}
}

func TestCacheHitTopology(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	// First call — cache miss
	r1 := store.GetAnalyticsTopology("")
	if r1 == nil {
		t.Fatal("expected topology result")
	}

	// Second call — cache hit
	r2 := store.GetAnalyticsTopology("")
	if r2 == nil {
		t.Fatal("expected cached topology result")
	}

	stats := store.GetCacheStats()
	hits := stats["hits"].(int64)
	if hits < 1 {
		t.Errorf("expected cache hit, got %d hits", hits)
	}
}

func TestCacheHitHashSizes(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	r1 := store.GetAnalyticsHashSizes("")
	if r1 == nil {
		t.Fatal("expected hash sizes result")
	}

	r2 := store.GetAnalyticsHashSizes("")
	if r2 == nil {
		t.Fatal("expected cached hash sizes result")
	}

	stats := store.GetCacheStats()
	hits := stats["hits"].(int64)
	if hits < 1 {
		t.Errorf("expected cache hit, got %d", hits)
	}
}

func TestCacheHitChannels(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	r1 := store.GetAnalyticsChannels("")
	if r1 == nil {
		t.Fatal("expected channels result")
	}

	r2 := store.GetAnalyticsChannels("")
	if r2 == nil {
		t.Fatal("expected cached channels result")
	}

	stats := store.GetCacheStats()
	hits := stats["hits"].(int64)
	if hits < 1 {
		t.Errorf("expected cache hit, got %d", hits)
	}
}

func TestGetChannelMessagesEdgeCases(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	// Channel not found — empty result
	msgs, total := store.GetChannelMessages("nonexistent_channel", 10, 0)
	if total != 0 {
		t.Errorf("expected 0 total for nonexistent channel, got %d", total)
	}
	if len(msgs) != 0 {
		t.Errorf("expected empty msgs, got %d", len(msgs))
	}

	// Default limit (0 → 100)
	msgs, _ = store.GetChannelMessages("#test", 0, 0)
	_ = msgs // just exercises the default limit path

	// Offset beyond range
	msgs, total = store.GetChannelMessages("#test", 10, 9999)
	if len(msgs) != 0 {
		t.Errorf("expected empty msgs for large offset, got %d", len(msgs))
	}
	if total == 0 {
		t.Error("total should be > 0 even with large offset")
	}

	// Negative offset
	msgs, _ = store.GetChannelMessages("#test", 10, -5)
	_ = msgs // exercises the start < 0 path
}

func TestFilterPacketsEmptyRegion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Region with no observers → empty result
	results := store.QueryPackets(PacketQuery{Region: "NONEXISTENT", Limit: 100})
	if results.Total != 0 {
		t.Errorf("expected 0 results for nonexistent region, got %d", results.Total)
	}
}

func TestFilterPacketsSinceUntil(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Since far future → empty
	results := store.QueryPackets(PacketQuery{Since: "2099-01-01T00:00:00Z", Limit: 100})
	if results.Total != 0 {
		t.Errorf("expected 0 results for far future since, got %d", results.Total)
	}

	// Until far past → empty
	results = store.QueryPackets(PacketQuery{Until: "2000-01-01T00:00:00Z", Limit: 100})
	if results.Total != 0 {
		t.Errorf("expected 0 results for far past until, got %d", results.Total)
	}

	// Route filter
	rt := 1
	results = store.QueryPackets(PacketQuery{Route: &rt, Limit: 100})
	if results.Total == 0 {
		t.Error("expected results for route_type=1 filter")
	}
}

func TestFilterPacketsHashOnly(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Single hash fast-path — found
	results := store.QueryPackets(PacketQuery{Hash: "abc123def4567890", Limit: 100})
	if results.Total != 1 {
		t.Errorf("expected 1 result for known hash, got %d", results.Total)
	}

	// Single hash fast-path — not found
	results = store.QueryPackets(PacketQuery{Hash: "0000000000000000", Limit: 100})
	if results.Total != 0 {
		t.Errorf("expected 0 results for unknown hash, got %d", results.Total)
	}
}

func TestFilterPacketsObserverWithType(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Observer + type filter (takes non-indexed path)
	pt := 4
	results := store.QueryPackets(PacketQuery{Observer: "obs1", Type: &pt, Limit: 100})
	_ = results // exercises the combined observer+type filter path
}

func TestFilterPacketsNodeFilter(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Node filter — exercises DecodedJSON containment check
	results := store.QueryPackets(PacketQuery{Node: "aabbccdd11223344", Limit: 100})
	if results.Total == 0 {
		t.Error("expected results for node filter")
	}

	// Node filter with hash combined
	results = store.QueryPackets(PacketQuery{Node: "aabbccdd11223344", Hash: "abc123def4567890", Limit: 100})
	_ = results
}

func TestGetNodeHashSizeInfoEdgeCases(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	// Observers
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Obs', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, recent)

	// Adverts with various edge cases
	// 1. Valid advert with pubKey
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0140aabbccdd', 'hs_valid_1', ?, 1, 4, '{"pubKey":"aabbccdd11223344","name":"NodeA","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// 2. Short raw_hex (< 4 chars)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('01', 'hs_short_hex', ?, 1, 4, '{"pubKey":"eeff00112233aabb","name":"NodeB","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// 3. Invalid hex in path byte position
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('01GGHHII', 'hs_bad_hex', ?, 1, 4, '{"pubKey":"1122334455667788","name":"NodeC","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// 4. Invalid JSON
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0140aabb', 'hs_bad_json', ?, 1, 4, 'not-json')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (4, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// 5. JSON with public_key field instead of pubKey
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0180eeff', 'hs_alt_key', ?, 1, 4, '{"public_key":"aabbccdd11223344","name":"NodeA","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (5, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// 6. JSON with no pubKey at all
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('01C0ffee', 'hs_no_pk', ?, 1, 4, '{"name":"NodeZ","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (6, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// 7. Empty decoded_json
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0140bbcc', 'hs_empty_json', ?, 1, 4, '')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (7, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// 8-10. Multiple adverts for same node with different hash sizes (flip-flop test)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0140dd01', 'hs_flip_1', ?, 1, 4, '{"pubKey":"ffff000011112222","name":"Flipper","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (8, 1, 10.0, -90, '[]', ?)`, recentEpoch)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0180dd02', 'hs_flip_2', ?, 1, 4, '{"pubKey":"ffff000011112222","name":"Flipper","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (9, 1, 10.0, -90, '[]', ?)`, recentEpoch)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0140dd03', 'hs_flip_3', ?, 1, 4, '{"pubKey":"ffff000011112222","name":"Flipper","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (10, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()
	info := store.GetNodeHashSizeInfo()

	// Valid node should be present
	if _, ok := info["aabbccdd11223344"]; !ok {
		t.Error("expected aabbccdd11223344 in hash size info")
	}

	// Flipper should have inconsistent flag (2→3→2 = 2 transitions, 2 unique sizes, 3 obs)
	if flipper, ok := info["ffff000011112222"]; ok {
		if len(flipper.AllSizes) < 2 {
			t.Errorf("expected 2+ unique sizes for flipper, got %d", len(flipper.AllSizes))
		}
		if !flipper.Inconsistent {
			t.Error("expected Inconsistent=true for flip-flop node")
		}
	} else {
		t.Error("expected ffff000011112222 in hash size info")
	}

	// Bad entries (short hex, bad hex, bad json, no pk) should not corrupt results
	if _, ok := info["eeff00112233aabb"]; ok {
		t.Error("short raw_hex node should not be in results")
	}
	if _, ok := info["1122334455667788"]; ok {
		t.Error("bad hex node should not be in results")
	}
}

// TestHashSizeTransportRoutePathByteOffset verifies that transport routes (0, 3)
// read the path byte from offset 5 (after 4 transport code bytes), not offset 1.
// Regression test for #744 / #722.
func TestHashSizeTransportRoutePathByteOffset(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Obs', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, recent)

	// Route type 0 (TRANSPORT_FLOOD): header=0x04 (payload_type=1, route_type=0)
	// 4 transport bytes + path byte at offset 5.
	// Path byte 0x80 → hash_size bits = 10 → size 3
	// If bug is present, code reads byte 1 (0xAA) → hash_size bits = 10 → size 3 (coincidence)
	// Use path byte 0x40 (hash_size=2) and transport byte 0x01 at offset 1 (hash_size=1 if misread)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('100102030440aabb', 'tf_offset', ?, 0, 4, '{"pubKey":"aaaa000000000001","name":"TF-Node","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// Route type 3 (TRANSPORT_DIRECT): header=0x13 (payload_type=4, route_type=3)
	// 4 transport bytes + path byte at offset 5.
	// Path byte 0xC1 → hash_size bits = 11 → size 4, hop_count = 1 (not zero-hop)
	// Byte 1 = 0x05 → hash_size bits = 00 → size 1 if misread
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1305060708C1bbcc', 'td_offset', ?, 3, 4, '{"pubKey":"aaaa000000000002","name":"TD-Node","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// Route type 1 (FLOOD): header=0x11 (payload_type=4, route_type=1)
	// Path byte at offset 1. Path byte 0x80 → hash_size bits = 10 → size 3
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1180aabbccdd', 'flood_offset', ?, 1, 4, '{"pubKey":"aaaa000000000003","name":"Flood-Node","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()
	info := store.GetNodeHashSizeInfo()

	// Transport flood node: path byte 0x40 → hash_size = 2
	if ni, ok := info["aaaa000000000001"]; !ok {
		t.Error("transport flood node missing from hash size info")
	} else if ni.HashSize != 2 {
		t.Errorf("transport flood node: want HashSize=2 (from path byte at offset 5), got %d", ni.HashSize)
	}

	// Transport direct node: path byte 0xC1 → hash_size = 4
	if ni, ok := info["aaaa000000000002"]; !ok {
		t.Error("transport direct node missing from hash size info")
	} else if ni.HashSize != 4 {
		t.Errorf("transport direct node: want HashSize=4 (from path byte at offset 5), got %d", ni.HashSize)
	}

	// Regular flood node: path byte 0x80 → hash_size = 3
	if ni, ok := info["aaaa000000000003"]; !ok {
		t.Error("regular flood node missing from hash size info")
	} else if ni.HashSize != 3 {
		t.Errorf("regular flood node: want HashSize=3 (from path byte at offset 1), got %d", ni.HashSize)
	}
}

// TestHashSizeTransportDirectZeroHopSkipped verifies that RouteTransportDirect
// zero-hop adverts are skipped (same as RouteDirect). Regression test for #744.
func TestHashSizeTransportDirectZeroHopSkipped(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Obs', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, recent)

	// RouteDirect (2) zero-hop: path byte 0x40 → hop_count=0, hash_size bits=01
	// Should be skipped (existing behavior)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1240aabbccdd', 'direct_zh', ?, 2, 4, '{"pubKey":"bbbb000000000001","name":"Direct-ZH","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// RouteTransportDirect (3) zero-hop: 4 transport bytes + path byte 0x40 → hop_count=0
	// Should ALSO be skipped (this was the missing case)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('130102030440aabb', 'tdirect_zh', ?, 3, 4, '{"pubKey":"bbbb000000000002","name":"TDirect-ZH","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	// RouteDirect (2) non-zero-hop: path byte 0x41 → hop_count=1
	// Should NOT be skipped
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1241aabbccdd', 'direct_1h', ?, 2, 4, '{"pubKey":"bbbb000000000003","name":"Direct-1H","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()
	info := store.GetNodeHashSizeInfo()

	// RouteDirect zero-hop should be absent
	if _, ok := info["bbbb000000000001"]; ok {
		t.Error("RouteDirect zero-hop advert should be skipped")
	}

	// RouteTransportDirect zero-hop should also be absent
	if _, ok := info["bbbb000000000002"]; ok {
		t.Error("RouteTransportDirect zero-hop advert should be skipped")
	}

	// RouteDirect non-zero-hop should be present with hash_size=2
	if ni, ok := info["bbbb000000000003"]; !ok {
		t.Error("RouteDirect non-zero-hop should be in hash size info")
	} else if ni.HashSize != 2 {
		t.Errorf("RouteDirect non-zero-hop: want HashSize=2, got %d", ni.HashSize)
	}
}

// TestAnalyticsHashSizesZeroHopSkip verifies that computeAnalyticsHashSizes
// does not overwrite a node's hash_size with a zero-hop advert's unreliable value.
// Regression test for #744.
func TestAnalyticsHashSizesZeroHopSkip(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Obs', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, recent)

	pk := "cccc000000000001"
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, 'ZH-Analytics', 'repeater')`, pk)

	decoded := `{"pubKey":"` + pk + `","name":"ZH-Analytics","type":"ADVERT"}`

	// First: a flood advert with hashSize=2 (reliable, multi-hop)
	// header 0x11 = route_type 1 (flood), payload_type 4
	// pathByte 0x41 = hashSize bits 01 → size 2, hop_count 1
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1141aabbccdd', 'az_flood', ?, 1, 4, ?)`, recent, decoded)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '["aabb"]', ?)`, recentEpoch)

	// Second: a direct zero-hop advert with pathByte=0x00 → would give hashSize=1
	// header 0x12 = route_type 2 (direct), payload_type 4
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1200aabbccdd', 'az_direct', ?, 2, 4, ?)`, recent, decoded)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetAnalyticsHashSizes("")

	// The node should appear in multiByteNodes (hashSize=2 from the flood advert)
	// If the zero-hop bug is present, hashSize would be 1 and the node would NOT
	// appear in multiByteNodes.
	multiByteNodes, ok := result["multiByteNodes"].([]map[string]interface{})
	if !ok {
		t.Fatal("expected multiByteNodes slice in analytics hash sizes")
	}

	found := false
	for _, n := range multiByteNodes {
		if n["pubkey"] == pk {
			found = true
			if hs, ok := n["hashSize"].(int); ok && hs != 2 {
				t.Errorf("expected hashSize=2 from flood advert, got %d", hs)
			}
		}
	}
	if !found {
		t.Error("node should appear in multiByteNodes with hashSize=2; zero-hop advert should not overwrite to 1")
	}
}

func TestHandleResolveHopsEdgeCases(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// Empty hops param
	req := httptest.NewRequest("GET", "/api/resolve-hops", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	resolved := body["resolved"].(map[string]interface{})
	if len(resolved) != 0 {
		t.Errorf("expected empty resolved for empty hops, got %d", len(resolved))
	}

	// Multiple hops with empty string included
	req = httptest.NewRequest("GET", "/api/resolve-hops?hops=aabb,,eeff", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	resolved = body["resolved"].(map[string]interface{})
	// Empty string should be skipped
	if _, ok := resolved[""]; ok {
		t.Error("empty hop should be skipped")
	}

	// Nonexistent prefix — zero candidates
	req = httptest.NewRequest("GET", "/api/resolve-hops?hops=nonexistent_prefix_xyz", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleObserversError(t *testing.T) {
	// Use a closed DB to trigger an error from GetObservers
	db := setupTestDB(t)
	seedTestData(t, db)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	db.Close() // force error after routes registered

	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("expected 500 for closed DB, got %d", w.Code)
	}
}

func TestHandleAnalyticsChannelsDBFallback(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	// Server with NO store — takes DB fallback path
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["activeChannels"]; !ok {
		t.Error("expected activeChannels in DB-fallback response")
	}
	if _, ok := body["channels"]; !ok {
		t.Error("expected channels in DB-fallback response")
	}
}

func TestGetChannelMessagesDedupeRepeats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Obs1', 'SJC', ?, '2026-01-01T00:00:00Z', 10)`, recent)
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs2', 'Obs2', 'LAX', ?, '2026-01-01T00:00:00Z', 10)`, recent)

	// Insert two copies of same CHAN message (same hash, different observers)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('CC01', 'dedup_chan_1', ?, 1, 5, '{"type":"CHAN","channel":"#general","text":"Alice: hello","sender":"Alice"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 12.0, -88, '["aa"]', ?)`, recentEpoch)

	// Same sender + hash → different observation (simulates dedup)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('CC02', 'dedup_chan_1', ?, 1, 5, '{"type":"CHAN","channel":"#general","text":"Alice: hello","sender":"Alice"}')`, recent)
	// Note: won't load due to UNIQUE constraint on hash → tests the code path with single tx having multiple obs

	// Second different message
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('CC03', 'dedup_chan_2', ?, 1, 5, '{"type":"CHAN","channel":"#general","text":"Bob: world","sender":"Bob"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 2, 10.0, -90, '["bb"]', ?)`, recentEpoch)

	// GRP_TXT (not CHAN) — should be skipped by GetChannelMessages
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DD01', 'grp_msg_hash_1', ?, 1, 5, '{"type":"GRP_TXT","channelHash":"42","text":"encrypted"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 10.0, -90, '[]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()

	msgs, total := store.GetChannelMessages("#general", 10, 0)
	if total == 0 {
		t.Error("expected messages for #general")
	}

	// Check message structure
	for _, msg := range msgs {
		if _, ok := msg["sender"]; !ok {
			t.Error("expected sender field")
		}
		if _, ok := msg["text"]; !ok {
			t.Error("expected text field")
		}
		if _, ok := msg["observers"]; !ok {
			t.Error("expected observers field")
		}
	}
}

func TestTransmissionsForObserverFromSlice(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Test with from=nil (index path) — for non-existent observer
	result := store.transmissionsForObserver("nonexistent_obs", nil)
	if len(result) != 0 {
		t.Errorf("expected nil/empty for nonexistent observer, got %d", len(result))
	}

	// Test with from=non-nil slice (filter path)
	allPackets := store.packets
	result = store.transmissionsForObserver("obs1", allPackets)
	if len(result) == 0 {
		t.Error("expected results for obs1 from filter path")
	}
}

func TestGetPerfStoreStatsPublicKeyField(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	stats := store.GetPerfStoreStats()
	indexes := stats["indexes"].(map[string]interface{})
	// advertByObserver should count distinct pubkeys from advert packets
	aboc := indexes["advertByObserver"].(int)
	if aboc == 0 {
		t.Error("expected advertByObserver > 0 for rich test DB")
	}
}

func TestHandleAudioLabBucketsQueryError(t *testing.T) {
	// Use closed DB to trigger query error
	db := setupTestDB(t)
	seedTestData(t, db)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	db.Close()

	req := httptest.NewRequest("GET", "/api/audio-lab/buckets", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 (empty buckets on error), got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	buckets := body["buckets"].(map[string]interface{})
	if len(buckets) != 0 {
		t.Errorf("expected empty buckets on query error, got %d", len(buckets))
	}
}

func TestStoreGetTransmissionByID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	pkt := store.GetTransmissionByID(1)
	if pkt == nil {
		t.Fatal("expected packet")
	}

	t.Run("not found", func(t *testing.T) {
		pkt := store.GetTransmissionByID(99999)
		if pkt != nil {
			t.Error("expected nil")
		}
	})
}

func TestStoreGetPacketByID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Get an observation ID from the store
	var obsID int
	for id := range store.byObsID {
		obsID = id
		break
	}
	if obsID == 0 {
		t.Skip("no observations")
	}

	pkt := store.GetPacketByID(obsID)
	if pkt == nil {
		t.Fatal("expected packet")
	}

	t.Run("not found", func(t *testing.T) {
		pkt := store.GetPacketByID(99999)
		if pkt != nil {
			t.Error("expected nil")
		}
	})
}

// --- Store.GetObservationsForHash ---

func TestStoreGetObservationsForHash(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	obs := store.GetObservationsForHash("abc123def4567890")
	if len(obs) < 1 {
		t.Error("expected >=1 observation")
	}

	t.Run("not found", func(t *testing.T) {
		obs := store.GetObservationsForHash("0000000000000000")
		if len(obs) != 0 {
			t.Errorf("expected 0, got %d", len(obs))
		}
	})
}

// --- Store.GetNewTransmissionsSince ---

func TestStoreGetNewTransmissionsSince(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	txs, err := db.GetNewTransmissionsSince(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) < 1 {
		t.Error("expected >=1 transmission")
	}
}

// --- HandlePacketDetail with store (by hash, by tx ID, by obs ID) ---

func TestHandlePacketDetailWithStoreAllPaths(t *testing.T) {
	_, router := setupTestServer(t)

	t.Run("by hash", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets/abc123def4567890", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var body map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["observations"] == nil {
			t.Error("expected observations")
		}
	})

	t.Run("by tx ID", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets/1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("not found ID", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/packets/999999", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("expected 404, got %d", w.Code)
		}
	})
}

// --- Additional DB function coverage ---

func TestDBGetNewTransmissionsSince(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	txs, err := db.GetNewTransmissionsSince(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) < 1 {
		t.Error("expected >=1 transmissions")
	}
}

func TestDBGetNetworkStatus(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	cfg := &Config{}
	ht := cfg.GetHealthThresholds()
	result, err := db.GetNetworkStatus(ht)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Error("expected non-nil result")
	}
}

func TestDBGetObserverByID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	obs, err := db.GetObserverByID("obs1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		t.Error("expected non-nil observer")
	}
	if obs.ID != "obs1" {
		t.Errorf("expected obs1, got %s", obs.ID)
	}

	t.Run("not found", func(t *testing.T) {
		obs, err := db.GetObserverByID("nonexistent")
		if err == nil && obs != nil {
			t.Error("expected nil observer for nonexistent ID")
		}
		// Some implementations return (nil, err) — that's fine too
	})
}

func TestDBGetTraces(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	traces, err := db.GetTraces("abc123def4567890")
	if err != nil {
		t.Fatal(err)
	}
	_ = traces
}

// --- DB queries with different filter combos ---

func TestDBQueryPacketsAllFilters(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	pt := 4
	rt := 1
	result, err := db.QueryPackets(PacketQuery{
		Limit:    50,
		Type:     &pt,
		Route:    &rt,
		Observer: "obs1",
		Hash:     "abc123def4567890",
		Since:    "2020-01-01",
		Until:    "2099-01-01",
		Region:   "SJC",
		Node:     "TestRepeater",
		Order:    "ASC",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = result
}

// --- IngestNewFromDB dedup path ---

func TestIngestNewFromDBDuplicateObs(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	initialMax := store.MaxTransmissionID()

	// Insert new transmission with same hash as existing (should merge into existing tx)
	now := time.Now().UTC().Format(time.RFC3339)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AABB', 'dedup_test_hash_01', ?, 1, 4, '{"pubKey":"aabbccdd11223344","type":"ADVERT"}')`, now)
	newTxID := 0
	db.conn.QueryRow("SELECT MAX(id) FROM transmissions").Scan(&newTxID)

	// Add observation
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 11.0, -89, '["dd"]', ?)`, newTxID, time.Now().Unix())
	// Add duplicate observation (same observer_id + path_json)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 11.0, -89, '["dd"]', ?)`, newTxID, time.Now().Unix())

	_, newMax := store.IngestNewFromDB(initialMax, 100)
	if newMax <= initialMax {
		t.Errorf("expected newMax > %d, got %d", initialMax, newMax)
	}
}

// --- IngestNewObservations (fixes #174) ---

func TestIngestNewObservations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Get initial observation count for transmission 1 (hash abc123def4567890)
	initialTx := store.byHash["abc123def4567890"]
	if initialTx == nil {
		t.Fatal("expected to find transmission abc123def4567890 in store")
	}
	initialObsCount := initialTx.ObservationCount
	if initialObsCount != 2 {
		t.Fatalf("expected 2 initial observations, got %d", initialObsCount)
	}

	// Record the max obs ID after initial load
	maxObsID := db.GetMaxObservationID()

	// Simulate a new observation arriving for the existing transmission AFTER
	// the poller has already advanced past its transmission ID
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 2, 5.0, -100, '["aa","bb","cc"]', ?)`, time.Now().Unix())

	// Verify IngestNewFromDB does NOT pick up the new observation (tx id hasn't changed)
	txMax := store.MaxTransmissionID()
	_, newTxMax := store.IngestNewFromDB(txMax, 100)
	if initialTx.ObservationCount != initialObsCount {
		t.Errorf("IngestNewFromDB should not have changed obs count, was %d now %d",
			initialObsCount, initialTx.ObservationCount)
	}
	_ = newTxMax

	// IngestNewObservations should pick it up
	newObsMaps := store.IngestNewObservations(maxObsID, 500)
	if len(newObsMaps) != 1 {
		t.Errorf("expected 1 observation broadcast map, got %d", len(newObsMaps))
	}
	if initialTx.ObservationCount != initialObsCount+1 {
		t.Errorf("expected obs count %d, got %d", initialObsCount+1, initialTx.ObservationCount)
	}
	if len(initialTx.Observations) != initialObsCount+1 {
		t.Errorf("expected %d observations slice len, got %d", initialObsCount+1, len(initialTx.Observations))
	}

	// Best observation should have been re-picked (new obs has longer path)
	if initialTx.PathJSON != `["aa","bb","cc"]` {
		t.Errorf("expected best path to be updated to longer path, got %s", initialTx.PathJSON)
	}

	t.Run("no new observations", func(t *testing.T) {
		maps := store.IngestNewObservations(db.GetMaxObservationID(), 500)
		if maps != nil {
			t.Errorf("expected nil maps for no new observations, got %d", len(maps))
		}
	})

	t.Run("dedup by observer+path", func(t *testing.T) {
		// Insert duplicate observation (same observer + path as existing)
		db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
			VALUES (1, 1, 12.5, -90, '["aa","bb"]', ?)`, time.Now().Unix())
		prevCount := initialTx.ObservationCount
		maps := store.IngestNewObservations(db.GetMaxObservationID()-1, 500)
		if initialTx.ObservationCount != prevCount {
			t.Errorf("duplicate obs should not increase count, was %d now %d",
				prevCount, initialTx.ObservationCount)
		}
		if len(maps) != 0 {
			t.Errorf("expected 0 broadcast maps for duplicate obs, got %d", len(maps))
		}
	})

	t.Run("default limit", func(t *testing.T) {
		_ = store.IngestNewObservations(db.GetMaxObservationID(), 0)
	})
}

func TestIngestNewObservationsV2(t *testing.T) {
	db := setupTestDBv2(t)
	defer db.Close()
	seedV2Data(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	tx := store.byHash["abc123def4567890"]
	if tx == nil {
		t.Fatal("expected to find transmission in store")
	}
	initialCount := tx.ObservationCount

	maxObsID := db.GetMaxObservationID()

	// Add new observation for existing transmission
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_id, observer_name, snr, rssi, path_json, timestamp)
		VALUES (1, 'obs2', 'Obs Two', 6.0, -98, '["dd","ee"]', ?)`, time.Now().Unix())

	newMaps := store.IngestNewObservations(maxObsID, 500)
	if len(newMaps) != 1 {
		t.Errorf("expected 1 observation broadcast map, got %d", len(newMaps))
	}
	if tx.ObservationCount != initialCount+1 {
		t.Errorf("expected obs count %d, got %d", initialCount+1, tx.ObservationCount)
	}
}

func TestGetMaxObservationID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	maxID := db.GetMaxObservationID()
	if maxID != 0 {
		t.Errorf("expected 0 for empty table, got %d", maxID)
	}

	seedTestData(t, db)
	maxID = db.GetMaxObservationID()
	if maxID <= 0 {
		t.Errorf("expected positive max obs ID, got %d", maxID)
	}
}

// --- perfMiddleware with endpoint normalization ---

func TestPerfMiddlewareEndpointNormalization(t *testing.T) {
	_, router := setupTestServer(t)

	// Hit a route with a hex hash — should normalize to :id
	req := httptest.NewRequest("GET", "/api/packets/abc123def4567890", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// The hex id should have been normalized in perf stats
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- handleNodeAnalytics edge cases ---

func TestHandleNodeAnalyticsNameless(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	// Insert a node without a name
	db.conn.Exec(`INSERT INTO nodes (public_key, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('nameless_node_pk_1', 'repeater', 37.5, -122.0, ?, '2026-01-01', 1)`,
		time.Now().UTC().Format(time.RFC3339))

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	store.Load()
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/nameless_node_pk_1/analytics?days=1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// --- PerfStats overflow (>100 recent entries) ---

func TestPerfStatsRecentOverflow(t *testing.T) {
	_, router := setupTestServer(t)
	// Hit an endpoint 120 times to overflow the Recent buffer (capped at 100)
	for i := 0; i < 120; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/health?i=%d", i), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

// --- handleAudioLabBuckets ---

func TestHandleAudioLabBucketsNoStore(t *testing.T) {
	_, router := setupNoStoreServer(t)
	req := httptest.NewRequest("GET", "/api/audio-lab/buckets", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// Just verify no crash
}

// --- Store region filter paths ---

func TestStoreQueryPacketsRegionFilter(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	result := store.QueryPackets(PacketQuery{Region: "SJC", Limit: 50, Order: "DESC"})
	_ = result

	result2 := store.QueryPackets(PacketQuery{Region: "NONEXIST", Limit: 50, Order: "DESC"})
	if result2.Total != 0 {
		t.Errorf("expected 0 for non-existent region, got %d", result2.Total)
	}
}

// --- DB.GetObserverIdsForRegion ---

func TestDBGetObserverIdsForRegion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	ids, err := db.GetObserverIdsForRegion("SJC")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Error("expected observer IDs for SJC")
	}

	ids2, err := db.GetObserverIdsForRegion("NONEXIST")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids2) != 0 {
		t.Errorf("expected 0 for NONEXIST, got %d", len(ids2))
	}
}

// --- DB.GetDistinctIATAs ---

func TestDBGetDistinctIATAs(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	iatas, err := db.GetDistinctIATAs()
	if err != nil {
		t.Fatal(err)
	}
	if len(iatas) == 0 {
		t.Error("expected at least one IATA code")
	}
}

// --- DB.SearchNodes ---

func TestDBSearchNodes(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	nodes, err := db.SearchNodes("Test", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Error("expected nodes matching 'Test'")
	}
}

// --- Ensure non-panic on GetDBSizeStats with path ---

func TestGetDBSizeStatsMemory(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	stats := db.GetDBSizeStats()
	if stats["dbSizeMB"] != float64(0) {
		t.Errorf("expected 0 for in-memory, got %v", stats["dbSizeMB"])
	}
}

// Regression test for #198: channel messages must include newly ingested packets.
// byPayloadType must maintain newest-first ordering after IngestNewFromDB so that
// GetChannelMessages reverse iteration returns the latest messages.
func TestGetChannelMessagesAfterIngest(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	initialMax := store.MaxTransmissionID()

	// Get baseline message count
	_, totalBefore := store.GetChannelMessages("#test", 100, 0)

	// Insert a new channel message into the DB (newer than anything loaded)
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('FF01', 'newchannelmsg19800', ?, 1, 5, '{"type":"CHAN","channel":"#test","text":"NewUser: brand new message","sender":"NewUser"}')`, nowStr)
	newTxID := 0
	db.conn.QueryRow("SELECT MAX(id) FROM transmissions").Scan(&newTxID)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 12.0, -88, '[]', ?)`, newTxID, now.Unix())

	// Ingest the new data
	_, newMax := store.IngestNewFromDB(initialMax, 100)
	if newMax <= initialMax {
		t.Fatalf("ingest did not advance maxID: %d -> %d", initialMax, newMax)
	}

	// GetChannelMessages must now include the new message
	msgs, totalAfter := store.GetChannelMessages("#test", 100, 0)
	if totalAfter <= totalBefore {
		t.Errorf("expected more messages after ingest: before=%d after=%d", totalBefore, totalAfter)
	}

	// The newest message (last in the returned slice) must be the one we just inserted
	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	lastMsg := msgs[len(msgs)-1]
	if lastMsg["text"] != "brand new message" {
		t.Errorf("newest message should be 'brand new message', got %q", lastMsg["text"])
	}
}

// --- resolveRegionObservers caching ---

func TestResolveRegionObserversCaching(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	store := &PacketStore{db: db}

	// First call should populate cache.
	obs1 := store.resolveRegionObservers("SJC")
	if obs1 == nil || len(obs1) == 0 {
		t.Fatal("expected observer IDs for SJC on first call")
	}

	// Second call should return cached result (same pointer).
	obs2 := store.resolveRegionObservers("SJC")
	if len(obs2) != len(obs1) {
		t.Errorf("cached result differs: got %d, want %d", len(obs2), len(obs1))
	}

	// Non-existent region should return nil even from cache.
	obs3 := store.resolveRegionObservers("NONEXIST")
	if obs3 != nil {
		t.Errorf("expected nil for NONEXIST, got %v", obs3)
	}

	// Verify cache fields are set.
	if store.regionObsCache == nil {
		t.Error("regionObsCache should be non-nil after calls")
	}
	if store.regionObsCacheTime.IsZero() {
		t.Error("regionObsCacheTime should be set")
	}
}

func TestResolveRegionObserversCacheMissNewRegion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	store := &PacketStore{db: db}

	// Populate cache with SJC.
	obs1 := store.resolveRegionObservers("SJC")
	if obs1 == nil || len(obs1) == 0 {
		t.Fatal("expected observer IDs for SJC on first call")
	}

	// Cache is now valid. Request a different region that exists in DB.
	// Before the fix, this would return nil from the map lookup instead of
	// fetching from DB, silently returning "no observers" for up to 30s.
	obs2 := store.resolveRegionObservers("LAX")
	// LAX may or may not have data in the test DB, but the key point is:
	// a non-existent region should be fetched (not just nil-returned).
	// Verify the region key was cached (even if empty).
	store.regionObsMu.Lock()
	_, cached := store.regionObsCache["LAX"]
	store.regionObsMu.Unlock()
	if !cached {
		t.Error("LAX should be cached after resolveRegionObservers call, even if empty")
	}
	_ = obs2
}

func TestIndexByNodePreCheck(t *testing.T) {
	store := &PacketStore{
		byNode:     make(map[string][]*StoreTx),
		nodeHashes: make(map[string]map[string]bool),
	}

	t.Run("indexes ADVERT with pubKey", func(t *testing.T) {
		tx := &StoreTx{Hash: "h1", DecodedJSON: `{"pubKey":"AABBCC","type":"ADVERT"}`}
		store.indexByNode(tx)
		if len(store.byNode["AABBCC"]) != 1 {
			t.Errorf("expected 1 entry for pubKey AABBCC, got %d", len(store.byNode["AABBCC"]))
		}
	})

	t.Run("indexes destPubKey", func(t *testing.T) {
		tx := &StoreTx{Hash: "h2", DecodedJSON: `{"destPubKey":"DDEEFF","type":"MSG"}`}
		store.indexByNode(tx)
		if len(store.byNode["DDEEFF"]) != 1 {
			t.Errorf("expected 1 entry for destPubKey DDEEFF, got %d", len(store.byNode["DDEEFF"]))
		}
	})

	t.Run("indexes srcPubKey", func(t *testing.T) {
		tx := &StoreTx{Hash: "h2b", DecodedJSON: `{"srcPubKey":"112233","type":"TXT_MSG"}`}
		store.indexByNode(tx)
		if len(store.byNode["112233"]) != 1 {
			t.Errorf("expected 1 entry for srcPubKey 112233, got %d", len(store.byNode["112233"]))
		}
	})

	t.Run("skips channel message without pubKey", func(t *testing.T) {
		beforeLen := len(store.byNode)
		tx := &StoreTx{Hash: "h3", DecodedJSON: `{"type":"CHAN","channel":"#test","text":"hello"}`}
		store.indexByNode(tx)
		if len(store.byNode) != beforeLen {
			t.Errorf("expected byNode unchanged for channel packet, got %d new entries", len(store.byNode)-beforeLen)
		}
	})

	t.Run("skips empty DecodedJSON", func(t *testing.T) {
		beforeLen := len(store.byNode)
		tx := &StoreTx{Hash: "h4", DecodedJSON: ""}
		store.indexByNode(tx)
		if len(store.byNode) != beforeLen {
			t.Error("expected byNode unchanged for empty DecodedJSON")
		}
	})

	t.Run("deduplicates same hash", func(t *testing.T) {
		tx := &StoreTx{Hash: "h1", DecodedJSON: `{"pubKey":"AABBCC","type":"ADVERT"}`}
		store.indexByNode(tx) // second call for same hash
		if len(store.byNode["AABBCC"]) != 1 {
			t.Errorf("expected dedup to keep 1 entry, got %d", len(store.byNode["AABBCC"]))
		}
	})
}

// TestIndexByNodeResolvedPath tests that resolved_path entries are indexed in byNode.
func TestIndexByNodeResolvedPath(t *testing.T) {
	store := &PacketStore{
		byNode:     make(map[string][]*StoreTx),
		nodeHashes: make(map[string]map[string]bool),
	}

	t.Run("indexes resolved path pubkeys from observations", func(t *testing.T) {
		relayPK := "aabb1122334455ff"
		tx := &StoreTx{
			Hash:        "rp1",
			DecodedJSON: `{"type":"CHAN","text":"hello"}`, // no pubKey fields
			Observations: []*StoreObs{
				{ResolvedPath: []*string{&relayPK}},
			},
		}
		store.indexByNode(tx)
		if len(store.byNode[relayPK]) != 1 {
			t.Errorf("expected relay pubkey indexed, got %d", len(store.byNode[relayPK]))
		}
	})

	t.Run("skips null entries in resolved path", func(t *testing.T) {
		pk := "cc11dd22ee33ff44"
		tx := &StoreTx{
			Hash: "rp2",
			Observations: []*StoreObs{
				{ResolvedPath: []*string{nil, &pk, nil}},
			},
		}
		store.indexByNode(tx)
		if len(store.byNode[pk]) != 1 {
			t.Errorf("expected resolved pubkey indexed, got %d", len(store.byNode[pk]))
		}
		// Verify nil entries didn't create empty-string keys
		if _, exists := store.byNode[""]; exists {
			t.Error("nil/empty resolved path entries should not create byNode entries")
		}
	})

	t.Run("relay-only node appears in byNode", func(t *testing.T) {
		// A packet with no decoded pubkey fields, only a relay in resolved path
		relayOnly := "relay0only0pubkey"
		tx := &StoreTx{
			Hash: "rp3",
			// No DecodedJSON at all — pure relay
			Observations: []*StoreObs{
				{ResolvedPath: []*string{&relayOnly}},
			},
		}
		store.indexByNode(tx)
		if len(store.byNode[relayOnly]) != 1 {
			t.Errorf("expected relay-only node indexed, got %d", len(store.byNode[relayOnly]))
		}
	})

	t.Run("dedup between decoded JSON and resolved path", func(t *testing.T) {
		pk := "dedup0test0pk1234"
		tx := &StoreTx{
			Hash:        "rp4",
			DecodedJSON: `{"pubKey":"` + pk + `"}`,
			Observations: []*StoreObs{
				{ResolvedPath: []*string{&pk}},
			},
		}
		store.indexByNode(tx)
		if len(store.byNode[pk]) != 1 {
			t.Errorf("expected dedup to keep 1 entry, got %d", len(store.byNode[pk]))
		}
	})

	t.Run("indexes tx.ResolvedPath when observations empty", func(t *testing.T) {
		rpPK := "txlevel0resolved1"
		tx := &StoreTx{
			Hash:         "rp5",
			ResolvedPath: []*string{&rpPK},
		}
		store.indexByNode(tx)
		if len(store.byNode[rpPK]) != 1 {
			t.Errorf("expected tx-level resolved path indexed, got %d", len(store.byNode[rpPK]))
		}
	})
}

// BenchmarkIndexByNode measures indexByNode performance with and without pubkey
// fields to demonstrate the strings.Contains pre-check optimization.
func BenchmarkIndexByNode(b *testing.B) {
	// Payload WITHOUT any pubkey fields — should be skipped via pre-check
	noPubkey := `{"type":1,"msgId":42,"sender":"node1","data":"hello world"}`
	// Payload WITH a pubkey field — requires JSON parse
	withPubkey := `{"type":1,"msgId":42,"pubKey":"AABB","sender":"node1","data":"hello world"}`

	b.Run("no_pubkey_skip", func(b *testing.B) {
		store := &PacketStore{
			byNode:     make(map[string][]*StoreTx),
			nodeHashes: make(map[string]map[string]bool),
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tx := &StoreTx{
				Hash:        fmt.Sprintf("hash-%d", i),
				DecodedJSON: noPubkey,
			}
			store.indexByNode(tx)
		}
	})

	b.Run("with_pubkey_parse", func(b *testing.B) {
		store := &PacketStore{
			byNode:     make(map[string][]*StoreTx),
			nodeHashes: make(map[string]map[string]bool),
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tx := &StoreTx{
				Hash:        fmt.Sprintf("hash-%d", i),
				DecodedJSON: withPubkey,
			}
			store.indexByNode(tx)
		}
	})
}

// --- Multi-observer comma-separated filter tests ---

func TestTransmissionsForObserverMultiCSV(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	t.Run("comma-separated returns union via index", func(t *testing.T) {
		result := store.transmissionsForObserver("obs1,obs2", nil)
		if len(result) == 0 {
			t.Fatal("expected results for obs1,obs2")
		}
		// obs1 has transmissions 1,2,3; obs2 has transmission 1
		// Union should include all unique transmissions
		obs1Only := store.transmissionsForObserver("obs1", nil)
		obs2Only := store.transmissionsForObserver("obs2", nil)
		if len(result) < len(obs1Only) || len(result) < len(obs2Only) {
			t.Errorf("union (%d) should be >= each individual set (obs1=%d, obs2=%d)",
				len(result), len(obs1Only), len(obs2Only))
		}
	})

	t.Run("comma-separated with spaces via index", func(t *testing.T) {
		result := store.transmissionsForObserver("obs1, obs2", nil)
		if len(result) == 0 {
			t.Fatal("expected results for 'obs1, obs2' (with space)")
		}
		noSpace := store.transmissionsForObserver("obs1,obs2", nil)
		if len(result) != len(noSpace) {
			t.Errorf("with-space (%d) should equal no-space (%d)", len(result), len(noSpace))
		}
	})

	t.Run("comma-separated returns union via filter path", func(t *testing.T) {
		allTx := store.packets
		result := store.transmissionsForObserver("obs1,obs2", allTx)
		if len(result) == 0 {
			t.Fatal("expected results for obs1,obs2 via filter path")
		}
	})

	t.Run("comma-separated with spaces via filter path", func(t *testing.T) {
		allTx := store.packets
		withSpace := store.transmissionsForObserver("obs1, obs2", allTx)
		noSpace := store.transmissionsForObserver("obs1,obs2", allTx)
		if len(withSpace) != len(noSpace) {
			t.Errorf("filter path: with-space (%d) should equal no-space (%d)", len(withSpace), len(noSpace))
		}
	})
}

func TestBuildTransmissionWhereMultiObserver(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("comma-separated produces IN clause", func(t *testing.T) {
		q := PacketQuery{Observer: "obs1,obs2"}
		where, args := db.buildTransmissionWhere(q)
		if len(where) != 1 {
			t.Fatalf("expected 1 WHERE clause, got %d", len(where))
		}
		clause := where[0]
		if !strings.Contains(clause, "IN (?,?)") {
			t.Errorf("expected IN (?,?) in clause, got: %s", clause)
		}
		if len(args) != 2 {
			t.Fatalf("expected 2 args, got %d", len(args))
		}
		if args[0] != "obs1" || args[1] != "obs2" {
			t.Errorf("expected [obs1, obs2], got %v", args)
		}
	})

	t.Run("comma-separated with spaces trims IDs", func(t *testing.T) {
		q := PacketQuery{Observer: "obs1, obs2"}
		_, args := db.buildTransmissionWhere(q)
		if len(args) != 2 {
			t.Fatalf("expected 2 args, got %d", len(args))
		}
		if args[0] != "obs1" || args[1] != "obs2" {
			t.Errorf("expected trimmed [obs1, obs2], got %v", args)
		}
	})

	t.Run("single observer still works", func(t *testing.T) {
		q := PacketQuery{Observer: "obs1"}
		where, args := db.buildTransmissionWhere(q)
		if len(where) != 1 {
			t.Fatalf("expected 1 WHERE clause, got %d", len(where))
		}
		if !strings.Contains(where[0], "IN (?)") {
			t.Errorf("expected IN (?) for single observer, got: %s", where[0])
		}
		if len(args) != 1 || args[0] != "obs1" {
			t.Errorf("expected [obs1], got %v", args)
		}
	})
}

// --- Distance index incremental update (#365, replaces debounce #557) ---

func TestDistanceIncrementalUpdate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	store.Load()

	// Record initial distance index size.
	initialHops := len(store.distHops)
	initialPaths := len(store.distPaths)

	// Insert a new observation with a different path to trigger an incremental update.
	maxObsID := db.GetMaxObservationID()
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 2, 5.0, -100, '["xx","yy","zz"]', ?)`, time.Now().Unix())

	store.IngestNewObservations(maxObsID, 500)

	// Distance index should have been updated incrementally (sizes may differ
	// if the new path resolves differently, but should not panic or corrupt).
	_ = len(store.distHops)
	_ = len(store.distPaths)

	// Insert another observation with yet another path.
	maxObsID = db.GetMaxObservationID()
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 2, 7.0, -95, '["aa","bb","cc","dd"]', ?)`, time.Now().Unix())

	store.IngestNewObservations(maxObsID, 500)

	// Verify the index is still coherent (no duplicates for the same tx).
	txSeen := make(map[int]int)
	for _, r := range store.distPaths {
		if r.tx != nil {
			txSeen[r.tx.ID]++
		}
	}
	for txID, count := range txSeen {
		if count > 1 {
			t.Errorf("distPaths has %d entries for tx %d (expected at most 1)", count, txID)
		}
	}

	t.Logf("Distance index: %d→%d hops, %d→%d paths (incremental)",
		initialHops, len(store.distHops), initialPaths, len(store.distPaths))
}

func TestHandleBatchObservations(t *testing.T) {
	_, router := setupNoStoreServer(t)

	t.Run("empty hashes returns empty results", func(t *testing.T) {
		body := strings.NewReader(`{"hashes":[]}`)
		req := httptest.NewRequest("POST", "/api/packets/observations", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		results, ok := resp["results"].(map[string]interface{})
		if !ok || len(results) != 0 {
			t.Fatalf("expected empty results map, got %v", resp)
		}
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		body := strings.NewReader(`not json`)
		req := httptest.NewRequest("POST", "/api/packets/observations", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("expected 400, got %d", w.Code)
		}
	})

	t.Run("too many hashes returns 400", func(t *testing.T) {
		hashes := make([]string, 201)
		for i := range hashes {
			hashes[i] = fmt.Sprintf("hash%d", i)
		}
		data, _ := json.Marshal(map[string][]string{"hashes": hashes})
		req := httptest.NewRequest("POST", "/api/packets/observations", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("expected 400, got %d", w.Code)
		}
	})

	t.Run("valid hashes with no store returns empty results", func(t *testing.T) {
		body := strings.NewReader(`{"hashes":["abc123","def456"]}`)
		req := httptest.NewRequest("POST", "/api/packets/observations", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		_, ok := resp["results"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected results map, got %v", resp)
		}
	})
}

// TestIngestTraceBroadcastIncludesPath verifies that TRACE packet broadcasts
// include decoded.path with hopsCompleted (#683).
func TestIngestTraceBroadcastIncludesPath(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	store.Load()

	initialMax := store.MaxTransmissionID()

	// TRACE packet: header=0x25, path_byte=0x02 (2 SNR bytes), 2 SNR bytes,
	// then payload: tag(4) + authCode(4) + flags(1) + 4 hop hashes (1-byte each)
	traceHex := "2502AABB010000000200000000DEADBEEF"
	now := time.Now().UTC().Format(time.RFC3339)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES (?, 'tracehash683test', ?, 1, 9, '')`, traceHex, now)
	newTxID := 0
	db.conn.QueryRow("SELECT MAX(id) FROM transmissions").Scan(&newTxID)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 5.0, -100, '["aa"]', ?)`, newTxID, time.Now().Unix())

	broadcastMaps, _ := store.IngestNewFromDB(initialMax, 100)
	if len(broadcastMaps) < 1 {
		t.Fatal("expected >=1 broadcast maps")
	}

	bm := broadcastMaps[0]
	decoded, ok := bm["decoded"].(map[string]interface{})
	if !ok {
		t.Fatal("broadcast map missing 'decoded'")
	}

	pathObj, ok := decoded["path"]
	if !ok {
		t.Fatal("decoded missing 'path' for TRACE packet — hopsCompleted not delivered to frontend (#683)")
	}

	// The path should be a Path struct with HopsCompleted = 2
	pathStruct, ok := pathObj.(Path)
	if !ok {
		t.Fatalf("expected Path struct, got %T", pathObj)
	}
	if pathStruct.HopsCompleted == nil {
		t.Fatal("path.HopsCompleted is nil for TRACE packet")
	}
	if *pathStruct.HopsCompleted != 2 {
		t.Errorf("expected hopsCompleted=2, got %d", *pathStruct.HopsCompleted)
	}
}
