// Package main: read-only neighbor-edges loader.
//
// Per issue #1287 (followup to #1283), cmd/server is the read path: it
// LOADS the in-memory neighbor graph from the SQLite snapshot the
// ingestor maintains, but never writes to it. The previous write-side
// helpers in this file (buildAndPersistEdges, asyncPersistResolvedPaths
// AndEdges, ensure*Column, softDeleteBlacklistedObservers,
// PruneNeighborEdges, openRW) all moved to cmd/ingestor; cmd/ingestor
// owns CREATE/ALTER/INSERT/UPDATE/DELETE on neighbor_edges and the
// observations/resolved_path column.
//
// Server now refreshes its in-memory copy of the graph via the
// recompNeighborGraph slot in analytics_recomputer.go: every 60s it
// re-reads neighbor_edges and atomic-swaps the resulting NeighborGraph
// into s.graph.
package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// ─── neighbor_edges loader (read-only) ─────────────────────────────────────────

// loadNeighborEdgesFromDB loads all edges from the neighbor_edges table
// and builds an in-memory NeighborGraph. Called on server startup and
// from the recompNeighborGraph background recomputer (#1287).
func loadNeighborEdgesFromDB(conn *sql.DB) *NeighborGraph {
	g := NewNeighborGraph()

	rows, err := conn.Query("SELECT node_a, node_b, count, last_seen FROM neighbor_edges")
	if err != nil {
		log.Printf("[neighbor] failed to load neighbor_edges: %v", err)
		return g
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var a, b string
		var cnt int
		var lastSeen sql.NullString
		if err := rows.Scan(&a, &b, &cnt, &lastSeen); err != nil {
			continue
		}
		ts := time.Time{}
		if lastSeen.Valid {
			ts = parseTimestamp(lastSeen.String)
		}
		key := makeEdgeKey(a, b)
		g.mu.Lock()
		e, exists := g.edges[key]
		if !exists {
			// Persisted snapshot stores only the flat Count — no per-mode
			// breakdown. Synthesize CountsByMode by attributing all Count
			// to the legacy/unknown bucket (0) so the invariant
			// sum(CountsByMode) == Count holds for downstream consumers.
			// Issue #1638 adv-#1: legacy-edge invariant.
			cbm := make(map[int]int)
			if cnt > 0 {
				cbm[0] = cnt
			}
			e = &NeighborEdge{
				NodeA:        key.A,
				NodeB:        key.B,
				Observers:    make(map[string]bool),
				FirstSeen:    ts,
				LastSeen:     ts,
				Count:        cnt,
				CountsByMode: cbm,
			}
			g.edges[key] = e
			g.byNode[key.A] = append(g.byNode[key.A], e)
			g.byNode[key.B] = append(g.byNode[key.B], e)
		} else {
			e.Count += cnt
			if e.CountsByMode == nil {
				e.CountsByMode = make(map[int]int)
			}
			if cnt > 0 {
				e.CountsByMode[0] += cnt
			}
			if ts.After(e.LastSeen) {
				e.LastSeen = ts
			}
		}
		g.mu.Unlock()
		count++
	}

	if count > 0 {
		g.mu.Lock()
		g.builtAt = time.Now()
		g.mu.Unlock()
		log.Printf("[neighbor] loaded %d edges from neighbor_edges table", count)
	}

	return g
}

// neighborEdgesTableExists returns true when neighbor_edges contains at
// least one row. Used by main.go to decide between "load snapshot" and
// "start with empty graph and wait for the ingestor to populate it".
func neighborEdgesTableExists(conn *sql.DB) bool {
	var cnt int
	err := conn.QueryRow("SELECT COUNT(*) FROM neighbor_edges").Scan(&cnt)
	if err != nil {
		return false
	}
	return cnt > 0
}

// ─── resolved_path helpers (read-only / in-memory only) ────────────────────────

