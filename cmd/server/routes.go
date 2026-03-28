package main

import (
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
}

// PerfStats tracks request performance.
type PerfStats struct {
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
	// Performance instrumentation middleware
	r.Use(s.perfMiddleware)

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
	r.HandleFunc("/api/perf/reset", s.handlePerfReset).Methods("POST")

	// Packet endpoints
	r.HandleFunc("/api/packets/timestamps", s.handlePacketTimestamps).Methods("GET")
	r.HandleFunc("/api/packets/{id}", s.handlePacketDetail).Methods("GET")
	r.HandleFunc("/api/packets", s.handlePackets).Methods("GET")
	r.HandleFunc("/api/packets", s.handlePostPacket).Methods("POST")

	// Decode endpoint
	r.HandleFunc("/api/decode", s.handleDecode).Methods("POST")

	// Node endpoints — fixed routes BEFORE parameterized
	r.HandleFunc("/api/nodes/search", s.handleNodeSearch).Methods("GET")
	r.HandleFunc("/api/nodes/bulk-health", s.handleBulkHealth).Methods("GET")
	r.HandleFunc("/api/nodes/network-status", s.handleNetworkStatus).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/health", s.handleNodeHealth).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/paths", s.handleNodePaths).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}/analytics", s.handleNodeAnalytics).Methods("GET")
	r.HandleFunc("/api/nodes/{pubkey}", s.handleNodeDetail).Methods("GET")
	r.HandleFunc("/api/nodes", s.handleNodes).Methods("GET")

	// Analytics endpoints
	r.HandleFunc("/api/analytics/rf", s.handleAnalyticsRF).Methods("GET")
	r.HandleFunc("/api/analytics/topology", s.handleAnalyticsTopology).Methods("GET")
	r.HandleFunc("/api/analytics/channels", s.handleAnalyticsChannels).Methods("GET")
	r.HandleFunc("/api/analytics/distance", s.handleAnalyticsDistance).Methods("GET")
	r.HandleFunc("/api/analytics/hash-sizes", s.handleAnalyticsHashSizes).Methods("GET")
	r.HandleFunc("/api/analytics/subpaths", s.handleAnalyticsSubpaths).Methods("GET")
	r.HandleFunc("/api/analytics/subpath-detail", s.handleAnalyticsSubpathDetail).Methods("GET")

	// Other endpoints
	r.HandleFunc("/api/resolve-hops", s.handleResolveHops).Methods("GET")
	r.HandleFunc("/api/channels/{hash}/messages", s.handleChannelMessages).Methods("GET")
	r.HandleFunc("/api/channels", s.handleChannels).Methods("GET")
	r.HandleFunc("/api/observers/{id}/analytics", s.handleObserverAnalytics).Methods("GET")
	r.HandleFunc("/api/observers/{id}", s.handleObserverDetail).Methods("GET")
	r.HandleFunc("/api/observers", s.handleObservers).Methods("GET")
	r.HandleFunc("/api/traces/{hash}", s.handleTraces).Methods("GET")
	r.HandleFunc("/api/iata-coords", s.handleIATACoords).Methods("GET")
	r.HandleFunc("/api/audio-lab/buckets", s.handleAudioLabBuckets).Methods("GET")
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

		s.perfStats.Requests++
		s.perfStats.TotalMs += ms

		// Normalize key: prefer mux route template (like Node.js req.route.path)
		key := r.URL.Path
		if route := mux.CurrentRoute(r); route != nil {
			if tmpl, err := route.GetPathTemplate(); err == nil {
				key = muxBraceParam.ReplaceAllString(tmpl, ":$1")
			}
		}
		if key == r.URL.Path {
			key = perfHexFallback.ReplaceAllString(key, ":id")
		}
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
		HealthThresholds:    s.cfg.HealthThresholds,
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
		"siteName": "MeshCore Analyzer",
		"tagline":  "Real-time MeshCore LoRa mesh network analyzer",
	}, s.cfg.Branding, theme.Branding)

	themeColors := mergeMap(map[string]interface{}{
		"accent":      "#4a9eff",
		"accentHover": "#6db3ff",
		"navBg":       "#0f0f23",
		"navBg2":      "#1a1a2e",
	}, s.cfg.Theme, theme.Theme)

	nodeColors := mergeMap(map[string]interface{}{
		"repeater":  "#dc2626",
		"companion": "#2563eb",
		"room":      "#16a34a",
		"sensor":    "#d97706",
		"observer":  "#8b5cf6",
	}, s.cfg.NodeColors, theme.NodeColors)

	themeDark := mergeMap(map[string]interface{}{}, s.cfg.ThemeDark, theme.ThemeDark)
	typeColors := mergeMap(map[string]interface{}{}, s.cfg.TypeColors, theme.TypeColors)

	var home interface{}
	if theme.Home != nil {
		home = theme.Home
	} else if s.cfg.Home != nil {
		home = s.cfg.Home
	}

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
	if s.store != nil {
		ps := s.store.GetPerfStoreStatsTyped()
		pktCount = ps.TotalLoaded
		pktEstMB = ps.EstimatedMB
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

	// Build slow queries list
	recentSlow := make([]SlowQuery, 0)
	sliceEnd := s.perfStats.SlowQueries
	if len(sliceEnd) > 5 {
		sliceEnd = sliceEnd[len(sliceEnd)-5:]
	}
	for _, sq := range sliceEnd {
		recentSlow = append(recentSlow, sq)
	}

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
		},
		Perf: HealthPerfStats{
			TotalRequests: int(s.perfStats.Requests),
			AvgMs:         safeAvg(s.perfStats.TotalMs, float64(s.perfStats.Requests)),
			SlowQueries:   len(s.perfStats.SlowQueries),
			RecentSlow:    recentSlow,
		},
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, StatsResponse{
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
	})
}

