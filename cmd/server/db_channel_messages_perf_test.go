package main

import (
	"fmt"
	"testing"
	"time"
)

// TestGetChannelMessagesPerfLargeChannel asserts that fetching a small page
// (limit=50) from a channel with many observations does not scan the full
// observation set. Regression guard for issue #1225 where the query loaded
// every observation row for the channel and deduped/paginated in Go memory.
//
// Dataset: 1500 transmissions in #perf, each with 50 observations
// (75K obs total). Same shape as staging where #wardriving had ~5.7K tx
// and ~275K obs — fewer rows are enough to demonstrate that the broken
// impl (which loads/dedups every observation in Go) blows the budget,
// while keeping setup fast enough for slower CI runners.
//
// On the broken implementation this takes multiple seconds (>2s on dev,
// ~6–9s on GitHub-hosted CI). With SQL-level pagination over
// transmissions it must complete well under the 1.5s budget
// (~sub-100ms observed on dev).
func TestGetChannelMessagesPerfLargeChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("perf test")
	}
	db := setupTestDB(t)
	defer db.Close()

	// Seed one observer.
	if _, err := db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs_perf', 'PerfObs', 'SJC', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 0)`); err != nil {
		t.Fatal(err)
	}

	const numTx = 1500
	const obsPerTx = 50

	tx, err := db.conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	txStmt, err := tx.Prepare(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
		VALUES (?, ?, ?, 1, 5, ?, '#perf')`)
	if err != nil {
		t.Fatal(err)
	}
	obsStmt, err := tx.Prepare(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (?, 1, 10.0, -90, '[]', ?)`)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-24 * time.Hour)
	for i := 0; i < numTx; i++ {
		ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		hash := fmt.Sprintf("perfhash%08d", i)
		body := fmt.Sprintf(`{"type":"CHAN","channel":"#perf","text":"Sender%d: msg %d","sender":"Sender%d"}`, i%10, i, i%10)
		res, err := txStmt.Exec(fmt.Sprintf("%04X", i), hash, ts, body)
		if err != nil {
			t.Fatal(err)
		}
		txID, _ := res.LastInsertId()
		for o := 0; o < obsPerTx; o++ {
			if _, err := obsStmt.Exec(txID, base.Unix()+int64(i*100+o)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Warm-up call to amortize first-run prepare cost.
	if _, _, err := db.GetChannelMessages("#perf", 50, 0); err != nil {
		t.Fatal(err)
	}

	// Clear the results cache so the timed call below still measures the
	// real SQL path rather than a cache hit — this test's whole point is
	// catching a regression in that query.
	db.msgCacheMu.Lock()
	db.msgCache = nil
	db.msgCacheMu.Unlock()

	start := time.Now()
	msgs, total, err := db.GetChannelMessages("#perf", 50, 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if total != numTx {
		t.Errorf("total: got %d want %d", total, numTx)
	}
	if len(msgs) != 50 {
		t.Errorf("page size: got %d want 50", len(msgs))
	}
	const budget = 1500 * time.Millisecond
	if elapsed > budget {
		t.Fatalf("GetChannelMessages too slow for #1225: %v (budget %v) on %d tx × %d obs",
			elapsed, budget, numTx, obsPerTx)
	}
}
