/* === MeshCore Analyzer — customize.js === */
/* Tools → Customization: visual config builder with live preview & JSON export */
'use strict';

(function () {
  let styleEl = null;
  let originalValues = {};
  let activeTab = 'branding';

  const DEFAULTS = {
    branding: {
      siteName: 'MeshCore Analyzer',
      tagline: 'Real-time MeshCore LoRa mesh network analyzer',
      logoUrl: '',
      faviconUrl: ''
    },
    theme: {
      accent: '#4a9eff',
      accentHover: '#6db3ff',
      navBg: '#0f0f23',
      navBg2: '#1a1a2e',
      text: '#1a1a2e',
      textMuted: '#5b6370',
      border: '#e2e5ea',
      surface0: '#f4f5f7',
      surface1: '#ffffff',
      surface2: '#ffffff',
      cardBg: '#ffffff',
      contentBg: '#f4f5f7',
      inputBg: '#ffffff',
      rowStripe: '#f9fafb',
      rowHover: '#eef2ff',
      selectedBg: '#dbeafe',
      statusGreen: '#22c55e',
      statusYellow: '#eab308',
      statusRed: '#ef4444'
    },
    themeDark: {
      accent: '#4a9eff',
      accentHover: '#6db3ff',
      navBg: '#0f0f23',
      navBg2: '#1a1a2e',
      text: '#e2e8f0',
      textMuted: '#a8b8cc',
      border: '#334155',
      surface0: '#0f0f23',
      surface1: '#1a1a2e',
      surface2: '#232340',
      cardBg: '#1a1a2e',
      contentBg: '#0f0f23',
      inputBg: '#1e1e34',
      rowStripe: '#1e1e34',
      rowHover: '#2d2d50',
      selectedBg: '#1e3a5f',
      statusGreen: '#22c55e',
      statusYellow: '#eab308',
      statusRed: '#ef4444'
    },
    nodeColors: {
      repeater: '#dc2626',
      companion: '#2563eb',
      room: '#16a34a',
      sensor: '#d97706',
      observer: '#8b5cf6'
    },
    home: {
      heroTitle: 'MeshCore Analyzer',
      heroSubtitle: 'Find your nodes to start monitoring them.',
      steps: [
        { emoji: '📡', title: 'Connect', description: 'Link your node to the mesh' },
        { emoji: '🔍', title: 'Monitor', description: 'Watch packets flow in real-time' },
        { emoji: '📊', title: 'Analyze', description: "Understand your network's health" }
      ],
      checklist: [
        { question: 'How do I add my node?', answer: 'Search for your node name or paste your public key.' },
        { question: 'What regions are covered?', answer: 'Check the map page to see active observers and nodes.' }
      ],
      footerLinks: [
        { label: '📦 Packets', url: '#/packets' },
        { label: '🗺️ Network Map', url: '#/map' },
        { label: '🔴 Live', url: '#/live' },
        { label: '📡 All Nodes', url: '#/nodes' },
        { label: '💬 Channels', url: '#/channels' }
      ]
    }
  };

  // CSS variable name → theme key mapping
  const THEME_CSS_MAP = {
    accent: '--accent',
    accentHover: '--accent-hover',
    navBg: '--nav-bg',
    navBg2: '--nav-bg2',
    text: '--text',
    textMuted: '--text-muted',
    border: '--border',
    surface0: '--surface-0',
    surface1: '--surface-1',
    surface2: '--surface-2',
    cardBg: '--card-bg',
    contentBg: '--content-bg',
    inputBg: '--input-bg',
    rowStripe: '--row-stripe',
    rowHover: '--row-hover',
    selectedBg: '--selected-bg',
    statusGreen: '--status-green',
    statusYellow: '--status-yellow',
    statusRed: '--status-red'
  };

  const THEME_LABELS = {
    accent: 'Accent',
    accentHover: 'Accent Hover',
    navBg: 'Nav Background',
    navBg2: 'Nav Background 2',
    text: 'Text',
    textMuted: 'Muted Text',
    border: 'Borders',
    surface0: 'Page Background',
    surface1: 'Card Background',
    surface2: 'Surface 2',
    cardBg: 'Card Background',
    contentBg: 'Content Background',
    inputBg: 'Input Background',
    rowStripe: 'Table Stripe',
    rowHover: 'Row Hover',
    selectedBg: 'Selected Row',
    statusGreen: 'Status Green',
    statusYellow: 'Status Yellow',
    statusRed: 'Status Red'
  };

  const THEME_HINTS = {
    accent: 'Active nav tab, buttons, links, selected rows, badges',
    accentHover: 'Button and link hover states',
    navBg: 'Top navigation bar gradient start',
    navBg2: 'Top navigation bar gradient end',
    text: 'Primary text color',
    textMuted: 'Secondary/muted text, labels, timestamps',
    border: 'Table borders, card borders, dividers',
    surface0: 'Main page background',
    surface1: 'Cards, panels, modals',
    surface2: 'Nested surfaces, inputs, secondary panels',
    cardBg: 'Card and detail panel backgrounds',
    contentBg: 'Content area background',
    inputBg: 'Text inputs, dropdowns, search boxes',
    rowStripe: 'Alternating table row background',
    rowHover: 'Table row hover highlight',
    selectedBg: 'Selected/active row background',
    statusGreen: 'Healthy nodes, online indicators',
    statusYellow: 'Degraded nodes, warnings',
    statusRed: 'Offline/silent nodes, errors'
  };

  const NODE_LABELS = {
    repeater: 'Repeater',
    companion: 'Companion',
    room: 'Room Server',
    sensor: 'Sensor',
    observer: 'Observer'
  };

  const NODE_HINTS = {
    repeater: 'Infrastructure nodes that relay packets — map markers, packet path badges, node list',
    companion: 'End-user devices — map markers, packet detail, node list',
    room: 'Room/chat server nodes — map markers, node list',
    sensor: 'Sensor/telemetry nodes — map markers, node list',
    observer: 'MQTT observer stations — map markers (purple stars), observer list, packet headers'
  };

  const NODE_EMOJI = { repeater: '◆', companion: '●', room: '■', sensor: '▲', observer: '★' };

  // Current state
  let state = {};

  function deepClone(o) { return JSON.parse(JSON.stringify(o)); }

  function initState() {
    const cfg = window.SITE_CONFIG || {};
    state = {
      branding: Object.assign({}, DEFAULTS.branding, cfg.branding || {}),
      theme: Object.assign({}, DEFAULTS.theme, cfg.theme || {}),
      themeDark: Object.assign({}, DEFAULTS.themeDark, cfg.themeDark || {}),
      nodeColors: Object.assign({}, DEFAULTS.nodeColors, cfg.nodeColors || {}),
      home: {
        heroTitle: (cfg.home && cfg.home.heroTitle) || DEFAULTS.home.heroTitle,
        heroSubtitle: (cfg.home && cfg.home.heroSubtitle) || DEFAULTS.home.heroSubtitle,
        steps: deepClone((cfg.home && cfg.home.steps) || DEFAULTS.home.steps),
        checklist: deepClone((cfg.home && cfg.home.checklist) || DEFAULTS.home.checklist),
        footerLinks: deepClone((cfg.home && cfg.home.footerLinks) || DEFAULTS.home.footerLinks)
      }
    };
  }

  function isDarkMode() {
    return document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
  }

  function activeTheme() { return isDarkMode() ? state.themeDark : state.theme; }
  function activeDefaults() { return isDarkMode() ? DEFAULTS.themeDark : DEFAULTS.theme; }

  function saveOriginalCSS() {
    var cs = getComputedStyle(document.documentElement);
    originalValues = {};
    for (var key in THEME_CSS_MAP) {
      originalValues[key] = cs.getPropertyValue(THEME_CSS_MAP[key]).trim();
    }
  }

  function applyThemePreview() {
    var t = activeTheme();
    for (var key in THEME_CSS_MAP) {
      if (t[key]) document.documentElement.style.setProperty(THEME_CSS_MAP[key], t[key]);
    }
  }

  function resetPreview() {
    for (var key in THEME_CSS_MAP) {
      document.documentElement.style.removeProperty(THEME_CSS_MAP[key]);
    }
  }

  function esc(s) { var d = document.createElement('div'); d.textContent = s || ''; return d.innerHTML; }
  function escAttr(s) { return (s || '').replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;'); }

  function injectStyles() {
    if (styleEl) return;
    styleEl = document.createElement('style');
    styleEl.textContent = `
      .cust-overlay { position: fixed; top: 52px; right: 12px; z-index: 1050; width: 380px; max-height: calc(100vh - 64px);
        background: var(--card-bg); border: 1px solid var(--border); border-radius: 10px;
        box-shadow: 0 8px 32px rgba(0,0,0,0.3); display: flex; flex-direction: column; overflow: hidden; }
      .cust-overlay.hidden { display: none; }
      .cust-header { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px;
        border-bottom: 1px solid var(--border); cursor: move; user-select: none; flex-shrink: 0; }
      .cust-header h2 { margin: 0; font-size: 15px; }
      .cust-close { background: none; border: none; font-size: 18px; cursor: pointer; color: var(--text-muted); padding: 4px 8px; border-radius: 4px; }
      .cust-close:hover { background: var(--surface-3); color: var(--text); }
      .cust-body { flex: 1; overflow-y: auto; padding: 0; }
      .cust-tabs { display: flex; gap: 0; border-bottom: 1px solid var(--border); flex-shrink: 0; }
      .cust-tab { padding: 8px 10px; cursor: pointer; border: none; background: none; color: var(--text-muted);
        font-size: 12px; font-weight: 500; border-bottom: 2px solid transparent; margin-bottom: -1px; white-space: nowrap; flex: 1; text-align: center; }
      .cust-tab-text { font-size: 10px; display: block; }
      .cust-tab:hover { color: var(--text); }
      .cust-tab.active { color: var(--accent); border-bottom-color: var(--accent); }
      .cust-panel { display: none; padding: 12px 16px; }
      .cust-panel.active { display: block; }
      .cust-field { margin-bottom: 12px; }
      .cust-field label { display: block; font-size: 12px; font-weight: 600; margin-bottom: 3px; color: var(--text); }
      .cust-field input[type="text"], .cust-field textarea { width: 100%; padding: 6px 8px; border: 1px solid var(--border);
        border-radius: 6px; font-size: 13px; background: var(--input-bg); color: var(--text); box-sizing: border-box; }
      .cust-field input[type="text"]:focus, .cust-field textarea:focus { outline: none; border-color: var(--accent); }
      .cust-color-row { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
      .cust-color-row > div:first-child { min-width: 160px; flex: 1; }
      .cust-color-row label { font-size: 12px; font-weight: 600; margin: 0; display: block; }
      .cust-hint { font-size: 10px; color: var(--text-muted); margin-top: 1px; line-height: 1.2; }
      .cust-color-row input[type="color"] { width: 40px; height: 32px; border: 1px solid var(--border);
        border-radius: 6px; cursor: pointer; padding: 2px; background: var(--input-bg); }
      .cust-color-row .cust-hex { font-family: var(--mono); font-size: 12px; color: var(--text-muted); min-width: 70px; }
      .cust-color-row .cust-reset-btn { font-size: 11px; padding: 2px 8px; border: 1px solid var(--border);
        border-radius: 4px; background: var(--surface-2); color: var(--text-muted); cursor: pointer; }
      .cust-color-row .cust-reset-btn:hover { background: var(--surface-3); }
      .cust-node-dot { display: inline-block; width: 16px; height: 16px; border-radius: 50%; vertical-align: middle; }
      .cust-preview-img { max-width: 200px; max-height: 60px; margin-top: 6px; border-radius: 6px; border: 1px solid var(--border); }
      .cust-list-item { display: flex; gap: 8px; align-items: flex-start; margin-bottom: 8px; padding: 8px;
        background: var(--surface-1); border: 1px solid var(--border); border-radius: 6px; }
      .cust-list-item input { flex: 1; padding: 6px 8px; border: 1px solid var(--border); border-radius: 4px;
        font-size: 13px; background: var(--input-bg); color: var(--text); }
      .cust-list-item .cust-emoji-input { max-width: 50px; text-align: center; }
      .cust-list-btn { padding: 4px 10px; border: 1px solid var(--border); border-radius: 4px; background: var(--surface-2);
        color: var(--text-muted); cursor: pointer; font-size: 12px; }
      .cust-list-btn:hover { background: var(--surface-3); }
      .cust-list-btn.danger { color: #ef4444; }
      .cust-list-btn.danger:hover { background: #fef2f2; }
      .cust-add-btn { display: inline-flex; align-items: center; gap: 4px; padding: 6px 14px; border: 1px dashed var(--border);
        border-radius: 6px; background: none; color: var(--accent); cursor: pointer; font-size: 13px; margin-top: 4px; }
      .cust-add-btn:hover { background: var(--hover-bg); }
      .cust-export-area { width: 100%; min-height: 300px; font-family: var(--mono); font-size: 12px;
        background: var(--surface-1); border: 1px solid var(--border); border-radius: 6px; padding: 12px;
        color: var(--text); resize: vertical; box-sizing: border-box; }
      .cust-export-btns { display: flex; gap: 8px; margin-top: 8px; flex-wrap: wrap; }
      .cust-export-btns button { padding: 6px 14px; border: none; border-radius: 6px; cursor: pointer; font-size: 12px; font-weight: 500; }
      .cust-copy-btn { background: var(--accent); color: #fff; }
      .cust-copy-btn:hover { opacity: 0.9; }
      .cust-dl-btn { background: var(--surface-2); color: var(--text); border: 1px solid var(--border) !important; }
      .cust-save-user { background: #22c55e; color: #fff; }
      .cust-save-user:hover { background: #16a34a; }
      .cust-reset-user { background: var(--surface-2); color: #ef4444; border: 1px solid #ef4444 !important; }
      .cust-reset-user:hover { background: #ef4444; color: #fff; }
      .cust-dl-btn:hover { background: var(--surface-3); }
      .cust-reset-preview { margin-top: 12px; padding: 8px 16px; border: 1px solid var(--border); border-radius: 6px;
        background: var(--surface-2); color: var(--text); cursor: pointer; font-size: 13px; }
      .cust-reset-preview:hover { background: var(--surface-3); }
      .cust-instructions { background: var(--surface-1); border: 1px solid var(--border); border-radius: 6px;
        padding: 12px 16px; margin-top: 16px; font-size: 13px; color: var(--text-muted); line-height: 1.6; }
      .cust-instructions code { background: var(--surface-2); padding: 2px 6px; border-radius: 3px; font-family: var(--mono); font-size: 12px; }
      .cust-section-title { font-size: 16px; font-weight: 600; margin: 0 0 12px; }
      @media (max-width: 600px) {
        .cust-overlay { left: 8px; right: 8px; width: auto; top: 56px; }
        .cust-tabs { gap: 0; }
        .cust-tab { padding: 6px 8px; font-size: 11px; }
        .cust-color-row > div:first-child { min-width: 120px; }
        .cust-list-item { flex-wrap: wrap; }
      }
    `;
    document.head.appendChild(styleEl);
  }

  function removeStyles() {
    if (styleEl) { styleEl.remove(); styleEl = null; }
  }

  function renderTabs() {
    var tabs = [
      { id: 'branding', label: '🏷️', title: 'Branding' },
      { id: 'theme', label: '🎨', title: 'Theme Colors' },
      { id: 'nodes', label: '📡', title: 'Node Colors' },
      { id: 'home', label: '🏠', title: 'Home Page' },
      { id: 'export', label: '📤', title: 'Export / Save' }
    ];
    return '<div class="cust-tabs">' +
      tabs.map(function (t) {
        return '<button class="cust-tab' + (t.id === activeTab ? ' active' : '') + '" data-tab="' + t.id + '" title="' + t.title + '">' + t.label + ' <span class="cust-tab-text">' + t.title + '</span></button>';
      }).join('') + '</div>';
  }

  function renderBranding() {
    var b = state.branding;
    var logoPreview = b.logoUrl ? '<img class="cust-preview-img" src="' + escAttr(b.logoUrl) + '" alt="Logo preview" onerror="this.style.display=\'none\'">' : '';
    return '<div class="cust-panel' + (activeTab === 'branding' ? ' active' : '') + '" data-panel="branding">' +
      '<div class="cust-field"><label>Site Name</label><input type="text" data-key="branding.siteName" value="' + escAttr(b.siteName) + '"></div>' +
      '<div class="cust-field"><label>Tagline</label><input type="text" data-key="branding.tagline" value="' + escAttr(b.tagline) + '"></div>' +
      '<div class="cust-field"><label>Logo URL</label><input type="text" data-key="branding.logoUrl" value="' + escAttr(b.logoUrl) + '" placeholder="https://...">' + logoPreview + '</div>' +
      '<div class="cust-field"><label>Favicon URL</label><input type="text" data-key="branding.faviconUrl" value="' + escAttr(b.faviconUrl) + '" placeholder="https://..."></div>' +
    '</div>';
  }

  function renderTheme() {
    var dark = isDarkMode();
    var modeLabel = dark ? '🌙 Dark Mode' : '☀️ Light Mode';
    var themeKey = dark ? 'themeDark' : 'theme';
    var defs = activeDefaults();
    var current = activeTheme();
    var rows = '';
    for (var key in THEME_LABELS) {
      var val = current[key] || defs[key] || '#000000';
      var def = defs[key] || '#000000';
      rows += '<div class="cust-color-row">' +
        '<div><label>' + THEME_LABELS[key] + '</label>' +
        '<div class="cust-hint">' + (THEME_HINTS[key] || '') + '</div></div>' +
        '<input type="color" data-theme="' + key + '" value="' + val + '">' +
        '<span class="cust-hex" data-hex="' + key + '">' + val + '</span>' +
        (val !== def ? '<button class="cust-reset-btn" data-reset-theme="' + key + '">Reset</button>' : '') +
      '</div>';
    }
    return '<div class="cust-panel' + (activeTab === 'theme' ? ' active' : '') + '" data-panel="theme">' +
      '<p class="cust-section-title">' + modeLabel + ' Colors</p>' +
      '<p style="font-size:11px;color:var(--text-muted);margin:0 0 12px">Editing ' + (dark ? 'dark' : 'light') + ' mode. Toggle ☀️/🌙 in nav to switch.</p>' +
      rows +
      '<button class="cust-reset-preview" id="custResetPreview">↩ Reset Preview</button>' +
    '</div>';
  }

  function renderNodes() {
    var rows = '';
    for (var key in NODE_LABELS) {
      var val = state.nodeColors[key];
      var def = DEFAULTS.nodeColors[key];
      rows += '<div class="cust-color-row">' +
        '<div><label>' + NODE_EMOJI[key] + ' ' + NODE_LABELS[key] + '</label>' +
        '<div class="cust-hint">' + (NODE_HINTS[key] || '') + '</div></div>' +
        '<input type="color" data-node="' + key + '" value="' + val + '">' +
        '<span class="cust-node-dot" style="background:' + val + '" data-dot="' + key + '"></span>' +
        '<span class="cust-hex" data-nhex="' + key + '">' + val + '</span>' +
        (val !== def ? '<button class="cust-reset-btn" data-reset-node="' + key + '">Reset</button>' : '') +
      '</div>';
    }
    return '<div class="cust-panel' + (activeTab === 'nodes' ? ' active' : '') + '" data-panel="nodes">' +
      '<p class="cust-section-title">Node Role Colors</p>' + rows + '</div>';
  }

  function renderHome() {
    var h = state.home;
    var stepsHtml = h.steps.map(function (s, i) {
      return '<div class="cust-list-item" data-step="' + i + '">' +
        '<input class="cust-emoji-input" data-step-field="emoji" data-idx="' + i + '" value="' + escAttr(s.emoji) + '" placeholder="📡">' +
        '<input data-step-field="title" data-idx="' + i + '" value="' + escAttr(s.title) + '" placeholder="Title">' +
        '<input data-step-field="description" data-idx="' + i + '" value="' + escAttr(s.description) + '" placeholder="Description" style="flex:2">' +
        '<button class="cust-list-btn" data-move-step="' + i + '" data-dir="up" title="Move up">↑</button>' +
        '<button class="cust-list-btn" data-move-step="' + i + '" data-dir="down" title="Move down">↓</button>' +
        '<button class="cust-list-btn danger" data-rm-step="' + i + '" title="Remove">✕</button>' +
      '</div>';
    }).join('');

    var checkHtml = h.checklist.map(function (c, i) {
      return '<div class="cust-list-item" data-check="' + i + '">' +
        '<input data-check-field="question" data-idx="' + i + '" value="' + escAttr(c.question) + '" placeholder="Question">' +
        '<input data-check-field="answer" data-idx="' + i + '" value="' + escAttr(c.answer) + '" placeholder="Answer" style="flex:2">' +
        '<button class="cust-list-btn danger" data-rm-check="' + i + '" title="Remove">✕</button>' +
      '</div>';
    }).join('');

    var linksHtml = h.footerLinks.map(function (l, i) {
      return '<div class="cust-list-item" data-link="' + i + '">' +
        '<input data-link-field="label" data-idx="' + i + '" value="' + escAttr(l.label) + '" placeholder="Label">' +
        '<input data-link-field="url" data-idx="' + i + '" value="' + escAttr(l.url) + '" placeholder="URL" style="flex:2">' +
        '<button class="cust-list-btn danger" data-rm-link="' + i + '" title="Remove">✕</button>' +
      '</div>';
    }).join('');

    return '<div class="cust-panel' + (activeTab === 'home' ? ' active' : '') + '" data-panel="home">' +
      '<div class="cust-field"><label>Hero Title</label><input type="text" data-key="home.heroTitle" value="' + escAttr(h.heroTitle) + '"></div>' +
      '<div class="cust-field"><label>Hero Subtitle</label><input type="text" data-key="home.heroSubtitle" value="' + escAttr(h.heroSubtitle) + '"></div>' +
      '<p class="cust-section-title" style="margin-top:20px">Steps</p>' + stepsHtml +
      '<button class="cust-add-btn" id="addStep">+ Add Step</button>' +
      '<p class="cust-section-title" style="margin-top:24px">FAQ / Checklist</p>' + checkHtml +
      '<button class="cust-add-btn" id="addCheck">+ Add Question</button>' +
      '<p class="cust-section-title" style="margin-top:24px">Footer Links</p>' + linksHtml +
      '<button class="cust-add-btn" id="addLink">+ Add Link</button>' +
    '</div>';
  }

  function buildExport() {
    var out = {};
    // Branding — only changed values
    var bd = {};
    for (var bk in DEFAULTS.branding) {
      if (state.branding[bk] && state.branding[bk] !== DEFAULTS.branding[bk]) bd[bk] = state.branding[bk];
    }
    if (Object.keys(bd).length) out.branding = bd;

    // Theme
    var th = {};
    for (var tk in DEFAULTS.theme) {
      if (state.theme[tk] !== DEFAULTS.theme[tk]) th[tk] = state.theme[tk];
    }
    if (Object.keys(th).length) out.theme = th;

    // Dark theme
    var thd = {};
    for (var tdk in DEFAULTS.themeDark) {
      if (state.themeDark[tdk] !== DEFAULTS.themeDark[tdk]) thd[tdk] = state.themeDark[tdk];
    }
    if (Object.keys(thd).length) out.themeDark = thd;

    // Node colors
    var nc = {};
    for (var nk in DEFAULTS.nodeColors) {
      if (state.nodeColors[nk] !== DEFAULTS.nodeColors[nk]) nc[nk] = state.nodeColors[nk];
    }
    if (Object.keys(nc).length) out.nodeColors = nc;

    // Home
    var hm = {};
    if (state.home.heroTitle !== DEFAULTS.home.heroTitle) hm.heroTitle = state.home.heroTitle;
    if (state.home.heroSubtitle !== DEFAULTS.home.heroSubtitle) hm.heroSubtitle = state.home.heroSubtitle;
    if (JSON.stringify(state.home.steps) !== JSON.stringify(DEFAULTS.home.steps)) hm.steps = state.home.steps;
    if (JSON.stringify(state.home.checklist) !== JSON.stringify(DEFAULTS.home.checklist)) hm.checklist = state.home.checklist;
    if (JSON.stringify(state.home.footerLinks) !== JSON.stringify(DEFAULTS.home.footerLinks)) hm.footerLinks = state.home.footerLinks;
    if (Object.keys(hm).length) out.home = hm;

    return out;
  }

  function renderExport() {
    var json = JSON.stringify(buildExport(), null, 2);
    var hasUserTheme = !!localStorage.getItem('meshcore-user-theme');
    return '<div class="cust-panel' + (activeTab === 'export' ? ' active' : '') + '" data-panel="export">' +
      '<p class="cust-section-title">My Preferences</p>' +
      '<p style="font-size:12px;color:var(--text-muted);margin-bottom:8px">Save these colors just for you — stored in your browser, works on any instance.</p>' +
      '<div class="cust-export-btns" style="margin-bottom:16px">' +
        '<button class="cust-save-user" id="custSaveUser">💾 Save as my theme</button>' +
        (hasUserTheme ? '<button class="cust-reset-user" id="custResetUser">🗑️ Reset my theme</button>' : '') +
      '</div>' +
      '<hr style="border:none;border-top:1px solid var(--border);margin:16px 0">' +
      '<p class="cust-section-title">Admin Export</p>' +
      '<p style="font-size:12px;color:var(--text-muted);margin-bottom:8px">Export as config.json for server deployment — applies to all users of this instance.</p>' +
      '<textarea class="cust-export-area" readonly id="custExportJson">' + esc(json) + '</textarea>' +
      '<div class="cust-export-btns">' +
        '<button class="cust-copy-btn" id="custCopy">📋 Copy to Clipboard</button>' +
        '<button class="cust-dl-btn" id="custDownload">💾 Download config-theme.json</button>' +
      '</div>' +
      '<div class="cust-instructions">' +
        '<strong>How to apply:</strong><br>' +
        'Merge this JSON into your <code>config.json</code> file and restart the server.<br>' +
        'Only values that differ from defaults are included.' +
      '</div>' +
    '</div>';
  }

  let panelEl = null;

  function render(container) {
    container.innerHTML =
      renderTabs() +
      '<div class="cust-body">' +
      renderBranding() +
      renderTheme() +
      renderNodes() +
      renderHome() +
      renderExport() +
      '</div>';
    bindEvents(container);
  }

  function bindEvents(container) {
    // Tab switching
    container.querySelectorAll('.cust-tab').forEach(function (btn) {
      btn.addEventListener('click', function () {
        activeTab = btn.dataset.tab;
        render(container);
      });
    });

    // Text inputs (branding + home hero)
    container.querySelectorAll('input[data-key]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        var parts = inp.dataset.key.split('.');
        if (parts.length === 2) {
          state[parts[0]][parts[1]] = inp.value;
        }
      });
    });

    // Theme color pickers
    container.querySelectorAll('input[data-theme]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        var key = inp.dataset.theme;
        var themeKey = isDarkMode() ? 'themeDark' : 'theme';
        state[themeKey][key] = inp.value;
        var hex = container.querySelector('[data-hex="' + key + '"]');
        if (hex) hex.textContent = inp.value;
        applyThemePreview();
      });
    });

    // Theme reset buttons
    container.querySelectorAll('[data-reset-theme]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var key = btn.dataset.resetTheme;
        var themeKey = isDarkMode() ? 'themeDark' : 'theme';
        state[themeKey][key] = activeDefaults()[key];
        applyThemePreview();
        render(container);
      });
    });

    // Reset preview button
    var resetBtn = document.getElementById('custResetPreview');
    if (resetBtn) {
      resetBtn.addEventListener('click', function () {
        state.theme = Object.assign({}, DEFAULTS.theme);
        resetPreview();
        render(container);
      });
    }

    // Node color pickers
    container.querySelectorAll('input[data-node]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        var key = inp.dataset.node;
        state.nodeColors[key] = inp.value;
        // Sync to global role colors used by map/packets/etc
        if (window.ROLE_COLORS) window.ROLE_COLORS[key] = inp.value;
        if (window.ROLE_STYLE && window.ROLE_STYLE[key]) window.ROLE_STYLE[key].color = inp.value;
        var dot = container.querySelector('[data-dot="' + key + '"]');
        if (dot) dot.style.background = inp.value;
        var hex = container.querySelector('[data-nhex="' + key + '"]');
        if (hex) hex.textContent = inp.value;
      });
    });

    // Node reset buttons
    container.querySelectorAll('[data-reset-node]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var key = btn.dataset.resetNode;
        state.nodeColors[key] = DEFAULTS.nodeColors[key];
        if (window.ROLE_COLORS) window.ROLE_COLORS[key] = DEFAULTS.nodeColors[key];
        if (window.ROLE_STYLE && window.ROLE_STYLE[key]) window.ROLE_STYLE[key].color = DEFAULTS.nodeColors[key];
        render(container);
      });
    });

    // Steps
    container.querySelectorAll('[data-step-field]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        var i = parseInt(inp.dataset.idx);
        state.home.steps[i][inp.dataset.stepField] = inp.value;
      });
    });
    container.querySelectorAll('[data-move-step]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var i = parseInt(btn.dataset.moveStep);
        var dir = btn.dataset.dir === 'up' ? -1 : 1;
        var j = i + dir;
        if (j < 0 || j >= state.home.steps.length) return;
        var tmp = state.home.steps[i];
        state.home.steps[i] = state.home.steps[j];
        state.home.steps[j] = tmp;
        render(container);
      });
    });
    container.querySelectorAll('[data-rm-step]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        state.home.steps.splice(parseInt(btn.dataset.rmStep), 1);
        render(container);
      });
    });
    var addStepBtn = document.getElementById('addStep');
    if (addStepBtn) addStepBtn.addEventListener('click', function () {
      state.home.steps.push({ emoji: '📌', title: '', description: '' });
      render(container);
    });

    // Checklist
    container.querySelectorAll('[data-check-field]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        var i = parseInt(inp.dataset.idx);
        state.home.checklist[i][inp.dataset.checkField] = inp.value;
      });
    });
    container.querySelectorAll('[data-rm-check]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        state.home.checklist.splice(parseInt(btn.dataset.rmCheck), 1);
        render(container);
      });
    });
    var addCheckBtn = document.getElementById('addCheck');
    if (addCheckBtn) addCheckBtn.addEventListener('click', function () {
      state.home.checklist.push({ question: '', answer: '' });
      render(container);
    });

    // Footer links
    container.querySelectorAll('[data-link-field]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        var i = parseInt(inp.dataset.idx);
        state.home.footerLinks[i][inp.dataset.linkField] = inp.value;
      });
    });
    container.querySelectorAll('[data-rm-link]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        state.home.footerLinks.splice(parseInt(btn.dataset.rmLink), 1);
        render(container);
      });
    });
    var addLinkBtn = document.getElementById('addLink');
    if (addLinkBtn) addLinkBtn.addEventListener('click', function () {
      state.home.footerLinks.push({ label: '', url: '' });
      render(container);
    });

    // Export copy
    var copyBtn = document.getElementById('custCopy');
    if (copyBtn) copyBtn.addEventListener('click', function () {
      var ta = document.getElementById('custExportJson');
      if (ta) {
        navigator.clipboard.writeText(ta.value).then(function () {
          copyBtn.textContent = '✓ Copied!';
          setTimeout(function () { copyBtn.textContent = '📋 Copy to Clipboard'; }, 2000);
        }).catch(function () {
          ta.select();
          document.execCommand('copy');
          copyBtn.textContent = '✓ Copied!';
          setTimeout(function () { copyBtn.textContent = '📋 Copy to Clipboard'; }, 2000);
        });
      }
    });

    // Export download
    var dlBtn = document.getElementById('custDownload');
    if (dlBtn) dlBtn.addEventListener('click', function () {
      var json = JSON.stringify(buildExport(), null, 2);
      var blob = new Blob([json], { type: 'application/json' });
      var a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'config-theme.json';
      a.click();
      URL.revokeObjectURL(a.href);
    });

    // Save user theme to localStorage
    var saveUserBtn = document.getElementById('custSaveUser');
    if (saveUserBtn) saveUserBtn.addEventListener('click', function () {
      var exportData = buildExport();
      localStorage.setItem('meshcore-user-theme', JSON.stringify(exportData));
      saveUserBtn.textContent = '✓ Saved!';
      setTimeout(function () { saveUserBtn.textContent = '💾 Save as my theme'; }, 2000);
    });

    // Reset user theme
    var resetUserBtn = document.getElementById('custResetUser');
    if (resetUserBtn) resetUserBtn.addEventListener('click', function () {
      localStorage.removeItem('meshcore-user-theme');
      resetPreview();
      initState();
      render(container);
      applyThemePreview();
    });
  }

  function toggle() {
    if (panelEl) {
      panelEl.classList.toggle('hidden');
      return;
    }
    // First open — create the panel
    injectStyles();
    saveOriginalCSS();
    initState();

    panelEl = document.createElement('div');
    panelEl.className = 'cust-overlay';
    panelEl.innerHTML =
      '<div class="cust-header">' +
        '<h2>🎨 Customize</h2>' +
        '<button class="cust-close" title="Close">✕</button>' +
      '</div>' +
      '<div class="cust-inner"></div>';
    document.body.appendChild(panelEl);

    panelEl.querySelector('.cust-close').addEventListener('click', () => panelEl.classList.add('hidden'));

    // Drag support
    const header = panelEl.querySelector('.cust-header');
    let dragX = 0, dragY = 0, startX = 0, startY = 0;
    header.addEventListener('mousedown', (e) => {
      if (e.target.closest('.cust-close')) return;
      dragX = panelEl.offsetLeft; dragY = panelEl.offsetTop;
      startX = e.clientX; startY = e.clientY;
      const onMove = (ev) => {
        panelEl.style.left = (dragX + ev.clientX - startX) + 'px';
        panelEl.style.top = (dragY + ev.clientY - startY) + 'px';
        panelEl.style.right = 'auto';
      };
      const onUp = () => { document.removeEventListener('mousemove', onMove); document.removeEventListener('mouseup', onUp); };
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });

    render(panelEl.querySelector('.cust-inner'));
    applyThemePreview();
  }

  // Wire up toggle button
  document.addEventListener('DOMContentLoaded', () => {
    const btn = document.getElementById('customizeToggle');
    if (btn) btn.addEventListener('click', toggle);

    // Auto-apply saved user theme from localStorage
    try {
      const saved = localStorage.getItem('meshcore-user-theme');
      if (saved) {
        const userTheme = JSON.parse(saved);
        const dark = document.documentElement.getAttribute('data-theme') === 'dark' ||
          (document.documentElement.getAttribute('data-theme') !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
        const themeData = dark ? (userTheme.themeDark || userTheme.theme) : userTheme.theme;
        if (themeData) {
          for (const [key, val] of Object.entries(themeData)) {
            if (THEME_CSS_MAP[key]) document.documentElement.style.setProperty(THEME_CSS_MAP[key], val);
          }
        }
        if (userTheme.nodeColors) {
          if (window.ROLE_COLORS) Object.assign(window.ROLE_COLORS, userTheme.nodeColors);
          if (window.ROLE_STYLE) {
            for (const [role, color] of Object.entries(userTheme.nodeColors)) {
              if (window.ROLE_STYLE[role]) window.ROLE_STYLE[role].color = color;
            }
          }
        }
      }
    } catch {}
  });
})();
