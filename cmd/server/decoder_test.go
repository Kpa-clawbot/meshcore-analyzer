package main

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
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
	pkt, err := DecodePacket(hex, false)
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
	pkt, err := DecodePacket(hex, false)
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
	pubkey := repeatHex("AB", 32)
	ts := "00000000" // 4 bytes
	sig := repeatHex("CD", 64)
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
	pubkey := repeatHex("00", 32)
	ts := "00000000"
	sig := repeatHex("00", 64)
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
	pubkey := repeatHex("00", 32)
	ts := "00000000"
	sig := repeatHex("00", 64)
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

func TestZeroHopDirectHashSize(t *testing.T) {
	// DIRECT (RouteType=2) + REQ (PayloadType=0) → header byte = 0x02
	// pathByte=0x00 → hash_count=0, hash_size bits=0 → should get HashSize=0
	// Need at least a few payload bytes after pathByte.
	hex := "02" + "00" + repeatHex("AA", 20)
	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 0 {
		t.Errorf("DIRECT zero-hop: want HashSize=0, got %d", pkt.Path.HashSize)
	}
}

func TestZeroHopDirectHashSizeWithNonZeroUpperBits(t *testing.T) {
	// DIRECT (RouteType=2) + REQ (PayloadType=0) → header byte = 0x02
	// pathByte=0x40 → hash_count=0, hash_size bits=01 → should still get HashSize=0
	// because hash_count is zero (lower 6 bits are 0).
	hex := "02" + "40" + repeatHex("AA", 20)
	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 0 {
		t.Errorf("DIRECT zero-hop with hash_size bits set: want HashSize=0, got %d", pkt.Path.HashSize)
	}
}

func TestZeroHopTransportDirectHashSize(t *testing.T) {
	// TRANSPORT_DIRECT (RouteType=3) + REQ (PayloadType=0) → header byte = 0x03
	// 4 bytes transport codes + pathByte=0x00 → hash_count=0 → should get HashSize=0
	hex := "03" + "11223344" + "00" + repeatHex("AA", 20)
	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 0 {
		t.Errorf("TRANSPORT_DIRECT zero-hop: want HashSize=0, got %d", pkt.Path.HashSize)
	}
}

func TestZeroHopTransportDirectHashSizeWithNonZeroUpperBits(t *testing.T) {
	// TRANSPORT_DIRECT (RouteType=3) + REQ (PayloadType=0) → header byte = 0x03
	// 4 bytes transport codes + pathByte=0xC0 → hash_count=0, hash_size bits=11 → should still get HashSize=0
	hex := "03" + "11223344" + "C0" + repeatHex("AA", 20)
	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 0 {
		t.Errorf("TRANSPORT_DIRECT zero-hop with hash_size bits set: want HashSize=0, got %d", pkt.Path.HashSize)
	}
}

func TestNonDirectZeroPathByteKeepsHashSize(t *testing.T) {
	// FLOOD (RouteType=1) + REQ (PayloadType=0) → header byte = 0x01
	// pathByte=0x00 → even though hash_count=0, non-DIRECT should keep HashSize=1
	hex := "01" + "00" + repeatHex("AA", 20)
	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 1 {
		t.Errorf("FLOOD zero pathByte: want HashSize=1 (unchanged), got %d", pkt.Path.HashSize)
	}
}

func TestDirectNonZeroHopKeepsHashSize(t *testing.T) {
	// DIRECT (RouteType=2) + REQ (PayloadType=0) → header byte = 0x02
	// pathByte=0x01 → hash_count=1, hash_size=1 → should keep HashSize=1
	// Need 1 hop hash byte after pathByte.
	hex := "02" + "01" + repeatHex("BB", 21)
	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 1 {
		t.Errorf("DIRECT with 1 hop: want HashSize=1, got %d", pkt.Path.HashSize)
	}
}

func repeatHex(byteHex string, n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += byteHex
	}
	return s
}

func TestDecodePacket_TraceHopsCompleted(t *testing.T) {
	// Build a TRACE packet:
	// header: route=FLOOD(1), payload=TRACE(9), version=0 → (0<<6)|(9<<2)|1 = 0x25
	// path_length: hash_size bits=0b00 (1-byte), hash_count=2 (2 SNR bytes) → 0x02
	// path: 2 SNR bytes: 0xAA, 0xBB
	// payload: tag(4 LE) + authCode(4 LE) + flags(1) + 4 hop hashes (1 byte each)
	hex := "2502AABB" + // header + path_length + 2 SNR bytes
		"01000000" + // tag = 1
		"02000000" + // authCode = 2
		"00" + // flags = 0
		"DEADBEEF" // 4 hops (1-byte hash each)

	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket error: %v", err)
	}
	if pkt.Payload.Type != "TRACE" {
		t.Fatalf("expected TRACE, got %s", pkt.Payload.Type)
	}
	// Full intended route = 4 hops from payload
	if len(pkt.Path.Hops) != 4 {
		t.Errorf("expected 4 hops, got %d: %v", len(pkt.Path.Hops), pkt.Path.Hops)
	}
	// HopsCompleted = 2 (from header path SNR count)
	if pkt.Path.HopsCompleted == nil {
		t.Fatal("expected HopsCompleted to be set")
	}
	if *pkt.Path.HopsCompleted != 2 {
		t.Errorf("expected HopsCompleted=2, got %d", *pkt.Path.HopsCompleted)
	}
	// FLOOD routing for TRACE is anomalous
	if pkt.Anomaly == "" {
		t.Error("expected anomaly flag for FLOOD-routed TRACE")
	}
}

