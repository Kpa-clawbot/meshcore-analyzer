package dbschema

import (
	"database/sql"
	"fmt"
	"strings"
)

// upsertMergedColumns are the columns the ingestor's observation UPSERT writes
// on conflict, in cmd/ingestor/db.go:
//
//	ON CONFLICT(...) DO UPDATE SET
//	  snr           = COALESCE(excluded.snr,           snr),
//	  rssi          = COALESCE(excluded.rssi,          rssi),
//	  score         = COALESCE(excluded.score,         score),
//	  raw_hex       = COALESCE(excluded.raw_hex,       raw_hex),
//	  resolved_path = COALESCE(excluded.resolved_path, resolved_path)
//
// Two things follow, and collapseDuplicatesAndIndex has to honour both to be a
// faithful replay rather than an approximation of it.
//
// First, `excluded` is the incoming row, so a non-NULL later value REPLACES an
// earlier one. Applied down a group in insertion order the result is the LAST
// non-NULL value, not the first.
//
// Second, every other column is absent from the SET list, so the UPSERT never
// touches it: the surviving row keeps its own direction, timestamp, path_json
// and so on. Merging those too would invent history the ingestor would not have
// written.
var upsertMergedColumns = []string{"snr", "rssi", "score", "raw_hex", "resolved_path"}

// dupGroupsDDL materialises one row per duplicate group: the group key plus the
// earliest id, which is the row that is kept and merged into.
//
// Materialised rather than left as a repeated subquery on purpose. In the
// subquery form every merged column's UPDATE re-ran this GROUP BY over the whole
// observations table — measured at 9.7s on 2.4M rows holding only 5 duplicates,
// nearly all of it the same scan performed repeatedly. This runs inside a write
// transaction and holds the write lock for its duration, so the repetition is
// worth removing.
const dupGroupsDDL = `CREATE TEMP TABLE dedup_groups AS
	SELECT transmission_id, observer_idx, COALESCE(path_json, '') AS p, MIN(id) AS keep
	  FROM observations
	 GROUP BY transmission_id, observer_idx, COALESCE(path_json, '')
	HAVING COUNT(*) > 1`

const dedupIndexDDL = `CREATE UNIQUE INDEX IF NOT EXISTS idx_observations_dedup ` +
	`ON observations(transmission_id, observer_idx, COALESCE(path_json, ''))`

// ensureObservationsDedupIndex creates the unique expression index that the
// ingestor's observation UPSERT resolves its ON CONFLICT target against:
//
//	ON CONFLICT(transmission_id, observer_idx, COALESCE(path_json, ''))
//
// cmd/ingestor/db.go creates this index, but only inside the branch that
// creates the observations table for the first time. Any database whose
// observations table predates that branch therefore never got one, so the
// UPSERT had no conflict target to resolve against. Under modernc.org/sqlite
// that surfaced late — statements compiled lazily, so it failed on the first
// insert. github.com/mattn/go-sqlite3 compiles inside Prepare, so it now
// surfaces at OpenStore. Same bug either way; creating the index here repairs
// the database rather than just moving the error.
//
// Because the index is what was supposed to prevent duplicates, a database that
// never had it can already hold rows violating it — the repo's own
// test-fixtures/e2e-fixture.db did. Those rows are collapsed first. Refusing
// would not be safer: without the index the ingestor cannot prepare its UPSERT,
// so it cannot run at all.
//
// Skipped on v2 schemas (observer_id rather than observer_idx): the UPSERT does
// not apply there, and indexing a column that does not exist would fail.
func ensureObservationsDedupIndex(rw *sql.DB, logf Logger) error {
	hasIdx, err := TableHasColumn(rw, "observations", "observer_idx")
	if err != nil {
		return err
	}
	if !hasIdx {
		return nil
	}
	// Fast path: no duplicates, so the index just builds. Atomic on its own.
	_, err = rw.Exec(dedupIndexDDL)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return err
	}

	removed, err := collapseDuplicatesAndIndex(rw)
	if err != nil {
		return fmt.Errorf("collapse duplicate observations: %w", err)
	}
	logf("[dbschema] collapsed %d duplicate observation row(s) so idx_observations_dedup could be created", removed)
	return nil
}

