package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
)

// createTestDBMultiDay creates a test DB with packets spread across numDays days.
// txPerDay transmissions are inserted per day, oldest day first.
// Packets within each day are spaced 1 minute apart.
func createTestDBMultiDay(t *testing.T, numDays, txPerDay int) string {
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
			t.Fatalf("createTestDBMultiDay setup: %v", err)
		}
	}
	execOrFail(`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT, route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT)`)
	execOrFail(`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT, observer_name TEXT, direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp TEXT, raw_hex TEXT)`)
	execOrFail(`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`)
	execOrFail(`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, frequency REAL)`)
	execOrFail(`CREATE TABLE schema_version (version INTEGER)`)
	execOrFail(`INSERT INTO schema_version (version) VALUES (1)`)
	execOrFail(`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`)

	id := 1
	now := time.Now().UTC()
	for day := numDays; day >= 1; day-- {
		// Offset by +30 minutes so day boundaries don't coincide exactly with
		// hotStartupHours/retentionHours cutoffs, preventing timing-boundary flakiness.
		// E.g. for numDays=3: day3 starts at now-71.5h, day2 at now-47.5h, day1 at now-23.5h.
		base := now.Add(-time.Duration(day)*24*time.Hour + 30*time.Minute)
		for i := 0; i < txPerDay; i++ {
			ts := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
			hash := fmt.Sprintf("hash%06d", id)
			if _, err := conn.Exec("INSERT INTO transmissions VALUES (?,?,?,?,0,4,1,?)", id, "aa", hash, ts, `{}`); err != nil {
				t.Fatalf("createTestDBMultiDay insert tx: %v", err)
			}
			if _, err := conn.Exec("INSERT INTO observations VALUES (?,?,?,?,?,?,?,?,?,?,?)", id, id, "obs1", "Obs1", "RX", -10.0, -80.0, 5, `[]`, ts, ""); err != nil {
				t.Fatalf("createTestDBMultiDay insert obs: %v", err)
			}
			id++
		}
	}
	return dbPath
}

// waitForBackgroundLoad polls backgroundLoadDone until true or timeout.
func waitForBackgroundLoad(t *testing.T, store *PacketStore, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if store.backgroundLoadDone.Load() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("background load did not complete within %v", timeout)
}

func TestHotStartupConfig_Clamp(t *testing.T) {
	dbPath := createTestDB(t, 10)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	// hotStartupHours > retentionHours → must be clamped
	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  24,
		HotStartupHours: 48,
	})
	if store.hotStartupHours != 24 {
		t.Errorf("expected hotStartupHours clamped to retentionHours=24, got %f", store.hotStartupHours)
	}
}

func TestHotStartupConfig_ZeroIsDisabled(t *testing.T) {
	dbPath := createTestDB(t, 10)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  24,
		HotStartupHours: 0,
	})
	if store.hotStartupHours != 0 {
		t.Errorf("expected hotStartupHours=0, got %f", store.hotStartupHours)
	}
}

