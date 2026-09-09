package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// createTestDBAmbiguousPrefix builds a fixture where TWO repeaters share the
// same 2-char hop prefix. An observation's path_json carries ONLY the
// ambiguous prefix (no longer prefix that would disambiguate). With no
// neighbor_edges seeded, the cold-load fallback in scanAndMergeChunk has
// nothing to anchor on — yet the current code resolves the prefix anyway
// (via observation_count_fallback or candidate[0]) and over-attributes the
// hop to ONE of the two repeaters. That is the time-travel bug munger
// flagged: the historical packet's actual relay is unknown, but the loader
// picks today's tier-4 winner against ~7-day-old observations.
func createTestDBAmbiguousPrefix(t *testing.T, relayA, relayB, hop, firstSeen string) string {
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

	// PREFLIGHT: async=true reason="test fixture: in-memory t.TempDir SQLite, never touches a real DB."
	exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY,
		raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER,
		decoded_json TEXT
	)`)
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

	// Two repeaters sharing the same 2-char prefix `hop`.
	// Different advert_counts so tier-4 tiebreak deterministically picks one
	// (proving the bug: it over-attributes to the higher-count node).
	exec(`INSERT INTO nodes (public_key, name, role, advert_count) VALUES (?,?,?,?)`,
		relayA, "Relay A", "repeater", 50)
	exec(`INSERT INTO nodes (public_key, name, role, advert_count) VALUES (?,?,?,?)`,
		relayB, "Relay B", "repeater", 10)

	// Aged 48h so it lands in the background window (loadChunk path).
	exec("INSERT INTO transmissions VALUES (?,?,?,?,0,4,1,?)",
		1, "aa", "hashamb_1", firstSeen, `{}`)
	exec("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp, raw_hex, resolved_path) VALUES (?,?,?,?,?,?,?,?,?,?,?,NULL)",
		1, 1, "obs1", "Obs1", "RX", -10.0, -80.0, 5, fmt.Sprintf(`[%q]`, hop), firstSeen, "")

	return dbPath
}

// TestLoadChunk_AmbiguousPrefix_SkipsAttribution pins the fix for the
// time-travel attribution gate (munger R1 #1). When path_json carries an
// ambiguous prefix that matches multiple repeaters, the cold-load path
// MUST NOT pick a winner via affinity/observation-count tiebreak — today's
// affinity winner is not necessarily the historical hop. Safer to
// under-attribute (skip byNode for that hop) than to mis-attribute.
func TestLoadChunk_AmbiguousPrefix_SkipsAttribution(t *testing.T) {
	relayA := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	relayB := "aa1122334455667788990011223344556677889900112233445566778899aabb"
	hop := "aa" // 2-char prefix shared by both relayA and relayB

	aged := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	dbPath := createTestDBAmbiguousPrefix(t, relayA, relayB, hop, aged)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1, // hot load skips the 48h-old row → goes to loadChunk
	})
	// Empty graph: no neighbor-affinity tiebreak signal. Mirrors a freshly
	// restarted server whose only relay info is the prefix map.
	store.graph.Store(NewNeighborGraph())

	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}
	if got := len(store.byNode[relayA]) + len(store.byNode[relayB]); got != 0 {
		t.Fatalf("setup: hot load unexpectedly picked up 48h-old row "+
			"(byNode total=%d, want 0) — test would not exercise loadChunk", got)
	}

	chunkStart := time.Now().UTC().Add(-72 * time.Hour)
	chunkEnd := time.Now().UTC().Add(-1 * time.Hour)
	if err := store.loadChunk(chunkStart, chunkEnd); err != nil {
		t.Fatalf("loadChunk: %v", err)
	}

	// Neither repeater may be over-attributed. The hop is ambiguous → the
	// cold-load loader MUST NOT pick one as the byNode owner.
	if got := len(store.byNode[relayA]); got != 0 {
		t.Errorf("byNode[%s]: got %d transmissions, want 0 — ambiguous-prefix hop "+
			"was over-attributed to relayA (time-travel attribution bug)", relayA, got)
	}
	if got := len(store.byNode[relayB]); got != 0 {
		t.Errorf("byNode[%s]: got %d transmissions, want 0 — ambiguous-prefix hop "+
			"was over-attributed to relayB (time-travel attribution bug)", relayB, got)
	}
}

// TestLoad_AmbiguousPrefix_SkipsAttribution covers the hot-window Load()
// path. Same setup as the loadChunk test but the row falls inside the hot
// window so it is loaded by Load() / scanAndMergeChunk.
func TestLoad_AmbiguousPrefix_SkipsAttribution(t *testing.T) {
	relayA := "bbccddeeff00112233445566778899aabbccddeeff00112233445566778899aa"
	relayB := "bb112233445566778899001122334455667788990011223344556677889900aa"
	hop := "bb"

	ts := time.Now().UTC().Format(time.RFC3339)
	dbPath := createTestDBAmbiguousPrefix(t, relayA, relayB, hop, ts)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 72})
	store.graph.Store(NewNeighborGraph())

	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	if got := len(store.byNode[relayA]); got != 0 {
		t.Errorf("byNode[%s]: got %d transmissions, want 0 — ambiguous-prefix hop "+
			"was over-attributed (hot Load path)", relayA, got)
	}
	if got := len(store.byNode[relayB]); got != 0 {
		t.Errorf("byNode[%s]: got %d transmissions, want 0 — ambiguous-prefix hop "+
			"was over-attributed (hot Load path)", relayB, got)
	}
}
