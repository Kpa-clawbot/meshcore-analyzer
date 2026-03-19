'use strict';

const express = require('express');
const http = require('http');
const { WebSocketServer } = require('ws');
const mqtt = require('mqtt');
const path = require('path');
const config = require('./config.json');
const decoder = require('./decoder');
const crypto = require('crypto');

// Compute a content hash from raw hex: header byte + payload (skipping path hops)
// This correctly groups retransmissions of the same packet (same content, different paths)
function computeContentHash(rawHex) {
  try {
    const buf = Buffer.from(rawHex, 'hex');
    if (buf.length < 2) return rawHex.slice(0, 16);
    const pathByte = buf[1];
    const hashSize = ((pathByte >> 6) & 0x3) + 1;
    const hashCount = pathByte & 0x3F;
    const pathBytes = hashSize * hashCount;
    const payloadStart = 2 + pathBytes;
    const payload = buf.subarray(payloadStart);
    const toHash = Buffer.concat([Buffer.from([buf[0]]), payload]);
    return crypto.createHash('sha256').update(toHash).digest('hex').slice(0, 16);
  } catch { return rawHex.slice(0, 16); }
}
const db = require('./db');
const channelKeys = require("./config.json").channelKeys || {};

// Seed DB if empty
db.seed();

const app = express();
const server = http.createServer(app);

// --- WebSocket ---
const wss = new WebSocketServer({ server });

function broadcast(msg) {
  const data = JSON.stringify(msg);
  wss.clients.forEach(c => { if (c.readyState === 1) c.send(data); });
}

// Auto-create stub nodes from path hops (≥2 bytes / 4 hex chars)
// When an advert arrives later with a full pubkey matching the prefix, upsertNode will upgrade it
const hopNodeCache = new Set(); // Avoid repeated DB lookups for known hops
function autoLearnHopNodes(hops, now) {
  for (const hop of hops) {
    if (hop.length < 4) continue; // Skip 1-byte hops — too ambiguous
    if (hopNodeCache.has(hop)) continue;
    const hopLower = hop.toLowerCase();
    const existing = db.db.prepare("SELECT public_key FROM nodes WHERE LOWER(public_key) LIKE ?").get(hopLower + '%');
    if (existing) {
      hopNodeCache.add(hop);
      continue;
    }
    // Create stub node — role is likely repeater (most hops are)
    db.upsertNode({ public_key: hopLower, name: null, role: 'repeater', lat: null, lon: null, last_seen: now });
    hopNodeCache.add(hop);
  }
}

// --- MQTT ---
try {
  const mqttClient = mqtt.connect(config.mqtt.broker, { reconnectPeriod: 5000 });
  mqttClient.on('connect', () => {
    console.log(`MQTT connected to ${config.mqtt.broker}`);
    // Subscribe to both packet-logging format and companion bridge format
    mqttClient.subscribe(config.mqtt.topic, (err) => {
      if (err) console.error('MQTT subscribe error:', err);
      else console.log(`MQTT subscribed to ${config.mqtt.topic}`);
    });
    mqttClient.subscribe('meshcore/#', (err) => {
      if (err) console.error('MQTT subscribe error (bridge):', err);
      else console.log('MQTT subscribed to meshcore/#');
    });
  });
  mqttClient.on('error', () => {}); // MQTT errors are expected when broker is offline
  mqttClient.on('offline', () => console.log('MQTT offline'));
  mqttClient.on('message', (topic, message) => {
    try {
      const msg = JSON.parse(message.toString());
      const parts = topic.split('/');
      const now = new Date().toISOString();

      // --- Format 1: Raw packet logging (meshcoretomqtt / Cisien format) ---
      // Topic: meshcore/<region>/<observer>/packets, payload: { raw, SNR, RSSI, hash }
      if (msg.raw && typeof msg.raw === 'string') {
        const decoded = decoder.decodePacket(msg.raw, channelKeys);
        const observerId = parts[2] || null;
        const region = parts[1] || null;

        const packetId = db.insertPacket({
          raw_hex: msg.raw,
          timestamp: now,
          observer_id: observerId,
          snr: msg.SNR ?? null,
          rssi: msg.RSSI ?? null,
          hash: computeContentHash(msg.raw),
          route_type: decoded.header.routeType,
          payload_type: decoded.header.payloadType,
          payload_version: decoded.header.payloadVersion,
          path_json: JSON.stringify(decoded.path.hops),
          decoded_json: JSON.stringify(decoded.payload),
        });

        if (decoded.path.hops.length > 0) {
          db.insertPath(packetId, decoded.path.hops);
          // Auto-create stub nodes from 2+ byte path hops
          autoLearnHopNodes(decoded.path.hops, now);
        }

        if (decoded.header.payloadTypeName === 'ADVERT' && decoded.payload.pubKey) {
          const p = decoded.payload;
          const role = p.flags ? (p.flags.repeater ? 'repeater' : p.flags.room ? 'room' : p.flags.sensor ? 'sensor' : 'companion') : 'companion';
          db.upsertNode({ public_key: p.pubKey, name: p.name || null, role, lat: p.lat, lon: p.lon, last_seen: now });
        }

        if (observerId) {
          db.upsertObserver({ id: observerId, iata: region });
        }

        const broadcastData = { id: packetId, raw: msg.raw, decoded, snr: msg.SNR, rssi: msg.RSSI, hash: msg.hash, observer: observerId };
        broadcast({ type: 'packet', data: broadcastData });

        if (decoded.header.payloadTypeName === 'GRP_TXT') {
          broadcast({ type: 'message', data: broadcastData });
        }
        return;
      }

      // --- Format 2: Companion bridge (ipnet-mesh/meshcore-mqtt) ---
      // Topics: meshcore/advertisement, meshcore/message/channel/<n>, meshcore/message/direct/<id>, etc.
      // Skip status/connection topics
      if (topic === 'meshcore/status' || topic === 'meshcore/events/connection') return;

      // Handle self_info - local node identity
      if (topic === 'meshcore/self_info') {
        const info = msg.payload || msg;
        const pubKey = info.pubkey || info.pub_key || info.public_key;
        if (pubKey) {
          db.upsertNode({ public_key: pubKey, name: info.name || 'L1 Pro (Local)', role: info.role || 'companion', lat: info.lat ?? null, lon: info.lon ?? null, last_seen: now });
        }
        return;
      }

      // Extract event type from topic
      const eventType = parts.slice(1).join('/');

      // Handle advertisements
      if (topic === 'meshcore/advertisement') {
        const advert = msg.payload || msg;
        if (advert.pubkey || advert.pub_key || advert.public_key || advert.name) {
          const pubKey = advert.pubkey || advert.pub_key || advert.public_key || `node-${(advert.name||'unknown').toLowerCase().replace(/[^a-z0-9]/g, '')}`;
          const name = advert.name || advert.node_name || null;
          const lat = advert.lat ?? advert.latitude ?? null;
          const lon = advert.lon ?? advert.lng ?? advert.longitude ?? null;
          const role = advert.role || (advert.flags?.repeater ? 'repeater' : advert.flags?.room ? 'room' : 'companion');
          db.upsertNode({ public_key: pubKey, name, role, lat, lon, last_seen: now });
          
          const packetId = db.insertPacket({
            raw_hex: null,
            timestamp: now,
            observer_id: 'companion',
            observer_name: 'L1 Pro (BLE)',
            snr: advert.SNR ?? advert.snr ?? null,
            rssi: advert.RSSI ?? advert.rssi ?? null,
            hash: 'advert',
            route_type: 1, // FLOOD
            payload_type: 4, // ADVERT
            payload_version: 0,
            path_json: JSON.stringify([]),
            decoded_json: JSON.stringify(advert),
          });
          broadcast({ type: 'packet', data: { id: packetId, decoded: { header: { payloadTypeName: 'ADVERT' }, payload: advert } } });
        }
        return;
      }

      // Handle channel messages
      if (topic.startsWith('meshcore/message/channel/')) {
        const channelMsg = msg.payload || msg;
        const channelIdx = channelMsg.channel_idx ?? msg.attributes?.channel_idx ?? topic.split('/').pop();
        const channelHash = `ch${channelIdx}`;
        // Extract sender name from "Name: message" format
        const senderName = channelMsg.text?.split(':')[0] || null;
        // Create/update node for sender
        if (senderName) {
          const senderKey = `sender-${senderName.toLowerCase().replace(/[^a-z0-9]/g, '')}`;
          db.upsertNode({ public_key: senderKey, name: senderName, role: 'companion', lat: null, lon: null, last_seen: now });
        }
        const packetId = db.insertPacket({
          raw_hex: null,
          timestamp: now,
          observer_id: 'companion',
          observer_name: 'L1 Pro (BLE)',
          snr: channelMsg.SNR ?? channelMsg.snr ?? null,
          rssi: channelMsg.RSSI ?? channelMsg.rssi ?? null,
          hash: channelHash,
          route_type: 1,
          payload_type: 5, // GRP_TXT
          payload_version: 0,
          path_json: JSON.stringify([]),
          decoded_json: JSON.stringify(channelMsg),
        });
        broadcast({ type: 'packet', data: { id: packetId, decoded: { header: { payloadTypeName: 'GRP_TXT' }, payload: channelMsg } } });
        broadcast({ type: 'message', data: { id: packetId, decoded: { header: { payloadTypeName: 'GRP_TXT' }, payload: channelMsg } } });
        return;
      }

      // Handle direct messages
      if (topic.startsWith('meshcore/message/direct/')) {
        const dm = msg.payload || msg;
        const packetId = db.insertPacket({
          raw_hex: null,
          timestamp: dm.timestamp || now,
          observer_id: 'companion',
          snr: dm.snr ?? null,
          rssi: dm.rssi ?? null,
          hash: null,
          route_type: 0,
          payload_type: 2, // TXT_MSG
          payload_version: 0,
          path_json: JSON.stringify(dm.hops || []),
          decoded_json: JSON.stringify(dm),
        });
        broadcast({ type: 'packet', data: { id: packetId, decoded: { header: { payloadTypeName: 'TXT_MSG' }, payload: dm } } });
        return;
      }

      // Handle traceroute
      if (topic.startsWith('meshcore/traceroute/')) {
        const trace = msg.payload || msg;
        const packetId = db.insertPacket({
          raw_hex: null,
          timestamp: now,
          observer_id: 'companion',
          snr: null,
          rssi: null,
          hash: null,
          route_type: 1,
          payload_type: 8, // PATH/TRACE
          payload_version: 0,
          path_json: JSON.stringify(trace.hops || trace.path || []),
          decoded_json: JSON.stringify(trace),
        });
        broadcast({ type: 'packet', data: { id: packetId, decoded: { header: { payloadTypeName: 'TRACE' }, payload: trace } } });
        return;
      }

    } catch (e) {
      if (topic !== 'meshcore/status' && topic !== 'meshcore/events/connection') {
        console.error(`MQTT handler error [${topic}]:`, e.message);
        try { console.error('  payload:', message.toString().substring(0, 200)); } catch {}
      }
    }
  });
} catch (e) {
  console.error('MQTT connection failed (non-fatal):', e.message);
}

