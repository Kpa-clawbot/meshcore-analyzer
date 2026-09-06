package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// End-to-end fixture test for issue #1201 sub-task 4.
//
// Builds a *PacketStore with multi-candidate-prefix nodes (intentional 1-byte
// prefix collisions across continents) and asserts that the top-hops ranking
// produced by buildDistanceIndex honors the resolver's neighbor-affinity
// choice, NOT the misresolution interpretations that would survive without
// context.
//
// Mutation-test sentinel: this test MUST fail if any call site that feeds
// per-tx context to the hop resolver is reverted to `nil`. Reproduce by
// replacing the `setContext(buildHopContextPubkeys(tx, pm))` call inside
// buildDistanceIndex (cmd/server/store.go, in the per-tx loop) with
// `setContext(nil)` and re-running this test — it fails with a "CA↔CA hop
// missing, saw 72dddd→8acccc (Berlin↔Berlin)" assertion. See PR body for
// the full mutation log.
//
// Fixture layout (no real handles — generic placeholders only):
//   Prefix "72" (4 candidates, all repeaters with GPS):
//     - 72aa…  SLO-CA   (35.30, -120.70)  obsCount=5
//     - 72bb…  LA-CA    (34.05, -118.25)  obsCount=5
//     - 72cc…  NYC      (40.70,  -74.00)  obsCount=5
//     - 72dd…  Berlin   (52.50,   13.40)  obsCount=200  ← would win tier-3
//   Prefix "8a" (3 candidates):
//     - 8aaa…  SF-CA    (37.00, -120.50)  obsCount=5
//     - 8abb…  CA-other (36.50, -119.50)  obsCount=5
//     - 8acc…  Berlin   (52.60,   13.50)  obsCount=200  ← would win tier-3
//
// Sender: CA repeater at (36.0, -120.0), pubkey "ccc…".
// Observer: CA repeater at (36.2, -120.2), pubkey "dddd…".
//
// Affinity graph: strong edges sender↔72aa and sender↔8aaa
// (count ≥ affinityMinObservations, recent timestamps).
//
// 50 synthetic transmissions, all with path ["72","8a"]. With per-tx context
// piped through (sender pubkey is added by buildHopContextPubkeys), tier 1
// picks the CA candidates. Without it, tier 3 picks the Berlin candidates
// and the Berlin↔Berlin hop (~11 km — under 300 km cap) becomes the only
// surviving hop. The test asserts the inverse: CA↔CA hop present, no
// Berlin pubkeys appear in distHops.

const (
	t1201Sender   = "ccccccccccccccc1"
	t1201Observer = "dddddddddddddddd"

	t1201_72aa = "72aaaaaaaaaaaaaa" // SLO
	t1201_72bb = "72bbbbbbbbbbbbbb" // LA
	t1201_72cc = "72cccccccccccccc" // NYC
	t1201_72dd = "72dddddddddddddd" // Berlin

	t1201_8aaa = "8aaaaaaaaaaaaaaa" // SF
	t1201_8abb = "8abbbbbbbbbbbbbb" // CA-other
	t1201_8acc = "8acccccccccccccc" // Berlin
)

type t1201Node struct {
	pk       string
	lat, lon float64
	obsCount int
}

func t1201InsertNode(t *testing.T, db *DB, n t1201Node) {
	t.Helper()
	// NOTE: `obsCount` is written to the `advert_count` column. That column
	// is what resolveWithContext reads (via nodeInfo.ObservationCount /
	// betterByObsCount) as the tier-3 popularity tiebreak. If the tier-3
	// source column ever changes (e.g. observations.packet_count), the
	// "Berlin would win tier-3" premise of this fixture weakens silently —
	// update both this insert and the candidate scoring assertions.
	_, err := db.conn.Exec(
		`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count) VALUES (?, ?, 'repeater', ?, ?, ?, '2026-01-01T00:00:00Z', ?)`,
		n.pk, "node-"+n.pk[:4], n.lat, n.lon, "2026-05-01T00:00:00Z", n.obsCount,
	)
	if err != nil {
		t.Fatalf("insert node %s: %v", n.pk, err)
	}
}

