package main

import (
	"crypto/sha256"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
)

// declaredRegionStat is one region name as reported over RF, with the two
// facts the cap ranks on: how many distinct repeaters declare it, and how
// recently any of them last answered.
type declaredRegionStat struct {
	Name      string
	Declarers int
	LastSeen  string // ISO, greatest observed_at across declarers
}

// maxRegionNameLen bounds a derived region name. Firmware region names are
// short labels; anything longer is a malformed or hostile entry, and each
// accepted name costs an HMAC on every transport-scoped packet.
const maxRegionNameLen = 32

// regionNameAcceptable reports whether a declared name may become a derived
// region key, and returns the name in the form the key is derived from.
//
// The two declared-region sources spell a name differently, so this
// canonicalises before judging: nodes.configured_scope carries the leading
// "#" (normalizeScopeList puts it there, matching every other stored scope
// value), while a node_declared_regions row carries the bare name the OTA
// query returned. Both mean the same region, and loadRegionKeys already
// prefixes a missing "#" before hashing, so the "#" is stripped here and put
// back by the caller.
//
// The rules are structural, never about the name's meaning. A declared set
// contains entries that look like junk ("null", "sol3"), but a blocklist on
// string values is unmaintainable, and the cost of one bad name is a single
// slot out of maxDerived plus a 1-in-65536 collision chance. What IS rejected
// is anything that cannot be a region name:
//
//   - "*" is the flood wildcard, not a region, and hashing it would invent a
//     region nobody declared
//   - a comma would split the name on the next round-trip through a
//     comma-separated column
//   - a second "#" cannot come from either source intact
//   - non-ASCII or whitespace would make the key a hash over bytes nobody
//     intended, silently mismatching the sender
//   - a NUL is block-cipher padding a stale client failed to trim
func regionNameAcceptable(name string) (string, bool) {
	name = strings.TrimPrefix(name, "#")
	if name == "" || name == "*" || len(name) > maxRegionNameLen {
		return "", false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= ' ' || c >= 0x7F || c == ',' || c == '#' {
			return "", false
		}
	}
	return name, true
}

