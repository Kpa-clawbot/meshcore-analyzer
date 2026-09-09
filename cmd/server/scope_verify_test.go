package main

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

// realTransportFloodPacket is transmission 0a065d41d51f1f77 from the live
// instance, captured 2026-09-07. Header 0x14 = route_type 0 (TRANSPORT_FLOOD),
// payload_type 5 (GRP_TXT); transport codes 9209/0000; path byte 0x41 =
// hash_size 2, one hop "E3D3"; the rest is payload.
//
// A hand-built fixture would only prove the parser agrees with itself. This
// packet is the one that started the investigation: its code1 is exactly the
// code #fm-112 derives over its own payload, which is why the audit showed
// fm-112 as "not observed" for a repeater that was forwarding it.
const realTransportFloodPacket = "149209000041E3D3EC2D4481DA70893CD71B763958B064A9AAC011D54223FF8A0140CBB4093653BC61D67C960E3ECCE6639CC9FF1147AA6D0F9017"

func TestScopeHMACInputsParsesRealPacket(t *testing.T) {
	payloadType, payload, code1, ok := scopeHMACInputs(realTransportFloodPacket)
	if !ok {
		t.Fatal("scopeHMACInputs returned ok=false for a valid transport-flood packet")
	}
	if payloadType != 5 {
		t.Errorf("payloadType = %d, want 5 (GRP_TXT)", payloadType)
	}
	if code1 != "9209" {
		t.Errorf("code1 = %q, want %q", code1, "9209")
	}
	if len(payload) != 51 {
		t.Errorf("len(payload) = %d, want 51", len(payload))
	}
	if got := strings.ToUpper(hex.EncodeToString(payload[:4])); got != "EC2D4481" {
		t.Errorf("payload starts %q, want %q — offset walked wrong", got, "EC2D4481")
	}
}

func TestScopeHMACInputsRejectsNonTransportRoutes(t *testing.T) {
	// A plain FLOOD packet carries no transport codes, so it has no code1 to
	// verify against. Returning ok=false rather than a zero code1 keeps the
	// caller from HMACing packets that can never match anything.
	//
	// Header 0x15 = route_type 1 (FLOOD), payload_type 5. No transport codes,
	// so the path byte follows the header directly.
	_, _, _, ok := scopeHMACInputs("15" + "41" + "E3D3" + "AABBCC")
	if ok {
		t.Error("ok = true for a non-transport route, want false — there is no code1 to verify")
	}
}

func TestScopeHMACInputsRejectsMalformed(t *testing.T) {
	for _, c := range []struct{ hex, why string }{
		{"", "empty"},
		{"zz", "not hex"},
		{"14", "header only, no transport codes"},
		{"1492090000", "transport codes but no path byte"},
		{"149209000041", "path byte claims one 2-byte hop, none present"},
		// pathByte 0xC0: upper two bits 11 -> hash_size 4, which firmware
		// reserves and isValidPathLen rejects even at hash_count 0
		// (cmd/server/decoder.go, mirroring Packet.cpp:13-18).
		{"1492090000C0" + strings.Repeat("00", 8), "hash_size 4 is reserved"},
	} {
		if _, _, _, ok := scopeHMACInputs(c.hex); ok {
			t.Errorf("ok = true for %q (%s), want false", c.hex, c.why)
		}
	}
}

func TestRegionCodeMatchesTheRealPacket(t *testing.T) {
	// The end-to-end arithmetic, against a packet whose true region is known.
	payloadType, payload, code1, ok := scopeHMACInputs(realTransportFloodPacket)
	if !ok {
		t.Fatal("setup: scopeHMACInputs failed")
	}
	if got := regionCode("fm-112", payloadType, payload); got != code1 {
		t.Errorf("regionCode(fm-112) = %q, want %q — this packet IS fm-112", got, code1)
	}
	// Both spellings must agree: the key is SHA256 over "#name", and callers
	// hand us names with the '#' already stripped by normScope.
	if got := regionCode("#fm-112", payloadType, payload); got != code1 {
		t.Errorf("regionCode(#fm-112) = %q, want %q — leading '#' must be optional", got, code1)
	}
	// A region the repeater also declares, which this packet is NOT.
	if got := regionCode("behss", payloadType, payload); got == code1 {
		t.Errorf("regionCode(behss) = %q, must not equal fm-112's code1", got)
	}
}

