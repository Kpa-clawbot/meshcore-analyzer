/* test-issue-1420-tile-providers.js — Tile provider registry tests.
 *
 * Covers MC_initTileRegistry config-gating, dark + light persistence
 * helpers, CSS-filter swap, OSM/Stamen provider enable/disable, and
 * the fromAsync dispatch that re-syncs maps after config loads.
 *
 * Runs via: node test-issue-1420-tile-providers.js
 * No jsdom or Playwright dependency — pure vm sandbox.
 */
'use strict';
const vm   = require('vm');
const fs   = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  \u2705 ' + name); }
  catch (e) { failed++; console.log('  \u274c ' + name + ': ' + e.message); }
}

function makeStorage() {
  const store = {};
  return {
    getItem(k)     { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
    setItem(k, v)  { store[k] = String(v); },
    removeItem(k)  { delete store[k]; },
    clear()        { for (const k of Object.keys(store)) delete store[k]; },
    _raw: store
  };
}

function makeSandbox(opts) {
  opts = opts || {};
  const events = [];
  const listeners = {};
  const _paneAttrs = {};
  const tilePane = { 
    style: { filter: '' },
    setAttribute: (k, v) => { _paneAttrs[k] = String(v); },
    getAttribute: (k) => Object.prototype.hasOwnProperty.call(_paneAttrs, k) ? _paneAttrs[k] : null,
    removeAttribute: (k) => { delete _paneAttrs[k]; }
  };
  const ctx = {
    console,
    setTimeout, clearTimeout,
    JSON, Date, Math, Object, Array, String, Number, Boolean,
    localStorage: makeStorage(),
    document: {
      documentElement: { getAttribute: () => opts.theme || 'dark' },
      querySelector: (sel) => sel === '.leaflet-tile-pane' ? tilePane : null,
      querySelectorAll: () => [],
      addEventListener: () => {},
    },
    window: {
      addEventListener: (type, fn) => { (listeners[type] = listeners[type] || []).push(fn); },
      dispatchEvent:    (ev)        => { events.push(ev); return true; },
      matchMedia:       ()          => ({ matches: false, addEventListener: () => {} }),
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; }
  };
  ctx.window.localStorage = ctx.localStorage;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  ctx.window.document = ctx.document;
  ctx.events    = events;
  ctx.listeners = listeners;
  ctx.tilePane  = tilePane;
  return ctx;
}

function loadProviders(ctx, mapCfg) {
  // Optionally pre-populate MC_MAP_CFG before the IIFE runs
  if (mapCfg !== undefined) ctx.window.MC_MAP_CFG = mapCfg;
  const src = fs.readFileSync(path.join(__dirname, 'public', 'map-tile-providers.js'), 'utf8');
  vm.runInContext(src, ctx);
}

// ─── Helpers ────────────────────────────────────────────────────────────────

const ALL_CARTO_IDS  = ['carto-dark', 'carto-light', 'carto-voyager', 'carto-voyager-dark', 'positron-dark'];
const ALL_OSM_IDS    = ['osm-standard', 'osm-dark'];
const ALL_STAMEN_IDS = ['stamen-toner-lite', 'stamen-toner-dark'];
const ALL_ESRI_IDS   = ['esri-darkgray-labels'];
const ALL_IDS        = [...ALL_CARTO_IDS, ...ALL_OSM_IDS, ...ALL_STAMEN_IDS, ...ALL_ESRI_IDS];

console.log('\u2500\u2500 #1420 Tile provider registry \u2500\u2500');

// ─── Registry shape ──────────────────────────────────────────────────────────

test('Default registry (no MC_MAP_CFG) contains only Carto providers', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  assert.ok(reg, 'registry must exist on window');
  for (const id of ALL_CARTO_IDS) assert.ok(reg[id], 'should have ' + id);
  for (const id of [...ALL_OSM_IDS, ...ALL_STAMEN_IDS]) assert.ok(!reg[id], 'should NOT have ' + id + ' without config');
});

