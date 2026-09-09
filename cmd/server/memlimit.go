package main

import (
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// cgroupUnlimitedThreshold is the sentinel above which a cgroup memory value
// means "no limit". cgroup v1 encodes unlimited as math.MaxInt64 (page-aligned
// near 1<<63); 1<<62 is a safe upper bound that excludes all real limits while
// staying well below the unlimited sentinel.
const cgroupUnlimitedThreshold = int64(1 << 62)

// applyMemoryLimit configures Go's soft memory limit (GOMEMLIMIT).
//
// Behavior:
//   - If envSet is true (GOMEMLIMIT env var present), the runtime has already
//     parsed it; we leave it alone and report source="env" with limit=0.
//   - Otherwise, if maxMemoryMB > 0, we derive a limit of maxMemoryMB * 1.5 MiB
//     and set it via debug.SetMemoryLimit. This forces aggressive GC under
//     cgroup pressure so the process self-throttles before SIGKILL. See #836.
//   - Otherwise, no limit is applied; source="none".
//
// Returns the limit (in bytes) we actually set, or 0 if we did not set one,
// plus a short source identifier ("env" | "derived" | "none") for logging.
func applyMemoryLimit(maxMemoryMB int, envSet bool) (int64, string) {
	if envSet {
		return 0, "env"
	}
	if maxMemoryMB <= 0 {
		return 0, "none"
	}
	// 1.5x headroom over the steady-state packet store budget covers
	// transient peaks (cold-load row-scan / decode pipeline, Go's NextGC
	// trigger at ~2x live heap). See issue #836 heap profile.
	//
	// GOMEMLIMIT covers memory the Go runtime manages — heap, stacks and
	// runtime structures — and nothing else. Since the SQLite driver became
	// cgo (github.com/mattn/go-sqlite3), what SQLite allocates in C is
	// outside it, so this limit is not an RSS ceiling: actual RSS is this
	// plus the native allocations plus whatever the runtime has reserved
	// but not returned.
	//
	// The page cache, at least, is deliberately bounded: both DSNs pin
	// _cache_size=-2000, i.e. ~2 MiB per connection, ~8 MiB across
	// SetMaxOpenConns(4). That fits inside the headroom above rather than
	// eating into the store budget. It does not bound SQLite's other native
	// allocations, so if the connection count or _cache_size grows
	// materially, or a workload starts holding many prepared statements,
	// this derivation needs revisiting against measured RSS.
	limit := int64(maxMemoryMB) * 1024 * 1024 * 3 / 2
	debug.SetMemoryLimit(limit)
	return limit, "derived"
}

// readCgroupMemoryMBFn is the package-level hook used by
// warnIfMemlimitUnderprovisioned. Tests override it to inject deterministic
// cgroup values without needing a Linux kernel with cgroup mounts.
var readCgroupMemoryMBFn = readCgroupMemoryMB

// readCgroupMemoryMB returns the container's memory limit from cgroup, in MiB.
// Returns 0 when unavailable (non-Linux, unlimited, or read error).
func readCgroupMemoryMB() int64 {
	// cgroup v2: single file, value in bytes or literal "max"
	if b, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		s := strings.TrimSpace(string(b))
		if s != "max" {
			if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
				return v / (1024 * 1024)
			}
		}
	}
	// cgroup v1: values near math.MaxInt64 represent "unlimited"
	if b, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			if v > 0 && v < cgroupUnlimitedThreshold {
				return v / (1024 * 1024)
			}
		}
	}
	return 0
}

// memlimitUnderprovisioned reports whether effectiveMB is less than half of
// cgroupMB. Extracted for unit testing the comparison boundary.
func memlimitUnderprovisioned(effectiveMB, cgroupMB int64) bool {
	return effectiveMB > 0 && cgroupMB > 0 && effectiveMB*2 < cgroupMB
}

// warnIfMemlimitUnderprovisioned logs a warning when GOMEMLIMIT is below 50%
// of the container cgroup memory limit, which causes the Go GC to thrash.
// In one reported incident (#1264) 82% of CPU was GC with a 1536 MiB limit
// on a 7.7 GB container — all endpoints 3-100x slower until maxMemoryMB was
// bumped and the process restarted.
//
// limitBytes is the value returned by applyMemoryLimit:
//   - source="derived": the limit we set ourselves (> 0)
//   - source="env":  0 — we did not touch the runtime; read it back below
//   - source="none": 0 — no limit set at all; runtime default is math.MaxInt64,
//     which the >= cgroupUnlimitedThreshold guard below catches and skips
func warnIfMemlimitUnderprovisioned(limitBytes int64) {
	cgroupMB := readCgroupMemoryMBFn()
	if cgroupMB <= 0 {
		return
	}
	effective := limitBytes
	if effective <= 0 {
		// Either GOMEMLIMIT was set via env (source="env") or no limit was
		// configured (source="none"). Read the runtime's current value:
		// - env case: returns whatever the operator set
		// - none case: returns math.MaxInt64, caught by the guard below
		// debug.SetMemoryLimit(-1) leaves the limit unchanged and returns it.
		effective = debug.SetMemoryLimit(-1)
	}
	if effective <= 0 || effective >= cgroupUnlimitedThreshold {
		return
	}
	effectiveMB := effective / (1024 * 1024)
	if memlimitUnderprovisioned(effectiveMB, cgroupMB) {
		log.Printf("[memlimit] WARN: GOMEMLIMIT=%d MiB is <50%% of container limit %d MiB — "+
			"GC may thrash under load; consider bumping packetStore.maxMemoryMB "+
			"(suggested: ~%d MiB, roughly 2/3 of container limit)",
			effectiveMB, cgroupMB, cgroupMB*2/3)
	}
}
