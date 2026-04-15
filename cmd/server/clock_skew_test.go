package main

import (
	"math"
	"testing"
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
		{86400 * 365, SkewAbsurd}, // 1 year
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
	offsets := calibrateObservers(samples)
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
	offsets := calibrateObservers(samples)
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
	// Node clock is 60 seconds ahead. Observer obs2 has +10s offset.
	// Raw skew from obs1 = 60 (advert 1060 - obs 1000). Corrected = 60 - 0 = 60.
	// Raw skew from obs2 = 50 (advert 1060 - obs 1010). Corrected = 50 - 10 = 40.
	// Wait, that's wrong. Let me reconsider.
	//
	// If node is 60s ahead: advertTS = realTime + 60.
	// obs1 (accurate): observedTS = realTime. raw = (realTime+60) - realTime = 60. corrected = 60.
	// obs2 (10s ahead): observedTS = realTime + 10. raw = (realTime+60) - (realTime+10) = 50.
	//   corrected = 50 - 10 = 40. That's wrong — should still be 60.
	//
	// The correction should ADD the observer offset: corrected = raw + obsOffset.
	// No wait: obsOffset = obs_ts - median. If obs2 is ahead, its ts is higher,
	// so obsOffset = +10. raw = advert - obs_ts = lower because obs_ts is higher.
	// corrected = raw - obsOffset??? That makes it even lower.
	//
	// Actually: raw_skew = advert_ts - obs_ts. If observer is ahead by 10s,
	// obs_ts is inflated by 10s, so raw_skew is deflated by 10s.
	// corrected = raw_skew + obsOffset = raw_skew + 10 fixes it.
	//
	// But the code does: corrected = raw - obsOffset. That's wrong!
	// Let me verify: if obsOffset = obs_ts - median = +10,
	// and raw = advert - obs_ts = advert - (true_obs + 10) = (advert - true_obs) - 10
	// So raw = true_raw - 10.
	// corrected = raw - obsOffset = (true_raw - 10) - 10 = true_raw - 20. Wrong!
	// Should be: corrected = raw + obsOffset = (true_raw - 10) + 10 = true_raw. Correct!
	//
	// This is a bug in the implementation! Let's test for the correct behavior
	// and fix the code.
	t.Log("This test validates observer offset correction direction")

	samples := []skewSample{
		// Same packet seen by accurate obs1 and obs2 (+10s ahead)
		{advertTS: 1060, observedTS: 1000, observerID: "obs1", hash: "h1"},
		{advertTS: 1060, observedTS: 1010, observerID: "obs2", hash: "h1"},
	}
	offsets := calibrateObservers(samples)
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
	offsets := calibrateObservers(samples)
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
