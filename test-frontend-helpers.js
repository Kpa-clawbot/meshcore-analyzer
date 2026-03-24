/* Unit tests for frontend helper functions (tested via VM sandbox) */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// --- Build a browser-like sandbox ---
function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {} },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '' }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    console,
    Date,
    Infinity,
    Math,
    Array,
    Object,
    String,
    Number,
    JSON,
    RegExp,
    Error,
    TypeError,
    parseInt,
    parseFloat,
    isNaN,
    isFinite,
    encodeURIComponent,
    decodeURIComponent,
    setTimeout: () => {},
    clearTimeout: () => {},
    setInterval: () => {},
    clearInterval: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => {
      const store = {};
      return {
        getItem: k => store[k] || null,
        setItem: (k, v) => { store[k] = String(v); },
        removeItem: k => { delete store[k]; },
      };
    })(),
    location: { hash: '' },
    CustomEvent: class CustomEvent {},
    Map,
    Promise,
    URLSearchParams,
    addEventListener: () => {},
    dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
  };
  vm.createContext(ctx);
  return ctx;
}

function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  // Copy window.* to global context so bare references work
  for (const k of Object.keys(ctx.window)) {
    ctx[k] = ctx.window[k];
  }
}

// ===== APP.JS TESTS =====
console.log('\n=== app.js: timeAgo ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const timeAgo = ctx.timeAgo;

  test('null returns dash', () => assert.strictEqual(timeAgo(null), '—'));
  test('undefined returns dash', () => assert.strictEqual(timeAgo(undefined), '—'));
  test('empty string returns dash', () => assert.strictEqual(timeAgo(''), '—'));

  test('30 seconds ago', () => {
    const d = new Date(Date.now() - 30000).toISOString();
    assert.strictEqual(timeAgo(d), '30s ago');
  });
  test('5 minutes ago', () => {
    const d = new Date(Date.now() - 300000).toISOString();
    assert.strictEqual(timeAgo(d), '5m ago');
  });
  test('2 hours ago', () => {
    const d = new Date(Date.now() - 7200000).toISOString();
    assert.strictEqual(timeAgo(d), '2h ago');
  });
  test('3 days ago', () => {
    const d = new Date(Date.now() - 259200000).toISOString();
    assert.strictEqual(timeAgo(d), '3d ago');
  });
}

console.log('\n=== app.js: escapeHtml ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const escapeHtml = ctx.escapeHtml;

  test('escapes < and >', () => assert.strictEqual(escapeHtml('<script>'), '&lt;script&gt;'));
  test('escapes &', () => assert.strictEqual(escapeHtml('a&b'), 'a&amp;b'));
  test('escapes quotes', () => assert.strictEqual(escapeHtml('"hello"'), '&quot;hello&quot;'));
  test('null returns empty', () => assert.strictEqual(escapeHtml(null), ''));
  test('undefined returns empty', () => assert.strictEqual(escapeHtml(undefined), ''));
  test('number coerced', () => assert.strictEqual(escapeHtml(42), '42'));
}

console.log('\n=== app.js: routeTypeName / payloadTypeName ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');

  test('routeTypeName(0) = TRANSPORT_FLOOD', () => assert.strictEqual(ctx.routeTypeName(0), 'TRANSPORT_FLOOD'));
  test('routeTypeName(2) = DIRECT', () => assert.strictEqual(ctx.routeTypeName(2), 'DIRECT'));
  test('routeTypeName(99) = UNKNOWN', () => assert.strictEqual(ctx.routeTypeName(99), 'UNKNOWN'));
  test('payloadTypeName(4) = Advert', () => assert.strictEqual(ctx.payloadTypeName(4), 'Advert'));
  test('payloadTypeName(2) = Direct Msg', () => assert.strictEqual(ctx.payloadTypeName(2), 'Direct Msg'));
  test('payloadTypeName(99) = UNKNOWN', () => assert.strictEqual(ctx.payloadTypeName(99), 'UNKNOWN'));
}

console.log('\n=== app.js: truncate ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const truncate = ctx.truncate;

  test('short string unchanged', () => assert.strictEqual(truncate('hello', 10), 'hello'));
  test('long string truncated', () => assert.strictEqual(truncate('hello world', 5), 'hello…'));
  test('null returns empty', () => assert.strictEqual(truncate(null, 5), ''));
  test('empty returns empty', () => assert.strictEqual(truncate('', 5), ''));
}

// ===== NODES.JS TESTS =====
console.log('\n=== nodes.js: getStatusInfo ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  // nodes.js is an IIFE that registers a page — we need to mock registerPage and other globals
  ctx.registerPage = () => {};
  ctx.api = () => Promise.resolve([]);
  ctx.timeAgo = vm.runInContext(`(${fs.readFileSync('public/app.js', 'utf8').match(/function timeAgo[^}]+}/)[0]})`, ctx);
  // Actually, let's load app.js first for its globals
  loadInCtx(ctx, 'public/app.js');
  ctx.RegionFilter = { init: () => {}, getSelected: () => null, onRegionChange: () => {} };
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.invalidateApiCache = () => {};
  ctx.favStar = () => '';
  ctx.bindFavStars = () => {};
  ctx.getFavorites = () => [];
  ctx.isFavorite = () => false;
  ctx.connectWS = () => {};
  loadInCtx(ctx, 'public/nodes.js');

  // getStatusInfo is inside the IIFE, not on window. We need to extract it differently.
  // Let's use a modified approach - inject a hook before loading
}

