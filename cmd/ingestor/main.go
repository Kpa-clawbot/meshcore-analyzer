package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	// pprof profiling — off by default, enable with ENABLE_PPROF=true
	if os.Getenv("ENABLE_PPROF") == "true" {
		pprofPort := os.Getenv("PPROF_PORT")
		if pprofPort == "" {
			pprofPort = "6061"
		}
		go func() {
			log.Printf("[pprof] ingestor profiling at http://localhost:%s/debug/pprof/", pprofPort)
			if err := http.ListenAndServe(":"+pprofPort, nil); err != nil {
				log.Printf("[pprof] failed to start: %v (non-fatal)", err)
			}
		}()
	}

	configPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[ingestor] ")

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Apply Go runtime soft memory limit (GOMEMLIMIT). See #1010.
	// Precedence: GOMEMLIMIT env > runtime.maxMemoryMB > unset (default).
	{
		_, envSet := os.LookupEnv("GOMEMLIMIT")
		runtimeMaxMB := 0
		if cfg.Runtime != nil {
			runtimeMaxMB = cfg.Runtime.MaxMemoryMB
		}
		limit, source := applyMemoryLimit(runtimeMaxMB, envSet)
		switch source {
		case "env":
			log.Printf("[memlimit] using GOMEMLIMIT from environment (%s)", os.Getenv("GOMEMLIMIT"))
		case "config":
			log.Printf("[memlimit] runtime.maxMemoryMB=%d → SetMemoryLimit(%d MiB)", runtimeMaxMB, limit/(1024*1024))
		default:
			log.Printf("[memlimit] unset → default (no soft memory limit; recommend setting GOMEMLIMIT or runtime.maxMemoryMB to ≥1.5× working set to avoid OOM-kill)")
		}
	}

	sources := cfg.ResolvedSources()

	store, err := OpenStoreWithInterval(cfg.DBPath, cfg.MetricsSampleInterval())
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()
	log.Printf("SQLite opened: %s", cfg.DBPath)

	// Async backfill: path_json from raw_hex (#888) — must not block MQTT startup
	store.BackfillPathJSONAsync()

	// Soft-delete blacklisted observers (#1287 — moved from cmd/server).
	if len(cfg.ObserverBlacklist) > 0 {
		store.SoftDeleteBlacklistedObservers(cfg.ObserverBlacklist)
	}

	// Async backfill: from_pubkey for legacy ADVERT rows (#1143).
	// Moved from cmd/server in #1287. Best-effort; must not block MQTT.
	go store.BackfillFromPubkey(5000, 100*time.Millisecond, nil)

	// Check auto_vacuum mode and optionally migrate (#919)
	store.CheckAutoVacuum(cfg)

	channelKeys := loadChannelKeys(cfg, *configPath)
	if len(channelKeys) > 0 {
		log.Printf("Loaded %d channel keys for GRP_TXT decryption", len(channelKeys))
	} else {
		log.Printf("No channel keys loaded — GRP_TXT packets will not be decrypted")
	}

	regionKeys := loadRegionKeys(cfg)
	store.BackfillDefaultScopeAsync(regionKeys)
	store.BackfillTransportCodesAsync()

	// Subscribe-early + buffer (#1608): the MQTT subscription is brought up
	// before startup maintenance so no packets are missed while the single
	// SQLite writer is blocked (e.g. a large CREATE INDEX migration). Received
	// messages are buffered here and drained once Ready() is called below.
	ingestBuffer := NewIngestBuffer(cfg.IngestBufferSizeOrDefault())
	ingestBuffer.Start()

	// Connect to each MQTT source
	var clients []mqtt.Client
	connectedCount := 0
	for _, source := range sources {
		tag := source.Name
		if tag == "" {
			tag = source.Broker
		}

		opts := buildMQTTOpts(source)
		connectTimeout := source.ConnectTimeoutOrDefault()
		log.Printf("MQTT [%s] connect timeout: %ds", tag, connectTimeout)

		// Pre-allocate the liveness pointer so OnConnect can reset its
		// stale-message clock on reconnect (PR #1216 r1 item 2). IsConnectedFn
		// is wired below once the client exists.
		liveness := &SourceLivenessState{
			Tag:    tag,
			Broker: source.Broker,
		}

		// #1043: per-source status registry. Idempotent — repeated
		// registration across reconnects returns the same state so
		// counters accumulate across the process lifetime.
		status := RegisterSourceStatus(tag, source.Broker)

		opts.SetOnConnectHandler(func(c mqtt.Client) {
			log.Printf("MQTT [%s] connected to %s", tag, source.Broker)
			status.MarkConnect(time.Now())
			// PR #1216 r1 item 2: clear the stale LastMessageUnix from
			// before the outage so the watchdog doesn't immediately scream
			// "stalled for 2h". Also restarts the cold-start grace window
			// and clears the alert cooldown so a fresh stall edge can fire.
			liveness.MarkReconnected(time.Now())
			topics := source.Topics
			if len(topics) == 0 {
				topics = []string{"meshcore/#"}
			}
			for _, t := range topics {
				token := c.Subscribe(t, 0, nil)
				token.Wait()
				if token.Error() != nil {
					log.Printf("MQTT [%s] subscribe error for %s: %v", tag, t, token.Error())
				} else {
					log.Printf("MQTT [%s] subscribed to %s", tag, t)
				}
			}
		})

		opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
			log.Printf("MQTT [%s] disconnected from %s: %v", tag, source.Broker, err)
			status.MarkDisconnect(time.Now(), err)
		})

		opts.SetReconnectingHandler(func(c mqtt.Client, options *mqtt.ClientOptions) {
			log.Printf("MQTT [%s] reconnecting to %s", tag, source.Broker)
		})

		// Capture source for closure
		src := source
		opts.SetDefaultPublishHandler(func(c mqtt.Client, m mqtt.Message) {
			// PR #1609 M1: stamp the RECEIPT clock here (broker liveness)
			// independently of the post-write clock that handleMessage
			// stamps. Without separation the watchdog/healthz could
			// report "fresh" while the writer was stalled and the
			// buffer was filling.
			markReceiptForTag(tag, time.Now())
			status.MarkPacket(time.Now())
			ingestBuffer.Submit(func() {
				handleMessage(store, tag, src, m, channelKeys, regionKeys, cfg)
			})
		})

		client := mqtt.NewClient(opts)
		// Wire IsConnectedFn now that the client exists, then register.
		// Registration BEFORE Connect so the attempt counter is available
		// to OnConnectAttempt on the very first dial.
		liveness.IsConnectedFn = client.IsConnected
		// #1335: wire force-reconnect so the watchdog can drop a
		// half-open TCP socket and re-dial when paho.IsConnected==true
		// but no messages have flowed past the stall threshold. Throttled
		// per source by the watchdog itself (forceReconnectThrottle).
		// Captured-by-value `client` is the same pointer used everywhere
		// else for this source. See buildForceReconnectFn for why this is
		// NOT simply "Disconnect(250) then Connect()".
		liveness.ForceReconnectFn = buildForceReconnectFn(client, tag)
		// PR #1216 r2 item 3: tag collisions used to log.Fatalf, which
		// killed the entire ingestor over one config typo and recreated
		// the #1212 total-ingest-stop class this PR exists to prevent.
		// registerLivenessOrSkip logs ERROR + skips liveness registration
		// for the duplicate; the MQTT source still attempts to connect,
		// it just isn't tracked by the watchdog. First registration
		// remains authoritative.
		registerLivenessOrSkip(liveness)
		token := client.Connect()
		// With ConnectRetry=true, token.Wait() blocks forever for unreachable brokers.
		// WaitTimeout lets startup proceed; the client keeps retrying in the background
		// and OnConnect fires (subscribing) when it eventually connects (#910).
		if !token.WaitTimeout(time.Duration(connectTimeout) * time.Second) {
			log.Printf("MQTT [%s] initial connection timed out — retrying in background", tag)
			clients = append(clients, client)
			continue
		}
		if token.Error() != nil {
			log.Printf("MQTT [%s] connection failed (non-fatal): %v", tag, token.Error())
			// BL1 fix: Disconnect to stop Paho's internal retry goroutines.
			// With ConnectRetry=true, Connect() spawns background goroutines
			// that leak if the client is simply discarded.
			client.Disconnect(0)
			continue
		}
		connectedCount++
		clients = append(clients, client)
	}

	// BL2 fix: require at least one immediately-connected source. Timed-out
	// clients are retrying in background (tracked in clients) but don't count
	// as "connected" — a single unreachable broker must not silently run with
	// zero active connections.
	if connectedCount == 0 {
		// Clean up any timed-out clients still retrying
		for _, c := range clients {
			c.Disconnect(0)
		}
		log.Fatal("no MQTT sources connected — all timed out or failed. Check broker is running (default: mqtt://localhost:1883). Set MQTT_BROKER env var or configure mqttSources in config.json")
	}

	if connectedCount < len(clients) {
		log.Printf("Running — %d MQTT source(s) connected, %d retrying in background", connectedCount, len(clients)-connectedCount)
	} else {
		log.Printf("Running — %d MQTT source(s) connected", connectedCount)
	}

	// Node retention: move stale nodes to inactive_nodes on startup
	nodeDays := cfg.NodeDaysOrDefault()
	store.MoveStaleNodes(nodeDays)

	// Observer retention: remove stale observers on startup
	observerDays := cfg.ObserverDaysOrDefault()
	store.RemoveStaleObservers(observerDays)

	// Observer purge: second stage, hard-deletes long-inactive observers whose
	// packets have already aged out. Always runs after the soft-delete so a row
	// crossing both thresholds is finalised in a single pass. 0 = disabled.
	observerPurgeDays := cfg.ObserverPurgeDaysOrZero()
	if _, err := store.PurgeStaleObservers(observerPurgeDays); err != nil {
		log.Printf("[prune] error: %v", err)
	}

	// Metrics retention: prune old metrics on startup
	metricsDays := cfg.MetricsRetentionDays()
	store.PruneOldMetrics(metricsDays)
	store.PruneDroppedPackets(metricsDays)

	// Packet (transmissions) retention: previously lived in cmd/server,
	// moved to ingestor in #1283 to eliminate cross-process write
	// contention (SQLITE_BUSY). 0 = disabled.
	packetDays := cfg.PacketDaysOrZero()
	if packetDays > 0 {
		if n, err := store.PruneOldPackets(packetDays); err != nil {
			log.Printf("[prune] error: %v", err)
		} else if n > 0 {
			log.Printf("[prune] startup pruned %d transmissions older than %d days", n, packetDays)
		}
	}

	// Client-RX coverage retention: bound the opt-in coverage tables (#1727).
	// Independent of the feature flag, so data persists are reaped even after
	// the feature is turned off. 0 = disabled.
	clientRxDays := cfg.ClientRxDaysOrZero()
	if clientRxDays > 0 {
		if n, err := store.PruneOldClientReceptions(clientRxDays); err != nil {
			log.Printf("[prune] error: %v", err)
		} else if n > 0 {
			log.Printf("[prune] startup pruned %d client_receptions older than %d days", n, clientRxDays)
		}
	}

	// Diagnostic client_rx_observations retention: separate, typically
	// shorter window than clientRxDays — this table is diagnostic, not
	// archival. 0 = disabled.
	clientRxObsDays := cfg.ClientRxObsDaysOrZero()
	if clientRxObsDays > 0 {
		if n, err := store.PruneOldClientRxObservations(clientRxObsDays); err != nil {
			log.Printf("[prune] client_rx_observations: %v", err)
		} else if n > 0 {
			log.Printf("[prune] startup pruned %d client_rx_observations older than %d days", n, clientRxObsDays)
		}
	}

	// Diagnostic client_rf_samples retention: separate window, independent
	// of clientRxDays/clientRxObsDays. 0 = disabled.
	clientRfDays := cfg.ClientRfDaysOrZero()
	if clientRfDays > 0 {
		if n, err := store.PruneOldClientRfSamples(clientRfDays); err != nil {
			log.Printf("[prune] client_rf_samples: %v", err)
		} else if n > 0 {
			log.Printf("[prune] startup pruned %d client_rf_samples older than %d days", n, clientRfDays)
		}
	}

	vacuumPages := cfg.IncrementalVacuumPages()
	store.RunIncrementalVacuum(vacuumPages)

	// Gate open: the synchronous startup writes above cannot return until the
	// single SQLite writer is free, which means any blocking async migration
	// (e.g. the CREATE INDEX) has finished. WaitForAsyncMigrations() makes that
	// explicit. Now drain everything the subscription buffered during startup.
	store.WaitForAsyncMigrations()
	ingestBuffer.Ready()
	if d := ingestBuffer.Dropped(); d > 0 {
		log.Printf("[ingest-buffer] write path ready; draining backlog (dropped %d during startup — consider raising ingestBufferSize)", d)
	} else {
		log.Printf("[ingest-buffer] write path ready; draining backlog (0 dropped)")
	}

	// Daily ticker for node retention
	retentionTicker := time.NewTicker(1 * time.Hour)
	go func() {
		for range retentionTicker.C {
			store.MoveStaleNodes(nodeDays)
			store.RunIncrementalVacuum(vacuumPages)
		}
	}()

	// Daily ticker for observer retention (every 24h, staggered 90s after startup)
	observerRetentionTicker := time.NewTicker(24 * time.Hour)
	go func() {
		time.Sleep(90 * time.Second) // stagger after metrics prune
		store.RemoveStaleObservers(observerDays)
		store.PurgeStaleObservers(observerPurgeDays)
		store.RunIncrementalVacuum(vacuumPages)
		for range observerRetentionTicker.C {
			store.RemoveStaleObservers(observerDays)
			store.PurgeStaleObservers(observerPurgeDays)
			store.RunIncrementalVacuum(vacuumPages)
		}
	}()

	// Daily ticker for metrics retention (every 24h)
	metricsRetentionTicker := time.NewTicker(24 * time.Hour)
	go func() {
		for range metricsRetentionTicker.C {
			store.PruneOldMetrics(metricsDays)
			store.PruneDroppedPackets(metricsDays)
			store.RunIncrementalVacuum(vacuumPages)
		}
	}()

	// Daily ticker for transmission retention (#1283).
	var packetRetentionTicker *time.Ticker
	if packetDays > 0 {
		packetRetentionTicker = time.NewTicker(24 * time.Hour)
		go func() {
			for range packetRetentionTicker.C {
				if n, err := store.PruneOldPackets(packetDays); err != nil {
					log.Printf("[prune] error: %v", err)
				} else if n > 0 {
					store.RunIncrementalVacuum(vacuumPages)
				}
			}
		}()
		log.Printf("[prune] auto-prune enabled: packets older than %d days will be removed daily", packetDays)
	}

	// Daily ticker for client-RX coverage retention (#1727), reused for the
	// diagnostic client_rx_observations and client_rf_samples retention
	// (Task 6) rather than starting a second ticker — the three flags are
	// independent (0 disables each separately), so the ticker itself must
	// run when any is set.
	if clientRxDays > 0 || clientRxObsDays > 0 || clientRfDays > 0 {
		clientRxRetentionTicker := time.NewTicker(24 * time.Hour)
		go func() {
			for range clientRxRetentionTicker.C {
				if clientRxDays > 0 {
					if n, err := store.PruneOldClientReceptions(clientRxDays); err != nil {
						log.Printf("[prune] error: %v", err)
					} else if n > 0 {
						store.RunIncrementalVacuum(vacuumPages)
					}
				}
				if clientRxObsDays > 0 {
					if n, err := store.PruneOldClientRxObservations(clientRxObsDays); err != nil {
						log.Printf("[prune] client_rx_observations: %v", err)
					} else if n > 0 {
						store.RunIncrementalVacuum(vacuumPages)
					}
				}
				if clientRfDays > 0 {
					if n, err := store.PruneOldClientRfSamples(clientRfDays); err != nil {
						log.Printf("[prune] client_rf_samples: %v", err)
					} else if n > 0 {
						store.RunIncrementalVacuum(vacuumPages)
					}
				}
			}
		}()
		if clientRxDays > 0 {
			log.Printf("[prune] auto-prune enabled: client_receptions older than %d days will be removed daily", clientRxDays)
		}
		if clientRxObsDays > 0 {
			log.Printf("[prune] auto-prune enabled: client_rx_observations older than %d days will be removed daily", clientRxObsDays)
		}
		if clientRfDays > 0 {
			log.Printf("[prune] auto-prune enabled: client_rf_samples older than %d days will be removed daily", clientRfDays)
		}
	}

	// Hourly WAL checkpoint to prevent unbounded WAL growth.
	// TRUNCATE resets the WAL file to zero bytes when all frames are flushed;
	// if the server's read connection holds frames, remaining pages stay in the
	// WAL until the next tick. Staggered 30s after startup to avoid competing
	// with the initial burst of ingest writes.
	walCheckpointTicker := time.NewTicker(1 * time.Hour)
	go func() {
		time.Sleep(30 * time.Second)
		store.Checkpoint()
		for range walCheckpointTicker.C {
			store.Checkpoint()
		}
	}()
	log.Printf("[db] WAL checkpoint scheduled every 1h")

	// Daily neighbor_edges retention (#1287 — moved from cmd/server).
	{
		nDays := cfg.NeighborEdgesDaysOrDefault()
		neighborPruneTicker := time.NewTicker(24 * time.Hour)
		go func() {
			time.Sleep(4 * time.Minute) // stagger
			if n, err := store.PruneNeighborEdges(nDays); err != nil {
				log.Printf("[neighbor-prune] error: %v", err)
			} else if n > 0 {
				log.Printf("[neighbor-prune] startup pruned %d edges older than %d days", n, nDays)
			}
			for range neighborPruneTicker.C {
				if n, err := store.PruneNeighborEdges(nDays); err != nil {
					log.Printf("[neighbor-prune] error: %v", err)
				} else if n > 0 {
					log.Printf("[neighbor-prune] pruned %d edges older than %d days", n, nDays)
				}
			}
		}()
		log.Printf("[neighbor-prune] auto-prune enabled: edges older than %d days", nDays)
	}

	// Periodic stats logging (every 5 minutes)
	statsTicker := time.NewTicker(5 * time.Minute)
	go func() {
		for range statsTicker.C {
			store.LogStats()
			if d := ingestBuffer.Dropped(); d > 0 || ingestBuffer.Pending() > 0 {
				log.Printf("[ingest-buffer] pending=%d dropped_total=%d", ingestBuffer.Pending(), d)
			}
		}
	}()

	// Prune-request queue (#669 M4 / #738): the read-only server enqueues
	// geo-prune requests as marker files; the ingestor (which holds the
	// write handle) executes the DELETEs. Process on startup, then every
	// 15 seconds — short enough for a one-click UX, long enough to avoid
	// useless wake-ups.
	store.RunPendingPruneRequests()
	pruneQueueTicker := time.NewTicker(15 * time.Second)
	go func() {
		for range pruneQueueTicker.C {
			store.RunPendingPruneRequests()
		}
	}()

	// Per-second stats file writer for the server's /api/perf/write-sources
	// endpoint (#1120). Best-effort; never fatal.
	StartStatsFileWriter(store, time.Second)

	// Multi-byte capability persister (#1324 follow-up): the server's
	// analytics cycle publishes a snapshot file via internal/mbcapqueue
	// (it cannot UPDATE itself, mode=ro since #1289). The ingestor
	// applies the snapshot here every 5 minutes — derived/cached
	// columns, ingestor owns the write.
	multibytePersistTicker := time.NewTicker(5 * time.Minute)
	go func() {
		time.Sleep(2 * time.Minute) // stagger after analytics warmup
		if _, err := store.RunMultibyteCapPersist(); err != nil {
			log.Printf("[multibyte-persist] error: %v", err)
		}
		for range multibytePersistTicker.C {
			if _, err := store.RunMultibyteCapPersist(); err != nil {
				log.Printf("[multibyte-persist] error: %v", err)
			}
		}
	}()
	log.Printf("[multibyte-persist] enabled (interval=5m)")

	// Neighbor-edges builder (#1287 — Option 4): ingestor owns
	// neighbor_edges writes. Runs every 60s. Server reads the snapshot
	// via cmd/server/neighbor_recomputer.go on the same cadence.
	// #1784: the neighbor builder is the first real consumer of the
	// path-trust threshold. Resolved once here so every tick shares the
	// same operator-configured value.
	neighborTrust := cfg.GetPathTrust()
	stopNeighborBuilder := store.StartNeighborEdgesBuilder(NeighborEdgesBuilderInterval, &neighborTrust)
	defer stopNeighborBuilder()
	log.Printf("[neighbor-build] enabled (interval=%s)", NeighborEdgesBuilderInterval)

	// #1212: per-source stall watchdog. Detects "silently dead" sources
	// where the client reports connected but no messages have flowed. Logs
	// a WARN line every minute for any source silent for >5m. Scan every
	// 60s so detection latency is bounded.
	stopWatchdog := runLivenessWatchdog(60*time.Second, 5*time.Minute)

	// Wait for shutdown signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("Shutting down...")
	retentionTicker.Stop()
	metricsRetentionTicker.Stop()
	if packetRetentionTicker != nil {
		packetRetentionTicker.Stop()
	}
	statsTicker.Stop()
	pruneQueueTicker.Stop()
	walCheckpointTicker.Stop()
	stopWatchdog()
	store.LogStats() // final stats on shutdown
	for _, c := range clients {
		c.Disconnect(5000) // 5s to allow in-flight messages to drain
	}
	log.Println("Done.")
}