func TestRegionCodeIsCaseSensitive(t *testing.T) {
	// The key is SHA256 over the raw bytes of "#name", so "#BEHSS" and
	// "#behss" are different regions. Folding case here would silently name
	// traffic for a region nobody configured.
	payloadType, payload, _, _ := scopeHMACInputs(realTransportFloodPacket)
	if regionCode("behss", payloadType, payload) == regionCode("BEHSS", payloadType, payload) {
		t.Error("regionCode folded case — the key is a hash over raw bytes and must not")
	}
}

func TestUnmatchedTransmissionsInWindow(t *testing.T) {
	s := newScopeTestStore(t)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	old := "2020-01-01T00:00:00Z"
	seedTransmissionRouteAt(t, s, "E3D3", scopeUnmatched(), RouteFlood, recent)
	seedTransmissionRouteAt(t, s, "E3D3", scopeMatched("#be"), RouteFlood, recent)
	seedTransmissionRouteAt(t, s, "E3D3", scopeUnscoped(), RouteFlood, recent)
	seedTransmissionRouteAt(t, s, "E3D3", scopeUnmatched(), RouteFlood, old)

	since := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	got, _, err := s.unmatchedTransmissionsInWindow(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 — only the recent scope_name='' row qualifies", len(got))
	}
	// scopeUnmatched() seeds raw_hex 'AA', which scopeHMACInputs rejects. The
	// query's job is selection; unparseable rows are dropped by the caller, so
	// they must still be returned here rather than filtered in SQL.
	if got[0].txID == 0 {
		t.Error("txID = 0, want the transmission's real id")
	}
}

// buildVerifierFromPackets is a test helper: wraps raw hex strings as rows the
// verifier consumes, with ids 1..N in order.
func buildVerifierFromPackets(t *testing.T, hexes ...string) *scopeVerifier {
	t.Helper()
	rows := make([]unmatchedTransmissionRow, 0, len(hexes))
	for i, h := range hexes {
		rows = append(rows, unmatchedTransmissionRow{txID: int64(i + 1), rawHex: h})
	}
	return newScopeVerifier(rows)
}

func TestScopeVerifierNeedsTwoCorroboratingPackets(t *testing.T) {
	// One match is 1-in-65536 and must not be enough; a second makes it
	// (1/65536)^2. This threshold is the reason the approach is sound.
	v := buildVerifierFromPackets(t, realTransportFloodPacket)
	one := v.evidence([]int64{1}, []string{"fm-112"})
	if one["fm-112"] != 1 {
		t.Fatalf("evidence = %v, want fm-112:1", one)
	}
	if got := v.verified(one); len(got) != 0 {
		t.Errorf("verified = %v, want none - one corroborating packet is not evidence", got)
	}

	// The same packet twice under different ids: two distinct transmissions
	// both deriving to fm-112.
	v2 := buildVerifierFromPackets(t, realTransportFloodPacket, realTransportFloodPacket)
	two := v2.evidence([]int64{1, 2}, []string{"fm-112"})
	if two["fm-112"] != 2 {
		t.Fatalf("evidence = %v, want fm-112:2", two)
	}
	got := v2.verified(two)
	if len(got) != 1 || got[0] != "fm-112" {
		t.Errorf("verified = %v, want [fm-112]", got)
	}
}