func (s *Server) handlePerf(w http.ResponseWriter, r *http.Request) {
	// Endpoint performance summary
	type epEntry struct {
		path string
		data *EndpointStatsResp
	}
	var entries []epEntry
	for path, ep := range s.perfStats.Endpoints {
		sorted := sortedCopy(ep.Recent)
		d := &EndpointStatsResp{
			Count: ep.Count,
			AvgMs: safeAvg(ep.TotalMs, float64(ep.Count)),
			P50Ms: round(percentile(sorted, 0.5), 1),
			P95Ms: round(percentile(sorted, 0.95), 1),
			MaxMs: round(ep.MaxMs, 1),
		}
		entries = append(entries, epEntry{path, d})
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

	uptimeSec := int(time.Since(s.perfStats.StartedAt).Seconds())

	// Convert slow queries
	slowQueries := make([]SlowQuery, 0)
	sliceEnd := s.perfStats.SlowQueries
	if len(sliceEnd) > 20 {
		sliceEnd = sliceEnd[len(sliceEnd)-20:]
	}
	for _, sq := range sliceEnd {
		slowQueries = append(slowQueries, sq)
	}

	writeJSON(w, PerfResponse{
		Uptime:        uptimeSec,
		TotalRequests: s.perfStats.Requests,
		AvgMs:         safeAvg(s.perfStats.TotalMs, float64(s.perfStats.Requests)),
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
	s.perfStats = NewPerfStats()
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
		Order:    "DESC",
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

	// Strip observations from default response
	if r.URL.Query().Get("expand") != "observations" {
		for _, p := range result.Packets {
			delete(p, "observations")
		}
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
	ts, err := s.db.GetTimestamps(since)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, ts)
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// muxBraceParam matches {param} in gorilla/mux route templates for normalization.
var muxBraceParam = regexp.MustCompile(`\{([^}]+)\}`)

// perfHexFallback matches hex IDs for perf path normalization fallback.
var perfHexFallback = regexp.MustCompile(`[0-9a-f]{8,}`)

func (s *Server) handlePacketDetail(w http.ResponseWriter, r *http.Request) {
	param := mux.Vars(r)["id"]
	var packet map[string]interface{}
	var err error

	if s.store != nil {
		// Use in-memory store for lookups
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
	} else {
		// Fallback to DB
		if hashPattern.MatchString(strings.ToLower(param)) {
			packet, err = s.db.GetPacketByHash(param)
		}
		if packet == nil {
			id, parseErr := strconv.Atoi(param)
			if parseErr == nil {
				packet, err = s.db.GetTransmissionByID(id)
				if packet == nil {
					packet, err = s.db.GetPacketByID(id)
				}
			}
		}
	}
	if err != nil || packet == nil {
		writeError(w, 404, "Not found")
		return
	}

	// Build observation list
	hash, _ := packet["hash"].(string)
	var observations []map[string]interface{}
	if s.store != nil {
		observations = s.store.GetObservationsForHash(hash)
	} else {
		observations, _ = s.db.GetObservationsForHash(hash)
	}
	observationCount := len(observations)
	if observationCount == 0 {
		observationCount = 1
	}

	// Parse path from path_json
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
		Breakdown:        struct{}{},
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
	decoded, err := DecodePacket(hexStr)
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
	decoded, err := DecodePacket(hexStr)
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
	var result map[string]interface{}
	var err error
	if s.store != nil {
		result, err = s.store.GetNodeHealth(pubkey)
	} else {
		result, err = s.db.GetNodeHealth(pubkey)
	}
	if err != nil || result == nil {
		writeError(w, 404, "Not found")
		return
	}
	writeJSON(w, result)
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

	rows, err := s.db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen FROM nodes ORDER BY last_seen DESC LIMIT ?", limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer rows.Close()

	type nodeDbInfo struct {
		pk, name, role, lastSeen string
		lat, lon                 interface{}
	}
	var nodes []nodeDbInfo
	for rows.Next() {
		var pk string
		var name, role, lastSeen sql.NullString
		var lat, lon sql.NullFloat64
		rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen)
		nodes = append(nodes, nodeDbInfo{
			pk: pk, name: nullStrVal(name), role: nullStrVal(role),
			lastSeen: nullStrVal(lastSeen),
			lat:      nullFloat(lat), lon: nullFloat(lon),
		})
	}

	// Batch query: per-node transmission stats
	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)
	results := make([]BulkHealthEntry, 0, len(nodes))
	for _, n := range nodes {
		pk := "%" + n.pk + "%"
		np := "%" + n.name + "%"

		var txCount, obsCount, packetsToday int
		var avgSnr sql.NullFloat64
		var lastHeard sql.NullString

		whereClause := "t.decoded_json LIKE ?"
		queryArgs := []interface{}{pk}
		if n.name != "" {
			whereClause = "(t.decoded_json LIKE ? OR t.decoded_json LIKE ?)"
			queryArgs = []interface{}{pk, np}
		}

		s.db.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM transmissions t WHERE %s", whereClause), queryArgs...).Scan(&txCount)

		if txCount > 0 {
			// Observation count
			s.db.conn.QueryRow(fmt.Sprintf(`SELECT COALESCE(SUM(
				(SELECT COUNT(*) FROM observations oi WHERE oi.transmission_id = t.id)
			), 0) FROM transmissions t WHERE %s`, whereClause), queryArgs...).Scan(&obsCount)

			// Packets today
			todayArgs := append(queryArgs, todayStart)
			s.db.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM transmissions t WHERE %s AND t.first_seen > ?", whereClause), todayArgs...).Scan(&packetsToday)

			// Avg SNR from best observation per transmission
			s.db.conn.QueryRow(fmt.Sprintf(`SELECT AVG(o.snr) FROM transmissions t
				LEFT JOIN observations o ON o.id = (
					SELECT id FROM observations WHERE transmission_id = t.id AND snr IS NOT NULL LIMIT 1
				) WHERE %s AND o.snr IS NOT NULL`, whereClause), queryArgs...).Scan(&avgSnr)

			// Last heard
			s.db.conn.QueryRow(fmt.Sprintf("SELECT MAX(t.first_seen) FROM transmissions t WHERE %s", whereClause), queryArgs...).Scan(&lastHeard)
		}

		lh := n.lastSeen
		if lastHeard.Valid && lastHeard.String > lh {
			lh = lastHeard.String
		}

		// Per-observer breakdown
		pvWhere := "pv.decoded_json LIKE ?"
		if n.name != "" {
			pvWhere = "(pv.decoded_json LIKE ? OR pv.decoded_json LIKE ?)"
		}
		obsSQL := fmt.Sprintf(`SELECT pv.observer_id, pv.observer_name, AVG(pv.snr) as avgSnr, AVG(pv.rssi) as avgRssi, COUNT(*) as packetCount
			FROM packets_v pv WHERE %s AND pv.observer_id IS NOT NULL
			GROUP BY pv.observer_id ORDER BY packetCount DESC`, pvWhere)
		obsRows, _ := s.db.conn.Query(obsSQL, queryArgs...)
		observers := make([]NodeObserverStatsResp, 0)
		if obsRows != nil {
			for obsRows.Next() {
				var oID, oName sql.NullString
				var oSnr, oRssi sql.NullFloat64
				var oPktCount int
				obsRows.Scan(&oID, &oName, &oSnr, &oRssi, &oPktCount)
				observers = append(observers, NodeObserverStatsResp{
					ObserverID: nullStr(oID), ObserverName: nullStr(oName),
					AvgSnr: nullFloat(oSnr), AvgRssi: nullFloat(oRssi),
					PacketCount: oPktCount,
				})
			}
			obsRows.Close()
		}

		results = append(results, BulkHealthEntry{
			PublicKey: n.pk,
			Name:      nilIfEmpty(n.name),
			Role:      nilIfEmpty(n.role),
			Lat:       n.lat,
			Lon:       n.lon,
			Stats: NodeStatsResp{
				TotalTransmissions: txCount,
				TotalObservations:  obsCount,
				TotalPackets:       txCount,
				PacketsToday:       packetsToday,
				AvgSnr:             nullFloat(avgSnr),
				LastHeard:          nilIfEmpty(lh),
			},
			Observers: observers,
		})
	}
	writeJSON(w, results)
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

	pk := "%" + pubkey[:8] + "%"
	name := ""
	if n, ok := node["name"]; ok && n != nil {
		name = fmt.Sprintf("%v", n)
	}

	whereClause := "path_json LIKE ?"
	args := []interface{}{pk}
	if name != "" {
		whereClause = "(path_json LIKE ? OR path_json LIKE ?)"
		args = append(args, "%"+name+"%")
	}

	pathSQL := fmt.Sprintf("SELECT path_json, hash, MAX(timestamp) as lastSeen, COUNT(*) as cnt FROM packets_v WHERE path_json IS NOT NULL AND path_json != '[]' AND %s GROUP BY path_json ORDER BY cnt DESC LIMIT 50", whereClause)
	rows, _ := s.db.conn.Query(pathSQL, args...)

	paths := make([]PathEntryResp, 0)
	var totalPaths, totalTx int
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var pj, hash string
			var lastSeen sql.NullString
			var cnt int
			rows.Scan(&pj, &hash, &lastSeen, &cnt)
			var hops []string
			if json.Unmarshal([]byte(pj), &hops) != nil {
				continue
			}
			hopEntries := make([]PathHopResp, 0, len(hops))
			for _, h := range hops {
				hopEntries = append(hopEntries, PathHopResp{
					Prefix: h, Name: h, Pubkey: nil, Lat: nil, Lon: nil,
				})
			}
			paths = append(paths, PathEntryResp{
				Hops: hopEntries, Count: cnt,
				LastSeen: nullStr(lastSeen), SampleHash: hash,
			})
			totalPaths++
			totalTx += cnt
		}
	}

	writeJSON(w, NodePathsResponse{
		Node: map[string]interface{}{
			"public_key": node["public_key"],
			"name":       node["name"],
			"lat":        node["lat"],
			"lon":        node["lon"],
		},
		Paths:              paths,
		TotalPaths:         totalPaths,
		TotalTransmissions: totalTx,
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

	// Use in-memory store when available (fast path)
	if s.store != nil {
		result, err := s.store.GetNodeAnalytics(pubkey, days)
		if err != nil || result == nil {
			writeError(w, 404, "Not found")
			return
		}
		writeJSON(w, result)
		return
	}

	// Fallback: SQL path (no in-memory store)
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		writeError(w, 404, "Not found")
		return
	}

	name := ""
	if n, ok := node["name"]; ok && n != nil {
		name = fmt.Sprintf("%v", n)
	}

	fromISO := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	toISO := time.Now().Format(time.RFC3339)

	pk := "%" + pubkey + "%"
	np := "%" + name + "%"
	whereClause := "decoded_json LIKE ? OR decoded_json LIKE ?"
	if name == "" {
		whereClause = "decoded_json LIKE ?"
		np = pk
	}
	timeWhere := fmt.Sprintf("(%s) AND timestamp > ?", whereClause)

	// Activity timeline
	actSQL := fmt.Sprintf(`SELECT substr(timestamp, 1, 13) || ':00:00Z' as bucket, COUNT(*) as count
		FROM packets_v WHERE %s GROUP BY bucket ORDER BY bucket`, timeWhere)
	aRows, _ := s.db.conn.Query(actSQL, pk, np, fromISO)
	activityTimeline := make([]TimeBucket, 0)
	if aRows != nil {
		defer aRows.Close()
		for aRows.Next() {
			var bucket string
			var count int
			aRows.Scan(&bucket, &count)
			b := bucket
			activityTimeline = append(activityTimeline, TimeBucket{Bucket: &b, Count: count})
		}
	}

	// SNR trend
	snrSQL := fmt.Sprintf(`SELECT timestamp, snr, rssi, observer_id, observer_name
		FROM packets_v WHERE %s AND snr IS NOT NULL ORDER BY timestamp`, timeWhere)
	sRows, _ := s.db.conn.Query(snrSQL, pk, np, fromISO)
	snrTrend := make([]SnrTrendEntry, 0)
	if sRows != nil {
		defer sRows.Close()
		for sRows.Next() {
			var ts string
			var snr, rssi sql.NullFloat64
			var obsID, obsName sql.NullString
			sRows.Scan(&ts, &snr, &rssi, &obsID, &obsName)
			snrTrend = append(snrTrend, SnrTrendEntry{
				Timestamp: ts, SNR: nullFloat(snr), RSSI: nullFloat(rssi),
				ObserverID: nullStr(obsID), ObserverName: nullStr(obsName),
			})
		}
	}

	// Packet type breakdown
	ptSQL := fmt.Sprintf("SELECT payload_type, COUNT(*) as count FROM packets_v WHERE %s GROUP BY payload_type", timeWhere)
	ptRows, _ := s.db.conn.Query(ptSQL, pk, np, fromISO)
	packetTypeBreakdown := make([]PayloadTypeCount, 0)
	if ptRows != nil {
		defer ptRows.Close()
		for ptRows.Next() {
			var pt, count int
			ptRows.Scan(&pt, &count)
			packetTypeBreakdown = append(packetTypeBreakdown, PayloadTypeCount{PayloadType: pt, Count: count})
		}
	}

	// Observer coverage
	ocSQL := fmt.Sprintf(`SELECT observer_id, observer_name, COUNT(*) as packetCount,
		AVG(snr) as avgSnr, AVG(rssi) as avgRssi, MIN(timestamp) as firstSeen, MAX(timestamp) as lastSeen
		FROM packets_v WHERE %s AND observer_id IS NOT NULL
		GROUP BY observer_id ORDER BY packetCount DESC`, timeWhere)
	ocRows, _ := s.db.conn.Query(ocSQL, pk, np, fromISO)
	observerCoverage := make([]NodeObserverStatsResp, 0)
	if ocRows != nil {
		defer ocRows.Close()
		for ocRows.Next() {
			var obsID, obsName, first, last sql.NullString
			var pktCount int
			var avgSnr, avgRssi sql.NullFloat64
			ocRows.Scan(&obsID, &obsName, &pktCount, &avgSnr, &avgRssi, &first, &last)
			observerCoverage = append(observerCoverage, NodeObserverStatsResp{
				ObserverID: nullStr(obsID), ObserverName: nullStr(obsName),
				PacketCount: pktCount, AvgSnr: nullFloat(avgSnr), AvgRssi: nullFloat(avgRssi),
				FirstSeen: nullStr(first), LastSeen: nullStr(last),
			})
		}
	}

	// Hop distribution from path_json
	hdSQL := fmt.Sprintf(`SELECT path_json FROM packets_v WHERE %s AND path_json IS NOT NULL AND path_json != '[]'`, timeWhere)
	hdRows, _ := s.db.conn.Query(hdSQL, pk, np, fromISO)
	hopCounts := map[string]int{}
	if hdRows != nil {
		defer hdRows.Close()
		for hdRows.Next() {
			var pj string
			hdRows.Scan(&pj)
			var hops []interface{}
			if json.Unmarshal([]byte(pj), &hops) == nil {
				key := fmt.Sprintf("%d", len(hops))
				if len(hops) >= 4 {
					key = "4+"
				}
				hopCounts[key]++
			}
		}
	}
	// Also count zero-hop packets
	zhSQL := fmt.Sprintf(`SELECT COUNT(*) FROM packets_v WHERE %s AND (path_json IS NULL OR path_json = '[]')`, timeWhere)
	var zeroHops int
	s.db.conn.QueryRow(zhSQL, pk, np, fromISO).Scan(&zeroHops)
	if zeroHops > 0 {
		hopCounts["0"] += zeroHops
	}
	hopDistribution := make([]HopDistEntry, 0)
	for _, h := range []string{"0", "1", "2", "3", "4+"} {
		if c, ok := hopCounts[h]; ok {
			hopDistribution = append(hopDistribution, HopDistEntry{Hops: h, Count: c})
		}
	}

	// Uptime heatmap
	uhSQL := fmt.Sprintf(`SELECT
		CAST(strftime('%%w', timestamp) AS INTEGER) as dayOfWeek,
		CAST(strftime('%%H', timestamp) AS INTEGER) as hour,
		COUNT(*) as count
		FROM packets_v WHERE %s
		GROUP BY dayOfWeek, hour ORDER BY count DESC`, timeWhere)
	uhRows, _ := s.db.conn.Query(uhSQL, pk, np, fromISO)
	uptimeHeatmap := make([]HeatmapCell, 0)
	if uhRows != nil {
		defer uhRows.Close()
		for uhRows.Next() {
			var dow, hr, cnt int
			uhRows.Scan(&dow, &hr, &cnt)
			uptimeHeatmap = append(uptimeHeatmap, HeatmapCell{DayOfWeek: dow, Hour: hr, Count: cnt})
		}
	}

	// Computed stats
	totalPackets := 0
	for _, entry := range activityTimeline {
		totalPackets += entry.Count
	}

	var snrMean, snrStdDev float64
	var snrCount int
	snrStatsSQL := fmt.Sprintf(`SELECT COUNT(*), COALESCE(AVG(snr),0), COALESCE(
		SQRT(AVG(snr*snr) - AVG(snr)*AVG(snr)), 0)
		FROM packets_v WHERE %s AND snr IS NOT NULL`, timeWhere)
	s.db.conn.QueryRow(snrStatsSQL, pk, np, fromISO).Scan(&snrCount, &snrMean, &snrStdDev)
	_ = snrCount

	signalGrade := "D"
	if snrMean >= 10 {
		signalGrade = "A"
	} else if snrMean >= 7 {
		signalGrade = "A-"
	} else if snrMean >= 4 {
		signalGrade = "B+"
	} else if snrMean >= 1 {
		signalGrade = "B"
	} else if snrMean >= -3 {
		signalGrade = "C"
	}

	relayCount := 0
	for _, h := range []string{"1", "2", "3", "4+"} {
		relayCount += hopCounts[h]
	}
	var relayPct float64
	if totalPackets > 0 {
		relayPct = round(float64(relayCount)*100.0/float64(totalPackets), 1)
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

	// Availability
	totalHours := float64(days) * 24
	activeHours := float64(len(activityTimeline))
	availabilityPct := round(activeHours*100.0/totalHours, 1)
	if availabilityPct > 100 {
		availabilityPct = 100
	}

	writeJSON(w, NodeAnalyticsResponse{
		Node:                node,
		TimeRange:           TimeRangeResp{From: fromISO, To: toISO, Days: days},
		ActivityTimeline:    activityTimeline,
		SnrTrend:            snrTrend,
		PacketTypeBreakdown: packetTypeBreakdown,
		ObserverCoverage:    observerCoverage,
		HopDistribution:     hopDistribution,
		PeerInteractions:    []PeerInteraction{},
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
			UniquePeers:         0,
			AvgPacketsPerDay:    avgPacketsPerDay,
		},
	})
}

// --- Analytics Handlers ---

func (s *Server) handleAnalyticsRF(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		region := r.URL.Query().Get("region")
		writeJSON(w, s.store.GetAnalyticsRF(region))
		return
	}
	// Fallback: basic RF analytics from SQL
	region := r.URL.Query().Get("region")
	regionFilter := ""
	var rArgs []interface{}
	if region != "" {
		regionFilter = "AND observer_id IN (SELECT id FROM observers WHERE iata = ?)"
		rArgs = append(rArgs, region)
	}

	// SNR/RSSI stats
	rfSQL := fmt.Sprintf(`SELECT COUNT(*) as cnt, AVG(snr) as avgSnr, MIN(snr) as minSnr, MAX(snr) as maxSnr,
		AVG(rssi) as avgRssi, MIN(rssi) as minRssi, MAX(rssi) as maxRssi
		FROM packets_v WHERE snr IS NOT NULL %s`, regionFilter)
	var cnt int
	var avgSnr, minSnr, maxSnr, avgRssi, minRssi, maxRssi sql.NullFloat64
	s.db.conn.QueryRow(rfSQL, rArgs...).Scan(&cnt, &avgSnr, &minSnr, &maxSnr, &avgRssi, &minRssi, &maxRssi)

	// Payload type distribution
	ptSQL := fmt.Sprintf(`SELECT payload_type, COUNT(DISTINCT hash) as count FROM packets_v WHERE 1=1 %s GROUP BY payload_type ORDER BY count DESC`, regionFilter)
	ptRows, _ := s.db.conn.Query(ptSQL, rArgs...)
	payloadTypes := make([]PayloadTypeEntry, 0)
	ptNames := map[int]string{0: "REQ", 1: "RESPONSE", 2: "TXT_MSG", 3: "ACK", 4: "ADVERT", 5: "GRP_TXT", 7: "ANON_REQ", 8: "PATH", 9: "TRACE", 11: "CONTROL"}
	if ptRows != nil {
		defer ptRows.Close()
		for ptRows.Next() {
			var pt, count int
			ptRows.Scan(&pt, &count)
			name := ptNames[pt]
			if name == "" {
				name = fmt.Sprintf("UNK(%d)", pt)
			}
			payloadTypes = append(payloadTypes, PayloadTypeEntry{Type: pt, Name: name, Count: count})
		}
	}

	// Total counts
	var totalAll int
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM packets_v WHERE 1=1 %s", regionFilter)
	s.db.conn.QueryRow(countSQL, rArgs...).Scan(&totalAll)
	var totalTx int
	txSQL := fmt.Sprintf("SELECT COUNT(DISTINCT hash) FROM packets_v WHERE 1=1 %s", regionFilter)
	s.db.conn.QueryRow(txSQL, rArgs...).Scan(&totalTx)

	writeJSON(w, RFAnalyticsResponse{
		TotalPackets:       cnt,
		TotalAllPackets:    totalAll,
		TotalTransmissions: totalTx,
		SNR: SignalStats{
			Min: nullFloatVal(minSnr), Max: nullFloatVal(maxSnr),
			Avg: nullFloatVal(avgSnr), Median: 0, Stddev: 0,
		},
		RSSI: SignalStats{
			Min: nullFloatVal(minRssi), Max: nullFloatVal(maxRssi),
			Avg: nullFloatVal(avgRssi), Median: 0, Stddev: 0,
		},
		SnrValues:      Histogram{Bins: []HistogramBin{}, Min: 0, Max: 0},
		RssiValues:     Histogram{Bins: []HistogramBin{}, Min: 0, Max: 0},
		PacketSizes:    Histogram{Bins: []HistogramBin{}, Min: 0, Max: 0},
		MinPacketSize:  0,
		MaxPacketSize:  0,
		AvgPacketSize:  0,
		PacketsPerHour:  []HourlyCount{},
		PayloadTypes:    payloadTypes,
		SnrByType:       []PayloadTypeSignal{},
		SignalOverTime:  []SignalOverTimeEntry{},
		ScatterData:     []ScatterPoint{},
		TimeSpanHours:   0,
	})
}

