package main

import (
	"log"
	"time"
)

// migrateContentHashesAsync recomputes content hashes in batches after the
// server is already serving HTTP.  Packets whose hash changes are updated in
// both the DB and the in-memory byHash index.  The migration is idempotent:
// once all hashes match the current formula it completes instantly.
func migrateContentHashesAsync(store *PacketStore, batchSize int, yieldDuration time.Duration) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[hash-migrate] panic recovered: %v", r)
		}
		store.hashMigrationComplete.Store(true)
	}()

	// Snapshot the packet slice length under lock (packets only grow).
	store.mu.RLock()
	total := len(store.packets)
	store.mu.RUnlock()

	migrated := 0
	for offset := 0; offset < total; offset += batchSize {
		end := offset + batchSize
		if end > total {
			end = total
		}

		// Collect stale hashes in this batch under RLock.
		type hashUpdate struct {
			tx      *StoreTx
			oldHash string
			newHash string
		}
		var updates []hashUpdate

		store.mu.RLock()
		for _, tx := range store.packets[offset:end] {
			if tx.RawHex == "" {
				continue
			}
			newHash := ComputeContentHash(tx.RawHex)
			if newHash != tx.Hash {
				updates = append(updates, hashUpdate{tx: tx, oldHash: tx.Hash, newHash: newHash})
			}
		}
		store.mu.RUnlock()

		if len(updates) == 0 {
			continue
		}

		// Write batch to DB in a single transaction.
		dbTx, err := store.db.conn.Begin()
		if err != nil {
			log.Printf("[hash-migrate] begin tx: %v", err)
			continue
		}
		stmt, err := dbTx.Prepare("UPDATE transmissions SET hash = ? WHERE id = ?")
		if err != nil {
			log.Printf("[hash-migrate] prepare: %v", err)
			dbTx.Rollback()
			continue
		}

		for _, u := range updates {
			if _, err := stmt.Exec(u.newHash, u.tx.ID); err != nil {
				// UNIQUE constraint = two old hashes map to the same new hash (duplicate).
				// Merge observations to the surviving tx, delete the duplicate.
				log.Printf("[hash-migrate] tx %d collides — merging duplicate", u.tx.ID)
				var survID int
				if err2 := dbTx.QueryRow("SELECT id FROM transmissions WHERE hash = ?", u.newHash).Scan(&survID); err2 == nil {
					dbTx.Exec("UPDATE observations SET transmission_id = ? WHERE transmission_id = ?", survID, u.tx.ID)
					dbTx.Exec("DELETE FROM transmissions WHERE id = ?", u.tx.ID)
					u.newHash = "" // mark for in-memory removal only
				}
			}
		}
		stmt.Close()

		if err := dbTx.Commit(); err != nil {
			log.Printf("[hash-migrate] commit: %v", err)
			continue
		}

		// Update in-memory index under write lock.
		store.mu.Lock()
		for _, u := range updates {
			delete(store.byHash, u.oldHash)
			if u.newHash == "" {
				// Merged duplicate — remove from packets slice and indexes.
				delete(store.byTxID, u.tx.ID)
				// Move observations to survivor if present.
				if surv := store.byHash[ComputeContentHash(u.tx.RawHex)]; surv != nil {
					for _, obs := range u.tx.Observations {
						surv.Observations = append(surv.Observations, obs)
						surv.ObservationCount++
					}
				}
			} else {
				u.tx.Hash = u.newHash
				store.byHash[u.newHash] = u.tx
			}
		}
		store.mu.Unlock()

		migrated += len(updates)

		// Yield to let HTTP handlers run.
		time.Sleep(yieldDuration)
	}

	if migrated > 0 {
		log.Printf("[hash-migrate] Migrated %d content hashes to new formula", migrated)
	}
}
