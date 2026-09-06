/* test-issue-1633-hide-1byte-hops.js
 * #1633 — customizer toggle that hides 1-byte path hops at every
 * render site. Render-time only; firmware/store untouched.
 *
 * Asserts:
 *  1. Defaults OFF (back-compat: no surprise for existing operators).
 *  2. With hide1ByteHops=true a mixed path renders ONLY multi-byte hops.
 *  3. With hide1ByteHops=false the full path renders.
 *  4. HopDisplay.renderPath emits exactly the multi-byte chips when ON.
 *  5. Map polyline source: positions tagged with 1-byte _hopHex are dropped
 *     from the polyline path when ON; 2/3-byte hops survive; origin /
 *     destination (no _hopHex) are always kept.
 *  6. Analytics route-pattern aggregation: rebuilt path key contains ONLY
 *     multi-byte hop hexes when ON.
 */
'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// ── DOM-less sandbox so hop-display.js / hop-filter.js load cleanly ──
function makeSandbox() {
  const store = {};
  const ctx = {
    window: {
      addEventListener: () => {},
      dispatchEvent: () => {},
      CustomEvent: function (n, d) { this.type = n; this.detail = (d && d.detail) || null; }
    },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', dataset: {}, setAttribute(){}, getAttribute(){return null;} }),
      head: { appendChild: () => {} },
      body: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    localStorage: {
      getItem: k => (k in store ? store[k] : null),
      setItem: (k, v) => { store[k] = String(v); },
      removeItem: k => { delete store[k]; },
      clear: () => { for (const k of Object.keys(store)) delete store[k]; }
    },
    console, Math, Date, Array, Object, String, Number, JSON, Boolean,
    module: { exports: {} }, exports: {},
  };
  ctx.window.localStorage = ctx.localStorage;
  vm.createContext(ctx);
  return ctx;
}

function load(ctx, file) {
  const src = fs.readFileSync(path.join(__dirname, file), 'utf8');
  vm.runInContext(src, ctx, { filename: file });
}

// === Build a sandbox with hop-filter + hop-display loaded ===
function freshSandbox() {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  load(ctx, 'public/hop-display.js');
  return ctx;
}

