/* test-clear-filters.js — behavioral tests for clear-filters button (#964)
 * Uses vm.createContext to exercise the actual clear handler logic,
 * not source-grep tautology.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

console.log('--- test-clear-filters.js ---');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

/**
 * Build a minimal sandbox that stubs DOM/localStorage/RegionFilter
 * enough for the clear handler and updatePacketsUrl to run.
 */
function makeSandbox() {
  const storage = {};
  const elements = {};
  const checkboxes = {};

  function makeEl(id, tag) {
    const el = {
      id, value: '', textContent: '', style: { display: '' },
      classList: { remove: function() { el._classes = el._classes.filter(c => !Array.from(arguments).includes(c)); }, _list: [] },
      _classes: [],
      addEventListener: (ev, fn) => { el['_on_' + ev] = fn; },
      querySelectorAll: () => checkboxes[id] || [],
    };
    el.classList.add = function() { el._classes.push(...arguments); };
    elements[id] = el;
    return el;
  }

  // Pre-create all elements the clear handler touches
  for (const id of [
    'clearFiltersBtn', 'fHash', 'fNode', 'fChannel', 'fTimeWindow',
    'packetFilterInput', 'packetFilterError', 'packetFilterCount',
    'fMyNodes', 'observerMenu', 'typeMenu', 'observerTrigger', 'typeTrigger'
  ]) {
    makeEl(id);
  }

  // Create mock checkboxes for observer/type menus
  for (const menuId of ['observerMenu', 'typeMenu']) {
    const cb1 = { checked: true }, cb2 = { checked: true };
    checkboxes[menuId] = [cb1, cb2];
  }

  const regionState = { selected: ['US-W'] };

  const ctx = {
    console,
    window: {
      addEventListener: () => {},
      dispatchEvent: () => {},
      matchMedia: () => ({ matches: false }),
      HashColor: null,
      buildPacketsQuery: null,
    },
    document: {
      readyState: 'complete',
      getElementById: (id) => elements[id] || null,
      createElement: (tag) => makeEl('_dynamic_' + Math.random(), tag),
      documentElement: { dataset: { theme: 'light' } },
      querySelectorAll: () => [],
      head: { appendChild: () => {} },
      addEventListener: () => {},
    },
    localStorage: {
      getItem: (k) => storage[k] !== undefined ? storage[k] : null,
      setItem: (k, v) => { storage[k] = String(v); },
      removeItem: (k) => { delete storage[k]; },
    },
    history: { replaceState: () => {} },
    location: { hash: '#/packets' },
    setTimeout: (fn) => fn(),
    clearTimeout: () => {},
    Number, String, Map, Set, Array, Object, JSON, Math,
    isNaN, isFinite, parseInt, parseFloat, encodeURIComponent, decodeURIComponent,
    RegExp, Error, TypeError, Date,
    // Stubs for globals packets.js references
    observerMap: new Map(),
    HashColor: null,
    RegionFilter: {
      getRegionParam: () => regionState.selected.length ? regionState.selected.join(',') : '',
      setSelected: (arr) => { regionState.selected = arr; },
      onUpdate: () => {},
      init: () => {},
    },
    // Provide isMobile
    navigator: { userAgent: 'node-test' },
  };
  ctx.window.RegionFilter = ctx.RegionFilter;
  ctx.globalThis = ctx;
  ctx.self = ctx;

  return { ctx, elements, storage, checkboxes, regionState };
}

/**
 * Extract the clear handler body from packets.js and wrap it as a callable function.
 * This is more robust than loading the entire IIFE (which needs full DOM).
 */
function extractClearHandler() {
  const src = fs.readFileSync(__dirname + '/public/packets.js', 'utf-8');

  // Find the clear handler
  const marker = "if (clearBtn) clearBtn.addEventListener('click', function()";
  const idx = src.indexOf(marker);
  assert(idx !== -1, 'clear handler not found in packets.js');

  // Find its opening brace and matching close
  const fnStart = src.indexOf('{', idx + marker.length);
  let depth = 0, fnEnd = -1;
  for (let i = fnStart; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (depth === 0) { fnEnd = i; break; } }
  }
  assert(fnEnd > fnStart, 'could not find end of clear handler');
  return src.substring(fnStart + 1, fnEnd);
}