// --- Express ---
app.use(express.json());

// REST API

app.get('/api/stats', (req, res) => {
  const stats = db.getStats();
  // Get role counts
  const counts = {};
  for (const role of ['repeater', 'room', 'companion', 'sensor']) {
    const r = db.db.prepare(`SELECT COUNT(*) as count FROM nodes WHERE role = ?`).get(role);
    counts[role + 's'] = r.count;
  }
  res.json({ ...stats, counts });
});

app.get('/api/packets', (req, res) => {
  const { limit = 50, offset = 0, type, route, region, observer, hash, since, until, groupByHash, node } = req.query;
  
  if (groupByHash === 'true') {
    let where = [];
    let params = {};
    if (type !== undefined) { where.push('payload_type = @type'); params.type = Number(type); }
    if (route !== undefined) { where.push('route_type = @route'); params.route = Number(route); }
    if (region) { where.push('observer_id IN (SELECT id FROM observers WHERE iata = @region)'); params.region = region; }
    if (observer) { where.push('observer_id = @observer'); params.observer = observer; }
    if (hash) { where.push('hash = @hash'); params.hash = hash; }
    if (since) { where.push('timestamp > @since'); params.since = since; }
    if (until) { where.push('timestamp < @until'); params.until = until; }
    if (node) { where.push("(decoded_json LIKE @nodePattern OR decoded_json LIKE @nodeNamePattern)"); params.nodePattern = `%${node}%`; const n = db.db.prepare('SELECT name FROM nodes WHERE public_key = ?').get(node); params.nodeNamePattern = n ? `%${n.name}%` : `%${node}%`; }
    const clause = where.length ? 'WHERE ' + where.join(' AND ') : '';
    const packets = db.db.prepare(`SELECT hash, COUNT(DISTINCT observer_id) as observer_count, COUNT(*) as count, MAX(timestamp) as latest, (SELECT observer_id FROM packets pObs WHERE pObs.hash = packets.hash ORDER BY pObs.timestamp ASC LIMIT 1) as observer_id, (SELECT observer_name FROM packets pOn WHERE pOn.hash = packets.hash ORDER BY pOn.timestamp ASC LIMIT 1) as observer_name, (SELECT path_json FROM packets p2 WHERE p2.hash = packets.hash ORDER BY p2.timestamp DESC LIMIT 1) as path_json, (SELECT payload_type FROM packets p3 WHERE p3.hash = packets.hash ORDER BY p3.timestamp DESC LIMIT 1) as payload_type, (SELECT raw_hex FROM packets p4 WHERE p4.hash = packets.hash ORDER BY p4.timestamp DESC LIMIT 1) as raw_hex, (SELECT decoded_json FROM packets p5 WHERE p5.hash = packets.hash ORDER BY p5.timestamp DESC LIMIT 1) as decoded_json FROM packets ${clause} GROUP BY hash ORDER BY latest DESC LIMIT @limit OFFSET @offset`).all({ ...params, limit: Number(limit), offset: Number(offset) });
    const total = db.db.prepare(`SELECT COUNT(DISTINCT hash) as count FROM packets ${clause}`).get(params).count;
    return res.json({ packets, total });
  }

  let where = [];
  let params = {};
  if (type !== undefined) { where.push('payload_type = @type'); params.type = Number(type); }
  if (route !== undefined) { where.push('route_type = @route'); params.route = Number(route); }
  if (region) { where.push('observer_id IN (SELECT id FROM observers WHERE iata = @region)'); params.region = region; }
  if (observer) { where.push('observer_id = @observer'); params.observer = observer; }
  if (hash) { where.push('hash = @hash'); params.hash = hash; }
  if (since) { where.push('timestamp > @since'); params.since = since; }
  if (until) { where.push('timestamp < @until'); params.until = until; }
  if (node) { where.push("(decoded_json LIKE @nodePattern OR decoded_json LIKE @nodeNamePattern)"); params.nodePattern = `%${node}%`; const nn = db.db.prepare('SELECT name FROM nodes WHERE public_key = ?').get(node); params.nodeNamePattern = nn ? `%${nn.name}%` : `%${node}%`; }
  const clause = where.length ? 'WHERE ' + where.join(' AND ') : '';
  const packets = db.db.prepare(`SELECT * FROM packets ${clause} ORDER BY timestamp DESC LIMIT @limit OFFSET @offset`).all({ ...params, limit: Number(limit), offset: Number(offset) });
  const total = db.db.prepare(`SELECT COUNT(*) as count FROM packets ${clause}`).get(params).count;
  res.json({ packets, total });
});

