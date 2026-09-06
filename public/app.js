/* === CoreScope — app.js === */
'use strict';

// --- Route/Payload name maps ---
const ROUTE_TYPES = { 0: 'TRANSPORT_FLOOD', 1: 'FLOOD', 2: 'DIRECT', 3: 'TRANSPORT_DIRECT' };
const PAYLOAD_TYPES = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 6: 'Group Data', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 10: 'Multipart', 11: 'Control', 15: 'Raw Custom' };
const PAYLOAD_COLORS = { 0: 'req', 1: 'response', 2: 'txt-msg', 3: 'ack', 4: 'advert', 5: 'grp-txt', 6: 'grp-data', 7: 'anon-req', 8: 'path', 9: 'trace', 10: 'multipart', 11: 'control', 15: 'raw-custom' };

function routeTypeName(n) { return ROUTE_TYPES[n] || 'UNKNOWN'; }
function payloadTypeName(n) { return PAYLOAD_TYPES[n] || 'UNKNOWN'; }
function payloadTypeColor(n) { return PAYLOAD_COLORS[n] || 'unknown'; }
function isTransportRoute(rt) { return rt === 0 || rt === 3; }
/** Byte offset of path_len in raw_hex: 5 for transport routes (4 bytes of next/last hop codes precede it), 1 otherwise. */
function getPathLenOffset(routeType) { return isTransportRoute(routeType) ? 5 : 1; }
function transportBadge(rt) { return isTransportRoute(rt) ? ' <span class="badge badge-transport" title="' + routeTypeName(rt) + '">T</span>' : ''; }

/**
 * Render a packet's transport region scope (transmissions.scope_name) for the
 * Scope column and the detail pane. Three states, matching what the DB stores:
 *   null/undefined — not transport-scoped; FLOOD and DIRECT carry no
 *                    transport_code_1 at all, so there is nothing to show.
 *   ""             — transport-scoped, but the code matched no configured
 *                    hashRegions entry.
 *   "#be"          — the matched region name.
 */
function scopeCellHtml(scopeName) {
  if (scopeName == null) return '—';
  if (scopeName === '') return '<span style="color:var(--text-muted)">unknown</span>';
  return escapeHtml(scopeName);
}

/**
 * Compute breakdown byte ranges from raw_hex on the client.
 * Mirrors cmd/server/decoder.go BuildBreakdown(). Used so per-observation raw_hex
 * (which can differ in path length from the top-level packet) gets accurate
 * highlighted byte ranges, instead of using the server-supplied breakdown
 * computed once from the top-level raw_hex.
 */
function computeBreakdownRanges(hexString, routeType, payloadType) {
  if (!hexString) return [];
  const clean = hexString.replace(/\s+/g, '');
  const bytes = clean.length / 2;
  if (bytes < 2) return [];
  const ranges = [];
  // Header
  ranges.push({ start: 0, end: 0, label: 'Header' });
  let offset = 1;
  if (isTransportRoute(routeType)) {
    if (bytes < offset + 4) return ranges;
    ranges.push({ start: offset, end: offset + 3, label: 'Transport Codes' });
    offset += 4;
  }
  if (offset >= bytes) return ranges;
  // Path Length byte
  ranges.push({ start: offset, end: offset, label: 'Path Length' });
  const pathByte = parseInt(clean.slice(offset * 2, offset * 2 + 2), 16);
  offset += 1;
  if (isNaN(pathByte)) return ranges;
  const hashSize = (pathByte >> 6) + 1;
  const hashCount = pathByte & 0x3F;
  const pathBytes = hashSize * hashCount;
  if (hashCount > 0 && offset + pathBytes <= bytes) {
    ranges.push({ start: offset, end: offset + pathBytes - 1, label: 'Path' });
  }
  offset += pathBytes;
  if (offset >= bytes) return ranges;
  const payloadStart = offset;
  // ADVERT (payload_type 4) gets sub-fields when full record present
  if (payloadType === 4 && bytes - payloadStart >= 100) {
    ranges.push({ start: payloadStart,      end: payloadStart + 31, label: 'PubKey' });
    ranges.push({ start: payloadStart + 32, end: payloadStart + 35, label: 'Timestamp' });
    ranges.push({ start: payloadStart + 36, end: payloadStart + 99, label: 'Signature' });
    const appStart = payloadStart + 100;
    if (appStart < bytes) {
      ranges.push({ start: appStart, end: appStart, label: 'Flags' });
      const appFlags = parseInt(clean.slice(appStart * 2, appStart * 2 + 2), 16);
      let fOff = appStart + 1;
      if (!isNaN(appFlags)) {
        if ((appFlags & 0x10) && fOff + 8 <= bytes) {
          ranges.push({ start: fOff,     end: fOff + 3, label: 'Latitude' });
          ranges.push({ start: fOff + 4, end: fOff + 7, label: 'Longitude' });
          fOff += 8;
        }
        if ((appFlags & 0x20) && fOff + 2 <= bytes) fOff += 2;
        if ((appFlags & 0x40) && fOff + 2 <= bytes) fOff += 2;
        if ((appFlags & 0x80) && fOff < bytes) {
          ranges.push({ start: fOff, end: bytes - 1, label: 'Name' });
        }
      }
    }
  } else {
    ranges.push({ start: payloadStart, end: bytes - 1, label: 'Payload' });
  }
  return ranges;
}

// --- Utilities ---
const _apiPerf = { calls: 0, totalMs: 0, log: [], cacheHits: 0 };
const _apiCache = new Map();
const _inflight = new Map();
// Client-side TTLs (ms) — loaded from server config, with defaults
const CLIENT_TTL = {
  stats: 10000, nodeDetail: 240000, nodeHealth: 240000, nodeList: 90000,
  bulkHealth: 300000, networkStatus: 300000, observers: 120000,
  channels: 15000, channelMessages: 10000, analyticsRF: 300000,
  analyticsTopology: 300000, analyticsChannels: 300000, analyticsHashSizes: 300000,
  analyticsSubpaths: 300000, analyticsSubpathDetail: 300000,
  nodeAnalytics: 60000, nodeSearch: 10000
};
// Fetch server cache config and use as client TTLs (server values are in seconds)
fetch('/api/config/cache').then(r => r.json()).then(cfg => {
  for (const [k, v] of Object.entries(cfg)) {
    if (k in CLIENT_TTL && typeof v === 'number') CLIENT_TTL[k] = v * 1000;
  }
}).catch(() => {});
async function api(path, { ttl = 0, bust = false } = {}) {
  const t0 = performance.now();
  if (!bust && ttl > 0) {
    const cached = _apiCache.get(path);
    if (cached && Date.now() < cached.expires) {
      _apiPerf.calls++;
      _apiPerf.cacheHits++;
      _apiPerf.log.push({ path, ms: 0, time: Date.now(), cached: true });
      if (_apiPerf.log.length > 200) _apiPerf.log.shift();
      return cached.data;
    }
  }
  // Deduplicate in-flight requests
  if (_inflight.has(path)) return _inflight.get(path);
  const promise = (async () => {
    // Issue #1659: 503 with Retry-After indicates server-side warm-up
    // (analytics recomputer first-pass, index build, etc.). Retry with
    // exponential backoff capped at 30s, up to 6 attempts (~63s total),
    // so the analytics cards never display the stale post-restart slice.
    //
    // PR #1688 r1 (adv #1 + munger #4): use a single `notified` flag
    // per outer call + `try / finally` so the in-flight banner counter
    // is decremented exactly once regardless of retry count or
    // exception path (success, retry-exhausted throw, network throw).
    // Previously, multi-attempt retries leaked the counter (incremented
    // per attempt, decremented at most once) and exhausted-retries
    // threw without decrementing at all — banner stuck across three
    // analytics endpoints, multiplied.
    let attempt = 0;
    let delay = 1000;
    const maxAttempts = 6;
    let notified = false;
    try {
      while (true) {
        const res = await fetch('/api' + path);
        if (res.status === 503 && attempt < maxAttempts) {
          const ra = parseInt(res.headers.get('Retry-After'), 10);
          const wait = isFinite(ra) && ra > 0 ? ra * 1000 : delay;
          if (!notified) { _warmupNotify_1659(true); notified = true; }
          await new Promise(r => setTimeout(r, Math.min(wait, 30000)));
          delay = Math.min(delay * 2, 30000);
          attempt++;
          continue;
        }
        if (!res.ok) throw new Error(`API ${res.status}: ${path}`);
        const data = await res.json();
        const ms = performance.now() - t0;
        _apiPerf.calls++;
        _apiPerf.totalMs += ms;
        _apiPerf.log.push({ path, ms: Math.round(ms), time: Date.now() });
        if (_apiPerf.log.length > 200) _apiPerf.log.shift();
        if (ms > 500) console.warn(`[SLOW API] ${path} took ${Math.round(ms)}ms`);
        if (ttl > 0) _apiCache.set(path, { data, expires: Date.now() + ttl });
        return data;
      }
    } finally {
      // Decrement exactly once iff we incremented. Runs on return,
      // throw, or retry-exhausted throw — counter is balanced.
      if (notified) _warmupNotify_1659(false);
    }
  })();
  _inflight.set(path, promise);
  promise.finally(() => _inflight.delete(path));
  return promise;
}

// Issue #1659: minimal "Computing…" indicator while an analytics
// endpoint is serving 503-warmup. We expose a small fixed banner; if a
// page later wants to wire its own indicator it can override
// window.onWarmup_1659 to receive the boolean state.
let _warmupBannerEl_1659 = null;
let _warmupInflight_1659 = 0;
function _warmupNotify_1659(active) {
  if (active) _warmupInflight_1659++;
  else _warmupInflight_1659 = Math.max(0, _warmupInflight_1659 - 1);
  const visible = _warmupInflight_1659 > 0;
  if (typeof window !== 'undefined' && typeof window.onWarmup_1659 === 'function') {
    try { window.onWarmup_1659(visible); } catch (_) { /* ignore */ }
  }
  if (typeof document === 'undefined') return;
  if (!_warmupBannerEl_1659) {
    const el = document.createElement('div');
    el.id = 'cs-warmup-banner-1659';
    el.style.cssText = 'position:fixed;top:8px;right:8px;z-index:9999;padding:6px 10px;background:var(--bg-elev,#222);color:var(--fg,#eee);border:1px solid var(--border,#444);border-radius:4px;font-size:12px;font-family:sans-serif;display:none;';
    el.textContent = 'Computing analytics…';
    if (document.body) document.body.appendChild(el);
    _warmupBannerEl_1659 = el;
  }
  _warmupBannerEl_1659.style.display = visible ? 'block' : 'none';
}