// declaredRegionSources reads the declared region names from every source this
// instance has, newest answer per node, and counts how many distinct nodes
// declare each name.
//
// Two sources, mirroring what the server's AllCurrentDeclaredRegions already
// merges, so the derived tier sees exactly what the audit sees:
//
//   - nodes.configured_scope, written by the observer /neighbors ingestion
//     (#1865). Always present; the column is part of the schema.
//   - node_declared_regions, an optional table a deployment may fill by other
//     means. Absent on a stock install, so its absence is not an error: the
//     probe below asks sqlite_master first rather than letting "no such table"
//     abort a refresh that the first source could still answer.
//
// A node counts once per name however many times it declares it, so the
// ranking below reflects how widely a region is claimed rather than how
// chatty one node is.
func (s *Store) declaredRegionSources() ([]declaredRegionStat, error) {
	agg := map[string]*declaredRegionStat{}
	count := func(csv, at string) {
		seenHere := map[string]bool{} // one node counts once per name
		for _, name := range splitDeclaredRegionsCSV(csv) {
			// '*' is the flood wildcard, not a region. regionNameAcceptable
			// rejects it too, but skipping it here as well keeps it out of the
			// declared-name count that the cap arithmetic and the refresh log
			// are stated in: nearly every node declares it, so counting it
			// would overstate both on every deployment.
			if name == "*" || name == "#*" || seenHere[name] {
				continue
			}
			seenHere[name] = true
			st, ok := agg[name]
			if !ok {
				st = &declaredRegionStat{Name: name}
				agg[name] = st
			}
			st.Declarers++
			if at > st.LastSeen {
				st.LastSeen = at
			}
		}
	}

	rows, err := s.db.Query(`
		SELECT COALESCE(configured_scope, ''), COALESCE(configured_scope_at, '')
		FROM nodes
		WHERE configured_scope IS NOT NULL AND configured_scope != ''
	`)
	if err != nil {
		return nil, fmt.Errorf("declared regions from configured_scope: %w", err)
	}
	for rows.Next() {
		var csv, at string
		if err := rows.Scan(&csv, &at); err != nil {
			rows.Close()
			return nil, fmt.Errorf("declared regions from configured_scope scan: %w", err)
		}
		count(csv, at)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("declared regions from configured_scope rows: %w", err)
	}
	rows.Close()

	var present string
	if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='node_declared_regions'`).Scan(&present); err == nil && present != "" {
		ndr, err := s.db.Query(`
			WITH ranked AS (
				SELECT target, observed_at, regions_csv,
					ROW_NUMBER() OVER (PARTITION BY target ORDER BY observed_at DESC) AS rn
				FROM node_declared_regions
			)
			SELECT observed_at, regions_csv FROM ranked WHERE rn = 1
		`)
		if err != nil {
			return nil, fmt.Errorf("declared regions from node_declared_regions: %w", err)
		}
		defer ndr.Close()
		for ndr.Next() {
			var at, csv string
			if err := ndr.Scan(&at, &csv); err != nil {
				return nil, fmt.Errorf("declared regions from node_declared_regions scan: %w", err)
			}
			count(csv, at)
		}
		if err := ndr.Err(); err != nil {
			return nil, fmt.Errorf("declared regions from node_declared_regions rows: %w", err)
		}
	}

	out := make([]declaredRegionStat, 0, len(agg))
	for _, st := range agg {
		out = append(out, *st)
	}
	return out, nil
}

// rankDeclaredRegions filters stats through regionNameAcceptable and returns at
// most max names, most-worth-keeping first: by declarer count descending, then
// by recency, then by name. The name tie-break is what makes the result
// deterministic — without it the derived tier would churn between refreshes on
// equally-ranked names and the add/drop logging would be noise.
func rankDeclaredRegions(stats []declaredRegionStat, max int) []string {
	kept := make([]declaredRegionStat, 0, len(stats))
	for _, s := range stats {
		if canonical, ok := regionNameAcceptable(s.Name); ok {
			s.Name = canonical
			kept = append(kept, s)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Declarers != kept[j].Declarers {
			return kept[i].Declarers > kept[j].Declarers
		}
		if kept[i].LastSeen != kept[j].LastSeen {
			return kept[i].LastSeen > kept[j].LastSeen
		}
		return kept[i].Name < kept[j].Name
	})
	if max > 0 && len(kept) > max {
		kept = kept[:max]
	}
	names := make([]string, 0, len(kept))
	for _, s := range kept {
		names = append(names, s.Name)
	}
	return names
}

// splitDeclaredRegionsCSV parses a regions_csv value into its entries. The
// ingestor writes this column with strings.Join(regions, ","), so this is its
// exact inverse. Mirrors splitRegionsCSV in cmd/server/scopes.go.
func splitDeclaredRegionsCSV(csv string) []string {
	out := []string{}
	if csv == "" {
		return out
	}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// regionKeySnapshot is an immutable view of the region keys in force for one
// packet. `all` is the single map matching iterates - merging at build time
// rather than per packet keeps the hot path free of allocation. `explicit`
// carries membership only, and exists so the ambiguity tie-break can tell an
// operator-configured region from one derived off the air.
type regionKeySnapshot struct {
	all      map[string][]byte
	explicit map[string]bool
}

func (s *regionKeySnapshot) isExplicit(name string) bool { return s.explicit[name] }

// regionKeySet holds the live snapshot. Readers take one atomic load; a
// refresh builds the replacement off to the side and swaps the pointer, so the
// ingest hot path never blocks on a rebuild (AGENTS.md rule 0).
type regionKeySet struct {
	cur     atomic.Pointer[regionKeySnapshot]
	enabled bool
	max     int
}

// newRegionKeySet builds the explicit tier from hashRegions. The derived tier
// starts empty; refreshDerived fills it, and does nothing at all when
// autoRegionKeys is off.
func newRegionKeySet(cfg *Config) *regionKeySet {
	explicitKeys := loadRegionKeys(cfg)
	explicitNames := make(map[string]bool, len(explicitKeys))
	all := make(map[string][]byte, len(explicitKeys))
	for name, key := range explicitKeys {
		explicitNames[name] = true
		all[name] = key
	}
	s := &regionKeySet{
		enabled: cfg.AutoRegionKeysEnabled(),
		max:     cfg.AutoRegionKeysMaxDerived(),
	}
	s.cur.Store(&regionKeySnapshot{all: all, explicit: explicitNames})
	return s
}

// regionKeySetFromKeys wraps an explicit key map in a set with no derived
// tier. Tests that predate the derived tier use it to keep expressing "these
// are the configured regions" without building a Config.
func regionKeySetFromKeys(keys map[string][]byte) *regionKeySet {
	names := make(map[string]bool, len(keys))
	all := make(map[string][]byte, len(keys))
	for name, key := range keys {
		names[name] = true
		all[name] = key
	}
	s := &regionKeySet{}
	s.cur.Store(&regionKeySnapshot{all: all, explicit: names})
	return s
}

// emptyRegionKeySnapshot backs the nil case below. Shared and never mutated:
// refreshDerived always builds a fresh map rather than writing into one.
var emptyRegionKeySnapshot = &regionKeySnapshot{all: map[string][]byte{}, explicit: map[string]bool{}}

// snapshot is nil-safe on purpose. A nil *regionKeySet means "no region keys",
// which is exactly what a nil map[string][]byte meant before M2 — the shape
// several call sites and a good many tests still pass. Panicking there would
// turn an absent key set into a crash on the ingest path, which is a far worse
// failure than naming nothing.
func (s *regionKeySet) snapshot() *regionKeySnapshot {
	if s == nil {
		return emptyRegionKeySnapshot
	}
	return s.cur.Load()
}

// refreshDerived rebuilds the derived tier from names (already ranked and
// capped by the caller) and swaps in a new snapshot. It REPLACES the derived
// tier rather than merging into it, so a region that stops being declared
// leaves the key set and the cap keeps meaning something.
//
// A name that duplicates an explicit key is skipped, not re-added: the
// explicit tier must stay authoritative for the tie-break, and demoting a
// configured region because a repeater also declares it would invert the whole
// rule.
//
// Returns the names actually added, for the caller to log.
func (s *regionKeySet) refreshDerived(names []string) []string {
	if !s.enabled {
		return nil
	}
	old := s.cur.Load()
	all := make(map[string][]byte, len(old.explicit)+len(names))
	for name := range old.explicit {
		all[name] = old.all[name]
	}
	added := make([]string, 0, len(names))
	for _, raw := range names {
		canonical, ok := regionNameAcceptable(raw)
		if !ok {
			continue
		}
		name := "#" + canonical
		if old.explicit[name] {
			continue
		}
		if _, exists := all[name]; exists {
			continue
		}
		h := sha256.Sum256([]byte(name))
		all[name] = h[:16]
		added = append(added, name)
	}
	s.cur.Store(&regionKeySnapshot{all: all, explicit: old.explicit})
	return added
}

// scopeReason records how a scope match was decided, so the outcome is
// auditable in logs without a schema change. It is deliberately not stored:
// transmissions.scope_name keeps its existing three-state encoding.
type scopeReason string

const (
	scopeReasonNone                scopeReason = "none"                  // no key matched
	scopeReasonUnique              scopeReason = "unique"                // exactly one key matched
	scopeReasonExplicitOverDerived scopeReason = "explicit-over-derived" // several matched, one was operator config
	scopeReasonAmbiguous           scopeReason = "ambiguous"             // several matched, no principled winner
)

// scopeMatch is the result of naming one packet's region scope.
type scopeMatch struct {
	Name   string // empty when unresolved - the caller stores that as the unmatched state
	Reason scopeReason
	// Candidates is every name whose derived code equals the packet's code1.
	// It is always populated, so a caller that needs the match COUNT (scope-repair
	// does, to tell "the key set changed" from "several keys now collide") can read
	// it from the same result that carries the verdict, instead of re-running the
	// match and risking a second, divergent decision rule.
	Candidates []string
}

// match names the region scope of a transport-scoped packet, resolving a
// multi-key collision by evidence rather than by map order.
//
// Tiers, in order:
//
//  1. Exactly one key matched - name it.
//  2. Several matched but exactly one came from hashRegions - name that one.
//     The operator's own configuration outranks a name picked up off the air,
//     and this covers the bulk of the ambiguity auto-derivation introduces.
//  3. Otherwise abstain, returning an empty name. Two equally-sourced
//     candidates offer no principled winner, and naming a packet wrongly is
//     worse than leaving it unnamed - the rule #1609 established, unchanged.
//
// (The spec's tier-3 path-evidence tie-break sits between 2 and 3 and is
// deliberately not built here; see
// docs/specs/2026-09-07-auto-region-keys-design.md. The scopeReasonAmbiguous
// counter is what measures whether it is worth building.)
func (s *regionKeySnapshot) match(payloadType byte, payloadRaw []byte, code1 string) scopeMatch {
	matched := matchingRegions(s.all, payloadType, payloadRaw, code1)
	switch len(matched) {
	case 0:
		return scopeMatch{Reason: scopeReasonNone, Candidates: matched}
	case 1:
		return scopeMatch{Name: matched[0], Reason: scopeReasonUnique, Candidates: matched}
	}

	var explicitMatches []string
	for _, name := range matched {
		if s.explicit[name] {
			explicitMatches = append(explicitMatches, name)
		}
	}
	if len(explicitMatches) == 1 {
		return scopeMatch{Name: explicitMatches[0], Reason: scopeReasonExplicitOverDerived, Candidates: matched}
	}
	return scopeMatch{Reason: scopeReasonAmbiguous, Candidates: matched}
}

// matchScopeName is what the ingest path calls: the tiered decision above,
// reduced to the string transmissions.scope_name stores, with the outcome
// tallied for the counters. A nil set (no region keys at all) names nothing,
// which is what an unconfigured instance did before this existed.
func (s *regionKeySet) matchScopeName(payloadType byte, payloadRaw []byte, code1 string) string {
	m := s.snapshot().match(payloadType, payloadRaw, code1)
	recordScopeMatch(m)
	return m.Name
}

// scopeMatchCounters tallies how each transport-scoped packet's region was
// decided. It exists to answer one question before more machinery is built:
// how often does an ambiguous collision actually happen? The spec gates the
// path-evidence tie-break (tier 3) on this number.
var scopeMatchCounters struct {
	unique              atomic.Int64
	explicitOverDerived atomic.Int64
	ambiguous           atomic.Int64
	none                atomic.Int64
}

// recordScopeMatch tallies one decision and logs the interesting ones. Unique
// and none are the overwhelming majority and are counted silently; the other
// two are rare by construction and worth a line each.
func recordScopeMatch(m scopeMatch) {
	switch m.Reason {
	case scopeReasonUnique:
		scopeMatchCounters.unique.Add(1)
	case scopeReasonNone:
		scopeMatchCounters.none.Add(1)
	case scopeReasonExplicitOverDerived:
		scopeMatchCounters.explicitOverDerived.Add(1)
		log.Printf("[regions] collision resolved to explicit %s over derived candidates %v", m.Name, m.Candidates)
	case scopeReasonAmbiguous:
		scopeMatchCounters.ambiguous.Add(1)
		log.Printf("[regions] ambiguous collision between %v; storing unmatched", m.Candidates)
	}
}

// logScopeMatchCounters prints the running tally. Called from the refresh
// ticker so the numbers arrive on the same cadence as the key-set changes that
// move them.
func logScopeMatchCounters() {
	log.Printf("[regions] scope matches: unique=%d explicit-over-derived=%d ambiguous=%d none=%d",
		scopeMatchCounters.unique.Load(), scopeMatchCounters.explicitOverDerived.Load(),
		scopeMatchCounters.ambiguous.Load(), scopeMatchCounters.none.Load())
}

// refreshFromStore reads the declared region names, ranks and caps them, and
// swaps in a new snapshot. Shared by startup and the ticker so both apply
// identical rules.
//
// A DB error is logged and the CURRENT snapshot is kept. That matters: an
// empty key set would silently unname all traffic, which looks exactly like
// the bug this feature exists to fix.
func (s *regionKeySet) refreshFromStore(store *Store) {
	if s == nil || !s.enabled {
		return
	}
	stats, err := store.declaredRegionSources()
	if err != nil {
		log.Printf("[regions] derived-key refresh failed, keeping %d existing key(s): %v", len(s.snapshot().all), err)
		return
	}
	ranked := rankDeclaredRegions(stats, s.max)
	added := s.refreshDerived(ranked)
	snap := s.snapshot()
	log.Printf("[regions] derived-key refresh: %d name(s) declared, %d kept after filter+cap(%d), %d total key(s) in force",
		len(stats), len(ranked), s.max, len(snap.all))
	if len(added) > 0 {
		log.Printf("[regions] derived keys now active: %v", added)
	}
	if len(stats) > s.max {
		log.Printf("[regions] NOTE: %d declared name(s) exceeded maxDerived=%d and were dropped, least-declared first", len(stats)-s.max, s.max)
	}
}
