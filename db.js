const Database = require('better-sqlite3');
const path = require('path');
const fs = require('fs');

// Ensure data directory exists
const dbPath = process.env.DB_PATH || path.join(__dirname, 'data', 'meshcore.db');
const dataDir = path.dirname(dbPath);
if (!fs.existsSync(dataDir)) fs.mkdirSync(dataDir, { recursive: true });

const db = new Database(dbPath);
db.pragma('journal_mode = WAL');
db.pragma('foreign_keys = ON');
db.pragma('wal_autocheckpoint = 0'); // Disable auto-checkpoint — manual checkpoint on timer to avoid random event loop spikes

// --- Migration: drop legacy tables (replaced by transmissions + observations in v2.3.0) ---
// Drop paths first (has FK to packets)
const legacyTables = ['paths', 'packets'];
for (const t of legacyTables) {
  const exists = db.prepare(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`).get(t);
  if (exists) {
    console.log(`[migration] Dropping legacy table: ${t}`);
    db.exec(`DROP TABLE IF EXISTS ${t}`);
  }
}

// --- Schema ---
db.exec(`
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
    noise_floor INTEGER
  );

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

  CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen);
  CREATE INDEX IF NOT EXISTS idx_observers_last_seen ON observers(last_seen);
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
`);

// --- Determine schema version ---
let schemaVersion = db.pragma('user_version', { simple: true }) || 0;

// Migrate from old schema_version table to pragma user_version
if (schemaVersion === 0) {
  try {
    const row = db.prepare('SELECT version FROM schema_version ORDER BY version DESC LIMIT 1').get();
    if (row && row.version >= 3) {
      db.pragma(`user_version = ${row.version}`);
      schemaVersion = row.version;
      db.exec('DROP TABLE IF EXISTS schema_version');
    }
  } catch {}
}

// Detect v3 schema by column presence (handles crash between migration and version write)
if (schemaVersion === 0) {
  try {
    const cols = db.pragma('table_info(observations)').map(c => c.name);
    if (cols.includes('observer_idx') && !cols.includes('observer_id')) {
      db.pragma('user_version = 3');
      schemaVersion = 3;
      console.log('[migration-v3] Detected already-migrated schema, set user_version = 3');
    }
  } catch {}
}

// --- v3 migration: lean observations table ---
function needsV3Migration() {
  if (schemaVersion >= 3) return false;
  // Check if observations table exists with old observer_id TEXT column
  const obsExists = db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='observations'").get();
  if (!obsExists) return false;
  const cols = db.pragma('table_info(observations)').map(c => c.name);
  return cols.includes('observer_id');
}

function runV3Migration() {
  const startTime = Date.now();
  console.log('[migration-v3] Starting observations table optimization...');

  // a. Backup DB
  const backupPath = dbPath + `.pre-v3-backup-${Date.now()}`;
  try {
    console.log(`[migration-v3] Backing up DB to ${backupPath}...`);
    fs.copyFileSync(dbPath, backupPath);
    console.log(`[migration-v3] Backup complete (${Date.now() - startTime}ms)`);
  } catch (e) {
    console.error(`[migration-v3] Backup failed, aborting migration: ${e.message}`);
    return false;
  }

  try {
    // b. Create lean table
    let stepStart = Date.now();
    db.exec(`
      CREATE TABLE observations_v3 (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
        observer_idx INTEGER,
        direction TEXT,
        snr REAL,
        rssi REAL,
        score INTEGER,
        path_json TEXT,
        timestamp INTEGER NOT NULL
      )
    `);
    console.log(`[migration-v3] Created observations_v3 table (${Date.now() - stepStart}ms)`);

    // c. Migrate data
    stepStart = Date.now();
    const result = db.prepare(`
      INSERT INTO observations_v3 (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
      SELECT o.id, o.transmission_id, obs.rowid, o.direction, o.snr, o.rssi, o.score, o.path_json,
        CAST(strftime('%s', o.timestamp) AS INTEGER)
      FROM observations o
      LEFT JOIN observers obs ON obs.id = o.observer_id
    `).run();
    console.log(`[migration-v3] Migrated ${result.changes} rows (${Date.now() - stepStart}ms)`);

    // d. Drop view, old table, rename
    stepStart = Date.now();
    db.exec('DROP VIEW IF EXISTS packets_v');
    db.exec('DROP TABLE observations');
    db.exec('ALTER TABLE observations_v3 RENAME TO observations');
    console.log(`[migration-v3] Replaced observations table (${Date.now() - stepStart}ms)`);

    // f. Create indexes
    stepStart = Date.now();
    db.exec(`
      CREATE INDEX idx_observations_transmission_id ON observations(transmission_id);
      CREATE INDEX idx_observations_observer_idx ON observations(observer_idx);
      CREATE INDEX idx_observations_timestamp ON observations(timestamp);
      CREATE UNIQUE INDEX idx_observations_dedup ON observations(transmission_id, observer_idx, COALESCE(path_json, ''));
    `);
    console.log(`[migration-v3] Created indexes (${Date.now() - stepStart}ms)`);

    // g. Set schema version
    
    db.pragma('user_version = 3');
    schemaVersion = 3;

    // h. Rebuild view (done below in common code)

    // i. VACUUM + checkpoint
    stepStart = Date.now();
    db.exec('VACUUM');
    db.pragma('wal_checkpoint(TRUNCATE)');
    console.log(`[migration-v3] VACUUM + checkpoint complete (${Date.now() - stepStart}ms)`);

    console.log(`[migration-v3] Migration complete! Total time: ${Date.now() - startTime}ms`);
    return true;
  } catch (e) {
    console.error(`[migration-v3] Migration failed: ${e.message}`);
    console.error('[migration-v3] Restore from backup if needed: ' + dbPath + '.pre-v3-backup');
    // Try to clean up v3 table if it exists
    try { db.exec('DROP TABLE IF EXISTS observations_v3'); } catch {}
    return false;
  }
}

const isV3 = schemaVersion >= 3;

if (!isV3 && needsV3Migration()) {
  runV3Migration();
}

// If user_version < 3 and no migration happened (fresh DB or migration skipped), create old-style table
if (schemaVersion < 3) {
  const obsExists = db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='observations'").get();
  if (!obsExists) {
    // Fresh DB — create v3 schema directly
    db.exec(`
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
      CREATE UNIQUE INDEX idx_observations_dedup ON observations(transmission_id, observer_idx, COALESCE(path_json, ''));
    `);
    
    db.pragma('user_version = 3');
    schemaVersion = 3;
  } else {
    // Old-style observations table exists but migration wasn't run (or failed)
    // Ensure indexes exist for old schema
    db.exec(`
      CREATE INDEX IF NOT EXISTS idx_observations_hash ON observations(hash);
      CREATE INDEX IF NOT EXISTS idx_observations_transmission_id ON observations(transmission_id);
      CREATE INDEX IF NOT EXISTS idx_observations_observer_id ON observations(observer_id);
      CREATE INDEX IF NOT EXISTS idx_observations_timestamp ON observations(timestamp);
    `);
    // Dedup cleanup for old schema
    try {
      db.exec(`DROP INDEX IF EXISTS idx_observations_dedup`);
      db.exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_observations_dedup ON observations(hash, observer_id, COALESCE(path_json, ''))`);
      db.exec(`DELETE FROM observations WHERE id NOT IN (SELECT MIN(id) FROM observations GROUP BY hash, observer_id, COALESCE(path_json, ''))`);
    } catch {}
  }
}

