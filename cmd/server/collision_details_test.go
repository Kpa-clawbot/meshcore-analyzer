package main

import (
	"testing"
	"time"
)

// TestCollisionDetailsIncludeNodePairs verifies that collision details contain
// the correct prefix and matching node pairs (#757).
func TestCollisionDetailsIncludeNodePairs(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	// Insert two repeater nodes with the same 3-byte prefix "AABB11"
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES ('aabb11ccdd001122', 'Node Alpha', 'repeater')`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES ('aabb11eeff334455', 'Node Beta', 'repeater')`)

	// Add advert transmissions with hash_size=3 path bytes (0x80 = bits 10 → size 3)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0180aabb11ccdd', 'col_hash_01', ?, 1, 4, '{"pubKey":"aabb11ccdd001122","name":"Node Alpha","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -91, '["aabb11"]', ?)`, recentEpoch)

	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0180aabb11eeff', 'col_hash_02', ?, 1, 4, '{"pubKey":"aabb11eeff334455","name":"Node Beta","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 9.0, -93, '["aabb11"]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetAnalyticsHashCollisions("")
	bySize, ok := result["by_size"].(map[string]interface{})
	if !ok {
		t.Fatal("expected by_size map")
	}

	size3, ok := bySize["3"].(map[string]interface{})
	if !ok {
		t.Fatal("expected by_size[3] map")
	}

	collisions, ok := size3["collisions"].([]collisionEntry)
	if !ok {
		t.Fatalf("expected collisions as []collisionEntry, got %T", size3["collisions"])
	}

	// Find our collision
	var found *collisionEntry
	for i := range collisions {
		if collisions[i].Prefix == "AABB11" {
			found = &collisions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected collision with prefix AABB11")
	}
	if found.Appearances != 2 {
		t.Errorf("expected 2 appearances, got %d", found.Appearances)
	}
	if len(found.Nodes) != 2 {
		t.Fatalf("expected 2 nodes in collision, got %d", len(found.Nodes))
	}

	// Verify node pairs
	pubkeys := map[string]bool{}
	names := map[string]bool{}
	for _, n := range found.Nodes {
		pubkeys[n.PublicKey] = true
		names[n.Name] = true
	}
	if !pubkeys["aabb11ccdd001122"] {
		t.Error("expected node aabb11ccdd001122 in collision")
	}
	if !pubkeys["aabb11eeff334455"] {
		t.Error("expected node aabb11eeff334455 in collision")
	}
	if !names["Node Alpha"] {
		t.Error("expected Node Alpha in collision")
	}
	if !names["Node Beta"] {
		t.Error("expected Node Beta in collision")
	}
}

// TestCollisionDetailsEmptyWhenNoCollisions verifies that collision details are
// empty when there are no collisions (#757).
func TestCollisionDetailsEmptyWhenNoCollisions(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	// Insert one repeater node with 3-byte hash
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES ('aabb11ccdd001122', 'Solo Node', 'repeater')`)

	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('0180aabb11ccdd', 'solo_hash_01', ?, 1, 4, '{"pubKey":"aabb11ccdd001122","name":"Solo Node","type":"ADVERT"}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -91, '["aabb11"]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()

	result := store.GetAnalyticsHashCollisions("")
	bySize, ok := result["by_size"].(map[string]interface{})
	if !ok {
		t.Fatal("expected by_size map")
	}

	size3, ok := bySize["3"].(map[string]interface{})
	if !ok {
		t.Fatal("expected by_size[3] map")
	}

	collisions, ok := size3["collisions"].([]collisionEntry)
	if !ok {
		t.Fatalf("expected collisions as []collisionEntry, got %T", size3["collisions"])
	}

	if len(collisions) != 0 {
		t.Errorf("expected 0 collisions, got %d", len(collisions))
	}
}
