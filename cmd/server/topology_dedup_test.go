package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestTopologyDedup_RepeatersMergeByPubkey verifies that topRepeaters
// merges entries whose hop prefixes resolve unambiguously to the same node.
func TestTopologyDedup_RepeatersMergeByPubkey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	exec := func(s string) {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("SQL exec failed: %v\nSQL: %s", err, s)
		}
	}
	exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT
	)`)
	exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT, observer_name TEXT,
		direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp TEXT, raw_hex TEXT
	)`)
	exec(`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`)
	exec(`CREATE TABLE nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
		last_seen TEXT, frequency REAL
	)`)
	exec(`CREATE TABLE schema_version (version INTEGER)`)
	exec(`INSERT INTO schema_version (version) VALUES (1)`)
	exec(`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`)

	// Insert two repeater nodes with distinct pubkeys.
	// AQUA: pubkey starts with 0735bc...
	// BETA: pubkey starts with 99aabb...
	exec(`INSERT INTO nodes (public_key, name, role) VALUES ('0735bc6dda4d1122aabbccdd', 'AQUA', 'Repeater')`)
	exec(`INSERT INTO nodes (public_key, name, role) VALUES ('99aabb001122334455667788', 'BETA', 'Repeater')`)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Create packets:
	// - 10 packets with path ["07", "99aa"] (short prefix for AQUA, medium for BETA)
	// - 5 packets with path ["0735bc", "99"] (medium prefix for AQUA, short for BETA)
	// - 3 packets with path ["0735bc6dda4d", "99aabb"] (long prefix for both)
	txID := 1
	obsID := 1
	insertTx := func(path string, count int) {
		for i := 0; i < count; i++ {
			ts := base.Add(time.Duration(txID) * time.Minute).Format(time.RFC3339)
			hash := fmt.Sprintf("h%04d", txID)
			conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json) VALUES (?, ?, ?, ?, 0, 4, 1, ?)",
				txID, "aabb", hash, ts, fmt.Sprintf(`{"pubKey":"pk%04d"}`, txID))
			conn.Exec("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				obsID, txID, "obs1", "Obs1", "RX", -10.0, -80.0, 5, path, ts)
			txID++
			obsID++
		}
	}

	insertTx(`["07","99aa"]`, 10)
	insertTx(`["0735bc","99"]`, 5)
	insertTx(`["0735bc6d","99aabb"]`, 3)

	// Total: AQUA appears as "07" (10×), "0735bc" (5×), "0735bc6d" (3×) = 18 total
	// Total: BETA appears as "99aa" (10×), "99" (5×), "99aabb" (3×) = 18 total
	// After dedup, each should appear ONCE with count=18.

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 100})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	result := store.computeAnalyticsTopology("", "", TimeWindow{})
	topRepeaters := result["topRepeaters"].([]map[string]interface{})

	// Build a map of pubkey → total count from topRepeaters
	pubkeyCounts := map[string]int{}
	for _, entry := range topRepeaters {
		pk, _ := entry["pubkey"].(string)
		if pk == "" {
			continue
		}
		pubkeyCounts[pk] += entry["count"].(int)
	}

	// Each pubkey should appear exactly once in topRepeaters
	aquaEntries := 0
	betaEntries := 0
	for _, entry := range topRepeaters {
		pk, _ := entry["pubkey"].(string)
		if pk == "0735bc6dda4d1122aabbccdd" {
			aquaEntries++
		}
		if pk == "99aabb001122334455667788" {
			betaEntries++
		}
	}

	if aquaEntries != 1 {
		t.Errorf("AQUA should appear exactly once in topRepeaters after dedup, got %d entries", aquaEntries)
		for _, e := range topRepeaters {
			t.Logf("  entry: hop=%v name=%v pubkey=%v count=%v", e["hop"], e["name"], e["pubkey"], e["count"])
		}
	}
	if betaEntries != 1 {
		t.Errorf("BETA should appear exactly once in topRepeaters after dedup, got %d entries", betaEntries)
	}

	// Check that the merged count is correct (18 each)
	if c := pubkeyCounts["0735bc6dda4d1122aabbccdd"]; c != 18 {
		t.Errorf("AQUA total count should be 18, got %d", c)
	}
	if c := pubkeyCounts["99aabb001122334455667788"]; c != 18 {
		t.Errorf("BETA total count should be 18, got %d", c)
	}
}

