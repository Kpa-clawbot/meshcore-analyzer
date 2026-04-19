package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"
)

// ─── Constants ─────────────────────────────────────────────────────────────────

const (
	// After this many observations, count contributes max weight to the score.
	affinitySaturationCount = 100
	// Time-decay half-life: 7 days.
	affinityHalfLifeHours = 168.0
	// Cache TTL for the built graph.
	neighborGraphTTL = 5 * time.Minute
	// Auto-resolve confidence: best must be >= this factor × second-best.
	affinityConfidenceRatio = 3.0
	// Minimum observation count to auto-resolve.
	affinityMinObservations = 3
)

// affinityLambda = ln(2) / half-life-hours, precomputed.
var affinityLambda = math.Ln2 / affinityHalfLifeHours

// ─── Data model ────────────────────────────────────────────────────────────────

// edgeKey is the canonical key for an undirected edge (A < B lexicographically).
// For ambiguous edges where NodeB is unknown, B is the raw prefix prefixed with "prefix:".
type edgeKey struct {
	A, B string
}

func makeEdgeKey(a, b string) edgeKey {
	if a > b {
		a, b = b, a
	}
	return edgeKey{A: a, B: b}
}

// NeighborEdge represents a weighted, undirected first-hop neighbor relationship.
type NeighborEdge struct {
	NodeA      string            // full pubkey
	NodeB      string            // full pubkey, or "" if unresolved/ambiguous
	Prefix     string            // raw hop prefix that established this edge
	Count      int               // total observations
	FirstSeen  time.Time         //
	LastSeen   time.Time         //
	SNRSum     float64           // running sum for average
	SNRCount   int               // how many SNR samples
	Observers  map[string]bool   // observer pubkeys that witnessed
	Ambiguous  bool              // multiple candidates or zero candidates
	Candidates []string          // candidate pubkeys when ambiguous
	Resolved   bool              // true if auto-resolved via Jaccard
}

// Score computes the affinity score at query time with time decay.
func (e *NeighborEdge) Score(now time.Time) float64 {
	countFactor := math.Min(1.0, float64(e.Count)/float64(affinitySaturationCount))
	hoursSince := now.Sub(e.LastSeen).Hours()
	if hoursSince < 0 {
		hoursSince = 0
	}
	decay := math.Exp(-affinityLambda * hoursSince)
	return countFactor * decay
}

// AvgSNR returns the average SNR, or 0 if no samples.
func (e *NeighborEdge) AvgSNR() float64 {
	if e.SNRCount == 0 {
		return 0
	}
	return e.SNRSum / float64(e.SNRCount)
}

// ─── NeighborGraph ─────────────────────────────────────────────────────────────

// NeighborGraph is a cached, in-memory first-hop neighbor affinity graph.
type NeighborGraph struct {
	mu      sync.RWMutex
	edges   map[edgeKey]*NeighborEdge
	byNode  map[string][]*NeighborEdge // pubkey → edges involving this node
	builtAt time.Time
	logFn   func(prefix, msg string) // optional structured logging callback
}

// NewNeighborGraph creates an empty graph.
func NewNeighborGraph() *NeighborGraph {
	return &NeighborGraph{
		edges:  make(map[edgeKey]*NeighborEdge),
		byNode: make(map[string][]*NeighborEdge),
	}
}

// Neighbors returns all edges for a given node pubkey.
func (g *NeighborGraph) Neighbors(pubkey string) []*NeighborEdge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.byNode[strings.ToLower(pubkey)]
}

// AllEdges returns all edges in the graph.
func (g *NeighborGraph) AllEdges() []*NeighborEdge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*NeighborEdge, 0, len(g.edges))
	for _, e := range g.edges {
		out = append(out, e)
	}
	return out
}

// IsStale returns true if the graph cache has expired.
func (g *NeighborGraph) IsStale() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.builtAt.IsZero() || time.Since(g.builtAt) > neighborGraphTTL
}

// ─── Builder ───────────────────────────────────────────────────────────────────

// BuildFromStore constructs the neighbor graph from all packets in the store.
// The store's read-lock must NOT be held by the caller.
func BuildFromStore(store *PacketStore) *NeighborGraph {
	return BuildFromStoreWithLog(store, false)
}

