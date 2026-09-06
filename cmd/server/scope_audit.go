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
	"net/http"
	"sort"
	"strconv"
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
// It applies the SAME three conditions scopeConformanceQuery does —
// minForwarderHopHexLen, scopeConformanceForwarderRouteTypesSQL, and the
// explicit json_valid guard against a single malformed path_json row
// erroring the whole query — but does not join against any target list:
// attribution to a specific declared target happens in Go
// (ScopeAuditForwarding), against the small in-memory prefix index built by
// scopeAuditPrefixIndex, so the SQL cost stays O(rows in window) regardless
// of len(targets).
var scopeAuditForwarderScanQuery = `
	SELECT t.id, je.value, t.scope_name, t.first_seen
	FROM transmissions t
	JOIN observations o ON o.transmission_id = t.id
	JOIN json_each(o.path_json) je ON je.key = json_array_length(o.path_json) - 1
	WHERE t.first_seen >= ?
	  AND ` + scopeConformanceForwarderRouteTypesSQL + `
	  AND o.path_json IS NOT NULL
	  AND json_valid(o.path_json)
	  AND json_array_length(o.path_json) > 0
	  AND LENGTH(je.value) >= ` + fmt.Sprint(minForwarderHopHexLen) + `
`

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

func (s *PacketStore) ScopeAuditForwarding(sinceISO string, targets []string) (map[string]*scopeAuditTargetAgg, error) {
	byLen := scopeAuditPrefixIndex(targets)
	result := make(map[string]*scopeAuditTargetAgg, len(targets))

	rows, err := s.db.conn.Query(scopeAuditForwarderScanQuery, sinceISO)
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

	seen := make(map[string]bool) // "<target>|<txID>" already counted (attributed or ambiguous)
	for rows.Next() {
		var txID int64
		var hop string
		var scopeName sql.NullString
		var firstSeen string
		if err := rows.Scan(&txID, &hop, &scopeName, &firstSeen); err != nil {
			return nil, fmt.Errorf("scope audit forwarder scan scan: %w", err)
		}
		hop = strings.ToLower(hop)
		candidates := byLen[len(hop)][hop]

		if len(candidates) > 1 {
			for _, target := range candidates {
				key := target + "|" + strconv.FormatInt(txID, 10)
				if seen[key] {
					continue
				}
				seen[key] = true
				getAgg(target).ambiguousHops++
			}
			continue
		}
		for _, target := range candidates {
			key := target + "|" + strconv.FormatInt(txID, 10)
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
				continue // unmatched — not part of the declared/observed comparison
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
	const scopeAuditTTL = 30 * time.Second

	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	lookback, ok := nodeScopesWindowLookback(window)
	if !ok {
		writeError(w, 400, "window must be 1h, 24h, or 7d")
		return
	}

	s.scopeAuditMu.Lock()
	if s.scopeAuditCache != nil {
		if cached, ok := s.scopeAuditCache[window]; ok && time.Since(s.scopeAuditCachedAt[window]) < scopeAuditTTL {
			s.scopeAuditMu.Unlock()
			writeJSON(w, cached)
			return
		}
	}
	s.scopeAuditMu.Unlock()

	declared, err := s.db.AllCurrentDeclaredRegions()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	targets := make([]string, 0, len(declared))
	for _, d := range declared {
		targets = append(targets, strings.ToLower(d.Target))
	}

	sinceISO := time.Now().Add(-lookback).UTC().Format(time.RFC3339)
	forwarding := map[string]*scopeAuditTargetAgg{}
	if s.store != nil {
		forwarding, err = s.store.ScopeAuditForwarding(sinceISO, targets)
		if err != nil {
			writeError(w, 500, err.Error())
			return
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

		notObserved := []string{}
		for _, rgn := range declaredNamed {
			if agg == nil || agg.scopes[rgn] == nil {
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

		var unscopedPackets, ambiguousHops int64
		if agg != nil {
			unscopedPackets = agg.unscopedPackets
			ambiguousHops = agg.ambiguousHops
		}

		resp.Repeaters = append(resp.Repeaters, ScopeAuditRow{
			PublicKey:               pk,
			Name:                    id.Name,
			Role:                    id.Role,
			DeclaredRegions:         declaredNamed,
			DeclaredWildcard:        declaredWildcard,
			ConfigState:             scopeAuditConfigState(declaredNamed, declaredWildcard),
			DeclaredAt:              d.ObservedAt,
			Truncated:               d.Truncated,
			NotObserved:             notObserved,
			UndeclaredObserved:      undeclared,
			ObservedUnscopedPackets: unscopedPackets,
			WildcardContradiction:   unscopedPackets > 0 && !declaredWildcard,
			AmbiguousHops:           ambiguousHops,
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

	s.scopeAuditMu.Lock()
	if s.scopeAuditCache == nil {
		s.scopeAuditCache = make(map[string]*ScopeAuditResponse)
		s.scopeAuditCachedAt = make(map[string]time.Time)
	}
	s.scopeAuditCache[window] = resp
	s.scopeAuditCachedAt[window] = time.Now()
	s.scopeAuditMu.Unlock()

	writeJSON(w, resp)
}
