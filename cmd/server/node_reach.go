package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/sync/singleflight"
)

// reachScanRowLimit hard-caps the windowed observation scan so a hot relay node
// with weeks of traffic can't pull an unbounded result set into memory. A node
// with >200k matching observations in the window is far past dashboard scale;
// beyond the cap the counts are a (still representative) truncation. The LIKE
// filter is unavoidably a text scan of path_json over the timestamp-narrowed
// window — an indexed path-token column would need an ingestor-side schema
// migration (the server is read-only by invariant), so it's a follow-up.
// var (not const) so tests can lower the cap to exercise the truncation path
// without inserting 200k rows.
var reachScanRowLimit = 200000

// pathRow is one observation fed to attributeDirections. path tokens are
// uppercase hex hop prefixes (as stored in observations.path_json). SNR is a
// value + validity flag (not *float64) to avoid a heap escape per row.
type pathRow struct {
	observerPK  string // lowercase pubkey of the observer (may be "")
	fromPubkey  string // lowercase originator pubkey (may be "")
	payloadType int
	path        []string
	snr         float64
	snrValid    bool
}

type obsAgg struct {
	count  int
	snrSum float64
	snrN   int
}

type dirCounts struct {
	we    map[string]int
	they  map[string]int
	obs   map[string]obsAgg // value map — no per-observer heap alloc
	relay int
}

// attributeDirections walks each path and attributes directional evidence for
// the target node (identified by any token in ourTokens). resolve maps a hop
// token → a unique relay pubkey ("" when ambiguous/unknown → skipped). ourPK is
// the target's own pubkey (lowercase) so self-edges are ignored.
func attributeDirections(rows []pathRow, ourTokens map[string]bool, ourPK string, resolve func(string) string) dirCounts {
	// Size hint: a small constant covers typical neighbour fan-out (dozens)
	// without over-allocating ~12.5k buckets on a 100k-row scan. Independent
	// r2 #4: the old `len(rows)/8+1` was ~250× too large for relays with
	// modest fan-out.
	const hint = 64
	d := dirCounts{
		we:   make(map[string]int, hint),
		they: make(map[string]int, hint),
		obs:  make(map[string]obsAgg, hint),
	}
	for _, r := range rows {
		n := len(r.path)
		if n == 0 {
			continue
		}
		hit := false
		for i, tok := range r.path {
			if !ourTokens[tok] {
				continue
			}
			hit = true
			// predecessor → we heard it
			if i > 0 {
				if pk := resolve(r.path[i-1]); pk != "" && pk != ourPK {
					d.we[pk]++
				}
			} else if r.payloadType == PayloadADVERT && r.fromPubkey != "" && r.fromPubkey != ourPK {
				d.we[r.fromPubkey]++
			}
			// successor → it heard us; or if we're the last hop, the observer did
			if i < n-1 {
				if pk := resolve(r.path[i+1]); pk != "" && pk != ourPK {
					d.they[pk]++
				}
			} else if r.observerPK != "" && r.observerPK != ourPK {
				d.they[r.observerPK]++
				a := d.obs[r.observerPK] // value copy; read-modify-write
				a.count++
				if r.snrValid {
					a.snrSum += r.snr
					a.snrN++
				}
				d.obs[r.observerPK] = a
			}
		}
		if hit {
			d.relay++
		}
	}
	return d
}

// reliableTokens returns the uppercase hex prefixes (1, 2, 3 byte) of pubkey
// that are UNIQUE among relay-capable nodes in pm AND resolve to pubkey itself.
// 1-byte prefixes almost always collide and are excluded. The self-check matters
// for non-relay targets (companion/sensor): pm only holds path-capable roles, so
// a companion's prefix could otherwise be "unique" while pointing at an unrelated
// relay — which would then credit that relay's traffic to the companion.
func reliableTokens(pubkey string, pm *prefixMap) map[string]bool {
	out := map[string]bool{}
	lpk := strings.ToLower(pubkey)
	for _, l := range []int{2, 4, 6} { // hex chars = 1,2,3 bytes
		if len(lpk) < l {
			continue
		}
		p := lpk[:l]
		if pm != nil && len(pm.m[p]) == 1 && strings.EqualFold(pm.m[p][0].PublicKey, pubkey) {
			out[strings.ToUpper(p)] = true
		}
	}
	return out
}

