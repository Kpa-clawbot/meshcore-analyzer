package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestLoad_PanicsWhenGraphNotLoadedAndEdgesExist pins the startup-ordering
// invariant (munger R1 #2). Graph-load-before-packet-load is the entire
// premise of PR #1643's fix: without an in-memory neighbor graph, the
// path_json relay-hop fallback cannot resolve hops, so relay-node analytics
// history collapses. main.go currently does the right thing — but nothing
// asserts the ordering, so a future refactor could silently regress.
//
// Load() must panic when neighbor_edges has rows but s.graph.Load() returns
// nil. Fast-fail at startup beats silently-wrong attribution.
func TestLoad_PanicsWhenGraphNotLoadedAndEdgesExist(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	rw, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()

	exec := func(s string, args ...interface{}) {
		if _, err := rw.Exec(s, args...); err != nil {
			t.Fatalf("setup exec failed: %v\nSQL: %s", err, s)
		}
	}

	// Minimal CoreScope schema. PREFLIGHT: async=true reason="test fixture, in-memory tmpdir DB"
	exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY,
		raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER,
		decoded_json TEXT
	)`)
	// PREFLIGHT: async=true reason="test fixture, in-memory tmpdir DB"
	exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER,
		observer_id TEXT, observer_name TEXT,
		direction TEXT, snr REAL, rssi REAL, score INTEGER,
		path_json TEXT, timestamp TEXT, raw_hex TEXT, resolved_path TEXT
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
	exec(`CREATE TABLE neighbor_edges (
		node_a TEXT NOT NULL,
		node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_seen TEXT,
		PRIMARY KEY (node_a, node_b)
	)`)
	now := time.Now().UTC().Format(time.RFC3339)
	exec(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, ?, ?)`,
		"aaa", "bbb", 5, now)

	d, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer d.conn.Close()

	// Deliberately DO NOT call store.graph.Store(...). s.graph.Load() returns
	// nil → the bug condition the invariant guard must catch.
	store := NewPacketStore(d, &PacketStoreConfig{RetentionHours: 72})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("Load() must panic when neighbor_edges has rows but graph is nil; got no panic")
		}
	}()
	_ = store.Load()
}
