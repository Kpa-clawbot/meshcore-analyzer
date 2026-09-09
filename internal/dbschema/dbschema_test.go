package dbschema

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// minimalDB bootstraps a SQLite DB with just enough tables for the
// ensure_* helpers to run against, but WITHOUT any of the optional
// columns that dbschema.Apply is responsible for ensuring.
func minimalDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "schema.db")
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		// Bare-bones tables, mirroring the legacy/empty fixture shape
		// pre-migration. Intentionally omit columns we expect Apply to add.
		`CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT
		)`,
		`CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL,
			observer_idx INTEGER,
			direction TEXT,
			snr REAL,
			rssi REAL,
			score INTEGER,
			path_json TEXT,
			timestamp INTEGER NOT NULL
		)`,
		`CREATE TABLE observers (
			id TEXT PRIMARY KEY,
			name TEXT
		)`,
		`CREATE TABLE nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT
		)`,
		`CREATE TABLE inactive_nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			last_seen TEXT
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
	}
	return db
}

// TestApplyAddsOptionalColumns_CanonicalSource is the regression gate
// for issue #1321: dbschema.Apply must be the single source of truth
// for ALL optional columns the server PRAGMA-detects. Previously
// scope_name/default_scope/observations.raw_hex lived ONLY in
// cmd/ingestor/db.go applySchema, so the server (which runs
// detectSchema AFTER dbschema.AssertReady) could race the writer and
// cache stale false values when ingestor hadn't yet finished its
// applySchema migrations.
func TestApplyAddsOptionalColumns_CanonicalSource(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()

	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	cases := []struct {
		table, col string
	}{
		{"transmissions", "scope_name"},
		{"nodes", "default_scope"},
		{"inactive_nodes", "default_scope"},
		{"observations", "raw_hex"},
	}
	for _, c := range cases {
		has, err := TableHasColumn(db, c.table, c.col)
		if err != nil {
			t.Fatalf("probe %s.%s: %v", c.table, c.col, err)
		}
		if !has {
			t.Errorf("after Apply: %s.%s missing — dbschema must be the source of truth for this optional column (#1321)", c.table, c.col)
		}
	}
}

// TestAssertReady_RequiresOptionalColumns enforces that AssertReady
// REFUSES a DB missing the optional columns the server depends on —
// proving dbschema.AssertReady (not server-side PRAGMA detection) is
// the gate.
func TestAssertReady_RequiresOptionalColumns(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()
	// Run pre-existing ensures so we only fail on the new ones.
	noop := Logger(func(string, ...interface{}) {})
	if err := ensureNeighborEdgesTable(db); err != nil {
		t.Fatal(err)
	}
	if err := ensureResolvedPathColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureObserverInactiveColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureLastPacketAtColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureObserverIATAColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureForeignAdvertColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureFromPubkeyColumn(db, noop); err != nil {
		t.Fatal(err)
	}

	// At this point the OLD AssertReady set is satisfied but the NEW
	// columns are NOT — AssertReady must still fail.
	err := AssertReady(db)
	if err == nil {
		t.Fatal("AssertReady should fail when scope_name/default_scope/observations.raw_hex are missing (#1321)")
	}
	for _, must := range []string{"scope_name", "default_scope", "raw_hex"} {
		if !contains(err.Error(), must) {
			t.Errorf("AssertReady error should mention missing %q; got: %v", must, err)
		}
	}

	// After full Apply, AssertReady passes.
	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := AssertReady(db); err != nil {
		t.Fatalf("AssertReady after full Apply: %v", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestPartialIdxTxLastSeenZero_BackfillMaxUsesPartialIndex pins the planner
// choice for issue #1740: the backfill MAX lookup
//
//	SELECT MAX(id) FROM transmissions WHERE last_seen = 0
//
// (and the chunked-backfill scan that walks WHERE last_seen=0 in id order)
// MUST use the partial index `idx_tx_last_seen_zero` introduced in #1740.
// The pre-fix full `idx_tx_last_seen` covers ALL rows (including the long
// tail where last_seen != 0 after backfill converges), so it grows with
// every transmission ever ingested. The partial index degenerates to the
// "unprocessed" hot subset and stays out of the page cache in steady state.
//
// Failure mode this test guards against:
//   - regressing to a full-table SCAN (no index)
//   - keeping the legacy full `idx_tx_last_seen` and letting the planner
//     reach for it instead of the partial
func TestPartialIdxTxLastSeenZero_BackfillMaxUsesPartialIndex(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()

	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Insert a handful of rows so the planner has something to weigh —
	// SQLite's planner short-circuits empty tables to "SCAN CONSTANT ROW"
	// which would mask a missing/wrong index.
	for i := 0; i < 16; i++ {
		_, err := db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, last_seen)
			VALUES (?, ?, '2026-01-01T00:00:00Z', ?)`,
			"00", "h"+string(rune('a'+i)), int64(i%2)) // half last_seen=0, half last_seen=1
		if err != nil {
			t.Fatalf("seed insert: %v", err)
		}
	}
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	// The backfill-MAX lookup — the exact shape the chunked backfill uses
	// to find the next batch of unprocessed ids.
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT MAX(id) FROM transmissions WHERE last_seen = 0`)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan += detail + "\n"
	}
	t.Logf("backfill MAX plan:\n%s", plan)

	if !strings.Contains(plan, "idx_tx_last_seen_zero") {
		t.Fatalf("backfill MAX lookup must use partial index idx_tx_last_seen_zero (#1740); plan was:\n%s", plan)
	}
	// Defense-in-depth: the legacy full index must NOT be the one used,
	// and ideally must not even exist post-migration (the gated DROP
	// covers that — see TestPartialIdxTxLastSeenZero_FullIndexDropped).
	if strings.Contains(plan, "idx_tx_last_seen ") || strings.HasSuffix(strings.TrimSpace(plan), "idx_tx_last_seen") {
		t.Fatalf("backfill MAX lookup should NOT use legacy full idx_tx_last_seen (#1740); plan was:\n%s", plan)
	}
}

// TestPartialIdxTxLastSeenZero_FullIndexDropped asserts the gated DROP
// migration ran after the partial index was created (#1740 step b).
func TestPartialIdxTxLastSeenZero_FullIndexDropped(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()
	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Confirm the partial exists (precondition for the DROP gate).
	var partialName string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_tx_last_seen_zero'`).Scan(&partialName)
	if err != nil {
		t.Fatalf("idx_tx_last_seen_zero must exist after Apply (#1740): %v", err)
	}

	// Legacy full index must be gone after Apply (gated DROP).
	var legacyName string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_tx_last_seen'`).Scan(&legacyName)
	if err == nil {
		t.Fatalf("legacy idx_tx_last_seen must be dropped after partial index is in place (#1740); still present as %q", legacyName)
	}
}
