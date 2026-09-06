package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestNeighborEdgesBuilderDeltaScan enforces issue #1339:
// after the initial (warm-up) full build, subsequent ticks of
// buildAndPersistNeighborEdges MUST scan only observations newer
// than the most recent edge already persisted. The watermark is
// derived from MAX(neighbor_edges.last_seen) — neighbor_edges itself
// is the persistence, no separate metadata table.
//
// RED expectations:
//  1. After warm-up that produces edges, a second build with NO new
//     observations is a fast no-op (<1s) and writes nothing.
//  2. After inserting K observations with timestamps strictly newer
//     than the prior MAX(last_seen), the next build upserts exactly
//     K edges in <1s.
//  3. Initial build (empty neighbor_edges) still does a full scan
//     (warm-up preserved).
func TestNeighborEdgesBuilderDeltaScan(t *testing.T) {
	if testing.Short() {
		t.Skip("synthetic 100k-row benchmark; skipped in -short")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "delta.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "from-node",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO observers (id, name) VALUES (?, ?)`,
		"obs-1", "observer-1",
	); err != nil {
		t.Fatal(err)
	}
	var obsRowid int64
	if err := store.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, "obs-1").Scan(&obsRowid); err != nil {
		t.Fatal(err)
	}

	// Baseline timestamps: a contiguous block ending at baselineMaxTs.
	const baseline = 100_000
	const baselineStartTs int64 = 1735689600 // 2025-01-01 UTC
	baselineMaxTs := baselineStartTs + int64(baseline) - 1

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	txStmt, err := tx.Prepare(`INSERT INTO transmissions
		(raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, from_pubkey)
		VALUES ('', ?, ?, 0, ?, 0, '{}', 'aaaaaaaaaa')`)
	if err != nil {
		t.Fatal(err)
	}
	obsStmt, err := tx.Prepare(`INSERT INTO observations
		(transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, '["bb"]', ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < baseline; i++ {
		res, err := txStmt.Exec(fmt.Sprintf("h%d", i), baselineStartTs+int64(i), payloadADVERT)
		if err != nil {
			t.Fatal(err)
		}
		txID, _ := res.LastInsertId()
		if _, err := obsStmt.Exec(txID, obsRowid, baselineStartTs+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Initial warm-up: drain to completion (StartNeighborEdgesBuilder
	// does the same — call directly so the test doesn't depend on the
	// goroutine harness). Full scan allowed because neighbor_edges
	// starts empty.
	for {
		n, err := store.buildAndPersistNeighborEdges(trustAllPrefixes())
		if err != nil {
			t.Fatalf("warm-up build: %v", err)
		}
		if n == 0 || n < 50000 {
			break
		}
	}
	var edgesAfterWarmup int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM neighbor_edges`).Scan(&edgesAfterWarmup); err != nil {
		t.Fatal(err)
	}
	if edgesAfterWarmup == 0 {
		t.Fatal("warm-up produced 0 edges; can't establish a watermark")
	}
	// Sanity: MAX(last_seen) should reflect the baseline tail timestamp.
	var maxLastSeen string
	if err := store.db.QueryRow(`SELECT MAX(last_seen) FROM neighbor_edges`).Scan(&maxLastSeen); err != nil {
		t.Fatal(err)
	}
	wantMax := time.Unix(baselineMaxTs, 0).UTC().Format(time.RFC3339)
	if maxLastSeen != wantMax {
		t.Fatalf("MAX(last_seen) after warm-up: want %s, got %s", wantMax, maxLastSeen)
	}

	// Tick #2: NO new observations. Expect no-op + fast.
	noopStart := time.Now()
	n2, err := store.buildAndPersistNeighborEdges(trustAllPrefixes())
	if err != nil {
		t.Fatalf("noop build: %v", err)
	}
	noopDur := time.Since(noopStart)
	if n2 != 0 {
		t.Fatalf("expected 0 edges on empty-delta tick; got %d (#1339)", n2)
	}
	if noopDur > time.Second {
		t.Fatalf("empty-delta build took %v; expected <1s — builder is "+
			"still doing a full table scan. (#1339)", noopDur)
	}

	// Tick #3: insert K observations with timestamps strictly newer
	// than baselineMaxTs.
	const delta = 100
	deltaStartTs := baselineMaxTs + 1
	tx2, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	txStmt2, err := tx2.Prepare(`INSERT INTO transmissions
		(raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, from_pubkey)
		VALUES ('', ?, ?, 0, ?, 0, '{}', 'aaaaaaaaaa')`)
	if err != nil {
		t.Fatal(err)
	}
	obsStmt2, err := tx2.Prepare(`INSERT INTO observations
		(transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, '["bb"]', ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < delta; i++ {
		res, err := txStmt2.Exec(fmt.Sprintf("d%d", i), deltaStartTs+int64(i), payloadADVERT)
		if err != nil {
			t.Fatal(err)
		}
		txID, _ := res.LastInsertId()
		if _, err := obsStmt2.Exec(txID, obsRowid, deltaStartTs+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}

	deltaStart := time.Now()
	n3, err := store.buildAndPersistNeighborEdges(trustAllPrefixes())
	if err != nil {
		t.Fatalf("delta build: %v", err)
	}
	deltaDur := time.Since(deltaStart)
	// Each ADVERT observation with a non-empty path produces 2 edge
	// candidates (from↔hop[0] and observer↔hop[-1]). The watermark
	// must clamp the scan to the delta rows ONLY — anything more
	// proves the WHERE clause was bypassed.
	if n3 != delta*2 {
		t.Fatalf("expected %d edges upserted (delta only, 2 per advert obs); got %d. "+
			"Builder must only scan observations with timestamp > MAX(neighbor_edges.last_seen). (#1339)",
			delta*2, n3)
	}
	if deltaDur > 500*time.Millisecond {
		t.Fatalf("delta build of %d rows took %v; expected <500ms. (#1339)", delta, deltaDur)
	}

	// Sanity: MAX(last_seen) advanced.
	var maxLastSeen2 string
	if err := store.db.QueryRow(`SELECT MAX(last_seen) FROM neighbor_edges`).Scan(&maxLastSeen2); err != nil {
		t.Fatal(err)
	}
	if maxLastSeen2 <= maxLastSeen {
		t.Fatalf("MAX(last_seen) did not advance: was %s, now %s", maxLastSeen, maxLastSeen2)
	}
}
