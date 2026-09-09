package main

// Issue #1975: the network-wide Scope Audit, GET /api/scope-audit.
//
// One row per repeater whose configured region list is known, answering a
// question no other view answers: you declare these regions, but were you
// actually seen forwarding them? default_scope says what a node's adverts were
// observed under and transported_scopes (#1751) says what it carried, but
// nothing lines the declared list up against observed forwarding, so a
// repeater configured for eight regions that only ever forwards one looks
// healthy everywhere else.
//
// Declared side: nodes.configured_scope, written by the observer /neighbors
// ingest from #1865/#1971. Observed side: forwarder hops in the window,
// aggregated per target. Both sides are compared through normScope, so the
// leading "#" that configured_scope carries and a bare region name are the
// same region.
//
// Ported from a long-running fork deployment. The measurements quoted in
// #1975 come from a 1179-repeater instance.

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// ScopeObservation is one region scope this repeater has been observed
// forwarding traffic for, within the query window.
type ScopeObservation struct {
	Scope     string `json:"scope"` // matched region name (transmissions.scope_name)
	Packets   int64  `json:"packets"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

// RouteTypeMix is the route-type breakdown of packets this node was
// observed FORWARDING — i.e. packets on which this pubkey was the last hop
// of a FLOOD-family route (RouteTransportFlood, RouteFlood). It does NOT
// mean "packets in which this node appears anywhere in the path": a DIRECT
// or TRANSPORT_DIRECT packet's last path hop is the route's far end, never
// the transmitter, so crediting it here would attribute forwarding this node
// never did. Direct and TransportDirect are therefore always zero by
// construction — the forwarder join can never match those route types.

// scopeConformanceForwarderRouteTypesSQL restricts the forwarder join to the
// only route types whose path[last] is the packet's actual transmitter:
// RouteTransportFlood (0) and RouteFlood (1). A DIRECT route consumes hops
// from the front, so its path[last] is the route's far end rather than the
// forwarder — including RouteDirect (2) / RouteTransportDirect (3) here
// would misattribute a scope to a node that never forwarded the packet.
const scopeConformanceForwarderRouteTypesSQL = "t.route_type IN (0, 1)"

// minForwarderHopHexLen is the shortest path_json hop ScopeConformance will
// treat as an attribution: 4 hex chars = 2 bytes. Matches deriveHeardKey's
// floor (cmd/ingestor/client_reception.go: "exclude 1-byte (collision-prone),
// matching Reach") — a 1-byte/2-hex-char hash collides too often across a
// real fleet, and attributing a scope to the wrong node is worse than
// attributing none.
const minForwarderHopHexLen = 4

type DeclaredRegions struct {
	Regions    []string `json:"regions"`
	ObservedAt string   `json:"observedAt"`
	Truncated  bool     `json:"truncated"`
}

// splitRegionsCSV parses regions_csv (comma-separated, "#" prefixes already
// stripped by the firmware) into a slice, always non-nil.
func splitRegionsCSV(csv string) []string {
	regions := []string{}
	if csv == "" {
		return regions
	}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			regions = append(regions, part)
		}
	}
	return regions
}

// CurrentDeclaredRegions returns pubkey's most recently declared region
// list, or nil (not an error) when the repeater has never successfully
// answered, or when node_declared_regions is absent (an older database that
// predates this table).
//
// "Most recent" is ordered by the greatest observed_at, NEVER ingested_at —
// mirrors the ingestor's own CurrentDeclaredRegions exactly: a drive
// buffered offline can arrive days late, and ordering by arrival would let

type DeclaredRegionsRow struct {
	Target     string
	ObservedAt string
	RegionsCSV string
	Truncated  bool
}

// AllCurrentDeclaredRegions returns the newest confirmed region list per node,
// merged across every source this database happens to carry.
//
// A confirmed list is a repeater's own answer about which regions it is
// configured to forward, read back off the node rather than inferred from
// traffic. Upstream that answer arrives one way, via the observer /neighbors
// report which #1865/#1971 writes to nodes.configured_scope. It is not the
// only possible collector: neighbor reports have also landed in
// meshcore-packet-capture and openHop, and some deployments gather the same
// answer over a companion app. So the lookup merges rather than hard-coding a
// single table, and an instance with only the stock source is simply the
// one-source case.
//
// Newest answer wins, compared on the recorded instant. Both sources store
// that in canonical UTC RFC3339, so a lexicographic compare is chronological;
// a row with no instant loses to any row that has one, and only wins against
// nothing at all.
//
// A node that has never answered is absent, never synthesized as declaring
// nothing: "no answer" and "answered with nothing" are different states and
// the audit distinguishes them.
func (db *DB) AllCurrentDeclaredRegions() ([]DeclaredRegionsRow, error) {
	merged := map[string]DeclaredRegionsRow{}

	keep := func(r DeclaredRegionsRow) {
		if r.Target == "" {
			return
		}
		if cur, ok := merged[r.Target]; ok && cur.ObservedAt >= r.ObservedAt {
			return
		}
		merged[r.Target] = r
	}

	if db.hasConfiguredScope {
		rows, err := db.conn.Query(`
			SELECT public_key, COALESCE(configured_scope_at, ''), configured_scope
			FROM nodes
			WHERE configured_scope IS NOT NULL
		`)
		if err != nil {
			return nil, fmt.Errorf("declared regions from configured_scope: %w", err)
		}
		for rows.Next() {
			var d DeclaredRegionsRow
			if err := rows.Scan(&d.Target, &d.ObservedAt, &d.RegionsCSV); err != nil {
				rows.Close()
				return nil, fmt.Errorf("declared regions from configured_scope scan: %w", err)
			}
			// Truncated has no equivalent on this source: the /neighbors report
			// has a size cap but does not say whether it fired, and claiming
			// false would invent a fact. The page omits the caveat instead.
			keep(d)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("declared regions from configured_scope rows: %w", err)
		}
		rows.Close()
	}

	if db.hasDeclaredRegionsTable {
		rows, err := db.conn.Query(`
			WITH ranked AS (
				SELECT target, observed_at, regions_csv, truncated,
					ROW_NUMBER() OVER (PARTITION BY target ORDER BY observed_at DESC) AS rn
				FROM node_declared_regions
			)
			SELECT target, observed_at, regions_csv, truncated
			FROM ranked
			WHERE rn = 1
		`)
		if err != nil {
			return nil, fmt.Errorf("declared regions from node_declared_regions: %w", err)
		}
		for rows.Next() {
			var d DeclaredRegionsRow
			var truncated int
			if err := rows.Scan(&d.Target, &d.ObservedAt, &d.RegionsCSV, &truncated); err != nil {
				rows.Close()
				return nil, fmt.Errorf("declared regions from node_declared_regions scan: %w", err)
			}
			d.Truncated = truncated == 1
			keep(d)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("declared regions from node_declared_regions rows: %w", err)
		}
		rows.Close()
	}

	if len(merged) == 0 {
		return nil, nil
	}
	out := make([]DeclaredRegionsRow, 0, len(merged))
	for _, r := range merged {
		out = append(out, r)
	}
	// Stable output: the handler ranks rows itself, but a deterministic order
	// keeps responses comparable between calls on unchanged data.
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out, nil
}

// scopeAuditNodeIdentity is the name/role display identity for one
// scope-audit row, resolved in bulk (one IN query for every declared
// target) rather than one GetNodeByPubkey call per repeater.

type scopeAuditNodeIdentity struct {
	Name *string
	Role *string
}

// scopeAuditNodeIdentities resolves name/role for pubkeys in a single query.
// A pubkey with no matching nodes row (deleted, pruned) resolves to the zero
// value rather than being an error — GET /api/scope-audit still has a
// PublicKey to identify and link the row.
func (db *DB) scopeAuditNodeIdentities(pubkeys []string) map[string]scopeAuditNodeIdentity {
	result := make(map[string]scopeAuditNodeIdentity, len(pubkeys))
	if len(pubkeys) == 0 {
		return result
	}
	placeholders := make([]string, len(pubkeys))
	args := make([]interface{}, len(pubkeys))
	for i, k := range pubkeys {
		placeholders[i] = "?"
		args[i] = strings.ToLower(k)
	}
	rows, err := db.conn.Query(
		"SELECT public_key, name, role FROM nodes WHERE public_key IN ("+strings.Join(placeholders, ",")+")",
		args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		var name, role sql.NullString
		if rows.Scan(&pk, &name, &role) != nil {
			continue
		}
		id := scopeAuditNodeIdentity{}
		if name.Valid {
			v := name.String
			id.Name = &v
		}
		if role.Valid {
			v := role.String
			id.Role = &v
		}
		result[strings.ToLower(pk)] = id
	}
	return result
}

// normScope strips a leading '#' so the two sides of the declared/observed
// comparison can be matched at all — mirrors public/node-scopes.js's
// normScope exactly. transmissions.scope_name keeps the '#' (region keys
// are configured as hashRegions: ["#belgium", "#eu"]); regions_csv arrives
// from the firmware with the prefix already stripped. Comparing raw
// silently inverts the whole comparison while looking entirely plausible.

func normScope(s string) string {
	if strings.HasPrefix(s, "#") {
		return s[1:]
	}
	return s
}

// scopeAuditTargetAgg is one declared target's forwarding aggregate for the
// GET /api/scope-audit scan window: named scopes it forwarded (key =
// normScope'd name) plus a count of the plain-FLOOD (unscoped) packets it
// forwarded — the signal the wildcard-contradiction check needs, since '*'
// governs exactly those packets, not any named scope.

type scopeAuditTargetAgg struct {
	scopes          map[string]*ScopeObservation
	unscopedPackets int64
	// unmatchedPackets counts packets this target was observed forwarding
	// that carried a transport scope no configured region key matched
	// (transmissions.scope_name = the empty string). Deliberately NOT folded
	// into unscopedPackets: those two are opposites. Unscoped means the packet
	// carried no scope at all (scope_name SQL NULL) and is what '*' governs;
	// unmatched means it IS scoped and this instance simply holds no key for
	// that region, so '*' says nothing about it. See scopeNameForDB in the
	// ingestor for the encoding.
	//
	// A non-zero value is a caveat on this target's notObserved entries: any
	// of them may be a region this instance cannot name rather than one the
	// repeater is not forwarding.
	unmatchedPackets int64

	// unmatchedTxIDs are the transmissions behind unmatchedPackets, kept so
	// declared-region verification can test this target's own declarations
	// against this target's own unnameable traffic (scope_verify.go). The same
	// (target, txID) de-duplication that guards unmatchedPackets guards this,
	// so one packet reaching a target by two hops cannot corroborate twice.
	//
	// Bounded by scopeVerifyMaxPacketsPerTarget, which unmatchedPackets is NOT:
	// the count stays the honest total, this is the working set.
	unmatchedTxIDs []int64

	// ambiguousHops counts forwarder-hop observations in the window whose
	// truncated hash prefix matched this target AND at least one other
	// declared target — see ScopeAuditForwarding's doc comment for why
	// those hops are attributed to neither candidate instead of both.
	ambiguousHops int64
}

// scopeAuditPrefixIndex builds, for every even hex length from
// minForwarderHopHexLen up to a full 64-char pubkey, a lowercase prefix ->
// []target map. A forwarder hop of any valid truncated-hash length then
// resolves to its matching declared target(s) via a single map lookup,
// instead of a per-target SQL join — this is what keeps
// ScopeAuditForwarding's underlying scan a single query independent of how
// many repeaters have declared a region list.
func scopeAuditPrefixIndex(targets []string) map[int]map[string][]string {
	byLen := make(map[int]map[string][]string)
	for _, t := range targets {
		t = strings.ToLower(t)
		for l := minForwarderHopHexLen; l <= len(t); l += 2 {
			m, ok := byLen[l]
			if !ok {
				m = map[string][]string{}
				byLen[l] = m
			}
			prefix := t[:l]
			m[prefix] = append(m[prefix], t)
		}
	}
	return byLen
}

// scopeAuditForwarderScanQuery is the single full-window scan behind
// GET /api/scope-audit. Unlike scopeConformanceQuery (one EXISTS-correlated
// call per pubkey — fine for one node, but 37+ repeater-sized loop of them
// would each re-scan the same first_seen index range), this scans the
// FLOOD-family window exactly once and returns every forwarder hop found.
//
// "Every forwarder hop" means every hop of the path, not only path[last] —
// see scopeConformanceQuery's doc comment for why, and note that this query
// returns one ROW PER HOP, so a single transmission now yields as many rows as
// it has hops (mean 7.08 on the live network). ScopeAuditForwarding's
// "<target>|<txID>" de-duplication is what keeps that from counting a
// transmission twice for one target, and it is now load-bearing rather than
// belt-and-braces: one path can carry the same target on several hops.
// It applies the SAME three conditions scopeConformanceQuery does —
// minForwarderHopHexLen, scopeConformanceForwarderRouteTypesSQL, and the
// explicit json_valid guard against a single malformed path_json row
// erroring the whole query — but does not join against any target list:
// attribution to a specific declared target happens in Go
// (ScopeAuditForwarding), against the small in-memory prefix index built by
// scopeAuditPrefixIndex, so the SQL cost stays O(rows in window) regardless
// of len(targets).
var scopeAuditForwarderScanQuery = `
	SELECT t.id, je.value
	FROM transmissions t
	JOIN observations o ON o.transmission_id = t.id
	JOIN json_each(o.path_json) je
	WHERE t.first_seen >= ?
	  AND ` + scopeConformanceForwarderRouteTypesSQL + `
	  AND o.path_json IS NOT NULL
	  AND json_valid(o.path_json)
	  AND json_array_length(o.path_json) > 0
	  AND LENGTH(je.value) >= ` + fmt.Sprint(minForwarderHopHexLen) + `
`

// scopeAuditWindowMetaQuery reads the two per-TRANSMISSION facts the hop scan
// used to carry on every hop row: the scope name and the timestamp. It applies
// the identical window and route-type filter, so it covers every transmission
// the hop scan can produce, and both run inside one read transaction so the
// two see the same snapshot.
//
// Splitting these out is why the hop scan carries two columns instead of four.
// Measured on the live-shaped staging database on 2026-09-07, a 7d window
// yields 3,470,188 hop rows against 79,652 transmissions: 43 hop rows per
// transmission, each of which was re-reading the same scope_name and
// first_seen. SQLite spends 2.7s of the 16.7s that window cost; the rest was
// the Go side scanning columns it already knew.
//
// Every SQL-side attempt to shrink the hop scan itself measured worse on that
// same database and was rejected: a first-4-hex prefix filter against the
// declared targets takes 20.9s (and needs lower() on both sides, because 80%
// of stored hops are uppercase), GROUP BY t.id, hop takes 38.0s, and
// SELECT DISTINCT t.id, path_json takes 17.7s. The 3.47M rows are inherent:
// 1,368,761 observations carrying a path, ~2.5 usable hops each.

// ScopeAuditForwarding runs scopeAuditForwarderScanQuery once for the whole
// window and attributes every forwarder hop it finds to targets, by the same
// truncated-hash prefix match ScopeConformance uses for a single pubkey.
//
// A hop is attributed only when its prefix matches EXACTLY ONE declared
// target. This endpoint exists to find a repeater that declares a region and
// is not actually forwarding it — crediting a hop to every target sharing
// its prefix would let a colliding neighbour's traffic silently paper over a
// real gap, and crediting nobody (the alternative of dropping the hop
// entirely) would invent failures for targets that simply share a collision-
// prone prefix. Instead, an ambiguous hop is credited to NEITHER candidate,
// and every candidate's ambiguousHops counter is incremented instead, so the
// row can say "this notObserved might just be a prefix collision" rather
// than presenting it as a confirmed finding. See scopeAuditTargetAgg's
// ambiguousHops field and ScopeAuditRow.AmbiguousHops.
//
// Each (target, transmission) pair is counted at most once even if seen via
// multiple observations, for both the attributed and the ambiguous count —
// the same de-duplication scopeConformanceQuery gets for free from EXISTS,
// done explicitly here since this scan is not correlated per target.

// scopeAuditWindowMetaQuery reads the two per-TRANSMISSION facts the hop scan
// used to carry on every hop row: the scope name and the timestamp. It applies
// the identical window and route-type filter, so it covers every transmission
// the hop scan can produce, and both run inside one read transaction so the
// two see the same snapshot.
//
// Splitting these out is why the hop scan carries two columns instead of four.
// Measured on the live-shaped staging database on 2026-09-07, a 7d window
// yields 3,470,188 hop rows against 79,652 transmissions: 43 hop rows per
// transmission, each of which was re-reading the same scope_name and
// first_seen. SQLite spends 2.7s of the 16.7s that window cost; the rest was
// the Go side scanning columns it already knew.
//
// Every SQL-side attempt to shrink the hop scan itself measured worse on that
// same database and was rejected: a first-4-hex prefix filter against the
// declared targets takes 20.9s (and needs lower() on both sides, because 80%
// of stored hops are uppercase), GROUP BY t.id, hop takes 38.0s, and
// SELECT DISTINCT t.id, path_json takes 17.7s. The 3.47M rows are inherent:
// 1,368,761 observations carrying a path, ~2.5 usable hops each.
var scopeAuditWindowMetaQuery = `
	SELECT t.id, t.scope_name, t.first_seen
	FROM transmissions t
	WHERE t.first_seen >= ?
	  AND ` + scopeConformanceForwarderRouteTypesSQL + `
`

// scopeAuditTxMeta is one transmission's contribution to the aggregate, held
// once per transmission rather than once per hop.

// scopeAuditTxMeta is one transmission's contribution to the aggregate, held
// once per transmission rather than once per hop.
type scopeAuditTxMeta struct {
	scopeName sql.NullString
	firstSeen string
}

// scopeAuditWindowMeta loads scopeAuditWindowMetaQuery into a map keyed by
// transmission id. Runs on the caller's transaction so it shares the hop
// scan's snapshot.

// scopeAuditWindowMeta loads scopeAuditWindowMetaQuery into a map keyed by
// transmission id. Runs on the caller's transaction so it shares the hop
// scan's snapshot.
func scopeAuditWindowMeta(tx *sql.Tx, sinceISO string) (map[int64]scopeAuditTxMeta, error) {
	rows, err := tx.Query(scopeAuditWindowMetaQuery, sinceISO)
	if err != nil {
		return nil, fmt.Errorf("scope audit window meta: %w", err)
	}
	defer rows.Close()

	meta := map[int64]scopeAuditTxMeta{}
	for rows.Next() {
		var id int64
		var m scopeAuditTxMeta
		if err := rows.Scan(&id, &m.scopeName, &m.firstSeen); err != nil {
			return nil, fmt.Errorf("scope audit window meta scan: %w", err)
		}
		meta[id] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scope audit window meta rows: %w", err)
	}
	return meta, nil
}

// scopeAuditSeenKey identifies one (target, transmission) pair for the
// de-duplication below. A struct key rather than the string it used to be
// built into: the hop scan reaches millions of rows on a 7d window, and every
// candidate hop was allocating a fresh "<target>|<txID>" string to ask a
// question that a comparable struct answers without allocating.

// scopeAuditSeenKey identifies one (target, transmission) pair for the
// de-duplication below. A struct key rather than the string it used to be
// built into: the hop scan reaches millions of rows on a 7d window, and every
// candidate hop was allocating a fresh "<target>|<txID>" string to ask a
// question that a comparable struct answers without allocating.
type scopeAuditSeenKey struct {
	target string
	txID   int64
}

// ScopeAuditForwarding runs scopeAuditForwarderScanQuery once for the whole
// window and attributes every forwarder hop it finds to targets, by the same
// truncated-hash prefix match ScopeConformance uses for a single pubkey.
//
// A hop is attributed only when its prefix matches EXACTLY ONE declared
// target. This endpoint exists to find a repeater that declares a region and
// is not actually forwarding it — crediting a hop to every target sharing
// its prefix would let a colliding neighbour's traffic silently paper over a
// real gap, and crediting nobody (the alternative of dropping the hop
// entirely) would invent failures for targets that simply share a collision-
// prone prefix. Instead, an ambiguous hop is credited to NEITHER candidate,
// and every candidate's ambiguousHops counter is incremented instead, so the
// row can say "this notObserved might just be a prefix collision" rather
// than presenting it as a confirmed finding. See scopeAuditTargetAgg's
// ambiguousHops field and ScopeAuditRow.AmbiguousHops.
//
// Each (target, transmission) pair is counted at most once even if seen via
// multiple observations OR via several hops of one path (a routing loop, or two
// hops colliding on the same truncated prefix), for both the attributed and the
// ambiguous count — the same de-duplication scopeConformanceQuery gets for free
// from EXISTS, done explicitly here since this scan is not correlated per
// target. Since the scan reads every hop rather than only path[last], this is
// the only thing keeping one transmission from counting several times for the
// same target; TestScopeAuditForwardingCountsOneTransmissionOncePerTarget pins
// it.

// ScopeAuditForwarding runs scopeAuditForwarderScanQuery once for the whole
// window and attributes every forwarder hop it finds to targets, by the same
// truncated-hash prefix match ScopeConformance uses for a single pubkey.
//
// A hop is attributed only when its prefix matches EXACTLY ONE declared
// target. This endpoint exists to find a repeater that declares a region and
// is not actually forwarding it — crediting a hop to every target sharing
// its prefix would let a colliding neighbour's traffic silently paper over a
// real gap, and crediting nobody (the alternative of dropping the hop
// entirely) would invent failures for targets that simply share a collision-
// prone prefix. Instead, an ambiguous hop is credited to NEITHER candidate,
// and every candidate's ambiguousHops counter is incremented instead, so the
// row can say "this notObserved might just be a prefix collision" rather
// than presenting it as a confirmed finding. See scopeAuditTargetAgg's
// ambiguousHops field and ScopeAuditRow.AmbiguousHops.
//
// Each (target, transmission) pair is counted at most once even if seen via
// multiple observations OR via several hops of one path (a routing loop, or two
// hops colliding on the same truncated prefix), for both the attributed and the
// ambiguous count — the same de-duplication scopeConformanceQuery gets for free
// from EXISTS, done explicitly here since this scan is not correlated per
// target. Since the scan reads every hop rather than only path[last], this is
// the only thing keeping one transmission from counting several times for the
// same target; TestScopeAuditForwardingCountsOneTransmissionOncePerTarget pins
// it.
func (s *PacketStore) ScopeAuditForwarding(sinceISO string, targets []string) (map[string]*scopeAuditTargetAgg, error) {
	byLen := scopeAuditPrefixIndex(targets)
	result := make(map[string]*scopeAuditTargetAgg, len(targets))

	// One read transaction for both queries. The hop scan and the per-
	// transmission metadata are two passes over the same window, and a
	// transmission arriving between them would otherwise appear in the hop scan
	// with no metadata to attribute it by — rare, but the fix is a shared
	// snapshot rather than a rule about what to do with the leftovers.
	tx, err := s.db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("scope audit forwarder scan begin: %w", err)
	}
	defer tx.Rollback()

	meta, err := scopeAuditWindowMeta(tx, sinceISO)
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(scopeAuditForwarderScanQuery, sinceISO)
	if err != nil {
		return nil, fmt.Errorf("scope audit forwarder scan: %w", err)
	}
	defer rows.Close()

	getAgg := func(target string) *scopeAuditTargetAgg {
		agg, ok := result[target]
		if !ok {
			agg = &scopeAuditTargetAgg{scopes: map[string]*ScopeObservation{}}
			result[target] = agg
		}
		return agg
	}

	seen := make(map[scopeAuditSeenKey]bool) // (target, txID) already counted (attributed or ambiguous)

	// hopBuf lower-cases the hop in place instead of through strings.ToLower.
	// 80% of the hops in this database are stored uppercase (1,026,814 of
	// 1,284,897 in a 24h window, measured 2026-09-07) because
	// packetpath.DecodePathFromRawHex writes them that way, and the great
	// majority of them match no declared target at all — so the allocation
	// ToLower makes is paid millions of times to answer "no". byLen's keys are
	// lowercase, and a map index expression on string(bytes) does not allocate.
	var hopBuf [64]byte
	for rows.Next() {
		var txID int64
		var hopRaw sql.RawBytes
		if err := rows.Scan(&txID, &hopRaw); err != nil {
			return nil, fmt.Errorf("scope audit forwarder scan scan: %w", err)
		}
		n := len(hopRaw)
		if n > len(hopBuf) {
			// Longer than a full pubkey: cannot be any target's prefix. The
			// SQL floor guards the short end, this guards the long one.
			continue
		}
		for i := 0; i < n; i++ {
			c := hopRaw[i]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			hopBuf[i] = c
		}
		candidates := byLen[n][string(hopBuf[:n])]
		if len(candidates) == 0 {
			continue
		}

		if len(candidates) > 1 {
			for _, target := range candidates {
				key := scopeAuditSeenKey{target: target, txID: txID}
				if seen[key] {
					continue
				}
				seen[key] = true
				getAgg(target).ambiguousHops++
			}
			continue
		}
		txMeta, ok := meta[txID]
		if !ok {
			// Impossible while both queries share one snapshot and one WHERE
			// clause; treated as "nothing to attribute" rather than silently
			// counted as unscoped, which is what a zero-valued meta would do.
			continue
		}
		scopeName, firstSeen := txMeta.scopeName, txMeta.firstSeen
		for _, target := range candidates {
			key := scopeAuditSeenKey{target: target, txID: txID}
			if seen[key] {
				continue
			}
			seen[key] = true

			agg := getAgg(target)
			if !scopeName.Valid {
				agg.unscopedPackets++
				continue
			}
			if scopeName.String == "" {
				// Unmatched: transport-scoped, but no configured region key
				// matched code1. Still not part of the declared/observed
				// comparison — it names no region, so it can never satisfy a
				// declaration — but it is the evidence that a notObserved
				// finding on this row may be a gap in this instance's
				// hashRegions rather than in the repeater's forwarding.
				agg.unmatchedPackets++
				if len(agg.unmatchedTxIDs) < scopeVerifyMaxPacketsPerTarget {
					agg.unmatchedTxIDs = append(agg.unmatchedTxIDs, txID)
				}
				continue
			}
			name := normScope(scopeName.String)
			so, ok := agg.scopes[name]
			if !ok {
				so = &ScopeObservation{Scope: name, FirstSeen: firstSeen, LastSeen: firstSeen}
				agg.scopes[name] = so
			}
			so.Packets++
			if firstSeen < so.FirstSeen {
				so.FirstSeen = firstSeen
			}
			if firstSeen > so.LastSeen {
				so.LastSeen = firstSeen
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scope audit forwarder scan rows: %w", err)
	}
	return result, nil
}

// Scope audit configuration-state values — see ScopeAuditRow.ConfigState and
// scopeAuditConfigState.

const (
	ScopeConfigFull       = "full"        // named regions AND '*'
	ScopeConfigNoScopes   = "no-scopes"   // '*' only, no named regions
	ScopeConfigNoUnscoped = "no-unscoped" // named regions, no '*'
	ScopeConfigNoFlood    = "no-flood"    // neither named regions nor '*'
)

// scopeAuditConfigState classifies a repeater's declared-regions answer into
// one of four configuration states, from ONLY the two fields the caller has
// already parsed out of the raw declared list (namedRegions and wildcard) —
// never re-derived from regions_csv, which splitRegionsCSV/normScope have
// already reduced to exactly these two facts.
//
// The firmware exports the FLOOD-allowed set (region_map.exportNamesTo(...,
// REGION_DENY_FLOOD) in examples/simple_repeater/MyMesh.cpp), so "no named
// regions in the export" does not strictly prove "no regions defined": a
// repeater with regions defined but every one of them marked deny-flood
// exports exactly the same list as one with no region tree at all, and the
// two are indistinguishable from this data alone. That caveat lands on
// ScopeConfigNoScopes and ScopeConfigNoFlood, both of which read "zero named
// regions in the export". It does NOT land on the wildcard half of the
// classification: '*' absent from the export is exact, not an inference —
// it means the wildcard denies flooding, i.e. plain unscoped floods are not
// forwarded, full stop. That exactness is why ScopeConfigNoUnscoped needs no
// caveat, and why ScopeConfigNoFlood's "no unscoped forwarding" half is
// exact even though its "no named regions" half is not.
func scopeAuditConfigState(namedRegions []string, wildcard bool) string {
	switch {
	case len(namedRegions) > 0 && wildcard:
		return ScopeConfigFull
	case len(namedRegions) == 0 && wildcard:
		return ScopeConfigNoScopes
	case len(namedRegions) > 0 && !wildcard:
		return ScopeConfigNoUnscoped
	default:
		return ScopeConfigNoFlood
	}
}

// ScopeAuditRow is one repeater's declared-vs-observed comparison for
// GET /api/scope-audit — the network-wide answer to "which repeaters
// declare a region they are not actually forwarding". Every field is
// already normalised (no leading '#') and '*' is never present in
// DeclaredRegions/NotObserved/UndeclaredObserved — see DeclaredWildcard and
// WildcardContradiction for its counterpart.

type ScopeAuditRow struct {
	PublicKey string `json:"publicKey"`
	// Name/Role are pointers so "we hold no nodes row for this target" serialises
	// as null rather than "". A declared-regions row can name a repeater this
	// instance has never recorded, and an empty string would make that
	// indistinguishable from a node we DO know that simply has no name — the
	// same absent-is-not-empty rule the declared side already follows.
	Name *string `json:"name"`
	Role *string `json:"role"`

	DeclaredRegions  []string `json:"declaredRegions"`  // '*' excluded — see DeclaredWildcard
	DeclaredWildcard bool     `json:"declaredWildcard"` // '*' present in the raw declared list
	// ConfigState is scopeAuditConfigState(DeclaredRegions, DeclaredWildcard)
	// — one of ScopeConfigFull/NoScopes/NoUnscoped/NoFlood, the config-state
	// reading of those same two fields so a caller does not have to
	// re-derive it from them. See scopeAuditConfigState's doc comment for
	// the caveat on the "no named regions" half of NoScopes/NoFlood.
	ConfigState string `json:"configState"`
	DeclaredAt  string `json:"declaredAt"` // ISO — age of the declared answer, not the window
	Truncated   bool   `json:"truncated"`  // declared list may have had entries silently dropped

	// NotObserved is declared regions with zero matched-forwarding observed
	// in the window — the headline this endpoint exists to surface.
	NotObserved []string `json:"notObserved"`
	// UndeclaredObserved is scopes this repeater was observed forwarding
	// that are absent from its declared list.
	UndeclaredObserved []ScopeObservation `json:"undeclaredObserved"`

	// ObservedUnscopedPackets is plain-FLOOD (no transport scope) packets
	// this repeater was observed forwarding in the window — the '*'
	// counterpart, per DeclaredWildcard's doc comment.
	ObservedUnscopedPackets int64 `json:"observedUnscopedPackets"`
	// WildcardContradiction is true when this repeater was observed
	// forwarding unscoped floods but its declared list omits '*' — it says
	// it does NOT forward them, and the traffic says otherwise.
	WildcardContradiction bool `json:"wildcardContradiction"`

	// AmbiguousHops counts forwarder-hop observations in the window whose
	// truncated hash prefix matched this target's pubkey AND at least one
	// other declared target's — see ScopeAuditForwarding's doc comment for
	// why those hops are attributed to neither and instead counted here on
	// every candidate. A non-zero value is a caveat, not a finding: any
	// NotObserved entry on this row could be explained by a colliding
	// neighbour's traffic rather than a real absence, and any
	// UndeclaredObserved entry is unaffected by it (ambiguous hops are never
	// attributed to a scope at all).
	AmbiguousHops int64 `json:"ambiguousHops"`

	// ObservedUnmatchedPackets counts packets this repeater was observed
	// forwarding whose transport scope matched no region key this instance
	// holds. Like AmbiguousHops it is a caveat rather than a finding, but the
	// two have different causes and different fixes: AmbiguousHops is a
	// pubkey-prefix collision between two repeaters and nobody's fault,
	// ObservedUnmatchedPackets is a missing entry in this instance's own
	// hashRegions and the reader can act on it. A non-zero value means any
	// NotObserved entry on this row may name a region this instance cannot
	// name rather than one the repeater is not forwarding.
	//
	// It says nothing about DeclaredWildcard: unmatched traffic IS scoped, so
	// it never feeds WildcardContradiction, which counts only plain unscoped
	// floods.
	ObservedUnmatchedPackets int64 `json:"observedUnmatchedPackets"`

	// ObservedUnmatchedSampled is how many of those packets verification could
	// actually look at: the per-target list is capped at
	// scopeVerifyMaxPacketsPerTarget and the window sample at
	// scopeVerifyMaxWindowPackets, while ObservedUnmatchedPackets keeps
	// counting past both.
	//
	// It exists because a client cannot otherwise subtract the two fields
	// honestly. RegionEvidence can only ever count packets inside this sample,
	// so on a row where this is smaller than ObservedUnmatchedPackets the
	// difference between them is an upper bound on the unexplained traffic, not
	// a figure. Equal values mean the subtraction is exact.
	ObservedUnmatchedSampled int64 `json:"observedUnmatchedSampled"`

	// RegionEvidence maps a declared region to how many of this repeater's own
	// unmatched forwarded packets derive to it — see scope_verify.go. A region
	// reaching scopeVerifyMinCorroboration is removed from NotObserved, so this
	// field is NOT what decides the chip's colour; NotObserved remains the sole
	// source of that. This exists so a client can say HOW a region was
	// established, and can explain a region that got exactly one hit and
	// therefore stayed in NotObserved.
	//
	// Absent regions simply had no matching traffic. Never nil in the response
	// — an empty object and a missing key mean the same thing, and an empty map
	// is the cheaper thing for a client to iterate.
	RegionEvidence map[string]int `json:"regionEvidence"`
}

// ScopeAuditResponse is the payload for GET /api/scope-audit. Only
// repeaters with at least one declared-regions answer are included — a
// repeater never successfully asked is absent, not shown declaring nothing
// (see AllCurrentDeclaredRegions).
type ScopeAuditResponse struct {
	Window    string          `json:"window"`
	Since     string          `json:"since"` // ISO — start of the observed-forwarding window
	Repeaters []ScopeAuditRow `json:"repeaters"`
}

// NodeScopesResponse is the payload for GET /api/nodes/{pubkey}/scopes: the
// observed-forwarding side (ScopeConformance, embedded BY VALUE so its three
// scope states sit at the JSON top level — unmatched and unscoped are
// separate counts and a matched scope never has an empty name) plus the
// declared side (DeclaredRegions), returned together so the UI needs only
// one request per node rather than a second per-item call.
//
// ScopeConformance is embedded by value rather than as *ScopeConformance:
// encoding/json silently skips fields promoted through a nil embedded
// pointer instead of erroring, which would drop observed/unmatched/unscoped
// from the body entirely rather than surfacing the failure.

type scopesState struct {
	cacheMu sync.RWMutex
	cache   map[string]scopesCacheEntry
	sf      singleflight.Group

	// lastSeenBlacklistGen mirrors reachState's field: when the live
	// blacklist generation advances past this value, the cache is purged
	// wholesale on the next request. The 404 path itself is never cached
	// (the handler returns before a cache key is even computed), so this
	// only guards a pre-existing successful entry from outliving a
	// blacklist/hide edit made after it was filled.
	lastSeenBlacklistGen atomic.Uint64
}

type scopesCacheEntry struct {
	at  time.Time
	raw []byte
}

const (
	// nodeScopesCacheTTL matches the sibling /api/scope-stats endpoint's
	// cache lifetime — also per-window region-scope data recomputed from
	// the same transmissions table.
	nodeScopesCacheTTL = 30 * time.Second
	nodeScopesCacheMax = 256
)

// scopesCacheGet returns the cached marshalled JSON for key. The returned
// slice is shared (not copied) and MUST NOT be mutated by callers.
func (s *Server) scopesCacheGet(key string) ([]byte, bool) {
	s.scopes.cacheMu.RLock()
	defer s.scopes.cacheMu.RUnlock()
	e, ok := s.scopes.cache[key]
	if !ok || time.Since(e.at) > nodeScopesCacheTTL {
		return nil, false
	}
	return e.raw, true
}

func (s *Server) scopesCachePut(key string, raw []byte) {
	s.scopes.cacheMu.Lock()
	defer s.scopes.cacheMu.Unlock()
	if s.scopes.cache == nil {
		s.scopes.cache = map[string]scopesCacheEntry{}
	}
	if _, exists := s.scopes.cache[key]; !exists && len(s.scopes.cache) >= nodeScopesCacheMax {
		s.evictScopesLocked()
	}
	s.scopes.cache[key] = scopesCacheEntry{at: time.Now(), raw: raw}
}

// evictScopesLocked drops expired entries first; if still at the cap it
// evicts the single oldest entry. Caller holds s.scopes.cacheMu (write).
func (s *Server) evictScopesLocked() {
	now := time.Now()
	for k, e := range s.scopes.cache {
		if now.Sub(e.at) > nodeScopesCacheTTL {
			delete(s.scopes.cache, k)
		}
	}
	if len(s.scopes.cache) < nodeScopesCacheMax {
		return
	}
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range s.scopes.cache {
		if first || e.at.Before(oldestAt) {
			oldestKey, oldestAt, first = k, e.at, false
		}
	}
	if !first {
		delete(s.scopes.cache, oldestKey)
	}
}

// scopesPurgeIfBlacklistGenChanged drops every cached entry when the live
// blacklist generation has advanced past the cache's last-seen value,
// mirroring reachPurgeIfBlacklistGenChanged. CAS gates the purge so
// concurrent callers only do the work once per gen bump.
func (s *Server) scopesPurgeIfBlacklistGenChanged(gen uint64) {
	seen := s.scopes.lastSeenBlacklistGen.Load()
	if gen == seen {
		return
	}
	if !s.scopes.lastSeenBlacklistGen.CompareAndSwap(seen, gen) {
		return
	}
	s.scopes.cacheMu.Lock()
	s.scopes.cache = nil
	s.scopes.cacheMu.Unlock()
}

// nodeScopesWindowLookback maps the ?window= vocabulary to a lookback
// duration. Matches the sibling /api/scope-stats endpoint's vocabulary
// exactly (1h, 24h, 7d) rather than the broader ParseTimeWindow alias set
// (which also accepts 1d/3d/1w/30d) used by unrelated analytics endpoints.

func nodeScopesWindowLookback(window string) (time.Duration, bool) {
	switch window {
	case "1h":
		return time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	case "7d":
		return 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// handleNodeScopes serves GET /api/nodes/{pubkey}/scopes?window=1h|24h|7d.
//
// A pubkey never heard forwarding anything is a valid question with an
// empty answer (200), not a 404 — ScopeConformance already treats it that
// way, and this handler performs no node-existence lookup that would
// override it.

// handleScopeAudit serves GET /api/scope-audit?window=1h|24h|7d: the
// network-wide answer to "which repeaters declare a region they are not
// actually forwarding". Unlike the per-repeater /api/nodes/{pubkey}/scopes,
// this compares every repeater that has ever declared a region list in one
// pass — see scopes.go's AllCurrentDeclaredRegions and ScopeAuditForwarding
// for why that stays a single scan rather than one query per repeater.
func (s *Server) handleScopeAudit(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	lookback, ok := nodeScopesWindowLookback(window)
	if !ok {
		writeError(w, 400, "window must be 1h, 24h, or 7d")
		return
	}

	sinceISO := time.Now().Add(-lookback).UTC().Format(time.RFC3339)

	if cached, ok := s.scopeAuditCached(window); ok {
		writeJSON(w, cached)
		return
	}

	// singleflight: the compute below runs outside the cache mutex, so without
	// this every request arriving on a cold window ran its own full scan
	// concurrently. Reading every hop makes that scan seconds of work over
	// millions of rows, which is the shape that turns a thundering herd from
	// wasteful into expensive. Same treatment /api/observers and
	// /api/nodes/{pubkey}/reach already have.
	v, err, _ := s.scopeAuditSF.Do(window, func() (interface{}, error) {
		// Waiters that arrive while a scan is in flight are served by that
		// scan's result; this second look is for the caller that acquires the
		// group right after a winner stored one.
		if cached, ok := s.scopeAuditCached(window); ok {
			return cached, nil
		}
		resp, cErr := s.computeScopeAudit(window, sinceISO)
		if cErr != nil {
			return nil, cErr
		}
		s.scopeAuditStore(window, resp)
		return resp, nil
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, v.(*ScopeAuditResponse))
}

// scopeAuditTTLFor is how long one window's computed audit stays fresh.
//
// 7d is not 30s because it does not cost what the others cost. Measured on a
// live-shaped database with 206 declared repeaters and 965k transmissions:
// 16.7s cold for 7d against 4.0s for 24h and 0.15s for 1h, with the 7d scan
// reading 3,470,188 hop rows. At a 30s TTL a single reader with that window
// open keeps the instance recomputing more than half the time, for an
// aggregate that moves at the pace of a week of traffic. Five minutes of
// staleness on a seven-day window is not a fact the reader can act on
// differently.
func scopeAuditTTLFor(window string) time.Duration {
	if window == "7d" {
		return 5 * time.Minute
	}
	return 30 * time.Second
}

// scopeAuditCached returns the cached response for a window while it is within
// that window's TTL.
func (s *Server) scopeAuditCached(window string) (*ScopeAuditResponse, bool) {
	s.scopeAuditMu.Lock()
	defer s.scopeAuditMu.Unlock()
	if s.scopeAuditCache == nil {
		return nil, false
	}
	cached, ok := s.scopeAuditCache[window]
	if !ok || time.Since(s.scopeAuditCachedAt[window]) >= scopeAuditTTLFor(window) {
		return nil, false
	}
	return cached, true
}

// scopeAuditStore publishes a freshly computed response for a window.
func (s *Server) scopeAuditStore(window string, resp *ScopeAuditResponse) {
	s.scopeAuditMu.Lock()
	defer s.scopeAuditMu.Unlock()
	if s.scopeAuditCache == nil {
		s.scopeAuditCache = make(map[string]*ScopeAuditResponse)
		s.scopeAuditCachedAt = make(map[string]time.Time)
	}
	s.scopeAuditCache[window] = resp
	s.scopeAuditCachedAt[window] = time.Now()
}

// computeScopeAudit builds one window's audit response: the declared lists and
// the forwarding evidence attributed to them. Split out of the handler so the
// cache and its singleflight wrap a plain function instead of a request.
func (s *Server) computeScopeAudit(window, sinceISO string) (*ScopeAuditResponse, error) {
	declared, err := s.db.AllCurrentDeclaredRegions()
	if err != nil {
		return nil, err
	}

	targets := make([]string, 0, len(declared))
	for _, d := range declared {
		targets = append(targets, strings.ToLower(d.Target))
	}

	forwarding := map[string]*scopeAuditTargetAgg{}
	if s.store != nil {
		forwarding, err = s.store.ScopeAuditForwarding(sinceISO, targets)
		if err != nil {
			return nil, err
		}
	}

	// Declared-region verification: a region this instance holds no key for is
	// unnameable, not absent, and the audit can settle which by deriving the key
	// from the repeater's own declaration and testing it against that
	// repeater's own unnameable traffic. One verifier serves every row so each
	// (region, transmission) pair is derived at most once — see scope_verify.go
	// for why that memo is what keeps this affordable.
	//
	// A failure here degrades to "no verification" rather than failing the
	// request: the audit was useful before this existed and must stay useful if
	// the extra query errors.
	var verifier *scopeVerifier
	if s.store != nil {
		unmatchedRows, truncated, uErr := s.store.unmatchedTransmissionsInWindow(sinceISO)
		if uErr != nil {
			log.Printf("[scope-audit] declared-region verification unavailable: %v", uErr)
		} else {
			if truncated {
				// Said out loud rather than absorbed: past the cap a region can
				// hold evidence this refresh did not look at, so a grey chip
				// means "not corroborated in this sample", not "not forwarded".
				log.Printf("[scope-audit] window %s holds more than %d unnameable packets; verification used the most recent %d and may under-report evidence",
					window, scopeVerifyMaxWindowPackets, scopeVerifyMaxWindowPackets)
			}
			verifier = newScopeVerifier(unmatchedRows)
		}
	}

	identities := s.db.scopeAuditNodeIdentities(targets)

	resp := &ScopeAuditResponse{Window: window, Since: sinceISO, Repeaters: []ScopeAuditRow{}}
	for _, d := range declared {
		pk := strings.ToLower(d.Target)
		if s.cfg != nil && s.cfg.IsBlacklisted(pk) {
			continue
		}
		id := identities[pk]
		// A target with no nodes row has no name to match a hidden-prefix rule
		// against; it cannot be hidden by name, so only check when we have one.
		if id.Name != nil && s.cfg != nil && s.cfg.IsNameHidden(*id.Name) {
			continue
		}

		allRegions := splitRegionsCSV(d.RegionsCSV)
		declaredWildcard := false
		declaredNamed := make([]string, 0, len(allRegions))
		declaredSet := make(map[string]bool, len(allRegions))
		for _, rgn := range allRegions {
			if rgn == "*" {
				declaredWildcard = true
				continue
			}
			// normScope mirrors the observed side (scopes.go: agg.scopes keys
			// are already normScope'd) — regions_csv is not guaranteed to
			// arrive with '#' already stripped in every case, and comparing
			// raw here would reintroduce the exact trap normScope exists to
			// prevent, just on the other side of the comparison.
			rgn = normScope(rgn)
			declaredNamed = append(declaredNamed, rgn)
			declaredSet[rgn] = true
		}

		agg := forwarding[pk]

		// Verify the declared regions this repeater has no NAMED evidence for,
		// against its own unmatched traffic. Regions already observed by name
		// need no verification and are not tested — that keeps the candidate
		// set to exactly the open questions, which is also what keeps the
		// verifier's work proportional to the problem rather than to the fleet.
		unnamed := []string{}
		for _, rgn := range declaredNamed {
			if agg == nil || agg.scopes[rgn] == nil {
				unnamed = append(unnamed, rgn)
			}
		}
		regionEvidence := map[string]int{}
		verifiedSet := map[string]bool{}
		if verifier != nil && agg != nil && len(unnamed) > 0 {
			// capVerifyRegions bounds the per-target half of the verifier's
			// work. The declared list arrives from a collector and its LENGTH
			// is not validated anywhere on the way in, while each distinct name
			// costs a full pass over the packet set.
			regionEvidence = verifier.evidence(agg.unmatchedTxIDs, capVerifyRegions(unnamed))
			for _, rgn := range verifier.verified(regionEvidence) {
				verifiedSet[rgn] = true
			}
		}
		notObserved := []string{}
		for _, rgn := range unnamed {
			if !verifiedSet[rgn] {
				notObserved = append(notObserved, rgn)
			}
		}

		undeclared := []ScopeObservation{}
		if agg != nil {
			names := make([]string, 0, len(agg.scopes))
			for name := range agg.scopes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if !declaredSet[name] {
					undeclared = append(undeclared, *agg.scopes[name])
				}
			}
		}

		var unscopedPackets, ambiguousHops, unmatchedPackets, unmatchedSampled int64
		if agg != nil {
			unscopedPackets = agg.unscopedPackets
			ambiguousHops = agg.ambiguousHops
			unmatchedPackets = agg.unmatchedPackets
			// What verification could actually see, against what was counted.
			// The list is capped; the count is not.
			unmatchedSampled = int64(len(agg.unmatchedTxIDs))
		}

		resp.Repeaters = append(resp.Repeaters, ScopeAuditRow{
			PublicKey:                pk,
			Name:                     id.Name,
			Role:                     id.Role,
			DeclaredRegions:          declaredNamed,
			DeclaredWildcard:         declaredWildcard,
			ConfigState:              scopeAuditConfigState(declaredNamed, declaredWildcard),
			DeclaredAt:               d.ObservedAt,
			Truncated:                d.Truncated,
			NotObserved:              notObserved,
			UndeclaredObserved:       undeclared,
			ObservedUnscopedPackets:  unscopedPackets,
			WildcardContradiction:    unscopedPackets > 0 && !declaredWildcard,
			AmbiguousHops:            ambiguousHops,
			ObservedUnmatchedPackets: unmatchedPackets,
			ObservedUnmatchedSampled: unmatchedSampled,
			RegionEvidence:           regionEvidence,
		})
	}

	// Interesting rows first: a repeater silently not forwarding a declared
	// region is the headline this endpoint exists to surface, ranked by how
	// many declared regions it's missing. The wildcard contradiction and
	// undeclared-observed counts are secondary tie-breaks — flags on the
	// row, not a separate ranking. Full agreement (no issues at all) sorts
	// to the bottom, alphabetically, so it doesn't crowd out the rows that
	// matter.
	sort.Slice(resp.Repeaters, func(i, j int) bool {
		a, b := resp.Repeaters[i], resp.Repeaters[j]
		if len(a.NotObserved) != len(b.NotObserved) {
			return len(a.NotObserved) > len(b.NotObserved)
		}
		if a.WildcardContradiction != b.WildcardContradiction {
			return a.WildcardContradiction
		}
		if len(a.UndeclaredObserved) != len(b.UndeclaredObserved) {
			return len(a.UndeclaredObserved) > len(b.UndeclaredObserved)
		}
		// Fall back to the pubkey for an unnamed or unknown node so the order is
		// still total and stable rather than grouping every nameless row together.
		an, bn := a.PublicKey, b.PublicKey
		if a.Name != nil && *a.Name != "" {
			an = *a.Name
		}
		if b.Name != nil && *b.Name != "" {
			bn = *b.Name
		}
		return an < bn
	})

	return resp, nil
}
