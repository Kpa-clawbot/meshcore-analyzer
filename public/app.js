/* === MeshCore Analyzer — app.js === */
'use strict';

// --- Route/Payload name maps ---
const ROUTE_TYPES = { 0: 'TRANSPORT_FLOOD', 1: 'FLOOD', 2: 'DIRECT', 3: 'TRANSPORT_DIRECT' };
const PAYLOAD_TYPES = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 11: 'Control' };
const PAYLOAD_COLORS = { 0: 'req', 1: 'response', 2: 'txt-msg', 3: 'ack', 4: 'advert', 5: 'grp-txt', 7: 'anon-req', 8: 'path', 9: 'trace' };

function routeTypeName(n) { return ROUTE_TYPES[n] || 'UNKNOWN'; }
function payloadTypeName(n) { return PAYLOAD_TYPES[n] || 'UNKNOWN'; }
function payloadTypeColor(n) { return PAYLOAD_COLORS[n] || 'unknown'; }

// --- Utilities ---
async function api(path) {
  const res = await fetch('/api' + path);
  if (!res.ok) throw new Error(`API ${res.status}: ${path}`);
  return res.json();
}

function timeAgo(iso) {
  if (!iso) return '—';
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

function truncate(str, len) {
  if (!str) return '';
  return str.length > len ? str.slice(0, len) + '…' : str;
}

// --- Favorites ---
const FAV_KEY = 'meshcore-favorites';
function getFavorites() {
  try { return JSON.parse(localStorage.getItem(FAV_KEY) || '[]'); } catch { return []; }
}
function isFavorite(pubkey) { return getFavorites().includes(pubkey); }
function toggleFavorite(pubkey) {
  const favs = getFavorites();
  const idx = favs.indexOf(pubkey);
  if (idx >= 0) favs.splice(idx, 1); else favs.push(pubkey);
  localStorage.setItem(FAV_KEY, JSON.stringify(favs));
  return idx < 0; // true if now favorited
}
function favStar(pubkey, cls) {
  const on = isFavorite(pubkey);
  return '<button class="fav-star ' + (cls || '') + (on ? ' on' : '') + '" data-fav="' + pubkey + '" title="' + (on ? 'Remove from favorites' : 'Add to favorites') + '">' + (on ? '★' : '☆') + '</button>';
}
function bindFavStars(container, onToggle) {
  container.querySelectorAll('.fav-star').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const pk = btn.dataset.fav;
      const nowOn = toggleFavorite(pk);
      btn.textContent = nowOn ? '★' : '☆';
      btn.classList.toggle('on', nowOn);
      btn.title = nowOn ? 'Remove from favorites' : 'Add to favorites';
      if (onToggle) onToggle(pk, nowOn);
    });
  });
}

function formatHex(hex) {
  if (!hex) return '';
  return hex.match(/.{1,2}/g).join(' ');
}

function createColoredHexDump(hex, ranges) {
  if (!hex || !ranges || !ranges.length) return `<span class="hex-byte">${formatHex(hex)}</span>`;
  const bytes = hex.match(/.{1,2}/g) || [];
  // Build per-byte class map; later ranges override earlier
  const classMap = new Array(bytes.length).fill('');
  const LABEL_CLASS = {
    'Header': 'hex-header', 'Path Length': 'hex-pathlen', 'Transport Codes': 'hex-transport',
    'Path': 'hex-path', 'Payload': 'hex-payload', 'PubKey': 'hex-pubkey',
    'Timestamp': 'hex-timestamp', 'Signature': 'hex-signature', 'Flags': 'hex-flags',
    'Latitude': 'hex-location', 'Longitude': 'hex-location', 'Name': 'hex-name',
  };
  for (const r of ranges) {
    const cls = LABEL_CLASS[r.label] || 'hex-payload';
    for (let i = r.start; i <= Math.min(r.end, bytes.length - 1); i++) classMap[i] = cls;
  }
  let html = '', prevCls = null;
  for (let i = 0; i < bytes.length; i++) {
    const cls = classMap[i];
    if (cls !== prevCls) {
      if (prevCls !== null) html += '</span>';
      html += `<span class="hex-byte ${cls}">`;
      prevCls = cls;
    } else {
      html += ' ';
    }
    html += bytes[i];
  }
  if (prevCls !== null) html += '</span>';
  return html;
}