// Lightweight endpoint: just timestamps for timeline sparkline
app.get('/api/packets/timestamps', (req, res) => {
  const { since } = req.query;
  if (!since) return res.status(400).json({ error: 'since required' });
  const rows = db.db.prepare('SELECT timestamp FROM packets WHERE timestamp > ? ORDER BY timestamp ASC').all(since);
  res.json(rows.map(r => r.timestamp));
});

app.get('/api/packets/:id', (req, res) => {
  const packet = db.getPacket(Number(req.params.id));
  if (!packet) return res.status(404).json({ error: 'Not found' });

  const pathHops = packet.paths || [];
  let decoded;
  try { decoded = JSON.parse(packet.decoded_json); } catch { decoded = null; }

  // Build byte breakdown
  const breakdown = buildBreakdown(packet.raw_hex, decoded);

  res.json({ packet, path: pathHops, breakdown });
});

function buildBreakdown(rawHex, decoded) {
  if (!rawHex) return {};
  const buf = Buffer.from(rawHex, 'hex');
  const ranges = [];

  // Header
  ranges.push({ start: 0, end: 0, color: 'red', label: 'Header' });

  if (buf.length < 2) return { ranges };

  // Path length byte
  ranges.push({ start: 1, end: 1, color: 'orange', label: 'Path Length' });

  const header = decoder.decodePacket(rawHex, channelKeys);
  let offset = 2;

  // Transport codes
  if (header.transportCodes) {
    ranges.push({ start: 2, end: 5, color: 'blue', label: 'Transport Codes' });
    offset = 6;
  }

  // Path data
  const pathByte = buf[1];
  const hashSize = (pathByte >> 6) + 1;
  const hashCount = pathByte & 0x3F;
  const pathBytes = hashSize * hashCount;
  if (pathBytes > 0) {
    ranges.push({ start: offset, end: offset + pathBytes - 1, color: 'green', label: 'Path' });
  }
  const payloadStart = offset + pathBytes;

  // Payload
  if (payloadStart < buf.length) {
    ranges.push({ start: payloadStart, end: buf.length - 1, color: 'yellow', label: 'Payload' });

    // Sub-ranges for ADVERT
    if (decoded && decoded.type === 'ADVERT') {
      const ps = payloadStart;
      const subRanges = [];
      subRanges.push({ start: ps, end: ps + 31, color: '#FFD700', label: 'PubKey' });
      subRanges.push({ start: ps + 32, end: ps + 35, color: '#FFA500', label: 'Timestamp' });
      subRanges.push({ start: ps + 36, end: ps + 99, color: '#FF6347', label: 'Signature' });
      if (buf.length > ps + 100) {
        subRanges.push({ start: ps + 100, end: ps + 100, color: '#7FFFD4', label: 'Flags' });
        let off = ps + 101;
        const flags = buf[ps + 100];
        if (flags & 0x10 && buf.length >= off + 8) {
          subRanges.push({ start: off, end: off + 3, color: '#87CEEB', label: 'Latitude' });
          subRanges.push({ start: off + 4, end: off + 7, color: '#87CEEB', label: 'Longitude' });
          off += 8;
        }
        if (flags & 0x80 && off < buf.length) {
          subRanges.push({ start: off, end: buf.length - 1, color: '#DDA0DD', label: 'Name' });
        }
      }
      ranges.push(...subRanges);
    }
  }

  return { ranges };
}

// Decode-only endpoint (no DB insert)
app.post('/api/decode', (req, res) => {
  try {
    const { hex } = req.body;
    if (!hex) return res.status(400).json({ error: 'hex is required' });
    const decoded = decoder.decodePacket(hex.trim().replace(/\s+/g, ''), channelKeys);
    res.json({ decoded });
  } catch (e) {
    res.status(400).json({ error: e.message });
  }
});

app.post('/api/packets', (req, res) => {
  try {
    const { hex, observer, snr, rssi, region, hash } = req.body;
    if (!hex) return res.status(400).json({ error: 'hex is required' });

    const decoded = decoder.decodePacket(hex, channelKeys);
    const now = new Date().toISOString();

    const packetId = db.insertPacket({
      raw_hex: hex.toUpperCase(),
      timestamp: now,
      observer_id: observer || null,
      snr: snr ?? null,
      rssi: rssi ?? null,
      hash: computeContentHash(hex),
      route_type: decoded.header.routeType,
      payload_type: decoded.header.payloadType,
      payload_version: decoded.header.payloadVersion,
      path_json: JSON.stringify(decoded.path.hops),
      decoded_json: JSON.stringify(decoded.payload),
    });

    if (decoded.path.hops.length > 0) {
      db.insertPath(packetId, decoded.path.hops);
      autoLearnHopNodes(decoded.path.hops, new Date().toISOString());
    }

    if (decoded.header.payloadTypeName === 'ADVERT' && decoded.payload.pubKey) {
      const p = decoded.payload;
      const role = p.flags ? (p.flags.repeater ? 'repeater' : p.flags.room ? 'room' : p.flags.sensor ? 'sensor' : 'companion') : 'companion';
      db.upsertNode({ public_key: p.pubKey, name: p.name || null, role, lat: p.lat, lon: p.lon, last_seen: now });
    }

    if (observer) {
      db.upsertObserver({ id: observer, iata: region || null });
    }

    broadcast({ type: 'packet', data: { id: packetId, decoded } });

    res.json({ id: packetId, decoded });
  } catch (e) {
    res.status(400).json({ error: e.message });
  }
});

