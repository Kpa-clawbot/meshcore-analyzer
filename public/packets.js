/* === MeshCore Analyzer — packets.js === */
'use strict';

(function () {
  let packets = [];
  let selectedId = null;
  let groupByHash = true;
  let filters = {};
  let wsHandler = null;
  let observers = [];
  let regionMap = {};
  const TYPE_NAMES = { 0:'Request', 1:'Response', 2:'Direct Msg', 3:'ACK', 4:'Advert', 5:'Channel Msg', 7:'Anon Req', 8:'Path', 9:'Trace', 11:'Control' };
  function typeName(t) { return TYPE_NAMES[t] ?? `Type ${t}`; }
  let totalCount = 0;
  let expandedHashes = new Set();
  let hopNameCache = {};
  let filtersBuilt = false;
  const PANEL_WIDTH_KEY = 'meshcore-panel-width';

  function initPanelResize() {
    const handle = document.getElementById('pktResizeHandle');
    const panel = document.getElementById('pktRight');
    if (!handle || !panel) return;
    // Restore saved width
    const saved = localStorage.getItem(PANEL_WIDTH_KEY);
    if (saved) panel.style.width = saved + 'px';

    let startX, startW;
    handle.addEventListener('mousedown', (e) => {
      e.preventDefault();
      startX = e.clientX;
      startW = panel.offsetWidth;
      handle.classList.add('dragging');
      document.body.style.cursor = 'col-resize';
      document.body.style.userSelect = 'none';

      function onMove(e2) {
        const w = Math.max(280, Math.min(window.innerWidth * 0.7, startW - (e2.clientX - startX)));
        panel.style.width = w + 'px';
        panel.style.minWidth = w + 'px';
      }
      function onUp() {
        handle.classList.remove('dragging');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        localStorage.setItem(PANEL_WIDTH_KEY, panel.offsetWidth);
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', onUp);
      }
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });
  }

  // Resolve hop hex prefixes to node names (cached)
  async function resolveHops(hops) {
    const unknown = hops.filter(h => !(h in hopNameCache));
    if (unknown.length) {
      try {
        const data = await api('/resolve-hops?hops=' + unknown.join(','));
        Object.assign(hopNameCache, data.resolved || {});
        // Cache misses as null so we don't re-query
        unknown.forEach(h => { if (!(h in hopNameCache)) hopNameCache[h] = null; });
      } catch {}
    }
  }

  function renderHop(h) {
    const entry = hopNameCache[h];
    const name = entry ? (typeof entry === 'string' ? entry : entry.name) : null;
    const pubkey = entry?.pubkey || h;
    const ambiguous = entry?.ambiguous || false;
    const display = name ? escapeHtml(name) : h;
    const title = ambiguous
      ? `${h} — ⚠ ${entry.candidates.length} matches: ${entry.candidates.map(c => c.name).join(', ')}`
      : h;
    return `<a class="hop hop-link ${name ? 'hop-named' : ''} ${ambiguous ? 'hop-ambiguous' : ''}" href="#/nodes/${encodeURIComponent(pubkey)}" title="${title}" onclick="event.stopPropagation()">${display}${ambiguous ? '<span class="hop-warn">⚠</span>' : ''}</a>`;
  }

  function renderPath(hops) {
    if (!hops || !hops.length) return '—';
    return hops.map(renderHop).join('<span class="arrow">→</span>');
  }

  let directPacketId = null;

  async function init(app, routeParam) {
    // Detect route param type: "id/123" for direct packet, short hex for hash, long hex for node
    if (routeParam) {
      if (routeParam.startsWith('id/')) {
        directPacketId = routeParam.slice(3);
      } else if (routeParam.length <= 16) {
        filters.hash = routeParam;
      } else {
        filters.node = routeParam;
      }
    }
    app.innerHTML = `<div class="split-layout">
      <div class="panel-left" id="pktLeft"></div>
      <div class="panel-right empty" id="pktRight">
        <div class="panel-resize-handle" id="pktResizeHandle"></div>
        <span>Select a packet to view details</span>
      </div>
    </div>`;
    initPanelResize();
    await loadObservers();
    loadPackets();

    // Event delegation for data-action buttons
    app.addEventListener('click', function (e) {
      var btn = e.target.closest('[data-action]');
      if (!btn) return;
      if (btn.dataset.action === 'pkt-refresh') loadPackets();
      else if (btn.dataset.action === 'pkt-byop') showBYOP();
    });

    // If linked directly to a packet by ID, load its detail and filter list
    if (directPacketId) {
      const pktId = Number(directPacketId);
      directPacketId = null;
      try {
        const data = await api(`/packets/${pktId}`);
        if (data.packet?.hash) {
          filters.hash = data.packet.hash;
          const hashInput = document.getElementById('fHash');
          if (hashInput) hashInput.value = filters.hash;
          await loadPackets();
        }
        // Show detail in sidebar
        const panel = document.getElementById('pktRight');
        if (panel) {
          panel.classList.remove('empty');
          panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div>';
          const content = document.createElement('div');
          panel.appendChild(content);
          const pkt = data.packet;
          try {
            const hops = JSON.parse(pkt.path_json || '[]');
            const newHops = hops.filter(h => !(h in hopNameCache));
            if (newHops.length) await resolveHops(newHops);
          } catch {}
          renderDetail(content, data);
          initPanelResize();
        }
      } catch {}
    }
    wsHandler = debouncedOnWS(function (msgs) {
      if (msgs.some(function (m) { return m.type === 'packet'; })) {
        loadPackets();
      }
    });
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    packets = [];
    selectedId = null;
    filtersBuilt = false;
    delete filters.node;
  }

  async function loadObservers() {
    try {
      const data = await api('/observers');
      observers = data.observers || [];
    } catch {}
  }

  async function loadPackets() {
    try {
      const params = new URLSearchParams();
      params.set('limit', '100');
      if (filters.type !== undefined && filters.type !== '') params.set('type', filters.type);
      if (filters.region) params.set('region', filters.region);
      if (filters.observer) params.set('observer', filters.observer);
      if (filters.hash) params.set('hash', filters.hash);
      if (filters.node) params.set('node', filters.node);
      if (groupByHash) params.set('groupByHash', 'true');

      const data = await api('/packets?' + params.toString());
      packets = data.packets || [];
      totalCount = data.total || packets.length;

      // Pre-resolve all path hops to node names
      const allHops = new Set();
      for (const p of packets) {
        try { const path = JSON.parse(p.path_json || '[]'); path.forEach(h => allHops.add(h)); } catch {}
      }
      if (allHops.size) await resolveHops([...allHops]);

      // Restore expanded group children
      if (groupByHash && expandedHashes.size > 0) {
        for (const hash of expandedHashes) {
          const group = packets.find(p => p.hash === hash);
          if (group) {
            try {
              const childData = await api(`/packets?hash=${hash}&limit=20`);
              group._children = childData.packets || [];
            } catch {}
          } else {
            // Group no longer in results — remove from expanded
            expandedHashes.delete(hash);
          }
        }
      }

      renderLeft();
    } catch (e) {
      console.error('Failed to load packets:', e);
    }
  }

  function renderLeft() {
    const el = document.getElementById('pktLeft');
    if (!el) return;

    // Only build the filter bar + table skeleton once; subsequent calls just update rows
    if (filtersBuilt) {
      renderTableRows();
      return;
    }
    filtersBuilt = true;

    el.innerHTML = `
      <div class="page-header">
        <h2>Latest Packets <span class="count">(${totalCount})</span></h2>
        <div>
          <button class="btn-icon" data-action="pkt-refresh" title="Refresh">🔄</button>
          <button class="btn-icon" data-action="pkt-byop" title="Bring Your Own Packet">📦 BYOP</button>
        </div>
      </div>
      <div class="filter-bar" id="pktFilters">
        <input type="text" placeholder="Packet hash…" id="fHash">
        <div class="node-filter-wrap" style="position:relative">
          <input type="text" placeholder="Node name…" id="fNode" autocomplete="off">
          <div class="node-filter-dropdown hidden" id="fNodeDropdown"></div>
        </div>
        <select id="fObserver"><option value="">All Observers</option></select>
        <select id="fRegion"><option value="">All Regions</option></select>
        <select id="fType"><option value="">All Types</option></select>
        <button class="btn ${groupByHash ? 'active' : ''}" id="fGroup">Group by Hash</button>
      </div>
      <table class="data-table" id="pktTable">
        <thead><tr>
          <th></th><th>Region</th><th>Time</th><th>Hash</th><th>Size</th>
          <th>Type</th><th>Observer</th><th>Path</th><th>Rpt</th><th>Details</th>
        </tr></thead>
        <tbody id="pktBody"></tbody>
      </table>
    `;

    // Populate filter dropdowns
    const regionSel = document.getElementById('fRegion');
    for (const [code, name] of Object.entries(regionMap || {})) {
      regionSel.innerHTML += `<option value="${code}" ${filters.region === code ? 'selected' : ''}>${code}</option>`;
    }

    const obsSel = document.getElementById('fObserver');
    for (const o of observers) {
      obsSel.innerHTML += `<option value="${o.id}" ${filters.observer === o.id ? 'selected' : ''}>${o.id}</option>`;
    }

    const typeSel = document.getElementById('fType');
    for (const [k, v] of Object.entries({0:'Request',1:'Response',2:'Direct Msg',3:'ACK',4:'Advert',5:'Channel Msg',7:'Anon Req',8:'Path',9:'Trace'})) {
      typeSel.innerHTML += `<option value="${k}" ${String(filters.type) === k ? 'selected' : ''}>${v}</option>`;
    }

    // Filter event listeners
    document.getElementById('fHash').value = filters.hash || '';
    document.getElementById('fHash').addEventListener('input', debounce((e) => { filters.hash = e.target.value || undefined; loadPackets(); }, 300));
    document.getElementById('fObserver').addEventListener('change', (e) => { filters.observer = e.target.value || undefined; loadPackets(); });
    document.getElementById('fRegion').addEventListener('change', (e) => { filters.region = e.target.value || undefined; loadPackets(); });
    document.getElementById('fType').addEventListener('change', (e) => { filters.type = e.target.value !== '' ? e.target.value : undefined; loadPackets(); });
    document.getElementById('fGroup').addEventListener('click', () => { groupByHash = !groupByHash; loadPackets(); });

    // Node name filter with autocomplete
    const fNode = document.getElementById('fNode');
    const fNodeDrop = document.getElementById('fNodeDropdown');
    fNode.value = filters.nodeName || '';
    fNode.addEventListener('input', debounce(async (e) => {
      const q = e.target.value.trim();
      if (!q) {
        fNodeDrop.classList.add('hidden');
        if (filters.node) { filters.node = undefined; filters.nodeName = undefined; loadPackets(); }
        return;
      }
      try {
        const resp = await fetch('/api/nodes/search?q=' + encodeURIComponent(q));
        const data = await resp.json();
        const nodes = data.nodes || [];
        if (nodes.length === 0) { fNodeDrop.classList.add('hidden'); return; }
        fNodeDrop.innerHTML = nodes.map(n =>
          `<div class="node-filter-option" data-key="${n.public_key}" data-name="${escapeHtml(n.name || n.public_key.slice(0,8))}">${escapeHtml(n.name || n.public_key.slice(0,8))} <span style="color:var(--muted);font-size:0.8em">${n.public_key.slice(0,8)}</span></div>`
        ).join('');
        fNodeDrop.classList.remove('hidden');
        fNodeDrop.querySelectorAll('.node-filter-option').forEach(opt => {
          opt.addEventListener('click', () => {
            filters.node = opt.dataset.key;
            filters.nodeName = opt.dataset.name;
            fNode.value = opt.dataset.name;
            fNodeDrop.classList.add('hidden');
            loadPackets();
          });
        });
      } catch {}
    }, 250));
    fNode.addEventListener('blur', () => { setTimeout(() => fNodeDrop.classList.add('hidden'), 200); });

    // Delegated click/keyboard handler for table rows
    const pktBody = document.getElementById('pktBody');
    if (pktBody) {
      const handler = (e) => {
        const row = e.target.closest('tr[data-action]');
        if (!row) return;
        if (e.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return;
        if (e.type === 'keydown') e.preventDefault();
        const action = row.dataset.action;
        const value = row.dataset.value;
        if (action === 'select') selectPacket(Number(value));
        else if (action === 'select-hash') pktSelectHash(value);
        else if (action === 'toggle-select') { pktToggleGroup(value); pktSelectHash(value); }
      };
      pktBody.addEventListener('click', handler);
      pktBody.addEventListener('keydown', handler);
    }

    renderTableRows();
    makeColumnsResizable('#pktTable', 'meshcore-pkt-col-widths');
  }

  function renderTableRows() {
    const tbody = document.getElementById('pktBody');
    if (!tbody) return;

    // Update dynamic parts of the header
    const countEl = document.querySelector('#pktLeft .count');
    if (countEl) countEl.textContent = `(${totalCount})`;
    const groupBtn = document.getElementById('fGroup');
    if (groupBtn) groupBtn.classList.toggle('active', groupByHash);

    if (groupByHash) {
      let html = '';
      for (const p of packets) {
        const isExpanded = expandedHashes.has(p.hash);
        const groupRegion = p.observer_id ? (observers.find(o => o.id === p.observer_id)?.iata || '') : '';
        let groupPath = [];
        try { groupPath = JSON.parse(p.path_json || '[]'); } catch {}
        const groupPathStr = renderPath(groupPath);
        const groupTypeName = payloadTypeName(p.payload_type);
        const groupTypeClass = payloadTypeColor(p.payload_type);
        const groupSize = p.raw_hex ? Math.floor(p.raw_hex.length / 2) : 0;
        const isSingle = p.count <= 1;
        html += `<tr class="${isSingle ? '' : 'group-header'} ${isExpanded ? 'expanded' : ''}" data-hash="${p.hash}" data-action="${isSingle ? 'select-hash' : 'toggle-select'}" data-value="${p.hash}" tabindex="0" role="row">
          <td style="width:28px;text-align:center;cursor:pointer">${isSingle ? '' : (isExpanded ? '▼' : '▶')}</td>
          <td>${groupRegion ? `<span class="badge-region">${groupRegion}</span>` : '—'}</td>
          <td>${timeAgo(p.latest)}</td>
          <td class="mono">${truncate(p.hash || '—', 8)}</td>
          <td>${groupSize ? groupSize + 'B' : '—'}</td>
          <td>${p.payload_type != null ? `<span class="badge badge-${groupTypeClass}">${groupTypeName}</span>` : '—'}</td>
          <td>${isSingle ? truncate(p.observer_name || p.observer_id || '—', 16) : truncate(p.observer_name || p.observer_id || '—', 10) + (p.observer_count > 1 ? ' +' + (p.observer_count - 1) : '')}</td>
          <td><span class="path-hops">${groupPathStr}</span></td>
          <td>${isSingle ? '' : p.count}</td>
          <td>${getDetailPreview((() => { try { return JSON.parse(p.decoded_json || '{}'); } catch { return {}; } })())}</td>
        </tr>`;
        // Child rows (loaded async when expanded)
        if (isExpanded && p._children) {
          for (const c of p._children) {
            const typeName = payloadTypeName(c.payload_type);
            const typeClass = payloadTypeColor(c.payload_type);
            const size = c.raw_hex ? Math.floor(c.raw_hex.length / 2) : 0;
            const childRegion = c.observer_id ? (observers.find(o => o.id === c.observer_id)?.iata || '') : '';
            let childPath = [];
            try { childPath = JSON.parse(c.path_json || '[]'); } catch {}
            const childPathStr = renderPath(childPath);
            html += `<tr class="group-child" data-id="${c.id}" data-action="select" data-value="${c.id}" tabindex="0" role="row">
              <td></td><td>${childRegion ? `<span class="badge-region">${childRegion}</span>` : '—'}</td>
              <td>${timeAgo(c.timestamp)}</td>
              <td class="mono">${truncate(c.hash || '', 8)}</td>
              <td>${size}B</td>
              <td><span class="badge badge-${typeClass}">${typeName}</span></td>
              <td>${truncate(c.observer_name || c.observer_id || '—', 16)}</td>
              <td><span class="path-hops">${childPathStr}</span></td>
              <td></td>
              <td>${getDetailPreview((() => { try { return JSON.parse(c.decoded_json); } catch { return {}; } })())}</td>
            </tr>`;
          }
        }
      }
      tbody.innerHTML = html;
      return;
    }

    tbody.innerHTML = packets.map(p => {
      let decoded, pathHops = [];
      try { decoded = JSON.parse(p.decoded_json); } catch {}
      try { pathHops = JSON.parse(p.path_json || '[]'); } catch {}

      const region = p.observer_id ? (observers.find(o => o.id === p.observer_id)?.iata || '') : '';
      const typeName = payloadTypeName(p.payload_type);
      const typeClass = payloadTypeColor(p.payload_type);
      const size = p.raw_hex ? Math.floor(p.raw_hex.length / 2) : 0;
      const pathStr = renderPath(pathHops);
      const detail = getDetailPreview(decoded);

      return `<tr data-id="${p.id}" data-action="select" data-value="${p.id}" tabindex="0" role="row" class="${selectedId === p.id ? 'selected' : ''}">
        <td></td><td>${region ? `<span class="badge-region">${region}</span>` : '—'}</td>
        <td>${timeAgo(p.timestamp)}</td>
        <td class="mono">${truncate(p.hash || String(p.id), 8)}</td>
        <td>${size}B</td>
        <td><span class="badge badge-${typeClass}">${typeName}</span></td>
        <td>${truncate(p.observer_name || p.observer_id || '—', 16)}</td>
        <td><span class="path-hops">${pathStr}</span></td>
        <td></td>
        <td>${detail}</td>
      </tr>`;
    }).join('');
  }

  function getDetailPreview(decoded) {
    if (!decoded) return '';
    // Channel messages (GRP_TXT) — show the message text
    if (decoded.type === 'CHAN' && decoded.text) {
      const t = decoded.text.length > 80 ? decoded.text.slice(0, 80) + '…' : decoded.text;
      return `💬 ${escapeHtml(t)}`;
    }
    // Advertisements — show node name and role
    if (decoded.type === 'ADVERT' && decoded.name) {
      const role = decoded.flags?.repeater ? '📡' : decoded.flags?.room ? '🏠' : decoded.flags?.sensor ? '🌡' : '📻';
      return `${role} ${escapeHtml(decoded.name)}`;
    }
    // Direct messages
    if (decoded.type === 'TXT_MSG') return `✉️ ${decoded.srcHash?.slice(0,8) || '?'} → ${decoded.destHash?.slice(0,8) || '?'}`;
    // Path updates
    if (decoded.type === 'PATH') return `🔀 ${decoded.srcHash?.slice(0,8) || '?'} → ${decoded.destHash?.slice(0,8) || '?'}`;
    // Requests/responses (encrypted)
    if (decoded.type === 'REQ' || decoded.type === 'RESPONSE') return `🔒 ${decoded.srcHash?.slice(0,8) || '?'} → ${decoded.destHash?.slice(0,8) || '?'}`;
    // Anonymous requests
    if (decoded.type === 'ANON_REQ') return `🔒 anon → ${decoded.destHash?.slice(0,8) || '?'}`;
    // Companion bridge text
    if (decoded.text) return escapeHtml(decoded.text.length > 80 ? decoded.text.slice(0, 80) + '…' : decoded.text);
    // Bare adverts with just pubkey
    if (decoded.public_key) return `📡 ${decoded.public_key.slice(0, 16)}…`;
    return '';
  }

  async function selectPacket(id) {
    selectedId = id;
    renderTableRows();
    const panel = document.getElementById('pktRight');
    panel.classList.remove('empty');
    panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div><div class="text-center text-muted" style="padding:40px">Loading…</div>';
    initPanelResize();

    try {
      const data = await api(`/packets/${id}`);
      // Resolve path hops for detail view
      const pkt = data.packet;
      try {
        const hops = JSON.parse(pkt.path_json || '[]');
        const newHops = hops.filter(h => !(h in hopNameCache));
        if (newHops.length) await resolveHops(newHops);
      } catch {}
      panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div>';
      const content = document.createElement('div');
      panel.appendChild(content);
      renderDetail(content, data);
      initPanelResize();
    } catch (e) {
      panel.innerHTML = `<div class="text-muted">Error: ${e.message}</div>`;
    }
  }

  function renderDetail(panel, data) {
    const pkt = data.packet;
    const breakdown = data.breakdown || {};
    const ranges = breakdown.ranges || [];
    let decoded;
    try { decoded = JSON.parse(pkt.decoded_json); } catch { decoded = {}; }
    let pathHops;
    try { pathHops = JSON.parse(pkt.path_json || '[]'); } catch { pathHops = []; }

    const size = pkt.raw_hex ? Math.floor(pkt.raw_hex.length / 2) : 0;
    const typeName = payloadTypeName(pkt.payload_type);

    const snr = pkt.snr ?? decoded.SNR ?? decoded.snr ?? null;
    const rssi = pkt.rssi ?? decoded.RSSI ?? decoded.rssi ?? null;
    const hasRawHex = !!pkt.raw_hex;

    // Build message preview
    let messageHtml = '';
    if (decoded.text) {
      const chLabel = decoded.channel || (decoded.channel_idx != null ? `Ch ${decoded.channel_idx}` : null) || (decoded.channelHash != null ? `Ch 0x${decoded.channelHash.toString(16)}` : '');
      const hopLabel = decoded.path_len != null ? `${decoded.path_len} hops` : '';
      const snrLabel = snr != null ? `SNR ${snr} dB` : '';
      const meta = [chLabel, hopLabel, snrLabel].filter(Boolean).join(' · ');
      messageHtml = `<div class="detail-message" style="padding:12px;margin:8px 0;background:var(--card-bg);border-radius:8px;border-left:3px solid var(--primary)">
        <div style="font-size:1.1em">${escapeHtml(decoded.text)}</div>
        ${meta ? `<div style="font-size:0.85em;color:var(--muted);margin-top:4px">${meta}</div>` : ''}
      </div>`;
    }

    panel.innerHTML = `
      <div class="detail-title">${hasRawHex ? `Packet Byte Breakdown (${size} bytes)` : typeName + ' Packet'}</div>
      <div class="detail-hash">${pkt.hash || 'Packet #' + pkt.id}</div>
      ${messageHtml}
      <dl class="detail-meta">
        <dt>Observer</dt><dd>${pkt.observer_name || pkt.observer_id || '—'}</dd>
        <dt>SNR / RSSI</dt><dd>${snr != null ? snr + ' dB' : '—'} / ${rssi != null ? rssi + ' dBm' : '—'}</dd>
        <dt>Route Type</dt><dd>${routeTypeName(pkt.route_type)}</dd>
        <dt>Payload Type</dt><dd><span class="badge badge-${payloadTypeColor(pkt.payload_type)}">${typeName}</span></dd>
        <dt>Timestamp</dt><dd>${pkt.timestamp}</dd>
        <dt>Path</dt><dd>${pathHops.length ? renderPath(pathHops) : '—'}</dd>
      </dl>
      <div class="detail-actions">
        ${pathHops.length ? `<button class="detail-map-link" id="viewRouteBtn">🗺️ View route on map</button>` : ''}
        <button class="replay-live-btn" title="Replay this packet on the live map">▶ Replay</button>
      </div>

      ${hasRawHex ? `<div class="hex-legend">${buildHexLegend(ranges)}</div>
      <div class="hex-dump">${createColoredHexDump(pkt.raw_hex, ranges)}</div>` : ''}

      ${hasRawHex ? buildFieldTable(pkt, decoded, pathHops, ranges) : buildDecodedTable(decoded)}
    `;

    // Wire up replay button
    const replayBtn = panel.querySelector('.replay-live-btn');
    if (replayBtn) {
      replayBtn.addEventListener('click', () => {
        const livePkt = {
          id: pkt.id, hash: pkt.hash,
          _ts: new Date(pkt.timestamp).getTime(),
          decoded: { header: { payloadTypeName: typeName }, payload: decoded, path: { hops: pathHops } },
          snr: pkt.snr, rssi: pkt.rssi, observer: pkt.observer_name
        };
        sessionStorage.setItem('replay-packet', JSON.stringify(livePkt));
        window.location.hash = '#/live';
      });
    }

    // Wire up view route on map button
    const routeBtn = document.getElementById('viewRouteBtn');
    if (routeBtn && pathHops.length) {
      routeBtn.addEventListener('click', async () => {
        try {
          const obsId = pkt.observer_name || pkt.observer_id || '';
          const observerParam = obsId ? '&observer=' + encodeURIComponent(obsId) : '';
          const resp = await fetch('/api/resolve-hops?hops=' + encodeURIComponent(pathHops.join(',')) + observerParam);
          const data = await resp.json();
          // Pass full pubkeys (server-disambiguated) to map, falling back to short prefix
          const resolvedKeys = pathHops.map(h => {
            const r = data.resolved?.[h];
            return r?.pubkey || h;
          });
          sessionStorage.setItem('map-route-hops', JSON.stringify(resolvedKeys));
          window.location.hash = '#/map?route=1';
        } catch {
          window.location.hash = '#/map';
        }
      });
    }
  }

  function escapeHtml(s) {
    return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
  }

  function buildDecodedTable(decoded) {
    let rows = '';
    for (const [k, v] of Object.entries(decoded)) {
      if (v === null || v === undefined) continue;
      rows += `<tr><td style="font-weight:600;padding:4px 8px">${escapeHtml(k)}</td><td style="padding:4px 8px">${escapeHtml(String(v))}</td></tr>`;
    }
    return rows ? `<table class="detail-decoded" style="width:100%;border-collapse:collapse;margin-top:8px">${rows}</table>` : '';
  }

  function buildFieldTable(pkt, decoded, pathHops, ranges) {
    const buf = pkt.raw_hex || '';
    const size = Math.floor(buf.length / 2);
    let rows = '';

    // Header section
    rows += sectionRow('Header');
    rows += fieldRow(0, 'Header Byte', '0x' + (buf.slice(0, 2) || '??'), `Route: ${routeTypeName(pkt.route_type)}, Payload: ${payloadTypeName(pkt.payload_type)}`);
    rows += fieldRow(1, 'Path Length', '0x' + (buf.slice(2, 4) || '??'), `hash_size=${decoded ? '' : '?'}, hash_count=${pathHops.length}`);

    // Transport codes
    let off = 2;
    if (pkt.route_type === 0 || pkt.route_type === 3) {
      rows += sectionRow('Transport Codes');
      rows += fieldRow(off, 'Next Hop', buf.slice(off * 2, (off + 2) * 2), '');
      rows += fieldRow(off + 2, 'Last Hop', buf.slice((off + 2) * 2, (off + 4) * 2), '');
      off += 4;
    }

    // Path
    if (pathHops.length > 0) {
      rows += sectionRow('Path (' + pathHops.length + ' hops)');
      const pathByte = parseInt(buf.slice(2, 4), 16);
      const hashSize = (pathByte >> 6) + 1;
      for (let i = 0; i < pathHops.length; i++) {
        const hopEntry = hopNameCache[pathHops[i]];
        const hopName = hopEntry ? (typeof hopEntry === 'string' ? hopEntry : hopEntry.name) : null;
        const hopPubkey = hopEntry?.pubkey || pathHops[i];
        const nameHtml = hopName
          ? `<a href="#/nodes/${encodeURIComponent(hopPubkey)}" class="hop-link hop-named" onclick="event.stopPropagation()">${escapeHtml(hopName)}</a>${hopEntry?.ambiguous ? ' ⚠' : ''}`
          : '';
        const label = hopName ? `Hop ${i} — ${nameHtml}` : `Hop ${i}`;
        rows += fieldRow(off + i * hashSize, label, pathHops[i], '');
      }
      off += hashSize * pathHops.length;
    }

    // Payload
    rows += sectionRow('Payload — ' + payloadTypeName(pkt.payload_type));

    if (decoded.type === 'ADVERT') {
      rows += fieldRow(off, 'Public Key (32B)', truncate(decoded.pubKey || '', 24), '');
      rows += fieldRow(off + 32, 'Timestamp (4B)', decoded.timestampISO || '', 'Unix: ' + (decoded.timestamp || ''));
      rows += fieldRow(off + 36, 'Signature (64B)', truncate(decoded.signature || '', 24), '');
      if (decoded.flags) {
        rows += fieldRow(off + 100, 'App Flags', '0x' + (decoded.flags.raw?.toString(16) || '??'),
          [decoded.flags.chat && 'chat', decoded.flags.repeater && 'repeater', decoded.flags.room && 'room',
           decoded.flags.sensor && 'sensor', decoded.flags.hasLocation && 'location', decoded.flags.hasName && 'name'].filter(Boolean).join(', '));
        let fOff = off + 101;
        if (decoded.flags.hasLocation) {
          rows += fieldRow(fOff, 'Latitude', decoded.lat?.toFixed(6) || '', '');
          rows += fieldRow(fOff + 4, 'Longitude', decoded.lon?.toFixed(6) || '', '');
          fOff += 8;
        }
        if (decoded.flags.hasName) {
          rows += fieldRow(fOff, 'Node Name', escapeHtml(decoded.name || ''), '');
        }
      }
    } else if (decoded.type === 'GRP_TXT') {
      rows += fieldRow(off, 'Channel Hash', decoded.channelHash, '');
      rows += fieldRow(off + 1, 'MAC (2B)', decoded.mac || '', '');
      rows += fieldRow(off + 3, 'Encrypted Data', truncate(decoded.encryptedData || '', 30), '');
    } else if (decoded.type === 'ACK') {
      rows += fieldRow(off, 'Dest Hash (6B)', decoded.destHash || '', '');
      rows += fieldRow(off + 6, 'Src Hash (6B)', decoded.srcHash || '', '');
      rows += fieldRow(off + 12, 'Extra (6B)', decoded.extraHash || '', '');
    } else if (decoded.destHash !== undefined) {
      rows += fieldRow(off, 'Dest Hash (6B)', decoded.destHash || '', '');
      rows += fieldRow(off + 6, 'Src Hash (6B)', decoded.srcHash || '', '');
      rows += fieldRow(off + 12, 'MAC (4B)', decoded.mac || '', '');
      rows += fieldRow(off + 16, 'Encrypted Data', truncate(decoded.encryptedData || '', 30), '');
    } else {
      rows += fieldRow(off, 'Raw', truncate(buf.slice(off * 2), 40), '');
    }

    return `<table class="field-table">
      <thead><tr><th>Offset</th><th>Field</th><th>Value</th><th>Description</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>`;
  }

  function sectionRow(label) {
    return `<tr class="section-row"><td colspan="4">${label}</td></tr>`;
  }
  function fieldRow(offset, name, value, desc) {
    return `<tr><td class="mono">${offset}</td><td>${name}</td><td class="mono">${value}</td><td class="text-muted">${desc || ''}</td></tr>`;
  }

  // BYOP modal — decode only, no DB injection
  function showBYOP() {
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay';
    overlay.innerHTML = '<div class="modal byop-modal">'
      + '<div class="byop-header"><h3>📦 Decode a Packet</h3><button class="btn-icon byop-x" title="Close">✕</button></div>'
      + '<p class="text-muted" style="margin:0 0 12px;font-size:.85rem">Paste raw hex bytes from your radio or MQTT feed:</p>'
      + '<textarea id="byopHex" class="byop-input" placeholder="e.g. 15C31A8D4674FEAE37..." spellcheck="false"></textarea>'
      + '<button class="btn-primary byop-go" id="byopDecode" style="width:100%;margin:8px 0">Decode</button>'
      + '<div id="byopResult"></div>'
      + '</div>';
    document.body.appendChild(overlay);

    const close = () => overlay.remove();
    overlay.querySelector('.byop-x').onclick = close;
    overlay.addEventListener('click', (e) => { if (e.target === overlay) close(); });

    const textarea = overlay.querySelector('#byopHex');
    textarea.focus();
    textarea.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        doDecode();
      }
    });

    overlay.querySelector('#byopDecode').onclick = doDecode;

    async function doDecode() {
      const hex = textarea.value.trim().replace(/[\s\n]/g, '');
      const result = document.getElementById('byopResult');
      if (!hex) { result.innerHTML = '<p class="text-muted">Enter hex data</p>'; return; }
      if (!/^[0-9a-fA-F]+$/.test(hex)) { result.innerHTML = '<p class="byop-err">Invalid hex — only 0-9 and A-F allowed</p>'; return; }
      result.innerHTML = '<p class="text-muted">Decoding...</p>';
      try {
        const res = await fetch('/api/decode', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ hex })
        });
        const data = await res.json();
        if (data.error) throw new Error(data.error);
        result.innerHTML = renderDecodedPacket(data.decoded, hex);
      } catch (e) {
        result.innerHTML = '<p class="byop-err">❌ ' + e.message + '</p>';
      }
    }
  }

  function renderDecodedPacket(d, hex) {
    const h = d.header || {};
    const p = d.payload || {};
    const path = d.path || {};
    const size = hex ? Math.floor(hex.length / 2) : 0;

    let html = '<div class="byop-decoded">';

    // Header section
    html += '<div class="byop-section">'
      + '<div class="byop-section-title">Header</div>'
      + '<div class="byop-kv">'
      + kv('Route Type', routeTypeName(h.routeType))
      + kv('Payload Type', payloadTypeName(h.payloadType))
      + kv('Version', h.payloadVersion)
      + kv('Size', size + ' bytes')
      + '</div></div>';

    // Path section
    if (path.hops && path.hops.length) {
      html += '<div class="byop-section">'
        + '<div class="byop-section-title">Path (' + path.hops.length + ' hops)</div>'
        + '<div class="byop-path">' + path.hops.map(function(hop) { return '<span class="hop">' + hop + '</span>'; }).join('<span class="arrow">→</span>') + '</div>'
        + '</div>';
    }

    // Payload section
    html += '<div class="byop-section">'
      + '<div class="byop-section-title">Payload — ' + payloadTypeName(h.payloadType) + '</div>'
      + '<div class="byop-kv">';
    for (const [k, v] of Object.entries(p)) {
      if (v === null || v === undefined) continue;
      if (typeof v === 'object') {
        html += kv(k, '<pre class="byop-pre">' + JSON.stringify(v, null, 2) + '</pre>');
      } else {
        html += kv(k, String(v));
      }
    }
    html += '</div></div>';

    // Raw hex
    html += '<div class="byop-section">'
      + '<div class="byop-section-title">Raw Hex</div>'
      + '<div class="byop-hex mono">' + hex.toUpperCase().match(/.{1,2}/g).join(' ') + '</div>'
      + '</div>';

    html += '</div>';
    return html;
  }

  function kv(key, val) {
    return '<div class="byop-row"><span class="byop-key">' + key + '</span><span class="byop-val">' + val + '</span></div>';
  }

  // Debounce helper
  function debounce(fn, ms) {
    let t;
    return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
  }

  // Load regions from config
  (async () => {
    try {
      // We'll use a simple approach - hardcode from config
      regionMap = {"SJC":"San Jose, US","SFO":"San Francisco, US","OAK":"Oakland, US","MRY":"Monterey, US","LAR":"Los Angeles, US"};
    } catch {}
  })();

  // Global handlers
  async function pktToggleGroup(hash) {
    if (expandedHashes.has(hash)) {
      expandedHashes.delete(hash);
      renderTableRows();
      return;
    }
    // Load children for this hash
    try {
      const data = await api(`/packets?hash=${hash}&limit=20`);
      const group = packets.find(p => p.hash === hash);
      if (group) group._children = data.packets || [];
      // Resolve any new hops from children
      const childHops = new Set();
      for (const c of (group?._children || [])) {
        try { JSON.parse(c.path_json || '[]').forEach(h => childHops.add(h)); } catch {}
      }
      const newHops = [...childHops].filter(h => !(h in hopNameCache));
      if (newHops.length) await resolveHops(newHops);
      expandedHashes.add(hash);
      renderTableRows();
    } catch {}
  }
  async function pktSelectHash(hash) {
    // When grouped, find first packet with this hash
    try {
      const data = await api(`/packets?hash=${hash}&limit=1`);
      if (data.packets?.[0]) selectPacket(data.packets[0].id);
    } catch {}
  }

  registerPage('packets', { init, destroy });
})();
