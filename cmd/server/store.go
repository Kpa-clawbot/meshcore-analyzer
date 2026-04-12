package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// payloadTypeNames maps payload_type int → human-readable name (firmware-standard).
var payloadTypeNames = map[int]string{
	0: "REQ", 1: "RESPONSE", 2: "TXT_MSG", 3: "ACK", 4: "ADVERT",
	5: "GRP_TXT", 7: "ANON_REQ", 8: "PATH", 9: "TRACE", 11: "CONTROL",
}

// StoreTx is an in-memory transmission with embedded observations.
type StoreTx struct {
	ID               int
	RawHex           string
	Hash             string
	FirstSeen        string
	RouteType        *int
	PayloadType      *int
	DecodedJSON      string
	Observations     []*StoreObs
	ObservationCount int
	// Display fields from longest-path observation
	ObserverID   string
	ObserverName string
	SNR          *float64
	RSSI         *float64
	PathJSON     string
	Direction    string
	ResolvedPath []*string // resolved path from best observation
	LatestSeen           string // max observation timestamp (or FirstSeen if no observations)
	UniqueObserverCount  int    // cached count of distinct observer IDs
	// Cached parsed fields (set once, read many)
	parsedPath    []string               // cached parsePathJSON result
	pathParsed    bool                    // whether parsedPath has been set
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
	Direction      string
	SNR            *float64
	RSSI           *float64
	Score          *int
	PathJSON       string
	ResolvedPath   []*string // resolved full pubkeys, parallel to path_json; nil elements = unresolved
	Timestamp      string
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
//   1. mu            (sync.RWMutex) — guards the core packet data: packets,
//                     indexes (byHash, byTxID, byObsID, byObserver, byNode,
//                     byPathHop, byPayloadType), counters, and loaded flag.
//
//   2. cacheMu       (sync.Mutex)  — guards analytics response caches:
//                     rfCache, topoCache, hashCache, collisionCache, chanCache,
//                     distCache, subpathCache, and their TTLs/hit counters.
//                     Also guards rate-limited invalidation state
//                     (lastInvalidated, pendingInv).
//
//   3. channelsCacheMu (sync.Mutex) — guards the short-lived GetChannels
//                     cache (channelsCacheKey/Exp/Res).
//
//   4. groupedCacheMu (sync.Mutex)  — guards the short-lived
//                     QueryGroupedPackets cache.
//
//   5. regionObsMu   (sync.Mutex)  — guards the region→observer mapping
//                     cache (regionObsCache, regionObsCacheTime).
//
//   6. hashSizeInfoMu (sync.Mutex)  — guards the cached hash-size-info
//                     result (hashSizeInfoCache). Acquired independently or
//                     under mu (in EvictStale).
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
	cacheMu      sync.Mutex
	rfCache      map[string]*cachedResult // region → cached RF result
	topoCache    map[string]*cachedResult // region → cached topology result
	hashCache      map[string]*cachedResult // region → cached hash-sizes result
	collisionCache map[string]*cachedResult // cached hash-collisions result keyed by region ("" = global)
	chanCache    map[string]*cachedResult // region → cached channels result
	distCache    map[string]*cachedResult // region → cached distance result
	subpathCache map[string]*cachedResult // params → cached subpaths result
	rfCacheTTL        time.Duration
	collisionCacheTTL time.Duration
	cacheHits    int64
	cacheMisses  int64
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
	// Cached node list + prefix map (rebuilt on demand, shared across analytics)
	nodeCache     []nodeInfo
	nodePM        *prefixMap
	nodeCacheTime time.Time
	// Precomputed subpath index: raw comma-joined hops → occurrence count.
	// Built during Load(), incrementally updated on ingest. Avoids full
	// packet iteration at query time (O(unique_subpaths) vs O(total_packets)).
	spIndex      map[string]int        // "hop1,hop2" → count
	spTxIndex    map[string][]*StoreTx // "hop1,hop2" → transmissions containing this subpath
	spTotalPaths int                   // transmissions with paths >= 2 hops
	// Precomputed distance analytics: hop distances and path totals
	// computed during Load() and incrementally updated on ingest.
	distHops  []distHopRecord
	distPaths []distPathRecord

	// Cached GetNodeHashSizeInfo result — recomputed at most once every 15s
	hashSizeInfoMu    sync.Mutex
	hashSizeInfoCache map[string]*hashSizeNodeInfo
	hashSizeInfoAt    time.Time

	// Precomputed distinct advert pubkey count (refcounted for eviction correctness).
	// Updated incrementally during Load/Ingest/Evict — avoids JSON parsing in GetPerfStoreStats.
	advertPubkeys map[string]int // pubkey → number of advert packets referencing it

	// Debounce map for touchRelayLastSeen: pubkey → last time we wrote last_seen to DB.
	// Limits DB writes to at most 1 per node per 5 minutes.
	lastSeenTouched map[string]time.Time

	// Persisted neighbor graph for hop resolution at ingest time.
	graph *NeighborGraph

	// Async backfill state: set after backfillResolvedPathsAsync completes.
	backfillComplete atomic.Bool
	// Progress tracking for async backfill (total pending and processed so far).
	backfillTotal     atomic.Int64 // set once at start of async backfill
	backfillProcessed atomic.Int64

	// Eviction config and stats
	retentionHours   float64        // 0 = unlimited
	maxMemoryMB      int            // 0 = unlimited (packet store memory budget)
	evicted          int64          // total packets evicted
	trackedBytes     int64          // running total of estimated packet store memory
	memoryEstimator  func() float64 // injectable for tests; nil = use runtime.ReadMemStats (stats only)
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
		hashCache:      make(map[string]*cachedResult),

		collisionCache: make(map[string]*cachedResult),
		chanCache:     make(map[string]*cachedResult),
		distCache:     make(map[string]*cachedResult),
		subpathCache:  make(map[string]*cachedResult),
		rfCacheTTL:         15 * time.Second,
		collisionCacheTTL: 3600 * time.Second,
		invCooldown:       10 * time.Second,
		spIndex:       make(map[string]int, 4096),
		spTxIndex:     make(map[string][]*StoreTx, 4096),
		advertPubkeys:   make(map[string]int),
		lastSeenTouched: make(map[string]time.Time),
	}
	if cfg != nil {
		ps.retentionHours = cfg.RetentionHours
		ps.maxMemoryMB = cfg.MaxMemoryMB
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
	}
	return ps
}

