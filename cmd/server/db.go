package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a read-only connection to the MeshCore SQLite database.
type DB struct {
	conn             *sql.DB
	path             string // filesystem path to the database file
	isV3             bool   // v3 schema: observer_idx in observations (vs observer_id in v2)
	hasResolvedPath  bool   // observations table has resolved_path column
	hasObsRawHex     bool   // observations table has raw_hex column (#881)
	hasScopeName     bool   // transmissions.scope_name column exists (#899)

	// Channel list cache (60s TTL) — avoids repeated GROUP BY scans (#762)
	channelsCacheMu  sync.Mutex
	channelsCacheKey string
	channelsCacheRes []map[string]interface{}
	channelsCacheExp time.Time
}

// OpenDB opens a read-only SQLite connection with WAL mode.
func OpenDB(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_journal_mode=WAL&_busy_timeout=5000", path)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(2)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping failed: %w", err)
	}
	d := &DB{conn: conn, path: path}
	d.detectSchema()
	return d, nil
}

func (db *DB) Close() error {
	// Checkpoint WAL before closing to release lock cleanly for new processes
	if _, err := db.conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("[db] WAL checkpoint error: %v", err)
	} else {
		log.Println("[db] WAL checkpoint complete")
	}
	return db.conn.Close()
}

// detectSchema checks if the observations table uses v3 schema (observer_idx).
func (db *DB) detectSchema() {
	rows, err := db.conn.Query("PRAGMA table_info(observations)")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var colName string
		var colType sql.NullString
		var notNull, pk int
		var dflt sql.NullString
		if rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk) == nil {
			if colName == "observer_idx" {
				db.isV3 = true
			}
			if colName == "resolved_path" {
				db.hasResolvedPath = true
			}
			if colName == "raw_hex" {
				db.hasObsRawHex = true
			}
		}
	}

	txRows, err := db.conn.Query("PRAGMA table_info(transmissions)")
	if err != nil {
		return
	}
	defer txRows.Close()
	for txRows.Next() {
		var cid int
		var colName string
		var colType sql.NullString
		var notNull, pk int
		var dflt sql.NullString
		if txRows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk) == nil {
			if colName == "scope_name" {
				db.hasScopeName = true
			}
		}
	}
}

// transmissionBaseSQL returns the SELECT columns and JOIN clause for transmission-centric queries.
func (db *DB) transmissionBaseSQL() (selectCols, observerJoin string) {
	if db.isV3 {
		selectCols = `t.id, t.raw_hex, t.hash, t.first_seen, t.route_type, t.payload_type, t.decoded_json,
			COALESCE((SELECT COUNT(*) FROM observations WHERE transmission_id = t.id), 0) AS observation_count,
			obs.id AS observer_id, obs.name AS observer_name,
			o.snr, o.rssi, o.path_json, o.direction`
		observerJoin = `LEFT JOIN observations o ON o.id = (
				SELECT id FROM observations WHERE transmission_id = t.id
				ORDER BY length(COALESCE(path_json,'')) DESC LIMIT 1
			)
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx`
	} else {
		selectCols = `t.id, t.raw_hex, t.hash, t.first_seen, t.route_type, t.payload_type, t.decoded_json,
			COALESCE((SELECT COUNT(*) FROM observations WHERE transmission_id = t.id), 0) AS observation_count,
			o.observer_id, o.observer_name,
			o.snr, o.rssi, o.path_json, o.direction`
		observerJoin = `LEFT JOIN observations o ON o.id = (
				SELECT id FROM observations WHERE transmission_id = t.id
				ORDER BY length(COALESCE(path_json,'')) DESC LIMIT 1
			)`
	}
	return
}

// scanTransmissionRow scans a row from the transmission-centric query.
// Returns a map matching the Node.js packet-store transmission shape.
func (db *DB) scanTransmissionRow(rows *sql.Rows) map[string]interface{} {
	var id, observationCount int
	var rawHex, hash, firstSeen, decodedJSON, observerID, observerName, pathJSON, direction sql.NullString
	var routeType, payloadType sql.NullInt64
	var snr, rssi sql.NullFloat64

	if err := rows.Scan(&id, &rawHex, &hash, &firstSeen, &routeType, &payloadType, &decodedJSON,
		&observationCount, &observerID, &observerName, &snr, &rssi, &pathJSON, &direction); err != nil {
		return nil
	}

	return map[string]interface{}{
		"id":                id,
		"raw_hex":           nullStr(rawHex),
		"hash":              nullStr(hash),
		"first_seen":        nullStr(firstSeen),
		"timestamp":         nullStr(firstSeen),
		"route_type":        nullInt(routeType),
		"payload_type":      nullInt(payloadType),
		"decoded_json":      nullStr(decodedJSON),
		"observation_count": observationCount,
		"observer_id":       nullStr(observerID),
		"observer_name":     nullStr(observerName),
		"snr":               nullFloat(snr),
		"rssi":              nullFloat(rssi),
		"path_json":         nullStr(pathJSON),
		"direction":         nullStr(direction),
	}
}

// Node represents a row from the nodes table.
type Node struct {
	PublicKey     string   `json:"public_key"`
	Name         *string  `json:"name"`
	Role         *string  `json:"role"`
	Lat          *float64 `json:"lat"`
	Lon          *float64 `json:"lon"`
	LastSeen     *string  `json:"last_seen"`
	FirstSeen    *string  `json:"first_seen"`
	AdvertCount  int      `json:"advert_count"`
	BatteryMv    *int     `json:"battery_mv"`
	TemperatureC *float64 `json:"temperature_c"`
}

// Observer represents a row from the observers table.
type Observer struct {
	ID            string   `json:"id"`
	Name          *string  `json:"name"`
	IATA          *string  `json:"iata"`
	LastSeen      *string  `json:"last_seen"`
	FirstSeen     *string  `json:"first_seen"`
	PacketCount   int      `json:"packet_count"`
	Model         *string  `json:"model"`
	Firmware      *string  `json:"firmware"`
	ClientVersion *string  `json:"client_version"`
	Radio         *string  `json:"radio"`
	BatteryMv     *int     `json:"battery_mv"`
	UptimeSecs    *int64   `json:"uptime_secs"`
	NoiseFloor    *float64 `json:"noise_floor"`
}

// Transmission represents a row from the transmissions table.
type Transmission struct {
	ID             int     `json:"id"`
	RawHex         *string `json:"raw_hex"`
	Hash           string  `json:"hash"`
	FirstSeen      string  `json:"first_seen"`
	RouteType      *int    `json:"route_type"`
	PayloadType    *int    `json:"payload_type"`
	PayloadVersion *int    `json:"payload_version"`
	DecodedJSON    *string `json:"decoded_json"`
	CreatedAt      *string `json:"created_at"`
}

// Observation (observation-level data).
type Observation struct {
	ID           int      `json:"id"`
	RawHex       *string  `json:"raw_hex"`
	Timestamp    *string  `json:"timestamp"`
	ObserverID   *string  `json:"observer_id"`
	ObserverName *string  `json:"observer_name"`
	Direction    *string  `json:"direction"`
	SNR          *float64 `json:"snr"`
	RSSI         *float64 `json:"rssi"`
	Score        *int     `json:"score"`
	Hash         *string  `json:"hash"`
	RouteType    *int     `json:"route_type"`
	PayloadType  *int     `json:"payload_type"`
	PayloadVer   *int     `json:"payload_version"`
	PathJSON     *string  `json:"path_json"`
	DecodedJSON  *string  `json:"decoded_json"`
	CreatedAt    *string  `json:"created_at"`
}

// Stats holds system statistics.
type Stats struct {
	TotalPackets       int `json:"totalPackets"`
	TotalTransmissions int `json:"totalTransmissions"`
	TotalObservations  int `json:"totalObservations"`
	TotalNodes         int `json:"totalNodes"`
	TotalNodesAllTime  int `json:"totalNodesAllTime"`
	TotalObservers     int `json:"totalObservers"`
	PacketsLastHour    int `json:"packetsLastHour"`
	PacketsLast24h     int `json:"packetsLast24h"`
}

// GetStats returns aggregate counts (matches Node.js db.getStats shape).
func (db *DB) GetStats() (*Stats, error) {
	s := &Stats{}
	err := db.conn.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&s.TotalTransmissions)
	if err != nil {
		return nil, err
	}
	s.TotalPackets = s.TotalTransmissions

	db.conn.QueryRow("SELECT COUNT(*) FROM observations").Scan(&s.TotalObservations)
	// Node.js uses 7-day active nodes for totalNodes
	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	db.conn.QueryRow("SELECT COUNT(*) FROM nodes WHERE last_seen > ?", sevenDaysAgo).Scan(&s.TotalNodes)
	db.conn.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&s.TotalNodesAllTime)
	db.conn.QueryRow("SELECT COUNT(*) FROM observers").Scan(&s.TotalObservers)

	oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
	db.conn.QueryRow("SELECT COUNT(*) FROM observations WHERE timestamp > ?", oneHourAgo).Scan(&s.PacketsLastHour)

	oneDayAgo := time.Now().Add(-24 * time.Hour).Unix()
	db.conn.QueryRow("SELECT COUNT(*) FROM observations WHERE timestamp > ?", oneDayAgo).Scan(&s.PacketsLast24h)

	return s, nil
}

