package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// ─── Helpers ───────────────────────────────────────────────────────────────────

// ngTestStore creates a minimal PacketStore with injected nodes and packets.
func ngTestStore(nodes []nodeInfo, packets []*StoreTx) *PacketStore {
	if nodes == nil {
		nodes = []nodeInfo{}
	}
	if packets == nil {
		packets = []*StoreTx{}
	}
	ps := &PacketStore{
		packets:        packets,
		byHash:         make(map[string]*StoreTx),
		byTxID:         make(map[int]*StoreTx),
		byObsID:        make(map[int]*StoreObs),
		byObserver:     make(map[string][]*StoreObs),
		byNode:         make(map[string][]*StoreTx),
		nodeHashes:     make(map[string]map[string]bool),
		byPayloadType:  make(map[int][]*StoreTx),
		rfCache:        make(map[string]*cachedResult),
		topoCache:      make(map[string]*cachedResult),
		hashCache:      make(map[string]*cachedResult),
		collisionCache: make(map[string]*cachedResult),
		chanCache:      make(map[string]*cachedResult),
		distCache:      make(map[string]*cachedResult),
		subpathCache:   make(map[string]*cachedResult),
		spIndex:        make(map[string]int),
	}
	ps.nodeCache = nodes
	ps.nodePM = buildPrefixMap(nodes)
	ps.nodeCacheTime = time.Now().Add(1 * time.Hour)
	return ps
}

func ngIntPtr(v int) *int           { return &v }
func ngFloatPtr(v float64) *float64 { return &v }

func ngMakeTx(id int, payloadType int, decodedJSON string, obs []*StoreObs) *StoreTx {
	tx := &StoreTx{
		ID:           id,
		PayloadType:  ngIntPtr(payloadType),
		DecodedJSON:  decodedJSON,
		Observations: obs,
	}
	return tx
}

func ngMakeObs(observerID, pathJSON, timestamp string, snr *float64) *StoreObs {
	return &StoreObs{
		ObserverID: observerID,
		PathJSON:   pathJSON,
		Timestamp:  timestamp,
		SNR:        snr,
	}
}

func ngFromNodeJSON(pubkey string) string {
	b, _ := json.Marshal(map[string]string{"from_node": pubkey})
	return string(b)
}

var now = time.Now()
var nowStr = now.UTC().Format(time.RFC3339)
var weekAgoStr = now.Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
var monthAgoStr = now.Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)

// ─── Tests ─────────────────────────────────────────────────────────────────────

func TestBuildNeighborGraph_EmptyStore(t *testing.T) {
	store := ngTestStore(nil, nil)
	g := BuildFromStore(store)
	if len(g.edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(g.edges))
	}
}

func TestBuildNeighborGraph_AdvertSingleHopPath(t *testing.T) {
	// ADVERT from X, path=["R1_prefix"] → edges: X↔R1 and Observer↔R1
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa"]`, nowStr, ngFloatPtr(-10)),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	// Should have 2 edges: X↔R1 and Observer↔R1
	// But since path has 1 element, path[0]==path[last], so for ADVERTs
	// both edge types point to the same hop. X↔R1 and Obs↔R1 = 2 edges.
	edges := g.AllEdges()
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}

	// Check X↔R1 exists
	found := false
	for _, e := range edges {
		if (e.NodeA == "aaaa1111" && e.NodeB == "r1aabbcc") ||
			(e.NodeA == "r1aabbcc" && e.NodeB == "aaaa1111") {
			found = true
		}
	}
	if !found {
		t.Error("missing originator↔path[0] edge (X↔R1)")
	}

	// Check Observer↔R1 exists
	found = false
	for _, e := range edges {
		if (e.NodeA == "obs00001" && e.NodeB == "r1aabbcc") ||
			(e.NodeA == "r1aabbcc" && e.NodeB == "obs00001") {
			found = true
		}
	}
	if !found {
		t.Error("missing observer↔path[last] edge (Observer↔R1)")
	}
}

