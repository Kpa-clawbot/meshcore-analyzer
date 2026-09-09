package main

// Tests for issue #1690 — cold-load uses wrong time axis (first_seen instead
// of effective recency). Three tests live in this file:
//
//   Test1690_ColdLoad_TimeAxis  — long-lived transmissions (first_seen 30d
//                                  ago) with recent observations must load
//                                  under a 1h hotStartupHours window.
//   Test1690_BackgroundLoadHonesty — backgroundLoadComplete must NOT flip to
//                                     true when coverage is below threshold.
//   Test1690_PerfStats_NewFields — typed perf response must expose
//                                   retentionHours, oldestLoaded,
//                                   loadCoverageRatio.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// createTestDBWithLastSeen seeds a DB with the post-fix schema (last_seen
// column on transmissions). nowSec is the unix-second reference; fixture
// rows are placed relative to it.
//
// numTx transmissions, each with first_seen = nowSec - firstSeenAgo, and
// last_seen = nowSec - lastSeenAgo. Each tx has obsPerTx observations whose
// timestamps are within the last 20 minutes.
//
// Thin wrapper over seedTestDBRows (PR #1811 adv #2 — DRY). The schema
// DDL and the block-level PREFLIGHT async-migration annotation live
// there; this helper only supplies the row-time policy used by the
// #1690 fixture (fixed first_seen, fixed last_seen).
func createTestDBWithLastSeen(t *testing.T, dbPath string, numTx, obsPerTx int, nowSec int64, firstSeenAgo, lastSeenAgo time.Duration) {
	t.Helper()
	firstSeenTime := time.Unix(nowSec, 0).UTC().Add(-firstSeenAgo).Format(time.RFC3339)
	lastSeenUnix := nowSec - int64(lastSeenAgo.Seconds())
	seedTestDBRows(t, dbPath, numTx, obsPerTx, func(i int) (string, int64) {
		return firstSeenTime, lastSeenUnix
	})
}

// Test1690_ColdLoad_TimeAxis seeds 1000 transmissions whose hash *first
// appeared* 30 days ago but whose last observation was 30 minutes ago.
// With a 1h hotStartupHours, the pre-fix code (filtering on first_seen)
// loads zero rows; the post-fix code (filtering on last_seen) must load
// all 1000.
func Test1690_ColdLoad_TimeAxis(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 1000, 1, nowSec,
		30*24*time.Hour, // first_seen = 30d ago
		30*time.Minute)  // last_seen = 30min ago

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	loaded := len(store.packets)
	if loaded < 1000 {
		t.Fatalf("Test1690_ColdLoad_TimeAxis: expected ≥1000 transmissions loaded "+
			"(all 1000 fixture rows have last_seen within 1h), got %d. "+
			"Pre-fix behavior: chunked_load.go filters t.first_seen >= now-1h "+
			"which excludes all 30d-old rows.", loaded)
	}
}

// Test1690_BackgroundLoadHonesty seeds 1000 transmissions but caps the
// store's memory budget so it can only fit a fraction. After
// loadBackgroundChunks runs, backgroundLoadDone must be FALSE and
// backgroundLoadFailed must be TRUE because actual coverage is < 90%.
func Test1690_BackgroundLoadHonesty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	// 5000 rows; chunkSize=500 + maxMemoryMB=1 (→ maxPackets ≈ 1000) so
	// the load breaks at the end of the chunk that crosses the cap and
	// totalLoaded ≪ 5000.
	createTestDBWithLastSeen(t, dbPath, 5000, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
		MaxMemoryMB:     1, // forces bounded load ≪ 5000 rows
	})
	if err := store.LoadChunked(500); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}
	store.loadBackgroundChunks()

	if store.backgroundLoadDone.Load() {
		t.Errorf("backgroundLoadDone=true with only %d/5000 packets loaded; "+
			"must be false until coverage ≥ 90%%", len(store.packets))
	}
	if !store.backgroundLoadFailed.Load() {
		t.Errorf("backgroundLoadFailed=false despite under-coverage "+
			"(%d/5000 packets loaded); must be true with a reason", len(store.packets))
	}
	// The error message must mention a percentage so operators can see
	// the actual ratio surface in the perf endpoint.
	errMsg := store.BackgroundLoadError()
	if !strings.Contains(errMsg, "%") {
		t.Errorf("backgroundLoadError=%q; expected human-readable ratio "+
			"(e.g. 'loaded X%% of Y rows')", errMsg)
	}
}

// Test1690_PerfStats_NewFields asserts the typed perf payload exposes the
// retention/coverage fields needed for prod observability.
func Test1690_PerfStats_NewFields(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 10, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})
	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	ps := store.GetPerfStoreStatsTyped()
	buf, err := json.Marshal(ps)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]interface{}
	if err := json.Unmarshal(buf, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"retentionHours", "oldestLoaded", "loadCoverageRatio"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("PerfPacketStoreStats missing %q field; payload=%s", key, string(buf))
		}
	}
}
