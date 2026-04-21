package main

// Lock ordering contract (MUST be followed everywhere):
//
//   s.mu  →  s.lruMu   (s.mu is the outer lock, lruMu is the inner lock)
//
// • Never acquire s.lruMu while holding s.mu.
// • fetchResolvedPathForObs takes lruMu independently — callers under s.mu
//   must NOT call it directly; instead collect IDs under s.mu, release, then
//   do LRU ops under lruMu separately.
// • The backfill path (backfillResolvedPathsAsync) follows this by collecting
//   obsIDs to invalidate under s.mu, releasing it, then taking lruMu.

import (
	"database/sql"
	"hash/fnv"
	"log"
	"strings"
)

// resolvedPubkeyHash computes a fast 64-bit hash for membership index keying.
// Uses FNV-1a from stdlib — good distribution, no external dependency.
func resolvedPubkeyHash(pk string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(strings.ToLower(pk)))
	return h.Sum64()
}

// addToResolvedPubkeyIndex adds a txID under each resolved pubkey hash.
// Deduplicates both within a single call AND across calls — won't add the
// same (hash, txID) pair twice even when called multiple times for the same tx.
// Must be called under s.mu write lock.
func (s *PacketStore) addToResolvedPubkeyIndex(txID int, resolvedPubkeys []string) {
	if !s.useResolvedPathIndex {
		return
	}
	seen := make(map[uint64]bool, len(resolvedPubkeys))
	for _, pk := range resolvedPubkeys {
		if pk == "" {
			continue
		}
		h := resolvedPubkeyHash(pk)
		if seen[h] {
			continue
		}
		seen[h] = true

		// Cross-call dedup: check if (h, txID) already exists in forward index.
		existing := s.resolvedPubkeyIndex[h]
		alreadyPresent := false
		for _, id := range existing {
			if id == txID {
				alreadyPresent = true
				break
			}
		}
		if alreadyPresent {
			continue
		}

		s.resolvedPubkeyIndex[h] = append(existing, txID)
		s.resolvedPubkeyReverse[txID] = append(s.resolvedPubkeyReverse[txID], h)
	}
}

