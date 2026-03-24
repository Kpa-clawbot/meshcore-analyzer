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
      accent: '#4a9eff', navBg: '#0f0f23', navText: '#ffffff', background: '#f4f5f7', text: '#1a1a2e',
      statusGreen: '#22c55e', statusYellow: '#eab308', statusRed: '#ef4444',
      accentHover: '#6db3ff', navBg2: '#1a1a2e', navTextMuted: '#cbd5e1', textMuted: '#5b6370', border: '#e2e5ea',
      surface1: '#ffffff', surface2: '#ffffff', cardBg: '#ffffff', contentBg: '#f4f5f7',
      detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#f9fafb', rowHover: '#eef2ff', selectedBg: '#dbeafe',
      font: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      mono: '"SF Mono", "Fira Code", "Cascadia Code", Consolas, monospace',
    },
    themeDark: {
      accent: '#4a9eff', navBg: '#0f0f23', navText: '#ffffff', background: '#0f0f23', text: '#e2e8f0',
      statusGreen: '#22c55e', statusYellow: '#eab308', statusRed: '#ef4444',
      accentHover: '#6db3ff', navBg2: '#1a1a2e', navTextMuted: '#cbd5e1', textMuted: '#a8b8cc', border: '#334155',
      surface1: '#1a1a2e', surface2: '#232340', cardBg: '#1a1a2e', contentBg: '#0f0f23',
      detailBg: '#232340', inputBg: '#1e1e34', rowStripe: '#1e1e34', rowHover: '#2d2d50', selectedBg: '#1e3a5f',
      font: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      mono: '"SF Mono", "Fira Code", "Cascadia Code", Consolas, monospace',
    },
    nodeColors: {
      repeater: '#dc2626',
      companion: '#2563eb',
      room: '#16a34a',
      sensor: '#d97706',
      observer: '#8b5cf6'
    },
    typeColors: {
      ADVERT: '#22c55e', GRP_TXT: '#3b82f6', TXT_MSG: '#f59e0b', ACK: '#6b7280',
      REQUEST: '#a855f7', RESPONSE: '#06b6d4', TRACE: '#ec4899', PATH: '#14b8a6',
      ANON_REQ: '#f43f5e'
    },
    home: {
      heroTitle: 'MeshCore Analyzer',
      heroSubtitle: 'Find your nodes to start monitoring them.',
      steps: [
        { emoji: '💬', title: 'Join the Bay Area MeshCore Discord', description: 'The community Discord is the best place to get help and find local mesh enthusiasts.' },
        { emoji: '🔵', title: 'Connect via Bluetooth', description: 'Flash BLE companion firmware and pair with your device.' },
        { emoji: '📻', title: 'Set the right frequency preset', description: 'Match the frequency preset used by your local mesh community.' },
        { emoji: '📡', title: 'Advertise yourself', description: 'Send an ADVERT so repeaters and observers can see you.' },
        { emoji: '🔁', title: 'Check "Heard N repeats"', description: 'Verify your node is being relayed through the mesh.' },
        { emoji: '📍', title: 'Repeaters near you?', description: 'Check the map for nearby repeaters and coverage.' }
      ],
      checklist: [],
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
    // Basic
    accent: '--accent',
    navBg: '--nav-bg',
    navText: '--nav-text',
    background: '--surface-0',
    text: '--text',
    statusGreen: '--status-green',
    statusYellow: '--status-yellow',
    statusRed: '--status-red',
    // Advanced (derived from basic by default)
    accentHover: '--accent-hover',
    navBg2: '--nav-bg2',
    navTextMuted: '--nav-text-muted',
    textMuted: '--text-muted',
    border: '--border',
    surface1: '--surface-1',
    surface2: '--surface-2',
    cardBg: '--card-bg',
    contentBg: '--content-bg',
    detailBg: '--detail-bg',
    inputBg: '--input-bg',
    rowStripe: '--row-stripe',
    rowHover: '--row-hover',
    selectedBg: '--selected-bg',
    font: '--font',
    mono: '--mono',
  };

  /* ── Theme Presets ── */
  const THEME_COLOR_KEYS = ['accent', 'navBg', 'navText', 'background', 'text', 'statusGreen', 'statusYellow', 'statusRed',
    'accentHover', 'navBg2', 'navTextMuted', 'textMuted', 'border', 'surface1', 'surface2', 'cardBg', 'contentBg',
    'detailBg', 'inputBg', 'rowStripe', 'rowHover', 'selectedBg'];

  const PRESETS = {
    default: {
      name: 'Default', desc: 'MeshCore blue',
      preview: ['#4a9eff', '#0f0f23', '#f4f5f7', '#1a1a2e', '#22c55e'],
      light: {
        accent: '#4a9eff', navBg: '#0f0f23', navText: '#ffffff', background: '#f4f5f7', text: '#1a1a2e',
        statusGreen: '#22c55e', statusYellow: '#eab308', statusRed: '#ef4444',
        accentHover: '#6db3ff', navBg2: '#1a1a2e', navTextMuted: '#cbd5e1', textMuted: '#5b6370', border: '#e2e5ea',
        surface1: '#ffffff', surface2: '#ffffff', cardBg: '#ffffff', contentBg: '#f4f5f7',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#f9fafb', rowHover: '#eef2ff', selectedBg: '#dbeafe',
      },
      dark: {
        accent: '#4a9eff', navBg: '#0f0f23', navText: '#ffffff', background: '#0f0f23', text: '#e2e8f0',
        statusGreen: '#22c55e', statusYellow: '#eab308', statusRed: '#ef4444',
        accentHover: '#6db3ff', navBg2: '#1a1a2e', navTextMuted: '#cbd5e1', textMuted: '#a8b8cc', border: '#334155',
        surface1: '#1a1a2e', surface2: '#232340', cardBg: '#1a1a2e', contentBg: '#0f0f23',
        detailBg: '#232340', inputBg: '#1e1e34', rowStripe: '#1e1e34', rowHover: '#2d2d50', selectedBg: '#1e3a5f',
      }
    },
    ocean: {
      name: 'Ocean', desc: 'Deep blues & teals',
      preview: ['#0077b6', '#03045e', '#f0f7fa', '#48cae4', '#15803d'],
      light: {
        accent: '#0077b6', navBg: '#03045e', navText: '#ffffff', background: '#f0f7fa', text: '#0a1628',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#0096d6', navBg2: '#023e8a', navTextMuted: '#90caf9', textMuted: '#4a6580', border: '#c8dce8',
        surface1: '#ffffff', surface2: '#e8f4f8', cardBg: '#ffffff', contentBg: '#f0f7fa',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#f5fafd', rowHover: '#e0f0f8', selectedBg: '#bde0fe',
      },
      dark: {
        accent: '#48cae4', navBg: '#03045e', navText: '#ffffff', background: '#0a1929', text: '#e0e7ef',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#76d7ea', navBg2: '#012a4a', navTextMuted: '#90caf9', textMuted: '#8eafc4', border: '#1e3a5f',
        surface1: '#0d2137', surface2: '#122d4a', cardBg: '#0d2137', contentBg: '#0a1929',
        detailBg: '#122d4a', inputBg: '#0d2137', rowStripe: '#0d2137', rowHover: '#153450', selectedBg: '#1a4570',
      }
    },
    forest: {
      name: 'Forest', desc: 'Greens & earth tones',
      preview: ['#2d6a4f', '#1b3a2d', '#f2f7f4', '#52b788', '#15803d'],
      light: {
        accent: '#2d6a4f', navBg: '#1b3a2d', navText: '#ffffff', background: '#f2f7f4', text: '#1a2e24',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#40916c', navBg2: '#2d6a4f', navTextMuted: '#a3c4b5', textMuted: '#557063', border: '#c8dcd2',
        surface1: '#ffffff', surface2: '#e8f0eb', cardBg: '#ffffff', contentBg: '#f2f7f4',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#f5faf7', rowHover: '#e4f0e8', selectedBg: '#c2e0cc',
      },
      dark: {
        accent: '#52b788', navBg: '#1b3a2d', navText: '#ffffff', background: '#0d1f17', text: '#d8e8df',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#74c69d', navBg2: '#14532d', navTextMuted: '#86b89a', textMuted: '#8aac9a', border: '#2d4a3a',
        surface1: '#162e23', surface2: '#1d3a2d', cardBg: '#162e23', contentBg: '#0d1f17',
        detailBg: '#1d3a2d', inputBg: '#162e23', rowStripe: '#162e23', rowHover: '#1f4030', selectedBg: '#265940',
      }
    },
    sunset: {
      name: 'Sunset', desc: 'Warm oranges & ambers',
      preview: ['#c2410c', '#431407', '#fef7f2', '#fb923c', '#dc2626'],
      light: {
        accent: '#c2410c', navBg: '#431407', navText: '#ffffff', background: '#fef7f2', text: '#1c0f06',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#ea580c', navBg2: '#7c2d12', navTextMuted: '#fdba74', textMuted: '#6b5344', border: '#e8d5c8',
        surface1: '#ffffff', surface2: '#fef0e6', cardBg: '#ffffff', contentBg: '#fef7f2',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#fefaf7', rowHover: '#fef0e0', selectedBg: '#fed7aa',
      },
      dark: {
        accent: '#fb923c', navBg: '#431407', navText: '#ffffff', background: '#1a0f08', text: '#f0ddd0',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#fdba74', navBg2: '#7c2d12', navTextMuted: '#c2855a', textMuted: '#b09080', border: '#4a2a18',
        surface1: '#261a10', surface2: '#332214', cardBg: '#261a10', contentBg: '#1a0f08',
        detailBg: '#332214', inputBg: '#261a10', rowStripe: '#261a10', rowHover: '#3a2818', selectedBg: '#5c3518',
      }
    },
    mono: {
      name: 'Monochrome', desc: 'Pure grays, no color',
      preview: ['#525252', '#171717', '#f5f5f5', '#a3a3a3', '#737373'],
      light: {
        accent: '#525252', navBg: '#171717', navText: '#ffffff', background: '#f5f5f5', text: '#171717',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#737373', navBg2: '#262626', navTextMuted: '#a3a3a3', textMuted: '#525252', border: '#d4d4d4',
        surface1: '#ffffff', surface2: '#fafafa', cardBg: '#ffffff', contentBg: '#f5f5f5',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#fafafa', rowHover: '#efefef', selectedBg: '#e5e5e5',
      },
      dark: {
        accent: '#a3a3a3', navBg: '#171717', navText: '#ffffff', background: '#0a0a0a', text: '#e5e5e5',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#d4d4d4', navBg2: '#1a1a1a', navTextMuted: '#737373', textMuted: '#a3a3a3', border: '#333333',
        surface1: '#171717', surface2: '#1f1f1f', cardBg: '#171717', contentBg: '#0a0a0a',
        detailBg: '#1f1f1f', inputBg: '#171717', rowStripe: '#141414', rowHover: '#222222', selectedBg: '#2a2a2a',
      }
    },
    highContrast: {
      name: 'High Contrast', desc: 'WCAG AAA, max readability',
      preview: ['#0050a0', '#000000', '#ffffff', '#66b3ff', '#006400'],
      light: {
        accent: '#0050a0', navBg: '#000000', navText: '#ffffff', background: '#ffffff', text: '#000000',
        statusGreen: '#006400', statusYellow: '#7a5900', statusRed: '#b30000',
        accentHover: '#0068cc', navBg2: '#1a1a1a', navTextMuted: '#e0e0e0', textMuted: '#333333', border: '#000000',
        surface1: '#ffffff', surface2: '#f0f0f0', cardBg: '#ffffff', contentBg: '#ffffff',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#f0f0f0', rowHover: '#e0e8f5', selectedBg: '#cce0ff',
      },
      dark: {
        accent: '#66b3ff', navBg: '#000000', navText: '#ffffff', background: '#000000', text: '#ffffff',
        statusGreen: '#66ff66', statusYellow: '#ffff00', statusRed: '#ff6666',
        accentHover: '#99ccff', navBg2: '#0a0a0a', navTextMuted: '#cccccc', textMuted: '#cccccc', border: '#ffffff',
        surface1: '#111111', surface2: '#1a1a1a', cardBg: '#111111', contentBg: '#000000',
        detailBg: '#1a1a1a', inputBg: '#111111', rowStripe: '#0d0d0d', rowHover: '#1a2a3a', selectedBg: '#003366',
      },
      nodeColors: { repeater: '#ff0000', companion: '#0066ff', room: '#009900', sensor: '#cc8800', observer: '#9933ff' },
      typeColors: {
        ADVERT: '#009900', GRP_TXT: '#0066ff', TXT_MSG: '#cc8800', ACK: '#666666',
        REQUEST: '#9933ff', RESPONSE: '#0099cc', TRACE: '#cc0066', PATH: '#009999', ANON_REQ: '#cc3355'
      }
    },
    midnight: {
      name: 'Midnight', desc: 'Deep purples & indigos',
      preview: ['#7c3aed', '#1e1045', '#f5f3ff', '#a78bfa', '#15803d'],
      light: {
        accent: '#7c3aed', navBg: '#1e1045', navText: '#ffffff', background: '#f5f3ff', text: '#1a1040',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#8b5cf6', navBg2: '#2e1065', navTextMuted: '#c4b5fd', textMuted: '#5b5075', border: '#d8d0e8',
        surface1: '#ffffff', surface2: '#ede9fe', cardBg: '#ffffff', contentBg: '#f5f3ff',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#faf8ff', rowHover: '#ede9fe', selectedBg: '#ddd6fe',
      },
      dark: {
        accent: '#a78bfa', navBg: '#1e1045', navText: '#ffffff', background: '#0f0a24', text: '#e2ddf0',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#c4b5fd', navBg2: '#2e1065', navTextMuted: '#9d8abf', textMuted: '#9a90b0', border: '#352a55',
        surface1: '#1a1338', surface2: '#221a48', cardBg: '#1a1338', contentBg: '#0f0a24',
        detailBg: '#221a48', inputBg: '#1a1338', rowStripe: '#1a1338', rowHover: '#2a2050', selectedBg: '#352a6a',
      }
    },
    ember: {
      name: 'Ember', desc: 'Warm red/orange, cyberpunk',
      preview: ['#dc2626', '#1a0a0a', '#faf5f5', '#ef4444', '#15803d'],
      light: {
        accent: '#dc2626', navBg: '#1a0a0a', navText: '#ffffff', background: '#faf5f5', text: '#1a0a0a',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#ef4444', navBg2: '#2a1010', navTextMuted: '#f0a0a0', textMuted: '#6b4444', border: '#e0c8c8',
        surface1: '#ffffff', surface2: '#faf0f0', cardBg: '#ffffff', contentBg: '#faf5f5',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#fdf8f8', rowHover: '#fce8e8', selectedBg: '#fecaca',
      },
      dark: {
        accent: '#ef4444', navBg: '#1a0505', navText: '#ffffff', background: '#0d0505', text: '#f0dada',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#f87171', navBg2: '#2a0a0a', navTextMuted: '#c07070', textMuted: '#b09090', border: '#4a2020',
        surface1: '#1a0d0d', surface2: '#261414', cardBg: '#1a0d0d', contentBg: '#0d0505',
        detailBg: '#261414', inputBg: '#1a0d0d', rowStripe: '#1a0d0d', rowHover: '#301818', selectedBg: '#4a1a1a',
      }
    }
  };

  function detectActivePreset() {
    for (var id in PRESETS) {
      var p = PRESETS[id];
      var match = true;
      for (var i = 0; i < THEME_COLOR_KEYS.length; i++) {
        var k = THEME_COLOR_KEYS[i];
        if (state.theme[k] !== p.light[k] || state.themeDark[k] !== p.dark[k]) { match = false; break; }
      }
      if (match && p.nodeColors) {
        for (var nk in p.nodeColors) { if (state.nodeColors[nk] !== p.nodeColors[nk]) { match = false; break; } }
      }
      if (match && p.typeColors) {
        for (var tk in p.typeColors) { if (state.typeColors[tk] !== p.typeColors[tk]) { match = false; break; } }
      }
      if (match) return id;
    }
    return null;
  }

  function renderPresets(container) {
    var active = detectActivePreset();
    var html = '<div style="margin-bottom:16px">' +
      '<p class="cust-section-title">Theme Presets</p>' +
      '<div style="display:flex;gap:8px;flex-wrap:wrap">';
    for (var id in PRESETS) {
      var p = PRESETS[id];
      var isActive = id === active;
      var dots = '';
      for (var di = 0; di < p.preview.length; di++) {
        dots += '<span style="display:inline-block;width:12px;height:12px;border-radius:50%;background:' + p.preview[di] + ';border:1px solid rgba(128,128,128,0.3)"></span>';
      }
      html += '<button class="cust-preset-btn" data-preset="' + id + '" style="' +
        'display:flex;flex-direction:column;align-items:center;gap:4px;padding:8px 10px;' +
        'border:2px solid ' + (isActive ? 'var(--accent)' : 'var(--border)') + ';' +
        'border-radius:8px;background:' + (isActive ? 'var(--selected-bg)' : 'var(--surface-1)') + ';' +
        'cursor:pointer;min-width:72px;color:var(--text)">' +
        '<div style="display:flex;gap:3px">' + dots + '</div>' +
        '<span style="font-size:11px;font-weight:' + (isActive ? '700' : '500') + '">' + esc(p.name) + '</span>' +
        '<span style="font-size:9px;color:var(--text-muted)">' + esc(p.desc) + '</span>' +
      '</button>';
    }
    html += '</div></div>';
    return html;
  }

  function applyPreset(id, container) {
    var p = PRESETS[id];
    if (!p) return;
    // Apply light theme colors
    for (var i = 0; i < THEME_COLOR_KEYS.length; i++) {
      var k = THEME_COLOR_KEYS[i];
      state.theme[k] = p.light[k];
      state.themeDark[k] = p.dark[k];
    }
    // Apply node/type colors
    if (p.nodeColors) {
      Object.assign(state.nodeColors, p.nodeColors);
      if (window.ROLE_COLORS) Object.assign(window.ROLE_COLORS, p.nodeColors);
      if (window.ROLE_STYLE) {
        for (var role in p.nodeColors) {
          if (window.ROLE_STYLE[role]) window.ROLE_STYLE[role].color = p.nodeColors[role];
        }
      }
    } else {
      // Reset to defaults
      Object.assign(state.nodeColors, DEFAULTS.nodeColors);
      if (window.ROLE_COLORS) Object.assign(window.ROLE_COLORS, DEFAULTS.nodeColors);
    }
    if (p.typeColors) {
      Object.assign(state.typeColors, p.typeColors);
      if (window.TYPE_COLORS) Object.assign(window.TYPE_COLORS, p.typeColors);
    } else {
      Object.assign(state.typeColors, DEFAULTS.typeColors);
      if (window.TYPE_COLORS) Object.assign(window.TYPE_COLORS, DEFAULTS.typeColors);
    }
    applyThemePreview();
    if (window.syncBadgeColors) window.syncBadgeColors();
    window.dispatchEvent(new CustomEvent('theme-changed'));
    autoSave();
    render(container);
  }

  const BASIC_KEYS = ['accent', 'navBg', 'navText', 'background', 'text', 'statusGreen', 'statusYellow', 'statusRed'];
  const ADVANCED_KEYS = ['accentHover', 'navBg2', 'navTextMuted', 'textMuted', 'border', 'surface1', 'surface2', 'cardBg', 'contentBg', 'detailBg', 'inputBg', 'rowStripe', 'rowHover', 'selectedBg'];
  const FONT_KEYS = ['font', 'mono'];

  const THEME_LABELS = {
    accent: 'Brand Color',
    navBg: 'Navigation',
    navText: 'Nav Text',
    background: 'Background',
    text: 'Text',
    statusGreen: 'Healthy',
    statusYellow: 'Warning',
    statusRed: 'Error',
    accentHover: 'Accent Hover',
    navBg2: 'Nav Gradient End',
    navTextMuted: 'Nav Muted Text',
    textMuted: 'Muted Text',
    border: 'Borders',
    surface1: 'Cards',
    surface2: 'Panels',
    cardBg: 'Card Fill',
    contentBg: 'Content Area',
    detailBg: 'Detail Panels',
    inputBg: 'Inputs',
    rowStripe: 'Table Stripe',
    rowHover: 'Row Hover',
    selectedBg: 'Selected',
    font: 'Body Font',
    mono: 'Mono Font',
  };

  const THEME_HINTS = {
    accent: 'Buttons, links, active tabs, badges, charts — your primary brand color',
    navBg: 'Top navigation bar',
    navText: 'Nav bar text, links, brand name, buttons',
    background: 'Main page background',
    text: 'Primary text — muted text auto-derives',
    statusGreen: 'Healthy/online indicators',
    statusYellow: 'Warning/degraded + hop conflicts',
    statusRed: 'Error/offline indicators',
    accentHover: 'Hover state for accent elements',
    navBg2: 'Darker end of nav gradient',
    navTextMuted: 'Inactive nav links, nav buttons',
    textMuted: 'Labels, timestamps, secondary text',
    border: 'Dividers, table borders, card borders',
    surface1: 'Card and panel backgrounds',
    surface2: 'Nested surfaces, secondary panels',
    cardBg: 'Detail panels, modals',
    contentBg: 'Content area behind cards',
    detailBg: 'Modal, packet detail, side panels',
    inputBg: 'Text inputs, dropdowns',
    rowStripe: 'Alternating table rows',
    rowHover: 'Table row hover',
    selectedBg: 'Selected/active rows',
    font: 'System font stack for body text',
    mono: 'Monospace font for hex, code, hashes',
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

  const TYPE_LABELS = {
    ADVERT: 'Advertisement', GRP_TXT: 'Channel Message', TXT_MSG: 'Direct Message', ACK: 'Acknowledgment',
    REQUEST: 'Request', RESPONSE: 'Response', TRACE: 'Traceroute', PATH: 'Path',
    ANON_REQ: 'Anonymous Request'
  };
  const TYPE_HINTS = {
    ADVERT: 'Node advertisements — map, feed, packet list',
    GRP_TXT: 'Group/channel messages — map, feed, channels',
    TXT_MSG: 'Direct messages — map, feed',
    ACK: 'Acknowledgments — packet list',
    REQUEST: 'Requests — packet list, feed',
    RESPONSE: 'Responses — packet list',
    TRACE: 'Traceroute — map, traces page',
    PATH: 'Path packets — packet list',
    ANON_REQ: 'Encrypted anonymous requests — sender identity hidden via ephemeral key'
  };
  const TYPE_EMOJI = {
    ADVERT: '📡', GRP_TXT: '💬', TXT_MSG: '✉️', ACK: '✓', REQUEST: '❓', RESPONSE: '📨', TRACE: '🔍', PATH: '🛤️', ANON_REQ: '🕵️'
  };

  // Current state
  let state = {};

  function deepClone(o) { return JSON.parse(JSON.stringify(o)); }

  function initState() {
    const cfg = window.SITE_CONFIG || {};
    // Merge: DEFAULTS → server config → localStorage saved values
    var local = {};
    try { var s = localStorage.getItem('meshcore-user-theme'); if (s) local = JSON.parse(s); } catch {}
    state = {
      branding: Object.assign({}, DEFAULTS.branding, cfg.branding || {}, local.branding || {}),
      theme: Object.assign({}, DEFAULTS.theme, cfg.theme || {}, local.theme || {}),
      themeDark: Object.assign({}, DEFAULTS.themeDark, cfg.themeDark || {}, local.themeDark || {}),
      nodeColors: Object.assign({}, DEFAULTS.nodeColors, cfg.nodeColors || {}, local.nodeColors || {}),
      typeColors: Object.assign({}, DEFAULTS.typeColors, cfg.typeColors || {}, local.typeColors || {}),
      home: {
        heroTitle: (local.home && local.home.heroTitle) || (cfg.home && cfg.home.heroTitle) || DEFAULTS.home.heroTitle,
        heroSubtitle: (local.home && local.home.heroSubtitle) || (cfg.home && cfg.home.heroSubtitle) || DEFAULTS.home.heroSubtitle,
        steps: deepClone((local.home && local.home.steps) || (cfg.home && cfg.home.steps) || DEFAULTS.home.steps),
        checklist: deepClone((local.home && local.home.checklist) || (cfg.home && cfg.home.checklist) || DEFAULTS.home.checklist),
        footerLinks: deepClone((local.home && local.home.footerLinks) || (cfg.home && cfg.home.footerLinks) || DEFAULTS.home.footerLinks)
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
    // Derived vars that reference other vars — need explicit override
    if (t.background) {
      document.documentElement.style.setProperty('--content-bg', t.background);
    }
    if (t.surface1) {
      document.documentElement.style.setProperty('--card-bg', t.surface1);
    }
    // Force nav bar to re-render gradient
    var nav = document.querySelector('.top-nav');
    if (nav) {
      nav.style.background = 'none';
      void nav.offsetHeight;
      nav.style.background = '';
    }
    // Sync badge CSS from TYPE_COLORS
    if (window.syncBadgeColors) window.syncBadgeColors();
  }

  function applyTypeColorCSS() {
    if (window.syncBadgeColors) window.syncBadgeColors();
  }

  // Auto-save to localStorage on every change
  let _autoSaveTimer = null;
  function autoSave() {
    if (_autoSaveTimer) clearTimeout(_autoSaveTimer);
    _autoSaveTimer = setTimeout(function() {
      _autoSaveTimer = null;
      try {
        var data = buildExport();
        localStorage.setItem('meshcore-user-theme', JSON.stringify(data));
        // Sync to SITE_CONFIG so live pages (home, etc.) pick up changes
        if (window.SITE_CONFIG) {
          if (state.branding) window.SITE_CONFIG.branding = Object.assign(window.SITE_CONFIG.branding || {}, state.branding);
          if (state.home) window.SITE_CONFIG.home = deepClone(state.home);
        }
        // Re-render current page to reflect home/branding changes
        window.dispatchEvent(new HashChangeEvent('hashchange'));
      } catch (e) { console.error('[customize] autoSave error:', e); }
    }, 500);
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
      .cust-overlay { position: fixed; top: 56px; right: 12px; z-index: 1050; width: 480px; height: calc(100vh - 68px);
        background: var(--card-bg); border: 1px solid var(--border); border-radius: 10px;
        box-shadow: 0 8px 32px rgba(0,0,0,0.3); display: flex; flex-direction: column;
        resize: both; min-width: 320px; min-height: 300px; overflow: hidden; }
      .cust-overlay.hidden { display: none; }
      .cust-header { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px;
        border-bottom: 1px solid var(--border); cursor: move; user-select: none; flex-shrink: 0; }
      .cust-header h2 { margin: 0; font-size: 15px; }
      .cust-close { background: none; border: none; font-size: 18px; cursor: pointer; color: var(--text-muted); padding: 4px 8px; border-radius: 4px; }
      .cust-close:hover { background: var(--surface-3); color: var(--text); }
      .cust-body { flex: 1; overflow-y: auto; min-height: 0; }
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
      .cust-list-item { display: flex; flex-direction: column; gap: 4px; margin-bottom: 8px; padding: 8px;
        background: var(--surface-1); border: 1px solid var(--border); border-radius: 6px; }
      .cust-list-row { display: flex; gap: 6px; align-items: center; }
      .cust-list-item input { flex: 1; padding: 5px 8px; border: 1px solid var(--border); border-radius: 4px;
        font-size: 12px; background: var(--input-bg); color: var(--text); min-width: 0; }
      .cust-list-item textarea { width: 100%; padding: 5px 8px; border: 1px solid var(--border); border-radius: 4px;
        font-size: 11px; font-family: var(--mono); background: var(--input-bg); color: var(--text); resize: vertical; box-sizing: border-box; }
      .cust-list-item textarea:focus, .cust-list-item input:focus { outline: none; border-color: var(--accent); }
      .cust-md-hint { font-size: 9px; color: var(--text-muted); margin-top: 2px; }
      .cust-md-hint code { background: var(--surface-2); padding: 0 3px; border-radius: 2px; font-size: 9px; }
      .cust-list-item .cust-emoji-input { max-width: 40px; text-align: center; flex: 0 0 40px; }
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
      { id: 'nodes', label: '🎯', title: 'Colors' },
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

  function renderColorRow(key, val, def, dataAttr) {
    var isFont = key === 'font' || key === 'mono';
    var inputHtml = isFont
      ? '<input type="text" data-' + dataAttr + '="' + key + '" value="' + escAttr(val) + '" style="width:160px;font-size:11px;font-family:var(--mono);padding:4px 6px;border:1px solid var(--border);border-radius:4px;background:var(--input-bg);color:var(--text)">'
      : '<input type="color" data-' + dataAttr + '="' + key + '" value="' + val + '">' +
        '<span class="cust-hex" data-hex="' + key + '">' + val + '</span>';
    return '<div class="cust-color-row">' +
      '<div><label>' + THEME_LABELS[key] + '</label>' +
      '<div class="cust-hint">' + (THEME_HINTS[key] || '') + '</div></div>' +
      inputHtml +
      (val !== def ? '<button class="cust-reset-btn" data-reset-theme="' + key + '">Reset</button>' : '') +
    '</div>';
  }

  function renderTheme() {
    var dark = isDarkMode();
    var modeLabel = dark ? '🌙 Dark Mode' : '☀️ Light Mode';
    var defs = activeDefaults();
    var current = activeTheme();

    var basicRows = '';
    for (var i = 0; i < BASIC_KEYS.length; i++) {
      var key = BASIC_KEYS[i];
      basicRows += renderColorRow(key, current[key] || defs[key] || '#000000', defs[key] || '#000000', 'theme');
    }

    var advancedRows = '';
    for (var j = 0; j < ADVANCED_KEYS.length; j++) {
      var akey = ADVANCED_KEYS[j];
      advancedRows += renderColorRow(akey, current[akey] || defs[akey] || '#000000', defs[akey] || '#000000', 'theme');
    }

    var fontRows = '';
    for (var f = 0; f < FONT_KEYS.length; f++) {
      var fkey = FONT_KEYS[f];
      fontRows += renderColorRow(fkey, current[fkey] || defs[fkey] || '', defs[fkey] || '', 'theme');
    }

    return '<div class="cust-panel' + (activeTab === 'theme' ? ' active' : '') + '" data-panel="theme">' +
      renderPresets() +
      '<p class="cust-section-title">' + modeLabel + '</p>' +
      '<p style="font-size:11px;color:var(--text-muted);margin:0 0 10px">Toggle ☀️/🌙 in nav to edit the other mode.</p>' +
      basicRows +
      '<details class="cust-advanced"><summary style="font-size:12px;font-weight:600;cursor:pointer;color:var(--text-muted);margin:12px 0 8px">Advanced (' + ADVANCED_KEYS.length + ' options)</summary>' +
      advancedRows +
      '</details>' +
      '<details class="cust-fonts" style="margin-top:12px"><summary style="font-size:12px;font-weight:600;cursor:pointer;color:var(--text-muted);margin:12px 0 8px">Fonts</summary>' +
      fontRows +
      '</details>' +
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
    var typeRows = '';
    for (var tkey in TYPE_LABELS) {
      var tval = state.typeColors[tkey];
      var tdef = DEFAULTS.typeColors[tkey];
      typeRows += '<div class="cust-color-row">' +
        '<div><label>' + (TYPE_EMOJI[tkey] || '') + ' ' + TYPE_LABELS[tkey] + '</label>' +
        '<div class="cust-hint">' + (TYPE_HINTS[tkey] || '') + '</div></div>' +
        '<input type="color" data-type-color="' + tkey + '" value="' + tval + '">' +
        '<span class="cust-node-dot" style="background:' + tval + '" data-tdot="' + tkey + '"></span>' +
        '<span class="cust-hex" data-thex="' + tkey + '">' + tval + '</span>' +
        (tval !== tdef ? '<button class="cust-reset-btn" data-reset-type="' + tkey + '">Reset</button>' : '') +
      '</div>';
    }
    var heatOpacity = parseFloat(localStorage.getItem('meshcore-heatmap-opacity'));
    if (isNaN(heatOpacity)) heatOpacity = 0.25;
    var heatPct = Math.round(heatOpacity * 100);
    return '<div class="cust-panel' + (activeTab === 'nodes' ? ' active' : '') + '" data-panel="nodes">' +
      '<p class="cust-section-title">Node Role Colors</p>' + rows +
      '<hr style="border:none;border-top:1px solid var(--border);margin:16px 0">' +
      '<p class="cust-section-title">Packet Type Colors</p>' + typeRows +
      '<hr style="border:none;border-top:1px solid var(--border);margin:16px 0">' +
      '<p class="cust-section-title">Heatmap</p>' +
      '<div class="cust-color-row">' +
        '<div><label>🔥 Opacity</label>' +
        '<div class="cust-hint">Controls how opaque the heatmap overlay appears on the live map (0–100%)</div></div>' +
        '<input type="range" id="custHeatOpacity" min="0" max="100" value="' + heatPct + '" style="width:120px;cursor:pointer">' +
        '<span id="custHeatOpacityVal" style="font-family:var(--mono);font-size:12px;color:var(--text-muted);min-width:36px">' + heatPct + '%</span>' +
      '</div>' +
    '</div>';
  }

  function renderHome() {
    var h = state.home;
    var stepsHtml = h.steps.map(function (s, i) {
      return '<div class="cust-list-item" data-step="' + i + '">' +
        '<div class="cust-list-row">' +
          '<input class="cust-emoji-input" data-step-field="emoji" data-idx="' + i + '" value="' + escAttr(s.emoji) + '" placeholder="📡">' +
          '<input data-step-field="title" data-idx="' + i + '" value="' + escAttr(s.title) + '" placeholder="Title">' +
          '<button class="cust-list-btn" data-move-step="' + i + '" data-dir="up" title="Move up">↑</button>' +
          '<button class="cust-list-btn" data-move-step="' + i + '" data-dir="down" title="Move down">↓</button>' +
          '<button class="cust-list-btn danger" data-rm-step="' + i + '" title="Remove">✕</button>' +
        '</div>' +
        '<textarea data-step-field="description" data-idx="' + i + '" placeholder="Description" rows="2">' + esc(s.description) + '</textarea>' +
        '<div class="cust-md-hint">Markdown: <code>**bold**</code> <code>*italic*</code> <code>`code`</code> <code>[text](url)</code> <code>- list</code></div>' +
      '</div>';
    }).join('');

    var checkHtml = h.checklist.map(function (c, i) {
      return '<div class="cust-list-item" data-check="' + i + '">' +
        '<div class="cust-list-row">' +
          '<input data-check-field="question" data-idx="' + i + '" value="' + escAttr(c.question) + '" placeholder="Question">' +
          '<button class="cust-list-btn danger" data-rm-check="' + i + '" title="Remove">✕</button>' +
        '</div>' +
        '<textarea data-check-field="answer" data-idx="' + i + '" placeholder="Answer" rows="2">' + esc(c.answer) + '</textarea>' +
        '<div class="cust-md-hint">Markdown: <code>**bold**</code> <code>*italic*</code> <code>`code`</code> <code>[text](url)</code> <code>- list</code></div>' +
      '</div>';
    }).join('');

    var linksHtml = h.footerLinks.map(function (l, i) {
      return '<div class="cust-list-item" data-link="' + i + '">' +
        '<div class="cust-list-row">' +
          '<input data-link-field="label" data-idx="' + i + '" value="' + escAttr(l.label) + '" placeholder="Label">' +
          '<button class="cust-list-btn danger" data-rm-link="' + i + '" title="Remove">✕</button>' +
        '</div>' +
        '<input data-link-field="url" data-idx="' + i + '" value="' + escAttr(l.url) + '" placeholder="URL">' +
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

    // Packet type colors
    var tc = {};
    for (var tck in DEFAULTS.typeColors) {
      if (state.typeColors[tck] !== DEFAULTS.typeColors[tck]) tc[tck] = state.typeColors[tck];
    }
    if (Object.keys(tc).length) out.typeColors = tc;

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
      '<p class="cust-section-title">Admin</p>' +
      '<p style="font-size:12px;color:var(--text-muted);margin-bottom:8px">Download or import a theme file. Admins place it as <code>theme.json</code> next to the server.</p>' +
      '<div class="cust-export-btns" style="margin-bottom:12px">' +
        '<button class="cust-dl-btn" id="custDownload">💾 Download theme.json</button>' +
        '<button class="cust-dl-btn" id="custImportFile">📂 Import File</button>' +
        '<input type="file" id="custImportInput" accept=".json,application/json" style="display:none">' +
        '<button class="cust-copy-btn" id="custCopy">📋 Copy</button>' +
      '</div>' +
      '<details style="margin-top:8px"><summary style="font-size:12px;font-weight:600;cursor:pointer;color:var(--text-muted)">Raw JSON</summary>' +
      '<textarea class="cust-export-area" id="custExportJson" style="margin-top:8px">' + esc(json) + '</textarea>' +
      '</details>' +
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

    // Preset buttons
    container.querySelectorAll('.cust-preset-btn').forEach(function (btn) {
      btn.addEventListener('click', function () {
        applyPreset(btn.dataset.preset, container);
      });
    });

    // Text inputs (branding + home hero)
    container.querySelectorAll('input[data-key]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        var parts = inp.dataset.key.split('.');
        if (parts.length === 2) {
          state[parts[0]][parts[1]] = inp.value;
          autoSave();
        }
        // Live DOM updates for branding
        if (inp.dataset.key === 'branding.siteName') {
          var brandEl = document.querySelector('.brand-text');
          if (brandEl) brandEl.textContent = inp.value;
          document.title = inp.value;
        }
        if (inp.dataset.key === 'branding.logoUrl') {
          var iconEl = document.querySelector('.brand-icon');
          if (iconEl) {
            if (inp.value) { iconEl.innerHTML = '<img src="' + inp.value + '" style="height:24px" onerror="this.style.display=\'none\'">'; }
            else { iconEl.textContent = '📡'; }
          }
        }
        if (inp.dataset.key === 'branding.faviconUrl') {
          var link = document.querySelector('link[rel="icon"]');
          if (link && inp.value) link.href = inp.value;
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
        applyThemePreview(); autoSave();
      });
    });

    // Theme reset buttons
    container.querySelectorAll('[data-reset-theme]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var key = btn.dataset.resetTheme;
        var themeKey = isDarkMode() ? 'themeDark' : 'theme';
        state[themeKey][key] = activeDefaults()[key];
        applyThemePreview(); autoSave();
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
        // Trigger re-render of current page
        window.dispatchEvent(new CustomEvent('theme-changed')); autoSave();
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

    // Packet type color pickers
    container.querySelectorAll('input[data-type-color]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        var key = inp.dataset.typeColor;
        state.typeColors[key] = inp.value;
        if (window.TYPE_COLORS) window.TYPE_COLORS[key] = inp.value;
        if (window.syncBadgeColors) window.syncBadgeColors();
        window.dispatchEvent(new CustomEvent('theme-changed')); autoSave();
        var dot = container.querySelector('[data-tdot="' + key + '"]');
        if (dot) dot.style.background = inp.value;
        var hex = container.querySelector('[data-thex="' + key + '"]');
        if (hex) hex.textContent = inp.value;
      });
    });
    container.querySelectorAll('[data-reset-type]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var key = btn.dataset.resetType;
        state.typeColors[key] = DEFAULTS.typeColors[key];
        if (window.TYPE_COLORS) window.TYPE_COLORS[key] = DEFAULTS.typeColors[key];
        render(container);
      });
    });

    // Heatmap opacity slider
    var heatSlider = container.querySelector('#custHeatOpacity');
    if (heatSlider) {
      heatSlider.addEventListener('input', function () {
        var pct = parseInt(heatSlider.value);
        var label = container.querySelector('#custHeatOpacityVal');
        if (label) label.textContent = pct + '%';
        var opacity = pct / 100;
        localStorage.setItem('meshcore-heatmap-opacity', opacity);
        // Live-update the heatmap if visible
        if (window._meshcoreHeatLayer && window._meshcoreHeatLayer.setOptions) {
          window._meshcoreHeatLayer.setOptions({ minOpacity: opacity });
        }
      });
    }

    // Steps
    container.querySelectorAll('[data-step-field]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        var i = parseInt(inp.dataset.idx);
        state.home.steps[i][inp.dataset.stepField] = inp.value; autoSave();
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
        state.home.checklist[i][inp.dataset.checkField] = inp.value; autoSave();
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
        state.home.footerLinks[i][inp.dataset.linkField] = inp.value; autoSave();
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
      applyThemePreview(); autoSave();
    });

        // Import from file
    var importBtn = document.getElementById('custImportFile');
    var importInput = document.getElementById('custImportInput');
    if (importBtn && importInput) {
      importBtn.addEventListener('click', function () { importInput.click(); });
      importInput.addEventListener('change', function () {
        var file = importInput.files[0];
        if (!file) return;
        var reader = new FileReader();
        reader.onload = function () {
          try {
            var data = JSON.parse(reader.result);
            // Merge imported data into state
            if (data.branding) Object.assign(state.branding, data.branding);
            if (data.theme) Object.assign(state.theme, data.theme);
            if (data.themeDark) Object.assign(state.themeDark, data.themeDark);
            if (data.nodeColors) {
              Object.assign(state.nodeColors, data.nodeColors);
              if (window.ROLE_COLORS) Object.assign(window.ROLE_COLORS, data.nodeColors);
              if (window.ROLE_STYLE) {
                for (var role in data.nodeColors) {
                  if (window.ROLE_STYLE[role]) window.ROLE_STYLE[role].color = data.nodeColors[role];
                }
              }
            }
            if (data.typeColors) {
              Object.assign(state.typeColors, data.typeColors);
              if (window.TYPE_COLORS) Object.assign(window.TYPE_COLORS, data.typeColors);
            }
            if (data.home) {
              if (data.home.heroTitle) state.home.heroTitle = data.home.heroTitle;
              if (data.home.heroSubtitle) state.home.heroSubtitle = data.home.heroSubtitle;
              if (data.home.steps) state.home.steps = deepClone(data.home.steps);
              if (data.home.checklist) state.home.checklist = deepClone(data.home.checklist);
              if (data.home.footerLinks) state.home.footerLinks = deepClone(data.home.footerLinks);
            }
            applyThemePreview();
            autoSave();
            window.dispatchEvent(new CustomEvent('theme-changed'));
            render(container);
            importBtn.textContent = '✓ Imported!';
            setTimeout(function () { importBtn.textContent = '📂 Import File'; }, 2000);
          } catch (e) {
            importBtn.textContent = '✕ Invalid JSON';
            setTimeout(function () { importBtn.textContent = '📂 Import File'; }, 3000);
          }
        };
        reader.readAsText(file);
        importInput.value = '';
      });
    }
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
        panelEl.style.left = Math.max(0, dragX + ev.clientX - startX) + 'px';
        panelEl.style.top = Math.max(56, dragY + ev.clientY - startY) + 'px';
        panelEl.style.right = 'auto';
      };
      const onUp = () => { document.removeEventListener('mousemove', onMove); document.removeEventListener('mouseup', onUp); };
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });

    render(panelEl.querySelector('.cust-inner'));
    applyThemePreview(); autoSave();
  }

  // Restore saved user theme IMMEDIATELY (before DOMContentLoaded, before map/app init)
  // roles.js has already loaded ROLE_COLORS, ROLE_STYLE, TYPE_COLORS at this point
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
        // Derived vars
        if (themeData.background) document.documentElement.style.setProperty('--content-bg', themeData.background);
        if (themeData.surface1) document.documentElement.style.setProperty('--card-bg', themeData.surface1);
      }
      if (userTheme.nodeColors) {
        if (window.ROLE_COLORS) Object.assign(window.ROLE_COLORS, userTheme.nodeColors);
        if (window.ROLE_STYLE) {
          for (const [role, color] of Object.entries(userTheme.nodeColors)) {
            if (window.ROLE_STYLE[role]) window.ROLE_STYLE[role].color = color;
          }
        }
      }
      if (userTheme.typeColors && window.TYPE_COLORS) {
        Object.assign(window.TYPE_COLORS, userTheme.typeColors);
        if (window.syncBadgeColors) window.syncBadgeColors();
      }
    }
  } catch {}

  // Wire up toggle button (needs DOM)
  document.addEventListener('DOMContentLoaded', () => {
    const btn = document.getElementById('customizeToggle');
    if (btn) btn.addEventListener('click', toggle);

    // Restore branding from localStorage (needs DOM elements to exist)
    try {
      const saved = localStorage.getItem('meshcore-user-theme');
      if (saved) {
        const userTheme = JSON.parse(saved);
        if (userTheme.branding) {
          if (userTheme.branding.siteName) {
            const brandEl = document.querySelector('.brand-text');
            if (brandEl) brandEl.textContent = userTheme.branding.siteName;
            document.title = userTheme.branding.siteName;
          }
          if (userTheme.branding.logoUrl) {
            const iconEl = document.querySelector('.brand-icon');
            if (iconEl) iconEl.innerHTML = '<img src="' + userTheme.branding.logoUrl + '" style="height:24px" onerror="this.style.display=\'none\'">';
          }
          if (userTheme.branding.faviconUrl) {
            const link = document.querySelector('link[rel="icon"]');
            if (link) link.href = userTheme.branding.faviconUrl;
          }
        }
      }
    } catch {}

    // Watch for dark/light mode toggle and re-apply theme preview
    new MutationObserver(function() {
      if (state.theme) applyThemePreview();
    }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  });
})();
