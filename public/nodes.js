/* === MeshCore Analyzer — nodes.js === */
'use strict';

(function () {
  let nodes = [];
  const PAYLOAD_TYPES = {0:'Request',1:'Response',2:'Direct Msg',3:'ACK',4:'Advert',5:'Channel Msg',7:'Anon Req',8:'Path',9:'Trace'};

  let counts = {};
  let selectedKey = null;
  let activeTab = 'all';
  let search = '';
  let sortBy = 'lastSeen';
  let lastHeard = '';
  let wsHandler = null;
  let detailMap = null;

  const ROLE_COLORS = { repeater: '#3b82f6', room: '#6b7280', companion: '#22c55e', sensor: '#f59e0b' };
  const TABS = [
    { key: 'all', label: 'All' },
    { key: 'repeater', label: 'Repeaters' },
    { key: 'room', label: 'Rooms' },
    { key: 'companion', label: 'Companions' },
    { key: 'sensor', label: 'Sensors' },
  ];

  let directNode = null; // set when navigating directly to #/nodes/:pubkey

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
      <div class="split-layout">
        <div class="panel-left" id="nodesLeft"></div>
        <div class="panel-right empty" id="nodesRight"><span>Select a node to view details</span></div>
      </div>
    </div>`;

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
        api('/nodes/' + encodeURIComponent(pubkey)),
        api('/nodes/' + encodeURIComponent(pubkey) + '/health').catch(() => null)
      ]);
      const n = nodeData.node;
      const adverts = nodeData.recentAdverts || [];
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
      const statusAge = lastHeard ? (Date.now() - new Date(lastHeard).getTime()) : Infinity;
      // Thresholds based on MeshCore advert intervals:
      // Repeaters/rooms: flood advert every 12-24h, so degraded after 24h, silent after 72h
      // Companions/sensors: user-initiated adverts, shorter thresholds
      const role = (n.role || '').toLowerCase();
      const isInfra = role === 'repeater' || role === 'room';
      const degradedMs = isInfra ? 86400000 : 3600000;   // 24h : 1h
      const silentMs = isInfra ? 259200000 : 86400000;    // 72h : 24h
      const statusLabel = statusAge < degradedMs ? '🟢 Active' : statusAge < silentMs ? '🟡 Degraded' : '🔴 Silent';

      body.innerHTML = `
        ${hasLoc ? `<div id="nodeFullMap" class="node-detail-map" style="border-radius:8px;overflow:hidden;margin-bottom:16px"></div>` : ''}
        <div class="node-full-card">
          <div class="node-detail-name" style="font-size:20px">${escapeHtml(n.name || '(unnamed)')}</div>
          <div style="margin:6px 0 12px"><span class="badge" style="background:${roleColor}20;color:${roleColor}">${n.role}</span> ${statusLabel}</div>
          <div class="node-detail-key mono" style="font-size:11px;word-break:break-all;margin-bottom:12px">${n.public_key}</div>
        </div>

        <div class="node-full-card">
          <h4>Stats</h4>
          <dl class="detail-meta">
            <dt>First Seen</dt><dd>${n.first_seen ? new Date(n.first_seen).toLocaleString() : '—'}</dd>
            <dt>Last Heard</dt><dd>${lastHeard ? timeAgo(lastHeard) : (n.last_seen ? timeAgo(n.last_seen) : '—')}</dd>
            <dt>Total Packets</dt><dd>${stats.totalPackets || n.advert_count || 0}</dd>
            <dt>Packets Today</dt><dd>${stats.packetsToday || 0}</dd>
            <dt>Observers</dt><dd>${observers.length || 0}${observers.length ? ' (' + observers.map(o => escapeHtml(o.observer_name || o.observer_id)).join(', ') + ')' : ''}</dd>
            ${hasLoc ? `<dt>Location</dt><dd>${n.lat.toFixed(5)}, ${n.lon.toFixed(5)}</dd>` : ''}
          </dl>
        </div>

        <div class="node-full-card">
          <h4>Recent Activity (${recent.length})</h4>
          <div class="node-activity-list">
            ${recent.length ? recent.slice(0, 20).map(p => {
              let decoded; try { decoded = JSON.parse(p.decoded_json); } catch {}
              const typeLabel = p.payload_type === 4 ? '📡 Advert' : p.payload_type === 5 ? '💬 Channel' : p.payload_type === 2 ? '✉️ DM' : 'Packet';
              const detail = decoded?.text ? ': ' + escapeHtml(truncate(decoded.text, 50)) : '';
              const snr = p.snr != null ? ` · SNR ${p.snr}dB` : (decoded?.SNR != null ? ` · SNR ${decoded.SNR}dB` : '');
              return `<div class="node-activity-item">
                <span class="node-activity-time">${timeAgo(p.timestamp)}</span>
                <span>${typeLabel}${detail}${snr}</span>
                <a href="#/packets/id/${p.id}" class="ch-analyze-link" style="margin-left:8px;font-size:0.8em">Analyze →</a>
              </div>`;
            }).join('') : '<div class="text-muted">No recent activity</div>'}
          </div>
        </div>

        <div class="node-full-card">
          <h4>Recent Adverts (${adverts.length})</h4>
          <div id="advertTimeline">
            ${adverts.length ? adverts.map(a => {
              const ts = a.timestamp || a.created_at;
              const obs = a.observer_name || a.observer_id || '—';
              const pType = PAYLOAD_TYPES[a.payload_type] || 'Packet';
              return `<div class="advert-entry">
                <span class="advert-dot" style="background:${roleColor}"></span>
                <div class="advert-info" style="color:var(--text)">
                  <strong>${timeAgo(ts)}</strong> · ${pType} via ${escapeHtml(obs)}
                  ${a.snr != null ? ` · SNR ${a.snr}dB` : ''}${a.rssi != null ? ` · RSSI ${a.rssi}dBm` : ''}
                </div>
              </div>`;
            }).join('') : '<div class="text-muted">No recent adverts</div>'}
          </div>
        </div>

        <div style="text-align:center;padding:16px">
          <button class="btn-primary" id="copyUrlBtn">📋 Copy URL</button>
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

    } catch (e) {
      body.innerHTML = `<div class="text-muted" style="padding:40px">Failed to load node: ${e.message}</div>`;
    }
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (detailMap) { detailMap.remove(); detailMap = null; }
    nodes = [];
    selectedKey = null;
  }

  async function loadNodes() {
    try {
      const params = new URLSearchParams({ limit: '200', sortBy });
      if (activeTab !== 'all') params.set('role', activeTab);
      if (search) params.set('search', search);
      if (lastHeard) params.set('lastHeard', lastHeard);
      const data = await api('/nodes?' + params);
      nodes = data.nodes || [];
      counts = data.counts || {};
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
      { k: 'repeaters', l: 'Repeaters', c: '#3b82f6' },
      { k: 'rooms', l: 'Rooms', c: '#6b7280' },
      { k: 'companions', l: 'Companions', c: '#22c55e' },
      { k: 'sensors', l: 'Sensors', c: '#f59e0b' },
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
          <select id="nodeSort" aria-label="Sort nodes">
            <option value="lastSeen" ${sortBy==='lastSeen'?'selected':''}>Sort: Last Seen</option>
            <option value="name" ${sortBy==='name'?'selected':''}>Sort: Name</option>
            <option value="packetCount" ${sortBy==='packetCount'?'selected':''}>Sort: Adverts</option>
          </select>
        </div>
      </div>
      <table class="data-table" id="nodesTable">
        <thead><tr>
          <th class="sortable" data-sort="name" aria-sort="${sortBy === 'name' ? 'ascending' : 'none'}">Name</th>
          <th>Public Key</th>
          <th>Role</th>
          <th class="sortable" data-sort="lastSeen" aria-sort="${sortBy === 'lastSeen' ? 'descending' : 'none'}">Last Seen</th>
          <th class="sortable" data-sort="packetCount" aria-sort="${sortBy === 'packetCount' ? 'descending' : 'none'}">Adverts</th>
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
    document.getElementById('nodeSort').addEventListener('change', e => { sortBy = e.target.value; loadNodes(); });

    // Sortable column headers
    el.querySelectorAll('th.sortable').forEach(th => {
      th.addEventListener('click', () => { sortBy = th.dataset.sort; loadNodes(); });
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

    tbody.innerHTML = nodes.map(n => {
      const roleColor = ROLE_COLORS[n.role] || '#6b7280';
      return `<tr data-key="${n.public_key}" data-action="select" data-value="${n.public_key}" tabindex="0" role="row" class="${selectedKey === n.public_key ? 'selected' : ''}">
        <td>${favStar(n.public_key, 'node-fav')}<strong>${n.name || '(unnamed)'}</strong></td>
        <td class="mono">${truncate(n.public_key, 16)}</td>
        <td><span class="badge" style="background:${roleColor}20;color:${roleColor}">${n.role}</span></td>
        <td>${timeAgo(n.last_seen)}</td>
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
        api('/nodes/' + encodeURIComponent(pubkey)),
        api('/nodes/' + encodeURIComponent(pubkey) + '/health').catch(() => null)
      ]);
      data.healthData = healthData;
      renderDetail(panel, data);
    } catch (e) {
      panel.innerHTML = `<div class="text-muted">Error: ${e.message}</div>`;
    }
  }

  function renderDetail(panel, data) {
    const n = data.node;
    const adverts = data.recentAdverts || [];
    const recent = data.healthData?.recentPackets || [];
    const roleColor = ROLE_COLORS[n.role] || '#6b7280';
    const hasLoc = n.lat != null && n.lon != null;
    const nodeUrl = location.origin + '#/nodes/' + encodeURIComponent(n.public_key);

    panel.innerHTML = `
      <div class="node-detail">
        ${hasLoc ? `<div class="node-map-container node-detail-map" id="nodeMap" style="border-radius:8px;overflow:hidden;"></div>` : ''}
        <div class="node-detail-name">${n.name || '(unnamed)'}</div>
        <div class="node-detail-role"><span class="badge" style="background:${roleColor}20;color:${roleColor}">${n.role}</span></div>

        <div class="node-detail-section">
          <h4>Public Key</h4>
          <div class="node-detail-key mono">${n.public_key}</div>
          <div class="node-qr" id="nodeQrCode"></div>
        </div>

        <div class="node-detail-section">
          <h4>Info</h4>
          <dl class="detail-meta">
            <dt>First Seen</dt><dd>${n.first_seen ? new Date(n.first_seen).toLocaleString() : '—'}</dd>
            <dt>Last Seen</dt><dd>${n.last_seen ? timeAgo(n.last_seen) : '—'}</dd>
            <dt>Adverts</dt><dd>${n.advert_count || 0}</dd>
            ${hasLoc ? `<dt>Location</dt><dd>${n.lat.toFixed(5)}, ${n.lon.toFixed(5)}</dd>` : ''}
          </dl>
        </div>

        <div style="text-align:center;margin-bottom:16px">
          <button class="btn-primary" id="copyUrlBtn">📋 Copy URL</button>
        </div>

        <div class="node-detail-section">
          <h4>Recent Adverts (${adverts.length})</h4>
          <div id="sidebarAdvertList">
            ${adverts.length ? adverts.map((a, i) => {
              const ts = a.timestamp || a.created_at || 'no-ts';
              const obs = a.observer_name || a.observer_id || 'no-obs';
              const pType = PAYLOAD_TYPES[a.payload_type] || 'Pkt';
              const snr = a.snr != null ? ' SNR:' + a.snr + 'dB' : '';
              const rssi = a.rssi != null ? ' RSSI:' + a.rssi : '';
              const age = typeof timeAgo === 'function' ? timeAgo(ts) : ts;
              return '<div style="padding:6px 0;border-bottom:1px solid #ddd;font-size:13px;color:#111">'
                + '<b>' + age + '</b> ' + pType + ' via ' + obs + snr + rssi
                + '</div>';
            }).join('') : '<div style="padding:8px;color:#999">No recent adverts</div>'}
          </div>
        </div>

        <div class="node-detail-section">
          <h4>Recent Activity (${recent.length})</h4>
          <div id="advertTimeline">
            ${recent.length ? recent.map(a => {
              let decoded;
              try { decoded = JSON.parse(a.decoded_json); } catch {}
              const pType = PAYLOAD_TYPES[a.payload_type] || 'Packet';
              const icon = a.payload_type === 4 ? '📡' : a.payload_type === 5 ? '💬' : a.payload_type === 2 ? '✉️' : '📦';
              const detail = decoded?.text ? ': ' + escapeHtml(truncate(decoded.text, 50)) : decoded?.name ? ' — ' + escapeHtml(decoded.name) : '';
              return `<div class="advert-entry">
                <span class="advert-dot" style="background:${roleColor}"></span>
                <div class="advert-info">
                  <strong>${timeAgo(a.timestamp)}</strong> ${icon} ${pType}${detail}
                  ${a.snr != null ? ` · SNR ${a.snr}dB` : ''}${a.rssi != null ? ` · RSSI ${a.rssi}dBm` : ''}
                  <br><a href="#/packets/id/${a.id}" class="ch-analyze-link">Analyze →</a>
                </div>
              </div>`;
            }).join('') : '<div class="text-muted" style="padding:8px">No recent activity</div>'}
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
        qrEl.innerHTML = `<div style="font-size:11px;color:var(--text-muted);margin-bottom:4px">Scan with MeshCore app to add contact</div>` + qr.createSvgTag(3, 0);
        const svg = qrEl.querySelector('svg');
        if (svg) { svg.style.display = 'block'; svg.style.margin = '0 auto'; }
      } catch {}
    }

    // Copy URL
    document.getElementById('copyUrlBtn').addEventListener('click', () => {
      navigator.clipboard.writeText(nodeUrl).then(() => {
        const btn = document.getElementById('copyUrlBtn');
        btn.textContent = '✅ Copied!';
        setTimeout(() => btn.textContent = '📋 Copy URL', 2000);
      }).catch(() => {});
    });
  }

  registerPage('nodes', { init, destroy });
})();
