package main

import (
	"testing"
	"time"
)

// Issue #1598 / #1611 — the relay-aware last_seen touch.
//
// History: the server had touchRelayLastSeen (cmd/server/store.go), which
// called TouchNodeLastSeen → UPDATE nodes SET last_seen. Since #1283/#1289
// the server opens SQLite with mode=ro, so that UPDATE has failed with
// "attempt to write a readonly database" on every call, and the error was
// discarded at the call site. Net effect: nodes.last_seen has tracked
// ADVERT arrivals only, and relay participation has never refreshed it.
//
// The writer lives in the ingestor, which since #1547 already resolves hop
// prefixes to full pubkeys for observations.resolved_path. These tests pin
// the touch to that existing resolution point.

// helper: seed a node so the prefix index can resolve a hop to it.
func seedRelayNode(t *testing.T, s *Store, pubkey, name, lastSeen string) {
	t.Helper()
	if err := s.UpsertNode(pubkey, name, "repeater", nil, nil, lastSeen); err != nil {
		t.Fatalf("seed node %s: %v", name, err)
	}
}

func nodeLastSeen(t *testing.T, s *Store, pubkey string) string {
	t.Helper()
	var ls string
	if err := s.db.QueryRow(`SELECT COALESCE(last_seen,'') FROM nodes WHERE public_key=?`, pubkey).Scan(&ls); err != nil {
		t.Fatalf("read last_seen for %s: %v", pubkey, err)
	}
	return ls
}

// TestTouchRelayNodes_AdvancesLastSeen is the core regression: a node that
// appears as a resolved relay hop must have its last_seen advanced, even
// though it sent no ADVERT of its own.
func TestTouchRelayNodes_AdvancesLastSeen(t *testing.T) {
	store := newTestStore(t)

	const relay = "aa11223344556677889900aabbccddeeff00112233445566778899aabbccddee"
	seedRelayNode(t, store, relay, "RelayOnly", "2026-07-01T00:00:00Z")

	rxTime := "2026-07-10T12:00:00Z"
	store.touchRelayNodesLocked([]string{relay}, rxTime)

	got := nodeLastSeen(t, store, relay)
	if got != rxTime {
		t.Errorf("last_seen = %q, want %q — relay participation did not refresh the node", got, rxTime)
	}
	if n := store.Stats.RelayTouches.Load(); n != 1 {
		t.Errorf("RelayTouches = %d, want 1", n)
	}
}

// TestTouchRelayNodes_NeverGoesBackwards guards the monotonic invariant.
// Out-of-order ingest (a late observation with an older rxTime) must not
// rewind a node's last_seen.
func TestTouchRelayNodes_NeverGoesBackwards(t *testing.T) {
	store := newTestStore(t)

	const relay = "bb11223344556677889900aabbccddeeff00112233445566778899aabbccddee"
	seedRelayNode(t, store, relay, "Backbone", "2026-07-10T12:00:00Z")

	store.touchRelayNodesLocked([]string{relay}, "2026-07-09T00:00:00Z")

	if got := nodeLastSeen(t, store, relay); got != "2026-07-10T12:00:00Z" {
		t.Errorf("last_seen went backwards: got %q, want 2026-07-10T12:00:00Z", got)
	}
}

// TestTouchRelayNodes_Debounces pins the write-amplification guard. The
// ingest path is hot; a backbone repeater appears in thousands of paths per
// hour and must not produce one UPDATE per observation.
func TestTouchRelayNodes_Debounces(t *testing.T) {
	store := newTestStore(t)

	const relay = "cc11223344556677889900aabbccddeeff00112233445566778899aabbccddee"
	seedRelayNode(t, store, relay, "Chatty", "2026-07-01T00:00:00Z")

	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store.touchRelayNodesLocked([]string{relay}, base.Format(time.RFC3339))
	// Second hit two minutes later — inside the debounce window, no write.
	store.touchRelayNodesLocked([]string{relay}, base.Add(2*time.Minute).Format(time.RFC3339))

	if n := store.Stats.RelayTouches.Load(); n != 1 {
		t.Errorf("RelayTouches = %d, want 1 (second touch should be debounced)", n)
	}
	if got := nodeLastSeen(t, store, relay); got != base.Format(time.RFC3339) {
		t.Errorf("last_seen = %q, want %q", got, base.Format(time.RFC3339))
	}

	// Past the debounce window the write goes through again.
	later := base.Add(6 * time.Minute)
	store.touchRelayNodesLocked([]string{relay}, later.Format(time.RFC3339))
	if n := store.Stats.RelayTouches.Load(); n != 2 {
		t.Errorf("RelayTouches = %d, want 2 after debounce window elapsed", n)
	}
	if got := nodeLastSeen(t, store, relay); got != later.Format(time.RFC3339) {
		t.Errorf("last_seen = %q, want %q", got, later.Format(time.RFC3339))
	}
}

// TestTouchRelayNodes_IgnoresEmptyAndUnknown covers the unresolved-hop case:
// resolvePathWithContext yields nil for ambiguous or unknown prefixes, and
// unknown pubkeys must not create rows.
func TestTouchRelayNodes_IgnoresEmptyAndUnknown(t *testing.T) {
	store := newTestStore(t)

	store.touchRelayNodesLocked(nil, "2026-07-10T12:00:00Z")
	store.touchRelayNodesLocked([]string{}, "2026-07-10T12:00:00Z")
	store.touchRelayNodesLocked([]string{""}, "2026-07-10T12:00:00Z")
	store.touchRelayNodesLocked([]string{"ff99887766554433221100ffeeddccbbaa99887766554433221100ffeeddccbb"}, "2026-07-10T12:00:00Z")

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("nodes count = %d, want 0 — touch must never insert rows", count)
	}
	if n := store.Stats.RelayTouches.Load(); n != 0 {
		t.Errorf("RelayTouches = %d, want 0", n)
	}
}

// TestTouchRelayNodes_MalformedTimestamp: rxTime that does not parse must be
// a no-op rather als writing a garbage timestamp into the node directory.
func TestTouchRelayNodes_MalformedTimestamp(t *testing.T) {
	store := newTestStore(t)

	const relay = "dd11223344556677889900aabbccddeeff00112233445566778899aabbccddee"
	seedRelayNode(t, store, relay, "Fine", "2026-07-01T00:00:00Z")

	store.touchRelayNodesLocked([]string{relay}, "not-a-timestamp")

	if got := nodeLastSeen(t, store, relay); got != "2026-07-01T00:00:00Z" {
		t.Errorf("last_seen = %q, want it unchanged on malformed rxTime", got)
	}
	if n := store.Stats.RelayTouches.Load(); n != 0 {
		t.Errorf("RelayTouches = %d, want 0", n)
	}
}
