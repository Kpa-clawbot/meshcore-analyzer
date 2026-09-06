package main

import (
	"testing"
	"time"
)

func TestMigrateContentHashesAsync(t *testing.T) {
	db := setupTestDBv2(t)
	store := NewPacketStore(db, nil)

	// Insert a packet with a manually wrong hash (simulating old formula).
	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	correctHash := ComputeContentHash(rawHex)
	wrongHash := "deadbeef12345678"

	_, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
		VALUES (?, ?, datetime('now'), 0, 2)`, rawHex, wrongHash)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	if store.byHash[wrongHash] == nil {
		t.Fatal("expected packet under wrong hash before migration")
	}

	migrateContentHashesAsync(store, 100, time.Millisecond)

	if !store.hashMigrationComplete.Load() {
		t.Error("expected hashMigrationComplete to be true")
	}
	if store.byHash[wrongHash] != nil {
		t.Error("old hash should be removed from index")
	}
	if store.byHash[correctHash] == nil {
		t.Error("new hash should be in index")
	}

	var dbHash string
	err = db.conn.QueryRow("SELECT hash FROM transmissions WHERE raw_hex = ?", rawHex).Scan(&dbHash)
	if err != nil {
		t.Fatal(err)
	}
	if dbHash != correctHash {
		t.Errorf("DB hash = %s, want %s", dbHash, correctHash)
	}
}

func TestMigrateContentHashesAsync_NoOp(t *testing.T) {
	db := setupTestDBv2(t)
	store := NewPacketStore(db, nil)

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	correctHash := ComputeContentHash(rawHex)

	_, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
		VALUES (?, ?, datetime('now'), 0, 2)`, rawHex, correctHash)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	migrateContentHashesAsync(store, 100, time.Millisecond)

	if !store.hashMigrationComplete.Load() {
		t.Error("expected hashMigrationComplete to be true")
	}
	if store.byHash[correctHash] == nil {
		t.Error("hash should remain in index")
	}
}

// #1856: a migration that could not write anything must not report completion.
//
// In production the server holds a mode=ro handle (#1283), so Begin, Prepare and
// Commit all fail, every batch takes a `continue`, and the loop reaches the
// deferred completion having migrated nothing. Before this fix the flag was set
// unconditionally there, so /api/stats answered hashMigrationComplete: true
// after doing no work at all.
//
// Closing the handle stands in for the read-only one: it is deterministic and it
// exercises the identical failure path (Begin returns an error, batch skipped).
func TestMigrateContentHashesAsyncDoesNotClaimCompletionWhenWritesFail(t *testing.T) {
	db := setupTestDBv2(t)
	store := NewPacketStore(db, nil)

	rawHex := "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	wrongHash := "deadbeef12345678"
	if _, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type)
		VALUES (?, ?, datetime('now'), 0, 2)`, rawHex, wrongHash); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if store.byHash[wrongHash] == nil {
		t.Fatal("expected packet under the wrong hash before migration")
	}

	// Make every write fail, the way a read-only handle does in production.
	if err := db.conn.Close(); err != nil {
		t.Fatal(err)
	}

	migrateContentHashesAsync(store, 100, time.Millisecond)

	if store.hashMigrationComplete.Load() {
		t.Error("hashMigrationComplete must stay false when no batch could be written; " +
			"reporting true here is what #1856 called self-reported success")
	}
	if store.byHash[wrongHash] == nil {
		t.Error("the in-memory index must be left alone when the DB write failed, " +
			"otherwise memory and disk disagree")
	}
}
