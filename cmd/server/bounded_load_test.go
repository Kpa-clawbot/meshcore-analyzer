package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// createTestDB creates a temporary SQLite database with N transmissions (1 obs each).
func createTestDB(t *testing.T, numTx int) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	createTestDBAt(t, dbPath, numTx)
	return dbPath
}

// loadStore creates a PacketStore from a test DB with given maxMemoryMB.
func loadStore(t *testing.T, dbPath string, maxMemMB int) *PacketStore {
	t.Helper()
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &PacketStoreConfig{MaxMemoryMB: maxMemMB}
	store := NewPacketStore(db, cfg)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestBoundedLoad_LimitedMemory(t *testing.T) {
	dbPath := createTestDB(t, 5000)
	defer os.RemoveAll(filepath.Dir(dbPath))

	// Use 1MB budget — should load far fewer than 5000 packets
	store := loadStore(t, dbPath, 1)
	defer store.db.conn.Close()

	loaded := len(store.packets)
	if loaded >= 5000 {
		t.Errorf("expected bounded load to limit packets, got %d/5000", loaded)
	}
	if loaded < 1000 {
		t.Errorf("expected at least 1000 packets (minimum), got %d", loaded)
	}
	t.Logf("Loaded %d/5000 packets with 1MB budget", loaded)
}

func TestBoundedLoad_NewestFirst(t *testing.T) {
	dbPath := createTestDB(t, 5000)
	defer os.RemoveAll(filepath.Dir(dbPath))

	store := loadStore(t, dbPath, 1)
	defer store.db.conn.Close()

	loaded := len(store.packets)
	if loaded >= 5000 {
		t.Skip("all packets loaded, can't verify newest-first")
	}

	// The newest packet in DB has first_seen based on minute 5000.
	// The loaded packets should be the newest ones.
	// Last packet in store (sorted ASC) should be the newest in DB.
	last := store.packets[loaded-1]
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newestExpected := base.Add(5000 * time.Minute).Format(time.RFC3339)
	if last.FirstSeen != newestExpected {
		t.Errorf("expected last packet to be newest (%s), got %s", newestExpected, last.FirstSeen)
	}

	// First packet should NOT be the oldest in the DB (minute 1)
	first := store.packets[0]
	oldestAll := base.Add(1 * time.Minute).Format(time.RFC3339)
	if first.FirstSeen == oldestAll {
		t.Errorf("first loaded packet should not be the absolute oldest when bounded")
	}
}

func TestBoundedLoad_OldestLoadedSet(t *testing.T) {
	dbPath := createTestDB(t, 5000)
	defer os.RemoveAll(filepath.Dir(dbPath))

	store := loadStore(t, dbPath, 1)
	defer store.db.conn.Close()

	if store.oldestLoaded == "" {
		t.Fatal("oldestLoaded should be set after bounded load")
	}
	if len(store.packets) > 0 && store.oldestLoaded != store.packets[0].FirstSeen {
		t.Errorf("oldestLoaded (%s) should match first packet (%s)", store.oldestLoaded, store.packets[0].FirstSeen)
	}
	t.Logf("oldestLoaded = %s", store.oldestLoaded)
}

func TestBoundedLoad_UnlimitedWithZero(t *testing.T) {
	dbPath := createTestDB(t, 200)
	defer os.RemoveAll(filepath.Dir(dbPath))

	store := loadStore(t, dbPath, 0)
	defer store.db.conn.Close()

	if len(store.packets) != 200 {
		t.Errorf("expected all 200 packets with maxMemoryMB=0, got %d", len(store.packets))
	}
}

func TestBoundedLoad_AscendingOrder(t *testing.T) {
	dbPath := createTestDB(t, 3000)
	defer os.RemoveAll(filepath.Dir(dbPath))

	store := loadStore(t, dbPath, 1)
	defer store.db.conn.Close()

	// Verify packets are in ascending first_seen order
	for i := 1; i < len(store.packets); i++ {
		if store.packets[i].FirstSeen < store.packets[i-1].FirstSeen {
			t.Fatalf("packets not in ascending order at index %d: %s < %s",
				i, store.packets[i].FirstSeen, store.packets[i-1].FirstSeen)
		}
	}
}

// loadStoreWithRetention creates a PacketStore with retentionHours set.
func loadStoreWithRetention(t *testing.T, dbPath string, retentionHours float64) *PacketStore {
	t.Helper()
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &PacketStoreConfig{RetentionHours: retentionHours}
	store := NewPacketStore(db, cfg)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	return store
}

// createTestDBWithAgedPackets inserts numRecent packets with timestamps within
// the last hour and numOld packets with timestamps 48 hours ago.
func createTestDBWithAgedPackets(t *testing.T, numRecent, numOld int) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	execOrFail := func(s string) {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("setup: %v\nSQL: %s", err, s)
		}
	}
	execOrFail(`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT, route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT)`)
	execOrFail(`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT, observer_name TEXT, direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp TEXT, raw_hex TEXT)`)
	execOrFail(`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`)
	execOrFail(`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, frequency REAL)`)
	execOrFail(`CREATE TABLE schema_version (version INTEGER)`)
	execOrFail(`INSERT INTO schema_version (version) VALUES (1)`)
	execOrFail(`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`)

	now := time.Now().UTC()
	id := 1
	// Single transaction for all inserts — see createTestDBAt for the rationale
	// (auto-commit per Exec fsyncs per row). numOld+numRecent
	// is small here today, but wrapping keeps the fixture robust if callers scale
	// it up, and is consistent with the other builders.
	if _, err := conn.Exec("BEGIN"); err != nil {
		t.Fatalf("test DB BEGIN: %v", err)
	}
	// Insert old packets (48 hours ago)
	for i := 0; i < numOld; i++ {
		oldT := now.Add(-48 * time.Hour).Add(time.Duration(i) * time.Second)
		ts := oldT.Format(time.RFC3339)
		conn.Exec("INSERT INTO transmissions VALUES (?,?,?,?,0,4,1,?)", id, "aa", fmt.Sprintf("old%d", i), ts, `{}`)
		// observations.timestamp is INTEGER (unix seconds) in production schema
		// — keep the fixture consistent so the RFC3339 subquery matches.
		conn.Exec("INSERT INTO observations VALUES (?,?,?,?,?,?,?,?,?,?,?)", id, id, "obs1", "Obs1", "RX", -10.0, -80.0, 5, `[]`, oldT.Unix(), "")
		id++
	}
	// Insert recent packets (within last hour)
	for i := 0; i < numRecent; i++ {
		newT := now.Add(-30 * time.Minute).Add(time.Duration(i) * time.Second)
		ts := newT.Format(time.RFC3339)
		conn.Exec("INSERT INTO transmissions VALUES (?,?,?,?,0,4,1,?)", id, "bb", fmt.Sprintf("new%d", i), ts, `{}`)
		conn.Exec("INSERT INTO observations VALUES (?,?,?,?,?,?,?,?,?,?,?)", id, id, "obs1", "Obs1", "RX", -10.0, -80.0, 5, `[]`, newT.Unix(), "")
		id++
	}
	if _, err := conn.Exec("COMMIT"); err != nil {
		t.Fatalf("test DB COMMIT: %v", err)
	}
	return dbPath
}

