package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/packetpath"
	_ "modernc.org/sqlite"
)

// DBStats tracks operational metrics for the ingestor database.
type DBStats struct {
	TransmissionsInserted  atomic.Int64
	ObservationsInserted   atomic.Int64
	DuplicateTransmissions atomic.Int64
	NodeUpserts            atomic.Int64
	ObserverUpserts        atomic.Int64
	WriteErrors            atomic.Int64
	SignatureDrops         atomic.Int64
	// RelayTouches counts nodes.last_seen refreshes driven by relay
	// participation rather than an ADVERT (#1598).
	RelayTouches atomic.Int64
	// WALCommits tracks every successful tx.Commit() that may have flushed
	// WAL pages.
	WALCommits atomic.Int64
	// BackfillUpdates tracks per-named-backfill row write counts so an
	// infinite-loop backfill (cf #1119) is obvious from the perf page.
	BackfillUpdates sync.Map // name (string) -> *atomic.Int64
}

// IncBackfill increments the backfill counter for the given name, allocating
// the counter on first use.
func (s *DBStats) IncBackfill(name string) {
	v, ok := s.BackfillUpdates.Load(name)
	if !ok {
		nc := new(atomic.Int64)
		actual, loaded := s.BackfillUpdates.LoadOrStore(name, nc)
		if loaded {
			v = actual
		} else {
			v = nc
		}
	}
	v.(*atomic.Int64).Add(1)
}

