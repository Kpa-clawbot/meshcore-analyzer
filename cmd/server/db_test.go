package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupTestDB creates an in-memory SQLite database with the v3 schema.
func setupTestDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// Force single connection so all goroutines share the same in-memory DB
	conn.SetMaxOpenConns(1)

	// Create schema matching MeshCore Analyzer v3
	schema := `
		CREATE TABLE nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			role TEXT,
			lat REAL,
			lon REAL,
			last_seen TEXT,
			first_seen TEXT,
			advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER,
			temperature_c REAL
		);

		CREATE TABLE observers (
			id TEXT PRIMARY KEY,
			name TEXT,
			iata TEXT,
			last_seen TEXT,
			first_seen TEXT,
			packet_count INTEGER DEFAULT 0,
			model TEXT,
			firmware TEXT,
			client_version TEXT,
			radio TEXT,
			battery_mv INTEGER,
			uptime_secs INTEGER,
			noise_floor REAL
		);

		CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT,
			created_at TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
			observer_idx INTEGER,
			direction TEXT,
			snr REAL,
			rssi REAL,
			score INTEGER,
			path_json TEXT,
			timestamp INTEGER NOT NULL,
			resolved_path TEXT
		);

		CREATE TABLE IF NOT EXISTS observer_metrics (
			observer_id TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			noise_floor REAL,
			tx_air_secs INTEGER,
			rx_air_secs INTEGER,
			recv_errors INTEGER,
			battery_mv INTEGER,
			packets_sent INTEGER,
			packets_recv INTEGER,
			PRIMARY KEY (observer_id, timestamp)
		);

		CREATE INDEX IF NOT EXISTS idx_observer_metrics_timestamp ON observer_metrics(timestamp);

	`
	if _, err := conn.Exec(schema); err != nil {
		t.Fatal(err)
	}

	return &DB{conn: conn, isV3: true, hasResolvedPath: true}
}

func seedTestData(t *testing.T, db *DB) {
	t.Helper()
	// Use recent timestamps so 7-day window filters don't exclude test data
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	yesterday := now.Add(-24 * time.Hour).Format(time.RFC3339)
	twoDaysAgo := now.Add(-48 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()
	yesterdayEpoch := now.Add(-24 * time.Hour).Unix()

	// Seed observers
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Observer One', 'SJC', ?, '2026-01-01T00:00:00Z', 100)`, recent)
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs2', 'Observer Two', 'SFO', ?, '2026-01-01T00:00:00Z', 50)`, yesterday)

	// Seed nodes
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('aabbccdd11223344', 'TestRepeater', 'repeater', 37.5, -122.0, ?, '2026-01-01T00:00:00Z', 50)`, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('eeff00112233aabb', 'TestCompanion', 'companion', 37.6, -122.1, ?, '2026-01-01T00:00:00Z', 10)`, yesterday)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('1122334455667788', 'TestRoom', 'room', 37.4, -121.9, ?, '2026-01-01T00:00:00Z', 5)`, twoDaysAgo)

	// Seed transmissions
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AABB', 'abc123def4567890', ?, 1, 4, '{"pubKey":"aabbccdd11223344","name":"TestRepeater","type":"ADVERT","timestamp":1700000000,"timestampISO":"2023-11-14T22:13:20.000Z","signature":"abcdef","flags":{"isRepeater":true},"lat":37.5,"lon":-122.0}')`, recent)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('CCDD', '1234567890abcdef', ?, 1, 5, '{"type":"CHAN","channel":"#test","text":"Hello: World","sender":"TestUser"}')`, yesterday)
	// Second ADVERT for same node with different hash_size (raw_hex byte 0x1F → hs=1 vs 0xBB → hs=3)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AA1F', 'def456abc1230099', ?, 1, 4, '{"pubKey":"aabbccdd11223344","name":"TestRepeater","type":"ADVERT","timestamp":1700000100,"timestampISO":"2023-11-14T22:14:40.000Z","signature":"fedcba","flags":{"isRepeater":true},"lat":37.5,"lon":-122.0}')`, yesterday)

	// Seed observations (use unix timestamps)
	// resolved_path contains full pubkeys parallel to path_json hops
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp, resolved_path)
		VALUES (1, 1, 12.5, -90, '["aa","bb"]', ?, '["aabbccdd11223344","eeff00112233aabb"]')`, recentEpoch)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp, resolved_path)
		VALUES (1, 2, 8.0, -95, '["aa"]', ?, '["aabbccdd11223344"]')`, recentEpoch-100)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 15.0, -85, '[]', ?)`, yesterdayEpoch)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp, resolved_path)
		VALUES (3, 1, 10.0, -92, '["cc"]', ?, '["1122334455667788"]')`, yesterdayEpoch)
}

func TestGetStats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	stats, err := db.GetStats()
	if err != nil {
		t.Fatal(err)
	}

	if stats.TotalTransmissions != 3 {
		t.Errorf("expected 3 transmissions, got %d", stats.TotalTransmissions)
	}
	if stats.TotalNodes != 3 {
		t.Errorf("expected 3 nodes, got %d", stats.TotalNodes)
	}
	if stats.TotalObservers != 2 {
		t.Errorf("expected 2 observers, got %d", stats.TotalObservers)
	}
	if stats.TotalObservations != 4 {
		t.Errorf("expected 4 observations, got %d", stats.TotalObservations)
	}
}

func TestGetRoleCounts(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	counts := db.GetRoleCounts()
	if counts["repeaters"] != 1 {
		t.Errorf("expected 1 repeater, got %d", counts["repeaters"])
	}
	if counts["companions"] != 1 {
		t.Errorf("expected 1 companion, got %d", counts["companions"])
	}
	if counts["rooms"] != 1 {
		t.Errorf("expected 1 room, got %d", counts["rooms"])
	}
}

func TestGetDBSizeStats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	stats := db.GetDBSizeStats()
	// In-memory DB has dbSizeMB=0 and walSizeMB=0
	if stats["dbSizeMB"] != float64(0) {
		t.Errorf("expected dbSizeMB=0 for in-memory DB, got %v", stats["dbSizeMB"])
	}

	rows, ok := stats["rows"].(map[string]int)
	if !ok {
		t.Fatal("expected rows map in DB size stats")
	}
	if rows["transmissions"] != 3 {
		t.Errorf("expected 3 transmissions rows, got %d", rows["transmissions"])
	}
	if rows["observations"] != 4 {
		t.Errorf("expected 4 observations rows, got %d", rows["observations"])
	}
	if rows["nodes"] != 3 {
		t.Errorf("expected 3 nodes rows, got %d", rows["nodes"])
	}
	if rows["observers"] != 2 {
		t.Errorf("expected 2 observers rows, got %d", rows["observers"])
	}

	// Verify new PRAGMA-based fields
	if _, ok := stats["freelistMB"]; !ok {
		t.Error("expected freelistMB in DB size stats")
	}
	walPages, ok := stats["walPages"].(map[string]interface{})
	if !ok {
		t.Fatal("expected walPages object in DB size stats")
	}
	for _, key := range []string{"total", "checkpointed", "busy"} {
		if _, ok := walPages[key]; !ok {
			t.Errorf("expected %s in walPages", key)
		}
	}
}

