package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// NeighborEdgesBuilderInterval is how often the ingestor rescans
// observations and refreshes neighbor_edges. Server reads with the
// same 60s cadence (see cmd/server/neighbor_recomputer.go); a 60s
// pulse here is sufficient to keep the snapshot fresh.
const NeighborEdgesBuilderInterval = 60 * time.Second

// neighborBuilderMaxBatch caps how many observation rows a single
// delta tick may process (#1339). With max_open_conns=1, an unbounded
// scan on a multi-million-row table holds the SQLite write lock for
// minutes and starves MQTT ingest. The cap keeps each tick bounded;
// if a backlog accumulates, successive ticks drain it 50k rows at a
// time without ever blocking ingest for long.
const neighborBuilderMaxBatch = 50000

// neighborBuilderSlowTickThreshold is the per-tick wallclock budget
// for the builder. Exceeding it is logged loudly so operators can
// catch a regression of #1339 quickly. The full instrumentation
// framework is tracked in #1340.
const neighborBuilderSlowTickThreshold = 5 * time.Second

// payloadADVERT mirrors the constant in cmd/server/decoder.go.
// Duplicated rather than imported so the ingestor binary stays
// independent of the server package.
const payloadADVERT = 0x04

// payloadAnonReq mirrors PayloadANON_REQ in cmd/server/decoder.go (#1777).
// ANON_REQ carries the sender's full Ed25519 ephemeral pubkey
// (decoder.go's EphemeralPubKey field), unlike REQ/RESP/PATH/TXT which
// only carry a 1-byte truncated hash of the originator in src/dst — not
// enough to seed a trustworthy neighbor edge (~1/256 collision odds).
// ANON_REQ is therefore treated like ADVERT for the originator↔path[0]
// edge; other non-ADVERT types are deliberately excluded.
const payloadAnonReq = 0x07

// edgeRow is one row to upsert into neighbor_edges. (a, b) is already
// canonical-ordered (a <= b).
type edgeRow struct {
	a, b, ts string
}