// SnapshotBackfills returns a name->count copy of all backfill counters.
func (s *DBStats) SnapshotBackfills() map[string]int64 {
	out := make(map[string]int64)
	s.BackfillUpdates.Range(func(k, v interface{}) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}

// Store wraps the SQLite database for packet ingestion.
type Store struct {
	db    *sql.DB
	path  string // filesystem path to the SQLite DB (used to resolve queue dirs)
	Stats DBStats

	stmtGetTxByHash            *sql.Stmt
	stmtInsertTransmission     *sql.Stmt
	stmtUpdateTxFirstSeen      *sql.Stmt
	stmtBumpTxLastSeen         *sql.Stmt
	stmtInsertObservation      *sql.Stmt
	stmtUpsertNode             *sql.Stmt
	stmtIncrementAdvertCount   *sql.Stmt
	stmtUpsertObserver         *sql.Stmt
	stmtGetObserverRowid       *sql.Stmt
	stmtUpdateObserverLastSeen *sql.Stmt
	stmtUpdateNodeTelemetry    *sql.Stmt
	stmtTouchNodeLastSeen      *sql.Stmt
	stmtUpsertMetrics          *sql.Stmt

	sampleIntervalSec int
	backfillWg        sync.WaitGroup

	// prefixIdx holds the prefix → pubkey index used by the
	// resolved_path writer (#1547). Rebuilt on startup and once per
	// neighbor-edges builder tick (60s).
	prefixIdx prefixIdxHolder

	// neighborGraph holds the in-memory NeighborGraph snapshot used
	// by the context-aware resolver (#1560). Rebuilt on startup and
	// once per neighbor-edges builder tick (60s).
	neighborGraph neighborGraphHolder

	// relayTouched is the debounce map for touchRelayNodesLocked:
	// pubkey -> rxTime of the last last_seen write (#1598). Guarded by
	// writerMu, which InsertTransmission holds for its whole body.
	relayTouched map[string]time.Time
}

// relayTouchDebounce is the minimum interval between two last_seen writes
// for the same relay node. A backbone repeater appears in thousands of
// paths per hour; without this the ingest path would issue one UPDATE per
// observation for no added freshness.
const relayTouchDebounce = 5 * time.Minute

// relayTouchedMaxEntries caps the debounce map. One entry per node ever
// seen relaying — ~10^3-10^4 on real deployments — but a long-lived
// process on a large mesh should not grow it without bound. On overflow
// we drop entries older than two debounce windows, which can only cause
// an extra UPDATE, never a missed one.
const relayTouchedMaxEntries = 50000

// OpenStore opens or creates a SQLite DB at the given path, applying the
// v3 schema that is compatible with the Node.js server.
func OpenStore(dbPath string) (*Store, error) {
	return OpenStoreWithInterval(dbPath, 300)
}

// OpenStoreWithInterval opens or creates a SQLite DB with a configurable sample interval.
func OpenStoreWithInterval(dbPath string, sampleIntervalSec int) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=auto_vacuum(INCREMENTAL)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pinging db: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	log.Printf("SQLite config: busy_timeout=5000ms, max_open_conns=1, max_idle_conns=1, journal=WAL")

	if err := applySchema(db); err != nil {
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	// Apply the additional server-originated migrations (now owned by
	// the ingestor per #1287). Adds the indexes/columns that used to live
	// in cmd/server/ensure_*.go: server now ASSERTS these exist.
	if err := dbschema.Apply(db, log.Printf); err != nil {
		return nil, fmt.Errorf("dbschema.Apply: %w", err)
	}

	s := &Store{db: db, path: dbPath, sampleIntervalSec: sampleIntervalSec}
	if err := s.prepareStatements(); err != nil {
		return nil, fmt.Errorf("preparing statements: %w", err)
	}

	// Schedule async migrations. These must NOT block boot. See
	// async_migration.go for the convention.
	// PREFLIGHT: async=true reason="composite index build on observations (1.9M+ rows in prod) — converted from sync after v3.8.3"
	var idxDone int
	if s.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'obs_observer_ts_idx_v1'").Scan(&idxDone) != nil {
		if err := s.RunAsyncMigration(context.Background(), "obs_observer_ts_idx_v1",
			func(ctx context.Context, d *sql.DB) error {
				log.Println("[migration/async] Building (observer_idx, timestamp) composite index on observations...")
				if _, err := d.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_observations_observer_idx_timestamp ON observations(observer_idx, timestamp)`); err != nil {
					return err
				}
				if _, err := d.ExecContext(ctx, `INSERT OR IGNORE INTO _migrations (name) VALUES ('obs_observer_ts_idx_v1')`); err != nil {
					return err
				}
				log.Println("[migration/async] observations(observer_idx, timestamp) index created")
				return nil
			}); err != nil {
			log.Printf("[migration/async] scheduling obs_observer_ts_idx_v1 failed: %v", err)
		}
	}

	// #1690: backfill transmissions.last_seen from MAX(observations.timestamp)
	// per transmission. The column is added inline by dbschema.Apply (cheap
	// metadata-only ALTER); the populate query is potentially expensive
	// (full obs scan + group) so we run it async. Subsequent observation
	// inserts maintain the column inline (see InsertTransmission below).
	// PREFLIGHT: async=true reason="full-table backfill JOIN (1.9M+ obs × 86k+ tx in prod) — must not block ingestor boot"
	if err := s.RunAsyncMigration(context.Background(), "tx_last_seen_backfill_v1",
		func(ctx context.Context, d *sql.DB) error {
			log.Println("[migration/async] Backfilling transmissions.last_seen from MAX(observations.timestamp)...")
			res, err := d.ExecContext(ctx, `
				UPDATE transmissions
				SET last_seen = COALESCE((
					SELECT MAX(timestamp) FROM observations WHERE transmission_id = transmissions.id
				), last_seen)
				WHERE last_seen = 0
			`)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			log.Printf("[migration/async] transmissions.last_seen backfill complete: %d rows updated", n)
			return nil
		}); err != nil {
		log.Printf("[migration/async] scheduling tx_last_seen_backfill_v1 failed: %v", err)
	}

	return s, nil
}

func applySchema(db *sql.DB) error {
	// auto_vacuum=INCREMENTAL is set via DSN pragma (must be before journal_mode).
	// Logging of current mode is handled by CheckAutoVacuum — no duplicate log here.

	schema := `
		CREATE TABLE IF NOT EXISTS nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			role TEXT,
			lat REAL,
			lon REAL,
			last_seen TEXT,
			first_seen TEXT,
			advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER,
			temperature_c REAL,
			foreign_advert INTEGER DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS observers (
			id TEXT PRIMARY KEY,
			name TEXT,
			iata TEXT,
			last_seen TEXT,
			first_seen TEXT,
			packet_count INTEGER DEFAULT 0,
			model TEXT,
			firmware TEXT,
			client_version TEXT,
			radio TEXT,
			battery_mv INTEGER,
			uptime_secs INTEGER,
			noise_floor REAL,
			inactive INTEGER DEFAULT 0,
			last_packet_at TEXT DEFAULT NULL,
			clock_skew_seconds INTEGER DEFAULT NULL,
			clock_skew_count_24h INTEGER DEFAULT 0,
			clock_last_naive_at TEXT DEFAULT NULL,
			can_relay INTEGER DEFAULT 1,
			can_relay_seen INTEGER DEFAULT 0
		);

		CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen);
		CREATE INDEX IF NOT EXISTS idx_observers_last_seen ON observers(last_seen);

		CREATE TABLE IF NOT EXISTS inactive_nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			role TEXT,
			lat REAL,
			lon REAL,
			last_seen TEXT,
			first_seen TEXT,
			advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER,
			temperature_c REAL,
			foreign_advert INTEGER DEFAULT 0
		);

		CREATE INDEX IF NOT EXISTS idx_inactive_nodes_last_seen ON inactive_nodes(last_seen);

		CREATE TABLE IF NOT EXISTS transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT,
			from_pubkey TEXT,
			last_seen INTEGER NOT NULL DEFAULT 0,
			created_at TEXT DEFAULT (datetime('now')),
			code1 TEXT,
			code2 TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_transmissions_hash ON transmissions(hash);
		CREATE INDEX IF NOT EXISTS idx_transmissions_first_seen ON transmissions(first_seen);
		CREATE INDEX IF NOT EXISTS idx_transmissions_payload_type ON transmissions(payload_type);
		-- idx_tx_code1 is created by the transport_codes_v1 migration below,
		-- after the ALTER runs — same reasoning as idx_transmissions_from_pubkey
		-- and idx_tx_last_seen_zero above: a legacy DB (table-exists,
		-- column-missing) would trip on this CREATE INDEX referencing code1
		-- before the column exists, since CREATE TABLE IF NOT EXISTS is a
		-- no-op against a pre-existing table.
		-- idx_transmissions_from_pubkey is created by the from_pubkey_v1
		-- migration after the column is added on legacy DBs (#1143).
		-- idx_tx_last_seen_zero (partial, WHERE last_seen=0) is created by
		-- dbschema.Apply after ensuring the last_seen column exists (#1690,
		-- partial-index swap #1740) — keep it OUT of this base schema block
		-- so legacy DBs (table-exists, column-missing) don't trip on the
		-- CREATE INDEX before the ALTER runs.

		-- Mobile client RX coverage: a roaming companion = a mobile observer
		-- with a moving GPS position, so it gets its own table rather than
		-- observations (which assumes a fixed observer/location).
		CREATE TABLE IF NOT EXISTS client_receptions (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			rx_pubkey     TEXT NOT NULL,
			heard_key     TEXT NOT NULL,
			heard_keylen  INTEGER NOT NULL,
			rssi          INTEGER,
			snr           REAL,
			lat           REAL NOT NULL,
			lon           REAL NOT NULL,
			pos_acc_m     REAL,
			rx_at         TEXT NOT NULL,
			ingested_at   TEXT NOT NULL,
			src           TEXT NOT NULL,
			UNIQUE(rx_pubkey, heard_key, rx_at)
		);
		-- Coverage queries filter by bbox AND match the heard node either by full
		-- key (heard_keylen=32 AND heard_key=?) or by 2-3 byte prefix. The composite
		-- (heard_key, heard_keylen, lat, lon) serves the heard_key-equality seek and
		-- carries lat/lon so the bbox range is satisfied from the index; it also
		-- supersedes the old single-column heard_key index. idx_client_recept_latlon
		-- lets the planner instead drive from a selective bbox. (#5, #18)
		CREATE INDEX IF NOT EXISTS idx_client_recept_heard_geo ON client_receptions(heard_key, heard_keylen, lat, lon);
		CREATE INDEX IF NOT EXISTS idx_client_recept_latlon ON client_receptions(lat, lon);
		-- rx_at backs both the retention reaper (DELETE WHERE rx_at < ?) and the
		-- leaderboard, which range-scans WHERE rx_at >= ? and aggregates per
		-- rx_pubkey in Go (see rxLeaderboard's frontier-weighted scoring). Without
		-- this index either would full-scan the table under the writer lock
		-- (verified by an EXPLAIN test). A dedicated rx_pubkey index stays
		-- redundant — the leaderboard no longer groups by rx_pubkey in SQL.
		CREATE INDEX IF NOT EXISTS idx_client_recept_rxat ON client_receptions(rx_at);
		DROP INDEX IF EXISTS idx_client_recept_rxpk;

		-- Self-reported name of each mobile client (companion), from the SELF_INFO
		-- name the app sends as "origin". Lets the leaderboard show a name even
		-- when the companion never advertised (so it isn't in the nodes table).
		CREATE TABLE IF NOT EXISTS client_observers (
			pubkey    TEXT PRIMARY KEY,
			name      TEXT,
			last_seen TEXT
		);

		-- Diagnostic RF observations from mobile clients. Unlike client_receptions
		-- this holds EVERY decodable packet, attributable or not, so it must never
		-- be used for coverage. pkt_hash is ComputeContentHash() — identical to
		-- transmissions.hash — so dark-traffic queries are a plain equality join.
		CREATE TABLE IF NOT EXISTS client_rx_observations (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			rx_pubkey     TEXT NOT NULL,
			rx_at         TEXT NOT NULL,
			ingested_at   TEXT NOT NULL,
			pkt_hash      TEXT NOT NULL,
			route_type    INTEGER NOT NULL,
			payload_type  INTEGER NOT NULL,
			code1         TEXT,
			code2         TEXT,
			scope_name    TEXT,
			hash_size     INTEGER NOT NULL,
			hop_count     INTEGER NOT NULL,
			path_json     TEXT,
			forwarder     TEXT,
			snr           REAL,
			rssi          INTEGER,
			lat           REAL NOT NULL,
			lon           REAL NOT NULL,
			pos_acc_m     REAL,
			UNIQUE(rx_pubkey, pkt_hash, rx_at)
		);
		CREATE INDEX IF NOT EXISTS idx_cro_prune     ON client_rx_observations(rx_at);
		CREATE INDEX IF NOT EXISTS idx_cro_hash      ON client_rx_observations(pkt_hash, rx_at);
		CREATE INDEX IF NOT EXISTS idx_cro_forwarder ON client_rx_observations(forwarder, rx_at);
		CREATE INDEX IF NOT EXISTS idx_cro_scope     ON client_rx_observations(scope_name, rx_at);

		-- RF environment samples from mobile clients: radio counters paired with
		-- a GPS point. Absolutes only — deltas are computed at query time, and a
		-- decrease in uptime_secs (reboot) or any counter (wrap) breaks the chain.
		CREATE TABLE IF NOT EXISTS client_rf_samples (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			rx_pubkey    TEXT NOT NULL,
			sampled_at   TEXT NOT NULL,
			ingested_at  TEXT NOT NULL,
			lat          REAL NOT NULL,
			lon          REAL NOT NULL,
			pos_acc_m    REAL,
			stationary   INTEGER NOT NULL DEFAULT 0,
			uptime_secs  INTEGER NOT NULL,
			battery_mv   INTEGER,
			queue_len    INTEGER,
			errors       INTEGER,
			noise_floor  INTEGER,
			last_rssi    INTEGER,
			last_snr     REAL,
			tx_air_secs  INTEGER,
			rx_air_secs  INTEGER,
			recv         INTEGER,
			sent         INTEGER,
			flood_rx     INTEGER,
			direct_rx    INTEGER,
			flood_tx     INTEGER,
			direct_tx    INTEGER,
			recv_errors  INTEGER,
			UNIQUE(rx_pubkey, sampled_at)
		);
		CREATE INDEX IF NOT EXISTS idx_crf_prune ON client_rf_samples(sampled_at);
		CREATE INDEX IF NOT EXISTS idx_crf_track ON client_rf_samples(rx_pubkey, sampled_at);
	`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("base schema: %w", err)
	}

	// Create observations table (v3 schema)
	obsExists := false
	row := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='observations'")
	var dummy string
	if row.Scan(&dummy) == nil {
		obsExists = true
	}

	if !obsExists {
		obs := `
			CREATE TABLE observations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
				observer_idx INTEGER,
				direction TEXT,
				snr REAL,
				rssi REAL,
				score INTEGER,
				path_json TEXT,
				timestamp INTEGER NOT NULL
			);
			CREATE INDEX idx_observations_transmission_id ON observations(transmission_id);
			CREATE INDEX idx_observations_observer_idx ON observations(observer_idx);
			CREATE INDEX idx_observations_timestamp ON observations(timestamp);
			CREATE UNIQUE INDEX IF NOT EXISTS idx_observations_dedup ON observations(transmission_id, observer_idx, COALESCE(path_json, ''));
		`
		if _, err := db.Exec(obs); err != nil {
			return fmt.Errorf("observations schema: %w", err)
		}
	}

	// Create/rebuild packets_v view (v3 schema: observer_idx → observers.rowid)
	// The Go server reads this view; without it fresh installs get "no such table: packets_v".
	db.Exec(`DROP VIEW IF EXISTS packets_v`)
	_, vErr := db.Exec(`
		CREATE VIEW packets_v AS
			SELECT o.id, COALESCE(o.raw_hex, t.raw_hex) AS raw_hex,
				   datetime(o.timestamp, 'unixepoch') AS timestamp,
				   obs.id AS observer_id, obs.name AS observer_name,
				   o.direction, o.snr, o.rssi, o.score, t.hash, t.route_type,
				   t.payload_type, t.payload_version, o.path_json, t.decoded_json,
				   t.created_at
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx AND (obs.inactive IS NULL OR obs.inactive = 0)
	`)
	if vErr != nil {
		return fmt.Errorf("packets_v view: %w", vErr)
	}

	// One-time migration: recalculate advert_count to count unique transmissions only
	db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY)`)
	var migDone int
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'advert_count_unique_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Recalculating advert_count (unique transmissions only)...")
		// Note: this migration is gated on a one-shot _migrations row, so it
		// runs at most once per DB. The historical version used a LIKE-on-JSON
		// substring match (#1143). Switching to from_pubkey here is safe even
		// though the column may not yet be backfilled on legacy DBs: the
		// migration is already marked done on those DBs and won't re-run.
		db.Exec(`
			UPDATE nodes SET advert_count = (
				SELECT COUNT(*) FROM transmissions t
				WHERE t.payload_type = 4
				  AND t.from_pubkey = nodes.public_key
			)
		`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('advert_count_unique_v1')`)
		log.Println("[migration] advert_count recalculated")
	}

	// One-time migration: change noise_floor from INTEGER to REAL affinity.
	// SQLite doesn't support ALTER COLUMN, but existing float values are stored
	// as REAL regardless of column affinity. New table definition already uses REAL.
	// This migration casts any integer-stored noise_floor values to real.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'noise_floor_real_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Ensuring noise_floor values are stored as REAL...")
		db.Exec(`UPDATE observers SET noise_floor = CAST(noise_floor AS REAL) WHERE noise_floor IS NOT NULL AND typeof(noise_floor) = 'integer'`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('noise_floor_real_v1')`)
		log.Println("[migration] noise_floor migration complete")
	}

	// One-time migration: add telemetry columns to nodes and inactive_nodes tables.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'node_telemetry_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding telemetry columns to nodes/inactive_nodes...")

		// checkAndAddColumn checks whether `column` already exists in `table`
		// using PRAGMA table_info, and adds it if missing. All call sites pass
		// hardcoded table/column/type literals so there is no SQL injection risk.
		checkAndAddColumn := func(table, column, colType string) error {
			rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
			if err != nil {
				return fmt.Errorf("querying table info for %s: %w", table, err)
			}
			defer rows.Close()

			exists := false
			for rows.Next() {
				var cid int
				var name, ctype string
				var notnull, pk int
				var dfltValue sql.NullString
				if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
					return fmt.Errorf("scanning table info for %s: %w", table, err)
				}
				if name == column {
					exists = true
					break
				}
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating table info for %s: %w", table, err)
			}
			if exists {
				return nil
			}
			if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colType)); err != nil {
				return fmt.Errorf("adding column %s to %s: %w", column, table, err)
			}
			return nil
		}

		if err := checkAndAddColumn("nodes", "battery_mv", "INTEGER"); err != nil {
			return err
		}
		if err := checkAndAddColumn("nodes", "temperature_c", "REAL"); err != nil {
			return err
		}
		if err := checkAndAddColumn("inactive_nodes", "battery_mv", "INTEGER"); err != nil {
			return err
		}
		if err := checkAndAddColumn("inactive_nodes", "temperature_c", "REAL"); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO _migrations (name) VALUES ('node_telemetry_v1')`); err != nil {
			return fmt.Errorf("recording node_telemetry_v1 migration: %w", err)
		}
		log.Println("[migration] node telemetry columns added")
	}

	// One-time migration: add timestamp index on observations for fast stats queries.
	// Older databases created before this index was added suffer from full table scans
	// on COUNT(*) WHERE timestamp > ?, causing /api/stats to take 30s+.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'obs_timestamp_index_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding timestamp index on observations...")
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_timestamp ON observations(timestamp)`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('obs_timestamp_index_v1')`)
		log.Println("[migration] observations timestamp index created")
	}

	// #1481 P0-3: covering index for GetObserverPacketCounts. The query
	// joins observations → observers and GROUP BYs observer_idx with a
	// timestamp WHERE filter; a composite (observer_idx, timestamp)
	// index lets SQLite resolve the grouping + range filter from the
	// index alone instead of a 1.9M-row scan.
	//
	// CONVERTED TO ASYNC (preflight-async-migration-gate). Scheduling
	// happens in OpenStore() once the real *Store exists so the
	// backfill WaitGroup is shared with the rest of the ingestor.
	// The legacy `_migrations` gate is preserved by the async fn so
	// DBs that already completed the sync build stay no-op.

	// #1483: normalize nodes.public_key to lowercase. The server's
	// GetNodeLocationsByKeys lookup dropped LOWER(public_key) for perf
	// (#1481 P0-3) and now relies on stored keys being lowercase. The
	// decoder writes lowercase today, but legacy/admin/API inserts may
	// have left mixed-case rows. Idempotent: counts and lowers any
	// non-lowercase rows on every boot, runs once via _migrations gate
	// for the bulk fix. Re-running stays cheap because subsequent
	// passes match zero rows.
	if r := db.QueryRow("SELECT COUNT(*) FROM nodes WHERE public_key != lower(public_key)"); r != nil {
		var n int64
		_ = r.Scan(&n)
		if n > 0 {
			log.Printf("[migration] Normalizing %d nodes.public_key row(s) to lowercase (#1483)...", n)
			if _, err := db.Exec(`UPDATE nodes SET public_key = lower(public_key) WHERE public_key != lower(public_key)`); err != nil {
				log.Printf("[migration] public_key lowercase normalize failed: %v", err)
			} else {
				log.Printf("[migration] public_key lowercase normalize complete (%d rows)", n)
			}
		}
	}

	// observer_metrics table for RF health dashboard
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observer_metrics_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Creating observer_metrics table...")
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS observer_metrics (
				observer_id TEXT NOT NULL,
				timestamp TEXT NOT NULL,
				noise_floor REAL,
				tx_air_secs INTEGER,
				rx_air_secs INTEGER,
				recv_errors INTEGER,
				battery_mv INTEGER,
				PRIMARY KEY (observer_id, timestamp)
			)
		`)
		if err != nil {
			return fmt.Errorf("observer_metrics schema: %w", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observer_metrics_v1')`)
		log.Println("[migration] observer_metrics table created")
	}

	// Migration: add timestamp index for cross-observer time-range queries
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observer_metrics_ts_idx'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Creating observer_metrics timestamp index...")
		_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_observer_metrics_timestamp ON observer_metrics(timestamp)`)
		if err != nil {
			return fmt.Errorf("observer_metrics timestamp index: %w", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observer_metrics_ts_idx')`)
		log.Println("[migration] observer_metrics timestamp index created")
	}

	// Migration: add inactive column to observers for soft-delete retention
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observers_inactive_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding inactive column to observers...")
		_, err := db.Exec(`ALTER TABLE observers ADD COLUMN inactive INTEGER DEFAULT 0`)
		if err != nil {
			// Column may already exist (e.g. fresh install with schema above)
			log.Printf("[migration] observers.inactive: %v (may already exist)", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observers_inactive_v1')`)
		log.Println("[migration] observers.inactive column added")
	}

	// Migration: add packets_sent and packets_recv columns to observer_metrics
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observer_metrics_packets_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding packets_sent/packets_recv columns to observer_metrics...")
		db.Exec(`ALTER TABLE observer_metrics ADD COLUMN packets_sent INTEGER`)
		db.Exec(`ALTER TABLE observer_metrics ADD COLUMN packets_recv INTEGER`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observer_metrics_packets_v1')`)
		log.Println("[migration] packets_sent/packets_recv columns added")
	}

	// Migration: add channel_hash column for fast channel queries (#762)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'channel_hash_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding channel_hash column to transmissions...")
		db.Exec(`ALTER TABLE transmissions ADD COLUMN channel_hash TEXT DEFAULT NULL`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_tx_channel_hash ON transmissions(channel_hash) WHERE payload_type = 5`)
		// Backfill: extract channel name for decrypted (CHAN) packets
		res, err := db.Exec(`UPDATE transmissions SET channel_hash = json_extract(decoded_json, '$.channel') WHERE payload_type = 5 AND channel_hash IS NULL AND json_extract(decoded_json, '$.type') = 'CHAN'`)
		if err == nil {
			n, _ := res.RowsAffected()
			log.Printf("[migration] Backfilled channel_hash for %d CHAN packets", n)
		}
		// Backfill: extract channelHashHex for encrypted (GRP_TXT) packets, prefixed with 'enc_'
		res, err = db.Exec(`UPDATE transmissions SET channel_hash = 'enc_' || json_extract(decoded_json, '$.channelHashHex') WHERE payload_type = 5 AND channel_hash IS NULL AND json_extract(decoded_json, '$.type') = 'GRP_TXT'`)
		if err == nil {
			n, _ := res.RowsAffected()
			log.Printf("[migration] Backfilled channel_hash for %d GRP_TXT packets", n)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('channel_hash_v1')`)
		log.Println("[migration] channel_hash column added and backfilled")
	}

	// Migration: dropped_packets table for signature validation failures (#793)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'dropped_packets_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Creating dropped_packets table...")
		_, err := db.Exec(`
			CREATE TABLE IF NOT EXISTS dropped_packets (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				hash TEXT,
				raw_hex TEXT,
				reason TEXT NOT NULL,
				observer_id TEXT,
				observer_name TEXT,
				node_pubkey TEXT,
				node_name TEXT,
				dropped_at DATETIME DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_dropped_observer ON dropped_packets(observer_id);
			CREATE INDEX IF NOT EXISTS idx_dropped_node ON dropped_packets(node_pubkey);
		`)
		if err != nil {
			return fmt.Errorf("dropped_packets schema: %w", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('dropped_packets_v1')`)
		log.Println("[migration] dropped_packets table created")
	}

	// Migration: observations.raw_hex (#881) is now owned by
	// internal/dbschema/dbschema.go (#1321). The server PRAGMA-detects
	// this column as hasObsRawHex; keeping a single canonical Apply
	// path closes the startup race where the server's detector ran
	// before this ALTER finished.

	// Migration: transmissions.scope_name (#899) is now owned by
	// internal/dbschema/dbschema.go (#1321). See above.

	// Migration: add last_packet_at column to observers (#last-packet-at)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observers_last_packet_at_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding last_packet_at column to observers...")
		_, alterErr := db.Exec(`ALTER TABLE observers ADD COLUMN last_packet_at TEXT DEFAULT NULL`)
		if alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column") {
			return fmt.Errorf("observers last_packet_at ALTER: %w", alterErr)
		}
		// Backfill: set last_packet_at = last_seen only for observers that actually have
		// observation rows (packet_count alone is unreliable — UpsertObserver sets it to 1
		// on INSERT even for status-only observers).
		res, err := db.Exec(`UPDATE observers SET last_packet_at = last_seen
			WHERE last_packet_at IS NULL
			AND rowid IN (SELECT DISTINCT observer_idx FROM observations WHERE observer_idx IS NOT NULL)`)
		if err == nil {
			n, _ := res.RowsAffected()
			log.Printf("[migration] Backfilled last_packet_at for %d observers with packets", n)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observers_last_packet_at_v1')`)
		log.Println("[migration] observers.last_packet_at column added")
	}

	// Migration: per-observer naive-clock skew tracking (#1478).
	// When the ingestor clamps a packet's envelope timestamp because the
	// observer emitted a zone-less local-time string off from UTC by >15min
	// (resolveRxTime in main.go), we record the event here so the UI can
	// surface a ⚠️ chip + banner. Decays after 24h via server-side read sweep.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observers_clock_naive_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding clock-naive columns to observers (#1478)...")
		// Each ALTER is independent — ignore "duplicate column" so reruns are safe.
		for _, stmt := range []string{
			`ALTER TABLE observers ADD COLUMN clock_skew_seconds INTEGER DEFAULT NULL`,
			`ALTER TABLE observers ADD COLUMN clock_skew_count_24h INTEGER DEFAULT 0`,
			`ALTER TABLE observers ADD COLUMN clock_last_naive_at TEXT DEFAULT NULL`,
		} {
			if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("clock_naive migration: %w", err)
			}
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observers_clock_naive_v1')`)
		log.Println("[migration] observers.clock_naive columns added")
	}

	// Migration: backfill observations.path_json from raw_hex (#888)
	// NOTE: This runs ASYNC via BackfillPathJSONAsync() to avoid blocking MQTT startup.
	// See staging outage where ~502K rows blocked ingest for 15+ hours.

	// One-time cleanup: delete legacy packets with empty hash or empty first_seen (#994)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'cleanup_legacy_null_hash_ts'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Cleaning up legacy packets with empty hash/timestamp...")
		db.Exec(`DELETE FROM observations WHERE transmission_id IN (SELECT id FROM transmissions WHERE hash = '' OR first_seen = '')`)
		res, err := db.Exec(`DELETE FROM transmissions WHERE hash = '' OR first_seen = ''`)
		if err == nil {
			deleted, _ := res.RowsAffected()
			log.Printf("[migration] deleted %d legacy packets with empty hash/timestamp", deleted)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('cleanup_legacy_null_hash_ts')`)
	}

	// Migration: foreign_advert column on nodes/inactive_nodes (#730)
	// Marks nodes whose ADVERT GPS lies outside the configured geofilter polygon.
	// Default 0; set to 1 by the ingestor when GeoFilter is configured and
	// PassesFilter() returns false. Allows operators to surface bridged/leaked
	// adverts without silently dropping them.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'foreign_advert_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding foreign_advert column to nodes/inactive_nodes...")
		if _, err := db.Exec(`ALTER TABLE nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
			log.Printf("[migration] nodes.foreign_advert: %v (may already exist)", err)
		}
		if _, err := db.Exec(`ALTER TABLE inactive_nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
			log.Printf("[migration] inactive_nodes.foreign_advert: %v (may already exist)", err)
		}
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_nodes_foreign_advert ON nodes(foreign_advert) WHERE foreign_advert = 1`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('foreign_advert_v1')`)
		log.Println("[migration] foreign_advert column added")
	}

	// Migration: from_pubkey column on transmissions (#1143).
	// Replaces the unsound `decoded_json LIKE '%pubkey%'` attribution path with
	// an exact-match indexed column. Synchronously adds the column + index;
	// row-level backfill is run by the SERVER asynchronously
	// (cmd/server/from_pubkey_migration.go) so we don't block ingestor boot.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'from_pubkey_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding from_pubkey column + index to transmissions (#1143)...")
		if _, err := db.Exec(`ALTER TABLE transmissions ADD COLUMN from_pubkey TEXT`); err != nil {
			log.Printf("[migration] transmissions.from_pubkey: %v (may already exist)", err)
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_transmissions_from_pubkey ON transmissions(from_pubkey)`); err != nil {
			log.Printf("[migration] idx_transmissions_from_pubkey: %v", err)
		}
		db.Exec(`INSERT INTO _migrations (name) VALUES ('from_pubkey_v1')`)
		log.Println("[migration] from_pubkey column + index added")
	}

	// Migration: nodes.default_scope (#899 Feature 3) is now owned by
	// internal/dbschema/dbschema.go (#1321). The server PRAGMA-detects
	// this column as hasDefaultScope; keeping a single canonical Apply
	// path closes the startup race that #1321 documented.

	// Migration: normalize known channel_hash values for existing rows.
	// Before this PR, config key "public" was stored as channel_hash="public".
	// After this PR, new rows use channel_hash="Public". Without backfill,
	// channel grouping queries split into two buckets across the upgrade boundary.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'channel_hash_casing_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Normalizing known channel_hash values...")
		res, err := db.Exec(`UPDATE transmissions SET channel_hash = 'Public' WHERE channel_hash = 'public' AND payload_type = 5`)
		if err != nil {
			log.Printf("[migration] ERROR: failed to normalize channel_hash: %v", err)
			return fmt.Errorf("migration channel_hash_casing_v1 UPDATE failed: %w", err)
		}
		n, _ := res.RowsAffected()
		log.Printf("[migration] Normalized %d channel_hash rows from 'public' to 'Public'", n)
		if _, err := db.Exec(`INSERT OR IGNORE INTO _migrations (name) VALUES ('channel_hash_casing_v1')`); err != nil {
			log.Printf("[migration] WARNING: failed to record migration: %v", err)
		}
		log.Println("[migration] channel_hash casing normalization complete")
	}

	// Migration: transmissions.code1/code2 — the transport codes were decoded
	// and discarded; storing them makes scope forwarding queryable per repeater.
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'transport_codes_v1'")
	if row.Scan(new(int)) != nil {
		log.Println("[migration] Adding code1/code2 columns to transmissions...")
		// Each ALTER is independent — ignore "duplicate column" so reruns (and
		// the fresh-database path, where the base schema already created the
		// columns) are safe. Any other failure must stop before the guard row
		// is inserted, or prepareStatements' code1/code2 INSERT fails forever
		// on every subsequent restart with no way to retry the migration.
		for _, stmt := range []string{
			`ALTER TABLE transmissions ADD COLUMN code1 TEXT DEFAULT NULL`,
			`ALTER TABLE transmissions ADD COLUMN code2 TEXT DEFAULT NULL`,
		} {
			if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("transport_codes_v1 migration: %w", err)
			}
		}
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_tx_code1 ON transmissions(code1) WHERE code1 IS NOT NULL`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('transport_codes_v1')`)
		log.Println("[migration] code1/code2 columns added")
	}

	return nil
}

