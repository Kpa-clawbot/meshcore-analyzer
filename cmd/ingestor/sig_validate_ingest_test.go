package main

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// buildAdvertHex constructs a full ADVERT packet hex string.
// header(1) + pathByte(1) + pubkey(32) + timestamp(4) + signature(64) + appdata
func buildAdvertHex(pubKey ed25519.PublicKey, privKey ed25519.PrivateKey, timestamp uint32, appdata []byte) string {
	// Build signed message: pubkey(32) + timestamp(4 LE) + appdata
	msg := make([]byte, 32+4+len(appdata))
	copy(msg[0:32], pubKey)
	binary.LittleEndian.PutUint32(msg[32:36], timestamp)
	copy(msg[36:], appdata)

	sig := ed25519.Sign(privKey, msg)

	// Payload: pubkey(32) + timestamp(4) + signature(64) + appdata
	payload := make([]byte, 0, 100+len(appdata))
	payload = append(payload, pubKey...)
	ts := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts, timestamp)
	payload = append(payload, ts...)
	payload = append(payload, sig...)
	payload = append(payload, appdata...)

	// Header: ADVERT (0x04 << 2) | FLOOD (1) = 0x11, pathByte=0 (no hops)
	header := byte(0x11)
	pathByte := byte(0x00)

	pkt := append([]byte{header, pathByte}, payload...)
	return hex.EncodeToString(pkt)
}

// makeAppdata builds minimal appdata: flags(1) + name
func makeAppdata(name string) []byte {
	flags := byte(0x81) // hasName=true, type=companion(1)
	data := []byte{flags}
	data = append(data, []byte(name)...)
	data = append(data, 0x00) // null terminator
	return data
}

func TestSigValidation_ValidAdvertStored(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := OpenStoreWithInterval(dbPath, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pub, priv, _ := ed25519.GenerateKey(nil)
	appdata := makeAppdata("TestNode")
	rawHex := buildAdvertHex(pub, priv, 1700000000, appdata)

	source := MQTTSource{Name: "test"}
	msg := newMockMsg("meshcore/US/obs1/packet", `{"raw":"`+rawHex+`","origin":"TestObs"}`)
	cfg := &Config{}

	handleMessage(store, "test", source, msg, nil, nil, cfg)

	// Verify packet was stored
	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count)
	if count == 0 {
		t.Fatal("valid advert should be stored, got 0 transmissions")
	}
}

func TestSigValidation_TamperedSignatureDropped(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := OpenStoreWithInterval(dbPath, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pub, priv, _ := ed25519.GenerateKey(nil)
	appdata := makeAppdata("BadNode")
	rawHex := buildAdvertHex(pub, priv, 1700000000, appdata)

	// Tamper with signature (flip a byte in the signature area)
	// Signature starts at offset 2 (header+path) + 32 (pubkey) + 4 (timestamp) = 38
	// That's byte 38 in the packet, hex chars 76-77
	rawBytes := []byte(rawHex)
	if rawBytes[76] == '0' {
		rawBytes[76] = 'f'
	} else {
		rawBytes[76] = '0'
	}
	tamperedHex := string(rawBytes)

	source := MQTTSource{Name: "test"}
	msg := newMockMsg("meshcore/US/obs1/packet", `{"raw":"`+tamperedHex+`","origin":"TestObs"}`)
	cfg := &Config{}

	handleMessage(store, "test", source, msg, nil, nil, cfg)

	// Verify packet was NOT stored in transmissions
	var txCount int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&txCount)
	if txCount != 0 {
		t.Fatalf("tampered advert should be dropped, got %d transmissions", txCount)
	}

	// Verify it was recorded in dropped_packets
	var dropCount int
	store.db.QueryRow("SELECT COUNT(*) FROM dropped_packets").Scan(&dropCount)
	if dropCount == 0 {
		t.Fatal("tampered advert should be recorded in dropped_packets")
	}

	// Verify drop counter incremented
	if store.Stats.SignatureDrops.Load() != 1 {
		t.Fatalf("expected 1 signature drop, got %d", store.Stats.SignatureDrops.Load())
	}

	// Verify dropped_packets has correct fields
	var reason, nodeKey, nodeName, obsID string
	store.db.QueryRow("SELECT reason, node_pubkey, node_name, observer_id FROM dropped_packets LIMIT 1").Scan(&reason, &nodeKey, &nodeName, &obsID)
	if reason != "invalid signature" {
		t.Fatalf("expected reason 'invalid signature', got %q", reason)
	}
	if nodeKey == "" {
		t.Fatal("dropped packet should have node_pubkey")
	}
	if !strings.Contains(nodeName, "BadNode") {
		t.Fatalf("expected node_name to contain 'BadNode', got %q", nodeName)
	}
	if obsID != "obs1" {
		t.Fatalf("expected observer_id 'obs1', got %q", obsID)
	}
}

func TestSigValidation_TruncatedAppdataDropped(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := OpenStoreWithInterval(dbPath, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pub, priv, _ := ed25519.GenerateKey(nil)
	appdata := makeAppdata("TruncNode")
	rawHex := buildAdvertHex(pub, priv, 1700000000, appdata)

	// Sign was computed with full appdata. Now truncate the raw hex to remove
	// some appdata bytes, making the signature invalid.
	// Truncate last 4 hex chars (2 bytes of appdata)
	truncatedHex := rawHex[:len(rawHex)-4]

	source := MQTTSource{Name: "test"}
	msg := newMockMsg("meshcore/US/obs1/packet", `{"raw":"`+truncatedHex+`","origin":"TestObs"}`)
	cfg := &Config{}

	handleMessage(store, "test", source, msg, nil, nil, cfg)

	var txCount int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&txCount)
	if txCount != 0 {
		t.Fatalf("truncated advert should be dropped, got %d transmissions", txCount)
	}
}