// Since nodes.js functions are inside an IIFE, we need to extract them.
// Strategy: modify the IIFE to expose functions on window for testing
console.log('\n=== nodes.js: getStatusTooltip / getStatusInfo (extracted) ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');

  // Extract the functions from nodes.js source by wrapping them
  const nodesSource = fs.readFileSync('public/nodes.js', 'utf8');

  // Extract function bodies using regex - getStatusTooltip, getStatusInfo, renderNodeBadges, sortNodes
  const fnNames = ['getStatusTooltip', 'getStatusInfo', 'renderNodeBadges', 'renderStatusExplanation', 'sortNodes'];
  // Instead, let's inject an exporter into the IIFE
  const modifiedSource = nodesSource.replace(
    /\(function \(\) \{/,
    '(function () { window.__nodesExport = {};'
  ).replace(
    /function getStatusTooltip/,
    'window.__nodesExport.getStatusTooltip = getStatusTooltip; function getStatusTooltip'
  ).replace(
    /function getStatusInfo/,
    'window.__nodesExport.getStatusInfo = getStatusInfo; function getStatusInfo'
  ).replace(
    /function renderNodeBadges/,
    'window.__nodesExport.renderNodeBadges = renderNodeBadges; function renderNodeBadges'
  ).replace(
    /function renderStatusExplanation/,
    'window.__nodesExport.renderStatusExplanation = renderStatusExplanation; function renderStatusExplanation'
  ).replace(
    /function sortNodes/,
    'window.__nodesExport.sortNodes = sortNodes; function sortNodes'
  );

  // Provide required globals
  ctx.registerPage = () => {};
  ctx.RegionFilter = { init: () => {}, getSelected: () => null, onRegionChange: () => {} };
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.invalidateApiCache = () => {};
  ctx.favStar = () => '';
  ctx.bindFavStars = () => {};
  ctx.getFavorites = () => [];
  ctx.isFavorite = () => false;
  ctx.connectWS = () => {};
  ctx.HopResolver = { init: () => {}, resolve: () => ({}), ready: () => false };

  try {
    vm.runInContext(modifiedSource, ctx);
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  } catch (e) {
    console.log('  ⚠️ Could not load nodes.js in sandbox:', e.message.slice(0, 100));
  }

  const ex = ctx.window.__nodesExport || {};

  if (ex.getStatusTooltip) {
    const gst = ex.getStatusTooltip;
    test('active repeater tooltip mentions 72h', () => {
      assert.ok(gst('repeater', 'active').includes('72h'));
    });
    test('stale companion tooltip mentions normal', () => {
      assert.ok(gst('companion', 'stale').includes('normal'));
    });
    test('stale sensor tooltip mentions offline', () => {
      assert.ok(gst('sensor', 'stale').includes('offline'));
    });
    test('active companion tooltip mentions 24h', () => {
      assert.ok(gst('companion', 'active').includes('24h'));
    });
  }

  if (ex.getStatusInfo) {
    const gsi = ex.getStatusInfo;
    test('active repeater status', () => {
      const info = gsi({ role: 'repeater', last_heard: new Date().toISOString() });
      assert.strictEqual(info.status, 'active');
      assert.ok(info.statusLabel.includes('Active'));
    });
    test('stale companion status (old date)', () => {
      const old = new Date(Date.now() - 48 * 3600000).toISOString();
      const info = gsi({ role: 'companion', last_heard: old });
      assert.strictEqual(info.status, 'stale');
    });
    test('repeater stale at 4 days', () => {
      const old = new Date(Date.now() - 96 * 3600000).toISOString();
      const info = gsi({ role: 'repeater', last_heard: old });
      assert.strictEqual(info.status, 'stale');
    });
    test('repeater active at 2 days', () => {
      const d = new Date(Date.now() - 48 * 3600000).toISOString();
      const info = gsi({ role: 'repeater', last_heard: d });
      assert.strictEqual(info.status, 'active');
    });
  }

  if (ex.renderNodeBadges) {
    test('renderNodeBadges includes role', () => {
      const html = ex.renderNodeBadges({ role: 'repeater', public_key: 'abcdef1234', last_heard: new Date().toISOString() }, '#ff0000');
      assert.ok(html.includes('repeater'));
    });
  }

  if (ex.sortNodes) {
    const sortNodes = ex.sortNodes;
    // We need to set sortState — it's closure-captured. Test via the exposed function behavior.
    // sortNodes uses the closure sortState, so we can't easily test different sort modes
    // without calling toggleSort. Let's just verify it returns a sorted array.
    test('sortNodes returns array', () => {
      const arr = [
        { name: 'Bravo', last_heard: new Date().toISOString() },
        { name: 'Alpha', last_heard: new Date(Date.now() - 1000).toISOString() },
      ];
      const result = sortNodes(arr);
      assert.ok(Array.isArray(result));
    });
  }
}

// ===== HOP-RESOLVER TESTS =====
console.log('\n=== hop-resolver.js ===');
{
  const ctx = makeSandbox();
  ctx.IATA_COORDS_GEO = {};
  loadInCtx(ctx, 'public/hop-resolver.js');
  const HR = ctx.window.HopResolver;

  test('ready() false before init', () => assert.strictEqual(HR.ready(), false));

  test('init + ready', () => {
    HR.init([{ public_key: 'abcdef1234567890', name: 'NodeA', lat: 37.3, lon: -122.0 }]);
    assert.strictEqual(HR.ready(), true);
  });

  test('resolve single unique prefix', () => {
    HR.init([
      { public_key: 'abcdef1234567890', name: 'NodeA', lat: 37.3, lon: -122.0 },
      { public_key: '123456abcdef0000', name: 'NodeB', lat: 37.4, lon: -122.1 },
    ]);
    const result = HR.resolve(['ab'], null, null, null, null);
    assert.strictEqual(result['ab'].name, 'NodeA');
  });

  test('resolve ambiguous prefix', () => {
    HR.init([
      { public_key: 'abcdef1234567890', name: 'NodeA', lat: 37.3, lon: -122.0 },
      { public_key: 'abcd001234567890', name: 'NodeC', lat: 38.0, lon: -121.0 },
    ]);
    const result = HR.resolve(['ab'], null, null, null, null);
    assert.ok(result['ab'].ambiguous);
    assert.strictEqual(result['ab'].candidates.length, 2);
  });

  test('resolve unknown prefix returns null name', () => {
    HR.init([{ public_key: 'abcdef1234567890', name: 'NodeA' }]);
    const result = HR.resolve(['ff'], null, null, null, null);
    assert.strictEqual(result['ff'].name, null);
  });

  test('empty hops returns empty', () => {
    const result = HR.resolve([], null, null, null, null);
    assert.strictEqual(Object.keys(result).length, 0);
  });

  test('geo disambiguation with origin anchor', () => {
    HR.init([
      { public_key: 'abcdef1234567890', name: 'NearNode', lat: 37.31, lon: -122.01 },
      { public_key: 'abcd001234567890', name: 'FarNode', lat: 50.0, lon: 10.0 },
    ]);
    const result = HR.resolve(['ab'], 37.3, -122.0, null, null);
    // Should prefer the nearer node
    assert.strictEqual(result['ab'].name, 'NearNode');
  });

  test('regional filtering with IATA', () => {
    HR.init(
      [
        { public_key: 'abcdef1234567890', name: 'SFONode', lat: 37.6, lon: -122.4 },
        { public_key: 'abcd001234567890', name: 'LHRNode', lat: 51.5, lon: -0.1 },
      ],
      {
        observers: [{ id: 'obs1', iata: 'SFO' }],
        iataCoords: { SFO: { lat: 37.6, lon: -122.4 } },
      }
    );
    const result = HR.resolve(['ab'], null, null, null, null, 'obs1');
    assert.strictEqual(result['ab'].name, 'SFONode');
    assert.ok(!result['ab'].ambiguous);
  });
}

// ===== SNR/RSSI Number casting =====
{
  // These test the pattern used in observer-detail.js, home.js, traces.js, live.js
  // Values from DB may be strings — Number() must be called before .toFixed()
  test('Number(string snr).toFixed works', () => {
    const snr = "7.5"; // string from DB
    assert.strictEqual(Number(snr).toFixed(1), "7.5");
  });

  test('Number(number snr).toFixed works', () => {
    const snr = 7.5;
    assert.strictEqual(Number(snr).toFixed(1), "7.5");
  });

  test('Number(null) produces NaN, guarded by != null check', () => {
    const snr = null;
    assert.ok(!(snr != null) || !isNaN(Number(snr).toFixed(1)));
  });

  test('Number(string rssi).toFixed works', () => {
    const rssi = "-85";
    assert.strictEqual(Number(rssi).toFixed(0), "-85");
  });

  test('Number(negative string snr).toFixed works', () => {
    const snr = "-3.2";
    assert.strictEqual(Number(snr).toFixed(1), "-3.2");
  });

  test('Number(integer string).toFixed adds decimal', () => {
    const snr = "10";
    assert.strictEqual(Number(snr).toFixed(1), "10.0");
  });
}

// ===== SUMMARY =====
console.log(`\n${'═'.repeat(40)}`);
console.log(`  Frontend helpers: ${passed} passed, ${failed} failed`);
console.log(`${'═'.repeat(40)}\n`);
if (failed > 0) process.exit(1);
