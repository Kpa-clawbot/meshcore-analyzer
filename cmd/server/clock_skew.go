package main

import (
	"math"
	"sort"
	"sync"
	"time"
)

// ── Clock Skew Severity ────────────────────────────────────────────────────────

type SkewSeverity string

const (
	SkewOK       SkewSeverity = "ok"       // < 5 min
	SkewWarning  SkewSeverity = "warning"  // 5 min – 1 hour
	SkewCritical SkewSeverity = "critical" // 1 hour – 30 days
	SkewAbsurd   SkewSeverity = "absurd"   // > 30 days
	SkewNoClock  SkewSeverity = "no_clock" // > 365 days — uninitialized RTC
)

// Default thresholds in seconds.
const (
	skewThresholdWarnSec     = 5 * 60          // 5 minutes
	skewThresholdCriticalSec = 60 * 60         // 1 hour
	skewThresholdAbsurdSec   = 30 * 24 * 3600  // 30 days
	skewThresholdNoClockSec  = 365 * 24 * 3600 // 365 days — uninitialized RTC

	// minDriftSamples is the minimum number of advert transmissions needed
	// to compute a meaningful linear drift rate.
	minDriftSamples = 5

	// maxReasonableDriftPerDay caps drift display. Physically impossible
	// drift rates (> 1 day/day) indicate insufficient or outlier samples.
	maxReasonableDriftPerDay = 86400.0
)

// classifySkew maps absolute skew (seconds) to a severity level.
// Float64 comparison is safe: inputs are rounded to 1 decimal via round(),
// and thresholds are integer multiples of 60 — no rounding artifacts.
func classifySkew(absSkewSec float64) SkewSeverity {
	switch {
	case absSkewSec >= skewThresholdNoClockSec:
		return SkewNoClock
	case absSkewSec >= skewThresholdAbsurdSec:
		return SkewAbsurd
	case absSkewSec >= skewThresholdCriticalSec:
		return SkewCritical
	case absSkewSec >= skewThresholdWarnSec:
		return SkewWarning
	default:
		return SkewOK
	}
}

// ── Data Types ─────────────────────────────────────────────────────────────────

// skewSample is a single raw skew measurement from one advert observation.
type skewSample struct {
	advertTS    int64  // node's advert Unix timestamp
	observedTS  int64  // observation Unix timestamp
	observerID  string // which observer saw this
	hash        string // transmission hash (for multi-observer grouping)
}

// ObserverCalibration holds the computed clock offset for an observer.
type ObserverCalibration struct {
	ObserverID string  `json:"observerID"`
	OffsetSec  float64 `json:"offsetSec"`  // positive = observer clock ahead
	Samples    int     `json:"samples"`    // number of multi-observer packets used
}

// NodeClockSkew is the API response for a single node's clock skew data.
type NodeClockSkew struct {
	Pubkey          string       `json:"pubkey"`
	MeanSkewSec     float64      `json:"meanSkewSec"`     // corrected mean skew (positive = node ahead)
	MedianSkewSec   float64      `json:"medianSkewSec"`   // corrected median skew
	LastSkewSec     float64      `json:"lastSkewSec"`     // most recent corrected skew
	DriftPerDaySec  float64      `json:"driftPerDaySec"`  // linear drift rate (sec/day)
	Severity        SkewSeverity `json:"severity"`
	SampleCount     int          `json:"sampleCount"`
	Calibrated      bool         `json:"calibrated"`      // true if observer calibration was applied
	LastAdvertTS    int64        `json:"lastAdvertTS"`     // most recent advert timestamp
	LastObservedTS  int64        `json:"lastObservedTS"`   // most recent observation timestamp
	Samples         []SkewSample `json:"samples,omitempty"` // time-series for sparklines
	NodeName        string       `json:"nodeName,omitempty"` // populated in fleet responses
	NodeRole        string       `json:"nodeRole,omitempty"` // populated in fleet responses
}

// SkewSample is a single (timestamp, skew) point for sparkline rendering.
type SkewSample struct {
	Timestamp int64   `json:"ts"`   // Unix epoch of observation
	SkewSec   float64 `json:"skew"` // corrected skew in seconds
}

// txSkewResult maps tx hash → per-transmission skew stats. This is an
// intermediate result keyed by hash (not pubkey); the store maps hash → pubkey
// when building the final per-node view.
type txSkewResult = map[string]*NodeClockSkew

// ── Clock Skew Engine ──────────────────────────────────────────────────────────

// ClockSkewEngine computes and caches clock skew data for nodes and observers.
type ClockSkewEngine struct {
	mu               sync.RWMutex
	observerOffsets  map[string]float64 // observerID → calibrated offset (seconds)
	observerSamples  map[string]int     // observerID → number of multi-observer packets used
	nodeSkew         txSkewResult
	lastComputed     time.Time
	computeInterval  time.Duration
}