func TestSigValidation_DisabledByConfig(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := OpenStoreWithInterval(dbPath, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pub, priv, _ := ed25519.GenerateKey(nil)
	appdata := makeAppdata("NoValNode")
	rawHex := buildAdvertHex(pub, priv, 1700000000, appdata)

	// Tamper with signature
	rawBytes := []byte(rawHex)
	if rawBytes[76] == '0' {
		rawBytes[76] = 'f'
	} else {
		rawBytes[76] = '0'
	}
	tamperedHex := string(rawBytes)

	source := MQTTSource{Name: "test"}
	msg := newMockMsg("meshcore/US/obs1/packet", `{"raw":"`+tamperedHex+`","origin":"TestObs"}`)
	falseVal := false
	cfg := &Config{ValidateSignatures: &falseVal}

	handleMessage(store, "test", source, msg, nil, nil, cfg)

	// With validation disabled, tampered packet should be stored
	var txCount int
	store.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&txCount)
	if txCount == 0 {
		t.Fatal("with validateSignatures=false, tampered advert should be stored")
	}
}

func TestSigValidation_DropCounterIncrements(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := OpenStoreWithInterval(dbPath, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pub, priv, _ := ed25519.GenerateKey(nil)
	source := MQTTSource{Name: "test"}
	cfg := &Config{}

	for i := 0; i < 3; i++ {
		appdata := makeAppdata("Node")
		rawHex := buildAdvertHex(pub, priv, uint32(1700000000+i), appdata)
		// Tamper
		rawBytes := []byte(rawHex)
		if rawBytes[76] == '0' {
			rawBytes[76] = 'f'
		} else {
			rawBytes[76] = '0'
		}
		msg := newMockMsg("meshcore/US/obs1/packet", `{"raw":"`+string(rawBytes)+`","origin":"Obs"}`)
		handleMessage(store, "test", source, msg, nil, nil, cfg)
	}

	if store.Stats.SignatureDrops.Load() != 3 {
		t.Fatalf("expected 3 signature drops, got %d", store.Stats.SignatureDrops.Load())
	}
}

func TestSigValidation_LogContainsFields(t *testing.T) {
	// This test verifies the dropped_packets row has all required fields
	dbPath := t.TempDir() + "/test.db"
	store, err := OpenStoreWithInterval(dbPath, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pub, priv, _ := ed25519.GenerateKey(nil)
	appdata := makeAppdata("LogTestNode")
	rawHex := buildAdvertHex(pub, priv, 1700000000, appdata)

	// Tamper
	rawBytes := []byte(rawHex)
	if rawBytes[76] == '0' {
		rawBytes[76] = 'f'
	} else {
		rawBytes[76] = '0'
	}

	source := MQTTSource{Name: "test"}
	msg := newMockMsg("meshcore/US/obs1/packet", `{"raw":"`+string(rawBytes)+`","origin":"MyObserver"}`)
	cfg := &Config{}

	handleMessage(store, "test", source, msg, nil, nil, cfg)

	var hash, reason, obsID, obsName, pubkey, nodeName string
	err = store.db.QueryRow("SELECT hash, reason, observer_id, observer_name, node_pubkey, node_name FROM dropped_packets LIMIT 1").
		Scan(&hash, &reason, &obsID, &obsName, &pubkey, &nodeName)
	if err != nil {
		t.Fatal(err)
	}

	if hash == "" {
		t.Error("dropped packet should have hash")
	}
	if reason != "invalid signature" {
		t.Errorf("expected reason 'invalid signature', got %q", reason)
	}
	if obsID != "obs1" {
		t.Errorf("expected observer_id 'obs1', got %q", obsID)
	}
	if obsName != "MyObserver" {
		t.Errorf("expected observer_name 'MyObserver', got %q", obsName)
	}
	if pubkey == "" {
		t.Error("dropped packet should have node_pubkey")
	}
	if !strings.Contains(nodeName, "LogTestNode") {
		t.Errorf("expected node_name containing 'LogTestNode', got %q", nodeName)
	}
}

func TestPruneDroppedPackets(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	store, err := OpenStoreWithInterval(dbPath, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Insert an old dropped packet
	store.db.Exec(`INSERT INTO dropped_packets (hash, reason, dropped_at) VALUES ('old', 'test', datetime('now', '-60 days'))`)
	store.db.Exec(`INSERT INTO dropped_packets (hash, reason, dropped_at) VALUES ('new', 'test', datetime('now'))`)

	n, err := store.PruneDroppedPackets(30)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}

	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM dropped_packets").Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 remaining, got %d", count)
	}
}

func TestShouldValidateSignatures_Default(t *testing.T) {
	cfg := &Config{}
	if !cfg.ShouldValidateSignatures() {
		t.Fatal("default should be true")
	}

	falseVal := false
	cfg2 := &Config{ValidateSignatures: &falseVal}
	if cfg2.ShouldValidateSignatures() {
		t.Fatal("explicit false should be false")
	}

	trueVal := true
	cfg3 := &Config{ValidateSignatures: &trueVal}
	if !cfg3.ShouldValidateSignatures() {
		t.Fatal("explicit true should be true")
	}
}

// newMockMsg creates a minimal mqtt.Message for testing.
func newMockMsg(topic, payload string) *mockMessage {
	return &mockMessage{topic: topic, payload: []byte(payload)}
}