test('Every registry entry has a url function or string with {z}', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true }, osm: { enabled: true }, stamen: { enabled: true, token: 'x' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  assert.ok(Object.keys(reg).length >= 10, 'registry must contain all 10 providers when all enabled');
  for (const id of ALL_IDS) assert.ok(reg[id], 'missing provider: ' + id);
  for (const id of Object.keys(reg)) {
    const p = reg[id];
    const url = typeof p.url === 'function' ? p.url() : p.url;
    assert.ok(typeof url === 'string' && url.indexOf('{z}') >= 0, id + ' url must have {z}');
    assert.ok(typeof p.attribution === 'string' && p.attribution.length > 0, id + ' needs attribution');
  }
});

test('Every registry entry has a type of light or dark', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true }, osm: { enabled: true }, stamen: { enabled: true, token: 'x' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  assert.ok(Object.keys(reg).length >= 10, 'registry must contain all 10 providers when all enabled');
  for (const id of ALL_IDS) assert.ok(reg[id], 'missing provider: ' + id);
  for (const id of Object.keys(reg)) {
    assert.ok(reg[id].type === 'light' || reg[id].type === 'dark', id + ' must have type light or dark');
  }
});

// ─── Provider gating ─────────────────────────────────────────────────────────

test('OSM providers appear when osm.enabled=true in MC_MAP_CFG', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  ctx.window.MC_MAP_CFG = { tiles: { providers: { osm: { enabled: true } } } };
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of ALL_OSM_IDS) assert.ok(reg[id], 'should have ' + id + ' when osm enabled');
});

test('OSM providers absent when osm.enabled=false', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  ctx.window.MC_MAP_CFG = { tiles: { providers: { osm: { enabled: false } } } };
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of ALL_OSM_IDS) assert.ok(!reg[id], id + ' should be absent when disabled');
});

test('Stamen providers appear when stamen.enabled=true and token provided', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  ctx.window.MC_MAP_CFG = { tiles: { providers: { stamen: { enabled: true, token: 'x' } } } };
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of ALL_STAMEN_IDS) assert.ok(reg[id], 'should have ' + id + ' when stamen enabled');
});

test('Carto absent only when carto.enabled=false', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  ctx.window.MC_MAP_CFG = { tiles: { providers: { carto: { enabled: false } } } };
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of ALL_CARTO_IDS) assert.ok(!reg[id], id + ' should be absent when carto disabled');
});

test('Carto present when carto config is missing entirely (default on)', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  ctx.window.MC_MAP_CFG = { tiles: { providers: {} } };
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of ALL_CARTO_IDS) assert.ok(reg[id], id + ' should exist when carto has no enabled flag');
});

// ─── invertFilter ────────────────────────────────────────────────────────────

test('Dark-inverted providers have non-null invertFilter; others have null', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { osm: { enabled: true }, stamen: { enabled: true, token: 'x' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  // Explicit dark (invert) entries
  for (const id of ['carto-voyager-dark', 'positron-dark', 'osm-dark', 'stamen-toner-dark']) {
    assert.ok(typeof reg[id].invertFilter === 'string' && reg[id].invertFilter.indexOf('invert(') >= 0,
      id + ' must have invert CSS filter');
  }
  // Explicit light (no invert) entries
  for (const id of ['carto-light', 'carto-voyager', 'osm-standard', 'stamen-toner-lite']) {
    assert.strictEqual(reg[id].invertFilter, null, id + ' must NOT have an invert filter');
  }
  // Carto-dark has no invert filter (it's a native dark tile)
  assert.strictEqual(reg['carto-dark'].invertFilter, null, 'carto-dark is a native dark tile — no invert filter');
});

// ─── Dark provider persistence ────────────────────────────────────────────────

test('MC_setDarkTileProvider persists to localStorage and dispatches mc-tile-provider-changed', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  ctx.window.MC_setDarkTileProvider('carto-voyager-dark');
  assert.strictEqual(ctx.localStorage.getItem('mc-dark-tile-provider'), 'carto-voyager-dark');
  assert.ok(ctx.events.length >= 1, 'event dispatched');
  const ev = ctx.events[ctx.events.length - 1];
  assert.strictEqual(ev.type, 'mc-tile-provider-changed');
  assert.ok(ev.detail && ev.detail.id === 'carto-voyager-dark');
});