func (s *Server) handleAnalyticsTopology(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		region := r.URL.Query().Get("region")
		writeJSON(w, s.store.GetAnalyticsTopology(region))
		return
	}
	// SQL fallback — compute basic topology from path_json
	region := r.URL.Query().Get("region")
	regionFilter := ""
	var rArgs []interface{}
	if region != "" {
		regionFilter = "AND observer_id IN (SELECT id FROM observers WHERE iata = ?)"
		rArgs = append(rArgs, region)
	}

	pathSQL := fmt.Sprintf("SELECT path_json, snr FROM packets_v WHERE path_json IS NOT NULL AND path_json != '[]' %s", regionFilter)
	pathRows, _ := s.db.conn.Query(pathSQL, rArgs...)

	hopCountMap := map[int]int{}
	repeaterCounts := map[string]int{}
	pairCounts := map[string]int{}
	nodesSeen := map[string]bool{}
	hopsVsSnrSum := map[int]float64{}
	hopsVsSnrCnt := map[int]int{}

	if pathRows != nil {
		defer pathRows.Close()
		for pathRows.Next() {
			var pj string
			var snr sql.NullFloat64
			pathRows.Scan(&pj, &snr)
			var hops []string
			if json.Unmarshal([]byte(pj), &hops) != nil || len(hops) == 0 {
				continue
			}
			hc := len(hops)
			if hc > 25 {
				hc = 25
			}
			hopCountMap[hc]++
			for _, h := range hops {
				nodesSeen[h] = true
				repeaterCounts[h]++
			}
			for i := 0; i+1 < len(hops); i++ {
				pair := hops[i] + ":" + hops[i+1]
				pairCounts[pair]++
			}
			if snr.Valid {
				hopsVsSnrSum[hc] += snr.Float64
				hopsVsSnrCnt[hc]++
			}
		}
	}

	var totalHopCount, maxHops int
	for h, c := range hopCountMap {
		totalHopCount += h * c
		if h > maxHops {
			maxHops = h
		}
	}
	totalPaths := 0
	for _, c := range hopCountMap {
		totalPaths += c
	}
	var avgHops float64
	if totalPaths > 0 {
		avgHops = round(float64(totalHopCount)/float64(totalPaths), 1)
	}

	hopDistribution := make([]TopologyHopDist, 0)
	for h := 1; h <= maxHops; h++ {
		if c, ok := hopCountMap[h]; ok {
			hopDistribution = append(hopDistribution, TopologyHopDist{Hops: h, Count: c})
		}
	}

	type kv struct {
		k string
		v int
	}
	var repSorted []kv
	for k, v := range repeaterCounts {
		repSorted = append(repSorted, kv{k, v})
	}
	sort.Slice(repSorted, func(i, j int) bool { return repSorted[i].v > repSorted[j].v })
	topRepeaters := make([]TopRepeater, 0)
	for i, rp := range repSorted {
		if i >= 20 {
			break
		}
		topRepeaters = append(topRepeaters, TopRepeater{Hop: rp.k, Count: rp.v, Name: nil, Pubkey: nil})
	}

	var pairSorted []kv
	for k, v := range pairCounts {
		pairSorted = append(pairSorted, kv{k, v})
	}
	sort.Slice(pairSorted, func(i, j int) bool { return pairSorted[i].v > pairSorted[j].v })
	topPairs := make([]TopPair, 0)
	for i, p := range pairSorted {
		if i >= 20 {
			break
		}
		parts := strings.SplitN(p.k, ":", 2)
		topPairs = append(topPairs, TopPair{
			HopA: parts[0], HopB: parts[1], Count: p.v,
			NameA: nil, NameB: nil, PubkeyA: nil, PubkeyB: nil,
		})
	}

	hopsVsSnr := make([]HopsVsSnr, 0)
	for h := 1; h <= maxHops; h++ {
		if cnt, ok := hopsVsSnrCnt[h]; ok && cnt > 0 {
			hopsVsSnr = append(hopsVsSnr, HopsVsSnr{
				Hops: h, Count: cnt, AvgSnr: round(hopsVsSnrSum[h]/float64(cnt), 1),
			})
		}
	}

	obsList := make([]ObserverRef, 0)
	obsRows, _ := s.db.conn.Query("SELECT id, name FROM observers")
	if obsRows != nil {
		defer obsRows.Close()
		for obsRows.Next() {
			var oid string
			var oname sql.NullString
			obsRows.Scan(&oid, &oname)
			obsList = append(obsList, ObserverRef{ID: oid, Name: nullStr(oname)})
		}
	}

	writeJSON(w, TopologyResponse{
		UniqueNodes:      len(nodesSeen),
		AvgHops:          avgHops,
		MedianHops:       0,
		MaxHops:          maxHops,
		HopDistribution:  hopDistribution,
		TopRepeaters:     topRepeaters,
		TopPairs:         topPairs,
		HopsVsSnr:        hopsVsSnr,
		Observers:        obsList,
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
	if s.store != nil {
		region := r.URL.Query().Get("region")
		writeJSON(w, s.store.GetAnalyticsDistance(region))
		return
	}
	// SQL fallback
	region := r.URL.Query().Get("region")
	regionFilter := ""
	var rArgs []interface{}
	if region != "" {
		regionFilter = "AND observer_id IN (SELECT id FROM observers WHERE iata = ?)"
		rArgs = append(rArgs, region)
	}

	nodeLocMap := s.db.GetNodeLocations()
	_ = nodeLocMap

	pathSQL := fmt.Sprintf("SELECT path_json, hash, timestamp, snr FROM packets_v WHERE path_json IS NOT NULL AND path_json != '[]' %s ORDER BY timestamp DESC LIMIT 5000", regionFilter)
	pathRows, _ := s.db.conn.Query(pathSQL, rArgs...)

	var totalHops, totalPaths int
	var maxDist, distSum float64
	topHops := make([]DistanceHop, 0)
	topPaths := make([]DistancePath, 0)
	catStats := map[string]*CategoryDistStats{}
	distOverTime := make([]DistOverTimeEntry, 0)

	if pathRows != nil {
		defer pathRows.Close()
		for pathRows.Next() {
			var pj, hash string
			var ts sql.NullString
			var snr sql.NullFloat64
			pathRows.Scan(&pj, &hash, &ts, &snr)
			var hops []string
			if json.Unmarshal([]byte(pj), &hops) != nil || len(hops) == 0 {
				continue
			}
			totalPaths++
			totalHops += len(hops)
		}
	}

	var avgDist float64
	if totalHops > 0 {
		avgDist = round(distSum/float64(totalHops), 2)
	}

	writeJSON(w, DistanceAnalyticsResponse{
		Summary: DistanceSummary{
			TotalHops: totalHops, TotalPaths: totalPaths,
			AvgDist: avgDist, MaxDist: maxDist,
		},
		TopHops:       topHops,
		TopPaths:      topPaths,
		CatStats:      catStats,
		DistHistogram: nil,
		DistOverTime:  distOverTime,
	})
}

func (s *Server) handleAnalyticsHashSizes(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		region := r.URL.Query().Get("region")
		writeJSON(w, s.store.GetAnalyticsHashSizes(region))
		return
	}
	writeJSON(w, HashSizeAnalyticsResponse{
		Total:          0,
		Distribution:   map[string]int{"1": 0, "2": 0, "3": 0},
		Hourly:         []HashSizeHourly{},
		TopHops:        []HashSizeHop{},
		MultiByteNodes: []MultiByteNode{},
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

	for _, hop := range hops {
		if hop == "" {
			continue
		}
		hopLower := strings.ToLower(hop)
		rows, err := s.db.conn.Query("SELECT public_key, name, lat, lon FROM nodes WHERE LOWER(public_key) LIKE ?", hopLower+"%")
		if err != nil {
			resolved[hop] = &HopResolution{Name: nil, Candidates: []HopCandidate{}, Conflicts: []interface{}{}}
			continue
		}

		var candidates []HopCandidate
		for rows.Next() {
			var pk string
			var name sql.NullString
			var lat, lon sql.NullFloat64
			rows.Scan(&pk, &name, &lat, &lon)
			candidates = append(candidates, HopCandidate{
				Name: nullStr(name), Pubkey: pk,
				Lat: nullFloat(lat), Lon: nullFloat(lon),
			})
		}
		rows.Close()

		if len(candidates) == 0 {
			resolved[hop] = &HopResolution{Name: nil, Candidates: []HopCandidate{}, Conflicts: []interface{}{}}
		} else if len(candidates) == 1 {
			resolved[hop] = &HopResolution{
				Name: candidates[0].Name, Pubkey: candidates[0].Pubkey,
				Candidates: candidates, Conflicts: []interface{}{},
			}
		} else {
			ambig := true
			resolved[hop] = &HopResolution{
				Name: candidates[0].Name, Pubkey: candidates[0].Pubkey,
				Ambiguous: &ambig, Candidates: candidates, Conflicts: hopCandidatesToConflicts(candidates),
			}
		}
	}
	writeJSON(w, ResolveHopsResponse{Resolved: resolved})
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		region := r.URL.Query().Get("region")
		channels := s.store.GetChannels(region)
		writeJSON(w, ChannelListResponse{Channels: channels})
		return
	}
	channels, err := s.db.GetChannels()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, ChannelListResponse{Channels: channels})
}

