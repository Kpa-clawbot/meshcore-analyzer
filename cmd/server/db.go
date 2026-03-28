package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a read-only connection to the MeshCore SQLite database.
type DB struct {
	conn *sql.DB
	path string // filesystem path to the database file
	isV3 bool   // v3 schema: observer_idx in observations (vs observer_id in v2)
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
		if rows.Scan(&cid, &colName, &colType, &notNull, &dflt, &pk) == nil && colName == "observer_idx" {
			db.isV3 = true
			return
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
	PublicKey   string   `json:"public_key"`
	Name       *string  `json:"name"`
	Role       *string  `json:"role"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	LastSeen   *string  `json:"last_seen"`
	FirstSeen  *string  `json:"first_seen"`
	AdvertCount int     `json:"advert_count"`
}

// Observer represents a row from the observers table.
type Observer struct {
	ID            string  `json:"id"`
	Name          *string `json:"name"`
	IATA          *string `json:"iata"`
	LastSeen      *string `json:"last_seen"`
	FirstSeen     *string `json:"first_seen"`
	PacketCount   int     `json:"packet_count"`
	Model         *string `json:"model"`
	Firmware      *string `json:"firmware"`
	ClientVersion *string `json:"client_version"`
	Radio         *string `json:"radio"`
	BatteryMv     *int    `json:"battery_mv"`
	UptimeSecs    *int    `json:"uptime_secs"`
	NoiseFloor    *int    `json:"noise_floor"`
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

// Observation (from packets_v view).
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
	Order    string // ASC or DESC
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

	// Count total transmissions (fast — queries transmissions directly, not packets_v)
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
	if q.Observer != "" {
		if db.isV3 {
			where = append(where, "EXISTS (SELECT 1 FROM observations oi JOIN observers obi ON obi.rowid = oi.observer_idx WHERE oi.transmission_id = t.id AND obi.id = ?)")
		} else {
			where = append(where, "EXISTS (SELECT 1 FROM observations oi WHERE oi.transmission_id = t.id AND oi.observer_id = ?)")
		}
		args = append(args, q.Observer)
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

// GetPacketByID fetches a single packet/observation.
func (db *DB) GetPacketByID(id int) (map[string]interface{}, error) {
	rows, err := db.conn.Query("SELECT id, raw_hex, timestamp, observer_id, observer_name, direction, snr, rssi, score, hash, route_type, payload_type, payload_version, path_json, decoded_json, created_at FROM packets_v WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanPacketRow(rows), nil
	}
	return nil, nil
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

// GetObservationsForHash returns all observations for a given hash.
func (db *DB) GetObservationsForHash(hash string) ([]map[string]interface{}, error) {
	rows, err := db.conn.Query(`SELECT id, raw_hex, timestamp, observer_id, observer_name, direction, snr, rssi, score, hash, route_type, payload_type, payload_version, path_json, decoded_json, created_at
		FROM packets_v WHERE hash = ? ORDER BY timestamp DESC`, strings.ToLower(hash))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]map[string]interface{}, 0)
	for rows.Next() {
		p := scanPacketRow(rows)
		if p != nil {
			result = append(result, p)
		}
	}
	return result, nil
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

	querySQL := fmt.Sprintf("SELECT public_key, name, role, lat, lon, last_seen, first_seen, advert_count FROM nodes %s ORDER BY %s LIMIT ? OFFSET ?", w, order)
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
	rows, err := db.conn.Query(`SELECT public_key, name, role, lat, lon, last_seen, first_seen, advert_count
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
	rows, err := db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen, first_seen, advert_count FROM nodes WHERE public_key = ?", pubkey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanNodeRow(rows), nil
	}
	return nil, nil
}

// GetRecentPacketsForNode returns recent packets referencing a node.
func (db *DB) GetRecentPacketsForNode(pubkey string, name string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 20
	}
	pk := "%" + pubkey + "%"
	np := "%" + name + "%"
	rows, err := db.conn.Query(`SELECT id, raw_hex, timestamp, observer_id, observer_name, direction, snr, rssi, score, hash, route_type, payload_type, payload_version, path_json, decoded_json, created_at
		FROM packets_v WHERE decoded_json LIKE ? OR decoded_json LIKE ?
		ORDER BY timestamp DESC LIMIT ?`, pk, np, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	packets := make([]map[string]interface{}, 0)
	for rows.Next() {
		p := scanPacketRow(rows)
		if p != nil {
			packets = append(packets, p)
		}
	}
	return packets, nil
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
		if err := rows.Scan(&o.ID, &o.Name, &o.IATA, &o.LastSeen, &o.FirstSeen, &o.PacketCount, &o.Model, &o.Firmware, &o.ClientVersion, &o.Radio, &o.BatteryMv, &o.UptimeSecs, &o.NoiseFloor); err != nil {
			continue
		}
		observers = append(observers, o)
	}
	return observers, nil
}