func NewClockSkewEngine() *ClockSkewEngine {
	return &ClockSkewEngine{
		observerOffsets:  make(map[string]float64),
		observerSamples: make(map[string]int),
		nodeSkew:       make(txSkewResult),
		computeInterval: 30 * time.Second,
	}
}

// Recompute recalculates all clock skew data from the packet store.
// Called periodically or on demand. Holds store RLock externally.
// Uses read-copy-update: heavy computation runs outside the write lock,
// then results are swapped in under a brief lock.
func (e *ClockSkewEngine) Recompute(store *PacketStore) {
	// Fast path: check under read lock if recompute is needed.
	e.mu.RLock()
	fresh := time.Since(e.lastComputed) < e.computeInterval
	e.mu.RUnlock()
	if fresh {
		return
	}

	// Phase 1: Collect skew samples from ADVERT packets (store RLock held by caller).
	samples := collectSamples(store)

	// Phase 2–3: Compute outside the write lock.
	var newOffsets map[string]float64
	var newSamples map[string]int
	var newNodeSkew txSkewResult

	if len(samples) > 0 {
		newOffsets, newSamples = calibrateObservers(samples)
		newNodeSkew = computeNodeSkew(samples, newOffsets)
	} else {
		newOffsets = make(map[string]float64)
		newSamples = make(map[string]int)
		newNodeSkew = make(txSkewResult)
	}

	// Swap results under brief write lock.
	e.mu.Lock()
	// Re-check: another goroutine may have computed while we were working.
	if time.Since(e.lastComputed) < e.computeInterval {
		e.mu.Unlock()
		return
	}
	e.observerOffsets = newOffsets
	e.observerSamples = newSamples
	e.nodeSkew = newNodeSkew
	e.lastComputed = time.Now()
	e.mu.Unlock()
}

// collectSamples extracts skew samples from ADVERT packets in the store.
// Must be called with store.mu held (at least RLock).
func collectSamples(store *PacketStore) []skewSample {
	adverts := store.byPayloadType[PayloadADVERT]
	if len(adverts) == 0 {
		return nil
	}

	samples := make([]skewSample, 0, len(adverts)*2)
	for _, tx := range adverts {
		decoded := tx.ParsedDecoded()
		if decoded == nil {
			continue
		}
		// Extract advert timestamp from decoded JSON.
		advertTS := extractTimestamp(decoded)
		if advertTS <= 0 {
			continue
		}
		// Sanity: skip timestamps before year 2020 or after year 2100.
		if advertTS < 1577836800 || advertTS > 4102444800 {
			continue
		}

		for _, obs := range tx.Observations {
			obsTS := parseISO(obs.Timestamp)
			if obsTS <= 0 {
				continue
			}
			samples = append(samples, skewSample{
				advertTS:   advertTS,
				observedTS: obsTS,
				observerID: obs.ObserverID,
				hash:       tx.Hash,
			})
		}
	}
	return samples
}

// extractTimestamp gets the Unix timestamp from a decoded ADVERT payload.
func extractTimestamp(decoded map[string]interface{}) int64 {
	// Try payload.timestamp first (nested in "payload" key).
	if payload, ok := decoded["payload"]; ok {
		if pm, ok := payload.(map[string]interface{}); ok {
			if ts := jsonNumber(pm, "timestamp"); ts > 0 {
				return ts
			}
		}
	}
	// Fallback: top-level timestamp.
	if ts := jsonNumber(decoded, "timestamp"); ts > 0 {
		return ts
	}
	return 0
}

// jsonNumber extracts an int64 from a JSON-parsed map (handles float64 and json.Number).
func jsonNumber(m map[string]interface{}, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

// parseISO parses an ISO 8601 timestamp string to Unix seconds.
func parseISO(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Try with fractional seconds.
		t, err = time.Parse("2006-01-02T15:04:05.999999999Z07:00", s)
		if err != nil {
			return 0
		}
	}
	return t.Unix()
}

// ── Phase 2: Observer Calibration ──────────────────────────────────────────────

