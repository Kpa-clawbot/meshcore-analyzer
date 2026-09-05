package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/prunequeue"
	"golang.org/x/sync/singleflight"
)

// memBreakdownNote is the static accounting caveat attached to the opt-in
// /api/perf?mem=1 store memory breakdown (PerfResponse.MemoryBreakdownNote).
const memBreakdownNote = "Per-component *MB count string content + one Go string header each and are a deliberate upper bound (the header is also in the *EstimatedMB base figures). struct/index/map overhead, the neighbor graph and analytics caches are excluded, so the totals sit below goHeapInuseMB. floodTxSharePct sizes the flood-forward multiplication: each flood hop is stored as its own transmission."

// Server holds shared state for route handlers.
type Server struct {
	db        *DB
	cfg       *Config
	hub       *Hub
	store     *PacketStore // in-memory packet store (nil = fallback to DB)
	configDir string       // directory containing config.json (for write-back via PUT /api/config/geo-filter)
	startedAt time.Time
	perfStats *PerfStats
	version   string
	commit    string
	buildTime string

	// Cached runtime.MemStats to avoid stop-the-world pauses on every health check
	memStatsMu       sync.Mutex
	memStatsCache    runtime.MemStats
	memStatsCachedAt time.Time

	// Cached /api/stats response — recomputed at most once every 10s
	statsMu       sync.Mutex
	statsCache    *StatsResponse
	statsCachedAt time.Time
	// #1910: collapses concurrent rebuilds. The cache check below releases
	// statsMu before building the response, so every request arriving while the
	// 10s window was expired used to rebuild it in full, each running its own DB
	// queries against a 4-connection pool.
	statsSF singleflight.Group

	// Guards s.cfg.GeoFilter — read by ingest/handler goroutines, written by PUT handler
	cfgMu sync.RWMutex

	// Serializes concurrent PUT /api/config/geo-filter disk writes so requests
	// can't race on the .tmp file or interleave disk/memory updates.
	saveMu sync.Mutex

	// Neighbor affinity graph (lazy-built, cached with TTL)
	neighborMu    sync.Mutex
	neighborGraph *NeighborGraph

	// Cached /api/scope-stats response — per-window, recomputed at most once every 30s
	scopeStatsMu       sync.Mutex
	scopeStatsCache    map[string]*ScopeStatsResponse
	scopeStatsCachedAt map[string]time.Time

	// #1975: cached /api/scope-audit response, per window, recomputed at most
	// once every 30s. Mirrors the scopeStats cache directly above it.
	scopeAuditMu       sync.Mutex
	scopeAuditCache    map[string]*ScopeAuditResponse
	scopeAuditCachedAt map[string]time.Time

	// #1975: /api/scope-audit window cache and its single-flight guard, so a
	// burst of viewers on a cold cache recomputes the network-wide scan once
	// rather than once per request. Lives in scope_audit.go.
	scopes scopesState

	// Router reference for OpenAPI spec generation
	router *mux.Router

	// Cached default (no-filter) /api/observers response, served from an
	// atomic-pointer snapshot. Refilled via singleflight on TTL boundary
	// to prevent thundering-herd SQL stampedes. Issue #1481 P0-3 +
	// #1483 follow-up (singleflight + monotonic time).
	observersCacheV2 observersCacheField

	// Cached default-shape /api/analytics/neighbor-graph response,
	// recomputed every 5 min in a background goroutine. Issue #1481 P0-1.
	neighborGraphCache neighborGraphCacheField

	// Counter for rebuild-panic events on the neighbor-graph cache
	// background recomputer. Surfaced via /api/stats. #1483 follow-up.
	neighborGraphCacheRebuildFailures uint64

	// Test injection: when non-nil, replaces the real
	// computeNeighborGraphResponse pipeline so tests can assert the
	// bypass branch was exercised without standing up a full DB/store.
	// Production code MUST leave this nil. #1483 follow-up.
	computeNeighborGraphResponseFn func(minCount int, minScore float64, region, role string) NeighborGraphResponse

	// Per-server state for /api/nodes/{pk}/reach: TTL cache + singleflight
	// + cached neighbor_edges degree snapshot. Lives on *Server (not as
	// package globals) so multiple instances don't share observable
	// state. Initialised lazily on first use; see node_reach.go.
	reach reachState

	// Known-channels catalogue cache (issue #1323). Nil until configured;
	// when nil the /api/known-channels endpoint returns an empty snapshot.
	knownChannels *knownChannelsCache
}

// PerfStats tracks request performance.
type PerfStats struct {
	mu          sync.Mutex
	Requests    int64
	TotalMs     float64
	Endpoints   map[string]*EndpointPerf
	SlowQueries []SlowQuery
	StartedAt   time.Time
}

type EndpointPerf struct {
	Count   int
	TotalMs float64
	MaxMs   float64
	Recent  []float64
}

func NewPerfStats() *PerfStats {
	return &PerfStats{
		Endpoints:   make(map[string]*EndpointPerf),
		SlowQueries: make([]SlowQuery, 0),
		StartedAt:   time.Now(),
	}
}

func NewServer(db *DB, cfg *Config, hub *Hub) *Server {
	if cfg != nil {
		cfg.applyListLimitsDefaults()
	}
	return &Server{
		db:        db,
		cfg:       cfg,
		hub:       hub,
		startedAt: time.Now(),
		perfStats: NewPerfStats(),
		version:   resolveVersion(),
		commit:    resolveCommit(),
		buildTime: resolveBuildTime(),
	}
}

const memStatsTTL = 5 * time.Second

// getMemStats returns cached runtime.MemStats, refreshing at most every 5 seconds.
// runtime.ReadMemStats() stops the world; caching prevents per-request GC pauses.
func (s *Server) getMemStats() runtime.MemStats {
	s.memStatsMu.Lock()
	defer s.memStatsMu.Unlock()
	if time.Since(s.memStatsCachedAt) > memStatsTTL {
		runtime.ReadMemStats(&s.memStatsCache)
		s.memStatsCachedAt = time.Now()
	}
	return s.memStatsCache
}

// getGeoFilter returns a pointer to the current geo_filter config under read lock.
// Callers MUST NOT mutate the returned struct.
// isPubkeyHidden returns true if the node with the given pubkey has a name
// matching an operator-configured hidden prefix (#1181). Mirrors the
// IsBlacklisted check used at the top of per-pubkey handlers: per-pubkey
// endpoints should 404 on hidden nodes so callers learn nothing about
// whether the row exists. Returns false on DB error / missing row (the
// downstream handler's own 404 covers those).
func (s *Server) isPubkeyHidden(pubkey string) bool {
	if s == nil || s.cfg == nil || len(s.cfg.HiddenNamePrefixes) == 0 {
		return false
	}
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		return false
	}
	name, _ := node["name"].(string)
	return s.cfg.IsNameHidden(name)
}

func (s *Server) getGeoFilter() *GeoFilterConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.GeoFilter
}

// setGeoFilter atomically swaps the geo_filter config; used by PUT /api/config/geo-filter.
func (s *Server) setGeoFilter(gf *GeoFilterConfig) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg.GeoFilter = gf
}

// RegisterRoutes sets up all HTTP routes on the given router.
func (s *Server) RegisterRoutes(r *mux.Router) {
	s.router = r
	// CORS middleware (must run before route handlers)
	r.Use(s.corsMiddleware)

	// Performance instrumentation middleware
	r.Use(s.perfMiddleware)

	// /api/* responses must not be cached by upstream CDNs (#1551).
	// Cloudflare/nginx/Varnish default zone policies cache
	// application/json for 15min–4h when no Cache-Control is set,
	// causing operators behind a CDN to serve stale observers/packets/
	// stats data. Scope: /api/ prefix only — static assets stay
	// CDN-cacheable (their headers are set by spaHandler).
	r.Use(noStoreAPIMiddleware)

	// Detect CDN-fronted deployments and warn the operator ONCE if
	// any CDN-typical header (CF-Ray, CF-Connecting-IP, etc.) is
	// observed. See #1561: no-store alone isn't sufficient on
	// Cloudflare zones with Cache Rules / Page Rules that ignore
	// origin Cache-Control. Operator must add a Bypass Cache rule
	// for /api/* — see docs/deployment-behind-cdn.md.
	r.Use(cdnDetectionMiddleware)

	// Config endpoints
	r.HandleFunc("/api/config/cache", s.handleConfigCache).Methods("GET")
	r.HandleFunc("/api/config/client", s.handleConfigClient).Methods("GET")
	r.HandleFunc("/api/config/regions", s.handleConfigRegions).Methods("GET")
	r.HandleFunc("/api/config/theme", s.handleConfigTheme).Methods("GET")
	r.HandleFunc("/api/config/map", s.handleConfigMap).Methods("GET")
	r.HandleFunc("/api/config/geo-filter", s.handleConfigGeoFilter).Methods("GET")
	r.HandleFunc("/api/config/areas", s.handleConfigAreas).Methods("GET")
	r.HandleFunc("/api/config/areas/polygons", s.handleConfigAreasPolygons).Methods("GET")
	r.Handle("/api/config/geo-filter", s.requireAPIKey(http.HandlerFunc(s.handlePutConfigGeoFilter))).Methods("PUT")

	// Readiness endpoint (gated on background init completion)
	r.HandleFunc("/api/healthz", s.handleHealthz).Methods("GET")

	// System endpoints
	r.HandleFunc("/api/health", s.handleHealth).Methods("GET")
	r.HandleFunc("/api/stats", s.handleStats).Methods("GET")
	r.HandleFunc("/api/scope-stats", s.handleScopeStats).Methods("GET")
	r.HandleFunc("/api/scope-audit", s.handleScopeAudit).Methods("GET") // #1975
	r.HandleFunc("/api/perf", s.handlePerf).Methods("GET")
	r.HandleFunc("/api/perf/io", s.handlePerfIO).Methods("GET")
	r.HandleFunc("/api/perf/sqlite", s.handlePerfSqlite).Methods("GET")
	r.HandleFunc("/api/perf/write-sources", s.handlePerfWriteSources).Methods("GET")
	r.HandleFunc("/api/mqtt/status", s.handleMqttStatus).Methods("GET")
	r.Handle("/api/perf/reset", s.requireAPIKey(http.HandlerFunc(s.handlePerfReset))).Methods("POST")
	// /api/admin/prune removed in #1283 — pruning is owned by the
	// ingestor process (scheduled tickers + startup pass). Operators
	// who want an ad-hoc prune can restart the ingestor.
	//
	// /api/admin/prune-geo-filter (#669 M4 / PR #738): server enqueues a
	// marker file; the ingestor (which holds the writable DB handle)
	// runs the DELETE. /status reports completion.
	r.Handle("/api/admin/prune-geo-filter", s.requireAPIKey(http.HandlerFunc(s.handlePruneGeoFilter))).Methods("POST")
	r.Handle("/api/admin/prune-geo-filter/status", s.requireAPIKey(http.HandlerFunc(s.handlePruneGeoFilterStatus))).Methods("GET")
	r.Handle("/api/debug/affinity", s.requireAPIKey(http.HandlerFunc(s.handleDebugAffinity))).Methods("GET")
	r.Handle("/api/dropped-packets", s.requireAPIKey(http.HandlerFunc(s.handleDroppedPackets))).Methods("GET")
	r.Handle("/api/backup", s.requireAPIKey(http.HandlerFunc(s.handleBackup))).Methods("GET")

	// Packet endpoints
	r.HandleFunc("/api/packets/observations", s.handleBatchObservations).Methods("POST")
	r.HandleFunc("/api/packets/timestamps", s.handlePacketTimestamps).Methods("GET")
	r.HandleFunc("/api/packets/{id}", s.handlePacketDetail).Methods("GET")
	r.HandleFunc("/api/packets", s.handlePackets).Methods("GET")

	// Decode endpoint
	r.HandleFunc("/api/decode", s.handleDecode).Methods("POST")

	// Node endpoints — fixed routes BEFORE parameterized
	r.HandleFunc("/api/nodes/search", s.handleNodeSearch).Methods("GET")
	r.HandleFunc("/api/nodes/bulk-health", s.handleBulkHealth).Methods("GET")
	r.HandleFunc("/api/nodes/network-status", s.handleNetworkStatus).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/health", s.handleNodeHealth).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/paths", s.handleNodePaths).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/analytics", s.handleNodeAnalytics).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/battery", s.handleNodeBattery).Methods("GET")
	r.HandleFunc("/api/nodes/clock-skew", s.handleFleetClockSkew).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/clock-skew", s.handleNodeClockSkew).Methods("GET")
	r.HandleFunc("/api/observers/clock-skew", s.handleObserverClockSkew).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/neighbors", s.handleNodeNeighbors).Methods("GET")
	// Keep specific sub-routes (…/reach, …/rx-coverage) registered BEFORE the
	// catch-all /api/nodes/{pubkey} — mux matches in registration order, so
	// reordering these below the catch-all would shadow them and break the route.
	r.HandleFunc("/api/nodes/{pubkey}/reach", s.handleNodeReach).Methods("GET")
	// Coverage routes are always registered; each handler 404s when the opt-in
	// clientRxCoverage flag is off (a clean 404 rather than the SPA fallback that
	// an unregistered /api route would hit). See requireClientRxCoverage.
	r.HandleFunc("/api/nodes/{pubkey}/rx-coverage", s.handleNodeRxCoverage).Methods("GET")
	r.HandleFunc("/api/nodes/resolve", s.handleResolvePrefix).Methods("GET")
	r.HandleFunc("/api/rx-coverage", s.handleRxCoverage).Methods("GET")
	r.HandleFunc("/api/rx-leaderboard", s.handleRxLeaderboard).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}", s.handleNodeDetail).Methods("GET")
	r.HandleFunc("/api/nodes", s.handleNodes).Methods("GET")

	// Analytics endpoints
	r.HandleFunc("/api/analytics/roles", s.handleAnalyticsRoles).Methods("GET")
	r.HandleFunc("/api/analytics/rf", s.handleAnalyticsRF).Methods("GET")
	r.HandleFunc("/api/analytics/topology", s.handleAnalyticsTopology).Methods("GET")
	r.HandleFunc("/api/analytics/channels", s.handleAnalyticsChannels).Methods("GET")
	r.HandleFunc("/api/analytics/distance", s.handleAnalyticsDistance).Methods("GET")
	r.HandleFunc("/api/analytics/hash-sizes", s.handleAnalyticsHashSizes).Methods("GET")
	r.HandleFunc("/api/analytics/hash-collisions", s.handleAnalyticsHashCollisions).Methods("GET")
	r.HandleFunc("/api/analytics/subpaths", s.handleAnalyticsSubpaths).Methods("GET")
	r.HandleFunc("/api/analytics/subpaths-bulk", s.handleAnalyticsSubpathsBulk).Methods("GET")
	r.HandleFunc("/api/analytics/subpath-detail", s.handleAnalyticsSubpathDetail).Methods("GET")
	r.HandleFunc("/api/analytics/neighbor-graph", s.handleNeighborGraph).Methods("GET")
	r.HandleFunc("/api/analytics/relay-airtime-share", s.handleAnalyticsRelayAirtimeShare).Methods("GET")

	// Other endpoints
	r.HandleFunc("/api/resolve-hops", s.handleResolveHops).Methods("GET")
	r.HandleFunc("/api/channels/{hash}/messages", s.handleChannelMessages).Methods("GET")
	r.HandleFunc("/api/channels", s.handleChannels).Methods("GET")
	r.HandleFunc("/api/known-channels", s.handleKnownChannels).Methods("GET")
	r.HandleFunc("/api/observers/metrics/summary", s.handleMetricsSummary).Methods("GET")
	r.HandleFunc("/api/observers/{id}/metrics", s.handleObserverMetrics).Methods("GET")
	r.HandleFunc("/api/observers/{id}/analytics", s.handleObserverAnalytics).Methods("GET")
	r.HandleFunc("/api/observers/{id}", s.handleObserverDetail).Methods("GET")
	r.HandleFunc("/api/observers", s.handleObservers).Methods("GET")
	r.HandleFunc("/api/traces/{hash}", s.handleTraces).Methods("GET")
	r.HandleFunc("/api/paths/inspect", s.handlePathInspect).Methods("POST")
	r.HandleFunc("/api/iata-coords", s.handleIATACoords).Methods("GET")
	r.HandleFunc("/api/audio-lab/buckets", s.handleAudioLabBuckets).Methods("GET")

	// OpenAPI spec + Swagger UI
	r.HandleFunc("/api/spec", s.handleOpenAPISpec).Methods("GET")
	r.HandleFunc("/api/docs", s.handleSwaggerUI).Methods("GET")
}