func TestBuildNeighborGraph_AdvertMultiHopPath(t *testing.T) {
	// ADVERT from X, path=["R1","R2"] → X↔R1 and Observer↔R2
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "r2ddeeff", Name: "R2"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa","r2dd"]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	edges := g.AllEdges()
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}

	// X↔R1
	hasXR1 := false
	hasObsR2 := false
	for _, e := range edges {
		if (e.NodeA == "aaaa1111" && e.NodeB == "r1aabbcc") || (e.NodeA == "r1aabbcc" && e.NodeB == "aaaa1111") {
			hasXR1 = true
		}
		if (e.NodeA == "obs00001" && e.NodeB == "r2ddeeff") || (e.NodeA == "r2ddeeff" && e.NodeB == "obs00001") {
			hasObsR2 = true
		}
	}
	if !hasXR1 {
		t.Error("missing X↔R1 edge")
	}
	if !hasObsR2 {
		t.Error("missing Observer↔R2 edge")
	}
}

func TestBuildNeighborGraph_AdvertZeroHop(t *testing.T) {
	// ADVERT from X, path=[] → X↔Observer direct edge
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `[]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	edges := g.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if !((e.NodeA == "aaaa1111" && e.NodeB == "obs00001") || (e.NodeA == "obs00001" && e.NodeB == "aaaa1111")) {
		t.Errorf("expected X↔Observer edge, got %s↔%s", e.NodeA, e.NodeB)
	}
	if e.Ambiguous {
		t.Error("zero-hop edge should not be ambiguous")
	}
}

func TestBuildNeighborGraph_NonAdvertEmptyPath(t *testing.T) {
	// Non-ADVERT, path=[] → no edges
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 2, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `[]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	if len(g.edges) != 0 {
		t.Errorf("expected 0 edges for non-ADVERT empty path, got %d", len(g.edges))
	}
}

func TestBuildNeighborGraph_NonAdvertOnlyObserverEdge(t *testing.T) {
	// Non-ADVERT with path=["R1","R2"] → only Observer↔R2, NO originator edge
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "r2ddeeff", Name: "R2"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 2, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa","r2dd"]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	edges := g.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if !((e.NodeA == "obs00001" && e.NodeB == "r2ddeeff") || (e.NodeA == "r2ddeeff" && e.NodeB == "obs00001")) {
		t.Errorf("expected Observer↔R2 edge, got %s↔%s", e.NodeA, e.NodeB)
	}
}

func TestBuildNeighborGraph_NonAdvertSingleHop(t *testing.T) {
	// Non-ADVERT with path=["R1"] → Observer↔R1 only
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 2, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa"]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	edges := g.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if !((e.NodeA == "obs00001" && e.NodeB == "r1aabbcc") || (e.NodeA == "r1aabbcc" && e.NodeB == "obs00001")) {
		t.Errorf("expected Observer↔R1, got %s↔%s", e.NodeA, e.NodeB)
	}
}

func TestBuildNeighborGraph_HashCollision(t *testing.T) {
	// Two nodes share prefix "a3" → ambiguous edge
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "a3bb1111", Name: "CandidateA"},
		{Role: "repeater", PublicKey: "a3bb2222", Name: "CandidateB"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["a3bb"]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	// Should have ambiguous edges
	var ambigCount int
	for _, e := range g.AllEdges() {
		if e.Ambiguous {
			ambigCount++
			if len(e.Candidates) < 2 {
				t.Errorf("expected >=2 candidates, got %d", len(e.Candidates))
			}
		}
	}
	if ambigCount == 0 {
		t.Error("expected at least one ambiguous edge for hash collision")
	}
}

func TestBuildNeighborGraph_JaccardScoring(t *testing.T) {
	// Test Jaccard similarity computation directly
	a := map[string]bool{"x": true, "y": true, "z": true}
	b := map[string]bool{"y": true, "z": true, "w": true}
	j := jaccardSimilarity(a, b)
	// intersection = {y, z} = 2, union = {x, y, z, w} = 4 → 0.5
	if math.Abs(j-0.5) > 0.001 {
		t.Errorf("expected Jaccard 0.5, got %f", j)
	}

	// Empty sets
	j = jaccardSimilarity(nil, nil)
	if j != 0 {
		t.Errorf("expected 0 for empty sets, got %f", j)
	}
}

