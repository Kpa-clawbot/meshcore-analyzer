package main

import (
	"sort"
	"strings"
	"time"
)

// GetRepeaterUsefulnessScore returns a 0..1 score representing what
// fraction of non-advert traffic in the store passes through this
// repeater as a relay hop. Issue #672 (Traffic axis only — bridge,
// coverage, and redundancy axes are deferred to follow-up work).
//
// Numerator:   count of non-advert StoreTx entries indexed under
//
//	pubkey in byPathHop.
//
// Denominator: total non-advert StoreTx entries in the store
//
//	(sum of byPayloadType for all keys != payloadTypeAdvert).
//
// Returns 0 when there is no non-advert traffic, the pubkey is empty,
// or the repeater never appears as a relay hop. Scores are clamped to
// [0,1] for defensive bounds.
//
// Cost: O(N) over byPayloadType keys (typically <20) plus the per-hop
// slice for pubkey. Cheap relative to the per-request enrichment loop
// in handleNodes; if it ever shows up in profiles, denominator can be
// memoized off store invalidation.
func (s *PacketStore) GetRepeaterUsefulnessScore(pubkey string) float64 {
	if pubkey == "" {
		return 0
	}
	key := strings.ToLower(pubkey)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Denominator: total non-advert packets.
	totalNonAdvert := 0
	for pt, list := range s.byPayloadType {
		if pt == payloadTypeAdvert {
			continue
		}
		totalNonAdvert += len(list)
	}
	if totalNonAdvert == 0 {
		return 0
	}

	// Numerator: this repeater's non-advert hop appearances.
	relayed := 0
	for _, tx := range s.byPathHop[key] {
		if tx == nil {
			continue
		}
		if tx.PayloadType != nil && *tx.PayloadType == payloadTypeAdvert {
			continue
		}
		relayed++
	}

	score := float64(relayed) / float64(totalNonAdvert)
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// RepeaterNodeStats bundles relay-activity and usefulness data for a single node.
type RepeaterNodeStats struct {
	Info  RepeaterRelayInfo
	Score float64
}

// GetRepeaterNodeStatsBatch computes relay info and usefulness scores for all given
// pubkeys in a single read-lock pass, sharing the non-advert denominator across all
// nodes. All StoreTx fields are read under the lock and copied into relayEntry
// snapshots before the lock is released; no StoreTx pointers escape the lock.
// Replaces the per-node loop in handleNodes that called GetRepeaterRelayInfo +
// GetRepeaterUsefulnessScore N times (O(N × byPayloadType) → O(byPayloadType + N)).
func (s *PacketStore) GetRepeaterNodeStatsBatch(pubkeys []string, windowHours float64) map[string]RepeaterNodeStats {
	result := make(map[string]RepeaterNodeStats, len(pubkeys))
	if len(pubkeys) == 0 {
		return result
	}

	type nodeSnap struct {
		entries []relayEntry
		relayed int // non-advert count in full-key list only (for usefulness score)
	}

	s.mu.RLock()

	totalNonAdvert := 0
	for pt, list := range s.byPayloadType {
		if pt != payloadTypeAdvert {
			totalNonAdvert += len(list)
		}
	}

	snaps := make(map[string]nodeSnap, len(pubkeys))
	for _, pk := range pubkeys {
		key := strings.ToLower(pk)
		entries := s.collectRelayEntriesLocked(key)
		relayed := 0
		for _, tx := range s.byPathHop[key] {
			if tx != nil && (tx.PayloadType == nil || *tx.PayloadType != payloadTypeAdvert) {
				relayed++
			}
		}
		snaps[pk] = nodeSnap{entries: entries, relayed: relayed}
	}

	s.mu.RUnlock()

	for _, pk := range pubkeys {
		snap := snaps[pk]
		info := computeRelayInfoFromEntries(snap.entries, windowHours)

		var score float64
		if totalNonAdvert > 0 && snap.relayed > 0 {
			score = float64(snap.relayed) / float64(totalNonAdvert)
			if score > 1 {
				score = 1
			}
		}

		result[pk] = RepeaterNodeStats{Info: info, Score: score}
	}

	return result
}

// GetRepeaterNodeStatsBatchCached wraps GetRepeaterNodeStatsBatch with a 5min
// TTL cache keyed on (pubkeys, windowHours). handleNodes calls this for every
// map/live/node request; without caching the full batch over ~1900 repeaters
// takes 20-30s on large datasets.
// 300s TTL: cold compute (~25s) runs at most once per 5min (~8% duty cycle)
// vs the previous 30s TTL (~82% duty cycle).
func (s *PacketStore) GetRepeaterNodeStatsBatchCached(pubkeys []string, windowHours float64) map[string]RepeaterNodeStats {
	sig := pubkeySig(pubkeys)

	s.relayStatsCacheMu.Lock()
	if s.relayStatsCache != nil &&
		s.relayStatsCacheSig == sig &&
		s.relayStatsCacheWindow == windowHours &&
		time.Since(s.relayStatsCacheAt) < 300*time.Second {
		cached := s.relayStatsCache
		s.relayStatsCacheMu.Unlock()
		return cached
	}
	s.relayStatsCacheMu.Unlock()

	result := s.GetRepeaterNodeStatsBatch(pubkeys, windowHours)

	s.relayStatsCacheMu.Lock()
	s.relayStatsCache = result
	s.relayStatsCacheAt = time.Now()
	s.relayStatsCacheWindow = windowHours
	s.relayStatsCacheSig = sig
	s.relayStatsCacheMu.Unlock()

	return result
}

// pubkeySig returns a stable, order-independent string key for a pubkey set.
func pubkeySig(pubkeys []string) string {
	if len(pubkeys) == 0 {
		return ""
	}
	sorted := make([]string, len(pubkeys))
	copy(sorted, pubkeys)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}
