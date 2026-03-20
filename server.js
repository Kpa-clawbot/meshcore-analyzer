'use strict';

const express = require('express');
const http = require('http');
const { WebSocketServer } = require('ws');
const mqtt = require('mqtt');
const path = require('path');
const config = require('./config.json');
const decoder = require('./decoder');
const crypto = require('crypto');
const PacketStore = require('./packet-store');

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
const pktStore = new PacketStore(db, config.packetStore || {}).load();
const channelKeys = require("./config.json").channelKeys || {};

// --- Cache TTL config (seconds → ms) ---
const _ttlCfg = config.cacheTTL || {};
const TTL = {
  stats:                   (_ttlCfg.stats || 10) * 1000,
  nodeDetail:              (_ttlCfg.nodeDetail || 300) * 1000,
  nodeHealth:              (_ttlCfg.nodeHealth || 300) * 1000,
  nodeList:                (_ttlCfg.nodeList || 90) * 1000,
  bulkHealth:              (_ttlCfg.bulkHealth || 600) * 1000,
  networkStatus:           (_ttlCfg.networkStatus || 600) * 1000,
  observers:               (_ttlCfg.observers || 300) * 1000,
  channels:                (_ttlCfg.channels || 15) * 1000,
  channelMessages:         (_ttlCfg.channelMessages || 10) * 1000,
  analyticsRF:             (_ttlCfg.analyticsRF || 1800) * 1000,
  analyticsTopology:       (_ttlCfg.analyticsTopology || 1800) * 1000,
  analyticsChannels:       (_ttlCfg.analyticsChannels || 1800) * 1000,
  analyticsHashSizes:      (_ttlCfg.analyticsHashSizes || 3600) * 1000,
  analyticsSubpaths:       (_ttlCfg.analyticsSubpaths || 3600) * 1000,
  analyticsSubpathDetail:  (_ttlCfg.analyticsSubpathDetail || 3600) * 1000,
  nodeAnalytics:           (_ttlCfg.nodeAnalytics || 60) * 1000,
  nodeSearch:              (_ttlCfg.nodeSearch || 10) * 1000,
  invalidationDebounce:    (_ttlCfg.invalidationDebounce || 30) * 1000,
};

// --- TTL Cache ---
class TTLCache {
  constructor() { this.store = new Map(); this.hits = 0; this.misses = 0; this.staleHits = 0; this.recomputes = 0; this._inflight = new Map(); }
  get(key) {
    const entry = this.store.get(key);
    if (!entry) { this.misses++; return undefined; }
    if (Date.now() > entry.expires) {
      // Stale-while-revalidate: return stale data if within grace period (2× TTL)
      if (Date.now() < entry.expires + entry.ttl) {
        this.staleHits++;
        return entry.value;
      }
      this.store.delete(key); this.misses++; return undefined;
    }
    this.hits++;
    return entry.value;
  }
  // Check if entry is stale (expired but within grace). Caller should trigger async recompute.
  isStale(key) {
    const entry = this.store.get(key);
    if (!entry) return false;
    return Date.now() > entry.expires && Date.now() < entry.expires + entry.ttl;
  }
  // Recompute guard: ensures only one recompute per key at a time
  recompute(key, fn) {
    if (this._inflight.has(key)) return;
    this._inflight.set(key, true);
    this.recomputes++;
    try { fn(); } catch (e) { console.error(`[cache] recompute error for ${key}:`, e.message); }
    this._inflight.delete(key);
  }
  set(key, value, ttlMs) {
    this.store.set(key, { value, expires: Date.now() + ttlMs, ttl: ttlMs });
  }
  invalidate(prefix) {
    for (const key of this.store.keys()) {
      if (key.startsWith(prefix)) this.store.delete(key);
    }
  }
  debouncedInvalidateAll() {
    if (this._debounceTimer) return;
    this._debounceTimer = setTimeout(() => {
      this._debounceTimer = null;
      // Only invalidate truly time-sensitive caches
      this.invalidate('channels');   // chat messages need freshness
      this.invalidate('observers');   // observer packet counts
      // node:, health:, bulk-health, analytics: all have long TTLs — let them expire naturally
    }, TTL.invalidationDebounce);
  }
  clear() { this.store.clear(); }
  get size() { return this.store.size; }
}
const cache = new TTLCache();


// Seed DB if empty
db.seed();

const app = express();
const server = http.createServer(app);

// --- Performance Instrumentation ---
const perfStats = {
  requests: 0,
  totalMs: 0,
  endpoints: {},  // { path: { count, totalMs, maxMs, avgMs, p95: [], lastSlow } }
  slowQueries: [], // last 50 requests > 100ms
  startedAt: Date.now(),
  reset() {
    this.requests = 0; this.totalMs = 0; this.endpoints = {}; this.slowQueries = []; this.startedAt = Date.now();
  }
};

app.use((req, res, next) => {
  if (!req.path.startsWith('/api/')) return next();
  // Benchmark mode: bypass cache when ?nocache=1
  if (req.query.nocache === '1') {
    const origGet = cache.get.bind(cache);
    cache.get = () => null;
    res.on('finish', () => { cache.get = origGet; });
  }
  const start = process.hrtime.bigint();
  const origEnd = res.end;
  res.end = function(...args) {
    const ms = Number(process.hrtime.bigint() - start) / 1e6;
    perfStats.requests++;
    perfStats.totalMs += ms;
    // Normalize parameterized routes
    const key = req.route ? req.route.path : req.path.replace(/[0-9a-f]{8,}/gi, ':id');
    if (!perfStats.endpoints[key]) perfStats.endpoints[key] = { count: 0, totalMs: 0, maxMs: 0, recent: [] };
    const ep = perfStats.endpoints[key];
    ep.count++;
    ep.totalMs += ms;
    if (ms > ep.maxMs) ep.maxMs = ms;
    ep.recent.push(ms);
    if (ep.recent.length > 100) ep.recent.shift();
    if (ms > 100) {
      perfStats.slowQueries.push({ path: req.path, ms: Math.round(ms * 10) / 10, time: new Date().toISOString(), status: res.statusCode });
      if (perfStats.slowQueries.length > 50) perfStats.slowQueries.shift();
    }
    origEnd.apply(res, args);
  };
  next();
});

// Expose cache TTL config to frontend
app.get('/api/config/cache', (req, res) => {
  res.json(config.cacheTTL || {});
});

