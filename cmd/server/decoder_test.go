package main

import (
	"strings"
	"testing"
)

func TestDecodeHeader_TransportFlood(t *testing.T) {
	// Route type 0 = TRANSPORT_FLOOD, payload type 5 = GRP_TXT, version 0
	// Header byte: (0 << 6) | (5 << 2) | 0 = 0x14
	h := decodeHeader(0x14)
	if h.RouteType != RouteTransportFlood {
		t.Errorf("expected RouteTransportFlood (0), got %d", h.RouteType)
	}
	if h.RouteTypeName != "TRANSPORT_FLOOD" {
		t.Errorf("expected TRANSPORT_FLOOD, got %s", h.RouteTypeName)
	}
	if h.PayloadType != PayloadGRP_TXT {
		t.Errorf("expected PayloadGRP_TXT (5), got %d", h.PayloadType)
	}
}

func TestDecodeHeader_TransportDirect(t *testing.T) {
	// Route type 3 = TRANSPORT_DIRECT, payload type 2 = TXT_MSG, version 0
	// Header byte: (0 << 6) | (2 << 2) | 3 = 0x0B
	h := decodeHeader(0x0B)
	if h.RouteType != RouteTransportDirect {
		t.Errorf("expected RouteTransportDirect (3), got %d", h.RouteType)
	}
	if h.RouteTypeName != "TRANSPORT_DIRECT" {
		t.Errorf("expected TRANSPORT_DIRECT, got %s", h.RouteTypeName)
	}
}

func TestDecodeHeader_Flood(t *testing.T) {
	// Route type 1 = FLOOD, payload type 4 = ADVERT
	// Header byte: (0 << 6) | (4 << 2) | 1 = 0x11
	h := decodeHeader(0x11)
	if h.RouteType != RouteFlood {
		t.Errorf("expected RouteFlood (1), got %d", h.RouteType)
	}
	if h.RouteTypeName != "FLOOD" {
		t.Errorf("expected FLOOD, got %s", h.RouteTypeName)
	}
}

func TestIsTransportRoute(t *testing.T) {
	if !isTransportRoute(RouteTransportFlood) {
		t.Error("expected RouteTransportFlood to be transport")
	}
	if !isTransportRoute(RouteTransportDirect) {
		t.Error("expected RouteTransportDirect to be transport")
	}
	if isTransportRoute(RouteFlood) {
		t.Error("expected RouteFlood to NOT be transport")
	}
	if isTransportRoute(RouteDirect) {
		t.Error("expected RouteDirect to NOT be transport")
	}
}