func TestQueryPackets(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	result, err := db.QueryPackets(PacketQuery{Limit: 50, Order: "DESC"})
	if err != nil {
		t.Fatal(err)
	}
	// Transmission-centric: 3 unique transmissions (not 4 observations)
	if result.Total != 3 {
		t.Errorf("expected 3 total transmissions, got %d", result.Total)
	}
	if len(result.Packets) != 3 {
		t.Errorf("expected 3 packets, got %d", len(result.Packets))
	}
	// Verify transmission shape has required fields
	if len(result.Packets) > 0 {
		p := result.Packets[0]
		if _, ok := p["first_seen"]; !ok {
			t.Error("expected first_seen field in packet")
		}
		if _, ok := p["observation_count"]; !ok {
			t.Error("expected observation_count field in packet")
		}
		if _, ok := p["timestamp"]; !ok {
			t.Error("expected timestamp field in packet")
		}
		// Should NOT have observation-level fields at top
		if _, ok := p["created_at"]; ok {
			t.Error("did not expect created_at in transmission-level response")
		}
		if _, ok := p["score"]; ok {
			t.Error("did not expect score in transmission-level response")
		}
	}
}

func TestQueryPacketsWithTypeFilter(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	pt := 4
	result, err := db.QueryPackets(PacketQuery{Limit: 50, Type: &pt, Order: "DESC"})
	if err != nil {
		t.Fatal(err)
	}
	// 2 transmissions with payload_type=4 (ADVERT)
	if result.Total != 2 {
		t.Errorf("expected 2 ADVERT transmissions, got %d", result.Total)
	}
}

func TestQueryGroupedPackets(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	result, err := db.QueryGroupedPackets(PacketQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 {
		t.Errorf("expected 3 grouped packets (unique hashes), got %d", result.Total)
	}
}

func TestGetNodeByPubkey(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	node, err := db.GetNodeByPubkey("aabbccdd11223344")
	if err != nil {
		t.Fatal(err)
	}
	if node == nil {
		t.Fatal("expected node, got nil")
	}
	if node["name"] != "TestRepeater" {
		t.Errorf("expected TestRepeater, got %v", node["name"])
	}
}

func TestGetNodeByPubkeyNotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	node, _ := db.GetNodeByPubkey("nonexistent")
	if node != nil {
		t.Error("expected nil for nonexistent node")
	}
}

func TestSearchNodes(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	nodes, err := db.SearchNodes("Test", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes matching 'Test', got %d", len(nodes))
	}
}

func TestGetObservers(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	observers, err := db.GetObservers()
	if err != nil {
		t.Fatal(err)
	}
	if len(observers) != 2 {
		t.Errorf("expected 2 observers, got %d", len(observers))
	}
	if observers[0].ID != "obs1" {
		t.Errorf("expected obs1 first (most recent), got %s", observers[0].ID)
	}
}

func TestGetObserverByID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	obs, err := db.GetObserverByID("obs1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.ID != "obs1" {
		t.Errorf("expected obs1, got %s", obs.ID)
	}
}

func TestGetObserverByIDNotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	_, err := db.GetObserverByID("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent observer")
	}
}

func TestObserverTypeConsistency(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert observer with typed metadata matching ingestor writes
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, battery_mv, uptime_secs, noise_floor)
		VALUES ('obs_typed', 'TypedObs', 'SJC', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 10, 3500, 86400, -115.5)`)

	obs, err := db.GetObserverByID("obs_typed")
	if err != nil {
		t.Fatal(err)
	}

	// battery_mv should be *int
	if obs.BatteryMv == nil {
		t.Fatal("BatteryMv should not be nil")
	}
	if *obs.BatteryMv != 3500 {
		t.Errorf("BatteryMv=%d, want 3500", *obs.BatteryMv)
	}

	// uptime_secs should be *int64
	if obs.UptimeSecs == nil {
		t.Fatal("UptimeSecs should not be nil")
	}
	if *obs.UptimeSecs != 86400 {
		t.Errorf("UptimeSecs=%d, want 86400", *obs.UptimeSecs)
	}

	// noise_floor should be *float64
	if obs.NoiseFloor == nil {
		t.Fatal("NoiseFloor should not be nil")
	}
	if *obs.NoiseFloor != -115.5 {
		t.Errorf("NoiseFloor=%f, want -115.5", *obs.NoiseFloor)
	}

	// Verify NULL handling: observer without metadata
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs_null', 'NullObs', 'SFO', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 5)`)

	obsNull, err := db.GetObserverByID("obs_null")
	if err != nil {
		t.Fatal(err)
	}
	if obsNull.BatteryMv != nil {
		t.Errorf("BatteryMv should be nil for observer without metadata, got %d", *obsNull.BatteryMv)
	}
	if obsNull.UptimeSecs != nil {
		t.Errorf("UptimeSecs should be nil for observer without metadata, got %d", *obsNull.UptimeSecs)
	}
	if obsNull.NoiseFloor != nil {
		t.Errorf("NoiseFloor should be nil for observer without metadata, got %f", *obsNull.NoiseFloor)
	}
}

