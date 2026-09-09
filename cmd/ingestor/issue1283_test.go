package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestIngestorPruneOldPackets enforces #1283: the writer for
// transmissions retention lives on the ingestor's *Store. Before the fix,
// this lived on cmd/server/*DB and raced with ingestor INSERTs. After
// the fix, ingestor owns it and runs it on its own write-locked handle.
func TestIngestorPruneOldPackets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prune.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	old := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339)
	new := time.Now().UTC().Format(time.RFC3339)
	for i, ts := range []string{old, old, new} {
		_, err := store.db.Exec(
			`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
			 VALUES (?, ?, ?, 0, 1, 1, '{}')`,
			"AA", "h"+string(rune('a'+i)), ts,
		)
		if err != nil {
			t.Fatalf("seed tx: %v", err)
		}
	}

	n, err := store.PruneOldPackets(5)
	if err != nil {
		t.Fatalf("PruneOldPackets: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 pruned, got %d", n)
	}

	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected 1 transmission remaining, got %d", remaining)
	}
}

// TestIngestorVacuumOnStartupMigratesNONEtoINCREMENTAL exercises the
// scenario that originally broke in #1283: a fresh DB with
// auto_vacuum=NONE, vacuumOnStartup=true, no contention from a server
// process. The ingestor must complete the VACUUM and flip auto_vacuum to
// INCREMENTAL. Before the fix, the migration ran inside cmd/server and
// hit SQLITE_BUSY because the ingestor (sharing the container) was
// already writing.
func TestIngestorVacuumOnStartupMigratesNONEtoINCREMENTAL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vac.db")

	// Create a NONE-auto_vacuum DB (simulates an older deployment).
	seed, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	seed.SetMaxOpenConns(1)
	if _, err := seed.Exec(`CREATE TABLE dummy(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	var before int
	seed.QueryRow("PRAGMA auto_vacuum").Scan(&before)
	if before != 0 {
		t.Fatalf("precondition: auto_vacuum=%d, want 0", before)
	}
	seed.Close()

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	cfg := &Config{DB: &DBConfig{VacuumOnStartup: true}}
	store.CheckAutoVacuum(cfg)

	var after int
	if err := store.db.QueryRow("PRAGMA auto_vacuum").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 2 {
		t.Fatalf("expected auto_vacuum=2 after ingestor VACUUM, got %d", after)
	}
}