func TestScopeVerifierIgnoresRegionsThatDoNotMatch(t *testing.T) {
	v := buildVerifierFromPackets(t, realTransportFloodPacket, realTransportFloodPacket)
	got := v.evidence([]int64{1, 2}, []string{"behss", "be", "eu"})
	if len(got) != 0 {
		t.Errorf("evidence = %v, want empty - none of these regions is this packet", got)
	}
}

func TestScopeVerifierSkipsUnparseablePackets(t *testing.T) {
	// A row whose raw_hex cannot be walked contributes nothing and must not
	// error the pass: one malformed row in the window would otherwise blank
	// the verification for every repeater.
	v := buildVerifierFromPackets(t, "AA", realTransportFloodPacket)
	got := v.evidence([]int64{1, 2}, []string{"fm-112"})
	if got["fm-112"] != 1 {
		t.Errorf("evidence = %v, want fm-112:1 - the malformed row is skipped, the good one still counts", got)
	}
}

func TestScopeVerifierCachesAcrossTargets(t *testing.T) {
	// The cost argument: work depends on (region, transmission), not on which
	// target asked. Two targets declaring the same region over the same packets
	// must not double the HMACs.
	v := buildVerifierFromPackets(t, realTransportFloodPacket, realTransportFloodPacket)
	v.evidence([]int64{1, 2}, []string{"fm-112"})
	after := v.hmacCount
	v.evidence([]int64{1, 2}, []string{"fm-112"})
	if v.hmacCount != after {
		t.Errorf("hmacCount %d -> %d on a repeat query, want unchanged - the cache is what keeps this inside rule 0", after, v.hmacCount)
	}
}

func TestScopeVerifierUnknownTxIDIsHarmless(t *testing.T) {
	// A target's unmatchedTxIDs come from a different query than the verifier's
	// rows. They are taken in the same window, but a row pruned between the two
	// must degrade to "no evidence", not panic.
	v := buildVerifierFromPackets(t, realTransportFloodPacket)
	got := v.evidence([]int64{1, 999}, []string{"fm-112"})
	if got["fm-112"] != 1 {
		t.Errorf("evidence = %v, want fm-112:1 - the unknown id contributes nothing", got)
	}
}