// --- Create/rebuild packets_v view ---
db.exec('DROP VIEW IF EXISTS packets_v');
if (schemaVersion >= 3) {
  db.exec(`
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
  `);
} else {
  db.exec(`
    CREATE VIEW packets_v AS
      SELECT o.id, t.raw_hex, o.timestamp, o.observer_id, o.observer_name,
             o.direction, o.snr, o.rssi, o.score, t.hash, t.route_type,
             t.payload_type, t.payload_version, o.path_json, t.decoded_json,
             t.created_at
      FROM observations o
      JOIN transmissions t ON t.id = o.transmission_id
  `);
}

// --- Migrations for existing DBs ---
const observerCols = db.pragma('table_info(observers)').map(c => c.name);
for (const col of ['model', 'firmware', 'client_version', 'radio', 'battery_mv', 'uptime_secs', 'noise_floor']) {
  if (!observerCols.includes(col)) {
    const type = ['battery_mv', 'uptime_secs', 'noise_floor'].includes(col) ? 'INTEGER' : 'TEXT';
    db.exec(`ALTER TABLE observers ADD COLUMN ${col} ${type}`);
    console.log(`[migration] Added observers.${col}`);
  }
}

// --- Cleanup corrupted nodes on startup ---
// Remove nodes with obviously invalid data (short pubkeys, control chars in names, etc.)
{
  const cleaned = db.prepare(`
    DELETE FROM nodes WHERE
      length(public_key) < 16
      OR public_key GLOB '*[^0-9a-fA-F]*'
      OR (lat IS NOT NULL AND (lat < -90 OR lat > 90))
      OR (lon IS NOT NULL AND (lon < -180 OR lon > 180))
  `).run();
  if (cleaned.changes > 0) console.log(`[cleanup] Removed ${cleaned.changes} corrupted node(s) from DB`);
}