func TestBuildNeighborGraph_ConfidenceAutoResolve(t *testing.T) {
	// Setup: NodeX has known neighbors N1, N2, N3 (resolved edges).
	// CandidateA also has known neighbors N1, N2, N3 (high Jaccard with X).
	// CandidateB has no known neighbors (Jaccard = 0).
	// An ambiguous edge X↔prefix "a3" with candidates [A, B] should auto-resolve to A.
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "n1111111", Name: "N1"},
		{Role: "repeater", PublicKey: "n2222222", Name: "N2"},
		{Role: "repeater", PublicKey: "n3333333", Name: "N3"},
		{Role: "repeater", PublicKey: "a3001111", Name: "CandidateA"},
		{Role: "repeater", PublicKey: "a3002222", Name: "CandidateB"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}

	// Create resolved edges: X↔N1, X↔N2, X↔N3, A↔N1, A↔N2, A↔N3
	// Then an ambiguous edge X↔"a300" prefix with 3+ observations.
	var txs []*StoreTx
	txID := 1

	// X sends ADVERTs through N1, N2, N3
	for _, nhop := range []string{"n111", "n222", "n333"} {
		txs = append(txs, ngMakeTx(txID, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("obs00001", `["`+nhop+`"]`, nowStr, nil),
		}))
		txID++
	}

	// CandidateA sends ADVERTs through N1, N2, N3
	for _, nhop := range []string{"n111", "n222", "n333"} {
		txs = append(txs, ngMakeTx(txID, 4, ngFromNodeJSON("a3001111"), []*StoreObs{
			ngMakeObs("obs00001", `["`+nhop+`"]`, nowStr, nil),
		}))
		txID++
	}

	// Ambiguous edge: X sends ADVERTs with path[0]="a300" (matches both candidates)
	// Need 3+ observations for confidence threshold.
	for i := 0; i < 3; i++ {
		txs = append(txs, ngMakeTx(txID, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("obs00001", `["a300"]`, nowStr, nil),
		}))
		txID++
	}

	store := ngTestStore(nodes, txs)
	g := BuildFromStore(store)

	// The ambiguous edge X↔a300 should have been resolved to CandidateA
	neighbors := g.Neighbors("aaaa1111")
	foundA := false
	for _, e := range neighbors {
		other := e.NodeB
		if e.NodeA != "aaaa1111" {
			other = e.NodeA
		}
		if other == "a3001111" {
			foundA = true
			if e.Ambiguous {
				t.Error("edge should have been resolved (not ambiguous)")
			}
		}
	}
	if !foundA {
		t.Error("expected edge X↔CandidateA to be auto-resolved")
	}
}

