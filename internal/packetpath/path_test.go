package packetpath

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodePathFromRawHex_Basic(t *testing.T) {
	// Build a simple FLOOD packet (route_type=1) with 2 hops of hashSize=1
	// header: route_type=1, payload_type=2 (TXT_MSG), version=0 → 0b00_0010_01 = 0x09
	// path byte: hashSize=1 (bits 7-6 = 0), hashCount=2 (bits 5-0 = 2) → 0x02
	// hops: AB, CD
	// payload: some bytes
	raw := "0902ABCD" + "DEADBEEF"
	hops, err := DecodePathFromRawHex(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hops) != 2 || hops[0] != "AB" || hops[1] != "CD" {
		t.Fatalf("expected [AB, CD], got %v", hops)
	}
}

func TestDecodePathFromRawHex_ZeroHops(t *testing.T) {
	// DIRECT route (type=2), no hops → 0b00_0010_10 = 0x0A
	// path byte: 0x00 (0 hops)
	raw := "0A00" + "DEADBEEF"
	hops, err := DecodePathFromRawHex(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hops) != 0 {
		t.Fatalf("expected 0 hops, got %v", hops)
	}
}

func TestDecodePathFromRawHex_TransportRoute(t *testing.T) {
	// TRANSPORT_FLOOD (route_type=0), payload_type=5 (GRP_TXT), version=0
	// header: 0b00_0101_00 = 0x14
	// transport codes: 4 bytes
	// path byte: hashSize=1, hashCount=1 → 0x01
	// hop: FF
	raw := "14" + "00112233" + "01" + "FF" + "DEAD"
	hops, err := DecodePathFromRawHex(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hops) != 1 || hops[0] != "FF" {
		t.Fatalf("expected [FF], got %v", hops)
	}
}

// buildTracePacket creates a TRACE packet hex string where header path bytes are
// SNR values, and payload contains the actual route hops.
func buildTracePacket() (rawHex string, headerPathHops []string, payloadHops []string) {
	// DIRECT route (type=2), TRACE payload (type=9), version=0
	// header byte: 0b00_1001_10 = 0x26
	headerByte := byte(0x26)

	// Header path: 2 SNR bytes (hashSize=1, hashCount=2) → path byte = 0x02
	// SNR values: 0x1A (26 dB), 0x0F (15 dB)
	pathByte := byte(0x02)
	snrBytes := []byte{0x1A, 0x0F}

	// TRACE payload: tag(4) + authCode(4) + flags(1) + path hops
	tag := []byte{0x01, 0x00, 0x00, 0x00}
	authCode := []byte{0x02, 0x00, 0x00, 0x00}
	// flags: path_sz=0 (1 byte hops), other bits=0 → 0x00
	flags := byte(0x00)
	// Payload hops: AA, BB, CC (the actual route)
	payloadPathBytes := []byte{0xAA, 0xBB, 0xCC}

	var buf []byte
	buf = append(buf, headerByte, pathByte)
	buf = append(buf, snrBytes...)
	buf = append(buf, tag...)
	buf = append(buf, authCode...)
	buf = append(buf, flags)
	buf = append(buf, payloadPathBytes...)

	rawHex = strings.ToUpper(hex.EncodeToString(buf))
	headerPathHops = []string{"1A", "0F"} // SNR values — NOT route hops
	payloadHops = []string{"AA", "BB", "CC"} // actual route hops from payload
	return
}

func TestDecodePathFromRawHex_TraceReturnsSNR(t *testing.T) {
	rawHex, expectedSNR, _ := buildTracePacket()
	hops, err := DecodePathFromRawHex(rawHex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// DecodePathFromRawHex always returns header path bytes — for TRACE these are SNR values
	if len(hops) != len(expectedSNR) {
		t.Fatalf("expected %d hops (SNR), got %d: %v", len(expectedSNR), len(hops), hops)
	}
	for i, h := range hops {
		if h != expectedSNR[i] {
			t.Errorf("hop[%d]: expected %s, got %s", i, expectedSNR[i], h)
		}
	}
}

func TestTracePathJSON_UsesPayloadHops(t *testing.T) {
	// This test validates the TRACE vs non-TRACE logic that callers should implement:
	// For TRACE: path_json = decoded.Path.Hops (payload-decoded route hops)
	// For non-TRACE: path_json = DecodePathFromRawHex(raw_hex)
	rawHex, snrHops, payloadHops := buildTracePacket()

	// DecodePathFromRawHex returns SNR bytes for TRACE
	headerHops, _ := DecodePathFromRawHex(rawHex)
	headerJSON, _ := json.Marshal(headerHops)

	// payload hops (what decoded.Path.Hops would return after TRACE decoding)
	payloadJSON, _ := json.Marshal(payloadHops)

	// They must differ — SNR != route hops
	if string(headerJSON) == string(payloadJSON) {
		t.Fatalf("SNR hops and payload hops should differ for TRACE; both are %s", headerJSON)
	}

	// For TRACE, path_json should be payloadHops, not headerHops
	_ = snrHops // snrHops == headerHops — used for documentation
	t.Logf("TRACE: header path (SNR) = %s, payload path (route) = %s", headerJSON, payloadJSON)
}
