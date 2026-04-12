package main

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestTouchNodeLastSeen_UpdatesDB(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Insert a node with no last_seen
	db.conn.Exec("INSERT INTO nodes (public_key, name, role) VALUES (?, ?, ?)", "abc123", "relay1", "REPEATER")

	err := db.TouchNodeLastSeen("abc123", "2026-04-12T04:00:00Z")
	if err != nil {
		t.Fatalf("TouchNodeLastSeen returned error: %v", err)
	}

	var lastSeen sql.NullString
	db.conn.QueryRow("SELECT last_seen FROM nodes WHERE public_key = ?", "abc123").Scan(&lastSeen)
	if !lastSeen.Valid || lastSeen.String != "2026-04-12T04:00:00Z" {
		t.Fatalf("expected last_seen=2026-04-12T04:00:00Z, got %v", lastSeen)
	}
}

func TestTouchNodeLastSeen_DoesNotGoBackwards(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)",
		"abc123", "relay1", "REPEATER", "2026-04-12T05:00:00Z")

	// Try to set an older timestamp
	err := db.TouchNodeLastSeen("abc123", "2026-04-12T04:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var lastSeen string
	db.conn.QueryRow("SELECT last_seen FROM nodes WHERE public_key = ?", "abc123").Scan(&lastSeen)
	if lastSeen != "2026-04-12T05:00:00Z" {
		t.Fatalf("last_seen went backwards: got %s", lastSeen)
	}
}

func TestTouchNodeLastSeen_NonExistentNode(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Should not error for non-existent node
	err := db.TouchNodeLastSeen("nonexistent", "2026-04-12T04:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error for non-existent node: %v", err)
	}
}

func TestTouchRelayLastSeen_Debouncing(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec("INSERT INTO nodes (public_key, name, role) VALUES (?, ?, ?)", "relay1", "R1", "REPEATER")

	s := &PacketStore{
		db:              db,
		lastSeenTouched: make(map[string]time.Time),
	}

	pk := "relay1"
	tx := &StoreTx{
		ResolvedPath: []*string{&pk},
	}

	now := time.Now()
	s.touchRelayLastSeen(tx, now)

	// Verify it was written
	var lastSeen sql.NullString
	db.conn.QueryRow("SELECT last_seen FROM nodes WHERE public_key = ?", "relay1").Scan(&lastSeen)
	if !lastSeen.Valid {
		t.Fatal("expected last_seen to be set after first touch")
	}

	// Reset last_seen to check debounce prevents second write
	db.conn.Exec("UPDATE nodes SET last_seen = NULL WHERE public_key = ?", "relay1")

	// Call again within 5 minutes — should be debounced (no write)
	s.touchRelayLastSeen(tx, now.Add(2*time.Minute))

	db.conn.QueryRow("SELECT last_seen FROM nodes WHERE public_key = ?", "relay1").Scan(&lastSeen)
	if lastSeen.Valid {
		t.Fatal("expected debounce to prevent second write within 5 minutes")
	}

	// Call after 5 minutes — should write again
	s.touchRelayLastSeen(tx, now.Add(6*time.Minute))
	db.conn.QueryRow("SELECT last_seen FROM nodes WHERE public_key = ?", "relay1").Scan(&lastSeen)
	if !lastSeen.Valid {
		t.Fatal("expected write after debounce interval expired")
	}
}

func TestTouchRelayLastSeen_SkipsNilResolvedPath(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	s := &PacketStore{
		db:              db,
		lastSeenTouched: make(map[string]time.Time),
	}

	// tx with nil entries and empty resolved_path
	tx := &StoreTx{
		ResolvedPath: []*string{nil, nil},
	}

	// Should not panic or error
	s.touchRelayLastSeen(tx, time.Now())
}

func TestTouchRelayLastSeen_NilDB(t *testing.T) {
	s := &PacketStore{
		db:              nil,
		lastSeenTouched: make(map[string]time.Time),
	}

	pk := "abc"
	tx := &StoreTx{
		ResolvedPath: []*string{&pk},
	}

	// Should not panic with nil db
	s.touchRelayLastSeen(tx, time.Now())
}
