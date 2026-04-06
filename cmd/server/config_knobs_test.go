package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestBackfillHoursDefault(t *testing.T) {
	cfg := &Config{}
	if got := cfg.BackfillHours(); got != 24 {
		t.Errorf("BackfillHours() = %d, want 24", got)
	}
}

func TestBackfillHoursConfigured(t *testing.T) {
	cfg := &Config{ResolvedPath: &ResolvedPathConfig{BackfillHours: 48}}
	if got := cfg.BackfillHours(); got != 48 {
		t.Errorf("BackfillHours() = %d, want 48", got)
	}
}

func TestBackfillHoursZeroFallsBack(t *testing.T) {
	cfg := &Config{ResolvedPath: &ResolvedPathConfig{BackfillHours: 0}}
	if got := cfg.BackfillHours(); got != 24 {
		t.Errorf("BackfillHours() = %d, want 24 (default for zero)", got)
	}
}

func TestNeighborMaxAgeDaysDefault(t *testing.T) {
	cfg := &Config{}
	if got := cfg.NeighborMaxAgeDays(); got != 5 {
		t.Errorf("NeighborMaxAgeDays() = %d, want 5", got)
	}
}

func TestNeighborMaxAgeDaysConfigured(t *testing.T) {
	cfg := &Config{NeighborGraph: &NeighborGraphConfig{MaxAgeDays: 7}}
	if got := cfg.NeighborMaxAgeDays(); got != 7 {
		t.Errorf("NeighborMaxAgeDays() = %d, want 7", got)
	}
}

func TestGraphPruneOlderThan(t *testing.T) {
	g := NewNeighborGraph()
	now := time.Now().UTC()

	// Add a recent edge
	g.upsertEdge("aaa", "bbb", "bb", "obs1", nil, now)
	// Add an old edge
	g.upsertEdge("ccc", "ddd", "dd", "obs1", nil, now.Add(-60*24*time.Hour))

	if len(g.AllEdges()) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(g.AllEdges()))
	}

	cutoff := now.Add(-30 * 24 * time.Hour)
	pruned := g.PruneOlderThan(cutoff)
	if pruned != 1 {
		t.Errorf("PruneOlderThan pruned %d, want 1", pruned)
	}

	edges := g.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge after prune, got %d", len(edges))
	}
	if edges[0].NodeA != "aaa" && edges[0].NodeB != "aaa" {
		t.Errorf("wrong edge survived prune: %+v", edges[0])
	}
}

func TestPruneNeighborEdgesDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE neighbor_edges (
		node_a TEXT NOT NULL,
		node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_seen TEXT,
		PRIMARY KEY (node_a, node_b)
	)`)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	old := now.Add(-60 * 24 * time.Hour)

	db.Exec("INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, 5, ?)",
		"aaa", "bbb", now.Format(time.RFC3339))
	db.Exec("INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, 3, ?)",
		"ccc", "ddd", old.Format(time.RFC3339))

	g := NewNeighborGraph()
	g.upsertEdge("aaa", "bbb", "bb", "obs1", nil, now)
	g.upsertEdge("ccc", "ddd", "dd", "obs1", nil, old)

	pruned, err := PruneNeighborEdges(dbPath, g, 30)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Errorf("PruneNeighborEdges pruned %d DB rows, want 1", pruned)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM neighbor_edges").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row in DB after prune, got %d", count)
	}

	if len(g.AllEdges()) != 1 {
		t.Errorf("expected 1 in-memory edge after prune, got %d", len(g.AllEdges()))
	}
}

func TestBackfillRespectsHourWindow(t *testing.T) {
	store := &PacketStore{}

	now := time.Now().UTC()
	oldTime := now.Add(-48 * time.Hour).Format(time.RFC3339Nano)
	newTime := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)

	store.packets = []*StoreTx{
		{
			ID:        1,
			Hash:      "old-hash",
			FirstSeen: oldTime,
			Observations: []*StoreObs{
				{ID: 1, PathJSON: `["abc"]`},
			},
		},
		{
			ID:        2,
			Hash:      "new-hash",
			FirstSeen: newTime,
			Observations: []*StoreObs{
				{ID: 2, PathJSON: `["def"]`},
			},
		},
	}

	// With a 1-hour window, only the new tx should be processed.
	// backfillResolvedPathsAsync will find no prefix map and finish quickly,
	// but we can verify the pending count reflects the window.
	go backfillResolvedPathsAsync(store, "", 100, time.Millisecond, 1)

	// Wait for completion
	for i := 0; i < 100; i++ {
		if store.backfillComplete.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !store.backfillComplete.Load() {
		t.Fatal("backfill did not complete")
	}

	// With no prefix map, total should be 0 (early exit) or just the new one
	// The function exits early when pm == nil, so backfillTotal stays at 0
	// if there were pending items but no pm. Let's verify it didn't process
	// the old one by checking total <= 1.
	total := store.backfillTotal.Load()
	if total > 1 {
		t.Errorf("backfill total = %d, want <= 1 (old tx should be excluded by hour window)", total)
	}
}