// Fetch the COMPLETE /api/nodes set, transparently paging around the server's
// per-request row cap. /api/nodes clamps ?limit to `listLimits.nodesMax`
// (default 2000, operator-configurable; originally a hard 500 in PR #1540,
// raised/made configurable in PR #1589). A single ?limit=N fetch therefore
// silently truncates to the top nodesMax rows by last_seen DESC, so on a mesh
// with more nodes than that cap every node-list consumer (map, live,
// analytics, packets, area-map) loses the older-advert tail — a node that
// relays constantly but last self-advertised hours ago drops off the map even
// though it is plainly alive. #1606 fixed this for the Nodes page; this helper
// generalizes the same loop for all callers, using a fixed client page size
// well under the server cap.
//
// extraQuery: query fragment appended after the paged limit/offset, each piece
//   already '&'-prefixed exactly as callers build it today
//   (e.g. '&lastHeard=30d&area=x', '&sortBy=lastSeen&region=y'); pass '' for none.
// safetyCap: hard ceiling on BOTH pages fetched and nodes returned — the result
//   is sliced to it (callers like live.js pass their render ceiling here).
// Returns { nodes, counts, total }: counts is from the first page; total is the
//   real deduped/capped node count (NOT the server's per-query `total`).
async function fetchAllNodes(extraQuery = '', { ttl = 0, pageSize = 500, safetyCap = 10000 } = {}) {
  const accumulated = [];
  let counts = {};
  for (let offset = 0; offset < safetyCap; offset += pageSize) {
    const data = await api(`/nodes?limit=${pageSize}&offset=${offset}${extraQuery}`, { ttl });
    const page = data && Array.isArray(data.nodes) ? data.nodes
      : (Array.isArray(data) ? data : []);
    accumulated.push.apply(accumulated, page);
    if (offset === 0) counts = (data && data.counts) || {};
    // Canonical stop: a short page is the end. The server's `total` is a real
    // COUNT(*) for the query, but the handler overwrites it with the filtered
    // length under area/geo/blacklist filtering — so we never loop on it, nor
    // surface it; a short page is the reliable end-of-data signal. See #1606.
    if (page.length < pageSize) break;
  }
  // Dedup by public_key: the sort window (last_seen DESC by default) can shift
  // under concurrent ingest, repeating a row across a page boundary. Rows
  // missing a public_key get a unique synthetic key so they are NOT collapsed.
  const seen = new Map();
  for (let i = 0; i < accumulated.length; i++) {
    const n = accumulated[i];
    seen.set((n && n.public_key) || ('__nokey' + i), n);
  }
  // Enforce safetyCap as a real node-count ceiling (the page loop only bounds
  // it to the next pageSize multiple), so e.g. live.js's LIVE_MAP_MAX_NODES is
  // honored exactly rather than overshooting by up to pageSize-1.
  const nodes = Array.from(seen.values()).slice(0, safetyCap);
  return { nodes, counts, total: nodes.length };
}

function invalidateApiCache(prefix) {
  for (const key of _apiCache.keys()) {
    if (key.startsWith(prefix || '')) _apiCache.delete(key);
  }
}
// Expose for console debugging: apiPerf()
window.apiPerf = function() {
  const byPath = {};
  _apiPerf.log.forEach(e => {
    if (!byPath[e.path]) byPath[e.path] = { count: 0, totalMs: 0, maxMs: 0 };
    byPath[e.path].count++;
    byPath[e.path].totalMs += e.ms;
    if (e.ms > byPath[e.path].maxMs) byPath[e.path].maxMs = e.ms;
  });
  const rows = Object.entries(byPath).map(([p, s]) => ({
    path: p, count: s.count, avgMs: Math.round(s.totalMs / s.count), maxMs: s.maxMs,
    totalMs: Math.round(s.totalMs)
  })).sort((a, b) => b.totalMs - a.totalMs);
  console.table(rows);
  const hitRate = _apiPerf.calls ? Math.round(_apiPerf.cacheHits / _apiPerf.calls * 100) : 0;
  const misses = _apiPerf.calls - _apiPerf.cacheHits;
  console.log(`Cache: ${_apiPerf.cacheHits} hits / ${misses} misses (${hitRate}% hit rate)`);
  return { calls: _apiPerf.calls, avgMs: Math.round(_apiPerf.totalMs / (misses || 1)), cacheHits: _apiPerf.cacheHits, cacheMisses: misses, cacheHitRate: hitRate, endpoints: rows };
};

function timeAgo(iso) {
  if (!iso) return '—';
  const ms = new Date(iso).getTime();
  if (!isFinite(ms)) return '—';
  const s = Math.floor((Date.now() - ms) / 1000);
  const abs = Math.abs(s);
  let value;
  let suffix;
  if (abs < 60) { value = abs; suffix = 's'; }
  else if (abs < 3600) { value = Math.floor(abs / 60); suffix = 'm'; }
  else if (abs < 86400) { value = Math.floor(abs / 3600); suffix = 'h'; }
  else { value = Math.floor(abs / 86400); suffix = 'd'; }
  if (s < 0) return 'in ' + value + suffix;
  return value + suffix + ' ago';
}

function getHashParams() {
  return new URLSearchParams(location.hash.split('?')[1] || '');
}

// parseViewportHash — issue #1709. Parses lat/lon/zoom viewport params from a
// hash query string and returns {lat, lon, zoom} if BOTH lat and lon are valid
// (and zoom, if present, is numeric), otherwise null. Partial lat-only or
// lon-only inputs are intentionally rejected (the issue explicitly forbids
// partial application of a center). When zoom is missing, defaults to 12. When
// zoom is out of the [minZoom, maxZoom] range it is clamped to that range.
//
// `hashOrSearch` may be either a full `location.hash` (e.g. `#/live?lat=...`)
// or a bare query string (e.g. `lat=...&lon=...`). Either is accepted.
//
// Bounds: lat ∈ [-90, 90], lon ∈ [-180, 180]; zoom defaults clamp to [1, 20]
// when bounds not supplied (sensible Leaflet fallback when tile-provider
// minZoom/maxZoom is unknown).
function parseViewportHash(hashOrSearch, opts) {
  if (hashOrSearch == null) return null;
  var s = String(hashOrSearch);
  if (s === '') return null;
  // Strip leading '#...?' if present so callers can pass raw location.hash.
  var qIdx = s.indexOf('?');
  if (qIdx >= 0) s = s.slice(qIdx + 1);
  // Also tolerate a leading '?' on a bare search string.
  if (s.charAt(0) === '?') s = s.slice(1);
  var params;
  try { params = new URLSearchParams(s); } catch (_) { return null; }
  var latStr = params.get('lat');
  var lonStr = params.get('lon');
  if (latStr == null || lonStr == null || latStr === '' || lonStr === '') return null;
  var lat = parseFloat(latStr);
  var lon = parseFloat(lonStr);
  if (!isFinite(lat) || !isFinite(lon)) return null;
  if (lat < -90 || lat > 90) return null;
  if (lon < -180 || lon > 180) return null;
  var minZ = (opts && typeof opts.minZoom === 'number') ? opts.minZoom : 1;
  var maxZ = (opts && typeof opts.maxZoom === 'number') ? opts.maxZoom : 20;
  var zoomStr = params.get('zoom');
  var zoom;
  if (zoomStr == null || zoomStr === '') {
    zoom = (opts && typeof opts.defaultZoom === 'number') ? opts.defaultZoom : 12;
  } else {
    zoom = parseFloat(zoomStr);
    if (!isFinite(zoom)) return null;
  }
  if (zoom < minZ) zoom = minZ;
  if (zoom > maxZ) zoom = maxZ;
  return { lat: lat, lon: lon, zoom: zoom };
}
if (typeof window !== 'undefined') { window.parseViewportHash = parseViewportHash; }

// shouldEmbedRoute — issue #1369. Returns true when the SPA should render in
// "embed" mode (chrome suppressed: no top-nav, no bottom-nav, no side drawer,
// content full-bleed). Triggered by ?embed=1 in the hash query string.
//
// Allowlisted to /#/map and /#/channels — the two surfaces operators asked
// for in the cross-domain embed scenario. Other pages have chrome assumptions
// we are not committing to in embed mode (Tufte: ship narrow, expand later
// only when there is a real ask).
function shouldEmbedRoute(basePage, hashSearch) {
  if (basePage !== 'map' && basePage !== 'channels') return false;
  if (!hashSearch) return false;
  var params = new URLSearchParams(hashSearch);
  return params.get('embed') === '1';
}

function getDistanceUnit() {
  var stored = localStorage.getItem('meshcore-distance-unit');
  if (stored === 'km') return 'km';
  if (stored === 'mi') return 'mi';
  // 'auto' or no value — locale detection
  var milesLocales = ['en-us', 'en-gb'];
  var lang = (typeof navigator !== 'undefined' && navigator.language || '').toLowerCase();
  for (var i = 0; i < milesLocales.length; i++) {
    if (lang === milesLocales[i] || lang.startsWith(milesLocales[i] + '-')) return 'mi';
  }
  return 'km';
}
window.getDistanceUnit = getDistanceUnit;

function formatDistance(km) {
  if (km == null || isNaN(+km)) return '—';
  var d = +km;
  var unit = getDistanceUnit();
  if (unit === 'mi') {
    var mi = d / 1.60934;
    if (mi < 0.1) return Math.round(mi * 5280) + ' ft';
    return mi.toFixed(1) + ' mi';
  }
  if (d < 1) return Math.round(d * 1000) + ' m';
  return d.toFixed(1) + ' km';
}
window.formatDistance = formatDistance;

function formatDistanceRound(km) {
  if (km == null || isNaN(+km)) return '—';
  var unit = getDistanceUnit();
  if (unit === 'mi') return Math.round(+km / 1.60934) + ' mi';
  return Math.round(+km) + ' km';
}
window.formatDistanceRound = formatDistanceRound;

function getTimestampMode() {
  const saved = localStorage.getItem('meshcore-timestamp-mode');
  if (saved === 'ago' || saved === 'absolute') return saved;
  const serverDefault = window.SITE_CONFIG?.timestamps?.defaultMode;
  return serverDefault === 'absolute' ? 'absolute' : 'ago';
}

function getTimestampTimezone() {
  const saved = localStorage.getItem('meshcore-timestamp-timezone');
  if (saved === 'utc' || saved === 'local') return saved;
  const serverDefault = window.SITE_CONFIG?.timestamps?.timezone;
  return serverDefault === 'utc' ? 'utc' : 'local';
}

function getTimestampFormatPreset() {
  const saved = localStorage.getItem('meshcore-timestamp-format');
  if (saved === 'iso' || saved === 'iso-seconds' || saved === 'locale') return saved;
  const serverDefault = window.SITE_CONFIG?.timestamps?.formatPreset;
  return (serverDefault === 'iso' || serverDefault === 'iso-seconds' || serverDefault === 'locale') ? serverDefault : 'iso';
}

function getTimestampCustomFormat() {
  if (window.SITE_CONFIG?.timestamps?.allowCustomFormat !== true) return '';
  const saved = localStorage.getItem('meshcore-timestamp-custom-format');
  if (saved != null) return String(saved);
  const serverDefault = window.SITE_CONFIG?.timestamps?.customFormat;
  return serverDefault == null ? '' : String(serverDefault);
}

function pad2(v) { return String(v).padStart(2, '0'); }
function pad3(v) { return String(v).padStart(3, '0'); }

function formatIsoLike(d, timezone, includeMs) {
  const useUtc = timezone === 'utc';
  const year = useUtc ? d.getUTCFullYear() : d.getFullYear();
  const month = useUtc ? d.getUTCMonth() + 1 : d.getMonth() + 1;
  const day = useUtc ? d.getUTCDate() : d.getDate();
  const hour = useUtc ? d.getUTCHours() : d.getHours();
  const minute = useUtc ? d.getUTCMinutes() : d.getMinutes();
  const second = useUtc ? d.getUTCSeconds() : d.getSeconds();
  const ms = useUtc ? d.getUTCMilliseconds() : d.getMilliseconds();
  let out = year + '-' + pad2(month) + '-' + pad2(day) + ' ' + pad2(hour) + ':' + pad2(minute) + ':' + pad2(second);
  if (includeMs) out += '.' + pad3(ms);
  return out;
}

