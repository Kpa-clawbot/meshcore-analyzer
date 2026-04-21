/* === CoreScope — roles.js (shared config module) === */
'use strict';

/*
 * Centralized roles, thresholds, tile URLs, and UI constants.
 * Loaded BEFORE all page scripts via index.html.
 * Defaults are set synchronously; server config overrides arrive via fetch.
 */

(function () {
  // ─── Role definitions ───
  window.ROLE_COLORS = {
    repeater: '#dc2626', companion: '#2563eb', room: '#16a34a',
    sensor: '#d97706', observer: '#8b5cf6', unknown: '#6b7280'
  };

  window.TYPE_COLORS = {
    ADVERT: '#22c55e', GRP_TXT: '#3b82f6', TXT_MSG: '#f59e0b', ACK: '#6b7280',
    REQUEST: '#a855f7', RESPONSE: '#06b6d4', TRACE: '#ec4899', PATH: '#14b8a6',
    ANON_REQ: '#f43f5e', UNKNOWN: '#6b7280'
  };

  // Badge CSS class name mapping
  const TYPE_BADGE_MAP = {
    ADVERT: 'advert', GRP_TXT: 'grp-txt', TXT_MSG: 'txt-msg', ACK: 'ack',
    REQUEST: 'req', RESPONSE: 'response', TRACE: 'trace', PATH: 'path',
    ANON_REQ: 'anon-req', UNKNOWN: 'unknown'
  };

  // Generate badge CSS from TYPE_COLORS — single source of truth
  window.syncBadgeColors = function() {
    var el = document.getElementById('type-color-badges');
    if (!el) { el = document.createElement('style'); el.id = 'type-color-badges'; document.head.appendChild(el); }
    var css = '';
    for (var type in TYPE_BADGE_MAP) {
      var color = window.TYPE_COLORS[type];
      if (!color) continue;
      var cls = TYPE_BADGE_MAP[type];
      css += '.badge-' + cls + ' { background: ' + color + '20; color: ' + color + '; }\n';
    }
    el.textContent = css;
  };

  // Auto-sync on load
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', window.syncBadgeColors);
  } else {
    window.syncBadgeColors();
  }

  window.ROLE_LABELS = {
    repeater: 'Repeaters', companion: 'Companions', room: 'Room Servers',
    sensor: 'Sensors', observer: 'Observers'
  };

  window.ROLE_STYLE = {
    repeater:  { color: '#dc2626', shape: 'diamond',  radius: 10, weight: 2 },
    companion: { color: '#2563eb', shape: 'circle',   radius: 8,  weight: 2 },
    room:      { color: '#16a34a', shape: 'square',   radius: 9,  weight: 2 },
    sensor:    { color: '#d97706', shape: 'triangle', radius: 8,  weight: 2 },
    observer:  { color: '#8b5cf6', shape: 'star',     radius: 11, weight: 2 }
  };

  window.ROLE_EMOJI = {
    repeater: '◆', companion: '●', room: '■', sensor: '▲', observer: '★'
  };

  window.ROLE_SORT = ['repeater', 'companion', 'room', 'sensor', 'observer'];

  // ─── Health thresholds (ms) ───
  window.HEALTH_THRESHOLDS = {
    infraDegradedMs: 86400000,   // 24h
    infraSilentMs:   259200000,  // 72h
    nodeDegradedMs:  3600000,    // 1h
    nodeSilentMs:    86400000    // 24h
  };

  // Helper: get degraded/silent thresholds for a role (backward compat)
  window.getHealthThresholds = function (role) {
    var isInfra = role === 'repeater' || role === 'room';
    return {
      degradedMs: isInfra ? HEALTH_THRESHOLDS.infraDegradedMs : HEALTH_THRESHOLDS.nodeDegradedMs,
      silentMs:   isInfra ? HEALTH_THRESHOLDS.infraSilentMs   : HEALTH_THRESHOLDS.nodeSilentMs
    };
  };

  // Simplified two-state helper: returns 'active' or 'stale'
  window.getNodeStatus = function (role, lastSeenMs) {
    var isInfra = role === 'repeater' || role === 'room';
    var staleMs = isInfra ? HEALTH_THRESHOLDS.infraSilentMs : HEALTH_THRESHOLDS.nodeSilentMs;
    var age = typeof lastSeenMs === 'number' ? (Date.now() - lastSeenMs) : Infinity;
    return age < staleMs ? 'active' : 'stale';
  };

  // ─── Tile URLs ───
  window.TILE_DARK  = 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png';
  window.TILE_LIGHT = 'https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png';

  window.getTileUrl = function () {
    var isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' &&
       window.matchMedia('(prefers-color-scheme: dark)').matches);
    return isDark ? TILE_DARK : TILE_LIGHT;
  };

  // ─── SNR thresholds ───
  window.SNR_THRESHOLDS = { excellent: 6, good: 0 };

  // ─── Distance thresholds (km) ───
  window.DIST_THRESHOLDS = { local: 50, regional: 200 };

  // ─── MAX_HOP_DIST (degrees, ~200km ≈ 1.8°) ───
  window.MAX_HOP_DIST = 1.8;

  // ─── Result limits ───
  window.LIMITS = {
    topNodes: 15,
    topPairs: 12,
    topRingNodes: 8,
    topSenders: 10,
    topCollisionNodes: 10,
    recentReplay: 8,
    feedMax: 25
  };

  // ─── Performance thresholds ───
  window.PERF_SLOW_MS = 100;

  // ─── WebSocket reconnect delay (ms) ───
  window.WS_RECONNECT_MS = 3000;

  // ─── Propagation buffer (ms) for realistic mode ───
  window.PROPAGATION_BUFFER_MS = 5000;

  // ─── Cache invalidation debounce (ms) ───
  window.CACHE_INVALIDATE_MS = 5000;

  // ─── External URLs ───
  window.EXTERNAL_URLS = {
    flasher: 'https://flasher.meshcore.co.uk/'
  };

  // ─── Fetch server overrides ───
  window.MeshConfigReady = fetch('/api/config/client').then(function (r) { return r.json(); }).then(function (cfg) {
    if (cfg.roles) {
      if (cfg.roles.colors) Object.assign(ROLE_COLORS, cfg.roles.colors);
      if (cfg.roles.labels) Object.assign(ROLE_LABELS, cfg.roles.labels);
      if (cfg.roles.style) {
        for (var k in cfg.roles.style) ROLE_STYLE[k] = Object.assign(ROLE_STYLE[k] || {}, cfg.roles.style[k]);
      }
      if (cfg.roles.emoji) Object.assign(ROLE_EMOJI, cfg.roles.emoji);
      if (cfg.roles.sort) window.ROLE_SORT = cfg.roles.sort;
    }
    if (cfg.healthThresholds) Object.assign(HEALTH_THRESHOLDS, cfg.healthThresholds);
    if (cfg.tiles) {
      if (cfg.tiles.dark) window.TILE_DARK = cfg.tiles.dark;
      if (cfg.tiles.light) window.TILE_LIGHT = cfg.tiles.light;
    }
    if (cfg.snrThresholds) Object.assign(SNR_THRESHOLDS, cfg.snrThresholds);
    if (cfg.distThresholds) Object.assign(DIST_THRESHOLDS, cfg.distThresholds);
    if (cfg.maxHopDist != null) window.MAX_HOP_DIST = cfg.maxHopDist;
    if (cfg.limits) Object.assign(LIMITS, cfg.limits);
    if (cfg.perfSlowMs != null) window.PERF_SLOW_MS = cfg.perfSlowMs;
    if (cfg.wsReconnectMs != null) window.WS_RECONNECT_MS = cfg.wsReconnectMs;
    if (cfg.cacheInvalidateMs != null) window.CACHE_INVALIDATE_MS = cfg.cacheInvalidateMs;
    if (cfg.externalUrls) Object.assign(EXTERNAL_URLS, cfg.externalUrls);
    if (cfg.propagationBufferMs != null) window.PROPAGATION_BUFFER_MS = cfg.propagationBufferMs;
    // Sync ROLE_STYLE colors with ROLE_COLORS
    for (var role in ROLE_STYLE) {
      if (ROLE_COLORS[role]) ROLE_STYLE[role].color = ROLE_COLORS[role];
    }
  }).catch(function () { /* use defaults */ });

  // ─── Built-in IATA airport code → city name mapping ───
  window.IATA_CITIES = {
    // United States
    'SEA': 'Seattle, WA',
    'SFO': 'San Francisco, CA',
    'PDX': 'Portland, OR',
    'LAX': 'Los Angeles, CA',
    'DEN': 'Denver, CO',
    'SLC': 'Salt Lake City, UT',
    'PHX': 'Phoenix, AZ',
    'DFW': 'Dallas, TX',
    'ATL': 'Atlanta, GA',
    'ORD': 'Chicago, IL',
    'JFK': 'New York, NY',
    'LGA': 'New York, NY',
    'BOS': 'Boston, MA',
    'MIA': 'Miami, FL',
    'FLL': 'Fort Lauderdale, FL',
    'IAH': 'Houston, TX',
    'HOU': 'Houston, TX',
    'MSP': 'Minneapolis, MN',
    'DTW': 'Detroit, MI',
    'CLT': 'Charlotte, NC',
    'EWR': 'Newark, NJ',
    'IAD': 'Washington, DC',
    'DCA': 'Washington, DC',
    'BWI': 'Baltimore, MD',
    'LAS': 'Las Vegas, NV',
    'MCO': 'Orlando, FL',
    'TPA': 'Tampa, FL',
    'BNA': 'Nashville, TN',
    'AUS': 'Austin, TX',
    'SAT': 'San Antonio, TX',
    'RDU': 'Raleigh, NC',
    'SAN': 'San Diego, CA',
    'OAK': 'Oakland, CA',
    'SJC': 'San Jose, CA',
    'SMF': 'Sacramento, CA',
    'PHL': 'Philadelphia, PA',
    'PIT': 'Pittsburgh, PA',
    'CLE': 'Cleveland, OH',
    'CMH': 'Columbus, OH',
    'CVG': 'Cincinnati, OH',
    'IND': 'Indianapolis, IN',
    'MCI': 'Kansas City, MO',
    'STL': 'St. Louis, MO',
    'MSY': 'New Orleans, LA',
    'MEM': 'Memphis, TN',
    'SDF': 'Louisville, KY',
    'JAX': 'Jacksonville, FL',
    'RIC': 'Richmond, VA',
    'ORF': 'Norfolk, VA',
    'BDL': 'Hartford, CT',
    'PVD': 'Providence, RI',
    'ABQ': 'Albuquerque, NM',
    'OKC': 'Oklahoma City, OK',
    'TUL': 'Tulsa, OK',
    'OMA': 'Omaha, NE',
    'BOI': 'Boise, ID',
    'GEG': 'Spokane, WA',
    'ANC': 'Anchorage, AK',
    'HNL': 'Honolulu, HI',
    'OGG': 'Maui, HI',
    'BUF': 'Buffalo, NY',
    'SYR': 'Syracuse, NY',
    'ROC': 'Rochester, NY',
    'ALB': 'Albany, NY',
    'BTV': 'Burlington, VT',
    'PWM': 'Portland, ME',
    'MKE': 'Milwaukee, WI',
    'DSM': 'Des Moines, IA',
    'LIT': 'Little Rock, AR',
    'BHM': 'Birmingham, AL',
    'CHS': 'Charleston, SC',
    'SAV': 'Savannah, GA',
    // Canada
    'YVR': 'Vancouver, BC',
    'YYZ': 'Toronto, ON',
    'YUL': 'Montreal, QC',
    'YOW': 'Ottawa, ON',
    'YYC': 'Calgary, AB',
    'YEG': 'Edmonton, AB',
    'YWG': 'Winnipeg, MB',
    'YHZ': 'Halifax, NS',
    'YQB': 'Quebec City, QC',
    // Europe
    'LHR': 'London, UK',
    'LGW': 'London, UK',
    'STN': 'London, UK',
    'CDG': 'Paris, FR',
    'ORY': 'Paris, FR',
    'FRA': 'Frankfurt, DE',
    'MUC': 'Munich, DE',
    'BER': 'Berlin, DE',
    'AMS': 'Amsterdam, NL',
    'MAD': 'Madrid, ES',
    'BCN': 'Barcelona, ES',
    'FCO': 'Rome, IT',
    'MXP': 'Milan, IT',
    'ZRH': 'Zurich, CH',
    'GVA': 'Geneva, CH',
    'VIE': 'Vienna, AT',
    'CPH': 'Copenhagen, DK',
    'ARN': 'Stockholm, SE',
    'OSL': 'Oslo, NO',
    'HEL': 'Helsinki, FI',
    'DUB': 'Dublin, IE',
    'LIS': 'Lisbon, PT',
    'ATH': 'Athens, GR',
    'IST': 'Istanbul, TR',
    'WAW': 'Warsaw, PL',
    'PRG': 'Prague, CZ',
    'BUD': 'Budapest, HU',
    'OTP': 'Bucharest, RO',
    'SOF': 'Sofia, BG',
    'ZAG': 'Zagreb, HR',
    'BEG': 'Belgrade, RS',
    'KBP': 'Kyiv, UA',
    'LED': 'St. Petersburg, RU',
    'SVO': 'Moscow, RU',
    'BRU': 'Brussels, BE',
    'EDI': 'Edinburgh, UK',
    'MAN': 'Manchester, UK',
    // Asia
    'NRT': 'Tokyo, JP',
    'HND': 'Tokyo, JP',
    'KIX': 'Osaka, JP',
    'ICN': 'Seoul, KR',
    'PEK': 'Beijing, CN',
    'PVG': 'Shanghai, CN',
    'HKG': 'Hong Kong',
    'TPE': 'Taipei, TW',
    'SIN': 'Singapore',
    'BKK': 'Bangkok, TH',
    'KUL': 'Kuala Lumpur, MY',
    'CGK': 'Jakarta, ID',
    'MNL': 'Manila, PH',
    'DEL': 'New Delhi, IN',
    'BOM': 'Mumbai, IN',
    'BLR': 'Bangalore, IN',
    'CCU': 'Kolkata, IN',
    'SGN': 'Ho Chi Minh City, VN',
    'HAN': 'Hanoi, VN',
    'DOH': 'Doha, QA',
    'DXB': 'Dubai, AE',
    'AUH': 'Abu Dhabi, AE',
    'TLV': 'Tel Aviv, IL',
    // Oceania
    'SYD': 'Sydney, AU',
    'MEL': 'Melbourne, AU',
    'BNE': 'Brisbane, AU',
    'PER': 'Perth, AU',
    'AKL': 'Auckland, NZ',
    'WLG': 'Wellington, NZ',
    'CHC': 'Christchurch, NZ',
    // South America
    'GRU': 'São Paulo, BR',
    'GIG': 'Rio de Janeiro, BR',
    'EZE': 'Buenos Aires, AR',
    'SCL': 'Santiago, CL',
    'BOG': 'Bogota, CO',
    'LIM': 'Lima, PE',
    'UIO': 'Quito, EC',
    'CCS': 'Caracas, VE',
    'MVD': 'Montevideo, UY',
    // Africa
    'JNB': 'Johannesburg, ZA',
    'CPT': 'Cape Town, ZA',
    'CAI': 'Cairo, EG',
    'NBO': 'Nairobi, KE',
    'ADD': 'Addis Ababa, ET',
    'CMN': 'Casablanca, MA',
    'LOS': 'Lagos, NG'
  };

  // Copy text to clipboard with fallback for Firefox and older browsers
  window.copyToClipboard = function(text, onSuccess, onFail) {
    function fallback() {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.left = '-9999px';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      try {
        var ok = document.execCommand('copy');
        document.body.removeChild(ta);
        if (ok && onSuccess) onSuccess();
        else if (!ok && onFail) onFail();
      } catch (e) {
        document.body.removeChild(ta);
        if (onFail) onFail();
      }
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(
        function() { if (onSuccess) onSuccess(); },
        function() { fallback(); }
      );
    } else {
      fallback();
    }
  };

  // Simple markdown → HTML (bold, italic, links, code, lists, line breaks)
  window.miniMarkdown = function(text) {
    if (!text) return '';
    var html = text
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
      .replace(/\*(.+?)\*/g, '<em>$1</em>')
      .replace(/`(.+?)`/g, '<code>$1</code>')
      .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener" style="color:var(--accent)">$1</a>')
      .replace(/^- (.+)/gm, '<li>$1</li>')
      .replace(/\n/g, '<br>');
    // Wrap consecutive <li> in <ul>
    html = html.replace(/((?:<li>.*?<\/li><br>?)+)/g, function(m) {
      return '<ul>' + m.replace(/<br>/g, '') + '</ul>';
    });
    return html;
  };

  // #690 — Clock Skew shared helpers
  var SKEW_SEVERITY_COLORS = {
    ok: 'var(--status-green)',
    warning: 'var(--status-yellow)',
    critical: 'var(--status-orange)',
    absurd: 'var(--status-purple)',
    no_clock: 'var(--text-muted)'
  };
  var SKEW_SEVERITY_LABELS = {
    ok: 'OK', warning: 'Warning', critical: 'Critical', absurd: 'Absurd', no_clock: 'No Clock'
  };
  var SKEW_SEVERITY_ORDER = { no_clock: 0, absurd: 1, critical: 2, warning: 3, ok: 4 };

  window.SKEW_SEVERITY_COLORS = SKEW_SEVERITY_COLORS;
  window.SKEW_SEVERITY_LABELS = SKEW_SEVERITY_LABELS;
  window.SKEW_SEVERITY_ORDER = SKEW_SEVERITY_ORDER;

  /** Format skew seconds into human-readable string like "+2m 34s" or "-15h 22m" */
  window.formatSkew = function(sec) {
    if (sec == null) return '—';
    var abs = Math.abs(sec);
    var sign = sec >= 0 ? '+' : '-';
    if (abs < 60) return sign + Math.round(abs) + 's';
    if (abs < 3600) return sign + Math.floor(abs / 60) + 'm ' + Math.round(abs % 60) + 's';
    if (abs < 86400) return sign + Math.floor(abs / 3600) + 'h ' + Math.round((abs % 3600) / 60) + 'm';
    return sign + Math.floor(abs / 86400) + 'd ' + Math.round((abs % 86400) / 3600) + 'h';
  };

  /** Format drift rate as "+X.Xs/day" or "—" if falsy */
  window.formatDrift = function(secPerDay) {
    if (!secPerDay) return '—';
    return (secPerDay >= 0 ? '+' : '') + secPerDay.toFixed(1) + ' s/day';
  };

  /** Pick the skew value that drives current-health UI: prefer the
   *  recent-window median (#789, current health) over the all-time median
   *  (poisoned by historical bad samples). Falls back gracefully if the
   *  field isn't present (older API responses). */
  window.currentSkewValue = function(cs) {
    if (!cs) return null;
    return cs.recentMedianSkewSec != null ? cs.recentMedianSkewSec : cs.medianSkewSec;
  };

  /** Render a clock skew badge HTML */
  window.renderSkewBadge = function(severity, skewSec) {
    if (!severity) return '';
    var cls = 'skew-badge skew-badge--' + severity;
    if (severity === 'no_clock') {
      return '<span class="' + cls + '" title="Uninitialized RTC — no valid clock">🚫 No Clock</span>';
    }
    var label = severity === 'ok' ? '⏰' : '⏰ ' + window.formatSkew(skewSec);
    return '<span class="' + cls + '" title="Clock skew: ' + window.formatSkew(skewSec) + ' (' + (SKEW_SEVERITY_LABELS[severity] || severity) + ')">' + label + '</span>';
  };

  /** Render a skew sparkline SVG (inline, word-sized) */
  window.renderSkewSparkline = function(samples, w, h) {
    w = w || 120; h = h || 24;
    if (!samples || samples.length < 2) return '';
    var values = samples.map(function(s) { return s.skew; });
    var max = Math.max.apply(null, values.map(function(v) { return Math.abs(v); }).concat([1]));
    var pts = values.map(function(v, i) {
      var x = i * (w / Math.max(values.length - 1, 1));
      var y = h / 2 - (v / max) * (h / 2 - 2);
      return x.toFixed(1) + ',' + y.toFixed(1);
    }).join(' ');
    // Zero line
    var zeroY = h / 2;
    return '<svg viewBox="0 0 ' + w + ' ' + h + '" style="width:' + w + 'px;height:' + h + 'px" role="img" aria-label="Clock skew sparkline">' +
      '<title>Clock skew over time</title>' +
      '<line x1="0" y1="' + zeroY + '" x2="' + w + '" y2="' + zeroY + '" stroke="var(--border)" stroke-width="0.5" stroke-dasharray="2"/>' +
      '<polyline points="' + pts + '" fill="none" stroke="var(--accent)" stroke-width="1.5"/></svg>';
  };
})();
