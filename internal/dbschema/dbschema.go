// Package dbschema centralizes schema migrations and read-side schema
// assertions for the CoreScope SQLite DB. Per issue #1287 the writer
// (cmd/ingestor) owns ALL CREATE/ALTER/INSERT/UPDATE/DELETE on schema
// objects; the server (cmd/server) only ASSERTS that the schema is in
// the expected shape and refuses to start otherwise.
//
// Apply(rw, log) runs from the ingestor at startup BEFORE subscribing to
// MQTT. AssertReady(ro) runs from the server at startup and returns an
// error listing every missing column/index/table.
//
// INVARIANT (#1321): Any optional column the SERVER detects via PRAGMA
// (e.g. cmd/server/db.go detectSchema → hasScopeName, hasDefaultScope,
// hasObsRawHex, hasResolvedPath) MUST be added here, in Apply, AND
// asserted in AssertReady. Adding it only to cmd/ingestor/db.go
// applySchema reintroduces the startup-race bug from #1321: the
// server's PRAGMA can run before the ingestor finishes applySchema,
// the boolean caches `false`, and feature endpoints permanently 500
// until the server restarts. Source of truth lives here — full stop.
package dbschema

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Logger is the minimal logging surface used by Apply. Both cmd/server
// and cmd/ingestor satisfy this with the stdlib `log` package's Printf
// (passed as a closure to avoid an indirect log dependency here).
type Logger func(format string, args ...interface{})

// Apply runs every server-side ensure_* migration against the given
// read-write SQLite connection. Each operation is idempotent
// (IF NOT EXISTS / column-probe-before-ALTER). Safe to call repeatedly.
//
// Called by the ingestor at startup. The server MUST NOT call this —
// it only calls AssertReady.
func Apply(rw *sql.DB, logf Logger) error {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	if err := ensureServerIndexes(rw); err != nil {
		return fmt.Errorf("ensure server indexes: %w", err)
	}
	if err := ensureNeighborEdgesTable(rw); err != nil {
		return fmt.Errorf("ensure neighbor_edges: %w", err)
	}
	if err := ensureInactiveNodesTable(rw); err != nil {
		return fmt.Errorf("ensure inactive_nodes: %w", err)
	}
	if err := ensureResolvedPathColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure resolved_path: %w", err)
	}
	if err := ensureObserverInactiveColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure observers.inactive: %w", err)
	}
	if err := ensureLastPacketAtColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure observers.last_packet_at: %w", err)
	}
	if err := ensureObserverIATAColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure observers.iata: %w", err)
	}
	// Must run after ensureObserverIATAColumn: the expression index below
	// is on observers.iata, which that step guarantees exists.
	if err := ensureChannelIndexes(rw); err != nil {
		return fmt.Errorf("ensure channel indexes: %w", err)
	}
	if err := ensureForeignAdvertColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure foreign_advert: %w", err)
	}
	if err := ensureFromPubkeyColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure from_pubkey: %w", err)
	}
	if err := ensureScopeNameColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure scope_name: %w", err)
	}
	if err := ensureDefaultScopeColumns(rw, logf); err != nil {
		return fmt.Errorf("ensure default_scope: %w", err)
	}
	if err := ensureConfiguredScopeColumns(rw, logf); err != nil {
		return fmt.Errorf("ensure configured_scope: %w", err)
	}
	if err := ensureObservationsRawHexColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure observations.raw_hex: %w", err)
	}
	if err := ensureMultibyteCapColumns(rw, logf); err != nil {
		return fmt.Errorf("ensure multibyte_cap columns: %w", err)
	}
	if err := ensureObserverNaiveClockColumns(rw, logf); err != nil {
		return fmt.Errorf("ensure observers naive-clock columns: %w", err)
	}
	if err := ensureObserverCanRelayColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure observers.can_relay: %w", err)
	}
	if err := ensureObserverCanRelaySeenColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure observers.can_relay_seen: %w", err)
	}
	// #1690: denormalized last_seen on transmissions so cold-load filters
	// on effective recency rather than first-ever first_seen. The column
	// add + index creation are cheap (single ALTER, indexed INTEGER
	// column); the *backfill* from observations is potentially expensive
	// and runs via RunAsyncMigration from the ingestor (see
	// cmd/ingestor/db.go OpenStore).
	if err := ensureTransmissionsLastSeenColumn(rw, logf); err != nil {
		return fmt.Errorf("ensure transmissions.last_seen: %w", err)
	}
	return nil
}

