package main

import (
	"sort"
	"strings"
	"time"
)

// RepeaterRelayInfo describes whether a repeater has been observed
// relaying traffic (appearing as a path hop in non-advert packets) and
// when. This is distinct from advert-based liveness (last_seen / last_heard),
// which only proves the repeater can transmit its own adverts.
//
// See issue #662.
type RepeaterRelayInfo struct {
	// LastRelayed is the ISO-8601 timestamp of the most recent non-advert
	// packet where this pubkey appeared as a relay hop. Empty if never.
	LastRelayed string `json:"lastRelayed,omitempty"`
	// RelayActive is true if LastRelayed falls within the configured
	// activity window (default 24h).
	RelayActive bool `json:"relayActive"`
	// WindowHours is the active-window threshold actually used.
	WindowHours float64 `json:"windowHours"`
	// RelayCount1h is the count of distinct non-advert packets where this
	// pubkey appeared as a relay hop in the last 1 hour.
	RelayCount1h int `json:"relayCount1h"`
	// RelayCount24h is the count of distinct non-advert packets where this
	// pubkey appeared as a relay hop in the last 24 hours.
	RelayCount24h int `json:"relayCount24h"`
	// UnscopedRelayCount24h is the subset of RelayCount24h that were UNSCOPED
	// floods (route_type == ROUTE_TYPE_FLOOD). A well-configured repeater runs
	// `flood.max.unscoped 0` and should not forward these, so a non-trivial
	// count flags a base-config problem (consumed by the ArcScope advisor).
	UnscopedRelayCount24h int `json:"unscopedRelayCount24h"`
	// TransportedScopes is the deduplicated, sorted set of region scope
	// names (transmissions.scope_name) across ALL non-advert packets in
	// which this pubkey appears as a path hop. Unlike RelayCount1h/24h this
	// is NOT time-windowed — it answers "which region scopes has this
	// repeater carried traffic for, ever (within the in-memory window)".
	// Empty/absent on schemas without scope_name (#1751).
	TransportedScopes []string `json:"transportedScopes,omitempty"`
}

// maxTransportedScopes bounds the per-node TransportedScopes list so a
// misbehaving sender flooding distinct scope_name values through a single
// repeater cannot inflate the node JSON unboundedly (#1751 review follow-up).
// Real region-scope counts are small; this is a defensive ceiling. When the
// set exceeds the cap the lexicographically-first names are kept, so the
// result stays deterministic.
const maxTransportedScopes = 32

