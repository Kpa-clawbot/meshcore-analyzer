package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

// PruneOldPackets deletes transmissions (and their child observations)
// older than `days`. Returns count of transmissions deleted.
//
// Owned by the ingestor per #1283: the writer process is the only one
// allowed to hold the DB write lock; previously this lived in
// cmd/server/db.go and raced ingestor INSERTs (SQLITE_BUSY).
func (s *Store) PruneOldPackets(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)

	// Tagged for writer-perf visibility (#1340).
	var n int64
	err := s.WriterTx("prune_packets", func(tx *sql.Tx) error {
		// Delete child observations first (no CASCADE in SQLite).
		if _, err := tx.Exec(`DELETE FROM observations WHERE transmission_id IN (
			SELECT id FROM transmissions WHERE first_seen < ?
		)`, cutoff); err != nil {
			return fmt.Errorf("prune observations: %w", err)
		}

		res, err := tx.Exec(`DELETE FROM transmissions WHERE first_seen < ?`, cutoff)
		if err != nil {
			return fmt.Errorf("prune transmissions: %w", err)
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		log.Printf("[prune] deleted %d transmissions older than %d days", n, days)
	}
	return n, nil
}

// PruneOldClientReceptions deletes mobile client-RX coverage rows older than
// `days` (by rx_at), and client_observers (companion names) whose last_seen has
// aged out. This bounds the otherwise-unbounded client_receptions table the
// opt-in coverage feature feeds. 0 disables. Owned by the ingestor writer
// (#1283). Returns the number of client_receptions rows deleted.
func (s *Store) PruneOldClientReceptions(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)

	var n int64
	err := s.WriterTx("prune_client_receptions", func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM client_receptions WHERE rx_at < ?`, cutoff)
		if err != nil {
			return fmt.Errorf("prune client_receptions: %w", err)
		}
		n, _ = res.RowsAffected()
		// Drop companion name rows not refreshed within the window.
		if _, err := tx.Exec(`DELETE FROM client_observers WHERE last_seen < ?`, cutoff); err != nil {
			return fmt.Errorf("prune client_observers: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if n > 0 {
		log.Printf("[prune] deleted %d client_receptions older than %d days", n, days)
	}
	return n, nil
}

// PruneOldClientRxObservations deletes diagnostic client_rx_observations rows
// older than `days` (by rx_at). Unlike client_receptions this table is
// diagnostic, not archival, so it gets its own (typically shorter) window.
// 0 disables. Owned by the ingestor writer (#1283). rx_at on this table is
// stored at millisecond precision via rxTimeMillisLayout, not RFC3339 — the
// cutoff must be formatted the same way so lexicographic comparison against
// stored values stays correct.
func (s *Store) PruneOldClientRxObservations(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(rxTimeMillisLayout)
	res, err := s.db.Exec(`DELETE FROM client_rx_observations WHERE rx_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune client_rx_observations: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("[prune] deleted %d client_rx_observations older than %d days", n, days)
	}
	return n, nil
}

