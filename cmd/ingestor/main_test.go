package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name   string
		input  interface{}
		want   float64
		wantOK bool
	}{
		{"float64", float64(3.14), 3.14, true},
		{"float32", float32(2.5), 2.5, true},
		{"int", int(42), 42.0, true},
		{"int64", int64(100), 100.0, true},
		{"json.Number valid", json.Number("9.5"), 9.5, true},
		{"json.Number invalid", json.Number("not_a_number"), 0, false},
		{"string valid", "3.14", 3.14, true},
		{"string with spaces", "  -7.5  ", -7.5, true},
		{"string integer", "42", 42.0, true},
		{"string invalid", "hello", 0, false},
		{"string empty", "", 0, false},
		{"uint", uint(10), 10.0, true},
		{"uint64", uint64(999), 999.0, true},
		{"bool unsupported", true, 0, false},
		{"nil unsupported", nil, 0, false},
		{"slice unsupported", []int{1}, 0, false},
		{"float64 zero", float64(0), 0.0, true},
		{"float64 negative", float64(-5.5), -5.5, true},
		{"int64 large", int64(math.MaxInt32), float64(math.MaxInt32), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toFloat64(tt.input)
			if ok != tt.wantOK {
				t.Errorf("toFloat64(%v) ok=%v, want %v", tt.input, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("toFloat64(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"all empty", []string{"", "", ""}, ""},
		{"first non-empty", []string{"", "hello", "world"}, "hello"},
		{"first value", []string{"first", "second"}, "first"},
		{"single empty", []string{""}, ""},
		{"single value", []string{"only"}, "only"},
		{"no args", nil, ""},
		{"empty then value", []string{"", "", "last"}, "last"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstNonEmpty(tt.args...)
			if got != tt.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestUnixTime(t *testing.T) {
	tests := []struct {
		name  string
		epoch int64
		want  time.Time
	}{
		{"zero epoch", 0, time.Unix(0, 0)},
		{"known date", 1700000000, time.Unix(1700000000, 0)},
		{"negative epoch", -1, time.Unix(-1, 0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unixTime(tt.epoch)
			if !got.Equal(tt.want) {
				t.Errorf("unixTime(%d) = %v, want %v", tt.epoch, got, tt.want)
			}
		})
	}
}

// mockMessage implements mqtt.Message for testing handleMessage
type mockMessage struct {
	topic   string
	payload []byte
}

func (m *mockMessage) Duplicate() bool  { return false }
func (m *mockMessage) Qos() byte        { return 0 }
func (m *mockMessage) Retained() bool   { return false }
func (m *mockMessage) Topic() string     { return m.topic }
func (m *mockMessage) MessageID() uint16 { return 0 }
func (m *mockMessage) Payload() []byte   { return m.payload }
func (m *mockMessage) Ack()              {}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHandleMessageRawPacket(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `","SNR":5.5,"RSSI":-100.0,"origin":"myobs"}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}

	handleMessage(store, "test", source, msg, nil, &Config{})

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Errorf("transmissions count=%d, want 1", count)
	}
}

func TestHandleMessageRawPacketAdvert(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	rawHex := "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"
	payload := []byte(`{"raw":"` + rawHex + `"}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}

	handleMessage(store, "test", source, msg, nil, &Config{})

	// Should create a node from the ADVERT
	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	if count != 1 {
		t.Errorf("nodes count=%d, want 1 (advert should upsert node)", count)
	}

	// Should create observer
	store.db.QueryRow("SELECT COUNT(*) FROM observers").Scan(&count)
	if count != 1 {
		t.Errorf("observers count=%d, want 1", count)
	}
}

func TestHandleMessageInvalidJSON(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: []byte(`not json`)}

	// Should not panic
	handleMessage(store, "test", source, msg, nil, &Config{})

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 0 {
		t.Error("invalid JSON should not insert")
	}
}

func TestHandleMessageStatusTopic(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/status",
		payload: []byte(`{"origin":"MyObserver"}`),
	}

	handleMessage(store, "test", source, msg, nil, &Config{})

	var name, iata string
	err := store.db.QueryRow("SELECT name, iata FROM observers WHERE id = 'obs1'").Scan(&name, &iata)
	if err != nil {
		t.Fatal(err)
	}
	if name != "MyObserver" {
		t.Errorf("name=%s, want MyObserver", name)
	}
	if iata != "SJC" {
		t.Errorf("iata=%s, want SJC", iata)
	}
}

func TestHandleMessageSkipStatusTopics(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	// meshcore/status should be skipped
	msg1 := &mockMessage{topic: "meshcore/status", payload: []byte(`{"raw":"0A00"}`)}
	handleMessage(store, "test", source, msg1, nil, &Config{})

	// meshcore/events/connection should be skipped
	msg2 := &mockMessage{topic: "meshcore/events/connection", payload: []byte(`{"raw":"0A00"}`)}
	handleMessage(store, "test", source, msg2, nil, &Config{})

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 0 {
		t.Error("status/connection topics should be skipped")
	}
}

func TestHandleMessageIATAFilter(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test", IATAFilter: []string{"LAX"}}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	// SJC is not in filter, should be skipped
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{})

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 0 {
		t.Error("IATA filter should skip non-matching regions")
	}

	// LAX is in filter, should be accepted
	msg2 := &mockMessage{
		topic:   "meshcore/LAX/obs2/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, msg2, nil, &Config{})

	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Errorf("IATA filter should allow matching region, got count=%d", count)
	}
}

func TestHandleMessageIATAFilterNoRegion(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test", IATAFilter: []string{"LAX"}}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	// topic with only 1 part — no region to filter on
	msg := &mockMessage{
		topic:   "meshcore",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{})

	// No region part → filter doesn't apply, message goes through
	// Actually the code checks len(parts) > 1 for IATA filter
	// Without > 1 parts, the filter is skipped and the message proceeds
}

func TestHandleMessageNoRawHex(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	// Valid JSON but no "raw" field → falls through to "other formats"
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"type":"companion","data":"something"}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{})

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 0 {
		t.Error("no raw hex should not insert")
	}
}