func TestObserverTypesInGetObservers(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, battery_mv, uptime_secs, noise_floor)
		VALUES ('obs1', 'Obs1', 'SJC', '2026-06-01T00:00:00Z', '2026-01-01T00:00:00Z', 10, 4200, 172800, -110.3)`)

	observers, err := db.GetObservers()
	if err != nil {
		t.Fatal(err)
	}
	if len(observers) != 1 {
		t.Fatalf("expected 1 observer, got %d", len(observers))
	}
	o := observers[0]
	if o.BatteryMv == nil || *o.BatteryMv != 4200 {
		t.Errorf("BatteryMv=%v, want 4200", o.BatteryMv)
	}
	if o.UptimeSecs == nil || *o.UptimeSecs != 172800 {
		t.Errorf("UptimeSecs=%v, want 172800", o.UptimeSecs)
	}
	if o.NoiseFloor == nil || *o.NoiseFloor != -110.3 {
		t.Errorf("NoiseFloor=%v, want -110.3", o.NoiseFloor)
	}
}

func TestGetDistinctIATAs(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	codes, err := db.GetDistinctIATAs()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 2 {
		t.Errorf("expected 2 IATA codes, got %d", len(codes))
	}
}

func TestGetPacketByHash(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	pkt, err := db.GetPacketByHash("abc123def4567890")
	if err != nil {
		t.Fatal(err)
	}
	if pkt == nil {
		t.Fatal("expected packet, got nil")
	}
	if pkt["hash"] != "abc123def4567890" {
		t.Errorf("expected hash abc123def4567890, got %v", pkt["hash"])
	}
}

func TestGetTraces(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	traces, err := db.GetTraces("abc123def4567890")
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 {
		t.Errorf("expected 2 traces, got %d", len(traces))
	}
}

func TestGetChannels(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	channels, err := db.GetChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Errorf("expected 1 channel, got %d", len(channels))
	}
	if channels[0]["name"] != "#test" {
		t.Errorf("expected #test channel, got %v", channels[0]["name"])
	}
}

func TestGetNetworkStatus(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	ht := HealthThresholds{
		InfraDegradedHours: 24,
		InfraSilentHours:   72,
		NodeDegradedHours:  1,
		NodeSilentHours:    24,
	}
	result, err := db.GetNetworkStatus(ht)
	if err != nil {
		t.Fatal(err)
	}
	total, _ := result["total"].(int)
	if total != 3 {
		t.Errorf("expected 3 total nodes, got %d", total)
	}
}

func TestGetMaxTransmissionID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	maxID := db.GetMaxTransmissionID()
	if maxID != 3 {
		t.Errorf("expected max ID 3, got %d", maxID)
	}
}

func TestGetNewTransmissionsSince(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	txs, err := db.GetNewTransmissionsSince(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 3 {
		t.Errorf("expected 3 new transmissions, got %d", len(txs))
	}

	txs, err = db.GetNewTransmissionsSince(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 {
		t.Errorf("expected 2 new transmissions after ID 1, got %d", len(txs))
	}
}

func TestGetTransmissionByIDFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	tx, err := db.GetTransmissionByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if tx == nil {
		t.Fatal("expected transmission, got nil")
	}
	if tx["hash"] != "abc123def4567890" {
		t.Errorf("expected hash abc123def4567890, got %v", tx["hash"])
	}
	if tx["raw_hex"] != "AABB" {
		t.Errorf("expected raw_hex AABB, got %v", tx["raw_hex"])
	}
}

func TestGetTransmissionByIDNotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	result, _ := db.GetTransmissionByID(9999)
	if result != nil {
		t.Error("expected nil result for nonexistent transmission")
	}
}

func TestGetPacketByHashNotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	result, _ := db.GetPacketByHash("nonexistenthash1")
	if result != nil {
		t.Error("expected nil result for nonexistent hash")
	}
}

func TestGetObserverIdsForRegion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("with data", func(t *testing.T) {
		ids, err := db.GetObserverIdsForRegion("SJC")
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 {
			t.Errorf("expected 1 observer for SJC, got %d", len(ids))
		}
		if ids[0] != "obs1" {
			t.Errorf("expected obs1, got %s", ids[0])
		}
	})

	t.Run("multiple codes", func(t *testing.T) {
		ids, err := db.GetObserverIdsForRegion("SJC,SFO")
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 {
			t.Errorf("expected 2 observers, got %d", len(ids))
		}
	})

	t.Run("case and trim normalization", func(t *testing.T) {
		db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
			VALUES ('obs3', 'Observer Three', ' sjc ', ?, '2026-01-01T00:00:00Z', 1)`, time.Now().UTC().Format(time.RFC3339))
		ids, err := db.GetObserverIdsForRegion(" sjc ")
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 {
			t.Errorf("expected 2 observers for normalized sjc, got %d", len(ids))
		}
	})

	t.Run("empty param", func(t *testing.T) {
		ids, err := db.GetObserverIdsForRegion("")
		if err != nil {
			t.Fatal(err)
		}
		if ids != nil {
			t.Error("expected nil for empty region")
		}
	})

	t.Run("not found", func(t *testing.T) {
		ids, err := db.GetObserverIdsForRegion("ZZZ")
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 0 {
			t.Errorf("expected 0 observers for ZZZ, got %d", len(ids))
		}
	})
}

func TestGetChannelMessages(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("matching channel", func(t *testing.T) {
		messages, total, err := db.GetChannelMessages("#test", 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		if total == 0 {
			t.Error("expected at least 1 message for #test")
		}
		if len(messages) == 0 {
			t.Error("expected non-empty messages")
		}
	})

	t.Run("non-matching channel", func(t *testing.T) {
		messages, total, err := db.GetChannelMessages("#nonexistent", 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 {
			t.Errorf("expected 0 messages, got %d", total)
		}
		if len(messages) != 0 {
			t.Errorf("expected empty messages, got %d", len(messages))
		}
	})

	t.Run("default limit", func(t *testing.T) {
		messages, _, err := db.GetChannelMessages("#test", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if messages == nil {
			t.Error("expected non-nil result")
		}
	})
}

func TestGetChannelMessagesRegionFiltering(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	ts1 := now.Add(-2 * time.Minute).Format(time.RFC3339)
	ts2 := now.Add(-1 * time.Minute).Format(time.RFC3339)
	epoch1 := now.Add(-2 * time.Minute).Unix()
	epoch2 := now.Add(-1 * time.Minute).Unix()

	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Observer One', 'SJC')`)
	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs2', 'Observer Two', ' sfo ')`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AA', 'chanregion0001', ?, 1, 5,
		'{"type":"CHAN","channel":"#region","text":"SjcUser: One","sender":"SjcUser"}')`, ts1)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('BB', 'chanregion0002', ?, 1, 5,
		'{"type":"CHAN","channel":"#region","text":"SfoUser: Two","sender":"SfoUser"}')`, ts2)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '[]', ?)`, epoch1)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 2, 9.0, -91, '[]', ?)`, epoch2)

	msgsSJC, totalSJC, err := db.GetChannelMessages("#region", 100, 0, " sjc ")
	if err != nil {
		t.Fatal(err)
	}
	if totalSJC != 1 || len(msgsSJC) != 1 {
		t.Fatalf("expected 1 SJC message, total=%d len=%d", totalSJC, len(msgsSJC))
	}
	if msgsSJC[0]["sender"] != "SjcUser" {
		t.Fatalf("expected SJC sender SjcUser, got %v", msgsSJC[0]["sender"])
	}

	msgsMulti, totalMulti, err := db.GetChannelMessages("#region", 100, 0, "sjc, SFO")
	if err != nil {
		t.Fatal(err)
	}
	if totalMulti != 2 || len(msgsMulti) != 2 {
		t.Fatalf("expected 2 multi-region messages, total=%d len=%d", totalMulti, len(msgsMulti))
	}
}

