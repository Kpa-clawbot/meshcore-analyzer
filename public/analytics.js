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
          <div class="analytics-tabs" id="analyticsTabs">
            <button class="tab-btn active" data-tab="overview">Overview</button>
            <button class="tab-btn" data-tab="rf">RF / Signal</button>
            <button class="tab-btn" data-tab="topology">Topology</button>
            <button class="tab-btn" data-tab="channels">Channels</button>
            <button class="tab-btn" data-tab="hashsizes">Hash Stats</button>
            <button class="tab-btn" data-tab="collisions">Hash Issues</button>
            <button class="tab-btn" data-tab="subpaths">Route Patterns</button>
            <button class="tab-btn" data-tab="nodes">Nodes</button>
            <button class="tab-btn" data-tab="distance">Distance</button>
          </div>
        </div>
        <div id="analyticsContent" class="analytics-content">
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

    loadAnalytics();
  }

  let _currentTab = 'overview';

  async function loadAnalytics() {
    try {
      _analyticsData = {};
      const rqs = RegionFilter.regionQueryString();
      const sep = rqs ? '?' + rqs.slice(1) : '';
      const [hashData, rfData, topoData, chanData] = await Promise.all([
        api('/analytics/hash-sizes' + sep, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/rf' + sep, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/topology' + sep, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/channels' + sep, { ttl: CLIENT_TTL.analyticsRF }),
      ]);
      _analyticsData = { hashData, rfData, topoData, chanData };
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
      case 'collisions': await renderCollisionTab(el, d.hashData); break;
      case 'subpaths': await renderSubpaths(el); break;
      case 'nodes': await renderNodesTab(el); break;
      case 'distance': await renderDistanceTab(el); break;
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
      const opacity = Math.max(0.3, 1 - ring.hops * 0.06);
      const nodeLinks = ring.nodes.slice(0, 8).map(n => {
        const label = n.name ? `<a href="#/nodes/${encodeURIComponent(n.pubkey)}" class="analytics-link">${esc(n.name)}</a>` : `<span class="mono">${n.hop}</span>`;
        const detail = n.distRange ? ` <span class="text-muted">(${n.distRange})</span>` : '';
        return label + detail;
      }).join(', ');
      const extra = ring.nodes.length > 8 ? ` <span class="text-muted">+${ring.nodes.length - 8} more</span>` : '';
      html += `<div class="reach-ring" style="opacity:${opacity}">
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
      const opacity = Math.max(0.3, 1 - (+dist) * 0.06);
      const nodeLinks = nodes.slice(0, 10).map(n => {
        const label = n.name
          ? `<a href="#/nodes/${encodeURIComponent(n.pubkey)}" class="analytics-link">${esc(n.name)}</a>`
          : `<span class="mono">${n.hop}</span>`;
        return label + ` <span class="text-muted">via ${esc(n.observer_name)}</span>`;
      }).join(', ');
      const extra = nodes.length > 10 ? ` <span class="text-muted">+${nodes.length - 10} more</span>` : '';
      html += `<div class="reach-ring" style="opacity:${opacity}">
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

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>Multi-Byte Hash Adopters</h3>
          <p class="text-muted">Nodes advertising with 2+ byte hash paths</p>
        ${data.multiByteNodes.length ? `
          <table class="analytics-table">
            <thead><tr><th scope="col">Node</th><th scope="col">Hash Size</th><th scope="col">Adverts</th><th scope="col">Last Seen</th></tr></thead>
            <tbody>
              ${data.multiByteNodes.map(n => `<tr class="clickable-row" data-action="navigate" data-value="#/nodes/${n.pubkey ? encodeURIComponent(n.pubkey) : ''}" tabindex="0" role="row">
                <td><strong>${esc(n.name)}</strong></td>
                <td><span class="badge badge-hash-${n.hashSize}">${n.hashSize}-byte</span></td>
                <td>${n.packets}</td>
                <td>${timeAgo(n.lastSeen)}</td>
              </tr>`).join('')}
            </tbody>
          </table>
        ` : '<div class="text-muted" style="padding:16px">No multi-byte adopters found</div>'}
        </div>

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

  async function renderCollisionTab(el, data) {
    el.innerHTML = `
      <nav id="hashIssuesToc" style="display:flex;gap:12px;margin-bottom:12px;font-size:13px;flex-wrap:wrap">
        <a href="#/analytics?tab=collisions&section=inconsistentHashSection" style="color:var(--accent)">⚠️ Inconsistent Sizes</a>
        <span style="color:var(--border)">|</span>
        <a href="#/analytics?tab=collisions&section=hashMatrixSection" style="color:var(--accent)">🔢 Hash Matrix</a>
        <span style="color:var(--border)">|</span>
        <a href="#/analytics?tab=collisions&section=collisionRiskSection" style="color:var(--accent)">💥 Collision Risk</a>
      </nav>

      <div class="analytics-card" id="inconsistentHashSection">
        <div style="display:flex;justify-content:space-between;align-items:center"><h3 style="margin:0">⚠️ Inconsistent Hash Sizes</h3><a href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a></div>
        <p class="text-muted" style="margin:4px 0 8px;font-size:0.8em">Nodes sending adverts with varying hash sizes. Caused by a <a href="https://github.com/meshcore-dev/MeshCore/commit/fcfdc5f" target="_blank" style="color:var(--accent)">bug</a> where automatic adverts ignored the configured multibyte path setting. Fixed in <a href="https://github.com/meshcore-dev/MeshCore/releases/tag/repeater-v1.14.1" target="_blank" style="color:var(--accent)">repeater v1.14.1</a>.</p>
        <div id="inconsistentHashList"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading…</div></div>
      </div>

      <div class="analytics-card" id="hashMatrixSection">
        <div style="display:flex;justify-content:space-between;align-items:center"><h3 style="margin:0">🔢 1-Byte Hash Usage Matrix</h3><a href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a></div>
        <p class="text-muted" style="margin:4px 0 8px;font-size:0.8em">Click a cell to see which nodes share that prefix. Green = available, yellow = taken, red = collision.</p>
        <div id="hashMatrix"></div>
      </div>

      <div class="analytics-card" id="collisionRiskSection">
        <div style="display:flex;justify-content:space-between;align-items:center"><h3 style="margin:0">💥 1-Byte Collision Risk</h3><a href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a></div>
        <div id="collisionList"><div class="text-muted" style="padding:8px">Loading…</div></div>
      </div>
    `;
    let allNodes = [];
    try { const nd = await api('/nodes?limit=2000' + RegionFilter.regionQueryString(), { ttl: CLIENT_TTL.nodeList }); allNodes = nd.nodes || []; } catch {}

    // Render inconsistent hash sizes
    const inconsistent = allNodes.filter(n => n.hash_size_inconsistent);
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

    // Only repeaters matter for routing — filter out non-repeaters for collision analysis
    const repeaterNodes = allNodes.filter(n => n.role === 'repeater');
    renderHashMatrix(data.topHops, repeaterNodes);
    renderCollisions(data.topHops, repeaterNodes);
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

  async function renderHashMatrix(topHops, allNodes) {
    const el = document.getElementById('hashMatrix');

    // Build prefix → node count map
    const prefixNodes = {};
    for (let i = 0; i < 256; i++) {
      const hex = i.toString(16).padStart(2, '0').toUpperCase();
      prefixNodes[hex] = allNodes.filter(n => n.public_key.toUpperCase().startsWith(hex));
    }

    const nibbles = '0123456789ABCDEF'.split('');
    const cellSize = 36;
    const headerSize = 24;

    let html = `<div style="display:flex;gap:16px;flex-wrap:wrap"><div class="hash-matrix-scroll"><table class="hash-matrix-table" style="border-collapse:collapse;font-size:12px;font-family:monospace">`;
    html += `<tr><td style="width:${headerSize}px"></td>`;
    for (const n of nibbles) {
      html += `<td style="width:${cellSize}px;text-align:center;padding:2px 0;font-weight:bold;color:var(--text-muted)">${n}</td>`;
    }
    html += '</tr>';

    for (let hi = 0; hi < 16; hi++) {
      html += `<tr><td style="text-align:right;padding-right:4px;font-weight:bold;color:var(--text-muted)">${nibbles[hi]}</td>`;
      for (let lo = 0; lo < 16; lo++) {
        const hex = nibbles[hi] + nibbles[lo];
        const nodes = prefixNodes[hex] || [];
        const count = nodes.length;
        let bg, color;
        if (count === 0) {
          bg = 'var(--card-bg)'; color = 'var(--text-muted)'; // empty — subtle
        } else if (count === 1) {
          bg = '#dcfce7'; color = '#166534'; // light green — taken, no collision
        } else {
          // 2+ nodes: orange→red
          const t = Math.min((count - 2) / 4, 1);
          const r = Math.round(220 + 35 * t);
          const g = Math.round(120 * (1 - t));
          bg = `rgb(${r},${g},30)`; color = '#fff';
        }
        const status = count === 0 ? 'available' : count === 1 ? `1 node: ${nodes[0].name || nodes[0].public_key.slice(0,12)}` : `${count} nodes — COLLISION`;
        const cellText = count === 0 ? `<span style="font-size:11px">${hex}</span>` : count >= 2 ? `<strong>${count >= 3 ? '3+' : count}</strong>` : String(count);
        html += `<td class="hash-cell${count ? ' hash-active' : ''}" data-hex="${hex}" style="width:${cellSize}px;height:${cellSize}px;text-align:center;background:${bg};color:${color};border:1px solid var(--border);cursor:${count ? 'pointer' : 'default'};font-size:13px;font-weight:${count >= 2 ? '700' : '400'}" title="0x${hex}: ${status}">${cellText}</td>`;
      }
      html += '</tr>';
    }
    html += '</table></div>';
    html += `<div id="hashDetail" style="flex:1;min-width:200px;max-width:400px;font-size:0.85em"></div></div>
    <div style="margin-top:8px;font-size:0.8em;display:flex;gap:16px;align-items:center">
      <span><span class="legend-swatch" style="background:var(--card-bg);border:1px solid var(--border)"></span> 0 — Available</span>
      <span><span class="legend-swatch" style="background:#dcfce7"></span> 1 — One node</span>
      <span><span class="legend-swatch" style="background:rgb(200,80,30)"></span> 2 — Two nodes (collision)</span>
      <span><span class="legend-swatch" style="background:rgb(200,0,30)"></span> 3+ — Three+ nodes (collision)</span>
    </div>`;
    el.innerHTML = html;

    // Click handler for cells
    el.querySelectorAll('.hash-active').forEach(td => {
      td.addEventListener('click', () => {
        const hex = td.dataset.hex.toUpperCase();
        const matches = prefixNodes[hex] || [];
        const detail = document.getElementById('hashDetail');
        if (!matches.length) {
          detail.innerHTML = `<strong class="mono">0x${hex}</strong><br><span class="text-muted">No known nodes</span>`;
          return;
        }
        detail.innerHTML = `<strong class="mono" style="font-size:1.1em">0x${hex}</strong> — ${matches.length} node${matches.length !== 1 ? 's' : ''}` +
          `<div style="margin-top:8px">${matches.map(m => {
            const coords = (m.lat && m.lon && !(m.lat === 0 && m.lon === 0))
              ? `<span class="text-muted" style="font-size:0.8em">(${m.lat.toFixed(2)}, ${m.lon.toFixed(2)})</span>`
              : '<span class="text-muted" style="font-size:0.8em">(no coords)</span>';
            const role = m.role ? `<span class="badge" style="font-size:0.7em;padding:1px 4px;background:var(--border)">${esc(m.role)}</span> ` : '';
            return `<div style="padding:3px 0">${role}<a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a> ${coords}</div>`;
          }).join('')}</div>`;
        el.querySelectorAll('.hash-selected').forEach(c => c.classList.remove('hash-selected'));
        td.classList.add('hash-selected');
      });
    });
  }

  async function renderCollisions(topHops, allNodes) {
    const el = document.getElementById('collisionList');
    const oneByteHops = topHops.filter(h => h.size === 1);
    if (!oneByteHops.length) { el.innerHTML = '<div class="text-muted">No 1-byte hops</div>'; return; }
    try {
      const nodes = allNodes;
      const collisions = [];
      for (const hop of oneByteHops) {
        const prefix = hop.hex.toLowerCase();
        const matches = nodes.filter(n => n.public_key.toLowerCase().startsWith(prefix));
        if (matches.length > 1) {
          // Calculate pairwise distances for classification
          const withCoords = matches.filter(m => m.lat && m.lon && !(m.lat === 0 && m.lon === 0));
          let maxDistKm = 0;
          let classification = 'unknown';
          if (withCoords.length >= 2) {
            for (let i = 0; i < withCoords.length; i++) {
              for (let j = i + 1; j < withCoords.length; j++) {
                const dLat = (withCoords[i].lat - withCoords[j].lat) * 111;
                const dLon = (withCoords[i].lon - withCoords[j].lon) * 85;
                const d = Math.sqrt(dLat * dLat + dLon * dLon);
                if (d > maxDistKm) maxDistKm = d;
              }
            }
            if (maxDistKm < 50) classification = 'local';
            else if (maxDistKm < 200) classification = 'regional';
            else classification = 'distant';
          } else if (withCoords.length < 2) {
            classification = 'incomplete';
          }
          collisions.push({ hop: hop.hex, count: hop.count, matches, maxDistKm, classification, withCoords: withCoords.length });
        }
      }
      if (!collisions.length) { el.innerHTML = '<div class="text-muted" style="padding:8px">No collisions detected</div>'; return; }
      
      // Sort: local first (most likely to collide), then regional, distant, incomplete
      const classOrder = { local: 0, regional: 1, distant: 2, incomplete: 3, unknown: 4 };
      collisions.sort((a, b) => classOrder[a.classification] - classOrder[b.classification] || b.count - a.count);

      el.innerHTML = `<table class="analytics-table">
        <thead><tr><th scope="col">Hop</th><th scope="col">Appearances</th><th scope="col">Max Distance</th><th scope="col">Assessment</th><th scope="col">Colliding Nodes</th></tr></thead>
        <tbody>${collisions.map(c => {
          let badge, tooltip;
          if (c.classification === 'local') {
            badge = '<span class="badge" style="background:var(--status-green);color:#fff" title="All nodes within 50km — likely true collision, same RF neighborhood">🏘️ Local</span>';
            tooltip = 'Nodes close enough for direct RF — probably genuine prefix collision';
          } else if (c.classification === 'regional') {
            badge = '<span class="badge" style="background:var(--status-yellow);color:#fff" title="Nodes 50–200km apart — edge of LoRa range, could be atmospheric">⚡ Regional</span>';
            tooltip = 'At edge of 915MHz range — could indicate atmospheric ducting or hilltop-to-hilltop links';
          } else if (c.classification === 'distant') {
            badge = '<span class="badge" style="background:var(--status-red);color:#fff" title="Nodes >200km apart — beyond typical 915MHz range">🌐 Distant</span>';
            tooltip = 'Beyond typical LoRa range — likely internet bridging, MQTT gateway, or separate mesh networks sharing prefix';
          } else {
            badge = '<span class="badge" style="background:#6b7280;color:#fff">❓ Unknown</span>';
            tooltip = 'Not enough coordinate data to classify';
          }
          const distStr = c.withCoords >= 2 ? `${Math.round(c.maxDistKm)} km` : '<span class="text-muted">—</span>';
          return `<tr>
            <td class="mono">${c.hop}</td>
            <td>${c.count.toLocaleString()}</td>
            <td>${distStr}</td>
            <td title="${tooltip}">${badge}</td>
            <td>${c.matches.map(m => {
              const loc = (m.lat && m.lon && !(m.lat === 0 && m.lon === 0)) 
                ? ` <span class="text-muted" style="font-size:0.75em">(${m.lat.toFixed(2)}, ${m.lon.toFixed(2)})</span>` 
                : ' <span class="text-muted" style="font-size:0.75em">(no coords)</span>';
              return `<a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a>${loc}`;
            }).join('<br>')}</td>
          </tr>`;
        }).join('')}</tbody>
      </table>
      <div class="text-muted" style="padding:8px;font-size:0.8em">
        <strong>🏘️ Local</strong> &lt;50km: true prefix collision, same mesh area &nbsp;
        <strong>⚡ Regional</strong> 50–200km: edge of LoRa range, possible atmospheric propagation &nbsp;
        <strong>🌐 Distant</strong> &gt;200km: beyond 915MHz range — internet bridge, MQTT gateway, or separate networks
      </div>`;
    } catch { el.innerHTML = '<div class="text-muted">Failed to load</div>'; }
  }

    async function renderSubpaths(el) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Analyzing route patterns…</div>';
    try {
      const rq = RegionFilter.regionQueryString();
      const [d2, d3, d4, d5] = await Promise.all([
        api('/analytics/subpaths?minLen=2&maxLen=2&limit=50' + rq, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/subpaths?minLen=3&maxLen=3&limit=30' + rq, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/subpaths?minLen=4&maxLen=4&limit=20' + rq, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/subpaths?minLen=5&maxLen=8&limit=15' + rq, { ttl: CLIENT_TTL.analyticsRF })
      ]);

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
                const dLat = (a.lat - b.lat) * 111;
                const dLon = (a.lon - b.lon) * 85;
                const km = Math.sqrt(dLat*dLat + dLon*dLon);
                total += km;
                const cls = km > 200 ? 'color:var(--status-red);font-weight:bold' : km > 50 ? 'color:var(--status-yellow)' : 'color:var(--status-green)';
                dists.push(`<div style="padding:2px 0"><span style="${cls}">${km < 1 ? (km*1000).toFixed(0)+'m' : km.toFixed(1)+'km'}</span> <span class="text-muted">${esc(a.name)} → ${esc(b.name)}</span></div>`);
              } else {
                dists.push(`<div style="padding:2px 0"><span class="text-muted">? ${esc(a.name)} → ${esc(b.name)} (no coords)</span></div>`);
              }
            }
            if (dists.length > 1) dists.push(`<div style="padding:4px 0;border-top:1px solid var(--border);margin-top:4px"><strong>Total: ${total < 1 ? (total*1000).toFixed(0)+'m' : total.toFixed(1)+'km'}</strong></div>`);
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
      const [nodesResp, bulkHealth, netStatus] = await Promise.all([
        api('/nodes?limit=200&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList }),
        api('/nodes/bulk-health?limit=50' + rq, { ttl: CLIENT_TTL.analyticsRF }),
        api('/nodes/network-status' + (rq ? '?' + rq.slice(1) : ''), { ttl: CLIENT_TTL.analyticsRF })
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

      // Use server-computed status across ALL nodes
      const { active, degraded, silent, total: totalNodes, roleCounts } = netStatus;

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
        <div class="stat-card"><div class="stat-value">${s.avgDist} km</div><div class="stat-label">Avg Hop Distance</div></div>
        <div class="stat-card"><div class="stat-value">${s.maxDist} km</div><div class="stat-label">Max Hop Distance</div></div>
      </div>`;

      // Category stats
      const cats = data.catStats;
      html += `<div class="analytics-section"><h3>Distance by Link Type</h3><table class="data-table"><thead><tr><th scope="col">Type</th><th scope="col">Count</th><th scope="col">Avg (km)</th><th scope="col">Median (km)</th><th scope="col">Min (km)</th><th scope="col">Max (km)</th></tr></thead><tbody>`;
      for (const [cat, st] of Object.entries(cats)) {
        if (!st.count) continue;
        html += `<tr><td><strong>${esc(cat)}</strong></td><td>${st.count.toLocaleString()}</td><td>${st.avg}</td><td>${st.median}</td><td>${st.min}</td><td>${st.max}</td></tr>`;
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
      html += `<div class="analytics-section"><h3>🏆 Top 20 Longest Hops</h3><table class="data-table"><thead><tr><th scope="col">#</th><th scope="col">From</th><th scope="col">To</th><th scope="col">Distance (km)</th><th scope="col">Type</th><th scope="col">SNR</th><th scope="col">Packet</th><th scope="col"></th></tr></thead><tbody>`;
      const top20 = data.topHops.slice(0, 20);
      top20.forEach((h, i) => {
        const fromLink = h.fromPk ? `<a href="#/nodes/${encodeURIComponent(h.fromPk)}" class="analytics-link">${esc(h.fromName)}</a>` : esc(h.fromName || '?');
        const toLink = h.toPk ? `<a href="#/nodes/${encodeURIComponent(h.toPk)}" class="analytics-link">${esc(h.toName)}</a>` : esc(h.toName || '?');
        const snr = h.snr != null ? h.snr + ' dB' : '<span class="text-muted">—</span>';
        const pktLink = h.hash ? `<a href="#/packet/${encodeURIComponent(h.hash)}" class="analytics-link mono" style="font-size:0.85em">${esc(h.hash.slice(0, 12))}…</a>` : '—';
        const mapBtn = h.fromPk && h.toPk ? `<button class="btn-icon dist-map-hop" data-from="${esc(h.fromPk)}" data-to="${esc(h.toPk)}" title="View on map">🗺️</button>` : '';
        html += `<tr><td>${i+1}</td><td>${fromLink}</td><td>${toLink}</td><td><strong>${h.dist}</strong></td><td>${esc(h.type)}</td><td>${snr}</td><td>${pktLink}</td><td>${mapBtn}</td></tr>`;
      });
      html += `</tbody></table></div>`;

      // Top paths
      if (data.topPaths.length) {
        html += `<div class="analytics-section"><h3>🛤️ Top 10 Longest Multi-Hop Paths</h3><table class="data-table"><thead><tr><th scope="col">#</th><th scope="col">Total Distance (km)</th><th scope="col">Hops</th><th scope="col">Route</th><th scope="col">Packet</th><th scope="col"></th></tr></thead><tbody>`;
        data.topPaths.slice(0, 10).forEach((p, i) => {
          const route = p.hops.map(h => esc(h.fromName)).concat(esc(p.hops[p.hops.length-1].toName)).join(' → ');
          const pktLink = p.hash ? `<a href="#/packet/${encodeURIComponent(p.hash)}" class="analytics-link mono" style="font-size:0.85em">${esc(p.hash.slice(0, 12))}…</a>` : '—';
          // Collect all unique pubkeys in path order
          const pathPks = [];
          p.hops.forEach(h => { if (h.fromPk && !pathPks.includes(h.fromPk)) pathPks.push(h.fromPk); });
          if (p.hops.length && p.hops[p.hops.length-1].toPk) { const last = p.hops[p.hops.length-1].toPk; if (!pathPks.includes(last)) pathPks.push(last); }
          const mapBtn = pathPks.length >= 2 ? `<button class="btn-icon dist-map-path" data-hops='${JSON.stringify(pathPks)}' title="View on map">🗺️</button>` : '';
          html += `<tr><td>${i+1}</td><td><strong>${p.totalDist}</strong></td><td>${p.hopCount}</td><td style="font-size:0.9em">${route}</td><td>${pktLink}</td><td>${mapBtn}</td></tr>`;
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

function destroy() { _analyticsData = {}; _channelData = null; }

  // Expose for testing
  if (typeof window !== 'undefined') {
    window._analyticsSortChannels = sortChannels;
    window._analyticsLoadChannelSort = loadChannelSort;
    window._analyticsSaveChannelSort = saveChannelSort;
    window._analyticsChannelTbodyHtml = channelTbodyHtml;
    window._analyticsChannelTheadHtml = channelTheadHtml;
  }

  registerPage('analytics', { init, destroy });
})();
