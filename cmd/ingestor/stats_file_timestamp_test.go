package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStatsFileWriter_SampledAtMatchesProcIOSampledAt drives the real
// StartStatsFileWriter and asserts the byte-equal invariant established
// by #1167 Carmack must-fix #5: the writer captures time.Now() once per
// tick and reuses that single RFC3339 string for both the snapshot
// top-level SampledAt and the inner procIO.SampledAt. If a future change
// reintroduces two independent time.Now() calls — or, equivalently,
// reverts procIORate to format procIO.SampledAt from its own
// (independently-sampled) `cur.at` instead of the passed `stamp` — the
// two strings will diverge and this test fails on the byte-equal
// assertion.
//
// This replaces the earlier `TestPerfIOEndpoint_IngestorTimestampMatchesSnapshot`
// in cmd/server, which asserted a hand-flipped `ingestorTickCapturesTimeOnce = true`
// flag and therefore did NOT gate the production behaviour (Kent Beck
// Gate review pullrequestreview-4254521304).
//
// Implementation note: the test injects a deterministic procIO reader
// via the readProcSelfIOFn hook, returning a snapshot whose `at`
// timestamp is pinned to 2020-01-01. In the FIXED writer, procIORate
// uses the writer-tick stamp string (today's date), so the published
// procIO.SampledAt equals snap.SampledAt byte-for-byte. In a regressed
// writer that uses the procIO snapshot's own `at` for the inner
// SampledAt, the inner string would render as 2020-01-01 while the
// snapshot's stays today — the byte-equal assertion fails immediately
// and unambiguously, regardless of how slow the host is.
func TestStatsFileWriter_SampledAtMatchesProcIOSampledAt(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Inject a deterministic procIO reader. `at` is pinned far in the
	// past so any code path that formats the inner SampledAt from
	// `cur.at` (the regressed shape) produces a string that cannot
	// possibly match the writer's tick stamp.
	origFn := readProcSelfIOFn
	t.Cleanup(func() { readProcSelfIOFn = origFn })
	pinnedAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	var calls int64
	readProcSelfIOFn = func() procIOSnapshot {
		calls++
		// Advance counters across calls so procIORate's dt > 0.001
		// gate passes and a non-nil PerfIOSample is published. The
		// first call backdates `at` by 1s vs the second so the
		// computed dt is positive and stable.
		return procIOSnapshot{
			at:             pinnedAt.Add(time.Duration(calls) * time.Second),
			readBytes:      1000 * calls,
			writeBytes:     2000 * calls,
			cancelledWrite: 0,
			syscR:          10 * calls,
			syscW:          20 * calls,
			ok:             true,
		}
	}

	t.Cleanup(StartStatsFileWriter(store, 50*time.Millisecond))

	// Wait for the file to land with a populated procIO block.
	deadline := time.Now().Add(3 * time.Second)
	var snap map[string]interface{}
	for time.Now().Before(deadline) {
		time.Sleep(75 * time.Millisecond)
		b, err := os.ReadFile(statsPath)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(b, &snap); err != nil {
			continue
		}
		if _, ok := snap["procIO"].(map[string]interface{}); ok {
			break
		}
	}

	topSampledAt, ok := snap["sampledAt"].(string)
	if !ok || topSampledAt == "" {
		t.Fatalf("expected snapshot.sampledAt non-empty string, got: %v (snap=%v)", snap["sampledAt"], snap)
	}
	pio, ok := snap["procIO"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected procIO block, snap=%v", snap)
	}
	innerSampledAt, ok := pio["sampledAt"].(string)
	if !ok || innerSampledAt == "" {
		t.Fatalf("expected procIO.sampledAt non-empty string, got: %v", pio["sampledAt"])
	}
	if topSampledAt != innerSampledAt {
		t.Errorf("snapshot.sampledAt != procIO.sampledAt (writer reverted to two independent timestamps?)\n  top:   %q\n  inner: %q", topSampledAt, innerSampledAt)
	}
}