// --- One-time migration: recalculate advert_count to count unique transmissions only ---
{
  db.exec(`CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY)`);
  const done = db.prepare(`SELECT 1 FROM _migrations WHERE name = 'advert_count_unique_v1'`).get();
  if (!done) {
    const start = Date.now();
    console.log('[migration] Recalculating advert_count (unique transmissions only)...');
    db.prepare(`
      UPDATE nodes SET advert_count = (
        SELECT COUNT(*) FROM transmissions t
        WHERE t.payload_type = 4
          AND t.decoded_json LIKE '%' || nodes.public_key || '%'
      )
    `).run();
    db.prepare(`INSERT INTO _migrations (name) VALUES ('advert_count_unique_v1')`).run();
    console.log(`[migration] advert_count recalculated in ${Date.now() - start}ms`);
  }
}

// --- One-time migration: add telemetry columns to nodes and inactive_nodes ---
{
  const done = db.prepare(`SELECT 1 FROM _migrations WHERE name = 'node_telemetry_v1'`).get();
  if (!done) {
    console.log('[migration] Adding telemetry columns to nodes/inactive_nodes...');
    const nodeCols = db.pragma('table_info(nodes)').map(c => c.name);
    if (!nodeCols.includes('battery_mv')) db.exec(`ALTER TABLE nodes ADD COLUMN battery_mv INTEGER`);
    if (!nodeCols.includes('temperature_c')) db.exec(`ALTER TABLE nodes ADD COLUMN temperature_c REAL`);
    const inactiveCols = db.pragma('table_info(inactive_nodes)').map(c => c.name);
    if (!inactiveCols.includes('battery_mv')) db.exec(`ALTER TABLE inactive_nodes ADD COLUMN battery_mv INTEGER`);
    if (!inactiveCols.includes('temperature_c')) db.exec(`ALTER TABLE inactive_nodes ADD COLUMN temperature_c REAL`);
    db.prepare(`INSERT INTO _migrations (name) VALUES ('node_telemetry_v1')`).run();
    console.log('[migration] node telemetry columns added');
  }
}

// --- Prepared statements ---
const stmts = {
  upsertNode: db.prepare(`
    INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen)
    VALUES (@public_key, @name, @role, @lat, @lon, @last_seen, @first_seen)
    ON CONFLICT(public_key) DO UPDATE SET
      name = COALESCE(@name, name),
      role = COALESCE(@role, role),
      lat = COALESCE(@lat, lat),
      lon = COALESCE(@lon, lon),
      last_seen = @last_seen
  `),
  incrementAdvertCount: db.prepare(`
    UPDATE nodes SET advert_count = advert_count + 1 WHERE public_key = @public_key
  `),
  updateNodeTelemetry: db.prepare(`
    UPDATE nodes SET
      battery_mv = COALESCE(@battery_mv, battery_mv),
      temperature_c = COALESCE(@temperature_c, temperature_c)
    WHERE public_key = @public_key
  `),
  upsertObserver: db.prepare(`
    INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor)
    VALUES (@id, @name, @iata, @last_seen, @first_seen, 1, @model, @firmware, @client_version, @radio, @battery_mv, @uptime_secs, @noise_floor)
    ON CONFLICT(id) DO UPDATE SET
      name = COALESCE(@name, name),
      iata = COALESCE(@iata, iata),
      last_seen = @last_seen,
      packet_count = packet_count + 1,
      model = COALESCE(@model, model),
      firmware = COALESCE(@firmware, firmware),
      client_version = COALESCE(@client_version, client_version),
      radio = COALESCE(@radio, radio),
      battery_mv = COALESCE(@battery_mv, battery_mv),
      uptime_secs = COALESCE(@uptime_secs, uptime_secs),
      noise_floor = COALESCE(@noise_floor, noise_floor)
  `),
  updateObserverStatus: db.prepare(`
    INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, model, firmware, client_version, radio, battery_mv, uptime_secs, noise_floor)
    VALUES (@id, @name, @iata, @last_seen, @first_seen, 0, @model, @firmware, @client_version, @radio, @battery_mv, @uptime_secs, @noise_floor)
    ON CONFLICT(id) DO UPDATE SET
      name = COALESCE(@name, name),
      iata = COALESCE(@iata, iata),
      last_seen = @last_seen,
      model = COALESCE(@model, model),
      firmware = COALESCE(@firmware, firmware),
      client_version = COALESCE(@client_version, client_version),
      radio = COALESCE(@radio, radio),
      battery_mv = COALESCE(@battery_mv, battery_mv),
      uptime_secs = COALESCE(@uptime_secs, uptime_secs),
      noise_floor = COALESCE(@noise_floor, noise_floor)
  `),
  getPacket: db.prepare(`SELECT * FROM packets_v WHERE id = ?`),
  getNode: db.prepare(`SELECT * FROM nodes WHERE public_key = ?`),
  getRecentPacketsForNode: db.prepare(`
    SELECT * FROM packets_v WHERE decoded_json LIKE ? OR decoded_json LIKE ? OR decoded_json LIKE ? OR decoded_json LIKE ?
    ORDER BY timestamp DESC LIMIT 20
  `),
  getObservers: db.prepare(`SELECT * FROM observers ORDER BY last_seen DESC`),
  countPackets: db.prepare(`SELECT COUNT(*) as count FROM observations`),
  countNodes: db.prepare(`SELECT COUNT(*) as count FROM nodes`),
  countActiveNodes: db.prepare(`SELECT COUNT(*) as count FROM nodes WHERE last_seen > ?`),
  countActiveNodesByRole: db.prepare(`SELECT COUNT(*) as count FROM nodes WHERE role = ? AND last_seen > ?`),
  countObservers: db.prepare(`SELECT COUNT(*) as count FROM observers`),
  countRecentPackets: schemaVersion >= 3
    ? db.prepare(`SELECT COUNT(*) as count FROM observations WHERE timestamp > CAST(strftime('%s', ?) AS INTEGER)`)
    : db.prepare(`SELECT COUNT(*) as count FROM observations WHERE timestamp > ?`),
  getTransmissionByHash: db.prepare(`SELECT id, first_seen FROM transmissions WHERE hash = ?`),
  insertTransmission: db.prepare(`
    INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
    VALUES (@raw_hex, @hash, @first_seen, @route_type, @payload_type, @payload_version, @decoded_json)
  `),
  updateTransmissionFirstSeen: db.prepare(`UPDATE transmissions SET first_seen = @first_seen WHERE id = @id`),
  insertObservation: schemaVersion >= 3
    ? db.prepare(`
        INSERT OR IGNORE INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
        VALUES (@transmission_id, @observer_idx, @direction, @snr, @rssi, @score, @path_json, @timestamp)
      `)
    : db.prepare(`
        INSERT OR IGNORE INTO observations (transmission_id, hash, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp)
        VALUES (@transmission_id, @hash, @observer_id, @observer_name, @direction, @snr, @rssi, @score, @path_json, @timestamp)
      `),
  getObserverRowid: db.prepare(`SELECT rowid FROM observers WHERE id = ?`),
};

