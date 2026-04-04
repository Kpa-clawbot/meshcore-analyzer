package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

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

// persistEdge upserts a single edge into the neighbor_edges SQLite table.
func persistEdge(rw *sql.DB, nodeA, nodeB string, now string) {
	a, b := nodeA, nodeB
	if a > b {
		a, b = b, a
	}
	_, err := rw.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(node_a, node_b) DO UPDATE SET
		count = count + 1, last_seen = excluded.last_seen`,
		a, b, now)
	if err != nil {
		log.Printf("[neighbor] persist edge error: %v", err)
	}
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
	for _, pkt := range packets {
		isAdvert := pkt.PayloadType != nil && *pkt.PayloadType == 4
		fromNode := extractFromNode(pkt)

		for _, obs := range pkt.Observations {
			path := parsePathJSON(obs.PathJSON)
			observerPK := strings.ToLower(obs.ObserverID)
			ts := obs.Timestamp

			if len(path) == 0 {
				if isAdvert && fromNode != "" {
					fromLower := strings.ToLower(fromNode)
					if fromLower != observerPK {
						a, b := fromLower, observerPK
						if a > b {
							a, b = b, a
						}
						stmt.Exec(a, b, ts)
						edgeCount++
					}
				}
				continue
			}

			// Edge 1: originator ↔ path[0] — ADVERTs only (resolve prefix to full pubkey)
			if isAdvert && fromNode != "" {
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
						stmt.Exec(a, b, ts)
						edgeCount++
					}
				}
			}

			// Edge 2: observer ↔ path[last] — ALL packet types
			lastHop := strings.ToLower(path[len(path)-1])
			candidates := pm.m[lastHop]
			if len(candidates) == 1 {
				resolved := strings.ToLower(candidates[0].PublicKey)
				if resolved != observerPK {
					a, b := observerPK, resolved
					if a > b {
						a, b = b, a
					}
					stmt.Exec(a, b, ts)
					edgeCount++
				}
			}
		}
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

	for _, r := range results {
		stmt.Exec(r.rpJSON, r.obsID)
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

// ─── Shared helpers ────────────────────────────────────────────────────────────

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
