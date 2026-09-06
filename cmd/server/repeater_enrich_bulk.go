package main

import (
	"slices"
	"strings"
	"time"
)

// GetRepeaterRelayInfoMap returns a cached pubkey → RepeaterRelayInfo
// map covering EVERY pubkey that currently appears as a path hop in any
// non-advert StoreTx. This is the bulk equivalent of calling
// GetRepeaterRelayInfo(pk, windowHours) once per node.
//
// Why this exists (issue #1257): handleNodes used to call the per-node
// helper inside a per-page loop. Each call grabbed its own RLock and
// re-parsed FirstSeen on every StoreTx indexed under that pubkey's
// byPathHop entry (plus, when the pubkey was >= 2 hex chars, the 1-byte
// prefix bucket — which on busy networks fans out to almost the whole
// non-advert tx set). For the default top-50 page of hot repeaters this
// burned 50 lock acquisitions and hundreds of thousands of timestamp
// parses per request, dominating /api/nodes latency.
//
// The cached map is keyed by lowercase pubkey/hop key (same shape as
// byPathHop). Lookups should use strings.ToLower(pk).
//
// The cache is refreshed by the background recomputer (every 5 min by
// default). This function never rebuilds inline on a populated cache —
// serving a slightly stale snapshot is always preferable to a 700ms
// on-request rebuild. The only time an inline compute happens is when
// the cache is nil (i.e. before the recomputer's synchronous prewarm
// completes, which can occur in tests without a running recomputer).
func (s *PacketStore) GetRepeaterRelayInfoMap(windowHours float64) map[string]RepeaterRelayInfo {
	s.repeaterEnrichMu.Lock()
	cached := s.repeaterRelayCache
	s.repeaterEnrichMu.Unlock()
	if cached != nil {
		return cached
	}

	// Cache is nil — recomputer hasn't prewarmed yet (edge case: tests
	// without a running recomputer, or a request racing the initial
	// synchronous prewarm). Build once inline; the recomputer takes over.
	result := s.computeRepeaterRelayInfoMap(windowHours)

	s.repeaterEnrichMu.Lock()
	if s.repeaterRelayCache == nil {
		s.repeaterRelayCache = result
		s.repeaterRelayCacheWin = windowHours
		s.repeaterRelayAt = time.Now()
	}
	cached = s.repeaterRelayCache
	s.repeaterEnrichMu.Unlock()
	return cached
}

// snapshotPathHopIndexLocked copies hop buckets for reads after releasing s.mu.
// Eviction and raw-path updates compact their backing arrays in place, so a
// header copy would allow those writers to change an in-flight snapshot.
// The caller must hold s.mu for reading. Cost: O(total path-hop entries).
func (s *PacketStore) snapshotPathHopIndexLocked() map[string][]*StoreTx {
	snap := make(map[string][]*StoreTx, len(s.byPathHop))
	for k, list := range s.byPathHop {
		snap[k] = slices.Clone(list)
	}
	return snap
}

