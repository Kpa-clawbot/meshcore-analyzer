package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// hmacSHA256 computes HMAC-SHA256 for test use.
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// newTestContext extracts the repeated newTestStore + MQTTSource boilerplate.
func newTestContext(t *testing.T) (*Store, MQTTSource) {
	t.Helper()
	return newTestStore(t), MQTTSource{Name: "test"}
}

// --- config.go: NodeDaysOrDefault (0% coverage) ---

func TestNodeDaysOrDefault(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want int
	}{
		{"nil retention", Config{}, 7},
		{"zero nodeDays", Config{Retention: &RetentionConfig{NodeDays: 0}}, 7},
		{"negative nodeDays", Config{Retention: &RetentionConfig{NodeDays: -1}}, 7},
		{"custom nodeDays", Config{Retention: &RetentionConfig{NodeDays: 14}}, 14},
		{"one day", Config{Retention: &RetentionConfig{NodeDays: 1}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.NodeDaysOrDefault()
			if got != tt.want {
				t.Errorf("NodeDaysOrDefault() = %d, want %d", got, tt.want)
			}
		})
	}
}

// --- config.go: ResolvedSources broker scheme normalization (71.4% → 100%) ---

func TestResolvedSourcesBrokerScheme(t *testing.T) {
	cfg := &Config{
		MQTTSources: []MQTTSource{
			{Name: "mqtt", Broker: "mqtt://broker:1883"},
			{Name: "mqtts", Broker: "mqtts://broker:8883"},
			{Name: "tcp", Broker: "tcp://broker:1883"},
		},
	}
	sources := cfg.ResolvedSources()
	if sources[0].Broker != "tcp://broker:1883" {
		t.Errorf("mqtt:// should become tcp://, got %s", sources[0].Broker)
	}
	if sources[1].Broker != "ssl://broker:8883" {
		t.Errorf("mqtts:// should become ssl://, got %s", sources[1].Broker)
	}
	if sources[2].Broker != "tcp://broker:1883" {
		t.Errorf("tcp:// should stay, got %s", sources[2].Broker)
	}
}

// --- db.go: MoveStaleNodes (0% coverage) ---

func TestMoveStaleNodes(t *testing.T) {
	store := newTestStore(t)

	// Insert a node with last_seen 30 days ago
	err := store.UpsertNode("deadbeef1234567890abcdef12345678", "OldNode", "companion", nil, nil, "2020-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}

	// Insert a recent node
	err = store.UpsertNode("aabbccdd1234567890abcdef12345678", "NewNode", "repeater", nil, nil, "2099-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}

	moved, err := store.MoveStaleNodes(7)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Errorf("moved=%d, want 1", moved)
	}

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM inactive_nodes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("inactive_nodes count=%d, want 1", count)
	}

	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("nodes count=%d, want 1", count)
	}
}

func TestMoveStaleNodesNoneToMove(t *testing.T) {
	store := newTestStore(t)
	moved, err := store.MoveStaleNodes(7)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 0 {
		t.Errorf("moved=%d, want 0", moved)
	}
}

// --- geo_filter.go: NodePassesGeoFilter (40% → 100%) ---

func TestNodePassesGeoFilterAllBranches(t *testing.T) {
	lat, lon := 37.0, -122.0
	outLat, outLon := 50.0, 10.0

	latMin, latMax := 36.0, 38.0
	lonMin, lonMax := -123.0, -121.0
	gf := &GeoFilterConfig{
		LatMin: &latMin, LatMax: &latMax,
		LonMin: &lonMin, LonMax: &lonMax,
	}

	if !NodePassesGeoFilter(nil, nil, gf) {
		t.Error("nil coords should pass")
	}
	if !NodePassesGeoFilter(&lat, nil, gf) {
		t.Error("nil lon should pass")
	}
	if !NodePassesGeoFilter(nil, &lon, gf) {
		t.Error("nil lat should pass")
	}
	if !NodePassesGeoFilter(&lat, &lon, gf) {
		t.Error("inside filter should pass")
	}
	if NodePassesGeoFilter(&outLat, &outLon, gf) {
		t.Error("outside filter should fail")
	}
}

// --- main.go: handleMessage channel messages (41.4% → higher) ---

