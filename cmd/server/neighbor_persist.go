package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// persistSem limits concurrent async persistence goroutines to 1.
// Without this, each ingest cycle spawns a goroutine that opens a new
// SQLite RW connection; under sustained load goroutines pile up with
// no backpressure, causing contention and busy-timeout cascades.
var persistSem = make(chan struct{}, 1)

// ─── neighbor_edges table ──────────────────────────────────────────────────────

// ensureNeighborEdgesTable creates the neighbor_edges table if it doesn't exist.
// Uses a separate read-write connection since the main DB is read-only.
func ensureNeighborEdgesTable(dbPath string) error {
	rw, err := openRW(dbPath)
	if err != nil {
		return fmt.Errorf("open rw for neighbor_edges: %w", err)
	}
	defer rw.Close()

	_, err = rw.Exec(`CREATE TABLE IF NOT EXISTS neighbor_edges (
		node_a TEXT NOT NULL,
		node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_seen TEXT,
		PRIMARY KEY (node_a, node_b)
	)`)
	return err
}

// loadNeighborEdgesFromDB loads all edges from the neighbor_edges table
// and builds an in-memory NeighborGraph.
func loadNeighborEdgesFromDB(conn *sql.DB) *NeighborGraph {
	g := NewNeighborGraph()

	rows, err := conn.Query("SELECT node_a, node_b, count, last_seen FROM neighbor_edges")
	if err != nil {
		log.Printf("[neighbor] failed to load neighbor_edges: %v", err)
		return g
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var a, b string
		var cnt int
		var lastSeen sql.NullString
		if err := rows.Scan(&a, &b, &cnt, &lastSeen); err != nil {
			continue
		}
		ts := time.Time{}
		if lastSeen.Valid {
			ts = parseTimestamp(lastSeen.String)
		}
		// Build edge directly (both nodes are full pubkeys from persisted data)
		key := makeEdgeKey(a, b)
		g.mu.Lock()
		e, exists := g.edges[key]
		if !exists {
			e = &NeighborEdge{
				NodeA:     key.A,
				NodeB:     key.B,
				Observers: make(map[string]bool),
				FirstSeen: ts,
				LastSeen:  ts,
				Count:     cnt,
			}
			g.edges[key] = e
			g.byNode[key.A] = append(g.byNode[key.A], e)
			g.byNode[key.B] = append(g.byNode[key.B], e)
		} else {
			e.Count += cnt
			if ts.After(e.LastSeen) {
				e.LastSeen = ts
			}
		}
		g.mu.Unlock()
		count++
	}

	if count > 0 {
		g.mu.Lock()
		g.builtAt = time.Now()
		g.mu.Unlock()
		log.Printf("[neighbor] loaded %d edges from neighbor_edges table", count)
	}

	return g
}

// ─── shared async persistence helper ───────────────────────────────────────────

// persistObsUpdate holds data for a resolved_path SQLite update.
type persistObsUpdate struct {
	obsID        int
	resolvedPath string
}

// persistEdgeUpdate holds data for a neighbor_edges SQLite upsert.
type persistEdgeUpdate struct {
	a, b, ts string
}

