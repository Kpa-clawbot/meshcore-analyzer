'use strict';

const express = require('express');
const http = require('http');
const https = require('https');
const { WebSocketServer } = require('ws');
const mqtt = require('mqtt');
const path = require('path');
const fs = require('fs');
// Config: bind-mounted config.json first, then fall back to data/ dir
const CONFIG_PATHS = [
  path.join(__dirname, 'config.json'),
  path.join(__dirname, 'data', 'config.json')
];
function loadConfigFile() {
  for (const p of CONFIG_PATHS) {
    try { return JSON.parse(fs.readFileSync(p, 'utf8')); } catch {}
  }
  return {};
}
const config = loadConfigFile();
const decoder = require('./decoder');
const PAYLOAD_TYPES = decoder.PAYLOAD_TYPES;
const { nodeNearRegion, IATA_COORDS } = require('./iata-coords');

// Health thresholds — configurable with sensible defaults
const _ht = config.healthThresholds || {};
const HEALTH = {
  infraDegradedMs: _ht.infraDegradedMs || 86400000,
  infraSilentMs:   _ht.infraSilentMs   || 259200000,
  nodeDegradedMs:  _ht.nodeDegradedMs  || 3600000,
  nodeSilentMs:    _ht.nodeSilentMs    || 86400000
};
function getHealthMs(role) {
  const isInfra = role === 'repeater' || role === 'room';
  return {
    degradedMs: isInfra ? HEALTH.infraDegradedMs : HEALTH.nodeDegradedMs,
    silentMs:   isInfra ? HEALTH.infraSilentMs   : HEALTH.nodeSilentMs
  };
}
const MAX_HOP_DIST_SERVER = config.maxHopDist || 1.8;
const crypto = require('crypto');
const PacketStore = require('./packet-store');

// --- Precomputed hash_size map (updated on new packets, not per-request) ---
const _hashSizeMap = new Map();      // pubkey → latest hash_size (number)
const _hashSizeAllMap = new Map();   // pubkey → Set of all hash_sizes seen
const _hashSizeSeqMap = new Map();   // pubkey → array of hash_sizes in chronological order (oldest first)
function _rebuildHashSizeMap() {
  _hashSizeMap.clear();
  _hashSizeAllMap.clear();
  _hashSizeSeqMap.clear();
  // Pass 1: from ADVERT packets (most authoritative — path byte bits 7-6)
  // packets array is sorted newest-first, so first-match = newest ADVERT
  for (const p of pktStore.packets) {
    if (p.payload_type === 4 && p.raw_hex) {
      try {
        const d = JSON.parse(p.decoded_json || '{}');
        const pk = d.pubKey || d.public_key;
        if (pk) {
          const pathByte = parseInt(p.raw_hex.slice(2, 4), 16);
          const hs = ((pathByte >> 6) & 0x3) + 1;
          if (!_hashSizeMap.has(pk)) _hashSizeMap.set(pk, hs);
          if (!_hashSizeAllMap.has(pk)) _hashSizeAllMap.set(pk, new Set());
          _hashSizeAllMap.get(pk).add(hs);
          // Build sequence (will reverse later since packets are newest-first)
          if (!_hashSizeSeqMap.has(pk)) _hashSizeSeqMap.set(pk, []);
          _hashSizeSeqMap.get(pk).push(hs);
        }
      } catch {}
    }
  }
  // Reverse sequences to chronological order (oldest first)
  for (const [, seq] of _hashSizeSeqMap) seq.reverse();
  // Pass 2: for nodes without ADVERTs, derive from path hop lengths in any packet
  for (const p of pktStore.packets) {
    if (p.path_json) {
      try {
        const hops = JSON.parse(p.path_json);
        if (hops.length > 0) {
          const hopLen = hops[0].length / 2;
          if (hopLen >= 1 && hopLen <= 4) {
            const pathByte = p.raw_hex ? parseInt(p.raw_hex.slice(2, 4), 16) : -1;
            const hs = pathByte >= 0 ? ((pathByte >> 6) & 0x3) + 1 : hopLen;
            if (p.decoded_json) {
              const d = JSON.parse(p.decoded_json);
              const pk = d.pubKey || d.public_key;
              if (pk && !_hashSizeMap.has(pk)) _hashSizeMap.set(pk, hs);
            }
          }
        }
      } catch {}
    }
  }
}
// Detect flip-flopping hash sizes (not just upgrades)
// A clean upgrade: [1,1,1,2,2,2] — sizes change once and stay. That's fine.
// Flip-flop: [1,2,1,2] or [2,1,2] — sizes go back and forth. That's a bug.
function _isHashSizeFlipFlop(pubkey) {
  const seq = _hashSizeSeqMap.get(pubkey);
  if (!seq || seq.length < 3) return false; // need enough samples
  const allSizes = _hashSizeAllMap.get(pubkey);
  if (!allSizes || allSizes.size < 2) return false; // only one size = no issue
  // Count transitions (size changes)
  let transitions = 0;
  for (let i = 1; i < seq.length; i++) {
    if (seq[i] !== seq[i - 1]) transitions++;
  }
  // A clean upgrade has exactly 1 transition. Flip-flop has 2+.
  return transitions >= 2;
}

// Update hash_size for a single new packet (called on insert)
function _updateHashSizeForPacket(p) {
  if (p.payload_type === 4 && p.raw_hex) {
    try {
      const d = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json || '{}') : (p.decoded_json || {});
      const pk = d.pubKey || d.public_key;
      if (pk) {
        const pathByte = parseInt(p.raw_hex.slice(2, 4), 16);
        const hs = ((pathByte >> 6) & 0x3) + 1;
        _hashSizeMap.set(pk, hs);
        if (!_hashSizeAllMap.has(pk)) _hashSizeAllMap.set(pk, new Set());
        _hashSizeAllMap.get(pk).add(hs);
        if (!_hashSizeSeqMap.has(pk)) _hashSizeSeqMap.set(pk, []);
        _hashSizeSeqMap.get(pk).push(hs); // already chronological for live packets
      }
    } catch {}
  } else if (p.path_json && p.decoded_json) {
    try {
      const d = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json) : p.decoded_json;
      const pk = d.pubKey || d.public_key;
      if (pk && !_hashSizeMap.has(pk)) {
        const hops = typeof p.path_json === 'string' ? JSON.parse(p.path_json) : p.path_json;
        if (hops.length > 0) {
          const pathByte = p.raw_hex ? parseInt(p.raw_hex.slice(2, 4), 16) : -1;
          const hs = pathByte >= 0 ? ((pathByte >> 6) & 0x3) + 1 : (hops[0].length / 2);
          if (hs >= 1 && hs <= 4) _hashSizeMap.set(pk, hs);
        }
      }
    } catch {}
  }
}

// API key middleware for write endpoints
const API_KEY = config.apiKey || null;
function requireApiKey(req, res, next) {
  if (!API_KEY) return next(); // no key configured = open (dev mode)
  const provided = req.headers['x-api-key'] || req.query.apiKey;
  if (provided === API_KEY) return next();
  return res.status(401).json({ error: 'Invalid or missing API key' });
}

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
_rebuildHashSizeMap();

// Backfill: fix roles for nodes whose adverts were decoded with old bitfield flags
// ADV_TYPE is a 4-bit enum (0=none, 1=chat, 2=repeater, 3=room, 4=sensor), not individual bits
(function _backfillRoles() {
  const ADV_ROLES = { 1: 'companion', 2: 'repeater', 3: 'room', 4: 'sensor' };
  let fixed = 0;
  for (const p of pktStore.packets) {
    if (p.payload_type !== 4 || !p.raw_hex) continue;
    try {
      const d = JSON.parse(p.decoded_json || '{}');
      const pk = d.pubKey || d.public_key;
      if (!pk) continue;
      const appStart = p.raw_hex.length - (d.flags?.raw != null ? 2 : 0); // flags byte position varies
      const flagsByte = d.flags?.raw;
      if (flagsByte == null) continue;
      const advType = flagsByte & 0x0F;
      const correctRole = ADV_ROLES[advType] || 'companion';
      const node = db.db.prepare('SELECT role FROM nodes WHERE public_key = ?').get(pk);
      if (node && node.role !== correctRole) {
        db.db.prepare('UPDATE nodes SET role = ? WHERE public_key = ?').run(correctRole, pk);
        fixed++;
      }
    } catch {}
  }
  if (fixed > 0) console.log(`[backfill] Fixed ${fixed} node roles (advert type enum vs bitfield)`);
})();

// --- Shared cached node list (refreshed every 30s, avoids repeated SQLite queries) ---
let _cachedAllNodes = null;
let _cachedAllNodesWithRole = null;
let _cachedAllNodesTs = 0;
const NODES_CACHE_MS = 30000;
function getCachedNodes(includeRole) {
  const now = Date.now();
  if (!_cachedAllNodes || now - _cachedAllNodesTs > NODES_CACHE_MS) {
    _cachedAllNodes = db.db.prepare('SELECT public_key, name, lat, lon FROM nodes WHERE name IS NOT NULL').all();
    _cachedAllNodesWithRole = db.db.prepare('SELECT public_key, name, lat, lon, role FROM nodes WHERE name IS NOT NULL').all();
    _cachedAllNodesTs = now;
    // Clear prefix index so disambiguateHops rebuilds it on fresh data
    delete _cachedAllNodes._prefixIdx;
    delete _cachedAllNodes._prefixIdxName;
    delete _cachedAllNodesWithRole._prefixIdx;
    delete _cachedAllNodesWithRole._prefixIdxName;
  }
  return includeRole ? _cachedAllNodesWithRole : _cachedAllNodes;
}

const configuredChannelKeys = config.channelKeys || {};
const hashChannels = Array.isArray(config.hashChannels) ? config.hashChannels : [];

function deriveHashtagChannelKey(channelName) {
  return crypto.createHash('sha256').update(channelName).digest('hex').slice(0, 32);
}

const derivedHashChannelKeys = {};
for (const rawChannel of hashChannels) {
  if (typeof rawChannel !== 'string') continue;
  const trimmed = rawChannel.trim();
  if (!trimmed) continue;
  const channelName = trimmed.startsWith('#') ? trimmed : `#${trimmed}`;
  if (Object.prototype.hasOwnProperty.call(configuredChannelKeys, channelName)) continue;
  derivedHashChannelKeys[channelName] = deriveHashtagChannelKey(channelName);
}

// Load rainbow table of pre-computed channel keys (common MeshCore channel names)
let rainbowKeys = {};
try {
  const rainbowPath = path.join(__dirname, 'channel-rainbow.json');
  if (fs.existsSync(rainbowPath)) {
    rainbowKeys = JSON.parse(fs.readFileSync(rainbowPath, 'utf8'));
    console.log(`[channels] Loaded ${Object.keys(rainbowKeys).length} rainbow table entries`);
  }
} catch (e) {
  console.warn('[channels] Failed to load channel-rainbow.json:', e.message);
}

