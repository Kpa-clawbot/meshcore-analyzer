package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

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
}

// Store wraps the SQLite database for packet ingestion.
type Store struct {
	db    *sql.DB
	Stats DBStats

	stmtGetTxByHash          *sql.Stmt
	stmtInsertTransmission   *sql.Stmt
	stmtUpdateTxFirstSeen    *sql.Stmt
	stmtInsertObservation    *sql.Stmt
	stmtUpsertNode           *sql.Stmt
	stmtIncrementAdvertCount *sql.Stmt
	stmtUpsertObserver       *sql.Stmt
	stmtGetObserverRowid       *sql.Stmt
	stmtUpdateObserverLastSeen *sql.Stmt
	stmtUpdateNodeTelemetry    *sql.Stmt
	stmtUpsertMetrics          *sql.Stmt

	sampleIntervalSec int
}

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

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
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

	s := &Store{db: db, sampleIntervalSec: sampleIntervalSec}
	if err := s.prepareStatements(); err != nil {
		return nil, fmt.Errorf("preparing statements: %w", err)
	}

	return s, nil
}

func applySchema(db *sql.DB) error {
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
			temperature_c REAL
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
			inactive INTEGER DEFAULT 0
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
			temperature_c REAL
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
			created_at TEXT DEFAULT (datetime('now'))
		);

		CREATE INDEX IF NOT EXISTS idx_transmissions_hash ON transmissions(hash);
		CREATE INDEX IF NOT EXISTS idx_transmissions_first_seen ON transmissions(first_seen);
		CREATE INDEX IF NOT EXISTS idx_transmissions_payload_type ON transmissions(payload_type);
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
		db.Exec(`
			UPDATE nodes SET advert_count = (
				SELECT COUNT(*) FROM transmissions t
				WHERE t.payload_type = 4
				  AND t.decoded_json LIKE '%' || nodes.public_key || '%'
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

	// Migration: add raw_hex column to observations (#881)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'observations_raw_hex_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding raw_hex column to observations...")
		db.Exec(`ALTER TABLE observations ADD COLUMN raw_hex TEXT`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('observations_raw_hex_v1')`)
		log.Println("[migration] observations.raw_hex column added")
	}

	// Migration: add scope_name column to transmissions (#899)
	row = db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'scope_name_v1'")
	if row.Scan(&migDone) != nil {
		log.Println("[migration] Adding scope_name column to transmissions...")
		db.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_tx_scope_name ON transmissions(scope_name) WHERE scope_name IS NOT NULL`)
		db.Exec(`INSERT INTO _migrations (name) VALUES ('scope_name_v1')`)
		log.Println("[migration] scope_name column added")
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
		INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, channel_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}

	s.stmtUpdateTxFirstSeen, err = s.db.Prepare("UPDATE transmissions SET first_seen = ? WHERE id = ?")
	if err != nil {
		return err
	}

	s.stmtInsertObservation, err = s.db.Prepare(`
		INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp, raw_hex)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(transmission_id, observer_idx, COALESCE(path_json, '')) DO UPDATE SET
			snr     = COALESCE(excluded.snr,     snr),
			rssi    = COALESCE(excluded.rssi,    rssi),
			score   = COALESCE(excluded.score,   score),
			raw_hex = COALESCE(excluded.raw_hex, raw_hex)
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
			last_seen = ?
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
		INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = COALESCE(?, name),
			iata = COALESCE(?, iata),
			last_seen = ?,
			packet_count = packet_count + 1,
			model = COALESCE(?, model),
			firmware = COALESCE(?, firmware),
			client_version = COALESCE(?, client_version),
			radio = COALESCE(?, radio),
			battery_mv = COALESCE(?, battery_mv),
			uptime_secs = COALESCE(?, uptime_secs),
			noise_floor = COALESCE(?, noise_floor)
	`)
	if err != nil {
		return err
	}

	s.stmtGetObserverRowid, err = s.db.Prepare("SELECT rowid FROM observers WHERE id = ?")
	if err != nil {
		return err
	}

	s.stmtUpdateObserverLastSeen, err = s.db.Prepare("UPDATE observers SET last_seen = ? WHERE rowid = ?")
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

	now := data.Timestamp
	if now == "" {
		now = time.Now().UTC().Format(time.RFC3339)
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
		if now < existingFirstSeen {
			_, _ = s.stmtUpdateTxFirstSeen.Exec(now, txID)
		}
	} else {
		// New transmission
		isNew = true
		result, err := s.stmtInsertTransmission.Exec(
			data.RawHex, hash, now,
			data.RouteType, data.PayloadType, data.PayloadVersion,
			data.DecodedJSON, nilIfEmpty(data.ChannelHash),
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
			// Update observer last_seen on every packet to prevent
			// low-traffic observers from appearing offline (#463)
			_, _ = s.stmtUpdateObserverLastSeen.Exec(now, rowid)
		}
	}

	// Insert observation
	epochTs := time.Now().Unix()
	if t, err := time.Parse(time.RFC3339, now); err == nil {
		epochTs = t.Unix()
	}

	_, err = s.stmtInsertObservation.Exec(
		txID, observerIdx, data.Direction,
		data.SNR, data.RSSI, data.Score,
		data.PathJSON, epochTs, nilIfEmpty(data.RawHex),
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
		log.Printf("[db] observation insert (non-fatal): %v", err)
	} else {
		s.Stats.ObservationsInserted.Add(1)
	}

	return isNew, nil
}

// UpsertNode inserts or updates a node.
func (s *Store) UpsertNode(pubKey, name, role string, lat, lon *float64, lastSeen string) error {
	now := lastSeen
	if now == "" {
		now = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := s.stmtUpsertNode.Exec(
		pubKey, name, role, lat, lon, now, now,
		name, role, lat, lon, now,
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
}

// UpsertObserver inserts or updates an observer with optional hardware metadata.
func (s *Store) UpsertObserver(id, name, iata string, meta *ObserverMeta) error {
	now := time.Now().UTC().Format(time.RFC3339)
	normalizedIATA := strings.TrimSpace(strings.ToUpper(iata))

	var model, firmware, clientVersion, radio interface{}
	var batteryMv, uptimeSecs, noiseFloor interface{}
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
	}

	_, err := s.stmtUpsertObserver.Exec(
		id, name, normalizedIATA, now, now, model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor,
		name, normalizedIATA, now, model, firmware, clientVersion, radio, batteryMv, uptimeSecs, noiseFloor,
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

// Close checkpoints the WAL and closes the database.
func (s *Store) Close() error {
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
	result, err := s.db.Exec(`DELETE FROM observer_metrics WHERE timestamp < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune metrics: %w", err)
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		log.Printf("[metrics] Pruned %d rows older than %d days", n, retentionDays)
	}
	return n, nil
}

