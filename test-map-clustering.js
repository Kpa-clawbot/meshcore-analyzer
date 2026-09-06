/* Unit tests for map.js clustering integration (issue #1036)
 *
 * Verifies:
 *   - makeClusterIcon produces a divIcon HTML containing the total + per-role pills
 *   - createClusterGroup instantiates an L.MarkerClusterGroup with the required options
 *   - The cluster group accepts markers via addLayer
 *
 * Tests run in a jsdom-free vm sandbox with a tiny Leaflet/Leaflet.markercluster
 * shim so we exercise our integration code (not the library itself).
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// ---- Tiny Leaflet shim ----
function makeLeafletShim() {
  const L = {};
  L.point = (x, y) => ({ x, y });
  L.latLng = (a, b) => ({ lat: a, lng: b });
  L.divIcon = (opts) => ({ _isDivIcon: true, options: opts, html: opts.html, className: opts.className });
  L.layerGroup = () => {
    const g = { _layers: [], addLayer(m){ this._layers.push(m); return this; }, removeLayer(m){ const i=this._layers.indexOf(m); if(i>=0) this._layers.splice(i,1); return this; }, clearLayers(){ this._layers=[]; return this; }, eachLayer(fn){ this._layers.forEach(fn); }, addTo(){ return this; }, hasLayer(m){ return this._layers.includes(m); } };
    return g;
  };
  L.marker = (latlng, opts) => ({ _isMarker: true, _latlng: latlng, options: opts || {}, getLatLng(){ return this._latlng; }, bindPopup(){ return this; }, bindTooltip(){ return this; } });
  // markercluster shim
  function MarkerClusterGroup(opts) {
    this.options = opts || {};
    this._layers = [];
    this._isClusterGroup = true;
  }
  MarkerClusterGroup.prototype.addLayer = function (m) { this._layers.push(m); return this; };
  MarkerClusterGroup.prototype.addLayers = function (ms) { ms.forEach(m => this._layers.push(m)); return this; };
  MarkerClusterGroup.prototype.removeLayer = function (m) { const i=this._layers.indexOf(m); if(i>=0) this._layers.splice(i,1); return this; };
  MarkerClusterGroup.prototype.clearLayers = function () { this._layers = []; return this; };
  MarkerClusterGroup.prototype.eachLayer = function (fn) { this._layers.forEach(fn); };
  MarkerClusterGroup.prototype.hasLayer = function (m) { return this._layers.includes(m); };
  MarkerClusterGroup.prototype.addTo = function () { return this; };
  MarkerClusterGroup.prototype.getLayers = function () { return this._layers.slice(); };
  L.MarkerClusterGroup = MarkerClusterGroup;
  L.markerClusterGroup = (opts) => new MarkerClusterGroup(opts);
  return L;
}

function makeSandbox() {
  const ctx = {
    window: {},
    document: { addEventListener(){}, getElementById(){ return null; }, querySelector(){ return null; }, querySelectorAll(){ return []; }, createElement(){ return { id:'', textContent:'', innerHTML:'', appendChild(){}, addEventListener(){}, setAttribute(){}, classList:{add(){},remove(){},toggle(){}} }; }, head: { appendChild(){} }, body: { appendChild(){} } },
    console, Date, Math, Array, Object, String, Number, JSON, RegExp, Error,
    parseInt, parseFloat, isFinite, isNaN, Map, Set, Promise,
    setTimeout: ()=>{}, clearTimeout: ()=>{}, setInterval: ()=>{}, clearInterval: ()=>{},
    registerPage: () => {}, esc: (s) => s, onWS: () => {}, offWS: () => {},
    localStorage: (() => { const s={}; return { getItem:k=>s[k]||null, setItem:(k,v)=>{s[k]=String(v);}, removeItem:k=>{delete s[k];} }; })(),
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    addEventListener(){}, dispatchEvent(){},
    L: makeLeafletShim(),
  };
  ctx.window.L = ctx.L;
  vm.createContext(ctx);
  // Load roles for ROLE_COLORS palette
  vm.runInContext(fs.readFileSync('public/roles.js','utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  // Load map.js (IIFE — exposes test hooks via window.__meshcoreMapInternals)
  vm.runInContext(fs.readFileSync('public/map.js','utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  return ctx;
}

console.log('\n=== map.js: clustering ===');
{
  const ctx = makeSandbox();
  const internals = ctx.window.__meshcoreMapInternals;

  test('exposes test hooks (__meshcoreMapInternals)', () => {
    assert.ok(internals, 'window.__meshcoreMapInternals not exposed by map.js');
    assert.ok(typeof internals.makeClusterIcon === 'function', 'makeClusterIcon not exported');
    assert.ok(typeof internals.createClusterGroup === 'function', 'createClusterGroup not exported');
  });

  test('createClusterGroup returns an L.MarkerClusterGroup with required options', () => {
    const g = internals.createClusterGroup();
    assert.ok(g, 'createClusterGroup returned falsy');
    assert.ok(g instanceof ctx.L.MarkerClusterGroup, 'expected L.MarkerClusterGroup instance');
    assert.strictEqual(g.options.chunkedLoading, true, 'chunkedLoading should be true');
    assert.strictEqual(g.options.removeOutsideVisibleBounds, true, 'removeOutsideVisibleBounds should be true');
    assert.strictEqual(g.options.disableClusteringAtZoom, 16, 'disableClusteringAtZoom should be 16');
    assert.strictEqual(g.options.spiderfyOnMaxZoom, true, 'spiderfyOnMaxZoom should be true');
    assert.strictEqual(typeof g.options.iconCreateFunction, 'function', 'iconCreateFunction should be set');
  });

  test('cluster group accepts markers via addLayer', () => {
    const g = internals.createClusterGroup();
    const m1 = ctx.L.marker(ctx.L.latLng(37.7, -122.4));
    const m2 = ctx.L.marker(ctx.L.latLng(37.8, -122.5));
    g.addLayer(m1);
    g.addLayer(m2);
    assert.strictEqual(g.getLayers().length, 2, 'cluster group should hold added markers');
  });

  test('makeClusterIcon: includes total count and role-pill counts', () => {
    const markers = [
      { _role: 'repeater' }, { _role: 'repeater' }, { _role: 'repeater' },
      { _role: 'companion' }, { _role: 'companion' },
      { _role: 'room' },
    ];
    const cluster = { getAllChildMarkers: () => markers, getChildCount: () => markers.length };
    const icon = internals.makeClusterIcon(cluster);
    assert.ok(icon && icon._isDivIcon, 'expected an L.divIcon');
    const html = icon.html || '';
    assert.ok(/>6</.test(html) || html.indexOf('>6<') >= 0, `total count 6 not in html: ${html}`);
    // Role letters disambiguate counts without relying on color alone.
    assert.ok(/class="mc-pill role-repeater"[^>]*>R3</.test(html), `repeater pill (R3) not in html: ${html}`);
    assert.ok(/class="mc-pill role-companion"[^>]*>C2</.test(html), `companion pill (C2) not in html: ${html}`);
    assert.ok(/class="mc-pill role-room"[^>]*>M1</.test(html), `room pill (M1) not in html: ${html}`);
    // CoreScope-themed wrapper class
    assert.ok((icon.className || '').indexOf('mc-cluster') >= 0, `expected mc-cluster class, got: ${icon.className}`);
  });

  test('makeClusterIcon: bucket sm/md/lg by total', () => {
    const mk = (n, role='companion') => Array.from({length:n}, () => ({ _role: role }));
    function clusterOf(n) { const ms = mk(n); return { getAllChildMarkers: () => ms, getChildCount: () => n }; }
    const small = internals.makeClusterIcon(clusterOf(5));
    const med   = internals.makeClusterIcon(clusterOf(40));
    const large = internals.makeClusterIcon(clusterOf(150));
    assert.ok(/mc-sm/.test(small.html || small.className || ''), 'small bucket missing');
    assert.ok(/mc-md/.test(med.html || med.className || ''), 'medium bucket missing');
    assert.ok(/mc-lg/.test(large.html || large.className || ''), 'large bucket missing');
  });
}

if (failed > 0) {
  console.log(`\n${failed} test(s) failed, ${passed} passed`);
  process.exit(1);
}
console.log(`\nAll ${passed} test(s) passed`);