func TestHandleMessageChannelMessage(t *testing.T) {
	store, source := newTestContext(t)

	payload := []byte(`{"text":"Alice: Hello everyone","channel_idx":3,"SNR":5.0,"RSSI":-95,"score":10,"direction":"rx","sender_timestamp":1700000000}`)
	msg := &mockMessage{topic: "meshcore/message/channel/2", payload: payload}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("transmissions count=%d, want 1", count)
	}

	// Verify stored transmission values
	var decodedJSON string
	if err := store.db.QueryRow("SELECT decoded_json FROM transmissions LIMIT 1").Scan(&decodedJSON); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(decodedJSON), &decoded); err != nil {
		t.Fatalf("decoded_json unmarshal: %v", err)
	}
	if decoded["type"] != "CHAN" {
		t.Errorf("type=%v, want CHAN", decoded["type"])
	}
	if decoded["text"] != "Alice: Hello everyone" {
		t.Errorf("text=%v, want 'Alice: Hello everyone'", decoded["text"])
	}
	if decoded["sender"] != "Alice" {
		t.Errorf("sender=%v, want Alice", decoded["sender"])
	}
	if decoded["channel"] != "ch3" {
		t.Errorf("channel=%v, want ch3", decoded["channel"])
	}

	// Verify observation values
	var snr, rssi *float64
	var score *float64
	var direction *string
	if err := store.db.QueryRow("SELECT snr, rssi, score, direction FROM observations LIMIT 1").Scan(&snr, &rssi, &score, &direction); err != nil {
		t.Fatal(err)
	}
	if snr == nil || *snr != 5.0 {
		t.Errorf("snr=%v, want 5.0", snr)
	}
	if direction == nil || *direction != "rx" {
		t.Errorf("direction=%v, want rx", direction)
	}

	// Sender node should NOT be created (see issue #665: synthetic "sender-" keys
	// are unreachable from the claiming/health flow)
	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("nodes count=%d, want 0 (no phantom sender node)", count)
	}
}

func TestHandleMessageChannelMessageEmptyText(t *testing.T) {
	store, source := newTestContext(t)

	msg := &mockMessage{topic: "meshcore/message/channel/1", payload: []byte(`{"text":""}`)}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("empty text should not insert")
	}
}

func TestHandleMessageChannelNoSender(t *testing.T) {
	store, source := newTestContext(t)

	msg := &mockMessage{topic: "meshcore/message/channel/1", payload: []byte(`{"text":"no sender here"}`)}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("no sender should mean no node")
	}
}

func TestHandleMessageDirectMessage(t *testing.T) {
	store, source := newTestContext(t)

	payload := []byte(`{"text":"Bob: Hey there","sender_timestamp":1700000000,"SNR":3.0,"rssi":-100,"Score":8,"Direction":"tx"}`)
	msg := &mockMessage{topic: "meshcore/message/direct/abc123", payload: payload}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("transmissions count=%d, want 1", count)
	}

	// Verify stored decoded values
	var decodedJSON string
	if err := store.db.QueryRow("SELECT decoded_json FROM transmissions LIMIT 1").Scan(&decodedJSON); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(decodedJSON), &decoded); err != nil {
		t.Fatalf("decoded_json unmarshal: %v", err)
	}
	if decoded["type"] != "DM" {
		t.Errorf("type=%v, want DM", decoded["type"])
	}
	if decoded["sender"] != "Bob" {
		t.Errorf("sender=%v, want Bob", decoded["sender"])
	}

	// Verify observation score=8 and direction=tx
	var score *float64
	var direction *string
	if err := store.db.QueryRow("SELECT score, direction FROM observations LIMIT 1").Scan(&score, &direction); err != nil {
		t.Fatal(err)
	}
	if score == nil || *score != 8.0 {
		t.Errorf("score=%v, want 8.0", score)
	}
	if direction == nil || *direction != "tx" {
		t.Errorf("direction=%v, want tx", direction)
	}
}

func TestHandleMessageDirectMessageEmptyText(t *testing.T) {
	store, source := newTestContext(t)

	msg := &mockMessage{topic: "meshcore/message/direct/abc", payload: []byte(`{"text":""}`)}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("empty text DM should not insert")
	}
}

func TestHandleMessageDirectNoSender(t *testing.T) {
	store, source := newTestContext(t)

	msg := &mockMessage{topic: "meshcore/message/direct/xyz", payload: []byte(`{"text":"message with no colon"}`)}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count=%d, want 1", count)
	}
}

