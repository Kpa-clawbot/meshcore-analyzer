package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/packetpath"
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
	// Source-diversity saturation: edges contributed by this many distinct
	// observers (or more) earn full confidence weight (multiplier 1.0).
	// Fewer observers earn a proportional fraction. Issue #1229 (Option C).
	affinityObserverSaturation = 3.0
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
	NodeA      string          // full pubkey
	NodeB      string          // full pubkey, or "" if unresolved/ambiguous
	Prefix     string          // raw hop prefix that established this edge
	Count      int             // total observations
	FirstSeen  time.Time       //
	LastSeen   time.Time       //
	SNRSum     float64         // running sum for average
	SNRCount   int             // how many SNR samples
	Observers  map[string]bool // observer pubkeys that witnessed
	Ambiguous  bool            // multiple candidates or zero candidates
	Candidates []string        // candidate pubkeys when ambiguous
	Resolved   bool            // true if auto-resolved via Jaccard
	// CountsByMode tallies sightings broken down by hash-prefix mode in bytes
	// (1, 2, or 3). Firmware path-byte encoding (Packet.cpp:13-18) sets
	// hash_size = (pathByte>>6)+1 with values 1/2/3 valid and 4 reserved.
	// 1-byte prefixes collide ~8-way across a typical mesh; 3-byte are
	// effectively unambiguous. Bucket 0 is the legacy/unknown bucket used
	// for edges loaded from the persisted neighbor_edges snapshot (which
	// stores only the flat Count). Sum of values == Count by construction.
	// Issue #1638 — lets the frontend weight confidence by ambiguity rather
	// than treating every observation as equal evidence.
	CountsByMode map[int]int
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

// Confidence returns a source-diversity multiplier in (0, 1] derived from the
// number of distinct observers that have contributed to this edge. Issue #1229
// (Option C): edges corroborated by multiple independent observers should
// outrank edges seen by a single observer at the same raw score.
//
// Formula: min(1.0, max(1, |Observers|) / affinityObserverSaturation).
// With saturation=3, a single observer yields 1/3, two observers 2/3, and
// three-or-more observers saturate at 1.0 — full historical weight. Edges
// with an empty observer set (legacy persisted rows lacking the column;
// see neighbor_persist.go backward-compat) default to a count of 1 so they
// behave like single-observer edges rather than disappearing — defensive.
func (e *NeighborEdge) Confidence() float64 {
	n := float64(len(e.Observers))
	if n < 1 {
		n = 1
	}
	c := n / affinityObserverSaturation
	if c > 1.0 {
		c = 1.0
	}
	return c
}

// AvgSNR returns the average SNR, or 0 if no samples.
func (e *NeighborEdge) AvgSNR() float64 {
	if e.SNRCount == 0 {
		return 0
	}
	return e.SNRSum / float64(e.SNRCount)
}

// incCountsByMode bumps the per-hash-mode tally on the edge based on the
// observed prefix length (hex chars / 2 = bytes). Per firmware
// firmware/src/Packet.cpp:13-18 (hash_size = (pathByte>>6)+1), valid wire
// modes are 1, 2 or 3 bytes; hash_size==4 is reserved. Anything outside
// 1/2/3 falls into the legacy/unknown bucket (0) so we don't lose the
// observation entirely. Issue #1638.
func incCountsByMode(e *NeighborEdge, prefix string) {
	if e.CountsByMode == nil {
		e.CountsByMode = make(map[int]int)
	}
	bytes := len(prefix) / 2
	switch bytes {
	case 1, 2, 3:
		// known firmware hash mode
	default:
		bytes = 0
	}
	e.CountsByMode[bytes]++
}

// ─── NeighborGraph ─────────────────────────────────────────────────────────────

// NeighborGraph is a cached, in-memory first-hop neighbor affinity graph.
type NeighborGraph struct {
	mu      sync.RWMutex
	edges   map[edgeKey]*NeighborEdge
	byNode  map[string][]*NeighborEdge // pubkey → edges involving this node
	builtAt time.Time
	logFn   func(prefix, msg string) // optional structured logging callback

	// RejectedEdgesGeoFar counts edges dropped at build time because both
	// endpoints had GPS and their haversine distance exceeded the
	// configurable threshold (NeighborGraphConfig.MaxEdgeKm, default 500).
	// Accessed via sync/atomic. See issue #1228.
	RejectedEdgesGeoFar uint64

	// maxEdgeKm is the geo-sanity threshold copied from config at build
	// time. 0 means "no limit" / filter disabled.
	maxEdgeKm float64

	// nodeGeo maps lowercased pubkey → (lat, lon, hasGPS) for geo-sanity
	// checks during upsertEdge. Populated by the builder; empty for graphs
	// constructed via NewNeighborGraph directly (geo filter inert).
	nodeGeo map[string]nodeGeoInfo
}

