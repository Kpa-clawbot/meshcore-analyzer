package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// createTestDBWithSchema creates a temp SQLite DB with the standard schema + resolved_path column.
func createTestDBWithSchema(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	conn, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}

	// Create tables
	conn.Exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		raw_hex TEXT, hash TEXT UNIQUE, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER, payload_version INTEGER,
		decoded_json TEXT, channel_hash TEXT DEFAULT NULL
	)`)
	conn.Exec(`CREATE TABLE observers (
		id TEXT PRIMARY KEY, name TEXT, iata TEXT
	)`)
	conn.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
		observer_id TEXT, observer_name TEXT, direction TEXT,
		snr REAL, rssi REAL, score INTEGER,
		path_json TEXT, timestamp TEXT,
		resolved_path TEXT, raw_hex TEXT
	)`)
	conn.Exec(`CREATE TABLE nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT,
		lat REAL, lon REAL, last_seen TEXT, first_seen TEXT,
		advert_count INTEGER DEFAULT 0
	)`)

	conn.Close()

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return db, dbPath
}

func TestResolvePathForObs(t *testing.T) {
	// Build a prefix map with known nodes
	nodes := []nodeInfo{
		{PublicKey: "aabbccddee1234567890aabbccddee1234567890aabbccddee1234567890aabb", Name: "Node-AA"},
		{PublicKey: "bbccddee1234567890aabbccddee1234567890aabbccddee1234567890aabb11", Name: "Node-BB"},
	}
	pm := buildPrefixMap(nodes)
	graph := NewNeighborGraph()

	tx := &StoreTx{
		DecodedJSON: `{"pubKey": "originator1234567890"}`,
		PayloadType: intPtr(4),
	}

	// Unambiguous prefixes should resolve
	rp := resolvePathForObs(`["aa","bb"]`, "observer1", tx, pm, graph)
	if len(rp) != 2 {
		t.Fatalf("expected 2 resolved hops, got %d", len(rp))
	}
	if rp[0] == nil || !strings.HasPrefix(*rp[0], "aabbcc") {
		t.Errorf("expected first hop to resolve to Node-AA, got %v", rp[0])
	}
	if rp[1] == nil || !strings.HasPrefix(*rp[1], "bbccdd") {
		t.Errorf("expected second hop to resolve to Node-BB, got %v", rp[1])
	}
}

func TestResolvePathForObs_EmptyPath(t *testing.T) {
	pm := buildPrefixMap(nil)
	rp := resolvePathForObs(`[]`, "", &StoreTx{}, pm, nil)
	if rp != nil {
		t.Errorf("expected nil for empty path, got %v", rp)
	}

	rp = resolvePathForObs("", "", &StoreTx{}, pm, nil)
	if rp != nil {
		t.Errorf("expected nil for empty string, got %v", rp)
	}
}

func TestResolvePathForObs_Unresolvable(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "aabbccddee1234567890aabbccddee1234567890aabbccddee1234567890aabb", Name: "Node-AA"},
	}
	pm := buildPrefixMap(nodes)

	// "zz" prefix doesn't match any node
	rp := resolvePathForObs(`["zz"]`, "", &StoreTx{}, pm, nil)
	if len(rp) != 1 {
		t.Fatalf("expected 1 hop, got %d", len(rp))
	}
	if rp[0] != nil {
		t.Errorf("expected nil for unresolvable hop, got %v", *rp[0])
	}
}

func TestMarshalUnmarshalResolvedPath(t *testing.T) {
	pk1 := "aabbccdd"
	var rp []*string
	rp = append(rp, &pk1, nil)

	j := marshalResolvedPath(rp)
	if j == "" {
		t.Fatal("expected non-empty JSON")
	}

	parsed := unmarshalResolvedPath(j)
	if len(parsed) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(parsed))
	}
	if parsed[0] == nil || *parsed[0] != "aabbccdd" {
		t.Errorf("first element wrong: %v", parsed[0])
	}
	if parsed[1] != nil {
		t.Errorf("second element should be nil, got %v", *parsed[1])
	}
}