// cachedToLower returns strings.ToLower(s), caching results to avoid
// repeated allocations for the same pubkey string.
func cachedToLower(cache map[string]string, s string) string {
	if v, ok := cache[s]; ok {
		return v
	}
	v := strings.ToLower(s)
	cache[s] = v
	return v
}

// BuildFromStoreWithLog constructs the neighbor graph, optionally logging disambiguation decisions.
func BuildFromStoreWithLog(store *PacketStore, enableLog bool) *NeighborGraph {
	g := NewNeighborGraph()
	if enableLog {
		g.logFn = func(prefix, msg string) {
			log.Printf("[affinity] resolve %s: %s", prefix, msg)
		}
	}

	store.mu.RLock()
	// Snapshot what we need under lock.
	packets := make([]*StoreTx, len(store.packets))
	copy(packets, store.packets)
	store.mu.RUnlock()

	// Build prefix map for candidate resolution.
	// Use cached nodes+PM (avoids DB call if cache is fresh).
	_, pm := store.getCachedNodesAndPM()

	// Local cache for strings.ToLower — pubkeys are immutable and repeat
	// across hundreds of thousands of observations.
	lowerCache := make(map[string]string, 256)

	// Phase 1: Extract edges from every transmission + observation.
	for _, tx := range packets {
		isAdvert := tx.PayloadType != nil && *tx.PayloadType == PayloadADVERT
		fromNode := extractFromNode(tx)
		// Pre-compute lowered originator once per tx (not per observation).
		fromLower := ""
		if fromNode != "" {
			fromLower = cachedToLower(lowerCache, fromNode)
		}

		for _, obs := range tx.Observations {
			path := parsePathJSON(obs.PathJSON)
			observerPK := cachedToLower(lowerCache, obs.ObserverID)

			if len(path) == 0 {
				// Zero-hop
				if isAdvert && fromLower != "" {
					if fromLower != observerPK { // self-edge guard
						g.upsertEdge(fromLower, observerPK, "", observerPK, obs.SNR, parseTimestamp(obs.Timestamp))
					}
				}
				continue
			}

			// Edge 1: originator ↔ path[0] — ADVERTs only
			if isAdvert && fromLower != "" {
				firstHop := cachedToLower(lowerCache, path[0])
				if fromLower != firstHop { // self-edge guard (shouldn't happen but spec says check)
					candidates := pm.m[firstHop]
					g.upsertEdgeWithCandidates(fromLower, firstHop, candidates, observerPK, obs.SNR, parseTimestamp(obs.Timestamp), lowerCache)
				}
			}

			// Edge 2: observer ↔ path[last] — ALL packet types
			lastHop := cachedToLower(lowerCache, path[len(path)-1])
			if observerPK != lastHop { // self-edge guard
				candidates := pm.m[lastHop]
				g.upsertEdgeWithCandidates(observerPK, lastHop, candidates, observerPK, obs.SNR, parseTimestamp(obs.Timestamp), lowerCache)
			}
		}
	}

	// Phase 1.5: Resolve ambiguous edges using full graph context.
	resolveAmbiguousEdges(pm, g)

	// Phase 2: Disambiguation via Jaccard similarity.
	g.disambiguate()

	g.mu.Lock()
	g.builtAt = time.Now()
	g.mu.Unlock()

	return g
}

