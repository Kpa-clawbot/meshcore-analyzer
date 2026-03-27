package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
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
}

// PerfStats tracks request performance.
type PerfStats struct {
	Requests    int64
	TotalMs     float64
	Endpoints   map[string]*EndpointPerf
	SlowQueries []map[string]interface{}
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
		SlowQueries: make([]map[string]interface{}, 0),
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
	}
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

	// System endpoints
	r.HandleFunc("/api/health", s.handleHealth).Methods("GET")
	r.HandleFunc("/api/stats", s.handleStats).Methods("GET")
	r.HandleFunc("/api/perf", s.handlePerf).Methods("GET")

	// Packet endpoints
	r.HandleFunc("/api/packets/timestamps", s.handlePacketTimestamps).Methods("GET")
	r.HandleFunc("/api/packets/{id}", s.handlePacketDetail).Methods("GET")
	r.HandleFunc("/api/packets", s.handlePackets).Methods("GET")

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

		// Normalize key
		re := regexp.MustCompile(`[0-9a-f]{8,}`)
		key := re.ReplaceAllString(r.URL.Path, ":id")
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
			slow := map[string]interface{}{
				"path": r.URL.Path, "ms": round(ms, 1),
				"time": time.Now().UTC().Format(time.RFC3339), "status": 200,
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
	writeJSON(w, ct)
}