// Merge: rainbow (lowest priority) -> derived from hashChannels -> explicit config (highest priority)
const channelKeys = { ...rainbowKeys, ...derivedHashChannelKeys, ...configuredChannelKeys };

const totalKeys = Object.keys(channelKeys).length;
const derivedCount = Object.keys(derivedHashChannelKeys).length;
const rainbowCount = Object.keys(rainbowKeys).length;
console.log(`[channels] ${totalKeys} channel key(s) (${derivedCount} derived from hashChannels, ${rainbowCount} from rainbow table)`);

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


// Seed DB only when explicitly requested via --seed flag or SEED_DB=true env var
if (process.argv.includes('--seed') || process.env.SEED_DB === 'true') {
  db.seed();
}

const app = express();

function createServer(app, cfg) {
  const tls = cfg.https || {};
  if (!tls.cert || !tls.key) {
    return { server: http.createServer(app), isHttps: false };
  }

  try {
    const certPath = path.resolve(tls.cert);
    const keyPath = path.resolve(tls.key);
    const options = {
      cert: fs.readFileSync(certPath),
      key: fs.readFileSync(keyPath),
    };
    console.log(`[https] enabled (cert: ${certPath}, key: ${keyPath})`);
    return { server: https.createServer(options, app), isHttps: true };
  } catch (e) {
    console.error(`[https] failed to load TLS cert/key, falling back to HTTP: ${e.message}`);
    return { server: http.createServer(app), isHttps: false };
  }
}

const { server, isHttps } = createServer(app, config);

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

// Expose all client-side config (roles, thresholds, tiles, limits, etc.)
app.get('/api/config/client', (req, res) => {
  res.json({
    roles: config.roles || null,
    healthThresholds: config.healthThresholds || null,
    tiles: config.tiles || null,
    snrThresholds: config.snrThresholds || null,
    distThresholds: config.distThresholds || null,
    maxHopDist: config.maxHopDist || null,
    limits: config.limits || null,
    perfSlowMs: config.perfSlowMs || null,
    wsReconnectMs: config.wsReconnectMs || null,
    cacheInvalidateMs: config.cacheInvalidateMs || null,
    externalUrls: config.externalUrls || null,
    propagationBufferMs: (config.liveMap || {}).propagationBufferMs || 5000
  });
});

app.get('/api/config/regions', (req, res) => {
  // Merge config regions with any IATA codes seen from observers
  const regions = { ...(config.regions || {}) };
  try {
    const rows = db.db.prepare("SELECT DISTINCT iata FROM observers WHERE iata IS NOT NULL").all();
    for (const r of rows) {
      if (r.iata && !regions[r.iata]) regions[r.iata] = r.iata; // fallback to code itself
    }
  } catch {}
  res.json(regions);
});

// Helper: get set of observer IDs matching region filter (comma-separated IATA codes)
function getObserverIdsForRegions(regionParam) {
  if (!regionParam) return null; // null = no filter
  const codes = regionParam.split(',').map(s => s.trim()).filter(Boolean);
  if (codes.length === 0) return null;
  const ids = new Set();
  const observers = db.getObservers();
  for (const o of observers) {
    if (o.iata && codes.includes(o.iata)) ids.add(o.id);
  }
  return ids;
}

// Theme: hot-load from theme.json (same dir as config.json, or data/ dir)
const THEME_PATHS = [
  path.join(__dirname, 'theme.json'),
  path.join(__dirname, 'data', 'theme.json')
];
function loadThemeFile() {
  for (const p of THEME_PATHS) {
    try { return JSON.parse(fs.readFileSync(p, 'utf8')); } catch {}
  }
  return {};
}

app.get('/api/config/theme', (req, res) => {
  const cfg = loadConfigFile();
  const theme = loadThemeFile();
  res.json({
    branding: {
      siteName: 'MeshCore Analyzer',
      tagline: 'Real-time MeshCore LoRa mesh network analyzer',
      ...(cfg.branding || {}),
      ...(theme.branding || {})
    },
    theme: {
      accent: '#4a9eff',
      accentHover: '#6db3ff',
      navBg: '#0f0f23',
      navBg2: '#1a1a2e',
      ...(cfg.theme || {}),
      ...(theme.theme || {})
    },
    themeDark: {
      ...(cfg.themeDark || {}),
      ...(theme.themeDark || {})
    },
    nodeColors: {
      repeater: '#dc2626',
      companion: '#2563eb',
      room: '#16a34a',
      sensor: '#d97706',
      observer: '#8b5cf6',
      ...(cfg.nodeColors || {}),
      ...(theme.nodeColors || {})
    },
    typeColors: {
      ...(cfg.typeColors || {}),
      ...(theme.typeColors || {})
    },
    home: theme.home || cfg.home || null,
  });
});



app.get('/api/config/map', (req, res) => {
  const defaults = config.mapDefaults || {};
  res.json({
    center: defaults.center || [37.45, -122.0],
    zoom: defaults.zoom || 9
  });
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
    cache: { size: cache.size, hits: cache.hits, misses: cache.misses, staleHits: cache.staleHits, recomputes: cache.recomputes, hitRate: cache.hits + cache.misses > 0 ? Math.round(cache.hits / (cache.hits + cache.misses) * 1000) / 10 : 0 },
    packetStore: pktStore.getStats(),
  });
});

app.post('/api/perf/reset', requireApiKey, (req, res) => { perfStats.reset(); res.json({ ok: true }); });

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

// Manual WAL checkpoint every 5 minutes (auto-checkpoint disabled to avoid random event loop spikes)
setInterval(() => {
  try {
    const t0 = Date.now();
    db.db.pragma('wal_checkpoint(PASSIVE)'); // PASSIVE = non-blocking, won't stall writers
    const ms = Date.now() - t0;
    if (ms > 50) console.log(`[wal] checkpoint: ${ms}ms`);
  } catch (e) { console.error('[wal] checkpoint error:', e.message); }
}, 300000).unref();

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
  const MAX_HOP_DIST = MAX_HOP_DIST_SERVER;

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
// Build list of MQTT sources: supports single config.mqtt (legacy) or config.mqttSources array
const mqttSources = [];
if (config.mqttSources && Array.isArray(config.mqttSources)) {
  mqttSources.push(...config.mqttSources);
} else if (config.mqtt && config.mqtt.broker) {
  // Legacy single-broker config
  mqttSources.push({
    name: 'default',
    broker: config.mqtt.broker,
    topics: [config.mqtt.topic, 'meshcore/#'],
  });
}