func TestRetentionLoad_OnlyLoadsRecentPackets(t *testing.T) {
	dbPath := createTestDBWithAgedPackets(t, 50, 100)
	defer os.RemoveAll(filepath.Dir(dbPath))

	// retention = 2 hours — should load only the 50 recent packets, not the 100 old ones
	store := loadStoreWithRetention(t, dbPath, 2)
	defer store.db.conn.Close()

	if len(store.packets) != 50 {
		t.Errorf("expected 50 recent packets, got %d (old packets should be excluded by retentionHours)", len(store.packets))
	}
}

func TestRetentionLoad_ZeroRetentionLoadsAll(t *testing.T) {
	dbPath := createTestDBWithAgedPackets(t, 50, 100)
	defer os.RemoveAll(filepath.Dir(dbPath))

	// retention = 0 (unlimited) — should load all 150 packets
	store := loadStoreWithRetention(t, dbPath, 0)
	defer store.db.conn.Close()

	if len(store.packets) != 150 {
		t.Errorf("expected all 150 packets with retentionHours=0, got %d", len(store.packets))
	}
}

func TestEstimateStoreTxBytesTypical(t *testing.T) {
	est := estimateStoreTxBytesTypical(10)
	if est < 1000 {
		t.Errorf("typical estimate too low: %d", est)
	}
	// Should be roughly proportional to observation count
	est1 := estimateStoreTxBytesTypical(1)
	est20 := estimateStoreTxBytesTypical(20)
	if est20 <= est1 {
		t.Errorf("estimate should grow with observations: 1obs=%d, 20obs=%d", est1, est20)
	}
	t.Logf("Typical estimate: 1obs=%d, 10obs=%d, 20obs=%d bytes", est1, est, est20)
}