app.get('/api/perf', (req, res) => {
  const summary = {};
  for (const [path, ep] of Object.entries(perfStats.endpoints)) {
    const sorted = [...ep.recent].sort((a, b) => a - b);
    const p95 = sorted[Math.floor(sorted.length * 0.95)] || 0;
    const p50 = sorted[Math.floor(sorted.length * 0.5)] || 0;
    summary[path] = {
      count: ep.count,
      avgMs: Math.round(ep.totalMs / ep.count * 10) / 10,
      p50Ms: Math.round(p50 * 10) / 10,
      p95Ms: Math.round(p95 * 10) / 10,
      maxMs: Math.round(ep.maxMs * 10) / 10,
    };
  }
  // Sort by total time spent (count * avg) descending
  const sorted = Object.entries(summary).sort((a, b) => (b[1].count * b[1].avgMs) - (a[1].count * a[1].avgMs));
  res.json({
    uptime: Math.round((Date.now() - perfStats.startedAt) / 1000),
    totalRequests: perfStats.requests,
    avgMs: perfStats.requests ? Math.round(perfStats.totalMs / perfStats.requests * 10) / 10 : 0,
    endpoints: Object.fromEntries(sorted),
    slowQueries: perfStats.slowQueries.slice(-20),
    cache: { size: cache.size, hits: cache.hits, misses: cache.misses, hitRate: cache.hits + cache.misses > 0 ? Math.round(cache.hits / (cache.hits + cache.misses) * 1000) / 10 : 0 },
    packetStore: pktStore.getStats(),
  });
});

app.post('/api/perf/reset', (req, res) => { perfStats.reset(); res.json({ ok: true }); });

// --- Event Loop Lag Monitoring ---
let evtLoopLag = 0, evtLoopMax = 0, evtLoopSamples = [];
const EL_INTERVAL = 1000;
let _elLast = process.hrtime.bigint();
setInterval(() => {
  const now = process.hrtime.bigint();
  const delta = Number(now - _elLast) / 1e6;  // ms
  const lag = Math.max(0, delta - EL_INTERVAL);
  evtLoopLag = lag;
  if (lag > evtLoopMax) evtLoopMax = lag;
  evtLoopSamples.push(lag);
  if (evtLoopSamples.length > 60) evtLoopSamples.shift();  // last 60s
  _elLast = now;
}, EL_INTERVAL).unref();

// --- Health / Telemetry Endpoint ---
app.get('/api/health', (req, res) => {
  const mem = process.memoryUsage();
  const uptime = process.uptime();
  const sorted = [...evtLoopSamples].sort((a, b) => a - b);
  const wsClients = wss ? wss.clients.size : 0;
  const pktStoreSize = pktStore ? pktStore.all().length : 0;
  const pktStoreMB = pktStore ? Math.round(pktStore.all().length * 430 / 1024 / 1024 * 10) / 10 : 0;

  res.json({
    status: 'ok',
    uptime: Math.round(uptime),
    uptimeHuman: `${Math.floor(uptime / 3600)}h ${Math.floor((uptime % 3600) / 60)}m`,
    memory: {
      rss: Math.round(mem.rss / 1024 / 1024),
      heapUsed: Math.round(mem.heapUsed / 1024 / 1024),
      heapTotal: Math.round(mem.heapTotal / 1024 / 1024),
      external: Math.round(mem.external / 1024 / 1024),
    },
    eventLoop: {
      currentLagMs: Math.round(evtLoopLag * 10) / 10,
      maxLagMs: Math.round(evtLoopMax * 10) / 10,
      p50Ms: Math.round((sorted[Math.floor(sorted.length * 0.5)] || 0) * 10) / 10,
      p95Ms: Math.round((sorted[Math.floor(sorted.length * 0.95)] || 0) * 10) / 10,
      p99Ms: Math.round((sorted[Math.floor(sorted.length * 0.99)] || 0) * 10) / 10,
    },
    cache: {
      entries: cache.size,
      hits: cache.hits,
      misses: cache.misses,
      staleHits: cache.staleHits,
      recomputes: cache.recomputes,
      hitRate: cache.hits + cache.misses > 0 ? Math.round(cache.hits / (cache.hits + cache.misses) * 1000) / 10 : 0,
    },
    websocket: {
      clients: wsClients,
    },
    packetStore: {
      packets: pktStoreSize,
      estimatedMB: pktStoreMB,
    },
    perf: {
      totalRequests: perfStats.requests,
      avgMs: perfStats.requests > 0 ? Math.round(perfStats.totalMs / perfStats.requests * 10) / 10 : 0,
      slowQueries: perfStats.slowQueries.length,
      recentSlow: perfStats.slowQueries.slice(-5),
    },
  });
});

// --- WebSocket ---
const wss = new WebSocketServer({ server });

function broadcast(msg) {
  const data = JSON.stringify(msg);
  wss.clients.forEach(c => { if (c.readyState === 1) c.send(data); });
}

// Auto-create stub nodes from path hops (≥2 bytes / 4 hex chars)
// When an advert arrives later with a full pubkey matching the prefix, upsertNode will upgrade it
const hopNodeCache = new Set(); // Avoid repeated DB lookups for known hops

// Shared distance helper (degrees, ~111km/lat, ~85km/lon at 37°N)
function geoDist(lat1, lon1, lat2, lon2) { return Math.sqrt((lat1 - lat2) ** 2 + (lon1 - lon2) ** 2); }

