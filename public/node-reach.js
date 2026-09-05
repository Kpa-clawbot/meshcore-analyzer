/* === CoreScope — node-reach.js ===
   Standalone per-node "Reach" page: importance stats + a map of the node's
   bidirectional RF links + a link table. Registered as page 'node-reach'
   (route #/nodes/<pubkey>/reach), mirroring node-analytics.js. */
'use strict';
(function () {
  var qmap = null;       // map controller from NodeReachMap.render (built once per load)
  var current = null;
  var loadGen = 0;       // bumped per load + on destroy; guards against in-flight races
  var DEFAULT_DAYS = 7;  // single JS source for the default window (mirrors the server default)
  var coverageOn = false; // mobile-client RX coverage hex layer (deep-linked ?coverage=1)
  var covHandle = null;   // handle from NodeReachCoverage.addLayer (off() to remove)

  // setCoverageHash reflects the coverage toggle in the URL hash without
  // re-triggering the router (history.replaceState, not location.hash =).
  function setCoverageHash(on) {
    var h = location.hash.replace(/([?&])coverage=1/, '$1').replace(/[?&]$/, '');
    if (on) { h += (h.indexOf('?') >= 0 ? '&' : '?') + 'coverage=1'; }
    try { history.replaceState(null, '', h || location.pathname + location.search); } catch (e) {}
  }

  // Single source of the bottleneck tiers: colour + threshold + colour-blind
  // glyph + legend text. The map legend and the table both read from this.
  // A one-way link has bottleneck 0 (one direction is 0) — its own tier so it
  // reads as "no two-way", not as a poor two-way (which would be red/weak).
  var TIERS = [
    { min: 300, label: 'strong', varName: '--link-strong', glyph: '●●●', legend: 'strong (≥300)' },
    { min: 100, label: 'medium', varName: '--link-medium', glyph: '●●', legend: 'medium (100–299)' },
    { min: 1, label: 'weak', varName: '--link-weak', glyph: '●', legend: 'weak (<100)' },
    { min: 0, label: 'one-way', varName: '--link-oneway', glyph: '○', legend: 'one-way (no two-way)' }
  ];
  function tierOf(b) {
    for (var i = 0; i < TIERS.length; i++) {
      if (b >= TIERS[i].min) return TIERS[i];
    }
    return TIERS[TIERS.length - 1];
  }

  function statCard(label, value, descShort, descFull) {
    return '<div class="analytics-stat-card" title="' + escapeHtml(descFull) + '">' +
      '<div class="analytics-stat-label">' + escapeHtml(label) + '</div>' +
      '<div class="analytics-stat-value">' + escapeHtml(String(value)) + '</div>' +
      '<div class="analytics-stat-desc">' + escapeHtml(descShort) + '</div></div>';
  }

  function linkRow(i, l) {
    var dist = l.distance_km != null ? Number(l.distance_km).toFixed(1) : '—';
    var dir = l.bidir ? '' : (l.we_hear > 0 ? 'incoming' : 'outgoing');
    var href = '#/nodes/' + encodeURIComponent(l.pubkey);
    var t = tierOf(l.bottleneck);
    return '<tr' + (l.bidir ? '' : ' class="nq-oneway"') + '>' +
      '<td class="nq-num">' + i + '</td>' +
      '<td><a href="' + href + '" class="nq-link">' + escapeHtml(l.name || l.pubkey.slice(0, 8)) + '</a>' +
      (dir ? ' <span class="nq-dir">' + dir + '</span>' : '') + '</td>' +
      '<td class="nq-n">' + l.we_hear + '</td>' +
      '<td class="nq-n">' + l.they_hear + '</td>' +
      '<td class="nq-n" title="' + t.label + '"><span class="nq-tier" style="color:var(' + t.varName + ')">' + l.bottleneck +
      '</span><span class="nq-tier-glyph" style="color:var(' + t.varName + ')">' + t.glyph + '</span></td>' +
      '<td class="nq-n">' + dist + '</td></tr>';
  }

  function dayBtn(d, cur, label) {
    var on = d === cur;
    return '<button data-days="' + d + '" aria-pressed="' + (on ? 'true' : 'false') + '"' +
      (on ? ' class="active"' : '') + '>' + label + '</button>';
  }

  // #1865: which region scopes this node serves, on the report itself so a
  // printed or shared reach page carries it. Two different claims, never
  // conflated: "Scope" is INFERRED from the transport scope of observed
  // adverts, while "Configured scope" is CONFIRMED, read back off the node by
  // an observer /neighbors report. Confirmed wins the line when both exist,
  // and an empty confirmed value is itself a statement ("responded, none
  // configured") rather than missing data, so it renders too.
  function scopeLineHtml(n) {
    var hasConfirmed = typeof n.configured_scope === 'string';
    if (!hasConfirmed && !n.default_scope) return '';
    if (hasConfirmed) {
      var at = n.configured_scope_at ? ' Last confirmed ' + n.configured_scope_at + '.' : '';
      var val = n.configured_scope === ''
        ? '<span class="nq-scope-none">none configured</span>'
        : '<code>' + escapeHtml(n.configured_scope) + '</code>';
      return '<div class="nq-scope">Configured scope ' +
        '<span class="nq-scope-ok" aria-label="confirmed">✓</span> ' + val +
        '<span class="nq-scope-src" title="Read back off the node via an observer /neighbors report (status=responded).' +
        escapeHtml(at) + '"> confirmed</span></div>';
    }
    return '<div class="nq-scope">Scope <code>' + escapeHtml(n.default_scope) + '</code>' +
      '<span class="nq-scope-src" title="Inferred from the transport scope of observed adverts, not read back off the node."> observed</span></div>';
  }

  function headerHtml(n, nodeName, days) {
    return '<div class="nq-head">' +
      '<a class="nq-back" href="#/nodes/' + encodeURIComponent(n.pubkey) + '" aria-label="Back to ' + nodeName + ' detail">← Back to ' + nodeName + '</a>' +
      '<h2 class="nq-title">' + nodeName + ' — Reach</h2>' +
      '<div class="nq-sub">' + escapeHtml(n.role || 'Unknown role') + ' · two-way RF link reach</div>' +
      scopeLineHtml(n) +
      '<div class="nq-note">Reliable by design: built only from unique <b>2–3 byte</b> path-hash (multibyte) matches. 1-byte hops collide between nodes and are excluded, so the links shown are trustworthy.</div>' +
      '<div class="analytics-time-range nq-noprint" id="nqDays" style="margin-top:8px">' +
      dayBtn(1, days, '24h') + dayBtn(7, days, '7d') + dayBtn(14, days, '14d') + dayBtn(30, days, '30d') +
      '</div></div>';
  }

  function wireTimeRange(container, pubkey) {
    var bar = container.querySelector('#nqDays');
    if (!bar) return;
    bar.addEventListener('click', function (e) {
      var b = e.target.closest('button[data-days]');
      if (b) load(container, pubkey, parseInt(b.getAttribute('data-days'), 10), false);
    });
  }

  function printReport() {
    // Leaflet only renders tiles for the on-screen size; on a wide screen the
    // printed page is narrower and the right half is clipped. Resize the map to
    // the print width (single source: --nq-print-width) and invalidate first.
    var mapEl = document.getElementById('nqMap');
    if (qmap && qmap.map && mapEl) {
      var pw = getComputedStyle(document.documentElement).getPropertyValue('--nq-print-width').trim() || '680px';
      mapEl.style.width = pw;
      qmap.map.invalidateSize();
      try { qmap.map.fitBounds(qmap.bounds, { padding: [20, 20] }); } catch (e) {}
      // Wait for layout to settle (two animation frames) instead of a fixed
      // sleep that races the browser reflow.
      requestAnimationFrame(function () {
        requestAnimationFrame(function () {
          window.print();
          mapEl.style.width = '';
          qmap.map.invalidateSize();
          try { qmap.map.fitBounds(qmap.bounds, { padding: [30, 30] }); } catch (e) {}
        });
      });
    } else {
      window.print();
    }
  }

  // isInitial=true shows the centred loader (first paint); time-range changes
  // pass false so the current report stays on screen until the swap (no flash).
  async function load(container, pubkey, days, isInitial) {
    var myGen = ++loadGen;
    // Wait for server config so the coverage flag (read just below and in the
    // actions bar markup) is populated even on a direct land on this route — a
    // race that otherwise hides the coverage toggle (#13).
    try { await window.MeshConfigReady; } catch (e) {}
    if (myGen !== loadGen) return; // superseded by a newer load while waiting
    current = { pubkey: pubkey, days: days };
    coverageOn = window.MC_CLIENT_RX_COVERAGE === true && (typeof getHashParams === 'function' && getHashParams().get('coverage') === '1');
    if (covHandle) { try { covHandle.off(); } catch (e) {} covHandle = null; }
    if (qmap) { qmap.destroy(); qmap = null; }
    if (isInitial) {
      container.innerHTML = '<div class="nq-load">Loading reach…</div>';
    }

    var d;
    try {
      d = await api('/nodes/' + encodeURIComponent(pubkey) + '/reach?days=' + days, { ttl: 30000 });
    } catch (e) {
      if (myGen !== loadGen) return; // superseded or destroyed mid-flight
      container.innerHTML = '<div id="nq-report"><div class="nq-error">Failed to load reach: ' + escapeHtml(e.message) + '</div></div>';
      return;
    }
    if (myGen !== loadGen) return; // a newer load (or destroy) won the race
    current.data = d;
    var n = d.node;
    var nodeName = escapeHtml(n.name || n.pubkey.slice(0, 12));
    var imp = d.importance || {};
    var twoWay = d.links.filter(function (l) { return l.bidir; });

    if (!d.reliable_tokens || d.reliable_tokens.length === 0) {
      // nodeName is already escaped; build then assign (keeps it off the
      // innerHTML line for the XSS-sink gate, like statsHtml below).
      var emptyHtml = '<div id="nq-report">' + headerHtml(n, nodeName, days) +
        '<div class="nq-msg">This node has no unique 1–3 byte prefix, so it cannot be reliably identified in paths — no link data available.</div></div>';
      container.innerHTML = emptyHtml;
      wireTimeRange(container, pubkey);
      return;
    }

    var statsHtml = headerHtml(n, nodeName, days) +
      '<div class="nq-body">' +
      '<div class="nq-group-h">Network position (all-time)</div>' +
      '<div class="analytics-stats">' +
      statCard('Neighbours', imp.neighbor_degree, 'All-time distinct neighbours',
        'Distinct neighbours in the all-time neighbour graph (advert first-hop + observer last-hop, geo-filtered).') +
      statCard('Rank', '#' + imp.degree_rank + ' / ' + imp.nodes_with_edges, 'Rank by neighbour count',
        'Rank by neighbour count among all nodes with edges. #1 = most-connected node in the network.') +
      '</div>' +
      '<div class="nq-group-h">Last ' + d.window.days + ' days</div>' +
      '<div class="analytics-stats">' +
      statCard('Links', d.links.length, 'Neighbours seen this window',
        'Distinct direct neighbours seen in paths this window (any direction).') +
      statCard('Two-way', imp.bidirectional_links, 'Heard both directions',
        'Neighbours heard in BOTH directions — stable links. Counts mid-path adjacency, so can exceed all-time Neighbours.') +
      statCard('Relay obs', imp.relay_observations, 'Times seen in a path',
        'Observations where this node appears anywhere in the path (its relay throughput).') +
      statCard('Direct observers', imp.direct_observers, 'Heard it at 0 hops',
        'Stations that received this node directly, at 0 hops.') +
      '</div>';

    // Identifiable, but no path adjacency observed in this window.
    if (!d.links.length) {
      container.innerHTML = '<div id="nq-report">' + statsHtml +
        '<div class="nq-empty">No observed RF links in the last ' + d.window.days +
        ' days — this node advertises but hasn’t been seen relaying traffic (or no observers captured it). Try a longer window.</div>' +
        '</div></div>';
      wireTimeRange(container, pubkey);
      return;
    }

    container.innerHTML = '<div id="nq-report">' + statsHtml +
      '<div class="nq-actions nq-noprint">' +
      '<fieldset class="nq-filter"><legend>Show one-way links</legend>' +
      '<label for="nqIncoming"><input type="checkbox" id="nqIncoming"> incoming <span class="nq-dir">(we hear them)</span></label>' +
      '<label for="nqOutgoing"><input type="checkbox" id="nqOutgoing"> outgoing <span class="nq-dir">(they hear us)</span></label>' +
      '</fieldset>' +
      (window.MC_CLIENT_RX_COVERAGE ? '<label for="nqCoverage"><input type="checkbox" id="nqCoverage"' + (coverageOn ? ' checked' : '') + '> coverage <span class="nq-dir">(where clients heard it)</span></label>' : '') +
      '<span id="nqCount" class="nq-count" aria-live="polite"></span>' +
      '<button id="nqPrintBtn" class="btn-primary nq-print-btn">Print / PDF</button>' +
      '</div>' +
      (window.MC_CLIENT_RX_COVERAGE ? '<div class="nq-cov-legend nq-noprint' + (coverageOn ? '' : ' is-hidden') + '" id="nqCovLegend">' +
      '<span><i style="background:var(--nq-cov-strong)"></i>strong</span>' +
      '<span><i style="background:var(--nq-cov-mid)"></i>medium</span>' +
      '<span><i style="background:var(--nq-cov-weak)"></i>weak</span>' +
      '<span><i style="background:var(--nq-cov-grey)"></i>no signal</span>' +
      '</div>' : '') +
      '<div id="nqNoGps" class="nq-nogps"></div>' +
      '<div id="nqMap" class="nq-map"></div>' +
      '<table class="nq-table"><thead><tr><th>#</th><th>Neighbour</th><th>we hear</th>' +
      '<th>they hear us</th><th title="smaller of we-hear / they-hear — gates two-way stability">bottleneck</th>' +
      '<th>distance (km)</th></tr></thead><tbody id="nqRows"></tbody></table>' +
      '</div></div>';

    // Build the map ONCE; toggles update the link layer in place (no flicker).
    if (window.NodeReachMap && n.lat != null) {
      qmap = window.NodeReachMap.render('nqMap', n, TIERS);
    }

    var lastList = []; // most recent filtered link list (so the coverage toggle can restore lines)
    // Two-way links are always shown; the two checkboxes add the asymmetric ones.
    function paint() {
      var inc = document.getElementById('nqIncoming').checked;
      var out = document.getElementById('nqOutgoing').checked;
      var list = d.links.filter(function (l) {
        if (l.bidir) return true;
        var weOnly = l.we_hear > 0 && l.they_hear === 0;
        return (inc && weOnly) || (out && !weOnly);
      }).sort(function (a, b) {
        return (b.bidir - a.bidir) || (b.bottleneck - a.bottleneck) ||
          ((b.we_hear + b.they_hear) - (a.we_hear + a.they_hear));
      });
      document.getElementById('nqRows').innerHTML = list.map(function (l, i) { return linkRow(i + 1, l); }).join('');
      document.getElementById('nqCount').textContent =
        'showing ' + list.length + ' of ' + d.links.length + ' (' + twoWay.length + ' two-way)';
      var noGps = list.filter(function (l) { return l.lat == null || l.lon == null; }).length;
      document.getElementById('nqNoGps').textContent =
        noGps ? noGps + ' link' + (noGps === 1 ? '' : 's') + ' have no location and are not drawn on the map.' : '';
      lastList = list;
      if (qmap) qmap.setLinks(coverageOn ? [] : list);
    }
    paint();
    document.getElementById('nqIncoming').addEventListener('change', paint);
    document.getElementById('nqOutgoing').addEventListener('change', paint);
    document.getElementById('nqPrintBtn').addEventListener('click', printReport);

    // Coverage overlay (fork): toggles the mobile-client RX hex layer in place and
    // hides the link lines under it (declutter) — the table still lists every link.
    var covLegend = document.getElementById('nqCovLegend');
    function applyCoverage() {
      if (covLegend) covLegend.classList.toggle('is-hidden', !coverageOn);
      if (coverageOn) {
        if (qmap && window.NodeReachCoverage && !covHandle) covHandle = window.NodeReachCoverage.addLayer(qmap.map, pubkey);
      } else if (covHandle) {
        try { covHandle.off(); } catch (e) {}
        covHandle = null;
      }
      if (qmap) qmap.setLinks(coverageOn ? [] : lastList);
    }
    var covCb = document.getElementById('nqCoverage');
    if (covCb) covCb.addEventListener('change', function (e) {
      coverageOn = e.target.checked;
      setCoverageHash(coverageOn);
      applyCoverage();
    });
    if (coverageOn) applyCoverage(); // deep-linked ?coverage=1: add layer + hide lines now

    wireTimeRange(container, pubkey);
  }

  function init(container, routeParam) {
    if (!routeParam || !routeParam.endsWith('/reach')) {
      container.innerHTML = '<div class="nq-load">Invalid reach URL</div>';
      return;
    }
    load(container, routeParam.slice(0, -'/reach'.length), DEFAULT_DAYS, true);
  }

  function destroy() {
    loadGen++; // invalidate any in-flight load so it won't mutate a foreign container
    if (covHandle) { try { covHandle.off(); } catch (e) {} covHandle = null; }
    if (qmap) { qmap.destroy(); qmap = null; }
    current = null;
  }

  if (typeof window !== 'undefined') {
    // #1865: exposed so the helper tests can assert what the scope line RENDERS
    // rather than grepping this file, the same reason map.js exposes its label
    // builder (#1356/#1933).
    window.__meshcoreReachInternals = { scopeLineHtml: scopeLineHtml };
  }

  registerPage('node-reach', { init: init, destroy: destroy });
})();