// uniqueResolve returns the single relay pubkey (lowercase) for a hop token, or
// "" when the token resolves to zero or multiple candidates (conservative).
// Callers should memoize across a request (see newResolver) so the per-hop
// ToLower + map lookup runs once per distinct token, not once per row.
func uniqueResolve(pm *prefixMap, token string) string {
	if pm == nil {
		return ""
	}
	cands := pm.m[strings.ToLower(token)]
	if len(cands) == 1 {
		return strings.ToLower(cands[0].PublicKey)
	}
	return ""
}

// parsePathTokens extracts the quoted hex hop tokens from a path_json array
// (e.g. `["AA","01FA","BB"]`) in a single pass, uppercased. Avoids the
// json.Unmarshal reflection + per-row interface allocations on the hot scan
// path. Tokens slice into pj (no copy) except where ToUpper must rewrite a
// lowercase hop; path_json holds only hex strings, so there are no escapes to
// worry about. Returns nil for an empty/degenerate array.
func parsePathTokens(pj string) []string {
	out := make([]string, 0, 8) // paths are short (a handful of hops)
	i := 0
	for {
		q1 := strings.IndexByte(pj[i:], '"')
		if q1 < 0 {
			break
		}
		q1 += i
		rel := strings.IndexByte(pj[q1+1:], '"')
		if rel < 0 {
			break
		}
		q2 := q1 + 1 + rel
		out = append(out, strings.ToUpper(pj[q1+1:q2]))
		i = q2 + 1
	}
	return out
}

// newResolver returns a memoized hop-token → pubkey resolver. Paths reuse the
// same hop tokens across thousands of rows, so caching collapses the repeated
// ToLower + prefix-map lookups to once per distinct token.
func newResolver(pm *prefixMap) func(string) string {
	cache := make(map[string]string)
	return func(tok string) string {
		if pk, ok := cache[tok]; ok {
			return pk
		}
		pk := uniqueResolve(pm, tok)
		cache[tok] = pk
		return pk
	}
}

// reachNodeScopes returns the node's inferred default_scope and, when an
// observer /neighbors report has confirmed it, the configured_scope and the
// instant that confirmation was reported (#1865).
//
// Both columns are optional: they are added by migration and an older database
// simply lacks them, so each is gated on the schema flag the handle probed at
// open time. Every return value is best-effort — a missing row, a missing
// column or a scan error yields empty strings, because a reach report must
// still render for a node we know nothing else about.
func (s *Server) reachNodeScopes(ctx context.Context, pubkey string) (string, string, string) {
	if s.db == nil || s.db.conn == nil {
		return "", "", ""
	}
	cols := make([]string, 0, 3)
	if s.db.hasDefaultScope {
		cols = append(cols, "default_scope")
	}
	if s.db.hasConfiguredScope {
		cols = append(cols, "configured_scope", "configured_scope_at")
	}
	if len(cols) == 0 {
		return "", "", ""
	}
	vals := make([]sql.NullString, len(cols))
	args := make([]interface{}, len(cols))
	for i := range vals {
		args[i] = &vals[i]
	}
	row := s.db.conn.QueryRowContext(ctx,
		"SELECT "+strings.Join(cols, ", ")+" FROM nodes WHERE public_key = ?", pubkey)
	if err := row.Scan(args...); err != nil {
		return "", "", ""
	}
	out := map[string]string{}
	for i, c := range cols {
		if vals[i].Valid {
			out[c] = vals[i].String
		}
	}
	return out["default_scope"], out["configured_scope"], out["configured_scope_at"]
}