/**
 * Extract updatePacketsUrl function body
 */
function extractUpdatePacketsUrl() {
  const src = fs.readFileSync(__dirname + '/public/packets.js', 'utf-8');
  const marker = 'function updatePacketsUrl()';
  const idx = src.indexOf(marker);
  assert(idx !== -1, 'updatePacketsUrl not found');
  const fnStart = src.indexOf('{', idx);
  let depth = 0, fnEnd = -1;
  for (let i = fnStart; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (depth === 0) { fnEnd = i; break; } }
  }
  return src.substring(fnStart + 1, fnEnd);
}

const clearBody = extractClearHandler();
const updateUrlBody = extractUpdatePacketsUrl();

// ---- Tests ----

test('clear handler resets all filter keys to undefined/null/false', () => {
  const { ctx, elements } = makeSandbox();
  const filters = {
    hash: 'abc123', node: 42, nodeName: 'Test', observer: 'obs1',
    channel: 3, type: 'IDENT', _filterExpr: 'src==5', _packetFilter: () => true,
    myNodes: true,
  };
  let savedTimeWindowMin = 60;
  const DEFAULT_TIME_WINDOW = 15;
  let _observerFilterSet = new Set([1, 2]);

  // Build a function with the handler body and needed locals in scope
  const fn = new Function(
    'filters', 'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW', '_observerFilterSet',
    'localStorage', 'document', 'RegionFilter', 'updatePacketsUrl', 'loadPackets',
    `${clearBody}; return { savedTimeWindowMin, _observerFilterSet };`
  );

  const result = fn(
    filters, savedTimeWindowMin, DEFAULT_TIME_WINDOW, _observerFilterSet,
    ctx.localStorage, ctx.document, ctx.RegionFilter,
    () => {}, () => {} // stubs for updatePacketsUrl and loadPackets
  );

  assert.strictEqual(filters.hash, undefined, 'hash not cleared');
  assert.strictEqual(filters.node, undefined, 'node not cleared');
  assert.strictEqual(filters.nodeName, undefined, 'nodeName not cleared');
  assert.strictEqual(filters.observer, undefined, 'observer not cleared');
  assert.strictEqual(filters.channel, undefined, 'channel not cleared');
  assert.strictEqual(filters.type, undefined, 'type not cleared');
  assert.strictEqual(filters._filterExpr, undefined, '_filterExpr not cleared');
  assert.strictEqual(filters._packetFilter, null, '_packetFilter not cleared');
  assert.strictEqual(filters.myNodes, false, 'myNodes not cleared');
});

test('clear handler resets savedTimeWindowMin to DEFAULT_TIME_WINDOW', () => {
  const { ctx } = makeSandbox();
  ctx.localStorage.setItem('meshcore-time-window', '120');
  const filters = { myNodes: false };
  const DEFAULT_TIME_WINDOW = 15;
  let _observerFilterSet = null;

  // The handler assigns to savedTimeWindowMin — we need to check the returned value
  const fn = new Function(
    'filters', 'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW', '_observerFilterSet',
    'localStorage', 'document', 'RegionFilter', 'updatePacketsUrl', 'loadPackets',
    `${clearBody}; return { savedTimeWindowMin };`
  );
  const result = fn(
    filters, 120, DEFAULT_TIME_WINDOW, _observerFilterSet,
    ctx.localStorage, ctx.document, ctx.RegionFilter,
    () => {}, () => {}
  );

  assert.strictEqual(result.savedTimeWindowMin, 15, 'savedTimeWindowMin not reset to default');
  assert.strictEqual(ctx.localStorage.getItem('meshcore-time-window'), null, 'time-window localStorage not cleared');
});