// AssertReady verifies the schema is in the expected shape. The server
// calls this at startup against a read-only connection; if it returns
// non-nil, the server MUST fatal-log and exit so the operator restarts
// the ingestor (which owns migrations).
func AssertReady(ro *sql.DB) error {
	var missing []string

	mustCol := func(table, col string) {
		has, err := TableHasColumn(ro, table, col)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s.%s (probe error: %v)", table, col, err))
			return
		}
		if !has {
			missing = append(missing, fmt.Sprintf("%s.%s", table, col))
		}
	}
	mustTable := func(name string) {
		var n int
		err := ro.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			missing = append(missing, "table:"+name)
		} else if err != nil {
			missing = append(missing, fmt.Sprintf("table:%s (probe error: %v)", name, err))
		}
	}

	mustTable("neighbor_edges")
	mustCol("observations", "resolved_path")
	mustCol("observers", "inactive")
	mustCol("observers", "last_packet_at")
	mustCol("observers", "iata")
	mustCol("nodes", "foreign_advert")
	mustCol("inactive_nodes", "foreign_advert")
	mustCol("transmissions", "from_pubkey")
	// #1690: denormalized recency axis for cold-load. Owned by ingestor
	// (cmd/ingestor/db.go OpenStore: ALTER + index + async backfill);
	// server reads it to filter the hot-window query.
	mustCol("transmissions", "last_seen")
	// #1321: server's detectSchema PRAGMA-detects these and caches a
	// boolean. To kill the startup race they're owned + asserted here.
	mustCol("transmissions", "scope_name")
	mustCol("nodes", "default_scope")
	mustCol("inactive_nodes", "default_scope")
	// #1865: confirmed region scopes from the observer /neighbors report.
	mustCol("nodes", "configured_scope")
	mustCol("inactive_nodes", "configured_scope")
	mustCol("observations", "raw_hex")
	// Multi-byte capability cache (#1324 follow-up; PR #903 surface).
	// Owned by ingestor — server reads these for O(1) /api/nodes
	// enrichment, ingestor's RunMultibyteCapPersist is the only writer.
	mustCol("nodes", "multibyte_sup")
	mustCol("nodes", "multibyte_evidence")
	mustCol("inactive_nodes", "multibyte_sup")
	mustCol("inactive_nodes", "multibyte_evidence")
	// Issue #1478: per-observer naive-clock skew tracking. Server reads
	// all three to populate ObserverResp.ClockNaive / ClockSkew* fields.
	mustCol("observers", "clock_skew_seconds")
	mustCol("observers", "clock_skew_count_24h")
	mustCol("observers", "clock_last_naive_at")
	// Issue #1290: firmware 1.16 publishes a `repeat: on|off` flag in
	// the MQTT /status JSON. Ingestor persists it as can_relay; server
	// reads it to filter listener-only observers out of the path-hop
	// disambiguator candidate set. Default 1 preserves prior behavior
	// for legacy observers that never sent the field.
	mustCol("observers", "can_relay")
	// Issue #1290 follow-up (PR #1624 MAJOR-2): tri-state badge. The
	// can_relay column defaults to 1 at INSERT, so we cannot distinguish
	// "firmware confirmed repeater" from "legacy observer that never
	// sent a repeat field". can_relay_seen=1 means the ingestor wrote
	// an explicit value; can_relay_seen=0 means leave the UI badge
	// unset (unknown state).
	mustCol("observers", "can_relay_seen")

	if len(missing) > 0 {
		return fmt.Errorf("schema not migrated by ingestor; restart ingestor first. missing: %s",
			strings.Join(missing, ", "))
	}
	return nil
}

// TableHasColumn reports whether the given table has the given column.
// Exported because tests and the read-side need it without re-implementing.
func TableHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var ctype sql.NullString
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ─── ensure_* helpers (writer side) ────────────────────────────────────────

