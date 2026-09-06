/* === CoreScope — roles.js (shared config module) === */
'use strict';

/*
 * Centralized roles, thresholds, tile URLs, and UI constants.
 * Loaded BEFORE all page scripts via index.html.
 * Defaults are set synchronously; server config overrides arrive via fetch.
 */

(function () {
  // ─── Role definitions ───
  // #1407 — Wong palette defaults that match the unscoped --mc-role-* CSS
  // vars in :root of style.css. These are FALLBACKS only — the live getter
  // below reads --mc-role-* from documentElement on every access, so any
  // preset switch (cb-presets.js) is reflected immediately without per-page
  // listener wiring. The legacy April palette (#dc2626 etc.) was the bug.
  var WONG_ROLE_DEFAULTS = {
    repeater:  '#D55E00',
    companion: '#56B4E9',
    room:      '#009E73',
    sensor:    '#F0E442',
    observer:  '#CC79A7',
    unknown:   '#6b7280'
  };
  var WONG_ROLE_TEXT_DEFAULTS = {
    repeater: '#1a1a1a', companion: '#1a1a1a', room: '#1a1a1a',
    sensor: '#1a1a1a', observer: '#1a1a1a', unknown: '#1a1a1a'
  };

  function _readCssVar(name, fallback) {
    try {
      if (typeof document === 'undefined' || !document.documentElement) return fallback;
      var v = '';
      if (typeof getComputedStyle === 'function') {
        v = getComputedStyle(document.documentElement).getPropertyValue(name);
      }
      if (!v && document.documentElement.style && typeof document.documentElement.style.getPropertyValue === 'function') {
        v = document.documentElement.style.getPropertyValue(name);
      }
      v = (v || '').trim();
      return v || fallback;
    } catch (e) { return fallback; }
  }

  // Server-config overrides go into this object; the getter prefers them
  // when present so backend-pushed role colors still win over CSS vars.
  var _roleOverrides = {};

  function _liveRoleColors() {
    var base = {};
    var roles = ['repeater', 'companion', 'room', 'sensor', 'observer'];
    for (var i = 0; i < roles.length; i++) {
      var k = roles[i];
      base[k] = _roleOverrides[k] || _readCssVar('--mc-role-' + k, WONG_ROLE_DEFAULTS[k]);
    }
    base.unknown = _roleOverrides.unknown || WONG_ROLE_DEFAULTS.unknown;
    // Wrap in a Proxy so per-key assignment by legacy callers (customizer:
    //   `window.ROLE_COLORS[key] = inp.value`) lands in _roleOverrides and
    //   is visible on the NEXT read. Without this, the mutation would be
    //   thrown away when the snapshot is GC'd. Falls back to a plain object
    //   in environments without Proxy (none we ship to, but cheap).
    if (typeof Proxy === 'function') {
      return new Proxy(base, {
        set: function (t, prop, value) {
          _roleOverrides[prop] = value;
          t[prop] = value;
          return true;
        }
      });
    }
    return base;
  }

  Object.defineProperty(window, 'ROLE_COLORS', {
    configurable: true,
    enumerable: true,
    get: function () { return _liveRoleColors(); },
    // Setter accepts per-key writes — older callers do
    //   `ROLE_COLORS.repeater = '#xxx'`
    // which on a getter-only object would silently no-op in strict mode.
    // We treat any whole-object assignment as an override merge so the
    // legacy customizer code path still works.
    set: function (v) {
      if (v && typeof v === 'object') {
        for (var k in v) if (Object.prototype.hasOwnProperty.call(v, k)) _roleOverrides[k] = v[k];
      }
    }
  });
  // Per-key writes via Proxy not portable enough — expose helper for callers
  // that want to override at runtime (customizer "node colors" path).
  // #1438: snapshot of the cb-preset (or initial) CSS-var value per role
  // so that clearing an override restores the preset, not nothing.
  // We write the override to BOTH documentElement and body inline styles
  // because cb-presets ships stylesheet rules of the form
  //   body[data-cb-preset="deut"] { --mc-role-X: #...; }
  // which beats inheritance from :root. Body inline beats both.
  var _presetCssSnapshot = {};
  function _styleTargets() {
    var t = [];
    try { if (document.documentElement && document.documentElement.style) t.push(document.documentElement.style); } catch (e) {}
    try { if (document.body && document.body.style) t.push(document.body.style); } catch (e) {}
    return t.filter(function (s) { return s && typeof s.setProperty === 'function'; });
  }
  window.setRoleColorOverride = function (role, hex) {
    if (!role) return;
    var targets = _styleTargets();
    var varName = '--mc-role-' + role;

    if (hex == null || hex === '') {
      // Clear override → restore prior CSS var values captured at
      // first-override time, so CSS-var consumers see the preset color
      // again (matches JS getter behavior, preserves #1412 contract).
      delete _roleOverrides[role];
      if (Object.prototype.hasOwnProperty.call(_presetCssSnapshot, role)) {
        var snap = _presetCssSnapshot[role] || {};
        targets.forEach(function (s, i) {
          var prior = snap[i];
          if (prior && prior.length) s.setProperty(varName, prior);
          else s.removeProperty(varName);
        });
        delete _presetCssSnapshot[role];
      } else {
        targets.forEach(function (s) { s.removeProperty(varName); });
      }
      return;
    }
    // Capture the current per-target CSS var values before overwriting,
    // but only on the first override for this role so repeated picks
    // don't lose the original preset value.
    if (!Object.prototype.hasOwnProperty.call(_presetCssSnapshot, role)) {
      _presetCssSnapshot[role] = targets.map(function (s) {
        return s.getPropertyValue ? (s.getPropertyValue(varName) || '').trim() : '';
      });
    }
    _roleOverrides[role] = hex;
    // #1438: drive the CSS var so CSS-var consumers (cluster pills,
    // route lines, all marker SVGs that use fill="var(--mc-role-X)")
    // pick up the operator's hex without a page reload. Writing to
    // body inline style is necessary because body[data-cb-preset="..."]
    // selectors beat :root inheritance.
    //
    // #1446: write with !important so the inline body declaration also
    // beats the body[data-cb-preset="X"] CSS rule on equal specificity.
    // Without !important, the cascade order picks the later-defined
    // stylesheet rule in some browser versions even though specificity
    // (1,0,1) matches the inline body style — operator pick visibly
    // loses to active preset (root cause of #1444).
    targets.forEach(function (s) {
      // documentElement gets the value without !important (used as the
      // canonical readout for the JS getter); body gets !important so it
      // wins the CSS cascade against body[data-cb-preset="X"].
      if (s === (document.body && document.body.style)) {
        s.setProperty(varName, hex, 'important');
      } else {
        s.setProperty(varName, hex);
      }
    });
  };
  // Back-compat: also export the writable override map so customize.js's
  // `window.ROLE_COLORS[key] = inp.value` style mutation works.
  // We intercept by replacing the getter target with a Proxy on access.
  Object.defineProperty(window, 'ROLE_COLORS_OVERRIDES', {
    value: _roleOverrides, writable: false, enumerable: false, configurable: false
  });

  // PR #1804 r1 item 4 (tufte4+adv5): ACK and UNKNOWN deliberately share
  // the neutral #6b7280 swatch (both render as the "Other" bucket). The
  // legend now identifies rows via data-enum="<ENUM>", so the reverse
  // color→enum lookup is no longer needed for tests or for the runtime
  // — the prior insertion-order workaround is gone, restoring natural
  // declaration order.
  window.TYPE_COLORS = {
    ADVERT: '#22c55e', GRP_TXT: '#3b82f6', GRP_DATA: '#8b5cf6', TXT_MSG: '#f59e0b',
    ACK: '#6b7280',
    REQ: '#a855f7', RESPONSE: '#06b6d4', TRACE: '#ec4899', PATH: '#14b8a6',
    ANON_REQ: '#f43f5e', MULTIPART: '#0d9488', CONTROL: '#b45309', RAW_CUSTOM: '#c026d3',
    UNKNOWN: '#6b7280'
  };

  // Badge CSS class name mapping
  const TYPE_BADGE_MAP = {
    ADVERT: 'advert', GRP_TXT: 'grp-txt', GRP_DATA: 'grp-data', TXT_MSG: 'txt-msg', ACK: 'ack',
    REQ: 'req', RESPONSE: 'response', TRACE: 'trace', PATH: 'path',
    ANON_REQ: 'anon-req', MULTIPART: 'multipart', CONTROL: 'control', RAW_CUSTOM: 'raw-custom',
    UNKNOWN: 'unknown'
  };

  // Generate badge CSS from TYPE_COLORS — single source of truth.
  // #1668-M4: pick a readable foreground per badge background, and DARKEN
  // the bg (only for the badge — TYPE_COLORS itself is untouched, so map
  // dots / markers / live-feed dots keep their full-saturation hue) until
  // the fg/bg pair clears WCAG AA (≥4.5:1). The legacy translucent wash
  // `${color}20` with `color: ${color}` failed AA for all 14 payload types
  // (M1 audit; ratios 1.0–4.25, BLOCKER).
  window.syncBadgeColors = function() {
    var el = document.getElementById('type-color-badges');
    if (!el) { el = document.createElement('style'); el.id = 'type-color-badges'; document.head.appendChild(el); }
    function hexToRgb(hex) {
      var h = hex.replace('#','');
      return [parseInt(h.slice(0,2),16), parseInt(h.slice(2,4),16), parseInt(h.slice(4,6),16)];
    }
    function rgbToHex(rgb) {
      return '#' + rgb.map(function(v){ var s = Math.max(0,Math.min(255,Math.round(v))).toString(16); return s.length===1?'0'+s:s; }).join('');
    }
    function relLum(rgb) {
      var lin = function(v){ var s=v/255; return s<=0.03928 ? s/12.92 : Math.pow((s+0.055)/1.055, 2.4); };
      return 0.2126*lin(rgb[0]) + 0.7152*lin(rgb[1]) + 0.0722*lin(rgb[2]);
    }
    function contrast(a, b) {
      var l1 = relLum(a), l2 = relLum(b);
      var hi = l1>l2?l1:l2, lo = l1>l2?l2:l1;
      return (hi+0.05) / (lo+0.05);
    }
    function readableFg(rgb) {
      // perceived luminance (Rec.601) — matches the M4 test's pick
      var lum = (0.299*rgb[0] + 0.587*rgb[1] + 0.114*rgb[2]) / 255;
      return lum > 0.55 ? [26,26,46] : [255,255,255];
    }
    function adjustForAA(bg, fg) {
      // Darken (when white fg) or lighten (when dark fg) the bg until
      // contrast >= 4.5, max 12 steps of 8%. Preserves hue.
      var c = contrast(fg, bg);
      if (c >= 4.5) return bg;
      var darkening = fg[0] > 128; // white fg → darken bg
      var out = bg.slice();
      for (var i=0; i<12 && contrast(fg, out) < 4.5; i++) {
        out = out.map(function(v){ return darkening ? v*0.92 : v + (255-v)*0.08; });
      }
      return out.map(function(v){ return Math.round(v); });
    }
    var css = '';
    for (var type in TYPE_BADGE_MAP) {
      var color = window.TYPE_COLORS[type];
      if (!color) continue;
      var cls = TYPE_BADGE_MAP[type];
      var bg = hexToRgb(color);
      var fg = readableFg(bg);
      var adj = adjustForAA(bg, fg);
      css += '.badge-' + cls + ' { background: ' + rgbToHex(adj) + '; color: rgb(' + fg.join(',') + '); }\n';
    }
    el.textContent = css;
  };

  // #1668 M5 — public helper for callers that render a badge INLINE
  // (analytics.js / nodes.js role badges where the color isn't known at
  // CSS-author time, e.g. role colors come from server config). Returns
  // "background:#xxx;color:#xxx" with AA-safe pairing — same algorithm
  // as syncBadgeColors above. Avoids the legacy 20%-alpha-on-tinted-bg
  // pattern that fails AA (M5 axe gate caught 90+ instances).
  window.aaBadgeStyle = function(color) {
    if (!color) return '';
    function hexToRgb(hex) {
      var h = String(hex).replace('#','');
      if (h.length !== 6) return [128,128,128];
      return [parseInt(h.slice(0,2),16), parseInt(h.slice(2,4),16), parseInt(h.slice(4,6),16)];
    }
    function rgbToHex(rgb) {
      return '#' + rgb.map(function(v){ var s = Math.max(0,Math.min(255,Math.round(v))).toString(16); return s.length===1?'0'+s:s; }).join('');
    }
    function relLum(rgb) {
      var lin = function(v){ var s=v/255; return s<=0.03928 ? s/12.92 : Math.pow((s+0.055)/1.055, 2.4); };
      return 0.2126*lin(rgb[0]) + 0.7152*lin(rgb[1]) + 0.0722*lin(rgb[2]);
    }
    function contrast(a, b) {
      var l1 = relLum(a), l2 = relLum(b);
      var hi = l1>l2?l1:l2, lo = l1>l2?l2:l1;
      return (hi+0.05) / (lo+0.05);
    }
    function readableFg(rgb) {
      var lum = (0.299*rgb[0] + 0.587*rgb[1] + 0.114*rgb[2]) / 255;
      return lum > 0.55 ? [26,26,46] : [255,255,255];
    }
    function adjustForAA(bg, fg) {
      var c = contrast(fg, bg);
      if (c >= 4.5) return bg;
      var darkening = fg[0] > 128;
      var out = bg.slice();
      for (var i=0; i<12 && contrast(fg, out) < 4.5; i++) {
        out = out.map(function(v){ return darkening ? v*0.92 : v + (255-v)*0.08; });
      }
      return out.map(function(v){ return Math.round(v); });
    }
    var bg = hexToRgb(color);
    var fg = readableFg(bg);
    var adj = adjustForAA(bg, fg);
    return 'background:' + rgbToHex(adj) + ';color:rgb(' + fg.join(',') + ')';
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

  // #1293 — Marker shape per role (WCAG 1.4.1 — shape, not only colour).
  // Single source of truth; ROLE_STYLE.shape is derived from this map.
  window.ROLE_SHAPES = {
    repeater:  'circle',
    companion: 'square',
    room:      'hexagon',
    sensor:    'triangle',
    observer:  'diamond'
  };

  // #1407 — ROLE_STYLE.color reads live (matches ROLE_COLORS getter).
  // The shape/radius/weight stay static. Stored overrides survive across
  // reads via the closure above.
  var _styleShapes = {
    repeater:  { shape: 'circle',   radius: 8, weight: 2 },
    companion: { shape: 'square',   radius: 8, weight: 2 },
    room:      { shape: 'hexagon',  radius: 9, weight: 2 },
    sensor:    { shape: 'triangle', radius: 8, weight: 2 },
    observer:  { shape: 'diamond',  radius: 9, weight: 2 }
  };
  function _buildRoleStyle() {
    var out = {};
    var live = _liveRoleColors();
    for (var role in _styleShapes) {
      var s = _styleShapes[role];
      out[role] = {
        color:  _roleOverrides[role] || live[role],
        shape:  s.shape,
        radius: s.radius,
        weight: s.weight
      };
    }
    return out;
  }
  Object.defineProperty(window, 'ROLE_STYLE', {
    configurable: true,
    enumerable: true,
    get: function () { return _buildRoleStyle(); },
    set: function (v) {
      // Legacy whole-object assignment: copy color overrides only.
      if (v && typeof v === 'object') {
        for (var k in v) if (v[k] && v[k].color) _roleOverrides[k] = v[k].color;
      }
    }
  });

  // Glyphs mirror the ROLE_SHAPES (used in tooltips, legends, lists).
  window.ROLE_EMOJI = {
    repeater: '●', companion: '■', room: '⬢', sensor: '▲', observer: '◆'
  };

  /**
   * #1293 — Shared SVG marker generator. Returns a self-contained
   * <svg>...</svg> string for the given role/colour/size, with white
   * stroke for contrast (works on both dark + light tiles). Used by:
   *   - public/live.js  → addNodeMarker (L.divIcon)
   *   - public/live.js  → role legend swatches
   *   - public/map.js   → makeMarkerIcon (legacy switch retained for
   *                       per-role overrides + observer star overlay)
   *
   * Reads ROLE_SHAPES for the role's geometry; falls back to circle.
   * Caller controls colour to allow theming overrides (matrix mode,
   * stale dim, etc.) without rebuilding the marker.
   */
  window.makeRoleMarkerSVG = function (role, color, size) {
    var shape = (window.ROLE_SHAPES && window.ROLE_SHAPES[role]) || 'circle';
    size = size || 16;
    var c = size / 2;
    // #1438: default fill resolves through the live CSS var so existing
    // mounted SVG markers recolor when cb-preset switches or the
    // operator picks a per-role override via the customizer. Callers
    // that need a fixed tint (matrix mode, stale dim) keep passing
    // their explicit colour.
    var fill = color || ('var(--mc-role-' + (role || 'companion') + ')');
    // #1488 — stroke routed through CSS vars so operators can dial
    // colour/width without code edits (customizer Colors → Marker Stroke).
    var strokeAttr = ' stroke="var(--mc-marker-stroke-color)" stroke-width="var(--mc-marker-stroke-width)" stroke-opacity="var(--mc-marker-stroke-opacity)"';
    var path;
    switch (shape) {
      case 'square':
        path = '<rect x="3" y="3" width="' + (size - 6) + '" height="' + (size - 6) +
               '" fill="' + fill + '"' + strokeAttr + '/>';
        break;
      case 'triangle':
        path = '<polygon points="' + c + ',2 ' + (size - 2) + ',' + (size - 2) +
               ' 2,' + (size - 2) + '" fill="' + fill + '"' + strokeAttr + '/>';
        break;
      case 'diamond':
        path = '<polygon points="' + c + ',2 ' + (size - 2) + ',' + c + ' ' +
               c + ',' + (size - 2) + ' 2,' + c +
               '" fill="' + fill + '"' + strokeAttr + '/>';
        break;
      case 'hexagon': {
        // Pointy-top hexagon centred at (c,c), inscribed radius ≈ c-1.5
        var r = c - 1.5;
        var pts = '';
        for (var i = 0; i < 6; i++) {
          var a = (i * 60 - 90) * Math.PI / 180;
          pts += (c + r * Math.cos(a)).toFixed(2) + ',' +
                 (c + r * Math.sin(a)).toFixed(2) + ' ';
        }
        path = '<polygon points="' + pts.trim() + '" fill="' + fill +
               '"' + strokeAttr + '/>';
        break;
      }
      case 'star': {
        var cx = c, cy = c, outer = c - 1, inner = outer * 0.4;
        var spts = '';
        for (var j = 0; j < 5; j++) {
          var aO = (j * 72 - 90) * Math.PI / 180;
          var aI = ((j * 72) + 36 - 90) * Math.PI / 180;
          spts += (cx + outer * Math.cos(aO)) + ',' + (cy + outer * Math.sin(aO)) + ' ';
          spts += (cx + inner * Math.cos(aI)) + ',' + (cy + inner * Math.sin(aI)) + ' ';
        }
        path = '<polygon points="' + spts.trim() + '" fill="' + fill +
               '"' + strokeAttr + '/>';
        break;
      }
      default: // circle
        path = '<circle cx="' + c + '" cy="' + c + '" r="' + (c - 2) +
               '" fill="' + fill + '"' + strokeAttr + '/>';
    }
    return '<svg width="' + size + '" height="' + size +
           '" viewBox="0 0 ' + size + ' ' + size +
           '" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">' + path + '</svg>';
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

  // Simplified two-state helper: returns 'active' or 'stale'.
  // Accepts either a full node object (preferred) or legacy (role, lastSeenMs).
  //
  // #1598: for infra roles (repeater/room), freshness is the max of the
  // ADVERT-based timestamp and last_relayed (resolved path participation).
  // Operators increasingly run long or disabled advert intervals (firmware
  // default trajectory is 47h flood adverts), so an actively-relaying
  // backbone repeater must not be marked stale just because its last
  // ADVERT is old.
  // #1845: the single definition of "how recently did we hear from this node",
  // extracted from getNodeStatus so a caller that needs the AGE gets the same
  // answer as the caller that needs the STATUS. Before this, nodes.js carried
  // two notions: getNodeStatus (relay-aware) and a local statusAge computed
  // from the advert timestamp alone. They disagreed for exactly the nodes
  // #1598 exists to protect. Returns NaN when nothing is known.
  window.getEffectiveHeardMs = function (n) {
    if (!n || typeof n !== 'object') return NaN;
    var role = String(n.role || 'companion').toLowerCase();
    // Freshness precedence mirrors the existing call sites:
    // _liveSeen (live view, ms) > _lastHeard (health API) >
    // last_heard (in-memory packets) > last_seen (DB, ADVERT).
    var seenTime = n._lastHeard || n.last_heard || n.last_seen;
    var effectiveMs = (typeof n._liveSeen === 'number' && n._liveSeen) ||
                      (seenTime ? new Date(seenTime).getTime() : NaN);
    // #1598: an infra node that forwards traffic is alive even when its last
    // ADVERT is old, which operators increasingly cause on purpose (the
    // firmware default flood advert interval moved to 47h).
    if ((role === 'repeater' || role === 'room') && n.last_relayed) {
      var relayedMs = new Date(n.last_relayed).getTime();
      if (!(effectiveMs >= relayedMs)) effectiveMs = relayedMs;
    }
    return effectiveMs;
  };

  window.getNodeStatus = function (roleOrNode, lastSeenMs) {
    var role, effectiveMs;
    if (roleOrNode && typeof roleOrNode === 'object') {
      role = roleOrNode.role || 'companion';
      effectiveMs = window.getEffectiveHeardMs(roleOrNode);
      if (isNaN(effectiveMs)) effectiveMs = undefined;
    } else {
      role = roleOrNode;
      effectiveMs = lastSeenMs;
    }
    var isInfra = String(role || '').toLowerCase() === 'repeater' ||
                  String(role || '').toLowerCase() === 'room';
    var staleMs = isInfra ? HEALTH_THRESHOLDS.infraSilentMs : HEALTH_THRESHOLDS.nodeSilentMs;
    var age = typeof effectiveMs === 'number' ? (Date.now() - effectiveMs) : Infinity;
    return age < staleMs ? 'active' : 'stale';
  };

  // ─── Tile URLs ───
  window.TILE_DARK  = 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png';
  window.TILE_LIGHT = 'https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png';

  window.getTileUrl = function () {
    var isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' &&
       window.matchMedia('(prefers-color-scheme: dark)').matches);
    if (!isDark) return TILE_LIGHT;
    // #1461 followup: honor customizer's dark-tile-provider pick (#1420 / #1430)
    // when the registry is loaded. Falls back to TILE_DARK if absent.
    try {
      if (window.MC_getDarkTileProvider && window.MC_TILE_PROVIDERS) {
        var id = window.MC_getDarkTileProvider();
        var p = window.MC_TILE_PROVIDERS[id];
        if (p && (p.url || p.baseUrl)) {
          // #1614: providers added in #1533 (carto/osm/stamen) declare
          // `url` as a function for lazy config resolution. Invoke it so
          // we always return a string URL template; L.tileLayer otherwise
          // stringifies the function source and every tile request 404s.
          var u = p.url || p.baseUrl;
          return (typeof u === 'function') ? u() : u;
        }
      }
    } catch (_e) {}
    return TILE_DARK;
  };
  /* Helper: get the full provider object (for callers that also need the
   * invertFilter or refUrl/attribution). Returns null when no customizer
   * provider applies (light mode, or registry not loaded). */
  window.getActiveTileProvider = function () {
    var isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' &&
       window.matchMedia('(prefers-color-scheme: dark)').matches);
    if (!isDark) return null;
    try {
      if (window.MC_getDarkTileProvider && window.MC_TILE_PROVIDERS) {
        var id = window.MC_getDarkTileProvider();
        return window.MC_TILE_PROVIDERS[id] || null;
      }
    } catch (_e) {}
    return null;
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

  // #1574 — operator-configurable cap on /live map node fetch (overridden
  // from /api/config/client below). Default mirrors the historical
  // hardcoded literal in public/live.js.
  window.LIVE_MAP_MAX_NODES = 2000;

  // ─── External URLs ───
  window.EXTERNAL_URLS = {
    flasher: 'https://flasher.meshcore.io/'
  };

  // ─── Fetch server overrides ───
  window.MeshConfigReady = fetch('/api/config/client').then(function (r) { return r.json(); }).then(function (cfg) {
    window.MC_CLIENT_RX_COVERAGE = cfg.clientRxCoverage === true;
    // Coverage is opt-in: the nav link is NOT in static HTML (so the default-off
    // nav matches upstream and the nav-overflow tests). Inject it after Analytics
    // only when enabled, then nudge applyNavPriority (it re-runs on 'resize').
    if (window.MC_CLIENT_RX_COVERAGE && !document.querySelector('.nav-links [data-route="rx-coverage"]')) {
      var navAnchor = document.querySelector('.nav-links [data-route="analytics"]');
      if (navAnchor) {
        var covLink = document.createElement('a');
        covLink.href = '#/rx-coverage';
        covLink.className = 'nav-link';
        covLink.setAttribute('data-route', 'rx-coverage');
        covLink.innerHTML = '<svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-broadcast"></use></svg> Coverage';
        navAnchor.insertAdjacentElement('afterend', covLink);
        window.dispatchEvent(new Event('resize'));
      }
    }
    if (cfg.roles) {
      if (cfg.roles.colors) {
        // #1407 — ROLE_COLORS is now a live getter; merge into the override map.
        for (var rk in cfg.roles.colors) _roleOverrides[rk] = cfg.roles.colors[rk];
      }
      if (cfg.roles.labels) Object.assign(ROLE_LABELS, cfg.roles.labels);
      if (cfg.roles.style) {
        // Same: merge color overrides only; shape/radius/weight come from _styleShapes.
        for (var sk in cfg.roles.style) {
          if (cfg.roles.style[sk] && cfg.roles.style[sk].color) _roleOverrides[sk] = cfg.roles.style[sk].color;
        }
      }
      if (cfg.roles.emoji) Object.assign(ROLE_EMOJI, cfg.roles.emoji);
      if (cfg.roles.sort) window.ROLE_SORT = cfg.roles.sort;
    }
    if (cfg.healthThresholds) Object.assign(HEALTH_THRESHOLDS, cfg.healthThresholds);
    if (cfg.map) {
      window.MC_MAP_CFG = cfg.map;
    } else {
      // Fallback for older configs
      window.MC_MAP_CFG = { tiles: { providers: {} } };
    }
    // Backward compat for older tile URL overrides
    if (cfg.tiles) {
      if (cfg.tiles.dark) window.TILE_DARK = cfg.tiles.dark;
      if (cfg.tiles.light) window.TILE_LIGHT = cfg.tiles.light;
    } else if (cfg.map && cfg.map.tiles) {
      if (cfg.map.tiles.darkUrl) window.TILE_DARK = cfg.map.tiles.darkUrl;
      if (cfg.map.tiles.lightUrl) window.TILE_LIGHT = cfg.map.tiles.lightUrl;
    }
    if (typeof window.MC_initTileRegistry === 'function') window.MC_initTileRegistry(true);
    // CARTO raster needs an API key now, so the bare TILE_DARK/TILE_LIGHT
    // fallbacks have to pick it up as well — getTileUrl() returns TILE_LIGHT
    // directly in light mode and never consults the registry. Explicit
    // darkUrl/lightUrl overrides above still win.
    if (typeof window.MC_tileUrlById === 'function') {
      var _tOv = (cfg.tiles || (cfg.map && cfg.map.tiles) || {});
      if (!_tOv.dark && !_tOv.darkUrl)   window.TILE_DARK  = window.MC_tileUrlById('carto-dark',  window.TILE_DARK);
      if (!_tOv.light && !_tOv.lightUrl) window.TILE_LIGHT = window.MC_tileUrlById('carto-light', window.TILE_LIGHT);
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
    // #1508 — expose operator-side customizer knobs to customize-v2.js.
    // Default to an empty disabledTabs list so the .indexOf() guard in
    // _renderTabs is a no-op when the field is absent.
    window.MC_CUSTOMIZER_CFG = (cfg.customizer && typeof cfg.customizer === 'object')
      ? { disabledTabs: Array.isArray(cfg.customizer.disabledTabs) ? cfg.customizer.disabledTabs : [] }
      : { disabledTabs: [] };
    // #1574 — operator-configurable cap on /live map node count.
    if (cfg.liveMapMaxNodes != null) window.LIVE_MAP_MAX_NODES = cfg.liveMapMaxNodes;
    // #1784 — path trust threshold: minimum hash bytes for mapping evidence.
    // Default 2 means 1-byte observations are excluded from topology/mapping.
    window.PATH_TRUST = cfg.pathTrust && cfg.pathTrust.minHashBytesForMapping
      ? cfg.pathTrust.minHashBytesForMapping
      : 2;
    // Sync ROLE_STYLE colors with ROLE_COLORS
    // #1407 — both are now live getters; no manual sync needed. Kept as no-op for clarity.
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
      .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener" style="color:var(--link-color)">$1</a>')
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
    bimodal_clock: 'var(--status-amber)',
    no_clock: 'var(--text-muted)'
  };
  var SKEW_SEVERITY_LABELS = {
    ok: 'OK', warning: 'Warning', critical: 'Critical', absurd: 'Absurd', bimodal_clock: 'Bimodal', no_clock: 'No Clock'
  };
  var SKEW_SEVERITY_ORDER = { no_clock: 0, bimodal_clock: 1, absurd: 2, critical: 3, warning: 4, ok: 5 };

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
  window.renderSkewBadge = function(severity, skewSec, cs) {
    if (!severity) return '';
    var cls = 'skew-badge skew-badge--' + severity;
    if (severity === 'no_clock') {
      return '<span class="' + cls + '" title="Uninitialized RTC — no valid clock"><svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-prohibit"/></svg> No Clock</span>';
    }
    if (severity === 'bimodal_clock' && cs) {
      var badPct = cs.goodFraction != null ? Math.round((1 - cs.goodFraction) * 100) : '?';
      var label = '⏰ ' + window.formatSkew(skewSec);
      return '<span class="' + cls + '" title="Clock skew: ' + window.formatSkew(skewSec) + ' (bimodal: ' + badPct + '% of recent adverts have nonsense timestamps)">' + label + '</span>';
    }
    var label = severity === 'ok' ? '⏰' : '⏰ ' + window.formatSkew(skewSec);
    return '<span class="' + cls + '" title="Clock skew: ' + window.formatSkew(skewSec) + ' (' + (SKEW_SEVERITY_LABELS[severity] || severity) + ')">' + label + '</span>';
  };

  /** Compute severity for an observer's clock offset (seconds). */
  window.observerSkewSeverity = function(offsetSec) {
    var abs = Math.abs(offsetSec);
    return abs >= 3600 ? 'critical' : abs >= 300 ? 'warning' : 'ok';
  };

  /**
   * Hash prefix for display. `hash_size` is EVIDENCE, not a default: it is set
   * only from adverts the analyzer could actually read a size out of, so a node
   * that has not advertised in the retention window has no value at all. Reading
   * that absence as "1" claims a 1-byte configuration nobody observed — and the
   * 1-byte bucket is exactly where a node is most likely to be miscounted.
   *
   * nodes.js (detail: "Unknown") and analytics.js ("?B") already treat it that
   * way; this helper is the shared version so the map can too.
   *
   * @returns {{known: boolean, bytes: number, prefix: string}} `bytes` is the
   * rendering width — the real evidence when known, a 1-byte fallback when not,
   * in which case `known` is false and callers must not print it as a size.
   */
  window.hashPrefixInfo = function (node) {
    var raw = node ? node.hash_size : null;
    var known = raw != null && Number(raw) > 0;
    var bytes = known ? Number(raw) : 1;
    var pk = (node && node.public_key) || '';
    return {
      known: known,
      bytes: bytes,
      prefix: pk ? pk.slice(0, bytes * 2).toUpperCase() : '??',
    };
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