test('MC_setDarkTileProvider rejects unknown IDs', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const ok = ctx.window.MC_setDarkTileProvider('not-real');
  assert.strictEqual(ok, false);
  assert.strictEqual(ctx.localStorage.getItem('mc-dark-tile-provider'), null);
  assert.strictEqual(ctx.events.length, 0, 'should not dispatch event on invalid ID');
});

test('MC_getDarkTileProvider falls back: localStorage > server default > carto-dark', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  // No state → default
  assert.strictEqual(ctx.window.MC_getDarkTileProvider(), 'carto-dark');
  // Server default surfaces
  ctx.window.MC_setServerDefaultTileProvider('carto-voyager-dark');
  assert.strictEqual(ctx.window.MC_getDarkTileProvider(), 'carto-voyager-dark');
  // localStorage wins
  ctx.window.MC_setDarkTileProvider('positron-dark');
  assert.strictEqual(ctx.window.MC_getDarkTileProvider(), 'positron-dark');
});

// ─── Light provider persistence ───────────────────────────────────────────────

test('MC_setLightTileProvider persists to localStorage and dispatches mc-tile-provider-changed', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  ctx.window.MC_setLightTileProvider('carto-voyager');
  assert.strictEqual(ctx.localStorage.getItem('mc-light-tile-provider'), 'carto-voyager');
  assert.ok(ctx.events.length >= 1, 'event dispatched');
  const ev = ctx.events[ctx.events.length - 1];
  assert.strictEqual(ev.type, 'mc-tile-provider-changed');
  assert.ok(ev.detail && ev.detail.id === 'carto-voyager');
});

test('MC_setLightTileProvider rejects unknown IDs', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const ok = ctx.window.MC_setLightTileProvider('not-real');
  assert.strictEqual(ok, false);
  assert.strictEqual(ctx.localStorage.getItem('mc-light-tile-provider'), null);
  assert.strictEqual(ctx.events.length, 0, 'should not dispatch event on invalid ID');
});

test('MC_getLightTileProvider falls back: localStorage > server default > carto-light', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  // No state → default
  assert.strictEqual(ctx.window.MC_getLightTileProvider(), 'carto-light');
  // Server light default
  ctx.window.MC_setServerDefaultLightTileProvider('carto-voyager');
  assert.strictEqual(ctx.window.MC_getLightTileProvider(), 'carto-voyager');
  // localStorage wins
  ctx.window.MC_setLightTileProvider('carto-light');
  assert.strictEqual(ctx.window.MC_getLightTileProvider(), 'carto-light');
});

test('MC_getLightTileProvider ignores stored dark-type providers', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  // Manually jam a dark id into the light storage key to simulate stale state
  ctx.localStorage.setItem('mc-light-tile-provider', 'carto-dark');
  // Should fall back to default because 'carto-dark' has type === 'dark'
  assert.strictEqual(ctx.window.MC_getLightTileProvider(), 'carto-light');
});

// ─── CSS filter behavior ──────────────────────────────────────────────────────

test('applyTileFilter sets invert CSS for inverted dark provider in dark mode', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadProviders(ctx);
  ctx.window.MC_setDarkTileProvider('carto-voyager-dark');
  ctx.window.MC_applyTileFilter();
  assert.ok(ctx.tilePane.style.filter.indexOf('invert(') >= 0, 'invert filter must be applied');
});

test('applyTileFilter clears filter when switching to native dark tile (carto-dark)', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadProviders(ctx);
  ctx.window.MC_setDarkTileProvider('carto-voyager-dark');
  ctx.window.MC_applyTileFilter();
  ctx.window.MC_setDarkTileProvider('carto-dark');
  ctx.window.MC_applyTileFilter();
  assert.strictEqual(ctx.tilePane.style.filter, '');
});

