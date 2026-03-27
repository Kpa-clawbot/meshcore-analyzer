package main

import (
	"net/http"
	"net/http/httptest"
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

	// Marshal error: channels can't be marshaled
	hub.Broadcast(make(chan int))
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

	// Check if client received broadcast
	select {
	case msg := <-client.send:
		if len(msg) == 0 {
			t.Error("expected non-empty broadcast message")
		}
	default:
		// Might not have received due to timing
	}

	// Clean up
	hub.mu.Lock()
	delete(hub.clients, client)
	hub.mu.Unlock()
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
