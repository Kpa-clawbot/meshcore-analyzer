/* === MeshCore Analyzer — perf.js === */
'use strict';

(function () {
  let interval = null;

  async function render(app) {
    app.innerHTML = '<div style="height:100%;overflow-y:auto;padding:16px 24px;"><h2>⚡ Performance Dashboard</h2><div id="perfContent">Loading...</div></div>';
    await refresh();
  }

  async function refresh() {
    const el = document.getElementById('perfContent');
    if (!el) return;
    try {
      const [server, client] = await Promise.all([
        fetch('/api/perf').then(r => r.json()),
        Promise.resolve(window.apiPerf ? window.apiPerf() : null)
      ]);

      // Also fetch health telemetry
      const health = await fetch('/api/health').then(r => r.json()).catch(() => null);

      let html = '';

      // Server overview
      html += `<div style="display:flex;gap:16px;flex-wrap:wrap;margin:16px 0;">
        <div class="perf-card"><div class="perf-num">${server.totalRequests}</div><div class="perf-label">Total Requests</div></div>
        <div class="perf-card"><div class="perf-num">${server.avgMs}ms</div><div class="perf-label">Avg Response</div></div>
        <div class="perf-card"><div class="perf-num">${health ? health.uptimeHuman : Math.round(server.uptime / 60) + 'm'}</div><div class="perf-label">Uptime</div></div>
        <div class="perf-card"><div class="perf-num">${server.slowQueries.length}</div><div class="perf-label">Slow (&gt;100ms)</div></div>
      </div>`;

      // System health (memory, event loop, WS)
      if (health) {
        const m = health.memory, el = health.eventLoop;
        const elColor = el.p95Ms > 500 ? '#ef4444' : el.p95Ms > 100 ? '#f59e0b' : '#22c55e';
        const memColor = m.heapUsed > m.heapTotal * 0.85 ? '#ef4444' : m.heapUsed > m.heapTotal * 0.7 ? '#f59e0b' : '#22c55e';
        html += `<h3>System Health</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num" style="color:${memColor}">${m.heapUsed}MB</div><div class="perf-label">Heap Used / ${m.heapTotal}MB</div></div>
          <div class="perf-card"><div class="perf-num">${m.rss}MB</div><div class="perf-label">RSS</div></div>
          <div class="perf-card"><div class="perf-num" style="color:${elColor}">${el.p95Ms}ms</div><div class="perf-label">Event Loop p95</div></div>
          <div class="perf-card"><div class="perf-num">${el.maxLagMs}ms</div><div class="perf-label">EL Max Lag</div></div>
          <div class="perf-card"><div class="perf-num">${el.currentLagMs}ms</div><div class="perf-label">EL Current</div></div>
          <div class="perf-card"><div class="perf-num">${health.websocket.clients}</div><div class="perf-label">WS Clients</div></div>
        </div>`;

      // Cache stats
      if (server.cache) {
        const c = server.cache;
        const clientCache = _apiCache ? _apiCache.size : 0;
        html += `<h3>Cache</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num">${c.size}</div><div class="perf-label">Server Entries</div></div>
          <div class="perf-card"><div class="perf-num">${c.hits}</div><div class="perf-label">Server Hits</div></div>
          <div class="perf-card"><div class="perf-num">${c.misses}</div><div class="perf-label">Server Misses</div></div>
          <div class="perf-card"><div class="perf-num" style="color:${c.hitRate > 50 ? '#22c55e' : c.hitRate > 20 ? '#f59e0b' : '#ef4444'}">${c.hitRate}%</div><div class="perf-label">Server Hit Rate</div></div>
          <div class="perf-card"><div class="perf-num">${c.staleHits || 0}</div><div class="perf-label">Stale Hits (SWR)</div></div>
          <div class="perf-card"><div class="perf-num">${c.recomputes || 0}</div><div class="perf-label">Recomputes</div></div>
          <div class="perf-card"><div class="perf-num">${clientCache}</div><div class="perf-label">Client Entries</div></div>
        </div>`;
        if (client) {
          html += `<div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
            <div class="perf-card"><div class="perf-num">${client.cacheHits || 0}</div><div class="perf-label">Client Hits</div></div>
            <div class="perf-card"><div class="perf-num">${client.cacheMisses || 0}</div><div class="perf-label">Client Misses</div></div>
            <div class="perf-card"><div class="perf-num" style="color:${(client.cacheHitRate||0) > 50 ? '#22c55e' : '#f59e0b'}">${client.cacheHitRate || 0}%</div><div class="perf-label">Client Hit Rate</div></div>
          </div>`;
        }
      }

      // Packet Store stats
      if (server.packetStore) {
        const ps = server.packetStore;
        html += `<h3>In-Memory Packet Store</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num">${ps.inMemory.toLocaleString()}</div><div class="perf-label">Packets in RAM</div></div>
          <div class="perf-card"><div class="perf-num">${ps.estimatedMB}MB</div><div class="perf-label">Memory Used</div></div>
          <div class="perf-card"><div class="perf-num">${ps.maxMB}MB</div><div class="perf-label">Memory Limit</div></div>
          <div class="perf-card"><div class="perf-num">${ps.queries.toLocaleString()}</div><div class="perf-label">Queries Served</div></div>
          <div class="perf-card"><div class="perf-num">${ps.inserts.toLocaleString()}</div><div class="perf-label">Live Inserts</div></div>
          <div class="perf-card"><div class="perf-num">${ps.evicted.toLocaleString()}</div><div class="perf-label">Evicted</div></div>
          <div class="perf-card"><div class="perf-num">${ps.indexes.byHash.toLocaleString()}</div><div class="perf-label">Unique Hashes</div></div>
          <div class="perf-card"><div class="perf-num">${ps.indexes.byObserver}</div><div class="perf-label">Observers</div></div>
          <div class="perf-card"><div class="perf-num">${ps.indexes.byNode.toLocaleString()}</div><div class="perf-label">Indexed Nodes</div></div>
        </div>`;
      }

      // Server endpoints table
      const eps = Object.entries(server.endpoints);
      if (eps.length) {
        html += '<h3>Server Endpoints (sorted by total time)</h3>';
        html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th>Endpoint</th><th>Count</th><th>Avg</th><th>P50</th><th>P95</th><th>Max</th><th>Total</th></tr></thead><tbody>';
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
        html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th>Endpoint</th><th>Count</th><th>Avg</th><th>Max</th><th>Total</th></tr></thead><tbody>';
        for (const s of client.endpoints) {
          const cls = s.maxMs > 500 ? ' class="perf-slow"' : s.avgMs > 200 ? ' class="perf-warn"' : '';
          html += `<tr${cls}><td><code>${s.path}</code></td><td>${s.count}</td><td>${s.avgMs}ms</td><td>${s.maxMs}ms</td><td>${s.totalMs}ms</td></tr>`;
        }
        html += '</tbody></table></div>';
      }

      // Slow queries
      if (server.slowQueries.length) {
        html += '<h3>Recent Slow Queries (&gt;100ms)</h3>';
        html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th>Time</th><th>Path</th><th>Duration</th><th>Status</th></tr></thead><tbody>';
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
    } catch (err) {
      el.innerHTML = `<p style="color:red">Error: ${err.message}</p>`;
    }
  }

  registerPage('perf', {
    init(app) {
      render(app);
      interval = setInterval(refresh, 5000);
    },
    destroy() {
      if (interval) { clearInterval(interval); interval = null; }
    }
  });
})();