func (s *Store) prepareStatements() error {
	var err error

	s.stmtGetTxByHash, err = s.db.Prepare("SELECT id, first_seen FROM transmissions WHERE hash = ?")
	if err != nil {
		return err
	}

	s.stmtInsertTransmission, err = s.db.Prepare(`
		INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, channel_hash, scope_name, from_pubkey, last_seen, code1, code2)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}

	s.stmtUpdateTxFirstSeen, err = s.db.Prepare("UPDATE transmissions SET first_seen = ? WHERE id = ?")
	if err != nil {
		return err
	}

	// #1690: bump transmissions.last_seen to MAX(current, ?) on every
	// observation insert so cold-load can filter on effective recency.
	// This is NOT a migration — it's the steady-state writer path. The
	// one-time backfill (BackfillPathJSONAsync-shaped) runs via
	// RunAsyncMigration above; this prepared-statement UPDATE is the
	// per-row maintenance that keeps the column current after the
	// backfill completes. Recorded in _migrations under
	// "tx_last_seen_backfill_v1".
	// PREFLIGHT: async=true reason="prepared-statement row-level UPDATE BY PRIMARY KEY (transmissions.id) — single-row touch per observation, indexed by PK, constant-time at any scale. Not a migration."
	s.stmtTouchNodeLastSeen, err = s.db.Prepare(
		"UPDATE nodes SET last_seen = ? WHERE public_key = ? AND (last_seen IS NULL OR last_seen < ?)")
	if err != nil {
		return fmt.Errorf("preparing touch node last_seen: %w", err)
	}

	s.stmtBumpTxLastSeen, err = s.db.Prepare("UPDATE transmissions SET last_seen = ? WHERE id = ? AND last_seen < ?")
	if err != nil {
		return err
	}

	s.stmtInsertObservation, err = s.db.Prepare(`
		INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp, raw_hex, resolved_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(transmission_id, observer_idx, COALESCE(path_json, '')) DO UPDATE SET
			snr           = COALESCE(excluded.snr,           snr),
			rssi          = COALESCE(excluded.rssi,          rssi),
			score         = COALESCE(excluded.score,         score),
			raw_hex       = COALESCE(excluded.raw_hex,       raw_hex),
			resolved_path = COALESCE(excluded.resolved_path, resolved_path)
	`)
	if err != nil {
		return err
	}

	s.stmtUpsertNode, err = s.db.Prepare(`
		INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(public_key) DO UPDATE SET
			name = COALESCE(?, name),
			role = COALESCE(?, role),
			lat = COALESCE(?, lat),
			lon = COALESCE(?, lon),
			last_seen = MAX(MIN(COALESCE(last_seen, ''), ?), ?)
	`)
	if err != nil {
		return err
	}

	s.stmtIncrementAdvertCount, err = s.db.Prepare(`
		UPDATE nodes SET advert_count = advert_count + 1 WHERE public_key = ?
	`)
	if err != nil {
		return err
	}

	s.stmtUpsertObserver, err = s.db.Prepare(`
		INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor, can_relay, can_relay_seen)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, 1), CASE WHEN ? IS NULL THEN 0 ELSE 1 END)
		ON CONFLICT(id) DO UPDATE SET
			name = COALESCE(?, name),
			iata = COALESCE(?, iata),
			last_seen = MAX(MIN(COALESCE(last_seen, ''), ?), ?),
			packet_count = packet_count + 1,
			model = COALESCE(?, model),
			firmware = COALESCE(?, firmware),
			client_version = COALESCE(?, client_version),
			radio = COALESCE(?, radio),
			battery_mv = COALESCE(?, battery_mv),
			uptime_secs = COALESCE(?, uptime_secs),
			noise_floor = COALESCE(?, noise_floor),
			can_relay = COALESCE(?, can_relay),
			can_relay_seen = CASE WHEN ? IS NULL THEN can_relay_seen ELSE 1 END
	`)
	if err != nil {
		return err
	}

	s.stmtGetObserverRowid, err = s.db.Prepare("SELECT rowid FROM observers WHERE id = ?")
	if err != nil {
		return err
	}

	// Args: ingestNow, rxTime, ingestNow, rxTime, rowid
	// MIN(existing, ingestNow) clamps any future value already in the DB before
	// taking MAX with rxTime, so the guard never locks in a past bug's stale future.
	s.stmtUpdateObserverLastSeen, err = s.db.Prepare(`
		UPDATE observers SET
			last_seen      = MAX(MIN(COALESCE(last_seen, ''), ?), ?),
			last_packet_at = MAX(MIN(COALESCE(last_packet_at, ''), ?), ?)
		WHERE rowid = ?`)
	if err != nil {
		return err
	}

	s.stmtUpdateNodeTelemetry, err = s.db.Prepare(`
		UPDATE nodes SET
			battery_mv = COALESCE(?, battery_mv),
			temperature_c = COALESCE(?, temperature_c)
		WHERE public_key = ?
	`)
	if err != nil {
		return err
	}

	s.stmtUpsertMetrics, err = s.db.Prepare(`
		INSERT OR REPLACE INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv, packets_sent, packets_recv)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}

	return nil
}