// noStoreAPIMiddleware sets Cache-Control: no-store on every response
// whose request path starts with /api/. See #1551 — CDNs cache JSON
// for minutes when no Cache-Control is present, which causes observers/
// packets/stats responses to go stale through Cloudflare/nginx/Varnish.
//
// Why no-store (not private,max-age=0):
//   - no-store is the most conservative directive; forbids ANY cache
//     (CDN, browser, intermediary) from storing the response.
//   - private,max-age=0 still permits short browser caches and some
//     intermediaries; we don't gain anything from it because the data
//     is fresh-on-every-request semantics by contract (WS pushes diff
//     against REST GETs).
//
// Scope: /api/ prefix only. Static assets (HTML/JS/CSS) keep their
// existing headers from spaHandler and remain CDN-cacheable on
// hashed URLs.
func noStoreAPIMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) perfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		ms := float64(time.Since(start).Microseconds()) / 1000.0

		// Normalize key outside lock (no shared state needed)
		key := r.URL.Path
		if route := mux.CurrentRoute(r); route != nil {
			if tmpl, err := route.GetPathTemplate(); err == nil {
				key = muxBraceParam.ReplaceAllString(tmpl, ":$1")
			}
		}
		if key == r.URL.Path {
			key = perfHexFallback.ReplaceAllString(key, ":id")
		}

		s.perfStats.mu.Lock()
		s.perfStats.Requests++
		s.perfStats.TotalMs += ms

		if _, ok := s.perfStats.Endpoints[key]; !ok {
			s.perfStats.Endpoints[key] = &EndpointPerf{Recent: make([]float64, 0, 100)}
		}
		ep := s.perfStats.Endpoints[key]
		ep.Count++
		ep.TotalMs += ms
		if ms > ep.MaxMs {
			ep.MaxMs = ms
		}
		ep.Recent = append(ep.Recent, ms)
		if len(ep.Recent) > 100 {
			ep.Recent = ep.Recent[1:]
		}
		if ms > 100 {
			slow := SlowQuery{
				Path:   r.URL.Path,
				Ms:     round(ms, 1),
				Time:   time.Now().UTC().Format(time.RFC3339),
				Status: 200,
			}
			s.perfStats.SlowQueries = append(s.perfStats.SlowQueries, slow)
			if len(s.perfStats.SlowQueries) > 50 {
				s.perfStats.SlowQueries = s.perfStats.SlowQueries[1:]
			}
		}
		s.perfStats.mu.Unlock()
	})
}

func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg == nil || s.cfg.APIKey == "" {
			writeError(w, http.StatusForbidden, "write endpoints disabled — set apiKey in config.json")
			return
		}
		key := r.Header.Get("X-API-Key")
		if !constantTimeEqual(key, s.cfg.APIKey) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if IsWeakAPIKey(key) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- Config Handlers ---

func (s *Server) handleConfigCache(w http.ResponseWriter, r *http.Request) {
	ct := s.cfg.CacheTTL
	if ct == nil {
		ct = map[string]interface{}{}
	}
	writeJSON(w, ct) // CacheTTL is user-provided opaque config — map is appropriate
}

func (s *Server) handleConfigClient(w http.ResponseWriter, r *http.Request) {
	// #1508 — surface the operator-side customizer knobs. The frontend
	// (public/customize-v2.js _renderTabs) reads disabledTabs to hide
	// admin-only tabs from end users. Always return a non-nil slice so
	// the JSON shape is `[]` (not `null`) and the client can call
	// `.includes()` without an undefined guard.
	disabledTabs := []string{}
	if s.cfg.Customizer != nil && s.cfg.Customizer.DisabledTabs != nil {
		disabledTabs = s.cfg.Customizer.DisabledTabs
	}
	pathTrust := s.cfg.GetPathTrust()
	writeJSON(w, ClientConfigResponse{
		Roles:               s.cfg.Roles,
		HealthThresholds:    s.cfg.GetHealthThresholds().ToClientMs(),
		Map:                 s.cfg.Map,
		SnrThresholds:       s.cfg.SnrThresholds,
		DistThresholds:      s.cfg.DistThresholds,
		MaxHopDist:          s.cfg.MaxHopDist,
		Limits:              s.cfg.Limits,
		PerfSlowMs:          s.cfg.PerfSlowMs,
		WsReconnectMs:       s.cfg.WsReconnectMs,
		CacheInvalidateMs:   s.cfg.CacheInvalidMs,
		ExternalUrls:        s.cfg.ExternalUrls,
		PropagationBufferMs: float64(s.cfg.PropagationBufferMs()),
		LiveMapMaxNodes:     s.cfg.LiveMapMaxNodes(),
		Timestamps:          s.cfg.GetTimestampConfig(),
		DebugAffinity:       s.cfg.DebugAffinity,
		MapDarkTileProvider: s.cfg.MapDarkTileProvider,
		Tiles:               s.cfg.Tiles,
		Customizer:          CustomizerClientConfig{DisabledTabs: disabledTabs},
		ClientRxCoverage:    s.cfg.ClientRxCoverageEnabled(),
		PathTrust:           &pathTrust,
	})
}

func (s *Server) handleConfigAreas(w http.ResponseWriter, r *http.Request) {
	type areaListEntry struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	result := make([]areaListEntry, 0, len(s.cfg.Areas))
	for k, v := range s.cfg.Areas {
		if v.Label == "" {
			continue // skip comment/invalid entries (e.g. "_comment" keys in config)
		}
		result = append(result, areaListEntry{Key: k, Label: v.Label})
	}
	writeJSON(w, result)
}

func (s *Server) handleConfigAreasPolygons(w http.ResponseWriter, r *http.Request) {
	type areaDebugEntry struct {
		Key     string       `json:"key"`
		Label   string       `json:"label"`
		Polygon [][2]float64 `json:"polygon,omitempty"`
		LatMin  *float64     `json:"latMin,omitempty"`
		LatMax  *float64     `json:"latMax,omitempty"`
		LonMin  *float64     `json:"lonMin,omitempty"`
		LonMax  *float64     `json:"lonMax,omitempty"`
	}
	result := make([]areaDebugEntry, 0, len(s.cfg.Areas))
	for k, v := range s.cfg.Areas {
		result = append(result, areaDebugEntry{
			Key:     k,
			Label:   v.Label,
			Polygon: v.Polygon,
			LatMin:  v.LatMin,
			LatMax:  v.LatMax,
			LonMin:  v.LonMin,
			LonMax:  v.LonMax,
		})
	}
	writeJSON(w, result)
}

func (s *Server) handleConfigRegions(w http.ResponseWriter, r *http.Request) {
	regions := make(map[string]string)
	for k, v := range s.cfg.Regions {
		regions[k] = v
	}
	codes, _ := s.db.GetDistinctIATAs()
	for _, c := range codes {
		if _, ok := regions[c]; !ok {
			regions[c] = c
		}
	}
	writeJSON(w, regions)
}

func (s *Server) handleConfigTheme(w http.ResponseWriter, r *http.Request) {
	theme := LoadTheme(".")

	branding := mergeMap(map[string]interface{}{
		"siteName": "CoreScope",
		"tagline":  "Real-time MeshCore LoRa mesh network analyzer",
	}, s.cfg.Branding, theme.Branding)

	themeColors := mergeMap(map[string]interface{}{
		"accent":       "#4a9eff",
		"accentHover":  "#6db3ff",
		"navBg":        "#0f0f23",
		"navBg2":       "#1a1a2e",
		"navText":      "#ffffff",
		"navTextMuted": "#cbd5e1",
		"background":   "#f4f5f7",
		"text":         "#1a1a2e",
		"textMuted":    "#5b6370",
		"border":       "#e2e5ea",
		"surface1":     "#ffffff",
		"surface2":     "#ffffff",
		"surface3":     "#ffffff",
		"sectionBg":    "#eef2ff",
		"cardBg":       "#ffffff",
		"contentBg":    "#f4f5f7",
		"detailBg":     "#ffffff",
		"inputBg":      "#ffffff",
		"rowStripe":    "#f9fafb",
		"rowHover":     "#eef2ff",
		"selectedBg":   "#dbeafe",
		"statusGreen":  "#22c55e",
		"statusYellow": "#eab308",
		"statusRed":    "#ef4444",
	}, s.cfg.Theme, theme.Theme)

	nodeColors := mergeMap(map[string]interface{}{
		"repeater":  "#dc2626",
		"companion": "#2563eb",
		"room":      "#16a34a",
		"sensor":    "#d97706",
		"observer":  "#8b5cf6",
	}, s.cfg.NodeColors, theme.NodeColors)

	themeDark := mergeMap(map[string]interface{}{
		"accent":       "#4a9eff",
		"accentHover":  "#6db3ff",
		"navBg":        "#0f0f23",
		"navBg2":       "#1a1a2e",
		"navText":      "#ffffff",
		"navTextMuted": "#cbd5e1",
		"background":   "#0f0f23",
		"text":         "#e2e8f0",
		"textMuted":    "#a8b8cc",
		"border":       "#334155",
		"surface1":     "#1a1a2e",
		"surface2":     "#232340",
		"cardBg":       "#1a1a2e",
		"contentBg":    "#0f0f23",
		"detailBg":     "#232340",
		"inputBg":      "#1e1e34",
		"rowStripe":    "#1e1e34",
		"rowHover":     "#2d2d50",
		"selectedBg":   "#1e3a5f",
		"statusGreen":  "#22c55e",
		"statusYellow": "#eab308",
		"statusRed":    "#ef4444",
		"surface3":     "#2d2d50",
		"sectionBg":    "#1e1e34",
	}, s.cfg.ThemeDark, theme.ThemeDark)
	// #1799 PR #1804 r1 item 6: REQUEST→REQ rename is a BREAKING change to
	// the /api/config/theme shape. Compat policy for >=1 release cycle:
	//   - INCOMING: if an operator's config.json / theme.json carries the
	//     legacy "REQUEST" key, normalise it to "REQ" before mergeMap so
	//     the override wins the canonical slot.
	//   - OUTGOING: dual-emit REQ AND REQUEST in the GET response so any
	//     consumer still reading the legacy key keeps working.
	typeColors := mergeMap(map[string]interface{}{
		"ADVERT":   "#22c55e",
		"GRP_TXT":  "#3b82f6",
		"TXT_MSG":  "#f59e0b",
		"ACK":      "#6b7280",
		"REQ":      "#a855f7",
		"RESPONSE": "#06b6d4",
		"TRACE":    "#ec4899",
		"PATH":     "#14b8a6",
		"ANON_REQ": "#f43f5e",
		"UNKNOWN":  "#6b7280",
	}, normaliseTypeColorsLegacyKeys(s.cfg.TypeColors), normaliseTypeColorsLegacyKeys(theme.TypeColors))
	// Dual-emit REQUEST = REQ for back-compat (drop after >=1 release cycle).
	if v, ok := typeColors["REQ"]; ok {
		typeColors["REQUEST"] = v
	}

	defaultHome := map[string]interface{}{
		"heroTitle":    "CoreScope",
		"heroSubtitle": "Real-time MeshCore LoRa mesh network analyzer",
		"steps": []interface{}{
			// #1648 M5: defaults use 'ph:<name>' Phosphor sprite tokens.
			// The frontend render path (home.js _renderHomeGlyph, customize-v2.js
			// renderConfigGlyph) ALSO accepts legacy emoji strings, so any
			// operator config.json that still stores raw emoji values continues
			// to render as-is — this changes ONLY the built-in default set.
			map[string]interface{}{"emoji": "ph:bluetooth", "title": "Connect via Bluetooth", "description": "Flash **BLE companion** firmware from [MeshCore Flasher](https://flasher.meshcore.io/).\n- Screenless devices: default PIN `123456`\n- Screen devices: random PIN shown on display\n- If pairing fails: forget device, reboot, re-pair"},
			map[string]interface{}{"emoji": "ph:radio", "title": "Set the right frequency preset", "description": "**US Recommended:**\n`910.525 MHz · BW 62.5 kHz · SF 7 · CR 5`\nSelect **\"US Recommended\"** in the app or flasher."},
			map[string]interface{}{"emoji": "ph:broadcast", "title": "Advertise yourself", "description": "Tap the signal icon → **Flood** to broadcast your node to the mesh. Companions only advert when you trigger it manually."},
			map[string]interface{}{"emoji": "ph:repeat", "title": "Check \"Heard N repeats\"", "description": "- **\"Sent\"** = transmitted, no confirmation\n- **\"Heard 0 repeats\"** = no repeater picked it up\n- **\"Heard 1+ repeats\"** = you're on the mesh!"},
		},
		"footerLinks": []interface{}{
			map[string]interface{}{"label": "ph:package Packets", "url": "#/packets"},
			map[string]interface{}{"label": "ph:map-trifold Network Map", "url": "#/map"},
		},
	}
	home := mergeMap(defaultHome, s.cfg.Home, theme.Home)

	// #1488 — marker stroke overlay. Defaults mirror the :root values in
	// public/style.css so a fresh visitor with no config + no override
	// still gets the same painted outline as the static CSS fallback.
	markerStroke := mergeMap(map[string]interface{}{
		"color":   "rgba(255,255,255,0.85)",
		"width":   1,
		"opacity": 1,
	}, s.cfg.MarkerStroke, theme.MarkerStroke)

	writeJSON(w, ThemeResponse{
		Branding:     branding,
		Theme:        themeColors,
		ThemeDark:    themeDark,
		NodeColors:   nodeColors,
		TypeColors:   typeColors,
		Home:         home,
		MarkerStroke: markerStroke,
	})
}

func (s *Server) handleConfigMap(w http.ResponseWriter, r *http.Request) {
	center := s.cfg.MapDefaults.Center
	if len(center) == 0 {
		center = []float64{37.45, -122.0}
	}
	zoom := s.cfg.MapDefaults.Zoom
	if zoom == 0 {
		zoom = 9
	}
	writeJSON(w, MapConfigResponse{Center: center, Zoom: zoom})
}

func (s *Server) handleConfigGeoFilter(w http.ResponseWriter, r *http.Request) {
	gf := s.getGeoFilter()
	// writeEnabled signals to clients (e.g. the customizer UI) whether a
	// strong API key is configured. Low-sensitivity by design.
	writeEnabled := s.cfg != nil && s.cfg.APIKey != "" && !IsWeakAPIKey(s.cfg.APIKey)
	if gf == nil || len(gf.Polygon) == 0 {
		writeJSON(w, map[string]interface{}{"polygon": nil, "bufferKm": 0, "writeEnabled": writeEnabled})
		return
	}
	writeJSON(w, map[string]interface{}{"polygon": gf.Polygon, "bufferKm": gf.BufferKm, "writeEnabled": writeEnabled})
}

