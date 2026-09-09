package main

// Regression for PR #1596 (issue #1009) chunked load: when transmission
// ids are anti-correlated with first_seen (e.g. id=1 has the NEWEST
// timestamp), LoadChunked walks id-ASC and the post-load
// `s.oldestLoaded = s.packets[0].FirstSeen` line set oldestLoaded to
// the NEWEST first_seen. QueryPackets then mis-routed any
// `since>=oldestLoaded` query to the SQL fallback, hiding fresh
// in-memory rows. This shows up in real life on the e2e fixture after
// tools/freshen-fixture.sh shifts timestamps so id=1 (originally
// loaded first) carries the most recent first_seen.
//
// The mobile e2e test test-observer-iata-1188-e2e.js fails as a
// result: with the default 15-minute time window, /api/packets returns
// 0 rows and the mobile DOM has no `tr[data-hash]` to tap.
//
// This test asserts the in-memory invariant: after LoadChunked,
// oldestLoaded must equal the actual oldest FirstSeen across loaded
// transmissions, not the FirstSeen of the first row in s.packets.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// createTestDBReverseTime builds numTx transmissions whose ids run
// 1..numTx ASC while first_seen runs newest..oldest (id=1 = newest).
// This mirrors the freshen-fixture-shifted e2e DB exactly.
func createTestDBReverseTime(tb testing.TB, dbPath string, numTx int) {
	tb.Helper()
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		tb.Fatal(err)
	}
	defer conn.Close()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS transmissions (
			id INTEGER PRIMARY KEY,
			raw_hex TEXT, hash TEXT, first_seen TEXT,
			route_type INTEGER, payload_type INTEGER,
			payload_version INTEGER, decoded_json TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS observations (
			id INTEGER PRIMARY KEY,
			transmission_id INTEGER, observer_id TEXT, observer_name TEXT,
			direction TEXT, snr REAL, rssi REAL, score INTEGER,
			path_json TEXT, timestamp TEXT, raw_hex TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
			last_seen TEXT, first_seen TEXT, frequency REAL
		)`,
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
		`CREATE INDEX IF NOT EXISTS idx_tx_first_seen ON transmissions(first_seen)`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			tb.Fatalf("setup exec: %v\nSQL: %s", err, s)
		}
	}

	txStmt, _ := conn.Prepare("INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
	obsStmt, _ := conn.Prepare("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
	defer txStmt.Close()
	defer obsStmt.Close()

	// id=1 is the NEWEST (now); id=numTx is the OLDEST (numTx minutes ago).
	now := time.Now().UTC().Truncate(time.Second)
	for i := 1; i <= numTx; i++ {
		ts := now.Add(-time.Duration(i-1) * time.Minute).Format(time.RFC3339)
		unixTs := now.Add(-time.Duration(i-1) * time.Minute).Unix()
		hash := fmt.Sprintf("h%04d", i)
		txStmt.Exec(i, "aabb", hash, ts, 0, 4, 1, fmt.Sprintf(`{"pubKey":"pk%04d"}`, i))
		obsStmt.Exec(i, i, "obs1", "Obs1", "RX", -10.0, -80.0, 5, `["aa","bb"]`, unixTs)
	}
}

func openReverseTimeStore(t *testing.T, numTx int) *PacketStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rev.db")
	createTestDBReverseTime(t, dbPath, numTx)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	cfg := &PacketStoreConfig{}
	return NewPacketStore(db, cfg)
}

// TestLoadChunked_OldestLoadedIsActualOldest: when LoadChunked walks
// transmissions in id-ASC order but timestamps are anti-correlated
// with id (PR #1596 regression scenario), oldestLoaded MUST be the
// minimum FirstSeen across loaded packets, not the first row's
// FirstSeen. Otherwise QueryPackets routes "since=15min ago" to SQL
// fallback, hiding fresh rows.
func TestLoadChunked_OldestLoadedIsActualOldest(t *testing.T) {
	store := openReverseTimeStore(t, 50)
	defer store.db.conn.Close()

	if err := store.LoadChunked(20); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	// Compute the actual oldest first_seen across what got loaded.
	if len(store.packets) == 0 {
		t.Fatal("no packets loaded")
	}
	actualOldest := store.packets[0].FirstSeen
	for _, p := range store.packets {
		if p.FirstSeen < actualOldest {
			actualOldest = p.FirstSeen
		}
	}

	if store.oldestLoaded != actualOldest {
		t.Fatalf("oldestLoaded=%q must equal actual MIN(FirstSeen)=%q "+
			"(id-ordered chunk walk with anti-correlated timestamps "+
			"left oldestLoaded pointing at the newest row, which makes "+
			"QueryPackets mis-route since-windowed queries to SQL fallback "+
			"and the mobile e2e test renders 0 rows)",
			store.oldestLoaded, actualOldest)
	}
}

// TestLoadChunked_PacketsSortedByFirstSeenASC: QueryPackets and
// GetTimestamps both assume s.packets is "sorted oldest-first" (see
// store.go:2125 comment on GetTimestamps). LoadChunked walks rows
// id-ASC which only equals first_seen-ASC when ids and timestamps
// are correlated — not true after fixture freshen, not true after
// any out-of-order ingest. Assert the invariant directly.
func TestLoadChunked_PacketsSortedByFirstSeenASC(t *testing.T) {
	store := openReverseTimeStore(t, 25)
	defer store.db.conn.Close()

	if err := store.LoadChunked(10); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}
	for i := 1; i < len(store.packets); i++ {
		if store.packets[i-1].FirstSeen > store.packets[i].FirstSeen {
			t.Fatalf("s.packets must be sorted by FirstSeen ASC; "+
				"packets[%d].FirstSeen=%q > packets[%d].FirstSeen=%q",
				i-1, store.packets[i-1].FirstSeen,
				i, store.packets[i].FirstSeen)
		}
	}
}