// sortedCappedScopes converts a scope set into a sorted, length-capped slice,
// or nil when the set is empty/nil — so routes.go omits the JSON field via
// `omitempty`. Shared by the bulk (computeRepeaterRelayInfoMap) and per-node
// (computeRelayInfoFromEntries) paths to keep them in exact parity.
func sortedCappedScopes(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	scopes := make([]string, 0, len(set))
	for s := range set {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	if len(scopes) > maxTransportedScopes {
		scopes = scopes[:maxTransportedScopes]
	}
	return scopes
}

// payloadTypeAdvert is the MeshCore payload type for ADVERT packets.
// See firmware/src/Mesh.h. Adverts are NOT considered relay activity:
// a repeater that only sends adverts proves it is alive, not that it
// is forwarding traffic for other nodes.
const payloadTypeAdvert = 4

// routeTypeFlood is ROUTE_TYPE_FLOOD from the MeshCore packet header (the low 2
// bits of the header byte). Equal to packetpath.RouteFlood; kept as a local
// literal to avoid importing packetpath here. An "unscoped flood" is a
// route-type-FLOOD packet — the traffic `flood.max.unscoped` governs.
const routeTypeFlood = 1

// parseRelayTS attempts to parse a packet first-seen timestamp using the
// formats CoreScope writes in practice. Returns zero time and false on
// failure. Accepted (in order):
//   - RFC3339Nano  — Go's default UTC marshal output
//   - RFC3339      — second-precision ISO-8601 with offset
//   - "2006-01-02T15:04:05.000Z" — millisecond-precision Z form used by ingest
func parseRelayTS(ts string) (time.Time, bool) {
	if ts == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", ts); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// relayEntry is a minimal snapshot of a StoreTx taken while the store
// read-lock is held. Copying only the fields we need lets us release the
// lock before doing timestamp parsing and comparison work.
type relayEntry struct {
	ts string
	pt int
	// rt is the tx route type (transmissions.route_type), or -1 when absent.
	// rt == routeTypeFlood marks an unscoped flood (UnscopedRelayCount24h).
	rt int
	// scope is the tx's region scope name (transmissions.scope_name).
	// Empty when absent / on older schemas. Used for TransportedScopes (#1751).
	scope string
	// viaPrefix marks an entry that was only reachable through the 1-byte
	// wire-prefix bucket, i.e. some node whose pubkey starts with the same
	// byte carried it — not necessarily this one. Counters accept that
	// ambiguity (#662); scope attribution does not (#1902).
	viaPrefix bool
}

// collectRelayEntriesLocked returns deduplicated relayEntry snapshots for
// all StoreTx entries indexed under key (full pubkey) and its 1-byte wire
// prefix. Caller MUST hold s.mu at least for reading.
//
// byPathHop is keyed by both full resolved pubkey AND raw 1-byte hop
// prefix (e.g. "a3"). Many ingested non-advert packets only carry the
// raw hop on the wire — resolution to the full pubkey happens later via
// neighbor affinity. Looking up both keys and de-duping by tx ID matches
// what the "Paths seen through node" view shows.
//
// The 1-byte prefix lookup CAN over-count when multiple nodes share the
// same first byte. This trades a possible over-count for clearly false
// zeros (issue #662). Entries reached only that way are flagged viaPrefix
// so TransportedScopes can refuse them (#1902) while the counters keep the
// trade-off.
func (s *PacketStore) collectRelayEntriesLocked(key string) []relayEntry {
	txList := s.byPathHop[key]
	var prefixList []*StoreTx
	if len(key) >= 2 {
		// key[:2] is the first 2 hex characters — exactly 1 byte of raw
		// hop data, matching addTxToPathHopIndex for wire-level hops.
		prefix := key[:2]
		if prefix != key {
			prefixList = s.byPathHop[prefix]
		}
	}

	// Capacity hint: upper-bound is len(txList)+len(prefixList). The
	// collect() pass below uses `seen` for true dedup, so we don't need
	// a separate prepass (PR #1164 CR item 3: dead `uniq` map removed).
	hint := len(txList) + len(prefixList)
	entries := make([]relayEntry, 0, hint)
	seen := make(map[int]bool, hint)
	collect := func(list []*StoreTx, viaPrefix bool) {
		for _, tx := range list {
			if tx == nil || seen[tx.ID] {
				continue
			}
			seen[tx.ID] = true
			pt := -1
			if tx.PayloadType != nil {
				pt = *tx.PayloadType
			}
			rt := -1
			if tx.RouteType != nil {
				rt = *tx.RouteType
			}
			scope := ""
			if tx.ScopeName != nil {
				scope = *tx.ScopeName
			}
			entries = append(entries, relayEntry{ts: tx.FirstSeen, pt: pt, rt: rt, scope: scope, viaPrefix: viaPrefix})
		}
	}
	collect(txList, false)
	collect(prefixList, true)
	return entries
}

// computeRelayInfoFromEntries derives RepeaterRelayInfo from pre-snapshotted
// relayEntry values. Safe to call without any lock held.
func computeRelayInfoFromEntries(entries []relayEntry, windowHours float64) RepeaterRelayInfo {
	info := RepeaterRelayInfo{WindowHours: windowHours}

	now := time.Now().UTC()
	cutoff1h := now.Add(-time.Hour)
	cutoff24h := now.Add(-24 * time.Hour)

	var latest time.Time
	var latestRaw string
	var scopeSet map[string]struct{}
	for _, e := range entries {
		// Self-originated adverts are not relay activity.
		if e.pt == payloadTypeAdvert {
			continue
		}
		// #1751: accumulate transported scopes BEFORE the timestamp gate —
		// a non-advert path-hop tx proves scope transport even if its
		// first_seen is unparseable. Mirrors the bulk path.
		//
		// #1902: but only when the tx named this node by its full pubkey.
		// A 1-byte hop names one of every node sharing that byte, which is
		// enough for an approximate count and not enough to claim the node
		// serves a region.
		if e.scope != "" && !e.viaPrefix {
			if scopeSet == nil {
				scopeSet = map[string]struct{}{}
			}
			scopeSet[e.scope] = struct{}{}
		}
		t, ok := parseRelayTS(e.ts)
		if !ok {
			continue
		}
		if t.After(latest) {
			latest = t
			latestRaw = e.ts
		}
		if t.After(cutoff24h) {
			info.RelayCount24h++
			if e.rt == routeTypeFlood {
				info.UnscopedRelayCount24h++
			}
			if t.After(cutoff1h) {
				info.RelayCount1h++
			}
		}
	}
	// #1751: emit transported scopes regardless of whether any timestamp
	// parsed, and before the latestRaw early-return below.
	info.TransportedScopes = sortedCappedScopes(scopeSet)
	if latestRaw == "" {
		return info
	}
	info.LastRelayed = latestRaw

	if windowHours > 0 {
		cutoff := now.Add(-time.Duration(windowHours * float64(time.Hour)))
		if latest.After(cutoff) {
			info.RelayActive = true
		}
	}
	return info
}

// GetRepeaterRelayInfo returns relay-activity information for a node by
// scanning the byPathHop index for non-advert packets that name the
// pubkey as a hop. It computes the most recent appearance timestamp,
// 1h/24h hop counts, and whether the latest appearance falls within
// windowHours.
//
// Cost: O(N) over the indexed entries for `pubkey`. The byPathHop index
// is bounded by store eviction; on real data this is small per-node.
//
// Note on self-as-source: byPathHop is keyed by every hop in a packet's
// resolved path, including the originator. For ADVERT packets that's the
// node itself, which is filtered above by the payloadTypeAdvert check.
// For non-advert packets a node "originates" rather than "relays" only
// when it is the source; we don't currently have a clean signal for that
// distinction, so the count here is *path-hop appearances in non-advert
// packets*. In practice for a repeater nearly all such appearances are
// relay hops (the firmware doesn't originate user traffic), so this is
// the right approximation for issue #662.
func (s *PacketStore) GetRepeaterRelayInfo(pubkey string, windowHours float64) RepeaterRelayInfo {
	if pubkey == "" {
		return RepeaterRelayInfo{WindowHours: windowHours}
	}
	key := strings.ToLower(pubkey)

	s.mu.RLock()
	entries := s.collectRelayEntriesLocked(key)
	s.mu.RUnlock()

	return computeRelayInfoFromEntries(entries, windowHours)
}
