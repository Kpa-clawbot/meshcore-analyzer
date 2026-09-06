package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/meshcore-analyzer/perfio"
)

// PerfIOSample is the canonical per-process I/O rate sample, sourced from the
// shared internal/perfio package. The server consumes the same type when it
// reads this binary's stats file — sharing the type prevents silent JSON
// contract drift (#1167 follow-up).
type PerfIOSample = perfio.Sample

// IngestorStatsSnapshot mirrors the JSON shape consumed by the server's
// /api/perf/write-sources endpoint (see cmd/server/perf_io.go IngestorStats).
//
// NOTE: each field below is sampled with an independent atomic.Load(), so the
// snapshot is EVENTUALLY-CONSISTENT — invariants like
// `walCommits >= tx_inserted` may be momentarily violated
// in a single sample. Consumers MUST NOT derive ratios on the assumption these
// counters were captured at the same instant; treat each field as an
// independent monotonically-increasing counter and look at deltas across
// multiple samples instead.
type IngestorStatsSnapshot struct {
	SampledAt          string           `json:"sampledAt"`
	TxInserted         int64            `json:"tx_inserted"`
	ObsInserted        int64            `json:"obs_inserted"`
	DuplicateTx        int64            `json:"tx_dupes"`
	NodeUpserts        int64            `json:"node_upserts"`
	ObserverUpserts    int64            `json:"observer_upserts"`
	WriteErrors        int64            `json:"write_errors"`
	SignatureDrops     int64            `json:"sig_drops"`
	WALCommits         int64            `json:"walCommits"`
	GroupCommitFlushes int64            `json:"groupCommitFlushes"` // always 0 — group commit reverted (refs #1129)
	BackfillUpdates    map[string]int64 `json:"backfillUpdates"`
	// ProcIO is the ingestor's own /proc/self/io rate snapshot. Surfaced via
	// the server's /api/perf/io endpoint under .ingestor (#1120 — "Both
	// ingestor and server"). Optional; absent on non-Linux hosts.
	ProcIO *PerfIOSample `json:"procIO,omitempty"`
	// WriterPerf is the per-component SQLite writer-lock latency
	// snapshot (#1340) — wait_ms / hold_ms / contention_total tagged
	// by component (neighbor_builder, mqtt_handler, prune_packets,
	// prune_observers, prune_metrics, vacuum). Surfaced by the server
	// via /api/perf/write-sources under .writer_perf. Optional —
	// older ingestor builds don't publish this field.
	WriterPerf map[string]WriterStatsSnapshot `json:"writer_perf,omitempty"`
	// SourceLiveness (PR #1609 M1) is the per-MQTT-source receipt vs
	// write-path liveness snapshot. Keyed by source Tag. Surfaced by
	// the server via /api/healthz under .ingest_liveness so operators
	// can see "broker alive, write path stuck" (lastReceiptUnix recent,
	// lastMessageUnix stale) distinct from "everything stalled" (both
	// stale). Additive: omitempty so older server builds ignore it
	// gracefully.
	SourceLiveness map[string]SourceLivenessSnapshot `json:"source_liveness,omitempty"`
	// SourceStatuses (#1043) is the per-MQTT-source connection state and
	// counter view consumed by cmd/server's /api/mqtt/status handler.
	// Additive; omitempty so older server builds ignore it.
	SourceStatuses []SourceStatusSnapshot `json:"source_statuses,omitempty"`
	// WatchdogLastTickUnix (#1749) is the unix-seconds timestamp of the
	// most recent runLivenessWatchdogLoop tick. Surfaced via
	// /api/mqtt/status so external monitoring can assert the watchdog
	// goroutine is still alive — a value older than ~2× the watchdog
	// scan interval (typically 60s) indicates the watchdog itself has
	// wedged or panicked. 0 means the watchdog has never ticked
	// (e.g. cold start before the first scan). Additive — omitempty
	// so older server builds ignore it.
	WatchdogLastTickUnix int64 `json:"watchdogLastTickUnix,omitempty"`
	// WatchdogPanicCount (#1810 round-1) is the running total of
	// recovered panics inside the watchdog per-source work IIFE.
	// Exposed alongside WatchdogLastTickUnix because the tick clock is
	// stamped BEFORE the per-source work — a loop that panics on every
	// source still advances WatchdogLastTickUnix and looks healthy by
	// that signal alone. A rapidly-growing WatchdogPanicCount means
	// the loop is alive but per-source processing is broken (typically
	// a panic in emit / log sink). Monotonic; 0 means no recovered
	// panics yet. Additive — omitempty so older server builds ignore.
	WatchdogPanicCount int64 `json:"watchdogPanicCount,omitempty"`
	// WatchdogLogDropCount (#1749 root-cause fix) is the running total
	// of watchdog log lines dropped by the async emit queue because
	// the background writer goroutine could not keep up — almost
	// always because its underlying write() is itself stuck (Docker
	// JSON-file log driver backpressure, full stderr pipe, etc.).
	// Surfaced alongside WatchdogLastTickUnix / WatchdogPanicCount so
	// external monitoring can distinguish "watchdog dead" (stale tick)
	// from "watchdog alive, but its log sink is stuck" (ticking
	// normally, drop count climbing). Monotonic; 0 means the writer
	// has never fallen behind. Additive — omitempty so older server
	// builds ignore it.
	WatchdogLogDropCount int64 `json:"watchdogLogDropCount,omitempty"`
}

