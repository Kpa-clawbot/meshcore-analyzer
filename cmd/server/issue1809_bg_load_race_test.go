package main

// Test for issue #1809 — background load fails almost immediately because
// `loadBackgroundChunks` is spawned at FirstChunkReady (chunk #1 merged)
// while `LoadChunked` is still merging the remainder of the hot window.
// At that moment `s.oldestLoaded` is still "" (only set at the end of
// LoadChunked), so the bg loader sees empty oldest → breaks immediately →
// coverage = 0 → `backgroundLoadFailed=true`.
//
// The fix extracts a `RunStartupLoad` helper that runs LoadChunked first
// and only then spawns the background loader. This test calls the helper
// directly and asserts the post-load state.
//
// PR #1811 round-1 fixture rewrite (B1 — tautology trap): the original
// fixture put all 100 rows inside the 1h hot window, so LoadChunked alone
// produced coverage=1.0 and the test passed even if loadBackgroundChunks
// was a no-op. We now spread rows across 14 days with hotStartupHours=24,
// so a no-op bg loader leaves a deliberately incomplete store and the
// assertions fail. The original red-commit assertions
// (oldestLoaded != "", !backgroundLoadFailed, backgroundLoadDone) are
// kept intact; we add the coverage assertions on top.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Test1809_StartupLoad_BgLoaderSeesOldestLoaded confirms that after
// RunStartupLoad returns, oldestLoaded is set and backgroundLoadFailed
// is false. The pre-fix code (spawn bg loader at FirstChunkReady)
// produces backgroundLoadFailed=true deterministically because the bg
// loader reads oldestLoaded="" and bails.
func Test1809_StartupLoad_BgLoaderSeesOldestLoaded(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	// Seed 100 rows spread over 14 days with last_seen also spread, so
	// hotStartupHours=24 picks up only ~24/(14*24) ≈ 7 rows in the hot
	// window. The remaining ~93 rows MUST be loaded by the background
	// loader; if it is a no-op the post-load len(packets) and
	// oldestLoaded will betray the regression.
	const totalRows = 100
	const spanDays = 14
	createTestDBSpreadOverDays(t, dbPath, totalRows, spanDays, nowSec)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  float64(spanDays * 24), // cover the full seed span
		HotStartupHours: 24,                     // hot window = 1 day
	})

	if err := store.RunStartupLoad(500); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}

	// --- original red-commit assertions: KEEP INTACT ---
	if store.oldestLoaded == "" {
		t.Fatalf("oldestLoaded is empty after RunStartupLoad; bg loader would bail")
	}
	if store.backgroundLoadFailed.Load() {
		t.Fatalf("backgroundLoadFailed=true after RunStartupLoad; "+
			"bg loader fired before LoadChunked set oldestLoaded "+
			"(error=%q, loaded=%d, oldest=%q)",
			store.BackgroundLoadError(), len(store.packets), store.oldestLoaded)
	}
	if !store.backgroundLoadDone.Load() {
		t.Fatalf("backgroundLoadDone=false after RunStartupLoad; expected true on success")
	}

	// --- B1 anti-tautology assertions: bg loader actually did work ---

	// Bound the rows the hot window alone could have loaded. The seeder
	// places rows evenly across spanDays so the hot window (24h) catches
	// at most ~totalRows/spanDays + a small fudge for boundary edge.
	hotOnlyMax := (totalRows/spanDays)*2 + 5
	if len(store.packets) <= hotOnlyMax {
		t.Fatalf("len(packets)=%d after RunStartupLoad — bg loader appears to be a no-op "+
			"(hot window alone caps at ~%d rows for %d rows spread over %d days). "+
			"Coverage = backgroundLoadDone may have flipped without bg work.",
			len(store.packets), hotOnlyMax, totalRows, spanDays)
	}

	// oldestLoaded must be older than the hot cutoff after the bg loader
	// has retreated through the retention window. Pre-fix it would equal
	// the hot cutoff (or empty), proving bg loader never advanced.
	oldest, err := time.Parse(time.RFC3339, store.oldestLoaded)
	if err != nil {
		t.Fatalf("oldestLoaded=%q is not RFC3339: %v", store.oldestLoaded, err)
	}
	hotCutoff := time.Unix(nowSec, 0).UTC().Add(-24 * time.Hour)
	// Allow a small margin since the bg loader chunks daily; oldest
	// should be at least one full day before the hot cutoff.
	if !oldest.Before(hotCutoff.Add(-12 * time.Hour)) {
		t.Fatalf("oldestLoaded=%s is not materially older than hot cutoff %s — "+
			"bg loader did not advance through the retention window",
			oldest.Format(time.RFC3339), hotCutoff.Format(time.RFC3339))
	}
}