// resolvePathForObs resolves hop prefixes to full pubkeys for an
// observation. Pure compute — does NOT persist (the ingestor owns
// writes to observations.resolved_path).
func resolvePathForObs(pathJSON, observerID string, tx *StoreTx, pm *prefixMap, graph *NeighborGraph) []*string {
	hops := parsePathJSON(pathJSON)
	if len(hops) == 0 {
		return nil
	}
	contextPKs := make([]string, 0, 3)
	if observerID != "" {
		contextPKs = append(contextPKs, strings.ToLower(observerID))
	}
	fromNode := extractFromNode(tx)
	if fromNode != "" {
		contextPKs = append(contextPKs, strings.ToLower(fromNode))
	}
	resolved := make([]*string, len(hops))
	// Reuse a single ctx buffer across hops instead of allocating per hop.
	// Capacity +2 for the previous-hop resolved PK that may be appended.
	ctx := make([]string, len(contextPKs), len(contextPKs)+2)
	copy(ctx, contextPKs)
	ctxLen := len(ctx)
	for i, hop := range hops {
		// Reset to base context (trim any previously appended resolved PK)
		ctx = ctx[:ctxLen]
		if i > 0 && resolved[i-1] != nil {
			ctx = append(ctx, *resolved[i-1])
		}
		node, _, _ := pm.resolveWithContext(hop, ctx, graph)
		if node != nil {
			pk := strings.ToLower(node.PublicKey)
			resolved[i] = &pk
		}
	}
	return resolved
}

// resolvePathForObsColdLoad is the cold-load (Load / loadChunk / scanAndMergeChunk)
// variant of resolvePathForObs that gates hop resolution on `unique_prefix`
// only. Live ingest uses the affinity/observation-count tiebreak via
// resolvePathForObs because it has roughly-current state. Cold load runs
// against observations up to retentionHours (168h) old, where today's
// affinity winner ≠ historical affinity winner for that prefix — silently
// mis-attributing the relay (PR #1643 R1 munger #1, "time-travel attribution
// gate").
//
// Behavior: hops whose prefix maps to exactly one repeater resolve as
// usual; hops whose prefix maps to multiple candidates return nil and
// increment skipped (caller-owned counter for observability — a single
// summary log line at the end of Load surfaces the total).
//
// Under-attribute > mis-attribute (reviewer consensus on PR #1643).
func resolvePathForObsColdLoad(pathJSON, observerID string, tx *StoreTx, pm *prefixMap, skipped *int) []*string {
	hops := parsePathJSON(pathJSON)
	if len(hops) == 0 {
		return nil
	}
	resolved := make([]*string, len(hops))
	for i, hop := range hops {
		// unique_prefix iff the prefix maps to exactly one candidate
		// after the observer-known nonRelay filter. Mirrors the
		// `len(candidates) == 1 → "unique_prefix"` arm of
		// resolveWithContext (store.go ~6380). Calling resolveWithContext
		// with a nil graph and empty context skips the affinity/
		// observation-count tiers entirely — but tier-4
		// observation_count_fallback would still pick a winner for
		// ambiguous prefixes, which is exactly what we must NOT do.
		// Hence the explicit candidate-count check here.
		h := strings.ToLower(hop)
		candidates := pm.m[h]
		if len(pm.nonRelay) > 0 && len(candidates) > 0 {
			filtered := candidates[:0:0]
			for j := range candidates {
				if _, isListener := pm.nonRelay[strings.ToLower(candidates[j].PublicKey)]; isListener {
					continue
				}
				filtered = append(filtered, candidates[j])
			}
			candidates = filtered
		}
		if len(candidates) == 1 {
			pk := strings.ToLower(candidates[0].PublicKey)
			resolved[i] = &pk
			continue
		}
		// Ambiguous (len > 1) or no_match (len == 0). Under-attribute.
		if len(candidates) > 1 && skipped != nil {
			*skipped++
		}
		// resolved[i] stays nil; extractResolvedPubkeys filters it out.
	}
	return resolved
}

// marshalResolvedPath converts []*string to JSON for in-memory caching.
func marshalResolvedPath(rp []*string) string {
	if len(rp) == 0 {
		return ""
	}
	b, err := json.Marshal(rp)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalResolvedPath parses a resolved_path JSON string.
func unmarshalResolvedPath(s string) []*string {
	if s == "" {
		return nil
	}
	var result []*string
	if json.Unmarshal([]byte(s), &result) != nil {
		return nil
	}
	return result
}