func (s *Server) handleConfigClient(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"roles":              s.cfg.Roles,
		"healthThresholds":   s.cfg.HealthThresholds,
		"tiles":              s.cfg.Tiles,
		"snrThresholds":      s.cfg.SnrThresholds,
		"distThresholds":     s.cfg.DistThresholds,
		"maxHopDist":         s.cfg.MaxHopDist,
		"limits":             s.cfg.Limits,
		"perfSlowMs":         s.cfg.PerfSlowMs,
		"wsReconnectMs":      s.cfg.WsReconnectMs,
		"cacheInvalidateMs":  s.cfg.CacheInvalidMs,
		"externalUrls":       s.cfg.ExternalUrls,
		"propagationBufferMs": s.cfg.PropagationBufferMs(),
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

	writeJSON(w, map[string]interface{}{
		"branding":   branding,
		"theme":      themeColors,
		"themeDark":  themeDark,
		"nodeColors": nodeColors,
		"typeColors": typeColors,
		"home":       home,
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
	writeJSON(w, map[string]interface{}{"center": center, "zoom": zoom})
}

// --- System Handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	uptime := time.Since(s.startedAt).Seconds()

	wsClients := 0
	if s.hub != nil {
		wsClients = s.hub.ClientCount()
	}

	writeJSON(w, map[string]interface{}{
		"status":      "ok",
		"uptime":      int(uptime),
		"uptimeHuman": fmt.Sprintf("%dh %dm", int(uptime)/3600, (int(uptime)%3600)/60),
		"memory": map[string]interface{}{
			"rss":       int(m.Sys / 1024 / 1024),
			"heapUsed":  int(m.HeapAlloc / 1024 / 1024),
			"heapTotal": int(m.HeapSys / 1024 / 1024),
			"external":  0,
		},
		"eventLoop": map[string]interface{}{
			"currentLagMs": 0, "maxLagMs": 0,
			"p50Ms": 0, "p95Ms": 0, "p99Ms": 0,
		},
		"cache": map[string]interface{}{
			"entries": 0, "hits": 0, "misses": 0,
			"staleHits": 0, "recomputes": 0, "hitRate": 0,
		},
		"websocket": map[string]interface{}{"clients": wsClients},
		"packetStore": map[string]interface{}{
			"packets": 0, "estimatedMB": 0,
		},
		"perf": map[string]interface{}{
			"totalRequests": s.perfStats.Requests,
			"avgMs":         safeAvg(s.perfStats.TotalMs, float64(s.perfStats.Requests)),
			"slowQueries":   len(s.perfStats.SlowQueries),
			"recentSlow":    lastN(s.perfStats.SlowQueries, 5),
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
	result := map[string]interface{}{
		"totalPackets":       stats.TotalPackets,
		"totalTransmissions": stats.TotalTransmissions,
		"totalObservations":  stats.TotalObservations,
		"totalNodes":         stats.TotalNodes,
		"totalNodesAllTime":  stats.TotalNodesAllTime,
		"totalObservers":     stats.TotalObservers,
		"packetsLastHour":    stats.PacketsLastHour,
		"counts":             counts,
	}
	writeJSON(w, result)
}

func (s *Server) handlePerf(w http.ResponseWriter, r *http.Request) {
	summary := map[string]interface{}{}
	for path, ep := range s.perfStats.Endpoints {
		sorted := sortedCopy(ep.Recent)
		summary[path] = map[string]interface{}{
			"count": ep.Count,
			"avgMs": round(ep.TotalMs/float64(ep.Count), 1),
			"p50Ms": round(percentile(sorted, 0.5), 1),
			"p95Ms": round(percentile(sorted, 0.95), 1),
			"maxMs": round(ep.MaxMs, 1),
		}
	}
	writeJSON(w, map[string]interface{}{
		"uptime":        int(time.Since(s.perfStats.StartedAt).Seconds()),
		"totalRequests": s.perfStats.Requests,
		"avgMs":         safeAvg(s.perfStats.TotalMs, float64(s.perfStats.Requests)),
		"endpoints":     summary,
		"slowQueries":   lastN(s.perfStats.SlowQueries, 20),
		"cache": map[string]interface{}{
			"size": 0, "hits": 0, "misses": 0,
			"staleHits": 0, "recomputes": 0, "hitRate": 0,
		},
	})
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
		writeJSON(w, map[string]interface{}{
			"packets": result.Packets,
			"total":   result.Total,
			"limit":   queryInt(r, "limit", 50),
			"offset":  queryInt(r, "offset", 0),
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

	writeJSON(w, map[string]interface{}{
		"packet":            packet,
		"path":              pathHops,
		"breakdown":         map[string]interface{}{},
		"observation_count": observationCount,
		"observations":      observations,
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
	writeJSON(w, map[string]interface{}{"nodes": nodes, "total": total, "counts": counts})
}

func (s *Server) handleNodeSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeJSON(w, map[string]interface{}{"nodes": []interface{}{}})
		return
	}
	nodes, err := s.db.SearchNodes(strings.TrimSpace(q), 10)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"nodes": nodes})
}

func (s *Server) handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		writeError(w, 404, "Not found")
		return
	}

	name := ""
	if n, ok := node["name"]; ok && n != nil {
		name = fmt.Sprintf("%v", n)
	}
	recentAdverts, _ := s.db.GetRecentTransmissionsForNode(pubkey, name, 20)

	writeJSON(w, map[string]interface{}{
		"node":          node,
		"recentAdverts": recentAdverts,
	})
}

func (s *Server) handleNodeHealth(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	result, err := s.db.GetNodeHealth(pubkey)
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

	rows, err := s.db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen FROM nodes ORDER BY last_seen DESC LIMIT ?", limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer rows.Close()

	type nodeInfo struct {
		pk, name, role, lastSeen string
		lat, lon                 interface{}
	}
	var nodes []nodeInfo
	for rows.Next() {
		var pk string
		var name, role, lastSeen sql.NullString
		var lat, lon sql.NullFloat64
		rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen)
		nodes = append(nodes, nodeInfo{
			pk: pk, name: nullStrVal(name), role: nullStrVal(role),
			lastSeen: nullStrVal(lastSeen),
			lat:      nullFloat(lat), lon: nullFloat(lon),
		})
	}

	// Batch query: per-node transmission stats
	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)
	results := make([]map[string]interface{}, 0, len(nodes))
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

		results = append(results, map[string]interface{}{
			"public_key": n.pk,
			"name":       nilIfEmpty(n.name),
			"role":       nilIfEmpty(n.role),
			"lat":        n.lat,
			"lon":        n.lon,
			"stats": map[string]interface{}{
				"totalTransmissions": txCount,
				"totalObservations":  obsCount,
				"totalPackets":       txCount,
				"packetsToday":       packetsToday,
				"avgSnr":             nullFloat(avgSnr),
				"lastHeard":          nilIfEmpty(lh),
			},
			"observers": []interface{}{},
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
	writeJSON(w, map[string]interface{}{
		"node":               node,
		"paths":              []interface{}{},
		"totalPaths":         0,
		"totalTransmissions": 0,
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
	activityTimeline := make([]map[string]interface{}, 0)
	if aRows != nil {
		defer aRows.Close()
		for aRows.Next() {
			var bucket string
			var count int
			aRows.Scan(&bucket, &count)
			activityTimeline = append(activityTimeline, map[string]interface{}{"bucket": bucket, "count": count})
		}
	}

	// SNR trend
	snrSQL := fmt.Sprintf(`SELECT timestamp, snr, rssi, observer_id, observer_name
		FROM packets_v WHERE %s AND snr IS NOT NULL ORDER BY timestamp`, timeWhere)
	sRows, _ := s.db.conn.Query(snrSQL, pk, np, fromISO)
	snrTrend := make([]map[string]interface{}, 0)
	if sRows != nil {
		defer sRows.Close()
		for sRows.Next() {
			var ts string
			var snr, rssi sql.NullFloat64
			var obsID, obsName sql.NullString
			sRows.Scan(&ts, &snr, &rssi, &obsID, &obsName)
			snrTrend = append(snrTrend, map[string]interface{}{
				"timestamp": ts, "snr": nullFloat(snr), "rssi": nullFloat(rssi),
				"observer_id": nullStr(obsID), "observer_name": nullStr(obsName),
			})
		}
	}

	// Packet type breakdown
	ptSQL := fmt.Sprintf("SELECT payload_type, COUNT(*) as count FROM packets_v WHERE %s GROUP BY payload_type", timeWhere)
	ptRows, _ := s.db.conn.Query(ptSQL, pk, np, fromISO)
	packetTypeBreakdown := make([]map[string]interface{}, 0)
	if ptRows != nil {
		defer ptRows.Close()
		for ptRows.Next() {
			var pt, count int
			ptRows.Scan(&pt, &count)
			packetTypeBreakdown = append(packetTypeBreakdown, map[string]interface{}{"payload_type": pt, "count": count})
		}
	}

	// Observer coverage
	ocSQL := fmt.Sprintf(`SELECT observer_id, observer_name, COUNT(*) as packetCount,
		AVG(snr) as avgSnr, AVG(rssi) as avgRssi, MIN(timestamp) as firstSeen, MAX(timestamp) as lastSeen
		FROM packets_v WHERE %s AND observer_id IS NOT NULL
		GROUP BY observer_id ORDER BY packetCount DESC`, timeWhere)
	ocRows, _ := s.db.conn.Query(ocSQL, pk, np, fromISO)
	observerCoverage := make([]map[string]interface{}, 0)
	if ocRows != nil {
		defer ocRows.Close()
		for ocRows.Next() {
			var obsID, obsName, first, last sql.NullString
			var pktCount int
			var avgSnr, avgRssi sql.NullFloat64
			ocRows.Scan(&obsID, &obsName, &pktCount, &avgSnr, &avgRssi, &first, &last)
			observerCoverage = append(observerCoverage, map[string]interface{}{
				"observer_id": nullStr(obsID), "observer_name": nullStr(obsName),
				"packetCount": pktCount, "avgSnr": nullFloat(avgSnr), "avgRssi": nullFloat(avgRssi),
				"firstSeen": nullStr(first), "lastSeen": nullStr(last),
			})
		}
	}

	writeJSON(w, map[string]interface{}{
		"node":                node,
		"timeRange":           map[string]interface{}{"from": fromISO, "to": toISO, "days": days},
		"activityTimeline":    activityTimeline,
		"snrTrend":            snrTrend,
		"packetTypeBreakdown": packetTypeBreakdown,
		"observerCoverage":    observerCoverage,
		"hopDistribution":     []interface{}{},
		"peerInteractions":    []interface{}{},
		"uptimeHeatmap":       []interface{}{},
		"computedStats": map[string]interface{}{
			"availabilityPct": 0, "longestSilenceMs": 0, "longestSilenceStart": nil,
			"signalGrade": "D", "snrMean": 0, "snrStdDev": 0,
			"relayPct": 0, "totalPackets": len(activityTimeline),
			"uniqueObservers": len(observerCoverage), "uniquePeers": 0, "avgPacketsPerDay": 0,
		},
	})
}

// --- Analytics Handlers ---

func (s *Server) handleAnalyticsRF(w http.ResponseWriter, r *http.Request) {
	// Basic RF analytics from SQL
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
	payloadTypes := make([]map[string]interface{}, 0)
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
			payloadTypes = append(payloadTypes, map[string]interface{}{"type": pt, "name": name, "count": count})
		}
	}

	// Total counts
	var totalAll int
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM packets_v WHERE 1=1 %s", regionFilter)
	s.db.conn.QueryRow(countSQL, rArgs...).Scan(&totalAll)
	var totalTx int
	txSQL := fmt.Sprintf("SELECT COUNT(DISTINCT hash) FROM packets_v WHERE 1=1 %s", regionFilter)
	s.db.conn.QueryRow(txSQL, rArgs...).Scan(&totalTx)

	writeJSON(w, map[string]interface{}{
		"totalPackets":      cnt,
		"totalAllPackets":   totalAll,
		"totalTransmissions": totalTx,
		"snr": map[string]interface{}{
			"min": nullFloat(minSnr), "max": nullFloat(maxSnr),
			"avg": nullFloat(avgSnr), "median": 0, "stddev": 0,
		},
		"rssi": map[string]interface{}{
			"min": nullFloat(minRssi), "max": nullFloat(maxRssi),
			"avg": nullFloat(avgRssi), "median": 0, "stddev": 0,
		},
		"snrValues":     map[string]interface{}{"bins": []interface{}{}, "min": 0, "max": 0},
		"rssiValues":    map[string]interface{}{"bins": []interface{}{}, "min": 0, "max": 0},
		"packetSizes":   map[string]interface{}{"bins": []interface{}{}, "min": 0, "max": 0},
		"minPacketSize": 0, "maxPacketSize": 0, "avgPacketSize": 0,
		"packetsPerHour":  []interface{}{},
		"payloadTypes":    payloadTypes,
		"snrByType":       []interface{}{},
		"signalOverTime":  []interface{}{},
		"scatterData":     []interface{}{},
		"timeSpanHours":   0,
	})
}

func (s *Server) handleAnalyticsTopology(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"uniqueNodes": 0, "avgHops": 0, "medianHops": 0, "maxHops": 0,
		"hopDistribution":  []interface{}{},
		"topRepeaters":     []interface{}{},
		"topPairs":         []interface{}{},
		"hopsVsSnr":        []interface{}{},
		"observers":        []interface{}{},
		"perObserverReach": map[string]interface{}{},
		"multiObsNodes":    []interface{}{},
		"bestPathList":     []interface{}{},
	})
}

