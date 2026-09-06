package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// PR #1216 round-1 review fixes. Tests are RED before the fix lands:
//   - Item 1: cold-start blind spot — silent-from-start source never alarmed.
//   - Item 2: reconnect reset — stale LastMessageUnix triggers false stall after recovery.
//   - Item 3: log flood — every-60s rescan re-emits same WARN forever.
//   - Item 4: tag collision in registerLivenessState silently overwrites prior state.

// waitFor polls until emits reaches `want` items or the deadline elapses.
// Used to serialize "drain this tick before mutating state" in goroutine
// tests so we observe deterministic edge transitions.
func waitFor(t *testing.T, mu *sync.Mutex, emits *[]string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*emits)
		mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("timeout waiting for %d emits; got %d: %v", want, len(*emits), *emits)
}

// Item 1 (RED): a source that connects but never receives a message is
// invisible to the current watchdog (LastMessageUnix==0 → skip). This is
// the exact #1212 failure class — wrong channel hash, ACL drops SUBSCRIBE,
// half-open TCP after CONNECT. Fix: stamp StartedAt at registration; when
// LastMessageUnix==0 AND now-StartedAt > threshold, alarm with a distinct
// "NEVER received" message.
func TestMQTTStallWatchdog_FiresOnSilentFromStart(t *testing.T) {
	now := time.Now()
	state := &SourceLivenessState{
		Tag:           "cold",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
	}
	atomic.StoreInt64(&state.StartedAt, now.Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&state.FirstConnectedAt, now.Add(-10*time.Minute).Unix())
	// LastMessageUnix stays 0 — never received anything.

	msg, kind := checkSourceLiveness(state, 5*time.Minute, now)
	if kind != LivenessNeverReceived {
		t.Fatalf("expected LivenessNeverReceived for silent-from-start source after threshold; got kind=%v msg=%q", kind, msg)
	}
	if !strings.Contains(strings.ToUpper(msg), "NEVER") {
		t.Errorf("cold-start alarm must mention NEVER received to distinguish from generic stall; got %q", msg)
	}
	if !strings.Contains(msg, "cold") {
		t.Errorf("alarm must include source tag; got %q", msg)
	}
}

func TestMQTTStallWatchdog_QuietDuringColdStartGrace(t *testing.T) {
	now := time.Now()
	state := &SourceLivenessState{
		Tag:           "warming-up",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
	}
	atomic.StoreInt64(&state.StartedAt, now.Add(-30*time.Second).Unix())
	atomic.StoreInt64(&state.FirstConnectedAt, now.Add(-30*time.Second).Unix())

	_, kind := checkSourceLiveness(state, 5*time.Minute, now)
	if kind != LivenessOK {
		t.Fatalf("must NOT alarm during cold-start grace (30s in, threshold 5m); got kind=%v", kind)
	}
}

// Item 2 (RED): after a long outage + paho reconnect, LastMessageUnix is
// still 2h-old → watchdog screams "stalled for 2h" immediately. Fix: reset
// LastMessageUnix (and the cold-start clock) on OnConnect. This test
// asserts the reset method does what's required so the next watchdog scan
// stays quiet for the grace window.
func TestMQTTStallWatchdog_OnReconnectResetsClocks(t *testing.T) {
	now := time.Now()
	state := &SourceLivenessState{
		Tag:           "flaky",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
	}
	// 2-hour-old timestamp from before the outage.
	atomic.StoreInt64(&state.LastMessageUnix, now.Add(-2*time.Hour).Unix())
	atomic.StoreInt64(&state.StartedAt, now.Add(-3*time.Hour).Unix())
	// Stale alert cooldown from before the outage too — must NOT carry forward.
	atomic.StoreInt64(&state.LastAlertUnix, now.Add(-90*time.Minute).Unix())

	state.MarkReconnected(now)

	if last := atomic.LoadInt64(&state.LastMessageUnix); last != 0 {
		t.Errorf("LastMessageUnix must be cleared on reconnect so a stale pre-outage timestamp does not trip the watchdog; got %d", last)
	}
	if started := atomic.LoadInt64(&state.StartedAt); started != now.Unix() {
		t.Errorf("StartedAt must be re-stamped on reconnect so the cold-start grace window restarts; got %d want %d", started, now.Unix())
	}
	if alert := atomic.LoadInt64(&state.LastAlertUnix); alert != 0 {
		t.Errorf("LastAlertUnix must be cleared on reconnect so edge-trigger re-arms; got %d", alert)
	}

	// Now drive checkSourceLiveness immediately after reconnect: must NOT alarm.
	_, kind := checkSourceLiveness(state, 5*time.Minute, now.Add(1*time.Second))
	if kind != LivenessOK {
		t.Fatalf("watchdog must stay quiet immediately after MarkReconnected; got kind=%v", kind)
	}
}