// SourceLivenessSnapshot is the per-source two-clock view exposed for
// /api/healthz consumers. unixSeconds for both fields; 0 means "never".
type SourceLivenessSnapshot struct {
	LastReceiptUnix int64 `json:"lastReceiptUnix"`
	LastMessageUnix int64 `json:"lastMessageUnix"`
}

// statsFilePath returns the writable path the ingestor will publish stats to.
// Override via env CORESCOPE_INGESTOR_STATS for tests / non-default deploys.
//
// SECURITY: the default lives in /tmp which is world-writable. The writer uses
// O_NOFOLLOW + 0o600 so a pre-planted symlink cannot be used to clobber an
// arbitrary file via this path. Operators who want stronger guarantees should
// point CORESCOPE_INGESTOR_STATS at a private directory (e.g. /var/lib/corescope/).
func statsFilePath() string {
	if p := os.Getenv("CORESCOPE_INGESTOR_STATS"); p != "" {
		return p
	}
	return "/tmp/corescope-ingestor-stats.json"
}

// writeStatsAtomic writes b to path via a tmp-then-rename, refusing to follow
// symlinks on the tmp file. Returns nil on success, an error otherwise.
//
// Symlink semantics (refs #1170):
//
//   - tmp side (path+".tmp"): protected by O_NOFOLLOW below. If tmp is a
//     pre-planted symlink, openat fails with ELOOP instead of writing
//     through it. This is the defensive-coding path that matters when the
//     default stats path lives under world-writable /tmp.
//
//   - rename side (path): NOT protected by O_NOFOLLOW. Instead, os.Rename's
//     semantics are relied upon — rename atomically replaces any existing
//     entry at path (including a symlink) with the new regular file. The
//     symlink's target is NEVER written through, because all writes happened
//     to the unrelated tmp file before rename. Post-rename, path is a
//     regular file (not a symlink) and any prior symlink target's contents
//     are unchanged. The regression guardrail
//     TestWriteStatsAtomic_SymlinkAtDestIsReplaced pins this behavior so a
//     future refactor that swaps os.Rename for a destination-symlink-
//     following primitive (e.g. an open(path, O_WRONLY) without O_NOFOLLOW)
//     fails loudly.
func writeStatsAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	// O_NOFOLLOW: if tmp is a pre-existing symlink, openat fails with ELOOP
	// instead of clobbering the symlink target. O_TRUNC zeroes existing
	// regular-file content. 0o600 — no need for world-readable.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|oNoFollow, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// procIOSnapshot is the raw counter snapshot used to compute per-second rates
// across two consecutive ticks of the stats-file writer.
type procIOSnapshot struct {
	at             time.Time
	readBytes      int64
	writeBytes     int64
	cancelledWrite int64
	syscR          int64
	syscW          int64
	ok             bool
}

// readProcSelfIOFn is the package-level hook the writer loop uses to read
// /proc/self/io. Defaults to readProcSelfIO; tests override it to inject
// deterministic counter snapshots without depending on a Linux kernel
// that exposes /proc/self/io (CONFIG_TASK_IO_ACCOUNTING).
var readProcSelfIOFn = readProcSelfIO

// readProcSelfIO parses /proc/self/io. Returns ok=false on non-Linux hosts or
// any read/parse failure (caller skips the procIO block in that case).
func readProcSelfIO() procIOSnapshot {
	f, err := os.Open("/proc/self/io")
	if err != nil {
		return procIOSnapshot{}
	}
	defer f.Close()
	out := procIOSnapshot{at: time.Now()}
	parseProcSelfIOInto(bufio.NewScanner(f), &out)
	return out
}

// parseProcSelfIOInto reads /proc/self/io-shaped key:value lines from sc and
// populates the byte/syscall fields on out. Sets out.ok=true only if at
// least one expected key was successfully parsed (#1167 must-fix #3).
//
// Implementation delegates to perfio.ParseProcIO so the ingestor and the
// server share exactly one parser (Carmack must-fix #7).
func parseProcSelfIOInto(sc *bufio.Scanner, out *procIOSnapshot) {
	var c perfio.Counters
	out.ok = perfio.ParseProcIO(sc, &c)
	out.readBytes = c.ReadBytes
	out.writeBytes = c.WriteBytes
	out.cancelledWrite = c.CancelledWriteBytes
	out.syscR = c.SyscR
	out.syscW = c.SyscW
}