// collapseDuplicatesAndIndex merges duplicate observation rows into the lowest
// id of each group, deletes the rest, and creates the unique index — all in one
// transaction. It returns the number of rows deleted.
//
// The index creation belongs inside the same transaction as the deletion. With
// them separated, a writer inserting a duplicate in the gap makes the index
// creation fail while leaving the deletions committed: rows destroyed and no
// index to show for it. One transaction makes the repair all-or-nothing.
func collapseDuplicatesAndIndex(rw *sql.DB) (int64, error) {
	tx, err := rw.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// A TEMP table lives for the life of the connection, and database/sql hands
	// out pooled connections, so an earlier call may have left one behind on
	// this one. Drop before creating rather than assume, and again afterwards
	// so the connection returns to the pool clean.
	if _, err := tx.Exec(`DROP TABLE IF EXISTS temp.dedup_groups`); err != nil {
		return 0, fmt.Errorf("drop stale dedup_groups: %w", err)
	}
	if _, err := tx.Exec(dupGroupsDDL); err != nil {
		return 0, fmt.Errorf("build dedup_groups: %w", err)
	}
	defer func() { _, _ = rw.Exec(`DROP TABLE IF EXISTS temp.dedup_groups`) }()
	if _, err := tx.Exec(`CREATE INDEX temp.dedup_groups_key
		ON dedup_groups(transmission_id, observer_idx, p)`); err != nil {
		return 0, fmt.Errorf("index dedup_groups: %w", err)
	}

	// Only merge columns this database actually has. Apply runs this step
	// before ensureResolvedPathColumn and ensureObservationsRawHexColumn, so on
	// a database old enough to be missing the dedup index, resolved_path and
	// raw_hex may not exist yet either.
	cols, err := existingColumns(rw, "observations", upsertMergedColumns)
	if err != nil {
		return 0, err
	}

	// Replay the UPSERT: for each merged column take the LAST non-NULL value in
	// the group by id. The subquery spans the whole group, the survivor
	// included, so when every row is NULL the result is NULL — which is what
	// the UPSERT would also have left behind.
	for _, c := range cols {
		q := `UPDATE observations SET ` + c + ` = (
			SELECT o2.` + c + ` FROM observations o2
			WHERE o2.transmission_id = observations.transmission_id
			  AND o2.observer_idx = observations.observer_idx
			  AND COALESCE(o2.path_json, '') = COALESCE(observations.path_json, '')
			  AND o2.` + c + ` IS NOT NULL
			ORDER BY o2.id DESC LIMIT 1)
			WHERE id IN (SELECT keep FROM dedup_groups)`
		if _, err := tx.Exec(q); err != nil {
			return 0, fmt.Errorf("merge %s: %w", c, err)
		}
	}

	res, err := tx.Exec(`DELETE FROM observations WHERE id IN (
		SELECT o.id FROM observations o
		JOIN dedup_groups d
		  ON d.transmission_id = o.transmission_id
		 AND d.observer_idx = o.observer_idx
		 AND d.p = COALESCE(o.path_json, '')
		WHERE o.id <> d.keep)`)
	if err != nil {
		return 0, err
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	// Same transaction: if this fails, the deletions above roll back with it.
	if _, err := tx.Exec(dedupIndexDDL); err != nil {
		return 0, fmt.Errorf("create idx_observations_dedup after collapsing %d row(s): %w", removed, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

// existingColumns filters want down to the columns table actually has, keeping
// the given order.
func existingColumns(rw *sql.DB, table string, want []string) ([]string, error) {
	out := make([]string, 0, len(want))
	for _, c := range want {
		ok, err := TableHasColumn(rw, table, c)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}