// asyncPersistResolvedPathsAndEdges writes resolved_path updates and neighbor
// edge upserts to SQLite in a background goroutine. Shared between
// IngestNewFromDB and IngestNewObservations to avoid DRY violation.
func asyncPersistResolvedPathsAndEdges(dbPath string, obsUpdates []persistObsUpdate, edgeUpdates []persistEdgeUpdate, logPrefix string) {
	if len(obsUpdates) == 0 && len(edgeUpdates) == 0 {
		return
	}
	// Try-acquire semaphore BEFORE spawning goroutine. If another
	// persistence operation is already running, drop this batch —
	// data lives in memory and will be backfilled on restart.
	select {
	case persistSem <- struct{}{}:
		// Acquired — spawn goroutine to do the work.
	default:
		log.Printf("[store] %s skipped: persistence already in progress", logPrefix)
		return
	}
	go func() {
		defer func() { <-persistSem }()

		rw, err := openRW(dbPath)
		if err != nil {
			log.Printf("[store] %s rw open error: %v", logPrefix, err)
			return
		}
		defer rw.Close()

		if len(obsUpdates) > 0 {
			sqlTx, err := rw.Begin()
			if err == nil {
				stmt, err := sqlTx.Prepare("UPDATE observations SET resolved_path = ? WHERE id = ?")
				if err == nil {
					var firstErr error
					for _, u := range obsUpdates {
						if _, err := stmt.Exec(u.resolvedPath, u.obsID); err != nil && firstErr == nil {
							firstErr = err
						}
					}
					stmt.Close()
					if firstErr != nil {
						log.Printf("[store] %s resolved_path error (first): %v", logPrefix, firstErr)
					}
				} else {
					log.Printf("[store] %s resolved_path prepare error: %v", logPrefix, err)
				}
				sqlTx.Commit()
			}
		}

		if len(edgeUpdates) > 0 {
			sqlTx, err := rw.Begin()
			if err == nil {
				stmt, err := sqlTx.Prepare(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen)
					VALUES (?, ?, 1, ?)
					ON CONFLICT(node_a, node_b) DO UPDATE SET
					count = count + 1, last_seen = MAX(last_seen, excluded.last_seen)`)
				if err == nil {
					var firstErr error
					for _, e := range edgeUpdates {
						if _, err := stmt.Exec(e.a, e.b, e.ts); err != nil && firstErr == nil {
							firstErr = err
						}
					}
					stmt.Close()
					if firstErr != nil {
						log.Printf("[store] %s edge error (first): %v", logPrefix, firstErr)
					}
				} else {
					log.Printf("[store] %s edge prepare error: %v", logPrefix, err)
				}
				sqlTx.Commit()
			}
		}
	}()
}

// neighborEdgesTableExists checks if the neighbor_edges table has any data.
func neighborEdgesTableExists(conn *sql.DB) bool {
	var cnt int
	err := conn.QueryRow("SELECT COUNT(*) FROM neighbor_edges").Scan(&cnt)
	if err != nil {
		return false // table doesn't exist
	}
	return cnt > 0
}

// buildAndPersistEdges scans all packets in the store, extracts edges per
// ADVERT/non-ADVERT rules, and persists them to SQLite.
func buildAndPersistEdges(store *PacketStore, rw *sql.DB) int {
	store.mu.RLock()
	packets := make([]*StoreTx, len(store.packets))
	copy(packets, store.packets)
	store.mu.RUnlock()

	_, pm := store.getCachedNodesAndPM()

	tx, err := rw.Begin()
	if err != nil {
		log.Printf("[neighbor] begin tx error: %v", err)
		return 0
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(node_a, node_b) DO UPDATE SET
		count = count + 1, last_seen = MAX(last_seen, excluded.last_seen)`)
	if err != nil {
		log.Printf("[neighbor] prepare stmt error: %v", err)
		return 0
	}
	defer stmt.Close()

	edgeCount := 0
	var firstErr error
	for _, pkt := range packets {
		for _, obs := range pkt.Observations {
			for _, ec := range extractEdgesFromObs(obs, pkt, pm) {
				if _, err := stmt.Exec(ec.A, ec.B, ec.Timestamp); err != nil && firstErr == nil {
					firstErr = err
				}
				edgeCount++
			}
		}
	}
	if firstErr != nil {
		log.Printf("[neighbor] edge exec error (first): %v", firstErr)
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[neighbor] commit error: %v", err)
		return 0
	}
	return edgeCount
}

// ─── resolved_path column ──────────────────────────────────────────────────────

// ensureResolvedPathColumn adds the resolved_path column to observations if missing.
func ensureResolvedPathColumn(dbPath string) error {
	rw, err := openRW(dbPath)
	if err != nil {
		return err
	}
	defer rw.Close()

	// Check if column already exists
	rows, err := rw.Query("PRAGMA table_info(observations)")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var colName string
		var colType sql.NullString
		var notNull, pk int
		var dflt sql.NullString
		if rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk) == nil && colName == "resolved_path" {
			return nil // already exists
		}
	}

	_, err = rw.Exec("ALTER TABLE observations ADD COLUMN resolved_path TEXT")
	if err != nil {
		return fmt.Errorf("add resolved_path column: %w", err)
	}
	log.Println("[store] Added resolved_path column to observations")
	return nil
}