func (s *Server) handleAnalyticsChannels(w http.ResponseWriter, r *http.Request) {
	channels, _ := s.db.GetChannels()
	writeJSON(w, map[string]interface{}{
		"activeChannels": len(channels),
		"decryptable":    len(channels),
		"channels":       channels,
		"topSenders":     []interface{}{},
		"channelTimeline": []interface{}{},
		"msgLengths":     []interface{}{},
	})
}

func (s *Server) handleAnalyticsDistance(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"summary":       map[string]interface{}{"totalHops": 0, "totalPaths": 0, "avgDist": 0, "maxDist": 0},
		"topHops":       []interface{}{},
		"topPaths":      []interface{}{},
		"catStats":      map[string]interface{}{},
		"distHistogram": []interface{}{},
		"distOverTime":  []interface{}{},
	})
}

func (s *Server) handleAnalyticsHashSizes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"total":          0,
		"distribution":   map[string]int{"1": 0, "2": 0, "3": 0},
		"hourly":         []interface{}{},
		"topHops":        []interface{}{},
		"multiByteNodes": []interface{}{},
	})
}

func (s *Server) handleAnalyticsSubpaths(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"subpaths":   []interface{}{},
		"totalPaths": 0,
	})
}

func (s *Server) handleAnalyticsSubpathDetail(w http.ResponseWriter, r *http.Request) {
	hops := r.URL.Query().Get("hops")
	if hops == "" {
		writeJSON(w, map[string]interface{}{"error": "Need at least 2 hops"})
		return
	}
	writeJSON(w, map[string]interface{}{
		"hops":             strings.Split(hops, ","),
		"nodes":            []interface{}{},
		"totalMatches":     0,
		"firstSeen":        nil,
		"lastSeen":         nil,
		"signal":           map[string]interface{}{"avgSnr": nil, "avgRssi": nil, "samples": 0},
		"hourDistribution": make([]int, 24),
		"parentPaths":      []interface{}{},
		"observers":        []interface{}{},
	})
}