function formatTimestampCustom(d, formatString, timezone) {
  if (!/YYYY|MM|DD|HH|mm|ss|SSS|Z/.test(String(formatString))) return '';
  const useUtc = timezone === 'utc';
  const replacements = {
    YYYY: String(useUtc ? d.getUTCFullYear() : d.getFullYear()),
    MM: pad2((useUtc ? d.getUTCMonth() : d.getMonth()) + 1),
    DD: pad2(useUtc ? d.getUTCDate() : d.getDate()),
    HH: pad2(useUtc ? d.getUTCHours() : d.getHours()),
    mm: pad2(useUtc ? d.getUTCMinutes() : d.getMinutes()),
    ss: pad2(useUtc ? d.getUTCSeconds() : d.getSeconds()),
    SSS: pad3(useUtc ? d.getUTCMilliseconds() : d.getMilliseconds()),
    Z: (timezone === 'utc' ? 'UTC' : 'local')
  };
  return String(formatString).replace(/YYYY|MM|DD|HH|mm|ss|SSS|Z/g, token => replacements[token] || token);
}

function formatAbsoluteTimestamp(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (!isFinite(d.getTime())) return '—';
  const timezone = getTimestampTimezone();
  const preset = getTimestampFormatPreset();
  const customFormat = getTimestampCustomFormat().trim();
  if (customFormat) {
    const customOut = formatTimestampCustom(d, customFormat, timezone);
    if (customOut && !/Invalid Date|NaN|undefined|null/.test(customOut)) return customOut;
  }
  if (preset === 'iso-seconds') return formatIsoLike(d, timezone, true);
  if (preset === 'locale') {
    if (timezone === 'utc') return d.toLocaleString([], { timeZone: 'UTC' });
    return d.toLocaleString();
  }
  return formatIsoLike(d, timezone, false);
}

function formatTimestamp(isoString, mode) {
  return formatTimestampWithTooltip(isoString, mode).text;
}

function formatTimestampWithTooltip(isoString, mode) {
  if (!isoString) return { text: '—', tooltip: '—', isFuture: false };
  const d = new Date(isoString);
  if (!isFinite(d.getTime())) return { text: '—', tooltip: '—', isFuture: false };
  const activeMode = mode === 'absolute' || mode === 'ago' ? mode : getTimestampMode();
  const isFuture = d.getTime() > Date.now();
  const absolute = formatAbsoluteTimestamp(isoString);
  const relative = timeAgo(isoString);
  const text = isFuture ? absolute : (activeMode === 'absolute' ? absolute : relative);
  const tooltip = isFuture ? relative : (activeMode === 'absolute' ? relative : absolute);
  return { text, tooltip, isFuture };
}

// Format a Date for chart axis labels, respecting customizer timestamp settings.
// shortForm: true = time only (for intra-day), false = date+time (multi-day).
function formatChartAxisLabel(d, shortForm) {
  if (!(d instanceof Date) || !isFinite(d.getTime())) return '—';
  var timezone = (typeof getTimestampTimezone === 'function') ? getTimestampTimezone() : 'local';
  var preset = (typeof getTimestampFormatPreset === 'function') ? getTimestampFormatPreset() : 'iso';
  var useUtc = timezone === 'utc';

  if (preset === 'locale') {
    if (shortForm) {
      var opts = { hour: '2-digit', minute: '2-digit' };
      if (useUtc) opts.timeZone = 'UTC';
      return d.toLocaleTimeString([], opts);
    }
    var opts2 = { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' };
    if (useUtc) opts2.timeZone = 'UTC';
    return d.toLocaleString([], opts2);
  }

  // ISO-style (iso or iso-seconds)
  var hour = useUtc ? d.getUTCHours() : d.getHours();
  var minute = useUtc ? d.getUTCMinutes() : d.getMinutes();
  var timeStr = pad2(hour) + ':' + pad2(minute);
  if (preset === 'iso-seconds') {
    var sec = useUtc ? d.getUTCSeconds() : d.getSeconds();
    timeStr += ':' + pad2(sec);
  }
  if (shortForm) return timeStr;
  var month = useUtc ? d.getUTCMonth() + 1 : d.getMonth() + 1;
  var day = useUtc ? d.getUTCDate() : d.getDate();
  return pad2(month) + '-' + pad2(day) + ' ' + timeStr;
}

function truncate(str, len) {
  if (!str) return '';
  return str.length > len ? str.slice(0, len) + '…' : str;
}

// --- Favorites ---
const FAV_KEY = 'meshcore-favorites';
function getFavorites() {
  try { return JSON.parse(localStorage.getItem(FAV_KEY) || '[]'); } catch { return []; }
}
function isFavorite(pubkey) { return getFavorites().includes(pubkey); }
function toggleFavorite(pubkey) {
  const favs = getFavorites();
  const idx = favs.indexOf(pubkey);
  if (idx >= 0) favs.splice(idx, 1); else favs.push(pubkey);
  localStorage.setItem(FAV_KEY, JSON.stringify(favs));
  return idx < 0; // true if now favorited
}
function favStarIconHtml(on) {
  var id = on ? 'ph-star-fill' : 'ph-star';
  return '<svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#' + id + '"/></svg>';
}
function favStar(pubkey, cls) {
  const on = isFavorite(pubkey);
  return '<button class="fav-star ' + (cls || '') + (on ? ' on' : '') + '" data-fav="' + pubkey + '" aria-label="Toggle favorite" aria-pressed="' + (on ? 'true' : 'false') + '" title="' + (on ? 'Remove from favorites' : 'Add to favorites') + '">' + favStarIconHtml(on) + '</button>';
}
function bindFavStars(container, onToggle) {
  container.querySelectorAll('.fav-star').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const pk = btn.dataset.fav;
      const nowOn = toggleFavorite(pk);
      btn.innerHTML = favStarIconHtml(nowOn);
      btn.classList.toggle('on', nowOn);
      btn.setAttribute('aria-pressed', nowOn ? 'true' : 'false');
      btn.title = nowOn ? 'Remove from favorites' : 'Add to favorites';
      if (onToggle) onToggle(pk, nowOn);
    });
  });
}

function formatHex(hex) {
  if (!hex) return '';
  return hex.match(/.{1,2}/g).join(' ');
}

function createColoredHexDump(hex, ranges) {
  if (!hex || !ranges || !ranges.length) return `<span class="hex-byte">${formatHex(hex)}</span>`;
  const bytes = hex.match(/.{1,2}/g) || [];
  // Build per-byte class map; later ranges override earlier
  const classMap = new Array(bytes.length).fill('');
  const LABEL_CLASS = {
    'Header': 'hex-header', 'Path Length': 'hex-pathlen', 'Transport Codes': 'hex-transport',
    'Path': 'hex-path', 'Payload': 'hex-payload', 'PubKey': 'hex-pubkey',
    'Timestamp': 'hex-timestamp', 'Signature': 'hex-signature', 'Flags': 'hex-flags',
    'Latitude': 'hex-location', 'Longitude': 'hex-location', 'Name': 'hex-name',
  };
  for (const r of ranges) {
    const cls = LABEL_CLASS[r.label] || 'hex-payload';
    for (let i = r.start; i <= Math.min(r.end, bytes.length - 1); i++) classMap[i] = cls;
  }
  let html = '', prevCls = null;
  for (let i = 0; i < bytes.length; i++) {
    const cls = classMap[i];
    if (cls !== prevCls) {
      if (prevCls !== null) html += '</span>';
      html += `<span class="hex-byte ${cls}">`;
      prevCls = cls;
    } else {
      html += ' ';
    }
    html += bytes[i];
  }
  if (prevCls !== null) html += '</span>';
  return html;
}

function buildHexLegend(ranges) {
  if (!ranges || !ranges.length) return '';
  const LABEL_CLASS = {
    'Header': 'hex-header', 'Path Length': 'hex-pathlen', 'Transport Codes': 'hex-transport',
    'Path': 'hex-path', 'Payload': 'hex-payload', 'PubKey': 'hex-pubkey',
    'Timestamp': 'hex-timestamp', 'Signature': 'hex-signature', 'Flags': 'hex-flags',
    'Latitude': 'hex-location', 'Longitude': 'hex-location', 'Name': 'hex-name',
  };
  const BG_COLORS = {
    'hex-header': '#f38ba8', 'hex-pathlen': '#fab387', 'hex-transport': '#89b4fa',
    'hex-path': '#a6e3a1', 'hex-payload': '#f9e2af', 'hex-pubkey': '#f9e2af',
    'hex-timestamp': '#fab387', 'hex-signature': '#f38ba8', 'hex-flags': '#94e2d5',
    'hex-location': '#89b4fa', 'hex-name': '#cba6f7',
  };
  const seen = new Set();
  let html = '';
  for (const r of ranges) {
    if (seen.has(r.label)) continue;
    seen.add(r.label);
    const cls = LABEL_CLASS[r.label] || 'hex-payload';
    const bg = BG_COLORS[cls] || '#f9e2af';
    html += `<span><span class="swatch" style="background:${bg}"></span>${r.label}</span>`;
  }
  return html;
}

// --- WebSocket ---
let ws = null;
let wsListeners = [];

// --- Brand-logo packet-driven pulse (#1173) ---
// Replaces the legacy live-dot indicator. Class-toggle only (CSS animations); colors come from
// --logo-accent / --logo-accent-hi tokens. Test seam at window.__corescopeLogo.
//
// Cache the prefers-reduced-motion MediaQueryList ONCE at module load (#1177
// Carmack must-fix #2). Calling window.matchMedia on every pulse() allocates
// a new MQL + parses the query string — wasteful at 15Hz. The CSS @media rule
// already handles render-time switching, so we just cache and read .matches.
var _reducedMotionMQL = null;
try {
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    _reducedMotionMQL = window.matchMedia('(prefers-reduced-motion: reduce)');
  }
} catch (_) { _reducedMotionMQL = null; }