for (const source of mqttSources) {
  try {
    const opts = { reconnectPeriod: 5000 };
    if (source.username) opts.username = source.username;
    if (source.password) opts.password = source.password;
    if (source.rejectUnauthorized === false) opts.rejectUnauthorized = false;

    const client = mqtt.connect(source.broker, opts);
    const tag = source.name || source.broker;

    client.on('connect', () => {
      console.log(`MQTT [${tag}] connected to ${source.broker}`);
      const topics = Array.isArray(source.topics) ? source.topics : [source.topics || 'meshcore/#'];
      for (const t of topics) {
        client.subscribe(t, (err) => {
          if (err) console.error(`MQTT [${tag}] subscribe error for ${t}:`, err);
          else console.log(`MQTT [${tag}] subscribed to ${t}`);
        });
      }
    });
    client.on('error', (e) => console.error(`MQTT [${tag}] error:`, e.message));
    client.on('offline', () => console.log(`MQTT [${tag}] offline`));
    client.on('message', (topic, message) => {
    try {
      const msg = JSON.parse(message.toString());
      const parts = topic.split('/');
      const now = new Date().toISOString();

      // IATA filter: if source has iataFilter, only accept matching regions
      const region = parts[1] || null;
      if (source.iataFilter && Array.isArray(source.iataFilter) && region) {
        if (!source.iataFilter.includes(region)) return;
      }

      // --- Status topic: meshcore/<region>/<observer_id>/status ---
      if (parts[3] === 'status' && parts[2]) {
        const observerId = parts[2];
        const name = msg.origin || null;
        const iata = region;
        // Parse radio string: "freq,bw,sf,cr"
        let radioInfo = null;
        if (msg.radio) {
          const rp = msg.radio.split(',');
          radioInfo = { freq: parseFloat(rp[0]), bw: parseFloat(rp[1]), sf: parseInt(rp[2]), cr: parseInt(rp[3]) };
        }
        db.updateObserverStatus({
          id: observerId,
          name: name,
          iata: iata,
          model: msg.model || null,
          firmware: msg.firmware_version || null,
          client_version: msg.client_version || null,
          radio: msg.radio || null,
          battery_mv: msg.stats?.battery_mv || null,
          uptime_secs: msg.stats?.uptime_secs || null,
          noise_floor: msg.stats?.noise_floor || null,
        });
        console.log(`MQTT [${tag}] status: ${name || observerId} (${iata}) - ${msg.status}`);
        return;
      }

      // --- Format 1: Raw packet logging (meshcoretomqtt / Cisien format) ---
      // Topic: meshcore/<region>/<observer>/packets, payload: { raw, SNR, RSSI, hash }
      if (msg.raw && typeof msg.raw === 'string') {
        const decoded = decoder.decodePacket(msg.raw, channelKeys);
        const observerId = parts[2] || null;
        const region = parts[1] || null;

        const pktData = {
          raw_hex: msg.raw,
          timestamp: now,
          observer_id: observerId,
          observer_name: msg.origin || null,
          snr: msg.SNR ?? null,
          rssi: msg.RSSI ?? null,
          hash: computeContentHash(msg.raw),
          route_type: decoded.header.routeType,
          payload_type: decoded.header.payloadType,
          payload_version: decoded.header.payloadVersion,
          path_json: JSON.stringify(decoded.path.hops),
          decoded_json: JSON.stringify(decoded.payload),
        };
        const packetId = pktStore.insert(pktData); _updateHashSizeForPacket(pktData);
        try { db.insertTransmission(pktData); } catch (e) { console.error('[dual-write] transmission insert error:', e.message); }

        if (decoded.path.hops.length > 0) {
          // Auto-create stub nodes from 2+ byte path hops
          autoLearnHopNodes(decoded.path.hops, now);
        }

        if (decoded.header.payloadTypeName === 'ADVERT' && decoded.payload.pubKey) {
          const p = decoded.payload;
          const validation = decoder.validateAdvert(p);
          if (validation.valid) {
            const role = p.flags ? (p.flags.repeater ? 'repeater' : p.flags.room ? 'room' : p.flags.sensor ? 'sensor' : 'companion') : 'companion';
            db.upsertNode({ public_key: p.pubKey, name: p.name || null, role, lat: p.lat, lon: p.lon, last_seen: now });
            // Invalidate this node's caches on advert
            cache.invalidate('node:' + p.pubKey);
            cache.invalidate('health:' + p.pubKey);
            cache.invalidate('bulk-health');

            // Cross-reference: if this node's pubkey matches an existing observer, backfill observer name
            if (p.name && p.pubKey) {
              const existingObs = db.db.prepare('SELECT id FROM observers WHERE id = ?').get(p.pubKey);
              if (existingObs) db.updateObserverStatus({ id: p.pubKey, name: p.name });
            }
          } else {
            console.warn(`[advert] Skipping corrupted ADVERT from ${tag}: ${validation.reason} (raw: ${msg.raw.slice(0, 40)}…)`);
          }
        }

        if (observerId) {
          db.upsertObserver({ id: observerId, name: msg.origin || null, iata: region });
        }


    // Invalidate caches on new data
    cache.debouncedInvalidateAll();

        const fullPacket = pktStore.getById(packetId) || pktStore.byHash.get(pktData.hash) || pktData;
        const tx = pktStore.byHash.get(pktData.hash);
        const observation_count = tx ? tx.observation_count : 1;
        const broadcastData = { id: packetId, raw: msg.raw, decoded, snr: msg.SNR, rssi: msg.RSSI, hash: pktData.hash, observer: observerId, packet: fullPacket, observation_count };
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

          // Validate companion bridge adverts too
          const bridgeAdvert = { pubKey: pubKey, name, lat, lon, timestamp: Math.floor(Date.now() / 1000), flags: advert.flags || null };
          const validation = decoder.validateAdvert(bridgeAdvert);
          if (!validation.valid) {
            console.warn(`[advert] Skipping corrupted companion ADVERT: ${validation.reason}`);
            return;
          }

          db.upsertNode({ public_key: pubKey, name, role, lat, lon, last_seen: now });
          
          const advertPktData = {
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
          };
          const packetId = pktStore.insert(advertPktData); _updateHashSizeForPacket(advertPktData);
          try { db.insertTransmission(advertPktData); } catch (e) { console.error('[dual-write] transmission insert error:', e.message); }
          broadcast({ type: 'packet', data: { id: packetId, hash: advertPktData.hash, raw: advertPktData.raw_hex, decoded: { header: { payloadTypeName: 'ADVERT' }, payload: advert } } });
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
        const chPktData = {
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
        };
        const packetId = pktStore.insert(chPktData); _updateHashSizeForPacket(chPktData);
        try { db.insertTransmission(chPktData); } catch (e) { console.error('[dual-write] transmission insert error:', e.message); }
        broadcast({ type: 'packet', data: { id: packetId, hash: chPktData.hash, raw: chPktData.raw_hex, decoded: { header: { payloadTypeName: 'GRP_TXT' }, payload: channelMsg } } });
        broadcast({ type: 'message', data: { id: packetId, hash: chPktData.hash, decoded: { header: { payloadTypeName: 'GRP_TXT' }, payload: channelMsg } } });
        return;
      }

      // Handle direct messages
      if (topic.startsWith('meshcore/message/direct/')) {
        const dm = msg.payload || msg;
        const dmPktData = {
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
        };
        const packetId = pktStore.insert(dmPktData); _updateHashSizeForPacket(dmPktData);
        try { db.insertTransmission(dmPktData); } catch (e) { console.error('[dual-write] transmission insert error:', e.message); }
        broadcast({ type: 'packet', data: { id: packetId, hash: dmPktData.hash, raw: dmPktData.raw_hex, decoded: { header: { payloadTypeName: 'TXT_MSG' }, payload: dm } } });
        return;
      }

      // Handle traceroute
      if (topic.startsWith('meshcore/traceroute/')) {
        const trace = msg.payload || msg;
        const tracePktData = {
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
        };
        const packetId = pktStore.insert(tracePktData); _updateHashSizeForPacket(tracePktData);
        try { db.insertTransmission(tracePktData); } catch (e) { console.error('[dual-write] transmission insert error:', e.message); }
        broadcast({ type: 'packet', data: { id: packetId, hash: tracePktData.hash, raw: tracePktData.raw_hex, decoded: { header: { payloadTypeName: 'TRACE' }, payload: trace } } });
        return;
      }

    } catch (e) {
      if (topic !== 'meshcore/status' && topic !== 'meshcore/events/connection') {
        console.error(`MQTT [${tag}] handler error [${topic}]:`, e.message);
        try { console.error('  payload:', message.toString().substring(0, 200)); } catch {}
      }
    }
  });
  } catch (e) {
    console.error(`MQTT [${source.name || source.broker}] connection failed (non-fatal):`, e.message);
  }
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
  const { limit = 50, offset = 0, type, route, region, observer, hash, since, until, groupByHash, node, nodes } = req.query;
  const order = req.query.order === 'asc' ? 'ASC' : 'DESC';

  // Multi-node filter: comma-separated pubkeys
  if (nodes) {
    const pubkeys = nodes.split(',').map(s => s.trim()).filter(Boolean);
    const allPackets = new Map();
    for (const pk of pubkeys) {
      const { packets: found } = pktStore.findPacketsForNode(pk);
      for (const p of found) allPackets.set(p.id, p);
    }
    let results = [...allPackets.values()].sort((a, b) => order === 'DESC' ? b.timestamp.localeCompare(a.timestamp) : a.timestamp.localeCompare(b.timestamp));
    // Apply additional filters (type/observer filtering done client-side; server only filters for nodes query path)
    if (region) results = results.filter(p => (p.observer_id || '').includes(region) || (p.decoded_json || '').includes(region));
    if (since) results = results.filter(p => p.timestamp >= since);
    if (until) results = results.filter(p => p.timestamp <= until);
    const total = results.length;
    const paged = results.slice(Number(offset), Number(offset) + Number(limit));
    return res.json({ packets: paged, total, limit: Number(limit), offset: Number(offset) });
  }

  // groupByHash is now the default behavior (transmissions ARE grouped) — keep param for compat
  if (groupByHash === 'true') {
    return res.json(pktStore.queryGrouped({ limit, offset, type, route, region, observer, hash, since, until, node }));
  }

  const expand = req.query.expand;
  const result = pktStore.query({ limit, offset, type, route, region, observer, hash, since, until, node, order });

  // Strip observations[] from default response for bandwidth; include with ?expand=observations
  if (expand !== 'observations') {
    result.packets = result.packets.map(p => {
      const { observations, ...rest } = p;
      return rest;
    });
  }

  res.json(result);
});

// Lightweight endpoint: just timestamps for timeline sparkline
app.get('/api/packets/timestamps', (req, res) => {
  const { since } = req.query;
  if (!since) return res.status(400).json({ error: 'since required' });
  res.json(pktStore.getTimestamps(since));
});