app.get('/api/nodes', (req, res) => {
  const { limit = 50, offset = 0, role, region, lastHeard, sortBy = 'lastSeen', search } = req.query;

  let where = [];
  let params = {};

  if (role) { where.push('role = @role'); params.role = role; }
  if (search) { where.push('name LIKE @search'); params.search = `%${search}%`; }
  if (lastHeard) {
    const durations = { '1h': 3600000, '6h': 21600000, '24h': 86400000, '7d': 604800000, '30d': 2592000000 };
    const ms = durations[lastHeard];
    if (ms) { where.push('last_seen > @since'); params.since = new Date(Date.now() - ms).toISOString(); }
  }

  const clause = where.length ? 'WHERE ' + where.join(' AND ') : '';
  const sortMap = { name: 'name ASC', lastSeen: 'last_seen DESC', packetCount: 'advert_count DESC' };
  const order = sortMap[sortBy] || 'last_seen DESC';

  const nodes = db.db.prepare(`SELECT * FROM nodes ${clause} ORDER BY ${order} LIMIT @limit OFFSET @offset`).all({ ...params, limit: Number(limit), offset: Number(offset) });
  const total = db.db.prepare(`SELECT COUNT(*) as count FROM nodes ${clause}`).get(params).count;

  const counts = {};
  for (const r of ['repeater', 'room', 'companion', 'sensor']) {
    counts[r + 's'] = db.db.prepare(`SELECT COUNT(*) as count FROM nodes WHERE role = ?`).get(r).count;
  }

  res.json({ nodes, total, counts });
});

app.get('/api/nodes/search', (req, res) => {
  const q = req.query.q || '';
  if (!q.trim()) return res.json({ nodes: [] });
  const nodes = db.searchNodes(q.trim());
  res.json({ nodes });
});

app.get('/api/nodes/:pubkey', (req, res) => {
  const node = db.getNode(req.params.pubkey);
  if (!node) return res.status(404).json({ error: 'Not found' });
  const recentAdverts = node.recentPackets || [];
  delete node.recentPackets;
  res.json({ node, recentAdverts });
});

// --- Analytics API ---
// --- RF Analytics ---
app.get('/api/analytics/rf', (req, res) => {
  const PTYPES = { 0:'REQ',1:'RESPONSE',2:'TXT_MSG',3:'ACK',4:'ADVERT',5:'GRP_TXT',7:'ANON_REQ',8:'PATH',9:'TRACE',11:'CONTROL' };
  const packets = db.db.prepare(`SELECT snr, rssi, payload_type, timestamp, raw_hex FROM packets WHERE snr IS NOT NULL`).all();

  const snrVals = packets.map(p => p.snr).filter(v => v != null);
  const rssiVals = packets.map(p => p.rssi).filter(v => v != null);
  const packetSizes = packets.filter(p => p.raw_hex).map(p => p.raw_hex.length / 2);

  const sorted = arr => [...arr].sort((a, b) => a - b);
  const median = arr => { const s = sorted(arr); return s.length ? s[Math.floor(s.length/2)] : 0; };
  const stddev = (arr, avg) => Math.sqrt(arr.reduce((s, v) => s + (v - avg) ** 2, 0) / Math.max(arr.length, 1));

  const snrAvg = snrVals.reduce((a, b) => a + b, 0) / Math.max(snrVals.length, 1);
  const rssiAvg = rssiVals.reduce((a, b) => a + b, 0) / Math.max(rssiVals.length, 1);

  // Packets per hour
  const hourBuckets = {};
  packets.forEach(p => {
    const hr = p.timestamp.slice(0, 13);
    hourBuckets[hr] = (hourBuckets[hr] || 0) + 1;
  });
  const packetsPerHour = Object.entries(hourBuckets).sort().map(([hour, count]) => ({ hour, count }));

  // Payload type distribution
  const typeBuckets = {};
  packets.forEach(p => { typeBuckets[p.payload_type] = (typeBuckets[p.payload_type] || 0) + 1; });
  const payloadTypes = Object.entries(typeBuckets)
    .map(([type, count]) => ({ type: +type, name: PTYPES[type] || `UNK(${type})`, count }))
    .sort((a, b) => b.count - a.count);

  // SNR by payload type
  const snrByType = {};
  packets.forEach(p => {
    const name = PTYPES[p.payload_type] || `UNK(${p.payload_type})`;
    if (!snrByType[name]) snrByType[name] = { vals: [] };
    snrByType[name].vals.push(p.snr);
  });
  const snrByTypeArr = Object.entries(snrByType).map(([name, d]) => ({
    name, count: d.vals.length,
    avg: d.vals.reduce((a, b) => a + b, 0) / d.vals.length,
    min: Math.min(...d.vals), max: Math.max(...d.vals)
  })).sort((a, b) => b.count - a.count);

  // Signal over time
  const sigTime = {};
  packets.forEach(p => {
    const hr = p.timestamp.slice(0, 13);
    if (!sigTime[hr]) sigTime[hr] = { snrs: [], count: 0 };
    sigTime[hr].snrs.push(p.snr);
    sigTime[hr].count++;
  });
  const signalOverTime = Object.entries(sigTime).sort().map(([hour, d]) => ({
    hour, count: d.count, avgSnr: d.snrs.reduce((a, b) => a + b, 0) / d.snrs.length
  }));

  // Scatter data (SNR vs RSSI)
  const scatterData = packets.filter(p => p.snr != null && p.rssi != null).map(p => ({ snr: p.snr, rssi: p.rssi }));

  const times = packets.map(p => new Date(p.timestamp).getTime());
  const timeSpanHours = times.length ? (Math.max(...times) - Math.min(...times)) / 3600000 : 0;

  res.json({
    totalPackets: packets.length,
    snr: { min: Math.min(...snrVals), max: Math.max(...snrVals), avg: snrAvg, median: median(snrVals), stddev: stddev(snrVals, snrAvg) },
    rssi: { min: Math.min(...rssiVals), max: Math.max(...rssiVals), avg: rssiAvg, median: median(rssiVals), stddev: stddev(rssiVals, rssiAvg) },
    snrValues: snrVals, rssiValues: rssiVals, packetSizes,
    minPacketSize: packetSizes.length ? Math.min(...packetSizes) : 0,
    maxPacketSize: packetSizes.length ? Math.max(...packetSizes) : 0,
    avgPacketSize: packetSizes.length ? Math.round(packetSizes.reduce((a, b) => a + b, 0) / packetSizes.length) : 0,
    packetsPerHour, payloadTypes, snrByType: snrByTypeArr, signalOverTime, scatterData, timeSpanHours
  });
});

