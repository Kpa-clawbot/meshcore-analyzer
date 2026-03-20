/* === MeshCore Analyzer — node-analytics.js === */
'use strict';
(function () {
  const PAYLOAD_LABELS = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 11: 'Control' };
  const CHART_COLORS = ['#4a9eff', '#ff6b6b', '#51cf66', '#fcc419', '#cc5de8', '#20c997', '#ff922b', '#845ef7', '#f06595', '#339af0'];
  const GRADE_COLORS = { A: '#51cf66', 'A-': '#51cf66', 'B+': '#339af0', B: '#339af0', C: '#fcc419', D: '#ff6b6b' };
  const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

  let charts = [];
  let currentDays = 7;
  let currentPubkey = null;

  function destroyCharts() {
    charts.forEach(c => { try { c.destroy(); } catch {} });
    charts = [];
  }

  function chartDefaults() {
    const style = getComputedStyle(document.documentElement);
    Chart.defaults.color = style.getPropertyValue('--text-muted').trim() || '#6b7280';
    Chart.defaults.borderColor = style.getPropertyValue('--border').trim() || '#e2e5ea';
  }

  function formatSilence(ms) {
    if (!ms) return '—';
    const h = Math.floor(ms / 3600000);
    const m = Math.floor((ms % 3600000) / 60000);
    if (h > 24) return Math.floor(h / 24) + 'd ' + (h % 24) + 'h';
    if (h > 0) return h + 'h ' + m + 'm';
    return m + 'm';
  }

  async function loadAnalytics(container, pubkey, days) {
    currentPubkey = pubkey;
    currentDays = days;
    destroyCharts();
    chartDefaults();

    container.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading analytics…</div>';

    let data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/analytics?days=' + days, { ttl: CLIENT_TTL.nodeAnalytics });
    } catch (e) {
      container.innerHTML = '<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load analytics: ' + escapeHtml(e.message) + '</div>';
      return;
    }

    const n = data.node;
    const s = data.computedStats;
    const nodeName = escapeHtml(n.name || n.public_key.slice(0, 12));

    container.innerHTML = `
      <div style="max-width:1000px;margin:0 auto;padding:12px 16px;height:100%;overflow-y:auto">
        <div style="margin-bottom:12px">
          <a href="#/nodes/${encodeURIComponent(n.public_key)}" style="color:var(--accent);text-decoration:none;font-size:12px">← Back to ${nodeName}</a>
          <h2 style="margin:4px 0 2px;font-size:18px">📊 ${nodeName} — Analytics</h2>
          <div style="color:var(--text-muted);font-size:11px">${n.role || 'Unknown role'} · ${s.totalPackets} packets in ${days}d window</div>
        </div>

        <div class="analytics-time-range" id="timeRangeBtns">
          <button data-days="1" ${days===1?'class="active"':''}>24h</button>
          <button data-days="7" ${days===7?'class="active"':''}>7d</button>
          <button data-days="30" ${days===30?'class="active"':''}>30d</button>
          <button data-days="365" ${days===365?'class="active"':''}>All</button>
        </div>

        <div class="analytics-stats">
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Availability</div>
            <div class="analytics-stat-value">${s.availabilityPct}%</div>
            <div class="analytics-stat-desc">% of time windows with at least one packet</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Signal Grade</div>
            <div class="analytics-stat-value" style="color:${GRADE_COLORS[s.signalGrade]||'var(--text)'}">${s.signalGrade}</div>
            <div class="analytics-stat-desc">A–F based on average SNR across all observers</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Packets / Day</div>
            <div class="analytics-stat-value">${s.avgPacketsPerDay}</div>
            <div class="analytics-stat-desc">Average daily packet volume in this window</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Observers</div>
            <div class="analytics-stat-value">${s.uniqueObservers}</div>
            <div class="analytics-stat-desc">Distinct stations that heard this node</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Relay %</div>
            <div class="analytics-stat-value">${s.relayPct}%</div>
            <div class="analytics-stat-desc">Packets forwarded through repeaters vs direct</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Longest Silence</div>
            <div class="analytics-stat-value" style="font-size:18px">${formatSilence(s.longestSilenceMs)}</div>
            <div class="analytics-stat-desc">Longest gap between consecutive packets</div>
          </div>
        </div>

        <div class="analytics-charts">
          <div class="analytics-chart-card full">
            <h4>Activity Timeline</h4>
            <div class="analytics-chart-desc">Packet count per time bucket — shows when this node is most active</div>
            <canvas id="activityChart"></canvas>
          </div>
          <div class="analytics-chart-card">
            <h4>SNR Trend</h4>
            <div class="analytics-chart-desc">Signal-to-noise ratio over time — higher is better reception</div>
            <canvas id="snrChart"></canvas>
          </div>
          <div class="analytics-chart-card">
            <h4>Packet Types</h4>
            <div class="analytics-chart-desc">Breakdown of advert, position, text, and other packet types</div>
            <canvas id="packetTypeChart"></canvas>
          </div>
          <div class="analytics-chart-card">
            <h4>Observer Coverage</h4>
            <div class="analytics-chart-desc">Which stations hear this node and how often</div>
            <canvas id="observerChart"></canvas>
          </div>
          <div class="analytics-chart-card">
            <h4>Hop Distribution</h4>
            <div class="analytics-chart-desc">How many repeater hops packets take — 0 means direct</div>
            <canvas id="hopChart"></canvas>
          </div>
          <div class="analytics-chart-card full">
            <h4>Uptime Heatmap</h4>
            <div class="analytics-chart-desc">Hour-by-hour activity grid — darker = more packets in that slot</div>
            <div id="heatmapGrid" class="analytics-heatmap"></div>
          </div>
          ${data.peerInteractions.length ? `<div class="analytics-chart-card full">
            <h4>Peer Interactions</h4>
            <div class="analytics-chart-desc">Nodes this device has exchanged messages with</div>
            <table class="analytics-peer-table">
              <thead><tr><th>Peer</th><th>Messages</th><th>Last Contact</th></tr></thead>
              <tbody>${data.peerInteractions.map(p => `<tr>
                <td><a href="#/nodes/${encodeURIComponent(p.peer_key)}" style="color:var(--accent)">${escapeHtml(p.peer_name)}</a></td>
                <td>${p.messageCount}</td>
                <td>${timeAgo(p.lastContact)}</td>
              </tr>`).join('')}</tbody>
            </table>
          </div>` : ''}
        </div>
      </div>`;

    // Time range buttons
    container.querySelectorAll('#timeRangeBtns button').forEach(btn => {
      btn.addEventListener('click', () => {
        const d = Number(btn.dataset.days);
        loadAnalytics(container, pubkey, d);
      });
    });

    // Build charts
    buildActivityChart(data);
    buildSnrChart(data);
    buildPacketTypeChart(data);
    buildObserverChart(data);
    buildHopChart(data);
    buildHeatmap(data);
  }

  function buildActivityChart(data) {
    const ctx = document.getElementById('activityChart');
    if (!ctx) return;
    const tl = data.activityTimeline;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: tl.map(b => {
          const d = new Date(b.bucket);
          return currentDays <= 3 ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : d.toLocaleDateString([], { month: 'short', day: 'numeric' });
        }),
        datasets: [{ label: 'Packets', data: tl.map(b => b.count), backgroundColor: 'rgba(74,158,255,0.5)', borderColor: '#4a9eff', borderWidth: 1 }]
      },
      options: { responsive: true, plugins: { legend: { display: false } }, scales: { x: { ticks: { maxTicksAutoSkip: true, maxRotation: 45 } }, y: { beginAtZero: true } } }
    });
    charts.push(c);
  }

  function buildSnrChart(data) {
    const ctx = document.getElementById('snrChart');
    if (!ctx) return;
    // Group by observer
    const byObs = {};
    data.snrTrend.forEach(p => {
      const key = p.observer_id || 'unknown';
      if (!byObs[key]) byObs[key] = { name: p.observer_name || key, points: [] };
      byObs[key].points.push({ x: new Date(p.timestamp), y: p.snr });
    });
    const datasets = Object.values(byObs).map((obs, i) => ({
      label: obs.name, data: obs.points.map(p => p.y), borderColor: CHART_COLORS[i % CHART_COLORS.length],
      backgroundColor: 'transparent', pointRadius: 1, borderWidth: 1.5, tension: 0.3
    }));
    // Use labels from the observer with most points
    const longestObs = Object.values(byObs).sort((a, b) => b.points.length - a.points.length)[0];
    const labels = longestObs ? longestObs.points.map(p => {
      const d = p.x;
      return d.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    }) : [];
    const c = new Chart(ctx, {
      type: 'line',
      data: { labels, datasets },
      options: {
        responsive: true,
        scales: { x: { display: false }, y: { title: { display: true, text: 'SNR (dB)' } } },
        plugins: { legend: { position: 'bottom', labels: { boxWidth: 12, font: { size: 10 } } } }
      }
    });
    charts.push(c);
  }

  function buildPacketTypeChart(data) {
    const ctx = document.getElementById('packetTypeChart');
    if (!ctx) return;
    const items = data.packetTypeBreakdown;
    const c = new Chart(ctx, {
      type: 'doughnut',
      data: {
        labels: items.map(i => PAYLOAD_LABELS[i.payload_type] || 'Type ' + i.payload_type),
        datasets: [{ data: items.map(i => i.count), backgroundColor: items.map((_, i) => CHART_COLORS[i % CHART_COLORS.length]) }]
      },
      options: { responsive: true, plugins: { legend: { position: 'bottom', labels: { boxWidth: 12, font: { size: 10 } } } } }
    });
    charts.push(c);
  }

  function buildObserverChart(data) {
    const ctx = document.getElementById('observerChart');
    if (!ctx) return;
    const obs = data.observerCoverage;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: obs.map(o => (o.observer_name || o.observer_id || '?').slice(0, 20)),
        datasets: [{ label: 'Packets', data: obs.map(o => o.packetCount), backgroundColor: obs.map(o => {
          const snr = o.avgSnr || 0;
          const alpha = Math.min(1, Math.max(0.3, snr / 20));
          return `rgba(74,158,255,${alpha})`;
        }) }]
      },
      options: { indexAxis: 'y', responsive: true, plugins: { legend: { display: false } }, scales: { x: { beginAtZero: true } } }
    });
    charts.push(c);
  }

  function buildHopChart(data) {
    const ctx = document.getElementById('hopChart');
    if (!ctx) return;
    const hops = data.hopDistribution;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: hops.map(h => h.hops + ' hop' + (h.hops !== '1' ? 's' : '')),
        datasets: [{ label: 'Packets', data: hops.map(h => h.count), backgroundColor: 'rgba(81,207,102,0.6)', borderColor: '#51cf66', borderWidth: 1 }]
      },
      options: { responsive: true, plugins: { legend: { display: false } }, scales: { y: { beginAtZero: true } } }
    });
    charts.push(c);
  }

  function buildHeatmap(data) {
    const grid = document.getElementById('heatmapGrid');
    if (!grid) return;
    // Build lookup
    const lookup = {};
    let maxCount = 1;
    data.uptimeHeatmap.forEach(h => {
      const key = h.dayOfWeek + '-' + h.hour;
      lookup[key] = h.count;
      if (h.count > maxCount) maxCount = h.count;
    });

    // Header row
    grid.innerHTML = '<div class="analytics-heatmap-label"></div>';
    for (let h = 0; h < 24; h++) {
      grid.innerHTML += `<div class="analytics-heatmap-label" style="justify-content:center;font-size:9px">${h}</div>`;
    }
    // Day rows
    for (let d = 0; d < 7; d++) {
      grid.innerHTML += `<div class="analytics-heatmap-label">${DAY_NAMES[d]}</div>`;
      for (let h = 0; h < 24; h++) {
        const count = lookup[d + '-' + h] || 0;
        const intensity = count / maxCount;
        const bg = count === 0 ? 'var(--card-bg)' : `rgba(74,158,255,${0.15 + intensity * 0.85})`;
        grid.innerHTML += `<div class="analytics-heatmap-cell" style="background:${bg}" title="${DAY_NAMES[d]} ${h}:00 — ${count} packets"></div>`;
      }
    }
  }

  function init(container, routeParam) {
    // routeParam is "PUBKEY/analytics"
    if (!routeParam || !routeParam.endsWith('/analytics')) {
      container.innerHTML = '<div style="padding:40px;text-align:center">Invalid analytics URL</div>';
      return;
    }
    const pubkey = routeParam.slice(0, -'/analytics'.length);
    loadAnalytics(container, pubkey, 7);
  }

  function destroy() {
    destroyCharts();
    currentPubkey = null;
  }

  registerPage('node-analytics', { init, destroy });
})();
