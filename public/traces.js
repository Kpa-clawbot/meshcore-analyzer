/* === MeshCore Analyzer — traces.js === */
'use strict';

(function () {
  let currentHash = null;
  let traceData = [];
  let packetMeta = null;

  function init(app) {
    // Check URL for pre-filled hash
    const params = new URLSearchParams(location.hash.split('?')[1] || '');
    const urlHash = params.get('hash') || '';

    app.innerHTML = `
      <div class="traces-page">
        <div class="page-header">
          <h2>🔍 Packet Trace</h2>
        </div>
        <div class="trace-search">
          <input type="text" id="traceHashInput" placeholder="Enter packet hash…" value="${urlHash}">
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

      // Extract path from first packet that has it
      let pathHops = [];
      for (const p of packets) {
        try {
          const hops = JSON.parse(p.path_json || '[]');
          if (hops.length > 0) { pathHops = hops; break; }
        } catch {}
      }

      // Get packet type info from first packet
      packetMeta = packets[0] || null;
      let decoded = null;
      if (packetMeta) {
        try { decoded = JSON.parse(packetMeta.decoded_json); } catch {}
      }

      renderResults(results, pathHops, decoded);
    } catch (e) {
      results.innerHTML = `<div class="trace-empty" style="color:#ef4444">Error: ${e.message}</div>`;
    }
  }

  function renderResults(container, pathHops, decoded) {
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

      ${pathHops.length > 0 ? renderPathViz(pathHops) : ''}
      ${traceData.length > 0 ? renderTimeline(t0, spreadMs) : ''}
      ${renderObserverTable()}
    `;
    makeColumnsResizable('#traceObsTable', 'meshcore-trace-col-widths');
  }

  function renderPathViz(hops) {
    const arrows = hops.map(h => `<span class="trace-path-hop">${h}</span>`).join('<span class="trace-path-arrow">→</span>');
    return `
      <div class="trace-section">
        <h3>Path Visualization</h3>
        <div class="trace-path-viz">
          <span class="trace-path-label">Origin</span>
          <span class="trace-path-arrow">→</span>
          ${arrows}
          <span class="trace-path-arrow">→</span>
          <span class="trace-path-label">Dest</span>
        </div>
        <div class="trace-path-info">${hops.length} hop${hops.length !== 1 ? 's' : ''} in relay path</div>
      </div>`;
  }

  function renderTimeline(t0, spreadMs) {
    // Build timeline bars
    const barWidth = spreadMs > 0 ? spreadMs : 1;
    const rows = traceData.map((t, i) => {
      const time = new Date(t.time);
      const offsetMs = t0 !== null ? time.getTime() - t0 : 0;
      const pct = spreadMs > 0 ? (offsetMs / barWidth) * 100 : 50;
      const snrClass = t.snr != null ? (t.snr >= 0 ? 'good' : t.snr >= -10 ? 'ok' : 'bad') : '';
      const delta = spreadMs > 0 ? `+${(offsetMs / 1000).toFixed(3)}s` : '';

      return `<div class="tl-row">
        <div class="tl-observer">${truncate(t.observer || '—', 20)}</div>
        <div class="tl-bar-container">
          <div class="tl-marker" style="left:${pct}%" title="${time.toISOString()}"></div>
        </div>
        <div class="tl-delta mono">${delta}</div>
        <div class="tl-snr ${snrClass}">${t.snr != null ? t.snr.toFixed(1) + ' dB' : '—'}</div>
        <div class="tl-rssi">${t.rssi != null ? t.rssi.toFixed(0) + ' dBm' : '—'}</div>
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

  function renderObserverTable() {
    const rows = traceData.map((t, i) => {
      const snrClass = t.snr != null ? (t.snr >= 0 ? 'good' : t.snr >= -10 ? 'ok' : 'bad') : '';
      return `<tr>
        <td>${i + 1}</td>
        <td class="mono">${t.observer || '—'}</td>
        <td>${t.time ? new Date(t.time).toLocaleString() : '—'}</td>
        <td class="tl-snr ${snrClass}">${t.snr != null ? t.snr.toFixed(1) + ' dB' : '—'}</td>
        <td>${t.rssi != null ? t.rssi.toFixed(0) + ' dBm' : '—'}</td>
      </tr>`;
    });

    return `
      <div class="trace-section">
        <h3>Observer Details</h3>
        <table class="data-table" id="traceObsTable">
          <thead><tr><th>#</th><th>Observer</th><th>Timestamp</th><th>SNR</th><th>RSSI</th></tr></thead>
          <tbody>${rows.join('')}</tbody>
        </table>
      </div>`;
  }

  registerPage('traces', { init, destroy });
})();