app.get('/api/packets/:id', (req, res) => {
  const param = req.params.id;
  const isHash = /^[0-9a-f]{16}$/i.test(param);
  let packet;
  if (isHash) {
    // Hash-based lookup
    const tx = pktStore.byHash.get(param.toLowerCase());
    packet = tx || null;
  }
  if (!packet) {
    const id = Number(param);
    if (!isNaN(id)) {
      // Try transmission ID first (what the UI sends), then observation ID, then legacy
      packet = pktStore.getByTxId(id) || pktStore.getById(id) || db.getPacket(id);
    }
  }
  if (!packet) return res.status(404).json({ error: 'Not found' });

    // Note: packet.path_json reflects the first observer's path (earliest first_seen).
  // Individual observation paths are in siblingObservations below.

  const pathHops = packet.paths || [];
  let decoded;
  try { decoded = JSON.parse(packet.decoded_json); } catch { decoded = null; }

  // Build byte breakdown
  const breakdown = buildBreakdown(packet.raw_hex, decoded);

  // Include sibling observations for this transmission
  const transmission = packet.hash ? pktStore.byHash.get(packet.hash) : null;
  const siblingObservations = transmission ? transmission.observations : [];
  const observation_count = transmission ? transmission.observation_count : 1;

  res.json({ packet, path: pathHops, breakdown, observation_count, observations: siblingObservations });
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

app.post('/api/packets', requireApiKey, (req, res) => {
  try {
    const { hex, observer, snr, rssi, region, hash } = req.body;
    if (!hex) return res.status(400).json({ error: 'hex is required' });

    const decoded = decoder.decodePacket(hex, channelKeys);
    const now = new Date().toISOString();

    const apiPktData = {
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
    };
    const packetId = pktStore.insert(apiPktData); _updateHashSizeForPacket(apiPktData);
    try { db.insertTransmission(apiPktData); } catch (e) { console.error('[dual-write] transmission insert error:', e.message); }

    if (decoded.path.hops.length > 0) {
      autoLearnHopNodes(decoded.path.hops, new Date().toISOString());
    }

    if (decoded.header.payloadTypeName === 'ADVERT' && decoded.payload.pubKey) {
      const p = decoded.payload;
      const validation = decoder.validateAdvert(p);
      if (validation.valid) {
        const role = p.flags ? (p.flags.repeater ? 'repeater' : p.flags.room ? 'room' : p.flags.sensor ? 'sensor' : 'companion') : 'companion';
        db.upsertNode({ public_key: p.pubKey, name: p.name || null, role, lat: p.lat, lon: p.lon, last_seen: now });
      } else {
        console.warn(`[advert] Skipping corrupted ADVERT (API): ${validation.reason}`);
      }
    }

    if (observer) {
      db.upsertObserver({ id: observer, iata: region || null });
    }


    // Invalidate caches on new data
    cache.debouncedInvalidateAll();

    broadcast({ type: 'packet', data: { id: packetId, hash: apiPktData.hash, raw: apiPktData.raw_hex, decoded } });

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

  // Region filtering: if region param is set, only include nodes whose ADVERTs were seen by regional observers
  const regionObsIds = getObserverIdsForRegions(region);
  let regionNodeKeys = null;
  if (regionObsIds && regionObsIds.size > 0) {
    regionNodeKeys = pktStore.getNodesByAdvertObservers(regionObsIds);
  }

  const clause = where.length ? 'WHERE ' + where.join(' AND ') : '';
  const sortMap = { name: 'name ASC', lastSeen: 'last_seen DESC', packetCount: 'advert_count DESC' };
  const order = sortMap[sortBy] || 'last_seen DESC';

  let nodes, total, filteredAll;
  if (regionNodeKeys) {
    const allNodes = db.db.prepare(`SELECT * FROM nodes ${clause} ORDER BY ${order}`).all(params);
    filteredAll = allNodes.filter(n => regionNodeKeys.has(n.public_key));
    total = filteredAll.length;
    nodes = filteredAll.slice(Number(offset), Number(offset) + Number(limit));
  } else {
    nodes = db.db.prepare(`SELECT * FROM nodes ${clause} ORDER BY ${order} LIMIT @limit OFFSET @offset`).all({ ...params, limit: Number(limit), offset: Number(offset) });
    total = db.db.prepare(`SELECT COUNT(*) as count FROM nodes ${clause}`).get(params).count;
    filteredAll = null;
  }

  const counts = {};
  if (filteredAll) {
    for (const r of ['repeater', 'room', 'companion', 'sensor']) {
      counts[r + 's'] = filteredAll.filter(n => n.role === r).length;
    }
  } else {
    for (const r of ['repeater', 'room', 'companion', 'sensor']) {
      counts[r + 's'] = db.db.prepare(`SELECT COUNT(*) as count FROM nodes WHERE role = ?`).get(r).count;
    }
  }

  // Use precomputed hash_size map (rebuilt at startup, updated on new packets)
  for (const node of nodes) {
    node.hash_size = _hashSizeMap.get(node.public_key) || null;
    const allSizes = _hashSizeAllMap.get(node.public_key);
    node.hash_size_inconsistent = _isHashSizeFlipFlop(node.public_key);
    if (allSizes && allSizes.size > 1) node.hash_sizes_seen = [...allSizes].sort();
    // Compute lastHeard from in-memory packets (more accurate than DB last_seen)
    const nodePkts = pktStore.byNode.get(node.public_key);
    if (nodePkts && nodePkts.length > 0) {
      let latest = null;
      for (const p of nodePkts) {
        if (!latest || p.timestamp > latest) latest = p.timestamp;
      }
      if (latest) node.last_heard = latest;
    }
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
  const regionKey = req.query.region || '';
  const _ck = 'bulk-health:' + limit + ':r=' + regionKey;
  const _c = cache.get(_ck); if (_c) return res.json(_c);

  // Region filtering
  const regionObsIds = getObserverIdsForRegions(req.query.region);
  let regionNodeKeys = null;
  let regionalHashes = null;
  if (regionObsIds) {
    regionalHashes = new Set();
    for (const obsId of regionObsIds) {
      const obs = pktStore.byObserver.get(obsId);
      if (obs) for (const o of obs) regionalHashes.add(o.hash);
    }
    regionNodeKeys = new Set();
    for (const [pubkey, hashes] of pktStore._nodeHashIndex) {
      for (const h of hashes) {
        if (regionalHashes.has(h)) { regionNodeKeys.add(pubkey); break; }
      }
    }
  }

  let nodes = db.db.prepare(`SELECT * FROM nodes ORDER BY last_seen DESC LIMIT ?`).all(regionNodeKeys ? 500 : limit);
  if (regionNodeKeys) {
    nodes = nodes.filter(n => regionNodeKeys.has(n.public_key)).slice(0, limit);
  }
  if (nodes.length === 0) { cache.set(_ck, [], TTL.bulkHealth); return res.json([]); }

  const todayStart = new Date();
  todayStart.setUTCHours(0, 0, 0, 0);
  const todayISO = todayStart.toISOString();

  const results = [];
  for (const node of nodes) {
    const packets = pktStore.byNode.get(node.public_key) || [];
    let packetsToday = 0, snrSum = 0, snrCount = 0, lastHeard = null;
    const observers = {};
    let totalObservations = 0;

    for (const pkt of packets) {
      totalObservations += pkt.observation_count || 1;
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
      stats: {
        totalTransmissions: packets.length,
        totalObservations,
        totalPackets: packets.length, // backward compat
        packetsToday, avgSnr: snrCount ? snrSum / snrCount : null, lastHeard
      },
      observers: observerRows
    });
  }

  cache.set(_ck, results, TTL.bulkHealth);
  res.json(results);
});

app.get('/api/nodes/network-status', (req, res) => {
  const now = Date.now();
  let allNodes = db.db.prepare('SELECT public_key, name, role, last_seen FROM nodes').all();

  // Region filtering
  const regionObsIds = getObserverIdsForRegions(req.query.region);
  if (regionObsIds) {
    const regionalHashes = new Set();
    for (const obsId of regionObsIds) {
      const obs = pktStore.byObserver.get(obsId);
      if (obs) for (const o of obs) regionalHashes.add(o.hash);
    }
    const regionNodeKeys = new Set();
    for (const [pubkey, hashes] of pktStore._nodeHashIndex) {
      for (const h of hashes) {
        if (regionalHashes.has(h)) { regionNodeKeys.add(pubkey); break; }
      }
    }
    allNodes = allNodes.filter(n => regionNodeKeys.has(n.public_key));
  }

  let active = 0, degraded = 0, silent = 0;
  const roleCounts = {};
  allNodes.forEach(n => {
    const r = n.role || 'unknown';
    roleCounts[r] = (roleCounts[r] || 0) + 1;
    const ls = n.last_seen ? new Date(n.last_seen).getTime() : 0;
    const age = now - ls;
    const isInfra = r === 'repeater' || r === 'room';
    const { degradedMs, silentMs } = getHealthMs(r);
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
  node.hash_size = _hashSizeMap.get(pubkey) || null;
  const allSizes = _hashSizeAllMap.get(pubkey);
  node.hash_size_inconsistent = _isHashSizeFlipFlop(pubkey);
  if (allSizes && allSizes.size > 1) node.hash_sizes_seen = [...allSizes].sort();
  const recentAdverts = (pktStore.byNode.get(pubkey) || []).slice(-20).reverse();
  const _nResult = { node, recentAdverts };
  cache.set(_ck, _nResult, TTL.nodeDetail);
  res.json(_nResult);
});

// --- Analytics API ---
// --- RF Analytics ---
app.get('/api/analytics/rf', (req, res) => {
  const { region } = req.query;
  const regionObsIds = getObserverIdsForRegions(region);
  const _ck = 'analytics:rf' + (region ? ':' + region : '');
  const _c = cache.get(_ck); if (_c) return res.json(_c);
  const PTYPES = { 0:'REQ',1:'RESPONSE',2:'TXT_MSG',3:'ACK',4:'ADVERT',5:'GRP_TXT',7:'ANON_REQ',8:'PATH',9:'TRACE',11:'CONTROL' };

  // Step 1: Get ALL regional observations (no SNR requirement) — for general stats
  // Step 2: Filter by SNR for signal-specific stats
  // When no region filter, use all transmissions directly for backward compat
  let allRegional, signalPackets;
  if (regionObsIds) {
    // Collect observations from regional observers via byObserver index
    allRegional = [];
    for (const obsId of regionObsIds) {
      const obs = pktStore.byObserver.get(obsId);
      if (obs) allRegional.push(...obs);
    }
    signalPackets = allRegional.filter(p => p.snr != null);
  } else {
    // No region filter — flatten all observations from all transmissions
    allRegional = [];
    for (const tx of pktStore.packets) {
      if (tx.observations && tx.observations.length) {
        allRegional.push(...tx.observations);
      } else {
        allRegional.push(tx); // legacy packets without observations
      }
    }
    signalPackets = allRegional.filter(p => p.snr != null);
  }

  // Unique transmission hashes in the regional set
  const regionalHashes = new Set(allRegional.map(p => p.hash).filter(Boolean));

  const snrVals = signalPackets.map(p => p.snr).filter(v => v != null);
  const rssiVals = signalPackets.map(p => p.rssi).filter(v => v != null);
  // Packet sizes from ALL regional observations (use unique hashes to avoid double-counting)
  const seenSizeHashes = new Set();
  const packetSizes = [];
  for (const p of allRegional) {
    if (p.raw_hex && p.hash && !seenSizeHashes.has(p.hash)) {
      seenSizeHashes.add(p.hash);
      packetSizes.push(p.raw_hex.length / 2);
    }
  }

  const sorted = arr => [...arr].sort((a, b) => a - b);
  const median = arr => { const s = sorted(arr); return s.length ? s[Math.floor(s.length/2)] : 0; };
  const stddev = (arr, avg) => Math.sqrt(arr.reduce((s, v) => s + (v - avg) ** 2, 0) / Math.max(arr.length, 1));
  const arrMin = arr => { let m = Infinity; for (const v of arr) if (v < m) m = v; return m === Infinity ? 0 : m; };
  const arrMax = arr => { let m = -Infinity; for (const v of arr) if (v > m) m = v; return m === -Infinity ? 0 : m; };

  const snrAvg = snrVals.reduce((a, b) => a + b, 0) / Math.max(snrVals.length, 1);
  const rssiAvg = rssiVals.reduce((a, b) => a + b, 0) / Math.max(rssiVals.length, 1);

  // Packets per hour — from ALL regional observations
  const hourBuckets = {};
  allRegional.forEach(p => {
    const ts = p.timestamp || p.obs_timestamp;
    if (!ts) return;
    const hr = ts.slice(0, 13);
    hourBuckets[hr] = (hourBuckets[hr] || 0) + 1;
  });
  const packetsPerHour = Object.entries(hourBuckets).sort().map(([hour, count]) => ({ hour, count }));

  // Payload type distribution — from ALL regional (unique by hash to count transmissions)
  const seenTypeHashes = new Set();
  const typeBuckets = {};
  allRegional.forEach(p => {
    if (p.hash && !seenTypeHashes.has(p.hash)) {
      seenTypeHashes.add(p.hash);
      typeBuckets[p.payload_type] = (typeBuckets[p.payload_type] || 0) + 1;
    }
  });
  const payloadTypes = Object.entries(typeBuckets)
    .map(([type, count]) => ({ type: +type, name: PTYPES[type] || `UNK(${type})`, count }))
    .sort((a, b) => b.count - a.count);

  // SNR by payload type — from signal-filtered subset
  const snrByType = {};
  signalPackets.forEach(p => {
    const name = PTYPES[p.payload_type] || `UNK(${p.payload_type})`;
    if (!snrByType[name]) snrByType[name] = { vals: [] };
    snrByType[name].vals.push(p.snr);
  });
  const snrByTypeArr = Object.entries(snrByType).map(([name, d]) => ({
    name, count: d.vals.length,
    avg: d.vals.reduce((a, b) => a + b, 0) / d.vals.length,
    min: arrMin(d.vals), max: arrMax(d.vals)
  })).sort((a, b) => b.count - a.count);

  // Signal over time — from signal-filtered subset
  const sigTime = {};
  signalPackets.forEach(p => {
    const ts = p.timestamp || p.obs_timestamp;
    if (!ts) return;
    const hr = ts.slice(0, 13);
    if (!sigTime[hr]) sigTime[hr] = { snrs: [], count: 0 };
    sigTime[hr].snrs.push(p.snr);
    sigTime[hr].count++;
  });
  const signalOverTime = Object.entries(sigTime).sort().map(([hour, d]) => ({
    hour, count: d.count, avgSnr: d.snrs.reduce((a, b) => a + b, 0) / d.snrs.length
  }));

  // Scatter data (SNR vs RSSI) — downsample to max 500 points
  const scatterAll = signalPackets.filter(p => p.snr != null && p.rssi != null);
  const scatterStep = Math.max(1, Math.floor(scatterAll.length / 500));
  const scatterData = scatterAll.filter((_, i) => i % scatterStep === 0).map(p => ({ snr: p.snr, rssi: p.rssi }));

  // Pre-compute histograms server-side so we don't send raw arrays
  function buildHistogram(values, bins) {
    if (!values.length) return { bins: [], min: 0, max: 0 };
    const min = arrMin(values), max = arrMax(values);
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

  const times = allRegional.map(p => new Date(p.timestamp || p.obs_timestamp).getTime()).filter(t => !isNaN(t));
  const timeSpanHours = times.length ? (arrMax(times) - arrMin(times)) / 3600000 : 0;

  const _rfResult = {
    totalPackets: signalPackets.length,
    totalAllPackets: allRegional.length,
    totalTransmissions: regionalHashes.size,
    snr: snrVals.length ? { min: arrMin(snrVals), max: arrMax(snrVals), avg: snrAvg, median: median(snrVals), stddev: stddev(snrVals, snrAvg) } : { min: 0, max: 0, avg: 0, median: 0, stddev: 0 },
    rssi: rssiVals.length ? { min: arrMin(rssiVals), max: arrMax(rssiVals), avg: rssiAvg, median: median(rssiVals), stddev: stddev(rssiVals, rssiAvg) } : { min: 0, max: 0, avg: 0, median: 0, stddev: 0 },
    snrValues: snrHistogram, rssiValues: rssiHistogram, packetSizes: sizeHistogram,
    minPacketSize: packetSizes.length ? arrMin(packetSizes) : 0,
    maxPacketSize: packetSizes.length ? arrMax(packetSizes) : 0,
    avgPacketSize: packetSizes.length ? Math.round(packetSizes.reduce((a, b) => a + b, 0) / packetSizes.length) : 0,
    packetsPerHour, payloadTypes, snrByType: snrByTypeArr, signalOverTime, scatterData, timeSpanHours
  };
  cache.set(_ck, _rfResult, TTL.analyticsRF);
  res.json(_rfResult);
});

// --- Topology Analytics ---
app.get('/api/analytics/topology', (req, res) => {
  const { region } = req.query;
  const regionObsIds = getObserverIdsForRegions(region);
  const _ck = 'analytics:topology' + (region ? ':' + region : '');
  const _c = cache.get(_ck); if (_c) return res.json(_c);
  const packets = pktStore.filter(p => p.path_json && p.path_json !== '[]' && (!regionObsIds || regionObsIds.has(p.observer_id)));
  const allNodes = getCachedNodes(false);

  // Build prefix map for O(1) hop resolution (same pattern as distance endpoint)
  const topoPrefixMap = new Map();
  for (const n of allNodes) {
    const pk = n.public_key.toLowerCase();
    for (let len = 2; len <= pk.length; len++) {
      const pfx = pk.slice(0, len);
      if (!topoPrefixMap.has(pfx)) topoPrefixMap.set(pfx, []);
      topoPrefixMap.get(pfx).push(n);
    }
  }
  const topoHopCache = new Map();
  const resolveHop = (hop, contextPositions) => {
    if (topoHopCache.has(hop)) return topoHopCache.get(hop);
    const h = hop.toLowerCase();
    const candidates = topoPrefixMap.get(h);
    if (!candidates || candidates.length === 0) { topoHopCache.set(hop, null); return null; }
    let result;
    if (candidates.length === 1) { result = { name: candidates[0].name, pubkey: candidates[0].public_key }; }
    else if (contextPositions && contextPositions.length > 0) {
      const cLat = contextPositions.reduce((s, p) => s + p.lat, 0) / contextPositions.length;
      const cLon = contextPositions.reduce((s, p) => s + p.lon, 0) / contextPositions.length;
      const withLoc = candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
      if (withLoc.length) {
        withLoc.sort((a, b) => Math.hypot(a.lat - cLat, a.lon - cLon) - Math.hypot(b.lat - cLat, b.lon - cLon));
        result = { name: withLoc[0].name, pubkey: withLoc[0].public_key };
      } else { result = { name: candidates[0].name, pubkey: candidates[0].public_key }; }
    } else { result = { name: candidates[0].name, pubkey: candidates[0].public_key }; }
    // Only cache when no context positions (context-dependent results vary)
    if (!contextPositions || contextPositions.length === 0) topoHopCache.set(hop, result);
    return result;
  };

  // Hop distribution
  const hopCounts = {};
  const allHopsList = [];
  const hopSnr = {};
  const hopFreq = {};
  const pairFreq = {};
  packets.forEach(p => {
    const hops = p._parsedPath || (p._parsedPath = JSON.parse(p.path_json));
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
  const observerMap = new Map(); packets.forEach(p => { if (p.observer_id) observerMap.set(p.observer_id, p.observer_name); }); const observers = [...observerMap].map(([observer_id, observer_name]) => ({ observer_id, observer_name }));

  // Per-observer: node → min hop distance seen from that observer
  const perObserver = {}; // observer_id → { hop_hex → { minDist, maxDist, count } }
  const bestPath = {};    // hop_hex → { minDist, observer }
  const crossObserver = {}; // hop_hex → [ { observer_id, observer_name, minDist, count } ]

  packets.forEach(p => {
    const obsId = p.observer_id;
    if (!perObserver[obsId]) perObserver[obsId] = {};
    const hops = p._parsedPath || (p._parsedPath = JSON.parse(p.path_json));
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
  cache.set(_ck, _topoResult, TTL.analyticsTopology);
  res.json(_topoResult);
});

// --- Channel Analytics ---
app.get('/api/analytics/channels', (req, res) => {
  const { region } = req.query;
  const regionObsIds = getObserverIdsForRegions(region);
  const _ck = 'analytics:channels' + (region ? ':' + region : '');
  const _c = cache.get(_ck); if (_c) return res.json(_c);
  const packets = pktStore.filter(p => p.payload_type === 5 && p.decoded_json && (!regionObsIds || regionObsIds.has(p.observer_id)));

  const channels = {};
  const senderCounts = {};
  const msgLengths = [];
  const timeline = {};

  packets.forEach(p => {
    try {
      const d = p._parsedDecoded || (p._parsedDecoded = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json) : p.decoded_json);
      const hash = d.channelHash || d.channel_hash || '?';
      const name = d.channelName || (d.type === 'CHAN' ? (d.channel || `ch${hash}`) : `ch${hash}`);
      const encrypted = !d.text && !d.sender;
      // Use channel name as key when available to distinguish channels with same hash (#108)
      const chKey = (d.type === 'CHAN' && d.channel) ? `${hash}_${d.channel}` : String(hash);

      if (!channels[chKey]) channels[chKey] = { hash, name, messages: 0, senders: new Set(), lastActivity: p.timestamp, encrypted };
      channels[chKey].messages++;
      channels[chKey].lastActivity = p.timestamp;
      if (!encrypted) channels[chKey].encrypted = false;

      if (d.sender) {
        channels[chKey].senders.add(d.sender);
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
  cache.set(_ck, _chanResult, TTL.analyticsChannels);
  res.json(_chanResult);
});

app.get('/api/analytics/distance', (req, res) => {
  const { region } = req.query;
  const regionObsIds = getObserverIdsForRegions(region);
  const _ck = 'analytics:distance' + (region ? ':' + region : '');
  const _c = cache.get(_ck); if (_c) return res.json(_c);

  const arrMin = arr => { let m = Infinity; for (const v of arr) if (v < m) m = v; return m === Infinity ? 0 : m; };
  const arrMax = arr => { let m = -Infinity; for (const v of arr) if (v > m) m = v; return m === -Infinity ? 0 : m; };
  const median = arr => { if (!arr.length) return 0; const s = [...arr].sort((a,b)=>a-b); return s[Math.floor(s.length/2)]; };

  function haversine(lat1, lon1, lat2, lon2) {
    const R = 6371;
    const dLat = (lat2 - lat1) * Math.PI / 180;
    const dLon = (lon2 - lon1) * Math.PI / 180;
    const a = Math.sin(dLat/2)**2 + Math.cos(lat1*Math.PI/180) * Math.cos(lat2*Math.PI/180) * Math.sin(dLon/2)**2;
    return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1-a));
  }

  const allNodes = getCachedNodes(true);
  const nodeByPk = new Map(allNodes.map(n => [n.public_key, n]));

  // Build prefix map for O(1) hop resolution instead of O(N) filter per hop
  const prefixMap = new Map(); // lowercase prefix → [nodes]
  for (const n of allNodes) {
    const pk = n.public_key.toLowerCase();
    // Index all prefixes from length 2 to full key length
    for (let len = 2; len <= pk.length; len++) {
      const pfx = pk.slice(0, len);
      if (!prefixMap.has(pfx)) prefixMap.set(pfx, []);
      prefixMap.get(pfx).push(n);
    }
  }

  // Cache resolved hops to avoid re-resolving same hex prefix
  const hopCache = new Map();
  const resolveHop = (hop) => {
    if (hopCache.has(hop)) return hopCache.get(hop);
    const h = hop.toLowerCase();
    const candidates = prefixMap.get(h);
    let result = null;
    if (candidates && candidates.length === 1) result = candidates[0];
    else if (candidates && candidates.length > 1) {
      const withLoc = candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
      result = withLoc.length ? withLoc[0] : candidates[0];
    }
    hopCache.set(hop, result);
    return result;
  };

  // Pre-compute repeater status
  const repeaterSet = new Set();
  for (const n of allNodes) {
    if (n.role && n.role.toLowerCase().includes('repeater')) repeaterSet.add(n.public_key);
  }
  const validGps = n => n && n.lat != null && n.lon != null && !(n.lat === 0 && n.lon === 0);
  const isRepeater = n => n && repeaterSet.has(n.public_key);

  const packets = pktStore.filter(p => p.path_json && p.path_json !== '[]' && (!regionObsIds || regionObsIds.has(p.observer_id)));

  // Collect hops with distances
  const allHops = [];       // { from, to, dist, type, snr, hash, timestamp }
  const pathTotals = [];    // { hash, totalDist, hopCount, timestamp, hops: [{from,to,dist}] }
  const catDists = { 'R↔R': [], 'C↔R': [], 'C↔C': [] };
  const distByHour = {};    // hourBucket → [distances]

  for (const p of packets) {
    let hops;
    try {
      hops = p._parsedPath || (p._parsedPath = JSON.parse(p.path_json));
    } catch { continue; }
    if (!hops.length) continue;

    // Resolve all hops to nodes
    const resolved = hops.map(h => resolveHop(h));

    // Also try to resolve sender from decoded_json
    let senderNode = null;
    if (p.decoded_json) {
      try {
        const dec = p._parsedDecoded || (p._parsedDecoded = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json) : p.decoded_json);
        if (dec.pubKey) senderNode = nodeByPk.get(dec.pubKey) || null;
      } catch {}
    }

    // Build chain: sender → hop0 → hop1 → ... → observer
    // For distance we only measure consecutive hops where both have valid GPS
    const chain = [];
    if (senderNode && validGps(senderNode)) chain.push(senderNode);
    for (const r of resolved) {
      if (r && validGps(r)) chain.push(r);
    }

    if (chain.length < 2) continue;

    const hourBucket = p.timestamp ? new Date(p.timestamp).toISOString().slice(0, 13) : null;
    let pathDist = 0;
    const pathHops = [];

    for (let i = 0; i < chain.length - 1; i++) {
      const a = chain[i], b = chain[i + 1];
      const dist = haversine(a.lat, a.lon, b.lat, b.lon);
      if (dist > 300) continue; // sanity: skip > 300km (LoRa record ~250km)

      const aRep = isRepeater(a), bRep = isRepeater(b);
      let type;
      if (aRep && bRep) type = 'R↔R';
      else if (!aRep && !bRep) type = 'C↔C';
      else type = 'C↔R';

      const hop = { fromName: a.name, fromPk: a.public_key, toName: b.name, toPk: b.public_key, dist: Math.round(dist * 100) / 100, type, snr: p.snr || null, hash: p.hash, timestamp: p.timestamp };
      allHops.push(hop);
      catDists[type].push(dist);
      pathDist += dist;
      pathHops.push({ fromName: a.name, fromPk: a.public_key, toName: b.name, toPk: b.public_key, dist: hop.dist });

      if (hourBucket) {
        if (!distByHour[hourBucket]) distByHour[hourBucket] = [];
        distByHour[hourBucket].push(dist);
      }
    }

    if (pathHops.length > 0) {
      pathTotals.push({ hash: p.hash, totalDist: Math.round(pathDist * 100) / 100, hopCount: pathHops.length, timestamp: p.timestamp, hops: pathHops });
    }
  }

  // Top longest hops
  allHops.sort((a, b) => b.dist - a.dist);
  const topHops = allHops.slice(0, 50);

  // Top longest paths
  pathTotals.sort((a, b) => b.totalDist - a.totalDist);
  const topPaths = pathTotals.slice(0, 20);

  // Category stats
  const catStats = {};
  for (const [cat, dists] of Object.entries(catDists)) {
    if (!dists.length) { catStats[cat] = { count: 0, avg: 0, median: 0, min: 0, max: 0 }; continue; }
    const avg = dists.reduce((s, v) => s + v, 0) / dists.length;
    catStats[cat] = { count: dists.length, avg: Math.round(avg * 100) / 100, median: Math.round(median(dists) * 100) / 100, min: Math.round(arrMin(dists) * 100) / 100, max: Math.round(arrMax(dists) * 100) / 100 };
  }

  // Histogram of all hop distances
  const allDists = allHops.map(h => h.dist);
  let distHistogram = [];
  if (allDists.length) {
    const hMin = arrMin(allDists), hMax = arrMax(allDists);
    const binCount = 25;
    const binW = (hMax - hMin) / binCount || 1;
    const bins = new Array(binCount).fill(0);
    for (const d of allDists) {
      const idx = Math.min(Math.floor((d - hMin) / binW), binCount - 1);
      bins[idx]++;
    }
    distHistogram = { bins: bins.map((count, i) => ({ x: Math.round((hMin + i * binW) * 10) / 10, w: Math.round(binW * 10) / 10, count })), min: hMin, max: hMax };
  }

  // Distance over time
  const timeEntries = Object.entries(distByHour).sort((a, b) => a[0].localeCompare(b[0]));
  const distOverTime = timeEntries.map(([hour, dists]) => ({
    hour,
    avg: Math.round((dists.reduce((s, v) => s + v, 0) / dists.length) * 100) / 100,
    count: dists.length
  }));

  // Summary
  const totalDists = allHops.map(h => h.dist);
  const summary = {
    totalHops: allHops.length,
    totalPaths: pathTotals.length,
    avgDist: totalDists.length ? Math.round((totalDists.reduce((s, v) => s + v, 0) / totalDists.length) * 100) / 100 : 0,
    maxDist: totalDists.length ? Math.round(arrMax(totalDists) * 100) / 100 : 0,
  };

  const _distResult = { summary, topHops, topPaths, catStats, distHistogram, distOverTime };
  cache.set(_ck, _distResult, TTL.analyticsTopology);
  res.json(_distResult);
});

app.get('/api/analytics/hash-sizes', (req, res) => {
  const { region } = req.query;
  const regionObsIds = getObserverIdsForRegions(region);
  const _ck = 'analytics:hash-sizes' + (region ? ':' + region : '');
  const _c = cache.get(_ck); if (_c) return res.json(_c);
  // Get all packets with raw_hex and non-empty paths from memory store
  const packets = pktStore.filter(p => p.raw_hex && p.path_json && p.path_json !== '[]' && (!regionObsIds || regionObsIds.has(p.observer_id)));

  const distribution = { 1: 0, 2: 0, 3: 0 };
  const byHour = {};     // hour bucket → { 1: n, 2: n, 3: n }
  const byNode = {};     // node name/prefix → { hashSize, packets, lastSeen }
  const uniqueHops = {}; // hop hex → { size, count, resolvedName }

  // Resolve all known nodes for hop matching — use prefix map for O(1) lookup
  const allNodes = getCachedNodes(false);
  const hsPrefixMap = new Map();
  for (const n of allNodes) {
    const pk = n.public_key.toLowerCase();
    for (let len = 2; len <= pk.length; len++) {
      const pfx = pk.slice(0, len);
      if (!hsPrefixMap.has(pfx)) hsPrefixMap.set(pfx, []);
      hsPrefixMap.get(pfx).push(n);
    }
  }

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
        const candidates = hsPrefixMap.get(hopLower);
        const match = candidates && candidates.length ? candidates[0] : null;
        uniqueHops[hop] = { size: Math.ceil(hop.length / 2), count: 0, name: match?.name || null, pubkey: match?.public_key || null };
      }
      uniqueHops[hop].count++;
    }

    // Try to identify originator from decoded_json for advert packets
    if (p.payload_type === 4) {
      try {
        const d = p._parsedDecoded || (p._parsedDecoded = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json) : p.decoded_json);
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
  cache.set(_ck, _hsResult, TTL.analyticsHashSizes);
  res.json(_hsResult);
});

// Resolve path hop hex prefixes to node names
app.get('/api/resolve-hops', (req, res) => {
  const hops = (req.query.hops || '').split(',').filter(Boolean);
  const observerId = req.query.observer || null;
  const originLat = req.query.originLat ? parseFloat(req.query.originLat) : null;
  const originLon = req.query.originLon ? parseFloat(req.query.originLon) : null;
  if (!hops.length) return res.json({ resolved: {} });

  const allNodes = getCachedNodes(false);
  const allObservers = db.getObservers();

  // Build observer IATA lookup and regional observer sets
  const observerIataMap = {}; // observer_id → iata
  const observersByIata = {}; // iata → Set<observer_id>
  for (const obs of allObservers) {
    if (obs.iata) {
      observerIataMap[obs.id] = obs.iata;
      if (!observersByIata[obs.iata]) observersByIata[obs.iata] = new Set();
      observersByIata[obs.iata].add(obs.id);
    }
  }

  // Determine this packet's region from its observer
  const packetIata = observerId ? observerIataMap[observerId] : null;
  const regionalObserverIds = packetIata ? observersByIata[packetIata] : null;

  // Helper: check if a node is near the packet's region using layered filtering
  // Layer 1: Node has lat/lon → geographic distance to IATA center (bridge-proof)
  // Layer 2: Node has no lat/lon → observer-based (was ADVERT seen by regional observer)
  // Returns: { near: boolean, method: 'geo'|'observer'|'none', distKm?: number }
  const nodeInRegion = (candidate) => {
    // Layer 1: Geographic check (ground truth, bridge-proof)
    if (packetIata && candidate.lat && candidate.lon && !(candidate.lat === 0 && candidate.lon === 0)) {
      const geoCheck = nodeNearRegion(candidate.lat, candidate.lon, packetIata);
      if (geoCheck) return { near: geoCheck.near, method: 'geo', distKm: geoCheck.distKm };
    }
    // Layer 2: Observer-based check (fallback for nodes without GPS)
    if (regionalObserverIds) {
      const nodeObservers = pktStore._advertByObserver.get(candidate.public_key);
      if (nodeObservers) {
        for (const obsId of nodeObservers) {
          if (regionalObserverIds.has(obsId)) return { near: true, method: 'observer' };
        }
      }
      return { near: false, method: 'observer' };
    }
    // No region info available
    return { near: false, method: 'none' };
  };

  // Build observer geographic position
  let observerLat = null, observerLon = null;
  if (observerId) {
    const obsNode = allNodes.find(n => n.name === observerId);
    if (obsNode && obsNode.lat && obsNode.lon && !(obsNode.lat === 0 && obsNode.lon === 0)) {
      observerLat = obsNode.lat;
      observerLon = obsNode.lon;
    } else {
      const obsNodes = db.db.prepare(`
        SELECT n.lat, n.lon FROM packets_v p
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
  // First pass: find all candidates for each hop, split into regional and global
  for (const hop of hops) {
    const hopLower = hop.toLowerCase();
    const hopByteLen = Math.ceil(hop.length / 2); // 2 hex chars = 1 byte
    const allCandidates = allNodes.filter(n => n.public_key.toLowerCase().startsWith(hopLower));

    if (allCandidates.length === 0) {
      resolved[hop] = { name: null, candidates: [], conflicts: [] };
    } else if (allCandidates.length === 1) {
      const c = allCandidates[0];
      const regionCheck = nodeInRegion(c);
      resolved[hop] = { name: c.name, pubkey: c.public_key,
        candidates: [{ name: c.name, pubkey: c.public_key, lat: c.lat, lon: c.lon, regional: regionCheck.near, filterMethod: regionCheck.method, distKm: regionCheck.distKm }],
        conflicts: [] };
    } else {
      // Multiple candidates — apply layered regional filtering
      const checked = allCandidates.map(c => {
        const r = nodeInRegion(c);
        return { ...c, regional: r.near, filterMethod: r.method, distKm: r.distKm };
      });
      const regional = checked.filter(c => c.regional);
      // Sort by distance to region center — closest first
      regional.sort((a, b) => (a.distKm || 9999) - (b.distKm || 9999));
      const candidates = regional.length > 0 ? regional : checked;
      const globalFallback = regional.length === 0 && checked.length > 0;

      const conflicts = candidates.map(c => ({
        name: c.name, pubkey: c.public_key, lat: c.lat, lon: c.lon,
        regional: c.regional, filterMethod: c.filterMethod, distKm: c.distKm
      }));

      if (candidates.length === 1) {
        resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key,
          candidates: conflicts, conflicts, globalFallback,
          filterMethod: candidates[0].filterMethod };
      } else {
        resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key,
          ambiguous: true, candidates: conflicts, conflicts, globalFallback,
          hopBytes: hopByteLen, totalGlobal: allCandidates.length, totalRegional: regional.length,
          filterMethods: [...new Set(candidates.map(c => c.filterMethod))] };
      }
    }
  }

  const dist = (lat1, lon1, lat2, lon2) => Math.sqrt((lat1 - lat2) ** 2 + (lon1 - lon2) ** 2);

  // Forward pass: resolve each ambiguous hop using previous hop's position
  const hopPositions = {};
  // Seed unambiguous positions
  for (const hop of hops) {
    const r = resolved[hop];
    if (r && !r.ambiguous && r.pubkey) {
      const node = allNodes.find(n => n.public_key === r.pubkey);
      if (node && node.lat && node.lon && !(node.lat === 0 && node.lon === 0)) {
        hopPositions[hop] = { lat: node.lat, lon: node.lon };
      }
    }
  }

  let lastPos = (originLat != null && originLon != null) ? { lat: originLat, lon: originLon } : null;
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

  // Sanity check: drop hops impossibly far from both neighbors
  const MAX_HOP_DIST = MAX_HOP_DIST_SERVER;
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
      const r = resolved[hops[i]];
      if (r) { r.unreliable = true; }
      delete hopPositions[hops[i]];
    }
  }

  res.json({ resolved, region: packetIata || null });
});

// channelHashNames removed — we only use decoded channel names now

app.get('/api/channels', (req, res) => {
  const { region } = req.query;
  const regionObsIds = getObserverIdsForRegions(region);
  const _ck = 'channels' + (region ? ':' + region : '');
  const _c = cache.get(_ck); if (_c) return res.json(_c);
  // Single pass: only scan type-5 packets via filter (already in memory)
  const channelMap = {};

  for (const pkt of pktStore.all()) {
    if (pkt.payload_type !== 5) continue;
    if (regionObsIds && !regionObsIds.has(pkt.observer_id)) continue;
    let decoded;
    try { decoded = JSON.parse(pkt.decoded_json); } catch { continue; }

    // Only show decrypted messages — skip encrypted garbage
    if (decoded.type !== 'CHAN') continue;

    const channelName = decoded.channel || 'unknown';
    const key = channelName;
    
    if (!channelMap[key]) {
      channelMap[key] = {
        hash: key,
        name: channelName,
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
  cache.set(_ck, _chResult, TTL.channels);
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
    // Only decrypted messages
    if (decoded.type !== 'CHAN') continue;
    const ch = decoded.channel || 'unknown';
    if (ch !== channelHash) continue;

    const sender = decoded.sender || (decoded.text ? decoded.text.split(': ')[0] : null) || pkt.observer_name || pkt.observer_id || 'Unknown';
    const text = decoded.text || decoded.encryptedData || '';
    // Use server observation timestamp for dedup — sender_timestamp is unreliable (device clocks are wildly inaccurate)
    const ts = pkt.timestamp;
    const dedupeKey = `${sender}:${pkt.hash}`;

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
        timestamp: pkt.timestamp,
        sender_timestamp: decoded.sender_timestamp || null,
        packetId: pkt.id,
        packetHash: pkt.hash,
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
  // Batch-fetch all node locations in one query
  const allNodes = db.db.prepare("SELECT public_key, lat, lon, role FROM nodes").all();
  const nodeMap = new Map();
  for (const n of allNodes) nodeMap.set(n.public_key?.toLowerCase(), n);
  const result = observers.map(o => {
    const obsPackets = pktStore.byObserver.get(o.id) || [];
    // byObserver is sorted newest-first, so count from front until we pass the cutoff
    let count = 0;
    for (let i = 0; i < obsPackets.length; i++) {
      if (obsPackets[i].timestamp > oneHourAgo) count++;
      else break;
    }
    const node = nodeMap.get(o.id?.toLowerCase());
    return { ...o, packetsLastHour: count, lat: node?.lat || null, lon: node?.lon || null, nodeRole: node?.role || null };
  });
  const _oResult = { observers: result, server_time: new Date().toISOString() };
  cache.set('observers', _oResult, TTL.observers);
  res.json(_oResult);
});

// Observer detail
app.get('/api/observers/:id', (req, res) => {
  const id = req.params.id;
  const obs = db.db.prepare('SELECT * FROM observers WHERE id = ?').get(id);
  if (!obs) return res.status(404).json({ error: 'Observer not found' });
  const oneHourAgo = new Date(Date.now() - 3600000).toISOString();
  const obsPackets = pktStore.byObserver.get(id) || [];
  const packetsLastHour = obsPackets.filter(p => p.timestamp > oneHourAgo).length;
  res.json({ ...obs, packetsLastHour });
});

// Observer analytics
app.get('/api/observers/:id/analytics', (req, res) => {
  const id = req.params.id;
  const days = parseInt(req.query.days) || 7;
  const since = new Date(Date.now() - days * 86400000).toISOString();
  const obsPackets = (pktStore.byObserver.get(id) || []).filter(p => p.timestamp >= since).sort((a, b) => b.timestamp.localeCompare(a.timestamp));

  // Timeline: packets per hour (last N days, bucketed)
  const bucketMs = days <= 1 ? 3600000 : days <= 7 ? 3600000 * 4 : 86400000;
  const buckets = {};
  for (const p of obsPackets) {
    const t = Math.floor(new Date(p.timestamp).getTime() / bucketMs) * bucketMs;
    buckets[t] = (buckets[t] || 0) + 1;
  }
  const timeline = Object.entries(buckets)
    .sort((a, b) => a[0] - b[0])
    .map(([t, count]) => {
      const d = new Date(parseInt(t));
      const label = days <= 1
        ? d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })
        : days <= 7
        ? d.toLocaleDateString('en-US', { weekday: 'short', hour: '2-digit' })
        : d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
      return { label, count };
    });

  // Packet type breakdown
  const packetTypes = {};
  for (const p of obsPackets) {
    packetTypes[p.payload_type] = (packetTypes[p.payload_type] || 0) + 1;
  }

  // Unique nodes per time bucket
  const nodeBuckets = {};
  for (const p of obsPackets) {
    const t = Math.floor(new Date(p.timestamp).getTime() / bucketMs) * bucketMs;
    if (!nodeBuckets[t]) nodeBuckets[t] = new Set();
    try {
      const decoded = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json) : p.decoded_json;
      if (decoded && decoded.pubKey) nodeBuckets[t].add(decoded.pubKey);
      if (decoded && decoded.srcHash) nodeBuckets[t].add(decoded.srcHash);
      if (decoded && decoded.destHash) nodeBuckets[t].add(decoded.destHash);
    } catch {}
    const hops = typeof p.path_json === 'string' ? JSON.parse(p.path_json) : (p.path_json || []);
    for (const h of hops) nodeBuckets[t].add(h);
  }
  const nodesTimeline = Object.entries(nodeBuckets)
    .sort((a, b) => a[0] - b[0])
    .map(([t, nodes]) => {
      const d = new Date(parseInt(t));
      const label = days <= 1
        ? d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })
        : days <= 7
        ? d.toLocaleDateString('en-US', { weekday: 'short', hour: '2-digit' })
        : d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
      return { label, count: nodes.size };
    });

  // SNR distribution
  const snrBuckets = {};
  for (const p of obsPackets) {
    if (p.snr == null) continue;
    const bucket = Math.floor(p.snr / 2) * 2; // 2dB buckets
    const range = bucket + ' to ' + (bucket + 2);
    snrBuckets[bucket] = snrBuckets[bucket] || { range, count: 0 };
    snrBuckets[bucket].count++;
  }
  const snrDistribution = Object.values(snrBuckets).sort((a, b) => parseFloat(a.range) - parseFloat(b.range));

  // Recent packets (last 20) — obsPackets filtered from pktStore, newest-first
  const recentPackets = obsPackets.slice(0, 20);

  res.json({ timeline, packetTypes, nodesTimeline, snrDistribution, recentPackets });
});

app.get('/api/traces/:hash', (req, res) => {
  const packets = (pktStore.getSiblings(req.params.hash) || []).sort((a,b) => a.timestamp > b.timestamp ? 1 : -1);
  const traces = packets.map(p => ({
    observer: p.observer_id,
    observer_name: p.observer_name || null,
    time: p.timestamp,
    snr: p.snr,
    rssi: p.rssi,
    path_json: p.path_json || null
  }));
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
  
  // Single reusable lookup for all packets referencing this node
  const packets = pktStore.findPacketsForNode(pubkey).packets;

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

  // Build observer iata lookup
  const allObservers = db.getObservers();
  const obsIataMap = {};
  for (const obs of allObservers) { if (obs.iata) obsIataMap[obs.id] = obs.iata; }

  const observers = Object.entries(obsMap).map(([observer_id, o]) => ({
    observer_id, observer_name: o.observer_name, packetCount: o.packetCount,
    avgSnr: o.snrN ? o.snrSum / o.snrN : null, avgRssi: o.rssiN ? o.rssiSum / o.rssiN : null,
    iata: obsIataMap[observer_id] || null
  })).sort((a, b) => b.packetCount - a.packetCount);

  const recentPackets = packets.slice(0, 20);

  // Count transmissions vs observations
  const counts = pktStore.countForNode(pubkey);
  const recentWithoutObs = recentPackets.map(p => {
    const { observations, ...rest } = p;
    return { ...rest, observation_count: p.observation_count || 1 };
  });

  const result = {
    node: node.node || node, observers,
    stats: {
      totalTransmissions: counts.transmissions,
      totalObservations: counts.observations,
      totalPackets: counts.transmissions, // backward compat
      packetsToday, avgSnr: snrN ? snrSum / snrN : null, avgHops: hopCount > 0 ? Math.round(totalHops / hopCount) : 0, lastHeard
    },
    recentPackets: recentWithoutObs
  };
  cache.set(_ck, result, TTL.nodeHealth);
  res.json(result);
});

app.get('/api/nodes/:pubkey/paths', (req, res) => {
  const pubkey = req.params.pubkey;
  const _ck = 'nodePaths:' + pubkey;
  const _c = cache.get(_ck); if (_c) return res.json(_c);

  const node = db.db.prepare('SELECT public_key, name, lat, lon FROM nodes WHERE public_key = ?').get(pubkey);
  if (!node) return res.status(404).json({ error: 'Not found' });

  const prefix1 = pubkey.slice(0, 2).toLowerCase();
  const prefix2 = pubkey.slice(0, 4).toLowerCase();

  const allNodes = getCachedNodes(false);

  // Scan all transmissions for paths containing this node's prefix
  const matchingTx = [];
  for (const [, tx] of pktStore.byHash) {
    if (!tx.path_json) continue;
    const hops = tx._parsedPath || (tx.path_json ? (() => { try { return tx._parsedPath = JSON.parse(tx.path_json); } catch { return null; } })() : null);
    if (!Array.isArray(hops) || !hops.length) continue;
    const found = hops.some(h => {
      const hl = (typeof h === 'string' ? h : '').toLowerCase();
      return hl === prefix1 || hl === prefix2 || hl.startsWith(prefix2);
    });
    if (found) matchingTx.push({ tx, hops });
  }

  // Resolve and group paths using shared disambiguateHops (prefix-indexed)

  // Group by resolved path signature
  const pathGroups = {};
  let totalTransmissions = 0;
  const _pathsDisambigCache = {};
  for (const { tx, hops } of matchingTx) {
    totalTransmissions++;
    // Use disambiguateHops (has prefix index) instead of inline allNodes.filter per hop
    const cacheKey = hops.join(',');
    const resolved = _pathsDisambigCache[cacheKey] || (_pathsDisambigCache[cacheKey] = disambiguateHops(hops, allNodes));
    const resolvedHops = resolved.map(r => ({ prefix: r.hop, name: r.name, pubkey: r.pubkey || null, lat: r.lat || null, lon: r.lon || null }));
    const key = resolvedHops.map(h => h.pubkey || h.prefix).join('→');
    if (!pathGroups[key]) pathGroups[key] = { hops: resolvedHops, count: 0, lastSeen: null, sampleHash: tx.hash };
    pathGroups[key].count++;
    const ts = tx.timestamp;
    if (!pathGroups[key].lastSeen || ts > pathGroups[key].lastSeen) {
      pathGroups[key].lastSeen = ts;
      pathGroups[key].sampleHash = tx.hash;
    }
  }

  const paths = Object.values(pathGroups)
    .sort((a, b) => b.count - a.count)
    .slice(0, 50);

  const result = {
    node: { public_key: node.public_key, name: node.name, lat: node.lat, lon: node.lon },
    paths,
    totalPaths: Object.keys(pathGroups).length,
    totalTransmissions
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

  // Read from in-memory index + name search, filter by time range
  const allPkts = pktStore.findPacketsForNode(pubkey).packets;
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
  const allNodes = getCachedNodes(false);

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
    const hops = pkt._parsedPath || (pkt.path_json ? (() => { try { return pkt._parsedPath = JSON.parse(pkt.path_json); } catch { return null; } })() : null);
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
  const regionKey = req.query.region || '';
  const _ck = 'analytics:subpaths:' + (req.query.minLen||2) + ':' + (req.query.maxLen||8) + ':' + (req.query.limit||100) + ':r=' + regionKey;
  const _c = cache.get(_ck); if (_c) return res.json(_c);

  const minLen = Math.max(2, Number(req.query.minLen) || 2);
  const maxLen = Number(req.query.maxLen) || 8;
  const limit = Number(req.query.limit) || 100;

  const regionObsIds = getObserverIdsForRegions(req.query.region);
  if (regionObsIds) {
    // Region-filtered subpath computation
    const regionalHashes = new Set();
    for (const obsId of regionObsIds) {
      const obs = pktStore.byObserver.get(obsId);
      if (obs) for (const o of obs) regionalHashes.add(o.hash);
    }
    const packets = pktStore.filter(p => p.path_json && p.path_json !== '[]' && regionalHashes.has(p.hash));
    const allNodes = getCachedNodes(false);
    const subpathsByLen = {};
    let totalPaths = 0;
    for (const pkt of packets) {
      const hops = pkt._parsedPath || (pkt.path_json ? (() => { try { return pkt._parsedPath = JSON.parse(pkt.path_json); } catch { return null; } })() : null);
      if (!Array.isArray(hops) || hops.length < 2) continue;
      totalPaths++;
      const resolved = disambiguateHops(hops, allNodes);
      const named = resolved.map(r => r.name);
      for (let len = minLen; len <= Math.min(maxLen, named.length); len++) {
        if (!subpathsByLen[len]) subpathsByLen[len] = {};
        for (let start = 0; start <= named.length - len; start++) {
          const sub = named.slice(start, start + len).join(' \u2192 ');
          const raw = hops.slice(start, start + len).join(',');
          if (!subpathsByLen[len][sub]) subpathsByLen[len][sub] = { count: 0, raw };
          subpathsByLen[len][sub].count++;
        }
      }
    }
    const merged = {};
    for (let len = minLen; len <= maxLen; len++) {
      const bucket = subpathsByLen[len] || {};
      for (const [path, data] of Object.entries(bucket)) {
        if (!merged[path]) merged[path] = { count: 0, raw: data.raw };
        merged[path].count += data.count;
      }
    }
    const ranked = Object.entries(merged)
      .map(([path, data]) => ({ path, rawHops: data.raw.split(','), count: data.count, hops: path.split(' \u2192 ').length, pct: totalPaths > 0 ? Math.round(data.count / totalPaths * 1000) / 10 : 0 }))
      .sort((a, b) => b.count - a.count)
      .slice(0, limit);
    const result = { subpaths: ranked, totalPaths };
    cache.set(_ck, result, TTL.analyticsSubpaths);
    return res.json(result);
  }

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
  const allNodes = getCachedNodes(false);

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

// IATA coordinates for client-side regional filtering
app.get('/api/iata-coords', (req, res) => {
  res.json({ coords: IATA_COORDS });
});

// Audio Lab: representative packets bucketed by type
app.get('/api/audio-lab/buckets', (req, res) => {
  const buckets = {};
  const byType = {};
  for (const tx of pktStore.packets) {
    if (!tx.raw_hex) continue;
    let typeName = 'UNKNOWN';
    try { const d = JSON.parse(tx.decoded_json || '{}'); typeName = d.type || (PAYLOAD_TYPES[tx.payload_type] || 'UNKNOWN'); } catch {}
    if (!byType[typeName]) byType[typeName] = [];
    byType[typeName].push(tx);
  }
  for (const [type, pkts] of Object.entries(byType)) {
    const sorted = pkts.sort((a, b) => (a.raw_hex || '').length - (b.raw_hex || '').length);
    const count = Math.min(8, sorted.length);
    const picked = [];
    for (let i = 0; i < count; i++) {
      const idx = Math.floor((i / count) * sorted.length);
      const tx = sorted[idx];
      picked.push({
        hash: tx.hash, raw_hex: tx.raw_hex, decoded_json: tx.decoded_json,
        observation_count: tx.observation_count || 1, payload_type: tx.payload_type,
        path_json: tx.path_json, observer_id: tx.observer_id, timestamp: tx.timestamp,
      });
    }
    buckets[type] = picked;
  }
  res.json({ buckets });
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
const listenPort = process.env.PORT || config.port;
server.listen(listenPort, () => {
  const protocol = isHttps ? 'https' : 'http';
  console.log(`MeshCore Analyzer running on ${protocol}://localhost:${listenPort}`);
  // Log theme file location
  let themeFound = false;
  for (const p of THEME_PATHS) {
    try { fs.accessSync(p); console.log(`[theme] Loaded from ${p}`); themeFound = true; break; } catch {}
  }
  if (!themeFound) console.log(`[theme] No theme.json found. Place it next to config.json or in data/ to customize.`);
  // Pre-warm expensive caches via self-requests (yields event loop between each)
  setTimeout(() => {
    const port = listenPort;
    const warmClient = isHttps ? https : http;
    const warmEndpoints = [
      // Subpaths (heaviest — must go first so cache is ready)
      '/api/analytics/subpaths?minLen=2&maxLen=2&limit=50',
      '/api/analytics/subpaths?minLen=3&maxLen=3&limit=30',
      '/api/analytics/subpaths?minLen=4&maxLen=4&limit=20',
      '/api/analytics/subpaths?minLen=5&maxLen=8&limit=15',
      // Other analytics
      '/api/observers',
      '/api/nodes?limit=10000&lastHeard=259200',
      '/api/analytics/rf',
      '/api/analytics/topology',
      '/api/analytics/channels',
      '/api/analytics/hash-sizes',
      '/api/analytics/distance',
      '/api/nodes/bulk-health?limit=50',
    ];
    let warmed = 0;
    const tw = Date.now();
    const warmNext = () => {
      if (warmed >= warmEndpoints.length) {
        console.log(`[pre-warm] ${warmEndpoints.length} endpoints in ${Date.now() - tw}ms`);
        return;
      }
      const ep = warmEndpoints[warmed++];
      const requestOptions = { hostname: '127.0.0.1', port, path: ep };
      if (isHttps) requestOptions.rejectUnauthorized = false;
      warmClient.get(requestOptions, (res) => {
        res.resume();
        res.on('end', () => setImmediate(warmNext));
      }).on('error', () => setImmediate(warmNext));
    };
    warmNext();
  }, 5000); // 5s delay — let initial client page load complete first
});

// --- Graceful Shutdown ---
let _shuttingDown = false;
function shutdown(signal) {
  if (_shuttingDown) return;
  _shuttingDown = true;
  console.log(`\n[shutdown] received ${signal}, closing gracefully…`);

  // Terminate WebSocket clients first — open WS connections would prevent
  // server.close() from ever completing its callback otherwise.
  if (wss) {
    for (const client of wss.clients) {
      try { client.terminate(); } catch {}
    }
    wss.close();
    console.log('[shutdown] WebSocket server closed');
  }

  // Force-drain all keep-alive HTTP connections so server.close() fires promptly.
  // closeAllConnections() is available since Node 18.2 (we're on Node 22).
  server.closeAllConnections();
  server.close(() => console.log('[shutdown] HTTP server closed'));

  // Checkpoint WAL and close SQLite synchronously — performed unconditionally,
  // not gated on server.close(), so the DB is always cleanly flushed.
  try {
    db.db.pragma('wal_checkpoint(TRUNCATE)');
    db.db.close();
    console.log('[shutdown] database closed');
  } catch (e) {
    console.error('[shutdown] database close error:', e.message);
  }

  process.exit(0);
}

process.on('SIGTERM', () => shutdown('SIGTERM'));
process.on('SIGINT',  () => shutdown('SIGINT'));

module.exports = { app, server, wss };