func TestHandleMessageBadRawHex(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	// Invalid hex → decode error
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"ZZZZ"}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{})

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 0 {
		t.Error("bad hex should not insert")
	}
}

func TestHandleMessageWithSNRRSSIAsNumbers(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `","SNR":7.2,"RSSI":-95}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}

	handleMessage(store, "test", source, msg, nil, &Config{})

	var snr, rssi *float64
	store.db.QueryRow("SELECT snr, rssi FROM observations LIMIT 1").Scan(&snr, &rssi)
	if snr == nil || *snr != 7.2 {
		t.Errorf("snr=%v, want 7.2", snr)
	}
}

func TestHandleMessageMinimalTopic(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	// Topic with only 2 parts: meshcore/region (no observer ID)
	msg := &mockMessage{
		topic:   "meshcore/SJC",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{})

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Errorf("should insert even with short topic, got count=%d", count)
	}
}

func TestHandleMessageCorruptedAdvert(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	// An ADVERT that's too short to be valid — decoded but fails ValidateAdvert
	// header 0x12 = FLOOD+ADVERT, path 0x00 = 0 hops
	// Then a short payload that decodeAdvert will mark as "too short for advert"
	rawHex := "1200" + "AABBCCDD"
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{})

	// Transmission should be inserted (even if advert is invalid)
	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Errorf("transmission should be inserted even with corrupted advert, got %d", count)
	}

	// But no node should be created
	store.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	if count != 0 {
		t.Error("corrupted advert should not create a node")
	}
}

func TestHandleMessageNoObserverID(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	// Topic with only 1 part — no observer
	msg := &mockMessage{
		topic:   "packets",
		payload: []byte(`{"raw":"` + rawHex + `","origin":"obs1"}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{})

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Errorf("count=%d, want 1", count)
	}
	// No observer should be upserted since observerID is empty
	store.db.QueryRow("SELECT COUNT(*) FROM observers").Scan(&count)
	if count != 0 {
		t.Error("no observer should be created when observerID is empty")
	}
}

func TestHandleMessageSNRNotFloat(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	// SNR as a string value — should not parse as float
	payload := []byte(`{"raw":"` + rawHex + `","SNR":"bad","RSSI":"bad"}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}
	handleMessage(store, "test", source, msg, nil, &Config{})

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 1 {
		t.Error("should still insert even with bad SNR/RSSI")
	}
}

func TestHandleMessageOriginExtraction(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `","origin":"MyOrigin"}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}
	handleMessage(store, "test", source, msg, nil, &Config{})

	// Verify origin was extracted to observer name
	var name string
	store.db.QueryRow("SELECT name FROM observers WHERE id = 'obs1'").Scan(&name)
	if name != "MyOrigin" {
		t.Errorf("observer name=%s, want MyOrigin", name)
	}
}

func TestHandleMessagePanicRecovery(t *testing.T) {
	// Close the store to cause panics on prepared statement use
	store := newTestStore(t)
	store.Close()

	source := MQTTSource{Name: "test"}
	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}

	// Should not panic — the defer/recover should catch it
	handleMessage(store, "test", source, msg, nil, &Config{})
}

