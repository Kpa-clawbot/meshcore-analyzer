package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"
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

func ngIntPtr(v int) *int         { return &v }
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "r1aabbcc", Name: "R1"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "r1aabbcc", Name: "R1"},
		{PublicKey: "r2ddeeff", Name: "R2"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "r1aabbcc", Name: "R1"},
		{PublicKey: "r2ddeeff", Name: "R2"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "r1aabbcc", Name: "R1"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "a3bb1111", Name: "CandidateA"},
		{PublicKey: "a3bb2222", Name: "CandidateB"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "n1111111", Name: "N1"},
		{PublicKey: "n2222222", Name: "N2"},
		{PublicKey: "n3333333", Name: "N3"},
		{PublicKey: "a3001111", Name: "CandidateA"},
		{PublicKey: "a3002222", Name: "CandidateB"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "n1111111", Name: "N1"},
		{PublicKey: "a3001111", Name: "CandidateA"},
		{PublicKey: "a3002222", Name: "CandidateB"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "r1aabbcc", Name: "R1"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "r1aabbcc", Name: "R1"},
		{PublicKey: "obs00001", Name: "Obs1"},
		{PublicKey: "obs00002", Name: "Obs2"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "r1aabbcc", Name: "R1"},
		{PublicKey: "obs00001", Name: "Observer"},
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
		{PublicKey: "aaaa1111", Name: "NodeX"},
		{PublicKey: "r1aabbcc", Name: "R1"},
		{PublicKey: "r2ddeeff", Name: "R2"},
		{PublicKey: "obs00001", Name: "Observer"},
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