func (s *Server) handleChannelMessages(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)
	if s.store != nil {
		messages, total := s.store.GetChannelMessages(hash, limit, offset)
		writeJSON(w, ChannelMessagesResponse{Messages: messages, Total: total})
		return
	}
	messages, total, err := s.db.GetChannelMessages(hash, limit, offset)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, ChannelMessagesResponse{Messages: messages, Total: total})
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

	// Batch lookup: node locations (observer ID may match a node public_key)
	nodeLocations := s.db.GetNodeLocations()

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
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)

	// Timeline
	bucketH := 4
	if days <= 1 {
		bucketH = 1
	} else if days > 7 {
		bucketH = 24
	}
	// Timeline — packet count per time bucket
	bucketFmt := fmt.Sprintf("strftime('%%Y-%%m-%%dT', timestamp) || printf('%%02d', (CAST(strftime('%%H', timestamp) AS INTEGER) / %d) * %d) || ':00:00Z'", bucketH, bucketH)
	tlSQL := fmt.Sprintf(`SELECT %s as label, COUNT(*) as count
		FROM packets_v WHERE observer_id = ? AND timestamp > ?
		GROUP BY label ORDER BY label`, bucketFmt)
	tlRows, _ := s.db.conn.Query(tlSQL, id, since)
	timeline := make([]TimeBucket, 0)
	if tlRows != nil {
		defer tlRows.Close()
		for tlRows.Next() {
			var label string
			var count int
			tlRows.Scan(&label, &count)
			l := label
			timeline = append(timeline, TimeBucket{Label: &l, Count: count})
		}
	}

	// Nodes timeline — unique nodes per time bucket
	ntSQL := fmt.Sprintf(`SELECT %s as label, COUNT(DISTINCT hash) as count
		FROM packets_v WHERE observer_id = ? AND timestamp > ?
		GROUP BY label ORDER BY label`, bucketFmt)
	ntRows, _ := s.db.conn.Query(ntSQL, id, since)
	nodesTimeline := make([]TimeBucket, 0)
	if ntRows != nil {
		defer ntRows.Close()
		for ntRows.Next() {
			var label string
			var count int
			ntRows.Scan(&label, &count)
			l := label
			nodesTimeline = append(nodesTimeline, TimeBucket{Label: &l, Count: count})
		}
	}

	// SNR distribution
	snrSQL := `SELECT
		CAST(snr / 2 AS INTEGER) * 2 as rangeStart,
		COUNT(*) as count
		FROM packets_v WHERE observer_id = ? AND timestamp > ? AND snr IS NOT NULL
		GROUP BY rangeStart ORDER BY rangeStart`
	snrRows, _ := s.db.conn.Query(snrSQL, id, since)
	snrDistribution := make([]SnrDistributionEntry, 0)
	if snrRows != nil {
		defer snrRows.Close()
		for snrRows.Next() {
			var rangeStart, count int
			snrRows.Scan(&rangeStart, &count)
			snrDistribution = append(snrDistribution, SnrDistributionEntry{
				Range: fmt.Sprintf("%d to %d", rangeStart, rangeStart+2),
				Count: count,
			})
		}
	}

	// Packet type breakdown
	ptSQL := `SELECT payload_type, COUNT(*) as count FROM packets_v WHERE observer_id = ? AND timestamp > ? GROUP BY payload_type`
	ptRows, _ := s.db.conn.Query(ptSQL, id, since)
	packetTypes := map[string]int{}
	if ptRows != nil {
		defer ptRows.Close()
		for ptRows.Next() {
			var pt, count int
			ptRows.Scan(&pt, &count)
			packetTypes[strconv.Itoa(pt)] = count
		}
	}

	// Recent packets
	rpSQL := `SELECT id, raw_hex, timestamp, observer_id, observer_name, direction, snr, rssi, score, hash, route_type, payload_type, payload_version, path_json, decoded_json, created_at
		FROM packets_v WHERE observer_id = ? AND timestamp > ? ORDER BY timestamp DESC LIMIT 20`
	rpRows, _ := s.db.conn.Query(rpSQL, id, since)
	recentPackets := make([]map[string]interface{}, 0)
	if rpRows != nil {
		defer rpRows.Close()
		for rpRows.Next() {
			p := scanPacketRow(rpRows)
			if p != nil {
				recentPackets = append(recentPackets, p)
			}
		}
	}

	writeJSON(w, ObserverAnalyticsResponse{
		Timeline:        timeline,
		PacketTypes:     packetTypes,
		NodesTimeline:   nodesTimeline,
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
	} else {
		// Fallback: direct DB query when store is not loaded
		ptSQL := `SELECT payload_type, id, raw_hex, hash, decoded_json, path_json, observer_id, timestamp
			FROM (
				SELECT *, ROW_NUMBER() OVER (PARTITION BY payload_type ORDER BY length(raw_hex)) as rn
				FROM packets_v WHERE raw_hex IS NOT NULL
			) sub WHERE rn <= 8`
		rows, err := s.db.conn.Query(ptSQL)
		if err != nil {
			writeJSON(w, AudioLabBucketsResponse{Buckets: buckets})
			return
		}
		defer rows.Close()

		for rows.Next() {
			var pt, id int
			var rawHex, hash, decodedJSON, pathJSON, obsID, ts sql.NullString
			rows.Scan(&pt, &id, &rawHex, &hash, &decodedJSON, &pathJSON, &obsID, &ts)
			typeName := payloadTypeNames[pt]
			if typeName == "" {
				typeName = "UNKNOWN"
			}
			buckets[typeName] = append(buckets[typeName], AudioLabPacket{
				Hash: nullStr(hash), RawHex: nullStr(rawHex),
				DecodedJSON: nullStr(decodedJSON), ObservationCount: 1,
				PayloadType: pt, PathJSON: nullStr(pathJSON),
				ObserverID: nullStr(obsID), Timestamp: nullStr(ts),
			})
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
	for i := 0; i < len(cp); i++ {
		for j := i + 1; j < len(cp); j++ {
			if cp[j] < cp[i] {
				cp[i], cp[j] = cp[j], cp[i]
			}
		}
	}
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
