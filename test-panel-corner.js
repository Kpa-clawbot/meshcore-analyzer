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

  const mockEl = () => ({
    style: {}, textContent: '', innerHTML: '',
    classList: { add(){}, remove(){}, toggle(){}, contains(){ return false; } },
    appendChild(c){ return c; }, removeChild(){ }, insertBefore(c){ return c; },
    setAttribute(){}, getAttribute(){ return null; }, removeAttribute(){},
    addEventListener(){}, removeEventListener(){},
    querySelector(){ return null; }, querySelectorAll(){ return []; },
    getBoundingClientRect(){ return {top:0,left:0,right:0,bottom:0,width:0,height:0}; },
    closest(){ return null; }, matches(){ return false; },
    children: [], childNodes: [], parentNode: null, parentElement: null,
    focus(){}, blur(){}, click(){}, scrollTo(){},
    dataset: {}, offsetWidth: 0, offsetHeight: 0,
    getContext(){ return { clearRect(){}, fillRect(){}, beginPath(){}, moveTo(){}, lineTo(){}, stroke(){}, fill(){}, arc(){}, save(){}, restore(){}, translate(){}, rotate(){}, scale(){}, drawImage(){}, measureText(){ return {width:0}; }, createLinearGradient(){ return {addColorStop(){}}; }, canvas: {width:0,height:0} }; },
    width: 0, height: 0,
  });

  const ctx = {
    window: {},
    document: {
      getElementById: (id) => elements[id] || null,
      querySelectorAll: (sel) => {
        const results = [];
        for (const id in elements) {
          const el = elements[id];
          if (el._btns) results.push(...el._btns);
        }
        return results;
      },
      querySelector: () => null,
      documentElement: { getAttribute: () => null, style: {} },
      addEventListener: () => {},
      createElement: () => mockEl(),
      createElementNS: () => mockEl(),
      createTextNode: (t) => ({ textContent: t }),
      createDocumentFragment: () => ({ appendChild(){}, children: [] }),
      body: { appendChild(){}, removeChild(){}, style: {}, classList: { add(){}, remove(){} } },
      head: { appendChild(){} },
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
  // Load the REAL live.js in a VM context and return window._panelCorner.
  // This tests the actual code, not a copy (per AGENTS.md "test the real code, not copies").
  const src = fs.readFileSync(path.join(__dirname, 'public', 'live.js'), 'utf8');

  // Minimal stubs for live.js dependencies (only what's needed to avoid errors)
  ctx.registerPage = () => {};
  ctx.escapeHtml = (s) => String(s || '');
  ctx.timeAgo = () => '—';
  ctx.getParsedPath = () => [];
  ctx.getParsedDecoded = () => ({});
  ctx.TYPE_COLORS = { ADVERT: '#22c55e', GRP_TXT: '#3b82f6', TXT_MSG: '#f59e0b', ACK: '#6b7280', REQUEST: '#a855f7', RESPONSE: '#06b6d4', TRACE: '#ec4899', PATH: '#14b8a6' };
  ctx.ROLE_COLORS = {};
  ctx.ROLE_LABELS = {};
  ctx.ROLE_STYLE = {};
  ctx.ROLE_SORT = [];
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
  ctx.Number = Number; ctx.String = String; ctx.Array = Array; ctx.Object = Object;
  ctx.JSON = JSON; ctx.Math = Math; ctx.Date = Date; ctx.RegExp = RegExp;
  ctx.Error = Error; ctx.Map = Map; ctx.Set = Set; ctx.WeakMap = WeakMap;
  ctx.parseInt = parseInt; ctx.parseFloat = parseFloat;
  ctx.isNaN = isNaN; ctx.isFinite = isFinite;
  ctx.encodeURIComponent = encodeURIComponent;
  ctx.decodeURIComponent = decodeURIComponent;
  ctx.Promise = Promise; ctx.Symbol = Symbol;
  ctx.queueMicrotask = queueMicrotask;

  // Self-references needed for the IIFE
  ctx.self = ctx;
  ctx.globalThis = ctx;

  vm.createContext(ctx);
  vm.runInContext(src, ctx, { timeout: 3000 });
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
