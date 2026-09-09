package dbschema

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// observationsDB builds a database with an observations table but deliberately
// no idx_observations_dedup — the shape of every database created before
// cmd/ingestor/db.go started making that index, and of any database whose
// observations table it never ran against.
func observationsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "obs.db")+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transmission_id INTEGER NOT NULL,
		observer_idx INTEGER,
		direction TEXT,
		snr REAL,
		rssi REAL,
		path_json TEXT,
		timestamp INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func dedupIndexExists(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_observations_dedup'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestEnsureObservationsDedupIndexOnCleanTable(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (1, 1, '[]', 10), (1, 2, '[]', 10)`,
	); err != nil {
		t.Fatal(err)
	}
	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}
	if !dedupIndexExists(t, db) {
		t.Error("index was not created")
	}
}

// The interesting case: duplicates already present, which is what blocked the
// index from being created in the first place. They must be collapsed, and the
// survivor must inherit the non-NULL fields of the rows that went away — the
// same merge the ingestor's ON CONFLICT ... DO UPDATE SET x = COALESCE(...)
// would have performed had the index existed.
func TestEnsureObservationsDedupIndexCollapsesDuplicates(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp) VALUES
		(1, 5, 3, 'rx', NULL, -90,  '[]', 100),
		(2, 5, 3, NULL, 7.5,  NULL, '[]', 100),
		(3, 5, 3, NULL, NULL, NULL, NULL, 100),
		(4, 9, 1, 'rx', 1.0,  -80,  '["AA"]', 200)`); err != nil {
		t.Fatal(err)
	}

	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}
	if !dedupIndexExists(t, db) {
		t.Fatal("index was not created after collapsing duplicates")
	}

	// Rows 1 and 2 share the key (5, 3, '[]') and collapse into id 1.
	//
	// Row 3 does NOT join them: its path_json is NULL, and COALESCE(path_json,'')
	// makes that the empty string, a different key from '[]'. That is the
	// ingestor's conflict target verbatim, so an unrecorded path and an
	// explicitly empty path are distinct observations here — worth pinning,
	// because from the outside it looks like it ought to be one group.
	// Row 4 has its own key and is untouched.
	var ids string
	if err := db.QueryRow(`SELECT GROUP_CONCAT(id) FROM (SELECT id FROM observations ORDER BY id)`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != "1,3,4" {
		t.Errorf("surviving ids = %q, want \"1,3,4\" (lowest id of each group; NULL path_json is its own group)", ids)
	}

	var direction sql.NullString
	var snr, rssi sql.NullFloat64
	if err := db.QueryRow(`SELECT direction, snr, rssi FROM observations WHERE id = 1`).Scan(&direction, &snr, &rssi); err != nil {
		t.Fatal(err)
	}
	if direction.String != "rx" {
		t.Errorf("direction = %q, want \"rx\" (its own value)", direction.String)
	}
	if snr.Float64 != 7.5 {
		t.Errorf("snr = %v, want 7.5 (merged from the row that was removed)", snr.Float64)
	}
	if rssi.Float64 != -90 {
		t.Errorf("rssi = %v, want -90 (its own value, not overwritten by a NULL)", rssi.Float64)
	}
}

// The merge has to replay the UPSERT, and the UPSERT's
// `COALESCE(excluded.x, x)` means a later non-NULL value REPLACES an earlier
// one. Complementary NULLs (as above) cannot tell "first non-NULL wins" from
// "last non-NULL wins" — both produce the same answer — so this asserts the
// direction explicitly with values that conflict.
func TestEnsureObservationsDedupIndexKeepsLatestValues(t *testing.T) {
	db := observationsDB(t)
	// One group, three rows, every merged column non-NULL and different.
	// path_json is identical so they collide; direction is NOT in the UPSERT's
	// SET list, so the survivor must keep its own.
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp) VALUES
		(1, 7, 2, 'first',  1.0, -10, '[]', 100),
		(2, 7, 2, 'second', 7.0, -20, '[]', 100),
		(3, 7, 2, 'third',  9.0, -30, '[]', 100)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("ensureObservationsDedupIndex: %v", err)
	}

	var direction string
	var snr, rssi float64
	if err := db.QueryRow(`SELECT direction, snr, rssi FROM observations WHERE id = 1`).Scan(&direction, &snr, &rssi); err != nil {
		t.Fatal(err)
	}
	if snr != 9.0 {
		t.Errorf("snr = %v, want 9 (last non-NULL: COALESCE(excluded.snr, snr) lets later rows win)", snr)
	}
	if rssi != -30 {
		t.Errorf("rssi = %v, want -30 (last non-NULL)", rssi)
	}
	if direction != "first" {
		t.Errorf("direction = %q, want \"first\": the UPSERT never SETs direction, so the survivor keeps its own", direction)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("observations = %d, want 1", n)
	}
}

// The repair must be all-or-nothing. If the index cannot be created, the
// deletions must not survive: rows destroyed with no index to show for it is
// the worst outcome available.
func TestCollapseDuplicatesAndIndexIsAtomic(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp) VALUES
		(1, 1, 1, '[]', 10), (2, 1, 1, '[]', 10)`); err != nil {
		t.Fatal(err)
	}
	// Occupy the index name with a TABLE. `CREATE INDEX IF NOT EXISTS` only
	// shrugs when an *index* of that name exists; a table of that name is an
	// error, so the CREATE fails after the merge and delete have already run.
	if _, err := db.Exec(`CREATE TABLE idx_observations_dedup (x INTEGER)`); err != nil {
		t.Fatal(err)
	}

	if _, err := collapseDuplicatesAndIndex(db); err == nil {
		t.Fatal("expected index creation to fail")
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("observations = %d, want 2: a failed index creation must roll the deletions back", n)
	}
}

// Running twice must be a no-op: Apply runs on every ingestor start.
func TestEnsureObservationsDedupIndexIsIdempotent(t *testing.T) {
	db := observationsDB(t)
	if _, err := db.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (1, 1, '[]', 10), (1, 1, '[]', 10)`); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("observations = %d, want 1", n)
	}
}

// v2 schemas key observations by observer_id, not observer_idx. The ingestor's
// UPSERT does not apply there, and indexing a missing column would error, so the
// step must skip rather than fail.
func TestEnsureObservationsDedupIndexSkipsV2Schema(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "v2.db")+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureObservationsDedupIndex(db, t.Logf); err != nil {
		t.Fatalf("expected a skip on a v2 schema, got: %v", err)
	}
	if dedupIndexExists(t, db) {
		t.Error("index must not be created on a v2 schema")
	}
}