function buildHexLegend(ranges) {
  if (!ranges || !ranges.length) return '';
  const LABEL_CLASS = {
    'Header': 'hex-header', 'Path Length': 'hex-pathlen', 'Transport Codes': 'hex-transport',
    'Path': 'hex-path', 'Payload': 'hex-payload', 'PubKey': 'hex-pubkey',
    'Timestamp': 'hex-timestamp', 'Signature': 'hex-signature', 'Flags': 'hex-flags',
    'Latitude': 'hex-location', 'Longitude': 'hex-location', 'Name': 'hex-name',
  };
  const BG_COLORS = {
    'hex-header': '#f38ba8', 'hex-pathlen': '#fab387', 'hex-transport': '#89b4fa',
    'hex-path': '#a6e3a1', 'hex-payload': '#f9e2af', 'hex-pubkey': '#f9e2af',
    'hex-timestamp': '#fab387', 'hex-signature': '#f38ba8', 'hex-flags': '#94e2d5',
    'hex-location': '#89b4fa', 'hex-name': '#cba6f7',
  };
  const seen = new Set();
  let html = '';
  for (const r of ranges) {
    if (seen.has(r.label)) continue;
    seen.add(r.label);
    const cls = LABEL_CLASS[r.label] || 'hex-payload';
    const bg = BG_COLORS[cls] || '#f9e2af';
    html += `<span><span class="swatch" style="background:${bg}"></span>${r.label}</span>`;
  }
  return html;
}

// --- WebSocket ---
let ws = null;
let wsListeners = [];