// Test Score/Direction case-insensitive handling in raw packets
func TestHandleMessageUppercaseScoreDirection(t *testing.T) {
	store, source := newTestContext(t)

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `","Score":9.0,"Direction":"tx"}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var score *float64
	var direction *string
	if err := store.db.QueryRow("SELECT score, direction FROM observations LIMIT 1").Scan(&score, &direction); err != nil {
		t.Fatal(err)
	}
	if score == nil || *score != 9.0 {
		t.Errorf("score=%v, want 9.0", score)
	}
	if direction == nil || *direction != "tx" {
		t.Errorf("direction=%v, want tx", direction)
	}
}

// Test channel messages with lowercase snr/rssi/Score/Direction
func TestHandleMessageChannelLowercaseFields(t *testing.T) {
	store, source := newTestContext(t)

	payload := []byte(`{"text":"Test: msg","snr":3.0,"rssi":-90,"Score":5,"Direction":"rx"}`)
	msg := &mockMessage{topic: "meshcore/message/channel/0", payload: payload}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count=%d, want 1", count)
	}
}

func TestHandleMessageDirectLowercaseFields(t *testing.T) {
	store, source := newTestContext(t)

	payload := []byte(`{"text":"Test: msg","snr":2.0,"rssi":-85,"score":7,"direction":"tx"}`)
	msg := &mockMessage{topic: "meshcore/message/direct/xyz", payload: payload}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count=%d, want 1", count)
	}
}

// --- main.go: handleMessage advert with telemetry ---

func TestHandleMessageAdvertWithTelemetry(t *testing.T) {
	store, source := newTestContext(t)

	// Use a known ADVERT hex
	rawHex := "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}

	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	// Should have created transmission, node, and observer
	var txCount, nodeCount, obsCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&txCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodeCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM observers").Scan(&obsCount); err != nil {
		t.Fatal(err)
	}

	if txCount != 1 {
		t.Errorf("transmissions=%d, want 1", txCount)
	}
	if nodeCount != 1 {
		t.Errorf("nodes=%d, want 1", nodeCount)
	}
}

// --- main.go: handleMessage geo filter on advert ---

func TestHandleMessageAdvertGeoFiltered(t *testing.T) {
	store, source := newTestContext(t)

	rawHex := "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"

	latMin, latMax := -1.0, 1.0
	lonMin, lonMax := -1.0, 1.0
	gf := &GeoFilterConfig{
		LatMin: &latMin, LatMax: &latMax,
		LonMin: &lonMin, LonMax: &lonMax,
	}

	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{GeoFilter: gf})

	// Geo-filtered adverts should not create nodes
	var nodeCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodeCount); err != nil {
		t.Fatal(err)
	}
	if nodeCount != 0 {
		t.Errorf("nodes=%d, want 0 (geo-filtered advert should not create node)", nodeCount)
	}
}

// --- decoder.go: decodeAdvert with features but insufficient data ---

func TestDecodeAdvertLocationTruncated(t *testing.T) {
	buf := make([]byte, 105)
	for i := 0; i < 32; i++ {
		buf[i] = byte(i + 1)
	}
	for i := 36; i < 100; i++ {
		buf[i] = 0xCC
	}
	// flags: hasLocation(0x10) | type=1(chat) = 0x11
	buf[100] = 0x11
	// Only 4 bytes after flags — not enough for full location (needs 8)

	p := decodeAdvert(buf[:105], false)
	if p.Error != "" {
		t.Fatalf("error: %s", p.Error)
	}
	// Location should not be set (not enough data)
	if p.Lat != nil {
		t.Error("lat should be nil with truncated location data")
	}
}

func TestDecodeAdvertFeat1Truncated(t *testing.T) {
	buf := make([]byte, 102)
	for i := 0; i < 32; i++ {
		buf[i] = byte(i + 1)
	}
	for i := 36; i < 100; i++ {
		buf[i] = 0xCC
	}
	// flags: hasFeat1(0x20) | type=1 = 0x21
	buf[100] = 0x21
	// Only 1 byte after flags — not enough for feat1 (needs 2)

	p := decodeAdvert(buf[:102], false)
	if p.Feat1 != nil {
		t.Error("feat1 should be nil with truncated data")
	}
}

func TestDecodeAdvertFeat2Truncated(t *testing.T) {
	buf := make([]byte, 104)
	for i := 0; i < 32; i++ {
		buf[i] = byte(i + 1)
	}
	for i := 36; i < 100; i++ {
		buf[i] = 0xCC
	}
	// flags: hasFeat1(0x20) | hasFeat2(0x40) | type=1 = 0x61
	buf[100] = 0x61
	// feat1: 2 bytes
	buf[101] = 0x01
	buf[102] = 0x00
	// Only 1 byte left — not enough for feat2

	p := decodeAdvert(buf[:104], false)
	if p.Feat1 == nil {
		t.Error("feat1 should be set")
	}
	if p.Feat2 != nil {
		t.Error("feat2 should be nil with truncated data")
	}
}

// --- decoder.go: decodeAdvert sensor with out-of-range telemetry ---

func TestDecodeAdvertSensorBadTelemetry(t *testing.T) {
	buf := make([]byte, 112)
	// Bytes 0-31: public key
	for i := 0; i < 32; i++ {
		buf[i] = byte(i + 1)
	}
	// Bytes 32-35: reserved (4 bytes)
	// Bytes 36-99: padding (64 bytes of 0xCC)
	for i := 36; i < 100; i++ {
		buf[i] = 0xCC
	}
	// Byte 100: flags — sensor(4) | hasName(0x80) = 0x84
	//   off starts at 101 after flags byte
	buf[100] = 0x84

	// Bytes 101-102: name "S" + null terminator
	//   Name parsing reads from off=101, finds null at 102, advances off to 103
	copy(buf[101:], []byte("S\x00"))

	// Bytes 103-104: battery_mv (uint16 LE) = 0 (out of range, should be skipped)
	//   off=103 after name parsing
	buf[103] = 0x00
	buf[104] = 0x00

	// Bytes 105-106: temperature (uint16 LE) = 0x4E20 = 20000 raw (200.00°C, out of range)
	//   off=105 after battery
	buf[105] = 0x20
	buf[106] = 0x4E

	p := decodeAdvert(buf[:107], false)
	if p.BatteryMv != nil {
		t.Error("battery_mv=0 should be nil")
	}
	if p.TemperatureC != nil {
		t.Error("out-of-range temp should be nil")
	}
}

// --- decoder.go: countNonPrintable with RuneError ---

func TestCountNonPrintableRuneError(t *testing.T) {
	// Invalid UTF-8 bytes
	got := countNonPrintable(string([]byte{0xff, 0xfe}))
	if got != 2 {
		t.Errorf("countNonPrintable invalid UTF-8 = %d, want 2", got)
	}

	// Normal text
	if countNonPrintable("hello") != 0 {
		t.Error("normal text should have 0 non-printable")
	}
}

// --- decoder.go: decryptChannelMessage edge cases ---

func TestDecryptChannelMessageEmptyCiphertext(t *testing.T) {
	_, err := decryptChannelMessage("", "0011", "00112233445566778899aabbccddeeff")
	if err == nil {
		t.Error("empty ciphertext should error")
	}
}

func TestDecryptChannelMessageMACFailsBeforeAlignment(t *testing.T) {
	// 15 bytes of ciphertext — not aligned to AES block size (16).
	// However, decryptChannelMessage checks MAC before alignment, so with
	// random ciphertext the HMAC won't match and it errors on MAC first.
	_, err := decryptChannelMessage("00112233445566778899aabbccddee", "0011", "00112233445566778899aabbccddeeff")
	if err == nil {
		t.Error("should error (MAC mismatch)")
	}
	if err.Error() != "MAC verification failed" {
		t.Errorf("expected MAC error, got: %v", err)
	}
}

func TestDecryptChannelMessageNotAligned(t *testing.T) {
	// To actually exercise the alignment branch, we need ciphertext whose
	// HMAC-SHA256 first 2 bytes match the provided MAC, but whose length
	// is not a multiple of 16. We craft this by computing the real MAC.
	key := "00112233445566778899aabbccddeeff"
	// 15 bytes of ciphertext (not aligned to 16)
	ciphertextBytes := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}
	ciphertextHex := hex.EncodeToString(ciphertextBytes)

	// Compute real HMAC to pass the MAC check
	keyBytes, _ := hex.DecodeString(key)
	channelSecret := make([]byte, 32)
	copy(channelSecret, keyBytes)

	h := hmacSHA256(channelSecret, ciphertextBytes)
	macHex := hex.EncodeToString(h[:2])

	_, err := decryptChannelMessage(ciphertextHex, macHex, key)
	if err == nil {
		t.Error("unaligned ciphertext should error")
	}
	if err.Error() != "ciphertext not aligned to AES block size" {
		t.Errorf("expected alignment error, got: %v", err)
	}
}

func TestDecryptChannelMessageBadMACHex(t *testing.T) {
	_, err := decryptChannelMessage("00112233445566778899aabbccddeeff", "ZZ", "00112233445566778899aabbccddeeff")
	if err == nil {
		t.Error("invalid MAC hex should error")
	}
}

func TestDecryptChannelMessageBadCiphertextHex(t *testing.T) {
	_, err := decryptChannelMessage("ZZZZ", "0011", "00112233445566778899aabbccddeeff")
	if err == nil {
		t.Error("invalid ciphertext hex should error")
	}
}

// --- db.go: Checkpoint and LogStats ---

func TestCheckpointDoesNotPanic(t *testing.T) {
	store := newTestStore(t)
	store.Checkpoint()
}

func TestLogStatsDoesNotPanic(t *testing.T) {
	store := newTestStore(t)
	store.Stats.TransmissionsInserted.Add(5)
	store.LogStats()
}

// --- decoder.go: ComputeContentHash path overflow fallback ---

func TestComputeContentHashPathOverflow(t *testing.T) {
	// path byte 0xFF = hashSize=4, hashCount=63 = 252 bytes needed, but only a few available
	// payloadStart (2 + 252 = 254) > len(buf) (6), so fallback fires.
	// rawHex is 12 chars (< 16), so fallback returns rawHex itself.
	rawHex := "0AFF" + "AABBCCDD"
	got := ComputeContentHash(rawHex)
	if got != rawHex {
		t.Errorf("got=%s, want fallback=%s", got, rawHex)
	}
}

// --- main.go: handleMessage advert that fails ValidateAdvert ---

func TestHandleMessageCorruptedAdvertNoNode(t *testing.T) {
	store, source := newTestContext(t)

	// Build an ADVERT packet with all-zero pubkey (fails ValidateAdvert)
	// header: 0x12 = FLOOD + ADVERT, path: 0x00
	// Then 100+ bytes of zeros (pubkey all zeros)
	rawHex := "1200"
	for i := 0; i < 110; i++ {
		rawHex += "00"
	}
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("all-zero pubkey advert should not create a node")
	}
}

// --- main.go: handleMessage non-advert packet (else branch) ---

func TestHandleMessageNonAdvertPacket(t *testing.T) {
	store, source := newTestContext(t)

	// ACK packet: header 0x0E = FLOOD + ACK(0x03)
	rawHex := "0E00DEADBEEF"
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("non-advert should insert transmission, got %d", count)
	}

	// Verify the stored packet type
	var payloadType int
	if err := store.db.QueryRow("SELECT payload_type FROM transmissions LIMIT 1").Scan(&payloadType); err != nil {
		t.Fatal(err)
	}
	// ACK = type 3
	if payloadType != 3 {
		t.Errorf("payload_type=%d, want 3 (ACK)", payloadType)
	}

	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("non-advert should not create nodes")
	}
}

// --- decoder.go: decodeAdvert no name but sensor with telemetry ---

func TestDecodeAdvertSensorNoName(t *testing.T) {
	buf := make([]byte, 108)
	for i := 0; i < 32; i++ {
		buf[i] = byte(i + 1)
	}
	for i := 36; i < 100; i++ {
		buf[i] = 0xCC
	}
	// flags: sensor(4) = 0x04 (no hasName)
	buf[100] = 0x04
	// telemetry right after flags: battery=3700 (0x0E74), temp=2500 (25.00°C)
	buf[101] = 0x74
	buf[102] = 0x0E
	buf[103] = 0xC4
	buf[104] = 0x09

	p := decodeAdvert(buf[:105], false)
	if p.Error != "" {
		t.Fatalf("error: %s", p.Error)
	}
	if p.Name != "" {
		t.Errorf("name=%q, want empty", p.Name)
	}
	if p.BatteryMv == nil || *p.BatteryMv != 3700 {
		t.Errorf("battery_mv=%v, want 3700", p.BatteryMv)
	}
}

// --- db.go: OpenStore error path (invalid dir) ---

func TestOpenStoreInvalidPath(t *testing.T) {
	// Path under /dev/null can't create directory
	_, err := OpenStore("/dev/null/impossible/path/db.sqlite")
	if err == nil {
		t.Error("should error on impossible path")
	}
}

// --- db.go: InsertTransmission default timestamp ---

func TestInsertTransmissionDefaultTimestamp(t *testing.T) {
	store := newTestStore(t)
	data := &PacketData{
		RawHex:    "AABB",
		Hash:      "default_ts_test1",
		Timestamp: "", // empty → should use now
	}
	isNew, err := store.InsertTransmission(data)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Error("should be new")
	}
}

// --- db.go: UpsertNode default timestamp ---

func TestUpsertNodeDefaultTimestamp(t *testing.T) {
	store := newTestStore(t)
	// Empty lastSeen → uses time.Now()
	err := store.UpsertNode("pk_default_ts_00000000000000000000000000000001", "Node", "companion", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var lastSeen string
	if err := store.db.QueryRow("SELECT last_seen FROM nodes WHERE public_key = 'pk_default_ts_00000000000000000000000000000001'").Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if lastSeen == "" {
		t.Error("last_seen should be set")
	}
}

// --- db.go: UpsertNode with lat/lon ---

func TestUpsertNodeWithLatLon(t *testing.T) {
	store := newTestStore(t)
	lat, lon := 37.0, -122.0
	err := store.UpsertNode("pk_latlon_0000000000000000000000000000000001", "Node", "repeater", &lat, &lon, "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	var gotLat, gotLon float64
	if err := store.db.QueryRow("SELECT lat, lon FROM nodes WHERE public_key = 'pk_latlon_0000000000000000000000000000000001'").Scan(&gotLat, &gotLon); err != nil {
		t.Fatal(err)
	}
	if gotLat != 37.0 || gotLon != -122.0 {
		t.Errorf("lat=%f lon=%f", gotLat, gotLon)
	}
}

// --- decoder.go: ComputeContentHash with transport route short after transport codes ---

func TestComputeContentHashTransportShortAfterCodes(t *testing.T) {
	// Transport route (0x00), 4 bytes transport codes, but then nothing (no path byte).
	// offset after transport codes = 5, which equals len(buf), so fallback fires.
	// rawHex is 10 chars (< 16), so fallback returns rawHex itself.
	rawHex := "00AABBCCDD"
	got := ComputeContentHash(rawHex)
	if got != rawHex {
		t.Errorf("got=%s, want fallback=%s", got, rawHex)
	}
}

// --- decoder.go: DecodePacket no path byte after transport ---

func TestDecodePacketNoPathByteAfterHeader(t *testing.T) {
	// Non-transport route, but only header byte (no path byte)
	// Actually 0A alone = 1 byte, but we need >= 2
	// Header + exactly at offset boundary
	_, err := DecodePacket("0A", nil, false)
	if err == nil {
		t.Error("should error - too short")
	}
}

// --- decoder.go: decodeAdvert with name but no null terminator ---

func TestDecodeAdvertNameNoNull(t *testing.T) {
	buf := make([]byte, 115)
	for i := 0; i < 32; i++ {
		buf[i] = byte(i + 1)
	}
	for i := 36; i < 100; i++ {
		buf[i] = 0xCC
	}
	// flags: hasName(0x80) | type=1 = 0x81
	buf[100] = 0x81
	// Name without null terminator — goes to end of buffer
	copy(buf[101:], []byte("LongNameNoNull"))

	p := decodeAdvert(buf[:115], false)
	if p.Name != "LongNameNoNull" {
		t.Errorf("name=%q, want LongNameNoNull", p.Name)
	}
}

// --- main.go: handleMessage channel with very long sender (>50 chars = no extraction) ---

func TestHandleMessageChannelLongSender(t *testing.T) {
	store, source := newTestContext(t)

	// Colon at index > 50 — should not extract sender
	longText := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA: msg"
	payload := []byte(`{"text":"` + longText + `"}`)
	msg := &mockMessage{topic: "meshcore/message/channel/1", payload: payload}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("long sender should not extract")
	}
}

// --- main.go: handleMessage DM with long sender ---

func TestHandleMessageDirectLongSender(t *testing.T) {
	store, source := newTestContext(t)

	longText := "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB: msg"
	payload := []byte(`{"text":"` + longText + `"}`)
	msg := &mockMessage{topic: "meshcore/message/direct/abc", payload: payload}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count=%d, want 1", count)
	}
}

// DM with uppercase Score and Direction (fallback branches)
func TestHandleMessageDirectUppercaseScoreDirection(t *testing.T) {
	store, source := newTestContext(t)

	payload := []byte(`{"text":"X: hi","Score":6,"Direction":"rx"}`)
	msg := &mockMessage{topic: "meshcore/message/direct/d1", payload: payload}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count=%d, want 1", count)
	}

	// Verify uppercase Score/Direction were picked up
	var score *float64
	var direction *string
	if err := store.db.QueryRow("SELECT score, direction FROM observations LIMIT 1").Scan(&score, &direction); err != nil {
		t.Fatal(err)
	}
	if score == nil || *score != 6.0 {
		t.Errorf("score=%v, want 6.0", score)
	}
	if direction == nil || *direction != "rx" {
		t.Errorf("direction=%v, want rx", direction)
	}
}

// Channel with uppercase Score and Direction
func TestHandleMessageChannelUppercaseScoreDirection(t *testing.T) {
	store, source := newTestContext(t)

	payload := []byte(`{"text":"Y: hi","Score":4,"Direction":"tx"}`)
	msg := &mockMessage{topic: "meshcore/message/channel/5", payload: payload}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count=%d, want 1", count)
	}

	// Verify uppercase Score/Direction were picked up
	var score *float64
	var direction *string
	if err := store.db.QueryRow("SELECT score, direction FROM observations LIMIT 1").Scan(&score, &direction); err != nil {
		t.Fatal(err)
	}
	if score == nil || *score != 4.0 {
		t.Errorf("score=%v, want 4.0", score)
	}
	if direction == nil || *direction != "tx" {
		t.Errorf("direction=%v, want tx", direction)
	}
}

// Raw packet with only lowercase score (no uppercase Score present)
func TestHandleMessageRawLowercaseScore(t *testing.T) {
	store, source := newTestContext(t)

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `","score":3.5}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var score *float64
	if err := store.db.QueryRow("SELECT score FROM observations LIMIT 1").Scan(&score); err != nil {
		t.Fatal(err)
	}
	if score == nil || *score != 3.5 {
		t.Errorf("score=%v, want 3.5", score)
	}
}

