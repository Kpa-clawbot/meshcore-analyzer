package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/meshcore-analyzer/mbcapqueue"
	"golang.org/x/sync/singleflight"
)

// payloadTypeNames maps payload_type int → human-readable name (firmware-standard).
// Must stay in sync with the canonical map in cmd/ingestor/decoder.go and
// cmd/server/decoder.go. Source of truth: firmware/src/Packet.h:19-32.
var payloadTypeNames = map[int]string{
	0: "REQ", 1: "RESPONSE", 2: "TXT_MSG", 3: "ACK", 4: "ADVERT",
	5: "GRP_TXT", 6: "GRP_DATA", 7: "ANON_REQ", 8: "PATH", 9: "TRACE",
	10: "MULTIPART", 11: "CONTROL", 15: "RAW_CUSTOM",
}

// StoreTx is an in-memory transmission with embedded observations.
type StoreTx struct {
	ID          int
	RawHex      string
	Hash        string
	FirstSeen   string
	RouteType   *int
	PayloadType *int
	DecodedJSON string
	// ScopeName is the transmission's region scope name (transmissions.scope_name,
	// #899). nil means the row is not transport-scoped (SQL NULL), or the schema
	// has no such column (db.hasScopeName=false); a pointer to "" means
	// transport-scoped with an unmatched region. Used to surface the set of region
	// scopes a repeater has transported (#1751) and the packet-detail Scope row.
	ScopeName        *string
	Observations     []*StoreObs
	ObservationCount int
	// Display fields from longest-path observation
	ObserverID          string
	ObserverName        string
	ObserverIATA        string
	SNR                 *float64
	RSSI                *float64
	PathJSON            string
	Direction           string
	LatestSeen          string // max observation timestamp (or FirstSeen if no observations)
	UniqueObserverCount int    // cached count of distinct observer IDs
	// Cached parsed fields (set once, read many)
	parsedPath    []string               // cached parsePathJSON result
	pathParsed    bool                   // whether parsedPath has been set
	decodedOnce   sync.Once              // guards parsedDecoded
	parsedDecoded map[string]interface{} // cached json.Unmarshal of DecodedJSON
	// Dedup map: "observerID|pathJSON" → true for O(1) duplicate checks
	obsKeys     map[string]bool
	observerSet map[string]bool // unique observer IDs (for UniqueObserverCount)
}

// StoreObs is a lean in-memory observation (no duplication of transmission fields).
type StoreObs struct {
	ID             int
	TransmissionID int
	ObserverID     string
	ObserverName   string
	ObserverIATA   string
	Direction      string
	SNR            *float64
	RSSI           *float64
	Score          *int
	PathJSON       string
	RawHex         string
	Timestamp      string

	// #1481 P0-2: cached parsed timestamp. The legacy handlers parse
	// Timestamp as RFC3339Nano/RFC3339/"2006-01-02 15:04:05" with a
	// fallback chain on every read; for /api/observers/{id}/analytics
	// that fires 60k+ times per request under RLock. ParsedTime returns
	// the parsed value once, caching for the lifetime of the StoreObs.
	tsParseOnce sync.Once
	tsParsed    time.Time
	tsParsedOK  bool
}

// ParsedTime returns the parsed Timestamp, caching the result.
// Tries RFC3339Nano → RFC3339 → "2006-01-02 15:04:05" in order.
// Returns (zero, false) if Timestamp is empty or unparseable.
// Thread-safe via sync.Once.
func (o *StoreObs) ParsedTime() (time.Time, bool) {
	o.tsParseOnce.Do(func() {
		if o.Timestamp == "" {
			return
		}
		if t, err := time.Parse(time.RFC3339Nano, o.Timestamp); err == nil {
			o.tsParsed, o.tsParsedOK = t, true
			return
		}
		if t, err := time.Parse(time.RFC3339, o.Timestamp); err == nil {
			o.tsParsed, o.tsParsedOK = t, true
			return
		}
		if t, err := time.Parse("2006-01-02 15:04:05", o.Timestamp); err == nil {
			o.tsParsed, o.tsParsedOK = t, true
			return
		}
	})
	return o.tsParsed, o.tsParsedOK
}

// ParsedDecoded returns the parsed DecodedJSON map, caching the result.
// Thread-safe via sync.Once — the first call parses, subsequent calls return cached.
func (tx *StoreTx) ParsedDecoded() map[string]interface{} {
	tx.decodedOnce.Do(func() {
		if tx.DecodedJSON != "" {
			json.Unmarshal([]byte(tx.DecodedJSON), &tx.parsedDecoded)
		}
	})
	return tx.parsedDecoded
}

// PacketStore holds all transmissions in memory with indexes for fast queries.
//
// Lock ordering
// =============
// PacketStore uses several mutexes. To prevent deadlocks, locks MUST be
// acquired in the order listed below. Never acquire a higher-numbered lock
// while holding a lower-numbered one.
//
//  1. mu            (sync.RWMutex) — guards the core packet data: packets,
//     indexes (byHash, byTxID, byObsID, byObserver, byNode,
//     byPathHop, byPayloadType), counters, and loaded flag.
//
//  2. cacheMu       (sync.Mutex)  — guards analytics response caches:
//     rfCache, topoCache, hashCache, collisionCache, chanCache,
//     distCache, subpathCache, and their TTLs/hit counters.
//     Also guards rate-limited invalidation state
//     (lastInvalidated, pendingInv).
//
//  3. channelsCacheMu (sync.Mutex) — guards the short-lived GetChannels
//     cache (channelsCacheKey/Exp/Res).
//
//  4. groupedCacheMu (sync.Mutex)  — guards the short-lived
//     QueryGroupedPackets cache.
//
//  5. regionObsMu   (sync.Mutex)  — guards the region→observer mapping
//     cache (regionObsCache, regionObsCacheTime).
//
//  6. hashSizeInfoMu (sync.Mutex)  — guards the cached hash-size-info
//     result (hashSizeInfoCache). Acquired independently or
//     under mu (in EvictStale).
//
// Nesting that occurs today:
//   - IngestNew:               mu → cacheMu → channelsCacheMu  (1 → 2 → 3, OK)
//   - IngestObservations:      mu → cacheMu                    (1 → 2, OK)
//   - RunEviction/EvictStale:  mu → cacheMu → channelsCacheMu  (1 → 2 → 3, OK)
//   - RunEviction/EvictStale:  mu → hashSizeInfoMu             (1 → 6, OK)
//   - invalidateCachesFor:     cacheMu → channelsCacheMu       (2 → 3, OK)
//
// All other locks are acquired independently (no nesting).
// When adding new lock acquisitions, respect this ordering.
type PacketStore struct {
	mu            sync.RWMutex
	db            *DB
	packets       []*StoreTx                 // sorted by first_seen ASC (oldest first; newest at tail)
	byHash        map[string]*StoreTx        // hash → *StoreTx
	byTxID        map[int]*StoreTx           // transmission_id → *StoreTx
	byObsID       map[int]*StoreObs          // observation_id → *StoreObs
	maxTxID       int                        // highest transmission_id in store
	maxObsID      int                        // highest observation_id in store
	byObserver    map[string][]*StoreObs     // observer_id → observations
	byNode        map[string][]*StoreTx      // pubkey → transmissions
	nodeHashes    map[string]map[string]bool // pubkey → Set<hash>
	byPathHop     map[string][]*StoreTx      // lowercase hop/pubkey → transmissions with that hop in path
	byPayloadType map[int][]*StoreTx         // payload_type → transmissions
	loaded        bool
	totalObs      int
	insertCount   int64
	queryCount    int64
	// Response caches (separate mutex to avoid contention with store RWMutex)
	cacheMu           sync.RWMutex
	rfCache           map[string]*cachedResult // region → cached RF result
	topoCache         map[string]*cachedResult // region → cached topology result
	hashCache         map[string]*cachedResult // region → cached hash-sizes result
	collisionCache    map[string]*cachedResult // cached hash-collisions result keyed by region ("" = global)
	chanCache         map[string]*cachedResult // region → cached channels result
	distCache         map[string]*cachedResult // region → cached distance result
	subpathCache      map[string]*cachedResult // params → cached subpaths result
	rfCacheTTL        time.Duration
	collisionCacheTTL time.Duration
	// Steady-state analytics recomputers (issue #1240). Each holds the
	// latest snapshot for the default region="" / zero-window query of
	// an analytics endpoint in an atomic.Value, refreshed by a
	// background goroutine on a fixed interval. When set, the matching
	// GetAnalytics* function serves from Load() instead of running the
	// on-request compute path. Region/window variants still go through
	// the legacy TTL cache (compute-on-miss).
	analyticsRecomputerMu    sync.RWMutex
	recompTopology           *analyticsRecomputer
	recompRF                 *analyticsRecomputer
	recompDistance           *analyticsRecomputer
	recompChannels           *analyticsRecomputer
	recompHashCollisions     *analyticsRecomputer
	recompHashSizes          *analyticsRecomputer
	recompRoles              *analyticsRecomputer
	recompObserversClockSkew *analyticsRecomputer
	recompNodesClockSkew     *analyticsRecomputer
	cacheHits                int64
	cacheMisses              int64
	// Rate-limited invalidation (fixes #533: caches cleared faster than hit)
	lastInvalidated time.Time
	pendingInv      *cacheInvalidation // accumulated dirty flags during cooldown
	invCooldown     time.Duration      // minimum time between invalidations
	// Short-lived cache for QueryGroupedPackets (avoids repeated full sort)
	groupedCacheMu    sync.Mutex
	groupedCacheKey   string
	groupedCacheExp   time.Time
	groupedCacheTxs   []*StoreTx // sorted by LatestSeen DESC
	groupedCacheTotal int
	// Short-lived cache for GetChannels (avoids repeated full scan + JSON unmarshal)
	channelsCacheMu  sync.Mutex
	channelsCacheKey string
	channelsCacheExp time.Time
	channelsCacheRes []map[string]interface{}
	// Cached region → observer ID mapping (30s TTL, avoids repeated DB queries)
	regionObsMu        sync.Mutex
	regionObsCache     map[string]map[string]bool
	regionObsCacheTime time.Time
	// Cached area key → node pubkey set (30s per-key TTL)
	areaNodeMu         sync.RWMutex
	areaNodeCache      map[string]map[string]bool
	areaNodeCacheTimes map[string]time.Time
	// Full server config — needed for Areas map in resolveAreaNodes.
	config *Config
	// Cached node list + prefix map (rebuilt on demand, shared across analytics)
	nodeCache     []nodeInfo
	nodePM        *prefixMap
	nodeCacheTime time.Time
	// Per-store dedupe set for one-shot schema-degradation warnings. Field
	// (not package-level) so each test gets a fresh state — see #1199 item 5.
	schemaDegradationLogged sync.Map
	// Precomputed subpath index: raw comma-joined hops → occurrence count.
	// Built during Load(), incrementally updated on ingest. Avoids full
	// packet iteration at query time (O(unique_subpaths) vs O(total_packets)).
	spIndex      map[string]int        // "hop1,hop2" → count
	spTxIndex    map[string][]*StoreTx // "hop1,hop2" → transmissions containing this subpath
	spTotalPaths int                   // transmissions with paths >= 2 hops
	// Background-build ready gates for spIndex/spTxIndex and byPathHop
	// (#1008). Flipped from false→true exactly once by the goroutine
	// kicked off in Load() (or synchronously by the background chunk
	// loader). Handlers gate reads via SubpathIndexReady() /
	// PathHopIndexReady(); while false, they respond 503 + Retry-After.
	subpathReady atomic.Bool
	pathHopReady atomic.Bool
	// indexReadyChan is closed exactly once when BOTH subpathReady
	// and pathHopReady are true (#1008 review m6). Replaces the
	// previous 2ms poll in WaitIndexesReady. Lazily allocated by
	// indexReadyCh / maybeCloseIndexReadyCh in index_ready_1008.go.
	indexReadyChMu sync.Mutex
	indexReadyChan chan struct{}
	// Precomputed distance analytics: hop distances and path totals.
	// Built LAZILY on first /api/analytics/distance request (#1011) —
	// previously eager in Load() at startup, which was O(n²) work for
	// operators who never visit the distance analytics page. After the
	// initial lazy build, the in-memory index is maintained
	// incrementally by updateDistanceIndexForTxs on ingest.
	distHops  []distHopRecord
	distPaths []distPathRecord

	// Lazy-build gate for the distance index (#1011). distLazyBuilt is
	// true once the first /api/analytics/distance request has completed
	// its build (or a debounced rebuild). distLazyBuilding is true
	// while a build is currently running — concurrent requests in this
	// window receive 202 + Retry-After rather than racing N parallel
	// O(n²) computations. distLazyOnce serialises the first build;
	// reset by the background loader (Load() chunked merge) and by the
	// debounced-rebuild policy so subsequent rebuilds can re-fire.
	distLazyMu        sync.Mutex
	distLazyOnce      sync.Once
	distLazyBuilt     bool
	distLazyBuilding  bool
	distLazyLastBuilt time.Time
	distLazyLastObs   int // totalObs at last build, for Δobs debounce
	// distanceBuildHook, if non-nil, runs at the start of the lazy build
	// goroutine (after distLazyBuilding is set, before any lock is held). Tests
	// use it to hold the build open so concurrent requests deterministically
	// observe the "building" window; nil (and zero overhead) in production.
	distanceBuildHook func()

	// Cached GetNodeHashSizeInfo result — recomputed at most once every 15s
	hashSizeInfoMu    sync.Mutex
	hashSizeInfoCache map[string]*hashSizeNodeInfo
	hashSizeInfoAt    time.Time

	// Cached relay stats batch result — recomputed at most once every 300s
	// or when byPathHop changes (see invalidateRelayStatsCache).
	relayStatsCacheMu     sync.Mutex
	relayStatsCache       map[string]RepeaterNodeStats
	relayStatsCacheAt     time.Time
	relayStatsCacheWindow float64
	relayStatsCacheSig    string

	// Snapshot from the last analytics cycle + O(1) index, both under cacheMu.
	// Populated by analytics + pre-populated from DB on Load (read-only path).
	// Persistence to the DB is owned by the ingestor (#1289/#1324): the
	// analytics cycle publishes a snapshot file via internal/mbcapqueue
	// and the ingestor's RunMultibyteCapPersist applies it.
	mbCapSnapshot []MultiByteCapEntry
	mbCapIndex    map[string]MultiByteCapEntry

	// Cached per-pubkey relay info + usefulness score maps (#1257). These
	// fold the previously per-node GetRepeaterRelayInfo /
	// GetRepeaterUsefulnessScore loop in handleNodes into one O(N) pass
	// per 15s TTL window — eliminating N RLock acquisitions and N×
	// timestamp parses of the same byPathHop entries per request.
	repeaterEnrichMu      sync.Mutex
	repeaterRelayCache    map[string]RepeaterRelayInfo
	repeaterRelayCacheWin float64
	repeaterRelayAt       time.Time
	repeaterUsefulCache   map[string]float64
	repeaterUsefulAt      time.Time

	// Steady-state recomputer for the two caches above (#1262). When
	// started, an initial sync compute prewarms the caches so the very
	// first /api/nodes?limit=2000 from live.js's SPA bootstrap hits a
	// populated cache instead of paying the 15.7s on-thread rebuild.
	repeaterEnrichRecompMu      sync.Mutex
	repeaterEnrichRecompStarted bool
	repeaterEnrichRecompStop    chan struct{}
	repeaterEnrichRecompDone    chan struct{}

	// Bridge axis (issue #672 axis 2 of 4): atomic snapshot of pubkey
	// → 0..1 betweenness-centrality score over the current neighbor
	// graph. Populated by the bridge recomputer (bridge_recomputer.go);
	// nil until the first compute lands. Read path is a single atomic
	// pointer load — no lock contention with the per-request enrichment
	// path in handleNodes (same discipline as #1248).
	bridgeScoreMap atomic.Pointer[map[string]float64]

	// Coverage + Redundancy axes (issue #672 axes 3 & 4 of 4): atomic
	// snapshots of pubkey → 0..1 score over the current neighbor graph.
	// Coverage = normalized harmonic reach centrality; Redundancy =
	// articulation-point fragmentation criticality. Populated by the
	// usefulness-axes recomputer (usefulness_axes_recomputer.go); nil until
	// the first compute lands. Read path is a single atomic pointer load,
	// matching the bridge axis.
	coverageScoreMap   atomic.Pointer[map[string]float64]
	redundancyScoreMap atomic.Pointer[map[string]float64]

	// Start-once latch for the usefulness-axes recomputer
	// (usefulness_axes_recomputer.go). Per-store rather than package-global so
	// independent PacketStores each run their own recomputer; guards the
	// idempotent StartUsefulnessAxesRecomputer (a second call no-ops).
	usefulnessAxesRecompMu      sync.Mutex
	usefulnessAxesRecompStarted bool

	// Precomputed distinct advert pubkey count (refcounted for eviction correctness).
	// Updated incrementally during Load/Ingest/Evict — avoids JSON parsing in GetPerfStoreStats.
	advertPubkeys map[string]int // pubkey → number of advert packets referencing it

	// Resolved path membership index: xxhash → []txID (forward) and txID → []hashes (reverse).
	// Replaces per-StoreTx/StoreObs ResolvedPath []*string field (#800).
	resolvedPubkeyIndex           map[uint64][]int  // hash(pubkey) → []txID
	resolvedPubkeyReverse         map[int][]uint64  // txID → []hashes indexed under
	useResolvedPathIndex          bool              // feature flag (default true, off path = conservative)
	maxResolvedPubkeyIndexEntries int               // hard cap for size warning (0 = use default 5M)
	apiResolvedPathLRU            map[int][]*string // obsID → resolved path (LRU cache for API)
	lruOrder                      []int             // FIFO order for LRU eviction
	lruMu                         sync.RWMutex      // guards apiResolvedPathLRU + lruOrder

	// Persisted neighbor graph for hop resolution at ingest time.
	// Accessed via atomic.Pointer because async rebuilds (path_inspect.go
	// ensureNeighborGraph) and ingest-time readers race on the pointer
	// (issue #1203 sub-fix).
	graph atomic.Pointer[NeighborGraph]

	// Singleflight state for ensureNeighborGraph. These were package-globals
	// in #1203 r0 — moved to per-store fields (PR #1208 review) so parallel
	// tests with independent *PacketStore values don't share rebuild state
	// (cross-store deadlock/skip risk under -race).
	rebuildMu    sync.Mutex
	rebuildInFlt chan struct{} // nil when no rebuild is in flight

	// Path inspector score cache (issue #944).
	inspectMu    sync.RWMutex
	inspectCache map[string]*inspectCachedResult

	// Clock skew detection engine.
	clockSkew *ClockSkewEngine

	// Bounded cold load: oldest packet timestamp loaded into memory.
	// Empty string means all data is in memory (no limit applied).
	oldestLoaded string

	// Hot startup atomic gates — see contract below.
	//
	// Contract / ordering invariant (PR #1187):
	//   * hashMigrationComplete (set by migrateContentHashesAsync) gates
	//     content-hash–dependent code paths (e.g. dedup correctness on the
	//     write side). Set true ONLY after the migration loop finishes.
	//   * backgroundLoadDone is set true exactly once, after
	//     loadBackgroundChunks finishes its loop AND its post-load index
	//     rebuild. It gates "hot startup has finished filling
	//     retentionHours of data into memory" (used by /api/perf and the
	//     UI's hot-load banner). It says nothing about success — see
	//     backgroundLoadFailed for the success/failure signal.
	//   * backgroundLoadFailed is set true ONLY if at least one chunk
	//     errored during background load. /api/perf surfaces it so
	//     operators can distinguish "done & full" from "done & partial".
	//     Read order MUST be: load backgroundLoadDone first; only if true
	//     is backgroundLoadFailed meaningful.
	// 0 = disabled (current behavior). Background loader fills the rest.
	//
	// IMMUTABILITY: hotStartupHours is set ONCE in NewPacketStore (with
	// optional clamp against retentionHours) and is NEVER mutated
	// afterward. Readers (LoadChunked, RunStartupLoad,
	// loadBackgroundChunks, GetPerfStoreStats) intentionally read it
	// without s.mu. Do not add a write path without also adding the
	// lock to every reader — see #1809 / #1811.
	//
	// ENFORCEMENT (dij #5): immutability is enforced indirectly. A
	// mutation that left oldestLoaded inconsistent with the new
	// hotStartupHours would trip the loadBackgroundChunks invariant
	// (see store.go ~line 1442 — the invariantViolation() guard).
	// That guard is the runtime backstop; this comment is the
	// compile-time discipline. If you genuinely need to mutate
	// hotStartupHours at runtime, audit every reader above first.
	hotStartupHours        float64
	backgroundLoadDone     atomic.Bool
	backgroundLoadFailed   atomic.Bool
	backgroundLoadProgress atomic.Int64 // 0–100 percent complete

	// #1690: backgroundLoadError captures the human-readable reason when
	// backgroundLoadFailed flips true (e.g. "loaded 12.3% of 1000 rows").
	// Guarded by bgErrMu (RWMutex) so the perf endpoint can read it
	// without synchronising on s.mu (held by chunk-merge writers).
	// CONCURRENCY (dij #4): EVERY read and write of backgroundLoadErr
	// MUST hold bgErrMu (RLock for reads, Lock for writes). Do not
	// promote to atomic.Pointer without also auditing the two call
	// sites in chunked_load.go's RunStartupLoad and store.go's
	// loadBackgroundChunks + GetPerfStoreStats + BackgroundLoadError.
	bgErrMu           sync.RWMutex
	backgroundLoadErr string
	// loadCoverageRatio: totalLoaded / totalInDB (0.0–1.0). Updated by
	// loadBackgroundChunks after the chunk walk; surfaced via the typed
	// perf payload for prod observability.
	loadCoverageRatio float64

	// Async hash migration state: set after migrateContentHashesAsync completes.
	hashMigrationComplete atomic.Bool

	// Chunked startup load state (#1009). LoadChunked closes
	// firstChunkReady after the first chunk is merged so the HTTP
	// listener can bind. loadComplete flips true after all chunks have
	// been processed; loadProgressRows is updated per-chunk so
	// loadStatusMiddleware can emit progress.
	chunkInitOnce      sync.Once
	firstChunkReady    chan struct{}
	firstChunkSignaled atomic.Bool
	loadComplete       atomic.Bool
	loadProgressRows   atomic.Int64
	chunkCBMu          sync.Mutex
	chunkCallbacks     []func(rowsThisChunk, totalRows int)

	// Eviction config and stats
	retentionHours  float64        // 0 = unlimited
	maxMemoryMB     int            // 0 = unlimited (packet store memory budget)
	evicted         int64          // total packets evicted
	trackedBytes    int64          // running total of estimated packet store memory
	memoryEstimator func() float64 // injectable for tests; nil = use runtime.ReadMemStats (stats only)

	// Per-store ReadMemStats cache (5s TTL). Fields (not package-level vars) so
	// that test helpers constructing &PacketStore{...} directly get independent
	// cache state, avoiding order-dependent test failures.
	estMemMu  sync.Mutex
	estMemVal float64
	estMemAt  time.Time

	// Short-lived cache for the observations aggregate in GetStoreStats (30s TTL).
	// Avoids a per-/api/stats full-table scan; values accurate to ~30s which is
	// sufficient for dashboard display.
	statsCacheMu   sync.Mutex
	statsCacheTime time.Time
	statsLastHour  int
	statsLast24h   int
	// #1910: collapses concurrent misses onto one query. Without it every
	// in-flight request ran the observations scan itself the moment the cache
	// expired, because the check above releases statsCacheMu before doing the
	// work. With SetMaxOpenConns(4) and a page that fires five endpoints at
	// once, that turned one scan into a queue of them.
	statsSF singleflight.Group

	// Test-only hook fired at the very start of loadBackgroundChunks
	// (after the #1809 invariant check). Nil in production. Used by
	// ordering tests to capture the bg-loader entry timestamp/signal
	// without polling. See runstartup_load_test.go.
	bgLoaderEntryHook func()
}

// Precomputed distance records for fast analytics aggregation.
type distHopRecord struct {
	FromName   string
	FromPk     string
	ToName     string
	ToPk       string
	Dist       float64
	Type       string // "R↔R", "C↔R", "C↔C"
	SNR        *float64
	Hash       string
	Timestamp  string
	HourBucket string
	tx         *StoreTx
}

// dedupeHopsByPair groups hops by unordered node-pair, keeps the max-distance
// record per pair, and computes obsCount / bestSnr / medianSnr.  limit caps the
// number of returned entries (sorted by distance descending).
func dedupeHopsByPair(hops []distHopRecord, limit int) []map[string]interface{} {
	type pairAgg struct {
		best     *distHopRecord
		obsCount int
		maxSNR   *float64
		snrs     []float64
	}
	pairMap := make(map[string]*pairAgg)
	for i := range hops {
		h := &hops[i]
		pk1, pk2 := h.FromPk, h.ToPk
		if pk1 > pk2 {
			pk1, pk2 = pk2, pk1
		}
		key := pk1 + "|" + pk2
		agg, ok := pairMap[key]
		if !ok {
			agg = &pairAgg{}
			pairMap[key] = agg
		}
		agg.obsCount++
		if h.SNR != nil {
			agg.snrs = append(agg.snrs, *h.SNR)
			if agg.maxSNR == nil || *h.SNR > *agg.maxSNR {
				v := *h.SNR
				agg.maxSNR = &v
			}
		}
		if agg.best == nil || h.Dist > agg.best.Dist {
			agg.best = h
		}
	}
	type pairEntry struct {
		key string
		agg *pairAgg
	}
	pairs := make([]pairEntry, 0, len(pairMap))
	for k, v := range pairMap {
		pairs = append(pairs, pairEntry{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].agg.best.Dist > pairs[j].agg.best.Dist })
	result := make([]map[string]interface{}, 0, min(limit, len(pairs)))
	for i, pe := range pairs {
		if i >= limit {
			break
		}
		h := pe.agg.best
		var medianSNR *float64
		if len(pe.agg.snrs) > 0 {
			sorted := make([]float64, len(pe.agg.snrs))
			copy(sorted, pe.agg.snrs)
			sort.Float64s(sorted)
			mid := len(sorted) / 2
			if len(sorted)%2 == 0 {
				v := (sorted[mid-1] + sorted[mid]) / 2
				medianSNR = &v
			} else {
				v := sorted[mid]
				medianSNR = &v
			}
		}
		result = append(result, map[string]interface{}{
			"fromName": h.FromName, "fromPk": h.FromPk,
			"toName": h.ToName, "toPk": h.ToPk,
			"dist": h.Dist, "type": h.Type,
			"bestSnr": floatPtrOrNil(pe.agg.maxSNR), "medianSnr": floatPtrOrNil(medianSNR),
			"obsCount": pe.agg.obsCount,
			"hash":     h.Hash, "timestamp": h.Timestamp,
		})
	}
	return result
}

type distPathRecord struct {
	Hash      string
	TotalDist float64
	HopCount  int
	Timestamp string
	Hops      []distHopDetail
	tx        *StoreTx
}

type distHopDetail struct {
	FromName string
	FromPk   string
	ToName   string
	ToPk     string
	Dist     float64
}

type cachedResult struct {
	data      map[string]interface{}
	expiresAt time.Time
}

// cacheTTLSec extracts a duration from the cacheTTL config map.
// Values may be float64 (from JSON) or int. Returns false if key is missing or non-positive.
func cacheTTLSec(m map[string]interface{}, key string) (time.Duration, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	var sec float64
	switch n := v.(type) {
	case float64:
		sec = n
	case int:
		sec = float64(n)
	case int64:
		sec = float64(n)
	default:
		return 0, false
	}
	if sec <= 0 {
		return 0, false
	}
	return time.Duration(sec * float64(time.Second)), true
}

// NewPacketStore creates a new empty packet store backed by db.
// cacheTTLs is the optional cacheTTL map from config.json; keys are strings, values are seconds.
func NewPacketStore(db *DB, cfg *PacketStoreConfig, cacheTTLs ...map[string]interface{}) *PacketStore {
	ps := &PacketStore{
		db:            db,
		packets:       make([]*StoreTx, 0, 65536),
		byHash:        make(map[string]*StoreTx, 65536),
		byTxID:        make(map[int]*StoreTx, 65536),
		byObsID:       make(map[int]*StoreObs, 65536),
		byObserver:    make(map[string][]*StoreObs),
		byNode:        make(map[string][]*StoreTx),
		byPathHop:     make(map[string][]*StoreTx),
		nodeHashes:    make(map[string]map[string]bool),
		byPayloadType: make(map[int][]*StoreTx),
		rfCache:       make(map[string]*cachedResult),
		topoCache:     make(map[string]*cachedResult),
		hashCache:     make(map[string]*cachedResult),

		collisionCache: make(map[string]*cachedResult),
		chanCache:      make(map[string]*cachedResult),
		distCache:      make(map[string]*cachedResult),
		subpathCache:   make(map[string]*cachedResult),
		// #1239: 60 seconds by default. rfCacheTTL is the shared TTL for
		// the RF, topology, distance, hash-sizes, subpath, and channel
		// analytics caches. Distance analytics IS viewed live during
		// active analysis sessions (operators won't tolerate a 5-min
		// lag), so the bump from 15s → 60s smooths the most-frequent
		// cold-miss churn during heavy ingest without freezing data.
		// Override via cacheTTL.analyticsRF in config.json (also
		// propagates to distance / topology / hash-sizes / etc.).
		rfCacheTTL:           60 * time.Second,
		collisionCacheTTL:    3600 * time.Second,
		invCooldown:          300 * time.Second,
		spIndex:              make(map[string]int, 4096),
		spTxIndex:            make(map[string][]*StoreTx, 4096),
		advertPubkeys:        make(map[string]int),
		clockSkew:            NewClockSkewEngine(),
		useResolvedPathIndex: true,
		areaNodeCache:        make(map[string]map[string]bool),
		areaNodeCacheTimes:   make(map[string]time.Time),
	}
	ps.initResolvedPathIndex()
	if cfg != nil {
		ps.retentionHours = cfg.RetentionHours
		ps.maxMemoryMB = cfg.MaxMemoryMB
		ps.maxResolvedPubkeyIndexEntries = cfg.MaxResolvedPubkeyIndexEntries
		if cfg.HotStartupHours > 0 {
			h := cfg.HotStartupHours
			if ps.retentionHours > 0 && h > ps.retentionHours {
				log.Printf("[store] warning: hotStartupHours (%g) > retentionHours (%g) — clamping", h, ps.retentionHours)
				h = ps.retentionHours
			}
			ps.hotStartupHours = h
		}
	}
	// Wire cacheTTL config values to server-side cache durations.
	if len(cacheTTLs) > 0 && cacheTTLs[0] != nil {
		ct := cacheTTLs[0]
		if v, ok := cacheTTLSec(ct, "analyticsHashSizes"); ok {
			ps.collisionCacheTTL = v
		}
		if v, ok := cacheTTLSec(ct, "analyticsRF"); ok {
			ps.rfCacheTTL = v
		}
		if v, ok := cacheTTLSec(ct, "invalidationDebounce"); ok {
			ps.invCooldown = v
		}
	}
	return ps
}

// Load reads transmissions + observations from SQLite into memory.
// When maxMemoryMB > 0, loads only the newest N transmissions that fit
// within the memory budget, avoiding OOM on large databases.
func (s *PacketStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Startup-ordering invariant (PR #1643 R1 munger #2). Graph-load-
	// before-packet-load is the entire premise of the relay-history
	// cold-load fix: without an in-memory neighbor graph, the path_json
	// fallback below cannot disambiguate relay hops, so relay-node
	// analytics history silently collapses on every restart. main.go
	// loads neighbor_edges → s.graph BEFORE invoking Load/LoadChunked;
	// fast-fail here so a future refactor that swaps that ordering trips
	// the panic at startup instead of regressing the bug silently.
	if neighborEdgesTableExists(s.db.conn) && s.graph.Load() == nil {
		panic("packet store Load(): neighbor_edges table has rows but s.graph is nil — graph must be loaded before packet load (see main.go #1643 invariant)")
	}

	t0 := time.Now()

	// Count total transmissions for logging.
	var totalInDB int
	if err := s.db.conn.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&totalInDB); err != nil {
		totalInDB = -1 // non-fatal
	}

	// Calculate max packets to load based on memory budget.
	var maxPackets int64
	if s.maxMemoryMB > 0 {
		// Use a typical packet with ~10 observations as the estimate.
		avgBytes := int64(1000) // conservative floor
		if sample := estimateStoreTxBytesTypical(10); sample > avgBytes {
			avgBytes = sample
		}
		maxPackets = (int64(s.maxMemoryMB) * 1048576) / avgBytes
		if maxPackets < 1000 {
			maxPackets = 1000 // minimum 1000 packets
		}
	}
	// maxPackets == 0 means unlimited

	var loadSQL string
	rpCol := ""
	if s.db.hasResolvedPath {
		rpCol = ",\n\t\t\t\to.resolved_path"
	}
	obsRawHexCol := ""
	if s.db.hasObsRawHex {
		obsRawHexCol = ", o.raw_hex"
	}
	// #1751: scope_name is on the transmission row; append as the last
	// selected column so the observation fan-out doesn't affect its position.
	scopeNameCol := ""
	if s.db.hasScopeName {
		scopeNameCol = ", t.scope_name"
	}

	// Build WHERE conditions: retention cutoff (mirrors Evict logic) + optional memory-cap limit.
	// When hotStartupHours > 0, use it as the initial cutoff (smaller window = fast startup).
	//
	// PR #1187 r2 #7: compute the hot cutoff ONCE here and reuse the same
	// string for both the SQL filter below AND for s.oldestLoaded later.
	// Two separate time.Now().UTC() calls produced microsecond skew at chunk
	// borders, so the SQL window and oldestLoaded could disagree.
	var loadConditions []string
	hotCutoffHours := s.retentionHours
	if s.hotStartupHours > 0 {
		hotCutoffHours = s.hotStartupHours
	}
	var hotCutoffStr string
	if hotCutoffHours > 0 {
		hotCutoffT := time.Now().UTC().Add(-time.Duration(hotCutoffHours * float64(time.Hour)))
		hotCutoffStr = hotCutoffT.Format(time.RFC3339)
		// #1690: window on the denormalized last_seen so long-lived hashes
		// with recent traffic load on cold start. See chunked_load.go for
		// the full rationale + test-fixture fallback.
		if s.db.hasLastSeen {
			loadConditions = append(loadConditions, fmt.Sprintf("t.last_seen >= %d", hotCutoffT.Unix()))
		} else {
			loadConditions = append(loadConditions, fmt.Sprintf("t.first_seen >= '%s'", hotCutoffStr))
		}
	}
	if maxPackets > 0 {
		if s.db.hasLastSeen {
			loadConditions = append(loadConditions, fmt.Sprintf(
				"t.id IN (SELECT id FROM transmissions ORDER BY last_seen DESC LIMIT %d)", maxPackets))
		} else {
			loadConditions = append(loadConditions, fmt.Sprintf(
				"t.id IN (SELECT id FROM transmissions ORDER BY first_seen DESC LIMIT %d)", maxPackets))
		}
	}
	filterClause := ""
	if len(loadConditions) > 0 {
		filterClause = "\n\t\t\tWHERE " + strings.Join(loadConditions, "\n\t\t\t  AND ")
	}

	if s.db.isV3 {
		loadSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + obsRawHexCol + rpCol + scopeNameCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx` + filterClause + `
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	} else {
		loadSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp` + obsRawHexCol + rpCol + scopeNameCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.id = o.observer_id` + filterClause + `
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	}

	// Relay-hop fallback inputs. When resolved_path is empty (always, on
	// live, since #1287 the ingestor persists relay data as neighbor_edges
	// instead) we re-resolve relay hops from path_json using the prefix
	// map. PR #1643 R1 munger #1: cold load resolves ONLY when the prefix
	// is unique (no affinity tiebreak against ≤168h-old observations,
	// which would silently mis-attribute hops). Fetched BEFORE opening
	// the rows cursor below: getCachedNodesAndPM issues its own DB query,
	// which would deadlock against the still-open cursor on a single-
	// connection SQLite pool. Load holds s.mu.Lock(), so calling
	// getCachedNodesAndPM directly is safe.
	_, relayPM := s.getCachedNodesAndPM()
	var coldLoadAmbiguousHopsSkipped int

	rows, err := s.db.conn.Query(loadSQL)
	if err != nil {
		return err
	}
	defer rows.Close()

	hopsSeen := make(map[string]bool) // reused across observations; cleared per use

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsID sql.NullInt64
		var observerID, observerName, observerIATA, direction, pathJSON, obsTimestamp sql.NullString
		var snr, rssi sql.NullFloat64
		var score sql.NullInt64
		var obsRawHex sql.NullString
		var resolvedPathStr sql.NullString
		var scopeName sql.NullString

		scanArgs := []interface{}{&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsID, &observerID, &observerName, &observerIATA, &direction,
			&snr, &rssi, &score, &pathJSON, &obsTimestamp}
		if s.db.hasObsRawHex {
			scanArgs = append(scanArgs, &obsRawHex)
		}
		if s.db.hasResolvedPath {
			scanArgs = append(scanArgs, &resolvedPathStr)
		}
		if s.db.hasScopeName {
			scanArgs = append(scanArgs, &scopeName)
		}
		if err := rows.Scan(scanArgs...); err != nil {
			log.Printf("[store] scan error: %v", err)
			continue
		}

		hashStr := nullStrVal(hash)
		tx := s.byHash[hashStr]
		if tx == nil {
			tx = &StoreTx{
				ID:          txID,
				RawHex:      nullStrVal(rawHex),
				Hash:        hashStr,
				FirstSeen:   nullStrVal(firstSeen),
				LatestSeen:  nullStrVal(firstSeen),
				RouteType:   nullIntPtr(routeType),
				PayloadType: nullIntPtr(payloadType),
				DecodedJSON: nullStrVal(decodedJSON),
				ScopeName:   nullStrPtr(scopeName),
				obsKeys:     make(map[string]bool),
				observerSet: make(map[string]bool),
			}
			s.byHash[hashStr] = tx
			s.packets = append(s.packets, tx)
			s.byTxID[txID] = tx
			if txID > s.maxTxID {
				s.maxTxID = txID
			}
			s.indexByNode(tx)
			if tx.PayloadType != nil {
				pt := *tx.PayloadType
				s.byPayloadType[pt] = append(s.byPayloadType[pt], tx)
			}
			s.trackAdvertPubkey(tx)
			s.trackedBytes += estimateStoreTxBytes(tx)
		}

		if obsID.Valid {
			oid := int(obsID.Int64)
			obsIDStr := nullStrVal(observerID)
			obsPJ := nullStrVal(pathJSON)

			// Dedup: skip if same observer + same path already loaded (O(1) map lookup)
			dk := obsIDStr + "|" + obsPJ
			if tx.obsKeys[dk] {
				continue
			}

			obs := &StoreObs{
				ID:             oid,
				TransmissionID: txID,
				ObserverID:     obsIDStr,
				ObserverName:   nullStrVal(observerName),
				ObserverIATA:   nullStrVal(observerIATA),
				Direction:      nullStrVal(direction),
				SNR:            nullFloatPtr(snr),
				RSSI:           nullFloatPtr(rssi),
				Score:          nullIntPtr(score),
				PathJSON:       obsPJ,
				Timestamp:      normalizeTimestamp(nullStrVal(obsTimestamp)),
			}

			// Decode-window: extract resolved pubkeys for index, don't store on struct.
			rpStr := nullStrVal(resolvedPathStr)
			if rpStr != "" {
				rp := unmarshalResolvedPath(rpStr)
				pks := extractResolvedPubkeys(rp)
				// Single point of truth — see indexResolvedPathHops doc + #1558.
				s.indexResolvedPathHops(tx, pks, hopsSeen)
			} else if relayPM != nil && obsPJ != "" && obsPJ != "[]" {
				// resolved_path not persisted — reconstruct relay hops from
				// path_json so relay-node analytics history survives a restart.
				// Index into byNode ONLY: the resolved_path / path-hop indexes
				// (indexResolvedPathHops) are cross-checked by handleNodePaths
				// against the persisted resolved_path column, which is NULL
				// here — populating them would make that SQL confirmation fail
				// and wrongly drop the tx from paths-through (#1352). byNode is
				// what the node-analytics activity timeline reads.
				// PR #1643 R1 munger #1: gate on unique_prefix only — ambiguous
				// hops are silently dropped (skipped counter logged at end).
				rp := resolvePathForObsColdLoad(obsPJ, obsIDStr, tx, relayPM, &coldLoadAmbiguousHopsSkipped)
				for _, pk := range extractResolvedPubkeys(rp) {
					s.addToByNode(tx, pk)
				}
			}

			tx.Observations = append(tx.Observations, obs)
			tx.obsKeys[dk] = true
			if obs.ObserverID != "" && !tx.observerSet[obs.ObserverID] {
				tx.observerSet[obs.ObserverID] = true
				tx.UniqueObserverCount++
			}
			tx.ObservationCount++
			if obs.Timestamp > tx.LatestSeen {
				tx.LatestSeen = obs.Timestamp
			}

			s.byObsID[oid] = obs
			if oid > s.maxObsID {
				s.maxObsID = oid
			}

			if obsIDStr != "" {
				s.byObserver[obsIDStr] = append(s.byObserver[obsIDStr], obs)
			}

			s.totalObs++
			s.trackedBytes += estimateStoreObsBytes(obs)
		}
	}

	// Post-load: pick best observation (longest path) for each transmission,
	// then re-index so relay hops from resolved_path land in byNode.
	// indexByNode was called earlier (on StoreTx creation) before observations
	// were appended, so tx.ResolvedPath was nil at that point — call it again
	// now that pickBestObservation has propagated the best path.
	for _, tx := range s.packets {
		pickBestObservation(tx)
		s.indexByNode(tx)
	}

	// Build precomputed subpath index for O(1) analytics queries
	// — DEFERRED to a background goroutine (#1008). Same rationale
	// as the distance index (#1011): synchronous build under s.mu
	// blocks HTTP readiness ~60s at Cascadia scale. The goroutine is
	// started AFTER s.loaded = true below.
	// s.buildSubpathIndex()

	// Build path-hop index for O(1) node path lookups
	// — DEFERRED to a background goroutine (#1008).
	// s.buildPathHopIndex()

	// Precompute distance analytics (hop distances, path totals)
	// — DEFERRED to first /api/analytics/distance request (#1011).
	// At scale (2K+ nodes) this is O(n²) per-pair work that most
	// operators never trigger. See lazy build via
	// TriggerDistanceIndexBuild + handleAnalyticsDistance 202 path.
	// s.buildDistanceIndex()

	// Track oldest loaded timestamp for future SQL fallback queries.
	// When hotStartupHours > 0 use the SAME cutoff string that was used in
	// the load SQL (PR #1187 r2 #7) — recomputing time.Now().UTC() here
	// produced microsecond skew vs. the SQL filter at chunk boundaries.
	if s.hotStartupHours > 0 {
		s.oldestLoaded = hotCutoffStr
	} else if len(s.packets) > 0 {
		s.oldestLoaded = s.packets[0].FirstSeen
	}

	s.loaded = true
	elapsed := time.Since(t0)
	if maxPackets > 0 && totalInDB > len(s.packets) {
		log.Printf("[store] Loaded %d/%d transmissions (%d observations) in %v — bounded by %dMB budget (tracked ~%.0fMB, heap ~%.0fMB)",
			len(s.packets), totalInDB, s.totalObs, elapsed, s.maxMemoryMB, s.trackedMemoryMB(), s.estimatedMemoryMB())
	} else {
		log.Printf("[store] Loaded %d transmissions (%d observations) in %v (tracked ~%.0fMB, heap ~%.0fMB)",
			len(s.packets), s.totalObs, elapsed, s.trackedMemoryMB(), s.estimatedMemoryMB())
	}
	s.loadMultibyteCapFromDB()
	if coldLoadAmbiguousHopsSkipped > 0 {
		log.Printf("[store] cold load: skipped %d ambiguous-prefix relay hops (unique_prefix gate, PR #1643 R1)",
			coldLoadAmbiguousHopsSkipped)
	}
	// Kick off background subpath + path-hop index builds (#1008).
	// The goroutine acquires s.mu.Lock() and so will block until Load's
	// deferred Unlock fires when this function returns. HTTP handlers
	// gate reads behind SubpathIndexReady() / PathHopIndexReady() and
	// respond 503 + Retry-After: 5 until the builds finish.
	s.startBackgroundIndexBuilds()
	return nil
}

// loadChunk queries a [from, to) time window from SQLite without holding the
// write lock, builds local data structures, then merges them into the store
// under s.mu.Lock(). It is the building block for the background loader.
//
// The chunk is assumed to be older than the data already in the store, so
// localPackets are prepended to s.packets.
//
// byPayloadType is updated here incrementally. byPathHop, spIndex, and
// distHops are NOT updated here — the caller (loadBackgroundChunks) rebuilds
// those once after all chunks are merged.

// accumulateDedup appends pks to byTx[txID], deduplicating per-tx via the
// parallel seenByTx set. Used by loadChunk to build the per-tx relay-hop
// pubkey unions outside the merge critical section.
func accumulateDedup(byTx map[int][]string, seenByTx map[int]map[string]bool, txID int, pks []string) {
	if len(pks) == 0 {
		return
	}
	seen := seenByTx[txID]
	if seen == nil {
		seen = make(map[string]bool, len(pks))
		seenByTx[txID] = seen
	}
	for _, pk := range pks {
		if seen[pk] {
			continue
		}
		seen[pk] = true
		byTx[txID] = append(byTx[txID], pk)
	}
}

func (s *PacketStore) loadChunk(from, to time.Time) error {
	fromStr := from.UTC().Format(time.RFC3339)
	toStr := to.UTC().Format(time.RFC3339)
	fromUnix := from.UTC().Unix()
	toUnix := to.UTC().Unix()
	_ = fromStr
	_ = toStr

	// Build the same SQL as Load() but with a [from, to) window.
	rpCol := ""
	if s.db.hasResolvedPath {
		rpCol = ",\n\t\t\t\to.resolved_path"
	}
	obsRawHexCol := ""
	if s.db.hasObsRawHex {
		obsRawHexCol = ", o.raw_hex"
	}
	// #1751: scope_name is on the transmission row; append as the last column.
	scopeNameCol := ""
	if s.db.hasScopeName {
		scopeNameCol = ", t.scope_name"
	}

	// #1690: window on the denormalized last_seen (effective recency)
	// rather than first_seen. See chunked_load.go for the full rationale.
	// Test/legacy DBs without the column fall back to first_seen.
	var filterClause string
	var qArgs []interface{}
	if s.db.hasLastSeen {
		filterClause = "\n\t\t\tWHERE t.last_seen >= ? AND t.last_seen < ?"
		qArgs = []interface{}{fromUnix, toUnix}
	} else {
		filterClause = "\n\t\t\tWHERE t.first_seen >= ? AND t.first_seen < ?"
		qArgs = []interface{}{fromStr, toStr}
	}

	var chunkSQL string
	if s.db.isV3 {
		chunkSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + obsRawHexCol + rpCol + scopeNameCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx` + filterClause + `
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	} else {
		chunkSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp` + obsRawHexCol + rpCol + scopeNameCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id` + filterClause + `
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	}

	// Relay-hop fallback inputs. observations.resolved_path is NULL on
	// every live deployment (since #1287 the ingestor persists relay data
	// as aggregate neighbor_edges, not per-observation resolved_path), so
	// for this background-loaded older window we re-resolve relay hops
	// from the persisted path_json using the prefix map. PR #1643 R1
	// munger #1: cold load resolves ONLY when the prefix is unique
	// (affinity-tier resolution against ≤168h-old observations would
	// silently mis-attribute hops). Fetched BEFORE opening the rows
	// cursor below: getCachedNodesAndPM issues its own DB query, which
	// would deadlock against the open cursor on a single-connection SQLite
	// pool.
	s.mu.RLock()
	_, relayPM := s.getCachedNodesAndPM()
	s.mu.RUnlock()
	var coldLoadAmbiguousHopsSkipped int

	rows, err := s.db.conn.Query(chunkSQL, qArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Local data structures — built without holding the lock.
	localByHash := make(map[string]*StoreTx)
	localPackets := make([]*StoreTx, 0)
	localByTxID := make(map[int]*StoreTx)
	localByObsID := make(map[int]*StoreObs)
	localByObserver := make(map[string][]*StoreObs)
	// Issue #1558: accumulate the union of resolved_path relay-hop
	// pubkeys per tx outside the lock. We unmarshal + dedupe here so the
	// merge critical section below only does map-append work, mirroring
	// the rest of loadChunk's "build local, merge under lock" shape.
	localResolvedPKsByTx := make(map[int][]string)
	localResolvedSeenByTx := make(map[int]map[string]bool)
	// Path_json fallback pubkeys (resolved_path NULL): indexed into byNode
	// ONLY at merge, kept separate from localResolvedPKsByTx because they must
	// NOT enter the resolved_path/path-hop indexes (see the per-row comment).
	localByNodePKsByTx := make(map[int][]string)
	localByNodeSeenByTx := make(map[int]map[string]bool)
	var localTotalObs int
	var localTrackedBytes int64
	var localMaxTxID int
	var localMaxObsID int

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsID sql.NullInt64
		var observerID, observerName, direction, pathJSON, obsTimestamp sql.NullString
		var snr, rssi sql.NullFloat64
		var score sql.NullInt64
		var obsRawHex sql.NullString
		var resolvedPathStr sql.NullString
		var scopeName sql.NullString

		scanArgs := []interface{}{&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsID, &observerID, &observerName, &direction,
			&snr, &rssi, &score, &pathJSON, &obsTimestamp}
		if s.db.hasObsRawHex {
			scanArgs = append(scanArgs, &obsRawHex)
		}
		if s.db.hasResolvedPath {
			scanArgs = append(scanArgs, &resolvedPathStr)
		}
		if s.db.hasScopeName {
			scanArgs = append(scanArgs, &scopeName)
		}
		if err := rows.Scan(scanArgs...); err != nil {
			log.Printf("[store] loadChunk scan error: %v", err)
			continue
		}

		hashStr := nullStrVal(hash)
		tx := localByHash[hashStr]
		if tx == nil {
			tx = &StoreTx{
				ID:          txID,
				RawHex:      nullStrVal(rawHex),
				Hash:        hashStr,
				FirstSeen:   nullStrVal(firstSeen),
				LatestSeen:  nullStrVal(firstSeen),
				RouteType:   nullIntPtr(routeType),
				PayloadType: nullIntPtr(payloadType),
				DecodedJSON: nullStrVal(decodedJSON),
				ScopeName:   nullStrPtr(scopeName),
				obsKeys:     make(map[string]bool),
				observerSet: make(map[string]bool),
			}
			localByHash[hashStr] = tx
			localPackets = append(localPackets, tx)
			localByTxID[txID] = tx
			if txID > localMaxTxID {
				localMaxTxID = txID
			}
			localTrackedBytes += estimateStoreTxBytes(tx)
		}

		if obsID.Valid {
			oid := int(obsID.Int64)
			obsIDStr := nullStrVal(observerID)
			obsPJ := nullStrVal(pathJSON)

			dk := obsIDStr + "|" + obsPJ
			if tx.obsKeys[dk] {
				continue
			}

			obs := &StoreObs{
				ID:             oid,
				TransmissionID: txID,
				ObserverID:     obsIDStr,
				ObserverName:   nullStrVal(observerName),
				Direction:      nullStrVal(direction),
				SNR:            nullFloatPtr(snr),
				RSSI:           nullFloatPtr(rssi),
				Score:          nullIntPtr(score),
				PathJSON:       obsPJ,
				Timestamp:      normalizeTimestamp(nullStrVal(obsTimestamp)),
			}

			tx.Observations = append(tx.Observations, obs)
			tx.obsKeys[dk] = true
			if obs.ObserverID != "" && !tx.observerSet[obs.ObserverID] {
				tx.observerSet[obs.ObserverID] = true
				tx.UniqueObserverCount++
			}
			tx.ObservationCount++
			if obs.Timestamp > tx.LatestSeen {
				tx.LatestSeen = obs.Timestamp
			}

			localByObsID[oid] = obs
			if oid > localMaxObsID {
				localMaxObsID = oid
			}
			if obsIDStr != "" {
				localByObserver[obsIDStr] = append(localByObserver[obsIDStr], obs)
			}
			localTotalObs++
			localTrackedBytes += estimateStoreObsBytes(obs)

			// Issue #1558: collect resolved_path relay-hop pubkeys for
			// this observation. We unmarshal + dedupe OUTSIDE the merge
			// critical section so the lock-held work in the per-batch
			// merge below stays bounded. Without this, background-loaded
			// transmissions silently miss byNode entries for every relay
			// they were heard via — Load() does the equivalent inline.
			rpStr := nullStrVal(resolvedPathStr)
			if rpStr != "" {
				rp := unmarshalResolvedPath(rpStr)
				pks := extractResolvedPubkeys(rp)
				accumulateDedup(localResolvedPKsByTx, localResolvedSeenByTx, txID, pks)
			} else if relayPM != nil && obsPJ != "" && obsPJ != "[]" {
				// resolved_path not persisted — reconstruct relay hops from
				// path_json so the older retention window keeps relay-node
				// history across a restart (mirrors the hot-window fix in
				// scanAndMergeChunk and the live ingest path). These go into a
				// SEPARATE accumulator indexed into byNode ONLY at merge — they
				// must not enter the resolved_path/path-hop indexes, which
				// handleNodePaths cross-checks against the NULL resolved_path
				// column (#1352).
				// PR #1643 R1 munger #1: unique_prefix-only gate.
				rp := resolvePathForObsColdLoad(obsPJ, obsIDStr, tx, relayPM, &coldLoadAmbiguousHopsSkipped)
				pks := extractResolvedPubkeys(rp)
				accumulateDedup(localByNodePKsByTx, localByNodeSeenByTx, txID, pks)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Pick best observation for each local packet before merging.
	for _, tx := range localPackets {
		pickBestObservation(tx)
	}

	if len(localPackets) == 0 {
		return nil
	}

	// PR #1187 r3 MUST-FIX 1: index↔slice consistency.
	//
	// The previous (r2 #6, commit 2ec762aa) merge order was:
	//   1) prepend s.packets in one critical section
	//   2) populate s.byHash/byTxID/byObsID/byObserver/byNode/byPayloadType
	//      in separate per-batch critical sections
	//   3) bump counters in a third section
	//
	// Any RLock-holding reader between steps observed packets present in
	// s.packets but missing from s.byHash → silent partial data loss in
	// GetPacketByHash and the QueryPackets hash/node fast-paths.
	//
	// We invert the order: build the indexes FIRST in bounded per-batch
	// critical sections, then under a SINGLE final critical section
	// prepend s.packets AND bump counters AND advance maxIDs. The
	// invariant "every tx in s.packets is in s.byHash/s.byTxID" holds at
	// every RLock instant — readers either see the old state, or an
	// intermediate state where indexes contain a superset of s.packets
	// (which is harmless: nothing in s.packets dangles), or the fully
	// merged new state.
	const mergeBatchSize = 500

	for batchStart := 0; batchStart < len(localPackets); batchStart += mergeBatchSize {
		batchEnd := batchStart + mergeBatchSize
		if batchEnd > len(localPackets) {
			batchEnd = len(localPackets)
		}
		batch := localPackets[batchStart:batchEnd]

		// Build the per-batch index views outside the lock so the
		// critical section below is O(batch), not O(total local).
		batchHashes := make(map[string]*StoreTx, len(batch))
		batchTxIDs := make(map[int]*StoreTx, len(batch))
		batchObsIDs := make(map[int]*StoreObs)
		batchByObserver := make(map[string][]*StoreObs)
		for _, tx := range batch {
			batchHashes[tx.Hash] = tx
			batchTxIDs[tx.ID] = tx
			for _, o := range tx.Observations {
				batchObsIDs[o.ID] = o
				if o.ObserverID != "" {
					batchByObserver[o.ObserverID] = append(batchByObserver[o.ObserverID], o)
				}
			}
		}

		s.mu.Lock()
		// Issue #1558: hopsSeen scratch map for indexResolvedPathHops →
		// addResolvedPubkeysToPathHopIndex. Allocated once per batch and
		// reused across txs (clear()d on each call inside the helper).
		hopsSeen := make(map[string]bool)
		newObsIDs := make(map[int]bool, len(batchObsIDs))
		for k := range batchObsIDs {
			if s.byObsID[k] == nil {
				newObsIDs[k] = true
			}
		}
		for k, v := range batchHashes {
			if s.byHash[k] == nil {
				s.byHash[k] = v
			}
		}
		for k, v := range batchTxIDs {
			if s.byTxID[k] == nil {
				s.byTxID[k] = v
			}
		}
		for k, v := range batchObsIDs {
			if newObsIDs[k] {
				s.byObsID[k] = v
			}
		}
		for observerID, obsList := range batchByObserver {
			for _, o := range obsList {
				if newObsIDs[o.ID] {
					s.byObserver[observerID] = append(s.byObserver[observerID], o)
				}
			}
		}
		for _, tx := range batch {
			s.indexByNode(tx)
			if tx.PayloadType != nil {
				pt := *tx.PayloadType
				s.byPayloadType[pt] = append(s.byPayloadType[pt], tx)
			}
			s.trackAdvertPubkey(tx)
			// Issue #1558: mirror Load()'s 783-799 resolved-path branch.
			// Without this, background-loaded transmissions never enter
			// byNode under their relay-hop pubkeys → Home-page per-node
			// stats collapse after restart for relay-heavy nodes. The
			// pubkey union was pre-built outside the lock above.
			if pks := localResolvedPKsByTx[tx.ID]; len(pks) > 0 {
				s.indexResolvedPathHops(tx, pks, hopsSeen)
			}
			// path_json fallback (resolved_path NULL): byNode ONLY, so relay
			// nodes keep their analytics history across a restart without
			// polluting the resolved_path/path-hop indexes (#1352).
			for _, pk := range localByNodePKsByTx[tx.ID] {
				s.addToByNode(tx, pk)
			}
		}
		s.mu.Unlock()
		runtime.Gosched()
	}

	// Final atomic step: now that every tx in localPackets is fully
	// indexed, publish it into s.packets and bump counters in one short
	// critical section. After this point the new state is fully visible;
	// before it readers see the old slice (which is still fully indexed).
	s.mu.Lock()
	s.packets = append(localPackets, s.packets...)
	s.totalObs += localTotalObs
	s.trackedBytes += localTrackedBytes
	if localMaxTxID > s.maxTxID {
		s.maxTxID = localMaxTxID
	}
	if localMaxObsID > s.maxObsID {
		s.maxObsID = localMaxObsID
	}
	s.mu.Unlock()

	log.Printf("[store] background chunk [%s, %s) merged: %d tx, %d obs", fromStr, toStr, len(localPackets), localTotalObs)
	if coldLoadAmbiguousHopsSkipped > 0 {
		log.Printf("[store] background chunk: skipped %d ambiguous-prefix relay hops (unique_prefix gate, PR #1643 R1)",
			coldLoadAmbiguousHopsSkipped)
	}
	return nil
}

// loadBackgroundChunks fills the remaining retentionHours window by loading
// daily chunks from oldestLoaded back to the retention cutoff. After all
// chunks are merged it rebuilds analytics indexes once. Chunk errors are
// handled by advancing past the failed window so the loop always terminates.
func (s *PacketStore) loadBackgroundChunks() {
	// #1809 invariant: oldestLoaded MUST be set before the bg loader
	// runs whenever the in-memory store has packets. The original bug
	// was a parallel spawn that read oldestLoaded="" and silently
	// bailed → coverage gate trips → backgroundLoadFailed=true. Encode
	// the precondition here so a future refactor that re-introduces
	// the race fails loudly instead of silently shipping the same
	// regression. Empty store + empty oldestLoaded is the legitimate
	// "empty DB" path and is allowed.
	s.mu.RLock()
	oldestAtEntry := s.oldestLoaded
	packetCountAtEntry := len(s.packets)
	s.mu.RUnlock()
	if oldestAtEntry == "" && packetCountAtEntry > 0 {
		// adv #6 (PR #1811): in prod, a panic dumps every goroutine
		// stack and exits non-zero with `goroutine X [running]:` noise.
		// log.Fatalf is the cleaner shutdown: single-line "FATAL"
		// log, os.Exit(1), no stack spew, supervisor-friendly. Tests
		// override invariantViolation to panic so they can recover()
		// and assert the invariant message without crashing the test
		// runner. The invariant itself (refuse to silently bail on
		// the #1809 race) is preserved regardless of the handler.
		invariantViolation(fmt.Sprintf("loadBackgroundChunks: oldestLoaded=\"\" with %d packets in store — LoadChunked must run to completion first (#1809)", packetCountAtEntry))
		return
	}

	// Test-only entry hook. Production stores leave bgLoaderEntryHook
	// nil and pay the nil-check cost only.
	if s.bgLoaderEntryHook != nil {
		s.bgLoaderEntryHook()
	}

	if s.retentionHours <= 0 {
		s.backgroundLoadDone.Store(true)
		return
	}

	target := time.Now().UTC().Add(-time.Duration(s.retentionHours * float64(time.Hour)))
	// #1690: drop the `retentionHours - hotStartupHours <= 0` short-circuit.
	// Prod runs with both at 12h, which flipped backgroundLoadDone=true
	// immediately without ever walking the DB. We now always evaluate
	// coverage against actual rows in the retention window (see end of
	// function).
	retentionFloor := target.Unix()
	retentionFloorStr := target.Format(time.RFC3339)
	var totalInDB int64
	var countQuery string
	var countArg interface{}
	if s.db.hasLastSeen {
		countQuery = `SELECT COUNT(*) FROM transmissions WHERE last_seen >= ?`
		countArg = retentionFloor
	} else {
		countQuery = `SELECT COUNT(*) FROM transmissions WHERE first_seen >= ?`
		countArg = retentionFloorStr
	}
	if err := s.db.conn.QueryRow(countQuery, countArg).Scan(&totalInDB); err != nil {
		totalInDB = -1
	}

	var chunksLoaded float64
	var chunkErrors int
	totalHoursGap := s.retentionHours - s.hotStartupHours
	if totalHoursGap < 0 {
		totalHoursGap = 0
	}
	totalChunks := math.Ceil(totalHoursGap / 24)
	if totalChunks < 1 {
		totalChunks = 1
	}

	for {
		s.mu.RLock()
		oldest := s.oldestLoaded
		s.mu.RUnlock()

		if oldest == "" {
			break
		}
		chunkEnd, err := time.Parse(time.RFC3339, oldest)
		if err != nil {
			log.Printf("[store] background loader: bad oldestLoaded %q: %v", oldest, err)
			break
		}
		if !chunkEnd.After(target) {
			break
		}

		chunkStart := chunkEnd.Add(-24 * time.Hour)
		if chunkStart.Before(target) {
			chunkStart = target
		}

		chunkStartStr := chunkStart.Format(time.RFC3339)
		if err := s.loadChunk(chunkStart, chunkEnd); err != nil {
			chunkErrors++
			log.Printf("[store] background chunk [%s, %s) error: %v — advancing past it",
				chunkStartStr, chunkEnd.Format(time.RFC3339), err)
		}
		// Always advance oldestLoaded to chunkStart so the loop terminates,
		// even when the chunk was empty (loadChunk skips the update when 0 packets).
		s.mu.Lock()
		if s.oldestLoaded > chunkStartStr || s.oldestLoaded == "" {
			s.oldestLoaded = chunkStartStr
		}
		s.mu.Unlock()

		chunksLoaded++
		if totalChunks > 0 {
			pct := int64(chunksLoaded / totalChunks * 100)
			if pct > 100 {
				pct = 100
			}
			s.backgroundLoadProgress.Store(pct)
		}

		// Yield between chunks so ingest goroutines can acquire s.mu.Lock()
		// without being starved. The chosen tradeoff is brief lock-holds per
		// chunk rather than a configurable sleep; background fill is
		// best-effort and queries fall back to SQL while it runs.
		runtime.Gosched()
	}

	// Rebuild analytics indexes once after all chunks are merged.
	s.mu.Lock()
	s.buildSubpathIndex()
	s.buildPathHopIndex()
	// Distance index is now lazy (#1011) — built on first
	// /api/analytics/distance request, not at background-load
	// completion. If a previous request already triggered the build,
	// invalidate the gate so the next request rebuilds against the
	// fuller dataset.
	s.distLazyMu.Lock()
	s.distLazyBuilt = false
	s.distLazyOnce = sync.Once{}
	s.distLazyMu.Unlock()
	s.mu.Unlock()
	// #1008 review m3: flip the ready flags after the synchronous
	// rebuild for symmetry with startBackgroundIndexBuilds. Safe
	// today because the chunk loader runs after Load() has already
	// kicked the goroutines that set these to true; this is a
	// belt-and-suspenders against a future reorder where the chunk
	// loader could be the first writer.
	s.markIndexesReadySync()

	// #1690: compute actual coverage ratio and gate backgroundLoadDone
	// on it. The legacy code flipped done=true after walking the
	// chunk loop regardless of whether the in-memory store actually
	// reflected the on-disk DB; prod woke up at 0.3% loaded with
	// "complete". Threshold 0.90 mirrors the issue's acceptance
	// criterion.
	s.mu.RLock()
	loadedCount := int64(len(s.packets))
	s.mu.RUnlock()

	var ratio float64
	if totalInDB > 0 {
		ratio = float64(loadedCount) / float64(totalInDB)
	} else if totalInDB == 0 {
		// Empty DB: nothing to load, treat as complete.
		ratio = 1.0
	}
	s.bgErrMu.Lock()
	s.loadCoverageRatio = ratio
	if totalInDB > 0 && ratio < 0.90 {
		s.backgroundLoadFailed.Store(true)
		s.backgroundLoadErr = fmt.Sprintf("loaded %.1f%% of %d rows (%d in memory)",
			ratio*100, totalInDB, loadedCount)
	} else if chunkErrors > 0 {
		s.backgroundLoadFailed.Store(true)
		s.backgroundLoadErr = fmt.Sprintf("%d chunk error(s) during background load", chunkErrors)
	} else {
		s.backgroundLoadErr = ""
	}
	bgErr := s.backgroundLoadErr
	s.bgErrMu.Unlock()

	// Only flip done=true when the store truly reflects the DB. Operators
	// (and the analytics warm-up gate, #1659) read this as "no more rows
	// to load" — making it lie was the cascading failure.
	if !s.backgroundLoadFailed.Load() {
		s.backgroundLoadDone.Store(true)
	}
	s.backgroundLoadProgress.Store(100)

	s.mu.RLock()
	totalPkts := len(s.packets)
	oldest := s.oldestLoaded
	s.mu.RUnlock()
	if bgErr != "" {
		log.Printf("[store] background load FAILED (%s): %d packets in memory, oldestLoaded=%s", bgErr, totalPkts, oldest)
	} else {
		log.Printf("[store] background load complete: %d/%d packets in memory (coverage=%.1f%%), oldestLoaded=%s",
			totalPkts, totalInDB, ratio*100, oldest)
	}
}

// pickBestObservation selects the observation with the longest path
// and sets it as the transmission's display observation.
func pickBestObservation(tx *StoreTx) {
	if len(tx.Observations) == 0 {
		return
	}
	best := tx.Observations[0]
	bestLen := pathLen(best.PathJSON)
	for _, obs := range tx.Observations[1:] {
		l := pathLen(obs.PathJSON)
		if l > bestLen {
			best = obs
			bestLen = l
		}
	}
	tx.ObserverID = best.ObserverID
	tx.ObserverName = best.ObserverName
	tx.ObserverIATA = best.ObserverIATA
	tx.SNR = best.SNR
	tx.RSSI = best.RSSI
	tx.PathJSON = best.PathJSON
	tx.Direction = best.Direction
	tx.pathParsed = false // invalidate cached parsed path
}

func pathLen(pathJSON string) int {
	if pathJSON == "" {
		return 0
	}
	var hops []interface{}
	if json.Unmarshal([]byte(pathJSON), &hops) != nil {
		return 0
	}
	return len(hops)
}

// indexResolvedPathHops indexes a transmission under every relay-hop pubkey
// extracted from an observation's resolved_path, and refreshes the dependent
// resolved-pubkey + path-hop indexes. This is the single point of truth for
// the "feed decode-window consumers for resolved-path pubkeys" contract that
// must hold across every code path that materializes a transmission into the
// in-memory store: initial Load (cmd/server/store.go ~783-799), background
// chunk loads (loadChunk, see issue #1558), MQTT ingest (~2293-2306), and
// late-arriving-observation ingest (~2630-2643). Duplicating these three
// calls inline let loadChunk silently drop the branch and collapse per-node
// Home-page stats after restart for relay-heavy nodes — see #1558.
//
// Caller contract:
//   - Must hold s.mu write lock (addToByNode / addToResolvedPubkeyIndex /
//     addResolvedPubkeysToPathHopIndex all mutate store state).
//   - pks should be the output of extractResolvedPubkeys (no nils, no
//     empties); the helper is a no-op when pks is empty.
//   - hopsSeen is a reusable scratch map; addResolvedPubkeysToPathHopIndex
//     clear()s it on entry.
func (s *PacketStore) indexResolvedPathHops(tx *StoreTx, pks []string, hopsSeen map[string]bool) {
	if len(pks) == 0 {
		return
	}
	for _, pk := range pks {
		s.addToByNode(tx, pk)
	}
	s.addResolvedPubkeysToPathHopIndex(tx, pks, hopsSeen)
	s.addToResolvedPubkeyIndex(tx.ID, pks)
}

// indexByNode extracts pubkeys from decoded_json and indexes the transmission.
// indexByNode indexes a transmission under all pubkeys found in its decoded
// JSON. Resolved path pubkeys are handled separately via the decode-window.
// Returns true if any genuinely new node was discovered.
func (s *PacketStore) indexByNode(tx *StoreTx) bool {
	// Track which pubkeys have been indexed for this packet to avoid duplicates.
	indexed := make(map[string]bool)
	foundNew := false

	// Index by decoded JSON fields (pubKey, destPubKey, srcPubKey).
	if tx.DecodedJSON != "" && strings.Contains(tx.DecodedJSON, "ubKey") {
		if decoded := tx.ParsedDecoded(); decoded != nil {
			for _, field := range []string{"pubKey", "destPubKey", "srcPubKey"} {
				if v, ok := decoded[field].(string); ok && v != "" {
					if s.addToByNode(tx, v) {
						foundNew = true
					}
					indexed[v] = true
				}
			}
		}
	}

	return foundNew
}

// addToByNode adds tx to byNode[pubkey] with dedup via nodeHashes.
// Returns true if this is a genuinely new node (pubkey not seen before).
func (s *PacketStore) addToByNode(tx *StoreTx, pubkey string) bool {
	isNew := s.nodeHashes[pubkey] == nil
	if isNew {
		s.nodeHashes[pubkey] = make(map[string]bool)
	}
	if s.nodeHashes[pubkey][tx.Hash] {
		return false
	}
	s.nodeHashes[pubkey][tx.Hash] = true
	s.byNode[pubkey] = append(s.byNode[pubkey], tx)
	return isNew
}

// trackAdvertPubkey increments the advertPubkeys refcount for ADVERT packets.
// Must be called under s.mu write lock.
func (s *PacketStore) trackAdvertPubkey(tx *StoreTx) {
	if tx.PayloadType == nil || *tx.PayloadType != PayloadADVERT || tx.DecodedJSON == "" {
		return
	}
	d := tx.ParsedDecoded()
	if d == nil {
		return
	}
	pk := ""
	if v, ok := d["pubKey"].(string); ok {
		pk = v
	} else if v, ok := d["public_key"].(string); ok {
		pk = v
	}
	if pk != "" {
		s.advertPubkeys[pk]++
	}
}

// untrackAdvertPubkey decrements the advertPubkeys refcount for ADVERT packets.
// Must be called under s.mu write lock.
func (s *PacketStore) untrackAdvertPubkey(tx *StoreTx) {
	if tx.PayloadType == nil || *tx.PayloadType != PayloadADVERT || tx.DecodedJSON == "" {
		return
	}
	d := tx.ParsedDecoded()
	if d == nil {
		return
	}
	pk := ""
	if v, ok := d["pubKey"].(string); ok {
		pk = v
	} else if v, ok := d["public_key"].(string); ok {
		pk = v
	}
	if pk != "" {
		if s.advertPubkeys[pk] <= 1 {
			delete(s.advertPubkeys, pk)
		} else {
			s.advertPubkeys[pk]--
		}
	}
}

// QueryPackets returns filtered, paginated packets from memory.
func (s *PacketStore) QueryPackets(q PacketQuery) *PacketResult {
	// SQL fallback: if the query window predates the in-memory window, delegate
	// to the DB layer which covers the full SQLite retention period.
	s.mu.RLock()
	oldest := s.oldestLoaded
	s.mu.RUnlock()
	if oldest != "" {
		needsSQL := (q.Since != "" && q.Since < oldest) ||
			(q.Until != "" && q.Until < oldest)
		if needsSQL {
			if result, err := s.db.QueryPackets(q); err == nil {
				return result
			} else {
				log.Printf("[store] QueryPackets SQL fallback failed: %v — using in-memory", err)
			}
		}
	}
	atomic.AddInt64(&s.queryCount, 1)
	s.mu.RLock()
	defer s.mu.RUnlock()

	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Order == "" {
		q.Order = "DESC"
	}

	results := s.filterPackets(q)
	total := len(results)

	// #1345: order by ingest id, not insertion-into-s.packets order. After
	// Load() (which orders by first_seen ASC) the slice is mostly id-ordered
	// EXCEPT where rxTime ≠ ingest time — exactly the buffered-observer-upload
	// case that hides fresh activity. Sort by ID DESC so "page 0" is always
	// the most-recently-ingested transmissions, matching the DB-path fix.
	// Cost: O(n log n) on the filtered set per query; acceptable for the
	// typical filter-then-paginate flow (filterPackets already O(n)).
	sortedByID := make([]*StoreTx, len(results))
	copy(sortedByID, results)
	sort.Slice(sortedByID, func(i, j int) bool {
		return sortedByID[i].ID < sortedByID[j].ID
	})
	results = sortedByID

	// results is oldest-first (ASC). For DESC (default) read backwards from the tail;
	// for ASC read forwards. Both are O(page_size) — no sort copy needed.
	start := q.Offset
	if start >= total {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	pageSize := q.Limit
	if start+pageSize > total {
		pageSize = total - start
	}

	packets := make([]map[string]interface{}, 0, pageSize)
	if q.Order == "ASC" {
		for _, tx := range results[start : start+pageSize] {
			packets = append(packets, s.txToMapWithRP(tx, q.ExpandObservations))
		}
	} else {
		// DESC: newest items are at the tail; page 0 = last pageSize items reversed
		endIdx := total - start
		startIdx := endIdx - pageSize
		if startIdx < 0 {
			startIdx = 0
		}
		for i := endIdx - 1; i >= startIdx; i-- {
			packets = append(packets, s.txToMapWithRP(results[i], q.ExpandObservations))
		}
	}
	return &PacketResult{Packets: packets, Total: total}
}

// QueryGroupedPackets returns transmissions grouped by hash (already 1:1).
func (s *PacketStore) QueryGroupedPackets(q PacketQuery) *PacketResult {
	s.mu.RLock()
	oldest := s.oldestLoaded
	s.mu.RUnlock()
	if oldest != "" {
		needsSQL := (q.Since != "" && q.Since < oldest) ||
			(q.Until != "" && q.Until < oldest)
		if needsSQL {
			if result, err := s.db.QueryGroupedPackets(q); err == nil {
				return result
			} else {
				log.Printf("[store] QueryGroupedPackets SQL fallback failed: %v — using in-memory", err)
			}
		}
	}
	atomic.AddInt64(&s.queryCount, 1)

	if q.Limit <= 0 {
		q.Limit = 50
	}

	// Cache key covers all filter dimensions. Empty key = no filters.
	cacheKey := q.Since + "|" + q.Until + "|" + q.Region + "|" + q.Area + "|" + q.Node + "|" + q.Hash + "|" + q.Observer + "|" + q.Channel
	if q.Type != nil {
		cacheKey += fmt.Sprintf("|t%d", *q.Type)
	}
	if q.Route != nil {
		cacheKey += fmt.Sprintf("|r%d", *q.Route)
	}

	// Return cached sorted list if still fresh (3s TTL)
	s.groupedCacheMu.Lock()
	if s.groupedCacheTxs != nil && s.groupedCacheKey == cacheKey && time.Now().Before(s.groupedCacheExp) {
		cachedTxs := s.groupedCacheTxs
		cachedTotal := s.groupedCacheTotal
		s.groupedCacheMu.Unlock()
		return groupedTxsToPage(cachedTxs, cachedTotal, q.Offset, q.Limit)
	}
	s.groupedCacheMu.Unlock()

	// Collect StoreTx pointers under read lock; sort outside it.
	s.mu.RLock()
	results := s.filterPackets(q)
	txs := make([]*StoreTx, len(results))
	copy(txs, results)
	s.mu.RUnlock()

	total := len(txs)

	// Full sort by LatestSeen DESC so the cached slice supports all page offsets.
	sort.Slice(txs, func(i, j int) bool {
		return txs[i].LatestSeen > txs[j].LatestSeen
	})

	// Cache the sorted StoreTx slice (not maps) — lightweight and reusable for any page.
	s.groupedCacheMu.Lock()
	s.groupedCacheTxs = txs
	s.groupedCacheTotal = total
	s.groupedCacheKey = cacheKey
	s.groupedCacheExp = time.Now().Add(3 * time.Second)
	s.groupedCacheMu.Unlock()

	return groupedTxsToPage(txs, total, q.Offset, q.Limit)
}

// pagePacketResult returns a window of a PacketResult without re-allocating the slice.
func pagePacketResult(r *PacketResult, offset, limit int) *PacketResult {
	total := r.Total
	if offset >= total {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return &PacketResult{Packets: r.Packets[offset:end], Total: total}
}

// groupedTxsToPage builds map representations only for the requested page of sorted StoreTx pointers.
// This avoids allocating maps for all 30K+ transmissions when only 50 are needed.
func groupedTxsToPage(txs []*StoreTx, total, offset, limit int) *PacketResult {
	if offset >= len(txs) {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	end := offset + limit
	if end > len(txs) {
		end = len(txs)
	}
	page := txs[offset:end]

	packets := make([]map[string]interface{}, len(page))
	for i, tx := range page {
		// #1189 R2: compute distinct IATA set across all observations.
		// Frontend uses this in the default collapsed view to show CROSS-region
		// reception at a glance — see groupedObserverIataBadgesHtml in
		// public/packets.js.
		distinctIatas := storeTxDistinctIatas(tx)
		m := map[string]interface{}{
			"hash":              strOrNil(tx.Hash),
			"first_seen":        strOrNil(tx.FirstSeen),
			"count":             tx.ObservationCount,
			"observer_count":    tx.UniqueObserverCount,
			"observation_count": tx.ObservationCount,
			"latest":            strOrNil(tx.LatestSeen),
			"observer_id":       strOrNil(tx.ObserverID),
			"observer_name":     strOrNil(tx.ObserverName),
			"observer_iata":     strOrNil(tx.ObserverIATA),
			"distinct_iatas":    distinctIatas,
			"path_json":         strOrNil(tx.PathJSON),
			"payload_type":      intPtrOrNil(tx.PayloadType),
			"route_type":        intPtrOrNil(tx.RouteType),
			"raw_hex":           strOrNil(tx.RawHex),
			"decoded_json":      strOrNil(tx.DecodedJSON),
			"snr":               floatPtrOrNil(tx.SNR),
			"rssi":              floatPtrOrNil(tx.RSSI),
			"scope_name":        strPtrOrNil(tx.ScopeName),
		}
		// resolved_path omitted for grouped view (cold path, not worth SQL round-trip)
		packets[i] = m
	}

	return &PacketResult{Packets: packets, Total: total}
}

// storeTxDistinctIatas (#1189 R2) returns a sorted, deduped list of observer
// IATA codes for a StoreTx, excluding empty values. Returns an empty
// (non-nil) []string when the tx has no IATA'd observations so JSON
// serialization stays consistent across the in-memory store and SQL
// fallback paths (db.go's parseDistinctIatasCSV does the same).
func storeTxDistinctIatas(tx *StoreTx) []string {
	if tx == nil {
		return []string{}
	}
	seen := make(map[string]bool)
	// Include the header observer's IATA (some hot-path StoreTx records the
	// chosen observer fields directly without re-populating Observations).
	if tx.ObserverIATA != "" {
		seen[tx.ObserverIATA] = true
	}
	for _, o := range tx.Observations {
		if o != nil && o.ObserverIATA != "" {
			seen[o.ObserverIATA] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GetStoreStats returns aggregate counts (packet data from memory, node/observer from DB).
func (s *PacketStore) GetStoreStats() (*Stats, error) {
	s.mu.RLock()
	txCount := len(s.packets)
	obsCount := s.totalObs
	s.mu.RUnlock()

	st := &Stats{
		TotalTransmissions: txCount,
		TotalPackets:       txCount,
		TotalObservations:  obsCount,
	}

	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
	oneDayAgo := time.Now().Add(-24 * time.Hour).Unix()

	// Observation counts (#1910). Three cases, and only the cold one makes the
	// caller wait:
	//   fresh  — serve the cache.
	//   stale  — serve the cache anyway and refresh once in the background. A
	//            count that is half a minute old is not worth a page that hangs.
	//   cold   — compute, but under singleflight, so N concurrent callers share
	//            one scan instead of each running their own.
	var obsFromCache bool
	s.statsCacheMu.Lock()
	haveObsCache := !s.statsCacheTime.IsZero()
	obsCacheFresh := haveObsCache && time.Since(s.statsCacheTime) < 30*time.Second
	if haveObsCache {
		st.PacketsLastHour = s.statsLastHour
		st.PacketsLast24h = s.statsLast24h
	}
	s.statsCacheMu.Unlock()

	switch {
	case obsCacheFresh:
		obsFromCache = true
	case haveObsCache:
		obsFromCache = true
		go func() { _, _, _ = s.refreshObsCounts(oneHourAgo, oneDayAgo) }()
	}

	// Run node/observer counts and (if cold) observation counts concurrently.
	var wg sync.WaitGroup
	var nodeErr, obsErr error

	wg.Add(1)
	if !obsFromCache {
		wg.Add(1)
	}
	go func() {
		defer wg.Done()
		nodeErr = s.db.conn.QueryRow(
			`SELECT
				(SELECT COUNT(*) FROM nodes WHERE last_seen > ?) AS active_nodes,
				(SELECT COUNT(*) FROM nodes) AS all_nodes,
				(SELECT COUNT(*) FROM observers WHERE inactive IS NULL OR inactive = 0) AS observers`,
			sevenDaysAgo,
		).Scan(&st.TotalNodes, &st.TotalNodesAllTime, &st.TotalObservers)
	}()
	if !obsFromCache {
		go func() {
			defer wg.Done()
			lastHour, last24h, err := s.refreshObsCounts(oneHourAgo, oneDayAgo)
			obsErr = err
			if err == nil {
				st.PacketsLastHour = lastHour
				st.PacketsLast24h = last24h
			}
		}()
	}
	wg.Wait()

	if nodeErr != nil {
		return st, nodeErr
	}
	if obsErr != nil {
		return st, obsErr
	}

	return st, nil
}

// refreshObsCounts runs the observations range scan for the last hour and the
// last 24h, and writes the result into the stats cache.
//
// #1910: wrapped in singleflight. The cache check in GetStoreStats releases
// statsCacheMu before doing the work, so every request that arrived while the
// cache was expired used to run this scan itself. With SetMaxOpenConns(4) and a
// page that fires stats, observers, nodes, channels and clock-skew at once, that
// turned one scan into a queue of them: /stats was measured at 10-17s under
// mixed load while staying under 70ms when it was the only endpoint being hit.
// Now the second and later callers wait for the first one's result.
func (s *PacketStore) refreshObsCounts(oneHourAgo, oneDayAgo int64) (int, int, error) {
	v, err, _ := s.statsSF.Do("obs-counts", func() (interface{}, error) {
		var lastHour, last24h int
		if qErr := s.db.conn.QueryRow(
			`SELECT
				COALESCE(SUM(CASE WHEN timestamp > ? THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN timestamp > ? THEN 1 ELSE 0 END), 0)
			FROM observations WHERE timestamp > ?`,
			oneHourAgo, oneDayAgo, oneDayAgo,
		).Scan(&lastHour, &last24h); qErr != nil {
			return nil, qErr
		}
		s.statsCacheMu.Lock()
		s.statsLastHour = lastHour
		s.statsLast24h = last24h
		s.statsCacheTime = time.Now()
		s.statsCacheMu.Unlock()
		return [2]int{lastHour, last24h}, nil
	})
	if err != nil {
		return 0, 0, err
	}
	pair := v.([2]int)
	return pair[0], pair[1], nil
}

// GetPerfStoreStats returns packet store statistics for /api/perf.
func (s *PacketStore) GetPerfStoreStats() map[string]interface{} {
	s.mu.RLock()
	totalLoaded := len(s.packets)
	totalObs := s.totalObs
	hashIdx := len(s.byHash)
	txIdx := len(s.byTxID)
	obsIdx := len(s.byObsID)
	observerIdx := len(s.byObserver)
	nodeIdx := len(s.byNode)
	pathHopIdx := len(s.byPathHop)
	ptIdx := len(s.byPayloadType)
	oldestLoaded := s.oldestLoaded
	retentionHours := s.retentionHours
	maxMemoryMB := s.maxMemoryMB
	hotStartupHours := s.hotStartupHours

	// Distinct advert pubkey count — precomputed incrementally (see trackAdvertPubkey).
	advertByObsCount := len(s.advertPubkeys)
	s.mu.RUnlock()

	estimatedMB := math.Round(s.estimatedMemoryMB()*10) / 10
	trackedMB := math.Round(s.trackedMemoryMB()*10) / 10

	evicted := atomic.LoadInt64(&s.evicted)

	return map[string]interface{}{
		"totalLoaded":            totalLoaded,
		"totalObservations":      totalObs,
		"evicted":                evicted,
		"inserts":                atomic.LoadInt64(&s.insertCount),
		"queries":                atomic.LoadInt64(&s.queryCount),
		"inMemory":               totalLoaded,
		"sqliteOnly":             false,
		"retentionHours":         retentionHours,
		"maxMemoryMB":            maxMemoryMB,
		"oldestLoaded":           oldestLoaded,
		"estimatedMB":            estimatedMB,
		"trackedMB":              trackedMB,
		"hotStartupHours":        hotStartupHours,
		"backgroundLoadComplete": s.backgroundLoadDone.Load(),
		"backgroundLoadFailed":   s.backgroundLoadFailed.Load(),
		"backgroundLoadProgress": s.backgroundLoadProgress.Load(),
		"indexes": map[string]interface{}{
			"byHash":           hashIdx,
			"byTxID":           txIdx,
			"byObsID":          obsIdx,
			"byObserver":       observerIdx,
			"byNode":           nodeIdx,
			"byPathHop":        pathHopIdx,
			"byPayloadType":    ptIdx,
			"advertByObserver": advertByObsCount,
		},
	}
}

// GetCacheStats returns RF cache hit/miss statistics.
func (s *PacketStore) GetCacheStats() map[string]interface{} {
	s.cacheMu.Lock()
	size := len(s.rfCache) + len(s.topoCache) + len(s.hashCache) + len(s.chanCache) + len(s.distCache) + len(s.subpathCache)
	hits := s.cacheHits
	misses := s.cacheMisses
	s.cacheMu.Unlock()

	var hitRate float64
	if hits+misses > 0 {
		hitRate = math.Round(float64(hits)/float64(hits+misses)*1000) / 10
	}

	return map[string]interface{}{
		"size":       size,
		"hits":       hits,
		"misses":     misses,
		"staleHits":  0,
		"recomputes": misses,
		"hitRate":    hitRate,
	}
}

// GetCacheStatsTyped returns cache stats as a typed struct.
func (s *PacketStore) GetCacheStatsTyped() CacheStats {
	s.cacheMu.Lock()
	size := len(s.rfCache) + len(s.topoCache) + len(s.hashCache) + len(s.chanCache) + len(s.distCache) + len(s.subpathCache)
	hits := s.cacheHits
	misses := s.cacheMisses
	s.cacheMu.Unlock()

	var hitRate float64
	if hits+misses > 0 {
		hitRate = math.Round(float64(hits)/float64(hits+misses)*1000) / 10
	}

	return CacheStats{
		Entries:    size,
		Hits:       hits,
		Misses:     misses,
		StaleHits:  0,
		Recomputes: misses,
		HitRate:    hitRate,
	}
}

// cacheInvalidation flags indicate what kind of data changed during ingestion.
// Used by invalidateCachesFor to selectively clear only affected caches.
type cacheInvalidation struct {
	hasNewObservations  bool // new SNR/RSSI data → rfCache
	hasNewPaths         bool // new/changed path data → topoCache, distCache, subpathCache
	hasNewTransmissions bool // new transmissions → hashCache
	hasNewNodes         bool // genuinely new node pubkey discovered → collisionCache
	hasChannelData      bool // new GRP_TXT (payload_type 5) → chanCache
	eviction            bool // data removed → all caches
}

// invalidateCachesFor selectively clears only the analytics caches affected
// by the kind of data that changed. To prevent continuous ingestion from
// defeating caching entirely (issue #533), invalidation is rate-limited:
// if called within invCooldown of the last invalidation, the flags are
// accumulated in pendingInv and applied on the next call after cooldown.
func (s *PacketStore) invalidateCachesFor(inv cacheInvalidation) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	// Eviction bypasses rate-limiting — data was removed, caches must clear.
	if inv.eviction {
		s.rfCache = make(map[string]*cachedResult)
		s.topoCache = make(map[string]*cachedResult)
		s.hashCache = make(map[string]*cachedResult)
		s.collisionCache = make(map[string]*cachedResult)
		s.chanCache = make(map[string]*cachedResult)
		s.distCache = make(map[string]*cachedResult)
		s.subpathCache = make(map[string]*cachedResult)
		s.channelsCacheMu.Lock()
		s.channelsCacheRes = nil
		s.channelsCacheMu.Unlock()
		s.lastInvalidated = time.Now()
		s.pendingInv = nil
		return
	}

	now := time.Now()
	if now.Sub(s.lastInvalidated) < s.invCooldown {
		// Within cooldown — accumulate dirty flags
		if s.pendingInv == nil {
			s.pendingInv = &cacheInvalidation{}
		}
		s.pendingInv.hasNewObservations = s.pendingInv.hasNewObservations || inv.hasNewObservations
		s.pendingInv.hasNewPaths = s.pendingInv.hasNewPaths || inv.hasNewPaths
		s.pendingInv.hasNewTransmissions = s.pendingInv.hasNewTransmissions || inv.hasNewTransmissions
		s.pendingInv.hasNewNodes = s.pendingInv.hasNewNodes || inv.hasNewNodes
		s.pendingInv.hasChannelData = s.pendingInv.hasChannelData || inv.hasChannelData
		return
	}

	// Cooldown expired — merge any pending flags and apply
	if s.pendingInv != nil {
		inv.hasNewObservations = inv.hasNewObservations || s.pendingInv.hasNewObservations
		inv.hasNewPaths = inv.hasNewPaths || s.pendingInv.hasNewPaths
		inv.hasNewTransmissions = inv.hasNewTransmissions || s.pendingInv.hasNewTransmissions
		inv.hasNewNodes = inv.hasNewNodes || s.pendingInv.hasNewNodes
		inv.hasChannelData = inv.hasChannelData || s.pendingInv.hasChannelData
		s.pendingInv = nil
	}

	s.applyCacheInvalidation(inv)
	s.lastInvalidated = now
}

// applyCacheInvalidation performs the actual cache clearing. Must be called
// with cacheMu held.
func (s *PacketStore) applyCacheInvalidation(inv cacheInvalidation) {
	if inv.hasNewObservations {
		s.rfCache = make(map[string]*cachedResult)
	}
	if inv.hasNewPaths {
		s.topoCache = make(map[string]*cachedResult)
		s.distCache = make(map[string]*cachedResult)
		s.subpathCache = make(map[string]*cachedResult)
	}
	if inv.hasNewTransmissions {
		s.hashCache = make(map[string]*cachedResult)
	}
	if inv.hasNewNodes {
		s.collisionCache = make(map[string]*cachedResult)
	}
	if inv.hasChannelData {
		s.chanCache = make(map[string]*cachedResult)
		s.channelsCacheMu.Lock()
		s.channelsCacheRes = nil
		s.channelsCacheMu.Unlock()
	}
}

// GetPerfStoreStatsTyped returns packet store stats as a typed struct.
func (s *PacketStore) GetPerfStoreStatsTyped() PerfPacketStoreStats {
	s.mu.RLock()
	totalLoaded := len(s.packets)
	totalObs := s.totalObs
	hashIdx := len(s.byHash)
	observerIdx := len(s.byObserver)
	nodeIdx := len(s.byNode)
	oldestLoaded := s.oldestLoaded
	retentionHours := s.retentionHours

	advertByObsCount := len(s.advertPubkeys)
	s.mu.RUnlock()

	s.bgErrMu.RLock()
	loadCov := s.loadCoverageRatio
	bgErr := s.backgroundLoadErr
	s.bgErrMu.RUnlock()

	estimatedMB := math.Round(s.estimatedMemoryMB()*10) / 10
	trackedMB := math.Round(s.trackedMemoryMB()*10) / 10

	var avgBytesPerPacket int64
	if totalLoaded > 0 {
		avgBytesPerPacket = s.trackedBytes / int64(totalLoaded)
	}

	return PerfPacketStoreStats{
		TotalLoaded:       totalLoaded,
		TotalObservations: totalObs,
		Evicted:           int(atomic.LoadInt64(&s.evicted)),
		Inserts:           atomic.LoadInt64(&s.insertCount),
		Queries:           atomic.LoadInt64(&s.queryCount),
		InMemory:          totalLoaded,
		SqliteOnly:        false,
		MaxPackets:        2386092,
		EstimatedMB:       estimatedMB,
		TrackedMB:         trackedMB,
		AvgBytesPerPacket: avgBytesPerPacket,
		MaxMB:             s.maxMemoryMB,
		Indexes: PacketStoreIndexes{
			ByHash:           hashIdx,
			ByObserver:       observerIdx,
			ByNode:           nodeIdx,
			AdvertByObserver: advertByObsCount,
		},
		HotStartupHours:        s.hotStartupHours,
		BackgroundLoadComplete: s.backgroundLoadDone.Load(),
		BackgroundLoadFailed:   s.backgroundLoadFailed.Load(),
		BackgroundLoadProgress: s.backgroundLoadProgress.Load(),
		BackgroundLoadError:    bgErr,
		RetentionHours:         retentionHours,
		OldestLoaded:           oldestLoaded,
		LoadCoverageRatio:      loadCov,
	}
}

// GetStoreMemoryBreakdown walks the whole store ONCE (under RLock) and returns
// the flood-forward (route_type 0/1) share of stored transmissions plus a
// per-component breakdown of the string bytes held in memory. O(tx + obs) — it
// touches every observation, so it is opt-in (/api/perf?mem=1) and must not be
// on the hot path.
//
// LOCK CONTENTION: this holds s.mu.RLock for the entire scan of the store. On a
// large store (millions of observations) the walk takes long enough to stall
// concurrent writers (ingest/eviction take the write lock) for the duration.
// Operators should poll this infrequently (e.g. on demand, not on a tight
// dashboard refresh) — do not wire it into a high-frequency scrape.
//
// The per-component *MB figures count string CONTENT plus one Go string header
// per field. For fields that are inline struct members (RawHex, DecodedJSON,
// PathJSON, observer strings) that header is ALSO part of storeTxBaseBytes /
// storeObsBaseBytes, so the per-component totals are a deliberate UPPER BOUND,
// not additive with TotalTxEstimatedMB. struct/index/map overhead, the neighbor
// graph and analytics caches are excluded entirely — which is why the total
// here sits below goHeapInuseMB.
func (s *PacketStore) GetStoreMemoryBreakdown() *StoreMemoryBreakdown {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := &StoreMemoryBreakdown{}
	var txRawHex, txDecodedJSON, txPathJSON int64
	var obsPathJSON, obsStrings int64
	var floodTxBytes, totalTxBytes int64

	for _, tx := range s.packets {
		if tx == nil {
			continue
		}
		out.TotalTx++
		isFlood := tx.RouteType != nil && (*tx.RouteType == 0 || *tx.RouteType == 1)
		if isFlood {
			out.FloodTx++
		}
		txRawHex += int64(len(tx.RawHex) + strHdr)
		txDecodedJSON += int64(len(tx.DecodedJSON) + strHdr)
		txPathJSON += int64(len(tx.PathJSON) + strHdr)
		b := estimateStoreTxBytes(tx)
		totalTxBytes += b
		if isFlood {
			floodTxBytes += b
		}
		for _, o := range tx.Observations {
			if o == nil {
				continue
			}
			out.Observations++
			obsPathJSON += int64(len(o.PathJSON) + strHdr)
			obsStrings += int64(len(o.ObserverID)+len(o.ObserverName)+len(o.ObserverIATA)+len(o.Direction)+len(o.Timestamp)) + 5*strHdr
		}
	}

	// 2 decimal places so sub-0.1MB components don't round away to 0.
	mb := func(b int64) float64 { return math.Round(float64(b)/1048576*100) / 100 }
	out.TxRawHexMB = mb(txRawHex)
	out.TxDecodedJsonMB = mb(txDecodedJSON)
	out.TxPathJsonMB = mb(txPathJSON)
	out.ObsPathJsonMB = mb(obsPathJSON)
	out.ObsStringsMB = mb(obsStrings)
	out.FloodTxEstimatedMB = mb(floodTxBytes)
	out.TotalTxEstimatedMB = mb(totalTxBytes)
	if out.TotalTx > 0 {
		out.FloodTxSharePct = math.Round(float64(out.FloodTx)/float64(out.TotalTx)*1000) / 10
		out.ObsPerTx = math.Round(float64(out.Observations)/float64(out.TotalTx)*10) / 10
	}
	return out
}

// GetTransmissionByID returns a transmission by its DB ID, formatted as a map.
func (s *PacketStore) GetTransmissionByID(id int) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byTxID[id]
	if tx == nil {
		return nil
	}
	return s.txToMapWithRP(tx, true)
}

// GetPacketByHash returns a transmission by content hash.
func (s *PacketStore) GetPacketByHash(hash string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byHash[strings.ToLower(hash)]
	if tx == nil {
		return nil
	}
	return s.txToMapWithRP(tx, true)
}

// GetPacketByID returns an observation (enriched with transmission fields) by observation ID.
func (s *PacketStore) GetPacketByID(id int) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	obs := s.byObsID[id]
	if obs == nil {
		return nil
	}
	return s.enrichObs(obs)
}

// GetObservationsForHash returns all observations for a hash, enriched with transmission fields.
func (s *PacketStore) GetObservationsForHash(hash string) []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byHash[strings.ToLower(hash)]
	if tx == nil {
		return []map[string]interface{}{}
	}

	result := make([]map[string]interface{}, 0, len(tx.Observations))
	for _, obs := range tx.Observations {
		result = append(result, s.enrichObs(obs))
	}
	return result
}

// GetTimestamps returns transmission first_seen timestamps after since, in ASC order.
func (s *PacketStore) GetTimestamps(since string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// packets sorted oldest-first — scan from tail until we reach items older than since
	var result []string
	for i := len(s.packets) - 1; i >= 0; i-- {
		tx := s.packets[i]
		if tx.FirstSeen <= since {
			break
		}
		result = append(result, tx.FirstSeen)
	}
	// result is currently newest-first; reverse to return ASC order
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// QueryMultiNodePackets filters packets matching any of the given pubkeys.
func (s *PacketStore) QueryMultiNodePackets(pubkeys []string, limit, offset int, order, since, until string) *PacketResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(pubkeys) == 0 {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: 0}
	}
	if limit <= 0 {
		limit = 50
	}

	resolved := make([]string, len(pubkeys))
	for i, pk := range pubkeys {
		resolved[i] = s.db.resolveNodePubkey(pk)
	}

	// Use byNode index instead of scanning all packets (O(indexed) vs O(all×pubkeys×json)).
	hashSet := make(map[string]bool)
	var filtered []*StoreTx
	for _, pk := range resolved {
		for _, tx := range s.byNode[pk] {
			if hashSet[tx.Hash] {
				continue
			}
			if since != "" && tx.FirstSeen < since {
				continue
			}
			if until != "" && tx.FirstSeen > until {
				continue
			}
			hashSet[tx.Hash] = true
			filtered = append(filtered, tx)
		}
	}
	// #1345: sort by ingest id, not first_seen (=rxTime).
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID < filtered[j].ID
	})

	total := len(filtered)

	// filtered is oldest-first (built by iterating s.packets forward).
	// Apply same DESC/ASC pagination logic as QueryPackets.
	if offset >= total {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	pageSize := limit
	if offset+pageSize > total {
		pageSize = total - offset
	}

	packets := make([]map[string]interface{}, 0, pageSize)
	if order == "ASC" {
		for _, tx := range filtered[offset : offset+pageSize] {
			packets = append(packets, s.txToMapWithRP(tx))
		}
	} else {
		endIdx := total - offset
		startIdx := endIdx - pageSize
		if startIdx < 0 {
			startIdx = 0
		}
		for i := endIdx - 1; i >= startIdx; i-- {
			packets = append(packets, s.txToMapWithRP(filtered[i]))
		}
	}
	return &PacketResult{Packets: packets, Total: total}
}

// IngestNewFromDB loads new transmissions from SQLite into memory and returns
// broadcast-ready maps plus the new max transmission ID.
func (s *PacketStore) IngestNewFromDB(sinceID, limit int) ([]map[string]interface{}, int) {
	if limit <= 0 {
		limit = 100
	}

	// NOTE: The SQL query intentionally does NOT select resolved_path from the DB.
	// New ingests always resolve fresh using the current prefix map and neighbor graph.
	// On restart, Load() handles reading persisted resolved_path values. (review item #7)
	var querySQL string
	obsRHCol := ""
	if s.db.hasObsRawHex {
		obsRHCol = ", o.raw_hex"
	}
	// #1751: scope_name is on the transmission row; append as the last column.
	scopeNameCol := ""
	if s.db.hasScopeName {
		scopeNameCol = ", t.scope_name"
	}
	if s.db.isV3 {
		querySQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + obsRHCol + scopeNameCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE t.id > ?
			ORDER BY t.id ASC, o.timestamp DESC`
	} else {
		querySQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp` + obsRHCol + scopeNameCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.id = o.observer_id
			WHERE t.id > ?
			ORDER BY t.id ASC, o.timestamp DESC`
	}

	rows, err := s.db.conn.Query(querySQL, sinceID)
	if err != nil {
		log.Printf("[store] ingest query error: %v", err)
		return nil, sinceID
	}
	defer rows.Close()

	// Scan into temp structures
	type tempRow struct {
		txID                                                               int
		rawHex, hash, firstSeen, decodedJSON                               string
		routeType, payloadType                                             *int
		obsID                                                              *int
		observerID, observerName, observerIATA, direction, pathJSON, obsTS string
		obsRawHex                                                          string
		scopeName                                                          *string
		snr, rssi                                                          *float64
		score                                                              *int
	}

	var tempRows []tempRow
	txCount := 0
	lastTxID := sinceID

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsIDVal sql.NullInt64
		var observerID, observerName, observerIATA, direction, pathJSON, obsTimestamp sql.NullString
		var snrVal, rssiVal sql.NullFloat64
		var scoreVal sql.NullInt64
		var obsRawHex sql.NullString
		var scopeName sql.NullString

		scanArgs2 := []interface{}{&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsIDVal, &observerID, &observerName, &observerIATA, &direction,
			&snrVal, &rssiVal, &scoreVal, &pathJSON, &obsTimestamp}
		if s.db.hasObsRawHex {
			scanArgs2 = append(scanArgs2, &obsRawHex)
		}
		if s.db.hasScopeName {
			scanArgs2 = append(scanArgs2, &scopeName)
		}
		if err := rows.Scan(scanArgs2...); err != nil {
			continue
		}

		if txID != lastTxID {
			txCount++
			if txCount > limit {
				break
			}
			lastTxID = txID
		}

		tr := tempRow{
			txID:         txID,
			rawHex:       nullStrVal(rawHex),
			hash:         nullStrVal(hash),
			firstSeen:    nullStrVal(firstSeen),
			decodedJSON:  nullStrVal(decodedJSON),
			routeType:    nullIntPtr(routeType),
			payloadType:  nullIntPtr(payloadType),
			observerID:   nullStrVal(observerID),
			observerName: nullStrVal(observerName),
			observerIATA: nullStrVal(observerIATA),
			direction:    nullStrVal(direction),
			pathJSON:     nullStrVal(pathJSON),
			obsTS:        nullStrVal(obsTimestamp),
			obsRawHex:    nullStrVal(obsRawHex),
			scopeName:    nullStrPtr(scopeName),
			snr:          nullFloatPtr(snrVal),
			rssi:         nullFloatPtr(rssiVal),
			score:        nullIntPtr(scoreVal),
		}
		if obsIDVal.Valid {
			oid := int(obsIDVal.Int64)
			tr.obsID = &oid
		}
		tempRows = append(tempRows, tr)
	}

	if len(tempRows) == 0 {
		return nil, sinceID
	}

	// Now lock and merge into store
	s.mu.Lock()
	defer s.mu.Unlock()

	newMaxID := sinceID
	broadcastTxs := make(map[int]*StoreTx) // track new transmissions for broadcast
	hasNewNodes := false                   // track genuinely new node pubkeys
	var broadcastOrder []int

	// Hoist getCachedNodesAndPM() once before the observation loop to avoid
	// per-observation function calls (review item #1).
	_, cachedPM := s.getCachedNodesAndPM()
	// Hoist atomic graph.Load() out of the per-row loop too (PR #1208
	// carmack #1) — one Load per ingest call, not one per row.
	cachedGraph := s.graph.Load()

	// Decode-window tracking: resolved paths per-obs for broadcast/persist.
	var broadcastRP map[int][]*string // obsID → resolved path (for broadcast/persist)

	hopsSeen := make(map[string]bool) // reused across observations; cleared per use

	for _, r := range tempRows {
		if r.txID > newMaxID {
			newMaxID = r.txID
		}

		tx := s.byHash[r.hash]
		if tx == nil {
			tx = &StoreTx{
				ID:          r.txID,
				RawHex:      r.rawHex,
				Hash:        r.hash,
				FirstSeen:   r.firstSeen,
				LatestSeen:  r.firstSeen,
				RouteType:   r.routeType,
				PayloadType: r.payloadType,
				DecodedJSON: r.decodedJSON,
				ScopeName:   r.scopeName,
				obsKeys:     make(map[string]bool),
				observerSet: make(map[string]bool),
			}
			s.byHash[r.hash] = tx
			s.packets = append(s.packets, tx) // oldest-first; new items go to tail
			s.byTxID[r.txID] = tx
			if r.txID > s.maxTxID {
				s.maxTxID = r.txID
			}
			if s.indexByNode(tx) {
				hasNewNodes = true
			}
			if tx.PayloadType != nil {
				pt := *tx.PayloadType
				// Append to maintain oldest-first order (matches Load ordering)
				// so GetChannelMessages reverse iteration stays correct
				s.byPayloadType[pt] = append(s.byPayloadType[pt], tx)
			}
			s.trackAdvertPubkey(tx)
			s.trackedBytes += estimateStoreTxBytes(tx)

			if _, exists := broadcastTxs[r.txID]; !exists {
				broadcastTxs[r.txID] = tx
				broadcastOrder = append(broadcastOrder, r.txID)
			}
		}

		if r.obsID != nil {
			oid := *r.obsID
			// Dedup (O(1) map lookup)
			dk := r.observerID + "|" + r.pathJSON
			if tx.obsKeys == nil {
				tx.obsKeys = make(map[string]bool)
				tx.observerSet = make(map[string]bool)
			}
			if tx.obsKeys[dk] {
				continue
			}

			obs := &StoreObs{
				ID:             oid,
				TransmissionID: r.txID,
				ObserverID:     r.observerID,
				ObserverName:   r.observerName,
				ObserverIATA:   r.observerIATA,
				Direction:      r.direction,
				SNR:            r.snr,
				RSSI:           r.rssi,
				Score:          r.score,
				PathJSON:       r.pathJSON,
				Timestamp:      normalizeTimestamp(r.obsTS),
			}

			// Resolve path at ingest time using neighbor graph — decode-window discipline:
			// decode once, feed consumers, never store on struct.
			var resolvedPubkeys []string
			var rpForBroadcast []*string
			if r.pathJSON != "" && r.pathJSON != "[]" && cachedPM != nil {
				rpForBroadcast = resolvePathForObs(r.pathJSON, r.observerID, tx, cachedPM, cachedGraph)
				resolvedPubkeys = extractResolvedPubkeys(rpForBroadcast)
				// Single point of truth — see indexResolvedPathHops doc + #1558.
				s.indexResolvedPathHops(tx, resolvedPubkeys, hopsSeen)
			}
			// Stash rpForBroadcast for later broadcast/persist (keyed by obs ID)
			if rpForBroadcast != nil {
				if broadcastRP == nil {
					broadcastRP = make(map[int][]*string)
				}
				broadcastRP[*r.obsID] = rpForBroadcast
			}
			tx.Observations = append(tx.Observations, obs)
			tx.obsKeys[dk] = true
			if obs.ObserverID != "" && !tx.observerSet[obs.ObserverID] {
				tx.observerSet[obs.ObserverID] = true
				tx.UniqueObserverCount++
			}
			tx.ObservationCount++
			if obs.Timestamp > tx.LatestSeen {
				tx.LatestSeen = obs.Timestamp
			}
			s.byObsID[oid] = obs
			if oid > s.maxObsID {
				s.maxObsID = oid
			}
			if r.observerID != "" {
				s.byObserver[r.observerID] = append(s.byObserver[r.observerID], obs)
			}
			s.totalObs++
			s.trackedBytes += estimateStoreObsBytes(obs)
		}
	}

	// Pick best observation for new transmissions
	for _, tx := range broadcastTxs {
		pickBestObservation(tx)
	}

	// Incrementally update precomputed subpath index with new transmissions
	for _, tx := range broadcastTxs {
		if addTxToSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
			s.spTotalPaths++
		}
		addTxToPathHopIndex(s.byPathHop, tx)
	}
	if len(broadcastTxs) > 0 {
		s.invalidateRelayStatsCache()
	}

	// Incrementally update precomputed distance index with new transmissions
	if len(broadcastTxs) > 0 {
		allNodes, pm := s.getCachedNodesAndPM()
		nodeByPk := make(map[string]*nodeInfo, len(allNodes))
		repeaterSet := make(map[string]bool)
		for i := range allNodes {
			n := &allNodes[i]
			nodeByPk[n.PublicKey] = n
			if strings.Contains(strings.ToLower(n.Role), "repeater") {
				repeaterSet[n.PublicKey] = true
			}
		}
		// Per-tx hop resolver: cache reused across txs, context rebound per
		// tx via setContext (#1197 perf fix).
		resolveHop, setContext := s.hopResolverPerTx(pm)
		for _, tx := range broadcastTxs {
			// Per-tx context (sender + observer + unambiguous-prefix anchors)
			// so resolveWithContext tiers 1 and 2 light up. See #1197.
			setContext(buildHopContextPubkeys(tx, pm))
			txHops, txPath := computeDistancesForTx(tx, nodeByPk, repeaterSet, resolveHop)
			if len(txHops) > 0 {
				s.distHops = append(s.distHops, txHops...)
			}
			if txPath != nil {
				s.distPaths = append(s.distPaths, *txPath)
			}
		}
	}

	// Build broadcast maps (same shape as Node.js WS broadcast), one per observation.
	result := make([]map[string]interface{}, 0, len(broadcastOrder))
	for _, txID := range broadcastOrder {
		tx := broadcastTxs[txID]
		// Build decoded object with header.payloadTypeName for live.js
		decoded := map[string]interface{}{
			"header": map[string]interface{}{
				"payloadTypeName": resolvePayloadTypeName(tx.PayloadType),
			},
		}
		if tx.DecodedJSON != "" {
			if payload := tx.ParsedDecoded(); payload != nil {
				// Copy the cached map so broadcast consumers cannot mutate
				// the shared cache or cause a data race (#1871 review).
				cp := make(map[string]interface{}, len(payload))
				for k, v := range payload {
					cp[k] = v
				}
				decoded["payload"] = cp
			}
		}
		// For TRACE packets, decode the full packet to include path.hopsCompleted
		// so the frontend can distinguish completed vs remaining hops (#683).
		if tx.PayloadType != nil && *tx.PayloadType == PayloadTRACE && tx.RawHex != "" {
			if dp, err := DecodePacket(tx.RawHex, false); err == nil {
				decoded["path"] = dp.Path
			}
		}
		for _, obs := range tx.Observations {
			// Build the nested packet object (packets.js checks m.data.packet)
			pkt := map[string]interface{}{
				"id":                tx.ID,
				"raw_hex":           strOrNil(tx.RawHex),
				"hash":              strOrNil(tx.Hash),
				"first_seen":        strOrNil(tx.FirstSeen),
				"timestamp":         strOrNil(tx.FirstSeen),
				"route_type":        intPtrOrNil(tx.RouteType),
				"payload_type":      intPtrOrNil(tx.PayloadType),
				"decoded_json":      strOrNil(tx.DecodedJSON),
				"observer_id":       strOrNil(obs.ObserverID),
				"observer_name":     strOrNil(obs.ObserverName),
				"snr":               floatPtrOrNil(obs.SNR),
				"rssi":              floatPtrOrNil(obs.RSSI),
				"path_json":         strOrNil(obs.PathJSON),
				"direction":         strOrNil(obs.Direction),
				"observation_count": tx.ObservationCount,
			}
			// Use decode-window resolved path for broadcast (never from struct)
			if broadcastRP != nil {
				if rp, ok := broadcastRP[obs.ID]; ok && rp != nil {
					pkt["resolved_path"] = rp
				}
			}
			// Broadcast map: top-level fields for live.js + nested packet for packets.js
			broadcastMap := make(map[string]interface{}, len(pkt)+2)
			for k, v := range pkt {
				broadcastMap[k] = v
			}
			broadcastMap["decoded"] = decoded
			broadcastMap["packet"] = pkt
			result = append(result, broadcastMap)
		}
	}

	// Targeted cache invalidation: only clear caches affected by the ingested
	// data instead of wiping everything on every cycle (fixes #375).
	if len(result) > 0 {
		inv := cacheInvalidation{
			hasNewTransmissions: len(broadcastTxs) > 0,
			hasNewNodes:         hasNewNodes,
		}
		for _, tx := range broadcastTxs {
			if len(tx.Observations) > 0 {
				inv.hasNewObservations = true
			}
			if tx.PayloadType != nil && *tx.PayloadType == 5 {
				inv.hasChannelData = true
			}
			if tx.PathJSON != "" {
				inv.hasNewPaths = true
			}
			if inv.hasNewObservations && inv.hasChannelData && inv.hasNewPaths {
				break // all flags set, no need to continue
			}
		}
		s.invalidateCachesFor(inv)
	}

	// Per #1287 (Option 4): the server NEVER writes to the DB and
	// NEVER mutates the in-memory neighbor graph incrementally. The
	// ingestor owns neighbor_edges; recompNeighborGraph re-reads the
	// snapshot every 60s and atomic-swaps it into s.graph. We also no
	// longer persist resolved_path here — the ingestor (which already
	// sees every observation) owns that write too.
	_ = broadcastRP // resolved path is still computed in-memory (above) for live broadcast; no SQL write.

	return result, newMaxID
}

// IngestNewObservations loads new observations for transmissions already in the
// store. This catches observations that arrive after IngestNewFromDB has already
// advanced past the transmission's ID (fixes #174).
func (s *PacketStore) IngestNewObservations(sinceObsID, limit int) []map[string]interface{} {
	if limit <= 0 {
		limit = 500
	}

	var querySQL string
	obsRHCol2 := ""
	if s.db.hasObsRawHex {
		obsRHCol2 = ", o.raw_hex"
	}
	if s.db.isV3 {
		querySQL = `SELECT o.id, o.transmission_id, obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + obsRHCol2 + `
			FROM observations o
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE o.id > ?
			ORDER BY o.id ASC
			LIMIT ?`
	} else {
		querySQL = `SELECT o.id, o.transmission_id, o.observer_id, o.observer_name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp` + obsRHCol2 + `
			FROM observations o
			LEFT JOIN observers obs ON obs.id = o.observer_id
			WHERE o.id > ?
			ORDER BY o.id ASC
			LIMIT ?`
	}

	rows, err := s.db.conn.Query(querySQL, sinceObsID, limit)
	if err != nil {
		log.Printf("[store] ingest observations query error: %v", err)
		return nil
	}
	defer rows.Close()

	type obsRow struct {
		obsID        int
		txID         int
		observerID   string
		observerName string
		observerIATA string
		direction    string
		snr, rssi    *float64
		score        *int
		pathJSON     string
		rawHex       string
		timestamp    string
	}

	var obsRows []obsRow
	for rows.Next() {
		var oid, txID int
		var observerID, observerName, observerIATA, direction, pathJSON, ts sql.NullString
		var snr, rssi sql.NullFloat64
		var score sql.NullInt64
		var obsRawHex sql.NullString

		scanArgs3 := []interface{}{&oid, &txID, &observerID, &observerName, &observerIATA, &direction,
			&snr, &rssi, &score, &pathJSON, &ts}
		if s.db.hasObsRawHex {
			scanArgs3 = append(scanArgs3, &obsRawHex)
		}
		if err := rows.Scan(scanArgs3...); err != nil {
			continue
		}

		obsRows = append(obsRows, obsRow{
			obsID:        oid,
			txID:         txID,
			observerID:   nullStrVal(observerID),
			observerName: nullStrVal(observerName),
			observerIATA: nullStrVal(observerIATA),
			direction:    nullStrVal(direction),
			snr:          nullFloatPtr(snr),
			rssi:         nullFloatPtr(rssi),
			score:        nullIntPtr(score),
			pathJSON:     nullStrVal(pathJSON),
			rawHex:       nullStrVal(obsRawHex),
			timestamp:    nullStrVal(ts),
		})
	}

	if len(obsRows) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	updatedTxs := make(map[int]*StoreTx)
	broadcastMaps := make([]map[string]interface{}, 0, len(obsRows))
	// Track newly created observations for persistence — only these should be
	// persisted, not all observations of each updated tx (fixes edge count inflation).
	var newObs []*StoreObs
	var obsRPMap map[int][]*string // obsID → resolved path (decode-window)

	// Hoist getCachedNodesAndPM() before the loop — same pattern as IngestNewFromDB (review fix #1).
	_, pm := s.getCachedNodesAndPM()
	graphRef := s.graph.Load()

	hopsSeen := make(map[string]bool) // reused across observations; cleared per use

	for _, r := range obsRows {
		// Already ingested (e.g. by IngestNewFromDB in same cycle)
		if _, exists := s.byObsID[r.obsID]; exists {
			continue
		}

		tx := s.byTxID[r.txID]
		if tx == nil {
			continue // transmission not yet in store
		}

		// Dedup by observer + path (O(1) map lookup)
		dk := r.observerID + "|" + r.pathJSON
		if tx.obsKeys == nil {
			tx.obsKeys = make(map[string]bool)
			tx.observerSet = make(map[string]bool)
		}
		if tx.obsKeys[dk] {
			continue
		}

		obs := &StoreObs{
			ID:             r.obsID,
			TransmissionID: r.txID,
			ObserverID:     r.observerID,
			ObserverName:   r.observerName,
			ObserverIATA:   r.observerIATA,
			Direction:      r.direction,
			SNR:            r.snr,
			RSSI:           r.rssi,
			Score:          r.score,
			PathJSON:       r.pathJSON,
			// obs.RawHex deliberately NOT stored: it duplicates the parent
			// tx.RawHex and enrichObs falls back to tx.RawHex when obs.RawHex
			// == "". Live-polled observations must not re-introduce the dup.
			Timestamp: normalizeTimestamp(r.timestamp),
		}

		// Resolve path at ingest time for late-arriving observations (review item #2).
		// Decode-window discipline: decode, feed consumers, don't store on struct.
		var obsResolvedPath []*string
		if r.pathJSON != "" && r.pathJSON != "[]" {
			if pm != nil {
				obsResolvedPath = resolvePathForObs(r.pathJSON, r.observerID, tx, pm, graphRef)
				pks := extractResolvedPubkeys(obsResolvedPath)
				// Single point of truth — see indexResolvedPathHops doc + #1558.
				s.indexResolvedPathHops(tx, pks, hopsSeen)
			}
		}
		// Stash for broadcast/persist
		if obsResolvedPath != nil {
			if obsRPMap == nil {
				obsRPMap = make(map[int][]*string)
			}
			obsRPMap[r.obsID] = obsResolvedPath
		}

		tx.Observations = append(tx.Observations, obs)
		tx.obsKeys[dk] = true
		if obs.ObserverID != "" && !tx.observerSet[obs.ObserverID] {
			tx.observerSet[obs.ObserverID] = true
			tx.UniqueObserverCount++
		}
		tx.ObservationCount++
		newObs = append(newObs, obs)
		if obs.Timestamp > tx.LatestSeen {
			tx.LatestSeen = obs.Timestamp
		}
		s.byObsID[r.obsID] = obs
		if r.obsID > s.maxObsID {
			s.maxObsID = r.obsID
		}
		if r.observerID != "" {
			s.byObserver[r.observerID] = append(s.byObserver[r.observerID], obs)
		}
		s.totalObs++
		s.trackedBytes += estimateStoreObsBytes(obs)
		updatedTxs[r.txID] = tx

		decoded := map[string]interface{}{
			"header": map[string]interface{}{
				"payloadTypeName": resolvePayloadTypeName(tx.PayloadType),
			},
		}
		if tx.DecodedJSON != "" {
			if payload := tx.ParsedDecoded(); payload != nil {
				// Copy the cached map so broadcast consumers cannot mutate
				// the shared cache or cause a data race (#1871 review).
				cp := make(map[string]interface{}, len(payload))
				for k, v := range payload {
					cp[k] = v
				}
				decoded["payload"] = cp
			}
		}
		// For TRACE packets, decode the full packet to include path.hopsCompleted
		// so the frontend can distinguish completed vs remaining hops (#683).
		if tx.PayloadType != nil && *tx.PayloadType == PayloadTRACE && tx.RawHex != "" {
			if dp, err := DecodePacket(tx.RawHex, false); err == nil {
				decoded["path"] = dp.Path
			}
		}

		pkt := map[string]interface{}{
			"id":                tx.ID,
			"raw_hex":           strOrNil(tx.RawHex),
			"hash":              strOrNil(tx.Hash),
			"first_seen":        strOrNil(tx.FirstSeen),
			"timestamp":         strOrNil(tx.FirstSeen),
			"route_type":        intPtrOrNil(tx.RouteType),
			"payload_type":      intPtrOrNil(tx.PayloadType),
			"decoded_json":      strOrNil(tx.DecodedJSON),
			"observer_id":       strOrNil(obs.ObserverID),
			"observer_name":     strOrNil(obs.ObserverName),
			"snr":               floatPtrOrNil(obs.SNR),
			"rssi":              floatPtrOrNil(obs.RSSI),
			"path_json":         strOrNil(obs.PathJSON),
			"direction":         strOrNil(obs.Direction),
			"observation_count": tx.ObservationCount,
		}
		// Use decode-window resolved path for broadcast
		if obsRPMap != nil {
			if rp, ok := obsRPMap[obs.ID]; ok && rp != nil {
				pkt["resolved_path"] = rp
			}
		}
		broadcastMap := make(map[string]interface{}, len(pkt)+2)
		for k, v := range pkt {
			broadcastMap[k] = v
		}
		broadcastMap["decoded"] = decoded
		broadcastMap["packet"] = pkt
		broadcastMaps = append(broadcastMaps, broadcastMap)
	}

	// Re-pick best observation for updated transmissions and update subpath index
	// if the path changed.
	oldPaths := make(map[int]string, len(updatedTxs))
	for txID, tx := range updatedTxs {
		oldPaths[txID] = tx.PathJSON
	}
	for _, tx := range updatedTxs {
		pickBestObservation(tx)
	}
	pathHopMutated := false
	for txID, tx := range updatedTxs {
		if tx.PathJSON != oldPaths[txID] {
			// Path changed — remove old subpaths, add new ones.
			oldHops := parsePathJSON(oldPaths[txID])
			if len(oldHops) >= 2 {
				// Temporarily set parsedPath to old hops for removal.
				saved, savedFlag := tx.parsedPath, tx.pathParsed
				tx.parsedPath, tx.pathParsed = oldHops, true
				if removeTxFromSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
					s.spTotalPaths--
				}
				tx.parsedPath, tx.pathParsed = saved, savedFlag
			}
			// Remove old path-hop index entries using old hops.
			// Resolved pubkeys remain indexed independently of the best raw path.
			if len(oldHops) > 0 {
				saved, savedFlag := tx.parsedPath, tx.pathParsed
				tx.parsedPath, tx.pathParsed = oldHops, true
				removeTxFromPathHopIndex(s.byPathHop, tx)
				tx.parsedPath, tx.pathParsed = saved, savedFlag
			}
			// pickBestObservation already set pathParsed=false so
			// addTxToSubpathIndex will re-parse the new path.
			if addTxToSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
				s.spTotalPaths++
			}
			addTxToPathHopIndex(s.byPathHop, tx)
			// #1164: coalesce — one invalidate after the loop, not per-tx.
			pathHopMutated = true
		}
	}
	if pathHopMutated {
		s.invalidateRelayStatsCache()
	}

	// Check if any paths changed (used for distance update and cache invalidation).
	hasPathChanges := false
	var changedTxs []*StoreTx
	for txID, tx := range updatedTxs {
		if tx.PathJSON != oldPaths[txID] {
			hasPathChanges = true
			changedTxs = append(changedTxs, tx)
		}
	}
	if len(changedTxs) > 0 {
		s.updateDistanceIndexForTxs(changedTxs)
	}

	if len(updatedTxs) > 0 {
		// Targeted cache invalidation: new observations always affect RF
		// analytics; topology/distance/subpath caches only if paths changed.
		// Channel and hash caches are unaffected by observation-only ingestion.
		s.invalidateCachesFor(cacheInvalidation{
			hasNewObservations: true,
			hasNewPaths:        hasPathChanges,
		})
	}

	// Per #1287 (Option 4): server never writes to the DB and never
	// mutates the in-memory neighbor graph incrementally — the
	// ingestor owns both. recompNeighborGraph re-reads the snapshot
	// every 60s and atomic-swaps into s.graph.
	_ = obsRPMap // resolved path stays in-memory for broadcast; no SQL write.
	_ = newObs
	_ = pm
	_ = graphRef

	return broadcastMaps
}

// MaxTransmissionID returns the highest transmission ID in the store.
func (s *PacketStore) MaxTransmissionID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxTxID
}

// MaxObservationID returns the highest observation ID in the store.
func (s *PacketStore) MaxObservationID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxObsID
}

// --- Internal filter/query helpers ---

// filterPackets applies PacketQuery filters to the in-memory packet list.
func (s *PacketStore) filterPackets(q PacketQuery) []*StoreTx {
	// Fast path: single-key index lookups
	if q.Hash != "" && q.Type == nil && q.Route == nil && q.Observer == "" &&
		q.Region == "" && q.Area == "" && q.Node == "" && q.Channel == "" && q.Since == "" && q.Until == "" {
		h := strings.ToLower(q.Hash)
		tx := s.byHash[h]
		if tx == nil {
			return nil
		}
		return []*StoreTx{tx}
	}
	if q.Observer != "" && q.Type == nil && q.Route == nil &&
		q.Region == "" && q.Area == "" && q.Node == "" && q.Channel == "" && q.Hash == "" && q.Since == "" && q.Until == "" {
		return s.transmissionsForObserver(q.Observer, nil)
	}

	// Pre-compute filter parameters outside the hot loop.
	var (
		filterType  int
		hasType     bool
		filterRoute int
		hasRoute    bool
		filterHash  string
		hasSince    = q.Since != ""
		hasUntil    = q.Until != ""
	)
	if q.Type != nil {
		hasType = true
		filterType = *q.Type
	}
	if q.Route != nil {
		hasRoute = true
		filterRoute = *q.Route
	}
	if q.Hash != "" {
		filterHash = strings.ToLower(q.Hash)
	}
	filterChannel := q.Channel

	// Pre-compute observer set for observer filter.
	var observerSet map[string]bool
	if q.Observer != "" {
		ids := strings.Split(q.Observer, ",")
		observerSet = make(map[string]bool, len(ids))
		for _, id := range ids {
			observerSet[strings.TrimSpace(id)] = true
		}
	}

	// Pre-compute region observer set.
	var regionObservers map[string]bool
	if q.Region != "" {
		regionObservers = s.resolveRegionObservers(q.Region)
		if len(regionObservers) == 0 {
			return nil
		}
	}

	// Pre-compute area node set.
	var areaNodes map[string]bool
	if q.Area != "" {
		areaNodes = s.resolveAreaNodes(q.Area)
	}

	// Pre-compute node filter parameters.
	var nodePK string
	var nodeHashSet map[string]bool
	hasNode := q.Node != ""
	if hasNode {
		nodePK = s.db.resolveNodePubkey(q.Node)
		indexed := s.byNode[nodePK]
		nodeHashSet = make(map[string]bool, len(indexed))
		for _, tx := range indexed {
			nodeHashSet[tx.Hash] = true
		}
	}

	// Determine the source slice. Use index-based source when only node
	// filter is active and an index exists.
	source := s.packets
	if hasNode && !hasType && !hasRoute && q.Observer == "" &&
		filterHash == "" && !hasSince && !hasUntil && q.Region == "" && q.Area == "" && filterChannel == "" {
		if indexed, ok := s.byNode[nodePK]; ok {
			return indexed
		}
	}
	// Single-pass filter: apply all predicates in one scan.
	results := filterTxSlice(source, func(tx *StoreTx) bool {
		// Data integrity: exclude legacy rows missing hash or timestamp (#871)
		if tx.Hash == "" || tx.FirstSeen == "" {
			return false
		}
		if hasType && (tx.PayloadType == nil || *tx.PayloadType != filterType) {
			return false
		}
		if hasRoute && (tx.RouteType == nil || *tx.RouteType != filterRoute) {
			return false
		}
		if filterHash != "" && tx.Hash != filterHash {
			return false
		}
		if hasSince && tx.FirstSeen <= q.Since {
			return false
		}
		if hasUntil && tx.FirstSeen >= q.Until {
			return false
		}
		if observerSet != nil {
			found := false
			for _, obs := range tx.Observations {
				if observerSet[obs.ObserverID] {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		if regionObservers != nil {
			found := false
			for _, obs := range tx.Observations {
				if regionObservers[obs.ObserverID] {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		if areaNodes != nil {
			// Only ADVERT packets carry the originator pubkey (public_key/pubKey).
			// All other packet types (GRP_TXT, TXT_MSG, REQ, …) have encrypted
			// senders so pk == "" and are excluded when an area filter is active.
			d := tx.ParsedDecoded()
			pk, _ := d["public_key"].(string)
			if pk == "" {
				pk, _ = d["pubKey"].(string)
			}
			if pk == "" || !areaNodes[pk] {
				return false
			}
		}
		if hasNode {
			if !nodeHashSet[tx.Hash] {
				return false
			}
		}
		if filterChannel != "" {
			if !packetMatchesChannel(tx, filterChannel) {
				return false
			}
		}
		return true
	})

	return results
}

// packetMatchesChannel returns true if the transmission's decoded payload
// matches the requested channel filter (#812). The filter accepts either a
// plaintext channel name (e.g. "public", "#test") matching decoded.channel,
// or "enc_<HEX>" matching the channelHashHex of an undecryptable GRP_TXT.
func packetMatchesChannel(tx *StoreTx, filterChannel string) bool {
	if tx.PayloadType == nil || *tx.PayloadType != 5 {
		return false
	}
	if tx.DecodedJSON == "" {
		return false
	}
	d := tx.ParsedDecoded()
	if d == nil {
		return false
	}
	if ch, ok := d["channel"].(string); ok && ch != "" {
		if ch == filterChannel {
			return true
		}
	}
	if strings.HasPrefix(filterChannel, "enc_") {
		if hex, ok := d["channelHashHex"].(string); ok && hex != "" {
			if "enc_"+hex == filterChannel {
				return true
			}
		}
	}
	return false
}

// transmissionsForObserver returns unique transmissions for an observer.
func (s *PacketStore) transmissionsForObserver(observerIDs string, from []*StoreTx) []*StoreTx {
	ids := strings.Split(observerIDs, ",")
	idSet := make(map[string]bool, len(ids))
	for i, id := range ids {
		ids[i] = strings.TrimSpace(id)
		idSet[ids[i]] = true
	}
	if from != nil {
		return filterTxSlice(from, func(tx *StoreTx) bool {
			for _, obs := range tx.Observations {
				if idSet[obs.ObserverID] {
					return true
				}
			}
			return false
		})
	}
	// Use byObserver index: union transmissions for all IDs
	seen := make(map[int]bool)
	var result []*StoreTx
	for _, id := range ids {
		for _, obs := range s.byObserver[id] {
			if seen[obs.TransmissionID] {
				continue
			}
			seen[obs.TransmissionID] = true
			tx := s.byTxID[obs.TransmissionID]
			if tx != nil {
				result = append(result, tx)
			}
		}
	}
	return result
}

// resolveRegionObservers returns a set of observer IDs for a given IATA region.
// Results are cached for 30 seconds to avoid repeated DB queries.
// Uses its own mutex (regionObsMu) so callers holding s.mu won't deadlock.
func (s *PacketStore) resolveRegionObservers(region string) map[string]bool {
	s.regionObsMu.Lock()
	defer s.regionObsMu.Unlock()

	if s.regionObsCache != nil && time.Since(s.regionObsCacheTime) < 30*time.Second {
		if m, ok := s.regionObsCache[region]; ok {
			return m
		}
		return s.fetchAndCacheRegionObs(region)
	}
	// Cache expired — rebuild.
	s.regionObsCache = make(map[string]map[string]bool)
	s.regionObsCacheTime = time.Now()

	// Fetch for the requested region and cache it.
	return s.fetchAndCacheRegionObs(region)
}

// fetchAndCacheRegionObs fetches observer IDs for a region from the DB and stores in cache.
// Caller must hold regionObsMu.
func (s *PacketStore) fetchAndCacheRegionObs(region string) map[string]bool {
	if m, ok := s.regionObsCache[region]; ok {
		return m
	}
	ids, err := s.db.GetObserverIdsForRegion(region)
	if err != nil || len(ids) == 0 {
		s.regionObsCache[region] = nil
		return nil
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	s.regionObsCache[region] = m
	return m
}

// resolveAreaNodes returns a set of node pubkeys whose GPS coordinates fall
// inside the named area polygon. Returns nil if the area key is not in config.
// Results are cached per-key for 30 seconds. Uses its own RWMutex so callers
// holding s.mu won't deadlock.
func (s *PacketStore) resolveAreaNodes(areaKey string) map[string]bool {
	if s.config == nil || s.config.Areas == nil {
		return nil
	}
	entry, ok := s.config.Areas[areaKey]
	if !ok {
		return nil
	}

	// Fast path: serve from cache if the per-key TTL is still valid.
	s.areaNodeMu.RLock()
	if t, ok := s.areaNodeCacheTimes[areaKey]; ok && time.Since(t) < 30*time.Second {
		m := s.areaNodeCache[areaKey]
		s.areaNodeMu.RUnlock()
		return m
	}
	s.areaNodeMu.RUnlock()

	// Slow path: query the DB outside any lock, then write back under Lock.
	pks, err := s.db.GetNodePubkeysInArea(entry)
	var m map[string]bool
	if err == nil && len(pks) > 0 {
		m = make(map[string]bool, len(pks))
		for _, pk := range pks {
			m[pk] = true
		}
	}

	s.areaNodeMu.Lock()
	// Re-check in case another goroutine already refreshed while we queried.
	if t, ok := s.areaNodeCacheTimes[areaKey]; !ok || time.Since(t) >= 30*time.Second {
		s.areaNodeCache[areaKey] = m
		s.areaNodeCacheTimes[areaKey] = time.Now()
	} else {
		m = s.areaNodeCache[areaKey]
	}
	s.areaNodeMu.Unlock()
	return m
}

// iataMatchesRegion returns true if iata matches any of the comma-separated
// region codes in regionParam. Comparison is case-insensitive and trim-tolerant.
// Empty iata never matches; empty regionParam never matches.
//
// #804: shared helper used by analytics to attribute transmissions to a node's
// HOME region (derived from observers that hear its zero-hop direct adverts)
// rather than to the observer that happened to relay a packet.
func iataMatchesRegion(iata, regionParam string) bool {
	if iata == "" || regionParam == "" {
		return false
	}
	codes := normalizeRegionCodes(regionParam)
	if len(codes) == 0 {
		return false
	}
	got := strings.TrimSpace(strings.ToUpper(iata))
	if got == "" {
		return false
	}
	for _, c := range codes {
		if c == got {
			return true
		}
	}
	return false
}

// computeNodeHomeRegions returns a pubkey → IATA map deriving each node's
// HOME region from zero-hop DIRECT adverts. A zero-hop direct advert is the
// most authoritative location signal because the path byte is set locally on
// the originating radio and the packet has not been relayed: the observer
// that hears it is necessarily within direct RF range of the originator.
//
// When a node has zero-hop direct adverts heard by observers from multiple
// regions, the most-frequently-observed region wins (geographic plurality).
//
// Caller must hold s.mu (read or write). Returns empty map (not nil) if no
// observers are loaded or no zero-hop direct adverts have been seen.
//
// #804: feeds analytics region-attribution so a multi-byte repeater whose
// flood adverts get relayed across regions is still attributed to its home.
func (s *PacketStore) computeNodeHomeRegions() map[string]string {
	// Build observer → IATA map. observers table is small (≪ packets), so a
	// single DB read here is acceptable; resolveRegionObservers does similar.
	obsIATA := make(map[string]string, 64)
	if s.db != nil {
		if observers, err := s.db.GetObservers(); err == nil {
			for _, o := range observers {
				if o.IATA != nil && *o.IATA != "" {
					obsIATA[o.ID] = strings.TrimSpace(strings.ToUpper(*o.IATA))
				}
			}
		}
	}
	if len(obsIATA) == 0 {
		return map[string]string{}
	}

	// Tally zero-hop direct ADVERT region observations per pubkey.
	type tally struct {
		counts map[string]int
	}
	per := make(map[string]*tally, 256)

	for _, tx := range s.packets {
		if tx.RawHex == "" || len(tx.RawHex) < 4 {
			continue
		}
		if tx.PayloadType == nil || *tx.PayloadType != PayloadADVERT {
			continue
		}
		if tx.DecodedJSON == "" {
			continue
		}
		header, err := strconv.ParseUint(tx.RawHex[:2], 16, 8)
		if err != nil {
			continue
		}
		routeType := header & 0x03
		if routeType != uint64(RouteDirect) && routeType != uint64(RouteTransportDirect) {
			continue
		}
		// Path byte index — for direct/transport-direct it's at offset 1
		// (matches the analytics decoder's pathByteIdx logic).
		if len(tx.RawHex) < 4 {
			continue
		}
		pathByte, err := strconv.ParseUint(tx.RawHex[2:4], 16, 8)
		if err != nil {
			continue
		}
		hopCount := pathByte & 0x3F
		if hopCount != 0 {
			continue
		}

		d := tx.ParsedDecoded()
		if d == nil {
			continue
		}
		pk, _ := d["pubKey"].(string)
		if pk == "" {
			pk, _ = d["public_key"].(string)
		}
		if pk == "" {
			continue
		}

		for _, obs := range tx.Observations {
			iata := obsIATA[obs.ObserverID]
			if iata == "" {
				continue
			}
			t := per[pk]
			if t == nil {
				t = &tally{counts: map[string]int{}}
				per[pk] = t
			}
			t.counts[iata]++
		}
	}

	out := make(map[string]string, len(per))
	for pk, t := range per {
		var bestIATA string
		bestCount := 0
		for iata, n := range t.counts {
			if n > bestCount || (n == bestCount && iata < bestIATA) {
				bestCount = n
				bestIATA = iata
			}
		}
		if bestIATA != "" {
			out[pk] = bestIATA
		}
	}
	return out
}

// enrichObs returns a map with observation fields + transmission fields.
// Looks up the transmission in s.byTxID itself — safe only when the caller
// already holds s.mu (directly, or via a defer'd RLock spanning the call).
// Callers that snapshot observations under RLock and then release it before
// iterating (e.g. handleObserverAnalytics, #1830) must use enrichObsWithTx
// with a tx pointer resolved during that same snapshot instead.
func (s *PacketStore) enrichObs(obs *StoreObs) map[string]interface{} {
	return s.enrichObsWithTx(obs, s.byTxID[obs.TransmissionID])
}

// enrichObsWithTx is enrichObs with the transmission pointer already
// resolved by the caller, instead of looking it up in s.byTxID here. #1830:
// s.byTxID is guarded by s.mu (writes from ingest/eviction); reading it
// without holding at least RLock races with those writers — Go maps can
// panic with "concurrent map read and map write" during a rehash, not just
// fail under -race. Callers that need to read byTxID after releasing their
// RLock (to keep JSON decode / enrichment off the hot lock, per #1481)
// should resolve the *StoreTx for each observation during their RLock-held
// snapshot and pass it in here.
func (s *PacketStore) enrichObsWithTx(obs *StoreObs, tx *StoreTx) map[string]interface{} {
	m := map[string]interface{}{
		"id":            obs.ID,
		"timestamp":     strOrNil(obs.Timestamp),
		"observer_id":   strOrNil(obs.ObserverID),
		"observer_name": strOrNil(obs.ObserverName),
		"observer_iata": strOrNil(obs.ObserverIATA),
		"direction":     strOrNil(obs.Direction),
		"snr":           floatPtrOrNil(obs.SNR),
		"rssi":          floatPtrOrNil(obs.RSSI),
		"score":         intPtrOrNil(obs.Score),
		"path_json":     strOrNil(obs.PathJSON),
	}
	// On-demand SQL fetch for resolved_path
	rp := s.fetchResolvedPathForObs(obs.ID)
	if rp != nil {
		m["resolved_path"] = rp
	}

	if tx != nil {
		m["hash"] = strOrNil(tx.Hash)
		// raw_hex comes from the transmission (#881). obs.RawHex is no longer
		// retained when loading/ingesting the store — it duplicated this exact
		// value (same content hash ⇒ same frame) and at ~10.6 observations/tx
		// wasted ~98MB on a live ~1.7M-observation store. The obs.RawHex branch
		// stays for callers that build a StoreObs from a response map.
		if obs.RawHex != "" {
			m["raw_hex"] = obs.RawHex
		} else {
			m["raw_hex"] = strOrNil(tx.RawHex)
		}
		m["payload_type"] = intPtrOrNil(tx.PayloadType)
		m["route_type"] = intPtrOrNil(tx.RouteType)
		m["decoded_json"] = strOrNil(tx.DecodedJSON)
	}

	return m
}

// --- Conversion helpers ---

// txToMap converts a StoreTx to the map shape matching scanTransmissionRow output.
func txToMap(tx *StoreTx, includeObservations ...bool) map[string]interface{} {
	m := map[string]interface{}{
		"id":                tx.ID,
		"raw_hex":           strOrNil(tx.RawHex),
		"hash":              strOrNil(tx.Hash),
		"first_seen":        strOrNil(tx.FirstSeen),
		"timestamp":         strOrNil(tx.FirstSeen),
		"route_type":        intPtrOrNil(tx.RouteType),
		"payload_type":      intPtrOrNil(tx.PayloadType),
		"decoded_json":      strOrNil(tx.DecodedJSON),
		"observation_count": tx.ObservationCount,
		"observer_id":       strOrNil(tx.ObserverID),
		"observer_name":     strOrNil(tx.ObserverName),
		"observer_iata":     strOrNil(tx.ObserverIATA),
		"snr":               floatPtrOrNil(tx.SNR),
		"rssi":              floatPtrOrNil(tx.RSSI),
		"path_json":         strOrNil(tx.PathJSON),
		"direction":         strOrNil(tx.Direction),
		"scope_name":        strPtrOrNil(tx.ScopeName),
	}
	// Include parsed path array to match Node.js output shape
	if hops := txGetParsedPath(tx); len(hops) > 0 {
		m["_parsedPath"] = hops
	} else {
		m["_parsedPath"] = nil
	}
	// Only build observation sub-maps when caller requests them (avoids allocations that get stripped)
	if len(includeObservations) > 0 && includeObservations[0] {
		obs := make([]map[string]interface{}, 0, len(tx.Observations))
		for _, o := range tx.Observations {
			om := map[string]interface{}{
				"id":            o.ID,
				"observer_id":   strOrNil(o.ObserverID),
				"observer_name": strOrNil(o.ObserverName),
				"observer_iata": strOrNil(o.ObserverIATA),
				"snr":           floatPtrOrNil(o.SNR),
				"rssi":          floatPtrOrNil(o.RSSI),
				"path_json":     strOrNil(o.PathJSON),
				"timestamp":     strOrNil(o.Timestamp),
				"direction":     strOrNil(o.Direction),
			}
			obs = append(obs, om)
		}
		m["observations"] = obs
	}
	return m
}

// txToMapWithRP is like txToMap but also fetches resolved_path on demand from the store.
func (s *PacketStore) txToMapWithRP(tx *StoreTx, includeObservations ...bool) map[string]interface{} {
	m := txToMap(tx, includeObservations...)
	// On-demand SQL fetch for resolved_path
	rp := s.fetchResolvedPathForTxBest(tx)
	if rp != nil {
		m["resolved_path"] = rp
	}
	// Also add resolved_path to observation sub-maps if present
	if len(includeObservations) > 0 && includeObservations[0] {
		if obsList, ok := m["observations"].([]map[string]interface{}); ok {
			for i, o := range tx.Observations {
				if i < len(obsList) {
					obsRP := s.fetchResolvedPathForObs(o.ID)
					if obsRP != nil {
						obsList[i]["resolved_path"] = obsRP
					}
				}
			}
		}
	}
	return m
}

func strOrNil(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// normalizeTimestamp converts SQLite datetime format ("YYYY-MM-DD HH:MM:SS")
// to ISO 8601 ("YYYY-MM-DDTHH:MM:SSZ"). Already-ISO strings pass through.
func normalizeTimestamp(s string) string {
	if s == "" {
		return s
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return s
}

func strPtrOrNil(p *string) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func intPtrOrNil(p *int) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func floatPtrOrNil(p *float64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func nullIntPtr(ni sql.NullInt64) *int {
	if ni.Valid {
		v := int(ni.Int64)
		return &v
	}
	return nil
}

func nullFloatPtr(nf sql.NullFloat64) *float64 {
	if nf.Valid {
		return &nf.Float64
	}
	return nil
}

// resolvePayloadTypeName returns the firmware-standard name for a payload_type.
func resolvePayloadTypeName(pt *int) string {
	if pt == nil {
		return "UNKNOWN"
	}
	if name, ok := payloadTypeNames[*pt]; ok {
		return name
	}
	return fmt.Sprintf("UNK(%d)", *pt)
}

// txGetParsedPath returns cached parsed path hops, parsing on first call.
func txGetParsedPath(tx *StoreTx) []string {
	if tx.pathParsed {
		return tx.parsedPath
	}
	tx.parsedPath = parsePathJSON(tx.PathJSON)
	tx.pathParsed = true
	return tx.parsedPath
}

// addTxToSubpathIndex extracts all raw subpaths (lengths 2–8) from tx and
// increments their counts in the index.  Returns true if the tx contributed
// (path had ≥ 2 hops).
func addTxToSubpathIndex(idx map[string]int, tx *StoreTx) bool {
	return addTxToSubpathIndexFull(idx, nil, tx)
}

// addTxToSubpathIndexFull is like addTxToSubpathIndex but also appends
// tx to txIdx for each subpath key (if txIdx is non-nil).
func addTxToSubpathIndexFull(idx map[string]int, txIdx map[string][]*StoreTx, tx *StoreTx) bool {
	hops := txGetParsedPath(tx)
	if len(hops) < 2 {
		return false
	}
	maxL := min(8, len(hops))
	for l := 2; l <= maxL; l++ {
		for start := 0; start <= len(hops)-l; start++ {
			key := strings.ToLower(strings.Join(hops[start:start+l], ","))
			idx[key]++
			if txIdx != nil {
				txIdx[key] = append(txIdx[key], tx)
			}
		}
	}
	return true
}

// removeTxFromSubpathIndex is the inverse of addTxToSubpathIndex — it
// decrements counts for all raw subpaths of tx.  Returns true if the tx
// had a path.
func removeTxFromSubpathIndex(idx map[string]int, tx *StoreTx) bool {
	return removeTxFromSubpathIndexFull(idx, nil, tx)
}

// removeTxFromSubpathIndexFull is like removeTxFromSubpathIndex but also
// removes tx from txIdx for each subpath key (if txIdx is non-nil).
func removeTxFromSubpathIndexFull(idx map[string]int, txIdx map[string][]*StoreTx, tx *StoreTx) bool {
	hops := txGetParsedPath(tx)
	if len(hops) < 2 {
		return false
	}
	maxL := min(8, len(hops))
	for l := 2; l <= maxL; l++ {
		for start := 0; start <= len(hops)-l; start++ {
			key := strings.ToLower(strings.Join(hops[start:start+l], ","))
			idx[key]--
			if idx[key] <= 0 {
				delete(idx, key)
			}
			if txIdx != nil {
				txs := txIdx[key]
				for i, t := range txs {
					if t == tx {
						txIdx[key] = append(txs[:i], txs[i+1:]...)
						break
					}
				}
				if len(txIdx[key]) == 0 {
					delete(txIdx, key)
				}
			}
		}
	}
	return true
}

// buildSubpathIndex scans all packets and populates spIndex + spTotalPaths.
// Must be called with s.mu held.
func (s *PacketStore) buildSubpathIndex() {
	s.spIndex = make(map[string]int, 4096)
	s.spTxIndex = make(map[string][]*StoreTx, 4096)
	s.spTotalPaths = 0
	for _, tx := range s.packets {
		if addTxToSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
			s.spTotalPaths++
		}
	}
	log.Printf("[store] Built subpath index: %d unique raw subpaths from %d paths",
		len(s.spIndex), s.spTotalPaths)
}

// buildPathHopIndex rebuilds byPathHop: raw wire hops from every packet's
// path_json, plus the resolved full-pubkey hops carried over from the
// previous index (see retainResolvedPathHops).
// Must be called with s.mu held.
func (s *PacketStore) buildPathHopIndex() {
	prev := s.byPathHop
	s.byPathHop = make(map[string][]*StoreTx, max(4096, len(prev)))
	for _, tx := range s.packets {
		addTxToPathHopIndex(s.byPathHop, tx)
	}
	retained := s.retainResolvedPathHops(prev)
	log.Printf("[store] Built path-hop index: %d unique keys (%d resolved-hop entries retained)",
		len(s.byPathHop), retained)
}

// retainResolvedPathHops re-merges the entries of a pre-rebuild byPathHop
// that the raw-hop pass above cannot reproduce: the resolved full-pubkey
// keys fed by indexResolvedPathHops. Their pubkey strings are retained
// nowhere — #800 dropped the per-StoreTx ResolvedPath field in favour of a
// hash-only membership index — so a plain rebuild silently discarded every
// resolved relay attribution, leaving relay counts and transported scopes
// empty after each cold load until live ingestion refilled them (#1904).
//
// Only transmissions still in s.packets are carried over. Eviction also
// prunes raw and resolved keys (#1908); this membership check prevents a
// rebuild from reintroducing stale entries from the previous index.
//
// Cost is O(entries in prev) with one reused scratch map, and it runs only
// where buildPathHopIndex already runs — cold load and background-fill
// completion — never on an ingest or request path.
//
// Returns the number of entries carried over. Must be called with s.mu held,
// after s.byPathHop has been rebuilt from raw hops.
func (s *PacketStore) retainResolvedPathHops(prev map[string][]*StoreTx) int {
	if len(prev) == 0 {
		return 0
	}
	live := make(map[*StoreTx]struct{}, len(s.packets))
	for _, tx := range s.packets {
		live[tx] = struct{}{}
	}

	// Reused across keys (cleared per key) so a large index does not churn
	// one map allocation per key. Guards against both a key that the raw
	// pass already produced and repeated appends of the same tx in prev —
	// indexResolvedPathHops dedups within a call, not across the several
	// observations of one transmission.
	seen := make(map[*StoreTx]struct{}, 16)
	retained := 0
	for key, list := range prev {
		if len(list) == 0 {
			continue
		}
		clear(seen)
		for _, tx := range s.byPathHop[key] {
			seen[tx] = struct{}{}
		}
		for _, tx := range list {
			if tx == nil {
				continue
			}
			if _, ok := live[tx]; !ok {
				continue
			}
			if _, dup := seen[tx]; dup {
				continue
			}
			seen[tx] = struct{}{}
			s.byPathHop[key] = append(s.byPathHop[key], tx)
			retained++
		}
	}
	return retained
}

// addTxToPathHopIndex indexes a transmission under each unique raw hop key.
// Resolved pubkey keys are handled by the decode-window via feedDecodeWindowConsumers.
func addTxToPathHopIndex(idx map[string][]*StoreTx, tx *StoreTx) {
	hops := txGetParsedPath(tx)
	if len(hops) == 0 {
		return
	}
	seen := make(map[string]bool, len(hops))
	for _, hop := range hops {
		key := strings.ToLower(hop)
		if !seen[key] {
			seen[key] = true
			idx[key] = append(idx[key], tx)
		}
	}
}

// addTxToRelayTimeIndex records the relay timestamp for each resolved pubkey.
// pubkeys is the pre-extracted list (use extractResolvedPubkeys on the decoded path).
// Maintains sorted ascending order for O(log n) window queries.
// Must be called with s.mu held (or during build before store is live).
func addTxToRelayTimeIndex(idx map[string][]int64, firstSeen string, pubkeys []string) {
	if len(pubkeys) == 0 {
		return
	}
	ms, err := time.Parse(time.RFC3339, firstSeen)
	if err != nil {
		return
	}
	millis := ms.UnixMilli()
	seen := make(map[string]bool, len(pubkeys))
	for _, pk := range pubkeys {
		pk = strings.ToLower(pk)
		if pk == "" || seen[pk] {
			continue
		}
		seen[pk] = true
		slice := idx[pk]
		i := sort.Search(len(slice), func(j int) bool { return slice[j] >= millis })
		if i < len(slice) && slice[i] == millis {
			continue // idempotent
		}
		slice = append(slice, 0)
		copy(slice[i+1:], slice[i:])
		slice[i] = millis
		idx[pk] = slice
	}
}

// removeFromRelayTimeIndex removes the relay timestamp for each resolved pubkey.
// Inverse of addTxToRelayTimeIndex.
func removeFromRelayTimeIndex(idx map[string][]int64, firstSeen string, pubkeys []string) {
	if len(pubkeys) == 0 {
		return
	}
	ms, err := time.Parse(time.RFC3339, firstSeen)
	if err != nil {
		return
	}
	millis := ms.UnixMilli()
	seen := make(map[string]bool, len(pubkeys))
	for _, pk := range pubkeys {
		pk = strings.ToLower(pk)
		if pk == "" || seen[pk] {
			continue
		}
		seen[pk] = true
		slice := idx[pk]
		i := sort.Search(len(slice), func(j int) bool { return slice[j] >= millis })
		if i < len(slice) && slice[i] == millis {
			newSlice := make([]int64, 0, len(slice)-1)
			newSlice = append(newSlice, slice[:i]...)
			newSlice = append(newSlice, slice[i+1:]...)
			idx[pk] = newSlice
			if len(newSlice) == 0 {
				delete(idx, pk)
			}
		}
	}
}

// relayMetrics computes relay_count_1h, relay_count_24h, and last_relayed from a
// sorted unix-millis slice. now is time.Now().UnixMilli(). O(log n).
func relayMetrics(times []int64, now int64) (count1h, count24h int, lastRelayed string) {
	if len(times) == 0 {
		return 0, 0, ""
	}
	i1h := sort.Search(len(times), func(i int) bool { return times[i] >= now-3600000 })
	i24h := sort.Search(len(times), func(i int) bool { return times[i] >= now-86400000 })
	count1h = len(times) - i1h
	count24h = len(times) - i24h
	lastRelayed = time.UnixMilli(times[len(times)-1]).UTC().Format(time.RFC3339)
	return
}

// removeTxFromPathHopIndex removes a transmission from all its raw path-hop index entries.
// Used when the best raw path changes; eviction filters all keys in one batch.
func removeTxFromPathHopIndex(idx map[string][]*StoreTx, tx *StoreTx) {
	hops := txGetParsedPath(tx)
	if len(hops) == 0 {
		return
	}
	seen := make(map[string]bool, len(hops))
	for _, hop := range hops {
		key := strings.ToLower(hop)
		if !seen[key] {
			seen[key] = true
			removeTxFromSlice(idx, key, tx)
		}
	}
}

// addResolvedPubkeysToPathHopIndex appends tx into byPathHop under each
// resolved pubkey key that isn't already present as a raw hop. Mutating
// byPathHop here MUST be paired with invalidateRelayStatsCache so the
// cached batch relay stats don't go stale for up to relayStatsCacheTTL.
// hopsSeen is a scratch map the caller can reuse across calls (it will
// be cleared on entry).
//
// Must be called with s.mu held.
func (s *PacketStore) addResolvedPubkeysToPathHopIndex(tx *StoreTx, pubkeys []string, hopsSeen map[string]bool) bool {
	if len(pubkeys) == 0 {
		return false
	}
	clear(hopsSeen)
	for _, hop := range txGetParsedPath(tx) {
		hopsSeen[strings.ToLower(hop)] = true
	}
	mutated := false
	for _, pk := range pubkeys {
		if !hopsSeen[pk] {
			hopsSeen[pk] = true
			s.byPathHop[pk] = append(s.byPathHop[pk], tx)
			mutated = true
		}
	}
	// Mutating byPathHop invalidates the batch relay-stats cache (#1164).
	if mutated {
		s.invalidateRelayStatsCache()
	}
	return mutated
}

// invalidateRelayStatsCache drops the cached batch relay-stats result so
// the next call to GetRepeaterNodeStatsBatchCached recomputes from scratch.
// Call this whenever byPathHop changes (add or remove). Safe to call while
// holding s.mu — relayStatsCacheMu is never acquired while holding relayStatsCacheMu,
// so there is no lock-ordering cycle.
func (s *PacketStore) invalidateRelayStatsCache() {
	s.relayStatsCacheMu.Lock()
	s.relayStatsCache = nil
	s.relayStatsCacheMu.Unlock()
}

// removeTxFromSlice removes tx from idx[key] by ID, deleting the key if empty.
func removeTxFromSlice(idx map[string][]*StoreTx, key string, tx *StoreTx) {
	list := idx[key]
	for i, t := range list {
		if t.ID == tx.ID {
			idx[key] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(idx[key]) == 0 {
		delete(idx, key)
	}
}

// updateDistanceIndexForTxs removes old distance records for the given
// transmissions and recomputes them. Builds lookup maps once, amortising the
// cost across all changed txs in a single ingest cycle. Must be called with
// s.mu held.
func (s *PacketStore) updateDistanceIndexForTxs(txs []*StoreTx) {
	// Remove old records for all changed txs first.
	removeSet := make(map[*StoreTx]bool, len(txs))
	for _, tx := range txs {
		removeSet[tx] = true
	}
	n := 0
	for _, r := range s.distHops {
		if !removeSet[r.tx] {
			s.distHops[n] = r
			n++
		}
	}
	s.distHops = s.distHops[:n]
	n = 0
	for _, r := range s.distPaths {
		if !removeSet[r.tx] {
			s.distPaths[n] = r
			n++
		}
	}
	s.distPaths = s.distPaths[:n]

	// Build lookup maps once.
	allNodes, pm := s.getCachedNodesAndPM()
	nodeByPk := make(map[string]*nodeInfo, len(allNodes))
	repeaterSet := make(map[string]bool)
	for i := range allNodes {
		nd := &allNodes[i]
		nodeByPk[nd.PublicKey] = nd
		if strings.Contains(strings.ToLower(nd.Role), "repeater") {
			repeaterSet[nd.PublicKey] = true
		}
	}
	// Per-tx hop resolver shared across the recompute loop (#1197 perf).
	resolveHop, setContext := s.hopResolverPerTx(pm)
	// Recompute distance records for each changed tx.
	for _, tx := range txs {
		// Per-tx context for hop disambiguation (#1197).
		setContext(buildHopContextPubkeys(tx, pm))
		txHops, txPath := computeDistancesForTx(tx, nodeByPk, repeaterSet, resolveHop)
		if len(txHops) > 0 {
			s.distHops = append(s.distHops, txHops...)
		}
		if txPath != nil {
			s.distPaths = append(s.distPaths, *txPath)
		}
	}
}

// DistanceIndexBuilt reports whether the distance analytics index has
// been constructed. Used by tests and /api/perf to verify the lazy
// build invariant from issue #1011 (eager Load() build removed).
func (s *PacketStore) DistanceIndexBuilt() bool {
	s.distLazyMu.Lock()
	defer s.distLazyMu.Unlock()
	return s.distLazyBuilt
}

// DistanceIndexBuilding reports whether a lazy distance-index build
// is currently in flight. Used by the /api/analytics/distance handler
// to serve 202 + Retry-After to concurrent first-window requests
// (#1011).
func (s *PacketStore) DistanceIndexBuilding() bool {
	s.distLazyMu.Lock()
	defer s.distLazyMu.Unlock()
	return s.distLazyBuilding
}

// TriggerDistanceIndexBuild kicks off a lazy build of the distance
// index in a background goroutine if one is not already running and
// the debounce policy permits. Returns immediately. Idempotent:
// concurrent callers see only one build, gated by sync.Once.
//
// Debounce policy (#1011 triage Fix path): rebuild if Δobs > 5% since
// the last build OR at most once per 5 minutes — whichever is more
// restrictive. The first-ever build always runs.
func (s *PacketStore) TriggerDistanceIndexBuild() {
	s.distLazyMu.Lock()
	if s.distLazyBuilding {
		s.distLazyMu.Unlock()
		return
	}
	// Debounce: if a build has already completed, suppress re-trigger
	// unless Δobs > 5% or >5min has elapsed.
	if s.distLazyBuilt {
		s.mu.RLock()
		curObs := s.totalObs
		s.mu.RUnlock()
		elapsed := time.Since(s.distLazyLastBuilt)
		deltaPct := 0.0
		if s.distLazyLastObs > 0 {
			deltaPct = float64(curObs-s.distLazyLastObs) / float64(s.distLazyLastObs)
		}
		if elapsed < 5*time.Minute && deltaPct < 0.05 {
			s.distLazyMu.Unlock()
			return
		}
		// Reset the gate so a new build can fire.
		s.distLazyOnce = sync.Once{}
		s.distLazyBuilt = false
	}
	s.distLazyMu.Unlock()

	// Fire-and-forget; sync.Once collapses concurrent goroutines into
	// a single build. The Once is reset above (under the mutex) before
	// each rebuild cycle.
	go s.distLazyOnce.Do(func() {
		s.distLazyMu.Lock()
		s.distLazyBuilding = true
		s.distLazyMu.Unlock()

		if s.distanceBuildHook != nil {
			s.distanceBuildHook() // test seam: hold the build window open
		}

		s.mu.Lock()
		s.buildDistanceIndex()
		obsAtBuild := s.totalObs
		s.mu.Unlock()

		s.distLazyMu.Lock()
		s.distLazyBuilding = false
		s.distLazyBuilt = true
		s.distLazyLastBuilt = time.Now()
		s.distLazyLastObs = obsAtBuild
		s.distLazyMu.Unlock()
	})
}

// buildDistanceIndex precomputes haversine distances for all packets.
// Must be called with s.mu held (Lock).
func (s *PacketStore) buildDistanceIndex() {
	allNodes, pm := s.getCachedNodesAndPM()
	nodeByPk := make(map[string]*nodeInfo, len(allNodes))
	repeaterSet := make(map[string]bool)
	for i := range allNodes {
		n := &allNodes[i]
		nodeByPk[n.PublicKey] = n
		if strings.Contains(strings.ToLower(n.Role), "repeater") {
			repeaterSet[n.PublicKey] = true
		}
	}

	hops := make([]distHopRecord, 0, len(s.packets))
	paths := make([]distPathRecord, 0, len(s.packets)/2)

	// Per-tx hop resolver shared across the per-tx loop (#1197 perf).
	resolveHop, setContext := s.hopResolverPerTx(pm)
	for _, tx := range s.packets {
		// Per-tx context for hop disambiguation (#1197).
		setContext(buildHopContextPubkeys(tx, pm))
		txHops, txPath := computeDistancesForTx(tx, nodeByPk, repeaterSet, resolveHop)
		if len(txHops) > 0 {
			hops = append(hops, txHops...)
		}
		if txPath != nil {
			paths = append(paths, *txPath)
		}
	}

	s.distHops = hops
	s.distPaths = paths
	log.Printf("[store] Built distance index: %d hop records, %d path records",
		len(s.distHops), len(s.distPaths))
}

// Self-accounting memory estimation constants.
// These estimate the in-memory cost of StoreTx and StoreObs structs including
// map/index overhead. They don't need to be exact — just proportional to actual
// usage and independent of GC state.
//
// Issue #743: Previous estimates missed major per-packet allocations:
// - spTxIndex: O(path²) entries per tx (50-150MB at scale)
// - Per-tx maps: obsKeys, observerSet (~11MB at scale)
// - byPathHop index entries (20-40MB at scale)
// Note: ResolvedPath per-obs overhead eliminated by #800 refactor.
const (
	storeTxBaseBytes  = 384 // StoreTx struct fields + map headers + sync.Once + string headers
	storeObsBaseBytes = 192 // StoreObs struct fields + string headers
	strHdr            = 16  // Go string header (ptr + len) on a 64-bit build; used by GetStoreMemoryBreakdown
	indexEntryBytes   = 48  // average cost of one index map entry (key + pointer + bucket overhead)
	numIndexesPerTx   = 5   // byHash, byTxID, byNode, byPayloadType, nodeHashes entries
	numIndexesPerObs  = 2   // byObsID, byObserver entries

	// Per-tx map overhead (obsKeys + observerSet): map header + initial buckets
	perTxMapsBytes = 200

	// Per path hop: byPathHop index entry (pointer + map bucket)
	perPathHopBytes = 50

	// Per subpath entry in spTxIndex: string key + slice append + pointer
	perSubpathEntryBytes = 40
)

// estimateStoreTxBytes returns the estimated memory cost of a StoreTx (excluding observations).
// Includes per-tx maps (obsKeys, observerSet), byPathHop entries, and spTxIndex subpath entries.
func estimateStoreTxBytes(tx *StoreTx) int64 {
	base := int64(storeTxBaseBytes)
	base += int64(len(tx.RawHex) + len(tx.Hash) + len(tx.DecodedJSON) + len(tx.PathJSON))
	base += int64(numIndexesPerTx * indexEntryBytes)

	// Per-tx maps: obsKeys + observerSet
	base += perTxMapsBytes

	// Path-dependent costs
	hops := int64(len(txGetParsedPath(tx)))
	base += hops * perPathHopBytes

	// spTxIndex: O(path²) subpath combinations
	if hops > 1 {
		subpaths := hops * (hops - 1) / 2
		base += subpaths * perSubpathEntryBytes
	}

	return base
}

// estimateStoreTxBytesTypical returns the estimated memory cost of a typical
// transmission with the given number of observations. Used for budget
// calculation during bounded cold load (no actual StoreTx needed).
func estimateStoreTxBytesTypical(numObs int) int64 {
	// Typical tx: ~64 byte hash, ~200 byte decoded JSON, ~40 byte path, 3 hops
	base := int64(storeTxBaseBytes) + 64 + 200 + 40
	base += int64(numIndexesPerTx * indexEntryBytes)
	base += perTxMapsBytes
	hops := int64(3)
	base += hops * perPathHopBytes
	base += (hops * (hops - 1) / 2) * perSubpathEntryBytes
	// Add observation costs
	obsBase := int64(storeObsBaseBytes) + 30 + 30 + 60 // observer ID + name + path
	obsBase += int64(numIndexesPerObs * indexEntryBytes)
	// No per-obs ResolvedPath overhead (#800)
	base += int64(numObs) * obsBase
	return base
}

// estimateStoreObsBytes returns the estimated memory cost of a StoreObs.
// ResolvedPath membership index overhead is tracked separately.
func estimateStoreObsBytes(obs *StoreObs) int64 {
	base := int64(storeObsBaseBytes)
	base += int64(len(obs.PathJSON) + len(obs.ObserverID))
	base += int64(numIndexesPerObs * indexEntryBytes)
	// ResolvedPath field removed (#800) — no per-obs RP overhead
	return base
}

// estimatedMemoryMB returns current Go heap allocation in MB.
// Kept for stats/debug endpoints only — NOT used in eviction decisions.
// In tests, memoryEstimator can be set to inject a deterministic value.
// Caches the result for 5 seconds because runtime.ReadMemStats() stops the
// world and this is called from stats/debug endpoints that may be polled.
// The cache is per-store (not package-level) so that test helpers constructing
// &PacketStore{...} directly get independent cache state, avoiding
// order-dependent test failures from a shared global cache.
func (s *PacketStore) estimatedMemoryMB() float64 {
	if s.memoryEstimator != nil {
		return s.memoryEstimator()
	}
	s.estMemMu.Lock()
	defer s.estMemMu.Unlock()
	if time.Since(s.estMemAt) > 5*time.Second {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		s.estMemVal = float64(ms.HeapAlloc) / 1048576.0
		s.estMemAt = time.Now()
	}
	return s.estMemVal
}

// trackedMemoryMB returns the self-accounted packet store memory in MB.
func (s *PacketStore) trackedMemoryMB() float64 {
	return float64(s.trackedBytes) / 1048576.0
}

// EvictStale removes packets older than the retention window and/or exceeding
// the memory cap. Must be called with s.mu held (Lock). Returns the number of
// packets evicted.
// evictionCandidateTxIDs determines which tx IDs would be evicted and returns them.
// Must be called under s.mu.Lock (or RLock). Does NOT modify any state.
func (s *PacketStore) evictionCandidateTxIDs() []int {
	if s.retentionHours <= 0 && s.maxMemoryMB <= 0 {
		return nil
	}
	cutoffIdx := 0
	if s.retentionHours > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(s.retentionHours * float64(time.Hour))).Format(time.RFC3339)
		for cutoffIdx < len(s.packets) && s.packets[cutoffIdx].FirstSeen < cutoff {
			cutoffIdx++
		}
	}
	if s.maxMemoryMB > 0 {
		highWatermark := int64(s.maxMemoryMB) * 1048576
		lowWatermark := int64(float64(highWatermark) * 0.85)
		if s.trackedBytes > highWatermark && len(s.packets) > 0 {
			var bytesToEvict int64
			memCutoff := cutoffIdx
			for memCutoff < len(s.packets) && (s.trackedBytes-bytesToEvict) > lowWatermark {
				tx := s.packets[memCutoff]
				bytesToEvict += estimateStoreTxBytes(tx)
				for _, obs := range tx.Observations {
					bytesToEvict += estimateStoreObsBytes(obs)
				}
				memCutoff++
			}
			maxEvict := len(s.packets) / 4
			if maxEvict < 1 {
				maxEvict = 1
			}
			if memCutoff > maxEvict {
				memCutoff = maxEvict
			}
			if memCutoff > cutoffIdx {
				cutoffIdx = memCutoff
			}
		}
	}
	if cutoffIdx == 0 || cutoffIdx > len(s.packets) {
		return nil
	}
	ids := make([]int, cutoffIdx)
	for i := 0; i < cutoffIdx; i++ {
		ids[i] = s.packets[i].ID
	}
	return ids
}

// EvictStaleWithRP runs eviction using pre-fetched resolved pubkeys.
// rpBatch may be nil (in which case resolved pubkey cleanup for byNode/nodeHashes is skipped).
// Must be called under s.mu.Lock.
func (s *PacketStore) EvictStaleWithRP(rpBatch map[int][]string) int {
	return s.evictStaleInternal(rpBatch)
}

// EvictStale runs eviction, fetching resolved pubkeys inline (SQL under lock).
// Prefer RunEviction() which batches the SQL outside the lock.
// Must be called under s.mu.Lock.
func (s *PacketStore) EvictStale() int {
	return s.evictStaleInternal(nil)
}

func (s *PacketStore) evictStaleInternal(rpBatch map[int][]string) int {
	if s.retentionHours <= 0 && s.maxMemoryMB <= 0 {
		return 0
	}

	cutoffIdx := 0

	// Time-based eviction: find how many packets from the head are too old
	if s.retentionHours > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(s.retentionHours * float64(time.Hour))).Format(time.RFC3339)
		for cutoffIdx < len(s.packets) && s.packets[cutoffIdx].FirstSeen < cutoff {
			cutoffIdx++
		}
	}

	// Memory-based eviction: use self-accounted trackedBytes with watermark hysteresis.
	// High watermark = maxMemoryMB (trigger), low watermark = 85% (stop).
	// Safety cap: never evict more than 25% of packets in a single pass.
	if s.maxMemoryMB > 0 {
		highWatermark := int64(s.maxMemoryMB) * 1048576
		lowWatermark := int64(float64(highWatermark) * 0.85)
		if s.trackedBytes > highWatermark && len(s.packets) > 0 {
			// Evict from head until trackedBytes would drop below low watermark
			var bytesToEvict int64
			memCutoff := cutoffIdx
			for memCutoff < len(s.packets) && (s.trackedBytes-bytesToEvict) > lowWatermark {
				tx := s.packets[memCutoff]
				bytesToEvict += estimateStoreTxBytes(tx)
				for _, obs := range tx.Observations {
					bytesToEvict += estimateStoreObsBytes(obs)
				}
				memCutoff++
			}
			// Safety cap: never evict more than 25% in a single pass
			maxEvict := len(s.packets) / 4
			if maxEvict < 1 {
				maxEvict = 1
			}
			if memCutoff > maxEvict {
				memCutoff = maxEvict
			}
			if memCutoff > cutoffIdx {
				cutoffIdx = memCutoff
			}
		}
	}

	if cutoffIdx == 0 {
		return 0
	}
	if cutoffIdx > len(s.packets) {
		cutoffIdx = len(s.packets)
	}

	evicting := s.packets[:cutoffIdx]
	evictedObs := 0
	var evictedBytes int64

	// Build sets of evicted IDs for batch removal from secondary indexes
	evictedTxIDs := make(map[int]struct{}, cutoffIdx)
	evictedObsIDs := make(map[int]struct{}, cutoffIdx*2)
	// Track which observer IDs and payload types need filtering
	affectedObservers := make(map[string]struct{})
	affectedPayloadTypes := make(map[int]struct{})
	affectedNodes := make(map[string]struct{})

	// First pass: remove from primary indexes (byHash, byTxID, byObsID),
	// collect IDs for batch secondary index cleanup, and handle non-index work
	for _, tx := range evicting {
		delete(s.byHash, tx.Hash)
		delete(s.byTxID, tx.ID)
		evictedTxIDs[tx.ID] = struct{}{}
		evictedBytes += estimateStoreTxBytes(tx)

		for _, obs := range tx.Observations {
			delete(s.byObsID, obs.ID)
			evictedObsIDs[obs.ID] = struct{}{}
			evictedBytes += estimateStoreObsBytes(obs)
			if obs.ObserverID != "" {
				affectedObservers[obs.ObserverID] = struct{}{}
			}
			evictedObs++
		}

		s.untrackAdvertPubkey(tx)
		if tx.PayloadType != nil {
			affectedPayloadTypes[*tx.PayloadType] = struct{}{}
		}

		// Remove from nodeHashes and collect affected node keys.
		// Must mirror indexByNode: process decoded JSON fields AND resolved_path pubkeys.
		evictedFromNode := make(map[string]bool)
		if tx.DecodedJSON != "" {
			if decoded := tx.ParsedDecoded(); decoded != nil {
				for _, field := range []string{"pubKey", "destPubKey", "srcPubKey"} {
					if v, ok := decoded[field].(string); ok && v != "" {
						if hashes, ok := s.nodeHashes[v]; ok {
							delete(hashes, tx.Hash)
							if len(hashes) == 0 {
								delete(s.nodeHashes, v)
							}
						}
						affectedNodes[v] = struct{}{}
						evictedFromNode[v] = true
					}
				}
			}
		}
		// Clean up resolved_path pubkeys from byNode/nodeHashes.
		// Uses pre-fetched batch data when available (no SQL under lock).
		var rpPubkeys []string
		if rpBatch != nil {
			rpPubkeys = rpBatch[tx.ID]
		}
		for _, pk := range rpPubkeys {
			if pk == "" || evictedFromNode[pk] {
				continue
			}
			if hashes, ok := s.nodeHashes[pk]; ok {
				delete(hashes, tx.Hash)
				if len(hashes) == 0 {
					delete(s.nodeHashes, pk)
				}
			}
			affectedNodes[pk] = struct{}{}
			evictedFromNode[pk] = true
		}

		// Remove from resolved pubkey index
		s.removeFromResolvedPubkeyIndex(tx.ID)

		// Remove from subpath index
		removeTxFromSubpathIndexFull(s.spIndex, s.spTxIndex, tx)
	}
	// Sweep raw AND resolved hop keys once per batch (#1908). The hash-only
	// membership index cannot recover full pubkey strings. Reuse the evicted
	// ID set for O(total path-hop entries) work, with no per-tx string storage
	// or repeated scans of shared buckets. DeleteFunc clears discarded pointer
	// slots so backing arrays cannot keep evicted transmissions alive.
	for key, list := range s.byPathHop {
		filtered := slices.DeleteFunc(list, func(tx *StoreTx) bool {
			_, evicted := evictedTxIDs[tx.ID]
			return evicted
		})
		if len(filtered) == 0 {
			delete(s.byPathHop, key)
		} else {
			s.byPathHop[key] = filtered
		}
	}
	s.invalidateRelayStatsCache()

	// Batch-remove from byObserver: single pass per affected observer slice
	for obsID := range affectedObservers {
		obsList := s.byObserver[obsID]
		filtered := obsList[:0]
		for _, o := range obsList {
			if _, evicted := evictedObsIDs[o.ID]; !evicted {
				filtered = append(filtered, o)
			}
		}
		if len(filtered) == 0 {
			delete(s.byObserver, obsID)
		} else {
			s.byObserver[obsID] = filtered
		}
	}

	// Batch-remove from byPayloadType: single pass per affected type slice
	for pt := range affectedPayloadTypes {
		ptList := s.byPayloadType[pt]
		filtered := ptList[:0]
		for _, t := range ptList {
			if _, evicted := evictedTxIDs[t.ID]; !evicted {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			delete(s.byPayloadType, pt)
		} else {
			s.byPayloadType[pt] = filtered
		}
	}

	// Batch-remove from byNode: single pass per affected node slice
	for nodeKey := range affectedNodes {
		nodeList := s.byNode[nodeKey]
		filtered := nodeList[:0]
		for _, t := range nodeList {
			if _, evicted := evictedTxIDs[t.ID]; !evicted {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			delete(s.byNode, nodeKey)
		} else {
			s.byNode[nodeKey] = filtered
		}
	}

	// Remove from distance indexes — filter out records referencing evicted txs
	evictedTxSet := make(map[*StoreTx]bool, cutoffIdx)
	for _, tx := range evicting {
		evictedTxSet[tx] = true
	}
	newDistHops := s.distHops[:0]
	for i := range s.distHops {
		if !evictedTxSet[s.distHops[i].tx] {
			newDistHops = append(newDistHops, s.distHops[i])
		}
	}
	s.distHops = newDistHops

	newDistPaths := s.distPaths[:0]
	for i := range s.distPaths {
		if !evictedTxSet[s.distPaths[i].tx] {
			newDistPaths = append(newDistPaths, s.distPaths[i])
		}
	}
	s.distPaths = newDistPaths

	// Trim packets slice
	n := copy(s.packets, s.packets[cutoffIdx:])
	s.packets = s.packets[:n]
	s.totalObs -= evictedObs

	evictCount := cutoffIdx
	atomic.AddInt64(&s.evicted, int64(evictCount))
	s.trackedBytes -= evictedBytes
	if s.trackedBytes < 0 {
		s.trackedBytes = 0
	}
	log.Printf("[store] Evicted %d packets (%d obs, freed ~%.1fMB, tracked ~%.1fMB)",
		evictCount, evictedObs, float64(evictedBytes)/1048576.0, s.trackedMemoryMB())

	// Eviction removes data — all caches may be affected
	s.invalidateCachesFor(cacheInvalidation{eviction: true})

	// Invalidate hash size cache
	s.hashSizeInfoMu.Lock()
	s.hashSizeInfoCache = nil
	s.hashSizeInfoMu.Unlock()

	// Compact resolved pubkey index after eviction sweep
	s.CompactResolvedPubkeyIndex()
	s.CheckResolvedPubkeyIndexSize()

	return evictCount
}

// RunEviction acquires the write lock and runs eviction. Safe to call from
// a goroutine. Returns evicted count.
// Uses a two-phase approach: determines eviction candidates under lock,
// releases lock for batch SQL fetch of resolved pubkeys, then re-acquires
// lock for the actual eviction pass.
func (s *PacketStore) RunEviction() int {
	// Phase 1: determine candidates under lock
	s.mu.Lock()
	txIDs := s.evictionCandidateTxIDs()
	s.mu.Unlock()

	// Phase 2: batch-fetch resolved pubkeys from SQL (no lock held)
	var rpBatch map[int][]string
	if len(txIDs) > 0 {
		rpBatch = s.resolvedPubkeysForEvictionBatch(txIDs)
	}

	// Phase 3: actual eviction under write lock, using pre-fetched data
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.EvictStaleWithRP(rpBatch)
}

// StartEvictionTicker starts a background goroutine that runs eviction every
// minute. Returns a stop function.
func (s *PacketStore) StartEvictionTicker() func() {
	if s.retentionHours <= 0 && s.maxMemoryMB <= 0 {
		return func() {} // no-op
	}
	ticker := time.NewTicker(1 * time.Minute)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				s.RunEviction()
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// computeDistancesForTx computes distance records for a single transmission.
// buildHopContextPubkeys collects context pubkeys for hop disambiguation:
// the originator/sender pubkey plus any unambiguous-prefix anchors in the
// path (single-candidate prefixes — strong context). Used by callers of
// pm.resolveWithContext to light up tiers 1 and 2 of the resolver. See #1197.
//
// Returned pubkeys are de-duplicated and lowercased.
func buildHopContextPubkeys(tx *StoreTx, pm *prefixMap) []string {
	if tx == nil || pm == nil {
		return nil
	}
	seen := make(map[string]struct{}, 16)
	out := make([]string, 0, 16)
	add := func(pk string) {
		if pk == "" {
			return
		}
		l := strings.ToLower(pk)
		if _, ok := seen[l]; ok {
			return
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}

	// Sender / originator pubkey from decoded payload. Use the cached
	// ParsedDecoded() (sync.Once-gated) instead of re-unmarshaling — the
	// helper is hot (3 distance sites + analytics topology, all 30k+ tx
	// loops). See #1197 (carmack/adversarial r1).
	if dec := tx.ParsedDecoded(); dec != nil {
		if pk, ok := dec["pubKey"].(string); ok {
			add(pk)
		}
	}

	// Observer pubkey, where available. ObserverID is the observers.id PRIMARY
	// KEY from the MQTT topic — it is NOT guaranteed to be a node pubkey hex
	// (some observers register with arbitrary string ids like "myobserver").
	// Guard against polluting the context with non-pubkey strings: include
	// only when it parses as hex AND is long enough to plausibly be a pubkey
	// prefix. The full prefix-map lookup would also be acceptable, but the
	// hex+length check is O(len) and avoids one map probe per tx on a hot
	// path. See #1197 (adversarial r1 #4).
	if obs := tx.ObserverID; obs != "" && len(obs) >= 4 && isHexLower(strings.ToLower(obs)) {
		add(obs)
	}

	// Unambiguous-prefix anchors: any hop in the path whose prefix has exactly
	// one candidate is a strong context signal.
	for _, hop := range txGetParsedPath(tx) {
		h := strings.ToLower(hop)
		if cands, ok := pm.m[h]; ok && len(cands) == 1 {
			add(cands[0].PublicKey)
		}
	}

	return out
}

// isHexLower reports whether s consists only of [0-9a-f] (assumes already
// lowercased by caller). Used to guard ObserverID before adding it to the
// hop-disambiguation context, since ObserverID is a free-form observers.id
// and may not be a node pubkey hex.
func isHexLower(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// hasUpperASCII reports whether s contains any uppercase ASCII letter.
// Used by resolveWithContext tier-1 to skip the strings.ToLower allocation
// when the context pubkeys are already lowercased (the common case — see
// buildHopContextPubkeys / buildAggregateHopContextPubkeys, which lowercase
// on the way in). #1247.
func hasUpperASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}

// buildAggregateHopContextPubkeys gathers context across many txs for hot
// loops that resolve hops outside any per-tx scope (subpath/topology
// aggregations). Caller passes the slice of txs to consider; we union the
// per-tx contexts with de-dup. Used by call sites that read from precomputed
// indices (s.spIndex, s.spTxIndex) or that resolve user-supplied hops.
//
// Result is order-independent in semantics; iteration order is deterministic
// only modulo Go's map iteration (acceptable — the resolver's tier-2 averages
// GPS positions and tier-3 picks the lex-smallest pubkey on ties, so context
// order does not affect the chosen candidate).
func buildAggregateHopContextPubkeys(txs []*StoreTx, pm *prefixMap) []string {
	if len(txs) == 0 || pm == nil {
		return nil
	}
	seen := make(map[string]struct{}, 32)
	var out []string
	for _, tx := range txs {
		for _, pk := range buildHopContextPubkeys(tx, pm) {
			if _, ok := seen[pk]; ok {
				continue
			}
			seen[pk] = struct{}{}
			out = append(out, pk)
		}
	}
	return out
}

// hopResolverPerTx returns (resolveHop, setContext). The cache is allocated
// once and cleared between txs; setContext rebinds the per-tx context. Used
// by all per-tx distance/topology loops to avoid 4× duplicate closure
// definitions and per-tx map allocation. See #1197 (adversarial r1 #7,
// carmack r1 #3).
//
// CONCURRENCY: NOT safe for concurrent use. The returned closures share
// mutable captured state — `contextPubkeys` is reassigned by setContext and
// read by resolveHop, and `hopCache` is mutated by both (resolveHop writes
// on miss, setContext clears wholesale). Callers MUST invoke both functions
// from a single goroutine for the lifetime of the (resolveHop, setContext)
// pair. If a future caller fans out per-tx work across goroutines, allocate
// a fresh resolver pair per goroutine. See #1199 item 4.
func (s *PacketStore) hopResolverPerTx(pm *prefixMap) (resolveHop func(string) *nodeInfo, setContext func([]string)) {
	hopCache := make(map[string]*nodeInfo, 16)
	var contextPubkeys []string
	// Hoist the atomic graph.Load() out of the per-hop closure body so we do
	// one Load per resolver instance, not one per resolveHop call (PR #1208
	// carmack #1). Pair (resolveHop, setContext) is documented single-
	// goroutine, single-request scope — pinning the graph here matches that
	// lifetime.
	graph := s.graph.Load()
	resolveHop = func(hop string) *nodeInfo {
		if cached, ok := hopCache[hop]; ok {
			return cached
		}
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		hopCache[hop] = r
		return r
	}
	setContext = func(ctx []string) {
		contextPubkeys = ctx
		clear(hopCache)
	}
	return resolveHop, setContext
}

func computeDistancesForTx(tx *StoreTx, nodeByPk map[string]*nodeInfo, repeaterSet map[string]bool, resolveHop func(string) *nodeInfo) ([]distHopRecord, *distPathRecord) {
	pathHops := txGetParsedPath(tx)
	if len(pathHops) == 0 {
		return nil, nil
	}

	resolved := make([]*nodeInfo, len(pathHops))
	for i, h := range pathHops {
		resolved[i] = resolveHop(h)
	}

	var senderNode *nodeInfo
	if tx.DecodedJSON != "" {
		if dec := tx.ParsedDecoded(); dec != nil {
			if pk, ok := dec["pubKey"].(string); ok && pk != "" {
				senderNode = nodeByPk[pk]
			}
		}
	}

	chain := make([]*nodeInfo, 0, len(pathHops)+1)
	if senderNode != nil && senderNode.HasGPS {
		chain = append(chain, senderNode)
	}
	for _, r := range resolved {
		if r != nil && r.HasGPS {
			chain = append(chain, r)
		}
	}
	if len(chain) < 2 {
		return nil, nil
	}

	hourBucket := ""
	if tx.FirstSeen != "" && len(tx.FirstSeen) >= 13 {
		hourBucket = tx.FirstSeen[:13]
	}

	var hopRecords []distHopRecord
	var hopDetails []distHopDetail
	pathDist := 0.0

	for i := 0; i < len(chain)-1; i++ {
		a, b := chain[i], chain[i+1]
		dist := haversineKm(a.Lat, a.Lon, b.Lat, b.Lon)
		if dist > 300 {
			continue
		}

		aRep := repeaterSet[a.PublicKey]
		bRep := repeaterSet[b.PublicKey]
		var hopType string
		if aRep && bRep {
			hopType = "R↔R"
		} else if !aRep && !bRep {
			hopType = "C↔C"
		} else {
			hopType = "C↔R"
		}

		roundedDist := math.Round(dist*100) / 100
		hopRecords = append(hopRecords, distHopRecord{
			FromName: a.Name, FromPk: a.PublicKey,
			ToName: b.Name, ToPk: b.PublicKey,
			Dist: roundedDist, Type: hopType,
			SNR: tx.SNR, Hash: tx.Hash, Timestamp: tx.FirstSeen,
			HourBucket: hourBucket, tx: tx,
		})
		hopDetails = append(hopDetails, distHopDetail{
			FromName: a.Name, FromPk: a.PublicKey,
			ToName: b.Name, ToPk: b.PublicKey,
			Dist: roundedDist,
		})
		pathDist += dist
	}

	if len(hopRecords) == 0 {
		return nil, nil
	}

	pathRec := &distPathRecord{
		Hash: tx.Hash, TotalDist: math.Round(pathDist*100) / 100,
		HopCount: len(hopDetails), Timestamp: tx.FirstSeen,
		Hops: hopDetails, tx: tx,
	}
	return hopRecords, pathRec
}

func filterTxSlice(s []*StoreTx, fn func(*StoreTx) bool) []*StoreTx {
	var result []*StoreTx
	for _, tx := range s {
		if fn(tx) {
			result = append(result, tx)
		}
	}
	return result
}

// countNonPrintable counts characters that are non-printable (< 0x20 except \n, \t)
// or invalid UTF-8 replacement characters. Mirrors the heuristic from #197.
func countNonPrintable(s string) int {
	count := 0
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			count++
		} else if r == utf8.RuneError {
			count++
		}
	}
	return count
}

// hasGarbageChars returns true if the string contains garbage (non-printable) data.
func hasGarbageChars(s string) bool {
	return s != "" && (!utf8.ValidString(s) || countNonPrintable(s) > 2)
}

// GetChannels returns channel list from in-memory packets (payload_type 5, decoded type CHAN).
func (s *PacketStore) GetChannels(region string) []map[string]interface{} {
	cacheKey := region

	s.channelsCacheMu.Lock()
	if s.channelsCacheRes != nil && s.channelsCacheKey == cacheKey && time.Now().Before(s.channelsCacheExp) {
		res := s.channelsCacheRes
		s.channelsCacheMu.Unlock()
		return res
	}
	s.channelsCacheMu.Unlock()

	type txSnapshot struct {
		firstSeen   string
		decodedJSON string
		hasRegion   bool
	}

	// Copy only the fields needed — release the lock before JSON unmarshal.
	s.mu.RLock()
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}
	grpTxts := s.byPayloadType[5]
	snapshots := make([]txSnapshot, 0, len(grpTxts))
	for _, tx := range grpTxts {
		inRegion := true
		if regionObs != nil {
			inRegion = false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					inRegion = true
					break
				}
			}
		}
		snapshots = append(snapshots, txSnapshot{
			firstSeen:   tx.FirstSeen,
			decodedJSON: tx.DecodedJSON,
			hasRegion:   inRegion,
		})
	}
	s.mu.RUnlock()

	// JSON unmarshal outside the lock.
	type chanInfo struct {
		Hash         string
		Name         string
		LastMessage  interface{}
		LastSender   interface{}
		MessageCount int
		LastActivity string
	}
	type decodedGrp struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		Text    string `json:"text"`
		Sender  string `json:"sender"`
	}
	channelMap := map[string]*chanInfo{}
	for _, snap := range snapshots {
		if !snap.hasRegion {
			continue
		}
		var decoded decodedGrp
		if json.Unmarshal([]byte(snap.decodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "CHAN" {
			continue
		}
		if hasGarbageChars(decoded.Channel) || hasGarbageChars(decoded.Text) {
			continue
		}
		channelName := decoded.Channel
		if channelName == "" {
			// Issue #1373: encrypted-no-key packets decode with channel="".
			// Previously we bucketed them under a literal "unknown" channel
			// which then leaked into /api/channels as a ghost entry next to
			// real channels (especially visible after the operator added a
			// PSK client-side). Skip them — they belong in encrypted-channels
			// analytics, not the user-facing channel list.
			continue
		}
		ch := channelMap[channelName]
		if ch == nil {
			ch = &chanInfo{Hash: channelName, Name: channelName, LastActivity: snap.firstSeen}
			channelMap[channelName] = ch
		}
		ch.MessageCount++
		if snap.firstSeen >= ch.LastActivity {
			ch.LastActivity = snap.firstSeen
			if decoded.Text != "" {
				idx := strings.Index(decoded.Text, ": ")
				if idx > 0 {
					ch.LastMessage = decoded.Text[idx+2:]
				} else {
					ch.LastMessage = decoded.Text
				}
				if decoded.Sender != "" {
					ch.LastSender = decoded.Sender
				}
			}
		}
	}

	channels := make([]map[string]interface{}, 0, len(channelMap))
	for _, ch := range channelMap {
		channels = append(channels, map[string]interface{}{
			"hash": ch.Hash, "name": ch.Name,
			"lastMessage": ch.LastMessage, "lastSender": ch.LastSender,
			"messageCount": ch.MessageCount, "lastActivity": ch.LastActivity,
		})
	}

	// #688: scan decoded message text for #hashtag mentions and surface any
	// previously-unseen channel names as discovered channels. We dedup against
	// channelMap (matched by name) so a channel that already has traffic does
	// NOT also appear as discovered.
	discovered := map[string]string{} // name -> lastActivity
	for _, snap := range snapshots {
		if !snap.hasRegion {
			continue
		}
		var decoded decodedGrp
		if json.Unmarshal([]byte(snap.decodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "CHAN" || decoded.Text == "" {
			continue
		}
		if hasGarbageChars(decoded.Text) {
			continue
		}
		for _, tag := range extractHashtagsFromText(decoded.Text) {
			// Skip if already a known/decoded channel (by name with or without '#').
			bare := tag[1:]
			if _, ok := channelMap[tag]; ok {
				continue
			}
			if _, ok := channelMap[bare]; ok {
				continue
			}
			if existing, ok := discovered[tag]; !ok || snap.firstSeen > existing {
				discovered[tag] = snap.firstSeen
			}
		}
	}
	for name, lastActivity := range discovered {
		channels = append(channels, map[string]interface{}{
			"hash":         name,
			"name":         name,
			"lastMessage":  nil,
			"lastSender":   nil,
			"messageCount": 0,
			"lastActivity": lastActivity,
			"discovered":   true,
		})
	}

	s.channelsCacheMu.Lock()
	s.channelsCacheRes = channels
	s.channelsCacheKey = cacheKey
	s.channelsCacheExp = time.Now().Add(15 * time.Second)
	s.channelsCacheMu.Unlock()

	return channels
}

// GetEncryptedChannels returns undecryptable GRP_TXT channels from in-memory packets.
func (s *PacketStore) GetEncryptedChannels(region string) []map[string]interface{} {
	s.mu.RLock()
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}
	grpTxts := s.byPayloadType[5]

	type encInfo struct {
		hash         string
		messageCount int
		lastActivity string
	}
	type grpDec struct {
		Type             string      `json:"type"`
		ChannelHash      interface{} `json:"channelHash"`
		ChannelHashHex   string      `json:"channelHashHex"`
		DecryptionStatus string      `json:"decryptionStatus"`
	}
	channelMap := map[string]*encInfo{}

	for _, tx := range grpTxts {
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		var decoded grpDec
		if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "GRP_TXT" || decoded.DecryptionStatus != "no_key" {
			continue
		}
		chHash := decoded.ChannelHashHex
		if chHash == "" {
			if num, ok := decoded.ChannelHash.(float64); ok {
				chHash = fmt.Sprintf("%02X", int(num))
			}
		}
		if chHash == "" {
			chHash = "?"
		}
		ch := channelMap[chHash]
		if ch == nil {
			ch = &encInfo{hash: chHash, lastActivity: tx.FirstSeen}
			channelMap[chHash] = ch
		}
		ch.messageCount++
		if tx.FirstSeen >= ch.lastActivity {
			ch.lastActivity = tx.FirstSeen
		}
	}
	s.mu.RUnlock()

	channels := make([]map[string]interface{}, 0, len(channelMap))
	for _, ch := range channelMap {
		channels = append(channels, map[string]interface{}{
			"hash":         "enc_" + ch.hash,
			"name":         "Encrypted (0x" + ch.hash + ")",
			"lastMessage":  nil,
			"lastSender":   nil,
			"messageCount": ch.messageCount,
			"lastActivity": ch.lastActivity,
			"encrypted":    true,
		})
	}
	return channels
}

// GetChannelMessages returns deduplicated messages for a channel from in-memory packets.
func (s *PacketStore) GetChannelMessages(channelHash string, limit, offset int, region ...string) ([]map[string]interface{}, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	type msgEntry struct {
		Data      map[string]interface{}
		Repeats   int
		Observers []string
	}
	msgMap := map[string]*msgEntry{}
	var msgOrder []string
	regionParam := ""
	if len(region) > 0 {
		regionParam = region[0]
	}
	regionObs := s.resolveRegionObservers(regionParam)

	// Iterate type-5 packets oldest-first (byPayloadType is ASC = oldest first)
	type decodedMsg struct {
		Type            string      `json:"type"`
		Channel         string      `json:"channel"`
		Text            string      `json:"text"`
		Sender          string      `json:"sender"`
		SenderTimestamp interface{} `json:"sender_timestamp"`
		PathLen         int         `json:"path_len"`
	}

	grpTxts := s.byPayloadType[5]
	for _, tx := range grpTxts {
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		if tx.DecodedJSON == "" {
			continue
		}

		var decoded decodedMsg
		if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) != nil {
			continue
		}
		if decoded.Type != "CHAN" {
			continue
		}
		ch := decoded.Channel
		if ch == "" {
			ch = "unknown"
		}
		if ch != channelHash {
			continue
		}

		text := decoded.Text
		sender := decoded.Sender
		if sender == "" && text != "" {
			idx := strings.Index(text, ": ")
			if idx > 0 && idx < 50 {
				sender = text[:idx]
			}
		}

		dedupeKey := sender + ":" + tx.Hash

		if existing, ok := msgMap[dedupeKey]; ok {
			existing.Repeats++
			existing.Data["repeats"] = existing.Repeats
			// Add observer if new
			obsName := tx.ObserverName
			if obsName == "" {
				obsName = tx.ObserverID
			}
			if obsName != "" {
				found := false
				for _, o := range existing.Observers {
					if o == obsName {
						found = true
						break
					}
				}
				if !found {
					existing.Observers = append(existing.Observers, obsName)
					existing.Data["observers"] = existing.Observers
				}
			}
		} else {
			displaySender := sender
			displayText := text
			if text != "" {
				idx := strings.Index(text, ": ")
				if idx > 0 && idx < 50 {
					displaySender = text[:idx]
					displayText = text[idx+2:]
				}
			}

			hops := pathLen(tx.PathJSON)

			var snrVal interface{}
			if tx.SNR != nil {
				snrVal = *tx.SNR
			}

			senderTs := decoded.SenderTimestamp

			// Issue #1366: emit tx.LatestSeen (max observation timestamp,
			// server UTC) as the rendered timestamp — NOT tx.FirstSeen,
			// which stays pinned at the first-ever observation of a hash
			// and lags reality for heartbeat-style retransmissions. Fall
			// back to FirstSeen only when LatestSeen is empty (no obs).
			// sender_timestamp from the decoded payload is NOT used as the
			// rendered field: client RTCs are unreliable. It remains in
			// the response for debug surfaces.
			displayTs := tx.LatestSeen
			if displayTs == "" {
				displayTs = tx.FirstSeen
			}

			observers := []string{}
			obsName := tx.ObserverName
			if obsName == "" {
				obsName = tx.ObserverID
			}
			if obsName != "" {
				observers = []string{obsName}
			}

			entry := &msgEntry{
				Data: map[string]interface{}{
					"sender":           displaySender,
					"text":             displayText,
					"timestamp":        strOrNil(displayTs),
					"first_seen":       strOrNil(tx.FirstSeen),
					"sender_timestamp": senderTs,
					"packetId":         tx.ID,
					"packetHash":       strOrNil(tx.Hash),
					"repeats":          1,
					"observers":        observers,
					"hops":             hops,
					"snr":              snrVal,
				},
				Repeats:   1,
				Observers: observers,
			}
			msgMap[dedupeKey] = entry
			msgOrder = append(msgOrder, dedupeKey)
		}
	}

	// Issue #1366 follow-up: msgOrder is in tx insertion order
	// (≈ FirstSeen ascending). Re-sort by the rendered timestamp field
	// (= LatestSeen, set above) ascending, so the page tail = newest
	// LatestSeen. Without this, a long-running heartbeat with old
	// FirstSeen but fresh LatestSeen ends up at the head of msgOrder
	// and gets sliced off by the tail selection below.
	sort.SliceStable(msgOrder, func(i, j int) bool {
		ti, _ := msgMap[msgOrder[i]].Data["timestamp"].(string)
		tj, _ := msgMap[msgOrder[j]].Data["timestamp"].(string)
		return ti < tj
	})

	total := len(msgOrder)
	// Return latest messages (tail)
	start := total - limit - offset
	if start < 0 {
		start = 0
	}
	end := total - offset
	if end < 0 {
		end = 0
	}
	if end > total {
		end = total
	}

	messages := make([]map[string]interface{}, 0, end-start)
	for i := start; i < end; i++ {
		messages = append(messages, msgMap[msgOrder[i]].Data)
	}
	return messages, total
}

// GetAnalyticsChannels returns full channel analytics computed from in-memory packets.
func (s *PacketStore) GetAnalyticsChannels(region, area string) map[string]interface{} {
	return s.GetAnalyticsChannelsWithWindow(region, area, TimeWindow{})
}

// GetAnalyticsChannelsWithWindow returns channel analytics for the given region,
// optionally bounded to a time window (issue #842). Zero TimeWindow = all data.
func (s *PacketStore) GetAnalyticsChannelsWithWindow(region, area string, window TimeWindow) map[string]interface{} {
	if region == "" && area == "" && window.IsZero() {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompChannels
		s.analyticsRecomputerMu.RUnlock()
		if rc != nil {
			if v := rc.Load(); v != nil {
				if m, ok := v.(map[string]interface{}); ok {
					s.cacheMu.Lock()
					s.cacheHits++
					s.cacheMu.Unlock()
					return m
				}
			}
		}
	}
	cacheKey := region + "|" + area
	if !window.IsZero() {
		cacheKey += "|" + window.CacheKey()
	}
	s.cacheMu.Lock()
	if cached, ok := s.chanCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsChannels(region, area, window)

	s.cacheMu.Lock()
	s.chanCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

// channelNameMatchesHash validates that a decrypted channel name hashes to the
// observed single-byte channel hash. This rejects rainbow-table mismatches where
// an observer's lookup table incorrectly maps a hash byte to the wrong name.
// Firmware invariant: channelHash = SHA256(SHA256("#name")[:16])[0]
func channelNameMatchesHash(name string, hashStr string) bool {
	expected, err := strconv.Atoi(hashStr)
	if err != nil {
		return false
	}
	chanName := name
	if !strings.HasPrefix(chanName, "#") {
		chanName = "#" + chanName
	}
	h1 := sha256.Sum256([]byte(chanName))
	h2 := sha256.Sum256(h1[:16])
	return int(h2[0]) == expected
}

// isPlaceholderName returns true if the name is a "chN" placeholder (not a real decrypted name).
func isPlaceholderName(name string) bool {
	if !strings.HasPrefix(name, "ch") {
		return false
	}
	_, err := strconv.Atoi(name[2:])
	return err == nil
}

func (s *PacketStore) computeAnalyticsChannels(region, area string, window TimeWindow) map[string]interface{} {
	var areaNodes map[string]bool
	if area != "" {
		areaNodes = s.resolveAreaNodes(area)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	type decodedGrp struct {
		Type             string      `json:"type"`
		Channel          string      `json:"channel"`
		ChannelHash      interface{} `json:"channelHash"`
		ChannelHash2     string      `json:"channel_hash"`
		Text             string      `json:"text"`
		Sender           string      `json:"sender"`
		DecryptionStatus string      `json:"decryptionStatus,omitempty"`
	}

	// Convert channelHash (number or string in JSON) to string
	chHashStr := func(v interface{}) string {
		if v == nil {
			return ""
		}
		switch val := v.(type) {
		case string:
			return val
		case float64:
			return strconv.FormatFloat(val, 'f', -1, 64)
		default:
			return fmt.Sprintf("%v", val)
		}
	}

	type chanInfo struct {
		Hash         string
		Name         string
		Messages     int
		Senders      map[string]bool
		LastActivity string
		Encrypted    bool
	}

	channelMap := map[string]*chanInfo{}
	senderCounts := map[string]int{}
	msgLengths := make([]int, 0)
	timeline := map[string]int{} // hour|channelName → count

	grpTxts := s.byPayloadType[5]
	for _, tx := range grpTxts {
		if !window.Includes(tx.FirstSeen) {
			continue
		}
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		if areaNodes != nil {
			d := tx.ParsedDecoded()
			pk, _ := d["public_key"].(string)
			if pk == "" {
				pk, _ = d["pubKey"].(string)
			}
			if pk == "" || !areaNodes[pk] {
				continue
			}
		}

		var decoded decodedGrp
		if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) != nil {
			continue
		}

		hash := chHashStr(decoded.ChannelHash)
		if hash == "" {
			hash = decoded.ChannelHash2
		}
		if hash == "" {
			hash = "?"
		}
		name := decoded.Channel
		if name == "" {
			name = "ch" + hash
		}
		encrypted := decoded.Text == "" && decoded.Sender == ""

		// Bug #978 fix: validate channel name against hash to reject rainbow-table
		// mismatches. If the claimed channel name doesn't hash to the observed
		// channelHash byte, discard it — UNLESS the ingestor marked the packet
		// `decryptionStatus:"decrypted"`. That flag means the name came from a
		// successful key-based (PSK) decryption, not a rainbow-table lookup, so
		// it is trustworthy regardless of the hashtag-derived hash check. This
		// keeps the firmware-default Public channel (0x11, key-derived hash
		// SHA256(key)[0]=17, not the hashtag scheme's 186) from being wrongly
		// discarded and rendered as "Encrypted (0x11)". See #1729.
		ingestorDecrypted := decoded.DecryptionStatus == "decrypted"
		if name != "" && name != "ch"+hash && !ingestorDecrypted && !channelNameMatchesHash(name, hash) {
			name = "ch" + hash
			encrypted = true
		}

		// Bug #978 fix: always group by hash byte alone — same physical channel,
		// regardless of which observer decrypted it.
		chKey := hash

		ch := channelMap[chKey]
		if ch == nil {
			ch = &chanInfo{Hash: hash, Name: name, Senders: map[string]bool{}, LastActivity: tx.FirstSeen, Encrypted: encrypted}
			channelMap[chKey] = ch
		} else {
			// Upgrade bucket name: if current is placeholder and we have a validated decrypted name
			if isPlaceholderName(ch.Name) && !isPlaceholderName(name) {
				ch.Name = name
			}
		}
		ch.Messages++
		ch.LastActivity = tx.FirstSeen
		if !encrypted {
			ch.Encrypted = false
		}

		if decoded.Sender != "" {
			ch.Senders[decoded.Sender] = true
			senderCounts[decoded.Sender]++
		}
		if decoded.Text != "" {
			msgLengths = append(msgLengths, len(decoded.Text))
		}

		// Timeline
		if len(tx.FirstSeen) >= 13 {
			hr := tx.FirstSeen[:13]
			key := hr + "|" + name
			timeline[key]++
		}
	}

	channelList := make([]map[string]interface{}, 0, len(channelMap))
	decryptable := 0
	for _, c := range channelMap {
		if !c.Encrypted {
			decryptable++
		}
		channelList = append(channelList, map[string]interface{}{
			"hash": c.Hash, "name": c.Name,
			"messages": c.Messages, "senders": len(c.Senders),
			"lastActivity": c.LastActivity, "encrypted": c.Encrypted,
		})
	}
	sort.Slice(channelList, func(i, j int) bool {
		return channelList[i]["messages"].(int) > channelList[j]["messages"].(int)
	})

	// Top senders
	type senderEntry struct {
		name  string
		count int
	}
	senderList := make([]senderEntry, 0, len(senderCounts))
	for n, c := range senderCounts {
		senderList = append(senderList, senderEntry{n, c})
	}
	sort.Slice(senderList, func(i, j int) bool { return senderList[i].count > senderList[j].count })
	topSenders := make([]map[string]interface{}, 0)
	for i, e := range senderList {
		if i >= 15 {
			break
		}
		topSenders = append(topSenders, map[string]interface{}{"name": e.name, "count": e.count})
	}

	// Channel timeline
	type tlEntry struct {
		hour, channel string
		count         int
	}
	var tlList []tlEntry
	for key, count := range timeline {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) == 2 {
			tlList = append(tlList, tlEntry{parts[0], parts[1], count})
		}
	}
	sort.Slice(tlList, func(i, j int) bool { return tlList[i].hour < tlList[j].hour })
	channelTimeline := make([]map[string]interface{}, 0, len(tlList))
	for _, e := range tlList {
		channelTimeline = append(channelTimeline, map[string]interface{}{
			"hour": e.hour, "channel": e.channel, "count": e.count,
		})
	}

	return map[string]interface{}{
		"activeChannels":  len(channelList),
		"decryptable":     decryptable,
		"channels":        channelList,
		"topSenders":      topSenders,
		"channelTimeline": channelTimeline,
		"msgLengths":      msgLengths,
	}
}

// GetAnalyticsRF returns full RF analytics computed from in-memory observations.
func (s *PacketStore) GetAnalyticsRF(region, area string) map[string]interface{} {
	return s.GetAnalyticsRFWithWindow(region, area, TimeWindow{})
}

// GetAnalyticsRFWithWindow returns RF analytics bounded by an optional
// time window (issue #842). Zero TimeWindow = all data (backwards compatible).
func (s *PacketStore) GetAnalyticsRFWithWindow(region, area string, window TimeWindow) map[string]interface{} {
	if region == "" && area == "" && window.IsZero() {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompRF
		s.analyticsRecomputerMu.RUnlock()
		if rc != nil {
			if v := rc.Load(); v != nil {
				if m, ok := v.(map[string]interface{}); ok {
					s.cacheMu.Lock()
					s.cacheHits++
					s.cacheMu.Unlock()
					return m
				}
			}
		}
	}
	cacheKey := region + "|" + area
	if !window.IsZero() {
		cacheKey += "|" + window.CacheKey()
	}
	s.cacheMu.Lock()
	if cached, ok := s.rfCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsRF(region, area, window)

	s.cacheMu.Lock()
	s.rfCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsRF(region, area string, window TimeWindow) map[string]interface{} {
	var areaNodes map[string]bool
	if area != "" {
		areaNodes = s.resolveAreaNodes(area)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	ptNames := payloadTypeNames

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	// Collect all observations matching the region
	estCap := s.totalObs
	if estCap > 2000000 {
		estCap = 2000000
	}
	snrVals := make([]float64, 0, estCap/2)
	rssiVals := make([]float64, 0, estCap/2)
	packetSizes := make([]int, 0, len(s.packets))
	seenSizeHashes := make(map[string]bool, len(s.packets))
	seenTypeHashes := make(map[string]bool, len(s.packets))
	typeBuckets := map[int]int{}
	hourBuckets := map[string]int{}
	seenHourHash := make(map[string]bool, len(s.packets)) // dedup packets-per-hour by hash+hour
	snrByType := map[string]*struct{ vals []float64 }{}
	sigTime := map[string]*struct {
		snrs  []float64
		count int
	}{}
	scatterAll := make([]struct{ snr, rssi float64 }, 0, estCap/4)
	totalObs := 0
	regionalHashes := make(map[string]bool, len(s.packets))
	var minTimestamp, maxTimestamp string

	if regionObs != nil {
		// Regional: iterate observations from matching observers
		for obsID := range regionObs {
			obsList := s.byObserver[obsID]
			for _, obs := range obsList {
				if !window.Includes(obs.Timestamp) {
					continue
				}
				totalObs++
				tx := s.byTxID[obs.TransmissionID]
				if areaNodes != nil && tx != nil {
					d := tx.ParsedDecoded()
					pk, _ := d["public_key"].(string)
					if pk == "" {
						pk, _ = d["pubKey"].(string)
					}
					if pk == "" || !areaNodes[pk] {
						continue
					}
				}
				hash := ""
				if tx != nil {
					hash = tx.Hash
				}
				if hash != "" {
					regionalHashes[hash] = true
				}

				ts := obs.Timestamp
				if ts != "" {
					if minTimestamp == "" || ts < minTimestamp {
						minTimestamp = ts
					}
					if ts > maxTimestamp {
						maxTimestamp = ts
					}
				}

				// SNR/RSSI
				if obs.SNR != nil {
					snrVals = append(snrVals, *obs.SNR)
					typeName := "UNK"
					if tx != nil && tx.PayloadType != nil {
						if n, ok := ptNames[*tx.PayloadType]; ok {
							typeName = n
						} else {
							typeName = fmt.Sprintf("UNK(%d)", *tx.PayloadType)
						}
					}
					if snrByType[typeName] == nil {
						snrByType[typeName] = &struct{ vals []float64 }{}
					}
					snrByType[typeName].vals = append(snrByType[typeName].vals, *obs.SNR)

					if obs.RSSI != nil {
						scatterAll = append(scatterAll, struct{ snr, rssi float64 }{*obs.SNR, *obs.RSSI})
					}

					// Signal over time
					if len(ts) >= 13 {
						hr := ts[:13]
						if sigTime[hr] == nil {
							sigTime[hr] = &struct {
								snrs  []float64
								count int
							}{}
						}
						sigTime[hr].snrs = append(sigTime[hr].snrs, *obs.SNR)
						sigTime[hr].count++
					}
				}
				if obs.RSSI != nil {
					rssiVals = append(rssiVals, *obs.RSSI)
				}

				// Packets per hour (unique by hash per hour)
				if len(ts) >= 13 {
					hr := ts[:13]
					hk := hash + "|" + hr
					if hash == "" || !seenHourHash[hk] {
						if hash != "" {
							seenHourHash[hk] = true
						}
						hourBuckets[hr]++
					}
				}

				// Packet sizes (unique by hash)
				if hash != "" && !seenSizeHashes[hash] && tx != nil && tx.RawHex != "" {
					seenSizeHashes[hash] = true
					packetSizes = append(packetSizes, len(tx.RawHex)/2)
				}

				// Payload type distribution (unique by hash)
				if hash != "" && !seenTypeHashes[hash] && tx != nil && tx.PayloadType != nil {
					seenTypeHashes[hash] = true
					typeBuckets[*tx.PayloadType]++
				}
			}
		}
	} else {
		// No region: iterate all transmissions and their observations
		for _, tx := range s.packets {
			// Window filter: skip transmissions outside the requested window.
			// We use tx.FirstSeen as the bounding timestamp; per-obs window
			// filter below handles cases where individual obs timestamps differ.
			if !window.Includes(tx.FirstSeen) {
				continue
			}
			if areaNodes != nil {
				d := tx.ParsedDecoded()
				pk, _ := d["public_key"].(string)
				if pk == "" {
					pk, _ = d["pubKey"].(string)
				}
				if pk == "" || !areaNodes[pk] {
					continue
				}
			}
			hash := tx.Hash
			if hash != "" {
				regionalHashes[hash] = true
				if !seenSizeHashes[hash] && tx.RawHex != "" {
					seenSizeHashes[hash] = true
					packetSizes = append(packetSizes, len(tx.RawHex)/2)
				}
				if !seenTypeHashes[hash] && tx.PayloadType != nil {
					seenTypeHashes[hash] = true
					typeBuckets[*tx.PayloadType]++
				}
			}

			// Pre-resolve type name once per transmission
			typeName := "UNK"
			if tx.PayloadType != nil {
				if n, ok := ptNames[*tx.PayloadType]; ok {
					typeName = n
				} else {
					typeName = fmt.Sprintf("UNK(%d)", *tx.PayloadType)
				}
			}

			if len(tx.Observations) > 0 {
				for _, obs := range tx.Observations {
					totalObs++
					ts := obs.Timestamp
					if ts != "" {
						if minTimestamp == "" || ts < minTimestamp {
							minTimestamp = ts
						}
						if ts > maxTimestamp {
							maxTimestamp = ts
						}
					}

					if obs.SNR != nil {
						snr := *obs.SNR
						snrVals = append(snrVals, snr)
						entry := snrByType[typeName]
						if entry == nil {
							entry = &struct{ vals []float64 }{}
							snrByType[typeName] = entry
						}
						entry.vals = append(entry.vals, snr)

						if obs.RSSI != nil {
							scatterAll = append(scatterAll, struct{ snr, rssi float64 }{snr, *obs.RSSI})
						}

						if len(ts) >= 13 {
							hr := ts[:13]
							st := sigTime[hr]
							if st == nil {
								st = &struct {
									snrs  []float64
									count int
								}{}
								sigTime[hr] = st
							}
							st.snrs = append(st.snrs, snr)
							st.count++
						}
					}
					if obs.RSSI != nil {
						rssiVals = append(rssiVals, *obs.RSSI)
					}

					if len(ts) >= 13 {
						hr := ts[:13]
						hk := hash + "|" + hr
						if hash == "" || !seenHourHash[hk] {
							if hash != "" {
								seenHourHash[hk] = true
							}
							hourBuckets[hr]++
						}
					}
				}
			} else {
				// Legacy: transmission without observations
				totalObs++
				if tx.SNR != nil {
					snrVals = append(snrVals, *tx.SNR)
				}
				if tx.RSSI != nil {
					rssiVals = append(rssiVals, *tx.RSSI)
				}
				ts := tx.FirstSeen
				if ts != "" {
					if minTimestamp == "" || ts < minTimestamp {
						minTimestamp = ts
					}
					if ts > maxTimestamp {
						maxTimestamp = ts
					}
				}
				if len(ts) >= 13 {
					hourBuckets[ts[:13]]++
				}
			}
		}
	}

	// Stats helpers
	stddevF64 := func(arr []float64, avg float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		sum := 0.0
		for _, v := range arr {
			d := v - avg
			sum += d * d
		}
		return math.Sqrt(sum / float64(len(arr)))
	}
	minF64 := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v < m {
				m = v
			}
		}
		return m
	}
	maxF64 := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v > m {
				m = v
			}
		}
		return m
	}
	minInt := func(arr []int) int {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v < m {
				m = v
			}
		}
		return m
	}
	maxInt := func(arr []int) int {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v > m {
				m = v
			}
		}
		return m
	}

	// Sort snrVals and rssiVals once; reuse sorted order for min/max/median
	// instead of copying+sorting per stat call (#366).
	sort.Float64s(snrVals)
	sort.Float64s(rssiVals)

	snrAvg := 0.0
	if len(snrVals) > 0 {
		sum := 0.0
		for _, v := range snrVals {
			sum += v
		}
		snrAvg = sum / float64(len(snrVals))
	}
	rssiAvg := 0.0
	if len(rssiVals) > 0 {
		sum := 0.0
		for _, v := range rssiVals {
			sum += v
		}
		rssiAvg = sum / float64(len(rssiVals))
	}

	// Packets per hour
	type hourCount struct {
		Hour  string `json:"hour"`
		Count int    `json:"count"`
	}
	hourKeys := make([]string, 0, len(hourBuckets))
	for k := range hourBuckets {
		hourKeys = append(hourKeys, k)
	}
	sort.Strings(hourKeys)
	packetsPerHour := make([]hourCount, len(hourKeys))
	for i, k := range hourKeys {
		packetsPerHour[i] = hourCount{Hour: k, Count: hourBuckets[k]}
	}

	// Payload types
	type ptEntry struct {
		Type  int    `json:"type"`
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	payloadTypes := make([]ptEntry, 0, len(typeBuckets))
	for t, c := range typeBuckets {
		name := ptNames[t]
		if name == "" {
			name = fmt.Sprintf("UNK(%d)", t)
		}
		payloadTypes = append(payloadTypes, ptEntry{Type: t, Name: name, Count: c})
	}
	sort.Slice(payloadTypes, func(i, j int) bool { return payloadTypes[i].Count > payloadTypes[j].Count })

	// SNR by type
	type snrTypeEntry struct {
		Name  string  `json:"name"`
		Count int     `json:"count"`
		Avg   float64 `json:"avg"`
		Min   float64 `json:"min"`
		Max   float64 `json:"max"`
	}
	snrByTypeArr := make([]snrTypeEntry, 0, len(snrByType))
	for name, d := range snrByType {
		sum := 0.0
		for _, v := range d.vals {
			sum += v
		}
		snrByTypeArr = append(snrByTypeArr, snrTypeEntry{
			Name: name, Count: len(d.vals),
			Avg: sum / float64(len(d.vals)),
			Min: minF64(d.vals), Max: maxF64(d.vals),
		})
	}
	sort.Slice(snrByTypeArr, func(i, j int) bool { return snrByTypeArr[i].Count > snrByTypeArr[j].Count })

	// Signal over time
	type sigTimeEntry struct {
		Hour   string  `json:"hour"`
		Count  int     `json:"count"`
		AvgSnr float64 `json:"avgSnr"`
	}
	sigKeys := make([]string, 0, len(sigTime))
	for k := range sigTime {
		sigKeys = append(sigKeys, k)
	}
	sort.Strings(sigKeys)
	signalOverTime := make([]sigTimeEntry, len(sigKeys))
	for i, k := range sigKeys {
		d := sigTime[k]
		sum := 0.0
		for _, v := range d.snrs {
			sum += v
		}
		signalOverTime[i] = sigTimeEntry{Hour: k, Count: d.count, AvgSnr: sum / float64(d.count)}
	}

	// Scatter (downsample to 500)
	type scatterPoint struct {
		SNR  float64 `json:"snr"`
		RSSI float64 `json:"rssi"`
	}
	scatterStep := 1
	if len(scatterAll) > 500 {
		scatterStep = len(scatterAll) / 500
	}
	scatterData := make([]scatterPoint, 0, 500)
	for i, p := range scatterAll {
		if i%scatterStep == 0 {
			scatterData = append(scatterData, scatterPoint{SNR: p.snr, RSSI: p.rssi})
		}
	}

	// Histograms
	buildHistogramF64 := func(values []float64, bins int) map[string]interface{} {
		if len(values) == 0 {
			return map[string]interface{}{"bins": []interface{}{}, "min": 0, "max": 0}
		}
		mn, mx := minF64(values), maxF64(values)
		rng := mx - mn
		if rng == 0 {
			rng = 1
		}
		binWidth := rng / float64(bins)
		counts := make([]int, bins)
		for _, v := range values {
			idx := int((v - mn) / binWidth)
			if idx >= bins {
				idx = bins - 1
			}
			counts[idx]++
		}
		binArr := make([]map[string]interface{}, bins)
		for i, c := range counts {
			binArr[i] = map[string]interface{}{"x": mn + float64(i)*binWidth, "w": binWidth, "count": c}
		}
		return map[string]interface{}{"bins": binArr, "min": mn, "max": mx}
	}
	buildHistogramInt := func(values []int, bins int) map[string]interface{} {
		if len(values) == 0 {
			return map[string]interface{}{"bins": []interface{}{}, "min": 0, "max": 0}
		}
		mn, mx := float64(minInt(values)), float64(maxInt(values))
		rng := mx - mn
		if rng == 0 {
			rng = 1
		}
		binWidth := rng / float64(bins)
		counts := make([]int, bins)
		for _, v := range values {
			idx := int((float64(v) - mn) / binWidth)
			if idx >= bins {
				idx = bins - 1
			}
			counts[idx]++
		}
		binArr := make([]map[string]interface{}, bins)
		for i, c := range counts {
			binArr[i] = map[string]interface{}{"x": mn + float64(i)*binWidth, "w": binWidth, "count": c}
		}
		return map[string]interface{}{"bins": binArr, "min": mn, "max": mx}
	}

	snrHistogram := buildHistogramF64(snrVals, 20)
	rssiHistogram := buildHistogramF64(rssiVals, 20)
	sizeHistogram := buildHistogramInt(packetSizes, 25)

	// Time span from min/max timestamps tracked during first pass
	timeSpanHours := 0.0
	if minTimestamp != "" && maxTimestamp != "" && minTimestamp != maxTimestamp {
		// Parse only 2 timestamps instead of 1.2M
		parseTS := func(ts string) (time.Time, bool) {
			t, err := time.Parse("2006-01-02 15:04:05", ts)
			if err != nil {
				t, err = time.Parse(time.RFC3339, ts)
			}
			if err != nil {
				return time.Time{}, false
			}
			return t, true
		}
		if tMin, ok := parseTS(minTimestamp); ok {
			if tMax, ok := parseTS(maxTimestamp); ok {
				timeSpanHours = float64(tMax.UnixMilli()-tMin.UnixMilli()) / 3600000.0
			}
		}
	}

	// Avg packet size
	avgPktSize := 0
	if len(packetSizes) > 0 {
		sum := 0
		for _, v := range packetSizes {
			sum += v
		}
		avgPktSize = sum / len(packetSizes)
	}

	// snrVals and rssiVals are already sorted — read min/max/median directly.
	snrStats := map[string]interface{}{"min": 0.0, "max": 0.0, "avg": 0.0, "median": 0.0, "stddev": 0.0}
	if len(snrVals) > 0 {
		snrStats = map[string]interface{}{
			"min": snrVals[0], "max": snrVals[len(snrVals)-1],
			"avg": snrAvg, "median": snrVals[len(snrVals)/2],
			"stddev": stddevF64(snrVals, snrAvg),
		}
	}
	rssiStats := map[string]interface{}{"min": 0.0, "max": 0.0, "avg": 0.0, "median": 0.0, "stddev": 0.0}
	if len(rssiVals) > 0 {
		rssiStats = map[string]interface{}{
			"min": rssiVals[0], "max": rssiVals[len(rssiVals)-1],
			"avg": rssiAvg, "median": rssiVals[len(rssiVals)/2],
			"stddev": stddevF64(rssiVals, rssiAvg),
		}
	}

	return map[string]interface{}{
		"totalPackets":       len(snrVals),
		"totalAllPackets":    totalObs,
		"totalTransmissions": len(regionalHashes),
		"snr":                snrStats,
		"rssi":               rssiStats,
		"snrValues":          snrHistogram,
		"rssiValues":         rssiHistogram,
		"packetSizes":        sizeHistogram,
		"minPacketSize":      minInt(packetSizes),
		"maxPacketSize":      maxInt(packetSizes),
		"avgPacketSize":      avgPktSize,
		"packetsPerHour":     packetsPerHour,
		"payloadTypes":       payloadTypes,
		"snrByType":          snrByTypeArr,
		"signalOverTime":     signalOverTime,
		"scatterData":        scatterData,
		"timeSpanHours":      timeSpanHours,
	}
}

// --- Topology Analytics ---

type nodeInfo struct {
	PublicKey        string
	Name             string
	Role             string
	Lat              float64
	Lon              float64
	HasGPS           bool
	LastSeen         time.Time
	FirstSeen        string // RFC3339; populated by buildNodeInfoMap for callers that need it (e.g. /api/nodes/{pk}/reach)
	ObservationCount int    // count of advertisements/observations; used for tier-3 tiebreak in resolveWithContext
}

// schemaDegradationLogged is now a PacketStore field (see type definition) so
// each store/test instance has a fresh dedupe set. Issue #1199 item 5: the
// prior package-level sync.Map silently suppressed re-emission across tests.

func (s *PacketStore) logSchemaDegradationOnce(msg string) {
	if _, loaded := s.schemaDegradationLogged.LoadOrStore(msg, true); !loaded {
		log.Printf("[store] schema-degradation: %s", msg)
	}
}

func (s *PacketStore) getAllNodes() []nodeInfo {
	// Schema probe: try richest → leanest. Logs a one-shot warning at each
	// rung when we fall back to a thinner schema so operators see that a
	// column is missing and the new tiebreak features are degraded. See
	// #1197 (adversarial r1 #10). first_seen was added (#1627 r3) so callers
	// like /api/nodes/{pk}/reach can avoid a per-request single-row SELECT;
	// we fold it into the cached load (see getCachedNodesAndPM, 30s TTL) so
	// the 4 buildNodeInfoMap call sites don't each pay for a fresh scan.
	rows, err := s.db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen, COALESCE(advert_count, 0), COALESCE(first_seen, '') FROM nodes")
	hasLastSeen := true
	hasAdvertCount := true
	hasFirstSeen := true
	if err != nil {
		s.logSchemaDegradationOnce("nodes.first_seen missing — buildNodeInfoMap will not populate FirstSeen for /api/nodes/{pk}/reach and friends")
		hasFirstSeen = false
		rows, err = s.db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen, COALESCE(advert_count, 0) FROM nodes")
		if err != nil {
			s.logSchemaDegradationOnce("nodes.advert_count missing — tier-3/4 ObservationCount tiebreak degraded; resolveWithContext will fall back to lex-pubkey order")
			rows, err = s.db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen FROM nodes")
			hasAdvertCount = false
			if err != nil {
				s.logSchemaDegradationOnce("nodes.last_seen missing — node freshness signal unavailable")
				rows, err = s.db.conn.Query("SELECT public_key, name, role, lat, lon FROM nodes")
				hasLastSeen = false
				if err != nil {
					return nil
				}
			}
		}
	}
	defer rows.Close()
	var nodes []nodeInfo
	for rows.Next() {
		var pk string
		var name, role sql.NullString
		var lat, lon sql.NullFloat64
		var lastSeen sql.NullString
		var advertCount sql.NullInt64
		var firstSeen sql.NullString
		if hasFirstSeen {
			rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen, &advertCount, &firstSeen)
		} else if hasAdvertCount {
			rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen, &advertCount)
		} else if hasLastSeen {
			rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen)
		} else {
			rows.Scan(&pk, &name, &role, &lat, &lon)
		}
		n := nodeInfo{PublicKey: pk, Name: nullStrVal(name), Role: nullStrVal(role)}
		if hasFirstSeen && firstSeen.Valid {
			n.FirstSeen = firstSeen.String
		}
		if lat.Valid && lon.Valid {
			n.Lat = lat.Float64
			n.Lon = lon.Float64
			n.HasGPS = !(n.Lat == 0 && n.Lon == 0)
		}
		if hasLastSeen && lastSeen.Valid && lastSeen.String != "" {
			if t, err := time.Parse(time.RFC3339, lastSeen.String); err == nil {
				n.LastSeen = t
			} else if t, err := time.Parse("2006-01-02 15:04:05", lastSeen.String); err == nil {
				n.LastSeen = t
			}
		}
		if hasAdvertCount && advertCount.Valid {
			n.ObservationCount = int(advertCount.Int64)
		}
		nodes = append(nodes, n)
	}
	return nodes
}

type prefixMap struct {
	m map[string][]nodeInfo
	// nonRelay holds lowercase pubkeys of observer-known nodes that have
	// advertised `repeat:off` in their MQTT /status message (issue #1290).
	// Such nodes are pure listeners and must never be selected as a
	// path-hop candidate by resolveWithContext, since by firmware
	// contract they do not forward packets. nil/empty preserves the
	// pre-#1290 behavior (every prefix-matching node is a candidate).
	nonRelay map[string]struct{}
}

// markNonRelay registers a set of lowercase pubkeys as listener-only.
// Called by the server when wiring the prefix map after reading the
// observers table's can_relay column. Issue #1290.
func (pm *prefixMap) markNonRelay(pubkeys []string) {
	if pm == nil {
		return
	}
	if pm.nonRelay == nil {
		pm.nonRelay = make(map[string]struct{}, len(pubkeys))
	}
	for _, pk := range pubkeys {
		pm.nonRelay[strings.ToLower(pk)] = struct{}{}
	}
}

// maxPrefixLen caps prefix map entries. MeshCore path hops use 2–6 char
// prefixes; 8 gives comfortable headroom while cutting map size from ~31×N
// entries to ~7×N (+ 1 full-key entry per node for exact-match lookups).
const maxPrefixLen = 8

// canAppearInPath returns true if the node's role allows it to appear as a
// path hop.  Only repeaters, room servers, and rooms can forward packets;
// companions and sensors originate but never relay.
func canAppearInPath(role string) bool {
	r := strings.ToLower(role)
	return strings.Contains(r, "repeater") || strings.Contains(r, "room_server") || r == "room"
}

func buildPrefixMap(nodes []nodeInfo) *prefixMap {
	pm := &prefixMap{m: make(map[string][]nodeInfo, len(nodes)*(maxPrefixLen+1))}
	for _, n := range nodes {
		if !canAppearInPath(n.Role) {
			continue
		}
		pk := strings.ToLower(n.PublicKey)
		maxLen := maxPrefixLen
		if maxLen > len(pk) {
			maxLen = len(pk)
		}
		for l := 2; l <= maxLen; l++ {
			pfx := pk[:l]
			pm.m[pfx] = append(pm.m[pfx], n)
		}
		// Always add full pubkey so exact-match lookups work.
		if len(pk) > maxPrefixLen {
			pm.m[pk] = append(pm.m[pk], n)
		}
	}
	return pm
}

// getCachedNodesAndPM returns cached node list and prefix map, rebuilding if stale.
// Must be called with s.mu held (RLock or Lock).
func (s *PacketStore) getCachedNodesAndPM() ([]nodeInfo, *prefixMap) {
	s.cacheMu.Lock()
	if s.nodeCache != nil && time.Since(s.nodeCacheTime) < 30*time.Second {
		nodes, pm := s.nodeCache, s.nodePM
		s.cacheMu.Unlock()
		return nodes, pm
	}
	s.cacheMu.Unlock()

	nodes := s.getAllNodes()
	pm := buildPrefixMap(nodes)
	// Issue #1290: exclude observers that advertised `repeat:off` from
	// the path-hop candidate set. Failure is non-fatal — we log via the
	// schema-degradation channel and proceed with an empty filter (i.e.
	// pre-#1290 behavior).
	if s.db != nil && s.db.conn != nil {
		if pks, err := s.db.GetNonRelayObserverPubkeys(); err == nil {
			pm.markNonRelay(pks)
		} else {
			s.logSchemaDegradationOnce("observers.can_relay read failed; path-hop disambiguator will not filter listener-only observers: " + err.Error())
		}
	}

	s.cacheMu.Lock()
	s.nodeCache = nodes
	s.nodePM = pm
	s.nodeCacheTime = time.Now()
	s.cacheMu.Unlock()

	return nodes, pm
}

// InvalidateNodeCache forces the next getCachedNodesAndPM call to rebuild.
func (s *PacketStore) InvalidateNodeCache() {
	s.cacheMu.Lock()
	s.nodeCache = nil
	s.nodePM = nil
	s.nodeCacheTime = time.Time{}
	s.cacheMu.Unlock()
}

func (pm *prefixMap) resolve(hop string) *nodeInfo {
	h := strings.ToLower(hop)
	candidates := pm.m[h]
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return &candidates[0]
	}
	// Multiple candidates: prefer one with GPS
	for i := range candidates {
		if candidates[i].HasGPS {
			return &candidates[i]
		}
	}
	return &candidates[0]
}

// resolveWithContext resolves a hop prefix using the neighbor affinity graph
// for disambiguation when multiple candidates match. It applies a 4-tier
// priority:
//
//	(1) "neighbor_affinity"           — graph score vs context nodes,
//	                                    requires affinity ≥3× runner-up and
//	                                    affinityMinObservations
//	(2) "geo_proximity"               — geographic proximity to GPS context
//	                                    centroid (only fires when at least
//	                                    one context node has GPS)
//	(3) "gps_preference"              — among GPS-having candidates, pick
//	                                    highest ObservationCount; lex-pubkey
//	                                    tiebreak for determinism
//	(4) "observation_count_fallback"  — no GPS available; pick highest
//	                                    ObservationCount; lex-pubkey tiebreak
//
// (Pre-PR #1197/#1198 the tier-3 step was first-GPS-wins and tier-4 was
// first-slice-element. Both now use observation count + lex tiebreak; the
// returned method label was renamed accordingly.)
//
// contextPubkeys are pubkeys of nodes that provide context for disambiguation
// (e.g., the originator, observer, or adjacent hops in the path).
// graph may be nil, in which case tier-1 is skipped.
func (pm *prefixMap) resolveWithContext(hop string, contextPubkeys []string, graph *NeighborGraph) (*nodeInfo, string, float64) {
	h := strings.ToLower(hop)
	candidates := pm.m[h]
	// Issue #1290: drop observer-known listener-only nodes from the
	// candidate set. By firmware contract a node that advertises
	// `repeat:off` in its MQTT /status will never relay a packet, so it
	// cannot legitimately be a hop in someone else's path. Filtering
	// here shrinks ambiguous candidate sets without affecting any
	// upstream caller (the returned shape and confidence labels are
	// preserved; only no_match becomes more likely when the only
	// matching prefix belonged to a listener). Empty pm.nonRelay
	// preserves the pre-#1290 behavior exactly (back-compat).
	if len(pm.nonRelay) > 0 && len(candidates) > 0 {
		filtered := candidates[:0:0]
		for i := range candidates {
			if _, isListener := pm.nonRelay[strings.ToLower(candidates[i].PublicKey)]; isListener {
				continue
			}
			filtered = append(filtered, candidates[i])
		}
		candidates = filtered
	}
	if len(candidates) == 0 {
		return nil, "no_match", 0
	}
	if len(candidates) == 1 {
		return &candidates[0], "unique_prefix", 1.0
	}

	// Priority 1: Affinity graph score
	//
	// NOTE: We use raw Score() (count × time-decay) here rather than Jaccard
	// similarity. Jaccard is used at the graph builder level (disambiguate() in
	// neighbor_graph.go) to resolve ambiguous edges by comparing neighbor-set
	// overlap. Here, edges are already resolved — we just need to pick the
	// highest-affinity candidate among them. Raw score is appropriate because
	// it reflects both observation frequency and recency, which are the right
	// signals for "which candidate is this hop most likely referring to."
	//
	// Issue #1229 (Option C): the raw score is further multiplied by
	// e.Confidence() — a source-diversity factor in (0,1] derived from the
	// number of distinct observers that contributed to the edge. Edges seen
	// by a single observer are discounted to 1/3 weight; edges seen by ≥3
	// observers saturate at full weight. This stacks with the geo-rejection
	// filter merged for #1228 to give two independent lines of defense
	// against cross-region prefix-collision pollution. Backward-compatible
	// with the persistence format: legacy edges with empty Observers sets
	// fall back to single-observer weight.
	if graph != nil && len(contextPubkeys) > 0 {
		type scored struct {
			idx   int
			score float64
			count int // observation count of the best-scoring edge
		}
		now := time.Now()
		// PERF (#1247): hoist per-context work out of the candidate loop.
		// The previous shape ran graph.Neighbors(ToLower(ctxPK)) and
		// re-lowercased candPK on every (cand, ctxPK) pair, then used
		// strings.EqualFold to compare two already-lowercased pubkeys.
		// At analytics scale (5k+ contextPubkeys, ~30k resolveHop calls)
		// this dominated computeAnalyticsTopology / computeAnalyticsRF
		// CPU time (37% / 55% of those endpoints respectively per
		// pprof). The new shape:
		//   1. Lowercases ctx pubkeys at most once per call (skipped
		//      entirely when the input is already lowercased — the
		//      common case for analytics callers that go through
		//      buildHopContextPubkeys).
		//   2. Lowercases candidate pubkeys at most once per call and
		//      uses raw == comparisons against NeighborEdge.NodeA/NodeB
		//      (which makeEdgeKey already lowercases).
		//   3. Loops outer-ctx / inner-edge / matched-cand-lookup. The
		//      previous shape was outer-cand / inner-ctx / inner-edge,
		//      which called graph.Neighbors(ctxPK) — taking the graph
		//      RLock each time — once per (cand, ctx) pair instead of
		//      once per ctx.
		lowerCtx := contextPubkeys
		needLower := false
		for _, p := range contextPubkeys {
			if hasUpperASCII(p) {
				needLower = true
				break
			}
		}
		if needLower {
			lowerCtx = make([]string, len(contextPubkeys))
			for i, p := range contextPubkeys {
				lowerCtx[i] = strings.ToLower(p)
			}
		}
		candPKs := make([]string, len(candidates))
		bestScores := make([]float64, len(candidates))
		bestCounts := make([]int, len(candidates))
		needLowerCand := false
		for i, c := range candidates {
			if hasUpperASCII(c.PublicKey) {
				needLowerCand = true
				break
			}
			candPKs[i] = c.PublicKey
		}
		if needLowerCand {
			for i, c := range candidates {
				candPKs[i] = strings.ToLower(c.PublicKey)
			}
		}
		candByPK := make(map[string]int, len(candidates))
		for i, pk := range candPKs {
			candByPK[pk] = i
		}
		for _, ctxPK := range lowerCtx {
			for _, e := range graph.Neighbors(ctxPK) {
				if e.Ambiguous {
					continue
				}
				otherPK := e.NodeA
				if otherPK == ctxPK {
					otherPK = e.NodeB
				}
				ci, ok := candByPK[otherPK]
				if !ok {
					continue
				}
				s := e.Score(now) * e.Confidence()
				if s > bestScores[ci] {
					bestScores[ci] = s
					bestCounts[ci] = e.Count
				}
			}
		}
		var scores []scored
		for i, s := range bestScores {
			if s > 0 {
				scores = append(scores, scored{i, s, bestCounts[i]})
			}
		}

		if len(scores) >= 1 {
			// Sort descending
			for i := 0; i < len(scores)-1; i++ {
				for j := i + 1; j < len(scores); j++ {
					if scores[j].score > scores[i].score {
						scores[i], scores[j] = scores[j], scores[i]
					}
				}
			}
			best := scores[0]
			// Require both score ratio ≥ 3× AND minimum observations (mirrors
			// disambiguate() in neighbor_graph.go which checks affinityMinObservations).
			if best.count >= affinityMinObservations &&
				(len(scores) == 1 || best.score >= affinityConfidenceRatio*scores[1].score) {
				return &candidates[best.idx], "neighbor_affinity", best.score
			}
			// Scores too close — fall through to lower-priority strategies
		}
	}

	// Priority 2: Geographic proximity (if context pubkeys have GPS and candidates have GPS)
	if len(contextPubkeys) > 0 {
		// Find GPS positions of context nodes from the prefix map or candidates
		// We need nodeInfo for context pubkeys — look them up
		var contextLat, contextLon float64
		var contextGPSCount int
		for _, ctxPK := range contextPubkeys {
			ctxLower := strings.ToLower(ctxPK)
			if infos, ok := pm.m[ctxLower]; ok && len(infos) == 1 && infos[0].HasGPS {
				contextLat += infos[0].Lat
				contextLon += infos[0].Lon
				contextGPSCount++
			}
		}
		if contextGPSCount > 0 {
			contextLat /= float64(contextGPSCount)
			contextLon /= float64(contextGPSCount)

			bestIdx := -1
			bestDist := math.MaxFloat64
			for i, cand := range candidates {
				if !cand.HasGPS {
					continue
				}
				d := geoDistApprox(contextLat, contextLon, cand.Lat, cand.Lon)
				if d < bestDist {
					bestDist = d
					bestIdx = i
				}
			}
			if bestIdx >= 0 {
				return &candidates[bestIdx], "geo_proximity", 0
			}
		}
	}

	// Priority 3: GPS preference. Among GPS-having candidates, prefer the one
	// with the highest observation count (recent/active evidence) rather than
	// slice/DB-insertion order. Ties on count are broken by lexicographically
	// smallest PublicKey for full determinism. See #1197.
	bestGPSIdx := -1
	for i := range candidates {
		if !candidates[i].HasGPS {
			continue
		}
		if bestGPSIdx < 0 || betterByObsCount(&candidates[i], &candidates[bestGPSIdx]) {
			bestGPSIdx = i
		}
	}
	if bestGPSIdx >= 0 {
		return &candidates[bestGPSIdx], "gps_preference", 0
	}

	// Priority 4: Fallback — pick the candidate with the highest observation
	// count (no GPS available on any candidate). Avoids slice-order
	// arbitrariness. Ties on count are broken by lexicographically smallest
	// PublicKey. Method label "observation_count_fallback" — the previous
	// "first_match" was misleading after the tier-4 algorithm changed in
	// PR #1198 (adversarial r1 #2).
	bestIdx := 0
	for i := 1; i < len(candidates); i++ {
		if betterByObsCount(&candidates[i], &candidates[bestIdx]) {
			bestIdx = i
		}
	}
	return &candidates[bestIdx], "observation_count_fallback", 0
}

// betterByObsCount reports whether candidate a should beat b under the
// tier-3/4 selection rule: higher ObservationCount wins; ties go to the
// lexicographically smaller PublicKey for determinism. Pointer receivers
// avoid value-copying nodeInfo (string + 2 floats + time.Time + int) on
// the hot resolve path. See #1197 (adversarial r1 #6, carmack r1 #4).
func betterByObsCount(a, b *nodeInfo) bool {
	if a.ObservationCount != b.ObservationCount {
		return a.ObservationCount > b.ObservationCount
	}
	return a.PublicKey < b.PublicKey
}

// geoDistApprox returns an approximate distance between two lat/lon points
// (equirectangular approximation, sufficient for relative comparison).
func geoDistApprox(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180 * math.Cos((lat1+lat2)/2*math.Pi/180)
	return math.Sqrt(dLat*dLat + dLon*dLon)
}

func parsePathJSON(pathJSON string) []string {
	if pathJSON == "" || pathJSON == "[]" {
		return nil
	}
	var hops []string
	if json.Unmarshal([]byte(pathJSON), &hops) != nil {
		return nil
	}
	return hops
}

func (s *PacketStore) GetAnalyticsTopology(region, area string) map[string]interface{} {
	return s.GetAnalyticsTopologyWithWindow(region, area, TimeWindow{})
}

// GetAnalyticsTopologyWithWindow — see issue #842.
// For default (region="", area="", zero window), prefer the steady-state
// recomputer snapshot if registered (issue #1240).
func (s *PacketStore) GetAnalyticsTopologyWithWindow(region, area string, window TimeWindow) map[string]interface{} {
	if region == "" && area == "" && window.IsZero() {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompTopology
		s.analyticsRecomputerMu.RUnlock()
		if rc != nil {
			if v := rc.Load(); v != nil {
				if m, ok := v.(map[string]interface{}); ok {
					s.cacheMu.Lock()
					s.cacheHits++
					s.cacheMu.Unlock()
					return m
				}
			}
		}
	}
	cacheKey := region + "|" + area
	if !window.IsZero() {
		cacheKey += "|" + window.CacheKey()
	}
	s.cacheMu.Lock()
	if cached, ok := s.topoCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsTopology(region, area, window)

	s.cacheMu.Lock()
	s.topoCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsTopology(region, area string, window TimeWindow) map[string]interface{} {
	var areaNodes map[string]bool
	if area != "" {
		areaNodes = s.resolveAreaNodes(area)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	allNodes, pm := s.getCachedNodesAndPM()
	_ = allNodes // only pm is needed for topology

	// Materialize the filtered tx slice ONCE — both the context-build pass
	// and the main aggregation pass need the same window+region predicate.
	// Two scans of s.packets re-running identical predicates is wasteful at
	// the 30k+ packet hot-path scale (#1199 item 2). One filter, two passes
	// over the result.
	filteredTxs := make([]*StoreTx, 0, len(s.packets))
	for _, tx := range s.packets {
		if !window.Includes(tx.FirstSeen) {
			continue
		}
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		filteredTxs = append(filteredTxs, tx)
	}

	// Pre-pass: build the full hop-disambiguation context from all in-window
	// txs BEFORE any resolveHop call. The earlier shape — populating
	// contextPubkeys lazily during the main scan and reading it from a
	// closure — was correct only because the current code never calls
	// resolveHop inside the scan loop. A future maintainer who adds such a
	// call inside the loop would silently get partial context AND a
	// stale-cached result for any hop seen before the context grew. Two
	// explicit passes remove the hazard. See #1197 (carmack/adversarial r1).
	var contextPubkeys []string
	{
		seen := make(map[string]struct{}, 64)
		for _, tx := range filteredTxs {
			for _, pk := range buildHopContextPubkeys(tx, pm) {
				if _, ok := seen[pk]; ok {
					continue
				}
				seen[pk] = struct{}{}
				contextPubkeys = append(contextPubkeys, pk)
			}
		}
	}

	hopCache := make(map[string]*nodeInfo)
	graph := s.graph.Load() // hoist out of resolver closure (PR #1208 carmack #1)
	resolveHop := func(hop string) *nodeInfo {
		if cached, ok := hopCache[hop]; ok {
			return cached
		}
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		hopCache[hop] = r
		return r
	}

	hopCounts := map[int]int{}
	var allHopsList []int
	hopSnr := map[int][]float64{}
	hopFreq := map[string]int{}
	pairFreq := map[string]int{}
	observerMap := map[string]string{} // observer_id → observer_name
	perObserver := map[string]map[string]*struct{ minDist, maxDist, count int }{}

	for _, tx := range filteredTxs {
		hops := txGetParsedPath(tx)
		if len(hops) == 0 {
			continue
		}

		// Area filter: skip this tx entirely from hop-count stats if none of its
		// hops belong to an area node — keeps hopCounts histogram consistent with
		// hopFreq, which already filters per-hop.
		if areaNodes != nil {
			hasAreaHop := false
			for _, h := range hops {
				r := resolveHop(h)
				if r != nil && areaNodes[r.PublicKey] {
					hasAreaHop = true
					break
				}
			}
			if !hasAreaHop {
				continue
			}
		}

		n := len(hops)
		hopCounts[n]++
		allHopsList = append(allHopsList, n)
		if tx.SNR != nil {
			hopSnr[n] = append(hopSnr[n], *tx.SNR)
		}
		for _, h := range hops {
			// Area filter: only count hops belonging to nodes in the area.
			if areaNodes != nil {
				r := resolveHop(h)
				if r == nil || !areaNodes[r.PublicKey] {
					continue
				}
			}
			hopFreq[h]++
		}
		for i := 0; i < len(hops)-1; i++ {
			a, b := hops[i], hops[i+1]
			// Area filter: only count pairs where both nodes are in the area.
			if areaNodes != nil {
				rA := resolveHop(a)
				rB := resolveHop(b)
				if rA == nil || !areaNodes[rA.PublicKey] || rB == nil || !areaNodes[rB.PublicKey] {
					continue
				}
			}
			if a > b {
				a, b = b, a
			}
			pairFreq[a+"|"+b]++
		}

		obsID := tx.ObserverID
		if obsID != "" {
			observerMap[obsID] = tx.ObserverName
		}
		if _, ok := perObserver[obsID]; !ok {
			perObserver[obsID] = map[string]*struct{ minDist, maxDist, count int }{}
		}
		for i, h := range hops {
			if areaNodes != nil {
				r := resolveHop(h)
				if r == nil || !areaNodes[r.PublicKey] {
					continue
				}
			}
			dist := n - i
			entry := perObserver[obsID][h]
			if entry == nil {
				entry = &struct{ minDist, maxDist, count int }{dist, dist, 0}
				perObserver[obsID][h] = entry
			}
			if dist < entry.minDist {
				entry.minDist = dist
			}
			if dist > entry.maxDist {
				entry.maxDist = dist
			}
			entry.count++
		}
	}

	// Hop distribution
	hopDist := make([]map[string]interface{}, 0)
	for h, c := range hopCounts {
		if h <= 25 {
			hopDist = append(hopDist, map[string]interface{}{"hops": h, "count": c})
		}
	}
	sort.Slice(hopDist, func(i, j int) bool {
		return hopDist[i]["hops"].(int) < hopDist[j]["hops"].(int)
	})

	avgHops := 0.0
	if len(allHopsList) > 0 {
		sum := 0
		for _, v := range allHopsList {
			sum += v
		}
		avgHops = float64(sum) / float64(len(allHopsList))
	}
	medianHops := 0
	if len(allHopsList) > 0 {
		sorted := make([]int, len(allHopsList))
		copy(sorted, allHopsList)
		sort.Ints(sorted)
		medianHops = sorted[len(sorted)/2]
	}
	maxHops := 0
	for _, v := range allHopsList {
		if v > maxHops {
			maxHops = v
		}
	}

	// pmLookup resolves a hop hex string to its prefix-map candidates,
	// applying the same truncation used during map construction.
	pmLookup := func(hop string) []nodeInfo {
		key := strings.ToLower(hop)
		if len(key) > maxPrefixLen {
			key = key[:maxPrefixLen]
		}
		return pm.m[key]
	}

	// --- Dedup pass: merge hop prefixes that resolve unambiguously to the same node ---
	// Only merge when pm.m[hop] has exactly 1 candidate (unique_prefix).
	// Ambiguous short prefixes (efiten's concern: 1-byte collisions) stay separate.
	{
		type dedupInfo struct {
			totalCount int
			longestHop string
		}
		byPubkey := map[string]*dedupInfo{} // pubkey → merged info
		ambiguous := map[string]int{}       // hop → count (kept as-is)
		for h, c := range hopFreq {
			candidates := pmLookup(h)
			if len(candidates) == 1 {
				pk := strings.ToLower(candidates[0].PublicKey)
				if info, ok := byPubkey[pk]; ok {
					info.totalCount += c
					if len(h) > len(info.longestHop) {
						info.longestHop = h
					}
				} else {
					byPubkey[pk] = &dedupInfo{totalCount: c, longestHop: h}
				}
			} else {
				ambiguous[h] = c
			}
		}
		// Rebuild hopFreq
		hopFreq = make(map[string]int, len(byPubkey)+len(ambiguous))
		for _, info := range byPubkey {
			hopFreq[info.longestHop] = info.totalCount
		}
		for h, c := range ambiguous {
			hopFreq[h] = c
		}
	}

	// --- Dedup pass for pairs: merge by resolved pubkey pair ---
	{
		type pairDedupInfo struct {
			totalCount int
			longestA   string
			longestB   string
		}
		byPubkeyPair := map[string]*pairDedupInfo{} // "pkA|pkB" (sorted) → merged info
		ambiguousPairs := map[string]int{}
		for p, c := range pairFreq {
			parts := strings.SplitN(p, "|", 2)
			candA := pmLookup(parts[0])
			candB := pmLookup(parts[1])
			if len(candA) == 1 && len(candB) == 1 {
				pkA := strings.ToLower(candA[0].PublicKey)
				pkB := strings.ToLower(candB[0].PublicKey)
				// Canonicalize by sorted pubkey
				if pkA > pkB {
					pkA, pkB = pkB, pkA
					parts[0], parts[1] = parts[1], parts[0]
				}
				key := pkA + "|" + pkB
				if info, ok := byPubkeyPair[key]; ok {
					info.totalCount += c
					if len(parts[0]) > len(info.longestA) {
						info.longestA = parts[0]
					}
					if len(parts[1]) > len(info.longestB) {
						info.longestB = parts[1]
					}
				} else {
					byPubkeyPair[key] = &pairDedupInfo{totalCount: c, longestA: parts[0], longestB: parts[1]}
				}
			} else {
				ambiguousPairs[p] = c
			}
		}
		// Rebuild pairFreq
		pairFreq = make(map[string]int, len(byPubkeyPair)+len(ambiguousPairs))
		for _, info := range byPubkeyPair {
			a, b := info.longestA, info.longestB
			if a > b {
				a, b = b, a
			}
			pairFreq[a+"|"+b] = info.totalCount
		}
		for p, c := range ambiguousPairs {
			pairFreq[p] = c
		}
	}

	// Top repeaters
	type freqEntry struct {
		hop   string
		count int
	}
	freqList := make([]freqEntry, 0, len(hopFreq))
	for h, c := range hopFreq {
		freqList = append(freqList, freqEntry{h, c})
	}
	sort.Slice(freqList, func(i, j int) bool { return freqList[i].count > freqList[j].count })
	topRepeaters := make([]map[string]interface{}, 0)
	for i, e := range freqList {
		if i >= 20 {
			break
		}
		r := resolveHop(e.hop)
		entry := map[string]interface{}{"hop": e.hop, "count": e.count, "name": nil, "pubkey": nil}
		if r != nil {
			entry["name"] = r.Name
			entry["pubkey"] = r.PublicKey
		}
		topRepeaters = append(topRepeaters, entry)
	}

	// Top pairs
	pairList := make([]freqEntry, 0, len(pairFreq))
	for p, c := range pairFreq {
		pairList = append(pairList, freqEntry{p, c})
	}
	sort.Slice(pairList, func(i, j int) bool { return pairList[i].count > pairList[j].count })
	topPairs := make([]map[string]interface{}, 0)
	for i, e := range pairList {
		if i >= 15 {
			break
		}
		parts := strings.SplitN(e.hop, "|", 2)
		rA := resolveHop(parts[0])
		rB := resolveHop(parts[1])
		entry := map[string]interface{}{
			"hopA": parts[0], "hopB": parts[1], "count": e.count,
			"nameA": nil, "nameB": nil, "pubkeyA": nil, "pubkeyB": nil,
		}
		if rA != nil {
			entry["nameA"] = rA.Name
			entry["pubkeyA"] = rA.PublicKey
		}
		if rB != nil {
			entry["nameB"] = rB.Name
			entry["pubkeyB"] = rB.PublicKey
		}
		topPairs = append(topPairs, entry)
	}

	// Hops vs SNR
	hopsVsSnr := make([]map[string]interface{}, 0)
	for h, snrs := range hopSnr {
		if h > 20 {
			continue
		}
		sum := 0.0
		for _, v := range snrs {
			sum += v
		}
		hopsVsSnr = append(hopsVsSnr, map[string]interface{}{
			"hops": h, "count": len(snrs), "avgSnr": sum / float64(len(snrs)),
		})
	}
	sort.Slice(hopsVsSnr, func(i, j int) bool {
		return hopsVsSnr[i]["hops"].(int) < hopsVsSnr[j]["hops"].(int)
	})

	// Observers list
	observers := make([]map[string]interface{}, 0)
	for id, name := range observerMap {
		n := name
		if n == "" {
			n = id
		}
		observers = append(observers, map[string]interface{}{"id": id, "name": n})
	}

	// Per-observer reachability
	perObserverReach := map[string]interface{}{}
	for obsID, nodes := range perObserver {
		obsName := observerMap[obsID]
		if obsName == "" {
			obsName = obsID
		}
		byDist := map[int][]map[string]interface{}{}
		for hop, data := range nodes {
			d := data.minDist
			if d > 15 {
				continue
			}
			r := resolveHop(hop)
			entry := map[string]interface{}{
				"hop": hop, "name": nil, "pubkey": nil,
				"count": data.count, "distRange": nil,
			}
			if r != nil {
				entry["name"] = r.Name
				entry["pubkey"] = r.PublicKey
			}
			if data.minDist != data.maxDist {
				entry["distRange"] = fmt.Sprintf("%d-%d", data.minDist, data.maxDist)
			}
			byDist[d] = append(byDist[d], entry)
		}
		rings := make([]map[string]interface{}, 0)
		for dist, nodeList := range byDist {
			sort.Slice(nodeList, func(i, j int) bool {
				return nodeList[i]["count"].(int) > nodeList[j]["count"].(int)
			})
			rings = append(rings, map[string]interface{}{"hops": dist, "nodes": nodeList})
		}
		sort.Slice(rings, func(i, j int) bool {
			return rings[i]["hops"].(int) < rings[j]["hops"].(int)
		})
		perObserverReach[obsID] = map[string]interface{}{
			"observer_name": obsName,
			"rings":         rings,
		}
	}

	// Cross-observer: build from perObserver
	crossObserver := map[string][]map[string]interface{}{}
	bestPath := map[string]map[string]interface{}{}
	for obsID, nodes := range perObserver {
		obsName := observerMap[obsID]
		if obsName == "" {
			obsName = obsID
		}
		for hop, data := range nodes {
			crossObserver[hop] = append(crossObserver[hop], map[string]interface{}{
				"observer_id": obsID, "observer_name": obsName,
				"minDist": data.minDist, "count": data.count,
			})
			if bp, ok := bestPath[hop]; !ok || data.minDist < bp["minDist"].(int) {
				bestPath[hop] = map[string]interface{}{
					"minDist": data.minDist, "observer_id": obsID, "observer_name": obsName,
				}
			}
		}
	}

	// Multi-observer nodes
	multiObsNodes := make([]map[string]interface{}, 0)
	for hop, obs := range crossObserver {
		if len(obs) <= 1 {
			continue
		}
		sort.Slice(obs, func(i, j int) bool {
			return obs[i]["minDist"].(int) < obs[j]["minDist"].(int)
		})
		r := resolveHop(hop)
		entry := map[string]interface{}{
			"hop": hop, "name": nil, "pubkey": nil, "observers": obs,
		}
		if r != nil {
			entry["name"] = r.Name
			entry["pubkey"] = r.PublicKey
		}
		multiObsNodes = append(multiObsNodes, entry)
	}
	sort.Slice(multiObsNodes, func(i, j int) bool {
		return len(multiObsNodes[i]["observers"].([]map[string]interface{})) >
			len(multiObsNodes[j]["observers"].([]map[string]interface{}))
	})
	if len(multiObsNodes) > 50 {
		multiObsNodes = multiObsNodes[:50]
	}

	// Best path list
	bestPathList := make([]map[string]interface{}, 0, len(bestPath))
	for hop, data := range bestPath {
		r := resolveHop(hop)
		entry := map[string]interface{}{
			"hop": hop, "name": nil, "pubkey": nil,
			"minDist": data["minDist"], "observer_id": data["observer_id"],
			"observer_name": data["observer_name"],
		}
		if r != nil {
			entry["name"] = r.Name
			entry["pubkey"] = r.PublicKey
		}
		bestPathList = append(bestPathList, entry)
	}
	sort.Slice(bestPathList, func(i, j int) bool {
		return bestPathList[i]["minDist"].(int) < bestPathList[j]["minDist"].(int)
	})
	if len(bestPathList) > 50 {
		bestPathList = bestPathList[:50]
	}

	// Use DB 7-day active node count (matches /api/stats totalNodes)
	uniqueNodes := 0
	if s.db != nil {
		if stats, err := s.db.GetStats(); err == nil {
			uniqueNodes = stats.TotalNodes
		}
	}

	return map[string]interface{}{
		"uniqueNodes":      uniqueNodes,
		"avgHops":          avgHops,
		"medianHops":       medianHops,
		"maxHops":          maxHops,
		"hopDistribution":  hopDist,
		"topRepeaters":     topRepeaters,
		"topPairs":         topPairs,
		"hopsVsSnr":        hopsVsSnr,
		"observers":        observers,
		"perObserverReach": perObserverReach,
		"multiObsNodes":    multiObsNodes,
		"bestPathList":     bestPathList,
	}
}

// --- Distance Analytics ---

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// GetAnalyticsDistance returns the distance analytics map. For the
// default (region="", area="") query, prefer the steady-state recomputer
// snapshot if one is registered (issue #1240). Region/area-keyed variants
// continue to use the legacy TTL cache + on-request compute.
func (s *PacketStore) GetAnalyticsDistance(region, area string) map[string]interface{} {
	if region == "" && area == "" {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompDistance
		s.analyticsRecomputerMu.RUnlock()
		if rc != nil {
			if v := rc.Load(); v != nil {
				if m, ok := v.(map[string]interface{}); ok {
					s.cacheMu.Lock()
					s.cacheHits++
					s.cacheMu.Unlock()
					return m
				}
			}
		}
	}
	cacheKey := region + "|" + area
	s.cacheMu.Lock()
	if cached, ok := s.distCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsDistance(region, area)

	s.cacheMu.Lock()
	s.distCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsDistance(region, area string) map[string]interface{} {
	// #1239: hold s.mu.RLock() only long enough to (a) snapshot the
	// distHops/distPaths slice headers and (b) build the region match
	// set (which reads tx.Observations, a field mutated by ingest under
	// s.mu.Lock). All filtering, sorting, deduping, histogram building
	// and category stats run on locally-captured slices OUTSIDE the
	// lock so concurrent ingest writers are not blocked by readers.
	//
	// Safety: snapshot slice headers are O(1). distHops/distPaths are
	// append-only via re-slice in buildDistanceIndex / updateDistanceIndexForTxs
	// under s.mu.Lock; if the backing array is reallocated after we
	// release the RLock, our snapshot still points at the prior backing
	// array (kept alive by GC) and observes the consistent length we
	// captured. The distHopRecord / distPathRecord values themselves
	// are value types (not pointers to live records) so we cannot read
	// torn writes from them post-release.
	var regionObs map[string]bool
	if region != "" {
		// resolveRegionObservers uses its own mutex (regionObsMu)
		// and is safe to call without s.mu held.
		regionObs = s.resolveRegionObservers(region)
	}
	var areaNodes map[string]bool
	if area != "" {
		areaNodes = s.resolveAreaNodes(area)
	}

	s.mu.RLock()
	hopsSnap := s.distHops
	pathsSnap := s.distPaths

	// Build region match set INSIDE the lock — touches tx.Observations
	// (slice header mutated by ingest). For non-region calls (the common
	// cold path) we skip this entirely and the RLock hold is microseconds.
	var matchSet map[*StoreTx]bool
	if regionObs != nil {
		matchSet = make(map[*StoreTx]bool)
		seen := make(map[*StoreTx]bool)
		for i := range hopsSnap {
			tx := hopsSnap[i].tx
			if tx == nil || seen[tx] {
				continue
			}
			seen[tx] = true
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					matchSet[tx] = true
					break
				}
			}
		}
		for i := range pathsSnap {
			tx := pathsSnap[i].tx
			if tx == nil || seen[tx] {
				continue
			}
			seen[tx] = true
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					matchSet[tx] = true
					break
				}
			}
		}
	}
	s.mu.RUnlock()

	// Everything below operates on hopsSnap / pathsSnap / matchSet —
	// no s.mu, no s.distHops / s.distPaths access. Safe to run while
	// ingest writers reallocate the underlying store-owned slices.

	// Additionally filter matchSet by area nodes
	if areaNodes != nil && matchSet != nil {
		for tx := range matchSet {
			d := tx.ParsedDecoded()
			pk, _ := d["public_key"].(string)
			if pk == "" {
				pk, _ = d["pubKey"].(string)
			}
			if pk == "" || !areaNodes[pk] {
				delete(matchSet, tx)
			}
		}
	} else if areaNodes != nil {
		// No region filter but area filter: build matchSet from area nodes
		matchSet = make(map[*StoreTx]bool)
		for i := range s.distHops {
			tx := s.distHops[i].tx
			if matchSet[tx] {
				continue
			}
			d := tx.ParsedDecoded()
			pk, _ := d["public_key"].(string)
			if pk == "" {
				pk, _ = d["pubKey"].(string)
			}
			if pk != "" && areaNodes[pk] {
				matchSet[tx] = true
			}
		}
		for i := range s.distPaths {
			tx := s.distPaths[i].tx
			if matchSet[tx] {
				continue
			}
			d := tx.ParsedDecoded()
			pk, _ := d["public_key"].(string)
			if pk == "" {
				pk, _ = d["pubKey"].(string)
			}
			if pk != "" && areaNodes[pk] {
				matchSet[tx] = true
			}
		}
	}

	// Filter precomputed hop records (copy to avoid mutating precomputed data during sort)
	filteredHops := make([]distHopRecord, 0, len(hopsSnap))
	for i := range hopsSnap {
		if matchSet == nil || matchSet[hopsSnap[i].tx] {
			filteredHops = append(filteredHops, hopsSnap[i])
		}
	}

	// Filter precomputed path records
	filteredPaths := make([]distPathRecord, 0, len(pathsSnap))
	for i := range pathsSnap {
		if matchSet == nil || matchSet[pathsSnap[i].tx] {
			filteredPaths = append(filteredPaths, pathsSnap[i])
		}
	}

	// Build category stats and time series from precomputed data
	catDists := map[string][]float64{"R↔R": {}, "C↔R": {}, "C↔C": {}}
	distByHour := map[string][]float64{}
	for i := range filteredHops {
		h := &filteredHops[i]
		catDists[h.Type] = append(catDists[h.Type], h.Dist)
		if h.HourBucket != "" {
			distByHour[h.HourBucket] = append(distByHour[h.HourBucket], h.Dist)
		}
	}

	topHops := dedupeHopsByPair(filteredHops, 20)

	// Sort and pick top paths
	sort.Slice(filteredPaths, func(i, j int) bool { return filteredPaths[i].TotalDist > filteredPaths[j].TotalDist })
	topPaths := make([]map[string]interface{}, 0)
	for i := range filteredPaths {
		if i >= 20 {
			break
		}
		p := &filteredPaths[i]
		hops := make([]map[string]interface{}, len(p.Hops))
		for j, hd := range p.Hops {
			hops[j] = map[string]interface{}{
				"fromName": hd.FromName, "fromPk": hd.FromPk,
				"toName": hd.ToName, "toPk": hd.ToPk,
				"dist": hd.Dist,
			}
		}
		topPaths = append(topPaths, map[string]interface{}{
			"hash": p.Hash, "totalDist": p.TotalDist,
			"hopCount": p.HopCount, "timestamp": p.Timestamp, "hops": hops,
		})
	}

	// Category stats
	medianF := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		c := make([]float64, len(arr))
		copy(c, arr)
		sort.Float64s(c)
		return c[len(c)/2]
	}
	minF := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v < m {
				m = v
			}
		}
		return m
	}
	maxF := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v > m {
				m = v
			}
		}
		return m
	}

	catStats := map[string]interface{}{}
	for cat, dists := range catDists {
		if len(dists) == 0 {
			catStats[cat] = map[string]interface{}{"count": 0, "avg": 0, "median": 0, "min": 0, "max": 0}
			continue
		}
		sum := 0.0
		for _, v := range dists {
			sum += v
		}
		avg := sum / float64(len(dists))
		catStats[cat] = map[string]interface{}{
			"count":  len(dists),
			"avg":    math.Round(avg*100) / 100,
			"median": math.Round(medianF(dists)*100) / 100,
			"min":    math.Round(minF(dists)*100) / 100,
			"max":    math.Round(maxF(dists)*100) / 100,
		}
	}

	// Distance histogram
	var distHistogram interface{} = []interface{}{}
	allDists := make([]float64, len(filteredHops))
	for i := range filteredHops {
		allDists[i] = filteredHops[i].Dist
	}
	if len(allDists) > 0 {
		hMin, hMax := minF(allDists), maxF(allDists)
		binCount := 25
		binW := (hMax - hMin) / float64(binCount)
		if binW == 0 {
			binW = 1
		}
		bins := make([]int, binCount)
		for _, d := range allDists {
			idx := int(math.Floor((d - hMin) / binW))
			if idx >= binCount {
				idx = binCount - 1
			}
			if idx < 0 {
				idx = 0
			}
			bins[idx]++
		}
		binArr := make([]map[string]interface{}, binCount)
		for i, c := range bins {
			binArr[i] = map[string]interface{}{
				"x":     math.Round((hMin+float64(i)*binW)*10) / 10,
				"w":     math.Round(binW*10) / 10,
				"count": c,
			}
		}
		distHistogram = map[string]interface{}{"bins": binArr, "min": hMin, "max": hMax}
	}

	// Distance over time
	timeKeys := make([]string, 0, len(distByHour))
	for k := range distByHour {
		timeKeys = append(timeKeys, k)
	}
	sort.Strings(timeKeys)
	distOverTime := make([]map[string]interface{}, 0, len(timeKeys))
	for _, hour := range timeKeys {
		dists := distByHour[hour]
		sum := 0.0
		for _, v := range dists {
			sum += v
		}
		distOverTime = append(distOverTime, map[string]interface{}{
			"hour":  hour,
			"avg":   math.Round(sum/float64(len(dists))*100) / 100,
			"count": len(dists),
		})
	}

	// Summary
	summary := map[string]interface{}{
		"totalHops":  len(filteredHops),
		"totalPaths": len(filteredPaths),
		"avgDist":    0.0,
		"maxDist":    0.0,
	}
	if len(allDists) > 0 {
		sum := 0.0
		for _, v := range allDists {
			sum += v
		}
		summary["avgDist"] = math.Round(sum/float64(len(allDists))*100) / 100
		summary["maxDist"] = math.Round(maxF(allDists)*100) / 100
	}

	return map[string]interface{}{
		"summary":       summary,
		"topHops":       topHops,
		"topPaths":      topPaths,
		"catStats":      catStats,
		"distHistogram": distHistogram,
		"distOverTime":  distOverTime,
	}
}

// --- Hash Sizes Analytics ---

func (s *PacketStore) GetAnalyticsHashSizes(region, area string) map[string]interface{} {
	if region == "" && area == "" {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompHashSizes
		s.analyticsRecomputerMu.RUnlock()
		if rc != nil {
			if v := rc.Load(); v != nil {
				if m, ok := v.(map[string]interface{}); ok {
					s.cacheMu.Lock()
					s.cacheHits++
					s.cacheMu.Unlock()
					return m
				}
			}
		}
	}
	cacheKey := region + "|" + area
	s.cacheMu.Lock()
	if cached, ok := s.hashCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsHashSizesWithCapability(region, area)

	s.cacheMu.Lock()
	s.hashCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

// computeAnalyticsHashSizesWithCapability runs computeAnalyticsHashSizes
// then layers in the multiByteCapability augmentation. Extracted so the
// steady-state recomputer (issue #1240) produces the same shape as the
// cached GetAnalyticsHashSizes call.
func (s *PacketStore) computeAnalyticsHashSizesWithCapability(region, area string) map[string]interface{} {
	result := s.computeAnalyticsHashSizes(region, area)
	globalAdopterHS := make(map[string]int)
	if region == "" {
		if mbNodes, ok := result["multiByteNodes"].([]map[string]interface{}); ok {
			for _, n := range mbNodes {
				pk, _ := n["pubkey"].(string)
				hs, _ := n["hashSize"].(int)
				if pk != "" && hs >= 2 {
					globalAdopterHS[pk] = hs
				}
			}
		}
	} else {
		// Pull the global multiByteNodes set without the region/area filter.
		// Use a separate compute call (not the cached path) to avoid
		// recursive locking on hashCache and to keep this side-effect free.
		globalRes := s.computeAnalyticsHashSizes("", "")
		if mbNodes, ok := globalRes["multiByteNodes"].([]map[string]interface{}); ok {
			for _, n := range mbNodes {
				pk, _ := n["pubkey"].(string)
				hs, _ := n["hashSize"].(int)
				if pk != "" && hs >= 2 {
					globalAdopterHS[pk] = hs
				}
			}
		}
	}
	mbEntries := s.computeMultiByteCapability(globalAdopterHS)
	result["multiByteCapability"] = mbEntries

	// Build the O(1) lookup index OUTSIDE the cache lock — at Cascadia
	// scale this is a ~2400-entry allocation + hash + insert per cycle.
	// Holding cacheMu while doing it blocks every API reader for the
	// duration. Swap the pointers in under a short write-lock.
	mbIdx := make(map[string]MultiByteCapEntry, len(mbEntries))
	for _, e := range mbEntries {
		mbIdx[e.PublicKey] = e
	}
	s.cacheMu.Lock()
	s.mbCapSnapshot = mbEntries
	s.mbCapIndex = mbIdx
	s.cacheMu.Unlock()
	// Publish snapshot to the on-disk handoff so the ingestor can
	// persist it (#1289/#1324: server is read-only; persistence is the
	// ingestor's job). Best-effort — a write failure here does not
	// affect serving (the in-memory index above is the read path).
	s.publishMultibyteCapSnapshot(mbEntries)

	return result
}

func (s *PacketStore) computeAnalyticsHashSizes(region, area string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}
	var areaNodes map[string]bool
	if area != "" {
		areaNodes = s.resolveAreaNodes(area)
	}

	// #804: derive each node's HOME region from zero-hop direct adverts (the
	// most authoritative location signal — those packets cannot have been
	// relayed). When non-empty, multi-byte node attribution prefers this
	// over observer-region. Falls back to observer-region when unknown.
	nodeHomeRegion := s.computeNodeHomeRegions()
	attributionMethod := "observer"
	if region != "" && len(nodeHomeRegion) > 0 {
		attributionMethod = "repeater"
	}

	allNodes, pm := s.getCachedNodesAndPM()

	// Build pubkey→role map for filtering by node type.
	nodeRoleByPK := make(map[string]string, len(allNodes))
	for _, n := range allNodes {
		nodeRoleByPK[n.PublicKey] = n.Role
	}

	distribution := map[string]int{"1": 0, "2": 0, "3": 0}
	byHour := map[string]map[string]int{}
	byNode := map[string]map[string]interface{}{}
	uniqueHops := map[string]map[string]interface{}{}
	total := 0

	for _, tx := range s.packets {
		if tx.RawHex == "" {
			continue
		}

		// Parse header and path byte
		if len(tx.RawHex) < 4 {
			continue
		}
		header, err := strconv.ParseUint(tx.RawHex[:2], 16, 8)
		if err != nil {
			continue
		}
		routeType := header & 0x03
		pathByteIdx := 1
		if routeType == 0 || routeType == 3 {
			pathByteIdx = 5
		}
		hexStart := pathByteIdx * 2
		hexEnd := hexStart + 2
		if hexEnd > len(tx.RawHex) {
			continue
		}
		actualPathByte, err := strconv.ParseUint(tx.RawHex[hexStart:hexEnd], 16, 8)
		if err != nil {
			continue
		}

		hashSize := int((actualPathByte>>6)&0x3) + 1
		if hashSize > 3 {
			continue
		}

		// #804: pre-extract originator pubkey for ADVERT packets so we can
		// (a) relax observer-region filter when the originator's HOME region
		//     matches the requested region (a flood relay heard outside the
		//     home region must still attribute to the home), and
		// (b) reuse the parsed values below without re-parsing.
		var advertPK, advertName string
		var advertParsed bool
		if tx.PayloadType != nil && *tx.PayloadType == PayloadADVERT && tx.DecodedJSON != "" {
			if d := tx.ParsedDecoded(); d != nil {
				if v, ok := d["pubKey"].(string); ok {
					advertPK = v
				} else if v, ok := d["public_key"].(string); ok {
					advertPK = v
				}
				if n, ok := d["name"].(string); ok {
					advertName = n
				}
				advertParsed = advertPK != ""
			}
		}

		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			// #804: allow ADVERTs from a node whose HOME region matches the
			// requested region even if no observer in that region heard this
			// particular packet (e.g. flood relay heard only by an out-of-
			// region observer). Conservative: only ADVERTs (the source is
			// known by pubkey) and only when home is established.
			if !match && advertParsed {
				if home, ok := nodeHomeRegion[advertPK]; ok && iataMatchesRegion(home, region) {
					match = true
				}
			}
			if !match {
				continue
			}
		}

		// Area filter: skip ADVERT packets whose originator is not in the area.
		if areaNodes != nil && advertParsed && !areaNodes[advertPK] {
			continue
		}

		// Track originator from advert packets (including zero-hop adverts,
		// keyed by pubKey so same-name nodes don't merge).
		if advertParsed {
			pk := advertPK
			name := advertName
			if name == "" {
				if len(pk) >= 8 {
					name = pk[:8]
				} else {
					name = pk
				}
			}
			// Skip zero-hop direct adverts whose path byte is entirely zero —
			// there the size bits were wiped by the sender and say nothing.
			// A non-zero byte with a zero hop count (0x40 / 0x80) is a
			// deliberate size declaration; keep it. Same rule as
			// computeNodeHashSizeInfo. Skipped packets still count and still
			// update lastSeen.
			isUndeclaredZeroHop := (routeType == uint64(RouteDirect) || routeType == uint64(RouteTransportDirect)) && actualPathByte == 0x00
			if byNode[pk] == nil {
				role := nodeRoleByPK[pk] // empty if unknown
				initHS := hashSize
				if isUndeclaredZeroHop {
					initHS = 0
				}
				byNode[pk] = map[string]interface{}{
					"hashSize": initHS, "packets": 0,
					"lastSeen": tx.FirstSeen, "name": name,
					"role": role,
				}
			}
			byNode[pk]["packets"] = byNode[pk]["packets"].(int) + 1
			if !isUndeclaredZeroHop {
				byNode[pk]["hashSize"] = hashSize
			}
			byNode[pk]["lastSeen"] = tx.FirstSeen
		}

		// Distribution/hourly/uniqueHops only for packets with relay hops
		hops := txGetParsedPath(tx)
		if len(hops) == 0 {
			continue
		}
		total++

		sizeKey := strconv.Itoa(hashSize)
		distribution[sizeKey]++

		// Hourly buckets
		if len(tx.FirstSeen) >= 13 {
			hour := tx.FirstSeen[:13]
			if byHour[hour] == nil {
				byHour[hour] = map[string]int{"1": 0, "2": 0, "3": 0}
			}
			byHour[hour][sizeKey]++
		}

		// Track unique hops with their sizes
		for _, hop := range hops {
			if uniqueHops[hop] == nil {
				hopLower := strings.ToLower(hop)
				candidates := pm.m[hopLower]
				var matchName, matchPk interface{}
				if len(candidates) > 0 {
					matchName = candidates[0].Name
					matchPk = candidates[0].PublicKey
				}
				uniqueHops[hop] = map[string]interface{}{
					"size": (len(hop) + 1) / 2, "count": 0,
					"name": matchName, "pubkey": matchPk,
				}
			}
			uniqueHops[hop]["count"] = uniqueHops[hop]["count"].(int) + 1
		}
	}

	// Sort hourly data
	hourKeys := make([]string, 0, len(byHour))
	for k := range byHour {
		hourKeys = append(hourKeys, k)
	}
	sort.Strings(hourKeys)
	hourly := make([]map[string]interface{}, 0, len(hourKeys))
	for _, hour := range hourKeys {
		sizes := byHour[hour]
		hourly = append(hourly, map[string]interface{}{
			"hour": hour, "1": sizes["1"], "2": sizes["2"], "3": sizes["3"],
		})
	}

	// Top hops by frequency
	type hopEntry struct {
		hex  string
		data map[string]interface{}
	}
	hopList := make([]hopEntry, 0, len(uniqueHops))
	for hex, data := range uniqueHops {
		hopList = append(hopList, hopEntry{hex, data})
	}
	sort.Slice(hopList, func(i, j int) bool {
		return hopList[i].data["count"].(int) > hopList[j].data["count"].(int)
	})
	topHops := make([]map[string]interface{}, 0)
	for i, e := range hopList {
		if i >= 50 {
			break
		}
		topHops = append(topHops, map[string]interface{}{
			"hex": e.hex, "size": e.data["size"], "count": e.data["count"],
			"name": e.data["name"], "pubkey": e.data["pubkey"],
		})
	}

	// Multi-byte nodes
	multiByteNodes := make([]map[string]interface{}, 0)
	for pk, data := range byNode {
		// #804: when a region filter is active, prefer the repeater's HOME
		// region over the observer that happened to relay it. Falls back to
		// the (already-applied) observer-region filter when the node's home
		// region is unknown.
		if region != "" {
			if home, ok := nodeHomeRegion[pk]; ok && !iataMatchesRegion(home, region) {
				continue
			}
		}
		if data["hashSize"].(int) > 1 {
			multiByteNodes = append(multiByteNodes, map[string]interface{}{
				"name": data["name"], "hashSize": data["hashSize"],
				"packets": data["packets"], "lastSeen": data["lastSeen"],
				"pubkey": pk, "role": data["role"],
			})
		}
	}
	sort.Slice(multiByteNodes, func(i, j int) bool {
		return multiByteNodes[i]["packets"].(int) > multiByteNodes[j]["packets"].(int)
	})

	// Distribution by repeaters: count unique REPEATER nodes per hash size
	distributionByRepeaters := map[string]int{"1": 0, "2": 0, "3": 0}
	for pk, data := range byNode {
		role, _ := data["role"].(string)
		if !strings.Contains(strings.ToLower(role), "repeater") {
			continue
		}
		// #804: same repeater-region preference as multiByteNodes.
		if region != "" {
			if home, ok := nodeHomeRegion[pk]; ok && !iataMatchesRegion(home, region) {
				continue
			}
		}
		hs := data["hashSize"].(int)
		key := strconv.Itoa(hs)
		distributionByRepeaters[key]++
	}

	return map[string]interface{}{
		"total":                   total,
		"distribution":            distribution,
		"distributionByRepeaters": distributionByRepeaters,
		"hourly":                  hourly,
		"topHops":                 topHops,
		"multiByteNodes":          multiByteNodes,
		"attributionMethod":       attributionMethod,
	}
}

// hashSizeNodeInfo holds per-node hash size tracking data.
type hashSizeNodeInfo struct {
	HashSize     int
	AllSizes     map[int]bool
	Seq          []int
	Inconsistent bool
}

// --- Hash Collision Analytics ---

// GetAnalyticsHashCollisions returns pre-computed hash collision analysis.
// This moves the O(n²) distance computation from the frontend to the server.
func (s *PacketStore) GetAnalyticsHashCollisions(region, area string) map[string]interface{} {
	if region == "" && area == "" {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompHashCollisions
		s.analyticsRecomputerMu.RUnlock()
		if rc != nil {
			if v := rc.Load(); v != nil {
				if m, ok := v.(map[string]interface{}); ok {
					s.cacheMu.Lock()
					s.cacheHits++
					s.cacheMu.Unlock()
					return m
				}
			}
		}
	}
	cacheKey := region + "|" + area
	s.cacheMu.Lock()
	if cached, ok := s.collisionCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeHashCollisions(region, area)

	s.cacheMu.Lock()
	s.collisionCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.collisionCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

// collisionNode is a lightweight node representation for collision analysis.
type collisionNode struct {
	PublicKey            string  `json:"public_key"`
	Name                 string  `json:"name"`
	Role                 string  `json:"role"`
	Lat                  float64 `json:"lat"`
	Lon                  float64 `json:"lon"`
	HashSize             int     `json:"hash_size"`
	HashSizeInconsistent bool    `json:"hash_size_inconsistent"`
	HashSizesSeen        []int   `json:"hash_sizes_seen,omitempty"`
}

// collisionEntry represents a prefix collision with pre-computed distances.
type collisionEntry struct {
	Prefix         string          `json:"prefix"`
	ByteSize       int             `json:"byte_size"`
	Appearances    int             `json:"appearances"`
	Nodes          []collisionNode `json:"nodes"`
	MaxDistKm      float64         `json:"max_dist_km"`
	Classification string          `json:"classification"`
	WithCoords     int             `json:"with_coords"`
}

// prefixCellInfo holds per-prefix-cell data for the matrix view.
type prefixCellInfo struct {
	Nodes []collisionNode `json:"nodes"`
}

// twoByteCellInfo holds per-first-byte-group data for 2-byte matrix.
type twoByteCellInfo struct {
	GroupNodes     []collisionNode            `json:"group_nodes"`
	TwoByteMap     map[string][]collisionNode `json:"two_byte_map"`
	MaxCollision   int                        `json:"max_collision"`
	CollisionCount int                        `json:"collision_count"`
}

func (s *PacketStore) computeHashCollisions(region, area string) map[string]interface{} {
	// Get all nodes from DB
	nodes := s.getAllNodes()
	hashInfo := s.GetNodeHashSizeInfo()

	// If region is specified, filter to only nodes seen by regional observers
	if region != "" {
		regionObs := s.resolveRegionObservers(region)
		if regionObs != nil {
			s.mu.RLock()
			regionNodePKs := make(map[string]bool)
			for _, tx := range s.packets {
				match := false
				for _, obs := range tx.Observations {
					if regionObs[obs.ObserverID] {
						match = true
						break
					}
				}
				if !match {
					continue
				}
				// Collect node public keys from advert packets
				if tx.DecodedJSON != "" {
					if d := tx.ParsedDecoded(); d != nil {
						if pk, ok := d["pubKey"].(string); ok && pk != "" {
							regionNodePKs[pk] = true
						}
						if pk, ok := d["public_key"].(string); ok && pk != "" {
							regionNodePKs[pk] = true
						}
					}
				}
				// Include observers themselves as nodes in the region
				for _, obs := range tx.Observations {
					if obs.ObserverID != "" {
						regionNodePKs[obs.ObserverID] = true
					}
				}
			}
			s.mu.RUnlock()

			// Filter nodes to only those seen in the region
			filtered := make([]nodeInfo, 0, len(regionNodePKs))
			for _, n := range nodes {
				if regionNodePKs[n.PublicKey] {
					filtered = append(filtered, n)
				}
			}
			nodes = filtered
		}
	}

	// If area is specified, filter to only nodes in the area
	if area != "" {
		areaNodes := s.resolveAreaNodes(area)
		if areaNodes != nil {
			filtered := make([]nodeInfo, 0, len(nodes))
			for _, n := range nodes {
				if areaNodes[n.PublicKey] {
					filtered = append(filtered, n)
				}
			}
			nodes = filtered
		}
	}

	// Build collision nodes with hash info
	var allCNodes []collisionNode
	for _, n := range nodes {
		cn := collisionNode{
			PublicKey: n.PublicKey,
			Name:      n.Name,
			Role:      n.Role,
			Lat:       n.Lat,
			Lon:       n.Lon,
		}
		if info, ok := hashInfo[n.PublicKey]; ok && info != nil {
			cn.HashSize = info.HashSize
			cn.HashSizeInconsistent = info.Inconsistent
			if len(info.AllSizes) > 1 {
				sizes := make([]int, 0, len(info.AllSizes))
				for sz := range info.AllSizes {
					sizes = append(sizes, sz)
				}
				sort.Ints(sizes)
				cn.HashSizesSeen = sizes
			}
		}
		allCNodes = append(allCNodes, cn)
	}

	// Inconsistent nodes
	var inconsistentNodes []collisionNode
	for _, cn := range allCNodes {
		if cn.HashSizeInconsistent && (cn.Role == "repeater" || cn.Role == "room_server") {
			inconsistentNodes = append(inconsistentNodes, cn)
		}
	}
	if inconsistentNodes == nil {
		inconsistentNodes = make([]collisionNode, 0)
	}

	// Compute collisions for each byte size (1, 2, 3)
	collisionsBySize := make(map[string]interface{})
	for _, bytes := range []int{1, 2, 3} {
		// Filter nodes relevant to this byte size.
		// - Exclude hash_size==0 nodes: no adverts seen, so actual hash
		//   size is unknown. Including them in every bucket inflates
		//   collision counts.
		// - Exclude companions: they are mobile/temporary and don't form
		//   the mesh backbone, so collisions with them aren't meaningful.
		// (Fixes #441)
		var nodesForByte []collisionNode
		for _, cn := range allCNodes {
			if cn.HashSize == bytes && cn.Role == "repeater" {
				nodesForByte = append(nodesForByte, cn)
			}
		}

		// Build prefix map
		prefixMap := make(map[string][]collisionNode)
		for _, cn := range nodesForByte {
			if len(cn.PublicKey) < bytes*2 {
				continue
			}
			prefix := strings.ToUpper(cn.PublicKey[:bytes*2])
			prefixMap[prefix] = append(prefixMap[prefix], cn)
		}

		// Compute collisions with pairwise distances
		var collisions []collisionEntry
		for prefix, pnodes := range prefixMap {
			if len(pnodes) <= 1 {
				continue
			}
			// Pairwise distance
			var withCoords []collisionNode
			for _, cn := range pnodes {
				if cn.Lat != 0 || cn.Lon != 0 {
					withCoords = append(withCoords, cn)
				}
			}
			var maxDistKm float64
			classification := "unknown"
			if len(withCoords) >= 2 {
				for i := 0; i < len(withCoords); i++ {
					for j := i + 1; j < len(withCoords); j++ {
						d := haversineKm(withCoords[i].Lat, withCoords[i].Lon, withCoords[j].Lat, withCoords[j].Lon)
						if d > maxDistKm {
							maxDistKm = d
						}
					}
				}
				if maxDistKm < 50 {
					classification = "local"
				} else if maxDistKm < 200 {
					classification = "regional"
				} else {
					classification = "distant"
				}
			} else {
				classification = "incomplete"
			}
			collisions = append(collisions, collisionEntry{
				Prefix:         prefix,
				ByteSize:       bytes,
				Appearances:    len(pnodes),
				Nodes:          pnodes,
				MaxDistKm:      maxDistKm,
				Classification: classification,
				WithCoords:     len(withCoords),
			})
		}
		if collisions == nil {
			collisions = make([]collisionEntry, 0)
		}

		// Sort: local first, then regional, distant, incomplete
		classOrder := map[string]int{"local": 0, "regional": 1, "distant": 2, "incomplete": 3, "unknown": 4}
		sort.Slice(collisions, func(i, j int) bool {
			oi, oj := classOrder[collisions[i].Classification], classOrder[collisions[j].Classification]
			if oi != oj {
				return oi < oj
			}
			return collisions[i].Appearances > collisions[j].Appearances
		})

		// Stats
		nodeCount := len(nodesForByte)
		usingThisSize := 0
		for _, cn := range allCNodes {
			if cn.HashSize == bytes {
				usingThisSize++
			}
		}
		uniquePrefixes := len(prefixMap)
		collisionCount := len(collisions)
		var spaceSize int
		switch bytes {
		case 1:
			spaceSize = 256
		case 2:
			spaceSize = 65536
		case 3:
			spaceSize = 16777216
		}
		pctUsed := 0.0
		if spaceSize > 0 {
			pctUsed = float64(uniquePrefixes) / float64(spaceSize) * 100
		}

		// For 1-byte and 2-byte, include the full prefix cell data for matrix rendering
		var oneByteCells map[string][]collisionNode
		var twoByteCells map[string]*twoByteCellInfo
		if bytes == 1 {
			oneByteCells = make(map[string][]collisionNode)
			for i := 0; i < 256; i++ {
				hex := strings.ToUpper(fmt.Sprintf("%02x", i))
				oneByteCells[hex] = prefixMap[hex]
				if oneByteCells[hex] == nil {
					oneByteCells[hex] = make([]collisionNode, 0)
				}
			}
			// Fix #1218: a repeater configured for a 2- or 3-byte hash still
			// occupies its first byte in the 1-byte hash space — any packet
			// routed by 1-byte path-matching collides on that first byte
			// regardless of the configured prefix size. Project all repeater
			// hashes to their first byte so the 1-byte view reflects real
			// conflicts in the 1-byte hash space.
			for _, cn := range allCNodes {
				if cn.Role != "repeater" {
					continue
				}
				if cn.HashSize != 2 && cn.HashSize != 3 {
					continue
				}
				if len(cn.PublicKey) < 2 {
					continue
				}
				hex := strings.ToUpper(cn.PublicKey[:2])
				if _, ok := oneByteCells[hex]; !ok {
					continue
				}
				oneByteCells[hex] = append(oneByteCells[hex], cn)
			}
		} else if bytes == 2 {
			twoByteCells = make(map[string]*twoByteCellInfo)
			for i := 0; i < 256; i++ {
				hex := strings.ToUpper(fmt.Sprintf("%02x", i))
				cell := &twoByteCellInfo{
					GroupNodes: make([]collisionNode, 0),
					TwoByteMap: make(map[string][]collisionNode),
				}
				twoByteCells[hex] = cell
			}
			for _, cn := range nodesForByte {
				if len(cn.PublicKey) < 4 {
					continue
				}
				firstHex := strings.ToUpper(cn.PublicKey[:2])
				twoHex := strings.ToUpper(cn.PublicKey[:4])
				cell := twoByteCells[firstHex]
				if cell == nil {
					continue
				}
				cell.GroupNodes = append(cell.GroupNodes, cn)
				cell.TwoByteMap[twoHex] = append(cell.TwoByteMap[twoHex], cn)
			}
			for _, cell := range twoByteCells {
				for _, ns := range cell.TwoByteMap {
					if len(ns) > 1 {
						cell.CollisionCount++
						if len(ns) > cell.MaxCollision {
							cell.MaxCollision = len(ns)
						}
					}
				}
			}
		}

		sizeData := map[string]interface{}{
			"stats": map[string]interface{}{
				"total_nodes":     len(allCNodes),
				"nodes_for_byte":  nodeCount,
				"using_this_size": usingThisSize,
				"unique_prefixes": uniquePrefixes,
				"collision_count": collisionCount,
				"space_size":      spaceSize,
				"pct_used":        pctUsed,
			},
			"collisions": collisions,
		}
		if oneByteCells != nil {
			sizeData["one_byte_cells"] = oneByteCells
		}
		if twoByteCells != nil {
			sizeData["two_byte_cells"] = twoByteCells
		}
		collisionsBySize[strconv.Itoa(bytes)] = sizeData
	}

	return map[string]interface{}{
		"inconsistent_nodes": inconsistentNodes,
		"by_size":            collisionsBySize,
	}
}

// GetNodeHashSizeInfo returns cached per-node hash size data, recomputing at most every 15s.
func (s *PacketStore) GetNodeHashSizeInfo() map[string]*hashSizeNodeInfo {
	const ttl = 15 * time.Second
	s.hashSizeInfoMu.Lock()
	if s.hashSizeInfoCache != nil && time.Since(s.hashSizeInfoAt) < ttl {
		cached := s.hashSizeInfoCache
		s.hashSizeInfoMu.Unlock()
		return cached
	}
	s.hashSizeInfoMu.Unlock()
	result := s.computeNodeHashSizeInfo()
	s.hashSizeInfoMu.Lock()
	s.hashSizeInfoCache = result
	s.hashSizeInfoAt = time.Now()
	s.hashSizeInfoMu.Unlock()
	return result
}

// computeNodeHashSizeInfo scans advert packets to compute per-node hash size data.
// Only adverts from the last 7 days are considered so that legitimate config
// changes during testing don't create permanent false positives.
func (s *PacketStore) computeNodeHashSizeInfo() map[string]*hashSizeNodeInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect (timestamp, hashSize) per pubkey so we can order adverts
	// chronologically below. byPayloadType iteration is insertion order, which
	// is not guaranteed to be chronological (out-of-order MQTT ingest, chunked
	// cold-load), so we must sort by timestamp before reasoning about "latest"
	// or "most recent" adverts. We parse FirstSeen rather than string-compare it
	// so ordering is robust to timestamp-format differences (RFC3339 with or
	// without fractional seconds); an unparseable/empty FirstSeen sorts oldest
	// so it can never masquerade as the latest advert.
	type hsEntry struct {
		ts   time.Time
		size int
	}
	entries := make(map[string][]hsEntry)

	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")

	adverts := s.byPayloadType[4]
	for _, tx := range adverts {
		// Skip adverts older than 7 days to avoid false positives from
		// historical config changes during testing.
		if tx.FirstSeen != "" && tx.FirstSeen < cutoff {
			continue
		}
		if tx.RawHex == "" || tx.DecodedJSON == "" {
			continue
		}
		if len(tx.RawHex) < 4 {
			continue
		}
		header, err := strconv.ParseUint(tx.RawHex[:2], 16, 8)
		if err != nil {
			continue
		}
		routeType := int(header & 0x03)
		// Transport routes (0, 3) have 4 transport code bytes before the path
		// byte, so the path byte is at offset 5 instead of 1.
		pbOffset := 1
		if routeType == RouteTransportFlood || routeType == RouteTransportDirect {
			pbOffset = 5
		}
		if len(tx.RawHex) < (pbOffset+1)*2 {
			continue
		}
		pathByte, err := strconv.ParseUint(tx.RawHex[pbOffset*2:pbOffset*2+2], 16, 8)
		if err != nil {
			continue
		}
		// Direct zero-hop adverts carry no path, so the hop count is 0. Whether
		// the SIZE bits are meaningful depends on the sender: firmware that
		// predates meshcore-dev/MeshCore#3293 does `path_len = 0`, wiping the
		// whole byte including the two size bits, so 0x00 says nothing about
		// the node's path.hash.mode. A sender that writes the size through
		// setPathHashSizeAndCount() emits 0x40 / 0x80 with a zero hop count —
		// that is a deliberate declaration and the only way those bits can be
		// non-zero here. Skip on the byte's content, not on the route type.
		if (routeType == RouteDirect || routeType == RouteTransportDirect) && pathByte == 0x00 {
			continue
		}
		hs := int((pathByte>>6)&0x3) + 1

		d := tx.ParsedDecoded()
		if d == nil {
			continue
		}
		pk := ""
		if v, ok := d["pubKey"].(string); ok {
			pk = v
		} else if v, ok := d["public_key"].(string); ok {
			pk = v
		}
		if pk == "" {
			continue
		}

		// time.Parse(time.RFC3339, ...) accepts both the ingestor's no-fraction
		// form ("...05Z") and a fractional form ("...05.000Z"). Zero time on
		// parse failure → sorts oldest.
		ts, _ := time.Parse(time.RFC3339, tx.FirstSeen)
		entries[pk] = append(entries[pk], hsEntry{ts: ts, size: hs})
	}

	info := make(map[string]*hashSizeNodeInfo)
	for pk, es := range entries {
		// Order adverts chronologically. Stable sort so that adverts with equal
		// (or unparseable) timestamps keep insertion order.
		sort.SliceStable(es, func(i, j int) bool { return es[i].ts.Before(es[j].ts) })

		ni := &hashSizeNodeInfo{AllSizes: make(map[int]bool), Seq: make([]int, len(es))}
		for i, e := range es {
			ni.Seq[i] = e.size
			ni.AllSizes[e.size] = true
		}
		info[pk] = ni

		// Use the most recent advert's hash size (last in chronological order).
		// The upstream firmware bug causing stale path bytes in flood adverts
		// was fixed (meshcore-dev/MeshCore#2154).
		ni.HashSize = ni.Seq[len(ni.Seq)-1]

		// Flip-flop (inconsistent) flag: need a minimum number of observations,
		// >= 2 unique sizes, and >= 2 transitions in the sequence.
		if len(ni.Seq) < hashSizeMinObservations || len(ni.AllSizes) < 2 {
			continue
		}
		transitions := 0
		for i := 1; i < len(ni.Seq); i++ {
			if ni.Seq[i] != ni.Seq[i-1] {
				transitions++
			}
		}
		if transitions < 2 {
			continue
		}
		// Recency decay (issue #1726): if the most recent adverts all agree on
		// a single size, the node has settled on its current hash mode (e.g. an
		// operator flipped path.hash.mode mid-flight, or a lone stale 1-byte
		// advert sits earlier in the 7-day window). Don't keep reporting "varies"
		// over older history once the node is consistent again. A node whose
		// recent adverts still disagree remains flagged.
		//
		// Known limitation: a node that flaps slowly (long stable stretches
		// between toggles) is not flagged during a stable stretch. This is
		// intentional — "varies" describes the node's *current* state — and the
		// full history stays visible via hash_sizes_seen / AllSizes.
		if recentAdvertsAgree(ni.Seq, hashSizeRecentAgreeCount) {
			continue
		}
		ni.Inconsistent = true
	}

	return info
}

// hashSizeMinObservations is the minimum number of non-zero-hop adverts in the
// window before a node is eligible to be flagged as flip-flopping at all.
const hashSizeMinObservations = 3

// hashSizeRecentAgreeCount is how many of the most recent non-zero-hop adverts
// must share a single hash size for a node to be considered "settled", clearing
// its flip-flop ("varies") flag.
const hashSizeRecentAgreeCount = 3

// recentAdvertsAgree reports whether the last n entries of a chronologically
// ordered hash-size sequence are all equal.
func recentAdvertsAgree(seq []int, n int) bool {
	if len(seq) < n {
		return false
	}
	last := seq[len(seq)-1]
	for i := len(seq) - n; i < len(seq)-1; i++ {
		if seq[i] != last {
			return false
		}
	}
	return true
}

// EnrichNodeWithHashSize populates hash_size, hash_size_inconsistent, and
// hash_sizes_seen on a node map using precomputed hash size info.
func EnrichNodeWithHashSize(node map[string]interface{}, info *hashSizeNodeInfo) {
	if info == nil {
		return
	}
	node["hash_size"] = info.HashSize
	node["hash_size_inconsistent"] = info.Inconsistent
	if len(info.AllSizes) > 1 {
		sizes := make([]int, 0, len(info.AllSizes))
		for s := range info.AllSizes {
			sizes = append(sizes, s)
		}
		sort.Ints(sizes)
		node["hash_sizes_seen"] = sizes
	}
}

// EnrichNodeWithMultiByte adds multi-byte capability fields to a node map.
func EnrichNodeWithMultiByte(node map[string]interface{}, entry *MultiByteCapEntry) {
	if entry == nil {
		return
	}
	node["multi_byte_status"] = entry.Status
	node["multi_byte_evidence"] = entry.Evidence
	node["multi_byte_max_hash_size"] = entry.MaxHashSize
}

// GetMultibyteCapFor returns the capability entry for a single pubkey via an O(1) map
// lookup into the snapshot rebuilt by each analytics cycle (and pre-populated from
// the DB on cold start). Returns false when the pubkey has no known capability.
func (s *PacketStore) GetMultibyteCapFor(pk string) (*MultiByteCapEntry, bool) {
	s.cacheMu.RLock()
	e, ok := s.mbCapIndex[pk]
	s.cacheMu.RUnlock()
	if !ok {
		return nil, false
	}
	return &e, true
}

// loadMultibyteCapFromDB pre-populates mbCapSnapshot and mbCapIndex from the nodes
// table so cold starts serve the last-known capability without waiting for the first
// analytics cycle (~15s).
func (s *PacketStore) loadMultibyteCapFromDB() {
	if !s.db.hasMultibyteSupCols {
		return
	}
	rows, err := s.db.conn.Query(
		`SELECT public_key, COALESCE(name,''), COALESCE(role,''), COALESCE(last_seen,''), multibyte_sup, COALESCE(multibyte_evidence,'')
		 FROM nodes WHERE multibyte_sup > 0`)
	if err != nil {
		log.Printf("[multibyte] loadFromDB: %v", err)
		return
	}
	defer rows.Close()

	var entries []MultiByteCapEntry
	for rows.Next() {
		var pk, name, role, lastSeen, evidence string
		var sup int
		if err := rows.Scan(&pk, &name, &role, &lastSeen, &sup, &evidence); err != nil {
			continue
		}
		status := "unknown"
		switch sup {
		case 2:
			status = "confirmed"
		case 1:
			status = "suspected"
		}
		entries = append(entries, MultiByteCapEntry{
			PublicKey: pk,
			Name:      name,
			Role:      role,
			Status:    status,
			Evidence:  evidence,
			LastSeen:  lastSeen,
		})
	}
	if len(entries) == 0 {
		return
	}
	idx := make(map[string]MultiByteCapEntry, len(entries))
	for _, e := range entries {
		idx[e.PublicKey] = e
	}
	s.cacheMu.Lock()
	s.mbCapSnapshot = entries
	s.mbCapIndex = idx
	s.cacheMu.Unlock()
	log.Printf("[multibyte] loaded %d capability entries from DB", len(entries))
}

// publishMultibyteCapSnapshot writes the analytics-cycle output to the
// on-disk handoff (internal/mbcapqueue). The ingestor's
// RunMultibyteCapPersist consumes the file and writes confirmed /
// suspected entries to the DB.
//
// INVARIANT (#1289/#1324): the server is the read path and opens
// SQLite mode=ro. It MUST NOT execute any UPDATE on
// nodes.multibyte_* — see readonly_invariant_test.go. This helper is
// the only side-effect path for capability data leaving the server.
func (s *PacketStore) publishMultibyteCapSnapshot(entries []MultiByteCapEntry) {
	if s.db == nil || s.db.path == "" {
		return
	}
	out := make([]mbcapqueue.Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, mbcapqueue.Entry{
			PublicKey: e.PublicKey,
			Status:    e.Status,
			Evidence:  e.Evidence,
		})
	}
	if err := mbcapqueue.WriteSnapshot(s.db.path, mbcapqueue.Snapshot{Entries: out}); err != nil {
		log.Printf("[multibyte] publish snapshot: %v", err)
	}
}

// --- Multi-Byte Capability Inference ---

// MultiByteCapEntry represents a node's inferred multi-byte capability.
type MultiByteCapEntry struct {
	PublicKey   string `json:"pubkey"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Status      string `json:"status"`   // "confirmed", "suspected", "unknown"
	Evidence    string `json:"evidence"` // "advert", "path", ""
	MaxHashSize int    `json:"maxHashSize"`
	LastSeen    string `json:"lastSeen"`
}

// computeMultiByteCapability determines multi-byte capability for each
// node (repeaters, companions, rooms, sensors) using two methods:
//
//  1. Confirmed: the node has advertised with hash_size >= 2 (from advert
//     path byte). This is 100% reliable because the full public key is
//     received in adverts — no prefix collision ambiguity.
//
//  2. Suspected: the node's prefix appears as a hop in a packet whose path
//     header indicates hash_size >= 2. This is <100% reliable because
//     2-byte prefixes can collide — two different nodes may share the same
//     prefix. If one is confirmed multi-byte and the other is not, the
//     non-confirmed one could be a false positive.
//
//  3. Unknown: node has only been seen with 1-byte adverts and no
//     multi-byte path appearances. Could be pre-1.14 firmware or 1.14+
//     with default (1-byte) settings.
//
// Caller must hold NO locks — this method acquires mu.RLock internally.
func (s *PacketStore) computeMultiByteCapability(adopterHashSizes map[string]int) []MultiByteCapEntry {
	// Get hash size info from adverts (has its own locking)
	hashInfo := s.GetNodeHashSizeInfo()

	// Get all nodes for name/role lookup
	allNodes := s.getAllNodes()
	nodeByPK := make(map[string]nodeInfo, len(allNodes))
	for _, n := range allNodes {
		nodeByPK[n.PublicKey] = n
	}

	// Build set of confirmed multi-byte pubkeys (advert hash_size >= 2)
	confirmed := make(map[string]int) // pubkey → max hash size from adverts
	for pk, info := range hashInfo {
		maxHS := 1
		for sz := range info.AllSizes {
			if sz > maxHS {
				maxHS = sz
			}
		}
		if maxHS >= 2 {
			confirmed[pk] = maxHS
		}
	}

	// Scan path-hop index for suspected multi-byte nodes.
	// For each repeater, check if any packet in byPathHop has that
	// node as a hop with hash_size >= 2 in the path header.
	s.mu.RLock()

	// Build prefix→pubkey mapping for repeaters
	type prefixEntry struct {
		pubkey string
		prefix string
	}
	nodePrefixes := make(map[string][]prefixEntry) // prefix → entries
	for pk := range nodeByPK {
		// Generate 1-byte, 2-byte, 3-byte prefixes
		pkLower := strings.ToLower(pk)
		for byteLen := 1; byteLen <= 3; byteLen++ {
			hexLen := byteLen * 2
			if len(pkLower) >= hexLen {
				pfx := pkLower[:hexLen]
				nodePrefixes[pfx] = append(nodePrefixes[pfx], prefixEntry{pk, pfx})
			}
		}
	}

	suspected := make(map[string]int) // pubkey → max hash size from path appearances
	for pfx, entries := range nodePrefixes {
		txList := s.byPathHop[pfx]
		for _, tx := range txList {
			if tx.RawHex == "" || len(tx.RawHex) < 4 {
				continue
			}
			// Skip TRACE packets (payload_type 8) — they carry hash size in
			// TRACE flags, not the repeater's compile-time PATH_HASH_SIZE.
			// Pre-1.14 repeaters can forward multi-byte TRACEs, creating
			// false positives for "suspected" capability. See #714.
			if tx.PayloadType != nil && *tx.PayloadType == 8 {
				continue
			}
			header, err := strconv.ParseUint(tx.RawHex[:2], 16, 8)
			if err != nil {
				continue
			}
			routeType := header & 0x03
			pathByteIdx := 1
			if routeType == 0 || routeType == 3 {
				pathByteIdx = 5
			}
			hexStart := pathByteIdx * 2
			hexEnd := hexStart + 2
			if hexEnd > len(tx.RawHex) {
				continue
			}
			actualPathByte, err := strconv.ParseUint(tx.RawHex[hexStart:hexEnd], 16, 8)
			if err != nil {
				continue
			}
			hs := int((actualPathByte>>6)&0x3) + 1
			if hs < 2 {
				continue
			}
			// This packet uses multi-byte hashes and contains this prefix as a hop
			for _, e := range entries {
				if hs > suspected[e.pubkey] {
					suspected[e.pubkey] = hs
				}
			}
			break // one match is enough per prefix
		}
	}
	s.mu.RUnlock()

	// Build result for all nodes — fetch last_seen from DB
	dbLastSeen := make(map[string]string)
	rows, err := s.db.conn.Query("SELECT public_key, last_seen FROM nodes")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var pk string
			var ls sql.NullString
			rows.Scan(&pk, &ls)
			if ls.Valid {
				dbLastSeen[pk] = ls.String
			}
		}
	}

	var result []MultiByteCapEntry
	for pk, n := range nodeByPK {
		entry := MultiByteCapEntry{
			PublicKey:   pk,
			Name:        n.Name,
			Role:        n.Role,
			MaxHashSize: 1,
			LastSeen:    dbLastSeen[pk],
		}

		if maxHS, ok := confirmed[pk]; ok {
			entry.Status = "confirmed"
			entry.Evidence = "advert"
			entry.MaxHashSize = maxHS
		} else if maxHS, ok := adopterHashSizes[pk]; ok && maxHS >= 2 {
			// Adopter data (from computeAnalyticsHashSizes) shows hash_size >= 2
			// from advert analysis — this is advert-based evidence, so confirmed.
			entry.Status = "confirmed"
			entry.Evidence = "advert"
			entry.MaxHashSize = maxHS
		} else if maxHS, ok := suspected[pk]; ok {
			entry.Status = "suspected"
			entry.Evidence = "path"
			entry.MaxHashSize = maxHS
		} else {
			entry.Status = "unknown"
		}

		// Check advert hash info for max even if not confirmed multi-byte
		if info, ok := hashInfo[pk]; ok && entry.MaxHashSize == 1 {
			for sz := range info.AllSizes {
				if sz > entry.MaxHashSize {
					entry.MaxHashSize = sz
				}
			}
		}

		result = append(result, entry)
	}

	// Sort: confirmed first, then suspected, then unknown; within each group by name
	statusOrder := map[string]int{"confirmed": 0, "suspected": 1, "unknown": 2}
	sort.Slice(result, func(i, j int) bool {
		oi, oj := statusOrder[result[i].Status], statusOrder[result[j].Status]
		if oi != oj {
			return oi < oj
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})

	return result
}

// --- Bulk Health (in-memory) ---

func (s *PacketStore) GetBulkHealth(limit int, region, area string) []map[string]interface{} {
	var areaNodes map[string]bool
	if area != "" {
		areaNodes = s.resolveAreaNodes(area)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Region filtering
	var regionNodeKeys map[string]bool
	if region != "" {
		regionObs := s.resolveRegionObservers(region)
		if regionObs != nil {
			regionalHashes := make(map[string]bool)
			for obsID := range regionObs {
				obsList := s.byObserver[obsID]
				for _, o := range obsList {
					tx := s.byTxID[o.TransmissionID]
					if tx != nil {
						regionalHashes[tx.Hash] = true
					}
				}
			}
			regionNodeKeys = make(map[string]bool)
			for pk, hashes := range s.nodeHashes {
				for h := range hashes {
					if regionalHashes[h] {
						regionNodeKeys[pk] = true
						break
					}
				}
			}
		}
	}

	// Get nodes from DB — fetch more when filtering so we don't under-fill after exclusions
	queryLimit := limit
	if regionNodeKeys != nil || areaNodes != nil {
		queryLimit = 10000
	}
	rows, err := s.db.conn.Query("SELECT public_key, name, role, lat, lon FROM nodes ORDER BY last_seen DESC LIMIT ?", queryLimit)
	if err != nil {
		return []map[string]interface{}{}
	}
	defer rows.Close()

	type dbNode struct {
		pk, name, role string
		lat, lon       interface{}
	}
	var nodes []dbNode
	for rows.Next() {
		var pk string
		var name, role sql.NullString
		var lat, lon sql.NullFloat64
		rows.Scan(&pk, &name, &role, &lat, &lon)
		if regionNodeKeys != nil && !regionNodeKeys[pk] {
			continue
		}
		if areaNodes != nil && !areaNodes[pk] {
			continue
		}
		nodes = append(nodes, dbNode{
			pk: pk, name: nullStrVal(name), role: nullStrVal(role),
			lat: nullFloat(lat), lon: nullFloat(lon),
		})
		if regionNodeKeys == nil && areaNodes == nil && len(nodes) >= limit {
			break
		}
	}
	// Only cap to limit in the global (no-filter) case; area/region returns full filtered set
	if regionNodeKeys != nil && areaNodes == nil && len(nodes) > limit {
		nodes = nodes[:limit]
	}

	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)
	results := make([]map[string]interface{}, 0, len(nodes))

	for _, n := range nodes {
		packets := s.byNode[n.pk]
		var packetsToday int
		var snrSum float64
		var snrCount int
		var lastHeard string
		observerStats := map[string]*struct {
			name                       string
			snrSum, rssiSum            float64
			snrCount, rssiCount, count int
		}{}
		totalObservations := 0

		for _, pkt := range packets {
			totalObservations += pkt.ObservationCount
			if totalObservations == 0 {
				totalObservations = 1
			}
			if pkt.FirstSeen > todayStart {
				packetsToday++
			}
			if pkt.SNR != nil {
				snrSum += *pkt.SNR
				snrCount++
			}
			if lastHeard == "" || pkt.FirstSeen > lastHeard {
				lastHeard = pkt.FirstSeen
			}
			obsID := pkt.ObserverID
			if obsID != "" {
				obs := observerStats[obsID]
				if obs == nil {
					obs = &struct {
						name                       string
						snrSum, rssiSum            float64
						snrCount, rssiCount, count int
					}{name: pkt.ObserverName}
					observerStats[obsID] = obs
				}
				obs.count++
				if pkt.SNR != nil {
					obs.snrSum += *pkt.SNR
					obs.snrCount++
				}
				if pkt.RSSI != nil {
					obs.rssiSum += *pkt.RSSI
					obs.rssiCount++
				}
			}
		}

		observerRows := make([]map[string]interface{}, 0)
		for id, o := range observerStats {
			var avgSnr, avgRssi interface{}
			if o.snrCount > 0 {
				avgSnr = o.snrSum / float64(o.snrCount)
			}
			if o.rssiCount > 0 {
				avgRssi = o.rssiSum / float64(o.rssiCount)
			}
			observerRows = append(observerRows, map[string]interface{}{
				"observer_id": id, "observer_name": o.name,
				"avgSnr": avgSnr, "avgRssi": avgRssi, "packetCount": o.count,
			})
		}
		sort.Slice(observerRows, func(i, j int) bool {
			return observerRows[i]["packetCount"].(int) > observerRows[j]["packetCount"].(int)
		})

		var avgSnr interface{}
		if snrCount > 0 {
			avgSnr = snrSum / float64(snrCount)
		}
		var lhVal interface{}
		if lastHeard != "" {
			lhVal = lastHeard
		}

		results = append(results, map[string]interface{}{
			"public_key": n.pk,
			"name":       nilIfEmpty(n.name),
			"role":       nilIfEmpty(n.role),
			"lat":        n.lat,
			"lon":        n.lon,
			"stats": map[string]interface{}{
				"totalTransmissions": len(packets),
				"totalObservations":  totalObservations,
				"totalPackets":       len(packets),
				"packetsToday":       packetsToday,
				"avgSnr":             avgSnr,
				"lastHeard":          lhVal,
			},
			"observers": observerRows,
		})
	}

	return results
}

// --- Subpaths Analytics ---

// GetNodeHealth returns health info for a single node using in-memory data.
func (s *PacketStore) GetNodeHealth(pubkey string) (map[string]interface{}, error) {
	// Fetch node info from DB (fast single-row lookup)
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil {
		return nil, err
	}
	// If the node isn't in the DB (e.g. companion that never advertised),
	// check if we have any packet data for it. If so, build a partial response.
	if node == nil {
		s.mu.RLock()
		hasPackets := len(s.byNode[pubkey]) > 0
		s.mu.RUnlock()
		if !hasPackets {
			return nil, nil
		}
		// Build a synthetic node stub so the rest of the function works
		node = map[string]interface{}{
			"public_key": pubkey,
			"name":       "Unknown",
			"role":       "unknown",
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	packets := s.byNode[pubkey]
	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)

	var packetsToday int
	var snrSum float64
	var snrCount int
	var totalHops, hopCount int
	var lastHeard string
	totalObservations := 0

	observerStats := map[string]*struct {
		name                       string
		snrSum, rssiSum            float64
		snrCount, rssiCount, count int
	}{}

	for _, pkt := range packets {
		totalObservations += pkt.ObservationCount
		if pkt.FirstSeen > todayStart {
			packetsToday++
		}
		if pkt.SNR != nil {
			snrSum += *pkt.SNR
			snrCount++
		}
		if lastHeard == "" || pkt.FirstSeen > lastHeard {
			lastHeard = pkt.FirstSeen
		}
		// Hop counting
		hops := txGetParsedPath(pkt)
		if len(hops) > 0 {
			totalHops += len(hops)
			hopCount++
		}
		// Observer stats
		obsID := pkt.ObserverID
		if obsID != "" {
			obs := observerStats[obsID]
			if obs == nil {
				obs = &struct {
					name                       string
					snrSum, rssiSum            float64
					snrCount, rssiCount, count int
				}{name: pkt.ObserverName}
				observerStats[obsID] = obs
			}
			obs.count++
			if pkt.SNR != nil {
				obs.snrSum += *pkt.SNR
				obs.snrCount++
			}
			if pkt.RSSI != nil {
				obs.rssiSum += *pkt.RSSI
				obs.rssiCount++
			}
		}
	}

	observerRows := make([]map[string]interface{}, 0)
	// Issue #1290: surface listener/repeater hint on node detail by
	// looking up can_relay for each observer that heard this node.
	// One-shot fetch of the non-relay set keeps this O(observers) on
	// rare events; nil on error degrades to "neither badge" client-side.
	// Issue #1290: keep this set lowercase to match the convention used
	// by the resolver (cmd/server/store.go pm.nonRelay) and by
	// GetNonRelayObserverPubkeys (which already returns LOWER(id)).
	// Two case conventions on the same upstream string would be a
	// latent regression waiting for any refactor that touches the
	// observer-id normalization layer.
	nonRelaySet := map[string]struct{}{}
	// PR #1624 MAJOR-2: tri-state badge needs to distinguish "confirmed
	// repeater" (seen=1, can_relay=1) from "unknown" (seen=0). Build
	// the set of observers we have NO repeat-field record for so the
	// badge is nil/omitted for them — matches nodes.js:679 tri-state.
	seenSet := map[string]struct{}{}
	if s.db != nil && s.db.conn != nil {
		if pks, err := s.db.GetNonRelayObserverPubkeys(); err == nil {
			for _, pk := range pks {
				nonRelaySet[strings.ToLower(pk)] = struct{}{}
			}
		}
		if pks, err := s.db.GetCanRelaySeenObserverPubkeys(); err == nil {
			for _, pk := range pks {
				seenSet[strings.ToLower(pk)] = struct{}{}
			}
		}
	}
	for id, o := range observerStats {
		var avgSnr, avgRssi interface{}
		if o.snrCount > 0 {
			avgSnr = o.snrSum / float64(o.snrCount)
		}
		if o.rssiCount > 0 {
			avgRssi = o.rssiSum / float64(o.rssiCount)
		}
		idLower := strings.ToLower(id)
		var canRelay interface{} // nil = unknown (no repeat field ever)
		if _, seen := seenSet[idLower]; seen {
			if _, isListener := nonRelaySet[idLower]; isListener {
				canRelay = false
			} else {
				canRelay = true
			}
		}
		observerRows = append(observerRows, map[string]interface{}{
			"observer_id": id, "observer_name": o.name,
			"avgSnr": avgSnr, "avgRssi": avgRssi, "packetCount": o.count,
			"can_relay": canRelay,
		})
	}
	sort.Slice(observerRows, func(i, j int) bool {
		return observerRows[i]["packetCount"].(int) > observerRows[j]["packetCount"].(int)
	})

	var avgSnr interface{}
	if snrCount > 0 {
		avgSnr = snrSum / float64(snrCount)
	}
	avgHops := 0
	if hopCount > 0 {
		avgHops = int(math.Round(float64(totalHops) / float64(hopCount)))
	}
	var lhVal interface{}
	if lastHeard != "" {
		lhVal = lastHeard
	}

	// Recent packets (up to 20, newest first — read from tail of oldest-first slice)
	recentLimit := 20
	if len(packets) < recentLimit {
		recentLimit = len(packets)
	}
	recentPackets := make([]map[string]interface{}, 0, recentLimit)
	for i := len(packets) - 1; i >= len(packets)-recentLimit; i-- {
		p := s.txToMapWithRP(packets[i])
		delete(p, "observations")
		recentPackets = append(recentPackets, p)
	}

	return map[string]interface{}{
		"node":      node,
		"observers": observerRows,
		"stats": map[string]interface{}{
			"totalTransmissions": len(packets),
			"totalObservations":  totalObservations,
			"totalPackets":       len(packets),
			"packetsToday":       packetsToday,
			"avgSnr":             avgSnr,
			"avgHops":            avgHops,
			"lastHeard":          lhVal,
		},
		"recentPackets": recentPackets,
	}, nil
}

// GetNodeAnalytics computes analytics for a single node using in-memory byNode index.
func (s *PacketStore) GetNodeAnalytics(pubkey string, days int) (*NodeAnalyticsResponse, error) {
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		return nil, err
	}

	fromTime := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	fromISO := fromTime.Format(time.RFC3339)
	toISO := time.Now().Format(time.RFC3339)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect packets from byNode index (time-filtered).
	// Raw JSON text search is intentionally avoided: a GRP_TXT packet whose message
	// text contains a node's pubkey is not a packet *for* that node.
	indexed := s.byNode[pubkey]
	var packets []*StoreTx
	for _, p := range indexed {
		if p.FirstSeen > fromISO {
			packets = append(packets, p)
		}
	}

	// Activity timeline (hourly buckets)
	timelineBuckets := map[string]int{}
	for _, p := range packets {
		if len(p.FirstSeen) >= 13 {
			bucket := p.FirstSeen[:13] + ":00:00Z"
			timelineBuckets[bucket]++
		}
	}
	bucketKeys := make([]string, 0, len(timelineBuckets))
	for k := range timelineBuckets {
		bucketKeys = append(bucketKeys, k)
	}
	sort.Strings(bucketKeys)
	activityTimeline := make([]TimeBucket, 0, len(bucketKeys))
	for _, k := range bucketKeys {
		b := k
		activityTimeline = append(activityTimeline, TimeBucket{Bucket: &b, Count: timelineBuckets[k]})
	}

	// SNR trend
	snrTrend := make([]SnrTrendEntry, 0)
	for _, p := range packets {
		if p.SNR != nil {
			snrTrend = append(snrTrend, SnrTrendEntry{
				Timestamp:    p.FirstSeen,
				SNR:          floatPtrOrNil(p.SNR),
				RSSI:         floatPtrOrNil(p.RSSI),
				ObserverID:   strOrNil(p.ObserverID),
				ObserverName: strOrNil(p.ObserverName),
			})
		}
	}

	// Packet type breakdown
	typeBuckets := map[int]int{}
	for _, p := range packets {
		if p.PayloadType != nil {
			typeBuckets[*p.PayloadType]++
		}
	}
	packetTypeBreakdown := make([]PayloadTypeCount, 0, len(typeBuckets))
	for pt, cnt := range typeBuckets {
		packetTypeBreakdown = append(packetTypeBreakdown, PayloadTypeCount{PayloadType: pt, Count: cnt})
	}

	// Observer coverage
	type obsAccum struct {
		name                       string
		snrSum, rssiSum            float64
		snrCount, rssiCount, count int
		first, last                string
	}
	obsMap := map[string]*obsAccum{}
	for _, p := range packets {
		if p.ObserverID == "" {
			continue
		}
		o := obsMap[p.ObserverID]
		if o == nil {
			o = &obsAccum{name: p.ObserverName, first: p.FirstSeen, last: p.FirstSeen}
			obsMap[p.ObserverID] = o
		}
		o.count++
		if p.SNR != nil {
			o.snrSum += *p.SNR
			o.snrCount++
		}
		if p.RSSI != nil {
			o.rssiSum += *p.RSSI
			o.rssiCount++
		}
		if p.FirstSeen < o.first {
			o.first = p.FirstSeen
		}
		if p.FirstSeen > o.last {
			o.last = p.FirstSeen
		}
	}
	observerCoverage := make([]NodeObserverStatsResp, 0, len(obsMap))
	for id, o := range obsMap {
		var avgSnr, avgRssi interface{}
		if o.snrCount > 0 {
			avgSnr = o.snrSum / float64(o.snrCount)
		}
		if o.rssiCount > 0 {
			avgRssi = o.rssiSum / float64(o.rssiCount)
		}
		observerCoverage = append(observerCoverage, NodeObserverStatsResp{
			ObserverID:   id,
			ObserverName: o.name,
			PacketCount:  o.count,
			AvgSnr:       avgSnr,
			AvgRssi:      avgRssi,
			FirstSeen:    o.first,
			LastSeen:     o.last,
		})
	}
	sort.Slice(observerCoverage, func(i, j int) bool {
		return observerCoverage[i].PacketCount > observerCoverage[j].PacketCount
	})

	// Hop distribution
	hopCounts := map[string]int{}
	totalWithPath := 0
	relayedCount := 0
	for _, p := range packets {
		hops := txGetParsedPath(p)
		if len(hops) > 0 {
			key := fmt.Sprintf("%d", len(hops))
			if len(hops) >= 4 {
				key = "4+"
			}
			hopCounts[key]++
			totalWithPath++
			if len(hops) > 1 {
				relayedCount++
			}
		} else {
			hopCounts["0"]++
		}
	}
	hopDistribution := make([]HopDistEntry, 0)
	for _, h := range []string{"0", "1", "2", "3", "4+"} {
		if c, ok := hopCounts[h]; ok {
			hopDistribution = append(hopDistribution, HopDistEntry{Hops: h, Count: c})
		}
	}

	// Peer interactions
	type peerAccum struct {
		key, name   string
		count       int
		lastContact string
	}
	peerMap := map[string]*peerAccum{}
	for _, p := range packets {
		if p.DecodedJSON == "" {
			continue
		}
		decoded := p.ParsedDecoded()
		if decoded == nil {
			continue
		}
		type candidate struct{ key, name string }
		var candidates []candidate
		if sk, ok := decoded["sender_key"].(string); ok && sk != "" && sk != pubkey {
			sn, _ := decoded["sender_name"].(string)
			if sn == "" {
				sn, _ = decoded["sender_short_name"].(string)
			}
			candidates = append(candidates, candidate{sk, sn})
		}
		if rk, ok := decoded["recipient_key"].(string); ok && rk != "" && rk != pubkey {
			rn, _ := decoded["recipient_name"].(string)
			if rn == "" {
				rn, _ = decoded["recipient_short_name"].(string)
			}
			candidates = append(candidates, candidate{rk, rn})
		}
		if pk, ok := decoded["pubkey"].(string); ok && pk != "" && pk != pubkey {
			nm, _ := decoded["name"].(string)
			candidates = append(candidates, candidate{pk, nm})
		}
		for _, c := range candidates {
			if c.key == "" {
				continue
			}
			pm := peerMap[c.key]
			if pm == nil {
				pn := c.name
				if pn == "" && len(c.key) >= 12 {
					pn = c.key[:12]
				}
				pm = &peerAccum{key: c.key, name: pn, lastContact: p.FirstSeen}
				peerMap[c.key] = pm
			}
			pm.count++
			if p.FirstSeen > pm.lastContact {
				pm.lastContact = p.FirstSeen
			}
		}
	}
	peerSlice := make([]PeerInteraction, 0, len(peerMap))
	for _, pm := range peerMap {
		peerSlice = append(peerSlice, PeerInteraction{
			PeerKey: pm.key, PeerName: pm.name,
			MessageCount: pm.count, LastContact: pm.lastContact,
		})
	}
	sort.Slice(peerSlice, func(i, j int) bool {
		return peerSlice[i].MessageCount > peerSlice[j].MessageCount
	})
	if len(peerSlice) > 20 {
		peerSlice = peerSlice[:20]
	}

	// Uptime heatmap
	heatBuckets := map[string]*HeatmapCell{}
	for _, p := range packets {
		t, err := time.Parse(time.RFC3339, p.FirstSeen)
		if err != nil {
			t, err = time.Parse("2006-01-02 15:04:05", p.FirstSeen)
			if err != nil {
				continue
			}
		}
		dow := int(t.UTC().Weekday())
		hr := t.UTC().Hour()
		k := fmt.Sprintf("%d:%d", dow, hr)
		if heatBuckets[k] == nil {
			heatBuckets[k] = &HeatmapCell{DayOfWeek: dow, Hour: hr}
		}
		heatBuckets[k].Count++
	}
	uptimeHeatmap := make([]HeatmapCell, 0, len(heatBuckets))
	for _, cell := range heatBuckets {
		uptimeHeatmap = append(uptimeHeatmap, *cell)
	}

	// Computed stats
	totalPackets := len(packets)
	distinctHours := len(activityTimeline)
	totalHours := float64(days) * 24
	availabilityPct := 0.0
	if totalHours > 0 {
		availabilityPct = round(float64(distinctHours)*100.0/totalHours, 1)
		if availabilityPct > 100 {
			availabilityPct = 100
		}
	}

	var avgPacketsPerDay float64
	if days > 0 {
		avgPacketsPerDay = round(float64(totalPackets)/float64(days), 1)
	}

	// Longest silence
	var longestSilenceMs int
	var longestSilenceStart interface{}
	if len(activityTimeline) >= 2 {
		for i := 1; i < len(activityTimeline); i++ {
			var t1Str, t2Str string
			if activityTimeline[i-1].Bucket != nil {
				t1Str = *activityTimeline[i-1].Bucket
			}
			if activityTimeline[i].Bucket != nil {
				t2Str = *activityTimeline[i].Bucket
			}
			t1, e1 := time.Parse(time.RFC3339, t1Str)
			t2, e2 := time.Parse(time.RFC3339, t2Str)
			if e1 == nil && e2 == nil {
				gap := int(t2.Sub(t1).Milliseconds())
				if gap > longestSilenceMs {
					longestSilenceMs = gap
					longestSilenceStart = t1Str
				}
			}
		}
	}

	// Signal grade & SNR stats
	var snrMean, snrStdDev float64
	if len(snrTrend) > 0 {
		var sum float64
		for _, e := range snrTrend {
			if v, ok := e.SNR.(float64); ok {
				sum += v
			}
		}
		snrMean = sum / float64(len(snrTrend))
		if len(snrTrend) > 1 {
			var sqSum float64
			for _, e := range snrTrend {
				if v, ok := e.SNR.(float64); ok {
					sqSum += (v - snrMean) * (v - snrMean)
				}
			}
			snrStdDev = math.Sqrt(sqSum / float64(len(snrTrend)))
		}
	}

	signalGrade := "D"
	if snrMean > 15 && snrStdDev < 2 {
		signalGrade = "A"
	} else if snrMean > 15 {
		signalGrade = "A-"
	} else if snrMean > 12 && snrStdDev < 3 {
		signalGrade = "B+"
	} else if snrMean > 8 {
		signalGrade = "B"
	} else if snrMean > 3 {
		signalGrade = "C"
	}

	var relayPct float64
	if totalWithPath > 0 {
		relayPct = round(float64(relayedCount)*100.0/float64(totalWithPath), 1)
	}

	// Compute clock skew (already under RLock).
	clockSkew := s.getNodeClockSkewLocked(pubkey)

	return &NodeAnalyticsResponse{
		Node:                node,
		TimeRange:           TimeRangeResp{From: fromISO, To: toISO, Days: days},
		ActivityTimeline:    activityTimeline,
		SnrTrend:            snrTrend,
		PacketTypeBreakdown: packetTypeBreakdown,
		ObserverCoverage:    observerCoverage,
		HopDistribution:     hopDistribution,
		PeerInteractions:    peerSlice,
		UptimeHeatmap:       uptimeHeatmap,
		ComputedStats: ComputedNodeStats{
			AvailabilityPct:     availabilityPct,
			LongestSilenceMs:    longestSilenceMs,
			LongestSilenceStart: longestSilenceStart,
			SignalGrade:         signalGrade,
			SnrMean:             round(snrMean, 1),
			SnrStdDev:           round(snrStdDev, 1),
			RelayPct:            relayPct,
			TotalPackets:        totalPackets,
			UniqueObservers:     len(observerCoverage),
			UniquePeers:         len(peerSlice),
			AvgPacketsPerDay:    avgPacketsPerDay,
		},
		ClockSkew: clockSkew,
	}, nil
}

// GetAnalyticsSubpathsWithWindow is the window-aware variant of
// GetAnalyticsSubpaths. Issue #1217: the Route Patterns chart on /analytics
// ignored the Time window filter because callers always reached the unbounded
// path. With a zero TimeWindow this is byte-equivalent to GetAnalyticsSubpaths.
//
// For non-zero windows we iterate the packet list and filter on
// `tx.FirstSeen`. We deliberately do not consult the precomputed `spIndex`
// (which has no per-tx timestamp), so the windowed path is O(N_tx · path²);
// this matches the slow region-filtered path and keeps the fast unbounded
// hot path untouched. Results are cached by (region|area|window) so repeated
// renders of the same window don't re-scan.
func (s *PacketStore) GetAnalyticsSubpathsWithWindow(region string, minLen, maxLen, limit int, window TimeWindow) map[string]interface{} {
	if window.IsZero() {
		return s.GetAnalyticsSubpaths(region, minLen, maxLen, limit)
	}

	cacheKey := fmt.Sprintf("%s|%d|%d|%d|w=%s", region, minLen, maxLen, limit, window.CacheKey())
	s.cacheMu.Lock()
	if cached, ok := s.subpathCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsSubpathsWindowed(region, minLen, maxLen, limit, window)

	s.cacheMu.Lock()
	s.subpathCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

// computeAnalyticsSubpathsWindowed iterates s.packets and bounds the result
// to transmissions whose FirstSeen falls inside `window`. Optionally also
// region-filters. Mirrors computeSubpathsSlow but with the window predicate
// inlined and without requiring a non-empty region.
func (s *PacketStore) computeAnalyticsSubpathsWindowed(region string, minLen, maxLen, limit int, window TimeWindow) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, pm := s.getCachedNodesAndPM()
	contextPubkeys := buildAggregateHopContextPubkeys(s.packets, pm)
	hopCache := make(map[string]*nodeInfo)
	graph := s.graph.Load()
	resolveHop := func(hop string) string {
		if cached, ok := hopCache[hop]; ok {
			if cached != nil {
				return cached.Name
			}
			return hop
		}
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		hopCache[hop] = r
		if r != nil {
			return r.Name
		}
		return hop
	}

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	subpathCounts := make(map[string]*subpathAccum)
	totalPaths := 0

	for _, tx := range s.packets {
		if !window.Includes(tx.FirstSeen) {
			continue
		}
		hops := txGetParsedPath(tx)
		if len(hops) < 2 {
			continue
		}
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		totalPaths++

		named := make([]string, len(hops))
		for i, h := range hops {
			named[i] = resolveHop(h)
		}

		for l := minLen; l <= maxLen && l <= len(named); l++ {
			for start := 0; start <= len(named)-l; start++ {
				sub := strings.Join(named[start:start+l], " → ")
				raw := strings.Join(hops[start:start+l], ",")
				entry := subpathCounts[sub]
				if entry == nil {
					entry = &subpathAccum{raw: raw}
					subpathCounts[sub] = entry
				}
				entry.count++
			}
		}
	}

	return s.rankSubpaths(subpathCounts, totalPaths, limit)
}

func (s *PacketStore) GetAnalyticsSubpaths(region string, minLen, maxLen, limit int) map[string]interface{} {
	cacheKey := fmt.Sprintf("%s|%d|%d|%d", region, minLen, maxLen, limit)

	s.cacheMu.Lock()
	if cached, ok := s.subpathCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsSubpaths(region, minLen, maxLen, limit)

	s.cacheMu.Lock()
	s.subpathCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

// GetAnalyticsSubpathsBulkWithWindow is the window-aware variant. For a zero
// TimeWindow it is byte-equivalent to GetAnalyticsSubpathsBulk; for a non-
// zero window it delegates to GetAnalyticsSubpathsWithWindow per group so
// the `tx.FirstSeen` filter is honored (issue #1217).
func (s *PacketStore) GetAnalyticsSubpathsBulkWithWindow(region string, groups []subpathGroup, window TimeWindow) []map[string]interface{} {
	if window.IsZero() {
		return s.GetAnalyticsSubpathsBulk(region, groups)
	}
	results := make([]map[string]interface{}, len(groups))
	for i, g := range groups {
		results[i] = s.GetAnalyticsSubpathsWithWindow(region, g.MinLen, g.MaxLen, g.Limit, window)
	}
	return results
}

// GetAnalyticsSubpathsBulk returns multiple length-range buckets from a single
// scan of the subpath index, avoiding repeated iterations.
func (s *PacketStore) GetAnalyticsSubpathsBulk(region string, groups []subpathGroup) []map[string]interface{} {
	// For region queries or when there are few groups, fall back to individual calls
	// which benefit from per-key caching.
	if region != "" {
		results := make([]map[string]interface{}, len(groups))
		for i, g := range groups {
			results[i] = s.GetAnalyticsSubpaths(region, g.MinLen, g.MaxLen, g.Limit)
		}
		return results
	}

	// Check if all groups are cached.
	allCached := true
	cachedResults := make([]map[string]interface{}, len(groups))
	s.cacheMu.Lock()
	for i, g := range groups {
		cacheKey := fmt.Sprintf("|%d|%d|%d", g.MinLen, g.MaxLen, g.Limit)
		if cached, ok := s.subpathCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
			cachedResults[i] = cached.data
		} else {
			allCached = false
			break
		}
	}
	if allCached {
		s.cacheHits += int64(len(groups))
		s.cacheMu.Unlock()
		return cachedResults
	}
	s.cacheMu.Unlock()

	// Single scan: bucket by hop length into per-group accumulators.
	s.mu.RLock()
	_, pm := s.getCachedNodesAndPM()
	// Aggregate hop-disambiguation context across all packets so the
	// resolver's tiers 1 and 2 light up even on this bulk-aggregate path
	// (the index iterates raw subpath strings, not per-tx). See #1197.
	contextPubkeys := buildAggregateHopContextPubkeys(s.packets, pm)
	hopCache := make(map[string]*nodeInfo)
	graph := s.graph.Load() // hoist out of resolver closure (PR #1208 carmack #1)
	resolveHop := func(hop string) string {
		if cached, ok := hopCache[hop]; ok {
			if cached != nil {
				return cached.Name
			}
			return hop
		}
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		hopCache[hop] = r
		if r != nil {
			return r.Name
		}
		return hop
	}

	perGroup := make([]map[string]*subpathAccum, len(groups))
	for i := range groups {
		perGroup[i] = make(map[string]*subpathAccum)
	}

	for rawKey, count := range s.spIndex {
		hops := strings.Split(rawKey, ",")
		hopLen := len(hops)

		// Resolve hop names once, reuse across groups.
		var named []string
		var namedKey string
		resolved := false

		for gi, g := range groups {
			if hopLen < g.MinLen || hopLen > g.MaxLen {
				continue
			}
			if !resolved {
				named = make([]string, hopLen)
				for i, h := range hops {
					named[i] = resolveHop(h)
				}
				namedKey = strings.Join(named, " → ")
				resolved = true
			}
			entry := perGroup[gi][namedKey]
			if entry == nil {
				entry = &subpathAccum{raw: rawKey}
				perGroup[gi][namedKey] = entry
			}
			entry.count += count
		}
	}
	totalPaths := s.spTotalPaths
	s.mu.RUnlock()

	results := make([]map[string]interface{}, len(groups))
	for i, g := range groups {
		results[i] = s.rankSubpaths(perGroup[i], totalPaths, g.Limit)
	}

	// Cache individual results for future single-key lookups too.
	s.cacheMu.Lock()
	for i, g := range groups {
		cacheKey := fmt.Sprintf("|%d|%d|%d", g.MinLen, g.MaxLen, g.Limit)
		s.subpathCache[cacheKey] = &cachedResult{data: results[i], expiresAt: time.Now().Add(s.rfCacheTTL)}
	}
	s.cacheMu.Unlock()

	return results
}

// subpathAccum holds a running count for a single named subpath.
type subpathAccum struct {
	count int
	raw   string // first raw-hop key seen (used for rawHops in the API response)
}

func (s *PacketStore) computeAnalyticsSubpaths(region string, minLen, maxLen, limit int) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, pm := s.getCachedNodesAndPM()
	// Aggregate hop-disambiguation context across all packets — bulk
	// aggregator over s.spIndex / per-tx fallback both need it. See #1197.
	contextPubkeys := buildAggregateHopContextPubkeys(s.packets, pm)
	hopCache := make(map[string]*nodeInfo)
	graph := s.graph.Load() // hoist out of resolver closure (PR #1208 carmack #1)
	resolveHop := func(hop string) string {
		if cached, ok := hopCache[hop]; ok {
			if cached != nil {
				return cached.Name
			}
			return hop
		}
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		hopCache[hop] = r
		if r != nil {
			return r.Name
		}
		return hop
	}

	// For region queries fall back to packet iteration (region filtering
	// requires per-transmission observer checks).
	if region != "" {
		return s.computeSubpathsSlow(region, minLen, maxLen, limit, resolveHop)
	}

	// Fast path: read from precomputed raw-hop subpath index.
	// Resolve raw hop prefixes to names and merge counts.
	namedCounts := make(map[string]*subpathAccum, len(s.spIndex))
	for rawKey, count := range s.spIndex {
		hops := strings.Split(rawKey, ",")
		hopLen := len(hops)
		if hopLen < minLen || hopLen > maxLen {
			continue
		}
		named := make([]string, hopLen)
		for i, h := range hops {
			named[i] = resolveHop(h)
		}
		namedKey := strings.Join(named, " → ")
		entry := namedCounts[namedKey]
		if entry == nil {
			entry = &subpathAccum{raw: rawKey}
			namedCounts[namedKey] = entry
		}
		entry.count += count
	}

	return s.rankSubpaths(namedCounts, s.spTotalPaths, limit)
}

// computeSubpathsSlow is the original O(N) packet-iteration path, used only
// for region-filtered queries where we must check per-transmission observers.
func (s *PacketStore) computeSubpathsSlow(region string, minLen, maxLen, limit int, resolveHop func(string) string) map[string]interface{} {
	regionObs := s.resolveRegionObservers(region)

	subpathCounts := make(map[string]*subpathAccum)
	totalPaths := 0

	for _, tx := range s.packets {
		hops := txGetParsedPath(tx)
		if len(hops) < 2 {
			continue
		}
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		totalPaths++

		named := make([]string, len(hops))
		for i, h := range hops {
			named[i] = resolveHop(h)
		}

		for l := minLen; l <= maxLen && l <= len(named); l++ {
			for start := 0; start <= len(named)-l; start++ {
				sub := strings.Join(named[start:start+l], " → ")
				raw := strings.Join(hops[start:start+l], ",")
				entry := subpathCounts[sub]
				if entry == nil {
					entry = &subpathAccum{raw: raw}
					subpathCounts[sub] = entry
				}
				entry.count++
			}
		}
	}

	return s.rankSubpaths(subpathCounts, totalPaths, limit)
}

// rankSubpaths sorts accumulated subpath counts by frequency, truncates to
// limit, and builds the API response map.
func (s *PacketStore) rankSubpaths(counts map[string]*subpathAccum, totalPaths, limit int) map[string]interface{} {
	type subpathEntry struct {
		path  string
		count int
		raw   string
	}
	ranked := make([]subpathEntry, 0, len(counts))
	for path, data := range counts {
		ranked = append(ranked, subpathEntry{path, data.count, data.raw})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].count > ranked[j].count })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	subpaths := make([]map[string]interface{}, 0, len(ranked))
	for _, e := range ranked {
		pct := 0.0
		if totalPaths > 0 {
			pct = math.Round(float64(e.count)/float64(totalPaths)*1000) / 10
		}
		subpaths = append(subpaths, map[string]interface{}{
			"path":    e.path,
			"rawHops": strings.Split(e.raw, ","),
			"count":   e.count,
			"hops":    len(strings.Split(e.path, " → ")),
			"pct":     pct,
		})
	}

	return map[string]interface{}{
		"subpaths":   subpaths,
		"totalPaths": totalPaths,
	}
}

// --- Subpath Detail ---

func (s *PacketStore) GetSubpathDetail(rawHops []string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, pm := s.getCachedNodesAndPM()

	// Build the subpath key the same way the index does (lowercase, comma-joined)
	spKey := strings.ToLower(strings.Join(rawHops, ","))

	// Direct lookup instead of scanning all packets
	matchedTxs := s.spTxIndex[spKey]

	// Hop-disambiguation context: union over the matched txs that produced
	// this subpath. This is the right scope — those are the packets that
	// witnessed the requested hop sequence. See #1197.
	contextPubkeys := buildAggregateHopContextPubkeys(matchedTxs, pm)
	// Hoist atomic graph.Load() once for the whole request (PR #1208
	// carmack #1) — used by the rawHops loop AND the matched-tx loop below.
	graph := s.graph.Load()

	// Resolve the requested hops
	nodes := make([]map[string]interface{}, len(rawHops))
	for i, hop := range rawHops {
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		entry := map[string]interface{}{"hop": hop, "name": hop, "lat": nil, "lon": nil, "pubkey": nil}
		if r != nil {
			entry["name"] = r.Name
			entry["pubkey"] = r.PublicKey
			if r.HasGPS {
				entry["lat"] = r.Lat
				entry["lon"] = r.Lon
			}
		}
		nodes[i] = entry
	}

	hourBuckets := make([]int, 24)
	var snrSum, rssiSum float64
	var snrCount, rssiCount int
	observers := map[string]int{}
	parentPaths := map[string]int{}
	matchCount := len(matchedTxs)
	var firstSeen, lastSeen string

	for _, tx := range matchedTxs {
		ts := tx.FirstSeen
		if ts != "" {
			if firstSeen == "" || ts < firstSeen {
				firstSeen = ts
			}
			if lastSeen == "" || ts > lastSeen {
				lastSeen = ts
			}
			t, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				t, err = time.Parse("2006-01-02 15:04:05", ts)
			}
			if err == nil {
				hourBuckets[t.Hour()]++
			}
		}
		if tx.SNR != nil {
			snrSum += *tx.SNR
			snrCount++
		}
		if tx.RSSI != nil {
			rssiSum += *tx.RSSI
			rssiCount++
		}
		if tx.ObserverName != "" {
			observers[tx.ObserverName]++
		}

		// Full parent path (resolved). Per-tx context so the resolver picks
		// the right candidate when prefixes are ambiguous. See #1197.
		txCtx := buildHopContextPubkeys(tx, pm)
		hops := txGetParsedPath(tx)
		resolved := make([]string, len(hops))
		for i, h := range hops {
			r, _, _ := pm.resolveWithContext(h, txCtx, graph)
			if r != nil {
				resolved[i] = r.Name
			} else {
				resolved[i] = h
			}
		}
		fullPath := strings.Join(resolved, " → ")
		parentPaths[fullPath]++
	}

	var avgSnr, avgRssi interface{}
	if snrCount > 0 {
		avgSnr = snrSum / float64(snrCount)
	}
	if rssiCount > 0 {
		avgRssi = rssiSum / float64(rssiCount)
	}

	topParents := make([]map[string]interface{}, 0)
	for path, count := range parentPaths {
		topParents = append(topParents, map[string]interface{}{"path": path, "count": count})
	}
	sort.Slice(topParents, func(i, j int) bool {
		return topParents[i]["count"].(int) > topParents[j]["count"].(int)
	})
	if len(topParents) > 15 {
		topParents = topParents[:15]
	}

	topObs := make([]map[string]interface{}, 0)
	for name, count := range observers {
		topObs = append(topObs, map[string]interface{}{"name": name, "count": count})
	}
	sort.Slice(topObs, func(i, j int) bool {
		return topObs[i]["count"].(int) > topObs[j]["count"].(int)
	})
	if len(topObs) > 10 {
		topObs = topObs[:10]
	}

	return map[string]interface{}{
		"hops":             rawHops,
		"nodes":            nodes,
		"totalMatches":     matchCount,
		"firstSeen":        firstSeen,
		"lastSeen":         lastSeen,
		"signal":           map[string]interface{}{"avgSnr": avgSnr, "avgRssi": avgRssi, "samples": snrCount},
		"hourDistribution": hourBuckets,
		"parentPaths":      topParents,
		"observers":        topObs,
	}
}

// BackgroundLoadError returns the human-readable reason captured the last
// time backgroundLoadFailed flipped true. Empty string when no error has
// been recorded (either load is still running, succeeded, or has not run).
// See #1690 for the failure modes this surfaces.
func (s *PacketStore) BackgroundLoadError() string {
	s.bgErrMu.RLock()
	defer s.bgErrMu.RUnlock()
	return s.backgroundLoadErr
}

// LoadCoverageRatio returns the most recent totalLoaded / totalInDB ratio
// computed by loadBackgroundChunks. 0.0 until the background loader has
// run at least once. See #1690.
func (s *PacketStore) LoadCoverageRatio() float64 {
	s.bgErrMu.RLock()
	defer s.bgErrMu.RUnlock()
	return s.loadCoverageRatio
}