// removeFromResolvedPubkeyIndex removes all index entries for a txID using the reverse map.
// Must be called under s.mu write lock.
func (s *PacketStore) removeFromResolvedPubkeyIndex(txID int) {
	if !s.useResolvedPathIndex {
		return
	}
	hashes := s.resolvedPubkeyReverse[txID]
	for _, h := range hashes {
		list := s.resolvedPubkeyIndex[h]
		// Remove ALL occurrences of txID (not just the first) to prevent orphans.
		filtered := list[:0]
		for _, id := range list {
			if id != txID {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) == 0 {
			delete(s.resolvedPubkeyIndex, h)
		} else {
			s.resolvedPubkeyIndex[h] = filtered
		}
	}
	delete(s.resolvedPubkeyReverse, txID)
}

// extractResolvedPubkeys extracts all non-nil, non-empty pubkeys from a resolved path.
func extractResolvedPubkeys(rp []*string) []string {
	if len(rp) == 0 {
		return nil
	}
	result := make([]string, 0, len(rp))
	for _, p := range rp {
		if p != nil && *p != "" {
			result = append(result, *p)
		}
	}
	return result
}

// mergeResolvedPubkeys collects unique non-empty pubkeys from multiple resolved paths.
func mergeResolvedPubkeys(paths ...[]*string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, rp := range paths {
		for _, p := range rp {
			if p != nil && *p != "" && !seen[*p] {
				seen[*p] = true
				result = append(result, *p)
			}
		}
	}
	return result
}

// nodeInResolvedPathViaIndex checks whether a transmission is associated with
// a target pubkey using the membership index + collision-safety SQL check.
// Must be called under s.mu RLock at minimum.
func (s *PacketStore) nodeInResolvedPathViaIndex(tx *StoreTx, targetPK string) bool {
	if !s.useResolvedPathIndex {
		// Flag off: can't disambiguate, keep candidate (conservative)
		return true
	}

	// If this tx has no indexed pubkeys at all, we can't disambiguate —
	// keep the candidate (same as old behavior for NULL resolved_path).
	if _, hasReverse := s.resolvedPubkeyReverse[tx.ID]; !hasReverse {
		return true
	}

	h := resolvedPubkeyHash(targetPK)
	txIDs := s.resolvedPubkeyIndex[h]

	// Check if this tx's ID is in the candidate list
	for _, id := range txIDs {
		if id == tx.ID {
			// Found in index. Collision-safety: verify with SQL.
			if s.db != nil && s.db.conn != nil {
				return s.confirmResolvedPathContains(tx.ID, targetPK)
			}
			return true // no DB, trust the index
		}
	}

	return false
}

// confirmResolvedPathContains verifies an exact pubkey match in resolved_path
// via SQL. This is the collision-safety fallback for the membership index.
func (s *PacketStore) confirmResolvedPathContains(txID int, pubkey string) bool {
	if s.db == nil || s.db.conn == nil {
		return true
	}
	// Use INSTR with surrounding quotes for exact match — avoids LIKE escape issues.
	// resolved_path format: ["pubkey1","pubkey2",...]
	needle := `"` + strings.ToLower(pubkey) + `"`
	var count int
	err := s.db.conn.QueryRow(
		`SELECT COUNT(*) FROM observations WHERE transmission_id = ? AND INSTR(LOWER(resolved_path), ?) > 0`,
		txID, needle,
	).Scan(&count)
	if err != nil {
		return true // on error, keep the candidate
	}
	return count > 0
}

// fetchResolvedPathsForTx fetches resolved_path from SQLite for all observations
// of a transmission. Used for on-demand API responses and eviction cleanup.
func (s *PacketStore) fetchResolvedPathsForTx(txID int) map[int][]*string {
	if s.db == nil || s.db.conn == nil {
		return nil
	}
	rows, err := s.db.conn.Query(
		`SELECT id, resolved_path FROM observations WHERE transmission_id = ? AND resolved_path IS NOT NULL`,
		txID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[int][]*string)
	for rows.Next() {
		var obsID int
		var rpJSON sql.NullString
		if err := rows.Scan(&obsID, &rpJSON); err != nil {
			continue
		}
		if rpJSON.Valid && rpJSON.String != "" {
			result[obsID] = unmarshalResolvedPath(rpJSON.String)
		}
	}
	return result
}

// fetchResolvedPathForObs fetches resolved_path for a single observation,
// using the LRU cache.
func (s *PacketStore) fetchResolvedPathForObs(obsID int) []*string {
	if s.db == nil || s.db.conn == nil {
		return nil
	}

	// Check LRU cache first
	s.lruMu.RLock()
	if s.apiResolvedPathLRU != nil {
		if entry, ok := s.apiResolvedPathLRU[obsID]; ok {
			s.lruMu.RUnlock()
			return entry
		}
	}
	s.lruMu.RUnlock()

	var rpJSON sql.NullString
	err := s.db.conn.QueryRow(
		`SELECT resolved_path FROM observations WHERE id = ?`, obsID,
	).Scan(&rpJSON)
	if err != nil || !rpJSON.Valid {
		return nil
	}
	rp := unmarshalResolvedPath(rpJSON.String)

	// Store in LRU
	s.lruMu.Lock()
	s.lruPut(obsID, rp)
	s.lruMu.Unlock()

	return rp
}

// fetchResolvedPathForTxBest returns the best observation's resolved_path for a tx.
//
// "Best" = the longest path_json among observations that actually have a stored
// resolved_path. Earlier versions picked the longest-path obs unconditionally
// and queried SQL for that single ID — if the longest-path obs had NULL
// resolved_path while a shorter sibling had one, the call returned nil and
// callers (e.g. /api/nodes/{pk}/health.recentPackets) lost the field. Fixes
// #810 by checking all observations and falling back to the longest sibling
// that has a stored path.
func (s *PacketStore) fetchResolvedPathForTxBest(tx *StoreTx) []*string {
	if tx == nil || len(tx.Observations) == 0 {
		return nil
	}
	// Fast path: try the longest-path obs first via the LRU/SQL helper.
	longest := tx.Observations[0]
	longestLen := pathLen(longest.PathJSON)
	for _, obs := range tx.Observations[1:] {
		if l := pathLen(obs.PathJSON); l > longestLen {
			longest = obs
			longestLen = l
		}
	}
	if rp := s.fetchResolvedPathForObs(longest.ID); rp != nil {
		return rp
	}
	// Fallback: longest-path obs has no stored resolved_path. Query all
	// observations for this tx and pick the one with the longest path_json
	// that actually has a stored resolved_path.
	rpMap := s.fetchResolvedPathsForTx(tx.ID)
	if len(rpMap) == 0 {
		return nil
	}
	var bestRP []*string
	bestObsID := 0
	bestLen := -1
	for _, obs := range tx.Observations {
		rp, ok := rpMap[obs.ID]
		if !ok || rp == nil {
			continue
		}
		if l := pathLen(obs.PathJSON); l > bestLen {
			bestLen = l
			bestRP = rp
			bestObsID = obs.ID
		}
	}
	// Populate LRU so repeat lookups for this tx don't re-issue the multi-row
	// SQL fallback (e.g. dashboard polling /api/nodes/{pk}/health).
	if bestRP != nil && bestObsID != 0 {
		s.lruMu.Lock()
		s.lruPut(bestObsID, bestRP)
		s.lruMu.Unlock()
	}
	return bestRP
}

// --- Simple LRU cache for resolved paths ---

const lruMaxSize = 10000

// lruPut adds an entry. Must be called under s.lruMu write lock.
func (s *PacketStore) lruPut(obsID int, rp []*string) {
	if s.apiResolvedPathLRU == nil {
		return
	}
	if _, exists := s.apiResolvedPathLRU[obsID]; exists {
		return
	}
	// Compact lruOrder if stale entries exceed 50% of capacity.
	// This prevents effective capacity degradation after bulk deletions.
	if len(s.lruOrder) >= lruMaxSize && len(s.apiResolvedPathLRU) < lruMaxSize/2 {
		compacted := make([]int, 0, len(s.apiResolvedPathLRU))
		for _, id := range s.lruOrder {
			if _, ok := s.apiResolvedPathLRU[id]; ok {
				compacted = append(compacted, id)
			}
		}
		s.lruOrder = compacted
	}
	if len(s.lruOrder) >= lruMaxSize {
		// Evict oldest, skipping stale entries
		for len(s.lruOrder) > 0 {
			evictID := s.lruOrder[0]
			s.lruOrder = s.lruOrder[1:]
			if _, ok := s.apiResolvedPathLRU[evictID]; ok {
				delete(s.apiResolvedPathLRU, evictID)
				break
			}
			// stale entry — skip and continue
		}
	}
	s.apiResolvedPathLRU[obsID] = rp
	s.lruOrder = append(s.lruOrder, obsID)
}

// lruDelete removes an entry. Must be called under s.lruMu write lock.
func (s *PacketStore) lruDelete(obsID int) {
	if s.apiResolvedPathLRU == nil {
		return
	}
	delete(s.apiResolvedPathLRU, obsID)
	// Don't scan lruOrder — eviction handles stale entries naturally.
}

// resolvedPubkeysForEvictionBatch fetches resolved pubkeys for multiple txIDs
// from SQL in a single batched query. Returns a map from txID to unique pubkeys.
// MUST be called WITHOUT holding s.mu — this is the whole point of the batch approach.
// Chunks queries to stay under SQLite's 500-parameter limit.
func (s *PacketStore) resolvedPubkeysForEvictionBatch(txIDs []int) map[int][]string {
	result := make(map[int][]string, len(txIDs))
	if len(txIDs) == 0 || s.db == nil || s.db.conn == nil {
		return result
	}

	const chunkSize = 499 // SQLite SQLITE_MAX_VARIABLE_NUMBER default is 999; stay well under
	for start := 0; start < len(txIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(txIDs) {
			end = len(txIDs)
		}
		chunk := txIDs[start:end]

		// Build query with placeholders
		placeholders := make([]byte, 0, len(chunk)*2)
		args := make([]interface{}, len(chunk))
		for i, id := range chunk {
			if i > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, '?')
			args[i] = id
		}

		query := "SELECT transmission_id, resolved_path FROM observations WHERE transmission_id IN (" +
			string(placeholders) + ") AND resolved_path IS NOT NULL"

		rows, err := s.db.conn.Query(query, args...)
		if err != nil {
			continue
		}

		for rows.Next() {
			var txID int
			var rpJSON sql.NullString
			if err := rows.Scan(&txID, &rpJSON); err != nil {
				continue
			}
			if !rpJSON.Valid || rpJSON.String == "" {
				continue
			}
			rp := unmarshalResolvedPath(rpJSON.String)
			for _, p := range rp {
				if p != nil && *p != "" {
					result[txID] = append(result[txID], *p)
				}
			}
		}
		rows.Close()
	}

	// Deduplicate per-txID
	for txID, pks := range result {
		seen := make(map[string]bool, len(pks))
		deduped := pks[:0]
		for _, pk := range pks {
			if !seen[pk] {
				seen[pk] = true
				deduped = append(deduped, pk)
			}
		}
		result[txID] = deduped
	}

	return result
}

