/* === CoreScope — nodes.js === */
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
  // Managed by TableSort utility (public/table-sort.js) when DOM is available,
  // falls back to simple object for unit testing
  var _nodesTableSortCtrl = null;
  // TODO(M5): remove fallback when tests use DOM sandbox
  var _fallbackSortState = null; // used when TableSort controller not initialized (tests)

  function _getSortState() {
    if (_nodesTableSortCtrl) return _nodesTableSortCtrl.getState();
    if (_fallbackSortState) return _fallbackSortState;
    try {
      var saved = JSON.parse(localStorage.getItem('meshcore-nodes-sort'));
      if (saved && saved.column && saved.direction) return saved;
    } catch (e) { /* ignore */ }
    return { column: 'last_seen', direction: 'desc' };
  }

  function sortNodes(arr) {
    var sortState = _getSortState();
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
        va = a.last_heard ? new Date(a.last_heard).getTime() : a.last_seen ? new Date(a.last_seen).getTime() : 0;
        vb = b.last_heard ? new Date(b.last_heard).getTime() : b.last_seen ? new Date(b.last_seen).getTime() : 0;
        return (va - vb) * dir;
      } else if (col === 'advert_count') {
        va = a.advert_count || 0; vb = b.advert_count || 0;
        return (va - vb) * dir;
      }
      return 0;
    });
  }
  let lastHeard = localStorage.getItem('meshcore-nodes-last-heard') || '';
  let statusFilter = localStorage.getItem('meshcore-nodes-status-filter') || 'all';
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

  function renderNodeTimestampHtml(isoString) {
    if (typeof formatTimestampWithTooltip !== 'function' || typeof getTimestampMode !== 'function') {
      return escapeHtml(typeof timeAgo === 'function' ? timeAgo(isoString) : '—');
    }
    const f = formatTimestampWithTooltip(isoString, getTimestampMode());
    const warn = f.isFuture
      ? ' <span class="timestamp-future-icon" title="Timestamp is in the future — node clock may be skewed">⚠️</span>'
      : '';
    return `<span class="timestamp-text" title="${escapeHtml(f.tooltip)}">${escapeHtml(f.text)}</span>${warn}`;
  }

  function renderNodeTimestampText(isoString) {
    if (typeof formatTimestamp !== 'function' || typeof getTimestampMode !== 'function') {
      return typeof timeAgo === 'function' ? timeAgo(isoString) : '—';
    }
    return formatTimestamp(isoString, getTimestampMode());
  }

  /* === Shared helper functions for node detail rendering === */

  function getStatusTooltip(role, status) {
    const isInfra = role === 'repeater' || role === 'room';
    const threshMs = isInfra ? HEALTH_THRESHOLDS.infraSilentMs : HEALTH_THRESHOLDS.nodeSilentMs;
    const threshold = threshMs >= 3600000 ? Math.round(threshMs / 3600000) + 'h' : Math.round(threshMs / 60000) + 'm';
    if (status === 'active') {
      return 'Active \u2014 heard within the last ' + threshold + '.' + (isInfra ? ' Repeaters typically advertise every 12-24h.' : '');
    }
    if (role === 'companion') {
      return 'Stale \u2014 not heard for over ' + threshold + '. Companions only advertise when the user initiates \u2014 this may be normal.';
    }
    if (role === 'sensor') {
      return 'Stale \u2014 not heard for over ' + threshold + '. This sensor may be offline.';
    }
    return 'Stale \u2014 not heard for over ' + threshold + '. This ' + role + ' may be offline or out of range.';
  }

  function getStatusInfo(n) {
    // Single source of truth for all status-related info
    const role = (n.role || '').toLowerCase();
    const roleColor = ROLE_COLORS[n.role] || '#6b7280';
    // Prefer last_heard (from in-memory packets) > _lastHeard (health API) > last_seen (DB)
    const lastHeardTime = n._lastHeard || n.last_heard || n.last_seen;
    const lastHeardMs = lastHeardTime ? new Date(lastHeardTime).getTime() : 0;
    const status = getNodeStatus(role, lastHeardMs);
    const statusTooltip = getStatusTooltip(role, status);
    const statusLabel = status === 'active' ? '🟢 Active' : '⚪ Stale';
    const statusAge = lastHeardMs ? (Date.now() - lastHeardMs) : Infinity;

    let explanation = '';
    if (status === 'active') {
      explanation = 'Last heard ' + (lastHeardTime ? renderNodeTimestampText(lastHeardTime) : 'unknown');
    } else {
      const ageDays = Math.floor(statusAge / 86400000);
      const ageHours = Math.floor(statusAge / 3600000);
      const ageStr = ageDays >= 1 ? ageDays + 'd' : ageHours + 'h';
      const isInfra = role === 'repeater' || role === 'room';
      const reason = isInfra
        ? 'repeaters typically advertise every 12-24h'
        : 'companions only advertise when user initiates, this may be normal';
      explanation = 'Not heard for ' + ageStr + ' — ' + reason;
    }

    return { status, statusLabel, statusTooltip, statusAge, explanation, roleColor, lastHeardMs, role };
  }

  function renderNodeBadges(n, roleColor) {
    // Returns HTML for: role badge, hash prefix badge, hash inconsistency link, status label
    const info = getStatusInfo(n);
    let html = `<span class="badge" style="background:${roleColor}20;color:${roleColor}">${n.role}</span>`;
    if (n.hash_size) {
      html += ` <span class="badge" style="background:var(--nav-bg);color:var(--nav-text);font-family:var(--mono)">${n.public_key.slice(0, n.hash_size * 2).toUpperCase()}</span>`;
    }
    if (n.hash_size_inconsistent) {
      html += ` <a href="#/nodes/${encodeURIComponent(n.public_key)}?section=node-packets" class="badge" style="background:var(--status-yellow);color:#000;font-size:10px;cursor:pointer;text-decoration:none">⚠️ variable hash size</a>`;
    }
    html += ` <span title="${info.statusTooltip}">${info.statusLabel}</span>`;
    return html;
  }

  function renderStatusExplanation(n) {
    const info = getStatusInfo(n);
    return `<div style="font-size:12px;color:var(--text-muted);margin:4px 0 6px"><span title="${info.statusTooltip}">${info.statusLabel}</span> — ${info.explanation}</div>`;
  }

  function renderHashInconsistencyWarning(n) {
    if (!n.hash_size_inconsistent) return '';
    const sizes = Array.isArray(n.hash_sizes_seen) ? n.hash_sizes_seen : [];
    return `<div style="font-size:11px;color:var(--text-muted);margin:-2px 0 6px;padding:6px 10px;background:var(--surface-2);border-radius:4px;border-left:3px solid var(--status-yellow)">Adverts show varying hash sizes (<strong>${sizes.join('-byte, ')}-byte</strong>). This is a <a href="https://github.com/meshcore-dev/MeshCore/commit/fcfdc5f" target="_blank" style="color:var(--accent)">known bug</a> where automatic adverts ignore the configured multibyte path setting. Fixed in <a href="https://github.com/meshcore-dev/MeshCore/releases/tag/repeater-v1.14.1" target="_blank" style="color:var(--accent)">repeater v1.14.1</a>.</div>`;
  }

  // ─── Neighbor section helpers ───────────────────────────────────────────────

  // Cache: pubkey → { data, ts }
  var _neighborCache = {};

  function getConfidenceIndicator(entry) {
    if (entry.ambiguous) return { icon: '⚠️', label: 'AMBIGUOUS', cls: 'confidence-ambiguous' };
    if (entry.count <= 1) return { icon: '🔴', label: 'LOW', cls: 'confidence-low' };
    if (entry.score >= 0.5 && entry.count >= 3) return { icon: '🟢', label: 'HIGH', cls: 'confidence-high' };
    return { icon: '🟡', label: 'MEDIUM', cls: 'confidence-medium' };
  }

  function renderNeighborRows(neighbors, limit) {
    var sorted = neighbors.slice().sort(function(a, b) {
      return (b.count || 0) - (a.count || 0);
    });
    var items = limit ? sorted.slice(0, limit) : sorted;
    return items.map(function(nb) {
      var conf = getConfidenceIndicator(nb);
      var name = nb.name || (nb.prefix + '… (unknown)');
      var nameHtml = nb.pubkey
        ? '<a href="#/nodes/' + encodeURIComponent(nb.pubkey) + '">' + escapeHtml(name) + '</a>'
        : '<span class="text-muted">' + escapeHtml(name) + '</span>';
      var role = nb.role || '—';
      var roleBadge = nb.role
        ? '<span class="badge" style="background:' + (ROLE_COLORS[nb.role] || 'var(--surface-2)') + ';color:#fff;font-size:10px">' + escapeHtml(role) + '</span>'
        : '<span class="text-muted">—</span>';
      var scoreTitle = 'Observations: ' + nb.count;
      if (nb.avg_snr != null) scoreTitle += ' · Avg SNR: ' + Number(nb.avg_snr).toFixed(1) + ' dB';
      var distanceCell = nb.distance_km != null
        ? Number(nb.distance_km).toFixed(1) + ' km'
        : '<span class="text-muted">—</span>';
      var showOnMap = nb.pubkey
        ? ' <button class="btn-link neighbor-show-map" data-pubkey="' + escapeHtml(nb.pubkey) + '" style="font-size:11px;padding:1px 6px;white-space:nowrap">📍 Map</button>'
        : '';
      var lastSeenVal = nb.last_seen ? new Date(nb.last_seen).getTime() : 0;
      var distanceVal = nb.distance_km != null ? Number(nb.distance_km) : '';
      return '<tr>' +
        '<td data-value="' + escapeHtml(name.toLowerCase()) + '" style="font-weight:600">' + nameHtml + '</td>' +
        '<td data-value="' + escapeHtml(role.toLowerCase()) + '">' + roleBadge + '</td>' +
        '<td data-value="' + Number(nb.score || 0) + '" title="' + escapeHtml(scoreTitle) + '">' + Number(nb.score).toFixed(2) + '</td>' +
        '<td data-value="' + (nb.count || 0) + '">' + nb.count + '</td>' +
        '<td data-value="' + lastSeenVal + '">' + renderNodeTimestampHtml(nb.last_seen) + '</td>' +
        '<td data-value="' + distanceVal + '">' + distanceCell + '</td>' +
        '<td><span title="' + conf.label + '">' + conf.icon + '</span></td>' +
        '<td style="text-align:right">' + showOnMap + '</td>' +
        '</tr>';
    }).join('');
  }

  function renderNeighborTable(neighbors, limit) {
    return '<table class="data-table neighbor-sort-table" style="font-size:12px">' +
      '<thead><tr>' +
      '<th scope="col" data-sort="name">Neighbor</th>' +
      '<th scope="col" data-sort="role">Role</th>' +
      '<th scope="col" data-sort="score" data-type="number" data-sort-default="desc">Score</th>' +
      '<th scope="col" data-sort="count" data-type="number" data-sort-default="desc">Obs</th>' +
      '<th scope="col" data-sort="last_seen" data-type="number" data-sort-default="desc">Last Seen</th>' +
      '<th scope="col" data-sort="distance" data-type="number">Distance</th>' +
      '<th scope="col">Conf</th><th scope="col"></th>' +
      '</tr></thead>' +
      '<tbody>' + renderNeighborRows(neighbors, limit) + '</tbody></table>';
  }

  function fetchAndRenderNeighbors(pubkey, containerId, opts) {
    opts = opts || {};
    var limit = opts.limit || 0;
    var headerSelector = opts.headerSelector;
    var viewAllPubkey = opts.viewAllPubkey;

    // Always set spinner as initial DOM state (synchronous) so tests can observe it
    var spinnerEl = document.getElementById(containerId);
    if (spinnerEl) spinnerEl.innerHTML = '<div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading neighbors…</div>';

    // Check cache
    var cached = _neighborCache[pubkey];
    if (cached && (Date.now() - cached.ts < 300000)) { // 5 min cache
      renderNeighborData(cached.data, containerId, limit, headerSelector, viewAllPubkey);
      return;
    }

    api('/nodes/' + encodeURIComponent(pubkey) + '/neighbors', { ttl: CLIENT_TTL.nodeDetail }).then(function(data) {
      _neighborCache[pubkey] = { data: data, ts: Date.now() };
      renderNeighborData(data, containerId, limit, headerSelector, viewAllPubkey);
    }).catch(function() {
      var el = document.getElementById(containerId);
      if (el) el.innerHTML = '<div class="text-muted" style="padding:8px">Could not load neighbor data</div>';
    });
  }

  function renderNeighborData(data, containerId, limit, headerSelector, viewAllPubkey) {
    var el = document.getElementById(containerId);
    if (!el) return;
    if (!data || !data.neighbors || !data.neighbors.length) {
      el.innerHTML = '<div class="text-muted" style="padding:8px">No neighbor data available yet. Neighbor relationships are built from observed packet paths over time.</div>';
      if (headerSelector) {
        var h = document.querySelector(headerSelector);
        if (h) h.textContent = 'Neighbors (0)';
      }
      return;
    }
    if (headerSelector) {
      var h = document.querySelector(headerSelector);
      if (h) h.textContent = 'Neighbors (' + data.neighbors.length + ')';
    }
    var html = renderNeighborTable(data.neighbors, limit);
    if (limit && data.neighbors.length > limit && viewAllPubkey) {
      html += '<div style="margin-top:6px;text-align:right"><a href="#/nodes/' + encodeURIComponent(viewAllPubkey) + '?section=node-neighbors" style="font-size:12px">View all ' + data.neighbors.length + ' neighbors →</a></div>';
    }
    el.innerHTML = html;

    // Initialize TableSort on neighbor table
    var neighborTable = el.querySelector('.neighbor-sort-table');
    if (neighborTable && window.TableSort) {
      TableSort.init(neighborTable, {
        defaultColumn: 'count',
        defaultDirection: 'desc'
      });
    }

    // Wire up "Show on Map" buttons via event delegation
    el.addEventListener('click', function(e) {
      var btn = e.target.closest('.neighbor-show-map');
      if (!btn) return;
      var pk = btn.getAttribute('data-pubkey');
      if (pk) location.hash = '#/map?node=' + encodeURIComponent(pk);
    });
  }

  // ─── End neighbor helpers ─────────────────────────────────────────────────

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
        <div class="panel-left" id="nodesLeft" aria-live="polite" aria-relevant="additions removals"></div>
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
    // Auto-refresh when ADVERT packets arrive via WebSocket (fixes #131)
    wsHandler = debouncedOnWS(function (msgs) {
      const advertMsgs = msgs.filter(isAdvertMessage);
      if (!advertMsgs.length) return;

      if (!_allNodes) {
        invalidateApiCache('/nodes');
        loadNodes(true);
        return;
      }

      let needReload = false;
      for (const m of advertMsgs) {
        const payload = m.data && m.data.decoded && m.data.decoded.payload;
        const pubKey = payload && (payload.pubKey || payload.public_key);
        if (!pubKey) { needReload = true; break; }

        const existing = _allNodes.find(n => n.public_key === pubKey);
        if (existing) {
          if (payload.name) existing.name = payload.name;
          if (payload.lat != null) existing.lat = payload.lat;
          if (payload.lon != null) existing.lon = payload.lon;
          const ts = m.data.packet && (m.data.packet.timestamp || m.data.packet.first_seen);
          if (ts) existing.last_seen = ts;
        } else {
          needReload = true;
          break;
        }
      }

      if (needReload) {
        _allNodes = null;
        invalidateApiCache('/nodes');
      }
      loadNodes(true);
    }, 5000);
  }

  /**
   * Fetch node detail + health data in parallel.
   * Both selectNode() and loadFullNode() need the same data —
   * this shared helper avoids duplicating the fetch logic (fixes #391).
   */
  async function fetchNodeDetail(pubkey) {
    const [nodeData, healthData] = await Promise.all([
      api('/nodes/' + encodeURIComponent(pubkey), { ttl: CLIENT_TTL.nodeDetail }),
      api('/nodes/' + encodeURIComponent(pubkey) + '/health', { ttl: CLIENT_TTL.nodeDetail }).catch(() => null)
    ]);
    nodeData.healthData = healthData;
    return nodeData;
  }

  async function loadFullNode(pubkey) {
    const body = document.getElementById('nodeFullBody');
    try {
      const nodeData = await fetchNodeDetail(pubkey);
      const healthData = nodeData.healthData;
      const n = nodeData.node;
      const adverts = (nodeData.recentAdverts || []).sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
      const title = document.querySelector('.node-full-title');
      if (title) title.textContent = n.name || pubkey.slice(0, 12);

      const hasLoc = n.lat != null && n.lon != null;

      // Health stats
      const h = healthData || {};
      const stats = h.stats || {};
      const observers = h.observers || [];
      const recent = h.recentPackets || [];
      const lastHeard = stats.lastHeard;

      // Attach health lastHeard for shared helpers
      n._lastHeard = lastHeard || n.last_seen;
      const si = getStatusInfo(n);
      const roleColor = si.roleColor;
      const statusLabel = si.statusLabel;
      const statusExplanation = si.explanation;

      const dupMap = buildDupNameMap(_allNodes);
      const dupBadge = dupNameBadge(n.name, n.public_key, dupMap);
      const dupKeys = n.name && dupMap[n.name.toLowerCase()] ? dupMap[n.name.toLowerCase()].filter(function(k) { return k !== n.public_key; }) : [];
      const dupSection = dupKeys.length ? '<div class="dup-also-known" style="font-size:11px;color:var(--text-muted);margin-top:4px">Also known as: ' + dupKeys.map(function(k) { return '<a href="#/nodes/' + encodeURIComponent(k) + '" class="mono" style="font-size:11px">' + escapeHtml(k.slice(0, 12)) + '…</a>'; }).join(', ') + '</div>' : '';

      body.innerHTML = `
        <div class="node-full-card" style="padding:12px 16px;margin-bottom:8px">
          <div class="node-detail-name" style="font-size:20px">${escapeHtml(n.name || '(unnamed)')}${dupBadge}</div>
          ${dupSection}
          <div style="margin:4px 0 6px">${renderNodeBadges(n, roleColor)}</div>
          ${renderHashInconsistencyWarning(n)}
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
          <tr><td>Status</td><td><span title="${si.statusTooltip}">${statusLabel}</span> <span style="font-size:11px;color:var(--text-muted);margin-left:4px">${statusExplanation}</span></td></tr>
          <tr><td>Last Heard</td><td>${renderNodeTimestampHtml(lastHeard || n.last_seen)}</td></tr>
          <tr><td>First Seen</td><td>${renderNodeTimestampHtml(n.first_seen)}</td></tr>
          <tr><td>Total Packets</td><td>${stats.totalTransmissions || stats.totalPackets || n.advert_count || 0}${stats.totalObservations && stats.totalObservations !== (stats.totalTransmissions || stats.totalPackets) ? ' <span class="text-muted" style="font-size:0.85em">(seen ' + stats.totalObservations + '×)</span>' : ''}</td></tr>
          <tr><td>Packets Today</td><td>${stats.packetsToday || 0}</td></tr>
          ${stats.avgSnr != null ? `<tr><td>Avg SNR</td><td>${Number(stats.avgSnr).toFixed(1)} dB</td></tr>` : ''}
          ${stats.avgHops ? `<tr><td>Avg Hops</td><td>${stats.avgHops}</td></tr>` : ''}
          ${hasLoc ? `<tr><td>Location</td><td>${Number(n.lat).toFixed(5)}, ${Number(n.lon).toFixed(5)}</td></tr>` : ''}
          <tr><td>Hash Prefix</td><td>${n.hash_size ? '<code style="font-family:var(--mono);font-weight:700">' + n.public_key.slice(0, n.hash_size * 2).toUpperCase() + '</code> (' + n.hash_size + '-byte)' : 'Unknown'}${n.hash_size_inconsistent ? ' <span style="color:var(--status-yellow);cursor:help" title="Seen: ' + (Array.isArray(n.hash_sizes_seen) ? n.hash_sizes_seen : []).join(', ') + '-byte">⚠️ varies</span>' : ''}</td></tr>
        </table>

        ${observers.length ? `<div class="node-full-card" id="node-observers">
          ${(() => { const regions = [...new Set(observers.map(o => o.iata).filter(Boolean))]; return regions.length ? `<div style="margin-bottom:8px"><strong>Regions:</strong> ${regions.map(r => '<span class="badge" style="margin:0 2px">' + escapeHtml(r) + '</span>').join(' ')}</div>` : ''; })()}
          <h4>Heard By (${observers.length} observer${observers.length > 1 ? 's' : ''})</h4>
          <table class="data-table observer-sort-table" style="font-size:12px">
            <thead><tr>
              <th scope="col" data-sort="observer">Observer</th>
              <th scope="col" data-sort="region">Region</th>
              <th scope="col" data-sort="packets" data-type="number" data-sort-default="desc">Packets</th>
              <th scope="col" data-sort="snr" data-type="number" data-sort-default="desc">Avg SNR</th>
              <th scope="col" data-sort="rssi" data-type="number" data-sort-default="desc">Avg RSSI</th>
            </tr></thead>
            <tbody>
              ${observers.map(o => `<tr>
                <td data-value="${escapeHtml((o.observer_name || o.observer_id || '').toLowerCase())}" style="font-weight:600">${escapeHtml(o.observer_name || o.observer_id)}</td>
                <td data-value="${escapeHtml((o.iata || '').toLowerCase())}">${o.iata ? escapeHtml(o.iata) : '—'}</td>
                <td data-value="${o.packetCount || 0}">${o.packetCount}</td>
                <td data-value="${o.avgSnr != null ? Number(o.avgSnr) : ''}">${o.avgSnr != null ? Number(o.avgSnr).toFixed(1) + ' dB' : '—'}</td>
                <td data-value="${o.avgRssi != null ? Number(o.avgRssi) : ''}">${o.avgRssi != null ? Number(o.avgRssi).toFixed(0) + ' dBm' : '—'}</td>
              </tr>`).join('')}
            </tbody>
          </table>
        </div>` : ''}

        <div class="node-full-card" id="node-neighbors">
          <h4 id="fullNeighborsHeader">Neighbors</h4>
          <div id="fullNeighborsContent"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading neighbors…</div></div>
        </div>

        <div class="node-full-card" id="node-affinity-debug" style="display:none">
          <h4 style="cursor:pointer" onclick="this.parentElement.querySelector('.affinity-debug-body').style.display=this.parentElement.querySelector('.affinity-debug-body').style.display==='none'?'block':'none'; this.querySelector('.toggle-icon').textContent=this.parentElement.querySelector('.affinity-debug-body').style.display==='none'?'▶':'▼'"><span class="toggle-icon">▶</span> 🔍 Affinity Debug</h4>
          <div class="affinity-debug-body" style="display:none">
            <div id="affinityDebugContent"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading debug data…</div></div>
          </div>
        </div>

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
                <span class="node-activity-time">${renderNodeTimestampHtml(p.timestamp)}</span>
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
        const btn = document.getElementById('copyUrlBtn');
        window.copyToClipboard(nodeUrl, () => {
          btn.textContent = '✅ Copied!';
          setTimeout(() => btn.textContent = '📋 Copy URL', 2000);
        });
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

      // Initialize TableSort on observer table (full detail page)
      var observerTable = document.querySelector('#node-observers .observer-sort-table');
      if (observerTable && window.TableSort) {
        TableSort.init(observerTable, {
          defaultColumn: 'packets',
          defaultDirection: 'desc'
        });
      }

      // Fetch neighbors for this node (full-screen view)
      fetchAndRenderNeighbors(n.public_key, 'fullNeighborsContent', {
        headerSelector: '#fullNeighborsHeader'
      });

      // Affinity debug panel — show if debugAffinity is enabled
      (function loadAffinityDebug() {
        var show = (window.CLIENT_CONFIG && window.CLIENT_CONFIG.debugAffinity) || localStorage.getItem('meshcore-affinity-debug') === 'true';
        var panel = document.getElementById('node-affinity-debug');
        if (!show || !panel) return;
        panel.style.display = '';
        var apiKey = localStorage.getItem('meshcore-api-key') || '';
        fetch('/api/debug/affinity?node=' + encodeURIComponent(n.public_key), { headers: { 'X-API-Key': apiKey } })
          .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
          .then(function (data) {
            var el = document.getElementById('affinityDebugContent');
            if (!el) return;
            var html = '';

            // Edges table
            if (data.edges && data.edges.length) {
              html += '<h5 style="margin:8px 0 4px">Neighbor Edges (' + data.edges.length + ')</h5>';
              html += '<table class="mini-table" style="width:100%;font-size:12px"><thead><tr><th>Neighbor</th><th>Score</th><th>Count</th><th>Last Seen</th><th>Observers</th><th>Status</th></tr></thead><tbody>';
              data.edges.forEach(function (e) {
                var neighbor = e.nodeBName || e.nodeAName || (e.nodeB || e.nodeA || '').substring(0, 8);
                if (e.nodeA.toLowerCase() === n.public_key.toLowerCase()) {
                  neighbor = e.nodeBName || (e.nodeB || e.prefix || '?').substring(0, 8);
                } else {
                  neighbor = e.nodeAName || (e.nodeA || '').substring(0, 8);
                }
                var status = e.ambiguous ? (e.unresolved ? '❓ Unresolved' : '⚠️ Ambiguous') : (e.resolved ? '✅ Auto-resolved' : '✅ Resolved');
                html += '<tr><td>' + escapeHtml(neighbor) + '</td><td>' + (e.score || 0).toFixed(3) + '</td><td>' + e.weight + '</td><td>' + (e.lastSeen || '').substring(0, 10) + '</td><td>' + (e.observers || []).length + '</td><td>' + status + '</td></tr>';
              });
              html += '</tbody></table>';
            } else {
              html += '<div class="text-muted" style="padding:8px">No affinity edges for this node</div>';
            }

            // Resolutions
            if (data.resolutions && data.resolutions.length) {
              html += '<h5 style="margin:12px 0 4px">Prefix Resolutions (' + data.resolutions.length + ')</h5>';
              data.resolutions.forEach(function (r) {
                html += '<div style="border:1px solid var(--border);border-radius:4px;padding:8px;margin-bottom:6px;font-size:12px">';
                html += '<b>Prefix: ' + escapeHtml(r.prefix) + '</b> → ';
                if (r.method === 'auto-resolved') {
                  html += '<span style="color:var(--status-green)">✅ ' + escapeHtml(r.chosenName || r.chosen || '?') + '</span>';
                  html += ' (Jaccard=' + r.chosenJaccard.toFixed(2) + ', ratio=' + ((isFinite(r.ratio) && r.ratio < 100) ? r.ratio.toFixed(1) + '×' : '∞') + ')';
                } else {
                  html += '<span style="color:var(--status-yellow)">⚠️ Ambiguous</span>';
                  if (r.ratio) html += ' (ratio=' + r.ratio.toFixed(1) + '×, threshold=' + r.thresholdApplied + '×)';
                }
                // Show disambiguation tier used (M4 resolveWithContext)
                if (r.tier) {
                  var tierLabels = {
                    'neighbor_affinity': '🏘️ Affinity',
                    'geo_proximity': '🌍 Geo',
                    'gps_preference': '📍 GPS',
                    'first_match': '🎲 Naive',
                    'unique_prefix': '✓ Unique',
                    'no_match': '∅ None'
                  };
                  html += ' <span style="font-size:11px;opacity:0.8">[tier: ' + (tierLabels[r.tier] || escapeHtml(r.tier)) + ']</span>';
                }
                // Candidates table
                if (r.candidates && r.candidates.length) {
                  html += '<div style="margin-top:4px"><table class="mini-table" style="width:100%;font-size:11px"><thead><tr><th>Candidate</th><th>Jaccard</th><th>Count</th></tr></thead><tbody>';
                  r.candidates.forEach(function (c) {
                    var highlight = r.chosen && c.pubkey === r.chosen ? ' style="background:var(--status-green-bg,rgba(34,197,94,0.1))"' : '';
                    html += '<tr' + highlight + '><td>' + escapeHtml(c.name || c.pubkey.substring(0, 8)) + '</td><td>' + c.jaccard.toFixed(3) + '</td><td>' + c.score + '</td></tr>';
                  });
                  html += '</tbody></table></div>';
                }
                html += '</div>';
              });
            }

            // Stats summary
            if (data.stats) {
              html += '<h5 style="margin:12px 0 4px">Graph Stats</h5>';
              html += '<div style="font-size:12px;line-height:1.6">';
              html += 'Total edges: ' + data.stats.totalEdges + '<br>';
              html += 'Total nodes: ' + data.stats.totalNodes + '<br>';
              html += 'Resolved: ' + data.stats.resolvedCount + ' | Ambiguous: ' + data.stats.ambiguousCount + ' | Unresolved: ' + data.stats.unresolvedCount + '<br>';
              html += 'Avg confidence: ' + (data.stats.avgConfidence || 0).toFixed(3) + '<br>';
              html += 'Cold-start coverage: ' + (data.stats.coldStartCoverage || 0).toFixed(1) + '%<br>';
              html += 'Cache age: ' + (data.stats.cacheAge || 'N/A') + ' | Last rebuild: ' + (data.stats.lastRebuild || 'N/A');
              html += '</div>';
            }

            el.innerHTML = html;
          })
          .catch(function (err) {
            var el = document.getElementById('affinityDebugContent');
            if (el) el.innerHTML = '<div class="text-muted" style="padding:8px">Failed to load debug data: ' + escapeHtml(err.message) + '</div>';
          });
      })();

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
              <div style="color:var(--text-muted);margin-top:2px">${p.count}× · last ${renderNodeTimestampHtml(p.lastSeen)} · <a href="#/packets/${p.sampleHash}" class="ch-analyze-link">Analyze →</a></div>
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

  let _themeRefreshHandler = null;

  let _allNodes = null; // cached full node list

  // Build a map of lowercased name → count of distinct pubkeys sharing that name
  function buildDupNameMap(allNodes) {
    var map = {};
    (allNodes || []).forEach(function(n) {
      if (!n.name) return;
      var key = n.name.toLowerCase();
      if (!map[key]) map[key] = [];
      if (map[key].indexOf(n.public_key) === -1) map[key].push(n.public_key);
    });
    return map;
  }

  function dupNameBadge(name, pubkey, dupMap) {
    if (!name || !dupMap) return '';
    var keys = dupMap[name.toLowerCase()];
    if (!keys || keys.length <= 1) return '';
    var others = keys.filter(function(k) { return k !== pubkey; });
    var title = keys.length + ' nodes share this name (' + others.map(function(k) { return k.slice(0, 8) + '…'; }).join(', ') + ')';
    return ' <span class="dup-name-badge" title="' + escapeHtml(title) + '">(' + keys.length + ')</span>';
  }

  async function loadNodes(refreshOnly) {
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
        const ms = { '1h': 3600000, '2h': 7200000, '6h': 21600000, '12h': 43200000, '24h': 86400000, '48h': 172800000, '3d': 259200000, '7d': 604800000, '14d': 1209600000, '30d': 2592000000 }[lastHeard];
        if (ms) filtered = filtered.filter(n => {
          const t = n.last_heard || n.last_seen;
          return t && (Date.now() - new Date(t).getTime()) < ms;
        });
      }
      // Status filter (active/stale)
      if (statusFilter === 'active' || statusFilter === 'stale') {
        filtered = filtered.filter(n => {
          const role = (n.role || 'companion').toLowerCase();
          const t = n.last_heard || n.last_seen;
          const lastMs = t ? new Date(t).getTime() : 0;
          return getNodeStatus(role, lastMs) === statusFilter;
        });
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
      if (refreshOnly) {
        renderRows();
      } else {
        renderLeft();
      }
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
          <div class="filter-group" id="nodeStatusFilter">
            <button class="btn ${statusFilter==='all'?'active':''}" data-status="all">All</button>
            <button class="btn ${statusFilter==='active'?'active':''}" data-status="active">Active</button>
            <button class="btn ${statusFilter==='stale'?'active':''}" data-status="stale">Stale</button>
          </div>
          <select id="nodeLastHeard" aria-label="Filter by last heard time">
            <option value="">Last Heard: Any</option>
            <option value="1h" ${lastHeard==='1h'?'selected':''}>1 hour</option>
            <option value="2h" ${lastHeard==='2h'?'selected':''}>2 hours</option>
            <option value="6h" ${lastHeard==='6h'?'selected':''}>6 hours</option>
            <option value="12h" ${lastHeard==='12h'?'selected':''}>12 hours</option>
            <option value="24h" ${lastHeard==='24h'?'selected':''}>24 hours</option>
            <option value="48h" ${lastHeard==='48h'?'selected':''}>48 hours</option>
            <option value="3d" ${lastHeard==='3d'?'selected':''}>3 days</option>
            <option value="7d" ${lastHeard==='7d'?'selected':''}>7 days</option>
            <option value="14d" ${lastHeard==='14d'?'selected':''}>14 days</option>
            <option value="30d" ${lastHeard==='30d'?'selected':''}>30 days</option>
          </select>
        </div>
      </div>
      <table class="data-table" id="nodesTable">
        <thead><tr>
          <th scope="col" data-sort="name">Name</th>
          <th scope="col" class="col-pubkey" data-sort="public_key">Public Key</th>
          <th scope="col" data-sort="role">Role</th>
          <th scope="col" data-sort="last_seen" data-sort-default="desc">Last Seen</th>
          <th scope="col" data-sort="advert_count" data-sort-default="desc">Adverts</th>
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
    document.getElementById('nodeLastHeard').addEventListener('change', e => { lastHeard = e.target.value; localStorage.setItem('meshcore-nodes-last-heard', lastHeard); loadNodes(); });

    // Status filter buttons
    document.querySelectorAll('#nodeStatusFilter .btn').forEach(btn => {
      btn.addEventListener('click', () => {
        statusFilter = btn.dataset.status;
        localStorage.setItem('meshcore-nodes-status-filter', statusFilter);
        document.querySelectorAll('#nodeStatusFilter .btn').forEach(b => b.classList.toggle('active', b.dataset.status === statusFilter));
        loadNodes();
      });
    });

    // Initialize TableSort on nodes table (handles header clicks, indicators, persistence)
    // We use onSort callback to re-render rows (sorting is done at JS-array level in renderRows
    // because of claimed/favorites pinning logic that TableSort can't handle)
    var nodesTableEl = document.getElementById('nodesTable');
    if (nodesTableEl && window.TableSort) {
      _nodesTableSortCtrl = TableSort.init(nodesTableEl, {
        defaultColumn: 'last_seen',
        defaultDirection: 'desc',
        storageKey: 'meshcore-nodes-sort',
        onSort: function () { renderRows(); }
      });
    }

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

    // #630: Close button for node detail panel (important for mobile full-screen overlay)
    document.getElementById('nodesRight').addEventListener('click', function(e) {
      if (e.target.closest('.panel-close-btn')) {
        const panel = document.getElementById('nodesRight');
        panel.classList.add('empty');
        panel.innerHTML = '<span>Select a node to view details</span>';
        selectedKey = null;
        renderRows();
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

    const dupMap = buildDupNameMap(_allNodes);
    tbody.innerHTML = sorted.map(n => {
      const roleColor = ROLE_COLORS[n.role] || '#6b7280';
      const isClaimed = myKeys.has(n.public_key);
      const lastSeenTime = n.last_heard || n.last_seen;
      const status = getNodeStatus(n.role || 'companion', lastSeenTime ? new Date(lastSeenTime).getTime() : 0);
      const lastSeenClass = status === 'active' ? 'last-seen-active' : 'last-seen-stale';
      return `<tr data-key="${n.public_key}" data-action="select" data-value="${n.public_key}" tabindex="0" role="row" class="${selectedKey === n.public_key ? 'selected' : ''}${isClaimed ? ' claimed-row' : ''}">
        <td>${favStar(n.public_key, 'node-fav')}${isClaimed ? '<span class="claimed-badge" title="My Mesh">★</span> ' : ''}<strong>${n.name || '(unnamed)'}</strong>${dupNameBadge(n.name, n.public_key, dupMap)}</td>
        <td class="mono col-pubkey">${truncate(n.public_key, 16)}</td>
        <td><span class="badge" style="background:${roleColor}20;color:${roleColor}">${n.role}</span></td>
        <td class="${lastSeenClass}">${renderNodeTimestampHtml(n.last_heard || n.last_seen)}</td>
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
      const data = await fetchNodeDetail(pubkey);
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
    const hasLoc = n.lat != null && n.lon != null;
    const nodeUrl = location.origin + '#/nodes/' + encodeURIComponent(n.public_key);

    // Status calculation via shared helper
    const lastHeard = stats.lastHeard;
    n._lastHeard = lastHeard || n.last_seen;
    const si = getStatusInfo(n);
    const roleColor = si.roleColor;
    const totalPackets = stats.totalTransmissions || stats.totalPackets || n.advert_count || 0;

    const dupMap = buildDupNameMap(_allNodes);
    const dupBadge = dupNameBadge(n.name, n.public_key, dupMap);

    panel.innerHTML = `
      <button class="panel-close-btn" title="Close detail pane (Esc)">✕</button>
      <div class="node-detail">
        <div class="node-detail-name">${escapeHtml(n.name || '(unnamed)')}${dupBadge}</div>
        <div class="node-detail-role">${renderNodeBadges(n, roleColor)}
          <a href="#/nodes/${encodeURIComponent(n.public_key)}" class="btn-primary" style="display:inline-block;text-decoration:none;font-size:11px;padding:2px 8px;margin-left:8px">🔍 Details</a>
          <a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="btn-primary" style="display:inline-block;margin-left:4px;text-decoration:none;font-size:11px;padding:2px 8px">📊 Analytics</a>
        </div>
        ${renderStatusExplanation(n)}

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
            <dt>Last Heard</dt><dd>${renderNodeTimestampHtml(lastHeard || n.last_seen)}</dd>
            <dt>First Seen</dt><dd>${renderNodeTimestampHtml(n.first_seen)}</dd>
            <dt>Total Packets</dt><dd>${totalPackets}</dd>
            <dt>Packets Today</dt><dd>${stats.packetsToday || 0}</dd>
            ${stats.avgSnr != null ? `<dt>Avg SNR</dt><dd>${Number(stats.avgSnr).toFixed(1)} dB</dd>` : ''}
            ${stats.avgHops ? `<dt>Avg Hops</dt><dd>${stats.avgHops}</dd>` : ''}
            ${hasLoc ? `<dt>Location</dt><dd>${Number(n.lat).toFixed(5)}, ${Number(n.lon).toFixed(5)}</dd>` : ''}
          </dl>
        </div>

        ${observers.length ? `<div class="node-detail-section">
          ${(() => { const regions = [...new Set(observers.map(o => o.iata).filter(Boolean))]; return regions.length ? `<div style="margin-bottom:6px;font-size:12px"><strong>Regions:</strong> ${regions.join(', ')}</div>` : ''; })()}
          <h4>Heard By (${observers.length} observer${observers.length > 1 ? 's' : ''})</h4>
          <div class="observer-list">
            ${observers.map(o => `<div class="observer-row" style="display:flex;justify-content:space-between;align-items:center;padding:4px 0;border-bottom:1px solid var(--border);font-size:12px">
              <span style="font-weight:600">${escapeHtml(o.observer_name || o.observer_id)}${o.iata ? ' <span class="badge" style="font-size:10px">' + escapeHtml(o.iata) + '</span>' : ''}</span>
              <span style="color:var(--text-muted)">${o.packetCount} pkts · ${o.avgSnr != null ? 'SNR ' + Number(o.avgSnr).toFixed(1) + 'dB' : ''}${o.avgRssi != null ? ' · RSSI ' + Number(o.avgRssi).toFixed(0) : ''}</span>
            </div>`).join('')}
          </div>
        </div>` : ''}

        <div class="node-detail-section" id="panelNeighborsSection">
          <h4 id="panelNeighborsHeader">Neighbors</h4>
          <div id="panelNeighborsContent"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading neighbors…</div></div>
        </div>

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
                  <strong>${renderNodeTimestampHtml(a.timestamp)}</strong> ${icon} ${pType}${detail}
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

    // Fetch neighbors for this node (condensed panel — top 5)
    fetchAndRenderNeighbors(n.public_key, 'panelNeighborsContent', {
      limit: 5,
      headerSelector: '#panelNeighborsHeader',
      viewAllPubkey: n.public_key
    });

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
            <div style="color:var(--text-muted);margin-top:2px">${p.count}× · last ${renderNodeTimestampHtml(p.lastSeen)} · <a href="#/packets/${p.sampleHash}" class="ch-analyze-link">Analyze →</a></div>
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

  function isAdvertMessage(m) {
    if (m.type !== 'packet') return false;
    if (m.data && m.data.packet && m.data.packet.payload_type === 4) return true;
    if (m.data && m.data.decoded && m.data.decoded.header && m.data.decoded.header.payloadTypeName === 'ADVERT') return true;
    return false;
  }

  registerPage('nodes', {
    init: function(app, routeParam) {
      _themeRefreshHandler = () => {
        if (directNode) loadFullNode(directNode);
        else {
          renderRows();
          if (selectedKey) selectNode(selectedKey);
        }
      };
      window.addEventListener('theme-refresh', _themeRefreshHandler);
      return init(app, routeParam);
    },
    destroy: function() {
      if (_themeRefreshHandler) { window.removeEventListener('theme-refresh', _themeRefreshHandler); _themeRefreshHandler = null; }
      return destroy();
    }
  });

  // Test hooks
  window._nodesIsAdvertMessage = isAdvertMessage;
  window._nodesGetAllNodes = function() { return _allNodes; };
  window._nodesSetAllNodes = function(n) { _allNodes = n; };
  window._nodesToggleSort = function(col) {
    if (_nodesTableSortCtrl) { _nodesTableSortCtrl.sort(col); return; }
    // Fallback for tests without DOM
    var st = _getSortState();
    var descDefault = ['last_seen', 'advert_count'];
    if (st.column === col) {
      _fallbackSortState = { column: col, direction: st.direction === 'asc' ? 'desc' : 'asc' };
    } else {
      _fallbackSortState = { column: col, direction: descDefault.indexOf(col) >= 0 ? 'desc' : 'asc' };
    }
    localStorage.setItem('meshcore-nodes-sort', JSON.stringify(_fallbackSortState));
  };
  window._nodesSortNodes = sortNodes;
  window._nodesSortArrow = function(col) {
    var st = _getSortState();
    if (st.column !== col) return '';
    return '<span class="sort-arrow">' + (st.direction === 'asc' ? '▲' : '▼') + '</span>';
  };
  window._nodesGetSortState = _getSortState;
  window._nodesSetSortState = function(s) {
    _fallbackSortState = s;
    if (_nodesTableSortCtrl) _nodesTableSortCtrl.sort(s.column, s.direction);
  };
  window._nodesSyncClaimedToFavorites = syncClaimedToFavorites;
  window._nodesRenderNodeTimestampHtml = renderNodeTimestampHtml;
  window._nodesRenderNodeTimestampText = renderNodeTimestampText;
  window._nodesGetStatusInfo = getStatusInfo;
  window._nodesGetStatusTooltip = getStatusTooltip;
})();
