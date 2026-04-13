package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
)

func TestDeriveHashtagKey(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"#General", "649af2cab73ed5a890890a5485a0c004"},
		{"#test", "9cd8fcf22a47333b591d96a2b848b73f"},
		{"#MeshCore", "dcf73f393fa217f6b28fcec6ffc411ad"},
	}
	for _, tt := range tests {
		got := deriveHashtagKey(tt.name)
		if got != tt.want {
			t.Errorf("deriveHashtagKey(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestChannelKeyManager(t *testing.T) {
	m := NewChannelKeyManager()

	// Add keys
	m.AddKey("#test", "abc123")
	m.AddKey("#other", "def456")

	keys := m.GetKeys()
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if keys["#test"] != "abc123" {
		t.Errorf("expected #test key abc123, got %s", keys["#test"])
	}

	// Remove key
	existed := m.RemoveKey("#test")
	if !existed {
		t.Error("expected RemoveKey to return true")
	}
	existed = m.RemoveKey("#nonexistent")
	if existed {
		t.Error("expected RemoveKey to return false for nonexistent key")
	}

	keys = m.GetKeys()
	if len(keys) != 1 {
		t.Fatalf("expected 1 key after removal, got %d", len(keys))
	}
}

func TestUserChannelKeysPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create the DB file with minimum schema
	createTestDB(t, dbPath)

	// Ensure table
	if err := ensureUserChannelKeysTable(dbPath); err != nil {
		t.Fatalf("ensureUserChannelKeysTable: %v", err)
	}

	// Save a key
	key := UserChannelKey{Name: "#test", KeyHex: "abc123", Source: "hashtag"}
	if err := saveUserChannelKey(dbPath, key); err != nil {
		t.Fatalf("saveUserChannelKey: %v", err)
	}

	// Load keys via read-only connection
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	keys, err := loadUserChannelKeys(db)
	if err != nil {
		t.Fatalf("loadUserChannelKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].Name != "#test" || keys[0].KeyHex != "abc123" || keys[0].Source != "hashtag" {
		t.Errorf("unexpected key: %+v", keys[0])
	}

	// Delete key
	if err := deleteUserChannelKey(dbPath, "#test"); err != nil {
		t.Fatalf("deleteUserChannelKey: %v", err)
	}

	keys, err = loadUserChannelKeys(db)
	if err != nil {
		t.Fatalf("loadUserChannelKeys after delete: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(keys))
	}
}

func createTestDB(t *testing.T, dbPath string) {
	t.Helper()
	rw, err := openRW(dbPath)
	if err != nil {
		t.Fatalf("openRW: %v", err)
	}
	defer rw.Close()

	// Minimal schema for the server's OpenDB to work
	_, err = rw.Exec(`
		CREATE TABLE IF NOT EXISTS transmissions (
			id INTEGER PRIMARY KEY,
			raw_hex TEXT NOT NULL,
			hash TEXT,
			first_seen TEXT,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT
		);
		CREATE TABLE IF NOT EXISTS observations (
			id INTEGER PRIMARY KEY,
			transmission_id INTEGER,
			observer_idx INTEGER,
			timestamp TEXT,
			snr REAL,
			rssi REAL,
			hops INTEGER,
			path_json TEXT
		);
		CREATE TABLE IF NOT EXISTS nodes (
			public_key TEXT PRIMARY KEY,
			short_name TEXT,
			long_name TEXT,
			role TEXT,
			lat REAL,
			lon REAL,
			last_seen TEXT,
			advert_count INTEGER DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS observers (
			id INTEGER PRIMARY KEY,
			name TEXT,
			iata TEXT
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
}

func TestHandleAddChannelKeyAPI(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	createTestDB(t, dbPath)
	if err := ensureUserChannelKeysTable(dbPath); err != nil {
		t.Fatalf("ensureUserChannelKeysTable: %v", err)
	}

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	srv := NewServer(db, &Config{}, nil)

	// Test adding a hashtag channel
	body := `{"name":"#TestChannel"}`
	req := httptest.NewRequest("POST", "/api/channels/keys", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleAddChannelKey(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["name"] != "#TestChannel" {
		t.Errorf("expected name #TestChannel, got %v", resp["name"])
	}
	if resp["source"] != "hashtag" {
		t.Errorf("expected source hashtag, got %v", resp["source"])
	}
	expectedKey := deriveHashtagKey("#TestChannel")
	if resp["key"] != expectedKey {
		t.Errorf("expected key %s, got %v", expectedKey, resp["key"])
	}

	// Verify it's in the runtime key manager
	keys := srv.channelKeys.GetKeys()
	if keys["#TestChannel"] != expectedKey {
		t.Errorf("key not in runtime manager")
	}

	// Verify persistence
	loaded, err := loadUserChannelKeys(db)
	if err != nil {
		t.Fatalf("loadUserChannelKeys: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Name != "#TestChannel" {
		t.Errorf("expected persisted key, got %+v", loaded)
	}
}

func TestHandleAddChannelKeyAutoPrefix(t *testing.T) {
	srv := NewServer(nil, &Config{}, nil)

	body := `{"name":"NoHash"}`
	req := httptest.NewRequest("POST", "/api/channels/keys", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleAddChannelKey(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["name"] != "#NoHash" {
		t.Errorf("expected auto-prefixed name #NoHash, got %v", resp["name"])
	}
}

func TestHandleAddChannelKeyValidation(t *testing.T) {
	srv := NewServer(nil, &Config{}, nil)

	// Empty name
	body := `{"name":""}`
	req := httptest.NewRequest("POST", "/api/channels/keys", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	srv.handleAddChannelKey(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for empty name, got %d", w.Code)
	}

	// Name too long
	body = `{"name":"#` + "abcdefghijklmnopqrstuvwxyz0123456" + `"}`
	req = httptest.NewRequest("POST", "/api/channels/keys", bytes.NewBufferString(body))
	w = httptest.NewRecorder()
	srv.handleAddChannelKey(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for long name, got %d", w.Code)
	}

	// Invalid key length
	body = `{"name":"#test","key":"abc"}`
	req = httptest.NewRequest("POST", "/api/channels/keys", bytes.NewBufferString(body))
	w = httptest.NewRecorder()
	srv.handleAddChannelKey(w, req)
	if w.Code != 400 {
		t.Errorf("expected 400 for short key, got %d", w.Code)
	}
}

func TestHandleListChannelKeys(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	createTestDB(t, dbPath)
	ensureUserChannelKeysTable(dbPath)
	saveUserChannelKey(dbPath, UserChannelKey{Name: "#a", KeyHex: "aaaa", Source: "hashtag"})

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	srv := NewServer(db, &Config{}, nil)
	req := httptest.NewRequest("GET", "/api/channels/keys", nil)
	w := httptest.NewRecorder()
	srv.handleListChannelKeys(w, req)

	var resp struct {
		Keys []UserChannelKey `json:"keys"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Keys) != 1 || resp.Keys[0].Name != "#a" {
		t.Errorf("unexpected keys response: %+v", resp)
	}
}

func TestHandleDeleteChannelKey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	createTestDB(t, dbPath)
	ensureUserChannelKeysTable(dbPath)
	saveUserChannelKey(dbPath, UserChannelKey{Name: "#del", KeyHex: "bbbb", Source: "hashtag"})

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	srv := NewServer(db, &Config{}, nil)
	srv.channelKeys.AddKey("#del", "bbbb")

	// Use mux to inject path vars
	router := mux.NewRouter()
	router.HandleFunc("/api/channels/keys/{name}", srv.handleDeleteChannelKey).Methods("DELETE")

	req := httptest.NewRequest("DELETE", "/api/channels/keys/del", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify removed from runtime
	keys := srv.channelKeys.GetKeys()
	if _, exists := keys["#del"]; exists {
		t.Error("key should have been removed from runtime manager")
	}

	// Verify removed from DB
	loaded, _ := loadUserChannelKeys(db)
	if len(loaded) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(loaded))
	}
}

func TestDecryptChannelMessageServer(t *testing.T) {
	// Verify key derivation matches ingestor
	key := deriveHashtagKey("#General")
	if key != "649af2cab73ed5a890890a5485a0c004" {
		t.Fatalf("unexpected #General key: %s", key)
	}
	// Verify the function handles invalid inputs gracefully
	_, err := decryptChannelMessage("", "aabb", key)
	if err == nil {
		t.Error("expected error for empty ciphertext")
	}
	_, err = decryptChannelMessage("aabbccdd", "", key)
	if err == nil {
		t.Error("expected error for empty MAC")
	}
	_, err = decryptChannelMessage("aabbccdd", "aabb", "short")
	if err == nil {
		t.Error("expected error for invalid key")
	}
}

// Ensure the test fixture DB path resolver works
func TestFixtureDBExists(t *testing.T) {
	// This just verifies our test helper creates a usable DB
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	createTestDB(t, dbPath)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("test DB was not created")
	}
}