// calibrateObservers computes each observer's clock offset using multi-observer
// packets. Returns offset map and sample count map.
func calibrateObservers(samples []skewSample) (map[string]float64, map[string]int) {
	// Group observations by packet hash.
	byHash := make(map[string][]skewSample)
	for _, s := range samples {
		byHash[s.hash] = append(byHash[s.hash], s)
	}

	// For each multi-observer packet, compute per-observer deviation from median.
	deviations := make(map[string][]float64) // observerID → list of deviations
	for _, group := range byHash {
		if len(group) < 2 {
			continue // single-observer packet, can't calibrate
		}
		// Compute median observation timestamp for this packet.
		obsTimes := make([]float64, len(group))
		for i, s := range group {
			obsTimes[i] = float64(s.observedTS)
		}
		medianObs := median(obsTimes)
		for _, s := range group {
			dev := float64(s.observedTS) - medianObs
			deviations[s.observerID] = append(deviations[s.observerID], dev)
		}
	}

	// Each observer's offset = median of its deviations.
	offsets := make(map[string]float64, len(deviations))
	counts := make(map[string]int, len(deviations))
	for obsID, devs := range deviations {
		offsets[obsID] = median(devs)
		counts[obsID] = len(devs)
	}
	return offsets, counts
}

// ── Phase 3: Per-Node Skew ─────────────────────────────────────────────────────

// computeNodeSkew calculates corrected skew statistics for each node.
func computeNodeSkew(samples []skewSample, obsOffsets map[string]float64) txSkewResult {
	// Compute corrected skew per sample, grouped by hash (each hash = one
	// node's advert transmission). The caller maps hash → pubkey via byNode.
	type correctedSample struct {
		skew       float64
		observedTS int64
		calibrated bool
	}

	byHash := make(map[string][]correctedSample)
	hashAdvertTS := make(map[string]int64)

	for _, s := range samples {
		obsOffset, hasCal := obsOffsets[s.observerID]
		rawSkew := float64(s.advertTS - s.observedTS)
		corrected := rawSkew
		if hasCal {
			// Observer offset = obs_ts - median(all_obs_ts). If observer is ahead,
			// its obs_ts is inflated, making raw_skew too low. Add offset to correct.
			corrected = rawSkew + obsOffset
		}
		byHash[s.hash] = append(byHash[s.hash], correctedSample{
			skew:       corrected,
			observedTS: s.observedTS,
			calibrated: hasCal,
		})
		hashAdvertTS[s.hash] = s.advertTS
	}

	// Each hash represents one advert from one node. Compute median corrected
	// skew per hash (across multiple observers).

	result := make(map[string]*NodeClockSkew) // keyed by hash for now
	for hash, cs := range byHash {
		skews := make([]float64, len(cs))
		for i, c := range cs {
			skews[i] = c.skew
		}
		medSkew := median(skews)
		meanSkew := mean(skews)

		// Find latest observation.
		var latestObsTS int64
		var anyCal bool
		for _, c := range cs {
			if c.observedTS > latestObsTS {
				latestObsTS = c.observedTS
			}
			if c.calibrated {
				anyCal = true
			}
		}

		absMedian := math.Abs(medSkew)
		result[hash] = &NodeClockSkew{
			MeanSkewSec:    round(meanSkew, 1),
			MedianSkewSec:  round(medSkew, 1),
			LastSkewSec:    round(cs[len(cs)-1].skew, 1),
			Severity:       classifySkew(absMedian),
			SampleCount:    len(cs),
			Calibrated:     anyCal,
			LastAdvertTS:   hashAdvertTS[hash],
			LastObservedTS: latestObsTS,
		}
	}
	return result
}

// ── Integration with PacketStore ───────────────────────────────────────────────

// GetNodeClockSkew returns the clock skew data for a specific node (acquires RLock).
func (s *PacketStore) GetNodeClockSkew(pubkey string) *NodeClockSkew {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getNodeClockSkewLocked(pubkey)
}