// GetDBSizeStats returns SQLite file sizes and row counts (matching Node.js /api/perf sqlite shape).
func (db *DB) GetDBSizeStats() map[string]interface{} {
	result := map[string]interface{}{}

	// DB file size
	var dbSizeMB float64
	if db.path != "" && db.path != ":memory:" {
		if info, err := os.Stat(db.path); err == nil {
			dbSizeMB = math.Round(float64(info.Size())/1048576*10) / 10
		}
	}
	result["dbSizeMB"] = dbSizeMB

	// WAL file size
	var walSizeMB float64
	if db.path != "" && db.path != ":memory:" {
		if info, err := os.Stat(db.path + "-wal"); err == nil {
			walSizeMB = math.Round(float64(info.Size())/1048576*10) / 10
		}
	}
	result["walSizeMB"] = walSizeMB

	// Freelist size via PRAGMA (matches Node.js: page_size * freelist_count)
	var pageSize, freelistCount int64
	db.conn.QueryRow("PRAGMA page_size").Scan(&pageSize)
	db.conn.QueryRow("PRAGMA freelist_count").Scan(&freelistCount)
	freelistMB := math.Round(float64(pageSize*freelistCount)/1048576*10) / 10
	result["freelistMB"] = freelistMB

	// WAL checkpoint info (matches Node.js: PRAGMA wal_checkpoint(PASSIVE))
	var walBusy, walLog, walCheckpointed int
	err := db.conn.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&walBusy, &walLog, &walCheckpointed)
	if err == nil {
		result["walPages"] = map[string]interface{}{
			"total":        walLog,
			"checkpointed": walCheckpointed,
			"busy":         walBusy,
		}
	} else {
		result["walPages"] = map[string]interface{}{
			"total":        0,
			"checkpointed": 0,
			"busy":         0,
		}
	}

	// Row counts per table
	rows := map[string]int{}
	for _, table := range []string{"transmissions", "observations", "nodes", "observers"} {
		var count int
		db.conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		rows[table] = count
	}
	result["rows"] = rows

	return result
}

// GetDBSizeStatsTyped returns SQLite file sizes and row counts as a typed struct.
func (db *DB) GetDBSizeStatsTyped() SqliteStats {
	result := SqliteStats{}

	if db.path != "" && db.path != ":memory:" {
		if info, err := os.Stat(db.path); err == nil {
			result.DbSizeMB = math.Round(float64(info.Size())/1048576*10) / 10
		}
	}

	if db.path != "" && db.path != ":memory:" {
		if info, err := os.Stat(db.path + "-wal"); err == nil {
			result.WalSizeMB = math.Round(float64(info.Size())/1048576*10) / 10
		}
	}

	var pageSize, freelistCount int64
	db.conn.QueryRow("PRAGMA page_size").Scan(&pageSize)
	db.conn.QueryRow("PRAGMA freelist_count").Scan(&freelistCount)
	result.FreelistMB = math.Round(float64(pageSize*freelistCount)/1048576*10) / 10

	var walBusy, walLog, walCheckpointed int
	err := db.conn.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&walBusy, &walLog, &walCheckpointed)
	if err == nil {
		result.WalPages = &WalPages{
			Total:        walLog,
			Checkpointed: walCheckpointed,
			Busy:         walBusy,
		}
	} else {
		result.WalPages = &WalPages{}
	}

	rows := &SqliteRowCounts{}
	for _, table := range []string{"transmissions", "observations", "nodes", "observers"} {
		var count int
		db.conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		switch table {
		case "transmissions":
			rows.Transmissions = count
		case "observations":
			rows.Observations = count
		case "nodes":
			rows.Nodes = count
		case "observers":
			rows.Observers = count
		}
	}
	result.Rows = rows

	return result
}

// GetRoleCounts returns count per role (7-day active, matching Node.js /api/stats).
func (db *DB) GetRoleCounts() map[string]int {
	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	counts := map[string]int{}
	for _, role := range []string{"repeater", "room", "companion", "sensor"} {
		var c int
		db.conn.QueryRow("SELECT COUNT(*) FROM nodes WHERE role = ? AND last_seen > ?", role, sevenDaysAgo).Scan(&c)
		counts[role+"s"] = c
	}
	return counts
}

// GetAllRoleCounts returns count per role (all nodes, no time filter — matching Node.js /api/nodes).
func (db *DB) GetAllRoleCounts() map[string]int {
	counts := map[string]int{}
	for _, role := range []string{"repeater", "room", "companion", "sensor"} {
		var c int
		db.conn.QueryRow("SELECT COUNT(*) FROM nodes WHERE role = ?", role).Scan(&c)
		counts[role+"s"] = c
	}
	return counts
}

// PacketQuery holds filter params for packet listing.
type PacketQuery struct {
	Limit    int
	Offset   int
	Type     *int
	Route    *int
	Observer string
	Hash     string
	Since    string
	Until    string
	Region   string
	Node     string
	Channel  string // channel_hash filter (#812). Plain names like "#test"/"public" or "enc_<HEX>" for encrypted
	Order               string // ASC or DESC
	ExpandObservations  bool   // when true, include observation sub-maps in txToMap output
}

// PacketResult wraps paginated packet list.
type PacketResult struct {
	Packets []map[string]interface{} `json:"packets"`
	Total   int                      `json:"total"`
}

// QueryPackets returns paginated, filtered packets as transmissions (matching Node.js shape).
func (db *DB) QueryPackets(q PacketQuery) (*PacketResult, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Order == "" {
		q.Order = "DESC"
	}

	where, args := db.buildTransmissionWhere(q)
	w := ""
	if len(where) > 0 {
		w = "WHERE " + strings.Join(where, " AND ")
	}

	// Count transmissions (not observations)
	var total int
	if len(where) == 0 {
		db.conn.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&total)
	} else {
		countSQL := fmt.Sprintf("SELECT COUNT(*) FROM transmissions t %s", w)
		db.conn.QueryRow(countSQL, args...).Scan(&total)
	}

	selectCols, observerJoin := db.transmissionBaseSQL()
	querySQL := fmt.Sprintf("SELECT %s FROM transmissions t %s %s ORDER BY t.first_seen %s LIMIT ? OFFSET ?",
		selectCols, observerJoin, w, q.Order)

	qArgs := make([]interface{}, len(args))
	copy(qArgs, args)
	qArgs = append(qArgs, q.Limit, q.Offset)

	rows, err := db.conn.Query(querySQL, qArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	packets := make([]map[string]interface{}, 0)
	for rows.Next() {
		p := db.scanTransmissionRow(rows)
		if p != nil {
			packets = append(packets, p)
		}
	}

	return &PacketResult{Packets: packets, Total: total}, nil
}

// QueryGroupedPackets groups by hash (transmissions) — queries transmissions table directly for performance.
func (db *DB) QueryGroupedPackets(q PacketQuery) (*PacketResult, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}

	where, args := db.buildTransmissionWhere(q)
	w := ""
	if len(where) > 0 {
		w = "WHERE " + strings.Join(where, " AND ")
	}

	// Count total transmissions (fast — queries transmissions directly, not a VIEW)
	var total int
	if len(where) == 0 {
		db.conn.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&total)
	} else {
		db.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM transmissions t %s", w), args...).Scan(&total)
	}

	// Build grouped query using transmissions table with correlated subqueries
	var querySQL string
	if db.isV3 {
		querySQL = fmt.Sprintf(`SELECT t.hash, t.first_seen, t.raw_hex, t.decoded_json, t.payload_type, t.route_type,
			COALESCE((SELECT COUNT(*) FROM observations oi WHERE oi.transmission_id = t.id), 0) AS count,
			COALESCE((SELECT COUNT(DISTINCT oi.observer_idx) FROM observations oi WHERE oi.transmission_id = t.id), 0) AS observer_count,
			COALESCE((SELECT MAX(strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', oi.timestamp, 'unixepoch')) FROM observations oi WHERE oi.transmission_id = t.id), t.first_seen) AS latest,
			obs.id AS observer_id, obs.name AS observer_name,
			o.snr, o.rssi, o.path_json
		FROM transmissions t
		LEFT JOIN observations o ON o.id = (
			SELECT id FROM observations WHERE transmission_id = t.id
			ORDER BY length(COALESCE(path_json,'')) DESC LIMIT 1
		)
		LEFT JOIN observers obs ON obs.rowid = o.observer_idx
		%s ORDER BY latest DESC LIMIT ? OFFSET ?`, w)
	} else {
		querySQL = fmt.Sprintf(`SELECT t.hash, t.first_seen, t.raw_hex, t.decoded_json, t.payload_type, t.route_type,
			COALESCE((SELECT COUNT(*) FROM observations oi WHERE oi.transmission_id = t.id), 0) AS count,
			COALESCE((SELECT COUNT(DISTINCT oi.observer_id) FROM observations oi WHERE oi.transmission_id = t.id), 0) AS observer_count,
			COALESCE((SELECT MAX(oi.timestamp) FROM observations oi WHERE oi.transmission_id = t.id), t.first_seen) AS latest,
			o.observer_id, o.observer_name,
			o.snr, o.rssi, o.path_json
		FROM transmissions t
		LEFT JOIN observations o ON o.id = (
			SELECT id FROM observations WHERE transmission_id = t.id
			ORDER BY length(COALESCE(path_json,'')) DESC LIMIT 1
		)
		%s ORDER BY latest DESC LIMIT ? OFFSET ?`, w)
	}

	qArgs := make([]interface{}, len(args))
	copy(qArgs, args)
	qArgs = append(qArgs, q.Limit, q.Offset)

	rows, err := db.conn.Query(querySQL, qArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	packets := make([]map[string]interface{}, 0)
	for rows.Next() {
		var hash, firstSeen, rawHex, decodedJSON, latest, observerID, observerName, pathJSON sql.NullString
		var payloadType, routeType sql.NullInt64
		var count, observerCount int
		var snr, rssi sql.NullFloat64

		if err := rows.Scan(&hash, &firstSeen, &rawHex, &decodedJSON, &payloadType, &routeType,
			&count, &observerCount, &latest,
			&observerID, &observerName, &snr, &rssi, &pathJSON); err != nil {
			continue
		}

		packets = append(packets, map[string]interface{}{
			"hash":              nullStr(hash),
			"first_seen":        nullStr(firstSeen),
			"count":             count,
			"observer_count":    observerCount,
			"observation_count": count,
			"latest":            nullStr(latest),
			"observer_id":       nullStr(observerID),
			"observer_name":     nullStr(observerName),
			"path_json":         nullStr(pathJSON),
			"payload_type":      nullInt(payloadType),
			"route_type":        nullInt(routeType),
			"raw_hex":           nullStr(rawHex),
			"decoded_json":      nullStr(decodedJSON),
			"snr":               nullFloat(snr),
			"rssi":              nullFloat(rssi),
		})
	}

	return &PacketResult{Packets: packets, Total: total}, nil
}

