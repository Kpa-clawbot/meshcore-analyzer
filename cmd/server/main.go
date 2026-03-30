package main

import (
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
		log.Printf("[security] WARNING: no apiKey configured — write endpoints are unprotected")
	}

	// Resolve DB path
	resolvedDB := cfg.ResolveDBPath(configDir)
	log.Printf("[config] port=%d db=%s public=%s", cfg.Port, resolvedDB, publicDir)

	// Open database
	database, err := OpenDB(resolvedDB)
	if err != nil {
		log.Fatalf("[db] failed to open %s: %v", resolvedDB, err)
	}
	defer database.Close()

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
	store := NewPacketStore(database, cfg.PacketStore)
	if err := store.Load(); err != nil {
		log.Fatalf("[store] failed to load: %v", err)
	}

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
		<-sigCh
		log.Println("[server] shutting down...")
		poller.Stop()
		httpServer.Close()
	}()

	log.Printf("[server] CoreScope (Go) listening on http://localhost:%d", cfg.Port)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("[server] %v", err)
	}
}

// spaHandler serves static files, falling back to index.html for SPA routes.
func spaHandler(root string, fs http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(root, r.URL.Path)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			http.ServeFile(w, r, filepath.Join(root, "index.html"))
			return
		}
		// Disable caching for JS/CSS/HTML
		if filepath.Ext(path) == ".js" || filepath.Ext(path) == ".css" || filepath.Ext(path) == ".html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}
		fs.ServeHTTP(w, r)
	})
}