func BenchmarkLoad_Bounded(b *testing.B) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench.db")
	createTestDBAt(b, dbPath, 5000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, _ := OpenDB(dbPath)
		cfg := &PacketStoreConfig{MaxMemoryMB: 1}
		store := NewPacketStore(db, cfg)
		store.Load()
		db.conn.Close()
	}
}

func BenchmarkLoad_Unlimited(b *testing.B) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench.db")
	createTestDBAt(b, dbPath, 5000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, _ := OpenDB(dbPath)
		cfg := &PacketStoreConfig{MaxMemoryMB: 0}
		store := NewPacketStore(db, cfg)
		store.Load()
		db.conn.Close()
	}
}

// BenchmarkLoad_30K_Bounded benchmarks bounded Load() with 30K transmissions
// and realistic observation counts (1–5 per transmission).
func BenchmarkLoad_30K_Bounded(b *testing.B) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench30k.db")
	createTestDBWithObs(b, dbPath, 30000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, _ := OpenDB(dbPath)
		cfg := &PacketStoreConfig{MaxMemoryMB: 50}
		store := NewPacketStore(db, cfg)
		store.Load()
		db.conn.Close()
	}
}

// BenchmarkLoad_30K_Unlimited benchmarks unlimited Load() with 30K transmissions
// and realistic observation counts (1–5 per transmission).
func BenchmarkLoad_30K_Unlimited(b *testing.B) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench30k.db")
	createTestDBWithObs(b, dbPath, 30000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, _ := OpenDB(dbPath)
		cfg := &PacketStoreConfig{MaxMemoryMB: 0}
		store := NewPacketStore(db, cfg)
		store.Load()
		db.conn.Close()
	}
}

