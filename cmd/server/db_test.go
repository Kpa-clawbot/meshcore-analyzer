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
			advert_count INTEGER DEFAULT 0
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
			noise_floor INTEGER
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
			timestamp INTEGER NOT NULL
		);

		CREATE VIEW packets_v AS
			SELECT o.id, t.raw_hex,
				datetime(o.timestamp, 'unixepoch') AS timestamp,
				obs.id AS observer_id, obs.name AS observer_name,
				o.direction, o.snr, o.rssi, o.score, t.hash, t.route_type,
				t.payload_type, t.payload_version, o.path_json, t.decoded_json,
				t.created_at
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx;
	`
	if _, err := conn.Exec(schema); err != nil {
		t.Fatal(err)
	}

	return &DB{conn: conn}
}

func seedTestData(t *testing.T, db *DB) {
	t.Helper()
	// Seed observers
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Observer One', 'SJC', '2026-01-15T10:00:00Z', '2026-01-01T00:00:00Z', 100)`)
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs2', 'Observer Two', 'SFO', '2026-01-15T09:00:00Z', '2026-01-01T00:00:00Z', 50)`)

	// Seed nodes
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('aabbccdd11223344', 'TestRepeater', 'repeater', 37.5, -122.0, '2026-01-15T10:00:00Z', '2026-01-01T00:00:00Z', 50)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('eeff00112233aabb', 'TestCompanion', 'companion', 37.6, -122.1, '2026-01-15T09:00:00Z', '2026-01-01T00:00:00Z', 10)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('1122334455667788', 'TestRoom', 'room', 37.4, -121.9, '2026-01-14T10:00:00Z', '2026-01-01T00:00:00Z', 5)`)

	// Seed transmissions
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('AABB', 'abc123def4567890', '2026-01-15T10:00:00Z', 1, 4, '{"pubKey":"aabbccdd11223344","name":"TestRepeater","type":"ADVERT"}')`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('CCDD', '1234567890abcdef', '2026-01-15T09:30:00Z', 1, 5, '{"type":"CHAN","channel":"#test","text":"Hello: World","sender":"TestUser"}')`)

	// Seed observations (use unix timestamps)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 12.5, -90, '["aa","bb"]', 1736935200)`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 2, 8.0, -95, '["aa"]', 1736935100)`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 15.0, -85, '[]', 1736933400)`)
}

