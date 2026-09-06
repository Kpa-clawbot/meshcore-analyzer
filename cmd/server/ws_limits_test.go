package main

// Tests for the #1794 /ws transport limits. The five cases the issue lists as
// TDD requirements are marked with the exact wording from the issue body, so a
// reviewer can match them off without reading the implementation.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func req(remoteAddr, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.RemoteAddr = remoteAddr
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func limiter(maxConns, perMin int, trusted, deny []string) *wsLimiter {
	l := newWSLimiter()
	l.maxConnsPerIP = maxConns
	l.upgradesPerMin = perMin
	l.trustedProxies = parseCIDRList(trusted, "trusted")
	l.denyNets = parseCIDRList(deny, "deny")
	return l
}

// "Deny CIDR match → 403, hub count unchanged."
func TestWSDeny_CIDRMatchRejects(t *testing.T) {
	l := limiter(0, 0, nil, []string{"203.0.113.0/24"})
	ok, reason, _, _ := l.allow(req("203.0.113.7:1234", ""))
	if ok || reason != wsRejectDeny {
		t.Fatalf("denied CIDR: ok=%v reason=%q, want rejected with %q", ok, reason, wsRejectDeny)
	}
	if ok, _, _, _ := l.allow(req("203.0.114.7:1234", "")); !ok {
		t.Error("an address outside the denied range must still connect")
	}
	if d, _, _ := l.counts(); d != 1 {
		t.Errorf("rejectedDeny = %d, want 1", d)
	}
}

func TestWSDeny_BareAddressIsAcceptedAsSingleHost(t *testing.T) {
	// Operators write "1.2.3.4", not "1.2.3.4/32". Silently ignoring the bare
	// form would be the worst possible failure for a deny list: it looks
	// configured and blocks nothing.
	l := limiter(0, 0, nil, []string{"198.51.100.9"})
	if ok, _, _, _ := l.allow(req("198.51.100.9:5000", "")); ok {
		t.Error("bare address in the deny list must block")
	}
	if ok, _, _, _ := l.allow(req("198.51.100.10:5000", "")); !ok {
		t.Error("the neighbouring address must not be blocked")
	}
}

func TestWSDeny_UnparseableEntryIsSkippedNotFatal(t *testing.T) {
	// One typo must not take the deny list, or the server, down.
	nets := parseCIDRList([]string{"not-an-ip", "203.0.113.0/24"}, "deny")
	if len(nets) != 1 {
		t.Fatalf("parsed %d networks, want 1 (the valid entry)", len(nets))
	}
}

// "6th conn from same IP with cap=5 → 403."
func TestWSConnCap_SixthConnectionRejected(t *testing.T) {
	l := limiter(5, 0, nil, nil)
	for i := 1; i <= 5; i++ {
		if ok, _, _, _ := l.allow(req("203.0.113.5:1", "")); !ok {
			t.Fatalf("connection %d of 5 was rejected", i)
		}
	}
	ok, reason, _, _ := l.allow(req("203.0.113.5:1", ""))
	if ok || reason != wsRejectConnCap {
		t.Fatalf("6th connection: ok=%v reason=%q, want rejected with %q", ok, reason, wsRejectConnCap)
	}
	if _, _, c := l.counts(); c != 1 {
		t.Errorf("rejectedConnCap = %d, want 1", c)
	}
}

func TestWSConnCap_ReleaseFreesTheSlot(t *testing.T) {
	l := limiter(1, 0, nil, nil)
	_, _, _, release := l.allow(req("203.0.113.5:1", ""))
	if ok, _, _, _ := l.allow(req("203.0.113.5:1", "")); ok {
		t.Fatal("cap=1 must refuse the second concurrent connection")
	}
	release()
	if ok, _, _, _ := l.allow(req("203.0.113.5:1", "")); !ok {
		t.Error("after release the slot must be reusable")
	}
}

func TestWSConnCap_ReleaseIsIdempotent(t *testing.T) {
	// Unregister can run twice for one client; double-crediting would let an
	// IP accumulate slots it never gave back.
	l := limiter(1, 0, nil, nil)
	_, _, _, release := l.allow(req("203.0.113.5:1", ""))
	release()
	release()
	if ok, _, _, _ := l.allow(req("203.0.113.5:1", "")); !ok {
		t.Fatal("first reconnect must be allowed")
	}
	if ok, _, _, _ := l.allow(req("203.0.113.5:1", "")); ok {
		t.Error("cap=1 must still hold: the double release must not have credited two slots")
	}
}