func ensureServerIndexes(rw *sql.DB) error {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_transmissions_first_seen ON transmissions(first_seen)`,
		`CREATE INDEX IF NOT EXISTS idx_transmissions_hash ON transmissions(hash)`,
		`CREATE INDEX IF NOT EXISTS idx_transmissions_payload_type ON transmissions(payload_type)`,
		`CREATE INDEX IF NOT EXISTS idx_observations_timestamp ON observations(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_observations_transmission_id ON observations(transmission_id)`,
		// Composite covers GetChannelMessages' grouped MAX(timestamp) per
		// transmission_id (issue #1366 / PR #1368). With this index sqlite can
		// satisfy the aggregate index-only without touching the heap.
		`CREATE INDEX IF NOT EXISTS idx_observations_tx_ts ON observations(transmission_id, timestamp)`,
	}
	for _, s := range stmts {
		if _, err := rw.Exec(s); err != nil {
			return fmt.Errorf("ensure index %q: %w", s, err)
		}
	}
	// observer_idx (v3) vs observer_id (v2) — probe + index the matching one.
	hasIdx, err := TableHasColumn(rw, "observations", "observer_idx")
	if err != nil {
		return err
	}
	if hasIdx {
		if _, err := rw.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_observer_idx ON observations(observer_idx)`); err != nil {
			return err
		}
	}
	hasID, err := TableHasColumn(rw, "observations", "observer_id")
	if err != nil {
		return err
	}
	if hasID {
		if _, err := rw.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_observer_id ON observations(observer_id)`); err != nil {
			return err
		}
	}
	return nil
}

// ensureChannelIndexes speeds up /api/channels and
// /api/channels/{hash}/messages.
//
//   - idx_transmissions_channel_hash_payload adds first_seen to the
//     existing channel_hash index (cmd/ingestor/db.go migration
//     'channel_hash_v1', idx_tx_channel_hash) so GetChannels' "latest
//     message" correlated subquery (channel_hash + payload_type, ORDER BY
//     first_seen DESC LIMIT 1) and GetChannelMessages' count/page queries
//     resolve as index-order scans instead of sorting per channel.
//   - idx_observers_iata_norm indexes the UPPER(TRIM(iata)) expression
//     that GetChannels/GetEncryptedChannels/GetChannelMessages use for
//     region filtering; a plain index on iata can't be used under that
//     expression, so the expression itself is indexed.
//
// transmissions.channel_hash itself is added by the ingestor's legacy
// 'channel_hash_v1' migration (cmd/ingestor/db.go), not by this package,
// so it isn't guaranteed to exist yet on every DB Apply runs against —
// probe before indexing, same pattern as the observer_idx/observer_id
// probe in ensureServerIndexes above.
func ensureChannelIndexes(rw *sql.DB) error {
	hasChannelHash, err := TableHasColumn(rw, "transmissions", "channel_hash")
	if err != nil {
		return err
	}
	if hasChannelHash {
		if _, err := rw.Exec(`CREATE INDEX IF NOT EXISTS idx_transmissions_channel_hash_payload ON transmissions(channel_hash, payload_type, first_seen)`); err != nil {
			return fmt.Errorf("ensure idx_transmissions_channel_hash_payload: %w", err)
		}
	}
	if _, err := rw.Exec(`CREATE INDEX IF NOT EXISTS idx_observers_iata_norm ON observers(UPPER(TRIM(iata)))`); err != nil {
		return fmt.Errorf("ensure idx_observers_iata_norm: %w", err)
	}
	return nil
}

func ensureNeighborEdgesTable(rw *sql.DB) error {
	_, err := rw.Exec(`CREATE TABLE IF NOT EXISTS neighbor_edges (
		node_a TEXT NOT NULL,
		node_b TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_seen TEXT,
		PRIMARY KEY (node_a, node_b)
	)`)
	return err
}

// ensureInactiveNodesTable creates the inactive_nodes table if missing.
// The ingestor's applySchema also creates this table — duplicating it
// here makes dbschema.Apply self-sufficient when called against a
// fixture DB that pre-dates the soft-delete feature (e.g. CI's
// test-fixtures/e2e-fixture.db, which never had any inactive rows).
// Schema kept in sync with cmd/ingestor/db.go:applySchema.
func ensureInactiveNodesTable(rw *sql.DB) error {
	_, err := rw.Exec(`CREATE TABLE IF NOT EXISTS inactive_nodes (
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
	)`)
	if err != nil {
		return err
	}
	_, err = rw.Exec(`CREATE INDEX IF NOT EXISTS idx_inactive_nodes_last_seen ON inactive_nodes(last_seen)`)
	return err
}

func ensureResolvedPathColumn(rw *sql.DB, logf Logger) error {
	has, err := TableHasColumn(rw, "observations", "resolved_path")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := rw.Exec("ALTER TABLE observations ADD COLUMN resolved_path TEXT"); err != nil {
		return err
	}
	logf("[dbschema] added resolved_path column to observations")
	return nil
}

func ensureObserverInactiveColumn(rw *sql.DB, logf Logger) error {
	has, err := TableHasColumn(rw, "observers", "inactive")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := rw.Exec("ALTER TABLE observers ADD COLUMN inactive INTEGER DEFAULT 0"); err != nil {
		return err
	}
	logf("[dbschema] added inactive column to observers")
	return nil
}

func ensureLastPacketAtColumn(rw *sql.DB, logf Logger) error {
	has, err := TableHasColumn(rw, "observers", "last_packet_at")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := rw.Exec("ALTER TABLE observers ADD COLUMN last_packet_at TEXT"); err != nil {
		return err
	}
	logf("[dbschema] added last_packet_at column to observers")
	return nil
}

func ensureObserverIATAColumn(rw *sql.DB, logf Logger) error {
	has, err := TableHasColumn(rw, "observers", "iata")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := rw.Exec("ALTER TABLE observers ADD COLUMN iata TEXT"); err != nil {
		return err
	}
	logf("[dbschema] added iata column to observers")
	return nil
}

func ensureForeignAdvertColumn(rw *sql.DB, logf Logger) error {
	for _, table := range []string{"nodes", "inactive_nodes"} {
		has, err := TableHasColumn(rw, table, "foreign_advert")
		if err != nil {
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		if has {
			continue
		}
		if _, err := rw.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN foreign_advert INTEGER DEFAULT 0", table)); err != nil {
			return err
		}
		logf("[dbschema] added foreign_advert column to %s", table)
	}
	return nil
}

func ensureFromPubkeyColumn(rw *sql.DB, logf Logger) error {
	has, err := TableHasColumn(rw, "transmissions", "from_pubkey")
	if err != nil {
		return err
	}
	if !has {
		if _, err := rw.Exec("ALTER TABLE transmissions ADD COLUMN from_pubkey TEXT"); err != nil {
			return err
		}
		logf("[dbschema] added from_pubkey column to transmissions (#1143)")
	}
	if _, err := rw.Exec("CREATE INDEX IF NOT EXISTS idx_transmissions_from_pubkey ON transmissions(from_pubkey)"); err != nil {
		return err
	}
	return nil
}

// ensureScopeNameColumn adds transmissions.scope_name (+ partial index) and
// records the legacy `_migrations` marker so the ingestor's old gated path
// stays idempotent on existing DBs. Moved here from cmd/ingestor/db.go in
// #1321 to be the canonical source of truth for the optional column the
// server PRAGMA-detects as hasScopeName.
func ensureScopeNameColumn(rw *sql.DB, logf Logger) error {
	if err := ensureMigrationsTable(rw); err != nil {
		return err
	}
	has, err := TableHasColumn(rw, "transmissions", "scope_name")
	if err != nil {
		return err
	}
	if !has {
		if _, err := rw.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`); err != nil {
			return fmt.Errorf("alter transmissions add scope_name: %w", err)
		}
		logf("[dbschema] added scope_name column to transmissions (#899)")
	}
	if _, err := rw.Exec(`CREATE INDEX IF NOT EXISTS idx_tx_scope_name ON transmissions(scope_name) WHERE scope_name IS NOT NULL`); err != nil {
		return fmt.Errorf("create idx_tx_scope_name: %w", err)
	}
	if _, err := rw.Exec(`INSERT OR IGNORE INTO _migrations (name) VALUES ('scope_name_v1')`); err != nil {
		return fmt.Errorf("record scope_name_v1: %w", err)
	}
	return nil
}