// --- Other Handlers ---

func (s *Server) handleResolveHops(w http.ResponseWriter, r *http.Request) {
	hopsParam := r.URL.Query().Get("hops")
	if hopsParam == "" {
		writeJSON(w, map[string]interface{}{"resolved": map[string]interface{}{}})
		return
	}
	hops := strings.Split(hopsParam, ",")
	resolved := map[string]interface{}{}

	for _, hop := range hops {
		if hop == "" {
			continue
		}
		hopLower := strings.ToLower(hop)
		rows, err := s.db.conn.Query("SELECT public_key, name, lat, lon FROM nodes WHERE LOWER(public_key) LIKE ?", hopLower+"%")
		if err != nil {
			resolved[hop] = map[string]interface{}{"name": nil, "candidates": []interface{}{}, "conflicts": []interface{}{}}
			continue
		}

		var candidates []map[string]interface{}
		for rows.Next() {
			var pk string
			var name sql.NullString
			var lat, lon sql.NullFloat64
			rows.Scan(&pk, &name, &lat, &lon)
			candidates = append(candidates, map[string]interface{}{
				"name": nullStr(name), "pubkey": pk,
				"lat": nullFloat(lat), "lon": nullFloat(lon),
			})
		}
		rows.Close()

		if len(candidates) == 0 {
			resolved[hop] = map[string]interface{}{"name": nil, "candidates": []interface{}{}, "conflicts": []interface{}{}}
		} else if len(candidates) == 1 {
			resolved[hop] = map[string]interface{}{
				"name": candidates[0]["name"], "pubkey": candidates[0]["pubkey"],
				"candidates": candidates, "conflicts": []interface{}{},
			}
		} else {
			resolved[hop] = map[string]interface{}{
				"name": candidates[0]["name"], "pubkey": candidates[0]["pubkey"],
				"ambiguous": true, "candidates": candidates, "conflicts": candidates,
			}
		}
	}
	writeJSON(w, map[string]interface{}{"resolved": resolved})
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.db.GetChannels()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"channels": channels})
}

