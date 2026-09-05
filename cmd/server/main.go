package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/dbschema"
)

// Set via -ldflags at build time
var Version string
var Commit string
var BuildTime string

func resolveCommit() string {
	if Commit != "" {
		return Commit
	}
	// Try .git-commit file (baked by Docker / CI)
	if data, err := os.ReadFile(".git-commit"); err == nil {
		if c := strings.TrimSpace(string(data)); c != "" && c != "unknown" {
			return c
		}
	}
	// Try git rev-parse at runtime
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return "unknown"
}

func resolveVersion() string {
	// Issue #1807: the release fast-path re-tags :edge -> :vX.Y.Z without a
	// rebuild, so the ldflags-baked Version still reports "edge" on tagged
	// releases. Resolution order: operator env override, then the
	// .image-version file appended by the fast-path retag (mirrors the
	// .git-commit pattern in resolveCommit), then the baked Version.
	if v := strings.TrimSpace(os.Getenv("CORESCOPE_VERSION")); v != "" {
		return v
	}
	if data, err := os.ReadFile(".image-version"); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" && v != "unknown" {
			return v
		}
	}
	if Version != "" {
		return Version
	}
	return "unknown"
}

func resolveBuildTime() string {
	if BuildTime != "" {
		return BuildTime
	}
	return "unknown"
}