// resolvePathForObs resolves hop prefixes to full pubkeys for an observation.
// Returns nil if path is empty.
func resolvePathForObs(pathJSON, observerID string, tx *StoreTx, pm *prefixMap, graph *NeighborGraph) []*string {
	hops := parsePathJSON(pathJSON)
	if len(hops) == 0 {
		return nil
	}

	// Build context pubkeys: observer + originator (if known)
	contextPKs := make([]string, 0, 3)
	if observerID != "" {
		contextPKs = append(contextPKs, strings.ToLower(observerID))
	}
	fromNode := extractFromNode(tx)
	if fromNode != "" {
		contextPKs = append(contextPKs, strings.ToLower(fromNode))
	}

	resolved := make([]*string, len(hops))
	for i, hop := range hops {
		// Add adjacent hops as context for disambiguation
		ctx := make([]string, len(contextPKs), len(contextPKs)+2)
		copy(ctx, contextPKs)
		// Add previously resolved hops as context
		if i > 0 && resolved[i-1] != nil {
			ctx = append(ctx, *resolved[i-1])
		}

		node, _, _ := pm.resolveWithContext(hop, ctx, graph)
		if node != nil {
			pk := strings.ToLower(node.PublicKey)
			resolved[i] = &pk
		}
	}

	return resolved
}