func (db *DB) buildPacketWhere(q PacketQuery) ([]string, []interface{}) {
	var where []string
	var args []interface{}

	if q.Type != nil {
		where = append(where, "payload_type = ?")
		args = append(args, *q.Type)
	}
	if q.Route != nil {
		where = append(where, "route_type = ?")
		args = append(args, *q.Route)
	}
	if q.Observer != "" {
		where = append(where, "observer_id = ?")
		args = append(args, q.Observer)
	}
	if q.Hash != "" {
		where = append(where, "hash = ?")
		args = append(args, strings.ToLower(q.Hash))
	}
	if q.Since != "" {
		where = append(where, "timestamp > ?")
		args = append(args, q.Since)
	}
	if q.Until != "" {
		where = append(where, "timestamp < ?")
		args = append(args, q.Until)
	}
	if q.Region != "" {
		where = append(where, "observer_id IN (SELECT id FROM observers WHERE iata = ?)")
		args = append(args, q.Region)
	}
	if q.Node != "" {
		pk := db.resolveNodePubkey(q.Node)
		where = append(where, "decoded_json LIKE ?")
		args = append(args, "%"+pk+"%")
	}
	return where, args
}

// buildTransmissionWhere builds WHERE clauses for transmission-centric queries.
// Uses t. prefix for transmission columns and EXISTS subqueries for observation filters.
func (db *DB) buildTransmissionWhere(q PacketQuery) ([]string, []interface{}) {
	var where []string
	var args []interface{}

	if q.Type != nil {
		where = append(where, "t.payload_type = ?")
		args = append(args, *q.Type)
	}
	if q.Route != nil {
		where = append(where, "t.route_type = ?")
		args = append(args, *q.Route)
	}
	if q.Hash != "" {
		where = append(where, "t.hash = ?")
		args = append(args, strings.ToLower(q.Hash))
	}
	if q.Since != "" {
		if t, err := time.Parse(time.RFC3339Nano, q.Since); err == nil {
			where = append(where, "t.id IN (SELECT DISTINCT transmission_id FROM observations WHERE timestamp >= ?)")
			args = append(args, t.Unix())
		} else {
			where = append(where, "t.first_seen > ?")
			args = append(args, q.Since)
		}
	}
	if q.Until != "" {
		if t, err := time.Parse(time.RFC3339Nano, q.Until); err == nil {
			where = append(where, "t.id IN (SELECT DISTINCT transmission_id FROM observations WHERE timestamp <= ?)")
			args = append(args, t.Unix())
		} else {
			where = append(where, "t.first_seen < ?")
			args = append(args, q.Until)
		}
	}
	if q.Node != "" {
		pk := db.resolveNodePubkey(q.Node)
		where = append(where, "t.decoded_json LIKE ?")
		args = append(args, "%"+pk+"%")
	}
	if q.Channel != "" {
		// channel_hash column is indexed for payload_type = 5; filter is exact match.
		where = append(where, "t.channel_hash = ?")
		args = append(args, q.Channel)
	}
	if q.Observer != "" {
		ids := strings.Split(q.Observer, ",")
		placeholders := strings.Repeat("?,", len(ids))
		placeholders = placeholders[:len(placeholders)-1]
		if db.isV3 {
			where = append(where, "EXISTS (SELECT 1 FROM observations oi JOIN observers obi ON obi.rowid = oi.observer_idx WHERE oi.transmission_id = t.id AND obi.id IN ("+placeholders+"))")
		} else {
			where = append(where, "EXISTS (SELECT 1 FROM observations oi WHERE oi.transmission_id = t.id AND oi.observer_id IN ("+placeholders+"))")
		}
		for _, id := range ids {
			args = append(args, strings.TrimSpace(id))
		}
	}
	if q.Region != "" {
		if db.isV3 {
			where = append(where, "EXISTS (SELECT 1 FROM observations oi JOIN observers obi ON obi.rowid = oi.observer_idx WHERE oi.transmission_id = t.id AND obi.iata = ?)")
		} else {
			where = append(where, "EXISTS (SELECT 1 FROM observations oi JOIN observers obi ON obi.id = oi.observer_id WHERE oi.transmission_id = t.id AND obi.iata = ?)")
		}
		args = append(args, q.Region)
	}
	return where, args
}

func (db *DB) resolveNodePubkey(nodeIDOrName string) string {
	var pk string
	err := db.conn.QueryRow("SELECT public_key FROM nodes WHERE public_key = ? OR name = ? LIMIT 1", nodeIDOrName, nodeIDOrName).Scan(&pk)
	if err != nil {
		return nodeIDOrName
	}
	return pk
}


// GetTransmissionByID fetches from transmissions table with observer data.
func (db *DB) GetTransmissionByID(id int) (map[string]interface{}, error) {
	selectCols, observerJoin := db.transmissionBaseSQL()
	querySQL := fmt.Sprintf("SELECT %s FROM transmissions t %s WHERE t.id = ?", selectCols, observerJoin)

	rows, err := db.conn.Query(querySQL, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return db.scanTransmissionRow(rows), nil
	}
	return nil, nil
}

// GetPacketByHash fetches a transmission by content hash with observer data.
func (db *DB) GetPacketByHash(hash string) (map[string]interface{}, error) {
	selectCols, observerJoin := db.transmissionBaseSQL()
	querySQL := fmt.Sprintf("SELECT %s FROM transmissions t %s WHERE t.hash = ?", selectCols, observerJoin)

	rows, err := db.conn.Query(querySQL, strings.ToLower(hash))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return db.scanTransmissionRow(rows), nil
	}
	return nil, nil
}

// GetObservationsForHash returns all observations for the transmission with
// the given content hash. Used as a fallback by the packet-detail handler
// when the in-memory PacketStore has pruned the entry but the DB still has it.
func (db *DB) GetObservationsForHash(hash string) []map[string]interface{} {
	var txID int
	err := db.conn.QueryRow("SELECT id FROM transmissions WHERE hash = ?",
		strings.ToLower(hash)).Scan(&txID)
	if err != nil {
		return nil
	}
	obsByTx := db.getObservationsForTransmissions([]int{txID})
	return obsByTx[txID]
}