const Logo = (function () {
  const RATE_GAP_MS = 66;       // 15/sec (≤16 toggles per second).
  const HALF_MS = 80;           // each half of a ping ≤80ms.
  const stats = { triggered: 0, dropped: 0 };
  let lastPingTs = 0;
  let flip = 0;                 // 0 → A→B, 1 → B→A.
  let lastDirection = null;     // 'a' or 'b' (source circle).
  let connected = true;         // WS state — gates in-flight chained pulses.
  let generation = 0;           // bumped on setConnected(false) / visibilitychange to cancel scheduled halves.

  function reducedMotion() {
    return _reducedMotionMQL ? !!_reducedMotionMQL.matches : false;
  }
  function $all(sel) { return Array.prototype.slice.call(document.querySelectorAll(sel)); }
  function clearAll() {
    $all('.brand-logo circle.logo-node-a, .brand-mark-only circle.logo-node-a,' +
         '.brand-logo circle.logo-node-b, .brand-mark-only circle.logo-node-b').forEach((el) => {
      el.classList.remove('logo-pulse-active', 'logo-pulse-blip');
    });
  }
  function pulseChained(srcSel, dstSel) {
    const gen = generation;
    // Source half: ~80ms.
    $all(srcSel).forEach((el) => el.classList.add('logo-pulse-active'));
    setTimeout(() => {
      $all(srcSel).forEach((el) => el.classList.remove('logo-pulse-active'));
      // Destination half: scheduled via rAF then ~80ms.
      // Bail if WS dropped (or another disconnect cycle ran) since this ping started —
      // otherwise a zombie pulse fires on a logo that's already showing the
      // .logo-disconnected sustained state.
      if (gen !== generation || !connected) return;
      requestAnimationFrame(() => {
        if (gen !== generation || !connected) return;
        $all(dstSel).forEach((el) => el.classList.add('logo-pulse-active'));
        setTimeout(() => {
          $all(dstSel).forEach((el) => el.classList.remove('logo-pulse-active'));
        }, HALF_MS);
      });
    }, HALF_MS);
  }
  function pulseBlip(dstSel) {
    // Reduced-motion: single-step opacity blip on destination only.
    $all(dstSel).forEach((el) => el.classList.add('logo-pulse-blip'));
    setTimeout(() => {
      $all(dstSel).forEach((el) => el.classList.remove('logo-pulse-blip'));
    }, 140);
  }
  function pulse(_msg) {
    // Hidden-tab gate (#1177 Carmack must-fix #1): drop the pulse BEFORE
    // mutating lastPingTs and BEFORE scheduling any rAF/setTimeout chain.
    // Background tabs throttle timers but still ran the source-class toggle
    // and queued a chain that fired in a clump on tab focus — wasted work
    // and a visible storm. Returning early here makes the gate cost ~1
    // property read per WS message.
    if (typeof document !== 'undefined' && document.hidden) {
      stats.dropped++;
      return false;
    }
    if (!connected) { stats.dropped++; return false; }
    const now = (typeof performance !== 'undefined' && performance.now) ? performance.now() : Date.now();
    if (now - lastPingTs < RATE_GAP_MS) { stats.dropped++; return false; }
    lastPingTs = now;
    stats.triggered++;
    const aToB = (flip === 0);
    flip ^= 1;
    lastDirection = aToB ? 'a' : 'b';
    const srcSel = aToB ? '.brand-logo circle.logo-node-a, .brand-mark-only circle.logo-node-a'
                        : '.brand-logo circle.logo-node-b, .brand-mark-only circle.logo-node-b';
    const dstSel = aToB ? '.brand-logo circle.logo-node-b, .brand-mark-only circle.logo-node-b'
                        : '.brand-logo circle.logo-node-a, .brand-mark-only circle.logo-node-a';
    if (reducedMotion()) {
      pulseBlip(dstSel);
    } else {
      pulseChained(srcSel, dstSel);
    }
    return true;
  }
  function setConnected(isConnected) {
    connected = !!isConnected;
    // Bump generation so any in-flight chained-pulse callbacks bail before
    // toggling classes on the destination circle (otherwise a zombie pulse
    // briefly fights the .logo-disconnected sustained desaturate state).
    generation++;
    $all('.brand-logo, .brand-mark-only').forEach((el) => {
      if (connected) el.classList.remove('logo-disconnected');
      else el.classList.add('logo-disconnected');
    });
    // #1174 mesh-op review: mirror connected state onto the bottom-nav so
    // the 2px top-border indicator (see bottom-nav.css) goes red on
    // disconnect. Mesh-alive is otherwise invisible at ≤768 because
    // .nav-stats is hidden at that breakpoint.
    var bn = document.querySelector('[data-bottom-nav]');
    if (bn) {
      if (connected) bn.classList.remove('disconnected');
      else bn.classList.add('disconnected');
    }
    if (!connected) clearAll();
  }
  // Expose hook for E2E + customizer/devtools introspection.
  // Frozen so consumers can't replace .pulse / .setConnected from outside
  // (the seam is read-only — invocation only).
  const api = Object.freeze({
    pulse: pulse,
    setConnected: setConnected,
    get lastDirection() { return lastDirection; },
    get stats() { return { triggered: stats.triggered, dropped: stats.dropped }; },
  });
  try { window.__corescopeLogo = api; } catch (_) {}

  // Visibility gate (#1177 Carmack must-fix #1): when the tab becomes
  // hidden, bump generation so any in-flight chained pulse halves bail
  // out before they paint, and clear any active pulse classes. The
  // pulse() entry already early-returns on document.hidden — this handles
  // pulses already mid-flight at the moment the tab is backgrounded.
  try {
    if (typeof document !== 'undefined' && typeof document.addEventListener === 'function') {
      document.addEventListener('visibilitychange', function () {
        if (document.hidden) {
          generation++;
          clearAll();
        }
      });
    }
  } catch (_) {}

  return api;
})();

