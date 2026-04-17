/* === CoreScope — perf.js === */
'use strict';

(function () {
  let interval = null;

  // --- History ring buffer (1 hour at 5 s intervals) ---
  const MAX_SAMPLES = 720;
  const history = [];
  const TIMEFRAME_SAMPLES = { '5m': 60, '15m': 180, '30m': 360, '1h': 720 };

  // --- View state (persisted across navigations) ---
  let viewMode  = localStorage.getItem('perf-view')      || 'cards';
  let timeframe = localStorage.getItem('perf-timeframe') || '5m';
  let activeCharts = []; // [{ chart: Chart, key: string }]

  const METRICS = [
    { key: 'cpuPercent',   label: 'CPU Usage (%)',               color: '#f43f5e' },
    { key: 'totalSysMB',   label: 'Server RAM — Total Sys (MB)', color: '#22c55e' },
    { key: 'heapAllocMB',  label: 'Heap Alloc (MB)',             color: '#4a9eff' },
    { key: 'goroutines',   label: 'Goroutines',                  color: '#eab308' },
    { key: 'packetsInRAM', label: 'Packets in RAM',              color: '#a855f7' },
    { key: 'cacheHitRate', label: 'Cache Hit Rate (%)',           color: '#f97316' },
    { key: 'avgMs',        label: 'Avg Response (ms)',           color: '#ef4444' },
    { key: 'dbSizeMB',     label: 'DB Size (MB)',                color: '#64748b' },
  ];

  // --- Ring buffer helpers ---
  function pushSample(server) {
    const gr = server.goRuntime;
    const ps = server.packetStore;
    const sq = server.sqlite;
    history.push({
      ts:           Date.now(),
      cpuPercent:   gr ? +gr.cpuPercent                : null,
      totalSysMB:   gr ? +gr.totalSysMB                : null,
      heapAllocMB:  gr ? +gr.heapAllocMB               : null,
      goroutines:   gr ? gr.goroutines                 : null,
      packetsInRAM: ps ? ps.inMemory                   : null,
      cacheHitRate: server.cache ? server.cache.hitRate : null,
      avgMs:        server.avgMs  || null,
      dbSizeMB:     sq ? +sq.dbSizeMB                  : null,
    });
    if (history.length > MAX_SAMPLES) history.shift();
  }

  function getSlice() {
    return history.slice(-(TIMEFRAME_SAMPLES[timeframe] || 60));
  }

  // --- Chart lifecycle ---
  function destroyCharts() {
    activeCharts.forEach(function (c) { c.chart.destroy(); });
    activeCharts = [];
  }

  // --- Init: builds page chrome once per navigation ---
  async function render(app) {
    app.innerHTML = `
      <div id="perfWrapper" style="padding:16px 24px">
        <div class="perf-header">
          <h2 style="margin:0">⚡ Performance Dashboard</h2>
          <div class="perf-header-controls">
            <button id="perfViewToggle" class="perf-view-btn">${viewMode === 'graphs' ? '📋 Cards' : '📊 Graphs'}</button>
            <div id="perfTfBar" class="perf-tf-bar" style="display:${viewMode === 'graphs' ? 'flex' : 'none'}">
              ${['5m', '15m', '30m', '1h'].map(function (t) {
                return `<button class="perf-tf-btn${t === timeframe ? ' active' : ''}" data-tf="${t}">${t}</button>`;
              }).join('')}
            </div>
          </div>
        </div>
        <div id="perfContent">Loading…</div>
      </div>`;

    document.getElementById('perfViewToggle').addEventListener('click', function () {
      viewMode = viewMode === 'graphs' ? 'cards' : 'graphs';
      localStorage.setItem('perf-view', viewMode);
      document.getElementById('perfViewToggle').textContent = viewMode === 'graphs' ? '📋 Cards' : '📊 Graphs';
      document.getElementById('perfTfBar').style.display = viewMode === 'graphs' ? 'flex' : 'none';
      if (viewMode === 'cards') destroyCharts();
      refresh();
    });

    document.getElementById('perfTfBar').addEventListener('click', function (e) {
      var btn = e.target.closest('[data-tf]');
      if (!btn) return;
      timeframe = btn.dataset.tf;
      localStorage.setItem('perf-timeframe', timeframe);
      document.querySelectorAll('.perf-tf-btn').forEach(function (b) {
        b.classList.toggle('active', b.dataset.tf === timeframe);
      });
      // Re-slice and push new data into existing charts immediately — no redraw
      if (activeCharts.length > 0) {
        var slice = getSlice();
        var labels = slice.map(function (s) { return new Date(s.ts).toLocaleTimeString(); });
        activeCharts.forEach(function (c) {
          c.chart.data.labels = labels;
          c.chart.data.datasets[0].data = slice.map(function (s) { return s[c.key]; });
          c.chart.update('none');
        });
      }
    });

    await refresh();
  }

  // --- Polling refresh ---
  async function refresh() {
    var el = document.getElementById('perfContent');
    if (!el) return;
    try {
      const [server, client] = await Promise.all([
        fetch('/api/perf').then(r => r.json()),
        Promise.resolve(window.apiPerf ? window.apiPerf() : null)
      ]);
      const health = await fetch('/api/health').then(r => r.json()).catch(() => null);

      pushSample(server);

      if (viewMode === 'graphs') {
        renderGraphs(el);
      } else {
        renderCards(el, server, health, client);
      }
    } catch (err) {
      el.innerHTML = `<p style="color:red">Error: ${err.message}</p>`;
    }
  }

  // --- Cards view (original rendering, unchanged) ---
  function renderCards(el, server, health, client) {
    destroyCharts();
    let html = '';

    // Server overview
    const gr0 = server.goRuntime;
    const cpuColor = gr0 && gr0.cpuPercent > 80 ? 'var(--status-red)' : gr0 && gr0.cpuPercent > 40 ? 'var(--status-yellow)' : 'var(--status-green)';
    html += `<div style="display:flex;gap:16px;flex-wrap:wrap;margin:16px 0;">
      <div class="perf-card"><div class="perf-num">${server.totalRequests}</div><div class="perf-label">Total Requests</div></div>
      <div class="perf-card"><div class="perf-num">${server.avgMs}ms</div><div class="perf-label">Avg Response</div></div>
      <div class="perf-card"><div class="perf-num">${health ? health.uptimeHuman : Math.round(server.uptime / 60) + 'm'}</div><div class="perf-label">Uptime</div></div>
      <div class="perf-card"><div class="perf-num">${server.slowQueries.length}</div><div class="perf-label">Slow (&gt;100ms)</div></div>
      ${gr0 ? `<div class="perf-card"><div class="perf-num" style="color:${cpuColor}">${(+gr0.cpuPercent).toFixed(1)}%</div><div class="perf-label">CPU Usage</div></div>` : ''}
      ${gr0 ? `<div class="perf-card"><div class="perf-num">${(+gr0.totalSysMB).toFixed(0)}MB</div><div class="perf-label">Server RAM</div></div>` : ''}
      ${server.sqlite ? `<div class="perf-card"><div class="perf-num">${server.sqlite.dbSizeMB}MB</div><div class="perf-label">Dataset Size</div></div>` : ''}
    </div>`;

    // System health
    if (health) {
      const isGo = health.engine === 'go';
      if (isGo && server.goRuntime) {
        const gr = server.goRuntime;
        const gcColor = gr.lastPauseMs > 5 ? 'var(--status-red)' : gr.lastPauseMs > 1 ? 'var(--status-yellow)' : 'var(--status-green)';
        const cpuPctColor = gr.cpuPercent > 80 ? 'var(--status-red)' : gr.cpuPercent > 40 ? 'var(--status-yellow)' : 'var(--status-green)';
        html += `<h3>🔧 Go Runtime</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num" style="color:${cpuPctColor}">${(+gr.cpuPercent).toFixed(1)}%</div><div class="perf-label">CPU Usage</div></div>
          <div class="perf-card"><div class="perf-num">${(+gr.totalSysMB).toFixed(0)}MB</div><div class="perf-label">Total Sys RAM</div></div>
          <div class="perf-card"><div class="perf-num">${gr.goroutines}</div><div class="perf-label">Goroutines</div></div>
          <div class="perf-card"><div class="perf-num">${gr.numGC}</div><div class="perf-label">GC Collections</div></div>
          <div class="perf-card"><div class="perf-num" style="color:${gcColor}">${(+gr.pauseTotalMs).toFixed(1)}ms</div><div class="perf-label">GC Pause Total</div></div>
          <div class="perf-card"><div class="perf-num">${(+gr.lastPauseMs).toFixed(1)}ms</div><div class="perf-label">Last GC Pause</div></div>
          <div class="perf-card"><div class="perf-num">${(+gr.heapAllocMB).toFixed(1)}MB</div><div class="perf-label">Heap Alloc</div></div>
          <div class="perf-card"><div class="perf-num">${(+gr.heapSysMB).toFixed(1)}MB</div><div class="perf-label">Heap Sys</div></div>
          <div class="perf-card"><div class="perf-num">${(+gr.heapInuseMB).toFixed(1)}MB</div><div class="perf-label">Heap Inuse</div></div>
          <div class="perf-card"><div class="perf-num">${(+gr.heapIdleMB).toFixed(1)}MB</div><div class="perf-label">Heap Idle</div></div>
          <div class="perf-card"><div class="perf-num">${gr.numCPU}</div><div class="perf-label">CPUs</div></div>
          <div class="perf-card"><div class="perf-num">${health.websocket.clients}</div><div class="perf-label">WS Clients</div></div>
        </div>`;
      } else {
        const m = health.memory, evl = health.eventLoop;
        const elColor  = evl.p95Ms > 500 ? 'var(--status-red)' : evl.p95Ms > 100 ? 'var(--status-yellow)' : 'var(--status-green)';
        const memColor = m.heapUsed > m.heapTotal * 0.85 ? 'var(--status-red)' : m.heapUsed > m.heapTotal * 0.7 ? 'var(--status-yellow)' : 'var(--status-green)';
        html += `<h3>System Health</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num" style="color:${memColor}">${m.heapUsed}MB</div><div class="perf-label">Heap Used / ${m.heapTotal}MB</div></div>
          <div class="perf-card"><div class="perf-num">${m.rss}MB</div><div class="perf-label">RSS</div></div>
          <div class="perf-card"><div class="perf-num" style="color:${elColor}">${evl.p95Ms}ms</div><div class="perf-label">Event Loop p95</div></div>
          <div class="perf-card"><div class="perf-num">${evl.maxLagMs}ms</div><div class="perf-label">EL Max Lag</div></div>
          <div class="perf-card"><div class="perf-num">${evl.currentLagMs}ms</div><div class="perf-label">EL Current</div></div>
          <div class="perf-card"><div class="perf-num">${health.websocket.clients}</div><div class="perf-label">WS Clients</div></div>
        </div>`;
      }
    }

    // Cache stats
    if (server.cache) {
      const c = server.cache;
      const clientCache = typeof _apiCache !== 'undefined' ? _apiCache.size : 0;
      html += `<h3>Cache</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
        <div class="perf-card"><div class="perf-num">${c.size}</div><div class="perf-label">Server Entries</div></div>
        <div class="perf-card"><div class="perf-num">${c.hits}</div><div class="perf-label">Server Hits</div></div>
        <div class="perf-card"><div class="perf-num">${c.misses}</div><div class="perf-label">Server Misses</div></div>
        <div class="perf-card"><div class="perf-num" style="color:${c.hitRate > 50 ? 'var(--status-green)' : c.hitRate > 20 ? 'var(--status-yellow)' : 'var(--status-red)'}">${c.hitRate}%</div><div class="perf-label">Server Hit Rate</div></div>
        <div class="perf-card"><div class="perf-num">${c.staleHits || 0}</div><div class="perf-label">Stale Hits (SWR)</div></div>
        <div class="perf-card"><div class="perf-num">${c.recomputes || 0}</div><div class="perf-label">Recomputes</div></div>
        <div class="perf-card"><div class="perf-num">${clientCache}</div><div class="perf-label">Client Entries</div></div>
      </div>`;
      if (client) {
        html += `<div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num">${client.cacheHits || 0}</div><div class="perf-label">Client Hits</div></div>
          <div class="perf-card"><div class="perf-num">${client.cacheMisses || 0}</div><div class="perf-label">Client Misses</div></div>
          <div class="perf-card"><div class="perf-num" style="color:${(client.cacheHitRate || 0) > 50 ? 'var(--status-green)' : 'var(--status-yellow)'}">${client.cacheHitRate || 0}%</div><div class="perf-label">Client Hit Rate</div></div>
        </div>`;
      }
    }

    // Packet Store stats
    if (server.packetStore) {
      const ps = server.packetStore;
      html += `<h3>In-Memory Packet Store</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
        <div class="perf-card"><div class="perf-num">${ps.inMemory.toLocaleString()}</div><div class="perf-label">Packets in RAM</div></div>
        <div class="perf-card"><div class="perf-num">${ps.trackedMB}MB</div><div class="perf-label">Tracked Memory</div></div>
        <div class="perf-card"><div class="perf-num">${ps.maxMB}MB</div><div class="perf-label">Memory Limit</div></div>
        <div class="perf-card"><div class="perf-num">${ps.estimatedMB}MB</div><div class="perf-label">Heap (debug)</div></div>
        <div class="perf-card"><div class="perf-num">${ps.queries.toLocaleString()}</div><div class="perf-label">Queries Served</div></div>
        <div class="perf-card"><div class="perf-num">${ps.inserts.toLocaleString()}</div><div class="perf-label">Live Inserts</div></div>
        <div class="perf-card"><div class="perf-num">${ps.evicted.toLocaleString()}</div><div class="perf-label">Evicted</div></div>
        <div class="perf-card"><div class="perf-num">${ps.indexes.byHash.toLocaleString()}</div><div class="perf-label">Unique Hashes</div></div>
        <div class="perf-card"><div class="perf-num">${ps.indexes.byObserver}</div><div class="perf-label">Observers</div></div>
        <div class="perf-card"><div class="perf-num">${ps.indexes.byNode.toLocaleString()}</div><div class="perf-label">Indexed Nodes</div></div>
      </div>`;
    }

    // SQLite stats
    if (server.sqlite && !server.sqlite.error) {
      const sq = server.sqlite;
      const walColor      = sq.walSizeMB > 50  ? 'var(--status-red)'    : sq.walSizeMB > 10  ? 'var(--status-yellow)' : 'var(--status-green)';
      const freelistColor = sq.freelistMB > 10 ? 'var(--status-yellow)' : 'var(--status-green)';
      html += `<h3>SQLite</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
        <div class="perf-card"><div class="perf-num">${sq.dbSizeMB}MB</div><div class="perf-label">DB Size</div></div>
        <div class="perf-card"><div class="perf-num" style="color:${walColor}">${sq.walSizeMB}MB</div><div class="perf-label">WAL Size</div></div>
        <div class="perf-card"><div class="perf-num" style="color:${freelistColor}">${sq.freelistMB}MB</div><div class="perf-label">Freelist</div></div>
        <div class="perf-card"><div class="perf-num">${(sq.rows.transmissions || 0).toLocaleString()}</div><div class="perf-label">Transmissions</div></div>
        <div class="perf-card"><div class="perf-num">${(sq.rows.observations || 0).toLocaleString()}</div><div class="perf-label">Observations</div></div>
        <div class="perf-card"><div class="perf-num">${sq.rows.nodes || 0}</div><div class="perf-label">Nodes</div></div>
        <div class="perf-card"><div class="perf-num">${sq.rows.observers || 0}</div><div class="perf-label">Observers</div></div>`;
      if (sq.walPages) {
        html += `<div class="perf-card"><div class="perf-num">${sq.walPages.busy}</div><div class="perf-label">WAL Busy Pages</div></div>`;
      }
      html += `</div>`;
    }

    // Server endpoints table
    const eps = Object.entries(server.endpoints);
    if (eps.length) {
      html += '<h3>Server Endpoints (sorted by total time)</h3>';
      html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th scope="col">Endpoint</th><th scope="col">Count</th><th scope="col">Avg</th><th scope="col">P50</th><th scope="col">P95</th><th scope="col">Max</th><th scope="col">Total</th></tr></thead><tbody>';
      for (const [path, s] of eps) {
        const total = Math.round(s.count * s.avgMs);
        const cls = s.p95Ms > 200 ? ' class="perf-slow"' : s.p95Ms > 50 ? ' class="perf-warn"' : '';
        html += `<tr${cls}><td><code>${path}</code></td><td>${s.count}</td><td>${s.avgMs}ms</td><td>${s.p50Ms}ms</td><td>${s.p95Ms}ms</td><td>${s.maxMs}ms</td><td>${total}ms</td></tr>`;
      }
      html += '</tbody></table></div>';
    }

    // Client API calls
    if (client && client.endpoints.length) {
      html += '<h3>Client API Calls (this session)</h3>';
      html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th scope="col">Endpoint</th><th scope="col">Count</th><th scope="col">Avg</th><th scope="col">Max</th><th scope="col">Total</th></tr></thead><tbody>';
      for (const s of client.endpoints) {
        const cls = s.maxMs > 500 ? ' class="perf-slow"' : s.avgMs > 200 ? ' class="perf-warn"' : '';
        html += `<tr${cls}><td><code>${s.path}</code></td><td>${s.count}</td><td>${s.avgMs}ms</td><td>${s.maxMs}ms</td><td>${s.totalMs}ms</td></tr>`;
      }
      html += '</tbody></table></div>';
    }

    // Slow queries
    if (server.slowQueries.length) {
      html += '<h3>Recent Slow Queries (&gt;100ms)</h3>';
      html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th scope="col">Time</th><th scope="col">Path</th><th scope="col">Duration</th><th scope="col">Status</th></tr></thead><tbody>';
      for (const q of server.slowQueries.slice().reverse()) {
        html += `<tr class="perf-slow"><td>${new Date(q.time).toLocaleTimeString()}</td><td><code>${q.path}</code></td><td>${q.ms}ms</td><td>${q.status}</td></tr>`;
      }
      html += '</tbody></table></div>';
    }

    html += `<div style="margin-top:16px"><button id="perfReset" style="padding:8px 16px;cursor:pointer">Reset Stats</button> <button id="perfRefresh" style="padding:8px 16px;cursor:pointer">Refresh</button></div>`;
    el.innerHTML = html;

    document.getElementById('perfReset')?.addEventListener('click', async () => {
      await fetch('/api/perf/reset', { method: 'POST' });
      if (window._apiPerf) { window._apiPerf = { calls: 0, totalMs: 0, log: [] }; }
      refresh();
    });
    document.getElementById('perfRefresh')?.addEventListener('click', refresh);
  }

  // --- Graphs view ---
  function renderGraphs(el) {
    const slice = getSlice();

    if (slice.length < 2) {
      if (activeCharts.length === 0) {
        el.innerHTML = '<div class="text-muted text-center" style="padding:40px">Collecting data… check back in a few seconds.</div>';
      }
      return;
    }

    const labels = slice.map(function (s) { return new Date(s.ts).toLocaleTimeString(); });
    const visibleMetrics = METRICS.filter(function (m) {
      return slice.some(function (s) { return s[m.key] != null; });
    });

    // Charts already exist — update data in place, no DOM rebuild
    if (activeCharts.length > 0) {
      activeCharts.forEach(function (c) {
        c.chart.data.labels = labels;
        c.chart.data.datasets[0].data = slice.map(function (s) { return s[c.key]; });
        c.chart.update('none');
      });
      return;
    }

    // First render — build DOM and create Chart instances
    el.innerHTML = `<div class="perf-graphs-grid">${
      visibleMetrics.map(function (m) {
        return `<div class="perf-graph-card">
          <div class="perf-graph-title">${m.label}</div>
          <div style="position:relative;height:160px"><canvas id="pgc-${m.key}"></canvas></div>
        </div>`;
      }).join('')
    }</div>`;

    const isDark = document.documentElement.dataset.theme === 'dark' ||
      (!document.documentElement.dataset.theme && window.matchMedia('(prefers-color-scheme: dark)').matches);
    const gridColor = isDark ? 'rgba(255,255,255,0.07)' : 'rgba(0,0,0,0.06)';
    const tickColor = isDark ? '#a8b8cc' : '#5b6370';

    visibleMetrics.forEach(function (m) {
      const canvas = document.getElementById('pgc-' + m.key);
      if (!canvas) return;
      const chart = new Chart(canvas, {
        type: 'line',
        data: {
          labels: labels,
          datasets: [{
            data: slice.map(function (s) { return s[m.key]; }),
            borderColor: m.color,
            backgroundColor: m.color + '22',
            borderWidth: 1.5,
            pointRadius: 0,
            tension: 0.3,
            fill: true,
          }]
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: false,
          plugins: {
            legend: { display: false },
            tooltip: { mode: 'index', intersect: false }
          },
          scales: {
            x: {
              ticks: { maxTicksLimit: 6, font: { size: 10 }, color: tickColor },
              grid: { color: gridColor }
            },
            y: {
              beginAtZero: false,
              ticks: { font: { size: 10 }, color: tickColor },
              grid: { color: gridColor }
            }
          }
        }
      });
      activeCharts.push({ chart: chart, key: m.key });
    });
  }

  registerPage('perf', {
    init(app) {
      render(app);
      interval = setInterval(refresh, 5000);
    },
    destroy() {
      destroyCharts();
      if (interval) { clearInterval(interval); interval = null; }
    }
  });
})();
