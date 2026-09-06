package main

import (
	"testing"
	"time"
)

// staleObserver inserts an observer already soft-deleted (inactive = 1) with
// last_seen ageDays in the past, and returns its rowid.
func staleObserver(t *testing.T, s *Store, id string) int64 {
	t.Helper()
	if err := s.UpsertObserver(id, id, "LAX", nil); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -90).Format(time.RFC3339)
	if _, err := s.db.Exec(`UPDATE observers SET last_seen = ?, inactive = 1 WHERE id = ?`, old, id); err != nil {
		t.Fatal(err)
	}
	var rowid int64
	if err := s.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, id).Scan(&rowid); err != nil {
		t.Fatal(err)
	}
	return rowid
}

func observerExists(t *testing.T, s *Store, id string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestPurgeStaleObserversDeletesUnreferencedRow(t *testing.T) {
	store := newTestStore(t)
	staleObserver(t, store, "obs-gone")

	purged, err := store.PurgeStaleObservers(30)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Errorf("purged=%d, want 1", purged)
	}
	if observerExists(t, store, "obs-gone") {
		t.Error("obs-gone still present, want hard-deleted")
	}
}

// Regression: an observer whose rowid is still referenced by observations must
// survive the purge — deleting it orphans observations.observer_idx, which
// breaks the packets_v join and mis-attributes historical packets.
func TestPurgeStaleObserversKeepsObserverWithObservations(t *testing.T) {
	store := newTestStore(t)
	rowid := staleObserver(t, store, "obs-referenced")

	if _, err := store.db.Exec(
		`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, 'AA', 'h1', ?)`,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, timestamp) VALUES (1, ?, 0)`, rowid,
	); err != nil {
		t.Fatal(err)
	}

	purged, err := store.PurgeStaleObservers(30)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Errorf("purged=%d, want 0 (row is referenced by observations)", purged)
	}
	if !observerExists(t, store, "obs-referenced") {
		t.Error("obs-referenced was deleted, want kept")
	}

	var orphans int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM observations WHERE observer_idx IS NOT NULL
		 AND observer_idx NOT IN (SELECT rowid FROM observers)`,
	).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("orphaned observations=%d, want 0", orphans)
	}
}

func TestPurgeStaleObserversKeepsObserverWithMetrics(t *testing.T) {
	store := newTestStore(t)
	staleObserver(t, store, "obs-metrics")

	if _, err := store.db.Exec(
		`INSERT INTO observer_metrics (observer_id, timestamp) VALUES ('obs-metrics', ?)`,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatal(err)
	}

	purged, err := store.PurgeStaleObservers(30)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Errorf("purged=%d, want 0 (row is referenced by observer_metrics)", purged)
	}
	if !observerExists(t, store, "obs-metrics") {
		t.Error("obs-metrics was deleted, want kept")
	}
}

func TestPurgeStaleObserversKeepsObserverWithDroppedPackets(t *testing.T) {
	store := newTestStore(t)
	staleObserver(t, store, "obs-dropped")

	if _, err := store.db.Exec(
		`INSERT INTO dropped_packets (reason, observer_id) VALUES ('bad-sig', 'obs-dropped')`,
	); err != nil {
		t.Fatal(err)
	}

	purged, err := store.PurgeStaleObservers(30)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Errorf("purged=%d, want 0 (row is referenced by dropped_packets)", purged)
	}
	if !observerExists(t, store, "obs-dropped") {
		t.Error("obs-dropped was deleted, want kept")
	}
}

// An observer old enough to purge but not yet soft-deleted must be left to
// RemoveStaleObservers — the hard purge only ever finalises an inactive row.
func TestPurgeStaleObserversKeepsActiveObserver(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertObserver("obs-active", "Active", "LAX", nil); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -90).Format(time.RFC3339)
	if _, err := store.db.Exec(`UPDATE observers SET last_seen = ? WHERE id = ?`, old, "obs-active"); err != nil {
		t.Fatal(err)
	}

	purged, err := store.PurgeStaleObservers(30)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Errorf("purged=%d, want 0 (inactive = 0)", purged)
	}
	if !observerExists(t, store, "obs-active") {
		t.Error("obs-active was deleted, want kept")
	}
}

func TestPurgeStaleObserversKeepsObserverInsideWindow(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertObserver("obs-recent", "Recent", "LAX", nil); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().UTC().AddDate(0, 0, -20).Format(time.RFC3339)
	if _, err := store.db.Exec(
		`UPDATE observers SET last_seen = ?, inactive = 1 WHERE id = ?`, recent, "obs-recent",
	); err != nil {
		t.Fatal(err)
	}

	purged, err := store.PurgeStaleObservers(30)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Errorf("purged=%d, want 0 (last_seen inside the 30-day window)", purged)
	}
	if !observerExists(t, store, "obs-recent") {
		t.Error("obs-recent was deleted, want kept")
	}
}

func TestPurgeStaleObserversDisabled(t *testing.T) {
	store := newTestStore(t)
	staleObserver(t, store, "obs-kept")

	for _, days := range []int{0, -1} {
		purged, err := store.PurgeStaleObservers(days)
		if err != nil {
			t.Fatal(err)
		}
		if purged != 0 {
			t.Errorf("PurgeStaleObservers(%d) purged=%d, want 0 (disabled)", days, purged)
		}
	}
	if !observerExists(t, store, "obs-kept") {
		t.Error("obs-kept was deleted, want kept while purge is disabled")
	}
}

func TestObserverPurgeDaysOrZero(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want int
	}{
		{"nil retention", &Config{}, 0},
		{"unset", &Config{Retention: &RetentionConfig{}}, 0},
		{"configured", &Config{Retention: &RetentionConfig{ObserverPurgeDays: 30}}, 30},
		{"negative disables", &Config{Retention: &RetentionConfig{ObserverPurgeDays: -1}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ObserverPurgeDaysOrZero(); got != tt.want {
				t.Errorf("ObserverPurgeDaysOrZero() = %d, want %d", got, tt.want)
			}
		})
	}
}