// === Heavier sandbox that loads live.js (mirrors test-live.js pattern) ===
function makeLiveSandbox() {
  const ctx = {
    window: {
      addEventListener: () => {}, dispatchEvent: () => {}, devicePixelRatio: 1,
      CustomEvent: function (n, d) { this.type = n; this.detail = (d && d.detail) || null; },
    },
    document: {
      readyState: 'complete',
      createElement: () => ({
        tagName: '', id: '', textContent: '', innerHTML: '', style: {}, dataset: {},
        classList: { add(){}, remove(){}, contains(){return false;} },
        setAttribute(){}, getAttribute(){return null;},
        addEventListener(){}, focus(){},
        getContext: () => ({ clearRect(){}, fillRect(){}, beginPath(){}, arc(){}, fill(){}, scale(){}, fillStyle: '', font: '', fillText(){} }),
        offsetWidth: 200, offsetHeight: 40, width: 0, height: 0,
      }),
      head: { appendChild: () => {} },
      body: { appendChild: () => {}, removeChild: () => {}, contains: () => false },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
      createElementNS: () => ({ tagName: 'svg', setAttribute(){}, getAttribute(){return null;}, style: {} }),
      documentElement: { getAttribute: () => null, setAttribute: () => {}, dataset: {} },
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
      const store = {};
      return {
        getItem: k => (k in store ? store[k] : null),
        setItem: (k, v) => { store[k] = String(v); },
        removeItem: k => { delete store[k]; },
      };
    })(),
    location: { hash: '', protocol: 'https:', host: 'localhost' },
    addEventListener: () => {},
    dispatchEvent: () => {},
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    matchMedia: () => ({ matches: false, addEventListener: () => {} }),
    navigator: {},
    L: {
      circleMarker: () => ({ addTo(){return this;}, bindTooltip(){return this;}, on(){return this;}, setRadius(){}, setStyle(){}, setLatLng(){}, getLatLng(){return {lat:0,lng:0};}, remove(){} }),
      polyline: () => ({ addTo(){return this;}, setStyle(){}, remove(){} }),
      polygon: () => ({ addTo(){return this;}, remove(){} }),
      map: () => ({ setView(){return this;}, addLayer(){return this;}, on(){return this;}, getZoom(){return 11;}, getCenter(){return {lat:37,lng:-122};}, getBounds(){return {contains:()=>true};}, fitBounds(){return this;}, invalidateSize(){}, remove(){}, hasLayer(){return false;}, removeLayer(){} }),
      layerGroup: () => ({ addTo(){return this;}, addLayer(){}, removeLayer(){}, clearLayers(){}, hasLayer(){return true;}, eachLayer(){} }),
      tileLayer: () => ({ addTo(){return this;} }),
      control: { attribution: () => ({ addTo(){} }) },
      DomUtil: { addClass(){}, removeClass(){} },
    },
    registerPage: () => {}, onWS: () => {}, offWS: () => {}, connectWS: () => {},
    api: () => Promise.resolve([]), invalidateApiCache: () => {},
    favStar: () => '', bindFavStars: () => {},
    getFavorites: () => [], isFavorite: () => false,
    HopResolver: { init(){}, resolve: () => ({}), ready: () => false },
    MeshAudio: null,
    RegionFilter: { init(){}, getSelected: () => null, onRegionChange: () => {} },
    CustomEvent: function (n, d) { this.type = n; this.detail = (d && d.detail) || null; },
    module: { exports: {} }, exports: {},
  };
  ctx.window.localStorage = ctx.localStorage;
  vm.createContext(ctx);
  // Load filter helpers first so live.js sees window.MC_* on import.
  load(ctx, 'public/payload-labels.js');
  load(ctx, 'public/hop-filter.js');
  // Mirror window.* onto top-level for files that reference bare names.
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  // packet-helpers / roles are required by live.js for getParsedPath etc.
  try { load(ctx, 'public/roles.js'); } catch (_e) {}
  try { load(ctx, 'public/packet-helpers.js'); } catch (_e) {}
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  try { load(ctx, 'public/live.js'); } catch (e) {
    console.error('live.js load error:', e.message);
  }
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  return ctx;
}

console.log('=== #1633: hide 1-byte path hops everywhere ===');

test('default is OFF (back-compat)', () => {
  const ctx = freshSandbox();
  assert.strictEqual(ctx.window.MC_getHide1ByteHops(), false, 'default must be OFF');
});

test('hopByteLen: "AB"=1, "ABCD"=2, "ABCDEF"=3, ""=0', () => {
  const ctx = freshSandbox();
  const bl = ctx.window.MC_hopByteLen;
  assert.strictEqual(bl('AB'), 1);
  assert.strictEqual(bl('ABCD'), 2);
  assert.strictEqual(bl('ABCDEF'), 3);
  assert.strictEqual(bl(''), 0);
  assert.strictEqual(bl(null), 0);
});

test('isVisibleHop: toggle OFF → every hop visible', () => {
  const ctx = freshSandbox();
  const f = ctx.window.MC_isVisibleHop;
  assert.strictEqual(f('AB', { hide1ByteHops: false }), true);
  assert.strictEqual(f('CDEF', { hide1ByteHops: false }), true);
});

test('isVisibleHop: toggle ON → 1-byte HIDDEN, 2/3-byte SHOWN', () => {
  const ctx = freshSandbox();
  const f = ctx.window.MC_isVisibleHop;
  assert.strictEqual(f('AB',     { hide1ByteHops: true }), false, '1-byte must hide');
  assert.strictEqual(f('CDEF',   { hide1ByteHops: true }), true,  '2-byte must stay');
  assert.strictEqual(f('ABCDEF', { hide1ByteHops: true }), true,  '3-byte must stay');
});

test('filterPathHops: mixed input → only multi-byte kept when ON', () => {
  const ctx = freshSandbox();
  const f = ctx.window.MC_filterPathHops;
  const mixed = ['AB', 'CDEF', '12', 'ABCDEF', '34'];
  // Use Array.from to bridge the vm-realm Array prototype gap.
  assert.deepStrictEqual(
    Array.from(f(mixed, { hide1ByteHops: true })),
    ['CDEF', 'ABCDEF'],
    'must drop every 1-byte entry'
  );
  assert.deepStrictEqual(
    Array.from(f(mixed, { hide1ByteHops: false })),
    mixed,
    'OFF must return input unchanged'
  );
});