// --- Topology Analytics ---
app.get('/api/analytics/topology', (req, res) => {
  const packets = db.db.prepare(`SELECT path_json, snr, decoded_json, observer_id FROM packets WHERE path_json IS NOT NULL AND path_json != '[]'`).all();
  const allNodes = db.db.prepare('SELECT public_key, name, lat, lon FROM nodes WHERE name IS NOT NULL').all();
  const resolveHop = (hop, contextPositions) => {
    const h = hop.toLowerCase();
    const candidates = allNodes.filter(n => n.public_key.toLowerCase().startsWith(h));
    if (candidates.length === 0) return null;
    if (candidates.length === 1) return { name: candidates[0].name, pubkey: candidates[0].public_key };
    // Disambiguate by proximity to context positions
    if (contextPositions && contextPositions.length > 0) {
      const cLat = contextPositions.reduce((s, p) => s + p.lat, 0) / contextPositions.length;
      const cLon = contextPositions.reduce((s, p) => s + p.lon, 0) / contextPositions.length;
      const withLoc = candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
      if (withLoc.length) {
        withLoc.sort((a, b) => Math.hypot(a.lat - cLat, a.lon - cLon) - Math.hypot(b.lat - cLat, b.lon - cLon));
        return { name: withLoc[0].name, pubkey: withLoc[0].public_key };
      }
    }
    return { name: candidates[0].name, pubkey: candidates[0].public_key };
  };

  // Hop distribution
  const hopCounts = {};
  const allHopsList = [];
  const hopSnr = {};
  const hopFreq = {};
  const pairFreq = {};
  packets.forEach(p => {
    const hops = JSON.parse(p.path_json);
    const n = hops.length;
    hopCounts[n] = (hopCounts[n] || 0) + 1;
    allHopsList.push(n);
    if (!hopSnr[n]) hopSnr[n] = [];
    if (p.snr != null) hopSnr[n].push(p.snr);
    hops.forEach(h => { hopFreq[h] = (hopFreq[h] || 0) + 1; });
    for (let i = 0; i < hops.length - 1; i++) {
      const pair = [hops[i], hops[i + 1]].sort().join('|');
      pairFreq[pair] = (pairFreq[pair] || 0) + 1;
    }
  });

  const hopDistribution = Object.entries(hopCounts)
    .map(([hops, count]) => ({ hops: +hops, count }))
    .filter(h => h.hops <= 25)
    .sort((a, b) => a.hops - b.hops);

  const avgHops = allHopsList.length ? allHopsList.reduce((a, b) => a + b, 0) / allHopsList.length : 0;
  const medianHops = allHopsList.length ? [...allHopsList].sort((a, b) => a - b)[Math.floor(allHopsList.length / 2)] : 0;
  const maxHops = allHopsList.length ? Math.max(...allHopsList) : 0;

  // Top repeaters
  const topRepeaters = Object.entries(hopFreq)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 20)
    .map(([hop, count]) => {
      const resolved = resolveHop(hop);
      return { hop, count, name: resolved?.name || null, pubkey: resolved?.pubkey || null };
    });

  // Top pairs
  const topPairs = Object.entries(pairFreq)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 15)
    .map(([pair, count]) => {
      const [a, b] = pair.split('|');
      const rA = resolveHop(a), rB = resolveHop(b);
      return { hopA: a, hopB: b, count, nameA: rA?.name, nameB: rB?.name, pubkeyA: rA?.pubkey, pubkeyB: rB?.pubkey };
    });

  // Hops vs SNR
  const hopsVsSnr = Object.entries(hopSnr)
    .filter(([h]) => +h <= 20)
    .map(([hops, snrs]) => ({
      hops: +hops, count: snrs.length,
      avgSnr: snrs.reduce((a, b) => a + b, 0) / snrs.length
    }))
    .sort((a, b) => a.hops - b.hops);

  // Reachability: per-observer hop distances + cross-observer comparison + best path
  const observers = db.db.prepare(`SELECT DISTINCT observer_id, observer_name FROM packets WHERE path_json IS NOT NULL AND path_json != '[]'`).all();

  // Per-observer: node → min hop distance seen from that observer
  const perObserver = {}; // observer_id → { hop_hex → { minDist, maxDist, count } }
  const bestPath = {};    // hop_hex → { minDist, observer }
  const crossObserver = {}; // hop_hex → [ { observer_id, observer_name, minDist, count } ]

  packets.forEach(p => {
    const obsId = p.observer_id;
    if (!perObserver[obsId]) perObserver[obsId] = {};
    const hops = JSON.parse(p.path_json);
    hops.forEach((h, i) => {
      const dist = hops.length - i;
      if (!perObserver[obsId][h]) perObserver[obsId][h] = { minDist: dist, maxDist: dist, count: 0 };
      const entry = perObserver[obsId][h];
      entry.minDist = Math.min(entry.minDist, dist);
      entry.maxDist = Math.max(entry.maxDist, dist);
      entry.count++;
    });
  });

  // Build cross-observer and best-path from perObserver
  for (const [obsId, nodes] of Object.entries(perObserver)) {
    const obsName = observers.find(o => o.observer_id === obsId)?.observer_name || obsId;
    for (const [hop, data] of Object.entries(nodes)) {
      // Cross-observer
      if (!crossObserver[hop]) crossObserver[hop] = [];
      crossObserver[hop].push({ observer_id: obsId, observer_name: obsName, minDist: data.minDist, count: data.count });
      // Best path
      if (!bestPath[hop] || data.minDist < bestPath[hop].minDist) {
        bestPath[hop] = { minDist: data.minDist, observer_id: obsId, observer_name: obsName };
      }
    }
  }

  // Format per-observer reachability (grouped by distance)
  const perObserverReach = {};
  for (const [obsId, nodes] of Object.entries(perObserver)) {
    const obsInfo = observers.find(o => o.observer_id === obsId);
    const byDist = {};
    for (const [hop, data] of Object.entries(nodes)) {
      const d = data.minDist;
      if (d > 15) continue;
      if (!byDist[d]) byDist[d] = [];
      const r = resolveHop(hop);
      byDist[d].push({ hop, name: r?.name || null, pubkey: r?.pubkey || null, count: data.count, distRange: data.minDist === data.maxDist ? null : `${data.minDist}-${data.maxDist}` });
    }
    perObserverReach[obsId] = {
      observer_name: obsInfo?.observer_name || obsId,
      rings: Object.entries(byDist).map(([dist, nodes]) => ({ hops: +dist, nodes: nodes.sort((a, b) => b.count - a.count) })).sort((a, b) => a.hops - b.hops)
    };
  }

  // Cross-observer: nodes seen by multiple observers
  const multiObsNodes = Object.entries(crossObserver)
    .filter(([, obs]) => obs.length > 1)
    .map(([hop, obs]) => {
      const r = resolveHop(hop);
      return { hop, name: r?.name || null, pubkey: r?.pubkey || null, observers: obs.sort((a, b) => a.minDist - b.minDist) };
    })
    .sort((a, b) => b.observers.length - a.observers.length)
    .slice(0, 50);

  // Best path: sorted by distance
  const bestPathList = Object.entries(bestPath)
    .map(([hop, data]) => {
      const r = resolveHop(hop);
      return { hop, name: r?.name || null, pubkey: r?.pubkey || null, ...data };
    })
    .sort((a, b) => a.minDist - b.minDist)
    .slice(0, 50);

  res.json({
    uniqueNodes: new Set(Object.keys(hopFreq)).size,
    avgHops, medianHops, maxHops,
    hopDistribution, topRepeaters, topPairs, hopsVsSnr,
    observers: observers.map(o => ({ id: o.observer_id, name: o.observer_name || o.observer_id })),
    perObserverReach,
    multiObsNodes,
    bestPathList
  });
});

