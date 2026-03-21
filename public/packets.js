/* === MeshCore Analyzer — packets.js === */
'use strict';

(function () {
  let packets = [];

  // Resolve observer_id to friendly name from loaded observers list
  function obsName(id) {
    if (!id) return '—';
    const o = observers.find(ob => ob.id === id);
    return o?.name || id;
  }
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
    function startResize(clientX) {
      startX = clientX;
      startW = panel.offsetWidth;
      handle.classList.add('dragging');
      document.body.style.cursor = 'col-resize';
      document.body.style.userSelect = 'none';
    }
    function doResize(clientX) {
      const w = Math.max(280, Math.min(window.innerWidth * 0.7, startW - (clientX - startX)));
      panel.style.width = w + 'px';
      panel.style.minWidth = w + 'px';
      const left = document.getElementById('pktLeft');
      if (left) {
        const available = left.parentElement.clientWidth - w;
        left.style.width = available + 'px';
      }
    }
    function endResize() {
      handle.classList.remove('dragging');
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
      localStorage.setItem(PANEL_WIDTH_KEY, panel.offsetWidth);
      const left = document.getElementById('pktLeft');
      if (left) left.style.width = '';
    }

    handle.addEventListener('mousedown', (e) => {
      e.preventDefault();
      startResize(e.clientX);

      function onMove(e2) { doResize(e2.clientX); }
      function onUp() {
        endResize();
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', onUp);
      }
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });

    handle.addEventListener('touchstart', (e) => {
      if (e.touches.length !== 1) return;
      e.preventDefault();
      startResize(e.touches[0].clientX);

      function onTouchMove(e2) {
        if (e2.touches.length !== 1) return;
        e2.preventDefault();
        doResize(e2.touches[0].clientX);
      }
      function onTouchEnd() {
        endResize();
        document.removeEventListener('touchmove', onTouchMove);
        document.removeEventListener('touchend', onTouchEnd);
      }
      document.addEventListener('touchmove', onTouchMove, { passive: false });
      document.addEventListener('touchend', onTouchEnd);
    }, { passive: false });
  }

  // Ensure HopResolver is initialized with the nodes list
  async function ensureHopResolver() {
    if (!HopResolver.ready()) {
      try {
        const data = await api('/nodes?limit=2000', { ttl: 60000 });
        HopResolver.init(data.nodes || []);
      } catch {}
    }
  }

  // Resolve hop hex prefixes to node names (cached, client-side)
  async function resolveHops(hops) {
    const unknown = hops.filter(h => !(h in hopNameCache));
    if (unknown.length) {
      await ensureHopResolver();
      const resolved = HopResolver.resolve(unknown);
      Object.assign(hopNameCache, resolved || {});
      // Cache misses as null so we don't re-query
      unknown.forEach(h => { if (!(h in hopNameCache)) hopNameCache[h] = null; });
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
    return `<a class="hop hop-link ${name ? 'hop-named' : ''} ${ambiguous ? 'hop-ambiguous' : ''}" href="#/nodes/${encodeURIComponent(pubkey)}" title="${title}" data-hop-link="true">${display}${ambiguous ? '<span class="hop-warn">⚠</span>' : ''}</a>`;
  }

  function renderPath(hops) {
    if (!hops || !hops.length) return '—';
    return hops.map(renderHop).join('<span class="arrow">→</span>');
  }

  let directPacketId = null;
  let directPacketHash = null;
  let initGeneration = 0;

  async function init(app, routeParam) {
    const gen = ++initGeneration;
    // Detect route param type: "id/123" for direct packet, short hex for hash, long hex for node
    if (routeParam) {
      if (routeParam.startsWith('id/')) {
        directPacketId = routeParam.slice(3);
      } else if (routeParam.length <= 16) {
        filters.hash = routeParam;
        directPacketHash = routeParam;
      } else {
        filters.node = routeParam;
      }
    }
    app.innerHTML = `<div class="split-layout">
      <div class="panel-left" id="pktLeft"></div>
      <div class="panel-right empty" id="pktRight" aria-live="polite">
        <div class="panel-resize-handle" id="pktResizeHandle"></div>
        <span>Select a packet to view details</span>
      </div>
    </div>`;
    initPanelResize();
    await loadObservers();
    loadPackets();

    // Auto-select packet detail when arriving via hash URL
    if (directPacketHash) {
      const h = directPacketHash;
      directPacketHash = null;
      try {
        const data = await api(`/packets/${h}`);
        if (gen === initGeneration && data?.packet) {
          selectPacket(data.packet.id, h);
        }
      } catch {}
    }

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
        if (gen !== initGeneration) return;
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
      const newPkts = msgs
        .filter(m => m.type === 'packet' && m.data?.packet)
        .map(m => m.data.packet);
      if (!newPkts.length) return;

      // Check if new packets pass current filters
      const filtered = newPkts.filter(p => {
        if (filters.type !== undefined && filters.type !== '' && p.payload_type !== Number(filters.type)) return false;
        if (filters.observer && p.observer_id !== filters.observer) return false;
        if (filters.hash && p.hash !== filters.hash) return false;
        if (RegionFilter.getRegionParam()) {
          const selectedRegions = RegionFilter.getRegionParam().split(',');
          const obs = observers.find(o => o.id === p.observer_id);
          if (!obs || !selectedRegions.includes(obs.iata)) return false;
        }
        if (filters.node && !(p.decoded_json || '').includes(filters.node)) return false;
        return true;
      });
      if (!filtered.length) return;

      // Resolve any new hops, then update and re-render
      const newHops = new Set();
      for (const p of filtered) {
        try { JSON.parse(p.path_json || '[]').forEach(h => { if (!(h in hopNameCache)) newHops.add(h); }); } catch {}
      }
      (newHops.size ? resolveHops([...newHops]) : Promise.resolve()).then(() => {
        if (groupByHash) {
          // Update existing groups or create new ones
          for (const p of filtered) {
            const h = p.hash;
            const existing = packets.find(g => g.hash === h);
            if (existing) {
              existing.count = (existing.count || 1) + 1;
              existing.observation_count = (existing.observation_count || 1) + 1;
              existing.latest = p.timestamp > existing.latest ? p.timestamp : existing.latest;
              // Track unique observers
              if (p.observer_id && p.observer_id !== existing.observer_id) {
                existing.observer_count = (existing.observer_count || 1) + 1;
              }
              // Keep longest path
              if (p.path_json && (!existing.path_json || p.path_json.length > existing.path_json.length)) {
                existing.path_json = p.path_json;
                existing.raw_hex = p.raw_hex;
              }
              // Update decoded_json to latest
              if (p.decoded_json) existing.decoded_json = p.decoded_json;
              // Update expanded children if this group is expanded
              if (expandedHashes.has(h) && existing._children) {
                existing._children.unshift(p);
              }
            } else {
              // New group
              packets.unshift({
                hash: h,
                count: 1,
                observer_count: 1,
                latest: p.timestamp,
                observer_id: p.observer_id,
                observer_name: p.observer_name,
                path_json: p.path_json,
                payload_type: p.payload_type,
                raw_hex: p.raw_hex,
                decoded_json: p.decoded_json,
              });
            }
          }
          // Re-sort by latest DESC, cap size
          packets.sort((a, b) => (b.latest || '').localeCompare(a.latest || ''));
          packets = packets.slice(0, 200);
        } else {
          // Flat mode: prepend
          packets = filtered.concat(packets).slice(0, 200);
        }
        totalCount += filtered.length;
        renderTableRows();
      });
    });
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    packets = [];
    selectedId = null;
    filtersBuilt = false;
    delete filters.node;
    expandedHashes = new Set();
    hopNameCache = {};
    totalCount = 0;
    observers = [];
    directPacketId = null;
    directPacketHash = null;
    groupByHash = true;
    filters = {};
    regionMap = {};
  }

  async function loadObservers() {
    try {
      const data = await api('/observers', { ttl: CLIENT_TTL.observers });
      observers = data.observers || [];
    } catch {}
  }

  async function loadPackets() {
    try {
      const params = new URLSearchParams();
      params.set('limit', '100');
      if (filters.type !== undefined && filters.type !== '') params.set('type', filters.type);
      const regionParam = RegionFilter.getRegionParam();
      if (regionParam) params.set('region', regionParam);
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
      const tbody = document.getElementById('pktBody');
      if (tbody) tbody.innerHTML = '<tr><td colspan="10" class="text-center" style="padding:24px;color:var(--error,#ef4444)"><div role="alert" aria-live="polite">Failed to load packets. Please try again.</div></td></tr>';
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
          <button class="btn-icon" data-action="pkt-byop" title="Bring Your Own Packet" aria-label="Bring Your Own Packet - paste raw packet hex for analysis" aria-haspopup="dialog">📦 BYOP</button>
        </div>
      </div>
      <div class="filter-bar" id="pktFilters">
        <button class="btn filter-toggle-btn" id="filterToggleBtn">Filters ▾</button>
        <input type="text" placeholder="Packet hash…" id="fHash" aria-label="Filter by packet hash">
        <div class="node-filter-wrap" style="position:relative">
          <input type="text" placeholder="Node name…" id="fNode" autocomplete="off" role="combobox" aria-expanded="false" aria-owns="fNodeDropdown" aria-activedescendant="" aria-autocomplete="list">
          <div class="node-filter-dropdown hidden" id="fNodeDropdown" role="listbox"></div>
        </div>
        <select id="fObserver" aria-label="Filter by observer"><option value="">All Observers</option></select>
        <div id="packetsRegionFilter" class="region-filter-container" style="display:inline-block;vertical-align:middle"></div>
        <select id="fType" aria-label="Filter by packet type"><option value="">All Types</option></select>
        <button class="btn ${groupByHash ? 'active' : ''}" id="fGroup">Group by Hash</button>
        <button class="btn" id="fMyNodes" title="Show only packets from claimed/favorited nodes">★ My Nodes</button>
        <div class="col-toggle-wrap">
          <button class="col-toggle-btn" id="colToggleBtn">Columns ▾</button>
          <div class="col-toggle-menu" id="colToggleMenu"></div>
        </div>
      </div>
      <table class="data-table" id="pktTable">
        <thead><tr>
          <th></th><th class="col-region">Region</th><th class="col-time">Time</th><th class="col-hash">Hash</th><th class="col-size">Size</th>
          <th class="col-type">Type</th><th class="col-observer">Observer</th><th class="col-path">Path</th><th class="col-rpt">Rpt</th><th class="col-details">Details</th>
        </tr></thead>
        <tbody id="pktBody"></tbody>
      </table>
    `;

    // Init shared RegionFilter component
    RegionFilter.init(document.getElementById('packetsRegionFilter'), { dropdown: true });
    RegionFilter.onChange(function() { loadPackets(); });

    const obsSel = document.getElementById('fObserver');
    for (const o of observers) {
      obsSel.innerHTML += `<option value="${o.id}" ${filters.observer === o.id ? 'selected' : ''}>${o.name || o.id}</option>`;
    }

    const typeSel = document.getElementById('fType');
    for (const [k, v] of Object.entries({0:'Request',1:'Response',2:'Direct Msg',3:'ACK',4:'Advert',5:'Channel Msg',7:'Anon Req',8:'Path',9:'Trace'})) {
      typeSel.innerHTML += `<option value="${k}" ${String(filters.type) === k ? 'selected' : ''}>${v}</option>`;
    }

    // Filter toggle button for mobile
    document.getElementById('filterToggleBtn').addEventListener('click', function() {
      const bar = document.getElementById('pktFilters');
      bar.classList.toggle('filters-expanded');
      this.textContent = bar.classList.contains('filters-expanded') ? 'Filters ▴' : 'Filters ▾';
    });

    // Filter event listeners
    document.getElementById('fHash').value = filters.hash || '';
    document.getElementById('fHash').addEventListener('input', debounce((e) => { filters.hash = e.target.value || undefined; loadPackets(); }, 300));
    document.getElementById('fObserver').addEventListener('change', (e) => { filters.observer = e.target.value || undefined; loadPackets(); });
    document.getElementById('fType').addEventListener('change', (e) => { filters.type = e.target.value !== '' ? e.target.value : undefined; loadPackets(); });
    document.getElementById('fGroup').addEventListener('click', () => { groupByHash = !groupByHash; loadPackets(); });
    document.getElementById('fMyNodes').addEventListener('click', function () {
      filters.myNodes = !filters.myNodes;
      this.classList.toggle('active', filters.myNodes);
      loadPackets();
    });

    // Column visibility toggle (#71)
    const COL_DEFS = [
      { key: 'region', label: 'Region' },
      { key: 'time', label: 'Time' },
      { key: 'hash', label: 'Hash' },
      { key: 'size', label: 'Size' },
      { key: 'type', label: 'Type' },
      { key: 'observer', label: 'Observer' },
      { key: 'path', label: 'Path' },
      { key: 'rpt', label: 'Rpt' },
      { key: 'details', label: 'Details' },
    ];
    const isMobile = window.innerWidth <= 640;
    const defaultHidden = isMobile ? ['region', 'hash', 'observer', 'path', 'rpt', 'size'] : ['region'];
    let visibleCols;
    try {
      visibleCols = JSON.parse(localStorage.getItem('packets-visible-cols'));
    } catch {}
    if (!visibleCols) visibleCols = COL_DEFS.map(c => c.key).filter(k => !defaultHidden.includes(k));
    const colMenu = document.getElementById('colToggleMenu');
    const pktTable = document.getElementById('pktTable');
    function applyColVisibility() {
      COL_DEFS.forEach(c => {
        pktTable.classList.toggle('hide-col-' + c.key, !visibleCols.includes(c.key));
      });
      localStorage.setItem('packets-visible-cols', JSON.stringify(visibleCols));
    }
    colMenu.innerHTML = COL_DEFS.map(c =>
      `<label><input type="checkbox" data-col="${c.key}" ${visibleCols.includes(c.key) ? 'checked' : ''}> ${c.label}</label>`
    ).join('');
    colMenu.addEventListener('change', (e) => {
      const cb = e.target;
      const col = cb.dataset.col;
      if (!col) return;
      if (cb.checked) { if (!visibleCols.includes(col)) visibleCols.push(col); }
      else { visibleCols = visibleCols.filter(k => k !== col); }
      applyColVisibility();
    });
    document.getElementById('colToggleBtn').addEventListener('click', (e) => {
      e.stopPropagation();
      colMenu.classList.toggle('open');
    });
    document.addEventListener('click', () => colMenu.classList.remove('open'));
    applyColVisibility();

    // Node name filter with autocomplete
    const fNode = document.getElementById('fNode');
    const fNodeDrop = document.getElementById('fNodeDropdown');
    fNode.value = filters.nodeName || '';
    let nodeActiveIdx = -1;
    fNode.addEventListener('input', debounce(async (e) => {
      const q = e.target.value.trim();
      nodeActiveIdx = -1;
      fNode.setAttribute('aria-activedescendant', '');
      if (!q) {
        fNodeDrop.classList.add('hidden');
        fNode.setAttribute('aria-expanded', 'false');
        if (filters.node) { filters.node = undefined; filters.nodeName = undefined; loadPackets(); }
        return;
      }
      try {
        const resp = await fetch('/api/nodes/search?q=' + encodeURIComponent(q));
        const data = await resp.json();
        const nodes = data.nodes || [];
        if (nodes.length === 0) { fNodeDrop.classList.add('hidden'); fNode.setAttribute('aria-expanded', 'false'); return; }
        fNodeDrop.innerHTML = nodes.map((n, i) =>
          `<div class="node-filter-option" id="fNodeOpt-${i}" role="option" data-key="${n.public_key}" data-name="${escapeHtml(n.name || n.public_key.slice(0,8))}">${escapeHtml(n.name || n.public_key.slice(0,8))} <span style="color:var(--muted);font-size:0.8em">${n.public_key.slice(0,8)}</span></div>`
        ).join('');
        fNodeDrop.classList.remove('hidden');
        fNode.setAttribute('aria-expanded', 'true');
        fNodeDrop.querySelectorAll('.node-filter-option').forEach(opt => {
          opt.addEventListener('click', () => {
            selectNodeOption(opt);
          });
        });
      } catch {}
    }, 250));

    function selectNodeOption(opt) {
      filters.node = opt.dataset.key;
      filters.nodeName = opt.dataset.name;
      fNode.value = opt.dataset.name;
      fNodeDrop.classList.add('hidden');
      fNode.setAttribute('aria-expanded', 'false');
      fNode.setAttribute('aria-activedescendant', '');
      nodeActiveIdx = -1;
      loadPackets();
    }

    fNode.addEventListener('keydown', (e) => {
      const options = fNodeDrop.querySelectorAll('.node-filter-option');
      if (!options.length || fNodeDrop.classList.contains('hidden')) return;
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        nodeActiveIdx = Math.min(nodeActiveIdx + 1, options.length - 1);
        updateNodeActive(options);
      } else if (e.key === 'ArrowUp') {
        e.preventDefault();
        nodeActiveIdx = Math.max(nodeActiveIdx - 1, 0);
        updateNodeActive(options);
      } else if (e.key === 'Enter') {
        e.preventDefault();
        if (nodeActiveIdx >= 0 && options[nodeActiveIdx]) selectNodeOption(options[nodeActiveIdx]);
      } else if (e.key === 'Escape') {
        fNodeDrop.classList.add('hidden');
        fNode.setAttribute('aria-expanded', 'false');
        nodeActiveIdx = -1;
      }
    });

    function updateNodeActive(options) {
      options.forEach((o, i) => {
        o.classList.toggle('node-filter-active', i === nodeActiveIdx);
        o.setAttribute('aria-selected', i === nodeActiveIdx ? 'true' : 'false');
      });
      if (nodeActiveIdx >= 0 && options[nodeActiveIdx]) {
        fNode.setAttribute('aria-activedescendant', options[nodeActiveIdx].id);
        options[nodeActiveIdx].scrollIntoView({ block: 'nearest' });
      }
    }

    fNode.addEventListener('blur', () => { setTimeout(() => { fNodeDrop.classList.add('hidden'); fNode.setAttribute('aria-expanded', 'false'); }, 200); });

    // Delegated click/keyboard handler for table rows
    const pktBody = document.getElementById('pktBody');
    if (pktBody) {
      const handler = (e) => {
        // Let hop links navigate naturally without selecting the row
        if (e.target.closest('[data-hop-link]')) return;
        const row = e.target.closest('tr[data-action]');
        if (!row) return;
        if (e.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return;
        if (e.type === 'keydown') e.preventDefault();
        const action = row.dataset.action;
        const value = row.dataset.value;
        if (action === 'select') {
          const hash = row.dataset.hash;
          if (hash) selectPacket(null, hash);
          else selectPacket(Number(value));
        }
        else if (action === 'select-hash') pktSelectHash(value);
        else if (action === 'toggle-select') { pktToggleGroup(value); pktSelectHash(value); }
      };
      pktBody.addEventListener('click', handler);
      pktBody.addEventListener('keydown', handler);
    }

    // Escape to close packet detail panel
    document.addEventListener('keydown', function pktEsc(e) {
      if (e.key === 'Escape') {
        const panel = document.getElementById('pktRight');
        if (panel && !panel.classList.contains('empty')) {
          panel.classList.add('empty');
          panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div><span>Select a packet to view details</span>';
          selectedId = null;
          renderTableRows();
        }
      }
    });

    renderTableRows();
    makeColumnsResizable('#pktTable', 'meshcore-pkt-col-widths');
  }

  async function renderTableRows() {
    const tbody = document.getElementById('pktBody');
    if (!tbody) return;

    // Update dynamic parts of the header
    const countEl = document.querySelector('#pktLeft .count');
    if (countEl) countEl.textContent = `(${totalCount})`;
    const groupBtn = document.getElementById('fGroup');
    if (groupBtn) groupBtn.classList.toggle('active', groupByHash);

    // Filter to claimed/favorited nodes if toggle is on — use server-side multi-node lookup
    let displayPackets = packets;
    if (filters.myNodes) {
      const myNodes = JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]');
      const myKeys = myNodes.map(n => n.pubkey).filter(Boolean);
      const favs = getFavorites();
      const allKeys = [...new Set([...myKeys, ...favs])];
      if (allKeys.length > 0) {
        try {
          const myData = await api('/packets?nodes=' + allKeys.join(',') + '&limit=500');
          displayPackets = myData.packets || [];
        } catch { displayPackets = []; }
      } else {
        displayPackets = [];
      }
    }

    if (!displayPackets.length) {
      tbody.innerHTML = '<tr><td colspan="10" class="text-center text-muted" style="padding:24px">' + (filters.myNodes ? 'No packets from your claimed/favorited nodes' : 'No packets found') + '</td></tr>';
      return;
    }

    if (groupByHash) {
      let html = '';
      for (const p of displayPackets) {
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
          <td class="col-region">${groupRegion ? `<span class="badge-region">${groupRegion}</span>` : '—'}</td>
          <td class="col-time">${timeAgo(p.latest)}</td>
          <td class="mono col-hash">${truncate(p.hash || '—', 8)}</td>
          <td class="col-size">${groupSize ? groupSize + 'B' : '—'}</td>
          <td class="col-type">${p.payload_type != null ? `<span class="badge badge-${groupTypeClass}">${groupTypeName}</span>` : '—'}</td>
          <td class="col-observer">${isSingle ? truncate(obsName(p.observer_id), 16) : truncate(obsName(p.observer_id), 10) + (p.observer_count > 1 ? ' +' + (p.observer_count - 1) : '')}</td>
          <td class="col-path"><span class="path-hops">${groupPathStr}</span></td>
          <td class="col-rpt">${p.observation_count > 1 ? '<span class="badge badge-obs" title="Seen ' + p.observation_count + ' times">👁 ' + p.observation_count + '</span>' : (isSingle ? '' : p.count)}</td>
          <td class="col-details">${getDetailPreview((() => { try { return JSON.parse(p.decoded_json || '{}'); } catch { return {}; } })())}</td>
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
            html += `<tr class="group-child" data-id="${c.id}" data-hash="${c.hash || ''}" data-action="select" data-value="${c.id}" tabindex="0" role="row">
              <td></td><td class="col-region">${childRegion ? `<span class="badge-region">${childRegion}</span>` : '—'}</td>
              <td class="col-time">${timeAgo(c.timestamp)}</td>
              <td class="mono col-hash">${truncate(c.hash || '', 8)}</td>
              <td class="col-size">${size}B</td>
              <td class="col-type"><span class="badge badge-${typeClass}">${typeName}</span></td>
              <td class="col-observer">${truncate(obsName(c.observer_id), 16)}</td>
              <td class="col-path"><span class="path-hops">${childPathStr}</span></td>
              <td class="col-rpt"></td>
              <td class="col-details">${getDetailPreview((() => { try { return JSON.parse(c.decoded_json); } catch { return {}; } })())}</td>
            </tr>`;
          }
        }
      }
      tbody.innerHTML = html;
      return;
    }

    tbody.innerHTML = displayPackets.map(p => {
      let decoded, pathHops = [];
      try { decoded = JSON.parse(p.decoded_json); } catch {}
      try { pathHops = JSON.parse(p.path_json || '[]'); } catch {}

      const region = p.observer_id ? (observers.find(o => o.id === p.observer_id)?.iata || '') : '';
      const typeName = payloadTypeName(p.payload_type);
      const typeClass = payloadTypeColor(p.payload_type);
      const size = p.raw_hex ? Math.floor(p.raw_hex.length / 2) : 0;
      const pathStr = renderPath(pathHops);
      const detail = getDetailPreview(decoded);

      return `<tr data-id="${p.id}" data-hash="${p.hash || ''}" data-action="select" data-value="${p.id}" tabindex="0" role="row" class="${selectedId === p.id ? 'selected' : ''}">
        <td></td><td class="col-region">${region ? `<span class="badge-region">${region}</span>` : '—'}</td>
        <td class="col-time">${timeAgo(p.timestamp)}</td>
        <td class="mono col-hash">${truncate(p.hash || String(p.id), 8)}</td>
        <td class="col-size">${size}B</td>
        <td class="col-type"><span class="badge badge-${typeClass}">${typeName}</span></td>
        <td class="col-observer">${truncate(obsName(p.observer_id), 16)}</td>
        <td class="col-path"><span class="path-hops">${pathStr}</span></td>
        <td class="col-rpt"></td>
        <td class="col-details">${detail}</td>
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
      return `${role} <a href="#/nodes/${encodeURIComponent(decoded.pubKey)}" class="hop-link hop-named" data-hop-link="true">${escapeHtml(decoded.name)}</a>`;
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

  async function selectPacket(id, hash, prefetchedData) {
    selectedId = id;
    if (hash) {
      history.replaceState(null, '', `#/packets/${hash}`);
    } else {
      history.replaceState(null, '', `#/packets/${id}`);
    }
    renderTableRows();
    const isMobileNow = window.innerWidth <= 640;
    let panel;
    if (isMobileNow) {
      // Use mobile bottom sheet
      let sheet = document.getElementById('mobileDetailSheet');
      if (!sheet) {
        sheet = document.createElement('div');
        sheet.id = 'mobileDetailSheet';
        sheet.className = 'mobile-detail-sheet';
        sheet.innerHTML = '<div class="mobile-sheet-handle"></div><button class="mobile-sheet-close" id="mobileSheetClose">✕</button><div class="mobile-sheet-content"></div>';
        document.body.appendChild(sheet);
        sheet.querySelector('#mobileSheetClose').addEventListener('click', () => {
          sheet.classList.remove('open');
        });
        sheet.querySelector('.mobile-sheet-handle').addEventListener('click', () => {
          sheet.classList.remove('open');
        });
      }
      panel = sheet.querySelector('.mobile-sheet-content');
      panel.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading…</div>';
      sheet.classList.add('open');
    } else {
      panel = document.getElementById('pktRight');
      panel.classList.remove('empty');
      panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div><div class="text-center text-muted" style="padding:40px">Loading…</div>';
      initPanelResize();
    }

    try {
      const data = prefetchedData || await api(hash ? `/packets/${hash}` : `/packets/${id}`);
      // Resolve path hops for detail view
      const pkt = data.packet;
      try {
        const hops = JSON.parse(pkt.path_json || '[]');
        const newHops = hops.filter(h => !(h in hopNameCache));
        if (newHops.length) await resolveHops(newHops);
      } catch {}
      panel.innerHTML = isMobileNow ? '' : '<div class="panel-resize-handle" id="pktResizeHandle"></div>';
      const content = document.createElement('div');
      panel.appendChild(content);
      renderDetail(content, data);
      if (!isMobileNow) initPanelResize();
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

    // Parse hash size from path byte
    const rawPathByte = pkt.raw_hex ? parseInt(pkt.raw_hex.slice(2, 4), 16) : NaN;
    const hashSize = isNaN(rawPathByte) ? null : ((rawPathByte >> 6) + 1);

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

    const observations = data.observations || [];
    const obsCount = data.observation_count || observations.length || 1;
    const uniqueObservers = new Set(observations.map(o => o.observer_id)).size;

    // Propagation time: spread between first and last observation
    let propagationHtml = '—';
    if (observations.length >= 2) {
      const times = observations.map(o => new Date(o.timestamp).getTime()).filter(t => !isNaN(t));
      if (times.length >= 2) {
        const first = Math.min(...times);
        const last = Math.max(...times);
        const spread = last - first;
        if (spread < 1000) {
          propagationHtml = `${spread}ms`;
        } else if (spread < 60000) {
          propagationHtml = `${(spread / 1000).toFixed(1)}s`;
        } else {
          propagationHtml = `${(spread / 60000).toFixed(1)}m`;
        }
        propagationHtml += ` <span style="color:var(--text-muted);font-size:0.85em">(${obsCount} obs × ${uniqueObservers} observers)</span>`;
      }
    }

    panel.innerHTML = `
      <div class="detail-title">${hasRawHex ? `Packet Byte Breakdown (${size} bytes)` : typeName + ' Packet'}</div>
      <div class="detail-hash">${pkt.hash || 'Packet #' + pkt.id}</div>
      ${messageHtml}
      <dl class="detail-meta">
        <dt>Observer</dt><dd>${obsName(pkt.observer_id)}</dd>
        <dt>SNR / RSSI</dt><dd>${snr != null ? snr + ' dB' : '—'} / ${rssi != null ? rssi + ' dBm' : '—'}</dd>
        <dt>Route Type</dt><dd>${routeTypeName(pkt.route_type)}</dd>
        <dt>Payload Type</dt><dd><span class="badge badge-${payloadTypeColor(pkt.payload_type)}">${typeName}</span></dd>
        ${hashSize ? `<dt>Hash Size</dt><dd>${hashSize} byte${hashSize !== 1 ? 's' : ''}</dd>` : ''}
        <dt>Timestamp</dt><dd>${pkt.timestamp}</dd>
        <dt>Propagation</dt><dd>${propagationHtml}</dd>
        <dt>Path</dt><dd>${pathHops.length ? renderPath(pathHops) : '—'}</dd>
      </dl>
      <div class="detail-actions">
        <button class="copy-link-btn" data-packet-hash="${pkt.hash || ''}" data-packet-id="${pkt.id}" title="Copy link to this packet">🔗 Copy Link</button>
        ${pathHops.length ? `<button class="detail-map-link" id="viewRouteBtn">🗺️ View route on map</button>` : ''}
        <button class="replay-live-btn" title="Replay this packet on the live map">▶ Replay</button>
      </div>

      ${hasRawHex ? `<div class="hex-legend">${buildHexLegend(ranges)}</div>
      <div class="hex-dump">${createColoredHexDump(pkt.raw_hex, ranges)}</div>` : ''}

      ${hasRawHex ? buildFieldTable(pkt, decoded, pathHops, ranges) : buildDecodedTable(decoded)}
    `;

    // Wire up copy link button
    const copyLinkBtn = panel.querySelector('.copy-link-btn');
    if (copyLinkBtn) {
      copyLinkBtn.addEventListener('click', () => {
        const pktHash = copyLinkBtn.dataset.packetHash;
        const url = pktHash ? `${location.origin}/#/packets/${pktHash}` : `${location.origin}/#/packets/${copyLinkBtn.dataset.packetId}`;
        navigator.clipboard.writeText(url).then(() => {
          copyLinkBtn.textContent = '✅ Copied!';
          setTimeout(() => { copyLinkBtn.textContent = '🔗 Copy Link'; }, 1500);
        }).catch(() => {
          prompt('Copy this link:', url);
        });
      });
    }

    // Wire up replay button
    const replayBtn = panel.querySelector('.replay-live-btn');
    if (replayBtn) {
      replayBtn.addEventListener('click', () => {
        // Build replay packets for ALL observations of this transmission
        const obs = data.observations || [];
        const replayPackets = [];
        if (obs.length > 1) {
          for (const o of obs) {
            let oPath;
            try { oPath = JSON.parse(o.path_json || '[]'); } catch { oPath = pathHops; }
            let oDec;
            try { oDec = JSON.parse(o.decoded_json || '{}'); } catch { oDec = decoded; }
            replayPackets.push({
              id: o.id, hash: pkt.hash,
              _ts: new Date(o.timestamp).getTime(),
              decoded: { header: { payloadTypeName: typeName }, payload: oDec, path: { hops: oPath } },
              snr: o.snr, rssi: o.rssi, observer: obsName(o.observer_id)
            });
          }
        } else {
          replayPackets.push({
            id: pkt.id, hash: pkt.hash,
            _ts: new Date(pkt.timestamp).getTime(),
            decoded: { header: { payloadTypeName: typeName }, payload: decoded, path: { hops: pathHops } },
            snr: pkt.snr, rssi: pkt.rssi, observer: obsName(pkt.observer_id)
          });
        }
        sessionStorage.setItem('replay-packet', JSON.stringify(replayPackets));
        window.location.hash = '#/live';
      });
    }

    // Wire up view route on map button
    const routeBtn = document.getElementById('viewRouteBtn');
    if (routeBtn && pathHops.length) {
      routeBtn.addEventListener('click', async () => {
        try {
          // Anchor disambiguation from sender's location if known (e.g. ADVERT lat/lon)
          const senderLat = decoded.lat || decoded.latitude;
          const senderLon = decoded.lon || decoded.longitude;
          // Resolve observer position for backward-pass anchor
          let obsLat = null, obsLon = null;
          const obsId = obsName(pkt.observer_id);
          if (obsId && HopResolver.ready()) {
            // Try to find observer in nodes list by name — best effort
          }
          await ensureHopResolver();
          const data = { resolved: HopResolver.resolve(pathHops, senderLat || null, senderLon || null, obsLat, obsLon) };
          // Pass full pubkeys (client-disambiguated) to map, falling back to short prefix
          const resolvedKeys = pathHops.map(h => {
            const r = data.resolved?.[h];
            return r?.pubkey || h;
          });
          // Build origin info for the sender node
          const origin = {};
          if (decoded.pubKey) origin.pubkey = decoded.pubKey;
          else if (decoded.srcHash) origin.pubkey = decoded.srcHash;
          if (decoded.adName || decoded.name) origin.name = decoded.adName || decoded.name;
          if (senderLat != null && senderLon != null) { origin.lat = senderLat; origin.lon = senderLon; }
          sessionStorage.setItem('map-route-hops', JSON.stringify({
            origin: origin,
            hops: resolvedKeys
          }));
          window.location.hash = '#/map?route=1';
        } catch {
          window.location.hash = '#/map';
        }
      });
    }
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
    const pathByte0 = parseInt(buf.slice(2, 4), 16);
    const hashSizeVal = isNaN(pathByte0) ? '?' : ((pathByte0 >> 6) + 1);
    const hashCountVal = isNaN(pathByte0) ? '?' : (pathByte0 & 0x3F);
    rows += fieldRow(1, 'Path Length', '0x' + (buf.slice(2, 4) || '??'), `hash_size=${hashSizeVal} byte${hashSizeVal !== 1 ? 's' : ''}, hash_count=${hashCountVal}`);

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
          ? `<a href="#/nodes/${encodeURIComponent(hopPubkey)}" class="hop-link hop-named" data-hop-link="true">${escapeHtml(hopName)}</a>${hopEntry?.ambiguous ? ' ⚠' : ''}`
          : '';
        const label = hopName ? `Hop ${i} — ${nameHtml}` : `Hop ${i}`;
        rows += fieldRow(off + i * hashSize, label, pathHops[i], '');
      }
      off += hashSize * pathHops.length;
    }

    // Payload
    rows += sectionRow('Payload — ' + payloadTypeName(pkt.payload_type));

    if (decoded.type === 'ADVERT') {
      rows += fieldRow(1, 'Advertised Hash Size', hashSizeVal + ' byte' + (hashSizeVal !== 1 ? 's' : ''), 'From path byte 0x' + (buf.slice(2, 4) || '??') + ' — bits 7-6 = ' + (hashSizeVal - 1));
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
          rows += fieldRow(fOff, 'Node Name', decoded.pubKey ? `<a href="#/nodes/${encodeURIComponent(decoded.pubKey)}" class="hop-link hop-named" data-hop-link="true">${escapeHtml(decoded.name || '')}</a>` : escapeHtml(decoded.name || ''), '');
        }
      }
    } else if (decoded.type === 'GRP_TXT') {
      rows += fieldRow(off, 'Channel Hash', decoded.channelHash, '');
      rows += fieldRow(off + 1, 'MAC (2B)', decoded.mac || '', '');
      rows += fieldRow(off + 3, 'Encrypted Data', truncate(decoded.encryptedData || '', 30), '');
    } else if (decoded.type === 'CHAN') {
      rows += fieldRow(off, 'Channel', decoded.channel || `0x${(decoded.channelHash || 0).toString(16)}`, '');
      rows += fieldRow(off + 1, 'Sender', decoded.sender || '—', '');
      if (decoded.sender_timestamp) rows += fieldRow(off + 2, 'Sender Time', decoded.sender_timestamp, '');
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
    const triggerBtn = document.querySelector('[data-action="pkt-byop"]');
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay';
    overlay.innerHTML = '<div class="modal byop-modal" role="dialog" aria-label="Decode a Packet" aria-modal="true">'
      + '<div class="byop-header"><h3>📦 Decode a Packet</h3><button class="btn-icon byop-x" title="Close" aria-label="Close dialog">✕</button></div>'
      + '<p class="text-muted" style="margin:0 0 12px;font-size:.85rem">Paste raw hex bytes from your radio or MQTT feed:</p>'
      + '<textarea id="byopHex" class="byop-input" aria-label="Packet hex data" placeholder="e.g. 15C31A8D4674FEAE37..." spellcheck="false"></textarea>'
      + '<button class="btn-primary byop-go" id="byopDecode" style="width:100%;margin:8px 0">Decode</button>'
      + '<div id="byopResult" role="status" aria-live="polite"></div>'
      + '</div>';
    document.body.appendChild(overlay);

    const modal = overlay.querySelector('.byop-modal');
    const close = () => { overlay.remove(); if (triggerBtn) triggerBtn.focus(); };
    overlay.querySelector('.byop-x').onclick = close;
    overlay.addEventListener('click', (e) => { if (e.target === overlay) close(); });

    // Focus trap
    function getFocusable() {
      return modal.querySelectorAll('textarea, button, input, [tabindex]:not([tabindex="-1"])');
    }
    overlay.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { e.preventDefault(); close(); return; }
      if (e.key === 'Tab') {
        const focusable = getFocusable();
        if (!focusable.length) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (e.shiftKey) {
          if (document.activeElement === first) { e.preventDefault(); last.focus(); }
        } else {
          if (document.activeElement === last) { e.preventDefault(); first.focus(); }
        }
      }
    });

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
      if (!/^[0-9a-fA-F]+$/.test(hex)) { result.innerHTML = '<p class="byop-err" role="alert">Invalid hex — only 0-9 and A-F allowed</p>'; return; }
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
        result.innerHTML = '<p class="byop-err" role="alert">❌ ' + e.message + '</p>';
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

  // Load regions from config API
  (async () => {
    try {
      regionMap = await api('/config/regions', { ttl: 3600 });
    } catch {}
  })();

  // Global handlers
  async function pktToggleGroup(hash) {
    if (expandedHashes.has(hash)) {
      expandedHashes.delete(hash);
      renderTableRows();
      return;
    }
    // Single fetch — gets packet + observations + path + breakdown
    try {
      const data = await api(`/packets/${hash}`);
      const pkt = data.packet;
      if (!pkt) return;
      const group = packets.find(p => p.hash === hash);
      if (group && data.observations) {
        group._children = data.observations.map(o => ({...pkt, ...o, _isObservation: true}));
      }
      // Resolve any new hops from children
      const childHops = new Set();
      for (const c of (group?._children || [])) {
        try { JSON.parse(c.path_json || '[]').forEach(h => childHops.add(h)); } catch {}
      }
      const newHops = [...childHops].filter(h => !(h in hopNameCache));
      if (newHops.length) await resolveHops(newHops);
      expandedHashes.add(hash);
      renderTableRows();
      // Also open detail panel — no extra fetch needed
      selectPacket(pkt.id, hash, data);
    } catch {}
  }
  async function pktSelectHash(hash) {
    // When grouped, select packet — reuse cached detail endpoint
    try {
      const data = await api(`/packets/${hash}`);
      if (data?.packet) selectPacket(data.packet.id, hash, data);
    } catch {}
  }

  registerPage('packets', { init, destroy });

  // Standalone packet detail page: #/packet/123 or #/packet/HASH
  registerPage('packet-detail', {
    init: async (app, routeParam) => {
      const param = routeParam;
      app.innerHTML = `<div style="max-width:800px;margin:0 auto;padding:20px"><div class="text-center text-muted" style="padding:40px">Loading packet…</div></div>`;
      try {
        const data = await api(`/packets/${param}`);
        if (!data?.packet) { app.innerHTML = `<div style="max-width:800px;margin:0 auto;padding:40px;text-align:center"><h2>Packet not found</h2><p>Packet ${param} doesn't exist.</p><a href="#/packets">← Back to packets</a></div>`; return; }
        const hops = [];
        try { const ph = JSON.parse(data.packet.path_json || '[]'); hops.push(...ph); } catch {}
        const newHops = hops.filter(h => !(h in hopNameCache));
        if (newHops.length) await resolveHops(newHops);
        const container = document.createElement('div');
        container.style.cssText = 'max-width:800px;margin:0 auto;padding:20px';
        container.innerHTML = `<div style="margin-bottom:16px"><a href="#/packets" style="color:var(--primary);text-decoration:none">← Back to packets</a></div>`;
        const detail = document.createElement('div');
        container.appendChild(detail);
        renderDetail(detail, data);
        app.innerHTML = '';
        app.appendChild(container);
      } catch (e) {
        app.innerHTML = `<div style="max-width:800px;margin:0 auto;padding:40px;text-align:center"><h2>Error</h2><p>${e.message}</p><a href="#/packets">← Back to packets</a></div>`;
      }
    },
    destroy: () => {}
  });
})();