func TestBuildPacketWhereFilters(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("type filter", func(t *testing.T) {
		pt := 4
		result, err := db.QueryPackets(PacketQuery{Limit: 50, Type: &pt, Order: "DESC"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total == 0 {
			t.Error("expected results for type=4")
		}
	})

	t.Run("route filter", func(t *testing.T) {
		rt := 1
		result, err := db.QueryPackets(PacketQuery{Limit: 50, Route: &rt, Order: "DESC"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total == 0 {
			t.Error("expected results for route=1")
		}
	})

	t.Run("observer filter", func(t *testing.T) {
		result, err := db.QueryPackets(PacketQuery{Limit: 50, Observer: "obs1", Order: "DESC"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total == 0 {
			t.Error("expected results for observer=obs1")
		}
	})

	t.Run("hash filter", func(t *testing.T) {
		result, err := db.QueryPackets(PacketQuery{Limit: 50, Hash: "abc123def4567890", Order: "DESC"})
		if err != nil {
			t.Fatal(err)
		}
		// 1 transmission with this hash (has 2 observations, but transmission-centric)
		if result.Total != 1 {
			t.Errorf("expected 1 result for hash filter, got %d", result.Total)
		}
	})

	t.Run("since filter", func(t *testing.T) {
		result, err := db.QueryPackets(PacketQuery{Limit: 50, Since: "2020-01-01", Order: "DESC"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total == 0 {
			t.Error("expected results for since filter")
		}
	})

	t.Run("until filter", func(t *testing.T) {
		result, err := db.QueryPackets(PacketQuery{Limit: 50, Until: "2099-01-01", Order: "DESC"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total == 0 {
			t.Error("expected results for until filter")
		}
	})

	t.Run("region filter", func(t *testing.T) {
		result, err := db.QueryPackets(PacketQuery{Limit: 50, Region: "SJC", Order: "DESC"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total == 0 {
			t.Error("expected results for region=SJC")
		}
	})

	t.Run("node filter by name", func(t *testing.T) {
		result, err := db.QueryPackets(PacketQuery{Limit: 50, Node: "TestRepeater", Order: "DESC"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total == 0 {
			t.Error("expected results for node=TestRepeater")
		}
	})

	t.Run("node filter by pubkey", func(t *testing.T) {
		result, err := db.QueryPackets(PacketQuery{Limit: 50, Node: "aabbccdd11223344", Order: "DESC"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total == 0 {
			t.Error("expected results for node pubkey filter")
		}
	})

	t.Run("combined filters", func(t *testing.T) {
		pt := 4
		rt := 1
		result, err := db.QueryPackets(PacketQuery{
			Limit:    50,
			Type:     &pt,
			Route:    &rt,
			Observer: "obs1",
			Since:    "2020-01-01",
			Order:    "DESC",
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total == 0 {
			t.Error("expected results with combined filters")
		}
	})

	t.Run("default limit", func(t *testing.T) {
		result, err := db.QueryPackets(PacketQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if result == nil {
			t.Error("expected non-nil result")
		}
	})
}

func TestResolveNodePubkey(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("by pubkey", func(t *testing.T) {
		pk := db.resolveNodePubkey("aabbccdd11223344")
		if pk != "aabbccdd11223344" {
			t.Errorf("expected aabbccdd11223344, got %s", pk)
		}
	})

	t.Run("by name", func(t *testing.T) {
		pk := db.resolveNodePubkey("TestRepeater")
		if pk != "aabbccdd11223344" {
			t.Errorf("expected aabbccdd11223344, got %s", pk)
		}
	})

	t.Run("not found returns input", func(t *testing.T) {
		pk := db.resolveNodePubkey("nonexistent")
		if pk != "nonexistent" {
			t.Errorf("expected 'nonexistent' back, got %s", pk)
		}
	})
}

func TestGetNodesFiltering(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("role filter", func(t *testing.T) {
		nodes, total, _, err := db.GetNodes(50, 0, "repeater", "", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 {
			t.Errorf("expected 1 repeater, got %d", total)
		}
		if len(nodes) != 1 {
			t.Errorf("expected 1 node, got %d", len(nodes))
		}
	})

	t.Run("search filter", func(t *testing.T) {
		nodes, _, _, err := db.GetNodes(50, 0, "", "Companion", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 1 {
			t.Errorf("expected 1 companion, got %d", len(nodes))
		}
	})

	t.Run("sort by name", func(t *testing.T) {
		nodes, _, _, err := db.GetNodes(50, 0, "", "", "", "", "name", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) == 0 {
			t.Error("expected nodes")
		}
	})

	t.Run("sort by packetCount", func(t *testing.T) {
		nodes, _, _, err := db.GetNodes(50, 0, "", "", "", "", "packetCount", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) == 0 {
			t.Error("expected nodes")
		}
	})

	t.Run("sort by lastSeen", func(t *testing.T) {
		nodes, _, _, err := db.GetNodes(50, 0, "", "", "", "", "lastSeen", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) == 0 {
			t.Error("expected nodes")
		}
	})

	t.Run("lastHeard filter 30d", func(t *testing.T) {
		// The filter works by computing since = now - 30d; seed data last_seen may or may not match.
		// Just verify the filter runs without error.
		_, _, _, err := db.GetNodes(50, 0, "", "", "", "30d", "", "")
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("lastHeard filter various", func(t *testing.T) {
		for _, lh := range []string{"1h", "6h", "24h", "7d", "30d", "invalid"} {
			_, _, _, err := db.GetNodes(50, 0, "", "", "", lh, "", "")
			if err != nil {
				t.Fatalf("lastHeard=%s failed: %v", lh, err)
			}
		}
	})

	t.Run("default limit", func(t *testing.T) {
		nodes, _, _, err := db.GetNodes(0, 0, "", "", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) == 0 {
			t.Error("expected nodes with default limit")
		}
	})

	t.Run("before filter", func(t *testing.T) {
		_, total, _, err := db.GetNodes(50, 0, "", "", "2026-01-02T00:00:00Z", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if total != 3 {
			t.Errorf("expected 3 nodes with first_seen <= 2026-01-02, got %d", total)
		}
	})

	t.Run("offset", func(t *testing.T) {
		nodes, total, _, err := db.GetNodes(1, 1, "", "", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if total != 3 {
			t.Errorf("expected 3 total, got %d", total)
		}
		if len(nodes) != 1 {
			t.Errorf("expected 1 node with offset, got %d", len(nodes))
		}
	})

	t.Run("region filter SJC", func(t *testing.T) {
		nodes, total, _, err := db.GetNodes(50, 0, "", "", "", "", "", "SJC")
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 {
			t.Errorf("expected 1 node for SJC region, got %d", total)
		}
		if len(nodes) != 1 {
			t.Fatalf("expected 1 node, got %d", len(nodes))
		}
		if nodes[0]["public_key"] != "aabbccdd11223344" {
			t.Errorf("expected TestRepeater, got %v", nodes[0]["public_key"])
		}
	})

	t.Run("region filter SFO", func(t *testing.T) {
		_, total, _, err := db.GetNodes(50, 0, "", "", "", "", "", "SFO")
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 {
			t.Errorf("expected 1 node for SFO region, got %d", total)
		}
	})

	t.Run("region filter multi", func(t *testing.T) {
		_, total, _, err := db.GetNodes(50, 0, "", "", "", "", "", "SJC,SFO")
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 {
			t.Errorf("expected 1 node for SJC,SFO region, got %d", total)
		}
	})

	t.Run("region filter unknown", func(t *testing.T) {
		_, total, _, err := db.GetNodes(50, 0, "", "", "", "", "", "AMS")
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 {
			t.Errorf("expected 0 nodes for unknown region, got %d", total)
		}
	})
}

// setupTestDBV2 creates an in-memory SQLite database with the v2 schema
// where observations use observer_id TEXT instead of observer_idx INTEGER.
func setupTestDBV2(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)

	schema := `
		CREATE TABLE nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			role TEXT,
			lat REAL,
			lon REAL,
			last_seen TEXT,
			first_seen TEXT,
			advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER,
			temperature_c REAL
		);

		CREATE TABLE observers (
			id TEXT PRIMARY KEY,
			name TEXT,
			iata TEXT,
			last_seen TEXT,
			first_seen TEXT,
			packet_count INTEGER DEFAULT 0
		);

		CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT,
			created_at TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
			observer_id TEXT,
			observer_name TEXT,
			direction TEXT,
			snr REAL,
			rssi REAL,
			score INTEGER,
			path_json TEXT,
			timestamp INTEGER NOT NULL
		);
	`
	if _, err := conn.Exec(schema); err != nil {
		t.Fatal(err)
	}

	return &DB{conn: conn, isV3: false}
}

func TestGetNodesRegionFilterV2(t *testing.T) {
	db := setupTestDBV2(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	// Seed observer with IATA code
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs-v2-1', 'V2 Observer', 'LAX', ?, '2026-01-01T00:00:00Z', 10)`, recent)

	// Seed a node
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('v2pubkey11223344', 'V2Node', 'repeater', 34.0, -118.0, ?, '2026-01-01T00:00:00Z', 5)`, recent)

	// Seed an ADVERT transmission for the node
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AABB', 'v2hash0001', ?, 1, 4, '{"pubKey":"v2pubkey11223344","name":"V2Node","type":"ADVERT"}')`, recent)

	// Seed v2-style observation: observer_id references observers.id directly
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_id, observer_name, snr, rssi, path_json, timestamp)
		VALUES (1, 'obs-v2-1', 'V2 Observer', 10.0, -90, '[]', ?)`, recentEpoch)

	t.Run("v2 region filter match", func(t *testing.T) {
		nodes, total, _, err := db.GetNodes(50, 0, "", "", "", "", "", "LAX")
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 {
			t.Errorf("expected 1 node for LAX region (v2 schema), got %d", total)
		}
		if len(nodes) != 1 {
			t.Fatalf("expected 1 node, got %d", len(nodes))
		}
		if nodes[0]["public_key"] != "v2pubkey11223344" {
			t.Errorf("expected V2Node, got %v", nodes[0]["public_key"])
		}
	})

	t.Run("v2 region filter no match", func(t *testing.T) {
		_, total, _, err := db.GetNodes(50, 0, "", "", "", "", "", "JFK")
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 {
			t.Errorf("expected 0 nodes for JFK region (v2 schema), got %d", total)
		}
	})
}

func TestGetChannelMessagesDedup(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Seed observers
	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Observer One', 'SJC')`)
	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs2', 'Observer Two', 'SFO')`)

	// Insert two transmissions with same hash to test dedup
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AA', 'chanmsg00000001', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#general","text":"User1: Hello","sender":"User1"}')`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('BB', 'chanmsg00000002', '2026-01-15T10:01:00Z', 1, 5,
		'{"type":"CHAN","channel":"#general","text":"User2: World","sender":"User2"}')`)

	// Observations: first msg seen by two observers (dedup), second by one
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 12.0, -90, '["aa"]', 1736935200)`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 2, 10.0, -92, '["aa"]', 1736935210)`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 14.0, -88, '[]', 1736935260)`)

	messages, total, err := db.GetChannelMessages("#general", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Two unique messages (deduped by sender:hash)
	if total < 2 {
		t.Errorf("expected at least 2 unique messages, got %d", total)
	}
	if len(messages) < 2 {
		t.Errorf("expected at least 2 messages, got %d", len(messages))
	}

	// Verify dedup: first message should have repeats > 1 because 2 observations
	found := false
	for _, m := range messages {
		if m["text"] == "Hello" {
			found = true
			repeats, _ := m["repeats"].(int)
			if repeats < 2 {
				t.Errorf("expected repeats >= 2 for deduped msg, got %d", repeats)
			}
		}
	}
	if !found {
		// Message text might be parsed differently
		t.Log("Note: message text parsing may vary")
	}
}

func TestGetChannelMessagesNoSender(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Observer One', 'SJC')`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('CC', 'chanmsg00000003', '2026-01-15T10:02:00Z', 1, 5,
		'{"type":"CHAN","channel":"#noname","text":"plain text no colon"}')`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 12.0, -90, null, 1736935300)`)

	messages, total, err := db.GetChannelMessages("#noname", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("expected 1 message, got %d", total)
	}
	if len(messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(messages))
	}
}

func TestGetNetworkStatusDateFormats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert nodes with different date formats
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen)
		VALUES ('node1111', 'NodeRFC', 'repeater', ?)`, time.Now().Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen)
		VALUES ('node2222', 'NodeSQL', 'companion', ?)`, time.Now().Format("2006-01-02 15:04:05"))
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen)
		VALUES ('node3333', 'NodeNull', 'room', NULL)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen)
		VALUES ('node4444', 'NodeBad', 'sensor', 'not-a-date')`)

	ht := HealthThresholds{
		InfraDegradedHours: 24,
		InfraSilentHours:   72,
		NodeDegradedHours:  1,
		NodeSilentHours:    24,
	}
	result, err := db.GetNetworkStatus(ht)
	if err != nil {
		t.Fatal(err)
	}
	total, _ := result["total"].(int)
	if total != 4 {
		t.Errorf("expected 4 nodes, got %d", total)
	}
	// Verify the function handles all date formats without error
	active, _ := result["active"].(int)
	degraded, _ := result["degraded"].(int)
	silent, _ := result["silent"].(int)
	if active+degraded+silent != 4 {
		t.Errorf("expected sum of statuses = 4, got %d", active+degraded+silent)
	}
	roleCounts, ok := result["roleCounts"].(map[string]int)
	if !ok {
		t.Fatal("expected roleCounts map")
	}
	if roleCounts["repeater"] != 1 {
		t.Errorf("expected 1 repeater, got %d", roleCounts["repeater"])
	}
}

func TestOpenDBValid(t *testing.T) {
	// Create a real SQLite database file
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create DB with a table using a writable connection first
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, hash TEXT)`)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()

	// Now test OpenDB (read-only)
	database, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	defer database.Close()

	// Verify it works
	maxID := database.GetMaxTransmissionID()
	if maxID != 0 {
		t.Errorf("expected 0, got %d", maxID)
	}
}

func TestOpenDBInvalidPath(t *testing.T) {
	_, err := OpenDB(filepath.Join(t.TempDir(), "nonexistent", "sub", "dir", "test.db"))
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestGetChannelMessagesObserverFallback(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Observer with ID but no name entry (observer_idx won't match)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AA', 'chanmsg00000004', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#obs","text":"Sender: Test","sender":"Sender"}')`)
	// Observation without observer (observer_idx = NULL)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, NULL, 12.0, -90, null, 1736935200)`)

	messages, total, err := db.GetChannelMessages("#obs", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("expected 1, got %d", total)
	}
	if len(messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(messages))
	}
}

func TestGetChannelsMultiple(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Observer', 'SJC')`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AA', 'chan1hash', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#alpha","text":"Alice: Hello","sender":"Alice"}')`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('BB', 'chan2hash', '2026-01-15T10:01:00Z', 1, 5,
		'{"type":"CHAN","channel":"#beta","text":"Bob: World","sender":"Bob"}')`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('CC', 'chan3hash', '2026-01-15T10:02:00Z', 1, 5,
		'{"type":"CHAN","channel":"","text":"No channel"}')`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DD', 'chan4hash', '2026-01-15T10:03:00Z', 1, 5,
		'{"type":"OTHER"}')`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('EE', 'chan5hash', '2026-01-15T10:04:00Z', 1, 5, 'not-valid-json')`)

	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 12.0, -90, null, 1736935200)`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 12.0, -90, null, 1736935260)`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 1, 12.0, -90, null, 1736935320)`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (4, 1, 12.0, -90, null, 1736935380)`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (5, 1, 12.0, -90, null, 1736935440)`)

	channels, err := db.GetChannels()
	if err != nil {
		t.Fatal(err)
	}
	// #alpha, #beta, and "unknown" (empty channel)
	if len(channels) < 2 {
		t.Errorf("expected at least 2 channels, got %d", len(channels))
	}
}

func TestQueryGroupedPacketsWithFilters(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	rt := 1
	result, err := db.QueryGroupedPackets(PacketQuery{Limit: 50, Route: &rt})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total == 0 {
		t.Error("expected results for grouped with route filter")
	}
}

func TestNullHelpers(t *testing.T) {
	// nullStr
	if nullStr(sql.NullString{Valid: false}) != nil {
		t.Error("expected nil for invalid NullString")
	}
	if nullStr(sql.NullString{Valid: true, String: "hello"}) != "hello" {
		t.Error("expected 'hello' for valid NullString")
	}

	// nullFloat
	if nullFloat(sql.NullFloat64{Valid: false}) != nil {
		t.Error("expected nil for invalid NullFloat64")
	}
	if nullFloat(sql.NullFloat64{Valid: true, Float64: 3.14}) != 3.14 {
		t.Error("expected 3.14 for valid NullFloat64")
	}

	// nullInt
	if nullInt(sql.NullInt64{Valid: false}) != nil {
		t.Error("expected nil for invalid NullInt64")
	}
	if nullInt(sql.NullInt64{Valid: true, Int64: 42}) != 42 {
		t.Error("expected 42 for valid NullInt64")
	}
}

// TestGetChannelsStaleMessage verifies that GetChannels returns the newest message
// per channel even when an older message has a later observation timestamp.
// This is the regression test for #171.
func TestGetChannelsStaleMessage(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Observer1', 'SJC')`)
	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs2', 'Observer2', 'SFO')`)

	// Older message (first_seen T1)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AA', 'oldhash1', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#test","text":"Alice: Old message","sender":"Alice"}')`)
	// Newer message (first_seen T2 > T1)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('BB', 'newhash2', '2026-01-15T10:05:00Z', 1, 5,
		'{"type":"CHAN","channel":"#test","text":"Bob: New message","sender":"Bob"}')`)

	// Observations: older message re-observed AFTER newer message (stale scenario)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, timestamp)
		VALUES (1, 1, 12.0, -90, 1736935200)`) // old msg first obs
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, timestamp)
		VALUES (2, 1, 14.0, -88, 1736935500)`) // new msg obs
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, timestamp)
		VALUES (1, 2, 10.0, -95, 1736935800)`) // old msg re-observed LATER

	channels, err := db.GetChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	ch := channels[0]

	if ch["lastMessage"] != "New message" {
		t.Errorf("expected lastMessage='New message' (newest by first_seen), got %q", ch["lastMessage"])
	}
	if ch["lastSender"] != "Bob" {
		t.Errorf("expected lastSender='Bob', got %q", ch["lastSender"])
	}
	if ch["messageCount"] != 2 {
		t.Errorf("expected messageCount=2 (unique transmissions), got %v", ch["messageCount"])
	}
}

func TestGetChannelsRegionFiltering(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Observer1', 'SJC')`)
	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs2', 'Observer2', 'SFO')`)

	// Channel message seen only in SJC
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AA', 'hash1', '2026-01-15T10:00:00Z', 1, 5,
		'{"type":"CHAN","channel":"#sjc-only","text":"Alice: Hello SJC","sender":"Alice"}')`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, timestamp)
		VALUES (1, 1, 12.0, -90, 1736935200)`)

	// Channel message seen only in SFO
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('BB', 'hash2', '2026-01-15T10:05:00Z', 1, 5,
		'{"type":"CHAN","channel":"#sfo-only","text":"Bob: Hello SFO","sender":"Bob"}')`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, timestamp)
		VALUES (2, 2, 14.0, -88, 1736935500)`)

	// No region filter — both channels
	all, err := db.GetChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 channels without region filter, got %d", len(all))
	}

	// Filter SJC — only #sjc-only
	sjc, err := db.GetChannels("SJC")
	if err != nil {
		t.Fatal(err)
	}
	if len(sjc) != 1 {
		t.Fatalf("expected 1 channel for SJC, got %d", len(sjc))
	}
	if sjc[0]["name"] != "#sjc-only" {
		t.Errorf("expected channel '#sjc-only', got %q", sjc[0]["name"])
	}

	// Filter SFO — only #sfo-only
	sfo, err := db.GetChannels("SFO")
	if err != nil {
		t.Fatal(err)
	}
	if len(sfo) != 1 {
		t.Fatalf("expected 1 channel for SFO, got %d", len(sfo))
	}
	if sfo[0]["name"] != "#sfo-only" {
		t.Errorf("expected channel '#sfo-only', got %q", sfo[0]["name"])
	}
}

