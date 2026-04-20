package main

import (
	"database/sql"
	"hash/fnv"
	"strings"
	"sync"
)

// resolvedPubkeyHash computes a fast 64-bit hash for membership index keying.
// Uses FNV-1a from stdlib — good distribution, no external dependency.
func resolvedPubkeyHash(pk string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(strings.ToLower(pk)))
	return h.Sum64()
}

// addToResolvedPubkeyIndex adds a txID under each resolved pubkey hash.
// Deduplicates: won't add the same (hash, txID) pair twice.
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
		s.resolvedPubkeyIndex[h] = append(s.resolvedPubkeyIndex[h], txID)
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
		for i, id := range list {
			if id == txID {
				s.resolvedPubkeyIndex[h] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(s.resolvedPubkeyIndex[h]) == 0 {
			delete(s.resolvedPubkeyIndex, h)
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
	// Use LIKE with surrounding quotes to prevent substring false positives.
	// resolved_path format: ["pubkey1","pubkey2",...]
	pattern := `%"` + strings.ToLower(pubkey) + `"%`
	var count int
	err := s.db.conn.QueryRow(
		`SELECT COUNT(*) FROM observations WHERE transmission_id = ? AND resolved_path LIKE ?`,
		txID, pattern,
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
func (s *PacketStore) fetchResolvedPathForTxBest(tx *StoreTx) []*string {
	if tx == nil || len(tx.Observations) == 0 {
		return nil
	}
	best := tx.Observations[0]
	bestLen := pathLen(best.PathJSON)
	for _, obs := range tx.Observations[1:] {
		l := pathLen(obs.PathJSON)
		if l > bestLen {
			best = obs
			bestLen = l
		}
	}
	return s.fetchResolvedPathForObs(best.ID)
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
	if len(s.lruOrder) >= lruMaxSize {
		// Evict oldest
		evictID := s.lruOrder[0]
		s.lruOrder = s.lruOrder[1:]
		delete(s.apiResolvedPathLRU, evictID)
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

// resolvedPubkeysForEviction fetches resolved pubkeys for a tx from SQL
// for use during eviction cleanup of byNode/nodeHashes.
func (s *PacketStore) resolvedPubkeysForEviction(txID int) []string {
	obsMap := s.fetchResolvedPathsForTx(txID)
	seen := make(map[string]bool)
	var result []string
	for _, rp := range obsMap {
		for _, p := range rp {
			if p != nil && *p != "" && !seen[*p] {
				seen[*p] = true
				result = append(result, *p)
			}
		}
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

// Placeholder for sync.RWMutex usage check
var _ sync.RWMutex