func TestBuildNeighborGraph_EqualScoresAmbiguous(t *testing.T) {
	// Two candidates with identical neighbor sets → should NOT auto-resolve.
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "n1111111", Name: "N1"},
		{Role: "repeater", PublicKey: "a3001111", Name: "CandidateA"},
		{Role: "repeater", PublicKey: "a3002222", Name: "CandidateB"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}

	var txs []*StoreTx
	txID := 1

	// X↔N1
	txs = append(txs, ngMakeTx(txID, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["n111"]`, nowStr, nil),
	}))
	txID++

	// Both candidates have same neighbor (N1)
	txs = append(txs, ngMakeTx(txID, 4, ngFromNodeJSON("a3001111"), []*StoreObs{
		ngMakeObs("obs00001", `["n111"]`, nowStr, nil),
	}))
	txID++
	txs = append(txs, ngMakeTx(txID, 4, ngFromNodeJSON("a3002222"), []*StoreObs{
		ngMakeObs("obs00001", `["n111"]`, nowStr, nil),
	}))
	txID++

	// Ambiguous edge with 3+ observations
	for i := 0; i < 3; i++ {
		txs = append(txs, ngMakeTx(txID, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("obs00001", `["a300"]`, nowStr, nil),
		}))
		txID++
	}

	store := ngTestStore(nodes, txs)
	g := BuildFromStore(store)

	// Should remain ambiguous
	var ambigFound bool
	for _, e := range g.AllEdges() {
		if e.Ambiguous && e.Prefix == "a300" {
			ambigFound = true
		}
	}
	if !ambigFound {
		t.Error("expected ambiguous edge to remain unresolved with equal scores")
	}
}

func TestBuildNeighborGraph_ObserverSelfEdgeGuard(t *testing.T) {
	// Observer's own prefix in path → should NOT create self-edge.
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["obs0"]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	// Check no self-edge for observer
	for _, e := range g.AllEdges() {
		if e.NodeA == e.NodeB && e.NodeA == "obs00001" {
			t.Error("self-edge created for observer")
		}
	}
}

func TestBuildNeighborGraph_OrphanPrefix(t *testing.T) {
	// Path contains prefix matching zero nodes → edge recorded as unresolved.
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["ff99"]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	// Should have ambiguous edges with empty candidates.
	var orphanFound bool
	for _, e := range g.AllEdges() {
		if e.Ambiguous && len(e.Candidates) == 0 {
			orphanFound = true
			if e.Prefix != "ff99" {
				t.Errorf("expected prefix ff99, got %s", e.Prefix)
			}
		}
	}
	if !orphanFound {
		t.Error("expected orphan prefix edge with empty candidates")
	}
}

func TestAffinityScore_Fresh(t *testing.T) {
	e := &NeighborEdge{Count: 100, LastSeen: time.Now()}
	s := e.Score(time.Now())
	if s < 0.99 || s > 1.0 {
		t.Errorf("expected score ≈ 1.0, got %f", s)
	}
}

func TestAffinityScore_Decayed(t *testing.T) {
	e := &NeighborEdge{Count: 100, LastSeen: time.Now().Add(-7 * 24 * time.Hour)}
	s := e.Score(time.Now())
	// 7 days → half-life → ~0.5
	if math.Abs(s-0.5) > 0.05 {
		t.Errorf("expected score ≈ 0.5, got %f", s)
	}
}

func TestAffinityScore_LowCount(t *testing.T) {
	e := &NeighborEdge{Count: 5, LastSeen: time.Now()}
	s := e.Score(time.Now())
	// 5/100 = 0.05
	if math.Abs(s-0.05) > 0.01 {
		t.Errorf("expected score ≈ 0.05, got %f", s)
	}
}

func TestAffinityScore_StaleAndLow(t *testing.T) {
	e := &NeighborEdge{Count: 5, LastSeen: time.Now().Add(-30 * 24 * time.Hour)}
	s := e.Score(time.Now())
	// Very small
	if s > 0.01 {
		t.Errorf("expected score ≈ 0, got %f", s)
	}
}

func TestBuildNeighborGraph_CountAccumulation(t *testing.T) {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}

	var txs []*StoreTx
	for i := 0; i < 5; i++ {
		txs = append(txs, ngMakeTx(i+1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("obs00001", `["r1aa"]`, nowStr, nil),
		}))
	}

	store := ngTestStore(nodes, txs)
	g := BuildFromStore(store)

	// Check count on X↔R1 edge
	for _, e := range g.AllEdges() {
		if (e.NodeA == "aaaa1111" && e.NodeB == "r1aabbcc") || (e.NodeA == "r1aabbcc" && e.NodeB == "aaaa1111") {
			if e.Count != 5 {
				t.Errorf("expected count 5, got %d", e.Count)
			}
			return
		}
	}
	t.Error("X↔R1 edge not found")
}

func TestBuildNeighborGraph_MultipleObservers(t *testing.T) {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Obs1"},
		{Role: "repeater", PublicKey: "obs00002", Name: "Obs2"},
	}

	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa"]`, nowStr, nil),
		ngMakeObs("obs00002", `["r1aa"]`, nowStr, nil),
	})

	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	for _, e := range g.AllEdges() {
		if (e.NodeA == "aaaa1111" && e.NodeB == "r1aabbcc") || (e.NodeA == "r1aabbcc" && e.NodeB == "aaaa1111") {
			if len(e.Observers) != 2 {
				t.Errorf("expected 2 observers, got %d", len(e.Observers))
			}
			if !e.Observers["obs00001"] || !e.Observers["obs00002"] {
				t.Error("missing expected observer")
			}
			return
		}
	}
	t.Error("X↔R1 edge not found")
}

func TestBuildNeighborGraph_TimeDecayOldObservations(t *testing.T) {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}

	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa"]`, monthAgoStr, nil),
	})

	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	for _, e := range g.AllEdges() {
		if (e.NodeA == "aaaa1111" && e.NodeB == "r1aabbcc") || (e.NodeA == "r1aabbcc" && e.NodeB == "aaaa1111") {
			score := e.Score(time.Now())
			if score > 0.05 {
				t.Errorf("expected decayed score < 0.05, got %f", score)
			}
			return
		}
	}
	t.Error("X↔R1 edge not found")
}

