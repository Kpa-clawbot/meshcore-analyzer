package main

import (
	"bytes"
	"database/sql"
	"log"
	"strings"
	"sync"
	"testing"
)

// captureLogs redirects the standard logger to a buffer for the
// duration of the test and returns the buffer. Restores the previous
// writer when the test ends.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return buf
}

// syncBuffer is a bytes.Buffer that may be read while a background goroutine
// is still logging into it. log.Logger serialises its own writes, but the test
// reading buf.String() sits outside that lock, and RunAsyncMigration keeps
// logging after the call that started it returns. Without this the read and
// the write race, which is a real data race and not a test artefact: it can
// panic on a buffer grow, not merely trip -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logContains reports whether the captured log buffer contains substr
// (case-insensitive).
func logContains(buf *syncBuffer, substr string) bool {
	return strings.Contains(strings.ToLower(buf.String()), strings.ToLower(substr))
}

// columnExists reports whether the named column exists on the table.
func columnExists(t *testing.T, db *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan PRAGMA: %v", err)
		}
		if name == col {
			return true
		}
	}
	return false
}