// --- Channel Analytics ---
app.get('/api/analytics/channels', (req, res) => {
  const packets = db.db.prepare(`SELECT decoded_json, timestamp FROM packets WHERE payload_type = 5 AND decoded_json IS NOT NULL`).all();

  const channels = {};
  const senderCounts = {};
  const msgLengths = [];
  const timeline = {};

  packets.forEach(p => {
    try {
      const d = JSON.parse(p.decoded_json);
      const hash = d.channelHash || d.channel_hash || '?';
      const name = d.channelName || (d.type === 'CHAN' ? (d.channel || `ch${hash}`) : `ch${hash}`);
      const encrypted = !d.text && !d.sender;

      if (!channels[hash]) channels[hash] = { hash, name, messages: 0, senders: new Set(), lastActivity: p.timestamp, encrypted };
      channels[hash].messages++;
      channels[hash].lastActivity = p.timestamp;
      if (!encrypted) channels[hash].encrypted = false;

      if (d.sender) {
        channels[hash].senders.add(d.sender);
        senderCounts[d.sender] = (senderCounts[d.sender] || 0) + 1;
      }
      if (d.text) msgLengths.push(d.text.length);

      // Timeline
      const hr = p.timestamp.slice(0, 13);
      const key = hr + '|' + (name || `ch${hash}`);
      timeline[key] = (timeline[key] || 0) + 1;
    } catch {}
  });

  const channelList = Object.values(channels)
    .map(c => ({ ...c, senders: c.senders.size }))
    .sort((a, b) => b.messages - a.messages);

  const topSenders = Object.entries(senderCounts)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 15)
    .map(([name, count]) => ({ name, count }));

  const channelTimeline = Object.entries(timeline)
    .map(([key, count]) => {
      const [hour, channel] = key.split('|');
      return { hour, channel, count };
    })
    .sort((a, b) => a.hour.localeCompare(b.hour));

  res.json({
    activeChannels: channelList.length,
    decryptable: channelList.filter(c => !c.encrypted).length,
    channels: channelList,
    topSenders,
    channelTimeline,
    msgLengths
  });
});

app.get('/api/analytics/hash-sizes', (req, res) => {
  // Get all packets with raw_hex and non-empty paths, extract hash_size from path_length byte
  const packets = db.db.prepare(`
    SELECT raw_hex, path_json, timestamp, payload_type, decoded_json
    FROM packets
    WHERE raw_hex IS NOT NULL AND path_json IS NOT NULL AND path_json != '[]'
    ORDER BY timestamp DESC
  `).all();

  const distribution = { 1: 0, 2: 0, 3: 0 };
  const byHour = {};     // hour bucket → { 1: n, 2: n, 3: n }
  const byNode = {};     // node name/prefix → { hashSize, packets, lastSeen }
  const uniqueHops = {}; // hop hex → { size, count, resolvedName }

  // Resolve all known nodes for hop matching
  const allNodes = db.db.prepare('SELECT public_key, name FROM nodes WHERE name IS NOT NULL').all();

  for (const p of packets) {
    const pathByte = parseInt(p.raw_hex.slice(2, 4), 16);
    // Check if this packet has transport codes (route type 0 or 3)
    const header = parseInt(p.raw_hex.slice(0, 2), 16);
    const routeType = header & 0x03;
    let pathByteIdx = 1; // normally byte index 1
    if (routeType === 0 || routeType === 3) pathByteIdx = 5; // skip 4 transport code bytes
    const actualPathByte = parseInt(p.raw_hex.slice(pathByteIdx * 2, pathByteIdx * 2 + 2), 16);

    const hashSize = ((actualPathByte >> 6) & 0x3) + 1;
    const hashCount = actualPathByte & 0x3F;
    if (hashSize > 3) continue; // reserved

    distribution[hashSize] = (distribution[hashSize] || 0) + 1;

    // Hourly buckets
    const hour = p.timestamp.slice(0, 13); // "2026-03-18T04"
    if (!byHour[hour]) byHour[hour] = { 1: 0, 2: 0, 3: 0 };
    byHour[hour][hashSize]++;

    // Track unique hops with their sizes
    const hops = JSON.parse(p.path_json);
    for (const hop of hops) {
      if (!uniqueHops[hop]) {
        const hopLower = hop.toLowerCase();
        const match = allNodes.find(n => n.public_key.toLowerCase().startsWith(hopLower));
        uniqueHops[hop] = { size: Math.ceil(hop.length / 2), count: 0, name: match?.name || null, pubkey: match?.public_key || null };
      }
      uniqueHops[hop].count++;
    }

    // Try to identify originator from decoded_json for advert packets
    if (p.payload_type === 4) {
      try {
        const d = JSON.parse(p.decoded_json);
        const name = d.name || (d.pubKey || d.public_key || '').slice(0, 8);
        if (name) {
          if (!byNode[name]) byNode[name] = { hashSize, packets: 0, lastSeen: p.timestamp, pubkey: d.pubKey || d.public_key || null };
          byNode[name].packets++;
          byNode[name].hashSize = hashSize;
          byNode[name].lastSeen = p.timestamp;
        }
      } catch {}
    }
  }

  // Sort hourly data
  const hourly = Object.entries(byHour)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([hour, sizes]) => ({ hour, ...sizes }));

  // Top hops by frequency
  const topHops = Object.entries(uniqueHops)
    .sort(([, a], [, b]) => b.count - a.count)
    .slice(0, 50)
    .map(([hex, data]) => ({ hex, ...data }));

  // Nodes that use non-default (>1 byte) hash sizes
  const multiByteNodes = Object.entries(byNode)
    .filter(([, v]) => v.hashSize > 1)
    .sort(([, a], [, b]) => b.packets - a.packets)
    .map(([name, data]) => ({ name, ...data }));

  res.json({
    total: packets.length,
    distribution,
    hourly,
    topHops,
    multiByteNodes
  });
});

// Resolve path hop hex prefixes to node names
app.get('/api/resolve-hops', (req, res) => {
  const hops = (req.query.hops || '').split(',').filter(Boolean);
  const observerId = req.query.observer || null;
  if (!hops.length) return res.json({ resolved: {} });

  const allNodes = db.db.prepare('SELECT public_key, name, lat, lon FROM nodes WHERE name IS NOT NULL').all();

  // Build observer geographic center from nodes it typically hears
  let observerLat = null, observerLon = null;
  if (observerId) {
    const obsNodes = db.db.prepare(`
      SELECT DISTINCT n.lat, n.lon FROM packets p
      JOIN nodes n ON p.decoded_json LIKE '%' || n.public_key || '%'
      WHERE p.observer_id = ? AND n.lat IS NOT NULL AND n.lat != 0 AND n.lon != 0
      LIMIT 50
    `).all(observerId);
    if (obsNodes.length) {
      observerLat = obsNodes.reduce((s, n) => s + n.lat, 0) / obsNodes.length;
      observerLon = obsNodes.reduce((s, n) => s + n.lon, 0) / obsNodes.length;
    }
  }

  const resolved = {};
  // First pass: find all candidates for each hop
  for (const hop of hops) {
    const hopLower = hop.toLowerCase();
    const candidates = allNodes.filter(n => n.public_key.toLowerCase().startsWith(hopLower));
    if (candidates.length === 0) {
      resolved[hop] = { name: null, candidates: [] };
    } else if (candidates.length === 1) {
      resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key, candidates: [{ name: candidates[0].name, pubkey: candidates[0].public_key }] };
    } else {
      resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key, ambiguous: true, candidates: candidates.map(c => ({ name: c.name, pubkey: c.public_key, lat: c.lat, lon: c.lon })) };
    }
  }

  // Second pass: disambiguate using geographic context
  // Collect known positions from unambiguous hops
  const knownPositions = [];
  for (const hop of hops) {
    const r = resolved[hop];
    if (r && !r.ambiguous && r.pubkey) {
      const node = allNodes.find(n => n.public_key === r.pubkey);
      if (node && node.lat && node.lon && !(node.lat === 0 && node.lon === 0)) {
        knownPositions.push({ lat: node.lat, lon: node.lon });
      }
    }
  }
  if (observerLat != null) knownPositions.push({ lat: observerLat, lon: observerLon });

  if (knownPositions.length > 0) {
    const centerLat = knownPositions.reduce((s, p) => s + p.lat, 0) / knownPositions.length;
    const centerLon = knownPositions.reduce((s, p) => s + p.lon, 0) / knownPositions.length;
    const dist = (lat, lon) => Math.sqrt((lat - centerLat) ** 2 + (lon - centerLon) ** 2);

    for (const hop of hops) {
      const r = resolved[hop];
      if (r && r.ambiguous) {
        const withLoc = r.candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
        if (withLoc.length > 0) {
          withLoc.sort((a, b) => dist(a.lat, a.lon) - dist(b.lat, b.lon));
          r.name = withLoc[0].name;
          r.pubkey = withLoc[0].pubkey;
        }
      }
    }
  }

  res.json({ resolved });
});

