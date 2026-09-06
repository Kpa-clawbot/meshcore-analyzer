package main

import (
	"database/sql"
	"strings"
	"sync/atomic"
)

// Context-aware hop resolver — full restore of pre-#1289 hop
// disambiguation semantics, ported into the ingestor (where the
// neighbor graph + node directory now live, per #1283).
//
// Why this exists (issues #1547 / #1560):
//   The naive `resolvePath` only resolves hops whose prefix is unique
//   in the node table. On a >2K-node mesh the dominant case is 1-byte
//   prefix collisions (multiple candidates per prefix). Without
//   adjacency disambiguation those hops always serialize as `nil`
//   and the resolved_path remains effectively empty for the largest
//   meshes — the very deployments that need it most.
//
// Algorithm (ported from cmd/server/store.go @ commit 450236d5
// `pm.resolveWithContext`, intersected with the disambiguation gating
// from PR #1144 / #1352):
//
//   For each hop:
//     1. Collect candidate pubkeys by prefix-match (existing prefixIndex).
//     2. len==0 → nil.
//     3. len==1 → that pubkey.
//     4. len>1 → filter by NeighborGraph adjacency to the anchor:
//          - hop 0 anchor = fromPubkey (ADVERT originator) if known;
//          - hop i (i>0) anchor = previous resolved hop's pubkey;
//            if the previous hop did not resolve, the chain breaks
//            and subsequent >1-candidate hops fall to nil.
//        Surviving candidates after filter:
//          - exactly 1 → use it
//          - 0 or >1   → nil (cannot disambiguate further)
//
// This is the conservative tier-1 variant. Pre-#1289 also carried
// tier-2 (geo proximity), tier-3 (GPS preference), tier-4 (obs-count
// fallback) — those were noisy in practice and are intentionally NOT
// ported here; this PR is a regression restore, not an enhancement.

// NeighborGraph is the in-memory adjacency snapshot used by the
// context-aware resolver. Internally lowercased.
type NeighborGraph struct {
	adj map[string]map[string]struct{}
}

// NewNeighborGraph returns an empty graph.
func NewNeighborGraph() *NeighborGraph {
	return &NeighborGraph{adj: make(map[string]map[string]struct{})}
}

// AddEdge adds an undirected adjacency a↔b. Self-loops and empty
// endpoints are ignored.
func (g *NeighborGraph) AddEdge(a, b string) {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	if a == "" || b == "" || a == b {
		return
	}
	if g.adj[a] == nil {
		g.adj[a] = make(map[string]struct{})
	}
	if g.adj[b] == nil {
		g.adj[b] = make(map[string]struct{})
	}
	g.adj[a][b] = struct{}{}
	g.adj[b][a] = struct{}{}
}

// IsAdjacent reports whether a and b appear together in any neighbor edge.
func (g *NeighborGraph) IsAdjacent(a, b string) bool {
	if g == nil {
		return false
	}
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	if a == "" || b == "" {
		return false
	}
	nbrs, ok := g.adj[a]
	if !ok {
		return false
	}
	_, present := nbrs[b]
	return present
}

// neighborGraphHolder caches the graph for the InsertTransmission hot
// path. atomic.Value lets the 60s rebuild publish without a read-side
// lock.
type neighborGraphHolder struct {
	v atomic.Value // holds *NeighborGraph
}

func (h *neighborGraphHolder) load() *NeighborGraph {
	if v := h.v.Load(); v != nil {
		return v.(*NeighborGraph)
	}
	return nil
}

func (h *neighborGraphHolder) store(g *NeighborGraph) {
	h.v.Store(g)
}

// loadNeighborGraph reads neighbor_edges and returns an in-memory
// adjacency snapshot. Safe to call against a fresh DB (returns an
// empty graph).
func loadNeighborGraph(db *sql.DB) (*NeighborGraph, error) {
	rows, err := db.Query(`SELECT node_a, node_b FROM neighbor_edges`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	g := NewNeighborGraph()
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			continue
		}
		g.AddEdge(a, b)
	}
	return g, nil
}

// resolveHopWithContext resolves a single hop using NeighborGraph
// adjacency to the anchor. Returns nil when the hop cannot be
// disambiguated.
//
// exclude is a set of pubkeys to discard from the candidate pool
// (typically the prior hops already resolved on the path — a packet
// does not revisit a node).
//
// Behavior matrix:
//
//	len(candidates) | anchor       | graph | result
//	0               | —            | —     | nil
//	1               | —            | —     | candidates[0]
//	>1              | "" or no graph|—     | nil
//	>1              | non-empty    | set   | unique adjacent candidate
//	                                         (or nil if 0 or >1 survive)
func resolveHopWithContext(hop string, anchor string, graph *NeighborGraph, idx prefixIndex, exclude map[string]struct{}) *string {
	if idx == nil {
		return nil
	}
	h := strings.ToLower(hop)
	candidates := idx[h]
	switch len(candidates) {
	case 0:
		return nil
	case 1:
		pk := candidates[0]
		if _, skip := exclude[pk]; skip {
			return nil
		}
		return &pk
	}
	if graph == nil || anchor == "" {
		return nil
	}
	var match string
	survivors := 0
	for _, cand := range candidates {
		if _, skip := exclude[cand]; skip {
			continue
		}
		if graph.IsAdjacent(anchor, cand) {
			survivors++
			if survivors > 1 {
				return nil
			}
			match = cand
		}
	}
	if survivors == 1 {
		return &match
	}
	return nil
}

// resolvePathWithContext walks the hop list, anchoring hop 0 on
// fromPubkey (for ADVERTs) and each subsequent hop on the previous
// resolved hop. Previously-resolved pubkeys (plus the originator) are
// excluded from later candidate pools so the walk doesn't revisit a
// node. Returns a `[]*string` shape compatible with
// marshalResolvedPath (and the all-nil clobber-guard from PR #1548).
func resolvePathWithContext(hops []string, fromPubkey string, graph *NeighborGraph, idx prefixIndex) []*string {
	if len(hops) == 0 {
		return nil
	}
	out := make([]*string, len(hops))
	if idx == nil {
		return out
	}
	prevAnchor := strings.ToLower(fromPubkey)
	seen := make(map[string]struct{}, len(hops)+1)
	if prevAnchor != "" {
		seen[prevAnchor] = struct{}{}
	}
	for i, hop := range hops {
		r := resolveHopWithContext(hop, prevAnchor, graph, idx, seen)
		out[i] = r
		if r != nil {
			lc := strings.ToLower(*r)
			seen[lc] = struct{}{}
			prevAnchor = lc
		} else {
			prevAnchor = ""
		}
	}
	return out
}

// RefreshNeighborGraph loads the latest neighbor_edges snapshot and
// publishes it atomically. Called on startup and once per neighbor-
// edges builder tick (60s) alongside RefreshPrefixIndex.
func (s *Store) RefreshNeighborGraph() error {
	g, err := loadNeighborGraph(s.db)
	if err != nil {
		return err
	}
	s.neighborGraph.store(g)
	return nil
}
