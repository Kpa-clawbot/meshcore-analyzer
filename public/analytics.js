/* === CoreScope — analytics.js (v2 — full nerd mode) === */
'use strict';

(function () {
  let _analyticsData = {};
  const sf = (v, d) => (v != null ? v.toFixed(d) : '–'); // safe toFixed
  function esc(s) { return s ? String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;') : ''; }

  // #1085 — Roles tab helpers (hoisted from renderRolesTab so they're not
  // re-allocated per render).
  function _rolesEmoji(role) {
    if (window.ROLE_EMOJI && window.ROLE_EMOJI[role]) return window.ROLE_EMOJI[role];
    return '•';
  }
  function _rolesFmtSec(v) {
    if (!v && v !== 0) return '—';
    var abs = Math.abs(v);
    if (abs < 1) return v.toFixed(2) + 's';
    if (abs < 60) return v.toFixed(1) + 's';
    if (abs < 3600) return (v / 60).toFixed(1) + 'm';
    if (abs < 86400) return (v / 3600).toFixed(1) + 'h';
    return (v / 86400).toFixed(1) + 'd';
  }
  // #1085 — auto-refresh timer for the Roles tab. Started when the Roles
  // tab is rendered, cleared on tab switch and destroy.
  var _rolesRefreshTimer = null;
  function _stopRolesRefresh() {
    if (_rolesRefreshTimer) { clearInterval(_rolesRefreshTimer); _rolesRefreshTimer = null; }
  }
  var _scopesRefreshTimer = null;
  function _stopScopesRefresh() {
    if (_scopesRefreshTimer) { clearInterval(_scopesRefreshTimer); _scopesRefreshTimer = null; }
  }

  // --- Status color helpers (read from CSS variables for theme support) ---
  function cssVar(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
  function statusGreen() { return cssVar('--status-green') || '#22c55e'; }
  function statusYellow() { return cssVar('--status-yellow') || '#eab308'; }
  function statusRed() { return cssVar('--status-red') || '#ef4444'; }
  function accentColor() { return cssVar('--accent') || '#4a9eff'; }
  function snrColor(snr) { return snr > 6 ? statusGreen() : snr > 0 ? statusYellow() : statusRed(); }

  // --- SVG helpers ---
  function sparkSvg(data, color, w = 120, h = 32) {
    if (!data.length) return '';
    const max = Math.max(...data, 1);
    const pts = data.map((v, i) => {
      const x = i * (w / Math.max(data.length - 1, 1));
      const y = h - 2 - (v / max) * (h - 4);
      return `${x},${y}`;
    }).join(' ');
    return `<svg viewBox="0 0 ${w} ${h}" style="width:${w}px;height:${h}px" role="img" aria-label="Sparkline showing trend of ${data.length} data points"><title>Sparkline showing trend of ${data.length} data points</title><polyline points="${pts}" fill="none" stroke="${color}" stroke-width="1.5"/></svg>`;
  }

  function barChart(data, labels, colors, w = 800, h = 220, pad = 40) {
    const max = Math.max(...data, 1);
    const barW = Math.max(1, Math.min((w - pad * 2) / data.length - 2, 30));
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Bar chart showing data distribution"><title>Bar chart showing data distribution</title>`;
    // Grid
    for (let i = 0; i <= 4; i++) {
      const y = pad + (h - pad * 2) * i / 4;
      const val = Math.round(max * (4 - i) / 4);
      svg += `<line x1="${pad}" y1="${y}" x2="${w-pad}" y2="${y}" stroke="var(--border)" stroke-dasharray="2"/>`;
      svg += `<text x="${pad-4}" y="${y+4}" text-anchor="end" font-size="10" fill="var(--text-muted)">${val}</text>`;
    }
    data.forEach((v, i) => {
      const x = pad + i * ((w - pad * 2) / data.length) + barW / 2;
      const bh = (v / max) * (h - pad * 2);
      const y = h - pad - bh;
      const c = typeof colors === 'string' ? colors : colors[i % colors.length];
      svg += `<rect x="${x}" y="${y}" width="${barW}" height="${bh}" fill="${c}" rx="2"/>`;
      if (labels[i]) svg += `<text x="${x + barW/2}" y="${h - pad + 14}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${labels[i]}</text>`;
    });
    svg += '</svg>';
    return svg;
  }

  function histogram(data, bins, color, w = 800, h = 180) {
    // Support pre-computed histogram from server { bins: [{x, w, count}], min, max }
    if (data && data.bins && Array.isArray(data.bins)) {
      const buckets = data.bins.map(b => b.count);
      const labels = data.bins.map(b => b.x.toFixed(1));
      return { svg: barChart(buckets, labels, color, w, h), buckets, labels };
    }
    // Legacy: raw values array
    const values = data;
    const min = Math.min(...values), max = Math.max(...values);
    const step = (max - min) / bins;
    const buckets = Array(bins).fill(0);
    const labels = [];
    for (let i = 0; i < bins; i++) labels.push((min + step * i).toFixed(1));
    values.forEach(v => { const b = Math.min(Math.floor((v - min) / step), bins - 1); buckets[b]++; });
    return { svg: barChart(buckets, labels, color, w, h), buckets, labels };
  }

  // --- Main ---
  async function init(app) {
    app.innerHTML = `
      <div class="analytics-page">
        <div class="analytics-header">
          <h2><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Mesh Analytics</h2>
          <p class="text-muted">Deep dive into your mesh network data</p>
          <div class="analytics-filters">
            <div id="analyticsRegionFilter" class="region-filter-container"></div>
            <div id="analyticsAreaFilter" style="display:none"></div>
            <select id="analyticsTimeWindow" class="analytics-time-window-select" data-testid="analytics-time-window" aria-label="Time window">
              <option value="">All data</option>
              <option value="1h">Last 1 hour</option>
              <option value="24h">Last 24 hours</option>
              <option value="7d">Last 7 days</option>
              <option value="30d">Last 30 days</option>
            </select>
          </div>
          <div class="analytics-tabs" id="analyticsTabs" role="tablist" aria-label="Analytics tabs">
            <button class="tab-btn active" data-tab="overview">Overview</button>
            <button class="tab-btn" data-tab="rf">RF / Signal</button>
            <button class="tab-btn" data-tab="topology">Topology</button>
            <button class="tab-btn" data-tab="channels">Channels</button>
            <button class="tab-btn" data-tab="hashsizes">Hash Stats</button>
            <button class="tab-btn" data-tab="collisions">Hash Issues</button>
            <button class="tab-btn" data-tab="subpaths">Route Patterns</button>
            <button class="tab-btn" data-tab="nodes">Nodes</button>
            <button class="tab-btn" data-tab="my-repeaters">My Repeaters</button>
            <button class="tab-btn" data-tab="repeater-metrics">Repeater Metrics</button>
            <button class="tab-btn" data-tab="distance">Distance</button>
            <button class="tab-btn" data-tab="neighbor-graph">Neighbor Graph</button>
            <button class="tab-btn" data-tab="rf-health">RF Health</button>
            <button class="tab-btn" data-tab="clock-health">Clock Health</button>
            <!-- #1085 — Roles tab folded in from former /#/roles standalone page.
                 Placed after Clock Health (clock-skew posture is shown per-role
                 inside this tab) and before Prefix Tool (utility tabs trail). -->
            <button class="tab-btn" data-tab="roles">Roles</button>
            <button class="tab-btn" data-tab="scopes">Scopes</button>
            <button class="tab-btn" data-tab="prefix-tool">Prefix Tool</button>
          </div>
        </div>
        <div id="analyticsContent" class="analytics-content" aria-live="polite">
          <div class="text-center text-muted" style="padding:40px">Loading analytics…</div>
        </div>
      </div>`;

    // Tabs where the area filter is meaningful (transmitter GPS attribution)
    const AREA_FILTER_TABS = new Set(['overview', 'rf', 'topology', 'hashsizes', 'collisions', 'nodes', 'my-repeaters', 'repeater-metrics', 'clock-health']);

    function setAreaFilterVisibility(tab) {
      const el = document.getElementById('analyticsAreaFilter');
      if (el) el.style.display = AREA_FILTER_TABS.has(tab) ? '' : 'none';
    }

    // Tab handling
    const analyticsTabs = document.getElementById('analyticsTabs');
    initTabBar(analyticsTabs);
    // #749 — keep analytics tab + window in URL for deep-linking.
    function _updateAnalyticsUrl() {
      if (!window.URLState) return;
      var twElNow = document.getElementById('analyticsTimeWindow');
      var updates = {
        tab: _currentTab && _currentTab !== 'overview' ? _currentTab : '',
        window: twElNow && twElNow.value ? twElNow.value : ''
      };
      // Drop any subview-specific keys that don't belong to the active tab
      // so switching tabs gives a clean URL. (rf-health uses 'range', 'observer', 'from', 'to')
      if (_currentTab !== 'rf-health') {
        var cleared = ['range', 'observer', 'from', 'to'];
        for (var i = 0; i < cleared.length; i++) updates[cleared[i]] = '';
      }
      var newHash = URLState.updateHashParams(updates, location.hash);
      if (newHash !== location.hash) history.replaceState(null, '', newHash);
    }

    analyticsTabs.addEventListener('click', e => {
      const btn = e.target.closest('.tab-btn');
      if (!btn) return;
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      _currentTab = btn.dataset.tab;
      setAreaFilterVisibility(_currentTab);
      // #1085 — Roles tab owns its own 60s auto-refresh; stop it on switch.
      if (_currentTab !== 'roles') _stopRolesRefresh();
      if (_currentTab !== 'scopes') _stopScopesRefresh();
      _updateAnalyticsUrl();
      renderTab(_currentTab);
    });

    // Deep-link: #/analytics?tab=collisions&window=7d
    const hashParams = location.hash.split('?')[1] || '';
    const _ap = new URLSearchParams(hashParams);
    const urlTab = _ap.get('tab');
    if (urlTab) {
      const tabBtn = analyticsTabs.querySelector(`[data-tab="${urlTab}"]`);
      if (tabBtn) {
        analyticsTabs.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
        tabBtn.classList.add('active');
        _currentTab = urlTab;
      }
    }
    // #749 — restore time window from URL.
    const urlWindow = _ap.get('window');
    if (urlWindow) {
      const twInit = document.getElementById('analyticsTimeWindow');
      if (twInit) twInit.value = urlWindow;
    }

    RegionFilter.init(document.getElementById('analyticsRegionFilter'));
    AreaFilter.init(document.getElementById('analyticsAreaFilter'));
    setAreaFilterVisibility(_currentTab);
    RegionFilter.onChange(function () { loadAnalytics(); });
    AreaFilter.onChange(function () { loadAnalytics(); });

    // Time-window picker (#842) — refresh analytics on change.
    const tw = document.getElementById('analyticsTimeWindow');
    if (tw) {
      tw.addEventListener('change', function () { _updateAnalyticsUrl(); loadAnalytics(); });
    }

    // Delegated click/keyboard handler for clickable table rows
    const analyticsContent = document.getElementById('analyticsContent');
    if (analyticsContent) {
      const handler = (e) => {
        const row = e.target.closest('tr[data-action="navigate"]');
        if (!row) return;
        if (e.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return;
        if (e.type === 'keydown') e.preventDefault();
        location.hash = row.dataset.value;
      };
      analyticsContent.addEventListener('click', handler);
      analyticsContent.addEventListener('keydown', handler);
    }

    // Re-render when distance unit or theme changes
    _themeRefreshHandler = function () {
      // #1925: never full-rebuild the neighbor-graph tab on theme-refresh.
      // Every page load fires one theme-refresh ~300ms after
      // /api/config/theme resolves (app.js dispatches 'theme-changed', then
      // debounces 300ms). renderTab() here replaced el.innerHTML, which reset
      // the role checkboxes to their defaults and rebuilt _ngState from the
      // full graph, discarding any filtering the user had applied in the
      // meantime. Restarting the renderer keeps the current filter state and
      // still picks up the new theme: node colors are read live per frame
      // from window.ROLE_COLORS, role swatches use CSS tokens, and the one
      // cached value (_labelColor) is re-read on restart.
      if (_currentTab === 'neighbor-graph' && _ngState) { startGraphRenderer(); return; }
      renderTab(_currentTab);
    };
    window.addEventListener('theme-refresh', _themeRefreshHandler);

    loadAnalytics();
  }

  var _themeRefreshHandler = null;
  let _currentTab = 'overview';

  async function loadAnalytics() {
    try {
      _analyticsData = {};
      const rqs = RegionFilter.regionQueryString(); // "&region=..." or ""
      const aqs = AreaFilter.areaQueryString();     // "&area=..." or ""
      // Time window picker (#842) — append &window=… when set.
      // NOTE: only the three window-aware endpoints (rf/topology/channels)
      // receive ?window=…; hash-sizes and hash-collisions are about node
      // identity / hash-byte distribution and intentionally span all data.
      const twEl = document.getElementById('analyticsTimeWindow');
      const twVal = twEl ? twEl.value : '';
      const tws = twVal ? '&window=' + encodeURIComponent(twVal) : '';
      // hash-sizes / hash-collisions: region + area, no window
      const baseQS = (rqs + aqs).slice(1);
      const sepBase = baseQS ? '?' + baseQS : '';
      // rf / topology: region + area + window
      const windowedQS = (rqs + aqs + tws).slice(1);
      const sepWin = windowedQS ? '?' + windowedQS : '';
      // channels: region + window (no area per original PR intent)
      const chanQS = (rqs + tws).slice(1);
      const sepChan = chanQS ? '?' + chanQS : '';
      const [hashData, rfData, topoData, chanData, collisionData, airtimeData] = await Promise.all([
        api('/analytics/hash-sizes' + sepBase, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/rf' + sepWin, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/topology' + sepWin, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/channels' + sepChan, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/hash-collisions' + sepBase, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/relay-airtime-share' + sepWin, { ttl: CLIENT_TTL.analyticsRF }).catch(() => ({ rows: [] })),
      ]);
      _analyticsData = { hashData, rfData, topoData, chanData, collisionData, airtimeData };
      renderTab(_currentTab);
    } catch (e) {
      document.getElementById('analyticsContent').innerHTML =
        `<div class="text-muted" role="alert" aria-live="polite" style="padding:40px">Failed to load: ${e.message}</div>`;
    }
  }

  async function renderTab(tab) {
    const el = document.getElementById('analyticsContent');
    const d = _analyticsData;
    switch (tab) {
      case 'overview': renderOverview(el, d); break;
      case 'rf': renderRF(el, d.rfData); break;
      case 'topology': renderTopology(el, d.topoData); break;
      case 'channels': renderChannels(el, d.chanData); break;
      case 'hashsizes': renderHashSizes(el, d.hashData); break;
      case 'collisions': await renderCollisionTab(el, d.hashData, d.collisionData); break;
      case 'subpaths': await renderSubpaths(el); break;
      case 'nodes': await renderNodesTab(el); break;
      case 'my-repeaters': await renderMyRepeatersTab(el); break;
      case 'repeater-metrics': await renderRepeaterMetricsTab(el); break;
      case 'distance': await renderDistanceTab(el); break;
      case 'neighbor-graph': await renderNeighborGraphTab(el); break;
      case 'rf-health': await renderRFHealthTab(el); break;
      case 'clock-health': await renderClockHealthTab(el); break;
      case 'roles': await renderRolesTab(el); break;
      case 'prefix-tool': await renderPrefixTool(el); break;
      case 'scopes': await renderScopesTab(el); break;
    }
    // Auto-apply column resizing to all analytics tables
    requestAnimationFrame(() => {
      el.querySelectorAll('.analytics-table').forEach((tbl, i) => {
        tbl.id = tbl.id || `analytics-tbl-${tab}-${i}`;
        if (typeof makeColumnsResizable === 'function') makeColumnsResizable('#' + tbl.id, `meshcore-analytics-${tab}-${i}-col-widths`);
      });
      // #206 — Wrap analytics tables in scroll containers on mobile
      el.querySelectorAll('.analytics-table').forEach(tbl => {
        if (!tbl.parentElement.classList.contains('analytics-table-scroll')) {
          const wrapper = document.createElement('div');
          wrapper.className = 'analytics-table-scroll';
          tbl.parentElement.insertBefore(wrapper, tbl);
          wrapper.appendChild(tbl);
        }
      });
    });
    // Deep-link scroll to section within tab
    const sectionId = new URLSearchParams((location.hash.split('?')[1] || '')).get('section');
    if (sectionId) {
      setTimeout(() => {
        const target = document.getElementById(sectionId);
        if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }, 400);
    }
  }

  // ===================== OVERVIEW =====================
  function renderOverview(el, d) {
    const rf = d.rfData, topo = d.topoData, ch = d.chanData, hs = d.hashData;
    el.innerHTML = `
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-value">${(rf.totalTransmissions || rf.totalAllPackets || rf.totalPackets).toLocaleString()}</div>
          <div class="stat-label">Total Transmissions</div>
          <div class="stat-spark">${sparkSvg(rf.packetsPerHour.map(h=>h.count), 'var(--accent)')}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${rf.totalPackets.toLocaleString()}</div>
          <div class="stat-label">Observations with Signal</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${topo.uniqueNodes}</div>
          <div class="stat-label">Unique Nodes</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${sf(rf.snr.avg, 1)} dB</div>
          <div class="stat-label">Avg SNR</div>
          <div class="stat-detail">${sf(rf.snr.min, 1)} to ${sf(rf.snr.max, 1)}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${sf(rf.rssi.avg, 0)} dBm</div>
          <div class="stat-label">Avg RSSI</div>
          <div class="stat-detail">${rf.rssi.min} to ${rf.rssi.max}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${sf(topo.avgHops, 1)}</div>
          <div class="stat-label">Avg Hops</div>
          <div class="stat-detail">max ${topo.maxHops}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${ch.activeChannels}</div>
          <div class="stat-label">Active Channels</div>
          <div class="stat-detail">${ch.decryptable} decryptable</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${rf.avgPacketSize} B</div>
          <div class="stat-label">Avg Packet Size</div>
          <div class="stat-detail">${rf.minPacketSize}–${rf.maxPacketSize} B</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${((rf.timeSpanHours || 1)).toFixed(1)}h</div>
          <div class="stat-label">Data Span</div>
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trend-up"/></svg> Packets / Hour</h3>
          ${(() => {
            const pph = rf.packetsPerHour;
            const counts = pph.map(h => h.count);
            // Decimate x-axis labels to avoid overlap
            const totalHours = pph.length;
            // Pick label interval: <=24h show every 6h, <=72h every 12h, else every 24h
            const labelInterval = totalHours <= 24 ? 6 : totalHours <= 72 ? 12 : 24;
            const labels = pph.map((h, i) => {
              const hh = h.hour.slice(11, 13); // "HH"
              const hourNum = parseInt(hh, 10);
              if (hourNum % labelInterval === 0) {
                // For multi-day ranges, show date on 00h boundaries
                if (totalHours > 48 && hourNum === 0) return h.hour.slice(5, 10);
                return hh + 'h';
              }
              return ''; // skip label
            });
            return barChart(counts, labels, 'var(--accent)');
          })()}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-package"/></svg> Payload Type Mix</h3>
          ${renderPayloadPie(rf.payloadTypes)}
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-link"/></svg> Hop Count Distribution</h3>
          ${barChart(topo.hopDistribution.map(h=>h.count), topo.hopDistribution.map(h=>h.hops), ['#3b82f6'])}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> Relay Airtime Share</h3>
          <p class="text-muted" style="margin-top:-4px;font-size:12px">Score = LoRa Time-on-Air × distinct repeaters that forwarded the packet (issue #1768). Counts relay re-transmissions; originator TX excluded. Not comparable across meshes — see preset caption below.</p>
          ${renderRelayAirtimeDumbbell(d.airtimeData)}
        </div>
      </div>
    `;

    // Affinity stats widget — fetch and append if debugAffinity enabled
    var showDebug = (window.CLIENT_CONFIG && window.CLIENT_CONFIG.debugAffinity) || localStorage.getItem('meshcore-affinity-debug') === 'true';
    if (showDebug) {
      var apiKey = localStorage.getItem('meshcore-api-key') || '';
      fetch('/api/debug/affinity', { headers: { 'X-API-Key': apiKey } })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (data) {
          if (!data || !data.stats) return;
          var s = data.stats;
          var total = s.resolvedCount + s.ambiguousCount + s.unresolvedCount;
          var resolvedPct = total > 0 ? (s.resolvedCount / total * 100).toFixed(1) : '0.0';
          var ambiguousPct = total > 0 ? (s.ambiguousCount / total * 100).toFixed(1) : '0.0';
          var widget = document.createElement('div');
          widget.className = 'analytics-row';
          widget.innerHTML = '<div class="analytics-card flex-1">' +
            '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> Neighbor Affinity Graph</h3>' +
            '<div class="stats-grid">' +
            '<div class="stat-card"><div class="stat-value">' + s.totalEdges + '</div><div class="stat-label">Total Edges</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + s.totalNodes + '</div><div class="stat-label">Total Nodes</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + s.resolvedCount + ' <span style="font-size:12px;color:var(--text-muted)">(' + resolvedPct + '%)</span></div><div class="stat-label">Resolved Prefixes</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + s.ambiguousCount + ' <span style="font-size:12px;color:var(--text-muted)">(' + ambiguousPct + '%)</span></div><div class="stat-label">Ambiguous Prefixes</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (s.avgConfidence || 0).toFixed(3) + '</div><div class="stat-label">Avg Confidence</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (s.coldStartCoverage || 0).toFixed(1) + '%</div><div class="stat-label">Cold-Start Coverage</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (s.cacheAge || 'N/A') + '</div><div class="stat-label">Cache Age</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (s.lastRebuild ? s.lastRebuild.substring(0, 19) : 'N/A') + '</div><div class="stat-label">Last Rebuild</div></div>' +
            '</div></div>';
          el.appendChild(widget);
        })
        .catch(function () {});
    }
  }

  function renderPayloadPie(types) {
    const total = types.reduce((s, t) => s + t.count, 0);
    const colors = ['#ef4444','#f59e0b','#22c55e','#3b82f6','#8b5cf6','#ec4899','#14b8a6','#64748b','#f97316','#06b6d4','#84cc16'];
    let html = '<div class="payload-bars">';
    types.forEach((t, i) => {
      const pct = (t.count / total * 100).toFixed(1);
      const w = Math.max(t.count / total * 100, 1);
      html += `<div class="payload-bar-row">
        <div class="payload-bar-label"><span class="legend-dot" style="background:${colors[i]}"></span>${escapeHtml(t.name)}</div>
        <div class="hash-bar-track"><div class="hash-bar-fill" style="width:${w}%;background:${colors[i]}"></div></div>
        <div class="payload-bar-value">${t.count} <span class="text-muted">(${pct}%)</span></div>
      </div>`;
    });
    return html + '</div>';
  }

  // === Relay Airtime Share — dumbbell chart (#1359) ===
  // Two dots per payload_type row: gray = count %, colored = airtime %.
  // Connector line between them = the divergence. Shared 0–100% axis.
  // Sorted desc by airtime (server-side) so count dots visibly scatter
  // out of rank order — that visible disorder IS the headline.
  function renderRelayAirtimeDumbbell(data) {
    var rows = (data && Array.isArray(data.rows)) ? data.rows : [];
    if (!rows.length) {
      return '<div class="text-muted" style="padding:20px">No relay-airtime data in this window.</div>';
    }
    var totalScore = (data && typeof data.total_score === 'number') ? data.total_score : 0;
    if (totalScore <= 0) {
      return '<div class="text-muted" style="padding:20px">No relay activity observed in this window (all packets direct).</div>';
    }
    // Issue #1768 — surface the LoRa preset baked into the ToA score. Share
    // numbers are only meaningful relative to one PHY preset; operators must
    // know what was assumed. All fields are run through esc() defensively
    // (preset.sf/cr/preamble/bw_khz/freq_hz arrive from the server JSON,
    // which the operator can configure — never inject untrusted text raw).
    // BW formatted consistent with home.js / customize-v2.js presets
    // (e.g. `62.5 kHz`, `125 kHz`) — strip trailing `.0` for integer kHz.
    var preset = data && data.preset;
    var presetCaption = '';
    if (preset && typeof preset === 'object') {
      var freqMHz = Number(preset.freq_hz || 0) / 1e6;
      var bwKhz = Number(preset.bw_khz || 0);
      var bwStr = bwKhz ? bwKhz.toFixed(1).replace(/\.0$/, '') : '0';
      presetCaption =
        '<div class="dumbbell-preset text-muted" style="font-size:11px;padding:0 4px 6px 4px">' +
        'Assumed LoRa preset: ' +
        (freqMHz ? esc(freqMHz.toFixed(3)) + ' MHz / ' : '') +
        'BW ' + esc(bwStr) + ' kHz / ' +
        'SF ' + esc(String(Number(preset.sf || 0))) + ' / ' +
        'CR 4/' + esc(String(Number(preset.cr || 0))) +
        ' (preamble ' + esc(String(Number(preset.preamble || 0))) + ' sym)' +
        '</div>';
    }
    // Layout: per row → label | track 0..100% | values
    var palette = ['#ef4444','#f59e0b','#22c55e','#3b82f6','#8b5cf6','#ec4899','#14b8a6','#64748b','#f97316','#06b6d4','#84cc16'];
    var html = '<div class="dumbbell-chart" style="display:flex;flex-direction:column;gap:8px;padding:8px 4px">';
    if (presetCaption) html += presetCaption;
    rows.forEach(function (r, i) {
      var name = r.payload_type || 'UNK';
      var cnt = Number(r.count || 0);
      var cpct = Number(r.count_pct || 0);
      var apct = Number(r.airtime_pct || 0);
      var score = Number(r.score || 0);
      var color = palette[i % palette.length];
      var loPct = Math.min(cpct, apct);
      var hiPct = Math.max(cpct, apct);
      // Tooltip per row — payload_type, count %, count N, airtime %, raw score, caveat.
      // Score is summed (ToA in ns) × distinct repeaters across packets of
      // this type; converted to ms for human-readable display per #1768
      // round-1 review.
      var scoreMs = score / 1e6;
      var scoreStr = scoreMs >= 1000
        ? (scoreMs / 1000).toFixed(2) + ' s'
        : scoreMs.toFixed(2) + ' ms';
      var tip =
        name + '\n' +
        'Count: ' + cnt.toLocaleString() + ' (' + cpct.toFixed(2) + '%)\n' +
        'Airtime: ' + apct.toFixed(2) + '% (score ' + scoreStr + ' · airtime × repeaters)\n' +
        'Score = LoRa Time-on-Air × distinct repeaters. Within-mesh only.';
      html += '<div class="dumbbell-row" title="' + esc(tip) + '" style="display:grid;grid-template-columns:80px 1fr 180px;align-items:center;gap:10px;font-size:12px">' +
        '<div class="dumbbell-label" style="font-weight:600;color:var(--text)">' + esc(name) + '</div>' +
        '<div class="dumbbell-track" style="position:relative;height:18px;background:var(--bg-elev,rgba(127,127,127,0.12));border-radius:9px">' +
          '<div class="dumbbell-connector" style="position:absolute;top:50%;left:' + loPct.toFixed(3) + '%;width:' + (hiPct - loPct).toFixed(3) + '%;height:2px;background:var(--text-muted,#888);transform:translateY(-50%);opacity:0.5"></div>' +
          '<div class="dumbbell-dot dumbbell-dot-count" style="position:absolute;top:50%;left:' + cpct.toFixed(3) + '%;width:10px;height:10px;border-radius:50%;background:var(--text-muted,#888);transform:translate(-50%,-50%);border:1px solid var(--bg,#fff)" aria-label="count share"></div>' +
          '<div class="dumbbell-dot dumbbell-dot-airtime" style="position:absolute;top:50%;left:' + apct.toFixed(3) + '%;width:12px;height:12px;border-radius:50%;background:' + color + ';transform:translate(-50%,-50%);border:1px solid var(--bg,#fff)" aria-label="airtime share"></div>' +
        '</div>' +
        '<div class="dumbbell-values" style="text-align:right;color:var(--text-muted);font-variant-numeric:tabular-nums">' +
          '<span style="color:var(--text-muted)">cnt ' + cpct.toFixed(1) + '%</span>' +
          ' &nbsp;·&nbsp; ' +
          '<span style="display:inline-flex;align-items:center;gap:4px;color:var(--text);font-weight:600"><span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:' + color + '" aria-hidden="true"></span>air ' + apct.toFixed(1) + '%</span>' +
        '</div>' +
      '</div>';
    });
    // Axis legend.
    html += '<div class="dumbbell-axis" style="display:grid;grid-template-columns:80px 1fr 180px;gap:10px;color:var(--text-muted);font-size:11px;margin-top:4px">' +
      '<div></div>' +
      '<div style="display:flex;justify-content:space-between"><span>0%</span><span>50%</span><span>100%</span></div>' +
      '<div></div>' +
    '</div>';
    html += '<div class="dumbbell-legend" style="display:flex;gap:14px;font-size:11px;color:var(--text-muted);padding:4px 0 0 84px"><span><span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--text-muted,#888);vertical-align:middle"></span> count %</span><span><span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:var(--accent,#3b82f6);vertical-align:middle"></span> airtime %</span></div>';
    html += '</div>';
    return html;
  }

  // ===================== RF / SIGNAL =====================
  function renderRF(el, rf) {
    const snrHist = histogram(rf.snrValues, 20, statusGreen());
    const rssiHist = histogram(rf.rssiValues, 20, accentColor());

    el.innerHTML = `
      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-cell-signal-high"/></svg> SNR Distribution</h3>
          <p class="text-muted">Signal-to-Noise Ratio (higher = cleaner signal)</p>
          ${snrHist.svg}
          <div class="rf-stats">
            <span>Min: <strong>${sf(rf.snr.min, 1)} dB</strong></span>
            <span>Mean: <strong>${sf(rf.snr.avg, 1)} dB</strong></span>
            <span>Median: <strong>${sf(rf.snr.median, 1)} dB</strong></span>
            <span>Max: <strong>${sf(rf.snr.max, 1)} dB</strong></span>
            <span>σ: <strong>${sf(rf.snr.stddev, 1)} dB</strong></span>
          </div>
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> RSSI Distribution</h3>
          <p class="text-muted">Received Signal Strength (closer to 0 = stronger)</p>
          ${rssiHist.svg}
          <div class="rf-stats">
            <span>Min: <strong>${rf.rssi.min} dBm</strong></span>
            <span>Mean: <strong>${sf(rf.rssi.avg, 0)} dBm</strong></span>
            <span>Median: <strong>${rf.rssi.median} dBm</strong></span>
            <span>Max: <strong>${rf.rssi.max} dBm</strong></span>
            <span>σ: <strong>${sf(rf.rssi.stddev, 1)} dBm</strong></span>
          </div>
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-target"/></svg> SNR vs RSSI Scatter</h3>
          <p class="text-muted">Each dot = one packet. Cluster position reveals link quality.</p>
          ${renderScatter(rf.scatterData)}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> SNR by Payload Type</h3>
          ${renderSNRByType(rf.snrByType)}
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trend-up"/></svg> Signal Quality Over Time</h3>
          ${renderSignalTimeline(rf.signalOverTime)}
        </div>
      </div>

      <div class="analytics-card">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-ruler"/></svg> Packet Size Distribution</h3>
        <p class="text-muted">Raw packet length in bytes</p>
        ${histogram(rf.packetSizes, 25, '#8b5cf6').svg}
        <div class="rf-stats">
          <span>Min: <strong>${rf.minPacketSize} B</strong></span>
          <span>Avg: <strong>${rf.avgPacketSize} B</strong></span>
          <span>Max: <strong>${rf.maxPacketSize} B</strong></span>
        </div>
      </div>
    `;
  }

  function renderScatter(data) {
    const w = 600, h = 300, pad = 40;
    const snrMin = -12, snrMax = 15, rssiMin = -130, rssiMax = -5;
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:300px" role="img" aria-label="SNR vs RSSI scatter plot showing signal quality distribution"><title>SNR vs RSSI scatter plot showing signal quality distribution</title>`;
    // Axes
    svg += `<line x1="${pad}" y1="${h-pad}" x2="${w-pad}" y2="${h-pad}" stroke="var(--text-muted)" stroke-width="0.5"/>`;
    svg += `<line x1="${pad}" y1="${pad}" x2="${pad}" y2="${h-pad}" stroke="var(--text-muted)" stroke-width="0.5"/>`;
    svg += `<text x="${w/2}" y="${h-5}" text-anchor="middle" font-size="11" fill="var(--text-muted)">SNR (dB)</text>`;
    svg += `<text x="12" y="${h/2}" text-anchor="middle" font-size="11" fill="var(--text-muted)" transform="rotate(-90,12,${h/2})">RSSI (dBm)</text>`;
    // Grid labels
    for (let snr = -10; snr <= 14; snr += 4) {
      const x = pad + (snr - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      svg += `<text x="${x}" y="${h-pad+14}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${snr}</text>`;
    }
    for (let rssi = -120; rssi <= -20; rssi += 20) {
      const y = h - pad - (rssi - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      svg += `<text x="${pad-4}" y="${y+3}" text-anchor="end" font-size="9" fill="var(--text-muted)">${rssi}</text>`;
    }
    // Quality zones
    const _sg = statusGreen(), _sy = statusYellow(), _sr = statusRed();
    const zones = [
      { label: 'Excellent', snr: [6, 15], rssi: [-80, -5], color: _sg + '20' },
      { label: 'Good', snr: [0, 6], rssi: [-100, -80], color: _sy + '15' },
      { label: 'Weak', snr: [-12, 0], rssi: [-130, -100], color: _sr + '10' },
    ];
    // Define patterns for color-blind accessibility
    svg += `<defs>`;
    svg += `<pattern id="pat-excellent" patternUnits="userSpaceOnUse" width="8" height="8"><line x1="0" y1="8" x2="8" y2="0" stroke="${_sg}" stroke-width="0.5" opacity="0.4"/></pattern>`;
    svg += `<pattern id="pat-good" patternUnits="userSpaceOnUse" width="6" height="6"><circle cx="3" cy="3" r="1" fill="${_sy}" opacity="0.4"/></pattern>`;
    svg += `<pattern id="pat-weak" patternUnits="userSpaceOnUse" width="8" height="8"><line x1="0" y1="0" x2="8" y2="8" stroke="${_sr}" stroke-width="0.5" opacity="0.4"/><line x1="0" y1="8" x2="8" y2="0" stroke="${_sr}" stroke-width="0.5" opacity="0.4"/></pattern>`;
    svg += `</defs>`;
    const zonePatterns = { 'Excellent': 'pat-excellent', 'Good': 'pat-good', 'Weak': 'pat-weak' };
    const zoneDash = { 'Excellent': '4,2', 'Good': '6,3', 'Weak': '2,2' };
    const zoneBorder = { 'Excellent': _sg, 'Good': _sy, 'Weak': _sr };
    zones.forEach(z => {
      const x1 = pad + (z.snr[0] - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      const x2 = pad + (z.snr[1] - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      const y1 = h - pad - (z.rssi[1] - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      const y2 = h - pad - (z.rssi[0] - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      svg += `<rect x="${x1}" y="${y1}" width="${x2-x1}" height="${y2-y1}" fill="${z.color}"/>`;
      svg += `<rect x="${x1}" y="${y1}" width="${x2-x1}" height="${y2-y1}" fill="url(#${zonePatterns[z.label]})"/>`;
      svg += `<rect x="${x1}" y="${y1}" width="${x2-x1}" height="${y2-y1}" fill="none" stroke="${zoneBorder[z.label]}" stroke-width="1" stroke-dasharray="${zoneDash[z.label]}" opacity="0.6"/>`;
      svg += `<text x="${x1+4}" y="${y1+12}" font-size="9" fill="var(--text-muted)" opacity="0.7">${z.label}</text>`;
    });
    // Dots (sample if too many)
    const sample = data.length > 500 ? data.filter((_, i) => i % Math.ceil(data.length / 500) === 0) : data;
    sample.forEach(d => {
      const x = pad + (d.snr - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      const y = h - pad - (d.rssi - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      svg += `<circle cx="${x}" cy="${y}" r="2" fill="var(--accent)" opacity="0.5"/>`;
    });
    svg += '</svg>';
    return svg;
  }

  function renderSNRByType(snrByType) {
    if (!snrByType.length) return '<div class="text-muted">No data</div>';
    let html = '<table class="analytics-table"><thead><tr><th scope="col">Type</th><th scope="col">Packets</th><th scope="col">Avg SNR</th><th scope="col">Min</th><th scope="col">Max</th><th scope="col">Distribution</th></tr></thead><tbody>';
    snrByType.forEach(t => {
      const barPct = Math.max(((t.avg - (-12)) / 27) * 100, 2);
      const color = t.avg > 6 ? statusGreen() : t.avg > 0 ? statusYellow() : statusRed();
      html += `<tr>
        <td><strong>${escapeHtml(t.name)}</strong></td>
        <td>${t.count}</td>
        <td><strong>${sf(t.avg, 1)} dB</strong></td>
        <td>${sf(t.min, 1)}</td>
        <td>${sf(t.max, 1)}</td>
        <td><div class="hash-bar-track" style="height:14px"><div class="hash-bar-fill" style="width:${barPct}%;background:${color};height:100%"></div></div></td>
      </tr>`;
    });
    return html + '</tbody></table>';
  }

  function renderSignalTimeline(data) {
    if (!data.length) return '<div class="text-muted">No data</div>';
    const w = 400, h = 160, pad = 35;
    const maxPkts = Math.max(...data.map(d => d.count), 1);
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:160px" role="img" aria-label="Signal quality over time showing SNR trend and packet volume"><title>Signal quality over time showing SNR trend and packet volume</title>`;
    const snrPts = data.map((d, i) => {
      const x = pad + i * ((w - pad * 2) / Math.max(data.length - 1, 1));
      const y = h - pad - ((d.avgSnr + 12) / 27) * (h - pad * 2);
      return `${x},${y}`;
    }).join(' ');
    svg += `<polyline points="${snrPts}" fill="none" stroke="${statusGreen()}" stroke-width="2"/>`;
    // Packet count as area
    const areaPts = data.map((d, i) => {
      const x = pad + i * ((w - pad * 2) / Math.max(data.length - 1, 1));
      const y = h - pad - (d.count / maxPkts) * (h - pad * 2) * 0.4;
      return `${x},${y}`;
    });
    const baseline = data.map((_, i) => {
      const x = pad + i * ((w - pad * 2) / Math.max(data.length - 1, 1));
      return `${x},${h - pad}`;
    });
    svg += `<polygon points="${areaPts.join(' ')} ${baseline.reverse().join(' ')}" fill="var(--accent)" opacity="0.15"/>`;
    // Labels
    const step = Math.max(1, Math.floor(data.length / 6));
    for (let i = 0; i < data.length; i += step) {
      const x = pad + i * ((w - pad * 2) / Math.max(data.length - 1, 1));
      svg += `<text x="${x}" y="${h-pad+14}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${data[i].hour.slice(11)}h</text>`;
    }
    svg += '</svg>';
    svg += `<div class="timeline-legend"><span><span class="legend-dot" style="background:${statusGreen()}"></span>Avg SNR</span><span><span class="legend-dot" style="background:var(--accent);opacity:0.3"></span>Volume</span></div>`;
    return svg;
  }

  // ===================== TOPOLOGY =====================
  function renderTopology(el, topo) {
    el.innerHTML = `
      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-link"/></svg> Hop Count Distribution</h3>
          <p class="text-muted">Number of repeater hops per packet</p>
          ${barChart(topo.hopDistribution.map(h=>h.count), topo.hopDistribution.map(h=>h.hops), ['#3b82f6'])}
          <div class="rf-stats">
            <span>Avg: <strong>${sf(topo.avgHops, 1)} hops</strong></span>
            <span>Median: <strong>${topo.medianHops}</strong></span>
            <span>Max: <strong>${topo.maxHops}</strong></span>
            <span>1-hop direct: <strong>${topo.hopDistribution[0]?.count || 0}</strong></span>
          </div>
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-graph"/></svg> Top Repeaters</h3>
          <p class="text-muted">Nodes appearing most in packet paths</p>
          ${renderRepeaterTable(topo.topRepeaters)}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-handshake"/></svg> Repeater Pair Heatmap</h3>
          <p class="text-muted">Which repeaters frequently appear together in paths</p>
          ${renderPairTable(topo.topPairs)}
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Hops vs SNR</h3>
          <p class="text-muted">Does more hops = worse signal?</p>
          ${renderHopsSNR(topo.hopsVsSnr)}
        </div>
      </div>

      <div class="analytics-card">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trophy"/></svg> Best Path to Each Node</h3>
        <p class="text-muted">Shortest hop distance seen across all observers</p>
        ${renderBestPath(topo.bestPathList)}
      </div>

      <div class="analytics-card">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-globe"/></svg> Per-Observer Reachability</h3>
        <p class="text-muted">Nodes at each hop distance, from each observer's perspective</p>
        ${topo.observers.length > 1 ? `<div class="observer-selector" id="obsSelector">
          ${topo.observers.map((o, i) => `<button class="tab-btn ${i === 0 ? 'active' : ''}" data-obs="${o.id}">${esc(o.name)}</button>`).join('')}
          <button class="tab-btn" data-obs="__all">All Observers</button>
        </div>` : ''}
        <div id="reachContent">${renderPerObserverReach(topo.perObserverReach, topo.observers[0]?.id)}</div>
      </div>

      ${topo.multiObsNodes.length ? `<div class="analytics-card">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-shuffle"/></svg> Cross-Observer Comparison</h3>
        <p class="text-muted">Nodes seen by multiple observers — hop distance varies by vantage point</p>
        ${renderCrossObserver(topo.multiObsNodes)}
      </div>` : ''}
    `;

    // Observer selector event handling
    const selector = document.getElementById('obsSelector');
    if (selector) {
      initTabBar(selector);
      selector.addEventListener('click', e => {
        const btn = e.target.closest('.tab-btn');
        if (!btn) return;
        selector.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        const obsId = btn.dataset.obs;
        document.getElementById('reachContent').innerHTML =
          obsId === '__all' ? renderAllObserversReach(topo.perObserverReach) : renderPerObserverReach(topo.perObserverReach, obsId);
      });
    }
  }

  function renderRepeaterTable(repeaters) {
    if (!repeaters.length) return '<div class="text-muted">No data</div>';
    const max = repeaters[0].count;
    let html = '<div class="repeater-list">';
    repeaters.slice(0, 15).forEach(r => {
      const pct = (r.count / max * 100).toFixed(0);
      html += `<div class="repeater-row ${r.pubkey ? 'clickable-row' : ''}" ${r.pubkey ? `onclick="location.hash='#/nodes/${encodeURIComponent(r.pubkey)}'"` : ''}>
        <div class="repeater-name">${r.name ? '<strong>' + esc(r.name) + '</strong>' : '<span class="mono">' + r.hop + '</span>'}</div>
        <div class="repeater-bar"><div class="hash-bar-track"><div class="hash-bar-fill" style="width:${pct}%;background:var(--accent)"></div></div></div>
        <div class="repeater-count">${r.count.toLocaleString()}</div>
      </div>`;
    });
    return html + '</div>';
  }

  function renderPairTable(pairs) {
    if (!pairs.length) return '<div class="text-muted">Not enough multi-hop data</div>';
    let html = '<table class="analytics-table"><thead><tr><th scope="col">Node A</th><th scope="col">Node B</th><th scope="col">Co-appearances</th></tr></thead><tbody>';
    pairs.slice(0, 12).forEach(p => {
      html += `<tr>
        <td>${p.nameA ? `<a href="#/nodes/${encodeURIComponent(p.pubkeyA)}" class="analytics-link">${esc(p.nameA)}</a>` : `<span class="mono">${p.hopA}</span>`}</td>
        <td>${p.nameB ? `<a href="#/nodes/${encodeURIComponent(p.pubkeyB)}" class="analytics-link">${esc(p.nameB)}</a>` : `<span class="mono">${p.hopB}</span>`}</td>
        <td>${p.count}</td>
      </tr>`;
    });
    return html + '</tbody></table>';
  }

  function renderHopsSNR(data) {
    if (!data.length) return '<div class="text-muted">No data</div>';
    const w = 380, h = 160, pad = 40;
    const maxHop = Math.max(...data.map(d => d.hops));
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:160px" role="img" aria-label="Hops vs SNR bubble chart showing signal degradation over distance"><title>Hops vs SNR bubble chart showing signal degradation over distance</title>`;
    data.forEach(d => {
      const x = pad + (d.hops / maxHop) * (w - pad * 2);
      const y = h - pad - ((d.avgSnr + 12) / 27) * (h - pad * 2);
      const r = Math.min(Math.sqrt(d.count) * 1.5, 12);
      const color = d.avgSnr > 6 ? statusGreen() : d.avgSnr > 0 ? statusYellow() : statusRed();
      svg += `<circle cx="${x}" cy="${y}" r="${r}" fill="${color}" opacity="0.6"/>`;
      svg += `<text x="${x}" y="${y-r-3}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${d.hops}h</text>`;
    });
    svg += `<text x="${w/2}" y="${h-5}" text-anchor="middle" font-size="10" fill="var(--text-muted)">Hops</text>`;
    svg += `<text x="10" y="${h/2}" text-anchor="middle" font-size="10" fill="var(--text-muted)" transform="rotate(-90,10,${h/2})">Avg SNR</text>`;
    svg += '</svg>';
    return svg;
  }

  function renderPerObserverReach(perObserverReach, obsId) {
    const data = perObserverReach[obsId];
    if (!data || !data.rings.length) return '<div class="text-muted">No path data for this observer</div>';
    let html = `<div class="reach-rings">`;
    data.rings.forEach(ring => {
      const nodeLinks = ring.nodes.slice(0, 8).map(n => {
        const label = n.name ? `<a href="#/nodes/${encodeURIComponent(n.pubkey)}" class="analytics-link">${esc(n.name)}</a>` : `<span class="mono">${n.hop}</span>`;
        const detail = n.distRange ? ` <span class="text-muted">(${n.distRange})</span>` : '';
        return label + detail;
      }).join(', ');
      const extra = ring.nodes.length > 8 ? ` <span class="text-muted">+${ring.nodes.length - 8} more</span>` : '';
      html += `<div class="reach-ring">
        <div class="reach-hop">${ring.hops} hop${ring.hops > 1 ? 's' : ''}</div>
        <div class="reach-nodes">${nodeLinks}${extra}</div>
        <div class="reach-count">${ring.nodes.length} node${ring.nodes.length > 1 ? 's' : ''}</div>
      </div>`;
    });
    return html + '</div>';
  }

  function renderAllObserversReach(perObserverReach) {
    let html = '';
    for (const [obsId, data] of Object.entries(perObserverReach)) {
      html += `<h4 style="margin:12px 0 6px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> ${esc(data.observer_name)}</h4>`;
      html += renderPerObserverReach(perObserverReach, obsId);
    }
    return html || '<div class="text-muted">No data</div>';
  }

  function renderCrossObserver(nodes) {
    if (!nodes.length) return '<div class="text-muted">No nodes seen by multiple observers</div>';
    let html = `<table class="analytics-table">
      <thead><tr><th scope="col">Node</th><th scope="col">Observers</th><th scope="col">Hop Distances</th></tr></thead><tbody>`;
    nodes.forEach(n => {
      const name = n.name
        ? `<a href="#/nodes/${encodeURIComponent(n.pubkey)}" class="analytics-link">${esc(n.name)}</a>`
        : `<span class="mono">${n.hop}</span>`;
      const obsInfo = n.observers.map(o =>
        `${esc(o.observer_name)}: <strong>${o.minDist} hop${o.minDist > 1 ? 's' : ''}</strong> <span class="text-muted">(${o.count} pkts)</span>`
      ).join('<br>');
      html += `<tr><td>${name}</td><td>${n.observers.length}</td><td>${obsInfo}</td></tr>`;
    });
    return html + '</tbody></table>';
  }

  function renderBestPath(nodes) {
    if (!nodes.length) return '<div class="text-muted">No data</div>';
    // Group by distance for a cleaner view
    const byDist = {};
    nodes.forEach(n => {
      if (!byDist[n.minDist]) byDist[n.minDist] = [];
      byDist[n.minDist].push(n);
    });
    let html = '<div class="reach-rings">';
    Object.entries(byDist).sort((a, b) => +a[0] - +b[0]).forEach(([dist, nodes]) => {
      const nodeLinks = nodes.slice(0, 10).map(n => {
        const label = n.name
          ? `<a href="#/nodes/${encodeURIComponent(n.pubkey)}" class="analytics-link">${esc(n.name)}</a>`
          : `<span class="mono">${n.hop}</span>`;
        return label + ` <span class="text-muted">via ${esc(n.observer_name)}</span>`;
      }).join(', ');
      const extra = nodes.length > 10 ? ` <span class="text-muted">+${nodes.length - 10} more</span>` : '';
      html += `<div class="reach-ring">
        <div class="reach-hop">${dist} hop${+dist > 1 ? 's' : ''}</div>
        <div class="reach-nodes">${nodeLinks}${extra}</div>
        <div class="reach-count">${nodes.length} node${nodes.length > 1 ? 's' : ''}</div>
      </div>`;
    });
    return html + '</div>';
  }

  // ===================== CHANNELS =====================
  var _channelSortState = null;
  var _channelData = null;
  var _channelRenderGen = 0;
  var CHANNEL_SORT_KEY = 'meshcore-channel-sort';

  function loadChannelSort() {
    try {
      var s = localStorage.getItem(CHANNEL_SORT_KEY);
      if (s) { var p = JSON.parse(s); if (p.col && p.dir) return p; }
    } catch (e) {}
    return { col: 'lastActivity', dir: 'desc' };
  }

  // True when the user has explicitly chosen a sort (saved in localStorage).
  // Used by the grouped analytics view to decide whether to apply its own
  // default ("messages desc") instead of the global flat-list default.
  function hasSavedChannelSort() {
    try {
      var s = localStorage.getItem(CHANNEL_SORT_KEY);
      if (!s) return false;
      var p = JSON.parse(s);
      return !!(p && p.col && p.dir);
    } catch (e) { return false; }
  }

  function saveChannelSort(state) {
    try { localStorage.setItem(CHANNEL_SORT_KEY, JSON.stringify(state)); } catch (e) {}
  }

  function sortChannels(channels, col, dir) {
    var sorted = channels.slice();
    var mult = dir === 'asc' ? 1 : -1;
    sorted.sort(function (a, b) {
      var av, bv;
      switch (col) {
        case 'name':
          av = (a.name || '').toLowerCase(); bv = (b.name || '').toLowerCase();
          return av < bv ? -1 * mult : av > bv ? 1 * mult : 0;
        case 'hash':
          av = typeof a.hash === 'number' ? a.hash : String(a.hash);
          bv = typeof b.hash === 'number' ? b.hash : String(b.hash);
          if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * mult;
          av = String(av).toLowerCase(); bv = String(bv).toLowerCase();
          return av < bv ? -1 * mult : av > bv ? 1 * mult : 0;
        case 'messages': return (a.messages - b.messages) * mult;
        case 'senders': return (a.senders - b.senders) * mult;
        case 'lastActivity':
          av = a.lastActivity || ''; bv = b.lastActivity || '';
          return av < bv ? -1 * mult : av > bv ? 1 * mult : 0;
        case 'encrypted':
          av = a.encrypted ? 1 : 0; bv = b.encrypted ? 1 : 0;
          return (av - bv) * mult;
        default: return 0;
      }
    });
    return sorted;
  }

  function channelRowHtml(c) {
    // displayNameHtml: app-controlled raw HTML (e.g. the encrypted
    // placeholder which carries a Phosphor sprite — set by
    // decorateAnalyticsChannels for unknown encrypted rows). Same pattern
    // as the section-header row: sprite-bearing labels must NOT be
    // HTML-escaped (#1657 class of bug). For any other input we still
    // go through esc() — never trust c.name / c.displayName as HTML.
    var nameHtml = c.displayNameHtml
      ? c.displayNameHtml
      : esc(c.displayName || c.name || 'Unknown');
    return '<tr class="clickable-row" data-action="navigate" data-value="#/channels?ch=' + c.hash + '" tabindex="0" role="row">' +
      '<td><strong>' + nameHtml + '</strong></td>' +
      '<td class="mono">' + (typeof c.hash === 'number' ? '0x' + c.hash.toString(16).toUpperCase().padStart(2, '0') : c.hash) + '</td>' +
      '<td>' + c.messages + '</td>' +
      '<td>' + c.senders + '</td>' +
      '<td>' + timeAgo(c.lastActivity) + '</td>' +
      '<td>' + (c.encrypted ? (c.group === 'mine' ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-key"/></svg>' : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg>') : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg>') + '</td>' +
    '</tr>';
  }

  // ── PSK-aware decoration ──────────────────────────────────────────────────
  // Server returns raw "chNNN" placeholder names for encrypted channels it
  // doesn't know. Decorate so the UI shows a useful display name and a
  // group bucket: mine / network / encrypted. Pure function for testability.
  function decorateAnalyticsChannels(channels, hashByteToKeyName, labels) {
    var keyMap = hashByteToKeyName || {};
    var lab = labels || {};
    var out = [];
    for (var i = 0; i < (channels || []).length; i++) {
      var c = channels[i];
      var copy = Object.assign({}, c);
      var hashNum = typeof c.hash === 'number' ? c.hash : parseInt(c.hash, 10);
      var rawName = String(c.name || '');
      var isPlaceholder = /^ch(\d+|\?)$/.test(rawName);
      if (c.encrypted) {
        var keyName = !isNaN(hashNum) ? keyMap[hashNum] : null;
        if (keyName) {
          copy.displayName = lab[keyName] || keyName;
          copy.group = 'mine';
        } else if (isPlaceholder || !rawName) {
          // Placeholder ("chNNN") or empty name → render as opaque encrypted.
          // Empty-name encrypted rows would otherwise leak through with an
          // empty <strong> in the row; force the placeholder rendering.
          //
          // displayName carries a clean text label (no emoji, no markup) for
          // screen readers / sort comparison / tests. displayNameHtml carries
          // the rendered HTML with a Phosphor ph-lock sprite — consumed by
          // channelRowHtml(). Two fields, one source of truth, no escaping
          // surprises (v384-12.18 follow-up to #1648/#1657).
          var hex = !isNaN(hashNum)
            ? ' (0x' + hashNum.toString(16).toUpperCase().padStart(2, '0') + ')'
            : '';
          copy.displayName = 'Encrypted' + hex;
          copy.displayNameHtml =
            '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg> Encrypted' + hex;
          copy.group = 'encrypted';
        } else {
          // Server gave us a real name (rainbow table hit) for an encrypted ch.
          copy.displayName = rawName;
          copy.group = 'network';
        }
      } else {
        copy.displayName = rawName || 'Unknown';
        copy.group = 'network';
      }
      out.push(copy);
    }
    return out;
  }

  // Build the (hash byte → key name) map from ChannelDecrypt's stored keys.
  // Async because computeChannelHash uses subtle.digest. Returns {} if the
  // module or its keys are unavailable (graceful fallback).
  async function buildHashKeyMap() {
    if (typeof ChannelDecrypt === 'undefined' || !ChannelDecrypt.getStoredKeys) return {};
    var keys = ChannelDecrypt.getStoredKeys();
    var map = {};
    var names = Object.keys(keys || {});
    for (var ni = 0; ni < names.length; ni++) {
      var name = names[ni];
      try {
        var bytes = ChannelDecrypt.hexToBytes(keys[name]);
        var hb = await ChannelDecrypt.computeChannelHash(bytes);
        if (typeof hb === 'number') map[hb] = name;
      } catch (e) { /* skip bad key */ }
    }
    return map;
  }

  function channelTbodyHtml(channels, col, dir, opts) {
    var sorted = sortChannels(channels, col, dir);
    var parts = [];
    if (opts && opts.grouped) {
      // Group by .group: mine → network → encrypted. Inside each group keep
      // the active sort (caller passes col/dir; for the integration we sort
      // by messages desc by default).
      var groups = { mine: [], network: [], encrypted: [] };
      for (var gi = 0; gi < sorted.length; gi++) {
        var g = sorted[gi].group || (sorted[gi].encrypted ? 'encrypted' : 'network');
        (groups[g] || (groups[g] = [])).push(sorted[gi]);
      }
      var sections = [
        { key: 'mine', label: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-key"/></svg> My Channels' },
        { key: 'network', label: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-radio"/></svg> Network' },
        { key: 'encrypted', label: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg> Encrypted' },
      ];
      for (var si = 0; si < sections.length; si++) {
        var rows = groups[sections[si].key] || [];
        if (!rows.length) continue;
        parts.push(
          '<tr class="ch-section-row"><td colspan="6" class="ch-section-header">' +
          // sections[].label is a hardcoded sprite-bearing string (no user
          // input) — must be inserted raw so the <svg> renders. esc() here
          // HTML-escaped the angle brackets and surfaced literal "<svg…>"
          // text in the table (#1657).
          sections[si].label + ' <span class="text-muted">(' + rows.length + ')</span>' +
          '</td></tr>'
        );
        for (var ri = 0; ri < rows.length; ri++) parts.push(channelRowHtml(rows[ri]));
      }
    } else {
      for (var i = 0; i < sorted.length; i++) parts.push(channelRowHtml(sorted[i]));
    }
    return parts.join('');
  }

  function channelSortArrow(col, activeCol, dir) {
    if (col !== activeCol) return '<span class="sort-arrow">⇅</span>';
    return '<span class="sort-arrow">' + (dir === 'asc' ? '↑' : '↓') + '</span>';
  }

  function channelTheadHtml(activeCol, dir) {
    var cols = [
      { key: 'name', label: 'Channel' },
      { key: 'hash', label: 'Hash' },
      { key: 'messages', label: 'Messages' },
      { key: 'senders', label: 'Unique Senders' },
      { key: 'lastActivity', label: 'Last Activity' },
      { key: 'encrypted', label: 'Decrypted' },
    ];
    var ths = '';
    for (var i = 0; i < cols.length; i++) {
      var c = cols[i];
      ths += '<th scope="col" class="sortable' + (c.key === activeCol ? ' sort-active' : '') + '" data-sort-col="' + c.key + '">' +
        c.label + channelSortArrow(c.key, activeCol, dir) + '</th>';
    }
    return '<thead><tr>' + ths + '</tr></thead>';
  }

  function updateChannelTable() {
    var tbody = document.getElementById('channelsTbody');
    var thead = document.querySelector('#channelsTable thead');
    if (!tbody || !_channelData) return;
    tbody.innerHTML = channelTbodyHtml(_channelData, _channelSortState.col, _channelSortState.dir, { grouped: true });
    if (thead) thead.outerHTML = channelTheadHtml(_channelSortState.col, _channelSortState.dir);
  }

  function renderChannels(el, ch) {
    // Decorate first so grouping/display name reflect locally-stored PSK keys.
    // buildHashKeyMap is async; render once with a sync best-effort empty map,
    // then upgrade once keys resolve. That keeps first paint fast and avoids
    // blocking on subtle.digest in environments where it's slow.
    var rawChannels = ch.channels || [];
    // Resolve the persisted sort first so the default-fallback below doesn't
    // shadow what the user previously chose. Default for the grouped view is
    // messages desc (matches the PR description); only used when nothing saved.
    if (!_channelSortState) {
      _channelSortState = hasSavedChannelSort()
        ? loadChannelSort()
        : { col: 'messages', dir: 'desc' };
    }
    var ranOnce = false;
    // Generation token: if renderChannels is called again before
    // buildHashKeyMap() resolves, the older promise must not clobber the
    // newer rawChannels / decoration with stale-key data.
    var myGen = ++_channelRenderGen;
    function applyDecorate(map) {
      if (myGen !== _channelRenderGen) return; // superseded
      var labels = (typeof ChannelDecrypt !== 'undefined' && ChannelDecrypt.getLabels)
        ? ChannelDecrypt.getLabels() : {};
      _channelData = decorateAnalyticsChannels(rawChannels, map, labels);
      if (ranOnce) updateChannelTable();
    }
    applyDecorate({});
    ranOnce = true;
    buildHashKeyMap().then(applyDecorate).catch(function () { /* graceful */ });

    var timelineHtml = renderChannelTimeline(ch.channelTimeline);
    var topSendersHtml = renderTopSenders(ch.topSenders);
    var histoHtml = ch.msgLengths.length ? histogram(ch.msgLengths, 20, '#8b5cf6').svg : '<div class="text-muted">No decrypted messages</div>';

    el.innerHTML =
      '<div class="analytics-card">' +
        '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-radio"/></svg> Channel Activity</h3>' +
        '<p class="text-muted">' + ch.activeChannels + ' active channels, ' + ch.decryptable + ' decryptable</p>' +
        '<table class="analytics-table" id="channelsTable">' +
          channelTheadHtml(_channelSortState.col, _channelSortState.dir) +
          '<tbody id="channelsTbody">' +
            channelTbodyHtml(_channelData, _channelSortState.col, _channelSortState.dir, { grouped: true }) +
          '</tbody>' +
        '</table>' +
      '</div>' +
      '<div class="analytics-row">' +
        '<div class="analytics-card flex-1">' +
          '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chat-circle"/></svg> Messages / Hour by Channel</h3>' +
          timelineHtml +
        '</div>' +
        '<div class="analytics-card flex-1">' +
          '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-megaphone"/></svg> Top Senders</h3>' +
          topSendersHtml +
        '</div>' +
      '</div>' +
      '<div class="analytics-card">' +
        '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Message Length Distribution</h3>' +
        histoHtml +
      '</div>';

    // Attach sort handler via delegation on the table
    var table = document.getElementById('channelsTable');
    if (table) {
      table.addEventListener('click', function (e) {
        var th = e.target.closest('th[data-sort-col]');
        if (!th) return;
        var col = th.dataset.sortCol;
        if (_channelSortState.col === col) {
          _channelSortState.dir = _channelSortState.dir === 'asc' ? 'desc' : 'asc';
        } else {
          _channelSortState.col = col;
          _channelSortState.dir = col === 'name' || col === 'hash' ? 'asc' : 'desc';
        }
        saveChannelSort(_channelSortState);
        updateChannelTable();
      });
    }
  }

  var CHANNEL_TIMELINE_MAX_SERIES = 8;

  function renderChannelTimeline(data) {
    if (!data.length) return '<div class="text-muted">No data</div>';
    var hours = []; var hourSet = {};
    var channelList = []; var channelSet = {};
    var lookup = {};
    var channelVolume = {};
    for (var i = 0; i < data.length; i++) {
      var d = data[i];
      if (!hourSet[d.hour]) { hourSet[d.hour] = 1; hours.push(d.hour); }
      if (!channelSet[d.channel]) { channelSet[d.channel] = 1; channelList.push(d.channel); }
      lookup[d.hour + '|' + d.channel] = d.count;
      channelVolume[d.channel] = (channelVolume[d.channel] || 0) + d.count;
    }
    hours.sort();
    // Sort channels by total volume descending, cap to top N
    channelList.sort(function(a, b) { return channelVolume[b] - channelVolume[a]; });
    var hiddenCount = Math.max(0, channelList.length - CHANNEL_TIMELINE_MAX_SERIES);
    var visibleChannels = channelList.slice(0, CHANNEL_TIMELINE_MAX_SERIES);

    var maxCount = 1;
    for (var vi = 0; vi < visibleChannels.length; vi++) {
      for (var hi2 = 0; hi2 < hours.length; hi2++) {
        var c = lookup[hours[hi2] + '|' + visibleChannels[vi]] || 0;
        if (c > maxCount) maxCount = c;
      }
    }

    var colors = ['#ef4444','#22c55e','#3b82f6','#f59e0b','#8b5cf6','#ec4899','#14b8a6','#64748b'];
    var w = 600, h = 180, pad = 35;
    var xScale = (w - pad * 2) / Math.max(hours.length - 1, 1);
    var yScale = (h - pad * 2) / maxCount;
    var svg = '<svg viewBox="0 0 ' + w + ' ' + h + '" style="width:100%;max-height:180px" role="img" aria-label="Channel message activity over time"><title>Channel message activity over time</title>';
    for (var ci = 0; ci < visibleChannels.length; ci++) {
      var pts = [];
      for (var hi = 0; hi < hours.length; hi++) {
        var count = lookup[hours[hi] + '|' + visibleChannels[ci]] || 0;
        var x = pad + hi * xScale;
        var y = h - pad - count * yScale;
        pts.push(x + ',' + y);
      }
      svg += '<polyline points="' + pts.join(' ') + '" fill="none" stroke="' + colors[ci % colors.length] + '" stroke-width="1.5" opacity="0.8"/>';
    }
    var step = Math.max(1, Math.floor(hours.length / 6));
    for (var li = 0; li < hours.length; li += step) {
      var lx = pad + li * xScale;
      svg += '<text x="' + lx + '" y="' + (h - pad + 14) + '" text-anchor="middle" font-size="9" fill="var(--text-muted)">' + hours[li].slice(11) + 'h</text>';
    }
    svg += '</svg>';
    var legendParts = [];
    for (var lci = 0; lci < visibleChannels.length; lci++) {
      legendParts.push('<span><span class="legend-dot" style="background:' + colors[lci % colors.length] + '"></span>' + esc(visibleChannels[lci]) + '</span>');
    }
    if (hiddenCount > 0) {
      legendParts.push('<span class="text-muted">+' + hiddenCount + ' more</span>');
    }
    svg += '<div class="timeline-legend">' + legendParts.join('') + '</div>';
    return svg;
  }

  function renderTopSenders(senders) {
    if (!senders.length) return '<div class="text-muted">No decrypted messages</div>';
    const max = senders[0].count;
    let html = '<div class="repeater-list">';
    senders.slice(0, 10).forEach(s => {
      html += `<div class="repeater-row">
        <div class="repeater-name"><strong>${esc(s.name)}</strong></div>
        <div class="repeater-bar"><div class="hash-bar-track"><div class="hash-bar-fill" style="width:${(s.count/max*100).toFixed(0)}%;background:#8b5cf6"></div></div></div>
        <div class="repeater-count">${s.count} msgs</div>
      </div>`;
    });
    return html + '</div>';
  }

  // ===================== HASH SIZES (original) =====================
  function renderHashSizes(el, data) {
    const d = data.distribution;
    const total = data.total;
    const pct = (n) => total ? (n / total * 100).toFixed(1) : '0';
    const maxCount = Math.max(d[1] || 0, d[2] || 0, d[3] || 0, 1);

    el.innerHTML = `
      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>Hash Size Distribution</h3>
          <p class="text-muted">${total.toLocaleString()} packets with path hops</p>
          <div class="hash-bars">
            ${[1, 2, 3].map(size => {
              const count = d[size] || 0;
              const width = Math.max((count / maxCount) * 100, count ? 2 : 0);
              const colors = { 1: '#ef4444', 2: '#22c55e', 3: '#3b82f6' };
              return `<div class="hash-bar-row">
              <div class="hash-bar-label"><strong>${size}-byte</strong> <span class="text-muted">(${size * 8}-bit, ${Math.pow(256, size).toLocaleString()} IDs)</span></div>
              <div class="hash-bar-track"><div class="hash-bar-fill" style="width:${width}%;background:${colors[size]}"></div></div>
              <div class="hash-bar-value">${count.toLocaleString()} <span class="text-muted">(${pct(count)}%)</span></div>
            </div>`;
            }).join('')}
          </div>
          ${data.distributionByRepeaters ? (() => {
            const dr = data.distributionByRepeaters;
            const totalRepeaters = (dr[1] || 0) + (dr[2] || 0) + (dr[3] || 0);
            const rpct = (n) => totalRepeaters ? (n / totalRepeaters * 100).toFixed(1) : '0';
            const maxRepeaters = Math.max(dr[1] || 0, dr[2] || 0, dr[3] || 0, 1);
            const colors = { 1: '#ef4444', 2: '#22c55e', 3: '#3b82f6' };
            return `<h4 style="margin:16px 0 4px">By Repeaters</h4>
              <p class="text-muted">${totalRepeaters.toLocaleString()} unique repeaters</p>
              <div class="hash-bars">
                ${[1, 2, 3].map(size => {
                  const count = dr[size] || 0;
                  const width = Math.max((count / maxRepeaters) * 100, count ? 2 : 0);
                  return `<div class="hash-bar-row">
                  <div class="hash-bar-label"><strong>${size}-byte</strong></div>
                  <div class="hash-bar-track"><div class="hash-bar-fill" style="width:${width}%;background:${colors[size]};opacity:0.7"></div></div>
                  <div class="hash-bar-value">${count.toLocaleString()} <span class="text-muted">(${rpct(count)}%)</span></div>
                </div>`;
                }).join('')}
              </div>`;
          })() : ''}
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trend-up"/></svg> Hash Size Over Time</h3>
          ${renderHashTimeline(data.hourly)}
        </div>
      </div>

      ${renderMultiByteAdopters(data.multiByteNodes, data.multiByteCapability || [])}

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>Top Path Hops</h3>
        <table class="analytics-table">
          <thead><tr><th scope="col">Hop</th><th scope="col">Node</th><th scope="col">Bytes</th><th scope="col">Appearances</th></tr></thead>
          <tbody>
            ${data.topHops.map(h => {
              const link = h.pubkey ? `#/nodes/${encodeURIComponent(h.pubkey)}` : `#/packets?search=${h.hex}`;
              return `<tr class="clickable-row" data-action="navigate" data-value="${link}" tabindex="0" role="row">
              <td class="mono">${h.hex}</td>
              <td>${h.name ? `<strong>${esc(h.name)}</strong>` : '<span class="text-muted">unknown</span>'}</td>
              <td><span class="badge badge-hash-${h.size}">${h.size}-byte</span></td>
              <td>${h.count.toLocaleString()}</td>
            </tr>`;
            }).join('')}
          </tbody>
        </table>
        </div>
      </div>
    `;
  }

  function renderMultiByteAdopters(nodes, caps) {
    // Merge capability status into adopter nodes
    var capByPubkey = {};
    (caps || []).forEach(function(c) { capByPubkey[c.pubkey] = c; });

    var statusIcon = { confirmed: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg>', suspected: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>', unknown: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg>' };
    var statusLabel = { confirmed: 'Confirmed', suspected: 'Suspected', unknown: 'Unknown' };
    var statusColor = { confirmed: 'var(--success, #22c55e)', suspected: 'var(--warning, #eab308)', unknown: 'var(--text-muted, #888)' };

    // Build merged rows: each adopter node gets a capability status
    var rows = (nodes || []).map(function(n) {
      var cap = capByPubkey[n.pubkey] || {};
      return {
        name: n.name, pubkey: n.pubkey || '', role: n.role || '',
        hashSize: n.hashSize, packets: n.packets, lastSeen: n.lastSeen,
        status: cap.status || 'unknown', evidence: cap.evidence || ''
      };
    });

    // Count statuses
    var counts = { confirmed: 0, suspected: 0, unknown: 0 };
    rows.forEach(function(r) { counts[r.status] = (counts[r.status] || 0) + 1; });

    function buildTableContent(rows, filter) {
      var filtered = filter === 'all' ? rows : rows.filter(function(r) { return r.status === filter; });
      return (filtered.length ? '<table class="analytics-table" id="mbAdoptersTable" style="margin-top:12px">' +
          '<thead><tr>' +
            '<th scope="col" data-sort="name">Node</th>' +
            '<th scope="col" data-sort="role">Role</th>' +
            '<th scope="col" data-sort="status">Status</th>' +
            '<th scope="col" data-sort="hashSize">Hash Size</th>' +
            '<th scope="col" data-sort="packets">Adverts</th>' +
            '<th scope="col" data-sort="lastSeen">Last Seen</th>' +
          '</tr></thead>' +
          '<tbody>' +
            filtered.map(function(r) {
              var roleColor = (window.ROLE_COLORS || {})[r.role] || '#6b7280';
              return '<tr class="clickable-row" data-action="navigate" data-value="#/nodes/' + encodeURIComponent(r.pubkey) + '" tabindex="0" role="row">' +
                '<td><strong>' + esc(r.name) + '</strong></td>' +
                '<td><span class="badge" style="' + (window.aaBadgeStyle ? window.aaBadgeStyle(roleColor) : 'background:' + roleColor + ';color:#fff') + '">' + esc(r.role || 'unknown') + '</span></td>' +
                '<td><span style="color:' + (statusColor[r.status] || statusColor.unknown) + '">' +
                  (statusIcon[r.status] || '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg>') + ' ' + (statusLabel[r.status] || 'Unknown') + '</span></td>' +
                '<td><span class="badge badge-hash-' + r.hashSize + '">' + r.hashSize + '-byte</span></td>' +
                '<td>' + r.packets + '</td>' +
                '<td>' + (r.lastSeen ? timeAgo(r.lastSeen) : '—') + '</td>' +
              '</tr>';
            }).join('') +
          '</tbody>' +
        '</table>' : '<div class="text-muted" style="padding:16px">No adopters match this filter.</div>');
    }

    if (!rows.length) return '<div class="analytics-row"><div class="analytics-card flex-1">' +
      '<h3>Multi-Byte Hash Adopters</h3>' +
      '<div class="text-muted" style="padding:16px">No multi-byte adopters found</div></div></div>';

    var html = '<div class="analytics-row"><div class="analytics-card flex-1" id="mbAdoptersSection">' +
      '<div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px">' +
        '<div>' +
          '<h3 style="margin:0">Multi-Byte Hash Adopters</h3>' +
          '<p class="text-muted" style="margin:4px 0 0;font-size:0.8em">Nodes advertising with 2+ byte hash paths. ' +
          '<strong>Confirmed</strong> = seen advertising with multi-byte hash. ' +
          '<strong>Suspected</strong> = prefix appeared in a multi-byte path. ' +
          '<strong>Unknown</strong> = no multi-byte evidence yet.</p>' +
        '</div>' +
        '<div style="display:flex;gap:4px;flex-wrap:wrap" id="mbCapFilters">' +
          '<button class="tab-btn active" data-mb-filter="all">All (' + rows.length + ')</button>' +
          '<button class="tab-btn" data-mb-filter="confirmed" style="--filter-color:var(--success, #22c55e)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> Confirmed (' + counts.confirmed + ')</button>' +
          '<button class="tab-btn" data-mb-filter="suspected" style="--filter-color:var(--warning, #eab308)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Suspected (' + counts.suspected + ')</button>' +
          '<button class="tab-btn" data-mb-filter="unknown" style="--filter-color:var(--text-muted, #888)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg> Unknown (' + counts.unknown + ')</button>' +
        '</div>' +
      '</div>' +
      '<div id="mbAdoptersTableWrap">' + buildTableContent(rows, 'all') + '</div>' +
    '</div></div>';

    // Use setTimeout for event delegation on the stable section container
    setTimeout(function() {
      var section = document.getElementById('mbAdoptersSection');
      if (!section) return;
      var currentFilter = 'all';

      section.addEventListener('click', function handler(e) {
        var btn = e.target.closest('[data-mb-filter]');
        if (btn) {
          currentFilter = btn.dataset.mbFilter;
          // Update active state on buttons (no DOM replacement needed)
          var buttons = section.querySelectorAll('[data-mb-filter]');
          buttons.forEach(function(b) { b.classList.toggle('active', b.dataset.mbFilter === currentFilter); });
          // Replace only the table content, not the whole section
          var wrap = section.querySelector('#mbAdoptersTableWrap');
          if (wrap) wrap.innerHTML = buildTableContent(rows, currentFilter);
          return;
        }
        var th = e.target.closest('[data-sort]');
        if (th) {
          var tbody = section.querySelector('tbody');
          if (!tbody) return;
          var sortRows = Array.from(tbody.querySelectorAll('tr'));
          var col = th.dataset.sort;
          var colIdx = { name: 0, status: 1, hashSize: 2, packets: 3, lastSeen: 4 };
          var statusWeight = { 'confirmed': 0, 'suspected': 1, 'unknown': 2 };
          sortRows.sort(function(a, b) {
            var va = a.children[colIdx[col]] ? a.children[colIdx[col]].textContent.trim() : '';
            var vb = b.children[colIdx[col]] ? b.children[colIdx[col]].textContent.trim() : '';
            if (col === 'status') {
              va = statusWeight[va.toLowerCase().split(' ').pop()] !== undefined ? statusWeight[va.toLowerCase().split(' ').pop()] : 2;
              vb = statusWeight[vb.toLowerCase().split(' ').pop()] !== undefined ? statusWeight[vb.toLowerCase().split(' ').pop()] : 2;
            }
            if (col === 'hashSize' || col === 'packets') { va = parseInt(va) || 0; vb = parseInt(vb) || 0; }
            if (va < vb) return -1;
            if (va > vb) return 1;
            return 0;
          });
          sortRows.forEach(function(r) { tbody.appendChild(r); });
        }
      });
    }, 100);

    return html;
  }

  // Legacy alias for tests — delegates to renderMultiByteAdopters with empty nodes
  function renderMultiByteCapability(caps) {
    if (!caps.length) return '';
    // Convert caps to adopter-style rows for backward compat
    var fakeNodes = caps.map(function(c) {
      return { name: c.name, pubkey: c.pubkey, role: c.role, hashSize: c.maxHashSize, packets: 0, lastSeen: c.lastSeen };
    });
    return renderMultiByteAdopters(fakeNodes, caps);
  }

  async function renderCollisionTab(el, data, collisionData) {
    el.innerHTML = `
      <nav id="hashIssuesToc" style="display:flex;gap:12px;margin-bottom:12px;font-size:13px;flex-wrap:wrap">
        <a data-hash-section="inconsistentHashSection" href="#/analytics?tab=collisions&section=inconsistentHashSection" style="color:var(--link-color)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Inconsistent Sizes</a>
        <span style="color:var(--border)">|</span>
        <a data-hash-section="hashMatrixSection" href="#/analytics?tab=collisions&section=hashMatrixSection" style="color:var(--link-color)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-list-numbers"/></svg> Hash Matrix</a>
        <span style="color:var(--border)">|</span>
        <a data-hash-section="collisionRiskSection" href="#/analytics?tab=collisions&section=collisionRiskSection" style="color:var(--link-color)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-bomb"/></svg> Collision Risk</a>
        <span style="color:var(--border)">|</span>
        <a href="#/analytics?tab=prefix-tool" style="color:var(--link-color)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> Check a prefix →</a>
      </nav>
      <p class="text-muted" style="margin:0 0 12px;font-size:0.78em">Collisions <strong>actually observed in packet traffic</strong> — among <strong>repeaters</strong> grouped by their configured hash size. For <em>theoretical</em> address conflicts that <em>would</em> occur if all repeaters used a given hash size, see the <a href="#/analytics?tab=prefix-tool" style="color:var(--link-color)">Prefix Tool</a> tab.</p>

      <div class="analytics-card" id="inconsistentHashSection">
        <div style="display:flex;justify-content:space-between;align-items:center"><h3 style="margin:0"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Inconsistent Hash Sizes</h3><a data-hash-section="" href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a></div>
        <p class="text-muted" style="margin:4px 0 8px;font-size:0.8em">Repeaters and room servers sending adverts with varying hash sizes in the last 7 days. Originally caused by a <a href="https://github.com/meshcore-dev/MeshCore/commit/fcfdc5f" target="_blank" style="color:var(--link-color)">firmware bug</a> where automatic adverts ignored the configured multibyte path setting, fixed in <a href="https://github.com/meshcore-dev/MeshCore/releases/tag/repeater-v1.14.1" target="_blank" style="color:var(--link-color)">repeater v1.14.1</a>. Companion nodes are excluded.</p>
        <div id="inconsistentHashList"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading…</div></div>
      </div>

      <div class="analytics-card" id="hashMatrixSection">
        <div style="display:flex;justify-content:space-between;align-items:center">
          <h3 style="margin:0" id="hashMatrixTitle"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-list-numbers"/></svg> Hash Usage Matrix</h3>
          <a data-hash-section="" href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a>
        </div>
        <div style="display:flex;align-items:center;gap:16px;margin:8px 0">
          <div class="hash-byte-selector" id="hashByteSelector" style="display:flex;gap:4px">
            <button class="hash-byte-btn active" data-bytes="1">1-Byte</button>
            <button class="hash-byte-btn" data-bytes="2">2-Byte</button>
            <button class="hash-byte-btn" data-bytes="3">3-Byte</button>
          </div>
          <p class="text-muted" id="hashMatrixDesc" style="margin:0;font-size:0.8em">Click a cell to see which nodes share that prefix.</p>
        </div>
        <div id="hashMatrix"></div>
      </div>

      <div class="analytics-card" id="collisionRiskSection">
        <div style="display:flex;justify-content:space-between;align-items:center"><h3 style="margin:0" id="collisionRiskTitle"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-bomb"/></svg> Collision Risk</h3><a data-hash-section="" href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a></div>
        <div id="collisionList"><div class="text-muted" style="padding:8px">Loading…</div></div>
      </div>
    `;
    // Use pre-computed collision data from server (no more /nodes?limit=2000 fetch)
    const cData = collisionData || { inconsistent_nodes: [], by_size: {} };
    const inconsistent = cData.inconsistent_nodes || [];
    const ihEl = document.getElementById('inconsistentHashList');
    if (ihEl) {
      if (!inconsistent.length) {
        ihEl.innerHTML = '<div class="text-muted" style="padding:4px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No inconsistencies detected — all nodes are reporting consistent hash sizes.</div>';
      } else {
        ihEl.innerHTML = `<table class="analytics-table" style="background:var(--card-bg);border:1px solid var(--border);border-radius:8px;overflow:hidden">
          <thead><tr><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Current Hash</th><th scope="col">Sizes Seen</th></tr></thead>
          <tbody>${inconsistent.map((n, i) => {
            const roleColor = window.ROLE_COLORS?.[n.role] || '#6b7280';
            const prefix = n.hash_size ? n.public_key.slice(0, n.hash_size * 2).toUpperCase() : '?';
            const sizeBadges = (Array.isArray(n.hash_sizes_seen) ? n.hash_sizes_seen : []).map(s => {
              const c = s >= 3 ? '#16a34a' : s === 2 ? '#86efac' : '#f97316';
              const fg = s === 2 ? '#064e3b' : '#fff';
              return '<span class="badge" style="background:' + c + ';color:' + fg + ';font-size:10px;font-family:var(--mono)">' + s + 'B</span>';
            }).join(' ');
            const stripe = i % 2 === 1 ? 'background:var(--row-stripe)' : '';
            return `<tr style="${stripe}">
              <td><a href="#/nodes/${encodeURIComponent(n.public_key)}?section=node-packets" style="font-weight:600;color:var(--link-color)">${esc(n.name || n.public_key.slice(0, 12))}</a></td>
              <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(roleColor)) || ('background:'+roleColor+';color:#fff')}">${n.role}</span></td>
              <td><code style="font-family:var(--mono);font-weight:700">${prefix}</code> <span class="text-muted">(${n.hash_size || '?'}B)</span></td>
              <td>${sizeBadges}</td>
            </tr>`;
          }).join('')}</tbody>
        </table>
        <p class="text-muted" style="margin:8px 0 0;font-size:0.8em">${inconsistent.length} node${inconsistent.length > 1 ? 's' : ''} affected. Click a node name to see which adverts have different hash sizes.</p>`;
      }
    }

    // Repeaters and routing nodes no longer needed — collision data is server-computed

    function refreshHashViews(bytes) {
      // #1914: keep both the bookmark and section links on this byte size.
      if (window.URLState) {
        const newHash = URLState.updateHashParams({ bytes }, location.hash);
        if (newHash !== location.hash) history.replaceState(null, '', newHash);
        el.querySelectorAll('[data-hash-section]').forEach(link => {
          link.href = URLState.updateHashParams({ section: link.dataset.hashSection }, newHash);
        });
      }
      hideMatrixTip();
      // Update selector button states
      document.querySelectorAll('.hash-byte-btn').forEach(b => {
        b.classList.toggle('active', Number(b.dataset.bytes) === bytes);
      });
      // Update titles and description
      const matrixTitle = document.getElementById('hashMatrixTitle');
      const matrixDesc = document.getElementById('hashMatrixDesc');
      const riskTitle = document.getElementById('collisionRiskTitle');
      if (matrixTitle) matrixTitle.innerHTML = bytes === 3 ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-list-numbers"/></svg> Hash Usage Matrix' : `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-list-numbers"/></svg> ${bytes}-Byte Hash Usage Matrix`;
      if (riskTitle) riskTitle.innerHTML = `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-bomb"/></svg> ${bytes}-Byte Collision Risk`;
      if (matrixDesc) {
        if (bytes === 1) matrixDesc.textContent = 'Cells include the first byte of all repeaters — including those using 2- or 3-byte prefixes — so this reflects real conflicts in the 1-byte hash space. Click a cell to see the nodes.';
        else if (bytes === 2) matrixDesc.textContent = 'Each cell = first-byte group. Color shows worst 2-byte collision within. Click a cell to see the breakdown.';
        else matrixDesc.textContent = '3-byte prefix space is too large to visualize as a matrix — collision table is shown below.';
      }
      renderHashMatrixFromServer(cData.by_size[String(bytes)], bytes);
      // Show collision risk section for all byte sizes
      const riskCard = document.getElementById('collisionRiskSection');
      if (riskCard) riskCard.style.display = '';
      renderCollisionsFromServer(cData.by_size[String(bytes)], bytes);
    }

    // Wire up selector
    document.getElementById('hashByteSelector')?.querySelectorAll('.hash-byte-btn').forEach(btn => {
      btn.addEventListener('click', () => refreshHashViews(Number(btn.dataset.bytes)));
    });

    // Read on every render so tab, filter and theme refreshes retain the view.
    const urlBytes = new URLSearchParams(location.hash.split('?')[1] || '').get('bytes');
    refreshHashViews(['1', '2', '3'].includes(urlBytes) ? Number(urlBytes) : 1);
  }

  function renderHashTimeline(hourly) {
    if (!hourly.length) return '<div class="text-muted">Not enough data</div>';
    const w = 800, h = 180, pad = 35;
    const maxVal = Math.max(...hourly.map(h => Math.max(h[1] || 0, h[2] || 0, h[3] || 0)), 1);
    const colors = { 1: '#ef4444', 2: '#22c55e', 3: '#3b82f6' };
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:180px" role="img" aria-label="Hash size distribution over time showing 1-byte, 2-byte, and 3-byte hash trends"><title>Hash size distribution over time showing 1-byte, 2-byte, and 3-byte hash trends</title>`;
    for (const size of [1, 2, 3]) {
      const pts = hourly.map((d, i) => {
        const x = pad + i * ((w - pad * 2) / Math.max(hourly.length - 1, 1));
        const y = h - pad - ((d[size] || 0) / maxVal) * (h - pad * 2);
        return `${x},${y}`;
      }).join(' ');
      if (hourly.some(d => d[size] > 0)) svg += `<polyline points="${pts}" fill="none" stroke="${colors[size]}" stroke-width="2"/>`;
    }
    const step = Math.max(1, Math.floor(hourly.length / 8));
    for (let i = 0; i < hourly.length; i += step) {
      const x = pad + i * ((w - pad * 2) / Math.max(hourly.length - 1, 1));
      svg += `<text x="${x}" y="${h-5}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${hourly[i].hour.slice(11)}h</text>`;
    }
    svg += '</svg>';
    svg += `<div class="timeline-legend"><span><span class="legend-dot" style="background:#ef4444"></span>1-byte</span><span><span class="legend-dot" style="background:#22c55e"></span>2-byte</span><span><span class="legend-dot" style="background:#3b82f6"></span>3-byte</span></div>`;
    return svg;
  }

  // Shared hover tooltip for hash matrix cells.
  // Called once per container — reads content from data-tip on each <td>.
  // Single shared tooltip element for the entire hash matrix — avoids DOM accumulation on mode switch
  let _matrixTip = null;
  function getMatrixTip() {
    if (!_matrixTip) {
      _matrixTip = document.createElement('div');
      _matrixTip.className = 'hash-matrix-tooltip';
      _matrixTip.style.display = 'none';
      document.body.appendChild(_matrixTip);
    }
    return _matrixTip;
  }
  function hideMatrixTip() { if (_matrixTip) _matrixTip.style.display = 'none'; }

  // SECURITY (ANL-1, PR #1539 round 2): rebuild tooltip body via DOM APIs.
  // The previous round-1 fix used `tip.textContent = td.dataset.tip`, which
  // defeated the mutation-XSS but also broke the rendered tooltip — the
  // payload had been a structured HTML string from hashTooltipHtml(), so
  // users saw literal `<div class="...">…</div>` text. The correct fix is
  // to stop carrying HTML through the dataset round-trip entirely: each
  // tooltip field rides as its own data-tip-* attribute (entity-decoded by
  // the browser on read = plain text), and we materialize the three styled
  // child <div>s via createElement + textContent. No innerHTML on any
  // node-controlled field path.
  function buildMatrixTipChildren(tip, td) {
    while (tip.firstChild) tip.removeChild(tip.firstChild);
    const ds = td.dataset || {};
    if (ds.tipHex) {
      const h = document.createElement('div');
      h.className = 'hash-matrix-tooltip-hex';
      h.textContent = ds.tipHex;
      tip.appendChild(h);
    }
    if (ds.tipStatus) {
      const s = document.createElement('div');
      s.className = 'hash-matrix-tooltip-status';
      s.textContent = ds.tipStatus;
      tip.appendChild(s);
    }
    if (ds.tipLines) {
      const wrap = document.createElement('div');
      wrap.className = 'hash-matrix-tooltip-nodes';
      const lines = ds.tipLines.split('\u001f');
      for (let i = 0; i < lines.length; i++) {
        const row = document.createElement('div');
        row.style.fontSize = '11px';
        row.textContent = lines[i];
        wrap.appendChild(row);
      }
      tip.appendChild(wrap);
    }
  }

  function initMatrixTooltip(el) {
    if (el._matrixTipInit) return;
    el._matrixTipInit = true;
    el.addEventListener('mouseover', e => {
      const td = e.target.closest('td[data-tip-hex]');
      if (!td) return;
      const tip = getMatrixTip();
      buildMatrixTipChildren(tip, td);
      tip.style.display = 'block';
    });
    el.addEventListener('mousemove', e => {
      if (!_matrixTip || _matrixTip.style.display === 'none') return;
      const x = e.clientX + 14, y = e.clientY + 14;
      _matrixTip.style.left = Math.min(x, window.innerWidth - _matrixTip.offsetWidth - 8) + 'px';
      _matrixTip.style.top = Math.min(y, window.innerHeight - _matrixTip.offsetHeight - 8) + 'px';
    });
    el.addEventListener('mouseout', e => {
      if (e.target.closest('td[data-tip-hex]') && !e.relatedTarget?.closest('td[data-tip-hex]')) hideMatrixTip();
    });
    el.addEventListener('mouseleave', hideMatrixTip);
  }

  // --- Shared helpers for hash matrix rendering ---

  function hashStatCardsHtml(totalNodes, usingCount, sizeLabel, spaceSize, usedCount, collisionCount) {
    const pct = spaceSize > 0 && usedCount > 0 ? ((usedCount / spaceSize) * 100) : 0;
    const pctStr = spaceSize > 65536 ? pct.toFixed(6) : spaceSize > 256 ? pct.toFixed(3) : pct.toFixed(1);
    const spaceLabel = spaceSize >= 1e6 ? (spaceSize / 1e6).toFixed(1) + 'M' : spaceSize.toLocaleString();
    return `<div style="display:flex;gap:12px;flex-wrap:wrap;margin-bottom:12px">
      <div class="analytics-stat-card" style="flex:1;min-width:110px">
        <div class="analytics-stat-label">Nodes tracked</div>
        <div class="analytics-stat-value">${totalNodes.toLocaleString()}</div>
      </div>
      <div class="analytics-stat-card" style="flex:1;min-width:110px">
        <div class="analytics-stat-label">Using ${sizeLabel} ID</div>
        <div class="analytics-stat-value">${usingCount.toLocaleString()}</div>
      </div>
      <div class="analytics-stat-card" style="flex:1;min-width:110px">
        <div class="analytics-stat-label">Prefix space used</div>
        <div class="analytics-stat-value" style="font-size:16px">${pctStr}%</div>
        <div style="font-size:10px;color:var(--text-muted);margin-top:2px">${usedCount > 256 ? usedCount + ' of ' : 'of '}${spaceLabel} possible</div>
      </div>
      <div class="analytics-stat-card" style="flex:1;min-width:110px;border-color:${collisionCount > 0 ? 'var(--status-red)' : 'var(--border)'}${collisionCount > 0 ? ';cursor:pointer' : ''}" ${collisionCount > 0 ? 'onclick="document.getElementById(\'collisionRiskSection\')?.scrollIntoView({behavior:\'smooth\',block:\'start\'})"' : ''} ${collisionCount > 0 ? 'title="Click to see collision details"' : ''}>
        <div class="analytics-stat-label">Prefix collisions</div>
        <div class="analytics-stat-value" style="color:${collisionCount > 0 ? 'var(--status-red)' : 'var(--status-green)'}">${collisionCount}${collisionCount > 0 ? ' <span style="font-size:11px;opacity:0.7"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-down"/></svg></span>' : ''}</div>
      </div>
    </div>`;
  }

  function hashMatrixGridHtml(nibbles, cellSize, headerSize, cellDataFn) {
    let html = `<div style="display:flex;gap:16px;flex-wrap:wrap"><div class="hash-matrix-scroll"><table class="hash-matrix-table" style="border-collapse:collapse;font-size:12px;font-family:monospace">`;
    html += `<tr><td style="width:${headerSize}px"></td>`;
    for (const n of nibbles) html += `<td style="width:${cellSize}px;text-align:center;padding:2px 0;font-weight:bold;color:var(--text-muted)">${n}</td>`;
    html += '</tr>';
    for (let hi = 0; hi < 16; hi++) {
      html += `<tr><td style="text-align:right;padding-right:4px;font-weight:bold;color:var(--text-muted)">${nibbles[hi]}</td>`;
      for (let lo = 0; lo < 16; lo++) {
        html += cellDataFn(nibbles[hi] + nibbles[lo], cellSize);
      }
      html += '</tr>';
    }
    html += '</table></div>';
    return html;
  }

  function hashMatrixLegendHtml(labels) {
    return `<div style="margin-top:8px;font-size:0.8em;display:flex;gap:16px;align-items:center;flex-wrap:wrap">
      ${labels.map(l => `<span><span class="legend-swatch ${l.cls}"${l.style ? ' style="'+l.style+'"' : ''}></span> ${l.text}</span>`).join('\n')}
    </div>`;
  }

  // --- Shared cell classification for hash matrix ---

  function classifyHashCell(count, isConfirmedCollision, isPossibleConflict) {
    if (count === 0) return { cls: 'hash-cell-empty', bg: '' };
    if (!isConfirmedCollision && !isPossibleConflict) return { cls: 'hash-cell-taken', bg: '' };
    if (isPossibleConflict) return { cls: 'hash-cell-possible', bg: '' };
    const t = Math.min((count - 2) / 4, 1);
    // #1668-M4 r1: gradient endpoints darkened from orange-500→red-500 (3.11:1/3.96:1 vs #fff)
    // to orange-800→red-800 (7.31:1→8.31:1 vs #fff) — passes WCAG AA across the whole range.
    // Encoding stays distinguishable from green-700 (taken) and yellow-800 (possible).
    const cr = Math.round(154 + (-1) * t);   // 154 → 153
    const cg = Math.round(52  + (-25) * t);  // 52  → 27
    const cb = Math.round(18  + 9 * t);      // 18  → 27
    return { cls: 'hash-cell-collision', bg: `background:rgb(${cr},${cg},${cb});` };
  }

  // hashCellTd — emits a hash-matrix <td>. Tooltip data rides as separate
  // data-tip-* attributes (plain text per field); buildMatrixTipChildren()
  // materializes the styled DOM on mouseover. NEVER carry HTML through
  // data-tip-* — see ANL-1 (PR #1539).
  function hashCellTd(hex, cellSize, cls, bg, count, tipSpec, fontWeight) {
    // tipSpec: { hex: string, status?: string, lines?: string[] }
    const spec = tipSpec || {};
    const hexAttr = ' data-tip-hex="' + esc(spec.hex || '') + '"';
    const statusAttr = spec.status
      ? ' data-tip-status="' + esc(spec.status) + '"'
      : '';
    // Join row lines with U+001F (Unit Separator) — a control char that
    // never appears in node names, so we can split safely on the read side.
    const linesAttr = (spec.lines && spec.lines.length)
      ? ' data-tip-lines="' + esc(spec.lines.join('\u001f')) + '"'
      : '';
    return `<td class="hash-cell ${cls}${count ? ' hash-active' : ''}" data-hex="${hex}"${hexAttr}${statusAttr}${linesAttr} style="width:${cellSize}px;height:${cellSize}px;text-align:center;${bg}border:1px solid var(--border);cursor:${count ? 'pointer' : 'default'};font-size:12px;font-weight:${fontWeight}">${hex}</td>`;
  }

  // hashTooltipSpec — returns a plain-data tooltip descriptor consumed by
  // hashCellTd and rendered by buildMatrixTipChildren. Plain text only;
  // no HTML, no markup. `lines` is an array of pre-formatted row strings.
  function hashTooltipSpec(hexLabel, statusText, lines) {
    const spec = { hex: hexLabel, status: statusText };
    if (lines && lines.length) spec.lines = lines;
    return spec;
  }

  function renderHashMatrixPanel(el, statCardsHtml, cellRendererFn, detailMaxWidth, legendLabels, clickHandlerFn) {
    const nibbles = '0123456789ABCDEF'.split('');
    const cellSize = 36;
    const headerSize = 24;
    let html = statCardsHtml;
    html += hashMatrixGridHtml(nibbles, cellSize, headerSize, cellRendererFn);
    html += `<div id="hashDetail" style="flex:1;min-width:200px;max-width:${detailMaxWidth}px;font-size:0.85em"></div></div>`;
    html += hashMatrixLegendHtml(legendLabels);
    el.innerHTML = html;
    initMatrixTooltip(el);
    // #1473 — Grey out cells whose first byte the MeshCore firmware keygen
    // routine avoids (pub_key[0] in {0x00, 0xFF}). This is a keygen
    // CONVENTION, not a protocol-level rejection — see firmware
    // examples/simple_repeater/main.cpp:83 (HEAD 8ede7641). Must run BEFORE
    // we wire click handlers so .hash-active is stripped first.
    if (typeof PrefixReserved !== 'undefined' && PrefixReserved && typeof PrefixReserved.markReservedCells === 'function') {
      PrefixReserved.markReservedCells(el);
    }
    el.querySelectorAll('.hash-active').forEach(td => {
      td.addEventListener('click', () => {
        clickHandlerFn(td);
        el.querySelectorAll('.hash-selected').forEach(c => c.classList.remove('hash-selected'));
        td.classList.add('hash-selected');
      });
    });
  }

  function renderHashMatrixFromServer(sizeData, bytes) {
    const el = document.getElementById('hashMatrix');
    if (!sizeData) { el.innerHTML = '<div class="text-muted">No data</div>'; return; }
    const stats = sizeData.stats || {};
    const totalNodes = stats.total_nodes || 0;

    // 3-byte: show a summary panel instead of a matrix
    if (bytes === 3) {
      el.innerHTML = hashStatCardsHtml(totalNodes, stats.using_this_size || 0, '3-byte', 16777216, stats.unique_prefixes || 0, stats.collision_count || 0) +
        `<p class="text-muted" style="margin:0;font-size:0.8em">The 3-byte prefix space (16.7M values) is too large to visualize as a grid.${(stats.collision_count || 0) > 0 ? ' See collision details below.' : ''}</p>` +
        `<p class="text-muted" style="margin:8px 0 0;font-size:0.8em">ℹ️ This tab only counts collisions among repeaters configured for this hash size. The <a href="#/analytics?tab=prefix-tool" style="color:var(--link-color)">Prefix Tool</a> checks all repeaters regardless of configured hash size.</p>`;
      return;
    }

    if (bytes === 1) {
      const oneByteCells = sizeData.one_byte_cells || {};
      const oneByteCount = stats.using_this_size || 0;
      const oneUsed = Object.values(oneByteCells).filter(v => v.length > 0).length;
      const oneCollisions = Object.values(oneByteCells).filter(v => v.length > 1).length;

      renderHashMatrixPanel(el,
        hashStatCardsHtml(totalNodes, oneByteCount, '1-byte', 256, oneUsed, oneCollisions),
        (hex, cs) => {
          const nodes = oneByteCells[hex] || [];
          const count = nodes.length;
          const repeaterCount = nodes.filter(n => n.role === 'repeater').length;
          const isCollision = count >= 2 && repeaterCount >= 2;
          const isPossible = count >= 2 && !isCollision;
          const { cls, bg } = classifyHashCell(count, isCollision, isPossible);
          const nodeLabel = m => (m.name || m.public_key.slice(0,12)) + (!m.role ? ' (unknown role)' : '');
          const previewLines = nodes.slice(0,5).map(nodeLabel);
          if (nodes.length > 5) previewLines.push(`+${nodes.length-5} more`);
          const tip = count === 0 ? hashTooltipSpec(`0x${hex}`, 'Available')
            : count === 1 ? hashTooltipSpec(`0x${hex}`, 'One node — no collision', [nodeLabel(nodes[0])])
            : isPossible ? hashTooltipSpec(`0x${hex}`, `${count} nodes — POSSIBLE CONFLICT`, previewLines)
            : hashTooltipSpec(`0x${hex}`, `${count} nodes — COLLISION`, previewLines);
          return hashCellTd(hex, cs, cls, bg, count, tip, count >= 2 ? '700' : '400');
        },
        400,
        [
          {cls: 'hash-cell-empty', style: 'border:1px solid var(--border)', text: 'Available'},
          {cls: 'hash-cell-taken', text: 'One node'},
          {cls: 'hash-cell-possible', text: 'Possible conflict'},
          {cls: 'hash-cell-collision', style: 'background:rgb(154,40,22)', text: 'Collision'}
        ],
        (td) => {
          const hex = td.dataset.hex.toUpperCase();
          const matches = oneByteCells[hex] || [];
          const detail = document.getElementById('hashDetail');
          if (!matches.length) { detail.innerHTML = `<strong class="mono">0x${hex}</strong><br><span class="text-muted">No known nodes</span>`; return; }
          detail.innerHTML = `<strong class="mono" style="font-size:1.1em">0x${hex}</strong> — ${matches.length} node${matches.length !== 1 ? 's' : ''}` +
            `<div style="margin-top:8px">${matches.map(m => {
              const coords = (m.lat && m.lon && !(m.lat === 0 && m.lon === 0)) ? `<span class="text-muted" style="font-size:0.8em">(${m.lat.toFixed(2)}, ${m.lon.toFixed(2)})</span>` : '<span class="text-muted" style="font-size:0.8em">(no coords)</span>';
              const role = m.role ? `<span class="badge" style="font-size:0.7em;padding:1px 4px;background:var(--border)">${esc(m.role)}</span> ` : '';
              return `<div style="padding:3px 0">${role}<a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a> ${coords}</div>`;
            }).join('')}</div>`;
        }
      );

    } else if (bytes === 2) {
      const twoByteCells = sizeData.two_byte_cells || {};
      const twoByteCount = stats.using_this_size || 0;
      const uniqueTwoBytePrefixes = stats.unique_prefixes || 0;
      const twoCollisions = Object.values(twoByteCells).filter(v => v.collision_count > 0).length;

      renderHashMatrixPanel(el,
        hashStatCardsHtml(totalNodes, twoByteCount, '2-byte', 65536, uniqueTwoBytePrefixes, twoCollisions),
        (hex, cs) => {
          const info = twoByteCells[hex] || { group_nodes: [], max_collision: 0, collision_count: 0, two_byte_map: {} };
          const nodeCount = (info.group_nodes || []).length;
          const maxCol = info.max_collision || 0;
          const overlapping = Object.values(info.two_byte_map || {}).filter(v => v.length > 1);
          const hasConfirmed = overlapping.some(ns => ns.filter(n => n.role === 'repeater').length >= 2);
          const hasPossible = !hasConfirmed && overlapping.some(ns => ns.length >= 2);
          const { cls, bg } = classifyHashCell(maxCol > 0 ? maxCol : nodeCount === 0 ? 0 : 1, hasConfirmed, hasPossible);
          const nodeLabel2 = m => (m.name || m.public_key.slice(0,8)) + (!m.role ? ' (?)' : '');
          const tip = nodeCount === 0
            ? hashTooltipSpec(`0x${hex}__`, 'No nodes in this group')
            : (info.collision_count || 0) === 0
              ? hashTooltipSpec(`0x${hex}__`, `${nodeCount} node${nodeCount>1?'s':''} — no 2-byte collisions`)
              : hashTooltipSpec(`0x${hex}__`,
                  hasConfirmed ? (info.collision_count||0) + ' collision' + ((info.collision_count||0)>1?'s':'') : 'Possible conflict',
                  Object.entries(info.two_byte_map||{}).filter(([,v])=>v.length>1).slice(0,4).map(([p,ns])=>`${p} — ${ns.map(nodeLabel2).join(', ')}`));
          return hashCellTd(hex, cs, cls, bg, nodeCount, tip, maxCol > 0 ? '700' : '400');
        },
        420,
        [
          {cls: 'hash-cell-empty', style: 'border:1px solid var(--border)', text: 'No nodes in group'},
          {cls: 'hash-cell-taken', text: 'Nodes present, no collision'},
          {cls: 'hash-cell-possible', text: 'Possible conflict'},
          {cls: 'hash-cell-collision', style: 'background:rgb(154,40,22)', text: 'Collision'}
        ],
        (td) => {
          const hex = td.dataset.hex.toUpperCase();
          const info = twoByteCells[hex];
          const detail = document.getElementById('hashDetail');
          if (!info || !(info.group_nodes || []).length) { detail.innerHTML = ''; return; }
          const groupNodes = info.group_nodes || [];
          let dhtml = `<strong class="mono" style="font-size:1.1em">0x${hex}__</strong> — ${groupNodes.length} node${groupNodes.length !== 1 ? 's' : ''} in group`;
          if ((info.collision_count || 0) === 0) {
            dhtml += `<div class="text-muted" style="margin-top:6px;font-size:0.85em"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No 2-byte collisions in this group</div>`;
            dhtml += `<div style="margin-top:8px">${groupNodes.map(m => {
              const prefix = m.public_key.slice(0,4).toUpperCase();
              return `<div style="padding:2px 0"><code class="mono" style="font-size:0.85em">${prefix}</code> <a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a></div>`;
            }).join('')}</div>`;
          } else {
            dhtml += `<div style="margin-top:8px">`;
            for (const [twoHex, nodes] of Object.entries(info.two_byte_map || {}).sort()) {
              const isCollision = nodes.length > 1;
              dhtml += `<div style="margin-bottom:6px;padding:4px 6px;border-radius:4px;background:${isCollision ? 'rgba(220,50,30,0.1)' : 'transparent'};border:1px solid ${isCollision ? 'rgba(220,50,30,0.3)' : 'transparent'}">`;
              dhtml += `<code class="mono" style="font-size:0.9em;font-weight:${isCollision?'700':'400'}">${twoHex}</code>${isCollision ? ' <span style="color:var(--danger);font-size:0.75em;font-weight:700">COLLISION</span>' : ''} `;
              dhtml += nodes.map(m => `<a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link" style="font-size:0.85em">${esc(m.name || m.public_key.slice(0,12))}</a>`).join(', ');
              dhtml += `</div>`;
            }
            dhtml += '</div>';
          }
          detail.innerHTML = dhtml;
        }
      );
    }
  }

  function renderCollisionsFromServer(sizeData, bytes) {
    const el = document.getElementById('collisionList');
    if (!sizeData) { el.innerHTML = '<div class="text-muted">No data</div>'; return; }
    const collisions = sizeData.collisions || [];

    if (!collisions.length) {
      const cleanMsg = bytes === 3
        ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No 3-byte prefix collisions detected — all repeaters have unique 3-byte prefixes.'
        : `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No ${bytes}-byte collisions detected`;
      el.innerHTML = `<div class="text-muted" style="padding:8px">${cleanMsg}</div>`;
      return;
    }

    const showAppearances = bytes < 3;
    const t50 = formatDistanceRound(50);
    const t200 = formatDistanceRound(200);
    el.innerHTML = `<table class="analytics-table">
      <thead><tr>
        <th scope="col">Prefix</th>
        ${showAppearances ? '<th scope="col">Appearances</th>' : ''}
        <th scope="col">Max Distance</th>
        <th scope="col">Assessment</th>
        <th scope="col">Colliding Nodes</th>
      </tr></thead>
      <tbody>${collisions.map(c => {
        let badge, tooltip;
        if (c.classification === 'local') {
          badge = `<span class="badge" style="background:var(--status-green);color:#fff" title="All nodes within ${t50} — likely true collision, same RF neighborhood"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-buildings"/></svg> Local</span>`;
          tooltip = 'Nodes close enough for direct RF — probably genuine prefix collision';
        } else if (c.classification === 'regional') {
          badge = `<span class="badge" style="background:var(--status-yellow);color:#fff" title="Nodes ${t50}–${t200} apart — edge of LoRa range, could be atmospheric"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lightning"/></svg> Regional</span>`;
          tooltip = 'At edge of 915MHz range — could indicate atmospheric ducting or hilltop-to-hilltop links';
        } else if (c.classification === 'distant') {
          badge = `<span class="badge" style="background:var(--status-red);color:#fff" title="Nodes >${t200} apart — beyond typical 915MHz range"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-globe"/></svg> Distant</span>`;
          tooltip = 'Beyond typical LoRa range — likely internet bridging, MQTT gateway, or separate mesh networks sharing prefix';
        } else {
          badge = '<span class="badge" style="background:#6b7280;color:#fff"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg> Unknown</span>';
          tooltip = 'Not enough coordinate data to classify';
        }
        const nodes = c.nodes || [];
        const distStr = c.with_coords >= 2 ? formatDistanceRound(c.max_dist_km) : '<span class="text-muted">—</span>';
        return `<tr>
          <td class="mono">${c.prefix}</td>
          ${showAppearances ? `<td>${(c.appearances || 0).toLocaleString()}</td>` : ''}
          <td>${distStr}</td>
          <td title="${tooltip}">${badge}</td>
          <td>${nodes.map(m => {
            const loc = (m.lat && m.lon && !(m.lat === 0 && m.lon === 0))
              ? ` <span class="text-muted" style="font-size:0.75em">(${m.lat.toFixed(2)}, ${m.lon.toFixed(2)})</span>`
              : ' <span class="text-muted" style="font-size:0.75em">(no coords)</span>';
            return `<a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a>${loc}`;
          }).join('<br>')}</td>
        </tr>`;
      }).join('')}</tbody>
    </table>
    <div class="text-muted" style="padding:8px;font-size:0.8em">
      <strong><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-buildings"/></svg> Local</strong> &lt;${t50}: true prefix collision, same mesh area &nbsp;
      <strong><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lightning"/></svg> Regional</strong> ${t50}–${t200}: edge of LoRa range, possible atmospheric propagation &nbsp;
      <strong><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-globe"/></svg> Distant</strong> &gt;${t200}: beyond 915MHz range — internet bridge, MQTT gateway, or separate networks
    </div>`;
  }
    async function renderSubpaths(el) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Analyzing route patterns…</div>';
    try {
      const rq = RegionFilter.regionQueryString();
      // Issue #1217: thread the Time window picker into the Route Patterns
      // request so the chart actually reflects the user's selection.
      const twEl = document.getElementById('analyticsTimeWindow');
      const twVal = twEl ? twEl.value : '';
      const tws = twVal ? '&window=' + encodeURIComponent(twVal) : '';
      const bulk = await api('/analytics/subpaths-bulk?groups=2-2:50,3-3:30,4-4:20,5-8:15' + rq + tws, { ttl: CLIENT_TTL.analyticsRF });
      const [d2, d3, d4, d5] = bulk.results;

      function renderTable(data, title) {
        if (!data.subpaths.length) return `<h4>${title}</h4><div class="text-muted">No data</div>`;
        // #1633 — when "Hide 1-byte path hops" is ON, filter route patterns
        // whose underlying rawHops contain any 1-byte hex token. We filter
        // INPUT (not just CSS-hide) so the displayed % and ordering reflect
        // the surviving population.
        const _hide1 = !!(typeof window !== 'undefined' && window.MC_getHide1ByteHops && window.MC_getHide1ByteHops());
        // #1784 — path trust threshold: filter patterns whose hops are below
        // the configured minimum hash bytes for mapping. Operators set this
        // via pathTrust.minHashBytesForMapping in config.json (default 2).
        const _trustTT = (typeof window !== 'undefined' && window.MC_getPathTrustThreshold) ? window.MC_getPathTrustThreshold() : 1;
        const _hop1 = function (h) { return String(h || '').length === 2; };
        const _meetsTrust = function (h) {
          if (_trustTT <= 1) return true;
          return window.MC_meetsPathTrust ? window.MC_meetsPathTrust(h) : true;
        };
        const subpaths = (_hide1 || _trustTT >= 2)
          ? data.subpaths.filter(function (s) {
              var rh = s.rawHops || [];
              for (var k = 0; k < rh.length; k++) {
                if (_hide1 && _hop1(rh[k])) return false;
                if (_trustTT >= 2 && !_meetsTrust(rh[k])) return false;
              }
              return true;
            })
          : data.subpaths;
        if (!subpaths.length) {
          var _reason = [];
          if (_hide1) _reason.push('1-byte hide toggle');
          if (_trustTT >= 2) _reason.push(_trustTT + '-byte trust threshold');
          return `<h4>${title}</h4><div class="text-muted">No data (all matching routes filtered: ${_reason.join(' + ')} — adjust in customizer or config.json)</div>`;
        }
        const maxCount = subpaths[0]?.count || 1;
        var _filterNote = '';
        if (_hide1 && _trustTT >= 2) _filterNote = ` · showing ${subpaths.length} of ${data.subpaths.length} (1-byte hide + ${_trustTT}-byte trust)`;
        else if (_hide1) _filterNote = ` · showing ${subpaths.length} of ${data.subpaths.length} (1-byte filtered)`;
        else if (_trustTT >= 2) _filterNote = ` · showing ${subpaths.length} of ${data.subpaths.length} (${_trustTT}-byte trust threshold applied)`;
        return `<h4>${title}</h4>
          <p class="text-muted" style="margin:4px 0 8px">From ${data.totalPaths.toLocaleString()} paths with 2+ hops${_filterNote}</p>
          <table class="analytics-table"><thead><tr>
            <th scope="col">#</th><th scope="col">Route</th><th scope="col">Occurrences</th><th scope="col">% of paths</th><th scope="col">Frequency</th>
          </tr></thead><tbody>
          ${subpaths.map((s, i) => {
            const barW = Math.max(2, Math.round(s.count / maxCount * 100));
            const hops = s.path.split(' → ');
            const rawHops = s.rawHops || [];
            const hasSelfLoop = hops.some((h, j) => j > 0 && h === hops[j - 1]);
            const routeDisplay = hops.map(h => esc(h)).join(' → ');
            const prefixDisplay = rawHops.join(' → ');
            return `<tr data-hops="${esc(rawHops.join(','))}" ${hasSelfLoop ? 'class="subpath-selfloop"' : ''} style="cursor:pointer">
              <td>${i + 1}</td>
              <td>${routeDisplay}${hasSelfLoop ? ' <span title="Contains self-loop — likely 1-byte prefix collision" style="cursor:help"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-arrow-clockwise"/></svg></span>' : ''}<br><span class="hop-prefix mono">${esc(prefixDisplay)}</span></td>
              <td>${s.count.toLocaleString()}</td>
              <td>${s.pct}%</td>
              <td><div style="background:${hasSelfLoop ? 'var(--status-yellow)' : 'var(--accent)'};height:14px;border-radius:3px;width:${barW}%;opacity:0.7"></div></td>
            </tr>`;
          }).join('')}
          </tbody></table>`;
      }

      el.innerHTML = `
        <div class="subpath-layout">
          <div class="subpath-list" id="subpathList">
            <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-path"/></svg> Route Pattern Analysis</h3>
            <p>Click a route to see details. Most common subpaths — reveals backbone routes, bottlenecks, and preferred relay chains.</p>
            <label style="display:inline-flex;align-items:center;gap:6px;margin-bottom:12px;cursor:pointer;font-size:0.9em">
              <input type="checkbox" id="hideCollisions" aria-label="Hide likely prefix collisions" ${localStorage.getItem('subpath-hide-collisions') === '1' ? 'checked' : ''}> Hide likely prefix collisions (self-loops)
            </label>
            <div class="subpath-jump-nav">
              <span>Jump to:</span>
              <a href="#sp-pairs">Pairs</a>
              <a href="#sp-triples">Triples</a>
              <a href="#sp-quads">Quads</a>
              <a href="#sp-long">5+ hops</a>
            </div>
            <div id="sp-pairs">${renderTable(d2, 'Pairs (2-hop links)')}</div>
            <div id="sp-triples">${renderTable(d3, 'Triples (3-hop chains)')}</div>
            <div id="sp-quads">${renderTable(d4, 'Quads (4-hop chains)')}</div>
            <div id="sp-long">${renderTable(d5, 'Long chains (5+ hops)')}</div>
          </div>
          <div class="subpath-detail collapsed" id="subpathDetail">
            <div class="text-muted" style="padding:40px;text-align:center">Select a route to view details</div>
          </div>
        </div>`;

      // Click handler for rows
      el.addEventListener('click', e => {
        const tr = e.target.closest('tr[data-hops]');
        if (!tr) return;
        el.querySelectorAll('tr.subpath-selected').forEach(r => r.classList.remove('subpath-selected'));
        tr.classList.add('subpath-selected');
        loadSubpathDetail(tr.dataset.hops);
      });

      // Jump nav — scroll within list panel
      el.querySelectorAll('.subpath-jump-nav a').forEach(a => {
        a.addEventListener('click', e => {
          e.preventDefault();
          const target = document.getElementById(a.getAttribute('href').slice(1));
          if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
        });
      });

      // Collision toggle
      const toggle = document.getElementById('hideCollisions');
      function applyCollisionFilter() {
        const hide = toggle.checked;
        localStorage.setItem('subpath-hide-collisions', hide ? '1' : '0');
        el.querySelectorAll('tr.subpath-selfloop').forEach(r => r.style.display = hide ? 'none' : '');
      }
      toggle.addEventListener('change', applyCollisionFilter);
      applyCollisionFilter();
    } catch (e) {
      el.innerHTML = `<div class="text-muted">Error loading subpath data: ${e.message}</div>`;
    }
  }

  async function loadSubpathDetail(hopsStr) {
    const panel = document.getElementById('subpathDetail');
    panel.classList.remove('collapsed');
    panel.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading…</div>';
    try {
      const data = await api('/analytics/subpath-detail?hops=' + encodeURIComponent(hopsStr), { ttl: CLIENT_TTL.analyticsRF });
      renderSubpathDetail(panel, data);
    } catch (e) {
      panel.innerHTML = `<div class="text-muted">Error: ${e.message}</div>`;
    }
  }

  function renderSubpathDetail(panel, data) {
    const nodesWithLoc = data.nodes.filter(n => n.lat && n.lon && !(n.lat === 0 && n.lon === 0));
    const hasMap = nodesWithLoc.length >= 2;
    const maxHour = Math.max(...data.hourDistribution, 1);

    panel.innerHTML = `
      <div class="subpath-detail-inner">
        <h4>${data.nodes.map(n => esc(n.name)).join(' → ')}</h4>
        <div class="subpath-meta">
          <span class="hop-prefix mono">${data.hops.join(' → ')}</span>
          <span>${data.totalMatches.toLocaleString()} occurrences</span>
        </div>

        ${nodesWithLoc.length >= 2 ? `<div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-ruler"/></svg> Hop Distances</h5>
          ${(() => {
            const dists = [];
            let total = 0;
            for (let i = 0; i < data.nodes.length - 1; i++) {
              const a = data.nodes[i], b = data.nodes[i+1];
              if (a.lat && a.lon && b.lat && b.lon && !(a.lat===0&&a.lon===0) && !(b.lat===0&&b.lon===0)) {
                const km = window.HopResolver && window.HopResolver.haversineKm
                  ? window.HopResolver.haversineKm(a.lat, a.lon, b.lat, b.lon)
                  : (() => { const R=6371, dLat=(b.lat-a.lat)*Math.PI/180, dLon=(b.lon-a.lon)*Math.PI/180, h=Math.sin(dLat/2)**2+Math.cos(a.lat*Math.PI/180)*Math.cos(b.lat*Math.PI/180)*Math.sin(dLon/2)**2; return R*2*Math.atan2(Math.sqrt(h),Math.sqrt(1-h)); })();
                total += km;
                const cls = km > 200 ? 'color:var(--status-red);font-weight:bold' : km > 50 ? 'color:var(--status-yellow)' : 'color:var(--status-green-text)';
                dists.push(`<div style="padding:2px 0"><span style="${cls}">${formatDistance(km)}</span> <span class="text-muted">${esc(a.name)} → ${esc(b.name)}</span></div>`);
              } else {
                dists.push(`<div style="padding:2px 0"><span class="text-muted">? ${esc(a.name)} → ${esc(b.name)} (no coords)</span></div>`);
              }
            }
            if (dists.length > 1) dists.push(`<div style="padding:4px 0;border-top:1px solid var(--border);margin-top:4px"><strong>Total: ${formatDistance(total)}</strong></div>`);
            return dists.join('');
          })()}
        </div>` : ''}

        ${hasMap ? '<div id="subpathMap" style="height:200px;border-radius:8px;margin:12px 0;border:1px solid var(--border,#e5e7eb)"></div>' : ''}

        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> Observer Receive Signal</h5>
          <p class="text-muted" style="font-size:0.8em;margin:0 0 4px">Last hop → observer only, not between nodes in the route</p>
          ${data.signal.avgSnr != null
            ? `<div>Avg SNR: <strong>${data.signal.avgSnr} dB</strong> · Avg RSSI: <strong>${data.signal.avgRssi} dBm</strong> · ${data.signal.samples} samples</div>`
            : '<div class="text-muted">No signal data</div>'}
        </div>

        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clock"/></svg> Activity by Hour (UTC)</h5>
          <div class="hour-chart">
            ${data.hourDistribution.map((c, h) => `<div class="hour-bar" title="${h}:00 UTC — ${c} packets" style="height:${Math.max(2, c / maxHour * 100)}%"></div>`).join('')}
          </div>
          <div class="hour-labels"><span>0</span><span>6</span><span>12</span><span>18</span><span>23</span></div>
        </div>

        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clock"/></svg> Timeline</h5>
          <div>First seen: ${data.firstSeen ? (typeof formatAbsoluteTimestamp === 'function' ? formatAbsoluteTimestamp(data.firstSeen) : new Date(data.firstSeen).toLocaleString()) : '—'}</div>
          <div>Last seen: ${data.lastSeen ? (typeof formatAbsoluteTimestamp === 'function' ? formatAbsoluteTimestamp(data.lastSeen) : new Date(data.lastSeen).toLocaleString()) : '—'}</div>
        </div>

        ${data.observers.length ? `
        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> Observers</h5>
          ${data.observers.map(o => `<div>${esc(o.name)}: ${o.count}</div>`).join('')}
        </div>` : ''}

        ${data.parentPaths.length ? `
        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-link"/></svg> Full Paths Containing This Route</h5>
          <div class="parent-paths">
            ${data.parentPaths.map(p => `<div class="parent-path"><span class="mono" style="font-size:0.85em">${esc(p.path)}</span> <span class="text-muted">×${p.count}</span></div>`).join('')}
          </div>
        </div>` : ''}
      </div>`;

    // Render minimap
    if (hasMap && typeof L !== 'undefined') {
      const map = L.map('subpathMap', { zoomControl: false, attributionControl: false });
      L.tileLayer(getTileUrl(), { maxZoom: 18 }).addTo(map);

      const latlngs = [];
      nodesWithLoc.forEach((n, i) => {
        const ll = [n.lat, n.lon];
        latlngs.push(ll);
        const isEnd = i === 0 || i === nodesWithLoc.length - 1;
        L.circleMarker(ll, {
          radius: isEnd ? 8 : 5,
          color: isEnd ? (i === 0 ? statusGreen() : statusRed()) : statusYellow(),
          fillColor: isEnd ? (i === 0 ? statusGreen() : statusRed()) : statusYellow(),
          fillOpacity: 0.9, weight: 2
        }).bindTooltip(esc(n.name), { permanent: false }).addTo(map);
      });

      L.polyline(latlngs, { color: statusYellow(), weight: 3, dashArray: '8,6', opacity: 0.8 }).addTo(map);
      map.fitBounds(L.latLngBounds(latlngs).pad(0.3));
    }
  }

  async function renderNodesTab(el) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading node analytics…</div>';
    try {
      const rq = RegionFilter.regionQueryString() + AreaFilter.areaQueryString();
      const [nodesResp, bulkHealth] = await Promise.all([
        fetchAllNodes('&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList }),
        api('/nodes/bulk-health?limit=50' + rq, { ttl: CLIENT_TTL.analyticsRF })
      ]);
      const nodes = nodesResp.nodes || nodesResp;
      const myNodes = JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]');
      const myKeys = new Set(myNodes.map(n => n.pubkey));

      // Map bulk health by pubkey
      const healthMap = {};
      bulkHealth.forEach(h => { healthMap[h.public_key] = h; });
      const enriched = nodes.filter(n => healthMap[n.public_key]).map(n => ({ ...n, health: { stats: healthMap[n.public_key].stats, observers: healthMap[n.public_key].observers } }));

      // Compute rankings
      const byPackets = [...enriched].sort((a, b) => (b.health.stats.totalTransmissions || b.health.stats.totalPackets || 0) - (a.health.stats.totalTransmissions || a.health.stats.totalPackets || 0));
      const bySnr = [...enriched].filter(n => n.health.stats.avgSnr != null).sort((a, b) => b.health.stats.avgSnr - a.health.stats.avgSnr);
      const byObservers = [...enriched].sort((a, b) => (b.health.observers?.length || 0) - (a.health.observers?.length || 0));
      const byRecent = [...enriched].filter(n => n.health.stats.lastHeard).sort((a, b) => new Date(b.health.stats.lastHeard) - new Date(a.health.stats.lastHeard));

      // Compute network status client-side from loaded nodes using shared getHealthThresholds()
      const now = Date.now();
      let active = 0, degraded = 0, silent = 0;
      nodes.forEach(function(n) {
        const role = n.role || 'unknown';
        const th = getHealthThresholds(role);
        const lastMs = n.last_heard ? new Date(n.last_heard).getTime()
                     : n.last_seen ? new Date(n.last_seen).getTime()
                     : 0;
        const age = lastMs ? (now - lastMs) : Infinity;
        if (age < th.degradedMs) active++;
        else if (age < th.silentMs) degraded++;
        else silent++;
      });
      const totalNodes = nodesResp.total || nodes.length;
      const roleCounts = nodesResp.counts || {};

      function nodeLink(n) {
        return `<a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link">${esc(n.name || n.public_key.slice(0, 12))}</a>`;
      }
      function claimedBadge(n) {
        return myKeys.has(n.public_key) ? ' <span style="color:var(--link-color);font-size:10px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star-fill"/></svg> MINE</span>' : '';
      }

      // ROLE_COLORS from shared roles.js

      el.innerHTML = `
        <div class="analytics-section">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> Network Status</h3>
          <div style="display:flex;gap:16px;flex-wrap:wrap;margin-bottom:20px">
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700;color:var(--status-green-text)">${active}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)"><span style="color:var(--status-green-text)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span> Active</div>
            </div>
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700;color:var(--status-yellow)">${degraded}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)"><span style="color:var(--status-yellow)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span> Degraded</div>
            </div>
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700;color:var(--status-red)">${silent}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)"><span style="color:var(--status-red)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span> Silent</div>
            </div>
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700">${totalNodes}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)">Total Nodes</div>
            </div>
          </div>

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Role Breakdown</h3>
          <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:24px">
            ${Object.entries(roleCounts).sort((a,b) => b[1]-a[1]).map(([role, count]) => {
              const c = ROLE_COLORS[role] || '#6b7280';
              return `<span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(c)) || ('background:'+c+';color:#fff')};padding:6px 12px;font-size:13px">${role}: ${count}</span>`;
            }).join('')}
          </div>

          ${myKeys.size ? `<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star-fill"/></svg> My Claimed Nodes</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Packets</th><th scope="col">Avg SNR</th><th scope="col">Observers</th><th scope="col">Last Heard</th></tr></thead>
            <tbody>
              ${enriched.filter(n => myKeys.has(n.public_key)).map(n => {
                const s = n.health.stats;
                return `<tr>
                  <td>${nodeLink(n)}</td>
                  <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                  <td>${s.totalTransmissions || s.totalPackets || 0}</td>
                  <td>${s.avgSnr != null ? s.avgSnr.toFixed(1) + ' dB' : '—'}</td>
                  <td>${n.health.observers?.length || 0}</td>
                  <td>${s.lastHeard ? timeAgo(s.lastHeard) : '—'}</td>
                </tr>`;
              }).join('') || '<tr><td colspan="6" class="text-muted">No claimed nodes have health data</td></tr>'}
            </tbody>
          </table>` : ''}

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trophy"/></svg> Most Active Nodes</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">#</th><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Total Packets</th><th scope="col">Packets Today</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${byPackets.slice(0, 15).map((n, i) => `<tr>
                <td>${i + 1}</td>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                <td>${n.health.stats.totalTransmissions || n.health.stats.totalPackets || 0}</td>
                <td>${n.health.stats.packetsToday || 0}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link" aria-label="Per-node analytics"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg></a></td>
              </tr>`).join('')}
            </tbody>
          </table>

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-cell-signal-high"/></svg> Best Signal Quality</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">#</th><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Avg SNR</th><th scope="col">Observers</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${bySnr.slice(0, 15).map((n, i) => `<tr>
                <td>${i + 1}</td>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                <td>${n.health.stats.avgSnr.toFixed(1)} dB</td>
                <td>${n.health.observers?.length || 0}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link" aria-label="Per-node analytics"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg></a></td>
              </tr>`).join('')}
            </tbody>
          </table>

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> Most Observed Nodes</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">#</th><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Observers</th><th scope="col">Avg SNR</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${byObservers.slice(0, 15).map((n, i) => `<tr>
                <td>${i + 1}</td>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                <td>${n.health.observers?.length || 0}</td>
                <td>${n.health.stats.avgSnr != null ? n.health.stats.avgSnr.toFixed(1) + ' dB' : '—'}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link" aria-label="Per-node analytics"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg></a></td>
              </tr>`).join('')}
            </tbody>
          </table>

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clock"/></svg> Recently Active</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Last Heard</th><th scope="col">Packets Today</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${byRecent.slice(0, 15).map(n => `<tr>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                <td>${timeAgo(n.health.stats.lastHeard)}</td>
                <td>${n.health.stats.packetsToday || 0}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link" aria-label="Per-node analytics"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg></a></td>
              </tr>`).join('')}
            </tbody>
          </table>
        </div>`;
    } catch (e) {
      el.innerHTML = `<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load node analytics: ${esc(e.message)}</div>`;
    }
  }

  // ===================== MY REPEATERS (favorites monitoring dashboard) =====================
  // An at-a-glance monitor over the repeaters the operator has starred
  // (meshcore-favorites). Pure frontend aggregation over existing endpoints:
  //   /api/nodes                      relay activity + traffic/bridge scores
  //   /api/nodes/clock-skew           per-node "is the clock OK" severity
  //   /api/analytics/neighbor-graph   affinity edges, filtered to favorites
  //                                   ("throughput between my repeaters")
  function _mrSummaryCard(value, label, color) {
    return `<div class="analytics-stat-card" style="flex:1;min-width:110px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
      <div style="font-size:26px;font-weight:700${color ? ';color:' + color : ''}">${esc(String(value))}</div>
      <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)">${esc(label)}</div>
    </div>`;
  }

  // Status is carried by colour AND text — never colour alone — so it is
  // legible to color-blind users and screen readers.
  const MR_STATUS_META = {
    active: { color: 'var(--status-green)', label: 'Active' },
    degraded: { color: 'var(--status-yellow)', label: 'Degraded' },
    silent: { color: 'var(--status-red)', label: 'Silent' },
    unknown: { color: 'var(--text-muted)', label: 'Unknown' },
  };
  function _mrStatusCell(status) {
    // Fall back to an explicit "Unknown" rather than silently mislabelling an
    // unexpected status as "Silent".
    const m = MR_STATUS_META[status] || MR_STATUS_META.unknown;
    return `<span style="display:inline-flex;gap:5px;align-items:center;white-space:nowrap"><span style="color:${m.color}" aria-hidden="true"><svg class="ph-icon"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span><span style="font-size:11px">${m.label}</span></span>`;
  }

  function _mrEmptyState() {
    return `
      <div class="analytics-section" style="text-align:center;padding:48px 24px;color:var(--text-muted)">
        <svg class="ph-icon" aria-hidden="true" style="width:40px;height:40px;opacity:0.6"><use href="/icons/phosphor-sprite.svg#ph-star"/></svg>
        <h3 style="margin:12px 0 6px">No favorite repeaters yet</h3>
        <p>Star a repeater (the <svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star"/></svg> on the <a href="#/nodes" class="analytics-link">Nodes</a> page or a node's detail) to add it to your watch-list. Starred repeaters show up here for at-a-glance monitoring.</p>
      </div>`;
  }

  async function renderMyRepeatersTab(el) {
    const haveFavs = () => (typeof getFavorites === 'function' ? getFavorites() : []);
    if (!haveFavs().length) { el.innerHTML = _mrEmptyState(); return; }
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading your repeaters…</div>';
    try {
      const rq = RegionFilter.regionQueryString() + AreaFilter.areaQueryString();
      // When a region/area filter is active the node fetch is scoped, so a
      // starred repeater outside that scope won't come back. Surface that count
      // instead of silently dropping it from the watch-list (#1761 MAJOR).
      const filterActive = rq.length > 0;
      // Fetch the three sources ONCE; star toggles re-paint from this cache
      // (paint() below) instead of re-hitting the network. Trade-off: this
      // pages the full node list (up to ~10k) to keep only the starred few —
      // fine for a handful of favorites and amortized by CLIENT_TTL.nodeList
      // across tabs. If favorite counts ever grow large, switch to per-node
      // Promise.all(favs.map(pk => api('/nodes/' + pk))).
      const [nodesResp, clockData, graphData] = await Promise.all([
        fetchAllNodes('&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList }),
        api('/nodes/clock-skew', { ttl: CLIENT_TTL.analyticsRF }).catch(() => []),
        api('/analytics/neighbor-graph?min_count=1&min_score=0', { ttl: CLIENT_TTL.analyticsRF }).catch(() => ({ nodes: [], edges: [] })),
      ]);
      const allNodes = nodesResp.nodes || nodesResp;
      const clockByPk = {};
      (Array.isArray(clockData) ? clockData : []).forEach(c => { if (c && c.pubkey) clockByPk[c.pubkey] = c; });

      const now = Date.now();
      function ageOf(n) {
        const ms = n.last_heard ? new Date(n.last_heard).getTime() : n.last_seen ? new Date(n.last_seen).getTime() : 0;
        return ms ? now - ms : Infinity;
      }
      function statusOf(n) {
        const th = (typeof getHealthThresholds === 'function') ? getHealthThresholds(n.role) : { degradedMs: 3600000, silentMs: 86400000 };
        const age = ageOf(n);
        return age < th.degradedMs ? 'active' : age < th.silentMs ? 'degraded' : 'silent';
      }
      const pct = v => (v != null ? (v * 100).toFixed(1) + '%' : '—');
      // #1927: no fallback to usefulness_score. They are different metrics:
      // traffic_share_score is the single traffic axis, usefulness_score is the
      // #672 composite (0.30*bridge + 0.25*coverage + ...), set separately at
      // cmd/server/usefulness_composite.go:147 and :152. Substituting one for the
      // other put a composite under a column and an axis that both promise share
      // of non-advert traffic. A node without the metric now reads as unknown.
      const trafficOf = n => (n.traffic_share_score != null ? n.traffic_share_score : null);
      function roleBadge(role) {
        // Route the unknown-role fallback through ROLE_COLORS.unknown (backed by
        // --mc-role-unknown / the shared Wong palette) instead of a hard-coded
        // literal, so every swatch color flows through the CSS-variable system
        // (#1761 nit). ROLE_COLORS always resolves .unknown.
        const rc = window.ROLE_COLORS || {};
        const c = rc[role] || rc.unknown || 'var(--role-unknown, #6b7280)';
        const style = (window.aaBadgeStyle && window.aaBadgeStyle(c)) || ('background:' + c + ';color:#fff');
        return `<span class="badge" style="${style}">${esc(role)}</span>`;
      }

      // Re-renders from the cached fetch using the LIVE favorites set, so an
      // un-star drops the row with no refetch. Re-binds its stars after each
      // paint (the innerHTML reset clears listeners).
      function paint() {
        const favs = new Set(haveFavs());
        if (!favs.size) { el.innerHTML = _mrEmptyState(); return; }
        const myNodes = allNodes.filter(n => favs.has(n.public_key) && (n.role === 'repeater' || n.role === 'room'));
        const myKeys = new Set(myNodes.map(n => n.public_key));

        // Favorites the scoped fetch didn't return — with a region/area filter
        // active these are (most likely) outside the current scope. Surface the
        // count so a hidden favorite isn't silently missing (#1761 MAJOR).
        const allByPk = new Set(allNodes.map(n => n.public_key));
        // Favorites absent from the scoped fetch: usually outside the active
        // region/area filter, but the set also covers deleted/expired stars
        // and role-filtered clients — so the notice stays causally neutral
        // ("not shown with the active filter") rather than claiming a cause.
        const hiddenByFilter = [...favs].filter(pk => !allByPk.has(pk)).length;
        const filterNotice = (filterActive && hiddenByFilter > 0)
          ? `<p class="text-muted" style="display:flex;gap:6px;align-items:center"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-funnel"/></svg>${hiddenByFilter} favorite${hiddenByFilter === 1 ? '' : 's'} not shown with the active region/area filter.</p>`
          : '';

        let active = 0, degraded = 0, silent = 0, clockOk = 0, totalRelay24 = 0;
        myNodes.forEach(n => {
          const s = statusOf(n); if (s === 'active') active++; else if (s === 'degraded') degraded++; else silent++;
          const cs = clockByPk[n.public_key];
          if (cs && cs.severity === 'ok') clockOk++;
          totalRelay24 += (n.relay_count_24h || 0);
        });

        const rank = { silent: 0, degraded: 1, active: 2 };
        const sorted = myNodes.slice().sort((a, b) => {
          const d = rank[statusOf(a)] - rank[statusOf(b)];
          return d !== 0 ? d : (a.name || '').localeCompare(b.name || '');
        });

        const rowsHtml = sorted.map(n => {
          const cs = clockByPk[n.public_key];
          const clockCell = cs ? renderSkewBadge(cs.severity, currentSkewValue(cs), cs) : '<span class="text-muted">—</span>';
          const battery = n.battery_mv != null ? (n.battery_mv / 1000).toFixed(2) + ' V' : '—';
          return `<tr>
            <td>${favStar(n.public_key)}</td>
            <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link">${esc(n.name || n.public_key.slice(0, 12))}</a></td>
            <td>${roleBadge(n.role)}</td>
            <td>${_mrStatusCell(statusOf(n))}</td>
            <td>${clockCell}</td>
            <td style="text-align:right">${pct(trafficOf(n))}</td>
            <td style="text-align:right">${pct(n.bridge_score)}</td>
            <td style="text-align:right">${n.relay_count_1h != null ? n.relay_count_1h : '—'}</td>
            <td style="text-align:right">${n.relay_count_24h != null ? n.relay_count_24h : '—'}</td>
            <td>${n.last_relayed ? timeAgo(n.last_relayed) : '—'}</td>
            <td>${(n.last_heard || n.last_seen) ? timeAgo(n.last_heard || n.last_seen) : '—'}</td>
            <td style="text-align:right">${battery}</td>
          </tr>`;
        }).join('');

        const nameByPk = {};
        myNodes.forEach(n => { nameByPk[n.public_key] = n.name || n.public_key.slice(0, 12); });
        const myEdges = ((graphData && graphData.edges) || [])
          .filter(e => myKeys.has(e.source) && myKeys.has(e.target))
          .sort((a, b) => (b.score || 0) - (a.score || 0));
        let affinityHtml;
        if (!myEdges.length) {
          affinityHtml = `<p class="text-muted" style="padding:8px 0">No affinity links detected between your repeaters yet — they may be too far apart, or not enough shared traffic has been observed.</p>`;
        } else {
          affinityHtml = `<table class="analytics-table">
            <thead><tr><th scope="col">Link</th><th scope="col" style="text-align:right">Affinity</th><th scope="col" style="text-align:right">Packets</th><th scope="col" style="text-align:right">Avg SNR</th></tr></thead>
            <tbody>${myEdges.map(e => {
              // The affinity graph is undirected — the server marks every edge
              // bidirectional — so the link glyph is always a double arrow.
              const arrow = '&harr;';
              const snr = (e.avg_snr != null) ? e.avg_snr.toFixed(1) + ' dB' : '—';
              return `<tr>
                <td>${esc(nameByPk[e.source] || e.source.slice(0, 12))} ${arrow} ${esc(nameByPk[e.target] || e.target.slice(0, 12))}${e.ambiguous ? ' <svg class="ph-icon" role="img" aria-label="Path attribution ambiguous"><title>Path attribution ambiguous</title><use href="/icons/phosphor-sprite.svg#ph-question"/></svg>' : ''}</td>
                <td style="text-align:right">${pct(e.score)}</td>
                <td style="text-align:right">${e.weight != null ? e.weight : '—'}</td>
                <td style="text-align:right">${snr}</td>
              </tr>`;
            }).join('')}</tbody>
          </table>`;
        }

        el.innerHTML = `
          <div class="analytics-section">
            <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star-fill"/></svg> My Repeaters</h3>
            <p class="text-muted">At-a-glance monitoring over the repeaters you have starred. Un-star a row to drop it from this list.</p>
            ${filterNotice}
            <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(120px,1fr));gap:16px;margin-bottom:20px">
              ${_mrSummaryCard(myNodes.length, 'My Repeaters')}
              ${_mrSummaryCard(active, 'Active')}
              ${_mrSummaryCard(degraded, 'Degraded')}
              ${_mrSummaryCard(silent, 'Silent')}
              ${_mrSummaryCard(clockOk + ' / ' + myNodes.length, 'Clock OK')}
              ${_mrSummaryCard(totalRelay24.toLocaleString(), 'Relays (24h)')}
            </div>

            <table class="analytics-table" id="my-repeaters-table">
              <thead><tr>
                <th scope="col" aria-label="Favorite"></th>
                <th scope="col">Repeater</th>
                <th scope="col">Role</th>
                <th scope="col">Status</th>
                <th scope="col">Clock</th>
                <th scope="col" style="text-align:right">Traffic</th>
                <th scope="col" style="text-align:right">Bridge</th>
                <th scope="col" style="text-align:right" title="Relays in the last hour">Relays 1h</th>
                <th scope="col" style="text-align:right" title="Relays in the last 24 hours">Relays 24h</th>
                <th scope="col">Last Relayed</th>
                <th scope="col">Last Heard</th>
                <th scope="col" style="text-align:right">Battery</th>
              </tr></thead>
              <tbody>${rowsHtml || '<tr><td colspan="12" class="text-muted">None of your favorites are repeaters/rooms in this view.</td></tr>'}</tbody>
            </table>

            <h3 style="margin-top:28px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-graph"/></svg> Links Between My Repeaters</h3>
            <p class="text-muted">Affinity sub-graph filtered to your favorites — how much traffic flows directly between them.</p>
            ${affinityHtml}
          </div>`;

        // Live un-star: re-paint from cache (no refetch) when a star toggles.
        if (typeof bindFavStars === 'function') bindFavStars(el, paint);
      }
      paint();
    } catch (e) {
      el.innerHTML = `<div style="padding:40px;text-align:center;color:var(--status-red)">Failed to load your repeaters: ${esc(e.message)}</div>`;
    }
  }
  // === MY REPEATERS BLOCK END (test harness slices the renderer block to here) ===
  // ===================== REPEATER METRIC SCATTER =====================
  // Each point is a repeater (or room); the axes are two selectable metrics
  // that /api/nodes already attaches to repeater/room rows — the two #672
  // usefulness axes that exist today (traffic_share_score, bridge_score)
  // plus relay activity (relay_count_1h/24h) and advert_count. This view
  // only PLOTS those metrics; nothing is computed client-side.
  const REPEATER_METRIC_AXES = [
    { key: 'traffic',  label: 'Traffic share', score: true,  get: p => p.traffic },
    { key: 'bridge',   label: 'Bridge score',  score: true,  get: p => p.bridge },
    { key: 'relay1h',  label: 'Relays (1h)',   score: false, get: p => p.relay1h },
    { key: 'relay24h', label: 'Relays (24h)',  score: false, get: p => p.relay24h },
    { key: 'adverts',  label: 'Adverts',       score: false, get: p => p.adverts },
  ];

  // _niceCeil rounds v up to a "nice" 1/2/5 x 10^n value for an axis ceiling
  // so the plot fills its area instead of squashing tiny scores against 0.
  function _niceCeil(v) {
    if (!(v > 0)) return 1;
    const pow = Math.pow(10, Math.floor(Math.log10(v)));
    const f = v / pow;
    const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
    return nice * pow;
  }

  // Scores (0..1) render as percentages; count axes render as integers
  // (keyed on axis kind, not on whether a given tick value happens to be
  // whole — niceCeil maxima don't always divide into 5 integer steps).
  function _axisFmt(axis, v) {
    if (v == null) return '—';
    if (!axis.score) return String(Math.round(v));
    // Drop the trailing ".0" on whole-percent gridlines (0%, 20%, 40%); keep a
    // decimal only when the value actually needs it.
    const pct = v * 100;
    return (Number.isInteger(pct) ? pct.toFixed(0) : pct.toFixed(1)) + '%';
  }

  // Build a concrete axis (domain [0, niceCeil(max)]) from a registry entry and
  // a precomputed data max — factored out so draw() can resolve both axes in a
  // SINGLE pass over the points instead of one full pass per axis.
  function _axisFromMax(axis, max) {
    return { key: axis.key, label: axis.label, score: axis.score, get: axis.get, min: 0, max: _niceCeil(max) };
  }

  // Resolve a metric key to a concrete axis with a domain computed from the
  // points actually being plotted. An unknown key falls back to the first axis —
  // the single source of axis-key validation (callers need not pre-check).
  function _resolveAxis(axisKey, points) {
    const axis = REPEATER_METRIC_AXES.find(a => a.key === axisKey) || REPEATER_METRIC_AXES[0];
    let max = 0;
    points.forEach(p => { const v = axis.get(p); if (v != null && v > max) max = v; });
    return _axisFromMax(axis, max);
  }

  // Map /api/nodes rows to plottable points. Pure and factored out of
  // renderRepeaterMetricsTab so the repeater/room filter and the name fallback
  // (name → pubkey prefix → '?') are unit-testable (#1760 review). The traffic
  // fallback to usefulness_score that used to be documented here was removed in
  // #1927; see the note above trafficOf.
  function _toScatterPoints(nodes, favs) {
    return nodes
      .filter(n => n.role === 'repeater' || n.role === 'room')
      .map(n => ({
        pk: n.public_key,
        name: n.name || (n.public_key ? n.public_key.slice(0, 12) : '?'),
        role: n.role,
        fav: favs.has(n.public_key),
        // #1927: see trafficOf above. A null drops the point from the scatter via
        // the plottable filter, the same way a node with no bridge score is dropped.
        traffic: n.traffic_share_score != null ? n.traffic_share_score : null,
        bridge: n.bridge_score != null ? n.bridge_score : null,
        relay1h: n.relay_count_1h != null ? n.relay_count_1h : null,
        relay24h: n.relay_count_24h != null ? n.relay_count_24h : null,
        adverts: n.advert_count != null ? n.advert_count : null,
      }));
  }

  function renderMetricScatter(points, xAxis, yAxis) {
    const w = 620, h = 380, pad = 60;
    const plotW = w - pad * 2, plotH = h - pad * 2;
    const xOf = v => pad + (v - xAxis.min) / (xAxis.max - xAxis.min || 1) * plotW;
    const yOf = v => h - pad - (v - yAxis.min) / (yAxis.max - yAxis.min || 1) * plotH;
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:420px" role="img" aria-label="Scatter plot of repeaters: ${esc(xAxis.label)} versus ${esc(yAxis.label)}"><title>Repeater metric scatter: ${esc(xAxis.label)} (x) vs ${esc(yAxis.label)} (y)</title>`;
    // Gridlines + tick labels (5 steps per axis).
    for (let i = 0; i <= 5; i++) {
      const gx = pad + plotW * i / 5;
      const gy = h - pad - plotH * i / 5;
      const xv = xAxis.min + (xAxis.max - xAxis.min) * i / 5;
      const yv = yAxis.min + (yAxis.max - yAxis.min) * i / 5;
      svg += `<line x1="${gx}" y1="${pad}" x2="${gx}" y2="${h-pad}" stroke="var(--border)" stroke-dasharray="2" opacity="0.5"/>`;
      svg += `<line x1="${pad}" y1="${gy}" x2="${w-pad}" y2="${gy}" stroke="var(--border)" stroke-dasharray="2" opacity="0.5"/>`;
      svg += `<text x="${gx}" y="${h-pad+14}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${esc(_axisFmt(xAxis, xv))}</text>`;
      svg += `<text x="${pad-6}" y="${gy+3}" text-anchor="end" font-size="9" fill="var(--text-muted)">${esc(_axisFmt(yAxis, yv))}</text>`;
    }
    // Axis lines + titles.
    svg += `<line x1="${pad}" y1="${h-pad}" x2="${w-pad}" y2="${h-pad}" stroke="var(--text-muted)" stroke-width="0.75"/>`;
    svg += `<line x1="${pad}" y1="${pad}" x2="${pad}" y2="${h-pad}" stroke="var(--text-muted)" stroke-width="0.75"/>`;
    svg += `<text x="${pad + plotW/2}" y="${h-6}" text-anchor="middle" font-size="11" fill="var(--text-muted)">${esc(xAxis.label)}</text>`;
    svg += `<text x="14" y="${pad + plotH/2}" text-anchor="middle" font-size="11" fill="var(--text-muted)" transform="rotate(-90,14,${pad + plotH/2})">${esc(yAxis.label)}</text>`;
    // Only points with BOTH axis values present are plottable.
    const plottable = points.filter(p => xAxis.get(p) != null && yAxis.get(p) != null);
    if (!plottable.length) {
      svg += `<text x="${pad + plotW / 2}" y="${pad + plotH / 2}" text-anchor="middle" font-size="12" fill="var(--text-muted)">No repeaters have values for both selected metrics.</text></svg>`;
      return svg;
    }
    // Cap plotted points to keep the SVG snappy (~2000 stays smooth in a
    // headless browser), but keep ALL favorites unconditionally and stride
    // only the non-favorites into the remaining budget — so the legend's
    // favorite count can never be a lie (#1760 review).
    const POINT_CAP = 2000;
    let sample = plottable;
    if (plottable.length > POINT_CAP) {
      let favs = plottable.filter(p => p.fav);
      const others = plottable.filter(p => !p.fav);
      // Favorites are kept ahead of non-favorites, but even they are strided
      // if they ALONE exceed the cap — so a pathological favorite count can't
      // blow past POINT_CAP (the "showing N of M" disclosure stays honest).
      if (favs.length > POINT_CAP) {
        favs = favs.filter((_, i) => i % Math.ceil(favs.length / POINT_CAP) === 0);
      }
      const budget = Math.max(0, POINT_CAP - favs.length);
      const strided = (budget > 0 && others.length > budget)
        ? others.filter((_, i) => i % Math.ceil(others.length / budget) === 0)
        : others.slice(0, budget);
      sample = favs.concat(strided);
    }
    // Sampling disclosure (graphical integrity): if we strided the points
    // down, say so in-plot rather than silently hiding the rest.
    if (sample.length < plottable.length) {
      svg += `<text x="${w - pad}" y="${pad - 6}" text-anchor="end" font-size="9" fill="var(--text-muted)">showing ${sample.length} of ${plottable.length} points</text>`;
    }
    // Draw favorites last so their rings are never hidden behind other dots.
    const ordered = sample.slice().sort((a, b) => (a.fav === b.fav ? 0 : a.fav ? 1 : -1));
    // Favorite ring uses the neutral foreground colour, NOT a status colour:
    // statusYellow means "degraded" elsewhere in the dashboard, so reusing it
    // here would cross semantic wires.
    const favColor = 'var(--text)';
    ordered.forEach(p => {
      const cx = xOf(xAxis.get(p)).toFixed(1);
      const cy = yOf(yAxis.get(p)).toFixed(1);
      const color = (window.ROLE_COLORS && window.ROLE_COLORS[p.role]) || 'var(--text-muted)';
      // ' · ' separators, not '\n': SVG <title> tooltips collapse whitespace,
      // so newlines would render as a run-on string.
      const tip = `${p.name} · ${xAxis.label}: ${_axisFmt(xAxis, xAxis.get(p))} · ${yAxis.label}: ${_axisFmt(yAxis, yAxis.get(p))}`;
      // tabindex="-1": with up to 2000 points we keep them clickable but out
      // of the sequential keyboard tab order (the Nodes table is the
      // keyboard-navigable surface for per-node drill-down).
      svg += `<a href="#/nodes/${encodeURIComponent(p.pk)}/analytics" tabindex="-1"><title>${esc(tip)}</title>`;
      if (p.fav) svg += `<circle cx="${cx}" cy="${cy}" r="5.5" fill="none" stroke="${favColor}" stroke-width="1.75"/>`;
      svg += `<circle cx="${cx}" cy="${cy}" r="3" fill="${color}" opacity="0.8"/></a>`;
    });
    svg += '</svg>';
    return svg;
  }

  async function renderRepeaterMetricsTab(el) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading repeater metrics…</div>';
    try {
      const rq = RegionFilter.regionQueryString() + AreaFilter.areaQueryString();
      const resp = await fetchAllNodes('&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList });
      const nodes = resp.nodes || resp;
      const favs = new Set(typeof getFavorites === 'function' ? getFavorites() : []);
      const points = _toScatterPoints(nodes, favs);

      if (!points.length) {
        el.innerHTML = `<div class="analytics-section">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-target"/></svg> Repeater Metric Scatter</h3>
          <div class="text-muted" style="padding:24px">No repeater or room nodes in this view.</div>
        </div>`;
        return;
      }

      // Restore the last-picked axes (default Traffic share x Bridge score). A
      // stale/unknown stored key is tolerated by _resolveAxis's own fallback
      // (single validation path); the <select> then just shows its first option.
      let xKey = localStorage.getItem('meshcore-repeater-scatter-x') || 'traffic';
      let yKey = localStorage.getItem('meshcore-repeater-scatter-y') || 'bridge';
      const opts = sel => REPEATER_METRIC_AXES.map(a => `<option value="${a.key}"${a.key === sel ? ' selected' : ''}>${esc(a.label)}</option>`).join('');

      el.innerHTML = `
        <div class="analytics-section">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-target"/></svg> Repeater Metric Scatter</h3>
          <p class="text-muted">Each point is a repeater or room. Pick two metrics to compare — colors follow node role, favorites are ringed. Click a point for its analytics.</p>
          <p class="text-muted" style="font-size:12px;margin-top:-6px">Units — <strong>Traffic share</strong>: % of non-advert traffic relayed. <strong>Bridge score</strong>: 0–100% (network-normalized betweenness). <strong>Relays (1h/24h)</strong> and <strong>Adverts</strong>: packet counts. Axes auto-scale to the data.</p>
          <div style="display:flex;gap:20px;flex-wrap:wrap;align-items:center;margin-bottom:14px">
            <label style="display:flex;gap:6px;align-items:center">X axis
              <select id="metricScatterX" class="analytics-time-window-select" aria-label="X axis metric">${opts(xKey)}</select>
            </label>
            <label style="display:flex;gap:6px;align-items:center">Y axis
              <select id="metricScatterY" class="analytics-time-window-select" aria-label="Y axis metric">${opts(yKey)}</select>
            </label>
            <span class="text-muted">${points.length} repeater${points.length === 1 ? '' : 's'}</span>
          </div>
          <div id="metricScatterPlot"></div>
          <div id="metricScatterLegend" style="display:flex;gap:16px;flex-wrap:wrap;margin-top:10px;font-size:12px"></div>
        </div>`;

      const plotEl = el.querySelector('#metricScatterPlot');
      const legendEl = el.querySelector('#metricScatterLegend');
      const xSel = el.querySelector('#metricScatterX');
      const ySel = el.querySelector('#metricScatterY');

      function draw() {
        // Both axes share the same point set, so compute their maxima in one
        // pass instead of a full pass per _resolveAxis call.
        const xA = REPEATER_METRIC_AXES.find(a => a.key === xSel.value) || REPEATER_METRIC_AXES[0];
        const yA = REPEATER_METRIC_AXES.find(a => a.key === ySel.value) || REPEATER_METRIC_AXES[0];
        let xMax = 0, yMax = 0;
        points.forEach(p => {
          const xv = xA.get(p); if (xv != null && xv > xMax) xMax = xv;
          const yv = yA.get(p); if (yv != null && yv > yMax) yMax = yv;
        });
        const xAxis = _axisFromMax(xA, xMax), yAxis = _axisFromMax(yA, yMax);
        plotEl.innerHTML = renderMetricScatter(points, xAxis, yAxis);
        // Legend: each role present, plus the favorite-ring marker.
        const roles = Array.from(new Set(points.map(p => p.role)));
        let lg = roles.map(r => {
          const c = (window.ROLE_COLORS && window.ROLE_COLORS[r]) || 'var(--text-muted)';
          return `<span style="display:inline-flex;gap:6px;align-items:center"><svg width="12" height="12" aria-hidden="true"><circle cx="6" cy="6" r="4" fill="${c}"/></svg>${esc(r)}</span>`;
        }).join('');
        const favCount = points.filter(p => p.fav).length;
        if (favCount) {
          lg += `<span style="display:inline-flex;gap:6px;align-items:center"><svg width="14" height="14" aria-hidden="true"><circle cx="7" cy="7" r="5" fill="none" stroke="var(--text)" stroke-width="1.75"/></svg>Favorite (${favCount})</span>`;
        }
        legendEl.innerHTML = lg;
      }

      // One wiring body for both axes — a behaviour change can't miss one
      // of the two listeners (#1760 review).
      const wireAxis = (sel, storageKey) => sel.addEventListener('change', () => {
        try { localStorage.setItem(storageKey, sel.value); } catch (e) { /* ignore */ }
        draw();
      });
      wireAxis(xSel, 'meshcore-repeater-scatter-x');
      wireAxis(ySel, 'meshcore-repeater-scatter-y');
      draw();
    } catch (e) {
      el.innerHTML = `<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load repeater metrics: ${esc(e.message)}</div>`;
    }
  }

  async function renderDistanceTab(el) {
    try {
      const rqs = RegionFilter.regionQueryString();
      const sep = rqs ? '?' + rqs.slice(1) : '';
      const data = await api('/analytics/distance' + sep, { ttl: CLIENT_TTL.analyticsRF });
      const s = data.summary;
      let html = `<div class="analytics-grid">
        <div class="stat-card"><div class="stat-value">${s.totalHops.toLocaleString()}</div><div class="stat-label">Total Hops Analyzed</div></div>
        <div class="stat-card"><div class="stat-value">${s.totalPaths.toLocaleString()}</div><div class="stat-label">Paths Analyzed</div></div>
        <div class="stat-card"><div class="stat-value">${formatDistance(s.avgDist)}</div><div class="stat-label">Avg Hop Distance</div></div>
        <div class="stat-card"><div class="stat-value">${formatDistance(s.maxDist)}</div><div class="stat-label">Max Hop Distance</div></div>
      </div>`;

      // Category stats
      const cats = data.catStats;
      const distUnitLabel = getDistanceUnit() === 'mi' ? 'mi' : 'km';
      html += `<div class="analytics-section"><h3>Distance by Link Type</h3><table class="data-table"><thead><tr><th scope="col">Type</th><th scope="col">Count</th><th scope="col">Avg (${distUnitLabel})</th><th scope="col">Median (${distUnitLabel})</th><th scope="col">Min (${distUnitLabel})</th><th scope="col">Max (${distUnitLabel})</th></tr></thead><tbody>`;
      for (const [cat, st] of Object.entries(cats)) {
        if (!st.count) continue;
        html += `<tr><td><strong>${esc(cat)}</strong></td><td>${st.count.toLocaleString()}</td><td>${formatDistance(st.avg)}</td><td>${formatDistance(st.median)}</td><td>${formatDistance(st.min)}</td><td>${formatDistance(st.max)}</td></tr>`;
      }
      html += `</tbody></table></div>`;

      // Histogram
      if (data.distHistogram && data.distHistogram.bins) {
        const buckets = data.distHistogram.bins.map(b => b.count);
        const labels = data.distHistogram.bins.map(b => b.x.toFixed(1));
        html += `<div class="analytics-section"><h3>Hop Distance Distribution</h3>${barChart(buckets, labels, statusGreen())}</div>`;
      }

      // Distance over time
      if (data.distOverTime && data.distOverTime.length > 1) {
        html += `<div class="analytics-section"><h3>Average Distance Over Time</h3>${sparkSvg(data.distOverTime.map(d => d.avg), 'var(--accent)', 800, 120)}</div>`;
      }

      // Top hops leaderboard
      html += `<div class="analytics-section"><h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trophy"/></svg> Top 20 Longest Hops</h3><table class="data-table"><thead><tr><th scope="col">#</th><th scope="col">From</th><th scope="col">To</th><th scope="col">Distance (${distUnitLabel})</th><th scope="col">Type</th><th scope="col">Obs</th><th scope="col">Best SNR</th><th scope="col">Median SNR</th><th scope="col">Packet</th><th scope="col"></th></tr></thead><tbody>`;
      const top20 = data.topHops.slice(0, 20);
      top20.forEach((h, i) => {
        const fromLink = h.fromPk ? `<a href="#/nodes/${encodeURIComponent(h.fromPk)}" class="analytics-link">${esc(h.fromName)}</a>` : esc(h.fromName || '?');
        const toLink = h.toPk ? `<a href="#/nodes/${encodeURIComponent(h.toPk)}" class="analytics-link">${esc(h.toName)}</a>` : esc(h.toName || '?');
        const bestSnr = h.bestSnr != null ? Number(h.bestSnr).toFixed(1) + ' dB' : '<span class="text-muted">—</span>';
        const medianSnr = h.medianSnr != null ? Number(h.medianSnr).toFixed(1) + ' dB' : '<span class="text-muted">—</span>';
        const obs = h.obsCount != null ? h.obsCount : 1;
        const pktLink = h.hash ? `<a href="#/packet/${encodeURIComponent(h.hash)}" class="analytics-link mono" style="font-size:0.85em">${esc(h.hash.slice(0, 12))}…</a>` : '—';
        const mapBtn = h.fromPk && h.toPk ? `<button class="btn-icon dist-map-hop" data-from="${esc(h.fromPk)}" data-to="${esc(h.toPk)}" title="View on map" aria-label="View on map"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-trifold"/></svg></button>` : '';
        const tsTitle = h.timestamp ? `Best observation: ${h.timestamp}` : '';
        html += `<tr title="${esc(tsTitle)}"><td>${i+1}</td><td>${fromLink}</td><td>${toLink}</td><td><strong>${formatDistance(h.dist)}</strong></td><td>${esc(h.type)}</td><td>${obs}</td><td>${bestSnr}</td><td>${medianSnr}</td><td>${pktLink}</td><td>${mapBtn}</td></tr>`;
      });
      html += `</tbody></table></div>`;

      // Top paths
      if (data.topPaths.length) {
        html += `<div class="analytics-section"><h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-path"/></svg> Top 10 Longest Multi-Hop Paths</h3><table class="data-table"><thead><tr><th scope="col">#</th><th scope="col">Total Distance (${distUnitLabel})</th><th scope="col">Hops</th><th scope="col">Route</th><th scope="col">Packet</th><th scope="col"></th></tr></thead><tbody>`;
        data.topPaths.slice(0, 10).forEach((p, i) => {
          const route = p.hops.map(h => esc(h.fromName)).concat(esc(p.hops[p.hops.length-1].toName)).join(' → ');
          const pktLink = p.hash ? `<a href="#/packet/${encodeURIComponent(p.hash)}" class="analytics-link mono" style="font-size:0.85em">${esc(p.hash.slice(0, 12))}…</a>` : '—';
          // Collect all unique pubkeys in path order
          const pathPks = [];
          p.hops.forEach(h => { if (h.fromPk && !pathPks.includes(h.fromPk)) pathPks.push(h.fromPk); });
          if (p.hops.length && p.hops[p.hops.length-1].toPk) { const last = p.hops[p.hops.length-1].toPk; if (!pathPks.includes(last)) pathPks.push(last); }
          const mapBtn = pathPks.length >= 2 ? `<button class="btn-icon dist-map-path" data-hops='${JSON.stringify(pathPks)}' title="View on map" aria-label="View on map"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-trifold"/></svg></button>` : '';
          html += `<tr><td>${i+1}</td><td><strong>${formatDistance(p.totalDist)}</strong></td><td>${p.hopCount}</td><td style="font-size:0.9em">${route}</td><td>${pktLink}</td><td>${mapBtn}</td></tr>`;
        });
        html += `</tbody></table></div>`;
      }

      el.innerHTML = html;

      // Wire up map buttons
      el.querySelectorAll('.dist-map-hop').forEach(btn => {
        btn.addEventListener('click', () => {
          sessionStorage.setItem('map-route-hops', JSON.stringify({ hops: [btn.dataset.from, btn.dataset.to] }));
          window.location.hash = '#/map?route=1';
        });
      });
      el.querySelectorAll('.dist-map-path').forEach(btn => {
        btn.addEventListener('click', () => {
          try {
            const hops = JSON.parse(btn.dataset.hops);
            sessionStorage.setItem('map-route-hops', JSON.stringify({ hops }));
            window.location.hash = '#/map?route=1';
          } catch {}
        });
      });
    } catch (e) {
      el.innerHTML = `<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load distance analytics: ${esc(e.message)}</div>`;
    }
  }

function destroy() { _stopRolesRefresh(); _stopScopesRefresh(); _analyticsData = {}; _channelData = null; if (_ngState && _ngState.animId) { cancelAnimationFrame(_ngState.animId); } _ngState = null; if (_themeRefreshHandler) { window.removeEventListener('theme-refresh', _themeRefreshHandler); _themeRefreshHandler = null; } }

  // Expose for testing
  if (typeof window !== 'undefined') {
    window._analyticsDecorateChannels = decorateAnalyticsChannels;
    window._analyticsSortChannels = sortChannels;
    window._analyticsLoadChannelSort = loadChannelSort;
    window._analyticsSaveChannelSort = saveChannelSort;
    window._analyticsChannelTbodyHtml = channelTbodyHtml;
    window._analyticsChannelTheadHtml = channelTheadHtml;
    window._analyticsRfNFColumnChart = rfNFColumnChart;
    window._analyticsRenderMultiByteCapability = renderMultiByteCapability;
    window._analyticsRenderMultiByteAdopters = renderMultiByteAdopters;
    window._analyticsHashStatCardsHtml = hashStatCardsHtml;
    window._analyticsRenderCollisionsFromServer = renderCollisionsFromServer;
  }

  // ─── Neighbor Graph Tab ─────────────────────────────────────────────────────

  let _ngState = null; // neighbor graph state

  async function renderNeighborGraphTab(el) {
    el.innerHTML = `
      <div class="analytics-card" id="ngCard">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-graph"/></svg> Neighbor Graph</h3>
        <div id="ngFilters" class="ng-filters" style="display:flex;gap:12px;flex-wrap:wrap;align-items:center;margin-bottom:12px">
          <label style="font-size:13px">Roles:
            <span id="ngRoleChecks" style="margin-left:4px"></span>
          </label>
          <label style="font-size:13px">Min Score: <input type="range" id="ngMinScore" min="0" max="100" value="70" style="width:100px;vertical-align:middle">
            <span id="ngMinScoreVal">0.70</span>
          </label>
          <label style="font-size:13px">Confidence:
            <select id="ngConfidence" style="font-size:12px;padding:2px 4px">
              <option value="all">Show All</option>
              <option value="high">High Only</option>
              <option value="hide-ambiguous">Hide Ambiguous</option>
            </select>
          </label>
        </div>
        <div id="ngStats" class="stat-row" style="display:flex;gap:16px;flex-wrap:wrap;margin-bottom:12px"></div>
        <div style="position:relative;border:1px solid var(--border);border-radius:6px;overflow:hidden">
          <canvas id="ngCanvas" width="900" height="600" style="width:100%;height:600px;cursor:grab;outline-offset:2px" role="img" aria-label="Neighbor affinity graph visualization — interactive force-directed network topology" tabindex="0"></canvas>
          <div id="ngTooltip" style="position:absolute;display:none;background:var(--bg-secondary);border:1px solid var(--border);border-radius:4px;padding:6px 10px;font-size:12px;pointer-events:none;z-index:10;box-shadow:0 2px 8px rgba(0,0,0,0.2)"></div>
        </div>
        <details id="ngAccessibleList" style="margin-top:12px">
          <summary style="cursor:pointer;font-size:13px;color:var(--text-secondary)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clipboard-text"/></svg> Text-based neighbor list (accessible alternative)</summary>
          <div id="ngTextList" style="font-size:12px;max-height:300px;overflow-y:auto;padding:8px;background:var(--bg-secondary);border-radius:4px;margin-top:4px"></div>
        </details>
      </div>`;

    // Role checkboxes
    // #1715 — swatches use `.role-swatch--{role}` classes that read the
    // per-theme `--role-*` CSS tokens (public/style.css). The previous
    // inline `style="color:${ROLE_COLORS[r]}"` bypassed dark-theme
    // overrides and failed WCAG AA on #1a1a2e. Do NOT reintroduce inline
    // color hex here.
    const roles = ['repeater','companion','room','sensor'];
    const rcEl = document.getElementById('ngRoleChecks');
    roles.forEach(r => {
      rcEl.innerHTML += `<label style="font-size:12px;margin-right:8px"><input type="checkbox" data-role="${r}" checked> <span class="role-swatch role-swatch--${r}">${esc(r)}</span></label>`;
    });
    // Observer checkbox — unchecked by default (observers create hub-and-spoke noise)
    {
      rcEl.innerHTML += `<label style="font-size:12px;margin-right:8px"><input type="checkbox" data-role="observer"> <span class="role-swatch role-swatch--observer">observer</span></label>`;
    }

    // Load data
    const rqs = RegionFilter.regionQueryString();
    const sep = rqs ? '?' + rqs.slice(1) : '';
    let graphData;
    try {
      graphData = await api('/analytics/neighbor-graph' + sep + (sep ? '&' : '?') + 'min_count=1&min_score=0', { ttl: CLIENT_TTL.analyticsRF });
    } catch (e) {
      el.innerHTML = `<div class="analytics-card"><p class="text-muted">Failed to load neighbor graph: ${esc(e.message)}</p></div>`;
      return;
    }

    _ngState = createGraphState(graphData);
    // Render through the filter path so the first paint already respects the
    // default filters (observers unchecked, saved min-score) and the
    // node-count guard is evaluated against the displayed set, not the full
    // fetched graph.
    applyNGFilters();

    // Filter listeners
    // Restore saved min score from localStorage
    var savedScore = localStorage.getItem('ng-min-score');
    if (savedScore !== null) {
      document.getElementById('ngMinScore').value = savedScore;
      document.getElementById('ngMinScoreVal').textContent = (savedScore / 100).toFixed(2);
      applyNGFilters();
    }

    var ngScoreDebounce;
    document.getElementById('ngMinScore').addEventListener('input', function() {
      document.getElementById('ngMinScoreVal').textContent = (this.value / 100).toFixed(2);
      localStorage.setItem('ng-min-score', this.value);
      // Debounce the O(N+E) refilter so dragging the slider doesn't rebuild the
      // node set on every pixel (review of #1758).
      clearTimeout(ngScoreDebounce);
      ngScoreDebounce = setTimeout(applyNGFilters, 50);
    });
    document.getElementById('ngConfidence').addEventListener('change', applyNGFilters);
    rcEl.addEventListener('change', applyNGFilters);
  }

  function createGraphState(data) {
    const nodes = (data.nodes || []).map((n, i) => ({
      ...n,
      x: 450 + (Math.random() - 0.5) * 400,
      y: 300 + (Math.random() - 0.5) * 300,
      vx: 0, vy: 0,
      radius: Math.max(6, Math.min(18, 6 + (n.neighbor_count || 0)))
    }));
    const nodeIdx = {};
    nodes.forEach((n, i) => { nodeIdx[n.pubkey] = i; });
    const edges = (data.edges || []).filter(e => nodeIdx[e.source] !== undefined && nodeIdx[e.target] !== undefined);
    return {
      allNodes: nodes, allEdges: edges,
      nodes, edges, nodeIdx,
      stats: data.stats || {},
      zoom: 1, panX: 0, panY: 0,
      dragging: null, panning: false,
      hoverNode: null,
      lastMouseX: 0, lastMouseY: 0,
      cooling: 1.0, animId: null
    };
  }

  function applyNGFilters() {
    if (!_ngState) return;
    const minScore = parseInt(document.getElementById('ngMinScore').value, 10) / 100;
    const conf = document.getElementById('ngConfidence').value;
    const checkedRoles = new Set();
    document.querySelectorAll('#ngRoleChecks input:checked').forEach(cb => checkedRoles.add(cb.dataset.role));

    // Filter nodes by role
    const visibleNodes = _ngState.allNodes.filter(n => {
      const role = (n.role || 'unknown').toLowerCase();
      return checkedRoles.has(role) || role === 'unknown';
    });
    const visiblePKs = new Set(visibleNodes.map(n => n.pubkey));

    // Filter edges
    _ngState.edges = _ngState.allEdges.filter(e => {
      if (e.score < minScore) return false;
      if (conf === 'high' && (e.ambiguous || e.score < 0.5)) return false;
      if (conf === 'hide-ambiguous' && e.ambiguous) return false;
      return visiblePKs.has(e.source) && visiblePKs.has(e.target);
    });

    // Only include nodes that have at least one visible edge
    const edgeNodes = new Set();
    _ngState.edges.forEach(e => { edgeNodes.add(e.source); edgeNodes.add(e.target); });
    _ngState.nodes = visibleNodes.filter(n => edgeNodes.has(n.pubkey));

    // Rebuild index
    _ngState.nodeIdx = {};
    _ngState.nodes.forEach((n, i) => { _ngState.nodeIdx[n.pubkey] = i; });

    // A full re-anneal (cooling back to 1.0) on every filter change is
    // deliberate: the displayed node set just changed, so the prior layout
    // is stale and we want the simulation to settle the new graph. This is
    // acceptable at the current 1000-node limit; revisit if the limit grows.
    _ngState.cooling = 1.0;
    renderNGStats(_ngState);
    // Re-evaluate the node-count guard against the new filtered set and
    // (re)start or stop the force-simulation loop accordingly.
    startGraphRenderer();
  }

  function renderNGStats(st) {
    const nodes = st.nodes, edges = st.edges;
    const totalScore = edges.reduce((s, e) => s + e.score, 0);
    const avgScore = edges.length ? (totalScore / edges.length) : 0;
    const ambiguous = edges.filter(e => e.ambiguous).length;
    const resolved = edges.length ? ((edges.length - ambiguous) / edges.length * 100) : 0;
    const statsEl = document.getElementById('ngStats');
    if (!statsEl) return;
    statsEl.innerHTML = `
      <div class="stat-card"><div class="stat-value">${nodes.length}</div><div class="stat-label">Nodes</div></div>
      <div class="stat-card"><div class="stat-value">${edges.length}</div><div class="stat-label">Edges</div></div>
      <div class="stat-card"><div class="stat-value">${avgScore.toFixed(2)}</div><div class="stat-label">Avg Score</div></div>
      <div class="stat-card"><div class="stat-value">${resolved.toFixed(0)}%</div><div class="stat-label">Resolved</div></div>
      <div class="stat-card"><div class="stat-value">${ambiguous}</div><div class="stat-label">Ambiguous</div></div>`;

    // Update canvas aria-label with current graph summary
    var canvas = document.getElementById('ngCanvas');
    if (canvas) {
      canvas.setAttribute('aria-label', 'Neighbor affinity graph: ' + nodes.length + ' nodes, ' + edges.length + ' edges, ' + resolved.toFixed(0) + '% resolved. Use arrow keys to pan, +/- to zoom, 0 to reset.');
    }

    // Update accessible text list
    updateNGTextList(st);
  }

  function updateNGTextList(st) {
    var listEl = document.getElementById('ngTextList');
    if (!listEl) return;
    var nodes = st.nodes, edges = st.edges;
    if (nodes.length === 0) {
      listEl.innerHTML = '<p class="text-muted">No nodes to display.</p>';
      return;
    }
    // Build adjacency for text list
    var adj = {};
    edges.forEach(function(e) {
      if (!adj[e.source]) adj[e.source] = [];
      if (!adj[e.target]) adj[e.target] = [];
      adj[e.source].push({ pk: e.target, score: e.score, ambiguous: e.ambiguous });
      adj[e.target].push({ pk: e.source, score: e.score, ambiguous: e.ambiguous });
    });
    var nodeMap = {};
    nodes.forEach(function(n) { nodeMap[n.pubkey] = n; });
    var html = '<table style="width:100%;border-collapse:collapse"><thead><tr><th style="text-align:left;padding:4px;border-bottom:1px solid var(--border)">Node</th><th style="text-align:left;padding:4px;border-bottom:1px solid var(--border)">Role</th><th style="text-align:left;padding:4px;border-bottom:1px solid var(--border)">Neighbors</th></tr></thead><tbody>';
    nodes.slice().sort(function(a, b) { return (a.name || a.pubkey).localeCompare(b.name || b.pubkey); }).forEach(function(n) {
      var neighbors = (adj[n.pubkey] || []).map(function(nb) {
        var peer = nodeMap[nb.pk];
        var name = peer ? (peer.name || nb.pk.slice(0, 8)) : nb.pk.slice(0, 8);
        var conf = nb.ambiguous ? ' ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>' : (nb.score >= 0.5 ? ' ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg>' : ' ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle"/></svg>');
        return esc(name) + conf;
      }).join(', ');
      html += '<tr><td style="padding:4px;border-bottom:1px solid var(--border)">' + esc(n.name || n.pubkey.slice(0, 12)) + '</td><td style="padding:4px;border-bottom:1px solid var(--border)">' + esc(n.role || 'unknown') + '</td><td style="padding:4px;border-bottom:1px solid var(--border)">' + (neighbors || '<em>none</em>') + '</td></tr>';
    });
    html += '</tbody></table>';
    html += '<p style="margin-top:8px;font-size:11px;color:var(--text-secondary)">' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg>' + ' = high confidence (score ≥ 0.5), ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle"/></svg>' + ' = low confidence, ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>' + ' = ambiguous/unresolved</p>';
    listEl.innerHTML = html;
  }

  function bindNGCanvasInteraction(canvas) {
    function canvasToGraph(cx, cy) {
      return { x: (cx - _ngState.panX) / _ngState.zoom, y: (cy - _ngState.panY) / _ngState.zoom };
    }

    function findNode(cx, cy) {
      const gp = canvasToGraph(cx, cy);
      for (let i = _ngState.nodes.length - 1; i >= 0; i--) {
        const n = _ngState.nodes[i];
        const dx = gp.x - n.x, dy = gp.y - n.y;
        if (dx * dx + dy * dy <= n.radius * n.radius) return n;
      }
      return null;
    }

    canvas.addEventListener('mousedown', function(e) {
      const rect = canvas.getBoundingClientRect();
      const cx = e.clientX - rect.left, cy = e.clientY - rect.top;
      const n = findNode(cx, cy);
      if (n) {
        _ngState.dragging = n;
        n._pinned = true;
        canvas.style.cursor = 'grabbing';
      } else {
        _ngState.panning = true;
        canvas.style.cursor = 'grabbing';
      }
      _ngState.lastMouseX = e.clientX;
      _ngState.lastMouseY = e.clientY;
    });

    canvas.addEventListener('mousemove', function(e) {
      const rect = canvas.getBoundingClientRect();
      const cx = e.clientX - rect.left, cy = e.clientY - rect.top;
      if (_ngState.dragging) {
        const dx = (e.clientX - _ngState.lastMouseX) / _ngState.zoom;
        const dy = (e.clientY - _ngState.lastMouseY) / _ngState.zoom;
        _ngState.dragging.x += dx;
        _ngState.dragging.y += dy;
        _ngState.lastMouseX = e.clientX;
        _ngState.lastMouseY = e.clientY;
        _ngState.cooling = Math.max(_ngState.cooling, 0.3);
      } else if (_ngState.panning) {
        _ngState.panX += e.clientX - _ngState.lastMouseX;
        _ngState.panY += e.clientY - _ngState.lastMouseY;
        _ngState.lastMouseX = e.clientX;
        _ngState.lastMouseY = e.clientY;
      } else {
        const n = findNode(cx, cy);
        if (n !== _ngState.hoverNode) {
          _ngState.hoverNode = n;
          canvas.style.cursor = n ? 'pointer' : 'grab';
          const tip = document.getElementById('ngTooltip');
          if (n && tip) {
            tip.style.display = 'block';
            tip.style.left = (cx + 12) + 'px';
            tip.style.top = (cy - 8) + 'px';
            tip.innerHTML = `<strong>${escapeHtml(n.name || n.pubkey.slice(0, 12) + '…')}</strong><br>Role: ${escapeHtml(n.role || 'unknown')}<br>Neighbors: ${n.neighbor_count || 0}`;
          } else if (tip) {
            tip.style.display = 'none';
          }
        } else if (_ngState.hoverNode) {
          const tip = document.getElementById('ngTooltip');
          if (tip) { tip.style.left = (cx + 12) + 'px'; tip.style.top = (cy - 8) + 'px'; }
        }
      }
    });

    canvas.addEventListener('mouseup', function() {
      if (_ngState.dragging) {
        _ngState.dragging._pinned = false;
        _ngState._wasDragging = true;
      }
      _ngState.dragging = null;
      _ngState.panning = false;
      canvas.style.cursor = _ngState.hoverNode ? 'pointer' : 'grab';
    });

    canvas.addEventListener('mouseleave', function() {
      _ngState.dragging = null;
      _ngState.panning = false;
      _ngState._wasDragging = false;
      const tip = document.getElementById('ngTooltip');
      if (tip) tip.style.display = 'none';
      _ngState.hoverNode = null;
    });

    canvas.addEventListener('click', function(e) {
      if (_ngState._wasDragging) { _ngState._wasDragging = false; return; }
      if (_ngState.dragging) return;
      const rect = canvas.getBoundingClientRect();
      const n = findNode(e.clientX - rect.left, e.clientY - rect.top);
      if (n) location.hash = '#/nodes/' + n.pubkey;
    });

    canvas.addEventListener('keydown', function(e) {
      const PAN_STEP = 30, ZOOM_STEP = 1.15;
      switch (e.key) {
        case 'ArrowLeft':  _ngState.panX += PAN_STEP; e.preventDefault(); break;
        case 'ArrowRight': _ngState.panX -= PAN_STEP; e.preventDefault(); break;
        case 'ArrowUp':    _ngState.panY += PAN_STEP; e.preventDefault(); break;
        case 'ArrowDown':  _ngState.panY -= PAN_STEP; e.preventDefault(); break;
        case '+': case '=': _ngState.zoom = Math.min(10, _ngState.zoom * ZOOM_STEP); e.preventDefault(); break;
        case '-': case '_': _ngState.zoom = Math.max(0.1, _ngState.zoom / ZOOM_STEP); e.preventDefault(); break;
        case '0':           _ngState.zoom = 1; _ngState.panX = 0; _ngState.panY = 0; e.preventDefault(); break;
      }
    });

    canvas.addEventListener('wheel', function(e) {
      e.preventDefault();
      const rect = canvas.getBoundingClientRect();
      const cx = e.clientX - rect.left, cy = e.clientY - rect.top;
      const factor = e.deltaY < 0 ? 1.1 : 0.9;
      const newZoom = Math.max(0.1, Math.min(10, _ngState.zoom * factor));
      // Zoom towards mouse position
      _ngState.panX = cx - (cx - _ngState.panX) * (newZoom / _ngState.zoom);
      _ngState.panY = cy - (cy - _ngState.panY) * (newZoom / _ngState.zoom);
      _ngState.zoom = newZoom;
    }, { passive: false });
  }

  function startGraphRenderer() {
    if (!_ngState) return;

    // Re-entrant: the filter path calls this to restart against the new node
    // set, so cancel any loop already running before deciding what to do.
    if (_ngState.animId) {
      cancelAnimationFrame(_ngState.animId);
      _ngState.animId = null;
    }
    // Re-entrancy epoch: a just-cancelled in-flight tick() (or one re-entered
    // from a microtask) must not schedule a second loop. Each run bumps the
    // epoch; tick() captures its own and early-returns once a newer run starts.
    _ngState._runEpoch = (_ngState._runEpoch || 0) + 1;
    var myEpoch = _ngState._runEpoch;

    const canvas = document.getElementById('ngCanvas');
    if (!canvas) return;
    var skipMsg = document.getElementById('ngSkipMsg');

    // Node count guard: skip the force simulation for graphs too large to
    // render usefully. Keyed off the DISPLAYED (filtered) set — _ngState.nodes
    // — NOT the full fetched graph (_ngState.allNodes), so narrowing the
    // filters re-enables rendering. The "skipped" notice has a stable id so
    // it can be toggled on the next call.
    var NODE_LIMIT = 1000;
    if (_ngState.nodes.length > NODE_LIMIT) {
      canvas.style.display = 'none';
      if (!skipMsg) {
        skipMsg = document.createElement('div');
        skipMsg.id = 'ngSkipMsg';
        skipMsg.className = 'analytics-card';
        canvas.parentNode.insertBefore(skipMsg, canvas);
      }
      // Build via textContent (never innerHTML) per the project's XSS rule —
      // these are Numbers today, but a future coercion bug must not inject. Show
      // filtered-of-total so the operator sees the rest were filtered out, not
      // lost (review of #1758).
      var ngTotal = (_ngState.allNodes || _ngState.nodes).length;
      skipMsg.textContent = '';
      var skipP = document.createElement('p');
      skipP.className = 'text-muted';
      skipP.textContent = _ngState.nodes.length + ' of ' + ngTotal + ' nodes match the current filters (limit: ' + NODE_LIMIT + '). Force simulation skipped for performance — tighten the filters to render.';
      skipMsg.appendChild(skipP);
      return;
    }
    if (skipMsg) skipMsg.remove();
    canvas.style.display = '';
    const ctx = canvas.getContext('2d');
    const dpr = window.devicePixelRatio || 1;
    canvas.width = canvas.clientWidth * dpr;
    canvas.height = canvas.clientHeight * dpr;
    ctx.scale(dpr, dpr);
    const W = canvas.clientWidth, H = canvas.clientHeight;

    // Interaction listeners are bound once per tab session (the #ngCanvas
    // element and _ngState persist across filter re-renders); re-attaching on
    // every applyNGFilters() would leak handlers (review of #1758). Binding
    // once also means transient flags like _wasDragging survive re-renders.
    // Hover state lives on _ngState so listeners and the draw loop agree.
    if (!_ngState._listenersBound) {
      bindNGCanvasInteraction(canvas);
      _ngState._listenersBound = true;
    }

    // Cache text color to avoid getComputedStyle every frame
    const _labelColor = cssVar('--text-primary') || '#e0e0e0';

    // Force simulation + render loop
    // Performance: 500 nodes brute-force repulsion: avg ~4ms/frame = 250fps headroom (measured Chrome 120, M1)
    var _perfFrameTimes = [], _perfLastTime = 0;
    function tick() {
      if (_ngState._runEpoch !== myEpoch) { return; } // a newer startGraphRenderer run superseded this loop
      if (!document.getElementById('ngCanvas')) { _ngState.animId = null; return; }
      var now = performance.now();
      if (_perfLastTime) _perfFrameTimes.push(now - _perfLastTime);
      _perfLastTime = now;
      if (_perfFrameTimes.length === 100) {
        var avg = _perfFrameTimes.reduce(function(a, b) { return a + b; }, 0) / 100;
        console.log('[NeighborGraph perf] avg frame time over 100 frames: ' + avg.toFixed(2) + 'ms (' + (1000 / avg).toFixed(0) + ' fps)');
        _perfFrameTimes = [];
      }
      const st = _ngState;
      const nodes = st.nodes, edges = st.edges, idx = st.nodeIdx;

      if (st.cooling > 0.001) {
        // Repulsion (all pairs — use grid for large sets, brute force for small)
        const k = 80; // repulsion constant
        for (let i = 0; i < nodes.length; i++) {
          for (let j = i + 1; j < nodes.length; j++) {
            let dx = nodes[j].x - nodes[i].x;
            let dy = nodes[j].y - nodes[i].y;
            let d2 = dx * dx + dy * dy;
            if (d2 < 1) { dx = Math.random() - 0.5; dy = Math.random() - 0.5; d2 = 1; }
            const f = k * k / d2;
            const fx = dx / Math.sqrt(d2) * f;
            const fy = dy / Math.sqrt(d2) * f;
            nodes[i].vx -= fx; nodes[i].vy -= fy;
            nodes[j].vx += fx; nodes[j].vy += fy;
          }
        }

        // Attraction along edges
        const idealLen = 120;
        for (const e of edges) {
          const si = idx[e.source], ti = idx[e.target];
          if (si === undefined || ti === undefined) continue;
          const a = nodes[si], b = nodes[ti];
          let dx = b.x - a.x, dy = b.y - a.y;
          const d = Math.sqrt(dx * dx + dy * dy) || 1;
          const f = (d - idealLen) * 0.05 * (0.5 + e.score * 0.5);
          const fx = dx / d * f, fy = dy / d * f;
          a.vx += fx; a.vy += fy;
          b.vx -= fx; b.vy -= fy;
        }

        // Center gravity
        for (const n of nodes) {
          n.vx += (W / 2 - n.x) * 0.001;
          n.vy += (H / 2 - n.y) * 0.001;
        }

        // Apply velocities with damping
        const damping = 0.85;
        for (const n of nodes) {
          if (n._pinned) { n.vx = 0; n.vy = 0; continue; }
          n.vx *= damping * st.cooling;
          n.vy *= damping * st.cooling;
          const speed = Math.sqrt(n.vx * n.vx + n.vy * n.vy);
          if (speed > 10) { n.vx *= 10 / speed; n.vy *= 10 / speed; }
          n.x += n.vx;
          n.y += n.vy;
        }
        st.cooling *= 0.995;
      }

      // Render
      ctx.save();
      ctx.clearRect(0, 0, W, H);
      ctx.translate(st.panX, st.panY);
      ctx.scale(st.zoom, st.zoom);

      // Edges
      for (const e of edges) {
        const si = idx[e.source], ti = idx[e.target];
        if (si === undefined || ti === undefined) continue;
        const a = nodes[si], b = nodes[ti];
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
        ctx.strokeStyle = e.ambiguous ? 'rgba(255,200,0,0.4)' : 'rgba(150,150,150,0.35)';
        ctx.lineWidth = Math.max(0.5, e.score * 4);
        if (e.ambiguous) { ctx.setLineDash([4, 4]); } else { ctx.setLineDash([]); }
        ctx.stroke();
        ctx.setLineDash([]);
      }

      // Nodes
      const roleColors = window.ROLE_COLORS || {};
      for (const n of nodes) {
        const color = roleColors[(n.role || '').toLowerCase()] || '#6b7280';
        ctx.beginPath();
        ctx.arc(n.x, n.y, n.radius, 0, Math.PI * 2);
        ctx.fillStyle = color;
        ctx.fill();
        if (n === _ngState.hoverNode) {
          ctx.strokeStyle = '#fff';
          ctx.lineWidth = 2;
          ctx.stroke();
        }
        // Label
        const label = n.name || (n.pubkey ? n.pubkey.slice(0, 8) + '…' : '');
        if (label && st.zoom > 0.4) {
          ctx.fillStyle = _labelColor;
          ctx.font = '10px sans-serif';
          ctx.textAlign = 'center';
          ctx.fillText(label, n.x, n.y + n.radius + 12);
        }
      }

      ctx.restore();
      st.animId = requestAnimationFrame(tick);
    }

    _ngState.animId = requestAnimationFrame(tick);
  }

  // --- Prefix Tool ---
  async function renderPrefixTool(el) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading prefix data…</div>';

    const rq = RegionFilter.regionQueryString() + AreaFilter.areaQueryString();
    const regionLabel = rq ? (new URLSearchParams(rq.slice(1)).get('region') || '') : '';

    let nodesResp, hashSizesResp;
    try {
      [nodesResp, hashSizesResp] = await Promise.all([
        fetchAllNodes('&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList }),
        // #1270: fetch CONFIGURED-hash-size counts so the Network Overview
        // tells the operational story (matching Hash Stats "By Repeaters"),
        // not just a math-only count of unique pubkey slices.
        api('/analytics/hash-sizes' + rq, { ttl: CLIENT_TTL.analyticsRF }).catch(() => null),
      ]);
    } catch (e) {
      el.innerHTML = `<div class="text-muted" role="alert" style="padding:40px">Failed to load: ${esc(e.message)}</div>`;
      return;
    }

    // Deduplicate by public_key, require at least 6 hex chars to build all 3 tiers
    const nodeMap = new Map();
    (nodesResp.nodes || nodesResp).forEach(n => {
      if (n.public_key && n.public_key.length >= 6 && !nodeMap.has(n.public_key)) {
        nodeMap.set(n.public_key, n);
      }
    });
    const allNodes = [...nodeMap.values()];
    // Only repeaters matter for prefix collisions — they relay packets using hash prefixes.
    // Companions, rooms, and sensors don't route, so their prefix collisions are harmless.
    const nodes = allNodes.filter(n => n.role === 'repeater');

    if (nodes.length === 0) {
      el.innerHTML = `<div class="analytics-card"><p class="text-muted">No repeaters in the network yet. Any prefix is available!</p></div>`;
      return;
    }

    // Build 3-tier prefix indexes: prefix (uppercase hex) -> [nodes]
    const idx = { 1: new Map(), 2: new Map(), 3: new Map() };
    nodes.forEach(n => {
      const pk = n.public_key.toUpperCase();
      [1, 2, 3].forEach(b => {
        const p = pk.slice(0, b * 2);
        if (!idx[b].has(p)) idx[b].set(p, []);
        idx[b].get(p).push(n);
      });
    });

    // Network overview stats
    const spaceSizes = { 1: 256, 2: 65536, 3: 16777216 };
    const stats = {};
    [1, 2, 3].forEach(b => {
      stats[b] = {
        usedPrefixes: idx[b].size,
        collidingPrefixes: [...idx[b].values()].filter(arr => arr.length > 1).length,
      };
    });

    // #1270: CONFIGURED-hash-size counts (operational truth) from
    // /api/analytics/hash-sizes — same source the Hash Stats tab uses.
    // distributionByRepeaters keys are stringified ints ("1","2","3").
    // "0" = no adverts observed yet, so the configured size is unknown
    // and that node doesn't count for any tier.
    const distByRepeaters = (hashSizesResp && hashSizesResp.distributionByRepeaters) || {};
    const configuredCount = {
      1: Number(distByRepeaters['1'] || 0),
      2: Number(distByRepeaters['2'] || 0),
      3: Number(distByRepeaters['3'] || 0),
    };
    const totalConfigured = configuredCount[1] + configuredCount[2] + configuredCount[3];

    // Operational collisions per tier: only consider repeaters CONFIGURED
    // for this hash size — same definition the Hash Issues tab uses.
    // n.hash_size is enriched on the /nodes payload from GetNodeHashSizeInfo.
    // #1306: keep the per-tier *node lists* per colliding slice so we can
    // surface WHICH prefixes/nodes collide (not just an aggregate count).
    const opCollisions = { 1: 0, 2: 0, 3: 0 };
    const opIdx = { 1: new Map(), 2: new Map(), 3: new Map() };
    [1, 2, 3].forEach(b => {
      nodes.forEach(n => {
        if (n.hash_size !== b) return;
        const p = n.public_key.toUpperCase().slice(0, b * 2);
        if (!opIdx[b].has(p)) opIdx[b].set(p, []);
        opIdx[b].get(p).push(n);
      });
      opCollisions[b] = [...opIdx[b].values()].filter(arr => arr.length > 1).length;
    });

    // #1306: precompute colliding slices per tier (entries with >1 node) for
    // the "Show N colliding slices →" expandable lists. THEORETICAL = across
    // every repeater's pubkey prefix (math fact). OPERATIONAL = only among
    // repeaters configured for this hash size (matches Hash Issues tab math).
    const collEntries = { theoretical: { 1: [], 2: [], 3: [] }, operational: { 1: [], 2: [], 3: [] } };
    [1, 2, 3].forEach(b => {
      collEntries.theoretical[b] = [...idx[b].entries()]
        .filter(([, arr]) => arr.length > 1)
        .sort((a, b2) => b2[1].length - a[1].length || a[0].localeCompare(b2[0]));
      collEntries.operational[b] = [...opIdx[b].entries()]
        .filter(([, arr]) => arr.length > 1)
        .sort((a, b2) => b2[1].length - a[1].length || a[0].localeCompare(b2[0]));
    });

    // #1306: render a "Prefix · Nodes sharing" table for a list of
    // colliding entries `[ [prefix, [node, ...]], ... ]`.
    function renderCollideTable(entries) {
      if (!entries || !entries.length) {
        return '<div class="text-muted" style="padding:8px;font-size:0.8em">No collisions.</div>';
      }
      const rows = entries.map(([prefix, arr]) => {
        const nodeLinks = arr.map(n => {
          const label = esc(n.name || n.public_key.slice(0, 10));
          return `<a href="#/nodes/${encodeURIComponent(n.public_key)}" style="color:var(--link-color);text-decoration:none">${label}</a>`;
        }).join(', ');
        return `<tr>
          <td style="padding:4px 8px;font-family:var(--mono);font-weight:600;border-bottom:1px solid var(--border);white-space:nowrap">${esc(prefix)}</td>
          <td style="padding:4px 8px;font-size:0.85em;border-bottom:1px solid var(--border)">${nodeLinks} <span class="text-muted" style="font-size:0.85em">(${arr.length})</span></td>
        </tr>`;
      }).join('');
      return `<table style="width:100%;border-collapse:collapse;font-size:0.85em">
        <thead><tr>
          <th scope="col" style="text-align:left;padding:4px 8px;border-bottom:1px solid var(--border);background:var(--bg-secondary,var(--bg))">Prefix</th>
          <th scope="col" style="text-align:left;padding:4px 8px;border-bottom:1px solid var(--border);background:var(--bg-secondary,var(--bg))">Nodes sharing it</th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>`;
    }

    // Recommendation by network size
    const totalNodes = nodes.length;
    let rec, recDetail;
    if (totalNodes < 20) {
      rec = '1-byte'; recDetail = `With only ${totalNodes} repeaters, 1-byte prefixes have low collision risk.`;
    } else if (totalNodes < 500) {
      rec = '2-byte'; recDetail = `With ${totalNodes} repeaters, 2-byte prefixes are recommended to avoid collisions.`;
    } else {
      rec = '2-byte'; recDetail = `With ${totalNodes} repeaters, 2-byte prefixes are strongly recommended.`;
    }

    // URL params for pre-fill / auto-run
    const hashParams = new URLSearchParams((location.hash.split('?')[1] || ''));
    const initPrefix = hashParams.get('prefix') || '';
    const initGenerate = hashParams.get('generate') || '';

    const regionNote = regionLabel
      ? `<p class="text-muted" style="font-size:0.85em;margin:4px 0 0">Showing data for region: <strong>${esc(regionLabel)}</strong>. <a href="#/analytics?tab=prefix-tool" style="color:var(--link-color)">Check all repeaters →</a></p>`
      : '';

    el.innerHTML = `
      <div class="analytics-card" id="ptOverview">
        <div style="display:flex;align-items:center;gap:8px;cursor:pointer;user-select:none" id="ptOverviewToggle">
          <span id="ptOverviewChevron" class="pt-overview-chevron" style="color:var(--text-muted);transition:transform 0.2s;display:inline-flex;align-items:center"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-right"/></svg></span>
          <h3 style="margin:0">Network Overview</h3>
        </div>
        <div id="ptOverviewBody" style="display:none">
          ${regionNote}
          <div style="display:flex;gap:12px;flex-wrap:wrap;margin:12px 0 16px">
            <div class="analytics-stat-card" style="flex:1;min-width:110px">
              <div class="analytics-stat-label">Total repeaters</div>
              <div class="analytics-stat-value">${totalNodes.toLocaleString()}</div>
              <div class="text-muted" style="font-size:0.78em;margin-top:4px">
                ${totalConfigured.toLocaleString()} with known hash size
              </div>
            </div>
            ${[1, 2, 3].map(b => {
              // #1270: PRIMARY = configured-for-this-size repeater count
              // (operational truth, matches Hash Stats "By Repeaters").
              // SECONDARY = math fact (unique pubkey slices). OPERATIONAL
              // address conflicts = colliding slices among configured-for-this-size repeaters only.
              const cfg = configuredCount[b];
              const opC = opCollisions[b];
              const theoC = stats[b].collidingPrefixes;
              const borderVar = opC > 0 ? 'var(--status-red)' : 'var(--border)';
              // #1306: replace bare "collisions" with "address conflicts" to
              // distinguish theoretical/would-collide-if-used from packet-
              // traffic-observed collisions shown on the Hash Issues tab.
              const opLine = opC === 0
                ? `<span style="color:var(--status-green-text)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No address conflicts among configured repeaters</span>`
                : `<span style="color:var(--status-red)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> ${opC} address conflict${opC !== 1 ? 's' : ''} among configured repeaters (would-collide-if-used)</span>`;
              // #1306: expandable WHICH-collides toggles (op + theoretical)
              const opEntries = collEntries.operational[b];
              const theoEntries = collEntries.theoretical[b];
              const opTogId = `ptCollOp${b}`;
              const theoTogId = `ptCollTheo${b}`;
              const opToggle = opC > 0 ? `
                <div style="margin-top:6px">
                  <button type="button" class="pt-collide-toggle-btn" data-pt-collide-toggle="op-${b}" data-target="${opTogId}"
                    aria-expanded="false"
                    style="background:none;border:none;color:var(--link-color);cursor:pointer;font-size:0.82em;padding:0">
                    Show ${opC} colliding slice${opC !== 1 ? 's' : ''} →
                  </button>
                  <div id="${opTogId}" data-pt-collide-panel="op-${b}" style="display:none;margin-top:6px;max-height:400px;overflow-y:auto;border:1px solid var(--border);border-radius:4px;background:var(--bg)">
                    ${renderCollideTable(opEntries)}
                  </div>
                </div>` : '';
              const theoToggle = theoC > 0 ? `
                <div style="margin-top:4px">
                  <button type="button" class="pt-collide-toggle-btn" data-pt-collide-toggle="theo-${b}" data-target="${theoTogId}"
                    aria-expanded="false"
                    style="background:none;border:none;color:var(--link-color);cursor:pointer;font-size:0.78em;padding:0">
                    Show ${theoC} would-collide slice${theoC !== 1 ? 's' : ''} (across all repeaters) →
                  </button>
                  <div id="${theoTogId}" data-pt-collide-panel="theo-${b}" style="display:none;margin-top:6px;max-height:400px;overflow-y:auto;border:1px solid var(--border);border-radius:4px;background:var(--bg)">
                    ${renderCollideTable(theoEntries)}
                  </div>
                </div>` : '';
              return `
            <div class="analytics-stat-card" style="flex:1;min-width:170px;border-color:${borderVar}">
              <div class="analytics-stat-label">${b}-byte prefixes</div>
              <div class="analytics-stat-value" style="font-size:1em"
                   data-pt-configured="${b}" data-value="${cfg}">
                ${cfg.toLocaleString()}
                <span class="text-muted" style="font-size:0.7em"> of ${totalNodes.toLocaleString()} repeaters configured</span>
              </div>
              <div style="font-size:0.82em;margin-top:4px">${opLine}</div>
              ${opToggle}
              <div class="text-muted" style="font-size:0.72em;margin-top:6px;border-top:1px dashed var(--border);padding-top:4px">
                <em>Theoretical:</em> ${stats[b].usedPrefixes.toLocaleString()} unique ${b}-byte slice${stats[b].usedPrefixes !== 1 ? 's' : ''}
                across all repeater pubkeys (of ${spaceSizes[b].toLocaleString()} possible)${theoC > 0 ? ` — ${theoC} would-collide if every repeater used ${b}-byte` : ''}
              </div>
              ${theoToggle}
            </div>`;
            }).join('')}
          </div>
          <div style="background:var(--bg-secondary,var(--bg));border:1px solid var(--accent);border-radius:6px;padding:10px 14px;margin-bottom:12px;font-size:0.85em">
            <strong>ℹ️ Theoretical vs observed:</strong> These are <em>theoretical address conflicts</em> that would occur IF all repeaters used this hash size (would-collide-if-used). For collisions <em>actually observed in packet traffic</em>, see the <a href="#/analytics?tab=collisions" style="color:var(--link-color)">Hash Issues</a> tab.
          </div>
          <div style="background:var(--bg-secondary,var(--bg));border:1px solid var(--border);border-radius:6px;padding:10px 14px;margin-bottom:12px">
            <strong>Recommendation: ${rec} prefixes</strong> — ${recDetail}
            <span class="text-muted" style="font-size:0.8em;display:block;margin-top:4px">Hash size is configured per-node in firmware. Changing requires reflashing.</span>
          </div>
          <div style="background:var(--bg-secondary,var(--bg));border:1px solid var(--border);border-radius:6px;padding:10px 14px;font-size:0.85em">
            <strong>ℹ️ About these numbers:</strong> The primary count is how many repeaters are <em>configured</em> for each hash size (their advertised path hash byte length), matching the
            <a href="#/analytics?tab=hashsizes" style="color:var(--link-color)">Hash Stats</a> tab. Address conflicts (would-collide-if-used) count colliding slices among repeaters configured for the same hash size — same definition the
            <a href="#/analytics?tab=collisions" style="color:var(--link-color)">Hash Issues</a> tab uses, except Hash Issues counts collisions <em>actually observed in packet traffic</em> rather than theoretical. The <em>theoretical</em> line shows the math fact: how many distinct slices appear when every repeater pubkey is truncated to N bytes, regardless of configured hash size.
          </div>
        </div>
      </div>

      <div class="analytics-card" id="ptChecker">
        <h3 style="margin-top:0">Check a Prefix</h3>
        <p class="text-muted" style="margin-top:0;font-size:0.9em">Enter a 1-byte (2 hex chars), 2-byte (4 hex chars), or 3-byte (6 hex chars) prefix — or paste a full public key.</p>
        <div style="display:flex;gap:8px;align-items:flex-start;flex-wrap:wrap">
          <input id="ptPrefixInput" type="text" placeholder="e.g. A3F1" maxlength="64"
            style="font-family:var(--mono);font-size:1em;padding:6px 10px;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:4px;min-width:180px;flex:1"
            value="${esc(initPrefix)}">
          <button id="ptCheckBtn" class="btn-active-accent" style="padding:6px 16px;border:none;border-radius:4px;cursor:pointer;font-size:0.95em">Check</button>
        </div>
        <div id="ptCheckerResults" style="margin-top:14px"></div>
      </div>

      <div class="analytics-card" id="ptGenerator">
        <h3 style="margin-top:0">Generate Available Prefix</h3>
        <p class="text-muted" style="margin-top:0;font-size:0.9em">Find a prefix with zero current collisions.</p>
        <p class="text-muted" style="margin:4px 0 10px;font-size:0.82em">
          <span aria-hidden="true"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-prohibit"/></svg></span>
          <strong>0x00 and 0xFF excluded</strong> as a first byte — the MeshCore firmware keygen routine re-rolls identities whose <code>pub_key[0]</code> is <code>00</code> or <code>FF</code>, so by convention you should not see those prefixes on real nodes (see
          <a href="https://github.com/meshcore-dev/MeshCore/blob/8ede7641/examples/simple_repeater/main.cpp#L83"
             target="_blank" rel="noopener noreferrer" style="color:var(--link-color)">simple_repeater/main.cpp:83</a>).
        </p>
        <div style="display:flex;gap:16px;align-items:center;flex-wrap:wrap;margin-bottom:12px">
          <label style="display:flex;align-items:center;gap:6px;cursor:pointer">
            <input type="radio" name="ptGenSize" value="1" ${initGenerate === '1' ? 'checked' : ''}> 1-byte
          </label>
          <label style="display:flex;align-items:center;gap:6px;cursor:pointer">
            <input type="radio" name="ptGenSize" value="2" ${initGenerate !== '1' && initGenerate !== '3' ? 'checked' : ''}> 2-byte
            <span class="text-muted" style="font-size:0.8em">(recommended)</span>
          </label>
          <label style="display:flex;align-items:center;gap:6px;cursor:pointer">
            <input type="radio" name="ptGenSize" value="3" ${initGenerate === '3' ? 'checked' : ''}> 3-byte
          </label>
          <button id="ptGenBtn" class="btn-active-accent" style="padding:6px 16px;border:none;border-radius:4px;cursor:pointer;font-size:0.95em">Generate</button>
        </div>
        <div id="ptGenResult"></div>
        <div style="margin-top:14px;padding:10px 14px;border:1px solid var(--accent);border-radius:6px;background:var(--bg-secondary,var(--bg));font-size:0.88em">
          <svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-book-open"/></svg> <strong>New to multi-byte prefixes?</strong>
          <a href="https://github.com/meshcore-dev/MeshCore/blob/main/docs/faq.md#39-q-what-is-multi-byte-support--what-do-1-byte-2-byte-3-byte-adverts-and-messages-mean"
            target="_blank" rel="noopener noreferrer" style="color:var(--link-color);margin-left:4px">
            Read the MeshCore FAQ on multi-byte support →
          </a>
        </div>
      </div>`;

    // --- Helpers ---
    function nodeEntry(n) {
      const name = esc(n.name || n.public_key.slice(0, 12));
      const role = n.role ? `<span class="text-muted" style="font-size:0.82em">${esc(n.role)}</span>` : '';
      const hs = n.hash_size ? ` <span class="text-muted" style="font-size:0.78em;opacity:0.7">${n.hash_size}B hash</span>` : '';
      const when = n.last_seen ? ` <span class="text-muted" style="font-size:0.8em">${(typeof formatAbsoluteTimestamp === 'function') ? formatAbsoluteTimestamp(n.last_seen) : new Date(n.last_seen).toLocaleDateString()}</span>` : '';
      return `<div style="padding:3px 0"><a href="#/nodes/${encodeURIComponent(n.public_key)}" class="analytics-link">${name}</a> ${role}${hs}${when}</div>`;
    }

    function severityBadge(count) {
      if (count === 0) return '<span style="color:var(--status-green-text)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> Unique</span>';
      if (count <= 2) return `<span style="color:var(--status-yellow)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> ${count} collision${count !== 1 ? 's' : ''}</span>`;
      return `<span style="color:var(--status-red)"><span style="color:var(--status-red)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span> ${count} collisions</span>`;
    }

    // --- Checker ---
    function doCheck(raw) {
      const resultsEl = document.getElementById('ptCheckerResults');
      if (!resultsEl) return;
      const input = raw.trim().toUpperCase();
      if (!input) { resultsEl.innerHTML = ''; return; }

      if (!/^[0-9A-F]+$/.test(input)) {
        resultsEl.innerHTML = '<p style="color:var(--status-red);margin:0">Invalid input — hex characters only (0-9, A-F).</p>';
        return;
      }
      if (input.length % 2 !== 0 || (input.length !== 2 && input.length !== 4 && input.length !== 6 && input.length < 8)) {
        resultsEl.innerHTML = '<p style="color:var(--status-red);margin:0">Prefix must be 2, 4, or 6 hex characters. For a full public key, use 64 characters.</p>';
        return;
      }

      const isFullKey = input.length >= 8;
      const tiers = isFullKey
        ? [{ b: 1, prefix: input.slice(0, 2) }, { b: 2, prefix: input.slice(0, 4) }, { b: 3, prefix: input.slice(0, 6) }]
        : [{ b: input.length / 2, prefix: input }];

      let html = '';
      // #1473 — Warn when the user pastes a prefix or full pubkey whose
      // first byte is one the MeshCore firmware keygen routine avoids
      // (pub_key[0] in {0x00, 0xFF}). Firmware keygen CONVENTION, not a
      // protocol-level rejection — see simple_repeater/main.cpp:83.
      if (typeof PrefixReserved !== 'undefined' && PrefixReserved &&
          PrefixReserved.isReservedPrefix(input)) {
        html += `<div role="alert" style="margin-bottom:10px;padding:10px 14px;border:1px solid var(--status-yellow);border-radius:6px;background:var(--bg-secondary,var(--bg))">
          <strong style="color:var(--status-yellow)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Firmware avoids this first byte</strong>
          <div class="text-muted" style="font-size:0.85em;margin-top:4px">
            <code class="mono">${input.slice(0,2)}</code> as the first byte of a node pubkey is avoided by the MeshCore firmware keygen convention (the standard repeater re-rolls identities whose <code class="mono">pub_key[0]</code> is <code class="mono">00</code> or <code class="mono">FF</code>). You generally shouldn't see this on real nodes.
          </div>
        </div>`;
      }
      if (isFullKey) {
        const inNetwork = nodes.some(n => n.public_key.toUpperCase() === input);
        html += `<p class="text-muted" style="font-size:0.85em;margin:0 0 10px">Derived prefixes: <code class="mono">${input.slice(0,2)}</code> / <code class="mono">${input.slice(0,4)}</code> / <code class="mono">${input.slice(0,6)}</code>${!inNetwork ? ' — <em>this node is not yet in the network</em>' : ''}</p>`;
      }

      tiers.forEach(({ b, prefix }) => {
        const matches = idx[b].get(prefix) || [];
        const colliders = isFullKey ? matches.filter(n => n.public_key.toUpperCase() !== input) : matches;
        const count = colliders.length;
        html += `
          <div style="margin-bottom:10px;padding:10px 14px;border:1px solid var(--border);border-radius:6px;background:var(--bg-secondary,var(--bg))">
            <div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">
              <code class="mono" style="font-weight:700">${prefix}</code>
              <span class="text-muted" style="font-size:0.82em">${b}-byte</span>
              ${severityBadge(count)}
            </div>
            ${count === 0
              ? '<div class="text-muted" style="font-size:0.85em">No existing nodes use this prefix.</div>'
              : `<div style="font-size:0.85em;max-height:140px;overflow-y:auto">${colliders.map(nodeEntry).join('')}</div>`}
          </div>`;
      });

      resultsEl.innerHTML = html;
    }

    // --- Generator ---
    function doGenerate() {
      const genResultEl = document.getElementById('ptGenResult');
      if (!genResultEl) return;
      const sizeInput = el.querySelector('input[name="ptGenSize"]:checked');
      const b = sizeInput ? parseInt(sizeInput.value) : 2;
      const hexLen = b * 2;
      const totalSpace = spaceSizes[b];
      // #1473 — Reserved prefixes (first byte 0x00 / 0xFF) are dropped from
      // the candidate pool because the MeshCore firmware keygen routine
      // re-rolls identities whose pub_key[0] is 0x00 or 0xFF — a keygen
      // CONVENTION (not a protocol rejection). See firmware
      // examples/simple_repeater/main.cpp:83 (HEAD 8ede7641).
      // Available = space - used - reserved.
      const reservedTotal = (typeof PrefixReserved !== 'undefined' && PrefixReserved)
        ? PrefixReserved.reservedCount(b)
        : 0;
      // Count reserved prefixes that are ALREADY used so we don't subtract them twice.
      let reservedUsed = 0;
      if (typeof PrefixReserved !== 'undefined' && PrefixReserved) {
        for (const p of idx[b].keys()) {
          if (PrefixReserved.isReservedPrefix(p)) reservedUsed++;
        }
      }
      const available = totalSpace - idx[b].size - (reservedTotal - reservedUsed);

      if (available <= 0) {
        const next = b < 3 ? (b + 1) + '-byte' : 'a different size';
        genResultEl.innerHTML = `<p style="color:var(--status-red);margin:0">No collision-free ${b}-byte prefixes available. Try ${next}.</p>`;
        return;
      }

      const isReserved = (p) =>
        (typeof PrefixReserved !== 'undefined' && PrefixReserved)
          ? PrefixReserved.isReservedPrefix(p)
          : false;

      let prefix;
      if (b === 1) {
        // Enumerate all 256 options, skipping used + reserved.
        const free = [];
        for (let i = 0; i < totalSpace; i++) {
          const p = i.toString(16).toUpperCase().padStart(hexLen, '0');
          if (!idx[b].has(p) && !isReserved(p)) free.push(p);
        }
        prefix = free[Math.floor(Math.random() * free.length)];
      } else {
        // Random sampling — with 2K used / 65K space, hit rate >96%.
        let attempts = 0;
        do {
          prefix = Math.floor(Math.random() * totalSpace).toString(16).toUpperCase().padStart(hexLen, '0');
        } while ((idx[b].has(prefix) || isReserved(prefix)) && ++attempts < 500);
        // Fallback to enumeration if sampling kept hitting used/reserved prefixes.
        if (idx[b].has(prefix) || isReserved(prefix)) {
          for (let i = 0; i < totalSpace; i++) {
            const p = i.toString(16).toUpperCase().padStart(hexLen, '0');
            if (!idx[b].has(p) && !isReserved(p)) { prefix = p; break; }
          }
        }
      }

      genResultEl.innerHTML = `
        <div style="padding:12px 16px;border:1px solid var(--status-green);border-radius:6px;background:var(--bg-secondary,var(--bg))">
          <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
            <code class="mono" style="font-size:1.3em;font-weight:700;color:var(--status-green-text)">${prefix}</code>
            <span style="color:var(--status-green-text)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No existing nodes use this prefix</span>
          </div>
          <div class="text-muted" style="font-size:0.85em;margin-top:6px">${available.toLocaleString()} of ${totalSpace.toLocaleString()} ${b}-byte prefixes are available.</div>
          <div style="margin-top:10px;display:flex;gap:8px;flex-wrap:wrap;align-items:center">
            <button id="ptRegenBtn" style="padding:5px 14px;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:4px;cursor:pointer;font-size:0.9em">Try another</button>
            <a href="https://agessaman.github.io/meshcore-web-keygen/?prefix=${prefix}" target="_blank" rel="noopener noreferrer"
              style="padding:5px 14px;background:var(--bg);color:var(--link-color);border:1px solid var(--border);border-radius:4px;text-decoration:none;font-size:0.9em">
              Generate key with this prefix →
            </a>
          </div>
        </div>`;
      document.getElementById('ptRegenBtn').addEventListener('click', doGenerate);
    }

    // --- Wire up ---
    const checkBtn = document.getElementById('ptCheckBtn');
    const prefixInput = document.getElementById('ptPrefixInput');
    const genBtn = document.getElementById('ptGenBtn');

    checkBtn.addEventListener('click', () => doCheck(prefixInput.value));
    prefixInput.addEventListener('keydown', e => { if (e.key === 'Enter') doCheck(prefixInput.value); });
    genBtn.addEventListener('click', doGenerate);

    // Network Overview toggle
    document.getElementById('ptOverviewToggle').addEventListener('click', () => {
      const body = document.getElementById('ptOverviewBody');
      const chevron = document.getElementById('ptOverviewChevron');
      const open = body.style.display === 'none';
      body.style.display = open ? '' : 'none';
      chevron.style.transform = open ? 'rotate(90deg)' : '';
    });

    // #1306: WHICH-collides toggles
    document.querySelectorAll('[data-pt-collide-toggle]').forEach(btn => {
      btn.addEventListener('click', () => {
        const panel = document.getElementById(btn.dataset.target);
        if (!panel) return;
        const open = panel.style.display === 'none' || panel.style.display === '';
        // Toggle: if currently hidden ('none'), open; else close.
        const currentlyHidden = panel.style.display === 'none';
        panel.style.display = currentlyHidden ? 'block' : 'none';
        btn.setAttribute('aria-expanded', String(currentlyHidden));
        // Swap arrow direction in label
        btn.textContent = btn.textContent.replace(currentlyHidden ? '→' : '↓', currentlyHidden ? '↓' : '→');
      });
    });

    // Auto-run from URL params
    if (initPrefix) {
      doCheck(initPrefix);
      setTimeout(() => { document.getElementById('ptChecker')?.scrollIntoView({ behavior: 'smooth', block: 'start' }); }, 150);
    } else if (initGenerate) {
      doGenerate();
      setTimeout(() => { document.getElementById('ptGenerator')?.scrollIntoView({ behavior: 'smooth', block: 'start' }); }, 150);
    }
  }

  // ===================== RF HEALTH =====================

  let _rfHealthState = { range: '24h', selectedObserver: null, customFrom: '', customTo: '' };

  function rfHealthTimeRangeToParams(range, customFrom, customTo) {
    const now = new Date();
    let since, until;
    if (range === 'custom' && customFrom) {
      since = new Date(customFrom).toISOString();
      until = customTo ? new Date(customTo).toISOString() : now.toISOString();
    } else {
      const durations = { '1h': 1, '3h': 3, '6h': 6, '12h': 12, '24h': 24, '3d': 72, '7d': 168, '30d': 720 };
      const hours = durations[range] || 24;
      since = new Date(now.getTime() - hours * 3600000).toISOString();
      until = now.toISOString();
    }
    return { since, until };
  }

  function rfHealthUpdateHash() {
    const params = new URLSearchParams();
    params.set('tab', 'rf-health');
    if (_rfHealthState.range !== '24h') params.set('range', _rfHealthState.range);
    if (_rfHealthState.selectedObserver) params.set('observer', _rfHealthState.selectedObserver);
    if (_rfHealthState.range === 'custom') {
      if (_rfHealthState.customFrom) params.set('from', _rfHealthState.customFrom);
      if (_rfHealthState.customTo) params.set('to', _rfHealthState.customTo);
    }
    history.replaceState(null, '', '#/analytics?' + params.toString());
  }

  async function renderRFHealthTab(el) {
    // Restore state from URL
    const hashParams = new URLSearchParams((location.hash.split('?')[1] || ''));
    if (hashParams.get('range')) _rfHealthState.range = hashParams.get('range');
    if (hashParams.get('observer')) _rfHealthState.selectedObserver = hashParams.get('observer');
    if (hashParams.get('from')) { _rfHealthState.customFrom = hashParams.get('from'); _rfHealthState.range = 'custom'; }
    if (hashParams.get('to')) { _rfHealthState.customTo = hashParams.get('to'); _rfHealthState.range = 'custom'; }

    const ranges = ['1h','3h','6h','12h','24h','3d','7d','30d'];
    const rangeButtons = ranges.map(r =>
      `<button class="rf-range-btn${_rfHealthState.range === r ? ' active' : ''}" data-range="${r}">${r}</button>`
    ).join('');

    el.innerHTML = `
      <div class="rf-health-container">
        <div class="rf-time-selector">
          ${rangeButtons}
          <button class="rf-range-btn${_rfHealthState.range === 'custom' ? ' active' : ''}" data-range="custom">Custom</button>
          <span class="rf-custom-inputs" style="display:${_rfHealthState.range === 'custom' ? 'inline' : 'none'}">
            <input type="datetime-local" class="rf-datetime" id="rfFrom" value="${_rfHealthState.customFrom}">
            <span>→</span>
            <input type="datetime-local" class="rf-datetime" id="rfTo" value="${_rfHealthState.customTo}">
            <button class="rf-range-btn" id="rfCustomApply">Apply</button>
          </span>
        </div>
        <div class="rf-health-split">
          <div id="rfHealthGrid" class="rf-health-grid">
            <div class="text-muted" style="padding:20px">Loading RF metrics…</div>
          </div>
          <div id="rfHealthDetail" class="rf-health-detail rf-panel-empty">
            <span>Select an observer to view details</span>
          </div>
        </div>
      </div>`;

    // Range button handlers
    el.querySelectorAll('.rf-range-btn[data-range]').forEach(btn => {
      btn.addEventListener('click', () => {
        const range = btn.dataset.range;
        _rfHealthState.range = range;
        el.querySelectorAll('.rf-range-btn').forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        const customInputs = el.querySelector('.rf-custom-inputs');
        if (customInputs) customInputs.style.display = range === 'custom' ? 'inline' : 'none';
        if (range !== 'custom') {
          rfHealthUpdateHash();
          loadRFHealthData(el);
        }
      });
    });

    const applyBtn = document.getElementById('rfCustomApply');
    if (applyBtn) {
      applyBtn.addEventListener('click', () => {
        _rfHealthState.customFrom = document.getElementById('rfFrom').value;
        _rfHealthState.customTo = document.getElementById('rfTo').value;
        rfHealthUpdateHash();
        loadRFHealthData(el);
      });
    }

    await loadRFHealthData(el);
  }

  async function loadRFHealthData(el) {
    const grid = document.getElementById('rfHealthGrid');
    const detail = document.getElementById('rfHealthDetail');

    try {
      // Compute window string for summary endpoint
      const windowMap = { '1h':'1h', '3h':'3h', '6h':'6h', '12h':'12h', '24h':'24h', '3d':'3d', '7d':'7d', '30d':'30d' };
      const window = windowMap[_rfHealthState.range] || '24h';
      const summaryData = await api('/observers/metrics/summary?window=' + window + (RegionFilter.regionQueryString() || ''));
      const observers = summaryData.observers || [];

      // Filter to observers with sufficient sparkline data (≥2 non-null noise_floor values)
      const filteredObservers = observers.filter(obs => {
        const nfValues = (obs.sparkline || []).filter(v => v != null);
        return nfValues.length >= 2;
      });

      if (!filteredObservers.length) {
        grid.innerHTML = '<div class="text-muted" style="padding:20px">No RF metrics data available yet. Metrics are collected from observer status messages every ~5 minutes.</div>';
        return;
      }

      // Render small multiples grid
      grid.innerHTML = filteredObservers.map(obs => {
        const nf = obs.current_noise_floor != null ? obs.current_noise_floor.toFixed(1) : '—';
        const avgNf = obs.avg_noise_floor_24h != null ? obs.avg_noise_floor_24h.toFixed(1) : '—';
        const maxNf = obs.max_noise_floor_24h != null ? obs.max_noise_floor_24h.toFixed(1) : '—';
        const batt = obs.battery_mv != null ? (obs.battery_mv / 1000).toFixed(2) + 'V' : '';
        const name = obs.observer_name || obs.observer_id.substring(0, 8);
        const isSelected = _rfHealthState.selectedObserver === obs.observer_id;

        // NF status coloring
        let nfClass = '';
        if (obs.current_noise_floor != null) {
          if (obs.current_noise_floor >= -85) nfClass = 'rf-nf-critical';
          else if (obs.current_noise_floor >= -100) nfClass = 'rf-nf-warning';
        }

        return `<div class="rf-cell${isSelected ? ' rf-cell-selected' : ''}" data-observer="${obs.observer_id}" tabindex="0" role="button" aria-label="Observer ${esc(name)}, noise floor ${nf} dBm">
          <div class="rf-cell-header">
            <span class="rf-cell-name">${esc(name)}</span>
            <span class="rf-cell-nf ${nfClass}">${nf} dBm</span>
            ${batt ? `<span class="rf-cell-batt">${batt}</span>` : ''}
          </div>
          <div class="rf-cell-sparkline" id="rf-spark-${obs.observer_id}"></div>
          <div class="rf-cell-stats">
            <span>avg: ${avgNf}</span>
            <span>max: ${maxNf}</span>
            <span>${obs.sample_count} samples</span>
          </div>
        </div>`;
      }).join('');

      // Click handler for cells
      grid.querySelectorAll('.rf-cell').forEach(cell => {
        cell.addEventListener('click', () => {
          const obsId = cell.dataset.observer;
          grid.querySelectorAll('.rf-cell').forEach(c => c.classList.remove('rf-cell-selected'));
          cell.classList.add('rf-cell-selected');
          _rfHealthState.selectedObserver = obsId;
          rfHealthUpdateHash();
          loadRFHealthDetail(obsId, detail);
        });
        cell.addEventListener('keydown', e => {
          if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); cell.click(); }
        });
      });

      // Render sparklines from summary data (no extra API calls)
      for (const obs of filteredObservers) {
        const nfValues = (obs.sparkline || []).filter(v => v != null);
        const container = document.getElementById(`rf-spark-${obs.observer_id}`);
        if (container && nfValues.length > 1) {
          container.innerHTML = rfNFSparkline(nfValues, 140, 24);
        }
      }

      // Auto-expand selected observer from URL
      if (_rfHealthState.selectedObserver) {
        const selectedCell = grid.querySelector(`[data-observer="${_rfHealthState.selectedObserver}"]`);
        if (selectedCell) {
          selectedCell.classList.add('rf-cell-selected');
          loadRFHealthDetail(_rfHealthState.selectedObserver, detail);
        }
      }
    } catch (e) {
      grid.innerHTML = `<div class="text-muted" style="padding:20px">Failed to load RF health data: ${esc(e.message)}</div>`;
    }
  }

  async function loadRFSparkline(observerId) {
    const { since, until } = rfHealthTimeRangeToParams(_rfHealthState.range, _rfHealthState.customFrom, _rfHealthState.customTo);
    try {
      const data = await api(`/observers/${observerId}/metrics?since=${encodeURIComponent(since)}&until=${encodeURIComponent(until)}`);
      const metrics = data.metrics || [];
      const nfValues = metrics.map(m => m.noise_floor).filter(v => v != null);
      const container = document.getElementById(`rf-spark-${observerId}`);
      if (container && nfValues.length > 1) {
        container.innerHTML = rfNFSparkline(nfValues, 140, 24);
      } else if (container) {
        container.innerHTML = '<span class="text-muted" style="font-size:10px">insufficient data</span>';
      }
    } catch (e) {
      // Non-fatal — sparkline just won't render
    }
  }

  function rfNFSparkline(data, w, h) {
    if (!data.length) return '';
    // For noise floor, invert: more negative = better = lower on chart
    const min = Math.min(...data);
    const max = Math.max(...data);
    const range = max - min || 1;
    const pts = data.map((v, i) => {
      const x = (i / Math.max(data.length - 1, 1)) * w;
      // Higher dBm (worse) = higher on chart
      const y = h - 2 - ((v - min) / range) * (h - 4);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    }).join(' ');

    // Reference lines
    let refs = '';
    if (min <= -100 && max >= -100) {
      const y100 = h - 2 - ((-100 - min) / range) * (h - 4);
      refs += `<line x1="0" y1="${y100.toFixed(1)}" x2="${w}" y2="${y100.toFixed(1)}" stroke="var(--text-muted)" stroke-width="0.5" stroke-dasharray="2"/>`;
    }

    return `<svg viewBox="0 0 ${w} ${h}" style="width:${w}px;height:${h}px" role="img" aria-label="Noise floor sparkline"><title>Noise floor trend</title>${refs}<polyline points="${pts}" fill="none" stroke="var(--accent)" stroke-width="1.5"/></svg>`;
  }

  async function loadRFHealthDetail(observerId, container) {
    container.classList.remove('rf-panel-empty');
    container.innerHTML = '<div class="text-muted" style="padding:10px">Loading detail…</div>';

    const { since, until } = rfHealthTimeRangeToParams(_rfHealthState.range, _rfHealthState.customFrom, _rfHealthState.customTo);
    // Choose resolution based on time range
    let resolution = '5m';
    const rangeMap = { '7d': '1h', '30d': '1h' };
    if (rangeMap[_rfHealthState.range]) resolution = rangeMap[_rfHealthState.range];

    try {
      const data = await api(`/observers/${observerId}/metrics?since=${encodeURIComponent(since)}&until=${encodeURIComponent(until)}&resolution=${resolution}`);
      const metrics = data.metrics || [];
      const reboots = (data.reboots || []).map(r => new Date(r).getTime());
      const name = data.observer_name || observerId.substring(0, 8);

      if (!metrics.length) {
        container.innerHTML = `<div class="text-muted" style="padding:10px">No metrics data for ${esc(name)} in selected time range.</div>`;
        return;
      }

      // Extract data series
      const nfData = metrics.map(m => ({ t: m.timestamp, v: m.noise_floor })).filter(d => d.v != null);
      const txData = metrics.map(m => ({ t: m.timestamp, v: m.tx_airtime_pct })).filter(d => d.v != null);
      const rxData = metrics.map(m => ({ t: m.timestamp, v: m.rx_airtime_pct })).filter(d => d.v != null);
      const errData = metrics.map(m => ({ t: m.timestamp, v: m.recv_error_rate })).filter(d => d.v != null);
      const battData = metrics.map(m => ({ t: m.timestamp, v: m.battery_mv })).filter(d => d.v != null && d.v > 0);

      const hasAirtime = txData.length > 1 || rxData.length > 1;
      const hasErrors = errData.length > 1;
      const hasBattery = battData.length > 1;

      // Current values
      const latest = metrics[metrics.length - 1];
      const nfValues = metrics.map(m => m.noise_floor).filter(v => v != null);
      const avgNf = nfValues.length ? (nfValues.reduce((a,b) => a+b, 0) / nfValues.length).toFixed(1) : '—';
      const minNf = nfValues.length ? Math.min(...nfValues).toFixed(1) : '—';
      const maxNf = nfValues.length ? Math.max(...nfValues).toFixed(1) : '—';
      const curNf = latest.noise_floor != null ? latest.noise_floor.toFixed(1) : '—';
      const curBatt = latest.battery_mv != null && latest.battery_mv > 0 ? (latest.battery_mv / 1000).toFixed(2) + 'V' : '—';
      const curTx = latest.tx_airtime_pct != null ? latest.tx_airtime_pct.toFixed(1) + '%' : '—';
      const curRx = latest.rx_airtime_pct != null ? latest.rx_airtime_pct.toFixed(1) + '%' : '—';
      const curErr = latest.recv_error_rate != null ? latest.recv_error_rate.toFixed(2) + '%' : '—';

      container.innerHTML = `
        <div class="rf-detail-header">
          <h3>${esc(name)}</h3>
          <button class="rf-detail-close" aria-label="Close detail" title="Close"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button>
        </div>
        <div class="rf-detail-charts">
          <div class="rf-detail-chart" id="rfDetailNFChart"></div>
          ${hasAirtime ? '<div class="rf-detail-chart" id="rfDetailAirtimeChart"></div>' : ''}
          ${hasErrors ? '<div class="rf-detail-chart" id="rfDetailErrorChart"></div>' : ''}
          ${hasBattery ? '<div class="rf-detail-chart" id="rfDetailBatteryChart"></div>' : ''}
        </div>
        <div class="rf-detail-summary">
          NF: ${curNf} dBm | avg: ${avgNf} | min: ${minNf} | max: ${maxNf} | TX: ${curTx} | RX: ${curRx} | Err: ${curErr} | Batt: ${curBatt}${reboots.length ? ' | ' + reboots.length + ' reboots' : ''}
        </div>`;

      // Close button
      container.querySelector('.rf-detail-close').addEventListener('click', () => {
        container.classList.add('rf-panel-empty');
        container.innerHTML = '<span>Select an observer to view details</span>';
        _rfHealthState.selectedObserver = null;
        rfHealthUpdateHash();
        document.querySelectorAll('.rf-cell').forEach(c => c.classList.remove('rf-cell-selected'));
      });

      // Compute shared time range across all charts
      const allTimestamps = metrics.map(m => new Date(m.timestamp).getTime());
      const minT = Math.min(...allTimestamps);
      const maxT = Math.max(...allTimestamps);

      // Render noise floor chart
      const nfEl = document.getElementById('rfDetailNFChart');
      if (nfEl && nfData.length > 1) {
        nfEl.innerHTML = rfNFColumnChart(nfData, nfEl.clientWidth || 700, 180, reboots, minT, maxT);
      } else if (nfEl) {
        nfEl.innerHTML = '<span class="text-muted">Not enough noise floor data</span>';
      }

      // Render airtime chart
      if (hasAirtime) {
        const atEl = document.getElementById('rfDetailAirtimeChart');
        if (atEl) {
          atEl.innerHTML = rfAirtimeChart(txData, rxData, atEl.clientWidth || 700, 150, reboots, minT, maxT);
        }
      }

      // Render error rate chart
      if (hasErrors) {
        const errEl = document.getElementById('rfDetailErrorChart');
        if (errEl) {
          errEl.innerHTML = rfErrorRateChart(errData, errEl.clientWidth || 700, 120, reboots, minT, maxT);
        }
      }

      // Render battery chart
      if (hasBattery) {
        const battEl = document.getElementById('rfDetailBatteryChart');
        if (battEl) {
          battEl.innerHTML = rfBatteryChart(battData, battEl.clientWidth || 700, 120, reboots, minT, maxT);
        }
      }
    } catch (e) {
      container.innerHTML = `<div class="text-muted" style="padding:10px">Failed to load detail: ${esc(e.message)}</div>`;
    }
  }

  // Shared helper: render reboot markers as vertical hairlines
  function rfRebootMarkers(reboots, sx, pad, h, w) {
    let svg = '';
    for (const rt of reboots) {
      const x = sx(rt);
      if (x >= pad.left && x <= w - pad.right) {
        svg += `<line x1="${x.toFixed(1)}" y1="${pad.top}" x2="${x.toFixed(1)}" y2="${(h - pad.bottom).toFixed(1)}" stroke="var(--text-muted)" stroke-width="0.5" stroke-dasharray="3,3" opacity="0.6"/>`;
        svg += `<text x="${(x + 2).toFixed(1)}" y="${(pad.top + 8).toFixed(1)}" font-size="7" fill="var(--text-muted)" opacity="0.7">reboot</text>`;
      }
    }
    return svg;
  }

  // Shared helper: render X-axis time labels
  function rfTooltipCircles(data, sx, sy, label, unit, formatV) {
    let svg = '';
    formatV = formatV || (v => v.toFixed(1));
    data.forEach(d => {
      const t = new Date(d.t);
      const x = sx(t.getTime());
      const y = sy(d.v);
      const ts = (typeof formatAbsoluteTimestamp === 'function') ? formatAbsoluteTimestamp(d.t) : t.toISOString().replace('T', ' ').replace(/\.\d+Z/, ' UTC');
      const tip = `${label}: ${formatV(d.v)}${unit}\n${ts}`;
      svg += `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="8" fill="transparent" stroke="none" pointer-events="all"><title>${tip}</title></circle>`;
    });
    return svg;
  }

  function rfXAxisLabels(data, sx, h, pad) {
    let svg = '';
    const xTicks = Math.min(6, data.length);
    for (let i = 0; i < xTicks; i++) {
      const idx = Math.floor(i * (data.length - 1) / Math.max(xTicks - 1, 1));
      const t = new Date(data[idx].t);
      const x = sx(t.getTime());
      const label = (typeof formatChartAxisLabel === 'function') ? formatChartAxisLabel(t, true) : t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
      svg += `<text x="${x.toFixed(1)}" y="${h - 5}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${label}</text>`;
    }
    return svg;
  }

  // Shared: build polyline points string from data, skip nulls (break line)
  // Airtime chart: TX (red/orange) + RX (blue) lines, Y 0-100%
  function rfAirtimeChart(txData, rxData, w, h, reboots, sharedMinT, sharedMaxT) {
    const pad = { top: 20, right: 50, bottom: 30, left: 55 };
    const cw = w - pad.left - pad.right;
    const ch = h - pad.top - pad.bottom;
    const minT = sharedMinT, maxT = sharedMaxT;
    const rangeT = maxT - minT || 1;

    // Auto-scale Y-axis to data range (20% headroom, min 1%)
    let dataMax = 0;
    for (let i = 0; i < txData.length; i++) { if (txData[i].v > dataMax) dataMax = txData[i].v; }
    for (let i = 0; i < rxData.length; i++) { if (rxData[i].v > dataMax) dataMax = rxData[i].v; }
    const yMax = Math.max(dataMax * 1.2, 1);

    const sx = t => pad.left + ((t - minT) / rangeT) * cw;
    const sy = v => pad.top + ch - (v / yMax) * ch;

    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Airtime chart"><title>Airtime %</title>`;

    // Chart title
    svg += `<text x="${pad.left}" y="12" font-size="10" fill="var(--text-muted)" font-weight="600">Airtime %</text>`;

    // Y-axis: 5 ticks from 0 to yMax
    const yTicks = 4;
    for (let i = 0; i <= yTicks; i++) {
      const v = yMax * i / yTicks;
      const y = sy(v);
      svg += `<text x="${pad.left - 4}" y="${(y + 3).toFixed(1)}" text-anchor="end" font-size="9" fill="var(--text-muted)">${v.toFixed(1)}</text>`;
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--border)" stroke-width="0.3"/>`;
    }

    // Reboot markers
    svg += rfRebootMarkers(reboots, sx, pad, h, w);

    // TX line (red/orange)
    if (txData.length > 1) {
      const txPts = txData.map(d => `${sx(new Date(d.t).getTime()).toFixed(1)},${sy(d.v).toFixed(1)}`).join(' ');
      svg += `<polyline points="${txPts}" fill="none" stroke="var(--danger, #e74c3c)" stroke-width="1.5"/>`;
      // Direct label at last point
      const lastTx = txData[txData.length - 1];
      const lx = sx(new Date(lastTx.t).getTime());
      const ly = sy(lastTx.v);
      // Offset label up if RX label would overlap (within 12px)
      const lastRx = rxData.length > 1 ? rxData[rxData.length - 1] : null;
      const rxLy = lastRx ? sy(lastRx.v) : Infinity;
      const txLabelY = (Math.abs(ly - rxLy) < 12) ? ly - 8 : ly + 3;
      svg += `<text x="${(lx + 4).toFixed(1)}" y="${txLabelY.toFixed(1)}" font-size="9" fill="var(--danger, #e74c3c)">TX ${lastTx.v.toFixed(1)}%</text>`;
    }

    // RX line (blue)
    if (rxData.length > 1) {
      const rxPts = rxData.map(d => `${sx(new Date(d.t).getTime()).toFixed(1)},${sy(d.v).toFixed(1)}`).join(' ');
      svg += `<polyline points="${rxPts}" fill="none" stroke="var(--info, #3498db)" stroke-width="1.5"/>`;
      // Direct label at last point
      const lastRx = rxData[rxData.length - 1];
      const lx = sx(new Date(lastRx.t).getTime());
      const ly = sy(lastRx.v);
      // Offset label down if TX label is nearby
      const lastTx = txData.length > 1 ? txData[txData.length - 1] : null;
      const txLy = lastTx ? sy(lastTx.v) : -Infinity;
      const rxLabelY = (Math.abs(ly - txLy) < 12) ? ly + 12 : ly + 3;
      svg += `<text x="${(lx + 4).toFixed(1)}" y="${rxLabelY.toFixed(1)}" font-size="9" fill="var(--info, #3498db)">RX ${lastRx.v.toFixed(1)}%</text>`;
    }

    // X-axis labels
    const allData = txData.length >= rxData.length ? txData : rxData;
    svg += rfXAxisLabels(allData, sx, h, pad);

    // Hover tooltips
    svg += rfTooltipCircles(txData, sx, sy, 'TX', '%');
    svg += rfTooltipCircles(rxData, sx, sy, 'RX', '%');

    svg += '</svg>';
    return svg;
  }

  // Error rate chart: recv_error_rate line
  function rfErrorRateChart(errData, w, h, reboots, sharedMinT, sharedMaxT) {
    const pad = { top: 20, right: 50, bottom: 30, left: 55 };
    const cw = w - pad.left - pad.right;
    const ch = h - pad.top - pad.bottom;
    const minT = sharedMinT, maxT = sharedMaxT;
    const rangeT = maxT - minT || 1;

    const values = errData.map(d => d.v);
    const maxV = Math.max(...values, 1); // at least 1% scale
    const rangeV = maxV || 1;

    const sx = t => pad.left + ((t - minT) / rangeT) * cw;
    const sy = v => pad.top + ch - (v / rangeV) * ch;

    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Error rate chart"><title>Error Rate</title>`;

    // Chart title
    svg += `<text x="${pad.left}" y="12" font-size="10" fill="var(--text-muted)" font-weight="600">Error Rate %</text>`;

    // Y-axis
    const yTicks = 4;
    for (let i = 0; i <= yTicks; i++) {
      const v = (rangeV * i / yTicks);
      const y = sy(v);
      svg += `<text x="${pad.left - 4}" y="${(y + 3).toFixed(1)}" text-anchor="end" font-size="9" fill="var(--text-muted)">${v.toFixed(1)}</text>`;
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--border)" stroke-width="0.3"/>`;
    }

    // Reboot markers
    svg += rfRebootMarkers(reboots, sx, pad, h, w);

    // Error rate line
    const pts = errData.map(d => `${sx(new Date(d.t).getTime()).toFixed(1)},${sy(d.v).toFixed(1)}`).join(' ');
    svg += `<polyline points="${pts}" fill="none" stroke="var(--warning, #f39c12)" stroke-width="1.5"/>`;

    // Direct label at last point
    const last = errData[errData.length - 1];
    const lx = sx(new Date(last.t).getTime());
    const ly = sy(last.v);
    svg += `<text x="${(lx + 4).toFixed(1)}" y="${(ly + 3).toFixed(1)}" font-size="9" fill="var(--warning, #f39c12)">${last.v.toFixed(2)}%</text>`;

    // X-axis labels
    svg += rfXAxisLabels(errData, sx, h, pad);

    // Hover tooltips
    svg += rfTooltipCircles(errData, sx, sy, 'Err', '%', v => v.toFixed(2));

    svg += '</svg>';
    return svg;
  }

  // Battery voltage chart
  function rfBatteryChart(battData, w, h, reboots, sharedMinT, sharedMaxT) {
    const pad = { top: 20, right: 50, bottom: 30, left: 55 };
    const cw = w - pad.left - pad.right;
    const ch = h - pad.top - pad.bottom;
    const minT = sharedMinT, maxT = sharedMaxT;
    const rangeT = maxT - minT || 1;

    const values = battData.map(d => d.v);
    const minV = Math.min(...values);
    const maxV = Math.max(...values);
    const rangeV = maxV - minV || 100; // at least 100mV range

    const sx = t => pad.left + ((t - minT) / rangeT) * cw;
    const sy = v => pad.top + ch - ((v - minV) / rangeV) * ch;

    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Battery voltage chart"><title>Battery</title>`;

    // Chart title
    svg += `<text x="${pad.left}" y="12" font-size="10" fill="var(--text-muted)" font-weight="600">Battery</text>`;

    // Y-axis (in volts)
    const yTicks = 4;
    for (let i = 0; i <= yTicks; i++) {
      const v = minV + (rangeV * i / yTicks);
      const y = sy(v);
      svg += `<text x="${pad.left - 4}" y="${(y + 3).toFixed(1)}" text-anchor="end" font-size="9" fill="var(--text-muted)">${(v/1000).toFixed(2)}V</text>`;
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--border)" stroke-width="0.3"/>`;
    }

    // Low battery reference line at 3.3V
    const lowBattMv = 3300;
    if (lowBattMv >= minV && lowBattMv <= maxV) {
      const y = sy(lowBattMv);
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--warning, #f39c12)" stroke-width="0.5" stroke-dasharray="4,2"/>`;
      svg += `<text x="${w - pad.right + 2}" y="${(y + 3).toFixed(1)}" font-size="8" fill="var(--warning, #f39c12)">3.3V low</text>`;
    }

    // Reboot markers
    svg += rfRebootMarkers(reboots, sx, pad, h, w);

    // Battery line
    const pts = battData.map(d => `${sx(new Date(d.t).getTime()).toFixed(1)},${sy(d.v).toFixed(1)}`).join(' ');
    svg += `<polyline points="${pts}" fill="none" stroke="var(--success, #27ae60)" stroke-width="1.5"/>`;

    // Direct label at last point
    const last = battData[battData.length - 1];
    const lx = sx(new Date(last.t).getTime());
    const ly = sy(last.v);
    svg += `<text x="${(lx + 4).toFixed(1)}" y="${(ly + 3).toFixed(1)}" font-size="9" fill="var(--success, #27ae60)">${(last.v/1000).toFixed(2)}V</text>`;

    // X-axis labels
    svg += rfXAxisLabels(battData, sx, h, pad);

    // Hover tooltips
    svg += rfTooltipCircles(battData, sx, sy, 'Batt', 'V', v => (v/1000).toFixed(2));

    svg += '</svg>';
    return svg;
  }

  /**
   * Noise floor column chart — color-coded bars (green/yellow/red) by threshold.
   * Replaces the old line chart for better discrete-sample readability.
   * Thresholds: green (< -100 dBm), yellow (-100 to -85 dBm), red (≥ -85 dBm).
   */
  function rfNFColumnChart(data, w, h, reboots, sharedMinT, sharedMaxT) {
    if (!data || !data.length) return '<svg viewBox="0 0 1 1"></svg>';
    reboots = reboots || [];
    const pad = { top: 20, right: 40, bottom: 30, left: 55 };
    const cw = w - pad.left - pad.right;
    const ch = h - pad.top - pad.bottom;

    const values = data.map(d => d.v);
    const minT = sharedMinT != null ? sharedMinT : Math.min(...data.map(d => new Date(d.t).getTime()));
    const maxT = sharedMaxT != null ? sharedMaxT : Math.max(...data.map(d => new Date(d.t).getTime()));
    const minV = Math.min(...values);
    const maxV = Math.max(...values);
    // Guard against zero range (single data point or constant values):
    // use a ±5 dBm window so bars are visible and centered in the chart
    const rawRangeV = maxV - minV;
    const rangeV = rawRangeV || 10;
    const adjMinV = rawRangeV ? minV : minV - 5;
    const rangeT = maxT - minT || 1;

    const sx = t => pad.left + ((t - minT) / rangeT) * cw;
    const sy = v => pad.top + ch - ((v - adjMinV) / rangeV) * ch;

    // Column width: proportional to chart width / data points, min 2px, gap of 1px
    const colW = Math.max(2, Math.floor(cw / data.length) - 1);

    const times = data.map(d => new Date(d.t).getTime());

    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Noise floor column chart"><title>Noise floor over time</title>`;

    // Inline style for hover highlighting
    svg += `<style>.nf-bar{transition:opacity 0.05s}.nf-bar:hover{opacity:0.75;stroke:var(--text);stroke-width:1}</style>`;

    // Chart title
    svg += `<text x="${pad.left}" y="12" font-size="10" fill="var(--text-muted)" font-weight="600">Noise Floor dBm</text>`;

    // Y-axis labels + grid lines
    const yTicks = 5;
    for (let i = 0; i <= yTicks; i++) {
      const v = adjMinV + (rangeV * i / yTicks);
      const y = sy(v);
      svg += `<text x="${pad.left - 4}" y="${(y + 3).toFixed(1)}" text-anchor="end" font-size="9" fill="var(--text-muted)">${v.toFixed(0)}</text>`;
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--border)" stroke-width="0.3"/>`;
    }

    // Reboot markers
    svg += rfRebootMarkers(reboots, sx, pad, h, w);

    // X-axis labels
    svg += rfXAxisLabels(data, sx, h, pad);

    // Color-coded columns
    for (let i = 0; i < data.length; i++) {
      const t = times[i];
      const v = data[i].v;
      const x = sx(t) - colW / 2;
      const y = sy(v);
      const barH = pad.top + ch - y;

      // Threshold color: green < -100, yellow -100 to -85, red >= -85
      let color;
      if (v < -100) color = 'var(--success, #22c55e)';
      else if (v < -85) color = 'var(--warning, #eab308)';
      else color = 'var(--danger, #ef4444)';

      const ts = new Date(data[i].t).toISOString().replace('T', ' ').replace(/\.\d+Z/, ' UTC');
      const tip = `NF: ${v.toFixed(1)} dBm\n${ts}`;

      svg += `<rect class="nf-bar" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${colW}" height="${Math.max(0, barH).toFixed(1)}" fill="${color}" rx="0.5"><title>${tip}</title></rect>`;
    }

    // Y-axis label
    svg += `<text x="12" y="${(h / 2)}" text-anchor="middle" font-size="10" fill="var(--text-muted)" transform="rotate(-90,12,${h/2})">dBm</text>`;

    // Legend
    const legendY = pad.top + 2;
    const legendX = w - pad.right - 140;
    svg += `<rect x="${legendX}" y="${legendY}" width="8" height="8" fill="var(--success, #22c55e)" rx="1"/>`;
    svg += `<text x="${legendX + 11}" y="${legendY + 7}" font-size="8" fill="var(--text-muted)">&lt; -100</text>`;
    svg += `<rect x="${legendX + 48}" y="${legendY}" width="8" height="8" fill="var(--warning, #eab308)" rx="1"/>`;
    svg += `<text x="${legendX + 59}" y="${legendY + 7}" font-size="8" fill="var(--text-muted)">-100…-85</text>`;
    svg += `<rect x="${legendX + 105}" y="${legendY}" width="8" height="8" fill="var(--danger, #ef4444)" rx="1"/>`;
    svg += `<text x="${legendX + 116}" y="${legendY + 7}" font-size="8" fill="var(--text-muted)">≥ -85</text>`;

    svg += '</svg>';
    return svg;
  }

  // #690 — Clock Health fleet view (M3)
  async function renderClockHealthTab(el) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading clock health data…</div>';
    try {
      const aqs = AreaFilter.areaQueryString();
      var data = await (await fetch('/api/nodes/clock-skew' + (aqs ? '?' + aqs.slice(1) : ''))).json();
      if (!Array.isArray(data) || !data.length) {
        el.innerHTML = '<div class="text-center text-muted" style="padding:40px">No clock skew data available. Nodes need recent adverts for clock analysis.</div>';
        return;
      }

      // State
      var activeFilter = 'all';
      var sortKey = 'severity';
      var sortDir = 'asc'; // severity worst-first

      function render() {
        // Filter
        var filtered = activeFilter === 'all' ? data : data.filter(function(n) { return n.severity === activeFilter; });

        // Sort
        filtered = filtered.slice().sort(function(a, b) {
          var v;
          if (sortKey === 'severity') {
            v = (SKEW_SEVERITY_ORDER[a.severity] || 9) - (SKEW_SEVERITY_ORDER[b.severity] || 9);
          } else if (sortKey === 'skew') {
            v = Math.abs(window.currentSkewValue(b) || 0) - Math.abs(window.currentSkewValue(a) || 0);
          } else if (sortKey === 'name') {
            v = (a.nodeName || '').localeCompare(b.nodeName || '');
          } else if (sortKey === 'drift') {
            v = Math.abs(b.driftPerDaySec || 0) - Math.abs(a.driftPerDaySec || 0);
          }
          return sortDir === 'desc' ? -v : v;
        });

        // Summary
        var counts = { ok: 0, warning: 0, critical: 0, absurd: 0 };
        data.forEach(function(n) { if (counts[n.severity] !== undefined) counts[n.severity]++; });

        // Filter buttons (also serve as summary — no separate stats pills needed)
        var filterColors = { ok: 'var(--status-green)', warning: 'var(--status-yellow)', critical: 'var(--status-orange)', absurd: 'var(--status-purple)', no_clock: 'var(--text-muted)' };
        var filters = ['all', 'ok', 'warning', 'critical', 'absurd', 'no_clock'];
        var filterHtml = '<div style="margin-bottom:10px">' + filters.map(function(f) {
          var dot = f !== 'all' ? '<span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:' + filterColors[f] + ';margin-right:4px;vertical-align:middle"></span>' : '';
          return '<button class="clock-filter-btn' + (activeFilter === f ? ' active' : '') + '" data-filter="' + f + '">' +
            dot + (f === 'all' ? 'All (' + data.length + ')' : (SKEW_SEVERITY_LABELS[f] || f) + ' (' + (counts[f] || 0) + ')') +
            '</button>';
        }).join('') + '</div>';

        // Table
        var rowsHtml = filtered.map(function(n) {
          var rowClass = 'clock-fleet-row--' + (n.severity || 'ok');
          var lastAdv = n.lastObservedTS ? new Date(n.lastObservedTS * 1000).toISOString().replace('T', ' ').replace(/\.\d+Z/, ' UTC') : '—';
          var skewVal = window.currentSkewValue(n);
          var skewText = n.severity === 'no_clock' ? 'No Clock' : formatSkew(skewVal);
          var driftText = n.severity === 'no_clock' || !n.driftPerDaySec ? '–' : formatDrift(n.driftPerDaySec);
          return '<tr class="' + rowClass + '" data-pubkey="' + esc(n.pubkey) + '" style="cursor:pointer">' +
            '<td><strong>' + esc(n.nodeName || n.pubkey.slice(0, 12)) + '</strong></td>' +
            '<td style="font-family:var(--mono,monospace)">' + skewText + '</td>' +
            '<td>' + renderSkewBadge(n.severity, skewVal, n) + '</td>' +
            '<td style="font-family:var(--mono,monospace)">' + driftText + '</td>' +
            '<td style="font-size:11px">' + lastAdv + '</td>' +
            '</tr>';
        }).join('');

        el.innerHTML = '<h3 style="margin:0 0 10px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clock"/></svg> Clock Health</h3>' +
          filterHtml +
          '<table class="data-table analytics-table" id="clock-health-table">' +
          '<thead><tr>' +
          '<th data-sort-col="name" style="cursor:pointer">Name</th>' +
          '<th data-sort-col="skew" style="cursor:pointer">Skew</th>' +
          '<th data-sort-col="severity" style="cursor:pointer">Severity</th>' +
          '<th data-sort-col="drift" style="cursor:pointer">Drift Rate</th>' +
          '<th>Last Advert</th>' +
          '</tr></thead><tbody>' + rowsHtml + '</tbody></table>';

        // Bind filter clicks
        el.querySelectorAll('.clock-filter-btn').forEach(function(btn) {
          btn.addEventListener('click', function() {
            activeFilter = btn.dataset.filter;
            render();
          });
        });

        // Bind header sort clicks
        el.querySelectorAll('[data-sort-col]').forEach(function(th) {
          th.addEventListener('click', function() {
            var col = th.dataset.sortCol;
            if (sortKey === col) { sortDir = sortDir === 'asc' ? 'desc' : 'asc'; }
            else { sortKey = col; sortDir = 'asc'; }
            render();
          });
        });

        // Bind row clicks → navigate to node
        el.querySelectorAll('tr[data-pubkey]').forEach(function(tr) {
          tr.addEventListener('click', function() {
            location.hash = '#/nodes/' + encodeURIComponent(tr.dataset.pubkey);
          });
        });
      }

      render();
    } catch (err) {
      el.innerHTML = '<div class="text-center" style="color:var(--status-red);padding:40px">Failed to load clock health data: ' + esc(String(err)) + '</div>';
    }
  }

  // ===================== SCOPES =====================
  async function renderScopesTab(el) {
    var winKey = 'scopes_window';
    var selectedWindow = (typeof sessionStorage !== 'undefined' && sessionStorage.getItem(winKey)) || '24h';

    // Fix 5: write static frame only once
    if (!el.querySelector('#scopes-cards')) {
      el.innerHTML =
        '<h3 style="margin:0 0 4px">Scope Statistics</h3>' +
        '<p class="text-muted" style="margin:0 0 12px;font-size:0.85em">' +
          'Denominator is all observed transmissions. Only TRANSPORT_FLOOD (0) and TRANSPORT_DIRECT (3) routes carry a scope; FLOOD (1) and DIRECT (2) are inherently unscoped per MeshCore protocol.' +
        '</p>' +
        '<div style="margin-bottom:12px">' +
          ['1h', '24h', '7d'].map(function(v) {
            return '<button class="tab-btn' + (selectedWindow === v ? ' active' : '') + '" data-win="' + v + '">' + v + '</button>';
          }).join('') +
        '</div>' +
        '<div id="scopes-cards" class="stats-grid" style="margin-bottom:16px"></div>' +
        '<div class="text-center text-muted" id="scopes-loading" style="padding:20px">Loading scope stats…</div>' +
        '<table class="data-table analytics-table" style="margin-bottom:8px">' +
          '<thead><tr><th>Region</th><th>Messages</th><th>% of Scoped</th></tr></thead>' +
          '<tbody id="scopes-tbody"></tbody>' +
        '</table>' +
        '<div id="scopes-chart"></div>';

      // Attach window-button click listeners (once)
      el.querySelectorAll('[data-win]').forEach(function(btn) {
        btn.addEventListener('click', function() {
          selectedWindow = btn.dataset.win;
          if (typeof sessionStorage !== 'undefined') sessionStorage.setItem(winKey, selectedWindow);
          el.querySelectorAll('[data-win]').forEach(function(b) { b.classList.toggle('active', b.dataset.win === selectedWindow); });
          load(selectedWindow);
        });
      });
    }

    function pct(n, total) {
      if (!total) return '—';
      return (n / total * 100).toFixed(1) + '%';
    }

    async function load(w) {
      var loadingEl = document.getElementById('scopes-loading');
      if (loadingEl) loadingEl.style.display = '';
      try {
        // Fix 4: use api() instead of raw fetch()
        var data = await api('/scope-stats?window=' + encodeURIComponent(w), { ttl: 30000 });
        if (loadingEl) loadingEl.style.display = 'none';
        if (data.error) {
          var cardsEl2 = document.getElementById('scopes-cards');
          if (cardsEl2) cardsEl2.innerHTML = '<div class="text-center text-muted" style="padding:20px">' + esc(data.error) + '</div>';
          return;
        }
        updateData(data, w);
      } catch (err) {
        if (loadingEl) loadingEl.style.display = 'none';
        var cardsEl3 = document.getElementById('scopes-cards');
        if (cardsEl3) cardsEl3.innerHTML = '<div class="text-center" style="color:var(--status-red);padding:20px">Failed to load scope stats: ' + esc(String(err)) + '</div>';
      }
    }

    function updateData(d, w) {
      var s = d.summary;
      // #1838: denominator = transport-carrying transmissions (route_type 0,3).
      // Unscoped now includes non-transport routes (1,2) which are inherently
      // unscoped by MeshCore protocol, so unscoped can exceed transportTotal.
      var total = s.transportTotal || 0;
      var overall = (s.scoped || 0) + (s.unscoped || 0);

      // Summary cards
      var cardsEl = document.getElementById('scopes-cards');
      if (cardsEl) {
        cardsEl.innerHTML = [
          { label: 'Transport Total', value: total.toLocaleString(), note: 'routes 0,3 (carry scope)' },
          { label: 'Scoped', value: s.scoped.toLocaleString(), note: pct(s.scoped, overall) + ' of all traffic' },
          { label: 'Unscoped', value: s.unscoped.toLocaleString(), note: pct(s.unscoped, overall) + ' of all traffic' },
          { label: 'Unknown Scope', value: s.unknownScope.toLocaleString(), note: pct(s.unknownScope, s.scoped) + ' of scoped' },
        ].map(function(c) {
          return '<div class="stat-card"><div class="stat-value">' + c.value + '</div>' +
            '<div class="stat-label">' + c.label + '</div>' +
            (c.note ? '<div class="stat-note text-muted" style="font-size:11px">' + c.note + '</div>' : '') +
            '</div>';
        }).join('');
      }

      // Per-region table
      var tbodyEl = document.getElementById('scopes-tbody');
      if (tbodyEl) {
        var tableBody = '';
        if (d.byRegion && d.byRegion.length) {
          tableBody = d.byRegion.map(function(r) {
            return '<tr><td><code>' + esc(r.name) + '</code></td>' +
              '<td>' + r.count.toLocaleString() + '</td>' +
              '<td>' + pct(r.count, s.scoped) + '</td></tr>';
          }).join('');
          if (s.unknownScope > 0) {
            tableBody += '<tr><td><em class="text-muted">Unknown scope</em></td>' +
              '<td>' + s.unknownScope.toLocaleString() + '</td>' +
              '<td>' + pct(s.unknownScope, s.scoped) + '</td></tr>';
          }
        } else if (s.scoped === 0) {
          tableBody = '<tr><td colspan="3" class="text-muted" style="text-align:center">No scoped messages in this window</td></tr>';
        } else {
          tableBody = '<tr><td colspan="3" class="text-muted" style="text-align:center">No regions configured — add <code>hashRegions</code> to your config</td></tr>';
        }
        tbodyEl.innerHTML = tableBody;
      }

      // Time-series chart (two-line SVG)
      var chartEl = document.getElementById('scopes-chart');
      if (chartEl) {
        var chartHtml = '';
        if (d.timeSeries && d.timeSeries.length > 1) {
          var scopedVals = d.timeSeries.map(function(p) { return p.scoped; });
          var unscopedVals = d.timeSeries.map(function(p) { return p.unscoped; });
          var maxVal = Math.max(1, Math.max.apply(null, scopedVals.concat(unscopedVals)));
          var W = 800, H = 180, padL = 44, padT = 10, padR = 10;
          var plotW = W - padL - padR, plotH = H - 24 - padT;
          var n = d.timeSeries.length;

          function pts(vals) {
            return vals.map(function(v, i) {
              var x = padL + i * plotW / Math.max(n - 1, 1);
              var y = padT + plotH - (v / maxVal) * plotH;
              return x.toFixed(1) + ',' + y.toFixed(1);
            }).join(' ');
          }

          var grid = '';
          for (var gi = 0; gi <= 4; gi++) {
            var gy = padT + plotH * gi / 4;
            var gv = Math.round(maxVal * (4 - gi) / 4);
            grid += '<line x1="' + padL + '" y1="' + gy.toFixed(1) + '" x2="' + (W - padR) + '" y2="' + gy.toFixed(1) + '" stroke="var(--border)" stroke-dasharray="2"/>';
            grid += '<text x="' + (padL - 4) + '" y="' + (gy + 4).toFixed(1) + '" text-anchor="end" font-size="9" fill="var(--text-muted)">' + gv + '</text>';
          }

          var legendX = padL + plotW - 120;
          chartHtml = '<div style="margin-top:16px">' +
            '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;max-height:' + H + 'px" role="img" aria-label="Scope time series">' +
            grid +
            '<polyline points="' + pts(scopedVals) + '" fill="none" stroke="var(--accent)" stroke-width="2"/>' +
            '<polyline points="' + pts(unscopedVals) + '" fill="none" stroke="var(--text-muted)" stroke-width="1.5" stroke-dasharray="4"/>' +
            '<rect x="' + legendX + '" y="' + padT + '" width="10" height="10" fill="var(--accent)"/>' +
            '<text x="' + (legendX + 14) + '" y="' + (padT + 9) + '" font-size="10" fill="var(--text)">Scoped</text>' +
            '<rect x="' + legendX + '" y="' + (padT + 16) + '" width="10" height="10" fill="var(--text-muted)"/>' +
            '<text x="' + (legendX + 14) + '" y="' + (padT + 25) + '" font-size="10" fill="var(--text)">Unscoped</text>' +
            '</svg></div>';
        } else {
          chartHtml = '<p class="text-muted" style="font-size:0.85em;margin:12px 0 0">Insufficient data points to render chart — wait for more observations in this window.</p>';
        }
        chartEl.innerHTML = chartHtml;
      }
    }

    load(selectedWindow);

    // Fix 6: auto-refresh every 60s
    _stopScopesRefresh();
    _scopesRefreshTimer = setInterval(function() {
      if (_currentTab !== 'scopes') { _stopScopesRefresh(); return; }
      var cur = document.getElementById('analyticsContent');
      if (!cur) { _stopScopesRefresh(); return; }
      load(selectedWindow);
    }, 60000);
  }

  // #1085 — Roles tab (folded in from former /#/roles page).
  // Renders distribution of node roles + per-role clock-skew posture.
  // Auto-refreshes every 60s while the Roles tab is active (matches the
  // behavior of the former standalone roles-page.js).
  async function renderRolesTab(el) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading roles…</div>';
    await _renderRolesTabBody(el);
    // (Re)start the 60s auto-refresh.
    _stopRolesRefresh();
    _rolesRefreshTimer = setInterval(function () {
      // Bail if the user navigated away from the Roles tab.
      if (_currentTab !== 'roles') { _stopRolesRefresh(); return; }
      var cur = document.getElementById('analyticsContent');
      if (!cur) { _stopRolesRefresh(); return; }
      _renderRolesTabBody(cur);
    }, 60000);
  }

  async function _renderRolesTabBody(el) {
    try {
      var data = await api('/analytics/roles', { ttl: CLIENT_TTL.analyticsRF });
      var roles = (data && data.roles) || [];
      var total = (data && data.totalNodes) || 0;
      if (!roles.length) {
        el.innerHTML = '<div class="text-center text-muted" style="padding:40px">No roles to show.</div>';
        return;
      }
      var maxCount = roles.reduce(function (m, r) { return Math.max(m, r.nodeCount || 0); }, 0) || 1;
      var rows = roles.map(function (r) {
        var pct = total > 0 ? ((r.nodeCount / total) * 100).toFixed(1) : '0.0';
        var barW = Math.round((r.nodeCount / maxCount) * 100);
        var sevCells =
          '<span title="OK (skew &lt; 5min)" style="color:var(--color-success,#0a0)">' + (r.okCount || 0) + '</span> / ' +
          '<span title="Warning (5min – 1h)" style="color:var(--color-warning,#e80)">' + (r.warningCount || 0) + '</span> / ' +
          '<span title="Critical (1h – 30d)" style="color:var(--color-error,#c00)">' + (r.criticalCount || 0) + '</span> / ' +
          '<span title="Absurd (&gt; 30d)" style="color:#a0a">' + (r.absurdCount || 0) + '</span> / ' +
          '<span title="No clock (&gt; 365d)" style="color:var(--text-muted)">' + (r.noClockCount || 0) + '</span>';
        return '' +
          '<tr data-role="' + esc(r.role) + '">' +
            '<td>' + _rolesEmoji(r.role) + ' <strong>' + esc(r.role) + '</strong></td>' +
            '<td style="text-align:right">' + r.nodeCount + '</td>' +
            '<td style="text-align:right">' + pct + '%</td>' +
            '<td style="min-width:140px">' +
              '<div style="background:var(--color-surface-2,#eee);height:10px;border-radius:5px;overflow:hidden">' +
                '<div style="background:var(--color-accent,#06c);width:' + barW + '%;height:100%"></div>' +
              '</div>' +
            '</td>' +
            '<td style="text-align:right">' + (r.withSkew || 0) + '</td>' +
            '<td style="text-align:right">' + _rolesFmtSec(r.medianAbsSkewSec || 0) + '</td>' +
            '<td style="text-align:right">' + _rolesFmtSec(r.meanAbsSkewSec || 0) + '</td>' +
            '<td style="white-space:nowrap">' + sevCells + '</td>' +
          '</tr>';
      }).join('');
      el.innerHTML =
        '<p class="text-muted" style="margin:0 0 12px 0">Distribution of node roles across the mesh, with per-role clock-skew posture.</p>' +
        '<div class="roles-summary" style="margin-bottom:12px;color:var(--text-muted)">' +
          '<strong>' + total + '</strong> nodes across <strong>' + roles.length + '</strong> roles' +
        '</div>' +
        '<table id="rolesTable" class="data-table analytics-table" style="width:100%">' +
          '<thead><tr>' +
            '<th>Role</th>' +
            '<th style="text-align:right">Count</th>' +
            '<th style="text-align:right">Share</th>' +
            '<th>Distribution</th>' +
            '<th style="text-align:right" title="Nodes with clock-skew samples">w/ Skew</th>' +
            '<th style="text-align:right" title="Median absolute skew">Median |skew|</th>' +
            '<th style="text-align:right" title="Mean absolute skew">Mean |skew|</th>' +
            '<th title="OK / Warning / Critical / Absurd / No-clock">Severity</th>' +
          '</tr></thead>' +
          '<tbody>' + rows + '</tbody>' +
        '</table>';
    } catch (err) {
      el.innerHTML = '<div class="text-center" style="color:var(--status-red);padding:40px">Failed to load roles: ' + esc(String(err.message || err)) + '</div>';
    }
  }

  registerPage('analytics', { init, destroy });
})();