// Load reads all transmissions + observations from SQLite into memory.
func (s *PacketStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t0 := time.Now()

	var loadSQL string
	rpCol := ""
	if s.db.hasResolvedPath {
		rpCol = ",\n\t\t\t\to.resolved_path"
	}
	if s.db.isV3 {
		loadSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + rpCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	} else {
		loadSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp` + rpCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	}

	rows, err := s.db.conn.Query(loadSQL)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsID sql.NullInt64
		var observerID, observerName, direction, pathJSON, obsTimestamp sql.NullString
		var snr, rssi sql.NullFloat64
		var score sql.NullInt64
		var resolvedPathStr sql.NullString

		scanArgs := []interface{}{&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsID, &observerID, &observerName, &direction,
			&snr, &rssi, &score, &pathJSON, &obsTimestamp}
		if s.db.hasResolvedPath {
			scanArgs = append(scanArgs, &resolvedPathStr)
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
				Direction:      nullStrVal(direction),
				SNR:            nullFloatPtr(snr),
				RSSI:           nullFloatPtr(rssi),
				Score:          nullIntPtr(score),
				PathJSON:       obsPJ,
				ResolvedPath:   unmarshalResolvedPath(nullStrVal(resolvedPathStr)),
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

	// Post-load: pick best observation (longest path) for each transmission
	for _, tx := range s.packets {
		pickBestObservation(tx)
	}

	// Build precomputed subpath index for O(1) analytics queries
	s.buildSubpathIndex()

	// Build path-hop index for O(1) node path lookups
	s.buildPathHopIndex()

	// Precompute distance analytics (hop distances, path totals)
	s.buildDistanceIndex()

	s.loaded = true
	elapsed := time.Since(t0)
	log.Printf("[store] Loaded %d transmissions (%d observations) in %v (tracked ~%.0fMB, heap ~%.0fMB)",
		len(s.packets), s.totalObs, elapsed, s.trackedMemoryMB(), s.estimatedMemoryMB())
	return nil
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
	tx.SNR = best.SNR
	tx.RSSI = best.RSSI
	tx.PathJSON = best.PathJSON
	tx.Direction = best.Direction
	tx.ResolvedPath = best.ResolvedPath
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

// indexByNode extracts pubkeys from decoded_json and indexes the transmission.
func (s *PacketStore) indexByNode(tx *StoreTx) {
	// Track which pubkeys have been indexed for this packet to avoid duplicates
	// when the same pubkey appears in both decoded JSON and resolved path.
	indexed := make(map[string]bool)

	// Index by decoded JSON fields (pubKey, destPubKey, srcPubKey).
	if tx.DecodedJSON != "" && strings.Contains(tx.DecodedJSON, "ubKey") {
		if decoded := tx.ParsedDecoded(); decoded != nil {
			for _, field := range []string{"pubKey", "destPubKey", "srcPubKey"} {
				if v, ok := decoded[field].(string); ok && v != "" {
					s.addToByNode(tx, v)
					indexed[v] = true
				}
			}
		}
	}

	// Index by resolved path entries — relay nodes that forwarded this packet.
	for _, obs := range tx.Observations {
		for _, rp := range obs.ResolvedPath {
			if rp == nil {
				continue
			}
			pk := *rp
			if pk == "" || indexed[pk] {
				continue
			}
			s.addToByNode(tx, pk)
			indexed[pk] = true
		}
	}
	// Also check tx.ResolvedPath (best observation's resolved path) for packets
	// loaded from DB where Observations may be empty.
	for _, rp := range tx.ResolvedPath {
		if rp == nil {
			continue
		}
		pk := *rp
		if pk == "" || indexed[pk] {
			continue
		}
		s.addToByNode(tx, pk)
		indexed[pk] = true
	}
}

// addToByNode adds tx to byNode[pubkey] with dedup via nodeHashes.
func (s *PacketStore) addToByNode(tx *StoreTx, pubkey string) {
	if s.nodeHashes[pubkey] == nil {
		s.nodeHashes[pubkey] = make(map[string]bool)
	}
	if s.nodeHashes[pubkey][tx.Hash] {
		return
	}
	s.nodeHashes[pubkey][tx.Hash] = true
	s.byNode[pubkey] = append(s.byNode[pubkey], tx)
}

// touchRelayLastSeen updates last_seen in the DB for relay nodes that appear
// in resolved_path entries. Debounced to at most 1 write per node per 5 minutes.
// Must be called under s.mu write lock (reads/writes lastSeenTouched).
func (s *PacketStore) touchRelayLastSeen(tx *StoreTx, now time.Time) {
	if s.db == nil {
		return
	}
	const debounceInterval = 5 * time.Minute

	seen := make(map[string]bool)
	// Collect unique non-nil resolved pubkeys from all observations.
	for _, obs := range tx.Observations {
		for _, rp := range obs.ResolvedPath {
			if rp == nil {
				continue
			}
			pk := *rp
			if pk != "" {
				seen[pk] = true
			}
		}
	}
	// Also check tx.ResolvedPath (best observation, used after Load).
	for _, rp := range tx.ResolvedPath {
		if rp == nil {
			continue
		}
		pk := *rp
		if pk != "" {
			seen[pk] = true
		}
	}

	ts := now.UTC().Format(time.RFC3339)
	for pk := range seen {
		if last, ok := s.lastSeenTouched[pk]; ok && now.Sub(last) < debounceInterval {
			continue
		}
		if err := s.db.TouchNodeLastSeen(pk, ts); err == nil {
			s.lastSeenTouched[pk] = now
		}
	}
}

// trackAdvertPubkey increments the advertPubkeys refcount for ADVERT packets.
// Must be called under s.mu write lock.
func (s *PacketStore) trackAdvertPubkey(tx *StoreTx) {
	if tx.PayloadType == nil || *tx.PayloadType != 4 || tx.DecodedJSON == "" {
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
	if tx.PayloadType == nil || *tx.PayloadType != 4 || tx.DecodedJSON == "" {
		return
	}
	var d map[string]interface{}
	if json.Unmarshal([]byte(tx.DecodedJSON), &d) != nil {
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
			packets = append(packets, txToMap(tx, q.ExpandObservations))
		}
	} else {
		// DESC: newest items are at the tail; page 0 = last pageSize items reversed
		endIdx := total - start
		startIdx := endIdx - pageSize
		if startIdx < 0 {
			startIdx = 0
		}
		for i := endIdx - 1; i >= startIdx; i-- {
			packets = append(packets, txToMap(results[i], q.ExpandObservations))
		}
	}
	return &PacketResult{Packets: packets, Total: total}
}

// QueryGroupedPackets returns transmissions grouped by hash (already 1:1).
func (s *PacketStore) QueryGroupedPackets(q PacketQuery) *PacketResult {
	atomic.AddInt64(&s.queryCount, 1)

	if q.Limit <= 0 {
		q.Limit = 50
	}

	// Cache key covers all filter dimensions. Empty key = no filters.
	cacheKey := q.Since + "|" + q.Until + "|" + q.Region + "|" + q.Node + "|" + q.Hash + "|" + q.Observer
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
		m := map[string]interface{}{
			"hash":              strOrNil(tx.Hash),
			"first_seen":        strOrNil(tx.FirstSeen),
			"count":             tx.ObservationCount,
			"observer_count":    tx.UniqueObserverCount,
			"observation_count": tx.ObservationCount,
			"latest":            strOrNil(tx.LatestSeen),
			"observer_id":       strOrNil(tx.ObserverID),
			"observer_name":     strOrNil(tx.ObserverName),
			"path_json":         strOrNil(tx.PathJSON),
			"payload_type":      intPtrOrNil(tx.PayloadType),
			"route_type":        intPtrOrNil(tx.RouteType),
			"raw_hex":           strOrNil(tx.RawHex),
			"decoded_json":      strOrNil(tx.DecodedJSON),
			"snr":               floatPtrOrNil(tx.SNR),
			"rssi":              floatPtrOrNil(tx.RSSI),
		}
		if tx.ResolvedPath != nil {
			m["resolved_path"] = tx.ResolvedPath
		}
		packets[i] = m
	}

	return &PacketResult{Packets: packets, Total: total}
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

	// Run node/observer counts and observation counts concurrently (2 queries instead of 5).
	var wg sync.WaitGroup
	var nodeErr, obsErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		nodeErr = s.db.conn.QueryRow(
			`SELECT
				(SELECT COUNT(*) FROM nodes WHERE last_seen > ?) AS active_nodes,
				(SELECT COUNT(*) FROM nodes) AS all_nodes,
				(SELECT COUNT(*) FROM observers) AS observers`,
			sevenDaysAgo,
		).Scan(&st.TotalNodes, &st.TotalNodesAllTime, &st.TotalObservers)
	}()
	go func() {
		defer wg.Done()
		obsErr = s.db.conn.QueryRow(
			`SELECT
				COALESCE(SUM(CASE WHEN timestamp > ? THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN timestamp > ? THEN 1 ELSE 0 END), 0)
			FROM observations WHERE timestamp > ?`,
			oneHourAgo, oneDayAgo, oneDayAgo,
		).Scan(&st.PacketsLastHour, &st.PacketsLast24h)
	}()
	wg.Wait()

	if nodeErr != nil {
		return st, nodeErr
	}
	if obsErr != nil {
		return st, obsErr
	}

	return st, nil
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

	// Distinct advert pubkey count — precomputed incrementally (see trackAdvertPubkey).
	advertByObsCount := len(s.advertPubkeys)
	s.mu.RUnlock()

	estimatedMB := math.Round(s.estimatedMemoryMB()*10) / 10
	trackedMB := math.Round(s.trackedMemoryMB()*10) / 10

	evicted := atomic.LoadInt64(&s.evicted)

	return map[string]interface{}{
		"totalLoaded":       totalLoaded,
		"totalObservations": totalObs,
		"evicted":           evicted,
		"inserts":           atomic.LoadInt64(&s.insertCount),
		"queries":           atomic.LoadInt64(&s.queryCount),
		"inMemory":          totalLoaded,
		"sqliteOnly":        false,
		"retentionHours":    s.retentionHours,
		"maxMemoryMB":       s.maxMemoryMB,
		"estimatedMB":       estimatedMB,
		"trackedMB":         trackedMB,
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
	hasNewObservations bool // new SNR/RSSI data → rfCache
	hasNewPaths        bool // new/changed path data → topoCache, distCache, subpathCache
	hasNewTransmissions bool // new transmissions → hashCache
	hasChannelData     bool // new GRP_TXT (payload_type 5) → chanCache
	eviction           bool // data removed → all caches
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
		s.pendingInv.hasChannelData = s.pendingInv.hasChannelData || inv.hasChannelData
		return
	}

	// Cooldown expired — merge any pending flags and apply
	if s.pendingInv != nil {
		inv.hasNewObservations = inv.hasNewObservations || s.pendingInv.hasNewObservations
		inv.hasNewPaths = inv.hasNewPaths || s.pendingInv.hasNewPaths
		inv.hasNewTransmissions = inv.hasNewTransmissions || s.pendingInv.hasNewTransmissions
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

	advertByObsCount := len(s.advertPubkeys)
	s.mu.RUnlock()

	estimatedMB := math.Round(s.estimatedMemoryMB()*10) / 10
	trackedMB := math.Round(s.trackedMemoryMB()*10) / 10

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
		MaxMB:             s.maxMemoryMB,
		Indexes: PacketStoreIndexes{
			ByHash:           hashIdx,
			ByObserver:       observerIdx,
			ByNode:           nodeIdx,
			AdvertByObserver: advertByObsCount,
		},
	}
}

// GetTransmissionByID returns a transmission by its DB ID, formatted as a map.
func (s *PacketStore) GetTransmissionByID(id int) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byTxID[id]
	if tx == nil {
		return nil
	}
	return txToMap(tx, true)
}

// GetPacketByHash returns a transmission by content hash.
func (s *PacketStore) GetPacketByHash(hash string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byHash[strings.ToLower(hash)]
	if tx == nil {
		return nil
	}
	return txToMap(tx, true)
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
	// Sort oldest-first to match pagination expectations (same as s.packets order).
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].FirstSeen < filtered[j].FirstSeen
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
			packets = append(packets, txToMap(tx))
		}
	} else {
		endIdx := total - offset
		startIdx := endIdx - pageSize
		if startIdx < 0 {
			startIdx = 0
		}
		for i := endIdx - 1; i >= startIdx; i-- {
			packets = append(packets, txToMap(filtered[i]))
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
	if s.db.isV3 {
		querySQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE t.id > ?
			ORDER BY t.id ASC, o.timestamp DESC`
	} else {
		querySQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
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
		txID                                                 int
		rawHex, hash, firstSeen, decodedJSON                 string
		routeType, payloadType                               *int
		obsID                                                *int
		observerID, observerName, direction, pathJSON, obsTS string
		snr, rssi                                            *float64
		score                                                *int
	}

	var tempRows []tempRow
	txCount := 0
	lastTxID := sinceID

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsIDVal sql.NullInt64
		var observerID, observerName, direction, pathJSON, obsTimestamp sql.NullString
		var snrVal, rssiVal sql.NullFloat64
		var scoreVal sql.NullInt64

		if err := rows.Scan(&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsIDVal, &observerID, &observerName, &direction,
			&snrVal, &rssiVal, &scoreVal, &pathJSON, &obsTimestamp); err != nil {
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
			direction:    nullStrVal(direction),
			pathJSON:     nullStrVal(pathJSON),
			obsTS:        nullStrVal(obsTimestamp),
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
	var broadcastOrder []int

	// Hoist getCachedNodesAndPM() once before the observation loop to avoid
	// per-observation function calls (review item #1).
	_, cachedPM := s.getCachedNodesAndPM()

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
				obsKeys:     make(map[string]bool),
				observerSet: make(map[string]bool),
			}
			s.byHash[r.hash] = tx
			s.packets = append(s.packets, tx) // oldest-first; new items go to tail
			s.byTxID[r.txID] = tx
			if r.txID > s.maxTxID {
				s.maxTxID = r.txID
			}
			s.indexByNode(tx)
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
				Direction:      r.direction,
				SNR:            r.snr,
				RSSI:           r.rssi,
				Score:          r.score,
				PathJSON:       r.pathJSON,
				Timestamp:      normalizeTimestamp(r.obsTS),
			}

			// Resolve path at ingest time using neighbor graph
			// (cachedPM is hoisted before the observation loop to avoid per-obs function calls)
			if r.pathJSON != "" && r.pathJSON != "[]" && cachedPM != nil {
				obs.ResolvedPath = resolvePathForObs(r.pathJSON, r.observerID, tx, cachedPM, s.graph)
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

	// Phase 2 of #660: update last_seen in DB for relay nodes seen in resolved_path.
	now := time.Now()
	for _, tx := range broadcastTxs {
		s.touchRelayLastSeen(tx, now)
	}

	// Incrementally update precomputed subpath index with new transmissions
	for _, tx := range broadcastTxs {
		if addTxToSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
			s.spTotalPaths++
		}
		addTxToPathHopIndex(s.byPathHop, tx)
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
		hopCache := make(map[string]*nodeInfo)
		resolveHop := func(hop string) *nodeInfo {
			if cached, ok := hopCache[hop]; ok {
				return cached
			}
			r, _, _ := pm.resolveWithContext(hop, nil, s.graph)
			hopCache[hop] = r
			return r
		}
		for _, tx := range broadcastTxs {
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
			var payload map[string]interface{}
			if json.Unmarshal([]byte(tx.DecodedJSON), &payload) == nil {
				decoded["payload"] = payload
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
			if obs.ResolvedPath != nil {
				pkt["resolved_path"] = obs.ResolvedPath
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

	// Persist resolved paths and neighbor edges asynchronously (don't block ingest).
	if len(broadcastTxs) > 0 && s.db != nil {
		dbPath := s.db.path
		var obsUpdates []persistObsUpdate
		var edgeUpdates []persistEdgeUpdate

		_, pm := s.getCachedNodesAndPM()
		// Read graph ref under lock (it's set during startup and not replaced after,
		// but reading under lock is safer — review item #5).
		graphRef := s.graph
		for _, tx := range broadcastTxs {
			for _, obs := range tx.Observations {
				if obs.ResolvedPath != nil {
					rpJSON := marshalResolvedPath(obs.ResolvedPath)
					if rpJSON != "" {
						obsUpdates = append(obsUpdates, persistObsUpdate{obs.ID, rpJSON})
					}
				}
				for _, ec := range extractEdgesFromObs(obs, tx, pm) {
					edgeUpdates = append(edgeUpdates, persistEdgeUpdate{ec.A, ec.B, ec.Timestamp})
					if graphRef != nil {
						graphRef.upsertEdge(ec.A, ec.B, "", obs.ObserverID, obs.SNR, parseTimestamp(ec.Timestamp))
					}
				}
			}
		}

		asyncPersistResolvedPathsAndEdges(dbPath, obsUpdates, edgeUpdates, "persist")
	}

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
	if s.db.isV3 {
		querySQL = `SELECT o.id, o.transmission_id, obs.id, obs.name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')
			FROM observations o
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE o.id > ?
			ORDER BY o.id ASC
			LIMIT ?`
	} else {
		querySQL = `SELECT o.id, o.transmission_id, o.observer_id, o.observer_name, o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp
			FROM observations o
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
		direction    string
		snr, rssi    *float64
		score        *int
		pathJSON     string
		timestamp    string
	}

	var obsRows []obsRow
	for rows.Next() {
		var oid, txID int
		var observerID, observerName, direction, pathJSON, ts sql.NullString
		var snr, rssi sql.NullFloat64
		var score sql.NullInt64

		if err := rows.Scan(&oid, &txID, &observerID, &observerName, &direction,
			&snr, &rssi, &score, &pathJSON, &ts); err != nil {
			continue
		}

		obsRows = append(obsRows, obsRow{
			obsID:        oid,
			txID:         txID,
			observerID:   nullStrVal(observerID),
			observerName: nullStrVal(observerName),
			direction:    nullStrVal(direction),
			snr:          nullFloatPtr(snr),
			rssi:         nullFloatPtr(rssi),
			score:        nullIntPtr(score),
			pathJSON:     nullStrVal(pathJSON),
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

	// Hoist getCachedNodesAndPM() before the loop — same pattern as IngestNewFromDB (review fix #1).
	_, pm := s.getCachedNodesAndPM()
	graphRef := s.graph

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
			Direction:      r.direction,
			SNR:            r.snr,
			RSSI:           r.rssi,
			Score:          r.score,
			PathJSON:       r.pathJSON,
			Timestamp:      normalizeTimestamp(r.timestamp),
		}

		// Resolve path at ingest time for late-arriving observations (review item #2).
		if r.pathJSON != "" && r.pathJSON != "[]" {
			if pm != nil {
				obs.ResolvedPath = resolvePathForObs(r.pathJSON, r.observerID, tx, pm, s.graph)
			}
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
			var payload map[string]interface{}
			if json.Unmarshal([]byte(tx.DecodedJSON), &payload) == nil {
				decoded["payload"] = payload
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
		if obs.ResolvedPath != nil {
			pkt["resolved_path"] = obs.ResolvedPath
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
	oldResolvedPaths := make(map[int][]*string, len(updatedTxs))
	for txID, tx := range updatedTxs {
		oldPaths[txID] = tx.PathJSON
		oldResolvedPaths[txID] = tx.ResolvedPath
	}
	for _, tx := range updatedTxs {
		pickBestObservation(tx)
	}
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
			// Remove old path-hop index entries using old hops + old resolved path.
			if len(oldHops) > 0 {
				saved, savedFlag := tx.parsedPath, tx.pathParsed
				savedRP := tx.ResolvedPath
				tx.parsedPath, tx.pathParsed = oldHops, true
				tx.ResolvedPath = oldResolvedPaths[txID]
				removeTxFromPathHopIndex(s.byPathHop, tx)
				tx.parsedPath, tx.pathParsed = saved, savedFlag
				tx.ResolvedPath = savedRP
			}
			// pickBestObservation already set pathParsed=false so
			// addTxToSubpathIndex will re-parse the new path.
			if addTxToSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
				s.spTotalPaths++
			}
			addTxToPathHopIndex(s.byPathHop, tx)
		}
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

	// Persist resolved paths and neighbor edges asynchronously (review fix #3).
	// Only process NEW observations — not all observations of each updated tx —
	// to avoid edge count inflation and unnecessary UPDATEs for pre-existing data.
	if len(newObs) > 0 && s.db != nil {
		dbPath := s.db.path
		var obsUpdates []persistObsUpdate
		var edgeUpdates []persistEdgeUpdate

		for _, obs := range newObs {
			tx := s.byTxID[obs.TransmissionID]
			if tx == nil {
				continue
			}
			if obs.ResolvedPath != nil {
				rpJSON := marshalResolvedPath(obs.ResolvedPath)
				if rpJSON != "" {
					obsUpdates = append(obsUpdates, persistObsUpdate{obs.ID, rpJSON})
				}
			}
			for _, ec := range extractEdgesFromObs(obs, tx, pm) {
				edgeUpdates = append(edgeUpdates, persistEdgeUpdate{ec.A, ec.B, ec.Timestamp})
				if graphRef != nil {
					graphRef.upsertEdge(ec.A, ec.B, "", obs.ObserverID, obs.SNR, parseTimestamp(ec.Timestamp))
				}
			}
		}

		asyncPersistResolvedPathsAndEdges(dbPath, obsUpdates, edgeUpdates, "obs-persist")
	}

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
		q.Region == "" && q.Node == "" && q.Since == "" && q.Until == "" {
		h := strings.ToLower(q.Hash)
		tx := s.byHash[h]
		if tx == nil {
			return nil
		}
		return []*StoreTx{tx}
	}
	if q.Observer != "" && q.Type == nil && q.Route == nil &&
		q.Region == "" && q.Node == "" && q.Hash == "" && q.Since == "" && q.Until == "" {
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

	// Pre-compute node filter parameters.
	var nodePK string
	hasNode := q.Node != ""
	if hasNode {
		nodePK = s.db.resolveNodePubkey(q.Node)
	}

	// Determine the source slice. Use index-based source when only node
	// filter is active and an index exists.
	source := s.packets
	if hasNode && !hasType && !hasRoute && q.Observer == "" &&
		filterHash == "" && !hasSince && !hasUntil && q.Region == "" {
		if indexed, ok := s.byNode[nodePK]; ok {
			return indexed
		}
	}
	// Single-pass filter: apply all predicates in one scan.
	results := filterTxSlice(source, func(tx *StoreTx) bool {
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
		if hasNode {
			if tx.DecodedJSON == "" {
				return false
			}
			if !strings.Contains(tx.DecodedJSON, nodePK) && !strings.Contains(tx.DecodedJSON, q.Node) {
				return false
			}
		}
		return true
	})

	return results
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

// enrichObs returns a map with observation fields + transmission fields.
func (s *PacketStore) enrichObs(obs *StoreObs) map[string]interface{} {
	tx := s.byTxID[obs.TransmissionID]

	m := map[string]interface{}{
		"id":            obs.ID,
		"timestamp":     strOrNil(obs.Timestamp),
		"observer_id":   strOrNil(obs.ObserverID),
		"observer_name": strOrNil(obs.ObserverName),
		"direction":     strOrNil(obs.Direction),
		"snr":           floatPtrOrNil(obs.SNR),
		"rssi":          floatPtrOrNil(obs.RSSI),
		"score":         intPtrOrNil(obs.Score),
		"path_json":     strOrNil(obs.PathJSON),
	}
	if obs.ResolvedPath != nil {
		m["resolved_path"] = obs.ResolvedPath
	}

	if tx != nil {
		m["hash"] = strOrNil(tx.Hash)
		m["raw_hex"] = strOrNil(tx.RawHex)
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
		"snr":               floatPtrOrNil(tx.SNR),
		"rssi":              floatPtrOrNil(tx.RSSI),
		"path_json":         strOrNil(tx.PathJSON),
		"direction":         strOrNil(tx.Direction),
	}
	if tx.ResolvedPath != nil {
		m["resolved_path"] = tx.ResolvedPath
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
				"snr":           floatPtrOrNil(o.SNR),
				"rssi":          floatPtrOrNil(o.RSSI),
				"path_json":     strOrNil(o.PathJSON),
				"timestamp":     strOrNil(o.Timestamp),
				"direction":     strOrNil(o.Direction),
			}
			if o.ResolvedPath != nil {
				om["resolved_path"] = o.ResolvedPath
			}
			obs = append(obs, om)
		}
		m["observations"] = obs
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

// nodeInResolvedPath checks whether a transmission's resolved_path contains
// the target node's full pubkey. Returns true if at least one observation's
// resolved_path includes targetPK (lowercased). Excludes transmissions where
// resolved_path is nil/empty or the hop resolved to a different node.
func nodeInResolvedPath(tx *StoreTx, targetPK string) bool {
	// If no resolved_path data exists anywhere on this tx, we can't
	// disambiguate — return true to keep it (avoid dropping old data).
	hasAny := false

	// Check the best observation's resolved_path (stored on tx directly).
	if tx.ResolvedPath != nil && len(tx.ResolvedPath) > 0 {
		hasAny = true
		for _, rp := range tx.ResolvedPath {
			if rp != nil && strings.ToLower(*rp) == targetPK {
				return true
			}
		}
	}
	// Also check all observations in case a non-best observation resolved it.
	for _, obs := range tx.Observations {
		if obs.ResolvedPath == nil || len(obs.ResolvedPath) == 0 {
			continue
		}
		hasAny = true
		for _, rp := range obs.ResolvedPath {
			if rp != nil && strings.ToLower(*rp) == targetPK {
				return true
			}
		}
	}
	// No resolved_path data at all — can't disambiguate, keep the candidate.
	return !hasAny
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

// buildPathHopIndex scans all packets and populates byPathHop.
// Must be called with s.mu held.
func (s *PacketStore) buildPathHopIndex() {
	s.byPathHop = make(map[string][]*StoreTx, 4096)
	for _, tx := range s.packets {
		addTxToPathHopIndex(s.byPathHop, tx)
	}
	log.Printf("[store] Built path-hop index: %d unique keys", len(s.byPathHop))
}

// addTxToPathHopIndex indexes a transmission under each unique hop key
// (raw lowercase hop + resolved full pubkey from ResolvedPath).
func addTxToPathHopIndex(idx map[string][]*StoreTx, tx *StoreTx) {
	hops := txGetParsedPath(tx)
	if len(hops) == 0 {
		return
	}
	seen := make(map[string]bool, len(hops)*2)
	for i, hop := range hops {
		key := strings.ToLower(hop)
		if !seen[key] {
			seen[key] = true
			idx[key] = append(idx[key], tx)
		}
		// Also index by resolved pubkey if available
		if tx.ResolvedPath != nil && i < len(tx.ResolvedPath) && tx.ResolvedPath[i] != nil {
			pk := *tx.ResolvedPath[i]
			if !seen[pk] {
				seen[pk] = true
				idx[pk] = append(idx[pk], tx)
			}
		}
	}
}

// removeTxFromPathHopIndex removes a transmission from all its path-hop index entries.
func removeTxFromPathHopIndex(idx map[string][]*StoreTx, tx *StoreTx) {
	hops := txGetParsedPath(tx)
	if len(hops) == 0 {
		return
	}
	seen := make(map[string]bool, len(hops)*2)
	for i, hop := range hops {
		key := strings.ToLower(hop)
		if !seen[key] {
			seen[key] = true
			removeTxFromSlice(idx, key, tx)
		}
		if tx.ResolvedPath != nil && i < len(tx.ResolvedPath) && tx.ResolvedPath[i] != nil {
			pk := *tx.ResolvedPath[i]
			if !seen[pk] {
				seen[pk] = true
				removeTxFromSlice(idx, pk, tx)
			}
		}
	}
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
	hopCache := make(map[string]*nodeInfo)
	resolveHop := func(hop string) *nodeInfo {
		if cached, ok := hopCache[hop]; ok {
			return cached
		}
		r, _, _ := pm.resolveWithContext(hop, nil, s.graph)
		hopCache[hop] = r
		return r
	}

	// Recompute distance records for each changed tx.
	for _, tx := range txs {
		txHops, txPath := computeDistancesForTx(tx, nodeByPk, repeaterSet, resolveHop)
		if len(txHops) > 0 {
			s.distHops = append(s.distHops, txHops...)
		}
		if txPath != nil {
			s.distPaths = append(s.distPaths, *txPath)
		}
	}
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

	hopCache := make(map[string]*nodeInfo)
	resolveHop := func(hop string) *nodeInfo {
		if cached, ok := hopCache[hop]; ok {
			return cached
		}
		r, _, _ := pm.resolveWithContext(hop, nil, s.graph)
		hopCache[hop] = r
		return r
	}

	hops := make([]distHopRecord, 0, len(s.packets))
	paths := make([]distPathRecord, 0, len(s.packets)/2)

	for _, tx := range s.packets {
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
const (
	storeTxBaseBytes  = 384 // StoreTx struct fields + map headers + sync.Once + string headers
	storeObsBaseBytes = 192 // StoreObs struct fields + string headers
	indexEntryBytes   = 48  // average cost of one index map entry (key + pointer + bucket overhead)
	numIndexesPerTx   = 5   // byHash, byTxID, byNode, byPayloadType, nodeHashes entries
	numIndexesPerObs  = 2   // byObsID, byObserver entries
)

// estimateStoreTxBytes returns the estimated memory cost of a StoreTx (excluding observations).
func estimateStoreTxBytes(tx *StoreTx) int64 {
	base := int64(storeTxBaseBytes)
	base += int64(len(tx.RawHex) + len(tx.Hash) + len(tx.DecodedJSON) + len(tx.PathJSON))
	base += int64(numIndexesPerTx * indexEntryBytes)
	return base
}

// estimateStoreObsBytes returns the estimated memory cost of a StoreObs.
func estimateStoreObsBytes(obs *StoreObs) int64 {
	base := int64(storeObsBaseBytes)
	base += int64(len(obs.PathJSON) + len(obs.ObserverID))
	base += int64(numIndexesPerObs * indexEntryBytes)
	return base
}

// estimatedMemoryMB returns current Go heap allocation in MB.
// Kept for stats/debug endpoints only — NOT used in eviction decisions.
// In tests, memoryEstimator can be set to inject a deterministic value.
func (s *PacketStore) estimatedMemoryMB() float64 {
	if s.memoryEstimator != nil {
		return s.memoryEstimator()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.HeapAlloc) / 1048576.0
}

// trackedMemoryMB returns the self-accounted packet store memory in MB.
func (s *PacketStore) trackedMemoryMB() float64 {
	return float64(s.trackedBytes) / 1048576.0
}

// EvictStale removes packets older than the retention window and/or exceeding
// the memory cap. Must be called with s.mu held (Lock). Returns the number of
// packets evicted.
func (s *PacketStore) EvictStale() int {
	if s.retentionHours <= 0 && s.maxMemoryMB <= 0 {
		return 0
	}

	cutoffIdx := 0

	// Time-based eviction: find how many packets from the head are too old
	if s.retentionHours > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(s.retentionHours*3600) * time.Second).Format(time.RFC3339)
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
			var decoded map[string]interface{}
			if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) == nil {
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
		// Clean up resolved_path pubkeys from byNode/nodeHashes
		for _, obs := range tx.Observations {
			for _, rp := range obs.ResolvedPath {
				if rp == nil {
					continue
				}
				pk := *rp
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
		}
		for _, rp := range tx.ResolvedPath {
			if rp == nil {
				continue
			}
			pk := *rp
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

		// Remove from subpath index
		removeTxFromSubpathIndexFull(s.spIndex, s.spTxIndex, tx)
		// Remove from path-hop index
		removeTxFromPathHopIndex(s.byPathHop, tx)
	}

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

	return evictCount
}

// RunEviction acquires the write lock and runs eviction. Safe to call from
// a goroutine. Returns evicted count.
func (s *PacketStore) RunEviction() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.EvictStale()
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
		var dec map[string]interface{}
		if json.Unmarshal([]byte(tx.DecodedJSON), &dec) == nil {
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
			channelName = "unknown"
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

	s.channelsCacheMu.Lock()
	s.channelsCacheRes = channels
	s.channelsCacheKey = cacheKey
	s.channelsCacheExp = time.Now().Add(15 * time.Second)
	s.channelsCacheMu.Unlock()

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
					"timestamp":        strOrNil(tx.FirstSeen),
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
func (s *PacketStore) GetAnalyticsChannels(region string) map[string]interface{} {
	s.cacheMu.Lock()
	if cached, ok := s.chanCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsChannels(region)

	s.cacheMu.Lock()
	s.chanCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsChannels(region string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	type decodedGrp struct {
		Type         string      `json:"type"`
		Channel      string      `json:"channel"`
		ChannelHash  interface{} `json:"channelHash"`
		ChannelHash2 string      `json:"channel_hash"`
		Text         string      `json:"text"`
		Sender       string      `json:"sender"`
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
		// Use hash as key for grouping (matches Node.js String(hash))
		chKey := hash
		if decoded.Type == "CHAN" && decoded.Channel != "" {
			chKey = hash + "_" + decoded.Channel
		}

		ch := channelMap[chKey]
		if ch == nil {
			ch = &chanInfo{Hash: hash, Name: name, Senders: map[string]bool{}, LastActivity: tx.FirstSeen, Encrypted: encrypted}
			channelMap[chKey] = ch
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
func (s *PacketStore) GetAnalyticsRF(region string) map[string]interface{} {
	s.cacheMu.Lock()
	if cached, ok := s.rfCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsRF(region)

	s.cacheMu.Lock()
	s.rfCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsRF(region string) map[string]interface{} {
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
				totalObs++
				tx := s.byTxID[obs.TransmissionID]
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
	PublicKey string
	Name      string
	Role      string
	Lat       float64
	Lon       float64
	HasGPS    bool
}

func (s *PacketStore) getAllNodes() []nodeInfo {
	rows, err := s.db.conn.Query("SELECT public_key, name, role, lat, lon FROM nodes")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var nodes []nodeInfo
	for rows.Next() {
		var pk string
		var name, role sql.NullString
		var lat, lon sql.NullFloat64
		rows.Scan(&pk, &name, &role, &lat, &lon)
		n := nodeInfo{PublicKey: pk, Name: nullStrVal(name), Role: nullStrVal(role)}
		if lat.Valid && lon.Valid {
			n.Lat = lat.Float64
			n.Lon = lon.Float64
			n.HasGPS = !(n.Lat == 0 && n.Lon == 0)
		}
		nodes = append(nodes, n)
	}
	return nodes
}

type prefixMap struct {
	m map[string][]nodeInfo
}

// maxPrefixLen caps prefix map entries. MeshCore path hops use 2–6 char
// prefixes; 8 gives comfortable headroom while cutting map size from ~31×N
// entries to ~7×N (+ 1 full-key entry per node for exact-match lookups).
const maxPrefixLen = 8

func buildPrefixMap(nodes []nodeInfo) *prefixMap {
	pm := &prefixMap{m: make(map[string][]nodeInfo, len(nodes)*(maxPrefixLen+1))}
	for _, n := range nodes {
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
// priority: (1) affinity graph score, (2) geographic proximity to context
// nodes, (3) GPS preference, (4) first match fallback.
//
// contextPubkeys are pubkeys of nodes that provide context for disambiguation
// (e.g., the originator, observer, or adjacent hops in the path).
// graph may be nil, in which case it falls back to the existing resolve().
func (pm *prefixMap) resolveWithContext(hop string, contextPubkeys []string, graph *NeighborGraph) (*nodeInfo, string, float64) {
	h := strings.ToLower(hop)
	candidates := pm.m[h]
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
	if graph != nil && len(contextPubkeys) > 0 {
		type scored struct {
			idx   int
			score float64
			count int // observation count of the best-scoring edge
		}
		now := time.Now()
		var scores []scored
		for i, cand := range candidates {
			candPK := strings.ToLower(cand.PublicKey)
			bestScore := 0.0
			bestCount := 0
			for _, ctxPK := range contextPubkeys {
				edges := graph.Neighbors(strings.ToLower(ctxPK))
				for _, e := range edges {
					if e.Ambiguous {
						continue
					}
					otherPK := e.NodeA
					if strings.EqualFold(otherPK, ctxPK) {
						otherPK = e.NodeB
					}
					if strings.EqualFold(otherPK, candPK) {
						s := e.Score(now)
						if s > bestScore {
							bestScore = s
							bestCount = e.Count
						}
					}
				}
			}
			if bestScore > 0 {
				scores = append(scores, scored{i, bestScore, bestCount})
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

	// Priority 3: GPS preference
	for i := range candidates {
		if candidates[i].HasGPS {
			return &candidates[i], "gps_preference", 0
		}
	}

	// Priority 4: First match fallback
	return &candidates[0], "first_match", 0
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

func (s *PacketStore) GetAnalyticsTopology(region string) map[string]interface{} {
	s.cacheMu.Lock()
	if cached, ok := s.topoCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsTopology(region)

	s.cacheMu.Lock()
	s.topoCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsTopology(region string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	allNodes, pm := s.getCachedNodesAndPM()
	_ = allNodes // only pm is needed for topology
	hopCache := make(map[string]*nodeInfo)

	resolveHop := func(hop string) *nodeInfo {
		if cached, ok := hopCache[hop]; ok {
			return cached
		}
		r, _, _ := pm.resolveWithContext(hop, nil, s.graph)
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

	for _, tx := range s.packets {
		hops := txGetParsedPath(tx)
		if len(hops) == 0 {
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

		n := len(hops)
		hopCounts[n]++
		allHopsList = append(allHopsList, n)
		if tx.SNR != nil {
			hopSnr[n] = append(hopSnr[n], *tx.SNR)
		}
		for _, h := range hops {
			hopFreq[h]++
		}
		for i := 0; i < len(hops)-1; i++ {
			a, b := hops[i], hops[i+1]
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

func (s *PacketStore) GetAnalyticsDistance(region string) map[string]interface{} {
	s.cacheMu.Lock()
	if cached, ok := s.distCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsDistance(region)

	s.cacheMu.Lock()
	s.distCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsDistance(region string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	// Build region match set using precomputed tx pointers
	var matchSet map[*StoreTx]bool
	if regionObs != nil {
		matchSet = make(map[*StoreTx]bool)
		seen := make(map[*StoreTx]bool)
		for i := range s.distHops {
			tx := s.distHops[i].tx
			if seen[tx] {
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
		for i := range s.distPaths {
			tx := s.distPaths[i].tx
			if seen[tx] {
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

	// Filter precomputed hop records (copy to avoid mutating precomputed data during sort)
	filteredHops := make([]distHopRecord, 0, len(s.distHops))
	for i := range s.distHops {
		if matchSet == nil || matchSet[s.distHops[i].tx] {
			filteredHops = append(filteredHops, s.distHops[i])
		}
	}

	// Filter precomputed path records
	filteredPaths := make([]distPathRecord, 0, len(s.distPaths))
	for i := range s.distPaths {
		if matchSet == nil || matchSet[s.distPaths[i].tx] {
			filteredPaths = append(filteredPaths, s.distPaths[i])
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

	// Sort and pick top hops
	sort.Slice(filteredHops, func(i, j int) bool { return filteredHops[i].Dist > filteredHops[j].Dist })
	topHops := make([]map[string]interface{}, 0)
	for i := range filteredHops {
		if i >= 50 {
			break
		}
		h := &filteredHops[i]
		topHops = append(topHops, map[string]interface{}{
			"fromName": h.FromName, "fromPk": h.FromPk,
			"toName": h.ToName, "toPk": h.ToPk,
			"dist": h.Dist, "type": h.Type,
			"snr": floatPtrOrNil(h.SNR), "hash": h.Hash, "timestamp": h.Timestamp,
		})
	}

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

func (s *PacketStore) GetAnalyticsHashSizes(region string) map[string]interface{} {
	s.cacheMu.Lock()
	if cached, ok := s.hashCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsHashSizes(region)

	// Add multi-byte capability data (only for unfiltered/global view)
	if region == "" {
		result["multiByteCapability"] = s.computeMultiByteCapability()
	}

	s.cacheMu.Lock()
	s.hashCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsHashSizes(region string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
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

		// Track originator from advert packets (including zero-hop adverts,
		// keyed by pubKey so same-name nodes don't merge).
		if tx.PayloadType != nil && *tx.PayloadType == 4 && tx.DecodedJSON != "" {
			var d map[string]interface{}
			if json.Unmarshal([]byte(tx.DecodedJSON), &d) == nil {
				pk := ""
				if v, ok := d["pubKey"].(string); ok {
					pk = v
				} else if v, ok := d["public_key"].(string); ok {
					pk = v
				}
				if pk != "" {
					name := ""
					if n, ok := d["name"].(string); ok {
						name = n
					}
					if name == "" {
						if len(pk) >= 8 {
							name = pk[:8]
						} else {
							name = pk
						}
					}
					if byNode[pk] == nil {
						role := nodeRoleByPK[pk] // empty if unknown
						byNode[pk] = map[string]interface{}{
							"hashSize": hashSize, "packets": 0,
							"lastSeen": tx.FirstSeen, "name": name,
							"role": role,
						}
					}
					byNode[pk]["packets"] = byNode[pk]["packets"].(int) + 1
					byNode[pk]["hashSize"] = hashSize
					byNode[pk]["lastSeen"] = tx.FirstSeen
				}
			}
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
	for _, data := range byNode {
		role, _ := data["role"].(string)
		if !strings.Contains(strings.ToLower(role), "repeater") {
			continue
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
func (s *PacketStore) GetAnalyticsHashCollisions(region string) map[string]interface{} {
	s.cacheMu.Lock()
	if cached, ok := s.collisionCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeHashCollisions(region)

	s.cacheMu.Lock()
	s.collisionCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.collisionCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

// collisionNode is a lightweight node representation for collision analysis.
type collisionNode struct {
	PublicKey          string `json:"public_key"`
	Name               string `json:"name"`
	Role               string `json:"role"`
	Lat                float64 `json:"lat"`
	Lon                float64 `json:"lon"`
	HashSize           int    `json:"hash_size"`
	HashSizeInconsistent bool `json:"hash_size_inconsistent"`
	HashSizesSeen      []int  `json:"hash_sizes_seen,omitempty"`
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
	GroupNodes      []collisionNode            `json:"group_nodes"`
	TwoByteMap      map[string][]collisionNode `json:"two_byte_map"`
	MaxCollision    int                         `json:"max_collision"`
	CollisionCount  int                         `json:"collision_count"`
}

func (s *PacketStore) computeHashCollisions(region string) map[string]interface{} {
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
					var d map[string]interface{}
					if json.Unmarshal([]byte(tx.DecodedJSON), &d) == nil {
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

	info := make(map[string]*hashSizeNodeInfo)

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
		pathByte, err := strconv.ParseUint(tx.RawHex[2:4], 16, 8)
		if err != nil {
			continue
		}
		// DIRECT zero-hop adverts use path byte 0x00 locally and can misreport
		// multibyte repeater hash mode as 1-byte.
		if routeType == RouteDirect && (pathByte&0x3F) == 0 {
			continue
		}
		hs := int((pathByte>>6)&0x3) + 1

		var d map[string]interface{}
		if json.Unmarshal([]byte(tx.DecodedJSON), &d) != nil {
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

		ni := info[pk]
		if ni == nil {
			ni = &hashSizeNodeInfo{AllSizes: make(map[int]bool)}
			info[pk] = ni
		}
		ni.AllSizes[hs] = true
		ni.Seq = append(ni.Seq, hs)
	}

	// Post-process: use latest advert hash size and compute flip-flop flag.
	// The most recent advert reflects the node's current hash size
	// configuration. The upstream firmware bug causing stale path bytes in
	// flood adverts was fixed (meshcore-dev/MeshCore#2154).
	for _, ni := range info {
		// Use the most recent advert's hash size (last in chronological order).
		ni.HashSize = ni.Seq[len(ni.Seq)-1]

		// Flip-flop (inconsistent) flag: need >= 3 observations,
		// >= 2 unique sizes, and >= 2 transitions in the sequence.
		if len(ni.Seq) < 3 || len(ni.AllSizes) < 2 {
			continue
		}
		transitions := 0
		for i := 1; i < len(ni.Seq); i++ {
			if ni.Seq[i] != ni.Seq[i-1] {
				transitions++
			}
		}
		ni.Inconsistent = transitions >= 2
	}

	return info
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

// --- Multi-Byte Capability Inference ---

// MultiByteCapEntry represents a repeater's inferred multi-byte capability.
type MultiByteCapEntry struct {
	PublicKey   string `json:"pubkey"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Status      string `json:"status"`      // "confirmed", "suspected", "unknown"
	Evidence    string `json:"evidence"`     // "advert", "path", ""
	MaxHashSize int    `json:"maxHashSize"`
	LastSeen    string `json:"lastSeen"`
}

// computeMultiByteCapability determines multi-byte capability for each
// repeater using two methods:
//
// 1. Confirmed: the node has advertised with hash_size >= 2 (from advert
//    path byte). This is 100% reliable because the full public key is
//    received in adverts — no prefix collision ambiguity.
//
// 2. Suspected: the node's prefix appears as a hop in a packet whose path
//    header indicates hash_size >= 2. This is <100% reliable because
//    2-byte prefixes can collide — two different nodes may share the same
//    prefix. If one is confirmed multi-byte and the other is not, the
//    non-confirmed one could be a false positive.
//
// 3. Unknown: node has only been seen with 1-byte adverts and no
//    multi-byte path appearances. Could be pre-1.14 firmware or 1.14+
//    with default (1-byte) settings.
//
// Caller must hold NO locks — this method acquires mu.RLock internally.
func (s *PacketStore) computeMultiByteCapability() []MultiByteCapEntry {
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
	repeaterPrefixes := make(map[string][]prefixEntry) // prefix → entries
	for pk, n := range nodeByPK {
		if !strings.Contains(strings.ToLower(n.Role), "repeater") {
			continue
		}
		// Generate 1-byte, 2-byte, 3-byte prefixes
		pkLower := strings.ToLower(pk)
		for byteLen := 1; byteLen <= 3; byteLen++ {
			hexLen := byteLen * 2
			if len(pkLower) >= hexLen {
				pfx := pkLower[:hexLen]
				repeaterPrefixes[pfx] = append(repeaterPrefixes[pfx], prefixEntry{pk, pfx})
			}
		}
	}

	suspected := make(map[string]int) // pubkey → max hash size from path appearances
	for pfx, entries := range repeaterPrefixes {
		txList := s.byPathHop[pfx]
		for _, tx := range txList {
			if tx.RawHex == "" || len(tx.RawHex) < 4 {
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

	// Build result for all repeaters — fetch last_seen from DB
	dbLastSeen := make(map[string]string)
	rows, err := s.db.conn.Query("SELECT public_key, last_seen FROM nodes WHERE role LIKE '%repeater%'")
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
		if !strings.Contains(strings.ToLower(n.Role), "repeater") {
			continue
		}
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

func (s *PacketStore) GetBulkHealth(limit int, region string) []map[string]interface{} {
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

	// Get nodes from DB
	queryLimit := limit
	if regionNodeKeys != nil {
		queryLimit = 500
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
		nodes = append(nodes, dbNode{
			pk: pk, name: nullStrVal(name), role: nullStrVal(role),
			lat: nullFloat(lat), lon: nullFloat(lon),
		})
		if regionNodeKeys == nil && len(nodes) >= limit {
			break
		}
	}
	if regionNodeKeys != nil && len(nodes) > limit {
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
		p := txToMap(packets[i])
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

	name := ""
	if n, ok := node["name"]; ok && n != nil {
		name = fmt.Sprintf("%v", n)
	}

	fromTime := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	fromISO := fromTime.Format(time.RFC3339)
	toISO := time.Now().Format(time.RFC3339)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect packets from byNode index + text search (matches Node.js findPacketsForNode)
	indexed := s.byNode[pubkey]
	hashSet := make(map[string]bool, len(indexed))
	for _, tx := range indexed {
		hashSet[tx.Hash] = true
	}
	var packets []*StoreTx
	if name != "" {
		for _, tx := range s.packets {
			if tx.FirstSeen <= fromISO {
				continue // Skip old packets early before expensive string matching
			}
			if hashSet[tx.Hash] {
				packets = append(packets, tx)
			} else if tx.DecodedJSON != "" && (strings.Contains(tx.DecodedJSON, name) || strings.Contains(tx.DecodedJSON, pubkey)) {
				packets = append(packets, tx)
			}
		}
	} else {
		// Filter indexed packets by time range
		for _, p := range indexed {
			if p.FirstSeen > fromISO {
				packets = append(packets, p)
			}
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
		var decoded map[string]interface{}
		if json.Unmarshal([]byte(p.DecodedJSON), &decoded) != nil {
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
	}, nil
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
	hopCache := make(map[string]*nodeInfo)
	resolveHop := func(hop string) string {
		if cached, ok := hopCache[hop]; ok {
			if cached != nil {
				return cached.Name
			}
			return hop
		}
		r, _, _ := pm.resolveWithContext(hop, nil, s.graph)
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
	hopCache := make(map[string]*nodeInfo)
	resolveHop := func(hop string) string {
		if cached, ok := hopCache[hop]; ok {
			if cached != nil {
				return cached.Name
			}
			return hop
		}
		r, _, _ := pm.resolveWithContext(hop, nil, s.graph)
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

	// Resolve the requested hops
	nodes := make([]map[string]interface{}, len(rawHops))
	for i, hop := range rawHops {
		r, _, _ := pm.resolveWithContext(hop, nil, s.graph)
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

	// Build the subpath key the same way the index does (lowercase, comma-joined)
	spKey := strings.ToLower(strings.Join(rawHops, ","))

	// Direct lookup instead of scanning all packets
	matchedTxs := s.spTxIndex[spKey]

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

		// Full parent path (resolved)
		hops := txGetParsedPath(tx)
		resolved := make([]string, len(hops))
		for i, h := range hops {
			r, _, _ := pm.resolveWithContext(h, nil, s.graph)
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