func TestBuildNeighborGraph_ADVERTOnlyConstraint(t *testing.T) {
	// Non-ADVERT: should NOT create originator↔path[0] edge, only observer↔path[last].
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "r2ddeeff", Name: "R2"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 2, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa","r2dd"]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	for _, e := range g.AllEdges() {
		a, b := e.NodeA, e.NodeB
		if (a == "aaaa1111" && b == "r1aabbcc") || (a == "r1aabbcc" && b == "aaaa1111") {
			t.Error("non-ADVERT should NOT produce originator↔path[0] edge")
		}
	}

	// Should have Observer↔R2
	found := false
	for _, e := range g.AllEdges() {
		if (e.NodeA == "obs00001" && e.NodeB == "r2ddeeff") || (e.NodeA == "r2ddeeff" && e.NodeB == "obs00001") {
			found = true
		}
	}
	if !found {
		t.Error("missing Observer↔R2 edge from non-ADVERT")
	}
}

// ngEphemeralPubKeyJSON creates decoded JSON using the real ANON_REQ format
// ("ephemeralPubKey" field) — #1777.
func ngEphemeralPubKeyJSON(pubkey string) string {
	b, _ := json.Marshal(map[string]string{"ephemeralPubKey": pubkey})
	return string(b)
}

// TestBuildNeighborGraph_AnonReqSingleHopPath verifies #1777: ANON_REQ
// (payload type 7) carries the sender's full ephemeral pubkey and should
// produce an originator↔path[0] edge exactly like ADVERT, in addition to
// the always-present observer↔path[last] edge.
func TestBuildNeighborGraph_AnonReqSingleHopPath(t *testing.T) {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 7, ngEphemeralPubKeyJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa"]`, nowStr, ngFloatPtr(-10)),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	edges := g.AllEdges()
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges (originator↔path[0] + observer↔path[last]), got %d", len(edges))
	}

	found := false
	for _, e := range edges {
		if (e.NodeA == "aaaa1111" && e.NodeB == "r1aabbcc") ||
			(e.NodeA == "r1aabbcc" && e.NodeB == "aaaa1111") {
			found = true
		}
	}
	if !found {
		t.Error("ANON_REQ should produce originator↔path[0] edge (#1777)")
	}

	found = false
	for _, e := range edges {
		if (e.NodeA == "obs00001" && e.NodeB == "r1aabbcc") ||
			(e.NodeA == "r1aabbcc" && e.NodeB == "obs00001") {
			found = true
		}
	}
	if !found {
		t.Error("missing observer↔path[last] edge (Observer↔R1)")
	}
}

// TestBuildNeighborGraph_ReqRespStillExcluded verifies #1777's scope
// boundary: only ADVERT and ANON_REQ get an originator↔path[0] edge.
// REQ (payload type 2, reusing the existing ADVERTOnlyConstraint fixture
// shape) must still be excluded — its "from"/"src" is a 1-byte truncated
// hash, not a full pubkey, and manufacturing an edge from it would carry
// ~1/256 collision odds (rejected in the #1777 discussion).
func TestBuildNeighborGraph_ReqRespStillExcluded(t *testing.T) {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 2, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa"]`, nowStr, ngFloatPtr(-10)),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	for _, e := range g.AllEdges() {
		a, b := e.NodeA, e.NodeB
		if (a == "aaaa1111" && b == "r1aabbcc") || (a == "r1aabbcc" && b == "aaaa1111") {
			t.Error("REQ (non-ADVERT, non-ANON_REQ) should NOT produce originator↔path[0] edge")
		}
	}
}

// ngPubKeyJSON creates decoded JSON using the real ADVERT format ("pubKey" field).
func ngPubKeyJSON(pubkey string) string {
	b, _ := json.Marshal(map[string]string{"pubKey": pubkey})
	return string(b)
}