// Checkpoint forces a WAL checkpoint to release the WAL lock file,
// preventing lock contention with a new process starting up.
func (s *Store) Checkpoint() {
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("[db] WAL checkpoint error: %v", err)
	} else {
		log.Println("[db] WAL checkpoint complete")
	}
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
	result, err := s.db.Exec(`UPDATE observers SET inactive = 1 WHERE last_seen < ? AND (inactive IS NULL OR inactive = 0)`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("mark stale observers inactive: %w", err)
	}
	removed, _ := result.RowsAffected()
	if removed > 0 {
		// Clean up orphaned metrics for now-inactive observers
		s.db.Exec(`DELETE FROM observer_metrics WHERE observer_id IN (SELECT id FROM observers WHERE inactive = 1)`)
		log.Printf("Marked %d observer(s) as inactive (not seen in %d days)", removed, observerDays)
	}
	return removed, nil
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

// BackfillScopeNames sets scope_name on existing transport-route transmissions that
// lack it, using the provided region HMAC keys. Safe to re-run — only processes
// rows where scope_name IS NULL and route_type IN (0, 3).
func (s *Store) BackfillScopeNames(regionKeys map[string][]byte) {
	if len(regionKeys) == 0 {
		return
	}
	rows, err := s.db.Query(`
		SELECT id, raw_hex FROM transmissions
		WHERE route_type IN (0, 3) AND scope_name IS NULL AND raw_hex IS NOT NULL
	`)
	if err != nil {
		log.Printf("[backfill] scope_name query: %v", err)
		return
	}
	defer rows.Close()

	type row struct {
		id     int64
		rawHex string
	}
	var pending []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.rawHex) == nil && r.rawHex != "" {
			pending = append(pending, r)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[backfill] scope_name iteration error: %v", err)
		return
	}

	updated := 0
	for _, r := range pending {
		decoded, err := DecodePacket(r.rawHex, nil, false)
		if err != nil || decoded.TransportCodes == nil {
			continue
		}
		if decoded.TransportCodes.Code1 == "0000" {
			continue
		}
		scopeName := matchScope(regionKeys, byte(decoded.Header.PayloadType), decoded.PayloadRaw, decoded.TransportCodes.Code1)
		s.db.Exec(`UPDATE transmissions SET scope_name = ? WHERE id = ?`, scopeName, r.id)
		updated++
	}
	if updated > 0 {
		log.Printf("[backfill] scope_name set for %d/%d transport-route rows", updated, len(pending))
	}
}

// PacketData holds the data needed to insert a packet into the DB.
type PacketData struct {
	RawHex         string
	Timestamp      string
	ObserverID     string
	ObserverName   string
	SNR            *float64
	RSSI           *float64
	Score          *float64
	Direction      *string
	Hash           string
	RouteType      int
	PayloadType    int
	PayloadVersion int
	PathJSON       string
	DecodedJSON    string
	ChannelHash    string // grouping key for channel queries (#762)
}

// nilIfEmpty returns nil for empty strings (for nullable DB columns).
func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// MQTTPacketMessage is the JSON payload from an MQTT raw packet message.
type MQTTPacketMessage struct {
	Raw       string   `json:"raw"`
	SNR       *float64 `json:"SNR"`
	RSSI      *float64 `json:"RSSI"`
	Score     *float64 `json:"score"`
	Direction *string  `json:"direction"`
	Origin    string   `json:"origin"`
}

// BuildPacketData constructs a PacketData from a decoded packet and MQTT message.
// path_json is derived directly from raw_hex header bytes (not decoded.Path.Hops)
// to guarantee the stored path always matches the raw bytes. This matters for
// TRACE packets where decoded.Path.Hops is overwritten with payload hops (#886).
func BuildPacketData(msg *MQTTPacketMessage, decoded *DecodedPacket, observerID, region string) *PacketData {
	now := time.Now().UTC().Format(time.RFC3339)
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
		Timestamp:      now,
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

	// Populate channel_hash for fast channel queries (#762)
	if decoded.Header.PayloadType == PayloadGRP_TXT {
		if decoded.Payload.Type == "CHAN" && decoded.Payload.Channel != "" {
			pd.ChannelHash = decoded.Payload.Channel
		} else if decoded.Payload.Type == "GRP_TXT" && decoded.Payload.ChannelHashHex != "" {
			pd.ChannelHash = "enc_" + decoded.Payload.ChannelHashHex
		}
	}

	return pd
}