// --- In-memory observer map (observer_id text → rowid integer) ---
const observerIdToRowid = new Map();
if (schemaVersion >= 3) {
  const rows = db.prepare('SELECT id, rowid FROM observers').all();
  for (const r of rows) observerIdToRowid.set(r.id, r.rowid);
}

// --- In-memory dedup set for v3 ---
const dedupSet = new Map(); // key → timestamp (for cleanup)
const DEDUP_TTL_MS = 5 * 60 * 1000; // 5 minutes

function cleanupDedupSet() {
  const cutoff = Date.now() - DEDUP_TTL_MS;
  for (const [key, ts] of dedupSet) {
    if (ts < cutoff) dedupSet.delete(key);
  }
}

// Periodic cleanup every 60s
setInterval(cleanupDedupSet, 60000).unref();

function resolveObserverIdx(observerId) {
  if (!observerId) return null;
  let rowid = observerIdToRowid.get(observerId);
  if (rowid !== undefined) return rowid;
  // Try DB lookup (observer may have been inserted elsewhere)
  const row = stmts.getObserverRowid.get(observerId);
  if (row) {
    observerIdToRowid.set(observerId, row.rowid);
    return row.rowid;
  }
  return null;
}

// --- Helper functions ---

function insertTransmission(data) {
  const hash = data.hash;
  if (!hash) return null;

  const timestamp = data.timestamp || new Date().toISOString();
  let transmissionId;

  let isNew = false;
  const existing = stmts.getTransmissionByHash.get(hash);
  if (existing) {
    transmissionId = existing.id;
    if (timestamp < existing.first_seen) {
      stmts.updateTransmissionFirstSeen.run({ id: transmissionId, first_seen: timestamp });
    }
  } else {
    isNew = true;
    const result = stmts.insertTransmission.run({
      raw_hex: data.raw_hex || '',
      hash,
      first_seen: timestamp,
      route_type: data.route_type ?? null,
      payload_type: data.payload_type ?? null,
      payload_version: data.payload_version ?? null,
      decoded_json: data.decoded_json || null,
    });
    transmissionId = result.lastInsertRowid;
  }

  let obsResult;
  if (schemaVersion >= 3) {
    const observerIdx = resolveObserverIdx(data.observer_id);
    const epochTs = typeof timestamp === 'number' ? timestamp : Math.floor(new Date(timestamp).getTime() / 1000);

    // In-memory dedup check
    const dedupKey = `${transmissionId}|${observerIdx}|${data.path_json || ''}`;
    if (dedupSet.has(dedupKey)) {
      return { transmissionId, observationId: 0, isNew };
    }

    obsResult = stmts.insertObservation.run({
      transmission_id: transmissionId,
      observer_idx: observerIdx,
      direction: data.direction || null,
      snr: data.snr ?? null,
      rssi: data.rssi ?? null,
      score: data.score ?? null,
      path_json: data.path_json || null,
      timestamp: epochTs,
    });
    dedupSet.set(dedupKey, Date.now());
  } else {
    obsResult = stmts.insertObservation.run({
      transmission_id: transmissionId,
      hash,
      observer_id: data.observer_id || null,
      observer_name: data.observer_name || null,
      direction: data.direction || null,
      snr: data.snr ?? null,
      rssi: data.rssi ?? null,
      score: data.score ?? null,
      path_json: data.path_json || null,
      timestamp,
    });
  }

  return { transmissionId, observationId: obsResult.lastInsertRowid, isNew };
}