function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}`);
  ws.onopen = () => document.getElementById('liveDot')?.classList.add('connected');
  ws.onclose = () => {
    document.getElementById('liveDot')?.classList.remove('connected');
    setTimeout(connectWS, 3000);
  };
  ws.onerror = () => ws.close();
  ws.onmessage = (e) => {
    try {
      const msg = JSON.parse(e.data);
      wsListeners.forEach(fn => fn(msg));
    } catch {}
  };
}

function onWS(fn) { wsListeners.push(fn); }
function offWS(fn) { wsListeners = wsListeners.filter(f => f !== fn); }

/* Global escapeHtml — used by multiple pages */
function escapeHtml(s) {
  if (!s) return '';
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

/* Global debounce */
function debounce(fn, ms) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}

/* Debounced WS helper — batches rapid messages, calls fn with array of msgs */
function debouncedOnWS(fn, ms) {
  if (typeof ms === 'undefined') ms = 250;
  let pending = [];
  let timer = null;
  function handler(msg) {
    pending.push(msg);
    if (!timer) {
      timer = setTimeout(function () {
        const batch = pending;
        pending = [];
        timer = null;
        fn(batch);
      }, ms);
    }
  }
  onWS(handler);
  return handler; // caller stores this to pass to offWS() in destroy
}

// --- Router ---
const pages = {};

function registerPage(name, mod) { pages[name] = mod; }

let currentPage = null;

function navigate() {
  const hash = location.hash.replace('#/', '') || 'packets';
  const route = hash.split('?')[0];

  // Handle parameterized routes: nodes/<pubkey> → nodes page + select
  let basePage = route;
  let routeParam = null;
  const slashIdx = route.indexOf('/');
  if (slashIdx > 0) {
    basePage = route.substring(0, slashIdx);
    routeParam = decodeURIComponent(route.substring(slashIdx + 1));
  }

  // Update nav active state
  document.querySelectorAll('.nav-link[data-route]').forEach(el => {
    el.classList.toggle('active', el.dataset.route === basePage);
  });

  if (currentPage && pages[currentPage]?.destroy) {
    pages[currentPage].destroy();
  }
  currentPage = basePage;

  const app = document.getElementById('app');
  if (pages[basePage]?.init) {
    pages[basePage].init(app, routeParam);
    app.classList.remove('page-enter'); void app.offsetWidth; app.classList.add('page-enter');
  } else {
    app.innerHTML = `<div style="padding:40px;text-align:center;color:#6b7280"><h2>${route}</h2><p>Page not yet implemented.</p></div>`;
  }
}

window.addEventListener('hashchange', navigate);
window.addEventListener('DOMContentLoaded', () => {
  connectWS();

  // --- Dark Mode ---
  const darkToggle = document.getElementById('darkModeToggle');
  const savedTheme = localStorage.getItem('meshcore-theme');
  function applyTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme);
    darkToggle.textContent = theme === 'dark' ? '🌙' : '☀️';
    localStorage.setItem('meshcore-theme', theme);
  }
  // On load: respect saved pref, else OS pref, else light
  if (savedTheme) {
    applyTheme(savedTheme);
  } else if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
    applyTheme('dark');
  } else {
    applyTheme('light');
  }
  darkToggle.addEventListener('click', () => {
    const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
    applyTheme(isDark ? 'light' : 'dark');
  });

  // --- Hamburger Menu ---
  const hamburger = document.getElementById('hamburger');
  const navLinks = document.querySelector('.nav-links');
  hamburger.addEventListener('click', () => navLinks.classList.toggle('open'));
  // Close menu on nav link click
  navLinks.querySelectorAll('.nav-link').forEach(link => {
    link.addEventListener('click', () => navLinks.classList.remove('open'));
  });

  // --- Favorites dropdown ---
  const favToggle = document.getElementById('favToggle');
  const favDropdown = document.getElementById('favDropdown');
  let favOpen = false;

  favToggle.addEventListener('click', (e) => {
    e.stopPropagation();
    favOpen = !favOpen;
    if (favOpen) {
      renderFavDropdown();
      favDropdown.classList.add('open');
    } else {
      favDropdown.classList.remove('open');
    }
  });

  document.addEventListener('click', (e) => {
    if (favOpen && !e.target.closest('.nav-fav-wrap')) {
      favOpen = false;
      favDropdown.classList.remove('open');
    }
  });

  async function renderFavDropdown() {
    const favs = getFavorites();
    if (!favs.length) {
      favDropdown.innerHTML = '<div class="fav-dd-empty">No favorites yet.<br><small>Click ☆ on any node to add it.</small></div>';
      return;
    }
    favDropdown.innerHTML = '<div class="fav-dd-loading">Loading...</div>';
    const items = await Promise.all(favs.map(async (pk) => {
      try {
        const h = await api('/nodes/' + pk + '/health');
        const age = h.stats.lastHeard ? Date.now() - new Date(h.stats.lastHeard).getTime() : null;
        const status = age === null ? '🔴' : age < 3600000 ? '🟢' : age < 86400000 ? '🟡' : '🔴';
        return '<a href="#/nodes/' + pk + '" class="fav-dd-item" data-key="' + pk + '">'
          + '<span class="fav-dd-status">' + status + '</span>'
          + '<span class="fav-dd-name">' + (h.node.name || truncate(pk, 12)) + '</span>'
          + '<span class="fav-dd-meta">' + (h.stats.lastHeard ? timeAgo(h.stats.lastHeard) : 'never') + '</span>'
          + favStar(pk, 'fav-dd-star')
          + '</a>';
      } catch {
        return '<a href="#/nodes/' + pk + '" class="fav-dd-item" data-key="' + pk + '">'
          + '<span class="fav-dd-status">❓</span>'
          + '<span class="fav-dd-name">' + truncate(pk, 16) + '</span>'
          + '<span class="fav-dd-meta">not found</span>'
          + favStar(pk, 'fav-dd-star')
          + '</a>';
      }
    }));
    favDropdown.innerHTML = items.join('');
    bindFavStars(favDropdown, () => renderFavDropdown());
    // Close dropdown on link click
    favDropdown.querySelectorAll('.fav-dd-item').forEach(a => {
      a.addEventListener('click', (e) => {
        if (e.target.closest('.fav-star')) { e.preventDefault(); return; }
        favOpen = false;
        favDropdown.classList.remove('open');
      });
    });
  }

  // --- Search ---
  const searchToggle = document.getElementById('searchToggle');
  const searchOverlay = document.getElementById('searchOverlay');
  const searchInput = document.getElementById('searchInput');
  const searchResults = document.getElementById('searchResults');
  let searchTimeout = null;

  searchToggle.addEventListener('click', () => {
    searchOverlay.classList.toggle('hidden');
    if (!searchOverlay.classList.contains('hidden')) {
      searchInput.value = '';
      searchResults.innerHTML = '';
      searchInput.focus();
    }
  });
  searchOverlay.addEventListener('click', (e) => {
    if (e.target === searchOverlay) searchOverlay.classList.add('hidden');
  });
  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
      e.preventDefault();
      searchOverlay.classList.remove('hidden');
      searchInput.value = '';
      searchResults.innerHTML = '';
      searchInput.focus();
    }
    if (e.key === 'Escape') searchOverlay.classList.add('hidden');
  });

  searchInput.addEventListener('input', () => {
    clearTimeout(searchTimeout);
    const q = searchInput.value.trim();
    if (!q) { searchResults.innerHTML = ''; return; }
    searchTimeout = setTimeout(async () => {
      try {
        const [packets, nodes, channels] = await Promise.all([
          fetch('/api/packets?limit=5&hash=' + encodeURIComponent(q)).then(r => r.json()).catch(() => ({ packets: [] })),
          fetch('/api/nodes?search=' + encodeURIComponent(q)).then(r => r.json()).catch(() => []),
          fetch('/api/channels').then(r => r.json()).catch(() => [])
        ]);
        let html = '';
        const pktList = packets.packets || packets;
        if (Array.isArray(pktList)) {
          for (const p of pktList.slice(0, 5)) {
            html += `<div class="search-result-item" onclick="location.hash='#/packets?id=${p.id}';document.getElementById('searchOverlay').classList.add('hidden')">
              <span class="search-result-type">Packet</span>${truncate(p.packet_hash || '', 16)} — ${payloadTypeName(p.payload_type)}</div>`;
          }
        }
        const nodeList = Array.isArray(nodes) ? nodes : (nodes.nodes || []);
        for (const n of nodeList.slice(0, 5)) {
          if (n.name && n.name.toLowerCase().includes(q.toLowerCase())) {
            html += `<div class="search-result-item" onclick="location.hash='#/nodes/${n.public_key}';document.getElementById('searchOverlay').classList.add('hidden')">
              <span class="search-result-type">Node</span>${n.name} — ${truncate(n.public_key || '', 16)}</div>`;
          }
        }
        const chList = Array.isArray(channels) ? channels : [];
        for (const c of chList) {
          if (c.name && c.name.toLowerCase().includes(q.toLowerCase())) {
            html += `<div class="search-result-item" onclick="location.hash='#/channels?ch=${c.channel_hash}';document.getElementById('searchOverlay').classList.add('hidden')">
              <span class="search-result-type">Channel</span>${c.name}</div>`;
          }
        }
        if (!html) html = '<div class="search-no-results">No results found</div>';
        searchResults.innerHTML = html;
      } catch { searchResults.innerHTML = '<div class="search-no-results">Search error</div>'; }
    }, 300);
  });

  // --- Login ---
  // (removed — no auth yet)

  // --- Nav Stats ---
  async function updateNavStats() {
    try {
      const stats = await api('/stats');
      const el = document.getElementById('navStats');
      if (el) {
        el.innerHTML = `<span class="stat-val">${stats.totalPackets}</span> pkts · <span class="stat-val">${stats.totalNodes}</span> nodes · <span class="stat-val">${stats.totalObservers}</span> obs`;
        el.querySelectorAll('.stat-val').forEach(s => s.classList.add('updated'));
        setTimeout(() => { el.querySelectorAll('.stat-val').forEach(s => s.classList.remove('updated')); }, 600);
      }
    } catch {}
  }
  updateNavStats();
  setInterval(updateNavStats, 15000);
  debouncedOnWS(function () { updateNavStats(); });

  if (!location.hash || location.hash === '#/') location.hash = '#/home';
  else navigate();
});

/**
 * Reusable ARIA tab-bar initialiser.
 * Adds role="tablist" to container, role="tab" + aria-selected to each button,
 * and arrow-key navigation between tabs.
 * @param {HTMLElement} container - the tab bar element
 * @param {Function} [onChange] - optional callback(activeBtn) on tab change
 */
function initTabBar(container, onChange) {
  if (!container || container.getAttribute('role') === 'tablist') return;
  container.setAttribute('role', 'tablist');
  const tabs = Array.from(container.querySelectorAll('button, [data-tab], [data-obs]'));
  tabs.forEach(btn => {
    btn.setAttribute('role', 'tab');
    const isActive = btn.classList.contains('active');
    btn.setAttribute('aria-selected', String(isActive));
    btn.setAttribute('tabindex', isActive ? '0' : '-1');
    // Link to panel if aria-controls target exists
    const panelId = btn.dataset.tab || btn.dataset.obs;
    if (panelId && document.getElementById(panelId)) {
      btn.setAttribute('aria-controls', panelId);
    }
  });
  container.addEventListener('click', (e) => {
    const btn = e.target.closest('[role="tab"]');
    if (!btn || !container.contains(btn)) return;
    tabs.forEach(b => { b.setAttribute('aria-selected', 'false'); b.setAttribute('tabindex', '-1'); });
    btn.setAttribute('aria-selected', 'true');
    btn.setAttribute('tabindex', '0');
    if (onChange) onChange(btn);
  });
  container.addEventListener('keydown', (e) => {
    const btn = e.target.closest('[role="tab"]');
    if (!btn) return;
    let idx = tabs.indexOf(btn), next = -1;
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = (idx + 1) % tabs.length;
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') next = (idx - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = tabs.length - 1;
    if (next < 0) return;
    e.preventDefault();
    tabs.forEach(b => { b.setAttribute('aria-selected', 'false'); b.setAttribute('tabindex', '-1'); });
    tabs[next].setAttribute('aria-selected', 'true');
    tabs[next].setAttribute('tabindex', '0');
    tabs[next].focus();
    tabs[next].click();
  });
}

/**
 * Make table columns resizable with drag handles. Widths saved to localStorage.
 * Call after table is in DOM. Re-call safe (idempotent per table).
 * @param {string} tableSelector - CSS selector for the table
 * @param {string} storageKey - localStorage key for persisted widths
 */
function makeColumnsResizable(tableSelector, storageKey) {
  const table = document.querySelector(tableSelector);
  if (!table) return;
  const thead = table.querySelector('thead');
  if (!thead) return;
  const ths = Array.from(thead.querySelectorAll('tr:first-child th'));
  if (ths.length < 2) return;

  if (table.dataset.resizable) return;
  table.dataset.resizable = '1';
  table.style.tableLayout = 'fixed';

  const containerW = table.parentElement.clientWidth;
  const saved = localStorage.getItem(storageKey);
  let widths;

  if (saved) {
    try { widths = JSON.parse(saved); } catch { widths = null; }
    // Validate: must be array of correct length with values summing to ~100 (percentages)
    if (widths && Array.isArray(widths) && widths.length === ths.length) {
      const sum = widths.reduce((s, w) => s + w, 0);
      if (sum > 90 && sum < 110) {
        // Saved percentages — apply directly
        table.style.tableLayout = 'fixed';
        table.style.width = '100%';
        ths.forEach((th, i) => { th.style.width = widths[i] + '%'; });
        // Skip measurement, jump to adding handles
        addResizeHandles();
        return;
      }
    }
    widths = null; // Force remeasure
  }

  if (!widths) {
    // Measure actual max content width per column by scanning visible rows
    const tbody = table.querySelector('tbody');
    const rows = tbody ? Array.from(tbody.querySelectorAll('tr')).slice(0, 30) : [];

    // Temporarily set auto layout to measure
    table.style.tableLayout = 'auto';
    table.style.width = 'auto';
    // Remove nowrap temporarily so we get true content width
    const cells = table.querySelectorAll('td, th');
    cells.forEach(c => { c.dataset.origWs = c.style.whiteSpace || ''; c.style.whiteSpace = 'nowrap'; });

    // Measure each column's max content width across header + rows
    widths = ths.map((th, i) => {
      let maxW = th.scrollWidth;
      rows.forEach(row => {
        const td = row.children[i];
        if (td) maxW = Math.max(maxW, td.scrollWidth);
      });
      return maxW + 4; // small padding buffer
    });

    cells.forEach(c => { c.style.whiteSpace = c.dataset.origWs || ''; delete c.dataset.origWs; });
  }

  // Now fit to container: if total > container, squish widest first
  const totalNeeded = widths.reduce((s, w) => s + w, 0);
  const finalWidths = [...widths];

  if (totalNeeded > containerW) {
    let excess = totalNeeded - containerW;
    const MIN_COL = 28;
    // Iteratively shave from widest columns
    while (excess > 0) {
      // Find current max width
      const maxW = Math.max(...finalWidths);
      if (maxW <= MIN_COL) break;
      // Find second-max to know our target
      const sorted = [...new Set(finalWidths)].sort((a, b) => b - a);
      const target = sorted.length > 1 ? Math.max(sorted[1], MIN_COL) : MIN_COL;
      // How many columns are at maxW?
      const atMax = finalWidths.filter(w => w >= maxW).length;
      const canShavePerCol = maxW - target;
      const neededPerCol = Math.ceil(excess / atMax);
      const shavePerCol = Math.min(canShavePerCol, neededPerCol);

      for (let i = 0; i < finalWidths.length; i++) {
        if (finalWidths[i] >= maxW) {
          const shave = Math.min(shavePerCol, excess);
          finalWidths[i] -= shave;
          excess -= shave;
          if (excess <= 0) break;
        }
      }
    }
  } else if (totalNeeded < containerW) {
    // Give surplus to the 2 widest columns (content-heavy ones)
    const surplus = containerW - totalNeeded;
    const indexed = finalWidths.map((w, i) => ({ w, i })).sort((a, b) => b.w - a.w);
    const topN = indexed.slice(0, Math.min(2, indexed.length));
    const topTotal = topN.reduce((s, x) => s + x.w, 0);
    topN.forEach(x => { finalWidths[x.i] += Math.round(surplus * (x.w / topTotal)); });
  }

  table.style.width = '100%';
  const totalFinal = finalWidths.reduce((s, w) => s + w, 0);
  ths.forEach((th, i) => { th.style.width = (finalWidths[i] / totalFinal * 100) + '%'; });

  addResizeHandles();

  function addResizeHandles() {
  // Add resize handles
  ths.forEach((th, i) => {
    if (i === ths.length - 1) return;
    th.style.position = 'relative';
    const handle = document.createElement('div');
    handle.className = 'col-resize-handle';
    handle.addEventListener('mousedown', (e) => {
      e.preventDefault();
      e.stopPropagation();
      const startX = e.clientX;
      const startW = th.offsetWidth;
      const startTableW = table.offsetWidth;
      handle.classList.add('active');
      document.body.style.cursor = 'col-resize';
      document.body.style.userSelect = 'none';

      function onMove(e2) {
        const dx = e2.clientX - startX;
        const newW = Math.max(50, startW + dx);
        const delta = newW - th.offsetWidth;
        if (delta === 0) return;
        // Steal/give space from columns to the right, proportionally
        const rightThs = ths.slice(i + 1);
        const rightWidths = rightThs.map(t => t.offsetWidth);
        const rightTotal = rightWidths.reduce((s, w) => s + w, 0);
        if (rightTotal - delta < rightThs.length * 50) return; // can't squeeze below 50px each
        th.style.width = newW + 'px';
        const scale = (rightTotal - delta) / rightTotal;
        rightThs.forEach(t => { t.style.width = Math.max(50, t.offsetWidth * scale) + 'px'; });
      }
      function onUp() {
        handle.classList.remove('active');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        // Save as percentages
        const tableW = table.offsetWidth;
        const ws = ths.map(t => (t.offsetWidth / tableW * 100));
        localStorage.setItem(storageKey, JSON.stringify(ws));
        // Re-apply as percentages
        ths.forEach((t, j) => { t.style.width = ws[j] + '%'; });
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', onUp);
      }
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });
    th.appendChild(handle);
  });
  } // end addResizeHandles
}
