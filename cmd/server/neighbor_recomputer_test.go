package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbschema"
)

// TestNeighborGraphRecomputerLoadsSnapshot enforces #1287 Option 4:
// the server LOADS its in-memory neighbor graph from the SQLite
// snapshot the ingestor writes. After a write to neighbor_edges (here
// done synthetically), the recomputer's atomic-swap must reflect it.
func TestNeighborGraphRecomputerLoadsSnapshot(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "neighbor_recomp.db")

	// Bootstrap a WAL DB with the neighbor_edges table.
	rw, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`CREATE TABLE neighbor_edges (
		node_a TEXT NOT NULL,
		node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_seen TEXT,
		PRIMARY KEY (node_a, node_b)
	)`); err != nil {
		t.Fatal(err)
	}
	ensurePreparable(t, rw)

	// Stage one edge.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := rw.Exec(
		`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, ?, ?)`,
		"aaa", "bbb", 5, now,
	); err != nil {
		t.Fatal(err)
	}

	// Server opens read-only and refreshes via the recomputer.
	d, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer d.conn.Close()
	store := &PacketStore{db: d}
	store.graph.Store(NewNeighborGraph())

	store.refreshNeighborGraphFromSnapshot()
	g := store.graph.Load()
	if g == nil {
		t.Fatal("graph nil after refresh")
	}
	if got := len(g.AllEdges()); got != 1 {
		t.Fatalf("expected 1 edge after first refresh, got %d", got)
	}

	// Add another row, refresh, assert the new total.
	if _, err := rw.Exec(
		`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, ?, ?)`,
		"ccc", "ddd", 2, now,
	); err != nil {
		t.Fatal(err)
	}
	store.refreshNeighborGraphFromSnapshot()
	g = store.graph.Load()
	if got := len(g.AllEdges()); got != 2 {
		t.Fatalf("expected 2 edges after second refresh, got %d", got)
	}
}

// TestServerStartupRequiresMigratedSchema enforces #1287: the server
// MUST refuse to start if the ingestor hasn't run schema migrations.
// AssertReady on a DB missing the required columns returns an error
// listing every missing surface; main.go then calls log.Fatalf.
func TestServerStartupRequiresMigratedSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "unmigrated.db")

	// Bootstrap with ONLY transmissions/observations (the things
	// server tries to read) but WITHOUT the columns dbschema asserts
	// (resolved_path, last_packet_at, iata, foreign_advert, from_pubkey,
	// neighbor_edges).
	//
	// observers.inactive used to be in that list. It no longer is: OpenDB
	// prepares statements eagerly under github.com/mattn/go-sqlite3 and one of
	// them selects on inactive, so a fixture without it cannot reach
	// AssertReady at all. The other missing surfaces above still make
	// AssertReady fail, which is what this test asserts.
	rw, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	for _, s := range []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, hash TEXT, payload_type INTEGER)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER)`,
		`CREATE TABLE observers (id TEXT PRIMARY KEY, name TEXT, inactive INTEGER)`,
		`CREATE TABLE nodes (public_key TEXT PRIMARY KEY)`,
		`CREATE TABLE inactive_nodes (public_key TEXT PRIMARY KEY)`,
	} {
		if _, err := rw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}

	ensurePreparable(t, rw)

	// Open the read-only server handle and call AssertReady directly
	// (production path: main.go does this before any business logic).
	d, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer d.conn.Close()

	// The package-level dbschema.AssertReady requires every missing
	// surface to be reported. We hit it directly through the same
	// path main.go uses.
	if err := assertReadyForTest(d); err == nil {
		t.Fatal("expected AssertReady to fail against an unmigrated DB; server would have started against an incomplete schema")
	}
}

// assertReadyForTest is the same call main.go makes — declared here so
// the test stays decoupled from any future inlining or rename.
func assertReadyForTest(d *DB) error {
	return dbschemaAssertReadyShim(d)
}

// dbschemaAssertReadyShim wraps the package import so tests don't
// directly depend on the import being present (production wires it
// via main.go).
func dbschemaAssertReadyShim(d *DB) error { return dbschema.AssertReady(d.conn) }