func TestDecodePacket_TraceNoSNR(t *testing.T) {
	// TRACE with 0 SNR bytes (trace hasn't been forwarded yet)
	// path_length: hash_size=0b00 (1-byte), hash_count=0 → 0x00
	hex := "2500" + // header + path_length (0 hops in header)
		"01000000" + // tag
		"02000000" + // authCode
		"00" + // flags
		"AABBCC" // 3 hops intended

	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket error: %v", err)
	}
	if pkt.Path.HopsCompleted == nil {
		t.Fatal("expected HopsCompleted to be set")
	}
	if *pkt.Path.HopsCompleted != 0 {
		t.Errorf("expected HopsCompleted=0, got %d", *pkt.Path.HopsCompleted)
	}
	if len(pkt.Path.Hops) != 3 {
		t.Errorf("expected 3 hops, got %d", len(pkt.Path.Hops))
	}
}

func TestDecodePacket_TraceFullyCompleted(t *testing.T) {
	// TRACE where all hops completed (SNR count = hop count)
	// path_length: hash_size=0b00 (1-byte), hash_count=3 → 0x03
	hex := "2503AABBCC" + // header + path_length + 3 SNR bytes
		"01000000" + // tag
		"02000000" + // authCode
		"00" + // flags
		"DDEEFF" // 3 hops intended

	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket error: %v", err)
	}
	if pkt.Path.HopsCompleted == nil {
		t.Fatal("expected HopsCompleted to be set")
	}
	if *pkt.Path.HopsCompleted != 3 {
		t.Errorf("expected HopsCompleted=3, got %d", *pkt.Path.HopsCompleted)
	}
	if len(pkt.Path.Hops) != 3 {
		t.Errorf("expected 3 hops, got %d", len(pkt.Path.Hops))
	}
}

func TestDecodePacket_TraceFlags1_TwoBytePathSz(t *testing.T) {
	// TRACE with flags=1 → path_sz = 1 << (1 & 0x03) = 2-byte hashes
	// Firmware always sends TRACE as DIRECT (route_type=2), so header byte =
	// (0<<6)|(9<<2)|2 = 0x26. path_length 0x00 = 0 SNR bytes.
	hex := "2600" + // header (DIRECT+TRACE) + path_length (0 SNR)
		"01000000" + // tag
		"02000000" + // authCode
		"01" + // flags = 1 → path_sz = 2
		"AABBCCDD" // 4 bytes = 2 hops of 2-byte each

	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket error: %v", err)
	}
	if len(pkt.Path.Hops) != 2 {
		t.Errorf("expected 2 hops (2-byte path_sz), got %d: %v", len(pkt.Path.Hops), pkt.Path.Hops)
	}
	if pkt.Path.HashSize != 2 {
		t.Errorf("expected HashSize=2, got %d", pkt.Path.HashSize)
	}
	if pkt.Anomaly != "" {
		t.Errorf("expected no anomaly for DIRECT TRACE, got %q", pkt.Anomaly)
	}
}

func TestDecodePacket_TraceFlags2_FourBytePathSz(t *testing.T) {
	// TRACE with flags=2 → path_sz = 1 << (2 & 0x03) = 4-byte hashes
	// DIRECT route_type (0x26)
	hex := "2600" + // header (DIRECT+TRACE) + path_length (0 SNR)
		"01000000" + // tag
		"02000000" + // authCode
		"02" + // flags = 2 → path_sz = 4
		"AABBCCDD11223344" // 8 bytes = 2 hops of 4-byte each

	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket error: %v", err)
	}
	if len(pkt.Path.Hops) != 2 {
		t.Errorf("expected 2 hops (4-byte path_sz), got %d: %v", len(pkt.Path.Hops), pkt.Path.Hops)
	}
	if pkt.Path.HashSize != 4 {
		t.Errorf("expected HashSize=4, got %d", pkt.Path.HashSize)
	}
}

