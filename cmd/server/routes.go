package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// Server holds shared state for route handlers.
type Server struct {
	db        *DB
	cfg       *Config
	hub       *Hub
	store     *PacketStore // in-memory packet store (nil = fallback to DB)
	startedAt time.Time
	perfStats *PerfStats
	version   string
	commit    string
	buildTime string

	// Cached runtime.MemStats to avoid stop-the-world pauses on every health check
	memStatsMu   sync.Mutex
	memStatsCache runtime.MemStats
	memStatsCachedAt time.Time

	// Cached /api/stats response — recomputed at most once every 10s
	statsMu      sync.Mutex
	statsCache   *StatsResponse
	statsCachedAt time.Time

	// Neighbor affinity graph (lazy-built, cached with TTL)
	neighborMu    sync.Mutex
	neighborGraph *NeighborGraph

	// Router reference for OpenAPI spec generation
	router *mux.Router
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

// RegisterRoutes sets up all HTTP routes on the given router.
func (s *Server) RegisterRoutes(r *mux.Router) {
	s.router = r
	// Performance instrumentation middleware
	r.Use(s.perfMiddleware)

	// Backfill status header middleware
	r.Use(s.backfillStatusMiddleware)

	// Config endpoints
	r.HandleFunc("/api/config/cache", s.handleConfigCache).Methods("GET")
	r.HandleFunc("/api/config/client", s.handleConfigClient).Methods("GET")
	r.HandleFunc("/api/config/regions", s.handleConfigRegions).Methods("GET")
	r.HandleFunc("/api/config/theme", s.handleConfigTheme).Methods("GET")
	r.HandleFunc("/api/config/map", s.handleConfigMap).Methods("GET")
	r.HandleFunc("/api/config/geo-filter", s.handleConfigGeoFilter).Methods("GET")

	// System endpoints
	r.HandleFunc("/api/health", s.handleHealth).Methods("GET")
	r.HandleFunc("/api/stats", s.handleStats).Methods("GET")
	r.HandleFunc("/api/perf", s.handlePerf).Methods("GET")
	r.Handle("/api/perf/reset", s.requireAPIKey(http.HandlerFunc(s.handlePerfReset))).Methods("POST")
	r.Handle("/api/admin/prune", s.requireAPIKey(http.HandlerFunc(s.handleAdminPrune))).Methods("POST")
	r.Handle("/api/debug/affinity", s.requireAPIKey(http.HandlerFunc(s.handleDebugAffinity))).Methods("GET")

	// Packet endpoints
	r.HandleFunc("/api/packets/observations", s.handleBatchObservations).Methods("POST")
	r.HandleFunc("/api/packets/timestamps", s.handlePacketTimestamps).Methods("GET")
	r.HandleFunc("/api/packets/{id}", s.handlePacketDetail).Methods("GET")
	r.HandleFunc("/api/packets", s.handlePackets).Methods("GET")
	r.Handle("/api/packets", s.requireAPIKey(http.HandlerFunc(s.handlePostPacket))).Methods("POST")

	// Decode endpoint
	r.HandleFunc("/api/decode", s.handleDecode).Methods("POST")

	// Node endpoints — fixed routes BEFORE parameterized
	r.HandleFunc("/api/nodes/search", s.handleNodeSearch).Methods("GET")
	r.HandleFunc("/api/nodes/bulk-health", s.handleBulkHealth).Methods("GET")
	r.HandleFunc("/api/nodes/network-status", s.handleNetworkStatus).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/health", s.handleNodeHealth).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/paths", s.handleNodePaths).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/analytics", s.handleNodeAnalytics).Methods("GET")
	r.HandleFunc("/api/nodes/clock-skew", s.handleFleetClockSkew).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/clock-skew", s.handleNodeClockSkew).Methods("GET")
	r.HandleFunc("/api/observers/clock-skew", s.handleObserverClockSkew).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/neighbors", s.handleNodeNeighbors).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}", s.handleNodeDetail).Methods("GET")
	r.HandleFunc("/api/nodes", s.handleNodes).Methods("GET")

	// Analytics endpoints
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

	// Other endpoints
	r.HandleFunc("/api/resolve-hops", s.handleResolveHops).Methods("GET")
	r.HandleFunc("/api/channels/{hash}/messages", s.handleChannelMessages).Methods("GET")
	r.HandleFunc("/api/channels", s.handleChannels).Methods("GET")
	r.HandleFunc("/api/observers/metrics/summary", s.handleMetricsSummary).Methods("GET")
	r.HandleFunc("/api/observers/{id}/metrics", s.handleObserverMetrics).Methods("GET")
	r.HandleFunc("/api/observers/{id}/analytics", s.handleObserverAnalytics).Methods("GET")
	r.HandleFunc("/api/observers/{id}", s.handleObserverDetail).Methods("GET")
	r.HandleFunc("/api/observers", s.handleObservers).Methods("GET")
	r.HandleFunc("/api/traces/{hash}", s.handleTraces).Methods("GET")
	r.HandleFunc("/api/iata-coords", s.handleIATACoords).Methods("GET")
	r.HandleFunc("/api/audio-lab/buckets", s.handleAudioLabBuckets).Methods("GET")

	// OpenAPI spec + Swagger UI
	r.HandleFunc("/api/spec", s.handleOpenAPISpec).Methods("GET")
	r.HandleFunc("/api/docs", s.handleSwaggerUI).Methods("GET")
}