func (s *Server) handleChannelMessages(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)
	messages, total, err := s.db.GetChannelMessages(hash, limit, offset)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"messages": messages, "total": total})
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

	result := make([]map[string]interface{}, 0, len(observers))
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

		m := map[string]interface{}{
			"id": o.ID, "name": o.Name, "iata": o.IATA,
			"last_seen": o.LastSeen, "first_seen": o.FirstSeen,
			"packet_count": o.PacketCount,
			"model": o.Model, "firmware": o.Firmware,
			"client_version": o.ClientVersion, "radio": o.Radio,
			"battery_mv": o.BatteryMv, "uptime_secs": o.UptimeSecs,
			"noise_floor": o.NoiseFloor,
			"packetsLastHour": plh,
			"lat": lat, "lon": lon, "nodeRole": nodeRole,
		}
		result = append(result, m)
	}
	writeJSON(w, map[string]interface{}{
		"observers":   result,
		"server_time": time.Now().UTC().Format(time.RFC3339),
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

	writeJSON(w, map[string]interface{}{
		"id": obs.ID, "name": obs.Name, "iata": obs.IATA,
		"last_seen": obs.LastSeen, "first_seen": obs.FirstSeen,
		"packet_count": obs.PacketCount,
		"model": obs.Model, "firmware": obs.Firmware,
		"client_version": obs.ClientVersion, "radio": obs.Radio,
		"battery_mv": obs.BatteryMv, "uptime_secs": obs.UptimeSecs,
		"noise_floor": obs.NoiseFloor,
		"packetsLastHour": plh,
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
	_ = bucketH

	// Packet type breakdown
	ptSQL := `SELECT payload_type, COUNT(*) as count FROM packets_v WHERE observer_id = ? AND timestamp > ? GROUP BY payload_type`
	ptRows, _ := s.db.conn.Query(ptSQL, id, since)
	packetTypes := map[string]interface{}{}
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

	writeJSON(w, map[string]interface{}{
		"timeline":        []interface{}{},
		"packetTypes":     packetTypes,
		"nodesTimeline":   []interface{}{},
		"snrDistribution": []interface{}{},
		"recentPackets":   recentPackets,
	})
}

func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	traces, err := s.db.GetTraces(hash)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"traces": traces})
}

