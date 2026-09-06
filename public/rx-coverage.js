/* === CoreScope — rx-coverage.js ===
   Mobile RX coverage hub (route #/rx-coverage):
   - global H3-style hex coverage map (all mobile observers), time-windowed
   - leaderboard of top mobile observers (companion name + counts)
   - click an observer to filter the map to just their coverage
   Fork-only feature; isolated page (no changes to the core map). */
'use strict';
(function () {
  var map = null, covLayer = null, days = 7, selectedRx = '', selectedName = '', boardCache = [], destroyed = false;

  function cssColor(varName) {
    try { return getComputedStyle(document.documentElement).getPropertyValue(varName).trim() || '#888'; }
    catch (e) { return '#888'; }
  }
  // SF8 SNR thresholds: ≥ −5 good margin, −9..−5 near the limit, < −9 packet loss
  // likely. Grey = heard but no SNR metric.
  function colorVar(p) {
    if (!p || !p.has_sig || p.best_snr == null) return '--nq-cov-grey';
    var s = Number(p.best_snr);
    if (s >= -5) return '--nq-cov-strong';
    if (s >= -9) return '--nq-cov-mid';
    return '--nq-cov-weak';
  }

  function dayBtn(d) { return '<button data-days="' + d + '"' + (d === days ? ' class="active"' : '') + ' aria-pressed="' + (d === days ? 'true' : 'false') + '">' + (d === 1 ? '24h' : d + 'd') + '</button>'; }

  function pageHtml() {
    return '<div style="max-width:1100px;margin:0 auto;padding:12px 16px">' +
      '<h2 style="margin:4px 0 2px;font-size:18px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-trifold"/></svg> Mobile RX coverage</h2>' +
      '<div style="color:var(--text-muted);font-size:11px">Where roaming CoreScope-RX clients heard nodes. Colour = best signal per cell. <a href="https://github.com/efiten/corescope-rx" target="_blank" rel="noopener">Get the companion app →</a></div>' +
      '<div class="analytics-time-range" id="rxDays" style="margin:8px 0">' + dayBtn(1) + dayBtn(7) + dayBtn(14) + dayBtn(30) + '</div>' +
      '<div class="nq-cov-legend"><span><i style="background:var(--nq-cov-strong)"></i>strong</span><span><i style="background:var(--nq-cov-mid)"></i>medium</span><span><i style="background:var(--nq-cov-weak)"></i>weak</span><span><i style="background:var(--nq-cov-grey)"></i>no signal</span></div>' +
      '<div id="rxMap" style="height:60vh;min-height:360px;border:1px solid var(--border,#d0d7de);border-radius:6px;margin:8px 0"></div>' +
      '<div class="nq-group-h">Top mobile observers</div>' +
      '<div id="rxBoard" class="rxb"></div>' +
      '</div>';
  }

  // coverageNodeRow renders one heard node: name (or heard_key prefix) + latest SNR + count.
  function coverageNodeRow(n) {
    var label = n.name ? escapeHtml(n.name) : '<code>' + escapeHtml(n.prefix || '?') + '</code>';
    var snr = (n.snr != null) ? Number(n.snr).toFixed(1) + ' dB' : 'no sig';
    return '<div style="display:flex;gap:8px;justify-content:space-between;font-size:12px;line-height:1.6">' +
      '<span>' + label + '</span>' +
      '<span style="color:var(--text-muted)">' + snr + ' · ×' + n.count + '</span></div>';
  }

  // coverageNodesHtml lists the nodes directly heard in a cell (properties.nodes:
  // {prefix, name, snr, count}, strongest latest-SNR first; prefix shown when the
  // name is unresolved). Rendered in the hover tooltip; capped at 10 rows with a
  // "(N more)" footer so dense cells don't produce an unwieldy tooltip.
  var COVERAGE_NODE_CAP = 10;
  function coverageNodesHtml(p) {
    var nodes = (p && p.nodes) || [];
    var head = '<div style="font-weight:600;margin-bottom:4px">' +
      nodes.length + (nodes.length === 1 ? ' node heard here' : ' nodes heard here') + '</div>';
    if (!nodes.length) return head + '<div style="color:var(--text-muted)">n=' + (p ? p.count : 0) + '</div>';
    var rows = nodes.slice(0, COVERAGE_NODE_CAP).map(coverageNodeRow).join('');
    var more = (nodes.length > COVERAGE_NODE_CAP)
      ? '<div style="color:var(--text-muted);font-size:11px;margin-top:3px">(' + (nodes.length - COVERAGE_NODE_CAP) + ' more)</div>'
      : '';
    return head + '<div style="min-width:180px">' + rows + '</div>' + more;
  }

  // fillOpacityFor adds a redundant, non-hue cue to the SNR tier so the map is
  // distinguishable for colour-blind users (orange vs red): stronger signal =
  // more opaque. Pairs with the hue and the per-cell SNR in the tooltip (#a11y).
  function fillOpacityFor(p) {
    switch (colorVar(p)) {
      case '--nq-cov-strong': return 0.6;
      case '--nq-cov-mid': return 0.48;
      case '--nq-cov-weak': return 0.34;
      default: return 0.22;
    }
  }

  function drawCoverage() {
    if (!map || destroyed) return;
    var b = map.getBounds();
    var bbox = [b.getSouth(), b.getWest(), b.getNorth(), b.getEast()].join(',');
    var url = '/api/rx-coverage?bbox=' + bbox + '&z=' + map.getZoom() + '&days=' + days + (selectedRx ? '&rx=' + encodeURIComponent(selectedRx) : '');
    fetch(url).then(function (r) { return r.json(); }).then(function (fc) {
      if (destroyed || !covLayer) return;
      covLayer.clearLayers();
      (fc.features || []).forEach(function (f) {
        var ring = (f.geometry.coordinates[0] || []).map(function (c) { return [c[1], c[0]]; });
        var col = cssColor(colorVar(f.properties));
        L.polygon(ring, { color: col, weight: 1, fillColor: col, fillOpacity: fillOpacityFor(f.properties) }).addTo(covLayer)
          .bindTooltip(coverageNodesHtml(f.properties));
      });
    }).catch(function (e) { console.warn('rx-coverage: coverage fetch failed', e); });
  }

  // Leaderboard sort state. Default = frontier score, descending. The rank (#)
  // column is not sortable (it just reflects the current order). Numeric columns
  // default to descending on first click; the name column to ascending.
  var boardSort = { key: 'score', dir: 'desc' };
  var BOARD_COLS = [
    { key: 'name', label: 'Observer (companion)', cls: 'rxb-name' },
    { key: 'score', label: 'score', cls: 'rxb-score',
      title: 'Score telt je gedekte cellen, waarbij elke cel zwaarder weegt naarmate minder andere waarnemers ze bereikt hebben — grensverleggende dekking weegt meer dan drukke zones opnieuw afrijden.' },
    { key: 'cells', label: 'cells', cls: 'rxb-cells',
      title: 'Aantal unieke ~150 m-cellen waar deze waarnemer iets hoorde.' },
    { key: 'nodes', label: 'nodes', cls: 'rxb-nodes' },
    { key: 'receptions', label: 'pkts', cls: 'rxb-rec' }
  ];

  function sortBoard() {
    var k = boardSort.key, dir = boardSort.dir === 'asc' ? 1 : -1;
    boardCache.sort(function (a, b) {
      if (k === 'name') {
        var an = (a.name || a.pubkey).toLowerCase(), bn = (b.name || b.pubkey).toLowerCase();
        return an < bn ? -dir : an > bn ? dir : 0;
      }
      return (Number(a[k]) - Number(b[k])) * dir;
    });
  }

  function boardHeadHtml() {
    var cells = BOARD_COLS.map(function (c) {
      var arrow = boardSort.key === c.key ? (boardSort.dir === 'asc' ? ' ▲' : ' ▼') : '';
      return '<span class="' + c.cls + ' rxb-sort" data-sort="' + c.key + '"' +
        (c.title ? ' title="' + escapeHtml(c.title) + '"' : '') +
        ' role="button" tabindex="0">' + escapeHtml(c.label) + arrow + '</span>';
    }).join('');
    return '<div class="rxb-row rxb-head"><span class="rxb-rank">#</span>' + cells + '</div>';
  }

  function renderBoard() {
    var el = document.getElementById('rxBoard');
    if (!el) return;
    if (!boardCache.length) { el.innerHTML = '<div class="muted" style="color:var(--text-muted);font-size:13px">No mobile observers in this window yet.</div>'; return; }
    sortBoard();
    var rows = boardCache.map(function (o, i) {
      var nm = o.name ? escapeHtml(o.name) : (escapeHtml(o.pubkey.slice(0, 10)) + '…');
      return '<div class="rxb-row' + (o.pubkey === selectedRx ? ' sel' : '') + '" data-rx="' + escapeHtml(o.pubkey) + '" data-name="' + escapeHtml(o.name || '') + '"' +
        ' role="button" tabindex="0" aria-pressed="' + (o.pubkey === selectedRx ? 'true' : 'false') + '" aria-label="Show coverage for ' + escapeHtml(o.name || o.pubkey.slice(0, 10)) + '">' +
        '<span class="rxb-rank">' + (i + 1) + '</span><span class="rxb-name">' + nm + '</span>' +
        '<span class="rxb-score">' + Number(o.score).toFixed(1) + '</span>' +
        '<span class="rxb-cells">' + o.cells + '</span>' +
        '<span class="rxb-nodes">' + o.nodes + '</span>' +
        '<span class="rxb-rec">' + o.receptions + '</span></div>';
    }).join('');
    el.innerHTML = (selectedRx ? '<button id="rxAll" class="btn-primary" style="margin:0 0 8px">← Show all observers</button>' : '') +
      boardHeadHtml() + rows;
    // Column sort handlers (click + keyboard).
    el.querySelectorAll('.rxb-sort[data-sort]').forEach(function (h) {
      function applySort() {
        var k = h.dataset.sort;
        if (boardSort.key === k) {
          boardSort.dir = boardSort.dir === 'asc' ? 'desc' : 'asc';
        } else {
          boardSort.key = k;
          boardSort.dir = (k === 'name') ? 'asc' : 'desc';
        }
        renderBoard();
      }
      h.addEventListener('click', applySort);
      h.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' || e.key === ' ' || e.key === 'Spacebar') { e.preventDefault(); applySort(); }
      });
    });
    // Row click-to-filter (preserved from the original).
    el.querySelectorAll('.rxb-row[data-rx]').forEach(function (r) {
      function activate() {
        selectedRx = r.dataset.rx; selectedName = r.dataset.name || '';
        renderBoard(); fitToObserver(); syncHash();
      }
      r.addEventListener('click', activate);
      r.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' || e.key === ' ' || e.key === 'Spacebar') { e.preventDefault(); activate(); }
      });
    });
    var all = document.getElementById('rxAll');
    if (all) all.addEventListener('click', function () { selectedRx = ''; selectedName = ''; renderBoard(); drawCoverage(); syncHash(); });
  }

  // fitToObserver zooms the map to the selected observer's full coverage extent
  // (fetched with a world bbox so it's independent of the current view), then the
  // resulting moveend redraws the hexes at the fitted resolution.
  function fitToObserver() {
    if (!map || !selectedRx) { drawCoverage(); return; }
    var url = '/api/rx-coverage?bbox=-90,-180,90,180&z=' + Math.max(8, map.getZoom()) + '&days=' + days + '&rx=' + encodeURIComponent(selectedRx);
    fetch(url).then(function (r) { return r.json(); }).then(function (fc) {
      if (destroyed || !map) return;
      var minLat = 90, minLon = 180, maxLat = -90, maxLon = -180, any = false;
      (fc.features || []).forEach(function (f) {
        (f.geometry.coordinates[0] || []).forEach(function (c) {
          any = true;
          if (c[1] < minLat) minLat = c[1]; if (c[1] > maxLat) maxLat = c[1];
          if (c[0] < minLon) minLon = c[0]; if (c[0] > maxLon) maxLon = c[0];
        });
      });
      if (!any) { drawCoverage(); return; } // observer has no data in window → keep view
      map.fitBounds([[minLat, minLon], [maxLat, maxLon]], { padding: [30, 30], maxZoom: 15 });
      drawCoverage(); // fitBounds may not fire moveend if the view is unchanged
    }).catch(function (e) { console.warn('rx-coverage: observer extent fetch failed', e); drawCoverage(); });
  }

  function loadBoard() {
    fetch('/api/rx-leaderboard?days=' + days + '&limit=25').then(function (r) { return r.json(); })
      .then(function (d) { if (destroyed) return; boardCache = d.observers || []; renderBoard(); })
      .catch(function (e) {
        console.warn('rx-coverage: leaderboard fetch failed', e);
        var el = document.getElementById('rxBoard');
        if (el) el.innerHTML = '<div class="muted" style="color:var(--text-muted);font-size:13px">Could not load mobile observers.</div>';
      });
  }

  function setDays(d) {
    days = d;
    var bar = document.getElementById('rxDays');
    if (bar) bar.querySelectorAll('button').forEach(function (b) { b.classList.toggle('active', +b.dataset.days === d); });
    loadBoard(); drawCoverage(); syncHash();
  }

  function syncHash() {
    var q = 'days=' + days + (selectedRx ? '&rx=' + selectedRx : '');
    try { history.replaceState(null, '', '#/rx-coverage?' + q); } catch (e) {}
  }

  function init(container) {
    destroyed = false;
    // A direct land on #/rx-coverage can run before MeshConfigReady resolves, at
    // which point MC_CLIENT_RX_COVERAGE is still undefined and the page would
    // wrongly show "not enabled". Defer until server config is loaded (#13).
    Promise.resolve(window.MeshConfigReady).then(function () {
      if (!destroyed) start(container);
    });
  }

  function start(container) {
    if (!window.MC_CLIENT_RX_COVERAGE) {
      container.innerHTML = '<div class="nq-msg">Coverage is not enabled on this deployment.</div>';
      return;
    }
    selectedRx = ''; selectedName = ''; days = 7; boardCache = [];
    try {
      var p = (typeof getHashParams === 'function') ? getHashParams() : null;
      if (p) { var dd = parseInt(p.get('days'), 10); if ([1, 7, 14, 30].indexOf(dd) >= 0) days = dd; selectedRx = (p.get('rx') || '').toLowerCase(); }
    } catch (e) {}
    container.innerHTML = pageHtml();
    map = L.map('rxMap', { zoomControl: true, attributionControl: false }).setView([51.0, 4.8], 8);
    if (typeof window._applyTilesToNodeMap === 'function') window._applyTilesToNodeMap(map);
    else L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    covLayer = L.layerGroup().addTo(map);
    // Debounce pan/zoom redraws so dragging the map doesn't fire a storm of
    // /api/rx-coverage requests (#6). Direct calls (setDays, fit) stay immediate.
    map.on('moveend zoomend', debounce(drawCoverage, 200));
    var bar = document.getElementById('rxDays');
    if (bar) bar.addEventListener('click', function (e) { var b = e.target.closest('button[data-days]'); if (b) setDays(+b.dataset.days); });
    setTimeout(function () { if (!destroyed && map) { map.invalidateSize(); if (selectedRx) fitToObserver(); else drawCoverage(); } }, 150);
    loadBoard();
  }

  function destroy() {
    destroyed = true;
    if (map) { try { map.remove(); } catch (e) {} map = null; }
    covLayer = null;
  }

  registerPage('rx-coverage', { init: init, destroy: destroy });
})();