func (s *Server) backfillStatusMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.store != nil && s.store.backfillComplete.Load() {
			w.Header().Set("X-CoreScope-Status", "ready")
		} else {
			w.Header().Set("X-CoreScope-Status", "backfilling")
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
	writeJSON(w, ClientConfigResponse{
		Roles:               s.cfg.Roles,
		HealthThresholds:    s.cfg.GetHealthThresholds().ToClientMs(),
		Tiles:               s.cfg.Tiles,
		SnrThresholds:       s.cfg.SnrThresholds,
		DistThresholds:      s.cfg.DistThresholds,
		MaxHopDist:          s.cfg.MaxHopDist,
		Limits:              s.cfg.Limits,
		PerfSlowMs:          s.cfg.PerfSlowMs,
		WsReconnectMs:       s.cfg.WsReconnectMs,
		CacheInvalidateMs:   s.cfg.CacheInvalidMs,
		ExternalUrls:        s.cfg.ExternalUrls,
		PropagationBufferMs: float64(s.cfg.PropagationBufferMs()),
		Timestamps:          s.cfg.GetTimestampConfig(),
		DebugAffinity:       s.cfg.DebugAffinity,
	})
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
		"accent":      "#4a9eff",
		"accentHover": "#6db3ff",
		"navBg":       "#0f0f23",
		"navBg2":      "#1a1a2e",
		"navText":     "#ffffff",
		"navTextMuted": "#cbd5e1",
		"background":  "#f4f5f7",
		"text":        "#1a1a2e",
		"textMuted":   "#5b6370",
		"border":      "#e2e5ea",
		"surface1":    "#ffffff",
		"surface2":    "#ffffff",
		"surface3":    "#ffffff",
		"sectionBg":   "#eef2ff",
		"cardBg":      "#ffffff",
		"contentBg":   "#f4f5f7",
		"detailBg":    "#ffffff",
		"inputBg":     "#ffffff",
		"rowStripe":   "#f9fafb",
		"rowHover":    "#eef2ff",
		"selectedBg":  "#dbeafe",
		"statusGreen": "#22c55e",
		"statusYellow": "#eab308",
		"statusRed":   "#ef4444",
	}, s.cfg.Theme, theme.Theme)

	nodeColors := mergeMap(map[string]interface{}{
		"repeater":  "#dc2626",
		"companion": "#2563eb",
		"room":      "#16a34a",
		"sensor":    "#d97706",
		"observer":  "#8b5cf6",
	}, s.cfg.NodeColors, theme.NodeColors)

	themeDark := mergeMap(map[string]interface{}{
		"accent":      "#4a9eff",
		"accentHover": "#6db3ff",
		"navBg":       "#0f0f23",
		"navBg2":      "#1a1a2e",
		"navText":     "#ffffff",
		"navTextMuted": "#cbd5e1",
		"background":  "#0f0f23",
		"text":        "#e2e8f0",
		"textMuted":   "#a8b8cc",
		"border":      "#334155",
		"surface1":    "#1a1a2e",
		"surface2":    "#232340",
		"cardBg":      "#1a1a2e",
		"contentBg":   "#0f0f23",
		"detailBg":    "#232340",
		"inputBg":     "#1e1e34",
		"rowStripe":   "#1e1e34",
		"rowHover":    "#2d2d50",
		"selectedBg":  "#1e3a5f",
		"statusGreen": "#22c55e",
		"statusYellow": "#eab308",
		"statusRed":   "#ef4444",
		"surface3":    "#2d2d50",
		"sectionBg":   "#1e1e34",
	}, s.cfg.ThemeDark, theme.ThemeDark)
	typeColors := mergeMap(map[string]interface{}{
		"ADVERT":   "#22c55e",
		"GRP_TXT":  "#3b82f6",
		"TXT_MSG":  "#f59e0b",
		"ACK":      "#6b7280",
		"REQUEST":  "#a855f7",
		"RESPONSE": "#06b6d4",
		"TRACE":    "#ec4899",
		"PATH":     "#14b8a6",
		"ANON_REQ": "#f43f5e",
		"UNKNOWN":  "#6b7280",
	}, s.cfg.TypeColors, theme.TypeColors)

	defaultHome := map[string]interface{}{
		"heroTitle":    "CoreScope",
		"heroSubtitle": "Real-time MeshCore LoRa mesh network analyzer",
		"steps": []interface{}{
			map[string]interface{}{"emoji": "🔵", "title": "Connect via Bluetooth", "description": "Flash **BLE companion** firmware from [MeshCore Flasher](https://flasher.meshcore.co.uk/).\n- Screenless devices: default PIN `123456`\n- Screen devices: random PIN shown on display\n- If pairing fails: forget device, reboot, re-pair"},
			map[string]interface{}{"emoji": "📻", "title": "Set the right frequency preset", "description": "**US Recommended:**\n`910.525 MHz · BW 62.5 kHz · SF 7 · CR 5`\nSelect **\"US Recommended\"** in the app or flasher."},
			map[string]interface{}{"emoji": "📡", "title": "Advertise yourself", "description": "Tap the signal icon → **Flood** to broadcast your node to the mesh. Companions only advert when you trigger it manually."},
			map[string]interface{}{"emoji": "🔁", "title": "Check \"Heard N repeats\"", "description": "- **\"Sent\"** = transmitted, no confirmation\n- **\"Heard 0 repeats\"** = no repeater picked it up\n- **\"Heard 1+ repeats\"** = you're on the mesh!"},
		},
		"footerLinks": []interface{}{
			map[string]interface{}{"label": "📦 Packets", "url": "#/packets"},
			map[string]interface{}{"label": "🗺️ Network Map", "url": "#/map"},
		},
	}
	home := mergeMap(defaultHome, s.cfg.Home, theme.Home)

	writeJSON(w, ThemeResponse{
		Branding:   branding,
		Theme:      themeColors,
		ThemeDark:  themeDark,
		NodeColors: nodeColors,
		TypeColors: typeColors,
		Home:       home,
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
	gf := s.cfg.GeoFilter
	if gf == nil || len(gf.Polygon) == 0 {
		writeJSON(w, map[string]interface{}{"polygon": nil, "bufferKm": 0})
		return
	}
	writeJSON(w, map[string]interface{}{"polygon": gf.Polygon, "bufferKm": gf.BufferKm})
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

	var stats *Stats
	var err error
	if s.store != nil {
		stats, err = s.store.GetStoreStats()
	} else {
		stats, err = s.db.GetStats()
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	counts := s.db.GetRoleCounts()

	// Compute backfill progress
	backfilling := s.store != nil && !s.store.backfillComplete.Load()
	var backfillProgress float64
	if backfilling && s.store != nil && s.store.backfillTotal.Load() > 0 {
		backfillProgress = float64(s.store.backfillProcessed.Load()) / float64(s.store.backfillTotal.Load())
		if backfillProgress > 1 {
			backfillProgress = 1
		}
	} else if !backfilling {
		backfillProgress = 1
	}

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
		Backfilling:      backfilling,
		BackfillProgress: backfillProgress,
	}

	s.statsMu.Lock()
	s.statsCache = resp
	s.statsCachedAt = time.Now()
	s.statsMu.Unlock()

	writeJSON(w, resp)
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

	// SQLite stats
	var sqliteStats *SqliteStats
	if s.db != nil {
		ss := s.db.GetDBSizeStatsTyped()
		sqliteStats = &ss
	}

	writeJSON(w, PerfResponse{
		Uptime:        uptimeSec,
		TotalRequests: totalRequests,
		AvgMs:         safeAvg(totalMs, float64(totalRequests)),
		Endpoints:     summary,
		SlowQueries:   slowQueries,
		Cache:         perfCS,
		PacketStore:   pktStoreStats,
		Sqlite:        sqliteStats,
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
		var result *PacketResult
		var err error
		if s.store != nil {
			result = s.store.QueryMultiNodePackets(cleaned,
				queryInt(r, "limit", 50), queryInt(r, "offset", 0),
				order, r.URL.Query().Get("since"), r.URL.Query().Get("until"))
		} else {
			result, err = s.db.QueryMultiNodePackets(cleaned,
				queryInt(r, "limit", 50), queryInt(r, "offset", 0),
				order, r.URL.Query().Get("since"), r.URL.Query().Get("until"))
		}
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, PacketListResponse{
			Packets: mapSliceToTransmissions(result.Packets),
			Total:   result.Total,
			Limit:   queryInt(r, "limit", 50),
			Offset:  queryInt(r, "offset", 0),
		})
		return
	}

	q := PacketQuery{
		Limit:    queryInt(r, "limit", 50),
		Offset:   queryInt(r, "offset", 0),
		Observer: r.URL.Query().Get("observer"),
		Hash:     r.URL.Query().Get("hash"),
		Since:    r.URL.Query().Get("since"),
		Until:    r.URL.Query().Get("until"),
		Region:   r.URL.Query().Get("region"),
		Node:     r.URL.Query().Get("node"),
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

	if s.store != nil {
		if hashPattern.MatchString(strings.ToLower(param)) {
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
	if packet == nil {
		writeError(w, 404, "Not found")
		return
	}

	hash, _ := packet["hash"].(string)
	var observations []map[string]interface{}
	if s.store != nil {
		observations = s.store.GetObservationsForHash(hash)
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

	rawHex, _ := packet["raw_hex"].(string)
	writeJSON(w, PacketDetailResponse{
		Packet:           packet,
		Path:             pathHops,
		Breakdown:        BuildBreakdown(rawHex),
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

func (s *Server) handlePostPacket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hex      string   `json:"hex"`
		Observer *string  `json:"observer"`
		Snr      *float64 `json:"snr"`
		Rssi     *float64 `json:"rssi"`
		Region   *string  `json:"region"`
		Hash     *string  `json:"hash"`
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
	decoded, err := DecodePacket(hexStr, false)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}

	contentHash := ComputeContentHash(hexStr)
	pathJSON := "[]"
	if len(decoded.Path.Hops) > 0 {
		if pj, e := json.Marshal(decoded.Path.Hops); e == nil {
			pathJSON = string(pj)
		}
	}
	decodedJSON := PayloadJSON(&decoded.Payload)
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	var obsID, obsName interface{}
	if body.Observer != nil {
		obsID = *body.Observer
	}
	var snr, rssi interface{}
	if body.Snr != nil {
		snr = *body.Snr
	}
	if body.Rssi != nil {
		rssi = *body.Rssi
	}

	res, dbErr := s.db.conn.Exec(`INSERT INTO transmissions (hash, raw_hex, route_type, payload_type, payload_version, path_json, decoded_json, first_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		contentHash, strings.ToUpper(hexStr), decoded.Header.RouteType, decoded.Header.PayloadType,
		decoded.Header.PayloadVersion, pathJSON, decodedJSON, now)

	var insertedID int64
	if dbErr == nil {
		insertedID, _ = res.LastInsertId()
		s.db.conn.Exec(`INSERT INTO observations (transmission_id, observer_id, observer_name, snr, rssi, timestamp)
			VALUES (?, ?, ?, ?, ?, ?)`,
			insertedID, obsID, obsName, snr, rssi, now)
	}

	writeJSON(w, PacketIngestResponse{
		ID: insertedID,
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
		queryInt(r, "limit", 50),
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
		for _, node := range nodes {
			if pk, ok := node["public_key"].(string); ok {
				EnrichNodeWithHashSize(node, hashInfo[pk])
			}
		}
	}
	if s.cfg.GeoFilter != nil {
		filtered := nodes[:0]
		for _, node := range nodes {
			if NodePassesGeoFilter(node["lat"], node["lon"], s.cfg.GeoFilter) {
				filtered = append(filtered, node)
			}
		}
		total = len(filtered)
		nodes = filtered
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
	writeJSON(w, NodeSearchResponse{Nodes: nodes})
}

func (s *Server) handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		writeError(w, 404, "Not found")
		return
	}

	if s.store != nil {
		hashInfo := s.store.GetNodeHashSizeInfo()
		EnrichNodeWithHashSize(node, hashInfo[pubkey])
	}

	name := ""
	if n, ok := node["name"]; ok && n != nil {
		name = fmt.Sprintf("%v", n)
	}
	recentAdverts, _ := s.db.GetRecentTransmissionsForNode(pubkey, name, 20)

	writeJSON(w, NodeDetailResponse{
		Node:          node,
		RecentAdverts: recentAdverts,
	})
}

func (s *Server) handleNodeHealth(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
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
	limit := queryInt(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}

	if s.store != nil {
		region := r.URL.Query().Get("region")
		writeJSON(w, s.store.GetBulkHealth(limit, region))
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
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		writeError(w, 404, "Not found")
		return
	}
	if s.store == nil {
		writeError(w, 503, "Packet store unavailable")
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
	seen := make(map[int]bool)
	var candidates []*StoreTx
	addCandidates := func(key string) {
		for _, tx := range s.store.byPathHop[key] {
			if !seen[tx.ID] {
				seen[tx.ID] = true
				candidates = append(candidates, tx)
			}
		}
	}
	addCandidates(lowerPK) // full pubkey match (from resolved_path)
	addCandidates(prefix1) // 2-char raw hop match
	addCandidates(prefix2) // 4-char raw hop match
	// Also check any raw hops that start with prefix2 (longer prefixes).
	// Raw hops are typically 2 chars, so iterate only keys with HasPrefix
	// on the small set of index keys rather than all packets.
	for key := range s.store.byPathHop {
		if len(key) > 4 && len(key) < len(lowerPK) && strings.HasPrefix(key, prefix2) {
			addCandidates(key)
		}
	}

	// Post-filter: verify target node actually appears in each candidate's resolved_path.
	// The byPathHop index uses short prefixes which can collide (e.g. "c0" matches multiple nodes).
	// We lean on resolved_path (from neighbor affinity graph) to disambiguate.
	filtered := candidates[:0] // reuse backing array
	for _, tx := range candidates {
		if nodeInResolvedPath(tx, lowerPK) {
			filtered = append(filtered, tx)
		}
	}
	candidates = filtered

	type pathAgg struct {
		Hops       []PathHopResp
		Count      int
		LastSeen   string
		SampleHash string
	}
	pathGroups := map[string]*pathAgg{}
	totalTransmissions := 0
	hopCache := make(map[string]*nodeInfo)
	resolveHop := func(hop string) *nodeInfo {
		if cached, ok := hopCache[hop]; ok {
			return cached
		}
		r, _, _ := pm.resolveWithContext(hop, nil, s.store.graph)
		hopCache[hop] = r
		return r
	}
	for _, tx := range candidates {
		totalTransmissions++
		hops := txGetParsedPath(tx)
		resolvedHops := make([]PathHopResp, len(hops))
		sigParts := make([]string, len(hops))
		for i, hop := range hops {
			resolved := resolveHop(hop)
			entry := PathHopResp{Prefix: hop, Name: hop}
			if resolved != nil {
				entry.Name = resolved.Name
				entry.Pubkey = resolved.PublicKey
				if resolved.HasGPS {
					entry.Lat = resolved.Lat
					entry.Lon = resolved.Lon
				}
				sigParts[i] = resolved.PublicKey
			} else {
				sigParts[i] = hop
			}
			resolvedHops[i] = entry
		}

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
		if paths[i].Count == paths[j].Count {
			li := ""
			lj := ""
			if paths[i].LastSeen != nil {
				li = fmt.Sprintf("%v", paths[i].LastSeen)
			}
			if paths[j].LastSeen != nil {
				lj = fmt.Sprintf("%v", paths[j].LastSeen)
			}
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
	writeJSON(w, s.store.GetFleetClockSkew())
}

// --- Analytics Handlers ---

func (s *Server) handleAnalyticsRF(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	if s.store != nil {
		writeJSON(w, s.store.GetAnalyticsRF(region))
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

func (s *Server) handleAnalyticsTopology(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	if s.store != nil {
		writeJSON(w, s.store.GetAnalyticsTopology(region))
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
		writeJSON(w, s.store.GetAnalyticsChannels(region))
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
	if s.store != nil {
		writeJSON(w, s.store.GetAnalyticsDistance(region))
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
		writeJSON(w, s.store.GetAnalyticsHashSizes(region))
		return
	}
	writeJSON(w, map[string]interface{}{
		"total":                    0,
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
		writeJSON(w, s.store.GetAnalyticsHashCollisions(region))
		return
	}
	writeJSON(w, map[string]interface{}{
		"inconsistent_nodes": []interface{}{},
		"by_size":            map[string]interface{}{},
	})
}

func (s *Server) handleAnalyticsSubpaths(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		region := r.URL.Query().Get("region")
		minLen := queryInt(r, "minLen", 2)
		if minLen < 2 {
			minLen = 2
		}
		maxLen := queryInt(r, "maxLen", 8)
		limit := queryInt(r, "limit", 100)
		writeJSON(w, s.store.GetAnalyticsSubpaths(region, minLen, maxLen, limit))
		return
	}
	writeJSON(w, SubpathsResponse{
		Subpaths:   []SubpathResp{},
		TotalPaths: 0,
	})
}

// handleAnalyticsSubpathsBulk returns multiple length-range buckets in a single
// response, avoiding repeated scans of the same packet data. Query format:
//   ?groups=2-2:50,3-3:30,4-4:20,5-8:15   (minLen-maxLen:limit per group)
func (s *Server) handleAnalyticsSubpathsBulk(w http.ResponseWriter, r *http.Request) {
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

	results := s.store.GetAnalyticsSubpathsBulk(region, groups)
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
	if s.store != nil {
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
			if best != nil {
				hr.Name = best.Name
				hr.Pubkey = best.PublicKey
			}

			// Only promote to bestCandidate when affinity is confident.
			if confidence == "neighbor_affinity" && best != nil {
				pk := best.PublicKey
				hr.BestCandidate = &pk
				hr.Confidence = "neighbor_affinity"
			} else if (confidence == "geo_proximity" || confidence == "gps_preference" || confidence == "first_match") && best != nil {
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
	limit := queryInt(r, "limit", 100)
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
	observers, err := s.db.GetObservers()
	if err != nil {
		writeError(w, 500, err.Error())
		return
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
	for _, o := range observers {
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

		result = append(result, ObserverResp{
			ID: o.ID, Name: o.Name, IATA: o.IATA,
			LastSeen: o.LastSeen, FirstSeen: o.FirstSeen,
			PacketCount: o.PacketCount,
			Model: o.Model, Firmware: o.Firmware,
			ClientVersion: o.ClientVersion, Radio: o.Radio,
			BatteryMv: o.BatteryMv, UptimeSecs: o.UptimeSecs,
			NoiseFloor: o.NoiseFloor,
			PacketsLastHour: plh,
			Lat: lat, Lon: lon, NodeRole: nodeRole,
		})
	}
	writeJSON(w, ObserverListResponse{
		Observers:  result,
		ServerTime: time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleObserverDetail(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
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

	writeJSON(w, ObserverResp{
		ID: obs.ID, Name: obs.Name, IATA: obs.IATA,
		LastSeen: obs.LastSeen, FirstSeen: obs.FirstSeen,
		PacketCount: obs.PacketCount,
		Model: obs.Model, Firmware: obs.Firmware,
		ClientVersion: obs.ClientVersion, Radio: obs.Radio,
		BatteryMv: obs.BatteryMv, UptimeSecs: obs.UptimeSecs,
		NoiseFloor: obs.NoiseFloor,
		PacketsLastHour: plh,
	})
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

	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	s.store.mu.RLock()
	obsList := s.store.byObserver[id]
	filtered := make([]*StoreObs, 0, len(obsList))
	for _, obs := range obsList {
		if obs.Timestamp == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, obs.Timestamp)
		if err != nil {
			t, err = time.Parse(time.RFC3339, obs.Timestamp)
		}
		if err != nil {
			t, err = time.Parse("2006-01-02 15:04:05", obs.Timestamp)
		}
		if err != nil {
			continue
		}
		if t.Equal(since) || t.After(since) {
			filtered = append(filtered, obs)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Timestamp > filtered[j].Timestamp })

	bucketDur := 24 * time.Hour
	if days <= 1 {
		bucketDur = time.Hour
	} else if days <= 7 {
		bucketDur = 4 * time.Hour
	}
	formatLabel := func(t time.Time) string {
		if days <= 1 {
			return t.UTC().Format("15:04")
		}
		if days <= 7 {
			return t.UTC().Format("Mon 15:04")
		}
		return t.UTC().Format("Jan 02")
	}

	packetTypes := map[string]int{}
	timelineCounts := map[int64]int{}
	nodeBucketSets := map[int64]map[string]struct{}{}
	snrBuckets := map[int]*SnrDistributionEntry{}
	recentPackets := make([]map[string]interface{}, 0, 20)

	for i, obs := range filtered {
		ts, err := time.Parse(time.RFC3339Nano, obs.Timestamp)
		if err != nil {
			ts, err = time.Parse(time.RFC3339, obs.Timestamp)
		}
		if err != nil {
			ts, err = time.Parse("2006-01-02 15:04:05", obs.Timestamp)
		}
		if err != nil {
			continue
		}
		bucketStart := ts.UTC().Truncate(bucketDur).Unix()
		timelineCounts[bucketStart]++
		if nodeBucketSets[bucketStart] == nil {
			nodeBucketSets[bucketStart] = map[string]struct{}{}
		}

		enriched := s.store.enrichObs(obs)
		if pt, ok := enriched["payload_type"].(int); ok {
			packetTypes[strconv.Itoa(pt)]++
		}
		if decodedRaw, ok := enriched["decoded_json"].(string); ok && decodedRaw != "" {
			var decoded map[string]interface{}
			if json.Unmarshal([]byte(decodedRaw), &decoded) == nil {
				for _, k := range []string{"pubKey", "srcHash", "destHash"} {
					if v, ok := decoded[k].(string); ok && v != "" {
						nodeBucketSets[bucketStart][v] = struct{}{}
					}
				}
			}
		}
		for _, hop := range parsePathJSON(obs.PathJSON) {
			if hop != "" {
				nodeBucketSets[bucketStart][hop] = struct{}{}
			}
		}
		if obs.SNR != nil {
			bucket := int(*obs.SNR) / 2 * 2
			if *obs.SNR < 0 && int(*obs.SNR) != bucket {
				bucket -= 2
			}
			if snrBuckets[bucket] == nil {
				snrBuckets[bucket] = &SnrDistributionEntry{Range: fmt.Sprintf("%d to %d", bucket, bucket+2)}
			}
			snrBuckets[bucket].Count++
		}
		if i < 20 {
			recentPackets = append(recentPackets, enriched)
		}
	}
	s.store.mu.RUnlock()

	buildTimeline := func(counts map[int64]int) []TimeBucket {
		keys := make([]int64, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		out := make([]TimeBucket, 0, len(keys))
		for _, k := range keys {
			lbl := formatLabel(time.Unix(k, 0))
			out = append(out, TimeBucket{Label: &lbl, Count: counts[k]})
		}
		return out
	}

	nodeCounts := make(map[int64]int, len(nodeBucketSets))
	for k, nodes := range nodeBucketSets {
		nodeCounts[k] = len(nodes)
	}
	snrKeys := make([]int, 0, len(snrBuckets))
	for k := range snrBuckets {
		snrKeys = append(snrKeys, k)
	}
	sort.Ints(snrKeys)
	snrDistribution := make([]SnrDistributionEntry, 0, len(snrKeys))
	for _, k := range snrKeys {
		snrDistribution = append(snrDistribution, *snrBuckets[k])
	}

	writeJSON(w, ObserverAnalyticsResponse{
		Timeline:        buildTimeline(timelineCounts),
		PacketTypes:     packetTypes,
		NodesTimeline:   buildTimeline(nodeCounts),
		SnrDistribution: snrDistribution,
		RecentPackets:   recentPackets,
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
				var d map[string]interface{}
				if err := json.Unmarshal([]byte(tx.DecodedJSON), &d); err == nil {
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
		tx.SNR = m["snr"]
		tx.RSSI = m["rssi"]
		tx.PathJSON = m["path_json"]
		tx.Direction = m["direction"]
		tx.Score = m["score"]
		if rp, ok := m["resolved_path"].([]*string); ok {
			tx.ResolvedPath = rp
		}
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
		obs.SNR = m["snr"]
		obs.RSSI = m["rssi"]
		obs.PathJSON = m["path_json"]
		obs.Timestamp = m["timestamp"]
		if rp, ok := m["resolved_path"].([]*string); ok {
			obs.ResolvedPath = rp
		}
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

func (s *Server) handleAdminPrune(w http.ResponseWriter, r *http.Request) {
	days := 0
	if d := r.URL.Query().Get("days"); d != "" {
		fmt.Sscanf(d, "%d", &days)
	}
	if days <= 0 && s.cfg.Retention != nil {
		days = s.cfg.Retention.PacketDays
	}
	if days <= 0 {
		writeError(w, 400, "days parameter required (or set retention.packetDays in config)")
		return
	}
	n, err := s.db.PruneOldPackets(days)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	log.Printf("[prune] deleted %d transmissions older than %d days", n, days)
	writeJSON(w, map[string]interface{}{"deleted": n, "days": days})
}

// constantTimeEqual compares two strings in constant time to prevent timing attacks.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