// GetNodes returns filtered, paginated node list.
func (db *DB) GetNodes(limit, offset int, role, search, before, lastHeard, sortBy, region string) ([]map[string]interface{}, int, map[string]int, error) {
	var where []string
	var args []interface{}

	if role != "" {
		where = append(where, "role = ?")
		args = append(args, role)
	}
	if search != "" {
		where = append(where, "name LIKE ?")
		args = append(args, "%"+search+"%")
	}
	if before != "" {
		where = append(where, "first_seen <= ?")
		args = append(args, before)
	}
	if lastHeard != "" {
		durations := map[string]int64{
			"1h": 3600000, "6h": 21600000, "24h": 86400000,
			"7d": 604800000, "30d": 2592000000,
		}
		if ms, ok := durations[lastHeard]; ok {
			since := time.Now().Add(-time.Duration(ms) * time.Millisecond).Format(time.RFC3339)
			where = append(where, "last_seen > ?")
			args = append(args, since)
		}
	}

	if region != "" {
		codes := normalizeRegionCodes(region)
		if len(codes) > 0 {
			placeholders := make([]string, len(codes))
			regionArgs := make([]interface{}, len(codes))
			for i, c := range codes {
				placeholders[i] = "?"
				regionArgs[i] = c
			}
			joinCond := "obs.rowid = o.observer_idx"
			if !db.isV3 {
				joinCond = "obs.id = o.observer_id"
			}
			subq := fmt.Sprintf(`public_key IN (
				SELECT DISTINCT JSON_EXTRACT(t.decoded_json, '$.pubKey')
				FROM transmissions t
				JOIN observations o ON o.transmission_id = t.id
				JOIN observers obs ON %s
				WHERE t.payload_type = 4
				AND UPPER(TRIM(obs.iata)) IN (%s)
			)`, joinCond, strings.Join(placeholders, ","))
			where = append(where, subq)
			args = append(args, regionArgs...)
		}
	}

	w := ""
	if len(where) > 0 {
		w = "WHERE " + strings.Join(where, " AND ")
	}

	sortMap := map[string]string{
		"name": "name ASC", "lastSeen": "last_seen DESC", "packetCount": "advert_count DESC",
	}
	order := "last_seen DESC"
	if s, ok := sortMap[sortBy]; ok {
		order = s
	}

	if limit <= 0 {
		limit = 50
	}

	var total int
	db.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM nodes %s", w), args...).Scan(&total)

	querySQL := fmt.Sprintf("SELECT public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c FROM nodes %s ORDER BY %s LIMIT ? OFFSET ?", w, order)
	qArgs := append(args, limit, offset)

	rows, err := db.conn.Query(querySQL, qArgs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer rows.Close()

	nodes := make([]map[string]interface{}, 0)
	for rows.Next() {
		n := scanNodeRow(rows)
		if n != nil {
			nodes = append(nodes, n)
		}
	}

	counts := db.GetAllRoleCounts()
	return nodes, total, counts, nil
}

// SearchNodes searches nodes by name or pubkey prefix.
func (db *DB) SearchNodes(query string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.conn.Query(`SELECT public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c
		FROM nodes WHERE name LIKE ? OR public_key LIKE ? ORDER BY last_seen DESC LIMIT ?`,
		"%"+query+"%", query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := make([]map[string]interface{}, 0)
	for rows.Next() {
		n := scanNodeRow(rows)
		if n != nil {
			nodes = append(nodes, n)
		}
	}
	return nodes, nil
}

// GetNodeByPubkey returns a single node.
func (db *DB) GetNodeByPubkey(pubkey string) (map[string]interface{}, error) {
	rows, err := db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c FROM nodes WHERE public_key = ?", pubkey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanNodeRow(rows), nil
	}
	return nil, nil
}


// GetRecentTransmissionsForNode returns recent transmissions referencing a node (Node.js-compatible shape).
func (db *DB) GetRecentTransmissionsForNode(pubkey string, name string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 20
	}
	pk := "%" + pubkey + "%"
	np := "%" + name + "%"

	selectCols, observerJoin := db.transmissionBaseSQL()

	var querySQL string
	var args []interface{}
	if name != "" {
		querySQL = fmt.Sprintf("SELECT %s FROM transmissions t %s WHERE t.decoded_json LIKE ? OR t.decoded_json LIKE ? ORDER BY t.first_seen DESC LIMIT ?",
			selectCols, observerJoin)
		args = []interface{}{pk, np, limit}
	} else {
		querySQL = fmt.Sprintf("SELECT %s FROM transmissions t %s WHERE t.decoded_json LIKE ? ORDER BY t.first_seen DESC LIMIT ?",
			selectCols, observerJoin)
		args = []interface{}{pk, limit}
	}

	rows, err := db.conn.Query(querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	packets := make([]map[string]interface{}, 0)
	var txIDs []int
	for rows.Next() {
		p := db.scanTransmissionRow(rows)
		if p != nil {
			// Placeholder for observations — filled below
			p["observations"] = []map[string]interface{}{}
			if id, ok := p["id"].(int); ok {
				txIDs = append(txIDs, id)
			}
			packets = append(packets, p)
		}
	}

	// Fetch observations for all transmissions
	if len(txIDs) > 0 {
		obsMap := db.getObservationsForTransmissions(txIDs)
		for _, p := range packets {
			if id, ok := p["id"].(int); ok {
				if obs, found := obsMap[id]; found {
					p["observations"] = obs
				}
			}
		}
	}

	return packets, nil
}

// getObservationsForTransmissions fetches all observations for a set of transmission IDs,
// returning a map of txID → []observation maps (matching Node.js recentAdverts shape).
func (db *DB) getObservationsForTransmissions(txIDs []int) map[int][]map[string]interface{} {
	result := make(map[int][]map[string]interface{})
	if len(txIDs) == 0 {
		return result
	}

	// Build IN clause
	placeholders := make([]string, len(txIDs))
	args := make([]interface{}, len(txIDs))
	for i, id := range txIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	var querySQL string
	if db.isV3 {
		querySQL = fmt.Sprintf(`SELECT o.transmission_id, o.id, obs.id AS observer_id, obs.name AS observer_name,
			o.direction, o.snr, o.rssi, o.path_json, strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', o.timestamp, 'unixepoch') AS obs_timestamp
			FROM observations o
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE o.transmission_id IN (%s)
			ORDER BY o.timestamp DESC`, strings.Join(placeholders, ","))
	} else {
		querySQL = fmt.Sprintf(`SELECT o.transmission_id, o.id, o.observer_id, o.observer_name,
			o.direction, o.snr, o.rssi, o.path_json, o.timestamp AS obs_timestamp
			FROM observations o
			WHERE o.transmission_id IN (%s)
			ORDER BY o.timestamp DESC`, strings.Join(placeholders, ","))
	}

	rows, err := db.conn.Query(querySQL, args...)
	if err != nil {
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var txID, obsID int
		var observerID, observerName, direction, pathJSON, obsTimestamp sql.NullString
		var snr, rssi sql.NullFloat64

		if err := rows.Scan(&txID, &obsID, &observerID, &observerName, &direction,
			&snr, &rssi, &pathJSON, &obsTimestamp); err != nil {
			continue
		}

		ts := nullStr(obsTimestamp)
		if s, ok := ts.(string); ok {
			ts = normalizeTimestamp(s)
		}

		obs := map[string]interface{}{
			"id":              obsID,
			"transmission_id": txID,
			"observer_id":     nullStr(observerID),
			"observer_name":   nullStr(observerName),
			"snr":             nullFloat(snr),
			"rssi":            nullFloat(rssi),
			"path_json":       nullStr(pathJSON),
			"timestamp":       ts,
		}
		result[txID] = append(result[txID], obs)
	}

	return result
}

// GetObservers returns all observers sorted by last_seen DESC.
func (db *DB) GetObservers() ([]Observer, error) {
	rows, err := db.conn.Query("SELECT id, name, iata, last_seen, first_seen, packet_count, model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor FROM observers ORDER BY last_seen DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var observers []Observer
	for rows.Next() {
		var o Observer
		var batteryMv, uptimeSecs sql.NullInt64
		var noiseFloor sql.NullFloat64
		if err := rows.Scan(&o.ID, &o.Name, &o.IATA, &o.LastSeen, &o.FirstSeen, &o.PacketCount, &o.Model, &o.Firmware, &o.ClientVersion, &o.Radio, &batteryMv, &uptimeSecs, &noiseFloor); err != nil {
			continue
		}
		if batteryMv.Valid {
			v := int(batteryMv.Int64)
			o.BatteryMv = &v
		}
		if uptimeSecs.Valid {
			o.UptimeSecs = &uptimeSecs.Int64
		}
		if noiseFloor.Valid {
			o.NoiseFloor = &noiseFloor.Float64
		}
		observers = append(observers, o)
	}
	return observers, nil
}

// GetObserverByID returns a single observer.
func (db *DB) GetObserverByID(id string) (*Observer, error) {
	var o Observer
	var batteryMv, uptimeSecs sql.NullInt64
	var noiseFloor sql.NullFloat64
	err := db.conn.QueryRow("SELECT id, name, iata, last_seen, first_seen, packet_count, model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor FROM observers WHERE id = ?", id).
		Scan(&o.ID, &o.Name, &o.IATA, &o.LastSeen, &o.FirstSeen, &o.PacketCount, &o.Model, &o.Firmware, &o.ClientVersion, &o.Radio, &batteryMv, &uptimeSecs, &noiseFloor)
	if err != nil {
		return nil, err
	}
	if batteryMv.Valid {
		v := int(batteryMv.Int64)
		o.BatteryMv = &v
	}
	if uptimeSecs.Valid {
		o.UptimeSecs = &uptimeSecs.Int64
	}
	if noiseFloor.Valid {
		o.NoiseFloor = &noiseFloor.Float64
	}
	return &o, nil
}

// GetObserverIdsForRegion returns observer IDs for given IATA codes.
func (db *DB) GetObserverIdsForRegion(regionParam string) ([]string, error) {
	codes := normalizeRegionCodes(regionParam)
	if len(codes) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(codes))
	args := make([]interface{}, len(codes))
	for i, c := range codes {
		placeholders[i] = "?"
		args[i] = c
	}
	rows, err := db.conn.Query(fmt.Sprintf("SELECT id FROM observers WHERE UPPER(TRIM(iata)) IN (%s)", strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func normalizeRegionCodes(regionParam string) []string {
	if regionParam == "" {
		return nil
	}
	tokens := strings.Split(regionParam, ",")
	codes := make([]string, 0, len(tokens))
	for _, token := range tokens {
		code := strings.TrimSpace(strings.ToUpper(token))
		if code != "" {
			codes = append(codes, code)
		}
	}
	return codes
}

// GetDistinctIATAs returns all distinct IATA codes from observers.
func (db *DB) GetDistinctIATAs() ([]string, error) {
	rows, err := db.conn.Query("SELECT DISTINCT iata FROM observers WHERE iata IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var code string
		rows.Scan(&code)
		codes = append(codes, code)
	}
	return codes, nil
}


// GetNetworkStatus returns overall network health status.
func (db *DB) GetNetworkStatus(healthThresholds HealthThresholds) (map[string]interface{}, error) {
	rows, err := db.conn.Query("SELECT public_key, name, role, last_seen FROM nodes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now().UnixMilli()
	active, degraded, silent, total := 0, 0, 0, 0
	roleCounts := map[string]int{}

	for rows.Next() {
		var pk string
		var name, role, lastSeen sql.NullString
		rows.Scan(&pk, &name, &role, &lastSeen)
		total++
		r := "unknown"
		if role.Valid {
			r = role.String
		}
		roleCounts[r]++

		age := int64(math.MaxInt64)
		if lastSeen.Valid {
			if t, err := time.Parse(time.RFC3339, lastSeen.String); err == nil {
				age = now - t.UnixMilli()
			} else if t, err := time.Parse("2006-01-02 15:04:05", lastSeen.String); err == nil {
				age = now - t.UnixMilli()
			}
		}
		degradedMs, silentMs := healthThresholds.GetHealthMs(r)
		if age < int64(degradedMs) {
			active++
		} else if age < int64(silentMs) {
			degraded++
		} else {
			silent++
		}
	}

	return map[string]interface{}{
		"total": total, "active": active, "degraded": degraded, "silent": silent,
		"roleCounts": roleCounts,
	}, nil
}

// GetTraces returns observations for a hash using direct table queries.
func (db *DB) GetTraces(hash string) ([]map[string]interface{}, error) {
	var querySQL string
	if db.isV3 {
		querySQL = `SELECT obs.id AS observer_id, obs.name AS observer_name,
			strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch') AS timestamp,
			o.snr, o.rssi, o.path_json
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE t.hash = ?
			ORDER BY o.timestamp ASC`
	} else {
		querySQL = `SELECT o.observer_id, o.observer_name,
			strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch') AS timestamp,
			o.snr, o.rssi, o.path_json
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			WHERE t.hash = ?
			ORDER BY o.timestamp ASC`
	}
	rows, err := db.conn.Query(querySQL, strings.ToLower(hash))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var traces []map[string]interface{}
	for rows.Next() {
		var obsID, obsName, ts, pathJSON sql.NullString
		var snr, rssi sql.NullFloat64
		rows.Scan(&obsID, &obsName, &ts, &snr, &rssi, &pathJSON)
		traces = append(traces, map[string]interface{}{
			"observer":      nullStr(obsID),
			"observer_name": nullStr(obsName),
			"time":          nullStr(ts),
			"snr":           nullFloat(snr),
			"rssi":          nullFloat(rssi),
			"path_json":     nullStr(pathJSON),
		})
	}
	if traces == nil {
		traces = make([]map[string]interface{}, 0)
	}
	return traces, nil
}

// GetChannels returns channel list from GRP_TXT packets.
// Queries transmissions directly (not a VIEW) to avoid observation-level
// duplicates that could cause stale lastMessage when an older message has
// a later re-observation timestamp.
func (db *DB) GetChannels(region ...string) ([]map[string]interface{}, error) {
	regionParam := ""
	if len(region) > 0 {
		regionParam = region[0]
	}

	// Check cache (60s TTL)
	db.channelsCacheMu.Lock()
	if db.channelsCacheRes != nil && db.channelsCacheKey == regionParam && time.Now().Before(db.channelsCacheExp) {
		res := db.channelsCacheRes
		db.channelsCacheMu.Unlock()
		return res, nil
	}
	db.channelsCacheMu.Unlock()

	regionCodes := normalizeRegionCodes(regionParam)

	var querySQL string
	args := make([]interface{}, 0, len(regionCodes))

	if len(regionCodes) > 0 {
		placeholders := make([]string, len(regionCodes))
		for i, code := range regionCodes {
			placeholders[i] = "?"
			args = append(args, code)
		}
		regionPlaceholder := strings.Join(placeholders, ",")
		if db.isV3 {
			querySQL = fmt.Sprintf(`SELECT t.channel_hash,
					COUNT(*) AS msg_count,
					MAX(t.first_seen) AS last_activity,
					(SELECT t2.decoded_json FROM transmissions t2
					 WHERE t2.channel_hash = t.channel_hash AND t2.payload_type = 5
					 ORDER BY t2.first_seen DESC LIMIT 1) AS sample_json
				FROM transmissions t
				JOIN observations o ON o.transmission_id = t.id
				LEFT JOIN observers obs ON obs.rowid = o.observer_idx
				WHERE t.payload_type = 5
				AND t.channel_hash IS NOT NULL
				AND t.channel_hash NOT LIKE 'enc_%%'
				AND obs.rowid IS NOT NULL AND UPPER(TRIM(obs.iata)) IN (%s)
				GROUP BY t.channel_hash
				ORDER BY last_activity DESC`, regionPlaceholder)
		} else {
			querySQL = fmt.Sprintf(`SELECT t.channel_hash,
					COUNT(*) AS msg_count,
					MAX(t.first_seen) AS last_activity,
					(SELECT t2.decoded_json FROM transmissions t2
					 WHERE t2.channel_hash = t.channel_hash AND t2.payload_type = 5
					 ORDER BY t2.first_seen DESC LIMIT 1) AS sample_json
				FROM transmissions t
				JOIN observations o ON o.transmission_id = t.id
				WHERE t.payload_type = 5
				AND t.channel_hash IS NOT NULL
				AND t.channel_hash NOT LIKE 'enc_%%'
				AND EXISTS (
					SELECT 1 FROM observers obs
					WHERE obs.id = o.observer_id
					AND UPPER(TRIM(obs.iata)) IN (%s)
				)
				GROUP BY t.channel_hash
				ORDER BY last_activity DESC`, regionPlaceholder)
		}
	} else {
		querySQL = `SELECT channel_hash,
				COUNT(*) AS msg_count,
				MAX(first_seen) AS last_activity,
				(SELECT t2.decoded_json FROM transmissions t2
				 WHERE t2.channel_hash = t.channel_hash AND t2.payload_type = 5
				 ORDER BY t2.first_seen DESC LIMIT 1) AS sample_json
			FROM transmissions t
			WHERE payload_type = 5
			AND channel_hash IS NOT NULL
			AND channel_hash NOT LIKE 'enc_%%'
			GROUP BY channel_hash
			ORDER BY last_activity DESC`
	}

	rows, err := db.conn.Query(querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := make([]map[string]interface{}, 0)
	for rows.Next() {
		var chHash, lastActivity, sampleJSON sql.NullString
		var msgCount int
		if err := rows.Scan(&chHash, &msgCount, &lastActivity, &sampleJSON); err != nil {
			continue
		}
		channelName := nullStr(chHash)
		if channelName == "" {
			continue
		}

		var lastMessage, lastSender interface{}
		if sampleJSON.Valid {
			var decoded map[string]interface{}
			if json.Unmarshal([]byte(sampleJSON.String), &decoded) == nil {
				if text, ok := decoded["text"].(string); ok && text != "" {
					idx := strings.Index(text, ": ")
					if idx > 0 {
						lastMessage = text[idx+2:]
					} else {
						lastMessage = text
					}
					if sender, ok := decoded["sender"].(string); ok {
						lastSender = sender
					}
				}
			}
		}

		channels = append(channels, map[string]interface{}{
			"hash": channelName, "name": channelName,
			"lastMessage": lastMessage, "lastSender": lastSender,
			"messageCount": msgCount, "lastActivity": nullStr(lastActivity),
		})
	}

	// Store in cache (60s TTL)
	db.channelsCacheMu.Lock()
	db.channelsCacheRes = channels
	db.channelsCacheKey = regionParam
	db.channelsCacheExp = time.Now().Add(60 * time.Second)
	db.channelsCacheMu.Unlock()

	return channels, nil
}

// GetEncryptedChannels returns channels where all messages are undecryptable (no key).
// Uses channel_hash column (prefixed with 'enc_') for fast grouped queries.
func (db *DB) GetEncryptedChannels(region ...string) ([]map[string]interface{}, error) {
	regionParam := ""
	if len(region) > 0 {
		regionParam = region[0]
	}
	regionCodes := normalizeRegionCodes(regionParam)

	var querySQL string
	args := make([]interface{}, 0, len(regionCodes))

	if len(regionCodes) > 0 {
		placeholders := make([]string, len(regionCodes))
		for i, code := range regionCodes {
			placeholders[i] = "?"
			args = append(args, code)
		}
		regionPlaceholder := strings.Join(placeholders, ",")
		if db.isV3 {
			querySQL = fmt.Sprintf(`SELECT t.channel_hash,
					COUNT(*) AS msg_count,
					MAX(t.first_seen) AS last_activity
				FROM transmissions t
				JOIN observations o ON o.transmission_id = t.id
				LEFT JOIN observers obs ON obs.rowid = o.observer_idx
				WHERE t.payload_type = 5
				AND t.channel_hash LIKE 'enc_%%'
				AND obs.rowid IS NOT NULL AND UPPER(TRIM(obs.iata)) IN (%s)
				GROUP BY t.channel_hash
				ORDER BY last_activity DESC`, regionPlaceholder)
		} else {
			querySQL = fmt.Sprintf(`SELECT t.channel_hash,
					COUNT(*) AS msg_count,
					MAX(t.first_seen) AS last_activity
				FROM transmissions t
				JOIN observations o ON o.transmission_id = t.id
				WHERE t.payload_type = 5
				AND t.channel_hash LIKE 'enc_%%'
				AND EXISTS (
					SELECT 1 FROM observers obs
					WHERE obs.id = o.observer_id
					AND UPPER(TRIM(obs.iata)) IN (%s)
				)
				GROUP BY t.channel_hash
				ORDER BY last_activity DESC`, regionPlaceholder)
		}
	} else {
		querySQL = `SELECT channel_hash,
				COUNT(*) AS msg_count,
				MAX(first_seen) AS last_activity
			FROM transmissions
			WHERE payload_type = 5
			AND channel_hash LIKE 'enc_%%'
			GROUP BY channel_hash
			ORDER BY last_activity DESC`
	}

	rows, err := db.conn.Query(querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := make([]map[string]interface{}, 0)
	for rows.Next() {
		var chHash, lastActivity sql.NullString
		var msgCount int
		if err := rows.Scan(&chHash, &msgCount, &lastActivity); err != nil {
			continue
		}
		fullHash := nullStrVal(chHash) // e.g. "enc_3A"
		hexPart := strings.TrimPrefix(fullHash, "enc_")
		channels = append(channels, map[string]interface{}{
			"hash":         fullHash,
			"name":         "Encrypted (0x" + hexPart + ")",
			"lastMessage":  nil,
			"lastSender":   nil,
			"messageCount": msgCount,
			"lastActivity": nullStr(lastActivity),
			"encrypted":    true,
		})
	}
	return channels, nil
}

// GetChannelMessages returns messages for a specific channel.
// Uses transmission-level ordering (first_seen) to ensure correct message
// sequence even when observations arrive out of order.
func (db *DB) GetChannelMessages(channelHash string, limit, offset int, region ...string) ([]map[string]interface{}, int, error) {
	if limit <= 0 {
		limit = 100
	}

	regionParam := ""
	if len(region) > 0 {
		regionParam = region[0]
	}
	regionCodes := normalizeRegionCodes(regionParam)
	regionArgs := make([]interface{}, 0, len(regionCodes))
	regionPlaceholders := ""
	if len(regionCodes) > 0 {
		placeholders := make([]string, len(regionCodes))
		for i, code := range regionCodes {
			placeholders[i] = "?"
			regionArgs = append(regionArgs, code)
		}
		regionPlaceholders = strings.Join(placeholders, ",")
	}

	// Fetch messages with channel_hash filter (pagination applied in Go after dedup)
	var querySQL string
	args := []interface{}{channelHash}
	if db.isV3 {
		querySQL = `SELECT o.id, t.hash, t.decoded_json, t.first_seen,
				obs.id, obs.name, o.snr, o.path_json
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE t.channel_hash = ? AND t.payload_type = 5`
		if len(regionCodes) > 0 {
			querySQL += fmt.Sprintf(" AND obs.rowid IS NOT NULL AND UPPER(TRIM(obs.iata)) IN (%s)", regionPlaceholders)
			args = append(args, regionArgs...)
		}
		querySQL += `
			ORDER BY t.first_seen ASC`
	} else {
		querySQL = `SELECT o.id, t.hash, t.decoded_json, t.first_seen,
				o.observer_id, o.observer_name, o.snr, o.path_json
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			WHERE t.channel_hash = ? AND t.payload_type = 5`
		if len(regionCodes) > 0 {
			querySQL += fmt.Sprintf(` AND EXISTS (
				SELECT 1 FROM observers obs WHERE obs.id = o.observer_id
				AND UPPER(TRIM(obs.iata)) IN (%s))`, regionPlaceholders)
			args = append(args, regionArgs...)
		}
		querySQL += `
			ORDER BY t.first_seen ASC`
	}

	rows, err := db.conn.Query(querySQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	type msg struct {
		Data    map[string]interface{}
		Repeats int
	}
	msgMap := map[string]*msg{}
	var msgOrder []string

	for rows.Next() {
		var pktID int
		var pktHash, dj, fs, obsID, obsName, pathJSON sql.NullString
		var snr sql.NullFloat64
		rows.Scan(&pktID, &pktHash, &dj, &fs, &obsID, &obsName, &snr, &pathJSON)
		if !dj.Valid {
			continue
		}
		var decoded map[string]interface{}
		if json.Unmarshal([]byte(dj.String), &decoded) != nil {
			continue
		}

		text, _ := decoded["text"].(string)
		sender, _ := decoded["sender"].(string)
		if sender == "" && text != "" {
			idx := strings.Index(text, ": ")
			if idx > 0 && idx < 50 {
				sender = text[:idx]
			}
		}

		dedupeKey := fmt.Sprintf("%s:%s", sender, nullStr(pktHash))

		if existing, ok := msgMap[dedupeKey]; ok {
			existing.Repeats++
		} else {
			displaySender := sender
			displayText := text
			if text != "" {
				idx := strings.Index(text, ": ")
				if idx > 0 && idx < 50 {
					displaySender = text[:idx]
					displayText = text[idx+2:]
				}
			}

			var hops int
			if pathJSON.Valid {
				var h []interface{}
				if json.Unmarshal([]byte(pathJSON.String), &h) == nil {
					hops = len(h)
				}
			}

			senderTs, _ := decoded["sender_timestamp"]
			m := &msg{
				Data: map[string]interface{}{
					"sender":           displaySender,
					"text":             displayText,
					"timestamp":        nullStr(fs),
					"sender_timestamp": senderTs,
					"packetId":         pktID,
					"packetHash":       nullStr(pktHash),
					"repeats":          1,
					"observers":        []string{},
					"hops":             hops,
					"snr":              nullFloat(snr),
				},
				Repeats: 1,
			}
			if obsName.Valid {
				m.Data["observers"] = []string{obsName.String}
			} else if obsID.Valid {
				m.Data["observers"] = []string{obsID.String}
			}
			msgMap[dedupeKey] = m
			msgOrder = append(msgOrder, dedupeKey)
		}
	}

	// Return latest messages (tail) with pagination
	msgTotal := len(msgOrder)
	start := msgTotal - limit - offset
	if start < 0 {
		start = 0
	}
	end := msgTotal - offset
	if end < 0 {
		end = 0
	}
	if end > msgTotal {
		end = msgTotal
	}

	messages := make([]map[string]interface{}, 0)
	for i := start; i < end; i++ {
		key := msgOrder[i]
		m := msgMap[key]
		m.Data["repeats"] = m.Repeats
		messages = append(messages, m.Data)
	}

	return messages, msgTotal, nil
}



// GetNewTransmissionsSince returns new transmissions after a given ID for WebSocket polling.
func (db *DB) GetNewTransmissionsSince(lastID int, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.conn.Query(`SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type, t.payload_type, t.payload_version, t.decoded_json
		FROM transmissions t WHERE t.id > ? ORDER BY t.id ASC LIMIT ?`, lastID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		rows.Scan(&id, &rawHex, &hash, &firstSeen, &routeType, &payloadType, &payloadVersion, &decodedJSON)
		result = append(result, map[string]interface{}{
			"id":              id,
			"raw_hex":         nullStr(rawHex),
			"hash":            nullStr(hash),
			"first_seen":      nullStr(firstSeen),
			"route_type":      nullInt(routeType),
			"payload_type":    nullInt(payloadType),
			"payload_version": nullInt(payloadVersion),
			"decoded_json":    nullStr(decodedJSON),
		})
	}
	return result, nil
}

// GetMaxTransmissionID returns the current max ID for polling.
func (db *DB) GetMaxTransmissionID() int {
	var maxID int
	db.conn.QueryRow("SELECT COALESCE(MAX(id), 0) FROM transmissions").Scan(&maxID)
	return maxID
}

// GetMaxObservationID returns the current max observation ID for polling.
func (db *DB) GetMaxObservationID() int {
	var maxID int
	db.conn.QueryRow("SELECT COALESCE(MAX(id), 0) FROM observations").Scan(&maxID)
	return maxID
}

// GetObserverPacketCounts returns packetsLastHour for all observers (batch query).
func (db *DB) GetObserverPacketCounts(sinceEpoch int64) map[string]int {
	counts := make(map[string]int)
	var rows *sql.Rows
	var err error
	if db.isV3 {
		rows, err = db.conn.Query(`SELECT obs.id, COUNT(*) as cnt
			FROM observations o
			JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE o.timestamp > ?
			GROUP BY obs.id`, sinceEpoch)
	} else {
		rows, err = db.conn.Query(`SELECT o.observer_id, COUNT(*) as cnt
			FROM observations o
			WHERE o.observer_id IS NOT NULL AND o.timestamp > ?
			GROUP BY o.observer_id`, sinceEpoch)
	}
	if err != nil {
		return counts
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var cnt int
		rows.Scan(&id, &cnt)
		counts[id] = cnt
	}
	return counts
}

// GetNodeLocations returns a map of lowercase public_key → {lat, lon, role} for node geo lookups.
func (db *DB) GetNodeLocations() map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})
	rows, err := db.conn.Query("SELECT public_key, lat, lon, role FROM nodes")
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		var role sql.NullString
		var lat, lon sql.NullFloat64
		rows.Scan(&pk, &lat, &lon, &role)
		result[strings.ToLower(pk)] = map[string]interface{}{
			"lat":  nullFloat(lat),
			"lon":  nullFloat(lon),
			"role": nullStr(role),
		}
	}
	return result
}

// GetNodeLocationsByKeys returns location data only for the given public keys.
// This avoids fetching ALL nodes when only a few keys need to be matched.
func (db *DB) GetNodeLocationsByKeys(keys []string) map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})
	if len(keys) == 0 {
		return result
	}
	placeholders := make([]string, len(keys))
	args := make([]interface{}, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = strings.ToLower(k)
	}
	query := "SELECT public_key, lat, lon, role FROM nodes WHERE LOWER(public_key) IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		var role sql.NullString
		var lat, lon sql.NullFloat64
		rows.Scan(&pk, &lat, &lon, &role)
		result[strings.ToLower(pk)] = map[string]interface{}{
			"lat":  nullFloat(lat),
			"lon":  nullFloat(lon),
			"role": nullStr(role),
		}
	}
	return result
}