func main() {
	// pprof profiling — off by default, enable with ENABLE_PPROF=true
	if os.Getenv("ENABLE_PPROF") == "true" {
		pprofPort := os.Getenv("PPROF_PORT")
		if pprofPort == "" {
			pprofPort = "6060"
		}
		go func() {
			log.Printf("[pprof] profiling UI at http://localhost:%s/debug/pprof/", pprofPort)
			if err := http.ListenAndServe(":"+pprofPort, nil); err != nil {
				log.Printf("[pprof] failed to start: %v (non-fatal)", err)
			}
		}()
	}

	var (
		configDir string
		port      int
		dbPath    string
		publicDir string
		pollMs    int
	)

	flag.StringVar(&configDir, "config-dir", ".", "Directory containing config.json")
	flag.IntVar(&port, "port", 0, "HTTP port (overrides config)")
	flag.StringVar(&dbPath, "db", "", "SQLite database path (overrides config/env)")
	flag.StringVar(&publicDir, "public", "public", "Directory to serve static files from")
	flag.IntVar(&pollMs, "poll-ms", 1000, "SQLite poll interval for WebSocket broadcast (ms)")
	flag.Parse()

	// Load config
	cfg, err := LoadConfig(configDir)
	if err != nil {
		log.Printf("[config] warning: %v (using defaults)", err)
	}

	// CLI flags override config
	if port > 0 {
		cfg.Port = port
	}
	if cfg.Port == 0 {
		cfg.Port = 3000
	}
	if dbPath != "" {
		cfg.DBPath = dbPath
	}
	if cfg.APIKey == "" {
		log.Printf("[security] WARNING: no apiKey configured — write endpoints are BLOCKED (set apiKey in config.json to enable them)")
	} else if IsWeakAPIKey(cfg.APIKey) {
		log.Printf("[security] WARNING: API key is weak or a known default — write endpoints are vulnerable")
	}

	// Apply Go runtime soft memory limit (#836, #1010).
	// Precedence: GOMEMLIMIT env > runtime.maxMemoryMB > derived from packetStore.maxMemoryMB.
	{
		_, envSet := os.LookupEnv("GOMEMLIMIT")
		runtimeMaxMB := 0
		if cfg.Runtime != nil {
			runtimeMaxMB = cfg.Runtime.MaxMemoryMB
		}
		maxMB := 0
		if cfg.PacketStore != nil {
			maxMB = cfg.PacketStore.MaxMemoryMB
		}
		// runtime.maxMemoryMB (explicit) wins over packetStore-derived (implicit).
		effectiveMB := maxMB
		usedRuntimeCfg := false
		if !envSet && runtimeMaxMB > 0 {
			effectiveMB = runtimeMaxMB
			usedRuntimeCfg = true
		}
		limit, source := applyMemoryLimit(effectiveMB, envSet)
		switch source {
		case "env":
			log.Printf("[memlimit] using GOMEMLIMIT from environment (%s)", os.Getenv("GOMEMLIMIT"))
		case "derived":
			if usedRuntimeCfg {
				log.Printf("[memlimit] runtime.maxMemoryMB=%d → %d MiB (1.5x headroom)", runtimeMaxMB, limit/(1024*1024))
			} else {
				log.Printf("[memlimit] derived from packetStore.maxMemoryMB=%d → %d MiB (1.5x headroom)", maxMB, limit/(1024*1024))
			}
		default:
			log.Printf("[memlimit] unset → default (no soft memory limit; recommend setting GOMEMLIMIT or runtime.maxMemoryMB to ≥1.5× working set to avoid OOM-kill)")
		}
		warnIfMemlimitUnderprovisioned(limit)
	}

	// Resolve DB path
	resolvedDB := cfg.ResolveDBPath(configDir)
	log.Printf("[config] port=%d db=%s public=%s", cfg.Port, resolvedDB, publicDir)
	if len(cfg.NodeBlacklist) > 0 {
		log.Printf("[config] nodeBlacklist: %d node(s) will be hidden from API", len(cfg.NodeBlacklist))
		for _, pk := range cfg.NodeBlacklist {
			if trimmed := strings.ToLower(strings.TrimSpace(pk)); trimmed != "" {
				log.Printf("[config]   blacklisted: %s", trimmed)
			}
		}
	}

	// Open database
	database, err := OpenDB(resolvedDB)
	if err != nil {
		log.Fatalf("[db] failed to open %s: %v", resolvedDB, err)
	}
	var dbCloseOnce sync.Once
	dbClose := func() error {
		var err error
		dbCloseOnce.Do(func() { err = database.Close() })
		return err
	}
	defer dbClose()

	// Verify DB has expected tables
	var tableName string
	err = database.conn.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='transmissions'").Scan(&tableName)
	if err == sql.ErrNoRows {
		log.Fatalf("[db] table 'transmissions' not found — is this a CoreScope database?")
	}

	stats, err := database.GetStats()
	if err != nil {
		log.Printf("[db] warning: could not read stats: %v", err)
	} else {
		log.Printf("[db] transmissions=%d observations=%d nodes=%d observers=%d",
			stats.TotalTransmissions, stats.TotalObservations, stats.TotalNodes, stats.TotalObservers)
	}

	// auto_vacuum is checked + migrated by the ingestor (#1283). The
	// server is read-only and must not race the writer for the lock.

	// Assert all schema migrations the ingestor owns have already run
	// (#1287). The server NEVER migrates — it only reads. If a required
	// column/index/table is missing, the operator must restart the
	// ingestor (which owns dbschema.Apply) before this server can start.
	if err := dbschema.AssertReady(database.conn); err != nil {
		log.Fatalf("[db] schema not ready (ingestor must run migrations first): %v", err)
	}

	// In-memory packet store
	store := NewPacketStore(database, cfg.PacketStore, cfg.CacheTTL)
	store.config = cfg

	// Load the persisted neighbor graph BEFORE the packet load so the
	// chunked loader can resolve relay-hop pubkeys from path_json. Since
	// #1287 the ingestor persists relay data only as aggregate
	// neighbor_edges — observations.resolved_path is never written — so
	// without an available graph at load time a relay node's analytics
	// history would rebuild only from post-restart live traffic (the
	// "timeline empty after every restart" bug). neighbor_edges is small,
	// so this adds negligible latency before the HTTP listener binds. The
	// fresh-DB branch (no snapshot) still builds in-memory AFTER the load
	// below, because BuildFromStore needs the loaded packets.
	neighborEdgesPersisted := neighborEdgesTableExists(database.conn)
	if neighborEdgesPersisted {
		store.graph.Store(loadNeighborEdgesFromDB(database.conn))
		log.Printf("[neighbor] loaded persisted neighbor graph")
	}

	// #1009: chunked Load with early HTTP readiness. RunStartupLoad runs
	// asynchronously and signals FirstChunkReady after the first chunk
	// is merged so the HTTP listener can bind without waiting for the
	// full multi-minute scan to finish. loadStatusMiddleware (wired
	// below) advertises loading|ready via X-CoreScope-Load-Status.
	//
	// #1809: the background fill loader (loadBackgroundChunks) used to
	// be spawned here at FirstChunkReady, but at that point LoadChunked
	// has not yet set s.oldestLoaded → bg loader read "" and bailed →
	// coverage gate trips. RunStartupLoad now gates the bg loader on
	// LoadChunked completion, preserving FirstChunkReady's parallelism
	// for the HTTP listener bind.
	chunkSize := cfg.DBLoadChunkSize()
	// loadErrCh is buffered(1) and is the SOLE consumer slot for the
	// RunStartupLoad goroutine's terminal error. We have exactly ONE
	// reader path below: either the select consumes the error (fast
	// empty-DB path) OR we hand it off to a single late-reader goroutine
	// (slow path: bg loader still running after FirstChunkReady fired).
	// The pre-#1811 code spawned a late-reader unconditionally, which
	// leaked a goroutine forever after the select had already drained
	// the channel. See PR #1811 adv #1.
	loadErrCh := make(chan error, 1)
	go func() {
		loadErrCh <- store.RunStartupLoad(chunkSize)
	}()
	firstChunkPath := false
	select {
	case <-store.FirstChunkReady():
		firstChunkPath = true
		log.Printf("[store] first chunk ready (chunkSize=%d) — HTTP listener may bind", chunkSize)
	case err := <-loadErrCh:
		if err != nil {
			log.Fatalf("[store] RunStartupLoad failed before first chunk: %v", err)
		}
		log.Printf("[store] RunStartupLoad completed before first-chunk signal (empty DB?)")
	}
	if store.hotStartupHours > 0 {
		log.Printf("[store] hot-startup window configured: hotStartupHours=%gh, retentionHours=%gh (background fill loader runs after LoadChunked succeeds — see RunStartupLoad)",
			store.hotStartupHours, store.retentionHours)
	} else {
		log.Printf("[store] hot-startup disabled (hotStartupHours=0) — no background fill loader will run")
	}
	if firstChunkPath {
		// Only spawn the late-reader if the select did NOT already drain
		// loadErrCh. Otherwise this goroutine would block forever on an
		// empty channel (goroutine leak — adv #1).
		go func() {
			if err := <-loadErrCh; err != nil {
				log.Printf("[store] RunStartupLoad background error: %v", err)
			}
		}()
	}

	// Neighbor graph: the persisted snapshot (if present) was already
	// loaded above, before the packet load. Per #1287 schema migrations
	// all live in the ingestor; the server only reads the snapshot and
	// then refreshes it via the recompNeighborGraph slot every 60s.
	dbPath = database.path
	database.hasResolvedPath = true // dbschema.AssertReady above already verified observations.resolved_path exists

	// WaitGroup for background init steps that gate /api/healthz readiness.
	var initWg sync.WaitGroup

	if !neighborEdgesPersisted {
		// No persisted snapshot yet (e.g. fresh DB before the ingestor
		// has run its first edge-build cycle). Build an in-memory graph
		// from the packets we already have so reads aren't empty. We
		// do NOT persist — the ingestor owns neighbor_edges writes per
		// #1287; the recompNeighborGraph recomputer will pick up the
		// real snapshot as soon as the ingestor populates it.
		log.Printf("[neighbor] no persisted edges found, will build in-memory in background...")
		store.graph.Store(NewNeighborGraph())
		initWg.Add(1)
		go func() {
			defer initWg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[neighbor] graph build panic recovered: %v", r)
				}
			}()
			built := BuildFromStore(store)
			store.graph.Store(built)
			log.Printf("[neighbor] in-memory graph build complete")
		}()
	}

	// Initial pickBestObservation runs in background — doesn't need to block HTTP.
	// API serves best-effort data until this completes (~10s for 100K txs).
	// Processes in chunks of 5000, releasing the lock between chunks so API
	// handlers remain responsive.
	initWg.Add(1)
	go func() {
		defer initWg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[store] pickBestObservation panic recovered: %v", r)
			}
		}()
		const chunkSize = 5000
		store.mu.RLock()
		totalPackets := len(store.packets)
		store.mu.RUnlock()

		for i := 0; i < totalPackets; i += chunkSize {
			end := i + chunkSize
			if end > totalPackets {
				end = totalPackets
			}
			store.mu.Lock()
			for j := i; j < end && j < len(store.packets); j++ {
				pickBestObservation(store.packets[j])
			}
			store.mu.Unlock()
			if end < totalPackets {
				time.Sleep(10 * time.Millisecond) // yield to API handlers
			}
		}
		log.Printf("[store] initial pickBestObservation complete (%d transmissions)", totalPackets)
	}()

	// Mark server ready once all background init completes.
	go func() {
		initWg.Wait()
		readiness.Store(1)
		log.Printf("[server] readiness: ready=true (background init complete)")
	}()

	// WebSocket hub
	hub := NewHub()
	hub.SetAllowedOrigins(cfg.CORSAllowedOrigins)
	hub.ConfigureLimits(cfg.WSMaxConnsPerIP(), cfg.WSUpgradesPerMinPerIP(),
		cfg.WSTrustedProxies(), cfg.WSDeny()) // #1794
	hub.upgrader.EnableCompression = cfg.WSCompressionEnabled()

	// HTTP server
	srv := NewServer(database, cfg, hub)
	srv.configDir = configDir
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	// WebSocket endpoint
	router.HandleFunc("/ws", hub.ServeWS)

	// Static files + SPA fallback
	absPublic, _ := filepath.Abs(publicDir)
	if _, err := os.Stat(absPublic); err == nil {
		fs := http.FileServer(http.Dir(absPublic))
		router.PathPrefix("/").Handler(wsOrStatic(hub, spaHandler(absPublic, fs)))
		log.Printf("[static] serving %s", absPublic)
	} else {
		log.Printf("[static] directory %s not found — API-only mode", absPublic)
		router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<!DOCTYPE html><html><body><h1>CoreScope</h1><p>Frontend not found. API available at /api/</p></body></html>`))
		})
	}

	// Start SQLite poller for WebSocket broadcast
	poller := NewPoller(database, hub, time.Duration(pollMs)*time.Millisecond)
	poller.store = store
	go poller.Start()

	// Start periodic eviction
	stopEviction := store.StartEvictionTicker()
	defer stopEviction()

	// Steady-state analytics recomputers (issue #1240). Replaces the
	// on-request compute-then-cache pattern for the default (region="",
	// zero-window) analytics queries with a background refresh loop so
	// reads always hit cache in <1ms.
	stopAnalyticsRecomp := store.StartAnalyticsRecomputers(
		cfg.AnalyticsDefaultRecomputeInterval(),
		cfg.AnalyticsRecomputeIntervals(),
	)
	defer stopAnalyticsRecomp()
	log.Printf("[analytics-recompute] background recompute enabled (default=%s)", cfg.AnalyticsDefaultRecomputeInterval())

	// #1481 P0-1: background recomputer for the default-shape
	// /api/analytics/neighbor-graph response (5 min cadence). Reads
	// hit an atomic pointer; the rebuild path no longer runs on the
	// request goroutine for the common filter shape.
	stopNeighborGraphCache := make(chan struct{})
	ngInterval := neighborGraphCacheInterval
	if cfg.NeighborGraph != nil && cfg.NeighborGraph.CacheRecomputeIntervalSeconds > 0 {
		ngInterval = time.Duration(cfg.NeighborGraph.CacheRecomputeIntervalSeconds) * time.Second
	}
	srv.startNeighborGraphRecomputer(ngInterval, stopNeighborGraphCache)
	defer close(stopNeighborGraphCache)
	log.Printf("[neighbor-graph-cache] background recompute enabled (interval=%s)", ngInterval)

	// Known-channels catalogue cache (issue #1323). OPT-IN: an empty
	// cfg.KnownChannelsURL leaves srv.knownChannels nil and starts no
	// background fetch. The /api/known-channels endpoint then serves an
	// empty snapshot. Operators who want the community catalogue must
	// set knownChannelsUrl explicitly in config.json (see
	// config.example.json for the pinned-SHA recommendation).
	if cfg.KnownChannelsURL != "" {
		kcRefresh := DefaultKnownChannelsRefresh
		if cfg.KnownChannelsRefreshMs > 0 {
			kcRefresh = time.Duration(cfg.KnownChannelsRefreshMs) * time.Millisecond
		}
		srv.knownChannels = newKnownChannelsCache(cfg.KnownChannelsURL, kcRefresh)
		kcCtx, stopKnownChannels := context.WithCancel(context.Background())
		srv.knownChannels.run(kcCtx)
		defer stopKnownChannels()
		log.Printf("[known-channels] background fetch enabled (url=%s, refresh=%s)", cfg.KnownChannelsURL, kcRefresh)
	} else {
		log.Printf("[known-channels] disabled (knownChannelsUrl unset in config)")
	}

	// Steady-state repeater-enrichment recomputer (issue #1262).
	// Prewarms the bulk caches feeding handleNodes so the very first
	// /api/nodes?limit=2000 from live.js's SPA bootstrap hits a
	// populated cache instead of paying a 15.7s on-thread rebuild.
	// Uses the configured RelayActiveHours window and the same
	// default recompute interval as the other analytics caches.
	relayWindowHours := cfg.GetHealthThresholds().RelayActiveHours
	stopRepeaterEnrichRecomp := store.StartRepeaterEnrichmentRecomputer(
		relayWindowHours,
		cfg.AnalyticsDefaultRecomputeInterval(),
	)
	defer stopRepeaterEnrichRecomp()
	log.Printf("[repeater-enrich-recompute] background recompute enabled (window=%.1fh, interval=%s)",
		relayWindowHours, cfg.AnalyticsDefaultRecomputeInterval())

	// Steady-state bridge-centrality recomputer (issue #672 axis 2).
	// Computes betweenness centrality over the in-memory neighbor
	// graph and stores the per-pubkey score map atomically. Read by
	// handleNodes via a single atomic load.
	stopBridgeRecomp := store.StartBridgeScoreRecomputer(
		cfg.AnalyticsDefaultRecomputeInterval(),
	)
	defer stopBridgeRecomp()
	log.Printf("[bridge-recompute] background recompute enabled (interval=%s)",
		cfg.AnalyticsDefaultRecomputeInterval())

	// Steady-state coverage + redundancy recomputer (issue #672 axes 3 & 4).
	// Computes harmonic-reach coverage and articulation-point redundancy over
	// the same neighbor graph as the bridge axis, storing both per-pubkey
	// maps atomically. Read by handleNodes via single atomic loads.
	stopUsefulnessAxesRecomp := store.StartUsefulnessAxesRecomputer(
		cfg.AnalyticsDefaultRecomputeInterval(),
	)
	defer stopUsefulnessAxesRecomp()
	log.Printf("[usefulness-axes-recompute] background recompute enabled (interval=%s)",
		cfg.AnalyticsDefaultRecomputeInterval())

	// Steady-state neighbor-graph snapshot recomputer (issue #1287).
	// Per Option 4: the ingestor owns neighbor_edges; the server
	// READS the snapshot every 60s and atomic-swaps it into s.graph.
	// This is the ONLY path that updates s.graph at steady state.
	stopNeighborRecomp := store.StartNeighborGraphRecomputer(NeighborGraphRecomputerDefaultInterval)
	defer stopNeighborRecomp()
	log.Printf("[neighbor-recompute] snapshot reload enabled (interval=%s)",
		NeighborGraphRecomputerDefaultInterval)

	// Packet / metrics / observer retention moved to the ingestor in
	// #1283 (writes only belong on the writer process). Neighbor-edge
	// pruning moved to the ingestor in #1287 for the same reason. The
	// server no longer schedules any of these; the ingestor's tickers
	// handle them.
	_ = cfg.IncrementalVacuumPages() // kept reachable for config validation; not used here
	_ = cfg.NeighborMaxAgeDays()     // ditto — owned by ingestor now

	// Graceful shutdown
	var handler http.Handler = router
	if cfg.GZipEnabled() {
		handler = gzipMiddlewareWithConfig(cfg.Compression, router)
		log.Printf("[server] HTTP gzip compression enabled")
	}
	// #1009: stamp X-CoreScope-Load-Status on every response so probes
	// and dashboards can see when the chunked Load is still in flight.
	// Outermost wrap so the header is set regardless of gzip/etc.
	handler = loadStatusMiddleware(store, handler)
	if cfg.WSCompressionEnabled() {
		log.Printf("[server] WebSocket permessage-deflate compression enabled")
	}
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("[server] received %v, shutting down...", sig)

		// 1. Stop accepting new WebSocket/poll data
		poller.Stop()

		// 1b. Auto-prune tickers were all relocated to the ingestor in
		// #1283/#1287 — nothing to stop here.

		// 1c. Stop steady-state analytics recomputers (issue #1240).
		// Must happen before dbClose so any in-flight compute that
		// reaches into SQLite has finished.
		if stopAnalyticsRecomp != nil {
			stopAnalyticsRecomp()
		}

		// 2. Gracefully drain HTTP connections (up to 15s)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("[server] HTTP shutdown error: %v", err)
		}

		// 3. Close WebSocket hub
		hub.Close()

		// 4. Close database (release SQLite WAL lock)
		if err := dbClose(); err != nil {
			log.Printf("[server] DB close error: %v", err)
		}
		log.Println("[server] shutdown complete")
	}()

	log.Printf("[server] CoreScope (Go) listening on http://localhost:%d", cfg.Port)

	// Backfills (resolved_path, from_pubkey) moved to the ingestor in
	// #1287 — they are write operations and belong on the writer
	// process. The server reads the results via the periodic
	// recompNeighborGraph / fetchResolvedPathForObs paths.

	// Migrate old content hashes in background (one-time, idempotent).
	go migrateContentHashesAsync(store, 5000, 100*time.Millisecond)

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("[server] %v", err)
	}
}

// spaHandler serves static files, falling back to index.html for SPA routes.
// It reads index.html once at creation time and replaces the __BUST__ placeholder
// with a Unix timestamp so browsers fetch fresh JS/CSS after each server restart.
func spaHandler(root string, fs http.Handler) http.Handler {
	// Pre-process index.html: replace __BUST__ with a cache-bust timestamp
	indexPath := filepath.Join(root, "index.html")
	rawHTML, err := os.ReadFile(indexPath)
	if err != nil {
		log.Printf("[static] warning: could not read index.html for cache-bust: %v", err)
		rawHTML = []byte("<!DOCTYPE html><html><body><h1>CoreScope</h1><p>index.html not found</p></body></html>")
	}
	bustValue := fmt.Sprintf("%d", time.Now().Unix())
	indexHTML := []byte(strings.ReplaceAll(string(rawHTML), "__BUST__", bustValue))
	log.Printf("[static] cache-bust value: %s", bustValue)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Defense-in-depth: explicitly reject path-traversal attempts before
		// we touch the filesystem. gorilla/mux + http.FileServer already clean
		// most of these, but we don't want a future SkipClean(true) (or a
		// different router) to silently expose the FS. See
		// audit-input-vulns-20260603 (LOW — SPA static handler depends on
		// default mux path-cleaning).
		if !isSafeStaticPath(r.URL.Path, r.URL.RawPath) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Serve pre-processed index.html for root and /index.html
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Write(indexHTML)
			return
		}

		path := filepath.Join(root, r.URL.Path)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// SPA fallback — serve pre-processed index.html
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Write(indexHTML)
			return
		}
		// Disable caching for JS/CSS/HTML
		if filepath.Ext(path) == ".js" || filepath.Ext(path) == ".css" || filepath.Ext(path) == ".html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}
		fs.ServeHTTP(w, r)
	})
}

// isSafeStaticPath rejects request paths that contain traversal sequences
// or backslashes — defense-in-depth for the SPA static handler so a future
// router with SkipClean(true) cannot expose the filesystem. Empty input is
// safe (root handled earlier).
//
// urlPath is the decoded path (r.URL.Path); rawPath is the raw, possibly
// percent-encoded path (r.URL.RawPath) used to catch encoded `..` / `\`.
func isSafeStaticPath(urlPath, rawPath string) bool {
	for _, p := range []string{urlPath, rawPath} {
		if p == "" {
			continue
		}
		// Lowercase for case-insensitive percent-encoding checks.
		lp := strings.ToLower(p)
		// Block "..", any URL-encoded "%2e%2e" sequence, and backslashes
		// (which Windows-style traversal exploits convert to "\").
		if strings.Contains(p, "..") ||
			strings.Contains(lp, "%2e%2e") ||
			strings.Contains(p, "\\") ||
			strings.Contains(lp, "%5c") {
			return false
		}
	}
	return true
}