function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}`);
  ws.onopen = () => Logo.setConnected(true);
  ws.onclose = () => {
    Logo.setConnected(false);
    setTimeout(connectWS, 3000);
  };
  ws.onerror = () => ws.close();
  ws.onmessage = (e) => {
    Logo.pulse(e);
    try {
      const msg = JSON.parse(e.data);
      // Debounce cache invalidation — don't nuke on every packet
      if (!api._invalidateTimer) {
        api._invalidateTimer = setTimeout(() => {
          api._invalidateTimer = null;
          invalidateApiCache('/stats');
          invalidateApiCache('/nodes');
        }, 5000);
      }
      wsListeners.forEach(fn => fn(msg));
    } catch {}
  };
}

function onWS(fn) { wsListeners.push(fn); }
function offWS(fn) { wsListeners = wsListeners.filter(f => f !== fn); }

// --- Pull-to-reconnect (#1063) ---
// Touch-device pull-down at scrollTop=0 reconnects the WebSocket
// (instead of triggering native pull-to-refresh full-page reload).
// Visual indicator pulses during pull; toast confirms result.
const PULL_THRESHOLD_PX = 140;
let _pullToast = null;
let _pullToastTimer = null;
let _pullIndicator = null;

function _ensurePullIndicator() {
  if (_pullIndicator && document.body && typeof document.body.contains === 'function' && document.body.contains(_pullIndicator)) return _pullIndicator;
  if (_pullIndicator) return _pullIndicator;
  const el = document.createElement('div');
  el.id = 'pullReconnectIndicator';
  el.setAttribute('aria-hidden', 'true');
  el.innerHTML = '<span class="prr-icon">⟳</span>';
  el.style.cssText = [
    'position:fixed', 'top:0', 'left:50%', 'transform:translate(-50%,-100%)',
    'z-index:99999', 'padding:8px 14px', 'border-radius:0 0 12px 12px',
    'background:var(--accent,#2563eb)', 'color:#fff', 'font:14px/1 var(--font,system-ui)',
    'box-shadow:0 2px 8px rgba(0,0,0,.2)', 'pointer-events:none',
    'transition:transform .15s ease, opacity .15s ease', 'opacity:0',
  ].join(';');
  document.body.appendChild(el);
  _pullIndicator = el;
  return el;
}

function _showPullToast(msg, ok) {
  try {
    if (_pullToast && _pullToast.remove) _pullToast.remove();
  } catch (e) {}
  if (_pullToastTimer) { try { clearTimeout(_pullToastTimer); } catch (e) {} _pullToastTimer = null; }
  const el = document.createElement('div');
  el.className = 'pull-reconnect-toast';
  el.textContent = msg;
  el.style.cssText = [
    'position:fixed', 'top:12px', 'left:50%', 'transform:translateX(-50%)',
    'z-index:99999', 'padding:8px 16px', 'border-radius:8px',
    'background:' + (ok ? 'var(--status-green,#16a34a)' : 'var(--status-red,#dc2626)'),
    'color:#fff', 'font:14px/1.2 var(--font,system-ui)',
    'box-shadow:0 2px 8px rgba(0,0,0,.2)', 'pointer-events:none',
  ].join(';');
  document.body.appendChild(el);
  _pullToast = el;
  _pullToastTimer = setTimeout(function () {
    _pullToastTimer = null;
    try { el.remove(); } catch (e) {}
  }, 1800);
}

function pullReconnect() {
  // If WS is connected (readyState OPEN), give a brief "Connected"
  // confirmation but still cycle so the user sees fresh data.
  const wasOpen = ws && ws.readyState === 1;
  if (wasOpen) {
    _showPullToast('Connected', true);
    // Fast cycle: close and let onclose reconnect immediately
    try { ws.close(); } catch (e) {}
  } else {
    _showPullToast('Reconnecting…', true);
    try { if (ws) ws.close(); } catch (e) {}
    // onclose handler schedules reconnect; force one now in case ws was null
    try { connectWS(); } catch (e) {}
  }
}

function _isTouchDevice() {
  try {
    return ('ontouchstart' in window) ||
      (navigator && (navigator.maxTouchPoints > 0 || navigator.msMaxTouchPoints > 0));
  } catch (e) { return false; }
}

function setupPullToReconnect() {
  // Always attach listeners (tests + future-proof). Inside the handler we
  // gate on _isTouchDevice() AND scrollTop=0 so desktop/scrolled pages are
  // unaffected.
  let startY = null;
  let pulling = false;
  let dist = 0;

  function getScrollTop() {
    return (document.documentElement && document.documentElement.scrollTop) ||
      (document.body && document.body.scrollTop) || 0;
  }

  function onStart(e) {
    if (!_isTouchDevice()) return;
    // Strict scrollTop === 0: ignore any negative overscroll, ignore any scrolled state
    if (getScrollTop() !== 0) { startY = null; pulling = false; return; }
    const t = e.touches && e.touches[0];
    startY = t ? t.clientY : null;
    pulling = false;
    dist = 0;
  }

  function onMove(e) {
    if (startY == null) return;
    // Cancel gesture if scrollTop leaves 0 (page scrolled mid-pull)
    if (getScrollTop() !== 0) { startY = null; pulling = false; dist = 0; return; }
    const t = e.touches && e.touches[0];
    if (!t) return;
    const dy = t.clientY - startY;
    if (dy <= 0) {
      // Upward swipe / retract. If we were past the commit threshold and the
      // user retracts back, cancel the gesture so a subsequent touchend does
      // NOT fire reconnect.
      if (pulling) {
        pulling = false;
        dist = 0;
        if (_pullIndicator) {
          _pullIndicator.style.opacity = '0';
          _pullIndicator.style.transform = 'translate(-50%, -100%)';
        }
      }
      return;
    }
    dist = dy;
    if (dy > 8) {
      pulling = true;
      const ind = _ensurePullIndicator();
      const pct = Math.min(1, dy / PULL_THRESHOLD_PX);
      ind.style.opacity = String(pct);
      ind.style.transform = 'translate(-50%, ' + (-100 + pct * 100) + '%)';
      const icon = ind.querySelector && ind.querySelector('.prr-icon');
      if (icon) icon.style.transform = 'rotate(' + Math.round(pct * 360) + 'deg)';
      // Only block native pull-to-refresh once we've crossed the commit
      // threshold — below that, let the browser handle natural scroll/bounce.
      if (dy >= PULL_THRESHOLD_PX && typeof e.preventDefault === 'function' && e.cancelable !== false) {
        try { e.preventDefault(); } catch (_) {}
      }
    }
  }

  function onEnd() {
    const wasPulling = pulling;
    const finalDist = dist;
    const stillAtTop = getScrollTop() === 0;
    startY = null; pulling = false; dist = 0;
    if (_pullIndicator) {
      _pullIndicator.style.opacity = '0';
      _pullIndicator.style.transform = 'translate(-50%, -100%)';
    }
    // Trigger only if: gesture was active, crossed threshold, and page is still at scrollTop=0.
    if (wasPulling && finalDist >= PULL_THRESHOLD_PX && stillAtTop) {
      try { (window.pullReconnect || pullReconnect)(); } catch (e) {}
    }
  }

  document.addEventListener('touchstart', onStart, { passive: true });
  document.addEventListener('touchmove', onMove, { passive: false });
  document.addEventListener('touchend', onEnd, { passive: true });
  document.addEventListener('touchcancel', onEnd, { passive: true });
}

window.pullReconnect = pullReconnect;
window.setupPullToReconnect = setupPullToReconnect;
window.connectWS = connectWS;

/* Global escapeHtml — used by multiple pages.
   5-char OWASP set: escapes ' too so this helper is safe in both
   double-quoted AND single-quoted attribute contexts (e.g. the
   data-conflict='${escapeHtml(JSON.stringify(...))}' attr in
   hop-display.js, where JSON containing a single quote would
   otherwise break out of the attribute). Fixes #1536.

   CANONICAL ESCAPE for HTML sinks that interpolate node-controlled or
   MQTT-controlled fields (name, adv_name, observer, sender, channel,
   pubkey, body, …). Enforced at PR-creation time by:
     - scripts/check-xss-sinks.sh                            (local mirror)
     - ~/.openclaw/skills/pr-preflight/scripts/check-xss-sinks.sh  (canonical)
     - test-preflight-xss-gate.js                            (CI gate)
   See also: escapeAttr (public/home.js, public/path-inspector.js) for
   attribute-only contexts. */
function escapeHtml(s) {
  if (s == null) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

/* Global debounce */
function debounce(fn, ms) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}

/* Debounced WS helper — batches rapid messages, calls fn with array of msgs */
function debouncedOnWS(fn, ms) {
  if (typeof ms === 'undefined') ms = 250;
  let pending = [];
  let timer = null;
  function handler(msg) {
    pending.push(msg);
    if (!timer) {
      timer = setTimeout(function () {
        const batch = pending;
        pending = [];
        timer = null;
        fn(batch);
      }, ms);
    }
  }
  onWS(handler);
  return handler; // caller stores this to pass to offWS() in destroy
}

// --- Router ---
const pages = {};

function registerPage(name, mod) { pages[name] = mod; }

// Tools landing page — shows sub-menu with Trace and Path Inspector (spec §2.8, M1 fix).
registerPage('tools-landing', {
  init: function (container) {
    container.innerHTML =
      '<div class="tools-landing">' +
        '<h2>Tools</h2>' +
        '<div class="tools-menu">' +
          '<a href="#/tools/path-inspector" class="tools-card"><h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> Path Inspector</h3><p>Resolve prefix paths to candidate full-pubkey routes with confidence scoring.</p></a>' +
          '<a href="#/tools/trace/" class="tools-card"><h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> Trace Viewer</h3><p>View detailed packet traces by hash.</p></a>' +
        '</div>' +
      '</div>';
  },
  destroy: function () {}
});

let currentPage = null;

function closeNav() {
  document.querySelector('.nav-links')?.classList.remove('open');
  document.body.classList.remove('nav-open');
  var btn = document.getElementById('hamburger');
  if (btn) btn.setAttribute('aria-expanded', 'false');
  closeMoreMenu();
}

function closeMoreMenu() {
  var menu = document.getElementById('navMoreMenu');
  var btn = document.getElementById('navMoreBtn');
  if (menu) menu.classList.remove('open');
  if (btn) btn.setAttribute('aria-expanded', 'false');
}

function navigate() {
  closeNav();

  // Backward-compat redirect: #/traces/<hash> → #/tools/trace/<hash> (issue #944).
  // Uses replaceState (not location.hash =) so this redirect doesn't add its
  // own history entry, otherwise the back button gets stuck bouncing off it.
  if (location.hash.startsWith('#/traces/')) {
    history.replaceState(null, '', location.hash.replace('#/traces/', '#/tools/trace/'));
    navigate();
    return;
  }

  // Backward-compat redirect: #/roles → #/analytics?tab=roles (issue #1085).
  // The Roles page was folded into the Analytics tab strip; old links and
  // bookmarks must keep working. Uses replaceState for the same reason as above.
  if (location.hash === '#/roles' || location.hash.startsWith('#/roles?') || location.hash.startsWith('#/roles/')) {
    history.replaceState(null, '', '#/analytics?tab=roles');
    navigate();
    return;
  }

  const hash = location.hash.replace('#/', '') || 'packets';
  const route = hash.split('?')[0];

  // Handle parameterized routes: nodes/<pubkey> → nodes page + select
  let basePage = route;
  let routeParam = null;
  const slashIdx = route.indexOf('/');
  if (slashIdx > 0) {
    basePage = route.substring(0, slashIdx);
    routeParam = decodeURIComponent(route.substring(slashIdx + 1));
  }

  // Special route: nodes/PUBKEY/analytics → node-analytics page
  if (basePage === 'nodes' && routeParam && routeParam.endsWith('/analytics')) {
    basePage = 'node-analytics';
  }

  // Special route: nodes/PUBKEY/reach → node-reach page
  if (basePage === 'nodes' && routeParam && routeParam.endsWith('/reach')) {
    basePage = 'node-reach';
  }

  // Special route: packet/123 → standalone packet detail page
  if (basePage === 'packet' && routeParam) {
    basePage = 'packet-detail';
  }

  // Special route: observers/ID → observer detail page
  if (basePage === 'observers' && routeParam) {
    basePage = 'observer-detail';
  }

  // Tools sub-routing (issue #944): tools/trace/<hash>, tools/path-inspector
  if (basePage === 'tools') {
    if (routeParam && routeParam.startsWith('trace/')) {
      basePage = 'traces';
      routeParam = routeParam.substring(6); // strip "trace/"
    } else if (routeParam === 'path-inspector' || (routeParam && routeParam.startsWith('path-inspector'))) {
      basePage = 'path-inspector';
      routeParam = null;
    } else if (!routeParam) {
      // Default tools landing shows menu with both entries.
      basePage = 'tools-landing';
    }
  }
  // Also support old #/traces (no sub-path) → traces page.
  if (basePage === 'traces' && !routeParam) {
    basePage = 'traces';
  }

  // Update nav active state
  document.querySelectorAll('.nav-link[data-route]').forEach(el => {
    el.classList.toggle('active', el.dataset.route === basePage || (el.dataset.route === 'tools' && (basePage === 'traces' || basePage === 'path-inspector' || basePage === 'tools-landing')));
  });
  // Update "More" button to show active state if a low-priority page is selected
  var moreBtn = document.getElementById('navMoreBtn');
  if (moreBtn) {
    var moreMenu = document.getElementById('navMoreMenu');
    var hasActiveMore = moreMenu && moreMenu.querySelector('.nav-link.active');
    moreBtn.classList.toggle('active', !!hasActiveMore);
  }

  if (currentPage && pages[currentPage]?.destroy) {
    pages[currentPage].destroy();
  }
  currentPage = basePage;

  // #1572 — defensive: ensure body.live-fullscreen is cleared whenever
  // we navigate to a non-live route. Live page's destroy() also clears
  // it, but this guards against any boot path (deep-link, restore) that
  // somehow puts the body into fullscreen state outside /live.
  if (basePage !== 'live' && document.body) {
    document.body.classList.remove('live-fullscreen');
  }

  const app = document.getElementById('app');
  // Pages with fixed-height containers (maps, virtual-scroll, split-panels)
  const fixedPages = { packets: 1, nodes: 1, map: 1, live: 1, channels: 1, 'audio-lab': 1 };
  app.classList.toggle('app-fixed', basePage in fixedPages);

  // Issue #1369: ?embed=1 chrome suppression for cross-domain iframe embeds.
  // Toggles body.embed; CSS in style.css hides top-nav / bottom-nav / nav-drawer
  // and zeroes body padding so /#/map and /#/channels render full-bleed.
  try {
    var hashSearch = (location.hash.split('?')[1] || '');
    document.body.classList.toggle('embed', shouldEmbedRoute(basePage, hashSearch));
  } catch (_) { /* DOM may be missing in some test contexts */ }
  if (pages[basePage]?.init) {
    const t0 = performance.now();
    pages[basePage].init(app, routeParam);
    const ms = performance.now() - t0;
    if (ms > 100) console.warn(`[SLOW PAGE] ${basePage} init took ${Math.round(ms)}ms`);
    app.classList.remove('page-enter'); void app.offsetWidth; app.classList.add('page-enter');
    // #1206 followup: sweep TableResponsive ResizeObservers whose tables were
    // detached when the prior page's destroy ran (its <table> was wired in
    // TableResponsive.register; on SPA nav app.innerHTML is rebuilt by the
    // next init, so the previous table is now detached). Without this, the
    // last-rendered table on each remountable page leaks 1 RO per remount.
    if (window.TableResponsive && typeof window.TableResponsive.sweep === 'function') {
      try { window.TableResponsive.sweep(); } catch (_) {}
    }
    // #630-7: SPA focus management — move focus to first heading or main content
    requestAnimationFrame(function() {
      var heading = app.querySelector('h1, h2, h3, [role="heading"]');
      if (heading) { heading.setAttribute('tabindex', '-1'); heading.focus({ preventScroll: true }); }
      else { app.setAttribute('tabindex', '-1'); app.focus({ preventScroll: true }); }
    });
  } else {
    app.innerHTML = `<div style="padding:40px;text-align:center;color:#6b7280"><h2>${route}</h2><p>Page not yet implemented.</p></div>`;
  }
}

window.addEventListener('hashchange', navigate);
let _themeRefreshTimer = null;
window.addEventListener('theme-changed', () => {
  if (_themeRefreshTimer) clearTimeout(_themeRefreshTimer);
  _themeRefreshTimer = setTimeout(() => {
    _themeRefreshTimer = null;
    window.dispatchEvent(new CustomEvent('theme-refresh'));
  }, 300);
});
window.addEventListener('timestamp-mode-changed', () => {
  window.dispatchEvent(new CustomEvent('theme-refresh'));
});
window.addEventListener('DOMContentLoaded', () => {
  connectWS();
  setupPullToReconnect();

  // --- Dark Mode ---
  const darkToggle = document.getElementById('darkModeToggle');
  const darkCheckbox = document.getElementById('darkModeCheckbox');
  const savedTheme = localStorage.getItem('meshcore-theme');
  function applyTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme);
    if (darkCheckbox) darkCheckbox.checked = theme === 'dark';
    localStorage.setItem('meshcore-theme', theme);
    // Re-apply user theme CSS vars for the correct mode (light/dark)
    reapplyUserThemeVars(theme === 'dark');
  }
  function reapplyUserThemeVars(dark) {
    try {
      var userTheme = JSON.parse(localStorage.getItem('meshcore-user-theme') || '{}');
      if (!userTheme.theme && !userTheme.themeDark) {
        // Fall back to server config
        var cfg = window.SITE_CONFIG || {};
        if (!cfg.theme && !cfg.themeDark) return;
        userTheme = cfg;
      }
      var themeData = dark ? Object.assign({}, userTheme.theme || {}, userTheme.themeDark || {}) : (userTheme.theme || {});
      if (!Object.keys(themeData).length) return;
      var varMap = {
        accent: '--accent', accentHover: '--accent-hover',
        navBg: '--nav-bg', navBg2: '--nav-bg2', navText: '--nav-text', navTextMuted: '--nav-text-muted',
        background: '--surface-0', text: '--text', textMuted: '--text-muted', border: '--border',
        statusGreen: '--status-green', statusYellow: '--status-yellow', statusRed: '--status-red',
        surface1: '--surface-1', surface2: '--surface-2', surface3: '--surface-3',
        cardBg: '--card-bg', contentBg: '--content-bg', inputBg: '--input-bg',
        rowStripe: '--row-stripe', rowHover: '--row-hover', detailBg: '--detail-bg',
        selectedBg: '--selected-bg', sectionBg: '--section-bg',
        font: '--font', mono: '--mono'
      };
      var root = document.documentElement.style;
      for (var key in varMap) {
        if (themeData[key]) root.setProperty(varMap[key], themeData[key]);
      }
      if (themeData.background) root.setProperty('--content-bg', themeData.contentBg || themeData.background);
      if (themeData.surface1) root.setProperty('--card-bg', themeData.cardBg || themeData.surface2 || themeData.surface1);
      // Nav gradient
      if (themeData.navBg) {
        var nav = document.querySelector('.top-nav');
        if (nav) { nav.style.background = ''; void nav.offsetHeight; }
      }
    } catch (e) { console.error('[theme] reapply error:', e); }
  }
  // On load: respect saved pref, else OS pref, else light
  if (savedTheme) {
    applyTheme(savedTheme);
  } else if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
    applyTheme('dark');
  } else {
    applyTheme('light');
  }
  if (darkCheckbox) {
    darkCheckbox.addEventListener('change', () => {
      applyTheme(darkCheckbox.checked ? 'dark' : 'light');
    });
  } else {
    // Fallback for button-style toggle (upstream compatibility)
    darkToggle.addEventListener('click', () => {
      const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
      applyTheme(isDark ? 'light' : 'dark');
    });
  }
  // PR #893 follow-up: cross-tab sync — when another tab toggles theme,
  // mirror it here without re-persisting (avoid loop). Matches the pattern
  // used by the cb-presets storage listener below.
  window.addEventListener('storage', function (ev) {
    if (!ev || ev.key !== 'meshcore-theme' || !ev.newValue) return;
    if (ev.newValue !== 'dark' && ev.newValue !== 'light') return;
    document.documentElement.setAttribute('data-theme', ev.newValue);
    if (darkCheckbox) darkCheckbox.checked = ev.newValue === 'dark';
    try { reapplyUserThemeVars(ev.newValue === 'dark'); } catch (_) {}
  });

  // --- #1361 Colorblind preset bootstrap & cross-tab sync ---
  // cb-presets.js auto-inits on module load, but body may not have existed
  // yet (script loads in <head>); re-apply now that DOMContentLoaded fired
  // so body[data-cb-preset] is set before first paint of map/cluster bubbles.
  try {
    if (window.MeshCorePresets && typeof window.MeshCorePresets.initFromStorage === 'function') {
      window.MeshCorePresets.initFromStorage();
    }
  } catch (e) { console.error('[cb-preset] init failed:', e); }
  // Cross-tab sync: storage event listener is also registered inside
  // cb-presets.js, but we wire a redundant one here so any future refactor
  // of the module still leaves the cross-tab guarantee intact.
  window.addEventListener('storage', function (ev) {
    if (!ev || ev.key !== 'meshcore-cb-preset') return;
    if (window.MeshCorePresets && ev.newValue) {
      window.MeshCorePresets.applyPreset(ev.newValue, { skipPersist: true });
    }
  });

  // --- Hamburger Menu ---
  const hamburger = document.getElementById('hamburger');
  const navLinks = document.querySelector('.nav-links');
  hamburger.addEventListener('click', () => {
    const opening = !navLinks.classList.contains('open');
    navLinks.classList.toggle('open');
    document.body.classList.toggle('nav-open');
    hamburger.setAttribute('aria-expanded', String(opening));
  });
  navLinks.querySelectorAll('.nav-link').forEach(link => {
    link.addEventListener('click', closeNav);
  });

  // --- "More" dropdown — JS-driven Priority+ (Issue #1102) ---
  const navMoreBtn = document.getElementById('navMoreBtn');
  const navMoreMenu = document.getElementById('navMoreMenu');
  const navMoreWrap = document.querySelector('.nav-more-wrap');
  const navTop  = document.querySelector('.top-nav');
  const navLeft = document.querySelector('.nav-left');
  const navRightEl = document.querySelector('.nav-right');
  const linksContainer = document.querySelector('.nav-links');
  // Belt-and-braces null guards (#1105 MINOR 4): the outer block measures
  // and mutates all of these; if any are missing the layout math throws
  // before we can fall back gracefully.
  let navPriorityFn = null;
  if (navMoreBtn && navMoreMenu && navMoreWrap && navLeft && navRightEl && linksContainer && navTop) {
    // Measure available room and decide which links overflow.
    // Algorithm: try to fit all links inline. If the link strip doesn't
    // fit alongside .nav-right + .nav-brand, hide non-priority links one
    // at a time (right-to-left, lowest priority first) until it does.
    // Then mirror the hidden links into the "More ▾" menu so nothing
    // disappears from the user's reach.
    const allLinks = Array.from(linksContainer.querySelectorAll('.nav-link'));
    // overflowQueue (#1105 MINOR 6): the order links are removed from the
    // inline strip when space runs out. Built right-to-left from
    // non-priority links (lowest priority dropped first) and then high-
    // priority links as a last-resort tail. `data-priority="high"` is the
    // only signal — if you ever need finer ordering, switch to a numeric
    // attribute (e.g. data-overflow-order="3") rather than re-shuffling
    // index in HTML.
    // #1391: ALSO exclude the currently-active link from the queue.
    // The active pill has wider rendered width (background + padding),
    // and acceptance for #1391 requires "Active-route pill MUST always
    // be visible inline (never overflowed to More) at any viewport
    // ≥768px." The queue is rebuilt on hashchange (applyNavPriority
    // is wired to hashchange below), so the exclusion tracks the
    // current route automatically.
    function buildOverflowQueue() {
      var isPinned = function(a) {
        return a.dataset.priority === 'high' || a.classList.contains('active');
      };
      return allLinks.filter(a => !isPinned(a))
                     .reverse() // right-to-left
                     .concat(allLinks.filter(a => a.dataset.priority === 'high' && !a.classList.contains('active')).reverse());
    }
    var overflowQueue = buildOverflowQueue();

    function rebuildMoreMenu() {
      navMoreMenu.innerHTML = '';
      const hidden = allLinks.filter(a => a.classList.contains('is-overflow'));
      hidden.forEach(function(link) {
        var clone = link.cloneNode(true);
        // The clone is in the overflow menu, not the inline strip.
        clone.classList.remove('is-overflow');
        clone.setAttribute('role', 'menuitem');
        // cloneNode(true) preserves DOM but NOT event listeners. The
        // originals get `closeNav` attached up above (#1105 MINOR 5);
        // mirror that here so a click on the More-menu clone behaves
        // identically to a click on the inline link (closes the
        // hamburger panel + dismisses the More menu).
        clone.addEventListener('click', closeNav);
        clone.addEventListener('click', closeMoreMenu);
        navMoreMenu.appendChild(clone);
      });
      // If nothing overflows, hide the More button entirely so wide
      // viewports don't show a useless dropdown trigger.
      navMoreWrap.classList.toggle('is-hidden', hidden.length === 0);
      // Refresh active state on the More button (a hidden active link
      // means the More menu currently "is" the active section).
      var hasActiveMore = navMoreMenu.querySelector('.nav-link.active');
      navMoreBtn.classList.toggle('active', !!hasActiveMore);
    }

    // #1105 MINOR 1: cached intrinsic width of the More button. Captured
    // the first time `fits()` sees navMoreWrap rendered (display:flex).
    // Falls back to MORE_BTN_RESERVE_PX (a conservative initial guess
    // sized for "More ▾" at default font/padding) until that happens.
    var cachedMoreW = 0;
    var MORE_BTN_RESERVE_PX = 70;

    function applyNavPriority() {
      // Skip on mobile (<768px) — hamburger CSS owns that layout.
      if (window.innerWidth < 768) {
        allLinks.forEach(a => a.classList.remove('is-overflow'));
        navMoreWrap.classList.add('is-hidden');
        return;
      }
      // Reset: show everything, then hide as needed.
      allLinks.forEach(a => a.classList.remove('is-overflow'));
      navMoreWrap.classList.remove('is-hidden');
      // #1106: in the 768-1100px narrow-desktop band the CSS already
      // hides .nav-stats and tightens .nav-link padding (see the
      // "Nav narrow-desktop tightening" media query in style.css).
      // The design intent of that band is "show exactly the 5 high-
      // priority links + More". Pure measurement says everything fits
      // (~981px needed in a 1080px viewport once nav-stats is gone),
      // but the design contract — locked by test-nav-priority-1102-
      // e2e.js #1105 MINOR 7 — is exact identity, not "fits". Force-
      // collapse all non-high-priority links inside this band so the
      // overflow menu is non-empty and the high-priority set is the
      // only thing inline. Above 1100px the measurement loop below
      // owns the decision (and at 2560px nothing overflows).
      if (window.innerWidth <= 1100) {
        allLinks.forEach(a => {
          // #1391: never overflow the active-route pill, even in the
          // narrow-desktop CSS branch — acceptance requires it stay
          // inline at any viewport ≥768px. Without this guard, a
          // non-high-priority active route (e.g. /#/perf) would be
          // shoved into More alongside the rest.
          if (a.dataset.priority !== 'high' && !a.classList.contains('active')) {
            a.classList.add('is-overflow');
          }
        });
        rebuildMoreMenu();
        return;
      }
      // Iteratively hide low-priority links until the link strip fits.
      // .top-nav has overflow:hidden and .nav-left has flex-shrink:1, so
      // an overflowing strip silently clips rather than pushing
      // nav-right out — bounding-rect math on .nav-left lies. Instead
      // measure the *intrinsic* widths of the parts (independent of
      // current clipping) and compare to the viewport. SAFETY absorbs
      // the .top-nav side padding + nav-right inner gaps + sub-pixel
      // rounding (the historic #1055 bug was a 6–20px overlap).
      //
      // #1105 MINOR 3: at the 1101px media-query flip `.nav-stats`
      // toggles from display:none → flex (and vice-versa). The resize
      // handler is rAF-debounced and runs *after* the layout flip, so
      // navRightEl.scrollWidth measured here reflects the post-flip
      // intrinsic width — not stale pre-flip width.
      const navBrand   = document.querySelector('.nav-brand');
      const SAFETY     = 32;
      // #1105 MINOR 1+2: read both gap values from CSS rather than a
      // shared `GUTTER = 24` constant. Today `.nav-left` (gap between
      // brand/links/more/right cells) and `.nav-links` (gap between
      // individual link items) both resolve to --space-lg = 24px, but
      // they're conceptually distinct gaps. If --space-lg or .nav-left's
      // gap diverges in the future, the fit math must follow.
      const navLeftGap = parseFloat(getComputedStyle(navLeft).columnGap ||
                                    getComputedStyle(navLeft).gap || '0') || 0;
      // #1105 MINOR 1: compute the More-button reserve from its actual
      // rendered width on first measure, instead of a hard-coded 70px
      // fallback. Cached so we don't re-measure (offsetWidth is 0 when
      // display:none; we capture the value the first time it's visible).
      function fits() {
        const visibleLinks = allLinks.filter(a => !a.classList.contains('is-overflow'));
        let linkW = 0;
        visibleLinks.forEach(a => { linkW += a.getBoundingClientRect().width; });
        const linkGapPx = parseFloat(getComputedStyle(linksContainer).columnGap ||
                                     getComputedStyle(linksContainer).gap || '0') || 0;
        const linksGap = Math.max(0, visibleLinks.length - 1) * linkGapPx;
        const brandW = navBrand ? navBrand.getBoundingClientRect().width : 0;
        // Always reserve space for the More button if anything could
        // overflow. Measure the live width when visible and cache it
        // for use when the button is currently hidden (display:none →
        // getBoundingClientRect() returns 0). MORE_BTN_RESERVE_PX is
        // the conservative initial fallback used until we get a real
        // measurement.
        const moreVis = !navMoreWrap.classList.contains('is-hidden');
        const liveMoreW = moreVis ? navMoreWrap.getBoundingClientRect().width : 0;
        if (liveMoreW > 0) cachedMoreW = liveMoreW;
        const moreW = liveMoreW > 0 ? liveMoreW
                    : (cachedMoreW > 0 ? cachedMoreW : MORE_BTN_RESERVE_PX);
        const rightW  = navRightEl.scrollWidth; // intrinsic, ignores clipping
        const needed  = brandW + navLeftGap + linkW + linksGap + navLeftGap + moreW + navLeftGap + rightW + SAFETY;
        return needed <= window.innerWidth;
      }
      let i = 0;
      // #1391: rebuild queue here so it reflects the CURRENT active
      // link (hashchange wakes applyNavPriority, but the queue was
      // captured at init-time; we need to re-evaluate which link is
      // active on every run). Cheap — just filters allLinks twice.
      overflowQueue = buildOverflowQueue();
      // #1311 floor: protect data-priority="high" links from being
      // dropped by the greedy fit loop. The bug was that on a non-high
      // active route (e.g. /#/perf, /#/audio-lab) at ~1101-1200px, the
      // active-route pill renders wider than other links, fits() keeps
      // returning false even after every non-high link is in overflow,
      // and the loop happily walked into the high-priority tail of
      // overflowQueue and dropped Home/Packets/Map/Live/Nodes too —
      // leaving the user with just brand + "More ▾" + the active pill.
      // High-priority links are inline-pinned by contract; if the strip
      // still doesn't fit at that point, that's a layout issue (e.g.
      // shrink the active pill, drop nav-stats earlier) — never the
      // measurer's call to delete primary navigation.
      //
      // #1391: also break on .active — buildOverflowQueue already
      // excludes the active link from the queue, but the break is a
      // defensive belt for any future code that re-enqueues it.
      while (!fits() && i < overflowQueue.length) {
        if (overflowQueue[i].dataset.priority === 'high') break;
        if (overflowQueue[i].classList.contains('active')) break;
        overflowQueue[i].classList.add('is-overflow');
        i++;
      }
      // #1139 Bug B: floor the More menu at >=2 items. The greedy
      // fits() loop above is happy to stop after pushing exactly ONE
      // link into overflow (commonly "🎵 Lab" at ~1600px viewports), // EMOJI-OK: comment referencing prior nav label
      // producing a degenerate single-item dropdown. If exactly one
      // link overflowed, promote one more from the queue so the user
      // sees a useful menu instead of a one-item fragment. Skip when
      // nothing overflowed (everything fits inline → More is hidden,
      // which is the correct UX) and skip when the queue is exhausted.
      var overflowedCount = allLinks.filter(a => a.classList.contains('is-overflow')).length;
      if (overflowedCount === 1) {
        // #1311: respect the high-priority floor here too — if the only
        // remaining queue item is a high-priority link, do NOT promote
        // it just to satisfy the >=2 More-menu floor. A degenerate
        // 1-item dropdown is a smaller UX paper-cut than nuking a
        // primary nav link.
        if (i < overflowQueue.length && overflowQueue[i].dataset.priority !== 'high' && !overflowQueue[i].classList.contains('active')) {
          overflowQueue[i].classList.add('is-overflow');
          i++;
        } else {
          // Defensive: queue exhausted with exactly 1 overflowed link
          // means we cannot satisfy the >=2 floor (only one promotable
          // link existed). Surface it loudly instead of silently
          // shipping the degenerate single-item dropdown the floor
          // was added to prevent.
          console.warn('[nav] More menu floor: overflowQueue exhausted with 1 item; cannot enforce >=2 floor');
        }
      }
      rebuildMoreMenu();
    }

    // Run once on load, again after fonts settle (label widths shift),
    // and on resize (debounced via rAF).
    navPriorityFn = applyNavPriority;
    applyNavPriority();
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(applyNavPriority);
    }
    let rafId = 0;
    window.addEventListener('resize', function() {
      if (rafId) cancelAnimationFrame(rafId);
      rafId = requestAnimationFrame(applyNavPriority);
    });
    // Re-apply on route change too: the active link gets bigger padding
    // (background pill), so which links fit can shift between pages.
    window.addEventListener('hashchange', function() {
      // Defer so the route handler's class toggles run first.
      requestAnimationFrame(applyNavPriority);
    });

    // #1406: position the fixed dropdown relative to the More button on each open.
    // Required because .nav-more-menu is position:fixed (so it escapes
    // .nav-more-wrap's layout box and doesn't inflate the parent flex line).
    function positionMoreMenu() {
      var wr = navMoreWrap.getBoundingClientRect();
      navMoreMenu.style.top = (wr.bottom + 4) + 'px';
      navMoreMenu.style.right = (window.innerWidth - wr.right) + 'px';
      navMoreMenu.style.left = 'auto';
    }
    navMoreBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      const opening = !navMoreMenu.classList.contains('open');
      if (opening) positionMoreMenu();
      navMoreMenu.classList.toggle('open');
      navMoreBtn.setAttribute('aria-expanded', String(opening));
      if (opening) {
        var firstLink = navMoreMenu.querySelector('.nav-link');
        if (firstLink) firstLink.focus();
      }
    });
    // Re-position on window resize while open.
    window.addEventListener('resize', () => {
      if (navMoreMenu.classList.contains('open')) positionMoreMenu();
    });
  }

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      if (navMoreMenu && navMoreMenu.classList.contains('open')) closeMoreMenu();
      if (navLinks.classList.contains('open')) closeNav();
    }
  });
  document.addEventListener('click', (e) => {
    if (navLinks.classList.contains('open') &&
        !navLinks.contains(e.target) &&
        !hamburger.contains(e.target)) {
      closeNav();
    }
    if (navMoreMenu && navMoreMenu.classList.contains('open') &&
        !navMoreMenu.contains(e.target) &&
        !navMoreBtn.contains(e.target)) {
      closeMoreMenu();
    }
  });

  // --- Favorites dropdown ---
  const favToggle = document.getElementById('favToggle');
  const favDropdown = document.getElementById('favDropdown');
  let favOpen = false;

  favToggle.addEventListener('click', (e) => {
    e.stopPropagation();
    favOpen = !favOpen;
    if (favOpen) {
      renderFavDropdown();
      favDropdown.classList.add('open');
    } else {
      favDropdown.classList.remove('open');
    }
  });

  document.addEventListener('click', (e) => {
    if (favOpen && !e.target.closest('.nav-fav-wrap')) {
      favOpen = false;
      favDropdown.classList.remove('open');
    }
  });

  async function renderFavDropdown() {
    const favs = getFavorites();
    if (!favs.length) {
      favDropdown.innerHTML = '<div class="fav-dd-empty">No favorites yet.<br><small>Click the star on any node to add it.</small></div>';
      return;
    }
    favDropdown.innerHTML = '<div class="fav-dd-loading">Loading...</div>';
    const items = await Promise.all(favs.map(async (pk) => {
      try {
        const h = await api('/nodes/' + pk + '/health', { ttl: CLIENT_TTL.nodeHealth });
        const age = h.stats.lastHeard ? Date.now() - new Date(h.stats.lastHeard).getTime() : null;
        const statusCls = age === null ? 'status-err' : age < HEALTH_THRESHOLDS.nodeDegradedMs ? 'status-ok' : age < HEALTH_THRESHOLDS.nodeSilentMs ? 'status-warn' : 'status-err';
        const statusLabel = age === null ? 'unknown' : age < HEALTH_THRESHOLDS.nodeDegradedMs ? 'healthy' : age < HEALTH_THRESHOLDS.nodeSilentMs ? 'degraded' : 'silent';
        return '<a href="#/nodes/' + pk + '" class="fav-dd-item" data-key="' + pk + '">'
          + '<span class="fav-dd-status ' + statusCls + '" title="' + statusLabel + '" aria-label="' + statusLabel + '"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span>'
          + '<span class="fav-dd-name">' + (h.node.name || truncate(pk, 12)) + '</span>'
          + '<span class="fav-dd-meta">' + (h.stats.lastHeard ? timeAgo(h.stats.lastHeard) : 'never') + '</span>'
          + favStar(pk, 'fav-dd-star')
          + '</a>';
      } catch {
        return '<a href="#/nodes/' + pk + '" class="fav-dd-item" data-key="' + pk + '">'
          + '<span class="fav-dd-status status-muted" title="not found" aria-label="not found"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg></span>'
          + '<span class="fav-dd-name">' + truncate(pk, 16) + '</span>'
          + '<span class="fav-dd-meta">not found</span>'
          + favStar(pk, 'fav-dd-star')
          + '</a>';
      }
    }));
    // Footer action, separated from the favorites list by a top border. It is
    // deliberately not a favorite row (no star), so it carries no data-key the
    // star handler would act on.
    favDropdown.innerHTML = items.join('')
      + '<a href="#/analytics?tab=my-repeaters" class="fav-dd-item" style="border-top:1px solid var(--border)">'
      + '<span class="fav-dd-status"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-line"/></svg></span>'
      + '<span class="fav-dd-name">Monitor my repeaters</span>'
      + '<span class="fav-dd-meta"></span>'
      + '</a>';
    bindFavStars(favDropdown, () => renderFavDropdown());
    // Close dropdown on link click
    favDropdown.querySelectorAll('.fav-dd-item').forEach(a => {
      a.addEventListener('click', (e) => {
        if (e.target.closest('.fav-star')) { e.preventDefault(); return; }
        favOpen = false;
        favDropdown.classList.remove('open');
      });
    });
  }

  // --- Search ---
  const searchToggle = document.getElementById('searchToggle');
  const searchOverlay = document.getElementById('searchOverlay');
  const searchInput = document.getElementById('searchInput');
  const searchResults = document.getElementById('searchResults');
  let searchTimeout = null;

  searchToggle.addEventListener('click', () => {
    searchOverlay.classList.toggle('hidden');
    if (!searchOverlay.classList.contains('hidden')) {
      searchInput.value = '';
      searchResults.innerHTML = '';
      searchInput.focus();
    }
  });
  searchOverlay.addEventListener('click', (e) => {
    if (e.target === searchOverlay) searchOverlay.classList.add('hidden');
  });
  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
      e.preventDefault();
      searchOverlay.classList.remove('hidden');
      searchInput.value = '';
      searchResults.innerHTML = '';
      searchInput.focus();
    }
    if (e.key === 'Escape') searchOverlay.classList.add('hidden');
  });

  searchInput.addEventListener('input', () => {
    clearTimeout(searchTimeout);
    const q = searchInput.value.trim();
    if (!q) { searchResults.innerHTML = ''; return; }
    searchTimeout = setTimeout(async () => {
      try {
        const [packets, nodes, channels] = await Promise.all([
          fetch('/api/packets?limit=5&hash=' + encodeURIComponent(q)).then(r => r.json()).catch(() => ({ packets: [] })),
          fetch('/api/nodes?search=' + encodeURIComponent(q)).then(r => r.json()).catch(() => []),
          fetch('/api/channels').then(r => r.json()).catch(() => [])
        ]);
        let html = '';
        const pktList = packets.packets || packets;
        if (Array.isArray(pktList)) {
          for (const p of pktList.slice(0, 5)) {
            html += `<div class="search-result-item" tabindex="0" role="option" data-href="#/packets/${p.packet_hash || p.hash || p.id}">
              <span class="search-result-type">Packet</span>${truncate(p.packet_hash || '', 16)} — ${payloadTypeName(p.payload_type)}</div>`;
          }
        }
        const nodeList = Array.isArray(nodes) ? nodes : (nodes.nodes || []);
        for (const n of nodeList.slice(0, 5)) {
          if (n.name && n.name.toLowerCase().includes(q.toLowerCase())) {
            html += `<div class="search-result-item" tabindex="0" role="option" data-href="#/nodes/${n.public_key}">
              <span class="search-result-type">Node</span>${escapeHtml(n.name)} — ${truncate(n.public_key || '', 16)}</div>`;
          }
        }
        const chList = Array.isArray(channels) ? channels : [];
        for (const c of chList) {
          if (c.name && c.name.toLowerCase().includes(q.toLowerCase())) {
            html += `<div class="search-result-item" tabindex="0" role="option" data-href="#/channels/${c.channel_hash}">
              <span class="search-result-type">Channel</span>${escapeHtml(c.name)}</div>`;
          }
        }
        if (!html) html = '<div class="search-no-results">No results found</div>';
        searchResults.innerHTML = html;
      } catch { searchResults.innerHTML = '<div class="search-no-results">Search error</div>'; }
    }, 300);
  });

  // #208 — Search results keyboard: click, Enter/Space, arrow-key navigation
  function activateSearchItem(item) {
    if (!item || !item.dataset.href) return;
    location.hash = item.dataset.href;
    searchOverlay.classList.add('hidden');
  }
  searchResults.addEventListener('click', (e) => {
    activateSearchItem(e.target.closest('.search-result-item'));
  });
  searchResults.addEventListener('keydown', (e) => {
    const item = e.target.closest('.search-result-item');
    if (!item) return;
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      activateSearchItem(item);
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      const next = item.nextElementSibling;
      if (next && next.classList.contains('search-result-item')) next.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      const prev = item.previousElementSibling;
      if (prev && prev.classList.contains('search-result-item')) prev.focus();
      else searchInput.focus();
    }
  });
  searchInput.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      const first = searchResults.querySelector('.search-result-item');
      if (first) first.focus();
    }
  });

  // --- Login ---
  // (removed — no auth yet)

  // --- Nav Stats ---
  async function updateNavStats() {
    try {
      const stats = await api('/stats', { ttl: CLIENT_TTL.stats });
      const el = document.getElementById('navStats');
      if (el) {
        el.innerHTML = `<span class="stat-val">${stats.totalPackets}</span> pkts · <span class="stat-val">${stats.totalNodes}</span> nodes · <span class="stat-val">${stats.totalObservers}</span> obs`;
        el.querySelectorAll('.stat-val').forEach(s => s.classList.add('updated'));
        setTimeout(() => { el.querySelectorAll('.stat-val').forEach(s => s.classList.remove('updated')); }, 600);
        if (navPriorityFn) requestAnimationFrame(navPriorityFn);
      }
    } catch {}
  }
  updateNavStats();
  setInterval(updateNavStats, 15000);
  debouncedOnWS(function () { updateNavStats(); });

  // --- Theme Customization ---
  // Fetch theme config and apply via customizer v2 pipeline
  fetch('/api/config/theme', { cache: 'no-store' }).then(r => r.json()).then(cfg => {
    // Normalize timestamp defaults
    cfg = cfg || {};
    if (!cfg.timestamps) cfg.timestamps = {};
    const tsCfg = cfg.timestamps;
    if (tsCfg.defaultMode !== 'absolute' && tsCfg.defaultMode !== 'ago') tsCfg.defaultMode = 'ago';
    if (tsCfg.timezone !== 'utc' && tsCfg.timezone !== 'local') tsCfg.timezone = 'local';
    if (tsCfg.formatPreset !== 'iso' && tsCfg.formatPreset !== 'iso-seconds' && tsCfg.formatPreset !== 'locale') tsCfg.formatPreset = 'iso';
    if (typeof tsCfg.customFormat !== 'string') tsCfg.customFormat = '';
    tsCfg.allowCustomFormat = tsCfg.allowCustomFormat === true;

    // Customizer v2: set server defaults and run full pipeline
    // (reads localStorage overrides → merges → sets SITE_CONFIG → applies CSS → dispatches theme-changed)
    if (window._customizerV2) {
      window._customizerV2.init(cfg);
    } else {
      // Fallback if customize-v2.js didn't load
      window.SITE_CONFIG = cfg;
    }
  }).catch(() => {
    window.SITE_CONFIG = { timestamps: { defaultMode: 'ago', timezone: 'local', formatPreset: 'iso', customFormat: '', allowCustomFormat: false } };
    if (window._customizerV2) window._customizerV2.init(window.SITE_CONFIG);
  });

  // Navigate immediately — don't gate data-fetching pages on cosmetic theme fetch
  if (!location.hash || location.hash === '#/') location.hash = '#/home';
  else navigate();
});

/**
 * Reusable ARIA tab-bar initialiser.
 * Adds role="tablist" to container, role="tab" + aria-selected to each button,
 * and arrow-key navigation between tabs.
 * @param {HTMLElement} container - the tab bar element
 * @param {Function} [onChange] - optional callback(activeBtn) on tab change
 */
function initTabBar(container, onChange) {
  if (!container || container.getAttribute('role') === 'tablist') return;
  container.setAttribute('role', 'tablist');
  const tabs = Array.from(container.querySelectorAll('button, [data-tab], [data-obs]'));
  tabs.forEach(btn => {
    btn.setAttribute('role', 'tab');
    const isActive = btn.classList.contains('active');
    btn.setAttribute('aria-selected', String(isActive));
    btn.setAttribute('tabindex', isActive ? '0' : '-1');
    // Link to panel if aria-controls target exists
    const panelId = btn.dataset.tab || btn.dataset.obs;
    if (panelId && document.getElementById(panelId)) {
      btn.setAttribute('aria-controls', panelId);
    }
  });
  container.addEventListener('click', (e) => {
    const btn = e.target.closest('[role="tab"]');
    if (!btn || !container.contains(btn)) return;
    tabs.forEach(b => { b.setAttribute('aria-selected', 'false'); b.setAttribute('tabindex', '-1'); });
    btn.setAttribute('aria-selected', 'true');
    btn.setAttribute('tabindex', '0');
    if (onChange) onChange(btn);
  });
  container.addEventListener('keydown', (e) => {
    const btn = e.target.closest('[role="tab"]');
    if (!btn) return;
    let idx = tabs.indexOf(btn), next = -1;
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = (idx + 1) % tabs.length;
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') next = (idx - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = tabs.length - 1;
    if (next < 0) return;
    e.preventDefault();
    tabs.forEach(b => { b.setAttribute('aria-selected', 'false'); b.setAttribute('tabindex', '-1'); });
    tabs[next].setAttribute('aria-selected', 'true');
    tabs[next].setAttribute('tabindex', '0');
    tabs[next].focus();
    tabs[next].click();
  });
}

/**
 * Make table columns resizable with drag handles. Widths saved to localStorage.
 * Call after table is in DOM. Re-call safe (idempotent per table).
 * @param {string} tableSelector - CSS selector for the table
 * @param {string} storageKey - localStorage key for persisted widths
 */
function makeColumnsResizable(tableSelector, storageKey) {
  const table = document.querySelector(tableSelector);
  if (!table) return;
  const thead = table.querySelector('thead');
  if (!thead) return;
  const ths = Array.from(thead.querySelectorAll('tr:first-child th'));
  if (ths.length < 2) return;

  if (table.dataset.resizable) return;
  table.dataset.resizable = '1';
  table.style.tableLayout = 'fixed';

  const containerW = table.parentElement.clientWidth;
  const saved = localStorage.getItem(storageKey);
  let widths;

  if (saved) {
    try { widths = JSON.parse(saved); } catch { widths = null; }
    // Validate: must be array of correct length with values summing to ~100 (percentages)
    if (widths && Array.isArray(widths) && widths.length === ths.length) {
      const sum = widths.reduce((s, w) => s + w, 0);
      if (sum > 90 && sum < 110) {
        // Saved percentages — apply directly
        table.style.tableLayout = 'fixed';
        table.style.width = '100%';
        ths.forEach((th, i) => { th.style.width = widths[i] + '%'; });
        // Skip measurement, jump to adding handles
        addResizeHandles();
        return;
      }
    }
    widths = null; // Force remeasure
  }

  if (!widths) {
    // Measure actual max content width per column by scanning visible rows
    const tbody = table.querySelector('tbody');
    const rows = tbody ? Array.from(tbody.querySelectorAll('tr')).slice(0, 30) : [];

    // Temporarily set auto layout to measure
    table.style.tableLayout = 'auto';
    table.style.width = 'auto';
    // Remove nowrap temporarily so we get true content width
    const cells = table.querySelectorAll('td, th');
    cells.forEach(c => { c.dataset.origWs = c.style.whiteSpace || ''; c.style.whiteSpace = 'nowrap'; });

    // Measure each column's max content width across header + rows
    widths = ths.map((th, i) => {
      let maxW = th.scrollWidth;
      rows.forEach(row => {
        const td = row.children[i];
        if (td) maxW = Math.max(maxW, td.scrollWidth);
      });
      return maxW + 4; // small padding buffer
    });

    cells.forEach(c => { c.style.whiteSpace = c.dataset.origWs || ''; delete c.dataset.origWs; });
  }

  // Now fit to container: if total > container, squish widest first
  const totalNeeded = widths.reduce((s, w) => s + w, 0);
  const finalWidths = [...widths];

  if (totalNeeded > containerW) {
    let excess = totalNeeded - containerW;
    const MIN_COL = 28;
    // Iteratively shave from widest columns
    while (excess > 0) {
      // Find current max width
      const maxW = Math.max(...finalWidths);
      if (maxW <= MIN_COL) break;
      // Find second-max to know our target
      const sorted = [...new Set(finalWidths)].sort((a, b) => b - a);
      const target = sorted.length > 1 ? Math.max(sorted[1], MIN_COL) : MIN_COL;
      // How many columns are at maxW?
      const atMax = finalWidths.filter(w => w >= maxW).length;
      const canShavePerCol = maxW - target;
      const neededPerCol = Math.ceil(excess / atMax);
      const shavePerCol = Math.min(canShavePerCol, neededPerCol);

      for (let i = 0; i < finalWidths.length; i++) {
        if (finalWidths[i] >= maxW) {
          const shave = Math.min(shavePerCol, excess);
          finalWidths[i] -= shave;
          excess -= shave;
          if (excess <= 0) break;
        }
      }
    }
  } else if (totalNeeded < containerW) {
    // Give surplus to the 2 widest columns (content-heavy ones)
    const surplus = containerW - totalNeeded;
    const indexed = finalWidths.map((w, i) => ({ w, i })).sort((a, b) => b.w - a.w);
    const topN = indexed.slice(0, Math.min(2, indexed.length));
    const topTotal = topN.reduce((s, x) => s + x.w, 0);
    topN.forEach(x => { finalWidths[x.i] += Math.round(surplus * (x.w / topTotal)); });
  }

  table.style.width = '100%';
  const totalFinal = finalWidths.reduce((s, w) => s + w, 0);
  ths.forEach((th, i) => { th.style.width = (finalWidths[i] / totalFinal * 100) + '%'; });

  addResizeHandles();

  function addResizeHandles() {
  // Add resize handles
  ths.forEach((th, i) => {
    if (i === ths.length - 1) return;
    const handle = document.createElement('div');
    handle.className = 'col-resize-handle';
    handle.addEventListener('mousedown', (e) => {
      e.preventDefault();
      e.stopPropagation();
      const startX = e.clientX;
      const startW = th.offsetWidth;
      const startTableW = table.offsetWidth;
      handle.classList.add('active');
      document.body.style.cursor = 'col-resize';
      document.body.style.userSelect = 'none';

      function onMove(e2) {
        const dx = e2.clientX - startX;
        const newW = Math.max(50, startW + dx);
        const delta = newW - th.offsetWidth;
        if (delta === 0) return;
        // Steal/give space from columns to the right, proportionally
        const rightThs = ths.slice(i + 1);
        const rightWidths = rightThs.map(t => t.offsetWidth);
        const rightTotal = rightWidths.reduce((s, w) => s + w, 0);
        if (rightTotal - delta < rightThs.length * 50) return; // can't squeeze below 50px each
        th.style.width = newW + 'px';
        const scale = (rightTotal - delta) / rightTotal;
        rightThs.forEach(t => { t.style.width = Math.max(50, t.offsetWidth * scale) + 'px'; });
      }
      function onUp() {
        handle.classList.remove('active');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        // Save as percentages
        const tableW = table.offsetWidth;
        const ws = ths.map(t => (t.offsetWidth / tableW * 100));
        localStorage.setItem(storageKey, JSON.stringify(ws));
        // Re-apply as percentages
        ths.forEach((t, j) => { t.style.width = ws[j] + '%'; });
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', onUp);
      }
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });
    th.appendChild(handle);
  });
  } // end addResizeHandles
}