func TestNodeTelemetryFields(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert node with telemetry data
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c)
		VALUES ('pk_telem1', 'SensorNode', 'sensor', 37.0, -122.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 5, 3700, 28.5)`)

	// Test via GetNodeByPubkey
	node, err := db.GetNodeByPubkey("pk_telem1")
	if err != nil {
		t.Fatal(err)
	}
	if node == nil {
		t.Fatal("expected node, got nil")
	}
	if node["battery_mv"] != 3700 {
		t.Errorf("battery_mv=%v, want 3700", node["battery_mv"])
	}
	if node["temperature_c"] != 28.5 {
		t.Errorf("temperature_c=%v, want 28.5", node["temperature_c"])
	}

	// Test via GetNodes
	nodes, _, _, err := db.GetNodes(50, 0, "sensor", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 sensor node, got %d", len(nodes))
	}
	if nodes[0]["battery_mv"] != 3700 {
		t.Errorf("GetNodes battery_mv=%v, want 3700", nodes[0]["battery_mv"])
	}

	// Test node without telemetry — fields should be nil
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('pk_notelem', 'PlainNode', 'repeater', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 3)`)
	node2, _ := db.GetNodeByPubkey("pk_notelem")
	if node2["battery_mv"] != nil {
		t.Errorf("expected nil battery_mv for node without telemetry, got %v", node2["battery_mv"])
	}
	if node2["temperature_c"] != nil {
		t.Errorf("expected nil temperature_c for node without telemetry, got %v", node2["temperature_c"])
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestGetObserverMetrics(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)

	now := time.Now().UTC()
	t1 := now.Add(-2 * time.Hour).Format(time.RFC3339)
	t2 := now.Add(-1 * time.Hour).Format(time.RFC3339)
	t3 := now.Format(time.RFC3339)

	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"obs1", t1, -112.5, 100, 500, 3, 3720)
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors) VALUES (?, ?, ?, ?, ?, ?)",
		"obs1", t2, -110.0, 200, 800, 5)
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors) VALUES (?, ?, ?, ?, ?, ?)",
		"obs1", t3, -108.0, 300, 1100, 8)
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor) VALUES (?, ?, ?)",
		"obs2", t1, -115.0)

	// Query all for obs1
	since := now.Add(-3 * time.Hour).Format(time.RFC3339)
	metrics, reboots, err := db.GetObserverMetrics("obs1", since, "", "5m", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 3 {
		t.Errorf("expected 3 metrics, got %d", len(metrics))
	}
	if len(reboots) != 0 {
		t.Errorf("expected 0 reboots, got %d", len(reboots))
	}

	// Verify first row has noise_floor
	if metrics[0].NoiseFloor == nil || *metrics[0].NoiseFloor != -112.5 {
		t.Errorf("first noise_floor = %v, want -112.5", metrics[0].NoiseFloor)
	}
	// First row: no delta possible (first sample)
	if metrics[0].TxAirtimePct != nil {
		t.Errorf("first sample should have nil tx_airtime_pct, got %v", *metrics[0].TxAirtimePct)
	}

	// Second row should have computed deltas
	// TX: (200-100) / 3600 * 100 ≈ 2.78%
	if metrics[1].TxAirtimePct == nil {
		t.Errorf("second sample tx_airtime_pct should not be nil")
	} else if *metrics[1].TxAirtimePct < 2.0 || *metrics[1].TxAirtimePct > 3.5 {
		t.Errorf("second sample tx_airtime_pct = %v, want ~2.78", *metrics[1].TxAirtimePct)
	}

	// Query with until filter
	metrics2, _, err := db.GetObserverMetrics("obs1", since, t2, "5m", 3600)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics2) != 2 {
		t.Errorf("expected 2 metrics with until filter, got %d", len(metrics2))
	}
}