// InsertTransmission inserts a decoded packet into transmissions + observations.
// Returns true if a new transmission was created (not a duplicate hash).
func (s *Store) InsertTransmission(data *PacketData) (bool, error) {
	hash := data.Hash
	if hash == "" {
		return false, nil
	}

	// Wait/hold instrumentation (#1340). The hot path uses prepared
	// statements that auto-commit; gate the whole function under
	// writerMu so concurrent mqtt_handler inserts queue behind any
	// other writer (vacuum, prune, neighbor-builder) and the wait is
	// Go-visible.
	mqttWaitStart := time.Now()
	writerMu.Lock()
	mqttWait := time.Since(mqttWaitStart)
	mqttHoldStart := time.Now()
	defer func() {
		mqttHold := time.Since(mqttHoldStart)
		writerMu.Unlock()
		recordWriterTiming("mqtt_handler", mqttWait, mqttHold, "InsertTransmission")
	}()

	rxTime := data.Timestamp
	ingestNow := time.Now().UTC().Format(time.RFC3339)
	if rxTime == "" {
		rxTime = ingestNow
	}

	var txID int64
	isNew := false

	// Check for existing transmission
	var existingID int64
	var existingFirstSeen string
	err := s.stmtGetTxByHash.QueryRow(hash).Scan(&existingID, &existingFirstSeen)
	if err == nil {
		// Existing transmission
		txID = existingID
		if rxTime < existingFirstSeen {
			_, _ = s.stmtUpdateTxFirstSeen.Exec(rxTime, txID)
		}
	} else {
		// New transmission
		isNew = true
		result, err := s.stmtInsertTransmission.Exec(
			data.RawHex, hash, rxTime,
			data.RouteType, data.PayloadType, data.PayloadVersion,
			data.DecodedJSON, nilIfEmpty(data.ChannelHash),
			scopeNameForDB(data),
			nilIfEmpty(data.FromPubkey),
			epochSecondsForLastSeen(rxTime),
			nilIfEmpty(data.Code1),
			nilIfEmpty(data.Code2),
		)
		if err != nil {
			s.Stats.WriteErrors.Add(1)
			return false, fmt.Errorf("insert transmission: %w", err)
		}
		txID, _ = result.LastInsertId()
		s.Stats.TransmissionsInserted.Add(1)
	}

	if !isNew {
		s.Stats.DuplicateTransmissions.Add(1)
	}

	// Resolve observer_idx and update last_seen
	var observerIdx *int64
	if data.ObserverID != "" {
		var rowid int64
		err := s.stmtGetObserverRowid.QueryRow(data.ObserverID).Scan(&rowid)
		if err == nil {
			observerIdx = &rowid
			// observer.last_seen and last_packet_at answer "when did the analyzer
			// last hear from this observer" — both are ingest-time questions.
			// Per-packet rxTime is stored separately on observations/transmissions
			// using envelope time (see InsertTransmission above). See #1465.
			_, _ = s.stmtUpdateObserverLastSeen.Exec(ingestNow, ingestNow, ingestNow, ingestNow, rowid)
		}
	}

	// Insert observation
	epochTs := time.Now().Unix()
	if t, err := time.Parse(time.RFC3339, rxTime); err == nil {
		epochTs = t.Unix()
	}

	// Resolve hop prefixes to full pubkeys for `observations.resolved_path`.
	// Per #1547: this writer was lost in the #1289 refactor and lives in
	// the ingestor now. Per #1560: use the context-aware resolver so
	// 1-byte prefix collisions are disambiguated via NeighborGraph
	// adjacency (anchored on from_pubkey for ADVERTs, previous hop
	// otherwise). Empty resolved JSON → NULL via nilIfEmpty.
	resolved := resolvePathWithContext(
		parsePathArray(data.PathJSON),
		strings.ToLower(data.FromPubkey),
		s.neighborGraph.load(),
		s.prefixIdx.load(),
	)
	resolvedJSON := marshalResolvedPath(resolved)

	_, err = s.stmtInsertObservation.Exec(
		txID, observerIdx, data.Direction,
		data.SNR, data.RSSI, data.Score,
		data.PathJSON, epochTs, nilIfEmpty(data.RawHex),
		nilIfEmpty(resolvedJSON),
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		log.Printf("[db] observation insert (non-fatal): %v", err)
	} else {
		s.Stats.ObservationsInserted.Add(1)
		// #1598: a resolved hop proves the node was forwarding traffic at
		// rxTime. Refresh its last_seen so staleness/eviction logic sees
		// relay activity, not just ADVERTs.
		s.touchRelayNodesLocked(resolvedPubkeys(resolved), rxTime)
		// #1690: bump transmissions.last_seen so cold-load can filter on
		// effective recency. Conditional `last_seen < ?` so we never go
		// backwards on out-of-order ingest.
		if _, err := s.stmtBumpTxLastSeen.Exec(epochTs, txID, epochTs); err != nil {
			log.Printf("[db] tx last_seen bump (non-fatal): %v", err)
		}
	}

	// Each prepared-stmt Exec auto-commits. Count one WAL commit per
	// successful InsertTransmission so the perf page sees commit pressure.
	s.Stats.WALCommits.Add(1)

	return isNew, nil
}

// UpsertNode inserts or updates a node.
func (s *Store) UpsertNode(pubKey, name, role string, lat, lon *float64, lastSeen string) error {
	ingestNow := time.Now().UTC().Format(time.RFC3339)
	now := lastSeen
	if now == "" {
		now = ingestNow
	}
	_, err := s.stmtUpsertNode.Exec(
		pubKey, name, role, lat, lon, now, now,
		name, role, lat, lon, ingestNow, now,
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
	} else {
		s.Stats.NodeUpserts.Add(1)
	}
	return err
}

// IncrementAdvertCount increments advert_count for a node by public key.
func (s *Store) IncrementAdvertCount(pubKey string) error {
	_, err := s.stmtIncrementAdvertCount.Exec(pubKey)
	return err
}

