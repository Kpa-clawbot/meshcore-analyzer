package main

import (
	"testing"
	"time"
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