test('clear handler resets fTimeWindow dropdown value', () => {
  const { ctx, elements } = makeSandbox();
  elements['fTimeWindow'].value = '120';
  const filters = { myNodes: false };
  const fn = new Function(
    'filters', 'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW', '_observerFilterSet',
    'localStorage', 'document', 'RegionFilter', 'updatePacketsUrl', 'loadPackets',
    `${clearBody}; return { savedTimeWindowMin };`
  );
  fn(filters, 120, 15, null, ctx.localStorage, ctx.document, ctx.RegionFilter, () => {}, () => {});
  assert.strictEqual(elements['fTimeWindow'].value, '15', 'fTimeWindow DOM not reset');
});

test('clear handler clears observer and type localStorage', () => {
  const { ctx } = makeSandbox();
  ctx.localStorage.setItem('meshcore-observer-filter', 'obs1');
  ctx.localStorage.setItem('meshcore-type-filter', 'IDENT');
  const filters = { myNodes: false };
  const fn = new Function(
    'filters', 'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW', '_observerFilterSet',
    'localStorage', 'document', 'RegionFilter', 'updatePacketsUrl', 'loadPackets',
    `${clearBody};`
  );
  fn(filters, 15, 15, null, ctx.localStorage, ctx.document, ctx.RegionFilter, () => {}, () => {});
  assert.strictEqual(ctx.localStorage.getItem('meshcore-observer-filter'), null);
  assert.strictEqual(ctx.localStorage.getItem('meshcore-type-filter'), null);
});

test('clear handler unchecks observer/type multi-select checkboxes', () => {
  const { ctx, checkboxes } = makeSandbox();
  const filters = { myNodes: false };
  const fn = new Function(
    'filters', 'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW', '_observerFilterSet',
    'localStorage', 'document', 'RegionFilter', 'updatePacketsUrl', 'loadPackets',
    `${clearBody};`
  );
  fn(filters, 15, 15, null, ctx.localStorage, ctx.document, ctx.RegionFilter, () => {}, () => {});
  for (const cb of checkboxes['observerMenu']) assert.strictEqual(cb.checked, false, 'observer checkbox still checked');
  for (const cb of checkboxes['typeMenu']) assert.strictEqual(cb.checked, false, 'type checkbox still checked');
});

test('clear handler resets RegionFilter', () => {
  const { ctx, regionState } = makeSandbox();
  regionState.selected = ['US-W', 'EU'];
  const filters = { myNodes: false };
  const fn = new Function(
    'filters', 'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW', '_observerFilterSet',
    'localStorage', 'document', 'RegionFilter', 'updatePacketsUrl', 'loadPackets',
    `${clearBody};`
  );
  fn(filters, 15, 15, null, ctx.localStorage, ctx.document, ctx.RegionFilter, () => {}, () => {});
  assert.deepStrictEqual(regionState.selected, [], 'RegionFilter not cleared');
});

test('updatePacketsUrl shows clear button when time window != default', () => {
  const { ctx, elements } = makeSandbox();
  // No other filters active, but time window is non-default
  const filters = {};
  let savedTimeWindowMin = 60;
  const DEFAULT_TIME_WINDOW = 15;
  const fn = new Function(
    'filters', 'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW',
    'document', 'history', 'RegionFilter', 'buildPacketsQuery', 'location',
    updateUrlBody
  );
  fn(filters, savedTimeWindowMin, DEFAULT_TIME_WINDOW,
    ctx.document, ctx.history, ctx.RegionFilter, () => '', { hash: '#/packets' });
  assert.strictEqual(elements['clearFiltersBtn'].style.display, '', 'clear button should be visible when time window != default');
});

test('updatePacketsUrl hides clear button when all filters default', () => {
  const { ctx, elements, regionState } = makeSandbox();
  regionState.selected = [];
  const filters = {};
  const fn = new Function(
    'filters', 'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW',
    'document', 'history', 'RegionFilter', 'buildPacketsQuery', 'location',
    updateUrlBody
  );
  fn(filters, 15, 15, ctx.document, ctx.history, ctx.RegionFilter, () => '', { hash: '#/packets' });
  assert.strictEqual(elements['clearFiltersBtn'].style.display, 'none', 'clear button should be hidden');
});

// Summary
console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
console.log('All tests passed ✅');