// MarkNodeForeign sets foreign_advert=1 on the node row identified by pubKey.
// Used when an ADVERT arrives whose GPS lies outside the configured geofilter
// polygon (#730). Idempotent — safe to call repeatedly. No-op if pubKey is
// empty.
func (s *Store) MarkNodeForeign(pubKey string) error {
	if pubKey == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE nodes SET foreign_advert = 1 WHERE public_key = ?`, pubKey)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
	}
	return err
}

// UpdateNodeTelemetry updates battery and temperature for a node.
func (s *Store) UpdateNodeTelemetry(pubKey string, batteryMv *int, temperatureC *float64) error {
	var bv, tc interface{}
	if batteryMv != nil {
		bv = *batteryMv
	}
	if temperatureC != nil {
		tc = *temperatureC
	}
	_, err := s.stmtUpdateNodeTelemetry.Exec(bv, tc, pubKey)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
	}
	return err
}

// ObserverMeta holds optional observer hardware metadata.
type ObserverMeta struct {
	Model         *string  // e.g., L1
	Firmware      *string  // firmware version string
	ClientVersion *string  // client app version string
	Radio         *string  // radio chipset/platform string
	BatteryMv     *int     // millivolts, always integer
	UptimeSecs    *int64   // seconds, always integer
	NoiseFloor    *float64 // dBm, may have decimals
	TxAirSecs     *int     // cumulative TX seconds since boot
	RxAirSecs     *int     // cumulative RX seconds since boot
	RecvErrors    *int     // cumulative CRC/decode failures since boot
	PacketsSent   *int     // cumulative packets sent since boot
	PacketsRecv   *int     // cumulative packets received since boot
	// CanRelay reflects the firmware 1.16 /status `repeat` flag (#1290).
	// nil means the firmware did not send the field — caller must
	// preserve the existing observers.can_relay value (default 1).
	// true → relay-capable (`repeat:on`); false → listener-only
	// (`repeat:off`), which causes the server-side disambiguator to
	// exclude this observer's pubkey from path-hop candidate sets.
	CanRelay *bool
}

// UpsertObserver inserts or updates an observer using the current wall-clock
// time as last_seen. Use UpsertObserverAt when the message envelope provides
// an observer receive-time (e.g. MQTT status and data packet handlers).
func (s *Store) UpsertObserver(id, name, iata string, meta *ObserverMeta) error {
	return s.UpsertObserverAt(id, name, iata, meta, time.Now().UTC().Format(time.RFC3339))
}

// UpsertObserverAt inserts or updates an observer with an explicit lastSeen
// timestamp (typically the observer receive-time from the MQTT envelope). The
// SQL uses MAX so last_seen never moves backwards — a retained or replayed
// message whose rxTime pre-dates the existing last_seen is a no-op for that
// field, preventing offline observers from flashing as Online on reconnect.
func (s *Store) UpsertObserverAt(id, name, iata string, meta *ObserverMeta, lastSeen string) error {
	ingestNow := time.Now().UTC().Format(time.RFC3339)
	if lastSeen == "" {
		lastSeen = ingestNow
	}
	normalizedIATA := strings.TrimSpace(strings.ToUpper(iata))

	model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay := observerMetaColumns(meta)

	_, err := s.stmtUpsertObserver.Exec(
		id, name, normalizedIATA, lastSeen, lastSeen, model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay, canRelay,
		name, normalizedIATA, ingestNow, lastSeen, model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay, canRelay,
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return err
	}
	s.Stats.ObserverUpserts.Add(1)

	// Reactivate if this observer was previously marked inactive
	s.db.Exec(`UPDATE observers SET inactive = 0 WHERE id = ? AND inactive = 1`, id)
	return nil
}

// UpsertObserverRetained applies the metadata from a RETAINED status message
// without treating it as a sign of life. The broker replays retained messages
// on every subscribe, so an ingestor restart would otherwise stamp last_seen
// with the restart time for every observer that ever published one — making
// dead observers permanently un-ageable by RemoveStaleObservers, and undoing
// any inactive flag it did manage to set.
//
// Deliberately narrower than UpsertObserverAt: it updates metadata columns on
// an existing row only. It does not advance last_seen, does not clear
// inactive, does not bump packet_count, and does not INSERT — a retained-only
// observer the analyzer has never heard from live describes a past that may be
// months old and does not belong in the list. A live message from the same
// observer arrives moments later and creates the row through the normal path.
func (s *Store) UpsertObserverRetained(id, name, iata string, meta *ObserverMeta) error {
	normalizedIATA := strings.TrimSpace(strings.ToUpper(iata))
	model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay := observerMetaColumns(meta)

	_, err := s.db.Exec(`
		UPDATE observers SET
			name = COALESCE(?, name),
			iata = COALESCE(?, iata),
			model = COALESCE(?, model),
			firmware = COALESCE(?, firmware),
			client_version = COALESCE(?, client_version),
			radio = COALESCE(?, radio),
			battery_mv = COALESCE(?, battery_mv),
			uptime_secs = COALESCE(?, uptime_secs),
			noise_floor = COALESCE(?, noise_floor),
			can_relay = COALESCE(?, can_relay),
			can_relay_seen = CASE WHEN ? IS NULL THEN can_relay_seen ELSE 1 END
		WHERE id = ?`,
		name, normalizedIATA, model, firmware, clientVersion, radio,
		batteryMv, uptimeSecs, noiseFloor, canRelay, canRelay, id,
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return err
	}
	return nil
}

// observerMetaColumns flattens an *ObserverMeta into the driver args the
// observer upserts bind. A nil field stays nil so the COALESCE in the SQL
// leaves the existing column untouched (#1290).
func observerMetaColumns(meta *ObserverMeta) (model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay interface{}) {
	if meta != nil {
		if meta.Model != nil {
			model = *meta.Model
		}
		if meta.Firmware != nil {
			firmware = *meta.Firmware
		}
		if meta.ClientVersion != nil {
			clientVersion = *meta.ClientVersion
		}
		if meta.Radio != nil {
			radio = *meta.Radio
		}
		if meta.BatteryMv != nil {
			batteryMv = *meta.BatteryMv
		}
		if meta.UptimeSecs != nil {
			uptimeSecs = *meta.UptimeSecs
		}
		if meta.NoiseFloor != nil {
			noiseFloor = *meta.NoiseFloor
		}
		// Issue #1290: nil → leave DB column unchanged (COALESCE in
		// the prepared stmt); 0/1 written when firmware provided
		// the `repeat` field. INSERT branch defaults to 1 via the
		// COALESCE in the VALUES clause.
		if meta.CanRelay != nil {
			if *meta.CanRelay {
				canRelay = 1
			} else {
				canRelay = 0
			}
		}
	}
	return model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor, canRelay
}

// Close checkpoints the WAL and closes the database.
func (s *Store) Close() error {
	s.backfillWg.Wait()
	s.Checkpoint()
	return s.db.Close()
}

// RoundToInterval rounds a time to the nearest sample interval boundary.
func RoundToInterval(t time.Time, intervalSec int) time.Time {
	if intervalSec <= 0 {
		intervalSec = 300
	}
	epoch := t.Unix()
	half := int64(intervalSec) / 2
	rounded := ((epoch + half) / int64(intervalSec)) * int64(intervalSec)
	return time.Unix(rounded, 0).UTC()
}

// MetricsData holds the fields to insert into observer_metrics.
type MetricsData struct {
	ObserverID  string
	NoiseFloor  *float64
	TxAirSecs   *int
	RxAirSecs   *int
	RecvErrors  *int
	BatteryMv   *int
	PacketsSent *int
	PacketsRecv *int
}

// InsertMetrics inserts a metrics sample for an observer using ingestor wall clock.
func (s *Store) InsertMetrics(data *MetricsData) error {
	ts := RoundToInterval(time.Now().UTC(), s.sampleIntervalSec)
	tsStr := ts.Format(time.RFC3339)

	var nf, txAir, rxAir, recvErr, batt, pktSent, pktRecv interface{}
	if data.NoiseFloor != nil {
		nf = *data.NoiseFloor
	}
	if data.TxAirSecs != nil {
		txAir = *data.TxAirSecs
	}
	if data.RxAirSecs != nil {
		rxAir = *data.RxAirSecs
	}
	if data.RecvErrors != nil {
		recvErr = *data.RecvErrors
	}
	if data.BatteryMv != nil {
		batt = *data.BatteryMv
	}
	if data.PacketsSent != nil {
		pktSent = *data.PacketsSent
	}
	if data.PacketsRecv != nil {
		pktRecv = *data.PacketsRecv
	}

	_, err := s.stmtUpsertMetrics.Exec(data.ObserverID, tsStr, nf, txAir, rxAir, recvErr, batt, pktSent, pktRecv)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return fmt.Errorf("insert metrics: %w", err)
	}
	return nil
}

// PruneOldMetrics deletes observer_metrics rows older than retentionDays.
func (s *Store) PruneOldMetrics(retentionDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	// Tagged for /api/perf writer-lock visibility (#1340).
	result, err := s.instrumentedExec("prune_metrics", `DELETE FROM observer_metrics WHERE timestamp < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune metrics: %w", err)
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		log.Printf("[metrics] Pruned %d rows older than %d days", n, retentionDays)
	}
	return n, nil
}

// CheckAutoVacuum inspects the current auto_vacuum mode and logs a warning
// if not INCREMENTAL. Performs opt-in full VACUUM if db.vacuumOnStartup is set (#919).
func (s *Store) CheckAutoVacuum(cfg *Config) {
	var autoVacuum int
	if err := s.db.QueryRow("PRAGMA auto_vacuum").Scan(&autoVacuum); err != nil {
		log.Printf("[db] warning: could not read auto_vacuum: %v", err)
		return
	}

	if autoVacuum == 2 {
		log.Printf("[db] auto_vacuum=INCREMENTAL")
		return
	}

	modes := map[int]string{0: "NONE", 1: "FULL", 2: "INCREMENTAL"}
	mode := modes[autoVacuum]
	if mode == "" {
		mode = fmt.Sprintf("UNKNOWN(%d)", autoVacuum)
	}

	log.Printf("[db] auto_vacuum=%s — DB needs one-time VACUUM to enable incremental auto-vacuum. "+
		"Set db.vacuumOnStartup: true in config to migrate (will block startup for several minutes on large DBs). "+
		"See https://github.com/Kpa-clawbot/CoreScope/issues/919", mode)

	if cfg.DB != nil && cfg.DB.VacuumOnStartup {
		// WARNING: Full VACUUM creates a temporary copy of the entire DB file.
		// Requires ~2× the DB file size in free disk space or it will fail.
		log.Printf("[db] vacuumOnStartup=true — starting one-time full VACUUM (ensure 2x DB size free disk space)...")
		start := time.Now()

		if _, err := s.instrumentedExec("vacuum", "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
			log.Printf("[db] VACUUM failed: could not set auto_vacuum: %v", err)
			return
		}
		if _, err := s.instrumentedExec("vacuum", "VACUUM"); err != nil {
			log.Printf("[db] VACUUM failed: %v", err)
			return
		}

		elapsed := time.Since(start)
		log.Printf("[db] VACUUM complete in %v — auto_vacuum is now INCREMENTAL", elapsed.Round(time.Millisecond))
	}
}

// RunIncrementalVacuum returns free pages to the OS (#919).
// Safe to call on auto_vacuum=NONE databases (noop).
func (s *Store) RunIncrementalVacuum(pages int) {
	// Tagged for /api/perf writer-lock visibility (#1340).
	if _, err := s.instrumentedExec("vacuum", fmt.Sprintf("PRAGMA incremental_vacuum(%d)", pages)); err != nil {
		log.Printf("[vacuum] incremental_vacuum error: %v", err)
	}
}

// Checkpoint runs a WAL checkpoint (TRUNCATE mode).
// Returns the number of WAL frames checkpointed (0 if WAL was already empty).
// TRUNCATE resets the WAL file to zero bytes when all frames are checkpointed;
// if active readers hold frames, it checkpoints what it can and leaves the rest.
func (s *Store) Checkpoint() int {
	var busy, walFrames, checkpointed int
	if err := s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &walFrames, &checkpointed); err != nil {
		log.Printf("[db] WAL checkpoint error: %v", err)
		return 0
	}
	if walFrames > 0 {
		log.Printf("[db] WAL checkpoint: %d/%d frames checkpointed (blocked=%v)", checkpointed, walFrames, busy != 0)
	}
	return checkpointed
}

// BackfillPathJSONAsync launches the path_json backfill in a background goroutine.
// It processes observations with NULL/empty path_json that have raw_hex available,
// decoding hop paths and updating the column. Safe to run concurrently with ingest
// because new observations get path_json at write time; this only touches NULL rows.
// Idempotent: skips if migration already recorded.
func (s *Store) BackfillPathJSONAsync() {
	s.backfillWg.Add(1)
	go func() {
		defer s.backfillWg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[backfill] path_json async panic recovered: %v", r)
			}
		}()

		var migDone int
		row := s.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'backfill_path_json_from_raw_hex_v1'")
		if row.Scan(&migDone) == nil {
			return // already done
		}

		log.Println("[backfill] Starting async path_json backfill from raw_hex...")
		updated := 0
		errored := false
		const batchSize = 1000
		batchNum := 0
		for {
			rows, err := s.db.Query(`
				SELECT o.id, o.raw_hex
				FROM observations o
				JOIN transmissions t ON o.transmission_id = t.id
				WHERE o.raw_hex IS NOT NULL AND o.raw_hex != ''
				-- NB: '[]' is the "already attempted, no hops" sentinel; excluded
				-- to prevent the infinite re-UPDATE loop fixed in #1119.
				AND (o.path_json IS NULL OR o.path_json = '')
				AND t.payload_type != 9
				LIMIT ?`, batchSize)
			if err != nil {
				log.Printf("[backfill] path_json query error: %v", err)
				errored = true
				break
			}
			type pendingRow struct {
				id     int64
				rawHex string
			}
			var batch []pendingRow
			for rows.Next() {
				var r pendingRow
				if err := rows.Scan(&r.id, &r.rawHex); err == nil {
					batch = append(batch, r)
				}
			}
			rows.Close()
			if len(batch) == 0 {
				break
			}
			for _, r := range batch {
				hops, err := packetpath.DecodePathFromRawHex(r.rawHex)
				if err != nil || len(hops) == 0 {
					if _, execErr := s.db.Exec(`UPDATE observations SET path_json = '[]' WHERE id = ?`, r.id); execErr != nil {
						log.Printf("[backfill] write error (id=%d): %v", r.id, execErr)
					} else {
						s.Stats.IncBackfill("path_json")
					}
					continue
				}
				b, _ := json.Marshal(hops)
				if _, execErr := s.db.Exec(`UPDATE observations SET path_json = ? WHERE id = ?`, string(b), r.id); execErr != nil {
					log.Printf("[backfill] write error (id=%d): %v", r.id, execErr)
				} else {
					updated++
					s.Stats.IncBackfill("path_json")
				}
			}
			batchNum++
			if batchNum%50 == 0 {
				log.Printf("[backfill] progress: %d observations updated so far (%d batches)", updated, batchNum)
			}
			// Throttle: yield to ingest writers between batches
			time.Sleep(50 * time.Millisecond)
		}
		log.Printf("[backfill] Async path_json backfill complete: %d observations updated", updated)
		if !errored {
			s.db.Exec(`INSERT INTO _migrations (name) VALUES ('backfill_path_json_from_raw_hex_v1')`)
		} else {
			log.Printf("[backfill] NOT recording migration due to errors — will retry on next restart")
		}
	}()
}