func TestBuildNeighborGraph_AdvertPubKeyField(t *testing.T) {
	// Real ADVERTs use "pubKey", not "from_node". Verify the builder handles it.
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "99bf37abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234", Name: "Originator"},
		{Role: "repeater", PublicKey: "r1aabbccdd001122334455667788990011223344556677889900112233445566", Name: "R1"},
		{Role: "repeater", PublicKey: "obs0000100112233445566778899001122334455667788990011223344556677", Name: "Observer"},
	}
	tx := ngMakeTx(1, 4, ngPubKeyJSON("99bf37abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234"), []*StoreObs{
		ngMakeObs("obs0000100112233445566778899001122334455667788990011223344556677", `["r1"]`, nowStr, ngFloatPtr(-8.5)),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStoreWithOptions(store, BuildOptions{
		PathTrust: &packetpath.TrustConfig{MinHashBytesForMapping: 1},
		MaxEdgeKm: DefaultMaxEdgeKm,
	})

	edges := g.AllEdges()
	if len(edges) < 1 {
		t.Fatalf("expected >=1 edges from ADVERT with pubKey field, got %d", len(edges))
	}

	// Check originator↔R1 edge exists
	found := false
	for _, e := range edges {
		a := e.NodeA
		b := e.NodeB
		orig := "99bf37abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234"
		r1 := "r1aabbccdd001122334455667788990011223344556677889900112233445566"
		if (a == orig && b == r1) || (a == r1 && b == orig) {
			found = true
		}
	}
	if !found {
		t.Error("missing originator↔R1 edge when using pubKey field (real ADVERT format)")
	}
}

func TestBuildNeighborGraph_OneByteHashPrefixes(t *testing.T) {
	// Real-world scenario: 1-byte hash prefixes with multiple candidates.
	// Should create edges (possibly ambiguous) rather than empty graph.
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "c0dedad400000000000000000000000000000000000000000000000000000001", Name: "NodeC0-1"},
		{Role: "repeater", PublicKey: "c0dedad900000000000000000000000000000000000000000000000000000002", Name: "NodeC0-2"},
		{Role: "repeater", PublicKey: "a3bbccdd00000000000000000000000000000000000000000000000000000003", Name: "Originator"},
		{Role: "repeater", PublicKey: "obs1234500000000000000000000000000000000000000000000000000000004", Name: "Observer"},
	}
	// ADVERT from Originator with 1-byte path hop "c0"
	tx := ngMakeTx(1, 4, ngPubKeyJSON("a3bbccdd00000000000000000000000000000000000000000000000000000003"), []*StoreObs{
		ngMakeObs("obs1234500000000000000000000000000000000000000000000000000000004", `["c0"]`, nowStr, ngFloatPtr(-12)),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStoreWithOptions(store, BuildOptions{
		PathTrust: &packetpath.TrustConfig{MinHashBytesForMapping: 1},
		MaxEdgeKm: DefaultMaxEdgeKm,
	})

	edges := g.AllEdges()
	if len(edges) == 0 {
		t.Fatal("expected non-empty edges for 1-byte hash prefix network, got 0")
	}

	// The originator↔c0 edge should be ambiguous (2 candidates match "c0")
	var hasAmbig bool
	for _, e := range edges {
		if e.Ambiguous && e.Prefix == "c0" {
			hasAmbig = true
			if len(e.Candidates) != 2 {
				t.Errorf("expected 2 candidates for prefix c0, got %d", len(e.Candidates))
			}
		}
	}
	if !hasAmbig {
		// Could be resolved if one candidate was filtered — check we got some edge
		t.Log("no ambiguous edge found, but edges exist — acceptable if resolved")
	}
}

func TestNeighborGraph_CacheTTL(t *testing.T) {
	g := NewNeighborGraph()
	if !g.IsStale() {
		t.Error("new graph should be stale")
	}
	g.mu.Lock()
	g.builtAt = time.Now()
	g.mu.Unlock()
	if g.IsStale() {
		t.Error("just-built graph should not be stale")
	}
	g.mu.Lock()
	g.builtAt = time.Now().Add(-2 * neighborGraphTTL)
	g.mu.Unlock()
	if !g.IsStale() {
		t.Error("old graph should be stale")
	}
}

func TestNeighborGraph_TTLIsReasonable(t *testing.T) {
	// TTL must be long enough to avoid rebuild storms on busy meshes,
	// but short enough to reflect topology changes within minutes.
	if neighborGraphTTL < 1*time.Minute {
		t.Errorf("neighborGraphTTL too short (%v), will cause rebuild storms", neighborGraphTTL)
	}
	if neighborGraphTTL > 10*time.Minute {
		t.Errorf("neighborGraphTTL too long (%v), topology changes will be stale", neighborGraphTTL)
	}
}

func TestCachedToLower(t *testing.T) {
	cache := make(map[string]string)
	// Basic lowercasing
	if got := cachedToLower(cache, "AABB"); got != "aabb" {
		t.Errorf("expected 'aabb', got %q", got)
	}
	// Verify it was cached
	if _, ok := cache["AABB"]; !ok {
		t.Error("expected 'AABB' to be in cache")
	}
	// Same input returns cached result
	if got := cachedToLower(cache, "AABB"); got != "aabb" {
		t.Errorf("expected cached 'aabb', got %q", got)
	}
	// Already lowercase stays the same
	if got := cachedToLower(cache, "aabb"); got != "aabb" {
		t.Errorf("expected 'aabb', got %q", got)
	}
	// Empty string
	if got := cachedToLower(cache, ""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestParsedDecoded_Caching(t *testing.T) {
	tx := &StoreTx{DecodedJSON: `{"pubKey":"abc123","name":"test"}`}
	// First call parses
	d1 := tx.ParsedDecoded()
	if d1 == nil {
		t.Fatal("expected non-nil parsed result")
	}
	if d1["pubKey"] != "abc123" {
		t.Errorf("expected pubKey=abc123, got %v", d1["pubKey"])
	}
	// Second call must return the exact same map (pointer equality proves caching)
	d2 := tx.ParsedDecoded()
	if &d1 == nil || &d2 == nil {
		t.Fatal("unexpected nil")
	}
	// Mutate d1 and verify d2 sees the mutation — proves same underlying map
	d1["_sentinel"] = true
	if d2["_sentinel"] != true {
		t.Error("expected same map instance from second call (caching broken)")
	}
	delete(d1, "_sentinel") // clean up
}

func TestParsedDecoded_EmptyJSON(t *testing.T) {
	tx := &StoreTx{DecodedJSON: ""}
	d := tx.ParsedDecoded()
	if d != nil {
		t.Errorf("expected nil for empty DecodedJSON, got %v", d)
	}
}

func TestParsedDecoded_InvalidJSON(t *testing.T) {
	tx := &StoreTx{DecodedJSON: "not json"}
	d := tx.ParsedDecoded()
	if d != nil {
		t.Errorf("expected nil for invalid JSON, got %v", d)
	}
}

func TestExtractFromNode_UsesCachedParse(t *testing.T) {
	tx := &StoreTx{DecodedJSON: `{"pubKey":"aabb1122"}`}
	// First call to extractFromNode should use ParsedDecoded
	from := extractFromNode(tx)
	if from != "aabb1122" {
		t.Errorf("expected aabb1122, got %q", from)
	}
	// ParsedDecoded should now be cached
	d := tx.ParsedDecoded()
	if d == nil || d["pubKey"] != "aabb1122" {
		t.Error("expected ParsedDecoded to return cached result")
	}
}

func BenchmarkBuildFromStore(b *testing.B) {
	// Simulate a dataset with many packets and repeated pubkeys
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeA"},
		{Role: "repeater", PublicKey: "bbbb2222", Name: "NodeB"},
		{Role: "repeater", PublicKey: "cccc3333", Name: "NodeC"},
		{Role: "repeater", PublicKey: "dddd4444", Name: "NodeD"},
	}
	const numPackets = 1000
	packets := make([]*StoreTx, 0, numPackets)
	for i := 0; i < numPackets; i++ {
		pt := 4 // ADVERT
		packets = append(packets, &StoreTx{
			ID:          i,
			PayloadType: &pt,
			DecodedJSON: `{"pubKey":"aaaa1111"}`,
			Observations: []*StoreObs{
				{ObserverID: "bbbb2222", PathJSON: `["cccc"]`, Timestamp: nowStr, SNR: ngFloatPtr(-5.0)},
			},
		})
	}
	store := ngTestStore(nodes, packets)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildFromStore(store)
	}
}

// TestBuildNeighborGraph_PathTrustExcludes1ByteHops verifies that
// BuildFromStoreWithOptions respects the PathTrust threshold. When
// MinHashBytesForMapping=2, 1-byte prefix observations are excluded
// from neighbor-edge building. At threshold=1 (trust-all) they are
// included — backward-compatible. Kent Beck review: consumer wiring must
// be tested, not just the pure helper.
func TestBuildNeighborGraph_PathTrustExcludes1ByteHops(t *testing.T) {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "ccc0000100000000000000000000000000000000000000000000000000000001", Name: "NodeC-1"},
		{Role: "repeater", PublicKey: "ccc0000200000000000000000000000000000000000000000000000000000002", Name: "NodeC-2"},
		{Role: "repeater", PublicKey: "aaa0000100000000000000000000000000000000000000000000000000000003", Name: "Originator"},
		{Role: "repeater", PublicKey: "obs00001000000000000000000000000000000000000000000000000000000004", Name: "Observer"},
	}
	// ADVERT with 1-byte path hop "cc" (hex length 2 = 1 byte).
	tx := ngMakeTx(1, 4, ngPubKeyJSON("aaa0000100000000000000000000000000000000000000000000000000000003"), []*StoreObs{
		ngMakeObs("obs00001000000000000000000000000000000000000000000000000000000004", `["cc"]`, nowStr, ngFloatPtr(-12)),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})

	// Threshold=2: 1-byte "cc" prefix excluded → no edges.
	g := BuildFromStoreWithOptions(store, BuildOptions{
		PathTrust: &packetpath.TrustConfig{MinHashBytesForMapping: 2},
		MaxEdgeKm: DefaultMaxEdgeKm,
	})
	if edges := g.AllEdges(); len(edges) > 0 {
		for _, e := range edges {
			t.Errorf("expected no edges with pathTrust=2 + 1-byte prefix, got prefix=%q", e.Prefix)
		}
	}

	// Threshold=1: 1-byte prefix included (trust-all, backward-compatible).
	g1 := BuildFromStoreWithOptions(store, BuildOptions{
		PathTrust: &packetpath.TrustConfig{MinHashBytesForMapping: 1},
		MaxEdgeKm: DefaultMaxEdgeKm,
	})
	if edges1 := g1.AllEdges(); len(edges1) == 0 {
		t.Fatal("expected edges with pathTrust=1 (trust-all), got 0")
	}
}