// marshalResolvedPath converts []*string to JSON for storage.
func marshalResolvedPath(rp []*string) string {
	if len(rp) == 0 {
		return ""
	}
	b, err := json.Marshal(rp)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalResolvedPath parses a resolved_path JSON string.
func unmarshalResolvedPath(s string) []*string {
	if s == "" {
		return nil
	}
	var result []*string
	if json.Unmarshal([]byte(s), &result) != nil {
		return nil
	}
	return result
}

// backfillResolvedPaths resolves paths for all observations that have NULL resolved_path.
func backfillResolvedPaths(store *PacketStore, dbPath string) int {
	// Collect pending observations and snapshot immutable fields under read lock.
	// graph is set in main.go before backfill is called; nil-safe throughout (review item #6).
	type obsRef struct {
		obsID      int
		pathJSON   string
		observerID string
		txJSON     string // snapshot of DecodedJSON for extractFromNode
		payloadType *int
	}
	store.mu.RLock()
	pm := store.nodePM
	graph := store.graph
	var pending []obsRef
	for _, tx := range store.packets {
		for _, obs := range tx.Observations {
			if obs.ResolvedPath == nil && obs.PathJSON != "" && obs.PathJSON != "[]" {
				pending = append(pending, obsRef{
					obsID:       obs.ID,
					pathJSON:    obs.PathJSON,
					observerID:  obs.ObserverID,
					txJSON:      tx.DecodedJSON,
					payloadType: tx.PayloadType,
				})
			}
		}
	}
	store.mu.RUnlock()

	if len(pending) == 0 || pm == nil {
		return 0
	}

	// Resolve paths outside the lock — resolvePathForObs only reads pm and graph.
	type resolved struct {
		obsID  int
		rp     []*string
		rpJSON string
	}
	var results []resolved
	for _, ref := range pending {
		// Build a minimal StoreTx for extractFromNode (only needs DecodedJSON + PayloadType).
		fakeTx := &StoreTx{DecodedJSON: ref.txJSON, PayloadType: ref.payloadType}
		rp := resolvePathForObs(ref.pathJSON, ref.observerID, fakeTx, pm, graph)
		if len(rp) > 0 {
			rpJSON := marshalResolvedPath(rp)
			if rpJSON != "" {
				results = append(results, resolved{ref.obsID, rp, rpJSON})
			}
		}
	}

	if len(results) == 0 {
		return 0
	}

	// Persist to SQLite (no lock needed — separate RW connection).
	rw, err := openRW(dbPath)
	if err != nil {
		log.Printf("[store] backfill: open rw error: %v", err)
		return 0
	}
	defer rw.Close()

	sqlTx, err := rw.Begin()
	if err != nil {
		log.Printf("[store] backfill: begin tx error: %v", err)
		return 0
	}
	defer sqlTx.Rollback()

	stmt, err := sqlTx.Prepare("UPDATE observations SET resolved_path = ? WHERE id = ?")
	if err != nil {
		log.Printf("[store] backfill: prepare error: %v", err)
		return 0
	}
	defer stmt.Close()

	var firstErr error
	for _, r := range results {
		if _, err := stmt.Exec(r.rpJSON, r.obsID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		log.Printf("[store] backfill resolved_path exec error (first): %v", firstErr)
	}

	if err := sqlTx.Commit(); err != nil {
		log.Printf("[store] backfill: commit error: %v", err)
		return 0
	}

	// Update in-memory state under write lock.
	store.mu.Lock()
	count := 0
	for _, r := range results {
		if obs, ok := store.byObsID[r.obsID]; ok {
			obs.ResolvedPath = r.rp
			count++
		}
	}
	store.mu.Unlock()

	return count
}

// backfillResolvedPathsAsync processes observations with NULL resolved_path in
// chunks, yielding between batches so HTTP handlers remain responsive. It sets
// store.backfillComplete when finished and re-picks best observations for any
// transmissions affected by newly resolved paths.
func backfillResolvedPathsAsync(store *PacketStore, dbPath string, chunkSize int, yieldDuration time.Duration) {
	// Snapshot of pending observation references — collected once under read lock.
	type obsRef struct {
		obsID       int
		pathJSON    string
		observerID  string
		txJSON      string
		payloadType *int
		txHash      string
	}

	store.mu.RLock()
	pm := store.nodePM
	graph := store.graph
	var pending []obsRef
	for _, tx := range store.packets {
		for _, obs := range tx.Observations {
			if obs.ResolvedPath == nil && obs.PathJSON != "" && obs.PathJSON != "[]" {
				pending = append(pending, obsRef{
					obsID:       obs.ID,
					pathJSON:    obs.PathJSON,
					observerID:  obs.ObserverID,
					txJSON:      tx.DecodedJSON,
					payloadType: tx.PayloadType,
					txHash:      tx.Hash,
				})
			}
		}
	}
	store.mu.RUnlock()

	if len(pending) == 0 || pm == nil {
		store.backfillComplete.Store(true)
		log.Printf("[store] async resolved_path backfill: nothing to do")
		return
	}

	store.backfillTotal.Store(int64(len(pending)))
	store.backfillProcessed.Store(0)
	log.Printf("[store] async resolved_path backfill starting: %d observations", len(pending))

	type resolved struct {
		obsID  int
		rp     []*string
		rpJSON string
		txHash string
	}

	for i := 0; i < len(pending); i += chunkSize {
		end := i + chunkSize
		if end > len(pending) {
			end = len(pending)
		}
		chunk := pending[i:end]

		// Resolve paths outside the lock.
		var results []resolved
		for _, ref := range chunk {
			fakeTx := &StoreTx{DecodedJSON: ref.txJSON, PayloadType: ref.payloadType}
			rp := resolvePathForObs(ref.pathJSON, ref.observerID, fakeTx, pm, graph)
			if len(rp) > 0 {
				rpJSON := marshalResolvedPath(rp)
				if rpJSON != "" {
					results = append(results, resolved{ref.obsID, rp, rpJSON, ref.txHash})
				}
			}
		}

		// Persist to SQLite in a single transaction per chunk.
		if len(results) > 0 {
			rw, err := openRW(dbPath)
			if err != nil {
				log.Printf("[store] async backfill: open rw error: %v", err)
			} else {
				sqlTx, err := rw.Begin()
				if err != nil {
					log.Printf("[store] async backfill: begin tx error: %v", err)
				} else {
					stmt, err := sqlTx.Prepare("UPDATE observations SET resolved_path = ? WHERE id = ?")
					if err != nil {
						log.Printf("[store] async backfill: prepare error: %v", err)
						sqlTx.Rollback()
					} else {
						for _, r := range results {
							if _, err := stmt.Exec(r.rpJSON, r.obsID); err != nil {
								log.Printf("[store] async backfill: exec error obs %d: %v", r.obsID, err)
							}
						}
						stmt.Close()
						if err := sqlTx.Commit(); err != nil {
							log.Printf("[store] async backfill: commit error: %v", err)
						}
					}
				}
				rw.Close()
			}

			// Update in-memory state + re-pick best obs for affected txs.
			affectedTxs := make(map[string]bool)
			store.mu.Lock()
			for _, r := range results {
				if obs, ok := store.byObsID[r.obsID]; ok {
					obs.ResolvedPath = r.rp
				}
				affectedTxs[r.txHash] = true
			}
			for hash := range affectedTxs {
				if tx, ok := store.byHash[hash]; ok {
					pickBestObservation(tx)
				}
			}
			store.mu.Unlock()
		}

		store.backfillProcessed.Store(int64(end))
		pct := float64(end) / float64(len(pending)) * 100
		log.Printf("[store] backfill progress: %d/%d observations (%.1f%%)", end, len(pending), pct)

		time.Sleep(yieldDuration)
	}

	store.backfillComplete.Store(true)
	log.Printf("[store] async resolved_path backfill complete: %d observations processed", len(pending))
}

// ─── Shared helpers ────────────────────────────────────────────────────────────

// edgeCandidate represents an extracted edge to be persisted.
type edgeCandidate struct {
	A, B, Timestamp string
}

// extractEdgesFromObs extracts neighbor edge candidates from a single observation.
// For ADVERTs: originator↔path[0] (if unambiguous). For ALL types: observer↔path[last] (if unambiguous).
// Also handles zero-hop ADVERTs (originator↔observer direct link).
func extractEdgesFromObs(obs *StoreObs, tx *StoreTx, pm *prefixMap) []edgeCandidate {
	isAdvert := tx.PayloadType != nil && *tx.PayloadType == 4
	fromNode := extractFromNode(tx)
	path := parsePathJSON(obs.PathJSON)
	observerPK := strings.ToLower(obs.ObserverID)
	ts := obs.Timestamp
	var edges []edgeCandidate

	if len(path) == 0 {
		if isAdvert && fromNode != "" {
			fromLower := strings.ToLower(fromNode)
			if fromLower != observerPK {
				a, b := fromLower, observerPK
				if a > b {
					a, b = b, a
				}
				edges = append(edges, edgeCandidate{a, b, ts})
			}
		}
		return edges
	}

	// Edge 1: originator ↔ path[0] — ADVERTs only (resolve prefix to full pubkey)
	if isAdvert && fromNode != "" && pm != nil {
		firstHop := strings.ToLower(path[0])
		fromLower := strings.ToLower(fromNode)
		candidates := pm.m[firstHop]
		if len(candidates) == 1 {
			resolved := strings.ToLower(candidates[0].PublicKey)
			if resolved != fromLower {
				a, b := fromLower, resolved
				if a > b {
					a, b = b, a
				}
				edges = append(edges, edgeCandidate{a, b, ts})
			}
		}
	}

	// Edge 2: observer ↔ path[last] — ALL packet types
	if pm != nil {
		lastHop := strings.ToLower(path[len(path)-1])
		candidates := pm.m[lastHop]
		if len(candidates) == 1 {
			resolved := strings.ToLower(candidates[0].PublicKey)
			if resolved != observerPK {
				a, b := observerPK, resolved
				if a > b {
					a, b = b, a
				}
				edges = append(edges, edgeCandidate{a, b, ts})
			}
		}
	}

	return edges
}

// openRW opens a read-write SQLite connection (same pattern as PruneOldPackets).
func openRW(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=10000", dbPath)
	rw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	rw.SetMaxOpenConns(1)
	return rw, nil
}
