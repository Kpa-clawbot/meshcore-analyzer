package main

import (
	"crypto/aes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/sigvalidate"
)

func TestDecodeHeaderRoutTypes(t *testing.T) {
	tests := []struct {
		b    byte
		rt   int
		name string
	}{
		{0x00, 0, "TRANSPORT_FLOOD"},
		{0x01, 1, "FLOOD"},
		{0x02, 2, "DIRECT"},
		{0x03, 3, "TRANSPORT_DIRECT"},
	}
	for _, tt := range tests {
		h := decodeHeader(tt.b)
		if h.RouteType != tt.rt {
			t.Errorf("header 0x%02X: routeType=%d, want %d", tt.b, h.RouteType, tt.rt)
		}
		if h.RouteTypeName != tt.name {
			t.Errorf("header 0x%02X: routeTypeName=%s, want %s", tt.b, h.RouteTypeName, tt.name)
		}
	}
}

func TestDecodeHeaderPayloadTypes(t *testing.T) {
	// 0x11 = 0b00_0100_01 → routeType=1(FLOOD), payloadType=4(ADVERT), version=0
	h := decodeHeader(0x11)
	if h.RouteType != 1 {
		t.Errorf("0x11: routeType=%d, want 1", h.RouteType)
	}
	if h.PayloadType != 4 {
		t.Errorf("0x11: payloadType=%d, want 4", h.PayloadType)
	}
	if h.PayloadVersion != 0 {
		t.Errorf("0x11: payloadVersion=%d, want 0", h.PayloadVersion)
	}
	if h.RouteTypeName != "FLOOD" {
		t.Errorf("0x11: routeTypeName=%s, want FLOOD", h.RouteTypeName)
	}
	if h.PayloadTypeName != "ADVERT" {
		t.Errorf("0x11: payloadTypeName=%s, want ADVERT", h.PayloadTypeName)
	}
}

