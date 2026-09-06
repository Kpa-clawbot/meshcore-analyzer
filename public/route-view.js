/* route-view.js — sequence-primary redesign of the packet-route map view.
 *
 * Principles (from consultation):
 *   - The data is a SEQUENCE. Geography is annotation.
 *   - Sequence encoded ONCE: viridis edge gradient (1=purple → N=yellow).
 *   - NO numeric chips on markers. NO mid-edge arrows. NO floating labels.
 *   - Origin = filled square 2x. Destination = filled triangle 2x. Intermediates = plain 8px circles.
 *   - Sidebar timeline (320px) is the PRIMARY view; map is secondary locator.
 *   - Hop-distance sparkline at top of sidebar.
 *   - Hover sidebar row → marker scales 1.5x, edge segment highlights.
 *   - Mobile: sidebar full-width, map toggle-only.
 */
(function () {
  'use strict';

  function haversineKm(a, b) {
    if (a == null || b == null || a.lat == null || b.lat == null) return null;
    var R = 6371, dLat = (b.lat - a.lat) * Math.PI / 180;
    var dLon = (b.lon - a.lon) * Math.PI / 180;
    var la1 = a.lat * Math.PI / 180, la2 = b.lat * Math.PI / 180;
    var h = Math.sin(dLat/2)**2 + Math.cos(la1)*Math.cos(la2)*Math.sin(dLon/2)**2;
    return Math.round(R * 2 * Math.atan2(Math.sqrt(h), Math.sqrt(1-h)));
  }

  // Sequence ramp (5 stops). Read from CSS vars so cb-presets.js can override
  // per CB preset (viridis / plasma / luminance-only / etc.). Falls back to
  // viridis (light) / trimmed-magma (dark) when CSS vars aren't set.
  var FALLBACK_LIGHT = ['#440154', '#3b528b', '#21918c', '#5ec962', '#fde725']; // viridis
  var FALLBACK_DARK  = ['#3b0f70', '#641a80', '#9c179e', '#cc4778', '#fb9f3a']; // magma trimmed

  function isDark() {
    return document.documentElement.getAttribute('data-theme') === 'dark';
  }
  function _cssVar(name) {
    try {
      var v = getComputedStyle(document.documentElement).getPropertyValue(name);
      return v ? v.trim() : '';
    } catch (e) { return ''; }
  }
  function currentRamp() {
    var ramp = [];
    for (var i = 0; i < 5; i++) {
      var v = _cssVar('--mc-rt-ramp-' + i);
      if (v) ramp.push(v);
    }
    if (ramp.length === 5) return ramp;
    return isDark() ? FALLBACK_DARK : FALLBACK_LIGHT;
  }
  function rampColor(i, n) {
    var ramp = currentRamp();
    if (n <= 1) return ramp[ramp.length - 1];
    var t = i / (n - 1);
    var bucket = t * (ramp.length - 1);
    var lo = Math.floor(bucket), hi = Math.min(lo + 1, ramp.length - 1);
    var f = bucket - lo;
    function mix(c1, c2, f) {
      var r1 = parseInt(c1.slice(1,3),16), g1 = parseInt(c1.slice(3,5),16), b1 = parseInt(c1.slice(5,7),16);
      var r2 = parseInt(c2.slice(1,3),16), g2 = parseInt(c2.slice(3,5),16), b2 = parseInt(c2.slice(5,7),16);
      var r = Math.round(r1 + (r2-r1)*f), g = Math.round(g1 + (g2-g1)*f), b = Math.round(b1 + (b2-b1)*f);
      return '#' + ((1<<24)+(r<<16)+(g<<8)+b).toString(16).slice(1);
    }
    return mix(ramp[lo], ramp[hi], f);
  }

  function relativeTime(iso) {
    if (!iso) return '–';
    var t = new Date(iso).getTime();
    var d = Date.now() - t;
    if (d < 60000) return Math.round(d/1000) + 's ago';
    if (d < 3600000) return Math.round(d/60000) + 'm ago';
    if (d < 86400000) return Math.round(d/3600000) + 'h ago';
    return Math.round(d/86400000) + 'd ago';
  }

  // #1424 — pure helpers (escapeHtml, buildPacketContextBlock,
  // buildSnrSparkline) extracted into public/route-view-utils.js. Loader
  // for that file MUST run before route-view.js (see index.html). Local
  // refs keep the call sites in this file unchanged.
  var _MC_RT_U = window.MC_ROUTE_UTILS || {};
  var escapeHtml = _MC_RT_U.escapeHtml;
  var buildPacketContextBlock = _MC_RT_U.buildPacketContextBlock;
  var buildSnrSparkline = _MC_RT_U.buildSnrSparkline;

  // Polish review (carmack #1423): bound _detailCache (was unbounded plain
  // object; every distinct pubkey ever clicked was retained for the tab's
  // lifetime). LRU(50) via Map insertion-order. Also cleared on
  // teardownIfNavigatedAway so a navigate-away frees memory immediately.
  var DETAIL_CACHE_MAX = 50;
  var _detailCache = new Map();
  function fetchHopDetail(pubkey) {
    if (!pubkey) return Promise.resolve(null);
    var pk = String(pubkey).toLowerCase();
    if (_detailCache.has(pk)) {
      // LRU bump: re-insert to move to most-recent position.
      var cached = _detailCache.get(pk);
      _detailCache.delete(pk);
      _detailCache.set(pk, cached);
      return Promise.resolve(cached);
    }
    return Promise.all([
      fetch('/api/nodes/' + pk).then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; }),
      fetch('/api/nodes/' + pk + '/analytics?window=24h').then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; }),
      fetch('/api/nodes/' + pk + '/paths?limit=20').then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; }),
    ]).then(function (out) {
      var result = { detail: out[0], analytics: out[1], paths: out[2] };
      // Evict oldest entry (Map preserves insertion order) before adding.
      while (_detailCache.size >= DETAIL_CACHE_MAX) {
        var oldestKey = _detailCache.keys().next().value;
        _detailCache.delete(oldestKey);
      }
      _detailCache.set(pk, result);
      return result;
    });
  }

  function renderHopDetail(p, container) {
    container.innerHTML = '<div class="mc-rt-detail-loading">Loading hop info…</div>';
    fetchHopDetail(p.pubkey).then(function (data) {
      if (!data) {
        container.innerHTML = '<div class="mc-rt-detail-na">No data for this node</div>';
        return;
      }
      var node = (data.detail && data.detail.node) || {};
      var ana = data.analytics || {};
      var paths = data.paths || {};
      var pkShort = p.pubkey ? (String(p.pubkey).slice(0, 6) + '…' + String(p.pubkey).slice(-4)) : '?';
      var suspectedWarn = '';
      if (node.multi_byte_status && node.multi_byte_status !== 'confirmed') {
        // Describe WHAT we're unsure about (hash-prefix size), not just
        // 'SUSPECTED' which reads like an accusation. Per multibyte detection:
        //   'suspected' = saw conflicting prefix evidence, hash size unclear
        //   'unknown'   = no advert sample yet, can't determine size
        var lbl = node.multi_byte_status === 'suspected' ? 'hash ambiguous' : 'hash unverified';
        var titleTxt = node.multi_byte_status === 'suspected'
          ? 'Conflicting evidence about this node\u2019s hash-prefix size (multi-byte not confirmed)'
          : 'No advert sample yet to confirm hash-prefix size';
        suspectedWarn = '<span class="mc-rt-detail-warn status-warn" title="' + escapeHtml(titleTxt) + '"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> ' + lbl + '</span>';
      }
      var rel = node.last_seen ? relativeTime(node.last_seen) : '–';
      var snr = buildSnrSparkline(ana.snrTrend || []);
      var rx = node.relay_count_24h, total_tx = paths.totalTransmissions;
      var ratioHtml = (rx != null && total_tx != null && total_tx > 0)
        ? '<b>' + rx + '</b> relays / <b>' + total_tx + '</b> tx (24h)'
        : (rx != null ? '<b>' + rx + '</b> relays (24h)' : '<span class="mc-rt-detail-na">no relay data</span>');
      var routeCount = paths.totalPaths || (paths.paths ? paths.paths.length : 0);
      // Link to the node's detail page — that page shows all paths through
      // this node in its "Paths Through This Node" section. Earlier label
      // was "also in N routes →" which buried the destination + had poor
      // a11y (no aria-label, no role, only ASCII arrow).
      var nodePk = node.public_key || p.pubkey;
      var alsoIn = '<a class="mc-rt-detail-link mc-rt-detail-action"' +
        ' href="#/nodes/' + escapeHtml(nodePk) + '"' +
        ' role="link"' +
        ' aria-label="Open node details for ' + escapeHtml(node.name || p.name || 'this hop') + (routeCount > 1 ? ' (seen in ' + routeCount + ' routes)' : '') + '"' +
        ' title="Open this node\u2019s detail page' + (routeCount > 1 ? ' — including the ' + routeCount + ' routes it appears in' : '') + '">' +
        '<span aria-hidden="true">↗</span> Node details' +
        (routeCount > 1 ? ' <span class="mc-rt-route-badge">' + routeCount + ' routes</span>' : '') +
      '</a>';

      container.innerHTML =
        '<div class="mc-rt-detail">' +
          '<div class="mc-rt-detail-row1">' +
            '<span class="mc-rt-detail-name">' + escapeHtml(node.name || p.name || '?') + '</span>' +
            suspectedWarn +
            '<span class="mc-rt-detail-meta">' + rel + ' · ' + escapeHtml(node.role || p.role || '?') + ' · ' + pkShort + '</span>' +
          '</div>' +
          '<div class="mc-rt-detail-snr"><span class="mc-rt-detail-label">SNR</span>' + snr + '</div>' +
          '<div class="mc-rt-detail-relay"><span class="mc-rt-detail-label">activity</span>' + ratioHtml + '</div>' +
          '<div class="mc-rt-detail-also">' + alsoIn + '</div>' +
        '</div>';
    });
  }


  function buildMarkerSVG(p, opts) {
    // all markers same size + shape. Sequence number INSIDE the
    // marker. SRC and DST each get a 2px hollow ring as pre-attentive
    // endpoint cue. SRC=DST (loop) gets a SECOND concentric ring — same
    // grammar (ring = endpoint), extended for the loop case. Unresolved
    // hops get a dashed muted ring.
    var size = 22;
    var color = opts.color;
    var stroke = opts.stroke || '#fff';
    var isOrigin = !!p.isOrigin;
    var isDest = !!p.isDest;
    var isLoop = !!opts.isLoop; // true when SRC == DST physical node
    var seq = (opts.seqNum != null) ? String(opts.seqNum) : '';
    var textColor = '#fff';
    try {
      if (color && color[0] === '#' && color.length === 7) {
        var r = parseInt(color.slice(1,3),16), g = parseInt(color.slice(3,5),16), b = parseInt(color.slice(5,7),16);
        var L = (0.299*r + 0.587*g + 0.114*b) / 255;
        if (L > 0.55) textColor = '#000';
      }
    } catch (e) { console.warn('[route-view]', e); }
    // If loop, grow the SVG box to fit the second ring.
    if (isLoop) size = 28;
    var cx = size / 2, cy = size / 2;
    var html = '<svg width="' + size + '" height="' + size + '" viewBox="0 0 ' + size + ' ' + size + '" aria-hidden="true">';
    if (p.resolved === false) {
      html += '<circle cx="' + cx + '" cy="' + cy + '" r="8" fill="rgba(80,80,80,0.7)" stroke="' + color + '" stroke-width="1.5" stroke-dasharray="2 2"/>';
    } else {
      html += '<circle cx="' + cx + '" cy="' + cy + '" r="8" fill="' + color + '" stroke="' + stroke + '" stroke-width="1"/>';
    }
    // Endpoint ring: inner ring for any endpoint
    if (isOrigin || isDest || isLoop) {
      html += '<circle cx="' + cx + '" cy="' + cy + '" r="10" fill="none" stroke="' + color + '" stroke-width="2" opacity="0.9"/>';
    }
    // loop case: second outer ring 3px further out
    if (isLoop) {
      html += '<circle cx="' + cx + '" cy="' + cy + '" r="13" fill="none" stroke="' + color + '" stroke-width="2" opacity="0.7"/>';
    }
    if (seq) {
      html += '<text x="' + cx + '" y="' + (cy + 3) + '" text-anchor="middle" font-family="ui-monospace,Menlo,monospace" font-size="9" font-weight="700" fill="' + textColor + '" style="pointer-events:none;user-select:none">' + seq + '</text>';
    }
    html += '</svg>';
    return { html: html, size: size };
  }

  function roleGlyph(role) {
    // #1648 M3: role shape glyphs → Phosphor sprite refs.
    // Map preserves prior visual intent (●→circle-fill, ■→square-fill, // EMOJI-OK: comment
    // ⬢→hexagon, ▲→triangle, ◆→diamond) and falls back to a hollow circle. // EMOJI-OK: comment
    var name = ({ repeater: 'ph-circle-fill', companion: 'ph-square-fill',
                  room: 'ph-hexagon', sensor: 'ph-triangle',
                  observer: 'ph-diamond' })[role] || 'ph-circle';
    return '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#' + name + '"/></svg>';
  }

  function buildSidebar(positions, mapRef, layer, edges, markers, opts) {
    opts = opts || {};
    var total = positions.length;
    // Compute hop distances
    var dists = [], maxDist = 0;
    for (var i = 1; i < total; i++) {
      var d = haversineKm(positions[i-1], positions[i]);
      dists.push(d);
      if (d != null && d > maxDist) maxDist = d;
    }
    // Sparkline header with inline title + max
    var maxDistRound = maxDist || 0;
    var sparkW = 280, sparkH = 36;
    var sparkSvg = '<svg class="mc-rt-spark" viewBox="0 0 ' + sparkW + ' ' + sparkH + '" width="' + sparkW + '" height="' + sparkH + '" aria-label="Hop distance per sequence">';
    var dotPositions = [];
    if (dists.length && maxDist > 0) {
      var pts = dists.map(function (d, idx) {
        var x = (idx / (dists.length - 1 || 1)) * sparkW;
        var y = sparkH - 2 - (d != null ? (d / maxDist) * (sparkH - 4) : 0);
        dotPositions.push({ x: x, y: y, idx: idx + 1, d: d });
        return x.toFixed(1) + ',' + y.toFixed(1);
      }).join(' ');
      sparkSvg += '<polyline points="' + pts + '" fill="none" stroke="var(--text-muted, #94a3b8)" stroke-width="1.5"/>';
      dotPositions.forEach(function (p) {
        sparkSvg += '<circle class="mc-rt-spark-dot" data-hop-idx="' + p.idx + '" data-dist="' + (p.d != null ? p.d : '') + '" cx="' + p.x.toFixed(1) + '" cy="' + p.y.toFixed(1) + '" r="2" fill="' + rampColor(p.idx - 1, dists.length) + '"/>';
      });
    }
    sparkSvg += '</svg>';
    var sparkTitle = '<div class="mc-rt-spark-title"><span>Hop distance</span><b>max ' + (maxDistRound || 0) + ' km</b></div>';
    var spark = sparkTitle + sparkSvg;

    // Build rows — stripe color = the edge ENTERING this hop (color of edge i-1).
    // Hop 0 has no incoming edge; use color of outgoing edge 0 as visual seed.
    var rows = positions.map(function (p, idx) {
      var dist = idx > 0 ? dists[idx - 1] : null;
      var distBar = '';
      if (dist != null && maxDist > 0) {
        var pct = Math.max(2, (dist / maxDist) * 100);
        distBar = '<div class="mc-rt-distbar" style="width:' + pct.toFixed(1) + '%;background:' + rampColor(idx - 1, dists.length) + '"></div>';
      }
      var distLabel = dist != null ? dist + ' km' : '–';
      var pinned = p.isOrigin ? 'origin' : (p.isDest ? 'dest' : '');
      var glyph = roleGlyph(p.role);
      var name = escapeHtml(p.name || (p.pubkey ? String(p.pubkey).slice(0,8) : '?'));
      // Show a status badge for unresolved hops:
      //  - gpsless: node identified but missing GPS → no-GPS pin chip
      //  - else:    couldn't resolve prefix       → unknown chip
      var statusBadge = '';
      if (p.resolved === false) {
        if (p.gpsless) {
          statusBadge = ' <span class="mc-rt-status-chip mc-rt-status-nogps" title="Node identified but has no GPS coordinates"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-pin"/></svg> no GPS</span>';
        } else {
          statusBadge = ' <span class="mc-rt-status-chip mc-rt-status-unknown" title="Could not resolve this hop prefix to a known node"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> unknown</span>';
        }
      }
      // hops derived from the packet's PAYLOAD (sender/recipient
      // encoded in decoded.srcHash/destHash) are visually distinct from hops
      // derived from path_json (the floodband repeaters). Operator confusion:
      // PATH packets show both, but packet-detail page only shows the inner
      // path. Marking with a chip makes the layering explicit.
      var payloadBadge = '';
      if (p._fromPayload) {
        payloadBadge = ' <span class="mc-rt-status-chip mc-rt-status-payload" title="Sender/recipient decoded from packet payload (not from outer path bytes)">from payload</span>';
      }
      var unresolved = p.resolved === false ? ' mc-rt-unresolved' : '';
      // Stripe color: incoming edge color (idx-1) for non-origin, outgoing for origin.
      var stripeIdx = idx === 0 ? 0 : (idx - 1);
      var stripeColor = total > 1 ? rampColor(stripeIdx, total - 1) : 'transparent';
      // Multi-path observer chip (passed via p.observerCount / p.observerTotal if available)
      var obsChip = '';
      if (p.observerCount != null && p.observerTotal != null && p.observerTotal > 1) {
        obsChip = '<span class="mc-rt-obs-chip" title="Observed by ' + p.observerCount + ' of ' + p.observerTotal + ' observers">' + p.observerCount + '/' + p.observerTotal + '</span>';
      }
      return '<li class="mc-rt-row ' + pinned + unresolved + '" data-hop-idx="' + idx + '" tabindex="0" style="--mc-rt-row-color:' + stripeColor + '">' +
        '<span class="mc-rt-stripe" aria-hidden="true"></span>' +
        '<span class="mc-rt-seq">' + (idx + 1) + '</span>' +
        '<span class="mc-rt-glyph" title="' + (p.role || 'unknown') + '">' + glyph + '</span>' +
        '<span class="mc-rt-name">' + name + obsChip + statusBadge + payloadBadge + '</span>' +
        '<span class="mc-rt-distlabel">' + distLabel + '</span>' +
        '<div class="mc-rt-distbar-wrap">' + distBar + '</div>' +
        '</li>';
    });

    var totalKm = dists.filter(function(d){return d!=null}).reduce(function(a,b){return a+b},0);
    var unresolvedCount = positions.filter(function(p){return p.resolved===false}).length;
    var multiPath = (opts && opts.multiPath) === true;
    var totalObservers = (opts && opts.totalObservers) || 1;
    var packetHash = (opts && opts.packetHash) || null;
    // packet-context block. Type chip + 3-5 facts above the
    // multi-path chip. opts.packetContext is set by the deep-link loader
    // after fetching /api/packets/<hash> and parsing decoded_json from the
    // chosen observation. Falls back to {} when absent (legacy sessionStorage
    // flow without packetContext).
    var pktCtx = (opts && opts.packetContext) || null;
    var contextBlock = buildPacketContextBlock(pktCtx);
    var uniquePathsCount = (opts && opts.allPaths) ? (function () {
      var seen = {};
      opts.allPaths.forEach(function (p) {
        // #1633 — collapse paths whose only difference is 1-byte hops when
        // the customizer toggle is ON. Aggregation uses the FILTERED hop
        // list so the picker count reflects the surviving distinct routes.
        // #1689 r1 (adv #1): if the filter strips EVERY hop the key becomes
        // an empty string and distinct all-1-byte routes collide into one
        // bucket. Fall back to the raw key with an `__all1byte__::` prefix
        // so they stay distinct while still being marked as filtered.
        var rawHops = (p.path || []);
        var hops = (typeof window !== 'undefined' && window.MC_filterPathHops)
          ? window.MC_filterPathHops(rawHops)
          : rawHops;
        var key = hops.length ? hops.join('-') : ('__all1byte__::' + rawHops.join('-'));
        seen[key] = true;
      });
      return Object.keys(seen).length;
    })() : 1;
    var multiPathChip = '';
    var pathPicker = '';
    if (multiPath) {
      multiPathChip = '<div class="mc-rt-multipath-chip">' +
        '<div><b>' + totalObservers + '</b> observers · <b>' + uniquePathsCount + '</b> unique paths</div>' +
        '<div class="mc-rt-multipath-key">thicker edge = more observers saw it</div>' +
        '</div>';
      // Group observers by their unique path-key so picker shows N unique
      // paths, each with the observer-count and a click-to-isolate affordance.
      var pathGroups = {};
      (opts.allPaths || []).forEach(function (p) {
        var rawHops = p.path || [];
        // #1633 — same render-time filter as the count above.
        // #1689 r1 (adv #1): empty filtered result must NOT collide with
        // other all-1-byte paths. Use a sentinel that includes the rawHops
        // signature so distinct routes stay separate buckets in the picker.
        var hops = (typeof window !== 'undefined' && window.MC_filterPathHops)
          ? window.MC_filterPathHops(rawHops)
          : rawHops;
        var k = hops.length ? hops.join('→') : ('⟨all-1byte⟩::' + rawHops.join('→'));
        var _bt = false;
        // #1784 — trust threshold: if any raw hop is below the configured
        // minimum hash bytes, mark for speculative display treatment.
        if (rawHops.length > 0 && typeof window !== 'undefined' && window.MC_meetsPathTrust) {
          for (var _bi = 0; _bi < rawHops.length; _bi++) {
            if (!window.MC_meetsPathTrust(rawHops[_bi])) { _bt = true; break; }
          }
        }
        if (!pathGroups[k]) pathGroups[k] = { key: k, observers: [], count: 0, allOneByte: hops.length === 0 && rawHops.length > 0, belowTrust: _bt, rawHops: rawHops };
        pathGroups[k].observers.push(p.observer || '?');
        pathGroups[k].count++;
      });
      var groupList = Object.values(pathGroups).sort(function (a, b) { return b.count - a.count; });
      var pickerRows = groupList.map(function (g, idx) {
        var sample = g.observers[0];
        var moreSuffix = g.observers.length > 1 ? ' +' + (g.observers.length - 1) : '';
        // #1689 r1 (kb #2): all-1-byte path → show a labeled chip instead of
        // an empty hop row so operators see WHY the route appears collapsed.
        var hopsHtml, hopsCount;
        if (g.allOneByte) {
          hopsCount = (g.rawHops || []).length;
          hopsHtml = '<span class="text-muted">— (' + hopsCount + ' × 1-byte filtered)</span>';
        } else {
          var hops = g.key.split('→').filter(function(s){return s.length>0;});
          hopsCount = hops.length;
          hopsHtml = hops.map(escapeHtml).join(' → ');
          // #1784 — trust threshold: append a speculative annotation when any
          // hop is below the configured minimum hash bytes for mapping.
          if (g.belowTrust) {
            var _tt = (typeof window !== 'undefined' && window.MC_getPathTrustThreshold) ? window.MC_getPathTrustThreshold() : 1;
            hopsHtml += ' <span class="text-muted" style="font-size:0.85em" title="One or more hops in this path have a shorter prefix than the configured ' + _tt + '-byte trust threshold. These observations count as speculative — raise pathTrust.minHashBytesForMapping in config.json for stricter evidence.">(speculative, <' + _tt + '-byte hops)</span>';
          }
        }
        return '<li class="mc-rt-path-row" data-path-key="' + escapeHtml(g.key) + '" data-obs-count="' + g.count + '" tabindex="0" role="button" aria-label="Isolate path with ' + hopsCount + ' hops, seen by ' + g.count + ' of ' + totalObservers + ' observers">' +
          '<span class="mc-rt-path-count">' + g.count + '/' + totalObservers + '</span>' +
          '<span class="mc-rt-path-hops">' + hopsHtml + '</span>' +
          '<span class="mc-rt-path-obs" title="' + escapeHtml(g.observers.join(', ')) + '">' + escapeHtml(sample) + moreSuffix + '</span>' +
        '</li>';
      }).join('');
      pathPicker = '<details class="mc-rt-paths" open><summary class="mc-rt-paths-header">' +
        '<svg class="ph-icon mc-rt-paths-chevron" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-down"/></svg>' +
        uniquePathsCount + ' unique paths · click to isolate' +
        '<button type="button" class="mc-rt-path-clear" aria-label="Show all paths">All</button>' +
        '</summary><ul class="mc-rt-path-list">' + pickerRows + '</ul></details>';
    }
    // "Back to packet" link when route was entered from a
    // specific packet — operators want fast round-trip without using browser
    // back (which doesn't reliably restore split-layout state on mobile).
    // Includes ?obs=<id> so the operator lands on the SAME observation they
    // launched the route from.
    var backLink = '';
    if (packetHash) {
      var obsId = (opts && opts.observationId) || null;
      // Try to infer from current URL if not passed
      if (!obsId) {
        try {
          var qs = (location.hash || '').split('?')[1];
          if (qs) obsId = new URLSearchParams(qs).get('obs');
        } catch (e) { console.warn('[route-view]', e); }
      }
      var backHref = '#/packets/' + escapeHtml(packetHash) + (obsId ? '?obs=' + escapeHtml(obsId) : '');
      backLink = '<a class="mc-rt-back-link" href="' + backHref +
        '" aria-label="Back to packet detail page' + (obsId ? ' (same observation)' : '') + '"' +
        ' title="Back to this packet\u2019s detail page' + (obsId ? ' (same observation)' : '') + '">' +
        '<span aria-hidden="true">←</span> Back to packet</a>';
    }
    var headerHtml =
      '<div class="mc-rt-header">' +
        '<div class="mc-rt-title-row">' +
          '<div class="mc-rt-title">Route</div>' +
          backLink +
        '</div>' +
        '<div class="mc-rt-meta">' + total + ' hops · ' + totalKm + ' km' +
          (unresolvedCount ? ' · ' + unresolvedCount + ' unresolved' : '') +
        '</div>' +
        contextBlock +
        multiPathChip +
        pathPicker +
        '<div class="mc-rt-spark-wrap">' + spark + '</div>' +
        '<button class="mc-rt-close" aria-label="Close route view" type="button"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button>' +
      '</div>';

    // origin row pinned at top, dest at bottom; middle scrollable
    var originRow = rows[0] || '';
    var destRow = rows[total - 1] || '';
    var middleRows = rows.slice(1, -1).join('');

    var bodyHtml =
      '<div class="mc-rt-pinned mc-rt-pinned-top">' + originRow + '</div>' +
      '<ul class="mc-rt-list" role="list">' + middleRows + '</ul>' +
      '<div class="mc-rt-pinned mc-rt-pinned-bottom">' + destRow + '</div>';

    var sidebar = document.createElement('aside');
    sidebar.className = 'mc-rt-sidebar';
    sidebar.setAttribute('role', 'region');
    sidebar.setAttribute('aria-label', 'Route timeline');
    // Desktop: restore saved width from localStorage
    try {
      var savedW = parseInt(localStorage.getItem('mc-rt-sidebar-width'), 10);
      if (savedW && savedW >= 200 && savedW <= 700) {
        sidebar.style.width = savedW + 'px';
      }
    } catch (e) { console.warn('[route-view]', e); }
    // Mobile: collapsible bottom-sheet. Drag handle + compact summary in
    // collapsed state. Tap handle to expand → full content. Per operator
    // feedback, collapsed summary shows: type, hop count, total km, hex route.
    var summaryLine = '';
    var typeTag = (pktCtx && pktCtx.type) ? pktCtx.type.replace('_',' ') : '';
    var hexRoute = '';
    try {
      // Hex route = the raw path bytes from the chosen observation. Prefer
      // opts.allPaths[].path (already the raw 1-byte prefixes from the wire);
      // fall back to canonicalPath if needed.
      if (opts.allPaths && opts.allPaths.length) {
        var bestMatch = opts.allPaths.find(function (p) {
          return Array.isArray(p.path) && opts.canonicalPath &&
            p.path.length === opts.canonicalPath.length;
        }) || opts.allPaths[0];
        hexRoute = (bestMatch.path || []).map(function (h) {
          return String(h).slice(0, 2).toUpperCase();
        }).join('→');
      } else if (Array.isArray(opts.canonicalPath) && opts.canonicalPath.length) {
        hexRoute = opts.canonicalPath.map(function (h) {
          return String(h).slice(0, 2).toUpperCase();
        }).join('→');
      }
    } catch (e) { console.warn('[route-view]', e); }
    summaryLine = (typeTag ? '<b>' + escapeHtml(typeTag) + '</b> · ' : '') +
                  total + ' hops · ' + totalKm + ' km' +
                  (multiPath ? ' · ' + totalObservers + ' obs' : '') +
                  (hexRoute ? '<br><span class="mc-rt-mobile-hex">' + escapeHtml(hexRoute) + '</span>' : '');
    sidebar.innerHTML =
      // Desktop: resize handle on the right edge + collapse button.
      '<div class="mc-rt-resize-handle" role="separator" aria-label="Resize route panel" aria-orientation="vertical" tabindex="0"></div>' +
      '<button type="button" class="mc-rt-collapse-btn" aria-label="Collapse route panel" title="Collapse route panel"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-left"/></svg></button>' +
      '<div class="mc-rt-collapsed-label" aria-hidden="true">ROUTE</div>' +
      // Mobile: bottom-sheet header (summary + chevron). No drag-grip —
      // conflicted with browser pull-to-refresh and CoreScope's own pull-to-
      // reconnect gesture. Tap the chevron / summary to expand instead.
      '<div class="mc-rt-mobile-handle" role="button" tabindex="0" aria-label="Expand route details" aria-expanded="false">' +
        '<div class="mc-rt-mobile-summary">' + summaryLine + '</div>' +
        '<div class="mc-rt-mobile-chevron" aria-hidden="true">⌃</div>' +
      '</div>' +
      headerHtml + bodyHtml;
    // Wire desktop collapse button (per-session, not persisted)
    var collapseBtn = sidebar.querySelector('.mc-rt-collapse-btn');
    if (collapseBtn) {
      collapseBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        var collapsed = sidebar.classList.toggle('mc-rt-collapsed');
        collapseBtn.setAttribute('aria-label', collapsed ? 'Expand route panel' : 'Collapse route panel');
        collapseBtn.setAttribute('title', collapsed ? 'Expand route panel' : 'Collapse route panel');
        // #1648 M4: swap Phosphor sprite glyph (caret-right when collapsed,
        // caret-left when expanded). Replaces prior ▶/◀ Misc-Symbols chars. // EMOJI-OK: comment
        collapseBtn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#' +
          (collapsed ? 'ph-caret-right' : 'ph-caret-left') + '"/></svg>';
        setTimeout(function () { if (mapRef && mapRef.invalidateSize) mapRef.invalidateSize(); }, 280);
      });
    }
    // Collapsed-state click-to-expand on the vertical "ROUTE" label
    var collapsedLabel = sidebar.querySelector('.mc-rt-collapsed-label');
    if (collapsedLabel) {
      collapsedLabel.addEventListener('click', function () {
        if (sidebar.classList.contains('mc-rt-collapsed')) {
          sidebar.classList.remove('mc-rt-collapsed');
          if (collapseBtn) {
            collapseBtn.setAttribute('aria-label', 'Collapse route panel');
            collapseBtn.innerHTML = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-left"/></svg>';
          }
          setTimeout(function () { if (mapRef && mapRef.invalidateSize) mapRef.invalidateSize(); }, 280);
        }
      });
    }
    // Wire desktop resize handle (drag, persist to localStorage)
    var resizeHandle = sidebar.querySelector('.mc-rt-resize-handle');
    if (resizeHandle) {
      var startX = 0, startW = 0, dragging = false;
      resizeHandle.addEventListener('mousedown', function (e) {
        if (window.innerWidth <= 767) return; // mobile: no resize
        dragging = true;
        startX = e.clientX;
        startW = sidebar.getBoundingClientRect().width;
        document.body.style.userSelect = 'none';
        e.preventDefault();
      });
      document.addEventListener('mousemove', function (e) {
        if (!dragging) return;
        var newW = startW + (e.clientX - startX);
        newW = Math.max(220, Math.min(700, newW));
        sidebar.style.width = newW + 'px';
        // Throttled invalidate so map keeps pace with the drag
        if (!resizeHandle._raf) {
          resizeHandle._raf = requestAnimationFrame(function () {
            try { mapRef.invalidateSize(); } catch (_) {}
            resizeHandle._raf = null;
          });
        }
      });
      document.addEventListener('mouseup', function () {
        if (!dragging) return;
        dragging = false;
        document.body.style.userSelect = '';
        try { localStorage.setItem('mc-rt-sidebar-width', String(parseInt(sidebar.style.width, 10) || 320)); } catch (e) {}
      });
    }
    // Wire mobile expand/collapse
    var handle = sidebar.querySelector('.mc-rt-mobile-handle');
    if (handle) {
      handle.addEventListener('click', function () {
        var expanded = sidebar.classList.toggle('mc-rt-mobile-expanded');
        document.body.classList.toggle('mc-rt-mobile-sheet-expanded', expanded);
        handle.setAttribute('aria-expanded', String(expanded));
        // Force map to recompute size + re-fit after sheet animation settles
        setTimeout(function () {
          try {
            if (mapRef && typeof mapRef.invalidateSize === 'function') mapRef.invalidateSize();
            // Re-fit to current positions so the route stays centered as the
            // map dimensions change with sheet expand/collapse.
            var fitPts = positions.filter(function(p){return p.lat!=null});
            if (fitPts.length === 1) {
              mapRef.setView([fitPts[0].lat, fitPts[0].lon], 11, { animate: false });
            } else if (fitPts.length >= 2) {
              var isMob = window.innerWidth <= 767;
              mapRef.fitBounds(L.latLngBounds(fitPts.map(function(p){return [p.lat, p.lon]})), isMob ? { paddingTopLeft: [30, 70], paddingBottomRight: [30, 130], maxZoom: 14 } : { padding: [40, 40], maxZoom: 14 });
            }
          } catch (e) { console.warn('[route-view]', e); }
        }, 280);
      });
      handle.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handle.click(); }
      });
    }

    // Wire hover/focus on rows
    var rowEls = sidebar.querySelectorAll('.mc-rt-row');
    function highlightHop(idx, on) {
      var mk = markers[idx];
      if (mk && mk._icon) mk._icon.classList.toggle('mc-rt-hover', on);
      // edges around this hop
      if (idx > 0 && edges[idx-1]) edges[idx-1].setStyle({ weight: on ? 6 : 3.5, opacity: on ? 1 : 0.85 });
      if (idx < edges.length && edges[idx]) edges[idx].setStyle({ weight: on ? 6 : 3.5, opacity: on ? 1 : 0.85 });
    }
    function scrollRowIntoView(idx) {
      var row = sidebar.querySelector('.mc-rt-row[data-hop-idx="' + idx + '"]');
      if (!row) return;
      row.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
      rowEls.forEach(function (r) { r.classList.remove('mc-rt-row-active'); });
      row.classList.add('mc-rt-row-active');
      setTimeout(function () { row.classList.remove('mc-rt-row-active'); }, 1500);
    }
    // Expose so marker-click can call it from render()
    sidebar._highlightHop = highlightHop;
    sidebar._scrollRowIntoView = scrollRowIntoView;
    // Polish review (dijkstra #1423): centralized row-wireup helper. The
    // sidebar isolate→restore round-trip nukes child listeners via
    // innerHTML, so the restore path needs to re-wire. Previously the
    // re-wire lived inline inside restoreAllPaths' closure — a future
    // refactor adding a new row listener type could easily forget the
    // restore-side rewire and silently regress. Funneling both paths
    // through wireRow eliminates that risk.
    function wireRow(row) {
      var idx = parseInt(row.dataset.hopIdx, 10);
      if (isNaN(idx)) return;
      row.addEventListener('mouseenter', function () { highlightHop(idx, true); });
      row.addEventListener('mouseleave', function () { highlightHop(idx, false); });
      row.addEventListener('focus', function () { highlightHop(idx, true); });
      row.addEventListener('blur', function () { highlightHop(idx, false); });
      row.addEventListener('click', function (e) {
        // Allow clicks on links INSIDE the expanded detail panel to navigate
        // normally. Without this guard, the row-level stopPropagation +
        // preventDefault below would eat anchor clicks.
        if (e.target && e.target.closest && e.target.closest('a.mc-rt-detail-link, .mc-rt-detail-panel a, .mc-rt-detail-panel button')) {
          return;
        }
        e.stopPropagation();
        e.preventDefault();
        // Toggle drill-in panel (expanding row)
        var existing = row.querySelector('.mc-rt-detail-panel');
        if (existing) {
          existing.remove();
          row.classList.remove('mc-rt-row-expanded');
          return;
        }
        // Close any other open panels first
        sidebar.querySelectorAll('.mc-rt-detail-panel').forEach(function (el) { el.remove(); });
        sidebar.querySelectorAll('.mc-rt-row-expanded').forEach(function (el) { el.classList.remove('mc-rt-row-expanded'); });
        var panel = document.createElement('div');
        panel.className = 'mc-rt-detail-panel';
        row.appendChild(panel);
        row.classList.add('mc-rt-row-expanded');
        renderHopDetail(positions[idx], panel);
        // Also fly map to the hop
        var p = positions[idx];
        if (p.lat != null && p.lon != null) mapRef.flyTo([p.lat, p.lon], 13, { duration: 0.6 });
      });
    }
    // Expose on sidebar so restoreAllPaths (defined later) can re-use it.
    sidebar._wireRow = wireRow;
    rowEls.forEach(wireRow);

    // Path picker (multi-path mode) — clicking a path REPLACES the
    // canonical edges + markers with the selected path's own. Stroke-width
    // stays = global observer count per edge. "All" restores the union view.
    // Hides ALL canonical markers + edges to avoid phantom polylines.
    function isolatePath(pathKey) {
      if (!pathKey) return restoreAllPaths();
      var prefixes = pathKey.split('\u2192').filter(function(s){return s.length>0;});
      if (!prefixes.length) return restoreAllPaths();
      function findPosByPrefix(pre) {
        var preLow = pre.toLowerCase();
        for (var k = 0; k < positions.length; k++) {
          var pk = positions[k].pubkey;
          if (!pk) continue;
          var pkLow = String(pk).toLowerCase();
          if (pkLow === preLow || pkLow.startsWith(preLow) || preLow.startsWith(pkLow)) return positions[k];
        }
        return null;
      }
      function resolveUnknownPrefix(pre) {
        if (!window.__mc_nodes && !Array.isArray(window.nodes)) return null;
        var nodes = window.__mc_nodes || window.nodes || [];
        var preLow = pre.toLowerCase();
        var match = nodes.find(function (n) {
          var pk = (n.public_key || '').toLowerCase();
          return pk === preLow || pk.startsWith(preLow) || preLow.startsWith(pk);
        });
        if (match && match.lat != null && match.lon != null && !(match.lat === 0 && match.lon === 0)) {
          return { lat: match.lat, lon: match.lon, name: match.name, pubkey: match.public_key, role: match.role, resolved: true, _adhoc: true };
        }
        return null;
      }
      var iso = [];
      if (positions[0] && positions[0].isOrigin) iso.push(positions[0]);
      prefixes.forEach(function (pre) {
        var p = findPosByPrefix(pre);
        if (!p) p = resolveUnknownPrefix(pre);
        if (p && iso.indexOf(p) === -1) iso.push(p);
      });
      var lastPos = positions[positions.length - 1];
      if (lastPos && lastPos.isDest && iso.indexOf(lastPos) === -1) iso.push(lastPos);

      // FULL CLEAR: hide every canonical edge + marker + sequence label.
      edges.forEach(function (poly) { if (poly) poly.setStyle({ opacity: 0 }); });
      markers.forEach(function (mk) { if (mk && mk._icon) mk._icon.style.opacity = '0'; });
      // Tear down ALL previous overlays: isolation (re-click) + union (after All)
      if (sidebar._isoEdges) sidebar._isoEdges.forEach(function (e) { try { e.remove(); } catch (_) {} });
      if (sidebar._isoMarkers) sidebar._isoMarkers.forEach(function (m) { try { m.remove(); } catch (_) {} });
      if (sidebar._unionOverlay) sidebar._unionOverlay.forEach(function (e) { try { e.remove(); } catch (_) {} });
      if (sidebar._unionMarkers) sidebar._unionMarkers.forEach(function (m) { try { m.remove(); } catch (_) {} });
      sidebar._isoEdges = [];
      sidebar._isoMarkers = [];
      sidebar._unionOverlay = [];
      sidebar._unionMarkers = [];

      var localEdgeCounts = opts.edgeCounts || {};
      var localTotalObs = opts.totalObservers || 1;
      // Draw new edges for the selected path
      for (var i = 0; i < iso.length - 1; i++) {
        var a = iso[i], b = iso[i + 1];
        if (a.lat == null || b.lat == null) continue;
        var fromPre = String(a.pubkey || '').slice(0, 2).toUpperCase();
        var toPre = String(b.pubkey || '').slice(0, 2).toUpperCase();
        var matchCount = 1;
        Object.keys(localEdgeCounts).forEach(function (k) {
          var parts = k.split('\u2192');
          if (parts.length !== 2) return;
          var ka = parts[0].toUpperCase(), kb = parts[1].toUpperCase();
          if ((ka === fromPre || fromPre.startsWith(ka)) &&
              (kb === toPre || toPre.startsWith(kb))) {
            matchCount = Math.max(matchCount, localEdgeCounts[k]);
          }
        });
        var ratio = matchCount / localTotalObs;
        var w = 2 + ratio * 6;
        var color = rampColor(i, iso.length - 1);
        var poly = L.polyline([[a.lat, a.lon], [b.lat, b.lon]], {
          color: color, weight: w, opacity: 0.95,
          className: 'mc-rt-edge mc-rt-edge-iso'
        }).addTo(layer);
        sidebar._isoEdges.push(poly);
      }
      // Draw new markers for selected path (numbered 1..iso.length)
      iso.forEach(function (p, i) {
        if (p.lat == null || p.lon == null) return;
        var c = (window.ROLE_COLORS && window.ROLE_COLORS[p.role]) || '#3b82f6';
        var ep = { isOrigin: i === 0, isDest: i === iso.length - 1, resolved: true };
        var built = buildMarkerSVG(ep, { color: c, seqNum: i + 1 });
        var icon = L.divIcon({
          html: '<div class="mc-rt-marker mc-rt-iso-marker" aria-label="Hop ' + (i+1) + ' of ' + iso.length + ', ' + escapeHtml(p.name || '?') + '">' + built.html + '</div>',
          className: 'mc-rt-marker-icon',
          iconSize: [built.size + 4, built.size + 4],
          iconAnchor: [(built.size + 4)/2, (built.size + 4)/2]
        });
        var mk = L.marker([p.lat, p.lon], { icon: icon }).addTo(layer);
        mk.bindTooltip('hop ' + (i+1) + ' · ' + escapeHtml(p.name || '?'), { direction: 'top', offset: [0, -10] });
        sidebar._isoMarkers.push(mk);
      });
      // Fit bounds to selected path. Defer + invalidateSize for mobile,
      // where the map container may not have its final dimensions yet
      // when the operator clicks. Re-fit twice (immediate + delayed) so
      // the route always lands centered.
      var coordsForFit = iso.filter(function(p){return p.lat!=null}).map(function(p){return [p.lat, p.lon]});
      function doFit() {
        try {
          mapRef.invalidateSize();
          if (coordsForFit.length === 1) {
            mapRef.setView(coordsForFit[0], 11, { animate: false });
          } else if (coordsForFit.length >= 2) {
            var isMob2 = window.innerWidth <= 767;
            mapRef.fitBounds(L.latLngBounds(coordsForFit), isMob2
              ? { paddingTopLeft: [30, 70], paddingBottomRight: [30, 190], maxZoom: 13 }
              : { padding: [40, 40], maxZoom: 13 });
          }
        } catch (e) { console.warn('[route-view]', e); }
      }
      if (coordsForFit.length > 0) {
        // Polish review (carmack/doshi/tufte): single rAF replaces the prior
        // 3-staggered (0/200/600/1400ms) fit storm. The ResizeObserver wired
        // in render() catches any subsequent layout settles.
        requestAnimationFrame(doFit);
      }
      // Re-fan spider on the new isolated markers
      if (sidebar._respider) sidebar._respider();

      // when isolating, REPLACE the sidebar hop list with the
      // isolated path's hops so the km-from-prev distances stay correct for
      // what's visible on the map. Restore canonical list on "All".
      var listEl = sidebar.querySelector('.mc-rt-list');
      var pinnedTop = sidebar.querySelector('.mc-rt-pinned-top');
      var pinnedBottom = sidebar.querySelector('.mc-rt-pinned-bottom');
      if (listEl) {
        // Save original rows once for later restore
        if (!sidebar._canonRows) {
          sidebar._canonRows = {
            top: pinnedTop ? pinnedTop.innerHTML : '',
            bottom: pinnedBottom ? pinnedBottom.innerHTML : '',
            middle: listEl.innerHTML
          };
        }
        // Compute distances + render rows for the isolated path
        var isoDists = [];
        var isoMaxDist = 0;
        for (var di = 1; di < iso.length; di++) {
          var d = haversineKm(iso[di-1], iso[di]);
          isoDists.push(d);
          if (d != null && d > isoMaxDist) isoMaxDist = d;
        }
        function rowFor(p, idx) {
          var dist = idx > 0 ? isoDists[idx - 1] : null;
          var distBar = '';
          if (dist != null && isoMaxDist > 0) {
            var pct = Math.max(2, (dist / isoMaxDist) * 100);
            distBar = '<div class="mc-rt-distbar" style="width:' + pct.toFixed(1) + '%;background:' + rampColor(idx - 1, isoDists.length) + '"></div>';
          }
          var distLabel = dist != null ? dist + ' km' : '–';
          var pinned = idx === 0 ? 'origin' : (idx === iso.length - 1 ? 'dest' : '');
          var glyph = roleGlyph(p.role);
          var name = escapeHtml(p.name || (p.pubkey ? String(p.pubkey).slice(0,8) : '?'));
          var stripeColor = iso.length > 1 ? rampColor(idx === 0 ? 0 : idx - 1, iso.length - 1) : 'transparent';
          return '<li class="mc-rt-row mc-rt-row-iso ' + pinned + '" tabindex="0" style="--mc-rt-row-color:' + stripeColor + '">' +
            '<span class="mc-rt-stripe" aria-hidden="true"></span>' +
            '<span class="mc-rt-seq">' + (idx + 1) + '</span>' +
            '<span class="mc-rt-glyph" title="' + (p.role || '?') + '">' + glyph + '</span>' +
            '<span class="mc-rt-name">' + name + '</span>' +
            '<span class="mc-rt-distlabel">' + distLabel + '</span>' +
            '<div class="mc-rt-distbar-wrap">' + distBar + '</div>' +
            '</li>';
        }
        if (iso.length === 0) return;
        if (pinnedTop) pinnedTop.innerHTML = rowFor(iso[0], 0);
        if (pinnedBottom) pinnedBottom.innerHTML = iso.length > 1 ? rowFor(iso[iso.length - 1], iso.length - 1) : '';
        listEl.innerHTML = iso.slice(1, -1).map(function (p, k) { return rowFor(p, k + 1); }).join('');
      }
    }
    function restoreAllPaths() {
      if (sidebar._isoEdges) {
        sidebar._isoEdges.forEach(function (e) { try { e.remove(); } catch (_) {} });
        sidebar._isoEdges = [];
      }
      if (sidebar._isoMarkers) {
        sidebar._isoMarkers.forEach(function (m) { try { m.remove(); } catch (_) {} });
        sidebar._isoMarkers = [];
      }
      if (sidebar._unionOverlay) {
        sidebar._unionOverlay.forEach(function (e) { try { e.remove(); } catch (_) {} });
        sidebar._unionOverlay = [];
      }
      if (sidebar._unionMarkers) {
        sidebar._unionMarkers.forEach(function (m) { try { m.remove(); } catch (_) {} });
        sidebar._unionMarkers = [];
      }
      // Restore canonical sidebar rows
      if (sidebar._canonRows) {
        var listEl2 = sidebar.querySelector('.mc-rt-list');
        var pinnedTop2 = sidebar.querySelector('.mc-rt-pinned-top');
        var pinnedBottom2 = sidebar.querySelector('.mc-rt-pinned-bottom');
        if (pinnedTop2) pinnedTop2.innerHTML = sidebar._canonRows.top;
        if (pinnedBottom2) pinnedBottom2.innerHTML = sidebar._canonRows.bottom;
        if (listEl2) listEl2.innerHTML = sidebar._canonRows.middle;
        var newRowEls = sidebar.querySelectorAll('.mc-rt-row');
        // Polish review (dijkstra #1423): centralized wireRow ensures the
        // restore path picks up every listener type the initial render
        // attached (mouseenter/leave/focus/blur/click) — no risk of a
        // future row-listener type being added to the initial path and
        // forgotten here.
        newRowEls.forEach(sidebar._wireRow || function () {});
      }

      // Union-of-edges (NOT per-path). Iterate edgeCounts once;
      // draw EACH unique edge as a single polyline; stroke-width = count.
      // No sequence numbers (multiple paths means no single sequence).
      if (multiPath && opts.edgeCounts) {
        // Hide canonical edges + markers
        edges.forEach(function (poly) { if (poly) poly.setStyle({ opacity: 0 }); });
        markers.forEach(function (mk) { if (mk && mk._icon) mk._icon.style.opacity = '0'; });
        // Build position lookup by 2-char prefix → {lat,lon,pubkey,name,role}
        function resolvePrefix(pre) {
          var preLow = pre.toLowerCase();
          for (var k = 0; k < positions.length; k++) {
            var pk = positions[k].pubkey;
            if (!pk) continue;
            var pkLow = String(pk).toLowerCase();
            if (pkLow === preLow || pkLow.startsWith(preLow) || preLow.startsWith(pkLow)) {
              if (positions[k].lat != null) return positions[k];
            }
          }
          if (window.__mc_nodes) {
            var nodes = window.__mc_nodes;
            var m = nodes.find(function (n) {
              var p = (n.public_key || '').toLowerCase();
              return p === preLow || p.startsWith(preLow) || preLow.startsWith(p);
            });
            if (m && m.lat != null && m.lon != null && !(m.lat === 0 && m.lon === 0)) {
              return { lat: m.lat, lon: m.lon, pubkey: m.public_key, name: m.name, role: m.role };
            }
          }
          return null;
        }
        sidebar._unionOverlay = [];
        sidebar._unionMarkers = [];
        // Track unique nodes seen (for marker rendering)
        var uniqueNodes = {};
        Object.keys(opts.edgeCounts).forEach(function (k) {
          var parts = k.split('\u2192');
          if (parts.length !== 2) return;
          var aPre = parts[0].toUpperCase(), bPre = parts[1].toUpperCase();
          var aPos = resolvePrefix(aPre);
          var bPos = resolvePrefix(bPre);
          if (!aPos || !bPos) return;
          uniqueNodes[aPre] = aPos;
          uniqueNodes[bPre] = bPos;
          var count = opts.edgeCounts[k];
          var ratio = count / (opts.totalObservers || 1);
          var w = 2 + ratio * 6; // 2..8
          // Single color (no gradient — sequence has no meaning here)
          var poly = L.polyline([[aPos.lat, aPos.lon], [bPos.lat, bPos.lon]], {
            color: 'var(--accent, #06b6d4)',
            weight: w,
            opacity: 0.7,
            className: 'mc-rt-edge mc-rt-edge-union'
          }).addTo(layer);
          // Convert CSS var to inline (Leaflet doesn't resolve CSS vars in stroke)
          var c = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() || '#06b6d4';
          poly.setStyle({ color: c });
          sidebar._unionOverlay.push(poly);
        });
        // Draw small plain markers (NO seq numbers, NO role colors) at each
        // unique node. Single accent color matches the edges — role isn't
        // the story in union view; consensus/divergence is.
        Object.keys(uniqueNodes).forEach(function (pre) {
          var n = uniqueNodes[pre];
          var accent = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() || '#06b6d4';
          var html = '<div class="mc-rt-marker mc-rt-marker-union" aria-label="' + escapeHtml(n.name || pre) + '">' +
            '<svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">' +
              '<circle cx="7" cy="7" r="4" fill="' + accent + '" stroke="#fff" stroke-width="1"/>' +
            '</svg></div>';
          var icon = L.divIcon({
            html: html, className: 'mc-rt-marker-icon',
            iconSize: [18, 18], iconAnchor: [9, 9]
          });
          var mk = L.marker([n.lat, n.lon], { icon: icon }).addTo(layer);
          mk.bindTooltip(escapeHtml(n.name || pre), { direction: 'top', offset: [0, -8] });
          sidebar._unionMarkers.push(mk);
        });
      } else {
        // Single-path mode: just restore canonical edges + markers
        edges.forEach(function (poly, i) {
          if (!poly) return;
          poly.setStyle({ opacity: 0.85, weight: 5 });
        });
        markers.forEach(function (mk) { if (mk && mk._icon) mk._icon.style.opacity = '1'; });
      }
      // Re-fan spider on whatever marker set is active now
      if (sidebar._respider) sidebar._respider();
      // Re-fit the map to whatever is now drawn (union nodes can extend
      // well beyond the canonical path's bounds). Stagger to survive any
      // CSS reflow / mobile URL-bar resize that may follow.
      function _restoreFit() {
        try {
          mapRef.invalidateSize();
          var bounds = null;
          if (layer && typeof layer.eachLayer === 'function') {
            layer.eachLayer(function (child) {
              try {
                if (child.getLatLng) {
                  var ll = child.getLatLng();
                  if (!bounds) bounds = L.latLngBounds(ll, ll); else bounds.extend(ll);
                } else if (child.getLatLngs) {
                  var lls = child.getLatLngs();
                  (function walk(x) {
                    if (!x) return;
                    if (Array.isArray(x)) x.forEach(walk);
                    else if (x.lat != null) {
                      if (!bounds) bounds = L.latLngBounds(x, x); else bounds.extend(x);
                    }
                  })(lls);
                }
              } catch (e) { console.warn('[route-view]', e); }
            });
          }
          if (bounds && bounds.isValid()) {
            var isMob3 = window.innerWidth <= 767;
            mapRef.fitBounds(bounds, isMob3
              ? { paddingTopLeft: [30, 70], paddingBottomRight: [30, 190], maxZoom: 14 }
              : { padding: [40, 40], maxZoom: 14 });
          }
        } catch (e) { console.warn('[route-view]', e); }
      }
      _restoreFit();
      requestAnimationFrame(_restoreFit);
    }
    var pathRows = sidebar.querySelectorAll('.mc-rt-path-row');
    pathRows.forEach(function (row) {
      row.addEventListener('click', function () {
        pathRows.forEach(function (r) { r.classList.remove('mc-rt-path-active'); });
        row.classList.add('mc-rt-path-active');
        isolatePath(row.dataset.pathKey);
      });
      row.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); row.click(); }
      });
    });
    var clearBtn = sidebar.querySelector('.mc-rt-path-clear');
    if (clearBtn) {
      clearBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        e.preventDefault();
        pathRows.forEach(function (r) { r.classList.remove('mc-rt-path-active'); });
        restoreAllPaths();
      });
    }

    // Sparkline interactivity
    var sparkDots = sidebar.querySelectorAll('.mc-rt-spark-dot');
    var tipEl = null;
    function showTip(evt, text) {
      if (!tipEl) {
        tipEl = document.createElement('div');
        tipEl.className = 'mc-rt-spark-tooltip';
        document.body.appendChild(tipEl);
      }
      tipEl.textContent = text;
      tipEl.style.left = (evt.clientX + 10) + 'px';
      tipEl.style.top = (evt.clientY - 24) + 'px';
      tipEl.style.display = 'block';
    }
    function hideTip() { if (tipEl) tipEl.style.display = 'none'; }
    sparkDots.forEach(function (dot) {
      var hopIdx = parseInt(dot.dataset.hopIdx, 10); // 1-based (hop N from prev)
      var dist = dot.dataset.dist;
      dot.addEventListener('mouseenter', function (e) {
        showTip(e, 'hop ' + (hopIdx + 1) + ' · ' + (dist ? dist + ' km from prev' : '–'));
        highlightHop(hopIdx, true);
      });
      dot.addEventListener('mousemove', function (e) { if (tipEl && tipEl.style.display !== 'none') { tipEl.style.left = (e.clientX + 10) + 'px'; tipEl.style.top = (e.clientY - 24) + 'px'; } });
      dot.addEventListener('mouseleave', function () { hideTip(); highlightHop(hopIdx, false); });
      dot.addEventListener('click', function () {
        var p = positions[hopIdx];
        if (p && p.lat != null) mapRef.flyTo([p.lat, p.lon], 13, { duration: 0.6 });
        scrollRowIntoView(hopIdx);
      });
    });

    // Close handler
    var closeBtn = sidebar.querySelector('.mc-rt-close');
    if (closeBtn) {
      closeBtn.addEventListener('click', function () {
        try { sessionStorage.removeItem('map-route-hops'); } catch (e) {}
        sidebar.remove();
        if (layer && layer.clearLayers) layer.clearLayers();
        document.body.classList.remove('mc-route-active');
        // Setting hash fires hashchange which would also trigger teardown,
        // but that path already exited because mc-route-active is gone.
        location.hash = '#/map';
        // Leaflet cached width while sidebar was open — force re-measure.
        if (mapRef && typeof mapRef.invalidateSize === 'function') {
          setTimeout(function () { mapRef.invalidateSize(); }, 50);
        }
      });
    }

    return sidebar;
  }

  function render(mapRef, layer, positions, opts) {
    if (!positions || !positions.length) return;
    opts = opts || {};
    var total = positions.length;

    // Mark origin/destination
    positions.forEach(function (p, i) {
      p.isOrigin = (i === 0);
      p.isDest = (i === total - 1);
    });

    document.body.classList.add('mc-route-active');

    // Auto-collapse the Map Controls panel on route view entry (it covers
    // ~20% of map otherwise). The toggle button stays available so operator
    // can re-expand if needed.
    try {
      var mcPanel = document.getElementById('mapControls');
      var mcToggle = document.getElementById('mapControlsToggle');
      if (mcPanel && !mcPanel.classList.contains('collapsed')) {
        mcPanel.classList.add('collapsed');
        if (mcToggle) mcToggle.setAttribute('aria-expanded', 'false');
      }
    } catch (e) { console.warn('[route-view]', e); }

    // #1418: tear down route view when navigating away from /#/map?route=*.
    // Without this hashchange listener, clicking nav 'Map' from inside the route
    // view leaves the sidebar + route layer + body class up forever, hiding the
    // normal mesh markers and Map Controls.
    function teardownIfNavigatedAway() {
      var h = location.hash || '';
      // Active routes are #/map?packet=<hash>&obs=<id> (deep-link, #1419)
      // OR legacy #/map?route=1 (sessionStorage flow).
      var stillRoute = /^#\/map(\?|$|#)/.test(h) && /[?&](packet|route)=/.test(h);
      // Plain #/map should NOT keep route active; user clicked Map nav.
      if (!stillRoute) {
        if (layer && layer.clearLayers) layer.clearLayers();
        document.querySelectorAll('.mc-rt-sidebar').forEach(function (el) { el.remove(); });
        document.body.classList.remove('mc-route-active');
        try { sessionStorage.removeItem('map-route-hops'); } catch (e) {}
        window.removeEventListener('hashchange', teardownIfNavigatedAway);
        // Polish review (carmack/munger): drop the resize listener + ResizeObserver
        // wired in render() so they don't accumulate across nav cycles.
        try {
          if (window.__mc_routeResizeRefit) {
            window.removeEventListener('resize', window.__mc_routeResizeRefit);
            window.__mc_routeResizeRefit = null;
          }
        } catch (e) { console.warn('[route-view] resize teardown:', e); }
        try {
          if (window.__mc_routeResizeObserver && window.__mc_routeResizeObserver.disconnect) {
            window.__mc_routeResizeObserver.disconnect();
            window.__mc_routeResizeObserver = null;
          }
        } catch (e) { console.warn('[route-view] ResizeObserver teardown:', e); }
        // Polish review (carmack): clear the unbounded _detailCache (it grew
        // for the lifetime of the tab; bounded now to per-route session).
        try { if (typeof _detailCache !== 'undefined') _detailCache.clear && _detailCache.clear(); } catch (e) {}
        // Leaflet cached its container width while sidebar was open; force a
        // re-measure so markers/tiles re-render at full width.
        if (mapRef && typeof mapRef.invalidateSize === 'function') {
          // Wait a tick for CSS to recompute (left:320px → unset).
          setTimeout(function () { mapRef.invalidateSize(); }, 50);
        }
      }
    }
    window.removeEventListener('hashchange', window.__mc_routeTeardown || function(){});
    window.__mc_routeTeardown = teardownIfNavigatedAway;
    window.addEventListener('hashchange', teardownIfNavigatedAway);

    // #1418 Phase E — relive the ramp on CB-preset / theme change. Walks the
    // existing edges / stripes / sparkline dots and re-applies rampColor()
    // which now reads --mc-rt-ramp-* CSS vars set by cb-presets.js.
    // Polish review (torvalds #1423): scope to the current sidebar/map layer
    // rather than walking document. If the page ever embeds a second route
    // surface (e.g. a preview thumbnail), the recolor would otherwise paint
    // both. `sidebar` is the active root captured by this render() closure;
    // `layer` is the active route LayerGroup on the map.
    function recolorRoute() {
      var edgeEls = [];
      try {
        if (layer && typeof layer.eachLayer === 'function') {
          layer.eachLayer(function (child) {
            try {
              var el = child && child._path;
              if (el && el.classList && el.classList.contains('mc-rt-edge')) {
                edgeEls.push(el);
              }
            } catch (e) { console.warn('[route-view] recolor edge walk:', e); }
          });
        }
      } catch (e) { console.warn('[route-view] recolor layer walk:', e); }
      var n = edgeEls.length;
      edgeEls.forEach(function (el, i) {
        var c = rampColor(i, n);
        el.setAttribute('stroke', c);
        el.style.color = c;
      });
      // Sidebar row stripes + distance bars — scoped to THIS sidebar.
      var rows = sidebar ? sidebar.querySelectorAll('.mc-rt-row') : [];
      var rowCount = rows.length;
      rows.forEach(function (row, idx) {
        var stripeIdx = idx === 0 ? 0 : (idx - 1);
        var c = rowCount > 1 ? rampColor(stripeIdx, rowCount - 1) : 'transparent';
        row.style.setProperty('--mc-rt-row-color', c);
        var bar = row.querySelector('.mc-rt-distbar');
        if (bar) bar.style.background = c;
      });
      // Sparkline dots — scoped to THIS sidebar.
      var dots = sidebar ? sidebar.querySelectorAll('.mc-rt-spark-dot') : [];
      var dotCount = dots.length;
      dots.forEach(function (d, i) { d.setAttribute('fill', rampColor(i, dotCount)); });
    }
    function recolorHandler() { try { recolorRoute(); } catch (e) {} }
    window.removeEventListener('cb-preset-changed', window.__mc_routeRecolor || function(){});
    window.removeEventListener('theme-changed', window.__mc_routeRecolor || function(){});
    window.__mc_routeRecolor = recolorHandler;
    window.addEventListener('cb-preset-changed', recolorHandler);
    window.addEventListener('theme-changed', recolorHandler);

    // Edges. If a hop is unresolved (no lat/lon), bridge across it by drawing
    // a dashed line from the previous resolved hop to the next resolved hop —
    // otherwise the route appears truncated everywhere an intermediate is
    // unresolved. Each unresolved bridge gets the average position visually so
    // the path remains continuous.
    //
    // #1418 Phase C: multi-path mode. opts.edgeCounts maps "AB→60" to count
    // of observers that saw that edge. Stroke width scales with count
    // (consensus = thick, lone-witness = hairline).
    var edges = [];
    var multiPath = opts.multiPath === true;
    var edgeCounts = opts.edgeCounts || {};
    var totalObservers = opts.totalObservers || 1;
    function resolveCoord(idx) {
      if (positions[idx].lat != null && positions[idx].lon != null) {
        return { lat: positions[idx].lat, lon: positions[idx].lon, resolved: true };
      }
      var l = idx - 1, r = idx + 1;
      while (l >= 0 && positions[l].lat == null) l--;
      while (r < total && positions[r].lat == null) r++;
      var lp = l >= 0 ? positions[l] : null;
      var rp = r < total ? positions[r] : null;
      if (lp && rp) return { lat: (lp.lat + rp.lat)/2, lon: (lp.lon + rp.lon)/2, resolved: false };
      if (lp) return { lat: lp.lat, lon: lp.lon, resolved: false };
      if (rp) return { lat: rp.lat, lon: rp.lon, resolved: false };
      return null;
    }
    function edgeWeight(idx) {
      if (!multiPath) return 5;
      var fromKey = positions[idx].pubkey;
      var toKey = positions[idx + 1] && positions[idx + 1].pubkey;
      if (!fromKey || !toKey) return 5;
      // Special case: origin→first-hop and last-hop→destination edges are
      // NOT in edgeCounts (which only tracks path_json hop transitions).
      // Use the highest count of any edge originating from / arriving at
      // the boundary node as a proxy: any observer who saw the packet
      // implicitly transited this edge.
      var isOriginEdge = positions[idx].isOrigin;
      var isDestEdge = positions[idx + 1] && positions[idx + 1].isDest;
      if (isOriginEdge || isDestEdge) {
        var boundaryPrefix = isOriginEdge
          ? String(toKey).slice(0, 2).toUpperCase()
          : String(fromKey).slice(0, 2).toUpperCase();
        var max = 0;
        Object.keys(edgeCounts).forEach(function (k) {
          var parts = k.split('\u2192');
          if (parts.length !== 2) return;
          var a = parts[0].toUpperCase(), b = parts[1].toUpperCase();
          // For origin: edge starts at first path hop
          // For dest: edge ends at last path hop
          if ((isOriginEdge && a === boundaryPrefix) ||
              (isDestEdge && b === boundaryPrefix)) {
            if (edgeCounts[k] > max) max = edgeCounts[k];
          }
        });
        if (max > 0) {
          var bRatio = max / totalObservers;
          return 3 + bRatio * 6;
        }
        return 5;
      }
      var matchCount = 0;
      var fromPrefix = String(fromKey).slice(0, 2).toUpperCase();
      var toPrefix = String(toKey).slice(0, 2).toUpperCase();
      Object.keys(edgeCounts).forEach(function (k) {
        var parts = k.split('\u2192');
        if (parts.length !== 2) return;
        var a = parts[0].toUpperCase(), b = parts[1].toUpperCase();
        if ((a === fromPrefix || fromPrefix.startsWith(a)) &&
            (b === toPrefix || toPrefix.startsWith(b))) {
          matchCount += edgeCounts[k];
        }
      });
      if (matchCount === 0) return 1.5;
      var ratio = matchCount / totalObservers;
      // ensure ≥2× min↔max ratio so the visual difference is
      // visible. Range 2..8 px (4× ratio). Linear in coverage ratio.
      return 3 + ratio * 6;
    }
    for (var i = 0; i < total - 1; i++) {
      var ca = resolveCoord(i), cb = resolveCoord(i + 1);
      if (!ca || !cb) { edges.push(null); continue; }
      var color = rampColor(i, total - 1);
      var unresolvedEdge = !ca.resolved || !cb.resolved;
      var w = edgeWeight(i);
      var poly = L.polyline([[ca.lat, ca.lon], [cb.lat, cb.lon]], {
        color: color, weight: w, opacity: unresolvedEdge ? 0.5 : 0.85,
        dashArray: unresolvedEdge ? '6 4' : null,
        className: 'mc-rt-edge'
      }).addTo(layer);
      edges.push(poly);
    }

    // Markers (numbered, no chips, no labels, no arrows).
    // when same physical node OR distinct nodes within 25px collide,
    // we draw one marker per data point and spider-fan them on the ARC around
    // the collision centroid in a post-process step (after Leaflet projects
    // to pixel coords). Loop case (SRC == DST same node) gets a double ring
    // applied via isLoop flag. The fan keeps every seq# legible without comma-
    // stacking or aggregating.
    var srcDstSameNode = positions.length >= 2 &&
      positions[0].isOrigin && positions[positions.length-1].isDest &&
      positions[0].pubkey && positions[positions.length-1].pubkey &&
      String(positions[0].pubkey).toLowerCase() === String(positions[positions.length-1].pubkey).toLowerCase();
    var markers = positions.map(function (p, i) {
      if (p.lat == null || p.lon == null) return null;
      var color = (window.ROLE_COLORS && window.ROLE_COLORS[p.role]) || '#3b82f6';
      var isLoop = srcDstSameNode && (p.isOrigin || p.isDest);
      var built = buildMarkerSVG(p, { color: color, seqNum: i + 1, isLoop: isLoop });
      var html = '<div class="mc-rt-marker" data-hop-idx="' + i + '" tabindex="0" aria-label="Hop ' + (i+1) + ' of ' + total + ', ' + escapeHtml(p.name || '?') + '">' + built.html + '</div>';
      var icon = L.divIcon({
        html: html, className: 'mc-rt-marker-icon',
        iconSize: [built.size + 4, built.size + 4],
        iconAnchor: [(built.size + 4)/2, (built.size + 4)/2]
      });
      var mk = L.marker([p.lat, p.lon], { icon: icon }).addTo(layer);
      return mk;
    });

    // Spider-fan: after Leaflet projects, group any markers within
    // 25px of each other and offset them on an arc around their centroid.
    // Draw a hairline from each offset marker back to the centroid.
    // #1424: NOT extracted into route-view-utils.js — this consumes Leaflet
    // types (mapRef.latLngToLayerPoint, mk.getLatLng / setLatLng, L.point)
    // and mutates marker objects, so it isn't pure.
    function spiderFanFor(markerArray, positionArray) {
      if (!mapRef || !mapRef.latLngToLayerPoint) return;
      var pts = markerArray.map(function (mk, i) {
        if (!mk) return null;
        var ll;
        try { ll = mk._origLatLng || mk.getLatLng(); } catch (e) { return null; }
        if (!ll) return null;
        if (!mk._origLatLng) mk._origLatLng = ll;
        var origLL = mk._origLatLng;
        var lp = mapRef.latLngToLayerPoint([origLL.lat, origLL.lng]);
        return { idx: i, mk: mk, x: lp.x, y: lp.y, origLat: origLL.lat, origLon: origLL.lng };
      }).filter(function (x) { return x; });
      var visited = {};
      var groups = [];
      // Tuned: only fan if markers are TIGHTLY overlapping (<14px).
      // Looser thresholds make the map "dance" as zoom changes group membership.
      var COLLISION_THRESHOLD = 14;
      pts.forEach(function (a, ai) {
        if (visited[ai]) return;
        var group = [a];
        visited[ai] = true;
        pts.forEach(function (b, bi) {
          if (bi === ai || visited[bi]) return;
          var dx = a.x - b.x, dy = a.y - b.y;
          if (Math.sqrt(dx*dx + dy*dy) < COLLISION_THRESHOLD) { group.push(b); visited[bi] = true; }
        });
        if (group.length > 1) groups.push(group);
      });
      // Reset non-grouped markers to origin
      pts.forEach(function (p) {
        var inGroup = groups.some(function (g) { return g.indexOf(p) >= 0; });
        if (!inGroup) {
          try { p.mk.setLatLng([p.origLat, p.origLon]); } catch (e) {}
        }
      });
      groups.forEach(function (group) {
        var cx = group.reduce(function (s, g) { return s + g.x; }, 0) / group.length;
        var cy = group.reduce(function (s, g) { return s + g.y; }, 0) / group.length;
        var centerLatLng = mapRef.layerPointToLatLng(L.point(cx, cy));
        // Smaller fan radius — just enough to clear overlap (16px) instead of 28.
        var R = 16;
        group.forEach(function (g, k) {
          var angle = (k / group.length) * 2 * Math.PI;
          var ox = cx + R * Math.cos(angle);
          var oy = cy + R * Math.sin(angle);
          var newLatLng = mapRef.layerPointToLatLng(L.point(ox, oy));
          g.mk.setLatLng(newLatLng);
          var line = L.polyline([newLatLng, centerLatLng], {
            color: '#888', weight: 1, opacity: 0.5, dashArray: '2 2',
            interactive: false, className: 'mc-rt-spider-line'
          }).addTo(layer);
          sidebar._spiderLines.push(line);
        });
      });
    }
    function spiderFanMarkers() {
      // Clear previous spider artifacts (for both canonical + isolate)
      if (sidebar._spiderLines) {
        sidebar._spiderLines.forEach(function (l) { try { l.remove(); } catch (_) {} });
      }
      sidebar._spiderLines = [];
      // Apply fan to whichever marker set is currently active
      if (sidebar._isoMarkers && sidebar._isoMarkers.length) {
        spiderFanFor(sidebar._isoMarkers, []);
      } else if (sidebar._unionMarkers && sidebar._unionMarkers.length) {
        spiderFanFor(sidebar._unionMarkers, []);
      } else {
        spiderFanFor(markers, positions);
      }
    }
    // Expose so isolatePath / restoreAllPaths can re-fan after rendering.
    // (Function declared but `sidebar` ref deferred until after buildSidebar.)
    function exposeRespider() {
      if (typeof sidebar !== 'undefined' && sidebar) {
        sidebar._respider = function () { setTimeout(spiderFanMarkers, 200); };
      }
    }
    // Run spider after Leaflet finishes projecting + on zoom only (pan
    // shouldn't re-cluster since relative positions don't change).
    setTimeout(spiderFanMarkers, 400);
    var _spiderDebounce = null;
    mapRef.on('zoomend', function () {
      if (_spiderDebounce) clearTimeout(_spiderDebounce);
      _spiderDebounce = setTimeout(spiderFanMarkers, 250);
    });

    // Sidebar
    var prevSidebar = document.querySelector('.mc-rt-sidebar');
    if (prevSidebar) prevSidebar.remove();
    var sidebar = buildSidebar(positions, mapRef, layer, edges, markers, opts);
    exposeRespider();
    var mapContainer = document.querySelector('#leaflet-map');
    if (mapContainer && mapContainer.parentElement) {
      mapContainer.parentElement.insertBefore(sidebar, mapContainer);
    } else {
      document.body.appendChild(sidebar);
    }

    // Wire marker → sidebar (after sidebar exists). Click marker = scroll sidebar
    // to corresponding row + highlight. Hover marker = tooltip (Leaflet popup
    // already exists from .bindPopup, we add a click handler too).
    markers.forEach(function (mk, idx) {
      if (!mk) return;
      mk.on('click', function () {
        if (sidebar._scrollRowIntoView) sidebar._scrollRowIntoView(idx);
        // Trigger row click to open detail panel
        var row = sidebar.querySelector('.mc-rt-row[data-hop-idx="' + idx + '"]');
        if (row && !row.querySelector('.mc-rt-detail-panel')) {
          row.click();
        }
      });
      // Hover tooltip — Leaflet's built-in
      var p = positions[idx];
      var dist = idx > 0 ? (function () {
        var a = positions[idx-1], b = positions[idx];
        if (a.lat == null || b.lat == null) return null;
        return haversineKm(a, b);
      })() : null;
      var tipText = 'hop ' + (idx + 1) + ' · ' + escapeHtml(p.name || '?') + (dist != null ? ' · ' + dist + ' km from prev' : '');
      mk.bindTooltip(tipText, { direction: 'top', offset: [0, -10] });
    });

    // Fit bounds (immediate + deferred — defer fixes mobile where the map
    // hasn't been sized to its final mobile-overlay rect yet).
    // Special case: a single point gives degenerate bounds → max zoom (street
    // level on empty water often). setView with reasonable zoom instead.
    var fitPts = positions.filter(function(p){return p.lat!=null});
    function refit() {
      try {
        mapRef.invalidateSize();
        // Iterate layer children manually — L.LayerGroup doesn't aggregate
        // child bounds (only L.FeatureGroup does, but route layer is a plain
        // LayerGroup). Collect every marker latLng + polyline latLngs.
        var bounds = null;
        try {
          if (layer && typeof layer.eachLayer === 'function') {
            layer.eachLayer(function (child) {
              try {
                if (child.getLatLng) {
                  var ll = child.getLatLng();
                  if (!bounds) bounds = L.latLngBounds(ll, ll); else bounds.extend(ll);
                } else if (child.getLatLngs) {
                  var lls = child.getLatLngs();
                  // Flatten — could be array or nested
                  var flat = [];
                  (function walk(x) {
                    if (!x) return;
                    if (Array.isArray(x)) x.forEach(walk);
                    else if (x.lat != null) flat.push(x);
                  })(lls);
                  flat.forEach(function (ll) {
                    if (!bounds) bounds = L.latLngBounds(ll, ll); else bounds.extend(ll);
                  });
                }
              } catch (e) { console.warn('[route-view]', e); }
            });
          }
        } catch (e) { console.warn('[route-view]', e); }
        if (!bounds && fitPts.length >= 2) {
          bounds = L.latLngBounds(fitPts.map(function(p){return [p.lat, p.lon]}));
        }
        var isMob = window.innerWidth <= 767;
        if (fitPts.length === 1 && !bounds) {
          mapRef.setView([fitPts[0].lat, fitPts[0].lon], 11, { animate: false });
        } else if (bounds && bounds.isValid()) {
          mapRef.fitBounds(bounds, isMob
            ? { paddingTopLeft: [30, 70], paddingBottomRight: [30, 190], maxZoom: 14 }
            : { padding: [40, 40], maxZoom: 14 });
        }
      } catch (e) { console.warn('[route-view]', e); }
    }
    if (fitPts.length > 0) {
      // Polish review (carmack/doshi/tufte): the previous 5-staggered-timer
      // (0/300/800/1600/2800ms) + naked window.resize listener leaked one
      // closure per route-view render and made the map "dance" for ~3s.
      // Replace with a single rAF for initial settle + a ResizeObserver on
      // the map container for layout changes. The resize handler is also
      // stashed on window.__mc_routeResizeRefit so subsequent renders can
      // detach the prior closure (same pattern as hashchange/cb-preset).
      requestAnimationFrame(refit);

      // Tear down any prior resize/ResizeObserver attachments first.
      try {
        if (window.__mc_routeResizeRefit) {
          window.removeEventListener('resize', window.__mc_routeResizeRefit);
        }
      } catch (e) { console.warn('[route-view] resize cleanup:', e); }
      try {
        if (window.__mc_routeResizeObserver && window.__mc_routeResizeObserver.disconnect) {
          window.__mc_routeResizeObserver.disconnect();
        }
      } catch (e) { console.warn('[route-view] ResizeObserver cleanup:', e); }

      var _resizeRefitTimer = null;
      function onResize() {
        if (_resizeRefitTimer) clearTimeout(_resizeRefitTimer);
        _resizeRefitTimer = setTimeout(refit, 200);
      }
      window.__mc_routeResizeRefit = onResize;
      window.addEventListener('resize', onResize);

      // ResizeObserver on the map container catches sidebar open/close,
      // mobile bottom-sheet expand, and other layout settles that don't
      // fire a window.resize event.
      try {
        var mapEl = mapRef && mapRef.getContainer ? mapRef.getContainer() : null;
        if (mapEl && typeof ResizeObserver === 'function') {
          var ro = new ResizeObserver(function () { onResize(); });
          ro.observe(mapEl);
          window.__mc_routeResizeObserver = ro;
        }
      } catch (e) { console.warn('[route-view] ResizeObserver attach:', e); }
    }
  }

  window.MeshRouteView = { render: render };
})();
