/* === MeshCore Analyzer — analytics.js (v2 — full nerd mode) === */
'use strict';

(function () {
  function esc(s) { return s ? String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;') : ''; }

  // --- SVG helpers ---
  function svgLine(points, color, w, h, pad, maxX, maxY) {
    return points.map((v, i) => {
      const x = pad + i * ((w - pad * 2) / Math.max(points.length - 1, 1));
      const y = h - pad - (v / Math.max(maxY, 1)) * (h - pad * 2);
      return `${x},${y}`;
    }).join(' ');
  }

  function sparkSvg(data, color, w = 120, h = 32) {
    if (!data.length) return '';
    const max = Math.max(...data, 1);
    const pts = data.map((v, i) => {
      const x = i * (w / Math.max(data.length - 1, 1));
      const y = h - 2 - (v / max) * (h - 4);
      return `${x},${y}`;
    }).join(' ');
    return `<svg viewBox="0 0 ${w} ${h}" style="width:${w}px;height:${h}px"><polyline points="${pts}" fill="none" stroke="${color}" stroke-width="1.5"/></svg>`;
  }

  function barChart(data, labels, colors, w = 800, h = 220, pad = 40) {
    const max = Math.max(...data, 1);
    const barW = Math.min((w - pad * 2) / data.length - 2, 30);
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px">`;
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

  function histogram(values, bins, color, w = 800, h = 180) {
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
          <div class="analytics-tabs" id="analyticsTabs">
            <button class="tab-btn active" data-tab="overview">Overview</button>
            <button class="tab-btn" data-tab="rf">RF / Signal</button>
            <button class="tab-btn" data-tab="topology">Topology</button>
            <button class="tab-btn" data-tab="channels">Channels</button>
            <button class="tab-btn" data-tab="hashsizes">Hash Sizes</button>
            <button class="tab-btn" data-tab="subpaths">Route Patterns</button>
          </div>
        </div>
        <div id="analyticsContent" class="analytics-content">
          <div class="text-center text-muted" style="padding:40px">Loading analytics…</div>
        </div>
      </div>`;

    // Tab handling
    document.getElementById('analyticsTabs').addEventListener('click', e => {
      const btn = e.target.closest('.tab-btn');
      if (!btn) return;
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      renderTab(btn.dataset.tab);
    });

    try {
      window._analyticsData = {};
      const [hashData, rfData, topoData, chanData] = await Promise.all([
        api('/analytics/hash-sizes'),
        api('/analytics/rf'),
        api('/analytics/topology'),
        api('/analytics/channels'),
      ]);
      window._analyticsData = { hashData, rfData, topoData, chanData };
      renderTab('overview');
    } catch (e) {
      document.getElementById('analyticsContent').innerHTML =
        `<div class="text-muted" style="padding:40px">Failed to load: ${e.message}</div>`;
    }
  }

  function renderTab(tab) {
    const el = document.getElementById('analyticsContent');
    const d = window._analyticsData;
    switch (tab) {
      case 'overview': renderOverview(el, d); break;
      case 'rf': renderRF(el, d.rfData); break;
      case 'topology': renderTopology(el, d.topoData); break;
      case 'channels': renderChannels(el, d.chanData); break;
      case 'hashsizes': renderHashSizes(el, d.hashData); break;
      case 'subpaths': renderSubpaths(el); break;
    }
    // Auto-apply column resizing to all analytics tables
    requestAnimationFrame(() => {
      el.querySelectorAll('.analytics-table').forEach((tbl, i) => {
        tbl.id = tbl.id || `analytics-tbl-${tab}-${i}`;
        makeColumnsResizable('#' + tbl.id, `meshcore-analytics-${tab}-${i}-col-widths`);
      });
    });
  }

  // ===================== OVERVIEW =====================
  function renderOverview(el, d) {
    const rf = d.rfData, topo = d.topoData, ch = d.chanData, hs = d.hashData;
    el.innerHTML = `
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-value">${rf.totalPackets.toLocaleString()}</div>
          <div class="stat-label">Total Packets</div>
          <div class="stat-spark">${sparkSvg(rf.packetsPerHour.map(h=>h.count), 'var(--accent)')}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${topo.uniqueNodes}</div>
          <div class="stat-label">Unique Nodes</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${rf.snr.avg.toFixed(1)} dB</div>
          <div class="stat-label">Avg SNR</div>
          <div class="stat-detail">${rf.snr.min.toFixed(1)} to ${rf.snr.max.toFixed(1)}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${rf.rssi.avg.toFixed(0)} dBm</div>
          <div class="stat-label">Avg RSSI</div>
          <div class="stat-detail">${rf.rssi.min} to ${rf.rssi.max}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${topo.avgHops.toFixed(1)}</div>
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
    const snrHist = histogram(rf.snrValues, 20, '#22c55e');
    const rssiHist = histogram(rf.rssiValues, 20, '#3b82f6');

    el.innerHTML = `
      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>📶 SNR Distribution</h3>
          <p class="text-muted">Signal-to-Noise Ratio (higher = cleaner signal)</p>
          ${snrHist.svg}
          <div class="rf-stats">
            <span>Min: <strong>${rf.snr.min.toFixed(1)} dB</strong></span>
            <span>Mean: <strong>${rf.snr.avg.toFixed(1)} dB</strong></span>
            <span>Median: <strong>${rf.snr.median.toFixed(1)} dB</strong></span>
            <span>Max: <strong>${rf.snr.max.toFixed(1)} dB</strong></span>
            <span>σ: <strong>${rf.snr.stddev.toFixed(1)} dB</strong></span>
          </div>
        </div>
        <div class="analytics-card flex-1">
          <h3>📡 RSSI Distribution</h3>
          <p class="text-muted">Received Signal Strength (closer to 0 = stronger)</p>
          ${rssiHist.svg}
          <div class="rf-stats">
            <span>Min: <strong>${rf.rssi.min} dBm</strong></span>
            <span>Mean: <strong>${rf.rssi.avg.toFixed(0)} dBm</strong></span>
            <span>Median: <strong>${rf.rssi.median} dBm</strong></span>
            <span>Max: <strong>${rf.rssi.max} dBm</strong></span>
            <span>σ: <strong>${rf.rssi.stddev.toFixed(1)} dBm</strong></span>
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
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:300px">`;
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
    const zones = [
      { label: 'Excellent', snr: [6, 15], rssi: [-80, -5], color: '#22c55e20' },
      { label: 'Good', snr: [0, 6], rssi: [-100, -80], color: '#f59e0b15' },
      { label: 'Weak', snr: [-12, 0], rssi: [-130, -100], color: '#ef444410' },
    ];
    zones.forEach(z => {
      const x1 = pad + (z.snr[0] - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      const x2 = pad + (z.snr[1] - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      const y1 = h - pad - (z.rssi[1] - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      const y2 = h - pad - (z.rssi[0] - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      svg += `<rect x="${x1}" y="${y1}" width="${x2-x1}" height="${y2-y1}" fill="${z.color}"/>`;
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
    let html = '<table class="analytics-table"><thead><tr><th>Type</th><th>Packets</th><th>Avg SNR</th><th>Min</th><th>Max</th><th>Distribution</th></tr></thead><tbody>';
    snrByType.forEach(t => {
      const barPct = Math.max(((t.avg - (-12)) / 27) * 100, 2);
      const color = t.avg > 6 ? '#22c55e' : t.avg > 0 ? '#f59e0b' : '#ef4444';
      html += `<tr>
        <td><strong>${t.name}</strong></td>
        <td>${t.count}</td>
        <td><strong>${t.avg.toFixed(1)} dB</strong></td>
        <td>${t.min.toFixed(1)}</td>
        <td>${t.max.toFixed(1)}</td>
        <td><div class="hash-bar-track" style="height:14px"><div class="hash-bar-fill" style="width:${barPct}%;background:${color};height:100%"></div></div></td>
      </tr>`;
    });
    return html + '</tbody></table>';
  }

  function renderSignalTimeline(data) {
    if (!data.length) return '<div class="text-muted">No data</div>';
    const w = 400, h = 160, pad = 35;
    const maxPkts = Math.max(...data.map(d => d.count), 1);
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:160px">`;
    // SNR line
    const snrPts = data.map((d, i) => {
      const x = pad + i * ((w - pad * 2) / Math.max(data.length - 1, 1));
      const y = h - pad - ((d.avgSnr + 12) / 27) * (h - pad * 2);
      return `${x},${y}`;
    }).join(' ');
    svg += `<polyline points="${snrPts}" fill="none" stroke="#22c55e" stroke-width="2"/>`;
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
    svg += `<div class="timeline-legend"><span><span class="legend-dot" style="background:#22c55e"></span>Avg SNR</span><span><span class="legend-dot" style="background:var(--accent);opacity:0.3"></span>Volume</span></div>`;
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
            <span>Avg: <strong>${topo.avgHops.toFixed(1)} hops</strong></span>
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
    let html = '<table class="analytics-table"><thead><tr><th>Node A</th><th>Node B</th><th>Co-appearances</th></tr></thead><tbody>';
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
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:160px">`;
    data.forEach(d => {
      const x = pad + (d.hops / maxHop) * (w - pad * 2);
      const y = h - pad - ((d.avgSnr + 12) / 27) * (h - pad * 2);
      const r = Math.min(Math.sqrt(d.count) * 1.5, 12);
      const color = d.avgSnr > 6 ? '#22c55e' : d.avgSnr > 0 ? '#f59e0b' : '#ef4444';
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
      <thead><tr><th>Node</th><th>Observers</th><th>Hop Distances</th></tr></thead><tbody>`;
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
  function renderChannels(el, ch) {
    el.innerHTML = `
      <div class="analytics-card">
        <h3>📻 Channel Activity</h3>
        <p class="text-muted">${ch.activeChannels} active channels, ${ch.decryptable} decryptable</p>
        <table class="analytics-table">
          <thead><tr><th>Channel</th><th>Hash</th><th>Messages</th><th>Unique Senders</th><th>Last Activity</th><th>Decrypted</th></tr></thead>
          <tbody>
            ${ch.channels.map(c => `<tr class="clickable-row" onclick="location.hash='#/channels?ch=${c.hash}'">
              <td><strong>${esc(c.name || 'Unknown')}</strong></td>
              <td class="mono">${c.hash}</td>
              <td>${c.messages}</td>
              <td>${c.senders}</td>
              <td>${timeAgo(c.lastActivity)}</td>
              <td>${c.encrypted ? '🔒' : '✅'}</td>
            </tr>`).join('')}
          </tbody>
        </table>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>💬 Messages / Hour by Channel</h3>
          ${renderChannelTimeline(ch.channelTimeline)}
        </div>
        <div class="analytics-card flex-1">
          <h3>🗣️ Top Senders</h3>
          ${renderTopSenders(ch.topSenders)}
        </div>
      </div>

      <div class="analytics-card">
        <h3>📊 Message Length Distribution</h3>
        ${ch.msgLengths.length ? histogram(ch.msgLengths, 20, '#8b5cf6').svg : '<div class="text-muted">No decrypted messages</div>'}
      </div>
    `;
  }

  function renderChannelTimeline(data) {
    if (!data.length) return '<div class="text-muted">No data</div>';
    const hours = [...new Set(data.map(d => d.hour))].sort();
    const channels = [...new Set(data.map(d => d.channel))];
    const colors = ['#ef4444','#22c55e','#3b82f6','#f59e0b','#8b5cf6','#ec4899','#14b8a6','#64748b'];
    const w = 600, h = 180, pad = 35;
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:180px">`;
    channels.forEach((ch, ci) => {
      const pts = hours.map((hr, i) => {
        const entry = data.find(d => d.hour === hr && d.channel === ch);
        const count = entry ? entry.count : 0;
        const max = Math.max(...data.map(d => d.count), 1);
        const x = pad + i * ((w - pad * 2) / Math.max(hours.length - 1, 1));
        const y = h - pad - (count / max) * (h - pad * 2);
        return `${x},${y}`;
      }).join(' ');
      svg += `<polyline points="${pts}" fill="none" stroke="${colors[ci % colors.length]}" stroke-width="1.5" opacity="0.8"/>`;
    });
    const step = Math.max(1, Math.floor(hours.length / 6));
    for (let i = 0; i < hours.length; i += step) {
      const x = pad + i * ((w - pad * 2) / Math.max(hours.length - 1, 1));
      svg += `<text x="${x}" y="${h-pad+14}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${hours[i].slice(11)}h</text>`;
    }
    svg += '</svg>';
    svg += `<div class="timeline-legend">${channels.map((ch, i) => `<span><span class="legend-dot" style="background:${colors[i % colors.length]}"></span>${esc(ch)}</span>`).join('')}</div>`;
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
      <div class="analytics-card">
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
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>📈 Hash Size Over Time</h3>
          ${renderHashTimeline(data.hourly)}
        </div>
      </div>

      <div class="analytics-card">
        <h3>Multi-Byte Hash Adopters</h3>
        <p class="text-muted">Nodes advertising with 2+ byte hash paths</p>
        ${data.multiByteNodes.length ? `
          <table class="analytics-table">
            <thead><tr><th>Node</th><th>Hash Size</th><th>Adverts</th><th>Last Seen</th></tr></thead>
            <tbody>
              ${data.multiByteNodes.map(n => `<tr class="clickable-row" onclick="location.hash='#/nodes/${n.pubkey ? encodeURIComponent(n.pubkey) : ''}'">
                <td><strong>${esc(n.name)}</strong></td>
                <td><span class="badge badge-hash-${n.hashSize}">${n.hashSize}-byte</span></td>
                <td>${n.packets}</td>
                <td>${timeAgo(n.lastSeen)}</td>
              </tr>`).join('')}
            </tbody>
          </table>
        ` : '<div class="text-muted" style="padding:16px">No multi-byte adopters found</div>'}
      </div>

      <div class="analytics-card">
        <h3>Top Path Hops</h3>
        <table class="analytics-table">
          <thead><tr><th>Hop</th><th>Node</th><th>Bytes</th><th>Appearances</th></tr></thead>
          <tbody>
            ${data.topHops.map(h => {
              const link = h.pubkey ? `#/nodes/${encodeURIComponent(h.pubkey)}` : `#/packets?search=${h.hex}`;
              return `<tr class="clickable-row" onclick="location.hash='${link}'">
              <td class="mono">${h.hex}</td>
              <td>${h.name ? `<strong>${esc(h.name)}</strong>` : '<span class="text-muted">unknown</span>'}</td>
              <td><span class="badge badge-hash-${h.size}">${h.size}-byte</span></td>
              <td>${h.count.toLocaleString()}</td>
            </tr>`;
            }).join('')}
          </tbody>
        </table>
      </div>

      <div class="analytics-card">
        <h3>1-Byte Collision Risk</h3>
        <div id="collisionList"><div class="text-muted" style="padding:8px">Loading…</div></div>
      </div>
    `;
    renderCollisions(data.topHops);
  }

  function renderHashTimeline(hourly) {
    if (!hourly.length) return '<div class="text-muted">Not enough data</div>';
    const w = 800, h = 180, pad = 35;
    const maxVal = Math.max(...hourly.map(h => Math.max(h[1] || 0, h[2] || 0, h[3] || 0)), 1);
    const colors = { 1: '#ef4444', 2: '#22c55e', 3: '#3b82f6' };
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:180px">`;
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

  async function renderCollisions(topHops) {
    const el = document.getElementById('collisionList');
    const oneByteHops = topHops.filter(h => h.size === 1);
    if (!oneByteHops.length) { el.innerHTML = '<div class="text-muted">No 1-byte hops</div>'; return; }
    try {
      const nodesData = await api('/nodes?limit=500');
      const nodes = nodesData.nodes || [];
      const collisions = [];
      for (const hop of oneByteHops) {
        const prefix = hop.hex.toLowerCase();
        const matches = nodes.filter(n => n.public_key.toLowerCase().startsWith(prefix));
        if (matches.length > 1) collisions.push({ hop: hop.hex, count: hop.count, matches });
      }
      if (!collisions.length) { el.innerHTML = '<div class="text-muted" style="padding:8px">No collisions detected</div>'; return; }
      el.innerHTML = `<table class="analytics-table">
        <thead><tr><th>Hop</th><th>Appearances</th><th>Colliding Nodes</th></tr></thead>
        <tbody>${collisions.map(c => `<tr>
          <td class="mono">${c.hop}</td>
          <td>${c.count.toLocaleString()}</td>
          <td>${c.matches.map(m => `<a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a>`).join(', ')}</td>
        </tr>`).join('')}</tbody>
      </table>`;
    } catch { el.innerHTML = '<div class="text-muted">Failed to load</div>'; }
  }

    async function renderSubpaths(el) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Analyzing route patterns…</div>';
    try {
      const [d2, d3, d4, d5] = await Promise.all([
        api('/analytics/subpaths?minLen=2&maxLen=2&limit=50'),
        api('/analytics/subpaths?minLen=3&maxLen=3&limit=30'),
        api('/analytics/subpaths?minLen=4&maxLen=4&limit=20'),
        api('/analytics/subpaths?minLen=5&maxLen=8&limit=15')
      ]);

      function renderTable(data, title) {
        if (!data.subpaths.length) return `<h4>${title}</h4><div class="text-muted">No data</div>`;
        const maxCount = data.subpaths[0]?.count || 1;
        return `<h4>${title}</h4>
          <p class="text-muted" style="margin:4px 0 8px">From ${data.totalPaths.toLocaleString()} paths with 2+ hops</p>
          <table class="analytics-table"><thead><tr>
            <th>#</th><th>Route</th><th>Occurrences</th><th>% of paths</th><th>Frequency</th>
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
              <td><div style="background:${hasSelfLoop ? '#f59e0b' : 'var(--accent,#3b82f6)'};height:14px;border-radius:3px;width:${barW}%;opacity:0.7"></div></td>
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
              <input type="checkbox" id="hideCollisions" ${localStorage.getItem('subpath-hide-collisions') === '1' ? 'checked' : ''}> Hide likely prefix collisions (self-loops)
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
      const data = await api('/analytics/subpath-detail?hops=' + encodeURIComponent(hopsStr));
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
      L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', { maxZoom: 18 }).addTo(map);

      const latlngs = [];
      nodesWithLoc.forEach((n, i) => {
        const ll = [n.lat, n.lon];
        latlngs.push(ll);
        const isEnd = i === 0 || i === nodesWithLoc.length - 1;
        L.circleMarker(ll, {
          radius: isEnd ? 8 : 5,
          color: isEnd ? (i === 0 ? '#22c55e' : '#ef4444') : '#f59e0b',
          fillColor: isEnd ? (i === 0 ? '#22c55e' : '#ef4444') : '#f59e0b',
          fillOpacity: 0.9, weight: 2
        }).bindTooltip(n.name, { permanent: false }).addTo(map);
      });

      L.polyline(latlngs, { color: '#f59e0b', weight: 3, dashArray: '8,6', opacity: 0.8 }).addTo(map);
      map.fitBounds(L.latLngBounds(latlngs).pad(0.3));
    }
  }

function destroy() { delete window._analyticsData; }

  registerPage('analytics', { init, destroy });
})();
