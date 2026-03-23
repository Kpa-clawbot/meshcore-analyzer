/* === MeshCore Analyzer — nodes.js === */
'use strict';

(function () {
  let nodes = [];
  const PAYLOAD_TYPES = {0:'Request',1:'Response',2:'Direct Msg',3:'ACK',4:'Advert',5:'Channel Msg',7:'Anon Req',8:'Path',9:'Trace'};

  function syncClaimedToFavorites() {
    const myNodes = JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]');
    const favs = getFavorites();
    let changed = false;
    myNodes.forEach(mn => {
      if (!favs.includes(mn.pubkey)) { favs.push(mn.pubkey); changed = true; }
    });
    if (changed) localStorage.setItem('meshcore-favorites', JSON.stringify(favs));
  }

  let counts = {};
  let selectedKey = null;
  let activeTab = 'all';
  let search = '';
  // Sort state: column + direction, persisted to localStorage
  let sortState = (function () {
    try {
      const saved = JSON.parse(localStorage.getItem('meshcore-nodes-sort'));
      if (saved && saved.column && saved.direction) return saved;
    } catch {}
    return { column: 'last_seen', direction: 'desc' };
  })();

  function toggleSort(column) {
    if (sortState.column === column) {
      sortState.direction = sortState.direction === 'asc' ? 'desc' : 'asc';
    } else {
      // Default direction per column type
      const descDefault = ['last_seen', 'advert_count'];
      sortState = { column, direction: descDefault.includes(column) ? 'desc' : 'asc' };
    }
    localStorage.setItem('meshcore-nodes-sort', JSON.stringify(sortState));
  }

  function sortNodes(arr) {
    const col = sortState.column;
    const dir = sortState.direction === 'asc' ? 1 : -1;
    return arr.sort(function (a, b) {
      let va, vb;
      if (col === 'name') {
        va = (a.name || '').toLowerCase(); vb = (b.name || '').toLowerCase();
        if (!a.name && b.name) return 1;
        if (a.name && !b.name) return -1;
        return va < vb ? -dir : va > vb ? dir : 0;
      } else if (col === 'public_key') {
        va = a.public_key || ''; vb = b.public_key || '';
        return va < vb ? -dir : va > vb ? dir : 0;
      } else if (col === 'role') {
        va = (a.role || '').toLowerCase(); vb = (b.role || '').toLowerCase();
        return va < vb ? -dir : va > vb ? dir : 0;
      } else if (col === 'last_seen') {
        va = a.last_seen ? new Date(a.last_seen).getTime() : 0;
        vb = b.last_seen ? new Date(b.last_seen).getTime() : 0;
        return (va - vb) * dir;
      } else if (col === 'advert_count') {
        va = a.advert_count || 0; vb = b.advert_count || 0;
        return (va - vb) * dir;
      }
      return 0;
    });
  }

  function sortArrow(col) {
    if (sortState.column !== col) return '';
    return '<span class="sort-arrow">' + (sortState.direction === 'asc' ? '▲' : '▼') + '</span>';
  }
  let lastHeard = '';
  let wsHandler = null;
  let detailMap = null;

  // ROLE_COLORS loaded from shared roles.js
  const TABS = [
    { key: 'all', label: 'All' },
    { key: 'repeater', label: 'Repeaters' },
    { key: 'room', label: 'Rooms' },
    { key: 'companion', label: 'Companions' },
    { key: 'sensor', label: 'Sensors' },
  ];

  let directNode = null; // set when navigating directly to #/nodes/:pubkey

  let regionChangeHandler = null;

  function init(app, routeParam) {
    directNode = routeParam || null;

    if (directNode) {
      // Full-screen single node view
      app.innerHTML = `<div class="node-fullscreen">
        <div class="node-full-header">
          <button class="detail-back-btn node-back-btn" id="nodeBackBtn" aria-label="Back to nodes">←</button>
          <span class="node-full-title">Loading…</span>
        </div>
        <div class="node-full-body" id="nodeFullBody">
          <div class="text-center text-muted" style="padding:40px">Loading…</div>
        </div>
      </div>`;
      document.getElementById('nodeBackBtn').addEventListener('click', () => { location.hash = '#/nodes'; });
      loadFullNode(directNode);
      // Escape to go back to nodes list
      document.addEventListener('keydown', function nodesEsc(e) {
        if (e.key === 'Escape') {
          document.removeEventListener('keydown', nodesEsc);
          location.hash = '#/nodes';
        }
      });
      return;
    }

    app.innerHTML = `<div class="nodes-page">
      <div class="nodes-topbar">
        <input type="text" class="nodes-search" id="nodeSearch" placeholder="Search nodes by name…" aria-label="Search nodes by name">
        <div class="nodes-counts" id="nodeCounts"></div>
      </div>
      <div id="nodesRegionFilter" class="region-filter-container"></div>
      <div class="split-layout">
        <div class="panel-left" id="nodesLeft"></div>
        <div class="panel-right empty" id="nodesRight"><span>Select a node to view details</span></div>
      </div>
    </div>`;

    RegionFilter.init(document.getElementById('nodesRegionFilter'));
    regionChangeHandler = RegionFilter.onChange(function () { _allNodes = null; loadNodes(); });

    document.getElementById('nodeSearch').addEventListener('input', debounce(e => {
      search = e.target.value;
      loadNodes();
    }, 250));

    loadNodes();
    wsHandler = debouncedOnWS(function (msgs) { if (msgs.some(function (m) { return m.type === 'packet'; })) loadNodes(); });
  }

  async function loadFullNode(pubkey) {
    const body = document.getElementById('nodeFullBody');
    try {
      const [nodeData, healthData] = await Promise.all([
        api('/nodes/' + encodeURIComponent(pubkey), { ttl: CLIENT_TTL.nodeDetail }),
        api('/nodes/' + encodeURIComponent(pubkey) + '/health', { ttl: CLIENT_TTL.nodeDetail }).catch(() => null)
      ]);
      const n = nodeData.node;
      const adverts = (nodeData.recentAdverts || []).sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
      const title = document.querySelector('.node-full-title');
      if (title) title.textContent = n.name || pubkey.slice(0, 12);

      const roleColor = ROLE_COLORS[n.role] || '#6b7280';
      const hasLoc = n.lat != null && n.lon != null;

      // Health stats
      const h = healthData || {};
      const stats = h.stats || {};
      const observers = h.observers || [];
      const recent = h.recentPackets || [];
      const lastHeard = stats.lastHeard;
      // Thresholds based on MeshCore advert intervals:
      // Repeaters/rooms: flood advert every 12-24h, so degraded after 24h, silent after 72h
      // Companions/sensors: user-initiated adverts, shorter thresholds
      const role = (n.role || '').toLowerCase();
      const lastHeardTime = lastHeard || n.last_seen;
      const lastHeardMs = lastHeardTime ? new Date(lastHeardTime).getTime() : 0;
      const status = getNodeStatus(role, lastHeardMs);
      const statusLabel = status === 'active' ? '🟢 Active' : '⚪ Stale';
      const statusAge = lastHeardMs ? (Date.now() - lastHeardMs) : Infinity;
      let statusExplanation = '';
      if (status === 'active') {
        statusExplanation = 'Last heard ' + (lastHeard ? timeAgo(lastHeard) : 'unknown');
      } else {
        const ageDays = Math.floor(statusAge / 86400000);
        const ageHours = Math.floor(statusAge / 3600000);
        const ageStr = ageDays >= 1 ? ageDays + 'd' : ageHours + 'h';
        const isInfra = role === 'repeater' || role === 'room';
        const reason = isInfra
          ? 'repeaters typically advertise every 12-24h'
          : 'companions only advertise when user initiates, this may be normal';
        statusExplanation = 'Not heard for ' + ageStr + ' — ' + reason;
      }

      body.innerHTML = `
        <div class="node-full-card" style="padding:12px 16px;margin-bottom:8px">
          <div class="node-detail-name" style="font-size:20px">${escapeHtml(n.name || '(unnamed)')}</div>
          <div style="margin:4px 0 6px"><span class="badge" style="background:${roleColor}20;color:${roleColor}">${n.role}</span> ${n.hash_size ? `<span class="badge" style="background:var(--nav-bg);color:var(--nav-text);font-family:var(--mono)">${n.public_key.slice(0, n.hash_size * 2).toUpperCase()}</span>` : ''} ${n.hash_size_inconsistent ? `<a href="#/nodes/${encodeURIComponent(n.public_key)}?section=node-packets" class="badge" style="background:var(--status-yellow);color:#000;font-size:10px;cursor:pointer;text-decoration:none">⚠️ variable hash size</a>` : ''} ${statusLabel}</div>
          ${n.hash_size_inconsistent ? `<div style="font-size:11px;color:var(--text-muted);margin:-2px 0 6px;padding:6px 10px;background:var(--surface-2);border-radius:4px;border-left:3px solid var(--status-yellow)">Adverts show varying hash sizes (<strong>${(n.hash_sizes_seen||[]).join('-byte, ')}-byte</strong>). This is a <a href="https://github.com/meshcore-dev/MeshCore/commit/fcfdc5f" target="_blank" style="color:var(--accent)">known bug</a> where automatic adverts ignore the configured multibyte path setting. Fixed in <a href="https://github.com/meshcore-dev/MeshCore/releases/tag/repeater-v1.14.1" target="_blank" style="color:var(--accent)">repeater v1.14.1</a>.</div>` : ''}
          <div class="node-detail-key mono" style="font-size:11px;word-break:break-all;margin-bottom:6px">${n.public_key}</div>
          <div>
            <button class="btn-primary" id="copyUrlBtn" style="font-size:12px;padding:4px 10px">📋 Copy URL</button>
            <a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="btn-primary" style="display:inline-block;margin-left:6px;text-decoration:none;font-size:12px;padding:4px 10px">📊 Analytics</a>
          </div>
        </div>

        <div class="node-top-row">
          ${hasLoc ? `<div class="node-map-wrap"><div id="nodeFullMap" class="node-detail-map" style="height:100%;min-height:200px;border-radius:8px;overflow:hidden"></div></div>` : ''}
          <div class="node-qr-wrap${hasLoc ? '' : ' node-qr-wrap--full'}">
            <div class="node-qr" id="nodeFullQrCode"></div>
            <div class="mono" style="font-size:10px;color:var(--text-muted);margin-top:8px;word-break:break-all;text-align:center;max-width:180px">${n.public_key.slice(0, 16)}…${n.public_key.slice(-8)}</div>
          </div>
        </div>

        <table class="node-stats-table" id="node-stats">
          <tr><td>Status</td><td>${statusLabel} <span style="font-size:11px;color:var(--text-muted);margin-left:4px">${statusExplanation}</span></td></tr>
          <tr><td>Last Heard</td><td>${lastHeard ? timeAgo(lastHeard) : (n.last_seen ? timeAgo(n.last_seen) : '—')}</td></tr>
          <tr><td>First Seen</td><td>${n.first_seen ? new Date(n.first_seen).toLocaleString() : '—'}</td></tr>
          <tr><td>Total Packets</td><td>${stats.totalTransmissions || stats.totalPackets || n.advert_count || 0}${stats.totalObservations && stats.totalObservations !== (stats.totalTransmissions || stats.totalPackets) ? ' <span class="text-muted" style="font-size:0.85em">(seen ' + stats.totalObservations + '×)</span>' : ''}</td></tr>
          <tr><td>Packets Today</td><td>${stats.packetsToday || 0}</td></tr>
          ${stats.avgSnr != null ? `<tr><td>Avg SNR</td><td>${stats.avgSnr.toFixed(1)} dB</td></tr>` : ''}
          ${stats.avgHops ? `<tr><td>Avg Hops</td><td>${stats.avgHops}</td></tr>` : ''}
          ${hasLoc ? `<tr><td>Location</td><td>${n.lat.toFixed(5)}, ${n.lon.toFixed(5)}</td></tr>` : ''}
          <tr><td>Hash Prefix</td><td>${n.hash_size ? '<code style="font-family:var(--mono);font-weight:700">' + n.public_key.slice(0, n.hash_size * 2).toUpperCase() + '</code> (' + n.hash_size + '-byte)' : 'Unknown'}${n.hash_size_inconsistent ? ' <span style="color:var(--status-yellow);cursor:help" title="Seen: ' + (n.hash_sizes_seen || []).join(', ') + '-byte">⚠️ varies</span>' : ''}</td></tr>
        </table>

        ${observers.length ? `<div class="node-full-card" id="node-observers">
          ${(() => { const regions = [...new Set(observers.map(o => o.iata).filter(Boolean))]; return regions.length ? `<div style="margin-bottom:8px"><strong>Regions:</strong> ${regions.map(r => '<span class="badge" style="margin:0 2px">' + escapeHtml(r) + '</span>').join(' ')}</div>` : ''; })()}
          <h4>Heard By (${observers.length} observer${observers.length > 1 ? 's' : ''})</h4>
          <table class="data-table" style="font-size:12px">
            <thead><tr><th>Observer</th><th>Region</th><th>Packets</th><th>Avg SNR</th><th>Avg RSSI</th></tr></thead>
            <tbody>
              ${observers.map(o => `<tr>
                <td style="font-weight:600">${escapeHtml(o.observer_name || o.observer_id)}</td>
                <td>${o.iata ? escapeHtml(o.iata) : '—'}</td>
                <td>${o.packetCount}</td>
                <td>${o.avgSnr != null ? o.avgSnr.toFixed(1) + ' dB' : '—'}</td>
                <td>${o.avgRssi != null ? o.avgRssi.toFixed(0) + ' dBm' : '—'}</td>
              </tr>`).join('')}
            </tbody>
          </table>
        </div>` : ''}

        <div class="node-full-card" id="fullPathsSection">
          <h4>Paths Through This Node</h4>
          <div id="fullPathsContent"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading paths…</div></div>
        </div>

        <div class="node-full-card" id="node-packets">
          <h4>Recent Packets (${adverts.length})</h4>
          <div class="node-activity-list">
            ${adverts.length ? adverts.map(p => {
              let decoded; try { decoded = JSON.parse(p.decoded_json); } catch {}
              const typeLabel = p.payload_type === 4 ? '📡 Advert' : p.payload_type === 5 ? '💬 Channel' : p.payload_type === 2 ? '✉️ DM' : '📦 Packet';
              const detail = decoded?.text ? ': ' + escapeHtml(truncate(decoded.text, 50)) : decoded?.name ? ' — ' + escapeHtml(decoded.name) : '';
              const obs = p.observer_name || p.observer_id;
              const snr = p.snr != null ? ` · SNR ${p.snr}dB` : '';
              const rssi = p.rssi != null ? ` · RSSI ${p.rssi}dBm` : '';
              const obsBadge = p.observation_count > 1 ? ` <span class="badge badge-obs" title="Seen ${p.observation_count} times">👁 ${p.observation_count}</span>` : '';
              // Show hash size per advert if inconsistent
              let hashSizeBadge = '';
              if (n.hash_size_inconsistent && p.payload_type === 4 && p.raw_hex) {
                const pb = parseInt(p.raw_hex.slice(2, 4), 16);
                const hs = ((pb >> 6) & 0x3) + 1;
                const hsColor = hs >= 3 ? '#16a34a' : hs === 2 ? '#86efac' : '#f97316';
                const hsFg = hs === 2 ? '#064e3b' : '#fff';
                hashSizeBadge = ` <span class="badge" style="background:${hsColor};color:${hsFg};font-size:9px;font-family:var(--mono)">${hs}B</span>`;
              }
              return `<div class="node-activity-item">
                <span class="node-activity-time">${timeAgo(p.timestamp)}</span>
                <span>${typeLabel}${detail}${hashSizeBadge}${obsBadge}${obs ? ' via ' + escapeHtml(obs) : ''}${snr}${rssi}</span>
                <a href="#/packets/${p.hash}" class="ch-analyze-link" style="margin-left:8px;font-size:0.8em">Analyze →</a>
              </div>`;
            }).join('') : '<div class="text-muted">No recent packets</div>'}
          </div>
        </div>`;

      // Map
      if (hasLoc) {
        try {
          if (detailMap) { detailMap.remove(); detailMap = null; }
          detailMap = L.map('nodeFullMap', { zoomControl: true, attributionControl: false }).setView([n.lat, n.lon], 13);
          L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 18 }).addTo(detailMap);
          L.marker([n.lat, n.lon]).addTo(detailMap).bindPopup(n.name || n.public_key.slice(0, 12));
          setTimeout(() => detailMap.invalidateSize(), 100);
        } catch {}
      }

      // Copy URL
      const nodeUrl = location.origin + '#/nodes/' + encodeURIComponent(n.public_key);
      document.getElementById('copyUrlBtn')?.addEventListener('click', () => {
        navigator.clipboard.writeText(nodeUrl).then(() => {
          const btn = document.getElementById('copyUrlBtn');
          btn.textContent = '✅ Copied!';
          setTimeout(() => btn.textContent = '📋 Copy URL', 2000);
        }).catch(() => {});
      });

      // Deep-link scroll: ?section=node-packets or ?section=node-packets
      const hashParams = location.hash.split('?')[1] || '';
      const urlParams = new URLSearchParams(hashParams);
      const scrollTarget = urlParams.get('section') || (urlParams.has('highlight') ? 'node-packets' : null);
      if (scrollTarget) {
        const targetEl = document.getElementById(scrollTarget);
        if (targetEl) setTimeout(() => targetEl.scrollIntoView({ behavior: 'smooth', block: 'start' }), 300);
      }

      // QR code for full-screen view
      const qrFullEl = document.getElementById('nodeFullQrCode');
      if (qrFullEl && typeof qrcode === 'function') {
        try {
          const typeMap = { companion: 1, repeater: 2, room: 3, sensor: 4 };
          const contactType = typeMap[(n.role || '').toLowerCase()] || 2;
          const meshcoreUrl = `meshcore://contact/add?name=${encodeURIComponent(n.name || 'Unknown')}&public_key=${n.public_key}&type=${contactType}`;
          const qr = qrcode(0, 'M');
          qr.addData(meshcoreUrl);
          qr.make();
          qrFullEl.innerHTML = qr.createSvgTag(3, 0);
          const svg = qrFullEl.querySelector('svg');
          if (svg) { svg.style.display = 'block'; svg.style.margin = '0 auto'; }
        } catch {}
      }

      // Fetch paths through this node (full-screen view)
      api('/nodes/' + encodeURIComponent(n.public_key) + '/paths', { ttl: CLIENT_TTL.nodeDetail }).then(pathData => {
        const el = document.getElementById('fullPathsContent');
        if (!el) return;
        if (!pathData || !pathData.paths || !pathData.paths.length) {
          el.innerHTML = '<div class="text-muted" style="padding:8px">No paths observed through this node</div>';
          return;
        }
        document.querySelector('#fullPathsSection h4').textContent = `Paths Through This Node (${pathData.totalPaths} unique, ${pathData.totalTransmissions} transmissions)`;
        const COLLAPSE_LIMIT = 10;
        function renderPaths(paths) {
          return paths.map(p => {
            const chain = p.hops.map(h => {
              const isThis = h.pubkey === n.public_key;
              if (window.HopDisplay) {
                const entry = { name: h.name, pubkey: h.pubkey, ambiguous: h.ambiguous, conflicts: h.conflicts, totalGlobal: h.totalGlobal, totalRegional: h.totalRegional, globalFallback: h.globalFallback, unreliable: h.unreliable };
                const html = HopDisplay.renderHop(h.prefix, entry);
                return isThis ? html.replace('class="', 'class="hop-current ') : html;
              }
              const name = escapeHtml(h.name || h.prefix);
              const link = h.pubkey ? `<a href="#/nodes/${encodeURIComponent(h.pubkey)}" style="${isThis ? 'font-weight:700;color:var(--accent, #3b82f6)' : ''}">${name}</a>` : `<span>${name}</span>`;
              return link;
            }).join(' → ');
            return `<div style="padding:6px 0;border-bottom:1px solid var(--border);font-size:12px">
              <div>${chain}</div>
              <div style="color:var(--text-muted);margin-top:2px">${p.count}× · last ${timeAgo(p.lastSeen)} · <a href="#/packets/${p.sampleHash}" class="ch-analyze-link">Analyze →</a></div>
            </div>`;
          }).join('');
        }
        if (pathData.paths.length <= COLLAPSE_LIMIT) {
          el.innerHTML = renderPaths(pathData.paths);
        } else {
          el.innerHTML = renderPaths(pathData.paths.slice(0, COLLAPSE_LIMIT)) +
            `<button id="showAllFullPaths" class="btn-primary" style="margin-top:8px;font-size:11px;padding:4px 12px">Show all ${pathData.paths.length} paths</button>`;
          document.getElementById('showAllFullPaths').addEventListener('click', function() {
            el.innerHTML = renderPaths(pathData.paths);
          });
        }
      }).catch(() => {
        const el = document.getElementById('fullPathsContent');
        if (el) el.innerHTML = '<div class="text-muted" style="padding:8px">Failed to load paths</div>';
      });

    } catch (e) {
      body.innerHTML = `<div class="text-muted" style="padding:40px">Failed to load node: ${e.message}</div>`;
    }
  }

    function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (detailMap) { detailMap.remove(); detailMap = null; }
    if (regionChangeHandler) RegionFilter.offChange(regionChangeHandler);
    regionChangeHandler = null;
    nodes = [];
    selectedKey = null;
  }

  let _allNodes = null; // cached full node list

  async function loadNodes() {
    try {
      // Fetch all nodes once, filter client-side
      if (!_allNodes) {
        const params = new URLSearchParams({ limit: '5000' });
        const rp = RegionFilter.getRegionParam();
        if (rp) params.set('region', rp);
        const data = await api('/nodes?' + params, { ttl: CLIENT_TTL.nodeList });
        _allNodes = data.nodes || [];
        counts = data.counts || {};
      }

      // Client-side filtering
      let filtered = _allNodes;
      if (activeTab !== 'all') filtered = filtered.filter(n => (n.role || '').toLowerCase() === activeTab);
      if (search) {
        const q = search.toLowerCase();
        filtered = filtered.filter(n => (n.name || '').toLowerCase().includes(q) || (n.public_key || '').toLowerCase().includes(q));
      }
      if (lastHeard) {
        const ms = { '1h': 3600000, '6h': 21600000, '24h': 86400000, '7d': 604800000, '30d': 2592000000 }[lastHeard];
        if (ms) filtered = filtered.filter(n => n.last_seen && (Date.now() - new Date(n.last_seen).getTime()) < ms);
      }
      nodes = filtered;

      // Defensive filter: hide nodes with obviously corrupted data
      nodes = nodes.filter(n => {
        if (n.public_key && n.public_key.length < 16) return false;
        if (!n.name && !n.advert_count) return false;
        return true;
      });

      // Ensure claimed nodes are always present even if not in current page
      const myNodes = JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]');
      const existingKeys = new Set(nodes.map(n => n.public_key));
      const missing = myNodes.filter(mn => !existingKeys.has(mn.pubkey));
      if (missing.length) {
        const fetched = await Promise.allSettled(
          missing.map(mn => api('/nodes/' + encodeURIComponent(mn.pubkey), { ttl: CLIENT_TTL.nodeDetail }))
        );
        fetched.forEach(r => {
          if (r.status === 'fulfilled' && r.value && r.value.public_key) nodes.push(r.value);
        });
      }

      // Auto-sync claimed → favorites
      syncClaimedToFavorites();

      renderCounts();
      renderLeft();
    } catch (e) {
      console.error('Failed to load nodes:', e);
      const tbody = document.getElementById('nodesBody');
      if (tbody) tbody.innerHTML = '<tr><td colspan="6" class="text-center" style="padding:24px;color:var(--error,#ef4444)"><div role="alert" aria-live="polite">Failed to load nodes. Please try again.</div></td></tr>';
    }
  }

  function renderCounts() {
    const el = document.getElementById('nodeCounts');
    if (!el) return;
    el.innerHTML = [
      { k: 'repeaters', l: 'Repeaters', c: ROLE_COLORS.repeater },
      { k: 'rooms', l: 'Rooms', c: ROLE_COLORS.room || '#6b7280' },
      { k: 'companions', l: 'Companions', c: ROLE_COLORS.companion },
      { k: 'sensors', l: 'Sensors', c: ROLE_COLORS.sensor },
    ].map(r => `<span class="node-count-pill" style="background:${r.c}">${counts[r.k] || 0} ${r.l}</span>`).join('');
  }

  function renderLeft() {
    const el = document.getElementById('nodesLeft');
    if (!el) return;

    el.innerHTML = `
      <div class="nodes-tabs-bar">
        <div class="nodes-tabs" id="nodeTabs">
          ${TABS.map(t => `<button class="node-tab ${activeTab === t.key ? 'active' : ''}" data-tab="${t.key}">${t.label}</button>`).join('')}
        </div>
        <div class="nodes-filters">
          <select id="nodeLastHeard" aria-label="Filter by last heard time">
            <option value="">Last Heard: Any</option>
            <option value="1h" ${lastHeard==='1h'?'selected':''}>1 hour</option>
            <option value="6h" ${lastHeard==='6h'?'selected':''}>6 hours</option>
            <option value="24h" ${lastHeard==='24h'?'selected':''}>24 hours</option>
            <option value="7d" ${lastHeard==='7d'?'selected':''}>7 days</option>
            <option value="30d" ${lastHeard==='30d'?'selected':''}>30 days</option>
          </select>
        </div>
      </div>
      <table class="data-table" id="nodesTable">
        <thead><tr>
          <th class="sortable${sortState.column==='name'?' sort-active':''}" data-sort="name">Name${sortArrow('name')}</th>
          <th class="sortable${sortState.column==='public_key'?' sort-active':''}" data-sort="public_key">Public Key${sortArrow('public_key')}</th>
          <th class="sortable${sortState.column==='role'?' sort-active':''}" data-sort="role">Role${sortArrow('role')}</th>
          <th class="sortable${sortState.column==='last_seen'?' sort-active':''}" data-sort="last_seen">Last Seen${sortArrow('last_seen')}</th>
          <th class="sortable${sortState.column==='advert_count'?' sort-active':''}" data-sort="advert_count">Adverts${sortArrow('advert_count')}</th>
        </tr></thead>
        <tbody id="nodesBody"></tbody>
      </table>`;

    // Tab clicks
    const nodeTabs = document.getElementById('nodeTabs');
    initTabBar(nodeTabs);
    el.querySelectorAll('.node-tab').forEach(btn => {
      btn.addEventListener('click', () => { activeTab = btn.dataset.tab; loadNodes(); });
    });

    // Filter changes
    document.getElementById('nodeLastHeard').addEventListener('change', e => { lastHeard = e.target.value; loadNodes(); });

    // Sortable column headers
    el.querySelectorAll('th.sortable').forEach(th => {
      th.addEventListener('click', () => { toggleSort(th.dataset.sort); renderLeft(); });
    });

    // Delegated click/keyboard handler for table rows
    const tbody = document.getElementById('nodesBody');
    if (tbody) {
      const handler = (e) => {
        const row = e.target.closest('tr[data-action="select"]');
        if (!row) return;
        if (e.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return;
        if (e.type === 'keydown') e.preventDefault();
        selectNode(row.dataset.value);
      };
      tbody.addEventListener('click', handler);
      tbody.addEventListener('keydown', handler);
    }

    // Escape to close node detail panel
    document.addEventListener('keydown', function nodesPanelEsc(e) {
      if (e.key === 'Escape') {
        const panel = document.getElementById('nodesRight');
        if (panel && !panel.classList.contains('empty')) {
          panel.classList.add('empty');
          panel.innerHTML = '<span>Select a node to view details</span>';
          selectedKey = null;
          renderRows();
        }
      }
    });

    renderRows();
  }

  function renderRows() {
    const tbody = document.getElementById('nodesBody');
    if (!tbody) return;

    if (!nodes.length) {
      tbody.innerHTML = '<tr><td colspan="6" class="text-center text-muted" style="padding:24px">No nodes found</td></tr>';
      return;
    }

    // Claimed ("My Mesh") nodes always on top, then favorites, then sort
    const myNodes = JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]');
    const myKeys = new Set(myNodes.map(n => n.pubkey));
    const favs = getFavorites();
    const sorted = sortNodes([...nodes]);
    // Stable re-sort: claimed first, then favorites, preserving sort within each group
    sorted.sort((a, b) => {
      const aMy = myKeys.has(a.public_key) ? 0 : 1;
      const bMy = myKeys.has(b.public_key) ? 0 : 1;
      if (aMy !== bMy) return aMy - bMy;
      const aFav = favs.includes(a.public_key) ? 0 : 1;
      const bFav = favs.includes(b.public_key) ? 0 : 1;
      return aFav - bFav;
    });

    tbody.innerHTML = sorted.map(n => {
      const roleColor = ROLE_COLORS[n.role] || '#6b7280';
      const isClaimed = myKeys.has(n.public_key);
      const status = getNodeStatus(n.role || 'companion', n.last_seen ? new Date(n.last_seen).getTime() : 0);
      const lastSeenClass = status === 'active' ? 'last-seen-active' : 'last-seen-stale';
      return `<tr data-key="${n.public_key}" data-action="select" data-value="${n.public_key}" tabindex="0" role="row" class="${selectedKey === n.public_key ? 'selected' : ''}${isClaimed ? ' claimed-row' : ''}">
        <td>${favStar(n.public_key, 'node-fav')}${isClaimed ? '<span class="claimed-badge" title="My Mesh">★</span> ' : ''}<strong>${n.name || '(unnamed)'}</strong></td>
        <td class="mono">${truncate(n.public_key, 16)}</td>
        <td><span class="badge" style="background:${roleColor}20;color:${roleColor}">${n.role}</span></td>
        <td class="${lastSeenClass}">${timeAgo(n.last_seen)}</td>
        <td>${n.advert_count || 0}</td>
      </tr>`;
    }).join('');
    bindFavStars(tbody);
    makeColumnsResizable('#nodesTable', 'meshcore-nodes-col-widths');
  }

  async function selectNode(pubkey) {
    // On mobile, navigate to full-screen node view
    if (window.innerWidth <= 640) {
      location.hash = '#/nodes/' + encodeURIComponent(pubkey);
      return;
    }
    selectedKey = pubkey;
    renderRows();
    const panel = document.getElementById('nodesRight');
    panel.classList.remove('empty');
    panel.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading…</div>';

    try {
      const [data, healthData] = await Promise.all([
        api('/nodes/' + encodeURIComponent(pubkey), { ttl: CLIENT_TTL.nodeDetail }),
        api('/nodes/' + encodeURIComponent(pubkey) + '/health', { ttl: CLIENT_TTL.nodeDetail }).catch(() => null)
      ]);
      data.healthData = healthData;
      renderDetail(panel, data);
    } catch (e) {
      panel.innerHTML = `<div class="text-muted">Error: ${e.message}</div>`;
    }
  }

  function renderDetail(panel, data) {
    const n = data.node;
    const adverts = (data.recentAdverts || []).sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
    const h = data.healthData || {};
    const stats = h.stats || {};
    const observers = h.observers || [];
    const recent = h.recentPackets || [];
    const roleColor = ROLE_COLORS[n.role] || '#6b7280';
    const hasLoc = n.lat != null && n.lon != null;
    const nodeUrl = location.origin + '#/nodes/' + encodeURIComponent(n.public_key);

    // Status calculation
    const lastHeard = stats.lastHeard;
    const lastHeardTime = lastHeard || n.last_seen;
    const lastHeardMs = lastHeardTime ? new Date(lastHeardTime).getTime() : 0;
    const role = (n.role || '').toLowerCase();
    const status = getNodeStatus(role, lastHeardMs);
    const statusLabel = status === 'active' ? '🟢 Active' : '⚪ Stale';
    const totalPackets = stats.totalTransmissions || stats.totalPackets || n.advert_count || 0;

    panel.innerHTML = `
      <div class="node-detail">
        <div class="node-detail-name">${escapeHtml(n.name || '(unnamed)')}</div>
        <div class="node-detail-role"><span class="badge" style="background:${roleColor}20;color:${roleColor}">${n.role}</span> ${n.hash_size ? `<span class="badge" style="background:var(--nav-bg);color:var(--nav-text);font-family:var(--mono)">${n.public_key.slice(0, n.hash_size * 2).toUpperCase()}</span>` : ''} ${n.hash_size_inconsistent ? `<a href="#/nodes/${encodeURIComponent(n.public_key)}?section=node-packets" class="badge" style="background:var(--status-yellow);color:#000;font-size:10px;cursor:pointer;text-decoration:none">⚠️ variable hash size</a>` : ''} ${statusLabel}
          <a href="#/nodes/${encodeURIComponent(n.public_key)}" class="btn-primary" style="display:inline-block;text-decoration:none;font-size:11px;padding:2px 8px;margin-left:8px">🔍 Details</a>
          <a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="btn-primary" style="display:inline-block;margin-left:4px;text-decoration:none;font-size:11px;padding:2px 8px">📊 Analytics</a>
        </div>

        ${hasLoc ? `<div class="node-map-qr-wrap">
          <div class="node-map-container node-detail-map" id="nodeMap" style="border-radius:8px;overflow:hidden;"></div>
          <div class="node-map-qr-overlay node-qr" id="nodeQrCode"></div>
        </div>` : `<div class="node-qr" id="nodeQrCode" style="margin:8px 0"></div>`}

        <div class="node-detail-section">
          <div class="node-detail-key mono" style="font-size:11px;word-break:break-all;margin-bottom:4px">${n.public_key}</div>
        </div>

        <div class="node-detail-section">
          <h4>Overview</h4>
          <dl class="detail-meta">
            <dt>Last Heard</dt><dd>${lastHeard ? timeAgo(lastHeard) : (n.last_seen ? timeAgo(n.last_seen) : '—')}</dd>
            <dt>First Seen</dt><dd>${n.first_seen ? new Date(n.first_seen).toLocaleString() : '—'}</dd>
            <dt>Total Packets</dt><dd>${totalPackets}</dd>
            <dt>Packets Today</dt><dd>${stats.packetsToday || 0}</dd>
            ${stats.avgSnr != null ? `<dt>Avg SNR</dt><dd>${stats.avgSnr.toFixed(1)} dB</dd>` : ''}
            ${stats.avgHops ? `<dt>Avg Hops</dt><dd>${stats.avgHops}</dd>` : ''}
            ${hasLoc ? `<dt>Location</dt><dd>${n.lat.toFixed(5)}, ${n.lon.toFixed(5)}</dd>` : ''}
          </dl>
        </div>

        ${observers.length ? `<div class="node-detail-section">
          ${(() => { const regions = [...new Set(observers.map(o => o.iata).filter(Boolean))]; return regions.length ? `<div style="margin-bottom:6px;font-size:12px"><strong>Regions:</strong> ${regions.join(', ')}</div>` : ''; })()}
          <h4>Heard By (${observers.length} observer${observers.length > 1 ? 's' : ''})</h4>
          <div class="observer-list">
            ${observers.map(o => `<div class="observer-row" style="display:flex;justify-content:space-between;align-items:center;padding:4px 0;border-bottom:1px solid var(--border);font-size:12px">
              <span style="font-weight:600">${escapeHtml(o.observer_name || o.observer_id)}${o.iata ? ' <span class="badge" style="font-size:10px">' + escapeHtml(o.iata) + '</span>' : ''}</span>
              <span style="color:var(--text-muted)">${o.packetCount} pkts · ${o.avgSnr != null ? 'SNR ' + o.avgSnr.toFixed(1) + 'dB' : ''}${o.avgRssi != null ? ' · RSSI ' + o.avgRssi.toFixed(0) : ''}</span>
            </div>`).join('')}
          </div>
        </div>` : ''}

        <div class="node-detail-section" id="pathsSection">
          <h4>Paths Through This Node</h4>
          <div id="pathsContent"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading paths…</div></div>
        </div>

        <div class="node-detail-section">
          <h4>Recent Packets (${adverts.length})</h4>
          <div id="advertTimeline">
            ${adverts.length ? adverts.map(a => {
              let decoded;
              try { decoded = JSON.parse(a.decoded_json); } catch {}
              const pType = PAYLOAD_TYPES[a.payload_type] || 'Packet';
              const icon = a.payload_type === 4 ? '📡' : a.payload_type === 5 ? '💬' : a.payload_type === 2 ? '✉️' : '📦';
              const detail = decoded?.text ? ': ' + escapeHtml(truncate(decoded.text, 50)) : decoded?.name ? ' — ' + escapeHtml(decoded.name) : '';
              const obs = a.observer_name || a.observer_id;
              return `<div class="advert-entry">
                <span class="advert-dot" style="background:${roleColor}"></span>
                <div class="advert-info">
                  <strong>${timeAgo(a.timestamp)}</strong> ${icon} ${pType}${detail}
                  ${a.observation_count > 1 ? ' <span class="badge badge-obs">👁 ' + a.observation_count + '</span>' : ''}
                  ${obs ? ' via ' + escapeHtml(obs) : ''}
                  ${a.snr != null ? ` · SNR ${a.snr}dB` : ''}${a.rssi != null ? ` · RSSI ${a.rssi}dBm` : ''}
                  <br><a href="#/packets/${a.hash}" class="ch-analyze-link">Analyze →</a>
                </div>
              </div>`;
            }).join('') : '<div class="text-muted" style="padding:8px">No recent packets</div>'}
          </div>
        </div>
      </div>`;

    // Init map
    if (hasLoc) {
      try {
        if (detailMap) { detailMap.remove(); detailMap = null; }
        detailMap = L.map('nodeMap', { zoomControl: false, attributionControl: false }).setView([n.lat, n.lon], 13);
        L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 18 }).addTo(detailMap);
        L.marker([n.lat, n.lon]).addTo(detailMap).bindPopup(n.name || n.public_key.slice(0, 12));
        setTimeout(() => detailMap.invalidateSize(), 100);
      } catch {}
    }


    // QR code — meshcore://contact/add format (scannable by MeshCore app)
    const qrEl = document.getElementById('nodeQrCode');
    if (qrEl && typeof qrcode === 'function') {
      try {
        const typeMap = { companion: 1, repeater: 2, room: 3, sensor: 4 };
        const contactType = typeMap[(n.role || '').toLowerCase()] || 2;
        const meshcoreUrl = `meshcore://contact/add?name=${encodeURIComponent(n.name || 'Unknown')}&public_key=${n.public_key}&type=${contactType}`;
        const qr = qrcode(0, 'M');
        qr.addData(meshcoreUrl);
        qr.make();
        const isOverlay = !!qrEl.closest('.node-map-qr-overlay');
        qrEl.innerHTML = qr.createSvgTag(3, 0);
        const svg = qrEl.querySelector('svg');
        if (svg) {
          svg.style.display = 'block'; svg.style.margin = '0 auto';
          // Make QR background transparent for map overlay
          if (isOverlay) {
            svg.querySelectorAll('rect').forEach(r => {
              const fill = (r.getAttribute('fill') || '').toLowerCase();
              if (fill === '#ffffff' || fill === 'white' || fill === '#fff') {
                r.setAttribute('fill', 'transparent');
              }
            });
          }
        }
      } catch {}
    }

    // Fetch paths through this node
    api('/nodes/' + encodeURIComponent(n.public_key) + '/paths', { ttl: CLIENT_TTL.nodeDetail }).then(pathData => {
      const el = document.getElementById('pathsContent');
      if (!el) return;
      if (!pathData || !pathData.paths || !pathData.paths.length) {
        el.innerHTML = '<div class="text-muted" style="padding:8px">No paths observed through this node</div>';
        document.querySelector('#pathsSection h4').textContent = 'Paths Through This Node';
        return;
      }
      document.querySelector('#pathsSection h4').textContent = `Paths Through This Node (${pathData.totalPaths} unique path${pathData.totalPaths !== 1 ? 's' : ''}, ${pathData.totalTransmissions} transmissions)`;
      const COLLAPSE_LIMIT = 10;
      const showAll = pathData.paths.length <= COLLAPSE_LIMIT;
      function renderPaths(paths) {
        return paths.map(p => {
          const chain = p.hops.map(h => {
            const isThis = h.pubkey === n.public_key;
            const name = escapeHtml(h.name || h.prefix);
            const link = h.pubkey ? `<a href="#/nodes/${encodeURIComponent(h.pubkey)}" style="${isThis ? 'font-weight:700;color:var(--accent, #3b82f6)' : ''}">${name}</a>` : `<span>${name}</span>`;
            return link;
          }).join(' → ');
          return `<div style="padding:6px 0;border-bottom:1px solid var(--border);font-size:12px">
            <div>${chain}</div>
            <div style="color:var(--text-muted);margin-top:2px">${p.count}× · last ${timeAgo(p.lastSeen)} · <a href="#/packets/${p.sampleHash}" class="ch-analyze-link">Analyze →</a></div>
          </div>`;
        }).join('');
      }
      if (showAll) {
        el.innerHTML = renderPaths(pathData.paths);
      } else {
        el.innerHTML = renderPaths(pathData.paths.slice(0, COLLAPSE_LIMIT)) +
          `<button id="showAllPaths" class="btn-primary" style="margin-top:8px;font-size:11px;padding:4px 12px">Show all ${pathData.paths.length} paths</button>`;
        document.getElementById('showAllPaths').addEventListener('click', function() {
          el.innerHTML = renderPaths(pathData.paths);
        });
      }
    }).catch(() => {
      const el = document.getElementById('pathsContent');
      if (el) el.innerHTML = '<div class="text-muted" style="padding:8px">Failed to load paths</div>';
    });
  }

  registerPage('nodes', { init, destroy });
})();