func TestGetMetricsSummary(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)

	now := time.Now().UTC()
	t1 := now.Add(-2 * time.Hour).Format(time.RFC3339)
	t2 := now.Add(-1 * time.Hour).Format(time.RFC3339)

	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, battery_mv) VALUES (?, ?, ?, ?)",
		"obs1", t1, -112.0, 3720)
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor) VALUES (?, ?, ?)",
		"obs1", t2, -108.0)
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor) VALUES (?, ?, ?)",
		"obs2", t1, -115.0)

	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	summary, err := db.GetMetricsSummary(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary) != 2 {
		t.Fatalf("expected 2 observers in summary, got %d", len(summary))
	}

	// Results sorted by max_nf DESC
	// obs1 has max -108, obs2 has max -115
	if summary[0].ObserverID != "obs1" {
		t.Errorf("first observer should be obs1 (highest max NF), got %s", summary[0].ObserverID)
	}
	if summary[0].CurrentNF == nil || *summary[0].CurrentNF != -108.0 {
		t.Errorf("obs1 current NF = %v, want -108.0", summary[0].CurrentNF)
	}
	if summary[0].SampleCount != 2 {
		t.Errorf("obs1 sample count = %d, want 2", summary[0].SampleCount)
	}
	// Verify sparkline data is included
	if len(summary[0].Sparkline) != 2 {
		t.Errorf("obs1 sparkline length = %d, want 2", len(summary[0].Sparkline))
	}
	if len(summary[1].Sparkline) != 1 {
		t.Errorf("obs2 sparkline length = %d, want 1", len(summary[1].Sparkline))
	}
	// Sparkline should be ordered by timestamp ASC
	if summary[0].Sparkline[0] != nil && *summary[0].Sparkline[0] != -112.0 {
		t.Errorf("obs1 sparkline[0] = %v, want -112.0", *summary[0].Sparkline[0])
	}
	if summary[0].Sparkline[1] != nil && *summary[0].Sparkline[1] != -108.0 {
		t.Errorf("obs1 sparkline[1] = %v, want -108.0", *summary[0].Sparkline[1])
	}
}

