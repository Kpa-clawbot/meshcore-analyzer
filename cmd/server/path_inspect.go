package main

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// ─── Path Inspector ────────────────────────────────────────────────────────────
// POST /api/paths/inspect — beam-search scorer for prefix path candidates.
// Spec: issue #944 §2.1–2.5.

// pathInspectRequest is the JSON body for the inspect endpoint.
type pathInspectRequest struct {
	Prefixes []string            `json:"prefixes"`
	Context  *pathInspectContext `json:"context,omitempty"`
	Limit    int                 `json:"limit,omitempty"`
}

type pathInspectContext struct {
	ObserverID string `json:"observerId,omitempty"`
	Since      string `json:"since,omitempty"`
	Until      string `json:"until,omitempty"`
}

// pathCandidate is one scored candidate path in the response.
type pathCandidate struct {
	Path  []string `json:"path"`
	Names []string `json:"names"`
	Score float64  `json:"score"`
	// Speculative is true when the score is below speculativeThreshold or any
	// hop is below the configured path-trust threshold. Consumers needing the
	// reason should inspect evidence.perHop[].trusted.
	Speculative bool         `json:"speculative"`
	Evidence    pathEvidence `json:"evidence"`
}

type pathEvidence struct {
	PerHop []hopEvidence `json:"perHop"`
}

type hopEvidence struct {
	Prefix               string           `json:"prefix"`
	CandidatesConsidered int              `json:"candidatesConsidered"`
	Chosen               string           `json:"chosen"`
	EdgeWeight           float64          `json:"edgeWeight"`
	Trusted              bool             `json:"trusted"`
	Alternatives         []hopAlternative `json:"alternatives,omitempty"`
}

// hopAlternative shows a candidate that was considered but not chosen for this hop.
type hopAlternative struct {
	PublicKey string  `json:"publicKey"`
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
}

type pathInspectResponse struct {
	Candidates []pathCandidate        `json:"candidates"`
	Input      map[string]interface{} `json:"input"`
	Stats      map[string]interface{} `json:"stats"`
	// Stale is true when the response was served from a stale neighbor graph
	// while a background rebuild is in progress (issue #1203).
	Stale bool `json:"stale,omitempty"`
}

// beamEntry represents a partial path being extended during beam search.
type beamEntry struct {
	pubkeys  []string
	names    []string
	evidence []hopEvidence
	score    float64 // product of per-hop scores (pre-geometric-mean)
}

const (
	beamWidth            = 20
	maxInputHops         = 64
	maxPrefixBytes       = 3
	maxRequestItems      = 64
	geoMaxKm             = 50.0
	hopScoreFloor        = 0.05
	speculativeThreshold = 0.7
	inspectCacheTTL      = 30 * time.Second
	inspectBodyLimit     = 4096
)

// Weights per spec §2.3.
const (
	wEdge        = 0.35
	wGeo         = 0.20
	wRecency     = 0.15
	wSelectivity = 0.30
)

