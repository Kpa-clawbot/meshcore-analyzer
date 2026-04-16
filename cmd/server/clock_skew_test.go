package main

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// ── classifySkew ───────────────────────────────────────────────────────────────

func TestClassifySkew(t *testing.T) {
	tests := []struct {
		absSkew  float64
		expected SkewSeverity
	}{
		{0, SkewOK},
		{60, SkewOK},           // 1 min
		{299, SkewOK},          // just under 5 min
		{300, SkewWarning},     // exactly 5 min
		{1800, SkewWarning},    // 30 min
		{3599, SkewWarning},    // just under 1 hour
		{3600, SkewCritical},   // exactly 1 hour
		{86400, SkewCritical},  // 1 day
		{2592000 - 1, SkewCritical}, // just under 30 days
		{2592000, SkewAbsurd},  // exactly 30 days
		{86400 * 365 - 1, SkewAbsurd}, // just under 365 days
		{86400 * 365, SkewNoClock}, // exactly 365 days
		{86400 * 365 * 10, SkewNoClock}, // 10 years (epoch-0 style)
	}
	for _, tc := range tests {
		got := classifySkew(tc.absSkew)
		if got != tc.expected {
			t.Errorf("classifySkew(%v) = %v, want %v", tc.absSkew, got, tc.expected)
		}
	}
}

// ── median ─────────────────────────────────────────────────────────────────────

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
	// Nested payload.timestamp
	decoded := map[string]interface{}{
		"payload": map[string]interface{}{
			"timestamp": float64(1776340800),
		},
	}
	got := extractTimestamp(decoded)
	if got != 1776340800 {
		t.Errorf("extractTimestamp (nested) = %v, want 1776340800", got)
	}

	// Top-level timestamp
	decoded2 := map[string]interface{}{
		"timestamp": float64(1776340900),
	}
	got2 := extractTimestamp(decoded2)
	if got2 != 1776340900 {
		t.Errorf("extractTimestamp (top-level) = %v, want 1776340900", got2)
	}

	// No timestamp
	decoded3 := map[string]interface{}{"foo": "bar"}
	got3 := extractTimestamp(decoded3)
	if got3 != 0 {
		t.Errorf("extractTimestamp (missing) = %v, want 0", got3)
	}
}

// ── calibrateObservers ─────────────────────────────────────────────────────────

func TestCalibrateObservers_SingleObserver(t *testing.T) {
	// Single-observer packets can't calibrate — should return empty.
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
	// Packet h1 seen by 3 observers: obs1 at t=100, obs2 at t=110, obs3 at t=100.
	// Median observation = 100. obs1=0, obs2=+10, obs3=0
	// Packet h2 seen by 3 observers: obs1 at t=200, obs2 at t=210, obs3 at t=200.
	// Median observation = 200. obs1=0, obs2=+10, obs3=0
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
	// Validates observer offset correction direction.
	//
	// Setup: node is 60s ahead, obs1 accurate, obs2 is 10s ahead.
	// With 2 observers, median obs_ts = 1005.
	//   obs1 offset = 1000 - 1005 = -5
	//   obs2 offset = 1010 - 1005 = +5
	// Correction: corrected = raw_skew + obsOffset
	//   obs1: raw=60, corrected = 60 + (-5) = 55
	//   obs2: raw=50, corrected = 50 + 5 = 55
	// Both converge to 55 (not exact 60 because with only 2 observers,
	// the median can't fully distinguish which observer is drifted).

	samples := []skewSample{
		// Same packet seen by accurate obs1 and obs2 (+10s ahead)
		{advertTS: 1060, observedTS: 1000, observerID: "obs1", hash: "h1"},
		{advertTS: 1060, observedTS: 1010, observerID: "obs2", hash: "h1"},
	}
	offsets, _ := calibrateObservers(samples)
	// median obs = 1005, obs1 offset = -5, obs2 offset = +5
	// So the median approach finds obs2 is +5 ahead (relative to median)

	// Now compute node skew with those offsets:
	nodeSkew := computeNodeSkew(samples, offsets)
	cs, ok := nodeSkew["h1"]
	if !ok {
		t.Fatal("expected skew data for hash h1")
	}
	// With only 2 observers, median obs_ts = 1005.
	// obs1 offset = 1000-1005 = -5, obs2 offset = 1010-1005 = +5
	// raw from obs1 = 60, corrected = 60 + (-5) = 55
	// raw from obs2 = 50, corrected = 50 + 5 = 55
	// median = 55
	if cs.MedianSkewSec != 55 {
		t.Errorf("median skew = %v, want 55", cs.MedianSkewSec)
	}
}

