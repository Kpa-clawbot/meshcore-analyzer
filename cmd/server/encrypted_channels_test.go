package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// seedEncryptedChannelData adds undecryptable GRP_TXT packets to the test DB.
func seedEncryptedChannelData(t *testing.T, db *DB) {
	t.Helper()
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	// Two encrypted GRP_TXT packets on channel hash "A1B2"
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('EE01', 'enc_hash_001', ?, 1, 5, '{"type":"GRP_TXT","channelHashHex":"A1B2","decryptionStatus":"no_key"}', 'enc_A1B2')`, recent)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES ('EE02', 'enc_hash_002', ?, 1, 5, '{"type":"GRP_TXT","channelHashHex":"A1B2","decryptionStatus":"no_key"}', 'enc_A1B2')`, recent)

	// Observations for both
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES ((SELECT id FROM transmissions WHERE hash='enc_hash_001'), 1, 10.0, -90, '[]', ?)`, recentEpoch)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES ((SELECT id FROM transmissions WHERE hash='enc_hash_002'), 1, 10.0, -90, '[]', ?)`, recentEpoch)
}

func TestGetEncryptedChannels(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	seedEncryptedChannelData(t, db)

	channels, err := db.GetEncryptedChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected 1 encrypted channel, got %d", len(channels))
	}
	ch := channels[0]
	if ch["hash"] != "enc_A1B2" {
		t.Errorf("expected hash enc_A1B2, got %v", ch["hash"])
	}
	if ch["encrypted"] != true {
		t.Errorf("expected encrypted=true, got %v", ch["encrypted"])
	}
	if ch["messageCount"] != 2 {
		t.Errorf("expected messageCount=2, got %v", ch["messageCount"])
	}
}

func TestChannelsAPIExcludesEncrypted(t *testing.T) {
	_, router := setupTestServer(t)
	// Seed encrypted data into the server's DB
	// setupTestServer uses seedTestData which has no encrypted packets,
	// so default /api/channels should NOT include encrypted channels.
	req := httptest.NewRequest("GET", "/api/channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	channels := body["channels"].([]interface{})

	for _, ch := range channels {
		m := ch.(map[string]interface{})
		if enc, ok := m["encrypted"]; ok && enc == true {
			t.Errorf("default /api/channels should not include encrypted channels, found: %v", m["hash"])
		}
	}
}

func TestChannelsAPIIncludesEncryptedWithParam(t *testing.T) {
	srv, router := setupTestServer(t)
	// Add encrypted data to the server's DB
	seedEncryptedChannelData(t, srv.db)
	// Reload store so in-memory also has the data
	store := NewPacketStore(srv.db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store

	req := httptest.NewRequest("GET", "/api/channels?includeEncrypted=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	channels := body["channels"].([]interface{})

	foundEncrypted := false
	for _, ch := range channels {
		m := ch.(map[string]interface{})
		if enc, ok := m["encrypted"]; ok && enc == true {
			foundEncrypted = true
			break
		}
	}
	if !foundEncrypted {
		t.Error("expected encrypted channels with includeEncrypted=true, found none")
	}
}

func TestChannelMessagesExcludesEncrypted(t *testing.T) {
	srv, router := setupTestServer(t)
	seedEncryptedChannelData(t, srv.db)
	store := NewPacketStore(srv.db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store

	// Request messages for the encrypted channel — should return empty
	req := httptest.NewRequest("GET", "/api/channels/enc_A1B2/messages", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	messages, ok := body["messages"].([]interface{})
	if !ok {
		// messages might be null/missing — that's fine, means no messages
		return
	}
	// Encrypted messages should not be returned as readable messages
	for _, msg := range messages {
		m := msg.(map[string]interface{})
		if text, ok := m["text"].(string); ok && text != "" {
			t.Errorf("encrypted channel should not return readable messages, got text: %s", text)
		}
	}
}