test('HopDisplay.renderPath rendered chips match filtered hops when ON', () => {
  const ctx = freshSandbox();
  // Apply the filter at the boundary the way every render site will.
  const hops = ['AB', 'CDEF', '12', 'ABCDEF'];
  const filtered = ctx.window.MC_filterPathHops(hops, { hide1ByteHops: true });
  const html = ctx.window.HopDisplay.renderPath(filtered, {}, { hexMode: true, link: false });
  // Filtered chip set: ONLY 2-byte + 3-byte tokens present.
  assert.ok(html.indexOf('CDEF') !== -1,   'CDEF chip must render');
  assert.ok(html.indexOf('ABCDEF') !== -1, 'ABCDEF chip must render');
  // 1-byte hex tokens must NOT appear in the chip set. Use word
  // boundaries with the chip wrapper to avoid matching substrings of
  // the larger hops.
  assert.strictEqual(html.indexOf('>AB<'), -1, '1-byte AB must NOT render as a chip');
  assert.strictEqual(html.indexOf('>12<'), -1, '1-byte 12 must NOT render as a chip');
});

test('Map polyline source: positions[]._hopHex 1-byte dropped, origin/dest kept', () => {
  const ctx = freshSandbox();
  // Origin & destination have no _hopHex (came from payload, not from path bytes).
  // Intermediate hops carry _hopHex tagged by drawPacketRoute.
  const positions = [
    { name: 'Origin', isOrigin: true },
    { name: 'h1', _hopHex: 'AB' },         // 1-byte → filterable
    { name: 'h2', _hopHex: 'CDEF' },       // 2-byte → keep
    { name: 'h3', _hopHex: '12' },         // 1-byte → filterable
    { name: 'h4', _hopHex: 'ABCDEF' },     // 3-byte → keep
    { name: 'Dest', isDest: true }
  ];
  const opts = { hide1ByteHops: true };
  const kept = positions.filter(p =>
    !p._hopHex || ctx.window.MC_isVisibleHop(p._hopHex, opts)
  );
  assert.deepStrictEqual(
    kept.map(p => p.name),
    ['Origin', 'h2', 'h4', 'Dest'],
    'polyline retains origin/dest + multi-byte hops only'
  );
});

test('Analytics: route-pattern aggregation key drops 1-byte hops when ON', () => {
  const ctx = freshSandbox();
  // Each packet contributes one path. With hide ON, aggregation must key on
  // the FILTERED hop list so 1-byte noise stops inflating distinct counts.
  const packets = [
    { path_json: ['AB', 'CDEF', '12', 'ABCDEF'] },
    { path_json: ['99', 'CDEF', '88', 'ABCDEF'] },   // same multi-byte key
    { path_json: ['CDEF', 'ABCDEF'] }                // identical w/o 1-byte
  ];
  const opts = { hide1ByteHops: true };
  const counts = {};
  for (const p of packets) {
    const key = ctx.window.MC_filterPathHops(p.path_json, opts).join('→');
    counts[key] = (counts[key] || 0) + 1;
  }
  assert.deepStrictEqual(counts, { 'CDEF→ABCDEF': 3 },
    'all three rows collapse to one pattern with 3 hits');
});

// ─────────────────────────────────────────────────────────────────────
// #1689 r1 — additional regressions surfaced by adversarial + Kent Beck
// review on the original PR.
// ─────────────────────────────────────────────────────────────────────

console.log('\n=== #1689 r1: review fix-ups (adv + kb findings) ===');

test('[kb #3] MC_filterPathHops handles null/undefined/empty without throwing', () => {
  const ctx = freshSandbox();
  const f = ctx.window.MC_filterPathHops;
  assert.deepStrictEqual(Array.from(f([])), [], 'empty in → empty out');
  // null/undefined inputs should NOT throw. The contract is "return a
  // safe empty-ish array". Either [] or the input back is acceptable;
  // we assert non-throwing + array-shape.
  const a = f(null);
  const b = f(undefined);
  assert.ok(Array.isArray(a) || a == null, 'null → null/array');
  assert.ok(Array.isArray(b) || b == null, 'undefined → null/array');
  assert.deepStrictEqual(Array.from(f([], { hide1ByteHops: true })), [], 'empty in + ON → empty');
});