func TestComputeNodeSkew_ThreeObservers(t *testing.T) {
	// Node is exactly 60s ahead. obs1 accurate, obs2 accurate, obs3 +30s ahead.
	// advertTS = 1060, real time = 1000
	samples := []skewSample{
		{advertTS: 1060, observedTS: 1000, observerID: "obs1", hash: "h1"},
		{advertTS: 1060, observedTS: 1000, observerID: "obs2", hash: "h1"},
		{advertTS: 1060, observedTS: 1030, observerID: "obs3", hash: "h1"},
	}
	offsets, _ := calibrateObservers(samples)
	// median obs_ts = 1000. obs1=0, obs2=0, obs3=+30
	if offsets["obs3"] != 30 {
		t.Errorf("obs3 offset = %v, want 30", offsets["obs3"])
	}

	nodeSkew := computeNodeSkew(samples, offsets)
	cs := nodeSkew["h1"]
	if cs == nil {
		t.Fatal("expected skew data for h1")
	}
	// raw from obs1 = 60, corrected = 60 + 0 = 60
	// raw from obs2 = 60, corrected = 60 + 0 = 60
	// raw from obs3 = 30, corrected = 30 + 30 = 60
	// All three converge to 60. 
	if cs.MedianSkewSec != 60 {
		t.Errorf("median skew = %v, want 60 (node is 60s ahead)", cs.MedianSkewSec)
	}
}

// ── computeDrift ───────────────────────────────────────────────────────────────

func TestComputeDrift_Stable(t *testing.T) {
	// Constant skew = no drift.
	pairs := []tsSkewPair{
		{ts: 0, skew: 60},
		{ts: 7200, skew: 60},
		{ts: 14400, skew: 60},
	}
	drift := computeDrift(pairs)
	if drift != 0 {
		t.Errorf("drift = %v, want 0 for stable skew", drift)
	}
}

func TestComputeDrift_LinearDrift(t *testing.T) {
	// 1 second drift per hour = 24 sec/day.
	pairs := []tsSkewPair{
		{ts: 0, skew: 0},
		{ts: 3600, skew: 1},
		{ts: 7200, skew: 2},
	}
	drift := computeDrift(pairs)
	expected := 24.0
	if math.Abs(drift-expected) > 0.1 {
		t.Errorf("drift = %v, want ~%v", drift, expected)
	}
}

func TestComputeDrift_TooFewSamples(t *testing.T) {
	pairs := []tsSkewPair{{ts: 0, skew: 10}}
	if computeDrift(pairs) != 0 {
		t.Error("expected 0 drift for single sample")
	}
}