// extractFromNode pulls the originator pubkey from a StoreTx's DecodedJSON.
// ADVERTs use "pubKey", other packets may use "from_node" or "from".
// Uses the cached ParsedDecoded() accessor to avoid repeated json.Unmarshal.
func extractFromNode(tx *StoreTx) string {
	decoded := tx.ParsedDecoded()
	if decoded == nil {
		return ""
	}
	// ADVERTs store the originator pubkey as "pubKey"; other packets may use
	// "from_node" or "from".  Check all three so we never miss the originator.
	for _, field := range []string{"pubKey", "from_node", "from"} {
		if v, ok := decoded[field]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// jsonUnmarshalFast is a thin wrapper; could be optimized later.
func jsonUnmarshalFast(data string, v interface{}) error {
	return json.Unmarshal([]byte(data), v)
}

// upsertEdge adds/updates an edge between two fully-known pubkeys.
func (g *NeighborGraph) upsertEdge(pubkeyA, pubkeyB, prefix, observer string, snr *float64, ts time.Time) {
	key := makeEdgeKey(pubkeyA, pubkeyB)

	g.mu.Lock()
	defer g.mu.Unlock()

	e, exists := g.edges[key]
	if !exists {
		e = &NeighborEdge{
			NodeA:     key.A,
			NodeB:     key.B,
			Prefix:    prefix,
			Observers: make(map[string]bool),
			FirstSeen: ts,
			LastSeen:  ts,
		}
		g.edges[key] = e
		g.byNode[key.A] = append(g.byNode[key.A], e)
		g.byNode[key.B] = append(g.byNode[key.B], e)
	}

	e.Count++
	if ts.After(e.LastSeen) {
		e.LastSeen = ts
	}
	if ts.Before(e.FirstSeen) {
		e.FirstSeen = ts
	}
	if snr != nil {
		e.SNRSum += *snr
		e.SNRCount++
	}
	if observer != "" {
		e.Observers[observer] = true
	}
}

// upsertEdgeWithCandidates handles prefix-based edges that may be ambiguous.
func (g *NeighborGraph) upsertEdgeWithCandidates(knownPK, prefix string, candidates []nodeInfo, observer string, snr *float64, ts time.Time, lc map[string]string) {
	if len(candidates) == 1 {
		resolved := cachedToLower(lc, candidates[0].PublicKey)
		if resolved == knownPK {
			return // self-edge guard
		}
		g.upsertEdge(knownPK, resolved, prefix, observer, snr, ts)
		return
	}

	// Filter out self from candidates
	filtered := make([]string, 0, len(candidates))
	for _, c := range candidates {
		pk := cachedToLower(lc, c.PublicKey)
		if pk != knownPK {
			filtered = append(filtered, pk)
		}
	}

	if len(filtered) == 1 {
		g.upsertEdge(knownPK, filtered[0], prefix, observer, snr, ts)
		return
	}

	// Ambiguous or orphan: use prefix-based key
	pseudoB := "prefix:" + prefix
	key := makeEdgeKey(knownPK, pseudoB)

	g.mu.Lock()
	defer g.mu.Unlock()

	e, exists := g.edges[key]
	if !exists {
		e = &NeighborEdge{
			NodeA:      key.A,
			NodeB:      "",
			Prefix:     prefix,
			Observers:  make(map[string]bool),
			Ambiguous:  true,
			Candidates: filtered,
			FirstSeen:  ts,
			LastSeen:   ts,
		}
		g.edges[key] = e
		g.byNode[knownPK] = append(g.byNode[knownPK], e)
	}

	e.Count++
	if ts.After(e.LastSeen) {
		e.LastSeen = ts
	}
	if ts.Before(e.FirstSeen) {
		e.FirstSeen = ts
	}
	if snr != nil {
		e.SNRSum += *snr
		e.SNRCount++
	}
	if observer != "" {
		e.Observers[observer] = true
	}
}

// ─── Phase 1.5: Context-based resolution of ambiguous edges ────────────────────

// resolveAmbiguousEdges attempts to resolve ambiguous prefix edges using the
// fully-built graph context. Called after Phase 1 (edge collection) completes
// so that affinity and geo proximity tiers have full neighbor data.
func resolveAmbiguousEdges(pm *prefixMap, graph *NeighborGraph) {
	// Step 1: Collect ambiguous edges under read lock.
	graph.mu.RLock()
	type ambiguousEntry struct {
		key       edgeKey
		edge      *NeighborEdge
		knownNode string
		prefix    string
	}
	var ambiguous []ambiguousEntry
	for key, e := range graph.edges {
		if !e.Ambiguous {
			continue
		}
		knownNode := e.NodeA
		if strings.HasPrefix(e.NodeA, "prefix:") {
			knownNode = e.NodeB
		}
		if knownNode == "" {
			continue
		}
		ambiguous = append(ambiguous, ambiguousEntry{key, e, knownNode, e.Prefix})
	}
	graph.mu.RUnlock()

	// Step 2: Resolve each (no lock needed — resolveWithContext takes its own RLock).
	type resolution struct {
		ambiguousEntry
		resolvedPK string
	}
	var resolutions []resolution
	for _, ae := range ambiguous {
		resolved, confidence, _ := pm.resolveWithContext(ae.prefix, []string{ae.knownNode}, graph)
		if resolved == nil || confidence == "no_match" || confidence == "first_match" || confidence == "gps_preference" {
			continue
		}
		rpk := strings.ToLower(resolved.PublicKey)
		if rpk == ae.knownNode {
			continue // self-edge guard
		}
		resolutions = append(resolutions, resolution{ae, rpk})
	}

	// Step 3: Apply resolutions under write lock.
	if len(resolutions) == 0 {
		return
	}
	graph.mu.Lock()
	for _, r := range resolutions {
		// Verify edge still exists and is still ambiguous (could have been
		// resolved by a prior iteration if two ambiguous edges resolve to same target).
		e, ok := graph.edges[r.key]
		if !ok || !e.Ambiguous {
			continue
		}
		graph.resolveEdge(r.key, e, r.knownNode, r.resolvedPK)
	}
	graph.mu.Unlock()
}

// ─── Disambiguation ────────────────────────────────────────────────────────────

// disambiguate resolves ambiguous edges using Jaccard similarity of neighbor sets.
// Only fully-resolved edges are used as evidence (transitivity poisoning guard).
func (g *NeighborGraph) disambiguate() {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Build resolved neighbor sets: for each node, collect the set of nodes
	// it has fully-resolved (non-ambiguous) edges with.
	resolvedNeighbors := make(map[string]map[string]bool)
	for _, e := range g.edges {
		if e.Ambiguous || e.NodeB == "" {
			continue
		}
		if resolvedNeighbors[e.NodeA] == nil {
			resolvedNeighbors[e.NodeA] = make(map[string]bool)
		}
		if resolvedNeighbors[e.NodeB] == nil {
			resolvedNeighbors[e.NodeB] = make(map[string]bool)
		}
		resolvedNeighbors[e.NodeA][e.NodeB] = true
		resolvedNeighbors[e.NodeB][e.NodeA] = true
	}

	// Try to resolve each ambiguous edge.
	for key, e := range g.edges {
		if !e.Ambiguous || len(e.Candidates) < 2 {
			continue
		}
		if e.Count < affinityMinObservations {
			continue
		}

		// Determine the known node (the one that's a real pubkey, not the prefix side).
		knownNode := e.NodeA
		if strings.HasPrefix(e.NodeA, "prefix:") {
			knownNode = e.NodeB
		}
		// If knownNode is empty (shouldn't happen for ambiguous edges with candidates), skip.
		if knownNode == "" {
			continue
		}

		knownNeighbors := resolvedNeighbors[knownNode]

		type scored struct {
			pubkey  string
			jaccard float64
		}
		var scores []scored

		for _, cand := range e.Candidates {
			candNeighbors := resolvedNeighbors[cand]
			j := jaccardSimilarity(knownNeighbors, candNeighbors)
			scores = append(scores, scored{cand, j})
		}

		if len(scores) < 2 {
			continue
		}

		// Find best and second-best.
		best, secondBest := scores[0], scores[1]
		if secondBest.jaccard > best.jaccard {
			best, secondBest = secondBest, best
		}
		for i := 2; i < len(scores); i++ {
			if scores[i].jaccard > best.jaccard {
				secondBest = best
				best = scores[i]
			} else if scores[i].jaccard > secondBest.jaccard {
				secondBest = scores[i]
			}
		}

		// Auto-resolve only if best >= 3× second-best AND enough observations.
		if secondBest.jaccard == 0 {
			// If second-best is 0 and best > 0, ratio is infinite → resolve.
			if best.jaccard > 0 {
				if g.logFn != nil {
					g.logFn(e.Prefix, fmt.Sprintf("%s score=%d Jaccard=%.2f vs %s score=%d Jaccard=%.2f → neighbor_affinity (ratio ∞)",
						best.pubkey[:minLen(best.pubkey, 8)], e.Count, best.jaccard,
						secondBest.pubkey[:minLen(secondBest.pubkey, 8)], e.Count, secondBest.jaccard))
				}
				g.resolveEdge(key, e, knownNode, best.pubkey)
			}
		} else if best.jaccard/secondBest.jaccard >= affinityConfidenceRatio {
			ratio := best.jaccard / secondBest.jaccard
			if g.logFn != nil {
				g.logFn(e.Prefix, fmt.Sprintf("%s score=%d Jaccard=%.2f vs %s score=%d Jaccard=%.2f → neighbor_affinity (ratio %.1f×)",
					best.pubkey[:minLen(best.pubkey, 8)], e.Count, best.jaccard,
					secondBest.pubkey[:minLen(secondBest.pubkey, 8)], e.Count, secondBest.jaccard, ratio))
			}
			g.resolveEdge(key, e, knownNode, best.pubkey)
		} else {
			// Ambiguous
			if g.logFn != nil {
				ratio := 0.0
				if secondBest.jaccard > 0 {
					ratio = best.jaccard / secondBest.jaccard
				}
				g.logFn(e.Prefix, fmt.Sprintf("scores too close (Jaccard %.2f vs %.2f, ratio %.1f×) → ambiguous, returning %d candidates",
					best.jaccard, secondBest.jaccard, ratio, len(e.Candidates)))
			}
		}
	}
}

// resolveEdge converts an ambiguous edge to a resolved one.
// Must be called with g.mu held.
func (g *NeighborGraph) resolveEdge(oldKey edgeKey, e *NeighborEdge, knownNode, resolvedPK string) {
	// Remove old edge.
	delete(g.edges, oldKey)
	g.removeFromByNode(oldKey.A, e)
	g.removeFromByNode(oldKey.B, e)

	// Update edge.
	newKey := makeEdgeKey(knownNode, resolvedPK)
	e.NodeA = newKey.A
	e.NodeB = newKey.B
	e.Ambiguous = false
	e.Resolved = true

	// Merge with existing edge if any.
	if existing, ok := g.edges[newKey]; ok {
		existing.Count += e.Count
		if e.LastSeen.After(existing.LastSeen) {
			existing.LastSeen = e.LastSeen
		}
		if e.FirstSeen.Before(existing.FirstSeen) {
			existing.FirstSeen = e.FirstSeen
		}
		existing.SNRSum += e.SNRSum
		existing.SNRCount += e.SNRCount
		for obs := range e.Observers {
			existing.Observers[obs] = true
		}
		return
	}

	g.edges[newKey] = e
	g.byNode[newKey.A] = append(g.byNode[newKey.A], e)
	g.byNode[newKey.B] = append(g.byNode[newKey.B], e)
}

// removeFromByNode removes an edge from the byNode index for the given key.
func (g *NeighborGraph) removeFromByNode(nodeKey string, edge *NeighborEdge) {
	edges := g.byNode[nodeKey]
	for i, e := range edges {
		if e == edge {
			g.byNode[nodeKey] = append(edges[:i], edges[i+1:]...)
			return
		}
	}
}

// jaccardSimilarity computes |A ∩ B| / |A ∪ B|.
func jaccardSimilarity(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	for k := range a {
		if b[k] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// parseTimestamp parses a timestamp string into time.Time.
func parseTimestamp(s string) time.Time {
	// Try common formats.
	for _, fmt := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.000Z",
	} {
		if t, err := time.Parse(fmt, s); err == nil {
			return t
		}
	}
	return time.Time{}
}


// minLen returns the smaller of n and len(s).
func minLen(s string, n int) int {
	if len(s) < n {
		return len(s)
	}
	return n
}

// PruneOlderThan removes all edges with LastSeen before cutoff.
// Returns the number of edges removed.
func (g *NeighborGraph) PruneOlderThan(cutoff time.Time) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	pruned := 0
	for key, edge := range g.edges {
		if edge.LastSeen.Before(cutoff) {
			// Remove from byNode index
			g.removeFromByNode(edge.NodeA, edge)
			if edge.NodeB != "" {
				g.removeFromByNode(edge.NodeB, edge)
			}
			delete(g.edges, key)
			pruned++
		}
	}
	return pruned
}
