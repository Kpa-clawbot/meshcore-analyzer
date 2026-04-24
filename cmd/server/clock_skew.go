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
	SkewDefault   SkewSeverity = "default"   // firmware-default epoch + uptime
	SkewOK        SkewSeverity = "ok"        // |skew| <= 15s
	SkewDegrading SkewSeverity = "degrading" // 15s < |skew| <= 60s
	SkewDegraded  SkewSeverity = "degraded"  // 60s < |skew| <= 600s
	SkewWrong     SkewSeverity = "wrong"     // |skew| > 600s and not default
)

// Known firmware default epochs. Nodes with advert_ts in
// [epoch, epoch + maxPlausibleUptimeSec] are classified as "default".
// See docs/clock-skew-redesign.md for provenance of each value.
var defaultEpochs = []int64{0, 1609459200, 1672531200, 1715770351}

// Default thresholds in seconds.
const (
	// maxPlausibleUptimeSec caps how far past a default epoch we still
	// consider "default + uptime ticking". 730 days ≈ 2 years.
	maxPlausibleUptimeSec = 730 * 86400

	// Severity band boundaries (absolute skew in seconds).
	skewThresholdOKSec        = 15
	skewThresholdDegradingSec = 60
	skewThresholdDegradedSec  = 600

	// minDriftSamples is the minimum number of advert transmissions needed
	// to compute a meaningful linear drift rate.
	minDriftSamples = 5

	// maxReasonableDriftPerDay caps drift display. Physically impossible
	// drift rates (> 1 day/day) indicate insufficient or outlier samples.
	maxReasonableDriftPerDay = 86400.0

	// maxPlausibleSkewJumpSec is the largest skew change between
	// consecutive samples that we treat as physical drift.
	maxPlausibleSkewJumpSec = 60.0

	// theilSenMaxPoints caps the number of points fed to Theil-Sen
	// regression (O(n²) in pairs).
	theilSenMaxPoints = 200
)

// isDefaultEpoch returns true if the raw advert timestamp falls within
// [epoch, epoch + maxPlausibleUptimeSec] for any known firmware default.
// If matched, returns the matched epoch; otherwise returns 0.
func isDefaultEpoch(advertTS int64) (bool, int64) {
	// Find the largest epoch <= advertTS (closest match). Since ranges
	// overlap, picking the closest avoids attributing a 2023-firmware
	// node's timestamp to the 2024 epoch.
	bestEpoch := int64(-1)
	for _, epoch := range defaultEpochs {
		if epoch <= advertTS && epoch > bestEpoch {
			bestEpoch = epoch
		}
	}
	if bestEpoch >= 0 && advertTS <= bestEpoch+maxPlausibleUptimeSec {
		return true, bestEpoch
	}
	return false, 0
}

// classifySkew maps a raw advert timestamp and corrected absolute skew
// to a severity level. Default detection runs on the raw advert_ts
// (independent of observer calibration).
func classifySkew(advertTS int64, absCorrectedSkewSec float64) (SkewSeverity, int64) {
	if ok, epoch := isDefaultEpoch(advertTS); ok {
		return SkewDefault, epoch
	}
	switch {
	case absCorrectedSkewSec <= skewThresholdOKSec:
		return SkewOK, 0
	case absCorrectedSkewSec <= skewThresholdDegradingSec:
		return SkewDegrading, 0
	case absCorrectedSkewSec <= skewThresholdDegradedSec:
		return SkewDegraded, 0
	default:
		return SkewWrong, 0
	}
}

// ── Data Types ─────────────────────────────────────────────────────────────────

// skewSample is a single raw skew measurement from one advert observation.
type skewSample struct {
	advertTS   int64  // node's advert Unix timestamp
	observedTS int64  // observation Unix timestamp
	observerID string // which observer saw this
	hash       string // transmission hash (for multi-observer grouping)
}

// ObserverCalibration holds the computed clock offset for an observer.
type ObserverCalibration struct {
	ObserverID string  `json:"observerID"`
	OffsetSec  float64 `json:"offsetSec"` // positive = observer clock ahead
	Samples    int     `json:"samples"`   // number of multi-observer packets used
}

