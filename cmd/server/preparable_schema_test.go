package main

import (
	"database/sql"
	"strings"
	"testing"
)

// The statements compiled by prepareStatements reference a set of tables and
// columns. Under modernc.org/sqlite that never mattered for test fixtures:
// newStmt only stored the SQL string and compiled it lazily on first use, so a
// fixture could omit half the schema and still open. github.com/mattn/go-sqlite3
// calls sqlite3_prepare_v2 inside Prepare, so OpenDB now fails on an incomplete
// schema — which is the production behaviour we want (#1901), but it means
// fixtures have to declare what they are prepared against.
//
// ensurePreparable is the seam. Fixtures that deliberately build a partial
// schema (to exercise schema detection, say) call it on the writable connection
// just before OpenDB, and it fills in whatever is missing without disturbing the
// shapes the test actually cares about.
var preparableTables = []string{
	`CREATE TABLE IF NOT EXISTS transmissions (id INTEGER PRIMARY KEY, hash TEXT)`,
	`CREATE TABLE IF NOT EXISTS observations (id INTEGER PRIMARY KEY, timestamp TEXT)`,
	`CREATE TABLE IF NOT EXISTS nodes (public_key TEXT PRIMARY KEY)`,
	`CREATE TABLE IF NOT EXISTS observers (id TEXT)`,
}

// preparableColumns are added when absent. A fixture that already declared the
// table keeps its own definition, so only the missing columns get appended.
var preparableColumns = []struct{ table, column, decl string }{
	{"transmissions", "hash", "TEXT"},
	{"observations", "timestamp", "TEXT"},
	{"nodes", "name", "TEXT"},
	{"nodes", "role", "TEXT"},
	{"nodes", "last_seen", "TEXT"},
	{"observers", "inactive", "INTEGER"},
}

// ensurePreparable makes conn's schema sufficient for prepareStatements.
// It is idempotent and never overwrites an existing table or column.
func ensurePreparable(tb testing.TB, conn *sql.DB) {
	tb.Helper()
	for _, ddl := range preparableTables {
		if _, err := conn.Exec(ddl); err != nil {
			tb.Fatalf("ensurePreparable: %v\nSQL: %s", err, ddl)
		}
	}
	for _, c := range preparableColumns {
		_, err := conn.Exec("ALTER TABLE " + c.table + " ADD COLUMN " + c.column + " " + c.decl)
		// "duplicate column name" means the fixture already declared it.
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			tb.Fatalf("ensurePreparable: add %s.%s: %v", c.table, c.column, err)
		}
	}
}

// TestEnsurePreparableMatchesPrepareStatements keeps the helper honest. If a new
// prepared statement references a table or column ensurePreparable does not
// create, it fails here rather than as a confusing OpenDB error in whichever
// fixture happens to be thinnest.
func TestEnsurePreparableMatchesPrepareStatements(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// prepareStatements compiles against a pooled connection; pin the pool so
	// the in-memory database the DDL landed in is the one it prepares against.
	conn.SetMaxOpenConns(1)
	ensurePreparable(t, conn)

	db := &DB{conn: conn}
	if err := db.prepareStatements(); err != nil {
		t.Fatalf("prepareStatements against the minimal preparable schema: %v\n"+
			"Add the missing table/column to preparableTables or preparableColumns.", err)
	}
}