// getNodeClockSkewLocked returns clock skew for a node.
// Must be called with s.mu held (at least RLock).
func (s *PacketStore) getNodeClockSkewLocked(pubkey string) *NodeClockSkew {
	s.clockSkew.Recompute(s)

	txs := s.byNode[pubkey]
	if len(txs) == 0 {
		return nil
	}

	s.clockSkew.mu.RLock()
	defer s.clockSkew.mu.RUnlock()

	var allSkews []float64
	var lastSkew float64
	var lastObsTS, lastAdvTS int64
	var totalSamples int
	var anyCal bool
	var tsSkews []tsSkewPair

	for _, tx := range txs {
		if tx.PayloadType == nil || *tx.PayloadType != PayloadADVERT {
			continue
		}
		cs, ok := s.clockSkew.nodeSkew[tx.Hash]
		if !ok {
			continue
		}
		allSkews = append(allSkews, cs.MedianSkewSec)
		totalSamples += cs.SampleCount
		if cs.Calibrated {
			anyCal = true
		}
		if cs.LastObservedTS > lastObsTS {
			lastObsTS = cs.LastObservedTS
			lastSkew = cs.LastSkewSec
			lastAdvTS = cs.LastAdvertTS
		}
		tsSkews = append(tsSkews, tsSkewPair{ts: cs.LastObservedTS, skew: cs.MedianSkewSec})
	}

	if len(allSkews) == 0 {
		return nil
	}

	medSkew := median(allSkews)
	meanSkew := mean(allSkews)
	absMedian := math.Abs(medSkew)
	severity := classifySkew(absMedian)

	// For no_clock nodes (uninitialized RTC), skip drift — data is meaningless.
	var drift float64
	if severity != SkewNoClock && len(tsSkews) >= minDriftSamples {
		drift = computeDrift(tsSkews)
		// Cap physically impossible drift rates.
		if math.Abs(drift) > maxReasonableDriftPerDay {
			drift = 0
		}
	}

	// Build sparkline samples from tsSkews (sorted by time).
	sort.Slice(tsSkews, func(i, j int) bool { return tsSkews[i].ts < tsSkews[j].ts })
	samples := make([]SkewSample, len(tsSkews))
	for i, p := range tsSkews {
		samples[i] = SkewSample{Timestamp: p.ts, SkewSec: round(p.skew, 1)}
	}

	return &NodeClockSkew{
		Pubkey:         pubkey,
		MeanSkewSec:    round(meanSkew, 1),
		MedianSkewSec:  round(medSkew, 1),
		LastSkewSec:    round(lastSkew, 1),
		DriftPerDaySec: round(drift, 2),
		Severity:       severity,
		SampleCount:    totalSamples,
		Calibrated:     anyCal,
		LastAdvertTS:   lastAdvTS,
		LastObservedTS: lastObsTS,
		Samples:        samples,
	}
}

// GetFleetClockSkew returns clock skew data for all nodes that have skew data.
// Must NOT be called with s.mu held.
func (s *PacketStore) GetFleetClockSkew() []*NodeClockSkew {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build name/role lookup from DB cache (requires s.mu held).
	allNodes, _ := s.getCachedNodesAndPM()
	nameMap := make(map[string]nodeInfo, len(allNodes))
	for _, ni := range allNodes {
		nameMap[ni.PublicKey] = ni
	}

	var results []*NodeClockSkew
	for pubkey := range s.byNode {
		cs := s.getNodeClockSkewLocked(pubkey)
		if cs == nil {
			continue
		}
		// Enrich with node name/role.
		if ni, ok := nameMap[pubkey]; ok {
			cs.NodeName = ni.Name
			cs.NodeRole = ni.Role
		}
		// Omit samples in fleet response (too much data).
		cs.Samples = nil
		results = append(results, cs)
	}
	return results
}

// GetObserverCalibrations returns the current observer clock offsets.
func (s *PacketStore) GetObserverCalibrations() []ObserverCalibration {
	s.mu.RLock()
	defer s.mu.RUnlock()

	s.clockSkew.Recompute(s)

	s.clockSkew.mu.RLock()
	defer s.clockSkew.mu.RUnlock()

	result := make([]ObserverCalibration, 0, len(s.clockSkew.observerOffsets))
	for obsID, offset := range s.clockSkew.observerOffsets {
		result = append(result, ObserverCalibration{
			ObserverID: obsID,
			OffsetSec:  round(offset, 1),
			Samples:    s.clockSkew.observerSamples[obsID],
		})
	}
	// Sort by absolute offset descending.
	sort.Slice(result, func(i, j int) bool {
		return math.Abs(result[i].OffsetSec) > math.Abs(result[j].OffsetSec)
	})
	return result
}

// ── Math Helpers ───────────────────────────────────────────────────────────────

func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[n/2]
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// tsSkewPair is a (timestamp, skew) pair for drift estimation.
type tsSkewPair struct {
	ts   int64
	skew float64
}

// computeDrift estimates linear drift in seconds per day from time-ordered
// (timestamp, skew) pairs using simple linear regression.
func computeDrift(pairs []tsSkewPair) float64 {
	if len(pairs) < 2 {
		return 0
	}
	// Sort by timestamp.
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].ts < pairs[j].ts
	})

	// Time span too short? Skip.
	spanSec := float64(pairs[len(pairs)-1].ts - pairs[0].ts)
	if spanSec < 3600 { // need at least 1 hour of data
		return 0
	}

	// Simple linear regression: skew = a + b*t
	n := float64(len(pairs))
	var sumX, sumY, sumXY, sumX2 float64
	for _, p := range pairs {
		x := float64(p.ts - pairs[0].ts) // normalize to avoid large numbers
		y := p.skew
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	denom := n*sumX2 - sumX*sumX
	if denom == 0 {
		return 0
	}
	slope := (n*sumXY - sumX*sumY) / denom // seconds of drift per second
	return slope * 86400                     // convert to seconds per day
}