// ensureDefaultScopeColumns adds default_scope to nodes + inactive_nodes
// and records the `_migrations` marker. Source of truth lives here per
// #1321 (was previously cmd/ingestor/db.go only).
func ensureDefaultScopeColumns(rw *sql.DB, logf Logger) error {
	if err := ensureMigrationsTable(rw); err != nil {
		return err
	}
	for _, table := range []string{"nodes", "inactive_nodes"} {
		has, err := TableHasColumn(rw, table, "default_scope")
		if err != nil {
			return fmt.Errorf("inspect %s.default_scope: %w", table, err)
		}
		if has {
			continue
		}
		if _, err := rw.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN default_scope TEXT DEFAULT NULL`, table)); err != nil {
			return fmt.Errorf("alter %s add default_scope: %w", table, err)
		}
		logf("[dbschema] added default_scope column to %s (#899)", table)
	}
	if _, err := rw.Exec(`INSERT OR IGNORE INTO _migrations (name) VALUES ('nodes_default_scope_v1')`); err != nil {
		return fmt.Errorf("record nodes_default_scope_v1: %w", err)
	}
	return nil
}

// ensureConfiguredScopeColumns adds nodes.configured_scope +
// nodes.configured_scope_at (and mirrors on inactive_nodes) for #1865.
// Unlike default_scope (inferred from observed advert transport scope, and
// overwritten on every observation), configured_scope holds the region scopes
// a node has CONFIGURED, taken as concrete evidence from the observer
// /neighbors report — written only for the observer's own `self` scopes and
// for neighbors whose OTA scope query returned status="responded". The
// server PRAGMA-detects configured_scope as hasConfiguredScope.
func ensureConfiguredScopeColumns(rw *sql.DB, logf Logger) error {
	if err := ensureMigrationsTable(rw); err != nil {
		return err
	}
	for _, table := range []string{"nodes", "inactive_nodes"} {
		for _, col := range []string{"configured_scope", "configured_scope_at"} {
			has, err := TableHasColumn(rw, table, col)
			if err != nil {
				return fmt.Errorf("inspect %s.%s: %w", table, col, err)
			}
			if has {
				continue
			}
			if _, err := rw.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s TEXT DEFAULT NULL`, table, col)); err != nil {
				return fmt.Errorf("alter %s add %s: %w", table, col, err)
			}
			logf("[dbschema] added %s column to %s (#1865)", col, table)
		}
	}
	if _, err := rw.Exec(`INSERT OR IGNORE INTO _migrations (name) VALUES ('nodes_configured_scope_v1')`); err != nil {
		return fmt.Errorf("record nodes_configured_scope_v1: %w", err)
	}
	return nil
}

