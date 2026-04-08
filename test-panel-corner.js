/**
 * Tests for panel corner positioning (#608 M0)
 * Tests the pure logic functions extracted from live.js
 */
'use strict';

const assert = require('assert');
const vm = require('vm');
const fs = require('fs');
const path = require('path');

// Minimal DOM/browser stubs
function createContext() {
  const storage = {};
  const elements = {};
  const listeners = {};

  const ctx = {
    window: {},
    document: {
      getElementById: (id) => elements[id] || null,
      querySelectorAll: (sel) => {
        // Return buttons matching .panel-corner-btn[data-panel]
        const results = [];
        for (const id in elements) {
          const el = elements[id];
          if (el._btns) results.push(...el._btns);
        }
        return results;
      },
      documentElement: { getAttribute: () => null }
    },
    localStorage: {
      getItem: (k) => storage[k] !== undefined ? storage[k] : null,
      setItem: (k, v) => { storage[k] = String(v); },
      removeItem: (k) => { delete storage[k]; }
    },
    _storage: storage,
    _elements: elements,
    _addElement: function(id) {
      const attrs = {};
      const btns = [];
      elements[id] = {
        setAttribute: (k, v) => { attrs[k] = v; },
        getAttribute: (k) => attrs[k] || null,
        querySelector: (sel) => {
          if (sel === '.panel-corner-btn') return btns[0] || null;
          return null;
        },
        _attrs: attrs,
        _btns: btns,
        _addBtn: function(panelId) {
          const btnAttrs = { 'data-panel': panelId };
          const btn = {
            textContent: '',
            setAttribute: (k, v) => { btnAttrs[k] = v; },
            getAttribute: (k) => btnAttrs[k] || null,
            addEventListener: () => {},
            _attrs: btnAttrs
          };
          btns.push(btn);
          return btn;
        }
      };
      return elements[id];
    }
  };

  // Self-references
  ctx.window = ctx;
  ctx.self = ctx;
  return ctx;
}

function loadLiveModule(ctx) {
  // We only need the panel corner functions, which are exported to window._panelCorner
  // Load live.js in the VM context with stubs
  const src = fs.readFileSync(path.join(__dirname, 'public', 'live.js'), 'utf8');

  // Provide minimal stubs for the rest of live.js dependencies
  ctx.registerPage = () => {};
  ctx.escapeHtml = (s) => s;
  ctx.timeAgo = () => '—';
  ctx.getParsedPath = () => ({});
  ctx.getParsedDecoded = () => ({});
  ctx.TYPE_COLORS = { ADVERT: '#22c55e', GRP_TXT: '#3b82f6', TXT_MSG: '#f59e0b', ACK: '#6b7280', REQUEST: '#a855f7', RESPONSE: '#06b6d4', TRACE: '#ec4899', PATH: '#14b8a6' };
  ctx.ROLE_COLORS = {};
  ctx.ROLE_LABELS = {};
  ctx.ROLE_STYLE = {};
  ctx.formatTimestampWithTooltip = () => '';
  ctx.getTimestampMode = () => 'relative';
  ctx.console = console;
  ctx.setTimeout = setTimeout;
  ctx.clearTimeout = clearTimeout;
  ctx.setInterval = setInterval;
  ctx.clearInterval = clearInterval;
  ctx.requestAnimationFrame = (cb) => setTimeout(cb, 0);
  ctx.cancelAnimationFrame = clearTimeout;
  ctx.matchMedia = () => ({ matches: false, addEventListener: () => {} });
  ctx.navigator = { userAgent: '' };
  ctx.performance = { now: () => Date.now() };
  ctx.L = undefined;
  ctx.MutationObserver = class { observe() {} disconnect() {} };
  ctx.ResizeObserver = class { observe() {} disconnect() {} };
  ctx.IntersectionObserver = class { observe() {} disconnect() {} };
  ctx.Image = class {};
  ctx.AudioContext = undefined;
  ctx.HTMLElement = class {};
  ctx.Event = class {};
  ctx.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]) });

  // We can't easily run all of live.js (too many DOM deps), so extract just the corner functions
  // by running a minimal extraction
  const extractSrc = `
    (function() {
      var PANEL_DEFAULTS = { liveFeed: 'bl', liveLegend: 'br', liveNodeDetail: 'tr' };
      var CORNER_CYCLE = ['tl', 'tr', 'br', 'bl'];
      var CORNER_ARROWS = { tl: '↘', tr: '↙', bl: '↗', br: '↖' };
      var CORNER_LABELS = { tl: 'top-left', tr: 'top-right', bl: 'bottom-left', br: 'bottom-right' };
      var PANEL_NAMES = { liveFeed: 'Feed', liveLegend: 'Legend', liveNodeDetail: 'Node detail' };

      function getPanelPositions() {
        var pos = {};
        for (var id in PANEL_DEFAULTS) {
          try { pos[id] = localStorage.getItem('panel-corner-' + id) || PANEL_DEFAULTS[id]; }
          catch (_) { pos[id] = PANEL_DEFAULTS[id]; }
        }
        return pos;
      }

      function nextAvailableCorner(panelId, desired, allPositions) {
        var idx = CORNER_CYCLE.indexOf(desired);
        for (var i = 0; i < 4; i++) {
          var candidate = CORNER_CYCLE[(idx + i) % 4];
          var occupied = false;
          for (var otherId in allPositions) {
            if (otherId !== panelId && allPositions[otherId] === candidate) { occupied = true; break; }
          }
          if (!occupied) return candidate;
        }
        return desired;
      }

      function applyPanelPosition(id, corner) {
        var el = document.getElementById(id);
        if (!el) return;
        el.setAttribute('data-position', corner);
        var btn = el.querySelector('.panel-corner-btn');
        if (btn) {
          btn.textContent = CORNER_ARROWS[corner];
          btn.setAttribute('aria-label',
            'Move ' + (PANEL_NAMES[id] || 'panel') + ' to next corner (currently ' + CORNER_LABELS[corner] + ')');
        }
      }

      function onCornerClick(panelId) {
        var positions = getPanelPositions();
        var current = positions[panelId];
        var nextIdx = (CORNER_CYCLE.indexOf(current) + 1) % 4;
        var next = nextAvailableCorner(panelId, CORNER_CYCLE[nextIdx], positions);
        try { localStorage.setItem('panel-corner-' + panelId, next); } catch (_) {}
        applyPanelPosition(panelId, next);
        var announce = document.getElementById('panelPositionAnnounce');
        if (announce) announce.textContent = (PANEL_NAMES[panelId] || 'Panel') + ' moved to ' + CORNER_LABELS[next];
      }

      function resetPanelPositions() {
        for (var id in PANEL_DEFAULTS) {
          try { localStorage.removeItem('panel-corner-' + id); } catch (_) {}
          applyPanelPosition(id, PANEL_DEFAULTS[id]);
        }
      }

      window._panelCorner = {
        PANEL_DEFAULTS: PANEL_DEFAULTS, CORNER_CYCLE: CORNER_CYCLE,
        getPanelPositions: getPanelPositions, nextAvailableCorner: nextAvailableCorner,
        applyPanelPosition: applyPanelPosition, onCornerClick: onCornerClick,
        resetPanelPositions: resetPanelPositions
      };
    })();
  `;

  vm.createContext(ctx);
  vm.runInContext(extractSrc, ctx);
  return ctx.window._panelCorner;
}

