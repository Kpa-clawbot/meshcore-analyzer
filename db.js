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

// --- Schema ---
db.exec(`
  CREATE TABLE IF NOT EXISTS packets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    raw_hex TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    observer_id TEXT,
    observer_name TEXT,
    direction TEXT,
    snr REAL,
    rssi REAL,
    score INTEGER,
    hash TEXT,
    route_type INTEGER,
    payload_type INTEGER,
    payload_version INTEGER,
    path_json TEXT,
    decoded_json TEXT,
    created_at TEXT DEFAULT (datetime('now'))
  );

  CREATE TABLE IF NOT EXISTS nodes (
    public_key TEXT PRIMARY KEY,
    name TEXT,
    role TEXT,
    lat REAL,
    lon REAL,
    last_seen TEXT,
    first_seen TEXT,
    advert_count INTEGER DEFAULT 0
  );

  CREATE TABLE IF NOT EXISTS observers (
    id TEXT PRIMARY KEY,
    name TEXT,
    iata TEXT,
    last_seen TEXT,
    first_seen TEXT,
    packet_count INTEGER DEFAULT 0
  );

  CREATE TABLE IF NOT EXISTS paths (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    packet_id INTEGER REFERENCES packets(id),
    hop_index INTEGER,
    node_hash TEXT
  );

  CREATE INDEX IF NOT EXISTS idx_packets_timestamp ON packets(timestamp);
  CREATE INDEX IF NOT EXISTS idx_packets_hash ON packets(hash);
  CREATE INDEX IF NOT EXISTS idx_packets_payload_type ON packets(payload_type);
  CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen);
  CREATE INDEX IF NOT EXISTS idx_observers_last_seen ON observers(last_seen);
`);

// --- Prepared statements ---
const stmts = {
  insertPacket: db.prepare(`
    INSERT INTO packets (raw_hex, timestamp, observer_id, observer_name, direction, snr, rssi, score, hash, route_type, payload_type, payload_version, path_json, decoded_json)
    VALUES (@raw_hex, @timestamp, @observer_id, @observer_name, @direction, @snr, @rssi, @score, @hash, @route_type, @payload_type, @payload_version, @path_json, @decoded_json)
  `),
  insertPath: db.prepare(`INSERT INTO paths (packet_id, hop_index, node_hash) VALUES (?, ?, ?)`),
  upsertNode: db.prepare(`
    INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
    VALUES (@public_key, @name, @role, @lat, @lon, @last_seen, @first_seen, 1)
    ON CONFLICT(public_key) DO UPDATE SET
      name = COALESCE(@name, name),
      role = COALESCE(@role, role),
      lat = COALESCE(@lat, lat),
      lon = COALESCE(@lon, lon),
      last_seen = @last_seen,
      advert_count = advert_count + 1
  `),
  upsertObserver: db.prepare(`
    INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
    VALUES (@id, @name, @iata, @last_seen, @first_seen, 1)
    ON CONFLICT(id) DO UPDATE SET
      name = COALESCE(@name, name),
      iata = COALESCE(@iata, iata),
      last_seen = @last_seen,
      packet_count = packet_count + 1
  `),
  getPacket: db.prepare(`SELECT * FROM packets WHERE id = ?`),
  getPathsForPacket: db.prepare(`SELECT * FROM paths WHERE packet_id = ? ORDER BY hop_index`),
  getNode: db.prepare(`SELECT * FROM nodes WHERE public_key = ?`),
  getRecentPacketsForNode: db.prepare(`
    SELECT * FROM packets WHERE decoded_json LIKE ? OR decoded_json LIKE ? OR decoded_json LIKE ? OR decoded_json LIKE ?
    ORDER BY timestamp DESC LIMIT 20
  `),
  getObservers: db.prepare(`SELECT * FROM observers ORDER BY last_seen DESC`),
  countPackets: db.prepare(`SELECT COUNT(*) as count FROM packets`),
  countNodes: db.prepare(`SELECT COUNT(*) as count FROM nodes`),
  countObservers: db.prepare(`SELECT COUNT(*) as count FROM observers`),
  countRecentPackets: db.prepare(`SELECT COUNT(*) as count FROM packets WHERE timestamp > ?`),
};

// --- Helper functions ---

function insertPacket(data) {
  const d = {
    raw_hex: data.raw_hex,
    timestamp: data.timestamp || new Date().toISOString(),
    observer_id: data.observer_id || null,
    observer_name: data.observer_name || null,
    direction: data.direction || null,
    snr: data.snr ?? null,
    rssi: data.rssi ?? null,
    score: data.score ?? null,
    hash: data.hash || null,
    route_type: data.route_type ?? null,
    payload_type: data.payload_type ?? null,
    payload_version: data.payload_version ?? null,
    path_json: data.path_json || null,
    decoded_json: data.decoded_json || null,
  };
  return stmts.insertPacket.run(d).lastInsertRowid;
}

function insertPath(packetId, hops) {
  const tx = db.transaction((hops) => {
    for (let i = 0; i < hops.length; i++) {
      stmts.insertPath.run(packetId, i, hops[i]);
    }
  });
  tx(hops);
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
  const rows = db.prepare(`SELECT * FROM packets ${clause} ORDER BY timestamp DESC LIMIT @limit OFFSET @offset`).all({ ...params, limit, offset });
  const total = db.prepare(`SELECT COUNT(*) as count FROM packets ${clause}`).get(params).count;
  return { rows, total };
}

