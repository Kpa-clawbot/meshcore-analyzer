/* === CoreScope — analytics.js (v2 — full nerd mode) === */
'use strict';

(function () {
  let _analyticsData = {};
  const sf = (v, d) => (v != null ? v.toFixed(d) : '–'); // safe toFixed
  function esc(s) { return s ? String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;') : ''; }

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
    const barW = Math.min((w - pad * 2) / data.length - 2, 30);
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
          <h2>📊 Mesh Analytics</h2>
          <p class="text-muted">Deep dive into your mesh network data</p>
          <div id="analyticsRegionFilter" class="region-filter-container"></div>
          <div class="analytics-tabs" id="analyticsTabs" role="tablist" aria-label="Analytics tabs">
            <button class="tab-btn active" data-tab="overview">Overview</button>
            <button class="tab-btn" data-tab="rf">RF / Signal</button>
            <button class="tab-btn" data-tab="topology">Topology</button>
            <button class="tab-btn" data-tab="channels">Channels</button>
            <button class="tab-btn" data-tab="hashsizes">Hash Stats</button>
            <button class="tab-btn" data-tab="collisions">Hash Issues</button>
            <button class="tab-btn" data-tab="subpaths">Route Patterns</button>
            <button class="tab-btn" data-tab="nodes">Nodes</button>
            <button class="tab-btn" data-tab="distance">Distance</button>
            <button class="tab-btn" data-tab="neighbor-graph">Neighbor Graph</button>
            <button class="tab-btn" data-tab="rf-health">RF Health</button>
            <button class="tab-btn" data-tab="clock-health">Clock Health</button>
            <button class="tab-btn" data-tab="prefix-tool">Prefix Tool</button>
          </div>
        </div>
        <div id="analyticsContent" class="analytics-content" aria-live="polite">
          <div class="text-center text-muted" style="padding:40px">Loading analytics…</div>
        </div>
      </div>`;

    // Tab handling
    const analyticsTabs = document.getElementById('analyticsTabs');
    initTabBar(analyticsTabs);
    analyticsTabs.addEventListener('click', e => {
      const btn = e.target.closest('.tab-btn');
      if (!btn) return;
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      _currentTab = btn.dataset.tab;
      renderTab(_currentTab);
    });

    // Deep-link: #/analytics?tab=collisions
    const hashParams = location.hash.split('?')[1] || '';
    const urlTab = new URLSearchParams(hashParams).get('tab');
    if (urlTab) {
      const tabBtn = analyticsTabs.querySelector(`[data-tab="${urlTab}"]`);
      if (tabBtn) {
        analyticsTabs.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
        tabBtn.classList.add('active');
        _currentTab = urlTab;
      }
    }

    RegionFilter.init(document.getElementById('analyticsRegionFilter'));
    RegionFilter.onChange(function () { loadAnalytics(); });

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
    _themeRefreshHandler = function () { renderTab(_currentTab); };
    window.addEventListener('theme-refresh', _themeRefreshHandler);

    loadAnalytics();
  }

  var _themeRefreshHandler = null;
  let _currentTab = 'overview';

  async function loadAnalytics() {
    try {
      _analyticsData = {};
      const rqs = RegionFilter.regionQueryString();
      const sep = rqs ? '?' + rqs.slice(1) : '';
      const [hashData, rfData, topoData, chanData, collisionData] = await Promise.all([
        api('/analytics/hash-sizes' + sep, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/rf' + sep, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/topology' + sep, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/channels' + sep, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/hash-collisions' + sep, { ttl: CLIENT_TTL.analyticsRF }),
      ]);
      _analyticsData = { hashData, rfData, topoData, chanData, collisionData };
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
      case 'distance': await renderDistanceTab(el); break;
      case 'neighbor-graph': await renderNeighborGraphTab(el); break;
      case 'rf-health': await renderRFHealthTab(el); break;
      case 'clock-health': await renderClockHealthTab(el); break;
      case 'prefix-tool': await renderPrefixTool(el); break;
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
          <h3>📈 Packets / Hour</h3>
          ${barChart(rf.packetsPerHour.map(h=>h.count), rf.packetsPerHour.map(h=>h.hour.slice(11)+'h'), 'var(--accent)')}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>📦 Payload Type Mix</h3>
          ${renderPayloadPie(rf.payloadTypes)}
        </div>
        <div class="analytics-card flex-1">
          <h3>🔗 Hop Count Distribution</h3>
          ${barChart(topo.hopDistribution.map(h=>h.count), topo.hopDistribution.map(h=>h.hops), ['#3b82f6'])}
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
            '<h3>🔍 Neighbor Affinity Graph</h3>' +
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
        <div class="payload-bar-label"><span class="legend-dot" style="background:${colors[i]}"></span>${t.name}</div>
        <div class="hash-bar-track"><div class="hash-bar-fill" style="width:${w}%;background:${colors[i]}"></div></div>
        <div class="payload-bar-value">${t.count} <span class="text-muted">(${pct}%)</span></div>
      </div>`;
    });
    return html + '</div>';
  }

  // ===================== RF / SIGNAL =====================
  function renderRF(el, rf) {
    const snrHist = histogram(rf.snrValues, 20, statusGreen());
    const rssiHist = histogram(rf.rssiValues, 20, accentColor());

    el.innerHTML = `
      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>📶 SNR Distribution</h3>
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
          <h3>📡 RSSI Distribution</h3>
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
          <h3>🎯 SNR vs RSSI Scatter</h3>
          <p class="text-muted">Each dot = one packet. Cluster position reveals link quality.</p>
          ${renderScatter(rf.scatterData)}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>📊 SNR by Payload Type</h3>
          ${renderSNRByType(rf.snrByType)}
        </div>
        <div class="analytics-card flex-1">
          <h3>📈 Signal Quality Over Time</h3>
          ${renderSignalTimeline(rf.signalOverTime)}
        </div>
      </div>

      <div class="analytics-card">
        <h3>📏 Packet Size Distribution</h3>
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
        <td><strong>${t.name}</strong></td>
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
          <h3>🔗 Hop Count Distribution</h3>
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
          <h3>🕸️ Top Repeaters</h3>
          <p class="text-muted">Nodes appearing most in packet paths</p>
          ${renderRepeaterTable(topo.topRepeaters)}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>🤝 Repeater Pair Heatmap</h3>
          <p class="text-muted">Which repeaters frequently appear together in paths</p>
          ${renderPairTable(topo.topPairs)}
        </div>
        <div class="analytics-card flex-1">
          <h3>📊 Hops vs SNR</h3>
          <p class="text-muted">Does more hops = worse signal?</p>
          ${renderHopsSNR(topo.hopsVsSnr)}
        </div>
      </div>

      <div class="analytics-card">
        <h3>🏆 Best Path to Each Node</h3>
        <p class="text-muted">Shortest hop distance seen across all observers</p>
        ${renderBestPath(topo.bestPathList)}
      </div>

      <div class="analytics-card">
        <h3>🌐 Per-Observer Reachability</h3>
        <p class="text-muted">Nodes at each hop distance, from each observer's perspective</p>
        ${topo.observers.length > 1 ? `<div class="observer-selector" id="obsSelector">
          ${topo.observers.map((o, i) => `<button class="tab-btn ${i === 0 ? 'active' : ''}" data-obs="${o.id}">${esc(o.name)}</button>`).join('')}
          <button class="tab-btn" data-obs="__all">All Observers</button>
        </div>` : ''}
        <div id="reachContent">${renderPerObserverReach(topo.perObserverReach, topo.observers[0]?.id)}</div>
      </div>

      ${topo.multiObsNodes.length ? `<div class="analytics-card">
        <h3>🔀 Cross-Observer Comparison</h3>
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
      html += `<h4 style="margin:12px 0 6px">📡 ${esc(data.observer_name)}</h4>`;
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
  var CHANNEL_SORT_KEY = 'meshcore-channel-sort';

  function loadChannelSort() {
    try {
      var s = localStorage.getItem(CHANNEL_SORT_KEY);
      if (s) { var p = JSON.parse(s); if (p.col && p.dir) return p; }
    } catch (e) {}
    return { col: 'lastActivity', dir: 'desc' };
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
    return '<tr class="clickable-row" data-action="navigate" data-value="#/channels?ch=' + c.hash + '" tabindex="0" role="row">' +
      '<td><strong>' + esc(c.name || 'Unknown') + '</strong></td>' +
      '<td class="mono">' + (typeof c.hash === 'number' ? '0x' + c.hash.toString(16).toUpperCase().padStart(2, '0') : c.hash) + '</td>' +
      '<td>' + c.messages + '</td>' +
      '<td>' + c.senders + '</td>' +
      '<td>' + timeAgo(c.lastActivity) + '</td>' +
      '<td>' + (c.encrypted ? '🔒' : '✅') + '</td>' +
    '</tr>';
  }

  function channelTbodyHtml(channels, col, dir) {
    var sorted = sortChannels(channels, col, dir);
    var parts = [];
    for (var i = 0; i < sorted.length; i++) parts.push(channelRowHtml(sorted[i]));
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
    tbody.innerHTML = channelTbodyHtml(_channelData, _channelSortState.col, _channelSortState.dir);
    if (thead) thead.outerHTML = channelTheadHtml(_channelSortState.col, _channelSortState.dir);
  }

  function renderChannels(el, ch) {
    _channelData = ch.channels;
    if (!_channelSortState) _channelSortState = loadChannelSort();

    var timelineHtml = renderChannelTimeline(ch.channelTimeline);
    var topSendersHtml = renderTopSenders(ch.topSenders);
    var histoHtml = ch.msgLengths.length ? histogram(ch.msgLengths, 20, '#8b5cf6').svg : '<div class="text-muted">No decrypted messages</div>';

    el.innerHTML =
      '<div class="analytics-card">' +
        '<h3>📻 Channel Activity</h3>' +
        '<p class="text-muted">' + ch.activeChannels + ' active channels, ' + ch.decryptable + ' decryptable</p>' +
        '<table class="analytics-table" id="channelsTable">' +
          channelTheadHtml(_channelSortState.col, _channelSortState.dir) +
          '<tbody id="channelsTbody">' +
            channelTbodyHtml(_channelData, _channelSortState.col, _channelSortState.dir) +
          '</tbody>' +
        '</table>' +
      '</div>' +
      '<div class="analytics-row">' +
        '<div class="analytics-card flex-1">' +
          '<h3>💬 Messages / Hour by Channel</h3>' +
          timelineHtml +
        '</div>' +
        '<div class="analytics-card flex-1">' +
          '<h3>🗣️ Top Senders</h3>' +
          topSendersHtml +
        '</div>' +
      '</div>' +
      '<div class="analytics-card">' +
        '<h3>📊 Message Length Distribution</h3>' +
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

  function renderChannelTimeline(data) {
    if (!data.length) return '<div class="text-muted">No data</div>';
    var hours = []; var hourSet = {};
    var channelList = []; var channelSet = {};
    var lookup = {};
    var maxCount = 1;
    for (var i = 0; i < data.length; i++) {
      var d = data[i];
      if (!hourSet[d.hour]) { hourSet[d.hour] = 1; hours.push(d.hour); }
      if (!channelSet[d.channel]) { channelSet[d.channel] = 1; channelList.push(d.channel); }
      lookup[d.hour + '|' + d.channel] = d.count;
      if (d.count > maxCount) maxCount = d.count;
    }
    hours.sort();
    var colors = ['#ef4444','#22c55e','#3b82f6','#f59e0b','#8b5cf6','#ec4899','#14b8a6','#64748b'];
    var w = 600, h = 180, pad = 35;
    var xScale = (w - pad * 2) / Math.max(hours.length - 1, 1);
    var yScale = (h - pad * 2) / maxCount;
    var svg = '<svg viewBox="0 0 ' + w + ' ' + h + '" style="width:100%;max-height:180px" role="img" aria-label="Channel message activity over time"><title>Channel message activity over time</title>';
    for (var ci = 0; ci < channelList.length; ci++) {
      var pts = [];
      for (var hi = 0; hi < hours.length; hi++) {
        var count = lookup[hours[hi] + '|' + channelList[ci]] || 0;
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
    for (var lci = 0; lci < channelList.length; lci++) {
      legendParts.push('<span><span class="legend-dot" style="background:' + colors[lci % colors.length] + '"></span>' + esc(channelList[lci]) + '</span>');
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
          <h3>📈 Hash Size Over Time</h3>
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

    var statusIcon = { confirmed: '✅', suspected: '⚠️', unknown: '❓' };
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
                '<td><span class="badge" style="background:' + roleColor + '20;color:' + roleColor + '">' + esc(r.role || 'unknown') + '</span></td>' +
                '<td><span style="color:' + (statusColor[r.status] || statusColor.unknown) + '">' +
                  (statusIcon[r.status] || '❓') + ' ' + (statusLabel[r.status] || 'Unknown') + '</span></td>' +
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
          '<button class="tab-btn" data-mb-filter="confirmed" style="--filter-color:var(--success, #22c55e)">✅ Confirmed (' + counts.confirmed + ')</button>' +
          '<button class="tab-btn" data-mb-filter="suspected" style="--filter-color:var(--warning, #eab308)">⚠️ Suspected (' + counts.suspected + ')</button>' +
          '<button class="tab-btn" data-mb-filter="unknown" style="--filter-color:var(--text-muted, #888)">❓ Unknown (' + counts.unknown + ')</button>' +
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
        <a href="#/analytics?tab=collisions&section=inconsistentHashSection" style="color:var(--accent)">⚠️ Inconsistent Sizes</a>
        <span style="color:var(--border)">|</span>
        <a href="#/analytics?tab=collisions&section=hashMatrixSection" style="color:var(--accent)">🔢 Hash Matrix</a>
        <span style="color:var(--border)">|</span>
        <a href="#/analytics?tab=collisions&section=collisionRiskSection" style="color:var(--accent)">💥 Collision Risk</a>
        <span style="color:var(--border)">|</span>
        <a href="#/analytics?tab=prefix-tool" style="color:var(--accent)">🔎 Check a prefix →</a>
      </nav>
      <p class="text-muted" style="margin:0 0 12px;font-size:0.78em">This tab shows operational collisions among <strong>repeaters</strong> grouped by their configured hash size. The <a href="#/analytics?tab=prefix-tool" style="color:var(--accent)">Prefix Tool</a> checks all repeaters regardless of their configured hash size.</p>

      <div class="analytics-card" id="inconsistentHashSection">
        <div style="display:flex;justify-content:space-between;align-items:center"><h3 style="margin:0">⚠️ Inconsistent Hash Sizes</h3><a href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a></div>
        <p class="text-muted" style="margin:4px 0 8px;font-size:0.8em">Repeaters and room servers sending adverts with varying hash sizes in the last 7 days. Originally caused by a <a href="https://github.com/meshcore-dev/MeshCore/commit/fcfdc5f" target="_blank" style="color:var(--accent)">firmware bug</a> where automatic adverts ignored the configured multibyte path setting, fixed in <a href="https://github.com/meshcore-dev/MeshCore/releases/tag/repeater-v1.14.1" target="_blank" style="color:var(--accent)">repeater v1.14.1</a>. Companion nodes are excluded.</p>
        <div id="inconsistentHashList"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading…</div></div>
      </div>

      <div class="analytics-card" id="hashMatrixSection">
        <div style="display:flex;justify-content:space-between;align-items:center">
          <h3 style="margin:0" id="hashMatrixTitle">🔢 Hash Usage Matrix</h3>
          <a href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a>
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
        <div style="display:flex;justify-content:space-between;align-items:center"><h3 style="margin:0" id="collisionRiskTitle">💥 Collision Risk</h3><a href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a></div>
        <div id="collisionList"><div class="text-muted" style="padding:8px">Loading…</div></div>
      </div>
    `;
    // Use pre-computed collision data from server (no more /nodes?limit=2000 fetch)
    const cData = collisionData || { inconsistent_nodes: [], by_size: {} };
    const inconsistent = cData.inconsistent_nodes || [];
    const ihEl = document.getElementById('inconsistentHashList');
    if (ihEl) {
      if (!inconsistent.length) {
        ihEl.innerHTML = '<div class="text-muted" style="padding:4px">✅ No inconsistencies detected — all nodes are reporting consistent hash sizes.</div>';
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
              <td><a href="#/nodes/${encodeURIComponent(n.public_key)}?section=node-packets" style="font-weight:600;color:var(--accent)">${esc(n.name || n.public_key.slice(0, 12))}</a></td>
              <td><span class="badge" style="background:${roleColor}20;color:${roleColor}">${n.role}</span></td>
              <td><code style="font-family:var(--mono);font-weight:700">${prefix}</code> <span class="text-muted">(${n.hash_size || '?'}B)</span></td>
              <td>${sizeBadges}</td>
            </tr>`;
          }).join('')}</tbody>
        </table>
        <p class="text-muted" style="margin:8px 0 0;font-size:0.8em">${inconsistent.length} node${inconsistent.length > 1 ? 's' : ''} affected. Click a node name to see which adverts have different hash sizes.</p>`;
      }
    }

    // Repeaters and routing nodes no longer needed — collision data is server-computed

    let currentBytes = 1;
    function refreshHashViews(bytes) {
      currentBytes = bytes;
      hideMatrixTip();
      // Update selector button states
      document.querySelectorAll('.hash-byte-btn').forEach(b => {
        b.classList.toggle('active', Number(b.dataset.bytes) === bytes);
      });
      // Update titles and description
      const matrixTitle = document.getElementById('hashMatrixTitle');
      const matrixDesc = document.getElementById('hashMatrixDesc');
      const riskTitle = document.getElementById('collisionRiskTitle');
      if (matrixTitle) matrixTitle.textContent = bytes === 3 ? '🔢 Hash Usage Matrix' : `🔢 ${bytes}-Byte Hash Usage Matrix`;
      if (riskTitle) riskTitle.textContent = `💥 ${bytes}-Byte Collision Risk`;
      if (matrixDesc) {
        if (bytes === 1) matrixDesc.textContent = 'Click a cell to see which nodes share that 1-byte prefix.';
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

    refreshHashViews(1);
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

  function initMatrixTooltip(el) {
    if (el._matrixTipInit) return;
    el._matrixTipInit = true;
    el.addEventListener('mouseover', e => {
      const td = e.target.closest('td[data-tip]');
      if (!td) return;
      const tip = getMatrixTip();
      tip.innerHTML = td.dataset.tip;
      tip.style.display = 'block';
    });
    el.addEventListener('mousemove', e => {
      if (!_matrixTip || _matrixTip.style.display === 'none') return;
      const x = e.clientX + 14, y = e.clientY + 14;
      _matrixTip.style.left = Math.min(x, window.innerWidth - _matrixTip.offsetWidth - 8) + 'px';
      _matrixTip.style.top = Math.min(y, window.innerHeight - _matrixTip.offsetHeight - 8) + 'px';
    });
    el.addEventListener('mouseout', e => {
      if (e.target.closest('td[data-tip]') && !e.relatedTarget?.closest('td[data-tip]')) hideMatrixTip();
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
        <div class="analytics-stat-value" style="color:${collisionCount > 0 ? 'var(--status-red)' : 'var(--status-green)'}">${collisionCount}${collisionCount > 0 ? ' <span style="font-size:11px;opacity:0.7">▼</span>' : ''}</div>
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
    return { cls: 'hash-cell-collision', bg: `background:rgb(${Math.round(220+35*t)},${Math.round(120*(1-t))},30);` };
  }

  function hashCellTd(hex, cellSize, cls, bg, count, tipHtml, fontWeight) {
    return `<td class="hash-cell ${cls}${count ? ' hash-active' : ''}" data-hex="${hex}" data-tip="${tipHtml.replace(/"/g,'&quot;')}" style="width:${cellSize}px;height:${cellSize}px;text-align:center;${bg}border:1px solid var(--border);cursor:${count ? 'pointer' : 'default'};font-size:11px;font-weight:${fontWeight}">${hex}</td>`;
  }

  function hashTooltipHtml(hexLabel, statusText, nodesHtml) {
    let html = `<div class="hash-matrix-tooltip-hex">${hexLabel}</div><div class="hash-matrix-tooltip-status">${statusText}</div>`;
    if (nodesHtml) html += `<div class="hash-matrix-tooltip-nodes">${nodesHtml}</div>`;
    return html;
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
        `<p class="text-muted" style="margin:8px 0 0;font-size:0.8em">ℹ️ This tab only counts collisions among repeaters configured for this hash size. The <a href="#/analytics?tab=prefix-tool" style="color:var(--accent)">Prefix Tool</a> checks all repeaters regardless of configured hash size.</p>`;
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
          const nodeLabel = m => `<div style="font-size:11px">${esc(m.name||m.public_key.slice(0,12))}${!m.role ? ' <span style="opacity:0.7">(unknown role)</span>' : ''}</div>`;
          const nodesPreview = nodes.slice(0,5).map(nodeLabel).join('') + (nodes.length > 5 ? `<div class="hash-matrix-tooltip-status">+${nodes.length-5} more</div>` : '');
          const tip = count === 0 ? hashTooltipHtml(`0x${hex}`, 'Available')
            : count === 1 ? hashTooltipHtml(`0x${hex}`, 'One node — no collision', nodeLabel(nodes[0]))
            : isPossible ? hashTooltipHtml(`0x${hex}`, `${count} nodes — POSSIBLE CONFLICT`, nodesPreview)
            : hashTooltipHtml(`0x${hex}`, `${count} nodes — COLLISION`, nodesPreview);
          return hashCellTd(hex, cs, cls, bg, count, tip, count >= 2 ? '700' : '400');
        },
        400,
        [
          {cls: 'hash-cell-empty', style: 'border:1px solid var(--border)', text: 'Available'},
          {cls: 'hash-cell-taken', text: 'One node'},
          {cls: 'hash-cell-possible', text: 'Possible conflict'},
          {cls: 'hash-cell-collision', style: 'background:rgb(220,80,30)', text: 'Collision'}
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
          const nodeLabel2 = m => esc(m.name||m.public_key.slice(0,8)) + (!m.role ? ' (?)' : '');
          const tip = nodeCount === 0
            ? hashTooltipHtml(`0x${hex}__`, 'No nodes in this group')
            : (info.collision_count || 0) === 0
              ? hashTooltipHtml(`0x${hex}__`, `${nodeCount} node${nodeCount>1?'s':''} — no 2-byte collisions`)
              : hashTooltipHtml(`0x${hex}__`,
                  hasConfirmed ? (info.collision_count||0) + ' collision' + ((info.collision_count||0)>1?'s':'') : 'Possible conflict',
                  Object.entries(info.two_byte_map||{}).filter(([,v])=>v.length>1).slice(0,4).map(([p,ns])=>`<div style="font-size:11px;padding:1px 0"><span style="color:${hasConfirmed?'var(--status-red)':'var(--status-yellow)'};font-family:var(--mono);font-weight:700">${p}</span> — ${ns.map(nodeLabel2).join(', ')}</div>`).join(''));
          return hashCellTd(hex, cs, cls, bg, nodeCount, tip, maxCol > 0 ? '700' : '400');
        },
        420,
        [
          {cls: 'hash-cell-empty', style: 'border:1px solid var(--border)', text: 'No nodes in group'},
          {cls: 'hash-cell-taken', text: 'Nodes present, no collision'},
          {cls: 'hash-cell-possible', text: 'Possible conflict'},
          {cls: 'hash-cell-collision', style: 'background:rgb(220,80,30)', text: 'Collision'}
        ],
        (td) => {
          const hex = td.dataset.hex.toUpperCase();
          const info = twoByteCells[hex];
          const detail = document.getElementById('hashDetail');
          if (!info || !(info.group_nodes || []).length) { detail.innerHTML = ''; return; }
          const groupNodes = info.group_nodes || [];
          let dhtml = `<strong class="mono" style="font-size:1.1em">0x${hex}__</strong> — ${groupNodes.length} node${groupNodes.length !== 1 ? 's' : ''} in group`;
          if ((info.collision_count || 0) === 0) {
            dhtml += `<div class="text-muted" style="margin-top:6px;font-size:0.85em">✅ No 2-byte collisions in this group</div>`;
            dhtml += `<div style="margin-top:8px">${groupNodes.map(m => {
              const prefix = m.public_key.slice(0,4).toUpperCase();
              return `<div style="padding:2px 0"><code class="mono" style="font-size:0.85em">${prefix}</code> <a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a></div>`;
            }).join('')}</div>`;
          } else {
            dhtml += `<div style="margin-top:8px">`;
            for (const [twoHex, nodes] of Object.entries(info.two_byte_map || {}).sort()) {
              const isCollision = nodes.length > 1;
              dhtml += `<div style="margin-bottom:6px;padding:4px 6px;border-radius:4px;background:${isCollision ? 'rgba(220,50,30,0.1)' : 'transparent'};border:1px solid ${isCollision ? 'rgba(220,50,30,0.3)' : 'transparent'}">`;
              dhtml += `<code class="mono" style="font-size:0.9em;font-weight:${isCollision?'700':'400'}">${twoHex}</code>${isCollision ? ' <span style="color:#dc2626;font-size:0.75em;font-weight:700">COLLISION</span>' : ''} `;
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
        ? '✅ No 3-byte prefix collisions detected — all repeaters have unique 3-byte prefixes.'
        : `✅ No ${bytes}-byte collisions detected`;
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
          badge = `<span class="badge" style="background:var(--status-green);color:#fff" title="All nodes within ${t50} — likely true collision, same RF neighborhood">🏘️ Local</span>`;
          tooltip = 'Nodes close enough for direct RF — probably genuine prefix collision';
        } else if (c.classification === 'regional') {
          badge = `<span class="badge" style="background:var(--status-yellow);color:#fff" title="Nodes ${t50}–${t200} apart — edge of LoRa range, could be atmospheric">⚡ Regional</span>`;
          tooltip = 'At edge of 915MHz range — could indicate atmospheric ducting or hilltop-to-hilltop links';
        } else if (c.classification === 'distant') {
          badge = `<span class="badge" style="background:var(--status-red);color:#fff" title="Nodes >${t200} apart — beyond typical 915MHz range">🌐 Distant</span>`;
          tooltip = 'Beyond typical LoRa range — likely internet bridging, MQTT gateway, or separate mesh networks sharing prefix';
        } else {
          badge = '<span class="badge" style="background:#6b7280;color:#fff">❓ Unknown</span>';
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
      <strong>🏘️ Local</strong> &lt;${t50}: true prefix collision, same mesh area &nbsp;
      <strong>⚡ Regional</strong> ${t50}–${t200}: edge of LoRa range, possible atmospheric propagation &nbsp;
      <strong>🌐 Distant</strong> &gt;${t200}: beyond 915MHz range — internet bridge, MQTT gateway, or separate networks
    </div>`;
  }
    async function renderSubpaths(el) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Analyzing route patterns…</div>';
    try {
      const rq = RegionFilter.regionQueryString();
      const bulk = await api('/analytics/subpaths-bulk?groups=2-2:50,3-3:30,4-4:20,5-8:15' + rq, { ttl: CLIENT_TTL.analyticsRF });
      const [d2, d3, d4, d5] = bulk.results;

      function renderTable(data, title) {
        if (!data.subpaths.length) return `<h4>${title}</h4><div class="text-muted">No data</div>`;
        const maxCount = data.subpaths[0]?.count || 1;
        return `<h4>${title}</h4>
          <p class="text-muted" style="margin:4px 0 8px">From ${data.totalPaths.toLocaleString()} paths with 2+ hops</p>
          <table class="analytics-table"><thead><tr>
            <th scope="col">#</th><th scope="col">Route</th><th scope="col">Occurrences</th><th scope="col">% of paths</th><th scope="col">Frequency</th>
          </tr></thead><tbody>
          ${data.subpaths.map((s, i) => {
            const barW = Math.max(2, Math.round(s.count / maxCount * 100));
            const hops = s.path.split(' → ');
            const rawHops = s.rawHops || [];
            const hasSelfLoop = hops.some((h, j) => j > 0 && h === hops[j - 1]);
            const routeDisplay = hops.map(h => esc(h)).join(' → ');
            const prefixDisplay = rawHops.join(' → ');
            return `<tr data-hops="${esc(rawHops.join(','))}" ${hasSelfLoop ? 'class="subpath-selfloop"' : ''} style="cursor:pointer">
              <td>${i + 1}</td>
              <td>${routeDisplay}${hasSelfLoop ? ' <span title="Contains self-loop — likely 1-byte prefix collision" style="cursor:help">🔄</span>' : ''}<br><span class="hop-prefix mono">${esc(prefixDisplay)}</span></td>
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
            <h3>🛤️ Route Pattern Analysis</h3>
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
          <h5>📏 Hop Distances</h5>
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
                const cls = km > 200 ? 'color:var(--status-red);font-weight:bold' : km > 50 ? 'color:var(--status-yellow)' : 'color:var(--status-green)';
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
          <h5>📡 Observer Receive Signal</h5>
          <p class="text-muted" style="font-size:0.8em;margin:0 0 4px">Last hop → observer only, not between nodes in the route</p>
          ${data.signal.avgSnr != null
            ? `<div>Avg SNR: <strong>${data.signal.avgSnr} dB</strong> · Avg RSSI: <strong>${data.signal.avgRssi} dBm</strong> · ${data.signal.samples} samples</div>`
            : '<div class="text-muted">No signal data</div>'}
        </div>

        <div class="subpath-section">
          <h5>🕐 Activity by Hour (UTC)</h5>
          <div class="hour-chart">
            ${data.hourDistribution.map((c, h) => `<div class="hour-bar" title="${h}:00 UTC — ${c} packets" style="height:${Math.max(2, c / maxHour * 100)}%"></div>`).join('')}
          </div>
          <div class="hour-labels"><span>0</span><span>6</span><span>12</span><span>18</span><span>23</span></div>
        </div>

        <div class="subpath-section">
          <h5>⏱️ Timeline</h5>
          <div>First seen: ${data.firstSeen ? new Date(data.firstSeen).toLocaleString() : '—'}</div>
          <div>Last seen: ${data.lastSeen ? new Date(data.lastSeen).toLocaleString() : '—'}</div>
        </div>

        ${data.observers.length ? `
        <div class="subpath-section">
          <h5>👁️ Observers</h5>
          ${data.observers.map(o => `<div>${esc(o.name)}: ${o.count}</div>`).join('')}
        </div>` : ''}

        ${data.parentPaths.length ? `
        <div class="subpath-section">
          <h5>🔗 Full Paths Containing This Route</h5>
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
        }).bindTooltip(n.name, { permanent: false }).addTo(map);
      });

      L.polyline(latlngs, { color: statusYellow(), weight: 3, dashArray: '8,6', opacity: 0.8 }).addTo(map);
      map.fitBounds(L.latLngBounds(latlngs).pad(0.3));
    }
  }

  async function renderNodesTab(el) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading node analytics…</div>';
    try {
      const rq = RegionFilter.regionQueryString();
      const [nodesResp, bulkHealth] = await Promise.all([
        api('/nodes?limit=10000&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList }),
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
        return myKeys.has(n.public_key) ? ' <span style="color:var(--accent);font-size:10px">★ MINE</span>' : '';
      }

      // ROLE_COLORS from shared roles.js

      el.innerHTML = `
        <div class="analytics-section">
          <h3>🔍 Network Status</h3>
          <div style="display:flex;gap:16px;flex-wrap:wrap;margin-bottom:20px">
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700;color:var(--status-green)">${active}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)">🟢 Active</div>
            </div>
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700;color:var(--status-yellow)">${degraded}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)">🟡 Degraded</div>
            </div>
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700;color:var(--status-red)">${silent}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)">🔴 Silent</div>
            </div>
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700">${totalNodes}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)">Total Nodes</div>
            </div>
          </div>

          <h3>📊 Role Breakdown</h3>
          <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:24px">
            ${Object.entries(roleCounts).sort((a,b) => b[1]-a[1]).map(([role, count]) => {
              const c = ROLE_COLORS[role] || '#6b7280';
              return `<span class="badge" style="background:${c}20;color:${c};padding:6px 12px;font-size:13px">${role}: ${count}</span>`;
            }).join('')}
          </div>

          ${myKeys.size ? `<h3>⭐ My Claimed Nodes</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Packets</th><th scope="col">Avg SNR</th><th scope="col">Observers</th><th scope="col">Last Heard</th></tr></thead>
            <tbody>
              ${enriched.filter(n => myKeys.has(n.public_key)).map(n => {
                const s = n.health.stats;
                return `<tr>
                  <td>${nodeLink(n)}</td>
                  <td><span class="badge" style="background:${(ROLE_COLORS[n.role]||'#6b7280')}20;color:${ROLE_COLORS[n.role]||'#6b7280'}">${n.role}</span></td>
                  <td>${s.totalTransmissions || s.totalPackets || 0}</td>
                  <td>${s.avgSnr != null ? s.avgSnr.toFixed(1) + ' dB' : '—'}</td>
                  <td>${n.health.observers?.length || 0}</td>
                  <td>${s.lastHeard ? timeAgo(s.lastHeard) : '—'}</td>
                </tr>`;
              }).join('') || '<tr><td colspan="6" class="text-muted">No claimed nodes have health data</td></tr>'}
            </tbody>
          </table>` : ''}

          <h3>🏆 Most Active Nodes</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">#</th><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Total Packets</th><th scope="col">Packets Today</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${byPackets.slice(0, 15).map((n, i) => `<tr>
                <td>${i + 1}</td>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="background:${(ROLE_COLORS[n.role]||'#6b7280')}20;color:${ROLE_COLORS[n.role]||'#6b7280'}">${n.role}</span></td>
                <td>${n.health.stats.totalTransmissions || n.health.stats.totalPackets || 0}</td>
                <td>${n.health.stats.packetsToday || 0}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link">📊</a></td>
              </tr>`).join('')}
            </tbody>
          </table>

          <h3>📶 Best Signal Quality</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">#</th><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Avg SNR</th><th scope="col">Observers</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${bySnr.slice(0, 15).map((n, i) => `<tr>
                <td>${i + 1}</td>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="background:${(ROLE_COLORS[n.role]||'#6b7280')}20;color:${ROLE_COLORS[n.role]||'#6b7280'}">${n.role}</span></td>
                <td>${n.health.stats.avgSnr.toFixed(1)} dB</td>
                <td>${n.health.observers?.length || 0}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link">📊</a></td>
              </tr>`).join('')}
            </tbody>
          </table>

          <h3>👀 Most Observed Nodes</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">#</th><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Observers</th><th scope="col">Avg SNR</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${byObservers.slice(0, 15).map((n, i) => `<tr>
                <td>${i + 1}</td>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="background:${(ROLE_COLORS[n.role]||'#6b7280')}20;color:${ROLE_COLORS[n.role]||'#6b7280'}">${n.role}</span></td>
                <td>${n.health.observers?.length || 0}</td>
                <td>${n.health.stats.avgSnr != null ? n.health.stats.avgSnr.toFixed(1) + ' dB' : '—'}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link">📊</a></td>
              </tr>`).join('')}
            </tbody>
          </table>

          <h3>⏰ Recently Active</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Last Heard</th><th scope="col">Packets Today</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${byRecent.slice(0, 15).map(n => `<tr>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="background:${(ROLE_COLORS[n.role]||'#6b7280')}20;color:${ROLE_COLORS[n.role]||'#6b7280'}">${n.role}</span></td>
                <td>${timeAgo(n.health.stats.lastHeard)}</td>
                <td>${n.health.stats.packetsToday || 0}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link">📊</a></td>
              </tr>`).join('')}
            </tbody>
          </table>
        </div>`;
    } catch (e) {
      el.innerHTML = `<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load node analytics: ${esc(e.message)}</div>`;
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
      html += `<div class="analytics-section"><h3>🏆 Top 20 Longest Hops</h3><table class="data-table"><thead><tr><th scope="col">#</th><th scope="col">From</th><th scope="col">To</th><th scope="col">Distance (${distUnitLabel})</th><th scope="col">Type</th><th scope="col">Obs</th><th scope="col">Best SNR</th><th scope="col">Median SNR</th><th scope="col">Packet</th><th scope="col"></th></tr></thead><tbody>`;
      const top20 = data.topHops.slice(0, 20);
      top20.forEach((h, i) => {
        const fromLink = h.fromPk ? `<a href="#/nodes/${encodeURIComponent(h.fromPk)}" class="analytics-link">${esc(h.fromName)}</a>` : esc(h.fromName || '?');
        const toLink = h.toPk ? `<a href="#/nodes/${encodeURIComponent(h.toPk)}" class="analytics-link">${esc(h.toName)}</a>` : esc(h.toName || '?');
        const bestSnr = h.bestSnr != null ? Number(h.bestSnr).toFixed(1) + ' dB' : '<span class="text-muted">—</span>';
        const medianSnr = h.medianSnr != null ? Number(h.medianSnr).toFixed(1) + ' dB' : '<span class="text-muted">—</span>';
        const obs = h.obsCount != null ? h.obsCount : 1;
        const pktLink = h.hash ? `<a href="#/packet/${encodeURIComponent(h.hash)}" class="analytics-link mono" style="font-size:0.85em">${esc(h.hash.slice(0, 12))}…</a>` : '—';
        const mapBtn = h.fromPk && h.toPk ? `<button class="btn-icon dist-map-hop" data-from="${esc(h.fromPk)}" data-to="${esc(h.toPk)}" title="View on map">🗺️</button>` : '';
        const tsTitle = h.timestamp ? `Best observation: ${h.timestamp}` : '';
        html += `<tr title="${esc(tsTitle)}"><td>${i+1}</td><td>${fromLink}</td><td>${toLink}</td><td><strong>${formatDistance(h.dist)}</strong></td><td>${esc(h.type)}</td><td>${obs}</td><td>${bestSnr}</td><td>${medianSnr}</td><td>${pktLink}</td><td>${mapBtn}</td></tr>`;
      });
      html += `</tbody></table></div>`;

      // Top paths
      if (data.topPaths.length) {
        html += `<div class="analytics-section"><h3>🛤️ Top 10 Longest Multi-Hop Paths</h3><table class="data-table"><thead><tr><th scope="col">#</th><th scope="col">Total Distance (${distUnitLabel})</th><th scope="col">Hops</th><th scope="col">Route</th><th scope="col">Packet</th><th scope="col"></th></tr></thead><tbody>`;
        data.topPaths.slice(0, 10).forEach((p, i) => {
          const route = p.hops.map(h => esc(h.fromName)).concat(esc(p.hops[p.hops.length-1].toName)).join(' → ');
          const pktLink = p.hash ? `<a href="#/packet/${encodeURIComponent(p.hash)}" class="analytics-link mono" style="font-size:0.85em">${esc(p.hash.slice(0, 12))}…</a>` : '—';
          // Collect all unique pubkeys in path order
          const pathPks = [];
          p.hops.forEach(h => { if (h.fromPk && !pathPks.includes(h.fromPk)) pathPks.push(h.fromPk); });
          if (p.hops.length && p.hops[p.hops.length-1].toPk) { const last = p.hops[p.hops.length-1].toPk; if (!pathPks.includes(last)) pathPks.push(last); }
          const mapBtn = pathPks.length >= 2 ? `<button class="btn-icon dist-map-path" data-hops='${JSON.stringify(pathPks)}' title="View on map">🗺️</button>` : '';
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

function destroy() { _analyticsData = {}; _channelData = null; if (_ngState && _ngState.animId) { cancelAnimationFrame(_ngState.animId); } _ngState = null; if (_themeRefreshHandler) { window.removeEventListener('theme-refresh', _themeRefreshHandler); _themeRefreshHandler = null; } }

  // Expose for testing
  if (typeof window !== 'undefined') {
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
        <h3>🕸️ Neighbor Graph</h3>
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
          <summary style="cursor:pointer;font-size:13px;color:var(--text-secondary)">📋 Text-based neighbor list (accessible alternative)</summary>
          <div id="ngTextList" style="font-size:12px;max-height:300px;overflow-y:auto;padding:8px;background:var(--bg-secondary);border-radius:4px;margin-top:4px"></div>
        </details>
      </div>`;

    // Role checkboxes
    const roles = ['repeater','companion','room','sensor'];
    const rcEl = document.getElementById('ngRoleChecks');
    roles.forEach(r => {
      const color = (window.ROLE_COLORS || {})[r] || '#888';
      rcEl.innerHTML += `<label style="font-size:12px;margin-right:8px"><input type="checkbox" data-role="${r}" checked> <span style="color:${esc(color)}">${esc(r)}</span></label>`;
    });
    // Observer checkbox — unchecked by default (observers create hub-and-spoke noise)
    {
      const color = (window.ROLE_COLORS || {}).observer || '#8b5cf6';
      rcEl.innerHTML += `<label style="font-size:12px;margin-right:8px"><input type="checkbox" data-role="observer"> <span style="color:${esc(color)}">observer</span></label>`;
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
    renderNGStats(_ngState);
    startGraphRenderer();

    // Filter listeners
    // Restore saved min score from localStorage
    var savedScore = localStorage.getItem('ng-min-score');
    if (savedScore !== null) {
      document.getElementById('ngMinScore').value = savedScore;
      document.getElementById('ngMinScoreVal').textContent = (savedScore / 100).toFixed(2);
      applyNGFilters();
    }

    document.getElementById('ngMinScore').addEventListener('input', function() {
      document.getElementById('ngMinScoreVal').textContent = (this.value / 100).toFixed(2);
      localStorage.setItem('ng-min-score', this.value);
      applyNGFilters();
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

    _ngState.cooling = 1.0;
    renderNGStats(_ngState);
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
        var conf = nb.ambiguous ? ' ⚠' : (nb.score >= 0.5 ? ' ●' : ' ○');
        return esc(name) + conf;
      }).join(', ');
      html += '<tr><td style="padding:4px;border-bottom:1px solid var(--border)">' + esc(n.name || n.pubkey.slice(0, 12)) + '</td><td style="padding:4px;border-bottom:1px solid var(--border)">' + esc(n.role || 'unknown') + '</td><td style="padding:4px;border-bottom:1px solid var(--border)">' + (neighbors || '<em>none</em>') + '</td></tr>';
    });
    html += '</tbody></table>';
    html += '<p style="margin-top:8px;font-size:11px;color:var(--text-secondary)">● = high confidence (score ≥ 0.5), ○ = low confidence, ⚠ = ambiguous/unresolved</p>';
    listEl.innerHTML = html;
  }

  function startGraphRenderer() {
    if (!_ngState) return;

    // Node count guard: skip force simulation for very large graphs
    var NODE_LIMIT = 1000;
    if (_ngState.allNodes.length > NODE_LIMIT) {
      var el = document.getElementById('ngCanvas');
      if (el) {
        el.style.display = 'none';
        var msg = document.createElement('div');
        msg.className = 'analytics-card';
        msg.innerHTML = '<p class="text-muted">Graph has ' + _ngState.allNodes.length + ' nodes (limit: ' + NODE_LIMIT + '). Force simulation skipped for performance. Use filters to reduce the node count.</p>';
        el.parentNode.insertBefore(msg, el);
      }
      return;
    }

    const canvas = document.getElementById('ngCanvas');
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    const dpr = window.devicePixelRatio || 1;
    canvas.width = canvas.clientWidth * dpr;
    canvas.height = canvas.clientHeight * dpr;
    ctx.scale(dpr, dpr);
    const W = canvas.clientWidth, H = canvas.clientHeight;

    // Interaction
    let hoverNode = null;

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
        if (n !== hoverNode) {
          hoverNode = n;
          canvas.style.cursor = n ? 'pointer' : 'grab';
          const tip = document.getElementById('ngTooltip');
          if (n && tip) {
            tip.style.display = 'block';
            tip.style.left = (cx + 12) + 'px';
            tip.style.top = (cy - 8) + 'px';
            tip.innerHTML = `<strong>${esc(n.name || n.pubkey.slice(0, 12) + '…')}</strong><br>Role: ${esc(n.role || 'unknown')}<br>Neighbors: ${n.neighbor_count || 0}`;
          } else if (tip) {
            tip.style.display = 'none';
          }
        } else if (hoverNode) {
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
      canvas.style.cursor = hoverNode ? 'pointer' : 'grab';
    });

    canvas.addEventListener('mouseleave', function() {
      _ngState.dragging = null;
      _ngState.panning = false;
      _ngState._wasDragging = false;
      const tip = document.getElementById('ngTooltip');
      if (tip) tip.style.display = 'none';
      hoverNode = null;
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

    // Cache text color to avoid getComputedStyle every frame
    const _labelColor = cssVar('--text-primary') || '#e0e0e0';

    // Force simulation + render loop
    // Performance: 500 nodes brute-force repulsion: avg ~4ms/frame = 250fps headroom (measured Chrome 120, M1)
    var _perfFrameTimes = [], _perfLastTime = 0;
    function tick() {
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
        if (n === hoverNode) {
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

    const rq = RegionFilter.regionQueryString();
    const regionLabel = rq ? (new URLSearchParams(rq.slice(1)).get('region') || '') : '';

    let nodesResp;
    try {
      nodesResp = await api('/nodes?limit=10000&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList });
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
      ? `<p class="text-muted" style="font-size:0.85em;margin:4px 0 0">Showing data for region: <strong>${esc(regionLabel)}</strong>. <a href="#/analytics?tab=prefix-tool" style="color:var(--accent)">Check all repeaters →</a></p>`
      : '';

    el.innerHTML = `
      <div class="analytics-card" id="ptOverview">
        <div style="display:flex;align-items:center;gap:8px;cursor:pointer;user-select:none" id="ptOverviewToggle">
          <span id="ptOverviewChevron" style="font-size:0.75em;color:var(--text-muted);transition:transform 0.2s">▶</span>
          <h3 style="margin:0">Network Overview</h3>
        </div>
        <div id="ptOverviewBody" style="display:none">
          ${regionNote}
          <div style="display:flex;gap:12px;flex-wrap:wrap;margin:12px 0 16px">
            <div class="analytics-stat-card" style="flex:1;min-width:110px">
              <div class="analytics-stat-label">Total repeaters</div>
              <div class="analytics-stat-value">${totalNodes.toLocaleString()}</div>
            </div>
            ${[1, 2, 3].map(b => `
            <div class="analytics-stat-card" style="flex:1;min-width:150px;border-color:${stats[b].collidingPrefixes > 0 ? 'var(--status-red)' : 'var(--border)'}">
              <div class="analytics-stat-label">${b}-byte prefixes</div>
              <div class="analytics-stat-value" style="font-size:1em">
                ${stats[b].usedPrefixes.toLocaleString()}
                <span class="text-muted" style="font-size:0.7em"> / ${spaceSizes[b].toLocaleString()}</span>
              </div>
              <div style="font-size:0.82em;margin-top:4px;color:${stats[b].collidingPrefixes > 0 ? 'var(--status-red)' : 'var(--status-green)'}">
                ${stats[b].collidingPrefixes === 0
                  ? '✅ No collisions'
                  : `⚠️ ${stats[b].collidingPrefixes} prefix${stats[b].collidingPrefixes !== 1 ? 'es' : ''} collide`}
              </div>
            </div>`).join('')}
          </div>
          <div style="background:var(--bg-secondary,var(--bg));border:1px solid var(--border);border-radius:6px;padding:10px 14px;margin-bottom:12px">
            <strong>Recommendation: ${rec} prefixes</strong> — ${recDetail}
            <span class="text-muted" style="font-size:0.8em;display:block;margin-top:4px">Hash size is configured per-node in firmware. Changing requires reflashing.</span>
          </div>
          <div style="background:var(--bg-secondary,var(--bg));border:1px solid var(--border);border-radius:6px;padding:10px 14px;font-size:0.85em">
            <strong>ℹ️ About these numbers:</strong> This tool checks <em>repeater</em> public key prefixes regardless of their configured hash size. Only repeaters are included because they are the nodes that relay packets using hash-based addressing.
            The <a href="#/analytics?tab=collisions" style="color:var(--accent)">Hash Issues</a> tab shows only <em>operational</em> collisions — nodes that actually use the same hash size and are repeaters.
            A collision shown here may not appear in Hash Issues if the nodes use a different hash size.
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
          <button id="ptCheckBtn" style="padding:6px 16px;background:var(--accent);color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:0.95em">Check</button>
        </div>
        <div id="ptCheckerResults" style="margin-top:14px"></div>
      </div>

      <div class="analytics-card" id="ptGenerator">
        <h3 style="margin-top:0">Generate Available Prefix</h3>
        <p class="text-muted" style="margin-top:0;font-size:0.9em">Find a prefix with zero current collisions.</p>
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
          <button id="ptGenBtn" style="padding:6px 16px;background:var(--accent);color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:0.95em">Generate</button>
        </div>
        <div id="ptGenResult"></div>
        <div style="margin-top:14px;padding:10px 14px;border:1px solid var(--accent);border-radius:6px;background:var(--bg-secondary,var(--bg));font-size:0.88em">
          📖 <strong>New to multi-byte prefixes?</strong>
          <a href="https://github.com/meshcore-dev/MeshCore/blob/main/docs/faq.md#39-q-what-is-multi-byte-support--what-do-1-byte-2-byte-3-byte-adverts-and-messages-mean"
            target="_blank" rel="noopener noreferrer" style="color:var(--accent);margin-left:4px">
            Read the MeshCore FAQ on multi-byte support →
          </a>
        </div>
      </div>`;

    // --- Helpers ---
    function nodeEntry(n) {
      const name = esc(n.name || n.public_key.slice(0, 12));
      const role = n.role ? `<span class="text-muted" style="font-size:0.82em">${esc(n.role)}</span>` : '';
      const hs = n.hash_size ? ` <span class="text-muted" style="font-size:0.78em;opacity:0.7">${n.hash_size}B hash</span>` : '';
      const when = n.last_seen ? ` <span class="text-muted" style="font-size:0.8em">${new Date(n.last_seen).toLocaleDateString()}</span>` : '';
      return `<div style="padding:3px 0"><a href="#/nodes/${encodeURIComponent(n.public_key)}" class="analytics-link">${name}</a> ${role}${hs}${when}</div>`;
    }

    function severityBadge(count) {
      if (count === 0) return '<span style="color:var(--status-green)">✅ Unique</span>';
      if (count <= 2) return `<span style="color:var(--status-yellow)">⚠️ ${count} collision${count !== 1 ? 's' : ''}</span>`;
      return `<span style="color:var(--status-red)">🔴 ${count} collisions</span>`;
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
      const available = totalSpace - idx[b].size;

      if (available === 0) {
        const next = b < 3 ? (b + 1) + '-byte' : 'a different size';
        genResultEl.innerHTML = `<p style="color:var(--status-red);margin:0">No collision-free ${b}-byte prefixes available. Try ${next}.</p>`;
        return;
      }

      let prefix;
      if (b === 1) {
        // Enumerate all 256 options
        const free = [];
        for (let i = 0; i < totalSpace; i++) {
          const p = i.toString(16).toUpperCase().padStart(hexLen, '0');
          if (!idx[b].has(p)) free.push(p);
        }
        prefix = free[Math.floor(Math.random() * free.length)];
      } else {
        // Random sampling — with 2K used / 65K space, hit rate >96%
        let attempts = 0;
        do {
          prefix = Math.floor(Math.random() * totalSpace).toString(16).toUpperCase().padStart(hexLen, '0');
        } while (idx[b].has(prefix) && ++attempts < 500);
        // Fallback to enumeration if sampling kept hitting used prefixes
        if (idx[b].has(prefix)) {
          for (let i = 0; i < totalSpace; i++) {
            const p = i.toString(16).toUpperCase().padStart(hexLen, '0');
            if (!idx[b].has(p)) { prefix = p; break; }
          }
        }
      }

      genResultEl.innerHTML = `
        <div style="padding:12px 16px;border:1px solid var(--status-green);border-radius:6px;background:var(--bg-secondary,var(--bg))">
          <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
            <code class="mono" style="font-size:1.3em;font-weight:700;color:var(--status-green)">${prefix}</code>
            <span style="color:var(--status-green)">✅ No existing nodes use this prefix</span>
          </div>
          <div class="text-muted" style="font-size:0.85em;margin-top:6px">${available.toLocaleString()} of ${totalSpace.toLocaleString()} ${b}-byte prefixes are available.</div>
          <div style="margin-top:10px;display:flex;gap:8px;flex-wrap:wrap;align-items:center">
            <button id="ptRegenBtn" style="padding:5px 14px;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:4px;cursor:pointer;font-size:0.9em">Try another</button>
            <a href="https://agessaman.github.io/meshcore-web-keygen/?prefix=${prefix}" target="_blank" rel="noopener noreferrer"
              style="padding:5px 14px;background:var(--bg);color:var(--accent);border:1px solid var(--border);border-radius:4px;text-decoration:none;font-size:0.9em">
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

        return `<div class="rf-cell${isSelected ? ' rf-cell-selected' : ''}" data-observer="${obs.observer_id}" tabindex="0" role="button" aria-label="Observer ${name}, noise floor ${nf} dBm">
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
          <button class="rf-detail-close" aria-label="Close detail" title="Close">✕</button>
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
      const ts = t.toISOString().replace('T', ' ').replace(/\.\d+Z/, ' UTC');
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
      const label = t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
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
      var data = await (await fetch('/api/nodes/clock-skew')).json();
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

        el.innerHTML = '<h3 style="margin:0 0 10px">⏰ Clock Health</h3>' +
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

  registerPage('analytics', { init, destroy });
})();