// BackfillDefaultScopeAsync populates default_scope for existing nodes that have
// transport-scoped ADVERT rows (scope_name IS NOT NULL AND scope_name != “).
// Runs in a background goroutine so it does not block MQTT startup.
// Uses the from_pubkey index — O(nodes × indexed lookup), not a full table scan.
//
// Concurrency: the store uses SetMaxOpenConns(1) so all DB writes — including
// MQTT packet inserts and any concurrent backfill goroutines — serialize through
// the single connection pool. busy_timeout(5000) handles transient cross-process
// contention with the read-only server process. No additional locking is needed.
func (s *Store) BackfillDefaultScopeAsync(regionKeys map[string][]byte) {
	// No region keys configured — all scope_name values will be NULL, nothing to backfill.
	if len(regionKeys) == 0 {
		return
	}
	s.backfillWg.Add(1)
	go func() {
		defer s.backfillWg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[backfill] default_scope async panic recovered: %v", r)
			}
		}()

		var done int
		if s.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'backfill_default_scope_v1'").Scan(&done) == nil {
			return // already ran
		}

		res, err := s.db.Exec(`
			UPDATE nodes SET default_scope = (
				SELECT t.scope_name FROM transmissions t
				WHERE t.from_pubkey = nodes.public_key
				  AND t.payload_type = 4
				  AND t.scope_name IS NOT NULL AND t.scope_name != ''
				ORDER BY t.first_seen DESC LIMIT 1  -- most-recently observed scope wins; first_seen is insertion time
			) WHERE EXISTS (
				SELECT 1 FROM transmissions t
				WHERE t.from_pubkey = nodes.public_key
				  AND t.payload_type = 4
				  AND t.scope_name IS NOT NULL AND t.scope_name != ''
			)`)
		if err != nil {
			log.Printf("[backfill] default_scope: %v", err)
			return
		}
		n, _ := res.RowsAffected()
		s.Stats.IncBackfill("default_scope")
		log.Printf("[backfill] default_scope populated for %d nodes", n)
		s.db.Exec(`INSERT INTO _migrations (name) VALUES ('backfill_default_scope_v1')`)
	}()
}

// BackfillTransportCodesAsync populates transmissions.code1/code2 for rows
// inserted before the transport_codes_v1 migration, by re-parsing raw_hex.
// Runs in a background goroutine so it does not block MQTT startup.
//
// Batched: SQLite holds a single write connection (SetMaxOpenConns(1)), so a
// multi-million-row UPDATE in one transaction would stall live ingest for the
// whole run. 5k-row batches keep each transaction short.
func (s *Store) BackfillTransportCodesAsync() {
	s.backfillWg.Add(1)
	go func() {
		defer s.backfillWg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[backfill] transport_codes async panic recovered: %v", r)
			}
		}()

		var done int
		if s.db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'backfill_transport_codes_v1'").Scan(&done) == nil {
			return // already ran
		}

		total, err := s.backfillTransportCodes(5000)
		if err != nil {
			log.Printf("[backfill] transport_codes: %v", err)
			return // NOT recording the guard row — retry from scratch on next restart
		}
		s.Stats.IncBackfill("transport_codes")
		log.Printf("[backfill] transport_codes populated for %d transmissions", total)
		s.db.Exec(`INSERT INTO _migrations (name) VALUES ('backfill_transport_codes_v1')`)
	}()
}

// backfillTransportCodes does the actual batched decode-and-update work,
// returning the number of rows updated. Uses keyset pagination on id rather
// than counting decoded items per page: a page can hit the LIMIT while
// containing undecodable rows, so "items returned < batchSize" does NOT mean
// "no rows remain" — that conflation previously caused the backfill to record
// its completion migration after silently truncating on the first page with
// an undecodable row. Termination is driven by how many rows the page
// actually scanned (seen), not by how many decoded successfully; lastID
// advances past undecodable rows too, so they are stepped over rather than
// re-selected forever.
//
// Invariant: the caller records the backfill_transport_codes_v1 guard row iff
// this returns a nil error, i.e. iff the loop ran to genuine exhaustion of all
// matching rows. Every error path below returns non-nil immediately, without
// finishing the run, so the guard is withheld and the next startup retries.
func (s *Store) backfillTransportCodes(batchSize int) (int, error) {
	var total int
	var lastID int64
	for {
		rows, err := s.db.Query(`
			SELECT id, raw_hex FROM transmissions
			WHERE id > ? AND code1 IS NULL AND route_type IN (0, 3)
			ORDER BY id
			LIMIT ?`, lastID, batchSize)
		if err != nil {
			return total, fmt.Errorf("transport_codes select: %w", err)
		}
		type item struct {
			id           int64
			code1, code2 string
		}
		var items []item
		var seen int
		for rows.Next() {
			var id int64
			var raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return total, fmt.Errorf("transport_codes scan: %w", err)
			}
			seen++
			lastID = id // advance past undecodable rows too, or they are re-selected forever
			decoded, err := DecodePacket(raw, nil, false)
			if err != nil || decoded.TransportCodes == nil {
				continue
			}
			items = append(items, item{id, decoded.TransportCodes.Code1, decoded.TransportCodes.Code2})
		}
		rows.Close()
		if seen == 0 {
			break // no rows left matching the filter
		}

		if len(items) > 0 {
			tx, err := s.db.Begin()
			if err != nil {
				return total, fmt.Errorf("transport_codes begin: %w", err)
			}
			stmt, err := tx.Prepare(`UPDATE transmissions SET code1 = ?, code2 = ? WHERE id = ?`)
			if err != nil {
				tx.Rollback()
				return total, fmt.Errorf("transport_codes prepare: %w", err)
			}
			for _, it := range items {
				if _, err := stmt.Exec(it.code1, it.code2, it.id); err != nil {
					stmt.Close()
					tx.Rollback()
					return total, fmt.Errorf("transport_codes update id=%d: %w", it.id, err)
				}
			}
			stmt.Close()
			if err := tx.Commit(); err != nil {
				return total, fmt.Errorf("transport_codes commit: %w", err)
			}
			total += len(items)
		}
		if seen < batchSize {
			break // last partial page
		}
	}
	return total, nil
}

// LogStats logs current operational metrics.
func (s *Store) LogStats() {
	log.Printf("[stats] tx_inserted=%d tx_dupes=%d obs_inserted=%d node_upserts=%d observer_upserts=%d write_errors=%d sig_drops=%d",
		s.Stats.TransmissionsInserted.Load(),
		s.Stats.DuplicateTransmissions.Load(),
		s.Stats.ObservationsInserted.Load(),
		s.Stats.NodeUpserts.Load(),
		s.Stats.ObserverUpserts.Load(),
		s.Stats.WriteErrors.Load(),
		s.Stats.SignatureDrops.Load(),
	)
}

// touchRelayNodesLocked refreshes nodes.last_seen for nodes observed as
// relay hops, so a node that forwards traffic stays fresh even when it
// adverts rarely or not at all.
//
// MUST be called with writerMu held. InsertTransmission holds it for its
// entire body, so the debounce map needs no lock of its own; the name
// carries the requirement for future callers.
//
// Ownership (#1283/#1287/#1289): nodes is written by the ingestor only.
// The server opens SQLite mode=ro; its former touchRelayLastSeen has been
// failing on every call since that refactor and is removed in this change.
// cmd/server/readonly_invariant_test.go now guards against reintroduction.
//
// Callers pass the resolved pubkeys already computed for
// observations.resolved_path (#1547/#1560) — only unambiguously resolved
// hops reach this function, so a 1-byte prefix collision cannot keep a
// silent node alive. resolvedPubkeys already dedups, so no second pass here.
//
// Never inserts: the UPDATE matches an existing row or does nothing.
// Never rewinds: the last_seen guard makes out-of-order ingest a no-op.
// UpsertNode's ON CONFLICT clause is monotonic in the same direction
// (MAX(MIN(last_seen, ingestNow), rxTime) reduces to MAX(last_seen, rxTime)
// for any non-future stored value), so an ADVERT cannot undo a touch.
func (s *Store) touchRelayNodesLocked(pubkeys []string, rxTime string) {
	if len(pubkeys) == 0 || s.stmtTouchNodeLastSeen == nil {
		return
	}
	// Reject unparsable timestamps rather than writing them into the node
	// directory. Callers hand us the observation rxTime, which comes off
	// the wire and is not guaranteed well-formed.
	ts, err := time.Parse(time.RFC3339, rxTime)
	if err != nil {
		return
	}
	// Same layout UpsertNode uses for last_seen, so the SQL comparisons
	// above stay lexicographic-equals-chronological.
	stamp := ts.UTC().Format(time.RFC3339)

	if s.relayTouched == nil {
		s.relayTouched = make(map[string]time.Time)
	}
	if len(s.relayTouched) >= relayTouchedMaxEntries {
		s.compactRelayTouched(ts)
	}
	for _, pk := range pubkeys {
		if pk == "" {
			continue
		}
		if last, ok := s.relayTouched[pk]; ok && ts.Sub(last) < relayTouchDebounce {
			continue
		}
		res, err := s.stmtTouchNodeLastSeen.Exec(stamp, pk, stamp)
		if err != nil {
			s.Stats.WriteErrors.Add(1)
			continue
		}
		// Debounce on attempt, not on row match: an unknown pubkey would
		// otherwise be retried on every observation it appears in.
		//
		// Side effect: a pubkey touched while absent from nodes, then
		// inserted by an ADVERT moments later, is skipped for the rest of
		// the window. Harmless — the ADVERT wrote last_seen itself, and it
		// is newer than anything this window would have written.
		s.relayTouched[pk] = ts
		if n, _ := res.RowsAffected(); n > 0 {
			s.Stats.RelayTouches.Add(n)
		}
	}
}

// compactRelayTouched drops debounce entries older than two windows.
// Caller must hold writerMu.
func (s *Store) compactRelayTouched(now time.Time) {
	cutoff := now.Add(-2 * relayTouchDebounce)
	for pk, t := range s.relayTouched {
		if t.Before(cutoff) {
			delete(s.relayTouched, pk)
		}
	}
}

