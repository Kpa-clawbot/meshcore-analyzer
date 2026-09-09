package dbschema

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestWriterDSNPragmas pins what every writer connection actually gets.
//
// cmd/ingestor has its own end-to-end version of this through the store, but
// this one covers the DSN itself, which is what cmd/migrate also opens with.
// The synchronous line matters most: mattn defaults it to NORMAL and runs the
// pragma unconditionally, so if it ever drops out of the DSN the durability
// change is silent.
func TestWriterDSNPragmas(t *testing.T) {
	db, err := sql.Open("sqlite3", WriterDSN(filepath.Join(t.TempDir(), "w.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	for _, want := range []struct{ pragma, value, why string }{
		{"journal_mode", "wal", "cmd/server reads concurrently"},
		{"synchronous", "2", "FULL; mattn defaults to 1 (NORMAL) unless the DSN says otherwise"},
		{"auto_vacuum", "2", "INCREMENTAL; maintenance.go drives incremental_vacuum"},
		{"foreign_keys", "1", "the schema relies on FK enforcement"},
		{"busy_timeout", "5000", "writers serialise behind the reader"},
		{"cache_size", "-2000", "C-allocated page cache, pinned: it sits outside GOMEMLIMIT"},
	} {
		var got string
		if err := db.QueryRow("PRAGMA " + want.pragma).Scan(&got); err != nil {
			t.Errorf("PRAGMA %s: %v", want.pragma, err)
			continue
		}
		if got != want.value {
			t.Errorf("PRAGMA %s = %q, want %q (%s)", want.pragma, got, want.value, want.why)
		}
	}
}