func TestDecodePathZeroHops(t *testing.T) {
	// 0x00: 0 hops, 1-byte hashes
	pkt, err := DecodePacket("0500"+strings.Repeat("00", 10), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Path.HashCount != 0 {
		t.Errorf("hashCount=%d, want 0", pkt.Path.HashCount)
	}
	if pkt.Path.HashSize != 1 {
		t.Errorf("hashSize=%d, want 1", pkt.Path.HashSize)
	}
	if len(pkt.Path.Hops) != 0 {
		t.Errorf("hops=%d, want 0", len(pkt.Path.Hops))
	}
}

func TestDecodePath1ByteHashes(t *testing.T) {
	// 0x05: 5 hops, 1-byte hashes → 5 path bytes
	pkt, err := DecodePacket("0505"+"AABBCCDDEE"+strings.Repeat("00", 10), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Path.HashCount != 5 {
		t.Errorf("hashCount=%d, want 5", pkt.Path.HashCount)
	}
	if pkt.Path.HashSize != 1 {
		t.Errorf("hashSize=%d, want 1", pkt.Path.HashSize)
	}
	if len(pkt.Path.Hops) != 5 {
		t.Fatalf("hops=%d, want 5", len(pkt.Path.Hops))
	}
	if pkt.Path.Hops[0] != "AA" {
		t.Errorf("hop[0]=%s, want AA", pkt.Path.Hops[0])
	}
	if pkt.Path.Hops[4] != "EE" {
		t.Errorf("hop[4]=%s, want EE", pkt.Path.Hops[4])
	}
}

func TestDecodePath2ByteHashes(t *testing.T) {
	// 0x45: 5 hops, 2-byte hashes
	pkt, err := DecodePacket("0545"+"AA11BB22CC33DD44EE55"+strings.Repeat("00", 10), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Path.HashCount != 5 {
		t.Errorf("hashCount=%d, want 5", pkt.Path.HashCount)
	}
	if pkt.Path.HashSize != 2 {
		t.Errorf("hashSize=%d, want 2", pkt.Path.HashSize)
	}
	if pkt.Path.Hops[0] != "AA11" {
		t.Errorf("hop[0]=%s, want AA11", pkt.Path.Hops[0])
	}
}

func TestDecodePath3ByteHashes(t *testing.T) {
	// 0x8A: 10 hops, 3-byte hashes
	pkt, err := DecodePacket("058A"+strings.Repeat("AA11FF", 10)+strings.Repeat("00", 10), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Path.HashCount != 10 {
		t.Errorf("hashCount=%d, want 10", pkt.Path.HashCount)
	}
	if pkt.Path.HashSize != 3 {
		t.Errorf("hashSize=%d, want 3", pkt.Path.HashSize)
	}
	if len(pkt.Path.Hops) != 10 {
		t.Errorf("hops=%d, want 10", len(pkt.Path.Hops))
	}
}

func TestTransportCodes(t *testing.T) {
	// Route type 0 (TRANSPORT_FLOOD) should have transport codes
	// Firmware order: header + transport_codes(4) + path_len + path + payload
	hex := "14" + "AABB" + "CCDD" + "00" + strings.Repeat("00", 10)
	pkt, err := DecodePacket(hex, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Header.RouteType != 0 {
		t.Errorf("routeType=%d, want 0", pkt.Header.RouteType)
	}
	if pkt.TransportCodes == nil {
		t.Fatal("transportCodes should not be nil for TRANSPORT_FLOOD")
	}
	if pkt.TransportCodes.Code1 != "AABB" {
		t.Errorf("code1=%s, want AABB", pkt.TransportCodes.Code1)
	}
	if pkt.TransportCodes.Code2 != "CCDD" {
		t.Errorf("code2=%s, want CCDD", pkt.TransportCodes.Code2)
	}

	// Route type 1 (FLOOD) should NOT have transport codes
	pkt2, err := DecodePacket("0500"+strings.Repeat("00", 10), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt2.TransportCodes != nil {
		t.Error("FLOOD should not have transport codes")
	}
}

func TestDecodeAdvertFull(t *testing.T) {
	pubkey := strings.Repeat("AA", 32)
	timestamp := "78563412" // 0x12345678 LE
	signature := strings.Repeat("BB", 64)
	// flags: 0x92 = repeater(2) | hasLocation(0x10) | hasName(0x80)
	flags := "92"
	lat := "40933402" // ~37.0
	lon := "E0E6B8F8" // ~-122.1
	name := "546573744E6F6465" // "TestNode"

	hex := "1200" + pubkey + timestamp + signature + flags + lat + lon + name
	pkt, err := DecodePacket(hex, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	if pkt.Payload.Type != "ADVERT" {
		t.Errorf("type=%s, want ADVERT", pkt.Payload.Type)
	}
	if pkt.Payload.PubKey != strings.ToLower(pubkey) {
		t.Errorf("pubkey mismatch")
	}
	if pkt.Payload.Timestamp != 0x12345678 {
		t.Errorf("timestamp=%d, want %d", pkt.Payload.Timestamp, 0x12345678)
	}

	if pkt.Payload.Flags == nil {
		t.Fatal("flags should not be nil")
	}
	if pkt.Payload.Flags.Raw != 0x92 {
		t.Errorf("flags.raw=%d, want 0x92", pkt.Payload.Flags.Raw)
	}
	if pkt.Payload.Flags.Type != 2 {
		t.Errorf("flags.type=%d, want 2", pkt.Payload.Flags.Type)
	}
	if !pkt.Payload.Flags.Repeater {
		t.Error("flags.repeater should be true")
	}
	if pkt.Payload.Flags.Room {
		t.Error("flags.room should be false")
	}
	if !pkt.Payload.Flags.HasLocation {
		t.Error("flags.hasLocation should be true")
	}
	if !pkt.Payload.Flags.HasName {
		t.Error("flags.hasName should be true")
	}

	if pkt.Payload.Lat == nil {
		t.Fatal("lat should not be nil")
	}
	if math.Abs(*pkt.Payload.Lat-37.0) > 0.001 {
		t.Errorf("lat=%f, want ~37.0", *pkt.Payload.Lat)
	}
	if pkt.Payload.Lon == nil {
		t.Fatal("lon should not be nil")
	}
	if math.Abs(*pkt.Payload.Lon-(-122.1)) > 0.001 {
		t.Errorf("lon=%f, want ~-122.1", *pkt.Payload.Lon)
	}
	if pkt.Payload.Name != "TestNode" {
		t.Errorf("name=%s, want TestNode", pkt.Payload.Name)
	}
}

func TestDecodeAdvertTypeEnums(t *testing.T) {
	makeAdvert := func(flagsByte byte) *DecodedPacket {
		hex := "1200" + strings.Repeat("AA", 32) + "00000000" + strings.Repeat("BB", 64) +
			strings.ToUpper(string([]byte{hexDigit(flagsByte>>4), hexDigit(flagsByte & 0x0f)}))
		pkt, err := DecodePacket(hex, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		return pkt
	}

	// type 1 = chat/companion
	p1 := makeAdvert(0x01)
	if p1.Payload.Flags.Type != 1 {
		t.Errorf("type 1: flags.type=%d", p1.Payload.Flags.Type)
	}
	if !p1.Payload.Flags.Chat {
		t.Error("type 1: chat should be true")
	}

	// type 2 = repeater
	p2 := makeAdvert(0x02)
	if !p2.Payload.Flags.Repeater {
		t.Error("type 2: repeater should be true")
	}

	// type 3 = room
	p3 := makeAdvert(0x03)
	if !p3.Payload.Flags.Room {
		t.Error("type 3: room should be true")
	}

	// type 4 = sensor
	p4 := makeAdvert(0x04)
	if !p4.Payload.Flags.Sensor {
		t.Error("type 4: sensor should be true")
	}
}

func hexDigit(v byte) byte {
	v = v & 0x0f
	if v < 10 {
		return '0' + v
	}
	return 'a' + v - 10
}

func TestDecodeAdvertNoLocationNoName(t *testing.T) {
	hex := "1200" + strings.Repeat("CC", 32) + "00000000" + strings.Repeat("DD", 64) + "02"
	pkt, err := DecodePacket(hex, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Payload.Flags.HasLocation {
		t.Error("hasLocation should be false")
	}
	if pkt.Payload.Flags.HasName {
		t.Error("hasName should be false")
	}
	if pkt.Payload.Lat != nil {
		t.Error("lat should be nil")
	}
	if pkt.Payload.Name != "" {
		t.Errorf("name should be empty, got %s", pkt.Payload.Name)
	}
}

func TestGoldenFixtureTxtMsg(t *testing.T) {
	pkt, err := DecodePacket("0A00D69FD7A5A7475DB07337749AE61FA53A4788E976", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Header.PayloadType != PayloadTXT_MSG {
		t.Errorf("payloadType=%d, want %d", pkt.Header.PayloadType, PayloadTXT_MSG)
	}
	if pkt.Header.RouteType != RouteDirect {
		t.Errorf("routeType=%d, want %d", pkt.Header.RouteType, RouteDirect)
	}
	if pkt.Path.HashCount != 0 {
		t.Errorf("hashCount=%d, want 0", pkt.Path.HashCount)
	}
	if pkt.Payload.DestHash != "d6" {
		t.Errorf("destHash=%s, want d6", pkt.Payload.DestHash)
	}
	if pkt.Payload.SrcHash != "9f" {
		t.Errorf("srcHash=%s, want 9f", pkt.Payload.SrcHash)
	}
}

func TestGoldenFixtureAdvert(t *testing.T) {
	rawHex := "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"
	pkt, err := DecodePacket(rawHex, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Payload.Type != "ADVERT" {
		t.Errorf("type=%s, want ADVERT", pkt.Payload.Type)
	}
	if pkt.Payload.PubKey != "46d62de27d4c5194d7821fc5a34a45565dcc2537b300b9ab6275255cefb65d84" {
		t.Errorf("pubKey mismatch: %s", pkt.Payload.PubKey)
	}
	if pkt.Payload.Flags == nil || !pkt.Payload.Flags.Repeater {
		t.Error("should be repeater")
	}
	if math.Abs(*pkt.Payload.Lat-37.0) > 0.001 {
		t.Errorf("lat=%f, want ~37.0", *pkt.Payload.Lat)
	}
	if pkt.Payload.Name != "MRR2-R" {
		t.Errorf("name=%s, want MRR2-R", pkt.Payload.Name)
	}
}

func TestGoldenFixtureUnicodeAdvert(t *testing.T) {
	rawHex := "120073CFF971E1CB5754A742C152B2D2E0EB108A19B246D663ED8898A72C4A5AD86EA6768E66694B025EDF6939D5C44CFF719C5D5520E5F06B20680A83AD9C2C61C3227BBB977A85EE462F3553445FECF8EDD05C234ECE217272E503F14D6DF2B1B9B133890C923CDF3002F8FDC1F85045414BF09F8CB3"
	pkt, err := DecodePacket(rawHex, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Payload.Type != "ADVERT" {
		t.Errorf("type=%s, want ADVERT", pkt.Payload.Type)
	}
	if !pkt.Payload.Flags.Repeater {
		t.Error("should be repeater")
	}
	// Name contains emoji: PEAK🌳
	if !strings.HasPrefix(pkt.Payload.Name, "PEAK") {
		t.Errorf("name=%s, expected to start with PEAK", pkt.Payload.Name)
	}
}

func TestDecodePacketTooShort(t *testing.T) {
	_, err := DecodePacket("FF", nil, false)
	if err == nil {
		t.Error("expected error for 1-byte packet")
	}
}

func TestDecodePacketInvalidHex(t *testing.T) {
	_, err := DecodePacket("ZZZZ", nil, false)
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestComputeContentHash(t *testing.T) {
	hash := ComputeContentHash("0A00D69FD7A5A7475DB07337749AE61FA53A4788E976")
	if len(hash) != 16 {
		t.Errorf("hash length=%d, want 16", len(hash))
	}
	// Same content with different path should produce same hash
	// (path bytes are stripped, only header + payload hashed)

	// Verify consistency
	hash2 := ComputeContentHash("0A00D69FD7A5A7475DB07337749AE61FA53A4788E976")
	if hash != hash2 {
		t.Error("content hash not deterministic")
	}
}

func TestValidateAdvert(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)

	// Good advert
	good := &Payload{PubKey: goodPk, Flags: &AdvertFlags{Repeater: true}}
	ok, _ := ValidateAdvert(good)
	if !ok {
		t.Error("good advert should validate")
	}

	// Nil
	ok, _ = ValidateAdvert(nil)
	if ok {
		t.Error("nil should fail")
	}

	// Error payload
	ok, _ = ValidateAdvert(&Payload{Error: "bad"})
	if ok {
		t.Error("error payload should fail")
	}

	// Short pubkey
	ok, _ = ValidateAdvert(&Payload{PubKey: "aa"})
	if ok {
		t.Error("short pubkey should fail")
	}

	// All-zero pubkey
	ok, _ = ValidateAdvert(&Payload{PubKey: strings.Repeat("0", 64)})
	if ok {
		t.Error("all-zero pubkey should fail")
	}

	// Invalid lat
	badLat := 999.0
	ok, _ = ValidateAdvert(&Payload{PubKey: goodPk, Lat: &badLat})
	if ok {
		t.Error("invalid lat should fail")
	}

	// Invalid lon
	badLon := -999.0
	ok, _ = ValidateAdvert(&Payload{PubKey: goodPk, Lon: &badLon})
	if ok {
		t.Error("invalid lon should fail")
	}

	// Control chars in name
	ok, _ = ValidateAdvert(&Payload{PubKey: goodPk, Name: "test\x00name"})
	if ok {
		t.Error("control chars in name should fail")
	}

	// Name too long
	ok, _ = ValidateAdvert(&Payload{PubKey: goodPk, Name: strings.Repeat("x", 65)})
	if ok {
		t.Error("long name should fail")
	}
}

func TestDecodeGrpTxtShort(t *testing.T) {
	p := decodeGrpTxt([]byte{0x01, 0x02}, nil)
	if p.Error != "too short" {
		t.Errorf("expected 'too short' error, got %q", p.Error)
	}
	if p.Type != "GRP_TXT" {
		t.Errorf("type=%s, want GRP_TXT", p.Type)
	}
}

func TestDecodeGrpTxtValid(t *testing.T) {
	p := decodeGrpTxt([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE}, nil)
	if p.Error != "" {
		t.Errorf("unexpected error: %s", p.Error)
	}
	if p.ChannelHash != 0xAA {
		t.Errorf("channelHash=%d, want 0xAA", p.ChannelHash)
	}
	if p.MAC != "bbcc" {
		t.Errorf("mac=%s, want bbcc", p.MAC)
	}
	if p.EncryptedData != "ddee" {
		t.Errorf("encryptedData=%s, want ddee", p.EncryptedData)
	}
}

func TestDecodeAnonReqShort(t *testing.T) {
	p := decodeAnonReq(make([]byte, 10))
	if p.Error != "too short" {
		t.Errorf("expected 'too short' error, got %q", p.Error)
	}
	if p.Type != "ANON_REQ" {
		t.Errorf("type=%s, want ANON_REQ", p.Type)
	}
}

func TestDecodeAnonReqValid(t *testing.T) {
	buf := make([]byte, 40)
	buf[0] = 0xFF // destHash
	for i := 1; i < 33; i++ {
		buf[i] = byte(i)
	}
	buf[33] = 0xAA
	buf[34] = 0xBB
	p := decodeAnonReq(buf)
	if p.Error != "" {
		t.Errorf("unexpected error: %s", p.Error)
	}
	if p.DestHash != "ff" {
		t.Errorf("destHash=%s, want ff", p.DestHash)
	}
	if p.MAC != "aabb" {
		t.Errorf("mac=%s, want aabb", p.MAC)
	}
}

func TestDecodePathPayloadShort(t *testing.T) {
	p := decodePathPayload([]byte{0x01, 0x02, 0x03})
	if p.Error != "too short" {
		t.Errorf("expected 'too short' error, got %q", p.Error)
	}
	if p.Type != "PATH" {
		t.Errorf("type=%s, want PATH", p.Type)
	}
}

func TestDecodePathPayloadValid(t *testing.T) {
	buf := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	p := decodePathPayload(buf)
	if p.Error != "" {
		t.Errorf("unexpected error: %s", p.Error)
	}
	if p.DestHash != "aa" {
		t.Errorf("destHash=%s, want aa", p.DestHash)
	}
	if p.SrcHash != "bb" {
		t.Errorf("srcHash=%s, want bb", p.SrcHash)
	}
	if p.PathData != "eeff" {
		t.Errorf("pathData=%s, want eeff", p.PathData)
	}
}

func TestDecodeTraceShort(t *testing.T) {
	p := decodeTrace(make([]byte, 5))
	if p.Error != "too short" {
		t.Errorf("expected 'too short' error, got %q", p.Error)
	}
	if p.Type != "TRACE" {
		t.Errorf("type=%s, want TRACE", p.Type)
	}
}

func TestDecodeTraceValid(t *testing.T) {
	buf := make([]byte, 16)
	// tag(4) + authCode(4) + flags(1) + pathData
	binary.LittleEndian.PutUint32(buf[0:4], 1)          // tag = 1
	binary.LittleEndian.PutUint32(buf[4:8], 0xDEADBEEF) // authCode
	buf[8] = 0x02                                         // flags
	buf[9] = 0xAA                                         // path data
	p := decodeTrace(buf)
	if p.Error != "" {
		t.Errorf("unexpected error: %s", p.Error)
	}
	if p.Tag != 1 {
		t.Errorf("tag=%d, want 1", p.Tag)
	}
	if p.AuthCode != 0xDEADBEEF {
		t.Errorf("authCode=%d, want 0xDEADBEEF", p.AuthCode)
	}
	if p.TraceFlags == nil || *p.TraceFlags != 2 {
		t.Errorf("traceFlags=%v, want 2", p.TraceFlags)
	}
	if p.Type != "TRACE" {
		t.Errorf("type=%s, want TRACE", p.Type)
	}
	if p.PathData == "" {
		t.Error("pathData should not be empty")
	}
}

func TestDecodeTracePathParsing(t *testing.T) {
	// Packet from issue #276: 260001807dca00000000007d547d
	// Path byte 0x00 → hashSize=1, hops in payload at buf[9:] = 7d 54 7d
	// Expected path: ["7D", "54", "7D"]
	pkt, err := DecodePacket("260001807dca00000000007d547d", nil, false)
	if err != nil {
		t.Fatalf("DecodePacket error: %v", err)
	}
	if pkt.Payload.Type != "TRACE" {
		t.Errorf("payload type=%s, want TRACE", pkt.Payload.Type)
	}
	want := []string{"7D", "54", "7D"}
	if len(pkt.Path.Hops) != len(want) {
		t.Fatalf("hops=%v, want %v", pkt.Path.Hops, want)
	}
	for i, h := range want {
		if pkt.Path.Hops[i] != h {
			t.Errorf("hops[%d]=%s, want %s", i, pkt.Path.Hops[i], h)
		}
	}
	if pkt.Path.HashCount != 3 {
		t.Errorf("hashCount=%d, want 3", pkt.Path.HashCount)
	}
}

func TestDecodeAdvertShort(t *testing.T) {
	p := decodeAdvert(make([]byte, 50), false)
	if p.Error != "too short for advert" {
		t.Errorf("expected 'too short for advert' error, got %q", p.Error)
	}
}

func TestDecodeEncryptedPayloadShort(t *testing.T) {
	p := decodeEncryptedPayload("REQ", []byte{0x01, 0x02})
	if p.Error != "too short" {
		t.Errorf("expected 'too short' error, got %q", p.Error)
	}
	if p.Type != "REQ" {
		t.Errorf("type=%s, want REQ", p.Type)
	}
}

func TestDecodeEncryptedPayloadValid(t *testing.T) {
	buf := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	p := decodeEncryptedPayload("RESPONSE", buf)
	if p.Error != "" {
		t.Errorf("unexpected error: %s", p.Error)
	}
	if p.DestHash != "aa" {
		t.Errorf("destHash=%s, want aa", p.DestHash)
	}
	if p.SrcHash != "bb" {
		t.Errorf("srcHash=%s, want bb", p.SrcHash)
	}
	if p.MAC != "ccdd" {
		t.Errorf("mac=%s, want ccdd", p.MAC)
	}
	if p.EncryptedData != "eeff" {
		t.Errorf("encryptedData=%s, want eeff", p.EncryptedData)
	}
}

func TestDecodePayloadGRPData(t *testing.T) {
	buf := []byte{0x01, 0x02, 0x03}
	p := decodePayload(PayloadGRP_DATA, buf, nil, false)
	if p.Type != "UNKNOWN" {
		t.Errorf("type=%s, want UNKNOWN", p.Type)
	}
	if p.RawHex != "010203" {
		t.Errorf("rawHex=%s, want 010203", p.RawHex)
	}
}

func TestDecodePayloadRAWCustom(t *testing.T) {
	buf := []byte{0xFF, 0xFE}
	p := decodePayload(PayloadRAW_CUSTOM, buf, nil, false)
	if p.Type != "UNKNOWN" {
		t.Errorf("type=%s, want UNKNOWN", p.Type)
	}
}

func TestDecodePayloadAllTypes(t *testing.T) {
	// REQ
	p := decodePayload(PayloadREQ, make([]byte, 10), nil, false)
	if p.Type != "REQ" {
		t.Errorf("REQ: type=%s", p.Type)
	}

	// RESPONSE
	p = decodePayload(PayloadRESPONSE, make([]byte, 10), nil, false)
	if p.Type != "RESPONSE" {
		t.Errorf("RESPONSE: type=%s", p.Type)
	}

	// TXT_MSG
	p = decodePayload(PayloadTXT_MSG, make([]byte, 10), nil, false)
	if p.Type != "TXT_MSG" {
		t.Errorf("TXT_MSG: type=%s", p.Type)
	}

	// ACK
	p = decodePayload(PayloadACK, make([]byte, 10), nil, false)
	if p.Type != "ACK" {
		t.Errorf("ACK: type=%s", p.Type)
	}

	// GRP_TXT
	p = decodePayload(PayloadGRP_TXT, make([]byte, 10), nil, false)
	if p.Type != "GRP_TXT" {
		t.Errorf("GRP_TXT: type=%s", p.Type)
	}

	// ANON_REQ
	p = decodePayload(PayloadANON_REQ, make([]byte, 40), nil, false)
	if p.Type != "ANON_REQ" {
		t.Errorf("ANON_REQ: type=%s", p.Type)
	}

	// PATH
	p = decodePayload(PayloadPATH, make([]byte, 10), nil, false)
	if p.Type != "PATH" {
		t.Errorf("PATH: type=%s", p.Type)
	}

	// TRACE
	p = decodePayload(PayloadTRACE, make([]byte, 20), nil, false)
	if p.Type != "TRACE" {
		t.Errorf("TRACE: type=%s", p.Type)
	}
}

func TestPayloadJSON(t *testing.T) {
	p := &Payload{Type: "TEST", Name: "hello"}
	j := PayloadJSON(p)
	if j == "" || j == "{}" {
		t.Errorf("PayloadJSON returned empty: %s", j)
	}
	if !strings.Contains(j, `"type":"TEST"`) {
		t.Errorf("PayloadJSON missing type: %s", j)
	}
	if !strings.Contains(j, `"name":"hello"`) {
		t.Errorf("PayloadJSON missing name: %s", j)
	}
}

func TestPayloadJSONNil(t *testing.T) {
	// nil should not panic
	j := PayloadJSON(nil)
	if j != "null" && j != "{}" {
		// json.Marshal(nil) returns "null"
		t.Logf("PayloadJSON(nil) = %s", j)
	}
}

func TestValidateAdvertNaNLat(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	nanVal := math.NaN()
	ok, reason := ValidateAdvert(&Payload{PubKey: goodPk, Lat: &nanVal})
	if ok {
		t.Error("NaN lat should fail")
	}
	if !strings.Contains(reason, "lat") {
		t.Errorf("reason should mention lat: %s", reason)
	}
}

func TestValidateAdvertInfLon(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	infVal := math.Inf(1)
	ok, reason := ValidateAdvert(&Payload{PubKey: goodPk, Lon: &infVal})
	if ok {
		t.Error("Inf lon should fail")
	}
	if !strings.Contains(reason, "lon") {
		t.Errorf("reason should mention lon: %s", reason)
	}
}

func TestValidateAdvertNegInfLat(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	negInf := math.Inf(-1)
	ok, _ := ValidateAdvert(&Payload{PubKey: goodPk, Lat: &negInf})
	if ok {
		t.Error("-Inf lat should fail")
	}
}

func TestValidateAdvertNaNLon(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	nan := math.NaN()
	ok, _ := ValidateAdvert(&Payload{PubKey: goodPk, Lon: &nan})
	if ok {
		t.Error("NaN lon should fail")
	}
}

func TestValidateAdvertControlChars(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	tests := []struct {
		name string
		char string
	}{
		{"null", "\x00"},
		{"bell", "\x07"},
		{"backspace", "\x08"},
		{"vtab", "\x0b"},
		{"formfeed", "\x0c"},
		{"shift out", "\x0e"},
		{"unit sep", "\x1f"},
		{"delete", "\x7f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, _ := ValidateAdvert(&Payload{PubKey: goodPk, Name: "test" + tt.char + "name"})
			if ok {
				t.Errorf("control char %q in name should fail", tt.char)
			}
		})
	}
}

func TestValidateAdvertAllowedCharsInName(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	// Tab (\t = 0x09), newline (\n = 0x0a), carriage return (\r = 0x0d) are NOT blocked
	ok, reason := ValidateAdvert(&Payload{PubKey: goodPk, Name: "hello\tworld", Flags: &AdvertFlags{Repeater: true}})
	if !ok {
		t.Errorf("tab in name should be allowed, got reason: %s", reason)
	}
}

func TestValidateAdvertUnknownRole(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	// type=0 maps to companion via Chat=false, Repeater=false, Room=false, Sensor=false → companion
	// type=5 (unknown) → companion (default), which IS a valid role
	// But if all booleans are false AND type is 0, advertRole returns "companion" which is valid
	// To get "unknown", we'd need a flags combo that doesn't match any valid role
	// Actually advertRole always returns companion as default — so let's just test the validation path
	flags := &AdvertFlags{Type: 5, Chat: false, Repeater: false, Room: false, Sensor: false}
	ok, reason := ValidateAdvert(&Payload{PubKey: goodPk, Flags: flags})
	// advertRole returns "companion" for this, which is valid
	if !ok {
		t.Errorf("default companion role should be valid, got: %s", reason)
	}
}

func TestValidateAdvertValidLocation(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	lat := 45.0
	lon := -90.0
	ok, _ := ValidateAdvert(&Payload{PubKey: goodPk, Lat: &lat, Lon: &lon, Flags: &AdvertFlags{Repeater: true}})
	if !ok {
		t.Error("valid lat/lon should pass")
	}
}

func TestValidateAdvertBoundaryLat(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	// Exactly at boundary
	lat90 := 90.0
	ok, _ := ValidateAdvert(&Payload{PubKey: goodPk, Lat: &lat90})
	if !ok {
		t.Error("lat=90 should pass")
	}
	latNeg90 := -90.0
	ok, _ = ValidateAdvert(&Payload{PubKey: goodPk, Lat: &latNeg90})
	if !ok {
		t.Error("lat=-90 should pass")
	}
	// Just over
	lat91 := 90.001
	ok, _ = ValidateAdvert(&Payload{PubKey: goodPk, Lat: &lat91})
	if ok {
		t.Error("lat=90.001 should fail")
	}
}

func TestValidateAdvertBoundaryLon(t *testing.T) {
	goodPk := strings.Repeat("aa", 32)
	lon180 := 180.0
	ok, _ := ValidateAdvert(&Payload{PubKey: goodPk, Lon: &lon180})
	if !ok {
		t.Error("lon=180 should pass")
	}
	lonNeg180 := -180.0
	ok, _ = ValidateAdvert(&Payload{PubKey: goodPk, Lon: &lonNeg180})
	if !ok {
		t.Error("lon=-180 should pass")
	}
}

func TestComputeContentHashShortHex(t *testing.T) {
	// Less than 16 hex chars and invalid hex
	hash := ComputeContentHash("AB")
	if hash != "AB" {
		t.Errorf("short hex hash=%s, want AB", hash)
	}

	// Exactly 16 chars invalid hex
	hash = ComputeContentHash("ZZZZZZZZZZZZZZZZ")
	if len(hash) != 16 {
		t.Errorf("invalid hex hash length=%d, want 16", len(hash))
	}
}

func TestComputeContentHashTransportRoute(t *testing.T) {
	// Route type 0 (TRANSPORT_FLOOD) with transport codes then path=0x00 (0 hops)
	// header=0x14 (TRANSPORT_FLOOD, ADVERT), transport(4), path=0x00
	hex := "14" + "AABBCCDD" + "00" + strings.Repeat("EE", 10)
	hash := ComputeContentHash(hex)
	if len(hash) != 16 {
		t.Errorf("hash length=%d, want 16", len(hash))
	}
}

func TestComputeContentHashPayloadBeyondBuffer(t *testing.T) {
	// path claims more bytes than buffer has → fallback
	// header=0x05 (FLOOD, REQ), pathByte=0x3F (63 hops of 1 byte = 63 path bytes)
	// but total buffer is only 4 bytes
	hex := "053F" + "AABB"
	hash := ComputeContentHash(hex)
	// payloadStart = 2 + 63 = 65, but buffer is only 4 bytes
	// Should fallback — rawHex is 8 chars (< 16), so returns rawHex
	if hash != hex {
		t.Errorf("hash=%s, want %s", hash, hex)
	}
}

func TestComputeContentHashPayloadBeyondBufferLongHex(t *testing.T) {
	// Same as above but with rawHex >= 16 chars → returns first 16
	hex := "053F" + strings.Repeat("AA", 20) // 44 chars total, but pathByte claims 63 hops
	hash := ComputeContentHash(hex)
	if len(hash) != 16 {
		t.Errorf("hash length=%d, want 16", len(hash))
	}
	if hash != hex[:16] {
		t.Errorf("hash=%s, want %s", hash, hex[:16])
	}
}

func TestComputeContentHashTransportBeyondBuffer(t *testing.T) {
	// Transport route (0x00 = TRANSPORT_FLOOD) with path claiming some bytes
	// header=0x00, transport(4), pathByte=0x02 (2 hops, 1-byte hash)
	// offset=1+4+1+2=8, buffer needs to be >= 8
	hex := "00" + "AABB" + "CCDD" + "02" + strings.Repeat("CC", 6) // 20 chars = 10 bytes  
	hash := ComputeContentHash(hex)
	if len(hash) != 16 {
		t.Errorf("hash length=%d, want 16", len(hash))
	}
}

func TestComputeContentHashLongFallback(t *testing.T) {
	// Long rawHex (>= 16) but invalid → returns first 16 chars
	longInvalid := "ZZZZZZZZZZZZZZZZZZZZZZZZ"
	hash := ComputeContentHash(longInvalid)
	if hash != longInvalid[:16] {
		t.Errorf("hash=%s, want first 16 of input", hash)
	}
}

// TestComputeContentHashRouteTypeIndependence verifies that the same logical
// packet produces the same content hash regardless of route type (issue #786).
func TestComputeContentHashRouteTypeIndependence(t *testing.T) {
	// Same payload type (TXT_MSG=2, bits 2-5) with different route types.
	// Header 0x08 = route_type 0 (TRANSPORT_FLOOD), payload_type 2
	// Header 0x0A = route_type 2 (DIRECT), payload_type 2
	// Header 0x09 = route_type 1 (FLOOD), payload_type 2
	// pathByte=0x00, payload=D69FD7A5A7
	payloadHex := "D69FD7A5A7"

	// FLOOD: header=0x09 (route_type 1), pathByte=0x00
	floodHex := "09" + "00" + payloadHex
	// DIRECT: header=0x0A (route_type 2), pathByte=0x00
	directHex := "0A" + "00" + payloadHex

	hashFlood := ComputeContentHash(floodHex)
	hashDirect := ComputeContentHash(directHex)
	if hashFlood != hashDirect {
		t.Errorf("same payload with different route types produced different hashes: flood=%s direct=%s", hashFlood, hashDirect)
	}
}

// TestComputeContentHashTraceIncludesPathLen verifies TRACE packets include
// path_len in the hash (matching firmware behavior).
func TestComputeContentHashTraceIncludesPathLen(t *testing.T) {
	// TRACE = payload_type 0x09, so header bits 2-5 = 0x09 → header = 0x09<<2 | route=2 = 0x26
	// pathByte=0x01 (1 hop, 1-byte hash) → 1 path byte
	traceHeader1 := "26" // route=2, payload_type=9
	pathByte1 := "01"
	pathData1 := "AA"
	payload := "DEADBEEF"
	hex1 := traceHeader1 + pathByte1 + pathData1 + payload

	// Same but pathByte=0x02 (2 hops) → 2 path bytes
	pathByte2 := "02"
	pathData2 := "AABB"
	hex2 := traceHeader1 + pathByte2 + pathData2 + payload

	hash1 := ComputeContentHash(hex1)
	hash2 := ComputeContentHash(hex2)
	if hash1 == hash2 {
		t.Error("TRACE packets with different path_len should produce different hashes (path_len is part of hash input)")
	}
}

// TestComputeContentHashMatchesFirmware verifies hash output matches what the
// firmware would compute: SHA256(payload_type_byte + payload)[:16hex].
func TestComputeContentHashMatchesFirmware(t *testing.T) {
	// header=0x0A → payload_type = (0x0A >> 2) & 0x0F = 2
	// pathByte=0x00, payload = D69FD7A5A7475DB07337749AE61FA53A4788E976
	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	hash := ComputeContentHash(rawHex)

	// Manually compute expected: SHA256(0x02 + payload_bytes)
	payloadBytes, _ := hex.DecodeString("D69FD7A5A7475DB07337749AE61FA53A4788E976")
	toHash := append([]byte{0x02}, payloadBytes...)
	expected := sha256.Sum256(toHash)
	expectedHex := hex.EncodeToString(expected[:])[:16]
	if hash != expectedHex {
		t.Errorf("hash=%s, want %s (firmware-compatible)", hash, expectedHex)
	}
}

func TestDecodePacketWithWhitespace(t *testing.T) {
	raw := "0A 00 D6 9F D7 A5 A7 47 5D B0 73 37 74 9A E6 1F A5 3A 47 88 E9 76"
	pkt, err := DecodePacket(raw, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Header.PayloadType != PayloadTXT_MSG {
		t.Errorf("payloadType=%d, want %d", pkt.Header.PayloadType, PayloadTXT_MSG)
	}
}

func TestDecodePacketWithNewlines(t *testing.T) {
	raw := "0A00\nD69F\r\nD7A5A7475DB07337749AE61FA53A4788E976"
	pkt, err := DecodePacket(raw, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Payload.Type != "TXT_MSG" {
		t.Errorf("type=%s, want TXT_MSG", pkt.Payload.Type)
	}
}

func TestDecodePacketTransportRouteTooShort(t *testing.T) {
	// TRANSPORT_FLOOD (route=0) but only 2 bytes total → too short for transport codes
	_, err := DecodePacket("1400", nil, false)
	if err == nil {
		t.Error("expected error for transport route with too-short buffer")
	}
	if !strings.Contains(err.Error(), "transport codes") {
		t.Errorf("error should mention transport codes: %v", err)
	}
}

func TestDecodeAckShort(t *testing.T) {
	p := decodeAck([]byte{0x01, 0x02, 0x03})
	if p.Error != "too short" {
		t.Errorf("expected 'too short', got %q", p.Error)
	}
}

func TestDecodeAckValid(t *testing.T) {
	buf := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	p := decodeAck(buf)
	if p.Error != "" {
		t.Errorf("unexpected error: %s", p.Error)
	}
	if p.ExtraHash != "ddccbbaa" {
		t.Errorf("extraHash=%s, want ddccbbaa", p.ExtraHash)
	}
	if p.DestHash != "" {
		t.Errorf("destHash should be empty, got %s", p.DestHash)
	}
	if p.SrcHash != "" {
		t.Errorf("srcHash should be empty, got %s", p.SrcHash)
	}
}

func TestIsTransportRoute(t *testing.T) {
	if !isTransportRoute(RouteTransportFlood) {
		t.Error("RouteTransportFlood should be transport")
	}
	if !isTransportRoute(RouteTransportDirect) {
		t.Error("RouteTransportDirect should be transport")
	}
	if isTransportRoute(RouteFlood) {
		t.Error("RouteFlood should not be transport")
	}
	if isTransportRoute(RouteDirect) {
		t.Error("RouteDirect should not be transport")
	}
}

func TestDecodeHeaderUnknownTypes(t *testing.T) {
	// Payload type that doesn't map to any known name
	// bits 5-2 = 0x0C (12) is CONTROL but 0x0D (13) would be unknown
	// byte = 0b00_1101_01 = 0x35 → routeType=1, payloadType=0x0D(13), version=0
	h := decodeHeader(0x35)
	if h.PayloadTypeName != "UNKNOWN" {
		t.Errorf("payloadTypeName=%s, want UNKNOWN for type 13", h.PayloadTypeName)
	}
}

func TestDecodePayloadMultipart(t *testing.T) {
	// MULTIPART (0x0A) falls through to default → UNKNOWN
	p := decodePayload(PayloadMULTIPART, []byte{0x01, 0x02}, nil, false)
	if p.Type != "UNKNOWN" {
		t.Errorf("MULTIPART type=%s, want UNKNOWN", p.Type)
	}
}

func TestDecodePayloadControl(t *testing.T) {
	// CONTROL (0x0B) falls through to default → UNKNOWN
	p := decodePayload(PayloadCONTROL, []byte{0x01, 0x02}, nil, false)
	if p.Type != "UNKNOWN" {
		t.Errorf("CONTROL type=%s, want UNKNOWN", p.Type)
	}
}

func TestDecodePathTruncatedBuffer(t *testing.T) {
	// path byte claims 5 hops of 2 bytes = 10 bytes, but only 4 available
	path, consumed := decodePath(0x45, []byte{0xAA, 0x11, 0xBB, 0x22}, 0)
	if path.HashCount != 5 {
		t.Errorf("hashCount=%d, want 5", path.HashCount)
	}
	// Should only decode 2 hops (4 bytes / 2 bytes per hop)
	if len(path.Hops) != 2 {
		t.Errorf("hops=%d, want 2 (truncated)", len(path.Hops))
	}
	if consumed != 10 {
		t.Errorf("consumed=%d, want 10 (full claimed size)", consumed)
	}
}

func TestDecodeFloodAdvert5Hops(t *testing.T) {
	// From test-decoder.js Test 1
	raw := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	pkt, err := DecodePacket(raw, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Header.RouteTypeName != "FLOOD" {
		t.Errorf("route=%s, want FLOOD", pkt.Header.RouteTypeName)
	}
	if pkt.Header.PayloadTypeName != "ADVERT" {
		t.Errorf("payload=%s, want ADVERT", pkt.Header.PayloadTypeName)
	}
	if pkt.Path.HashSize != 2 {
		t.Errorf("hashSize=%d, want 2", pkt.Path.HashSize)
	}
	if pkt.Path.HashCount != 5 {
		t.Errorf("hashCount=%d, want 5", pkt.Path.HashCount)
	}
	if pkt.Path.Hops[0] != "1000" {
		t.Errorf("hop[0]=%s, want 1000", pkt.Path.Hops[0])
	}
	if pkt.Path.Hops[1] != "D818" {
		t.Errorf("hop[1]=%s, want D818", pkt.Path.Hops[1])
	}
	if pkt.TransportCodes != nil {
		t.Error("FLOOD should have no transport codes")
	}
}

// --- Channel decryption tests ---

// buildTestCiphertext creates a valid AES-128-ECB encrypted GRP_TXT payload
// with a matching HMAC-SHA256 MAC for testing.
func buildTestCiphertext(channelKeyHex, senderMsg string, timestamp uint32) (ciphertextHex, macHex string) {
	channelKey, _ := hex.DecodeString(channelKeyHex)

	// Build plaintext: timestamp(4 LE) + flags(1) + message
	plain := make([]byte, 4+1+len(senderMsg))
	binary.LittleEndian.PutUint32(plain[0:4], timestamp)
	plain[4] = 0x00 // flags
	copy(plain[5:], senderMsg)

	// Pad to AES block boundary
	pad := aes.BlockSize - (len(plain) % aes.BlockSize)
	if pad != aes.BlockSize {
		plain = append(plain, make([]byte, pad)...)
	}

	// AES-128-ECB encrypt
	block, _ := aes.NewCipher(channelKey)
	ct := make([]byte, len(plain))
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Encrypt(ct[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}

	// HMAC-SHA256 MAC (first 2 bytes)
	secret := make([]byte, 32)
	copy(secret, channelKey)
	h := hmac.New(sha256.New, secret)
	h.Write(ct)
	mac := h.Sum(nil)

	return hex.EncodeToString(ct), hex.EncodeToString(mac[:2])
}

func TestDecryptChannelMessageValid(t *testing.T) {
	key := "2cc3d22840e086105ad73443da2cacb8"
	ctHex, macHex := buildTestCiphertext(key, "Alice: Hello world", 1700000000)

	result, err := decryptChannelMessage(ctHex, macHex, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Sender != "Alice" {
		t.Errorf("sender=%q, want Alice", result.Sender)
	}
	if result.Message != "Hello world" {
		t.Errorf("message=%q, want 'Hello world'", result.Message)
	}
	if result.Timestamp != 1700000000 {
		t.Errorf("timestamp=%d, want 1700000000", result.Timestamp)
	}
}

func TestDecryptChannelMessageMACFail(t *testing.T) {
	key := "2cc3d22840e086105ad73443da2cacb8"
	ctHex, _ := buildTestCiphertext(key, "Alice: Hello", 100)
	wrongMac := "ffff"

	_, err := decryptChannelMessage(ctHex, wrongMac, key)
	if err == nil {
		t.Fatal("expected MAC verification failure")
	}
	if !strings.Contains(err.Error(), "MAC") {
		t.Errorf("error should mention MAC: %v", err)
	}
}

func TestDecryptChannelMessageWrongKey(t *testing.T) {
	key := "2cc3d22840e086105ad73443da2cacb8"
	ctHex, macHex := buildTestCiphertext(key, "Alice: Hello", 100)
	wrongKey := "deadbeefdeadbeefdeadbeefdeadbeef"

	_, err := decryptChannelMessage(ctHex, macHex, wrongKey)
	if err == nil {
		t.Fatal("expected error with wrong key")
	}
}

func TestDecryptChannelMessageNoSender(t *testing.T) {
	key := "aaaabbbbccccddddaaaabbbbccccdddd"
	ctHex, macHex := buildTestCiphertext(key, "Just a message", 500)

	result, err := decryptChannelMessage(ctHex, macHex, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Sender != "" {
		t.Errorf("sender=%q, want empty", result.Sender)
	}
	if result.Message != "Just a message" {
		t.Errorf("message=%q, want 'Just a message'", result.Message)
	}
}

func TestDecryptChannelMessageSenderWithBrackets(t *testing.T) {
	key := "aaaabbbbccccddddaaaabbbbccccdddd"
	ctHex, macHex := buildTestCiphertext(key, "[admin]: Not a sender", 500)

	result, err := decryptChannelMessage(ctHex, macHex, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Sender != "" {
		t.Errorf("sender=%q, want empty (brackets disqualify)", result.Sender)
	}
	if result.Message != "[admin]: Not a sender" {
		t.Errorf("message=%q", result.Message)
	}
}

func TestDecryptChannelMessageInvalidKey(t *testing.T) {
	_, err := decryptChannelMessage("aabb", "cc", "ZZZZ")
	if err == nil {
		t.Fatal("expected error for invalid key hex")
	}
}

func TestDecryptChannelMessageShortKey(t *testing.T) {
	_, err := decryptChannelMessage("aabb", "cc", "aabb")
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestDecodeGrpTxtWithDecryption(t *testing.T) {
	key := "2cc3d22840e086105ad73443da2cacb8"
	ctHex, macHex := buildTestCiphertext(key, "Bob: Testing 123", 1700000000)
	macBytes, _ := hex.DecodeString(macHex)
	ctBytes, _ := hex.DecodeString(ctHex)

	// Build GRP_TXT payload: channelHash(1) + MAC(2) + encrypted
	buf := []byte{0xAA}
	buf = append(buf, macBytes...)
	buf = append(buf, ctBytes...)

	keys := map[string]string{"#test": key}
	p := decodeGrpTxt(buf, keys)

	if p.Type != "CHAN" {
		t.Errorf("type=%s, want CHAN", p.Type)
	}
	if p.DecryptionStatus != "decrypted" {
		t.Errorf("decryptionStatus=%s, want decrypted", p.DecryptionStatus)
	}
	if p.Channel != "#test" {
		t.Errorf("channel=%s, want #test", p.Channel)
	}
	if p.Sender != "Bob" {
		t.Errorf("sender=%q, want Bob", p.Sender)
	}
	if p.Text != "Bob: Testing 123" {
		t.Errorf("text=%q, want 'Bob: Testing 123'", p.Text)
	}
	if p.ChannelHash != 0xAA {
		t.Errorf("channelHash=%d, want 0xAA", p.ChannelHash)
	}
	if p.ChannelHashHex != "AA" {
		t.Errorf("channelHashHex=%s, want AA", p.ChannelHashHex)
	}
	if p.SenderTimestamp != 1700000000 {
		t.Errorf("senderTimestamp=%d, want 1700000000", p.SenderTimestamp)
	}
}

func TestDecodeGrpTxtDecryptionFailed(t *testing.T) {
	key := "2cc3d22840e086105ad73443da2cacb8"
	ctHex, macHex := buildTestCiphertext(key, "Hello", 100)
	macBytes, _ := hex.DecodeString(macHex)
	ctBytes, _ := hex.DecodeString(ctHex)

	buf := []byte{0xFF}
	buf = append(buf, macBytes...)
	buf = append(buf, ctBytes...)

	wrongKeys := map[string]string{"#wrong": "deadbeefdeadbeefdeadbeefdeadbeef"}
	p := decodeGrpTxt(buf, wrongKeys)

	if p.Type != "GRP_TXT" {
		t.Errorf("type=%s, want GRP_TXT", p.Type)
	}
	if p.DecryptionStatus != "decryption_failed" {
		t.Errorf("decryptionStatus=%s, want decryption_failed", p.DecryptionStatus)
	}
	if p.ChannelHashHex != "FF" {
		t.Errorf("channelHashHex=%s, want FF", p.ChannelHashHex)
	}
}

func TestDecodeGrpTxtNoKey(t *testing.T) {
	buf := []byte{0x03, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}
	p := decodeGrpTxt(buf, nil)

	if p.Type != "GRP_TXT" {
		t.Errorf("type=%s, want GRP_TXT", p.Type)
	}
	if p.DecryptionStatus != "no_key" {
		t.Errorf("decryptionStatus=%s, want no_key", p.DecryptionStatus)
	}
	if p.ChannelHashHex != "03" {
		t.Errorf("channelHashHex=%s, want 03", p.ChannelHashHex)
	}
}

func TestDecodeGrpTxtEmptyKeys(t *testing.T) {
	buf := []byte{0xFF, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}
	p := decodeGrpTxt(buf, map[string]string{})

	if p.DecryptionStatus != "no_key" {
		t.Errorf("decryptionStatus=%s, want no_key", p.DecryptionStatus)
	}
}

func TestDecodeGrpTxtShortEncryptedNoDecryptAttempt(t *testing.T) {
	// encryptedData < 5 bytes (10 hex chars) → should not attempt decryption
	buf := []byte{0xFF, 0xAA, 0xBB, 0xCC, 0xDD}
	keys := map[string]string{"#test": "2cc3d22840e086105ad73443da2cacb8"}
	p := decodeGrpTxt(buf, keys)

	if p.DecryptionStatus != "no_key" {
		t.Errorf("decryptionStatus=%s, want no_key (too short for decryption)", p.DecryptionStatus)
	}
}

func TestDecodeGrpTxtMultipleKeysTriesAll(t *testing.T) {
	correctKey := "2cc3d22840e086105ad73443da2cacb8"
	ctHex, macHex := buildTestCiphertext(correctKey, "Eve: Found it", 999)
	macBytes, _ := hex.DecodeString(macHex)
	ctBytes, _ := hex.DecodeString(ctHex)

	buf := []byte{0x01}
	buf = append(buf, macBytes...)
	buf = append(buf, ctBytes...)

	keys := map[string]string{
		"#wrong1":  "deadbeefdeadbeefdeadbeefdeadbeef",
		"#correct": correctKey,
		"#wrong2":  "11111111111111111111111111111111",
	}
	p := decodeGrpTxt(buf, keys)

	if p.Type != "CHAN" {
		t.Errorf("type=%s, want CHAN", p.Type)
	}
	if p.Channel != "#correct" {
		t.Errorf("channel=%s, want #correct", p.Channel)
	}
	if p.Sender != "Eve" {
		t.Errorf("sender=%q, want Eve", p.Sender)
	}
}

func TestDecodeGrpTxtChannelHashHexZeroPad(t *testing.T) {
	buf := []byte{0x03, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE}
	p := decodeGrpTxt(buf, nil)
	if p.ChannelHashHex != "03" {
		t.Errorf("channelHashHex=%s, want 03 (zero-padded)", p.ChannelHashHex)
	}
}

func TestDecodeGrpTxtChannelHashHexFF(t *testing.T) {
	buf := []byte{0xFF, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE}
	p := decodeGrpTxt(buf, nil)
	if p.ChannelHashHex != "FF" {
		t.Errorf("channelHashHex=%s, want FF", p.ChannelHashHex)
	}
}

// --- Garbage text detection (fixes #197) ---

func TestDecryptChannelMessageGarbageText(t *testing.T) {
	// Build ciphertext with binary garbage as the message
	key := "2cc3d22840e086105ad73443da2cacb8"
	garbage := "\x01\x02\x03\x80\x81"
	ctHex, macHex := buildTestCiphertext(key, garbage, 1700000000)

	_, err := decryptChannelMessage(ctHex, macHex, key)
	if err == nil {
		t.Fatal("expected error for garbage text, got nil")
	}
	if !strings.Contains(err.Error(), "non-printable") {
		t.Errorf("error should mention non-printable: %v", err)
	}
}

func TestDecryptChannelMessageValidText(t *testing.T) {
	key := "2cc3d22840e086105ad73443da2cacb8"
	ctHex, macHex := buildTestCiphertext(key, "Alice: Hello\nworld", 1700000000)

	result, err := decryptChannelMessage(ctHex, macHex, key)
	if err != nil {
		t.Fatalf("unexpected error for valid text: %v", err)
	}
	if result.Sender != "Alice" {
		t.Errorf("sender=%q, want Alice", result.Sender)
	}
	if result.Message != "Hello\nworld" {
		t.Errorf("message=%q, want 'Hello\\nworld'", result.Message)
	}
}

func TestDecodeGrpTxtGarbageMarkedFailed(t *testing.T) {
	key := "2cc3d22840e086105ad73443da2cacb8"
	garbage := "\x01\x02\x03\x04\x05"
	ctHex, macHex := buildTestCiphertext(key, garbage, 1700000000)

	macBytes, _ := hex.DecodeString(macHex)
	ctBytes, _ := hex.DecodeString(ctHex)
	buf := make([]byte, 1+2+len(ctBytes))
	buf[0] = 0xFF // channel hash
	buf[1] = macBytes[0]
	buf[2] = macBytes[1]
	copy(buf[3:], ctBytes)

	keys := map[string]string{"#general": key}
	p := decodeGrpTxt(buf, keys)

	if p.DecryptionStatus != "decryption_failed" {
		t.Errorf("decryptionStatus=%s, want decryption_failed", p.DecryptionStatus)
	}
	if p.Type != "GRP_TXT" {
		t.Errorf("type=%s, want GRP_TXT", p.Type)
	}
}

func TestDecodeAdvertWithTelemetry(t *testing.T) {
	pubkey := strings.Repeat("AA", 32)
	timestamp := "78563412"
	signature := strings.Repeat("BB", 64)
	flags := "94" // sensor(4) | hasLocation(0x10) | hasName(0x80)
	lat := "40933402"
	lon := "E0E6B8F8"
	name := hex.EncodeToString([]byte("Sensor1"))
	nullTerm := "00"
	batteryLE := make([]byte, 2)
	binary.LittleEndian.PutUint16(batteryLE, 3700)
	tempLE := make([]byte, 2)
	binary.LittleEndian.PutUint16(tempLE, uint16(int16(2850)))

	hexStr := "1200" + pubkey + timestamp + signature + flags + lat + lon +
		name + nullTerm +
		hex.EncodeToString(batteryLE) + hex.EncodeToString(tempLE)

	pkt, err := DecodePacket(hexStr, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	if pkt.Payload.Name != "Sensor1" {
		t.Errorf("name=%s, want Sensor1", pkt.Payload.Name)
	}
	if pkt.Payload.BatteryMv == nil {
		t.Fatal("battery_mv should not be nil")
	}
	if *pkt.Payload.BatteryMv != 3700 {
		t.Errorf("battery_mv=%d, want 3700", *pkt.Payload.BatteryMv)
	}
	if pkt.Payload.TemperatureC == nil {
		t.Fatal("temperature_c should not be nil")
	}
	if math.Abs(*pkt.Payload.TemperatureC-28.50) > 0.01 {
		t.Errorf("temperature_c=%f, want 28.50", *pkt.Payload.TemperatureC)
	}
}

func TestDecodeAdvertWithTelemetryNegativeTemp(t *testing.T) {
	pubkey := strings.Repeat("CC", 32)
	timestamp := "00000000"
	signature := strings.Repeat("DD", 64)
	flags := "84" // sensor(4) | hasName(0x80), no location
	name := hex.EncodeToString([]byte("Cold"))
	nullTerm := "00"
	batteryLE := make([]byte, 2)
	binary.LittleEndian.PutUint16(batteryLE, 4200)
	tempLE := make([]byte, 2)
	var negTemp int16 = -550
	binary.LittleEndian.PutUint16(tempLE, uint16(negTemp))

	hexStr := "1200" + pubkey + timestamp + signature + flags +
		name + nullTerm +
		hex.EncodeToString(batteryLE) + hex.EncodeToString(tempLE)

	pkt, err := DecodePacket(hexStr, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	if pkt.Payload.Name != "Cold" {
		t.Errorf("name=%s, want Cold", pkt.Payload.Name)
	}
	if pkt.Payload.BatteryMv == nil || *pkt.Payload.BatteryMv != 4200 {
		t.Errorf("battery_mv=%v, want 4200", pkt.Payload.BatteryMv)
	}
	if pkt.Payload.TemperatureC == nil {
		t.Fatal("temperature_c should not be nil")
	}
	if math.Abs(*pkt.Payload.TemperatureC-(-5.50)) > 0.01 {
		t.Errorf("temperature_c=%f, want -5.50", *pkt.Payload.TemperatureC)
	}
}

func TestDecodeAdvertWithoutTelemetry(t *testing.T) {
	pubkey := strings.Repeat("EE", 32)
	timestamp := "00000000"
	signature := strings.Repeat("FF", 64)
	flags := "82" // repeater(2) | hasName(0x80)
	name := hex.EncodeToString([]byte("Node1"))

	hexStr := "1200" + pubkey + timestamp + signature + flags + name
	pkt, err := DecodePacket(hexStr, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	if pkt.Payload.Name != "Node1" {
		t.Errorf("name=%s, want Node1", pkt.Payload.Name)
	}
	if pkt.Payload.BatteryMv != nil {
		t.Errorf("battery_mv should be nil for advert without telemetry, got %d", *pkt.Payload.BatteryMv)
	}
	if pkt.Payload.TemperatureC != nil {
		t.Errorf("temperature_c should be nil for advert without telemetry, got %f", *pkt.Payload.TemperatureC)
	}
}

func TestDecodeAdvertNonSensorIgnoresTelemetryBytes(t *testing.T) {
	// A repeater node with 4 trailing bytes after the name should NOT decode telemetry.
	pubkey := strings.Repeat("AB", 32)
	timestamp := "00000000"
	signature := strings.Repeat("CD", 64)
	flags := "82" // repeater(2) | hasName(0x80)
	name := hex.EncodeToString([]byte("Rptr"))
	nullTerm := "00"
	extraBytes := "B40ED403" // battery-like and temp-like bytes

	hexStr := "1200" + pubkey + timestamp + signature + flags + name + nullTerm + extraBytes
	pkt, err := DecodePacket(hexStr, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Payload.BatteryMv != nil {
		t.Errorf("battery_mv should be nil for non-sensor node, got %d", *pkt.Payload.BatteryMv)
	}
	if pkt.Payload.TemperatureC != nil {
		t.Errorf("temperature_c should be nil for non-sensor node, got %f", *pkt.Payload.TemperatureC)
	}
}

func TestDecodeAdvertTelemetryZeroTemp(t *testing.T) {
	// 0°C is a valid temperature and must be emitted.
	pubkey := strings.Repeat("12", 32)
	timestamp := "00000000"
	signature := strings.Repeat("34", 64)
	flags := "84" // sensor(4) | hasName(0x80)
	name := hex.EncodeToString([]byte("FreezeSensor"))
	nullTerm := "00"
	batteryLE := make([]byte, 2)
	binary.LittleEndian.PutUint16(batteryLE, 3600)
	tempLE := make([]byte, 2) // tempRaw=0 → 0°C

	hexStr := "1200" + pubkey + timestamp + signature + flags +
		name + nullTerm +
		hex.EncodeToString(batteryLE) + hex.EncodeToString(tempLE)

	pkt, err := DecodePacket(hexStr, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Payload.TemperatureC == nil {
		t.Fatal("temperature_c should not be nil for 0°C")
	}
	if *pkt.Payload.TemperatureC != 0.0 {
		t.Errorf("temperature_c=%f, want 0.0", *pkt.Payload.TemperatureC)
	}
}

func repeatHex(byteHex string, n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += byteHex
	}
	return s
}

func TestZeroHopDirectHashSize(t *testing.T) {
	// DIRECT (RouteType=2) + REQ (PayloadType=0) → header byte = 0x02
	// pathByte=0x00 → hash_count=0, hash_size bits=0 → should get HashSize=0
	hex := "02" + "00" + repeatHex("AA", 20)
	pkt, err := DecodePacket(hex, nil, false)
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
	hex := "02" + "40" + repeatHex("AA", 20)
	pkt, err := DecodePacket(hex, nil, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 0 {
		t.Errorf("DIRECT zero-hop with hash_size bits set: want HashSize=0, got %d", pkt.Path.HashSize)
	}
}

func TestNonDirectZeroPathByteKeepsHashSize(t *testing.T) {
	// FLOOD (RouteType=1) + REQ (PayloadType=0) → header byte = 0x01
	// pathByte=0x00 → non-DIRECT should keep HashSize=1
	hex := "01" + "00" + repeatHex("AA", 20)
	pkt, err := DecodePacket(hex, nil, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 1 {
		t.Errorf("FLOOD zero pathByte: want HashSize=1, got %d", pkt.Path.HashSize)
	}
}

func TestDirectNonZeroHopKeepsHashSize(t *testing.T) {
	// DIRECT (RouteType=2) + REQ (PayloadType=0) → header byte = 0x02
	// pathByte=0x01 → hash_count=1, hash_size=1 → should keep HashSize=1
	hex := "02" + "01" + repeatHex("BB", 21)
	pkt, err := DecodePacket(hex, nil, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 1 {
		t.Errorf("DIRECT with 1 hop: want HashSize=1, got %d", pkt.Path.HashSize)
	}
}

func TestZeroHopTransportDirectHashSize(t *testing.T) {
	// TRANSPORT_DIRECT (RouteType=3) + REQ (PayloadType=0) → header byte = 0x03
	// 4 bytes transport codes + pathByte=0x00 → hash_count=0 → should get HashSize=0
	hex := "03" + "11223344" + "00" + repeatHex("AA", 20)
	pkt, err := DecodePacket(hex, nil, false)
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
	pkt, err := DecodePacket(hex, nil, false)
	if err != nil {
		t.Fatalf("DecodePacket failed: %v", err)
	}
	if pkt.Path.HashSize != 0 {
		t.Errorf("TRANSPORT_DIRECT zero-hop with hash_size bits set: want HashSize=0, got %d", pkt.Path.HashSize)
	}
}

func TestValidateAdvertSignature(t *testing.T) {
	// Generate a real ed25519 key pair
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	var timestamp uint32 = 1234567890
	appdata := []byte{0x02, 0x11, 0x22} // flags + some data

	// Build the signed message: pubKey + timestamp(LE) + appdata
	message := make([]byte, 32+4+len(appdata))
	copy(message[0:32], pub)
	binary.LittleEndian.PutUint32(message[32:36], timestamp)
	copy(message[36:], appdata)

	sig := ed25519.Sign(priv, message)

	// Valid signature
	valid, err := sigvalidate.ValidateAdvert([]byte(pub), sig, timestamp, appdata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Error("expected valid signature")
	}

	// Tampered appdata → invalid
	badAppdata := []byte{0x03, 0x11, 0x22}
	valid, err = sigvalidate.ValidateAdvert([]byte(pub), sig, timestamp, badAppdata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Error("expected invalid signature with tampered appdata")
	}

	// Wrong timestamp → invalid
	valid, err = sigvalidate.ValidateAdvert([]byte(pub), sig, timestamp+1, appdata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Error("expected invalid signature with wrong timestamp")
	}

	// Wrong length pubkey
	_, err = sigvalidate.ValidateAdvert([]byte{0xAA, 0xBB}, sig, timestamp, appdata)
	if err == nil {
		t.Error("expected error for short pubkey")
	}

	// Wrong length signature
	_, err = sigvalidate.ValidateAdvert([]byte(pub), []byte{0xAA, 0xBB}, timestamp, appdata)
	if err == nil {
		t.Error("expected error for short signature")
	}
}

func TestDecodeAdvertWithSignatureValidation(t *testing.T) {
	// Generate key pair
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	var timestamp uint32 = 1000000
	appdata := []byte{0x02} // repeater type, no location

	// Build signed message
	message := make([]byte, 32+4+len(appdata))
	copy(message[0:32], pub)
	binary.LittleEndian.PutUint32(message[32:36], timestamp)
	copy(message[36:], appdata)
	sig := ed25519.Sign(priv, message)

	// Build advert buffer: pubkey(32) + timestamp(4) + signature(64) + appdata
	buf := make([]byte, 0, 101)
	buf = append(buf, pub...)
	ts := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts, timestamp)
	buf = append(buf, ts...)
	buf = append(buf, sig...)
	buf = append(buf, appdata...)

	// With validation enabled
	p := decodeAdvert(buf, true)
	if p.Error != "" {
		t.Fatalf("decode error: %s", p.Error)
	}
	if p.SignatureValid == nil {
		t.Fatal("SignatureValid should be set when validation enabled")
	}
	if !*p.SignatureValid {
		t.Error("expected valid signature")
	}

	// Without validation
	p2 := decodeAdvert(buf, false)
	if p2.SignatureValid != nil {
		t.Error("SignatureValid should be nil when validation disabled")
	}
}