// TestTopHopsRespectsContextAcrossAllCallSites is the end-to-end regression
// sentinel for issue #1201. See file-header docblock for design.
func TestTopHopsRespectsContextAcrossAllCallSites(t *testing.T) {
	db := setupTestDB(t)

	// Insert all repeater nodes with GPS + observation counts.
	nodes := []t1201Node{
		{t1201Sender, 36.0, -120.0, 50},
		{t1201Observer, 36.2, -120.2, 60},

		{t1201_72aa, 35.30, -120.70, 5},
		{t1201_72bb, 34.05, -118.25, 5},
		{t1201_72cc, 40.70, -74.00, 5},
		{t1201_72dd, 52.50, 13.40, 200}, // would win tier-3 without context

		{t1201_8aaa, 37.00, -120.50, 5},
		{t1201_8abb, 36.50, -119.50, 5},
		{t1201_8acc, 52.60, 13.50, 200}, // would win tier-3 without context
	}
	for _, n := range nodes {
		t1201InsertNode(t, db, n)
	}

	// Insert observer row (referenced by observations via observer_idx).
	if _, err := db.conn.Exec(
		`INSERT INTO observers (id, name, last_seen, first_seen, packet_count) VALUES (?, ?, ?, '2026-01-01T00:00:00Z', 100)`,
		t1201Observer, "obs-ca", "2026-05-01T00:00:00Z",
	); err != nil {
		t.Fatal(err)
	}

	// Insert 50 transmissions, each with path ["72","8a"], sender pubkey
	// embedded in decoded_json (read by buildHopContextPubkeys via ParsedDecoded).
	// Wrapped in a single BEGIN/COMMIT — shaves wall time on slow CI runners.
	decoded, _ := json.Marshal(map[string]interface{}{"pubKey": t1201Sender, "type": "data"})
	pathJSON := `["72","8a"]`
	baseTime := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	tx, err := db.conn.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	for i := 0; i < 50; i++ {
		ts := baseTime.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		hash := fmt.Sprintf("hash1201_%03d", i)
		res, err := tx.Exec(
			`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json) VALUES (?, ?, ?, 1, 1, ?)`,
			"AA", hash, ts, string(decoded),
		)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		txID, _ := res.LastInsertId()
		if _, err := tx.Exec(
			`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (?, 1, 12.0, -90, ?, ?)`,
			txID, pathJSON, baseTime.Add(time.Duration(i)*time.Minute).Unix(),
		); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	// Build store and seed graph BEFORE Load() — Load calls buildDistanceIndex
	// which reads s.graph; if it's nil, tier 1 is skipped.
	store := NewPacketStore(db, nil)
	g := NewNeighborGraph()
	// Strong sender↔72aa and sender↔8aaa edges (count well above
	// affinityMinObservations, recent timestamp).
	now := time.Now()
	for i := 0; i < 100; i++ {
		g.upsertEdge(t1201Sender, t1201_72aa, "72", t1201Observer, nil, now.Add(-time.Duration(i)*time.Minute))
		g.upsertEdge(t1201Sender, t1201_8aaa, "8a", t1201Observer, nil, now.Add(-time.Duration(i)*time.Minute))
	}
	// Weaker sender↔Berlin edges so even if someone weakens the ratio guard,
	// the CA candidates still dominate by 100× — and the Berlin counts in
	// node table don't bleed through.
	for i := 0; i < 2; i++ {
		g.upsertEdge(t1201Sender, t1201_72dd, "72", t1201Observer, nil, now.Add(-time.Duration(i)*time.Hour))
	}
	store.graph.Store(g)

	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// #1011: distance index is now lazy — trigger it explicitly and
	// wait for build completion before inspecting distHops.
	store.TriggerDistanceIndexBuild()
	deadline := time.Now().Add(5 * time.Second)
	for !store.DistanceIndexBuilt() {
		if time.Now().After(deadline) {
			t.Fatal("distance index did not finish building within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Inspect precomputed distance index.
	store.mu.RLock()
	hops := make([]distHopRecord, len(store.distHops))
	copy(hops, store.distHops)
	store.mu.RUnlock()

	if len(hops) == 0 {
		t.Fatal("buildDistanceIndex produced zero hops; expected at least the CA↔CA leg")
	}

	// Assertion 1: CA↔CA hop between 72aa (SLO) and 8aaa (SF) must appear.
	pairHas := func(h *distHopRecord, a, b string) bool {
		return (h.FromPk == a && h.ToPk == b) || (h.FromPk == b && h.ToPk == a)
	}
	var sawCAPair bool
	for i := range hops {
		if pairHas(&hops[i], t1201_72aa, t1201_8aaa) {
			sawCAPair = true
			break
		}
	}
	if !sawCAPair {
		// Surface what we did see so failure is debuggable.
		seen := []string{}
		for i := range hops {
			seen = append(seen, fmt.Sprintf("%s→%s@%.1fkm", hops[i].FromPk[:6], hops[i].ToPk[:6], hops[i].Dist))
			if i >= 5 {
				seen = append(seen, "…")
				break
			}
		}
		t.Fatalf("expected CA↔CA hop (72aa↔8aaa) in distHops; saw %v", seen)
	}

	// Assertion 2: no hop should reference Berlin pubkeys. The Berlin↔Berlin
	// pair is the misresolution-only outcome that emerges when context is
	// dropped; its presence proves a regression at one of the call sites.
	// Note: 72cc (NYC) is omitted from this guard — its obsCount=5 would
	// never win the tier-3 obsCount-200 fight against Berlin, so checking
	// for it was redundant defense. Berlin pubkeys carry the signal.
	berlinPKs := map[string]bool{
		t1201_72dd: true,
		t1201_8acc: true,
	}
	for i := range hops {
		if berlinPKs[hops[i].FromPk] || berlinPKs[hops[i].ToPk] {
			t.Fatalf("misresolution hop leaked into distHops: %s→%s dist=%.1fkm (any call site dropped context?)",
				hops[i].FromPk, hops[i].ToPk, hops[i].Dist)
		}
	}

	// Assertion 3: top-hop max distance must be consistent with CA geometry,
	// well under the continent-spanning misresolution range.
	maxDist := 0.0
	for i := range hops {
		if hops[i].Dist > maxDist {
			maxDist = hops[i].Dist
		}
	}
	// SLO→SF ≈ 190 km; LA→SF ≈ 560 km (>300 cap → dropped). Cap should
	// keep max well under 300. We drop the lower-bound "suspiciously small"
	// floor: the >300 ceiling carries the misresolution signal on its own,
	// and a tight floor would false-fire if a future cap tightening or
	// fixture tweak legitimately shrinks the surviving CA↔CA leg.
	if maxDist > 300 {
		t.Fatalf("top-hop max distance %.1fkm exceeds 300km cap — resolver picked continent-spanning candidate", maxDist)
	}
}