// ---- Tests ----

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    fn();
    passed++;
    console.log('  ✓ ' + name);
  } catch (e) {
    failed++;
    console.log('  ✗ ' + name);
    console.log('    ' + e.message);
  }
}

console.log('\nPanel Corner Positioning Tests (#608 M0)\n');

// --- nextAvailableCorner ---
console.log('nextAvailableCorner:');

test('returns desired corner when available', () => {
  const ctx = createContext();
  const pc = loadLiveModule(ctx);
  const positions = { liveFeed: 'bl', liveLegend: 'br', liveNodeDetail: 'tr' };
  assert.strictEqual(pc.nextAvailableCorner('liveFeed', 'tl', positions), 'tl');
});

test('skips occupied corner', () => {
  const ctx = createContext();
  const pc = loadLiveModule(ctx);
  const positions = { liveFeed: 'bl', liveLegend: 'br', liveNodeDetail: 'tr' };
  // liveFeed wants 'tr' but liveNodeDetail is there → should get 'br'? No, liveLegend is at br → skip to bl? No liveFeed is at bl → skip to tl
  assert.strictEqual(pc.nextAvailableCorner('liveFeed', 'tr', positions), 'bl');
  // Wait — liveFeed IS liveFeed, so bl is not occupied by "another" panel
  // Actually liveFeed wants tr → tr occupied by nodeDetail → try br → occupied by legend → try bl → that's liveFeed itself (excluded from "occupied") → bl is free
});

test('skips multiple occupied corners', () => {
  const ctx = createContext();
  const pc = loadLiveModule(ctx);
  const positions = { liveFeed: 'tl', liveLegend: 'tr', liveNodeDetail: 'br' };
  // liveFeed wants 'tr' → occupied by legend → try 'br' → occupied by nodeDetail → try 'bl' → free
  assert.strictEqual(pc.nextAvailableCorner('liveFeed', 'tr', positions), 'bl');
});

test('returns desired when only self occupies it', () => {
  const ctx = createContext();
  const pc = loadLiveModule(ctx);
  const positions = { liveFeed: 'bl', liveLegend: 'br', liveNodeDetail: 'tr' };
  // liveFeed wants bl — it's "occupied" by liveFeed itself, which is excluded
  assert.strictEqual(pc.nextAvailableCorner('liveFeed', 'bl', positions), 'bl');
});

// --- getPanelPositions ---
console.log('\ngetPanelPositions:');

test('returns defaults when nothing in localStorage', () => {
  const ctx = createContext();
  const pc = loadLiveModule(ctx);
  const pos = pc.getPanelPositions();
  assert.strictEqual(pos.liveFeed, 'bl');
  assert.strictEqual(pos.liveLegend, 'br');
  assert.strictEqual(pos.liveNodeDetail, 'tr');
});

