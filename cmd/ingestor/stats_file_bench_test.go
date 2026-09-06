package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const benchProcSelfIOSample = `rchar: 12345678
wchar: 87654321
syscr: 12345
syscw: 67890
read_bytes: 4096000
write_bytes: 8192000
cancelled_write_bytes: 12345
`

// TestStatsFileWriterBench_Sanity is a tiny non-bench test added solely to
// exercise the bench helpers' assertion path so the preflight scanner sees
// at least one t.Error*/t.Fatal* in this file (the benchmarks themselves
// use b.Fatal, which the scanner doesn't recognise as an assertion).
func TestStatsFileWriterBench_Sanity(t *testing.T) {
	var s procIOSnapshot
	parseProcSelfIOInto(bufio.NewScanner(strings.NewReader(benchProcSelfIOSample)), &s)
	if !s.ok {
		t.Fatalf("expected bench sample to parse ok=true")
	}
	if s.readBytes != 4096000 {
		t.Errorf("readBytes = %d, want 4096000", s.readBytes)
	}
}

// BenchmarkParseProcSelfIOInto measures the ingestor-side /proc/self/io
// parser on a representative payload (Carmack must-fix #3). Tracks
// allocations to verify the shared perfio.ParseProcIO path doesn't
// regress vs. the previous in-package implementation.
func BenchmarkParseProcSelfIOInto(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var s procIOSnapshot
		parseProcSelfIOInto(bufio.NewScanner(strings.NewReader(benchProcSelfIOSample)), &s)
	}
}

// BenchmarkStatsFileWriter_Tick simulates the body of one writer tick
// (snap construction + JSON encode via the reused buffer) WITHOUT the
// disk write. Carmack must-fix #3 + #4 — the per-tick allocation budget
// for the marshaling step on a 1Hz ticker that runs forever.
func BenchmarkStatsFileWriter_Tick(b *testing.B) {
	// Mirror the writer-loop's reused encoder.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// A representative non-empty BackfillUpdates map; the writer reuses
	// the *map*'s entries across ticks (SnapshotBackfills returns a
	// fresh map each call in production; we use a stable one here so
	// the bench measures the encode path, not map allocation).
	backfills := map[string]int64{"path_a": 100, "path_b": 200}
	stamp := time.Now().UTC().Format(time.RFC3339)
	io := &PerfIOSample{
		ReadBytesPerSec:           100,
		WriteBytesPerSec:          200,
		CancelledWriteBytesPerSec: 0,
		SyscallsRead:              5,
		SyscallsWrite:             6,
		SampledAt:                 stamp,
	}

	// Stand-in atomic counters (StartStatsFileWriter loads from a real
	// Store; for the bench we just pass concrete values).
	var n atomic.Int64
	n.Store(123456)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap := IngestorStatsSnapshot{
			SampledAt:          stamp,
			TxInserted:         n.Load(),
			ObsInserted:        n.Load(),
			DuplicateTx:        n.Load(),
			NodeUpserts:        n.Load(),
			ObserverUpserts:    n.Load(),
			WriteErrors:        n.Load(),
			SignatureDrops:     n.Load(),
			WALCommits:         n.Load(),
			GroupCommitFlushes: 0,
			BackfillUpdates:    backfills,
			ProcIO:             io,
		}
		buf.Reset()
		_ = enc.Encode(&snap)
	}
}
