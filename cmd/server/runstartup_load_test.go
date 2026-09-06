package main

// Tests for RunStartupLoad branch behavior and #1809 invariants
// (PR #1811 round-1 followups B2/B3/B4/B5).
//
// The pre-#1811 RunStartupLoad left several steady states undefined:
//   * hotStartupHours == 0 → backgroundLoadDone stayed false forever
//   * LoadChunked error    → both done & failed stayed false
//   * empty DB + no bg work needed → backgroundLoadDone stayed false
//
// These tests codify the post-#1811 contract:
//   * LoadChunked error → backgroundLoadFailed=true, done=false
//   * hotStartupHours == 0 → backgroundLoadDone=true, failed=false,
//     bg loader NOT called
//   * empty DB + hot window → backgroundLoadDone reflects coverage
//     (1.0 on empty DB → done=true, failed=false)
//   * call ordering inside RunStartupLoad: LoadChunked completes
//     before loadBackgroundChunks executes (so oldestLoaded is set)

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunStartupLoad_HotStartupHoursZero_SetsDoneImmediately covers B3:
// when hotStartupHours == 0 the bg loader has no work to do; healthz
// must NOT be stuck on backgroundLoadComplete=false.
func TestRunStartupLoad_HotStartupHoursZero_SetsDoneImmediately(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 10, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 0, // disable hot window → bg loader must not run
	})

	if err := store.RunStartupLoad(500); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}
	if !store.backgroundLoadDone.Load() {
		t.Fatalf("backgroundLoadDone must be true when hotStartupHours=0 (no bg work needed)")
	}
	if store.backgroundLoadFailed.Load() {
		t.Fatalf("backgroundLoadFailed must be false on the no-bg-work path; got error=%q",
			store.BackgroundLoadError())
	}
}

// TestRunStartupLoad_LoadChunkedError_SetsFailedTerminal covers B2:
// when LoadChunked errors, the steady state must be terminal — both
// done=true (LoadChunked contract: done is the primary observable
// signal, see store.go contract block) AND failed=true. The pre-#1811
// implementation left done=false, leaving healthz wedged on the
// failure path (dij #1 / adv #7).
func TestRunStartupLoad_LoadChunkedError_SetsFailedTerminal(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 5, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	// Close the underlying connection to force LoadChunked to fail on
	// its very first query. We're explicitly verifying the failure path
	// terminal state, not the success path.
	_ = db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	loadErr := store.RunStartupLoad(500)
	if loadErr == nil {
		t.Fatalf("RunStartupLoad must return an error when LoadChunked fails")
	}
	if !store.backgroundLoadFailed.Load() {
		t.Fatalf("backgroundLoadFailed must be true after LoadChunked error (terminal state)")
	}
	if !store.backgroundLoadDone.Load() {
		t.Fatalf("backgroundLoadDone must be true on LoadChunked error (LoadChunked contract: done observable, qualified by failed)")
	}
	if store.BackgroundLoadError() == "" {
		t.Fatalf("BackgroundLoadError must be non-empty after LoadChunked failure")
	}
}