// PruneOldClientRfSamples deletes RF environment sample rows older than
// `days` (by sampled_at). Diagnostic, not archival, so it gets its own
// window, independent of clientRxDays/clientRxObsDays. 0 disables. Owned by
// the ingestor writer (#1283). sampled_at on this table is stored at
// millisecond precision via rxTimeMillisLayout, not RFC3339 — the cutoff
// must be formatted the same way so lexicographic comparison against
// stored values stays correct.
func (s *Store) PruneOldClientRfSamples(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(rxTimeMillisLayout)
	res, err := s.db.Exec(`DELETE FROM client_rf_samples WHERE sampled_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune client_rf_samples: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("[prune] deleted %d client_rf_samples older than %d days", n, days)
	}
	return n, nil
}

// SoftDeleteBlacklistedObservers marks observers in the blacklist as
// inactive=1 so they are hidden from API responses. Owned by ingestor
// per #1287. Runs once at startup.
func (s *Store) SoftDeleteBlacklistedObservers(blacklist []string) {
	n, err := dbschema.SoftDeleteBlacklistedObservers(s.db, blacklist)
	if err != nil {
		log.Printf("[observer-blacklist] warning: soft-delete failed: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[observer-blacklist] soft-deleted %d blacklisted observer(s)", n)
	}
}

// PruneNeighborEdges deletes rows older than maxAgeDays from
// neighbor_edges. Owned by the ingestor per #1287 (was in cmd/server).
// Returns DB rows deleted.
func (s *Store) PruneNeighborEdges(maxAgeDays int) (int64, error) {
	if maxAgeDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(maxAgeDays) * 24 * time.Hour).Format(time.RFC3339)
	res, err := s.db.Exec("DELETE FROM neighbor_edges WHERE last_seen < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune neighbor_edges: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("[neighbor-prune] removed %d DB rows older than %d days", n, maxAgeDays)
	}
	return n, nil
}

// ─── from_pubkey backfill (#1143) ──────────────────────────────────────────
//
// Moved from cmd/server/from_pubkey_migration.go in #1287. Runs from the
// ingestor's maintenance loop. Populates transmissions.from_pubkey for
// ADVERT rows whose value is still NULL, by parsing decoded_json.pubKey.

// FromPubkeyBackfillStats holds progress for /api/healthz exposure.
// The ingestor exposes these via stats_file.go so the server can read
// them without writing.
type FromPubkeyBackfillStats struct {
	Total     int64 `json:"total"`
	Processed int64 `json:"processed"`
	Done      bool  `json:"done"`
}

// BackfillFromPubkey scans transmissions where from_pubkey IS NULL and
// payload_type = 4 (ADVERT) and populates from_pubkey from decoded_json.
// Chunked + yields between batches. Safe to call repeatedly; once a row
// is set to either "" or hex it never matches the WHERE clause again.
func (s *Store) BackfillFromPubkey(chunkSize int, yieldDuration time.Duration, progress func(total, processed int64, done bool)) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[backfill] from_pubkey panic recovered: %v", r)
		}
		if progress != nil {
			progress(0, 0, true) // signal done; values overwritten below if collected
		}
	}()
	if chunkSize <= 0 {
		chunkSize = 5000
	}

	var total int64
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM transmissions WHERE from_pubkey IS NULL AND payload_type = 4",
	).Scan(&total); err != nil {
		log.Printf("[backfill] from_pubkey count error: %v", err)
		return
	}
	if total == 0 {
		log.Println("[backfill] from_pubkey: nothing to do")
		if progress != nil {
			progress(0, 0, true)
		}
		return
	}
	if progress != nil {
		progress(total, 0, false)
	}
	log.Printf("[backfill] from_pubkey starting: %d ADVERT rows", total)

	stmt, err := s.db.Prepare("UPDATE transmissions SET from_pubkey = ? WHERE id = ?")
	if err != nil {
		log.Printf("[backfill] from_pubkey prepare: %v", err)
		return
	}
	defer stmt.Close()

	var processed int64
	for {
		rows, err := s.db.Query(
			"SELECT id, decoded_json FROM transmissions WHERE from_pubkey IS NULL AND payload_type = 4 LIMIT ?",
			chunkSize)
		if err != nil {
			log.Printf("[backfill] from_pubkey select: %v", err)
			return
		}
		type row struct {
			id int64
			pk string
		}
		batch := make([]row, 0, chunkSize)
		for rows.Next() {
			var id int64
			var dj sql.NullString
			if err := rows.Scan(&id, &dj); err != nil {
				continue
			}
			batch = append(batch, row{id: id, pk: extractPubkeyFromAdvertJSON(dj.String)})
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}

		tx, err := s.db.Begin()
		if err != nil {
			log.Printf("[backfill] from_pubkey begin tx: %v", err)
			return
		}
		txStmt := tx.Stmt(stmt)
		for _, b := range batch {
			// Sentinel: "" = scanned-no-pubkey (so the WHERE clause
			// won't keep rescanning this row). hex = real pubkey.
			var val interface{} = ""
			if b.pk != "" {
				val = b.pk
			}
			if _, err := txStmt.Exec(val, b.id); err != nil {
				log.Printf("[backfill] from_pubkey update id=%d: %v", b.id, err)
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("[backfill] from_pubkey commit: %v", err)
			return
		}
		processed += int64(len(batch))
		if progress != nil {
			progress(total, processed, false)
		}
		if len(batch) < chunkSize {
			break
		}
		if yieldDuration > 0 {
			time.Sleep(yieldDuration)
		}
	}
	log.Printf("[backfill] from_pubkey complete: %d rows processed", processed)
	if progress != nil {
		progress(total, processed, true)
	}
}

// extractPubkeyFromAdvertJSON parses an ADVERT decoded_json blob and
// returns the pubKey field, or "" if absent/invalid.
func extractPubkeyFromAdvertJSON(s string) string {
	if s == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return ""
	}
	if v, ok := m["pubKey"].(string); ok {
		return v
	}
	return ""
}

// extractPubkeyFromAnonReqJSON parses an ANON_REQ decoded_json blob and
// returns the ephemeralPubKey field, or "" if absent/invalid (#1777).
// ANON_REQ carries the sender's full Ed25519 ephemeral pubkey — the same
// trust level as ADVERT's pubKey — unlike REQ/RESP/PATH/TXT, which only
// carry a 1-byte truncated hash of the originator.
func extractPubkeyFromAnonReqJSON(s string) string {
	if s == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return ""
	}
	if v, ok := m["ephemeralPubKey"].(string); ok {
		return v
	}
	return ""
}
