package main

import (
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// decodedJSONFixtureBytes is the size of the synthetic decoded_json payload used
// below: ~0.3 MB, chosen so the per-component byte breakdown registers a
// measurable, predictable TxDecodedJsonMB rather than rounding to zero.
const decodedJSONFixtureBytes = 300_000

// TestStoreMemoryBreakdown exercises the opt-in /api/perf?mem=1 diagnostic: the
// flood-forward (route_type 0/1) share and the per-component byte breakdown,
// over a hand-built store.
func TestStoreMemoryBreakdown(t *testing.T) {
	rt0, rt1, rt2 := 0, 1, 2
	obs := func(id, pj string) *StoreObs {
		return &StoreObs{ObserverID: id, ObserverName: "obs-" + id, PathJSON: pj}
	}
	bigJSON := strings.Repeat("x", decodedJSONFixtureBytes)
	s := &PacketStore{}
	s.packets = []*StoreTx{
		{RouteType: &rt0, RawHex: "aabbccdd", DecodedJSON: bigJSON, PathJSON: "[]",
			Observations: []*StoreObs{obs("a", "[1]"), obs("b", "[2]"), obs("c", "[3]")}},
		{RouteType: &rt1, RawHex: "ee", PathJSON: "[]",
			Observations: []*StoreObs{obs("a", "[1]")}},
		{RouteType: &rt2, RawHex: "ff0011", PathJSON: "[]",
			Observations: []*StoreObs{obs("a", "[1]"), obs("b", "[2]")}},
		{RouteType: nil, RawHex: "22", PathJSON: "[]"}, // unknown route_type, no obs
	}

	b := s.GetStoreMemoryBreakdown()
	if b.TotalTx != 4 {
		t.Errorf("TotalTx: want 4, got %d", b.TotalTx)
	}
	if b.FloodTx != 2 {
		t.Errorf("FloodTx (route_type 0/1): want 2, got %d", b.FloodTx)
	}
	if b.FloodTxSharePct != 50 {
		t.Errorf("FloodTxSharePct: want 50, got %v", b.FloodTxSharePct)
	}
	if b.Observations != 6 {
		t.Errorf("Observations: want 6, got %d", b.Observations)
	}
	if b.ObsPerTx != 1.5 {
		t.Errorf("ObsPerTx: want 1.5, got %v", b.ObsPerTx)
	}
	// decodedJSONFixtureBytes (+ a few string headers) over 1 MiB is ~0.286 MB,
	// which rounds to 0.29 at 2 dp. Assert the real magnitude, not merely > 0,
	// so a units/rounding regression in the byte accounting is caught.
	wantDecodedMB := float64(decodedJSONFixtureBytes) / (1024 * 1024)
	if math.Abs(b.TxDecodedJsonMB-wantDecodedMB) > 0.02 {
		t.Errorf("TxDecodedJsonMB: want ≈%.2f, got %v", wantDecodedMB, b.TxDecodedJsonMB)
	}
	if b.TotalTxEstimatedMB <= 0 {
		t.Errorf("TotalTxEstimatedMB should be > 0, got %v", b.TotalTxEstimatedMB)
	}
	// The route_type 0 flood tx carries the ~0.3 MB decoded_json, so the flood
	// share of estimated bytes must register above the 2-dp MB rounding floor.
	if b.FloodTxEstimatedMB == 0 {
		t.Errorf("expected FloodTxEstimatedMB > 0, got %v", b.FloodTxEstimatedMB)
	}
	if b.FloodTxEstimatedMB > b.TotalTxEstimatedMB {
		t.Error("flood estimated bytes cannot exceed total")
	}
	if b.ObsStringsMB < 0 || b.ObsPathJsonMB < 0 || b.TxRawHexMB < 0 {
		t.Error("byte breakdown components must be >= 0")
	}
}

// TestStoreMemoryBreakdown_Empty: an empty store yields all-zero, no divide.
func TestStoreMemoryBreakdown_Empty(t *testing.T) {
	s := &PacketStore{}
	b := s.GetStoreMemoryBreakdown()
	if b.TotalTx != 0 || b.FloodTx != 0 || b.FloodTxSharePct != 0 || b.Observations != 0 || b.ObsPerTx != 0 {
		t.Errorf("empty store: want all zero, got %+v", b)
	}
}

// TestObsRawHexNotRetainedOnLoad locks in the behavior change: even when the
// DB carries a non-empty observations.raw_hex, the production load path
// (LoadChunked) must NOT retain it on the in-memory StoreObs (it duplicates the
// parent tx.RawHex), and the read/enrich path must still serve raw_hex by
// falling back to the parent transmission. If this regresses, obs raw_hex would
// be silently lost — so this is the safety gate for dropping obs.RawHex.
func TestObsRawHexNotRetainedOnLoad(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "obsrawhex.db")

	const txHex = "deadbeefcafe"
	const obsHex = "c0ffee0102" // distinct from txHex: proves we DON'T keep it

	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT,
			route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT
		)`,
		`CREATE TABLE observations (
			id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT,
			observer_name TEXT, direction TEXT, snr REAL, rssi REAL, score INTEGER,
			path_json TEXT, timestamp TEXT, raw_hex TEXT
		)`,
		`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`,
		`CREATE TABLE nodes (
			public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
			last_seen TEXT, first_seen TEXT, frequency REAL
		)`,
		`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`,
	}
	for _, st := range stmts {
		if _, err := conn.Exec(st); err != nil {
			t.Fatalf("schema exec: %v\nSQL: %s", err, st)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := conn.Exec(
		`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		1, txHex, "hashobs", now.Add(-time.Minute).Format(time.RFC3339), 1, 5, 0, `{"type":"CHAN"}`); err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	// Observation carries its OWN non-empty raw_hex in the DB.
	if _, err := conn.Exec(
		`INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp, raw_hex) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "obs1", "Obs1", "rx", 5.0, -95.0, 0, `["AA"]`, now.Add(-time.Minute).Unix(), obsHex); err != nil {
		t.Fatalf("insert obs: %v", err)
	}
	conn.Close()

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	if !db.hasObsRawHex {
		t.Fatal("fixture precondition: observations.raw_hex must be detected (hasObsRawHex)")
	}
	store := NewPacketStore(db, &PacketStoreConfig{})
	defer store.db.conn.Close()

	if err := store.LoadChunked(5); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	tx := store.byHash["hashobs"]
	if tx == nil {
		t.Fatal("transmission not loaded")
	}
	if len(tx.Observations) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(tx.Observations))
	}
	obs := tx.Observations[0]

	// The dup must NOT be retained, even though the DB column held obsHex.
	if obs.RawHex != "" {
		t.Errorf("obs.RawHex should be empty after load (dropped as redundant dup), got %q", obs.RawHex)
	}

	// The read path must still serve raw_hex via the parent-tx fallback.
	store.mu.RLock()
	m := store.enrichObs(obs)
	store.mu.RUnlock()
	rh, _ := m["raw_hex"].(string)
	if rh != txHex {
		t.Errorf("enrichObs raw_hex: want parent tx fallback %q, got %q", txHex, rh)
	}
}
