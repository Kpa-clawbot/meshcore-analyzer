/* === CoreScope — packets.js === */
'use strict';

(function () {
  let packets = [];
  let hashIndex = new Map(); // hash → packet group for O(1) dedup

  // Resolve observer_id to friendly name from loaded observers list
  function obsName(id) {
    if (!id) return '—';
    const o = observers.find(ob => ob.id === id);
    if (!o) return id;
    return o.iata ? `${o.name} (${o.iata})` : o.name;
  }
  let selectedId = null;
  let groupByHash = true;
  let filters = {};
  { const o = localStorage.getItem('meshcore-observer-filter'); if (o) filters.observer = o;
    const t = localStorage.getItem('meshcore-type-filter'); if (t) filters.type = t; }
  let wsHandler = null;
  let packetsPaused = false;
  let pauseBuffer = [];
  let observers = [];
  let regionMap = {};
  const TYPE_NAMES = { 0:'Request', 1:'Response', 2:'Direct Msg', 3:'ACK', 4:'Advert', 5:'Channel Msg', 7:'Anon Req', 8:'Path', 9:'Trace', 11:'Control' };
  function typeName(t) { return TYPE_NAMES[t] ?? `Type ${t}`; }
  let totalCount = 0;
  let expandedHashes = new Set();
  let hopNameCache = {};
  let showHexHashes = localStorage.getItem('meshcore-hex-hashes') === 'true';
  let filtersBuilt = false;
  const PANEL_WIDTH_KEY = 'meshcore-panel-width';
  const PANEL_CLOSE_HTML = '<button class="panel-close-btn" title="Close detail pane (Esc)">✕</button>';

  function closeDetailPanel() {
    var panel = document.getElementById('pktRight');
    if (panel) {
      panel.classList.add('empty');
      panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div>' + PANEL_CLOSE_HTML + '<span>Select a packet to view details</span>';
      var layout = panel.closest('.split-layout');
      if (layout) layout.classList.add('detail-collapsed');
      selectedId = null;
      renderTableRows();
    }
  }

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

  // Ensure HopResolver is initialized with the nodes list + observer IATA data
  async function ensureHopResolver() {
    if (!HopResolver.ready()) {
      try {
        const [nodeData, obsData, coordData] = await Promise.all([
          api('/nodes?limit=2000', { ttl: 60000 }),
          api('/observers', { ttl: 60000 }),
          api('/iata-coords', { ttl: 300000 }).catch(() => ({ coords: {} })),
        ]);
        HopResolver.init(nodeData.nodes || [], {
          observers: obsData.observers || obsData || [],
          iataCoords: coordData.coords || {},
        });
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

  function renderHop(h, observerId) {
    // Use per-packet cache key if observer context available (ambiguous hops differ by region)
    const cacheKey = observerId ? h + ':' + observerId : h;
    const entry = hopNameCache[cacheKey] || hopNameCache[h];
    return HopDisplay.renderHop(h, entry, { hexMode: showHexHashes });
  }

  function renderPath(hops, observerId) {
    if (!hops || !hops.length) return '—';
    return hops.map(h => renderHop(h, observerId)).join('<span class="arrow">→</span>');
  }

  let directPacketId = null;
  let directPacketHash = null;
  let initGeneration = 0;
  let _docActionHandler = null;
  let _docMenuCloseHandler = null;
  let _docColMenuCloseHandler = null;

  let directObsId = null;

  function removeAllByopOverlays() {
    document.querySelectorAll('.byop-overlay').forEach(function (el) { el.remove(); });
  }

  function bindDocumentHandler(kind, eventName, handler) {
    const prev = kind === 'action'
      ? _docActionHandler
      : kind === 'menu'
        ? _docMenuCloseHandler
        : _docColMenuCloseHandler;
    if (prev) document.removeEventListener(eventName, prev);
    document.addEventListener(eventName, handler);
    if (kind === 'action') _docActionHandler = handler;
    else if (kind === 'menu') _docMenuCloseHandler = handler;
    else _docColMenuCloseHandler = handler;
  }

  function renderTimestampCell(isoString) {
    if (typeof formatTimestampWithTooltip !== 'function' || typeof getTimestampMode !== 'function') {
      return escapeHtml(typeof timeAgo === 'function' ? timeAgo(isoString) : '—');
    }
    const f = formatTimestampWithTooltip(isoString, getTimestampMode());
    const warn = f.isFuture
      ? ' <span class="timestamp-future-icon" title="Timestamp is in the future — node clock may be skewed">⚠️</span>'
      : '';
    return `<span class="timestamp-text" title="${escapeHtml(f.tooltip)}">${escapeHtml(f.text)}</span>${warn}`;
  }

  async function init(app, routeParam) {
    const gen = ++initGeneration;
    // Parse ?obs=OBSERVER_ID from routeParam
    if (routeParam && routeParam.includes('?')) {
      const qIdx = routeParam.indexOf('?');
      const qs = new URLSearchParams(routeParam.substring(qIdx));
      directObsId = qs.get('obs');
      routeParam = routeParam.substring(0, qIdx);
    }
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
    app.innerHTML = `<div class="split-layout detail-collapsed">
      <div class="panel-left" id="pktLeft"></div>
      <div class="panel-right empty" id="pktRight" aria-live="polite">
        <div class="panel-resize-handle" id="pktResizeHandle"></div>
        ${PANEL_CLOSE_HTML}
        <span>Select a packet to view details</span>
      </div>
    </div>`;
    initPanelResize();
    document.getElementById('pktRight').addEventListener('click', function(e) {
      if (e.target.closest('.panel-close-btn')) closeDetailPanel();
    });
    await loadObservers();
    // Restore saved time window before first load
    const fTW = document.getElementById('fTimeWindow');
    const savedTW = localStorage.getItem('meshcore-time-window');
    if (savedTW !== null && fTW) fTW.value = savedTW;
    loadPackets();

    // Auto-select packet detail when arriving via hash URL
    if (directPacketHash) {
      const h = directPacketHash;
      const obsTarget = directObsId;
      directPacketHash = null;
      directObsId = null;
      try {
        const data = await api(`/packets/${h}`);
        if (gen === initGeneration && data?.packet) {
          if (obsTarget && data.observations) {
            // Find the matching observation by its unique id
            const obs = data.observations.find(o => String(o.id) === String(obsTarget));
            if (obs) {
              expandedHashes.add(h);
              const obsPacket = {...data.packet, observer_id: obs.observer_id, observer_name: obs.observer_name, snr: obs.snr, rssi: obs.rssi, path_json: obs.path_json, timestamp: obs.timestamp, first_seen: obs.timestamp};
              selectPacket(obs.id, h, {packet: obsPacket, breakdown: data.breakdown, observations: data.observations}, obs.id);
            } else {
              selectPacket(data.packet.id, h, data);
            }
          } else {
            selectPacket(data.packet.id, h, data);
          }
        }
      } catch {}
    }

    // Event delegation for data-action buttons
    bindDocumentHandler('action', 'click', function (e) {
      var btn = e.target.closest('[data-action]');
      if (!btn) return;
      if (btn.dataset.action === 'pkt-refresh') loadPackets();
      else if (btn.dataset.action === 'pkt-byop') showBYOP();
      else if (btn.dataset.action === 'pkt-pause') {
        packetsPaused = !packetsPaused;
        const pauseBtn = document.getElementById('pktPauseBtn');
        if (pauseBtn) {
          pauseBtn.textContent = packetsPaused ? '▶' : '⏸';
          pauseBtn.title = packetsPaused ? 'Resume live updates' : 'Pause live updates';
          pauseBtn.classList.toggle('active', packetsPaused);
        }
        if (!packetsPaused && pauseBuffer.length) {
          const handler = wsHandler;
          pauseBuffer.forEach(msg => { if (handler) handler(msg); });
          pauseBuffer = [];
        }
      }
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
          panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div>' + PANEL_CLOSE_HTML;
          const content = document.createElement('div');
          panel.appendChild(content);
          const pkt = data.packet;
          try {
            const hops = JSON.parse(pkt.path_json || '[]');
            const newHops = hops.filter(h => !(h in hopNameCache));
            if (newHops.length) await resolveHops(newHops);
          } catch {}
          await renderDetail(content, data);
          initPanelResize();
        }
      } catch {}
    }
    wsHandler = debouncedOnWS(function (msgs) {
      if (packetsPaused) {
        pauseBuffer.push(...msgs);
        const btn = document.getElementById('pktPauseBtn');
        if (btn) btn.textContent = '▶ ' + pauseBuffer.length;
        return;
      }
      const newPkts = msgs
        .filter(m => m.type === 'packet' && m.data?.packet)
        .map(m => m.data.packet);
      if (!newPkts.length) return;

      // Check if new packets pass current filters
      const filtered = newPkts.filter(p => {
        if (filters.type) { const types = filters.type.split(',').map(Number); if (!types.includes(p.payload_type)) return false; }
        if (filters.observer) { const obsSet = new Set(filters.observer.split(',')); if (!obsSet.has(p.observer_id)) return false; }
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
            const existing = hashIndex.get(h);
            if (existing) {
              existing.count = (existing.count || 1) + 1;
              existing.observation_count = (existing.observation_count || 1) + 1;
              existing.latest = p.timestamp > existing.latest ? p.timestamp : existing.latest;
              // Track unique observers
              if (p.observer_id && p.observer_id !== existing.observer_id) {
                existing.observer_count = (existing.observer_count || 1) + 1;
              }
              // Don't update path — header always shows first observer's path
              // Update decoded_json to latest
              if (p.decoded_json) existing.decoded_json = p.decoded_json;
              // Update expanded children if this group is expanded
              if (expandedHashes.has(h) && existing._children) {
                existing._children.unshift(p);
                sortGroupChildren(existing);
              }
            } else {
              // New group
              const newGroup = {
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
              };
              packets.unshift(newGroup);
              if (h) hashIndex.set(h, newGroup);
            }
          }
          // Re-sort by latest DESC
          packets.sort((a, b) => (b.latest || '').localeCompare(a.latest || ''));
        } else {
          // Flat mode: prepend
          packets = filtered.concat(packets);
        }
        totalCount += filtered.length;
        renderTableRows();
      });
    });
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (_docActionHandler) { document.removeEventListener('click', _docActionHandler); _docActionHandler = null; }
    if (_docMenuCloseHandler) { document.removeEventListener('click', _docMenuCloseHandler); _docMenuCloseHandler = null; }
    if (_docColMenuCloseHandler) { document.removeEventListener('click', _docColMenuCloseHandler); _docColMenuCloseHandler = null; }
    removeAllByopOverlays();
    packets = [];
    hashIndex = new Map();    selectedId = null;
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
      const windowMin = Number(document.getElementById('fTimeWindow')?.value || 15);
      if (windowMin > 0 && !filters.hash) {
        const since = new Date(Date.now() - windowMin * 60000).toISOString();
        params.set('since', since);
      }
      params.set('limit', '50000');
      const regionParam = RegionFilter.getRegionParam();
      if (regionParam) params.set('region', regionParam);
      if (filters.hash) params.set('hash', filters.hash);
      if (filters.node) params.set('node', filters.node);
      params.set('groupByHash', 'true'); // always fetch grouped

      const data = await api('/packets?' + params.toString());
      packets = data.packets || [];
      hashIndex = new Map();
      for (const p of packets) { if (p.hash) hashIndex.set(p.hash, p); }
      totalCount = data.total || packets.length;

      // When ungrouped, fetch observations for all multi-obs packets and flatten
      if (!groupByHash) {
        const multiObs = packets.filter(p => (p.observation_count || p.count || 1) > 1);
        await Promise.all(multiObs.map(async (p) => {
          try {
            const d = await api(`/packets/${p.hash}`);
            if (d?.observations) p._children = d.observations.map(o => ({...d.packet, ...o, _isObservation: true}));
          } catch {}
        }));
        // Flatten: replace grouped packets with individual observations
        const flat = [];
        for (const p of packets) {
          if (p._children && p._children.length > 1) {
            for (const c of p._children) flat.push(c);
          } else {
            flat.push(p);
          }
        }
        packets = flat;
        totalCount = flat.length;
      }

      // Pre-resolve all path hops to node names
      const allHops = new Set();
      for (const p of packets) {
        try { const path = JSON.parse(p.path_json || '[]'); path.forEach(h => allHops.add(h)); } catch {}
      }
      if (allHops.size) await resolveHops([...allHops]);

      // Per-observer batch resolve for ambiguous hops (context-aware disambiguation)
      const hopsByObserver = {};
      for (const p of packets) {
        if (!p.observer_id) continue;
        try {
          const path = JSON.parse(p.path_json || '[]');
          const ambiguous = path.filter(h => hopNameCache[h]?.ambiguous);
          if (ambiguous.length) {
            if (!hopsByObserver[p.observer_id]) hopsByObserver[p.observer_id] = new Set();
            ambiguous.forEach(h => hopsByObserver[p.observer_id].add(h));
          }
        } catch {}
      }
      // Ambiguous hops are already resolved by HopResolver client-side
      // No need for per-observer server API calls

      // Restore expanded group children
      if (groupByHash && expandedHashes.size > 0) {
        for (const hash of expandedHashes) {
          const group = packets.find(p => p.hash === hash);
          if (group) {
            try {
              const childData = await api(`/packets?hash=${hash}&limit=20`);
              group._children = childData.packets || [];
              sortGroupChildren(group);
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
          <button class="btn-icon" id="pktPauseBtn" data-action="pkt-pause" title="Pause live updates">⏸</button>
          <button class="btn-icon" data-action="pkt-byop" title="Bring Your Own Packet" aria-label="Bring Your Own Packet - paste raw packet hex for analysis" aria-haspopup="dialog">📦 BYOP</button>
        </div>
      </div>
      <div class="filter-group" style="flex:1;margin-bottom:8px">
        <input type="text" id="packetFilterInput" class="packet-filter-input"
          placeholder='Filter: type == Advert && snr > 5 · payload.name contains "Gilroy"'
          aria-label="Packet filter expression"
          style="width:100%;padding:6px 10px;border:1px solid var(--border);border-radius:6px;font-family:var(--mono);font-size:13px;background:var(--input-bg);color:var(--text)">
        <div id="packetFilterError" style="color:var(--status-red);font-size:11px;margin-top:2px;display:none"></div>
        <div id="packetFilterCount" style="color:var(--text-muted);font-size:11px;margin-top:2px;display:none"></div>
      </div>
      <div class="filter-bar" id="pktFilters">
        <button class="btn filter-toggle-btn" id="filterToggleBtn">Filters ▾</button>
        <div class="filter-group">
          <input type="text" placeholder="Packet hash…" id="fHash" aria-label="Filter by packet hash" title="Filter packets by hex hash prefix">
          <div class="node-filter-wrap" style="position:relative">
            <input type="text" placeholder="Node name…" id="fNode" autocomplete="off" role="combobox" aria-expanded="false" aria-owns="fNodeDropdown" aria-activedescendant="" aria-autocomplete="list" title="Filter packets involving this node (sender or path)">
            <div class="node-filter-dropdown hidden" id="fNodeDropdown" role="listbox"></div>
          </div>
          <div class="multi-select-wrap" id="observerFilterWrap">
            <button class="multi-select-trigger" id="observerTrigger" title="Show only packets seen by selected observer stations">All Observers ▾</button>
            <div class="multi-select-menu" id="observerMenu"></div>
          </div>
          <div id="packetsRegionFilter" class="region-filter-container" style="display:inline-block;vertical-align:middle"></div>
          <div class="multi-select-wrap" id="typeFilterWrap">
            <button class="multi-select-trigger" id="typeTrigger" title="Filter by packet type">All Types ▾</button>
            <div class="multi-select-menu" id="typeMenu"></div>
          </div>
        </div>
        <div class="filter-group">
          <button class="btn ${groupByHash ? 'active' : ''}" id="fGroup" title="Collapse duplicate observations of the same packet into expandable groups">Group by Hash</button>
          <button class="btn" id="fMyNodes" title="Show only packets from your favorited/claimed nodes">★ My Nodes</button>
        </div>
        <div class="filter-group">
          <select id="fTimeWindow" class="filter-select" aria-label="Time window filter">
            <option value="15">Last 15 min</option>
            <option value="30">Last 30 min</option>
            <option value="60">Last 1 hour</option>
            <option value="180">Last 3 hours</option>
            <option value="360">Last 6 hours</option>
            <option value="720">Last 12 hours</option>
            <option value="1440">Last 24 hours</option>
            <option value="0">All time</option>
          </select>
        </div>
        <div class="filter-group">
          <select id="fObsSort" aria-label="Observation sort order" title="Controls how observations are ordered within packet groups and which observation appears in the header row. Observer: Groups by observer station, earliest first. Path: Orders by hop count. Time: Orders by observation timestamp.">
            <option value="observer">Sort: Observer</option>
            <option value="path-asc">Sort: Path ↑ (shortest)</option>
            <option value="path-desc">Sort: Path ↓ (longest)</option>
            <option value="chrono-asc">Sort: Time ↑ (earliest)</option>
            <option value="chrono-desc">Sort: Time ↓ (latest)</option>
          </select>
          <span class="sort-help" id="sortHelpIcon">ⓘ</span>
        </div>
        <div class="filter-group">
          <div class="col-toggle-wrap">
            <button class="col-toggle-btn" id="colToggleBtn" title="Show/hide table columns">Columns ▾</button>
            <div class="col-toggle-menu" id="colToggleMenu"></div>
          </div>
          <button class="btn btn-icon${showHexHashes ? ' active' : ''}" id="hexHashToggle" title="Show raw hex hash prefixes instead of resolved node names in the path column">Hex Paths</button>
        </div>
      </div>
      <table class="data-table" id="pktTable">
        <thead><tr>
          <th scope="col"></th><th scope="col" class="col-region">Region</th><th scope="col" class="col-time">Time</th><th scope="col" class="col-hash">Hash</th><th scope="col" class="col-size">Size</th>
          <th scope="col" class="col-hashsize">HB</th>
          <th scope="col" class="col-type">Type</th><th scope="col" class="col-observer">Observer</th><th scope="col" class="col-path">Path</th><th scope="col" class="col-rpt">Rpt</th><th scope="col" class="col-details">Details</th>
        </tr></thead>
        <tbody id="pktBody"></tbody>
      </table>
    `;

    // Init shared RegionFilter component
    RegionFilter.init(document.getElementById('packetsRegionFilter'), { dropdown: true });
    RegionFilter.onChange(function() { loadPackets(); });

    // --- Packet Filter Language ---
    (function() {
      var pfInput = document.getElementById('packetFilterInput');
      var pfError = document.getElementById('packetFilterError');
      var pfCount = document.getElementById('packetFilterCount');
      if (!pfInput || !window.PacketFilter) return;
      var pfTimer = null;
      pfInput.addEventListener('input', function() {
        clearTimeout(pfTimer);
        pfTimer = setTimeout(function() {
          var expr = pfInput.value.trim();
          if (!expr) {
            pfInput.classList.remove('filter-error', 'filter-active');
            pfError.style.display = 'none';
            pfCount.style.display = 'none';
            filters._packetFilter = null;
            renderTableRows();
            return;
          }
          var compiled = PacketFilter.compile(expr);
          if (compiled.error) {
            pfInput.classList.add('filter-error');
            pfInput.classList.remove('filter-active');
            pfError.textContent = compiled.error;
            pfError.style.display = 'block';
            pfCount.style.display = 'none';
            filters._packetFilter = null;
            renderTableRows();
          } else {
            pfInput.classList.remove('filter-error');
            pfInput.classList.add('filter-active');
            pfError.style.display = 'none';
            filters._packetFilter = compiled.filter;
            renderTableRows();
          }
        }, 300);
      });
    })();

    // --- Observer multi-select ---
    const obsMenu = document.getElementById('observerMenu');
    const obsTrigger = document.getElementById('observerTrigger');
    const selectedObservers = new Set(filters.observer ? filters.observer.split(',') : []);
    function buildObserverMenu() {
      const allChecked = selectedObservers.size === 0;
      let html = `<label class="multi-select-item"><input type="checkbox" data-obs-id="__all__" ${allChecked ? 'checked' : ''}> All Observers</label>`;
      for (const o of observers) {
        const checked = selectedObservers.has(String(o.id)) ? 'checked' : '';
        html += `<label class="multi-select-item"><input type="checkbox" data-obs-id="${o.id}" ${checked}> ${o.name || o.id}</label>`;
      }
      obsMenu.innerHTML = html;
    }
    function updateObsTrigger() {
      if (selectedObservers.size === 0 || selectedObservers.size === observers.length) {
        obsTrigger.textContent = 'All Observers ▾';
      } else if (selectedObservers.size === 1) {
        const id = [...selectedObservers][0];
        const o = observers.find(x => String(x.id) === id);
        obsTrigger.textContent = (o ? (o.name || o.id) : id) + ' ▾';
      } else {
        obsTrigger.textContent = selectedObservers.size + ' Observers ▾';
      }
    }
    buildObserverMenu();
    updateObsTrigger();
    obsTrigger.addEventListener('click', (e) => { e.stopPropagation(); obsMenu.classList.toggle('open'); typeMenu.classList.remove('open'); });
    obsMenu.addEventListener('change', (e) => {
      const id = e.target.dataset.obsId;
      if (id === '__all__') {
        selectedObservers.clear();
      } else {
        if (e.target.checked) selectedObservers.add(id); else selectedObservers.delete(id);
      }
      filters.observer = selectedObservers.size > 0 ? [...selectedObservers].join(',') : undefined;
      if (filters.observer) localStorage.setItem('meshcore-observer-filter', filters.observer); else localStorage.removeItem('meshcore-observer-filter');
      buildObserverMenu();
      updateObsTrigger();
      renderTableRows();
    });

    // --- Type multi-select ---
    const typeMenu = document.getElementById('typeMenu');
    const typeTrigger = document.getElementById('typeTrigger');
    const typeMap = {0:'Request',1:'Response',2:'Direct Msg',3:'ACK',4:'Advert',5:'Channel Msg',7:'Anon Req',8:'Path',9:'Trace'};
    const selectedTypes = new Set(filters.type ? String(filters.type).split(',') : []);
    function buildTypeMenu() {
      const allChecked = selectedTypes.size === 0;
      let html = `<label class="multi-select-item"><input type="checkbox" data-type-id="__all__" ${allChecked ? 'checked' : ''}> All Types</label>`;
      for (const [k, v] of Object.entries(typeMap)) {
        const checked = selectedTypes.has(k) ? 'checked' : '';
        html += `<label class="multi-select-item"><input type="checkbox" data-type-id="${k}" ${checked}> ${v}</label>`;
      }
      typeMenu.innerHTML = html;
    }
    function updateTypeTrigger() {
      const total = Object.keys(typeMap).length;
      if (selectedTypes.size === 0 || selectedTypes.size === total) {
        typeTrigger.textContent = 'All Types ▾';
      } else if (selectedTypes.size === 1) {
        const k = [...selectedTypes][0];
        typeTrigger.textContent = (typeMap[k] || k) + ' ▾';
      } else {
        typeTrigger.textContent = selectedTypes.size + ' Types ▾';
      }
    }
    buildTypeMenu();
    updateTypeTrigger();
    typeTrigger.addEventListener('click', (e) => { e.stopPropagation(); typeMenu.classList.toggle('open'); obsMenu.classList.remove('open'); });
    typeMenu.addEventListener('change', (e) => {
      const id = e.target.dataset.typeId;
      if (id === '__all__') {
        selectedTypes.clear();
      } else {
        if (e.target.checked) selectedTypes.add(id); else selectedTypes.delete(id);
      }
      filters.type = selectedTypes.size > 0 ? [...selectedTypes].join(',') : undefined;
      if (filters.type) localStorage.setItem('meshcore-type-filter', filters.type); else localStorage.removeItem('meshcore-type-filter');
      buildTypeMenu();
      updateTypeTrigger();
      renderTableRows();
    });

    // Close multi-select menus on outside click
    bindDocumentHandler('menu', 'click', (e) => {
      const obsWrap = document.getElementById('observerFilterWrap');
      const typeWrap = document.getElementById('typeFilterWrap');
      if (obsWrap && !obsWrap.contains(e.target)) { const m = obsWrap.querySelector('.multi-select-menu'); if (m) m.classList.remove('open'); }
      if (typeWrap && !typeWrap.contains(e.target)) { const m = typeWrap.querySelector('.multi-select-menu'); if (m) m.classList.remove('open'); }
    });

    // Filter toggle button for mobile
    document.getElementById('filterToggleBtn').addEventListener('click', function() {
      const bar = document.getElementById('pktFilters');
      bar.classList.toggle('filters-expanded');
      this.textContent = bar.classList.contains('filters-expanded') ? 'Filters ▴' : 'Filters ▾';
    });

    // Filter event listeners
    document.getElementById('fHash').value = filters.hash || '';
    document.getElementById('fHash').addEventListener('input', debounce((e) => { filters.hash = e.target.value || undefined; loadPackets(); }, 300));

    // Time window dropdown — restore from localStorage and bind change
    const fTimeWindow = document.getElementById('fTimeWindow');
    const savedWindow = localStorage.getItem('meshcore-time-window');
    if (savedWindow !== null) fTimeWindow.value = savedWindow;
    fTimeWindow.addEventListener('change', () => {
      localStorage.setItem('meshcore-time-window', fTimeWindow.value);
      loadPackets();
    });

    document.getElementById('fGroup').addEventListener('click', () => { groupByHash = !groupByHash; loadPackets(); });
    document.getElementById('fMyNodes').addEventListener('click', function () {
      filters.myNodes = !filters.myNodes;
      this.classList.toggle('active', filters.myNodes);
      loadPackets();
    });

    // Observation sort dropdown
    const obsSortSel = document.getElementById('fObsSort');
    obsSortSel.value = obsSortMode;
    const sortHelpEl = document.getElementById('sortHelpIcon');
    if (sortHelpEl) {
      const tip = document.createElement('span');
      tip.className = 'sort-help-tip';
      tip.textContent = "Sort controls how observations are ordered within packet groups and which observation appears in the header row.\n\nObserver — Groups by observer station, earliest first.\nPath \u2191 — Shortest paths first.\nPath \u2193 — Longest paths first.\nTime \u2191 — Earliest observation first.\nTime \u2193 — Most recent first.";
      sortHelpEl.appendChild(tip);
    }
    obsSortSel.addEventListener('change', async function () {
      obsSortMode = this.value;
      localStorage.setItem('meshcore-obs-sort', obsSortMode);
      // For non-observer sorts, fetch children for visible groups that don't have them yet
      if (obsSortMode !== SORT_OBSERVER && groupByHash) {
        const toFetch = packets.filter(p => p.hash && !p._children && (p.observation_count || 0) > 1);
        await Promise.all(toFetch.map(async (p) => {
          try {
            const data = await api(`/packets/${p.hash}`);
            if (data?.packet && data.observations) {
              p._children = data.observations.map(o => ({...data.packet, ...o, _isObservation: true}));
              p._fetchedData = data;
            }
          } catch {}
        }));
      }
      // Re-sort all groups with children
      for (const p of packets) {
        if (p._children) sortGroupChildren(p);
      }
      // Resolve any new hops from updated header paths
      const newHops = new Set();
      for (const p of packets) {
        try { JSON.parse(p.path_json || '[]').forEach(h => { if (!(h in hopNameCache)) newHops.add(h); }); } catch {}
      }
      if (newHops.size) await resolveHops([...newHops]);
      renderTableRows();
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
    bindDocumentHandler('colmenu', 'click', () => colMenu.classList.remove('open'));
    applyColVisibility();

    document.getElementById('hexHashToggle').addEventListener('click', function () {
      showHexHashes = !showHexHashes;
      localStorage.setItem('meshcore-hex-hashes', showHexHashes);
      this.classList.toggle('active', showHexHashes);
      renderTableRows();
    });

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
        else if (action === 'select-observation') {
          const parentHash = row.dataset.parentHash;
          const group = packets.find(p => p.hash === parentHash);
          const child = group?._children?.find(c => String(c.id) === String(value));
          if (child) {
            const parentData = group._fetchedData;
            const obsPacket = parentData ? {...parentData.packet, observer_id: child.observer_id, observer_name: child.observer_name, snr: child.snr, rssi: child.rssi, path_json: child.path_json, timestamp: child.timestamp, first_seen: child.timestamp} : child;
            selectPacket(child.id, parentHash, {packet: obsPacket, breakdown: parentData?.breakdown, observations: parentData?.observations}, child.id);
          }
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
        closeDetailPanel();
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

    // Client-side type/observer filtering
    if (filters.type) {
      const types = filters.type.split(',').map(Number);
      displayPackets = displayPackets.filter(p => types.includes(p.payload_type));
    }
    if (filters.observer) {
      const obsIds = new Set(filters.observer.split(','));
      displayPackets = displayPackets.filter(p => obsIds.has(p.observer_id));
    }

    // Packet Filter Language
    const pfCount = document.getElementById('packetFilterCount');
    if (filters._packetFilter) {
      const beforeCount = displayPackets.length;
      displayPackets = displayPackets.filter(filters._packetFilter);
      if (pfCount) {
        pfCount.textContent = 'Showing ' + displayPackets.length.toLocaleString() + ' of ' + beforeCount.toLocaleString() + ' packets';
        pfCount.style.display = 'block';
      }
    } else if (pfCount) {
      pfCount.style.display = 'none';
    }

    if (countEl) countEl.textContent = `(${displayPackets.length})`;

    if (!displayPackets.length) {
      tbody.innerHTML = '<tr><td colspan="10" class="text-center text-muted" style="padding:24px">' + (filters.myNodes ? 'No packets from your claimed/favorited nodes' : 'No packets found') + '</td></tr>';
      return;
    }

    if (groupByHash) {
      let html = '';
      for (const p of displayPackets) {
        const isExpanded = expandedHashes.has(p.hash);
        // When observer filter is active, use first matching child's data for header
        let headerObserverId = p.observer_id;
        let headerPathJson = p.path_json;
        if (filters.observer && p._children?.length) {
          const obsIds = new Set(filters.observer.split(','));
          const match = p._children.find(c => obsIds.has(String(c.observer_id)));
          if (match) {
            headerObserverId = match.observer_id;
            headerPathJson = match.path_json;
          }
        }
        const groupRegion = headerObserverId ? (observers.find(o => o.id === headerObserverId)?.iata || '') : '';
        let groupPath = [];
        try { groupPath = JSON.parse(headerPathJson || '[]'); } catch {}
        const groupPathStr = renderPath(groupPath, headerObserverId);
        const groupTypeName = payloadTypeName(p.payload_type);
        const groupTypeClass = payloadTypeColor(p.payload_type);
        const groupSize = p.raw_hex ? Math.floor(p.raw_hex.length / 2) : 0;
        const groupHashBytes = ((parseInt(p.raw_hex?.slice(2, 4), 16) || 0) >> 6) + 1;
        const isSingle = p.count <= 1;
        html += `<tr class="${isSingle ? '' : 'group-header'} ${isExpanded ? 'expanded' : ''}" data-hash="${p.hash}" data-action="${isSingle ? 'select-hash' : 'toggle-select'}" data-value="${p.hash}" tabindex="0" role="row">
          <td style="width:28px;text-align:center;cursor:pointer">${isSingle ? '' : (isExpanded ? '▼' : '▶')}</td>
          <td class="col-region">${groupRegion ? `<span class="badge-region">${groupRegion}</span>` : '—'}</td>
          <td class="col-time">${renderTimestampCell(p.latest)}</td>
          <td class="mono col-hash">${truncate(p.hash || '—', 8)}</td>
          <td class="col-size">${groupSize ? groupSize + 'B' : '—'}</td>
          <td class="col-hashsize mono">${groupHashBytes}</td>
          <td class="col-type">${p.payload_type != null ? `<span class="badge badge-${groupTypeClass}">${groupTypeName}</span>` : '—'}</td>
          <td class="col-observer">${isSingle ? truncate(obsName(headerObserverId), 16) : truncate(obsName(headerObserverId), 10) + (p.observer_count > 1 ? ' +' + (p.observer_count - 1) : '')}</td>
          <td class="col-path"><span class="path-hops">${groupPathStr}</span></td>
          <td class="col-rpt">${p.observation_count > 1 ? '<span class="badge badge-obs" title="Seen ' + p.observation_count + ' times">👁 ' + p.observation_count + '</span>' : (isSingle ? '' : p.count)}</td>
          <td class="col-details">${getDetailPreview((() => { try { return JSON.parse(p.decoded_json || '{}'); } catch { return {}; } })())}</td>
        </tr>`;
        // Child rows (loaded async when expanded)
        if (isExpanded && p._children) {
          let visibleChildren = p._children;
          // Filter children by selected observers
          if (filters.observer) {
            const obsSet = new Set(filters.observer.split(','));
            visibleChildren = visibleChildren.filter(c => obsSet.has(String(c.observer_id)));
          }
          for (const c of visibleChildren) {
            const typeName = payloadTypeName(c.payload_type);
            const typeClass = payloadTypeColor(c.payload_type);
            const size = c.raw_hex ? Math.floor(c.raw_hex.length / 2) : 0;
            const childHashBytes = ((parseInt(c.raw_hex?.slice(2, 4), 16) || 0) >> 6) + 1;
            const childRegion = c.observer_id ? (observers.find(o => o.id === c.observer_id)?.iata || '') : '';
            let childPath = [];
            try { childPath = JSON.parse(c.path_json || '[]'); } catch {}
            const childPathStr = renderPath(childPath, c.observer_id);
            html += `<tr class="group-child" data-id="${c.id}" data-hash="${c.hash || ''}" data-action="select-observation" data-value="${c.id}" data-parent-hash="${p.hash}" tabindex="0" role="row">
              <td></td><td class="col-region">${childRegion ? `<span class="badge-region">${childRegion}</span>` : '—'}</td>
              <td class="col-time">${renderTimestampCell(c.timestamp)}</td>
              <td class="mono col-hash">${truncate(c.hash || '', 8)}</td>
              <td class="col-size">${size}B</td>
              <td class="col-hashsize mono">${childHashBytes}</td>
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
      const hashBytes = ((parseInt(p.raw_hex?.slice(2, 4), 16) || 0) >> 6) + 1;
      const pathStr = renderPath(pathHops, p.observer_id);      const detail = getDetailPreview(decoded);

      return `<tr data-id="${p.id}" data-hash="${p.hash || ''}" data-action="select-hash" data-value="${p.hash || p.id}" tabindex="0" role="row" class="${selectedId === p.id ? 'selected' : ''}">
        <td></td><td class="col-region">${region ? `<span class="badge-region">${region}</span>` : '—'}</td>
        <td class="col-time">${renderTimestampCell(p.timestamp)}</td>
        <td class="mono col-hash">${truncate(p.hash || String(p.id), 8)}</td>
        <td class="col-size">${size}B</td>
        <td class="col-hashsize mono">${hashBytes}</td>
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
    // Channel messages (GRP_TXT) — show channel name and message text
    if (decoded.type === 'CHAN' && decoded.text) {
      const ch = decoded.channel ? `<span class="chan-tag">${escapeHtml(decoded.channel)}</span> ` : '';
      const t = decoded.text.length > 80 ? decoded.text.slice(0, 80) + '…' : decoded.text;
      return `${ch}💬 ${escapeHtml(t)}`;
    }
    // Advertisements — show node name and role
    if (decoded.type === 'ADVERT' && decoded.name) {
      const role = decoded.flags?.repeater ? '📡' : decoded.flags?.room ? '🏠' : decoded.flags?.sensor ? '🌡' : '📻';
      return `${role} <a href="#/nodes/${encodeURIComponent(decoded.pubKey)}" class="hop-link hop-named" data-hop-link="true">${escapeHtml(decoded.name)}</a>`;
    }
    // Undecrypted channel messages — show channel hash and decryption status
    if (decoded.type === 'GRP_TXT' && decoded.channelHash != null) {
      const hashHex = decoded.channelHashHex || decoded.channelHash.toString(16).padStart(2, '0').toUpperCase();
      const statusLabel = decoded.decryptionStatus === 'no_key' ? 'no key' : 'decryption failed';
      return `🔒 Ch 0x${hashHex} <span class="muted">(${statusLabel})</span>`;
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

  let selectedObservationId = null;

  async function selectPacket(id, hash, prefetchedData, obsRowId) {
    selectedId = id;
    selectedObservationId = obsRowId || null;
    const obsParam = selectedObservationId ? `?obs=${selectedObservationId}` : '';
    if (hash) {
      history.replaceState(null, '', `#/packets/${hash}${obsParam}`);
    } else {
      history.replaceState(null, '', `#/packets/${id}${obsParam}`);
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
      var layout = panel.closest('.split-layout');
      if (layout) layout.classList.remove('detail-collapsed');
      panel.innerHTML = '<div class="panel-resize-handle" id="pktResizeHandle"></div>' + PANEL_CLOSE_HTML + '<div class="text-center text-muted" style="padding:40px">Loading…</div>';
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
      panel.innerHTML = isMobileNow ? '' : '<div class="panel-resize-handle" id="pktResizeHandle"></div>' + PANEL_CLOSE_HTML;
      const content = document.createElement('div');
      panel.appendChild(content);
      await renderDetail(content, data);
      if (!isMobileNow) initPanelResize();
    } catch (e) {
      panel.innerHTML = `<div class="text-muted">Error: ${e.message}</div>`;
    }
  }

  async function renderDetail(panel, data) {
    const pkt = data.packet;
    const breakdown = data.breakdown || {};
    const ranges = breakdown.ranges || [];
    let decoded;
    try { decoded = JSON.parse(pkt.decoded_json); } catch { decoded = {}; }
    let pathHops;
    try { pathHops = JSON.parse(pkt.path_json || '[]'); } catch { pathHops = []; }

    // Resolve sender GPS — from packet directly, or from known node in DB
    let senderLat = decoded.lat != null ? decoded.lat : (decoded.latitude || null);
    let senderLon = decoded.lon != null ? decoded.lon : (decoded.longitude || null);
    if (senderLat == null) {
      // Try to find sender node GPS from DB
      const senderKey = decoded.pubKey || decoded.srcPubKey;
      const senderName = decoded.sender || decoded.name;
      try {
        if (senderKey) {
          const nd = await api(`/nodes/${senderKey}`, { ttl: 30000 }).catch(() => null);
          if (nd?.node?.lat && nd.node.lon) { senderLat = nd.node.lat; senderLon = nd.node.lon; }
        }
        if (senderLat == null && senderName) {
          const sd = await api(`/nodes/search?q=${encodeURIComponent(senderName)}`, { ttl: 30000 }).catch(() => null);
          const match = sd?.nodes?.[0];
          if (match?.lat && match.lon) { senderLat = match.lat; senderLon = match.lon; }
        }
      } catch {}
    }

    // Re-resolve hops using client-side HopResolver with sender GPS context
    if (pathHops.length) {
      try {
        await ensureHopResolver();
        const resolved = HopResolver.resolve(pathHops);
        if (resolved) {
          for (const [k, v] of Object.entries(resolved)) {
            hopNameCache[k] = v;
            if (pkt.observer_id) hopNameCache[k + ':' + pkt.observer_id] = v;
          }
        }
      } catch {}
    }

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
      messageHtml = `<div class="detail-message" style="padding:12px;margin:8px 0;background:var(--card-bg);border-radius:8px;border-left:3px solid var(--accent)">
        <div style="font-size:1.1em">${escapeHtml(decoded.text)}</div>
        ${meta ? `<div style="font-size:0.85em;color:var(--muted);margin-top:4px">${meta}</div>` : ''}
      </div>`;
    } else if (decoded.type === 'GRP_TXT' && decoded.channelHash != null) {
      const hashHex = decoded.channelHashHex || decoded.channelHash.toString(16).padStart(2, '0').toUpperCase();
      const statusLabel = decoded.decryptionStatus === 'no_key' ? 'no key' : 'decryption failed';
      messageHtml = `<div class="detail-message" style="padding:12px;margin:8px 0;background:var(--card-bg);border-radius:8px;border-left:3px solid var(--warning, #f0ad4e)">
        <div style="font-size:1.1em">🔒 Channel Hash: 0x${hashHex} <span style="color:var(--muted)">(${statusLabel})</span></div>
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

    // Location: from ADVERT lat/lon, or from known node via pubkey/sender name
    let locationHtml = '—';
    let locationNodeKey = null;
    if (decoded.lat != null && decoded.lon != null && !(decoded.lat === 0 && decoded.lon === 0)) {
      locationNodeKey = decoded.pubKey || decoded.srcPubKey || '';
      const nodeName = decoded.name || '';
      locationHtml = `${decoded.lat.toFixed(5)}, ${decoded.lon.toFixed(5)}`;
      if (nodeName) locationHtml = `${escapeHtml(nodeName)} — ${locationHtml}`;
      if (locationNodeKey) locationHtml += ` <a href="#/map?node=${encodeURIComponent(locationNodeKey)}" style="font-size:0.85em">📍map</a>`;
    } else {
      // Try to resolve sender node location from nodes list
      const senderKey = decoded.pubKey || decoded.srcPubKey;
      const senderName = decoded.sender || decoded.name;
      if (senderKey || senderName) {
        try {
          const nodeData = senderKey ? await api(`/nodes/${senderKey}`, { ttl: 30000 }).catch(() => null) : null;
          if (nodeData && nodeData.node && nodeData.node.lat && nodeData.node.lon) {
            locationNodeKey = nodeData.node.public_key;
            locationHtml = `${nodeData.node.lat.toFixed(5)}, ${nodeData.node.lon.toFixed(5)}`;
            if (nodeData.node.name) locationHtml = `${escapeHtml(nodeData.node.name)} — ${locationHtml}`;
            locationHtml += ` <a href="#/map?node=${encodeURIComponent(locationNodeKey)}" style="font-size:0.85em">📍map</a>`;
          } else if (senderName && !senderKey) {
            // Search by name
            const searchData = await api(`/nodes/search?q=${encodeURIComponent(senderName)}`, { ttl: 30000 }).catch(() => null);
            const match = searchData && searchData.nodes && searchData.nodes[0];
            if (match && match.lat && match.lon) {
              locationNodeKey = match.public_key;
              locationHtml = `${match.lat.toFixed(5)}, ${match.lon.toFixed(5)}`;
              locationHtml = `${escapeHtml(match.name)} — ${locationHtml}`;
              locationHtml += ` <a href="#/map?node=${encodeURIComponent(locationNodeKey)}" style="font-size:0.85em">📍map</a>`;
            }
          }
        } catch {}
      }
    }

    panel.innerHTML = `
      <div class="detail-title">${hasRawHex ? `Packet Byte Breakdown (${size} bytes)` : typeName + ' Packet'}</div>
      <div class="detail-hash">${pkt.hash || 'Packet #' + pkt.id}</div>
      ${messageHtml}
      <dl class="detail-meta">
        <dt>Observer</dt><dd>${obsName(pkt.observer_id)}</dd>
        <dt>Location</dt><dd>${locationHtml}</dd>
        <dt>SNR / RSSI</dt><dd>${snr != null ? snr + ' dB' : '—'} / ${rssi != null ? rssi + ' dBm' : '—'}</dd>
        <dt>Route Type</dt><dd>${routeTypeName(pkt.route_type)}</dd>
        <dt>Payload Type</dt><dd><span class="badge badge-${payloadTypeColor(pkt.payload_type)}">${typeName}</span></dd>
        ${hashSize ? `<dt>Hash Size</dt><dd>${hashSize} byte${hashSize !== 1 ? 's' : ''}</dd>` : ''}
        <dt>Timestamp</dt><dd>${renderTimestampCell(pkt.timestamp)}</dd>
        <dt>Propagation</dt><dd>${propagationHtml}</dd>
        <dt>Path</dt><dd>${pathHops.length ? renderPath(pathHops, pkt.observer_id) : '—'}</dd>
      </dl>
      <div class="detail-actions">
        <button class="copy-link-btn" data-packet-hash="${pkt.hash || ''}" data-packet-id="${pkt.id}" title="Copy link to this packet">🔗 Copy Link</button>
        ${pathHops.length ? `<button class="detail-map-link" id="viewRouteBtn">🗺️ View route on map</button>` : ''}
        ${pkt.hash ? `<a href="#/traces/${pkt.hash}" class="detail-map-link" style="text-decoration:none">🔍 Trace</a>` : ''}
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
        const obsParam = selectedObservationId ? `?obs=${selectedObservationId}` : '';
        const url = pktHash ? `${location.origin}/#/packets/${pktHash}${obsParam}` : `${location.origin}/#/packets/${copyLinkBtn.dataset.packetId}${obsParam}`;
        window.copyToClipboard(url, () => {
          copyLinkBtn.textContent = '✅ Copied!';
          setTimeout(() => { copyLinkBtn.textContent = '🔗 Copy Link'; }, 1500);
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
              id: o.id, hash: pkt.hash, raw: o.raw_hex || pkt.raw_hex,
              _ts: new Date(o.timestamp).getTime(),
              decoded: { header: { payloadTypeName: typeName }, payload: oDec, path: { hops: oPath } },
              snr: o.snr, rssi: o.rssi, observer: obsName(o.observer_id)
            });
          }
        } else {
          replayPackets.push({
            id: pkt.id, hash: pkt.hash, raw: pkt.raw_hex,
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
          const data = { resolved: HopResolver.resolve(pathHops, senderLat || null, senderLon || null, obsLat, obsLon, pkt.observer_id) };
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
        const hopHtml = HopDisplay.renderHop(pathHops[i], hopNameCache[pathHops[i]]);
        const label = `Hop ${i} — ${hopHtml}`;
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
        const _typeLabels = {1:'Companion',2:'Repeater',3:'Room Server',4:'Sensor'};
        const _typeName = _typeLabels[decoded.flags.type] || ('Unknown(' + decoded.flags.type + ')');
        const _boolFlags = [decoded.flags.hasLocation && 'location', decoded.flags.hasName && 'name'].filter(Boolean);
        const _flagDesc = _typeName + (_boolFlags.length ? ' + ' + _boolFlags.join(', ') : '');
        rows += fieldRow(off + 100, 'App Flags', '0x' + (decoded.flags.raw?.toString(16).padStart(2,'0') || '??'), _flagDesc);
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
      const hashHex = decoded.channelHashHex || (decoded.channelHash != null ? decoded.channelHash.toString(16).padStart(2, '0').toUpperCase() : '??');
      const statusLabel = decoded.decryptionStatus === 'no_key' ? '(no key)' : decoded.decryptionStatus === 'decryption_failed' ? '(decryption failed)' : '';
      rows += fieldRow(off, 'Channel Hash', `0x${hashHex} ${statusLabel}`, '');
      rows += fieldRow(off + 1, 'MAC (2B)', decoded.mac || '', '');
      rows += fieldRow(off + 3, 'Encrypted Data', truncate(decoded.encryptedData || '', 30), '');
    } else if (decoded.type === 'CHAN') {
      rows += fieldRow(off, 'Channel', decoded.channel || `0x${(decoded.channelHash || 0).toString(16)}`, '');
      rows += fieldRow(off + 1, 'Sender', decoded.sender || '—', '');
      if (decoded.sender_timestamp) rows += fieldRow(off + 2, 'Sender Time', decoded.sender_timestamp, '');
    } else if (decoded.type === 'ACK') {
      rows += fieldRow(off, 'Checksum (4B)', decoded.ackChecksum || '', '');
    } else if (decoded.destHash !== undefined) {
      rows += fieldRow(off, 'Dest Hash (1B)', decoded.destHash || '', '');
      rows += fieldRow(off + 1, 'Src Hash (1B)', decoded.srcHash || '', '');
      rows += fieldRow(off + 2, 'MAC (2B)', decoded.mac || '', '');
      rows += fieldRow(off + 4, 'Encrypted Data', truncate(decoded.encryptedData || '', 30), '');
    } else {
      rows += fieldRow(off, 'Raw', truncate(buf.slice(off * 2), 40), '');
    }

    return `<table class="field-table">
      <thead><tr><th scope="col">Offset</th><th scope="col">Field</th><th scope="col">Value</th><th scope="col">Description</th></tr></thead>
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
    removeAllByopOverlays();
    const triggerBtn = document.querySelector('[data-action="pkt-byop"]');
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay byop-overlay';
    overlay.innerHTML = '<div class="modal byop-modal" role="dialog" aria-label="Decode a Packet" aria-modal="true">'
      + '<div class="byop-header"><h3>📦 Decode a Packet</h3><button class="btn-icon byop-x" title="Close" aria-label="Close dialog">✕</button></div>'
      + '<p class="text-muted" style="margin:0 0 12px;font-size:.85rem">Paste raw hex bytes from your radio or MQTT feed:</p>'
      + '<textarea id="byopHex" class="byop-input" aria-label="Packet hex data" placeholder="e.g. 15C31A8D4674FEAE37..." spellcheck="false"></textarea>'
      + '<button class="btn-primary byop-go" id="byopDecode" style="width:100%;margin:8px 0">Decode</button>'
      + '<div id="byopResult" role="status" aria-live="polite"></div>'
      + '</div>';
    document.body.appendChild(overlay);

    const modal = overlay.querySelector('.byop-modal');
    const close = () => { removeAllByopOverlays(); if (triggerBtn) triggerBtn.focus(); };
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
      const result = overlay.querySelector('#byopResult');
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

  // Observation sort modes
  const SORT_OBSERVER = 'observer';
  const SORT_PATH_ASC = 'path-asc';
  const SORT_PATH_DESC = 'path-desc';
  const SORT_CHRONO_ASC = 'chrono-asc';
  const SORT_CHRONO_DESC = 'chrono-desc';
  let obsSortMode = localStorage.getItem('meshcore-obs-sort') || SORT_OBSERVER;

  function getPathHopCount(c) {
    try { return JSON.parse(c.path_json || '[]').length; } catch { return 0; }
  }

  function sortGroupChildren(group) {
    if (!group || !group._children || !group._children.length) return;
    const mode = obsSortMode;

    if (mode === SORT_CHRONO_ASC || mode === SORT_CHRONO_DESC) {
      const dir = mode === SORT_CHRONO_ASC ? 1 : -1;
      group._children.sort((a, b) => {
        const tA = a.timestamp || '', tB = b.timestamp || '';
        return tA < tB ? -dir : tA > tB ? dir : 0;
      });
    } else if (mode === SORT_PATH_ASC || mode === SORT_PATH_DESC) {
      const dir = mode === SORT_PATH_ASC ? 1 : -1;
      group._children.sort((a, b) => {
        const lenA = getPathHopCount(a), lenB = getPathHopCount(b);
        if (lenA !== lenB) return (lenA - lenB) * dir;
        const oA = (a.observer_name || '').toLowerCase(), oB = (b.observer_name || '').toLowerCase();
        return oA < oB ? -1 : oA > oB ? 1 : 0;
      });
    } else {
      // Default: group by observer, earliest-observer first, then ascending time within each
      const earliest = {};
      for (const c of group._children) {
        const obs = c.observer_name || c.observer || '';
        const t = c.timestamp || c.rx_at || c.created_at || '';
        if (!earliest[obs] || t < earliest[obs]) earliest[obs] = t;
      }
      group._children.sort((a, b) => {
        const oA = a.observer_name || a.observer || '', oB = b.observer_name || b.observer || '';
        const eA = earliest[oA] || '', eB = earliest[oB] || '';
        if (eA !== eB) return eA < eB ? -1 : 1;
        if (oA !== oB) return oA < oB ? -1 : 1;
        const tA = a.timestamp || a.rx_at || '', tB = b.timestamp || b.rx_at || '';
        return tA < tB ? -1 : tA > tB ? 1 : 0;
      });
    }

    // Update header row to match first sorted child
    const first = group._children[0];
    if (first) {
      group.observer_id = first.observer_id;
      group.observer_name = first.observer_name;
      group.snr = first.snr;
      group.rssi = first.rssi;
      group.path_json = first.path_json;
      group.direction = first.direction;
    }
  }

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
        group._fetchedData = data;
        // Sort children based on current sort mode
        sortGroupChildren(group);
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

  let _themeRefreshHandler = null;

  registerPage('packets', {
    init: function(app, routeParam) {
      _themeRefreshHandler = () => { if (typeof renderTableRows === 'function') renderTableRows(); };
      window.addEventListener('theme-refresh', _themeRefreshHandler);
      return init(app, routeParam);
    },
    destroy: function() {
      if (_themeRefreshHandler) { window.removeEventListener('theme-refresh', _themeRefreshHandler); _themeRefreshHandler = null; }
      return destroy();
    }
  });

  // Standalone packet detail page: #/packet/123 or #/packet/HASH
  registerPage('packet-detail', {
    init: async (app, routeParam) => {
      const param = routeParam;
      app.innerHTML = `<div style="max-width:800px;margin:0 auto;padding:20px"><div class="text-center text-muted" style="padding:40px">Loading packet…</div></div>`;
      try {
        await loadObservers();
        const data = await api(`/packets/${param}`);
        if (!data?.packet) { app.innerHTML = `<div style="max-width:800px;margin:0 auto;padding:40px;text-align:center"><h2>Packet not found</h2><p>Packet ${param} doesn't exist.</p><a href="#/packets">← Back to packets</a></div>`; return; }
        const hops = [];
        try { const ph = JSON.parse(data.packet.path_json || '[]'); hops.push(...ph); } catch {}
        const newHops = hops.filter(h => !(h in hopNameCache));
        if (newHops.length) await resolveHops(newHops);
        const container = document.createElement('div');
        container.style.cssText = 'max-width:800px;margin:0 auto;padding:20px';
        container.innerHTML = `<div style="margin-bottom:16px"><a href="#/packets" style="color:var(--accent);text-decoration:none">← Back to packets</a></div>`;
        const detail = document.createElement('div');
        container.appendChild(detail);
        await renderDetail(detail, data);
        app.innerHTML = '';
        app.appendChild(container);
      } catch (e) {
        app.innerHTML = `<div style="max-width:800px;margin:0 auto;padding:40px;text-align:center"><h2>Error</h2><p>${e.message}</p><a href="#/packets">← Back to packets</a></div>`;
      }
    },
    destroy: () => {}
  });
})();