// Test handleMessage status without origin (log fallback)
func TestHandleMessageStatusNoOrigin(t *testing.T) {
	store, source := newTestContext(t)

	msg := &mockMessage{
		topic:   "meshcore/LAX/obs5/status",
		payload: []byte(`{"model":"L1"}`),
	}
	handleMessage(store, "test", source, msg, nil, nil, &Config{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM observers WHERE id = 'obs5'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("observer count=%d, want 1", count)
	}

	// Verify fallback behavior: origin is "" (no "origin" key in payload),
	// so name should be stored as empty string, and IATA should be "LAX".
	var name, iata string
	if err := store.db.QueryRow("SELECT name, iata FROM observers WHERE id = 'obs5'").Scan(&name, &iata); err != nil {
		t.Fatal(err)
	}
	if name != "" {
		t.Errorf("name=%q, want empty (no origin provided)", name)
	}
	if iata != "LAX" {
		t.Errorf("iata=%q, want LAX", iata)
	}
}

// --- db.go: applySchema migrations run on fresh DB ---

func TestApplySchemaMigrationsOnFreshDB(t *testing.T) {
	// OpenStore already runs all migrations; verify they completed
	store := newTestStore(t)

	// Check that migrations were recorded
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM _migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 3 {
		t.Errorf("expected at least 3 migrations recorded, got %d", count)
	}

	// Check observations table exists with dedup index
	var tblName string
	err := store.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='observations'").Scan(&tblName)
	if err != nil {
		t.Error("observations table should exist")
	}

	// Check inactive_nodes table exists
	err = store.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='inactive_nodes'").Scan(&tblName)
	if err != nil {
		t.Error("inactive_nodes table should exist")
	}

	// Check packets_v view exists
	err = store.db.QueryRow("SELECT name FROM sqlite_master WHERE type='view' AND name='packets_v'").Scan(&tblName)
	if err != nil {
		t.Error("packets_v view should exist")
	}
}

// Test OpenStore runs successfully on existing DB (re-open)
func TestOpenStoreExistingDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"

	// Open and close
	s1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// Re-open — should skip migrations (already applied)
	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

// Test MoveStaleNodes with existing inactive nodes (REPLACE behavior)
func TestMoveStaleNodesReplace(t *testing.T) {
	store := newTestStore(t)

	pk := "stale_node_replace_0000000000000000000000000001"
	// Insert into inactive_nodes first
	if _, err := store.db.Exec("INSERT INTO inactive_nodes (public_key, name, role, last_seen, first_seen) VALUES (?, 'Old', 'companion', '2019-01-01T00:00:00Z', '2019-01-01T00:00:00Z')", pk); err != nil {
		t.Fatal(err)
	}

	// Insert same node in nodes with old last_seen
	store.UpsertNode(pk, "StaleNode", "repeater", nil, nil, "2020-01-01T00:00:00Z")

	moved, err := store.MoveStaleNodes(7)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Errorf("moved=%d, want 1", moved)
	}

	// Should have replaced the inactive node
	var name string
	if err := store.db.QueryRow("SELECT name FROM inactive_nodes WHERE public_key = ?", pk).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "StaleNode" {
		t.Errorf("name=%s, want StaleNode (replaced)", name)
	}
}