// --- System Handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	m := s.getMemStats()
	uptime := time.Since(s.startedAt).Seconds()

	wsClients := 0
	if s.hub != nil {
		wsClients = s.hub.ClientCount()
	}

	// Real packet store stats
	pktCount := 0
	var pktEstMB float64
	var pktTrackedMB float64
	if s.store != nil {
		ps := s.store.GetPerfStoreStatsTyped()
		pktCount = ps.TotalLoaded
		pktEstMB = ps.EstimatedMB
		pktTrackedMB = ps.TrackedMB
	}

	// Real cache stats
	cs := CacheStats{}
	if s.store != nil {
		cs = s.store.GetCacheStatsTyped()
	}

	// Build eventLoop-equivalent from GC pause data (matches Node.js shape)
	var gcPauses []float64
	n := int(m.NumGC)
	if n > 256 {
		n = 256
	}
	for i := 0; i < n; i++ {
		idx := (int(m.NumGC) - n + i) % 256
		gcPauses = append(gcPauses, float64(m.PauseNs[idx])/1e6)
	}
	sortedPauses := sortedCopy(gcPauses)
	var lastPauseMs float64
	if m.NumGC > 0 {
		lastPauseMs = float64(m.PauseNs[(m.NumGC+255)%256]) / 1e6
	}

	// Build slow queries list (copy under lock)
	s.perfStats.mu.Lock()
	recentSlow := make([]SlowQuery, 0)
	sliceEnd := s.perfStats.SlowQueries
	if len(sliceEnd) > 5 {
		sliceEnd = sliceEnd[len(sliceEnd)-5:]
	}
	for _, sq := range sliceEnd {
		recentSlow = append(recentSlow, sq)
	}
	perfRequests := s.perfStats.Requests
	perfTotalMs := s.perfStats.TotalMs
	perfSlowCount := len(s.perfStats.SlowQueries)
	s.perfStats.mu.Unlock()

	writeJSON(w, HealthResponse{
		Status:      "ok",
		Engine:      "go",
		Version:     s.version,
		Commit:      s.commit,
		BuildTime:   s.buildTime,
		Uptime:      int(uptime),
		UptimeHuman: fmt.Sprintf("%dh %dm", int(uptime)/3600, (int(uptime)%3600)/60),
		Memory: MemoryStats{
			RSS:       int(m.Sys / 1024 / 1024),
			HeapUsed:  int(m.HeapAlloc / 1024 / 1024),
			HeapTotal: int(m.HeapSys / 1024 / 1024),
			External:  0,
		},
		EventLoop: EventLoopStats{
			CurrentLagMs: round(lastPauseMs, 1),
			MaxLagMs:     round(percentile(sortedPauses, 1.0), 1),
			P50Ms:        round(percentile(sortedPauses, 0.5), 1),
			P95Ms:        round(percentile(sortedPauses, 0.95), 1),
			P99Ms:        round(percentile(sortedPauses, 0.99), 1),
		},
		Cache:     cs,
		WebSocket: WebSocketStatsResp{Clients: wsClients},
		PacketStore: HealthPacketStoreStats{
			Packets:     pktCount,
			EstimatedMB: pktEstMB,
			TrackedMB:   pktTrackedMB,
		},
		Perf: HealthPerfStats{
			TotalRequests: int(perfRequests),
			AvgMs:         safeAvg(perfTotalMs, float64(perfRequests)),
			SlowQueries:   perfSlowCount,
			RecentSlow:    recentSlow,
		},
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	const statsTTL = 10 * time.Second

	s.statsMu.Lock()
	if s.statsCache != nil && time.Since(s.statsCachedAt) < statsTTL {
		cached := s.statsCache
		s.statsMu.Unlock()
		writeJSON(w, cached)
		return
	}
	s.statsMu.Unlock()

	// #1910: one rebuild per expiry, not one per request. Everything below runs
	// inside singleflight, so concurrent callers that miss the cache wait for the
	// first one's response instead of each running the same DB queries against a
	// 4-connection pool.
	built, sfErr, _ := s.statsSF.Do("stats", func() (interface{}, error) {
		// Re-check under the group: the winner may have just filled the cache.
		s.statsMu.Lock()
		if s.statsCache != nil && time.Since(s.statsCachedAt) < statsTTL {
			cached := s.statsCache
			s.statsMu.Unlock()
			return cached, nil
		}
		s.statsMu.Unlock()

		var stats *Stats
		var err error
		if s.store != nil {
			stats, err = s.store.GetStoreStats()
		} else {
			stats, err = s.db.GetStats()
		}
		if err != nil {
			return nil, err
		}
		counts := s.db.GetRoleCounts()

		// Memory accounting (#832). storeDataMB is the in-store packet byte
		// estimate (the old "trackedMB"); processRSSMB / goHeapInuseMB / goSysMB
		// give ops the breakdown needed to reason about real RSS. All values
		// share a single 1s-cached snapshot to amortize ReadMemStats cost.
		var storeDataMB float64
		if s.store != nil {
			storeDataMB = s.store.trackedMemoryMB()
		}
		mem := s.getMemorySnapshot(storeDataMB)

		resp := &StatsResponse{
			TotalPackets:       stats.TotalPackets,
			TotalTransmissions: &stats.TotalTransmissions,
			TotalObservations:  stats.TotalObservations,
			TotalNodes:         stats.TotalNodes,
			TotalNodesAllTime:  stats.TotalNodesAllTime,
			TotalObservers:     stats.TotalObservers,
			PacketsLastHour:    stats.PacketsLastHour,
			PacketsLast24h:     stats.PacketsLast24h,
			Engine:             "go",
			Version:            s.version,
			Commit:             s.commit,
			BuildTime:          s.buildTime,
			Counts: RoleCounts{
				Repeaters:  counts["repeaters"],
				Rooms:      counts["rooms"],
				Companions: counts["companions"],
				Sensors:    counts["sensors"],
			},
			SignatureDrops:        s.db.GetSignatureDropCount(),
			HashMigrationComplete: s.store != nil && s.store.hashMigrationComplete.Load(),

			TrackedMB:     mem.StoreDataMB, // deprecated alias
			StoreDataMB:   mem.StoreDataMB,
			ProcessRSSMB:  mem.ProcessRSSMB,
			GoHeapInuseMB: mem.GoHeapInuseMB,
			GoSysMB:       mem.GoSysMB,

			NeighborGraphCacheRebuildFailures: atomic.LoadUint64(&s.neighborGraphCacheRebuildFailures),
		}

		s.statsMu.Lock()
		s.statsCache = resp
		s.statsCachedAt = time.Now()
		s.statsMu.Unlock()

		return resp, nil
	})
	if sfErr != nil {
		writeError(w, 500, sfErr.Error())
		return
	}

	writeJSON(w, built.(*StatsResponse))
}

func (s *Server) handlePerf(w http.ResponseWriter, r *http.Request) {
	// Copy perfStats under lock to avoid data races
	s.perfStats.mu.Lock()
	type epSnapshot struct {
		path    string
		count   int
		totalMs float64
		maxMs   float64
		recent  []float64
	}
	epSnapshots := make([]epSnapshot, 0, len(s.perfStats.Endpoints))
	for path, ep := range s.perfStats.Endpoints {
		recentCopy := make([]float64, len(ep.Recent))
		copy(recentCopy, ep.Recent)
		epSnapshots = append(epSnapshots, epSnapshot{path, ep.Count, ep.TotalMs, ep.MaxMs, recentCopy})
	}
	uptimeSec := int(time.Since(s.perfStats.StartedAt).Seconds())
	totalRequests := s.perfStats.Requests
	totalMs := s.perfStats.TotalMs
	slowQueries := make([]SlowQuery, 0)
	sliceEnd := s.perfStats.SlowQueries
	if len(sliceEnd) > 20 {
		sliceEnd = sliceEnd[len(sliceEnd)-20:]
	}
	for _, sq := range sliceEnd {
		slowQueries = append(slowQueries, sq)
	}
	s.perfStats.mu.Unlock()

	// Process snapshots outside lock
	type epEntry struct {
		path string
		data *EndpointStatsResp
	}
	var entries []epEntry
	for _, snap := range epSnapshots {
		sorted := sortedCopy(snap.recent)
		d := &EndpointStatsResp{
			Count: snap.count,
			AvgMs: safeAvg(snap.totalMs, float64(snap.count)),
			P50Ms: round(percentile(sorted, 0.5), 1),
			P95Ms: round(percentile(sorted, 0.95), 1),
			MaxMs: round(snap.maxMs, 1),
		}
		entries = append(entries, epEntry{snap.path, d})
	}
	// Sort by total time spent (count * avg) descending, matching Node.js
	sort.Slice(entries, func(i, j int) bool {
		ti := float64(entries[i].data.Count) * entries[i].data.AvgMs
		tj := float64(entries[j].data.Count) * entries[j].data.AvgMs
		return ti > tj
	})
	summary := make(map[string]*EndpointStatsResp)
	for _, e := range entries {
		summary[e.path] = e.data
	}

	// Cache stats from packet store
	var perfCS PerfCacheStats
	if s.store != nil {
		cs := s.store.GetCacheStatsTyped()
		perfCS = PerfCacheStats{
			Size:       cs.Entries,
			Hits:       cs.Hits,
			Misses:     cs.Misses,
			StaleHits:  cs.StaleHits,
			Recomputes: cs.Recomputes,
			HitRate:    cs.HitRate,
		}
	}

	// Packet store stats
	var pktStoreStats *PerfPacketStoreStats
	if s.store != nil {
		ps := s.store.GetPerfStoreStatsTyped()
		pktStoreStats = &ps
	}

	// Opt-in store memory diagnostic: an O(tx+obs) walk, only when explicitly
	// requested so the hot /api/perf path stays cheap.
	var memBreakdown *StoreMemoryBreakdown
	var breakdownNote string
	if s.store != nil && r.URL.Query().Get("mem") == "1" {
		memBreakdown = s.store.GetStoreMemoryBreakdown()
		breakdownNote = memBreakdownNote
	}

	// SQLite stats
	var sqliteStats *SqliteStats
	if s.db != nil {
		ss := s.db.GetDBSizeStatsTyped()
		sqliteStats = &ss
	}

	writeJSON(w, PerfResponse{
		Uptime:              uptimeSec,
		TotalRequests:       totalRequests,
		AvgMs:               safeAvg(totalMs, float64(totalRequests)),
		Endpoints:           summary,
		SlowQueries:         slowQueries,
		Cache:               perfCS,
		PacketStore:         pktStoreStats,
		Sqlite:              sqliteStats,
		MemoryBreakdown:     memBreakdown,
		MemoryBreakdownNote: breakdownNote,
		GoRuntime: func() *GoRuntimeStats {
			ms := s.getMemStats()
			return &GoRuntimeStats{
				Goroutines:   runtime.NumGoroutine(),
				NumGC:        ms.NumGC,
				PauseTotalMs: float64(ms.PauseTotalNs) / 1e6,
				LastPauseMs:  float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6,
				HeapAllocMB:  float64(ms.HeapAlloc) / 1024 / 1024,
				HeapSysMB:    float64(ms.HeapSys) / 1024 / 1024,
				HeapInuseMB:  float64(ms.HeapInuse) / 1024 / 1024,
				HeapIdleMB:   float64(ms.HeapIdle) / 1024 / 1024,
				NumCPU:       runtime.NumCPU(),
			}
		}(),
	})
}

func (s *Server) handlePerfReset(w http.ResponseWriter, r *http.Request) {
	s.perfStats.mu.Lock()
	s.perfStats.Requests = 0
	s.perfStats.TotalMs = 0
	s.perfStats.Endpoints = make(map[string]*EndpointPerf)
	s.perfStats.SlowQueries = make([]SlowQuery, 0)
	s.perfStats.StartedAt = time.Now()
	s.perfStats.mu.Unlock()
	writeJSON(w, OkResp{Ok: true})
}

// --- Packet Handlers ---

func (s *Server) handlePackets(w http.ResponseWriter, r *http.Request) {
	// Multi-node filter: comma-separated pubkeys (Node.js parity)
	if nodesParam := r.URL.Query().Get("nodes"); nodesParam != "" {
		pubkeys := strings.Split(nodesParam, ",")
		var cleaned []string
		for _, pk := range pubkeys {
			pk = strings.TrimSpace(pk)
			if pk != "" {
				cleaned = append(cleaned, pk)
			}
		}
		order := "DESC"
		if r.URL.Query().Get("order") == "asc" {
			order = "ASC"
		}
		lim := queryLimit(r, 50, s.cfg.ListLimits.PacketsMax)
		var result *PacketResult
		var err error
		if s.store != nil {
			result = s.store.QueryMultiNodePackets(cleaned,
				lim, queryInt(r, "offset", 0),
				order, r.URL.Query().Get("since"), r.URL.Query().Get("until"))
		} else {
			result, err = s.db.QueryMultiNodePackets(cleaned,
				lim, queryInt(r, "offset", 0),
				order, r.URL.Query().Get("since"), r.URL.Query().Get("until"))
		}
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		result.Limit = lim
		result.Offset = queryInt(r, "offset", 0)
		writeJSON(w, result)
		return
	}

	q := PacketQuery{
		Limit:              queryLimit(r, 50, s.cfg.ListLimits.PacketsMax),
		Offset:             queryInt(r, "offset", 0),
		Observer:           r.URL.Query().Get("observer"),
		Hash:               r.URL.Query().Get("hash"),
		Since:              r.URL.Query().Get("since"),
		Until:              r.URL.Query().Get("until"),
		Region:             r.URL.Query().Get("region"),
		Node:               r.URL.Query().Get("node"),
		Channel:            r.URL.Query().Get("channel"),
		Area:               r.URL.Query().Get("area"),
		Order:              "DESC",
		ExpandObservations: r.URL.Query().Get("expand") == "observations",
	}
	if r.URL.Query().Get("order") == "asc" {
		q.Order = "ASC"
	}
	if v := r.URL.Query().Get("type"); v != "" {
		t, _ := strconv.Atoi(v)
		q.Type = &t
	}
	if v := r.URL.Query().Get("route"); v != "" {
		t, _ := strconv.Atoi(v)
		q.Route = &t
	}

	if r.URL.Query().Get("groupByHash") == "true" {
		var result *PacketResult
		var err error
		if s.store != nil {
			result = s.store.QueryGroupedPackets(q)
		} else {
			result, err = s.db.QueryGroupedPackets(q)
		}
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		result.Limit = q.Limit
		result.Offset = q.Offset
		writeJSON(w, result)
		return
	}

	var result *PacketResult
	var err error
	if s.store != nil {
		result = s.store.QueryPackets(q)
	} else {
		result, err = s.db.QueryPackets(q)
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	result.Limit = q.Limit
	result.Offset = q.Offset
	writeJSON(w, result)
}

func (s *Server) handlePacketTimestamps(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	if since == "" {
		writeError(w, 400, "since required")
		return
	}
	if s.store != nil {
		writeJSON(w, s.store.GetTimestamps(since))
		return
	}
	writeJSON(w, []string{})
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// muxBraceParam matches {param} in gorilla/mux route templates for normalization.
var muxBraceParam = regexp.MustCompile(`\{([^}]+)\}`)

// perfHexFallback matches hex IDs for perf path normalization fallback.
var perfHexFallback = regexp.MustCompile(`[0-9a-f]{8,}`)

// handleBatchObservations returns observations for multiple hashes in a single request.
// POST /api/packets/observations with JSON body: {"hashes": ["abc123", "def456", ...]}
// Response: {"results": {"abc123": [...observations...], "def456": [...], ...}}
// Limited to 200 hashes per request to prevent abuse.
func (s *Server) handleBatchObservations(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hashes []string `json:"hashes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "invalid JSON body")
		return
	}
	const maxHashes = 200
	if len(body.Hashes) > maxHashes {
		writeError(w, 400, fmt.Sprintf("too many hashes (max %d)", maxHashes))
		return
	}
	if len(body.Hashes) == 0 {
		writeJSON(w, map[string]interface{}{"results": map[string]interface{}{}})
		return
	}

	results := make(map[string][]ObservationResp, len(body.Hashes))
	if s.store != nil {
		for _, hash := range body.Hashes {
			obs := s.store.GetObservationsForHash(hash)
			results[hash] = mapSliceToObservations(obs)
		}
	}
	writeJSON(w, map[string]interface{}{"results": results})
}

func (s *Server) handlePacketDetail(w http.ResponseWriter, r *http.Request) {
	param := mux.Vars(r)["id"]
	var packet map[string]interface{}
	fromDB := false

	isHash := hashPattern.MatchString(strings.ToLower(param))
	if s.store != nil {
		if isHash {
			packet = s.store.GetPacketByHash(param)
		}
		if packet == nil {
			id, parseErr := strconv.Atoi(param)
			if parseErr == nil {
				packet = s.store.GetTransmissionByID(id)
				if packet == nil {
					packet = s.store.GetPacketByID(id)
				}
			}
		}
	}
	// DB fallback: in-memory PacketStore prunes old entries, but the SQLite
	// DB retains them and is the source for /api/nodes recentAdverts. Without
	// this fallback, links from node-detail pages 404 once the packet ages out.
	if packet == nil && s.db != nil {
		if isHash {
			if dbPkt, err := s.db.GetPacketByHash(param); err == nil && dbPkt != nil {
				packet = dbPkt
				fromDB = true
			}
		}
		if packet == nil {
			if id, parseErr := strconv.Atoi(param); parseErr == nil {
				if dbPkt, err := s.db.GetTransmissionByID(id); err == nil && dbPkt != nil {
					packet = dbPkt
					fromDB = true
				}
			}
		}
	}
	if packet == nil {
		writeError(w, 404, "Not found")
		return
	}

	hash, _ := packet["hash"].(string)
	var observations []map[string]interface{}
	if s.store != nil {
		observations = s.store.GetObservationsForHash(hash)
	}
	if len(observations) == 0 && fromDB && s.db != nil && hash != "" {
		observations = s.db.GetObservationsForHash(hash)
	}
	observationCount := len(observations)
	if observationCount == 0 {
		observationCount = 1
	}

	var pathHops []interface{}
	if pj, ok := packet["path_json"]; ok && pj != nil {
		if pjStr, ok := pj.(string); ok && pjStr != "" {
			json.Unmarshal([]byte(pjStr), &pathHops)
		}
	}
	if pathHops == nil {
		pathHops = []interface{}{}
	}

	writeJSON(w, PacketDetailResponse{
		Packet:           packet,
		Path:             pathHops,
		ObservationCount: observationCount,
		Observations:     mapSliceToObservations(observations),
	})
}

func (s *Server) handleDecode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hex string `json:"hex"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "invalid JSON body")
		return
	}
	hexStr := strings.TrimSpace(body.Hex)
	if hexStr == "" {
		writeError(w, 400, "hex is required")
		return
	}
	decoded, err := DecodePacket(hexStr, true)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, DecodeResponse{
		Decoded: map[string]interface{}{
			"header":  decoded.Header,
			"path":    decoded.Path,
			"payload": decoded.Payload,
		},
	})
}