// "31 upgrades/min from same IP → 31st rejected."
func TestWSRate_ThirtyFirstUpgradeRejected(t *testing.T) {
	l := limiter(0, 30, nil, nil)
	for i := 1; i <= 30; i++ {
		if ok, _, _, _ := l.allow(req("203.0.113.8:1", "")); !ok {
			t.Fatalf("upgrade %d of 30 was rejected", i)
		}
	}
	ok, reason, _, _ := l.allow(req("203.0.113.8:1", ""))
	if ok || reason != wsRejectRate {
		t.Fatalf("31st upgrade: ok=%v reason=%q, want rejected with %q", ok, reason, wsRejectRate)
	}
	if _, r, _ := l.counts(); r != 1 {
		t.Errorf("rejectedRate = %d, want 1", r)
	}
}

func TestWSRate_BudgetIsPerAddress(t *testing.T) {
	l := limiter(0, 2, nil, nil)
	l.allow(req("203.0.113.1:1", ""))
	l.allow(req("203.0.113.1:1", ""))
	if ok, _, _, _ := l.allow(req("203.0.113.1:1", "")); ok {
		t.Fatal("the first address should be out of budget")
	}
	if ok, _, _, _ := l.allow(req("203.0.113.2:1", "")); !ok {
		t.Error("a different address must have its own budget")
	}
}

func TestWSRate_OldAttemptsLeaveTheWindow(t *testing.T) {
	l := limiter(0, 2, nil, nil)
	key := "203.0.113.3"
	l.state[key] = &wsIPState{
		upgrades:   []time.Time{time.Now().Add(-2 * wsUpgradeWindow), time.Now().Add(-90 * time.Second)},
		lastSeenAt: time.Now(),
	}
	if ok, _, _, _ := l.allow(req(key+":1", "")); !ok {
		t.Error("attempts older than the window must not count against the budget")
	}
}

func TestWSRate_RejectedUpgradeDoesNotConsumeBudget(t *testing.T) {
	// A refusal must not push the client further into debt, or a client that
	// keeps retrying could never recover even once its window cleared.
	l := limiter(1, 5, nil, nil)
	l.allow(req("203.0.113.4:1", "")) // takes the only conn slot, 1 upgrade used
	for i := 0; i < 3; i++ {
		l.allow(req("203.0.113.4:1", "")) // all rejected on the conn cap
	}
	l.mu.Lock()
	used := len(l.state["203.0.113.4"].upgrades)
	l.mu.Unlock()
	if used != 1 {
		t.Errorf("upgrades charged = %d, want 1 (rejections must not be charged)", used)
	}
}

// "Trusted proxy + XFF spoofing → rate counted against the XFF IP, not the proxy."
func TestWSClientIP_TrustedProxyUsesForwardedFor(t *testing.T) {
	l := limiter(0, 1, []string{"10.0.0.0/8"}, nil)
	if ok, _, key, _ := l.allow(req("10.0.0.2:1", "203.0.113.20, 10.0.0.2")); !ok || key != "203.0.113.20" {
		t.Fatalf("key = %q (ok=%v), want the first XFF hop 203.0.113.20", key, ok)
	}
	// Budget of 1 is now spent for that client, but a different client behind
	// the SAME proxy must be unaffected. This is the whole point of honouring
	// XFF: one visitor must not exhaust the budget for everyone else.
	if ok, _, _, _ := l.allow(req("10.0.0.2:1", "203.0.113.21, 10.0.0.2")); !ok {
		t.Error("a second client behind the same trusted proxy must have its own budget")
	}
	if ok, _, _, _ := l.allow(req("10.0.0.2:1", "203.0.113.20, 10.0.0.2")); ok {
		t.Error("the first client's budget should still be spent")
	}
}

// "Untrusted RemoteAddr + XFF spoofing → XFF ignored, rate counted against RemoteAddr."
func TestWSClientIP_UntrustedPeerIgnoresForwardedFor(t *testing.T) {
	l := limiter(0, 1, []string{"10.0.0.0/8"}, nil)
	// A public peer that is not a configured proxy. If XFF were believed here,
	// an attacker would mint a fresh identity per request and the limit would
	// be worthless.
	if ok, _, key, _ := l.allow(req("203.0.113.30:1", "198.51.100.1")); !ok || key != "203.0.113.30" {
		t.Fatalf("key = %q (ok=%v), want the peer address, not the forged header", key, ok)
	}
	if ok, _, _, _ := l.allow(req("203.0.113.30:1", "198.51.100.2")); ok {
		t.Error("changing the forged header must not buy a fresh budget")
	}
}