// --- decoder.go: ValidateAdvert name too long ---

func TestValidateAdvertNameTooLong(t *testing.T) {
	longName := ""
	for i := 0; i < 65; i++ {
		longName += "A"
	}
	p := &Payload{PubKey: "aabbccdd00112233445566778899aabb", Name: longName}
	ok, reason := ValidateAdvert(p)
	if ok {
		t.Error("name >64 chars should fail")
	}
	if reason == "" {
		t.Error("should have reason")
	}
}

// Test ValidateAdvert pubkey too short
func TestValidateAdvertPubkeyTooShort(t *testing.T) {
	p := &Payload{PubKey: "aabb"}
	ok, _ := ValidateAdvert(p)
	if ok {
		t.Error("short pubkey should fail")
	}
}

// --- decoder.go: decodeTrace with extra path data ---

func TestDecodeTraceWithPath(t *testing.T) {
	buf := make([]byte, 15)
	// tag (4) + authCode (4) + flags (1) + path data (6)
	buf[0] = 0x01 // tag
	buf[4] = 0x02 // authCode
	buf[8] = 0x03 // flags
	buf[9] = 0xAA
	buf[10] = 0xBB
	buf[11] = 0xCC
	buf[12] = 0xDD
	buf[13] = 0xEE
	buf[14] = 0xFF

	p := decodeTrace(buf)
	if p.PathData == "" {
		t.Error("should have path data")
	}
	if p.TraceFlags == nil || *p.TraceFlags != 3 {
		t.Errorf("flags=%v, want 3", p.TraceFlags)
	}
}