type NodeReachInfo struct {
	Pubkey    string   `json:"pubkey"`
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
	FirstSeen string   `json:"first_seen"`
	// #1865: region scopes, so a printed reach report says which region the
	// node actually serves. DefaultScope is INFERRED from the transport scope
	// of observed adverts; ConfiguredScope is CONFIRMED, read back off the node
	// itself via an observer /neighbors report. Both omitempty: an instance
	// whose schema predates either column, or a node that has never been
	// confirmed, sends neither field and the UI shows nothing.
	DefaultScope      string `json:"default_scope,omitempty"`
	ConfiguredScope   string `json:"configured_scope,omitempty"`
	ConfiguredScopeAt string `json:"configured_scope_at,omitempty"`
}
type NodeReachWindow struct {
	Days  int    `json:"days"`
	Since string `json:"since"`
}
type NodeReachImportance struct {
	NeighborDegree     int `json:"neighbor_degree"`
	DegreeRank         int `json:"degree_rank"`
	NodesWithEdges     int `json:"nodes_with_edges"`
	RelayObservations  int `json:"relay_observations"`
	BidirectionalLinks int `json:"bidirectional_links"`
	DirectObservers    int `json:"direct_observers"`
}
type NodeReachObserver struct {
	Pubkey     string   `json:"pubkey"`
	Name       string   `json:"name"`
	Count      int      `json:"count"`
	AvgSNR     *float64 `json:"avg_snr"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	DistanceKm *float64 `json:"distance_km"`
}
type NodeReachLink struct {
	Pubkey     string   `json:"pubkey"`
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	WeHear     int      `json:"we_hear"`
	TheyHear   int      `json:"they_hear"`
	Bottleneck int      `json:"bottleneck"`
	Bidir      bool     `json:"bidir"`
	DistanceKm *float64 `json:"distance_km"`
}
type NodeReachResponse struct {
	Node            NodeReachInfo       `json:"node"`
	Window          NodeReachWindow     `json:"window"`
	ReliableTokens  []string            `json:"reliable_tokens"`
	Importance      NodeReachImportance `json:"importance"`
	DirectObservers []NodeReachObserver `json:"direct_observers"`
	Links           []NodeReachLink     `json:"links"`
}

func fptr(v float64) *float64 { return &v }

// gpsPtrs returns (lat,lon) pointers, nil when the node has no GPS.
func gpsPtrs(info nodeInfo) (*float64, *float64) {
	if !info.HasGPS {
		return nil, nil
	}
	return fptr(info.Lat), fptr(info.Lon)
}

// clampDays bounds the lookback window to [1,30]; default callers pass 7.
func clampDays(d int) int {
	if d < 1 {
		return 1
	}
	if d > 30 {
		return 30
	}
	return d
}

// --- bounded TTL cache. perf is gated by the time window; this just avoids
// recompute under dashboard polling. Keyed "pubkey|days". ---
//
// reachCacheMax bounds entry count; at ~2KB of marshalled JSON per entry the
// worst case is well under 1MB, so an entry cap (rather than a byte budget)
// keeps the bookkeeping trivial while staying memory-safe.
const (
	reachCacheTTL = 5 * time.Minute
	reachCacheMax = 256
)

type reachCacheEntry struct {
	at  time.Time
	raw []byte
}

// reachState bundles per-server reach caches. Was a set of package-level
// globals — moved onto *Server so two Server instances (tests, future
// per-listener) don't share observable state (Independent r2 #2).
type reachState struct {
	cacheMu sync.RWMutex
	cache   map[string]reachCacheEntry
	// sf dedups concurrent cold-cache requests for the same key so N
	// simultaneous callers run the scan + attribution once, not N times.
	sf singleflight.Group

	// lastSeenBlacklistGen is the BlacklistGeneration() value that the cache
	// was last reconciled with. When the live generation moves past this
	// value, the cache is purged wholesale on the next request to prevent
	// prior-gen entries from accumulating until their TTL expires (#1629
	// round-2, adversarial #5).
	lastSeenBlacklistGen atomic.Uint64

	degreeMu   sync.Mutex
	degreeSnap *degreeSnapshot
}

// reachCacheGet returns the cached marshalled JSON for key. The returned slice
// is shared (not copied): it is treated as immutable — only ever handed to
// w.Write — so callers MUST NOT mutate it.
func (s *Server) reachCacheGet(key string) ([]byte, bool) {
	s.reach.cacheMu.RLock()
	defer s.reach.cacheMu.RUnlock()
	e, ok := s.reach.cache[key]
	if !ok || time.Since(e.at) > reachCacheTTL {
		return nil, false
	}
	return e.raw, true
}

// reachCacheLen returns the current entry count in the reach response cache.
// Test helper — exposes the size without leaking the internal mutex/map.
func (s *Server) reachCacheLen() int {
	s.reach.cacheMu.RLock()
	defer s.reach.cacheMu.RUnlock()
	return len(s.reach.cache)
}

// reachPurgeIfBlacklistGenChanged drops every cached entry when the live
// blacklist generation has advanced past the cache's last-seen value. CAS
// gates the purge so concurrent callers only do the work once per gen bump
// (#1629 round-2, adversarial #5).
func (s *Server) reachPurgeIfBlacklistGenChanged(gen uint64) {
	seen := s.reach.lastSeenBlacklistGen.Load()
	if gen == seen {
		return
	}
	// CAS gates the actual purge to a single winner on a given gen bump.
	if !s.reach.lastSeenBlacklistGen.CompareAndSwap(seen, gen) {
		// Another goroutine already advanced (and purged). Done.
		return
	}
	s.reach.cacheMu.Lock()
	s.reach.cache = nil
	s.reach.cacheMu.Unlock()
}

// isHexPubkey reports whether s is a full 64-char lowercase-hex public key.
// The handler lowercases input first, so we only accept [0-9a-f].
func isHexPubkey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *Server) reachCachePut(key string, raw []byte) {
	s.reach.cacheMu.Lock()
	defer s.reach.cacheMu.Unlock()
	if s.reach.cache == nil {
		s.reach.cache = map[string]reachCacheEntry{}
	}
	if _, exists := s.reach.cache[key]; !exists && len(s.reach.cache) >= reachCacheMax {
		s.evictReachLocked()
	}
	s.reach.cache[key] = reachCacheEntry{at: time.Now(), raw: raw}
}

// evictReachLocked drops expired entries first; if still at the cap it evicts
// the single oldest entry. Avoids the full-map wipe that thrashed every cached
// key once the cap was reached. Caller holds s.reach.cacheMu (write).
func (s *Server) evictReachLocked() {
	now := time.Now()
	for k, e := range s.reach.cache {
		if now.Sub(e.at) > reachCacheTTL {
			delete(s.reach.cache, k)
		}
	}
	if len(s.reach.cache) < reachCacheMax {
		return
	}
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range s.reach.cache {
		if first || e.at.Before(oldestAt) {
			oldestKey, oldestAt, first = k, e.at, false
		}
	}
	if !first {
		delete(s.reach.cache, oldestKey)
	}
}

func (s *Server) handleNodeReach(w http.ResponseWriter, r *http.Request) {
	pubkey := strings.ToLower(mux.Vars(r)["pubkey"])
	// Reject malformed pubkeys up front (cheap defense against cache-key
	// pollution + wasted work on bogus IDs).
	if !isHexPubkey(pubkey) {
		writeError(w, 400, "invalid pubkey: expected 64 hex chars")
		return
	}
	if s.cfg != nil && s.cfg.IsBlacklisted(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	if s.isPubkeyHidden(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	days = clampDays(days)

	// cacheKey includes the blacklist generation so any mutation via
	// SetNodeBlacklist invalidates all prior reach cache entries on the
	// next request (#1629). Without the generation suffix a node added
	// to the blacklist post-warm would keep being served the cached
	// non-blacklisted response until the TTL expires.
	var gen uint64
	if s.cfg != nil {
		gen = s.cfg.BlacklistGeneration()
	}
	// Purge prior-gen entries wholesale when the generation advances so a
	// steady stream of operator blacklist edits cannot leak cache entries
	// up to the TTL. Cheap: one map reset under the cache mutex, only when
	// the gen actually moved (#1629 round-2, adversarial #5).
	s.reachPurgeIfBlacklistGenChanged(gen)
	cacheKey := pubkey + "|" + strconv.Itoa(days) + "|g" + strconv.FormatUint(gen, 10)
	if raw, ok := s.reachCacheGet(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
		return
	}

	// singleflight: collapse a thundering herd on a cold key to one scan. The
	// shared computation uses the triggering request's context; a disconnect
	// there can cancel the in-flight scan for all waiters (acceptable — the
	// next request recomputes).
	v, err, _ := s.reach.sf.Do(cacheKey, func() (interface{}, error) {
		if raw, ok := s.reachCacheGet(cacheKey); ok {
			return raw, nil
		}
		resp, ok, cErr := s.computeNodeReach(r.Context(), pubkey, days)
		if cErr != nil {
			// Real backend failure (e.g. DB scan exploded) — propagate so the
			// caller renders 500 instead of the misleading empty-reach
			// response. Do NOT cache. (#1631)
			return nil, cErr
		}
		if !ok {
			return []byte(nil), nil
		}
		raw, mErr := json.Marshal(resp)
		if mErr != nil {
			log.Printf("[reach] marshal failed for %s: %v", cacheKey, mErr)
			return nil, mErr
		}
		s.reachCachePut(cacheKey, raw)
		return raw, nil
	})
	if err != nil {
		writeError(w, 500, "reach computation failed")
		return
	}
	raw, _ := v.([]byte)
	if len(raw) == 0 {
		writeError(w, 404, "Not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// computeNodeReach does the read-only scan + assembly. ok=false → 404
// (target node not present / inputs unavailable). A non-nil error signals a
// real backend failure (e.g. DB scan exploded) — caller should render 500,
// not 404 (issue #1631).
func (s *Server) computeNodeReach(ctx context.Context, pubkey string, days int) (NodeReachResponse, bool, error) {
	if s.store == nil || s.db == nil || s.db.conn == nil {
		return NodeReachResponse{}, false, nil
	}
	nodeMap := s.buildNodeInfoMap()
	self, found := nodeMap[pubkey]
	if !found {
		return NodeReachResponse{}, false, nil
	}
	_, pm := s.store.getCachedNodesAndPM()
	tokens := reliableTokens(pubkey, pm)

	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	sinceEpoch := since.Unix()

	var d dirCounts
	if len(tokens) > 0 {
		rows, err := s.scanReachRows(ctx, tokens, sinceEpoch)
		if err != nil {
			return NodeReachResponse{}, false, err
		}
		d = attributeDirections(rows, tokens, pubkey, newResolver(pm))
	} else {
		d = dirCounts{we: map[string]int{}, they: map[string]int{}, obs: map[string]obsAgg{}}
	}

	// importance: neighbor_edges degree + rank (all-time). Served from a
	// coarse-TTL snapshot so the full UNION+GROUP-BY aggregate runs at most
	// once per snapshotTTL, not on every cache miss.
	degree, rank, nodesWithEdges := s.reachDegreeRank(ctx, pubkey)

	// node first_seen comes from nodeInfo (buildNodeInfoMap folds it in via a
	// single bulk SELECT). Missing → empty string (the node may be
	// observer-only or pre-first_seen-schema).
	firstSeen := self.FirstSeen

	// #1865: scopes for the report header. Single-row lookup rather than widening
	// buildNodeInfoMap's bulk SELECT, which every other consumer would pay for.
	defScope, confScope, confAt := s.reachNodeScopes(ctx, pubkey)

	// assemble links
	links := make([]NodeReachLink, 0, len(d.we)+len(d.they))
	bidir := 0
	seen := make(map[string]bool, len(d.we)+len(d.they))
	for pk := range d.we {
		seen[pk] = true
	}
	for pk := range d.they {
		seen[pk] = true
	}
	for pk := range seen {
		we, they := d.we[pk], d.they[pk]
		info := nodeMap[pk]
		lat, lon := gpsPtrs(info)
		var dist *float64
		if self.HasGPS && info.HasGPS {
			dist = fptr(haversineKm(self.Lat, self.Lon, info.Lat, info.Lon))
		}
		b := we > 0 && they > 0
		if b {
			bidir++
		}
		links = append(links, NodeReachLink{
			Pubkey: pk, Name: info.Name, Role: info.Role, Lat: lat, Lon: lon,
			WeHear: we, TheyHear: they, Bottleneck: min(we, they), Bidir: b, DistanceKm: dist,
		})
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Bidir != links[j].Bidir {
			return links[i].Bidir
		}
		if links[i].Bottleneck != links[j].Bottleneck {
			return links[i].Bottleneck > links[j].Bottleneck
		}
		return links[i].WeHear+links[i].TheyHear > links[j].WeHear+links[j].TheyHear
	})

	// direct observers
	directObs := make([]NodeReachObserver, 0, len(d.obs))
	for pk, a := range d.obs {
		info := nodeMap[pk]
		lat, lon := gpsPtrs(info)
		var avg, dist *float64
		if a.snrN > 0 {
			avg = fptr(a.snrSum / float64(a.snrN))
		}
		if self.HasGPS && info.HasGPS {
			dist = fptr(haversineKm(self.Lat, self.Lon, info.Lat, info.Lon))
		}
		directObs = append(directObs, NodeReachObserver{
			Pubkey: pk, Name: info.Name, Count: a.count, AvgSNR: avg, Lat: lat, Lon: lon, DistanceKm: dist,
		})
	}
	sort.Slice(directObs, func(i, j int) bool { return directObs[i].Count > directObs[j].Count })

	toks := make([]string, 0, len(tokens))
	for t := range tokens {
		toks = append(toks, t)
	}
	sort.Strings(toks)

	selfLat, selfLon := gpsPtrs(self)
	return NodeReachResponse{
		Node: NodeReachInfo{Pubkey: pubkey, Name: self.Name, Role: self.Role,
			Lat: selfLat, Lon: selfLon, FirstSeen: firstSeen,
			DefaultScope: defScope, ConfiguredScope: confScope, ConfiguredScopeAt: confAt},
		Window:         NodeReachWindow{Days: days, Since: since.Format(time.RFC3339)},
		ReliableTokens: toks,
		Importance: NodeReachImportance{
			NeighborDegree: degree, DegreeRank: rank, NodesWithEdges: nodesWithEdges,
			RelayObservations: d.relay, BidirectionalLinks: bidir, DirectObservers: len(directObs),
		},
		DirectObservers: directObs,
		Links:           links,
	}, true, nil
}

// --- neighbor-degree snapshot ---------------------------------------------
// The degree/rank importance is identical across all reach requests except the
// pubkey match, so the full neighbor_edges aggregate is computed once and shared
// behind a coarse TTL. Rank is a binary search over the descending degree list.
const reachDegreeTTL = 60 * time.Second

type degreeSnapshot struct {
	at         time.Time
	total      int            // nodes that have any edge
	deg        map[string]int // lowercase pubkey → neighbour count
	sortedDesc []int          // degrees sorted descending, for rank
}

func (s *Server) reachDegreeRank(ctx context.Context, pubkey string) (degree, rank, total int) {
	snap := s.getDegreeSnapshot(ctx)
	if snap == nil {
		return 0, 0, 0
	}
	degree = snap.deg[pubkey]
	if degree == 0 {
		// No edges → not ranked. rank=0 is the documented "off-the-list" value;
		// avoids the nonsensical "#N+1 / N" the binary search would produce.
		return 0, 0, snap.total
	}
	// rank = 1 + (number of nodes with strictly higher degree). sortedDesc is
	// descending, so the count of entries > degree is the first index whose
	// value is <= degree.
	rank = 1 + sort.Search(len(snap.sortedDesc), func(i int) bool { return snap.sortedDesc[i] <= degree })
	return degree, rank, snap.total
}

func (s *Server) getDegreeSnapshot(ctx context.Context) *degreeSnapshot {
	// Fast path: serve a fresh snapshot under a short lock.
	s.reach.degreeMu.Lock()
	if s.reach.degreeSnap != nil && time.Since(s.reach.degreeSnap.at) < reachDegreeTTL {
		snap := s.reach.degreeSnap
		s.reach.degreeMu.Unlock()
		return snap
	}
	stale := s.reach.degreeSnap
	s.reach.degreeMu.Unlock()

	// Rebuild WITHOUT holding the lock so concurrent reach requests aren't
	// serialized behind the aggregate query. A brief cold-start herd may run a
	// few redundant queries; the last writer wins.
	rows, err := s.db.conn.QueryContext(ctx, `
		SELECT pk, COUNT(*) neigh FROM (
			SELECT node_a pk FROM neighbor_edges
			UNION ALL SELECT node_b FROM neighbor_edges
		) GROUP BY pk`)
	if err != nil {
		log.Printf("[reach] degree snapshot query failed: %v (serving stale)", err)
		return stale // serve stale on error rather than zeroing
	}
	defer rows.Close()
	deg := make(map[string]int)
	var sortedDesc []int
	for rows.Next() {
		var pk string
		var neigh int
		if rows.Scan(&pk, &neigh) != nil {
			continue
		}
		deg[strings.ToLower(pk)] = neigh
		sortedDesc = append(sortedDesc, neigh)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sortedDesc)))
	snap := &degreeSnapshot{at: time.Now(), total: len(deg), deg: deg, sortedDesc: sortedDesc}
	s.reach.degreeMu.Lock()
	s.reach.degreeSnap = snap
	s.reach.degreeMu.Unlock()
	return snap
}

// scanReachRows reads windowed observations whose path contains any reliable
// token, with the originator + observer + snr needed for attribution. Observer
// id and originator pubkey are lowercased in SQL (not per row), the path slice
// is uppercased in place (no second allocation), and the result is hard-capped
// at reachScanRowLimit.
//
// Returns a non-nil error if the underlying QueryContext or rows.Err() fails;
// callers MUST treat that as a 500 (issue #1631 — previously the error was
// swallowed, surfacing a transient DB failure as a misleading 404 / empty
// reach to operators).
func (s *Server) scanReachRows(ctx context.Context, tokens map[string]bool, sinceEpoch int64) ([]pathRow, error) {
	if len(tokens) == 0 {
		return nil, nil // defensive: an empty LIKE chain would render `AND ()` (SQL error)
	}
	likes := make([]string, 0, len(tokens))
	args := []interface{}{sinceEpoch}
	// Sort tokens so the generated SQL text is byte-stable across requests
	// with the same token set — preserves the driver's prepared-statement
	// cache and keeps query plans reproducible (Independent r2 #3).
	toks := make([]string, 0, len(tokens))
	for tok := range tokens {
		toks = append(toks, tok)
	}
	sort.Strings(toks)
	for _, tok := range toks {
		likes = append(likes, "o.path_json LIKE ?")
		args = append(args, "%\""+tok+"\"%")
	}
	q := `SELECT LOWER(COALESCE(obs.id,'')), LOWER(COALESCE(t.from_pubkey,'')), COALESCE(t.payload_type,0), o.path_json, o.snr
	      FROM observations o
	      JOIN transmissions t ON t.id = o.transmission_id
	      LEFT JOIN observers obs ON obs.rowid = o.observer_idx
	      WHERE o.timestamp >= ? AND (` + strings.Join(likes, " OR ") + `)
	      LIMIT ?`
	args = append(args, reachScanRowLimit)
	rows, err := s.db.conn.QueryContext(ctx, q, args...)
	if err != nil {
		log.Printf("[reach] scan query failed: %v", err)
		return nil, err
	}
	defer rows.Close()
	// Modest preallocation: most nodes return far fewer than the cap, so seed a
	// reasonable capacity rather than reserving reachScanRowLimit up front.
	out := make([]pathRow, 0, 2048)
	var skipped int // malformed/empty rows discarded — surfaced below so ingest bugs aren't silent
	for rows.Next() {
		var oid, fpk, pj string
		var pt int
		var snr sql.NullFloat64
		if err := rows.Scan(&oid, &fpk, &pt, &pj, &snr); err != nil {
			skipped++
			continue
		}
		path := parsePathTokens(pj)
		if len(path) == 0 {
			skipped++
			continue
		}
		pr := pathRow{observerPK: oid, fromPubkey: fpk, payloadType: pt, path: path}
		if snr.Valid {
			pr.snr = snr.Float64
			pr.snrValid = true
		}
		out = append(out, pr)
	}
	if skipped > 0 {
		log.Printf("[reach] scan discarded %d malformed/empty rows (kept %d)", skipped, len(out))
	}
	if err := rows.Err(); err != nil {
		log.Printf("[reach] scan rows iteration failed: %v", err)
		return nil, err
	}
	return out, nil
}