func TestWSClientIP_TrustedProxyWithoutForwardedForUsesPeer(t *testing.T) {
	l := limiter(0, 5, []string{"10.0.0.0/8"}, nil)
	if _, _, key, _ := l.allow(req("10.0.0.2:1", "")); key != "10.0.0.2" {
		t.Errorf("key = %q, want the proxy's own address when it forwards nothing", key)
	}
}

// The safety property this design exists for: behind an unconfigured reverse
// proxy every visitor shares one address, so enforcing a per-IP cap would
// refuse real users. Limits must be skipped, not applied.
func TestWSLimits_SkippedWhenClientsCannotBeToldApart(t *testing.T) {
	l := limiter(1, 1, nil, nil) // caps of 1, and no trustedProxies
	for i := 0; i < 10; i++ {
		if ok, reason, _, _ := l.allow(req("127.0.0.1:1", "203.0.113.9")); !ok {
			t.Fatalf("request %d refused (%s): loopback peer with no trustedProxies must not be limited", i, reason)
		}
	}
	if _, r, c := l.counts(); r != 0 || c != 0 {
		t.Errorf("rejections rate=%d conncap=%d, want 0 and 0", r, c)
	}
}

func TestWSLimits_PrivatePeerAlsoCountsAsIndistinct(t *testing.T) {
	l := limiter(1, 1, nil, nil)
	for i := 0; i < 5; i++ {
		if ok, _, _, _ := l.allow(req("192.168.1.10:1", "")); !ok {
			t.Fatalf("request %d refused: a private peer with no trustedProxies must not be limited", i)
		}
	}
}

// The deny list is an explicit operator instruction, so unlike the caps it
// still applies when clients cannot be told apart.
func TestWSDeny_AppliesEvenWhenIndistinct(t *testing.T) {
	l := limiter(0, 0, nil, []string{"127.0.0.1"})
	if ok, reason, _, _ := l.allow(req("127.0.0.1:1", "")); ok || reason != wsRejectDeny {
		t.Errorf("ok=%v reason=%q, want the deny list to apply regardless", ok, reason)
	}
}

func TestWSLimits_NilLimiterAllowsEverything(t *testing.T) {
	// A Hub built without ConfigureLimits must behave exactly as before.
	var l *wsLimiter
	ok, _, _, release := l.allow(req("203.0.113.99:1", ""))
	if !ok {
		t.Fatal("a nil limiter must allow the upgrade")
	}
	release() // must not panic
	if d, r, c := l.counts(); d != 0 || r != 0 || c != 0 {
		t.Errorf("nil limiter counts = %d/%d/%d, want zeroes", d, r, c)
	}
}

func TestWSLimits_IdleStateIsCollected(t *testing.T) {
	// Without collection, scanning the server from many addresses would grow
	// the map forever.
	l := limiter(0, 5, nil, nil)
	l.state["203.0.113.50"] = &wsIPState{conns: 0, lastSeenAt: time.Now().Add(-2 * wsBucketIdleGC)}
	l.state["203.0.113.51"] = &wsIPState{conns: 1, lastSeenAt: time.Now().Add(-2 * wsBucketIdleGC)}
	l.mu.Lock()
	l.gcLocked(time.Now())
	_, idleKept := l.state["203.0.113.50"]
	_, liveKept := l.state["203.0.113.51"]
	l.mu.Unlock()
	if idleKept {
		t.Error("an idle record with no connections should be collected")
	}
	if !liveKept {
		t.Error("a record with a live connection must never be collected")
	}
}

func TestWSConfig_Defaults(t *testing.T) {
	// The connection cap is deliberately OFF by default: carrier-grade NAT
	// puts many unrelated subscribers behind one address, so a low cap would
	// refuse real visitors on mobile networks.
	var c Config
	if got := c.WSMaxConnsPerIP(); got != 0 {
		t.Errorf("default maxConnsPerIP = %d, want 0 (off)", got)
	}
	if got := c.WSUpgradesPerMinPerIP(); got != 30 {
		t.Errorf("default upgradesPerMinPerIP = %d, want 30", got)
	}
	zero := 0
	c.WebSocket = &WebSocketConfig{UpgradesPerMinPerIP: &zero}
	if got := c.WSUpgradesPerMinPerIP(); got != 0 {
		t.Errorf("explicit 0 = %d, want 0: an operator must be able to turn the rate limit off", got)
	}
	five := 5
	c.WebSocket = &WebSocketConfig{UpgradesPerMinPerIP: &five, MaxConnsPerIP: 3}
	if got := c.WSUpgradesPerMinPerIP(); got != 5 {
		t.Errorf("configured rate = %d, want 5", got)
	}
	if got := c.WSMaxConnsPerIP(); got != 3 {
		t.Errorf("configured cap = %d, want 3", got)
	}
}