// --- db.go: RemoveStaleObservers (soft-delete) ---

func TestRemoveStaleObservers(t *testing.T) {
	store := newTestStore(t)

	// Insert an observer with last_seen 30 days ago
	err := store.UpsertObserver("obs-old", "OldObserver", "LAX", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Override last_seen to 30 days ago
	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
	_, err = store.db.Exec("UPDATE observers SET last_seen = ? WHERE id = ?", cutoff, "obs-old")
	if err != nil {
		t.Fatal(err)
	}

	// Insert a recent observer
	err = store.UpsertObserver("obs-new", "NewObserver", "NYC", nil)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := store.RemoveStaleObservers(14)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed=%d, want 1", removed)
	}

	// Observer should still be in the table (soft-delete), but marked inactive
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM observers").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("observers count=%d, want 2 (soft-delete preserves row)", count)
	}

	// Check that the old observer is marked inactive
	var inactive int
	if err := store.db.QueryRow("SELECT inactive FROM observers WHERE id = ?", "obs-old").Scan(&inactive); err != nil {
		t.Fatal(err)
	}
	if inactive != 1 {
		t.Errorf("obs-old inactive=%d, want 1", inactive)
	}

	// Check that the recent observer is still active
	var newInactive int
	if err := store.db.QueryRow("SELECT inactive FROM observers WHERE id = ?", "obs-new").Scan(&newInactive); err != nil {
		t.Fatal(err)
	}
	if newInactive != 0 {
		t.Errorf("obs-new inactive=%d, want 0", newInactive)
	}
}