// computeRepeaterRelayInfoMap walks byPathHop once under a single RLock,
// pre-parses every FirstSeen timestamp once (not once-per-pubkey-bucket),
// and emits one RepeaterRelayInfo per hop key.
//
// Time-complexity invariant: O(unique-tx-in-byPathHop + total-key-bucket
// entries). Snapshot memory: one pointer per bucket entry, plus a map entry
// per key; both are bounded by the same eviction policy as byPathHop itself.
func (s *PacketStore) computeRepeaterRelayInfoMap(windowHours float64) map[string]RepeaterRelayInfo {
	s.mu.RLock()

	// Own the bucket arrays before releasing the lock for aggregation.
	// The result represents this snapshot even if eviction removes its txs
	// while aggregation runs; the next recomputer pass picks up that change.
	snap := s.snapshotPathHopIndexLocked()

	// Build a tx-id-keyed pre-parsed cache so the inner loop doesn't
	// re-parse the same FirstSeen N times when the same tx is indexed
	// under multiple hop keys (very common — every hop on a path indexes
	// the tx).
	type parsedTx struct {
		t  time.Time
		ok bool
		pt int
	}
	parseCache := make(map[int]parsedTx, 1<<14)
	for _, list := range snap {
		for _, tx := range list {
			if tx == nil {
				continue
			}
			if _, ok := parseCache[tx.ID]; ok {
				continue
			}
			pt := -1
			if tx.PayloadType != nil {
				pt = *tx.PayloadType
			}
			t, ok := parseRelayTS(tx.FirstSeen)
			parseCache[tx.ID] = parsedTx{t: t, ok: ok, pt: pt}
		}
	}
	s.mu.RUnlock()

	now := time.Now().UTC()
	cutoff1h := now.Add(-1 * time.Hour)
	cutoff24h := now.Add(-24 * time.Hour)
	var windowCutoff time.Time
	if windowHours > 0 {
		windowCutoff = now.Add(-time.Duration(windowHours * float64(time.Hour)))
	}

	out := make(map[string]RepeaterRelayInfo, len(snap))
	for key, list := range snap {
		info := RepeaterRelayInfo{WindowHours: windowHours}
		// #1751: accumulate the set of region scope names carried by this
		// hop key across every non-advert path-hop tx (NOT time-windowed).
		// Captured by the visit closure below — lazily allocated on the first
		// scope hit so hosts without scope_name pay nothing per key; converted
		// to a sorted, capped slice before this key's info is stored.
		var scopeSet map[string]struct{}
		// When key looks like a full pubkey (>= 2 hex chars), also fold
		// in the matching 1-byte raw-prefix bucket to mirror
		// GetRepeaterRelayInfo's behavior. We dedup by tx ID.
		var seen map[int]bool
		if len(key) >= 2 {
			prefix := key[:2]
			if prefix != key {
				if extra := snap[prefix]; len(extra) > 0 {
					seen = make(map[int]bool, len(list)+len(extra))
				}
			}
		}
		// viaPrefix marks the 1-byte wire-prefix bucket, shared by every
		// node with the same first pubkey byte. See collectRelayEntriesLocked
		// for the counters-vs-scopes split (#662 / #1902).
		visit := func(txs []*StoreTx, viaPrefix bool) {
			for _, tx := range txs {
				if tx == nil {
					continue
				}
				if seen != nil {
					if seen[tx.ID] {
						continue
					}
					seen[tx.ID] = true
				}
				p, ok := parseCache[tx.ID]
				if !ok {
					continue
				}
				if p.pt == payloadTypeAdvert {
					continue
				}
				// #1751: scope accumulation is intentionally NOT gated on
				// p.ok (timestamp parseability) — a packet with an
				// unparseable first_seen still proves the repeater
				// transported that scope. RelayCount/LastRelayed below
				// remain timestamp-gated.
				//
				// #1902: it IS gated on full-pubkey attribution — a 1-byte
				// hop cannot prove which of the nodes sharing that byte
				// carried the packet.
				if tx.ScopeName != nil && *tx.ScopeName != "" && !viaPrefix {
					if scopeSet == nil {
						scopeSet = map[string]struct{}{}
					}
					scopeSet[*tx.ScopeName] = struct{}{}
				}
				if !p.ok {
					continue
				}
				if p.t.After(cutoff24h) {
					info.RelayCount24h++
					if tx.RouteType != nil && *tx.RouteType == routeTypeFlood {
						info.UnscopedRelayCount24h++
					}
					if p.t.After(cutoff1h) {
						info.RelayCount1h++
					}
				}
				if info.LastRelayed == "" || tx.FirstSeen > info.LastRelayed {
					info.LastRelayed = tx.FirstSeen
					if windowHours > 0 && p.t.After(windowCutoff) {
						info.RelayActive = true
					} else if windowHours > 0 {
						info.RelayActive = false
					}
				}
			}
		}
		visit(list, false)
		if seen != nil {
			prefix := key[:2]
			if prefix != key {
				visit(snap[prefix], true)
			}
		}
		info.TransportedScopes = sortedCappedScopes(scopeSet)
		out[key] = info
	}
	return out
}

// GetRepeaterUsefulnessScoreMap returns a cached pubkey → 0..1 score
// for every pubkey appearing in byPathHop. Bulk equivalent of
// GetRepeaterUsefulnessScore. See GetRepeaterRelayInfoMap for the
// motivation (#1257) and the no-inline-rebuild rationale (#1272).
func (s *PacketStore) GetRepeaterUsefulnessScoreMap() map[string]float64 {
	s.repeaterEnrichMu.Lock()
	cached := s.repeaterUsefulCache
	s.repeaterEnrichMu.Unlock()
	if cached != nil {
		return cached
	}

	result := s.computeRepeaterUsefulnessScoreMap()

	s.repeaterEnrichMu.Lock()
	if s.repeaterUsefulCache == nil {
		s.repeaterUsefulCache = result
		s.repeaterUsefulAt = time.Now()
	}
	cached = s.repeaterUsefulCache
	s.repeaterEnrichMu.Unlock()
	return cached
}

func (s *PacketStore) computeRepeaterUsefulnessScoreMap() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	totalNonAdvert := 0
	for pt, list := range s.byPayloadType {
		if pt == payloadTypeAdvert {
			continue
		}
		totalNonAdvert += len(list)
	}
	out := make(map[string]float64, len(s.byPathHop))
	if totalNonAdvert == 0 {
		return out
	}
	denom := float64(totalNonAdvert)
	for key, list := range s.byPathHop {
		relayed := 0
		for _, tx := range list {
			if tx == nil {
				continue
			}
			if tx.PayloadType != nil && *tx.PayloadType == payloadTypeAdvert {
				continue
			}
			relayed++
		}
		if relayed == 0 {
			continue
		}
		score := float64(relayed) / denom
		if score < 0 {
			score = 0
		} else if score > 1 {
			score = 1
		}
		out[key] = score
	}
	return out
}

// lookupRelayInfo is a small helper to make handleNodes' map lookup
// case-insensitive (byPathHop keys are lowercase; pubkeys arriving from
// the DB row may be either case).
func lookupRelayInfo(m map[string]RepeaterRelayInfo, pubkey string) (RepeaterRelayInfo, bool) {
	if v, ok := m[pubkey]; ok {
		return v, true
	}
	if lc := strings.ToLower(pubkey); lc != pubkey {
		if v, ok := m[lc]; ok {
			return v, true
		}
	}
	return RepeaterRelayInfo{}, false
}

// lookupUsefulnessScore mirrors lookupRelayInfo for the score map.
func lookupUsefulnessScore(m map[string]float64, pubkey string) float64 {
	if v, ok := m[pubkey]; ok {
		return v
	}
	if lc := strings.ToLower(pubkey); lc != pubkey {
		if v, ok := m[lc]; ok {
			return v
		}
	}
	return 0
}