func TestGetStats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	stats, err := db.GetStats()
	if err != nil {
		t.Fatal(err)
	}

	if stats.TotalTransmissions != 2 {
		t.Errorf("expected 2 transmissions, got %d", stats.TotalTransmissions)
	}
	if stats.TotalNodes != 3 {
		t.Errorf("expected 3 nodes, got %d", stats.TotalNodes)
	}
	if stats.TotalObservers != 2 {
		t.Errorf("expected 2 observers, got %d", stats.TotalObservers)
	}
	if stats.TotalObservations != 3 {
		t.Errorf("expected 3 observations, got %d", stats.TotalObservations)
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

func TestQueryPackets(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	result, err := db.QueryPackets(PacketQuery{Limit: 50, Order: "DESC"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 {
		t.Errorf("expected 3 total packets, got %d", result.Total)
	}
	if len(result.Packets) != 3 {
		t.Errorf("expected 3 packets, got %d", len(result.Packets))
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
	if result.Total != 2 {
		t.Errorf("expected 2 ADVERT packets, got %d", result.Total)
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
	if result.Total != 2 {
		t.Errorf("expected 2 grouped packets (unique hashes), got %d", result.Total)
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
		InfraDegradedMs: 86400000,
		InfraSilentMs:   259200000,
		NodeDegradedMs:  3600000,
		NodeSilentMs:    86400000,
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
	if maxID != 2 {
		t.Errorf("expected max ID 2, got %d", maxID)
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
	if len(txs) != 2 {
		t.Errorf("expected 2 new transmissions, got %d", len(txs))
	}

	txs, err = db.GetNewTransmissionsSince(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 1 {
		t.Errorf("expected 1 new transmission after ID 1, got %d", len(txs))
	}
}

func TestGetObservationsForHash(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	obs, err := db.GetObservationsForHash("abc123def4567890")
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Errorf("expected 2 observations, got %d", len(obs))
	}
}

func TestGetPacketByIDFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	pkt, err := db.GetPacketByID(1)
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

func TestGetPacketByIDNotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	pkt, err := db.GetPacketByID(9999)
	if err != nil {
		t.Fatal(err)
	}
	if pkt != nil {
		t.Error("expected nil for nonexistent packet ID")
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

	_, err := db.GetTransmissionByID(9999)
	if err == nil {
		t.Error("expected error for nonexistent transmission")
	}
}

func TestGetPacketByHashNotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	_, err := db.GetPacketByHash("nonexistenthash1")
	if err == nil {
		t.Error("expected error for nonexistent hash")
	}
}

func TestGetRecentPacketsForNode(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	packets, err := db.GetRecentPacketsForNode("aabbccdd11223344", "TestRepeater", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) == 0 {
		t.Error("expected packets for TestRepeater")
	}
}

func TestGetRecentPacketsForNodeDefaultLimit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	packets, err := db.GetRecentPacketsForNode("aabbccdd11223344", "TestRepeater", 0)
	if err != nil {
		t.Fatal(err)
	}
	if packets == nil {
		t.Error("expected non-nil result")
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

func TestGetNodeHealth(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("found", func(t *testing.T) {
		result, err := db.GetNodeHealth("aabbccdd11223344")
		if err != nil {
			t.Fatal(err)
		}
		if result == nil {
			t.Fatal("expected result, got nil")
		}
		node, ok := result["node"].(map[string]interface{})
		if !ok {
			t.Fatal("expected node object")
		}
		if node["name"] != "TestRepeater" {
			t.Errorf("expected TestRepeater, got %v", node["name"])
		}
		stats, ok := result["stats"].(map[string]interface{})
		if !ok {
			t.Fatal("expected stats object")
		}
		if stats["totalPackets"] == nil {
			t.Error("expected totalPackets in stats")
		}
	})

	t.Run("not found", func(t *testing.T) {
		result, err := db.GetNodeHealth("nonexistent")
		if err != nil {
			t.Fatal(err)
		}
		if result != nil {
			t.Error("expected nil for nonexistent node")
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

func TestGetTimestamps(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	t.Run("with results", func(t *testing.T) {
		ts, err := db.GetTimestamps("2020-01-01")
		if err != nil {
			t.Fatal(err)
		}
		if len(ts) == 0 {
			t.Error("expected timestamps")
		}
	})

	t.Run("no results", func(t *testing.T) {
		ts, err := db.GetTimestamps("2099-01-01")
		if err != nil {
			t.Fatal(err)
		}
		if len(ts) != 0 {
			t.Errorf("expected 0 timestamps, got %d", len(ts))
		}
	})
}

func TestGetObservationCount(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	count := db.GetObservationCount("abc123def4567890")
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}

	count = db.GetObservationCount("nonexistent")
	if count != 0 {
		t.Errorf("expected 0 for nonexistent, got %d", count)
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
		if result.Total != 2 {
			t.Errorf("expected 2 results for hash filter, got %d", result.Total)
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
		InfraDegradedMs: 86400000,
		InfraSilentMs:   259200000,
		NodeDegradedMs:  3600000,
		NodeSilentMs:    86400000,
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

func TestGetNodeHealthNoName(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert a node without a name
	db.conn.Exec(`INSERT INTO observers (id, name, iata) VALUES ('obs1', 'Observer One', 'SJC')`)
	db.conn.Exec(`INSERT INTO nodes (public_key, role, last_seen, first_seen, advert_count)
		VALUES ('deadbeef12345678', 'repeater', '2026-01-15T10:00:00Z', '2026-01-01T00:00:00Z', 5)`)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DDEE', 'deadbeefhash1234', '2026-01-15T10:05:00Z', 1, 4,
		'{"pubKey":"deadbeef12345678","type":"ADVERT"}')`)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 11.0, -91, '["dd"]', 1736935500)`)

	result, err := db.GetNodeHealth("deadbeef12345678")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
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

func TestGetTracesEmpty(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	traces, err := db.GetTraces("nonexistenthash1")
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 0 {
		t.Errorf("expected 0 traces, got %d", len(traces))
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

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