// ensureObservationsRawHexColumn adds observations.raw_hex (#881).
// Source of truth lives here per #1321 (was previously cmd/ingestor/db.go only):
// the server PRAGMA-detects this column as hasObsRawHex.
func ensureObservationsRawHexColumn(rw *sql.DB, logf Logger) error {
	if err := ensureMigrationsTable(rw); err != nil {
		return err
	}
	has, err := TableHasColumn(rw, "observations", "raw_hex")
	if err != nil {
		return err
	}
	if !has {
		if _, err := rw.Exec(`ALTER TABLE observations ADD COLUMN raw_hex TEXT`); err != nil {
			return fmt.Errorf("alter observations add raw_hex: %w", err)
		}
		logf("[dbschema] added raw_hex column to observations (#881)")
	}
	if _, err := rw.Exec(`INSERT OR IGNORE INTO _migrations (name) VALUES ('observations_raw_hex_v1')`); err != nil {
		return fmt.Errorf("record observations_raw_hex_v1: %w", err)
	}
	return nil
}

// ensureMigrationsTable is idempotent — the ingestor's applySchema also
// creates this table on legacy paths. Keeping it here lets the gated
// `INSERT OR IGNORE` markers above stay self-sufficient even when Apply
// runs against a brand-new fixture DB (e.g. dbschema_test.go).
func ensureMigrationsTable(rw *sql.DB) error {
	_, err := rw.Exec(`CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY)`)
	return err
}