// Channel hash → name mapping from configured keys
const channelHashNames = {};
{
  const crypto = require('crypto');
  for (const [name, key] of Object.entries(channelKeys)) {
    const hash = crypto.createHash('sha256').update(Buffer.from(key, 'hex')).digest()[0];
    channelHashNames[hash] = name;
  }
}

app.get('/api/channels', (req, res) => {
  const packets = db.db.prepare(`SELECT * FROM packets WHERE payload_type = 5 ORDER BY timestamp DESC`).all();
  const channelMap = {};

  for (const pkt of packets) {
    let decoded;
    try { decoded = JSON.parse(pkt.decoded_json); } catch { continue; }
    const ch = decoded.channelHash !== undefined ? decoded.channelHash : decoded.channel_idx;
    if (ch === undefined) continue;
    
    const knownName = channelHashNames[ch];
    // If hash matches a known channel but decryption failed, it's a collision — separate bucket
    const isDecrypted = decoded.type === 'CHAN' || decoded.text;
    const isCollision = !!(knownName && !isDecrypted && decoded.encryptedData);
    const key = isCollision ? `unk_${ch}` : String(ch);
    
    if (!channelMap[key]) {
      channelMap[key] = {
        hash: key,
        name: isCollision ? `Unknown (hash 0x${Number(ch).toString(16).toUpperCase()})` : (knownName || `Channel 0x${Number(ch).toString(16).toUpperCase()}`),
        encrypted: isCollision || !knownName,
        lastMessage: null,
        lastSender: null,
        messageCount: 0,
        lastActivity: pkt.timestamp,
      };
    }
    channelMap[key].messageCount++;
    if (!channelMap[key].lastMessage || pkt.timestamp >= channelMap[key].lastActivity) {
      channelMap[key].lastActivity = pkt.timestamp;
      if (decoded.text) {
        const colonIdx = decoded.text.indexOf(': ');
        channelMap[key].lastMessage = colonIdx > 0 ? decoded.text.slice(colonIdx + 2) : decoded.text;
        channelMap[key].lastSender = decoded.sender || null;
      }
    }
  }

  // Also include companion bridge messages (no raw_hex, have text directly)
  const companionPkts = db.db.prepare(`SELECT * FROM packets WHERE payload_type = 5 AND raw_hex IS NULL ORDER BY timestamp DESC`).all();
  for (const pkt of companionPkts) {
    let decoded;
    try { decoded = JSON.parse(pkt.decoded_json); } catch { continue; }
    const ch = decoded.channel_idx !== undefined ? `c${decoded.channel_idx}` : null;
    if (!ch) continue;
    if (!channelMap[ch]) {
      channelMap[ch] = {
        hash: ch,
        name: `Companion Ch ${decoded.channel_idx}`,
        encrypted: false,
        lastMessage: null,
        lastSender: null,
        messageCount: 0,
        lastActivity: pkt.timestamp,
      };
    }
    // Don't double-count if already counted above
  }

  res.json({ channels: Object.values(channelMap) });
});

app.get('/api/channels/:hash/messages', (req, res) => {
  const { limit = 100, offset = 0 } = req.query;
  const channelHash = req.params.hash;
  const packets = db.db.prepare(`SELECT * FROM packets WHERE payload_type = 5 ORDER BY timestamp ASC`).all();

  // Group by message content + timestamp to deduplicate repeats
  const msgMap = new Map();
  for (const pkt of packets) {
    let decoded;
    try { decoded = JSON.parse(pkt.decoded_json); } catch { continue; }
    const rawCh = decoded.channelHash !== undefined ? decoded.channelHash : decoded.channel_idx;
    const isDecrypted = decoded.type === 'CHAN' || decoded.text;
    const knownName = channelHashNames[rawCh];
    const isCollision = !!(knownName && !isDecrypted && decoded.encryptedData);
    const ch = isCollision ? `unk_${rawCh}` : (rawCh !== undefined ? String(rawCh) : (decoded.channel_idx !== undefined ? `c${decoded.channel_idx}` : null));
    if (ch !== channelHash) continue;

    const sender = decoded.sender || (decoded.text ? decoded.text.split(': ')[0] : null) || pkt.observer_name || pkt.observer_id || 'Unknown';
    const text = decoded.text || decoded.encryptedData || '';
    const ts = decoded.sender_timestamp || pkt.timestamp;
    const dedupeKey = `${sender}:${ts}`;

    if (msgMap.has(dedupeKey)) {
      const existing = msgMap.get(dedupeKey);
      existing.repeats++;
      if (pkt.observer_name && !existing.observers.includes(pkt.observer_name)) {
        existing.observers.push(pkt.observer_name);
      }
    } else {
      // Parse sender and message from "sender: message" format
      let displaySender = sender;
      let displayText = text;
      if (decoded.text) {
        const colonIdx = decoded.text.indexOf(': ');
        if (colonIdx > 0 && colonIdx < 50) {
          displaySender = decoded.text.slice(0, colonIdx);
          displayText = decoded.text.slice(colonIdx + 2);
        }
      }
      msgMap.set(dedupeKey, {
        sender: displaySender,
        text: displayText,
        encrypted: !decoded.text && !decoded.sender,
        timestamp: pkt.timestamp,
        sender_timestamp: decoded.sender_timestamp || null,
        packetId: pkt.id,
        repeats: 1,
        observers: [pkt.observer_name || pkt.observer_id].filter(Boolean),
        hops: decoded.path_len || (pkt.path_json ? JSON.parse(pkt.path_json).length : 0),
        snr: pkt.snr || (decoded.SNR !== undefined ? decoded.SNR : null),
      });
    }
  }

  const allMessages = [...msgMap.values()];
  const total = allMessages.length;
  // Return the latest messages (tail), not the oldest (head)
  const start = Math.max(0, total - Number(limit) - Number(offset));
  const end = total - Number(offset);
  const messages = allMessages.slice(Math.max(0, start), Math.max(0, end));
  res.json({ messages, total });
});

app.get('/api/observers', (req, res) => {
  const observers = db.getObservers();
  const oneHourAgo = new Date(Date.now() - 3600000).toISOString();
  const result = observers.map(o => {
    const lastHour = db.db.prepare(`SELECT COUNT(*) as count FROM packets WHERE observer_id = ? AND timestamp > ?`).get(o.id, oneHourAgo);
    return { ...o, packetsLastHour: lastHour.count };
  });
  res.json({ observers: result });
});