// buildMQTTOpts creates MQTT client options for a source with bounded reconnect
// backoff, connect timeout, and TLS/auth configuration.
//
// Logs every TCP/TLS dial via OnConnectAttempt. Unlike SetReconnectingHandler
// (which only fires inside paho's reconnect goroutine and can be silent if
// that loop never iterates), OnConnectAttempt fires on every attempt — the
// initial Connect() and every reconnect. This is the observability fix for
// #1212 (prod outage on 2026-05-15 where the disconnect was logged but no
// reconnect activity was ever visible).
func buildMQTTOpts(source MQTTSource) *mqtt.ClientOptions {
	tag := source.Name
	if tag == "" {
		tag = source.Broker
	}
	opts := mqtt.NewClientOptions().
		AddBroker(source.Broker).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetOrderMatters(true).
		SetMaxReconnectInterval(30 * time.Second).
		SetConnectTimeout(10 * time.Second).
		SetWriteTimeout(10 * time.Second).
		// #1335: TCP-level keepalive surfaces a half-open socket within
		// ~30-60s instead of waiting for the application-level watchdog
		// (5m) to notice no messages. paho's MQTT PINGREQ uses this
		// interval too — if the broker's PINGRESP doesn't arrive,
		// ConnectionLost fires and auto-reconnect kicks in. Was unset
		// (paho default 30s actually — making this explicit so it can't
		// drift, and so operators reading the code know it's intentional
		// per the #1335 RCA).
		SetKeepAlive(30 * time.Second)

	opts.SetConnectionAttemptHandler(func(broker *url.URL, tlsCfg *tls.Config) *tls.Config {
		// Look up the per-source liveness state (registered in main) so we
		// can attach an attempt counter. If not yet registered (first dial
		// from Connect()), fall through with attempt=1.
		var attempt int64 = 1
		livenessRegistryMu.RLock()
		s := livenessRegistry[tag]
		livenessRegistryMu.RUnlock()
		if s != nil {
			attempt = atomic.AddInt64(&s.AttemptCount, 1)
		}
		log.Printf("MQTT [%s] connection attempt #%d to %s", tag, attempt, broker.String())
		return tlsCfg
	})

	if source.Username != "" {
		opts.SetUsername(source.Username)
	}
	if source.Password != "" {
		opts.SetPassword(source.Password)
	}
	if source.RejectUnauthorized != nil && !*source.RejectUnauthorized {
		opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: true})
	} else if strings.HasPrefix(source.Broker, "ssl://") || strings.HasPrefix(source.Broker, "wss://") {
		// TLS with system CA pool — valid for ssl:// MQTT brokers and
		// wss:// WebSocket brokers behind a publicly-trusted certificate.
		opts.SetTLSConfig(&tls.Config{})
	}
	return opts
}

