/* === MeshCore Analyzer — observer-detail.js === */
'use strict';
(function () {
  const PAYLOAD_LABELS = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 11: 'Control' };
  const CHART_COLORS = ['#4a9eff', '#ff6b6b', '#51cf66', '#fcc419', '#cc5de8', '#20c997', '#ff922b', '#845ef7', '#f06595', '#339af0'];

  let charts = [];
  let currentDays = 7;
  let currentId = null;

  function destroyCharts() {
    charts.forEach(c => { try { c.destroy(); } catch {} });
    charts = [];
  }

  function chartDefaults() {
    const style = getComputedStyle(document.documentElement);
    Chart.defaults.color = style.getPropertyValue('--text-muted').trim() || '#6b7280';
    Chart.defaults.borderColor = style.getPropertyValue('--border').trim() || '#e2e5ea';
  }

  function formatDuration(secs) {
    if (!secs) return '—';
    const d = Math.floor(secs / 86400);
    const h = Math.floor((secs % 86400) / 3600);
    const m = Math.floor((secs % 3600) / 60);
    if (d > 0) return d + 'd ' + h + 'h';
    if (h > 0) return h + 'h ' + m + 'm';
    return m + 'm';
  }

  function init(app, routeParam) {
    currentId = routeParam;
    if (!currentId) {
      app.innerHTML = '<div class="text-center text-muted" style="padding:40px">No observer ID specified.</div>';
      return;
    }

    app.innerHTML = `
      <div class="observer-detail-page" style="overflow-y:auto;height:calc(100vh - 56px);padding:16px">
        <div class="page-header" style="display:flex;align-items:center;gap:12px;margin-bottom:16px">
          <a href="#/observers" class="btn-icon" title="Back to Observers" aria-label="Back">←</a>
          <h2 style="margin:0" id="obsTitle">Observer Detail</h2>
          <div style="margin-left:auto;display:flex;gap:8px">
            <select id="obsDaysSelect" class="time-range-select" aria-label="Time range">
              <option value="1">24 Hours</option>
              <option value="3">3 Days</option>
              <option value="7" selected>7 Days</option>
              <option value="30">30 Days</option>
            </select>
          </div>
        </div>
        <div id="obsDetailContent"><div class="text-center text-muted" style="padding:40px">Loading…</div></div>
      </div>`;

    document.getElementById('obsDaysSelect').addEventListener('change', function (e) {
      currentDays = parseInt(e.target.value);
      loadDetail();
    });

    loadDetail();
  }

  function destroy() {
    destroyCharts();
    currentId = null;
  }

  async function loadDetail() {
    try {
      destroyCharts();
      chartDefaults();
      const [obs, analytics] = await Promise.all([
        api('/observers/' + encodeURIComponent(currentId)),
        api('/observers/' + encodeURIComponent(currentId) + '/analytics?days=' + currentDays),
      ]);
      renderDetail(obs, analytics);
    } catch (e) {
      document.getElementById('obsDetailContent').innerHTML =
        '<div class="text-muted" style="padding:40px">Error: ' + e.message + '</div>';
    }
  }

  function renderDetail(obs, analytics) {
    const el = document.getElementById('obsDetailContent');
    if (!el) return;

    const title = document.getElementById('obsTitle');
    if (title) title.textContent = obs.name || obs.id.substring(0, 16) + '…';

    // Parse radio string
    let radioHtml = '—';
    if (obs.radio) {
      const rp = obs.radio.split(',');
      radioHtml = rp[0] + ' MHz · SF' + (rp[2] || '?') + ' · BW' + (rp[1] || '?') + ' · CR' + (rp[3] || '?');
    }

    // Health status
    const ago = obs.last_seen ? Date.now() - new Date(obs.last_seen).getTime() : Infinity;
    const statusCls = ago < 600000 ? 'health-green' : ago < HEALTH_THRESHOLDS.nodeDegradedMs ? 'health-yellow' : 'health-red';
    const statusLabel = ago < 600000 ? 'Online' : ago < HEALTH_THRESHOLDS.nodeDegradedMs ? 'Stale' : 'Offline';

    el.innerHTML = `
      <div class="obs-info-grid" style="display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px;margin-bottom:20px">
        <div class="stat-card">
          <div class="stat-label">Status</div>
          <div class="stat-value"><span class="health-dot ${statusCls}">●</span> ${statusLabel}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Region</div>
          <div class="stat-value">${obs.iata ? '<span class="badge-region">' + obs.iata + '</span>' : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Model</div>
          <div class="stat-value">${obs.model || '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Firmware</div>
          <div class="stat-value" style="font-size:0.8em;word-break:break-all">${obs.firmware || '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Client</div>
          <div class="stat-value" style="font-size:0.8em;word-break:break-all">${obs.client_version || '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Radio</div>
          <div class="stat-value" style="font-size:0.85em">${radioHtml}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Battery</div>
          <div class="stat-value">${obs.battery_mv ? obs.battery_mv + ' mV' : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Uptime</div>
          <div class="stat-value">${formatDuration(obs.uptime_secs)}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Noise Floor</div>
          <div class="stat-value">${obs.noise_floor != null ? obs.noise_floor + ' dBm' : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Total Packets</div>
          <div class="stat-value">${(obs.packet_count || 0).toLocaleString()}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Packets/Hour</div>
          <div class="stat-value">${(obs.packetsLastHour || 0).toLocaleString()}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">First Seen</div>
          <div class="stat-value" style="font-size:0.85em">${obs.first_seen ? new Date(obs.first_seen).toLocaleDateString() : '—'}</div>
        </div>
      </div>
      <div class="mono" style="font-size:0.75em;color:var(--text-muted);margin-bottom:20px;word-break:break-all">
        ID: ${obs.id}
      </div>
      <div class="obs-charts" style="display:grid;grid-template-columns:repeat(auto-fit,minmax(400px,1fr));gap:16px">
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Packets Over Time</h3>
          <canvas id="obsTimeChart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Packet Types</h3>
          <div style="max-width:280px;margin:0 auto"><canvas id="obsTypeChart"></canvas></div>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Unique Nodes Heard</h3>
          <canvas id="obsNodesChart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">SNR Distribution</h3>
          <canvas id="obsSnrChart"></canvas>
        </div>
      </div>
      <div style="margin-top:20px">
        <h3 style="font-size:0.95em">Recent Packets</h3>
        <div id="obsRecentPackets"><div class="text-muted">Loading…</div></div>
      </div>`;

    // Render charts
    if (analytics.timeline && analytics.timeline.length > 0) {
      renderTimelineChart(analytics.timeline);
    }
    if (analytics.packetTypes) {
      renderTypeChart(analytics.packetTypes);
    }
    if (analytics.nodesTimeline && analytics.nodesTimeline.length > 0) {
      renderNodesChart(analytics.nodesTimeline);
    }
    if (analytics.snrDistribution && analytics.snrDistribution.length > 0) {
      renderSnrChart(analytics.snrDistribution);
    }
    if (analytics.recentPackets) {
      renderRecentPackets(analytics.recentPackets);
    }
  }

  function renderTimelineChart(timeline) {
    const ctx = document.getElementById('obsTimeChart');
    if (!ctx) return;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: timeline.map(t => t.label),
        datasets: [{
          label: 'Packets',
          data: timeline.map(t => t.count),
          backgroundColor: CHART_COLORS[0] + '80',
          borderColor: CHART_COLORS[0],
          borderWidth: 1,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { beginAtZero: true, ticks: { precision: 0 } }
        }
      }
    });
    charts.push(c);
  }

  function renderTypeChart(types) {
    const ctx = document.getElementById('obsTypeChart');
    if (!ctx) return;
    const labels = Object.keys(types).map(k => PAYLOAD_LABELS[k] || 'Type ' + k);
    const values = Object.values(types);
    const c = new Chart(ctx, {
      type: 'doughnut',
      data: {
        labels: labels,
        datasets: [{ data: values, backgroundColor: CHART_COLORS.slice(0, labels.length) }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { position: 'bottom', labels: { boxWidth: 12 } } }
      }
    });
    charts.push(c);
  }

  function renderNodesChart(timeline) {
    const ctx = document.getElementById('obsNodesChart');
    if (!ctx) return;
    const c = new Chart(ctx, {
      type: 'line',
      data: {
        labels: timeline.map(t => t.label),
        datasets: [{
          label: 'Unique Nodes',
          data: timeline.map(t => t.count),
          borderColor: CHART_COLORS[2],
          backgroundColor: CHART_COLORS[2] + '20',
          fill: true, tension: 0.3, pointRadius: 2,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { beginAtZero: true, ticks: { precision: 0 } }
        }
      }
    });
    charts.push(c);
  }

  function renderSnrChart(distribution) {
    const ctx = document.getElementById('obsSnrChart');
    if (!ctx) return;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: distribution.map(d => d.range),
        datasets: [{
          label: 'Packets',
          data: distribution.map(d => d.count),
          backgroundColor: CHART_COLORS[3] + '80',
          borderColor: CHART_COLORS[3],
          borderWidth: 1,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { title: { display: true, text: 'SNR (dB)' } },
          y: { beginAtZero: true, ticks: { precision: 0 } }
        }
      }
    });
    charts.push(c);
  }

  function renderRecentPackets(packets) {
    const el = document.getElementById('obsRecentPackets');
    if (!el || !packets.length) { if (el) el.innerHTML = '<div class="text-muted">No recent packets.</div>'; return; }
    el.innerHTML = `<table class="data-table" style="font-size:0.85em">
      <thead><tr><th>Time</th><th>Type</th><th>Hash</th><th>SNR</th><th>RSSI</th><th>Hops</th></tr></thead>
      <tbody>${packets.map(p => {
        const decoded = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json) : (p.decoded_json || {});
        const hops = typeof p.path_json === 'string' ? JSON.parse(p.path_json) : (p.path_json || []);
        const typeName = PAYLOAD_LABELS[p.payload_type] || 'Type ' + p.payload_type;
        return `<tr style="cursor:pointer" onclick="location.hash='#/packets/${p.hash || p.id}'">
          <td>${timeAgo(p.timestamp)}</td>
          <td>${typeName}</td>
          <td class="mono" style="font-size:0.85em">${(p.hash || '').substring(0, 10)}</td>
          <td>${p.snr != null ? Number(p.snr).toFixed(1) : '—'}</td>
          <td>${p.rssi != null ? p.rssi : '—'}</td>
          <td>${hops.length}</td>
        </tr>`;
      }).join('')}</tbody>
    </table>`;
  }

  registerPage('observer-detail', { init, destroy });
})();