func TestDecodePacket_TransportFloodHasCodes(t *testing.T) {
	// Build a minimal TRANSPORT_FLOOD packet:
	// Header 0x14 (route=0/T_FLOOD, payload=5/GRP_TXT)
	// Transport codes: AABB CCDD (4 bytes)
	// Path byte: 0x00 (hashSize=1, hashCount=0)
	// Payload: at least some bytes for GRP_TXT
	hex := "14AABBCCDD00112233445566778899"
	pkt, err := DecodePacket(hex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkt.TransportCodes == nil {
		t.Fatal("expected transport codes to be present")
	}
	if pkt.TransportCodes.Code1 != "AABB" {
		t.Errorf("expected Code1=AABB, got %s", pkt.TransportCodes.Code1)
	}
	if pkt.TransportCodes.Code2 != "CCDD" {
		t.Errorf("expected Code2=CCDD, got %s", pkt.TransportCodes.Code2)
	}
}

func TestDecodePacket_FloodHasNoCodes(t *testing.T) {
	// Header 0x11 (route=1/FLOOD, payload=4/ADVERT)
	// Path byte: 0x00 (no hops)
	// Some payload bytes
	hex := "110011223344556677889900AABBCCDD"
	pkt, err := DecodePacket(hex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkt.TransportCodes != nil {
		t.Error("expected no transport codes for FLOOD route")
	}
}

func TestBuildBreakdown_InvalidHex(t *testing.T) {
	b := BuildBreakdown("not-hex!")
	if len(b.Ranges) != 0 {
		t.Errorf("expected empty ranges for invalid hex, got %d", len(b.Ranges))
	}
}

func TestBuildBreakdown_TooShort(t *testing.T) {
	b := BuildBreakdown("11") // 1 byte — no path byte
	if len(b.Ranges) != 0 {
		t.Errorf("expected empty ranges for too-short packet, got %d", len(b.Ranges))
	}
}

func TestBuildBreakdown_FloodNonAdvert(t *testing.T) {
	// Header 0x15: route=1/FLOOD, payload=5/GRP_TXT
	// PathByte 0x01: 1 hop, 1-byte hash
	// PathHop: AA
	// Payload: FF0011
	b := BuildBreakdown("1501AAFFFF00")
	labels := rangeLabels(b.Ranges)
	expect := []string{"Header", "Path Length", "Path", "Payload"}
	if !equalLabels(labels, expect) {
		t.Errorf("expected labels %v, got %v", expect, labels)
	}
	// Verify byte positions
	assertRange(t, b.Ranges, "Header", 0, 0)
	assertRange(t, b.Ranges, "Path Length", 1, 1)
	assertRange(t, b.Ranges, "Path", 2, 2)
	assertRange(t, b.Ranges, "Payload", 3, 5)
}

func TestBuildBreakdown_TransportFlood(t *testing.T) {
	// Header 0x14: route=0/TRANSPORT_FLOOD, payload=5/GRP_TXT
	// TransportCodes: AABBCCDD (4 bytes)
	// PathByte 0x01: 1 hop, 1-byte hash
	// PathHop: EE
	// Payload: FF00
	b := BuildBreakdown("14AABBCCDD01EEFF00")
	assertRange(t, b.Ranges, "Header", 0, 0)
	assertRange(t, b.Ranges, "Transport Codes", 1, 4)
	assertRange(t, b.Ranges, "Path Length", 5, 5)
	assertRange(t, b.Ranges, "Path", 6, 6)
	assertRange(t, b.Ranges, "Payload", 7, 8)
}

func TestBuildBreakdown_FloodNoHops(t *testing.T) {
	// Header 0x15: FLOOD/GRP_TXT; PathByte 0x00: 0 hops; Payload: AABB
	b := BuildBreakdown("150000AABB")
	assertRange(t, b.Ranges, "Header", 0, 0)
	assertRange(t, b.Ranges, "Path Length", 1, 1)
	// No Path range since hashCount=0
	for _, r := range b.Ranges {
		if r.Label == "Path" {
			t.Error("expected no Path range for zero-hop packet")
		}
	}
	assertRange(t, b.Ranges, "Payload", 2, 4)
}

func TestBuildBreakdown_AdvertBasic(t *testing.T) {
	// Header 0x11: FLOOD/ADVERT
	// PathByte 0x01: 1 hop, 1-byte hash
	// PathHop: AA
	// Payload: 100 bytes (PubKey32 + Timestamp4 + Signature64) + Flags=0x02 (repeater, no extras)
	pubkey := strings.Repeat("AB", 32)
	ts := "00000000" // 4 bytes
	sig := strings.Repeat("CD", 64)
	flags := "02"
	hex := "1101AA" + pubkey + ts + sig + flags
	b := BuildBreakdown(hex)
	assertRange(t, b.Ranges, "Header", 0, 0)
	assertRange(t, b.Ranges, "Path Length", 1, 1)
	assertRange(t, b.Ranges, "Path", 2, 2)
	assertRange(t, b.Ranges, "PubKey", 3, 34)
	assertRange(t, b.Ranges, "Timestamp", 35, 38)
	assertRange(t, b.Ranges, "Signature", 39, 102)
	assertRange(t, b.Ranges, "Flags", 103, 103)
}

func TestBuildBreakdown_AdvertWithLocation(t *testing.T) {
	// flags=0x12: hasLocation bit set
	pubkey := strings.Repeat("00", 32)
	ts := "00000000"
	sig := strings.Repeat("00", 64)
	flags := "12" // 0x10 = hasLocation
	latBytes := "00000000"
	lonBytes := "00000000"
	hex := "1101AA" + pubkey + ts + sig + flags + latBytes + lonBytes
	b := BuildBreakdown(hex)
	assertRange(t, b.Ranges, "Latitude", 104, 107)
	assertRange(t, b.Ranges, "Longitude", 108, 111)
}

func TestBuildBreakdown_AdvertWithName(t *testing.T) {
	// flags=0x82: hasName bit set
	pubkey := strings.Repeat("00", 32)
	ts := "00000000"
	sig := strings.Repeat("00", 64)
	flags := "82" // 0x80 = hasName
	name := "4E6F6465" // "Node" in hex
	hex := "1101AA" + pubkey + ts + sig + flags + name
	b := BuildBreakdown(hex)
	assertRange(t, b.Ranges, "Name", 104, 107)
}

// helpers

func rangeLabels(ranges []HexRange) []string {
	out := make([]string, len(ranges))
	for i, r := range ranges {
		out[i] = r.Label
	}
	return out
}

func equalLabels(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertRange(t *testing.T, ranges []HexRange, label string, wantStart, wantEnd int) {
	t.Helper()
	for _, r := range ranges {
		if r.Label == label {
			if r.Start != wantStart || r.End != wantEnd {
				t.Errorf("range %q: want [%d,%d], got [%d,%d]", label, wantStart, wantEnd, r.Start, r.End)
			}
			return
		}
	}
	t.Errorf("range %q not found in %v", label, rangeLabels(ranges))
}



// --- BuildBreakdown tests (PR #500 review feedback) ---

func TestBuildBreakdown_SimplePayload(t *testing.T) {
	// Header 0x11 = ADVERT + ZERO_HOP, path byte 0x00 = no hops
	// Payload < 100 bytes → single "Payload" range
	h := "1100" + strings.Repeat("AB", 10)
	bd := BuildBreakdown(h)
	labels := rangeLabels(bd.Ranges)
	expect := []string{"Header", "Path Length", "Payload"}
	if len(labels) != len(expect) {
		t.Fatalf("expected %v, got %v", expect, labels)
	}
	for i, e := range expect {
		if labels[i] != e {
			t.Errorf("range[%d]: expected %s, got %s", i, e, labels[i])
		}
	}
}

func TestBuildBreakdown_TransportDirect(t *testing.T) {
	// TXT_MSG (0x01) + TRANSPORT_DIRECT (route 3) = 0x07
	h := "07" + "AABBCCDD" + "00" + strings.Repeat("EE", 5)
	bd := BuildBreakdown(h)
	labels := rangeLabels(bd.Ranges)
	if len(labels) < 4 {
		t.Fatalf("expected ≥4 ranges, got %v", labels)
	}
	if labels[1] != "Transport Codes" {
		t.Errorf("expected Transport Codes, got %s", labels[1])
	}
	if bd.Ranges[1].Start != 1 || bd.Ranges[1].End != 4 {
		t.Errorf("transport range wrong: %d-%d", bd.Ranges[1].Start, bd.Ranges[1].End)
	}
}

func TestBuildBreakdown_AdvertAllFlags(t *testing.T) {
	// ADVERT + ZERO_HOP = 0x11, path 0x00
	// flags 0xF2 = location(0x10) + feat1(0x20) + feat2(0x40) + name(0x80) + type 2
	pubkey := strings.Repeat("AA", 32)
	ts := "01020304"
	sig := strings.Repeat("BB", 64)
	flags := "F2"
	loc := "0100000002000000"
	feat1 := "C1C2"
	feat2 := "D1D2"
	name := strings.Repeat("48", 5)

	h := "11" + "00" + pubkey + ts + sig + flags + loc + feat1 + feat2 + name
	bd := BuildBreakdown(h)
	labels := rangeLabels(bd.Ranges)

	expect := []string{"Header", "Path Length", "PubKey", "Timestamp", "Signature",
		"Flags", "Latitude", "Longitude", "Feature1", "Feature2", "Name"}
	if len(labels) != len(expect) {
		t.Fatalf("expected %v, got %v", expect, labels)
	}
	for i, e := range expect {
		if labels[i] != e {
			t.Errorf("range[%d]: expected %s, got %s", i, e, labels[i])
		}
	}
	// Verify no overlaps
	for i := 1; i < len(bd.Ranges); i++ {
		if bd.Ranges[i].Start <= bd.Ranges[i-1].End {
			t.Errorf("overlap: %s [%d-%d] and %s [%d-%d]",
				bd.Ranges[i-1].Label, bd.Ranges[i-1].Start, bd.Ranges[i-1].End,
				bd.Ranges[i].Label, bd.Ranges[i].Start, bd.Ranges[i].End)
		}
	}
	// Feature1 & Feature2 are each 2 bytes
	if sz := bd.Ranges[8].End - bd.Ranges[8].Start + 1; sz != 2 {
		t.Errorf("Feature1 should be 2 bytes, got %d", sz)
	}
	if sz := bd.Ranges[9].End - bd.Ranges[9].Start + 1; sz != 2 {
		t.Errorf("Feature2 should be 2 bytes, got %d", sz)
	}
}

func TestBuildBreakdown_AdvertFeat1Only(t *testing.T) {
	// flags 0xA1 = feat1(0x20) + name(0x80) + type 1, no location
	pubkey := strings.Repeat("AA", 32)
	ts := "01020304"
	sig := strings.Repeat("BB", 64)
	h := "11" + "00" + pubkey + ts + sig + "A1" + "F1F2" + strings.Repeat("4E", 4)
	bd := BuildBreakdown(h)
	labels := rangeLabels(bd.Ranges)

	expect := []string{"Header", "Path Length", "PubKey", "Timestamp", "Signature",
		"Flags", "Feature1", "Name"}
	if len(labels) != len(expect) {
		t.Fatalf("expected %v, got %v", expect, labels)
	}
	for i, e := range expect {
		if labels[i] != e {
			t.Errorf("range[%d]: expected %s, got %s", i, e, labels[i])
		}
	}
}

func TestDecodePacket_TransportDirect(t *testing.T) {
	h := "07" + "AABBCCDD" + "00" + strings.Repeat("EE", 5)
	pkt, err := DecodePacket(h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkt.Header.RouteType != RouteTransportDirect {
		t.Errorf("expected route %d, got %d", RouteTransportDirect, pkt.Header.RouteType)
	}
	if pkt.TransportCodes == nil {
		t.Fatal("expected transport codes")
	}
	if pkt.TransportCodes.Code1 != "AABB" {
		t.Errorf("Code1: expected AABB, got %s", pkt.TransportCodes.Code1)
	}
}

// rangeLabels is defined earlier in this file
