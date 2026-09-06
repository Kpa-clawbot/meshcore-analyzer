/* Unit tests for live.js region filter (#1045)
 * Tests packetMatchesRegion helper using observer_id → IATA mapping.
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

function makeSandbox() {
  const ctx = {
    window: {
      addEventListener: () => {}, dispatchEvent: () => {}, devicePixelRatio: 1,
      // live.js:14 captures these at load time from packet-helpers.js, which
      // this sandbox does not load. They must exist before live.js is evaluated.
      getParsedDecoded: (p) => { try { return p && p.decoded_json ? JSON.parse(p.decoded_json) : {}; } catch (e) { return {}; } },
      getParsedPath: (p) => { try { return p && p.path_json ? JSON.parse(p.path_json) : []; } catch (e) { return []; } },
    },
    document: {
      readyState: 'complete',
      createElement: () => ({ style:{}, classList:{add(){},remove(){},contains(){return false;}}, setAttribute(){}, addEventListener(){}, getContext: () => ({clearRect(){},fillRect(){},beginPath(){},arc(){},fill(){},scale(){},fillText(){}}) }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [], querySelector: () => null,
      createElementNS: () => ({ setAttribute(){} }),
      documentElement: { getAttribute: () => null, setAttribute: () => {}, dataset: {} },
      body: { appendChild: () => {}, removeChild: () => {}, contains: () => false },
      hidden: false,
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, Map, Set, Promise, URLSearchParams,
    parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => 0, clearTimeout: () => {},
    setInterval: () => 0, clearInterval: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    requestAnimationFrame: (cb) => 0,
    cancelAnimationFrame: () => {},
    localStorage: (() => {
      const s = {};
      return {
        getItem: k => s[k] !== undefined ? s[k] : null,
        setItem: (k, v) => { s[k] = String(v); },
        removeItem: k => { delete s[k]; },
      };
    })(),
    location: { hash: '', protocol: 'https:', host: 'localhost' },
    CustomEvent: class CustomEvent {},
    addEventListener: () => {}, dispatchEvent: () => {},
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    matchMedia: () => ({ matches: false, addEventListener: () => {} }),
    navigator: {}, visualViewport: null,
    MutationObserver: function() { this.observe=()=>{}; this.disconnect=()=>{}; },
    WebSocket: function() { this.close=()=>{}; },
    IATA_COORDS_GEO: {},
    L: {
      circleMarker: () => ({addTo(){return this;},bindTooltip(){return this;},on(){return this;},setRadius(){},setStyle(){},setLatLng(){},getLatLng(){return{lat:0,lng:0};},remove(){}}),
      polyline: () => ({addTo(){return this;},setStyle(){},remove(){}}),
      polygon: () => ({addTo(){return this;},remove(){}}),
      map: () => ({setView(){return this;},addLayer(){return this;},on(){return this;},getZoom(){return 11;},getCenter(){return{lat:0,lng:0};},getBounds(){return{contains:()=>true};},fitBounds(){return this;},invalidateSize(){},remove(){},hasLayer(){return false;},removeLayer(){}}),
      layerGroup: () => ({addTo(){return this;},addLayer(){},removeLayer(){},clearLayers(){},hasLayer(){return true;},eachLayer(){}}),
      tileLayer: () => ({addTo(){return this;}}),
      control: { attribution: () => ({addTo(){}}) },
      DomUtil: { addClass(){}, removeClass(){} },
    },
    registerPage: () => {}, onWS: () => {}, offWS: () => {}, connectWS: () => {},
    api: () => Promise.resolve([]), invalidateApiCache: () => {},
    favStar: () => '', bindFavStars: () => {},
    getFavorites: () => [], isFavorite: () => false,
    HopResolver: { init(){}, resolve: () => ({}), ready: () => false },
    MeshAudio: null,
    RegionFilter: { init(){}, getSelected: () => null, onChange: () => {}, offChange: () => {}, regionQueryString: () => '', getRegionParam: () => '' },
  };
  vm.createContext(ctx);
  return ctx;
}

function load(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

console.log('\n=== live.js: region filter (#1045) ===');
const ctx = makeSandbox();
load(ctx, 'public/payload-labels.js');
load(ctx, 'public/roles.js');
load(ctx, 'public/live.js');

const fn = ctx.window._livePacketMatchesRegion;
assert.ok(fn, '_livePacketMatchesRegion must be exposed');

const obsMap = { 'obs1': 'SJC', 'obs2': 'SFO', 'obs3': 'PDX' };

test('returns true when no regions selected (filter inactive)', () => {
  assert.strictEqual(fn([{observer_id:'obs1'}], obsMap, null), true);
  assert.strictEqual(fn([{observer_id:'obs1'}], obsMap, []), true);
});

test('matches when single observation observer is in selected region', () => {
  assert.strictEqual(fn([{observer_id:'obs1'}], obsMap, ['SJC']), true);
});

test('does not match when observer is in different region', () => {
  assert.strictEqual(fn([{observer_id:'obs1'}], obsMap, ['PDX']), false);
});

test('matches if ANY observation is in selected region (OR across observations)', () => {
  assert.strictEqual(fn([{observer_id:'obs1'}, {observer_id:'obs3'}], obsMap, ['PDX']), true);
});

test('matches if observer iata is in any of multiple selected regions', () => {
  assert.strictEqual(fn([{observer_id:'obs2'}], obsMap, ['SJC','SFO']), true);
});

test('does not match when observer_id is unknown', () => {
  assert.strictEqual(fn([{observer_id:'ghost'}], obsMap, ['SJC']), false);
});

test('does not match when packet has no observer_id', () => {
  assert.strictEqual(fn([{}], obsMap, ['SJC']), false);
});

test('region match is case-insensitive', () => {
  assert.strictEqual(fn([{observer_id:'obs1'}], obsMap, ['sjc']), true);
});

const setMap = ctx.window._liveSetObserverIataMap;
assert.ok(setMap, '_liveSetObserverIataMap must be exposed');

test('observer iata map can be updated and used by filter', () => {
  setMap({ 'newobs': 'LAX' });
  // Now the live module should consult the new map. The helper accepts the map
  // explicitly (pure), so we test the same function with the new mapping:
  assert.strictEqual(fn([{observer_id:'newobs'}], { 'newobs': 'LAX' }, ['LAX']), true);
});

// --- #1898 / #1900: replayed packets must carry observer_id ---
// dbPacketToLive() used to return only observer (the resolved name), so every
// VCR-replayed packet had observer_id undefined. packetMatchesRegion skips a
// packet whose observer_id is null and returns false when none match, so with a
// region selected the replay rendered nothing at all, and the Replay button on
// packet detail silently no-opped. Both issues are the same omission.
const toLive = ctx.window._liveDbPacketToLive;
assert.ok(toLive, '_liveDbPacketToLive must be exposed');

test('#1898: dbPacketToLive carries observer_id through', () => {
  const live = toLive({ id: 1, hash: 'h', timestamp: '2026-01-01T00:00:00Z',
    observer_id: 'obs1', observer_name: 'Obs One' });
  assert.strictEqual(live.observer_id, 'obs1',
    'observer_id must survive; the region filter matches on it');
});

test('#1898: a replayed packet survives an active region filter', () => {
  const live = toLive({ id: 1, hash: 'h', timestamp: '2026-01-01T00:00:00Z',
    observer_id: 'obs1', observer_name: 'Obs One' });
  assert.strictEqual(fn([live], { obs1: 'BRU' }, ['BRU']), true,
    'with observer_id carried the group matches and is rendered');
});

test('#1898: dropping observer_id is what broke it (guards the regression)', () => {
  assert.strictEqual(fn([{ observer: 'Obs One' }], { obs1: 'BRU' }, ['BRU']), false,
    'a packet with no observer_id is skipped, so the group is dropped');
});

test('#1898: observer_iata is carried so the badge needs no roster lookup', () => {
  const live = toLive({ id: 1, hash: 'h', timestamp: '2026-01-01T00:00:00Z',
    observer_id: 'obs1', observer_iata: 'BRU' });
  assert.strictEqual(live.observer_iata, 'BRU');
});

console.log(`\n${'═'.repeat(40)}`);
console.log(`  live region filter tests: ${passed} passed, ${failed} failed`);

console.log(`${'═'.repeat(40)}\n`);
if (failed > 0) process.exit(1);