func TestDecodePacket_TracePathSzUnevenPayload(t *testing.T) {
	// TRACE with flags=1 → path_sz=2, but 5 bytes of path data (not evenly divisible)
	// Should produce 2 hops (4 bytes) and ignore the trailing byte
	hex := "2600" + // header (DIRECT+TRACE) + path_length (0 SNR)
		"01000000" + // tag
		"02000000" + // authCode
		"01" + // flags = 1 → path_sz = 2
		"AABBCCDDEE" // 5 bytes → 2 hops, 1 byte remainder ignored

	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket error: %v", err)
	}
	if len(pkt.Path.Hops) != 2 {
		t.Errorf("expected 2 hops (trailing byte ignored), got %d: %v", len(pkt.Path.Hops), pkt.Path.Hops)
	}
}

func TestDecodePacket_TraceTransportDirect(t *testing.T) {
	// TRACE via TRANSPORT_DIRECT (route_type=3) — includes 4 transport code bytes
	// header: (0<<6)|(9<<2)|3 = 0x27
	hex := "27" + // header (TRANSPORT_DIRECT+TRACE)
		"AABB" + "CCDD" + // transport codes (2+2 bytes)
		"02" + // path_length: hash_count=2 SNR bytes
		"EEFF" + // 2 SNR bytes
		"01000000" + // tag
		"02000000" + // authCode
		"00" + // flags = 0 → path_sz = 1
		"112233" // 3 hops (1-byte each)

	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("DecodePacket error: %v", err)
	}
	if pkt.TransportCodes == nil {
		t.Fatal("expected transport codes for TRANSPORT_DIRECT")
	}
	if pkt.TransportCodes.Code1 != "AABB" {
		t.Errorf("expected Code1=AABB, got %s", pkt.TransportCodes.Code1)
	}
	if len(pkt.Path.Hops) != 3 {
		t.Errorf("expected 3 hops, got %d: %v", len(pkt.Path.Hops), pkt.Path.Hops)
	}
	if pkt.Path.HopsCompleted == nil || *pkt.Path.HopsCompleted != 2 {
		t.Errorf("expected HopsCompleted=2, got %v", pkt.Path.HopsCompleted)
	}
	if pkt.Anomaly != "" {
		t.Errorf("expected no anomaly for TRANSPORT_DIRECT TRACE, got %q", pkt.Anomaly)
	}
}

func TestDecodePacket_TraceFloodRouteAnomaly(t *testing.T) {
	// TRACE via FLOOD (route_type=1) — anomalous per firmware (firmware only
	// sends TRACE as DIRECT). Should still parse but flag the anomaly.
	hex := "2500" + // header (FLOOD+TRACE) + path_length (0 SNR)
		"01000000" + // tag
		"02000000" + // authCode
		"01" + // flags = 1 → path_sz = 2
		"AABBCCDD" // 4 bytes = 2 hops of 2-byte each

	pkt, err := DecodePacket(hex, false)
	if err != nil {
		t.Fatalf("should not crash on anomalous FLOOD+TRACE: %v", err)
	}
	if len(pkt.Path.Hops) != 2 {
		t.Errorf("expected 2 hops even for anomalous FLOOD route, got %d", len(pkt.Path.Hops))
	}
	if pkt.Anomaly == "" {
		t.Error("expected anomaly flag for FLOOD-routed TRACE, got empty string")
	}
}

func TestDecodeAdvertSignatureValidation(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	var timestamp uint32 = 1234567890
	appdata := []byte{0x02} // flags: repeater, no extras

	// Build signed message: pubKey(32) + timestamp(4 LE) + appdata
	msg := make([]byte, 32+4+len(appdata))
	copy(msg[0:32], pub)
	binary.LittleEndian.PutUint32(msg[32:36], timestamp)
	copy(msg[36:], appdata)
	sig := ed25519.Sign(priv, msg)

	// Build a raw advert buffer: pubKey(32) + timestamp(4) + signature(64) + appdata
	buf := make([]byte, 100+len(appdata))
	copy(buf[0:32], pub)
	binary.LittleEndian.PutUint32(buf[32:36], timestamp)
	copy(buf[36:100], sig)
	copy(buf[100:], appdata)

	// With validation enabled
	p := decodeAdvert(buf, true)
	if p.SignatureValid == nil {
		t.Fatal("expected SignatureValid to be set")
	}
	if !*p.SignatureValid {
		t.Error("expected valid signature")
	}
	if p.PubKey != hex.EncodeToString(pub) {
		t.Errorf("pubkey mismatch: got %s", p.PubKey)
	}

	// Tamper with signature → invalid
	buf[40] ^= 0xFF
	p = decodeAdvert(buf, true)
	if p.SignatureValid == nil {
		t.Fatal("expected SignatureValid to be set")
	}
	if *p.SignatureValid {
		t.Error("expected invalid signature after tampering")
	}

	// Without validation → SignatureValid should be nil
	p = decodeAdvert(buf, false)
	if p.SignatureValid != nil {
		t.Error("expected SignatureValid to be nil when validation disabled")
	}
}