// BenchmarkScopeVerifierAudit models a full audit refresh: every declared name
// against every unmatched packet, once, through the memo. The naive shape would
// be targets x names x packets; this asserts the memo keeps it at names x
// packets, which is what makes the feature affordable (AGENTS.md rule 0).
func BenchmarkScopeVerifierAudit(b *testing.B) {
	const packets, names, targets = 400, 124, 205
	rows := make([]unmatchedTransmissionRow, 0, packets)
	txIDs := make([]int64, 0, packets)
	for i := 0; i < packets; i++ {
		rows = append(rows, unmatchedTransmissionRow{txID: int64(i + 1), rawHex: realTransportFloodPacket})
		txIDs = append(txIDs, int64(i+1))
	}
	declared := make([]string, 0, names)
	for i := 0; i < names; i++ {
		declared = append(declared, fmt.Sprintf("r%04d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v := newScopeVerifier(rows)
		for t := 0; t < targets; t++ {
			v.evidence(txIDs, declared)
		}
	}
}

// TestUnmatchedTransmissionsInWindowIsBounded pins the LIMIT and what it keeps.
//
// The "~400 packets in a 7d window" this feature was sized against holds only
// for an instance whose hashRegions covers most of what its repeaters forward.
// The ingestor writes scope_name = ” for EVERY transport-scoped packet it
// cannot name, so a stock instance with no hashRegions at all has every scoped
// packet in this result set, and the verifier's work is (distinct declared
// names x rows). Unbounded on the axis that grows fastest is exactly what
// AGENTS.md rule 0 forbids.
//
// Newest-first is not arbitrary: a partial answer built from the most recent
// traffic matches what the window claims to describe, and a repeater still
// forwarding a region is far likelier to have done so recently.
func TestUnmatchedTransmissionsInWindowIsBounded(t *testing.T) {
	s := newScopeTestStore(t)
	base := time.Now().UTC().Add(-2 * time.Hour)
	total := scopeVerifyMaxWindowPackets + 25
	for i := 0; i < total; i++ {
		at := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		seedTransmissionRouteAt(t, s, "E3D3", scopeUnmatched(), RouteFlood, at)
	}

	since := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	got, truncated, err := s.unmatchedTransmissionsInWindow(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != scopeVerifyMaxWindowPackets {
		t.Fatalf("got %d rows, want the cap of %d", len(got), scopeVerifyMaxWindowPackets)
	}
	if !truncated {
		t.Error("truncated = false, want true — the caller has to be able to say the evidence is partial")
	}

	// The 25 oldest rows are the ones that must have been dropped: seeded ids
	// ascend with first_seen, so every kept id must be above that boundary.
	for _, r := range got {
		if r.txID <= 25 {
			t.Fatalf("kept txID %d, want only the %d newest rows", r.txID, scopeVerifyMaxWindowPackets)
		}
	}
}

// TestUnmatchedTransmissionsInWindowReportsNoTruncationUnderCap: the flag has to
// distinguish "this is everything" from "this is a sample", or the caller
// cannot tell an exact answer from a partial one.
func TestUnmatchedTransmissionsInWindowReportsNoTruncationUnderCap(t *testing.T) {
	s := newScopeTestStore(t)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, s, "E3D3", scopeUnmatched(), RouteFlood, recent)

	since := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	got, truncated, err := s.unmatchedTransmissionsInWindow(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || truncated {
		t.Fatalf("got %d rows truncated=%v, want 1 and false", len(got), truncated)
	}
}

// TestCapVerifyRegions bounds the other axis. A repeater's declared list
// arrives over MQTT from a companion app: handleClientRegions validates the
// target and each entry, but nothing limits how MANY entries a client may
// report, and every distinct name costs one pass over every unmatched packet.
// The longest genuine list on this network declares 21 regions.
func TestCapVerifyRegions(t *testing.T) {
	short := []string{"be", "nl", "eu"}
	if got := capVerifyRegions(short); len(got) != 3 {
		t.Errorf("len = %d, want 3 — a normal list must pass through untouched", len(got))
	}
	long := make([]string, scopeVerifyMaxRegionsPerTarget+10)
	for i := range long {
		long[i] = fmt.Sprintf("r%03d", i)
	}
	got := capVerifyRegions(long)
	if len(got) != scopeVerifyMaxRegionsPerTarget {
		t.Fatalf("len = %d, want the cap of %d", len(got), scopeVerifyMaxRegionsPerTarget)
	}
	if got[0] != long[0] {
		t.Errorf("got[0] = %q, want %q — the cap keeps the first entries, it does not reorder", got[0], long[0])
	}
}

// BenchmarkScopeVerifierStress runs the shape the cap allows, not the shape
// today's network produces. BenchmarkScopeVerifierAudit models 400 packets;
// this one models a full sample, which is what an instance with few configured
// region keys actually hands the verifier.
func BenchmarkScopeVerifierStress(b *testing.B) {
	const names, targets = 124, 205
	packets := scopeVerifyMaxWindowPackets
	rows := make([]unmatchedTransmissionRow, 0, packets)
	txIDs := make([]int64, 0, packets)
	for i := 0; i < packets; i++ {
		rows = append(rows, unmatchedTransmissionRow{txID: int64(i + 1), rawHex: realTransportFloodPacket})
		txIDs = append(txIDs, int64(i+1))
	}
	declared := make([]string, 0, names)
	for i := 0; i < names; i++ {
		declared = append(declared, fmt.Sprintf("r%04d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v := newScopeVerifier(rows)
		for t := 0; t < targets; t++ {
			v.evidence(txIDs, declared)
		}
	}
}