// NodeClockSkew is the API response for a single node's clock skew data.
type NodeClockSkew struct {
	Pubkey         string       `json:"pubkey"`
	MeanSkewSec    float64      `json:"meanSkewSec"`    // corrected mean skew (positive = node ahead)
	MedianSkewSec  float64      `json:"medianSkewSec"`  // corrected median skew
	LastSkewSec    float64      `json:"lastSkewSec"`    // most recent corrected skew
	DriftPerDaySec float64      `json:"driftPerDaySec"` // linear drift rate (sec/day)
	Severity       SkewSeverity `json:"severity"`
	SampleCount    int          `json:"sampleCount"`
	Calibrated     bool         `json:"calibrated"`     // true if observer calibration was applied
	LastAdvertTS   int64        `json:"lastAdvertTS"`    // most recent advert timestamp
	LastObservedTS int64        `json:"lastObservedTS"`  // most recent observation timestamp
	DefaultEpoch   *int64       `json:"defaultEpoch,omitempty"` // matched epoch when severity=default
	Samples        []SkewSample `json:"samples,omitempty"` // time-series for sparklines
	NodeName       string       `json:"nodeName,omitempty"` // populated in fleet responses
	NodeRole       string       `json:"nodeRole,omitempty"` // populated in fleet responses
}

// SkewSample is a single (timestamp, skew) point for sparkline rendering.
type SkewSample struct {
	Timestamp int64   `json:"ts"`   // Unix epoch of observation
	SkewSec   float64 `json:"skew"` // corrected skew in seconds
}

// txSkewResult maps tx hash → per-transmission skew stats.
type txSkewResult = map[string]*NodeClockSkew

// ── Clock Skew Engine ──────────────────────────────────────────────────────────

// ClockSkewEngine computes and caches clock skew data for nodes and observers.
type ClockSkewEngine struct {
	mu              sync.RWMutex
	observerOffsets map[string]float64 // observerID → calibrated offset (seconds)
	observerSamples map[string]int     // observerID → number of multi-observer packets used
	nodeSkew        txSkewResult
	lastComputed    time.Time
	computeInterval time.Duration
}