// --- Node Handlers ---

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nodes, total, counts, err := s.db.GetNodes(
		queryLimit(r, 50, s.cfg.ListLimits.NodesMax),
		queryInt(r, "offset", 0),
		q.Get("role"), q.Get("search"), q.Get("before"),
		q.Get("lastHeard"), q.Get("sortBy"), q.Get("region"),
	)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if s.store != nil {
		hashInfo := s.store.GetNodeHashSizeInfo()
		relayWindow := s.cfg.GetHealthThresholds().RelayActiveHours
		// #1257: bulk-compute relay info + usefulness scores ONCE per
		// request (cached 15s) instead of calling the per-node helpers
		// inside the loop. The per-node calls each grabbed their own
		// RLock and walked byPathHop[pk] + byPayloadType, blowing
		// /api/nodes up to 30+s on busy networks.
		var relayMap map[string]RepeaterRelayInfo
		var usefulMap map[string]float64
		needsRelay := false
		for _, node := range nodes {
			if role, _ := node["role"].(string); role == "repeater" || role == "room" {
				needsRelay = true
				break
			}
		}
		if needsRelay {
			relayMap = s.store.GetRepeaterRelayInfoMap(relayWindow)
			usefulMap = s.store.GetRepeaterUsefulnessScoreMap()
		}
		// Bridge axis (#672 axis 2 of 4). Snapshot is an atomic load
		// — safe to call regardless of needsRelay, and we want the
		// score on repeater rows specifically.
		bridgeMap := s.store.GetBridgeScoreMap()
		// Coverage + Redundancy axes (#672 axes 3 & 4). Atomic snapshots,
		// same discipline as the bridge map.
		coverageMap := s.store.GetCoverageScoreMap()
		redundancyMap := s.store.GetRedundancyScoreMap()
		// Whether the structural-axis recomputer has produced a snapshot yet:
		// distinguishes a genuinely isolated repeater (real "F") from cold
		// start (grade withheld) for the all-zero node (#1762 MAJOR-4).
		axesComputed := s.store.UsefulnessAxesComputed()
		// Population max of the raw Traffic axis, used to max-normalize
		// traffic into the composite (the other three axes are already
		// max-normalized; #1762 review).
		maxUseful := maxFloat(usefulMap)
		for _, node := range nodes {
			if pk, ok := node["public_key"].(string); ok {
				EnrichNodeWithHashSize(node, hashInfo[pk])
				mbEntry, _ := s.store.GetMultibyteCapFor(pk)
				EnrichNodeWithMultiByte(node, mbEntry)
				if role, _ := node["role"].(string); role == "repeater" || role == "room" {
					info, _ := lookupRelayInfo(relayMap, pk)
					info.WindowHours = relayWindow
					if info.LastRelayed != "" {
						node["last_relayed"] = info.LastRelayed
					}
					node["relay_active"] = info.RelayActive
					node["relay_count_1h"] = info.RelayCount1h
					node["relay_count_24h"] = info.RelayCount24h
					node["unscoped_relay_count_24h"] = info.UnscopedRelayCount24h
					// #1751: region scopes this repeater has transported.
					// Set only when non-empty so the field is absent for
					// nodes without scopes / on older schemas.
					if len(info.TransportedScopes) > 0 {
						node["transported_scopes"] = info.TransportedScopes
					}
					// #672 4-axis usefulness. traffic_share_score keeps the
					// raw per-axis Traffic value (#1456); the structural axes
					// are surfaced individually; the composite uses the
					// max-normalized traffic so its 0.20 weight is real.
					trafficRaw := lookupUsefulnessScore(usefulMap, pk)
					trafficNorm := 0.0
					if maxUseful > 0 {
						trafficNorm = trafficRaw / maxUseful
					}
					enrichNodeUsefulness(node, trafficRaw, usefulnessAxes{
						Traffic:    trafficNorm,
						Bridge:     lookupUsefulnessScore(bridgeMap, pk),
						Coverage:   lookupUsefulnessScore(coverageMap, pk),
						Redundancy: lookupUsefulnessScore(redundancyMap, pk),
					}, axesComputed)
				}
			}
		}
	}
	if s.cfg.GeoFilter != nil {
		filtered := nodes[:0]
		for _, node := range nodes {
			// Foreign-flagged nodes (#730) are kept even when their GPS lies
			// outside the geofilter polygon — that's the whole point of the
			// flag: operators need to SEE bridged/leaked nodes, not have them
			// filtered away. The ingestor sets foreign_advert=1 when its
			// configured geo_filter rejected the advert; the server must
			// surface those.
			if isForeign, _ := node["foreign"].(bool); isForeign {
				filtered = append(filtered, node)
				continue
			}
			if NodePassesGeoFilter(node["lat"], node["lon"], s.cfg.GeoFilter) {
				filtered = append(filtered, node)
			}
		}
		total = len(filtered)
		nodes = filtered
	}
	// Filter blacklisted nodes
	if len(s.cfg.NodeBlacklist) > 0 {
		filtered := nodes[:0]
		for _, node := range nodes {
			if pk, ok := node["public_key"].(string); !ok || !s.cfg.IsBlacklisted(pk) {
				filtered = append(filtered, node)
			}
		}
		total = len(filtered)
		nodes = filtered
	}
	// Filter nodes whose name starts with a hidden prefix (#1181). DB rows
	// are preserved — this only drops them from the API surface so observer
	// history (paths, hops, distances) remains intact for analytics.
	if len(s.cfg.HiddenNamePrefixes) > 0 {
		filtered := nodes[:0]
		for _, node := range nodes {
			name, _ := node["name"].(string)
			if !s.cfg.IsNameHidden(name) {
				filtered = append(filtered, node)
			}
		}
		total = len(filtered)
		nodes = filtered
	}
	// Filter by area
	if area := q.Get("area"); area != "" {
		var areaNodes map[string]bool
		if s.store != nil {
			areaNodes = s.store.resolveAreaNodes(area)
		} else if s.cfg != nil && s.cfg.Areas != nil {
			if entry, ok := s.cfg.Areas[area]; ok {
				pks, err := s.db.GetNodePubkeysInArea(entry)
				if err == nil {
					areaNodes = make(map[string]bool, len(pks))
					for _, pk := range pks {
						areaNodes[pk] = true
					}
				}
			}
		}
		if areaNodes != nil {
			filtered := make([]map[string]interface{}, 0, len(nodes))
			for _, n := range nodes {
				pk, _ := n["public_key"].(string)
				if areaNodes[pk] {
					filtered = append(filtered, n)
				}
			}
			nodes = filtered
			total = len(filtered)
		}
	}
	writeJSON(w, NodeListResponse{Nodes: nodes, Total: total, Counts: counts})
}

func (s *Server) handleNodeSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeJSON(w, NodeSearchResponse{Nodes: []map[string]interface{}{}})
		return
	}
	nodes, err := s.db.SearchNodes(strings.TrimSpace(q), 10)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Filter blacklisted nodes from search results
	if len(s.cfg.NodeBlacklist) > 0 {
		filtered := make([]map[string]interface{}, 0, len(nodes))
		for _, node := range nodes {
			if pk, ok := node["public_key"].(string); !ok || !s.cfg.IsBlacklisted(pk) {
				filtered = append(filtered, node)
			}
		}
		nodes = filtered
	}
	// Drop hidden-prefix nodes from search results (#1181).
	if len(s.cfg.HiddenNamePrefixes) > 0 {
		filtered := make([]map[string]interface{}, 0, len(nodes))
		for _, node := range nodes {
			name, _ := node["name"].(string)
			if !s.cfg.IsNameHidden(name) {
				filtered = append(filtered, node)
			}
		}
		nodes = filtered
	}
	writeJSON(w, NodeSearchResponse{Nodes: nodes})
}

func (s *Server) handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	if s.cfg.IsBlacklisted(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Issue #772: short-URL fallback. If exact pubkey lookup misses and the
	// path looks like a hex prefix (>=8 chars, <64), try prefix resolution.
	if node == nil && len(pubkey) >= 8 && len(pubkey) < 64 {
		resolved, ambiguous, perr := s.db.GetNodeByPrefix(pubkey)
		if perr != nil {
			writeError(w, 500, perr.Error())
			return
		}
		if ambiguous {
			writeError(w, http.StatusConflict, "Ambiguous prefix: multiple nodes match. Use a longer prefix.")
			return
		}
		if resolved != nil {
			if pk, _ := resolved["public_key"].(string); pk != "" && s.cfg.IsBlacklisted(pk) {
				writeError(w, 404, "Not found")
				return
			}
			node = resolved
		}
	}
	if node == nil {
		writeError(w, 404, "Not found")
		return
	}
	// Hide the node when its name matches an operator-configured prefix
	// (#1181). 404 mirrors the blacklist behaviour above — callers learn
	// nothing about whether the row exists.
	if name, _ := node["name"].(string); s.cfg.IsNameHidden(name) {
		writeError(w, 404, "Not found")
		return
	}
	if pk, _ := node["public_key"].(string); pk != "" {
		pubkey = pk
	}

	if s.store != nil {
		hashInfo := s.store.GetNodeHashSizeInfo()
		EnrichNodeWithHashSize(node, hashInfo[pubkey])
		mbEntry, _ := s.store.GetMultibyteCapFor(pubkey)
		EnrichNodeWithMultiByte(node, mbEntry)
		if role, _ := node["role"].(string); role == "repeater" || role == "room" {
			ht := s.cfg.GetHealthThresholds()
			info := s.store.GetRepeaterRelayInfo(pubkey, ht.RelayActiveHours)
			if info.LastRelayed != "" {
				node["last_relayed"] = info.LastRelayed
			}
			node["relay_active"] = info.RelayActive
			node["relay_window_hours"] = info.WindowHours
			node["relay_count_1h"] = info.RelayCount1h
			node["relay_count_24h"] = info.RelayCount24h
			node["unscoped_relay_count_24h"] = info.UnscopedRelayCount24h
			// #1751: region scopes this repeater has transported. Set only
			// when non-empty (absent for no-scope nodes / older schemas).
			if len(info.TransportedScopes) > 0 {
				node["transported_scopes"] = info.TransportedScopes
			}
			// #672 4-axis usefulness (see handleNodes for the field
			// contract). traffic_share_score keeps the raw per-axis
			// Traffic value (#1456); the composite uses the
			// population-max-normalized traffic (#1762 review).
			usefulMap := s.store.GetRepeaterUsefulnessScoreMap()
			trafficRaw := lookupUsefulnessScore(usefulMap, pubkey)
			trafficNorm := 0.0
			if mx := maxFloat(usefulMap); mx > 0 {
				trafficNorm = trafficRaw / mx
			}
			enrichNodeUsefulness(node, trafficRaw, usefulnessAxes{
				Traffic:    trafficNorm,
				Bridge:     s.store.GetBridgeScore(pubkey),
				Coverage:   s.store.GetCoverageScore(pubkey),
				Redundancy: s.store.GetRedundancyScore(pubkey),
			}, s.store.UsefulnessAxesComputed())
		}
	}

	// #1143: GetRecentTransmissionsForNode no longer accepts a name fallback;
	// attribution is strict exact-match on the indexed from_pubkey column.
	recentAdverts, _ := s.db.GetRecentTransmissionsForNode(pubkey, 20)

	// Windowed flood-advert count (7d): only the mesh-wide-airtime advert kind,
	// separated from zero-hop adverts so a nearby observer hearing a node's
	// cheap local adverts does not inflate the number. Consumed by the ArcScope
	// repeater advisor to rate advert hygiene.
	if n, err := s.db.CountFloodAdvertsForNode(pubkey, 7*24, floodAdvertRowCap); err == nil {
		node["flood_advert_count_7d"] = n
	} else {
		log.Printf("WARN CountFloodAdvertsForNode(%s): %v", pubkey, err)
	}

	writeJSON(w, NodeDetailResponse{
		Node:          node,
		RecentAdverts: recentAdverts,
	})
}