function incrementAdvertCount(publicKey) {
  stmts.incrementAdvertCount.run({ public_key: publicKey });
}

function updateNodeTelemetry(data) {
  stmts.updateNodeTelemetry.run({
    public_key: data.public_key,
    battery_mv: data.battery_mv ?? null,
    temperature_c: data.temperature_c ?? null,
  });
}

function upsertNode(data) {
  const now = new Date().toISOString();
  stmts.upsertNode.run({
    public_key: data.public_key,
    name: data.name || null,
    role: data.role || null,
    lat: data.lat ?? null,
    lon: data.lon ?? null,
    last_seen: data.last_seen || now,
    first_seen: data.first_seen || now,
  });
}

function upsertObserver(data) {
  const now = new Date().toISOString();
  stmts.upsertObserver.run({
    id: data.id,
    name: data.name || null,
    iata: data.iata || null,
    last_seen: data.last_seen || now,
    first_seen: data.first_seen || now,
    model: data.model || null,
    firmware: data.firmware || null,
    client_version: data.client_version || null,
    radio: data.radio || null,
    battery_mv: data.battery_mv || null,
    uptime_secs: data.uptime_secs || null,
    noise_floor: data.noise_floor || null,
  });
  // Update in-memory map for v3
  if (schemaVersion >= 3 && !observerIdToRowid.has(data.id)) {
    const row = stmts.getObserverRowid.get(data.id);
    if (row) observerIdToRowid.set(data.id, row.rowid);
  }
}

function updateObserverStatus(data) {
  const now = new Date().toISOString();
  stmts.updateObserverStatus.run({
    id: data.id,
    name: data.name || null,
    iata: data.iata || null,
    last_seen: data.last_seen || now,
    first_seen: data.first_seen || now,
    model: data.model || null,
    firmware: data.firmware || null,
    client_version: data.client_version || null,
    radio: data.radio || null,
    battery_mv: data.battery_mv || null,
    uptime_secs: data.uptime_secs || null,
    noise_floor: data.noise_floor || null,
  });
}

function getPackets({ limit = 50, offset = 0, type, route, hash, since } = {}) {
  let where = [];
  let params = {};
  if (type !== undefined) { where.push('payload_type = @type'); params.type = type; }
  if (route !== undefined) { where.push('route_type = @route'); params.route = route; }
  if (hash) { where.push('hash = @hash'); params.hash = hash; }
  if (since) { where.push('timestamp > @since'); params.since = since; }
  const clause = where.length ? 'WHERE ' + where.join(' AND ') : '';
  const rows = db.prepare(`SELECT * FROM packets_v ${clause} ORDER BY timestamp DESC LIMIT @limit OFFSET @offset`).all({ ...params, limit, offset });
  const total = db.prepare(`SELECT COUNT(*) as count FROM packets_v ${clause}`).get(params).count;
  return { rows, total };
}

function getTransmission(id) {
  try {
    return db.prepare('SELECT * FROM transmissions WHERE id = ?').get(id) || null;
  } catch { return null; }
}

function getPacket(id) {
  const packet = stmts.getPacket.get(id);
  if (!packet) return null;
  return packet;
}

function getNodes({ limit = 50, offset = 0, sortBy = 'last_seen' } = {}) {
  const allowed = ['last_seen', 'name', 'advert_count', 'first_seen'];
  const col = allowed.includes(sortBy) ? sortBy : 'last_seen';
  const dir = col === 'name' ? 'ASC' : 'DESC';
  const rows = db.prepare(`SELECT * FROM nodes ORDER BY ${col} ${dir} LIMIT ? OFFSET ?`).all(limit, offset);
  const total = stmts.countNodes.get().count;
  return { rows, total };
}

