package main

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MemorySnapshot is a point-in-time view of process memory across several
// vantage points. Values are in MB (1024*1024 bytes), rounded to one decimal.
//
// Field invariants (typical, not guaranteed under exotic conditions):
//
//	processRSSMB  >=  goSysMB  >=  goHeapInuseMB  >=  storeDataMB
//
//   - processRSSMB is what the kernel charges the process (resident set).
//     Read from /proc/self/status `VmRSS:` on Linux; falls back to goSysMB
//     on other platforms or when /proc is unavailable.
//   - goSysMB is the total memory obtained from the OS by the Go runtime
//     (heap, stacks, GC metadata, mspans, mcache, etc.). Includes
//     fragmentation and unused-but-mapped span overhead.
//   - goHeapInuseMB is the live, in-use Go heap (HeapInuse). Excludes
//     idle spans and runtime overhead.
//   - storeDataMB is the in-store packet byte estimate (transmissions +
//     observations). Subset of HeapInuse. Does not include index maps,
//     analytics caches, broadcast queues, or runtime overhead. Used as
//     the input to the eviction watermark.
//
// processRSSMB and storeDataMB are monotonic only relative to ingest +
// eviction; both can shrink when packets age out. goHeapInuseMB and goSysMB
// fluctuate with GC.
//
// cgoBytesMB intentionally absent: this build uses the pure-Go
// modernc.org/sqlite driver, so there is no cgo allocator to measure.
// Reintroduce only if we ever switch back to mattn/go-sqlite3.
type MemorySnapshot struct {
	ProcessRSSMB  float64 `json:"processRSSMB"`
	GoHeapInuseMB float64 `json:"goHeapInuseMB"`
	GoSysMB       float64 `json:"goSysMB"`
	StoreDataMB   float64 `json:"storeDataMB"`
}

// rssCache rate-limits the /proc/self/status read. Go memory stats are
// already cached by Server.getMemStats (5s TTL). We use a tighter 1s TTL
// here so processRSSMB stays reasonably fresh during ops debugging
// without paying the syscall cost on every /api/stats hit.
var (
	rssCacheMu       sync.Mutex
	rssCacheValueMB  float64
	rssCacheCachedAt time.Time
)

const rssCacheTTL = 1 * time.Second

// getMemorySnapshot composes a MemorySnapshot using the Server's existing
// runtime.MemStats cache (5s TTL, used by /api/health and /api/perf too)
// plus a rate-limited /proc RSS read. storeDataMB is supplied by the
// caller because the packet store is the source of truth.
func (s *Server) getMemorySnapshot(storeDataMB float64) MemorySnapshot {
	ms := s.getMemStats()

	rssCacheMu.Lock()
	if time.Since(rssCacheCachedAt) > rssCacheTTL {
		rssCacheValueMB = readProcRSSMB()
		rssCacheCachedAt = time.Now()
	}
	rssMB := rssCacheValueMB
	rssCacheMu.Unlock()

	if rssMB <= 0 {
		// Fallback when /proc is unavailable (non-Linux, sandboxes, etc.).
		// runtime.Sys is an upper bound on Go-attributable memory and a
		// reasonable proxy for pure-Go builds.
		rssMB = float64(ms.Sys) / 1048576.0
	}

	return MemorySnapshot{
		ProcessRSSMB:  roundMB(rssMB),
		GoHeapInuseMB: roundMB(float64(ms.HeapInuse) / 1048576.0),
		GoSysMB:       roundMB(float64(ms.Sys) / 1048576.0),
		StoreDataMB:   roundMB(storeDataMB),
	}
}

// readProcRSSMB parses /proc/self/status for the VmRSS line. Returns 0 on
// any failure (file missing, malformed line, parse error) — the caller
// then uses a runtime fallback. Linux only; macOS/Windows return 0.
//
// Safety notes (djb): the file path is hard-coded, no untrusted input is
// concatenated. We bound the read at 8 KiB (the whole status file is
// well under 4 KiB on modern kernels) so a corrupt /proc can't OOM us.
// We only parse digits with strconv; no shell, no exec, no format strings.
func readProcRSSMB() float64 {
	const maxStatusBytes = 8 * 1024
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()

	buf := make([]byte, maxStatusBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return 0
	}
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		// Format: "VmRSS:\t   123456 kB"
		fields := strings.Fields(line[len("VmRSS:"):])
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || kb < 0 {
			return 0
		}
		// Unit is kB per kernel convention; convert to MB.
		return kb / 1024.0
	}
	return 0
}

func roundMB(v float64) float64 {
	if v < 0 {
		return 0
	}
	return float64(int64(v*10+0.5)) / 10.0
}