func TestHandleMessageStatusOriginFallback(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	// Status topic without origin field
	msg := &mockMessage{
		topic:   "meshcore/SJC/obs1/status",
		payload: []byte(`{"type":"status"}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{})

	var name string
	err := store.db.QueryRow("SELECT name FROM observers WHERE id = 'obs1'").Scan(&name)
	if err != nil {
		t.Fatal(err)
	}
	// firstNonEmpty with empty name should use observerID as fallback in log
	// The observer should still be inserted
}

func TestEpochToISO(t *testing.T) {
	// epoch 0 → 1970-01-01
	iso := epochToISO(0)
	if iso != "1970-01-01T00:00:00.000Z" {
		t.Errorf("epochToISO(0) = %s, want 1970-01-01T00:00:00.000Z", iso)
	}

	// Known timestamp
	iso2 := epochToISO(1700000000)
	if iso2 == "" {
		t.Error("epochToISO should return non-empty string")
	}
}

func TestAdvertRole(t *testing.T) {
	tests := []struct {
		name  string
		flags *AdvertFlags
		want  string
	}{
		{"repeater", &AdvertFlags{Repeater: true}, "repeater"},
		{"room", &AdvertFlags{Room: true}, "room"},
		{"sensor", &AdvertFlags{Sensor: true}, "sensor"},
		{"companion (default)", &AdvertFlags{Chat: true}, "companion"},
		{"companion (no flags)", &AdvertFlags{}, "companion"},
		{"repeater takes priority", &AdvertFlags{Repeater: true, Room: true}, "repeater"},
		{"room before sensor", &AdvertFlags{Room: true, Sensor: true}, "room"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := advertRole(tt.flags)
			if got != tt.want {
				t.Errorf("advertRole(%+v) = %s, want %s", tt.flags, got, tt.want)
			}
		})
	}
}

func TestDeriveHashtagChannelKey(t *testing.T) {
	// Test vectors validated against Node.js server-helpers.js
	tests := []struct {
		name string
		want string
	}{
		{"#General", "649af2cab73ed5a890890a5485a0c004"},
		{"#test", "9cd8fcf22a47333b591d96a2b848b73f"},
		{"#MeshCore", "dcf73f393fa217f6b28fcec6ffc411ad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveHashtagChannelKey(tt.name)
			if got != tt.want {
				t.Errorf("deriveHashtagChannelKey(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}

	// Deterministic
	k1 := deriveHashtagChannelKey("#foo")
	k2 := deriveHashtagChannelKey("#foo")
	if k1 != k2 {
		t.Error("deriveHashtagChannelKey should be deterministic")
	}

	// Returns 32-char hex string (16 bytes)
	if len(k1) != 32 {
		t.Errorf("key length = %d, want 32", len(k1))
	}

	// Different inputs → different keys
	k3 := deriveHashtagChannelKey("#bar")
	if k1 == k3 {
		t.Error("different inputs should produce different keys")
	}
}

func TestLoadChannelKeysMergePriority(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// Create a rainbow file with two keys: #rainbow (unique) and #override (to be overridden)
	rainbowPath := filepath.Join(dir, "channel-rainbow.json")
	t.Setenv("CHANNEL_KEYS_PATH", rainbowPath)
	rainbow := map[string]string{
		"#rainbow":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"#override": "rainbow_value_should_be_overridden",
	}
	rainbowJSON, err := json.Marshal(rainbow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rainbowPath, rainbowJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		HashChannels: []string{"General", "#override"},
		ChannelKeys:  map[string]string{"#override": "explicit_wins"},
	}

	keys := loadChannelKeys(cfg, cfgPath)

	// Rainbow key loaded
	if keys["#rainbow"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("rainbow key missing or wrong: %q", keys["#rainbow"])
	}

	// HashChannels derived #General
	expected := deriveHashtagChannelKey("#General")
	if keys["#General"] != expected {
		t.Errorf("#General = %q, want %q (derived)", keys["#General"], expected)
	}

	// Explicit config wins over both rainbow and derived
	if keys["#override"] != "explicit_wins" {
		t.Errorf("#override = %q, want explicit_wins", keys["#override"])
	}
}

func TestLoadChannelKeysHashChannelsNormalization(t *testing.T) {
	t.Setenv("CHANNEL_KEYS_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := &Config{
		HashChannels: []string{
			"NoPound",       // should become #NoPound
			"#HasPound",     // stays #HasPound
			"  Spaced  ",   // trimmed → #Spaced
			"",              // skipped
		},
	}

	keys := loadChannelKeys(cfg, cfgPath)

	if _, ok := keys["#NoPound"]; !ok {
		t.Error("should derive key for #NoPound (auto-prefixed)")
	}
	if _, ok := keys["#HasPound"]; !ok {
		t.Error("should derive key for #HasPound")
	}
	if _, ok := keys["#Spaced"]; !ok {
		t.Error("should derive key for #Spaced (trimmed)")
	}
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}
}

func TestLoadChannelKeysSkipExplicit(t *testing.T) {
	t.Setenv("CHANNEL_KEYS_PATH", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := &Config{
		HashChannels: []string{"General"},
		ChannelKeys:  map[string]string{"#General": "my_explicit_key"},
	}

	keys := loadChannelKeys(cfg, cfgPath)

	// Explicit key should win — hashChannels derivation should be skipped
	if keys["#General"] != "my_explicit_key" {
		t.Errorf("#General = %q, want my_explicit_key", keys["#General"])
	}
}

// --- Bug #321: SNR/RSSI case-insensitive fallback ---

func TestHandleMessageWithLowercaseSNRRSSI(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `","snr":5.5,"rssi":-102}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}

	handleMessage(store, "test", source, msg, nil, &Config{})

	var snr, rssi *float64
	store.db.QueryRow("SELECT snr, rssi FROM observations LIMIT 1").Scan(&snr, &rssi)
	if snr == nil || *snr != 5.5 {
		t.Errorf("snr=%v, want 5.5 (lowercase key)", snr)
	}
	if rssi == nil || *rssi != -102 {
		t.Errorf("rssi=%v, want -102 (lowercase key)", rssi)
	}
}

func TestHandleMessageSNRRSSIUppercaseWins(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	// Both uppercase and lowercase present — uppercase should take precedence
	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `","SNR":7.2,"snr":1.0,"RSSI":-95,"rssi":-50}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}

	handleMessage(store, "test", source, msg, nil, &Config{})

	var snr, rssi *float64
	store.db.QueryRow("SELECT snr, rssi FROM observations LIMIT 1").Scan(&snr, &rssi)
	if snr == nil || *snr != 7.2 {
		t.Errorf("snr=%v, want 7.2 (uppercase should take precedence)", snr)
	}
	if rssi == nil || *rssi != -95 {
		t.Errorf("rssi=%v, want -95 (uppercase should take precedence)", rssi)
	}
}

func TestHandleMessageNoSNRRSSI(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test"}

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	payload := []byte(`{"raw":"` + rawHex + `"}`)
	msg := &mockMessage{topic: "meshcore/SJC/obs1/packets", payload: payload}

	handleMessage(store, "test", source, msg, nil, &Config{})

	var snr, rssi *float64
	store.db.QueryRow("SELECT snr, rssi FROM observations LIMIT 1").Scan(&snr, &rssi)
	if snr != nil {
		t.Errorf("snr should be nil when not present, got %v", *snr)
	}
	if rssi != nil {
		t.Errorf("rssi should be nil when not present, got %v", *rssi)
	}
}

func TestStripUnitSuffix(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"-110dBm", "-110"},
		{"-110DBM", "-110"},
		{"5.5dB", "5.5"},
		{"100mW", "100"},
		{"1.5km", "1.5"},
		{"500m", "500"},
		{"10mi", "10"},
		{"42", "42"},
		{"", ""},
		{"hello", "hello"},
	}
	for _, tt := range tests {
		got := stripUnitSuffix(tt.input)
		if got != tt.want {
			t.Errorf("stripUnitSuffix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToFloat64WithUnits(t *testing.T) {
	tests := []struct {
		input interface{}
		want  float64
		ok    bool
	}{
		{"-110dBm", -110.0, true},
		{"5.5dB", 5.5, true},
		{"100mW", 100.0, true},
		{"-85.3dBm", -85.3, true},
		{"42", 42.0, true},
		{"not_a_number", 0, false},
	}
	for _, tt := range tests {
		got, ok := toFloat64(tt.input)
		if ok != tt.ok {
			t.Errorf("toFloat64(%v) ok=%v, want %v", tt.input, ok, tt.ok)
		}
		if ok && got != tt.want {
			t.Errorf("toFloat64(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// TestIATAFilterDoesNotDropStatusMessages verifies that status messages from
// out-of-region observers are still processed (noise_floor, battery, etc.)
// even when an IATA filter is configured for packet data.
func TestIATAFilterDoesNotDropStatusMessages(t *testing.T) {
	store := newTestStore(t)
	source := MQTTSource{Name: "test", IATAFilter: []string{"SJC"}}

	// BFL observer sends a status message with noise_floor — outside the IATA filter.
	msg := &mockMessage{
		topic:   "meshcore/BFL/bfl-obs1/status",
		payload: []byte(`{"origin":"BFLObserver","stats":{"noise_floor":-105.0}}`),
	}
	handleMessage(store, "test", source, msg, nil, &Config{})

	var name string
	var noiseFloor *float64
	err := store.db.QueryRow("SELECT name, noise_floor FROM observers WHERE id = 'bfl-obs1'").Scan(&name, &noiseFloor)
	if err != nil {
		t.Fatalf("observer not found after status from out-of-region observer: %v", err)
	}
	if name != "BFLObserver" {
		t.Errorf("name=%q, want BFLObserver", name)
	}
	if noiseFloor == nil || *noiseFloor != -105.0 {
		t.Errorf("noise_floor=%v, want -105.0 — status message was dropped by IATA filter when it should not be", noiseFloor)
	}

	// Verify that a packet from BFL is still filtered.
	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	pktMsg := &mockMessage{
		topic:   "meshcore/BFL/bfl-obs1/packets",
		payload: []byte(`{"raw":"` + rawHex + `"}`),
	}
	handleMessage(store, "test", source, pktMsg, nil, &Config{})
	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count != 0 {
		t.Error("packet from out-of-region BFL should still be filtered by IATA")
	}
}

func TestLoadRegionKeys(t *testing.T) {
	cfg := &Config{HashRegions: []string{"#belgium", "eu", "  #Test  ", "", "#belgium"}}
	keys := loadRegionKeys(cfg)

	// Deduplication + normalization
	if len(keys) != 3 {
		t.Fatalf("len(keys) = %d, want 3", len(keys))
	}
	// "#belgium" key = SHA256("#belgium")[:16]
	h := sha256.Sum256([]byte("#belgium"))
	want := h[:16]
	if got := keys["#belgium"]; !bytes.Equal(got, want) {
		t.Errorf("#belgium key mismatch: got %x, want %x", got, want)
	}
	// "eu" should be normalized to "#eu"
	if _, ok := keys["#eu"]; !ok {
		t.Error("expected #eu key")
	}
	// "  #Test  " should be normalized to "#Test"
	if _, ok := keys["#Test"]; !ok {
		t.Error("expected #Test key")
	}
}

func TestMatchScope(t *testing.T) {
	// Build a known Code1 for region "#test" and payload type 5, payload "hello"
	name := "#test"
	h := sha256.Sum256([]byte(name))
	key := h[:16]

	payloadType := byte(0x05)
	payloadRaw := []byte("hello")

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte{payloadType})
	mac.Write(payloadRaw)
	hmacBytes := mac.Sum(nil)
	code := uint16(hmacBytes[0]) | uint16(hmacBytes[1])<<8
	if code == 0 {
		code = 1
	} else if code == 0xFFFF {
		code = 0xFFFE
	}
	code1Bytes := [2]byte{byte(code & 0xFF), byte(code >> 8)}
	code1 := strings.ToUpper(hex.EncodeToString(code1Bytes[:]))

	regionKeys := map[string][]byte{name: key}

	got := matchScope(regionKeys, payloadType, payloadRaw, code1)
	if got != name {
		t.Errorf("matchScope = %q, want %q", got, name)
	}

	// Unscoped (Code1 = 0000) → empty
	if got := matchScope(regionKeys, payloadType, payloadRaw, "0000"); got != "" {
		t.Errorf("unscoped: matchScope = %q, want empty", got)
	}

	// Scoped but no match → empty string sentinel
	if got := matchScope(regionKeys, payloadType, payloadRaw, "BEEF"); got != "" {
		t.Errorf("no match: matchScope = %q, want empty", got)
	}
}
