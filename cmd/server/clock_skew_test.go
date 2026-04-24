package main

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// ── classifySkew ───────────────────────────────────────────────────────────────

func TestClassify_Default_AllKnownEpochs(t *testing.T) {
	// Each known default epoch at +0s, +1d uptime → default.
	for _, epoch := range defaultEpochs {
		for _, uptimeSec := range []int64{0, 86400} {
			advTS := epoch + uptimeSec
			sev, _ := classifySkew(advTS, 999999) // skew irrelevant for default
			if sev != SkewDefault {
				t.Errorf("classifySkew(epoch=%d + %ds) = %v, want default", epoch, uptimeSec, sev)
			}
		}
	}
	// Also test at 729d for the most recent epoch (no overlap issue).
	advTS := defaultEpochs[len(defaultEpochs)-1] + 729*86400
	sev, matched := classifySkew(advTS, 999999)
	if sev != SkewDefault {
		t.Errorf("classifySkew(latest epoch + 729d) = %v, want default", sev)
	}
	if matched != defaultEpochs[len(defaultEpochs)-1] {
		t.Errorf("matched = %d, want %d", matched, defaultEpochs[len(defaultEpochs)-1])
	}
}

func TestClassify_Default_BeyondUptimeCap(t *testing.T) {
	// 731 days past the LATEST epoch → NOT default.
	latestEpoch := defaultEpochs[len(defaultEpochs)-1]
	advTS := latestEpoch + 731*86400
	sev, _ := classifySkew(advTS, 5)
	if sev == SkewDefault {
		t.Errorf("classifySkew(latestEpoch + 731d) = default, should fall through")
	}
}

func TestClassify_OK(t *testing.T) {
	// Use a timestamp outside all default-epoch ranges.
	advTS := int64(1900000000) // 2030-03 — well past any default+730d
	tests := []struct {
		skew float64
		want SkewSeverity
	}{
		{0, SkewOK},
		{15, SkewOK},
		{15.0, SkewOK},
	}
	for _, tc := range tests {
		sev, _ := classifySkew(advTS, tc.skew)
		if sev != tc.want {
			t.Errorf("classifySkew(advTS, %v) = %v, want %v", tc.skew, sev, tc.want)
		}
	}
}

func TestClassify_Degrading(t *testing.T) {
	advTS := int64(1900000000)
	tests := []float64{16, 30, 60}
	for _, skew := range tests {
		sev, _ := classifySkew(advTS, skew)
		if sev != SkewDegrading {
			t.Errorf("classifySkew(advTS, %v) = %v, want degrading", skew, sev)
		}
	}
}

func TestClassify_Degraded(t *testing.T) {
	advTS := int64(1900000000)
	tests := []float64{61, 300, 600}
	for _, skew := range tests {
		sev, _ := classifySkew(advTS, skew)
		if sev != SkewDegraded {
			t.Errorf("classifySkew(advTS, %v) = %v, want degraded", skew, sev)
		}
	}
}

func TestClassify_Wrong(t *testing.T) {
	advTS := int64(1900000000)
	tests := []float64{601, 3600, 86400 * 365}
	for _, skew := range tests {
		sev, _ := classifySkew(advTS, skew)
		if sev != SkewWrong {
			t.Errorf("classifySkew(advTS, %v) = %v, want wrong", skew, sev)
		}
	}
}

func TestClassify_FutureWrong(t *testing.T) {
	// Advert_ts in 2030 + 700s offset — not a default epoch.
	advTS := int64(1900000700)
	sev, _ := classifySkew(advTS, 700)
	if sev != SkewWrong {
		t.Errorf("classifySkew(future, 700) = %v, want wrong", sev)
	}
}

// ── isDefaultEpoch ─────────────────────────────────────────────────────────────

func TestIsDefaultEpoch_Boundaries(t *testing.T) {
	// Exactly at epoch → true.
	ok, ep := isDefaultEpoch(1715770351)
	if !ok || ep != 1715770351 {
		t.Errorf("isDefaultEpoch(1715770351) = %v, %d", ok, ep)
	}
	// At epoch + maxPlausibleUptimeSec → true.
	ok, ep = isDefaultEpoch(1715770351 + maxPlausibleUptimeSec)
	if !ok {
		t.Error("expected true at epoch + maxPlausibleUptimeSec")
	}
	// Just past → false.
	ok, _ = isDefaultEpoch(1715770351 + maxPlausibleUptimeSec + 1)
	if ok {
		t.Error("expected false past max uptime")
	}
	// Epoch 0 → true.
	ok, ep = isDefaultEpoch(0)
	if !ok || ep != 0 {
		t.Errorf("isDefaultEpoch(0) = %v, %d", ok, ep)
	}
}