func (s *Server) handleIATACoords(w http.ResponseWriter, r *http.Request) {
	// Return empty coords — full IATA coordinate table would be in a shared package
	writeJSON(w, map[string]interface{}{"coords": map[string]interface{}{}})
}

func (s *Server) handleAudioLabBuckets(w http.ResponseWriter, r *http.Request) {
	// Query representative packets by type
	ptSQL := `SELECT payload_type, id, raw_hex, hash, decoded_json, path_json, observer_id, timestamp
		FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY payload_type ORDER BY length(raw_hex)) as rn
			FROM packets_v WHERE raw_hex IS NOT NULL
		) sub WHERE rn <= 8`
	rows, err := s.db.conn.Query(ptSQL)
	if err != nil {
		writeJSON(w, map[string]interface{}{"buckets": map[string]interface{}{}})
		return
	}
	defer rows.Close()

	ptNames := map[int]string{0: "REQ", 1: "RESPONSE", 2: "TXT_MSG", 3: "ACK", 4: "ADVERT", 5: "GRP_TXT", 7: "ANON_REQ", 8: "PATH", 9: "TRACE", 11: "CONTROL"}
	buckets := map[string][]map[string]interface{}{}
	for rows.Next() {
		var pt, id int
		var rawHex, hash, decodedJSON, pathJSON, obsID, ts sql.NullString
		rows.Scan(&pt, &id, &rawHex, &hash, &decodedJSON, &pathJSON, &obsID, &ts)
		typeName := ptNames[pt]
		if typeName == "" {
			typeName = "UNKNOWN"
		}
		if _, ok := buckets[typeName]; !ok {
			buckets[typeName] = make([]map[string]interface{}, 0)
		}
		buckets[typeName] = append(buckets[typeName], map[string]interface{}{
			"hash": nullStr(hash), "raw_hex": nullStr(rawHex),
			"decoded_json": nullStr(decodedJSON), "observation_count": 1,
			"payload_type": pt, "path_json": nullStr(pathJSON),
			"observer_id": nullStr(obsID), "timestamp": nullStr(ts),
		})
	}
	writeJSON(w, map[string]interface{}{"buckets": buckets})
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