// createTestDBAt is like createTestDB but writes to a specific path.
func createTestDBAt(tb testing.TB, dbPath string, numTx int) {
	tb.Helper()
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		tb.Fatal(err)
	}
	defer conn.Close()

	execOrFail := func(sql string) {
		if _, err := conn.Exec(sql); err != nil {
			tb.Fatalf("test DB setup exec failed: %v\nSQL: %s", err, sql)
		}
	}
	execOrFail(`CREATE TABLE IF NOT EXISTS transmissions (
		id INTEGER PRIMARY KEY,
		raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER,
		payload_version INTEGER, decoded_json TEXT
	)`)
	execOrFail(`CREATE TABLE IF NOT EXISTS observations (
		id INTEGER PRIMARY KEY,
		transmission_id INTEGER, observer_id TEXT, observer_name TEXT,
		direction TEXT, snr REAL, rssi REAL, score INTEGER,
		path_json TEXT, timestamp TEXT, raw_hex TEXT
	)`)
	execOrFail(`CREATE TABLE IF NOT EXISTS observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`)
	execOrFail(`CREATE TABLE IF NOT EXISTS nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
		last_seen TEXT, first_seen TEXT, frequency REAL
	)`)
	execOrFail(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER)`)
	execOrFail(`INSERT INTO schema_version (version) VALUES (1)`)
	execOrFail(`CREATE INDEX IF NOT EXISTS idx_tx_first_seen ON transmissions(first_seen)`)

	txStmt, err := conn.Prepare("INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		tb.Fatalf("test DB prepare transmissions insert: %v", err)
	}
	obsStmt, err := conn.Prepare("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		tb.Fatalf("test DB prepare observations insert: %v", err)
	}
	defer txStmt.Close()
	defer obsStmt.Close()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Wrap the inserts in a single transaction. Without this, SQLite
	// (pure-Go driver) auto-commits every Exec → one fsync per row → ~2N fsyncs
	// for N transmissions (tx + obs). At numTx=5000 that is ~10k fsyncs and the
	// fixture blows past the test timeout (the #1741 hang). A single
	// BEGIN/COMMIT makes the whole build one commit, finishing in well under a
	// second regardless of numTx.
	if _, err := conn.Exec("BEGIN"); err != nil {
		tb.Fatalf("test DB BEGIN: %v", err)
	}
	for i := 1; i <= numTx; i++ {
		ts := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		hash := fmt.Sprintf("h%04d", i)
		if _, err := txStmt.Exec(i, "aabb", hash, ts, 0, 4, 1, fmt.Sprintf(`{"pubKey":"pk%04d"}`, i)); err != nil {
			tb.Fatalf("test DB insert transmission %d: %v", i, err)
		}
		if _, err := obsStmt.Exec(i, i, "obs1", "Obs1", "RX", -10.0, -80.0, 5, `["aa","bb"]`, ts); err != nil {
			tb.Fatalf("test DB insert observation %d: %v", i, err)
		}
	}
	if _, err := conn.Exec("COMMIT"); err != nil {
		tb.Fatalf("test DB COMMIT: %v", err)
	}
}

// createTestDBWithObs creates a test DB with realistic observation counts (1–5 per tx).
func createTestDBWithObs(tb testing.TB, dbPath string, numTx int) {
	tb.Helper()
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		tb.Fatal(err)
	}
	defer conn.Close()

	execOrFail := func(sqlStr string) {
		if _, err := conn.Exec(sqlStr); err != nil {
			tb.Fatalf("test DB setup exec failed: %v\nSQL: %s", err, sqlStr)
		}
	}
	execOrFail(`CREATE TABLE IF NOT EXISTS transmissions (
		id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT
	)`)
	execOrFail(`CREATE TABLE IF NOT EXISTS observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT, observer_name TEXT,
		direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp TEXT, raw_hex TEXT
	)`)
	execOrFail(`CREATE TABLE IF NOT EXISTS observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`)
	execOrFail(`CREATE TABLE IF NOT EXISTS nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
		last_seen TEXT, first_seen TEXT, frequency REAL
	)`)
	execOrFail(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER)`)
	execOrFail(`INSERT INTO schema_version (version) VALUES (1)`)
	execOrFail(`CREATE INDEX IF NOT EXISTS idx_tx_first_seen ON transmissions(first_seen)`)

	txStmt, err := conn.Prepare("INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		tb.Fatalf("test DB prepare transmissions: %v", err)
	}
	obsStmt, err := conn.Prepare("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		tb.Fatalf("test DB prepare observations: %v", err)
	}
	defer txStmt.Close()
	defer obsStmt.Close()

	observers := []string{"obs1", "obs2", "obs3", "obs4", "obs5"}
	obsNames := []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"}
	obsID := 1
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Single transaction for all inserts — see createTestDBAt for the rationale
	// (auto-commit per Exec would fsync per row; at numTx=30000
	// the benchmarks would otherwise stall for minutes). One BEGIN/COMMIT.
	if _, err := conn.Exec("BEGIN"); err != nil {
		tb.Fatalf("test DB BEGIN: %v", err)
	}
	for i := 1; i <= numTx; i++ {
		ts := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		hash := fmt.Sprintf("h%06d", i)
		if _, err := txStmt.Exec(i, "aabb", hash, ts, 0, 4, 1, fmt.Sprintf(`{"pubKey":"pk%06d"}`, i)); err != nil {
			tb.Fatalf("test DB insert transmission %d: %v", i, err)
		}
		nObs := (i % 5) + 1 // 1–5 observations per transmission
		for j := 0; j < nObs; j++ {
			snr := -5.0 + float64(j)*2.5
			rssi := -90.0 + float64(j)*5.0
			if _, err := obsStmt.Exec(obsID, i, observers[j], obsNames[j], "RX", snr, rssi, 5-j, `["aa","bb"]`, ts); err != nil {
				tb.Fatalf("test DB insert observation %d: %v", obsID, err)
			}
			obsID++
		}
	}
	if _, err := conn.Exec("COMMIT"); err != nil {
		tb.Fatalf("test DB COMMIT: %v", err)
	}
}