// buildForceReconnectFn builds the watchdog's forced-reconnect action for a
// source (#1335, hardened against a race found while investigating a 100+
// minute reconnect failure).
//
// paho's own client.IsConnected() — used as liveness.IsConnectedFn — reports
// true not only when genuinely connected but for the ENTIRE time paho's
// background AutoReconnect/ConnectRetry loop is retrying (status
// reconnecting/connecting). So the watchdog's LivenessStalled classification
// (IsConnected==true, no messages) fires just as often for "paho is actively,
// correctly retrying a still-down broker" as it does for the true #1335
// half-open-TCP case. Naively doing Disconnect(250) then Connect() in the
// first case is actively harmful: paho's Disconnecting() must block until the
// CURRENT in-flight connection attempt plus its backoff sleep unwind (up to
// ConnectTimeout+MaxReconnectInterval, tens of seconds) before status
// actually reaches `disconnected`. Disconnect(250) returns to the caller
// after the 250ms quiesce regardless, so the following Connect() usually runs
// while status is still the transitional `disconnecting` state — paho then
// returns an error token (silently discarded by the old code) AND, because
// Disconnect() was called at all, tears down paho's own retry loop for good
// ("user requested no auto reconnection"). The client is left with nothing
// retrying until the watchdog's next trigger fires, which can repeat the same
// race — compounding into very long outages.
//
// client.IsConnectionOpen() (unlike IsConnected()) is strictly status ==
// connected — never true while paho is reconnecting/connecting — so it
// reliably distinguishes "genuinely connected, maybe half-open" (safe to
// Disconnect then Connect; Disconnecting() does not need to wait on any
// in-flight retry loop from status connected, so it completes well within
// the 250ms quiesce) from "paho is already retrying on its own" (must NOT
// call Disconnect; Connect() alone is a safe no-op per paho when a retry is
// already under way, and properly starts a fresh attempt on the rare
// occasion status has actually settled to disconnected).
func buildForceReconnectFn(client mqtt.Client, tag string) func() {
	return func() {
		if client.IsConnectionOpen() {
			client.Disconnect(250)
		}
		// Connect() resolves synchronously (Error() readable immediately,
		// no Wait() needed) for both error returns and the "already
		// retrying, treated as a safe no-op" success case — only a genuine
		// fresh connection attempt leaves the token pending in the
		// background, and we must not block this call on that.
		if token := client.Connect(); token.Error() != nil {
			log.Printf("MQTT [%s] WATCHDOG force-reconnect Connect() failed: %v", tag, token.Error())
		}
	}
}

