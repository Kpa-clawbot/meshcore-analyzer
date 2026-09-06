// Package perfio holds the canonical PerfIOSample type shared between the
// ingestor (which publishes /proc/self/io rate samples to its on-disk stats
// file) and the server (which reads that file and surfaces the sample under
// /api/perf/io's `ingestor` block). Sharing the type prevents silent JSON
// contract drift if a field is added on one side only.
//
// The /proc/self/io key:value parser also lives here (Carmack #1167
// must-fix #7) so the two binaries don't carry divergent copies of the
// same parser — past divergence already produced a real bug (see must-fix
// #6: the parsedAny empty-key gate was added on one side only).
package perfio

import (
	"bufio"
	"strconv"
	"strings"
)

// Sample is the per-process I/O rate sample written by the ingestor and
// consumed by the server. Field names + json tags MUST be considered the
// stable on-disk contract — adding/renaming a field is a breaking change.
type Sample struct {
	ReadBytesPerSec           float64 `json:"readBytesPerSec"`
	WriteBytesPerSec          float64 `json:"writeBytesPerSec"`
	CancelledWriteBytesPerSec float64 `json:"cancelledWriteBytesPerSec"`
	SyscallsRead              float64 `json:"syscallsRead"`
	SyscallsWrite             float64 `json:"syscallsWrite"`
	SampledAt                 string  `json:"sampledAt,omitempty"`
}

// Counters is the raw /proc/self/io counter snapshot. Both the ingestor's
// procIOSnapshot and the server's procIOSample are thin wrappers around
// these fields plus a sampled-at timestamp; the parser populates Counters
// directly so there's exactly ONE implementation of the key:value walker.
type Counters struct {
	ReadBytes           int64
	WriteBytes          int64
	CancelledWriteBytes int64
	SyscR               int64
	SyscW               int64
}

// ParseProcIO reads /proc/self/io-shaped key:value lines from sc and
// populates c. Returns true iff at least one recognised key was
// successfully parsed (Carmack must-fix #6 — empty / no-known-keys input
// must NOT be treated as a valid sample, otherwise the next tick computes
// a phantom delta against zero counters).
func ParseProcIO(sc *bufio.Scanner, c *Counters) bool {
	parsedAny := false
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "read_bytes":
			c.ReadBytes = val
			parsedAny = true
		case "write_bytes":
			c.WriteBytes = val
			parsedAny = true
		case "cancelled_write_bytes":
			c.CancelledWriteBytes = val
			parsedAny = true
		case "syscr":
			c.SyscR = val
			parsedAny = true
		case "syscw":
			c.SyscW = val
			parsedAny = true
		}
	}
	return parsedAny
}
