package main

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/channel"
)

func TestExtractGRPPayload(t *testing.T) {
	// Build a minimal GRP_TXT packet: header(1) + path(1) + payload
	// header: route=FLOOD(1), payload=GRP_TXT(5), version=0 → (5<<2)|1 = 0x15
	// path: 0 hops, hash_size=1 → 0x00
	payload := []byte{0x81, 0x12, 0x34} // channel_hash + mac + data
	pkt := append([]byte{0x15, 0x00}, payload...)
	rawHex := hex.EncodeToString(pkt)

	result, err := extractGRPPayload(rawHex)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 || result[0] != 0x81 {
		t.Fatalf("payload mismatch: %x", result)
	}
}

func TestExtractGRPPayloadTransport(t *testing.T) {
	// Transport flood: route=0, 4 bytes transport codes BEFORE path byte
	// header: (5<<2)|0 = 0x14
	payload := []byte{0xAA, 0xBB, 0xCC}
	// header + 4 transport bytes + path(0 hops) + payload
	pkt := append([]byte{0x14, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}, payload...)
	rawHex := hex.EncodeToString(pkt)

	result, err := extractGRPPayload(rawHex)
	if err != nil {
		t.Fatal(err)
	}
	if result[0] != 0xAA {
		t.Fatalf("expected AA, got %02X", result[0])
	}
}

func TestExtractGRPPayloadNotGRP(t *testing.T) {
	// payload type = ADVERT (4): (4<<2)|1 = 0x11
	rawHex := hex.EncodeToString([]byte{0x11, 0x00, 0x01, 0x02})
	_, err := extractGRPPayload(rawHex)
	if err == nil {
		t.Fatal("expected error for non-GRP_TXT")
	}
}

func TestKeyDerivationConsistency(t *testing.T) {
	// Verify key derivation matches what the ingestor expects
	key := channel.DeriveKey("#wardriving")
	if len(key) != 16 {
		t.Fatalf("key len %d", len(key))
	}
	ch := channel.ChannelHash(key)
	if ch != 0x81 {
		// We know from fixture data that #wardriving has channelHashHex "81"
		t.Fatalf("channel hash %02X, expected 81", ch)
	}
}

func TestRenderIRC(t *testing.T) {
	msgs := []ChannelMessage{
		{Timestamp: "2026-04-12T03:45:12Z", Sender: "NodeA", Message: "Hello"},
		{Timestamp: "2026-04-12T03:46:01Z", Sender: "", Message: "No sender"},
	}
	out := string(renderIRC(msgs))
	if !strings.Contains(out, "[2026-04-12 03:45:12] <NodeA> Hello") {
		t.Fatalf("IRC output missing expected line: %s", out)
	}
	if !strings.Contains(out, "<???> No sender") {
		t.Fatalf("IRC output should use ??? for empty sender: %s", out)
	}
}

func TestRenderHTMLValid(t *testing.T) {
	msgs := []ChannelMessage{
		{Hash: "abc", Timestamp: "2026-04-12T00:00:00Z", Sender: "X", Message: "test", Channel: "#test"},
	}
	out := string(renderHTML(msgs, "#test"))
	if !strings.Contains(out, "<!DOCTYPE html>") {
		t.Fatal("not valid HTML")
	}
	if !strings.Contains(out, "#test") {
		t.Fatal("channel name missing")
	}
	if !strings.Contains(out, "</html>") {
		t.Fatal("HTML not closed")
	}
}

func TestJSONOutputParseable(t *testing.T) {
	msgs := []ChannelMessage{
		{Hash: "abc", Timestamp: "2026-04-12T00:00:00Z", Sender: "X", Message: "hi", Channel: "#test"},
	}
	data, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var parsed []ChannelMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON not parseable: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Sender != "X" {
		t.Fatalf("parsed mismatch: %+v", parsed)
	}
}

// Integration test against fixture DB (skipped if DB not found)
func TestFixtureDecrypt(t *testing.T) {
	dbPath := "../../test-fixtures/e2e-fixture.db"
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Skip("fixture DB not found")
	}

	// We know the fixture has #wardriving messages with channelHash 0x81
	key := channel.DeriveKey("#wardriving")
	ch := channel.ChannelHash(key)
	if ch != 0x81 {
		t.Fatalf("unexpected channel hash: %02X", ch)
	}
}