func TestHotStartup_LoadsOnlyHotWindow(t *testing.T) {
	// 50 old packets (48h ago), 10 recent (30min ago)
	dbPath := createTestDBWithAgedPackets(t, 10, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1, // load only last 1 hour
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// Only the 10 recent packets should be in memory
	if len(store.packets) != 10 {
		t.Errorf("expected 10 recent packets in hot window, got %d", len(store.packets))
	}
	// oldestLoaded should be ~1h ago
	if store.oldestLoaded == "" {
		t.Fatal("oldestLoaded must be set after Load()")
	}
	oldest, _ := time.Parse(time.RFC3339, store.oldestLoaded)
	diff := time.Since(oldest)
	if diff < 30*time.Minute || diff > 90*time.Minute {
		t.Errorf("oldestLoaded %s should be ~1h ago, got diff=%v", store.oldestLoaded, diff)
	}
	// backgroundLoadDone must not be set by Load() itself
	if store.backgroundLoadDone.Load() {
		t.Error("backgroundLoadDone must not be true after Load()")
	}
}

func TestHotStartup_DisabledWhenZero(t *testing.T) {
	// 50 old (48h ago), 10 recent (30min ago) — all within 72h retention
	dbPath := createTestDBWithAgedPackets(t, 10, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 0, // disabled → load all retentionHours as before
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// All 60 packets should be loaded (both old and recent within 72h)
	if len(store.packets) != 60 {
		t.Errorf("expected 60 packets with hotStartupHours=0, got %d", len(store.packets))
	}
}

func TestHotStartup_loadChunk_AddsOlderData(t *testing.T) {
	// 50 old packets (48h ago), 10 recent (30min ago)
	dbPath := createTestDBWithAgedPackets(t, 10, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.packets) != 10 {
		t.Fatalf("setup: expected 10 packets after hot Load, got %d", len(store.packets))
	}

	// Load the old chunk (covers the 50 old packets at ~48h ago)
	chunkEnd := time.Now().UTC().Add(-1 * time.Hour)
	chunkStart := time.Now().UTC().Add(-72 * time.Hour)
	if err := store.loadChunk(chunkStart, chunkEnd); err != nil {
		t.Fatalf("loadChunk failed: %v", err)
	}

	// Should have 10 recent + 50 old
	if len(store.packets) != 60 {
		t.Errorf("expected 60 packets after loadChunk, got %d", len(store.packets))
	}
	// Packets must remain sorted ASC by first_seen
	for i := 1; i < len(store.packets); i++ {
		if store.packets[i].FirstSeen < store.packets[i-1].FirstSeen {
			t.Fatalf("packets not in ASC order at index %d: %s < %s",
				i, store.packets[i].FirstSeen, store.packets[i-1].FirstSeen)
		}
	}
	// byHash must include the old packets
	if len(store.byHash) != 60 {
		t.Errorf("expected byHash len=60, got %d", len(store.byHash))
	}
	// byObserver must reflect all 60 observations for obs1
	if len(store.byObserver["obs1"]) != 60 {
		t.Errorf("expected byObserver[obs1] len=60, got %d", len(store.byObserver["obs1"]))
	}
}

func TestHotStartup_BackgroundFillsToRetention(t *testing.T) {
	// 3 days × 50 tx/day = 150 total
	dbPath := createTestDBMultiDay(t, 3, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 24,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// After hot Load: only ~50 packets (day 1 = last 24h)
	afterHot := len(store.packets)
	if afterHot < 1 || afterHot > 60 {
		t.Errorf("expected ~50 packets after hot Load, got %d", afterHot)
	}

	// Start background fill
	go store.loadBackgroundChunks()
	waitForBackgroundLoad(t, store, 15*time.Second)

	// After background fill: all 150 packets should be loaded
	store.mu.RLock()
	total := len(store.packets)
	store.mu.RUnlock()

	if total != 150 {
		t.Errorf("expected 150 packets after background load, got %d", total)
	}
	if !store.backgroundLoadDone.Load() {
		t.Error("backgroundLoadDone must be true after loadBackgroundChunks returns")
	}
}

func TestHotStartup_ChunkErrorRecovery(t *testing.T) {
	dbPath := createTestDBWithAgedPackets(t, 10, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// intentional: closed early to simulate chunk-load failures; no defer
	db.conn.Close()

	done := make(chan struct{})
	go func() {
		store.loadBackgroundChunks()
		close(done)
	}()

	select {
	case <-done:
		// Good — completed without hanging.
	case <-time.After(10 * time.Second):
		t.Fatal("loadBackgroundChunks hung after DB close")
	}

	// #1690: backgroundLoadFailed must be true (chunk errors AND coverage
	// fell short); backgroundLoadDone stays false because the in-memory
	// store does NOT reflect the on-disk DB. Pre-#1690 the test asserted
	// Done=true on errors — that was the very lie the issue documents.
	if !store.backgroundLoadFailed.Load() {
		t.Error("backgroundLoadFailed must be true after all chunks fail (#1690)")
	}
	if store.backgroundLoadDone.Load() {
		t.Error("backgroundLoadDone must remain false when the store does not reflect the DB (#1690)")
	}
}

func TestHotStartup_SQLFallback_TriggeredForOldDate(t *testing.T) {
	// 50 old packets (48h ago), 10 recent (30min ago)
	dbPath := createTestDBWithAgedPackets(t, 10, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	// Hot load: only last 1h → 10 recent packets in memory
	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.packets) != 10 {
		t.Fatalf("setup: expected 10 in-memory packets, got %d", len(store.packets))
	}

	// Query with Since = 49h ago (before oldestLoaded ~1h ago) → SQL fallback
	since49h := time.Now().UTC().Add(-49 * time.Hour).Format(time.RFC3339)
	result := store.QueryPackets(PacketQuery{Since: since49h, Limit: 100, Order: "ASC"})

	// SQL fallback returns all packets newer than Since: 50 old (48h ago) + 10 recent (30min ago) = 60
	if result.Total != 60 {
		t.Errorf("expected SQL fallback to return 60 packets for Since=49h ago, got %d", result.Total)
	}
}

func TestHotStartup_PerfStats(t *testing.T) {
	dbPath := createTestDBWithAgedPackets(t, 10, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	stats := store.GetPerfStoreStats()

	if v, ok := stats["hotStartupHours"]; !ok || v.(float64) != 1 {
		t.Errorf("expected hotStartupHours=1 in stats, got %v", v)
	}
	if v, ok := stats["backgroundLoadComplete"]; !ok || v.(bool) != false {
		t.Errorf("expected backgroundLoadComplete=false in stats, got %v", v)
	}
	if _, ok := stats["backgroundLoadProgress"]; !ok {
		t.Error("expected backgroundLoadProgress in stats")
	}
}

func TestHotStartup_SQLFallback_NotTriggeredForRecentDate(t *testing.T) {
	// 50 old packets (48h ago), 10 recent (30min ago)
	dbPath := createTestDBWithAgedPackets(t, 10, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	// Hot load: last 1h → 10 recent packets in memory
	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// Query with Since = 45min ago (after oldestLoaded ~1h ago) → in-memory path
	since45m := time.Now().UTC().Add(-45 * time.Minute).Format(time.RFC3339)
	result := store.QueryPackets(PacketQuery{Since: since45m, Limit: 100, Order: "ASC"})

	// In-memory path: returns only the 10 recent packets (all within last 30min)
	if result.Total != 10 {
		t.Errorf("expected 10 in-memory packets for recent Since query, got %d", result.Total)
	}
}

func TestHotStartup_SQLFallback_Until(t *testing.T) {
	// 50 old packets (48h ago), 10 recent (30min ago)
	dbPath := createTestDBWithAgedPackets(t, 10, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	// Hot load: only last 1h → 10 recent in memory, oldestLoaded ~1h ago
	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.packets) != 10 {
		t.Fatalf("setup: expected 10 in-memory packets, got %d", len(store.packets))
	}

	// Until = 2h ago (before oldestLoaded ~1h ago) → SQL fallback
	until2h := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	result := store.QueryPackets(PacketQuery{Until: until2h, Limit: 100, Order: "ASC"})

	// SQL fallback returns the 50 old packets (stored at ~48h ago, all before Until)
	if result.Total != 50 {
		t.Errorf("expected SQL fallback to return 50 old packets for Until before oldestLoaded, got %d", result.Total)
	}
}

func TestHotStartup_PerfStoreHTTP(t *testing.T) {
	dbPath := createTestDBWithAgedPackets(t, 10, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, &Config{Port: 3000}, NewHub())
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/perf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	ps, ok := body["packetStore"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing packetStore in /api/perf response")
	}
	for _, field := range []string{"hotStartupHours", "backgroundLoadComplete", "backgroundLoadProgress"} {
		if _, ok := ps[field]; !ok {
			t.Errorf("missing field %q in packetStore", field)
		}
	}
	if v, ok := ps["hotStartupHours"].(float64); !ok || v != 1 {
		t.Errorf("expected hotStartupHours=1, got %v", ps["hotStartupHours"])
	}
}

func TestHotStartup_ConcurrentQueryDuringBackgroundLoad(t *testing.T) {
	// 5 days × 200 tx/day = 1000 total — small enough to run in CI fast,
	// large enough to give pollers >=1 query during the background fill.
	dbPath := createTestDBMultiDay(t, 5, 200)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	// Hot load: only last 24h → ~200 packets in memory
	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  120,
		HotStartupHours: 24,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	preLen := len(store.packets)

	// Real invariant (Munger r2 #5): while background fill is running,
	// the result set for a fixed [since, until] window must be monotonic
	// in TIME — rows only appear, never disappear. The query window must
	// straddle the moving oldestLoaded boundary so we exercise both the
	// SQL fallback (since < oldestLoaded) and the in-memory path
	// (oldestLoaded shrinks below since as chunks merge).
	//
	// since=200h ago covers everything; as oldestLoaded retreats from
	// 24h ago to 120h ago, the answer source switches from SQL fallback
	// to in-memory; Total must never decrease across that switch.
	since := time.Now().UTC().Add(-200 * time.Hour).Format(time.RFC3339)
	q := PacketQuery{Since: since, Limit: 5000, Order: "ASC"}

	// Start background fill.
	go store.loadBackgroundChunks()

	// Pollers: each goroutine keeps querying until the loader is done,
	// asserting that within its own series Total only grows or stays equal.
	// A shrink — even by one row — is a real-invariant violation that
	// the trivial Total>=0 / postLen>=preLen tests could not catch.
	var wg sync.WaitGroup
	pollers := 8
	totalSamples := atomicSamples{}
	for i := 0; i < pollers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lastTotal := -1
			for !store.backgroundLoadDone.Load() {
				r := store.QueryPackets(q)
				if r == nil {
					continue
				}
				if lastTotal >= 0 && r.Total < lastTotal {
					t.Errorf("poller %d: result set shrank (%d → %d) — non-monotonic across moving oldestLoaded boundary",
						i, lastTotal, r.Total)
				}
				lastTotal = r.Total
				totalSamples.inc()
			}
			r := store.QueryPackets(q)
			if r != nil {
				if lastTotal >= 0 && r.Total < lastTotal {
					t.Errorf("poller %d: post-load result set shrank (%d → %d)", i, lastTotal, r.Total)
				}
				totalSamples.inc()
			}
		}(i)
	}
	wg.Wait()

	waitForBackgroundLoad(t, store, 60*time.Second)

	store.mu.RLock()
	postLen := len(store.packets)
	store.mu.RUnlock()

	if postLen < preLen {
		t.Errorf("expected packet count after background load (%d) >= pre-background (%d)", postLen, preLen)
	}
	if totalSamples.get() == 0 {
		t.Error("pollers observed zero samples — test did not actually exercise the invariant")
	}
}

type atomicSamples struct {
	n int64
}

func (a *atomicSamples) inc() { atomic.AddInt64(&a.n, 1) }
func (a *atomicSamples) get() int64 {
	return atomic.LoadInt64(&a.n)
}

// TestHotStartup_BackgroundLoadFailureSurfacesInPerf asserts that when every
// background chunk errors, the store does NOT report backgroundLoadComplete=true
// — instead it surfaces backgroundLoadFailed=true via GetPerfStoreStats so
// operators see a visible failure rather than silent data loss. Munger r2 #3.
func TestHotStartup_BackgroundLoadFailureSurfacesInPerf(t *testing.T) {
	dbPath := createTestDBMultiDay(t, 3, 50)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 24,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// Force every loadChunk call to fail by closing the read connection.
	// loadBackgroundChunks must then NOT report "complete" — it must report failed.
	if err := db.conn.Close(); err != nil {
		t.Fatal(err)
	}

	store.loadBackgroundChunks()

	perf := store.GetPerfStoreStats()
	failed, hasFailedKey := perf["backgroundLoadFailed"].(bool)

	if !hasFailedKey {
		t.Fatalf("expected backgroundLoadFailed key in /api/perf payload, got keys: %v", perf)
	}
	if !failed {
		t.Errorf("expected backgroundLoadFailed=true after every chunk errored, got false (observability lying)")
	}
}