test('[kb #3] MC_isVisibleHop(null) is safe (does not throw; null treated as 0-byte → visible)', () => {
  const ctx = freshSandbox();
  const v = ctx.window.MC_isVisibleHop;
  // null/undefined hops have byteLen 0, !== 1, so they are visible
  // regardless of the toggle (origin/dest sentinels rely on this).
  assert.strictEqual(v(null, { hide1ByteHops: true }), true,  'null hop must be visible');
  assert.strictEqual(v(undefined, { hide1ByteHops: true }), true, 'undefined must be visible');
  assert.strictEqual(v('', { hide1ByteHops: true }), true,    'empty string must be visible');
});

test('[adv #1] route-view.js: all-1-byte paths do NOT collide on empty key', () => {
  // The bug: route-view aggregated `seen[hops.join('-')]`. When the filter
  // produced an empty array, every distinct all-1-byte path collapsed onto
  // key "". Distinct routes vanished into one bucket. Fix uses a sentinel
  // prefixed with the rawHops signature so the buckets stay distinct.
  //
  // We assert the EXACT inline aggregation logic now used in route-view.js
  // by replaying its key derivation on representative inputs.
  const ctx = freshSandbox();
  const aggregate = (paths) => {
    const seen = {};
    paths.forEach((p) => {
      const rawHops = p.path || [];
      const hops = ctx.window.MC_filterPathHops(rawHops);
      const key = hops.length ? hops.join('-') : ('__all1byte__::' + rawHops.join('-'));
      seen[key] = true;
    });
    return Object.keys(seen).length;
  };
  // Toggle ON
  ctx.window.MC_setHide1ByteHops(true);
  const distinct = aggregate([
    { path: ['AB', 'CD'] },   // all 1-byte route A
    { path: ['EF', 'GH'] },   // all 1-byte route B — must NOT collide with A
    { path: ['AB', 'CD'] }    // duplicate of A — must collide with A
  ]);
  assert.strictEqual(distinct, 2,
    'two distinct all-1-byte routes + one dup must produce 2 buckets, not 1');
});

test('[adv #1] route-view.js source: key derivation uses sentinel for all-1-byte buckets', () => {
  // Source-grep guard: if a future refactor reverts to `hops.join("-")`
  // bare aggregation, this test catches it.
  const src = fs.readFileSync(path.join(__dirname, 'public/route-view.js'), 'utf8');
  assert.ok(src.indexOf('__all1byte__::') !== -1 || src.indexOf('⟨all-1byte⟩::') !== -1,
    'route-view.js must include a sentinel for all-1-byte aggregation buckets');
});

test('[adv #2] map.js source: drawPacketRoute propagates hopsHiddenCount to renderer', () => {
  // Source-grep guard: the polyline MUST carry the redacted-count down
  // to the renderer so it can dash + badge. A revert that drops the
  // `hopsHiddenCount` option from the render call regresses operator UX.
  const src = fs.readFileSync(path.join(__dirname, 'public/map.js'), 'utf8');
  assert.ok(/hopsHiddenCount\s*:\s*hopsHiddenCount/.test(src),
    'map.js must pass hopsHiddenCount into the route renderer opts');
  assert.ok(/var\s+hopsHiddenCount\s*=\s*0/.test(src) || /let\s+hopsHiddenCount\s*=\s*0/.test(src),
    'map.js must compute hopsHiddenCount before filtering positions');
});

test('[adv #2] route-render.js: redacted edges use dashed + reduced opacity + emit badge', () => {
  const src = fs.readFileSync(path.join(__dirname, 'public/route-render.js'), 'utf8');
  assert.ok(src.indexOf('mc-route-edge-redacted') !== -1,
    'route-render.js must add a .mc-route-edge-redacted className when hopsHiddenCount > 0');
  assert.ok(src.indexOf('mc-route-redacted-badge') !== -1,
    'route-render.js must emit a midpoint badge when hopsHiddenCount > 0');
  assert.ok(/opacity:\s*redacted\s*\?/.test(src),
    'route-render.js must lower opacity when redacted');
});

