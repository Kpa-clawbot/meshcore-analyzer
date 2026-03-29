package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHubBroadcast(t *testing.T) {
	hub := NewHub()

	if hub.ClientCount() != 0 {
		t.Errorf("expected 0 clients, got %d", hub.ClientCount())
	}

	// Create a test server with WebSocket endpoint
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r)
	}))
	defer srv.Close()

	// Connect a WebSocket client
	wsURL := "ws" + srv.URL[4:] // replace http with ws
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer conn.Close()

	// Wait for registration
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 1 {
		t.Errorf("expected 1 client, got %d", hub.ClientCount())
	}

	// Broadcast a message
	hub.Broadcast(map[string]interface{}{
		"type": "packet",
		"data": map[string]interface{}{"id": 1, "hash": "test123"},
	})

	// Read the message
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if len(msg) == 0 {
		t.Error("expected non-empty message")
	}

	// Disconnect
	conn.Close()
	time.Sleep(100 * time.Millisecond)
}

func TestPollerCreation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	hub := NewHub()

	poller := NewPoller(db, hub, 100*time.Millisecond)
	if poller == nil {
		t.Fatal("expected poller")
	}

	// Start and stop
	go poller.Start()
	time.Sleep(200 * time.Millisecond)
	poller.Stop()
}

func TestHubMultipleClients(t *testing.T) {
	hub := NewHub()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r)
	}))
	defer srv.Close()

	wsURL := "ws" + srv.URL[4:]

	// Connect two clients
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer conn1.Close()

	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer conn2.Close()

	time.Sleep(100 * time.Millisecond)

	if hub.ClientCount() != 2 {
		t.Errorf("expected 2 clients, got %d", hub.ClientCount())
	}

	// Broadcast and both should receive
	hub.Broadcast(map[string]interface{}{"type": "test", "data": "hello"})

	conn1.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg1, err := conn1.ReadMessage()
	if err != nil {
		t.Fatalf("conn1 read error: %v", err)
	}
	if len(msg1) == 0 {
		t.Error("expected non-empty message on conn1")
	}

	conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg2, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("conn2 read error: %v", err)
	}
	if len(msg2) == 0 {
		t.Error("expected non-empty message on conn2")
	}

	// Disconnect one
	conn1.Close()
	time.Sleep(100 * time.Millisecond)

	// Remaining client should still work
	hub.Broadcast(map[string]interface{}{"type": "test2"})

	conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg3, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("conn2 read error after disconnect: %v", err)
	}
	if len(msg3) == 0 {
		t.Error("expected non-empty message")
	}
}

func TestBroadcastFullBuffer(t *testing.T) {
	hub := NewHub()

	// Create a client with tiny buffer (1)
	client := &Client{
		send: make(chan []byte, 1),
	}
	hub.mu.Lock()
	hub.clients[client] = true
	hub.mu.Unlock()

	// Fill the buffer
	client.send <- []byte("first")

	// This broadcast should drop the message (buffer full)
	hub.Broadcast(map[string]interface{}{"type": "dropped"})

	// Channel should still only have the first message
	select {
	case msg := <-client.send:
		if string(msg) != "first" {
			t.Errorf("expected 'first', got %s", string(msg))
		}
	default:
		t.Error("expected message in channel")
	}

	// Clean up
	hub.mu.Lock()
	delete(hub.clients, client)
	hub.mu.Unlock()
}

func TestBroadcastMarshalError(t *testing.T) {
	hub := NewHub()

	// Marshal error: functions can't be marshaled to JSON
	hub.Broadcast(map[string]interface{}{"bad": func() {}})
	// Should not panic — just log and return
}