// Sequential hop disambiguation: resolve 1-byte prefixes to best-matching nodes
// Returns array of {hop, name, lat, lon, pubkey, ambiguous, unreliable} per hop
function disambiguateHops(hops, allNodes) {
  const MAX_HOP_DIST = 1.8; // ~200km

  // Build prefix index on first call (cached on allNodes array)
  if (!allNodes._prefixIdx) {
    allNodes._prefixIdx = {};
    allNodes._prefixIdxName = {};
    for (const n of allNodes) {
      const pk = n.public_key.toLowerCase();
      for (let len = 1; len <= 3; len++) {
        const p = pk.slice(0, len * 2);
        if (!allNodes._prefixIdx[p]) allNodes._prefixIdx[p] = [];
        allNodes._prefixIdx[p].push(n);
        if (!allNodes._prefixIdxName[p]) allNodes._prefixIdxName[p] = n;
      }
    }
  }

  // First pass: find candidates per hop
  const resolved = hops.map(hop => {
    const h = hop.toLowerCase();
    const withCoords = (allNodes._prefixIdx[h] || []).filter(n => n.lat && n.lon && !(n.lat === 0 && n.lon === 0));
    if (withCoords.length === 1) {
      return { hop, name: withCoords[0].name, lat: withCoords[0].lat, lon: withCoords[0].lon, pubkey: withCoords[0].public_key, known: true };
    } else if (withCoords.length > 1) {
      return { hop, name: hop, lat: null, lon: null, pubkey: null, known: false, candidates: withCoords };
    }
    const nameMatch = allNodes._prefixIdxName[h];
    return { hop, name: nameMatch?.name || hop, lat: null, lon: null, pubkey: nameMatch?.public_key || null, known: false };
  });

  // Forward pass: resolve ambiguous hops by distance to previous
  let lastPos = null;
  for (const r of resolved) {
    if (r.known && r.lat) { lastPos = [r.lat, r.lon]; continue; }
    if (!r.candidates) continue;
    if (lastPos) r.candidates.sort((a, b) => geoDist(a.lat, a.lon, lastPos[0], lastPos[1]) - geoDist(b.lat, b.lon, lastPos[0], lastPos[1]));
    const best = r.candidates[0];
    r.name = best.name; r.lat = best.lat; r.lon = best.lon; r.pubkey = best.public_key; r.known = true;
    lastPos = [r.lat, r.lon];
  }

  // Backward pass
  let nextPos = null;
  for (let i = resolved.length - 1; i >= 0; i--) {
    const r = resolved[i];
    if (r.known && r.lat) { nextPos = [r.lat, r.lon]; continue; }
    if (!r.candidates || !nextPos) continue;
    r.candidates.sort((a, b) => geoDist(a.lat, a.lon, nextPos[0], nextPos[1]) - geoDist(b.lat, b.lon, nextPos[0], nextPos[1]));
    const best = r.candidates[0];
    r.name = best.name; r.lat = best.lat; r.lon = best.lon; r.pubkey = best.public_key; r.known = true;
    nextPos = [r.lat, r.lon];
  }

  // Distance sanity check
  for (let i = 0; i < resolved.length; i++) {
    const r = resolved[i];
    if (!r.lat) continue;
    const prev = i > 0 && resolved[i-1].lat ? resolved[i-1] : null;
    const next = i < resolved.length-1 && resolved[i+1].lat ? resolved[i+1] : null;
    if (!prev && !next) continue;
    const dPrev = prev ? geoDist(r.lat, r.lon, prev.lat, prev.lon) : 0;
    const dNext = next ? geoDist(r.lat, r.lon, next.lat, next.lon) : 0;
    if ((prev && dPrev > MAX_HOP_DIST) && (next && dNext > MAX_HOP_DIST)) { r.unreliable = true; r.lat = null; r.lon = null; }
    else if (prev && !next && dPrev > MAX_HOP_DIST) { r.unreliable = true; r.lat = null; r.lon = null; }
    else if (!prev && next && dNext > MAX_HOP_DIST) { r.unreliable = true; r.lat = null; r.lon = null; }
  }

  return resolved.map(r => ({ hop: r.hop, name: r.name, lat: r.lat, lon: r.lon, pubkey: r.pubkey, ambiguous: !!r.candidates, unreliable: !!r.unreliable }));
}

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

        const packetId = pktStore.insert({
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
          // Invalidate this node's caches on advert
          cache.invalidate('node:' + p.pubKey);
          cache.invalidate('health:' + p.pubKey);
          cache.invalidate('bulk-health');
        }

        if (observerId) {
          db.upsertObserver({ id: observerId, iata: region });
        }


    // Invalidate caches on new data
    cache.debouncedInvalidateAll();

        const fullPacket = pktStore.getById(packetId);
        const broadcastData = { id: packetId, raw: msg.raw, decoded, snr: msg.SNR, rssi: msg.RSSI, hash: msg.hash, observer: observerId, packet: fullPacket };
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
          
          const packetId = pktStore.insert({
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
        const packetId = pktStore.insert({
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
        const packetId = pktStore.insert({
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
        const packetId = pktStore.insert({
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
  const order = req.query.order === 'asc' ? 'ASC' : 'DESC';

  if (groupByHash === 'true') {
    return res.json(pktStore.queryGrouped({ limit, offset, type, route, region, observer, hash, since, until, node }));
  }

  res.json(pktStore.query({ limit, offset, type, route, region, observer, hash, since, until, node, order }));
});

// Lightweight endpoint: just timestamps for timeline sparkline
app.get('/api/packets/timestamps', (req, res) => {
  const { since } = req.query;
  if (!since) return res.status(400).json({ error: 'since required' });
  res.json(pktStore.getTimestamps(since));
});

app.get('/api/packets/:id', (req, res) => {
  const packet = pktStore.getById(Number(req.params.id)) || db.getPacket(Number(req.params.id));
  if (!packet) return res.status(404).json({ error: 'Not found' });

  // Use the sibling with the longest path (most hops) for display
  if (packet.hash) {
    const siblings = pktStore.getSiblings(packet.hash);
    const best = siblings.reduce((a, b) => (b.path_json || '').length > (a.path_json || '').length ? b : a, packet);
    if (best.path_json && best.path_json.length > (packet.path_json || '').length) {
      packet.path_json = best.path_json;
      packet.raw_hex = best.raw_hex;
    }
  }

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

    const packetId = pktStore.insert({
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


    // Invalidate caches on new data
    cache.debouncedInvalidateAll();

    broadcast({ type: 'packet', data: { id: packetId, decoded } });

    res.json({ id: packetId, decoded });
  } catch (e) {
    res.status(400).json({ error: e.message });
  }
});

app.get('/api/nodes', (req, res) => {
  const { limit = 50, offset = 0, role, region, lastHeard, sortBy = 'lastSeen', search, before } = req.query;

  let where = [];
  let params = {};

  if (role) { where.push('role = @role'); params.role = role; }
  if (search) { where.push('name LIKE @search'); params.search = `%${search}%`; }
  if (before) { where.push('first_seen <= @before'); params.before = before; }
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

// Bulk health summary for analytics — single query approach (MUST be before :pubkey routes)
app.get('/api/nodes/bulk-health', (req, res) => {
  const limit = Math.min(Number(req.query.limit) || 50, 200);
  const _ck = 'bulk-health:' + limit;
  const _c = cache.get(_ck); if (_c) return res.json(_c);

  const nodes = db.db.prepare(`SELECT * FROM nodes ORDER BY last_seen DESC LIMIT ?`).all(limit);
  if (nodes.length === 0) { cache.set(_ck, [], TTL.bulkHealth); return res.json([]); }

  const todayStart = new Date();
  todayStart.setUTCHours(0, 0, 0, 0);
  const todayISO = todayStart.toISOString();

  const results = [];
  for (const node of nodes) {
    const packets = pktStore.byNode.get(node.public_key) || [];
    let totalPackets = packets.length, packetsToday = 0, snrSum = 0, snrCount = 0, lastHeard = null;
    const observers = {};

    for (const pkt of packets) {
      if (pkt.timestamp > todayISO) packetsToday++;
      if (pkt.snr != null) { snrSum += pkt.snr; snrCount++; }
      if (!lastHeard || pkt.timestamp > lastHeard) lastHeard = pkt.timestamp;
      if (pkt.observer_id) {
        if (!observers[pkt.observer_id]) {
          observers[pkt.observer_id] = { name: pkt.observer_name, snrSum: 0, snrCount: 0, rssiSum: 0, rssiCount: 0, count: 0 };
        }
        const obs = observers[pkt.observer_id];
        obs.count++;
        if (pkt.snr != null) { obs.snrSum += pkt.snr; obs.snrCount++; }
        if (pkt.rssi != null) { obs.rssiSum += pkt.rssi; obs.rssiCount++; }
      }
    }

    const observerRows = Object.entries(observers)
      .map(([id, o]) => ({
        observer_id: id, observer_name: o.name,
        avgSnr: o.snrCount ? o.snrSum / o.snrCount : null,
        avgRssi: o.rssiCount ? o.rssiSum / o.rssiCount : null,
        packetCount: o.count
      }))
      .sort((a, b) => b.packetCount - a.packetCount);

    results.push({
      public_key: node.public_key, name: node.name, role: node.role,
      lat: node.lat, lon: node.lon,
      stats: { totalPackets, packetsToday, avgSnr: snrCount ? snrSum / snrCount : null, lastHeard },
      observers: observerRows
    });
  }

  cache.set(_ck, results, TTL.bulkHealth);
  res.json(results);
});

app.get('/api/nodes/network-status', (req, res) => {
  const now = Date.now();
  const allNodes = db.db.prepare('SELECT public_key, name, role, last_seen FROM nodes').all();
  let active = 0, degraded = 0, silent = 0;
  const roleCounts = {};
  allNodes.forEach(n => {
    const r = n.role || 'unknown';
    roleCounts[r] = (roleCounts[r] || 0) + 1;
    const ls = n.last_seen ? new Date(n.last_seen).getTime() : 0;
    const age = now - ls;
    const isInfra = r === 'repeater' || r === 'room';
    const degradedMs = isInfra ? 86400000 : 3600000;
    const silentMs = isInfra ? 259200000 : 86400000;
    if (age < degradedMs) active++;
    else if (age < silentMs) degraded++;
    else silent++;
  });
  res.json({ total: allNodes.length, active, degraded, silent, roleCounts });
});

app.get('/api/nodes/:pubkey', (req, res) => {
  const pubkey = req.params.pubkey;
  const _ck = 'node:' + pubkey;
  const _c = cache.get(_ck); if (_c) return res.json(_c);
  const node = db.db.prepare('SELECT * FROM nodes WHERE public_key = ?').get(pubkey);
  if (!node) return res.status(404).json({ error: 'Not found' });
  const recentAdverts = (pktStore.byNode.get(pubkey) || []).slice(-20).reverse();
  const _nResult = { node, recentAdverts };
  cache.set(_ck, _nResult, TTL.nodeDetail);
  res.json(_nResult);
});

// --- Analytics API ---
// --- RF Analytics ---
app.get('/api/analytics/rf', (req, res) => {
  const _c = cache.get('analytics:rf'); if (_c) return res.json(_c);
  const PTYPES = { 0:'REQ',1:'RESPONSE',2:'TXT_MSG',3:'ACK',4:'ADVERT',5:'GRP_TXT',7:'ANON_REQ',8:'PATH',9:'TRACE',11:'CONTROL' };
  const packets = pktStore.filter(p => p.snr != null);

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

  // Scatter data (SNR vs RSSI) — downsample to max 500 points
  const scatterAll = packets.filter(p => p.snr != null && p.rssi != null);
  const scatterStep = Math.max(1, Math.floor(scatterAll.length / 500));
  const scatterData = scatterAll.filter((_, i) => i % scatterStep === 0).map(p => ({ snr: p.snr, rssi: p.rssi }));

  // Pre-compute histograms server-side so we don't send raw arrays
  function buildHistogram(values, bins) {
    if (!values.length) return { bins: [], min: 0, max: 0 };
    const min = Math.min(...values), max = Math.max(...values);
    const range = max - min || 1;
    const binWidth = range / bins;
    const counts = new Array(bins).fill(0);
    for (const v of values) {
      const idx = Math.min(Math.floor((v - min) / binWidth), bins - 1);
      counts[idx]++;
    }
    return { bins: counts.map((count, i) => ({ x: min + i * binWidth, w: binWidth, count })), min, max };
  }

  const snrHistogram = buildHistogram(snrVals, 20);
  const rssiHistogram = buildHistogram(rssiVals, 20);
  const sizeHistogram = buildHistogram(packetSizes, 25);

  const times = packets.map(p => new Date(p.timestamp).getTime());
  const timeSpanHours = times.length ? (Math.max(...times) - Math.min(...times)) / 3600000 : 0;

  const _rfResult = {
    totalPackets: packets.length,
    snr: { min: Math.min(...snrVals), max: Math.max(...snrVals), avg: snrAvg, median: median(snrVals), stddev: stddev(snrVals, snrAvg) },
    rssi: { min: Math.min(...rssiVals), max: Math.max(...rssiVals), avg: rssiAvg, median: median(rssiVals), stddev: stddev(rssiVals, rssiAvg) },
    snrValues: snrHistogram, rssiValues: rssiHistogram, packetSizes: sizeHistogram,
    minPacketSize: packetSizes.length ? Math.min(...packetSizes) : 0,
    maxPacketSize: packetSizes.length ? Math.max(...packetSizes) : 0,
    avgPacketSize: packetSizes.length ? Math.round(packetSizes.reduce((a, b) => a + b, 0) / packetSizes.length) : 0,
    packetsPerHour, payloadTypes, snrByType: snrByTypeArr, signalOverTime, scatterData, timeSpanHours
  };
  cache.set('analytics:rf', _rfResult, TTL.analyticsRF);
  res.json(_rfResult);
});

// --- Topology Analytics ---
app.get('/api/analytics/topology', (req, res) => {
  const _c = cache.get('analytics:topology'); if (_c) return res.json(_c);
  const packets = pktStore.filter(p => p.path_json && p.path_json !== '[]');
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
  const observerMap = new Map(); pktStore.filter(p => p.path_json && p.path_json !== '[]' && p.observer_id).forEach(p => observerMap.set(p.observer_id, p.observer_name)); const observers = [...observerMap].map(([observer_id, observer_name]) => ({ observer_id, observer_name }));

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

  const _topoResult = {
    uniqueNodes: new Set(Object.keys(hopFreq)).size,
    avgHops, medianHops, maxHops,
    hopDistribution, topRepeaters, topPairs, hopsVsSnr,
    observers: observers.map(o => ({ id: o.observer_id, name: o.observer_name || o.observer_id })),
    perObserverReach,
    multiObsNodes,
    bestPathList
  };
  cache.set('analytics:topology', _topoResult, TTL.analyticsTopology);
  res.json(_topoResult);
});

// --- Channel Analytics ---
app.get('/api/analytics/channels', (req, res) => {
  const _c = cache.get('analytics:channels'); if (_c) return res.json(_c);
  const packets = pktStore.filter(p => p.payload_type === 5 && p.decoded_json);

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

  const _chanResult = {
    activeChannels: channelList.length,
    decryptable: channelList.filter(c => !c.encrypted).length,
    channels: channelList,
    topSenders,
    channelTimeline,
    msgLengths
  };
  cache.set('analytics:channels', _chanResult, TTL.analyticsChannels);
  res.json(_chanResult);
});

app.get('/api/analytics/hash-sizes', (req, res) => {
  const _c = cache.get('analytics:hash-sizes'); if (_c) return res.json(_c);
  // Get all packets with raw_hex and non-empty paths from memory store
  const packets = pktStore.filter(p => p.raw_hex && p.path_json && p.path_json !== '[]');

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

  const _hsResult = {
    total: packets.length,
    distribution,
    hourly,
    topHops,
    multiByteNodes
  };
  cache.set('analytics:hash-sizes', _hsResult, TTL.analyticsHashSizes);
  res.json(_hsResult);
});

// Resolve path hop hex prefixes to node names
app.get('/api/resolve-hops', (req, res) => {
  const hops = (req.query.hops || '').split(',').filter(Boolean);
  const observerId = req.query.observer || null;
  if (!hops.length) return res.json({ resolved: {} });

  const allNodes = db.db.prepare('SELECT public_key, name, lat, lon FROM nodes WHERE name IS NOT NULL').all();

  // Build observer geographic position
  let observerLat = null, observerLon = null;
  if (observerId) {
    // Try exact name match first
    const obsNode = allNodes.find(n => n.name === observerId);
    if (obsNode && obsNode.lat && obsNode.lon && !(obsNode.lat === 0 && obsNode.lon === 0)) {
      observerLat = obsNode.lat;
      observerLon = obsNode.lon;
    } else {
      // Fall back to averaging nearby nodes from adverts this observer received
      const obsNodes = db.db.prepare(`
        SELECT n.lat, n.lon FROM packets p
        JOIN nodes n ON n.public_key = json_extract(p.decoded_json, '$.pubKey')
        WHERE (p.observer_id = ? OR p.observer_name = ?)
          AND p.payload_type = 4
          AND n.lat IS NOT NULL AND n.lat != 0 AND n.lon != 0
        GROUP BY n.public_key
        ORDER BY COUNT(*) DESC
        LIMIT 20
      `).all(observerId, observerId);
      if (obsNodes.length) {
        observerLat = obsNodes.reduce((s, n) => s + n.lat, 0) / obsNodes.length;
        observerLon = obsNodes.reduce((s, n) => s + n.lon, 0) / obsNodes.length;
      }
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

  // Sequential disambiguation: each hop must be near the previous one
  // Walk the path forward, resolving ambiguous hops by distance to last known position
  // Start from first unambiguous hop (or observer position as anchor for last hop)
  
  // Build initial resolved positions map
  const hopPositions = {}; // hop -> {lat, lon}
  for (const hop of hops) {
    const r = resolved[hop];
    if (r && !r.ambiguous && r.pubkey) {
      const node = allNodes.find(n => n.public_key === r.pubkey);
      if (node && node.lat && node.lon && !(node.lat === 0 && node.lon === 0)) {
        hopPositions[hop] = { lat: node.lat, lon: node.lon };
      }
    }
  }

  const dist = (lat1, lon1, lat2, lon2) => Math.sqrt((lat1 - lat2) ** 2 + (lon1 - lon2) ** 2);

  // Forward pass: resolve each ambiguous hop using previous hop's position
  let lastPos = null;
  for (let hi = 0; hi < hops.length; hi++) {
    const hop = hops[hi];
    if (hopPositions[hop]) {
      lastPos = hopPositions[hop];
      continue;
    }
    const r = resolved[hop];
    if (!r || !r.ambiguous) continue;
    const withLoc = r.candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
    if (!withLoc.length) continue;

    // Use previous hop position, or observer position for last hop, or skip
    let anchor = lastPos;
    if (!anchor && hi === hops.length - 1 && observerLat != null) {
      anchor = { lat: observerLat, lon: observerLon };
    }
    if (anchor) {
      withLoc.sort((a, b) => dist(a.lat, a.lon, anchor.lat, anchor.lon) - dist(b.lat, b.lon, anchor.lat, anchor.lon));
    }
    r.name = withLoc[0].name;
    r.pubkey = withLoc[0].pubkey;
    hopPositions[hop] = { lat: withLoc[0].lat, lon: withLoc[0].lon };
    lastPos = hopPositions[hop];
  }

  // Backward pass: resolve any remaining ambiguous hops using next hop's position
  let nextPos = observerLat != null ? { lat: observerLat, lon: observerLon } : null;
  for (let hi = hops.length - 1; hi >= 0; hi--) {
    const hop = hops[hi];
    if (hopPositions[hop]) {
      nextPos = hopPositions[hop];
      continue;
    }
    const r = resolved[hop];
    if (!r || !r.ambiguous) continue;
    const withLoc = r.candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
    if (!withLoc.length || !nextPos) continue;
    withLoc.sort((a, b) => dist(a.lat, a.lon, nextPos.lat, nextPos.lon) - dist(b.lat, b.lon, nextPos.lat, nextPos.lon));
    r.name = withLoc[0].name;
    r.pubkey = withLoc[0].pubkey;
    hopPositions[hop] = { lat: withLoc[0].lat, lon: withLoc[0].lon };
    nextPos = hopPositions[hop];
  }

  // Sanity check: drop hops impossibly far from both neighbors (>200km ≈ 1.8°)
  const MAX_HOP_DIST = 1.8;
  for (let i = 0; i < hops.length; i++) {
    const pos = hopPositions[hops[i]];
    if (!pos) continue;
    const prev = i > 0 ? hopPositions[hops[i-1]] : null;
    const next = i < hops.length-1 ? hopPositions[hops[i+1]] : null;
    if (!prev && !next) continue;
    const dPrev = prev ? dist(pos.lat, pos.lon, prev.lat, prev.lon) : 0;
    const dNext = next ? dist(pos.lat, pos.lon, next.lat, next.lon) : 0;
    const tooFarPrev = prev && dPrev > MAX_HOP_DIST;
    const tooFarNext = next && dNext > MAX_HOP_DIST;
    if ((tooFarPrev && tooFarNext) || (tooFarPrev && !next) || (tooFarNext && !prev)) {
      // Mark as unreliable — likely prefix collision with distant node
      const r = resolved[hops[i]];
      if (r) { r.unreliable = true; }
      delete hopPositions[hops[i]];
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
  const _c = cache.get('channels'); if (_c) return res.json(_c);
  // Single pass: only scan type-5 packets via filter (already in memory)
  const channelMap = {};

  for (const pkt of pktStore.all()) {
    if (pkt.payload_type !== 5) continue;
    let decoded;
    try { decoded = JSON.parse(pkt.decoded_json); } catch { continue; }

    // Companion bridge messages (no raw_hex)
    if (!pkt.raw_hex) {
      const ch = decoded.channel_idx !== undefined ? `c${decoded.channel_idx}` : null;
      if (!ch) continue;
      if (!channelMap[ch]) {
        channelMap[ch] = { hash: ch, name: `Companion Ch ${decoded.channel_idx}`, encrypted: false, lastMessage: null, lastSender: null, messageCount: 0, lastActivity: pkt.timestamp };
      }
      continue;
    }

    const ch = decoded.channelHash !== undefined ? decoded.channelHash : decoded.channel_idx;
    if (ch === undefined) continue;
    
    const knownName = channelHashNames[ch];
    const isDecrypted = decoded.type === 'CHAN' || decoded.text;
    const isCollision = !!(knownName && !isDecrypted && decoded.encryptedData);
    const key = isCollision ? `unk_${ch}` : String(ch);
    
    if (!channelMap[key]) {
      channelMap[key] = {
        hash: key,
        name: isCollision ? `Unknown (hash 0x${Number(ch).toString(16).toUpperCase()})` : (knownName || `Channel 0x${Number(ch).toString(16).toUpperCase()}`),
        encrypted: isCollision || !knownName,
        lastMessage: null, lastSender: null, messageCount: 0, lastActivity: pkt.timestamp,
      };
    }
    channelMap[key].messageCount++;
    if (pkt.timestamp >= channelMap[key].lastActivity) {
      channelMap[key].lastActivity = pkt.timestamp;
      if (decoded.text) {
        const colonIdx = decoded.text.indexOf(': ');
        channelMap[key].lastMessage = colonIdx > 0 ? decoded.text.slice(colonIdx + 2) : decoded.text;
        channelMap[key].lastSender = decoded.sender || null;
      }
    }
  }

  const _chResult = { channels: Object.values(channelMap) };
  cache.set('channels', _chResult, TTL.channels);
  res.json(_chResult);
});

app.get('/api/channels/:hash/messages', (req, res) => {
  const _ck = 'channels:' + req.params.hash + ':' + (req.query.limit||100) + ':' + (req.query.offset||0);
  const _c = cache.get(_ck); if (_c) return res.json(_c);
  const { limit = 100, offset = 0 } = req.query;
  const channelHash = req.params.hash;
  const packets = pktStore.filter(p => p.payload_type === 5).sort((a,b) => a.timestamp > b.timestamp ? 1 : -1);

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
  const _msgResult = { messages, total };
  cache.set(_ck, _msgResult, TTL.channelMessages);
  res.json(_msgResult);
});

app.get('/api/observers', (req, res) => {
  const _c = cache.get('observers'); if (_c) return res.json(_c);
  const observers = db.getObservers();
  const oneHourAgo = new Date(Date.now() - 3600000).toISOString();
  const result = observers.map(o => {
    const obsPackets = pktStore.byObserver.get(o.id) || [];
    const lastHour = { count: obsPackets.filter(p => p.timestamp > oneHourAgo).length };
    return { ...o, packetsLastHour: lastHour.count };
  });
  const _oResult = { observers: result, server_time: new Date().toISOString() };
  cache.set('observers', _oResult, TTL.observers);
  res.json(_oResult);
});

app.get('/api/traces/:hash', (req, res) => {
  const packets = (pktStore.getSiblings(req.params.hash) || []).sort((a,b) => a.timestamp > b.timestamp ? 1 : -1);
  const traces = packets.map(p => ({ observer: p.observer_id, time: p.timestamp, snr: p.snr, rssi: p.rssi }));
  res.json({ traces });
});

app.get('/api/nodes/:pubkey/health', (req, res) => {
  const pubkey = req.params.pubkey;
  const _ck = 'health:' + pubkey;
  const _c = cache.get(_ck); if (_c) return res.json(_c);

  const node = db.db.prepare('SELECT * FROM nodes WHERE public_key = ?').get(pubkey);
  if (!node) return res.status(404).json({ error: 'Not found' });

  const todayStart = new Date(); todayStart.setUTCHours(0, 0, 0, 0);
  const todayISO = todayStart.toISOString();
  const packets = pktStore.byNode.get(pubkey) || [];

  // Observers
  const obsMap = {};
  let snrSum = 0, snrN = 0, totalHops = 0, hopCount = 0, lastHeard = null, packetsToday = 0;
  for (const p of packets) {
    if (p.timestamp > todayISO) packetsToday++;
    if (p.snr != null) { snrSum += p.snr; snrN++; }
    if (!lastHeard || p.timestamp > lastHeard) lastHeard = p.timestamp;
    if (p.path_json) {
      try { const h = JSON.parse(p.path_json); if (Array.isArray(h)) { totalHops += h.length; hopCount++; } } catch {}
    }
    if (p.observer_id) {
      if (!obsMap[p.observer_id]) obsMap[p.observer_id] = { observer_name: p.observer_name, snrSum: 0, snrN: 0, rssiSum: 0, rssiN: 0, packetCount: 0 };
      const o = obsMap[p.observer_id]; o.packetCount++;
      if (p.snr != null) { o.snrSum += p.snr; o.snrN++; }
      if (p.rssi != null) { o.rssiSum += p.rssi; o.rssiN++; }
    }
  }

  const observers = Object.entries(obsMap).map(([observer_id, o]) => ({
    observer_id, observer_name: o.observer_name, packetCount: o.packetCount,
    avgSnr: o.snrN ? o.snrSum / o.snrN : null, avgRssi: o.rssiN ? o.rssiSum / o.rssiN : null
  })).sort((a, b) => b.packetCount - a.packetCount);

  const recentPackets = packets.slice(-10).reverse();

  const result = {
    node: node.node || node, observers,
    stats: { totalPackets: packets.length, packetsToday, avgSnr: snrN ? snrSum / snrN : null, avgHops: hopCount > 0 ? Math.round(totalHops / hopCount) : 0, lastHeard },
    recentPackets
  };
  cache.set(_ck, result, TTL.nodeHealth);
  res.json(result);
});

app.get('/api/nodes/:pubkey/analytics', (req, res) => {
  const pubkey = req.params.pubkey;
  const days = Math.min(Math.max(Number(req.query.days) || 7, 1), 365);
  const _ck = `node-analytics:${pubkey}:${days}`;
  const _c = cache.get(_ck); if (_c) return res.json(_c);

  const node = db.db.prepare('SELECT * FROM nodes WHERE public_key = ?').get(pubkey);
  if (!node) return res.status(404).json({ error: 'Not found' });

  const now = new Date();
  const fromISO = new Date(now.getTime() - days * 86400000).toISOString();
  const toISO = now.toISOString();

  // Read from in-memory index, filter by time range
  const allPkts = pktStore.byNode.get(pubkey) || [];
  const packets = allPkts.filter(p => p.timestamp > fromISO);

  // Activity timeline
  const timelineBuckets = {};
  for (const p of packets) { const b = p.timestamp.slice(0, 13) + ':00:00Z'; timelineBuckets[b] = (timelineBuckets[b] || 0) + 1; }
  const activityTimeline = Object.entries(timelineBuckets).sort().map(([bucket, count]) => ({ bucket, count }));

  // SNR trend
  const snrTrend = packets.filter(p => p.snr != null).map(p => ({
    timestamp: p.timestamp, snr: p.snr, rssi: p.rssi, observer_id: p.observer_id, observer_name: p.observer_name
  }));

  // Packet type breakdown
  const typeBuckets = {};
  for (const p of packets) { typeBuckets[p.payload_type] = (typeBuckets[p.payload_type] || 0) + 1; }
  const packetTypeBreakdown = Object.entries(typeBuckets).map(([payload_type, count]) => ({ payload_type: +payload_type, count }));

  // Observer coverage
  const obsMap = {};
  for (const p of packets) {
    if (!p.observer_id) continue;
    if (!obsMap[p.observer_id]) obsMap[p.observer_id] = { observer_name: p.observer_name, packetCount: 0, snrSum: 0, snrN: 0, rssiSum: 0, rssiN: 0, first: p.timestamp, last: p.timestamp };
    const o = obsMap[p.observer_id]; o.packetCount++;
    if (p.snr != null) { o.snrSum += p.snr; o.snrN++; }
    if (p.rssi != null) { o.rssiSum += p.rssi; o.rssiN++; }
    if (p.timestamp < o.first) o.first = p.timestamp;
    if (p.timestamp > o.last) o.last = p.timestamp;
  }
  const observerCoverage = Object.entries(obsMap).map(([observer_id, o]) => ({
    observer_id, observer_name: o.observer_name, packetCount: o.packetCount,
    avgSnr: o.snrN ? o.snrSum / o.snrN : null, avgRssi: o.rssiN ? o.rssiSum / o.rssiN : null,
    firstSeen: o.first, lastSeen: o.last
  })).sort((a, b) => b.packetCount - a.packetCount);

  // Hop distribution
  const hopCounts = {};
  let totalWithPath = 0, relayedCount = 0;
  for (const p of packets) {
    if (!p.path_json) continue;
    try {
      const hops = JSON.parse(p.path_json);
      if (Array.isArray(hops)) {
        const h = hops.length; const key = h >= 4 ? '4+' : String(h);
        hopCounts[key] = (hopCounts[key] || 0) + 1;
        totalWithPath++; if (h > 1) relayedCount++;
      }
    } catch {}
  }
  const hopDistribution = Object.entries(hopCounts).map(([hops, count]) => ({ hops, count }))
    .sort((a, b) => a.hops.localeCompare(b.hops, undefined, { numeric: true }));

  // Peer interactions
  const peerMap = {};
  for (const p of packets) {
    if (!p.decoded_json) continue;
    try {
      const d = JSON.parse(p.decoded_json);
      const candidates = [];
      if (d.sender_key && d.sender_key !== pubkey) candidates.push({ key: d.sender_key, name: d.sender_name || d.sender_short_name });
      if (d.recipient_key && d.recipient_key !== pubkey) candidates.push({ key: d.recipient_key, name: d.recipient_name || d.recipient_short_name });
      if (d.pubkey && d.pubkey !== pubkey) candidates.push({ key: d.pubkey, name: d.name });
      for (const c of candidates) {
        if (!c.key) continue;
        if (!peerMap[c.key]) peerMap[c.key] = { peer_key: c.key, peer_name: c.name || c.key.slice(0, 12), messageCount: 0, lastContact: p.timestamp };
        peerMap[c.key].messageCount++;
        if (p.timestamp > peerMap[c.key].lastContact) peerMap[c.key].lastContact = p.timestamp;
      }
    } catch {}
  }
  const peerInteractions = Object.values(peerMap).sort((a, b) => b.messageCount - a.messageCount).slice(0, 20);

  // Uptime heatmap
  const heatmap = [];
  for (const p of packets) {
    const d = new Date(p.timestamp);
    heatmap.push({ dayOfWeek: d.getUTCDay(), hour: d.getUTCHours() });
  }
  const heatBuckets = {};
  for (const h of heatmap) { const k = `${h.dayOfWeek}:${h.hour}`; heatBuckets[k] = (heatBuckets[k] || 0) + 1; }
  const uptimeHeatmap = Object.entries(heatBuckets).map(([k, count]) => {
    const [d, h] = k.split(':'); return { dayOfWeek: +d, hour: +h, count };
  });

  // Computed stats
  const totalPackets = packets.length;
  const distinctHours = activityTimeline.length;
  const availabilityPct = days * 24 > 0 ? Math.round(distinctHours / (days * 24) * 1000) / 10 : 0;
  const avgPacketsPerDay = days > 0 ? Math.round(totalPackets / days * 10) / 10 : totalPackets;

  // Longest silence
  const timestamps = packets.map(p => new Date(p.timestamp).getTime()).sort((a, b) => a - b);
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

  const result = {
    node: node.node || node,
    timeRange: { from: fromISO, to: toISO, days },
    activityTimeline, snrTrend, packetTypeBreakdown, observerCoverage, hopDistribution, peerInteractions, uptimeHeatmap,
    computedStats: {
      availabilityPct, longestSilenceMs, longestSilenceStart, signalGrade,
      snrMean: Math.round(snrMean * 10) / 10, snrStdDev: Math.round(snrStdDev * 10) / 10,
      relayPct, totalPackets, uniqueObservers: observerCoverage.length, uniquePeers: peerInteractions.length, avgPacketsPerDay
    }
  };
  cache.set(_ck, result, TTL.nodeAnalytics);
  res.json(result);
});

// Pre-compute all subpath data in a single pass (shared across all subpath queries)
let _subpathsComputing = null;
function computeAllSubpaths() {
  const _c = cache.get('analytics:subpaths:master');
  if (_c) return _c;
  if (_subpathsComputing) return _subpathsComputing; // deduplicate concurrent calls

  const t0 = Date.now();
  const packets = pktStore.filter(p => p.path_json && p.path_json !== '[]');
  const allNodes = db.db.prepare('SELECT public_key, name, lat, lon FROM nodes WHERE name IS NOT NULL').all();

  const disambigCache = {};
  function cachedDisambiguate(hops) {
    const key = hops.join(',');
    if (disambigCache[key]) return disambigCache[key];
    const result = disambiguateHops(hops, allNodes);
    disambigCache[key] = result;
    return result;
  }

  // Single pass: extract ALL subpaths (lengths 2-8) at once
  const subpathsByLen = {}; // len → { path → { count, raw } }
  let totalPaths = 0;

  for (const pkt of packets) {
    let hops;
    try { hops = JSON.parse(pkt.path_json); } catch { continue; }
    if (!Array.isArray(hops) || hops.length < 2) continue;
    totalPaths++;

    const resolved = cachedDisambiguate(hops);
    const named = resolved.map(r => r.name);

    for (let len = 2; len <= Math.min(8, named.length); len++) {
      if (!subpathsByLen[len]) subpathsByLen[len] = {};
      for (let start = 0; start <= named.length - len; start++) {
        const sub = named.slice(start, start + len).join(' → ');
        const raw = hops.slice(start, start + len).join(',');
        if (!subpathsByLen[len][sub]) subpathsByLen[len][sub] = { count: 0, raw };
        subpathsByLen[len][sub].count++;
      }
    }
  }

  const master = { subpathsByLen, totalPaths };
  cache.set('analytics:subpaths:master', master, TTL.analyticsSubpaths);
  _subpathsComputing = master; // keep ref for concurrent callers
  setTimeout(() => { _subpathsComputing = null; }, 100); // release after brief window
  return master;
}

// Subpath frequency analysis — reads from pre-computed master
app.get('/api/analytics/subpaths', (req, res) => {
  const _ck = 'analytics:subpaths:' + (req.query.minLen||2) + ':' + (req.query.maxLen||8) + ':' + (req.query.limit||100);
  const _c = cache.get(_ck); if (_c) return res.json(_c);

  const minLen = Math.max(2, Number(req.query.minLen) || 2);
  const maxLen = Number(req.query.maxLen) || 8;
  const limit = Number(req.query.limit) || 100;

  const { subpathsByLen, totalPaths } = computeAllSubpaths();

  // Merge requested length ranges
  const merged = {};
  for (let len = minLen; len <= maxLen; len++) {
    const bucket = subpathsByLen[len] || {};
    for (const [path, data] of Object.entries(bucket)) {
      if (!merged[path]) merged[path] = { count: 0, raw: data.raw };
      merged[path].count += data.count;
    }
  }

  const ranked = Object.entries(merged)
    .map(([path, data]) => ({
      path,
      rawHops: data.raw.split(','),
      count: data.count,
      hops: path.split(' → ').length,
      pct: totalPaths > 0 ? Math.round(data.count / totalPaths * 1000) / 10 : 0
    }))
    .sort((a, b) => b.count - a.count)
    .slice(0, limit);

  const _spResult = { subpaths: ranked, totalPaths };
  cache.set(_ck, _spResult, TTL.analyticsSubpaths);
  res.json(_spResult);
});

// Subpath detail — stats for a specific subpath (by raw hop prefixes)
app.get('/api/analytics/subpath-detail', (req, res) => {
  const _sdck = 'analytics:subpath-detail:' + (req.query.hops || '');
  const _sdc = cache.get(_sdck); if (_sdc) return res.json(_sdc);
  const rawHops = (req.query.hops || '').split(',').filter(Boolean);
  if (rawHops.length < 2) return res.json({ error: 'Need at least 2 hops' });

  const packets = pktStore.filter(p => p.path_json && p.path_json !== '[]');
  const allNodes = db.db.prepare('SELECT public_key, name, lat, lon FROM nodes WHERE name IS NOT NULL').all();

  // Disambiguate the requested hops
  const resolvedHops = disambiguateHops(rawHops, allNodes);

  const matching = [];
  const parentPaths = {};
  const hourBuckets = new Array(24).fill(0);
  let snrSum = 0, snrCount = 0, rssiSum = 0, rssiCount = 0;
  const observers = {};
  const _detailCache = {};

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

    // Track full parent paths (disambiguated, cached)
    const cacheKey = hops.join(',');
    if (!_detailCache[cacheKey]) _detailCache[cacheKey] = disambiguateHops(hops, allNodes);
    const fullPath = _detailCache[cacheKey].map(r => r.name).join(' → ');
    parentPaths[fullPath] = (parentPaths[fullPath] || 0) + 1;
  }

  // Use disambiguated nodes for map
  const nodes = resolvedHops.map(r => ({ hop: r.hop, name: r.name, lat: r.lat, lon: r.lon, pubkey: r.pubkey }));

  const topParents = Object.entries(parentPaths)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 15)
    .map(([path, count]) => ({ path, count }));

  const topObservers = Object.entries(observers)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 10)
    .map(([name, count]) => ({ name, count }));

    const _sdResult = {
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
  };
  cache.set(_sdck, _sdResult, TTL.analyticsSubpathDetail);
  res.json(_sdResult);
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
  // Pre-warm expensive caches on startup
  setTimeout(() => {
    const t0 = Date.now();
    try {
      computeAllSubpaths();
      // Pre-warm the 4 specific subpath queries the frontend makes
      const warmUrls = [
        { minLen: 2, maxLen: 2, limit: 50 },
        { minLen: 3, maxLen: 3, limit: 30 },
        { minLen: 4, maxLen: 4, limit: 20 },
        { minLen: 5, maxLen: 8, limit: 15 },
      ];
      for (const q of warmUrls) {
        const ck = `analytics:subpaths:${q.minLen}:${q.maxLen}:${q.limit}`;
        if (!cache.get(ck)) {
          const { subpathsByLen, totalPaths } = computeAllSubpaths();
          const merged = {};
          for (let len = q.minLen; len <= q.maxLen; len++) {
            const bucket = subpathsByLen[len] || {};
            for (const [path, data] of Object.entries(bucket)) {
              if (!merged[path]) merged[path] = { count: 0, raw: data.raw };
              merged[path].count += data.count;
            }
          }
          const ranked = Object.entries(merged)
            .map(([path, data]) => ({ path, rawHops: data.raw.split(','), count: data.count, hops: path.split(' → ').length, pct: totalPaths > 0 ? Math.round(data.count / totalPaths * 1000) / 10 : 0 }))
            .sort((a, b) => b.count - a.count)
            .slice(0, q.limit);
          cache.set(ck, { subpaths: ranked, totalPaths }, TTL.analyticsSubpaths);
        }
      }
    } catch (e) { console.error('[pre-warm] subpaths:', e.message); }
    console.log(`[pre-warm] subpaths: ${Date.now() - t0}ms`);

    // Pre-warm other heavy analytics endpoints via self-request
    const port = process.env.PORT || config.port;
    const warmEndpoints = [
      '/api/analytics/rf',
      '/api/analytics/topology',
      '/api/analytics/channels',
      '/api/analytics/hash-sizes',
      '/api/nodes/bulk-health?limit=50',
    ];
    let warmed = 0;
    const tw = Date.now();
    const warmNext = () => {
      if (warmed >= warmEndpoints.length) {
        console.log(`[pre-warm] analytics: ${warmEndpoints.length} endpoints in ${Date.now() - tw}ms`);
        return;
      }
      const ep = warmEndpoints[warmed++];
      http.get(`http://127.0.0.1:${port}${ep}`, (res) => {
        res.resume();
        res.on('end', warmNext);
      }).on('error', warmNext);
    };
    // Stagger: warm analytics after subpaths are done (sequential to avoid blocking)
    warmNext();
  }, 1000);
});

module.exports = { app, server, wss };