// SoftDeleteBlacklistedObservers marks the given observer IDs as
// inactive=1 (case-insensitive match). Returns count affected.
// Writer-side helper; ingestor calls it at startup with the operator
// blacklist (read from config).
func SoftDeleteBlacklistedObservers(rw *sql.DB, blacklist []string) (int64, error) {
	placeholders := make([]string, 0, len(blacklist))
	args := make([]interface{}, 0, len(blacklist))
	for _, pk := range blacklist {
		t := strings.TrimSpace(pk)
		if t == "" {
			continue
		}
		placeholders = append(placeholders, "LOWER(?)")
		args = append(args, t)
	}
	if len(placeholders) == 0 {
		return 0, nil
	}
	q := "UPDATE observers SET inactive = 1 WHERE LOWER(id) IN (" +
		strings.Join(placeholders, ",") + ") AND (inactive IS NULL OR inactive = 0)"
	res, err := rw.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ensureMultibyteCapColumns adds the multi-byte capability cache columns
// to nodes / inactive_nodes (PR #903, canonical owner per #1324
// follow-up). These columns are populated by the ingestor's
// RunMultibyteCapPersist from snapshot files written by the server's
// analytics cycle; the server is read-only since #1289 and MUST NOT
// write here. The schema itself lives here in dbschema (the writer
// owns migrations, the read-only server merely AssertReady's them).
func ensureMultibyteCapColumns(rw *sql.DB, logf Logger) error {
	for _, table := range []string{"nodes", "inactive_nodes"} {
		hasSup, err := TableHasColumn(rw, table, "multibyte_sup")
		if err != nil {
			return fmt.Errorf("inspect %s.multibyte_sup: %w", table, err)
		}
		if !hasSup {
			if _, err := rw.Exec(fmt.Sprintf(
				"ALTER TABLE %s ADD COLUMN multibyte_sup INTEGER NOT NULL DEFAULT 0", table)); err != nil {
				return fmt.Errorf("add %s.multibyte_sup: %w", table, err)
			}
			logf("[dbschema] added multibyte_sup column to %s", table)
		}
		hasEvid, err := TableHasColumn(rw, table, "multibyte_evidence")
		if err != nil {
			return fmt.Errorf("inspect %s.multibyte_evidence: %w", table, err)
		}
		if !hasEvid {
			if _, err := rw.Exec(fmt.Sprintf(
				"ALTER TABLE %s ADD COLUMN multibyte_evidence TEXT", table)); err != nil {
				return fmt.Errorf("add %s.multibyte_evidence: %w", table, err)
			}
			logf("[dbschema] added multibyte_evidence column to %s", table)
		}
	}
	return nil
}

// ensureObserverNaiveClockColumns adds the three per-observer naive-clock
// skew tracking columns (#1478). Server reads them to populate the
// clock_naive / clock_skew_seconds / clock_skew_count_24h /
// clock_last_naive_at fields in /api/observers responses; ingestor writes
// them from resolveRxTime via Store.RecordNaiveSkew on each clamp event.
// Owned here per the #1321 source-of-truth invariant — the cmd/ingestor
// copy migrates real prod DBs, dbschema migrates fixture/staging DBs via
// cmd/migrate.
func ensureObserverNaiveClockColumns(rw *sql.DB, logf Logger) error {
	type col struct{ name, ddl string }
	cols := []col{
		{"clock_skew_seconds", "ALTER TABLE observers ADD COLUMN clock_skew_seconds INTEGER DEFAULT NULL"},
		{"clock_skew_count_24h", "ALTER TABLE observers ADD COLUMN clock_skew_count_24h INTEGER DEFAULT 0"},
		{"clock_last_naive_at", "ALTER TABLE observers ADD COLUMN clock_last_naive_at TEXT DEFAULT NULL"},
	}
	for _, c := range cols {
		has, err := TableHasColumn(rw, "observers", c.name)
		if err != nil {
			return fmt.Errorf("inspect observers.%s: %w", c.name, err)
		}
		if has {
			continue
		}
		if _, err := rw.Exec(c.ddl); err != nil {
			return fmt.Errorf("add observers.%s: %w", c.name, err)
		}
		logf("[dbschema] added %s column to observers", c.name)
	}
	return nil
}

// ensureObserverCanRelayColumn adds the can_relay column to observers.
// Firmware 1.16 publishes a `repeat: on|off` flag in the MQTT /status
// JSON (#1290); the ingestor parses it and writes 0/1 here. The server's
// path-hop disambiguator (cmd/server/store.go pm.resolveWithContext)
// excludes observers with can_relay=0 from the candidate set. Default 1
// preserves prior behavior for legacy observers (no repeat field).
func ensureObserverCanRelayColumn(rw *sql.DB, logf Logger) error {
	has, err := TableHasColumn(rw, "observers", "can_relay")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	// PREFLIGHT: async=true reason="single-column ALTER on observers (low-cardinality, ~1k rows in prod); DEFAULT 1 is a constant so SQLite does the rewrite as a metadata-only schema update, no row scan"
	if _, err := rw.Exec("ALTER TABLE observers ADD COLUMN can_relay INTEGER DEFAULT 1"); err != nil {
		return err
	}
	logf("[dbschema] added can_relay column to observers")
	return nil
}

// ensureObserverCanRelaySeenColumn adds the can_relay_seen tracking column.
// Issue #1290 follow-up (PR #1624 MAJOR-2): can_relay defaults to 1 at
// INSERT, which conflates "confirmed repeater" with "legacy observer
// that never sent the repeat field". can_relay_seen=1 is written by
// the ingestor whenever the firmware actually provided the field; the
// server's read layer returns CanRelay=nil whenever seen=0 so the UI
// can render the tri-state badge (no badge for unknown).
func ensureObserverCanRelaySeenColumn(rw *sql.DB, logf Logger) error {
	has, err := TableHasColumn(rw, "observers", "can_relay_seen")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	// PREFLIGHT: async=true reason="single-column ALTER on observers (low-cardinality, ~1k rows in prod); DEFAULT 0 is a constant so SQLite does the rewrite as a metadata-only schema update, no row scan"
	if _, err := rw.Exec("ALTER TABLE observers ADD COLUMN can_relay_seen INTEGER DEFAULT 0"); err != nil {
		return err
	}
	logf("[dbschema] added can_relay_seen column to observers")
	return nil
}

// ensureTransmissionsLastSeenColumn adds transmissions.last_seen (#1690),
// the denormalized "most recent observation timestamp" column the cold-load
// path filters on. Without it, hot-window queries that match
// `first_seen >= cutoff` exclude long-lived hashes whose hashes were first
// inserted weeks ago but are still active — see the issue for the full
// 0.3%-of-DB-loaded post-mortem.
//
// The backfill from `MAX(observations.timestamp) GROUP BY transmission_id`
// is potentially expensive (1.9M+ obs rows in prod) and runs as a separate
// async migration scheduled by cmd/ingestor/db.go::OpenStore via
// Store.RunAsyncMigration.
func ensureTransmissionsLastSeenColumn(rw *sql.DB, logf Logger) error {
	if err := ensureMigrationsTable(rw); err != nil {
		return err
	}
	has, err := TableHasColumn(rw, "transmissions", "last_seen")
	if err != nil {
		return err
	}
	if !has {
		// PREFLIGHT: async=true reason="single-column ALTER TABLE on transmissions; DEFAULT 0 is a constant so SQLite does a metadata-only schema rewrite (no row scan) — cheap at any scale"
		if _, err := rw.Exec(`ALTER TABLE transmissions ADD COLUMN last_seen INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("alter transmissions add last_seen: %w", err)
		}
		logf("[dbschema] added last_seen column to transmissions (#1690)")
	}
	// #1740 step (a): create the PARTIAL index first. Scans only rows with
	// last_seen=0 — i.e. the un-backfilled hot subset the chunked backfill
	// MAX(id) lookup walks. Degenerates to near-empty once the backfill
	// converges, so it stops competing for page cache.
	//
	// PREFLIGHT: async=false reason="partial index on `WHERE last_seen=0`; subset is bounded by un-backfilled rows (≤0 in steady state, ≤ingest-rate during ops) — cheap at any scale even on a 1.9M-row prod table"
	if _, err := rw.Exec(`CREATE INDEX IF NOT EXISTS idx_tx_last_seen_zero ON transmissions(id) WHERE last_seen=0`); err != nil {
		return fmt.Errorf("create idx_tx_last_seen_zero: %w", err)
	}
	// #1740 step (b): gated DROP of the legacy full index. Only runs AFTER
	// step (a) succeeded (sequential — we're past the CREATE above). The
	// DROP is a metadata-only schema rewrite in SQLite; no row scan.
	//
	// PREFLIGHT: async=false reason="DROP INDEX is metadata-only in SQLite (no row scan, no rewrite); safe inline at any DB size"
	if _, err := rw.Exec(`DROP INDEX IF EXISTS idx_tx_last_seen`); err != nil {
		return fmt.Errorf("drop legacy idx_tx_last_seen: %w", err)
	}
	return nil
}