// StartNeighborEdgesBuilder launches the periodic builder. On each
// tick it rescans recent observations + transmissions and upserts
// derived neighbor_edges rows. Builder is the only writer to
// neighbor_edges (#1287).
//
// The function returns a stop closure. Initial build runs synchronously
// before the ticker starts so the server's first snapshot load picks
// up real data instead of an empty table.
func (s *Store) StartNeighborEdgesBuilder(interval time.Duration, trust *packetpath.TrustConfig) func() {
	if interval <= 0 {
		interval = NeighborEdgesBuilderInterval
	}
	stop := make(chan struct{})
	done := make(chan struct{})

	// Synchronous warm-up: on a fresh DB this is a full scan; on a DB
	// with persisted neighbor_edges (most restarts), the watermark
	// short-circuits it into a delta scan. Loop until the per-tick
	// batch cap stops triggering so we drain any backlog before
	// returning — first server load needs a fully-populated table.
	wuStart := time.Now()
	var wuTotal int
	// Prime the prefix index (#1547) so the very first
	// InsertTransmission after startup can resolve hop prefixes.
	if err := s.RefreshPrefixIndex(); err != nil {
		log.Printf("[neighbor-build] initial prefix-index refresh error: %v", err)
	}
	// Prime the neighbor graph (#1560) so the context-aware resolver
	// has adjacency data on the very first InsertTransmission.
	if err := s.RefreshNeighborGraph(); err != nil {
		log.Printf("[neighbor-build] initial neighbor-graph refresh error: %v", err)
	}
	for {
		n, err := s.buildAndPersistNeighborEdges(trust)
		if err != nil {
			log.Printf("[neighbor-build] initial build error: %v", err)
			break
		}
		wuTotal += n
		if n < neighborBuilderMaxBatch {
			break
		}
	}
	log.Printf("[neighbor-build] initial build: %d edges upserted in %s", wuTotal, time.Since(wuStart))

	var stopOnce sync.Once
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				start := time.Now()
				// Refresh the prefix index alongside the edges build
				// (#1547) so new nodes become resolvable within a tick.
				if err := s.RefreshPrefixIndex(); err != nil {
					log.Printf("[neighbor-build] prefix-index refresh error: %v", err)
				}
				n, err := s.buildAndPersistNeighborEdges(trust)
				// Refresh the neighbor-graph snapshot after the edges
				// build (#1560) so the context-aware resolver picks up
				// newly persisted adjacencies on the next ingest.
				if grErr := s.RefreshNeighborGraph(); grErr != nil {
					log.Printf("[neighbor-build] neighbor-graph refresh error: %v", grErr)
				}
				dur := time.Since(start)
				if err != nil {
					log.Printf("[neighbor-build] tick error after %s: %v", dur, err)
				} else if n > 0 {
					log.Printf("[neighbor-build] tick: %d edges in %s (delta from watermark)", n, dur)
				}
				if dur > neighborBuilderSlowTickThreshold {
					log.Printf("[neighbor-build] SLOW tick: %s — possible regression of #1339", dur)
				}
			case <-stop:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() { close(stop) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

// buildAndPersistNeighborEdges scans transmissions + observations,
// extracts edge candidates (originator↔first-hop on ADVERTs;
// observer↔last-hop on all packet types) and upserts them into
// neighbor_edges. Returns count of attempted upserts.
//
// Watermark / delta semantics (#1339): the builder derives a watermark
// from MAX(neighbor_edges.last_seen). On an empty edges table (fresh
// DB), watermark is 0 and the builder does a full warm-up scan. On
// every subsequent call, the SELECT is restricted to observations
// whose timestamp is strictly greater than the watermark, bounded by
// neighborBuilderMaxBatch. neighbor_edges itself is the persistence —
// no metadata table or in-memory state is required, and restarts
// resume cleanly from whatever the table reflects.
//
// Trade-off (documented for #1340 follow-up): an anomalously-old
// observation that arrives AFTER its timestamp has already been
// crossed by the watermark will be skipped. Acceptable for an
// approximate neighbor graph; a periodic full-rebuild can be added
// later if needed.
//
// Resolution of hop-prefix → full pubkey is done via a one-shot
// SELECT of (lowered) pubkey prefixes from nodes. Prefixes with
// multiple candidates are skipped (matches the conservative
// resolution rule in cmd/server/extractEdgesFromObs).
func (s *Store) buildAndPersistNeighborEdges(trust *packetpath.TrustConfig) (int, error) {
	prefixIdx, err := buildPrefixIndex(s.db)
	if err != nil {
		return 0, fmt.Errorf("build prefix index: %w", err)
	}

	// Derive the watermark from the existing edges table. RFC3339
	// → epoch seconds so it can be compared against observations.timestamp
	// (stored as INTEGER unix epoch). On an empty edges table both the
	// query and the parse return zero → full warm-up scan.
	var watermarkRFC sql.NullString
	if err := s.db.QueryRow(`SELECT MAX(last_seen) FROM neighbor_edges`).Scan(&watermarkRFC); err != nil {
		return 0, fmt.Errorf("read watermark: %w", err)
	}
	var watermarkEpoch int64
	if watermarkRFC.Valid && watermarkRFC.String != "" {
		if t, parseErr := time.Parse(time.RFC3339, watermarkRFC.String); parseErr == nil {
			watermarkEpoch = t.Unix()
		}
	}

	rows, err := s.db.Query(`SELECT
		t.payload_type,
		t.decoded_json,
		COALESCE(t.from_pubkey, ''),
		COALESCE(o.path_json, ''),
		COALESCE(obs.id, '') AS observer_id,
		o.timestamp
	FROM observations o
	JOIN transmissions t ON t.id = o.transmission_id
	LEFT JOIN observers obs ON obs.rowid = o.observer_idx
	WHERE o.timestamp > ?
	ORDER BY o.timestamp
	LIMIT ?`, watermarkEpoch, neighborBuilderMaxBatch)
	if err != nil {
		return 0, fmt.Errorf("scan observations: %w", err)
	}
	defer rows.Close()

	var edges []edgeRow
	for rows.Next() {
		var payloadType sql.NullInt64
		var decodedJSON, fromPubkey, pathJSON, observerID string
		var epochTs int64
		if err := rows.Scan(&payloadType, &decodedJSON, &fromPubkey, &pathJSON, &observerID, &epochTs); err != nil {
			continue
		}
		isAdvert := payloadType.Valid && payloadType.Int64 == int64(payloadADVERT)
		isAnonReq := payloadType.Valid && payloadType.Int64 == int64(payloadAnonReq)
		// #1777: ANON_REQ's ephemeralPubKey is as trustworthy an originator
		// identity as ADVERT's pubKey — see payloadAnonReq doc comment.
		hasFullOriginator := isAdvert || isAnonReq

		fromNode := strings.ToLower(fromPubkey)
		if fromNode == "" {
			if isAdvert {
				fromNode = strings.ToLower(extractPubkeyFromAdvertJSON(decodedJSON))
			} else if isAnonReq {
				fromNode = strings.ToLower(extractPubkeyFromAnonReqJSON(decodedJSON))
			}
		}
		ts := time.Unix(epochTs, 0).UTC().Format(time.RFC3339)
		observerPK := strings.ToLower(observerID)
		path := parsePathArray(pathJSON)

		if len(path) == 0 {
			if hasFullOriginator && fromNode != "" && fromNode != observerPK && observerPK != "" {
				edges = append(edges, canonEdge(fromNode, observerPK, ts))
			}
			continue
		}
		if hasFullOriginator && fromNode != "" {
			if resolved, ok := resolvePrefix(prefixIdx, path[0], trust); ok && resolved != fromNode {
				edges = append(edges, canonEdge(fromNode, resolved, ts))
			}
		}
		if observerPK != "" {
			last := path[len(path)-1]
			if resolved, ok := resolvePrefix(prefixIdx, last, trust); ok && resolved != observerPK {
				edges = append(edges, canonEdge(observerPK, resolved, ts))
			}
		}
	}

	if len(edges) == 0 {
		return 0, nil
	}

	// Wrap the whole edge-persist tx under writer-perf instrumentation
	// (#1340). Slow neighbor-builder ticks (the #1339 root cause) now
	// show up on /api/perf under component=neighbor_builder.
	var inserted int
	err = s.WriterTx("neighbor_builder", func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen)
			VALUES (?, ?, 1, ?)
			ON CONFLICT(node_a, node_b) DO UPDATE SET
			  count = count + 1,
			  last_seen = MAX(last_seen, excluded.last_seen)`)
		if err != nil {
			return fmt.Errorf("prepare: %w", err)
		}
		defer stmt.Close()
		var firstErr error
		for _, e := range edges {
			if _, err := stmt.Exec(e.a, e.b, e.ts); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			return fmt.Errorf("upsert: %w", firstErr)
		}
		inserted = len(edges)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// canonEdge orders the pair so node_a <= node_b (matches the existing
// schema convention used by the loader and the bridge recomputer).
func canonEdge(a, b, ts string) edgeRow {
	if a > b {
		a, b = b, a
	}
	return edgeRow{a, b, ts}
}

// parsePathArray returns the hop strings from a path_json blob.
// Defensive against missing/invalid JSON.
func parsePathArray(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var arr []string
	if json.Unmarshal([]byte(s), &arr) != nil {
		return nil
	}
	return arr
}

// prefixIndex maps a hop prefix (lowercase) → all full pubkeys whose
// public_key starts with that prefix. Prefixes with > 1 candidate are
// considered ambiguous and skipped during resolution.
type prefixIndex map[string][]string

// buildPrefixIndex reads nodes.public_key and builds the prefix → pubkey
// map. We index every 1-byte (2 hex char) prefix length the firmware
// uses (1, 2, 3, 4, 6, 8). Memory cost is O(nodes × len(prefixLens)).
func buildPrefixIndex(db *sql.DB) (prefixIndex, error) {
	rows, err := db.Query(`SELECT public_key FROM nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := make(prefixIndex, 1024)
	var prefixLens = []int{1 * 2, 2 * 2, 3 * 2, 4 * 2, 6 * 2, 8 * 2}
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			continue
		}
		pkLower := strings.ToLower(pk)
		for _, n := range prefixLens {
			if len(pkLower) < n {
				continue
			}
			prefix := pkLower[:n]
			idx[prefix] = append(idx[prefix], pkLower)
		}
	}
	return idx, nil
}

// resolvePrefix returns the single resolved pubkey if exactly one
// candidate matches, otherwise (zero || multiple), it returns ok=false
// (matches the conservative server-side resolver in
// cmd/server/extractEdgesFromObs).
func resolvePrefix(idx prefixIndex, hop string, trust *packetpath.TrustConfig) (string, bool) {
	// #1784: gate on prefix length before consulting the index. A hop
	// hash short enough to fall below the operator's trust threshold is
	// not mapping evidence even when it happens to resolve to exactly
	// one candidate today — uniqueness is relative to the nodes we
	// currently know about, so an unknown or newly joined repeater
	// sharing that prefix silently turns it into a wrong edge. Gating
	// here rather than at each call site means every consumer of the
	// resolver (originator edge, observer edge, and any future hop
	// pair) inherits the threshold automatically.
	if !packetpath.MeetsPathTrust(len(hop)/2, trust) {
		return "", false
	}
	h := strings.ToLower(hop)
	candidates := idx[h]
	if len(candidates) != 1 {
		return "", false
	}
	return candidates[0], true
}