// MoveStaleNodes moves nodes not seen in nodeDays to the inactive_nodes table.
// Returns the number of nodes moved.
func (s *Store) MoveStaleNodes(nodeDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -nodeDays).Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT OR REPLACE INTO inactive_nodes SELECT * FROM nodes WHERE last_seen < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("insert inactive: %w", err)
	}
	result, err := tx.Exec(`DELETE FROM nodes WHERE last_seen < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete stale: %w", err)
	}
	moved, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	if moved > 0 {
		log.Printf("Moved %d node(s) to inactive_nodes (not seen in %d days)", moved, nodeDays)
	}
	return moved, nil
}

// RemoveStaleObservers marks observers that have not actively sent data in observerDays
// as inactive (soft-delete). This preserves JOIN integrity for observations.observer_idx
// and observer_metrics.observer_id — historical data still references the correct observer.
// An observer must actively send data to stay listed — being seen by another node does not count.
// observerDays <= -1 means never remove (keep forever).
func (s *Store) RemoveStaleObservers(observerDays int) (int64, error) {
	if observerDays <= -1 {
		return 0, nil // keep forever
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -observerDays).Format(time.RFC3339)
	// Tagged for /api/perf writer-lock visibility (#1340).
	result, err := s.instrumentedExec("prune_observers", `UPDATE observers SET inactive = 1 WHERE last_seen < ? AND (inactive IS NULL OR inactive = 0)`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("mark stale observers inactive: %w", err)
	}
	removed, _ := result.RowsAffected()
	if removed > 0 {
		// Clean up orphaned metrics for now-inactive observers
		_, _ = s.instrumentedExec("prune_observers", `DELETE FROM observer_metrics WHERE observer_id IN (SELECT id FROM observers WHERE inactive = 1)`)
		log.Printf("Marked %d observer(s) as inactive (not seen in %d days)", removed, observerDays)
	}
	return removed, nil
}

// PurgeStaleObservers hard-deletes rows RemoveStaleObservers already soft-deleted,
// once they are older than purgeDays and nothing references them any more. It is
// the second stage of observer retention: the soft-delete hides the observer,
// this reclaims the row after its packets have aged out via packetDays.
//
// observations.observer_idx is a bare rowid with no foreign key, so deleting a
// still-referenced observer silently orphans history — packets_v stops resolving
// the observer and the packets are mis-attributed. The three NOT EXISTS guards
// are what make the delete safe; they are correctness, not defensive padding.
// Each is an index seek per candidate row (observers is O(100)), so the whole
// statement stays cheap enough for the daily retention tick.
//
// purgeDays <= 0 disables the purge (the default).
func (s *Store) PurgeStaleObservers(purgeDays int) (int64, error) {
	if purgeDays <= 0 {
		return 0, nil // disabled
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -purgeDays).Format(time.RFC3339)
	// Tagged for /api/perf writer-lock visibility (#1340).
	result, err := s.instrumentedExec("purge_observers", `
		DELETE FROM observers
		WHERE inactive = 1
		  AND last_seen < ?
		  AND NOT EXISTS (SELECT 1 FROM observations o WHERE o.observer_idx = observers.rowid)
		  AND NOT EXISTS (SELECT 1 FROM observer_metrics m WHERE m.observer_id = observers.id)
		  AND NOT EXISTS (SELECT 1 FROM dropped_packets d WHERE d.observer_id = observers.id)`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge stale observers: %w", err)
	}
	purged, _ := result.RowsAffected()
	if purged > 0 {
		log.Printf("Purged %d inactive observer(s) with no remaining data (not seen in %d days)", purged, purgeDays)
	}
	return purged, nil
}

// DroppedPacket holds data for a packet rejected during ingest.
type DroppedPacket struct {
	Hash         string
	RawHex       string
	Reason       string
	ObserverID   string
	ObserverName string
	NodePubKey   string
	NodeName     string
}

// InsertDroppedPacket records a rejected packet in the dropped_packets table.
func (s *Store) InsertDroppedPacket(dp *DroppedPacket) error {
	_, err := s.db.Exec(
		`INSERT INTO dropped_packets (hash, raw_hex, reason, observer_id, observer_name, node_pubkey, node_name) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		dp.Hash, dp.RawHex, dp.Reason, dp.ObserverID, dp.ObserverName, dp.NodePubKey, dp.NodeName,
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		return fmt.Errorf("insert dropped packet: %w", err)
	}
	s.Stats.SignatureDrops.Add(1)
	return nil
}