func NewClockSkewEngine() *ClockSkewEngine {
	return &ClockSkewEngine{
		observerOffsets: make(map[string]float64),
		observerSamples: make(map[string]int),
		nodeSkew:        make(txSkewResult),
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
		advertTS := extractTimestamp(decoded)
		if advertTS < 0 {
			continue
		}
		// Allow epoch 0 and above (needed for default-epoch detection).
		// Upper bound: year 2100.
		if advertTS > 4102444800 {
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

// timestampMissing is the sentinel returned by extractTimestamp when no
// timestamp field is present in the decoded advert. Using -1 lets us
// distinguish "field absent" from a real epoch-0 timestamp (ts == 0).
const timestampMissing int64 = -1

// extractTimestamp gets the Unix timestamp from a decoded ADVERT payload.
// Returns timestampMissing (-1) if no timestamp field is found.
func extractTimestamp(decoded map[string]interface{}) int64 {
	if payload, ok := decoded["payload"]; ok {
		if pm, ok := payload.(map[string]interface{}); ok {
			if ts, ok := jsonNumberOk(pm, "timestamp"); ok {
				return ts
			}
		}
	}
	if ts, ok := jsonNumberOk(decoded, "timestamp"); ok {
		return ts
	}
	return timestampMissing
}

// jsonNumberOk extracts an int64 from a JSON-parsed map, returning (value, true)
// if the key exists and is numeric, or (0, false) otherwise.
func jsonNumberOk(m map[string]interface{}, key string) (int64, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
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
	byHash := make(map[string][]skewSample)
	for _, s := range samples {
		byHash[s.hash] = append(byHash[s.hash], s)
	}

	deviations := make(map[string][]float64)
	for _, group := range byHash {
		if len(group) < 2 {
			continue
		}
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
			corrected = rawSkew + obsOffset
		}
		byHash[s.hash] = append(byHash[s.hash], correctedSample{
			skew:       corrected,
			observedTS: s.observedTS,
			calibrated: hasCal,
		})
		hashAdvertTS[s.hash] = s.advertTS
	}

	result := make(map[string]*NodeClockSkew)
	for hash, cs := range byHash {
		skews := make([]float64, len(cs))
		for i, c := range cs {
			skews[i] = c.skew
		}
		medSkew := median(skews)
		meanSkew := mean(skews)

		// Pick the skew from the most recent observation (max observedTS),
		// not the last-appended sample which may be non-chronological.
		var latest correctedSample
		var anyCal bool
		for _, c := range cs {
			if c.observedTS > latest.observedTS {
				latest = c
			}
			if c.calibrated {
				anyCal = true
			}
		}
		lastCorrectedSkew := latest.skew
		advTS := hashAdvertTS[hash]
		severity, matchedEpoch := classifySkew(advTS, math.Abs(lastCorrectedSkew))

		ncs := &NodeClockSkew{
			MeanSkewSec:    round(meanSkew, 1),
			MedianSkewSec:  round(medSkew, 1),
			LastSkewSec:    round(lastCorrectedSkew, 1),
			Severity:       severity,
			SampleCount:    len(cs),
			Calibrated:     anyCal,
			LastAdvertTS:   advTS,
			LastObservedTS: latest.observedTS,
		}
		if severity == SkewDefault {
			ep := matchedEpoch
			ncs.DefaultEpoch = &ep
		}
		result[hash] = ncs
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

	// Classify using the most recent advert's raw timestamp and
	// the most recent corrected skew. No windowing or median-driven
	// severity — per-advert classification per the spec.
	severity, matchedEpoch := classifySkew(lastAdvTS, math.Abs(lastSkew))

	// Drift: display only, not a classifier input.
	var drift float64
	if severity != SkewDefault && len(tsSkews) >= minDriftSamples {
		drift = computeDrift(tsSkews)
		if math.Abs(drift) > maxReasonableDriftPerDay {
			drift = 0
		}
	}

	// Build sparkline samples.
	sort.Slice(tsSkews, func(i, j int) bool { return tsSkews[i].ts < tsSkews[j].ts })
	samples := make([]SkewSample, len(tsSkews))
	for i, p := range tsSkews {
		samples[i] = SkewSample{Timestamp: p.ts, SkewSec: round(p.skew, 1)}
	}

	result := &NodeClockSkew{
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
	if severity == SkewDefault {
		ep := matchedEpoch
		result.DefaultEpoch = &ep
	}
	return result
}

// GetFleetClockSkew returns clock skew data for all nodes that have skew data.
// Must NOT be called with s.mu held.
func (s *PacketStore) GetFleetClockSkew() []*NodeClockSkew {
	s.mu.RLock()
	defer s.mu.RUnlock()

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
		if ni, ok := nameMap[pubkey]; ok {
			cs.NodeName = ni.Name
			cs.NodeRole = ni.Role
		}
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
// (timestamp, skew) pairs using Theil-Sen regression with outlier filtering.
func computeDrift(pairs []tsSkewPair) float64 {
	if len(pairs) < 2 {
		return 0
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].ts < pairs[j].ts
	})

	spanSec := float64(pairs[len(pairs)-1].ts - pairs[0].ts)
	if spanSec < 3600 {
		return 0
	}

	filtered := make([]tsSkewPair, 0, len(pairs))
	filtered = append(filtered, pairs[0])
	for i := 1; i < len(pairs); i++ {
		prev := filtered[len(filtered)-1]
		if math.Abs(pairs[i].skew-prev.skew) <= maxPlausibleSkewJumpSec {
			filtered = append(filtered, pairs[i])
		}
	}
	if len(filtered) < 2 || float64(filtered[len(filtered)-1].ts-filtered[0].ts) < 3600 {
		filtered = pairs
	}

	if len(filtered) > theilSenMaxPoints {
		filtered = filtered[len(filtered)-theilSenMaxPoints:]
	}

	return theilSenSlope(filtered) * 86400
}

// theilSenSlope returns the Theil-Sen estimator: median of all pairwise slopes.
func theilSenSlope(pairs []tsSkewPair) float64 {
	n := len(pairs)
	if n < 2 {
		return 0
	}
	slopes := make([]float64, 0, n*(n-1)/2)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			dt := float64(pairs[j].ts - pairs[i].ts)
			if dt <= 0 {
				continue
			}
			slopes = append(slopes, (pairs[j].skew-pairs[i].skew)/dt)
		}
	}
	if len(slopes) == 0 {
		return 0
	}
	return median(slopes)
}
