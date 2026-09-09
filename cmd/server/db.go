package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/geofilter"
)

// routeTypeTransport covers TRANSPORT_FLOOD (0) and TRANSPORT_DIRECT (3) —
// the only route types that carry transport_code_1 (transport-level scope).
// Per firmware/docs/packet_format.md § Route Types:
//
//	0 = TRANSPORT_FLOOD, 1 = FLOOD, 2 = DIRECT, 3 = TRANSPORT_DIRECT.
//
// Routes 1 (FLOOD) and 2 (DIRECT) never carry a scope by protocol — they are
// inherently unscoped and are counted separately in GetScopeStats (#1838).
const routeTypeTransportSQL = "route_type IN (0, 3)"

// routeTypeNonTransportSQL matches FLOOD (1) and DIRECT (2) — non-transport
// routes that carry no transport_code_1 and are therefore inherently unscoped
// per MeshCore protocol (#1838).
const routeTypeNonTransportSQL = "route_type IN (1, 2)"

// DB wraps a read-only connection to the MeshCore SQLite database.
type DB struct {
	conn                    *sql.DB
	path                    string // filesystem path to the database file
	isV3                    bool   // v3 schema: observer_idx in observations (vs observer_id in v2)
	hasResolvedPath         bool   // observations table has resolved_path column
	hasObsRawHex            bool   // observations table has raw_hex column (#881)
	hasScopeName            bool   // transmissions.scope_name column exists (#899)
	hasDefaultScope         bool   // nodes.default_scope column exists (#899)
	hasConfiguredScope      bool   // nodes.configured_scope column exists (#1865)
	hasDeclaredRegionsTable bool   // node_declared_regions table exists (#1975, optional second scope source)
	hasMultibyteSupCols     bool   // nodes/inactive_nodes have multibyte_sup/multibyte_evidence (#903)
	hasLastSeen             bool   // transmissions.last_seen column exists (#1690)

	// Channel list caches, keyed by region param — avoids repeated GROUP BY
	// scans (#762). Keyed per-region (not a single slot) so mixed-region
	// traffic doesn't evict and re-run the query on every request.
	channelsCacheMu sync.Mutex
	channelsCache   map[string]channelsCacheEntry

	encChannelsCacheMu sync.Mutex
	encChannelsCache   map[string]channelsCacheEntry

	// Channel messages cache, keyed by hash+limit+offset+region. Unlike
	// GetChannels, this previously had no cache at all — every page
	// view/poll re-ran the full paginated query.
	msgCacheMu sync.Mutex
	msgCache   map[string]channelMessagesCacheEntry

	// Prepared statements for frequently-called queries.
	// Prepared once at initDB time, reused across all requests.
	stmtCountTransmissions  *sql.Stmt
	stmtCountObservations   *sql.Stmt
	stmtCountNodesActive    *sql.Stmt // WHERE last_seen > ?
	stmtCountNodesAll       *sql.Stmt
	stmtCountObservers      *sql.Stmt
	stmtCountObsLastHour    *sql.Stmt // WHERE timestamp > ?
	stmtCountObsLastDay     *sql.Stmt // WHERE timestamp > ?
	stmtNodeLookup          *sql.Stmt // WHERE public_key = ? OR name = ?
	stmtTxByHash            *sql.Stmt // WHERE hash = ?
	stmtCountNodesByRole    *sql.Stmt // WHERE role = ? AND last_seen > ?
	stmtCountNodesByRoleAll *sql.Stmt // WHERE role = ?
	stmtMaxTxID             *sql.Stmt // COALESCE(MAX(id), 0) FROM transmissions
	stmtMaxObsID            *sql.Stmt // COALESCE(MAX(id), 0) FROM observations
}

// channelsCacheTTL is shared by the GetChannels and GetEncryptedChannels
// caches — both are cheap to keep fresh at the same cadence as before.
const channelsCacheTTL = 60 * time.Second

// msgCacheTTL is shorter than channelsCacheTTL: channel message pages are
// polled more aggressively (e.g. an open channel view) and staleness is
// more visible to users than in the channel list.
const msgCacheTTL = 10 * time.Second

// maxCacheEntries bounds a keyed cache's size. Region/pagination keys are
// low-cardinality in practice; this is a defensive reset, not a real LRU.
const maxCacheEntries = 256

type channelsCacheEntry struct {
	res []map[string]interface{}
	exp time.Time
}

type channelMessagesCacheEntry struct {
	msgs  []map[string]interface{}
	total int
	exp   time.Time
}

// OpenDB opens a read-only SQLite connection.
//
// The DSN used to pass _journal_mode=WAL and _busy_timeout=5000, which
// modernc.org/sqlite silently ignored: it understood only the
// _pragma=name(value) form. Under github.com/mattn/go-sqlite3 those parameters
// are honoured, and setting journal_mode on a read-only handle is a write — it
// would succeed only because the database is already WAL. Both are gone:
// mattn's own busy_timeout default is already 5000ms, so the read handle keeps
// the timeout (which it had silently lacked) without the pointless write.
//
// What remains is deliberate. mode=ro is the read-only invariant from
// #1283/#1289 and works because mattn's C wrapper ORs SQLITE_OPEN_URI into the
// open flags — see TestOpenDBRefusesMissingDatabase, which fails if that ever
// stops holding. _cache_size pins SQLite's C-allocated page cache at 2 MiB per
// connection; it sits outside GOMEMLIMIT, so it is bounded here rather than
// left to the driver's default (see memlimit.go).
func OpenDB(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_cache_size=-2000", path)
	conn, err := sql.Open("sqlite3", dsn)
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
	// Detect the on-disk schema on a single pinned connection and fail loudly if
	// it cannot be probed. A swallowed detection failure would silently cache the
	// wrong schema mode (isV3=false against a v3 DB) for the entire process
	// lifetime, breaking every read path until a manual restart (#1901). Aborting
	// here lets the supervisor restart us, which clears any transient cause (e.g.
	// a WAL recovery racing the read-only open).
	ctx := context.Background()
	sc, err := conn.Conn(ctx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("schema detection: acquire connection: %w", err)
	}
	derr := d.detectSchema(ctx, sc)
	_ = sc.Close()
	if derr != nil {
		conn.Close()
		return nil, fmt.Errorf("schema detection failed: %w", derr)
	}
	// Statements are prepared after schema detection so they can never be
	// compiled against a schema mode that turned out to be wrong (#1901).
	if err := d.prepareStatements(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("prepare statements: %w", err)
	}
	return d, nil
}

// stmtQueryRow returns a QueryRow-like helper that uses the prepared statement
// when non-nil (production), or falls back to a direct db.conn.QueryRow query
// when the statement is nil (test DBs that skip prepareStatements).
func (db *DB) stmtQueryRow(stmt *sql.Stmt, fallbackSQL string, args ...interface{}) *sql.Row {
	if stmt != nil {
		return stmt.QueryRow(args...)
	}
	return db.conn.QueryRow(fallbackSQL, args...)
}

// prepareStatements creates prepared statements for frequently-called queries.
// SQLite compiles and caches the query plan once, avoiding re-parsing per request.
func (db *DB) prepareStatements() error {
	var err error
	prepare := func(query string) *sql.Stmt {
		if err != nil {
			return nil
		}
		var s *sql.Stmt
		s, err = db.conn.Prepare(query)
		return s
	}

	db.stmtCountTransmissions = prepare("SELECT COUNT(*) FROM transmissions")
	db.stmtCountObservations = prepare("SELECT COUNT(*) FROM observations")
	db.stmtCountNodesActive = prepare("SELECT COUNT(*) FROM nodes WHERE last_seen > ?")
	db.stmtCountNodesAll = prepare("SELECT COUNT(*) FROM nodes")
	db.stmtCountObservers = prepare("SELECT COUNT(*) FROM observers WHERE inactive IS NULL OR inactive = 0")
	db.stmtCountObsLastHour = prepare("SELECT COUNT(*) FROM observations WHERE timestamp > ?")
	db.stmtCountObsLastDay = prepare("SELECT COUNT(*) FROM observations WHERE timestamp > ?")
	db.stmtNodeLookup = prepare("SELECT public_key FROM nodes WHERE public_key = ? OR name = ? LIMIT 1")
	db.stmtTxByHash = prepare("SELECT id FROM transmissions WHERE hash = ?")
	db.stmtCountNodesByRole = prepare("SELECT COUNT(*) FROM nodes WHERE role = ? AND last_seen > ?")
	db.stmtCountNodesByRoleAll = prepare("SELECT COUNT(*) FROM nodes WHERE role = ?")
	db.stmtMaxTxID = prepare("SELECT COALESCE(MAX(id), 0) FROM transmissions")
	db.stmtMaxObsID = prepare("SELECT COALESCE(MAX(id), 0) FROM observations")
	return err
}