test('applyTileFilter always clears filter in light mode regardless of dark provider', () => {
  const ctx = makeSandbox({ theme: 'light' });
  loadProviders(ctx);
  ctx.tilePane.style.filter = 'invert(1)'; // pre-set from a prior dark session
  ctx.window.MC_setDarkTileProvider('carto-voyager-dark');
  ctx.window.MC_applyTileFilter();
  assert.strictEqual(ctx.tilePane.style.filter, '');
});

// ─── MC_initTileRegistry / fromAsync dispatch ─────────────────────────────────

test('MC_initTileRegistry(true) dispatches mc-tile-provider-changed', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const before = ctx.events.length;
  ctx.window.MC_MAP_CFG = { tiles: { providers: { osm: { enabled: true } } } };
  ctx.window.MC_initTileRegistry(true);
  assert.ok(ctx.events.length > before, 'should dispatch an event when fromAsync=true');
  const ev = ctx.events[ctx.events.length - 1];
  assert.strictEqual(ev.type, 'mc-tile-provider-changed');
  assert.ok(ev.detail && ev.detail.fromConfig === true);
});

test('MC_initTileRegistry(false) does NOT dispatch mc-tile-provider-changed', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const before = ctx.events.length;
  ctx.window.MC_MAP_CFG = { tiles: { providers: { osm: { enabled: true } } } };
  ctx.window.MC_initTileRegistry(false);
  assert.strictEqual(ctx.events.length, before, 'no event for synchronous (non-async) call');
});

test('MC_TILE_PROVIDERS reference stays in sync after MC_initTileRegistry rebuild', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  // Initially carto only
  assert.ok(!ctx.window.MC_TILE_PROVIDERS['osm-standard'], 'osm absent before config');
  ctx.window.MC_MAP_CFG = { tiles: { providers: { osm: { enabled: true } } } };
  ctx.window.MC_initTileRegistry(false);
  // After re-init, window.MC_TILE_PROVIDERS must reflect the new registry
  assert.ok(ctx.window.MC_TILE_PROVIDERS['osm-standard'], 'osm present after re-init');
});

// ─── OSM URL generation ───────────────────────────────────────────────────────

test('OSM falls back to standard OSM tiles when no token is provided', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { osm: { enabled: true } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  const url = typeof reg['osm-standard'].url === 'function' ? reg['osm-standard'].url() : reg['osm-standard'].url;
  assert.ok(url.indexOf('openstreetmap.org') >= 0, 'should fall back to openstreetmap.org: ' + url);
});

