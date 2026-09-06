// Package main: issue #1659 — analytics warmup gating.
//
// Problem: after server restart, recompRF (and recompTopology /
// recompChannels) cache the FIRST computation, which immediately after
// boot is just the small in-RAM-observations slice (background
// chunk-loader has not yet backfilled history). The recomputer then
// serves that small slice from GetAnalyticsRFWithWindow's default
// shortcut for an entire recompute interval, while the client pins it
// via CLIENT_TTL.analyticsRF. UX: cards show a tiny "post-restart"
// window even when the user selects "All data".
//
// Fix (r1 — addresses #1688 review munger #5):
//
// The first-pass-done signal is NOT enough on its own — the FIRST
// recomputer pass at boot can complete against the post-restart slice
// BEFORE the chunked loader (#1008 / chunked_load.go) has populated
// the full observation set. Marking the gate ready in that window
// reproduces the original #1659 bug.
//
// Two correctness invariants:
//
//  1. (#1688 munger #5) Only mark first-pass-done when BOTH:
//     a. a recomputer pass has completed, AND
//     b. the chunked loader has finished (s.LoadComplete()).
//     The gate's `readyGate` callback is wired by
//     StartAnalyticsRecomputers to `store.LoadComplete`. Passes that
//     complete while loadComplete is still false leave the gate in
//     the warming-up state; the NEXT pass after loadComplete flips
//     true is the one that opens the gate.
//
//  2. (#1688 munger #2 + kent-beck #2) The gate MUST lift in bounded
//     time. If compute() panics on every pass, hangs indefinitely,
//     or returns nil forever, an unguarded gate would leave the
//     503 banner permanent. Two safeguards:
//     a. compute() panics are already caught by runOnce()'s
//     defer recover(); we additionally call markFirstPassDone
//     on EVERY pass (even nil-result), so a recomputer that
//     returns nil but doesn't panic still flips the gate.
//     b. A hard fallback timeout (warmupForceTimeout, 60s by
//     default) elapsed since the recomputer was constructed
//     forces IsWarmingUp_1659() to false — degraded mode
//     (serve whatever cache exists, possibly empty) is
//     strictly better than a permanent 503.
//
// Concurrency (#1688 munger #3):
//
// The previous r0 design used a package-level map keyed by recomputer
// pointer, guarded by a global chanLock. Every default-shape analytics
// request acquired that lock — a serialization point on a hot path.
//
// r1 inlines the warmup fields directly on `analyticsRecomputer`:
//   - firstPassDoneNs  atomic.Int64
//   - warmupStartedNs  atomic.Int64
//   - readyGate        atomic.Value (holds func() bool, may be nil)
//
// Reads on the hot path are lock-free atomic loads. No package-level
// state, no map lookups, no mutex.
//
// Tests: analytics_warmup_1659_test.go.
package main

import (
	"net/http"
	"time"
)

// warmupForceTimeout is the deadline after which IsWarmingUp_1659()
// flips false regardless of whether a successful first pass has run.
// Operators get degraded analytics (possibly empty until the next
// successful compute) instead of a permanent 503 banner.
//
// Var (not const) so tests can shorten it.
var warmupForceTimeout = 60 * time.Second

// setWarmupReadyGate wires a callback that the recomputer consults
// before honoring a markFirstPassDone_1659() request. When the gate
// returns false, the warmup state is preserved across the pass —
// equivalent to "this pass doesn't count; we need at least one pass
// AFTER the gate flips true".
//
// nil callback means "no extra gating" (legacy behavior).
//
// Called from StartAnalyticsRecomputers; safe to call before Start().
func (r *analyticsRecomputer) setWarmupReadyGate_1659(gate func() bool) {
	if r == nil {
		return
	}
	if gate == nil {
		r.warmupReadyGate.Store((*func() bool)(nil))
		return
	}
	r.warmupReadyGate.Store(&gate)
}

func (r *analyticsRecomputer) loadWarmupReadyGate_1659() func() bool {
	v := r.warmupReadyGate.Load()
	if v == nil {
		return nil
	}
	p, ok := v.(*func() bool)
	if !ok || p == nil {
		return nil
	}
	return *p
}