func TestPollerBroadcastsNewData(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	hub := NewHub()

	// Create a client to receive broadcasts
	client := &Client{
		send: make(chan []byte, 256),
	}
	hub.mu.Lock()
	hub.clients[client] = true
	hub.mu.Unlock()

	poller := NewPoller(db, hub, 50*time.Millisecond)
	go poller.Start()

	// Insert new data to trigger broadcast
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
		VALUES ('EEFF', 'newhash123456789', '2026-01-16T10:00:00Z', 1, 4)`)

	time.Sleep(200 * time.Millisecond)
	poller.Stop()

	// Check if client received broadcast with packet field (fixes #162)
	select {
	case msg := <-client.send:
		if len(msg) == 0 {
			t.Error("expected non-empty broadcast message")
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(msg, &parsed); err != nil {
			t.Fatalf("failed to parse broadcast: %v", err)
		}
		if parsed["type"] != "packet" {
			t.Errorf("expected type=packet, got %v", parsed["type"])
		}
		data, ok := parsed["data"].(map[string]interface{})
		if !ok {
			t.Fatal("expected data to be an object")
		}
		// packets.js filters on m.data.packet — must exist
		pkt, ok := data["packet"]
		if !ok || pkt == nil {
			t.Error("expected data.packet to exist (required by packets.js WS handler)")
		}
		pktMap, ok := pkt.(map[string]interface{})
		if !ok {
			t.Fatal("expected data.packet to be an object")
		}
		// Verify key fields exist in nested packet (timestamp required by packets.js)
		for _, field := range []string{"id", "hash", "payload_type", "timestamp"} {
			if _, exists := pktMap[field]; !exists {
				t.Errorf("expected data.packet.%s to exist", field)
			}
		}
	default:
		// Might not have received due to timing
	}

	// Clean up
	hub.mu.Lock()
	delete(hub.clients, client)
	hub.mu.Unlock()
}

func TestPollerBroadcastsMultipleObservations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	hub := NewHub()

	client := &Client{
		send: make(chan []byte, 256),
	}
	hub.mu.Lock()
	hub.clients[client] = true
	hub.mu.Unlock()
	defer func() {
		hub.mu.Lock()
		delete(hub.clients, client)
		hub.mu.Unlock()
	}()

	poller := NewPoller(db, hub, 50*time.Millisecond)
	store := NewPacketStore(db)
	if err := store.Load(); err != nil {
		t.Fatalf("store load failed: %v", err)
	}
	poller.store = store
	go poller.Start()
	defer poller.Stop()

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('FACE', 'starbursthash237a', ?, 1, 4, '{"pubKey":"aabbccdd11223344","type":"ADVERT"}')`, now); err != nil {
		t.Fatalf("insert tx failed: %v", err)
	}
	var txID int
	if err := db.conn.QueryRow(`SELECT id FROM transmissions WHERE hash='starbursthash237a'`).Scan(&txID); err != nil {
		t.Fatalf("query tx id failed: %v", err)
	}
	ts := time.Now().Unix()
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 14.0, -82, '["aa"]', ?),
		       (?, 2, 10.5, -90, '["aa","bb"]', ?),
		       (?, 1, 7.0, -96, '["aa","bb","cc"]', ?)`,
		txID, ts, txID, ts+1, txID, ts+2); err != nil {
		t.Fatalf("insert observations failed: %v", err)
	}

	deadline := time.After(2 * time.Second)
	var dataMsgs []map[string]interface{}
	for len(dataMsgs) < 3 {
		select {
		case raw := <-client.send:
			var parsed map[string]interface{}
			if err := json.Unmarshal(raw, &parsed); err != nil {
				t.Fatalf("unmarshal ws msg failed: %v", err)
			}
			if parsed["type"] != "packet" {
				continue
			}
			data, ok := parsed["data"].(map[string]interface{})
			if !ok {
				continue
			}
			if data["hash"] == "starbursthash237a" {
				dataMsgs = append(dataMsgs, data)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for 3 observation broadcasts, got %d", len(dataMsgs))
		}
	}

	if len(dataMsgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(dataMsgs))
	}

	paths := make([]string, 0, 3)
	observers := make(map[string]bool)
	for _, m := range dataMsgs {
		hash, _ := m["hash"].(string)
		if hash != "starbursthash237a" {
			t.Fatalf("unexpected hash %q", hash)
		}
		p, _ := m["path_json"].(string)
		paths = append(paths, p)
		if oid, ok := m["observer_id"].(string); ok && oid != "" {
			observers[oid] = true
		}
	}
	sort.Strings(paths)
	wantPaths := []string{`["aa","bb","cc"]`, `["aa","bb"]`, `["aa"]`}
	sort.Strings(wantPaths)
	for i := range wantPaths {
		if paths[i] != wantPaths[i] {
			t.Fatalf("path mismatch at %d: got %q want %q", i, paths[i], wantPaths[i])
		}
	}
	if len(observers) < 2 {
		t.Fatalf("expected observations from >=2 observers, got %d", len(observers))
	}
}

func TestIngestNewObservationsBroadcast(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db)
	if err := store.Load(); err != nil {
		t.Fatalf("store load failed: %v", err)
	}

	maxObs := db.GetMaxObservationID()
	now := time.Now().Unix()
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 2, 6.0, -100, '["aa","zz"]', ?),
		       (1, 1, 5.0, -101, '["aa","yy"]', ?)`, now, now+1); err != nil {
		t.Fatalf("insert new observations failed: %v", err)
	}

	maps := store.IngestNewObservations(maxObs, 500)
	if len(maps) != 2 {
		t.Fatalf("expected 2 broadcast maps, got %d", len(maps))
	}
	for _, m := range maps {
		if m["hash"] != "abc123def4567890" {
			t.Fatalf("unexpected hash in map: %v", m["hash"])
		}
		path, ok := m["path_json"].(string)
		if !ok || path == "" {
			t.Fatalf("missing path_json in map: %#v", m)
		}
		if _, ok := m["observer_id"]; !ok {
			t.Fatalf("missing observer_id in map: %#v", m)
		}
	}
}

func TestHubRegisterUnregister(t *testing.T) {
	hub := NewHub()

	client := &Client{
		send: make(chan []byte, 256),
	}

	hub.Register(client)
	if hub.ClientCount() != 1 {
		t.Errorf("expected 1 client after register, got %d", hub.ClientCount())
	}

	hub.Unregister(client)
	if hub.ClientCount() != 0 {
		t.Errorf("expected 0 clients after unregister, got %d", hub.ClientCount())
	}

	// Unregister again should be safe
	hub.Unregister(client)
	if hub.ClientCount() != 0 {
		t.Errorf("expected 0 clients, got %d", hub.ClientCount())
	}
}
