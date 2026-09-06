package main

import (
	"testing"
	"time"
)

// #1888: /api/stats.totalObservers and /api/observers disagreed because they
// counted different sets. GetObservers() (and therefore the Observers page)
// returns only rows the retention sweep has not soft-deleted
// (inactive IS NULL OR inactive = 0), while the store's stats query counted
// every row in the table. On a deployment with observerDays retention the two
// drift apart by exactly the number of soft-deleted observers — 79 vs 51 on the
// instance this was reproduced against.
//
// The two stats implementations did not even agree with each other: the DB
// fallback (DB.GetStats) already applied the inactive filter, the store path
// did not.

// seedObserversForCount inserts 3 live observers and 2 soft-deleted ones.
func seedObserversForCount(t *testing.T, db *DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.conn.Exec(`DELETE FROM observers`); err != nil {
		t.Fatalf("clear observers: %v", err)
	}
	live := []string{"live1", "live2", "live3"}
	for _, id := range live {
		if _, err := db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count, inactive)
			VALUES (?, ?, ?, ?, 10, 0)`, id, "Observer "+id, now, now); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// One explicitly soft-deleted, one with a NULL flag — GetObservers treats
	// NULL as live, so only the inactive=1 row must be excluded.
	if _, err := db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count, inactive)
		VALUES ('gone1', 'Gone One', ?, ?, 5, 1)`, now, now); err != nil {
		t.Fatalf("insert gone1: %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count, inactive)
		VALUES ('nullflag', 'Null Flag', ?, ?, 5, NULL)`, now, now); err != nil {
		t.Fatalf("insert nullflag: %v", err)
	}
}

func TestStoreStatsTotalObserversExcludesSoftDeleted(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	seedObserversForCount(t, db)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}

	st, err := store.GetStoreStats()
	if err != nil {
		t.Fatalf("GetStoreStats: %v", err)
	}
	if st.TotalObservers != 4 {
		t.Errorf("TotalObservers = %d, want 4 (3 live + 1 NULL flag, excluding 1 soft-deleted)", st.TotalObservers)
	}
}

// The count must equal what the Observers page actually lists — that is the
// whole point of the issue: three UI surfaces showing three numbers.
func TestStoreStatsTotalObserversMatchesObserverList(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	seedObserversForCount(t, db)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}

	st, err := store.GetStoreStats()
	if err != nil {
		t.Fatalf("GetStoreStats: %v", err)
	}
	observers, err := db.GetObservers()
	if err != nil {
		t.Fatalf("GetObservers: %v", err)
	}
	if st.TotalObservers != len(observers) {
		t.Errorf("stats totalObservers = %d but /api/observers lists %d",
			st.TotalObservers, len(observers))
	}
}

// The store path and the DB fallback must not report different totals for the
// same database.
func TestStoreAndDBStatsAgreeOnTotalObservers(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	seedObserversForCount(t, db)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}

	storeStats, err := store.GetStoreStats()
	if err != nil {
		t.Fatalf("GetStoreStats: %v", err)
	}
	dbStats, err := db.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if storeStats.TotalObservers != dbStats.TotalObservers {
		t.Errorf("store path reports %d observers, DB fallback reports %d",
			storeStats.TotalObservers, dbStats.TotalObservers)
	}
}