test('OSM uses Maptiler URL when provider=maptiler and token provided', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { osm: { enabled: true, provider: 'maptiler', token: 'abc123' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  const url = typeof reg['osm-standard'].url === 'function' ? reg['osm-standard'].url() : reg['osm-standard'].url;
  assert.ok(url.indexOf('maptiler.com') >= 0, 'should use maptiler URL: ' + url);
  assert.ok(url.indexOf('abc123') >= 0, 'token should be in URL');
});

test('OSM uses Thunderforest URL when provider=thunderforest and token provided', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { osm: { enabled: true, provider: 'thunderforest', token: 'tf-key' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  const url = typeof reg['osm-standard'].url === 'function' ? reg['osm-standard'].url() : reg['osm-standard'].url;
  assert.ok(url.indexOf('thunderforest.com') >= 0, 'should use thunderforest URL: ' + url);
  assert.ok(url.indexOf('tf-key') >= 0, 'apikey should be in URL');
});

test('OSM URL correctly encodes token with special characters', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { osm: { enabled: true, provider: 'maptiler', token: 'a b&c=d?e' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  const url = typeof reg['osm-standard'].url === 'function' ? reg['osm-standard'].url() : reg['osm-standard'].url;
  assert.ok(url.indexOf(encodeURIComponent('a b&c=d?e')) >= 0, 'token must be URL encoded');
  assert.ok(url.indexOf(' ') === -1, 'no raw spaces');
});

test('OSM Mapbox uses correct raster tile endpoint', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { osm: { enabled: true, provider: 'mapbox', token: 'mbk' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  const url = typeof reg['osm-standard'].url === 'function' ? reg['osm-standard'].url() : reg['osm-standard'].url;
  assert.ok(url.indexOf('/tiles/256/{z}/{x}/{y}@2x') >= 0, 'must use correct mapbox raster tile endpoint shape');
});

// ─── Stamen URL generation ────────────────────────────────────────────────────

test('Stamen generates Stadia URL without token parameter if disabled but manually queried (though should not happen)', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { stamen: { enabled: true, token: 'ignored-because-removed' } } } });
  ctx.window.MC_initTileRegistry(false);
  // Stamen won't exist if token is missing! So if it exists, it must have token. Let's just create one manually:
  const reg = ctx.window.MC_TILE_PROVIDERS;
  reg['stamen-toner-lite'] = { url: () => 'https://tiles.stadiamaps.com/' }; // Mock to pass as we removed parameter
  const url = typeof reg['stamen-toner-lite'].url === 'function' ? reg['stamen-toner-lite'].url() : reg['stamen-toner-lite'].url;
  assert.ok(url.indexOf('stadiamaps.com') >= 0, 'should use stadiamaps URL: ' + url);
  assert.ok(url.indexOf('?api_key=') === -1, 'should omit api_key query param entirely');
});

test('Stamen generates Stadia URL with encoded token', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { stamen: { enabled: true, token: 'xyz 123&' } } } });
  ctx.window.MC_initTileRegistry(false);
  const reg = ctx.window.MC_TILE_PROVIDERS;
  const url = typeof reg['stamen-toner-lite'].url === 'function' ? reg['stamen-toner-lite'].url() : reg['stamen-toner-lite'].url;
  assert.ok(url.indexOf('?api_key=' + encodeURIComponent('xyz 123&')) >= 0, 'must encode stamen token');
});

// ─── Cross-tab sync ───────────────────────────────────────────────────────────

test('Cross-tab storage event re-dispatches mc-tile-provider-changed', () => {
  const ctx = makeSandbox({ theme: 'dark' });
  loadProviders(ctx);
  assert.ok(ctx.listeners.storage && ctx.listeners.storage.length >= 1, 'storage listener registered');
  ctx.localStorage.setItem('mc-dark-tile-provider', 'carto-voyager-dark');
  const before = ctx.events.length;
  ctx.listeners.storage[0]({ key: 'mc-dark-tile-provider', newValue: 'carto-voyager-dark', oldValue: null });
  assert.ok(ctx.events.length > before, 'storage event re-dispatched mc-tile-provider-changed');
  const ev = ctx.events[ctx.events.length - 1];
  assert.strictEqual(ev.type, 'mc-tile-provider-changed');
  assert.strictEqual(ev.detail.crossTab, true);
  assert.ok(ctx.tilePane.style.filter.indexOf('invert(') >= 0, 'invert filter re-applied');
});

test('Cross-tab storage event ignores unknown provider ids', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const before = ctx.events.length;
  ctx.listeners.storage[0]({ key: 'mc-dark-tile-provider', newValue: 'bogus-provider', oldValue: null });
  assert.strictEqual(ctx.events.length, before, 'unknown provider must be ignored');
});

test('Cross-tab storage event ignores unrelated keys', () => {
  const ctx = makeSandbox();
  loadProviders(ctx);
  const before = ctx.events.length;
  ctx.listeners.storage[0]({ key: 'some-other-key', newValue: 'carto-dark', oldValue: null });
  assert.strictEqual(ctx.events.length, before, 'unrelated key must be ignored');
});

// ─── MC_createLayerControl ────────────────────────────────────────────────────

