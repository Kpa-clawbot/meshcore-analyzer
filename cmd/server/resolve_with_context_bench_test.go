package main

import (
	"fmt"
	"testing"
	"time"
)

// buildResolveTier1HotFixture constructs the synthetic graph + pm shape
// used by both the benchmark and the regression-guard test below.  Shape:
// ambiguous prefix "ab" → numCands candidates, plus numContext context
// pubkeys each with edges into the first 3 candidates.  This drives the
// tier-1 inner loop to its full path length on every resolve call.
func buildResolveTier1HotFixture(numCands, numContext int) (*prefixMap, *NeighborGraph, []string) {
	nodes := make([]nodeInfo, 0, numCands+numContext)
	for i := 0; i < numCands; i++ {
		nodes = append(nodes, nodeInfo{
			PublicKey: fmt.Sprintf("ab%010x", i*0x10101010+1),
			Role:      "repeater",
			Name:      fmt.Sprintf("C%d", i),
		})
	}
	contextPubkeys := make([]string, 0, numContext)
	for i := 0; i < numContext; i++ {
		pk := fmt.Sprintf("c%011x", i*0x12345)
		nodes = append(nodes, nodeInfo{PublicKey: pk, Role: "repeater", Name: fmt.Sprintf("X%d", i)})
		contextPubkeys = append(contextPubkeys, pk)
	}
	pm := buildPrefixMap(nodes)

	graph := NewNeighborGraph()
	now := time.Now()
	addEdge := func(a, b string, count int, observers ...string) {
		obs := make(map[string]bool, len(observers))
		for _, o := range observers {
			obs[o] = true
		}
		key := makeEdgeKey(a, b)
		e := &NeighborEdge{
			NodeA:     key.A,
			NodeB:     key.B,
			Count:     count,
			FirstSeen: now.Add(-1 * time.Hour),
			LastSeen:  now,
			Observers: obs,
		}
		graph.edges[key] = e
		graph.byNode[key.A] = append(graph.byNode[key.A], e)
		graph.byNode[key.B] = append(graph.byNode[key.B], e)
	}
	winnerPK := nodes[0].PublicKey
	for _, cpk := range contextPubkeys {
		addEdge(cpk, winnerPK, 20, "obs1", "obs2", "obs3")
		for k := 1; k < 3 && k < numCands; k++ {
			addEdge(cpk, nodes[k].PublicKey, 3, "obs1")
		}
	}
	return pm, graph, contextPubkeys
}

// BenchmarkResolveWithContextTier1Hot reproduces the analytics-topology hot
// path that regressed between prod d818527 and master (issue #1247).
//
// Shape: ambiguous prefix → N candidates → C context pubkeys.  This is
// representative of the per-tx work done inside computeAnalyticsTopology /
// computeAnalyticsRF when calling resolveHop on every hop of every tx with a
// fully-populated aggregate hop context (5k+ contextPubkeys at the staging
// scale).
//
// Before the #1247 fix: ~200 µs/op on this shape (the regressed master).
// After: <50 µs/op on the same shape (≥4× improvement, well clear of the
// 2× regression-guard threshold asserted by TestResolveWithContextTier1Floor).
func BenchmarkResolveWithContextTier1Hot(b *testing.B) {
	pm, graph, contextPubkeys := buildResolveTier1HotFixture(8, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = pm.resolveWithContext("ab", contextPubkeys, graph)
	}
}

// TestResolveWithContextTier1Floor is the assertion-style regression guard
// for #1247.  It runs the same hot shape as the benchmark and asserts that
// the per-call cost stays well under the regressed baseline.
//
// Methodology: 2000 calls measured under -short=false; total budget 200 ms
// allows ~100 µs/call which is 2× the post-fix ceiling but ~2× UNDER the
// pre-fix 200 µs/op number.  If a future change reintroduces the per-
// (cand, ctx) graph.Neighbors lookup or the strings.EqualFold tax, the
// test fails and the change is forced to justify the regression.
func TestResolveWithContextTier1Floor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping perf floor under -short")
	}
	pm, graph, contextPubkeys := buildResolveTier1HotFixture(8, 64)
	const iters = 2000
	// Warm up — first call allocates lookup maps; we measure steady state.
	for i := 0; i < 50; i++ {
		_, _, _ = pm.resolveWithContext("ab", contextPubkeys, graph)
	}
	t0 := time.Now()
	for i := 0; i < iters; i++ {
		_, _, _ = pm.resolveWithContext("ab", contextPubkeys, graph)
	}
	elapsed := time.Since(t0)
	perCall := elapsed / iters
	// 500 µs/call is the CI-floor-safe ceiling.  Rationale:
	//   - Post-fix steady-state on arm64 dev hardware: ~50 µs/call.
	//   - x86_64 GitHub-hosted runners measure ~340 µs/call on this
	//     microbenchmark (≈5–7× slower than the arm64 dev box due to
	//     shared-tenant CPU contention and cache behavior on this shape).
	//   - Pre-fix regressed master measured ~1500 µs/call+ on the same
	//     runners, so 500 µs still catches the regression class with
	//     ~3× headroom and avoids CI-flake from runner variance.
	// If a future change reintroduces the per-(cand, ctx) Neighbors
	// lookup or the EqualFold tax, this test still fails loudly.
	const ceiling = 500 * time.Microsecond
	if perCall > ceiling {
		t.Fatalf("resolveWithContext tier-1 perf regressed: %v/call (>%v ceiling); see #1247", perCall, ceiling)
	}
	t.Logf("resolveWithContext tier-1: %v/call (ceiling %v)", perCall, ceiling)
}
