// Test that the migrate binary brings the e2e fixture DB up to the
// shape required by cmd/server's dbschema.AssertReady. Regression test
// for PR #1289 / fix for the CI "Server failed to start within 30s"
// failure: AssertReady fired against the unmigrated fixture and the
// server fatal-logged before opening its HTTP listener.
package main

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbschema"
)

// fixtureCandidates lists possible locations of the committed e2e
// fixture DB relative to this test's package directory. We resolve
// against runtime cwd which is cmd/migrate when `go test` runs.
var fixtureCandidates = []string{
	"../../test-fixtures/e2e-fixture.db",
}

func locateFixture(t *testing.T) string {
	t.Helper()
	for _, p := range fixtureCandidates {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	t.Skipf("e2e fixture not found (looked in: %v)", fixtureCandidates)
	return ""
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create dst: %v", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy: %v", err)
	}
}

// TestMigrateBringsFixtureToReady is the gate test for the CI bug.
// Before the fix landed, AssertReady against the committed fixture
// returned an error ("missing: inactive_nodes.foreign_advert" etc.).
// After Apply(), AssertReady must return nil.
func TestMigrateBringsFixtureToReady(t *testing.T) {
	src := locateFixture(t)
	dst := filepath.Join(t.TempDir(), "fixture-copy.db")
	copyFile(t, src, dst)

	db, err := sql.Open("sqlite3", dst)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Sanity: the committed fixture is missing at least one expected
	// migration column. If this stops being true, either someone
	// pre-migrated the fixture (and this test no longer protects #1289)
	// or AssertReady's required set changed.
	if err := dbschema.AssertReady(db); err == nil {
		t.Logf("note: fixture already passes AssertReady; skipping pre-condition assertion")
	}

	if err := dbschema.Apply(db, t.Logf); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := dbschema.AssertReady(db); err != nil {
		t.Fatalf("AssertReady after Apply: %v", err)
	}
}