// Item 3 (RED): the watchdog loop currently re-emits the same WARN on every
// 60s tick (60 alerts/hr/source). Fix: edge-trigger — emit WARN once on
// quiet→stalled transition, INFO once on stalled→flowing recovery, and an
// hourly heartbeat while still stalled. Asserts: 3 consecutive ticks on a
// stalled source produce exactly ONE WARN.
func TestMQTTStallWatchdog_EdgeTriggeredEmitsOnlyOnce(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	now := time.Now()
	s := &SourceLivenessState{
		Tag:           "stuck",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
	}
	atomic.StoreInt64(&s.LastMessageUnix, now.Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.StartedAt, now.Add(-20*time.Minute).Unix())
	registerLivenessState(s)

	var mu sync.Mutex
	var emits []string
	emit := func(args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) > 0 {
			if str, ok := args[0].(string); ok {
				emits = append(emits, str)
			}
		}
	}

	tick := make(chan time.Time, 3)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		runLivenessWatchdogLoop(tick, done, 5*time.Minute, emit)
		close(exited)
	}()

	// Three back-to-back ticks within the heartbeat window. Only the first
	// should emit a WARN; the other two must be suppressed (edge-triggered).
	tick <- now
	tick <- now.Add(30 * time.Second)
	tick <- now.Add(60 * time.Second)

	// Wait for ticks to drain.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(emits)
		mu.Unlock()
		if n >= 1 && time.Since(deadline.Add(-2*time.Second)) > 200*time.Millisecond {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(done)
	<-exited

	mu.Lock()
	got := append([]string(nil), emits...)
	mu.Unlock()

	warns := 0
	for _, e := range got {
		if strings.Contains(e, "WATCHDOG") || strings.Contains(e, "stalled") || strings.Contains(strings.ToUpper(e), "WARN") {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("expected exactly 1 stall WARN across 3 consecutive scans (edge-trigger); got %d: %v", warns, got)
	}
}

// Item 3 (RED): on stalled→flowing transition, a recovery INFO must fire
// exactly once. Future ticks must stay silent until a new stall edge.
func TestMQTTStallWatchdog_RecoveryEmitOnce(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	now := time.Now()
	s := &SourceLivenessState{
		Tag:           "src-b",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
	}
	atomic.StoreInt64(&s.LastMessageUnix, now.Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.StartedAt, now.Add(-20*time.Minute).Unix())
	registerLivenessState(s)

	var mu sync.Mutex
	var emits []string
	emit := func(args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) > 0 {
			if str, ok := args[0].(string); ok {
				emits = append(emits, str)
			}
		}
	}

	tick := make(chan time.Time, 4)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		runLivenessWatchdogLoop(tick, done, 5*time.Minute, emit)
		close(exited)
	}()

	tick <- now // → WARN
	// Wait for the goroutine to drain that tick and record the WARN edge
	// before we mutate state — otherwise we race the loop and the first
	// emit observes the "recovered" timestamp instead of the stall.
	waitFor(t, &mu, &emits, 1, 2*time.Second)
	// Source recovers: a recent message arrives.
	atomic.StoreInt64(&s.LastMessageUnix, now.Add(30*time.Second).Unix())
	tick <- now.Add(60 * time.Second) // → recovery INFO
	waitFor(t, &mu, &emits, 2, 2*time.Second)
	tick <- now.Add(120 * time.Second) // → silent
	tick <- now.Add(180 * time.Second) // → silent

	// Brief settle so any (incorrect) extra emits land before we count.
	time.Sleep(100 * time.Millisecond)
	close(done)
	<-exited

	mu.Lock()
	got := append([]string(nil), emits...)
	mu.Unlock()

	infos := 0
	for _, e := range got {
		upper := strings.ToUpper(e)
		if strings.Contains(upper, "RECOVER") || strings.Contains(upper, "FLOWING") {
			infos++
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 emits (1 WARN + 1 recovery INFO); got %d: %v", len(got), got)
	}
	if infos != 1 {
		t.Fatalf("expected exactly 1 recovery INFO emit; got %d (all=%v)", infos, got)
	}
}

// Item 4 (RED): registerLivenessState silently overwrites on tag collision
// (empty-Name + same broker, duplicate Name). Must detect & report.
func TestRegisterLivenessState_DetectsTagCollision(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	a := &SourceLivenessState{Tag: "dup", Broker: "tcp://a:1883"}
	b := &SourceLivenessState{Tag: "dup", Broker: "tcp://b:1883"}

	if err := registerLivenessState(a); err != nil {
		t.Fatalf("first registration must succeed; got %v", err)
	}
	if err := registerLivenessState(b); err == nil {
		t.Fatal("second registration with same tag must return a collision error (current behavior silently clobbers)")
	}

	// And the registry must still hold the FIRST registration — clobbering
	// AttemptCount/LastMessageUnix invisibly is the bug.
	livenessRegistryMu.RLock()
	got := livenessRegistry["dup"]
	livenessRegistryMu.RUnlock()
	if got != a {
		t.Errorf("on collision, first registration must remain authoritative (got pointer for broker=%s)", got.Broker)
	}
}
