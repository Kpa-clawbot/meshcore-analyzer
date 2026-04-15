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
)

// Default thresholds in seconds.
const (
	skewThresholdWarnSec     = 5 * 60        // 5 minutes
	skewThresholdCriticalSec = 60 * 60       // 1 hour
	skewThresholdAbsurdSec   = 30 * 24 * 3600 // 30 days
)

func classifySkew(absSkewSec float64) SkewSeverity {
	switch {
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
}

// ── Clock Skew Engine ──────────────────────────────────────────────────────────

// ClockSkewEngine computes and caches clock skew data for nodes and observers.
type ClockSkewEngine struct {
	mu               sync.RWMutex
	observerOffsets  map[string]float64 // observerID → calibrated offset (seconds)
	observerSamples  map[string]int     // observerID → number of multi-observer packets used
	nodeSkew         map[string]*NodeClockSkew
	lastComputed     time.Time
	computeInterval  time.Duration
}

func NewClockSkewEngine() *ClockSkewEngine {
	return &ClockSkewEngine{
		observerOffsets:  make(map[string]float64),
		observerSamples: make(map[string]int),
		nodeSkew:       make(map[string]*NodeClockSkew),
		computeInterval: 30 * time.Second,
	}
}

// Recompute recalculates all clock skew data from the packet store.
// Called periodically or on demand. Holds store RLock externally.
func (e *ClockSkewEngine) Recompute(store *PacketStore) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if time.Since(e.lastComputed) < e.computeInterval {
		return
	}

	// Phase 1: Collect skew samples from ADVERT packets.
	samples := e.collectSamples(store)
	if len(samples) == 0 {
		e.lastComputed = time.Now()
		return
	}

	// Phase 2: Observer calibration via multi-observer cross-check.
	e.observerOffsets, e.observerSamples = calibrateObservers(samples)

	// Phase 3: Compute per-node corrected skew.
	e.nodeSkew = computeNodeSkew(samples, e.observerOffsets)

	e.lastComputed = time.Now()
}

// collectSamples extracts skew samples from ADVERT packets in the store.
// Must be called with store.mu held (at least RLock).
func (e *ClockSkewEngine) collectSamples(store *PacketStore) []skewSample {
	advertType := 4
	adverts := store.byPayloadType[advertType]
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
func computeNodeSkew(samples []skewSample, obsOffsets map[string]float64) map[string]*NodeClockSkew {
	// Group samples by node pubkey (extracted from advert).
	// We need to figure out which node each sample belongs to.
	// The advert's public key is in the decoded JSON. Since we don't carry it
	// in skewSample, we group by hash first to get the node pubkey from the
	// transmission's byNode index. But we don't have that here.
	//
	// Actually, let's re-derive: each ADVERT transmission is indexed under
	// its advertised pubkey in store.byNode. We need the pubkey.
	// We'll compute skew keyed by hash and group later.
	//
	// Simpler approach: compute corrected skew per sample, group by hash
	// (each hash = one node's advert), then aggregate.
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

	// Each hash represents one advert from one node. Get the median corrected
	// skew per hash (if multiple observers saw it).
	type advertSkew struct {
		medianSkew  float64
		observedTS  int64
		calibrated  bool
	}

	// We need to group by node pubkey. Since samples don't carry pubkey,
	// we can't group here. We'll return keyed by hash and let the caller
	// resolve to pubkey. But that's awkward.
	//
	// Better: add pubkey to skewSample. Let's refactor.
	// For now, since each hash in byPayloadType[ADVERT] corresponds to one
	// node's advert, and the store.byNode index maps pubkey → txs, we
	// should collect pubkey at sample collection time.
	//
	// This is a design issue. Let me return per-hash results.
	// The caller (GetNodeClockSkew) will map hash → pubkey.

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
		if tx.PayloadType == nil || *tx.PayloadType != 4 {
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
	drift := computeDrift(tsSkews)

	return &NodeClockSkew{
		Pubkey:         pubkey,
		MeanSkewSec:    round(meanSkew, 1),
		MedianSkewSec:  round(medSkew, 1),
		LastSkewSec:    round(lastSkew, 1),
		DriftPerDaySec: round(drift, 2),
		Severity:       classifySkew(absMedian),
		SampleCount:    totalSamples,
		Calibrated:     anyCal,
		LastAdvertTS:   lastAdvTS,
		LastObservedTS: lastObsTS,
	}
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