func (db *DB) Close() error {
	// Close prepared statements
	closers := []*sql.Stmt{
		db.stmtCountTransmissions, db.stmtCountObservations,
		db.stmtCountNodesActive, db.stmtCountNodesAll, db.stmtCountObservers,
		db.stmtCountObsLastHour, db.stmtCountObsLastDay,
		db.stmtNodeLookup, db.stmtTxByHash,
		db.stmtCountNodesByRole, db.stmtCountNodesByRoleAll,
		db.stmtMaxTxID, db.stmtMaxObsID,
	}
	for _, s := range closers {
		if s != nil {
			s.Close()
		}
	}
	// No WAL checkpoint here. The connection is opened read-only (mode=ro in
	// OpenDB), so PRAGMA wal_checkpoint(TRUNCATE) always fails with "disk I/O
	// error (778)" on it. Attempting it emitted a misleading storage-fault line
	// on every shutdown that wasted incident investigation time (#1901); the
	// ingestor (the writer) owns checkpointing.
	return db.conn.Close()
}

// detectSchema probes the on-disk schema and sets the capability flags.
//
// It returns an error if any probe query fails. Previously these failures were
// swallowed with a bare return, leaving isV3 (and the feature flags) at their
// zero value for the whole process lifetime: a single transient failure of the
// first PRAGMA silently ran v2 SQL against a v3 database until the process was
// restarted (#1901). Detection is now all-or-nothing — on any probe error the
// caller aborts startup so the supervisor can retry.
func (db *DB) detectSchema(ctx context.Context, q rowQuerier) error {
	obs, err := schemaColumns(ctx, q, "observations")
	if err != nil {
		return fmt.Errorf("probe observations: %w", err)
	}
	db.isV3 = obs["observer_idx"]
	db.hasResolvedPath = obs["resolved_path"]
	db.hasObsRawHex = obs["raw_hex"]

	tx, err := schemaColumns(ctx, q, "transmissions")
	if err != nil {
		return fmt.Errorf("probe transmissions: %w", err)
	}
	db.hasScopeName = tx["scope_name"]
	db.hasLastSeen = tx["last_seen"]

	nodes, err := schemaColumns(ctx, q, "nodes")
	if err != nil {
		return fmt.Errorf("probe nodes: %w", err)
	}
	db.hasDefaultScope = nodes["default_scope"]
	db.hasMultibyteSupCols = nodes["multibyte_sup"]
	db.hasConfiguredScope = nodes["configured_scope"]

	// #1975: an optional second confirmed-scope source. Absent on a stock
	// install, so schemaColumns returns nothing and the flag stays false;
	// present on deployments that collect the same fact by another route.
	// A missing table is not an error here.
	ndr, ndrErr := schemaColumns(ctx, q, "node_declared_regions")
	db.hasDeclaredRegionsTable = ndrErr == nil && len(ndr) > 0

	if db.isV3 {
		log.Printf("[db] schema mode: v3 (observer_idx)")
	} else {
		log.Printf("[db] schema mode: v2 (observer_id)")
	}
	return nil
}

// rowQuerier is satisfied by both *sql.DB and *sql.Conn. detectSchema takes it
// so it can run against a single pinned connection (see OpenDB) and be
// unit-tested with an injected probe failure (#1901).
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// schemaColumns returns the set of column names present on the given table via
// PRAGMA table_info. Unlike the previous inline scans, a query or scan error is
// returned rather than swallowed (#1901). The table name is a trusted literal
// supplied by the caller, not user input.
func schemaColumns(ctx context.Context, q rowQuerier, table string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name string
		var ctype sql.NullString
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// nodeSelectCols returns the SELECT column list for nodes queries.
// When hasDefaultScope is true, default_scope is appended as the last column.
func (db *DB) nodeSelectCols() string {
	cols := "public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c, foreign_advert"
	if db.hasDefaultScope {
		cols += ", default_scope"
	}
	// #1865: confirmed scopes appended after default_scope; scan order must match.
	if db.hasConfiguredScope {
		cols += ", configured_scope, configured_scope_at"
	}
	return cols
}

// transmissionBaseSQL returns the SELECT columns and JOIN clause for transmission-centric queries.
func (db *DB) transmissionBaseSQL() (selectCols, observerJoin string) {
	if db.isV3 {
		selectCols = `t.id, t.raw_hex, t.hash, t.first_seen, t.route_type, t.payload_type, t.decoded_json,
			COALESCE((SELECT COUNT(*) FROM observations WHERE transmission_id = t.id), 0) AS observation_count,
			obs.id AS observer_id, obs.name AS observer_name, COALESCE(obs.iata, '') AS observer_iata,
			o.snr, o.rssi, o.path_json, o.direction`
		observerJoin = `LEFT JOIN observations o ON o.id = (
				SELECT id FROM observations WHERE transmission_id = t.id
				ORDER BY length(COALESCE(path_json,'')) DESC LIMIT 1
			)
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx`
	} else {
		selectCols = `t.id, t.raw_hex, t.hash, t.first_seen, t.route_type, t.payload_type, t.decoded_json,
			COALESCE((SELECT COUNT(*) FROM observations WHERE transmission_id = t.id), 0) AS observation_count,
			o.observer_id, o.observer_name, COALESCE(obs2.iata, '') AS observer_iata,
			o.snr, o.rssi, o.path_json, o.direction`
		observerJoin = `LEFT JOIN observations o ON o.id = (
				SELECT id FROM observations WHERE transmission_id = t.id
				ORDER BY length(COALESCE(path_json,'')) DESC LIMIT 1
			)
			LEFT JOIN observers obs2 ON obs2.id = o.observer_id`
	}
	if db.hasScopeName {
		selectCols += `, t.scope_name`
	}
	return
}

// scanTransmissionRow scans a row from the transmission-centric query.
// Returns a map matching the Node.js packet-store transmission shape.
func (db *DB) scanTransmissionRow(rows *sql.Rows) map[string]interface{} {
	var id, observationCount int
	var rawHex, hash, firstSeen, decodedJSON, observerID, observerName, observerIATA, pathJSON, direction sql.NullString
	var routeType, payloadType sql.NullInt64
	var snr, rssi sql.NullFloat64
	var scopeName sql.NullString

	scanArgs := []interface{}{&id, &rawHex, &hash, &firstSeen, &routeType, &payloadType, &decodedJSON,
		&observationCount, &observerID, &observerName, &observerIATA, &snr, &rssi, &pathJSON, &direction}
	if db.hasScopeName {
		scanArgs = append(scanArgs, &scopeName)
	}
	if err := rows.Scan(scanArgs...); err != nil {
		return nil
	}

	m := map[string]interface{}{
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
		"observer_iata":     nullStr(observerIATA),
		"snr":               nullFloat(snr),
		"rssi":              nullFloat(rssi),
		"path_json":         nullStr(pathJSON),
		"direction":         nullStr(direction),
	}
	if db.hasScopeName {
		m["scope_name"] = nullStr(scopeName)
	}
	return m
}

// Node represents a row from the nodes table.
type Node struct {
	PublicKey    string   `json:"public_key"`
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
	LastPacketAt  *string  `json:"last_packet_at"`
	// Issue #1478: per-observer naive-clock skew tracking.
	// Written by the ingestor in cmd/ingestor/db.go RecordNaiveSkew whenever
	// resolveRxTime clamps a naive envelope timestamp >15 min off UTC. The
	// server reads these as-is; the handler derives the bool `clock_naive`
	// from clock_last_naive_at being within the last 24h.
	ClockSkewSeconds  *int64  `json:"clock_skew_seconds"`
	ClockSkewCount24h int     `json:"clock_skew_count_24h"`
	ClockLastNaiveAt  *string `json:"clock_last_naive_at"`
	// Issue #1290: firmware 1.16 `repeat: on|off` flag persisted by the
	// ingestor. true = relay-capable, false = listener-only, nil =
	// unknown (legacy observer that never sent the field — drives the
	// tri-state UI badge so legacy rows don't masquerade as confirmed
	// repeaters). The ingestor sets can_relay_seen=1 only when it has
	// an explicit value; the read layer returns nil when seen=0.
	CanRelay *bool `json:"can_relay,omitempty"`
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
	err := db.stmtQueryRow(db.stmtCountTransmissions, "SELECT COUNT(*) FROM transmissions").Scan(&s.TotalTransmissions)
	if err != nil {
		return nil, err
	}
	s.TotalPackets = s.TotalTransmissions

	db.stmtQueryRow(db.stmtCountObservations, "SELECT COUNT(*) FROM observations").Scan(&s.TotalObservations)
	// Node.js uses 7-day active nodes for totalNodes
	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	db.stmtQueryRow(db.stmtCountNodesActive, "SELECT COUNT(*) FROM nodes WHERE last_seen > ?", sevenDaysAgo).Scan(&s.TotalNodes)
	db.stmtQueryRow(db.stmtCountNodesAll, "SELECT COUNT(*) FROM nodes").Scan(&s.TotalNodesAllTime)
	db.stmtQueryRow(db.stmtCountObservers, "SELECT COUNT(*) FROM observers WHERE inactive IS NULL OR inactive = 0").Scan(&s.TotalObservers)

	oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
	db.stmtQueryRow(db.stmtCountObsLastHour, "SELECT COUNT(*) FROM observations WHERE timestamp > ?", oneHourAgo).Scan(&s.PacketsLastHour)

	oneDayAgo := time.Now().Add(-24 * time.Hour).Unix()
	db.stmtQueryRow(db.stmtCountObsLastDay, "SELECT COUNT(*) FROM observations WHERE timestamp > ?", oneDayAgo).Scan(&s.PacketsLast24h)

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
		db.stmtQueryRow(db.stmtCountNodesByRole, "SELECT COUNT(*) FROM nodes WHERE role = ? AND last_seen > ?", role, sevenDaysAgo).Scan(&c)
		counts[role+"s"] = c
	}
	return counts
}