// TestRunStartupLoad_EmptyDB_SetsDoneTerminal covers B4: empty DB with
// hot window > 0 — oldestLoaded stays "" because there are no packets.
// loadBackgroundChunks must reach its coverage block (totalInDB==0 →
// ratio=1.0) and set done=true rather than leaving the store stuck.
func TestRunStartupLoad_EmptyDB_SetsDoneTerminal(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	createTestDBWithLastSeen(t, dbPath, 0, 0, time.Now().UTC().Unix(),
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	if err := store.RunStartupLoad(500); err != nil {
		t.Fatalf("RunStartupLoad on empty DB: %v", err)
	}
	if !store.backgroundLoadDone.Load() {
		t.Fatalf("backgroundLoadDone must be true after empty-DB load (nothing to load == complete)")
	}
	if store.backgroundLoadFailed.Load() {
		t.Fatalf("backgroundLoadFailed must be false on empty DB; got %q",
			store.BackgroundLoadError())
	}
}

// TestRunStartupLoad_BgLoaderRunsAfterLoadChunkedSets_OldestLoaded
// covers B5/B6 (dij #3, adv #3): assert the in-process call ordering
// inside RunStartupLoad — bg loader entry MUST happen-after the final
// OnChunkLoaded callback (which is the last write to s.oldestLoaded
// inside LoadChunked).
//
// The previous version only asserted that OnChunkLoaded fired and
// that oldestLoaded was non-empty after RunStartupLoad returned —
// which proves existence but not ordering (a future refactor could
// re-introduce the parallel-spawn race and the test would still
// pass because both signals are observable post-hoc).
//
// This version captures timestamps from each side (chunk callback,
// bg-loader entry) via a test-only hook and asserts
// tBgEntry >= tLastChunk monotonically. The runtime invariant in
// loadBackgroundChunks remains as the deterministic backstop.
func TestRunStartupLoad_BgLoaderRunsAfterLoadChunkedSets_OldestLoaded(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 50, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	var (
		mu             sync.Mutex
		lastChunkAt    time.Time
		bgEntryAt      time.Time
		chunkSawOldest string
	)
	store.OnChunkLoaded(func(rowsThisChunk, totalRows int) {
		mu.Lock()
		lastChunkAt = time.Now()
		// LoadChunked sets oldestLoaded under s.mu AFTER the last
		// fireChunkCallbacks call returns, so this read may be empty
		// on the last chunk — record whatever is visible.
		store.mu.RLock()
		chunkSawOldest = store.oldestLoaded
		store.mu.RUnlock()
		mu.Unlock()
	})
	store.bgLoaderEntryHook = func() {
		mu.Lock()
		bgEntryAt = time.Now()
		mu.Unlock()
	}

	if err := store.RunStartupLoad(500); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if lastChunkAt.IsZero() {
		t.Fatalf("OnChunkLoaded never fired — LoadChunked did not process any chunks")
	}
	if bgEntryAt.IsZero() {
		t.Fatalf("bgLoaderEntryHook never fired — loadBackgroundChunks did not enter")
	}
	if !bgEntryAt.After(lastChunkAt) && !bgEntryAt.Equal(lastChunkAt) {
		t.Fatalf("ordering violation: bg loader entered at %s BEFORE last chunk callback at %s — #1809 race re-introduced",
			bgEntryAt.Format(time.RFC3339Nano), lastChunkAt.Format(time.RFC3339Nano))
	}
	if store.oldestLoaded == "" {
		t.Fatalf("oldestLoaded is empty after RunStartupLoad — bg loader would have read \"\" and bailed")
	}
	_ = chunkSawOldest // diagnostic only; kept to make race future-debug easier
}

// TestRunStartupLoad_LoadChunkedError_MidLoad covers dij #6 / adv #5:
// the existing TestRunStartupLoad_LoadChunkedError_SetsFailedTerminal
// closes the conn BEFORE LoadChunked starts, so the error surfaces on
// chunk 0's first query. That's a degenerate path — it does not prove
// mid-load failure behavior. This test fires OnChunkLoaded once,
// closes the DB conn from the callback, and asserts the chunk-N query
// fails and the terminal state is still failed=true,done=true with a
// non-empty error.
func TestRunStartupLoad_LoadChunkedError_MidLoad(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	nowSec := time.Now().UTC().Unix()
	// Seed enough rows so a small chunk size yields >1 chunk.
	createTestDBWithLastSeen(t, dbPath, 200, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	// Close the underlying conn from inside the chunk callback so the
	// NEXT chunk's query fails mid-load instead of on the first query.
	var closed atomic.Bool
	store.OnChunkLoaded(func(rowsThisChunk, totalRows int) {
		if closed.CompareAndSwap(false, true) {
			_ = db.conn.Close()
		}
	})

	loadErr := store.RunStartupLoad(50)
	if loadErr == nil {
		t.Fatalf("RunStartupLoad must error when conn closes mid-load")
	}
	if !store.backgroundLoadFailed.Load() {
		t.Fatalf("backgroundLoadFailed must be true after mid-load error")
	}
	if !store.backgroundLoadDone.Load() {
		t.Fatalf("backgroundLoadDone must be true after mid-load error (terminal contract)")
	}
	if !strings.Contains(store.BackgroundLoadError(), "LoadChunked failed") {
		t.Fatalf("BackgroundLoadError must mention LoadChunked failure; got %q", store.BackgroundLoadError())
	}
}

// TestLoadBackgroundChunks_PanicsOnOldestLoadedEmpty_Invariant covers the
// runtime assertion (A7). Manually populate s.packets without setting
// oldestLoaded and call loadBackgroundChunks directly — the panic guard
// must fire so future refactors cannot silently re-introduce the
// #1809 race.
func TestLoadBackgroundChunks_PanicsOnOldestLoadedEmpty_Invariant(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// Reuse the existing schema-only fixture helper (0 rows) so this
	// test does not introduce a new inline CREATE TABLE block (pr-preflight
	// async-migration gate). The fixture provides exactly the bare schema
	// loadBackgroundChunks needs to reach its panic guard.
	createTestDBWithLastSeen(t, dbPath, 0, 0, time.Now().UTC().Unix(),
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})
	// Simulate the #1809 race: packets present, oldestLoaded never set.
	store.mu.Lock()
	store.packets = append(store.packets, &StoreTx{ID: 1, Hash: "deadbeef", FirstSeen: "2025-01-01T00:00:00Z"})
	store.oldestLoaded = ""
	store.mu.Unlock()

	// Override invariantViolation so the test can recover() and
	// assert the message without log.Fatalf killing the runner
	// (adv #6: prod uses log.Fatalf for a clean shutdown). Restore on
	// exit so other tests are unaffected.
	prev := invariantViolation
	defer func() { invariantViolation = prev }()
	invariantViolation = func(msg string) { panic(msg) }

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("loadBackgroundChunks must trip invariantViolation when oldestLoaded=\"\" with packets in store (#1809 invariant)")
		}
		msg := fmt.Sprintf("%v", r)
		// adv #4: tighten the tripwire — assert the specific invariant
		// message contains the issue tag so a future refactor that
		// reuses the panic path for a different bug doesn't silently
		// satisfy this test.
		if !strings.Contains(msg, "#1809") {
			t.Fatalf("invariant message must reference #1809 to gate future regressions; got %q", msg)
		}
		if !strings.Contains(msg, "oldestLoaded") {
			t.Fatalf("invariant message must mention oldestLoaded; got %q", msg)
		}
	}()
	store.loadBackgroundChunks()
}