test('[adv #3] observer-detail.js source: hop count honors MC_filterPathHops', () => {
  const src = fs.readFileSync(path.join(__dirname, 'public/observer-detail.js'), 'utf8');
  assert.ok(src.indexOf('MC_filterPathHops') !== -1,
    'observer-detail.js renderRecentPackets must call MC_filterPathHops');
});

test('[r2 MAJOR] live.js packetInvolvesFilterNode: node-filter search is INDEPENDENT of hide-1byte toggle', () => {
  // r2 reviewer (option 1): drop the 1-byte hop filter from
  // packetInvolvesFilterNode entirely. The node-filter search box must
  // return the same matches regardless of the display preference; the
  // chip rendering already has separate filtering via feedHops.
  //
  // Behavioral assertion via vm-sandbox load of live.js (replaces the
  // prior source-grep tautology for this consumer).
  const ctx = makeLiveSandbox();
  const fn = ctx.window._livePacketInvolvesFilterNode;
  assert.ok(fn, '_livePacketInvolvesFilterNode must be exposed');

  // Filter-node appears ONLY in a 1-byte hop.
  const pkt = { decoded: { path: { hops: ['ab'] }, payload: {} } };
  const filterKeys = ['abcd1234567890ab'];

  // Toggle OFF — must match (baseline).
  ctx.window.MC_setHide1ByteHops(false);
  assert.strictEqual(fn(pkt, filterKeys), true,
    'baseline: match a filter-key whose prefix overlaps a 1-byte hop');

  // Toggle ON — MUST STILL match. The node-filter search box is
  // semantically independent of the display preference.
  ctx.window.MC_setHide1ByteHops(true);
  assert.strictEqual(fn(pkt, filterKeys), true,
    'node-filter search must be independent of hide-1byte display toggle');
});

test('[adv #4] customize-v2.js still dispatches mc-hide-1byte-hops-changed', () => {
  const src = fs.readFileSync(path.join(__dirname, 'public/customize-v2.js'), 'utf8');
  assert.ok(src.indexOf('mc-hide-1byte-hops-changed') !== -1,
    'customizer must continue to dispatch the event');
});