function getNode(pubkey) {
  const node = stmts.getNode.get(pubkey);
  if (!node) return null;
  // Match by: pubkey anywhere, name in sender/text fields, name as text prefix ("Name: msg")
  const namePattern = node.name ? `%${node.name}%` : `%${pubkey}%`;
  const textPrefix = node.name ? `%"text":"${node.name}:%` : `%${pubkey}%`;
  node.recentPackets = stmts.getRecentPacketsForNode.all(
    `%${pubkey}%`,
    namePattern,
    textPrefix,
    `%"sender":"${node.name || pubkey}"%`
  );
  return node;
}

function getObservers() {
  return stmts.getObservers.all();
}

function getStats() {
  const oneHourAgo = new Date(Date.now() - 3600000).toISOString();
  const sevenDaysAgo = new Date(Date.now() - 7 * 24 * 3600000).toISOString();
  // Try to get transmission count from normalized schema
  let totalTransmissions = null;
  try {
    totalTransmissions = db.prepare('SELECT COUNT(*) as count FROM transmissions').get().count;
  } catch {}
  return {
    totalPackets: totalTransmissions || stmts.countPackets.get().count,
    totalTransmissions,
    totalObservations: stmts.countPackets.get().count,
    totalNodes: stmts.countActiveNodes.get(sevenDaysAgo).count,
    totalNodesAllTime: stmts.countNodes.get().count,
    totalObservers: stmts.countObservers.get().count,
    packetsLastHour: stmts.countRecentPackets.get(oneHourAgo).count,
    packetsLast24h: stmts.countRecentPackets.get(new Date(Date.now() - 24 * 3600000).toISOString()).count,
  };
}

// --- Run directly ---
if (require.main === module) {
  console.log('Stats:', getStats());
}

// Remove phantom nodes created by autoLearnHopNodes before this fix.
// Real MeshCore pubkeys are 32 bytes (64 hex chars). Phantom nodes have only
// the hop prefix as their public_key (typically 4-8 hex chars).
// Threshold: public_key <= 16 hex chars (8 bytes) is too short to be real.
function removePhantomNodes() {
  const result = db.prepare(`DELETE FROM nodes WHERE LENGTH(public_key) <= 16`).run();
  if (result.changes > 0) {
    console.log(`[cleanup] Removed ${result.changes} phantom node(s) with short public_key prefixes`);
  }
  return result.changes;
}

function searchNodes(query, limit = 10) {
  return db.prepare(`
    SELECT * FROM nodes
    WHERE name LIKE @q OR public_key LIKE @prefix
    ORDER BY last_seen DESC
    LIMIT @limit
  `).all({ q: `%${query}%`, prefix: `${query}%`, limit });
}

function getNodeHealth(pubkey) {
  const node = stmts.getNode.get(pubkey);
  if (!node) return null;

  const todayStart = new Date();
  todayStart.setUTCHours(0, 0, 0, 0);
  const todayISO = todayStart.toISOString();

  const keyPattern = `%${pubkey}%`;
  // Also match by node name in decoded_json (channel messages have sender name, not pubkey)
  const namePattern = node.name ? `%${node.name.replace(/[%_]/g, '')}%` : null;
  const whereClause = namePattern
    ? `(decoded_json LIKE @keyPattern OR decoded_json LIKE @namePattern)`
    : `decoded_json LIKE @keyPattern`;
  const params = namePattern ? { keyPattern, namePattern } : { keyPattern };

  // Observers that heard this node
  const observers = db.prepare(`
    SELECT observer_id, observer_name,
      AVG(snr) as avgSnr, AVG(rssi) as avgRssi, COUNT(*) as packetCount
    FROM packets_v
    WHERE ${whereClause} AND observer_id IS NOT NULL
    GROUP BY observer_id
    ORDER BY packetCount DESC
  `).all(params);

  // Stats
  const packetsToday = db.prepare(`
    SELECT COUNT(*) as count FROM packets_v WHERE ${whereClause} AND timestamp > @since
  `).get({ ...params, since: todayISO }).count;

  const avgStats = db.prepare(`
    SELECT AVG(snr) as avgSnr FROM packets_v WHERE ${whereClause}
  `).get(params);

  const lastHeard = db.prepare(`
    SELECT MAX(timestamp) as lastHeard FROM packets_v WHERE ${whereClause}
  `).get(params).lastHeard;

  // Avg hops from path_json
  const pathRows = db.prepare(`
    SELECT path_json FROM packets_v WHERE ${whereClause} AND path_json IS NOT NULL
  `).all(params);

  let totalHops = 0, hopCount = 0;
  for (const row of pathRows) {
    try {
      const hops = JSON.parse(row.path_json);
      if (Array.isArray(hops)) { totalHops += hops.length; hopCount++; }
    } catch {}
  }
  const avgHops = hopCount > 0 ? Math.round(totalHops / hopCount) : 0;

  const totalPackets = db.prepare(`
    SELECT COUNT(*) as count FROM packets_v WHERE ${whereClause}
  `).get(params).count;

  // Recent 10 packets
  const recentPackets = db.prepare(`
    SELECT * FROM packets_v WHERE ${whereClause} ORDER BY timestamp DESC LIMIT 10
  `).all(params);

  return {
    node,
    observers,
    stats: { totalPackets, packetsToday, avgSnr: avgStats.avgSnr, avgHops, lastHeard },
    recentPackets,
  };
}

