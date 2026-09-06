package main

// Regression tests for #1816 / #1818:
//
// getNodeClockSkewLocked() iterated every ADVERT transaction indexed under
// a pubkey in s.byNode WITHOUT checking whether the node actually
// originated (self-signed) that advert. byNode is an involvement index
// (store.go:1696-1705, indexResolvedPathHops, #1558/#1352): a transmission
// is also indexed under every relay-hop pubkey extracted from an
// observation's resolved_path. So a relay that merely forwarded a
// broken-clock node's advert inherited that node's skew as if it were its
// own — producing:
//
//   - Fleet-wide false no_clock / bimodal_clock classifications on healthy
//     relays whose only "bad" samples are adverts they relayed (#1816).
//   - Bit-identical RecentMedianSkewSec "clusters" across unrelated relays
//     that all forwarded the same broken-clock originator (#1816).
//   - A single relay showing a nonsense multi-day skew even though its own
//     self-adverts are perfectly healthy, because 1-2 relayed adverts from
//     a broken originator landed in the tail of its small recent-window
//     sample (#1818 — the "island" repro from cwichura).
//
// The fix (txOriginatedBy in clock_skew.go) restricts the ADVERT filter in
// getNodeClockSkewLocked to self-originated adverts only (decoded pubKey
// == pubkey), leaving byNode itself untouched (#1558/#1352 still depend on
// the broader involvement index for other consumers).

import (
	"testing"
	"time"
)

// ── txOriginatedBy ───────────────────────────────────────────────────────────

func TestTxOriginatedBy(t *testing.T) {
	pt := 4 // ADVERT
	selfTx := &StoreTx{
		Hash:        "self-1",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":1700000000},"pubKey":"AABBCC"}`,
	}
	foreignTx := &StoreTx{
		Hash:        "foreign-1",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":1700000000},"pubKey":"DDEEFF"}`,
	}
	noPubkeyTx := &StoreTx{
		Hash:        "nopk-1",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":1700000000}}`,
	}
	mixedCaseTx := &StoreTx{
		Hash:        "case-1",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":1700000000},"pubKey":"aabbcc"}`,
	}

	if !txOriginatedBy(selfTx, "AABBCC") {
		t.Error("expected self-signed advert to match its own pubkey")
	}
	if txOriginatedBy(foreignTx, "AABBCC") {
		t.Error("expected advert from a different originator NOT to match")
	}
	if txOriginatedBy(noPubkeyTx, "AABBCC") {
		t.Error("expected advert with no decoded pubKey NOT to match anything")
	}
	if !txOriginatedBy(mixedCaseTx, "AABBCC") {
		t.Error("expected case-insensitive pubkey match to succeed")
	}
}

// ── #1816: relay must not inherit the originator's skew ───────────────────────

// A relay ("RELAY001") only ever transmits healthy self-adverts (skew ~0s).
// It also forwards adverts from a broken-clock originator ("BROKENORIG",
// skew ~+100,500s, matching the #1816 report's +100.5k band) — those
// transmissions are indexed under RELAY001 in byNode too, exactly as
// indexResolvedPathHops does for real relay-hop traffic. Before the fix,
// these foreign adverts polluted RELAY001's skew stream and could flip its
// severity to no_clock/bimodal even though every advert RELAY001 itself
// sent is fine.
func TestIssue1816_RelayDoesNotInheritOriginatorSkew(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	pt := 4 // ADVERT
	baseObs := int64(1700000000)

	var relayTxs []*StoreTx

	// RELAY001's own healthy adverts: skew ~0s.
	for i := 0; i < 5; i++ {
		obsTS := baseObs + int64(i)*300
		advTS := obsTS - 2 // 2s behind, trivially healthy
		tx := &StoreTx{
			Hash:        "relay-self-" + formatInt64(int64(i)),
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `},"pubKey":"RELAY001"}`,
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		}
		relayTxs = append(relayTxs, tx)
	}

	// BROKENORIG's adverts: skew ~+100,500s (matches the #1816 band).
	// These are indexed under RELAY001 too (simulating indexResolvedPathHops
	// adding the transmission under the relay-hop pubkey), but their
	// decoded pubKey is BROKENORIG, not RELAY001.
	for i := 0; i < 5; i++ {
		obsTS := baseObs + int64(100+i)*300
		advTS := obsTS + 100500
		tx := &StoreTx{
			Hash:        "relayed-foreign-" + formatInt64(int64(i)),
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `},"pubKey":"BROKENORIG"}`,
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		}
		relayTxs = append(relayTxs, tx)
	}

	ps.mu.Lock()
	ps.byNode["RELAY001"] = relayTxs
	for _, tx := range relayTxs {
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
	}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	r := ps.GetNodeClockSkew("RELAY001")
	if r == nil {
		t.Fatal("expected clock skew result for RELAY001")
	}
	if r.Severity != SkewOK {
		t.Errorf("severity = %v, want ok — relay's own adverts are healthy; "+
			"relayed foreign adverts must not be attributed to it", r.Severity)
	}
	if abs(r.RecentMedianSkewSec) > 5 {
		t.Errorf("recentMedianSkewSec = %v, want ~0 (only RELAY001's own adverts "+
			"should count, not the relayed +100.5k s originator adverts)", r.RecentMedianSkewSec)
	}
	// Sample count must reflect only the 5 self-originated adverts.
	if r.RecentSampleCount != 5 {
		t.Errorf("recentSampleCount = %d, want 5 (relayed adverts must be excluded)", r.RecentSampleCount)
	}
}