function getPacket(id) {
  const packet = stmts.getPacket.get(id);
  if (!packet) return null;
  packet.paths = stmts.getPathsForPacket.all(id);
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
  return {
    totalPackets: stmts.countPackets.get().count,
    totalNodes: stmts.countNodes.get().count,
    totalObservers: stmts.countObservers.get().count,
    packetsLastHour: stmts.countRecentPackets.get(oneHourAgo).count,
  };
}

function seed() {
  if (stmts.countPackets.get().count > 0) return false;
  const now = new Date().toISOString();
  const rawHex = '11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172';

  upsertObserver({ id: 'obs-sjc-001', name: 'User Observer', iata: 'SJC', last_seen: now, first_seen: now });

  const pktId = insertPacket({
    raw_hex: rawHex,
    timestamp: now,
    observer_id: 'obs-sjc-001',
    observer_name: 'User Observer',
    direction: 'rx',
    snr: 10.5,
    rssi: -85,
    score: 42,
    hash: 'seed-test-hash',
    route_type: 1,
    payload_type: 4,
    payload_version: 1,
    path_json: JSON.stringify(['A1B2', 'C3D4']),
    decoded_json: JSON.stringify({ type: 'ADVERT', name: 'Kpa Roof Solar', role: 'repeater', lat: 37.31468, lon: -121.8921 }),
  });

  insertPath(pktId, ['A1B2', 'C3D4']);

  upsertNode({
    public_key: 'kpa-roof-solar-pubkey',
    name: 'Kpa Roof Solar',
    role: 'repeater',
    lat: 37.31468,
    lon: -121.8921,
    last_seen: now,
    first_seen: now,
  });

  return true;
}

// --- Run directly ---
if (require.main === module) {
  const seeded = seed();
  console.log(seeded ? 'Database seeded with test data.' : 'Database already has data, skipping seed.');
  console.log('Stats:', getStats());
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
    FROM packets
    WHERE ${whereClause} AND observer_id IS NOT NULL
    GROUP BY observer_id
    ORDER BY packetCount DESC
  `).all(params);

  // Stats
  const packetsToday = db.prepare(`
    SELECT COUNT(*) as count FROM packets WHERE ${whereClause} AND timestamp > @since
  `).get({ ...params, since: todayISO }).count;

  const avgStats = db.prepare(`
    SELECT AVG(snr) as avgSnr FROM packets WHERE ${whereClause}
  `).get(params);

  const lastHeard = db.prepare(`
    SELECT MAX(timestamp) as lastHeard FROM packets WHERE ${whereClause}
  `).get(params).lastHeard;

  // Avg hops from path_json
  const pathRows = db.prepare(`
    SELECT path_json FROM packets WHERE ${whereClause} AND path_json IS NOT NULL
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
    SELECT COUNT(*) as count FROM packets WHERE ${whereClause}
  `).get(params).count;

  // Recent 10 packets
  const recentPackets = db.prepare(`
    SELECT * FROM packets WHERE ${whereClause} ORDER BY timestamp DESC LIMIT 10
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
    FROM packets WHERE ${timeWhere} GROUP BY bucket ORDER BY bucket
  `).all(params);

  // SNR trend
  const snrTrend = db.prepare(`
    SELECT timestamp, snr, rssi, observer_id, observer_name
    FROM packets WHERE ${timeWhere} AND snr IS NOT NULL ORDER BY timestamp
  `).all(params);

  // Packet type breakdown
  const packetTypeBreakdown = db.prepare(`
    SELECT payload_type, COUNT(*) as count FROM packets WHERE ${timeWhere} GROUP BY payload_type
  `).all(params);

  // Observer coverage
  const observerCoverage = db.prepare(`
    SELECT observer_id, observer_name, COUNT(*) as packetCount,
      AVG(snr) as avgSnr, AVG(rssi) as avgRssi, MIN(timestamp) as firstSeen, MAX(timestamp) as lastSeen
    FROM packets WHERE ${timeWhere} AND observer_id IS NOT NULL
    GROUP BY observer_id ORDER BY packetCount DESC
  `).all(params);

  // Hop distribution
  const pathRows = db.prepare(`
    SELECT path_json FROM packets WHERE ${timeWhere} AND path_json IS NOT NULL
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
    SELECT decoded_json, timestamp FROM packets WHERE ${timeWhere} AND decoded_json IS NOT NULL
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
    FROM packets WHERE ${timeWhere} GROUP BY dayOfWeek, hour
  `).all(params);

  // Computed stats
  const totalPackets = db.prepare(`SELECT COUNT(*) as count FROM packets WHERE ${timeWhere}`).get(params).count;
  const uniqueObservers = observerCoverage.length;
  const uniquePeers = peerInteractions.length;
  const avgPacketsPerDay = days > 0 ? Math.round(totalPackets / days * 10) / 10 : totalPackets;

  // Availability: distinct hours with packets / total hours
  const distinctHours = activityTimeline.length;
  const totalHours = days * 24;
  const availabilityPct = totalHours > 0 ? Math.round(distinctHours / totalHours * 1000) / 10 : 0;

  // Longest silence
  const timestamps = db.prepare(`
    SELECT timestamp FROM packets WHERE ${timeWhere} ORDER BY timestamp
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

module.exports = { db, insertPacket, insertPath, upsertNode, upsertObserver, getPackets, getPacket, getNodes, getNode, getObservers, getStats, seed, searchNodes, getNodeHealth, getNodeAnalytics };