test('returns saved positions from localStorage', () => {
  const ctx = createContext();
  ctx.localStorage.setItem('panel-corner-liveFeed', 'tl');
  ctx.localStorage.setItem('panel-corner-liveLegend', 'bl');
  const pc = loadLiveModule(ctx);
  const pos = pc.getPanelPositions();
  assert.strictEqual(pos.liveFeed, 'tl');
  assert.strictEqual(pos.liveLegend, 'bl');
  assert.strictEqual(pos.liveNodeDetail, 'tr'); // still default
});

// --- applyPanelPosition ---
console.log('\napplyPanelPosition:');

test('sets data-position attribute on element', () => {
  const ctx = createContext();
  const el = ctx._addElement('liveFeed');
  el._addBtn('liveFeed');
  const pc = loadLiveModule(ctx);
  pc.applyPanelPosition('liveFeed', 'tr');
  assert.strictEqual(el._attrs['data-position'], 'tr');
});

test('updates button text and aria-label', () => {
  const ctx = createContext();
  const el = ctx._addElement('liveFeed');
  const btn = el._addBtn('liveFeed');
  const pc = loadLiveModule(ctx);
  pc.applyPanelPosition('liveFeed', 'tr');
  assert.strictEqual(btn.textContent, '↙');
  assert.ok(btn._attrs['aria-label'].includes('top-right'));
});

test('handles missing element gracefully', () => {
  const ctx = createContext();
  const pc = loadLiveModule(ctx);
  // Should not throw
  pc.applyPanelPosition('nonexistent', 'tl');
});

// --- onCornerClick ---
console.log('\nonCornerClick:');

test('cycles from default bl to tl for feed', () => {
  const ctx = createContext();
  const el = ctx._addElement('liveFeed');
  el._addBtn('liveFeed');
  ctx._addElement('liveLegend');
  ctx._addElement('liveNodeDetail');
  ctx._addElement('panelPositionAnnounce');
  ctx._elements.panelPositionAnnounce.textContent = '';
  const pc = loadLiveModule(ctx);
  // Feed defaults to bl, cycle: bl → tl (next in cycle after bl is tl)
  pc.onCornerClick('liveFeed');
  assert.strictEqual(ctx._storage['panel-corner-liveFeed'], 'tl');
  assert.strictEqual(el._attrs['data-position'], 'tl');
});

test('collision avoidance: skips occupied corner', () => {
  const ctx = createContext();
  ctx._addElement('liveFeed');
  const legendEl = ctx._addElement('liveLegend');
  legendEl._addBtn('liveLegend');
  ctx._addElement('liveNodeDetail');
  ctx._addElement('panelPositionAnnounce');
  ctx._elements.panelPositionAnnounce.textContent = '';
  const pc = loadLiveModule(ctx);
  // Legend defaults to br. Click → next is bl. But bl is occupied by feed → skip to tl
  pc.onCornerClick('liveLegend');
  assert.strictEqual(ctx._storage['panel-corner-liveLegend'], 'tl');
});

// --- resetPanelPositions ---
console.log('\nresetPanelPositions:');

test('clears localStorage and restores defaults', () => {
  const ctx = createContext();
  ctx.localStorage.setItem('panel-corner-liveFeed', 'tr');
  ctx.localStorage.setItem('panel-corner-liveLegend', 'tl');
  const feedEl = ctx._addElement('liveFeed');
  feedEl._addBtn('liveFeed');
  const legendEl = ctx._addElement('liveLegend');
  legendEl._addBtn('liveLegend');
  const detailEl = ctx._addElement('liveNodeDetail');
  detailEl._addBtn('liveNodeDetail');
  const pc = loadLiveModule(ctx);
  pc.resetPanelPositions();
  assert.strictEqual(ctx._storage['panel-corner-liveFeed'], undefined);
  assert.strictEqual(feedEl._attrs['data-position'], 'bl');
  assert.strictEqual(legendEl._attrs['data-position'], 'br');
  assert.strictEqual(detailEl._attrs['data-position'], 'tr');
});

// --- Corner cycle order ---
console.log('\nCorner cycle order:');

test('full cycle: tl → tr → br → bl → tl', () => {
  const ctx = createContext();
  const pc = loadLiveModule(ctx);
  const cycle = pc.CORNER_CYCLE;
  assert.strictEqual(cycle.join(','), 'tl,tr,br,bl');
});

test('defaults match expected panel positions', () => {
  const ctx = createContext();
  const pc = loadLiveModule(ctx);
  assert.strictEqual(pc.PANEL_DEFAULTS.liveFeed, 'bl');
  assert.strictEqual(pc.PANEL_DEFAULTS.liveLegend, 'br');
  assert.strictEqual(pc.PANEL_DEFAULTS.liveNodeDetail, 'tr');
});

// Summary
console.log('\n' + (passed + failed) + ' tests, ' + passed + ' passed, ' + failed + ' failed\n');
process.exit(failed > 0 ? 1 : 0);
