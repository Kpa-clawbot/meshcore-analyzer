package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// benchReachDB builds an in-memory DB with nObs observations. matchEvery
// controls payload mix: 1 = every row contains the "01FA" token (worst case),
// 2 = every other row matches (the rest carry an unrelated path), etc. This
// lets benches measure the scan over a realistic mix, not just all-matching.
func benchReachDB(b *testing.B, nObs, matchEvery int, lowerHops bool) *DB {
	b.Helper()
	if matchEvery < 1 {
		matchEvery = 1
	}
	matchPath, fillerPath := `["AA","01FA","BB"]`, `["AA","CC","BB"]`
	if lowerHops {
		// Lowercase hops force parsePathTokens' ToUpper to allocate (production
		// path_json is uppercase; this measures the worst case Carmack flagged).
		matchPath, fillerPath = `["aa","01fa","bb"]`, `["aa","cc","bb"]`
	}
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	schema := []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, hash TEXT, first_seen TEXT, payload_type INTEGER, from_pubkey TEXT)`,
		`CREATE TABLE observers (id TEXT PRIMARY KEY, name TEXT, inactive INTEGER)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_idx INTEGER, snr REAL, path_json TEXT, timestamp INTEGER)`,
		`CREATE INDEX idx_obs_ts ON observations(timestamp)`,
	}
	for _, s := range schema {
		if _, err := conn.Exec(s); err != nil {
			b.Fatal(err)
		}
	}
	tx, err := conn.Begin()
	if err != nil {
		b.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO observers (id, name) VALUES ('OBS', 'o')`); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < nObs; i++ {
		if _, err := tx.Exec(`INSERT INTO transmissions (id, hash, first_seen, payload_type, from_pubkey) VALUES (?,?,?,5,'')`,
			i, fmt.Sprintf("h%d", i), "2026-06-07T00:00:00Z"); err != nil {
			b.Fatal(err)
		}
		path := fillerPath // non-matching filler
		if i%matchEvery == 0 {
			path = matchPath
		}
		if _, err := tx.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, snr, path_json, timestamp) VALUES (?,?,1,-7.0,?,?)`,
			i, i, path, 1000); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return &DB{conn: conn}
}

// BenchmarkNodeReachScan measures the windowed scan + path-decode at increasing
// scale, all-matching (worst case for memory/allocs).
func BenchmarkNodeReachScan(b *testing.B) {
	tokens := map[string]bool{"01FA": true}
	for _, n := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			db := benchReachDB(b, n, 1, false)
			srv := &Server{db: db}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, _ := srv.scanReachRows(context.Background(), tokens, 0)
				if len(rows) == 0 {
					b.Fatal("expected rows")
				}
			}
		})
	}
}

// BenchmarkNodeReachScanMixed measures the scan when only half the windowed
// rows actually contain the token — closer to production path mixes.
func BenchmarkNodeReachScanMixed(b *testing.B) {
	tokens := map[string]bool{"01FA": true}
	db := benchReachDB(b, 100000, 2, false)
	srv := &Server{db: db}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, _ := srv.scanReachRows(context.Background(), tokens, 0)
		if len(rows) == 0 {
			b.Fatal("expected rows")
		}
	}
}

// BenchmarkNodeReachScanLowerCase measures the worst case for path decoding:
// lowercase hops force parsePathTokens' ToUpper to allocate a new string per
// hop (production path_json is uppercase, where ToUpper is a no-op). Publishing
// this alongside the all-uppercase numbers keeps the perf claims honest.
func BenchmarkNodeReachScanLowerCase(b *testing.B) {
	tokens := map[string]bool{"01FA": true}
	db := benchReachDB(b, 100000, 1, true)
	srv := &Server{db: db}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, _ := srv.scanReachRows(context.Background(), tokens, 0)
		if len(rows) == 0 {
			b.Fatal("expected rows")
		}
	}
}

// BenchmarkNodeReachAttribute measures the directional attribution pass over an
// already-scanned row set (the in-memory hot loop + map building), isolated
// from DB I/O.
func BenchmarkNodeReachAttribute(b *testing.B) {
	tokens := map[string]bool{"01FA": true}
	db := benchReachDB(b, 100000, 1, false)
	srv := &Server{db: db}
	rows, _ := srv.scanReachRows(context.Background(), tokens, 0)
	if len(rows) == 0 {
		b.Fatal("expected rows")
	}
	resolve := func(tok string) string {
		switch tok {
		case "AA":
			return "aa00000000000000"
		case "BB":
			return "bb00000000000000"
		}
		return ""
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := attributeDirections(rows, tokens, "01fa326b", resolve)
		if d.relay == 0 {
			b.Fatal("expected relay hits")
		}
	}
}

// TestScanReachRows_ErrorReturn anchors the new ([]pathRow, error) signature
// at the unit-level (issue #1631). Passing a Server whose db.conn is closed
// must surface an error, not a swallowed nil. Lives in this file because
// the bench callers in the same file rely on the same signature.
func TestScanReachRows_ErrorReturn(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// PREFLIGHT: async=true reason="test-only in-memory scratch schema, immediately closed"
	if _, err := conn.Exec(`CREATE TABLE observations (id INTEGER); CREATE TABLE transmissions (id INTEGER); CREATE TABLE observers (rowid INTEGER, id TEXT, inactive INTEGER)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	conn.Close() // force QueryContext to fail
	srv := &Server{db: &DB{conn: conn}}
	rows, err := srv.scanReachRows(context.Background(), map[string]bool{"01FA": true}, 0)
	if err == nil {
		t.Fatalf("expected error from closed DB, got nil (rows=%d)", len(rows))
	}
	if rows != nil {
		t.Fatalf("expected nil rows on error, got %d", len(rows))
	}
}