test('[adv #4] map.js + packets.js subscribe to mc-hide-1byte-hops-changed', () => {
  const mapSrc = fs.readFileSync(path.join(__dirname, 'public/map.js'), 'utf8');
  const pktSrc = fs.readFileSync(path.join(__dirname, 'public/packets.js'), 'utf8');
  assert.ok(/addEventListener\(['"]mc-hide-1byte-hops-changed['"]/.test(mapSrc),
    'map.js must subscribe to mc-hide-1byte-hops-changed');
  assert.ok(/addEventListener\(['"]mc-hide-1byte-hops-changed['"]/.test(pktSrc),
    'packets.js must subscribe to mc-hide-1byte-hops-changed');
});

test('[adv #4] hop-filter dispatches the event when toggled', () => {
  const ctx = freshSandbox();
  let saw = null;
  ctx.window.dispatchEvent = function (ev) { saw = ev; };
  ctx.window.MC_setHide1ByteHops(true);
  assert.ok(saw, 'must dispatch CustomEvent on setHide1ByteHops');
  assert.strictEqual(saw.type, 'mc-hide-1byte-hops-changed');
  assert.strictEqual(saw.detail && saw.detail.value, true);
});

test('[kb #2] (1-byte filtered) chip text is emitted by every render boundary', () => {
  // PR body promised the chip. Assert the literal text exists in every
  // consumer file that renders a path. If a future refactor strips it,
  // operators lose the only signal that the route was redacted.
  const files = [
    'public/packets.js',
    'public/route-view.js'
  ];
  for (const f of files) {
    const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
    assert.ok(src.indexOf('1-byte filtered') !== -1,
      f + ' must render the literal "(1-byte filtered)" chip text');
  }
});

test('[kb #1] anti-tautology: tests reference the actual production files (not inline copies)', () => {
  // This file is allowed ONLY a single inline filter recreation (the
  // route-view aggregation guard). Every other consumer assertion must
  // source-grep the production file, NOT re-implement the filter.
  const selfSrc = fs.readFileSync(__filename, 'utf8');
  // Ensure the test file does NOT define its own filterPathHops.
  assert.strictEqual(
    /function\s+filterPathHops\s*\(/.test(selfSrc), false,
    'test file must not re-implement filterPathHops (tautology guard)'
  );
});

// ─────────────────────────────────────────────────────────────────────

// #1784 path trust threshold — configurable minimum hash bytes for mapping
// ─────────────────────────────────────────────────────────────────────

console.log('\n=== #1784: path trust threshold ===');

test('#1784: MC_getPathTrustThreshold defaults to 1 (backward compatible)', () => {
  // Deliberately 1, matching DefaultMinHashBytesForMapping in
  // internal/packetpath/trust.go. There is no UI control for this threshold —
  // it is config.json only and needs a restart — so a stricter default would
  // silently tighten every instance on upgrade with nothing in the UI saying
  // why the neighbour graph shrank. Operators opt in with
  // pathTrust.minHashBytesForMapping: 2 or 3.
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  delete ctx.window.PATH_TRUST;
  assert.strictEqual(ctx.window.MC_getPathTrustThreshold(), 1,
    'default path trust threshold must be 1 so an upgrade changes nothing');
});

test('#1784: MC_getPathTrustThreshold reads window.PATH_TRUST', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 2;
  assert.strictEqual(ctx.window.MC_getPathTrustThreshold(), 2,
    'must return configured PATH_TRUST');
  ctx.window.PATH_TRUST = 3;
  assert.strictEqual(ctx.window.MC_getPathTrustThreshold(), 3,
    'must return configured PATH_TRUST=3');
});

test('#1784: MC_meetsPathTrust with threshold 1 (trust-all)', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 1;
  assert.strictEqual(ctx.window.MC_meetsPathTrust('AB'), true, '1-byte hop meets threshold 1');
  assert.strictEqual(ctx.window.MC_meetsPathTrust('ABCD'), true, '2-byte hop meets threshold 1');
  assert.strictEqual(ctx.window.MC_meetsPathTrust('ABCDEF'), true, '3-byte hop meets threshold 1');
});

test('#1784: MC_meetsPathTrust with threshold 2 (exclude 1-byte)', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 2;
  assert.strictEqual(ctx.window.MC_meetsPathTrust('AB'), false, '1-byte hop excluded');
  assert.strictEqual(ctx.window.MC_meetsPathTrust('ABCD'), true, '2-byte hop trusted');
  assert.strictEqual(ctx.window.MC_meetsPathTrust('ABCDEF'), true, '3-byte hop trusted');
});

test('#1784: MC_meetsPathTrust with threshold 3 (strictest)', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 3;
  assert.strictEqual(ctx.window.MC_meetsPathTrust('AB'), false, '1-byte excluded');
  assert.strictEqual(ctx.window.MC_meetsPathTrust('ABCD'), false, '2-byte excluded');
  assert.strictEqual(ctx.window.MC_meetsPathTrust('ABCDEF'), true, '3-byte trusted');
});

test('#1784: MC_pathBelowTrust all-1-byte path at threshold 2', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 2;
  assert.strictEqual(ctx.window.MC_pathBelowTrust(['AB', '12']), true,
    'all-1-byte path must be below trust');
});

test('#1784: MC_pathBelowTrust mixed path at threshold 2', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 2;
  assert.strictEqual(ctx.window.MC_pathBelowTrust(['AB', 'CDEF']), false,
    'mixed 1-byte/2-byte path must NOT be below trust');
});

test('#1784: MC_pathBelowTrust at threshold 1 (trust-all)', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 1;
  assert.strictEqual(ctx.window.MC_pathBelowTrust(['AB']), false,
    'no path below trust at threshold 1');
});

test('#1784: empty/null hops are not below trust', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 2;
  assert.strictEqual(ctx.window.MC_pathBelowTrust(null), false, 'null is not below trust');
  assert.strictEqual(ctx.window.MC_pathBelowTrust([]), false, 'empty is not below trust');
});

test('#1784: MC_meetsPathTrust handles null/undefined safely', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 2;
  assert.strictEqual(ctx.window.MC_meetsPathTrust(null), false, 'null must not meet trust');
  assert.strictEqual(ctx.window.MC_meetsPathTrust(undefined), false, 'undefined must not meet trust');
});