func TestRemoveStaleObserversNone(t *testing.T) {
	store := newTestStore(t)

	removed, err := store.RemoveStaleObservers(14)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed=%d, want 0", removed)
	}
}

func TestRemoveStaleObserversKeepForever(t *testing.T) {
	store := newTestStore(t)

	// Insert an old observer
	err := store.UpsertObserver("obs-ancient", "AncientObserver", "LAX", nil)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -365).Format(time.RFC3339)
	_, err = store.db.Exec("UPDATE observers SET last_seen = ? WHERE id = ?", cutoff, "obs-ancient")
	if err != nil {
		t.Fatal(err)
	}

	// observerDays = -1 means keep forever
	removed, err := store.RemoveStaleObservers(-1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed=%d, want 0 (keep forever)", removed)
	}

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM observers").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("observers count=%d, want 1 (keep forever)", count)
	}

	// Observer should NOT be marked inactive
	var inactive int
	if err := store.db.QueryRow("SELECT inactive FROM observers WHERE id = ?", "obs-ancient").Scan(&inactive); err != nil {
		t.Fatal(err)
	}
	if inactive != 0 {
		t.Errorf("obs-ancient inactive=%d, want 0 (keep forever)", inactive)
	}
}

func TestRemoveStaleObserversReactivation(t *testing.T) {
	store := newTestStore(t)

	// Insert and stale-mark an observer
	err := store.UpsertObserver("obs-test", "TestObserver", "LAX", nil)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
	_, err = store.db.Exec("UPDATE observers SET last_seen = ? WHERE id = ?", cutoff, "obs-test")
	if err != nil {
		t.Fatal(err)
	}

	removed, err := store.RemoveStaleObservers(14)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed=%d, want 1", removed)
	}

	// Verify it's inactive
	var inactive int
	if err := store.db.QueryRow("SELECT inactive FROM observers WHERE id = ?", "obs-test").Scan(&inactive); err != nil {
		t.Fatal(err)
	}
	if inactive != 1 {
		t.Errorf("inactive=%d, want 1 after soft-delete", inactive)
	}

	// Now UpsertObserver should reactivate it
	err = store.UpsertObserver("obs-test", "TestObserver", "LAX", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.db.QueryRow("SELECT inactive FROM observers WHERE id = ?", "obs-test").Scan(&inactive); err != nil {
		t.Fatal(err)
	}
	if inactive != 0 {
		t.Errorf("inactive=%d, want 0 after reactivation", inactive)
	}
}

func TestObserverDaysOrDefault(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want int
	}{
		{"nil retention", &Config{}, 14},
		{"zero observer days", &Config{Retention: &RetentionConfig{ObserverDays: 0}}, 14},
		{"positive value", &Config{Retention: &RetentionConfig{ObserverDays: 30}}, 30},
		{"keep forever", &Config{Retention: &RetentionConfig{ObserverDays: -1}}, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.ObserverDaysOrDefault()
			if got != tt.want {
				t.Errorf("ObserverDaysOrDefault() = %d, want %d", got, tt.want)
			}
		})
	}
}
