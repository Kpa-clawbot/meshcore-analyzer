package main

import (
	"path/filepath"
	"testing"
)

// TestOpenStorePragmas pins the pragmas the writer connection runs with.
//
// These were silently at risk during the modernc.org/sqlite →
// github.com/mattn/go-sqlite3 migration in two different ways:
//
//   - The DSN dialect changed. modernc understood only `_pragma=name(value)`;
//     mattn understands only `_`-prefixed parameters and ignores the other
//     form outright. A driver-only rename would have dropped every pragma on
//     the floor with no error anywhere.
//
//   - mattn runs `PRAGMA synchronous = NORMAL` unconditionally, defaulting the
//     mode itself to NORMAL rather than leaving SQLite's own FULL in place. So
//     the writer would have quietly moved from FULL (2) to NORMAL (1),
//     weakening durability under power loss. `_synchronous=FULL` in the DSN is
//     what holds it — this test is what notices if it is ever dropped.
//
// Read through the store's own connection on purpose: a separate connection, or
// the log line in OpenStoreWithInterval, would prove nothing about what the
// writer is actually using.
func TestOpenStorePragmas(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "pragma.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.db.Close()

	for _, want := range []struct {
		pragma string
		value  string
		why    string
	}{
		{"journal_mode", "wal", "concurrent reader (cmd/server) requires WAL"},
		{"synchronous", "2", "FULL; mattn defaults to 1 (NORMAL) unless the DSN says otherwise"},
		{"auto_vacuum", "2", "INCREMENTAL; maintenance.go drives incremental_vacuum"},
		{"foreign_keys", "1", "schema relies on FK enforcement"},
		{"busy_timeout", "5000", "writer serialises behind the reader"},
		{"cache_size", "-2000", "2 MiB of C-allocated page cache, pinned: it sits outside GOMEMLIMIT"},
	} {
		var got string
		if err := store.db.QueryRow("PRAGMA " + want.pragma).Scan(&got); err != nil {
			t.Errorf("PRAGMA %s: %v", want.pragma, err)
			continue
		}
		if got != want.value {
			t.Errorf("PRAGMA %s = %q, want %q (%s)", want.pragma, got, want.value, want.why)
		}
	}
}