// TestTopologyDedup_AmbiguousPrefixNotMerged verifies that ambiguous short
// prefixes (matching multiple nodes) are NOT merged — they stay separate.
func TestTopologyDedup_AmbiguousPrefixNotMerged(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	exec := func(s string) {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("SQL exec failed: %v\nSQL: %s", err, s)
		}
	}
	exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT
	)`)
	exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT, observer_name TEXT,
		direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp TEXT, raw_hex TEXT
	)`)
	exec(`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`)
	exec(`CREATE TABLE nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
		last_seen TEXT, frequency REAL
	)`)
	exec(`CREATE TABLE schema_version (version INTEGER)`)
	exec(`INSERT INTO schema_version (version) VALUES (1)`)
	exec(`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`)

	// Two nodes whose pubkeys share the prefix "ab" — collision!
	exec(`INSERT INTO nodes (public_key, name, role) VALUES ('ab11223344556677aabbccdd', 'NODE_A', 'Repeater')`)
	exec(`INSERT INTO nodes (public_key, name, role) VALUES ('ab99887766554433aabbccdd', 'NODE_B', 'Repeater')`)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	txID := 1
	obsID := 1

	// 10 packets with hop "ab" — ambiguous (matches both NODE_A and NODE_B)
	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(txID) * time.Minute).Format(time.RFC3339)
		hash := fmt.Sprintf("h%04d", txID)
		conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json) VALUES (?, ?, ?, ?, 0, 4, 1, ?)",
			txID, "aabb", hash, ts, fmt.Sprintf(`{"pubKey":"pk%04d"}`, txID))
		conn.Exec("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			obsID, txID, "obs1", "Obs1", "RX", -10.0, -80.0, 5, `["ab"]`, ts)
		txID++
		obsID++
	}
	// 5 packets with hop "ab1122" — unambiguous (only NODE_A)
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(txID) * time.Minute).Format(time.RFC3339)
		hash := fmt.Sprintf("h%04d", txID)
		conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json) VALUES (?, ?, ?, ?, 0, 4, 1, ?)",
			txID, "aabb", hash, ts, fmt.Sprintf(`{"pubKey":"pk%04d"}`, txID))
		conn.Exec("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			obsID, txID, "obs1", "Obs1", "RX", -10.0, -80.0, 5, `["ab1122"]`, ts)
		txID++
		obsID++
	}

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 100})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	result := store.computeAnalyticsTopology("", "", TimeWindow{})
	topRepeaters := result["topRepeaters"].([]map[string]interface{})

	// "ab" is ambiguous — should NOT be merged with "ab1122"
	// We expect two separate entries: one for "ab" (count=10) and one for "ab1122" (count=5)
	foundAb := false
	foundAb1122 := false
	for _, entry := range topRepeaters {
		hop := entry["hop"].(string)
		count := entry["count"].(int)
		if hop == "ab" {
			foundAb = true
			if count != 10 {
				t.Errorf("ambiguous hop 'ab' should have count=10, got %d", count)
			}
		}
		if hop == "ab1122" {
			foundAb1122 = true
			if count != 5 {
				t.Errorf("unambiguous hop 'ab1122' should have count=5, got %d", count)
			}
		}
	}
	if !foundAb {
		t.Error("ambiguous hop 'ab' should remain as separate entry")
	}
	if !foundAb1122 {
		t.Error("unambiguous hop 'ab1122' should remain as separate entry (not merged with ambiguous 'ab')")
	}
}

// TestTopologyDedup_PairsMergeByPubkey verifies that topPairs merges
// pair entries whose hops resolve unambiguously to the same node pair.
func TestTopologyDedup_PairsMergeByPubkey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	exec := func(s string) {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("SQL exec failed: %v\nSQL: %s", err, s)
		}
	}
	exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT
	)`)
	exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT, observer_name TEXT,
		direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp TEXT, raw_hex TEXT
	)`)
	exec(`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`)
	exec(`CREATE TABLE nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
		last_seen TEXT, frequency REAL
	)`)
	exec(`CREATE TABLE schema_version (version INTEGER)`)
	exec(`INSERT INTO schema_version (version) VALUES (1)`)
	exec(`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`)

	exec(`INSERT INTO nodes (public_key, name, role) VALUES ('0735bc6dda4d1122aabbccdd', 'AQUA', 'Repeater')`)
	exec(`INSERT INTO nodes (public_key, name, role) VALUES ('99aabb001122334455667788', 'BETA', 'Repeater')`)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	txID := 1
	obsID := 1
	insertTx := func(path string, count int) {
		for i := 0; i < count; i++ {
			ts := base.Add(time.Duration(txID) * time.Minute).Format(time.RFC3339)
			hash := fmt.Sprintf("h%04d", txID)
			conn.Exec("INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json) VALUES (?, ?, ?, ?, 0, 4, 1, ?)",
				txID, "aabb", hash, ts, fmt.Sprintf(`{"pubKey":"pk%04d"}`, txID))
			conn.Exec("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				obsID, txID, "obs1", "Obs1", "RX", -10.0, -80.0, 5, path, ts)
			txID++
			obsID++
		}
	}

	// Path ["07","99aa"] → pair "07|99aa", 10 times
	// Path ["0735bc","99"] → pair "0735bc|99" but sorted = "0735bc|99", 5 times
	// Wait: pair sorting is by string comparison: "07" < "99aa", "0735bc" < "99"
	// After dedup both should merge to AQUA|BETA pair with count=15
	insertTx(`["07","99aa"]`, 10)
	insertTx(`["0735bc","99"]`, 5)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 100})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	result := store.computeAnalyticsTopology("", "", TimeWindow{})
	topPairs := result["topPairs"].([]map[string]interface{})

	// Should have exactly 1 pair entry for AQUA-BETA with count=15
	aquaBetaPairs := 0
	totalCount := 0
	for _, entry := range topPairs {
		pkA, _ := entry["pubkeyA"].(string)
		pkB, _ := entry["pubkeyB"].(string)
		if (pkA == "0735bc6dda4d1122aabbccdd" && pkB == "99aabb001122334455667788") ||
			(pkA == "99aabb001122334455667788" && pkB == "0735bc6dda4d1122aabbccdd") {
			aquaBetaPairs++
			totalCount += entry["count"].(int)
		}
	}

	if aquaBetaPairs != 1 {
		t.Errorf("AQUA-BETA pair should appear exactly once after dedup, got %d entries", aquaBetaPairs)
		for _, e := range topPairs {
			t.Logf("  pair: hopA=%v hopB=%v count=%v pkA=%v pkB=%v", e["hopA"], e["hopB"], e["count"], e["pubkeyA"], e["pubkeyB"])
		}
	}
	if totalCount != 15 {
		t.Errorf("AQUA-BETA pair total count should be 15, got %d", totalCount)
	}
}
