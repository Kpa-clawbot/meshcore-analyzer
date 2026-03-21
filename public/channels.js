/* === MeshCore Analyzer — channels.js === */
'use strict';

(function () {
  let channels = [];
  let selectedHash = null;
  let messages = [];
  let wsHandler = null;
  let autoScroll = true;
  let nodeCache = {};
  let selectedNode = null;
  var _nodeCacheTTL = 5 * 60 * 1000; // 5 minutes

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
    const lastSeen = node.last_seen ? timeAgo(node.last_seen) : 'unknown';
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
    const node = await lookupNode(name);
    selectedNode = name;

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
      const lastSeen = n.last_seen ? timeAgo(n.last_seen) : 'unknown';

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

  function highlightMentions(text) {
    if (!text) return '';
    return escapeHtml(text).replace(/@\[([^\]]+)\]/g, function(_, name) {
      const safeId = btoa(encodeURIComponent(name));
      return '<span class="ch-mention ch-sender-link" tabindex="0" role="button" data-node="' + safeId + '">@' + name + '</span>';
    });
  }

  let regionChangeHandler = null;

  function init(app, routeParam) {
    app.innerHTML = `<div class="ch-layout">
      <div class="ch-sidebar" aria-label="Channel list">
        <div class="ch-sidebar-header">
          <div class="ch-sidebar-title"><span class="ch-icon">💬</span> Channels</div>
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
    regionChangeHandler = RegionFilter.onChange(function () { loadChannels(); });

    loadChannels().then(() => {
      if (routeParam) selectChannel(routeParam);
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

    wsHandler = debouncedOnWS(function (msgs) {
      var dominated = msgs.filter(function (m) {
        return m.type === 'message' || (m.type === 'packet' && m.data?.decoded?.header?.payloadTypeName === 'GRP_TXT');
      });
      if (!dominated.length) return;

      var channelListDirty = false;
      var messagesDirty = false;
      var seenHashes = new Set();

      for (var i = 0; i < dominated.length; i++) {
        var m = dominated[i];
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

        var ts = m.data?.packet?.timestamp || payload.sender_timestamp || new Date().toISOString();
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
          ch.lastActivity = ts;
          ch.lastSender = sender;
          ch.lastMessage = truncate(displayText, 100);
          channelListDirty = true;
        } else if (isFirstObservation) {
          // New channel we haven't seen
          channels.push({
            hash: channelName,
            name: channelName,
            messageCount: 1,
            lastActivity: ts,
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
        channels.sort(function (a, b) { return (b.lastActivity || '').localeCompare(a.lastActivity || ''); });
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
    });
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
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
      channels = (data.channels || []).sort((a, b) => (b.lastActivity || '').localeCompare(a.lastActivity || ''));
      renderChannelList();
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
      const name = ch.name || `Channel ${ch.hash}`;
      const color = getChannelColor(ch.hash);
      const time = ch.lastActivity ? timeAgo(ch.lastActivity) : '';
      const preview = ch.lastSender && ch.lastMessage
        ? `${ch.lastSender}: ${truncate(ch.lastMessage, 28)}`
        : `${ch.messageCount} messages`;
      const sel = selectedHash === ch.hash ? ' selected' : '';
      const abbr = name.startsWith('#') ? name.slice(0, 3) : name.slice(0, 2).toUpperCase();

      return `<button class="ch-item${sel}" data-hash="${ch.hash}" type="button" role="option" aria-selected="${selectedHash === ch.hash ? 'true' : 'false'}" aria-label="${escapeHtml(name)}">
        <div class="ch-badge" style="background:${color}" aria-hidden="true">${escapeHtml(abbr)}</div>
        <div class="ch-item-body">
          <div class="ch-item-top">
            <span class="ch-item-name">${escapeHtml(name)}</span>
            <span class="ch-item-time">${time}</span>
          </div>
          <div class="ch-item-preview">${escapeHtml(preview)}</div>
        </div>
      </button>`;
    }).join('');
  }

  async function selectChannel(hash) {
    selectedHash = hash;
    history.replaceState(null, '', `#/channels/${encodeURIComponent(hash)}`);
    renderChannelList();
    const ch = channels.find(c => c.hash === hash);
    const name = ch?.name || `Channel ${hash}`;
    const header = document.getElementById('chHeader');
    header.querySelector('.ch-header-text').textContent = `${name} — ${ch?.messageCount || 0} messages`;

    // On mobile, show the message view
    document.querySelector('.ch-layout')?.classList.add('ch-show-main');

    const msgEl = document.getElementById('chMessages');
    msgEl.innerHTML = '<div class="ch-loading">Loading messages…</div>';

    try {
      const data = await api(`/channels/${encodeURIComponent(hash)}/messages?limit=200`, { ttl: CLIENT_TTL.channelMessages });
      messages = data.messages || [];
      renderMessages();
      scrollToBottom();
    } catch (e) {
      msgEl.innerHTML = `<div class="ch-empty">Failed to load messages: ${e.message}</div>`;
    }
  }

  async function refreshMessages() {
    if (!selectedHash) return;
    const msgEl = document.getElementById('chMessages');
    if (!msgEl) return;
    const wasAtBottom = msgEl.scrollHeight - msgEl.scrollTop - msgEl.clientHeight < 60;
    try {
      const data = await api(`/channels/${encodeURIComponent(selectedHash)}/messages?limit=200`, { ttl: CLIENT_TTL.channelMessages });
      const newMsgs = data.messages || [];
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

  registerPage('channels', { init, destroy });
})();
