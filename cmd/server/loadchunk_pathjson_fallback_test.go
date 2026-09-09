package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// createTestDBPathJSONNoResolvedPath builds a fixture that mirrors the LIVE
// deployment state after #1287: observations carry a path_json hop list but
// observations.resolved_path is NULL (the ingestor no longer writes it; relay
// data is persisted as aggregate neighbor_edges instead). A single repeater
// node whose public_key starts with hopPrefix lets the in-memory prefix map
// resolve that hop unambiguously to relayPubkey.
//
// The transmission's decoded_json is empty ({}), so relayPubkey is NOT an
// endpoint (pubKey/destPubKey/srcPubKey). The ONLY way it can enter
// s.byNode is via path_json → resolvePathForObs relay-hop resolution.
func createTestDBPathJSONNoResolvedPath(t *testing.T, relayPubkey, hopPrefix, firstSeen string) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	exec := func(s string, args ...interface{}) {
		if _, err := conn.Exec(s, args...); err != nil {
			t.Fatalf("setup exec failed: %v\nSQL: %s", err, s)
		}
	}

	// PREFLIGHT: async=true reason="test fixture: in-memory t.TempDir SQLite, never touches a real DB. Tables are CREATE-from-empty in a one-shot OpenDB call, not a schema migration over existing data."
	exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY,
		raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER,
		decoded_json TEXT
	)`)
	// resolved_path column present (matches live schema) but left NULL.
	// PREFLIGHT: async=true reason="test fixture, in-memory tmpdir DB"
	exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY,
		transmission_id INTEGER,
		observer_id TEXT, observer_name TEXT,
		direction TEXT, snr REAL, rssi REAL, score INTEGER,
		path_json TEXT, timestamp TEXT,
		raw_hex TEXT,
		resolved_path TEXT
	)`)
	// PREFLIGHT: async=true reason="test fixture, in-memory tmpdir DB"
	exec(`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`)
	// Production nodes schema uses public_key (not pubkey) — getAllNodes /
	// buildPrefixMap reads public_key, role, advert_count, first_seen.
	// PREFLIGHT: async=true reason="test fixture, in-memory tmpdir DB"
	exec(`CREATE TABLE nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
		last_seen TEXT, first_seen TEXT, advert_count INTEGER DEFAULT 0
	)`)
	// PREFLIGHT: async=true reason="test fixture, in-memory tmpdir DB"
	exec(`CREATE TABLE schema_version (version INTEGER)`)
	exec(`INSERT INTO schema_version (version) VALUES (1)`)
	// PREFLIGHT: async=true reason="test fixture, in-memory tmpdir DB"
	exec(`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`)

	// Repeater node so canAppearInPath() admits it to the prefix map.
	exec(`INSERT INTO nodes (public_key, name, role, advert_count) VALUES (?,?,?,?)`,
		relayPubkey, "Relay One", "repeater", 10)

	exec("INSERT INTO transmissions VALUES (?,?,?,?,0,4,1,?)",
		1, "aa", "hashpjf_1", firstSeen, `{}`)
	// resolved_path explicitly NULL; path_json carries the relay hop prefix.
	exec("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp, raw_hex, resolved_path) VALUES (?,?,?,?,?,?,?,?,?,?,?,NULL)",
		1, 1, "obs1", "Obs1", "RX", -10.0, -80.0, 5, fmt.Sprintf(`[%q]`, hopPrefix), firstSeen, "")

	return dbPath
}

// TestLoadChunked_ResolvesRelayHopsFromPathJSON_WhenResolvedPathEmpty pins the
// fix for the "relay-node analytics empty after every restart" bug.
//
// On live, observations.resolved_path is 100% NULL (since #1287 the ingestor
// persists relay data as neighbor_edges, not per-observation resolved_path).
// The cold-load paths (Load / scanAndMergeChunk) indexed relay hops ONLY from
// resolved_path, so a relay node's path-hop attribution was never rebuilt on
// startup — it only re-accumulated from live traffic, collapsing the activity
// timeline to "just the hour the server restarted".
//
// The fix: when resolved_path is empty, fall back to resolving the hops from
// the persisted path_json using the in-memory prefix map + neighbor graph
// (exactly what the live ingest path already does), then index the relay hops.
func TestLoadChunked_ResolvesRelayHopsFromPathJSON_WhenResolvedPathEmpty(t *testing.T) {
	relayPK := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	hop := "aa" // 2-hex-char path hop; unique 2-char prefix of relayPK

	ts := time.Now().UTC().Format(time.RFC3339)
	dbPath := createTestDBPathJSONNoResolvedPath(t, relayPK, hop, ts)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	if !db.hasResolvedPath {
		t.Fatalf("setup: fixture should expose resolved_path column; hasResolvedPath=false")
	}

	store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 72})
	// Empty graph is sufficient: a single prefix candidate resolves without
	// neighbor-affinity disambiguation. Mirrors a freshly restarted server
	// that has loaded its neighbor_edges snapshot before the packet load.
	store.graph.Store(NewNeighborGraph())

	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	// The relay pubkey only reachable through path_json resolution must be
	// indexed in byNode for the transmission.
	if got := len(store.byNode[relayPK]); got != 1 {
		t.Errorf("byNode[%s]: got %d transmissions, want 1 — cold load did not "+
			"resolve relay hops from path_json when resolved_path was NULL "+
			"(relay history lost on restart)", relayPK, got)
	}
}

// TestLoadChunk_ResolvesRelayHopsFromPathJSON_WhenResolvedPathEmpty covers the
// background-window loader (loadBackgroundChunks → loadChunk), which on live
// loads everything older than hotStartupHours (24h) up to retentionHours
// (168h). Without the path_json fallback here, a relay node's analytics for
// the older 6 days would still vanish on every restart even with the hot
// window fixed.
func TestLoadChunk_ResolvesRelayHopsFromPathJSON_WhenResolvedPathEmpty(t *testing.T) {
	relayPK := "ccddeeff00112233445566778899aabbccddeeff00112233445566778899aabb"
	hop := "cc"

	// Aged 48h so it falls in the background window, not the hot window.
	aged := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	dbPath := createTestDBPathJSONNoResolvedPath(t, relayPK, hop, aged)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1, // hot load must NOT pick up the 48h-old row
	})
	store.graph.Store(NewNeighborGraph())

	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}
	if got := len(store.byNode[relayPK]); got != 0 {
		t.Fatalf("setup: hot load unexpectedly picked up 48h-old row; "+
			"byNode[relayPK]=%d (want 0) — test would not exercise loadChunk", got)
	}

	chunkStart := time.Now().UTC().Add(-72 * time.Hour)
	chunkEnd := time.Now().UTC().Add(-1 * time.Hour)
	if err := store.loadChunk(chunkStart, chunkEnd); err != nil {
		t.Fatalf("loadChunk: %v", err)
	}

	if got := len(store.byNode[relayPK]); got != 1 {
		t.Errorf("byNode[%s]: got %d transmissions, want 1 — background loadChunk "+
			"did not resolve relay hops from path_json when resolved_path was NULL "+
			"(relay history lost on restart for the older retention window)", relayPK, got)
	}
}