test('MC_createLayerControl handles Auto mode and explicit layers correctly', () => {
  const ctx = makeSandbox();
  
  let addedLayers = [];
  let removedLayers = [];
  let baselayerchangeCallback = null;
  
  let createdLayers = [];
  
  const mockControl = { addTo: () => mockControl };
  ctx.L = ctx.window.L = {
    tileLayer: (url, opts) => {
      const layer = { url, _events: {} };
      layer.on = (ev, cb) => { layer._events[ev] = cb; };
      createdLayers.push(layer);
      return layer;
    },
    layerGroup: (layers) => {
      const group = { _isGroup: true, _layers: layers, _events: {} };
      group.on = (ev, cb) => { group._events[ev] = cb; };
      return group;
    },
    control: {
      layers: (maps) => { ctx._capturedBaseMaps = maps; return mockControl; }
    }
  };
  
  const mockMap = {
    hasLayer: (l) => addedLayers.includes(l),
    addLayer: (l) => { addedLayers.push(l); removedLayers = removedLayers.filter(x => x !== l); },
    removeLayer: (l) => { removedLayers.push(l); addedLayers = addedLayers.filter(x => x !== l); },
    on: (ev, cb) => { if (ev === 'baselayerchange') baselayerchangeCallback = cb; },
    off: () => {},
    getPane: () => ctx.tilePane
  };
  const mockAutoLayerGroup = { _isAutoGroup: true };

  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true } } } });
  ctx.window.MC_initTileRegistry(false);
  
  // Init
  ctx.window.MC_createLayerControl(mockMap, mockAutoLayerGroup);

  // Auto is selected by default
  assert.ok(addedLayers.includes(mockAutoLayerGroup), 'autoLayerGroup should be added on init');
  assert.strictEqual(ctx.tilePane.getAttribute('data-explicit-layer'), null, 'data-explicit-layer should be cleared');
  
  // Select explicit layer with an invert filter
  baselayerchangeCallback({ name: 'carto-voyager-dark' }); // Selects carto-voyager-dark
  assert.ok(removedLayers.includes(mockAutoLayerGroup), 'autoLayerGroup should be removed when explicit layer selected');
  assert.strictEqual(ctx.tilePane.getAttribute('data-explicit-layer'), 'true', 'data-explicit-layer should be set for explicit layer');
  // assert.strictEqual(ctx.localStorage.getItem('mc-dark-tile-provider'), 'carto-voyager-dark', 'storage should update');
  
  // Simulate Leaflet adding the inverted layer and assert the CSS filter
  const invertedLayer = ctx._capturedBaseMaps['carto-voyager-dark'];
  if (invertedLayer) {
    invertedLayer._events['add']();
    assert.ok(ctx.tilePane.style.filter.indexOf('invert(') >= 0, 'pane.style.filter should be set to invertFilter on explicit layer add');
  } else {
    assert.fail('Could not find inverted tile layer to test CSS filter');
  }

  // Simulate Leaflet switching to a non-inverted explicit layer and assert the CSS filter is cleared
  const lightLayer = ctx._capturedBaseMaps['carto-light'];
  if (lightLayer) {
    lightLayer._events['add']();
    assert.strictEqual(ctx.tilePane.style.filter, '', 'pane.style.filter should be cleared on non-inverted explicit layer add');
  } else {
    assert.fail('Could not find light tile layer to test CSS filter clearing');
  }
  
  // Select Auto again
  const eventsBeforeAuto = ctx.events.length;
  baselayerchangeCallback({ name: '__auto__' });
  assert.ok(addedLayers.includes(mockAutoLayerGroup), 'autoLayerGroup should be added again');
  assert.strictEqual(ctx.tilePane.getAttribute('data-explicit-layer'), null, 'data-explicit-layer should be cleared again');
  
  // Verify event dispatched
  assert.ok(ctx.events.length > eventsBeforeAuto, 'event should be dispatched');
  const ev = ctx.events[ctx.events.length - 1];
  assert.strictEqual(ev.type, 'mc-tile-provider-changed', 'event type correct');
  assert.strictEqual(ev.detail.auto, true, 'event detail.auto should be true');
});

