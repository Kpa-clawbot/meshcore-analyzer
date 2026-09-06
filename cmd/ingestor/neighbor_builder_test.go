package main

import (
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/packetpath"
)

// TestNeighborEdgesBuilderUpsertsFromObservations enforces issue
// #1287 Option 4: the INGESTOR builds neighbor_edges from raw
// observations/transmissions and persists them. Server is read-only.
//
// Synthesize a tiny DB with one ADVERT observation whose path[0]
// uniquely resolves to a known node, then assert the builder writes
// the expected edge.
func TestNeighborEdgesBuilderUpsertsFromObservations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "build.db")

	// Open via the ingestor's normal opener so applySchema and
	// dbschema.Apply both run (the builder requires neighbor_edges +
	// observers.iata etc.).
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Seed two nodes whose pubkey prefixes will be used as hops.
	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "from-node",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}

	// Seed one observer.
	if _, err := store.db.Exec(
		`INSERT INTO observers (id, name) VALUES (?, ?)`,
		"obs-1", "observer-1",
	); err != nil {
		t.Fatal(err)
	}
	var obsRowid int64
	if err := store.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, "obs-1").Scan(&obsRowid); err != nil {
		t.Fatal(err)
	}

	// Insert one ADVERT transmission with from_pubkey = aaaaa…
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, from_pubkey)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"", "h1", "2026-01-01T00:00:00Z", 0, payloadADVERT, 0, "{}", "aaaaaaaaaa",
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()

	// Insert one observation whose path[0] = "bb" (2-hex prefix unique
	// to bbbbb… in the nodes table). Expected edge: a↔b.
	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, ?, ?)`,
		txID, obsRowid, `["bb"]`, int64(1735689600),
	); err != nil {
		t.Fatal(err)
	}

	n, err := store.buildAndPersistNeighborEdges(trustAllPrefixes())
	if err != nil {
		t.Fatalf("buildAndPersistNeighborEdges: %v", err)
	}
	if n == 0 {
		t.Fatal("expected at least 1 edge upserted, got 0")
	}

	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM neighbor_edges WHERE node_a = ? AND node_b = ?`, "aaaaaaaaaa", "bbbbbbbbbb").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("expected the a↔b edge to be persisted; got %d rows", got)
	}
}

// (test ends here)

