package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// DBStats tracks operational metrics for the ingestor database.
type DBStats struct {
	TransmissionsInserted atomic.Int64
	ObservationsInserted  atomic.Int64
	DuplicateTransmissions atomic.Int64
	NodeUpserts           atomic.Int64
	ObserverUpserts       atomic.Int64
	WriteErrors           atomic.Int64
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
	stmtGetObserverRowid        *sql.Stmt
	stmtUpdateNodeTelemetry *sql.Stmt
}

// OpenStore opens or creates a SQLite DB at the given path, applying the
// v3 schema that is compatible with the Node.js server.
func OpenStore(dbPath string) (*Store, error) {
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

	s := &Store{db: db}
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
			noise_floor REAL
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
			SELECT o.id, t.raw_hex,
				   datetime(o.timestamp, 'unixepoch') AS timestamp,
				   obs.id AS observer_id, obs.name AS observer_name,
				   o.direction, o.snr, o.rssi, o.score, t.hash, t.route_type,
				   t.payload_type, t.payload_version, o.path_json, t.decoded_json,
				   t.created_at
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
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

	return nil
}

func (s *Store) prepareStatements() error {
	var err error

	s.stmtGetTxByHash, err = s.db.Prepare("SELECT id, first_seen FROM transmissions WHERE hash = ?")
	if err != nil {
		return err
	}

	s.stmtInsertTransmission, err = s.db.Prepare(`
		INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}

	s.stmtUpdateTxFirstSeen, err = s.db.Prepare("UPDATE transmissions SET first_seen = ? WHERE id = ?")
	if err != nil {
		return err
	}

	s.stmtInsertObservation, err = s.db.Prepare(`
		INSERT OR IGNORE INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
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
		INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, battery_mv, uptime_secs, noise_floor)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = COALESCE(?, name),
			iata = COALESCE(?, iata),
			last_seen = ?,
			packet_count = packet_count + 1,
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

	s.stmtUpdateNodeTelemetry, err = s.db.Prepare(`
		UPDATE nodes SET
			battery_mv = COALESCE(?, battery_mv),
			temperature_c = COALESCE(?, temperature_c)
		WHERE public_key = ?
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
			data.DecodedJSON,
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

	// Resolve observer_idx
	var observerIdx *int64
	if data.ObserverID != "" {
		var rowid int64
		err := s.stmtGetObserverRowid.QueryRow(data.ObserverID).Scan(&rowid)
		if err == nil {
			observerIdx = &rowid
		}
	}

	// Insert observation
	epochTs := time.Now().Unix()
	if t, err := time.Parse(time.RFC3339, now); err == nil {
		epochTs = t.Unix()
	}

	_, err = s.stmtInsertObservation.Exec(
		txID, observerIdx, nil, // direction
		data.SNR, data.RSSI, nil, // score
		data.PathJSON, epochTs,
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
	BatteryMv  *int     // millivolts, always integer
	UptimeSecs *int64   // seconds, always integer
	NoiseFloor *float64 // dBm, may have decimals
}

// UpsertObserver inserts or updates an observer with optional hardware metadata.
func (s *Store) UpsertObserver(id, name, iata string, meta *ObserverMeta) error {
	now := time.Now().UTC().Format(time.RFC3339)

	var batteryMv, uptimeSecs, noiseFloor interface{}
	if meta != nil {
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
		id, name, iata, now, now, batteryMv, uptimeSecs, noiseFloor,
		name, iata, now, batteryMv, uptimeSecs, noiseFloor,
	)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
	} else {
		s.Stats.ObserverUpserts.Add(1)
	}
	return err
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// LogStats logs current operational metrics.
func (s *Store) LogStats() {
	log.Printf("[stats] tx_inserted=%d tx_dupes=%d obs_inserted=%d node_upserts=%d observer_upserts=%d write_errors=%d",
		s.Stats.TransmissionsInserted.Load(),
		s.Stats.DuplicateTransmissions.Load(),
		s.Stats.ObservationsInserted.Load(),
		s.Stats.NodeUpserts.Load(),
		s.Stats.ObserverUpserts.Load(),
		s.Stats.WriteErrors.Load(),
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

// PacketData holds the data needed to insert a packet into the DB.
type PacketData struct {
	RawHex         string
	Timestamp      string
	ObserverID     string
	ObserverName   string
	SNR            *float64
	RSSI           *float64
	Hash           string
	RouteType      int
	PayloadType    int
	PayloadVersion int
	PathJSON       string
	DecodedJSON    string
}

// MQTTPacketMessage is the JSON payload from an MQTT raw packet message.
type MQTTPacketMessage struct {
	Raw    string   `json:"raw"`
	SNR    *float64 `json:"SNR"`
	RSSI   *float64 `json:"RSSI"`
	Origin string   `json:"origin"`
}

// BuildPacketData constructs a PacketData from a decoded packet and MQTT message.
func BuildPacketData(msg *MQTTPacketMessage, decoded *DecodedPacket, observerID, region string) *PacketData {
	now := time.Now().UTC().Format(time.RFC3339)
	pathJSON := "[]"
	if len(decoded.Path.Hops) > 0 {
		b, _ := json.Marshal(decoded.Path.Hops)
		pathJSON = string(b)
	}

	return &PacketData{
		RawHex:         msg.Raw,
		Timestamp:      now,
		ObserverID:     observerID,
		ObserverName:   msg.Origin,
		SNR:            msg.SNR,
		RSSI:           msg.RSSI,
		Hash:           ComputeContentHash(msg.Raw),
		RouteType:      decoded.Header.RouteType,
		PayloadType:    decoded.Header.PayloadType,
		PayloadVersion: decoded.Header.PayloadVersion,
		PathJSON:       pathJSON,
		DecodedJSON:    PayloadJSON(&decoded.Payload),
	}
}
