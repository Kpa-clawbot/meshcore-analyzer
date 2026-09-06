/* test-issue-1470-node-tile-helper.js — behavioral test for the
 * _applyTilesToNodeMap helper shipped in #1471 and roles.js
 * getActiveTileProvider() / getTileUrl() integration with the
 * MC_TILE_PROVIDERS registry.
 *
 * Strategy:
 *   - vm-load public/map-tile-providers.js + public/roles.js into a single
 *     sandbox with mocked localStorage + document(theme=dark).
 *   - Extract the _applyTilesToNodeMap function source from public/nodes.js
 *     by regex and run it in the same sandbox.
 *   - Mock L.tileLayer / L.map to capture the URL + options the helper passes
 *     and the .addTo() target. Assert that selecting carto-voyager-dark in the
 *     customizer ends up calling tileLayer with the voyager URL AND setting
 *     the tile pane filter to the provider.invertFilter string.
 *
 * No source-grep on the production fix. The assertions exercise the helper
 * code path end-to-end. Reverting the fix (re-hardcoding OSM) breaks this.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

function makeStorage() {
  const store = {};
  return {
    getItem(k) { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
    setItem(k, v) { store[k] = String(v); },
    removeItem(k) { delete store[k]; },
    clear() { for (const k of Object.keys(store)) delete store[k]; },
  };
}

function makeLeafletMock() {
  const calls = [];
  const tilePane = { style: { filter: '' } };
  const fakeMap = {
    _tileLayers: [],
    getPane: (name) => name === 'tilePane' ? tilePane : null,
  };
  const L = {
    tileLayer(url, opts) {
      const layer = {
        url, opts,
        addTo(map) {
          calls.push({ kind: 'tileLayer', url, opts, map });
          map._tileLayers.push(this);
          return this;
        },
      };
      return layer;
    },
    map() { return fakeMap; },
    marker() { return { addTo: () => ({ bindPopup: () => {} }) }; },
  };
  return { L, calls, fakeMap, tilePane };
}

function makeSandbox(opts) {
  opts = opts || {};
  const ctx = {
    console,
    setTimeout, clearTimeout,
    JSON, Date, Math, Object, Array, String, Number, Boolean, Set, Map,
    fetch: () => Promise.resolve({ ok: false, json: () => Promise.resolve({}) }),
    localStorage: makeStorage(),
    document: {
      documentElement: {
        getAttribute: () => opts.theme || 'dark',
        style: { getPropertyValue: () => '' },
      },
      querySelector: () => null,
      querySelectorAll: () => [],
      getElementById: () => null,
      createElement: () => ({ style: {}, appendChild: () => {}, setAttribute: () => {}, addEventListener: () => {} }),
      addEventListener: () => {},
      body: { appendChild: () => {}, style: {} },
      head: { appendChild: () => {} },
      readyState: 'complete',
    },
    window: {
      addEventListener: () => {},
      dispatchEvent: () => true,
      matchMedia: () => ({ matches: opts.prefersDark !== false, addEventListener: () => {} }),
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; },
  };
  ctx.window.localStorage = ctx.localStorage;
  ctx.window.document = ctx.document;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  // After context creation, make the sandbox global object also be `window`
  // so `window.X = ...` in roles.js makes X a bare global the same file
  // can reference. We do this by copying assignments back — simpler: install
  // a Proxy. Cheapest practical fix: shadow common globals after load.
  return ctx;
}

function loadInto(ctx, relPath) {
  const src = fs.readFileSync(path.join(__dirname, relPath), 'utf8');
  vm.runInContext(src, ctx, { filename: relPath });
  // Mirror window.* back to sandbox globals so code that uses bare names
  // (which in a browser are window.X) can still resolve them in vm. We do
  // this AFTER each file load so window.getTileUrl, window.TILE_DARK, etc.
  // become bare refs `getTileUrl` / `TILE_DARK` for callers in the same
  // sandbox.
  for (const k of Object.keys(ctx.window)) {
    if (!(k in ctx)) ctx[k] = ctx.window[k];
  }
}

function extractApplyTilesHelper() {
  const nodesSrc = fs.readFileSync(path.join(__dirname, 'public', 'nodes.js'), 'utf8');
  // Match the function _applyTilesToNodeMap(map) { ... } block. Brace-count.
  const startMatch = nodesSrc.match(/function\s+_applyTilesToNodeMap\s*\(map\)\s*\{/);
  if (!startMatch) throw new Error('_applyTilesToNodeMap definition not found in public/nodes.js');
  const start = startMatch.index;
  let depth = 0, i = start;
  for (; i < nodesSrc.length; i++) {
    const ch = nodesSrc[i];
    if (ch === '{') depth++;
    else if (ch === '}') {
      depth--;
      if (depth === 0) { i++; break; }
    }
  }
  return nodesSrc.slice(start, i);
}

console.log('── #1470 node-detail inset-map tile-provider routing ──');

test('roles.js + providers: getActiveTileProvider returns null in light mode', () => {
  const ctx = makeSandbox({ theme: 'light', prefersDark: false });
  loadInto(ctx, 'public/map-tile-providers.js');
  loadInto(ctx, 'public/roles.js');
  const p = ctx.window.getActiveTileProvider();
  assert.strictEqual(p, null, 'expected null in light mode, got ' + JSON.stringify(p));
});

test('roles.js + providers: getActiveTileProvider returns selected provider in dark mode', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadInto(ctx, 'public/map-tile-providers.js');
  loadInto(ctx, 'public/roles.js');
  ctx.window.MC_setDarkTileProvider('carto-voyager-dark');
  const p = ctx.window.getActiveTileProvider();
  assert.ok(p, 'provider returned in dark mode');
  // #1533 made provider urls lazy functions so the Carto base can be resolved
  // at call time (_getCartoBase); they were plain strings under #1430.
  assert.ok(typeof p.url === 'function', 'provider url is a resolver function — got ' + typeof p.url);
  assert.ok(/voyager/.test(p.url()),
    'provider url resolves to voyager — got ' + JSON.stringify(p.url()));
  assert.ok(typeof p.invertFilter === 'string' && /invert\(/.test(p.invertFilter),
    'carto-voyager-dark invertFilter present — got ' + JSON.stringify(p.invertFilter));
});

test('roles.js: getTileUrl returns voyager URL in dark mode when carto-voyager-dark selected', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadInto(ctx, 'public/map-tile-providers.js');
  loadInto(ctx, 'public/roles.js');
  ctx.window.MC_setDarkTileProvider('carto-voyager-dark');
  const url = ctx.window.getTileUrl();
  assert.ok(/voyager/.test(url), 'getTileUrl returns voyager URL — got ' + url);
});

test('_applyTilesToNodeMap: dark + carto-voyager-dark → tileLayer(voyagerURL) + invert filter', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadInto(ctx, 'public/map-tile-providers.js');
  loadInto(ctx, 'public/roles.js');
  ctx.window.MC_setDarkTileProvider('carto-voyager-dark');

  const mock = makeLeafletMock();
  ctx.L = mock.L;
  ctx._fakeMap = mock.fakeMap;
  vm.runInContext(extractApplyTilesHelper(), ctx, { filename: 'nodes.js#_applyTilesToNodeMap' });
  vm.runInContext('_applyTilesToNodeMap(_fakeMap);', ctx);

  assert.ok(mock.calls.length >= 1, 'tileLayer was called — got ' + mock.calls.length + ' calls');
  const firstCall = mock.calls[0];
  assert.ok(/voyager/.test(firstCall.url),
    'first tileLayer URL is voyager — got ' + firstCall.url);
  assert.ok(/invert\(/.test(mock.tilePane.style.filter),
    'tile pane filter set to invert(...) — got ' + JSON.stringify(mock.tilePane.style.filter));
});

test('_applyTilesToNodeMap: dark + carto-dark (non-inverted) → no invert filter applied', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadInto(ctx, 'public/map-tile-providers.js');
  loadInto(ctx, 'public/roles.js');
  ctx.window.MC_setDarkTileProvider('carto-dark');

  const mock = makeLeafletMock();
  ctx.L = mock.L;
  ctx._fakeMap = mock.fakeMap;
  vm.runInContext(extractApplyTilesHelper(), ctx, { filename: 'nodes.js#_applyTilesToNodeMap' });
  vm.runInContext('_applyTilesToNodeMap(_fakeMap);', ctx);

  assert.ok(mock.calls.length >= 1, 'tileLayer was called');
  assert.ok(/cartocdn|basemaps\.cartocdn|dark_all/.test(mock.calls[0].url),
    'carto-dark URL — got ' + mock.calls[0].url);
  assert.strictEqual(mock.tilePane.style.filter, '',
    'no invert filter for carto-dark — got ' + JSON.stringify(mock.tilePane.style.filter));
});

test('_applyTilesToNodeMap: light mode → uses TILE_LIGHT (carto light_all), no invert', () => {
  const ctx = makeSandbox({ theme: 'light', prefersDark: false });
  loadInto(ctx, 'public/map-tile-providers.js');
  loadInto(ctx, 'public/roles.js');

  const mock = makeLeafletMock();
  ctx.L = mock.L;
  ctx._fakeMap = mock.fakeMap;
  vm.runInContext(extractApplyTilesHelper(), ctx, { filename: 'nodes.js#_applyTilesToNodeMap' });
  vm.runInContext('_applyTilesToNodeMap(_fakeMap);', ctx);

  assert.ok(mock.calls.length >= 1, 'tileLayer called');
  assert.ok(/light_all|openstreetmap/.test(mock.calls[0].url),
    'light mode uses light tile URL — got ' + mock.calls[0].url);
  assert.strictEqual(mock.tilePane.style.filter, '', 'no invert filter in light mode');
});

console.log(`\n#1470 node-detail tile helper: ${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