func (s *Server) handlePathInspect(w http.ResponseWriter, r *http.Request) {
	// Body limit per spec §2.1.
	r.Body = http.MaxBytesReader(w, r.Body, inspectBodyLimit)

	var req pathInspectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	// Validate prefixes.
	if len(req.Prefixes) == 0 {
		http.Error(w, `{"error":"prefixes required"}`, http.StatusBadRequest)
		return
	}
	if len(req.Prefixes) > maxRequestItems {
		http.Error(w, `{"error":"too many prefixes (max 64)"}`, http.StatusBadRequest)
		return
	}

	// Normalize + validate each prefix.
	prefixByteLen := -1
	for i, p := range req.Prefixes {
		p = strings.ToLower(strings.TrimSpace(p))
		req.Prefixes[i] = p
		if len(p) == 0 || len(p)%2 != 0 {
			http.Error(w, `{"error":"prefixes must be even-length hex"}`, http.StatusBadRequest)
			return
		}
		if _, err := hex.DecodeString(p); err != nil {
			http.Error(w, `{"error":"prefixes must be valid hex"}`, http.StatusBadRequest)
			return
		}
		byteLen := len(p) / 2
		if byteLen > maxPrefixBytes {
			http.Error(w, `{"error":"prefix exceeds 3 bytes"}`, http.StatusBadRequest)
			return
		}
		if prefixByteLen == -1 {
			prefixByteLen = byteLen
		} else if byteLen != prefixByteLen {
			http.Error(w, `{"error":"mixed prefix lengths not allowed"}`, http.StatusBadRequest)
			return
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	// Check cache.
	cacheKey := s.store.inspectCacheKey(req)
	s.store.inspectMu.RLock()
	if cached, ok := s.store.inspectCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.store.inspectMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cached.data)
		return
	}
	s.store.inspectMu.RUnlock()

	// Snapshot data under read lock.
	nodes, pm := s.store.getCachedNodesAndPM()

	// Build pubkey→nodeInfo map for O(1) geo lookup in scorer.
	nodeByPK := make(map[string]*nodeInfo, len(nodes))
	for i := range nodes {
		nodeByPK[strings.ToLower(nodes[i].PublicKey)] = &nodes[i]
	}

	// Get neighbor graph (issue #1203): stale-while-revalidate.
	//   - cold start (nil): return 503 + kick off async rebuild for next request.
	//   - stale non-nil: serve it immediately with stale:true + async rebuild.
	//   - fresh: serve normally.
	graph := s.store.graph.Load()
	stale := false
	if graph == nil {
		// Cold start — kick off rebuild so the next request lands warm,
		// then return 503 immediately. Don't spawn a fresh goroutine if a
		// rebuild is already in-flight: singleflight dedups the BUILD, not
		// the goroutine launch (PR #1208 carmack #2).
		if !s.store.rebuildInFlight() {
			go s.store.ensureNeighborGraph()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{"retry": true})
		return
	}
	if graph.IsStale() {
		stale = true
		if !s.store.rebuildInFlight() {
			go s.store.ensureNeighborGraph()
		}
	}

	now := time.Now()
	start := now

	// Beam search.
	beam := s.store.beamSearch(req.Prefixes, pm, graph, nodeByPK, now)
	pt := s.cfg.GetPathTrust()

	// Sort by score descending, take top limit.
	sortBeam(beam)
	if len(beam) > limit {
		beam = beam[:limit]
	}

	// Build response with per-hop alternatives (spec §2.7, M2 fix).
	candidates := make([]pathCandidate, 0, len(beam))
	for _, entry := range beam {
		nHops := len(entry.pubkeys)
		var score float64
		if nHops > 0 {
			score = math.Pow(entry.score, 1.0/float64(nHops))
		}

		// Populate per-hop alternatives: other candidates at each hop that weren't chosen.
		evidence := make([]hopEvidence, len(entry.evidence))
		copy(evidence, entry.evidence)
		allHopsTrusted := true
		for hi, ev := range evidence {
			if hi >= len(req.Prefixes) {
				break
			}
			prefix := req.Prefixes[hi]
			trusted := packetpath.MeetsPathTrust(len(prefix)/2, &pt)
			if !trusted {
				allHopsTrusted = false
			}
			allCands := pm.m[prefix]
			var alts []hopAlternative
			for _, c := range allCands {
				if !canAppearInPath(c.Role) || c.PublicKey == ev.Chosen {
					continue
				}
				var partialEntry beamEntry
				if hi > 0 {
					partialEntry = beamEntry{pubkeys: entry.pubkeys[:hi], names: entry.names[:hi], score: 1.0}
				}
				// Score this alternative in the context of the partial path.
				altScore := s.store.scoreHop(partialEntry, c, ev.CandidatesConsidered, graph, nodeByPK, now, hi)
				alts = append(alts, hopAlternative{PublicKey: c.PublicKey, Name: c.Name, Score: math.Round(altScore*1000) / 1000})
			}
			// Sort alternatives by score descending, cap at 5.
			sort.Slice(alts, func(i, j int) bool { return alts[i].Score > alts[j].Score })
			if len(alts) > 5 {
				alts = alts[:5]
			}
			evidence[hi] = hopEvidence{
				Prefix:               ev.Prefix,
				CandidatesConsidered: ev.CandidatesConsidered,
				Chosen:               ev.Chosen,
				EdgeWeight:           ev.EdgeWeight,
				Trusted:              trusted,
				Alternatives:         alts,
			}
		}

		candidates = append(candidates, pathCandidate{
			Path:        entry.pubkeys,
			Names:       entry.names,
			Score:       math.Round(score*1000) / 1000,
			Speculative: score < speculativeThreshold || !allHopsTrusted,
			Evidence:    pathEvidence{PerHop: evidence},
		})
	}

	elapsed := time.Since(start).Milliseconds()
	resp := pathInspectResponse{
		Candidates: candidates,
		Stale:      stale,
		Input: map[string]interface{}{
			"prefixes": req.Prefixes,
			"hops":     len(req.Prefixes),
		},
		Stats: map[string]interface{}{
			"beamWidth":              beamWidth,
			"expansionsRun":          len(req.Prefixes) * beamWidth,
			"elapsedMs":              elapsed,
			"minHashBytesForMapping": pt.MinHashBytesOrDefault(),
		},
	}

	// Cache result (and evict stale entries). Don't cache when the response
	// itself is stale — the rebuild kicked off above will land a fresh graph
	// shortly and we don't want to pin a stale answer for inspectCacheTTL
	// (issue #1203).
	if !stale {
		s.store.inspectMu.Lock()
		if s.store.inspectCache == nil {
			s.store.inspectCache = make(map[string]*inspectCachedResult)
		}
		now2 := time.Now()
		for k, v := range s.store.inspectCache {
			if now2.After(v.expiresAt) {
				delete(s.store.inspectCache, k)
			}
		}
		s.store.inspectCache[cacheKey] = &inspectCachedResult{
			data:      resp,
			expiresAt: now2.Add(inspectCacheTTL),
		}
		s.store.inspectMu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type inspectCachedResult struct {
	data      pathInspectResponse
	expiresAt time.Time
}

func (s *PacketStore) inspectCacheKey(req pathInspectRequest) string {
	key := strings.Join(req.Prefixes, ",")
	if req.Context != nil {
		key += "|" + req.Context.ObserverID + "|" + req.Context.Since + "|" + req.Context.Until
	}
	return key
}

func (s *PacketStore) beamSearch(prefixes []string, pm *prefixMap, graph *NeighborGraph, nodeByPK map[string]*nodeInfo, now time.Time) []beamEntry {
	// Start with empty beam.
	beam := []beamEntry{{pubkeys: nil, names: nil, evidence: nil, score: 1.0}}

	for hopIdx, prefix := range prefixes {
		candidates := pm.m[prefix]
		// Filter by role at lookup time (spec §2.2 step 2).
		var filtered []nodeInfo
		for _, c := range candidates {
			if canAppearInPath(c.Role) {
				filtered = append(filtered, c)
			}
		}

		candidateCount := len(filtered)
		if candidateCount == 0 {
			// No candidates for this hop — beam dies.
			return nil
		}

		var nextBeam []beamEntry
		for _, entry := range beam {
			for _, cand := range filtered {
				hopScore := s.scoreHop(entry, cand, candidateCount, graph, nodeByPK, now, hopIdx)
				if hopScore < hopScoreFloor {
					hopScore = hopScoreFloor
				}

				newEntry := beamEntry{
					pubkeys: append(append([]string{}, entry.pubkeys...), cand.PublicKey),
					names:   append(append([]string{}, entry.names...), cand.Name),
					evidence: append(append([]hopEvidence{}, entry.evidence...), hopEvidence{
						Prefix:               prefix,
						CandidatesConsidered: candidateCount,
						Chosen:               cand.PublicKey,
						EdgeWeight:           hopScore,
					}),
					score: entry.score * hopScore,
				}
				nextBeam = append(nextBeam, newEntry)
			}
		}

		// Prune to beam width.
		sortBeam(nextBeam)
		if len(nextBeam) > beamWidth {
			nextBeam = nextBeam[:beamWidth]
		}
		beam = nextBeam
	}

	return beam
}

func (s *PacketStore) scoreHop(entry beamEntry, cand nodeInfo, candidateCount int, graph *NeighborGraph, nodeByPK map[string]*nodeInfo, now time.Time, hopIdx int) float64 {
	var edgeScore float64
	var geoScore float64 = 1.0
	var recencyScore float64 = 1.0

	if hopIdx == 0 || len(entry.pubkeys) == 0 {
		// First hop: no prior node to compare against.
		edgeScore = 1.0
	} else {
		lastPK := entry.pubkeys[len(entry.pubkeys)-1]

		// Single scan over neighbors for both edge weight and recency.
		edges := graph.Neighbors(lastPK)
		var foundEdge *NeighborEdge
		for _, e := range edges {
			peer := e.NodeA
			if strings.EqualFold(peer, lastPK) {
				peer = e.NodeB
			}
			if strings.EqualFold(peer, cand.PublicKey) {
				foundEdge = e
				break
			}
		}

		if foundEdge != nil {
			edgeScore = foundEdge.Score(now)
			hoursSince := now.Sub(foundEdge.LastSeen).Hours()
			if hoursSince <= 24 {
				recencyScore = 1.0
			} else {
				recencyScore = math.Max(0.1, 24.0/hoursSince)
			}
		} else {
			edgeScore = 0
			recencyScore = 0
		}

		// Geographic plausibility.
		prevNode := nodeByPK[strings.ToLower(lastPK)]
		if prevNode != nil && prevNode.HasGPS && cand.HasGPS {
			dist := haversineKm(prevNode.Lat, prevNode.Lon, cand.Lat, cand.Lon)
			if dist > geoMaxKm {
				geoScore = math.Max(0.1, geoMaxKm/dist)
			}
		}
	}

	// Prefix selectivity.
	selectivityScore := 1.0 / float64(candidateCount)

	return wEdge*edgeScore + wGeo*geoScore + wRecency*recencyScore + wSelectivity*selectivityScore
}

func sortBeam(beam []beamEntry) {
	sort.Slice(beam, func(i, j int) bool {
		return beam[i].score > beam[j].score
	})
}

// buildGraphFn is the function used by ensureNeighborGraph to rebuild the
// neighbor graph. It's a package-level var so tests can swap it for a counter
// wrapper. Default is BuildFromStore.
var buildGraphFn = func(s *PacketStore) *NeighborGraph { return BuildFromStore(s) }

// Singleflight state for ensureNeighborGraph lives on *PacketStore
// (see store.go: rebuildMu, rebuildInFlt). It moved off package globals in
// PR #1208 round-1 so that parallel tests with independent stores don't
// share rebuild state (cross-store deadlock/skip under -race).

// ensureNeighborGraph triggers a graph rebuild if nil or stale. Concurrent
// callers share a single in-flight build (singleflight) so the store doesn't
// churn N parallel BuildFromStore goroutines under load.
func (s *PacketStore) ensureNeighborGraph() {
	if g := s.graph.Load(); g != nil && !g.IsStale() {
		return
	}
	s.rebuildMu.Lock()
	// Re-check under lock to avoid racing two callers past the cheap check.
	if g := s.graph.Load(); g != nil && !g.IsStale() {
		s.rebuildMu.Unlock()
		return
	}
	if s.rebuildInFlt != nil {
		// Another caller is rebuilding — wait for it.
		ch := s.rebuildInFlt
		s.rebuildMu.Unlock()
		<-ch
		return
	}
	// We're the leader. Publish the channel before unlocking so late
	// arrivals can attach.
	done := make(chan struct{})
	s.rebuildInFlt = done
	s.rebuildMu.Unlock()

	// Defer cleanup so a panic in buildGraphFn doesn't leak the in-flight
	// channel (which would deadlock every future waiter).
	var g *NeighborGraph
	defer func() {
		if g != nil {
			s.graph.Store(g)
		}
		s.rebuildMu.Lock()
		s.rebuildInFlt = nil
		s.rebuildMu.Unlock()
		close(done)
	}()

	g = buildGraphFn(s)
}

// rebuildInFlight reports whether a graph rebuild is currently in progress.
// Used by callers that want to avoid spawning a goroutine that would just
// block on the in-flight singleflight wait (PR #1208 carmack #2).
func (s *PacketStore) rebuildInFlight() bool {
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()
	return s.rebuildInFlt != nil
}