function getNodeAnalytics(pubkey, days) {
  const node = stmts.getNode.get(pubkey);
  if (!node) return null;

  const now = new Date();
  const from = new Date(now.getTime() - days * 86400000);
  const fromISO = from.toISOString();
  const toISO = now.toISOString();

  const keyPattern = `%${pubkey}%`;
  const namePattern = node.name ? `%${node.name.replace(/[%_]/g, '')}%` : null;
  const whereClause = namePattern
    ? `(decoded_json LIKE @keyPattern OR decoded_json LIKE @namePattern)`
    : `decoded_json LIKE @keyPattern`;
  const timeWhere = `${whereClause} AND timestamp > @fromISO`;
  const params = namePattern ? { keyPattern, namePattern, fromISO } : { keyPattern, fromISO };

  // Activity timeline
  const activityTimeline = db.prepare(`
    SELECT strftime('%Y-%m-%dT%H:00:00Z', timestamp) as bucket, COUNT(*) as count
    FROM packets_v WHERE ${timeWhere} GROUP BY bucket ORDER BY bucket
  `).all(params);

  // SNR trend
  const snrTrend = db.prepare(`
    SELECT timestamp, snr, rssi, observer_id, observer_name
    FROM packets_v WHERE ${timeWhere} AND snr IS NOT NULL ORDER BY timestamp
  `).all(params);

  // Packet type breakdown
  const packetTypeBreakdown = db.prepare(`
    SELECT payload_type, COUNT(*) as count FROM packets_v WHERE ${timeWhere} GROUP BY payload_type
  `).all(params);

  // Observer coverage
  const observerCoverage = db.prepare(`
    SELECT observer_id, observer_name, COUNT(*) as packetCount,
      AVG(snr) as avgSnr, AVG(rssi) as avgRssi, MIN(timestamp) as firstSeen, MAX(timestamp) as lastSeen
    FROM packets_v WHERE ${timeWhere} AND observer_id IS NOT NULL
    GROUP BY observer_id ORDER BY packetCount DESC
  `).all(params);

  // Hop distribution
  const pathRows = db.prepare(`
    SELECT path_json FROM packets_v WHERE ${timeWhere} AND path_json IS NOT NULL
  `).all(params);

  const hopCounts = {};
  let totalWithPath = 0, relayedCount = 0;
  for (const row of pathRows) {
    try {
      const hops = JSON.parse(row.path_json);
      if (Array.isArray(hops)) {
        const h = hops.length;
        const key = h >= 4 ? '4+' : String(h);
        hopCounts[key] = (hopCounts[key] || 0) + 1;
        totalWithPath++;
        if (h > 1) relayedCount++;
      }
    } catch {}
  }
  const hopDistribution = Object.entries(hopCounts).map(([hops, count]) => ({ hops, count }))
    .sort((a, b) => a.hops.localeCompare(b.hops, undefined, { numeric: true }));

  // Peer interactions from decoded_json
  const decodedRows = db.prepare(`
    SELECT decoded_json, timestamp FROM packets_v WHERE ${timeWhere} AND decoded_json IS NOT NULL
  `).all(params);

  const peerMap = {};
  for (const row of decodedRows) {
    try {
      const d = JSON.parse(row.decoded_json);
      // Look for sender/recipient pubkeys that aren't this node
      const candidates = [];
      if (d.sender_key && d.sender_key !== pubkey) candidates.push({ key: d.sender_key, name: d.sender_name || d.sender_short_name });
      if (d.recipient_key && d.recipient_key !== pubkey) candidates.push({ key: d.recipient_key, name: d.recipient_name || d.recipient_short_name });
      if (d.pubkey && d.pubkey !== pubkey) candidates.push({ key: d.pubkey, name: d.name });
      for (const c of candidates) {
        if (!c.key) continue;
        if (!peerMap[c.key]) peerMap[c.key] = { peer_key: c.key, peer_name: c.name || c.key.slice(0, 12), messageCount: 0, lastContact: row.timestamp };
        peerMap[c.key].messageCount++;
        if (row.timestamp > peerMap[c.key].lastContact) peerMap[c.key].lastContact = row.timestamp;
      }
    } catch {}
  }
  const peerInteractions = Object.values(peerMap).sort((a, b) => b.messageCount - a.messageCount).slice(0, 20);

  // Uptime heatmap
  const uptimeHeatmap = db.prepare(`
    SELECT CAST(strftime('%w', timestamp) AS INTEGER) as dayOfWeek,
      CAST(strftime('%H', timestamp) AS INTEGER) as hour, COUNT(*) as count
    FROM packets_v WHERE ${timeWhere} GROUP BY dayOfWeek, hour
  `).all(params);

  // Computed stats
  const totalPackets = db.prepare(`SELECT COUNT(*) as count FROM packets_v WHERE ${timeWhere}`).get(params).count;
  const uniqueObservers = observerCoverage.length;
  const uniquePeers = peerInteractions.length;
  const avgPacketsPerDay = days > 0 ? Math.round(totalPackets / days * 10) / 10 : totalPackets;

  // Availability: distinct hours with packets / total hours
  const distinctHours = activityTimeline.length;
  const totalHours = days * 24;
  const availabilityPct = totalHours > 0 ? Math.round(distinctHours / totalHours * 1000) / 10 : 0;

  // Longest silence
  const timestamps = db.prepare(`
    SELECT timestamp FROM packets_v WHERE ${timeWhere} ORDER BY timestamp
  `).all(params).map(r => new Date(r.timestamp).getTime());

  let longestSilenceMs = 0, longestSilenceStart = null;
  for (let i = 1; i < timestamps.length; i++) {
    const gap = timestamps[i] - timestamps[i - 1];
    if (gap > longestSilenceMs) { longestSilenceMs = gap; longestSilenceStart = new Date(timestamps[i - 1]).toISOString(); }
  }

  // Signal grade
  const snrValues = snrTrend.map(r => r.snr);
  const snrMean = snrValues.length > 0 ? snrValues.reduce((a, b) => a + b, 0) / snrValues.length : 0;
  const snrStdDev = snrValues.length > 1 ? Math.sqrt(snrValues.reduce((s, v) => s + (v - snrMean) ** 2, 0) / snrValues.length) : 0;
  let signalGrade = 'D';
  if (snrMean > 15 && snrStdDev < 2) signalGrade = 'A';
  else if (snrMean > 15) signalGrade = 'A-';
  else if (snrMean > 12 && snrStdDev < 3) signalGrade = 'B+';
  else if (snrMean > 8) signalGrade = 'B';
  else if (snrMean > 3) signalGrade = 'C';

  const relayPct = totalWithPath > 0 ? Math.round(relayedCount / totalWithPath * 1000) / 10 : 0;

  return {
    node,
    timeRange: { from: fromISO, to: toISO, days },
    activityTimeline,
    snrTrend,
    packetTypeBreakdown,
    observerCoverage,
    hopDistribution,
    peerInteractions,
    uptimeHeatmap,
    computedStats: {
      availabilityPct, longestSilenceMs, longestSilenceStart, signalGrade,
      snrMean: Math.round(snrMean * 10) / 10, snrStdDev: Math.round(snrStdDev * 10) / 10,
      relayPct, totalPackets, uniqueObservers, uniquePeers, avgPacketsPerDay
    }
  };
}

// Move stale nodes to inactive_nodes table based on retention.nodeDays config.
function moveStaleNodes(nodeDays) {
  if (!nodeDays || nodeDays <= 0) return 0;
  const cutoff = new Date(Date.now() - nodeDays * 24 * 3600000).toISOString();
  const move = db.transaction(() => {
    db.prepare(`INSERT OR REPLACE INTO inactive_nodes SELECT * FROM nodes WHERE last_seen < ?`).run(cutoff);
    const result = db.prepare(`DELETE FROM nodes WHERE last_seen < ?`).run(cutoff);
    return result.changes;
  });
  const moved = move();
  if (moved > 0) {
    console.log(`[retention] Moved ${moved} node(s) to inactive_nodes (not seen in ${nodeDays} days)`);
  }
  return moved;
}

module.exports = { db, schemaVersion, observerIdToRowid, resolveObserverIdx, insertTransmission, upsertNode, incrementAdvertCount, updateNodeTelemetry, upsertObserver, updateObserverStatus, getPackets, getPacket, getTransmission, getNodes, getNode, getObservers, getStats, searchNodes, getNodeHealth, getNodeAnalytics, removePhantomNodes, moveStaleNodes };