// ── #1816: bit-identical clusters across relays disappear ─────────────────────

// Five distinct relay pubkeys never self-advert at all — they only forward
// a single broken-clock originator's advert. Before the fix, each relay's
// GetNodeClockSkew would report the SAME bit-identical skew as the
// originator (a false "cluster"). After the fix, a node with zero
// self-originated adverts must report no clock-skew data at all (nil),
// since it never actually said anything about its own clock.
func TestIssue1816_PureRelaysReportNoSkew_NoBitIdenticalCluster(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	pt := 4 // ADVERT
	baseObs := int64(1700000000)

	const originator = "BROKENORIG2"
	obsTS := baseObs
	advTS := obsTS + 201000 // matches the #1816 report's +201k s band

	originatorTx := &StoreTx{
		Hash:        "originator-advert-1",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `},"pubKey":"` + originator + `"}`,
		Observations: []*StoreObs{
			{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
		},
	}

	relayKeys := []string{"RLY-A", "RLY-B", "RLY-C", "RLY-D", "RLY-E"}

	ps.mu.Lock()
	// The originator's own byNode entry (legitimate — it self-signed this advert).
	ps.byNode[originator] = []*StoreTx{originatorTx}
	// Each relay is ALSO indexed under this same transmission (as
	// indexResolvedPathHops does for every relay hop in the resolved path),
	// but none of them ever self-advert.
	for _, rk := range relayKeys {
		ps.byNode[rk] = []*StoreTx{originatorTx}
	}
	ps.byPayloadType[4] = []*StoreTx{originatorTx}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	// The originator itself should still report its own (bad) skew.
	origResult := ps.GetNodeClockSkew(originator)
	if origResult == nil {
		t.Fatal("expected clock skew result for the originator")
	}
	if abs(origResult.RecentMedianSkewSec-201000) > 5 {
		t.Errorf("originator recentMedianSkewSec = %v, want ~201000", origResult.RecentMedianSkewSec)
	}

	// None of the pure relays ever said anything about their own clock —
	// they must report nil, not a copy of the originator's skew.
	for _, rk := range relayKeys {
		r := ps.GetNodeClockSkew(rk)
		if r != nil {
			t.Errorf("relay %s: expected nil (no self-originated adverts), "+
				"got skew=%v — this is the bit-identical false cluster from #1816",
				rk, r.RecentMedianSkewSec)
		}
	}
}

// ── #1818: two foreign adverts must not poison an otherwise-healthy node ──────

// Reproduces the cwichura "island" report: a node's own adverts are all
// healthy (~0s skew), but two adverts from a broken-clock originator
// (skew ~10 days) are present in its byNode entry — exactly the volume
// described in the issue ("Only 2 adverts out of all the adverts seen are
// from other nodes"). Because getNodeClockSkewLocked sorts by time and
// takes only the last recentSkewWindowCount samples, even 2 catastrophic
// foreign samples can dominate the tail of a small window. After the fix,
// they must be excluded entirely since they aren't self-originated.
func TestIssue1818_TwoForeignAdvertsDoNotPoisonIslandNode(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	pt := 4 // ADVERT
	baseObs := int64(1700000000)

	const island = "ISLAND001"
	var txs []*StoreTx

	// Plenty of healthy self-adverts (island node mostly talks to itself).
	for i := 0; i < 8; i++ {
		obsTS := baseObs + int64(i)*300
		advTS := obsTS - 1 // ~0s skew
		tx := &StoreTx{
			Hash:        "island-self-" + formatInt64(int64(i)),
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `},"pubKey":"` + island + `"}`,
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		}
		txs = append(txs, tx)
	}

	// Two foreign adverts from a broken-clock originator, landing at the
	// tail of the recent window (most recent timestamps), skew ~10 days —
	// matching the report's "off by nearly ten days".
	for i := 0; i < 2; i++ {
		obsTS := baseObs + int64(100+i)*300
		advTS := obsTS + 10*86400
		tx := &StoreTx{
			Hash:        "island-foreign-" + formatInt64(int64(i)),
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `},"pubKey":"OFFCLOCKORIG"}`,
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		}
		txs = append(txs, tx)
	}

	ps.mu.Lock()
	ps.byNode[island] = txs
	for _, tx := range txs {
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
	}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	r := ps.GetNodeClockSkew(island)
	if r == nil {
		t.Fatal("expected clock skew result for island node")
	}
	if r.Severity == SkewNoClock || r.Severity == SkewBimodalClock {
		t.Errorf("severity = %v, want ok/warning — the node's own adverts are "+
			"healthy; the two foreign adverts must not be attributed to it (#1818)",
			r.Severity)
	}
	if abs(r.RecentMedianSkewSec) > 5 {
		t.Errorf("recentMedianSkewSec = %v, want ~0 (foreign adverts excluded)", r.RecentMedianSkewSec)
	}
	// recentSkewWindowCount caps the window at 5 most-recent samples; with
	// the 2 foreign adverts excluded, those 5 must all come from the
	// island node's own healthy stream.
	if r.RecentSampleCount != recentSkewWindowCount {
		t.Errorf("recentSampleCount = %d, want %d (only self-originated adverts, "+
			"windowed)", r.RecentSampleCount, recentSkewWindowCount)
	}
}