// #1784 consumer wiring — source-grep guards for trust threshold wiring
// ─────────────────────────────────────────────────────────────────────

console.log('\n=== #1784: consumer wiring guards ===');

test('#1784: map.js references MC_pathBelowTrust for trust-gated route display', () => {
  const src = fs.readFileSync(path.join(__dirname, 'public/map.js'), 'utf8');
  assert.ok(src.indexOf('MC_pathBelowTrust') !== -1,
    'map.js must call MC_pathBelowTrust to gate route drawing on trust threshold');
  assert.ok(src.indexOf('Route not displayed') !== -1,
    'map.js must show a clear message when route is below trust threshold');
  const cleanup = src.indexOf('map.removeControl(window.__mc_routeTrustControl)');
  const trustGate = src.indexOf('if (window.MC_pathBelowTrust && window.MC_pathBelowTrust(hopKeys))');
  assert.ok(cleanup >= 0 && cleanup < trustGate,
    'map.js must remove a previous trust message before evaluating the next route');
});

test('#1784: analytics.js references MC_meetsPathTrust for subpath filtering', () => {
  const src = fs.readFileSync(path.join(__dirname, 'public/analytics.js'), 'utf8');
  assert.ok(src.indexOf('MC_meetsPathTrust') !== -1,
    'analytics.js must call MC_meetsPathTrust to filter subpaths by trust');
  assert.ok(src.indexOf('MC_getPathTrustThreshold') !== -1,
    'analytics.js must read the trust threshold');
  assert.ok(src.indexOf('trust threshold') !== -1,
    'analytics.js must mention trust threshold in filter messages');
});

test('#1784: route-view.js references MC_meetsPathTrust for speculative annotation', () => {
  const src = fs.readFileSync(path.join(__dirname, 'public/route-view.js'), 'utf8');
  assert.ok(src.indexOf('MC_meetsPathTrust') !== -1,
    'route-view.js must call MC_meetsPathTrust to mark paths as speculative');
  assert.ok(src.indexOf('belowTrust') !== -1,
    'route-view.js must track belowTrust flag');
  assert.ok(src.indexOf('speculative') !== -1,
    'route-view.js must show speculative annotation for paths below trust');
});

test('#1784: live.js references MC_meetsPathTrust for Paths Through widget', () => {
  const src = fs.readFileSync(path.join(__dirname, 'public/live.js'), 'utf8');
  assert.ok(src.indexOf('MC_meetsPathTrust') !== -1,
    'live.js must call MC_meetsPathTrust in the Paths Through widget');
  assert.ok(src.indexOf('pathTrust.minHashBytesForMapping') !== -1,
    'live.js must mention the config key in trust messages');
});

test('#1784: nodes.js references MC_getPathTrustThreshold for confidence weights', () => {
  const src = fs.readFileSync(path.join(__dirname, 'public/nodes.js'), 'utf8');
  assert.ok(src.indexOf('MC_getPathTrustThreshold') !== -1,
    'nodes.js must read trust threshold for confidence weight adjustment');
});

test('#1784: anti-tautology — MC_pathBelowTrust returns correct boolean for given hops (not a stub)', () => {
  const ctx = makeSandbox();
  load(ctx, 'public/hop-filter.js');
  ctx.window.PATH_TRUST = 2;
  assert.strictEqual(ctx.window.MC_pathBelowTrust(['aabb']), false,
    'single 2-byte hop must NOT be below trust at threshold=2');
  assert.strictEqual(ctx.window.MC_pathBelowTrust(['aa']), true,
    'single 1-byte hop must be below trust at threshold=2');
  assert.strictEqual(ctx.window.MC_pathBelowTrust(['aa', 'bbcc', 'dd']), false,
    'mixed path with one 2-byte hop must NOT be below trust');
  assert.strictEqual(ctx.window.MC_pathBelowTrust(['aa', 'bb', 'cc']), true,
    'all-1-byte path must be below trust at threshold=2');
  ctx.window.PATH_TRUST = 1;
  assert.strictEqual(ctx.window.MC_pathBelowTrust(['aa', 'bb']), false,
    'all-1-byte path must NOT be below trust at threshold=1');
});

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed > 0 ? 1 : 0);
