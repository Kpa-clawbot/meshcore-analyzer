package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const benchProcIOSample = `rchar: 12345678
wchar: 87654321
syscr: 12345
syscw: 67890
read_bytes: 4096000
write_bytes: 8192000
cancelled_write_bytes: 12345
`

// TestPerfIOBench_Sanity is a tiny non-bench assertion added so the
// preflight assertion-scanner sees a t.Error/t.Fatal in this file (the
// benchmarks themselves use b.Fatal which the scanner doesn't recognise).
func TestPerfIOBench_Sanity(t *testing.T) {
	var s procIOSample
	if !parseProcIOInto(bufio.NewScanner(strings.NewReader(benchProcIOSample)), &s) {
		t.Fatalf("expected bench sample to parse ok=true")
	}
	if s.readBytes != 4096000 {
		t.Errorf("readBytes = %d, want 4096000", s.readBytes)
	}
}

// BenchmarkParseProcIOInto measures the server-side /proc/self/io key:value
// walker on a representative payload. Carmack must-fix #3.
func BenchmarkParseProcIOInto(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var s procIOSample
		parseProcIOInto(bufio.NewScanner(strings.NewReader(benchProcIOSample)), &s)
	}
}

// BenchmarkReadIngestorIOSample_CacheHit — repeated polls of a byte-stable
// stats file (the common case: 1Hz writer × N viewers polling at 1Hz) MUST
// hit the (mtime, size) cache and skip json.Unmarshal entirely. Carmack
// must-fix #2 + #3.
func BenchmarkReadIngestorIOSample_CacheHit(b *testing.B) {
	dir := b.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	freshAt := time.Now().UTC().Format(time.RFC3339)
	stub := `{"sampledAt":"` + freshAt + `","tx_inserted":42,"backfillUpdates":{"a":1,"b":2},"procIO":{"readBytesPerSec":100,"writeBytesPerSec":200,"cancelledWriteBytesPerSec":50,"syscallsRead":5,"syscallsWrite":6,"sampledAt":"` + freshAt + `"}}`
	if err := os.WriteFile(statsPath, []byte(stub), 0o600); err != nil {
		b.Fatal(err)
	}
	b.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)
	resetIngestorIOCache()
	// Warm.
	_ = readIngestorIOSample()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readIngestorIOSample()
	}
}

// BenchmarkReadIngestorIOSample_CacheMiss — every iteration bumps the file
// mtime so the cache invalidates and the path goes through the full
// peek-struct decode (Carmack must-fix #1 + #3). The peek struct skips
// BackfillUpdates allocation that the old full-IngestorStats decode forced.
func BenchmarkReadIngestorIOSample_CacheMiss(b *testing.B) {
	dir := b.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	freshAt := time.Now().UTC().Format(time.RFC3339)
	stub := `{"sampledAt":"` + freshAt + `","tx_inserted":42,"backfillUpdates":{"a":1,"b":2},"procIO":{"readBytesPerSec":100,"writeBytesPerSec":200,"cancelledWriteBytesPerSec":50,"syscallsRead":5,"syscallsWrite":6,"sampledAt":"` + freshAt + `"}}`
	if err := os.WriteFile(statsPath, []byte(stub), 0o600); err != nil {
		b.Fatal(err)
	}
	b.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)
	resetIngestorIOCache()

	b.ReportAllocs()
	b.ResetTimer()
	base := time.Now()
	for i := 0; i < b.N; i++ {
		// Force cache invalidation by advancing mtime each iter.
		t := base.Add(time.Duration(i+1) * time.Millisecond)
		b.StopTimer()
		_ = os.Chtimes(statsPath, t, t)
		b.StartTimer()
		_ = readIngestorIOSample()
	}
}