// QueryMultiNodePackets returns transmissions referencing any of the given pubkeys.
func (db *DB) QueryMultiNodePackets(pubkeys []string, limit, offset int, order, since, until string) (*PacketResult, error) {
	if len(pubkeys) == 0 {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: 0}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if order == "" {
		order = "DESC"
	}

	// Build OR conditions for decoded_json LIKE %pubkey%
	var conditions []string
	var args []interface{}
	for _, pk := range pubkeys {
		// Resolve pubkey to also check by name
		resolved := db.resolveNodePubkey(pk)
		conditions = append(conditions, "t.decoded_json LIKE ?")
		args = append(args, "%"+resolved+"%")
	}
	jsonWhere := "(" + strings.Join(conditions, " OR ") + ")"

	var timeFilters []string
	if since != "" {
		timeFilters = append(timeFilters, "t.first_seen >= ?")
		args = append(args, since)
	}
	if until != "" {
		timeFilters = append(timeFilters, "t.first_seen <= ?")
		args = append(args, until)
	}

	w := "WHERE " + jsonWhere
	if len(timeFilters) > 0 {
		w += " AND " + strings.Join(timeFilters, " AND ")
	}

	var total int
	db.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM transmissions t %s", w), args...).Scan(&total)

	selectCols, observerJoin := db.transmissionBaseSQL()
	querySQL := fmt.Sprintf("SELECT %s FROM transmissions t %s %s ORDER BY t.first_seen %s LIMIT ? OFFSET ?",
		selectCols, observerJoin, w, order)

	qArgs := make([]interface{}, len(args))
	copy(qArgs, args)
	qArgs = append(qArgs, limit, offset)

	rows, err := db.conn.Query(querySQL, qArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	packets := make([]map[string]interface{}, 0)
	for rows.Next() {
		p := db.scanTransmissionRow(rows)
		if p != nil {
			packets = append(packets, p)
		}
	}
	return &PacketResult{Packets: packets, Total: total}, nil
}

// --- Helpers ---

func scanPacketRow(rows *sql.Rows) map[string]interface{} {
	var id int
	var rawHex, ts, obsID, obsName, direction, hash, pathJSON, decodedJSON, createdAt sql.NullString
	var snr, rssi sql.NullFloat64
	var score, routeType, payloadType, payloadVersion sql.NullInt64

	if err := rows.Scan(&id, &rawHex, &ts, &obsID, &obsName, &direction, &snr, &rssi, &score, &hash, &routeType, &payloadType, &payloadVersion, &pathJSON, &decodedJSON, &createdAt); err != nil {
		return nil
	}
	return map[string]interface{}{
		"id":              id,
		"raw_hex":         nullStr(rawHex),
		"timestamp":       nullStr(ts),
		"observer_id":     nullStr(obsID),
		"observer_name":   nullStr(obsName),
		"direction":       nullStr(direction),
		"snr":             nullFloat(snr),
		"rssi":            nullFloat(rssi),
		"score":           nullInt(score),
		"hash":            nullStr(hash),
		"route_type":      nullInt(routeType),
		"payload_type":    nullInt(payloadType),
		"payload_version": nullInt(payloadVersion),
		"path_json":       nullStr(pathJSON),
		"decoded_json":    nullStr(decodedJSON),
		"created_at":      nullStr(createdAt),
	}
}

func scanNodeRow(rows *sql.Rows) map[string]interface{} {
	var pk string
	var name, role, lastSeen, firstSeen sql.NullString
	var lat, lon sql.NullFloat64
	var advertCount int
	var batteryMv sql.NullInt64
	var temperatureC sql.NullFloat64

	if err := rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen, &firstSeen, &advertCount, &batteryMv, &temperatureC); err != nil {
		return nil
	}
	m := map[string]interface{}{
		"public_key":             pk,
		"name":                   nullStr(name),
		"role":                   nullStr(role),
		"lat":                    nullFloat(lat),
		"lon":                    nullFloat(lon),
		"last_seen":              nullStr(lastSeen),
		"first_seen":             nullStr(firstSeen),
		"advert_count":           advertCount,
		"last_heard":             nullStr(lastSeen),
		"hash_size":              nil,
		"hash_size_inconsistent": false,
	}
	if batteryMv.Valid {
		m["battery_mv"] = int(batteryMv.Int64)
	} else {
		m["battery_mv"] = nil
	}
	if temperatureC.Valid {
		m["temperature_c"] = temperatureC.Float64
	} else {
		m["temperature_c"] = nil
	}
	return m
}