func (s *Server) handleNodeHealth(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	if s.cfg.IsBlacklisted(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	if s.isPubkeyHidden(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	if s.store != nil {
		result, err := s.store.GetNodeHealth(pubkey)
		if err != nil || result == nil {
			writeError(w, 404, "Not found")
			return
		}
		writeJSON(w, result)
		return
	}
	writeError(w, 404, "Not found")
}

func (s *Server) handleBulkHealth(w http.ResponseWriter, r *http.Request) {
	lim := queryLimit(r, 50, s.cfg.ListLimits.BulkHealthMax)

	if s.store != nil {
		region := r.URL.Query().Get("region")
		area := r.URL.Query().Get("area")
		results := s.store.GetBulkHealth(lim, region, area)
		// Filter blacklisted nodes + hidden-prefix nodes (#1181).
		needsBlacklist := len(s.cfg.NodeBlacklist) > 0
		needsHidden := len(s.cfg.HiddenNamePrefixes) > 0
		if needsBlacklist || needsHidden {
			filtered := make([]map[string]interface{}, 0, len(results))
			for _, entry := range results {
				if pk, ok := entry["public_key"].(string); ok && needsBlacklist && s.cfg.IsBlacklisted(pk) {
					continue
				}
				if needsHidden {
					name, _ := entry["name"].(string)
					if s.cfg.IsNameHidden(name) {
						continue
					}
				}
				filtered = append(filtered, entry)
			}
			writeJSON(w, filtered)
			return
		}
		writeJSON(w, results)
		return
	}

	writeJSON(w, []BulkHealthEntry{})
}

func (s *Server) handleNetworkStatus(w http.ResponseWriter, r *http.Request) {
	ht := s.cfg.GetHealthThresholds()
	result, err := s.db.GetNetworkStatus(ht)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleNodePaths(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	if s.cfg.IsBlacklisted(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	if s.isPubkeyHidden(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		writeError(w, 404, "Not found")
		return
	}
	if s.store == nil {
		writeError(w, 503, "Packet store unavailable")
		return
	}
	if !s.store.PathHopIndexReady() {
		writeIndexLoading503(w)
		return
	}

	// Use the precomputed byPathHop index instead of scanning all packets.
	// Look up by full pubkey (resolved hops) and by short prefixes (raw hops).
	lowerPK := strings.ToLower(pubkey)
	prefix2 := lowerPK
	if len(prefix2) > 4 {
		prefix2 = prefix2[:4]
	}
	prefix1 := lowerPK
	if len(prefix1) > 2 {
		prefix1 = prefix1[:2]
	}

	s.store.mu.RLock()
	_, pm := s.store.getCachedNodesAndPM()

	// Collect candidate transmissions from the index, deduplicating by tx ID.
	// confirmedByFullKey tracks TXs found via the full-pubkey index key — these are
	// already resolved_path-confirmed and bypass the hop-level check below.
	confirmedByFullKey := make(map[int]bool)
	seen := make(map[int]bool)
	var candidates []*StoreTx
	addCandidates := func(key string, confirmed bool) {
		for _, tx := range s.store.byPathHop[key] {
			if !seen[tx.ID] {
				seen[tx.ID] = true
				if confirmed {
					confirmedByFullKey[tx.ID] = true
				}
				candidates = append(candidates, tx)
			}
		}
	}
	addCandidates(lowerPK, true)  // full pubkey match (from resolved_path) → confirmed
	addCandidates(prefix1, false) // 2-char raw hop match
	addCandidates(prefix2, false) // 4-char raw hop match
	// Also check any raw hops that start with prefix2 (longer prefixes).
	// Raw hops are typically 2 chars, so iterate only keys with HasPrefix
	// on the small set of index keys rather than all packets.
	for key := range s.store.byPathHop {
		if len(key) > 4 && len(key) < len(lowerPK) && strings.HasPrefix(key, prefix2) {
			addCandidates(key, false)
		}
	}

	// Post-filter: verify target node actually appears in each candidate's resolved_path.
	// The byPathHop index uses short prefixes which can collide (e.g. "c0" matches multiple nodes).
	// We lean on resolved_path (from neighbor affinity graph) to disambiguate.
	//
	// Collect candidate IDs and index membership under the read lock, then release
	// the lock before running SQL queries (confirmResolvedPathContains does disk I/O).
	type candidateCheck struct {
		tx         *StoreTx
		hasReverse bool
		inIndex    bool
	}
	checks := make([]candidateCheck, len(candidates))
	for i, tx := range candidates {
		cc := candidateCheck{tx: tx}
		if !s.store.useResolvedPathIndex {
			cc.inIndex = true // flag off — keep all
		} else if _, hasRev := s.store.resolvedPubkeyReverse[tx.ID]; !hasRev {
			cc.inIndex = true // no indexed pubkeys — keep (conservative)
		} else {
			h := resolvedPubkeyHash(lowerPK)
			for _, id := range s.store.resolvedPubkeyIndex[h] {
				if id == tx.ID {
					cc.hasReverse = true // needs SQL confirmation
					break
				}
			}
			// If not in index at all, it's a definite no
		}
		checks[i] = cc
	}
	s.store.mu.RUnlock()

	// Now run SQL checks outside the lock for candidates that need confirmation.
	confirmedBySQL := make(map[int]bool)
	filtered := candidates[:0]
	for _, cc := range checks {
		if cc.inIndex {
			filtered = append(filtered, cc.tx)
		} else if cc.hasReverse {
			if s.store.confirmResolvedPathContains(cc.tx.ID, lowerPK) {
				filtered = append(filtered, cc.tx)
				confirmedBySQL[cc.tx.ID] = true
			}
		}
		// else: not in index → exclude
	}
	candidates = filtered

	// #1278: Read the CANONICAL persisted resolved_path for each surviving
	// candidate OUTSIDE s.mu (fetchResolvedPathForTxBest takes lruMu; the
	// lock-ordering contract forbids acquiring lruMu under s.mu).
	//
	// Option A from the issue: the packets page renders each tx via
	// fetchResolvedPathForTxBest. For /api/nodes/{pk}/paths to stay
	// CONSISTENT with the packets page, BOTH the containsTarget membership
	// decision AND the displayed hop names must come from that same
	// canonical resolved_path — not a re-resolution biased by passing the
	// queried node as hopContext anchor.
	//
	// Falls back to biased re-resolve only when a tx has no persisted
	// resolved_path (older data / async backfill incomplete); in that case
	// there's no canonical answer to be consistent with.
	canonicalRP := make(map[int][]*string, len(candidates))
	for _, tx := range candidates {
		if rp := s.store.fetchResolvedPathForTxBest(tx); rp != nil {
			canonicalRP[tx.ID] = rp
		}
	}

	// Re-acquire read lock for the aggregation phase that reads store data.
	s.store.mu.RLock()

	type pathAgg struct {
		Hops       []PathHopResp
		Count      int
		LastSeen   string
		SampleHash string
	}
	pathGroups := map[string]*pathAgg{}
	totalTransmissions := 0
	hopCache := make(map[string]*nodeInfo)
	// Anchor the resolver with the node being queried so tier-1/2 hop-context
	// resolution lights up when a hop prefix matches the destination node
	// (handleNodePaths aggregates paths terminating at lowerPK). Passing nil
	// here re-introduced regression #1197 in production. See
	// resolve_context_callsites_test.go.
	//
	// NOTE (#1278): this biased resolver is only consulted for the FALLBACK
	// path — txs with no persisted resolved_path. Txs with a canonical
	// resolved_path use the persisted pubkeys directly (see canonicalRP),
	// which keeps results consistent with the packets page.
	hopContext := []string{lowerPK}
	resolveHop := func(hop string) *nodeInfo {
		if cached, ok := hopCache[hop]; ok {
			return cached
		}
		r, _, _ := pm.resolveWithContext(hop, hopContext, s.store.graph.Load())
		hopCache[hop] = r
		return r
	}
	// nodeByPK caches pubkey → *nodeInfo lookups when rendering canonical
	// resolved_path entries. Cheap O(1) hit against pm.m (the prefix map
	// stores the full pubkey as a key for pubkeys >= maxPrefixLen).
	nodeByPK := make(map[string]*nodeInfo)
	lookupNode := func(pk string) *nodeInfo {
		key := strings.ToLower(pk)
		if cached, ok := nodeByPK[key]; ok {
			return cached
		}
		// Use plain resolve(); we have the full pubkey, no ambiguity.
		n := pm.resolve(key)
		if n == nil || !strings.EqualFold(n.PublicKey, key) {
			// Full pubkey may not be present in pm (role filter, eviction).
			// Fall through with nil; caller renders prefix-only entry.
			nodeByPK[key] = nil
			return nil
		}
		nodeByPK[key] = n
		return n
	}
	for _, tx := range candidates {
		hops := txGetParsedPath(tx)
		resolvedHops := make([]PathHopResp, len(hops))
		sigParts := make([]string, len(hops))
		containsTarget := false

		if rp, ok := canonicalRP[tx.ID]; ok {
			// Option A: render hops + decide membership from the CANONICAL
			// persisted resolved_path. resolved_path is parallel to the
			// best-obs path_json which may be longer than tx.PathJSON used by
			// txGetParsedPath; align by the shorter length.
			rpLen := len(rp)
			for i, hop := range hops {
				entry := PathHopResp{Prefix: hop, Name: hop}
				var resolvedPK string
				if i < rpLen && rp[i] != nil {
					resolvedPK = strings.ToLower(*rp[i])
				}
				if resolvedPK != "" {
					if n := lookupNode(resolvedPK); n != nil {
						entry.Name = n.Name
						entry.Pubkey = n.PublicKey
						if n.HasGPS {
							entry.Lat = n.Lat
							entry.Lon = n.Lon
						}
						sigParts[i] = n.PublicKey
					} else {
						entry.Pubkey = resolvedPK
						sigParts[i] = resolvedPK
					}
					if resolvedPK == lowerPK {
						containsTarget = true
					}
				} else {
					sigParts[i] = hop
				}
				resolvedHops[i] = entry
			}
		} else {
			// Fallback: no canonical resolved_path persisted (older data /
			// async backfill incomplete). Use biased re-resolve and the
			// legacy containsTarget heuristics (preserves #1197 behavior
			// and the #929 prefix-collision exclusion test).
			//
			// #1352: When a hop prefix has MULTIPLE candidates (sibling
			// prefix collisions), the biased resolver — anchored on the
			// queried target via hopContext=[lowerPK] — will preferentially
			// resolve to the target via tier-2 geo / tier-3 GPS. This
			// causes the SAME tx to be attributed to every prefix sibling
			// when each is queried in turn. To prevent wrong-node
			// attribution, we ONLY accept a resolver match as evidence of
			// target membership when:
			//   (a) the tx was already pre-confirmed via
			//       confirmedByFullKey (resolved_path index hit) or
			//       confirmedBySQL (verified pubkey in resolved_path), OR
			//   (b) the hop's prefix candidate set is UNIQUE — no
			//       collision, so the resolver had no choice to bias.
			// Multi-candidate hops with no SQL/index confirmation are
			// treated as ambiguous and excluded from paths-through.
			containsTarget = confirmedByFullKey[tx.ID] || confirmedBySQL[tx.ID]
			// preconfirmed: SNAPSHOT of containsTarget BEFORE the per-hop
			// loop runs. Captures only the SQL/full-key index pre-confirmation
			// signal (independent of biased-resolver output). MUST NOT be
			// reassigned inside the loop — doing so would let a biased-
			// resolver match in hop[i] silently authorize a later ambiguous
			// hop[j], re-opening the #1352 wrong-node attribution path.
			//
			// Note: today the loop only ever transitions containsTarget
			// false → true, so the snapshot is functionally redundant for
			// the preconfirmed==true case (containsTarget is already true).
			// We keep the snapshot + the `preconfirmed ||` clauses below
			// as a structural invariant: future edits that flip
			// containsTarget back to false inside the loop (e.g. an
			// "exclude if last hop doesn't match" tweak) would otherwise
			// silently lose the SQL/index confirmation. The snapshot is
			// the documented contract.
			preconfirmed := containsTarget
			for i, hop := range hops {
				resolved := resolveHop(hop)
				entry := PathHopResp{Prefix: hop, Name: hop}
				lowerHop := strings.ToLower(hop)
				// #1352 guard helper. We treat as "unique/safe" when the
				// hop's prefix candidate set has EXACTLY ONE member: no
				// sibling collision, so the biased resolver had no choice
				// to bias. len(pm.m[lowerHop]) == 0 is also accepted as
				// safe-by-default in the resolvable arm because the
				// resolver returned a non-nil candidate from somewhere
				// (e.g. a full-pubkey hop longer than maxPrefixLen, or a
				// hop indexed under a different prefix length); there's
				// no collision to resolve away. In the unresolvable arm
				// below, len==0 is the ONLY reachable case (resolveHop
				// returns nil iff pm.m[lowerHop] is empty — see
				// resolveWithContext priority chain), so the guard there
				// is intentionally permissive on len==0 and the
				// `preconfirmed ||` clause is the meaningful gate.
				uniquePrefix := len(pm.m[lowerHop]) <= 1
				if resolved != nil {
					entry.Name = resolved.Name
					entry.Pubkey = resolved.PublicKey
					if resolved.HasGPS {
						entry.Lat = resolved.Lat
						entry.Lon = resolved.Lon
					}
					sigParts[i] = resolved.PublicKey
					if strings.ToLower(resolved.PublicKey) == lowerPK {
						// #1352: only attribute when unambiguous OR
						// already pre-confirmed via SQL/full-key index.
						if preconfirmed || uniquePrefix {
							containsTarget = true
						}
					}
				} else {
					sigParts[i] = hop
					// Unresolvable hop: keep conservative if prefix could
					// be the target AND there's no sibling collision.
					// If multiple candidates share this prefix, attribution
					// is ambiguous — don't claim membership without SQL
					// confirmation (#1352). See comment on uniquePrefix
					// above re: why len==0 is treated as safe here.
					if strings.HasPrefix(lowerPK, lowerHop) {
						if preconfirmed || uniquePrefix {
							containsTarget = true
						}
					}
				}
				resolvedHops[i] = entry
			}
		}

		if !containsTarget {
			continue
		}
		totalTransmissions++

		sig := strings.Join(sigParts, "→")
		agg := pathGroups[sig]
		if agg == nil {
			pathGroups[sig] = &pathAgg{
				Hops:       resolvedHops,
				Count:      1,
				LastSeen:   tx.FirstSeen,
				SampleHash: tx.Hash,
			}
			continue
		}
		agg.Count++
		if tx.FirstSeen > agg.LastSeen {
			agg.LastSeen = tx.FirstSeen
			agg.SampleHash = tx.Hash
		}
	}
	s.store.mu.RUnlock()

	paths := make([]PathEntryResp, 0, len(pathGroups))
	for _, agg := range pathGroups {
		var lastSeen interface{}
		if agg.LastSeen != "" {
			lastSeen = agg.LastSeen
		}
		paths = append(paths, PathEntryResp{
			Hops:       agg.Hops,
			Count:      agg.Count,
			LastSeen:   lastSeen,
			SampleHash: agg.SampleHash,
		})
	}
	sort.Slice(paths, func(i, j int) bool {
		li := ""
		lj := ""
		if paths[i].LastSeen != nil {
			li = fmt.Sprintf("%v", paths[i].LastSeen)
		}
		if paths[j].LastSeen != nil {
			lj = fmt.Sprintf("%v", paths[j].LastSeen)
		}
		if li != lj {
			return li > lj
		}
		return paths[i].Count > paths[j].Count
	})
	if len(paths) > 50 {
		paths = paths[:50]
	}

	writeJSON(w, NodePathsResponse{
		Node: map[string]interface{}{
			"public_key": node["public_key"],
			"name":       node["name"],
			"lat":        node["lat"],
			"lon":        node["lon"],
		},
		Paths:              paths,
		TotalPaths:         len(pathGroups),
		TotalTransmissions: totalTransmissions,
	})
}

func (s *Server) handleNodeAnalytics(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	if s.cfg.IsBlacklisted(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	if s.isPubkeyHidden(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}

	if s.store != nil {
		result, err := s.store.GetNodeAnalytics(pubkey, days)
		if err != nil || result == nil {
			writeError(w, 404, "Not found")
			return
		}
		writeJSON(w, result)
		return
	}

	writeError(w, 404, "Not found")
}

func (s *Server) handleNodeClockSkew(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	if s.store == nil {
		writeError(w, 404, "Not found")
		return
	}
	result := s.store.GetNodeClockSkew(pubkey)
	if result == nil {
		writeError(w, 404, "No clock skew data for this node")
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleObserverClockSkew(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, []ObserverCalibration{})
		return
	}
	writeJSON(w, s.store.GetObserverCalibrations())
}

func (s *Server) handleFleetClockSkew(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, []*NodeClockSkew{})
		return
	}
	area := r.URL.Query().Get("area")
	writeJSON(w, s.store.GetFleetClockSkew(area))
}

// --- Analytics Handlers ---

func (s *Server) handleAnalyticsRF(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	area := r.URL.Query().Get("area")
	window := ParseTimeWindow(r)
	if s.store != nil {
		// Issue #1659: gate the default-shape request behind first-pass
		// completion of the RF recomputer. Otherwise the post-restart
		// in-RAM slice is served until the next recompute tick, and
		// the client pins it via CLIENT_TTL.analyticsRF.
		if region == "" && area == "" && window.IsZero() {
			s.store.analyticsRecomputerMu.RLock()
			rc := s.store.recompRF
			s.store.analyticsRecomputerMu.RUnlock()
			if rc != nil && rc.IsWarmingUp_1659() {
				writeAnalyticsWarmup503(w)
				return
			}
		}
		writeJSON(w, s.store.GetAnalyticsRFWithWindow(region, area, window))
		return
	}
	writeJSON(w, RFAnalyticsResponse{
		SNR:            SignalStats{},
		RSSI:           SignalStats{},
		SnrValues:      Histogram{Bins: []HistogramBin{}, Min: 0, Max: 0},
		RssiValues:     Histogram{Bins: []HistogramBin{}, Min: 0, Max: 0},
		PacketSizes:    Histogram{Bins: []HistogramBin{}, Min: 0, Max: 0},
		PacketsPerHour: []HourlyCount{},
		PayloadTypes:   []PayloadTypeEntry{},
		SnrByType:      []PayloadTypeSignal{},
		SignalOverTime: []SignalOverTimeEntry{},
		ScatterData:    []ScatterPoint{},
	})
}

func (s *Server) handleAnalyticsRelayAirtimeShare(w http.ResponseWriter, r *http.Request) {
	window := ParseTimeWindow(r)
	if s.store != nil {
		writeJSON(w, s.store.GetRelayAirtimeShareWithWindow(window))
		return
	}
	writeJSON(w, map[string]interface{}{
		"rows":        []map[string]interface{}{},
		"total_count": 0,
		"total_score": 0,
		"window":      "",
		"cached":      false,
	})
}

func (s *Server) handleAnalyticsTopology(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	area := r.URL.Query().Get("area")
	window := ParseTimeWindow(r)
	if s.store != nil {
		// #1659 warmup gate (see handleAnalyticsRF for rationale).
		if region == "" && area == "" && window.IsZero() {
			s.store.analyticsRecomputerMu.RLock()
			rc := s.store.recompTopology
			s.store.analyticsRecomputerMu.RUnlock()
			if rc != nil && rc.IsWarmingUp_1659() {
				writeAnalyticsWarmup503(w)
				return
			}
		}
		data := s.store.GetAnalyticsTopologyWithWindow(region, area, window)
		if s.cfg != nil && len(s.cfg.NodeBlacklist) > 0 {
			data = s.filterBlacklistedFromTopology(data)
		}
		writeJSON(w, data)
		return
	}
	writeJSON(w, TopologyResponse{
		HopDistribution:  []TopologyHopDist{},
		TopRepeaters:     []TopRepeater{},
		TopPairs:         []TopPair{},
		HopsVsSnr:        []HopsVsSnr{},
		Observers:        []ObserverRef{},
		PerObserverReach: map[string]*ObserverReach{},
		MultiObsNodes:    []MultiObsNode{},
		BestPathList:     []BestPathEntry{},
	})
}

func (s *Server) handleAnalyticsChannels(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		region := r.URL.Query().Get("region")
		area := r.URL.Query().Get("area")
		window := ParseTimeWindow(r)
		// #1659 warmup gate (see handleAnalyticsRF for rationale).
		if region == "" && area == "" && window.IsZero() {
			s.store.analyticsRecomputerMu.RLock()
			rc := s.store.recompChannels
			s.store.analyticsRecomputerMu.RUnlock()
			if rc != nil && rc.IsWarmingUp_1659() {
				writeAnalyticsWarmup503(w)
				return
			}
		}
		writeJSON(w, s.store.GetAnalyticsChannelsWithWindow(region, area, window))
		return
	}
	channels, _ := s.db.GetChannels()
	if channels == nil {
		channels = make([]map[string]interface{}, 0)
	}
	writeJSON(w, ChannelAnalyticsResponse{
		ActiveChannels:  len(channels),
		Decryptable:     len(channels),
		Channels:        []ChannelAnalyticsSummary{},
		TopSenders:      []TopSender{},
		ChannelTimeline: []ChannelTimelineEntry{},
		MsgLengths:      []int{},
	})
}

func (s *Server) handleAnalyticsDistance(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	area := r.URL.Query().Get("area")
	if s.store != nil {
		// Lazy build (#1011): distance index is not built at startup.
		// First request triggers an async build and gets 202 +
		// Retry-After; concurrent requests during the build window
		// also get 202. Cached results (after the build completes)
		// are served as 200 from the analytics recomputer / TTL cache.
		if !s.store.DistanceIndexBuilt() {
			s.store.TriggerDistanceIndexBuild()
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"building","retry_after_seconds":5,"detail":"distance index is being computed (lazy build, #1011). Retry after Retry-After seconds."}`))
			return
		}
		writeJSON(w, s.store.GetAnalyticsDistance(region, area))
		return
	}
	writeJSON(w, DistanceAnalyticsResponse{
		Summary:       DistanceSummary{},
		TopHops:       []DistanceHop{},
		TopPaths:      []DistancePath{},
		CatStats:      map[string]*CategoryDistStats{},
		DistHistogram: nil,
		DistOverTime:  []DistOverTimeEntry{},
	})
}

func (s *Server) handleAnalyticsHashSizes(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		region := r.URL.Query().Get("region")
		area := r.URL.Query().Get("area")
		writeJSON(w, s.store.GetAnalyticsHashSizes(region, area))
		return
	}
	writeJSON(w, map[string]interface{}{
		"total":                   0,
		"distribution":            map[string]int{"1": 0, "2": 0, "3": 0},
		"distributionByRepeaters": map[string]int{"1": 0, "2": 0, "3": 0},
		"hourly":                  []HashSizeHourly{},
		"topHops":                 []HashSizeHop{},
		"multiByteNodes":          []MultiByteNode{},
	})
}

func (s *Server) handleAnalyticsHashCollisions(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		region := r.URL.Query().Get("region")
		area := r.URL.Query().Get("area")
		writeJSON(w, s.store.GetAnalyticsHashCollisions(region, area))
		return
	}
	writeJSON(w, map[string]interface{}{
		"inconsistent_nodes": []interface{}{},
		"by_size":            map[string]interface{}{},
	})
}

func (s *Server) handleAnalyticsSubpaths(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		if !s.store.SubpathIndexReady() {
			writeIndexLoading503(w)
			return
		}
		region := r.URL.Query().Get("region")
		minLen := queryInt(r, "minLen", 2)
		if minLen < 2 {
			minLen = 2
		}
		maxLen := queryInt(r, "maxLen", 8)
		limit := queryLimit(r, 100, s.cfg.ListLimits.AnalyticsMax)
		// Issue #1217: honor the Time window filter on Route Patterns.
		window := ParseTimeWindow(r)
		data := s.store.GetAnalyticsSubpathsWithWindow(region, minLen, maxLen, limit, window)
		if s.cfg != nil && len(s.cfg.NodeBlacklist) > 0 {
			data = s.filterBlacklistedFromSubpaths(data)
		}
		writeJSON(w, data)
		return
	}
	writeJSON(w, SubpathsResponse{
		Subpaths:   []SubpathResp{},
		TotalPaths: 0,
	})
}

// handleAnalyticsSubpathsBulk returns multiple length-range buckets in a single
// response, avoiding repeated scans of the same packet data. Query format:
//
//	?groups=2-2:50,3-3:30,4-4:20,5-8:15   (minLen-maxLen:limit per group)
func (s *Server) handleAnalyticsSubpathsBulk(w http.ResponseWriter, r *http.Request) {
	if s.store != nil && !s.store.SubpathIndexReady() {
		writeIndexLoading503(w)
		return
	}
	region := r.URL.Query().Get("region")
	groupsParam := r.URL.Query().Get("groups")
	if groupsParam == "" {
		writeJSON(w, ErrorResp{Error: "groups parameter required (e.g. groups=2-2:50,3-3:30)"})
		return
	}

	var groups []subpathGroup
	for _, g := range strings.Split(groupsParam, ",") {
		parts := strings.SplitN(g, ":", 2)
		if len(parts) != 2 {
			writeJSON(w, ErrorResp{Error: "invalid group format: " + g})
			return
		}
		rangeParts := strings.SplitN(parts[0], "-", 2)
		if len(rangeParts) != 2 {
			writeJSON(w, ErrorResp{Error: "invalid range format: " + parts[0]})
			return
		}
		mn, err1 := strconv.Atoi(rangeParts[0])
		mx, err2 := strconv.Atoi(rangeParts[1])
		lim, err3 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || err3 != nil || mn < 2 || mx < mn || lim < 1 {
			writeJSON(w, ErrorResp{Error: "invalid group: " + g})
			return
		}
		// Uniform clamp per audit-input-vulns-20260603 (MEDIUM): match the
		// /api/analytics/subpaths ceiling so a single bulk request can't
		// allocate more than the per-group endpoint allows.
		if lim > 200 {
			lim = 200
		}
		groups = append(groups, subpathGroup{mn, mx, lim})
	}

	if s.store == nil {
		results := make([]map[string]interface{}, len(groups))
		for i := range groups {
			results[i] = map[string]interface{}{"subpaths": []interface{}{}, "totalPaths": 0}
		}
		writeJSON(w, map[string]interface{}{"results": results})
		return
	}

	results := s.store.GetAnalyticsSubpathsBulkWithWindow(region, groups, ParseTimeWindow(r))
	if s.cfg != nil && len(s.cfg.NodeBlacklist) > 0 {
		for i, r := range results {
			results[i] = s.filterBlacklistedFromSubpaths(r)
		}
	}
	writeJSON(w, map[string]interface{}{"results": results})
}

// subpathGroup defines a length-range + limit for the bulk subpaths endpoint.
type subpathGroup struct {
	MinLen, MaxLen, Limit int
}

func (s *Server) handleAnalyticsSubpathDetail(w http.ResponseWriter, r *http.Request) {
	hops := r.URL.Query().Get("hops")
	if hops == "" {
		writeJSON(w, ErrorResp{Error: "Need at least 2 hops"})
		return
	}
	rawHops := strings.Split(hops, ",")
	if len(rawHops) < 2 {
		writeJSON(w, ErrorResp{Error: "Need at least 2 hops"})
		return
	}
	// Reject if any hop is a blacklisted or hidden-prefix node (#1181).
	if s.cfg != nil && (len(s.cfg.NodeBlacklist) > 0 || len(s.cfg.HiddenNamePrefixes) > 0) {
		for _, hop := range rawHops {
			if s.cfg.IsBlacklisted(hop) || s.isPubkeyHidden(hop) {
				writeError(w, 404, "Not found")
				return
			}
		}
	}
	if s.store != nil {
		if !s.store.SubpathIndexReady() {
			writeIndexLoading503(w)
			return
		}
		writeJSON(w, s.store.GetSubpathDetail(rawHops))
		return
	}
	writeJSON(w, SubpathDetailResponse{
		Hops:             rawHops,
		Nodes:            []SubpathNode{},
		TotalMatches:     0,
		FirstSeen:        nil,
		LastSeen:         nil,
		Signal:           SubpathSignal{AvgSnr: nil, AvgRssi: nil, Samples: 0},
		HourDistribution: make([]int, 24),
		ParentPaths:      []ParentPath{},
		Observers:        []SubpathObserver{},
	})
}

// --- Other Handlers ---

func (s *Server) handleResolveHops(w http.ResponseWriter, r *http.Request) {
	hopsParam := r.URL.Query().Get("hops")
	if hopsParam == "" {
		writeJSON(w, ResolveHopsResponse{Resolved: map[string]*HopResolution{}})
		return
	}
	hops := strings.Split(hopsParam, ",")
	resolved := map[string]*HopResolution{}

	// Context for affinity-based disambiguation.
	fromNode := r.URL.Query().Get("from_node")
	observer := r.URL.Query().Get("observer")
	var contextPubkeys []string
	if fromNode != "" {
		contextPubkeys = append(contextPubkeys, fromNode)
	}
	if observer != "" {
		contextPubkeys = append(contextPubkeys, observer)
	}

	// Get the neighbor graph for affinity scoring (may be nil).
	var graph *NeighborGraph
	if len(contextPubkeys) > 0 {
		graph = s.getNeighborGraph()
	}

	// Get the server's prefix map for resolveWithContext.
	var pm *prefixMap
	if s.store != nil {
		s.store.mu.RLock()
		_, pm = s.store.getCachedNodesAndPM()
		s.store.mu.RUnlock()
	}

	for _, hop := range hops {
		if hop == "" {
			continue
		}
		hopLower := strings.ToLower(hop)

		// Resolve candidates from the in-memory prefix map instead of
		// issuing per-hop DB queries (fixes N+1 pattern, see #369).
		var candidates []HopCandidate
		if pm != nil {
			if matched, ok := pm.m[hopLower]; ok {
				for _, ni := range matched {
					// Skip blacklisted nodes from resolution results.
					if s.cfg != nil && s.cfg.IsBlacklisted(ni.PublicKey) {
						continue
					}
					// #1181: skip hidden-prefix nodes too. We have the
					// name on ni so no extra DB lookup is needed.
					if s.cfg != nil && s.cfg.IsNameHidden(ni.Name) {
						continue
					}
					c := HopCandidate{Pubkey: ni.PublicKey}
					if ni.Name != "" {
						c.Name = ni.Name
					}
					if ni.HasGPS {
						c.Lat = ni.Lat
						c.Lon = ni.Lon
					}
					candidates = append(candidates, c)
				}
			}
		}

		if len(candidates) == 0 {
			resolved[hop] = &HopResolution{Name: nil, Candidates: []HopCandidate{}, Conflicts: []interface{}{}, Confidence: "no_match"}
		} else if len(candidates) == 1 {
			resolved[hop] = &HopResolution{
				Name: candidates[0].Name, Pubkey: candidates[0].Pubkey,
				Candidates: candidates, Conflicts: []interface{}{},
				Confidence: "unique_prefix",
			}
		} else {
			// Compute affinity scores for each candidate if we have context.
			if graph != nil && len(contextPubkeys) > 0 {
				now := time.Now()
				for i := range candidates {
					candPK := strings.ToLower(candidates[i].Pubkey)
					bestScore := 0.0
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
								sc := e.Score(now)
								if sc > bestScore {
									bestScore = sc
								}
							}
						}
					}
					if bestScore > 0 {
						s := bestScore
						candidates[i].AffinityScore = &s
					}
				}
			}

			// Use resolveWithContext for 4-tier disambiguation.
			var best *nodeInfo
			var confidence string
			if pm != nil {
				best, confidence, _ = pm.resolveWithContext(hopLower, contextPubkeys, graph)
			}

			ambig := true
			hr := &HopResolution{
				Name: candidates[0].Name, Pubkey: candidates[0].Pubkey,
				Ambiguous: &ambig, Candidates: candidates, Conflicts: hopCandidatesToConflicts(candidates),
				Confidence: "ambiguous",
			}

			// Use the resolved node as the default (best-effort pick).
			// Skip if the best pick is blacklisted or has a hidden-prefix
			// name (#1181).
			if best != nil && !(s.cfg != nil && (s.cfg.IsBlacklisted(best.PublicKey) || s.cfg.IsNameHidden(best.Name))) {
				hr.Name = best.Name
				hr.Pubkey = best.PublicKey
			}

			// Only promote to bestCandidate when affinity is confident.
			if confidence == "neighbor_affinity" && best != nil {
				pk := best.PublicKey
				hr.BestCandidate = &pk
				hr.Confidence = "neighbor_affinity"
			} else if (confidence == "geo_proximity" || confidence == "gps_preference" || confidence == "observation_count_fallback") && best != nil {
				// Propagate lower-priority tiers so the API reflects the actual
				// resolution strategy used, rather than collapsing everything to "ambiguous".
				hr.Confidence = confidence
			}

			resolved[hop] = hr
		}
	}
	writeJSON(w, ResolveHopsResponse{Resolved: resolved})
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	includeEncrypted := r.URL.Query().Get("includeEncrypted") == "true"
	// Prefer DB for full history (in-memory store has limited retention)
	if s.db != nil {
		channels, err := s.db.GetChannels(region)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if includeEncrypted {
			encrypted, err := s.db.GetEncryptedChannels(region)
			if err != nil {
				log.Printf("WARN GetEncryptedChannels: %v", err)
			} else {
				channels = append(channels, encrypted...)
			}
		}
		writeJSON(w, ChannelListResponse{Channels: channels})
		return
	}
	if s.store != nil {
		channels := s.store.GetChannels(region)
		if includeEncrypted {
			channels = append(channels, s.store.GetEncryptedChannels(region)...)
		}
		writeJSON(w, ChannelListResponse{Channels: channels})
		return
	}
	writeJSON(w, ChannelListResponse{Channels: []map[string]interface{}{}})
}

func (s *Server) handleChannelMessages(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	limit := queryLimit(r, 100, s.cfg.ListLimits.ChannelMessagesMax)
	offset := queryInt(r, "offset", 0)
	region := r.URL.Query().Get("region")
	// Prefer DB for full history (in-memory store has limited retention)
	if s.db != nil {
		messages, total, err := s.db.GetChannelMessages(hash, limit, offset, region)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, ChannelMessagesResponse{Messages: messages, Total: total})
		return
	}
	if s.store != nil {
		messages, total := s.store.GetChannelMessages(hash, limit, offset, region)
		writeJSON(w, ChannelMessagesResponse{Messages: messages, Total: total})
		return
	}
	writeJSON(w, ChannelMessagesResponse{Messages: []map[string]interface{}{}, Total: 0})
}

func (s *Server) handleObservers(w http.ResponseWriter, r *http.Request) {
	// #1481 P0-3 + #1483: serve from 30s atomic-pointer cache for the
	// default (no-filter) query shape. Refill is collapsed via
	// singleflight so concurrent TTL-boundary requests do not stampede
	// the 1.9M-row observations table.
	isDefault := r.URL.RawQuery == ""
	if isDefault {
		if e, ok := s.loadObserversCache(); ok && !s.observersCacheExpired(e.at) {
			w.Header().Set("X-Cache-Age-Seconds", cacheAgeSecondsHeader(time.Since(e.at)))
			writeJSON(w, e.resp)
			return
		}
	}

	if isDefault {
		v, err, _ := s.observersCacheV2.sf.Do(observersCacheFlightKey, func() (interface{}, error) {
			// Double-check inside the singleflight: another winner
			// may have just stored a fresh entry.
			if e, ok := s.loadObserversCache(); ok && !s.observersCacheExpired(e.at) {
				return e, nil
			}
			resp, herr := s.buildObserversDefaultResponse()
			if herr != nil {
				return nil, herr
			}
			s.observersCacheV2.fillCount.Add(1)
			entry := &observersCacheEntry{resp: resp, at: time.Now()}
			s.observersCacheV2.ptr.Store(entry)
			return entry, nil
		})
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		entry := v.(*observersCacheEntry)
		w.Header().Set("X-Cache-Age-Seconds", cacheAgeSecondsHeader(time.Since(entry.at)))
		writeJSON(w, entry.resp)
		return
	}

	// Non-default queries bypass the cache entirely (filters not yet wired).
	resp, err := s.buildObserversDefaultResponse()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, resp)
}

// buildObserversDefaultResponse runs the underlying SQL pipeline for
// the default-shape /api/observers payload. Extracted so the cache
// refill path can be wrapped in singleflight and counted by tests.
// #1483 follow-up.
func (s *Server) buildObserversDefaultResponse() (ObserverListResponse, error) {
	observers, err := s.db.GetObservers()
	if err != nil {
		return ObserverListResponse{}, err
	}

	// Batch lookup: packetsLastHour per observer
	oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
	pktCounts := s.db.GetObserverPacketCounts(oneHourAgo)

	// Batch lookup: node locations only for observer IDs (not all nodes)
	observerIDs := make([]string, len(observers))
	for i, o := range observers {
		observerIDs[i] = o.ID
	}
	nodeLocations := s.db.GetNodeLocationsByKeys(observerIDs)

	result := make([]ObserverResp, 0, len(observers))
	nowTime := time.Now().UTC()
	for i := range observers {
		o := &observers[i]
		// Defense in depth: skip observers that are in the blacklist
		if s.cfg != nil && s.cfg.IsObserverBlacklisted(o.ID) {
			continue
		}
		plh := 0
		if c, ok := pktCounts[o.ID]; ok {
			plh = c
		}
		var lat, lon, nodeRole interface{}
		if nodeLoc, ok := nodeLocations[strings.ToLower(o.ID)]; ok {
			lat = nodeLoc["lat"]
			lon = nodeLoc["lon"]
			nodeRole = nodeLoc["role"]
		}

		resp := ObserverResp{
			ID: o.ID, Name: o.Name, IATA: o.IATA,
			LastSeen: o.LastSeen, FirstSeen: o.FirstSeen,
			PacketCount: o.PacketCount,
			Model:       o.Model, Firmware: o.Firmware,
			ClientVersion: o.ClientVersion, Radio: o.Radio,
			BatteryMv: o.BatteryMv, UptimeSecs: o.UptimeSecs,
			NoiseFloor:      o.NoiseFloor,
			LastPacketAt:    o.LastPacketAt,
			PacketsLastHour: plh,
			Lat:             lat, Lon: lon, NodeRole: nodeRole,
			CanRelay: o.CanRelay,
		}
		applyObserverNaiveClock(&resp, o, nowTime)
		result = append(result, resp)
	}
	return ObserverListResponse{
		Observers:  result,
		ServerTime: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (s *Server) handleObserverDetail(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	// Defense in depth: reject blacklisted observer
	if s.cfg != nil && s.cfg.IsObserverBlacklisted(id) {
		writeError(w, 404, "Observer not found")
		return
	}

	obs, err := s.db.GetObserverByID(id)
	if err != nil || obs == nil {
		writeError(w, 404, "Observer not found")
		return
	}

	// Compute packetsLastHour from observations
	oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
	pktCounts := s.db.GetObserverPacketCounts(oneHourAgo)
	plh := 0
	if c, ok := pktCounts[id]; ok {
		plh = c
	}

	writeJSON(w, func() ObserverResp {
		resp := ObserverResp{
			ID: obs.ID, Name: obs.Name, IATA: obs.IATA,
			LastSeen: obs.LastSeen, FirstSeen: obs.FirstSeen,
			PacketCount: obs.PacketCount,
			Model:       obs.Model, Firmware: obs.Firmware,
			ClientVersion: obs.ClientVersion, Radio: obs.Radio,
			BatteryMv: obs.BatteryMv, UptimeSecs: obs.UptimeSecs,
			NoiseFloor:      obs.NoiseFloor,
			LastPacketAt:    obs.LastPacketAt,
			PacketsLastHour: plh,
			CanRelay:        obs.CanRelay,
		}
		applyObserverNaiveClock(&resp, obs, time.Now().UTC())
		return resp
	}())
}

func (s *Server) handleObserverAnalytics(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}
	if s.store == nil {
		writeError(w, 503, "Packet store unavailable")
		return
	}

	// #1828 Phase A: aggregate builders extracted into observer_analytics.go.
	// Single snapshot under RLock (#1481 P0-2), then five composable helpers
	// off the snapshot. No behavior change; JSON output is byte-identical.
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	s.store.mu.RLock()
	obsList := s.store.byObserver[id]
	obsSnapshot := make([]*StoreObs, len(obsList))
	copy(obsSnapshot, obsList)
	// #1830: also resolve each referenced transmission's *StoreTx under
	// this same RLock. s.store.byTxID is guarded by s.store.mu (writes
	// from ingest/eviction); reading it after RUnlock — as the loop below
	// used to via s.store.byTxID[...] and enrichObs() — races with those
	// writers. Keyed by TransmissionID (not by observation index) since
	// multiple observations can share one transmission.
	txByID := make(map[int]*StoreTx, len(obsSnapshot))
	for _, obs := range obsSnapshot {
		if _, ok := txByID[obs.TransmissionID]; !ok {
			txByID[obs.TransmissionID] = s.store.byTxID[obs.TransmissionID]
		}
	}
	s.store.mu.RUnlock()
	filtered := make([]*StoreObs, 0, len(obsSnapshot))
	for _, obs := range obsSnapshot {
		t, ok := obs.ParsedTime()
		if !ok {
			continue
		}
		if t.Equal(since) || t.After(since) {
			filtered = append(filtered, obs)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Timestamp > filtered[j].Timestamp })

	writeJSON(w, ObserverAnalyticsResponse{
		Timeline:        buildTimeline(filtered, days),
		PacketTypes:     buildPacketTypes(filtered, txByID),
		NodesTimeline:   buildNodesTimeline(filtered, days, txByID),
		SnrDistribution: buildSnrDistribution(filtered),
		RecentPackets:   buildRecentPackets(s.store, filtered, 20, txByID),
	})
}

func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	traces, err := s.db.GetTraces(hash)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, TraceResponse{Traces: traces})
}

var iataCoords = map[string]IataCoord{
	"SJC": {Lat: 37.3626, Lon: -121.929},
	"SFO": {Lat: 37.6213, Lon: -122.379},
	"OAK": {Lat: 37.7213, Lon: -122.2208},
	"SEA": {Lat: 47.4502, Lon: -122.3088},
	"PDX": {Lat: 45.5898, Lon: -122.5951},
	"LAX": {Lat: 33.9425, Lon: -118.4081},
	"SAN": {Lat: 32.7338, Lon: -117.1933},
	"SMF": {Lat: 38.6954, Lon: -121.5908},
	"APC": {Lat: 38.2132, Lon: -122.2807}, // Napa County
	"STS": {Lat: 38.509, Lon: -122.8128},  // Charles M. Schulz–Sonoma County
	"MRY": {Lat: 36.587, Lon: -121.843},
	"EUG": {Lat: 44.1246, Lon: -123.2119},
	"RDD": {Lat: 40.509, Lon: -122.2934},
	"MFR": {Lat: 42.3742, Lon: -122.8735},
	"FAT": {Lat: 36.7762, Lon: -119.7181},
	"SBA": {Lat: 34.4262, Lon: -119.8405},
	"RNO": {Lat: 39.4991, Lon: -119.7681},
	"BOI": {Lat: 43.5644, Lon: -116.2228},
	"LAS": {Lat: 36.084, Lon: -115.1537},
	"PHX": {Lat: 33.4373, Lon: -112.0078},
	"SLC": {Lat: 40.7884, Lon: -111.9778},
	"DEN": {Lat: 39.8561, Lon: -104.6737},
	"DFW": {Lat: 32.8998, Lon: -97.0403},
	"IAH": {Lat: 29.9844, Lon: -95.3414},
	"AUS": {Lat: 30.1975, Lon: -97.6664},
	"MSP": {Lat: 44.8848, Lon: -93.2223},
	"ATL": {Lat: 33.6407, Lon: -84.4277},
	"ORD": {Lat: 41.9742, Lon: -87.9073},
	"JFK": {Lat: 40.6413, Lon: -73.7781},
	"EWR": {Lat: 40.6895, Lon: -74.1745},
	"BOS": {Lat: 42.3656, Lon: -71.0096},
	"MIA": {Lat: 25.7959, Lon: -80.287},
	"IAD": {Lat: 38.9531, Lon: -77.4565},
	"CLT": {Lat: 35.2144, Lon: -80.9473},
	"DTW": {Lat: 42.2124, Lon: -83.3534},
	"MCO": {Lat: 28.4312, Lon: -81.3081},
	"BNA": {Lat: 36.1263, Lon: -86.6774},
	"RDU": {Lat: 35.8801, Lon: -78.788},
	"YVR": {Lat: 49.1967, Lon: -123.1815},
	"YYZ": {Lat: 43.6777, Lon: -79.6248},
	"YYC": {Lat: 51.1215, Lon: -114.0076},
	"YEG": {Lat: 53.3097, Lon: -113.58},
	"YOW": {Lat: 45.3225, Lon: -75.6692},
	"LHR": {Lat: 51.47, Lon: -0.4543},
	"CDG": {Lat: 49.0097, Lon: 2.5479},
	"FRA": {Lat: 50.0379, Lon: 8.5622},
	"AMS": {Lat: 52.3105, Lon: 4.7683},
	"MUC": {Lat: 48.3537, Lon: 11.775},
	"SOF": {Lat: 42.6952, Lon: 23.4062},
	"NRT": {Lat: 35.772, Lon: 140.3929},
	"HND": {Lat: 35.5494, Lon: 139.7798},
	"ICN": {Lat: 37.4602, Lon: 126.4407},
	"SYD": {Lat: -33.9461, Lon: 151.1772},
	"MEL": {Lat: -37.669, Lon: 144.841},
}

func (s *Server) handleIATACoords(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, IataCoordsResponse{Coords: iataCoords})
}

func (s *Server) handleAudioLabBuckets(w http.ResponseWriter, r *http.Request) {
	buckets := map[string][]AudioLabPacket{}

	if s.store != nil {
		// Use in-memory store (matches Node.js pktStore.packets approach)
		s.store.mu.RLock()
		byType := map[string][]*StoreTx{}
		for _, tx := range s.store.packets {
			if tx.RawHex == "" {
				continue
			}
			typeName := "UNKNOWN"
			if tx.DecodedJSON != "" {
				if d := tx.ParsedDecoded(); d != nil {
					if t, ok := d["type"].(string); ok && t != "" {
						typeName = t
					}
				}
			}
			if typeName == "UNKNOWN" && tx.PayloadType != nil {
				if name, ok := payloadTypeNames[*tx.PayloadType]; ok {
					typeName = name
				}
			}
			byType[typeName] = append(byType[typeName], tx)
		}
		s.store.mu.RUnlock()

		for typeName, pkts := range byType {
			sort.Slice(pkts, func(i, j int) bool {
				return len(pkts[i].RawHex) < len(pkts[j].RawHex)
			})
			count := min(8, len(pkts))
			picked := make([]AudioLabPacket, 0, count)
			for i := 0; i < count; i++ {
				idx := (i * len(pkts)) / count
				tx := pkts[idx]
				pt := 0
				if tx.PayloadType != nil {
					pt = *tx.PayloadType
				}
				picked = append(picked, AudioLabPacket{
					Hash:             strOrNil(tx.Hash),
					RawHex:           strOrNil(tx.RawHex),
					DecodedJSON:      strOrNil(tx.DecodedJSON),
					ObservationCount: max(tx.ObservationCount, 1),
					PayloadType:      pt,
					PathJSON:         strOrNil(tx.PathJSON),
					ObserverID:       strOrNil(tx.ObserverID),
					Timestamp:        strOrNil(tx.FirstSeen),
				})
			}
			buckets[typeName] = picked
		}
	}

	writeJSON(w, AudioLabBucketsResponse{Buckets: buckets})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[routes] JSON encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// normaliseTypeColorsLegacyKeys returns a copy of `m` with the legacy
// "REQUEST" key renamed to the canonical "REQ". If both keys are present
// the canonical wins (caller already chose the new name explicitly).
// #1799 PR #1804 r1 item 6: keeps stale operator config.json working
// after the REQUEST→REQ rename for >=1 release cycle. Returns nil for
// a nil input so mergeMap's nil-skip continues to work.
func normaliseTypeColorsLegacyKeys(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	if legacy, ok := out["REQUEST"]; ok {
		if _, hasCanon := out["REQ"]; !hasCanon {
			out["REQ"] = legacy
		}
		delete(out, "REQUEST")
	}
	return out
}

func mergeMap(base map[string]interface{}, overlays ...map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range base {
		result[k] = v
	}
	for _, o := range overlays {
		if o == nil {
			continue
		}
		for k, v := range o {
			result[k] = v
		}
	}
	return result
}

func safeAvg(total, count float64) float64 {
	if count == 0 {
		return 0
	}
	return round(total/count, 1)
}

func round(val float64, places int) float64 {
	m := 1.0
	for i := 0; i < places; i++ {
		m *= 10
	}
	return float64(int(val*m+0.5)) / m
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)) * p)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func sortedCopy(arr []float64) []float64 {
	cp := make([]float64, len(arr))
	copy(cp, arr)
	sort.Float64s(cp)
	return cp
}

func lastN(arr []map[string]interface{}, n int) []map[string]interface{} {
	if len(arr) <= n {
		return arr
	}
	return arr[len(arr)-n:]
}

// mapSliceToTransmissions converts []map[string]interface{} to []TransmissionResp
// for type-safe JSON encoding. Used during transition from map-based to struct-based responses.
func mapSliceToTransmissions(maps []map[string]interface{}) []TransmissionResp {
	result := make([]TransmissionResp, 0, len(maps))
	for _, m := range maps {
		tx := TransmissionResp{
			Hash:      strVal(m["hash"]),
			FirstSeen: strVal(m["first_seen"]),
			Timestamp: strVal(m["first_seen"]),
		}
		if v, ok := m["id"].(int); ok {
			tx.ID = v
		}
		tx.RawHex = m["raw_hex"]
		tx.RouteType = m["route_type"]
		tx.PayloadType = m["payload_type"]
		tx.PayloadVersion = m["payload_version"]
		tx.DecodedJSON = m["decoded_json"]
		if v, ok := m["observation_count"].(int); ok {
			tx.ObservationCount = v
		}
		tx.ObserverID = m["observer_id"]
		tx.ObserverName = m["observer_name"]
		tx.ObserverIATA = m["observer_iata"]
		tx.SNR = m["snr"]
		tx.RSSI = m["rssi"]
		tx.PathJSON = m["path_json"]
		tx.Direction = m["direction"]
		tx.Score = m["score"]
		result = append(result, tx)
	}
	return result
}

// mapSliceToObservations converts []map[string]interface{} to []ObservationResp.
func mapSliceToObservations(maps []map[string]interface{}) []ObservationResp {
	result := make([]ObservationResp, 0, len(maps))
	for _, m := range maps {
		obs := ObservationResp{}
		if v, ok := m["id"].(int); ok {
			obs.ID = v
		}
		obs.TransmissionID = m["transmission_id"]
		obs.Hash = m["hash"]
		obs.ObserverID = m["observer_id"]
		obs.ObserverName = m["observer_name"]
		obs.ObserverIATA = m["observer_iata"]
		obs.SNR = m["snr"]
		obs.RSSI = m["rssi"]
		obs.PathJSON = m["path_json"]
		obs.ResolvedPath = m["resolved_path"]
		obs.Direction = m["direction"]
		obs.RawHex = m["raw_hex"]
		obs.Timestamp = m["timestamp"]
		result = append(result, obs)
	}
	return result
}

func strVal(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// hopCandidatesToConflicts converts typed candidates to interface slice for JSON.
func hopCandidatesToConflicts(candidates []HopCandidate) []interface{} {
	result := make([]interface{}, len(candidates))
	for i, c := range candidates {
		result[i] = c
	}
	return result
}

// nullFloatVal extracts float64 from sql.NullFloat64, returning 0 if null.
func nullFloatVal(n sql.NullFloat64) float64 {
	if n.Valid {
		return n.Float64
	}
	return 0
}

func (s *Server) handleObserverMetrics(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	since := r.URL.Query().Get("since")
	until := r.URL.Query().Get("until")
	resolution := r.URL.Query().Get("resolution")

	// Default to last 24h if no since provided
	if since == "" {
		since = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	}

	// Validate resolution
	if resolution == "" {
		resolution = "5m"
	}
	switch resolution {
	case "5m", "1h", "1d":
		// valid
	default:
		writeError(w, 400, "invalid resolution: "+resolution+". Must be 5m, 1h, or 1d")
		return
	}

	// Sample interval (default 300s = 5min)
	sampleInterval := 300

	metrics, reboots, err := s.db.GetObserverMetrics(id, since, until, resolution, sampleInterval)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if metrics == nil {
		metrics = []MetricsSample{}
	}
	if reboots == nil {
		reboots = []string{}
	}

	// Get observer name
	obs, _ := s.db.GetObserverByID(id)
	var name *string
	if obs != nil {
		name = obs.Name
	}

	writeJSON(w, map[string]interface{}{
		"observer_id":   id,
		"observer_name": name,
		"reboots":       reboots,
		"metrics":       metrics,
	})
}

func (s *Server) handleMetricsSummary(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	region := r.URL.Query().Get("region")

	// Parse window duration
	dur, err := parseWindowDuration(window)
	if err != nil {
		writeError(w, 400, "invalid window: "+window)
		return
	}

	since := time.Now().UTC().Add(-dur).Format(time.RFC3339)
	summary, err := s.db.GetMetricsSummary(since)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if summary == nil {
		summary = []MetricsSummaryRow{}
	}

	// Filter by region if specified
	if region != "" {
		filtered := make([]MetricsSummaryRow, 0)
		for _, row := range summary {
			if strings.EqualFold(row.IATA, region) {
				filtered = append(filtered, row)
			}
		}
		summary = filtered
	}

	writeJSON(w, map[string]interface{}{
		"observers": summary,
	})
}

// parseWindowDuration parses strings like "24h", "3d", "7d", "30d".
func parseWindowDuration(window string) (time.Duration, error) {
	if strings.HasSuffix(window, "d") {
		daysStr := strings.TrimSuffix(window, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid days: %s", daysStr)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(window)
}

// handleAdminPrune was removed in #1283. Prune now runs in the ingestor
// process (server is read-only). The function and route are gone.

// constantTimeEqual compares two strings in constant time to prevent timing attacks.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// filterBlacklistedFromTopology removes blacklisted + hidden-prefix node
// references (#1181) from the topology analytics response (TopRepeaters,
// TopPairs, BestPathList, MultiObsNodes, PerObserverReach).
func (s *Server) filterBlacklistedFromTopology(data map[string]interface{}) map[string]interface{} {
	// Filter TopRepeaters
	if repeaters, ok := data["topRepeaters"]; ok {
		if arr, ok := repeaters.([]TopRepeater); ok {
			var filtered []TopRepeater
			for _, r := range arr {
				if pk, ok := r.Pubkey.(string); ok && s.cfg.IsBlacklisted(pk) {
					continue
				}
				if name, ok := r.Name.(string); ok && s.cfg.IsNameHidden(name) {
					continue
				}
				filtered = append(filtered, r)
			}
			data["topRepeaters"] = filtered
		}
	}

	// Filter TopPairs
	if pairs, ok := data["topPairs"]; ok {
		if arr, ok := pairs.([]TopPair); ok {
			var filtered []TopPair
			for _, p := range arr {
				if pkA, ok := p.PubkeyA.(string); ok && s.cfg.IsBlacklisted(pkA) {
					continue
				}
				if pkB, ok := p.PubkeyB.(string); ok && s.cfg.IsBlacklisted(pkB) {
					continue
				}
				if nameA, ok := p.NameA.(string); ok && s.cfg.IsNameHidden(nameA) {
					continue
				}
				if nameB, ok := p.NameB.(string); ok && s.cfg.IsNameHidden(nameB) {
					continue
				}
				filtered = append(filtered, p)
			}
			data["topPairs"] = filtered
		}
	}

	// Filter BestPathList
	if paths, ok := data["bestPathList"]; ok {
		if arr, ok := paths.([]BestPathEntry); ok {
			var filtered []BestPathEntry
			for _, p := range arr {
				if pk, ok := p.Pubkey.(string); ok && s.cfg.IsBlacklisted(pk) {
					continue
				}
				if pk, ok := p.Pubkey.(string); ok && s.isPubkeyHidden(pk) {
					continue
				}
				filtered = append(filtered, p)
			}
			data["bestPathList"] = filtered
		}
	}

	// Filter MultiObsNodes
	if nodes, ok := data["multiObsNodes"]; ok {
		if arr, ok := nodes.([]MultiObsNode); ok {
			var filtered []MultiObsNode
			for _, n := range arr {
				if pk, ok := n.Pubkey.(string); ok && s.cfg.IsBlacklisted(pk) {
					continue
				}
				if name, ok := n.Name.(string); ok && s.cfg.IsNameHidden(name) {
					continue
				}
				filtered = append(filtered, n)
			}
			data["multiObsNodes"] = filtered
		}
	}

	// Filter PerObserverReach
	if reach, ok := data["perObserverReach"]; ok {
		if m, ok := reach.(map[string]*ObserverReach); ok {
			for k, v := range m {
				for ri := range v.Rings {
					var filteredNodes []ReachNode
					for _, rn := range v.Rings[ri].Nodes {
						if pk, ok := rn.Pubkey.(string); ok && s.cfg.IsBlacklisted(pk) {
							continue
						}
						if name, ok := rn.Name.(string); ok && s.cfg.IsNameHidden(name) {
							continue
						}
						filteredNodes = append(filteredNodes, rn)
					}
					v.Rings[ri].Nodes = filteredNodes
				}
				m[k] = v
			}
		}
	}

	return data
}

// filterBlacklistedFromSubpaths removes blacklisted node references from
// the subpaths analytics response.
func (s *Server) filterBlacklistedFromSubpaths(data map[string]interface{}) map[string]interface{} {
	if subpaths, ok := data["subpaths"]; ok {
		if arr, ok := subpaths.([]interface{}); ok {
			var filtered []interface{}
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					if hops, ok := m["hops"].([]interface{}); ok {
						skip := false
						for _, h := range hops {
							if hp, ok := h.(string); ok && (s.cfg.IsBlacklisted(hp) || s.isPubkeyHidden(hp)) {
								skip = true
								break
							}
						}
						if skip {
							continue
						}
					}
				}
				filtered = append(filtered, item)
			}
			data["subpaths"] = filtered
		}
	}
	return data
}

// handleDroppedPackets returns recently dropped packets for investigation.
func (s *Server) handleDroppedPackets(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 100, s.cfg.ListLimits.PacketsMax)
	observerID := r.URL.Query().Get("observer")
	nodePubkey := r.URL.Query().Get("pubkey")

	results, err := s.db.GetDroppedPackets(limit, observerID, nodePubkey)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, results)
}

func (s *Server) handleScopeStats(w http.ResponseWriter, r *http.Request) {
	const scopeStatsTTL = 30 * time.Second

	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	if window != "1h" && window != "24h" && window != "7d" {
		writeError(w, 400, "window must be 1h, 24h, or 7d")
		return
	}

	s.scopeStatsMu.Lock()
	if s.scopeStatsCache != nil {
		if cached, ok := s.scopeStatsCache[window]; ok && time.Since(s.scopeStatsCachedAt[window]) < scopeStatsTTL {
			s.scopeStatsMu.Unlock()
			writeJSON(w, cached)
			return
		}
	}
	s.scopeStatsMu.Unlock()

	resp, err := s.db.GetScopeStats(window)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	s.scopeStatsMu.Lock()
	if s.scopeStatsCache == nil {
		s.scopeStatsCache = make(map[string]*ScopeStatsResponse)
		s.scopeStatsCachedAt = make(map[string]time.Time)
	}
	s.scopeStatsCache[window] = resp
	s.scopeStatsCachedAt[window] = time.Now()
	s.scopeStatsMu.Unlock()

	writeJSON(w, resp)
}

// handlePruneGeoFilter identifies (dry_run=true, default) or enqueues (confirm=true)
// deletion of nodes whose GPS coordinates fall outside the currently configured
// geo_filter. Nodes with no GPS fix are always kept. Requires geo_filter to be
// configured.
//
// Since #1283/#1289 the server opens SQLite read-only, so the actual DELETE is
// performed by the ingestor. The server writes a request marker file (see
// internal/prunequeue); the ingestor's maintenance loop consumes it and writes a
// result marker. The confirm response is 202 Accepted with a request id;
// clients poll GET /api/admin/prune-geo-filter/status?id=<id> for completion.
//
// Confirm requires the pubkeys from the preview in the request body to prevent
// TOCTOU races: only nodes that were shown in preview AND are still outside the
// filter are enqueued.
func (s *Server) handlePruneGeoFilter(w http.ResponseWriter, r *http.Request) {
	gf := s.getGeoFilter()
	if gf == nil || len(gf.Polygon) < 3 {
		writeError(w, http.StatusBadRequest, "no geo_filter configured")
		return
	}

	nodes, err := s.db.GetNodesForGeoPrune()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	type nodeResult struct {
		PubKey string   `json:"pubkey"`
		Name   string   `json:"name"`
		Lat    *float64 `json:"lat"`
		Lon    *float64 `json:"lon"`
	}

	var outside []nodeResult
	for _, n := range nodes {
		if n.Lat == nil || n.Lon == nil {
			continue // no GPS — always keep
		}
		if !NodePassesGeoFilter(*n.Lat, *n.Lon, gf) {
			outside = append(outside, nodeResult{PubKey: n.PubKey, Name: n.Name, Lat: n.Lat, Lon: n.Lon})
		}
	}

	if r.URL.Query().Get("confirm") != "true" {
		// Dry run — return preview without enqueueing anything.
		writeJSON(w, map[string]interface{}{
			"dryRun": true,
			"count":  len(outside),
			"nodes":  outside,
		})
		return
	}

	// Confirmed enqueue — require pubkeys from the preview to prevent TOCTOU:
	// only nodes that were shown in preview AND are still outside the filter
	// at this exact moment are scheduled for deletion. (The ingestor honors
	// the list verbatim; it does NOT re-evaluate geo_filter membership.)
	var body struct {
		Pubkeys []string `json:"pubkeys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Pubkeys) == 0 {
		writeError(w, http.StatusBadRequest, "confirm requires pubkeys from preview in request body")
		return
	}
	allowed := make(map[string]bool, len(body.Pubkeys))
	for _, pk := range body.Pubkeys {
		allowed[pk] = true
	}

	var toDelete []nodeResult
	for _, n := range outside {
		if allowed[n.PubKey] {
			toDelete = append(toDelete, n)
		}
	}

	pubkeys := make([]string, 0, len(toDelete))
	for _, n := range toDelete {
		pubkeys = append(pubkeys, n.PubKey)
	}

	id := prunequeue.NewID()
	req := prunequeue.Request{
		ID:          id,
		RequestedAt: time.Now().UTC(),
		Reason:      "geo-prune",
		Pubkeys:     pubkeys,
	}
	if err := prunequeue.WriteRequest(s.db.path, req); err != nil {
		log.Printf("[geo-prune] failed to enqueue request %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to enqueue prune request")
		return
	}
	log.Printf("[geo-prune] enqueued request %s for %d node(s) (queue dir=%s)",
		id, len(pubkeys), prunequeue.QueueDir(s.db.path))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"dryRun":    false,
		"accepted":  true,
		"requestId": id,
		"count":     len(pubkeys),
		"nodes":     toDelete,
		"statusUrl": "/api/admin/prune-geo-filter/status?id=" + id,
	})
}

