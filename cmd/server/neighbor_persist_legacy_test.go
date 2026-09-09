package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestNeighborPersist_LegacyEdgeInvariant (#1638 adv-#1): edges loaded from
// the persisted neighbor_edges snapshot have no per-hash-mode breakdown
// (the table stores only the flat Count). Loader MUST synthesize
// CountsByMode so the invariant sum(CountsByMode) == Count holds — all
// pre-existing observations land in bucket 0 (legacy/unknown, conservative
// weight in the JS confidence indicator).
func TestNeighborPersist_LegacyEdgeInvariant(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "neighbor_legacy.db")
	rw, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`CREATE TABLE neighbor_edges (
		node_a TEXT NOT NULL,
		node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_seen TEXT,
		PRIMARY KEY (node_a, node_b)
	)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := rw.Exec(
		`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, ?, ?)`,
		"aaaa", "bbbb", 7, now,
	); err != nil {
		t.Fatal(err)
	}

	g := loadNeighborEdgesFromDB(rw)
	edges := g.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.Count != 7 {
		t.Fatalf("expected Count=7, got %d", e.Count)
	}
	if e.CountsByMode == nil {
		t.Fatalf("expected CountsByMode synthesized for legacy edge, got nil")
	}
	// All flat-count observations must land in bucket 0 (legacy/unknown).
	if got := e.CountsByMode[0]; got != 7 {
		t.Errorf("CountsByMode[0] = %d, want 7 (all legacy count in bucket 0)", got)
	}
	// Buckets 1/2/3 must be empty — no real wire-mode evidence on a
	// snapshot-only edge.
	for _, m := range []int{1, 2, 3} {
		if got := e.CountsByMode[m]; got != 0 {
			t.Errorf("CountsByMode[%d] = %d, want 0", m, got)
		}
	}
	// Invariant: sum(CountsByMode) == Count.
	sum := 0
	for _, c := range e.CountsByMode {
		sum += c
	}
	if sum != e.Count {
		t.Errorf("invariant violated: sum(CountsByMode)=%d, Count=%d", sum, e.Count)
	}
}

// TestNeighborPersist_LegacyEdgeMergeOnReload covers the "row appears twice
// in the snapshot" path (loader's else-branch): subsequent counts must
// accumulate into bucket 0 too, preserving the invariant.
func TestNeighborPersist_LegacyEdgeMergeOnReload(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "neighbor_legacy_merge.db")
	rw, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	// No PRIMARY KEY so we can insert two rows for the same (a,b) pair to
	// exercise the loader's else-branch.
	if _, err := rw.Exec(`CREATE TABLE neighbor_edges (
		node_a TEXT NOT NULL,
		node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_seen TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, cnt := range []int{3, 4} {
		if _, err := rw.Exec(
			`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, ?, ?)`,
			"aaaa", "bbbb", cnt, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	g := loadNeighborEdgesFromDB(rw)
	edges := g.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 merged edge, got %d", len(edges))
	}
	e := edges[0]
	if e.Count != 7 {
		t.Fatalf("expected merged Count=7, got %d", e.Count)
	}
	if got := e.CountsByMode[0]; got != 7 {
		t.Errorf("CountsByMode[0] = %d, want 7 after merge", got)
	}
	sum := 0
	for _, c := range e.CountsByMode {
		sum += c
	}
	if sum != e.Count {
		t.Errorf("invariant violated after merge: sum(CountsByMode)=%d, Count=%d", sum, e.Count)
	}
}