// initResolvedPathIndex initializes the resolved path index data structures.
func (s *PacketStore) initResolvedPathIndex() {
	s.resolvedPubkeyIndex = make(map[uint64][]int, 4096)
	s.resolvedPubkeyReverse = make(map[int][]uint64, 4096)
	s.apiResolvedPathLRU = make(map[int][]*string, lruMaxSize)
	s.lruOrder = make([]int, 0, lruMaxSize)
}

// CompactResolvedPubkeyIndex reclaims memory from the resolved pubkey index maps
// after eviction. It removes empty forward-index entries (shouldn't exist if
// removeFromResolvedPubkeyIndex is correct, but defense in depth) and clips
// oversized slice backing arrays where cap > 2*len.
// Must be called under s.mu write lock.
func (s *PacketStore) CompactResolvedPubkeyIndex() {
	if !s.useResolvedPathIndex {
		return
	}
	for h, ids := range s.resolvedPubkeyIndex {
		if len(ids) == 0 {
			delete(s.resolvedPubkeyIndex, h)
			continue
		}
		// Clip oversized backing arrays: if cap > 2*len, reallocate.
		if cap(ids) > 2*len(ids)+8 {
			clipped := make([]int, len(ids))
			copy(clipped, ids)
			s.resolvedPubkeyIndex[h] = clipped
		}
	}
	for txID, hashes := range s.resolvedPubkeyReverse {
		if len(hashes) == 0 {
			delete(s.resolvedPubkeyReverse, txID)
			continue
		}
		if cap(hashes) > 2*len(hashes)+8 {
			clipped := make([]uint64, len(hashes))
			copy(clipped, hashes)
			s.resolvedPubkeyReverse[txID] = clipped
		}
	}
}

// defaultMaxResolvedPubkeyIndexEntries is the default hard cap for the forward
// index. When exceeded, a warning is logged. No auto-eviction — that's the
// eviction ticker's job.
const defaultMaxResolvedPubkeyIndexEntries = 5_000_000

// CheckResolvedPubkeyIndexSize logs a warning if the resolved pubkey forward
// index exceeds the configured maximum entries. Must be called under s.mu
// read lock at minimum.
func (s *PacketStore) CheckResolvedPubkeyIndexSize() {
	if !s.useResolvedPathIndex {
		return
	}
	maxEntries := s.maxResolvedPubkeyIndexEntries
	if maxEntries <= 0 {
		maxEntries = defaultMaxResolvedPubkeyIndexEntries
	}
	fwdLen := len(s.resolvedPubkeyIndex)
	revLen := len(s.resolvedPubkeyReverse)
	if fwdLen > maxEntries || revLen > maxEntries {
		log.Printf("[store] WARNING: resolvedPubkeyIndex size exceeds limit — forward=%d reverse=%d limit=%d",
			fwdLen, revLen, maxEntries)
	}
}