// handlePruneGeoFilterStatus reports the state of a previously-enqueued
// geo-prune request. While the request marker is still present the response is
// {"status":"pending"}. Once the ingestor writes a result, the response is
// {"status":"done","deleted":N,"completedAt":...} (or "error" if the ingestor
// failed). Returns 404 when neither marker nor result is found.
func (s *Server) handlePruneGeoFilterStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	res, err := prunequeue.ReadResult(s.db.path, id)
	if err != nil {
		if strings.Contains(err.Error(), "invalid prune request id") {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		writeError(w, http.StatusInternalServerError, "status read failed")
		return
	}
	if res != nil {
		status := "done"
		if res.Error != "" {
			status = "error"
		}
		writeJSON(w, map[string]interface{}{
			"requestId":   res.ID,
			"status":      status,
			"deleted":     res.Deleted,
			"requestedAt": res.RequestedAt,
			"completedAt": res.CompletedAt,
			"error":       res.Error,
		})
		return
	}

	pending, err := prunequeue.RequestExists(s.db.path, id)
	if err != nil {
		if strings.Contains(err.Error(), "invalid prune request id") {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		writeError(w, http.StatusInternalServerError, "status read failed")
		return
	}
	if pending {
		writeJSON(w, map[string]interface{}{
			"requestId": id,
			"status":    "pending",
		})
		return
	}
	writeError(w, http.StatusNotFound, "unknown request id")
}

// handlePutConfigGeoFilter writes the geo_filter config to disk and updates the
// in-memory pointer atomically. Empty/missing polygon clears the filter.
//
// Backstop validation: ≤1000 points, every point in [-90,90]/[-180,180], no
// NaN/Inf; bufferKm finite, non-negative, ≤ 20000 km. Concurrent PUTs are
// serialized via s.saveMu so they cannot race on the .tmp file.
func (s *Server) handlePutConfigGeoFilter(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB cap

	var body struct {
		Polygon  [][2]float64 `json:"polygon"`
		BufferKm float64      `json:"bufferKm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if len(body.Polygon) > 0 && len(body.Polygon) < 3 {
		writeError(w, http.StatusBadRequest, "polygon must have at least 3 points")
		return
	}
	if len(body.Polygon) > 1000 {
		writeError(w, http.StatusBadRequest, "polygon must have at most 1000 points")
		return
	}
	for _, pt := range body.Polygon {
		if math.IsNaN(pt[0]) || math.IsNaN(pt[1]) || math.IsInf(pt[0], 0) || math.IsInf(pt[1], 0) ||
			pt[0] < -90 || pt[0] > 90 || pt[1] < -180 || pt[1] > 180 {
			writeError(w, http.StatusBadRequest, "polygon point out of range: lat must be in [-90,90], lon in [-180,180]")
			return
		}
	}
	if math.IsNaN(body.BufferKm) || math.IsInf(body.BufferKm, 0) ||
		body.BufferKm < 0 || body.BufferKm > 20000 {
		writeError(w, http.StatusBadRequest, "bufferKm must be a finite number in [0, 20000]")
		return
	}

	var gf *GeoFilterConfig
	if len(body.Polygon) >= 3 {
		gf = &GeoFilterConfig{Polygon: body.Polygon, BufferKm: body.BufferKm}
	}

	s.saveMu.Lock()
	if s.configDir != "" {
		if err := SaveGeoFilter(s.configDir, gf); err != nil {
			s.saveMu.Unlock()
			log.Printf("[geofilter] save failed: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to save config")
			return
		}
	}
	s.setGeoFilter(gf)
	s.saveMu.Unlock()

	if gf != nil {
		writeJSON(w, map[string]interface{}{"polygon": gf.Polygon, "bufferKm": gf.BufferKm})
	} else {
		writeJSON(w, map[string]interface{}{"polygon": nil, "bufferKm": 0})
	}
}