// markFirstPassDone_1659 is called from analyticsRecomputer.runOnce()
// after every compute attempt (success OR nil result; panics are
// caught upstream and never reach here).
//
// The gate flip is conditional on the readyGate (when set) reporting
// true — this implements the munger #5 fix: first-pass-done must
// require BOTH a recomputer pass complete AND the chunked loader to
// have finished populating the in-RAM observation set.
//
// Idempotent: only the FIRST successful flip wins; subsequent calls
// observe a non-zero firstPassDoneNs and return immediately.
func (r *analyticsRecomputer) markFirstPassDone_1659() {
	if r.firstPassDoneNs.Load() != 0 {
		return
	}
	if gate := r.loadWarmupReadyGate_1659(); gate != nil && !gate() {
		return
	}
	r.firstPassDoneNs.CompareAndSwap(0, time.Now().UnixNano())
}

// FirstPassDoneAt_1659 reports the time the first full compute pass
// completed (subject to the readyGate). Returns zero time if no
// qualifying pass has completed yet.
func (r *analyticsRecomputer) FirstPassDoneAt_1659() time.Time {
	if r == nil {
		return time.Time{}
	}
	ns := r.firstPassDoneNs.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// IsWarmingUp_1659 reports true when the recomputer has not yet
// completed a qualifying first pass AND the fallback timeout has not
// yet elapsed. Handlers for the default-shape request must return
// 503 + Retry-After: 5 while this is true.
//
// Fallback timeout (warmupForceTimeout) prevents a permanent 503 in
// pathological compute paths (perpetual panic, perpetual nil, hang).
//
// Lock-free: pure atomic loads.
func (r *analyticsRecomputer) IsWarmingUp_1659() bool {
	if r == nil {
		// No recomputer registered → treat as ready; the handler
		// falls through to the legacy compute path.
		return false
	}
	if r.firstPassDoneNs.Load() != 0 {
		return false
	}
	startedNs := r.warmupStartedNs.Load()
	if startedNs != 0 {
		if time.Since(time.Unix(0, startedNs)) >= warmupForceTimeout {
			// Forced-ready: gate has been stuck too long. Stop
			// serving 503; let the handler serve whatever is in
			// the cache (possibly empty).
			return false
		}
	}
	return true
}

// noteWarmupStart_1659 records the moment the recomputer was launched
// (called once from Start). Used by IsWarmingUp_1659 to compute the
// fallback-timeout elapsed window.
func (r *analyticsRecomputer) noteWarmupStart_1659() {
	if r == nil {
		return
	}
	r.warmupStartedNs.CompareAndSwap(0, time.Now().UnixNano())
}

// writeAnalyticsWarmup503 emits the standard warmup response. The body
// shape is documented for clients: error string + retry_after_s int.
func writeAnalyticsWarmup503(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"analytics warming up","retry_after_s":5}`))
}

// installWarmupBlocker_1659 is a test-only helper that registers the
// RF / topology / channels recomputers with a compute function that
// blocks on the supplied channel. firstPassDoneNs therefore stays
// zero, simulating the post-restart warmup window for the warmup test.
//
// We bypass StartAnalyticsRecomputers entirely and wire the
// recomputers manually so the background goroutines never fire. The
// test only needs the *analyticsRecomputer pointers to be non-nil and
// in the warmup state.
func (s *PacketStore) installWarmupBlocker_1659(block <-chan struct{}) {
	blockCompute := func() interface{} {
		<-block
		return nil
	}
	s.analyticsRecomputerMu.Lock()
	defer s.analyticsRecomputerMu.Unlock()
	s.recompRF = newAnalyticsRecomputer("rf-test-block", time.Hour, blockCompute)
	s.recompTopology = newAnalyticsRecomputer("topo-test-block", time.Hour, blockCompute)
	s.recompChannels = newAnalyticsRecomputer("chan-test-block", time.Hour, blockCompute)
	// Do NOT call Start() — leaving firstPassDoneNs at zero is exactly
	// the warmup state the test wants to exercise.
}