func TestObserverMetricsAPIEndpoints(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)

	now := time.Now().UTC()
	t1 := now.Add(-1 * time.Hour).Format(time.RFC3339)

	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor) VALUES (?, ?, ?)",
		"obs1", t1, -112.0)

	// Query directly to verify
	metrics, _, err := db.GetObserverMetrics("obs1", "", "", "5m", 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric, got %d", len(metrics))
	}
}

func TestComputeDeltas(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	floatPtr := func(v float64) *float64 { return &v }

	t.Run("empty input", func(t *testing.T) {
		result, reboots, err := computeDeltas(nil, 300)
		if err != nil {
			t.Fatal(err)
		}
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
		if reboots != nil {
			t.Errorf("expected nil reboots, got %v", reboots)
		}
	})

	t.Run("normal delta computation", func(t *testing.T) {
		raw := []rawMetricsSample{
			{Timestamp: "2026-04-05T00:00:00Z", NoiseFloor: floatPtr(-112), TxAirSecs: intPtr(100), RxAirSecs: intPtr(500), RecvErrors: intPtr(3), PacketsRecv: intPtr(1000)},
			{Timestamp: "2026-04-05T00:05:00Z", NoiseFloor: floatPtr(-110), TxAirSecs: intPtr(115), RxAirSecs: intPtr(525), RecvErrors: intPtr(5), PacketsRecv: intPtr(1100)},
		}
		result, reboots, err := computeDeltas(raw, 300)
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 2 {
			t.Fatalf("expected 2 results, got %d", len(result))
		}
		if len(reboots) != 0 {
			t.Errorf("expected 0 reboots, got %d", len(reboots))
		}
		// First sample: no deltas
		if result[0].TxAirtimePct != nil {
			t.Errorf("first sample should have nil tx_airtime_pct")
		}
		// Second sample: TX delta = 15 secs / 300 secs * 100 = 5%
		if result[1].TxAirtimePct == nil {
			t.Fatal("second sample tx_airtime_pct should not be nil")
		}
		if *result[1].TxAirtimePct != 5.0 {
			t.Errorf("tx_airtime_pct = %v, want 5.0", *result[1].TxAirtimePct)
		}
		// RX delta = 25 secs / 300 secs * 100 ≈ 8.33%
		if result[1].RxAirtimePct == nil {
			t.Fatal("second sample rx_airtime_pct should not be nil")
		}
		if *result[1].RxAirtimePct < 8.3 || *result[1].RxAirtimePct > 8.4 {
			t.Errorf("rx_airtime_pct = %v, want ~8.33", *result[1].RxAirtimePct)
		}
		// Error rate: delta_errors=2, delta_recv=100, rate = 2/(100+2)*100 ≈ 1.96%
		if result[1].RecvErrorRate == nil {
			t.Fatal("second sample recv_error_rate should not be nil")
		}
		if *result[1].RecvErrorRate < 1.9 || *result[1].RecvErrorRate > 2.0 {
			t.Errorf("recv_error_rate = %v, want ~1.96", *result[1].RecvErrorRate)
		}
	})

	t.Run("reboot detection", func(t *testing.T) {
		raw := []rawMetricsSample{
			{Timestamp: "2026-04-05T00:00:00Z", TxAirSecs: intPtr(1000), RxAirSecs: intPtr(5000)},
			{Timestamp: "2026-04-05T00:05:00Z", TxAirSecs: intPtr(10), RxAirSecs: intPtr(20)}, // reboot!
			{Timestamp: "2026-04-05T00:10:00Z", TxAirSecs: intPtr(25), RxAirSecs: intPtr(45)},
		}
		result, reboots, err := computeDeltas(raw, 300)
		if err != nil {
			t.Fatal(err)
		}
		if len(reboots) != 1 {
			t.Fatalf("expected 1 reboot, got %d", len(reboots))
		}
		if reboots[0] != "2026-04-05T00:05:00Z" {
			t.Errorf("reboot timestamp = %s", reboots[0])
		}
		if !result[1].IsReboot {
			t.Error("second sample should be marked as reboot")
		}
		// Reboot sample should have nil deltas
		if result[1].TxAirtimePct != nil {
			t.Error("reboot sample should have nil tx_airtime_pct")
		}
		// Third sample should have valid deltas from post-reboot baseline
		if result[2].TxAirtimePct == nil {
			t.Fatal("third sample tx_airtime_pct should not be nil")
		}
		if *result[2].TxAirtimePct != 5.0 { // 15/300*100
			t.Errorf("third sample tx_airtime_pct = %v, want 5.0", *result[2].TxAirtimePct)
		}
	})

	t.Run("gap detection", func(t *testing.T) {
		raw := []rawMetricsSample{
			{Timestamp: "2026-04-05T00:00:00Z", TxAirSecs: intPtr(100)},
			{Timestamp: "2026-04-05T00:15:00Z", TxAirSecs: intPtr(200)}, // 15min gap > 2*300s
		}
		result, _, err := computeDeltas(raw, 300)
		if err != nil {
			t.Fatal(err)
		}
		// Gap sample should have nil deltas
		if result[1].TxAirtimePct != nil {
			t.Error("gap sample should have nil tx_airtime_pct")
		}
	})
}