// ── median / mean ──────────────────────────────────────────────────────────────

func TestMedian(t *testing.T) {
	tests := []struct {
		vals     []float64
		expected float64
	}{
		{nil, 0},
		{[]float64{}, 0},
		{[]float64{5}, 5},
		{[]float64{1, 3}, 2},
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
		{[]float64{-10, 0, 10}, 0},
	}
	for _, tc := range tests {
		got := median(tc.vals)
		if got != tc.expected {
			t.Errorf("median(%v) = %v, want %v", tc.vals, got, tc.expected)
		}
	}
}

func TestMean(t *testing.T) {
	tests := []struct {
		vals     []float64
		expected float64
	}{
		{nil, 0},
		{[]float64{10}, 10},
		{[]float64{2, 4, 6}, 4},
	}
	for _, tc := range tests {
		got := mean(tc.vals)
		if got != tc.expected {
			t.Errorf("mean(%v) = %v, want %v", tc.vals, got, tc.expected)
		}
	}
}

// ── parseISO ───────────────────────────────────────────────────────────────────

func TestParseISO(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"", 0},
		{"garbage", 0},
		{"2026-04-15T12:00:00Z", 1776254400},
		{"2026-04-15T12:00:00+00:00", 1776254400},
	}
	for _, tc := range tests {
		got := parseISO(tc.input)
		if got != tc.expected {
			t.Errorf("parseISO(%q) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}

// ── extractTimestamp ────────────────────────────────────────────────────────────

func TestExtractTimestamp(t *testing.T) {
	decoded := map[string]interface{}{
		"payload": map[string]interface{}{
			"timestamp": float64(1776340800),
		},
	}
	got := extractTimestamp(decoded)
	if got != 1776340800 {
		t.Errorf("extractTimestamp (nested) = %v, want 1776340800", got)
	}

	decoded2 := map[string]interface{}{
		"timestamp": float64(1776340900),
	}
	got2 := extractTimestamp(decoded2)
	if got2 != 1776340900 {
		t.Errorf("extractTimestamp (top-level) = %v, want 1776340900", got2)
	}

	decoded3 := map[string]interface{}{"foo": "bar"}
	got3 := extractTimestamp(decoded3)
	if got3 != 0 {
		t.Errorf("extractTimestamp (missing) = %v, want 0", got3)
	}
}

// ── calibrateObservers ─────────────────────────────────────────────────────────

func TestCalibrateObservers_SingleObserver(t *testing.T) {
	samples := []skewSample{
		{advertTS: 1000, observedTS: 1000, observerID: "obs1", hash: "h1"},
		{advertTS: 2000, observedTS: 2000, observerID: "obs1", hash: "h2"},
	}
	offsets, _ := calibrateObservers(samples)
	if len(offsets) != 0 {
		t.Errorf("expected no offsets for single-observer, got %v", offsets)
	}
}

func TestCalibrateObservers_MultiObserver(t *testing.T) {
	samples := []skewSample{
		{advertTS: 100, observedTS: 100, observerID: "obs1", hash: "h1"},
		{advertTS: 100, observedTS: 110, observerID: "obs2", hash: "h1"},
		{advertTS: 100, observedTS: 100, observerID: "obs3", hash: "h1"},
		{advertTS: 200, observedTS: 200, observerID: "obs1", hash: "h2"},
		{advertTS: 200, observedTS: 210, observerID: "obs2", hash: "h2"},
		{advertTS: 200, observedTS: 200, observerID: "obs3", hash: "h2"},
	}
	offsets, _ := calibrateObservers(samples)
	if offsets["obs1"] != 0 {
		t.Errorf("obs1 offset = %v, want 0", offsets["obs1"])
	}
	if offsets["obs2"] != 10 {
		t.Errorf("obs2 offset = %v, want 10", offsets["obs2"])
	}
	if offsets["obs3"] != 0 {
		t.Errorf("obs3 offset = %v, want 0", offsets["obs3"])
	}
}

// ── computeNodeSkew ────────────────────────────────────────────────────────────

func TestComputeNodeSkew_BasicCorrection(t *testing.T) {
	samples := []skewSample{
		{advertTS: 1060, observedTS: 1000, observerID: "obs1", hash: "h1"},
		{advertTS: 1060, observedTS: 1010, observerID: "obs2", hash: "h1"},
	}
	offsets, _ := calibrateObservers(samples)
	nodeSkew := computeNodeSkew(samples, offsets)
	cs, ok := nodeSkew["h1"]
	if !ok {
		t.Fatal("expected skew data for hash h1")
	}
	if cs.MedianSkewSec != 55 {
		t.Errorf("median skew = %v, want 55", cs.MedianSkewSec)
	}
}

func TestComputeNodeSkew_ThreeObservers(t *testing.T) {
	samples := []skewSample{
		{advertTS: 1060, observedTS: 1000, observerID: "obs1", hash: "h1"},
		{advertTS: 1060, observedTS: 1000, observerID: "obs2", hash: "h1"},
		{advertTS: 1060, observedTS: 1030, observerID: "obs3", hash: "h1"},
	}
	offsets, _ := calibrateObservers(samples)
	nodeSkew := computeNodeSkew(samples, offsets)
	cs := nodeSkew["h1"]
	if cs == nil {
		t.Fatal("expected skew data for h1")
	}
	if cs.MedianSkewSec != 60 {
		t.Errorf("median skew = %v, want 60", cs.MedianSkewSec)
	}
}

// ── computeDrift ───────────────────────────────────────────────────────────────

func TestComputeDrift_Stable(t *testing.T) {
	pairs := []tsSkewPair{
		{ts: 0, skew: 60},
		{ts: 7200, skew: 60},
		{ts: 14400, skew: 60},
	}
	drift := computeDrift(pairs)
	if drift != 0 {
		t.Errorf("drift = %v, want 0", drift)
	}
}

func TestComputeDrift_LinearDrift(t *testing.T) {
	pairs := []tsSkewPair{
		{ts: 0, skew: 0},
		{ts: 3600, skew: 1},
		{ts: 7200, skew: 2},
	}
	drift := computeDrift(pairs)
	if math.Abs(drift-24.0) > 0.1 {
		t.Errorf("drift = %v, want ~24", drift)
	}
}

func TestComputeDrift_TooFewSamples(t *testing.T) {
	pairs := []tsSkewPair{{ts: 0, skew: 10}}
	if computeDrift(pairs) != 0 {
		t.Error("expected 0 drift for single sample")
	}
}

func TestComputeDrift_TooShortSpan(t *testing.T) {
	pairs := []tsSkewPair{
		{ts: 0, skew: 0},
		{ts: 1800, skew: 10},
	}
	if computeDrift(pairs) != 0 {
		t.Error("expected 0 drift for short time span")
	}
}

func TestDriftRejectsCorrectionJump(t *testing.T) {
	pairs := []tsSkewPair{}
	for i := 0; i < 12; i++ {
		ts := int64(i) * 300
		skew := float64(i) * (1.0 / 24.0)
		pairs = append(pairs, tsSkewPair{ts: ts, skew: skew})
	}
	pairs = append(pairs, tsSkewPair{ts: 3600 + 12*300, skew: 1000})
	drift := computeDrift(pairs)
	if math.Abs(drift) > 100 {
		t.Errorf("drift = %v, expected small", drift)
	}
}

func TestTheilSenMatchesOLSWhenClean(t *testing.T) {
	pairs := []tsSkewPair{}
	for i := 0; i < 20; i++ {
		pairs = append(pairs, tsSkewPair{
			ts:   int64(i) * 600,
			skew: float64(i) * (600.0 / 3600.0),
		})
	}
	drift := computeDrift(pairs)
	if math.Abs(drift-24.0) > 0.25 {
		t.Errorf("drift = %v, want ~24", drift)
	}
}

// ── jsonNumber ─────────────────────────────────────────────────────────────────

func TestJsonNumber(t *testing.T) {
	m := map[string]interface{}{
		"a": float64(42),
		"b": int64(99),
		"c": "not a number",
		"d": nil,
	}
	if jsonNumber(m, "a") != 42 {
		t.Error("float64 case failed")
	}
	if jsonNumber(m, "b") != 99 {
		t.Error("int64 case failed")
	}
	if jsonNumber(m, "c") != 0 {
		t.Error("string case should return 0")
	}
	if jsonNumber(m, "d") != 0 {
		t.Error("nil case should return 0")
	}
	if jsonNumber(m, "missing") != 0 {
		t.Error("missing key should return 0")
	}
}

// ── Integration: GetNodeClockSkew via PacketStore ──────────────────────────────

// formatInt64 is a test helper to format int64 as string for JSON embedding.
func formatInt64(n int64) string {
	return fmt.Sprintf("%d", n)
}

func TestGetNodeClockSkew_Integration(t *testing.T) {
	ps := NewPacketStore(nil, nil)

	pt := 4 // ADVERT
	// Use a base time outside all default-epoch ranges.
	base := int64(1900000000) // 2030
	tx1 := &StoreTx{
		Hash:        "hash1",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":` + formatInt64(base+10) + `}}`,
		Observations: []*StoreObs{
			{ObserverID: "obs1", Timestamp: time.Unix(base, 0).UTC().Format(time.RFC3339)},
			{ObserverID: "obs2", Timestamp: time.Unix(base, 0).UTC().Format(time.RFC3339)},
		},
	}
	tx2 := &StoreTx{
		Hash:        "hash2",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":` + formatInt64(base+3610) + `}}`,
		Observations: []*StoreObs{
			{ObserverID: "obs1", Timestamp: time.Unix(base+3600, 0).UTC().Format(time.RFC3339)},
			{ObserverID: "obs2", Timestamp: time.Unix(base+3600, 0).UTC().Format(time.RFC3339)},
		},
	}

	ps.mu.Lock()
	ps.byNode["AABB"] = []*StoreTx{tx1, tx2}
	ps.byPayloadType[4] = []*StoreTx{tx1, tx2}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	result := ps.GetNodeClockSkew("AABB")
	if result == nil {
		t.Fatal("expected clock skew result for node AABB")
	}
	if result.Pubkey != "AABB" {
		t.Errorf("pubkey = %q, want AABB", result.Pubkey)
	}
	// Both transmissions show ~10s skew → ok.
	if result.Severity != SkewOK {
		t.Errorf("severity = %v, want ok", result.Severity)
	}
}

func TestGetNodeClockSkew_NoData(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	result := ps.GetNodeClockSkew("nonexistent")
	if result != nil {
		t.Error("expected nil for nonexistent node")
	}
}

func TestGetNodeClockSkew_DefaultEpochNode(t *testing.T) {
	// Node with advert_ts at the current firmware default epoch → default severity.
	ps := NewPacketStore(nil, nil)
	pt := 4

	now := time.Now().Unix()
	advTS := int64(1715770351 + 86400) // default + 1 day uptime
	tx := &StoreTx{
		Hash:        "default-h1",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `}}`,
		Observations: []*StoreObs{
			{ObserverID: "obs1", Timestamp: time.Unix(now, 0).UTC().Format(time.RFC3339)},
		},
	}

	ps.mu.Lock()
	ps.byNode["DEFNODE"] = []*StoreTx{tx}
	ps.byPayloadType[4] = []*StoreTx{tx}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	r := ps.GetNodeClockSkew("DEFNODE")
	if r == nil {
		t.Fatal("expected result")
	}
	if r.Severity != SkewDefault {
		t.Errorf("severity = %v, want default", r.Severity)
	}
	if r.DefaultEpoch == nil || *r.DefaultEpoch != 1715770351 {
		t.Errorf("defaultEpoch = %v, want 1715770351", r.DefaultEpoch)
	}
}

func TestGetNodeClockSkew_WrongNode(t *testing.T) {
	// Node with advert_ts far from any default and large skew → wrong.
	ps := NewPacketStore(nil, nil)
	pt := 4

	now := int64(1900000000) // 2030, outside default ranges
	advTS := now + 86400     // 1 day ahead
	tx := &StoreTx{
		Hash:        "wrong-h1",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `}}`,
		Observations: []*StoreObs{
			{ObserverID: "obs1", Timestamp: time.Unix(now, 0).UTC().Format(time.RFC3339)},
		},
	}

	ps.mu.Lock()
	ps.byNode["WRONGNODE"] = []*StoreTx{tx}
	ps.byPayloadType[4] = []*StoreTx{tx}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	r := ps.GetNodeClockSkew("WRONGNODE")
	if r == nil {
		t.Fatal("expected result")
	}
	if r.Severity != SkewWrong {
		t.Errorf("severity = %v, want wrong", r.Severity)
	}
}

func TestGetNodeClockSkew_TooFewSamplesForDrift(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	pt := 4

	now := int64(1900000000) // 2030, outside default ranges
	var txs []*StoreTx
	for i := 0; i < 2; i++ {
		obsTS := now + int64(i)*7200
		advTS := obsTS + 10
		tx := &StoreTx{
			Hash:        "few-h" + string(rune('0'+i)),
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `}}`,
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		}
		txs = append(txs, tx)
	}

	ps.mu.Lock()
	ps.byNode["FEWSAMP"] = txs
	for _, tx := range txs {
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
	}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	result := ps.GetNodeClockSkew("FEWSAMP")
	if result == nil {
		t.Fatal("expected clock skew result")
	}
	if result.DriftPerDaySec != 0 {
		t.Errorf("drift = %v, want 0 for 2-sample node", result.DriftPerDaySec)
	}
}
