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
		configDir  string
		port       int
		dbPath     string
		publicDir  string
		pollMs     int
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

	// In-memory packet store
	store := NewPacketStore(database, cfg.PacketStore, cfg.CacheTTL)
	if err := store.Load(); err != nil {
		log.Fatalf("[store] failed to load: %v", err)
	}

	// Initialize persisted neighbor graph
	dbPath = database.path
	if err := ensureNeighborEdgesTable(dbPath); err != nil {
		log.Printf("[neighbor] warning: could not create neighbor_edges table: %v", err)
	}
	// Add resolved_path column if missing.
	// NOTE on startup ordering (review item #10): ensureResolvedPathColumn runs AFTER
	// OpenDB/detectSchema, so db.hasResolvedPath will be false on first run with a
	// pre-existing DB. This means Load() won't SELECT resolved_path from SQLite.
	// Async backfill runs after HTTP starts (see backfillResolvedPathsAsync below)
	// AND to SQLite. On next restart, detectSchema finds the column and Load() reads it.
	if err := ensureResolvedPathColumn(dbPath); err != nil {
		log.Printf("[store] warning: could not add resolved_path column: %v", err)
	} else {
		database.hasResolvedPath = true // detectSchema ran before column was added; fix the flag
	}

	// Load or build neighbor graph
	if neighborEdgesTableExists(database.conn) {
		store.graph = loadNeighborEdgesFromDB(database.conn)
		log.Printf("[neighbor] loaded persisted neighbor graph")
	} else {
		log.Printf("[neighbor] no persisted edges found, will build in background...")
		store.graph = NewNeighborGraph() // empty graph — gets populated by background goroutine
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[neighbor] graph build panic recovered: %v", r)
				}
			}()
			rw, rwErr := openRW(dbPath)
			if rwErr == nil {
				edgeCount := buildAndPersistEdges(store, rw)
				rw.Close()
				log.Printf("[neighbor] persisted %d edges", edgeCount)
			}
			built := BuildFromStore(store)
			store.mu.Lock()
			store.graph = built
			store.mu.Unlock()
			log.Printf("[neighbor] graph build complete")
		}()
	}

	// Initial pickBestObservation runs in background — doesn't need to block HTTP.
	// API serves best-effort data until this completes (~10s for 100K txs).
	// Processes in chunks of 5000, releasing the lock between chunks so API
	// handlers remain responsive.
	go func() {
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

	// WebSocket hub
	hub := NewHub()

	// HTTP server
	srv := NewServer(database, cfg, hub)
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

	// Auto-prune old packets if retention.packetDays is configured
	var stopPrune func()
	if cfg.Retention != nil && cfg.Retention.PacketDays > 0 {
		days := cfg.Retention.PacketDays
		pruneTicker := time.NewTicker(24 * time.Hour)
		pruneDone := make(chan struct{})
		stopPrune = func() {
			pruneTicker.Stop()
			close(pruneDone)
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[prune] panic recovered: %v", r)
				}
			}()
			time.Sleep(1 * time.Minute)
			if n, err := database.PruneOldPackets(days); err != nil {
				log.Printf("[prune] error: %v", err)
			} else {
				log.Printf("[prune] deleted %d transmissions older than %d days", n, days)
			}
			for {
				select {
				case <-pruneTicker.C:
					if n, err := database.PruneOldPackets(days); err != nil {
						log.Printf("[prune] error: %v", err)
					} else {
						log.Printf("[prune] deleted %d transmissions older than %d days", n, days)
					}
				case <-pruneDone:
					return
				}
			}
		}()
		log.Printf("[prune] auto-prune enabled: packets older than %d days will be removed daily", days)
	}

	// Auto-prune old metrics
	var stopMetricsPrune func()
	{
		metricsDays := cfg.MetricsRetentionDays()
		metricsPruneTicker := time.NewTicker(24 * time.Hour)
		metricsPruneDone := make(chan struct{})
		stopMetricsPrune = func() {
			metricsPruneTicker.Stop()
			close(metricsPruneDone)
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[metrics-prune] panic recovered: %v", r)
				}
			}()
			time.Sleep(2 * time.Minute) // stagger after packet prune
			database.PruneOldMetrics(metricsDays)
			for {
				select {
				case <-metricsPruneTicker.C:
					database.PruneOldMetrics(metricsDays)
				case <-metricsPruneDone:
					return
				}
			}
		}()
		log.Printf("[metrics-prune] auto-prune enabled: metrics older than %d days", metricsDays)
	}

	// Auto-prune stale observers
	var stopObserverPrune func()
	{
		observerDays := cfg.ObserverDaysOrDefault()
		if observerDays <= -1 {
			// -1 means keep forever, skip
		} else {
			observerPruneTicker := time.NewTicker(24 * time.Hour)
			observerPruneDone := make(chan struct{})
			stopObserverPrune = func() {
				observerPruneTicker.Stop()
				close(observerPruneDone)
			}
			go func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[observer-prune] panic recovered: %v", r)
					}
				}()
				time.Sleep(3 * time.Minute) // stagger after metrics prune
				database.RemoveStaleObservers(observerDays)
				for {
					select {
					case <-observerPruneTicker.C:
						database.RemoveStaleObservers(observerDays)
					case <-observerPruneDone:
						return
					}
				}
			}()
			log.Printf("[observer-prune] auto-prune enabled: observers not seen in %d days will be removed", observerDays)
		}
	}

	// Auto-prune old neighbor edges
	var stopEdgePrune func()
	{
		maxAgeDays := cfg.NeighborMaxAgeDays()
		edgePruneTicker := time.NewTicker(24 * time.Hour)
		edgePruneDone := make(chan struct{})
		stopEdgePrune = func() {
			edgePruneTicker.Stop()
			close(edgePruneDone)
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[neighbor-prune] panic recovered: %v", r)
				}
			}()
			time.Sleep(4 * time.Minute) // stagger after metrics prune
			store.mu.RLock()
			g := store.graph
			store.mu.RUnlock()
			PruneNeighborEdges(dbPath, g, maxAgeDays)
			for {
				select {
				case <-edgePruneTicker.C:
					store.mu.RLock()
					g := store.graph
					store.mu.RUnlock()
					PruneNeighborEdges(dbPath, g, maxAgeDays)
				case <-edgePruneDone:
					return
				}
			}
		}()
		log.Printf("[neighbor-prune] auto-prune enabled: edges older than %d days", maxAgeDays)
	}

	// Graceful shutdown
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
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

		// 1b. Stop auto-prune ticker
		if stopPrune != nil {
			stopPrune()
		}
		if stopMetricsPrune != nil {
			stopMetricsPrune()
		}
		if stopObserverPrune != nil {
			stopObserverPrune()
		}
		if stopEdgePrune != nil {
			stopEdgePrune()
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

	// Start async backfill in background — HTTP is now available.
	go backfillResolvedPathsAsync(store, dbPath, 5000, 100*time.Millisecond, cfg.BackfillHours())

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