// nodeGeoInfo is the minimal geo slice cached on the graph for upsertEdge.
type nodeGeoInfo struct {
	Lat, Lon float64
	HasGPS   bool
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

// MarkAmbiguous flips the Ambiguous flag on the edge between pubkeyA and
// pubkeyB (key direction-agnostic) to the supplied value. Returns true if
// the edge existed and was updated.
//
// This helper exists so tests don't have to mutate *NeighborEdge fields
// returned from AllEdges()/Neighbors() — those mutations work today only
// because the map stores pointers, which is a hidden coupling. Routing
// the flip through a method makes the intent explicit and lets the graph
// take its own write-lock.
func (g *NeighborGraph) MarkAmbiguous(pubkeyA, pubkeyB string, ambiguous bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := makeEdgeKey(strings.ToLower(pubkeyA), strings.ToLower(pubkeyB))
	e, ok := g.edges[key]
	if !ok {
		return false
	}
	e.Ambiguous = ambiguous
	return true
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
	return BuildFromStoreWithOptions(store, BuildOptions{MaxEdgeKm: DefaultMaxEdgeKm})
}

// BuildOptions controls optional behavior of BuildFromStoreWithOptions.
type BuildOptions struct {
	EnableLog bool    // structured disambiguation logging
	MaxEdgeKm float64 // geo-sanity threshold; 0 disables the filter
	// PathTrust is the trust threshold config for path-hash observations
	// (issue #1784). When nil, the default threshold (1 byte) is used:
	// all prefix lengths count as evidence (backward-compatible). Set
	// MinHashBytesForMapping: 2 to exclude 1-byte observations from
	// neighbor-edge building, or 3 for the strictest mode.
	PathTrust *packetpath.TrustConfig
}

// DefaultMaxEdgeKm is the conservative built-in cap for the
// geo-implausibility filter (issue #1228). 500 km is comfortably above any
// plausible terrestrial LoRa hop (including satellite-relayed cases).
const DefaultMaxEdgeKm = 500.0

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
// Kept for backward compatibility; new callers should use BuildFromStoreWithOptions.
func BuildFromStoreWithLog(store *PacketStore, enableLog bool) *NeighborGraph {
	return BuildFromStoreWithOptions(store, BuildOptions{EnableLog: enableLog, MaxEdgeKm: DefaultMaxEdgeKm})
}

// BuildFromStoreWithOptions constructs the neighbor graph with explicit options.
func BuildFromStoreWithOptions(store *PacketStore, opts BuildOptions) *NeighborGraph {
	g := NewNeighborGraph()
	g.maxEdgeKm = opts.MaxEdgeKm
	if opts.EnableLog {
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
	allNodes, pm := store.getCachedNodesAndPM()

	// Index node geo for upsertEdge geo-sanity checks (issue #1228).
	geo := make(map[string]nodeGeoInfo, len(allNodes))
	for _, n := range allNodes {
		geo[strings.ToLower(n.PublicKey)] = nodeGeoInfo{Lat: n.Lat, Lon: n.Lon, HasGPS: n.HasGPS}
	}
	g.mu.Lock()
	g.nodeGeo = geo
	g.mu.Unlock()

	// Local cache for strings.ToLower — pubkeys are immutable and repeat
	// across hundreds of thousands of observations.
	lowerCache := make(map[string]string, 256)

	// Phase 1: Extract edges from every transmission + observation.
	for _, tx := range packets {
		isAdvert := tx.PayloadType != nil && *tx.PayloadType == PayloadADVERT
		isAnonReq := tx.PayloadType != nil && *tx.PayloadType == PayloadANON_REQ
		// #1777: ANON_REQ's ephemeralPubKey is a full Ed25519 pubkey — the
		// same trust level as ADVERT's pubKey — so it can seed an
		// originator↔path[0] edge exactly like ADVERT. Other non-ADVERT
		// types (REQ/RESP/PATH/TXT) only carry a 1-byte truncated hash of
		// the originator in src/dst, which is deliberately excluded here
		// (would manufacture false edges at ~1/256 collision odds).
		hasFullOriginator := isAdvert || isAnonReq
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
				if hasFullOriginator && fromLower != "" {
					if fromLower != observerPK { // self-edge guard
						g.upsertEdge(fromLower, observerPK, "", observerPK, obs.SNR, parseTimestamp(obs.Timestamp))
					}
				}
				continue
			}

			// Edge 1: originator ↔ path[0] — ADVERT and ANON_REQ only (#1777)
			if hasFullOriginator && fromLower != "" {
				firstHop := cachedToLower(lowerCache, path[0])
				if fromLower != firstHop { // self-edge guard (shouldn't happen but spec says check)
					if packetpath.MeetsPathTrust(len(path[0])/2, opts.PathTrust) {
						candidates := pm.m[firstHop]
						g.upsertEdgeWithCandidates(fromLower, firstHop, candidates, observerPK, obs.SNR, parseTimestamp(obs.Timestamp), lowerCache)
					}
				}
			}

			// Edge 2: observer ↔ path[last] — ALL packet types
			lastHop := cachedToLower(lowerCache, path[len(path)-1])
			if observerPK != lastHop { // self-edge guard
				if packetpath.MeetsPathTrust(len(path[len(path)-1])/2, opts.PathTrust) {
					candidates := pm.m[lastHop]
					g.upsertEdgeWithCandidates(observerPK, lastHop, candidates, observerPK, obs.SNR, parseTimestamp(obs.Timestamp), lowerCache)
				}
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
// ADVERTs use "pubKey"; other packets may use "from_node" or "from".
// Uses the cached ParsedDecoded() accessor to avoid repeated json.Unmarshal.
func extractFromNode(tx *StoreTx) string {
	decoded := tx.ParsedDecoded()
	if decoded == nil {
		return ""
	}
	// ANON_REQ carries the originator's full Ed25519 pubkey as
	// "ephemeralPubKey" (#1777) — the same trust level as ADVERT's "pubKey",
	// unlike the 1-byte truncated src/dst hashes on REQ/RESP/PATH/TXT.
	// Gated on the actual payload type (rather than just checking whether
	// the JSON key happens to be present) so this stays correct even if a
	// future decoder change reuses the "ephemeralPubKey" name for a
	// different, non-originator field on some other payload type — the
	// field name alone would no longer be a safe signal, but the payload
	// type check still is.
	if tx.PayloadType != nil && *tx.PayloadType == PayloadANON_REQ {
		if v, ok := decoded["ephemeralPubKey"]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
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
	// Geo-sanity guard (issue #1228): if both endpoints have known GPS and
	// the haversine distance exceeds the configured threshold, drop the
	// edge. When either lacks GPS we have no signal and accept.
	if g.shouldRejectGeoFar(pubkeyA, pubkeyB) {
		atomic.AddUint64(&g.RejectedEdgesGeoFar, 1)
		return
	}
	key := makeEdgeKey(pubkeyA, pubkeyB)

	g.mu.Lock()
	defer g.mu.Unlock()

	e, exists := g.edges[key]
	if !exists {
		e = &NeighborEdge{
			NodeA:        key.A,
			NodeB:        key.B,
			Prefix:       prefix,
			Observers:    make(map[string]bool),
			FirstSeen:    ts,
			LastSeen:     ts,
			CountsByMode: make(map[int]int),
		}
		g.edges[key] = e
		g.byNode[key.A] = append(g.byNode[key.A], e)
		g.byNode[key.B] = append(g.byNode[key.B], e)
	}

	e.Count++
	incCountsByMode(e, prefix)
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
			NodeA:        key.A,
			NodeB:        "",
			Prefix:       prefix,
			Observers:    make(map[string]bool),
			Ambiguous:    true,
			Candidates:   filtered,
			FirstSeen:    ts,
			LastSeen:     ts,
			CountsByMode: make(map[int]int),
		}
		g.edges[key] = e
		g.byNode[knownPK] = append(g.byNode[knownPK], e)
	}

	e.Count++
	incCountsByMode(e, prefix)
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
		if resolved == nil || confidence == "no_match" || confidence == "observation_count_fallback" || confidence == "gps_preference" {
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
		if existing.CountsByMode == nil {
			existing.CountsByMode = make(map[int]int)
		}
		for m, c := range e.CountsByMode {
			existing.CountsByMode[m] += c
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

// shouldRejectGeoFar reports whether the edge (a, b) is geographically
// implausible under the configured threshold. Both endpoints must have known
// GPS to trigger a rejection; if either lacks GPS the edge is accepted
// (issue #1228 — "no signal to reject").
//
// All log output is PII-truncated to the first 8 hex chars of each pubkey.
func (g *NeighborGraph) shouldRejectGeoFar(a, b string) bool {
	if g == nil || g.maxEdgeKm <= 0 || g.nodeGeo == nil {
		return false
	}
	if strings.HasPrefix(a, "prefix:") || strings.HasPrefix(b, "prefix:") {
		return false
	}
	ga, oka := g.nodeGeo[a]
	gb, okb := g.nodeGeo[b]
	if !oka || !okb || !ga.HasGPS || !gb.HasGPS {
		return false
	}
	d := haversineKm(ga.Lat, ga.Lon, gb.Lat, gb.Lon)
	if d <= g.maxEdgeKm {
		return false
	}
	// PII-truncated INFO log (8-char prefix max).
	log.Printf("[neighbor-graph] reject geo-far edge %s↔%s distance=%.0fkm threshold=%.0fkm",
		piiTruncPubkey(a), piiTruncPubkey(b), d, g.maxEdgeKm)
	return true
}

// piiTruncPubkey returns at most the first 8 hex chars of a pubkey for log
// output. The repo is public and observer/node pubkeys are PII-adjacent.
func piiTruncPubkey(pk string) string {
	if len(pk) <= 8 {
		return pk
	}
	return pk[:8]
}