func handleMessage(store *Store, tag string, source MQTTSource, m mqtt.Message, channelKeys map[string]string, regionKeys map[string][]byte, cfg *Config) {
	// Liveness watchdog (#1212): record receipt before any processing so a
	// slow handler still counts as "source is alive". Cheap atomic store.
	markLivenessForTag(tag, time.Now())
	defer func() {
		if r := recover(); r != nil {
			log.Printf("MQTT [%s] panic in handler: %v", tag, r)
		}
	}()

	topic := m.Topic()
	parts := strings.Split(topic, "/")

	var msg map[string]interface{}
	if err := json.Unmarshal(m.Payload(), &msg); err != nil {
		return
	}

	// Mobile client topics: meshcore/client/{PUBLIC_KEY}/packets (RX coverage)
	// and meshcore/client/{PUBLIC_KEY}/rf (RF environment samples). A roaming
	// companion reports where it directly heard a node, or its own radio's
	// counters; both are handled in isolation from the observer/observations
	// path. EMQX ACL binds parts[2] to the client's own key.
	//
	// The topic match and the enable-gate MUST be separate: matching on
	// parts[1]=="client" always returns from this branch, whatever the config
	// says. The gate only decides drop-vs-handle per sub-topic. Previously the
	// gate sat inside the topic match, so a disabled gate made the whole
	// condition false and fell through to the observer path below, where
	// parts[1] ("client") would be taken as a region and the companion pubkey
	// as an observer id — silently poisoning the region list, and worse now
	// that fullRfLog multiplies client-topic volume.
	if len(parts) >= 4 && parts[1] == "client" {
		// The observer blacklist (checked below on the observer path) only runs
		// there, so a blacklisted operator could otherwise skirt it via the
		// client topic (#1). Enforce it here before any client-topic write, for
		// every sub-topic.
		if cfg.IsObserverBlacklisted(parts[2]) {
			log.Printf("MQTT [%s] client %.8s blacklisted, dropping", tag, parts[2])
			return
		}
		switch parts[3] {
		case "packets":
			if cfg.ClientRxCoverageEnabled() {
				handleClientPacket(store, cfg, tag, parts[2], msg, channelKeys, regionKeys)
			}
		case "rf":
			if cfg.ClientRfSamplesEnabled() {
				handleClientRfSample(store, tag, parts[2], msg)
			}
		}
		return
	}

	// Skip status/connection topics
	if topic == "meshcore/status" || topic == "meshcore/events/connection" {
		return
	}

	// Observer blacklist: drop ALL messages from blacklisted observers before any
	// DB writes (status, metrics, packets). Trumps IATA filter.
	if len(parts) > 2 && cfg.IsObserverBlacklisted(parts[2]) {
		log.Printf("MQTT [%s] observer %.8s blacklisted, dropping", tag, parts[2])
		return
	}

	// Global observer IATA whitelist: if configured, drop messages from observers
	// in non-whitelisted IATA regions. Applies to ALL message types (status + packets).
	if len(parts) > 1 && !cfg.IsObserverIATAAllowed(parts[1]) {
		return
	}

	// Neighbors report topic: meshcore/<region>/<observer_id>/neighbors (#1865).
	// The ESP32 observer firmware emits a periodic neighbor report carrying its
	// own configured region scopes (`self`) plus, for each zero-hop neighbor,
	// the scopes fetched via an OTA scope query. Like /status this is observer
	// metadata (region-independent), so the per-source packet IATA filter below
	// does not apply.
	if len(parts) >= 4 && parts[3] == "neighbors" {
		handleNeighborsReport(store, tag, parts[2], msg)
		return
	}

	// Status topic: meshcore/<region>/<observer_id>/status
	// Per-source IATA filter does NOT apply here — observer metadata (noise_floor, battery, etc.)
	// is region-independent and should be accepted from all observers regardless of
	// which IATA regions are configured for packet ingestion.
	if len(parts) >= 4 && parts[3] == "status" {
		observerID := parts[2]
		name, _ := msg["origin"].(string)
		iata := parts[1]
		meta := extractObserverMeta(msg)
		// A replayed status message is the broker handing us the observer's
		// last published snapshot — it is not evidence the observer is alive
		// now. Stamping last_seen from it resurrects dead observers, so the
		// replay path only refreshes metadata.
		//
		// Two ways to recognise one: the retain flag (our own subscribe), and
		// the payload itself (a replay that reached us through the mosquitto
		// bridge, where the flag does not survive the hop — see
		// statusIsLiveness).
		if m.Retained() || !statusIsLiveness(msg, time.Now().UTC()) {
			if err := store.UpsertObserverRetained(observerID, name, iata, meta); err != nil {
				log.Printf("MQTT [%s] retained observer status error: %v", tag, err)
			}
			log.Print(formatStatusLog(tag, firstNonEmpty(name, observerID), iata))
			return
		}
		// observer.last_seen is "when did the analyzer last hear from this
		// observer" — fundamentally an ingest-time question. Passing "" makes
		// UpsertObserverAt use time.Now(), independent of the envelope timestamp
		// (which can be stale/skewed even when well-formed). See #1465.
		if err := store.UpsertObserverAt(observerID, name, iata, meta, ""); err != nil {
			log.Printf("MQTT [%s] observer status error: %v", tag, err)
		}
		// Insert metrics sample from status message
		if meta != nil {
			metricsData := &MetricsData{
				ObserverID:  observerID,
				NoiseFloor:  meta.NoiseFloor,
				TxAirSecs:   meta.TxAirSecs,
				RxAirSecs:   meta.RxAirSecs,
				RecvErrors:  meta.RecvErrors,
				BatteryMv:   meta.BatteryMv,
				PacketsSent: meta.PacketsSent,
				PacketsRecv: meta.PacketsRecv,
			}
			if err := store.InsertMetrics(metricsData); err != nil {
				log.Printf("MQTT [%s] metrics insert error: %v", tag, err)
			}
		}
		log.Print(formatStatusLog(tag, firstNonEmpty(name, observerID), iata))
		return
	}

	// IATA filter applies to packet messages only — not status messages above.
	if len(source.IATAFilter) > 0 && len(parts) > 1 {
		region := parts[1]
		matched := false
		for _, f := range source.IATAFilter {
			if f == region {
				matched = true
				break
			}
		}
		if !matched {
			return
		}
	}

	// Format 1: Raw packet (meshcoretomqtt / Cisien format)
	rawHex, _ := msg["raw"].(string)
	if rawHex != "" {
		validateSigs := cfg.ShouldValidateSignatures()
		decoded, err := DecodePacket(rawHex, channelKeys, validateSigs)
		if err != nil {
			// Per #1211: include enough context to repro malformed-packet drops,
			// but NEVER log the full observer ID (PII / fingerprinting risk).
			// We log:
			//   - topic prefix (with observer segment elided)
			//   - 8-char observer prefix
			//   - payload length, claimed length (rawHex len)
			obs := ""
			if len(parts) > 2 {
				obs = parts[2]
			}
			// Build a redacted topic that replaces parts[2] (the observer id)
			// with the 8-char prefix, so the rest of the topic is preserved
			// for debugging without leaking the full identifier.
			redactedTopic := topic
			if len(parts) > 2 {
				redactedParts := make([]string, len(parts))
				copy(redactedParts, parts)
				if len(parts[2]) > 8 {
					redactedParts[2] = parts[2][:8]
				}
				redactedTopic = strings.Join(redactedParts, "/")
			}
			log.Printf("MQTT [%s] decode error: %v (topic=%s observer=%.8s rawHexLen=%d)", tag, err, redactedTopic, obs, len(rawHex))
			return
		}

		observerID := ""
		region := ""
		if len(parts) > 2 {
			observerID = parts[2]
		}
		if len(parts) > 1 {
			region = parts[1]
		}
		// Fallback to source-level region config when topic has no region (#788)
		if region == "" && source.Region != "" {
			region = source.Region
		}

		mqttMsg := &MQTTPacketMessage{Raw: rawHex}
		var naiveSkewSec int64
		mqttMsg.Timestamp, naiveSkewSec = resolveRxTime(msg, tag)
		if naiveSkewSec != 0 && observerID != "" {
			// Issue #1478: record so /api/observers can surface ⚠️ chip.
			if err := store.RecordNaiveSkew(observerID, naiveSkewSec, time.Now()); err != nil {
				log.Printf("MQTT [%s] RecordNaiveSkew(%s): %v", tag, observerID, err)
			}
		}
		// Parse optional region from JSON payload (#788)
		if v, ok := msg["region"].(string); ok && v != "" {
			mqttMsg.Region = v
		}
		if v, ok := msg["SNR"]; ok {
			if f, ok := toFloat64(v); ok {
				mqttMsg.SNR = &f
			}
		} else if v, ok := msg["snr"]; ok {
			if f, ok := toFloat64(v); ok {
				mqttMsg.SNR = &f
			}
		}
		if v, ok := msg["RSSI"]; ok {
			if f, ok := toFloat64(v); ok {
				mqttMsg.RSSI = &f
			}
		} else if v, ok := msg["rssi"]; ok {
			if f, ok := toFloat64(v); ok {
				mqttMsg.RSSI = &f
			}
		}
		if v, ok := msg["score"]; ok {
			if f, ok := toFloat64(v); ok {
				mqttMsg.Score = &f
			}
		} else if v, ok := msg["Score"]; ok {
			if f, ok := toFloat64(v); ok {
				mqttMsg.Score = &f
			}
		}
		if v, ok := msg["direction"].(string); ok {
			mqttMsg.Direction = &v
		} else if v, ok := msg["Direction"].(string); ok {
			mqttMsg.Direction = &v
		}
		if v, ok := msg["origin"].(string); ok {
			mqttMsg.Origin = v
		}

		// For ADVERT packets with known coordinates, enforce geo_filter before
		// storing anything — drop the entire message if outside the area.
		if decoded.Header.PayloadTypeName == "ADVERT" && decoded.Payload.PubKey != "" {
			ok, reason := ValidateAdvert(&decoded.Payload)
			if !ok {
				log.Printf("MQTT [%s] skipping corrupted ADVERT: %s", tag, reason)
				return
			}
			// Signature validation: drop adverts with invalid ed25519 signatures
			if validateSigs && decoded.Payload.SignatureValid != nil && !*decoded.Payload.SignatureValid {
				hash := ComputeContentHash(rawHex)
				truncPK := decoded.Payload.PubKey
				if len(truncPK) > 16 {
					truncPK = truncPK[:16]
				}
				log.Printf("MQTT [%s] DROPPED invalid signature: hash=%s name=%s observer=%s pubkey=%s",
					tag, hash, sanitizeLogString(decoded.Payload.Name), sanitizeLogString(firstNonEmpty(mqttMsg.Origin, observerID)), truncPK)
				store.InsertDroppedPacket(&DroppedPacket{
					Hash:         hash,
					RawHex:       rawHex,
					Reason:       "invalid signature",
					ObserverID:   observerID,
					ObserverName: mqttMsg.Origin,
					NodePubKey:   decoded.Payload.PubKey,
					NodeName:     decoded.Payload.Name,
				})
				return
			}
			foreign := false
			if !NodePassesGeoFilter(decoded.Payload.Lat, decoded.Payload.Lon, cfg.GeoFilter) {
				if cfg.ForeignAdverts.IsDropMode() {
					return
				}
				foreign = true
				lat, lon := 0.0, 0.0
				if decoded.Payload.Lat != nil {
					lat = *decoded.Payload.Lat
				}
				if decoded.Payload.Lon != nil {
					lon = *decoded.Payload.Lon
				}
				truncPK := decoded.Payload.PubKey
				if len(truncPK) > 16 {
					truncPK = truncPK[:16]
				}
				log.Printf("MQTT [%s] foreign advert: node=%s name=%s lat=%.4f lon=%.4f observer=%s",
					tag, truncPK, sanitizeLogString(decoded.Payload.Name), lat, lon, sanitizeLogString(firstNonEmpty(mqttMsg.Origin, observerID)))
			}
			pktData := BuildPacketData(mqttMsg, decoded, observerID, region, regionKeys)
			pktData.Foreign = foreign
			isNew, err := store.InsertTransmission(pktData)
			if err != nil {
				log.Printf("MQTT [%s] db insert error: %v", tag, err)
			}
			role := advertRole(decoded.Payload.Flags)
			if err := store.UpsertNode(decoded.Payload.PubKey, decoded.Payload.Name, role, decoded.Payload.Lat, decoded.Payload.Lon, pktData.Timestamp); err != nil {
				log.Printf("MQTT [%s] node upsert error: %v", tag, err)
			}
			if foreign {
				if err := store.MarkNodeForeign(decoded.Payload.PubKey); err != nil {
					log.Printf("MQTT [%s] mark foreign error: %v", tag, err)
				}
			}
			if isNew {
				if err := store.IncrementAdvertCount(decoded.Payload.PubKey); err != nil {
					log.Printf("MQTT [%s] advert count error: %v", tag, err)
				}
			}
			// Update telemetry if present in advert
			if decoded.Payload.BatteryMv != nil || decoded.Payload.TemperatureC != nil {
				if err := store.UpdateNodeTelemetry(decoded.Payload.PubKey, decoded.Payload.BatteryMv, decoded.Payload.TemperatureC); err != nil {
					log.Printf("MQTT [%s] node telemetry update error: %v", tag, err)
				}
			}
			// Update default_scope when advert carries a matched transport scope (#899, #1534)
			if shouldUpdateDefaultScope(pktData) {
				if err := store.UpdateNodeDefaultScope(decoded.Payload.PubKey, pktData.ScopeName); err != nil {
					log.Printf("MQTT [%s] node default_scope update error: %v", tag, err)
				}
			}
		} else {
			// Non-ADVERT packets: store normally (routing/channel messages from
			// in-area observers are relevant regardless of relay hop origin).
			pktData := BuildPacketData(mqttMsg, decoded, observerID, region, regionKeys)
			if _, err := store.InsertTransmission(pktData); err != nil {
				log.Printf("MQTT [%s] db insert error: %v", tag, err)
			}
		}

		// Upsert observer
		if observerID != "" {
			origin, _ := msg["origin"].(string)
			// Use effective region: payload > topic > source config (#788)
			effectiveRegion := region
			if mqttMsg.Region != "" {
				effectiveRegion = mqttMsg.Region
			}
			// Same as the status-path call above: observer.last_seen is ingest
			// time, not envelope time. Per-packet rxTime (stored in observations
			// via InsertTransmission) still uses envelope time. See #1465.
			if err := store.UpsertObserverAt(observerID, origin, effectiveRegion, nil, ""); err != nil {
				log.Printf("MQTT [%s] observer upsert error: %v", tag, err)
			}
		}

		return
	}

	// Format 2: Companion bridge channel message (meshcore/message/channel/<n>)
	if strings.HasPrefix(topic, "meshcore/message/channel/") {
		text, _ := msg["text"].(string)
		if text == "" {
			return
		}

		channelIdx := ""
		if len(parts) >= 4 {
			channelIdx = parts[3]
		}
		if ci, ok := msg["channel_idx"]; ok {
			channelIdx = fmt.Sprintf("%v", ci)
		}

		// Extract sender from "Name: message" format
		sender := ""
		if idx := strings.Index(text, ": "); idx > 0 && idx < 50 {
			sender = text[:idx]
		}

		channelName := fmt.Sprintf("ch%s", channelIdx)

		// Build decoded JSON matching Node.js CHAN format
		channelMsg := map[string]interface{}{
			"type":    "CHAN",
			"channel": channelName,
			"text":    text,
			"sender":  sender,
		}
		if st, ok := msg["sender_timestamp"]; ok {
			channelMsg["sender_timestamp"] = st
		}

		decodedJSON, _ := json.Marshal(channelMsg)

		ingestNow := time.Now().UTC().Format(time.RFC3339)
		hashInput := fmt.Sprintf("ch:%s:%s:%s", channelIdx, text, ingestNow)
		h := sha256.Sum256([]byte(hashInput))
		hash := hex.EncodeToString(h[:])[:16]

		var snr, rssi, score *float64
		var direction *string
		if v, ok := msg["SNR"]; ok {
			if f, ok := toFloat64(v); ok {
				snr = &f
			}
		} else if v, ok := msg["snr"]; ok {
			if f, ok := toFloat64(v); ok {
				snr = &f
			}
		}
		if v, ok := msg["RSSI"]; ok {
			if f, ok := toFloat64(v); ok {
				rssi = &f
			}
		} else if v, ok := msg["rssi"]; ok {
			if f, ok := toFloat64(v); ok {
				rssi = &f
			}
		}
		if v, ok := msg["score"]; ok {
			if f, ok := toFloat64(v); ok {
				score = &f
			}
		} else if v, ok := msg["Score"]; ok {
			if f, ok := toFloat64(v); ok {
				score = &f
			}
		}
		if v, ok := msg["direction"].(string); ok {
			direction = &v
		} else if v, ok := msg["Direction"].(string); ok {
			direction = &v
		}

		pktData := &PacketData{
			Timestamp:    ingestNow, // #1370 (counters #1233): server ingest time, not envelope rxTime
			ObserverID:   "companion",
			ObserverName: "L1 Pro (BLE)",
			SNR:          snr,
			RSSI:         rssi,
			Score:        score,
			Direction:    direction,
			Hash:         hash,
			RouteType:    1, // FLOOD
			PayloadType:  5, // GRP_TXT
			PathJSON:     "[]",
			DecodedJSON:  string(decodedJSON),
			ChannelHash:  channelName, // fast channel queries (#762)
		}

		if _, err := store.InsertTransmission(pktData); err != nil {
			log.Printf("MQTT [%s] channel insert error: %v", tag, err)
		}

		// Note: we intentionally do NOT create a node entry for channel message senders.
		// Channel messages don't carry the sender's real pubkey, so any entry we create
		// would use a synthetic key ("sender-<name>") that doesn't match the real pubkey
		// used for claiming/health lookups. The node will get a proper entry when it
		// sends an advert. See issue #665.

		log.Print(formatChannelMessageLog(tag, channelIdx, firstNonEmpty(sender, "unknown")))
		return
	}

	// Format 2b: Companion bridge direct message (meshcore/message/direct/<id>)
	if strings.HasPrefix(topic, "meshcore/message/direct/") {
		text, _ := msg["text"].(string)
		if text == "" {
			return
		}

		sender := ""
		if idx := strings.Index(text, ": "); idx > 0 && idx < 50 {
			sender = text[:idx]
		}

		dm := map[string]interface{}{
			"type":   "DM",
			"text":   text,
			"sender": sender,
		}
		if st, ok := msg["sender_timestamp"]; ok {
			dm["sender_timestamp"] = st
		}

		decodedJSON, _ := json.Marshal(dm)

		ingestNow := time.Now().UTC().Format(time.RFC3339)
		hashInput := fmt.Sprintf("dm:%s:%s", text, ingestNow)
		h := sha256.Sum256([]byte(hashInput))
		hash := hex.EncodeToString(h[:])[:16]

		var snr, rssi, score *float64
		var direction *string
		if v, ok := msg["SNR"]; ok {
			if f, ok := toFloat64(v); ok {
				snr = &f
			}
		} else if v, ok := msg["snr"]; ok {
			if f, ok := toFloat64(v); ok {
				snr = &f
			}
		}
		if v, ok := msg["RSSI"]; ok {
			if f, ok := toFloat64(v); ok {
				rssi = &f
			}
		} else if v, ok := msg["rssi"]; ok {
			if f, ok := toFloat64(v); ok {
				rssi = &f
			}
		}
		if v, ok := msg["score"]; ok {
			if f, ok := toFloat64(v); ok {
				score = &f
			}
		} else if v, ok := msg["Score"]; ok {
			if f, ok := toFloat64(v); ok {
				score = &f
			}
		}
		if v, ok := msg["direction"].(string); ok {
			direction = &v
		} else if v, ok := msg["Direction"].(string); ok {
			direction = &v
		}

		pktData := &PacketData{
			Timestamp:    ingestNow, // #1370 (counters #1233): server ingest time, not envelope rxTime
			ObserverID:   "companion",
			ObserverName: "L1 Pro (BLE)",
			SNR:          snr,
			RSSI:         rssi,
			Score:        score,
			Direction:    direction,
			Hash:         hash,
			RouteType:    1, // FLOOD
			PayloadType:  2, // TXT_MSG
			PathJSON:     "[]",
			DecodedJSON:  string(decodedJSON),
		}

		if _, err := store.InsertTransmission(pktData); err != nil {
			log.Printf("MQTT [%s] DM insert error: %v", tag, err)
		}

		log.Print(formatDirectMessageLog(tag, firstNonEmpty(sender, "unknown")))
		return
	}
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		s := strings.TrimSpace(n)
		s = stripUnitSuffix(s)
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// unitSuffixes lists common RF/signal unit suffixes to strip before parsing.
var unitSuffixes = []string{"dBm", "dB", "mW", "km", "mi", "m"}

// stripUnitSuffix removes a trailing unit suffix (case-insensitive) from a
// numeric string so that values like "-110dBm" can be parsed as float64.
func stripUnitSuffix(s string) string {
	lower := strings.ToLower(s)
	for _, suffix := range unitSuffixes {
		if strings.HasSuffix(lower, strings.ToLower(suffix)) {
			return strings.TrimSpace(s[:len(s)-len(suffix)])
		}
	}
	return s
}

// extractObserverMeta extracts hardware metadata from an MQTT status message.
// Casts battery_mv and uptime_secs to integers (they're always whole numbers).
func extractObserverMeta(msg map[string]interface{}) *ObserverMeta {
	meta := &ObserverMeta{}
	hasData := false

	if v, ok := msg["model"].(string); ok && v != "" {
		meta.Model = &v
		hasData = true
	}
	if v, ok := msg["firmware"].(string); ok && v != "" {
		meta.Firmware = &v
		hasData = true
	}
	if v, ok := msg["firmware_version"].(string); ok && v != "" {
		meta.Firmware = &v
		hasData = true
	}
	if v, ok := msg["client_version"].(string); ok && v != "" {
		meta.ClientVersion = &v
		hasData = true
	}
	if v, ok := msg["clientVersion"].(string); ok && v != "" {
		meta.ClientVersion = &v
		hasData = true
	}
	if v, ok := msg["radio"].(string); ok && v != "" {
		meta.Radio = &v
		hasData = true
	}

	// Stats fields may be nested under a "stats" object or at top level.
	// Try nested first, fall back to top-level for backward compatibility.
	stats, _ := msg["stats"].(map[string]interface{})

	if v := nestedOrTopLevel(stats, msg, "battery_mv"); v != nil {
		if f, ok := toFloat64(v); ok {
			iv := int(math.Round(f))
			meta.BatteryMv = &iv
			hasData = true
		}
	}
	if v := nestedOrTopLevel(stats, msg, "uptime_secs"); v != nil {
		if f, ok := toFloat64(v); ok {
			iv := int64(math.Round(f))
			meta.UptimeSecs = &iv
			hasData = true
		}
	}
	if v := nestedOrTopLevel(stats, msg, "noise_floor"); v != nil {
		if f, ok := toFloat64(v); ok {
			meta.NoiseFloor = &f
			hasData = true
		}
	}
	if v := nestedOrTopLevel(stats, msg, "tx_air_secs"); v != nil {
		if f, ok := toFloat64(v); ok {
			iv := int(math.Round(f))
			meta.TxAirSecs = &iv
			hasData = true
		}
	}
	if v := nestedOrTopLevel(stats, msg, "rx_air_secs"); v != nil {
		if f, ok := toFloat64(v); ok {
			iv := int(math.Round(f))
			meta.RxAirSecs = &iv
			hasData = true
		}
	}
	if v := nestedOrTopLevel(stats, msg, "recv_errors"); v != nil {
		if f, ok := toFloat64(v); ok {
			iv := int(math.Round(f))
			meta.RecvErrors = &iv
			hasData = true
		}
	}
	if v := nestedOrTopLevel(stats, msg, "packets_sent"); v != nil {
		if f, ok := toFloat64(v); ok {
			iv := int(math.Round(f))
			meta.PacketsSent = &iv
			hasData = true
		}
	}
	if v := nestedOrTopLevel(stats, msg, "packets_recv"); v != nil {
		if f, ok := toFloat64(v); ok {
			iv := int(math.Round(f))
			meta.PacketsRecv = &iv
			hasData = true
		}
	}

	// Issue #1290: firmware 1.16 publishes a `repeat` flag at the top
	// level of the /status JSON (MQTTMessageBuilder.cpp:58 — see
	// agessaman/MeshCore mqtt-bridge-implementation-flex). Accept
	// either a boolean or a case-insensitive `on|off|true|false|1|0`
	// string. Missing field → leave CanRelay nil; the writer preserves
	// the prior column value (default 1, back-compat).
	if v, ok := msg["repeat"]; ok && v != nil {
		switch t := v.(type) {
		case bool:
			b := t
			meta.CanRelay = &b
			hasData = true
		case string:
			s := strings.ToLower(strings.TrimSpace(t))
			switch s {
			case "on", "true", "1", "yes":
				b := true
				meta.CanRelay = &b
				hasData = true
			case "off", "false", "0", "no":
				b := false
				meta.CanRelay = &b
				hasData = true
			}
		case float64:
			b := t != 0
			meta.CanRelay = &b
			hasData = true
		}
	}

	if !hasData {
		return nil
	}
	return meta
}

// nestedOrTopLevel looks up a key in the nested map first, then the top-level map.
func nestedOrTopLevel(nested, toplevel map[string]interface{}, key string) interface{} {
	if nested != nil {
		if v, ok := nested[key]; ok {
			return v
		}
	}
	if v, ok := toplevel[key]; ok {
		return v
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolveRxTime returns the observer receive-time for a packet, taken from
// the MQTT envelope's "timestamp" field. Falls back to ingest time only when
// the field is missing, unparseable, or implausibly in the future (a
// clock-skewed observer). Result is always RFC3339 UTC.
//
// The envelope timestamp is stamped by the uploader when the radio receives
// the frame, not when the MQTT message is published — so a buffered packet
// uploaded hours late still carries its true receive time. Using ingest time
// (time.Now()) here mis-dated such packets by the upload delay.
//
// The returned naiveSkewSec is 0 unless a naive (zone-less) timestamp had to
// be clamped because it was off from server-now by >15min — in which case it
// is the signed offset in seconds (negative = observer behind UTC, positive =
// ahead). Caller records this via Store.RecordNaiveSkew so the UI can flag
// the observer (#1478).
func resolveRxTime(msg map[string]interface{}, tag string) (string, int64) {
	t, skew := resolveRxTimeCore(msg, tag)
	return t.Format(time.RFC3339), skew
}

// rxTimeMillisLayout is the fixed three-decimal millisecond layout for
// client_rx_observations.rx_at. UNIQUE(rx_pubkey, pkt_hash, rx_at) on that
// table relies on sub-second resolution to keep distinct forwarder copies of
// the same flood as separate rows — RFC3339's second resolution would
// collapse them and ON CONFLICT DO NOTHING would silently drop all but the
// first. Deliberately not time.RFC3339Nano, which strips trailing zeros, so
// this stays a stable uniqueness/sort key. The single definition here is
// shared by its only production caller (handleClientPacket, which formats a
// resolveRxTimeCore result with it directly) and by tests that need to
// derive an rx_at they can query back by.
const rxTimeMillisLayout = "2006-01-02T15:04:05.000Z07:00"

// resolveRxTimeCore holds the validation/clamping logic shared by
// resolveRxTime and any caller that needs the resolved time in a different
// format (handleClientPacket formats it with rxTimeMillisLayout for
// client_rx_observations). Returns UTC time so callers format it themselves —
// a caller that needs two different formatted strings for the SAME packet
// calls this once and formats twice, instead of parsing/validating/logging
// twice and risking two independent time.Now() reads straddling a second
// boundary.
func resolveRxTimeCore(msg map[string]interface{}, tag string) (time.Time, int64) {
	now := time.Now().UTC()
	raw, _ := msg["timestamp"].(string)
	if raw == "" {
		return now, 0
	}
	t, naive, err := parseEnvelopeTime(raw)
	if err != nil {
		log.Printf("MQTT [%s] unparseable timestamp %q, using ingest time", tag, raw)
		return now, 0
	}
	// Hard reject: > 14h ahead is a genuine clock error (UTC+14 is the maximum
	// standard offset, so nothing valid should be further ahead than that).
	if t.After(now.Add(14 * time.Hour)) {
		log.Printf("MQTT [%s] future timestamp %q, using ingest time", tag, raw)
		return now, 0
	}
	// Hard reject: > 30 days in the past is an RTC-reset node reporting a
	// factory date (e.g. 2020-01-01). Such a value would permanently drag
	// transmissions.first_seen backwards via stmtUpdateTxFirstSeen in
	// InsertTransmission. No legitimate buffered upload is that stale.
	if t.Before(now.Add(-30 * 24 * time.Hour)) {
		log.Printf("MQTT [%s] stale timestamp %q (>30d old), using ingest time", tag, raw)
		return now, 0
	}
	// Symmetric naive-timestamp clamp (issue #1463). Naive (zone-less) ISO
	// values from observers in non-UTC zones are parsed as-if UTC, leaving a
	// residual offset equal to the observer's UTC offset:
	//   - UTC+N observer → value appears N hours in the future
	//   - UTC-N observer → value appears N hours in the past
	// The past case was silently stored verbatim, poisoning last_seen and
	// rendering UTC-N observers perpetually "Stale" in the UI. Collapse any
	// naive value more than 15 min off server-now to now() — well-behaved
	// observers (Z-suffixed or explicit offset) are untouched regardless of
	// skew so legitimate buffered uploads remain accurate.
	const naiveTolerance = 15 * time.Minute
	if naive {
		signed := t.Sub(now) // signed: positive = ahead, negative = behind
		abs := signed
		if abs < 0 {
			abs = -abs
		}
		if abs > naiveTolerance {
			// Issue #1478: surface to UI via RecordNaiveSkew (called by handler).
			// Per-message log was silenced in #1479 — chip + banner in the UI
			// replace it.
			deltaSec := int64(signed / time.Second)
			return now, deltaSec
		}
	}
	// Legacy soft clamp for zone-aware near-future values: any value ahead of
	// now is from a slightly skewed observer clock — collapse to now so we
	// don't render ⚠️ in the UI for live packets from those nodes.
	if t.After(now) {
		return now, 0
	}
	return t.UTC(), 0
}

// parseEnvelopeTime parses the MQTT envelope timestamp. Two on-wire forms
// occur: zone-aware ISO8601 (RFC3339), and a naive local-clock ISO string
// with no zone (python datetime.isoformat()). Zone-aware layouts are tried
// first; naive layouts are assumed UTC but the caller is informed via the
// returned `naive` flag so it can apply a symmetric clamp (see issue #1463).
func parseEnvelopeTime(s string) (time.Time, bool, error) {
	// Zone-aware first — RFC3339 demands Z or ±HH:MM.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, false, nil
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999", // python isoformat w/ microseconds
		"2006-01-02T15:04:05",        // naive ISO
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("unrecognized timestamp layout: %q", s)
}

// deriveHashtagChannelKey derives an AES-128 key from a channel name.
// Same algorithm as Node.js: SHA-256(channelName) → first 32 hex chars (16 bytes).
func deriveHashtagChannelKey(channelName string) string {
	h := sha256.Sum256([]byte(channelName))
	return hex.EncodeToString(h[:16])
}

// builtinChannelKeys returns channel keys that are part of the MeshCore firmware
// defaults and should always be available, regardless of the rainbow file or config.
// Adding new entries here is the right move when a key is part of the protocol spec
// (not a community-named hashtag channel).
func builtinChannelKeys() map[string]string {
	return map[string]string{
		// Default Public channel — well-known PSK from the MeshCore companion
		// protocol spec. Channel-hash byte = 0x11.
		"Public": "8b3387e9c5cdea6ac9e5edbaa115cd72",
	}
}

// loadChannelKeys loads channel decryption keys from config and/or a JSON file.
// Merge priority: builtin (lowest) → rainbow → derived from hashChannels → explicit config (highest).
func loadChannelKeys(cfg *Config, configPath string) map[string]string {
	keys := make(map[string]string)

	// 0. Built-in firmware-default keys (lowest priority — overridable by everything else)
	for k, v := range builtinChannelKeys() {
		keys[k] = v
	}

	// 1. Rainbow table keys
	keysPath := os.Getenv("CHANNEL_KEYS_PATH")
	if keysPath == "" {
		keysPath = cfg.ChannelKeysPath
	}
	if keysPath == "" {
		keysPath = filepath.Join(filepath.Dir(configPath), "channel-rainbow.json")
	}

	rainbowCount := 0
	if data, err := os.ReadFile(keysPath); err == nil {
		var fileKeys map[string]string
		if err := json.Unmarshal(data, &fileKeys); err == nil {
			for k, v := range fileKeys {
				keys[k] = v
			}
			rainbowCount = len(fileKeys)
			log.Printf("Loaded %d channel keys from %s", rainbowCount, keysPath)
		} else {
			log.Printf("Warning: failed to parse channel keys file %s: %v", keysPath, err)
		}
	}

	// 2. Derived keys from hashChannels (middle priority)
	derivedCount := 0
	for _, raw := range cfg.HashChannels {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		channelName := trimmed
		if !strings.HasPrefix(channelName, "#") {
			channelName = "#" + channelName
		}
		// Skip if explicit config already has this key
		if _, exists := cfg.ChannelKeys[channelName]; exists {
			continue
		}
		keys[channelName] = deriveHashtagChannelKey(channelName)
		derivedCount++
	}
	if derivedCount > 0 {
		log.Printf("[channels] %d derived from hashChannels", derivedCount)
	}

	// 3. Explicit config keys (highest priority — overrides rainbow + derived)
	for k, v := range cfg.ChannelKeys {
		normalized := normalizeChannelName(k)
		if normalized != k {
			log.Printf("[channels] Normalizing known channel key %q → %q for display", k, normalized)
		}
		// Detect config collision: if both "public" and "Public" are present,
		// the normalized key collides. Resolve deterministically: prefer the
		// canonical (already-normalized) form over the lowercase variant.
		if _, dupe := keys[normalized]; dupe {
			// If the incoming key IS the canonical form, it wins (overwrite).
			// If the incoming key is a non-canonical form (e.g., "public"), keep existing.
			if k == normalized {
				log.Printf("[channels] Resolving duplicate %q: canonical form wins over non-canonical", normalized)
				keys[normalized] = v
			} else {
				log.Printf("[channels] WARNING: duplicate channel key %q — config has %q normalizing to %q, keeping canonical value", normalized, k, normalized)
			}
		} else {
			keys[normalized] = v
		}
	}

	return keys
}

func loadRegionKeys(cfg *Config) map[string][]byte {
	keys := make(map[string][]byte)
	for _, raw := range cfg.HashRegions {
		name := strings.TrimSpace(raw)
		if name == "" {
			log.Printf("[regions] skipping empty hashRegions entry")
			continue
		}
		if !strings.HasPrefix(name, "#") {
			name = "#" + name
		}
		if _, exists := keys[name]; exists {
			log.Printf("[regions] duplicate region %q ignored", name)
			continue
		}
		h := sha256.Sum256([]byte(name))
		keys[name] = h[:16]
	}
	if len(keys) > 0 {
		log.Printf("[regions] %d region key(s) loaded", len(keys))
	}
	return keys
}

// matchingRegions returns every configured region whose derived code equals
// the packet's code1, rather than the first one found.
//
// The distinction matters because code1 is two bytes: two configured regions
// collide on a given payload with probability 1/65536, and at 159 keys on a
// live instance that is roughly 0.25% of transport-scoped packets, hundreds a
// week rather than a curiosity. Returning the first match made the stored
// region name depend on Go's randomised map iteration order, so the same
// packet could be named differently on two runs and neither answer was
// evidence of anything.
//
// The cost is unchanged: this is the same single pass over the same keys, it
// just does not stop early. There is no indexable shortcut, because code1 is
// an HMAC over the payload and nothing here is payload-independent.
func matchingRegions(regionKeys map[string][]byte, payloadType byte, payloadRaw []byte, code1 string) []string {
	if code1 == "0000" || len(regionKeys) == 0 || len(payloadRaw) == 0 {
		return nil
	}
	var matched []string
	for name, key := range regionKeys {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte{payloadType})
		mac.Write(payloadRaw)
		hmacBytes := mac.Sum(nil)
		code := uint16(hmacBytes[0]) | uint16(hmacBytes[1])<<8
		if code == 0 {
			code = 1
		} else if code == 0xFFFF {
			code = 0xFFFE
		}
		codeBytes := [2]byte{byte(code & 0xFF), byte(code >> 8)}
		if strings.ToUpper(hex.EncodeToString(codeBytes[:])) == code1 {
			matched = append(matched, name)
		}
	}
	return matched
}

// matchScope names one packet's region scope, or returns "" when it cannot be
// named with confidence.
//
// Exactly one match names the packet. Several matches name nothing: the two
// candidates are equally sourced, there is no principled winner, and storing
// a wrong region name is worse than storing none. "" is already the ingestor's
// "transport-scoped but unnameable" state (scopeNameForDB), so an ambiguous
// packet lands in a state the rest of the system already understands rather
// than in a new one.
//
// The collision is logged because it is otherwise invisible: the packet is
// stored exactly like one whose region this instance holds no key for, and an
// operator looking at a growing unnameable count deserves to see which of
// their configured regions are colliding.
func matchScope(regionKeys map[string][]byte, payloadType byte, payloadRaw []byte, code1 string) string {
	matched := matchingRegions(regionKeys, payloadType, payloadRaw, code1)
	switch len(matched) {
	case 0:
		return ""
	case 1:
		return matched[0]
	}
	log.Printf("[regions] ambiguous collision between %v; storing unmatched", matched)
	return ""
}

// Version info (set via ldflags)
var version = "dev"

func init() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("corescope-ingestor", version)
		os.Exit(0)
	}
}