// procIORate computes a per-second rate sample between two procIOSnapshots
// using the supplied stamp string for the resulting Sample.SampledAt
// (Carmack must-fix #5 — the writer captures time.Now() once per tick and
// passes the same RFC3339 string down so the snapshot top-level SampledAt
// and the inner procIO SampledAt cannot drift).
// Returns nil if either snapshot is invalid or the interval is zero.
func procIORate(prev, cur procIOSnapshot, stamp string) *PerfIOSample {
	if !prev.ok || !cur.ok {
		return nil
	}
	dt := cur.at.Sub(prev.at).Seconds()
	if dt < 0.001 {
		return nil
	}
	return &PerfIOSample{
		ReadBytesPerSec:           float64(cur.readBytes-prev.readBytes) / dt,
		WriteBytesPerSec:          float64(cur.writeBytes-prev.writeBytes) / dt,
		CancelledWriteBytesPerSec: float64(cur.cancelledWrite-prev.cancelledWrite) / dt,
		SyscallsRead:              float64(cur.syscR-prev.syscR) / dt,
		SyscallsWrite:             float64(cur.syscW-prev.syscW) / dt,
		SampledAt:                 stamp,
	}
}

// StartStatsFileWriter writes the current stats snapshot to disk every
// `interval` so the server can serve them at /api/perf/write-sources.
// Failures are logged once-per-interval and never fatal.
//
// The stats file path is resolved via statsFilePath() once at writer-loop
// start; the env var (CORESCOPE_INGESTOR_STATS) is only re-read on process
// restart, not per tick.
func StartStatsFileWriter(s *Store, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		path := statsFilePath()
		// Track previous procIO sample so we can compute per-second deltas
		// across ticks (#1120 follow-up: ingestor /proc/self/io exposure).
		prevIO := readProcSelfIOFn()
		// Reuse a single bytes.Buffer + json.Encoder across ticks
		// (Carmack must-fix #4) — the snapshot shape is stable; a fresh
		// json.Marshal allocation per second × forever is pure GC waste.
		// The buffer grows once and stays.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for range t.C {
			// Capture time.Now() ONCE per tick (Carmack must-fix #5).
			// Both snapshot.SampledAt and procIO.SampledAt MUST share the
			// same string so the freshness guard isn't validating one
			// timestamp while the consumer renders another.
			tickAt := time.Now().UTC()
			stamp := tickAt.Format(time.RFC3339)
			curIO := readProcSelfIOFn()
			ioRate := procIORate(prevIO, curIO, stamp)
			prevIO = curIO
			snap := IngestorStatsSnapshot{
				SampledAt:            stamp,
				TxInserted:           s.Stats.TransmissionsInserted.Load(),
				ObsInserted:          s.Stats.ObservationsInserted.Load(),
				DuplicateTx:          s.Stats.DuplicateTransmissions.Load(),
				NodeUpserts:          s.Stats.NodeUpserts.Load(),
				ObserverUpserts:      s.Stats.ObserverUpserts.Load(),
				WriteErrors:          s.Stats.WriteErrors.Load(),
				SignatureDrops:       s.Stats.SignatureDrops.Load(),
				WALCommits:           s.Stats.WALCommits.Load(),
				GroupCommitFlushes:   0, // group commit reverted (refs #1129)
				BackfillUpdates:      s.Stats.SnapshotBackfills(),
				ProcIO:               ioRate,
				WriterPerf:           s.WriterStatsSnapshot(),
				SourceLiveness:       SnapshotLivenessClocks(),
				SourceStatuses:       SnapshotSourceStatuses(tickAt),
				WatchdogLastTickUnix: WatchdogLastTickUnix(),
				WatchdogPanicCount:   WatchdogPanicCount(),
				WatchdogLogDropCount: WatchdogLogDropCount(),
			}
			buf.Reset()
			if err := enc.Encode(&snap); err != nil {
				log.Printf("[stats-file] encode: %v", err)
				continue
			}
			// json.Encoder.Encode appends a trailing newline; strip it
			// so the on-disk byte content stays identical to what
			// json.Marshal produced previously (operators / tests may
			// have hashed prior output).
			b := buf.Bytes()
			if n := len(b); n > 0 && b[n-1] == '\n' {
				b = b[:n-1]
			}
			if err := writeStatsAtomic(path, b); err != nil {
				log.Printf("[stats-file] write %s: %v", path, err)
			}
		}
	}()
}
