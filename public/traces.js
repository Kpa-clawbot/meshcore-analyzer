/* === MeshCore Analyzer — traces.js === */
'use strict';

(function () {
  let currentHash = null;
  let traceData = [];
  let packetMeta = null;
  function init(app, routeParam) {
    // Check URL for pre-filled hash — support both route param and query param
    const params = new URLSearchParams(location.hash.split('?')[1] || '');
    const urlHash = routeParam || params.get('hash') || '';

    app.innerHTML = `
      <div class="traces-page">
        <div class="page-header">
          <h2>🔍 Packet Trace</h2>
        </div>
        <div class="trace-search">
          <input type="text" id="traceHashInput" placeholder="Enter packet hash…" value="${urlHash}" aria-label="Packet hash to trace">
          <button class="btn-primary" id="traceBtn">Trace</button>
        </div>
        <div id="traceResults"></div>
      </div>`;

    document.getElementById('traceBtn').addEventListener('click', doTrace);
    document.getElementById('traceHashInput').addEventListener('keydown', (e) => {
      if (e.key === 'Enter') doTrace();
    });

    if (urlHash) doTrace();
  }

  function destroy() {
    currentHash = null;
    traceData = [];
    packetMeta = null;
  }

  function obsLabel(t) {
    return t.observer_name || (t.observer && t.observer.length > 16 ? t.observer.slice(0, 12) + '…' : t.observer) || '—';
  }

  function obsLink(t) {
    const label = escapeHtml(obsLabel(t));
    if (!t.observer) return label;
    return `<a href="#/observers/${encodeURIComponent(t.observer)}" style="color:var(--accent);text-decoration:none;" title="${escapeHtml(t.observer)}">${label}</a>`;
  }

  async function doTrace() {
    const input = document.getElementById('traceHashInput');
    const hash = input.value.trim();
    if (!hash) return;
    currentHash = hash;

    const results = document.getElementById('traceResults');
    results.innerHTML = '<div class="text-center text-muted" style="padding:40px">Tracing…</div>';

    try {
      const [traceResp, pktResp] = await Promise.all([
        api(`/traces/${encodeURIComponent(hash)}`),
        api(`/packets?hash=${encodeURIComponent(hash)}&limit=50`)
      ]);

      traceData = traceResp.traces || [];
      const packets = pktResp.packets || [];

      if (traceData.length === 0 && packets.length === 0) {
        results.innerHTML = '<div class="trace-empty">No observations found for this packet hash.</div>';
        return;
      }

      // Extract ALL unique paths from observations
      const allPaths = [];
      for (const t of traceData) {
        try {
          const hops = JSON.parse(t.path_json || '[]');
          if (hops.length > 0) allPaths.push({ hops, observer: obsLabel(t) });
        } catch {}
      }
      // Fallback to packet-level path
      if (allPaths.length === 0) {
        for (const p of packets) {
          try {
            const hops = JSON.parse(p.path_json || '[]');
            if (hops.length > 0) { allPaths.push({ hops, observer: 'packet' }); break; }
          } catch {}
        }
      }

      // Get packet type info from first packet
      packetMeta = packets[0] || null;
      let decoded = null;
      if (packetMeta) {
        try { decoded = JSON.parse(packetMeta.decoded_json); } catch {}
      }

      renderResults(results, allPaths, decoded);
    } catch (e) {
      results.innerHTML = `<div class="trace-empty" style="color:#ef4444">Error: ${e.message}</div>`;
    }
  }

  function renderResults(container, allPaths, decoded) {
    const uniqueObservers = [...new Set(traceData.map(t => t.observer))];
    const typeName = packetMeta ? payloadTypeName(packetMeta.payload_type) : '—';
    const typeClass = packetMeta ? payloadTypeColor(packetMeta.payload_type) : 'unknown';

    // Compute timing
    let t0 = null, tLast = null;
    if (traceData.length > 0) {
      const times = traceData.map(t => new Date(t.time).getTime()).filter(t => !isNaN(t));
      if (times.length) {
        t0 = Math.min(...times);
        tLast = Math.max(...times);
      }
    }
    const spreadMs = (t0 !== null && tLast !== null) ? tLast - t0 : 0;

    container.innerHTML = `
      <div class="trace-summary">
        <div class="trace-stat">
          <div class="trace-stat-value">${uniqueObservers.length}</div>
          <div class="trace-stat-label">Observers</div>
        </div>
        <div class="trace-stat">
          <div class="trace-stat-value">${traceData.length}</div>
          <div class="trace-stat-label">Observations</div>
        </div>
        <div class="trace-stat">
          <div class="trace-stat-value">${spreadMs > 0 ? (spreadMs / 1000).toFixed(1) + 's' : '—'}</div>
          <div class="trace-stat-label">Time Spread</div>
        </div>
        <div class="trace-stat">
          <div class="trace-stat-value"><span class="badge badge-${typeClass}">${typeName}</span></div>
          <div class="trace-stat-label">Packet Type</div>
        </div>
      </div>

      ${allPaths.length > 0 ? renderPathGraph(allPaths) : ''}
      ${traceData.length > 0 ? renderTimeline(t0, spreadMs) : ''}
    `;
  }

  function renderPathGraph(allPaths) {
    // Collect unique nodes and edges across all observed paths
    const nodeSet = new Set();
    const edgeMap = new Map(); // "from→to" => Set of observer labels
    nodeSet.add('Origin');
    nodeSet.add('Dest');

    for (const { hops, observer } of allPaths) {
      const chain = ['Origin', ...hops, 'Dest'];
      for (let i = 0; i < chain.length - 1; i++) {
        nodeSet.add(chain[i]);
        nodeSet.add(chain[i + 1]);
        const key = chain[i] + '→' + chain[i + 1];
        if (!edgeMap.has(key)) edgeMap.set(key, new Set());
        edgeMap.get(key).add(observer);
      }
    }

    const nodes = [...nodeSet];
    // Assign positions: lay out nodes left to right by their earliest appearance in any path
    const order = new Map();
    order.set('Origin', 0);
    let maxCol = 0;
    for (const { hops } of allPaths) {
      const chain = ['Origin', ...hops, 'Dest'];
      for (let i = 0; i < chain.length; i++) {
        if (!order.has(chain[i])) {
          order.set(chain[i], i);
        }
        maxCol = Math.max(maxCol, i);
      }
    }
    order.set('Dest', maxCol);

    // Group nodes by column for vertical stacking
    const colGroups = new Map();
    for (const [node, col] of order) {
      if (!colGroups.has(col)) colGroups.set(col, []);
      colGroups.get(col).push(node);
    }

    const colCount = maxCol + 1;
    const svgW = Math.max(600, colCount * 140);
    const maxRows = Math.max(...[...colGroups.values()].map(g => g.length));
    const svgH = Math.max(120, maxRows * 60 + 40);
    const colSpacing = svgW / (colCount + 1);

    // Compute node positions
    const nodePos = new Map();
    for (const [col, group] of colGroups) {
      const rowSpacing = svgH / (group.length + 1);
      group.forEach((node, i) => {
        nodePos.set(node, { x: (col + 1) * colSpacing, y: (i + 1) * rowSpacing });
      });
    }

    // Colors for edges (cycle through)
    const edgeColors = ['#3b82f6', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#ec4899', '#06b6d4', '#84cc16'];
    const observerColorMap = new Map();
    let colorIdx = 0;
    for (const obsSet of edgeMap.values()) {
      for (const obs of obsSet) {
        if (!observerColorMap.has(obs)) {
          observerColorMap.set(obs, edgeColors[colorIdx % edgeColors.length]);
          colorIdx++;
        }
      }
    }

    // Build SVG
    let edgesSvg = '';
    for (const [key, observers] of edgeMap) {
      const [from, to] = key.split('→');
      const p1 = nodePos.get(from);
      const p2 = nodePos.get(to);
      if (!p1 || !p2) continue;
      const obsArr = [...observers];
      const thickness = Math.min(obsArr.length, 6);
      // Use first observer's color, show count as tooltip
      const color = observerColorMap.get(obsArr[0]) || '#6b7280';
      const title = obsArr.length > 1 ? `${obsArr.length} observers: ${obsArr.join(', ')}` : obsArr[0];
      edgesSvg += `<line x1="${p1.x}" y1="${p1.y}" x2="${p2.x}" y2="${p2.y}" stroke="${color}" stroke-width="${thickness}" stroke-opacity="0.6"><title>${escapeHtml(title)}</title></line>`;
      // Arrowhead
      const angle = Math.atan2(p2.y - p1.y, p2.x - p1.x);
      const arrowLen = 8;
      const ax = p2.x - 20 * Math.cos(angle);
      const ay = p2.y - 20 * Math.sin(angle);
      const a1x = ax - arrowLen * Math.cos(angle - 0.4);
      const a1y = ay - arrowLen * Math.sin(angle - 0.4);
      const a2x = ax - arrowLen * Math.cos(angle + 0.4);
      const a2y = ay - arrowLen * Math.sin(angle + 0.4);
      edgesSvg += `<polygon points="${ax},${ay} ${a1x},${a1y} ${a2x},${a2y}" fill="${color}" opacity="0.8"/>`;
    }

    let nodesSvg = '';
    for (const [node, pos] of nodePos) {
      const isEndpoint = node === 'Origin' || node === 'Dest';
      const r = isEndpoint ? 18 : 14;
      const fill = isEndpoint ? 'var(--accent, #3b82f6)' : 'var(--surface-2, #374151)';
      const stroke = isEndpoint ? 'var(--accent, #3b82f6)' : 'var(--border, #4b5563)';
      const label = isEndpoint ? node : node;
      nodesSvg += `<circle cx="${pos.x}" cy="${pos.y}" r="${r}" fill="${fill}" stroke="${stroke}" stroke-width="2"/>`;
      nodesSvg += `<text x="${pos.x}" y="${pos.y + 4}" text-anchor="middle" fill="white" font-size="${isEndpoint ? 10 : 9}" font-weight="${isEndpoint ? 700 : 500}">${escapeHtml(label)}</text>`;
    }

    // Legend: unique paths
    const uniquePaths = [...new Set(allPaths.map(p => p.hops.join('→')))];
    const legendHtml = uniquePaths.length > 1
      ? `<div class="trace-path-info" style="margin-top:8px">${uniquePaths.length} unique path${uniquePaths.length > 1 ? 's' : ''} observed by ${allPaths.length} observer${allPaths.length > 1 ? 's' : ''}</div>`
      : `<div class="trace-path-info">${allPaths[0].hops.length} hop${allPaths[0].hops.length !== 1 ? 's' : ''} in relay path</div>`;

    return `
      <div class="trace-section">
        <h3>Path Graph</h3>
        <div style="overflow-x:auto;">
          <svg width="${svgW}" height="${svgH}" style="display:block;margin:0 auto;">
            ${edgesSvg}
            ${nodesSvg}
          </svg>
        </div>
        ${legendHtml}
      </div>`;
  }

  function renderTimeline(t0, spreadMs) {
    const barWidth = spreadMs > 0 ? spreadMs : 1;
    const rows = traceData.map((t, i) => {
      const time = new Date(t.time);
      const offsetMs = t0 !== null ? time.getTime() - t0 : 0;
      const pct = spreadMs > 0 ? (offsetMs / barWidth) * 100 : 50;
      const snrClass = t.snr != null ? (t.snr >= 0 ? 'good' : t.snr >= -10 ? 'ok' : 'bad') : '';
      const delta = spreadMs > 0 ? `+${(offsetMs / 1000).toFixed(3)}s` : '';

      return `<div class="tl-row">
        <div class="tl-observer">${obsLink(t)}</div>
        <div class="tl-bar-container">
          <div class="tl-marker" style="left:${pct}%" title="${time.toISOString()}"></div>
        </div>
        <div class="tl-delta mono">${delta}</div>
        <div class="tl-snr ${snrClass}">${t.snr != null ? Number(t.snr).toFixed(1) + ' dB' : '—'}</div>
        <div class="tl-rssi">${t.rssi != null ? Number(t.rssi).toFixed(0) + ' dBm' : '—'}</div>
      </div>`;
    });

    return `
      <div class="trace-section">
        <h3>Propagation Timeline</h3>
        <div class="tl-header">
          <span>Observer</span><span>Time</span><span>Δ</span><span>SNR</span><span>RSSI</span>
        </div>
        ${rows.join('')}
      </div>`;
  }

  registerPage('traces', { init, destroy });
})();
