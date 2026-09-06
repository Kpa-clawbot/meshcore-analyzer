package main

import (
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// heartbeatInterval is how often the watchdog re-emits a still-stalled
// reminder once the initial WARN edge has fired. 1h matches the pager
// budget — frequent enough that an unattended stall is noticed within a
// shift, infrequent enough not to spam ops chat.
const livenessHeartbeatInterval = time.Hour

// forceReconnectThrottle is the minimum interval between forced
// reconnects on the SAME source. See processLivenessTransition.
const forceReconnectThrottle = 60 * time.Second

// disconnectedReconnectMultiplier (#1749) governs how long a source may
// stay in LivenessDisconnected before the watchdog escalates with a
// forced reconnect. paho's SetAutoReconnect(true) normally recovers a
// dropped connection; in production we have observed paho's reconnect
// machinery silently dying for a single source while another source on
// the same binary reconnects fine (prod 2026-06-30: connectCount=1,
// disconnectCount=1, lastError="EOF", zero retries for 18h). When the
// source stays !IsConnected for more than `multiplier × threshold`,
// the watchdog forces a reconnect rather than trusting paho to recover.
const disconnectedReconnectMultiplier = 5

// watchdogLastTickUnix (#1749) is the wall-clock unix-seconds timestamp
// of the most recent runLivenessWatchdogLoop tick. The watchdog
// goroutine has itself gone silent in production (#1749: 3 sources
// stalled simultaneously for 75 min with zero WATCHDOG log lines),
// suggesting goroutine-level failure. Exposing this clock via
// /api/mqtt/status lets external monitoring assert that the watchdog
// is still ticking — a stale value (e.g. > 2× the scan interval)
// indicates the watchdog itself is dead.
//
// Style note (#1810 round-1, adv #5): new package-level counters in
// this file use atomic.Int64 (typed, method-based) while per-source
// state on SourceLivenessState uses plain int64 + atomic.StoreInt64.
// The struct fields stay int64 because they are accessed through
// pointer receivers all over the codebase and atomic.Int64 inside a
// struct breaks the "noCopy" semantics expected of value receivers
// in a few callsites; package-level vars have no such constraint and
// the typed form catches misuse at compile time. Kept-both is
// intentional, not drift.
var watchdogLastTickUnix atomic.Int64

// watchdogPanicCount (#1810 round-1, Taleb #3) counts recovered panics
// inside the per-source watchdog work IIFE. A loop that panic-loops
// every tick currently looks healthy by WatchdogLastTickUnix alone —
// the tick stamp lands BEFORE the per-source work, so a panic on every
// source still advances the clock. This counter, surfaced via
// /api/mqtt/status and the stats snapshot, lets external monitoring
// alarm on a rapidly-growing value (= the loop is alive but the work
// is broken).
var watchdogPanicCount atomic.Int64

// WatchdogLastTickUnix returns the unix-seconds timestamp of the most
// recent watchdog tick. Returns 0 if the watchdog has never ticked.
func WatchdogLastTickUnix() int64 {
	return watchdogLastTickUnix.Load()
}

// WatchdogPanicCount returns the running total of recovered panics
// inside the watchdog per-source work IIFE (#1810). Monotonic across
// the process lifetime; never decreases.
func WatchdogPanicCount() int64 {
	return watchdogPanicCount.Load()
}

// logQueueCapacity (#1749 root-cause fix) bounds the async emit queue
// (see newAsyncEmit). Sized generously above any plausible per-scan
// emit volume (a handful of sources x a couple of log lines each) so
// legitimate bursts never spuriously drop, while still being small
// enough that a permanently stuck writer fills it -- and starts
// dropping -- within seconds rather than leaking memory unbounded.
const logQueueCapacity = 256

// watchdogLogDropCount (#1749 root-cause fix) counts emit() calls
// dropped because the async log queue (see newAsyncEmit) was
// saturated -- i.e. the background writer goroutine has not drained a
// message in a while, almost certainly because its underlying write()
// is itself blocked (Docker JSON-file log driver backpressure, full
// stderr pipe, etc. -- the exact production shape from #1749: 3 MQTT
// sources silent simultaneously, zero WATCHDOG log lines for 75+
// minutes, container otherwise healthy, only a restart recovered).
// Monotonic; 0 means the writer has never fallen behind.
var watchdogLogDropCount atomic.Int64

// WatchdogLogDropCount returns the running total of log lines dropped
// by the async emit queue because the background writer could not
// keep up (#1749 root-cause fix). Surfaced alongside
// WatchdogLastTickUnix / WatchdogPanicCount so external monitoring can
// distinguish "watchdog dead" (stale tick) from "watchdog alive, but
// its log sink is stuck" (ticking normally, drop count climbing).
//
// Observability note: this is a raw monotonic counter, not a rate. A
// dashboard alerting on "value > 0" will fire once and then stay
// permanently red after the first drop, ever. A dashboard alerting on
// "value == some threshold" will never fire again after crossing it.
// The actionable signal is the counter's RATE OF INCREASE (e.g.
// deriv() / increase() over a window in Prometheus terms) -- a
// handful of drops during a brief legitimate burst is not the same
// incident as hundreds of drops per minute because the writer has
// been stuck for an hour. This function intentionally exposes only
// the raw counter; rate-based alerting is a monitoring-stack
// concern, not something this package should encode.
func WatchdogLogDropCount() int64 {
	return watchdogLogDropCount.Load()
}

// newAsyncEmit (#1749 root-cause fix) decouples "the watchdog decided
// to log something" from "a log line was actually written".
//
// Background: PR #1810 added defer/recover around the per-source
// watchdog work so a PANIC inside emit cannot kill the loop. That
// does not cover the actual production failure mode: realEmit is
// log.Print in production, and log.Print's underlying write() can
// BLOCK -- rather than panic -- if the sink is backpressured (Docker's
// JSON-file log driver falling behind under load, a full stderr
// pipe, journald hiccups, etc.). A blocked syscall is not a panic;
// recover() does nothing for it. Because emit was called
// SYNCHRONOUSLY inside the per-source work, a single stuck write()
// froze the entire tick loop forever -- no further source ever got
// checked and no further tick was ever processed again. That is
// exactly the #1749 incident shape: 3 independent MQTT sources going
// silent within ~60s of each other (one shared dependency -- the
// watchdog goroutine itself -- died, not 3 independent paho clients),
// zero WATCHDOG log lines for the rest of the 75-minute window,
// every OTHER goroutine in the process continuing to run fine (no
// crash, no panic -- a hang), and only a full container restart
// (which spawns a fresh watchdog goroutine) recovering it.
//
// The returned emit function performs a non-blocking channel send
// and never waits on the real writer. A single background goroutine
// drains the channel calling realEmit; if THAT goroutine itself gets
// stuck on a slow/blocked write, the bounded channel simply fills up
// and further sends fall through to the `default` branch -- counted
// via watchdogLogDropCount -- instead of blocking. The watchdog tick
// loop can therefore never be wedged by a stuck log sink, no matter
// how long the sink stays stuck; worst case under a persistent
// backpressure event is lost log lines (visible and counted), not a
// silently dead watchdog (invisible and undetectable -- the actual
// #1749 failure).
//
// stop() closes the queue, asking the writer goroutine to drain and
// exit; it does NOT wait for the writer to finish (a stuck realEmit
// mid-drain must not make shutdown hang either). Consequently, if
// realEmit is genuinely stuck on a blocked syscall when stop() is
// called, the writer goroutine leaks for the remaining lifetime of
// the process -- close(queue) cannot interrupt a blocked write(), and
// nothing here (or anywhere in the Go standard library) can force a
// blocked syscall to return early. This is intentional: the whole
// point of this function is that the watchdog must survive a stuck
// writer, and a single leaked goroutine per stop() call is a vastly
// smaller cost than a permanently wedged watchdog.
//
// This DOES mean newAsyncEmit is meant to be called once per process
// lifetime, the way runLivenessWatchdog does, not repeatedly against
// the same realEmit across many stop()/start() cycles within one
// process (e.g. a test suite that starts and stops the watchdog
// hundreds of times) -- each cycle against a stuck realEmit leaks
// another writer goroutine, and nothing here bounds that
// accumulation. Callers that need repeated start/stop within a single
// process (tests) should either use a realEmit that is guaranteed to
// return promptly, or track and bound the number of cycles.
func newAsyncEmit(realEmit func(...any)) (emit func(...any), stop func()) {
	queue := make(chan string, logQueueCapacity)
	go func() {
		for msg := range queue {
			realEmit(msg)
		}
	}()
	emit = func(args ...any) {
		msg := fmt.Sprint(args...)
		select {
		case queue <- msg:
		default:
			watchdogLogDropCount.Add(1)
		}
	}
	stop = func() {
		close(queue)
	}
	return emit, stop
}

// LivenessKind enumerates the watchdog verdicts for a source. Edge-triggered
// transitions use this to decide whether to emit (and what severity).
type LivenessKind int

const (
	LivenessOK LivenessKind = iota
	LivenessStalled
	LivenessNeverReceived
	LivenessRecovered
	LivenessHeartbeat
	// LivenessDisconnected (PR #1216 r2 item 1): paho reports !IsConnected.
	// Distinct from LivenessOK so processLivenessTransition does NOT
	// interpret a TCP drop as recovery and fire a spurious "messages
	// flowing again" INFO when the source actually went from silently
	// broken to overtly broken. paho's own reconnect logging already
	// covers the disconnect — this kind exists solely to keep the
	// transition engine from mis-classifying it.
	LivenessDisconnected
)

// SourceLivenessState tracks per-source last-message timestamp and connection
// state for the stall watchdog (#1212). LastMessageUnix is updated by the
// message handler via atomic store; the watchdog reads it via atomic load.
//
// PR #1216 r1 added:
//   - StartedAt: re-stamped on reconnect to suppress transient-stall WARNs
//     during paho's reconnect window.
//   - LastAlertUnix: edge-trigger cooldown; prevents 60-per-hour re-emits
//     of the same WARN.
//
// PR #1216 r2 added:
//   - FirstConnectedAt: stamped ONCE at registration, never reset. The
//     cold-start "NEVER received" alarm uses this so a broker that flaps
//     in CONNECT → SUBSCRIBE-deny cannot indefinitely re-arm the grace
//     window. r1's StartedAt-as-grace-clock conflated transient-stall
//     suppression with cold-start grace; r2 separates them.
type SourceLivenessState struct {
	Tag             string
	Broker          string
	LastMessageUnix int64 // atomic; unix seconds of last successfully WRITTEN MQTT message (handleMessage post-write)
	// LastReceiptUnix (PR #1609 M1) is stamped at MQTT receipt time —
	// BEFORE the message is handed to the buffer/writer. STUB: unused
	// in production until the green commit wires MarkReceipt at the
	// receipt callsite and surfaces it in stats/healthz.
	LastReceiptUnix int64 // atomic; unix seconds of last RECEIPT (broker liveness)
	// FirstConnectedAt (PR #1216 r2 item 2) is stamped ONCE at
	// registerLivenessState time and never reset. Cold-start grace
	// checks against this so a flapping broker (CONNECT ok, SUBSCRIBE
	// ACL-denied — the #1212 shape) can no longer suppress the
	// "NEVER received" alarm by re-stamping StartedAt on every reconnect.
	FirstConnectedAt int64 // atomic; unix seconds of first registration
	StartedAt        int64 // atomic; unix seconds when the source was registered / last reconnected (transient-stall tracking)
	LastAlertUnix    int64 // atomic; unix seconds of last emit (WARN or heartbeat); 0 means quiet
	IsConnectedFn    func() bool
	// ForceReconnectFn (#1335) is called by the watchdog when a source
	// transitions INTO LivenessStalled. It must force the paho client
	// to drop its current TCP socket and re-establish (typically
	// client.Disconnect(250) followed by client.Connect()). Half-open
	// TCP sockets (Azure NAT idle timeout) report IsConnected==true so
	// paho's own auto-reconnect never fires; this is the recovery path.
	// May be nil (tests, or sources registered before wiring); the
	// watchdog must treat that as a safe no-op. Invocations are
	// throttled at forceReconnectThrottle per source so a
	// stall→reconnect→re-stall loop self-recovers without hammering
	// the broker.
	ForceReconnectFn func()
	// LastForceReconnectUnix is the unix-seconds timestamp of the most
	// recent forced reconnect for this source; the watchdog reads it
	// to enforce forceReconnectThrottle. atomic.
	LastForceReconnectUnix int64
	// DisconnectedSinceUnix (#1749) is the unix-seconds timestamp of
	// the FIRST tick on which the watchdog observed this source in
	// LivenessDisconnected (paho reports !IsConnected). Cleared back
	// to 0 on any tick where the source is NOT disconnected. When the
	// gap (now - DisconnectedSinceUnix) exceeds
	// disconnectedReconnectMultiplier × threshold, the watchdog
	// escalates with a forced reconnect on the assumption that paho's
	// own auto-reconnect machinery has silently died. atomic.
	DisconnectedSinceUnix int64
	// AttemptCount is incremented on every TCP/TLS connection attempt. Used
	// by ConnectionAttemptHandler to log attempt # independent of paho's
	// internal reconnect-loop state. atomic.
	AttemptCount int64
}

// MarkMessage records the time of a received MQTT message. Cheap; safe to
// call from the message-handling hot path.
func (s *SourceLivenessState) MarkMessage(now time.Time) {
	atomic.StoreInt64(&s.LastMessageUnix, now.Unix())
}

// MarkReceipt records the time of an MQTT message receipt — stamped at the
// paho receipt callback BEFORE the message enters the ingest buffer. PR
// #1609 M1: kept separate from LastMessageUnix so the watchdog/healthz can
// distinguish "broker alive, write path stuck" (LastReceiptUnix fresh,
// LastMessageUnix stale) from "everything stalled" (both stale). Cheap;
// safe to call from the message-handling hot path.
func (s *SourceLivenessState) MarkReceipt(now time.Time) {
	atomic.StoreInt64(&s.LastReceiptUnix, now.Unix())
}

// MarkReconnected clears stale liveness state so the watchdog does not
// false-alarm on a pre-outage timestamp after paho re-establishes the
// connection (PR #1216 r1 item 2). Resets LastMessageUnix, re-stamps
// StartedAt (transient-stall window restarts), and clears LastAlertUnix
// (edge-trigger re-arms).
//
// PR #1216 r2 item 2: FirstConnectedAt is INTENTIONALLY not touched here.
// Under broker flap (CONNECT ok, SUBSCRIBE ACL-denied — exact #1212
// class) r1 reset StartedAt on every reconnect, indefinitely re-arming
// the cold-start grace and silencing the headline "NEVER received"
// alarm. Cold-start grace now reads FirstConnectedAt instead, so the
// alarm fires after the FIRST grace window regardless of reconnect
// churn.
func (s *SourceLivenessState) MarkReconnected(now time.Time) {
	atomic.StoreInt64(&s.LastMessageUnix, 0)
	atomic.StoreInt64(&s.StartedAt, now.Unix())
	atomic.StoreInt64(&s.LastAlertUnix, 0)
	// #1810 round-1 (Taleb #5): clear DisconnectedSinceUnix so the
	// next LivenessDisconnected observation is treated as a NEW outage
	// and starts its escalation timer from scratch. Without this, a
	// post-recovery disconnect immediately satisfies (now -
	// DisconnectedSinceUnix > multiplier × threshold) and force-
	// reconnects on the first tick — making the escalation a
	// trigger-on-flap instead of trigger-on-persistent-failure.
	atomic.StoreInt64(&s.DisconnectedSinceUnix, 0)
}

// checkSourceLiveness returns (message, kind) describing the source's
// liveness state. kind==LivenessOK means quiet/healthy; kind==
// LivenessDisconnected means paho is not connected (silent state — no
// emit, no recovery). Any other kind indicates the caller may want to
// emit (subject to edge-trigger).
//
// Cold-start (PR #1216 r1 item 1, r2 item 2): when LastMessageUnix==0,
// the source has never published a single message. If FirstConnectedAt
// was stamped at registration and more than `threshold` has elapsed,
// this is the #1212 failure class — wrong channel hash, ACL drops
// SUBSCRIBE, half-open TCP after CONNECT, or a broker that loops
// CONNECT-then-disconnect. We emit a DISTINCT "NEVER received" alarm
// so operators can grep for it independently of generic stalls. Using
// FirstConnectedAt (not the reconnect-reset StartedAt) ensures broker
// flap cannot silence this alarm.
func checkSourceLiveness(s *SourceLivenessState, threshold time.Duration, now time.Time) (string, LivenessKind) {
	if s == nil || s.IsConnectedFn == nil {
		return "", LivenessOK
	}
	if !s.IsConnectedFn() {
		// paho's reconnect handler covers the disconnected case. Return
		// a DISTINCT kind so the transition engine does not mis-classify
		// disconnect as recovery (PR #1216 r2 item 1).
		return "", LivenessDisconnected
	}
	last := atomic.LoadInt64(&s.LastMessageUnix)
	if last == 0 {
		firstConnected := atomic.LoadInt64(&s.FirstConnectedAt)
		if firstConnected == 0 {
			// Registration didn't stamp FirstConnectedAt — conservative: stay quiet.
			return "", LivenessOK
		}
		sinceFirst := now.Sub(time.Unix(firstConnected, 0))
		if sinceFirst < threshold {
			return "", LivenessOK
		}
		msg := fmt.Sprintf("MQTT [%s] WATCHDOG: client reports connected to %s but has NEVER received a message in %s (threshold %s) — check channel hash / subscribe ACL / half-open TCP",
			s.Tag, s.Broker, sinceFirst.Round(time.Second), threshold)
		return msg, LivenessNeverReceived
	}
	silentFor := now.Sub(time.Unix(last, 0))
	if silentFor < threshold {
		return "", LivenessOK
	}
	msg := fmt.Sprintf("MQTT [%s] WATCHDOG: client reports connected to %s but no messages received for %s (threshold %s) — possible half-open socket or upstream stall",
		s.Tag, s.Broker, silentFor.Round(time.Second), threshold)
	return msg, LivenessStalled
}

// livenessRegistry is a package-level lookup so handleMessage (called with
// only `tag string`) can mark liveness without threading the state through
// every call site. Reads dominate (per message); writes happen once per
// source at startup.
var (
	livenessRegistry   = map[string]*SourceLivenessState{}
	livenessRegistryMu sync.RWMutex
)

// registerLivenessState publishes a state to the registry by tag. Returns
// an error on tag collision (PR #1216 r1 item 4) so operators see a
// startup misconfiguration instead of silently losing AttemptCount and
// LastMessageUnix for the clobbered source. The collision case is real:
// two MQTT sources with empty Name fall back to Broker; two sources with
// duplicate Name; copy-paste in config.json. Caller (main) decides whether
// to fatal or just log and skip. The first registration remains
// authoritative — we do NOT overwrite.
//
// Also stamps StartedAt (transient-stall window) and FirstConnectedAt
// (cold-start grace anchor — never reset; see r2 item 2 in
// MarkReconnected) so the cold-start watchdog has its clocks.
func registerLivenessState(s *SourceLivenessState) error {
	livenessRegistryMu.Lock()
	defer livenessRegistryMu.Unlock()
	if existing, ok := livenessRegistry[s.Tag]; ok {
		return fmt.Errorf("liveness registry: duplicate tag %q (existing broker=%s, new broker=%s) — fix config so each MQTT source has a unique Name", s.Tag, existing.Broker, s.Broker)
	}
	nowUnix := time.Now().Unix()
	if atomic.LoadInt64(&s.StartedAt) == 0 {
		atomic.StoreInt64(&s.StartedAt, nowUnix)
	}
	if atomic.LoadInt64(&s.FirstConnectedAt) == 0 {
		atomic.StoreInt64(&s.FirstConnectedAt, nowUnix)
	}
	livenessRegistry[s.Tag] = s
	return nil
}

// registerLivenessOrSkip (PR #1216 r2 item 3) is the main-callsite wrapper
// that replaces the previous log.Fatalf on tag collision. Fatal at
// startup over a config typo would kill the entire ingestor and recreate
// the #1212 total-ingest-stop class this PR exists to prevent. On
// collision we log ERROR + skip — the MQTT source still attempts to
// connect, it just won't be tracked by the liveness watchdog. Returns
// true iff the source was registered.
func registerLivenessOrSkip(s *SourceLivenessState) bool {
	if err := registerLivenessState(s); err != nil {
		log.Printf("[ingestor] ERROR: source tag collision %q — skipping duplicate liveness registration, this source will connect but will not be tracked by the watchdog (%v)", s.Tag, err)
		return false
	}
	return true
}

// markLivenessForTag is the hot-path entry point: O(1) map lookup +
// atomic store. Safe to call for unknown tags (no-op). Updates
// LastMessageUnix (post-write clock).
func markLivenessForTag(tag string, now time.Time) {
	livenessRegistryMu.RLock()
	s := livenessRegistry[tag]
	livenessRegistryMu.RUnlock()
	if s != nil {
		s.MarkMessage(now)
	}
}

// markReceiptForTag is the hot-path entry point used at MQTT receipt
// (BEFORE the message is buffered/written). Updates LastReceiptUnix only.
// PR #1609 M1 — separates broker-liveness signal from write-path
// liveness so /healthz can show a stalled writer with a live broker.
func markReceiptForTag(tag string, now time.Time) {
	livenessRegistryMu.RLock()
	s := livenessRegistry[tag]
	livenessRegistryMu.RUnlock()
	if s != nil {
		s.MarkReceipt(now)
	}
}

// SnapshotLivenessClocks returns the per-source receipt vs write-path
// liveness pair for every registered source. Read-only; safe to call
// from the stats-file writer. PR #1609 M1.
func SnapshotLivenessClocks() map[string]SourceLivenessSnapshot {
	livenessRegistryMu.RLock()
	defer livenessRegistryMu.RUnlock()
	if len(livenessRegistry) == 0 {
		return nil
	}
	out := make(map[string]SourceLivenessSnapshot, len(livenessRegistry))
	for tag, s := range livenessRegistry {
		out[tag] = SourceLivenessSnapshot{
			LastReceiptUnix: atomic.LoadInt64(&s.LastReceiptUnix),
			LastMessageUnix: atomic.LoadInt64(&s.LastMessageUnix),
		}
	}
	return out
}

// runLivenessWatchdog starts a goroutine that scans the registry every
// `interval` and logs a warning for any source that has been silent while
// connected for more than `threshold`. Returns a stop function that halts
// the ticker AND signals the goroutine to exit (time.Ticker.Stop does NOT
// close the channel, so a naive `for range t.C` would leak). interval
// should be a fraction of threshold (e.g. threshold/5) so detection
// latency is bounded.
func runLivenessWatchdog(interval, threshold time.Duration) (stop func()) {
	t := time.NewTicker(interval)
	done := make(chan struct{})
	loopExited := make(chan struct{})
	// #1749 root-cause fix: wrap log.Print via newAsyncEmit rather than
	// passing it directly. See newAsyncEmit's doc comment -- a
	// synchronous log.Print can block forever on a stuck writer, and
	// that used to wedge this entire goroutine (the production
	// incident this fix addresses).
	emit, stopEmit := newAsyncEmit(log.Print)
	go func() {
		defer close(loopExited)
		runLivenessWatchdogLoop(t.C, done, threshold, emit)
	}()
	return func() {
		t.Stop()
		close(done)
		// Wait for the loop goroutine to actually return before
		// tearing down the async emit queue. Without this, the loop
		// can still be mid processLivenessTransition -> emit() when
		// stopEmit() closes the queue out from under it: emit()'s
		// `queue <- msg` send races a closed channel and panics
		// (send on closed channel). done being closed only makes the
		// loop exit on its NEXT select check -- it does not abort
		// in-flight per-source work already past that check.
		<-loopExited
		stopEmit()
	}
}

// runLivenessWatchdogLoop is the goroutine body, extracted so tests can
// drive it with a synthetic tick channel and capture log output without
// racing on the real ticker.
//
// Edge-triggered (PR #1216 r1 item 3):
//   - quiet → stalled / never-received: emit WARN once, record LastAlertUnix
//   - still stalled, < heartbeat interval since last alert: suppress
//   - still stalled, ≥ heartbeat interval since last alert: emit reminder,
//     refresh LastAlertUnix
//   - stalled → flowing: emit recovery INFO once, clear LastAlertUnix
//
// Without this, the original loop re-emitted the same WARN on every 60s
// tick (60 alerts/hr/source) — the kind of log flood that trains ops to
// mute alerts and miss the next real outage.
func runLivenessWatchdogLoop(tick <-chan time.Time, done <-chan struct{}, threshold time.Duration, emit func(...any)) {
	for {
		select {
		case <-done:
			return
		case now, ok := <-tick:
			if !ok {
				return
			}
			// #1749: stamp the watchdog clock BEFORE per-source work so
			// a panic or hang inside processLivenessTransition does
			// not freeze the heartbeat that /api/mqtt/status exposes.
			// External monitoring on WatchdogLastTickUnix detects a
			// wedged loop only if this clock is fresh-while-ticking
			// and stale-when-dead.
			watchdogLastTickUnix.Store(now.Unix())
			livenessRegistryMu.RLock()
			states := make([]*SourceLivenessState, 0, len(livenessRegistry))
			for _, s := range livenessRegistry {
				states = append(states, s)
			}
			livenessRegistryMu.RUnlock()
			for _, s := range states {
				// #1749: handle disconnect-escalation bookkeeping
				// BEFORE the transition dispatch. checkSourceLiveness
				// returns LivenessDisconnected when paho reports
				// !IsConnected; we track how long that has persisted
				// and escalate with a forced reconnect when paho's
				// own auto-reconnect machinery has clearly failed
				// (multiplier × threshold without recovery).
				msg, kind := checkSourceLiveness(s, threshold, now)
				// #1749: a panic in emit (blocked log pipe, full
				// Docker JSON-file driver, etc.) MUST NOT kill the
				// watchdog goroutine. Recover per-source so one bad
				// source — or one bad log call — does not silence
				// all monitoring across all sources. Both
				// maybeEscalateDisconnected and processLivenessTransition
				// call emit, so both must be inside the recover scope.
				func(state *SourceLivenessState, k LivenessKind, m string) {
					defer func() {
						if r := recover(); r != nil {
							// #1810 round-1 (Taleb #2 + #3):
							// (a) Write to os.Stderr DIRECTLY rather than via
							// log.Printf. The original log sink is the prime
							// suspect for a panic in emit (blocked stderr pipe,
							// full Docker JSON-file driver) — using the same
							// sink to report the recovery risks a second
							// panic-on-recover that kills the goroutine the
							// recover was meant to save. os.Stderr.Write is a
							// raw syscall and bypasses log's own mutex.
							// (b) Increment watchdogPanicCount so a panic-per-
							// tick loop is visible to external monitoring even
							// though WatchdogLastTickUnix continues to advance.
							watchdogPanicCount.Add(1)
							fmt.Fprintf(os.Stderr, "[ingestor] WATCHDOG RECOVERED panic processing source %q: %v\n", state.Tag, r)
						}
					}()
					maybeEscalateDisconnected(state, k, threshold, now, emit)
					processLivenessTransition(state, k, m, now, emit)
				}(s, kind, msg)
			}
		}
	}
}

// maybeEscalateDisconnected (#1749) tracks how long a source has
// continuously been observed in LivenessDisconnected and triggers a
// forced reconnect (subject to forceReconnectThrottle) once the gap
// exceeds disconnectedReconnectMultiplier × threshold.
//
// Background: paho's SetAutoReconnect(true) is supposed to recover
// dropped connections on its own. In production we have observed paho
// silently giving up on one source — connectCount=1, disconnectCount=1,
// lastError="EOF", zero retries for 18h — while another source on the
// same binary reconnects fine. The existing watchdog's
// processLivenessTransition stays silent on LivenessDisconnected to
// avoid double-logging paho's own disconnect line; this escalation
// path is the recovery hook for the case where paho never tries again.
//
// Behavior:
//   - kind != LivenessDisconnected: clear DisconnectedSinceUnix (reset
//     the timer; recovery or other state).
//   - kind == LivenessDisconnected, first observation: stamp
//     DisconnectedSinceUnix = now.
//   - kind == LivenessDisconnected, gap >= multiplier × threshold:
//     emit a WARN and call maybeForceReconnect (throttled). The
//     timestamp is NOT advanced beyond the original observation so the
//     emit fires on every tick past the boundary; the throttle inside
//     maybeForceReconnect handles the broker-hammering concern.
//
// emit is wrapped in defer/recover by the caller — a panic here does
// not kill the loop.
// disconnectedEscalationGap returns the gap (now - DisconnectedSinceUnix)
// at which the watchdog escalates a persistent LivenessDisconnected
// observation into a forced reconnect. Hoisted out of
// maybeEscalateDisconnected (#1810 round-1, adv #6) so it is computed
// once per call site rather than per-source-per-tick. The result
// includes a per-source jitter offset (#1810 round-1, Taleb #4) so
// that a shared-broker outage across N sources does NOT cause a
// synchronized thundering-herd reconnect at the multiplier × threshold
// boundary. Jitter range: 0..forceReconnectThrottle, deterministic per
// tag (hash-based, no RNG state) so retries do not phase-walk.
func disconnectedEscalationGap(tag string, threshold time.Duration) time.Duration {
	base := disconnectedReconnectMultiplier * threshold
	if forceReconnectThrottle <= 0 {
		return base
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(tag))
	jitter := time.Duration(h.Sum32()%uint32(forceReconnectThrottle/time.Millisecond)) * time.Millisecond
	return base + jitter
}

func maybeEscalateDisconnected(s *SourceLivenessState, kind LivenessKind, threshold time.Duration, now time.Time, emit func(...any)) {
	if kind != LivenessDisconnected {
		atomic.StoreInt64(&s.DisconnectedSinceUnix, 0)
		return
	}
	disconnectedSince := atomic.LoadInt64(&s.DisconnectedSinceUnix)
	if disconnectedSince == 0 {
		atomic.StoreInt64(&s.DisconnectedSinceUnix, now.Unix())
		return
	}
	gap := now.Sub(time.Unix(disconnectedSince, 0))
	escalateAt := disconnectedEscalationGap(s.Tag, threshold)
	if gap < escalateAt {
		return
	}
	// #1810 round-1 (adv #2 + Taleb #1): only emit the ESCALATION WARN
	// when we are actually going to issue a force-reconnect. Without
	// this throttle, every tick past the boundary re-emits the WARN
	// — for a 1m scan interval and a 1h outage, that's 55+ duplicate
	// alert lines. maybeForceReconnect already enforces
	// forceReconnectThrottle and writes its own "forcing reconnect"
	// telemetry, so the operator-visible log surface is still
	// complete; we just no longer drown them in pre-amble.
	lastForce := atomic.LoadInt64(&s.LastForceReconnectUnix)
	if lastForce != 0 && now.Sub(time.Unix(lastForce, 0)) < forceReconnectThrottle {
		return
	}
	emit(fmt.Sprintf("MQTT [%s] WATCHDOG ESCALATION: paho disconnected for %s (>%d×threshold=%s) with no auto-reconnect — forcing reconnect (#1749)",
		s.Tag, gap.Round(time.Second), disconnectedReconnectMultiplier, escalateAt))
	maybeForceReconnect(s, now, emit)
}

// processLivenessTransition applies the edge-trigger rules and updates
// LastAlertUnix accordingly. Separated for testability and to keep the
// loop body small.
func processLivenessTransition(s *SourceLivenessState, kind LivenessKind, msg string, now time.Time, emit func(...any)) {
	lastAlert := atomic.LoadInt64(&s.LastAlertUnix)
	switch kind {
	case LivenessStalled, LivenessNeverReceived:
		if lastAlert == 0 {
			// First detection — fire WARN edge.
			emit(msg)
			atomic.StoreInt64(&s.LastAlertUnix, now.Unix())
			// #1335: ONLY LivenessStalled (paho reports connected but no
			// messages past threshold — classic half-open TCP) gets
			// force-reconnected. LivenessNeverReceived is almost always
			// an ACL deny / wrong channel hash — a new TCP socket won't
			// fix it and would just churn the broker. The distinct
			// "NEVER received" alarm is the right operator signal for
			// that class.
			if kind == LivenessStalled {
				maybeForceReconnect(s, now, emit)
			}
			return
		}
		// Already alerted; only re-emit on heartbeat interval to avoid log flood.
		if now.Sub(time.Unix(lastAlert, 0)) >= livenessHeartbeatInterval {
			emit(fmt.Sprintf("MQTT [%s] WATCHDOG heartbeat: still stalled — %s", s.Tag, msg))
			atomic.StoreInt64(&s.LastAlertUnix, now.Unix())
			// Heartbeat re-emit on a still-Stalled source: try another
			// force-reconnect IF the throttle window has elapsed. Under
			// a persistent broker issue this caps at one attempt per
			// heartbeat (1h) — orders of magnitude under any rate
			// limit and well within "don't hammer the broker".
			if kind == LivenessStalled {
				maybeForceReconnect(s, now, emit)
			}
		}
	case LivenessOK:
		if lastAlert != 0 {
			// Recovered: emit INFO once, clear the cooldown.
			emit(fmt.Sprintf("MQTT [%s] WATCHDOG INFO: messages flowing again (recovered)", s.Tag))
			atomic.StoreInt64(&s.LastAlertUnix, 0)
		}
	case LivenessDisconnected:
		// PR #1216 r2 item 1: disconnect is NOT recovery. Stay completely
		// silent — paho's reconnect handler already logs the drop — and
		// preserve LastAlertUnix so the WARN edge can re-fire if/when
		// the source comes back stalled. Clearing the cooldown here
		// would mean a flapping source spams the WARN every cycle.
	}
}

// maybeForceReconnect invokes ForceReconnectFn IFF (a) one is wired and
// (b) the throttle window (forceReconnectThrottle) has elapsed since
// the most recent forced reconnect for this source. Logs WATCHDOG
// telemetry before/after so operators can correlate the reconnect with
// downstream paho ConnectionAttempt/OnConnect lines.
func maybeForceReconnect(s *SourceLivenessState, now time.Time, emit func(...any)) {
	if s.ForceReconnectFn == nil {
		return
	}
	lastForce := atomic.LoadInt64(&s.LastForceReconnectUnix)
	if lastForce != 0 && now.Sub(time.Unix(lastForce, 0)) < forceReconnectThrottle {
		emit(fmt.Sprintf("MQTT [%s] WATCHDOG suppressing forced reconnect (last attempt %s ago, throttle %s)",
			s.Tag, now.Sub(time.Unix(lastForce, 0)).Round(time.Second), forceReconnectThrottle))
		return
	}
	atomic.StoreInt64(&s.LastForceReconnectUnix, now.Unix())
	emit(fmt.Sprintf("MQTT [%s] WATCHDOG forcing reconnect (half-open TCP suspected — paho.IsConnected==true but no messages)", s.Tag))
	// Run in a goroutine: ForceReconnectFn typically calls
	// client.Disconnect(250) which blocks up to 250ms, then
	// client.Connect() which can block on the connect timeout. The
	// watchdog goroutine must not stall a per-tick scan over a single
	// slow source.
	go func() {
		s.ForceReconnectFn()
		emit(fmt.Sprintf("MQTT [%s] WATCHDOG reconnect attempt issued", s.Tag))
	}()
}