// TestNeighborEdgesBuilderUpsertsFromAnonReqEphemeralPubKey verifies #1777:
// ANON_REQ transmissions (payload type 7) carry the sender's full
// ephemeral pubkey in decoded_json ("ephemeralPubKey"), not in the
// from_pubkey column (which is only populated for ADVERT at write time,
// see db.go's #1143 comment). The builder must fall back to parsing
// decoded_json for ANON_REQ, exactly as it already does for ADVERT.
func TestNeighborEdgesBuilderUpsertsFromAnonReqEphemeralPubKey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "build.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "sender",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec(
		`INSERT INTO observers (id, name) VALUES (?, ?)`,
		"obs-1", "observer-1",
	); err != nil {
		t.Fatal(err)
	}
	var obsRowid int64
	if err := store.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, "obs-1").Scan(&obsRowid); err != nil {
		t.Fatal(err)
	}

	// ANON_REQ transmission: from_pubkey left NULL (as real ingest does —
	// only ADVERT populates it at write time), sender identity carried in
	// decoded_json.ephemeralPubKey instead.
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"", "h2", "2026-01-01T00:00:00Z", 0, payloadAnonReq, 0, `{"ephemeralPubKey":"aaaaaaaaaa"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()

	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, ?, ?)`,
		txID, obsRowid, `["bb"]`, int64(1735689600),
	); err != nil {
		t.Fatal(err)
	}

	n, err := store.buildAndPersistNeighborEdges(trustAllPrefixes())
	if err != nil {
		t.Fatalf("buildAndPersistNeighborEdges: %v", err)
	}
	if n == 0 {
		t.Fatal("expected at least 1 edge upserted, got 0")
	}

	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM neighbor_edges WHERE node_a = ? AND node_b = ?`, "aaaaaaaaaa", "bbbbbbbbbb").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("expected the sender\u2194first-hop edge from ANON_REQ to be persisted (#1777); got %d rows", got)
	}
}

// TestNeighborEdgesBuilderExcludesOtherNonAdvertTypes verifies #1777's
// scope boundary: a plain REQ (payload type 2, not ADVERT or ANON_REQ)
// must NOT produce an originator↔path[0] edge, even if from_pubkey happens
// to be set — REQ's src is only a 1-byte truncated hash of the originator,
// not a full pubkey, and was explicitly rejected as an edge source in the
// #1777 discussion (collision odds ~1/256).
func TestNeighborEdgesBuilderExcludesOtherNonAdvertTypes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "build.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "sender",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec(
		`INSERT INTO observers (id, name) VALUES (?, ?)`,
		"obs-1", "observer-1",
	); err != nil {
		t.Fatal(err)
	}
	var obsRowid int64
	if err := store.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, "obs-1").Scan(&obsRowid); err != nil {
		t.Fatal(err)
	}

	const payloadREQ = 2
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, from_pubkey)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"", "h3", "2026-01-01T00:00:00Z", 0, payloadREQ, 0, "{}", "aaaaaaaaaa",
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()

	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, ?, ?)`,
		txID, obsRowid, `["bb"]`, int64(1735689600),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.buildAndPersistNeighborEdges(trustAllPrefixes()); err != nil {
		t.Fatalf("buildAndPersistNeighborEdges: %v", err)
	}

	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM neighbor_edges WHERE node_a = ? AND node_b = ?`, "aaaaaaaaaa", "bbbbbbbbbb").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("REQ should not produce an originator\u2194path[0] edge; got %d rows", got)
	}
}

// trustAllPrefixes returns the pre-#1784 threshold, where 1-byte hop
// hashes still count as mapping evidence. The builder tests above
// exercise edge *shape* using 1-byte fixtures; pinning them to the
// legacy threshold keeps their original intent intact, while the gate
// itself is covered by the two tests below.
func trustAllPrefixes() *packetpath.TrustConfig {
	return &packetpath.TrustConfig{MinHashBytesForMapping: 1}
}

// seedTrustFixture builds the minimal DB shape shared by the path-trust
// tests: two nodes, one observer, one ADVERT transmission, and one
// observation carrying hop as its single path element.
func seedTrustFixture(t *testing.T, store *Store, hop string) {
	t.Helper()
	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "from-node",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO observers (id, name) VALUES (?, ?)`, "obs-1", "observer-1"); err != nil {
		t.Fatal(err)
	}
	var obsRowid int64
	if err := store.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, "obs-1").Scan(&obsRowid); err != nil {
		t.Fatal(err)
	}
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, from_pubkey)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"", "h1", "2026-01-01T00:00:00Z", 0, payloadADVERT, 0, "{}", "aaaaaaaaaa",
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()
	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, ?, ?)`,
		txID, obsRowid, `["`+hop+`"]`, int64(1735689600),
	); err != nil {
		t.Fatal(err)
	}
}

// TestNeighborEdgesBuilderPathTrustExcludesOneByte pins #1784 at the
// builder: under the default threshold (2 bytes) a 1-byte hop hash
// produces no edge, even though it resolves to exactly one candidate in
// the nodes table. Uniqueness of a 1-byte prefix is a property of the
// nodes we happen to know about — on a mesh large enough to occupy all
// 256 values (SaarMesh: 1071 nodes, 13 of them uniquely resolvable by
// one byte) a later-joining repeater sharing that byte turns today's
// "unique" resolution into a wrong edge.
func TestNeighborEdgesBuilderPathTrustExcludesOneByte(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "trust1.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	seedTrustFixture(t, store, "bb")

	// Pass the threshold explicitly rather than leaning on the package default.
	// This test is about what happens AT threshold 2, not about what the default
	// happens to be, and coupling the two made it fail the moment the default
	// moved to 1 (#1929).
	trust := &packetpath.TrustConfig{MinHashBytesForMapping: 2}
	if _, err := store.buildAndPersistNeighborEdges(trust); err != nil {
		t.Fatalf("buildAndPersistNeighborEdges: %v", err)
	}

	var got int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM neighbor_edges WHERE node_a = ? AND node_b = ?`,
		"aaaaaaaaaa", "bbbbbbbbbb").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("1-byte hop must not produce an edge under the default threshold, got %d", got)
	}
}

// TestNeighborEdgesBuilderPathTrustAllowsTwoByte is the positive half of
// the gate: the same fixture with a 2-byte hop still produces the edge,
// so the threshold narrows the evidence base rather than disabling the
// builder.
func TestNeighborEdgesBuilderPathTrustAllowsTwoByte(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "trust2.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	seedTrustFixture(t, store, "bbbb")

	if _, err := store.buildAndPersistNeighborEdges(nil); err != nil {
		t.Fatalf("buildAndPersistNeighborEdges: %v", err)
	}

	var got int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM neighbor_edges WHERE node_a = ? AND node_b = ?`,
		"aaaaaaaaaa", "bbbbbbbbbb").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("2-byte hop must still produce the edge, got %d", got)
	}
}