func TestComputeDrift_TooShortSpan(t *testing.T) {
	// Less than 1 hour apart.
	pairs := []tsSkewPair{
		{ts: 0, skew: 0},
		{ts: 1800, skew: 10},
	}
	if computeDrift(pairs) != 0 {
		t.Error("expected 0 drift for short time span")
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

func TestGetNodeClockSkew_Integration(t *testing.T) {
	ps := NewPacketStore(nil, nil)

	// Simulate two ADVERT transmissions for the same node, seen by 2 observers each.
	// Node "AABB" has clock 120s ahead.
	pt := 4 // ADVERT
	tx1 := &StoreTx{
		Hash:        "hash1",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":1700002320}}`, // obs=1700002200, node ahead by 120s
		Observations: []*StoreObs{
			{ObserverID: "obs1", Timestamp: "2023-11-14T22:50:00Z"}, // 1700002200
			{ObserverID: "obs2", Timestamp: "2023-11-14T22:50:00Z"}, // 1700002200
		},
	}
	tx2 := &StoreTx{
		Hash:        "hash2",
		PayloadType: &pt,
		DecodedJSON: `{"payload":{"timestamp":1700005920}}`, // obs=1700005800, node ahead by 120s
		Observations: []*StoreObs{
			{ObserverID: "obs1", Timestamp: "2023-11-14T23:50:00Z"}, // 1700005800
			{ObserverID: "obs2", Timestamp: "2023-11-14T23:50:00Z"}, // 1700005800
		},
	}

	ps.mu.Lock()
	ps.byNode["AABB"] = []*StoreTx{tx1, tx2}
	ps.byPayloadType[4] = []*StoreTx{tx1, tx2}
	// Force recompute by setting interval to 0.
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	result := ps.GetNodeClockSkew("AABB")
	if result == nil {
		t.Fatal("expected clock skew result for node AABB")
	}
	if result.Pubkey != "AABB" {
		t.Errorf("pubkey = %q, want AABB", result.Pubkey)
	}
	// Both transmissions show 120s skew, so median should be 120.
	if result.MedianSkewSec != 120 {
		t.Errorf("median skew = %v, want 120", result.MedianSkewSec)
	}
	if result.SampleCount < 2 {
		t.Errorf("sample count = %v, want >= 2", result.SampleCount)
	}
	if result.Severity != SkewOK {
		t.Errorf("severity = %v, want ok (120s < 5min)", result.Severity)
	}
	// Drift should be ~0 since skew is constant.
	if math.Abs(result.DriftPerDaySec) > 1 {
		t.Errorf("drift = %v, want ~0 for constant skew", result.DriftPerDaySec)
	}
}

func TestGetNodeClockSkew_NoData(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	result := ps.GetNodeClockSkew("nonexistent")
	if result != nil {
		t.Error("expected nil for nonexistent node")
	}
}

// ── Sanity check tests (#XXX — clock skew crazy stats) ────────────────────────

func TestGetNodeClockSkew_NoClock_EpochZero(t *testing.T) {
	// Node with epoch-0 timestamp produces huge skew → no_clock severity, drift=0.
	ps := NewPacketStore(nil, nil)
	pt := 4 // ADVERT

	// Epoch-ish advert: advertTS near start of 2020, observed in 2023 → |skew| > 365 days
	var txs []*StoreTx
	baseObs := int64(1700000000) // ~Nov 2023
	for i := 0; i < 6; i++ {
		obsTS := baseObs + int64(i)*7200
		tx := &StoreTx{
			Hash:        "epoch-h" + string(rune('0'+i)),
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":1577836800}}`, // Jan 1 2020 — valid but way off
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		}
		txs = append(txs, tx)
	}

	ps.mu.Lock()
	ps.byNode["EPOCH"] = txs
	for _, tx := range txs {
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
	}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	result := ps.GetNodeClockSkew("EPOCH")
	if result == nil {
		t.Fatal("expected clock skew result for epoch-0 node")
	}
	if result.Severity != SkewNoClock {
		t.Errorf("severity = %v, want no_clock", result.Severity)
	}
	if result.DriftPerDaySec != 0 {
		t.Errorf("drift = %v, want 0 for no_clock node", result.DriftPerDaySec)
	}
}

func TestGetNodeClockSkew_TooFewSamplesForDrift(t *testing.T) {
	// Node with only 2 advert samples → drift should not be computed.
	ps := NewPacketStore(nil, nil)
	pt := 4

	baseObs := int64(1700000000)
	var txs []*StoreTx
	for i := 0; i < 2; i++ {
		obsTS := baseObs + int64(i)*7200
		advTS := obsTS + 120 // 120s ahead
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
		t.Errorf("drift = %v, want 0 for 2-sample node (minimum is %d)", result.DriftPerDaySec, minDriftSamples)
	}
}

func TestGetNodeClockSkew_AbsurdDriftCapped(t *testing.T) {
	// Node with wildly varying skew producing |drift| > 86400 s/day → drift capped to 0.
	ps := NewPacketStore(nil, nil)
	pt := 4

	// Create 6 samples with extreme skew variation to produce absurd drift.
	baseObs := int64(1700000000)
	var txs []*StoreTx
	for i := 0; i < 6; i++ {
		obsTS := baseObs + int64(i)*3600
		// Alternate between huge positive and negative skew offsets
		skewOffset := int64(50000 * (1 - 2*(i%2))) // +50000 or -50000
		advTS := obsTS + skewOffset
		tx := &StoreTx{
			Hash:        "wild-h" + string(rune('0'+i)),
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `}}`,
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		}
		txs = append(txs, tx)
	}

	ps.mu.Lock()
	ps.byNode["WILD"] = txs
	for _, tx := range txs {
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
	}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	result := ps.GetNodeClockSkew("WILD")
	if result == nil {
		t.Fatal("expected clock skew result")
	}
	if math.Abs(result.DriftPerDaySec) > maxReasonableDriftPerDay {
		t.Errorf("drift = %v, should be capped (|drift| > %v)", result.DriftPerDaySec, maxReasonableDriftPerDay)
	}
}

func TestGetNodeClockSkew_NormalNodeWithDrift(t *testing.T) {
	// Normal node with 6 samples and consistent linear drift → drift computed correctly.
	ps := NewPacketStore(nil, nil)
	pt := 4

	baseObs := int64(1700000000)
	var txs []*StoreTx
	for i := 0; i < 6; i++ {
		obsTS := baseObs + int64(i)*7200 // every 2 hours
		// Drift: 1 sec/hour = 24 sec/day
		advTS := obsTS + 120 + int64(i) // skew grows by 1s per sample (2h apart)
		tx := &StoreTx{
			Hash:        "norm-h" + string(rune('0'+i)),
			PayloadType: &pt,
			DecodedJSON: `{"payload":{"timestamp":` + formatInt64(advTS) + `}}`,
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		}
		txs = append(txs, tx)
	}

	ps.mu.Lock()
	ps.byNode["NORMAL"] = txs
	for _, tx := range txs {
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
	}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	result := ps.GetNodeClockSkew("NORMAL")
	if result == nil {
		t.Fatal("expected clock skew result")
	}
	if result.Severity != SkewOK {
		t.Errorf("severity = %v, want ok", result.Severity)
	}
	// 1s per 7200s = 12 s/day
	if result.DriftPerDaySec == 0 {
		t.Error("expected non-zero drift for linearly drifting node")
	}
	if math.Abs(result.DriftPerDaySec) > maxReasonableDriftPerDay {
		t.Errorf("drift = %v, should be reasonable", result.DriftPerDaySec)
	}
}

// formatInt64 is a test helper to format int64 as string for JSON embedding.
func formatInt64(n int64) string {
	return fmt.Sprintf("%d", n)
}