func TestMarshalResolvedPath_Empty(t *testing.T) {
	if marshalResolvedPath(nil) != "" {
		t.Error("expected empty for nil")
	}
	if marshalResolvedPath([]*string{}) != "" {
		t.Error("expected empty for empty slice")
	}
}

func TestUnmarshalResolvedPath_Invalid(t *testing.T) {
	if unmarshalResolvedPath("") != nil {
		t.Error("expected nil for empty string")
	}
	if unmarshalResolvedPath("not json") != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestEnsureNeighborEdgesTable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create initial DB
	conn, _ := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	conn.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY)")
	conn.Close()

	if err := ensureNeighborEdgesTable(dbPath); err != nil {
		t.Fatal(err)
	}

	// Verify table exists
	conn, _ = sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	defer conn.Close()
	var cnt int
	if err := conn.QueryRow("SELECT COUNT(*) FROM neighbor_edges").Scan(&cnt); err != nil {
		t.Fatalf("neighbor_edges table not created: %v", err)
	}
}

func TestLoadNeighborEdgesFromDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	conn, _ := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	conn.Exec(`CREATE TABLE neighbor_edges (
		node_a TEXT NOT NULL, node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1, last_seen TEXT,
		PRIMARY KEY (node_a, node_b)
	)`)
	conn.Exec("INSERT INTO neighbor_edges VALUES ('aaa', 'bbb', 5, '2024-01-01T00:00:00Z')")
	conn.Exec("INSERT INTO neighbor_edges VALUES ('ccc', 'ddd', 3, '2024-01-02T00:00:00Z')")

	g := loadNeighborEdgesFromDB(conn)
	conn.Close()

	// Should have 2 edges
	edges := g.AllEdges()
	if len(edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(edges))
	}

	// Check neighbors
	n := g.Neighbors("aaa")
	if len(n) != 1 {
		t.Errorf("expected 1 neighbor for aaa, got %d", len(n))
	}
}

func TestStoreObsResolvedPathInBroadcast(t *testing.T) {
	// After #800 refactor, resolved_path is no longer stored on StoreTx/StoreObs structs.
	// Broadcast maps carry resolved_path from the decode-window, not from struct fields.
	// This test verifies pickBestObservation no longer sets ResolvedPath on tx.
	obs := &StoreObs{
		ID:           1,
		ObserverID:   "obs1",
		ObserverName: "Observer 1",
		PathJSON:     `["aa"]`,
		Timestamp:    "2024-01-01T00:00:00Z",
	}

	tx := &StoreTx{
		ID:   1,
		Hash: "abc123",
		Observations: []*StoreObs{obs},
	}
	pickBestObservation(tx)

	// tx should NOT have a ResolvedPath field anymore (compile-time guard)
	// Verify the best observation's fields are propagated correctly
	if tx.ObserverID != "obs1" {
		t.Errorf("expected ObserverID=obs1, got %s", tx.ObserverID)
	}
}

func TestResolvedPathInTxToMap(t *testing.T) {
	// After #800, txToMap no longer includes resolved_path from the struct.
	// resolved_path is only available via on-demand SQL fetch (txToMapWithRP).
	tx := &StoreTx{
		ID:       1,
		Hash:     "abc123",
		PathJSON: `["aa"]`,
		obsKeys:  make(map[string]bool),
	}

	m := txToMap(tx)
	if _, ok := m["resolved_path"]; ok {
		t.Error("resolved_path should not be in txToMap output (removed in #800)")
	}
}

func TestResolvedPathOmittedWhenNil(t *testing.T) {
	tx := &StoreTx{
		ID:      1,
		Hash:    "abc123",
		obsKeys: make(map[string]bool),
	}

	m := txToMap(tx)
	if _, ok := m["resolved_path"]; ok {
		t.Error("resolved_path should not be in map when nil")
	}
}

func TestEnsureResolvedPathColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	conn, _ := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	conn.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER,
		observer_id TEXT, path_json TEXT, timestamp TEXT, raw_hex TEXT
	)`)
	conn.Close()

	if err := ensureResolvedPathColumn(dbPath); err != nil {
		t.Fatal(err)
	}

	// Verify column exists
	conn, _ = sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	defer conn.Close()
	rows, _ := conn.Query("PRAGMA table_info(observations)")
	found := false
	for rows.Next() {
		var cid int
		var colName string
		var colType sql.NullString
		var notNull, pk int
		var dflt sql.NullString
		rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk)
		if colName == "resolved_path" {
			found = true
		}
	}
	rows.Close()
	if !found {
		t.Error("resolved_path column not added")
	}

	// Running again should be idempotent
	if err := ensureResolvedPathColumn(dbPath); err != nil {
		t.Fatal("second call should be idempotent:", err)
	}
}

func TestDBDetectsResolvedPathColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create DB without resolved_path
	conn, _ := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	conn.Exec(`CREATE TABLE observations (id INTEGER PRIMARY KEY, observer_idx INTEGER)`)
	conn.Exec(`CREATE TABLE transmissions (id INTEGER PRIMARY KEY)`)
	conn.Close()

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if db.hasResolvedPath {
		t.Error("should not detect resolved_path when column missing")
	}
	db.Close()

	// Add resolved_path column
	conn, _ = sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	conn.Exec("ALTER TABLE observations ADD COLUMN resolved_path TEXT")
	conn.Close()

	db, err = OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !db.hasResolvedPath {
		t.Error("should detect resolved_path when column exists")
	}
	db.Close()
}

func TestLoadWithResolvedPath(t *testing.T) {
	db, dbPath := createTestDBWithSchema(t)
	defer db.Close()

	// Insert test data
	rw, _ := openRW(dbPath)
	rw.Exec(`INSERT INTO transmissions (id, hash, first_seen, payload_type, decoded_json)
		VALUES (1, 'hash1', '2024-01-01T00:00:00Z', 4, '{"pubKey":"origpk"}')`)
	rw.Exec(`INSERT INTO observations (id, transmission_id, observer_id, observer_name, path_json, timestamp, resolved_path)
		VALUES (1, 1, 'obs1', 'Observer1', '["aa"]', '2024-01-01T00:00:00Z', '["aabbccdd"]')`)
	rw.Close()

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	if len(store.packets) != 1 {
		t.Fatalf("expected 1 packet, got %d", len(store.packets))
	}

	tx := store.packets[0]
	if len(tx.Observations) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(tx.Observations))
	}

	// After #800, ResolvedPath is not stored on StoreObs struct.
	// Instead, resolved pubkeys are in the membership index.
	_ = tx.Observations[0] // obs exists
	h := resolvedPubkeyHash("aabbccdd")
	if len(store.resolvedPubkeyIndex[h]) != 1 {
		t.Fatal("expected resolved pubkey to be indexed")
	}
}

func TestResolvedPathInAPIResponse(t *testing.T) {
	// After #800, TransmissionResp no longer has ResolvedPath field.
	// resolved_path is included dynamically in map-based API responses.
	resp := TransmissionResp{
		ID:   1,
		Hash: "test",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	json.Unmarshal(data, &m)

	// resolved_path should NOT be in the marshaled JSON
	if _, ok := m["resolved_path"]; ok {
		t.Error("resolved_path should not be in TransmissionResp JSON (#800)")
	}
}

func TestResolvedPathOmittedWhenEmpty(t *testing.T) {
	resp := TransmissionResp{
		ID:   1,
		Hash: "test",
	}

	data, _ := json.Marshal(resp)
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	if _, ok := m["resolved_path"]; ok {
		t.Error("resolved_path should be omitted when nil")
	}
}

func TestExtractEdgesFromObs_AdvertNoPath(t *testing.T) {
	tx := &StoreTx{
		DecodedJSON: `{"pubKey":"aaaa1111"}`,
		PayloadType: intPtr(4),
	}
	obs := &StoreObs{
		ObserverID: "bbbb2222",
		PathJSON:   "",
		Timestamp:  "2024-01-01T00:00:00Z",
	}

	edges := extractEdgesFromObs(obs, tx, nil)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge for zero-hop advert, got %d", len(edges))
	}
	// Canonical ordering: aaaa < bbbb
	if edges[0].A != "aaaa1111" || edges[0].B != "bbbb2222" {
		t.Errorf("unexpected edge: %+v", edges[0])
	}
}

func TestExtractEdgesFromObs_NonAdvertNoPath(t *testing.T) {
	tx := &StoreTx{PayloadType: intPtr(1)}
	obs := &StoreObs{ObserverID: "obs1", PathJSON: ""}
	edges := extractEdgesFromObs(obs, tx, nil)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for non-advert without path, got %d", len(edges))
	}
}

func TestExtractEdgesFromObs_WithPath(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "aabbccddee1234567890aabbccddee1234567890aabbccddee1234567890aabb", Name: "Node-AA"},
		{PublicKey: "ffgghhii1234567890aabbccddee1234567890aabbccddee1234567890aabb11", Name: "Node-FF"},
	}
	pm := buildPrefixMap(nodes)

	tx := &StoreTx{
		DecodedJSON: `{"pubKey":"originator00"}`,
		PayloadType: intPtr(4),
	}
	obs := &StoreObs{
		ObserverID: "observer00",
		PathJSON:   `["aa","ff"]`,
		Timestamp:  "2024-01-01T00:00:00Z",
	}

	edges := extractEdgesFromObs(obs, tx, pm)
	// Should get: originator↔aa (advert), observer↔ff (last hop)
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
}

func TestExtractEdgesFromObs_SameNodeNoEdge(t *testing.T) {
	tx := &StoreTx{
		DecodedJSON: `{"pubKey":"same1234"}`,
		PayloadType: intPtr(4),
	}
	obs := &StoreObs{
		ObserverID: "same1234",
		PathJSON:   "",
		Timestamp:  "2024-01-01T00:00:00Z",
	}
	edges := extractEdgesFromObs(obs, tx, nil)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges when originator == observer, got %d", len(edges))
	}
}



func TestPersistSemaphoreTryAcquireSkipsBatch(t *testing.T) {
	// Verify that persistSem is a buffered channel of size 1.
	if cap(persistSem) != 1 {
		t.Errorf("persistSem capacity = %d, want 1", cap(persistSem))
	}
	// Acquire the semaphore to simulate an in-progress persistence.
	persistSem <- struct{}{}

	// asyncPersistResolvedPathsAndEdges should skip (not block, not
	// spawn a goroutine) when the semaphore is already held.
	done := make(chan struct{})
	go func() {
		asyncPersistResolvedPathsAndEdges(
			"/nonexistent/path.db",
			[]persistObsUpdate{{obsID: 1, resolvedPath: "x"}},
			nil,
			"test",
		)
		close(done)
	}()

	// If the function blocks on the semaphore instead of skipping,
	// this select will hit the timeout.
	select {
	case <-done:
		// Expected: returned immediately because semaphore was busy.
	case <-time.After(500 * time.Millisecond):
		<-persistSem
		t.Fatal("asyncPersistResolvedPathsAndEdges blocked instead of skipping when semaphore was held")
	}

	<-persistSem // release
}

func TestOpenRW_BusyTimeout(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create the DB file first
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TABLE dummy (id INTEGER)")
	db.Close()

	// Open via openRW and verify busy_timeout is set
	rw, err := openRW(dbPath)
	if err != nil {
		t.Fatalf("openRW failed: %v", err)
	}
	defer rw.Close()

	var timeout int
	if err := rw.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("expected busy_timeout=5000, got %d", timeout)
	}
}