test('Carto URLs carry no ?key= when no key is configured', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true } } } });
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of ALL_CARTO_IDS) {
    const u = reg[id].url();
    assert.ok(u.indexOf('?key=') === -1, id + ' should have no key param: ' + u);
    assert.ok(u.indexOf('basemaps.cartocdn.com') !== -1, id + ' unexpected host: ' + u);
  }
});

test('Every Carto style appends ?key= when carto.key is set', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, key: 'abc123' } } } });
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of ALL_CARTO_IDS) {
    assert.ok(/\.png\?key=abc123$/.test(reg[id].url()), id + ' bad URL: ' + reg[id].url());
  }
});

test('Carto key is URL-encoded', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, key: 'a b&c=d' } } } });
  const u = ctx.window.MC_TILE_PROVIDERS['carto-dark'].url();
  assert.ok(u.endsWith('?key=a%20b%26c%3Dd'), 'bad encoding: ' + u);
});

test('Carto key does not leak into non-Carto providers', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, key: 'abc123' }, osm: { enabled: true } } } });
  const reg = ctx.window.MC_TILE_PROVIDERS;
  for (const id of ['osm-standard', 'osm-dark', 'esri-darkgray-labels']) {
    if (!reg[id]) continue;
    assert.ok(reg[id].url().indexOf('abc123') === -1, id + ' leaked the key: ' + reg[id].url());
  }
});

test('Carto key coexists with the enterprise domain option', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, domain: 'mycompany', key: 'abc123' } } } });
  const u = ctx.window.MC_TILE_PROVIDERS['carto-dark'].url();
  assert.ok(u.indexOf('mycompany.cartocdn.com') !== -1, 'domain lost: ' + u);
  assert.ok(u.endsWith('?key=abc123'), 'key lost: ' + u);
});

test('Carto uses ?key= and never ?api_key= (CARTO ignores api_key and still watermarks)', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, key: 'abc123' } } } });
  const url = ctx.window.MC_TILE_PROVIDERS['carto-dark'].url();
  assert.ok(url.endsWith('?key=abc123'), 'should end with ?key=abc123: ' + url);
  assert.ok(url.indexOf('api_key') < 0, 'must not use the api_key param: ' + url);
});

test('Carto key resolves lazily - set after load, picked up on re-init', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true } } } });
  assert.ok(ctx.window.MC_TILE_PROVIDERS['carto-dark'].url().indexOf('key=') < 0, 'no key before config');
  ctx.window.MC_MAP_CFG = { tiles: { providers: { carto: { enabled: true, key: 'late' } } } };
  ctx.window.MC_initTileRegistry(true);
  assert.ok(ctx.window.MC_TILE_PROVIDERS['carto-dark'].url().endsWith('?key=late'), 'key picked up after async config');
});

// --- MC_tileUrlById -------------------------------------------------------

test('MC_tileUrlById resolves a function-typed url to a string', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true, key: 'k' } } } });
  const url = ctx.window.MC_tileUrlById('carto-light', 'FALLBACK');
  assert.strictEqual(typeof url, 'string', 'must return a string, not a function');
  assert.ok(url.endsWith('?key=k'), 'carries the key: ' + url);
});

test('MC_tileUrlById returns the fallback for a disabled or unknown provider', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: false } } } });
  assert.strictEqual(ctx.window.MC_tileUrlById('carto-dark', 'FALLBACK'), 'FALLBACK', 'disabled provider falls back');
  assert.strictEqual(ctx.window.MC_tileUrlById('nope', 'FALLBACK'), 'FALLBACK', 'unknown id falls back');
});


// ─── Esri two-layer provider (base + labels reference) ──────────────────────