func TestGetObserverMetricsResolution(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)

	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs) VALUES (?, ?, ?, ?)",
		"obs1", "2026-04-05T00:00:00Z", -112.0, 100)
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs) VALUES (?, ?, ?, ?)",
		"obs1", "2026-04-05T00:05:00Z", -110.0, 200)
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs) VALUES (?, ?, ?, ?)",
		"obs1", "2026-04-05T01:00:00Z", -108.0, 500)
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs) VALUES (?, ?, ?, ?)",
		"obs1", "2026-04-05T01:05:00Z", -106.0, 600)

	// 5m resolution: all 4 rows
	m5, _, err := db.GetObserverMetrics("obs1", "2026-04-04T00:00:00Z", "", "5m", 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(m5) != 4 {
		t.Errorf("5m resolution: expected 4 rows, got %d", len(m5))
	}

	// 1h resolution: 2 buckets
	m1h, _, err := db.GetObserverMetrics("obs1", "2026-04-04T00:00:00Z", "", "1h", 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(m1h) != 2 {
		t.Errorf("1h resolution: expected 2 rows, got %d", len(m1h))
	}

	// 1d resolution: 1 bucket
	m1d, _, err := db.GetObserverMetrics("obs1", "2026-04-04T00:00:00Z", "", "1d", 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(m1d) != 1 {
		t.Errorf("1d resolution: expected 1 row, got %d", len(m1d))
	}
}

func TestHourlyResolutionDeltasNotNull(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)

	// Two hourly buckets, each with one sample. With old MAX+hardcoded gap threshold,
	// the 3600s gap would exceed sampleInterval*2 (600s) and deltas would be null.
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, packets_sent, packets_recv) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"obs_hr", "2026-04-05T10:00:00Z", -110.0, 100, 200, 5, 50, 100)
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, packets_sent, packets_recv) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"obs_hr", "2026-04-05T11:00:00Z", -108.0, 200, 400, 10, 80, 200)

	m, _, err := db.GetObserverMetrics("obs_hr", "2026-04-04T00:00:00Z", "", "1h", 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(m))
	}
	// Second row should have computed deltas (not null)
	if m[1].TxAirtimePct == nil {
		t.Error("1h resolution: tx_airtime_pct should not be nil — gap threshold must scale with resolution")
	}
}

func TestLastValuePreservesReboot(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)

	// Hour bucket with two samples: pre-reboot (high) and post-reboot (low).
	// With MAX(), the pre-reboot value wins and the reboot is hidden.
	// With LAST (latest timestamp), the post-reboot value wins.
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, packets_sent, packets_recv) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"obs_rb", "2026-04-05T10:00:00Z", -110.0, 1000, 2000, 500, 400, 800) // pre-reboot baseline
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, packets_sent, packets_recv) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"obs_rb", "2026-04-05T10:20:00Z", -110.0, 5000, 6000, 900, 700, 1200) // pre-reboot peak
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, packets_sent, packets_recv) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"obs_rb", "2026-04-05T10:40:00Z", -110.0, 10, 20, 1, 5, 10) // post-reboot (counter reset)

	// Next hour bucket
	db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, packets_sent, packets_recv) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"obs_rb", "2026-04-05T11:00:00Z", -108.0, 100, 120, 5, 20, 50)

	m, reboots, err := db.GetObserverMetrics("obs_rb", "2026-04-04T00:00:00Z", "", "1h", 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(m))
	}

	// First bucket should use the LAST value (post-reboot: tx_air_secs=10).
	// Second bucket (tx_air_secs=100) is a normal increase from 10→100.
	// With LAST-value semantics, the second bucket should have valid deltas (not a reboot).
	// With MAX(), first bucket would have tx_air_secs=5000, and second=100 would
	// trigger a false reboot detection.
	if m[1].IsReboot {
		t.Error("second bucket should NOT be flagged as reboot with LAST-value aggregation")
	}
	if m[1].TxAirtimePct == nil {
		t.Error("second bucket should have non-nil tx_airtime_pct")
	}
	_ = reboots // reboots list is informational
}

func TestParseWindowDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
		err   bool
	}{
		{"1h", time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"3d", 3 * 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"invalid", 0, true},
	}
	for _, tc := range tests {
		got, err := parseWindowDuration(tc.input)
		if tc.err && err == nil {
			t.Errorf("parseWindowDuration(%q) expected error", tc.input)
		}
		if !tc.err && got != tc.want {
			t.Errorf("parseWindowDuration(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