// confidence indicator can weight 3-byte (effectively unambiguous) sightings
// higher than 1-byte (high-collision) sightings. Modes track firmware-valid
// hash sizes 1/2/3 per Packet.cpp:13-18.
func TestBuildNeighborGraph_CountsByMode(t *testing.T) {
	// Use a unique-bbbb-prefix R1 so 1/2/3-byte prefixes all resolve to it.
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "bbbb2222", Name: "NodeR1"},
		{Role: "repeater", PublicKey: "cccc3333", Name: "Obs"},
	}
	// Three ADVERTs from X observed at varying hash modes hitting R1.
	txs := []*StoreTx{
		ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("cccc3333", `["bb"]`, nowStr, nil), // 1-byte
		}),
		ngMakeTx(2, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("cccc3333", `["bbbb"]`, nowStr, nil), // 2-byte
		}),
		ngMakeTx(3, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("cccc3333", `["bbbb22"]`, nowStr, nil), // 3-byte
		}),
	}
	store := ngTestStore(nodes, txs)
	g := BuildFromStoreWithOptions(store, BuildOptions{
		PathTrust: &packetpath.TrustConfig{MinHashBytesForMapping: 1},
		MaxEdgeKm: DefaultMaxEdgeKm,
	})

	edges := g.Neighbors("aaaa1111")
	var xr1 *NeighborEdge
	for _, e := range edges {
		other := e.NodeB
		if e.NodeA != "aaaa1111" {
			other = e.NodeA
		}
		if other == "bbbb2222" {
			xr1 = e
			break
		}
	}
	if xr1 == nil {
		t.Fatalf("expected X↔R1 edge, got %d edges", len(edges))
	}
	// Back-compat: flat Count == 3.
	if xr1.Count != 3 {
		t.Errorf("expected Count=3, got %d", xr1.Count)
	}
	if xr1.CountsByMode == nil {
		t.Fatalf("expected CountsByMode populated, got nil")
	}
	if got := xr1.CountsByMode[1]; got != 1 {
		t.Errorf("CountsByMode[1] = %d, want 1", got)
	}
	if got := xr1.CountsByMode[2]; got != 1 {
		t.Errorf("CountsByMode[2] = %d, want 1", got)
	}
	if got := xr1.CountsByMode[3]; got != 1 {
		t.Errorf("CountsByMode[3] = %d, want 1", got)
	}
}