func nullStr(ns sql.NullString) interface{} {
	if ns.Valid {
		return ns.String
	}
	return nil
}

func nullStrVal(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullFloat(nf sql.NullFloat64) interface{} {
	if nf.Valid {
		return nf.Float64
	}
	return nil
}

func nullInt(ni sql.NullInt64) interface{} {
	if ni.Valid {
		return int(ni.Int64)
	}
	return nil
}

// PruneOldPackets deletes transmissions and their observations older than the
// given number of days. Nodes and observers are never touched.
// Returns the number of transmissions deleted.
// Opens a separate read-write connection since the main connection is read-only.
func (db *DB) PruneOldPackets(days int) (int64, error) {
	rw, err := openRW(db.path)
	if err != nil {
		return 0, err
	}
	defer rw.Close()

	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	tx, err := rw.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Delete observations linked to old transmissions first (no CASCADE in SQLite)
	_, err = tx.Exec(`DELETE FROM observations WHERE transmission_id IN (
		SELECT id FROM transmissions WHERE first_seen < ?
	)`, cutoff)
	if err != nil {
		return 0, err
	}

	res, err := tx.Exec(`DELETE FROM transmissions WHERE first_seen < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// MetricsSample represents a single row from observer_metrics with computed deltas.
type MetricsSample struct {
	Timestamp     string   `json:"timestamp"`
	NoiseFloor    *float64 `json:"noise_floor"`
	TxAirSecs     *int     `json:"tx_air_secs,omitempty"`
	RxAirSecs     *int     `json:"rx_air_secs,omitempty"`
	RecvErrors    *int     `json:"recv_errors,omitempty"`
	BatteryMv     *int     `json:"battery_mv"`
	PacketsSent   *int     `json:"packets_sent,omitempty"`
	PacketsRecv   *int     `json:"packets_recv,omitempty"`
	TxAirtimePct  *float64 `json:"tx_airtime_pct"`
	RxAirtimePct  *float64 `json:"rx_airtime_pct"`
	RecvErrorRate *float64 `json:"recv_error_rate"`
	IsReboot      bool     `json:"is_reboot_sample,omitempty"`
}

// rawMetricsSample is the raw DB row before delta computation.
type rawMetricsSample struct {
	Timestamp   string
	NoiseFloor  *float64
	TxAirSecs   *int
	RxAirSecs   *int
	RecvErrors  *int
	BatteryMv   *int
	PacketsSent *int
	PacketsRecv *int
}

// GetObserverMetrics returns time-series metrics with server-side delta computation.
// resolution: "5m" (raw), "1h", "1d"
// sampleIntervalSec: expected interval between samples (default 300)
func (db *DB) GetObserverMetrics(observerID, since, until, resolution string, sampleIntervalSec int) ([]MetricsSample, []string, error) {
	if sampleIntervalSec <= 0 {
		sampleIntervalSec = 300
	}

	// Build query based on resolution
	var query string
	args := []interface{}{observerID}

	// Determine the effective bucket size for gap threshold scaling.
	// For raw data (5m), use sampleIntervalSec. For aggregated resolutions,
	// use the bucket duration so consecutive buckets aren't treated as gaps.
	bucketSizeSec := sampleIntervalSec
	switch resolution {
	case "1h":
		bucketSizeSec = 3600
		// Use LAST value per bucket (latest timestamp) instead of MAX to preserve
		// reboot semantics: if a device reboots mid-bucket, the last sample is the
		// post-reboot baseline, not the pre-reboot high-water mark.
		query = `SELECT ts, noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv, packets_sent, packets_recv FROM (
			SELECT
				strftime('%Y-%m-%dT%H:00:00Z', timestamp) as ts,
				noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv, packets_sent, packets_recv,
				ROW_NUMBER() OVER (PARTITION BY observer_id, strftime('%Y-%m-%dT%H:00:00Z', timestamp) ORDER BY timestamp DESC) as rn
			FROM observer_metrics WHERE observer_id = ?`
	case "1d":
		bucketSizeSec = 86400
		query = `SELECT ts, noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv, packets_sent, packets_recv FROM (
			SELECT
				strftime('%Y-%m-%dT00:00:00Z', timestamp) as ts,
				noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv, packets_sent, packets_recv,
				ROW_NUMBER() OVER (PARTITION BY observer_id, strftime('%Y-%m-%dT00:00:00Z', timestamp) ORDER BY timestamp DESC) as rn
			FROM observer_metrics WHERE observer_id = ?`
	default: // "5m" or raw
		query = `SELECT timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv, packets_sent, packets_recv
			FROM observer_metrics WHERE observer_id = ?`
	}

	if since != "" {
		query += " AND timestamp >= ?"
		args = append(args, since)
	}
	if until != "" {
		query += " AND timestamp <= ?"
		args = append(args, until)
	}

	switch resolution {
	case "1h", "1d":
		query += ") WHERE rn = 1 ORDER BY ts ASC"
	default:
		query += " ORDER BY timestamp ASC"
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var raw []rawMetricsSample
	for rows.Next() {
		var s rawMetricsSample
		if err := rows.Scan(&s.Timestamp, &s.NoiseFloor, &s.TxAirSecs, &s.RxAirSecs, &s.RecvErrors, &s.BatteryMv, &s.PacketsSent, &s.PacketsRecv); err != nil {
			return nil, nil, err
		}
		raw = append(raw, s)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Compute deltas between consecutive samples.
	// bucketSizeSec determines gap threshold: for raw data it's sampleIntervalSec,
	// for aggregated resolutions it's the bucket duration (3600 for 1h, 86400 for 1d).
	return computeDeltas(raw, bucketSizeSec)
}

// computeDeltas computes per-interval rates from cumulative counters.
// Handles reboots (counter reset) and gaps (missing samples).
// bucketSizeSec is the expected interval between consecutive points
// (sampleInterval for raw data, bucket duration for aggregated resolutions).
func computeDeltas(raw []rawMetricsSample, bucketSizeSec int) ([]MetricsSample, []string, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}

	gapThreshold := float64(bucketSizeSec) * 2.0
	result := make([]MetricsSample, 0, len(raw))
	var reboots []string

	for i, cur := range raw {
		s := MetricsSample{
			Timestamp:  cur.Timestamp,
			NoiseFloor: cur.NoiseFloor,
			BatteryMv:  cur.BatteryMv,
		}

		if i == 0 {
			// First sample: no delta possible
			result = append(result, s)
			continue
		}

		prev := raw[i-1]

		// Check for gap
		curT, err1 := time.Parse(time.RFC3339, cur.Timestamp)
		prevT, err2 := time.Parse(time.RFC3339, prev.Timestamp)
		if err1 != nil || err2 != nil {
			result = append(result, s)
			continue
		}
		intervalSecs := curT.Sub(prevT).Seconds()
		if intervalSecs > gapThreshold {
			// Gap detected: insert null deltas (don't interpolate)
			result = append(result, s)
			continue
		}
		if intervalSecs <= 0 {
			result = append(result, s)
			continue
		}

		// Detect reboot: any cumulative counter decreased
		isReboot := false
		if cur.TxAirSecs != nil && prev.TxAirSecs != nil && *cur.TxAirSecs < *prev.TxAirSecs {
			isReboot = true
		}
		if cur.RxAirSecs != nil && prev.RxAirSecs != nil && *cur.RxAirSecs < *prev.RxAirSecs {
			isReboot = true
		}
		if cur.RecvErrors != nil && prev.RecvErrors != nil && *cur.RecvErrors < *prev.RecvErrors {
			isReboot = true
		}
		if cur.PacketsSent != nil && prev.PacketsSent != nil && *cur.PacketsSent < *prev.PacketsSent {
			isReboot = true
		}
		if cur.PacketsRecv != nil && prev.PacketsRecv != nil && *cur.PacketsRecv < *prev.PacketsRecv {
			isReboot = true
		}

		if isReboot {
			s.IsReboot = true
			reboots = append(reboots, cur.Timestamp)
			// Skip delta computation for reboot samples — use as new baseline
			result = append(result, s)
			continue
		}

		// Compute TX airtime percentage
		if cur.TxAirSecs != nil && prev.TxAirSecs != nil {
			delta := float64(*cur.TxAirSecs - *prev.TxAirSecs)
			pct := (delta / intervalSecs) * 100.0
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			result_pct := math.Round(pct*100) / 100
			s.TxAirtimePct = &result_pct
		}

		// Compute RX airtime percentage
		if cur.RxAirSecs != nil && prev.RxAirSecs != nil {
			delta := float64(*cur.RxAirSecs - *prev.RxAirSecs)
			pct := (delta / intervalSecs) * 100.0
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			result_pct := math.Round(pct*100) / 100
			s.RxAirtimePct = &result_pct
		}

		// Compute recv error rate
		if cur.RecvErrors != nil && prev.RecvErrors != nil &&
			cur.PacketsRecv != nil && prev.PacketsRecv != nil {
			deltaErrors := float64(*cur.RecvErrors - *prev.RecvErrors)
			deltaRecv := float64(*cur.PacketsRecv - *prev.PacketsRecv)
			total := deltaRecv + deltaErrors
			if total > 0 {
				rate := (deltaErrors / total) * 100.0
				rate = math.Round(rate*100) / 100
				s.RecvErrorRate = &rate
			}
		}

		result = append(result, s)
	}

	return result, reboots, nil
}

// MetricsSummaryRow holds summary data for one observer.
type MetricsSummaryRow struct {
	ObserverID    string     `json:"observer_id"`
	ObserverName  *string    `json:"observer_name"`
	IATA          string     `json:"iata,omitempty"`
	CurrentNF     *float64   `json:"current_noise_floor"`
	AvgNF         *float64   `json:"avg_noise_floor_24h"`
	MaxNF         *float64   `json:"max_noise_floor_24h"`
	CurrentBattMv *int       `json:"battery_mv"`
	SampleCount   int        `json:"sample_count"`
	Sparkline     []*float64 `json:"sparkline"`
}

// GetMetricsSummary returns a fleet summary of observer metrics within a time window.
// Uses a CTE with ROW_NUMBER to get latest values in a single pass (no correlated subqueries).
// Also returns sparkline data (noise_floor time series) per observer.
func (db *DB) GetMetricsSummary(since string) ([]MetricsSummaryRow, error) {
	query := `
		WITH ranked AS (
			SELECT observer_id, noise_floor, battery_mv,
				ROW_NUMBER() OVER (PARTITION BY observer_id ORDER BY timestamp DESC) as rn
			FROM observer_metrics
			WHERE timestamp >= ?
		)
		SELECT m.observer_id, o.name, COALESCE(o.iata, '') as iata,
			r.noise_floor as current_nf,
			AVG(m.noise_floor) as avg_nf,
			MAX(m.noise_floor) as max_nf,
			r.battery_mv as current_batt,
			COUNT(*) as sample_count
		FROM observer_metrics m
		LEFT JOIN observers o ON o.id = m.observer_id
		LEFT JOIN ranked r ON r.observer_id = m.observer_id AND r.rn = 1
		WHERE m.timestamp >= ?
		GROUP BY m.observer_id
		ORDER BY max_nf DESC
	`
	rows, err := db.conn.Query(query, since, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MetricsSummaryRow
	for rows.Next() {
		var s MetricsSummaryRow
		if err := rows.Scan(&s.ObserverID, &s.ObserverName, &s.IATA, &s.CurrentNF, &s.AvgNF, &s.MaxNF, &s.CurrentBattMv, &s.SampleCount); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fetch sparkline data (noise_floor series) for all observers in one query
	if len(result) > 0 {
		sparkQuery := `SELECT observer_id, noise_floor FROM observer_metrics
			WHERE timestamp >= ? ORDER BY observer_id, timestamp ASC`
		sparkRows, err := db.conn.Query(sparkQuery, since)
		if err != nil {
			return nil, err
		}
		defer sparkRows.Close()

		sparkMap := make(map[string][]*float64)
		for sparkRows.Next() {
			var oid string
			var nf *float64
			if err := sparkRows.Scan(&oid, &nf); err != nil {
				return nil, err
			}
			sparkMap[oid] = append(sparkMap[oid], nf)
		}
		if err := sparkRows.Err(); err != nil {
			return nil, err
		}

		for i := range result {
			if s, ok := sparkMap[result[i].ObserverID]; ok {
				result[i].Sparkline = s
			}
		}
	}

	return result, nil
}

// PruneOldMetrics deletes observer_metrics rows older than retentionDays.
func (db *DB) PruneOldMetrics(retentionDays int) (int64, error) {
	rw, err := openRW(db.path)
	if err != nil {
		return 0, err
	}
	defer rw.Close()

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	res, err := rw.Exec(`DELETE FROM observer_metrics WHERE timestamp < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("[metrics] Pruned %d observer_metrics rows older than %d days", n, retentionDays)
	}
	return n, nil
}

// RemoveStaleObservers marks observers that have not actively sent data in observerDays
// as inactive (soft-delete). This preserves JOIN integrity for observations.observer_idx
// and observer_metrics.observer_id — historical data still references the correct observer.
// An observer must actively send data to stay listed — being seen by another node does not count.
// observerDays <= -1 means never remove (keep forever).
func (db *DB) RemoveStaleObservers(observerDays int) (int64, error) {
	if observerDays <= -1 {
		return 0, nil // keep forever
	}
	rw, err := openRW(db.path)
	if err != nil {
		return 0, err
	}
	defer rw.Close()

	cutoff := time.Now().UTC().AddDate(0, 0, -observerDays).Format(time.RFC3339)
	res, err := rw.Exec(`UPDATE observers SET inactive = 1 WHERE last_seen < ? AND (inactive IS NULL OR inactive = 0)`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		// Clean up orphaned metrics for now-inactive observers
		rw.Exec(`DELETE FROM observer_metrics WHERE observer_id IN (SELECT id FROM observers WHERE inactive = 1)`)
		log.Printf("[observers] Marked %d observer(s) as inactive (not seen in %d days)", n, observerDays)
	}
	return n, nil
}

// TouchNodeLastSeen updates last_seen for a node identified by full public key.
// Only updates if the new timestamp is newer than the existing value (or NULL).
// Returns nil even if no rows are affected (node doesn't exist).
func (db *DB) TouchNodeLastSeen(pubkey string, timestamp string) error {
	_, err := db.conn.Exec(
		"UPDATE nodes SET last_seen = ? WHERE public_key = ? AND (last_seen IS NULL OR last_seen < ?)",
		timestamp, pubkey, timestamp,
	)
	return err
}

// GetDroppedPackets returns recently dropped packets, newest first.
func (db *DB) GetDroppedPackets(limit int, observerID, nodePubkey string) ([]map[string]interface{}, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, hash, raw_hex, reason, observer_id, observer_name, node_pubkey, node_name, dropped_at FROM dropped_packets`
	var conditions []string
	var args []interface{}
	if observerID != "" {
		conditions = append(conditions, "observer_id = ?")
		args = append(args, observerID)
	}
	if nodePubkey != "" {
		conditions = append(conditions, "node_pubkey = ?")
		args = append(args, nodePubkey)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY dropped_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var id int
		var hash, rawHex, reason, obsID, obsName, pubkey, name, droppedAt sql.NullString
		if err := rows.Scan(&id, &hash, &rawHex, &reason, &obsID, &obsName, &pubkey, &name, &droppedAt); err != nil {
			continue
		}
		row := map[string]interface{}{
			"id":            id,
			"hash":          nullStr(hash),
			"reason":        nullStr(reason),
			"observer_id":   nullStr(obsID),
			"observer_name": nullStr(obsName),
			"node_pubkey":   nullStr(pubkey),
			"node_name":     nullStr(name),
			"dropped_at":    nullStr(droppedAt),
		}
		// Only include raw_hex if explicitly requested (it's large)
		if rawHex.Valid {
			row["raw_hex"] = rawHex.String
		}
		results = append(results, row)
	}
	if results == nil {
		results = []map[string]interface{}{}
	}
	return results, nil
}

// GetSignatureDropCount returns the total number of dropped packets.
func (db *DB) GetSignatureDropCount() int64 {
	var count int64
	// Table may not exist yet if ingestor hasn't run the migration
	err := db.conn.QueryRow("SELECT COUNT(*) FROM dropped_packets").Scan(&count)
	if err != nil {
		return 0
	}
	return count
}

func (db *DB) GetScopeStats(window string) (*ScopeStatsResponse, error) {
	if !db.hasScopeName {
		return nil, fmt.Errorf("scope_name column not present — run ingestor to apply migrations")
	}

	var since string
	var bucketExpr string
	switch window {
	case "1h":
		since = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		// 5-minute buckets
		bucketExpr = `strftime('%Y-%m-%dT%H:', first_seen) || printf('%02d', (CAST(strftime('%M', first_seen) AS INTEGER) / 5) * 5) || ':00Z'`
	case "7d":
		since = time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
		// 6-hour buckets
		bucketExpr = `strftime('%Y-%m-%dT', first_seen) || printf('%02d', (CAST(strftime('%H', first_seen) AS INTEGER) / 6) * 6) || ':00:00Z'`
	default: // "24h"
		window = "24h"
		since = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
		// 1-hour buckets
		bucketExpr = `strftime('%Y-%m-%dT%H:00:00Z', first_seen)`
	}

	resp := &ScopeStatsResponse{Window: window}

	// Summary counts
	row := db.conn.QueryRow(`
		SELECT
			COUNT(*) AS transport_total,
			COUNT(scope_name) AS scoped,
			SUM(CASE WHEN scope_name IS NULL THEN 1 ELSE 0 END) AS unscoped,
			SUM(CASE WHEN scope_name = '' THEN 1 ELSE 0 END) AS unknown_scope
		FROM transmissions
		WHERE route_type IN (0, 3) AND first_seen >= ?
	`, since)
	if err := row.Scan(
		&resp.Summary.TransportTotal,
		&resp.Summary.Scoped,
		&resp.Summary.Unscoped,
		&resp.Summary.UnknownScope,
	); err != nil {
		return nil, fmt.Errorf("scope summary query: %w", err)
	}

	// Per-region counts (named regions only)
	rows, err := db.conn.Query(`
		SELECT scope_name, COUNT(*) AS cnt
		FROM transmissions
		WHERE route_type IN (0, 3) AND scope_name IS NOT NULL AND scope_name != '' AND first_seen >= ?
		GROUP BY scope_name
		ORDER BY cnt DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("scope byRegion query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rc ScopeRegionCount
		if rows.Scan(&rc.Name, &rc.Count) == nil {
			resp.ByRegion = append(resp.ByRegion, rc)
		}
	}
	if resp.ByRegion == nil {
		resp.ByRegion = []ScopeRegionCount{}
	}

	// Time series
	tsQuery := fmt.Sprintf(`
		SELECT %s AS bucket,
			COUNT(scope_name) AS scoped,
			SUM(CASE WHEN scope_name IS NULL THEN 1 ELSE 0 END) AS unscoped
		FROM transmissions
		WHERE route_type IN (0, 3) AND first_seen >= ?
		GROUP BY bucket
		ORDER BY bucket
	`, bucketExpr)
	tsRows, err := db.conn.Query(tsQuery, since)
	if err != nil {
		return nil, fmt.Errorf("scope timeseries query: %w", err)
	}
	defer tsRows.Close()
	for tsRows.Next() {
		var pt ScopeTimePoint
		if tsRows.Scan(&pt.T, &pt.Scoped, &pt.Unscoped) == nil {
			resp.TimeSeries = append(resp.TimeSeries, pt)
		}
	}
	if resp.TimeSeries == nil {
		resp.TimeSeries = []ScopeTimePoint{}
	}

	return resp, nil
}