// PruneDroppedPackets removes dropped_packets older than retentionDays.
func (s *Store) PruneDroppedPackets(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	result, err := s.db.Exec(`DELETE FROM dropped_packets WHERE dropped_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune dropped packets: %w", err)
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		log.Printf("Pruned %d dropped packet(s) older than %d days", n, retentionDays)
	}
	return n, nil
}

// PacketData holds the data needed to insert a packet into the DB.
type PacketData struct {
	RawHex            string
	Timestamp         string
	ObserverID        string
	ObserverName      string
	SNR               *float64
	RSSI              *float64
	Score             *float64
	Direction         *string
	Hash              string
	RouteType         int
	PayloadType       int
	PayloadVersion    int
	PathJSON          string
	DecodedJSON       string
	ChannelHash       string // grouping key for channel queries (#762)
	ScopeName         string // matched region name, or "" for unknown-scoped
	IsTransportScoped bool   // true when route_type IN (0,3) AND Code1 ≠ "0000"
	Code1             string // transport code 1 (scope), "" when the route carries none
	Code2             string // transport code 2 (return-region hint), "" when absent
	Region            string // observer region: payload > topic > source config (#788)
	Foreign           bool   // true when ADVERT GPS lies outside configured geofilter (#730)
	FromPubkey        string // pubkey of the originating node, for exact-match attribution (#1143)
}

// nilIfEmpty returns nil for empty strings (for nullable DB columns).
func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// boolToInt converts a bool to SQLite's 0/1 INTEGER representation.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// scopeNameForDB encodes PacketData scope semantics for DB storage:
// non-transport-scoped → nil (SQL NULL); transport-scoped → pointer to ScopeName
// (may be "" for unknown region, "#name" for matched region).
func scopeNameForDB(data *PacketData) *string {
	if !data.IsTransportScoped {
		return nil
	}
	s := data.ScopeName
	return &s
}

// UpdateNodeDefaultScope records the most-recently observed region scope for a
// node. Skips the UPDATE when the stored value already matches to avoid
// redundant writes on the hot MQTT ingest path. Updates both nodes and
// inactive_nodes to stay consistent.
//
// Defense-in-depth (#1534): an empty scope is treated as a no-op. The call
// site at handleMessage is the primary guard (shouldUpdateDefaultScope),
// but this layer refuses the invalid write so a future caller cannot
// reintroduce the bug by passing "" directly.
func (s *Store) UpdateNodeDefaultScope(pubkey, scope string) error {
	if scope == "" {
		return nil
	}
	// Short-circuit: skip if already stored.
	var cur sql.NullString
	row := s.db.QueryRow(`SELECT default_scope FROM nodes WHERE public_key = ?`, pubkey)
	if row.Scan(&cur) == nil && cur.Valid && cur.String == scope {
		return nil
	}
	if _, err := s.db.Exec(`UPDATE nodes SET default_scope = ? WHERE public_key = ?`, scope, pubkey); err != nil {
		return err
	}
	// Mirror to inactive_nodes (node may be there if recently moved by retention).
	_, err := s.db.Exec(`UPDATE inactive_nodes SET default_scope = ? WHERE public_key = ?`, scope, pubkey)
	return err
}

// normalizeReportTS parses an observer report timestamp and returns it in
// canonical UTC RFC3339 form ("2006-01-02T15:04:05Z"). It accepts both the
// firmware's fractional/offset form (e.g. "2026-07-26T09:43:48.000000+00:00")
// and plain "Z"/offset variants. An empty or unparseable input returns "" so
// the caller writes an empty configured_scope_at and skips the ordering guard;
// this keeps every stored timestamp either canonical or empty, never a mix of
// offset/precision formats that would break lexicographic last-write-wins.
func normalizeReportTS(raw string) string {
	if raw == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// UpdateNodeConfiguredScope records the region scopes a node has CONFIGURED,
// as concrete evidence from an observer /neighbors report (#1865). Unlike
// UpdateNodeDefaultScope (inferred, overwritten on every observation), this is
// only called for the observer's own `self` scopes and for neighbors whose OTA
// scope query returned status="responded" — so the caller, not this method,
// gates on status. An empty scope IS a valid "responded, no scopes configured"
// statement and is stored; a timeout must simply not reach this method.
//
// reportedAt is the report envelope timestamp (ISO-8601). It is stored in
// configured_scope_at and used for last-write-wins: an out-of-order older
// report must not clobber a newer confirmed value. A blank reportedAt skips
// the ordering guard (always writes).
func (s *Store) UpdateNodeConfiguredScope(pubkey, scope, reportedAt string) error {
	if pubkey == "" {
		return nil
	}
	// Normalize to canonical UTC RFC3339 so the last-write-wins comparison is
	// chronological, not lexicographic (see normalizeReportTS). Stored values
	// are therefore always canonical or empty.
	reportedAt = normalizeReportTS(reportedAt)
	// Last-write-wins: skip if the stored confirmation is newer-or-equal.
	if reportedAt != "" {
		var curAt sql.NullString
		row := s.db.QueryRow(`SELECT configured_scope_at FROM nodes WHERE public_key = ?`, pubkey)
		if row.Scan(&curAt) == nil && curAt.Valid && curAt.String != "" && curAt.String >= reportedAt {
			return nil
		}
	}
	if _, err := s.db.Exec(
		`UPDATE nodes SET configured_scope = ?, configured_scope_at = ? WHERE public_key = ?`,
		scope, reportedAt, pubkey); err != nil {
		return err
	}
	// Mirror to inactive_nodes (node may be there if recently moved by retention).
	_, err := s.db.Exec(
		`UPDATE inactive_nodes SET configured_scope = ?, configured_scope_at = ? WHERE public_key = ?`,
		scope, reportedAt, pubkey)
	return err
}

// RecordNaiveSkew is called when resolveRxTime() clamps a packet's envelope
// timestamp because the observer is emitting a zone-less local-time string
// off from UTC by more than 15 min (issue #1478). Stamps the observer's
// clock_skew_seconds / clock_skew_count_24h / clock_last_naive_at so the
// server can surface a ⚠️ chip + banner in the UI.
//
// The count is reset to 1 (not incremented) if no event has been recorded in
// the past 24h, otherwise incremented. deltaSec is signed: negative = observer
// clock is behind UTC, positive = ahead.
func (s *Store) RecordNaiveSkew(observerID string, deltaSec int64, now time.Time) error {
	if observerID == "" {
		return nil
	}
	nowStr := now.UTC().Format(time.RFC3339)
	cutoff := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	// One INSERT-or-UPDATE round trip. ON CONFLICT path resets the rolling
	// counter when the previous event is older than the 24h window, otherwise
	// increments it.
	_, err := s.db.Exec(`
		INSERT INTO observers (id, clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(id) DO UPDATE SET
			clock_skew_seconds = excluded.clock_skew_seconds,
			clock_last_naive_at = excluded.clock_last_naive_at,
			clock_skew_count_24h = CASE
				WHEN clock_last_naive_at IS NULL OR clock_last_naive_at < ?
					THEN 1
				ELSE COALESCE(clock_skew_count_24h, 0) + 1
			END
	`, observerID, deltaSec, nowStr, cutoff)
	return err
}

// MQTTPacketMessage is the JSON payload from an MQTT raw packet message.
type MQTTPacketMessage struct {
	Raw       string   `json:"raw"`
	SNR       *float64 `json:"SNR"`
	RSSI      *float64 `json:"RSSI"`
	Score     *float64 `json:"score"`
	Direction *string  `json:"direction"`
	Origin    string   `json:"origin"`
	Region    string   `json:"region,omitempty"`    // optional region override (#788)
	Timestamp string   `json:"timestamp,omitempty"` // observer receive time, resolved by handler
}

// BuildPacketData constructs a PacketData from a decoded packet and MQTT message.
// path_json is derived directly from raw_hex header bytes (not decoded.Path.Hops)
// to guarantee the stored path always matches the raw bytes. This matters for
// TRACE packets where decoded.Path.Hops is overwritten with payload hops (#886).
//
// Timestamp is server ingest time (time.Now()), NOT msg.Timestamp (#1370):
// PR #1233 (commit 498fbc03) routed the envelope timestamp into
// PacketData.Timestamp on the premise that uploader-stamped envelope time
// was trustworthy. Issue #1370 disproved that premise — observers with
// broken client clocks (staging Voodoo3 tx 304114: 4/5 obs stamped 18:42
// while genuine receive was 01:42) poisoned transmissions.first_seen /
// observations.timestamp and dragged the /api/channels lastActivity 7h
// into the past. Packet ordering is owned by the server clock; client
// clocks are untrusted. msg.Timestamp still flows into observer.last_seen
// via UpsertObserverAt — that's #1233's MAX/MIN guarded path and is fine.
func BuildPacketData(msg *MQTTPacketMessage, decoded *DecodedPacket, observerID, region string, regionKeys map[string][]byte) *PacketData {
	pathJSON := "[]"
	// For TRACE packets, path_json must be the payload-decoded route hops
	// (decoded.Path.Hops), NOT the raw_hex header bytes which are SNR values.
	// For all other packet types, derive path from raw_hex (#886).
	if !packetpath.PathBytesAreHops(byte(decoded.Header.PayloadType)) {
		if len(decoded.Path.Hops) > 0 {
			b, _ := json.Marshal(decoded.Path.Hops)
			pathJSON = string(b)
		}
	} else if hops, err := packetpath.DecodePathFromRawHex(msg.Raw); err == nil && len(hops) > 0 {
		b, _ := json.Marshal(hops)
		pathJSON = string(b)
	}

	pd := &PacketData{
		RawHex:         msg.Raw,
		Timestamp:      time.Now().UTC().Format(time.RFC3339), // #1370 (counters #1233)
		ObserverID:     observerID,
		ObserverName:   msg.Origin,
		SNR:            msg.SNR,
		RSSI:           msg.RSSI,
		Score:          msg.Score,
		Direction:      msg.Direction,
		Hash:           ComputeContentHash(msg.Raw),
		RouteType:      decoded.Header.RouteType,
		PayloadType:    decoded.Header.PayloadType,
		PayloadVersion: decoded.Header.PayloadVersion,
		PathJSON:       pathJSON,
		DecodedJSON:    PayloadJSON(&decoded.Payload),
	}

	// Region priority: payload field > topic-derived parameter (#788)
	if msg.Region != "" {
		pd.Region = msg.Region
	} else {
		pd.Region = region
	}

	// Populate channel_hash for fast channel queries (#762)
	if decoded.Header.PayloadType == PayloadGRP_TXT {
		if decoded.Payload.Type == "CHAN" && decoded.Payload.Channel != "" {
			pd.ChannelHash = decoded.Payload.Channel
		} else if decoded.Payload.Type == "GRP_TXT" && decoded.Payload.ChannelHashHex != "" {
			pd.ChannelHash = "enc_" + decoded.Payload.ChannelHashHex
		}
	}

	if decoded.TransportCodes != nil {
		pd.Code1 = decoded.TransportCodes.Code1
		pd.Code2 = decoded.TransportCodes.Code2
		if decoded.TransportCodes.Code1 != "0000" {
			pd.IsTransportScoped = true
			pd.ScopeName = matchScope(regionKeys, byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
		}
	}

	// Populate from_pubkey at write time (#1143). ADVERTs carry the
	// originating node's pubkey directly; other packet types stay NULL
	// (downstream attribution queries handle NULL gracefully).
	if decoded.Header.PayloadType == PayloadADVERT && decoded.Payload.PubKey != "" {
		pd.FromPubkey = decoded.Payload.PubKey
	}

	return pd
}

// ─── Writer-lock instrumentation (issue #1340) ────────────────────────────
//
// Make SQLite writer-lock starvation visible to operators. Per-component
// wait_ms / hold_ms / contention_total histograms, surfaced via
// /api/perf/write-sources under the "writer_perf" key. Component tags:
// neighbor_builder, mqtt_handler, prune_packets, prune_observers,
// prune_metrics, mbcap_persist (deferred — see PR body), vacuum.
//
// The single writer connection (SetMaxOpenConns(1)) means writes serialise
// inside the driver and the wait is invisible to Go. writerMu measures the
// wait Go can see (everyone queueing behind the current holder) by gating
// every wrapped call site through the same package-level mutex.

// WriterStatsSnapshot is a per-component wait/hold latency snapshot
// surfaced via /api/perf to make SQLite writer-lock starvation visible
// to operators (issue #1340). Times are in milliseconds.
type WriterStatsSnapshot struct {
	Count           int64   `json:"count"`
	ContentionTotal int64   `json:"contention_total"`
	WaitMsP50       float64 `json:"wait_ms_p50"`
	WaitMsP95       float64 `json:"wait_ms_p95"`
	WaitMsP99       float64 `json:"wait_ms_p99"`
	WaitMsMax       float64 `json:"wait_ms_max"`
	HoldMsP50       float64 `json:"hold_ms_p50"`
	HoldMsP95       float64 `json:"hold_ms_p95"`
	HoldMsP99       float64 `json:"hold_ms_p99"`
	HoldMsMax       float64 `json:"hold_ms_max"`
}

const (
	// writerSampleWindow bounds the per-component rolling window so a
	// long-running ingestor doesn't grow this unbounded.
	writerSampleWindow = 1024
	// contentionThresholdMs: wait_ms above this counts as a "contended"
	// write (per #1340 spec).
	contentionThresholdMs = 100.0
	defaultSlowWriterMs   = 500.0
)

// slowWriterThresholdMsAtomic — hold_ms threshold above which writes
// emit a [db-slow-writer] log line. Read on the hot path; written once
// at startup by SetSlowWriterThresholdMs.
var slowWriterThresholdMsAtomic atomic.Uint64

// SetSlowWriterThresholdMs sets the [db-slow-writer] log threshold.
// ms<=0 restores the 500ms default. Operators can also set
// CORESCOPE_DB_SLOW_WRITER_MS at process start — see initSlowWriterFromEnv.
func SetSlowWriterThresholdMs(ms float64) {
	if ms <= 0 {
		ms = defaultSlowWriterMs
	}
	slowWriterThresholdMsAtomic.Store(uint64(ms))
}

func getSlowWriterThresholdMs() float64 {
	v := slowWriterThresholdMsAtomic.Load()
	if v == 0 {
		return defaultSlowWriterMs
	}
	return float64(v)
}

// initSlowWriterFromEnv is called once from package init so operators can
// override the threshold via CORESCOPE_DB_SLOW_WRITER_MS without a
// Go-side Config change.
func initSlowWriterFromEnv() {
	v := os.Getenv("CORESCOPE_DB_SLOW_WRITER_MS")
	if v == "" {
		return
	}
	var ms float64
	if _, err := fmt.Sscanf(v, "%f", &ms); err == nil && ms > 0 {
		SetSlowWriterThresholdMs(ms)
	}
}

func init() { initSlowWriterFromEnv() }

type writerComponentStats struct {
	mu              sync.Mutex
	count           int64
	contentionTotal int64
	waitMs          []float64
	holdMs          []float64
	waitMax         float64
	holdMax         float64
}

func (c *writerComponentStats) record(waitMs, holdMs float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	if waitMs > contentionThresholdMs {
		c.contentionTotal++
	}
	if waitMs > c.waitMax {
		c.waitMax = waitMs
	}
	if holdMs > c.holdMax {
		c.holdMax = holdMs
	}
	c.waitMs = appendBoundedFloat(c.waitMs, waitMs, writerSampleWindow)
	c.holdMs = appendBoundedFloat(c.holdMs, holdMs, writerSampleWindow)
}

func appendBoundedFloat(s []float64, v float64, max int) []float64 {
	if len(s) < max {
		return append(s, v)
	}
	copy(s, s[1:])
	s[len(s)-1] = v
	return s
}

func (c *writerComponentStats) snapshot() WriterStatsSnapshot {
	c.mu.Lock()
	wait := append([]float64(nil), c.waitMs...)
	hold := append([]float64(nil), c.holdMs...)
	snap := WriterStatsSnapshot{
		Count:           c.count,
		ContentionTotal: c.contentionTotal,
		WaitMsMax:       c.waitMax,
		HoldMsMax:       c.holdMax,
	}
	c.mu.Unlock()
	sort.Float64s(wait)
	sort.Float64s(hold)
	snap.WaitMsP50 = nearestRankPercentile(wait, 0.50)
	snap.WaitMsP95 = nearestRankPercentile(wait, 0.95)
	snap.WaitMsP99 = nearestRankPercentile(wait, 0.99)
	snap.HoldMsP50 = nearestRankPercentile(hold, 0.50)
	snap.HoldMsP95 = nearestRankPercentile(hold, 0.95)
	snap.HoldMsP99 = nearestRankPercentile(hold, 0.99)
	return snap
}

func nearestRankPercentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	idx := int(p*float64(n-1) + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

type writerStatsAggregator struct {
	mu         sync.Mutex
	components map[string]*writerComponentStats
}

var writerStatsAgg = &writerStatsAggregator{
	components: make(map[string]*writerComponentStats),
}

func (a *writerStatsAggregator) get(component string) *writerComponentStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.components[component]
	if !ok {
		c = &writerComponentStats{}
		a.components[component] = c
	}
	return c
}

// reset clears all per-component samples. Test-only: lets a single
// scenario assert against a clean aggregator without prior-test noise
// in the same package run (TestWriterStarvationVisibleInPerf would
// otherwise mix this run's 5 starved samples with thousands of fast
// InsertTransmission samples from earlier tests and the p99 would
// collapse below the 50s threshold).
func (a *writerStatsAggregator) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.components = make(map[string]*writerComponentStats)
}

// ResetWriterStatsForTest wipes the per-component writer stats
// aggregator. Test-only; not safe to call from production code paths.
func ResetWriterStatsForTest() { writerStatsAgg.reset() }

func (a *writerStatsAggregator) snapshot() map[string]WriterStatsSnapshot {
	a.mu.Lock()
	keys := make([]string, 0, len(a.components))
	stats := make([]*writerComponentStats, 0, len(a.components))
	for k, v := range a.components {
		keys = append(keys, k)
		stats = append(stats, v)
	}
	a.mu.Unlock()
	out := make(map[string]WriterStatsSnapshot, len(keys))
	for i, k := range keys {
		out[k] = stats[i].snapshot()
	}
	return out
}

// WriterStatsSnapshot returns a per-component wait/hold/contention
// snapshot for exposure on /api/perf/write-sources (issue #1340).
func (s *Store) WriterStatsSnapshot() map[string]WriterStatsSnapshot {
	return writerStatsAgg.snapshot()
}

// recordWriterTiming aggregates a single sample under component and
// emits [db-slow-writer] if hold_ms > configured threshold (default
// 500ms). queryForLog is truncated to 200 chars.
func recordWriterTiming(component string, wait, hold time.Duration, queryForLog string) {
	waitMs := float64(wait.Nanoseconds()) / 1e6
	holdMs := float64(hold.Nanoseconds()) / 1e6
	writerStatsAgg.get(component).record(waitMs, holdMs)
	if holdMs > getSlowWriterThresholdMs() {
		q := queryForLog
		if len(q) > 200 {
			q = q[:200]
		}
		log.Printf("[db-slow-writer] component=%s duration=%.1fms query=%s", component, holdMs, q)
	}
}

// writerMu serialises every wrapped writer call so the wait the next
// caller sees is the wait the perf snapshot can attribute. The
// SQLite driver also enforces serial writes (SetMaxOpenConns(1)),
// but the wait inside the driver is invisible to Go — writerMu makes
// it Go-visible.
var writerMu sync.Mutex

// WriterExec wraps s.db.Exec with per-component wait/hold/contention
// instrumentation (issue #1340).
func (s *Store) WriterExec(component, query string, args ...interface{}) (sql.Result, error) {
	waitStart := time.Now()
	writerMu.Lock()
	wait := time.Since(waitStart)
	holdStart := time.Now()
	res, err := s.db.Exec(query, args...)
	hold := time.Since(holdStart)
	writerMu.Unlock()
	recordWriterTiming(component, wait, hold, query)
	return res, err
}

// WriterTx wraps Begin → fn → Commit under component tagging.
// hold_ms covers the whole tx so a slow body counts against its owner.
func (s *Store) WriterTx(component string, fn func(*sql.Tx) error) error {
	waitStart := time.Now()
	writerMu.Lock()
	wait := time.Since(waitStart)
	holdStart := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		hold := time.Since(holdStart)
		writerMu.Unlock()
		recordWriterTiming(component, wait, hold, "BEGIN")
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		hold := time.Since(holdStart)
		writerMu.Unlock()
		recordWriterTiming(component, wait, hold, "tx-body")
		return err
	}
	err = tx.Commit()
	hold := time.Since(holdStart)
	writerMu.Unlock()
	recordWriterTiming(component, wait, hold, "COMMIT")
	return err
}

// Wrap helpers below tag existing call sites with the canonical
// component names so the call sites read naturally. These keep the
// instrumentation out of the hot-path business logic.

// instrumentedExec is the package-internal pass-through used by call
// sites already inside db.go (PruneOldMetrics, RemoveStaleObservers,
// vacuum). Equivalent to WriterExec, kept short for readability.
func (s *Store) instrumentedExec(component, query string, args ...interface{}) (sql.Result, error) {
	return s.WriterExec(component, query, args...)
}

// epochSecondsForLastSeen parses an RFC3339 timestamp to a unix-second
// value for the transmissions.last_seen denormalized column (#1690).
// Falls back to the current time on parse failure so the column is
// never seeded with 0 for a brand-new row.
func epochSecondsForLastSeen(rfc3339 string) int64 {
	if t, err := time.Parse(time.RFC3339, rfc3339); err == nil {
		return t.Unix()
	}
	return time.Now().UTC().Unix()
}
