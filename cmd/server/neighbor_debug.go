package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ─── Debug API response types ──────────────────────────────────────────────────

type DebugAffinityResponse struct {
	Edges       []DebugEdge       `json:"edges"`
	Resolutions []DebugResolution `json:"resolutions"`
	Stats       DebugStats        `json:"stats"`
}

type DebugEdge struct {
	NodeA            string   `json:"nodeA"`
	NodeAName        string   `json:"nodeAName,omitempty"`
	NodeB            string   `json:"nodeB"`
	NodeBName        string   `json:"nodeBName,omitempty"`
	Prefix           string   `json:"prefix"`
	Weight           int      `json:"weight"`
	ObservationCount int      `json:"observationCount"`
	LastSeen         string   `json:"lastSeen"`
	FirstSeen        string   `json:"firstSeen"`
	Score            float64  `json:"score"`
	Jaccard          float64  `json:"jaccard,omitempty"`
	AvgSNR           *float64 `json:"avgSnr,omitempty"`
	Observers        []string `json:"observers"`
	Ambiguous        bool     `json:"ambiguous"`
	Unresolved       bool     `json:"unresolved,omitempty"`
	Resolved         bool     `json:"resolved,omitempty"`
}

type DebugResolution struct {
	Prefix           string             `json:"prefix"`
	Chosen           string             `json:"chosen,omitempty"`
	ChosenName       string             `json:"chosenName,omitempty"`
	ChosenScore      int                `json:"chosenScore"`
	ChosenJaccard    float64            `json:"chosenJaccard"`
	Confidence       string             `json:"confidence"`
	Candidates       []DebugCandidate   `json:"candidates"`
	Ratio            float64            `json:"ratio"`
	ThresholdApplied float64            `json:"thresholdApplied"`
	Method           string             `json:"method"`
	KnownNode        string             `json:"knownNode"`
	KnownNodeName    string             `json:"knownNodeName,omitempty"`
}

type DebugCandidate struct {
	Pubkey  string  `json:"pubkey"`
	Name    string  `json:"name,omitempty"`
	Score   int     `json:"score"`
	Jaccard float64 `json:"jaccard"`
}

type DebugStats struct {
	TotalEdges       int     `json:"totalEdges"`
	TotalNodes       int     `json:"totalNodes"`
	ResolvedCount    int     `json:"resolvedCount"`
	AmbiguousCount   int     `json:"ambiguousCount"`
	UnresolvedCount  int     `json:"unresolvedCount"`
	AvgConfidence    float64 `json:"avgConfidence"`
	ColdStartCoverage float64 `json:"coldStartCoverage"`
	CacheAge         string  `json:"cacheAge"`
	LastRebuild      string  `json:"lastRebuild"`
}

// ─── Debug API Handler ─────────────────────────────────────────────────────────