// GetObserverByID returns a single observer.
func (db *DB) GetObserverByID(id string) (*Observer, error) {
	var o Observer
	err := db.conn.QueryRow("SELECT id, name, iata, last_seen, first_seen, packet_count, model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor FROM observers WHERE id = ?", id).
		Scan(&o.ID, &o.Name, &o.IATA, &o.LastSeen, &o.FirstSeen, &o.PacketCount, &o.Model, &o.Firmware, &o.ClientVersion, &o.Radio, &o.BatteryMv, &o.UptimeSecs, &o.NoiseFloor)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// GetObserverIdsForRegion returns observer IDs for given IATA codes.
func (db *DB) GetObserverIdsForRegion(regionParam string) ([]string, error) {
	if regionParam == "" {
		return nil, nil
	}
	codes := strings.Split(regionParam, ",")
	placeholders := make([]string, len(codes))
	args := make([]interface{}, len(codes))
	for i, c := range codes {
		placeholders[i] = "?"
		args[i] = strings.TrimSpace(c)
	}
	rows, err := db.conn.Query(fmt.Sprintf("SELECT id FROM observers WHERE iata IN (%s)", strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, nil
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

// GetNodeHealth returns health info for a node (observers, stats, recent packets).
func (db *DB) GetNodeHealth(pubkey string) (map[string]interface{}, error) {
	node, err := db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		return nil, err
	}

	name := ""
	if n, ok := node["name"]; ok && n != nil {
		name = fmt.Sprintf("%v", n)
	}

	pk := "%" + pubkey + "%"
	np := "%" + name + "%"
	whereClause := "decoded_json LIKE ? OR decoded_json LIKE ?"
	if name == "" {
		whereClause = "decoded_json LIKE ?"
		np = pk
	}

	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)

	// Observers
	observerSQL := fmt.Sprintf(`SELECT observer_id, observer_name, AVG(snr) as avgSnr, AVG(rssi) as avgRssi, COUNT(*) as packetCount
		FROM packets_v WHERE (%s) AND observer_id IS NOT NULL GROUP BY observer_id ORDER BY packetCount DESC`, whereClause)
	oRows, err := db.conn.Query(observerSQL, pk, np)
	if err != nil {
		return nil, err
	}
	defer oRows.Close()

	observers := make([]map[string]interface{}, 0)
	for oRows.Next() {
		var obsID, obsName sql.NullString
		var avgSnr, avgRssi sql.NullFloat64
		var pktCount int
		oRows.Scan(&obsID, &obsName, &avgSnr, &avgRssi, &pktCount)
		observers = append(observers, map[string]interface{}{
			"observer_id":   nullStr(obsID),
			"observer_name": nullStr(obsName),
			"avgSnr":        nullFloat(avgSnr),
			"avgRssi":       nullFloat(avgRssi),
			"packetCount":   pktCount,
		})
	}

	// Stats
	var packetsToday, totalPackets int
	db.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM packets_v WHERE (%s) AND timestamp > ?", whereClause), pk, np, todayStart).Scan(&packetsToday)
	db.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM packets_v WHERE (%s)", whereClause), pk, np).Scan(&totalPackets)

	var avgSnr sql.NullFloat64
	db.conn.QueryRow(fmt.Sprintf("SELECT AVG(snr) FROM packets_v WHERE (%s)", whereClause), pk, np).Scan(&avgSnr)

	var lastHeard sql.NullString
	db.conn.QueryRow(fmt.Sprintf("SELECT MAX(timestamp) FROM packets_v WHERE (%s)", whereClause), pk, np).Scan(&lastHeard)

	// Avg hops
	hRows, _ := db.conn.Query(fmt.Sprintf("SELECT path_json FROM packets_v WHERE (%s) AND path_json IS NOT NULL", whereClause), pk, np)
	totalHops, hopCount := 0, 0
	if hRows != nil {
		defer hRows.Close()
		for hRows.Next() {
			var pj sql.NullString
			hRows.Scan(&pj)
			if pj.Valid {
				var hops []interface{}
				if json.Unmarshal([]byte(pj.String), &hops) == nil {
					totalHops += len(hops)
					hopCount++
				}
			}
		}
	}
	avgHops := 0
	if hopCount > 0 {
		avgHops = int(math.Round(float64(totalHops) / float64(hopCount)))
	}

	// Recent packets
	recentPackets, _ := db.GetRecentTransmissionsForNode(pubkey, name, 20)

	return map[string]interface{}{
		"node":      node,
		"observers": observers,
		"stats": map[string]interface{}{
			"totalTransmissions": totalPackets,
			"totalObservations":  totalPackets,
			"totalPackets":       totalPackets,
			"packetsToday":       packetsToday,
			"avgSnr":             nullFloat(avgSnr),
			"avgHops":            avgHops,
			"lastHeard":          nullStr(lastHeard),
		},
		"recentPackets": recentPackets,
	}, nil
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

// GetTraces returns observations for a hash.
func (db *DB) GetTraces(hash string) ([]map[string]interface{}, error) {
	rows, err := db.conn.Query(`SELECT observer_id, observer_name, timestamp, snr, rssi, path_json
		FROM packets_v WHERE hash = ? ORDER BY timestamp ASC`, strings.ToLower(hash))
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
// Queries transmissions directly (not packets_v) to avoid observation-level
// duplicates that could cause stale lastMessage when an older message has
// a later re-observation timestamp.
func (db *DB) GetChannels() ([]map[string]interface{}, error) {
	rows, err := db.conn.Query(`SELECT decoded_json, first_seen FROM transmissions WHERE payload_type = 5 ORDER BY first_seen ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channelMap := map[string]map[string]interface{}{}
	for rows.Next() {
		var dj, fs sql.NullString
		rows.Scan(&dj, &fs)
		if !dj.Valid {
			continue
		}
		var decoded map[string]interface{}
		if json.Unmarshal([]byte(dj.String), &decoded) != nil {
			continue
		}
		dtype, _ := decoded["type"].(string)
		if dtype != "CHAN" {
			continue
		}
		// Filter out garbage-decrypted channel names/messages (pre-#197 data still in DB)
		chanStr, _ := decoded["channel"].(string)
		textStr, _ := decoded["text"].(string)
		if hasGarbageChars(chanStr) || hasGarbageChars(textStr) {
			continue
		}
		channelName, _ := decoded["channel"].(string)
		if channelName == "" {
			channelName = "unknown"
		}
		key := channelName

		ch, exists := channelMap[key]
		if !exists {
			ch = map[string]interface{}{
				"hash": key, "name": channelName,
				"lastMessage": nil, "lastSender": nil,
				"messageCount": 0, "lastActivity": nullStr(fs),
			}
			channelMap[key] = ch
		}
		ch["messageCount"] = ch["messageCount"].(int) + 1
		if fs.Valid {
			ch["lastActivity"] = fs.String
		}
		if text, ok := decoded["text"].(string); ok && text != "" {
			idx := strings.Index(text, ": ")
			if idx > 0 {
				ch["lastMessage"] = text[idx+2:]
			} else {
				ch["lastMessage"] = text
			}
			if sender, ok := decoded["sender"].(string); ok {
				ch["lastSender"] = sender
			}
		}
	}

	channels := make([]map[string]interface{}, 0, len(channelMap))
	for _, ch := range channelMap {
		channels = append(channels, ch)
	}
	return channels, nil
}

// GetChannelMessages returns messages for a specific channel.
// Uses transmission-level ordering (first_seen) to ensure correct message
// sequence even when observations arrive out of order.
func (db *DB) GetChannelMessages(channelHash string, limit, offset int) ([]map[string]interface{}, int, error) {
	if limit <= 0 {
		limit = 100
	}

	var querySQL string
	if db.isV3 {
		querySQL = `SELECT o.id, t.hash, t.decoded_json, t.first_seen,
				obs.id, obs.name, o.snr, o.path_json
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE t.payload_type = 5
			ORDER BY t.first_seen ASC`
	} else {
		querySQL = `SELECT o.id, t.hash, t.decoded_json, t.first_seen,
				o.observer_id, o.observer_name, o.snr, o.path_json
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			WHERE t.payload_type = 5
			ORDER BY t.first_seen ASC`
	}

	rows, err := db.conn.Query(querySQL)
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
		dtype, _ := decoded["type"].(string)
		if dtype != "CHAN" {
			continue
		}
		ch, _ := decoded["channel"].(string)
		if ch == "" {
			ch = "unknown"
		}
		if ch != channelHash {
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

	total := len(msgOrder)
	// Return latest messages (tail)
	start := total - limit - offset
	if start < 0 {
		start = 0
	}
	end := total - offset
	if end < 0 {
		end = 0
	}
	if end > total {
		end = total
	}

	messages := make([]map[string]interface{}, 0)
	for i := start; i < end; i++ {
		key := msgOrder[i]
		m := msgMap[key]
		m.Data["repeats"] = m.Repeats
		messages = append(messages, m.Data)
	}

	return messages, total, nil
}

// GetTimestamps returns packet timestamps since a given time.
func (db *DB) GetTimestamps(since string) ([]string, error) {
	rows, err := db.conn.Query("SELECT timestamp FROM packets_v WHERE timestamp > ? ORDER BY timestamp ASC", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var timestamps []string
	for rows.Next() {
		var ts string
		rows.Scan(&ts)
		timestamps = append(timestamps, ts)
	}
	if timestamps == nil {
		timestamps = []string{}
	}
	return timestamps, nil
}

// GetNodeCountsForPacket returns observation count for a hash.
func (db *DB) GetObservationCount(hash string) int {
	var count int
	db.conn.QueryRow("SELECT COUNT(*) FROM packets_v WHERE hash = ?", strings.ToLower(hash)).Scan(&count)
	return count
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

	if err := rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen, &firstSeen, &advertCount); err != nil {
		return nil
	}
	return map[string]interface{}{
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
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=10000", db.path)
	rw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, err
	}
	rw.SetMaxOpenConns(1)
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
