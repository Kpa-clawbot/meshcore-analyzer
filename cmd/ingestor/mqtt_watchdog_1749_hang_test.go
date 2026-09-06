package main

import (
	"bytes"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #1749 (root-cause update) — the panic-recovery + escalation
// fixes from PR #1810 address a PANIC inside emit, but production
// emit is log.Print, whose underlying write() can BLOCK rather than
// panic (Docker JSON-file log driver backpressure, full stderr pipe,
// etc.). A blocking write is not a panic — defer/recover does not
// catch it — and because emit was called SYNCHRONOUSLY inside the
// watchdog tick loop, a single stuck write froze checking every
// source on every subsequent tick. This exactly reproduces the
// original incident (3 sources silent simultaneously, zero WATCHDOG
// log lines for 75+ minutes, container otherwise healthy, only a
// restart recovered) even after #1810 landed.
//
// Fix: newAsyncEmit decouples "decide to log" from "perform the
// write". The watchdog loop only ever does a non-blocking channel
// send; a dedicated background goroutine performs the actual
// (potentially blocking) write. If that goroutine itself wedges, the
// bounded channel fills and further sends are DROPPED (counted via
// WatchdogLogDropCount) instead of blocking — the watchdog tick loop
// can never be blocked by a stuck log sink, no matter how long the
// sink stays stuck.

// TestNewAsyncEmit_NeverBlocksWhenWriterStuck_1749 (RED before the
// fix — newAsyncEmit did not exist and emit was called directly):
// floods emit() far past the queue capacity while the background
// writer goroutine is permanently blocked on its first write. Every
// call MUST return immediately.
func TestNewAsyncEmit_NeverBlocksWhenWriterStuck_1749(t *testing.T) {
	writerEntered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	var enteredOnce sync.Once
	realEmit := func(args ...any) {
		enteredOnce.Do(func() { close(writerEntered) })
		<-release // simulates a permanently blocked write() (#1749)
	}

	emit, stop := newAsyncEmit(realEmit)
	defer stop()

	before := WatchdogLogDropCount()

	// Every emit() call -- including the very first -- MUST return
	// promptly regardless of whether the underlying writer is stuck.
	// The whole burst (first call + flood past queue capacity) runs
	// in one goroutine bounded by a single timeout, so a still-
	// blocking emit() fails cleanly via t.Fatal instead of hanging
	// the entire test binary until `go test -timeout` kills it.
	done := make(chan struct{})
	go func() {
		emit("first message — picked up by the writer goroutine and blocks it")
		for i := 0; i < logQueueCapacity+50; i++ {
			emit("flood while writer is stuck")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emit() blocked despite a permanently stuck writer — the #1749 hang has regressed")
	}

	select {
	case <-writerEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer goroutine never received the first message")
	}

	if after := WatchdogLogDropCount(); after <= before {
		t.Fatalf("expected WatchdogLogDropCount to advance once the async queue saturated; before=%d after=%d", before, after)
	}
}

// TestMQTTStallWatchdog_LoopSurvivesStuckWriter_1749 is the
// end-to-end regression: wires runLivenessWatchdogLoop exactly the
// way runLivenessWatchdog does in production (via newAsyncEmit around
// a realEmit that blocks forever on its very first call) and proves
// the loop keeps ticking and keeps checking all registered sources
// anyway — reproducing the #1749 incident shape (3 sources, one
// shared watchdog goroutine) but asserting the FIXED behavior. On the
// pre-fix code (emit called synchronously) the very first emit() call
// would block forever and every subsequent tick would never be
// consumed.
func TestMQTTStallWatchdog_LoopSurvivesStuckWriter_1749(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	release := make(chan struct{})
	defer close(release)
	realEmit := func(args ...any) { <-release } // permanently stuck write()
	emit, stop := newAsyncEmit(realEmit)
	defer stop()

	threshold := 100 * time.Millisecond
	tags := []string{"src-a-1749hang", "src-b-1749hang", "src-c-1749hang"}
	for _, tag := range tags {
		s := &SourceLivenessState{
			Tag:           tag,
			Broker:        "tcp://" + tag + ":1883",
			IsConnectedFn: func() bool { return true },
		}
		atomic.StoreInt64(&s.LastMessageUnix, time.Now().Add(-time.Hour).Unix())
		atomic.StoreInt64(&s.StartedAt, time.Now().Add(-2*time.Hour).Unix())
		if err := registerLivenessState(s); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	tick, done, exited := setupWatchdogTestLoop(t, threshold, emit)
	defer close(done)

	base := time.Now()
	// Drive several ticks. Every tick MUST be consumed promptly — on
	// the pre-fix code, the very first tick's first emit() call (for
	// whichever source the map iteration visits first) would block
	// forever inside processLivenessTransition, and tick 2 would
	// never be accepted.
	for i := 0; i < 5; i++ {
		sendTickOrFail(t, tick, base.Add(time.Duration(i)*time.Second), time.Second,
			"stuck-writer regression tick")
	}

	select {
	case <-exited:
		t.Fatal("watchdog loop exited unexpectedly while writer was stuck")
	default:
	}
}

// TestRunLivenessWatchdog_ProductionWiringUsesAsyncEmit_1749 smoke-
// tests the actual production entrypoint (not just the loop body) to
// confirm it still starts, ticks, and stops cleanly now that its emit
// is wrapped via newAsyncEmit. Pure wiring regression — no assertions
// on log content, just that runLivenessWatchdog remains well-behaved.
func TestRunLivenessWatchdog_ProductionWiringUsesAsyncEmit_1749(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	stop := runLivenessWatchdog(5*time.Millisecond, time.Minute)
	time.Sleep(50 * time.Millisecond)
	if got := WatchdogLastTickUnix(); got == 0 {
		t.Fatalf("expected runLivenessWatchdog to have ticked at least once")
	}

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("runLivenessWatchdog's stop() did not return — newAsyncEmit wiring must not hang shutdown")
	}
	// stop() signals the loop goroutine to exit but does not wait for
	// it (see runLivenessWatchdog) — give it a moment to actually
	// return before the deferred snapshotAndResetRegistry restores the
	// REAL shared registry. Without this, a still-running loop
	// iteration from THIS test's 5ms ticker can race the very next
	// test's registration and double-process its source (observed:
	// an unrelated throttle test intermittently saw 2 invocations
	// instead of 1 when this smoke test ran immediately before it).
	time.Sleep(50 * time.Millisecond)
}

// TestIngestorStatsSnapshot_WatchdogLogDropCountRoundTrip_1749
// mirrors TestIngestorStatsSnapshot_WatchdogFieldsRoundTrip_1810 for
// the new field: it must serialize through JSON and deserialize via
// the server's envelope shape alongside the existing watchdog fields.
func TestIngestorStatsSnapshot_WatchdogLogDropCountRoundTrip_1749(t *testing.T) {
	snap := IngestorStatsSnapshot{
		SampledAt:            "2026-07-18T12:30:00Z",
		WatchdogLastTickUnix: 1752841800,
		WatchdogPanicCount:   0,
		WatchdogLogDropCount: 413,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(b, []byte(`"watchdogLogDropCount":413`)) {
		t.Fatalf("watchdogLogDropCount missing from JSON: %s", string(b))
	}
	var back IngestorStatsSnapshot
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.WatchdogLogDropCount != 413 {
		t.Fatalf("round-trip mismatch: got %d, want 413", back.WatchdogLogDropCount)
	}
}

// TestRunLivenessWatchdog_StopDoesNotRaceQueueClose_1749 is a targeted
// regression for a review finding on this PR: runLivenessWatchdog's
// stop() used to call close(done) followed immediately by stopEmit()
// (which closes the async queue) WITHOUT waiting for the loop
// goroutine to actually return. done being closed only makes the loop
// exit on its NEXT select check -- it does not abort in-flight
// per-source work already past that check. If the loop was mid
// processLivenessTransition -> emit() (i.e. actively sending on the
// queue) at the exact moment stopEmit() closed it, that send raced a
// closed channel and panicked ("send on closed channel").
//
// To have any chance of hitting that window, a source must be
// actively triggering emit() on essentially every tick (LivenessOK
// never emits) and stop() must be called as close as possible to a
// tick firing. This drives many rapid start/stop cycles against a
// fast ticker with an always-stalled source and asserts no panic
// escapes -- run with -race for the strongest signal, but the panic
// itself does not require -race to reproduce.
func TestRunLivenessWatchdog_StopDoesNotRaceQueueClose_1749(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	s := &SourceLivenessState{
		Tag:           "src-race-1749",
		Broker:        "tcp://src-race-1749:1883",
		IsConnectedFn: func() bool { return true },
	}
	// Stalled from the start and re-armed every iteration below so
	// every single tick has real emit-triggering work to do.
	atomic.StoreInt64(&s.LastMessageUnix, time.Now().Add(-time.Hour).Unix())
	atomic.StoreInt64(&s.StartedAt, time.Now().Add(-2*time.Hour).Unix())
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	panicsBefore := WatchdogPanicCount()

	const iterations = 100
	for i := 0; i < iterations; i++ {
		// Re-arm: force LivenessStalled again (clear the edge-trigger
		// cooldown) so this iteration's ticks have emit() work to do,
		// same as production would after MarkReconnected.
		atomic.StoreInt64(&s.LastAlertUnix, 0)
		atomic.StoreInt64(&s.LastMessageUnix, time.Now().Add(-time.Hour).Unix())

		stop := runLivenessWatchdog(time.Microsecond, time.Nanosecond)
		// stop() is called essentially back-to-back with start,
		// maximizing the chance the loop goroutine is caught mid
		// per-source work (including an in-flight emit()) when
		// shutdown begins -- exactly the window the fix must close.
		// The 1ms pacing sleep is NOT part of the race window (it
		// happens after stop() has already returned); it exists only
		// to spread this test's goroutine churn out over ~200ms of
		// wall time instead of a sub-millisecond burst, so it does
		// not perturb the real-time throttle assertions in
		// neighboring tests via OS scheduler pressure.
		stop()
		time.Sleep(3 * time.Millisecond)
	}

	// The per-source defer/recover from #1810 means a "send on closed
	// channel" panic here does NOT crash the test binary -- it gets
	// silently caught, logged, and counted. A bare "did not crash"
	// assertion would therefore pass on the OLD, racy stop()
	// implementation too (verified: the race reproduces reliably
	// under this exact test with 2+ recovered panics per run on the
	// pre-fix code). The real assertion is that WatchdogPanicCount
	// must NOT have moved: ordinary, correctly-sequenced shutdown
	// should never panic in the first place, recovered or not.
	if got := WatchdogPanicCount(); got != panicsBefore {
		t.Fatalf("WatchdogPanicCount advanced from %d to %d across %d start/stop cycles -- "+
			"stop() is racing the async emit queue close against an in-flight emit() "+
			"(send on closed channel, recovered by the #1810 per-source defer/recover "+
			"but should never happen on a correctly sequenced shutdown)",
			panicsBefore, got, iterations)
	}
}
