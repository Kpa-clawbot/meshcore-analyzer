package main

// Issue #1009: chunked Load with early HTTP readiness.
//
// These tests gate three behaviors:
//   (a) FirstChunkReady() unblocks BEFORE LoadChunked returns, so the
//       HTTP listener can bind after the first chunk completes while
//       remaining rows continue loading in the background.
//   (b) loadStatusMiddleware stamps an X-CoreScope-Load-Status header
//       with "loading" + progress while a load is in flight, flipping
//       to "ready" once LoadComplete() reports true.
//   (c) LoadChunked honors the configured chunkSize: the per-chunk
//       progress callback fires once per chunk, so a 2500-row DB with
//       chunkSize=1000 must yield 3 callbacks (1000 + 1000 + 500).
//
// Each subtest fails on an assertion (not a build error) when the
// production code is absent — that is the red-commit contract.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openChunkedTestStore(t *testing.T, numTx int) *PacketStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chunked.db")
	createTestDBAt(t, dbPath, numTx)
	t.Cleanup(func() { os.RemoveAll(dir) })

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	cfg := &PacketStoreConfig{}
	return NewPacketStore(db, cfg)
}

// (a) FirstChunkReady fires before LoadChunked returns.
func TestLoadChunked_FirstChunkReadyBeforeComplete(t *testing.T) {
	store := openChunkedTestStore(t, 2500)
	defer store.db.conn.Close()

	doneCh := make(chan error, 1)
	go func() { doneCh <- store.LoadChunked(500) }()

	select {
	case <-store.FirstChunkReady():
		// Good: first chunk signaled. Load may or may not have completed
		// for tiny test DBs, but the gate must have fired without
		// requiring the full load.
	case err := <-doneCh:
		// If load completed before we could observe the signal, the
		// signal still must be closed.
		if err != nil {
			t.Fatalf("LoadChunked: %v", err)
		}
		select {
		case <-store.FirstChunkReady():
		default:
			t.Fatal("FirstChunkReady channel must be closed after LoadChunked completes")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("FirstChunkReady did not fire within 10s — listener would never bind")
	}

	// Drain background completion.
	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("LoadChunked returned error: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("LoadChunked never returned")
	}

	if !store.LoadComplete() {
		t.Fatal("LoadComplete() must report true after LoadChunked returns")
	}
}

// (b) Middleware stamps X-CoreScope-Load-Status correctly across the
//
//	loading→ready transition.
func TestLoadStatusMiddleware_HeaderTransition(t *testing.T) {
	store := openChunkedTestStore(t, 100)
	defer store.db.conn.Close()

	handler := loadStatusMiddleware(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Pre-load: header must report "loading".
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if got := w.Header().Get("X-CoreScope-Load-Status"); got == "" || got == "ready" {
		t.Fatalf("expected loading status header before Load, got %q", got)
	}

	if err := store.LoadChunked(50); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	// Post-load: header must report "ready".
	req2 := httptest.NewRequest("GET", "/api/healthz", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if got := w2.Header().Get("X-CoreScope-Load-Status"); got != "ready" {
		t.Fatalf("expected X-CoreScope-Load-Status=ready after load, got %q", got)
	}
}

// (c) LoadChunked honors the chunkSize argument — progress callback
//
//	fires once per chunk.
func TestLoadChunked_ChunkSizeHonored(t *testing.T) {
	store := openChunkedTestStore(t, 2500)
	defer store.db.conn.Close()

	var chunks []int
	store.OnChunkLoaded(func(rowsThisChunk, totalRows int) {
		chunks = append(chunks, rowsThisChunk)
	})

	if err := store.LoadChunked(1000); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks for 2500 rows @ chunkSize=1000, got %d (sizes=%v)", len(chunks), chunks)
	}
	if chunks[0] != 1000 || chunks[1] != 1000 || chunks[2] != 500 {
		t.Fatalf("expected chunk sizes [1000,1000,500], got %v", chunks)
	}
}

// (d) Config plumbing: DB.Load.ChunkSize threads through.
func TestConfig_DBLoadChunkSize(t *testing.T) {
	c := &Config{}
	if got := c.DBLoadChunkSize(); got != 10000 {
		t.Fatalf("DBLoadChunkSize() default = %d, want 10000", got)
	}
	c.DB = &DBConfig{Load: &dbLoadConfig{ChunkSize: 2500}}
	if got := c.DBLoadChunkSize(); got != 2500 {
		t.Fatalf("DBLoadChunkSize() configured = %d, want 2500", got)
	}
}