// GetAllRoleCounts returns count per role (all nodes, no time filter — matching Node.js /api/nodes).
func (db *DB) GetAllRoleCounts() map[string]int {
	counts := map[string]int{}
	for _, role := range []string{"repeater", "room", "companion", "sensor"} {
		var c int
		db.stmtQueryRow(db.stmtCountNodesByRoleAll, "SELECT COUNT(*) FROM nodes WHERE role = ?", role).Scan(&c)
		counts[role+"s"] = c
	}
	return counts
}

// PacketQuery holds filter params for packet listing.
type PacketQuery struct {
	Limit              int
	Offset             int
	Type               *int
	Route              *int
	Observer           string
	Hash               string
	Since              string
	Until              string
	Region             string
	Area               string // area key; filters by transmitting node's GPS position
	Node               string
	Channel            string // channel_hash filter (#812). Plain names like "#test"/"public" or "enc_<HEX>" for encrypted
	Order              string // ASC or DESC
	ExpandObservations bool   // when true, include observation sub-maps in txToMap output
}

// PacketResult wraps paginated packet list.
type PacketResult struct {
	Packets []map[string]interface{} `json:"packets"`
	Total   int                      `json:"total"`
	Limit   int                      `json:"limit"`
	Offset  int                      `json:"offset"`
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
		db.stmtQueryRow(db.stmtCountTransmissions, "SELECT COUNT(*) FROM transmissions").Scan(&total)
	} else {
		countSQL := fmt.Sprintf("SELECT COUNT(*) FROM transmissions t %s", w)
		db.conn.QueryRow(countSQL, args...).Scan(&total)
	}

	// #1345: order by ingest id, NOT first_seen. PR #1233 made first_seen=rxTime,
	// so buffered-then-uploaded observer packets with hours-old rxTime were
	// sorting to the top/middle and hiding fresh ingest. Ordering by id keeps
	// "latest activity" semantically equal to "what we ingested last" — which
	// is what the packets page is showing. The `since=` filter still uses
	// first_seen / observation timestamp, preserving "received-by-radio since X."
	selectCols, observerJoin := db.transmissionBaseSQL()
	querySQL := fmt.Sprintf("SELECT %s FROM transmissions t %s %s ORDER BY t.id %s LIMIT ? OFFSET ?",
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
		db.stmtQueryRow(db.stmtCountTransmissions, "SELECT COUNT(*) FROM transmissions").Scan(&total)
	} else {
		db.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM transmissions t %s", w), args...).Scan(&total)
	}

	// Build grouped query using transmissions table with correlated subqueries.
	// #1189 R2: distinct_iatas is a NEW column — comma-separated DISTINCT IATA
	// codes across all observers of the transmission, with empty/NULL IATAs
	// excluded. Frontend needs this on the DEFAULT COLLAPSED VIEW (where
	// p._children is empty), so we compute it server-side.
	//
	// scope_name lives on the transmission row, so appending it as the last
	// selected column is safe for both query shapes.
	scopeNameCol := ""
	if db.hasScopeName {
		scopeNameCol = ", t.scope_name"
	}
	var querySQL string
	if db.isV3 {
		querySQL = fmt.Sprintf(`SELECT t.hash, t.first_seen, t.raw_hex, t.decoded_json, t.payload_type, t.route_type,
			COALESCE((SELECT COUNT(*) FROM observations oi WHERE oi.transmission_id = t.id), 0) AS count,
			COALESCE((SELECT COUNT(DISTINCT oi.observer_idx) FROM observations oi WHERE oi.transmission_id = t.id), 0) AS observer_count,
			COALESCE((SELECT MAX(strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', oi.timestamp, 'unixepoch')) FROM observations oi WHERE oi.transmission_id = t.id), t.first_seen) AS latest,
			obs.id AS observer_id, obs.name AS observer_name, COALESCE(obs.iata, '') AS observer_iata,
			o.snr, o.rssi, o.path_json,
			COALESCE((SELECT GROUP_CONCAT(DISTINCT obi.iata) FROM observations oi JOIN observers obi ON obi.rowid = oi.observer_idx WHERE oi.transmission_id = t.id AND obi.iata IS NOT NULL AND obi.iata != ''), '') AS distinct_iatas`+scopeNameCol+`
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
			o.observer_id, o.observer_name, COALESCE(obs2.iata, '') AS observer_iata,
			o.snr, o.rssi, o.path_json,
			COALESCE((SELECT GROUP_CONCAT(DISTINCT obi.iata) FROM observations oi JOIN observers obi ON obi.id = oi.observer_id WHERE oi.transmission_id = t.id AND obi.iata IS NOT NULL AND obi.iata != ''), '') AS distinct_iatas`+scopeNameCol+`
		FROM transmissions t
		LEFT JOIN observations o ON o.id = (
			SELECT id FROM observations WHERE transmission_id = t.id
			ORDER BY length(COALESCE(path_json,'')) DESC LIMIT 1
		)
		LEFT JOIN observers obs2 ON obs2.id = o.observer_id
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
		var hash, firstSeen, rawHex, decodedJSON, latest, observerID, observerName, observerIATA, pathJSON, distinctIatasCSV sql.NullString
		var payloadType, routeType sql.NullInt64
		var count, observerCount int
		var snr, rssi sql.NullFloat64
		var scopeName sql.NullString

		scanArgs := []interface{}{&hash, &firstSeen, &rawHex, &decodedJSON, &payloadType, &routeType,
			&count, &observerCount, &latest,
			&observerID, &observerName, &observerIATA, &snr, &rssi, &pathJSON, &distinctIatasCSV}
		if db.hasScopeName {
			scanArgs = append(scanArgs, &scopeName)
		}
		if err := rows.Scan(scanArgs...); err != nil {
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
			"observer_iata":     nullStr(observerIATA),
			"distinct_iatas":    parseDistinctIatasCSV(nullStr(distinctIatasCSV)),
			"path_json":         nullStr(pathJSON),
			"payload_type":      nullInt(payloadType),
			"route_type":        nullInt(routeType),
			"raw_hex":           nullStr(rawHex),
			"decoded_json":      nullStr(decodedJSON),
			"snr":               nullFloat(snr),
			"rssi":              nullFloat(rssi),
			"scope_name":        nullStr(scopeName),
		})
	}

	return &PacketResult{Packets: packets, Total: total}, nil
}

// parseDistinctIatasCSV turns SQLite GROUP_CONCAT output ("SJC,SFO,OAK") into
// a sorted, deduped []string. Returns an empty (non-nil) slice when the input
// is empty/nil so JSON serialization stays consistent (`[]` not `null`).
func parseDistinctIatasCSV(v interface{}) []string {
	s, ok := v.(string)
	if !ok || s == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		code := strings.TrimSpace(p)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	sort.Strings(out)
	return out
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
		// #1143: exact-match on the dedicated from_pubkey column instead of
		// LIKE-on-JSON substring (adversarial spoof + same-name false positives).
		where = append(where, "from_pubkey = ?")
		args = append(args, pk)
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
		// RFC3339 since/until use an observations.timestamp subquery so that
		// re-observed packets (whose t.first_seen is older than the window
		// but which have observations inside the window) are still included.
		// Non-RFC3339 falls back to t.first_seen string compare.
		if ts, err := time.Parse(time.RFC3339Nano, q.Since); err == nil {
			where = append(where, "t.id IN (SELECT DISTINCT transmission_id FROM observations WHERE timestamp >= ?)")
			args = append(args, ts.Unix())
		} else {
			where = append(where, "t.first_seen > ?")
			args = append(args, q.Since)
		}
	}
	if q.Until != "" {
		if ts, err := time.Parse(time.RFC3339Nano, q.Until); err == nil {
			where = append(where, "t.id IN (SELECT DISTINCT transmission_id FROM observations WHERE timestamp <= ?)")
			args = append(args, ts.Unix())
		} else {
			where = append(where, "t.first_seen < ?")
			args = append(args, q.Until)
		}
	}
	if q.Node != "" {
		pk := db.resolveNodePubkey(q.Node)
		// #1143: exact-match on dedicated from_pubkey column.
		where = append(where, "t.from_pubkey = ?")
		args = append(args, pk)
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
	err := db.stmtQueryRow(db.stmtNodeLookup, "SELECT public_key FROM nodes WHERE public_key = ? OR name = ? LIMIT 1", nodeIDOrName, nodeIDOrName).Scan(&pk)
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
	err := db.stmtQueryRow(db.stmtTxByHash, "SELECT id FROM transmissions WHERE hash = ?", strings.ToLower(hash)).Scan(&txID)
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
			// #1143: from_pubkey is a dedicated, indexed column populated at
			// ingest (and backfilled) for ADVERT rows specifically so pubkey
			// lookups don't need to JSON_EXTRACT + parse decoded_json per row.
			subq := fmt.Sprintf(`public_key IN (
				SELECT DISTINCT t.from_pubkey
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

	querySQL := fmt.Sprintf("SELECT %s FROM nodes %s ORDER BY %s LIMIT ? OFFSET ?", db.nodeSelectCols(), w, order)
	qArgs := append(args, limit, offset)

	rows, err := db.conn.Query(querySQL, qArgs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer rows.Close()

	nodes := make([]map[string]interface{}, 0)
	for rows.Next() {
		n := db.scanNodeRow(rows)
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
	rows, err := db.conn.Query(fmt.Sprintf("SELECT %s FROM nodes WHERE name LIKE ? OR public_key LIKE ? ORDER BY last_seen DESC LIMIT ?", db.nodeSelectCols()),
		"%"+query+"%", query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := make([]map[string]interface{}, 0)
	for rows.Next() {
		n := db.scanNodeRow(rows)
		if n != nil {
			nodes = append(nodes, n)
		}
	}
	return nodes, nil
}

// GetNodeByPrefix resolves a hex prefix (>=8 chars) to a unique node.
// Returns (node, ambiguous, error). When multiple nodes share the prefix,
// returns (nil, true, nil). Used by the short-URL feature (issue #772).
//
// Trade-off vs an opaque ID lookup table: prefixes are stable across
// restarts, self-describing (no allocator needed), and resolve to the
// authoritative pubkey on the server. Cost: ambiguity grows with the
// node directory; we mitigate with a hard 8-hex-char (32-bit) minimum
// and surface 409 Conflict when collisions occur.
func (db *DB) GetNodeByPrefix(prefix string) (map[string]interface{}, bool, error) {
	if len(prefix) < 8 {
		return nil, false, nil
	}
	// Validate hex (avoid SQL LIKE wildcards leaking through).
	for _, c := range prefix {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return nil, false, nil
		}
	}
	rows, err := db.conn.Query(
		fmt.Sprintf("SELECT %s FROM nodes WHERE public_key LIKE ? LIMIT 2", db.nodeSelectCols()),
		prefix+"%",
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var first map[string]interface{}
	count := 0
	for rows.Next() {
		n := db.scanNodeRow(rows)
		if n == nil {
			continue
		}
		count++
		if count == 1 {
			first = n
		} else {
			return nil, true, nil
		}
	}
	if count == 0 {
		return nil, false, nil
	}
	return first, false, nil
}

// GetNodeByPubkey returns a single node.
func (db *DB) GetNodeByPubkey(pubkey string) (map[string]interface{}, error) {
	rows, err := db.conn.Query(fmt.Sprintf("SELECT %s FROM nodes WHERE public_key = ?", db.nodeSelectCols()), pubkey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return db.scanNodeRow(rows), nil
	}
	return nil, nil
}

// GetRecentTransmissionsForNode returns recent transmissions originated by a
// node, identified by exact pubkey match on the indexed from_pubkey column
// (#1143). The legacy `name` substring fallback was removed: it produced
// same-name false positives and an adversarial spoof path where any node
// could attribute its transmissions to a victim by naming itself with the
// victim's pubkey. Pubkey is unique by design — that's the whole point.
func (db *DB) GetRecentTransmissionsForNode(pubkey string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 20
	}

	selectCols, observerJoin := db.transmissionBaseSQL()

	// #1345: order by ingest id, not first_seen (=rxTime). Buffered observer
	// uploads with old rxTime would otherwise displace fresh activity from
	// the "recent transmissions for node" list.
	querySQL := fmt.Sprintf("SELECT %s FROM transmissions t %s WHERE t.from_pubkey = ? ORDER BY t.id DESC LIMIT ?",
		selectCols, observerJoin)
	args := []interface{}{pubkey, limit}

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
		querySQL = fmt.Sprintf(`SELECT o.transmission_id, o.id, obs.id AS observer_id, obs.name AS observer_name, COALESCE(obs.iata, '') AS observer_iata,
			o.direction, o.snr, o.rssi, o.path_json, strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', o.timestamp, 'unixepoch') AS obs_timestamp
			FROM observations o
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE o.transmission_id IN (%s)
			ORDER BY o.timestamp DESC`, strings.Join(placeholders, ","))
	} else {
		querySQL = fmt.Sprintf(`SELECT o.transmission_id, o.id, o.observer_id, o.observer_name, COALESCE(obs.iata, '') AS observer_iata,
			o.direction, o.snr, o.rssi, o.path_json, o.timestamp AS obs_timestamp
			FROM observations o
			LEFT JOIN observers obs ON obs.id = o.observer_id
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
		var observerID, observerName, observerIATA, direction, pathJSON, obsTimestamp sql.NullString
		var snr, rssi sql.NullFloat64

		if err := rows.Scan(&txID, &obsID, &observerID, &observerName, &observerIATA, &direction,
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
			"observer_iata":   nullStr(observerIATA),
			"snr":             nullFloat(snr),
			"rssi":            nullFloat(rssi),
			"path_json":       nullStr(pathJSON),
			"timestamp":       ts,
		}
		result[txID] = append(result[txID], obs)
	}

	return result
}

// GetObservers returns active observers (not soft-deleted) sorted by last_seen DESC.
func (db *DB) GetObservers() ([]Observer, error) {
	// Issue #1290: can_relay is read via COALESCE(can_relay, 1). The
	// column is added by internal/dbschema; older test fixtures and
	// pre-migration DBs may lack it, so we probe and fall back.
	// PR #1624 MAJOR-2: can_relay_seen is the tri-state sentinel — 1
	// means the ingestor explicitly wrote a value, 0 means "unknown"
	// and the server returns CanRelay=nil so the UI shows no badge.
	canRelayClause := "COALESCE(can_relay, 1)"
	canRelaySeenClause := "0"
	if hasCol, _ := dbschema.TableHasColumn(db.conn, "observers", "can_relay"); !hasCol {
		canRelayClause = "1"
	}
	if hasCol, _ := dbschema.TableHasColumn(db.conn, "observers", "can_relay_seen"); hasCol {
		canRelaySeenClause = "COALESCE(can_relay_seen, 0)"
	}
	rows, err := db.conn.Query(`SELECT id, name, iata, last_seen, first_seen, packet_count,
		model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor, last_packet_at,
		clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at,
		` + canRelayClause + `, ` + canRelaySeenClause + `
		FROM observers WHERE inactive IS NULL OR inactive = 0 ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var observers []Observer
	for rows.Next() {
		var o Observer
		var batteryMv, uptimeSecs, clockSkewSec sql.NullInt64
		var clockSkewCount sql.NullInt64
		var noiseFloor sql.NullFloat64
		var canRelay, canRelaySeen int
		if err := rows.Scan(&o.ID, &o.Name, &o.IATA, &o.LastSeen, &o.FirstSeen, &o.PacketCount,
			&o.Model, &o.Firmware, &o.ClientVersion, &o.Radio, &batteryMv, &uptimeSecs, &noiseFloor, &o.LastPacketAt,
			&clockSkewSec, &clockSkewCount, &o.ClockLastNaiveAt, &canRelay, &canRelaySeen); err != nil {
			continue
		}
		if canRelaySeen != 0 {
			b := canRelay != 0
			o.CanRelay = &b
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
		if clockSkewSec.Valid {
			v := clockSkewSec.Int64
			o.ClockSkewSeconds = &v
		}
		if clockSkewCount.Valid {
			o.ClockSkewCount24h = int(clockSkewCount.Int64)
		}
		observers = append(observers, o)
	}
	return observers, nil
}

// GetNonRelayObserverPubkeys returns the lowercase observer.id pubkeys
// for observers that have advertised `repeat:off` (#1290). The server's
// path-hop disambiguator consumes this to exclude listener-only nodes
// from the candidate set. Inactive observers are excluded for
// consistency with GetObservers; reactivation flips can_relay only on
// the next status message.
func (db *DB) GetNonRelayObserverPubkeys() ([]string, error) {
	// Graceful no-op when can_relay column is absent (legacy DB / older
	// test fixture). Avoids noisy schema-degradation log spam.
	if hasCol, _ := dbschema.TableHasColumn(db.conn, "observers", "can_relay"); !hasCol {
		return nil, nil
	}
	rows, err := db.conn.Query(`SELECT LOWER(id) FROM observers
		WHERE COALESCE(can_relay, 1) = 0
		  AND (inactive IS NULL OR inactive = 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err == nil && pk != "" {
			out = append(out, pk)
		}
	}
	return out, rows.Err()
}

// GetCanRelaySeenObserverPubkeys returns the lowercase observer.id
// pubkeys for which the ingestor has explicitly written a repeat-field
// value (can_relay_seen=1). PR #1624 MAJOR-2: the badge surface uses
// this to render tri-state — observers NOT in this set are "unknown"
// and the UI shows no badge.
func (db *DB) GetCanRelaySeenObserverPubkeys() ([]string, error) {
	if hasCol, _ := dbschema.TableHasColumn(db.conn, "observers", "can_relay_seen"); !hasCol {
		return nil, nil
	}
	rows, err := db.conn.Query(`SELECT LOWER(id) FROM observers
		WHERE COALESCE(can_relay_seen, 0) = 1
		  AND (inactive IS NULL OR inactive = 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err == nil && pk != "" {
			out = append(out, pk)
		}
	}
	return out, rows.Err()
}

// GetObserverByID returns a single observer.
func (db *DB) GetObserverByID(id string) (*Observer, error) {
	var o Observer
	var batteryMv, uptimeSecs, clockSkewSec sql.NullInt64
	var clockSkewCount sql.NullInt64
	var noiseFloor sql.NullFloat64
	var canRelay, canRelaySeen int
	canRelayClause := "COALESCE(can_relay, 1)"
	canRelaySeenClause := "0"
	if hasCol, _ := dbschema.TableHasColumn(db.conn, "observers", "can_relay"); !hasCol {
		canRelayClause = "1"
	}
	if hasCol, _ := dbschema.TableHasColumn(db.conn, "observers", "can_relay_seen"); hasCol {
		canRelaySeenClause = "COALESCE(can_relay_seen, 0)"
	}
	err := db.conn.QueryRow(`SELECT id, name, iata, last_seen, first_seen, packet_count,
		model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor, last_packet_at,
		clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at,
		`+canRelayClause+`, `+canRelaySeenClause+`
		FROM observers WHERE id = ?`, id).
		Scan(&o.ID, &o.Name, &o.IATA, &o.LastSeen, &o.FirstSeen, &o.PacketCount,
			&o.Model, &o.Firmware, &o.ClientVersion, &o.Radio, &batteryMv, &uptimeSecs, &noiseFloor, &o.LastPacketAt,
			&clockSkewSec, &clockSkewCount, &o.ClockLastNaiveAt, &canRelay, &canRelaySeen)
	if err != nil {
		return nil, err
	}
	if canRelaySeen != 0 {
		b := canRelay != 0
		o.CanRelay = &b
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
	if clockSkewSec.Valid {
		v := clockSkewSec.Int64
		o.ClockSkewSeconds = &v
	}
	if clockSkewCount.Valid {
		o.ClockSkewCount24h = int(clockSkewCount.Int64)
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

// normalizeRegionCodes parses a region query parameter into a list of upper-case
// IATA codes. Returns nil to signal "no filter" (match all regions).
//
// Sentinel handling (issue #770): the frontend region filter dropdown labels its
// catch-all option "All". When that option is selected the UI may send
// ?region=All; older code interpreted that literally and tried to match an
// IATA code "ALL", which never exists, returning an empty result set. Treat
// "All" / "ALL" / "all" (case-insensitive, optionally surrounded by whitespace
// or mixed with empty CSV slots) as equivalent to an empty value.
//
// Real IATA codes (e.g. "SJC", "PDX") still pass through unchanged.
func normalizeRegionCodes(regionParam string) []string {
	if regionParam == "" {
		return nil
	}
	tokens := strings.Split(regionParam, ",")
	codes := make([]string, 0, len(tokens))
	for _, token := range tokens {
		code := strings.TrimSpace(strings.ToUpper(token))
		if code == "" || code == "ALL" {
			continue
		}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return nil
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

// getChannelsCache returns the cached GetChannels result for key, if present
// and unexpired.
func (db *DB) getChannelsCache(key string) ([]map[string]interface{}, bool) {
	db.channelsCacheMu.Lock()
	defer db.channelsCacheMu.Unlock()
	e, ok := db.channelsCache[key]
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	return e.res, true
}

func (db *DB) setChannelsCache(key string, res []map[string]interface{}) {
	db.channelsCacheMu.Lock()
	defer db.channelsCacheMu.Unlock()
	if db.channelsCache == nil || len(db.channelsCache) > maxCacheEntries {
		db.channelsCache = make(map[string]channelsCacheEntry)
	}
	db.channelsCache[key] = channelsCacheEntry{res: res, exp: time.Now().Add(channelsCacheTTL)}
}

// getEncChannelsCache/setEncChannelsCache mirror getChannelsCache/
// setChannelsCache for GetEncryptedChannels, which previously had no cache.
func (db *DB) getEncChannelsCache(key string) ([]map[string]interface{}, bool) {
	db.encChannelsCacheMu.Lock()
	defer db.encChannelsCacheMu.Unlock()
	e, ok := db.encChannelsCache[key]
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	return e.res, true
}

func (db *DB) setEncChannelsCache(key string, res []map[string]interface{}) {
	db.encChannelsCacheMu.Lock()
	defer db.encChannelsCacheMu.Unlock()
	if db.encChannelsCache == nil || len(db.encChannelsCache) > maxCacheEntries {
		db.encChannelsCache = make(map[string]channelsCacheEntry)
	}
	db.encChannelsCache[key] = channelsCacheEntry{res: res, exp: time.Now().Add(channelsCacheTTL)}
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

	if cached, ok := db.getChannelsCache(regionParam); ok {
		return cached, nil
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
		// #1899: the sample_json subquery is region-scoped too, so its placeholders
		// appear FIRST in the statement (it sits in the SELECT list, ahead of the
		// WHERE). Bind the codes twice, subquery set first.
		args = append(append(make([]interface{}, 0, len(regionCodes)*2), args...), args...)
		if db.isV3 {
			querySQL = fmt.Sprintf(`SELECT t.channel_hash,
					COUNT(*) AS msg_count,
					MAX(t.first_seen) AS last_activity,
					(SELECT t2.decoded_json FROM transmissions t2
					 JOIN observations o2 ON o2.transmission_id = t2.id
					 LEFT JOIN observers obs2 ON obs2.rowid = o2.observer_idx
					 WHERE t2.channel_hash = t.channel_hash AND t2.payload_type = 5
					 AND obs2.rowid IS NOT NULL AND UPPER(TRIM(obs2.iata)) IN (%s)
					 ORDER BY t2.first_seen DESC LIMIT 1) AS sample_json
				FROM transmissions t
				JOIN observations o ON o.transmission_id = t.id
				LEFT JOIN observers obs ON obs.rowid = o.observer_idx
				WHERE t.payload_type = 5
				AND t.channel_hash IS NOT NULL
				AND t.channel_hash NOT LIKE 'enc_%%'
				AND obs.rowid IS NOT NULL AND UPPER(TRIM(obs.iata)) IN (%s)
				GROUP BY t.channel_hash
				ORDER BY last_activity DESC`, regionPlaceholder, regionPlaceholder)
		} else {
			querySQL = fmt.Sprintf(`SELECT t.channel_hash,
					COUNT(*) AS msg_count,
					MAX(t.first_seen) AS last_activity,
					(SELECT t2.decoded_json FROM transmissions t2
					 JOIN observations o2 ON o2.transmission_id = t2.id
					 WHERE t2.channel_hash = t.channel_hash AND t2.payload_type = 5
					 AND EXISTS (
						SELECT 1 FROM observers obs2
						WHERE obs2.id = o2.observer_id
						AND UPPER(TRIM(obs2.iata)) IN (%s)
					 )
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
				ORDER BY last_activity DESC`, regionPlaceholder, regionPlaceholder)
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

	db.setChannelsCache(regionParam, channels)

	return channels, nil
}

// GetEncryptedChannels returns channels where all messages are undecryptable (no key).
// Uses channel_hash column (prefixed with 'enc_') for fast grouped queries.
func (db *DB) GetEncryptedChannels(region ...string) ([]map[string]interface{}, error) {
	regionParam := ""
	if len(region) > 0 {
		regionParam = region[0]
	}

	if cached, ok := db.getEncChannelsCache(regionParam); ok {
		return cached, nil
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

	db.setEncChannelsCache(regionParam, channels)

	return channels, nil
}

// getMsgCache/setMsgCache cache GetChannelMessages results, keyed by
// hash+limit+offset+region. GetChannelMessages previously had no cache at
// all, so every page view/poll re-ran the full paginated query even though
// polling the same page repeatedly is the common case.
func (db *DB) getMsgCache(key string) ([]map[string]interface{}, int, bool) {
	db.msgCacheMu.Lock()
	defer db.msgCacheMu.Unlock()
	e, ok := db.msgCache[key]
	if !ok || time.Now().After(e.exp) {
		return nil, 0, false
	}
	return e.msgs, e.total, true
}

func (db *DB) setMsgCache(key string, msgs []map[string]interface{}, total int) {
	db.msgCacheMu.Lock()
	defer db.msgCacheMu.Unlock()
	if db.msgCache == nil || len(db.msgCache) > maxCacheEntries {
		db.msgCache = make(map[string]channelMessagesCacheEntry)
	}
	db.msgCache[key] = channelMessagesCacheEntry{msgs: msgs, total: total, exp: time.Now().Add(msgCacheTTL)}
}

// GetChannelMessages returns messages for a specific channel.
// Uses transmission-level ordering (first_seen) to ensure correct message
// sequence even when observations arrive out of order.
//
// Pagination is applied at the SQL level on the transmissions table (not on
// observations). The transmission.hash UNIQUE constraint means each
// transmission is one logical message; multiple observations of the same
// transmission collapse into one row with `repeats` = observation count.
// This avoids loading every observation row for a channel into Go memory
// before paginating (issue #1225: 5703 tx × ~50 obs ≈ 275K rows → ~30s
// for limit=50).
func (db *DB) GetChannelMessages(channelHash string, limit, offset int, region ...string) ([]map[string]interface{}, int, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	regionParam := ""
	if len(region) > 0 {
		regionParam = region[0]
	}

	cacheKey := fmt.Sprintf("%s|%d|%d|%s", channelHash, limit, offset, regionParam)
	if msgs, total, ok := db.getMsgCache(cacheKey); ok {
		return msgs, total, nil
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

	// regionFilter: a transmission is included only if at least one of its
	// observations has an observer in one of the requested regions.
	regionFilter := ""
	if len(regionCodes) > 0 {
		if db.isV3 {
			regionFilter = fmt.Sprintf(` AND EXISTS (
				SELECT 1 FROM observations o
				JOIN observers obs ON obs.rowid = o.observer_idx
				WHERE o.transmission_id = t.id
				  AND UPPER(TRIM(obs.iata)) IN (%s))`, regionPlaceholders)
		} else {
			regionFilter = fmt.Sprintf(` AND EXISTS (
				SELECT 1 FROM observations o
				JOIN observers obs ON obs.id = o.observer_id
				WHERE o.transmission_id = t.id
				  AND UPPER(TRIM(obs.iata)) IN (%s))`, regionPlaceholders)
		}
	}

	// 1) Total count (after region filter, before pagination).
	countSQL := `SELECT COUNT(*) FROM transmissions t
		WHERE t.channel_hash = ? AND t.payload_type = 5` + regionFilter
	countArgs := []interface{}{channelHash}
	countArgs = append(countArgs, regionArgs...)
	var total int
	if err := db.conn.QueryRow(countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 2) Page of transmission IDs — newest LIMIT msgs minus OFFSET.
	//    Issue #1366 follow-up (fix #2): select page by latest observation
	//    timestamp (LatestSeen) DESC, NOT by t.first_seen DESC — otherwise
	//    a heartbeat tx whose FirstSeen is 24h old but whose latest
	//    observation is fresh gets pushed off page 1.
	//
	//    PR #1368 perf fix: use a correlated subquery for MAX(timestamp) per
	//    transmission. With the composite index idx_observations_tx_ts
	//    (transmission_id, timestamp) sqlite resolves MAX as an index-only
	//    rightmost-leaf lookup — total O(N_tx · log N_obs). The previously-
	//    used grouped derived table (`GROUP BY transmission_id` over the
	//    whole observations table) scanned all observation rows (O(N_obs))
	//    and blew the 1.5s perf budget on 1500 tx × 50 obs under -race.
	//    LEFT JOIN + GROUP BY t.id was even slower because GROUP BY forced
	//    a temp B-tree on the full transmissions×observations join.
	//
	//    The returned page is in newest-LatestSeen-FIRST (DESC) order.
	//    The Go side re-orders the emitted rows ASC below (fix #3) so the
	//    contract matches the in-memory path's tail-of-msgOrder convention.
	pageSQL := `SELECT t.id,
		COALESCE((SELECT MAX(timestamp) FROM observations WHERE transmission_id = t.id), 0) AS latest_obs_epoch
		FROM transmissions t
		WHERE t.channel_hash = ? AND t.payload_type = 5
		ORDER BY latest_obs_epoch DESC, t.id DESC
		LIMIT ? OFFSET ?`
	if len(regionCodes) > 0 {
		pageSQL = `SELECT t.id,
			COALESCE((SELECT MAX(timestamp) FROM observations WHERE transmission_id = t.id), 0) AS latest_obs_epoch
			FROM transmissions t
			WHERE t.channel_hash = ? AND t.payload_type = 5` + regionFilter + `
			ORDER BY latest_obs_epoch DESC, t.id DESC
			LIMIT ? OFFSET ?`
	}
	pageArgs := []interface{}{channelHash}
	pageArgs = append(pageArgs, regionArgs...)
	pageArgs = append(pageArgs, limit, offset)

	idRows, err := db.conn.Query(pageSQL, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	pageIDs := make([]int, 0, limit)
	for idRows.Next() {
		var id int
		var le sql.NullInt64
		if err := idRows.Scan(&id, &le); err == nil {
			pageIDs = append(pageIDs, id)
		}
	}
	idRows.Close()

	if len(pageIDs) == 0 {
		empty := []map[string]interface{}{}
		db.setMsgCache(cacheKey, empty, total)
		return empty, total, nil
	}

	// 3) Fetch observations for just this page of transmissions. We keep
	//    the original "first observation wins" semantic for hops/snr/observer
	//    by ordering observations by id ASC and breaking after first per tx.
	idPlaceholders := make([]string, len(pageIDs))
	obsArgs := make([]interface{}, len(pageIDs))
	for i, id := range pageIDs {
		idPlaceholders[i] = "?"
		obsArgs[i] = id
	}
	var obsSQL string
	if db.isV3 {
		obsSQL = `SELECT o.id, t.id, t.hash, t.decoded_json, t.first_seen,
				obs.id, obs.name, o.snr, o.path_json, o.timestamp
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE t.id IN (` + strings.Join(idPlaceholders, ",") + `)
			ORDER BY o.id ASC`
	} else {
		obsSQL = `SELECT o.id, t.id, t.hash, t.decoded_json, t.first_seen,
				o.observer_id, o.observer_name, o.snr, o.path_json, o.timestamp
			FROM observations o
			JOIN transmissions t ON t.id = o.transmission_id
			WHERE t.id IN (` + strings.Join(idPlaceholders, ",") + `)
			ORDER BY o.id ASC`
	}

	rows, err := db.conn.Query(obsSQL, obsArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	type msg struct {
		Data        map[string]interface{}
		Repeats     int
		LatestEpoch int64 // max observation timestamp (unix seconds) — issue #1366
	}
	msgMap := make(map[int]*msg, len(pageIDs))

	for rows.Next() {
		var pktID, txID int
		var pktHash, dj, fs, obsID, obsName, pathJSON sql.NullString
		var snr sql.NullFloat64
		var obsTs sql.NullInt64
		rows.Scan(&pktID, &txID, &pktHash, &dj, &fs, &obsID, &obsName, &snr, &pathJSON, &obsTs)
		if !dj.Valid {
			continue
		}
		if existing, ok := msgMap[txID]; ok {
			existing.Repeats++
			if obsTs.Valid && obsTs.Int64 > existing.LatestEpoch {
				existing.LatestEpoch = obsTs.Int64
			}
			continue
		}
		var decoded map[string]interface{}
		if json.Unmarshal([]byte(dj.String), &decoded) != nil {
			continue
		}
		text, _ := decoded["text"].(string)
		sender, _ := decoded["sender"].(string)
		if sender == "" && text != "" {
			if idx := strings.Index(text, ": "); idx > 0 && idx < 50 {
				sender = text[:idx]
			}
		}
		displaySender := sender
		displayText := text
		if text != "" {
			if idx := strings.Index(text, ": "); idx > 0 && idx < 50 {
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
		senderTs := decoded["sender_timestamp"]
		m := &msg{
			Data: map[string]interface{}{
				"sender":           displaySender,
				"text":             displayText,
				"timestamp":        nullStr(fs),
				"first_seen":       nullStr(fs),
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
		if obsTs.Valid {
			m.LatestEpoch = obsTs.Int64
		}
		if obsName.Valid {
			m.Data["observers"] = []string{obsName.String}
		} else if obsID.Valid {
			m.Data["observers"] = []string{obsID.String}
		}
		msgMap[txID] = m
	}

	// Issue #1366 follow-up: emit batch sorted by LatestSeen ascending
	// (newest LAST) — matches the in-memory path's tail-of-msgOrder
	// convention and the frontend's scrollToBottom() behavior. pageIDs
	// order is not LatestSeen-ordered for in-page rows after fix #2.
	type emitted struct {
		latestEpoch int64
		txID        int
		data        map[string]interface{}
	}
	rowsOut := make([]emitted, 0, len(pageIDs))
	for _, id := range pageIDs {
		m, ok := msgMap[id]
		if !ok {
			// Transmission had no observations (shouldn't happen via normal
			// ingest) or decoded_json was NULL/invalid — skip silently to
			// preserve prior behavior.
			continue
		}
		m.Data["repeats"] = m.Repeats
		// Issue #1366: emit LatestSeen (max obs timestamp) as the rendered
		// `timestamp` field. `first_seen` stays alongside for debug.
		if m.LatestEpoch > 0 {
			m.Data["timestamp"] = time.Unix(m.LatestEpoch, 0).UTC().Format(time.RFC3339)
		}
		rowsOut = append(rowsOut, emitted{latestEpoch: m.LatestEpoch, txID: id, data: m.Data})
	}
	sort.SliceStable(rowsOut, func(i, j int) bool {
		if rowsOut[i].latestEpoch != rowsOut[j].latestEpoch {
			return rowsOut[i].latestEpoch < rowsOut[j].latestEpoch
		}
		return rowsOut[i].txID < rowsOut[j].txID
	})
	messages := make([]map[string]interface{}, 0, len(rowsOut))
	for _, e := range rowsOut {
		messages = append(messages, e.data)
	}

	db.setMsgCache(cacheKey, messages, total)

	return messages, total, nil
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
	db.stmtQueryRow(db.stmtMaxTxID, "SELECT COALESCE(MAX(id), 0) FROM transmissions").Scan(&maxID)
	return maxID
}

// GetMaxObservationID returns the current max observation ID for polling.
func (db *DB) GetMaxObservationID() int {
	var maxID int
	db.stmtQueryRow(db.stmtMaxObsID, "SELECT COALESCE(MAX(id), 0) FROM observations").Scan(&maxID)
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
	// #1481 P0-3: drop LOWER(public_key) — that wrap is non-sargable and
	// forces a full scan. Nodes are stored lowercase already; we lowercase
	// args in Go above so a plain IN matches the index on public_key.
	query := "SELECT public_key, lat, lon, role FROM nodes WHERE public_key IN (" + strings.Join(placeholders, ",") + ")"
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

	// Build IN(?, ?, ...) on the dedicated from_pubkey column (#1143):
	// exact match, indexed lookup, no JSON substring scan.
	var args []interface{}
	placeholders := make([]string, 0, len(pubkeys))
	for _, pk := range pubkeys {
		resolved := db.resolveNodePubkey(pk)
		args = append(args, resolved)
		placeholders = append(placeholders, "?")
	}
	pkWhere := "t.from_pubkey IN (" + strings.Join(placeholders, ",") + ")"

	var timeFilters []string
	if since != "" {
		timeFilters = append(timeFilters, "t.first_seen >= ?")
		args = append(args, since)
	}
	if until != "" {
		timeFilters = append(timeFilters, "t.first_seen <= ?")
		args = append(args, until)
	}

	w := "WHERE " + pkWhere
	if len(timeFilters) > 0 {
		w += " AND " + strings.Join(timeFilters, " AND ")
	}

	var total int
	db.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM transmissions t %s", w), args...).Scan(&total)

	selectCols, observerJoin := db.transmissionBaseSQL()
	// #1345: order by ingest id (see QueryPackets comment above).
	querySQL := fmt.Sprintf("SELECT %s FROM transmissions t %s %s ORDER BY t.id %s LIMIT ? OFFSET ?",
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

// scanNodeRow scans a node row. When hasDefaultScope is true the SELECT must
// include default_scope as the last column.
func (db *DB) scanNodeRow(rows *sql.Rows) map[string]interface{} {
	var pk string
	var name, role, lastSeen, firstSeen sql.NullString
	var lat, lon sql.NullFloat64
	var advertCount int
	var batteryMv sql.NullInt64
	var temperatureC sql.NullFloat64
	var foreign sql.NullInt64
	var defaultScope sql.NullString
	var configuredScope, configuredScopeAt sql.NullString

	scanArgs := []interface{}{&pk, &name, &role, &lat, &lon, &lastSeen, &firstSeen, &advertCount, &batteryMv, &temperatureC, &foreign}
	if db.hasDefaultScope {
		scanArgs = append(scanArgs, &defaultScope)
	}
	if db.hasConfiguredScope {
		scanArgs = append(scanArgs, &configuredScope, &configuredScopeAt)
	}
	if err := rows.Scan(scanArgs...); err != nil {
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
		"foreign":                foreign.Valid && foreign.Int64 != 0,
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
	if db.hasDefaultScope {
		m["default_scope"] = nullStr(defaultScope)
	}
	if db.hasConfiguredScope {
		m["configured_scope"] = nullStr(configuredScope)
		m["configured_scope_at"] = nullStr(configuredScopeAt)
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

// nullStrPtr preserves the NULL/"" distinction that nullStrVal collapses.
// transmissions.scope_name needs it: NULL means "not transport-scoped" while
// "" means "transport-scoped, region unmatched" (#899).
func nullStrPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
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

// PruneOldPackets, PruneOldMetrics, and RemoveStaleObservers were
// removed in #1283 — they are write operations and now live on the
// ingestor's *Store (cmd/ingestor/maintenance.go and cmd/ingestor/db.go).
// The server is the read path; it must not hold the SQLite write lock.

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

// (PruneOldMetrics / RemoveStaleObservers removed in #1283 — see note
// above the MetricsSample type. Ingestor owns these writes now.)

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

// GetNodePubkeysInArea returns public keys of nodes whose GPS coordinates
// fall inside the given area polygon or bounding box.
func (db *DB) GetNodePubkeysInArea(entry AreaEntry) ([]string, error) {
	rows, err := db.conn.Query("SELECT public_key, lat, lon FROM nodes WHERE lat IS NOT NULL AND lon IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	gf := &geofilter.Config{
		Polygon: entry.Polygon,
		LatMin:  entry.LatMin,
		LatMax:  entry.LatMax,
		LonMin:  entry.LonMin,
		LonMax:  entry.LonMax,
	}

	var result []string
	for rows.Next() {
		var pk string
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&pk, &lat, &lon); err != nil {
			continue
		}
		if !lat.Valid || !lon.Valid {
			continue
		}
		// Skip (0,0) — PassesFilter allows it but these nodes have no real GPS.
		if lat.Float64 == 0 && lon.Float64 == 0 {
			continue
		}
		if geofilter.PassesFilter(lat.Float64, lon.Float64, gf) {
			result = append(result, pk)
		}
	}
	return result, rows.Err()
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
			COALESCE(SUM(CASE WHEN scope_name IS NULL THEN 1 ELSE 0 END), 0) AS unscoped,
			COALESCE(SUM(CASE WHEN scope_name = '' THEN 1 ELSE 0 END), 0) AS unknown_scope
		FROM transmissions
		WHERE `+routeTypeTransportSQL+` AND first_seen >= ?
	`, since)
	if err := row.Scan(
		&resp.Summary.TransportTotal,
		&resp.Summary.Scoped,
		&resp.Summary.Unscoped,
		&resp.Summary.UnknownScope,
	); err != nil {
		return nil, fmt.Errorf("scope summary query: %w", err)
	}

	// #1838: non-transport routes (FLOOD=1, DIRECT=2) never carry
	// transport_code_1 per MeshCore protocol, so they are inherently unscoped.
	// Fold their count into Summary.Unscoped so the analytics denominator
	// reflects total-observed-transmissions rather than only transport-eligible.
	var nonTransportUnscoped int
	if err := db.conn.QueryRow(`
		SELECT COUNT(*) FROM transmissions
		WHERE `+routeTypeNonTransportSQL+` AND first_seen >= ?
	`, since).Scan(&nonTransportUnscoped); err != nil {
		return nil, fmt.Errorf("scope non-transport count query: %w", err)
	}
	resp.Summary.Unscoped += nonTransportUnscoped

	// Per-region counts (named regions only)
	rows, err := db.conn.Query(`
		SELECT scope_name, COUNT(*) AS cnt
		FROM transmissions
		WHERE `+routeTypeTransportSQL+` AND scope_name IS NOT NULL AND scope_name != '' AND first_seen >= ?
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scope byRegion iteration: %w", err)
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
		WHERE `+routeTypeTransportSQL+` AND first_seen >= ?
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
	if err := tsRows.Err(); err != nil {
		return nil, fmt.Errorf("scope timeseries iteration: %w", err)
	}
	if resp.TimeSeries == nil {
		resp.TimeSeries = []ScopeTimePoint{}
	}

	return resp, nil
}

// NodeForGeoPrune holds the minimal fields needed for geo-filter pruning.
type NodeForGeoPrune struct {
	PubKey string
	Name   string
	Lat    *float64
	Lon    *float64
}

// GetNodesForGeoPrune returns all nodes with their coordinates for geo-filter evaluation.
// Read-only — safe on the server's mode=ro handle.
func (db *DB) GetNodesForGeoPrune() ([]NodeForGeoPrune, error) {
	rows, err := db.conn.Query("SELECT public_key, name, lat, lon FROM nodes ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []NodeForGeoPrune
	for rows.Next() {
		var pk string
		var name sql.NullString
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&pk, &name, &lat, &lon); err != nil {
			continue
		}
		n := NodeForGeoPrune{PubKey: pk, Name: name.String}
		if lat.Valid {
			v := lat.Float64
			n.Lat = &v
		}
		if lon.Valid {
			v := lon.Float64
			n.Lon = &v
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// DeleteNodesByPubkeys was removed in PR #738 follow-up: server is read-only
// (opened with mode=ro after #1283/#1289), so DELETE statements would fail at
// runtime. Geo-prune now flows server → marker file → ingestor; see
// internal/prunequeue and cmd/ingestor/prune_geofilter.go.
