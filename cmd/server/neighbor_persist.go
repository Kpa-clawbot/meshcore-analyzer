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


// backfillResolvedPathsAsync processes observations with NULL resolved_path in
// chunks, yielding between batches so HTTP handlers remain responsive. It sets
// store.backfillComplete when finished and re-picks best observations for any
// transmissions affected by newly resolved paths.
func backfillResolvedPathsAsync(store *PacketStore, dbPath string, chunkSize int, yieldDuration time.Duration, backfillHours int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[store] backfillResolvedPathsAsync panic recovered: %v", r)
		}
	}()
	// Collect ALL pending obs refs upfront in one pass under a single RLock (fix A).
	type obsRef struct {
		obsID       int
		pathJSON    string
		observerID  string
		txJSON      string
		payloadType *int
		txHash      string // to re-pick best obs
	}

	cutoff := time.Now().UTC().Add(-time.Duration(backfillHours) * time.Hour)

	store.mu.RLock()
	pm := store.nodePM
	var allPending []obsRef
	for _, tx := range store.packets {
		// Skip transmissions older than the backfill window.
		if tx.FirstSeen != "" {
			if ts, err := time.Parse(time.RFC3339Nano, tx.FirstSeen); err == nil && ts.Before(cutoff) {
				continue
			}
			// Also try the common SQLite format
			if ts, err := time.Parse("2006-01-02 15:04:05", tx.FirstSeen); err == nil && ts.Before(cutoff) {
				continue
			}
		}
		for _, obs := range tx.Observations {
			if obs.ResolvedPath == nil && obs.PathJSON != "" && obs.PathJSON != "[]" {
				allPending = append(allPending, obsRef{
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

	totalPending := len(allPending)
	if totalPending == 0 || pm == nil {
		store.backfillComplete.Store(true)
		log.Printf("[store] async resolved_path backfill: nothing to do")
		return
	}

	store.backfillTotal.Store(int64(totalPending))
	store.backfillProcessed.Store(0)
	log.Printf("[store] async resolved_path backfill starting: %d observations", totalPending)

	// Open RW connection once before the chunk loop (fix B).
	var rw *sql.DB
	if dbPath != "" {
		var err error
		rw, err = openRW(dbPath)
		if err != nil {
			log.Printf("[store] async backfill: open rw error: %v", err)
		}
	}
	defer func() {
		if rw != nil {
			rw.Close()
		}
	}()

	totalProcessed := 0
	for totalProcessed < totalPending {
		end := totalProcessed + chunkSize
		if end > totalPending {
			end = totalPending
		}
		chunk := allPending[totalProcessed:end]

		// Re-read graph under RLock at the start of each chunk so we pick up
		// a freshly-built graph once the background build goroutine completes,
		// instead of using the potentially-empty graph captured at cold start.
		store.mu.RLock()
		graph := store.graph
		store.mu.RUnlock()

		// Resolve paths outside any lock.
		type resolved struct {
			obsID  int
			rp     []*string
			rpJSON string
			txHash string
		}
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

		// Persist to SQLite using the shared connection.
		if len(results) > 0 && rw != nil {
			sqlTx, err := rw.Begin()
			if err != nil {
				log.Printf("[store] async backfill: begin tx error: %v", err)
			} else {
				stmt, err := sqlTx.Prepare("UPDATE observations SET resolved_path = ? WHERE id = ?")
				if err != nil {
					log.Printf("[store] async backfill: prepare error: %v", err)
					sqlTx.Rollback()
				} else {
					var execErr error
					for _, r := range results {
						if _, e := stmt.Exec(r.rpJSON, r.obsID); e != nil && execErr == nil {
							execErr = e
						}
					}
					if execErr != nil {
						log.Printf("[store] async backfill: exec error (first): %v", execErr)
					}
					stmt.Close()
					if err := sqlTx.Commit(); err != nil {
						log.Printf("[store] async backfill: commit error: %v", err)
					}
				}
			}

			// Update in-memory state and re-pick best observation under a single
			// write lock. The per-tx pickBestObservation is O(observations) which is
			// typically <10 per tx — negligible cost vs. the race risk of splitting
			// the lock (pollAndMerge can append to tx.Observations concurrently).
			store.mu.Lock()
			affectedSet := make(map[string]bool)
			for _, r := range results {
				if obs, ok := store.byObsID[r.obsID]; ok {
					obs.ResolvedPath = r.rp
				}
				if !affectedSet[r.txHash] {
					affectedSet[r.txHash] = true
					if tx, ok := store.byHash[r.txHash]; ok {
						pickBestObservation(tx)
						// tx.ResolvedPath is now updated; add newly resolved pubkeys to
						// the relay time index. The first call during buildPathHopIndex
						// was a no-op (ResolvedPath was nil then), so no duplicates.
						addTxToRelayTimeIndex(store.relayTimes, tx)
					}
				}
			}
			store.mu.Unlock()
		}

		totalProcessed += len(chunk)
		store.backfillProcessed.Store(int64(totalProcessed))
		pct := float64(totalProcessed) / float64(totalPending) * 100
		log.Printf("[store] backfill progress: %d/%d observations (%.1f%%)", totalProcessed, totalPending, pct)

		time.Sleep(yieldDuration)
	}

	store.backfillComplete.Store(true)
	log.Printf("[store] async resolved_path backfill complete: %d observations processed", totalProcessed)
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
	isAdvert := tx.PayloadType != nil && *tx.PayloadType == PayloadADVERT
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
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL", dbPath)
	rw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	rw.SetMaxOpenConns(1)
	// DSN _busy_timeout may not be honored by all drivers; set via PRAGMA
	// to guarantee SQLite retries for up to 5s before returning SQLITE_BUSY.
	if _, err := rw.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		rw.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	return rw, nil
}

// PruneNeighborEdges removes edges older than maxAgeDays from both SQLite and
// the in-memory graph. Uses openRW internally because the shared database.conn
// is opened with mode=ro and DELETEs would silently fail.
func PruneNeighborEdges(dbPath string, graph *NeighborGraph, maxAgeDays int) (int, error) {
	cutoff := time.Now().UTC().Add(-time.Duration(maxAgeDays) * 24 * time.Hour)

	// 1. Prune from SQLite using a read-write connection
	var dbPruned int64
	rw, err := openRW(dbPath)
	if err != nil {
		return 0, fmt.Errorf("prune neighbor_edges: open rw: %w", err)
	}
	defer rw.Close()
	res, err := rw.Exec("DELETE FROM neighbor_edges WHERE last_seen < ?", cutoff.Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("prune neighbor_edges: %w", err)
	}
	dbPruned, _ = res.RowsAffected()

	// 2. Prune from in-memory graph
	memPruned := 0
	if graph != nil {
		memPruned = graph.PruneOlderThan(cutoff)
	}

	if dbPruned > 0 || memPruned > 0 {
		log.Printf("[neighbor-prune] removed %d DB rows, %d in-memory edges older than %d days", dbPruned, memPruned, maxAgeDays)
	}
	return int(dbPruned), nil
}