app.get('/api/traces/:hash', (req, res) => {
  const packets = db.db.prepare(`SELECT observer_id, timestamp, snr, rssi FROM packets WHERE hash = ? ORDER BY timestamp`).all(req.params.hash);
  const traces = packets.map(p => ({ observer: p.observer_id, time: p.timestamp, snr: p.snr, rssi: p.rssi }));
  res.json({ traces });
});

app.get('/api/nodes/:pubkey/health', (req, res) => {
  const health = db.getNodeHealth(req.params.pubkey);
  if (!health) return res.status(404).json({ error: 'Not found' });
  res.json(health);
});

// Subpath frequency analysis
app.get('/api/analytics/subpaths', (req, res) => {
  const minLen = Math.max(2, Number(req.query.minLen) || 2);
  const maxLen = Number(req.query.maxLen) || 8;
  const packets = db.db.prepare(`SELECT path_json FROM packets WHERE path_json IS NOT NULL AND path_json != '[]'`).all();
  const allNodes = db.db.prepare('SELECT public_key, name, lat, lon FROM nodes WHERE name IS NOT NULL').all();

  // Simple resolver (no geographic context needed here — just first match)
  const nameCache = {};
  function resolveName(hop) {
    if (nameCache[hop] !== undefined) return nameCache[hop];
    const h = hop.toLowerCase();
    const m = allNodes.find(n => n.public_key.toLowerCase().startsWith(h));
    nameCache[hop] = m ? m.name : hop;
    return nameCache[hop];
  }

  const subpathCounts = {};
  let totalPaths = 0;

  for (const pkt of packets) {
    let hops;
    try { hops = JSON.parse(pkt.path_json); } catch { continue; }
    if (!Array.isArray(hops) || hops.length < 2) continue;
    totalPaths++;

    // Resolve all hops to names
    const named = hops.map(h => resolveName(h));

    // Extract all subpaths of length minLen..maxLen
    for (let len = minLen; len <= Math.min(maxLen, named.length); len++) {
      for (let start = 0; start <= named.length - len; start++) {
        const sub = named.slice(start, start + len).join(' → ');
        const raw = hops.slice(start, start + len).join(',');
        if (!subpathCounts[sub]) subpathCounts[sub] = { count: 0, raw };
        subpathCounts[sub].count++;
      }
    }
  }

  // Sort by frequency, return top results
  const limit = Number(req.query.limit) || 100;
  const ranked = Object.entries(subpathCounts)
    .map(([path, data]) => ({
      path,
      rawHops: data.raw.split(','),
      count: data.count,
      hops: path.split(' → ').length,
      pct: totalPaths > 0 ? Math.round(data.count / totalPaths * 1000) / 10 : 0
    }))
    .sort((a, b) => b.count - a.count)
    .slice(0, limit);

  res.json({ subpaths: ranked, totalPaths });
});

// Subpath detail — stats for a specific subpath (by raw hop prefixes)
app.get('/api/analytics/subpath-detail', (req, res) => {
  const rawHops = (req.query.hops || '').split(',').filter(Boolean);
  if (rawHops.length < 2) return res.json({ error: 'Need at least 2 hops' });

  const packets = db.db.prepare(`SELECT path_json, snr, rssi, timestamp, decoded_json, observer_name FROM packets WHERE path_json IS NOT NULL AND path_json != '[]'`).all();
  const allNodes = db.db.prepare('SELECT public_key, name, lat, lon FROM nodes WHERE name IS NOT NULL').all();

  const nameCache = {};
  function resolveName(hop) {
    if (nameCache[hop] !== undefined) return nameCache[hop];
    const h = hop.toLowerCase();
    const m = allNodes.find(n => n.public_key.toLowerCase().startsWith(h));
    nameCache[hop] = m ? { name: m.name, lat: m.lat, lon: m.lon, pubkey: m.public_key } : null;
    return nameCache[hop];
  }

  const matching = [];
  const parentPaths = {};
  const hourBuckets = new Array(24).fill(0);
  let snrSum = 0, snrCount = 0, rssiSum = 0, rssiCount = 0;
  const observers = {};

  for (const pkt of packets) {
    let hops;
    try { hops = JSON.parse(pkt.path_json); } catch { continue; }
    if (!Array.isArray(hops) || hops.length < rawHops.length) continue;

    // Check if rawHops appears as a contiguous subsequence
    let found = false;
    for (let i = 0; i <= hops.length - rawHops.length; i++) {
      let match = true;
      for (let j = 0; j < rawHops.length; j++) {
        if (hops[i + j].toLowerCase() !== rawHops[j].toLowerCase()) { match = false; break; }
      }
      if (match) { found = true; break; }
    }
    if (!found) continue;

    matching.push(pkt);
    const hr = new Date(pkt.timestamp).getUTCHours();
    hourBuckets[hr]++;
    if (pkt.snr != null) { snrSum += pkt.snr; snrCount++; }
    if (pkt.rssi != null) { rssiSum += pkt.rssi; rssiCount++; }
    if (pkt.observer_name) observers[pkt.observer_name] = (observers[pkt.observer_name] || 0) + 1;

    // Track full parent paths (resolved names)
    const fullPath = hops.map(h => {
      const r = resolveName(h);
      return r ? r.name : h;
    }).join(' → ');
    parentPaths[fullPath] = (parentPaths[fullPath] || 0) + 1;
  }

  // Resolve hop nodes for map
  const nodes = rawHops.map(h => {
    const r = resolveName(h);
    return { hop: h, name: r?.name || h, lat: r?.lat || null, lon: r?.lon || null, pubkey: r?.pubkey || null };
  });

  const topParents = Object.entries(parentPaths)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 15)
    .map(([path, count]) => ({ path, count }));

  const topObservers = Object.entries(observers)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 10)
    .map(([name, count]) => ({ name, count }));

  res.json({
    hops: rawHops,
    nodes,
    totalMatches: matching.length,
    firstSeen: matching.length ? matching[0].timestamp : null,
    lastSeen: matching.length ? matching[matching.length - 1].timestamp : null,
    signal: {
      avgSnr: snrCount ? Math.round(snrSum / snrCount * 10) / 10 : null,
      avgRssi: rssiCount ? Math.round(rssiSum / rssiCount) : null,
      samples: snrCount
    },
    hourDistribution: hourBuckets,
    parentPaths: topParents,
    observers: topObservers
  });
});

// Static files + SPA fallback
app.use(express.static(path.join(__dirname, 'public'), {
  etag: false,
  lastModified: false,
  setHeaders: (res, filePath) => {
    if (filePath.endsWith('.js') || filePath.endsWith('.css') || filePath.endsWith('.html')) {
      res.setHeader('Cache-Control', 'no-cache, no-store, must-revalidate');
    }
  }
}));
app.get('/{*splat}', (req, res) => {
  const indexPath = path.join(__dirname, 'public', 'index.html');
  const fs = require('fs');
  if (fs.existsSync(indexPath)) {
    res.sendFile(indexPath);
  } else {
    res.status(200).send('<!DOCTYPE html><html><body><h1>MeshCore Analyzer</h1><p>Frontend not yet built.</p></body></html>');
  }
});

// --- Start ---
server.listen(process.env.PORT || config.port, () => {
  console.log(`MeshCore Analyzer running on http://localhost:${config.port}`);
});

module.exports = { app, server, wss };