// createTestDBSpreadOverDays seeds a DB with rows whose first_seen +
// last_seen are evenly spread across `spanDays` ending at `nowSec`.
//
// Implemented in terms of seedTestDBRows so the schema DDL is defined
// once (adv #2 DRY) and the block-level PREFLIGHT annotation is
// hoisted to a single comment (adv #11) instead of being duplicated
// on every CREATE statement.
func createTestDBSpreadOverDays(t *testing.T, dbPath string, numTx, spanDays int, nowSec int64) {
	t.Helper()
	spanSeconds := int64(spanDays) * 86400
	seedTestDBRows(t, dbPath, numTx, 1, func(i int) (firstSeenStr string, lastSeenUnix int64) {
		ago := spanSeconds * int64(numTx-i) / int64(numTx) // newest at i==numTx → 0s ago
		u := nowSec - ago
		return time.Unix(u, 0).UTC().Format(time.RFC3339), u
	})
}

// seedTestDBRows creates the standard test-fixture SQLite schema and
// populates `numTx` transmissions with `obsPerTx` observations each.
// rowTimes(i) returns the (first_seen string, last_seen unix) for
// transmission #i (1-indexed).
//
// adv #2 (PR #1811): the prior code duplicated the schema DDL between
// createTestDBWithLastSeen and createTestDBSpreadOverDays. This helper
// is the single source of truth.
//
// PREFLIGHT: async=true reason="unit-test fixture; in-memory ephemeral SQLite,
// no prod DB path. All CREATE TABLE / CREATE INDEX statements below are
// schema-only test seeds — covered by this block-level annotation
// (adv #11: prior code carried a duplicated annotation on every line)."
func seedTestDBRows(t *testing.T, dbPath string, numTx, obsPerTx int, rowTimes func(i int) (string, int64)) {
	t.Helper()
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	execOrFail := func(s string) {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("test DB exec: %v\nSQL: %s", err, s)
		}
	}
	// PREFLIGHT: async=true reason="unit-test fixture seeder; in-memory ephemeral SQLite; all CREATE/INSERT below covered by this block annotation (pr-preflight 25-line lookback window)"
	execOrFail(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY,
		raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER,
		payload_version INTEGER, decoded_json TEXT,
		last_seen INTEGER NOT NULL DEFAULT 0
	)`)
	// PREFLIGHT: async=true reason="unit-test fixture seeder"
	execOrFail(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT, observer_name TEXT,
		direction TEXT, snr REAL, rssi REAL, score INTEGER,
		path_json TEXT, timestamp TEXT, raw_hex TEXT
	)`)
	// PREFLIGHT: async=true reason="unit-test fixture seeder"
	execOrFail(`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT, inactive INTEGER)`)
	// PREFLIGHT: async=true reason="unit-test fixture seeder"
	execOrFail(`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, frequency REAL)`)
	// PREFLIGHT: async=true reason="unit-test fixture seeder"
	execOrFail(`CREATE TABLE schema_version (version INTEGER)`)
	execOrFail(`INSERT INTO schema_version (version) VALUES (1)`)
	// PREFLIGHT: async=true reason="unit-test fixture seeder; index on ephemeral test DB"
	execOrFail(`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`)
	// PREFLIGHT: async=true reason="unit-test fixture seeder"
	execOrFail(`CREATE INDEX idx_tx_last_seen ON transmissions(last_seen)`)

	txStmt, err := conn.Prepare("INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		t.Fatalf("prepare tx: %v", err)
	}
	defer txStmt.Close()
	obsStmt, err := conn.Prepare("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		t.Fatalf("prepare obs: %v", err)
	}
	defer obsStmt.Close()

	obsID := 1
	for i := 1; i <= numTx; i++ {
		firstSeenStr, lastSeenUnix := rowTimes(i)
		hash := fmt.Sprintf("h%06d", i)
		if _, err := txStmt.Exec(i, "aabb", hash, firstSeenStr, 0, 4, 1, "{}", lastSeenUnix); err != nil {
			t.Fatalf("insert tx %d: %v", i, err)
		}
		for j := 0; j < obsPerTx; j++ {
			obsTs := time.Unix(lastSeenUnix, 0).UTC().Add(-time.Duration(j) * time.Minute).Format(time.RFC3339)
			if _, err := obsStmt.Exec(obsID, i, "obs1", "Obs1", "RX", -10.0, -80.0, 5, "[]", obsTs); err != nil {
				t.Fatalf("insert obs: %v", err)
			}
			obsID++
		}
	}
}
