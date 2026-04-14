/* === CoreScope — customize-v2.js === */
/* Event-driven customizer with single-key localStorage, delta-based overrides, and migration from v1.
   Spec: docs/specs/customizer-rework.md | Issue: #502 */
'use strict';

(function () {
  // ── Constants ──

  var DEFAULT_HOME = {
    heroTitle: 'CoreScope',
    heroSubtitle: 'Real-time MeshCore LoRa mesh network analyzer',
    steps: [
      { emoji: '🔵', title: 'Connect via Bluetooth', description: 'Flash **BLE companion** firmware from [MeshCore Flasher](https://flasher.meshcore.co.uk/).\n- Screenless devices: default PIN `123456`\n- Screen devices: random PIN shown on display\n- If pairing fails: forget device, reboot, re-pair' },
      { emoji: '📻', title: 'Set the right frequency preset', description: '**US Recommended:**\n`910.525 MHz · BW 62.5 kHz · SF 7 · CR 5`\nSelect **"US Recommended"** in the app or flasher.' },
      { emoji: '📡', title: 'Advertise yourself', description: 'Tap the signal icon → **Flood** to broadcast your node to the mesh. Companions only advert when you trigger it manually.' },
      { emoji: '🔁', title: 'Check "Heard N repeats"', description: '- **"Sent"** = transmitted, no confirmation\n- **"Heard 0 repeats"** = no repeater picked it up\n- **"Heard 1+ repeats"** = you\'re on the mesh!' }
    ],
    footerLinks: [
      { label: '📦 Packets', url: '#/packets' },
      { label: '🗺️ Network Map', url: '#/map' }
    ]
  };

  var STORAGE_KEY = 'cs-theme-overrides';
  var DARK_MODE_KEY = 'meshcore-theme';
  var LEGACY_KEYS = [
    'meshcore-user-theme',
    'meshcore-timestamp-mode',
    'meshcore-timestamp-timezone',
    'meshcore-timestamp-format',
    'meshcore-timestamp-custom-format',
    'meshcore-heatmap-opacity',
    'meshcore-live-heatmap-opacity'
  ];

  var VALID_SECTIONS = ['branding', 'theme', 'themeDark', 'nodeColors', 'typeColors', 'home', 'timestamps', 'heatmapOpacity', 'liveHeatmapOpacity', 'distanceUnit'];
  var OBJECT_SECTIONS = ['branding', 'theme', 'themeDark', 'nodeColors', 'typeColors', 'home', 'timestamps'];
  var SCALAR_SECTIONS = ['heatmapOpacity', 'liveHeatmapOpacity'];
  var DISTANCE_UNIT_VALUES = ['km', 'mi', 'auto'];

  // CSS variable mapping (theme key → CSS custom property)
  var THEME_CSS_MAP = {
    accent: '--accent', accentHover: '--accent-hover',
    navBg: '--nav-bg', navBg2: '--nav-bg2', navText: '--nav-text', navTextMuted: '--nav-text-muted',
    background: '--surface-0', text: '--text', textMuted: '--text-muted', border: '--border',
    statusGreen: '--status-green', statusYellow: '--status-yellow', statusRed: '--status-red',
    surface1: '--surface-1', surface2: '--surface-2', surface3: '--surface-3',
    sectionBg: '--section-bg',
    cardBg: '--card-bg', contentBg: '--content-bg', detailBg: '--detail-bg',
    inputBg: '--input-bg', rowStripe: '--row-stripe', rowHover: '--row-hover', selectedBg: '--selected-bg',
    font: '--font', mono: '--mono'
  };

  var THEME_COLOR_KEYS = Object.keys(THEME_CSS_MAP).filter(function (k) { return k !== 'font' && k !== 'mono'; });

  // ── Presets (copied from v1 customize.js) ──
  var PRESETS = {
    default: {
      name: 'Default', desc: 'MeshCore blue',
      preview: ['#4a9eff', '#0f0f23', '#f4f5f7', '#1a1a2e', '#22c55e'],
      theme: {
        accent: '#4a9eff', navBg: '#0f0f23', navText: '#ffffff', background: '#f4f5f7', text: '#1a1a2e',
        statusGreen: '#22c55e', statusYellow: '#eab308', statusRed: '#ef4444',
        accentHover: '#6db3ff', navBg2: '#1a1a2e', navTextMuted: '#cbd5e1', textMuted: '#5b6370', border: '#e2e5ea',
        surface1: '#ffffff', surface2: '#ffffff', cardBg: '#ffffff', contentBg: '#f4f5f7',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#f9fafb', rowHover: '#eef2ff', selectedBg: '#dbeafe',
        surface3: '#ffffff', sectionBg: '#eef2ff'
      },
      themeDark: {
        accent: '#4a9eff', navBg: '#0f0f23', navText: '#ffffff', background: '#0f0f23', text: '#e2e8f0',
        statusGreen: '#22c55e', statusYellow: '#eab308', statusRed: '#ef4444',
        accentHover: '#6db3ff', navBg2: '#1a1a2e', navTextMuted: '#cbd5e1', textMuted: '#a8b8cc', border: '#334155',
        surface1: '#1a1a2e', surface2: '#232340', cardBg: '#1a1a2e', contentBg: '#0f0f23',
        detailBg: '#232340', inputBg: '#1e1e34', rowStripe: '#1e1e34', rowHover: '#2d2d50', selectedBg: '#1e3a5f',
        surface3: '#2d2d50', sectionBg: '#1e1e34'
      }
    },
    ocean: {
      name: 'Ocean', desc: 'Deep blues & teals',
      preview: ['#0077b6', '#03045e', '#f0f7fa', '#48cae4', '#15803d'],
      theme: {
        accent: '#0077b6', navBg: '#03045e', navText: '#ffffff', background: '#f0f7fa', text: '#0a1628',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#0096d6', navBg2: '#023e8a', navTextMuted: '#90caf9', textMuted: '#4a6580', border: '#c8dce8',
        surface1: '#ffffff', surface2: '#e8f4f8', cardBg: '#ffffff', contentBg: '#f0f7fa',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#f5fafd', rowHover: '#e0f0f8', selectedBg: '#bde0fe',
        surface3: '#f5fafd', sectionBg: '#e0f0f8'
      },
      themeDark: {
        accent: '#48cae4', navBg: '#03045e', navText: '#ffffff', background: '#0a1929', text: '#e0e7ef',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#76d7ea', navBg2: '#012a4a', navTextMuted: '#90caf9', textMuted: '#8eafc4', border: '#1e3a5f',
        surface1: '#0d2137', surface2: '#122d4a', cardBg: '#0d2137', contentBg: '#0a1929',
        detailBg: '#122d4a', inputBg: '#0d2137', rowStripe: '#0d2137', rowHover: '#153450', selectedBg: '#1a4570',
        surface3: '#153450', sectionBg: '#0d2137'
      }
    },
    forest: {
      name: 'Forest', desc: 'Greens & earth tones',
      preview: ['#2d6a4f', '#1b3a2d', '#f2f7f4', '#52b788', '#15803d'],
      theme: {
        accent: '#2d6a4f', navBg: '#1b3a2d', navText: '#ffffff', background: '#f2f7f4', text: '#1a2e24',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#40916c', navBg2: '#2d6a4f', navTextMuted: '#a3c4b5', textMuted: '#557063', border: '#c8dcd2',
        surface1: '#ffffff', surface2: '#e8f0eb', cardBg: '#ffffff', contentBg: '#f2f7f4',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#f5faf7', rowHover: '#e4f0e8', selectedBg: '#c2e0cc',
        surface3: '#f5faf7', sectionBg: '#e4f0e8'
      },
      themeDark: {
        accent: '#52b788', navBg: '#1b3a2d', navText: '#ffffff', background: '#0d1f17', text: '#d8e8df',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#74c69d', navBg2: '#14532d', navTextMuted: '#86b89a', textMuted: '#8aac9a', border: '#2d4a3a',
        surface1: '#162e23', surface2: '#1d3a2d', cardBg: '#162e23', contentBg: '#0d1f17',
        detailBg: '#1d3a2d', inputBg: '#162e23', rowStripe: '#162e23', rowHover: '#1f4030', selectedBg: '#265940',
        surface3: '#1f4030', sectionBg: '#162e23'
      }
    },
    sunset: {
      name: 'Sunset', desc: 'Warm oranges & ambers',
      preview: ['#c2410c', '#431407', '#fef7f2', '#fb923c', '#dc2626'],
      theme: {
        accent: '#c2410c', navBg: '#431407', navText: '#ffffff', background: '#fef7f2', text: '#1c0f06',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#ea580c', navBg2: '#7c2d12', navTextMuted: '#fdba74', textMuted: '#6b5344', border: '#e8d5c8',
        surface1: '#ffffff', surface2: '#fef0e6', cardBg: '#ffffff', contentBg: '#fef7f2',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#fefaf7', rowHover: '#fef0e0', selectedBg: '#fed7aa',
        surface3: '#fefaf7', sectionBg: '#fef0e0'
      },
      themeDark: {
        accent: '#fb923c', navBg: '#431407', navText: '#ffffff', background: '#1a0f08', text: '#f0ddd0',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#fdba74', navBg2: '#7c2d12', navTextMuted: '#c2855a', textMuted: '#b09080', border: '#4a2a18',
        surface1: '#261a10', surface2: '#332214', cardBg: '#261a10', contentBg: '#1a0f08',
        detailBg: '#332214', inputBg: '#261a10', rowStripe: '#261a10', rowHover: '#3a2818', selectedBg: '#5c3518',
        surface3: '#3a2818', sectionBg: '#261a10'
      }
    },
    mono: {
      name: 'Monochrome', desc: 'Pure grays, no color',
      preview: ['#525252', '#171717', '#f5f5f5', '#a3a3a3', '#737373'],
      theme: {
        accent: '#525252', navBg: '#171717', navText: '#ffffff', background: '#f5f5f5', text: '#171717',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#737373', navBg2: '#262626', navTextMuted: '#a3a3a3', textMuted: '#525252', border: '#d4d4d4',
        surface1: '#ffffff', surface2: '#fafafa', cardBg: '#ffffff', contentBg: '#f5f5f5',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#fafafa', rowHover: '#efefef', selectedBg: '#e5e5e5',
        surface3: '#fafafa', sectionBg: '#efefef'
      },
      themeDark: {
        accent: '#a3a3a3', navBg: '#171717', navText: '#ffffff', background: '#0a0a0a', text: '#e5e5e5',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#d4d4d4', navBg2: '#1a1a1a', navTextMuted: '#737373', textMuted: '#a3a3a3', border: '#333333',
        surface1: '#171717', surface2: '#1f1f1f', cardBg: '#171717', contentBg: '#0a0a0a',
        detailBg: '#1f1f1f', inputBg: '#171717', rowStripe: '#141414', rowHover: '#222222', selectedBg: '#2a2a2a',
        surface3: '#222222', sectionBg: '#171717'
      }
    },
    highContrast: {
      name: 'High Contrast', desc: 'WCAG AAA, max readability',
      preview: ['#0050a0', '#000000', '#ffffff', '#66b3ff', '#006400'],
      theme: {
        accent: '#0050a0', navBg: '#000000', navText: '#ffffff', background: '#ffffff', text: '#000000',
        statusGreen: '#006400', statusYellow: '#7a5900', statusRed: '#b30000',
        accentHover: '#0068cc', navBg2: '#1a1a1a', navTextMuted: '#e0e0e0', textMuted: '#333333', border: '#000000',
        surface1: '#ffffff', surface2: '#f0f0f0', cardBg: '#ffffff', contentBg: '#ffffff',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#f0f0f0', rowHover: '#e0e8f5', selectedBg: '#cce0ff',
        surface3: '#f0f0f0', sectionBg: '#e0e8f5'
      },
      themeDark: {
        accent: '#66b3ff', navBg: '#000000', navText: '#ffffff', background: '#000000', text: '#ffffff',
        statusGreen: '#66ff66', statusYellow: '#ffff00', statusRed: '#ff6666',
        accentHover: '#99ccff', navBg2: '#0a0a0a', navTextMuted: '#cccccc', textMuted: '#cccccc', border: '#ffffff',
        surface1: '#111111', surface2: '#1a1a1a', cardBg: '#111111', contentBg: '#000000',
        detailBg: '#1a1a1a', inputBg: '#111111', rowStripe: '#0d0d0d', rowHover: '#1a2a3a', selectedBg: '#003366',
        surface3: '#1a2a3a', sectionBg: '#111111'
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
      theme: {
        accent: '#7c3aed', navBg: '#1e1045', navText: '#ffffff', background: '#f5f3ff', text: '#1a1040',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#8b5cf6', navBg2: '#2e1065', navTextMuted: '#c4b5fd', textMuted: '#5b5075', border: '#d8d0e8',
        surface1: '#ffffff', surface2: '#ede9fe', cardBg: '#ffffff', contentBg: '#f5f3ff',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#faf8ff', rowHover: '#ede9fe', selectedBg: '#ddd6fe',
        surface3: '#faf8ff', sectionBg: '#ede9fe'
      },
      themeDark: {
        accent: '#a78bfa', navBg: '#1e1045', navText: '#ffffff', background: '#0f0a24', text: '#e2ddf0',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#c4b5fd', navBg2: '#2e1065', navTextMuted: '#9d8abf', textMuted: '#9a90b0', border: '#352a55',
        surface1: '#1a1338', surface2: '#221a48', cardBg: '#1a1338', contentBg: '#0f0a24',
        detailBg: '#221a48', inputBg: '#1a1338', rowStripe: '#1a1338', rowHover: '#2a2050', selectedBg: '#352a6a',
        surface3: '#2a2050', sectionBg: '#1a1338'
      }
    },
    ember: {
      name: 'Ember', desc: 'Warm red/orange, cyberpunk',
      preview: ['#dc2626', '#1a0a0a', '#faf5f5', '#ef4444', '#15803d'],
      theme: {
        accent: '#dc2626', navBg: '#1a0a0a', navText: '#ffffff', background: '#faf5f5', text: '#1a0a0a',
        statusGreen: '#15803d', statusYellow: '#a16207', statusRed: '#dc2626',
        accentHover: '#ef4444', navBg2: '#2a1010', navTextMuted: '#f0a0a0', textMuted: '#6b4444', border: '#e0c8c8',
        surface1: '#ffffff', surface2: '#faf0f0', cardBg: '#ffffff', contentBg: '#faf5f5',
        detailBg: '#ffffff', inputBg: '#ffffff', rowStripe: '#fdf8f8', rowHover: '#fce8e8', selectedBg: '#fecaca',
        surface3: '#fdf8f8', sectionBg: '#fce8e8'
      },
      themeDark: {
        accent: '#ef4444', navBg: '#1a0505', navText: '#ffffff', background: '#0d0505', text: '#f0dada',
        statusGreen: '#4ade80', statusYellow: '#facc15', statusRed: '#f87171',
        accentHover: '#f87171', navBg2: '#2a0a0a', navTextMuted: '#c07070', textMuted: '#b09090', border: '#4a2020',
        surface1: '#1a0d0d', surface2: '#261414', cardBg: '#1a0d0d', contentBg: '#0d0505',
        detailBg: '#261414', inputBg: '#1a0d0d', rowStripe: '#1a0d0d', rowHover: '#301818', selectedBg: '#4a1a1a',
        surface3: '#301818', sectionBg: '#1a0d0d'
      }
    }
  };

  // ── Labels, hints, emojis (carried from v1) ──

  var THEME_LABELS = {
    accent: 'Brand Color', accentHover: 'Accent Hover',
    navBg: 'Navigation', navBg2: 'Nav Gradient End', navText: 'Nav Text', navTextMuted: 'Nav Muted Text',
    background: 'Background', text: 'Text', textMuted: 'Muted Text', border: 'Borders',
    statusGreen: 'Healthy', statusYellow: 'Warning', statusRed: 'Error',
    surface1: 'Cards', surface2: 'Panels', surface3: 'Tertiary Surface', sectionBg: 'Section Header', cardBg: 'Card Fill', contentBg: 'Content Area',
    detailBg: 'Detail Panels', inputBg: 'Inputs', rowStripe: 'Table Stripe',
    rowHover: 'Row Hover', selectedBg: 'Selected',
    font: 'Body Font', mono: 'Mono Font'
  };

  var THEME_HINTS = {
    accent: 'Buttons, links, active tabs, badges, charts — your primary brand color',
    navBg: 'Top navigation bar', navText: 'Nav bar text, links, brand name, buttons',
    background: 'Main page background', text: 'Primary text — muted text auto-derives',
    statusGreen: 'Healthy/online indicators', statusYellow: 'Warning/degraded + hop conflicts',
    statusRed: 'Error/offline indicators', accentHover: 'Hover state for accent elements',
    navBg2: 'Darker end of nav gradient', navTextMuted: 'Inactive nav links, nav buttons',
    textMuted: 'Labels, timestamps, secondary text', border: 'Dividers, table borders, card borders',
    surface1: 'Card and panel backgrounds', surface2: 'Nested surfaces, secondary panels',
    surface3: 'Tertiary surfaces, hover accents', sectionBg: 'Section header backgrounds',
    cardBg: 'Detail panels, modals', contentBg: 'Content area behind cards',
    detailBg: 'Modal, packet detail, side panels', inputBg: 'Text inputs, dropdowns',
    rowStripe: 'Alternating table rows', rowHover: 'Table row hover', selectedBg: 'Selected/active rows',
    font: 'System font stack for body text', mono: 'Monospace font for hex, code, hashes'
  };

  var NODE_LABELS = { repeater: 'Repeater', companion: 'Companion', room: 'Room Server', sensor: 'Sensor', observer: 'Observer' };
  var NODE_HINTS = {
    repeater: 'Infrastructure nodes that relay packets', companion: 'End-user devices',
    room: 'Room/chat server nodes', sensor: 'Sensor/telemetry nodes', observer: 'MQTT observer stations'
  };
  var NODE_EMOJI = { repeater: '◆', companion: '●', room: '■', sensor: '▲', observer: '★' };

  var TYPE_LABELS = {
    ADVERT: 'Advertisement', GRP_TXT: 'Channel Message', TXT_MSG: 'Direct Message', ACK: 'Acknowledgment',
    REQUEST: 'Request', RESPONSE: 'Response', TRACE: 'Traceroute', PATH: 'Path', ANON_REQ: 'Anonymous Request'
  };
  var TYPE_HINTS = {
    ADVERT: 'Node advertisements', GRP_TXT: 'Group/channel messages', TXT_MSG: 'Direct messages',
    ACK: 'Acknowledgments', REQUEST: 'Requests', RESPONSE: 'Responses',
    TRACE: 'Traceroute', PATH: 'Path packets', ANON_REQ: 'Encrypted anonymous requests'
  };
  var TYPE_EMOJI = {
    ADVERT: '📡', GRP_TXT: '💬', TXT_MSG: '✉️', ACK: '✓', REQUEST: '❓',
    RESPONSE: '📨', TRACE: '🔍', PATH: '🛤️', ANON_REQ: '🕵️'
  };

  var BASIC_KEYS = ['accent', 'navBg', 'navText', 'background', 'text', 'statusGreen', 'statusYellow', 'statusRed'];
  var ADVANCED_KEYS = ['accentHover', 'navBg2', 'navTextMuted', 'textMuted', 'border', 'surface1', 'surface2', 'cardBg', 'contentBg', 'detailBg', 'inputBg', 'rowStripe', 'rowHover', 'selectedBg'];
  var FONT_KEYS = ['font', 'mono'];

  // ── Validation helpers ──

  var COLOR_RE = /^#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;
  var CSS_FUNC_RE = /^(?:rgb|rgba|hsl|hsla)\s*\(.*\)$/i;
  // Basic list of CSS named colors (subset — covers common ones)
  var NAMED_COLORS = 'aliceblue,antiquewhite,aqua,aquamarine,azure,beige,bisque,black,blanchedalmond,blue,blueviolet,brown,burlywood,cadetblue,chartreuse,chocolate,coral,cornflowerblue,cornsilk,crimson,cyan,darkblue,darkcyan,darkgoldenrod,darkgray,darkgreen,darkgrey,darkkhaki,darkmagenta,darkolivegreen,darkorange,darkorchid,darkred,darksalmon,darkseagreen,darkslateblue,darkslategray,darkslategrey,darkturquoise,darkviolet,deeppink,deepskyblue,dimgray,dimgrey,dodgerblue,firebrick,floralwhite,forestgreen,fuchsia,gainsboro,ghostwhite,gold,goldenrod,gray,green,greenyellow,grey,honeydew,hotpink,indianred,indigo,ivory,khaki,lavender,lavenderblush,lawngreen,lemonchiffon,lightblue,lightcoral,lightcyan,lightgoldenrodyellow,lightgray,lightgreen,lightgrey,lightpink,lightsalmon,lightseagreen,lightskyblue,lightslategray,lightslategrey,lightsteelblue,lightyellow,lime,limegreen,linen,magenta,maroon,mediumaquamarine,mediumblue,mediumorchid,mediumpurple,mediumseagreen,mediumslateblue,mediumspringgreen,mediumturquoise,mediumvioletred,midnightblue,mintcream,mistyrose,moccasin,navajowhite,navy,oldlace,olive,olivedrab,orange,orangered,orchid,palegoldenrod,palegreen,paleturquoise,palevioletred,papayawhip,peachpuff,peru,pink,plum,powderblue,purple,rebeccapurple,red,rosybrown,royalblue,saddlebrown,salmon,sandybrown,seagreen,seashell,sienna,silver,skyblue,slateblue,slategray,slategrey,snow,springgreen,steelblue,tan,teal,thistle,tomato,turquoise,violet,wheat,white,whitesmoke,yellow,yellowgreen,transparent,currentcolor,inherit'.split(',');
  var NAMED_SET = null;

  function isValidColor(val) {
    if (typeof val !== 'string') return false;
    if (COLOR_RE.test(val)) return true;
    if (CSS_FUNC_RE.test(val)) return true;
    if (!NAMED_SET) { NAMED_SET = new Set(NAMED_COLORS); }
    return NAMED_SET.has(val.toLowerCase().trim());
  }

  function isValidOpacity(val) {
    return typeof val === 'number' && isFinite(val) && val >= 0 && val <= 1;
  }

  var TS_ENUMS = {
    defaultMode: ['ago', 'absolute'],
    timezone: ['local', 'utc'],
    formatPreset: ['iso', 'iso-seconds', 'locale']
  };

  // ── Core data functions (exported for testing via window._customizerV2) ──

  /** @type {object|null} server defaults, set during init */
  var _serverDefaults = null;
  var _initDone = false;
  var _saveStatus = 'saved'; // 'saved' | 'saving' | 'error'
  var _writeTimer = null;

  function readOverrides() {
    try {
      var raw = localStorage.getItem(STORAGE_KEY);
      if (raw == null) return {};
      var parsed = JSON.parse(raw);
      if (parsed == null || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
      return parsed;
    } catch (e) {
      return {};
    }
  }

  function _validateDelta(delta) {
    // Validate color values in theme/themeDark/nodeColors/typeColors, numeric values, timestamp enums.
    // Returns a cleaned copy (invalid values removed with console.warn).
    var clean = {};
    var colorSections = ['theme', 'themeDark', 'nodeColors', 'typeColors'];
    for (var key in delta) {
      if (!delta.hasOwnProperty(key)) continue;
      if (colorSections.indexOf(key) !== -1 && typeof delta[key] === 'object' && delta[key] !== null) {
        var section = {};
        var src = delta[key];
        for (var sk in src) {
          if (!src.hasOwnProperty(sk)) continue;
          // font/mono are not colors
          if ((key === 'theme' || key === 'themeDark') && (sk === 'font' || sk === 'mono')) {
            if (typeof src[sk] === 'string') section[sk] = src[sk];
            continue;
          }
          if (typeof src[sk] === 'string' && isValidColor(src[sk])) {
            section[sk] = src[sk];
          } else {
            console.warn('[customizer-v2] Invalid color value rejected:', key + '.' + sk, src[sk]);
          }
        }
        if (Object.keys(section).length) clean[key] = section;
      } else if (key === 'heatmapOpacity' || key === 'liveHeatmapOpacity') {
        var numVal = typeof delta[key] === 'string' ? parseFloat(delta[key]) : delta[key];
        if (isValidOpacity(numVal)) {
          clean[key] = numVal;
        } else {
          console.warn('[customizer-v2] Invalid opacity value rejected:', key, delta[key]);
        }
      } else if (key === 'timestamps' && typeof delta[key] === 'object' && delta[key] !== null) {
        var ts = {};
        var tsrc = delta[key];
        for (var tk in tsrc) {
          if (!tsrc.hasOwnProperty(tk)) continue;
          if (TS_ENUMS[tk]) {
            if (TS_ENUMS[tk].indexOf(tsrc[tk]) !== -1) {
              ts[tk] = tsrc[tk];
            } else {
              console.warn('[customizer-v2] Invalid timestamp enum rejected:', tk, tsrc[tk]);
            }
          } else if (tk === 'customFormat') {
            if (typeof tsrc[tk] === 'string') ts[tk] = tsrc[tk];
          } else {
            ts[tk] = tsrc[tk]; // unknown timestamp keys pass through
          }
        }
        if (Object.keys(ts).length) clean[key] = ts;
      } else if (key === 'branding' || key === 'home') {
        // Pass through as-is (object shape)
        if (typeof delta[key] === 'object' && delta[key] !== null) {
          clean[key] = JSON.parse(JSON.stringify(delta[key]));
        }
      } else {
        // Unknown key — pass through for forward compatibility
        clean[key] = delta[key];
      }
    }
    return clean;
  }

  function writeOverrides(delta) {
    if (delta == null || typeof delta !== 'object') return;
    // If empty, remove key entirely
    var keys = Object.keys(delta);
    if (keys.length === 0) {
      try { localStorage.removeItem(STORAGE_KEY); } catch (e) { /* ignore */ }
      _updateSaveStatus('saved');
      return;
    }
    var validated = _validateDelta(delta);
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(validated));
      _updateSaveStatus('saved');
    } catch (e) {
      _updateSaveStatus('error');
      console.error('[customizer-v2] localStorage quota exceeded:', e);
      // Show visible warning
      _showQuotaWarning();
    }
  }

  function computeEffective(serverConfig, userOverrides) {
    var effective = JSON.parse(JSON.stringify(serverConfig || {}));
    // Defense-in-depth: if server returned home:null, use built-in defaults
    if (!effective.home || typeof effective.home !== 'object') {
      effective.home = JSON.parse(JSON.stringify(DEFAULT_HOME));
    }
    if (!userOverrides || typeof userOverrides !== 'object') return effective;
    for (var key in userOverrides) {
      if (!userOverrides.hasOwnProperty(key)) continue;
      var uv = userOverrides[key];
      if (uv != null && typeof uv === 'object' && !Array.isArray(uv)) {
        // Object section — shallow merge
        if (!effective[key] || typeof effective[key] !== 'object') effective[key] = {};
        var val = userOverrides[key];
        for (var sk in val) {
          if (val.hasOwnProperty(sk)) {
            if (Array.isArray(val[sk])) {
              effective[key][sk] = JSON.parse(JSON.stringify(val[sk])); // array: full replace
            } else {
              effective[key][sk] = val[sk];
            }
          }
        }
      } else if (Array.isArray(uv)) {
        // Array — full replacement
        effective[key] = JSON.parse(JSON.stringify(uv));
      } else {
        // Scalar — direct replacement
        effective[key] = uv;
      }
    }
    return effective;
  }

  function isDarkMode() {
    var attr = document.documentElement.getAttribute('data-theme');
    if (attr === 'dark') return true;
    if (attr === 'light') return false;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
  }

  function applyCSS(effectiveConfig) {
    var dark = isDarkMode();
    var themeSection = dark
      ? Object.assign({}, effectiveConfig.theme || {}, effectiveConfig.themeDark || {})
      : (effectiveConfig.theme || {});

    var root = document.documentElement.style;

    // Apply theme color/font CSS variables
    for (var key in THEME_CSS_MAP) {
      if (themeSection[key]) {
        root.setProperty(THEME_CSS_MAP[key], themeSection[key]);
      }
    }

    // Derived vars
    if (themeSection.background) root.setProperty('--content-bg', themeSection.contentBg || themeSection.background);
    if (themeSection.surface1) root.setProperty('--card-bg', themeSection.cardBg || themeSection.surface1);

    // Node colors → CSS vars + global objects
    var nc = effectiveConfig.nodeColors;
    if (nc) {
      for (var role in nc) {
        root.setProperty('--node-' + role, nc[role]);
        if (window.ROLE_COLORS && role in window.ROLE_COLORS) window.ROLE_COLORS[role] = nc[role];
        if (window.ROLE_STYLE && window.ROLE_STYLE[role]) window.ROLE_STYLE[role].color = nc[role];
      }
    }

    // Type colors → CSS vars + global objects
    var tc = effectiveConfig.typeColors;
    if (tc) {
      for (var type in tc) {
        root.setProperty('--type-' + type.toLowerCase(), tc[type]);
        if (window.TYPE_COLORS && type in window.TYPE_COLORS) window.TYPE_COLORS[type] = tc[type];
      }
      if (window.syncBadgeColors) window.syncBadgeColors();
    }

    // Timestamps → sync to SITE_CONFIG
    if (effectiveConfig.timestamps) {
      if (!window.SITE_CONFIG) window.SITE_CONFIG = {};
      if (!window.SITE_CONFIG.timestamps) window.SITE_CONFIG.timestamps = {};
      var ts = effectiveConfig.timestamps;
      if (ts.defaultMode) window.SITE_CONFIG.timestamps.defaultMode = ts.defaultMode;
      if (ts.timezone) window.SITE_CONFIG.timestamps.timezone = ts.timezone;
      if (ts.formatPreset) window.SITE_CONFIG.timestamps.formatPreset = ts.formatPreset;
      if (ts.customFormat != null) window.SITE_CONFIG.timestamps.customFormat = ts.customFormat;
    }

    // Heatmap opacity → sync to localStorage for map pages
    if (typeof effectiveConfig.heatmapOpacity === 'number') {
      localStorage.setItem('meshcore-heatmap-opacity', effectiveConfig.heatmapOpacity);
    }
    if (typeof effectiveConfig.liveHeatmapOpacity === 'number') {
      localStorage.setItem('meshcore-live-heatmap-opacity', effectiveConfig.liveHeatmapOpacity);
    }

    // Distance unit → sync to localStorage for all pages
    if (typeof effectiveConfig.distanceUnit === 'string' && DISTANCE_UNIT_VALUES.indexOf(effectiveConfig.distanceUnit) >= 0) {
      localStorage.setItem('meshcore-distance-unit', effectiveConfig.distanceUnit);
    }

    // Nav gradient
    if (themeSection.navBg) {
      var nav = document.querySelector('.top-nav');
      if (nav) {
        nav.style.background = 'linear-gradient(135deg, ' + themeSection.navBg + ' 0%, ' + (themeSection.navBg2 || themeSection.navBg) + ' 50%, ' + themeSection.navBg + ' 100%)';
      }
    }

    // Branding
    var br = effectiveConfig.branding;
    if (br) {
      if (br.siteName) {
        document.title = br.siteName;
        var brandEl = document.querySelector('.brand-text');
        if (brandEl) brandEl.textContent = br.siteName;
      }
      if (br.logoUrl) {
        var iconEl = document.querySelector('.brand-icon');
        if (iconEl) iconEl.innerHTML = '<img src="' + br.logoUrl + '" style="height:24px" onerror="this.style.display=\'none\'">';
      }
      if (br.faviconUrl) {
        var fav = document.querySelector('link[rel="icon"]');
        if (fav) fav.href = br.faviconUrl;
      }
    }

    // Dispatch theme-changed event (bare, no payload — matches existing behavior)
    window.dispatchEvent(new CustomEvent('theme-changed'));
  }

  /** Run the full pipeline: read → merge → atomic assign → applyCSS */
  function _runPipeline() {
    var overrides = readOverrides();
    var effective = computeEffective(_serverDefaults || {}, overrides);
    window.SITE_CONFIG = effective;
    applyCSS(effective);
  }

  // ── setOverride / clearOverride ──
  // Uses _pendingOverrides accumulator to prevent data loss when rapid calls
  // occur within the debounce window (each call would otherwise read stale
  // overrides from localStorage before the previous write landed).

  var _debounceTimer = null;
  var _pendingOverrides = {};

  function setOverride(section, key, value) {
    if (section) {
      if (!_pendingOverrides[section]) _pendingOverrides[section] = {};
      _pendingOverrides[section][key] = value;
    } else {
      _pendingOverrides[key] = value;
    }
    _debouncedWrite();
  }

  function clearOverride(section, key) {
    var delta = readOverrides();
    if (section) {
      if (delta[section]) {
        delete delta[section][key];
        if (Object.keys(delta[section]).length === 0) delete delta[section];
      }
    } else {
      delete delta[key];
    }
    // Also clear from pending
    if (section && _pendingOverrides[section]) {
      delete _pendingOverrides[section][key];
      if (Object.keys(_pendingOverrides[section]).length === 0) delete _pendingOverrides[section];
    } else if (!section) {
      delete _pendingOverrides[key];
    }
    // No debounce on reset — instant feedback
    writeOverrides(delta);
    _runPipeline();
    _refreshPanel();
  }

  function _debouncedWrite() {
    _updateSaveStatus('saving');
    if (_debounceTimer) clearTimeout(_debounceTimer);
    _debounceTimer = setTimeout(function () {
      _debounceTimer = null;
      var delta = readOverrides();
      for (var sec in _pendingOverrides) {
        if (typeof _pendingOverrides[sec] === 'object' && _pendingOverrides[sec] !== null) {
          if (!delta[sec]) delta[sec] = {};
          for (var k in _pendingOverrides[sec]) {
            delta[sec][k] = _pendingOverrides[sec][k];
          }
        } else {
          delta[sec] = _pendingOverrides[sec];
        }
      }
      var pendingKeys = _pendingOverrides;
      _pendingOverrides = {};
      // Spec Decision #7: don't silently prune existing overrides.
      // Only prevent redundant NEW writes: if a value just written matches
      // the server default, don't store it (clearOverride semantics).
      var server = _serverDefaults || {};
      for (var ps in pendingKeys) {
        if (typeof pendingKeys[ps] === 'object' && pendingKeys[ps] !== null && OBJECT_SECTIONS.indexOf(ps) >= 0) {
          var serverSec = server[ps] || {};
          if (delta[ps]) {
            for (var pk in pendingKeys[ps]) {
              var ov = delta[ps][pk];
              var sv = serverSec[pk];
              var match = (typeof ov === 'object' || typeof sv === 'object')
                ? JSON.stringify(ov) === JSON.stringify(sv) : ov === sv;
              if (match) delete delta[ps][pk];
            }
            if (Object.keys(delta[ps]).length === 0) delete delta[ps];
          }
        } else if (SCALAR_SECTIONS.indexOf(ps) >= 0 && delta[ps] === server[ps]) {
          delete delta[ps];
        }
      }
      writeOverrides(delta);
      _runPipeline();
      _refreshPanel();
    }, 300);
  }

  // ── Migration ──

  function migrateOldKeys() {
    // Skip if new key already exists
    if (localStorage.getItem(STORAGE_KEY) != null) return null;

    var hasLegacy = false;
    for (var i = 0; i < LEGACY_KEYS.length; i++) {
      if (localStorage.getItem(LEGACY_KEYS[i]) != null) { hasLegacy = true; break; }
    }
    if (!hasLegacy) return null;

    var delta = {};

    // meshcore-user-theme (JSON object with branding, theme, themeDark, nodeColors, typeColors, home)
    try {
      var raw = localStorage.getItem('meshcore-user-theme');
      if (raw) {
        var parsed = JSON.parse(raw);
        if (parsed && typeof parsed === 'object') {
          var allowedKeys = ['branding', 'theme', 'themeDark', 'nodeColors', 'typeColors', 'home'];
          for (var k = 0; k < allowedKeys.length; k++) {
            if (parsed[allowedKeys[k]] && typeof parsed[allowedKeys[k]] === 'object') {
              delta[allowedKeys[k]] = JSON.parse(JSON.stringify(parsed[allowedKeys[k]]));
            }
          }
        }
      }
    } catch (e) {
      console.warn('[customizer-v2] Migration: invalid meshcore-user-theme JSON, skipping');
    }

    // Timestamp keys → delta.timestamps
    var tsMode = localStorage.getItem('meshcore-timestamp-mode');
    var tsTz = localStorage.getItem('meshcore-timestamp-timezone');
    var tsFmt = localStorage.getItem('meshcore-timestamp-format');
    var tsCustom = localStorage.getItem('meshcore-timestamp-custom-format');
    if (tsMode || tsTz || tsFmt || tsCustom) {
      delta.timestamps = {};
      if (tsMode && (tsMode === 'ago' || tsMode === 'absolute')) delta.timestamps.defaultMode = tsMode;
      if (tsTz && (tsTz === 'local' || tsTz === 'utc')) delta.timestamps.timezone = tsTz;
      if (tsFmt && (tsFmt === 'iso' || tsFmt === 'iso-seconds' || tsFmt === 'locale')) delta.timestamps.formatPreset = tsFmt;
      if (tsCustom != null && tsCustom !== '') delta.timestamps.customFormat = tsCustom;
      if (Object.keys(delta.timestamps).length === 0) delete delta.timestamps;
    }

    // Heatmap opacities
    var heatRaw = localStorage.getItem('meshcore-heatmap-opacity');
    if (heatRaw != null && heatRaw !== '') {
      var heatVal = parseFloat(heatRaw);
      if (isFinite(heatVal)) delta.heatmapOpacity = Math.max(0, Math.min(1, heatVal));
    }
    var liveHeatRaw = localStorage.getItem('meshcore-live-heatmap-opacity');
    if (liveHeatRaw != null && liveHeatRaw !== '') {
      var liveHeatVal = parseFloat(liveHeatRaw);
      if (isFinite(liveHeatVal)) delta.liveHeatmapOpacity = Math.max(0, Math.min(1, liveHeatVal));
    }

    // Write the migrated delta
    writeOverrides(delta);

    // Remove all legacy keys
    for (var j = 0; j < LEGACY_KEYS.length; j++) {
      localStorage.removeItem(LEGACY_KEYS[j]);
    }

    console.log('[customizer-v2] Migrated', Object.keys(delta).length, 'sections from legacy localStorage keys');
    return delta;
  }

  // ── Validate shape (for import) ──

  function validateShape(obj) {
    var errors = [];
    if (obj == null || typeof obj !== 'object' || Array.isArray(obj)) {
      return { valid: false, errors: ['Input must be a plain object'] };
    }

    var colorSections = ['theme', 'themeDark', 'nodeColors', 'typeColors'];
    for (var key in obj) {
      if (!obj.hasOwnProperty(key)) continue;
      if (VALID_SECTIONS.indexOf(key) === -1) {
        console.warn('[customizer-v2] Unknown top-level key in import:', key);
        continue; // warning, not error
      }
      // Check section types
      if (OBJECT_SECTIONS.indexOf(key) !== -1) {
        if (typeof obj[key] !== 'object' || obj[key] === null || Array.isArray(obj[key])) {
          errors.push('Section "' + key + '" must be an object');
          continue;
        }
      }
      if (SCALAR_SECTIONS.indexOf(key) !== -1) {
        var num = typeof obj[key] === 'string' ? parseFloat(obj[key]) : obj[key];
        if (!isValidOpacity(num)) {
          errors.push('"' + key + '" must be a number between 0 and 1');
        }
        continue;
      }
      // Validate colors in color sections
      if (colorSections.indexOf(key) !== -1 && typeof obj[key] === 'object') {
        for (var ck in obj[key]) {
          if (!obj[key].hasOwnProperty(ck)) continue;
          if ((key === 'theme' || key === 'themeDark') && (ck === 'font' || ck === 'mono')) continue;
          if (typeof obj[key][ck] === 'string' && !isValidColor(obj[key][ck])) {
            errors.push('Invalid color: ' + key + '.' + ck + ' = "' + obj[key][ck] + '"');
          }
        }
      }
      // Validate timestamps
      if (key === 'timestamps' && typeof obj[key] === 'object') {
        for (var tk in obj[key]) {
          if (TS_ENUMS[tk] && TS_ENUMS[tk].indexOf(obj[key][tk]) === -1) {
            errors.push('Invalid timestamp enum: ' + tk + ' = "' + obj[key][tk] + '"');
          }
        }
      }
      // Validate distanceUnit
      if (key === 'distanceUnit' && DISTANCE_UNIT_VALUES.indexOf(obj[key]) === -1) {
        errors.push('Invalid distanceUnit: "' + obj[key] + '" — must be km, mi, or auto');
      }
    }
    return { valid: errors.length === 0, errors: errors };
  }

  // ── Save status indicator ──

  function _updateSaveStatus(status) {
    _saveStatus = status;
    var el = document.getElementById('cv2-save-status');
    if (!el) return;
    if (status === 'saved') { el.textContent = 'All changes saved'; el.style.color = 'var(--text-muted)'; }
    else if (status === 'saving') { el.textContent = 'Saving...'; el.style.color = 'var(--text-muted)'; }
    else if (status === 'error') { el.textContent = '⚠️ Storage full — changes may not be saved'; el.style.color = '#ef4444'; }
  }

  function _showQuotaWarning() {
    // Surface a visible warning in the panel
    _updateSaveStatus('error');
  }

  // ── Customizer panel UI ──

  var _panelEl = null;
  var _activeTab = 'branding';
  var _styleEl = null;

  // GeoFilter tab state
  var _gfMap = null;
  var _gfPoints = [];
  var _gfMarkers = [];
  var _gfPolygon = null;
  var _gfClosingLine = null;
  var _gfLoaded = false; // true after initial server load

  function esc(s) { var d = document.createElement('div'); d.textContent = s || ''; return d.innerHTML; }
  function escAttr(s) { return (s || '').replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;'); }

  function _getEffective() { return window.SITE_CONFIG || computeEffective(_serverDefaults || {}, readOverrides()); }
  function _getOverrides() { return readOverrides(); }
  function _getServer() { return _serverDefaults || {}; }

  /** Check if a specific field is overridden (differs from server default) */
  function _isOverridden(section, key) {
    var overrides = _getOverrides();
    var server = _getServer();
    if (section) {
      if (!overrides[section] || !overrides[section].hasOwnProperty(key)) return false;
      var serverSection = server[section] || {};
      var ov = overrides[section][key];
      var sv = serverSection[key];
      // Deep compare for arrays/objects
      if (typeof ov === 'object' || typeof sv === 'object') {
        return JSON.stringify(ov) !== JSON.stringify(sv);
      }
      return ov !== sv;
    }
    if (!overrides.hasOwnProperty(key)) return false;
    var ov2 = overrides[key];
    var sv2 = server[key];
    if (typeof ov2 === 'object' || typeof sv2 === 'object') {
      return JSON.stringify(ov2) !== JSON.stringify(sv2);
    }
    return ov2 !== sv2;
  }

  /** Count overridden fields in a section (only those that differ from server defaults) */
  function _countOverrides(section) {
    var overrides = _getOverrides();
    if (!overrides[section] || typeof overrides[section] !== 'object') return 0;
    var count = 0;
    var keys = Object.keys(overrides[section]);
    for (var i = 0; i < keys.length; i++) {
      if (_isOverridden(section, keys[i])) count++;
    }
    return count;
  }

  function _overrideDot(section, key) {
    if (!_isOverridden(section, key)) return '';
    return '<span class="cv2-override-dot" data-reset-s="' + (section || '') + '" data-reset-k="' + key + '" title="Reset to server default">●</span>';
  }

  function _injectStyles() {
    if (_styleEl) return;
    _styleEl = document.createElement('style');
    _styleEl.textContent = [
      '.cust-overlay{position:fixed;top:56px;right:12px;z-index:1050;width:480px;height:calc(100vh - 68px);background:var(--card-bg);border:1px solid var(--border);border-radius:10px;box-shadow:0 8px 32px rgba(0,0,0,.3);display:flex;flex-direction:column;resize:both;min-width:320px;min-height:300px;overflow:hidden}',
      '.cust-overlay.hidden{display:none}',
      '.cust-header{display:flex;align-items:center;justify-content:space-between;padding:12px 16px;border-bottom:1px solid var(--border);cursor:move;user-select:none;flex-shrink:0}',
      '.cust-header h2{margin:0;font-size:15px}',
      '.cv2-local-banner{font-size:10px;color:var(--text-muted);padding:4px 16px;background:var(--surface-1);border-bottom:1px solid var(--border);text-align:center;flex-shrink:0}',
      '.cust-close{background:none;border:none;font-size:18px;cursor:pointer;color:var(--text-muted);padding:4px 8px;border-radius:4px}',
      '.cust-close:hover{background:var(--surface-3);color:var(--text)}',
      '.cust-inner{flex:1;display:flex;flex-direction:column;overflow:hidden;min-height:0}',
      '.cust-body{flex:1;overflow-y:auto;min-height:0}',
      '.cust-tabs{display:flex;gap:0;border-bottom:1px solid var(--border);flex-shrink:0}',
      '.cust-tab{padding:8px 10px;cursor:pointer;border:none;background:none;color:var(--text-muted);font-size:12px;font-weight:500;border-bottom:2px solid transparent;margin-bottom:-1px;white-space:nowrap;flex:1;text-align:center}',
      '.cust-tab-text{font-size:10px;display:block}',
      '.cust-tab:hover{color:var(--text)}',
      '.cust-tab.active{color:var(--accent);border-bottom-color:var(--accent)}',
      '.cust-tab .cv2-tab-badge{font-size:9px;background:var(--accent);color:#fff;border-radius:8px;padding:0 4px;margin-left:2px}',
      '.cust-panel{display:none;padding:12px 16px}',
      '.cust-panel.active{display:block}',
      '.cust-field{margin-bottom:12px}',
      '.cust-field label{display:block;font-size:12px;font-weight:600;margin-bottom:3px;color:var(--text)}',
      '.cust-field input[type="text"],.cust-field textarea{width:100%;padding:6px 8px;border:1px solid var(--border);border-radius:6px;font-size:13px;background:var(--input-bg);color:var(--text);box-sizing:border-box}',
      '.cust-field input[type="text"]:focus,.cust-field textarea:focus{outline:none;border-color:var(--accent)}',
      '.cust-color-row{display:flex;align-items:center;gap:8px;margin-bottom:10px}',
      '.cust-color-row>div:first-child{min-width:160px;flex:1}',
      '.cust-color-row label{font-size:12px;font-weight:600;margin:0;display:block}',
      '.cust-hint{font-size:10px;color:var(--text-muted);margin-top:1px;line-height:1.2}',
      '.cust-color-row input[type="color"]{width:40px;height:32px;border:1px solid var(--border);border-radius:6px;cursor:pointer;padding:2px;background:var(--input-bg)}',
      '.cust-color-row .cust-hex{font-family:var(--mono);font-size:12px;color:var(--text-muted);min-width:70px}',
      '.cv2-override-dot{color:var(--accent);cursor:pointer;font-size:10px;margin-left:4px;vertical-align:middle;title:"Reset to server default"}',
      '.cv2-override-dot:hover{color:var(--status-red)}',
      '.cust-node-dot{display:inline-block;width:16px;height:16px;border-radius:50%;vertical-align:middle}',
      '.cust-preview-img{max-width:200px;max-height:60px;margin-top:6px;border-radius:6px;border:1px solid var(--border)}',
      '.cust-list-item{display:flex;flex-direction:column;gap:4px;margin-bottom:8px;padding:8px;background:var(--surface-1);border:1px solid var(--border);border-radius:6px}',
      '.cust-list-row{display:flex;gap:6px;align-items:center}',
      '.cust-list-item input{flex:1;padding:5px 8px;border:1px solid var(--border);border-radius:4px;font-size:12px;background:var(--input-bg);color:var(--text);min-width:0}',
      '.cust-list-item textarea{width:100%;padding:5px 8px;border:1px solid var(--border);border-radius:4px;font-size:11px;font-family:var(--mono);background:var(--input-bg);color:var(--text);resize:vertical;box-sizing:border-box}',
      '.cust-list-item textarea:focus,.cust-list-item input:focus{outline:none;border-color:var(--accent)}',
      '.cust-md-hint{font-size:9px;color:var(--text-muted);margin-top:2px}',
      '.cust-md-hint code{background:var(--surface-2);padding:0 3px;border-radius:2px;font-size:9px}',
      '.cust-list-item .cust-emoji-input{max-width:40px;text-align:center;flex:0 0 40px}',
      '.cust-list-btn{padding:4px 10px;border:1px solid var(--border);border-radius:4px;background:var(--surface-2);color:var(--text-muted);cursor:pointer;font-size:12px}',
      '.cust-list-btn:hover{background:var(--surface-3)}',
      '.cust-list-btn.danger{color:#ef4444}',
      '.cust-list-btn.danger:hover{background:#fef2f2}',
      '.cust-add-btn{display:inline-flex;align-items:center;gap:4px;padding:6px 14px;border:1px dashed var(--border);border-radius:6px;background:none;color:var(--accent);cursor:pointer;font-size:13px;margin-top:4px}',
      '.cust-add-btn:hover{background:var(--hover-bg)}',
      '.cust-export-btns{display:flex;gap:8px;margin-top:8px;flex-wrap:wrap}',
      '.cust-export-btns button{padding:6px 14px;border:none;border-radius:6px;cursor:pointer;font-size:12px;font-weight:500}',
      '.cust-copy-btn{background:var(--accent);color:#fff}',
      '.cust-copy-btn:hover{opacity:.9}',
      '.cust-dl-btn{background:var(--surface-2);color:var(--text);border:1px solid var(--border)!important}',
      '.cust-dl-btn:hover{background:var(--surface-3)}',
      '.cust-reset-all{background:var(--surface-2);color:#ef4444;border:1px solid #ef4444!important}',
      '.cust-reset-all:hover{background:#ef4444;color:#fff}',
      '.cust-section-title{font-size:16px;font-weight:600;margin:0 0 12px}',
      '.cust-preset-btn{display:flex;flex-direction:column;align-items:center;gap:4px;padding:8px 10px;border:2px solid var(--border);border-radius:8px;background:var(--surface-1);cursor:pointer;min-width:72px;color:var(--text)}',
      '.cust-preset-btn.active{border-color:var(--accent);background:var(--selected-bg)}',
      '.cv2-footer{padding:6px 16px;border-top:1px solid var(--border);font-size:11px;color:var(--text-muted);flex-shrink:0;display:flex;justify-content:space-between;align-items:center}',
      '@media(max-width:600px){.cust-overlay{left:8px;right:8px;width:auto;top:56px}.cust-tabs{gap:0}.cust-tab{padding:6px 8px;font-size:11px}.cust-color-row>div:first-child{min-width:120px}}'
    ].join('\n');
    document.head.appendChild(_styleEl);
  }

  function _tabBadge(section) {
    var n = _countOverrides(section);
    return n ? ' <span class="cv2-tab-badge">' + n + '</span>' : '';
  }

  function _renderTabs() {
    var tabs = [
      { id: 'branding', label: '🏷️', title: 'Branding', badge: _tabBadge('branding') },
      { id: 'theme', label: '🎨', title: 'Theme', badge: _tabBadge(isDarkMode() ? 'themeDark' : 'theme') },
      { id: 'nodes', label: '🎯', title: 'Colors', badge: (function () { var n = _countOverrides('nodeColors') + _countOverrides('typeColors'); return n ? ' <span class="cv2-tab-badge">' + n + '</span>' : ''; })() },
      { id: 'home', label: '🏠', title: 'Home', badge: _tabBadge('home') },
      { id: 'display', label: '🖥️', title: 'Display', badge: (function () { var n = _countOverrides('timestamps') + (_isOverridden(null, 'distanceUnit') ? 1 : 0); return n ? ' <span class="cv2-tab-badge">' + n + '</span>' : ''; })() },
      { id: 'geofilter', label: '🗺️', title: 'GeoFilter' },
      { id: 'export', label: '📤', title: 'Export' }
    ];
    return '<div class="cust-tabs">' + tabs.map(function (t) {
      return '<button class="cust-tab' + (t.id === _activeTab ? ' active' : '') + '" data-tab="' + t.id + '" title="' + t.title + '">' +
        t.label + ' <span class="cust-tab-text">' + t.title + '</span>' + (t.badge || '') + '</button>';
    }).join('') + '</div>';
  }

  function _renderColorRow(key, section, effectiveVal, serverDefault) {
    var isFont = key === 'font' || key === 'mono';
    var val = effectiveVal || '';
    var def = serverDefault || '';
    var dot = _overrideDot(section, key);
    var inputHtml;
    if (isFont) {
      inputHtml = '<input type="text" data-cv2-field="' + section + '.' + key + '" value="' + escAttr(val) + '" style="width:160px;font-size:11px;font-family:var(--mono);padding:4px 6px;border:1px solid var(--border);border-radius:4px;background:var(--input-bg);color:var(--text)">';
    } else {
      // Ensure hex is 7 chars for color input
      var hexVal = val.length === 7 ? val : (val.length === 4 ? '#' + val[1] + val[1] + val[2] + val[2] + val[3] + val[3] : val);
      inputHtml = '<input type="color" data-cv2-field="' + section + '.' + key + '" value="' + hexVal + '">' +
        '<span class="cust-hex">' + val + '</span>';
    }
    return '<div class="cust-color-row">' +
      '<div><label>' + (THEME_LABELS[key] || key) + dot + '</label>' +
      '<div class="cust-hint">' + (THEME_HINTS[key] || '') + '</div></div>' +
      inputHtml +
    '</div>';
  }

  function _detectActivePreset() {
    var eff = _getEffective();
    var effTheme = eff.theme || {};
    var effDark = eff.themeDark || {};
    for (var id in PRESETS) {
      var p = PRESETS[id];
      var match = true;
      for (var i = 0; i < THEME_COLOR_KEYS.length && match; i++) {
        var k = THEME_COLOR_KEYS[i];
        if (effTheme[k] !== (p.theme || {})[k] || effDark[k] !== (p.themeDark || {})[k]) match = false;
      }
      if (match && p.nodeColors && eff.nodeColors) {
        for (var nk in p.nodeColors) { if (eff.nodeColors[nk] !== p.nodeColors[nk]) { match = false; break; } }
      }
      if (match && p.typeColors && eff.typeColors) {
        for (var tk in p.typeColors) { if (eff.typeColors[tk] !== p.typeColors[tk]) { match = false; break; } }
      }
      if (match) return id;
    }
    return null;
  }

  function _renderPresets() {
    var active = _detectActivePreset();
    var html = '<div style="margin-bottom:16px"><p class="cust-section-title">Theme Presets</p><div style="display:flex;gap:8px;flex-wrap:wrap">';
    for (var id in PRESETS) {
      var p = PRESETS[id];
      var isActive = id === active;
      var dots = '';
      for (var di = 0; di < p.preview.length; di++) {
        dots += '<span style="display:inline-block;width:12px;height:12px;border-radius:50%;background:' + p.preview[di] + ';border:1px solid rgba(128,128,128,.3)"></span>';
      }
      html += '<button class="cust-preset-btn' + (isActive ? ' active' : '') + '" data-preset="' + id + '">' +
        '<div style="display:flex;gap:3px">' + dots + '</div>' +
        '<span style="font-size:11px;font-weight:' + (isActive ? '700' : '500') + '">' + esc(p.name) + '</span>' +
        '<span style="font-size:9px;color:var(--text-muted)">' + esc(p.desc) + '</span></button>';
    }
    html += '</div></div>';
    return html;
  }

  function _renderBranding() {
    var eff = _getEffective();
    var b = eff.branding || {};
    var logoPreview = b.logoUrl ? '<img class="cust-preview-img" src="' + escAttr(b.logoUrl) + '" alt="Logo preview" onerror="this.style.display=\'none\'">' : '';
    return '<div class="cust-panel' + (_activeTab === 'branding' ? ' active' : '') + '" data-panel="branding">' +
      '<div class="cust-field"><label>Site Name' + _overrideDot('branding', 'siteName') + '</label><input type="text" data-cv2-field="branding.siteName" value="' + escAttr(b.siteName || '') + '"></div>' +
      '<div class="cust-field"><label>Tagline' + _overrideDot('branding', 'tagline') + '</label><input type="text" data-cv2-field="branding.tagline" value="' + escAttr(b.tagline || '') + '"></div>' +
      '<div class="cust-field"><label>Logo URL' + _overrideDot('branding', 'logoUrl') + '</label><input type="text" data-cv2-field="branding.logoUrl" value="' + escAttr(b.logoUrl || '') + '" placeholder="https://...">' + logoPreview + '</div>' +
      '<div class="cust-field"><label>Favicon URL' + _overrideDot('branding', 'faviconUrl') + '</label><input type="text" data-cv2-field="branding.faviconUrl" value="' + escAttr(b.faviconUrl || '') + '" placeholder="https://..."></div>' +
    '</div>';
  }

  function _renderTheme() {
    var dark = isDarkMode();
    var section = dark ? 'themeDark' : 'theme';
    var eff = _getEffective();
    var server = _getServer();
    var current = dark ? Object.assign({}, eff.theme || {}, eff.themeDark || {}) : (eff.theme || {});
    var serverCurrent = dark ? Object.assign({}, server.theme || {}, server.themeDark || {}) : (server.theme || {});
    var modeLabel = dark ? '🌙 Dark Mode' : '☀️ Light Mode';

    var basicRows = '';
    for (var i = 0; i < BASIC_KEYS.length; i++) basicRows += _renderColorRow(BASIC_KEYS[i], section, current[BASIC_KEYS[i]], serverCurrent[BASIC_KEYS[i]]);
    var advancedRows = '';
    for (var j = 0; j < ADVANCED_KEYS.length; j++) advancedRows += _renderColorRow(ADVANCED_KEYS[j], section, current[ADVANCED_KEYS[j]], serverCurrent[ADVANCED_KEYS[j]]);
    var fontRows = '';
    for (var f = 0; f < FONT_KEYS.length; f++) fontRows += _renderColorRow(FONT_KEYS[f], section, current[FONT_KEYS[f]], serverCurrent[FONT_KEYS[f]]);

    return '<div class="cust-panel' + (_activeTab === 'theme' ? ' active' : '') + '" data-panel="theme">' +
      _renderPresets() +
      '<p class="cust-section-title">' + modeLabel + '</p>' +
      '<p style="font-size:11px;color:var(--text-muted);margin:0 0 10px">Toggle ☀️/🌙 in nav to edit the other mode.</p>' +
      basicRows +
      '<details class="cust-advanced"><summary style="font-size:12px;font-weight:600;cursor:pointer;color:var(--text-muted);margin:12px 0 8px">Advanced (' + ADVANCED_KEYS.length + ' options)</summary>' + advancedRows + '</details>' +
      '<details class="cust-fonts" style="margin-top:12px"><summary style="font-size:12px;font-weight:600;cursor:pointer;color:var(--text-muted);margin:12px 0 8px">Fonts</summary>' + fontRows + '</details>' +
    '</div>';
  }

  function _renderNodes() {
    var eff = _getEffective();
    var server = _getServer();
    var nc = eff.nodeColors || {};
    var snc = server.nodeColors || {};
    var rows = '';
    for (var key in NODE_LABELS) {
      var val = nc[key] || '#000000';
      rows += '<div class="cust-color-row">' +
        '<div><label>' + NODE_EMOJI[key] + ' ' + NODE_LABELS[key] + _overrideDot('nodeColors', key) + '</label>' +
        '<div class="cust-hint">' + (NODE_HINTS[key] || '') + '</div></div>' +
        '<input type="color" data-cv2-field="nodeColors.' + key + '" value="' + val + '">' +
        '<span class="cust-node-dot" style="background:' + val + '"></span>' +
        '<span class="cust-hex">' + val + '</span></div>';
    }

    var fallbackTC = (typeof window !== 'undefined' && window.TYPE_COLORS) || {};
    var tc = eff.typeColors || {};
    var stc = server.typeColors || {};
    var typeRows = '';
    for (var tkey in TYPE_LABELS) {
      var tval = tc[tkey] || fallbackTC[tkey] || '#000000';
      typeRows += '<div class="cust-color-row">' +
        '<div><label>' + (TYPE_EMOJI[tkey] || '') + ' ' + TYPE_LABELS[tkey] + _overrideDot('typeColors', tkey) + '</label>' +
        '<div class="cust-hint">' + (TYPE_HINTS[tkey] || '') + '</div></div>' +
        '<input type="color" data-cv2-field="typeColors.' + tkey + '" value="' + tval + '">' +
        '<span class="cust-node-dot" style="background:' + tval + '"></span>' +
        '<span class="cust-hex">' + tval + '</span></div>';
    }

    // Heatmap opacity
    var heatOpacity = typeof eff.heatmapOpacity === 'number' ? eff.heatmapOpacity : 0.25;
    var heatPct = Math.round(heatOpacity * 100);
    var liveHeatOpacity = typeof eff.liveHeatmapOpacity === 'number' ? eff.liveHeatmapOpacity : 0.3;
    var liveHeatPct = Math.round(liveHeatOpacity * 100);

    return '<div class="cust-panel' + (_activeTab === 'nodes' ? ' active' : '') + '" data-panel="nodes">' +
      '<p class="cust-section-title">Node Role Colors</p>' + rows +
      '<hr style="border:none;border-top:1px solid var(--border);margin:16px 0">' +
      '<p class="cust-section-title">Packet Type Colors</p>' + typeRows +
      '<hr style="border:none;border-top:1px solid var(--border);margin:16px 0">' +
      '<p class="cust-section-title">Heatmap Opacity</p>' +
      '<div class="cust-color-row"><div><label>🗺️ Nodes Map' + _overrideDot(null, 'heatmapOpacity') + '</label>' +
        '<div class="cust-hint">Heatmap overlay on the Nodes → Map page (0–100%)</div></div>' +
        '<input type="range" data-cv2-slider="heatmapOpacity" min="0" max="100" value="' + heatPct + '" style="width:120px;cursor:pointer">' +
        '<span class="cust-hex" id="cv2HeatPct">' + heatPct + '%</span></div>' +
      '<div class="cust-color-row"><div><label>📡 Live Map' + _overrideDot(null, 'liveHeatmapOpacity') + '</label>' +
        '<div class="cust-hint">Heatmap overlay on the Live page (0–100%)</div></div>' +
        '<input type="range" data-cv2-slider="liveHeatmapOpacity" min="0" max="100" value="' + liveHeatPct + '" style="width:120px;cursor:pointer">' +
        '<span class="cust-hex" id="cv2LiveHeatPct">' + liveHeatPct + '%</span></div>' +
    '</div>';
  }

  function _renderDisplay() {
    var eff = _getEffective();
    var distUnit = typeof eff.distanceUnit === 'string' && DISTANCE_UNIT_VALUES.indexOf(eff.distanceUnit) >= 0 ? eff.distanceUnit : 'auto';
    var ts = (eff.timestamps) || {};
    var tsMode = ts.defaultMode === 'absolute' ? 'absolute' : 'ago';
    var tsTz = ts.timezone === 'utc' ? 'utc' : 'local';
    var tsFmt = (ts.formatPreset === 'iso-seconds' || ts.formatPreset === 'locale') ? ts.formatPreset : 'iso';
    var customFmt = typeof ts.customFormat === 'string' ? ts.customFormat : '';
    var canCustom = !!(eff.timestamps && eff.timestamps.allowCustomFormat === true);
    var showAbs = tsMode === 'absolute' ? '' : ' style="display:none"';

    return '<div class="cust-panel' + (_activeTab === 'display' ? ' active' : '') + '" data-panel="display">' +
      '<p class="cust-section-title">Display Settings</p>' +
      '<p style="font-size:12px;color:var(--text-muted);margin-bottom:12px">UI preferences that affect how data is shown across pages.</p>' +
      '<p class="cust-section-title" style="font-size:14px;margin-bottom:8px">Timestamps</p>' +
      '<div class="cust-field"><label>Timestamp Display' + _overrideDot('timestamps', 'defaultMode') + '</label>' +
        '<select data-cv2-select="timestamps.defaultMode" style="width:100%;padding:6px 8px;border:1px solid var(--border);border-radius:6px;background:var(--input-bg);color:var(--text)">' +
          '<option value="ago"' + (tsMode === 'ago' ? ' selected' : '') + '>Relative (3m ago)</option>' +
          '<option value="absolute"' + (tsMode === 'absolute' ? ' selected' : '') + '>Absolute (ISO timestamp)</option></select></div>' +
      '<div class="cust-field"><label>Timezone' + _overrideDot('timestamps', 'timezone') + '</label>' +
        '<select data-cv2-select="timestamps.timezone" style="width:100%;padding:6px 8px;border:1px solid var(--border);border-radius:6px;background:var(--input-bg);color:var(--text)">' +
          '<option value="local"' + (tsTz === 'local' ? ' selected' : '') + '>Local time</option>' +
          '<option value="utc"' + (tsTz === 'utc' ? ' selected' : '') + '>UTC</option></select></div>' +
      '<div class="cust-field" data-ts-abs="format"' + showAbs + '><label>Format' + _overrideDot('timestamps', 'formatPreset') + '</label>' +
        '<select data-cv2-select="timestamps.formatPreset" style="width:100%;padding:6px 8px;border:1px solid var(--border);border-radius:6px;background:var(--input-bg);color:var(--text)">' +
          '<option value="iso"' + (tsFmt === 'iso' ? ' selected' : '') + '>ISO (2024-01-15 14:30:00)</option>' +
          '<option value="iso-seconds"' + (tsFmt === 'iso-seconds' ? ' selected' : '') + '>ISO + ms</option>' +
          '<option value="locale"' + (tsFmt === 'locale' ? ' selected' : '') + '>Locale (browser)</option></select></div>' +
      (canCustom ? '<div class="cust-field" data-ts-abs="custom"' + showAbs + '><label>Custom Format' + _overrideDot('timestamps', 'customFormat') + '</label>' +
        '<input type="text" data-cv2-field="timestamps.customFormat" value="' + escAttr(customFmt) + '" placeholder="YYYY-MM-DD HH:mm:ss"></div>' : '') +
      '<p class="cust-section-title" style="font-size:14px;margin:16px 0 8px">Distances</p>' +
      '<div class="cust-field"><label>Distance Unit' + _overrideDot(null, 'distanceUnit') + '</label>' +
        '<select data-cv2-select="distanceUnit" style="width:100%;padding:6px 8px;border:1px solid var(--border);border-radius:6px;background:var(--input-bg);color:var(--text)">' +
          '<option value="auto"' + (distUnit === 'auto' ? ' selected' : '') + '>Auto (browser locale)</option>' +
          '<option value="km"' + (distUnit === 'km' ? ' selected' : '') + '>Kilometers (km)</option>' +
          '<option value="mi"' + (distUnit === 'mi' ? ' selected' : '') + '>Miles (mi)</option>' +
        '</select></div>' +
    '</div>';
  }

  function _renderHome() {
    var eff = _getEffective();
    var h = eff.home || {};
    var steps = h.steps || [];
    var checklist = h.checklist || [];
    var footerLinks = h.footerLinks || [];

    var stepsHtml = steps.map(function (s, i) {
      return '<div class="cust-list-item">' +
        '<div class="cust-list-row">' +
          '<input class="cust-emoji-input" data-cv2-home="steps.' + i + '.emoji" value="' + escAttr(s.emoji) + '" placeholder="📡">' +
          '<input data-cv2-home="steps.' + i + '.title" value="' + escAttr(s.title) + '" placeholder="Title">' +
          '<button class="cust-list-btn" data-cv2-move="steps.' + i + '.up">↑</button>' +
          '<button class="cust-list-btn" data-cv2-move="steps.' + i + '.down">↓</button>' +
          '<button class="cust-list-btn danger" data-cv2-rm="steps.' + i + '">✕</button>' +
        '</div>' +
        '<textarea data-cv2-home="steps.' + i + '.description" placeholder="Description" rows="2">' + esc(s.description) + '</textarea>' +
        '<div class="cust-md-hint">Markdown: <code>**bold**</code> <code>*italic*</code> <code>`code`</code> <code>[text](url)</code></div></div>';
    }).join('');

    var checkHtml = checklist.map(function (c, i) {
      return '<div class="cust-list-item">' +
        '<div class="cust-list-row"><input data-cv2-home="checklist.' + i + '.question" value="' + escAttr(c.question) + '" placeholder="Question">' +
          '<button class="cust-list-btn danger" data-cv2-rm="checklist.' + i + '">✕</button></div>' +
        '<textarea data-cv2-home="checklist.' + i + '.answer" placeholder="Answer" rows="2">' + esc(c.answer) + '</textarea></div>';
    }).join('');

    var linksHtml = footerLinks.map(function (l, i) {
      return '<div class="cust-list-item">' +
        '<div class="cust-list-row"><input data-cv2-home="footerLinks.' + i + '.label" value="' + escAttr(l.label) + '" placeholder="Label">' +
          '<button class="cust-list-btn danger" data-cv2-rm="footerLinks.' + i + '">✕</button></div>' +
        '<input data-cv2-home="footerLinks.' + i + '.url" value="' + escAttr(l.url) + '" placeholder="URL"></div>';
    }).join('');

    return '<div class="cust-panel' + (_activeTab === 'home' ? ' active' : '') + '" data-panel="home">' +
      '<div class="cust-field"><label>Hero Title' + _overrideDot('home', 'heroTitle') + '</label>' +
        '<input type="text" data-cv2-field="home.heroTitle" value="' + escAttr(h.heroTitle || '') + '"></div>' +
      '<div class="cust-field"><label>Hero Subtitle' + _overrideDot('home', 'heroSubtitle') + '</label>' +
        '<input type="text" data-cv2-field="home.heroSubtitle" value="' + escAttr(h.heroSubtitle || '') + '"></div>' +
      '<p class="cust-section-title" style="margin-top:20px">Steps</p>' + stepsHtml +
      '<button class="cust-add-btn" data-cv2-add="steps">+ Add Step</button>' +
      '<p class="cust-section-title" style="margin-top:24px">FAQ / Checklist</p>' + checkHtml +
      '<button class="cust-add-btn" data-cv2-add="checklist">+ Add Question</button>' +
      '<p class="cust-section-title" style="margin-top:24px">Footer Links</p>' + linksHtml +
      '<button class="cust-add-btn" data-cv2-add="footerLinks">+ Add Link</button>' +
    '</div>';
  }

  function _renderGeoFilter() {
    return '<div class="cust-panel' + (_activeTab === 'geofilter' ? ' active' : '') + '" data-panel="geofilter">' +
      '<p class="cust-section-title">Geographic Filter</p>' +
      '<p style="font-size:12px;color:var(--text-muted);margin-bottom:12px">Shows the active geographic filter. Nodes outside this area are excluded at ingest time and in API responses.</p>' +
      '<div id="cv2-gf-map" style="height:220px;border-radius:6px;border:1px solid var(--border);margin-bottom:8px;background:var(--surface-1)"></div>' +
      '<div id="cv2-gf-status" style="font-size:12px;color:var(--text-muted);margin-bottom:10px">Loading current filter…</div>' +
      // Edit controls — hidden until server confirms write access (writeEnabled=true)
      '<div id="cv2-gf-edit" style="display:none">' +
        '<p style="font-size:11px;color:var(--text-muted);margin-bottom:8px">Click the map to add polygon points. Need at least 3 points.</p>' +
        '<div style="display:flex;gap:8px;margin-bottom:10px;align-items:center">' +
          '<button id="cv2-gf-undo" style="padding:5px 10px;background:var(--surface-1);color:var(--text-muted);border:1px solid var(--border);border-radius:6px;cursor:pointer;font-size:12px">↩ Undo</button>' +
          '<button id="cv2-gf-clear-pts" style="padding:5px 10px;background:var(--surface-1);color:var(--text-muted);border:1px solid var(--border);border-radius:6px;cursor:pointer;font-size:12px">✕ Clear</button>' +
          '<label style="font-size:12px;color:var(--text-muted);margin-left:auto">Buffer km:</label>' +
          '<input type="number" id="cv2-gf-buffer" value="20" min="0" max="500" style="width:64px;padding:4px 8px;border:1px solid var(--border);border-radius:6px;background:var(--input-bg);color:var(--text);font-size:12px">' +
        '</div>' +
        '<div class="cust-field"><label>Server API Key</label>' +
          '<input type="password" id="cv2-gf-apikey" placeholder="apiKey from config.json" style="width:100%;padding:5px 8px;border:1px solid var(--border);border-radius:6px;background:var(--input-bg);color:var(--text);font-size:12px">' +
        '</div>' +
        '<div style="display:flex;gap:8px;margin-top:12px">' +
          '<button id="cv2-gf-save" style="padding:7px 16px;background:var(--accent);color:#fff;border:none;border-radius:6px;cursor:pointer;font-size:13px;font-weight:500">Save to server</button>' +
          '<button id="cv2-gf-remove" style="padding:7px 14px;background:var(--surface-1);color:var(--status-red);border:1px solid var(--border);border-radius:6px;cursor:pointer;font-size:13px">Remove filter</button>' +
        '</div>' +
        '<div id="cv2-gf-msg" style="margin-top:8px;font-size:12px;display:none"></div>' +
        // Prune section — only shown when a polygon is active (toggled in _initGeoFilterTab)
        '<div id="cv2-gf-prune-section" style="display:none;margin-top:16px;border-top:1px solid var(--border);padding-top:14px">' +
          '<p class="cust-section-title" style="font-size:13px;margin-bottom:6px">Prune historical nodes</p>' +
          '<p style="font-size:11px;color:var(--text-muted);margin-bottom:10px">Remove nodes already in the database that fall outside the current filter. Run once after first enabling geo filtering.</p>' +
          '<button id="cv2-gf-prune-preview" style="padding:6px 14px;background:var(--surface-1);color:var(--text-muted);border:1px solid var(--border);border-radius:6px;cursor:pointer;font-size:12px">Preview prune</button>' +
          '<div id="cv2-gf-prune-result" style="display:none;margin-top:10px">' +
            '<div id="cv2-gf-prune-list" style="font-size:11px;color:var(--text-muted);max-height:100px;overflow-y:auto;margin-bottom:8px;background:var(--surface-1);border:1px solid var(--border);border-radius:4px;padding:6px 8px"></div>' +
            '<button id="cv2-gf-prune-confirm" style="padding:6px 14px;background:var(--status-red);color:#fff;border:none;border-radius:6px;cursor:pointer;font-size:12px;font-weight:500">Delete nodes</button>' +
          '</div>' +
          '<div id="cv2-gf-prune-msg" style="margin-top:8px;font-size:12px;display:none"></div>' +
        '</div>' +
      '</div>' +
    '</div>';
  }

  function _gfRender() {
    if (!_gfMap) return;
    if (_gfPolygon) { _gfMap.removeLayer(_gfPolygon); _gfPolygon = null; }
    if (_gfClosingLine) { _gfMap.removeLayer(_gfClosingLine); _gfClosingLine = null; }
    _gfMarkers.forEach(function (m) { _gfMap.removeLayer(m); });
    _gfMarkers = [];

    _gfPoints.forEach(function (pt, i) {
      var m = L.circleMarker(pt, { radius: 6, color: '#4a9eff', weight: 2, fillColor: '#4a9eff', fillOpacity: 0.9 })
        .addTo(_gfMap)
        .bindTooltip(String(i + 1), { permanent: true, direction: 'top', offset: [0, -8] });
      _gfMarkers.push(m);
    });

    if (_gfPoints.length >= 3) {
      _gfPolygon = L.polygon(_gfPoints, { color: '#4a9eff', weight: 2, fillColor: '#4a9eff', fillOpacity: 0.12 }).addTo(_gfMap);
    } else if (_gfPoints.length === 2) {
      _gfClosingLine = L.polyline(_gfPoints, { color: '#4a9eff', weight: 2, dashArray: '5,5' }).addTo(_gfMap);
    }
  }

  function _gfStatus(container, msg) {
    var el = container.querySelector('#cv2-gf-status');
    if (el) el.textContent = msg;
  }

  function _gfMsg(container, msg, ok) {
    var el = container.querySelector('#cv2-gf-msg');
    if (!el) return;
    el.textContent = msg;
    el.style.display = msg ? '' : 'none';
    el.style.color = ok ? 'var(--status-green)' : 'var(--status-red)';
  }

  function _gfSave(container) {
    if (_gfPoints.length < 3) { _gfMsg(container, 'Need at least 3 polygon points.', false); return; }
    var apiKey = (container.querySelector('#cv2-gf-apikey') || {}).value || '';
    if (!apiKey) { _gfMsg(container, 'API key required to save.', false); return; }
    var bufferKm = parseFloat((container.querySelector('#cv2-gf-buffer') || {}).value) || 0;
    fetch('/api/config/geo-filter', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'X-API-Key': apiKey },
      body: JSON.stringify({ polygon: _gfPoints, bufferKm: bufferKm })
    }).then(function (r) {
      if (!r.ok) return r.json().then(function (e) { throw new Error(e.error || ('HTTP ' + r.status)); });
      _gfMsg(container, 'Saved. Filter is active immediately.', true);
      _gfStatus(container, _gfPoints.length + ' points · bufferKm=' + bufferKm + ' · saved');
    }).catch(function (e) { _gfMsg(container, 'Error: ' + e.message, false); });
  }

  function _gfRemove(container) {
    var apiKey = (container.querySelector('#cv2-gf-apikey') || {}).value || '';
    if (!apiKey) { _gfMsg(container, 'API key required.', false); return; }
    if (!confirm('Remove geo filter? All nodes will be allowed through.')) return;
    fetch('/api/config/geo-filter', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'X-API-Key': apiKey },
      body: JSON.stringify({ polygon: null })
    }).then(function (r) {
      if (!r.ok) return r.json().then(function (e) { throw new Error(e.error || ('HTTP ' + r.status)); });
      _gfPoints = []; _gfLoaded = true;
      _gfRender();
      _gfStatus(container, 'No geo filter. Click the map to draw a polygon.');
      _gfMsg(container, 'Geo filter removed.', true);
    }).catch(function (e) { _gfMsg(container, 'Error: ' + e.message, false); });
  }

  var _gfPruneNodes = []; // nodes returned by last dry-run preview

  function _gfPruneMsg(container, msg, ok) {
    var el = container.querySelector('#cv2-gf-prune-msg');
    if (!el) return;
    el.textContent = msg;
    el.style.display = msg ? '' : 'none';
    el.style.color = ok ? 'var(--status-green)' : 'var(--status-red)';
  }

  function _gfPrunePreview(container) {
    var apiKey = (container.querySelector('#cv2-gf-apikey') || {}).value || '';
    if (!apiKey) { _gfPruneMsg(container, 'API key required.', false); return; }
    var btn = container.querySelector('#cv2-gf-prune-preview');
    if (btn) btn.textContent = 'Loading…';
    fetch('/api/admin/prune-geo-filter', {
      method: 'POST',
      headers: { 'X-API-Key': apiKey }
    }).then(function (r) {
      if (!r.ok) return r.json().then(function (e) { throw new Error(e.error || ('HTTP ' + r.status)); });
      return r.json();
    }).then(function (data) {
      if (btn) btn.textContent = 'Preview prune';
      _gfPruneNodes = data.nodes || [];
      var count = data.count || 0;
      var resultEl = container.querySelector('#cv2-gf-prune-result');
      var listEl = container.querySelector('#cv2-gf-prune-list');
      var confirmBtn = container.querySelector('#cv2-gf-prune-confirm');
      if (!resultEl || !listEl || !confirmBtn) return;
      if (count === 0) {
        _gfPruneMsg(container, 'No nodes outside the filter. Nothing to prune.', true);
        resultEl.style.display = 'none';
        return;
      }
      listEl.innerHTML = _gfPruneNodes.map(function (n) {
        var coords = n.lat != null ? (' · ' + n.lat.toFixed(4) + ', ' + n.lon.toFixed(4)) : '';
        return '<div>' + (n.name || n.pubkey.slice(0, 12)) + coords + '</div>';
      }).join('');
      confirmBtn.textContent = 'Delete ' + count + ' node' + (count !== 1 ? 's' : '');
      resultEl.style.display = '';
      _gfPruneMsg(container, '', true);
    }).catch(function (e) {
      if (btn) btn.textContent = 'Preview prune';
      _gfPruneMsg(container, 'Error: ' + e.message, false);
    });
  }

  function _gfPruneConfirm(container) {
    if (!_gfPruneNodes.length) { _gfPruneMsg(container, 'Run preview first.', false); return; }
    var apiKey = (container.querySelector('#cv2-gf-apikey') || {}).value || '';
    if (!apiKey) { _gfPruneMsg(container, 'API key required.', false); return; }
    var count = _gfPruneNodes.length;
    if (!confirm('Delete ' + count + ' node' + (count !== 1 ? 's' : '') + ' from the database? This cannot be undone.')) return;
    fetch('/api/admin/prune-geo-filter?confirm=true', {
      method: 'POST',
      headers: { 'X-API-Key': apiKey }
    }).then(function (r) {
      if (!r.ok) return r.json().then(function (e) { throw new Error(e.error || ('HTTP ' + r.status)); });
      return r.json();
    }).then(function (data) {
      _gfPruneNodes = [];
      var resultEl = container.querySelector('#cv2-gf-prune-result');
      if (resultEl) resultEl.style.display = 'none';
      _gfPruneMsg(container, 'Deleted ' + data.deleted + ' node' + (data.deleted !== 1 ? 's' : '') + '.', true);
    }).catch(function (e) { _gfPruneMsg(container, 'Error: ' + e.message, false); });
  }

  function _initGeoFilterTab(container) {
    var mapEl = container.querySelector('#cv2-gf-map');
    if (!mapEl || typeof L === 'undefined') return;

    _gfMap = L.map(mapEl, { zoomControl: true });
    L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
      attribution: '© OpenStreetMap © CartoDB', maxZoom: 19
    }).addTo(_gfMap);

    if (!_gfLoaded) {
      api('/config/geo-filter', { ttl: 0 }).then(function (gf) {
        // Show edit controls only on servers that have a write-capable API key configured
        if (gf && gf.writeEnabled) {
          var editEl = container.querySelector('#cv2-gf-edit');
          if (editEl) editEl.style.display = '';
        }
        if (gf && gf.polygon && gf.polygon.length >= 3) {
          _gfPoints = gf.polygon.map(function (p) { return [p[0], p[1]]; });
          var buf = container.querySelector('#cv2-gf-buffer');
          if (buf) buf.value = gf.bufferKm || 0;
          _gfRender();
          if (_gfPolygon) _gfMap.fitBounds(_gfPolygon.getBounds(), { padding: [20, 20] });
          _gfStatus(container, gf.polygon.length + ' points · bufferKm=' + (gf.bufferKm || 0));
          // Show prune section when a polygon is active and write access is available
          if (gf.writeEnabled) {
            var pruneEl = container.querySelector('#cv2-gf-prune-section');
            if (pruneEl) pruneEl.style.display = '';
          }
        } else {
          _gfPoints = [];
          _gfStatus(container, gf && gf.writeEnabled ? 'No geo filter. Click the map to draw a polygon.' : 'No geo filter configured.');
          _gfMap.setView([50.5, 4.4], 5);
        }
        _gfLoaded = true;
        setTimeout(function () { if (_gfMap) _gfMap.invalidateSize(); }, 100);
      }).catch(function () {
        _gfStatus(container, 'Could not load current filter.');
        _gfMap.setView([50.5, 4.4], 5);
        _gfLoaded = true;
        setTimeout(function () { if (_gfMap) _gfMap.invalidateSize(); }, 100);
      });
    } else {
      if (_gfPoints.length >= 3) {
        _gfRender();
        if (_gfPolygon) _gfMap.fitBounds(_gfPolygon.getBounds(), { padding: [20, 20] });
        _gfStatus(container, _gfPoints.length + ' points.');
      } else {
        _gfMap.setView([50.5, 4.4], 5);
        _gfStatus(container, _gfPoints.length ? _gfPoints.length + ' points (need at least 3).' : 'Click the map to draw a polygon.');
        _gfRender();
      }
      setTimeout(function () { if (_gfMap) _gfMap.invalidateSize(); }, 100);
    }

    _gfMap.on('click', function (e) {
      _gfPoints.push([parseFloat(e.latlng.lat.toFixed(6)), parseFloat(e.latlng.lng.toFixed(6))]);
      _gfRender();
      _gfStatus(container, _gfPoints.length + ' point' + (_gfPoints.length !== 1 ? 's' : '') + '.');
    });

    container.querySelector('#cv2-gf-undo').addEventListener('click', function () {
      if (!_gfPoints.length) return;
      _gfPoints.pop();
      _gfRender();
      _gfStatus(container, _gfPoints.length + ' point' + (_gfPoints.length !== 1 ? 's' : '') + '.');
    });
    container.querySelector('#cv2-gf-clear-pts').addEventListener('click', function () {
      _gfPoints = [];
      _gfRender();
      _gfStatus(container, 'Cleared. Click the map to draw a polygon.');
    });
    container.querySelector('#cv2-gf-save').addEventListener('click', function () { _gfSave(container); });
    container.querySelector('#cv2-gf-remove').addEventListener('click', function () { _gfRemove(container); });

    var prunePreviewBtn = container.querySelector('#cv2-gf-prune-preview');
    var pruneConfirmBtn = container.querySelector('#cv2-gf-prune-confirm');
    if (prunePreviewBtn) prunePreviewBtn.addEventListener('click', function () { _gfPrunePreview(container); });
    if (pruneConfirmBtn) pruneConfirmBtn.addEventListener('click', function () { _gfPruneConfirm(container); });
  }

  function _renderExport() {
    var delta = readOverrides();
    var json = JSON.stringify(delta, null, 2);
    var hasDelta = Object.keys(delta).length > 0;

    return '<div class="cust-panel' + (_activeTab === 'export' ? ' active' : '') + '" data-panel="export">' +
      '<p class="cust-section-title">Export / Import</p>' +
      '<p style="font-size:12px;color:var(--text-muted);margin-bottom:8px">Your customizations are stored in your browser. Export to share or back up.</p>' +
      '<div class="cust-export-btns">' +
        '<button class="cust-dl-btn" id="cv2Download">💾 Download JSON</button>' +
        '<button class="cust-dl-btn" id="cv2ImportFile">📂 Import File</button>' +
        '<input type="file" id="cv2ImportInput" accept=".json,application/json" style="display:none">' +
        '<button class="cust-copy-btn" id="cv2Copy">📋 Copy</button>' +
      '</div>' +
      (hasDelta ? '<div style="margin-top:12px"><button class="cust-reset-all" id="cv2ResetAll">🗑️ Reset All Customizations</button></div>' : '') +
      '<details style="margin-top:12px"><summary style="font-size:12px;font-weight:600;cursor:pointer;color:var(--text-muted)">Raw JSON</summary>' +
        '<textarea id="cv2ExportJson" style="width:100%;min-height:200px;font-family:var(--mono);font-size:12px;background:var(--surface-1);border:1px solid var(--border);border-radius:6px;padding:12px;color:var(--text);resize:vertical;box-sizing:border-box;margin-top:8px">' + esc(json) + '</textarea>' +
      '</details>' +
    '</div>';
  }

  function _renderPanel(container) {
    container.innerHTML =
      _renderTabs() +
      '<div class="cust-body">' +
        _renderBranding() +
        _renderTheme() +
        _renderNodes() +
        _renderHome() +
        _renderDisplay() +
        _renderGeoFilter() +
        _renderExport() +
      '</div>';
    _bindEvents(container);
  }

  /** Remove phantom overrides that match server defaults on startup */
  function _cleanPhantomOverrides() {
    var delta = readOverrides();
    if (!delta || Object.keys(delta).length === 0) return;
    var server = _serverDefaults || {};
    var changed = false;

    // Clean object sections
    for (var i = 0; i < OBJECT_SECTIONS.length; i++) {
      var sec = OBJECT_SECTIONS[i];
      if (!delta[sec] || typeof delta[sec] !== 'object') continue;
      var serverSec = server[sec];
      // If server has no defaults for this section, only remove values that
      // are clearly phantom (empty arrays/objects or undefined equivalents).
      // Non-trivial values may be legitimate user choices.
      if (!serverSec) {
        var dKeys = Object.keys(delta[sec]);
        for (var di = 0; di < dKeys.length; di++) {
          var dv = delta[sec][dKeys[di]];
          var isPhantom = (Array.isArray(dv) && dv.length === 0) ||
            (typeof dv === 'object' && dv !== null && !Array.isArray(dv) && Object.keys(dv).length === 0);
          if (isPhantom) { delete delta[sec][dKeys[di]]; changed = true; }
        }
        if (Object.keys(delta[sec]).length === 0) { delete delta[sec]; changed = true; }
        continue;
      }
      var keys = Object.keys(delta[sec]);
      for (var j = 0; j < keys.length; j++) {
        var k = keys[j];
        var ov = delta[sec][k];
        var sv = serverSec[k];
        var match = false;
        if (typeof ov === 'object' || typeof sv === 'object') {
          match = JSON.stringify(ov) === JSON.stringify(sv);
        } else {
          match = ov === sv;
        }
        if (match) { delete delta[sec][k]; changed = true; }
      }
      if (Object.keys(delta[sec]).length === 0) { delete delta[sec]; changed = true; }
    }

    // Clean scalar sections
    for (var si = 0; si < SCALAR_SECTIONS.length; si++) {
      var sk = SCALAR_SECTIONS[si];
      if (delta.hasOwnProperty(sk) && delta[sk] === server[sk]) {
        delete delta[sk]; changed = true;
      }
    }

    if (changed) writeOverrides(delta);
  }

  function _refreshPanel() {
    if (!_panelEl) return;
    var inner = _panelEl.querySelector('.cust-inner');
    if (inner) _renderPanel(inner);
  }

  function _bindEvents(container) {
    // Tab switching
    container.querySelectorAll('.cust-tab').forEach(function (btn) {
      btn.addEventListener('click', function () {
        if (_gfMap) { _gfMap.remove(); _gfMap = null; _gfMarkers = []; _gfPolygon = null; _gfClosingLine = null; }
        _activeTab = btn.dataset.tab;
        _renderPanel(container);
      });
    });

    // GeoFilter tab init
    if (_activeTab === 'geofilter') _initGeoFilterTab(container);

    // Preset buttons
    container.querySelectorAll('.cust-preset-btn').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var id = btn.dataset.preset;
        var p = PRESETS[id];
        if (!p) return;
        // "Reset to Default" preset = clear all overrides (full reset per spec)
        if (id === 'default') {
          localStorage.removeItem(STORAGE_KEY);
        } else {
          // Other presets: write preset data as delta (replaces entire delta per spec)
          var delta = {};
          if (p.theme) delta.theme = JSON.parse(JSON.stringify(p.theme));
          if (p.themeDark) delta.themeDark = JSON.parse(JSON.stringify(p.themeDark));
          if (p.nodeColors) delta.nodeColors = JSON.parse(JSON.stringify(p.nodeColors));
          if (p.typeColors) delta.typeColors = JSON.parse(JSON.stringify(p.typeColors));
          writeOverrides(delta);
        }
        _runPipeline();
        _renderPanel(container);
      });
    });

    // Override dot reset buttons
    container.querySelectorAll('.cv2-override-dot').forEach(function (dot) {
      dot.addEventListener('click', function (e) {
        e.stopPropagation();
        var s = dot.dataset.resetS || null;
        var k = dot.dataset.resetK;
        if (s === '') s = null;
        clearOverride(s, k);
      });
    });

    // Text/color inputs (unified via data-cv2-field="section.key")
    container.querySelectorAll('[data-cv2-field]').forEach(function (inp) {
      var parts = inp.dataset.cv2Field.split('.');
      var section = parts[0];
      var key = parts[1];
      // Optimistic CSS for color pickers on input event
      if (inp.type === 'color') {
        inp.addEventListener('input', function () {
          // Optimistic CSS update (Decision #12)
          var cssVar = THEME_CSS_MAP[key];
          if (cssVar) document.documentElement.style.setProperty(cssVar, inp.value);
          // Update hex display
          var hex = inp.parentElement.querySelector('.cust-hex');
          if (hex) hex.textContent = inp.value;
          // Update node dot
          var dot = inp.parentElement.querySelector('.cust-node-dot');
          if (dot) dot.style.background = inp.value;
        });
        inp.addEventListener('change', function () {
          setOverride(section, key, inp.value);
        });
      } else {
        // Text inputs — debounced write on input
        inp.addEventListener('input', function () {
          setOverride(section, key, inp.value);
          // Live branding updates
          if (section === 'branding' && key === 'siteName') {
            var el = document.querySelector('.brand-text');
            if (el) el.textContent = inp.value;
            document.title = inp.value;
          }
          if (section === 'branding' && key === 'logoUrl') {
            var iconEl = document.querySelector('.brand-icon');
            if (iconEl) {
              if (inp.value) iconEl.innerHTML = '<img src="' + inp.value + '" style="height:24px" onerror="this.style.display=\'none\'">';
              else iconEl.textContent = '📡';
            }
          }
          if (section === 'branding' && key === 'faviconUrl') {
            var link = document.querySelector('link[rel="icon"]');
            if (link && inp.value) link.href = inp.value;
          }
        });
      }
    });

    // Select elements
    container.querySelectorAll('[data-cv2-select]').forEach(function (sel) {
      sel.addEventListener('change', function () {
        var parts = sel.dataset.cv2Select.split('.');
        if (parts.length === 1) {
          setOverride(null, parts[0], sel.value);
        } else {
          setOverride(parts[0], parts[1], sel.value);
          // Show/hide absolute-only fields
          if (parts[1] === 'defaultMode') {
            container.querySelectorAll('[data-ts-abs]').forEach(function (el) {
              el.style.display = sel.value === 'absolute' ? '' : 'none';
            });
          }
        }
        window.dispatchEvent(new CustomEvent('timestamp-mode-changed'));
      });
    });

    // Slider inputs (heatmap opacity)
    container.querySelectorAll('[data-cv2-slider]').forEach(function (inp) {
      var key = inp.dataset.cv2Slider;
      inp.addEventListener('input', function () {
        var pct = parseInt(inp.value);
        var label = key === 'heatmapOpacity' ? document.getElementById('cv2HeatPct') : document.getElementById('cv2LiveHeatPct');
        if (label) label.textContent = pct + '%';
        var opacity = pct / 100;
        // Optimistic: update heatmap layer directly
        if (key === 'heatmapOpacity' && window._meshcoreHeatLayer) {
          var canvas = window._meshcoreHeatLayer._canvas || (window._meshcoreHeatLayer.getContainer && window._meshcoreHeatLayer.getContainer());
          if (canvas) canvas.style.opacity = opacity;
        }
        if (key === 'liveHeatmapOpacity' && window._meshcoreLiveHeatLayer) {
          var canvas2 = window._meshcoreLiveHeatLayer._canvas || (window._meshcoreLiveHeatLayer.getContainer && window._meshcoreLiveHeatLayer.getContainer());
          if (canvas2) canvas2.style.opacity = opacity;
        }
      });
      inp.addEventListener('change', function () {
        setOverride(null, key, parseInt(inp.value) / 100);
      });
    });

    // Home page list editing
    container.querySelectorAll('[data-cv2-home]').forEach(function (inp) {
      inp.addEventListener('input', function () {
        // Parse: steps.0.title → home.steps[0].title
        var path = inp.dataset.cv2Home.split('.');
        var eff = _getEffective();
        var home = JSON.parse(JSON.stringify(eff.home || {}));
        var arr = home[path[0]];
        if (arr && arr[parseInt(path[1])]) {
          arr[parseInt(path[1])][path[2]] = inp.value;
          setOverride('home', path[0], arr);
        }
      });
    });

    // Home list move/remove
    container.querySelectorAll('[data-cv2-move]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var parts = btn.dataset.cv2Move.split('.');
        var listKey = parts[0];
        var idx = parseInt(parts[1]);
        var dir = parts[2] === 'up' ? -1 : 1;
        var eff = _getEffective();
        var home = JSON.parse(JSON.stringify(eff.home || {}));
        var arr = home[listKey];
        if (!arr) return;
        var j = idx + dir;
        if (j < 0 || j >= arr.length) return;
        var tmp = arr[idx]; arr[idx] = arr[j]; arr[j] = tmp;
        setOverride('home', listKey, arr);
      });
    });
    container.querySelectorAll('[data-cv2-rm]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var parts = btn.dataset.cv2Rm.split('.');
        var listKey = parts[0];
        var idx = parseInt(parts[1]);
        var eff = _getEffective();
        var home = JSON.parse(JSON.stringify(eff.home || {}));
        var arr = home[listKey];
        if (!arr) return;
        arr.splice(idx, 1);
        setOverride('home', listKey, arr);
      });
    });
    container.querySelectorAll('[data-cv2-add]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var listKey = btn.dataset.cv2Add;
        var eff = _getEffective();
        var home = JSON.parse(JSON.stringify(eff.home || {}));
        var arr = home[listKey] || [];
        if (listKey === 'steps') arr.push({ emoji: '📌', title: '', description: '' });
        else if (listKey === 'checklist') arr.push({ question: '', answer: '' });
        else if (listKey === 'footerLinks') arr.push({ label: '', url: '' });
        setOverride('home', listKey, arr);
      });
    });

    // Export buttons
    var dlBtn = document.getElementById('cv2Download');
    if (dlBtn) dlBtn.addEventListener('click', function () {
      var json = JSON.stringify(readOverrides(), null, 2);
      var blob = new Blob([json], { type: 'application/json' });
      var a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'corescope-theme.json';
      a.click();
      URL.revokeObjectURL(a.href);
    });

    var copyBtn = document.getElementById('cv2Copy');
    if (copyBtn) copyBtn.addEventListener('click', function () {
      var json = JSON.stringify(readOverrides(), null, 2);
      if (window.copyToClipboard) {
        window.copyToClipboard(json, function () {
          copyBtn.textContent = '✓ Copied!';
          setTimeout(function () { copyBtn.textContent = '📋 Copy'; }, 2000);
        });
      }
    });

    // Import
    var importBtn = document.getElementById('cv2ImportFile');
    var importInput = document.getElementById('cv2ImportInput');
    if (importBtn && importInput) {
      importBtn.addEventListener('click', function () { importInput.click(); });
      importInput.addEventListener('change', function () {
        var file = importInput.files[0];
        if (!file) return;
        var reader = new FileReader();
        reader.onload = function () {
          try {
            var data = JSON.parse(reader.result);
            var result = validateShape(data);
            if (!result.valid) {
              importBtn.textContent = '✕ ' + result.errors[0];
              setTimeout(function () { importBtn.textContent = '📂 Import File'; }, 3000);
              return;
            }
            writeOverrides(data);
            _runPipeline();
            _renderPanel(container);
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

    // Reset All
    var resetBtn = document.getElementById('cv2ResetAll');
    if (resetBtn) resetBtn.addEventListener('click', function () {
      if (!confirm('Reset all customizations to server defaults?')) return;
      localStorage.removeItem(STORAGE_KEY);
      _runPipeline();
      _renderPanel(container);
    });
  }

  // ── Panel toggle ──

  function toggle() {
    if (_panelEl) {
      _panelEl.classList.toggle('hidden');
      if (!_panelEl.classList.contains('hidden')) _refreshPanel();
      return;
    }
    _injectStyles();
    _panelEl = document.createElement('div');
    _panelEl.className = 'cust-overlay';
    _panelEl.innerHTML =
      '<div class="cust-header"><h2>🎨 Customize</h2><button class="cust-close" title="Close">✕</button></div>' +
      '<div class="cv2-local-banner">These settings are saved in your browser only and don\'t affect other users.</div>' +
      '<div class="cust-inner"></div>' +
      '<div class="cv2-footer"><span id="cv2-save-status">All changes saved</span></div>';
    document.body.appendChild(_panelEl);

    _panelEl.querySelector('.cust-close').addEventListener('click', function () {
      if (_gfMap) { _gfMap.remove(); _gfMap = null; _gfMarkers = []; _gfPolygon = null; _gfClosingLine = null; }
      _panelEl.classList.add('hidden');
    });

    // Drag support
    var header = _panelEl.querySelector('.cust-header');
    header.addEventListener('mousedown', function (e) {
      if (e.target.closest('.cust-close')) return;
      var dragX = _panelEl.offsetLeft, dragY = _panelEl.offsetTop;
      var startX = e.clientX, startY = e.clientY;
      var onMove = function (ev) {
        _panelEl.style.left = Math.max(0, dragX + ev.clientX - startX) + 'px';
        _panelEl.style.top = Math.max(56, dragY + ev.clientY - startY) + 'px';
        _panelEl.style.right = 'auto';
      };
      var onUp = function () { document.removeEventListener('mousemove', onMove); document.removeEventListener('mouseup', onUp); };
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });

    _renderPanel(_panelEl.querySelector('.cust-inner'));
    _updateSaveStatus(_saveStatus);
  }

  // ── Initialization (runs immediately on script load) ──

  // 1. Migration check
  migrateOldKeys();

  // 2. Read overrides and apply CSS immediately (before DOMContentLoaded)
  // Server defaults will be set later when /api/config/theme completes.
  // For now, apply whatever overrides exist on top of current SITE_CONFIG.
  var earlyOverrides = readOverrides();
  if (Object.keys(earlyOverrides).length > 0) {
    var earlyServer = window.SITE_CONFIG || {};
    var earlyEffective = computeEffective(earlyServer, earlyOverrides);
    // Don't fully overwrite SITE_CONFIG yet — just apply CSS vars
    var dark = isDarkMode();
    var themeSection = dark
      ? Object.assign({}, earlyEffective.theme || {}, earlyEffective.themeDark || {})
      : (earlyEffective.theme || {});
    var root = document.documentElement.style;
    for (var key in THEME_CSS_MAP) {
      if (themeSection[key]) root.setProperty(THEME_CSS_MAP[key], themeSection[key]);
    }
    if (themeSection.background) root.setProperty('--content-bg', themeSection.contentBg || themeSection.background);
    if (themeSection.surface1) root.setProperty('--card-bg', themeSection.cardBg || themeSection.surface1);
    // Apply node/type colors from overrides early
    if (earlyOverrides.nodeColors) {
      for (var role in earlyOverrides.nodeColors) {
        if (window.ROLE_COLORS && role in window.ROLE_COLORS) window.ROLE_COLORS[role] = earlyOverrides.nodeColors[role];
        if (window.ROLE_STYLE && window.ROLE_STYLE[role]) window.ROLE_STYLE[role].color = earlyOverrides.nodeColors[role];
      }
    }
    if (earlyOverrides.typeColors && window.TYPE_COLORS) {
      for (var type in earlyOverrides.typeColors) {
        if (type in window.TYPE_COLORS) window.TYPE_COLORS[type] = earlyOverrides.typeColors[type];
      }
      if (window.syncBadgeColors) window.syncBadgeColors();
    }
  }

  // 3. Wire up toggle button + watch dark mode changes (needs DOM)
  document.addEventListener('DOMContentLoaded', function () {
    var btn = document.getElementById('customizeToggle');
    if (btn) btn.addEventListener('click', toggle);

    // Re-apply branding from overrides once DOM is ready
    var overrides = readOverrides();
    if (overrides.branding) {
      if (overrides.branding.siteName) {
        var brandEl = document.querySelector('.brand-text');
        if (brandEl) brandEl.textContent = overrides.branding.siteName;
        document.title = overrides.branding.siteName;
      }
      if (overrides.branding.logoUrl) {
        var iconEl = document.querySelector('.brand-icon');
        if (iconEl) iconEl.innerHTML = '<img src="' + overrides.branding.logoUrl + '" style="height:24px" onerror="this.style.display=\'none\'">';
      }
      if (overrides.branding.faviconUrl) {
        var link = document.querySelector('link[rel="icon"]');
        if (link) link.href = overrides.branding.faviconUrl;
      }
    }

    // Watch dark/light mode toggle and re-apply
    new MutationObserver(function () {
      _runPipeline();
      if (_panelEl && !_panelEl.classList.contains('hidden')) _refreshPanel();
    }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  });

  // ── Public API for app.js integration ──

  /**
   * Called by app.js after /api/config/theme fetch completes.
   * Sets server defaults and runs the full pipeline.
   */
  window._customizerV2 = {
    init: function (serverConfig) {
      _serverDefaults = serverConfig || {};
      _cleanPhantomOverrides();
      _runPipeline();
      _initDone = true;
    },
    /** True after init() has been called with server config and pipeline has run */
    get initDone() { return _initDone; },
    readOverrides: readOverrides,
    writeOverrides: writeOverrides,
    computeEffective: computeEffective,
    setOverride: setOverride,
    clearOverride: clearOverride,
    migrateOldKeys: migrateOldKeys,
    validateShape: validateShape,
    applyCSS: applyCSS,
    isValidColor: isValidColor,
    isOverridden: _isOverridden,
    THEME_CSS_MAP: THEME_CSS_MAP
  };
})();
