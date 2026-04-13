/* === CoreScope — channels.js === */
'use strict';

(function () {
  let channels = [];
  let selectedHash = null;
  let messages = [];
  let wsHandler = null;
  let autoScroll = true;
  let nodeCache = {};
  let selectedNode = null;
  let observerIataById = {};
  let observerIataByName = {};
  let messageRequestId = 0;
  var _nodeCacheTTL = 5 * 60 * 1000; // 5 minutes

  function getSelectedRegionsSnapshot() {
    var rp = RegionFilter.getRegionParam();
    return rp ? rp.split(',').filter(Boolean) : null;
  }

  function normalizeObserverNameKey(name) {
    if (!name) return '';
    return String(name).trim().toLowerCase();
  }

  function shouldProcessWSMessageForRegion(msg, selectedRegions, observerRegionsById, observerRegionsByName) {
    if (!selectedRegions || !selectedRegions.length) return true;
    if (observerRegionsById && observerRegionsById.byId) {
      observerRegionsByName = observerRegionsById.byName || {};
      observerRegionsById = observerRegionsById.byId || {};
    }
    observerRegionsById = observerRegionsById || {};
    observerRegionsByName = observerRegionsByName || {};

    var observerId = msg?.data?.packet?.observer_id || msg?.data?.observer_id || null;
    var observerRegion = observerId ? observerRegionsById[observerId] : null;
    if (!observerRegion) {
      var observerName = msg?.data?.packet?.observer_name || msg?.data?.observer_name || msg?.data?.observer || null;
      var observerNameKey = normalizeObserverNameKey(observerName);
      if (observerName) observerRegion = observerRegionsByName[observerName];
      if (!observerRegion && observerNameKey) observerRegion = observerRegionsByName[observerNameKey];
    }
    if (!observerRegion) return false;
    return selectedRegions.indexOf(observerRegion) !== -1;
  }

  async function loadObserverRegions() {
    try {
      var data = await api('/observers', { ttl: CLIENT_TTL.observers });
      var list = data && data.observers ? data.observers : [];
      var byId = {};
      var byName = {};
      for (var i = 0; i < list.length; i++) {
        var o = list[i];
        var id = o.id || o.observer_id;
        var name = o.name || o.observer_name;
        if (!o.iata) continue;
        if (id) byId[id] = o.iata;
        if (name) {
          byName[name] = o.iata;
          var key = normalizeObserverNameKey(name);
          if (key) byName[key] = o.iata;
        }
      }
      observerIataById = byId;
      observerIataByName = byName;
    } catch {}
  }

  function beginMessageRequest(hash, regionParam) {
    return { id: ++messageRequestId, hash: hash, regionParam: regionParam || '' };
  }

  function isStaleMessageRequest(req) {
    if (!req) return true;
    var currentRegion = RegionFilter.getRegionParam() || '';
    if (req.id !== messageRequestId) return true;
    if (selectedHash !== req.hash) return true;
    if (currentRegion !== req.regionParam) return true;
    return false;
  }

  function reconcileSelectionAfterChannelRefresh() {
    if (!selectedHash || channels.some(ch => ch.hash === selectedHash)) return false;
    selectedHash = null;
    messages = [];
    history.replaceState(null, '', '#/channels');
    renderChannelList();
    const header = document.getElementById('chHeader');
    if (header) header.querySelector('.ch-header-text').textContent = 'Select a channel';
    const msgEl = document.getElementById('chMessages');
    if (msgEl) msgEl.innerHTML = '<div class="ch-empty">Choose a channel from the sidebar to view messages</div>';
    document.querySelector('.ch-layout')?.classList.remove('ch-show-main');
    document.getElementById('chScrollBtn')?.classList.add('hidden');
    return true;
  }

  async function lookupNode(name) {
    var cached = nodeCache[name];
    if (cached !== undefined) {
      if (cached && cached.fetchedAt && (Date.now() - cached.fetchedAt < _nodeCacheTTL)) return cached.data;
      if (cached && !cached.fetchedAt) return cached; // legacy null entries
    }
    try {
      const data = await api('/nodes/search?q=' + encodeURIComponent(name), { ttl: CLIENT_TTL.channelMessages });
      // Try exact match first, then case-insensitive, then contains
      const nodes = data.nodes || [];
      const match = nodes.find(n => n.name === name)
        || nodes.find(n => n.name && n.name.toLowerCase() === name.toLowerCase())
        || nodes.find(n => n.name && n.name.toLowerCase().includes(name.toLowerCase()))
        || nodes[0] || null;
      nodeCache[name] = { data: match, fetchedAt: Date.now() };
      return match;
    } catch { nodeCache[name] = null; return null; }
  }

  async function showNodeTooltip(e, name) {
    const node = await lookupNode(name);
    let existing = document.getElementById('chNodeTooltip');
    if (existing) existing.remove();
    if (!node) return;

    const tip = document.createElement('div');
    tip.id = 'chNodeTooltip';
    tip.className = 'ch-node-tooltip';
    tip.setAttribute('role', 'tooltip');
    const roleKey = node.role || (node.is_repeater ? 'repeater' : node.is_room ? 'room' : node.is_sensor ? 'sensor' : 'companion');
    const role = (ROLE_EMOJI[roleKey] || '●') + ' ' + (ROLE_LABELS[roleKey] || roleKey);
    const lastActivity = node.last_heard || node.last_seen;
    const lastSeen = lastActivity ? timeAgo(lastActivity) : 'unknown';
    tip.innerHTML = `<div class="ch-tooltip-name">${escapeHtml(node.name)}</div>
      <div class="ch-tooltip-role">${role}</div>
      <div class="ch-tooltip-meta">Last seen: ${lastSeen}</div>
      <div class="ch-tooltip-key mono">${(node.public_key || '').slice(0, 16)}…</div>`;
    document.body.appendChild(tip);
    var trigger = e.target.closest('[data-node]') || e.target;
    trigger.setAttribute('aria-describedby', 'chNodeTooltip');
    const rect = trigger.getBoundingClientRect();
    tip.style.left = Math.min(rect.left, window.innerWidth - 220) + 'px';
    tip.style.top = (rect.bottom + 4) + 'px';
  }

  function hideNodeTooltip() {
    var trigger = document.querySelector('[aria-describedby="chNodeTooltip"]');
    if (trigger) trigger.removeAttribute('aria-describedby');
    const tip = document.getElementById('chNodeTooltip');
    if (tip) tip.remove();
  }

  let _focusTrapCleanup = null;
  let _nodePanelTrigger = null;

  function trapFocus(container) {
    function handler(e) {
      if (e.key === 'Escape') { closeNodeDetail(); return; }
      if (e.key !== 'Tab') return;
      const focusable = container.querySelectorAll('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
      if (!focusable.length) return;
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (e.shiftKey) {
        if (document.activeElement === first) { e.preventDefault(); last.focus(); }
      } else {
        if (document.activeElement === last) { e.preventDefault(); first.focus(); }
      }
    }
    container.addEventListener('keydown', handler);
    return function () { container.removeEventListener('keydown', handler); };
  }

  async function showNodeDetail(name) {
    _nodePanelTrigger = document.activeElement;
    if (_focusTrapCleanup) { _focusTrapCleanup(); _focusTrapCleanup = null; }
    var _capturedHash = selectedHash;
    const node = await lookupNode(name);
    selectedNode = name;
    var _chBase = _capturedHash ? '#/channels/' + encodeURIComponent(_capturedHash) : '#/channels';
    history.replaceState(null, '', _chBase + '?node=' + encodeURIComponent(name));

    let panel = document.getElementById('chNodePanel');
    if (!panel) {
      panel = document.createElement('div');
      panel.id = 'chNodePanel';
      panel.className = 'ch-node-panel';
      document.querySelector('.ch-main').appendChild(panel);
    }
    panel.classList.add('open');

    if (!node) {
      panel.innerHTML = `<div class="ch-node-panel-header">
          <strong>${escapeHtml(name)}</strong>
          <button class="ch-node-close" data-action="ch-close-node" aria-label="Close">✕</button>
        </div>
        <div class="ch-node-panel-body">
          <div class="ch-node-field" style="color:var(--text-muted)">No node record found — this sender has only been seen in channel messages, not via adverts.</div>
        </div>`;
      _focusTrapCleanup = trapFocus(panel);
      panel.querySelector('.ch-node-close')?.focus();
      return;
    }

    try {
      const detail = await api('/nodes/' + encodeURIComponent(node.public_key), { ttl: CLIENT_TTL.nodeDetail });
      const n = detail.node;
      const adverts = detail.recentAdverts || [];
      const roleKey = n.role || (n.is_repeater ? 'repeater' : n.is_room ? 'room' : n.is_sensor ? 'sensor' : 'companion');
      const role = (ROLE_EMOJI[roleKey] || '●') + ' ' + (ROLE_LABELS[roleKey] || roleKey);
      const lastActivity = n.last_heard || n.last_seen;
      const lastSeen = lastActivity ? timeAgo(lastActivity) : 'unknown';

      panel.innerHTML = `<div class="ch-node-panel-header">
          <strong>${escapeHtml(n.name || 'Unknown')}</strong>
          <button class="ch-node-close" data-action="ch-close-node" aria-label="Close">✕</button>
        </div>
        <div class="ch-node-panel-body">
          <div class="ch-node-field"><span class="ch-node-label">Role</span> ${role}</div>
          <div class="ch-node-field"><span class="ch-node-label">Last Seen</span> ${lastSeen}</div>
          <div class="ch-node-field"><span class="ch-node-label">Adverts</span> ${n.advert_count || 0}</div>
          ${n.lat && n.lon ? `<div class="ch-node-field"><span class="ch-node-label">Location</span> ${Number(n.lat).toFixed(4)}, ${Number(n.lon).toFixed(4)}</div>` : ''}
          <div class="ch-node-field mono" style="font-size:11px;word-break:break-all"><span class="ch-node-label">Key</span> ${n.public_key}</div>
          ${adverts.length ? `<div class="ch-node-adverts"><span class="ch-node-label">Recent Adverts</span>
            ${adverts.slice(0, 5).map(a => `<div class="ch-node-advert">${timeAgo(a.timestamp)} · SNR ${a.snr != null ? a.snr + 'dB' : '?'}</div>`).join('')}
          </div>` : ''}
          <a href="#/nodes/${n.public_key}" class="ch-node-link">View full node detail →</a>
        </div>`;
      _focusTrapCleanup = trapFocus(panel);
      panel.querySelector('.ch-node-close')?.focus();
    } catch (e) {
      panel.innerHTML = `<div class="ch-node-panel-header"><strong>${escapeHtml(name)}</strong><button class="ch-node-close" data-action="ch-close-node">✕</button></div><div class="ch-node-panel-body ch-empty">Failed to load</div>`;
      _focusTrapCleanup = trapFocus(panel);
      panel.querySelector('.ch-node-close')?.focus();
    }
  }

  function closeNodeDetail() {
    if (_focusTrapCleanup) { _focusTrapCleanup(); _focusTrapCleanup = null; }
    const panel = document.getElementById('chNodePanel');
    if (panel) panel.classList.remove('open');
    selectedNode = null;
    var _chRestoreUrl = selectedHash ? '#/channels/' + encodeURIComponent(selectedHash) : '#/channels';
    history.replaceState(null, '', _chRestoreUrl);
    if (_nodePanelTrigger && typeof _nodePanelTrigger.focus === 'function') {
      _nodePanelTrigger.focus();
      _nodePanelTrigger = null;
    }
  }

  function chBack() {
    closeNodeDetail();
    var layout = document.querySelector('.ch-layout');
    if (layout) layout.classList.remove('ch-show-main');
    var sidebar = document.querySelector('.ch-sidebar');
    if (sidebar) sidebar.style.pointerEvents = '';
  }

  // WCAG AA compliant colors — ≥4.5:1 contrast on both white and dark backgrounds
  // Channel badge colors (white text on colored background)
  const CHANNEL_COLORS = [
    '#1d4ed8', '#b91c1c', '#15803d', '#b45309', '#7e22ce',
    '#0e7490', '#a16207', '#0f766e', '#be185d', '#1e40af',
  ];
  // Sender name colors — must be readable on --card-bg (light: ~#fff, dark: ~#1e293b)
  // Using CSS vars via inline style would be ideal, but these are reasonable middle-ground
  // Light mode bg ~white: need dark enough. Dark mode bg ~#1e293b: need light enough.
  // Solution: use medium-bright saturated colors that work on both.
  const SENDER_COLORS_LIGHT = [
    '#16a34a', '#2563eb', '#db2777', '#ca8a04', '#7c3aed',
    '#0d9488', '#ea580c', '#c026d3', '#0284c7', '#dc2626',
    '#059669', '#4f46e5', '#e11d48', '#d97706', '#9333ea',
  ];
  const SENDER_COLORS_DARK = [
    '#4ade80', '#60a5fa', '#f472b6', '#facc15', '#a78bfa',
    '#2dd4bf', '#fb923c', '#e879f9', '#38bdf8', '#f87171',
    '#34d399', '#818cf8', '#fb7185', '#fbbf24', '#c084fc',
  ];

  function hashCode(str) {
    let h = 0;
    for (let i = 0; i < str.length; i++) h = ((h << 5) - h + str.charCodeAt(i)) | 0;
    return Math.abs(h);
  }
  function formatHashHex(hash) {
    return typeof hash === 'number' ? '0x' + hash.toString(16).toUpperCase().padStart(2, '0') : hash;
  }
  function getChannelColor(hash) { return CHANNEL_COLORS[hashCode(String(hash)) % CHANNEL_COLORS.length]; }
  function getSenderColor(name) {
    const isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (!document.documentElement.getAttribute('data-theme') && window.matchMedia('(prefers-color-scheme: dark)').matches);
    const palette = isDark ? SENDER_COLORS_DARK : SENDER_COLORS_LIGHT;
    return palette[hashCode(String(name)) % palette.length];
  }

  function escapeHtml(str) {
    if (!str) return '';
    return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  function truncate(str, len) {
    if (!str) return '';
    return str.length > len ? str.slice(0, len) + '…' : str;
  }

  function formatSecondsAgo(sec) {
    if (sec < 0) sec = 0;
    if (sec < 60) return sec + 's ago';
    if (sec < 3600) return Math.floor(sec / 60) + 'm ago';
    if (sec < 86400) return Math.floor(sec / 3600) + 'h ago';
    return Math.floor(sec / 86400) + 'd ago';
  }

  function highlightMentions(text) {
    if (!text) return '';
    return escapeHtml(text).replace(/@\[([^\]]+)\]/g, function(_, name) {
      const safeId = btoa(encodeURIComponent(name));
      return '<span class="ch-mention ch-sender-link" tabindex="0" role="button" data-node="' + safeId + '">@' + name + '</span>';
    });
  }

  let regionChangeHandler = null;

  // --- Client-side channel decryption (#725 M2) ---

  // Check if input is a valid hex string (32 hex chars = 16 bytes)
  function isHexKey(val) {
    return /^[0-9a-fA-F]{32}$/.test(val);
  }

  // Add a user channel by name (#channelname) or hex key
  async function addUserChannel(val) {
    var channelName, keyHex;
    if (val.startsWith('#')) {
      channelName = val;
      var keyBytes = await ChannelDecrypt.deriveKey(channelName);
      keyHex = ChannelDecrypt.bytesToHex(keyBytes);
    } else if (isHexKey(val)) {
      keyHex = val.toLowerCase();
      channelName = 'psk:' + keyHex.substring(0, 8);
    } else {
      // Try with # prefix if user forgot
      channelName = '#' + val;
      var keyBytes2 = await ChannelDecrypt.deriveKey(channelName);
      keyHex = ChannelDecrypt.bytesToHex(keyBytes2);
    }

    ChannelDecrypt.storeKey(channelName, keyHex);

    // Compute channel hash byte to find matching encrypted channels
    var keyBytes3 = ChannelDecrypt.hexToBytes(keyHex);
    var hashByte = await ChannelDecrypt.computeChannelHash(keyBytes3);

    // Add to sidebar or merge with existing encrypted channel
    mergeUserChannels();
    renderChannelList();

    // Auto-select and start decrypting
    var targetHash = 'user:' + channelName;
    // Check if there's an existing encrypted channel with this hash byte
    var existingEncrypted = channels.find(function (ch) {
      return ch.encrypted && String(ch.hash) === String(hashByte);
    });
    if (existingEncrypted) {
      targetHash = existingEncrypted.hash;
    }
    await selectChannel(targetHash, { userKey: keyHex, channelHashByte: hashByte, channelName: channelName });
  }

  // Merge user-stored keys into the channel list
  function mergeUserChannels() {
    var keys = ChannelDecrypt.getStoredKeys();
    var names = Object.keys(keys);
    for (var i = 0; i < names.length; i++) {
      var name = names[i];
      // Check if channel already exists by name
      var exists = channels.some(function (ch) {
        return ch.name === name || ch.hash === name || ch.hash === ('user:' + name);
      });
      if (!exists) {
        channels.push({
          hash: 'user:' + name,
          name: name,
          messageCount: 0,
          lastActivityMs: 0,
          lastSender: '',
          lastMessage: 'Encrypted — click to decrypt',
          encrypted: true,
          userAdded: true
        });
      }
    }
  }

  // Fetch and decrypt GRP_TXT packets client-side
  async function fetchAndDecryptChannel(keyHex, channelHashByte, channelName) {
    var keyBytes = ChannelDecrypt.hexToBytes(keyHex);

    // Check cache first
    var cacheKey = channelName || String(channelHashByte);
    var cached = ChannelDecrypt.getCache(cacheKey);
    var cachedMsgs = cached ? cached.messages : [];
    var lastTs = cached ? cached.lastTimestamp : '';

    // Fetch packets from API — get all payload_type=5 (GRP_TXT/CHAN)
    var rp = RegionFilter.getRegionParam();
    var qs = rp ? '&region=' + encodeURIComponent(rp) : '';
    var data;
    try {
      data = await api('/packets?limit=1000&payloadType=5' + qs, { ttl: 10000 });
    } catch (e) {
      return { messages: cachedMsgs, error: 'Failed to fetch packets: ' + e.message };
    }

    var packets = data.packets || [];
    // Filter for GRP_TXT (encrypted) packets matching our channel hash byte
    var candidates = [];
    for (var i = 0; i < packets.length; i++) {
      var p = packets[i];
      var dj;
      try { dj = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json) : p.decoded_json; }
      catch (e) { continue; }
      if (!dj) continue;

      // Include both undecrypted GRP_TXT and already-decrypted CHAN with matching channel
      if (dj.type === 'CHAN' && dj.channel === channelName) {
        // Already decrypted by server — use directly
        candidates.push({
          type: 'already_decrypted',
          decoded: dj,
          packet: p
        });
      } else if (dj.type === 'GRP_TXT' && dj.encryptedData && dj.mac) {
        // Check channelHash byte match for fast rejection
        if (dj.channelHash === channelHashByte) {
          candidates.push({
            type: 'encrypted',
            decoded: dj,
            packet: p
          });
        }
      }
    }

    if (candidates.length === 0) {
      return { messages: cachedMsgs, empty: true };
    }

    // Sort newest first for progressive rendering
    candidates.sort(function (a, b) {
      var ta = a.packet.first_seen || a.packet.timestamp || '';
      var tb = b.packet.first_seen || b.packet.timestamp || '';
      return tb.localeCompare(ta);
    });

    // Decrypt in chunks, checking for wrong key
    var decrypted = [];
    var macFailCount = 0;
    var macCheckCount = 0;

    for (var j = 0; j < candidates.length; j++) {
      var c = candidates[j];

      if (c.type === 'already_decrypted') {
        var d = c.decoded;
        var sender = d.sender || 'Unknown';
        var text = d.text || '';
        // Strip "sender: " prefix if present
        var ci = text.indexOf(': ');
        if (ci > 0 && ci < 50 && text.substring(0, ci) === sender) {
          text = text.substring(ci + 2);
        }
        decrypted.push({
          sender: sender,
          text: text,
          timestamp: c.packet.first_seen || c.packet.timestamp,
          sender_timestamp: d.sender_timestamp || null,
          packetHash: c.packet.hash,
          packetId: c.packet.id,
          hops: d.path_len || 0,
          snr: c.packet.snr || null,
          observers: c.packet.observer_name ? [c.packet.observer_name] : [],
          repeats: 1
        });
        continue;
      }

      // Encrypted — try to decrypt
      macCheckCount++;
      var result = await ChannelDecrypt.decryptPacket(keyBytes, c.decoded.mac, c.decoded.encryptedData);
      if (result) {
        macFailCount = 0; // reset on success
        decrypted.push({
          sender: result.sender,
          text: result.message,
          timestamp: c.packet.first_seen || c.packet.timestamp,
          sender_timestamp: result.timestamp || null,
          packetHash: c.packet.hash,
          packetId: c.packet.id,
          hops: 0,
          snr: c.packet.snr || null,
          observers: c.packet.observer_name ? [c.packet.observer_name] : [],
          repeats: 1
        });
      } else {
        macFailCount++;
        // Wrong key detection: if first ~10 MAC checks all fail, abort early
        if (macCheckCount >= 10 && macFailCount >= macCheckCount) {
          return { messages: decrypted, wrongKey: true };
        }
      }
    }

    // Sort chronologically (oldest first) for display
    decrypted.sort(function (a, b) {
      var ta = a.timestamp || '';
      var tb = b.timestamp || '';
      return ta.localeCompare(tb);
    });

    // Cache results
    var lastTimestamp = decrypted.length ? decrypted[decrypted.length - 1].timestamp : '';
    ChannelDecrypt.setCache(cacheKey, decrypted, lastTimestamp);

    return { messages: decrypted };
  }

  function init(app, routeParam) {
    var _initUrlParams = getHashParams();
    var _pendingNode = _initUrlParams.get('node');

    app.innerHTML = `<div class="ch-layout">
      <div class="ch-sidebar" aria-label="Channel list">
        <div class="ch-sidebar-header">
          <div class="ch-sidebar-title"><span class="ch-icon">💬</span> Channels</div>
        </div>
        <div class="ch-key-input-wrap" style="padding:4px 8px">
          <form id="chKeyForm" autocomplete="off">
            <input type="text" id="chKeyInput" class="ch-key-input" placeholder="#LongFast or paste hex key" aria-label="Add channel by name or hex key" spellcheck="false">
          </form>
        </div>
        <div id="chRegionFilter" class="region-filter-container" style="padding:0 8px"></div>
        <div class="ch-channel-list" id="chList" role="listbox" aria-label="Channels">
          <div class="ch-loading">Loading channels…</div>
        </div>
        <div class="ch-sidebar-resize" aria-hidden="true"></div>
      </div>
      <div class="ch-main" role="region" aria-label="Channel messages">
        <div class="ch-main-header" id="chHeader">
          <button class="ch-back-btn" id="chBackBtn" aria-label="Back to channels" data-action="ch-back">←</button>
          <span class="ch-header-text">Select a channel</span>
        </div>
        <div class="ch-messages" id="chMessages">
          <div class="ch-empty">Choose a channel from the sidebar to view messages</div>
        </div>
        <span id="chAriaLive" class="sr-only" aria-live="polite"></span>
        <button class="ch-scroll-btn hidden" id="chScrollBtn">↓ New messages</button>
      </div>
    </div>`;

    RegionFilter.init(document.getElementById('chRegionFilter'));
    regionChangeHandler = RegionFilter.onChange(function () {
      loadChannels(true).then(async function () {
        if (!selectedHash) return;
        await refreshMessages({ regionSwitch: true, forceNoCache: true });
      });
    });

    // Channel key input handler (#725 M2)
    var chKeyForm = document.getElementById('chKeyForm');
    if (chKeyForm) {
      chKeyForm.addEventListener('submit', async function (e) {
        e.preventDefault();
        var input = document.getElementById('chKeyInput');
        var val = (input.value || '').trim();
        if (!val) return;
        input.value = '';
        await addUserChannel(val);
      });
    }

    loadObserverRegions();
    loadChannels().then(async function () {
      // Also load user-added encrypted channels into the sidebar
      mergeUserChannels();
      if (routeParam) await selectChannel(routeParam);
      if (_pendingNode && _pendingNode.length < 200) await showNodeDetail(_pendingNode);
    });

    // #89: Sidebar resize handle
    (function () {
      var sidebar = app.querySelector('.ch-sidebar');
      var handle = app.querySelector('.ch-sidebar-resize');
      var saved = localStorage.getItem('channels-sidebar-width');
      if (saved) { var w = parseInt(saved, 10); if (w >= 180 && w <= 600) { sidebar.style.width = w + 'px'; sidebar.style.minWidth = w + 'px'; } }
      var dragging = false, startX, startW;
      handle.addEventListener('mousedown', function (e) { dragging = true; startX = e.clientX; startW = sidebar.getBoundingClientRect().width; e.preventDefault(); });
      document.addEventListener('mousemove', function (e) { if (!dragging) return; var w = Math.max(180, Math.min(600, startW + e.clientX - startX)); sidebar.style.width = w + 'px'; sidebar.style.minWidth = w + 'px'; });
      document.addEventListener('mouseup', function () { if (!dragging) return; dragging = false; localStorage.setItem('channels-sidebar-width', parseInt(sidebar.style.width, 10)); });
    })();

    // #90: Theme change observer — re-render messages on theme toggle
    var _themeObserver = new MutationObserver(function (muts) {
      for (var i = 0; i < muts.length; i++) {
        if (muts[i].attributeName === 'data-theme') { if (selectedHash) renderMessages(); break; }
      }
    });
    _themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });

    // #87: Fix pointer-events during mobile slide transition
    var chMain = app.querySelector('.ch-main');
    var chSidebar = app.querySelector('.ch-sidebar');
    chMain.addEventListener('transitionend', function () {
      var layout = app.querySelector('.ch-layout');
      if (layout && layout.classList.contains('ch-show-main')) {
        chSidebar.style.pointerEvents = 'none';
      } else {
        chSidebar.style.pointerEvents = '';
      }
    });

    // Event delegation for data-action buttons
    app.addEventListener('click', function (e) {
      var btn = e.target.closest('[data-action]');
      if (!btn) return;
      var action = btn.dataset.action;
      if (action === 'ch-close-node') closeNodeDetail();
      else if (action === 'ch-back') chBack();
    });

    // Event delegation for channel selection (touch-friendly)
    document.getElementById('chList').addEventListener('click', (e) => {
      // Color dot click — open picker, don't select channel
      const dot = e.target.closest('.ch-color-dot');
      if (dot && window.ChannelColorPicker) {
        e.stopPropagation();
        var ch = dot.getAttribute('data-channel');
        if (ch) ChannelColorPicker.show(ch, e.clientX, e.clientY);
        return;
      }
      const item = e.target.closest('.ch-item[data-hash]');
      if (item) selectChannel(item.dataset.hash);
    });

    const msgEl = document.getElementById('chMessages');
    msgEl.addEventListener('scroll', () => {
      const atBottom = msgEl.scrollHeight - msgEl.scrollTop - msgEl.clientHeight < 60;
      autoScroll = atBottom;
      document.getElementById('chScrollBtn').classList.toggle('hidden', atBottom);
    });
    document.getElementById('chScrollBtn').addEventListener('click', scrollToBottom);

    // Event delegation for node clicks and hovers (click + touchend for mobile reliability)
    function handleNodeTap(e) {
      const el = e.target.closest('[data-node]');
      if (el) {
        e.preventDefault();
        const name = decodeURIComponent(atob(el.dataset.node));
        showNodeDetail(name);
      } else if (selectedNode && !e.target.closest('.ch-node-panel')) {
        closeNodeDetail();
      }
    }
    // Keyboard support for data-node elements (Bug #82)
    msgEl.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' || e.key === ' ') {
        const el = e.target.closest('[data-node]');
        if (el) {
          e.preventDefault();
          const name = decodeURIComponent(atob(el.dataset.node));
          showNodeDetail(name);
        }
      }
    });

    msgEl.addEventListener('click', handleNodeTap);
    // touchend fires more reliably on mobile for non-button elements
    let touchMoved = false;
    msgEl.addEventListener('touchstart', () => { touchMoved = false; }, { passive: true });
    msgEl.addEventListener('touchmove', () => { touchMoved = true; }, { passive: true });
    msgEl.addEventListener('touchend', (e) => {
      if (touchMoved) return;
      const el = e.target.closest('[data-node]');
      if (el) {
        e.preventDefault();
        const name = decodeURIComponent(atob(el.dataset.node));
        showNodeDetail(name);
      } else if (selectedNode && !e.target.closest('.ch-node-panel')) {
        closeNodeDetail();
      }
    });
    let hoverTimeout = null;
    msgEl.addEventListener('mouseover', (e) => {
      const el = e.target.closest('[data-node]');
      if (el) {
        clearTimeout(hoverTimeout);
        const name = decodeURIComponent(atob(el.dataset.node));
        showNodeTooltip(e, name);
      }
    });
    msgEl.addEventListener('mouseout', (e) => {
      const el = e.target.closest('[data-node]');
      if (el) {
        hoverTimeout = setTimeout(hideNodeTooltip, 100);
      }
    });
    // #86: Show tooltip on focus for keyboard users
    msgEl.addEventListener('focusin', (e) => {
      const el = e.target.closest('[data-node]');
      if (el) {
        clearTimeout(hoverTimeout);
        const name = decodeURIComponent(atob(el.dataset.node));
        showNodeTooltip(e, name);
      }
    });
    msgEl.addEventListener('focusout', (e) => {
      const el = e.target.closest('[data-node]');
      if (el) {
        hoverTimeout = setTimeout(hideNodeTooltip, 100);
      }
    });

    function processWSBatch(msgs, selectedRegions) {
      var dominated = msgs.filter(function (m) {
        return m.type === 'message' || (m.type === 'packet' && m.data?.decoded?.header?.payloadTypeName === 'GRP_TXT');
      });
      if (!dominated.length) return;

      var channelListDirty = false;
      var messagesDirty = false;
      var seenHashes = new Set();

      for (var i = 0; i < dominated.length; i++) {
        var m = dominated[i];
        if (!shouldProcessWSMessageForRegion(m, selectedRegions, observerIataById, observerIataByName)) continue;
        var payload = m.data?.decoded?.payload;
        if (!payload) continue;

        var channelName = payload.channel || 'unknown';
        var rawText = payload.text || '';
        var sender = payload.sender || null;
        var displayText = rawText;

        // Parse "sender: message" format
        if (rawText && !sender) {
          var colonIdx = rawText.indexOf(': ');
          if (colonIdx > 0 && colonIdx < 50) {
            sender = rawText.slice(0, colonIdx);
            displayText = rawText.slice(colonIdx + 2);
          }
        } else if (rawText && sender) {
          var colonIdx2 = rawText.indexOf(': ');
          if (colonIdx2 > 0 && colonIdx2 < 50) {
            displayText = rawText.slice(colonIdx2 + 2);
          }
        }
        if (!sender) sender = 'Unknown';

        var ts = new Date().toISOString();
        var pktHash = m.data?.hash || m.data?.packet?.hash || null;
        var pktId = m.data?.id || null;
        var snr = m.data?.snr ?? m.data?.packet?.snr ?? payload.SNR ?? null;
        var observer = m.data?.packet?.observer_name || m.data?.observer || null;

        // Update channel list entry — only once per unique packet hash
        var isFirstObservation = pktHash && !seenHashes.has(pktHash + ':' + channelName);
        if (pktHash) seenHashes.add(pktHash + ':' + channelName);

        var ch = channels.find(function (c) { return c.hash === channelName; });
        if (ch) {
          if (isFirstObservation) ch.messageCount = (ch.messageCount || 0) + 1;
          ch.lastActivityMs = Date.now();
          ch.lastSender = sender;
          ch.lastMessage = truncate(displayText, 100);
          channelListDirty = true;
        } else if (isFirstObservation) {
          // New channel we haven't seen
          channels.push({
            hash: channelName,
            name: channelName,
            messageCount: 1,
            lastActivityMs: Date.now(),
            lastSender: sender,
            lastMessage: truncate(displayText, 100),
          });
          channelListDirty = true;
        }

        // If this message is for the selected channel, append to messages
        if (selectedHash && channelName === selectedHash) {
          // Deduplicate by packet hash — same message seen by multiple observers
          var existing = pktHash ? messages.find(function (msg) { return msg.packetHash === pktHash; }) : null;
          if (existing) {
            existing.repeats = (existing.repeats || 1) + 1;
            if (observer && existing.observers && existing.observers.indexOf(observer) === -1) {
              existing.observers.push(observer);
            }
          } else {
            messages.push({
              sender: sender,
              text: displayText,
              timestamp: ts,
              sender_timestamp: payload.sender_timestamp || null,
              packetId: pktId,
              packetHash: pktHash,
              repeats: 1,
              observers: observer ? [observer] : [],
              hops: payload.path_len || 0,
              snr: snr,
            });
          }
          messagesDirty = true;
        }
      }

      if (channelListDirty) {
        channels.sort(function (a, b) { return (b.lastActivityMs || 0) - (a.lastActivityMs || 0); });
        renderChannelList();
      }
      if (messagesDirty) {
        renderMessages();
        // Update header count
        var ch2 = channels.find(function (c) { return c.hash === selectedHash; });
        var header = document.getElementById('chHeader');
        if (header && ch2) {
          header.querySelector('.ch-header-text').textContent = (ch2.name || 'Channel ' + selectedHash) + ' — ' + messages.length + ' messages';
        }
        var msgEl = document.getElementById('chMessages');
        if (msgEl && autoScroll) scrollToBottom();
        else {
          document.getElementById('chScrollBtn')?.classList.remove('hidden');
          var liveEl = document.getElementById('chAriaLive');
          if (liveEl) liveEl.textContent = 'New message received';
        }
      }
    }

    function handleWSBatch(msgs) {
      var selectedRegions = getSelectedRegionsSnapshot();
      processWSBatch(msgs, selectedRegions);
    }

    wsHandler = debouncedOnWS(function (msgs) {
      handleWSBatch(msgs);
    });
    window._channelsHandleWSBatchForTest = handleWSBatch;
    window._channelsProcessWSBatchForTest = processWSBatch;

    // Tick relative timestamps every 1s — iterates channels array, updates DOM text only
    timeAgoTimer = setInterval(function () {
      var now = Date.now();
      for (var i = 0; i < channels.length; i++) {
        var ch = channels[i];
        if (!ch.lastActivityMs) continue;
        var el = document.querySelector('.ch-item-time[data-channel-hash="' + ch.hash + '"]');
        if (el) el.textContent = formatSecondsAgo(Math.floor((now - ch.lastActivityMs) / 1000));
      }
    }, 1000);
  }

  var timeAgoTimer = null;

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (timeAgoTimer) clearInterval(timeAgoTimer);
    timeAgoTimer = null;
    if (regionChangeHandler) RegionFilter.offChange(regionChangeHandler);
    regionChangeHandler = null;
    channels = [];
    messages = [];
    selectedHash = null;
    selectedNode = null;
    hideNodeTooltip();
    const panel = document.getElementById('chNodePanel');
    if (panel) panel.remove();
  }

  async function loadChannels(silent) {
    try {
      const rp = RegionFilter.getRegionParam();
      const qs = rp ? '?region=' + encodeURIComponent(rp) : '';
      const data = await api('/channels' + qs, { ttl: CLIENT_TTL.channels });
      channels = (data.channels || []).map(ch => {
        ch.lastActivityMs = ch.lastActivity ? new Date(ch.lastActivity).getTime() : 0;
        return ch;
      }).sort((a, b) => (b.lastActivityMs || 0) - (a.lastActivityMs || 0));
      renderChannelList();
      reconcileSelectionAfterChannelRefresh();
    } catch (e) {
      if (!silent) {
        const el = document.getElementById('chList');
        if (el) el.innerHTML = `<div class="ch-empty">Failed to load channels</div>`;
      }
    }
  }

  function renderChannelList() {
    const el = document.getElementById('chList');
    if (!el) return;
    if (channels.length === 0) { el.innerHTML = '<div class="ch-empty">No channels found</div>'; return; }

    // Sort by message count desc
    const sorted = [...channels].sort((a, b) => {
      return (b.messageCount || 0) - (a.messageCount || 0);
    });

    el.innerHTML = sorted.map(ch => {
      const name = ch.name || `Channel ${formatHashHex(ch.hash)}`;
      const color = getChannelColor(ch.hash);
      const time = ch.lastActivityMs ? formatSecondsAgo(Math.floor((Date.now() - ch.lastActivityMs) / 1000)) : '';
      const preview = ch.lastSender && ch.lastMessage
        ? `${ch.lastSender}: ${truncate(ch.lastMessage, 28)}`
        : `${ch.messageCount} messages`;
      const sel = selectedHash === ch.hash ? ' selected' : '';
      const abbr = name.startsWith('#') ? name.slice(0, 3) : name.slice(0, 2).toUpperCase();
      // Channel color dot for color picker (#674)
      const chColor = window.ChannelColors ? window.ChannelColors.get(ch.hash) : null;
      const dotStyle = chColor ? ` style="background:${chColor}"` : '';
      // Left border for assigned color
      const borderStyle = chColor ? ` style="border-left:3px solid ${chColor}"` : '';

      return `<button class="ch-item${sel}" data-hash="${ch.hash}"${borderStyle} type="button" role="option" aria-selected="${selectedHash === ch.hash ? 'true' : 'false'}" aria-label="${escapeHtml(name)}">
        <div class="ch-badge" style="background:${color}" aria-hidden="true">${escapeHtml(abbr)}</div>
        <div class="ch-item-body">
          <div class="ch-item-top">
            <span class="ch-item-name">${escapeHtml(name)}</span>
            <span class="ch-color-dot" data-channel="${escapeHtml(ch.hash)}"${dotStyle} title="Change channel color" aria-label="Change color for ${escapeHtml(name)}"></span>
            <span class="ch-item-time" data-channel-hash="${ch.hash}">${time}</span>
          </div>
          <div class="ch-item-preview">${escapeHtml(preview)}</div>
        </div>
      </button>`;
    }).join('');
  }

  async function selectChannel(hash, decryptOpts) {
    const rp = RegionFilter.getRegionParam() || '';
    const request = beginMessageRequest(hash, rp);
    selectedHash = hash;
    history.replaceState(null, '', `#/channels/${encodeURIComponent(hash)}`);
    renderChannelList();
    const ch = channels.find(c => c.hash === hash);
    const name = ch?.name || `Channel ${formatHashHex(hash)}`;
    const header = document.getElementById('chHeader');
    header.querySelector('.ch-header-text').textContent = `${name} — ${ch?.messageCount || 0} messages`;

    // On mobile, show the message view
    document.querySelector('.ch-layout')?.classList.add('ch-show-main');

    const msgEl = document.getElementById('chMessages');

    // Shared helper: fetch, decrypt, and render messages for a channel key
    async function decryptAndRender(keyHex, channelHashByte, channelName) {
      msgEl.innerHTML = '<div class="ch-loading">Decrypting messages…</div>';
      var result = await fetchAndDecryptChannel(keyHex, channelHashByte, channelName);
      if (isStaleMessageRequest(request)) return true;
      if (result.wrongKey) {
        msgEl.innerHTML = '<div class="ch-empty ch-wrong-key">🔒 Key does not match — no messages could be decrypted</div>';
        return true;
      }
      if (result.error) {
        msgEl.innerHTML = '<div class="ch-empty">' + escapeHtml(result.error) + '</div>';
        return true;
      }
      messages = result.messages || [];
      if (messages.length === 0) {
        msgEl.innerHTML = '<div class="ch-empty">No encrypted messages found for this channel</div>';
      } else {
        header.querySelector('.ch-header-text').textContent = `${name} — ${messages.length} messages (decrypted)`;
        renderMessages();
        scrollToBottom();
      }
      return true;
    }

    // Client-side decryption path (#725 M2)
    if (decryptOpts && decryptOpts.userKey) {
      await decryptAndRender(decryptOpts.userKey, decryptOpts.channelHashByte, decryptOpts.channelName);
      return;
    }

    // Check if this is a user-added channel that needs decryption
    var storedKeys = typeof ChannelDecrypt !== 'undefined' ? ChannelDecrypt.getStoredKeys() : {};
    if (hash.startsWith('user:')) {
      var chName = hash.substring(5);
      if (storedKeys[chName]) {
        var keyHex = storedKeys[chName];
        var keyBytes = ChannelDecrypt.hexToBytes(keyHex);
        var hashByte = await ChannelDecrypt.computeChannelHash(keyBytes);
        await decryptAndRender(keyHex, hashByte, chName);
        return;
      }
    }

    // Also check if an encrypted channel hash matches a stored key
    if (ch && ch.encrypted) {
      for (var kn in storedKeys) {
        var kh = storedKeys[kn];
        var kb = ChannelDecrypt.hexToBytes(kh);
        var hb = await ChannelDecrypt.computeChannelHash(kb);
        if (String(hb) === String(hash) || String(ch.hash) === String(hb)) {
          await decryptAndRender(kh, hb, kn);
          return;
        }
      }
    }

    msgEl.innerHTML = '<div class="ch-loading">Loading messages…</div>';

    try {
      const regionQs = rp ? '&region=' + encodeURIComponent(rp) : '';
      const data = await api(`/channels/${encodeURIComponent(hash)}/messages?limit=200${regionQs}`, { ttl: CLIENT_TTL.channelMessages });
      if (isStaleMessageRequest(request)) return;
      messages = data.messages || [];
      if (messages.length === 0 && rp) {
        msgEl.innerHTML = '<div class="ch-empty">Channel not available in selected region</div>';
      } else {
        renderMessages();
        scrollToBottom();
      }
    } catch (e) {
      if (isStaleMessageRequest(request)) return;
      msgEl.innerHTML = `<div class="ch-empty">Failed to load messages: ${e.message}</div>`;
    }
  }

  async function refreshMessages(opts) {
    if (!selectedHash) return;
    opts = opts || {};
    const msgEl = document.getElementById('chMessages');
    if (!msgEl) return;
    const wasAtBottom = msgEl.scrollHeight - msgEl.scrollTop - msgEl.clientHeight < 60;
    try {
      const requestHash = selectedHash;
      const rp = RegionFilter.getRegionParam() || '';
      const request = beginMessageRequest(requestHash, rp);
      const regionQs = rp ? '&region=' + encodeURIComponent(rp) : '';
      const data = await api(`/channels/${encodeURIComponent(requestHash)}/messages?limit=200${regionQs}`, { ttl: CLIENT_TTL.channelMessages, bust: !!opts.forceNoCache });
      if (isStaleMessageRequest(request)) return;
      const newMsgs = data.messages || [];
      if (opts.regionSwitch && rp && newMsgs.length === 0) {
        messages = [];
        msgEl.innerHTML = '<div class="ch-empty">Channel not available in selected region</div>';
        document.getElementById('chScrollBtn')?.classList.add('hidden');
        return;
      }
      // #92: Use message ID/hash for change detection instead of count + timestamp
      var _getLastId = function (arr) { var m = arr.length ? arr[arr.length - 1] : null; return m ? (m.id || m.packetId || m.timestamp || '') : ''; };
      if (newMsgs.length === messages.length && _getLastId(newMsgs) === _getLastId(messages)) return;
      var prevLen = messages.length;
      messages = newMsgs;
      renderMessages();
      if (wasAtBottom) scrollToBottom();
      else {
        document.getElementById('chScrollBtn')?.classList.remove('hidden');
        var liveEl = document.getElementById('chAriaLive');
        if (liveEl) liveEl.textContent = Math.max(1, newMsgs.length - prevLen) + ' new messages';
      }
    } catch {}
  }

  function renderMessages() {
    const msgEl = document.getElementById('chMessages');
    if (!msgEl) return;
    if (messages.length === 0) { msgEl.innerHTML = '<div class="ch-empty">No messages in this channel yet</div>'; return; }

    msgEl.innerHTML = messages.map(msg => {
      const sender = msg.sender || 'Unknown';
      const senderColor = getSenderColor(sender);
      const senderLetter = sender.replace(/[^\w]/g, '').charAt(0).toUpperCase() || '?';

      let displayText;
      displayText = highlightMentions(msg.text || '');

      const time = msg.timestamp ? new Date(msg.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '';
      const date = msg.timestamp ? new Date(msg.timestamp).toLocaleDateString() : '';

      const meta = [];
      meta.push(date + ' ' + time);
      if (msg.repeats > 1) meta.push(`${msg.repeats}× heard`);
      if (msg.observers?.length > 1) meta.push(`${msg.observers.length} observers`);
      if (msg.hops > 0) meta.push(`${msg.hops} hops`);
      if (msg.snr !== null && msg.snr !== undefined) meta.push(`SNR ${msg.snr}`);

      const safeId = btoa(encodeURIComponent(sender));
      return `<div class="ch-msg">
        <div class="ch-avatar ch-tappable" style="background:${senderColor}" tabindex="0" role="button" data-node="${safeId}">${senderLetter}</div>
        <div class="ch-msg-content">
          <div class="ch-msg-sender ch-sender-link ch-tappable" style="color:${senderColor}" tabindex="0" role="button" data-node="${safeId}">${escapeHtml(sender)}</div>
          <div class="ch-msg-bubble">${displayText}</div>
          <div class="ch-msg-meta">${meta.join(' · ')}${msg.packetHash ? ` · <a href="#/packets/${msg.packetHash}" class="ch-analyze-link">View packet →</a>` : ''}</div>
        </div>
      </div>`;
    }).join('');
  }

  function scrollToBottom() {
    const msgEl = document.getElementById('chMessages');
    if (msgEl) { msgEl.scrollTop = msgEl.scrollHeight; autoScroll = true; document.getElementById('chScrollBtn')?.classList.add('hidden'); }
  }

  window._channelsSetStateForTest = function (state) {
    if (!state) return;
    if (Array.isArray(state.channels)) channels = state.channels;
    if (Array.isArray(state.messages)) messages = state.messages;
    if (Object.prototype.hasOwnProperty.call(state, 'selectedHash')) selectedHash = state.selectedHash;
  };
  window._channelsSetObserverRegionsForTest = function (byId, byName) {
    observerIataById = byId || {};
    observerIataByName = byName || {};
  };
  window._channelsSelectChannelForTest = selectChannel;
  window._channelsRefreshMessagesForTest = refreshMessages;
  window._channelsLoadChannelsForTest = loadChannels;
  window._channelsBeginMessageRequestForTest = beginMessageRequest;
  window._channelsIsStaleMessageRequestForTest = isStaleMessageRequest;
  window._channelsReconcileSelectionForTest = reconcileSelectionAfterChannelRefresh;
  window._channelsGetStateForTest = function () {
    return { channels: channels, messages: messages, selectedHash: selectedHash };
  };
  window._channelsShouldProcessWSMessageForRegion = shouldProcessWSMessageForRegion;
  registerPage('channels', { init, destroy });
})();