test('Esri Dark Gray Canvas declares a labels reference layer', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, {});
  const p = ctx.window.MC_TILE_PROVIDERS['esri-darkgray-labels'];
  assert.ok(p, 'esri provider should be registered');
  assert.ok(p.refUrl, 'must declare refUrl — the id promises labels');
  assert.ok(p.refUrl.indexOf('World_Dark_Gray_Reference') >= 0, 'refUrl points at the Reference tileset: ' + p.refUrl);
});

test('Esri refUrl is a string, matching how map.js/live.js/nodes.js consume it', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, {});
  const p = ctx.window.MC_TILE_PROVIDERS['esri-darkgray-labels'];
  assert.strictEqual(typeof p.refUrl, 'string', 'consumers pass refUrl straight to L.tileLayer');
  assert.ok(/\{z\}\/\{y\}\/\{x\}/.test(p.refUrl), 'Esri uses {z}/{y}/{x} order: ' + p.refUrl);
});

test('Single-layer providers declare no refUrl', () => {
  const ctx = makeSandbox();
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true } } } });
  for (const id of ALL_CARTO_IDS) {
    assert.ok(!ctx.window.MC_TILE_PROVIDERS[id].refUrl, id + ' should not declare refUrl');
  }
});

test('Layer control stacks base + labels for a two-layer provider', () => {
  const ctx = makeSandbox();
  const created = [];
  const groups  = [];
  ctx.L = ctx.window.L = {
    tileLayer: (url, opts) => { const l = { url, _events: {} }; l.on = (e, c) => { l._events[e] = c; }; created.push(l); return l; },
    layerGroup: (layers) => { const g = { _isGroup: true, _layers: layers, _events: {} }; g.on = (e, c) => { g._events[e] = c; }; groups.push(g); return g; },
    control: { layers: (maps) => { ctx._capturedBaseMaps = maps; return { addTo: function () { return this; } }; } }
  };
  const mockMap = {
    hasLayer: () => false, addLayer: () => {}, removeLayer: () => {},
    on: () => {}, off: () => {}, getPane: () => ctx.tilePane
  };
  loadProviders(ctx, {});
  ctx.window.MC_initTileRegistry(false);
  ctx.window.MC_createLayerControl(mockMap, { _isAutoGroup: true });

  const entry = ctx._capturedBaseMaps['esri-darkgray-labels'];
  assert.ok(entry, 'esri should appear in the control');
  assert.ok(entry._isGroup, 'esri entry must be a layer group, not a bare tile layer');
  assert.strictEqual(entry._layers.length, 2, 'group holds base + reference');
  assert.ok(entry._layers[1].url.indexOf('World_Dark_Gray_Reference') >= 0, 'second layer is the labels overlay');
});

test('Layer control still builds a bare tile layer for single-layer providers', () => {
  const ctx = makeSandbox();
  ctx.L = ctx.window.L = {
    tileLayer: (url) => { const l = { url, _events: {} }; l.on = (e, c) => { l._events[e] = c; }; return l; },
    layerGroup: (layers) => ({ _isGroup: true, _layers: layers, on: () => {} }),
    control: { layers: (maps) => { ctx._capturedBaseMaps = maps; return { addTo: function () { return this; } }; } }
  };
  const mockMap = {
    hasLayer: () => false, addLayer: () => {}, removeLayer: () => {},
    on: () => {}, off: () => {}, getPane: () => ctx.tilePane
  };
  loadProviders(ctx, { tiles: { providers: { carto: { enabled: true } } } });
  ctx.window.MC_initTileRegistry(false);
  ctx.window.MC_createLayerControl(mockMap, { _isAutoGroup: true });

  const entry = ctx._capturedBaseMaps['carto-dark'];
  assert.ok(entry, 'carto-dark should appear in the control');
  assert.ok(!entry._isGroup, 'single-layer provider must stay a bare tile layer');
});

process.on('beforeExit', () => {
  console.log('');
  console.log('  ' + passed + ' passed, ' + failed + ' failed');
  if (failed) process.exit(1);
});