// handleNeighborsReport ingests an observer /neighbors report (#1865) and
// records CONFIRMED region scopes into nodes.configured_scope:
//   - the observer's own scopes from `self`, keyed by origin_id (the observer
//     node pubkey), which need no OTA query and are always trusted; and
//   - each neighbor whose OTA scope query returned status=="responded".
//
// Per the report contract: neighbors with any other status (e.g. "timeout")
// are skipped — a failed query is NOT evidence the scopes were cleared — and a
// missing neighbor is never a signal (the report is 10 KB-capped and truncates
// by ordering, so absent != gone). Report pubkeys are uppercase; nodes.public_key
// is lowercase hex, so keys are lowercased before the UPDATE. Unknown neighbors
// are a no-op (the UPDATE matches no row) until a later advert creates the node.
func handleNeighborsReport(store *Store, tag string, observerID string, msg map[string]interface{}) {
	reportedAt, _ := msg["timestamp"].(string)

	// self: the observer's own configured scopes.
	originID, _ := msg["origin_id"].(string)
	if originID == "" {
		originID = observerID
	}
	originID = strings.ToLower(originID)
	if self, ok := msg["self"].(map[string]interface{}); ok && originID != "" {
		if sc, ok := self["scopes"].(string); ok {
			if err := store.UpdateNodeConfiguredScope(originID, sc, reportedAt); err != nil {
				log.Printf("MQTT [%s] neighbors self scope error: %v", tag, err)
			}
		}
	}

	// neighbors[]: only status=="responded" carries usable scope evidence.
	neighbors, _ := msg["neighbors"].([]interface{})
	for _, raw := range neighbors {
		n, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if status, _ := n["status"].(string); status != "responded" {
			continue
		}
		pubkey, _ := n["pubkey"].(string)
		pubkey = strings.ToLower(pubkey)
		if pubkey == "" {
			continue
		}
		scopes, _ := n["scopes"].(string)
		if err := store.UpdateNodeConfiguredScope(pubkey, scopes, reportedAt); err != nil {
			log.Printf("MQTT [%s] neighbors scope error for %.8s: %v", tag, pubkey, err)
		}
	}
}

// shouldUpdateDefaultScope returns true when the packet carries a transport
// scope whose region key matched (#1534). Without the ScopeName non-empty
// guard, transport-scoped adverts from non-matching regions would overwrite
// previously-correct default_scope values with the empty string.
func shouldUpdateDefaultScope(pktData *PacketData) bool {
	return pktData.IsTransportScoped && pktData.ScopeName != ""
}
