package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

// ─── Neighbor API response types ───────────────────────────────────────────────

type NeighborResponse struct {
	Node              string             `json:"node"`
	Neighbors         []NeighborEntry    `json:"neighbors"`
	TotalObservations int                `json:"total_observations"`
}

type NeighborEntry struct {
	Pubkey      *string          `json:"pubkey"`
	Prefix      string           `json:"prefix"`
	Name        *string          `json:"name"`
	Role        *string          `json:"role"`
	Count       int              `json:"count"`
	Score       float64          `json:"score"`
	FirstSeen   string           `json:"first_seen"`
	LastSeen    string           `json:"last_seen"`
	AvgSNR      *float64         `json:"avg_snr"`
	DistanceKm  *float64         `json:"distance_km,omitempty"`
	Observers   []string         `json:"observers"`
	Ambiguous   bool             `json:"ambiguous"`
	Unresolved  bool             `json:"unresolved,omitempty"`
	Candidates  []CandidateEntry `json:"candidates,omitempty"`
}

type CandidateEntry struct {
	Pubkey string  `json:"pubkey"`
	Name   string  `json:"name"`
	Role   string  `json:"role"`
}

type NeighborGraphResponse struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	Stats GraphStats  `json:"stats"`
}

type GraphNode struct {
	Pubkey        string `json:"pubkey"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	NeighborCount int    `json:"neighbor_count"`
}

type GraphEdge struct {
	Source        string   `json:"source"`
	Target        string   `json:"target"`
	Weight        int      `json:"weight"`
	Score         float64  `json:"score"`
	Bidirectional bool     `json:"bidirectional"`
	AvgSNR        *float64 `json:"avg_snr"`
	Ambiguous     bool     `json:"ambiguous"`
}

type GraphStats struct {
	TotalNodes     int     `json:"total_nodes"`
	TotalEdges     int     `json:"total_edges"`
	AmbiguousEdges int     `json:"ambiguous_edges"`
	AvgClusterSize float64 `json:"avg_cluster_size"`
}

// ─── Graph accessor on Server ──────────────────────────────────────────────────

// getNeighborGraph returns the current neighbor graph, rebuilding if stale.
func (s *Server) getNeighborGraph() *NeighborGraph {
	s.neighborMu.Lock()
	defer s.neighborMu.Unlock()

	if s.neighborGraph == nil || s.neighborGraph.IsStale() {
		if s.store != nil {
			debugLog := s.cfg != nil && s.cfg.DebugAffinity
			s.neighborGraph = BuildFromStoreWithLog(s.store, debugLog)
		} else {
			s.neighborGraph = NewNeighborGraph()
		}
	}
	return s.neighborGraph
}

// ─── Handlers ──────────────────────────────────────────────────────────────────

func (s *Server) handleNodeNeighbors(w http.ResponseWriter, r *http.Request) {
	pubkey := strings.ToLower(mux.Vars(r)["pubkey"])

	minCount := 1
	if v := r.URL.Query().Get("min_count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			minCount = n
		}
	}
	minScore := 0.0
	if v := r.URL.Query().Get("min_score"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			minScore = f
		}
	}
	includeAmbiguous := true
	if v := r.URL.Query().Get("include_ambiguous"); v == "false" {
		includeAmbiguous = false
	}

	graph := s.getNeighborGraph()
	edges := graph.Neighbors(pubkey)
	now := time.Now()

	// Build node info lookup for names/roles/coordinates.
	nodeMap := s.buildNodeInfoMap()

	// Look up the queried node's GPS coordinates for distance computation.
	var srcInfo nodeInfo
	if nodeMap != nil {
		srcInfo = nodeMap[pubkey]
	}

	var entries []NeighborEntry
	totalObs := 0

	for _, e := range edges {
		score := e.Score(now)
		if e.Count < minCount || score < minScore {
			continue
		}
		if e.Ambiguous && !includeAmbiguous {
			continue
		}

		totalObs += e.Count

		// Determine the "other" node (neighbor of the queried pubkey).
		neighborPK := e.NodeA
		if strings.EqualFold(neighborPK, pubkey) {
			neighborPK = e.NodeB
		}

		entry := NeighborEntry{
			Prefix:    e.Prefix,
			Count:     e.Count,
			Score:     score,
			FirstSeen: e.FirstSeen.UTC().Format(time.RFC3339),
			LastSeen:  e.LastSeen.UTC().Format(time.RFC3339),
			Ambiguous: e.Ambiguous,
			Observers: observerList(e.Observers),
		}

		if e.SNRCount > 0 {
			avg := e.AvgSNR()
			entry.AvgSNR = &avg
		}

		if e.Ambiguous {
			if len(e.Candidates) == 0 {
				entry.Unresolved = true
			}
			for _, cpk := range e.Candidates {
				ce := CandidateEntry{Pubkey: cpk}
				if info, ok := nodeMap[strings.ToLower(cpk)]; ok {
					ce.Name = info.Name
					ce.Role = info.Role
				}
				entry.Candidates = append(entry.Candidates, ce)
			}
		} else if neighborPK != "" {
			entry.Pubkey = &neighborPK
			if info, ok := nodeMap[strings.ToLower(neighborPK)]; ok {
				entry.Name = &info.Name
				entry.Role = &info.Role
				if srcInfo.HasGPS && info.HasGPS {
					d := haversineKm(srcInfo.Lat, srcInfo.Lon, info.Lat, info.Lon)
					entry.DistanceKm = &d
				}
			}
		}

		entries = append(entries, entry)
	}

	// Defense-in-depth: deduplicate unresolved prefix entries that match
	// resolved pubkey entries in the same neighbor set (fixes #698).
	entries = dedupPrefixEntries(entries)

	// Sort by score descending.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Score > entries[j].Score
	})

	if entries == nil {
		entries = []NeighborEntry{}
	}

	resp := NeighborResponse{
		Node:              pubkey,
		Neighbors:         entries,
		TotalObservations: totalObs,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleNeighborGraph(w http.ResponseWriter, r *http.Request) {
	minCount := 5
	if v := r.URL.Query().Get("min_count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			minCount = n
		}
	}
	minScore := 0.1
	if v := r.URL.Query().Get("min_score"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			minScore = f
		}
	}
	region := r.URL.Query().Get("region")
	roleFilter := strings.ToLower(r.URL.Query().Get("role"))

	graph := s.getNeighborGraph()
	allEdges := graph.AllEdges()
	now := time.Now()

	// Resolve region observers if filtering.
	var regionObs map[string]bool
	if region != "" && s.store != nil {
		regionObs = s.store.resolveRegionObservers(region)
	}

	nodeMap := s.buildNodeInfoMap()
	nodeSet := make(map[string]bool)
	var filteredEdges []GraphEdge
	ambiguousCount := 0

	for _, e := range allEdges {
		score := e.Score(now)
		if e.Count < minCount || score < minScore {
			continue
		}

		// Role filter: at least one endpoint must match the role.
		if roleFilter != "" && nodeMap != nil {
			aInfo, aOK := nodeMap[strings.ToLower(e.NodeA)]
			bInfo, bOK := nodeMap[strings.ToLower(e.NodeB)]
			aMatch := aOK && strings.EqualFold(aInfo.Role, roleFilter)
			bMatch := bOK && strings.EqualFold(bInfo.Role, roleFilter)
			if !aMatch && !bMatch {
				continue
			}
		}

		// Region filter: at least one observer must be in the region.
		if regionObs != nil {
			match := false
			for obs := range e.Observers {
				if regionObs[obs] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		ge := GraphEdge{
			Source:        e.NodeA,
			Target:        e.NodeB,
			Weight:        e.Count,
			Score:         score,
			Bidirectional: true,
			Ambiguous:     e.Ambiguous,
		}
		if e.SNRCount > 0 {
			avg := e.AvgSNR()
			ge.AvgSNR = &avg
		}

		if e.Ambiguous {
			ambiguousCount++
			// For ambiguous edges, use prefix as target.
			if e.NodeB == "" {
				ge.Target = "prefix:" + e.Prefix
			}
		}

		filteredEdges = append(filteredEdges, ge)

		// Track nodes.
		if e.NodeA != "" && !strings.HasPrefix(e.NodeA, "prefix:") {
			nodeSet[e.NodeA] = true
		}
		if e.NodeB != "" && !strings.HasPrefix(e.NodeB, "prefix:") {
			nodeSet[e.NodeB] = true
		}
	}

	// Build node list.
	// Count neighbors per node from filtered edges.
	neighborCounts := make(map[string]int)
	for _, ge := range filteredEdges {
		neighborCounts[ge.Source]++
		neighborCounts[ge.Target]++
	}

	var nodes []GraphNode
	for pk := range nodeSet {
		gn := GraphNode{Pubkey: pk, NeighborCount: neighborCounts[pk]}
		if info, ok := nodeMap[strings.ToLower(pk)]; ok {
			gn.Name = info.Name
			gn.Role = info.Role
		}
		nodes = append(nodes, gn)
	}

	if filteredEdges == nil {
		filteredEdges = []GraphEdge{}
	}
	if nodes == nil {
		nodes = []GraphNode{}
	}

	avgCluster := 0.0
	if len(nodes) > 0 {
		avgCluster = float64(len(filteredEdges)*2) / float64(len(nodes))
	}

	resp := NeighborGraphResponse{
		Nodes: nodes,
		Edges: filteredEdges,
		Stats: GraphStats{
			TotalNodes:     len(nodes),
			TotalEdges:     len(filteredEdges),
			AmbiguousEdges: ambiguousCount,
			AvgClusterSize: avgCluster,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ─── Helpers ───────────────────────────────────────────────────────────────────

func observerList(m map[string]bool) []string {
	if len(m) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// buildNodeInfoMap returns a map of lowercase pubkey → nodeInfo for name/role lookups.
func (s *Server) buildNodeInfoMap() map[string]nodeInfo {
	if s.store == nil {
		return nil
	}
	nodes, _ := s.store.getCachedNodesAndPM()
	m := make(map[string]nodeInfo, len(nodes))
	for _, n := range nodes {
		m[strings.ToLower(n.PublicKey)] = n
	}
	return m
}

// dedupPrefixEntries merges unresolved prefix entries with resolved pubkey entries
// where the prefix is a prefix of the resolved pubkey. Defense-in-depth for #698.
func dedupPrefixEntries(entries []NeighborEntry) []NeighborEntry {
	if len(entries) < 2 {
		return entries
	}

	// Mark indices of unresolved entries to remove after merging.
	remove := make(map[int]bool)

	for i := range entries {
		if entries[i].Pubkey != nil {
			continue // only check unresolved (no pubkey)
		}
		prefix := strings.ToLower(entries[i].Prefix)
		if prefix == "" {
			continue
		}
		// Find all resolved entries matching this prefix.
		matchIdx := -1
		matchCount := 0
		for j := range entries {
			if i == j || entries[j].Pubkey == nil {
				continue
			}
			if strings.HasPrefix(strings.ToLower(*entries[j].Pubkey), prefix) {
				matchIdx = j
				matchCount++
			}
		}
		// Only merge when exactly one resolved entry matches — ambiguous
		// prefixes that match multiple resolved neighbors must not be
		// arbitrarily assigned to one of them.
		if matchCount != 1 {
			continue
		}
		j := matchIdx

		// Merge counts from unresolved into resolved.
		entries[j].Count += entries[i].Count

		// Preserve higher LastSeen.
		if entries[i].LastSeen > entries[j].LastSeen {
			entries[j].LastSeen = entries[i].LastSeen
		}

		// Merge observers.
		obsSet := make(map[string]bool)
		for _, o := range entries[j].Observers {
			obsSet[o] = true
		}
		for _, o := range entries[i].Observers {
			obsSet[o] = true
		}
		entries[j].Observers = observerList(obsSet)

		remove[i] = true
	}

	if len(remove) == 0 {
		return entries
	}

	result := make([]NeighborEntry, 0, len(entries)-len(remove))
	for i, e := range entries {
		if !remove[i] {
			result = append(result, e)
		}
	}
	return result
}