func (s *Server) handleDebugAffinity(w http.ResponseWriter, r *http.Request) {
	prefixFilter := strings.ToLower(r.URL.Query().Get("prefix"))
	nodeFilter := strings.ToLower(r.URL.Query().Get("node"))

	graph := s.getNeighborGraph()
	now := time.Now()
	nodeMap := s.buildNodeInfoMap()

	allEdges := graph.AllEdges()

	// Build edges response
	var debugEdges []DebugEdge
	nodeSet := make(map[string]bool)
	resolvedCount := 0
	ambiguousCount := 0
	unresolvedCount := 0
	var scoreSum float64
	var scoreCount int

	for _, e := range allEdges {
		// Apply filters
		if prefixFilter != "" && !strings.EqualFold(e.Prefix, prefixFilter) {
			continue
		}
		if nodeFilter != "" {
			if !strings.EqualFold(e.NodeA, nodeFilter) && !strings.EqualFold(e.NodeB, nodeFilter) {
				// Also check if any candidate matches
				found := false
				for _, c := range e.Candidates {
					if strings.EqualFold(c, nodeFilter) {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
		}

		score := e.Score(now)
		de := DebugEdge{
			NodeA:            e.NodeA,
			NodeB:            e.NodeB,
			Prefix:           e.Prefix,
			Weight:           e.Count,
			ObservationCount: e.Count,
			LastSeen:         e.LastSeen.UTC().Format(time.RFC3339),
			FirstSeen:        e.FirstSeen.UTC().Format(time.RFC3339),
			Score:            math.Round(score*1000) / 1000,
			Observers:        observerList(e.Observers),
			Ambiguous:        e.Ambiguous,
			Resolved:         e.Resolved,
		}

		if e.SNRCount > 0 {
			avg := e.AvgSNR()
			de.AvgSNR = &avg
		}

		// Add names
		if nodeMap != nil {
			if info, ok := nodeMap[strings.ToLower(e.NodeA)]; ok {
				de.NodeAName = info.Name
			}
			if info, ok := nodeMap[strings.ToLower(e.NodeB)]; ok {
				de.NodeBName = info.Name
			}
		}

		if e.Ambiguous {
			if len(e.Candidates) == 0 {
				de.Unresolved = true
				unresolvedCount++
			} else {
				ambiguousCount++
			}
		} else {
			resolvedCount++
			scoreSum += score
			scoreCount++
		}

		debugEdges = append(debugEdges, de)

		if e.NodeA != "" && !strings.HasPrefix(e.NodeA, "prefix:") {
			nodeSet[e.NodeA] = true
		}
		if e.NodeB != "" && !strings.HasPrefix(e.NodeB, "prefix:") {
			nodeSet[e.NodeB] = true
		}
	}

	// Build resolutions from the graph's disambiguation history
	resolutions := s.buildResolutions(graph, nodeMap, prefixFilter, nodeFilter)

	// Cold-start coverage: % of 1-byte prefixes with ≥3 observations
	coldStart := s.computeColdStartCoverage(allEdges)

	avgConf := 0.0
	if scoreCount > 0 {
		avgConf = math.Round(scoreSum/float64(scoreCount)*1000) / 1000
	}

	if debugEdges == nil {
		debugEdges = []DebugEdge{}
	}
	if resolutions == nil {
		resolutions = []DebugResolution{}
	}

	// Sort edges by weight descending
	sort.Slice(debugEdges, func(i, j int) bool {
		return debugEdges[i].Weight > debugEdges[j].Weight
	})

	graph.mu.RLock()
	builtAt := graph.builtAt
	graph.mu.RUnlock()

	cacheAge := ""
	lastRebuild := ""
	if !builtAt.IsZero() {
		cacheAge = fmt.Sprintf("%.1fs", time.Since(builtAt).Seconds())
		lastRebuild = builtAt.UTC().Format(time.RFC3339)
	}

	resp := DebugAffinityResponse{
		Edges:       debugEdges,
		Resolutions: resolutions,
		Stats: DebugStats{
			TotalEdges:        len(debugEdges),
			TotalNodes:        len(nodeSet),
			ResolvedCount:     resolvedCount,
			AmbiguousCount:    ambiguousCount,
			UnresolvedCount:   unresolvedCount,
			AvgConfidence:     avgConf,
			ColdStartCoverage: coldStart,
			CacheAge:          cacheAge,
			LastRebuild:       lastRebuild,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// buildResolutions generates per-prefix resolution decision logs.
func (s *Server) buildResolutions(graph *NeighborGraph, nodeMap map[string]nodeInfo, prefixFilter, nodeFilter string) []DebugResolution {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	// Build resolved neighbor sets for Jaccard computation
	resolvedNeighbors := make(map[string]map[string]bool)
	for _, e := range graph.edges {
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

	var resolutions []DebugResolution

	for _, e := range graph.edges {
		// Show resolution info for both resolved (auto-resolved) and ambiguous edges
		if !e.Resolved && !e.Ambiguous {
			continue
		}
		if len(e.Candidates) < 2 && !e.Resolved {
			continue
		}

		if prefixFilter != "" && !strings.EqualFold(e.Prefix, prefixFilter) {
			continue
		}

		knownNode := e.NodeA
		if strings.HasPrefix(e.NodeA, "prefix:") {
			knownNode = e.NodeB
		}

		if nodeFilter != "" && !strings.EqualFold(knownNode, nodeFilter) {
			// Check if the resolved node matches
			if e.Resolved && !strings.EqualFold(e.NodeB, nodeFilter) && !strings.EqualFold(e.NodeA, nodeFilter) {
				continue
			}
		}

		knownNeighbors := resolvedNeighbors[knownNode]

		var candidates []DebugCandidate
		candList := e.Candidates
		// For resolved edges, add the resolved node as a candidate too
		if e.Resolved {
			resolvedPK := e.NodeB
			if strings.EqualFold(e.NodeB, knownNode) {
				resolvedPK = e.NodeA
			}
			// Include resolved + original candidates
			found := false
			for _, c := range candList {
				if strings.EqualFold(c, resolvedPK) {
					found = true
					break
				}
			}
			if !found {
				candList = append([]string{resolvedPK}, candList...)
			}
		}

		for _, cpk := range candList {
			candNeighbors := resolvedNeighbors[cpk]
			j := jaccardSimilarity(knownNeighbors, candNeighbors)
			dc := DebugCandidate{
				Pubkey:  cpk,
				Score:   e.Count,
				Jaccard: math.Round(j*1000) / 1000,
			}
			if nodeMap != nil {
				if info, ok := nodeMap[strings.ToLower(cpk)]; ok {
					dc.Name = info.Name
				}
			}
			candidates = append(candidates, dc)
		}

		// Sort candidates by Jaccard descending
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Jaccard > candidates[j].Jaccard
		})

		dr := DebugResolution{
			Prefix:           e.Prefix,
			ThresholdApplied: affinityConfidenceRatio,
			KnownNode:        knownNode,
		}

		if nodeMap != nil {
			if info, ok := nodeMap[strings.ToLower(knownNode)]; ok {
				dr.KnownNodeName = info.Name
			}
		}

		if e.Resolved && len(candidates) > 0 {
			dr.Chosen = candidates[0].Pubkey
			dr.ChosenName = candidates[0].Name
			dr.ChosenScore = candidates[0].Score
			dr.ChosenJaccard = candidates[0].Jaccard
			dr.Confidence = "HIGH"
			dr.Method = "auto-resolved"
			if len(candidates) > 1 && candidates[1].Jaccard > 0 {
				dr.Ratio = math.Round(candidates[0].Jaccard/candidates[1].Jaccard*10) / 10
			} else if candidates[0].Jaccard > 0 {
				dr.Ratio = 999.0 // effectively infinite — JSON doesn't support Infinity
			}
		} else {
			dr.Confidence = "AMBIGUOUS"
			dr.Method = "ambiguous"
			if len(candidates) >= 2 {
				dr.ChosenScore = candidates[0].Score
				dr.ChosenJaccard = candidates[0].Jaccard
				if candidates[1].Jaccard > 0 {
					dr.Ratio = math.Round(candidates[0].Jaccard/candidates[1].Jaccard*10) / 10
				}
			}
		}
		dr.Candidates = candidates

		resolutions = append(resolutions, dr)
	}

	return resolutions
}

// computeColdStartCoverage returns the % of active 1-byte hex prefixes with ≥3 observations.
func (s *Server) computeColdStartCoverage(edges []*NeighborEdge) float64 {
	// Track which 1-byte prefixes have sufficient observations
	prefixObs := make(map[string]int) // 1-byte prefix → total observations
	for _, e := range edges {
		if len(e.Prefix) == 2 { // 1-byte = 2 hex chars
			prefixObs[strings.ToLower(e.Prefix)] += e.Count
		}
	}

	if len(prefixObs) == 0 {
		return 0
	}

	covered := 0
	for _, count := range prefixObs {
		if count >= affinityMinObservations {
			covered++
		}
	}

	return math.Round(float64(covered)/float64(len(prefixObs))*1000) / 10
}
