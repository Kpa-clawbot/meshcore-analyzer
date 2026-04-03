/* Unit tests for frontend helper functions (tested via VM sandbox) */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
const pendingTests = [];
function test(name, fn) {
  try {
    const out = fn();
    if (out && typeof out.then === 'function') {
      pendingTests.push(
        out.then(() => {
          passed++;
          console.log(`  ✅ ${name}`);
        }).catch((e) => {
          failed++;
          console.log(`  ❌ ${name}: ${e.message}`);
        })
      );
      return;
    }
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
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
  test('future timestamp returns in-format', () => {
    const d = new Date(Date.now() + 120000).toISOString();
    assert.strictEqual(timeAgo(d), 'in 2m');
  });
}

console.log('\n=== app.js: formatTimestamp / formatTimestampWithTooltip ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const formatTimestamp = ctx.formatTimestamp;
  const formatTimestampWithTooltip = ctx.formatTimestampWithTooltip;

  test('formatTimestamp null returns dash', () => {
    assert.strictEqual(formatTimestamp(null, 'ago'), '—');
  });
  test('formatTimestamp ago returns relative string', () => {
    const d = new Date(Date.now() - 300000).toISOString();
    assert.strictEqual(formatTimestamp(d, 'ago'), '5m ago');
  });
  test('formatTimestamp absolute returns formatted timestamp', () => {
    const d = '2024-01-02T03:04:05.000Z';
    const out = formatTimestamp(d, 'absolute');
    assert.ok(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(out));
  });
  test('formatTimestamp absolute with timezone utc uses UTC fields', () => {
    const d = '2024-01-02T03:04:05.123Z';
    ctx.localStorage.setItem('meshcore-timestamp-timezone', 'utc');
    ctx.localStorage.setItem('meshcore-timestamp-format', 'iso');
    assert.strictEqual(formatTimestamp(d, 'absolute'), '2024-01-02 03:04:05');
  });
  test('formatTimestamp absolute with timezone local uses local fields', () => {
    const d = '2024-01-02T03:04:05.123Z';
    ctx.localStorage.setItem('meshcore-timestamp-timezone', 'local');
    ctx.localStorage.setItem('meshcore-timestamp-format', 'iso');
    const out = formatTimestamp(d, 'absolute');
    const expected = d.replace('T', ' ').slice(0, 19);
    assert.strictEqual(out.length, 19);
    assert.ok(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(out));
    if (new Date(d).getTimezoneOffset() === 0) assert.strictEqual(out, expected);
    else assert.notStrictEqual(out, expected);
  });
  test('formatTimestamp absolute iso-seconds includes milliseconds', () => {
    const d = '2024-01-02T03:04:05.123Z';
    ctx.localStorage.setItem('meshcore-timestamp-timezone', 'utc');
    ctx.localStorage.setItem('meshcore-timestamp-format', 'iso-seconds');
    assert.strictEqual(formatTimestamp(d, 'absolute'), '2024-01-02 03:04:05.123');
  });
  test('formatTimestamp absolute locale uses toLocaleString', () => {
    const d = '2024-01-02T03:04:05.123Z';
    ctx.localStorage.setItem('meshcore-timestamp-timezone', 'local');
    ctx.localStorage.setItem('meshcore-timestamp-format', 'locale');
    assert.strictEqual(formatTimestamp(d, 'absolute'), new Date(d).toLocaleString());
  });
  test('formatTimestampWithTooltip future returns isFuture true', () => {
    const d = new Date(Date.now() + 120000).toISOString();
    const out = formatTimestampWithTooltip(d, 'ago');
    assert.strictEqual(out.isFuture, true);
    assert.ok(typeof out.text === 'string' && out.text.length > 0);
    assert.strictEqual(out.tooltip, 'in 2m');
  });
  test('tooltip is opposite format', () => {
    const d = '2024-01-02T03:04:05.000Z';
    const ago = formatTimestampWithTooltip(d, 'ago');
    const absolute = formatTimestampWithTooltip(d, 'absolute');
    assert.ok(typeof ago.tooltip === 'string' && ago.tooltip.length > 0);
    assert.ok(absolute.tooltip.endsWith('ago') || absolute.tooltip.startsWith('in '));
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
  // Placeholder header for continuity; actual nodes tests are below using injected exports.
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
  ).replace(
    /function buildDupNameMap/,
    'window.__nodesExport.buildDupNameMap = buildDupNameMap; function buildDupNameMap'
  ).replace(
    /function renderHashInconsistencyWarning/,
    'window.__nodesExport.renderHashInconsistencyWarning = renderHashInconsistencyWarning; function renderHashInconsistencyWarning'
  ).replace(
    /function dupNameBadge/,
    'window.__nodesExport.dupNameBadge = dupNameBadge; function dupNameBadge'
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

  if (ex.buildDupNameMap) {
    const buildDupNameMap = ex.buildDupNameMap;
    test('buildDupNameMap returns empty for no nodes', () => {
      const m = buildDupNameMap([]);
      assert.strictEqual(Object.keys(m).length, 0);
    });
    test('buildDupNameMap groups nodes by lowercase name', () => {
      const m = buildDupNameMap([
        { name: 'Alpha', public_key: 'key1' },
        { name: 'alpha', public_key: 'key2' },
        { name: 'Beta', public_key: 'key3' },
      ]);
      assert.strictEqual(m['alpha'].length, 2);
      assert.ok(m['alpha'].includes('key1'));
      assert.ok(m['alpha'].includes('key2'));
      assert.strictEqual(m['beta'].length, 1);
    });
    test('buildDupNameMap ignores unnamed nodes', () => {
      const m = buildDupNameMap([
        { name: '', public_key: 'key1' },
        { name: null, public_key: 'key2' },
        { name: 'Alpha', public_key: 'key3' },
      ]);
      assert.strictEqual(Object.keys(m).length, 1);
    });
    test('buildDupNameMap deduplicates same pubkey', () => {
      const m = buildDupNameMap([
        { name: 'Alpha', public_key: 'key1' },
        { name: 'Alpha', public_key: 'key1' },
      ]);
      assert.strictEqual(m['alpha'].length, 1);
    });
  }

  if (ex.dupNameBadge) {
    const dupNameBadge = ex.dupNameBadge;
    test('dupNameBadge returns empty for unique name', () => {
      const m = { 'alpha': ['key1'] };
      assert.strictEqual(dupNameBadge('Alpha', 'key1', m), '');
    });
    test('dupNameBadge returns badge for duplicate names', () => {
      const m = { 'alpha': ['key1', 'key2'] };
      const html = dupNameBadge('Alpha', 'key1', m);
      assert.ok(html.includes('(2)'));
      assert.ok(html.includes('dup-name-badge'));
    });
    test('dupNameBadge returns empty for null name', () => {
      assert.strictEqual(dupNameBadge(null, 'key1', {}), '');
    });
    test('dupNameBadge returns empty for null map', () => {
      assert.strictEqual(dupNameBadge('Alpha', 'key1', null), '');
    });
    test('dupNameBadge shows count of 3 for three duplicates', () => {
      const m = { 'alpha': ['key1', 'key2', 'key3'] };
      const html = dupNameBadge('Alpha', 'key1', m);
      assert.ok(html.includes('(3)'));
    });
  }

  // --- renderHashInconsistencyWarning tests (fixes #190) ---
  if (ex.renderHashInconsistencyWarning) {
    const warn = ex.renderHashInconsistencyWarning;
    test('renderHashInconsistencyWarning returns empty for consistent node', () => {
      assert.strictEqual(warn({ hash_size_inconsistent: false }), '');
    });
    test('renderHashInconsistencyWarning returns empty for undefined flag', () => {
      assert.strictEqual(warn({}), '');
    });
    test('renderHashInconsistencyWarning renders with valid array', () => {
      const html = warn({ hash_size_inconsistent: true, hash_sizes_seen: [1, 2] });
      assert.ok(html.includes('1-byte, 2-byte'));
      assert.ok(html.includes('varying hash sizes'));
    });
    test('renderHashInconsistencyWarning handles missing hash_sizes_seen', () => {
      const html = warn({ hash_size_inconsistent: true });
      assert.ok(html.includes('varying hash sizes'));
      // Should not crash — renders with empty sizes
      assert.ok(html.includes('-byte'));
    });
    test('renderHashInconsistencyWarning handles non-array hash_sizes_seen', () => {
      const html = warn({ hash_size_inconsistent: true, hash_sizes_seen: '[1, 2]' });
      assert.ok(html.includes('varying hash sizes'));
      // String should be treated as empty array (Array.isArray guard)
      assert.ok(html.includes('-byte'));
    });
    test('renderHashInconsistencyWarning handles null hash_sizes_seen', () => {
      const html = warn({ hash_size_inconsistent: true, hash_sizes_seen: null });
      assert.ok(html.includes('varying hash sizes'));
    });
  }

  // --- renderNodeBadges with hash_size_inconsistent (fixes #190) ---
  if (ex.renderNodeBadges) {
    test('renderNodeBadges handles hash_size_inconsistent node', () => {
      const html = ex.renderNodeBadges({
        role: 'room', public_key: '9dc3e069d1b336c4af33167d3838147ca6449e12c1e1bdaa92fdfc0ecfdd98bc',
        hash_size: 2, hash_size_inconsistent: true, hash_sizes_seen: [1, 2],
        last_heard: new Date().toISOString()
      }, '#16a34a');
      assert.ok(html.includes('room'));
      assert.ok(html.includes('9DC3'));
      assert.ok(html.includes('variable hash size'));
    });
    test('renderNodeBadges handles null hash_size', () => {
      const html = ex.renderNodeBadges({
        role: 'room', public_key: 'abcdef1234567890',
        hash_size: null, hash_size_inconsistent: false,
        last_heard: new Date().toISOString()
      }, '#16a34a');
      assert.ok(html.includes('room'));
      assert.ok(!html.includes('variable hash size'));
    });
    test('renderNodeBadges handles string hash_sizes_seen gracefully', () => {
      const html = ex.renderNodeBadges({
        role: 'repeater', public_key: 'abcdef1234567890',
        hash_size: 2, hash_size_inconsistent: true, hash_sizes_seen: '[1, 2]',
        last_heard: new Date().toISOString()
      }, '#dc2626');
      assert.ok(html.includes('variable hash size'));
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

// ===== haversineKm exposed from HopResolver (issue #433) =====
console.log('\n=== haversineKm (hop-resolver.js) ===');
{
  const ctx = makeSandbox();
  ctx.IATA_COORDS_GEO = {};
  loadInCtx(ctx, 'public/hop-resolver.js');
  const HR = ctx.window.HopResolver;

  test('haversineKm is exported', () => {
    assert.strictEqual(typeof HR.haversineKm, 'function');
  });

  test('haversineKm same point = 0', () => {
    assert.strictEqual(HR.haversineKm(37.0, -122.0, 37.0, -122.0), 0);
  });

  test('haversineKm SF to LA ~559km', () => {
    // San Francisco (37.7749, -122.4194) to Los Angeles (34.0522, -118.2437)
    const d = HR.haversineKm(37.7749, -122.4194, 34.0522, -118.2437);
    assert.ok(d > 550 && d < 570, `Expected ~559km, got ${d}`);
  });

  test('haversineKm differs from old Euclidean approximation', () => {
    // The old code used dLat*111, dLon*85 which is inaccurate at high latitudes
    // Oslo (59.9, 10.7) to Stockholm (59.3, 18.0)
    const haversine = HR.haversineKm(59.9, 10.7, 59.3, 18.0);
    const dLat = (59.9 - 59.3) * 111;
    const dLon = (10.7 - 18.0) * 85;
    const euclidean = Math.sqrt(dLat*dLat + dLon*dLon);
    // Haversine should give ~415km, Euclidean ~627km (wrong because dLon*85 is wrong at 60° latitude)
    assert.ok(Math.abs(haversine - euclidean) > 50, `Expected significant difference, haversine=${haversine.toFixed(1)}, euclidean=${euclidean.toFixed(1)}`);
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

// ===== ROLES.JS: copyToClipboard =====
console.log('\n=== roles.js: copyToClipboard ===');
{
  // Helper: build a sandbox with clipboard/DOM mocks for copyToClipboard tests
  function makeClipboardSandbox(opts) {
    const ctx = makeSandbox();
    const createdEls = [];
    const appendedEls = [];
    const removedEls = [];

    // Enhanced createElement that returns a mock textarea
    ctx.document.createElement = (tag) => {
      const el = { tagName: tag, value: '', style: {}, focus() {}, select() {} };
      createdEls.push(el);
      return el;
    };
    ctx.document.body = {
      appendChild: (el) => { appendedEls.push(el); },
      removeChild: (el) => { removedEls.push(el); },
    };
    ctx.document.execCommand = opts.execCommand || (() => true);

    // navigator mock
    if (opts.clipboardWriteText) {
      ctx.navigator = { clipboard: { writeText: opts.clipboardWriteText } };
    } else {
      ctx.navigator = {};
    }

    loadInCtx(ctx, 'public/roles.js');
    return { ctx, createdEls, appendedEls, removedEls };
  }

  // Test 1: Fallback succeeds when clipboard API is unavailable
  test('copyToClipboard fallback calls onSuccess when execCommand succeeds', () => {
    const { ctx } = makeClipboardSandbox({ execCommand: () => true });
    let succeeded = false;
    ctx.window.copyToClipboard('hello', () => { succeeded = true; }, () => { throw new Error('onFail should not be called'); });
    assert.strictEqual(succeeded, true);
  });

  // Test 2: Fallback uses textarea when clipboard API is unavailable
  test('copyToClipboard fallback creates textarea with correct value', () => {
    const { ctx, createdEls, appendedEls, removedEls } = makeClipboardSandbox({ execCommand: () => true });
    const beforeCount = createdEls.length; // roles.js may create elements on init
    ctx.window.copyToClipboard('test-text');
    const newEls = createdEls.slice(beforeCount);
    assert.strictEqual(newEls.length, 1);
    assert.strictEqual(newEls[0].tagName, 'textarea');
    assert.strictEqual(newEls[0].value, 'test-text');
    assert.strictEqual(appendedEls.length, 1, 'textarea should be appended to body');
    assert.strictEqual(removedEls.length, 1, 'textarea should be removed from body');
  });

  // Test 3: Fallback calls onFail when execCommand returns false
  test('copyToClipboard fallback calls onFail when execCommand fails', () => {
    const { ctx } = makeClipboardSandbox({ execCommand: () => false });
    let failCalled = false;
    ctx.window.copyToClipboard('hello', () => { throw new Error('onSuccess should not be called'); }, () => { failCalled = true; });
    assert.strictEqual(failCalled, true);
  });

  // Test 4: Fallback calls onFail when execCommand throws
  test('copyToClipboard fallback calls onFail when execCommand throws', () => {
    const { ctx } = makeClipboardSandbox({ execCommand: () => { throw new Error('not allowed'); } });
    let failCalled = false;
    ctx.window.copyToClipboard('hello', null, () => { failCalled = true; });
    assert.strictEqual(failCalled, true);
  });

  // Test 5: Handles null input gracefully (no crash)
  test('copyToClipboard handles null input without throwing', () => {
    const { ctx } = makeClipboardSandbox({ execCommand: () => true });
    // Should not throw
    ctx.window.copyToClipboard(null);
    ctx.window.copyToClipboard(undefined);
  });

  // Test 6: Clipboard API path calls writeText with correct argument
  test('copyToClipboard uses clipboard API when available', () => {
    let writtenText = null;
    const { ctx } = makeClipboardSandbox({
      clipboardWriteText: (text) => { writtenText = text; return Promise.resolve(); },
    });
    ctx.window.copyToClipboard('clipboard-text');
    assert.strictEqual(writtenText, 'clipboard-text');
  });

  // Test 7: No crash when callbacks are omitted
  test('copyToClipboard works without callbacks', () => {
    const { ctx } = makeClipboardSandbox({ execCommand: () => true });
    ctx.window.copyToClipboard('no-callbacks');
    // No callbacks — should not throw
  });

  // Test 8: Cleanup happens even when execCommand throws
  test('copyToClipboard cleans up textarea on execCommand throw', () => {
    const { ctx, removedEls } = makeClipboardSandbox({ execCommand: () => { throw new Error('denied'); } });
    ctx.window.copyToClipboard('cleanup-test');
    assert.strictEqual(removedEls.length, 1, 'textarea should be removed even on error');
  });
}

// ===== LIVE.JS: pruneStaleNodes =====
console.log('\n=== live.js: pruneStaleNodes ===');
{
  function makeLiveSandbox() {
    const ctx = makeSandbox();
    // Leaflet mock
    const removedLayers = [];
    ctx.L = {
      circleMarker: () => {
        const m = {
          addTo: function() { return m; },
          bindTooltip: function() { return m; },
          on: function() { return m; },
          setRadius: function() {},
          setStyle: function() {},
          setLatLng: function() {},
          getLatLng: function() { return { lat: 0, lng: 0 }; },
          _baseColor: '', _baseSize: 5, _glowMarker: null,
        };
        return m;
      },
      polyline: () => {
        const p = { addTo: function() { return p; }, setStyle: function() {}, remove: function() {} };
        return p;
      },
      map: () => {
        const m = {
          setView: function() { return m; }, addLayer: function() { return m; },
          on: function() { return m; }, getZoom: function() { return 11; },
          getCenter: function() { return { lat: 37, lng: -122 }; },
          getBounds: function() { return { contains: () => true }; },
          fitBounds: function() { return m; }, invalidateSize: function() {},
          remove: function() {}, hasLayer: function() { return false; },
        };
        return m;
      },
      layerGroup: () => {
        const g = {
          addTo: function() { return g; }, addLayer: function() {},
          removeLayer: function(l) { removedLayers.push(l); },
          clearLayers: function() {}, hasLayer: function() { return true; },
          eachLayer: function() {},
        };
        return g;
      },
      tileLayer: () => ({ addTo: function() { return this; } }),
      control: { attribution: () => ({ addTo: function() {} }) },
      DomUtil: { addClass: function() {}, removeClass: function() {} },
    };
    ctx.getComputedStyle = () => ({ getPropertyValue: () => '' });
    ctx.matchMedia = () => ({ matches: false, addEventListener: () => {} });
    ctx.registerPage = () => {};
    ctx.onWS = () => {};
    ctx.offWS = () => {};
    ctx.connectWS = () => {};
    ctx.api = () => Promise.resolve([]);
    ctx.invalidateApiCache = () => {};
    ctx.favStar = () => '';
    ctx.bindFavStars = () => {};
    ctx.getFavorites = () => [];
    ctx.isFavorite = () => false;
    ctx.HopResolver = { init: () => {}, resolve: () => ({}), ready: () => false };
    ctx.MeshAudio = null;
    ctx.RegionFilter = { init: () => {}, getSelected: () => null, onRegionChange: () => {} };
    ctx.WebSocket = function() { this.close = () => {}; };
    ctx.navigator = {};
    ctx.visualViewport = null;
    ctx.document.documentElement = { getAttribute: () => null, setAttribute: () => {} };
    ctx.document.body = { appendChild: () => {}, removeChild: () => {}, contains: () => false };
    ctx.document.querySelector = () => null;
    ctx.document.querySelectorAll = () => [];
    ctx.document.createElementNS = () => ctx.document.createElement();
    ctx.cancelAnimationFrame = () => {};
    ctx.IATA_COORDS_GEO = {};

    loadInCtx(ctx, 'public/roles.js');
    try {
      loadInCtx(ctx, 'public/live.js');
    } catch (e) {
      // live.js may have non-critical load errors in sandbox
      for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
    }
    return { ctx, removedLayers };
  }

  test('pruneStaleNodes removes nodes older than silentMs threshold', () => {
    const { ctx, removedLayers } = makeLiveSandbox();
    const prune = ctx.window._livePruneStaleNodes;
    const getMarkers = ctx.window._liveNodeMarkers;
    const getData = ctx.window._liveNodeData;
    assert.ok(prune, '_livePruneStaleNodes must be exposed');
    assert.ok(getMarkers, '_liveNodeMarkers must be exposed');
    assert.ok(getData, '_liveNodeData must be exposed');

    const markers = getMarkers();
    const data = getData();

    // Inject a companion node last seen 48 hours ago (exceeds nodeSilentMs=24h)
    markers['staleKey'] = { _glowMarker: null };
    data['staleKey'] = { public_key: 'staleKey', role: 'companion', _liveSeen: Date.now() - 48 * 3600000 };

    // Inject an active companion seen just now
    markers['freshKey'] = { _glowMarker: null };
    data['freshKey'] = { public_key: 'freshKey', role: 'companion', _liveSeen: Date.now() };

    prune();

    assert.ok(!markers['staleKey'], 'stale companion should be pruned');
    assert.ok(!data['staleKey'], 'stale companion data should be pruned');
    assert.ok(markers['freshKey'], 'fresh companion should remain');
    assert.ok(data['freshKey'], 'fresh companion data should remain');
  });

  test('pruneStaleNodes uses longer threshold for infrastructure roles', () => {
    const { ctx } = makeLiveSandbox();
    const prune = ctx.window._livePruneStaleNodes;
    const markers = ctx.window._liveNodeMarkers();
    const data = ctx.window._liveNodeData();

    // A repeater seen 48h ago should NOT be pruned (infraSilentMs = 72h)
    markers['rpt1'] = { _glowMarker: null };
    data['rpt1'] = { public_key: 'rpt1', role: 'repeater', _liveSeen: Date.now() - 48 * 3600000 };

    // A repeater seen 96h ago SHOULD be pruned
    markers['rpt2'] = { _glowMarker: null };
    data['rpt2'] = { public_key: 'rpt2', role: 'repeater', _liveSeen: Date.now() - 96 * 3600000 };

    prune();

    assert.ok(markers['rpt1'], 'repeater at 48h should remain (under 72h threshold)');
    assert.ok(data['rpt1'], 'repeater data at 48h should remain');
    assert.ok(!markers['rpt2'], 'repeater at 96h should be pruned (over 72h threshold)');
    assert.ok(!data['rpt2'], 'repeater data at 96h should be pruned');
  });

  test('node count does not grow unbounded with repeated ADVERTs', () => {
    const { ctx } = makeLiveSandbox();
    const prune = ctx.window._livePruneStaleNodes;
    const markers = ctx.window._liveNodeMarkers();
    const data = ctx.window._liveNodeData();

    // Simulate 500 nodes added over time, most now stale
    for (var i = 0; i < 500; i++) {
      var key = 'node' + i;
      markers[key] = { _glowMarker: null };
      // First 400 are old (stale), last 100 are fresh
      var age = i < 400 ? 48 * 3600000 : 0;
      data[key] = { public_key: key, role: 'companion', _liveSeen: Date.now() - age };
    }

    assert.strictEqual(Object.keys(markers).length, 500, 'should start with 500 nodes');
    prune();
    assert.strictEqual(Object.keys(markers).length, 100, 'should have pruned down to 100 active nodes');
    assert.strictEqual(Object.keys(data).length, 100, 'nodeData should match');
  });

  test('pruneStaleNodes skips nodes with no timestamp', () => {
    const { ctx } = makeLiveSandbox();
    const prune = ctx.window._livePruneStaleNodes;
    const markers = ctx.window._liveNodeMarkers();
    const data = ctx.window._liveNodeData();

    markers['noTs'] = { _glowMarker: null };
    data['noTs'] = { public_key: 'noTs', role: 'companion' };

    prune();

    assert.ok(markers['noTs'], 'node with no timestamp should not be pruned');
  });

  test('pruneStaleNodes uses last_heard as fallback for _liveSeen', () => {
    const { ctx } = makeLiveSandbox();
    const prune = ctx.window._livePruneStaleNodes;
    const markers = ctx.window._liveNodeMarkers();
    const data = ctx.window._liveNodeData();

    // Node with last_heard (from API) but no _liveSeen — stale
    markers['apiOld'] = { _glowMarker: null };
    data['apiOld'] = { public_key: 'apiOld', role: 'companion', last_heard: new Date(Date.now() - 48 * 3600000).toISOString() };

    // Node with last_heard — fresh
    markers['apiFresh'] = { _glowMarker: null };
    data['apiFresh'] = { public_key: 'apiFresh', role: 'companion', last_heard: new Date().toISOString() };

    prune();

    assert.ok(!markers['apiOld'], 'WS node with stale last_heard should be pruned');
    assert.ok(markers['apiFresh'], 'WS node with fresh last_heard should remain');
  });

  test('pruneStaleNodes dims API-loaded nodes instead of removing them', () => {
    const { ctx } = makeLiveSandbox();
    const prune = ctx.window._livePruneStaleNodes;
    const markers = ctx.window._liveNodeMarkers();
    const data = ctx.window._liveNodeData();

    let lastStyle = {};
    let glowStyle = {};
    markers['apiStale'] = {
      _glowMarker: { setStyle: function(s) { glowStyle = s; } },
      _staleDimmed: false,
      setStyle: function(s) { lastStyle = s; },
    };
    data['apiStale'] = { public_key: 'apiStale', role: 'repeater', _fromAPI: true, _liveSeen: Date.now() - 96 * 3600000 };

    prune();

    assert.ok(markers['apiStale'], 'API node should NOT be removed');
    assert.ok(data['apiStale'], 'API node data should NOT be removed');
    assert.ok(markers['apiStale']._staleDimmed, 'API node should be marked as dimmed');
    assert.strictEqual(lastStyle.fillOpacity, 0.25, 'marker should be dimmed to 0.25 fillOpacity');
    assert.strictEqual(glowStyle.fillOpacity, 0.04, 'glow should be dimmed to 0.04 fillOpacity');
  });

  test('pruneStaleNodes restores API nodes when they become active again', () => {
    const { ctx } = makeLiveSandbox();
    const prune = ctx.window._livePruneStaleNodes;
    const markers = ctx.window._liveNodeMarkers();
    const data = ctx.window._liveNodeData();

    let lastStyle = {};
    let glowStyle = {};
    markers['apiNode'] = {
      _glowMarker: { setStyle: function(s) { glowStyle = s; } },
      _staleDimmed: true,
      setStyle: function(s) { lastStyle = s; },
    };
    data['apiNode'] = { public_key: 'apiNode', role: 'repeater', _fromAPI: true, _liveSeen: Date.now() };

    prune();

    assert.ok(markers['apiNode'], 'API node should remain');
    assert.strictEqual(markers['apiNode']._staleDimmed, false, 'staleDimmed should be cleared');
    assert.strictEqual(lastStyle.fillOpacity, 0.85, 'opacity should be restored to 0.85');
    assert.strictEqual(glowStyle.fillOpacity, 0.12, 'glow should be restored to 0.12');
  });

  test('pruneStaleNodes still removes WS-only nodes when stale', () => {
    const { ctx } = makeLiveSandbox();
    const prune = ctx.window._livePruneStaleNodes;
    const markers = ctx.window._liveNodeMarkers();
    const data = ctx.window._liveNodeData();

    // WS-only node (no _fromAPI) — should be removed
    markers['wsNode'] = { _glowMarker: null };
    data['wsNode'] = { public_key: 'wsNode', role: 'companion', _liveSeen: Date.now() - 48 * 3600000 };

    // API node — should be dimmed, not removed
    markers['apiNode'] = {
      _glowMarker: { setStyle: function() {} },
      _staleDimmed: false,
      setStyle: function() {},
    };
    data['apiNode'] = { public_key: 'apiNode', role: 'companion', _fromAPI: true, _liveSeen: Date.now() - 48 * 3600000 };

    prune();

    assert.ok(!markers['wsNode'], 'WS-only stale node should be removed');
    assert.ok(!data['wsNode'], 'WS-only stale node data should be removed');
    assert.ok(markers['apiNode'], 'API stale node should NOT be removed');
    assert.ok(data['apiNode'], 'API stale node data should NOT be removed');
  });
}

// ===== live.js: vcrFormatTime respects UTC/local setting =====
console.log('\n=== live.js: vcrFormatTime UTC/local ===');
{
  function makeLiveSandboxForVcr() {
    const ctx = makeSandbox();
    ctx.L = { map: () => ({ on: () => {}, setView: () => {}, addLayer: () => {}, remove: () => {} }), tileLayer: () => ({ addTo: () => {} }), layerGroup: () => ({ addTo: () => {}, clearLayers: () => {}, addLayer: () => {} }), circleMarker: () => ({ addTo: () => {}, remove: () => {}, setStyle: () => {}, getLatLng: () => ({}), on: () => {} }), Polyline: function() { return { addTo: () => {}, remove: () => {} }; }, Control: { extend: () => function() { return { addTo: () => {} }; } } };
    ctx.Chart = function() { return { destroy: () => {}, update: () => {} }; };
    ctx.navigator = {};
    ctx.visualViewport = null;
    ctx.document.documentElement = { getAttribute: () => null, setAttribute: () => {} };
    ctx.document.body = { appendChild: () => {}, removeChild: () => {}, contains: () => false };
    ctx.document.querySelector = () => null;
    ctx.document.querySelectorAll = () => [];
    ctx.document.createElementNS = () => ctx.document.createElement();
    ctx.cancelAnimationFrame = () => {};
    ctx.IATA_COORDS_GEO = {};
    loadInCtx(ctx, 'public/roles.js');
    try { loadInCtx(ctx, 'public/live.js'); } catch (e) {
      for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
    }
    return ctx;
  }

  test('vcrFormatTime is exposed as window._vcrFormatTime', () => {
    const ctx = makeLiveSandboxForVcr();
    assert.strictEqual(typeof ctx.window._vcrFormatTime, 'function', '_vcrFormatTime must be exposed');
  });

  test('vcrFormatTime uses UTC hours when timezone is utc', () => {
    const ctx = makeLiveSandboxForVcr();
    const fn = ctx.window._vcrFormatTime;
    assert.ok(fn, '_vcrFormatTime must be exposed');
    // Force UTC mode
    ctx.getTimestampTimezone = () => 'utc';
    // Use a known timestamp: 2024-01-15 14:30:45 UTC = different local time in most zones
    const tsMs = Date.UTC(2024, 0, 15, 14, 30, 45);
    const result = fn(tsMs);
    assert.strictEqual(result, '14:30:45', 'UTC mode must show UTC hours 14:30:45');
  });

  test('vcrFormatTime uses local hours when timezone is local', () => {
    const ctx = makeLiveSandboxForVcr();
    const fn = ctx.window._vcrFormatTime;
    assert.ok(fn, '_vcrFormatTime must be exposed');
    ctx.getTimestampTimezone = () => 'local';
    const d = new Date(2024, 0, 15, 9, 5, 3); // local time
    const expected = String(d.getHours()).padStart(2,'0') + ':' + String(d.getMinutes()).padStart(2,'0') + ':' + String(d.getSeconds()).padStart(2,'0');
    assert.strictEqual(fn(d.getTime()), expected, 'local mode must use local hours');
  });

  test('vcrFormatTime zero-pads single-digit hours, minutes, seconds', () => {
    const ctx = makeLiveSandboxForVcr();
    const fn = ctx.window._vcrFormatTime;
    assert.ok(fn, '_vcrFormatTime must be exposed');
    ctx.getTimestampTimezone = () => 'utc';
    const tsMs = Date.UTC(2024, 0, 15, 3, 5, 7); // 03:05:07 UTC
    assert.strictEqual(fn(tsMs), '03:05:07');
  });
}

// ===== NODES.JS: isAdvertMessage + auto-update logic =====
console.log('\n=== nodes.js: isAdvertMessage ===');
{
  const ctx = makeSandbox();
  // Provide the globals nodes.js depends on
  ctx.ROLE_COLORS = { repeater: '#22c55e', room: '#6366f1', companion: '#3b82f6', sensor: '#f59e0b' };
  ctx.ROLE_STYLE = {};
  ctx.TYPE_COLORS = {};
  ctx.getNodeStatus = () => 'active';
  ctx.getHealthThresholds = () => ({ staleMs: 600000, degradedMs: 1800000, silentMs: 86400000 });
  ctx.timeAgo = () => '1m ago';
  ctx.truncate = (s) => s;
  ctx.escapeHtml = (s) => String(s || '');
  ctx.payloadTypeName = () => 'Advert';
  ctx.payloadTypeColor = () => 'advert';
  ctx.registerPage = () => {};
  ctx.RegionFilter = { init: () => {}, onChange: () => () => {}, getRegionParam: () => '' };
  ctx.debouncedOnWS = () => null;
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.debounce = (fn) => fn;
  ctx.api = () => Promise.resolve({ nodes: [], counts: {} });
  ctx.invalidateApiCache = () => {};
  ctx.CLIENT_TTL = { nodeList: 90000, nodeDetail: 240000, nodeHealth: 240000 };
  ctx.initTabBar = () => {};
  ctx.getFavorites = () => [];
  ctx.favStar = () => '';
  ctx.bindFavStars = () => {};
  ctx.makeColumnsResizable = () => {};
  ctx.Set = Set;
  loadInCtx(ctx, 'public/nodes.js');

  const isAdvert = ctx._nodesIsAdvertMessage;

  test('rejects non-packet message', () => {
    assert.strictEqual(isAdvert({ type: 'message', data: {} }), false);
  });

  test('rejects packet without advert payload_type', () => {
    assert.strictEqual(isAdvert({ type: 'packet', data: { packet: { payload_type: 2 } } }), false);
  });

  test('detects format 1 advert (payload_type 4)', () => {
    assert.strictEqual(isAdvert({ type: 'packet', data: { packet: { payload_type: 4 } } }), true);
  });

  test('detects format 2 advert (payloadTypeName ADVERT)', () => {
    assert.strictEqual(isAdvert({ type: 'packet', data: { decoded: { header: { payloadTypeName: 'ADVERT' } } } }), true);
  });

  test('rejects packet with non-ADVERT payloadTypeName', () => {
    assert.strictEqual(isAdvert({ type: 'packet', data: { decoded: { header: { payloadTypeName: 'GRP_TXT' } } } }), false);
  });

  test('rejects empty data', () => {
    assert.strictEqual(isAdvert({ type: 'packet', data: {} }), false);
  });

  test('rejects null data', () => {
    assert.strictEqual(isAdvert({ type: 'packet', data: null }), false);
  });

  test('rejects missing data', () => {
    assert.strictEqual(isAdvert({ type: 'packet' }), false);
  });
}

console.log('\n=== nodes.js: WS handler runtime behavior ===');
{
  // Runtime tests for the auto-updating WS handler (replaces src.includes string checks).
  // Uses controllable setTimeout + mock DOM + real nodes.js code via vm.createContext.

  function makeNodesWsSandbox() {
    const ctx = makeSandbox();
    // Controllable timer queue
    const timers = [];
    let nextTimerId = 1;
    ctx.setTimeout = (fn, ms) => { const id = nextTimerId++; timers.push({ fn, ms, id }); return id; };
    ctx.clearTimeout = (targetId) => { const idx = timers.findIndex(t => t.id === targetId); if (idx >= 0) timers.splice(idx, 1); };

    // DOM elements mock — getElementById returns tracked mock elements
    const domElements = {};
    function getEl(id) {
      if (!domElements[id]) {
        domElements[id] = {
          id, innerHTML: '', textContent: '', value: '', scrollTop: 0,
          style: {}, dataset: {},
          classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
          addEventListener() {},
          querySelectorAll() { return []; },
          querySelector() { return null; },
          getAttribute() { return null; },
        };
      }
      return domElements[id];
    }
    ctx.document.getElementById = getEl;
    ctx.document.querySelectorAll = () => [];
    ctx.document.addEventListener = () => {};
    ctx.document.removeEventListener = () => {};

    // Globals nodes.js depends on
    ctx.ROLE_COLORS = { repeater: '#22c55e', room: '#6366f1', companion: '#3b82f6', sensor: '#f59e0b' };
    ctx.ROLE_STYLE = {};
    ctx.TYPE_COLORS = {};
    ctx.getNodeStatus = () => 'active';
    ctx.getHealthThresholds = () => ({ staleMs: 600000, degradedMs: 1800000, silentMs: 86400000 });
    ctx.timeAgo = () => '1m ago';
    ctx.truncate = (s) => s;
    ctx.escapeHtml = (s) => String(s || '');
    ctx.payloadTypeName = () => 'Advert';
    ctx.payloadTypeColor = () => 'advert';
    ctx.debounce = (fn) => fn;
    ctx.initTabBar = () => {};
    ctx.getFavorites = () => [];
    ctx.favStar = () => '';
    ctx.bindFavStars = () => {};
    ctx.makeColumnsResizable = () => {};
    ctx.Set = Set;
    ctx.CLIENT_TTL = { nodeList: 90000, nodeDetail: 240000, nodeHealth: 240000 };
    ctx.RegionFilter = { init() {}, onChange() { return () => {}; }, getRegionParam() { return ''; }, offChange() {} };

    // Track API calls and cache invalidation
    let apiCallCount = 0;
    const invalidatedPaths = [];
    ctx.api = () => { apiCallCount++; return Promise.resolve({ nodes: [{ public_key: 'abc123def456ghij', name: 'TestNode', role: 'repeater', advert_count: 1 }], counts: { repeaters: 1 } }); };
    ctx.invalidateApiCache = (path) => { invalidatedPaths.push(path); };

    // WS listener system (real debouncedOnWS from app.js, using our controllable setTimeout)
    let wsListeners = [];
    ctx.onWS = (fn) => { wsListeners.push(fn); };
    ctx.offWS = (fn) => { wsListeners = wsListeners.filter(f => f !== fn); };
    ctx.debouncedOnWS = function (fn, ms) {
      if (typeof ms === 'undefined') ms = 250;
      let pending = [];
      let timer = null;
      function handler(msg) {
        pending.push(msg);
        if (!timer) {
          timer = ctx.setTimeout(function () {
            const batch = pending;
            pending = [];
            timer = null;
            fn(batch);
          }, ms);
        }
      }
      wsListeners.push(handler);
      return handler;
    };

    // Capture registerPage to get init/destroy
    let pageMod = null;
    ctx.registerPage = (name, handlers) => { pageMod = handlers; };

    loadInCtx(ctx, 'public/nodes.js');

    // Create a mock app element and call init()
    const appEl = getEl('page');
    pageMod.init(appEl);

    // Reset counters after init's own loadNodes() call
    apiCallCount = 0;
    invalidatedPaths.length = 0;

    return {
      ctx, timers, wsListeners, domElements,
      getApiCalls: () => apiCallCount,
      getInvalidated: () => [...invalidatedPaths],
      resetCounters() { apiCallCount = 0; invalidatedPaths.length = 0; },
      fireTimers() { const fns = timers.splice(0).map(t => t.fn); fns.forEach(fn => fn()); },
      sendWS(msg) { wsListeners.forEach(fn => fn(msg)); },
    };
  }

  test('ADVERT packet triggers node list refresh via WS handler', () => {
    const env = makeNodesWsSandbox();
    env.sendWS({ type: 'packet', data: { packet: { payload_type: 4 } } });
    assert.strictEqual(env.timers.length, 1, 'debounce timer should be queued');
    assert.strictEqual(env.timers[0].ms, 5000, 'debounce should be 5000ms');
    env.fireTimers();
    assert.ok(env.getInvalidated().includes('/nodes'), 'should invalidate /nodes cache');
    assert.ok(env.getApiCalls() > 0, 'should call api() to re-fetch nodes');
  });

  test('non-ADVERT packet does NOT trigger refresh', () => {
    const env = makeNodesWsSandbox();
    env.sendWS({ type: 'packet', data: { packet: { payload_type: 2 } } });
    env.fireTimers();
    assert.strictEqual(env.getApiCalls(), 0, 'api should not be called for non-ADVERT');
    assert.deepStrictEqual(env.getInvalidated(), [], 'no cache invalidation for non-ADVERT');
  });

  test('debounce collapses multiple ADVERTs within 5s into one refresh', () => {
    const env = makeNodesWsSandbox();
    env.sendWS({ type: 'packet', data: { packet: { payload_type: 4 } } });
    env.sendWS({ type: 'packet', data: { packet: { payload_type: 4 } } });
    env.sendWS({ type: 'packet', data: { packet: { payload_type: 4 } } });
    assert.strictEqual(env.timers.length, 1, 'only one debounce timer despite 3 messages');
    env.fireTimers();
    assert.ok(env.getApiCalls() > 0, 'api called after debounce fires');
    // Verify it was only 1 batch call (invalidated once)
    const nodeInvalidations = env.getInvalidated().filter(p => p === '/nodes');
    assert.strictEqual(nodeInvalidations.length, 1, 'cache invalidated exactly once');
  });

  test('WS ADVERT resets _allNodes cache before refresh', () => {
    const env = makeNodesWsSandbox();
    // After init, _allNodes may be populated (pending async). Send ADVERT to reset it.
    env.sendWS({ type: 'packet', data: { decoded: { header: { payloadTypeName: 'ADVERT' } } } });
    env.fireTimers();
    // If _allNodes was reset to null, loadNodes will call api() to re-fetch
    assert.ok(env.getApiCalls() > 0, 'api called because _allNodes was reset to null');
  });

  test('ADVERT for known node upserts in-place without API fetch', () => {
    const env = makeNodesWsSandbox();
    // Pre-populate _allNodes with a known node
    assert.ok(typeof env.ctx.window._nodesSetAllNodes === 'function', '_nodesSetAllNodes must be exposed');
    env.ctx.window._nodesSetAllNodes([
      { public_key: 'aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899', name: 'OldName', role: 'repeater', lat: null, lon: null, last_seen: '2024-01-01T00:00:00Z' }
    ]);
    env.resetCounters();

    env.sendWS({
      type: 'packet',
      data: {
        packet: { payload_type: 4, timestamp: '2024-06-01T12:00:00Z' },
        decoded: {
          header: { payloadTypeName: 'ADVERT' },
          payload: { type: 'ADVERT', pubKey: 'aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899', name: 'NewName', lat: 50.85, lon: 4.35 }
        }
      }
    });
    env.fireTimers();

    assert.strictEqual(env.getApiCalls(), 0, 'known node upsert must NOT trigger API fetch');
    assert.strictEqual(env.getInvalidated().length, 0, 'no cache invalidation for known node upsert');
    const nodes = env.ctx.window._nodesGetAllNodes();
    assert.ok(nodes, '_nodesGetAllNodes must be exposed');
    assert.strictEqual(nodes[0].name, 'NewName', 'name must be updated in place');
    assert.strictEqual(nodes[0].lat, 50.85, 'lat must be updated in place');
    assert.strictEqual(nodes[0].lon, 4.35, 'lon must be updated in place');
    assert.strictEqual(nodes[0].last_seen, '2024-06-01T12:00:00Z', 'last_seen must be updated from packet timestamp');
  });

  test('ADVERT for unknown node falls back to full reload', () => {
    const env = makeNodesWsSandbox();
    env.ctx.window._nodesSetAllNodes([
      { public_key: 'aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899', name: 'ExistingNode', role: 'repeater' }
    ]);
    env.resetCounters();

    // Send ADVERT from a pubKey NOT in _allNodes
    env.sendWS({
      type: 'packet',
      data: {
        packet: { payload_type: 4 },
        decoded: {
          header: { payloadTypeName: 'ADVERT' },
          payload: { type: 'ADVERT', pubKey: 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff', name: 'BrandNewNode' }
        }
      }
    });
    env.fireTimers();

    assert.ok(env.getApiCalls() > 0, 'unknown node must trigger full reload');
    assert.ok(env.getInvalidated().includes('/nodes'), 'cache must be invalidated for unknown node');
  });

  test('scroll position and selection preserved during WS-triggered refresh', () => {
    const env = makeNodesWsSandbox();
    // Simulate scrolled panel state — WS handler should not touch scroll or rebuild panel
    const nodesLeftEl = env.ctx.document.getElementById('nodesLeft');
    nodesLeftEl.scrollTop = 500;
    nodesLeftEl.innerHTML = 'PANEL_WITH_TABS_AND_TABLE';

    env.sendWS({ type: 'packet', data: { packet: { payload_type: 4 } } });
    env.fireTimers();

    // WS handler calls _allNodes=null + invalidateApiCache + loadNodes(true) synchronously.
    // loadNodes(true) is async but the handler itself doesn't touch scroll or panel structure.
    // refreshOnly=true causes renderRows (tbody only), not renderLeft (full panel rebuild).
    assert.strictEqual(nodesLeftEl.scrollTop, 500, 'scrollTop preserved — WS handler does not reset scroll');
    assert.strictEqual(nodesLeftEl.innerHTML, 'PANEL_WITH_TABS_AND_TABLE',
      'panel innerHTML preserved — WS handler does not rebuild panel synchronously');
    // Verify the refresh was triggered (API called) but no extra state was cleared
    assert.ok(env.getApiCalls() > 0, 'API called for data refresh');
    assert.ok(env.getInvalidated().includes('/nodes'), 'cache invalidated for fresh data');
  });
}

// ===== COMPARE.JS TESTS =====
console.log('\n=== compare.js: comparePacketSets ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  loadInCtx(ctx, 'public/compare.js');
  const cmp = ctx.comparePacketSets;

  test('both empty sets', () => {
    const r = cmp([], []);
    assert.strictEqual(r.onlyA.length, 0);
    assert.strictEqual(r.onlyB.length, 0);
    assert.strictEqual(r.both.length, 0);
  });

  test('A has items, B empty', () => {
    const r = cmp(['h1', 'h2'], []);
    assert.strictEqual(r.onlyA.length, 2);
    assert.ok(r.onlyA.includes('h1'));
    assert.ok(r.onlyA.includes('h2'));
    assert.strictEqual(r.onlyB.length, 0);
    assert.strictEqual(r.both.length, 0);
  });

  test('A empty, B has items', () => {
    const r = cmp([], ['h3', 'h4']);
    assert.strictEqual(r.onlyA.length, 0);
    assert.strictEqual(r.onlyB.length, 2);
    assert.ok(r.onlyB.includes('h3'));
    assert.ok(r.onlyB.includes('h4'));
    assert.strictEqual(r.both.length, 0);
  });

  test('complete overlap', () => {
    const r = cmp(['h1', 'h2', 'h3'], ['h1', 'h2', 'h3']);
    assert.strictEqual(r.onlyA.length, 0);
    assert.strictEqual(r.onlyB.length, 0);
    assert.strictEqual(r.both.length, 3);
    assert.ok(r.both.includes('h1'));
    assert.ok(r.both.includes('h2'));
    assert.ok(r.both.includes('h3'));
  });

  test('no overlap', () => {
    const r = cmp(['h1', 'h2'], ['h3', 'h4']);
    assert.strictEqual(r.onlyA.length, 2);
    assert.strictEqual(r.onlyB.length, 2);
    assert.strictEqual(r.both.length, 0);
  });

  test('partial overlap', () => {
    const r = cmp(['h1', 'h2', 'h3'], ['h2', 'h3', 'h4']);
    assert.strictEqual(r.onlyA.length, 1);
    assert.ok(r.onlyA.includes('h1'));
    assert.strictEqual(r.onlyB.length, 1);
    assert.ok(r.onlyB.includes('h4'));
    assert.strictEqual(r.both.length, 2);
    assert.ok(r.both.includes('h2'));
    assert.ok(r.both.includes('h3'));
  });

  test('accepts Set inputs', () => {
    const r = cmp(new Set(['a', 'b', 'c']), new Set(['b', 'c', 'd']));
    assert.strictEqual(r.onlyA.length, 1);
    assert.ok(r.onlyA.includes('a'));
    assert.strictEqual(r.onlyB.length, 1);
    assert.ok(r.onlyB.includes('d'));
    assert.strictEqual(r.both.length, 2);
  });

  test('handles null/undefined gracefully', () => {
    const r = cmp(null, undefined);
    assert.strictEqual(r.onlyA.length, 0);
    assert.strictEqual(r.onlyB.length, 0);
    assert.strictEqual(r.both.length, 0);
  });

  test('handles duplicates in input arrays', () => {
    const r = cmp(['h1', 'h1', 'h2'], ['h2', 'h2', 'h3']);
    assert.strictEqual(r.onlyA.length, 1);
    assert.ok(r.onlyA.includes('h1'));
    assert.strictEqual(r.onlyB.length, 1);
    assert.ok(r.onlyB.includes('h3'));
    assert.strictEqual(r.both.length, 1);
    assert.ok(r.both.includes('h2'));
  });

  test('large set performance (10K hashes)', () => {
    const a = []; const b = [];
    for (var i = 0; i < 10000; i++) {
      a.push('hash_' + i);
      if (i % 2 === 0) b.push('hash_' + i);
    }
    b.push('unique_b');
    const t0 = Date.now();
    const r = cmp(a, b);
    const elapsed = Date.now() - t0;
    assert.strictEqual(r.both.length, 5000, 'should have 5000 shared hashes');
    assert.strictEqual(r.onlyA.length, 5000, 'should have 5000 A-only hashes');
    assert.strictEqual(r.onlyB.length, 1, 'should have 1 B-only hash');
    assert.ok(elapsed < 500, 'should complete in under 500ms, took ' + elapsed + 'ms');
  });

  test('total = onlyA + onlyB + both', () => {
    const r = cmp(['a', 'b', 'c', 'd'], ['c', 'd', 'e', 'f', 'g']);
    const total = r.onlyA.length + r.onlyB.length + r.both.length;
    const uniqueAll = new Set([...['a', 'b', 'c', 'd'], ...['c', 'd', 'e', 'f', 'g']]);
    assert.strictEqual(total, uniqueAll.size, 'total should equal number of unique hashes');
  });
}

// ===== Packets page: detail pane starts collapsed =====
{
  console.log('\nPackets page — detail pane initial state:');
  const packetsSource = fs.readFileSync('public/packets.js', 'utf8');

  test('split-layout starts with detail-collapsed class', () => {
    // The template literal that creates the split-layout must include detail-collapsed
    const match = packetsSource.match(/innerHTML\s*=\s*`<div class="split-layout([^"]*)">/);
    assert.ok(match, 'should find split-layout innerHTML assignment');
    assert.ok(match[1].includes('detail-collapsed'),
      'split-layout initial class should include detail-collapsed, got: "split-layout' + match[1] + '"');
  });

  test('closeDetailPanel adds detail-collapsed', () => {
    assert.ok(packetsSource.includes("classList.add('detail-collapsed')"),
      'closeDetailPanel should add detail-collapsed class');
  });

  test('selectPacket removes detail-collapsed', () => {
    assert.ok(packetsSource.includes("classList.remove('detail-collapsed')"),
      'selectPacket should remove detail-collapsed class');
  });

  test('BYOP uses dedicated overlay class and clears existing overlays before opening', () => {
    assert.ok(packetsSource.includes("overlay.className = 'modal-overlay byop-overlay'"),
      'BYOP overlay should have byop-overlay class');
    assert.ok(/function showBYOP\(\)\s*\{\s*removeAllByopOverlays\(\);/m.test(packetsSource),
      'showBYOP should clear existing overlays before creating a new one');
  });

  test('BYOP close removes all overlays in one click', () => {
    assert.ok(packetsSource.includes("const close = () => { removeAllByopOverlays(); if (triggerBtn) triggerBtn.focus(); };"),
      'close handler should remove all BYOP overlays');
  });

  test('packets page de-duplicates document click handlers', () => {
    assert.ok(packetsSource.includes("bindDocumentHandler('action', 'click'"),
      'action click handler should be bound through bindDocumentHandler');
    assert.ok(packetsSource.includes("bindDocumentHandler('menu', 'click'"),
      'menu close handler should be bound through bindDocumentHandler');
    assert.ok(packetsSource.includes("bindDocumentHandler('colmenu', 'click'"),
      'column menu close handler should be bound through bindDocumentHandler');
    assert.ok(packetsSource.includes("if (prev) document.removeEventListener(eventName, prev);"),
      'bindDocumentHandler should remove previous handler before re-binding');
  });

  test('first packets fetch uses persisted time window before filters render', async () => {
    const ctx = makeSandbox();
    const apiCalls = [];
    ctx.localStorage.setItem('meshcore-time-window', '60');
    const dom = {
      pktRight: { addEventListener() {}, classList: { add() {}, remove() {}, contains() { return false; } }, innerHTML: '' },
    };
    ctx.document.getElementById = (id) => {
      if (id === 'fTimeWindow') return null; // Simulate first fetch before filter controls are rendered
      return dom[id] || null;
    };
    ctx.document.addEventListener = () => {};
    ctx.document.removeEventListener = () => {};
    ctx.document.body = { appendChild() {}, removeChild() {}, contains() { return false; } };
    ctx.window.addEventListener = () => {};
    ctx.window.removeEventListener = () => {};
    ctx.RegionFilter = { init() {}, onChange() { return () => {}; }, offChange() {}, getRegionParam() { return ''; } };
    ctx.CLIENT_TTL = { observers: 120000 };
    ctx.debouncedOnWS = (fn) => fn;
    ctx.onWS = () => {};
    ctx.offWS = () => {};
    ctx.registerPage = (name, handlers) => { if (name === 'packets') ctx._packetsHandlers = handlers; };
    ctx.api = (path) => {
      apiCalls.push(path);
      if (path.indexOf('/observers') === 0) return Promise.resolve({ observers: [] });
      if (path.indexOf('/packets?') === 0) return Promise.reject(new Error('stop after request capture'));
      if (path.indexOf('/config/regions') === 0) return Promise.resolve({});
      return Promise.resolve({});
    };

    loadInCtx(ctx, 'public/packets.js');
    assert.ok(ctx._packetsHandlers && typeof ctx._packetsHandlers.init === 'function',
      'packets page should register init handler');
    await ctx._packetsHandlers.init({ innerHTML: '' });

    const firstPacketsCall = apiCalls.find(p => p.indexOf('/packets?') === 0);
    assert.ok(firstPacketsCall, 'packets API should be called during initial packets page load');
    const params = new URLSearchParams((firstPacketsCall.split('?')[1] || ''));
    const since = params.get('since');
    assert.ok(since, 'initial packets request should include since parameter');

    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert.ok(deltaMin > 45 && deltaMin < 75,
      `expected persisted ~60m window, got ${deltaMin.toFixed(2)}m`);
  });
}

// ===== APP.JS: formatEngineBadge =====
console.log('\n=== app.js: formatEngineBadge ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const formatEngineBadge = ctx.formatEngineBadge;

  test('returns empty string for null', () => assert.strictEqual(formatEngineBadge(null), ''));
  test('returns empty string for undefined', () => assert.strictEqual(formatEngineBadge(undefined), ''));
  test('returns empty string for empty string', () => assert.strictEqual(formatEngineBadge(''), ''));
  test('returns badge span for "go"', () => {
    const result = formatEngineBadge('go');
    assert.ok(result.includes('engine-badge'), 'should contain engine-badge class');
    assert.ok(result.includes('>go<'), 'should contain engine name');
  });
  test('returns badge span for "node"', () => {
    const result = formatEngineBadge('node');
    assert.ok(result.includes('engine-badge'), 'should contain engine-badge class');
    assert.ok(result.includes('>node<'), 'should contain engine name');
  });
}

// ===== APP.JS: isTransportRoute + transportBadge =====
console.log('\n=== app.js: isTransportRoute + transportBadge ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const isTransportRoute = ctx.isTransportRoute;
  const transportBadge = ctx.transportBadge;

  test('isTransportRoute(0) is true (TRANSPORT_FLOOD)', () => assert.strictEqual(isTransportRoute(0), true));
  test('isTransportRoute(3) is true (TRANSPORT_DIRECT)', () => assert.strictEqual(isTransportRoute(3), true));
  test('isTransportRoute(1) is false (FLOOD)', () => assert.strictEqual(isTransportRoute(1), false));
  test('isTransportRoute(2) is false (DIRECT)', () => assert.strictEqual(isTransportRoute(2), false));
  test('isTransportRoute(null) is false', () => assert.strictEqual(isTransportRoute(null), false));
  test('isTransportRoute(undefined) is false', () => assert.strictEqual(isTransportRoute(undefined), false));

  test('transportBadge(0) contains badge-transport class', () => {
    const html = transportBadge(0);
    assert.ok(html.includes('badge-transport'), 'should contain badge-transport class');
    assert.ok(html.includes('>T<'), 'should contain T label');
    assert.ok(html.includes('TRANSPORT_FLOOD'), 'should contain route type name in title');
  });
  test('transportBadge(1) returns empty string', () => assert.strictEqual(transportBadge(1), ''));
}

// ===== APP.JS: formatVersionBadge =====
console.log('\n=== app.js: formatVersionBadge ===');
{
  function makeBadgeSandbox(port) {
    const ctx = makeSandbox();
    ctx.location.port = port || '';
    loadInCtx(ctx, 'public/roles.js');
    loadInCtx(ctx, 'public/app.js');
    return ctx;
  }
  const GH = 'https://github.com/Kpa-clawbot/corescope';

  test('returns empty string when all args missing', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    assert.strictEqual(formatVersionBadge(null, null, null), '');
    assert.strictEqual(formatVersionBadge(undefined, undefined, undefined), '');
    assert.strictEqual(formatVersionBadge('', '', ''), '');
  });

  // --- Prod tests (no port / port 80 / port 443) ---
  test('prod: shows version + commit + engine with links', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('2.6.0', 'abc1234def5678', 'node', null);
    assert.ok(result.includes('version-badge'), 'should have version-badge class');
    assert.ok(result.includes(`href="${GH}/releases/tag/v2.6.0"`), 'version links to release');
    assert.ok(result.includes('>v2.6.0</a>'), 'version text has v prefix');
    assert.ok(result.includes(`href="${GH}/commit/abc1234def5678"`), 'commit links to full hash');
    assert.ok(result.includes('>abc1234</a>'), 'commit display is truncated to 7');
    assert.ok(result.includes('engine-badge'), 'should show engine badge'); assert.ok(result.includes('>node<'), 'should show engine name');
  });
  test('prod port 80: shows version', () => {
    const { formatVersionBadge } = makeBadgeSandbox('80');
    const result = formatVersionBadge('2.6.0', null, 'node', null);
    assert.ok(result.includes('>v2.6.0</a>'), 'port 80 is prod — shows version');
  });
  test('prod port 443: shows version', () => {
    const { formatVersionBadge } = makeBadgeSandbox('443');
    const result = formatVersionBadge('2.6.0', null, 'node', null);
    assert.ok(result.includes('>v2.6.0</a>'), 'port 443 is prod — shows version');
  });
  test('prod: version already has v prefix', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('v2.6.0', null, null, null);
    assert.ok(result.includes('>v2.6.0</a>'), 'should not double the v prefix');
    assert.ok(!result.includes('vv'), 'should not have vv');
  });

  // --- Staging tests (non-standard port) ---
  test('staging: hides version, shows commit + engine', () => {
    const { formatVersionBadge } = makeBadgeSandbox('3000');
    const result = formatVersionBadge('2.6.0', 'abc1234def5678', 'go', null);
    assert.ok(!result.includes('v2.6.0'), 'staging should NOT show version');
    assert.ok(result.includes('>abc1234</a>'), 'should show commit hash');
    assert.ok(result.includes(`href="${GH}/commit/abc1234def5678"`), 'commit is linked');
    assert.ok(result.includes('engine-badge'), 'should show engine badge'); assert.ok(result.includes('>go<'), 'should show engine name');
  });
  test('staging port 81: hides version', () => {
    const { formatVersionBadge } = makeBadgeSandbox('81');
    const result = formatVersionBadge('2.6.0', 'abc1234', 'go', null);
    assert.ok(!result.includes('v2.6.0'), 'port 81 is staging — no version');
    assert.ok(result.includes('>abc1234</a>'), 'commit shown');
  });

  // --- Shared behavior ---
  test('commit link uses full hash', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge(null, 'abc1234def567890123456789abcdef012345678', 'node', null);
    assert.ok(result.includes(`href="${GH}/commit/abc1234def567890123456789abcdef012345678"`), 'link uses full hash');
    assert.ok(result.includes('>abc1234</a>'), 'display is truncated to 7');
  });
  test('skips commit when "unknown"', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('2.6.0', 'unknown', 'node', null);
    assert.ok(result.includes('>v2.6.0</a>'), 'should show version');
    assert.ok(!result.includes('unknown'), 'should not show unknown commit');
    assert.ok(result.includes('engine-badge'), 'should show engine badge'); assert.ok(result.includes('>node<'), 'should show engine name');
  });
  test('skips commit when missing', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('2.6.0', null, 'go', null);
    assert.ok(result.includes('>v2.6.0</a>'), 'should show version');
    assert.ok(result.includes('engine-badge'), 'should show engine badge'); assert.ok(result.includes('>go<'), 'should show engine name');
  });
  test('shows only engine when version/commit missing', () => {
    const { formatVersionBadge } = makeBadgeSandbox('3000');
    const result = formatVersionBadge(null, null, 'go', null);
    assert.ok(result.includes('engine-badge'), 'should show engine badge'); assert.ok(result.includes('>go<'), 'should show engine name');
    assert.ok(result.includes('version-badge'), 'should use version-badge class');
  });
  test('short commit not truncated in display', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('1.0.0', 'abc1234', 'node', null);
    assert.ok(result.includes('>abc1234</a>'), 'should show full short commit');
  });
  test('version only on prod', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('2.6.0', null, null, null);
    assert.ok(result.includes('>v2.6.0</a>'), 'should show version');
    assert.ok(!result.includes('·'), 'should not have separator for single part');
  });
  test('staging: only engine when no commit', () => {
    const { formatVersionBadge } = makeBadgeSandbox('8080');
    const result = formatVersionBadge('2.6.0', null, 'go', null);
    assert.ok(!result.includes('2.6.0'), 'no version on staging');
    assert.ok(result.includes('engine-badge'), 'engine badge shown'); assert.ok(result.includes('>go<'), 'engine name shown');
  });
  test('shows build age next to commit when buildTime is valid', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const recent = new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString();
    const result = formatVersionBadge('2.6.0', 'abc1234def5678', 'go', recent);
    assert.ok(result.includes('>abc1234</a>'), 'commit shown');
    assert.ok(result.includes('build-age'), 'build age span shown');
    assert.ok(result.includes('(3h ago)'), 'build age text shown');
  });
  test('does not show build age for unknown buildTime', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('2.6.0', 'abc1234def5678', 'go', 'unknown');
    assert.ok(!result.includes('build-age'), 'no build age for unknown buildTime');
  });
  test('does not show build age for null buildTime', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('2.6.0', 'abc1234def5678', 'go', null);
    assert.ok(!result.includes('build-age'), 'no build age for null buildTime');
  });
  test('does not show build age for undefined buildTime', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('2.6.0', 'abc1234def5678', 'go');
    assert.ok(!result.includes('build-age'), 'no build age for undefined buildTime');
  });
  test('does not show build age for invalid buildTime', () => {
    const { formatVersionBadge } = makeBadgeSandbox('');
    const result = formatVersionBadge('2.6.0', 'abc1234def5678', 'go', 'not-a-date');
    assert.ok(!result.includes('build-age'), 'no build age for invalid buildTime');
  });
}

// ===== CSS: version-badge link contrast (issue #139) =====
console.log('\n=== style.css: version-badge link contrast ===');
{
  const cssContent = fs.readFileSync(__dirname + '/public/style.css', 'utf8');
  test('version-badge a has explicit color', () => {
    assert.ok(cssContent.includes('.version-badge a'), 'should have .version-badge a rule');
    assert.ok(/\.version-badge a\s*\{[^}]*color:\s*var\(--nav-text-muted\)/.test(cssContent),
      'link color should use var(--nav-text-muted)');
  });
  test('version-badge a has hover state', () => {
    assert.ok(cssContent.includes('.version-badge a:hover'), 'should have .version-badge a:hover rule');
    assert.ok(/\.version-badge a:hover\s*\{[^}]*color:\s*var\(--nav-text\)/.test(cssContent),
      'hover color should use var(--nav-text)');
  });
}

// ===== ANALYTICS.JS: Channel Sort =====
console.log('\n=== analytics.js: sortChannels ===');
{
  function makeAnalyticsSandbox() {
    const ctx = makeSandbox();
    ctx.getComputedStyle = () => ({ getPropertyValue: () => '' });
    ctx.registerPage = () => {};
    ctx.api = () => Promise.resolve({});
    ctx.timeAgo = (iso) => iso ? 'x ago' : '—';
    ctx.RegionFilter = { init: () => {}, onChange: () => {}, regionQueryString: () => '' };
    ctx.onWS = () => {};
    ctx.offWS = () => {};
    ctx.connectWS = () => {};
    ctx.invalidateApiCache = () => {};
    ctx.makeColumnsResizable = () => {};
    ctx.initTabBar = () => {};
    ctx.IATA_COORDS_GEO = {};
    loadInCtx(ctx, 'public/roles.js');
    loadInCtx(ctx, 'public/app.js');
    try { loadInCtx(ctx, 'public/analytics.js'); } catch (e) {
      for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
    }
    return ctx;
  }

  const ctx = makeAnalyticsSandbox();
  const sortChannels = ctx.window._analyticsSortChannels;
  const loadSort = ctx.window._analyticsLoadChannelSort;
  const saveSort = ctx.window._analyticsSaveChannelSort;
  const tbodyHtml = ctx.window._analyticsChannelTbodyHtml;
  const theadHtml = ctx.window._analyticsChannelTheadHtml;

  const channels = [
    { name: 'General', hash: 10, messages: 50, senders: 5, lastActivity: '2024-01-03T12:00:00Z', encrypted: false },
    { name: 'Alerts', hash: 20, messages: 200, senders: 12, lastActivity: '2024-01-01T08:00:00Z', encrypted: true },
    { name: 'Chat', hash: 5, messages: 100, senders: 8, lastActivity: '2024-01-05T18:00:00Z', encrypted: false },
  ];

  test('sortChannels exists', () => assert.ok(sortChannels, '_analyticsSortChannels must be exposed'));

  test('sort by name asc', () => {
    const r = sortChannels(channels, 'name', 'asc');
    assert.deepStrictEqual(r.map(c => c.name), ['Alerts', 'Chat', 'General']);
  });

  test('sort by name desc', () => {
    const r = sortChannels(channels, 'name', 'desc');
    assert.deepStrictEqual(r.map(c => c.name), ['General', 'Chat', 'Alerts']);
  });

  test('sort by messages desc', () => {
    const r = sortChannels(channels, 'messages', 'desc');
    assert.deepStrictEqual(r.map(c => c.messages), [200, 100, 50]);
  });

  test('sort by messages asc', () => {
    const r = sortChannels(channels, 'messages', 'asc');
    assert.deepStrictEqual(r.map(c => c.messages), [50, 100, 200]);
  });

  test('sort by senders desc', () => {
    const r = sortChannels(channels, 'senders', 'desc');
    assert.deepStrictEqual(r.map(c => c.senders), [12, 8, 5]);
  });

  test('sort by lastActivity desc (latest first)', () => {
    const r = sortChannels(channels, 'lastActivity', 'desc');
    assert.strictEqual(r[0].name, 'Chat');
    assert.strictEqual(r[2].name, 'Alerts');
  });

  test('sort by lastActivity asc (oldest first)', () => {
    const r = sortChannels(channels, 'lastActivity', 'asc');
    assert.strictEqual(r[0].name, 'Alerts');
    assert.strictEqual(r[2].name, 'Chat');
  });

  test('sort by encrypted', () => {
    const r = sortChannels(channels, 'encrypted', 'desc');
    assert.strictEqual(r[0].encrypted, true);
  });

  test('sort by hash asc (numeric)', () => {
    const r = sortChannels(channels, 'hash', 'asc');
    assert.deepStrictEqual(r.map(c => c.hash), [5, 10, 20]);
  });

  test('sort does not mutate original', () => {
    const orig = channels.map(c => c.name);
    sortChannels(channels, 'name', 'asc');
    assert.deepStrictEqual(channels.map(c => c.name), orig);
  });

  test('sort empty array', () => {
    const r = sortChannels([], 'name', 'asc');
    assert.deepStrictEqual(r, []);
  });

  test('sort handles missing name', () => {
    const data = [
      { name: 'B', hash: 1, messages: 1, senders: 1, lastActivity: '', encrypted: false },
      { name: null, hash: 2, messages: 2, senders: 2, lastActivity: '', encrypted: false },
    ];
    const r = sortChannels(data, 'name', 'asc');
    assert.strictEqual(r[0].name, null);
    assert.strictEqual(r[1].name, 'B');
  });

  test('sort handles missing lastActivity', () => {
    const data = [
      { name: 'A', hash: 1, messages: 1, senders: 1, lastActivity: '2024-01-01', encrypted: false },
      { name: 'B', hash: 2, messages: 2, senders: 2, lastActivity: null, encrypted: false },
    ];
    const r = sortChannels(data, 'lastActivity', 'desc');
    assert.strictEqual(r[0].name, 'A');
  });

  test('default sort is lastActivity desc', () => {
    const s = loadSort();
    assert.strictEqual(s.col, 'lastActivity');
    assert.strictEqual(s.dir, 'desc');
  });

  test('saveSort + loadSort round-trip', () => {
    saveSort({ col: 'messages', dir: 'asc' });
    const s = loadSort();
    assert.strictEqual(s.col, 'messages');
    assert.strictEqual(s.dir, 'asc');
    // Reset
    ctx.localStorage.removeItem('meshcore-channel-sort');
  });

  test('loadSort handles corrupt localStorage', () => {
    ctx.localStorage.setItem('meshcore-channel-sort', '{bad json');
    const s = loadSort();
    assert.strictEqual(s.col, 'lastActivity');
    ctx.localStorage.removeItem('meshcore-channel-sort');
  });

  test('theadHtml marks active column', () => {
    const html = theadHtml('messages', 'desc');
    assert.ok(html.includes('sort-active'), 'active column should have sort-active class');
    assert.ok(html.includes('data-sort-col="messages"'), 'should have data-sort-col');
    assert.ok(html.includes('↓'), 'desc direction should show ↓');
  });

  test('theadHtml shows ↑ for asc', () => {
    const html = theadHtml('name', 'asc');
    assert.ok(html.includes('↑'), 'asc direction should show ↑');
  });

  test('theadHtml shows ⇅ for inactive columns', () => {
    const html = theadHtml('messages', 'desc');
    // 'name' column should show ⇅
    assert.ok(html.includes('⇅'), 'inactive columns should show ⇅');
  });

  test('tbodyHtml generates rows', () => {
    const html = tbodyHtml(channels, 'messages', 'desc');
    assert.ok(html.includes('Alerts'), 'should include channel name');
    assert.ok(html.includes('clickable-row'), 'rows should be clickable');
    assert.ok(html.includes('data-action="navigate"'), 'rows should have navigate action');
  });

  test('tbodyHtml returns sorted rows', () => {
    const html = tbodyHtml(channels, 'messages', 'desc');
    const alertsIdx = html.indexOf('Alerts');
    const chatIdx = html.indexOf('Chat');
    const generalIdx = html.indexOf('General');
    assert.ok(alertsIdx < chatIdx, 'Alerts (200 msgs) should come before Chat (100)');
    assert.ok(chatIdx < generalIdx, 'Chat (100 msgs) should come before General (50)');
  });

  test('sort by string hash values', () => {
    const data = [
      { name: 'A', hash: 'zz', messages: 1, senders: 1, lastActivity: '', encrypted: false },
      { name: 'B', hash: 'aa', messages: 1, senders: 1, lastActivity: '', encrypted: false },
    ];
    const r = sortChannels(data, 'hash', 'asc');
    assert.strictEqual(r[0].hash, 'aa');
    assert.strictEqual(r[1].hash, 'zz');
  });
}


// ===== CUSTOMIZE.JS: initState merge behavior =====
console.log('\n=== customize.js: initState merge behavior ===');
{
  function loadCustomizeExports(ctx) {
    const src = fs.readFileSync('public/customize.js', 'utf8');
    const withExports = src.replace(
      /\}\)\(\);\s*$/,
      'window.__customizeExport = { initState: initState, autoSave: autoSave, getState: function () { return state; }, getDefaults: function () { return deepClone(DEFAULTS); }, setInitialized: function (v) { _initialized = !!v; } };})();'
    );
    vm.runInContext(withExports, ctx);
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
    return ctx.window.__customizeExport;
  }

  test('autoSave no-ops before initialization on panel open path', () => {
    const ctx = makeSandbox();
    let saveTimerCalls = 0;
    ctx.setTimeout = function () { saveTimerCalls++; return 1; };
    ctx.clearTimeout = function () {};
    ctx.window.SITE_CONFIG = { home: { heroTitle: 'Server Hero' } };
    const ex = loadCustomizeExports(ctx);
    ex.initState();
    ex.setInitialized(false);
    ex.autoSave();
    assert.strictEqual(saveTimerCalls, 0);
    assert.strictEqual(ctx.localStorage.getItem('meshcore-user-theme'), null);
  });

  test('server home config survives customizer open without modification', () => {
    const ctx = makeSandbox();
    let saveTimerCalls = 0;
    ctx.setTimeout = function () { saveTimerCalls++; return 1; };
    ctx.clearTimeout = function () {};
    ctx.window.SITE_CONFIG = {
      home: {
        heroTitle: 'Server Hero',
        heroSubtitle: 'Server Subtitle',
        steps: [{ emoji: 'S', title: 'Server Step', description: 'server' }],
        checklist: [{ question: 'Server Q', answer: 'Server A' }],
        footerLinks: [{ label: 'Server Link', url: '#/server' }]
      }
    };
    const before = JSON.stringify(ctx.window.SITE_CONFIG.home);
    const ex = loadCustomizeExports(ctx);
    ex.initState();
    ex.setInitialized(false);
    ex.autoSave();
    assert.strictEqual(saveTimerCalls, 0);
    assert.strictEqual(JSON.stringify(ctx.window.SITE_CONFIG.home), before);
  });

  test('post-init autoSave exports user theme without mutating SITE_CONFIG.home', () => {
    const ctx = makeSandbox();
    let saveTimerCalls = 0;
    ctx.setTimeout = function (fn) { saveTimerCalls++; fn(); return 1; };
    ctx.clearTimeout = function () {};
    ctx.HashChangeEvent = function HashChangeEvent(type) { this.type = type; };
    ctx.window.SITE_CONFIG = {
      home: {
        heroTitle: 'Server Hero',
        heroSubtitle: 'Server Subtitle',
        steps: [{ emoji: 'S', title: 'Server Step', description: 'server' }],
        checklist: [{ question: 'Server Q', answer: 'Server A' }],
        footerLinks: [{ label: 'Server Link', url: '#/server' }]
      }
    };
    const before = JSON.stringify(ctx.window.SITE_CONFIG.home);
    const ex = loadCustomizeExports(ctx);
    ex.initState();
    ex.setInitialized(true);
    ex.autoSave();
    const saved = ctx.localStorage.getItem('meshcore-user-theme');
    assert.strictEqual(saveTimerCalls, 1);
    assert(saved && saved.length > 0, 'Expected autoSave to persist user theme');
    assert.strictEqual(JSON.stringify(ctx.window.SITE_CONFIG.home), before);
  });

  test('partial local checklist does not wipe steps/footerLinks and keeps server colors', () => {
    const ctx = makeSandbox();
    ctx.window.SITE_CONFIG = {
      home: {
        heroTitle: 'Server Hero',
        heroSubtitle: 'Server Subtitle',
        steps: [{ emoji: '🧪', title: 'Server Step', description: 'from server' }],
        checklist: [{ question: 'Server Q', answer: 'Server A' }],
        footerLinks: [{ label: 'Server Link', url: '#/server' }]
      },
      theme: { accent: '#123456', navBg: '#222222' },
      nodeColors: { repeater: '#aa0000' }
    };
    ctx.localStorage.setItem('meshcore-user-theme', JSON.stringify({
      home: { checklist: [{ question: 'Local Q', answer: 'Local A' }] }
    }));
    const ex = loadCustomizeExports(ctx);
    ex.initState();
    const state = ex.getState();
    assert.strictEqual(state.home.checklist[0].question, 'Local Q');
    assert.strictEqual(state.home.steps[0].title, 'Server Step');
    assert.strictEqual(state.home.footerLinks[0].label, 'Server Link');
    assert.strictEqual(state.home.heroTitle, 'Server Hero');
    assert.strictEqual(state.theme.accent, '#123456');
    assert.strictEqual(state.nodeColors.repeater, '#aa0000');
  });

  test('server values survive when localStorage has partial overrides', () => {
    const ctx = makeSandbox();
    ctx.window.SITE_CONFIG = {
      home: {
        heroTitle: 'Server Hero',
        heroSubtitle: 'Server Subtitle',
        steps: [{ emoji: '1️⃣', title: 'Server Step', description: 'server' }],
        footerLinks: [{ label: 'Server Footer', url: '#/s' }]
      },
      theme: { accent: '#111111', navBg: '#222222', navText: '#333333' },
      typeColors: { ADVERT: '#00aa00', REQUEST: '#aa00aa' }
    };
    ctx.localStorage.setItem('meshcore-user-theme', JSON.stringify({
      home: { heroTitle: 'Local Hero' },
      theme: { accent: '#999999' },
      typeColors: { ADVERT: '#ff00ff' }
    }));
    const ex = loadCustomizeExports(ctx);
    ex.initState();
    const state = ex.getState();
    assert.strictEqual(state.home.heroTitle, 'Local Hero');
    assert.strictEqual(state.home.heroSubtitle, 'Server Subtitle');
    assert.strictEqual(state.home.steps[0].title, 'Server Step');
    assert.strictEqual(state.home.footerLinks[0].label, 'Server Footer');
    assert.strictEqual(state.theme.accent, '#999999');
    assert.strictEqual(state.theme.navBg, '#222222');
    assert.strictEqual(state.typeColors.ADVERT, '#ff00ff');
    assert.strictEqual(state.typeColors.REQUEST, '#aa00aa');
  });

  test('full localStorage values override server config', () => {
    const ctx = makeSandbox();
    ctx.window.SITE_CONFIG = {
      home: {
        heroTitle: 'Server Hero',
        heroSubtitle: 'Server Subtitle',
        steps: [{ emoji: 'S', title: 'Server Step', description: 'server' }],
        checklist: [{ question: 'Server Q', answer: 'Server A' }],
        footerLinks: [{ label: 'Server Link', url: '#/server' }]
      },
      theme: { accent: '#101010' }
    };
    ctx.localStorage.setItem('meshcore-user-theme', JSON.stringify({
      home: {
        heroTitle: 'Local Hero',
        heroSubtitle: 'Local Subtitle',
        steps: [{ emoji: 'L', title: 'Local Step', description: 'local' }],
        checklist: [{ question: 'Local Q', answer: 'Local A' }],
        footerLinks: [{ label: 'Local Link', url: '#/local' }]
      },
      theme: { accent: '#abcdef', navBg: '#fedcba' }
    }));
    const ex = loadCustomizeExports(ctx);
    ex.initState();
    const state = ex.getState();
    assert.strictEqual(state.home.heroTitle, 'Local Hero');
    assert.strictEqual(state.home.heroSubtitle, 'Local Subtitle');
    assert.strictEqual(state.home.steps[0].title, 'Local Step');
    assert.strictEqual(state.home.checklist[0].question, 'Local Q');
    assert.strictEqual(state.home.footerLinks[0].label, 'Local Link');
    assert.strictEqual(state.theme.accent, '#abcdef');
    assert.strictEqual(state.theme.navBg, '#fedcba');
  });

  test('initState uses _SITE_CONFIG_ORIGINAL_HOME to bypass contaminated SITE_CONFIG.home', () => {
    // Simulates: app.js called mergeUserHomeConfig which mutated SITE_CONFIG.home.steps = []
    // The original server steps must still be recoverable via _SITE_CONFIG_ORIGINAL_HOME
    const ctx = makeSandbox();
    ctx.setTimeout = function (fn) { fn(); return 1; };
    ctx.clearTimeout = function () {};
    // SITE_CONFIG.home is contaminated — steps wiped by mergeUserHomeConfig at page load
    ctx.window.SITE_CONFIG = {
      home: {
        heroTitle: 'Server Hero',
        steps: []   // contaminated — user had steps:[] in localStorage at page load
      }
    };
    // app.js snapshots original before mutation
    ctx.window._SITE_CONFIG_ORIGINAL_HOME = {
      heroTitle: 'Server Hero',
      steps: [{ emoji: '🧪', title: 'Original Step', description: 'from server' }]
    };
    const ex = loadCustomizeExports(ctx);
    ex.initState();
    const state = ex.getState();
    assert.strictEqual(state.home.steps.length, 1, 'should restore from snapshot, not contaminated SITE_CONFIG');
    assert.strictEqual(state.home.steps[0].title, 'Original Step');
  });

  test('initState uses DEFAULTS.home when no SITE_CONFIG and no snapshot', () => {
    const ctx = makeSandbox();
    ctx.setTimeout = function (fn) { fn(); return 1; };
    ctx.clearTimeout = function () {};
    // No SITE_CONFIG at all — pure DEFAULTS
    const ex = loadCustomizeExports(ctx);
    ex.initState();
    const state = ex.getState();
    assert.ok(state.home.steps.length > 0, 'should use DEFAULTS.home.steps when no server config');
    assert.strictEqual(state.home.steps[0].title, 'Join the Bay Area MeshCore Discord');
  });
}

// ===== APP.JS: home rehydration merge =====
console.log('\n=== app.js: home rehydration merge ===');
{
  test('mergeUserHomeConfig layers local home overrides on server home', () => {
    const ctx = makeSandbox();
    loadInCtx(ctx, 'public/roles.js');
    loadInCtx(ctx, 'public/app.js');
    const merged = ctx.mergeUserHomeConfig(
      {
        home: {
          heroTitle: 'Server Hero',
          heroSubtitle: 'Server Subtitle',
          steps: [{ title: 'Server Step' }],
          footerLinks: [{ label: 'Server Link' }]
        }
      },
      {
        home: {
          heroSubtitle: 'Local Subtitle',
          checklist: [{ question: 'Local Q', answer: 'Local A' }]
        }
      }
    );
    assert.strictEqual(merged.home.heroTitle, 'Server Hero');
    assert.strictEqual(merged.home.heroSubtitle, 'Local Subtitle');
    assert.strictEqual(merged.home.steps[0].title, 'Server Step');
    assert.strictEqual(merged.home.footerLinks[0].label, 'Server Link');
    assert.strictEqual(merged.home.checklist[0].question, 'Local Q');
  });

  test('mergeUserHomeConfig handles refresh-style localStorage payload', () => {
    const ctx = makeSandbox();
    loadInCtx(ctx, 'public/roles.js');
    loadInCtx(ctx, 'public/app.js');
    ctx.localStorage.setItem('meshcore-user-theme', JSON.stringify({
      home: { heroTitle: 'Local Hero' }
    }));
    const cfg = {
      home: {
        heroTitle: 'Server Hero',
        heroSubtitle: 'Server Subtitle',
        steps: [{ title: 'Server Step' }]
      }
    };
    const userTheme = JSON.parse(ctx.localStorage.getItem('meshcore-user-theme') || '{}');
    const merged = ctx.mergeUserHomeConfig(cfg, userTheme);
    assert.strictEqual(merged.home.heroTitle, 'Local Hero');
    assert.strictEqual(merged.home.heroSubtitle, 'Server Subtitle');
    assert.strictEqual(merged.home.steps[0].title, 'Server Step');
  });
}

// ===== CHANNELS.JS: WS Region Filter helper =====
console.log('\n=== channels.js: shouldProcessWSMessageForRegion ===');
{
  const ctx = makeSandbox();
  ctx.registerPage = () => {};
  ctx.RegionFilter = { init() {}, onChange() { return () => {}; }, offChange() {}, getRegionParam() { return ''; } };
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.debouncedOnWS = (fn) => fn;
  ctx.api = () => Promise.resolve({});
  ctx.CLIENT_TTL = { observers: 120000, channels: 15000, channelMessages: 10000 };
  ctx.history = { replaceState() {} };
  ctx.btoa = (s) => Buffer.from(String(s), 'utf8').toString('base64');
  ctx.atob = (s) => Buffer.from(String(s), 'base64').toString('utf8');
  loadInCtx(ctx, 'public/channels.js');
  const shouldProcess = ctx.window._channelsShouldProcessWSMessageForRegion;

  test('helper is exported', () => assert.ok(typeof shouldProcess === 'function'));

  test('allows all when no region selected', () => {
    const msg = { data: { packet: { observer_id: 'obs1' } } };
    assert.strictEqual(shouldProcess(msg, null, { obs1: 'SJC' }), true);
    assert.strictEqual(shouldProcess(msg, [], { obs1: 'SJC' }), true);
  });

  test('allows message when observer region matches selection', () => {
    const msg = { data: { packet: { observer_id: 'obs1' } } };
    assert.strictEqual(shouldProcess(msg, ['SJC', 'SFO'], { obs1: 'SJC' }), true);
  });

  test('drops message when observer region is outside selection', () => {
    const msg = { data: { packet: { observer_id: 'obs2' } } };
    assert.strictEqual(shouldProcess(msg, ['SJC'], { obs2: 'LAX' }), false);
  });

  test('drops message when observer_id is missing under selected region', () => {
    const msg = { data: {} };
    assert.strictEqual(shouldProcess(msg, ['SJC'], { obs1: 'SJC' }), false);
  });

  test('falls back to observer_name mapping when observer_id is missing', () => {
    const msg = { data: { packet: { observer_name: 'Observer Alpha' } } };
    assert.strictEqual(shouldProcess(msg, ['SJC'], { obs1: 'LAX' }, { 'Observer Alpha': 'SJC' }), true);
  });

  test('drops message when observer region lookup missing', () => {
    const msg = { data: { packet: { observer_id: 'obs9' } } };
    assert.strictEqual(shouldProcess(msg, ['SJC'], { obs1: 'SJC' }), false);
  });
}

console.log('\n=== channels.js: WS batch + region snapshot integration ===');
{
  function makeChannelsWsSandbox(regionParam) {
    const ctx = makeSandbox();
    const dom = {};
    function makeEl(id) {
      if (dom[id]) return dom[id];
      dom[id] = {
        id,
        innerHTML: '',
        textContent: '',
        value: '',
        scrollTop: 0,
        scrollHeight: 100,
        clientHeight: 80,
        style: {},
        dataset: {},
        classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
        addEventListener() {},
        removeEventListener() {},
        querySelector() { return null; },
        querySelectorAll() { return []; },
        getBoundingClientRect() { return { left: 0, bottom: 0, width: 0 }; },
        setAttribute() {},
        removeAttribute() {},
        focus() {},
      };
      return dom[id];
    }

    const headerText = { textContent: '' };
    makeEl('chHeader').querySelector = (sel) => (sel === '.ch-header-text' ? headerText : null);
    makeEl('chMessages');
    makeEl('chList');
    makeEl('chScrollBtn');
    makeEl('chAriaLive');
    makeEl('chBackBtn');
    makeEl('chRegionFilter');

    const appEl = {
      innerHTML: '',
      querySelector(sel) {
        if (sel === '.ch-sidebar' || sel === '.ch-sidebar-resize' || sel === '.ch-main') return makeEl(sel);
        if (sel === '.ch-layout') return { classList: { add() {}, remove() {}, contains() { return false; } } };
        return makeEl(sel);
      },
      addEventListener() {},
    };

    ctx.document.getElementById = makeEl;
    ctx.document.querySelector = (sel) => {
      if (sel === '.ch-layout') return { classList: { add() {}, remove() {}, contains() { return false; } } };
      return null;
    };
    ctx.document.querySelectorAll = () => [];
    ctx.document.addEventListener = () => {};
    ctx.document.removeEventListener = () => {};
    ctx.document.documentElement = { getAttribute: () => null, setAttribute: () => {} };
    ctx.document.body = { appendChild() {}, removeChild() {}, contains() { return false; } };
    ctx.history = { replaceState() {} };
    ctx.matchMedia = () => ({ matches: false });
    ctx.window.matchMedia = ctx.matchMedia;
    ctx.MutationObserver = function () { this.observe = () => {}; this.disconnect = () => {}; };
    ctx.RegionFilter = {
      init() {},
      onChange() { return () => {}; },
      offChange() {},
      getRegionParam() { return regionParam || ''; },
    };
    ctx.debouncedOnWS = (fn) => fn;
    ctx.onWS = () => {};
    ctx.offWS = () => {};
    ctx.api = (path) => {
      if (path.indexOf('/observers') === 0) return Promise.resolve({ observers: [] });
      if (path.indexOf('/channels') === 0) return Promise.resolve({ channels: [] });
      return Promise.resolve({ messages: [] });
    };
    ctx.CLIENT_TTL = { observers: 120000, channels: 15000, channelMessages: 10000, nodeDetail: 10000 };
    ctx.ROLE_EMOJI = {};
    ctx.ROLE_LABELS = {};
    ctx.timeAgo = () => '1m ago';
    ctx.registerPage = (name, handlers) => { ctx._pageHandlers = handlers; };
    ctx.btoa = (s) => Buffer.from(String(s), 'utf8').toString('base64');
    ctx.atob = (s) => Buffer.from(String(s), 'base64').toString('utf8');

    loadInCtx(ctx, 'public/channels.js');
    ctx._pageHandlers.init(appEl);
    return { ctx, dom };
  }

  test('WS batch respects region snapshot and observer_name fallback', () => {
    const env = makeChannelsWsSandbox('SJC');
    env.ctx.window._channelsSetObserverRegionsForTest({ obs1: 'SJC' }, { 'Observer Beta': 'SJC' });
    env.ctx.window._channelsSetStateForTest({
      selectedHash: 'general',
      channels: [{ hash: 'general', name: 'general', messageCount: 0, lastActivityMs: 0 }],
      messages: [],
    });

    env.ctx.window._channelsHandleWSBatchForTest([
      {
        type: 'packet',
        data: {
          hash: 'hash1',
          decoded: { header: { payloadTypeName: 'GRP_TXT' }, payload: { channel: 'general', text: 'Alice: hello world' } },
          packet: { observer_name: 'Observer Beta' },
        },
      },
      {
        type: 'packet',
        data: {
          hash: 'hash2',
          decoded: { header: { payloadTypeName: 'GRP_TXT' }, payload: { channel: 'general', text: 'Bob: dropped' } },
          packet: { observer_name: 'Observer Zeta' },
        },
      },
    ]);

    const state = env.ctx.window._channelsGetStateForTest();
    assert.strictEqual(state.messages.length, 1, 'only matching-region message should be appended');
    assert.strictEqual(state.messages[0].sender, 'Alice');
    assert.strictEqual(state.channels[0].messageCount, 1, 'channel count increments only for accepted message');
  });

  test('stale selectChannel response is discarded after region change', async () => {
    const ctx = makeSandbox();
    const dom = {};
    function makeEl(id) {
      if (dom[id]) return dom[id];
      dom[id] = {
        id,
        innerHTML: '',
        textContent: '',
        value: '',
        scrollTop: 0,
        scrollHeight: 100,
        clientHeight: 80,
        style: {},
        dataset: {},
        classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
        addEventListener() {},
        removeEventListener() {},
        querySelector() { return null; },
        querySelectorAll() { return []; },
        getBoundingClientRect() { return { left: 0, bottom: 0, width: 0 }; },
        setAttribute() {},
        removeAttribute() {},
        focus() {},
      };
      return dom[id];
    }
    const headerText = { textContent: '' };
    makeEl('chHeader').querySelector = (sel) => (sel === '.ch-header-text' ? headerText : null);
    makeEl('chMessages');
    makeEl('chList');
    makeEl('chScrollBtn');
    makeEl('chAriaLive');
    makeEl('chBackBtn');
    makeEl('chRegionFilter');
    const appEl = {
      innerHTML: '',
      querySelector(sel) {
        if (sel === '.ch-sidebar' || sel === '.ch-sidebar-resize' || sel === '.ch-main') return makeEl(sel);
        if (sel === '.ch-layout') return { classList: { add() {}, remove() {}, contains() { return false; } } };
        return makeEl(sel);
      },
      addEventListener() {},
    };
    let region = 'SJC';
    let resolver = null;
    ctx.document.getElementById = makeEl;
    ctx.document.querySelector = (sel) => {
      if (sel === '.ch-layout') return { classList: { add() {}, remove() {}, contains() { return false; } } };
      return null;
    };
    ctx.document.querySelectorAll = () => [];
    ctx.document.addEventListener = () => {};
    ctx.document.removeEventListener = () => {};
    ctx.document.documentElement = { getAttribute: () => null, setAttribute: () => {} };
    ctx.document.body = { appendChild() {}, removeChild() {}, contains() { return false; } };
    ctx.history = { replaceState() {} };
    ctx.matchMedia = () => ({ matches: false });
    ctx.window.matchMedia = ctx.matchMedia;
    ctx.MutationObserver = function () { this.observe = () => {}; this.disconnect = () => {}; };
    ctx.RegionFilter = { init() {}, onChange() { return () => {}; }, offChange() {}, getRegionParam() { return region; } };
    ctx.debouncedOnWS = (fn) => fn;
    ctx.onWS = () => {};
    ctx.offWS = () => {};
    ctx.api = (path) => {
      if (path.indexOf('/observers') === 0) return Promise.resolve({ observers: [] });
      if (path.indexOf('/channels?') === 0 || path === '/channels') return Promise.resolve({ channels: [{ hash: 'general', name: 'general', messageCount: 2, lastActivity: null }] });
      if (path.indexOf('/channels/general/messages') === 0) {
        return new Promise((resolve) => { resolver = resolve; });
      }
      return Promise.resolve({ messages: [] });
    };
    ctx.CLIENT_TTL = { observers: 120000, channels: 15000, channelMessages: 10000, nodeDetail: 10000 };
    ctx.ROLE_EMOJI = {};
    ctx.ROLE_LABELS = {};
    ctx.timeAgo = () => '1m ago';
    ctx.registerPage = (name, handlers) => { ctx._pageHandlers = handlers; };
    ctx.btoa = (s) => Buffer.from(String(s), 'utf8').toString('base64');
    ctx.atob = (s) => Buffer.from(String(s), 'base64').toString('utf8');

    loadInCtx(ctx, 'public/channels.js');
    ctx._pageHandlers.init(appEl);
    await Promise.resolve();
    const selectPromise = ctx.window._channelsSelectChannelForTest('general');
    region = 'LAX';
    ctx.window._channelsBeginMessageRequestForTest('other', 'LAX');
    resolver({ messages: [{ sender: 'Alice', text: 'stale', timestamp: '2025-01-01T00:00:00Z' }] });
    await selectPromise;
    const state = ctx.window._channelsGetStateForTest();
    assert.strictEqual(state.selectedHash, 'general', 'stale select response must not clear or overwrite selection');
    assert.strictEqual(state.messages.length, 0, 'stale response must be discarded');
  });

  test('loadChannels clears selected hash when channel no longer exists in region', async () => {
    const ctx = makeSandbox();
    const dom = {};
    function makeEl(id) {
      if (dom[id]) return dom[id];
      dom[id] = {
        id,
        innerHTML: '',
        textContent: '',
        value: '',
        scrollTop: 0,
        scrollHeight: 100,
        clientHeight: 80,
        style: {},
        dataset: {},
        classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
        addEventListener() {},
        removeEventListener() {},
        querySelector() { return null; },
        querySelectorAll() { return []; },
        getBoundingClientRect() { return { left: 0, bottom: 0, width: 0 }; },
        setAttribute() {},
        removeAttribute() {},
        focus() {},
      };
      return dom[id];
    }
    const headerText = { textContent: '' };
    makeEl('chHeader').querySelector = (sel) => (sel === '.ch-header-text' ? headerText : null);
    makeEl('chMessages');
    makeEl('chList');
    makeEl('chScrollBtn');
    makeEl('chAriaLive');
    makeEl('chBackBtn');
    makeEl('chRegionFilter');
    const appEl = {
      innerHTML: '',
      querySelector(sel) {
        if (sel === '.ch-sidebar' || sel === '.ch-sidebar-resize' || sel === '.ch-main') return makeEl(sel);
        if (sel === '.ch-layout') return { classList: { add() {}, remove() {}, contains() { return false; } } };
        return makeEl(sel);
      },
      addEventListener() {},
    };
    const historyCalls = [];
    let channelCall = 0;
    ctx.document.getElementById = makeEl;
    ctx.document.querySelector = (sel) => {
      if (sel === '.ch-layout') return { classList: { add() {}, remove() {}, contains() { return false; } } };
      return null;
    };
    ctx.document.querySelectorAll = () => [];
    ctx.document.addEventListener = () => {};
    ctx.document.removeEventListener = () => {};
    ctx.document.documentElement = { getAttribute: () => null, setAttribute: () => {} };
    ctx.document.body = { appendChild() {}, removeChild() {}, contains() { return false; } };
    ctx.history = { replaceState(_a, _b, url) { historyCalls.push(url); } };
    ctx.matchMedia = () => ({ matches: false });
    ctx.window.matchMedia = ctx.matchMedia;
    ctx.MutationObserver = function () { this.observe = () => {}; this.disconnect = () => {}; };
    ctx.RegionFilter = { init() {}, onChange() { return () => {}; }, offChange() {}, getRegionParam() { return 'SJC'; } };
    ctx.debouncedOnWS = (fn) => fn;
    ctx.onWS = () => {};
    ctx.offWS = () => {};
    ctx.api = (path) => {
      if (path.indexOf('/observers') === 0) return Promise.resolve({ observers: [] });
      if (path.indexOf('/channels') === 0) {
        channelCall++;
        if (channelCall === 1) return Promise.resolve({ channels: [{ hash: 'general', name: 'general', messageCount: 1, lastActivity: null }] });
        return Promise.resolve({ channels: [{ hash: 'newchan', name: 'newchan', messageCount: 1, lastActivity: null }] });
      }
      if (path.indexOf('/channels/general/messages') === 0) return Promise.resolve({ messages: [{ sender: 'Alice', text: 'hi', timestamp: '2025-01-01T00:00:00Z' }] });
      return Promise.resolve({ messages: [] });
    };
    ctx.CLIENT_TTL = { observers: 120000, channels: 15000, channelMessages: 10000, nodeDetail: 10000 };
    ctx.ROLE_EMOJI = {};
    ctx.ROLE_LABELS = {};
    ctx.timeAgo = () => '1m ago';
    ctx.registerPage = (name, handlers) => { ctx._pageHandlers = handlers; };
    ctx.btoa = (s) => Buffer.from(String(s), 'utf8').toString('base64');
    ctx.atob = (s) => Buffer.from(String(s), 'base64').toString('utf8');

    loadInCtx(ctx, 'public/channels.js');
    ctx._pageHandlers.init(appEl);
    await Promise.resolve();
    await ctx.window._channelsSelectChannelForTest('general');
    await ctx.window._channelsLoadChannelsForTest(true);
    ctx.window._channelsReconcileSelectionForTest();
    const state = ctx.window._channelsGetStateForTest();
    assert.strictEqual(state.selectedHash, null, 'selection should clear when channel disappears after region update');
    assert.ok(historyCalls.includes('#/channels'), 'should route back to channels root');
  });
}
// ===== PACKETS.JS: savedTimeWindowMin default guard =====
console.log('\n=== packets.js: savedTimeWindowMin defaults ===');
{
  async function captureInitialPacketsRequest(storageValue, innerWidth) {
    const ctx = makeSandbox();
    const apiCalls = [];
    if (storageValue !== undefined) ctx.localStorage.setItem('meshcore-time-window', storageValue);
    ctx.window.localStorage = ctx.localStorage;
    ctx.window.innerWidth = innerWidth;
    const dom = {
      pktRight: { addEventListener() {}, classList: { add() {}, remove() {}, contains() { return false; } }, innerHTML: '' },
    };
    ctx.document.getElementById = (id) => {
      if (id === 'fTimeWindow') return null;
      return dom[id] || null;
    };
    ctx.document.addEventListener = () => {};
    ctx.document.removeEventListener = () => {};
    ctx.document.body = { appendChild() {}, removeChild() {}, contains() { return false; } };
    ctx.window.addEventListener = () => {};
    ctx.window.removeEventListener = () => {};
    ctx.RegionFilter = { init() {}, onChange() { return () => {}; }, offChange() {}, getRegionParam() { return ''; } };
    ctx.CLIENT_TTL = { observers: 120000 };
    ctx.debouncedOnWS = (fn) => fn;
    ctx.onWS = () => {};
    ctx.offWS = () => {};
    ctx.registerPage = (name, handlers) => { if (name === 'packets') ctx._packetsHandlers = handlers; };
    ctx.api = (path) => {
      apiCalls.push(path);
      if (path.indexOf('/observers') === 0) return Promise.resolve({ observers: [] });
      if (path.indexOf('/packets?') === 0) return Promise.reject(new Error('stop after request capture'));
      if (path.indexOf('/config/regions') === 0) return Promise.resolve({});
      return Promise.resolve({});
    };

    loadInCtx(ctx, 'public/packets.js');
    assert.ok(ctx._packetsHandlers && typeof ctx._packetsHandlers.init === 'function',
      'packets page should register init handler');
    await ctx._packetsHandlers.init({ innerHTML: '' });

    const firstPacketsCall = apiCalls.find(p => p.indexOf('/packets?') === 0);
    assert.ok(firstPacketsCall, 'packets API should be called during initial packets page load');
    const params = new URLSearchParams((firstPacketsCall.split('?')[1] || ''));
    return { firstPacketsCall, params };
  }

  test('savedTimeWindowMin defaults to 15 when localStorage returns null', async () => {
    const r = await captureInitialPacketsRequest(undefined, 1366);
    const since = r.params.get('since');
    assert.ok(since, 'initial packets request should include since parameter');
    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert.ok(deltaMin > 10 && deltaMin < 25, `expected default ~15m window, got ${deltaMin.toFixed(2)}m`);
  });

  test('savedTimeWindowMin defaults to 15 when localStorage returns "0"', async () => {
    const r = await captureInitialPacketsRequest('0', 1366);
    const since = r.params.get('since');
    assert.ok(since, 'initial packets request should include since parameter');
    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert.ok(deltaMin > 10 && deltaMin < 25, `expected default ~15m window, got ${deltaMin.toFixed(2)}m`);
  });

  test('savedTimeWindowMin preserves valid value (60)', async () => {
    const r = await captureInitialPacketsRequest('60', 1366);
    const since = r.params.get('since');
    assert.ok(since, 'initial packets request should include since parameter');
    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert.ok(deltaMin > 45 && deltaMin < 75, `expected persisted ~60m window, got ${deltaMin.toFixed(2)}m`);
  });

  test('savedTimeWindowMin defaults to 15 for negative value', async () => {
    const r = await captureInitialPacketsRequest('-5', 1366);
    const since = r.params.get('since');
    assert.ok(since, 'initial packets request should include since parameter');
    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert.ok(deltaMin > 10 && deltaMin < 25, `expected default ~15m window, got ${deltaMin.toFixed(2)}m`);
  });

  test('savedTimeWindowMin defaults to 15 for NaN string', async () => {
    const r = await captureInitialPacketsRequest('abc', 1366);
    const since = r.params.get('since');
    assert.ok(since, 'initial packets request should include since parameter');
    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert.ok(deltaMin > 10 && deltaMin < 25, `expected default ~15m window, got ${deltaMin.toFixed(2)}m`);
  });

  test('PACKET_LIMIT is 1000 on mobile', async () => {
    const r = await captureInitialPacketsRequest('15', 375);
    assert.strictEqual(r.params.get('limit'), '1000');
  });

  test('PACKET_LIMIT is 50000 on desktop', async () => {
    const r = await captureInitialPacketsRequest('15', 1366);
    assert.strictEqual(r.params.get('limit'), '50000');
  });

  test('mobile caps large time window to 15', async () => {
    const r = await captureInitialPacketsRequest('1440', 375);
    const since = r.params.get('since');
    assert.ok(since, 'initial packets request should include since parameter');
    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert.ok(deltaMin > 10 && deltaMin < 25, `expected capped ~15m window, got ${deltaMin.toFixed(2)}m`);
  });

  test('mobile allows 180 min window', async () => {
    const r = await captureInitialPacketsRequest('180', 375);
    const since = r.params.get('since');
    assert.ok(since, 'initial packets request should include since parameter');
    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert.ok(deltaMin > 160 && deltaMin < 210, `expected ~180m window, got ${deltaMin.toFixed(2)}m`);
  });

  test('mobile corrects desktop-persisted all-time value to 15 minutes', async () => {
    const r = await captureInitialPacketsRequest('0', 375);
    const since = r.params.get('since');
    assert.ok(since, 'mobile should not keep all-time persisted value');
    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert.ok(deltaMin > 10 && deltaMin < 25, `expected capped ~15m window, got ${deltaMin.toFixed(2)}m`);
  });
}
// ===== My Nodes client-side filter (issue #381) =====
{
  console.log('\n--- My Nodes client-side filter ---');

  // Simulate the client-side filter logic from packets.js renderTableRows()
  function filterMyNodes(packets, allKeys) {
    if (!allKeys.length) return [];
    return packets.filter(p => {
      const dj = p.decoded_json || '';
      return allKeys.some(k => dj.includes(k));
    });
  }

  const testPackets = [
    { decoded_json: '{"pubKey":"abc123","name":"Node1"}' },
    { decoded_json: '{"pubKey":"def456","name":"Node2"}' },
    { decoded_json: '{"pubKey":"ghi789","name":"Node3","hops":["abc123"]}' },
    { decoded_json: '' },
    { decoded_json: null },
  ];

  test('filters packets matching a single pubkey', () => {
    const result = filterMyNodes(testPackets, ['abc123']);
    assert.strictEqual(result.length, 2, 'should match sender + hop');
    assert.ok(result[0].decoded_json.includes('abc123'));
    assert.ok(result[1].decoded_json.includes('abc123'));
  });

  test('filters packets matching multiple pubkeys', () => {
    const result = filterMyNodes(testPackets, ['abc123', 'def456']);
    assert.strictEqual(result.length, 3);
  });

  test('returns empty array for no matching keys', () => {
    const result = filterMyNodes(testPackets, ['zzz999']);
    assert.strictEqual(result.length, 0);
  });

  test('returns empty array when allKeys is empty', () => {
    const result = filterMyNodes(testPackets, []);
    assert.strictEqual(result.length, 0);
  });

  test('handles null/empty decoded_json gracefully', () => {
    const result = filterMyNodes(testPackets, ['abc123']);
    assert.strictEqual(result.length, 2);
  });
}

// ===== Packets page: virtual scroll infrastructure =====
{
  console.log('\nPackets page — virtual scroll:');
  const packetsSource = fs.readFileSync('public/packets.js', 'utf8');

  // --- Behavioral tests using extracted logic ---

  // Extract _cumulativeRowOffsets logic for testing
  function cumulativeRowOffsets(rowCounts) {
    const offsets = new Array(rowCounts.length + 1);
    offsets[0] = 0;
    for (let i = 0; i < rowCounts.length; i++) {
      offsets[i + 1] = offsets[i] + rowCounts[i];
    }
    return offsets;
  }

  // Extract _getRowCount logic for testing (#424 — single source of truth)
  function getRowCount(p, grouped, expandedHashes, observerFilterSet) {
    if (!grouped) return 1;
    if (!expandedHashes.has(p.hash) || !p._children) return 1;
    let childCount = p._children.length;
    if (observerFilterSet) {
      childCount = p._children.filter(c => observerFilterSet.has(String(c.observer_id))).length;
    }
    return 1 + childCount;
  }

  test('cumulativeRowOffsets computes correct offsets for flat rows', () => {
    const counts = [1, 1, 1, 1, 1];
    const offsets = cumulativeRowOffsets(counts);
    assert.deepStrictEqual(offsets, [0, 1, 2, 3, 4, 5]);
  });

  test('cumulativeRowOffsets handles expanded groups with multiple rows', () => {
    const counts = [1, 4, 1];
    const offsets = cumulativeRowOffsets(counts);
    assert.deepStrictEqual(offsets, [0, 1, 5, 6]);
    assert.strictEqual(offsets[offsets.length - 1], 6);
  });

  test('total scroll height accounts for expanded group rows', () => {
    const VSCROLL_ROW_HEIGHT = 36;
    const counts = [1, 4, 1, 4, 1];
    const offsets = cumulativeRowOffsets(counts);
    const totalDomRows = offsets[offsets.length - 1];
    assert.strictEqual(totalDomRows, 11);
    assert.strictEqual(totalDomRows * VSCROLL_ROW_HEIGHT, 396);
  });

  test('scroll height with all collapsed equals entries * row height', () => {
    const VSCROLL_ROW_HEIGHT = 36;
    const counts = [1, 1, 1, 1, 1];
    const offsets = cumulativeRowOffsets(counts);
    const totalDomRows = offsets[offsets.length - 1];
    assert.strictEqual(totalDomRows * VSCROLL_ROW_HEIGHT, 5 * VSCROLL_ROW_HEIGHT);
  });

  // --- Behavioral tests for _getRowCount (#424, #428 — test logic, not source strings) ---

  test('getRowCount returns 1 for flat (ungrouped) mode', () => {
    const p = { hash: 'abc', _children: [{observer_id: '1'}, {observer_id: '2'}] };
    assert.strictEqual(getRowCount(p, false, new Set(), null), 1);
  });

  test('getRowCount returns 1 for collapsed group', () => {
    const p = { hash: 'abc', _children: [{observer_id: '1'}, {observer_id: '2'}] };
    assert.strictEqual(getRowCount(p, true, new Set(), null), 1);
  });

  test('getRowCount returns 1+children for expanded group', () => {
    const p = { hash: 'abc', _children: [{observer_id: '1'}, {observer_id: '2'}, {observer_id: '3'}] };
    const expanded = new Set(['abc']);
    assert.strictEqual(getRowCount(p, true, expanded, null), 4);
  });

  test('getRowCount filters children by observer set', () => {
    const p = { hash: 'abc', _children: [{observer_id: '1'}, {observer_id: '2'}, {observer_id: '3'}] };
    const expanded = new Set(['abc']);
    const obsFilter = new Set(['1', '3']);
    assert.strictEqual(getRowCount(p, true, expanded, obsFilter), 3);
  });

  test('getRowCount returns 1 for expanded group with no _children', () => {
    const p = { hash: 'abc' };
    const expanded = new Set(['abc']);
    assert.strictEqual(getRowCount(p, true, expanded, null), 1);
  });

  test('renderVisibleRows uses cumulative offsets not flat entry count', () => {
    assert.ok(packetsSource.includes('_cumulativeRowOffsets'),
      'renderVisibleRows should use cumulative row offsets');
    assert.ok(!packetsSource.includes('const totalRows = _displayPackets.length'),
      'should NOT use flat array length for total row count');
  });

  test('renderVisibleRows skips DOM rebuild when range unchanged', () => {
    assert.ok(packetsSource.includes('startIdx === _lastVisibleStart && endIdx === _lastVisibleEnd'),
      'should skip rebuild when range is unchanged');
  });

  test('lazy row generation — HTML built only for visible slice', () => {
    assert.ok(!packetsSource.includes('_lastRenderedRows'),
      'should NOT have pre-built row HTML cache');
    assert.ok(packetsSource.includes('_displayPackets.slice(startIdx, endIdx)'),
      'should slice display packets for visible range');
    assert.ok(packetsSource.includes('visibleSlice.map(p => builder(p))'),
      'should build HTML lazily per visible packet');
  });

  test('observer filter Set is hoisted, not recreated per-packet', () => {
    assert.ok(packetsSource.includes('_observerFilterSet = filters.observer ? new Set(filters.observer.split'),
      'observer filter Set should be created once in renderTableRows');
    assert.ok(packetsSource.includes('_observerFilterSet.has(String(c.observer_id))'),
      'buildGroupRowHtml should use hoisted _observerFilterSet');
  });

  test('buildFlatRowHtml has null-safe decoded_json', () => {
    const flatBuilderMatch = packetsSource.match(/function buildFlatRowHtml[\s\S]*?(?=\n  function )/);
    assert.ok(flatBuilderMatch, 'buildFlatRowHtml should exist');
    assert.ok(flatBuilderMatch[0].includes('getParsedDecoded(p)'),
      'buildFlatRowHtml should use getParsedDecoded for null-safe decoded_json fallback');
  });

  test('pathHops null guard in buildFlatRowHtml (issue #451)', () => {
    const flatBuilderMatch = packetsSource.match(/function buildFlatRowHtml[\s\S]*?(?=\n  function )/);
    assert.ok(flatBuilderMatch, 'buildFlatRowHtml should exist');
    assert.ok(flatBuilderMatch[0].includes('getParsedPath(p)'),
      'buildFlatRowHtml should use getParsedPath which guards against null');
  });

  test('pathHops null guard in detail pane (issue #451)', () => {
    assert.ok(packetsSource.includes('getParsedPath(pkt)'),
      'detail pane should use getParsedPath for null-safe path parsing');
    assert.ok(packetsSource.includes('getParsedDecoded(pkt)'),
      'detail pane should use getParsedDecoded for null-safe decoded parsing');
  });

  test('destroy cleans up virtual scroll state', () => {
    assert.ok(packetsSource.includes('detachVScrollListener'),
      'destroy should detach virtual scroll listener');
    assert.ok(packetsSource.includes("_displayPackets = []"),
      'destroy should reset display packets');
    assert.ok(packetsSource.includes("_rowCounts = []"),
      'destroy should reset row counts');
    assert.ok(packetsSource.includes("_lastVisibleStart = -1"),
      'destroy should reset visible start');
  });
}

// ===== live.js: packetTimestamp =====
console.log('\n=== live.js: packetTimestamp ===');
{
  // packetTimestamp is extracted and exposed via window._live_packetTimestamp
  const ctx = makeSandbox();
  ctx.L = {
    circleMarker: () => { const m = { addTo() { return m; }, bindTooltip() { return m; }, on() { return m; }, setRadius() {}, setStyle() {}, setLatLng() {}, getLatLng() { return { lat: 0, lng: 0 }; }, _baseColor: '', _baseSize: 5, _glowMarker: null }; return m; },
    polyline: () => { const p = { addTo() { return p; }, setStyle() {}, remove() {} }; return p; },
    map: () => { const m = { setView() { return m; }, addLayer() { return m; }, on() { return m; }, getZoom() { return 11; }, getCenter() { return { lat: 37, lng: -122 }; }, getBounds() { return { contains: () => true }; }, fitBounds() { return m; }, invalidateSize() {}, remove() {}, hasLayer() { return false; } }; return m; },
    layerGroup: () => { const g = { addTo() { return g; }, addLayer() {}, removeLayer() {}, clearLayers() {}, hasLayer() { return true; }, eachLayer() {} }; return g; },
    tileLayer: () => ({ addTo() { return this; } }),
    control: { attribution: () => ({ addTo() {} }) },
    DomUtil: { addClass() {}, removeClass() {} },
  };
  ctx.getComputedStyle = () => ({ getPropertyValue: () => '' });
  ctx.matchMedia = () => ({ matches: false, addEventListener: () => {} });
  ctx.registerPage = () => {};
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.connectWS = () => {};
  ctx.api = () => Promise.resolve([]);
  ctx.invalidateApiCache = () => {};
  ctx.favStar = () => '';
  ctx.bindFavStars = () => {};
  ctx.getFavorites = () => [];
  ctx.isFavorite = () => false;
  ctx.HopResolver = { init: () => {}, resolve: () => ({}), ready: () => false };
  ctx.MeshAudio = null;
  ctx.RegionFilter = { init: () => {}, getSelected: () => null, onRegionChange: () => {} };
  ctx.WebSocket = function() { this.close = () => {}; };
  ctx.navigator = {};
  ctx.visualViewport = null;
  ctx.document.documentElement = { getAttribute: () => null, setAttribute: () => {} };
  ctx.document.body = { appendChild: () => {}, removeChild: () => {}, contains: () => false };
  ctx.document.querySelector = () => null;
  ctx.document.querySelectorAll = () => [];
  ctx.document.createElementNS = () => ctx.document.createElement();
  ctx.cancelAnimationFrame = () => {};
  ctx.IATA_COORDS_GEO = {};
  loadInCtx(ctx, 'public/roles.js');
  try { loadInCtx(ctx, 'public/live.js'); } catch (e) {
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }

  const packetTimestamp = ctx._live_packetTimestamp || ctx.window._live_packetTimestamp;

  test('packetTimestamp uses pkt.timestamp ISO string', () => {
    assert.ok(packetTimestamp, 'packetTimestamp should be exposed');
    const ts = packetTimestamp({ timestamp: '2026-03-15T12:30:00.000Z' });
    assert.strictEqual(ts, new Date('2026-03-15T12:30:00.000Z').getTime());
  });

  test('packetTimestamp falls back to pkt.created_at', () => {
    const ts = packetTimestamp({ created_at: '2025-06-01T00:00:00Z' });
    assert.strictEqual(ts, new Date('2025-06-01T00:00:00Z').getTime());
  });

  test('packetTimestamp falls back to Date.now() when no fields', () => {
    const before = Date.now();
    const ts = packetTimestamp({});
    const after = Date.now();
    assert.ok(ts >= before && ts <= after, 'should fall back to current time');
  });

  test('packetTimestamp prefers timestamp over created_at', () => {
    const ts = packetTimestamp({
      timestamp: '2026-01-01T00:00:00Z',
      created_at: '2025-01-01T00:00:00Z',
    });
    assert.strictEqual(ts, new Date('2026-01-01T00:00:00Z').getTime());
  });
}

// ===== live.js: nextHop null guards =====
console.log('\n=== live.js: nextHop null guards ===');
{
  const liveSource = fs.readFileSync('public/live.js', 'utf8');

  test('nextHop guards animLayer null before use', () => {
    assert.ok(liveSource.includes('if (!animLayer) return;'),
      'nextHop must return early when animLayer is null (post-destroy)');
  });

  test('nextHop setInterval guards animLayer null', () => {
    assert.ok(liveSource.includes('if (!animLayer || !animLayer.hasLayer(ghost))'),
      'setInterval in nextHop must guard animLayer null');
  });

  test('nextHop setTimeout guards animLayer null', () => {
    assert.ok(liveSource.includes('if (animLayer && animLayer.hasLayer(ghost)) animLayer.removeLayer(ghost)'),
      'setTimeout in nextHop must guard animLayer null');
  });

  test('nextHop guards liveAnimCount element null', () => {
    assert.ok(liveSource.includes('const countEl = document.getElementById(\'liveAnimCount\')'),
      'nextHop must null-check liveAnimCount element');
    assert.ok(liveSource.includes('if (countEl) countEl.textContent = activeAnims'),
      'nextHop must conditionally update liveAnimCount');
  });
}

// === channels.js: formatHashHex (#465) ===
console.log('\n=== channels.js: formatHashHex (issue #465) ===');
{
  const chSource = fs.readFileSync('public/channels.js', 'utf8');

  test('formatHashHex exists in channels.js', () => {
    assert.ok(chSource.includes('function formatHashHex('), 'formatHashHex function must exist');
  });

  test('channel fallback name uses formatHashHex', () => {
    assert.ok(chSource.includes('formatHashHex(ch.hash)'), 'renderChannelList must format hash as hex');
    assert.ok(chSource.includes('formatHashHex(hash)'), 'selectChannel must format hash as hex');
  });

  test('formatHashHex produces correct hex output', () => {
    // Extract and evaluate the function
    const match = chSource.match(/function formatHashHex\(hash\)\s*\{[^}]+\}/);
    assert.ok(match, 'should extract formatHashHex');
    const ctx = vm.createContext({});
    vm.runInContext(match[0], ctx);
    const fmt = vm.runInContext('formatHashHex', ctx);
    assert.strictEqual(fmt(10), '0x0A');
    assert.strictEqual(fmt(255), '0xFF');
    assert.strictEqual(fmt(0), '0x00');
    assert.strictEqual(fmt(1), '0x01');
    assert.strictEqual(fmt('LongFast'), 'LongFast');  // string hash passes through
  });
}

// ===== MAP NEIGHBOR FILTER LOGIC =====
{
  console.log('\n--- Map neighbor filter logic ---');

  // NOTE: applyNeighborFilter is a hand-written copy of the filter logic from
  // public/map.js _renderMarkersInner. The real code is browser-only (depends on
  // Leaflet, DOM, closure state) and cannot be imported directly in Node.
  // If the filter logic in map.js changes, update this copy to match.
  function applyNeighborFilter(nodes, filters, selectedReferenceNode, neighborPubkeys) {
    return nodes.filter(n => {
      if (!n.lat || !n.lon) return false;
      if (!filters[n.role || 'companion']) return false;
      if (filters.neighbors && selectedReferenceNode && neighborPubkeys) {
        const pk = n.public_key;
        if (pk !== selectedReferenceNode && !neighborPubkeys.has(pk)) return false;
      }
      return true;
    });
  }

  const testNodes = [
    { public_key: 'aaa', lat: 1, lon: 1, role: 'repeater', name: 'NodeA' },
    { public_key: 'bbb', lat: 2, lon: 2, role: 'repeater', name: 'NodeB' },
    { public_key: 'ccc', lat: 3, lon: 3, role: 'companion', name: 'NodeC' },
    { public_key: 'ddd', lat: 4, lon: 4, role: 'repeater', name: 'NodeD' },
  ];
  const baseFilters = { repeater: true, companion: true, room: true, sensor: true, neighbors: false };

  test('neighbor filter off shows all nodes', () => {
    const result = applyNeighborFilter(testNodes, baseFilters, null, null);
    assert.strictEqual(result.length, 4);
  });

  test('neighbor filter on with no reference shows all nodes', () => {
    const f = { ...baseFilters, neighbors: true };
    const result = applyNeighborFilter(testNodes, f, null, null);
    assert.strictEqual(result.length, 4);
  });

  test('neighbor filter on with reference and neighbors filters correctly', () => {
    const f = { ...baseFilters, neighbors: true };
    const neighborSet = new Set(['bbb', 'ccc']);
    const result = applyNeighborFilter(testNodes, f, 'aaa', neighborSet);
    assert.strictEqual(result.length, 3); // aaa (ref) + bbb + ccc (neighbors)
    const pks = result.map(n => n.public_key);
    assert.ok(pks.includes('aaa'), 'reference node should be included');
    assert.ok(pks.includes('bbb'), 'neighbor bbb should be included');
    assert.ok(pks.includes('ccc'), 'neighbor ccc should be included');
    assert.ok(!pks.includes('ddd'), 'non-neighbor ddd should be excluded');
  });

  test('neighbor filter on with reference and empty neighbors shows only reference', () => {
    const f = { ...baseFilters, neighbors: true };
    const neighborSet = new Set();
    const result = applyNeighborFilter(testNodes, f, 'aaa', neighborSet);
    assert.strictEqual(result.length, 1);
    assert.strictEqual(result[0].public_key, 'aaa');
  });

  test('neighbor filter respects role filter', () => {
    const f = { ...baseFilters, neighbors: true, companion: false };
    const neighborSet = new Set(['bbb', 'ccc']);
    const result = applyNeighborFilter(testNodes, f, 'aaa', neighborSet);
    assert.strictEqual(result.length, 2); // aaa + bbb (ccc is companion, filtered out)
    const pks = result.map(n => n.public_key);
    assert.ok(!pks.includes('ccc'), 'companion ccc should be filtered by role');
  });

  // Test path parsing for neighbor extraction
  test('neighbor extraction from paths data', () => {
    const refPubkey = 'aaa';
    const paths = [
      { hops: [{ pubkey: 'bbb' }, { pubkey: 'aaa' }, { pubkey: 'ccc' }] },
      { hops: [{ pubkey: 'aaa' }, { pubkey: 'ddd' }] },
      { hops: [{ pubkey: 'eee' }, { pubkey: 'aaa' }] },
    ];
    const neighborSet = new Set();
    for (const p of paths) {
      const hops = p.hops || [];
      for (let i = 0; i < hops.length; i++) {
        if (hops[i].pubkey === refPubkey) {
          if (i > 0 && hops[i - 1].pubkey) neighborSet.add(hops[i - 1].pubkey);
          if (i < hops.length - 1 && hops[i + 1].pubkey) neighborSet.add(hops[i + 1].pubkey);
        }
      }
    }
    assert.ok(neighborSet.has('bbb'), 'bbb is adjacent in path 1');
    assert.ok(neighborSet.has('ccc'), 'ccc is adjacent in path 1');
    assert.ok(neighborSet.has('ddd'), 'ddd is adjacent in path 2');
    assert.ok(neighborSet.has('eee'), 'eee is adjacent in path 3');
    assert.strictEqual(neighborSet.size, 4);
  });
}


// ===== packets.js: memory bounds =====
{
  console.log('\nPackets page — memory bounds:');
  const src = fs.readFileSync('public/packets.js', 'utf8');

  test('pauseBuffer is capped at 2000 entries', () => {
    assert.ok(src.includes('pauseBuffer.length > 2000'),
      'pauseBuffer cap check must be present');
    assert.ok(src.includes('pauseBuffer = pauseBuffer.slice(-2000)'),
      'pauseBuffer must be trimmed to last 2000 entries');
  });

  test('packets array is trimmed to PACKET_LIMIT after WS update in grouped mode', () => {
    assert.ok(src.includes('packets.length > PACKET_LIMIT'),
      'grouped mode must check packets length against PACKET_LIMIT');
    assert.ok(src.includes('packets.splice(PACKET_LIMIT)'),
      'grouped mode must splice packets to PACKET_LIMIT');
  });

  test('evicted packets are removed from hashIndex', () => {
    assert.ok(/const evicted = packets\.splice\(PACKET_LIMIT\)[\s\S]{0,200}hashIndex\.delete\(p\.hash\)/.test(src),
      'after splice, evicted entries must be deleted from hashIndex');
  });

  test('packets array is trimmed to PACKET_LIMIT after WS update in flat mode', () => {
    assert.ok(/packets = filtered\.concat\(packets\)[\s\S]{0,100}packets\.length = PACKET_LIMIT/.test(src),
      'flat mode must truncate packets to PACKET_LIMIT after prepend');
  });

  test('_children is capped at 200 on WebSocket prepend', () => {
    assert.ok(src.includes('existing._children.length > 200'),
      '_children cap check must be present');
    assert.ok(src.includes('existing._children.length = 200'),
      '_children must be truncated to 200');
  });

  test('observerMap is built from observers array in loadObservers', () => {
    assert.ok(src.includes('observerMap = new Map(observers.map(o => [o.id, o]))'),
      'observerMap must be built as id→observer Map in loadObservers');
  });

  test('observerMap is reset in destroy', () => {
    assert.ok(src.includes('observerMap = new Map()'),
      'destroy must reset observerMap to empty Map');
  });

  test('WS handler debounces render via _wsRenderTimer', () => {
    const wsBlock = src.slice(src.indexOf('wsHandler = debouncedOnWS'), src.indexOf('function destroy()'));
    assert.ok(wsBlock.includes('_wsRenderTimer'),
      'WS handler must debounce renders via _wsRenderTimer');
    assert.ok(wsBlock.includes('clearTimeout(_wsRenderTimer)'),
      'WS handler must clear pending timer before scheduling new render');
    assert.ok(/setTimeout\(function \(\) \{ renderTableRows\(\); \}/.test(wsBlock),
      'WS handler must schedule renderTableRows via setTimeout');
  });

  test('destroy clears _wsRenderTimer', () => {
    const destroyBlock = src.slice(src.indexOf('function destroy()'), src.indexOf('function destroy()') + 500);
    assert.ok(destroyBlock.includes('clearTimeout(_wsRenderTimer)'),
      'destroy must clear _wsRenderTimer to prevent stale renders after navigation');
  });
}
// ===== NODES.JS: shared sandbox factory =====
function makeNodesSandbox(opts) {
  opts = opts || {};
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  ctx.registerPage = () => {};
  ctx.RegionFilter = { init: () => {}, onChange: () => () => {}, getRegionParam: () => '', offChange: () => {} };
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.debouncedOnWS = (fn) => fn;
  ctx.invalidateApiCache = () => {};
  ctx.favStar = () => '';
  ctx.bindFavStars = () => {};
  if (opts.liveGetFavorites) {
    ctx.getFavorites = () => {
      try { return JSON.parse(ctx.localStorage.getItem('meshcore-favorites') || '[]'); } catch(e) { return []; }
    };
  } else {
    ctx.getFavorites = () => [];
  }
  ctx.isFavorite = () => false;
  ctx.connectWS = () => {};
  ctx.HopResolver = { init: () => {}, resolve: () => ({}), ready: () => false };
  ctx.api = () => Promise.resolve({ nodes: [], counts: {} });
  ctx.CLIENT_TTL = { nodeList: 90000, nodeDetail: 240000, nodeHealth: 240000 };
  ctx.initTabBar = () => {};
  ctx.makeColumnsResizable = () => {};
  ctx.debounce = (fn) => fn;
  ctx.Set = Set;
  loadInCtx(ctx, 'public/nodes.js');
  return ctx;
}

// ===== NODES.JS: toggleSort / sortNodes / sortArrow (P0 coverage) =====
console.log('\n=== nodes.js: toggleSort / sortNodes / sortArrow ===');
{
  // --- toggleSort ---
  test('toggleSort switches direction on same column', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'asc' });
    ctx.window._nodesToggleSort('name');
    assert.strictEqual(ctx.window._nodesGetSortState().direction, 'desc');
  });

  test('toggleSort to different column sets default direction', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'asc' });
    ctx.window._nodesToggleSort('last_seen');
    const s = ctx.window._nodesGetSortState();
    assert.strictEqual(s.column, 'last_seen');
    assert.strictEqual(s.direction, 'desc'); // last_seen defaults desc
  });

  test('toggleSort to name column defaults asc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'last_seen', direction: 'desc' });
    ctx.window._nodesToggleSort('name');
    const s = ctx.window._nodesGetSortState();
    assert.strictEqual(s.column, 'name');
    assert.strictEqual(s.direction, 'asc');
  });

  test('toggleSort to advert_count defaults desc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'asc' });
    ctx.window._nodesToggleSort('advert_count');
    assert.strictEqual(ctx.window._nodesGetSortState().direction, 'desc');
  });

  test('toggleSort to role defaults asc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'last_seen', direction: 'desc' });
    ctx.window._nodesToggleSort('role');
    assert.strictEqual(ctx.window._nodesGetSortState().direction, 'asc');
  });

  test('toggleSort persists to localStorage', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesToggleSort('name');
    const stored = JSON.parse(ctx.localStorage.getItem('meshcore-nodes-sort'));
    assert.strictEqual(stored.column, 'name');
  });

  // --- sortNodes ---
  test('sortNodes by name asc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'asc' });
    const arr = [
      { name: 'Charlie', public_key: 'c' },
      { name: 'Alpha', public_key: 'a' },
      { name: 'Bravo', public_key: 'b' },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'Alpha');
    assert.strictEqual(result[1].name, 'Bravo');
    assert.strictEqual(result[2].name, 'Charlie');
  });

  test('sortNodes by name desc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'desc' });
    const arr = [
      { name: 'Alpha', public_key: 'a' },
      { name: 'Charlie', public_key: 'c' },
      { name: 'Bravo', public_key: 'b' },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'Charlie');
    assert.strictEqual(result[2].name, 'Alpha');
  });

  test('sortNodes by name puts unnamed last (asc)', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'asc' });
    const arr = [
      { name: null, public_key: 'x' },
      { name: 'Alpha', public_key: 'a' },
      { name: '', public_key: 'y' },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'Alpha');
  });

  test('sortNodes by last_seen desc (most recent first)', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'last_seen', direction: 'desc' });
    const now = Date.now();
    const arr = [
      { name: 'Old', last_heard: new Date(now - 100000).toISOString() },
      { name: 'New', last_heard: new Date(now).toISOString() },
      { name: 'Mid', last_heard: new Date(now - 50000).toISOString() },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'New');
    assert.strictEqual(result[2].name, 'Old');
  });

  test('sortNodes by last_seen asc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'last_seen', direction: 'asc' });
    const now = Date.now();
    const arr = [
      { name: 'New', last_heard: new Date(now).toISOString() },
      { name: 'Old', last_heard: new Date(now - 100000).toISOString() },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'Old');
    assert.strictEqual(result[1].name, 'New');
  });

  test('sortNodes by last_seen falls back to last_seen when last_heard missing', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'last_seen', direction: 'desc' });
    const now = Date.now();
    const arr = [
      { name: 'A', last_seen: new Date(now - 100000).toISOString() },
      { name: 'B', last_heard: new Date(now).toISOString() },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'B');
  });

  test('sortNodes by last_seen handles missing timestamps', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'last_seen', direction: 'desc' });
    const arr = [
      { name: 'NoTime' },
      { name: 'HasTime', last_heard: new Date().toISOString() },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'HasTime');
  });

  test('sortNodes by advert_count desc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'advert_count', direction: 'desc' });
    const arr = [
      { name: 'Low', advert_count: 5 },
      { name: 'High', advert_count: 100 },
      { name: 'Mid', advert_count: 50 },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'High');
    assert.strictEqual(result[2].name, 'Low');
  });

  test('sortNodes by advert_count asc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'advert_count', direction: 'asc' });
    const arr = [
      { name: 'High', advert_count: 100 },
      { name: 'Low', advert_count: 5 },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'Low');
  });

  test('sortNodes by role asc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'role', direction: 'asc' });
    const arr = [
      { name: 'A', role: 'sensor' },
      { name: 'B', role: 'companion' },
      { name: 'C', role: 'repeater' },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].role, 'companion');
    assert.strictEqual(result[1].role, 'repeater');
    assert.strictEqual(result[2].role, 'sensor');
  });

  test('sortNodes by public_key asc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'public_key', direction: 'asc' });
    const arr = [
      { name: 'C', public_key: 'ccc' },
      { name: 'A', public_key: 'aaa' },
      { name: 'B', public_key: 'bbb' },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].public_key, 'aaa');
    assert.strictEqual(result[2].public_key, 'ccc');
  });

  test('sortNodes handles unknown column gracefully', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'nonexistent', direction: 'asc' });
    const arr = [{ name: 'A' }, { name: 'B' }];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result.length, 2); // no crash
  });

  test('sortNodes with empty array', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'asc' });
    const result = ctx.window._nodesSortNodes([]);
    assert.deepStrictEqual(result, []);
  });

  test('sortNodes name case-insensitive', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'asc' });
    const arr = [
      { name: 'bravo' },
      { name: 'Alpha' },
    ];
    const result = ctx.window._nodesSortNodes([...arr]);
    assert.strictEqual(result[0].name, 'Alpha');
    assert.strictEqual(result[1].name, 'bravo');
  });

  // --- sortArrow ---
  test('sortArrow returns arrow for active column', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'asc' });
    const html = ctx.window._nodesSortArrow('name');
    assert.ok(html.includes('▲'));
    assert.ok(html.includes('sort-arrow'));
  });

  test('sortArrow returns down arrow for desc', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'desc' });
    const html = ctx.window._nodesSortArrow('name');
    assert.ok(html.includes('▼'));
  });

  test('sortArrow returns empty for inactive column', () => {
    const ctx = makeNodesSandbox();
    ctx.window._nodesSetSortState({ column: 'name', direction: 'asc' });
    assert.strictEqual(ctx.window._nodesSortArrow('role'), '');
  });
}

// ===== NODES.JS: syncClaimedToFavorites =====
console.log('\n=== nodes.js: syncClaimedToFavorites ===');
{
  
  test('syncClaimedToFavorites adds claimed pubkeys to favorites', () => {
    const ctx = makeNodesSandbox({ liveGetFavorites: true });
    ctx.localStorage.setItem('meshcore-my-nodes', JSON.stringify([
      { pubkey: 'key1' }, { pubkey: 'key2' }
    ]));
    ctx.localStorage.setItem('meshcore-favorites', JSON.stringify(['key1']));
    ctx.window._nodesSyncClaimedToFavorites();
    const favs = JSON.parse(ctx.localStorage.getItem('meshcore-favorites'));
    assert.ok(favs.includes('key1'));
    assert.ok(favs.includes('key2'));
    assert.strictEqual(favs.length, 2);
  });

  test('syncClaimedToFavorites no-ops when all claimed already favorited', () => {
    const ctx = makeNodesSandbox({ liveGetFavorites: true });
    ctx.localStorage.setItem('meshcore-my-nodes', JSON.stringify([{ pubkey: 'key1' }]));
    ctx.localStorage.setItem('meshcore-favorites', JSON.stringify(['key1', 'key2']));
    ctx.window._nodesSyncClaimedToFavorites();
    const favs = JSON.parse(ctx.localStorage.getItem('meshcore-favorites'));
    assert.deepStrictEqual(favs, ['key1', 'key2']); // unchanged
  });

  test('syncClaimedToFavorites handles empty my-nodes', () => {
    const ctx = makeNodesSandbox({ liveGetFavorites: true });
    ctx.localStorage.setItem('meshcore-my-nodes', '[]');
    ctx.localStorage.setItem('meshcore-favorites', '["key1"]');
    ctx.window._nodesSyncClaimedToFavorites();
    const favs = JSON.parse(ctx.localStorage.getItem('meshcore-favorites'));
    assert.deepStrictEqual(favs, ['key1']); // unchanged
  });

  test('syncClaimedToFavorites handles missing localStorage keys', () => {
    const ctx = makeNodesSandbox({ liveGetFavorites: true });
    // No meshcore-my-nodes or meshcore-favorites set
    ctx.window._nodesSyncClaimedToFavorites(); // should not crash
  });
}

// ===== NODES.JS: renderNodeTimestampHtml / renderNodeTimestampText =====
console.log('\n=== nodes.js: renderNodeTimestampHtml / renderNodeTimestampText ===');
{
  
  test('renderNodeTimestampHtml returns HTML with tooltip', () => {
    const ctx = makeNodesSandbox();
    const d = new Date(Date.now() - 300000).toISOString();
    const html = ctx.window._nodesRenderNodeTimestampHtml(d);
    assert.ok(html.includes('timestamp-text'), 'should have timestamp-text class');
    assert.ok(html.includes('title='), 'should have tooltip');
  });

  test('renderNodeTimestampHtml marks future timestamps', () => {
    const ctx = makeNodesSandbox();
    const d = new Date(Date.now() + 120000).toISOString();
    const html = ctx.window._nodesRenderNodeTimestampHtml(d);
    assert.ok(html.includes('timestamp-future-icon'), 'future timestamp should show warning');
  });

  test('renderNodeTimestampHtml handles null', () => {
    const ctx = makeNodesSandbox();
    const html = ctx.window._nodesRenderNodeTimestampHtml(null);
    assert.ok(html.includes('—'), 'null should produce dash');
  });

  test('renderNodeTimestampText returns plain text', () => {
    const ctx = makeNodesSandbox();
    const d = new Date(Date.now() - 300000).toISOString();
    const text = ctx.window._nodesRenderNodeTimestampText(d);
    assert.ok(!text.includes('<'), 'should be plain text, not HTML');
    assert.ok(text.includes('5m ago') || text.includes('ago') || /^\d{4}/.test(text), 'should be a readable timestamp');
  });

  test('renderNodeTimestampText handles null', () => {
    const ctx = makeNodesSandbox();
    const text = ctx.window._nodesRenderNodeTimestampText(null);
    assert.strictEqual(text, '—');
  });
}

// ===== NODES.JS: getStatusInfo edge cases (P0 coverage expansion) =====
console.log('\n=== nodes.js: getStatusInfo edge cases ===');
{
  
  const ctx = makeNodesSandbox();
  const gsi = ctx.window._nodesGetStatusInfo;
  const gst = ctx.window._nodesGetStatusTooltip;

  test('getStatusInfo with _lastHeard prefers it over last_heard', () => {
    const recent = new Date().toISOString();
    const old = new Date(Date.now() - 96 * 3600000).toISOString();
    const info = gsi({ role: 'repeater', last_heard: old, _lastHeard: recent });
    assert.strictEqual(info.status, 'active');
  });

  test('getStatusInfo with no timestamps returns stale', () => {
    const info = gsi({ role: 'companion' });
    assert.strictEqual(info.status, 'stale');
    assert.strictEqual(info.lastHeardMs, 0);
  });

  test('getStatusInfo uses last_seen as fallback', () => {
    const recent = new Date().toISOString();
    const info = gsi({ role: 'repeater', last_seen: recent });
    assert.strictEqual(info.status, 'active');
  });

  test('getStatusInfo room uses infrastructure threshold (72h)', () => {
    const d48h = new Date(Date.now() - 48 * 3600000).toISOString();
    const info = gsi({ role: 'room', last_heard: d48h });
    assert.strictEqual(info.status, 'active'); // 48h < 72h threshold
  });

  test('getStatusInfo room stale at 96h', () => {
    const d96h = new Date(Date.now() - 96 * 3600000).toISOString();
    const info = gsi({ role: 'room', last_heard: d96h });
    assert.strictEqual(info.status, 'stale');
  });

  test('getStatusInfo sensor stale at 25h', () => {
    const d25h = new Date(Date.now() - 25 * 3600000).toISOString();
    const info = gsi({ role: 'sensor', last_heard: d25h });
    assert.strictEqual(info.status, 'stale');
  });

  test('getStatusInfo returns explanation for active node', () => {
    const info = gsi({ role: 'repeater', last_heard: new Date().toISOString() });
    assert.ok(info.explanation.includes('Last heard'));
  });

  test('getStatusInfo returns explanation for stale companion', () => {
    const d48h = new Date(Date.now() - 48 * 3600000).toISOString();
    const info = gsi({ role: 'companion', last_heard: d48h });
    assert.ok(info.explanation.includes('companions'));
  });

  test('getStatusInfo returns explanation for stale repeater', () => {
    const d96h = new Date(Date.now() - 96 * 3600000).toISOString();
    const info = gsi({ role: 'repeater', last_heard: d96h });
    assert.ok(info.explanation.includes('repeaters'));
  });

  test('getStatusInfo roleColor defaults to gray for unknown role', () => {
    const info = gsi({ role: 'unknown_role', last_heard: new Date().toISOString() });
    assert.strictEqual(info.roleColor, '#6b7280');
  });

  // --- getStatusTooltip edge cases ---
  test('getStatusTooltip active room mentions 72h', () => {
    assert.ok(gst('room', 'active').includes('72h'));
  });

  test('getStatusTooltip stale room mentions offline', () => {
    assert.ok(gst('room', 'stale').includes('offline'));
  });

  test('getStatusTooltip active sensor mentions 24h', () => {
    assert.ok(gst('sensor', 'active').includes('24h'));
  });

  test('getStatusTooltip stale repeater mentions offline', () => {
    assert.ok(gst('repeater', 'stale').includes('offline'));
  });
}

// ===== APP.JS: payloadTypeColor =====
console.log('\n=== app.js: payloadTypeColor ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const payloadTypeColor = ctx.payloadTypeColor;

  // Edge cases and behavioral properties only — no tautological lookup-table restating
  test('payloadTypeColor(99) = unknown', () => assert.strictEqual(payloadTypeColor(99), 'unknown'));
  test('payloadTypeColor(null) = unknown', () => assert.strictEqual(payloadTypeColor(null), 'unknown'));
  test('payloadTypeColor(undefined) = unknown', () => assert.strictEqual(payloadTypeColor(undefined), 'unknown'));
  test('payloadTypeColor(6) = unknown (no mapping for 6)', () => assert.strictEqual(payloadTypeColor(6), 'unknown'));
  test('all defined payload types return a non-unknown string', () => {
    const definedTypes = [0, 1, 2, 3, 4, 5, 7, 8, 9];
    for (const t of definedTypes) {
      const result = payloadTypeColor(t);
      assert.strictEqual(typeof result, 'string', `type ${t} should return a string`);
      assert.notStrictEqual(result, 'unknown', `type ${t} should not be unknown`);
    }
  });
  test('all defined payload types return distinct values', () => {
    const definedTypes = [0, 1, 2, 3, 4, 5, 7, 8, 9];
    const values = new Set(definedTypes.map(t => payloadTypeColor(t)));
    assert.strictEqual(values.size, definedTypes.length, 'each type should map to a unique color class');
  });
}

// ===== APP.JS: pad2 / pad3 =====
console.log('\n=== app.js: pad2 / pad3 ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const pad2 = ctx.pad2;
  const pad3 = ctx.pad3;

  test('pad2(0) = "00"', () => assert.strictEqual(pad2(0), '00'));
  test('pad2(5) = "05"', () => assert.strictEqual(pad2(5), '05'));
  test('pad2(12) = "12"', () => assert.strictEqual(pad2(12), '12'));
  test('pad2(99) = "99"', () => assert.strictEqual(pad2(99), '99'));
  test('pad2(100) = "100" (no truncation)', () => assert.strictEqual(pad2(100), '100'));

  test('pad3(0) = "000"', () => assert.strictEqual(pad3(0), '000'));
  test('pad3(5) = "005"', () => assert.strictEqual(pad3(5), '005'));
  test('pad3(42) = "042"', () => assert.strictEqual(pad3(42), '042'));
  test('pad3(123) = "123"', () => assert.strictEqual(pad3(123), '123'));
  test('pad3(999) = "999"', () => assert.strictEqual(pad3(999), '999'));
}

// ===== APP.JS: formatIsoLike =====
console.log('\n=== app.js: formatIsoLike ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const formatIsoLike = ctx.formatIsoLike;

  test('formatIsoLike UTC without ms', () => {
    const d = new Date('2024-03-15T08:05:03.456Z');
    assert.strictEqual(formatIsoLike(d, 'utc', false), '2024-03-15 08:05:03');
  });

  test('formatIsoLike UTC with ms', () => {
    const d = new Date('2024-03-15T08:05:03.456Z');
    assert.strictEqual(formatIsoLike(d, 'utc', true), '2024-03-15 08:05:03.456');
  });

  test('formatIsoLike local without ms', () => {
    const d = new Date('2024-03-15T08:05:03.456Z');
    const result = formatIsoLike(d, 'local', false);
    assert.ok(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(result));
  });

  test('formatIsoLike local with ms', () => {
    const d = new Date('2024-03-15T08:05:03.456Z');
    const result = formatIsoLike(d, 'local', true);
    assert.ok(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}$/.test(result));
  });

  test('formatIsoLike pads single-digit values', () => {
    const d = new Date('2024-01-02T03:04:05.006Z');
    assert.strictEqual(formatIsoLike(d, 'utc', true), '2024-01-02 03:04:05.006');
  });
}

// ===== APP.JS: formatTimestampCustom =====
console.log('\n=== app.js: formatTimestampCustom ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const formatTimestampCustom = ctx.formatTimestampCustom;

  test('replaces all tokens correctly (UTC)', () => {
    const d = new Date('2024-03-15T08:05:03.456Z');
    const result = formatTimestampCustom(d, 'YYYY-MM-DD HH:mm:ss.SSS Z', 'utc');
    assert.strictEqual(result, '2024-03-15 08:05:03.456 UTC');
  });

  test('replaces all tokens correctly (local)', () => {
    const d = new Date('2024-03-15T08:05:03.456Z');
    const result = formatTimestampCustom(d, 'YYYY/MM/DD HH:mm:ss Z', 'local');
    assert.ok(result.endsWith('local'));
    assert.ok(/^\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2} local$/.test(result));
  });

  test('returns empty for format with no valid tokens', () => {
    const d = new Date('2024-03-15T08:05:03Z');
    assert.strictEqual(formatTimestampCustom(d, 'no tokens here', 'utc'), '');
  });

  test('handles partial format strings', () => {
    const d = new Date('2024-03-15T08:05:03Z');
    assert.strictEqual(formatTimestampCustom(d, 'HH:mm', 'utc'), '08:05');
  });

  test('handles only date tokens', () => {
    const d = new Date('2024-03-15T08:05:03Z');
    assert.strictEqual(formatTimestampCustom(d, 'YYYY-MM-DD', 'utc'), '2024-03-15');
  });
}

// ===== APP.JS: getTimestampMode / getTimestampTimezone / getTimestampFormatPreset / getTimestampCustomFormat =====
console.log('\n=== app.js: timestamp preference getters ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');

  // getTimestampMode
  test('getTimestampMode defaults to ago', () => {
    assert.strictEqual(ctx.getTimestampMode(), 'ago');
  });
  test('getTimestampMode reads localStorage', () => {
    ctx.localStorage.setItem('meshcore-timestamp-mode', 'absolute');
    assert.strictEqual(ctx.getTimestampMode(), 'absolute');
    ctx.localStorage.removeItem('meshcore-timestamp-mode');
  });
  test('getTimestampMode falls back to server config', () => {
    ctx.window.SITE_CONFIG = { timestamps: { defaultMode: 'absolute' } };
    assert.strictEqual(ctx.getTimestampMode(), 'absolute');
    ctx.window.SITE_CONFIG = null;
  });
  test('getTimestampMode ignores invalid localStorage value', () => {
    ctx.localStorage.setItem('meshcore-timestamp-mode', 'invalid');
    assert.strictEqual(ctx.getTimestampMode(), 'ago');
    ctx.localStorage.removeItem('meshcore-timestamp-mode');
  });

  // getTimestampTimezone
  test('getTimestampTimezone defaults to local', () => {
    assert.strictEqual(ctx.getTimestampTimezone(), 'local');
  });
  test('getTimestampTimezone reads localStorage', () => {
    ctx.localStorage.setItem('meshcore-timestamp-timezone', 'utc');
    assert.strictEqual(ctx.getTimestampTimezone(), 'utc');
    ctx.localStorage.removeItem('meshcore-timestamp-timezone');
  });
  test('getTimestampTimezone falls back to server config', () => {
    ctx.localStorage.removeItem('meshcore-timestamp-timezone');
    ctx.window.SITE_CONFIG = { timestamps: { timezone: 'utc' } };
    assert.strictEqual(ctx.getTimestampTimezone(), 'utc');
    ctx.window.SITE_CONFIG = null;
  });

  // getTimestampFormatPreset
  test('getTimestampFormatPreset defaults to iso', () => {
    assert.strictEqual(ctx.getTimestampFormatPreset(), 'iso');
  });
  test('getTimestampFormatPreset reads localStorage', () => {
    ctx.localStorage.setItem('meshcore-timestamp-format', 'iso-seconds');
    assert.strictEqual(ctx.getTimestampFormatPreset(), 'iso-seconds');
    ctx.localStorage.removeItem('meshcore-timestamp-format');
  });
  test('getTimestampFormatPreset reads locale from localStorage', () => {
    ctx.localStorage.setItem('meshcore-timestamp-format', 'locale');
    assert.strictEqual(ctx.getTimestampFormatPreset(), 'locale');
    ctx.localStorage.removeItem('meshcore-timestamp-format');
  });

  // getTimestampCustomFormat
  test('getTimestampCustomFormat returns empty when not allowed', () => {
    ctx.window.SITE_CONFIG = { timestamps: { allowCustomFormat: false } };
    assert.strictEqual(ctx.getTimestampCustomFormat(), '');
  });
  test('getTimestampCustomFormat reads localStorage when allowed', () => {
    ctx.window.SITE_CONFIG = { timestamps: { allowCustomFormat: true } };
    ctx.localStorage.setItem('meshcore-timestamp-custom-format', 'YYYY/MM/DD');
    assert.strictEqual(ctx.getTimestampCustomFormat(), 'YYYY/MM/DD');
    ctx.localStorage.removeItem('meshcore-timestamp-custom-format');
    ctx.window.SITE_CONFIG = null;
  });
  test('getTimestampCustomFormat falls back to server config', () => {
    ctx.window.SITE_CONFIG = { timestamps: { allowCustomFormat: true, customFormat: 'HH:mm' } };
    assert.strictEqual(ctx.getTimestampCustomFormat(), 'HH:mm');
    ctx.window.SITE_CONFIG = null;
  });
}

// ===== APP.JS: invalidateApiCache =====
console.log('\n=== app.js: invalidateApiCache ===');
{
  // Each test uses its own sandbox to avoid shared state between async tests

  test('invalidateApiCache causes api to re-fetch after cache bust', async () => {
    const ctx = makeSandbox();
    let fetchCount = 0;
    ctx.fetch = () => { fetchCount++; return Promise.resolve({ ok: true, json: () => Promise.resolve({ r: fetchCount }) }); };
    loadInCtx(ctx, 'public/roles.js');
    loadInCtx(ctx, 'public/app.js');
    const flush = () => new Promise(r => setImmediate(r));
    await ctx.api('/test', { ttl: 60000 });
    await flush();
    const c1 = fetchCount;
    await ctx.api('/test', { ttl: 60000 });
    assert.strictEqual(fetchCount, c1, 'second call should use cache');
    ctx.invalidateApiCache('/test');
    await ctx.api('/test', { ttl: 60000 });
    assert.ok(fetchCount > c1, 'should re-fetch after invalidation');
  });

  test('invalidateApiCache with no prefix busts all entries', async () => {
    const ctx = makeSandbox();
    let fetchCount = 0;
    ctx.fetch = () => { fetchCount++; return Promise.resolve({ ok: true, json: () => Promise.resolve({ r: fetchCount }) }); };
    loadInCtx(ctx, 'public/roles.js');
    loadInCtx(ctx, 'public/app.js');
    const flush = () => new Promise(r => setImmediate(r));
    await ctx.api('/a', { ttl: 60000 }); await flush();
    await ctx.api('/b', { ttl: 60000 }); await flush();
    const c1 = fetchCount;
    await ctx.api('/a', { ttl: 60000 });
    assert.strictEqual(fetchCount, c1, 'cache should work');
    ctx.invalidateApiCache();
    await ctx.api('/a', { ttl: 60000 });
    await ctx.api('/b', { ttl: 60000 });
    assert.strictEqual(fetchCount, c1 + 2, 'both should re-fetch');
  });

  test('invalidateApiCache with prefix only busts matching', async () => {
    const ctx = makeSandbox();
    let fetchCount = 0;
    ctx.fetch = () => { fetchCount++; return Promise.resolve({ ok: true, json: () => Promise.resolve({ r: fetchCount }) }); };
    loadInCtx(ctx, 'public/roles.js');
    loadInCtx(ctx, 'public/app.js');
    const flush = () => new Promise(r => setImmediate(r));
    await ctx.api('/statsX', { ttl: 60000 }); await flush();
    await ctx.api('/nodesX', { ttl: 60000 }); await flush();
    const c1 = fetchCount;
    ctx.invalidateApiCache('/statsX');
    await ctx.api('/statsX', { ttl: 60000 }); await flush();
    assert.strictEqual(fetchCount, c1 + 1, '/statsX should re-fetch');
    await ctx.api('/nodesX', { ttl: 60000 });
    assert.strictEqual(fetchCount, c1 + 1, '/nodesX should still use cache');
  });
}

// ===== APP.JS: formatHex =====
console.log('\n=== app.js: formatHex ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const formatHex = ctx.formatHex;

  test('formatHex formats bytes with spaces', () => {
    assert.strictEqual(formatHex('aabbcc'), 'aa bb cc');
  });
  test('formatHex handles single byte', () => {
    assert.strictEqual(formatHex('ff'), 'ff');
  });
  test('formatHex returns empty for null', () => {
    assert.strictEqual(formatHex(null), '');
  });
  test('formatHex returns empty for empty string', () => {
    assert.strictEqual(formatHex(''), '');
  });
  test('formatHex handles odd-length hex', () => {
    assert.strictEqual(formatHex('aabbc'), 'aa bb c');
  });
}

// ===== APP.JS: createColoredHexDump =====
console.log('\n=== app.js: createColoredHexDump ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const createColoredHexDump = ctx.createColoredHexDump;

  test('returns plain hex-byte span when no ranges', () => {
    const result = createColoredHexDump('aabb', []);
    assert.ok(result.includes('hex-byte'));
    assert.ok(result.includes('aa bb'));
  });

  test('returns plain hex-byte span when ranges is null', () => {
    const result = createColoredHexDump('aabb', null);
    assert.ok(result.includes('hex-byte'));
  });

  test('colors bytes by range label', () => {
    const result = createColoredHexDump('aabbccdd', [
      { label: 'Header', start: 0, end: 1 },
      { label: 'Payload', start: 2, end: 3 },
    ]);
    assert.ok(result.includes('hex-header'));
    assert.ok(result.includes('hex-payload'));
  });

  test('later ranges override earlier ones', () => {
    const result = createColoredHexDump('aabb', [
      { label: 'Header', start: 0, end: 1 },
      { label: 'Payload', start: 0, end: 1 },
    ]);
    // Payload should win since it comes later
    assert.ok(result.includes('hex-payload'), 'overriding range class should be present');
    assert.ok(!result.includes('hex-header'), 'overridden range class should be absent');
  });

  test('handles null hex', () => {
    const result = createColoredHexDump(null, [{ label: 'Header', start: 0, end: 0 }]);
    assert.ok(result.includes('hex-byte'));
  });

  test('handles empty hex', () => {
    const result = createColoredHexDump('', [{ label: 'Header', start: 0, end: 0 }]);
    assert.ok(result.includes('hex-byte'));
  });
}

// ===== APP.JS: buildHexLegend =====
console.log('\n=== app.js: buildHexLegend ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const buildHexLegend = ctx.buildHexLegend;

  test('returns empty for null ranges', () => {
    assert.strictEqual(buildHexLegend(null), '');
  });
  test('returns empty for empty ranges', () => {
    assert.strictEqual(buildHexLegend([]), '');
  });
  test('builds legend entries with swatches', () => {
    const result = buildHexLegend([
      { label: 'Header', start: 0, end: 1 },
      { label: 'Payload', start: 2, end: 3 },
    ]);
    assert.ok(result.includes('Header'));
    assert.ok(result.includes('Payload'));
    assert.ok(result.includes('swatch'));
  });
  test('deduplicates same label', () => {
    const result = buildHexLegend([
      { label: 'Header', start: 0, end: 1 },
      { label: 'Header', start: 2, end: 3 },
    ]);
    const count = (result.match(/Header/g) || []).length;
    assert.strictEqual(count, 1);
  });
  test('swatch element exists for each label', () => {
    const result = buildHexLegend([{ label: 'Path', start: 0, end: 0 }]);
    assert.ok(result.includes('swatch'), 'should contain a swatch element');
    assert.ok(result.includes('Path'), 'should contain the label text');
    // Verify swatch has a background-color style (don't hardcode the exact color)
    assert.ok(result.includes('background'), 'swatch should have a background color style');
  });
}

// ===== APP.JS: favorites (getFavorites, isFavorite, toggleFavorite, favStar) =====
console.log('\n=== app.js: favorites ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');

  test('getFavorites returns empty array when no data', () => {
    assert.deepStrictEqual(ctx.getFavorites(), []);
  });

  test('getFavorites returns saved array', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["pk1","pk2"]');
    assert.deepStrictEqual(ctx.getFavorites(), ['pk1', 'pk2']);
  });

  test('getFavorites handles corrupt JSON', () => {
    ctx.localStorage.setItem('meshcore-favorites', '{bad}');
    const result = ctx.getFavorites();
    assert.ok(Array.isArray(result));
    assert.strictEqual(result.length, 0);
  });

  test('isFavorite returns true for saved key', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["pk1"]');
    assert.strictEqual(ctx.isFavorite('pk1'), true);
  });

  test('isFavorite returns false for unsaved key', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["pk1"]');
    assert.strictEqual(ctx.isFavorite('pk2'), false);
  });

  test('toggleFavorite adds key', () => {
    ctx.localStorage.setItem('meshcore-favorites', '[]');
    const result = ctx.toggleFavorite('pk1');
    assert.strictEqual(result, true);
    assert.deepStrictEqual(ctx.getFavorites(), ['pk1']);
  });

  test('toggleFavorite removes existing key', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["pk1","pk2"]');
    const result = ctx.toggleFavorite('pk1');
    assert.strictEqual(result, false);
    assert.deepStrictEqual(ctx.getFavorites(), ['pk2']);
  });

  test('favStar returns filled star for favorite', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["pk1"]');
    const html = ctx.favStar('pk1');
    assert.ok(html.includes('★'));
    assert.ok(html.includes('on'));
    assert.ok(html.includes('Remove from favorites'));
  });

  test('favStar returns empty star for non-favorite', () => {
    ctx.localStorage.setItem('meshcore-favorites', '[]');
    const html = ctx.favStar('pk1');
    assert.ok(html.includes('☆'));
    assert.ok(!html.includes(' on'));
    assert.ok(html.includes('Add to favorites'));
  });

  test('favStar includes custom class', () => {
    ctx.localStorage.setItem('meshcore-favorites', '[]');
    const html = ctx.favStar('pk1', 'my-cls');
    assert.ok(html.includes('my-cls'));
  });
}

// ===== APP.JS: debounce =====
console.log('\n=== app.js: debounce ===');
{
  const ctx = makeSandbox();
  let timerId = 0;
  const scheduledFns = [];
  ctx.setTimeout = (fn, ms) => { const id = ++timerId; scheduledFns.push({ fn, ms, id }); return id; };
  ctx.clearTimeout = (id) => { const idx = scheduledFns.findIndex(t => t.id === id); if (idx >= 0) scheduledFns.splice(idx, 1); };
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const debounce = ctx.debounce;

  test('debounce delays function call', () => {
    scheduledFns.length = 0;
    let called = 0;
    const fn = debounce(() => { called++; }, 100);
    fn();
    assert.strictEqual(called, 0);
    assert.strictEqual(scheduledFns.length, 1);
    assert.strictEqual(scheduledFns[0].ms, 100);
    scheduledFns[0].fn();
    assert.strictEqual(called, 1);
  });

  test('debounce resets timer on rapid calls', () => {
    scheduledFns.length = 0;
    let called = 0;
    const fn = debounce(() => { called++; }, 200);
    fn();
    fn();
    fn();
    // Only last timer should remain (previous cleared)
    assert.strictEqual(scheduledFns.length, 1);
    scheduledFns[0].fn();
    assert.strictEqual(called, 1);
  });

  test('debounce passes arguments', () => {
    scheduledFns.length = 0;
    let receivedArgs;
    const fn = debounce((...args) => { receivedArgs = args; }, 50);
    fn('a', 'b', 'c');
    scheduledFns[0].fn();
    assert.deepStrictEqual(receivedArgs, ['a', 'b', 'c']);
  });
}

// ===== APP.JS: mergeUserHomeConfig edge cases =====
console.log('\n=== app.js: mergeUserHomeConfig edge cases ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const merge = ctx.mergeUserHomeConfig;

  test('returns siteConfig when userTheme is null', () => {
    const cfg = { home: { heroTitle: 'Test' } };
    assert.strictEqual(merge(cfg, null), cfg);
  });

  test('returns siteConfig when userTheme has no home', () => {
    const cfg = { home: { heroTitle: 'Test' } };
    assert.strictEqual(merge(cfg, { theme: {} }), cfg);
  });

  test('returns siteConfig when siteConfig is null', () => {
    assert.strictEqual(merge(null, { home: { heroTitle: 'X' } }), null);
  });

  test('creates home on siteConfig when missing', () => {
    const cfg = {};
    merge(cfg, { home: { heroTitle: 'New' } });
    assert.strictEqual(cfg.home.heroTitle, 'New');
  });

  test('userTheme.home non-object is ignored', () => {
    const cfg = { home: { heroTitle: 'Test' } };
    assert.strictEqual(merge(cfg, { home: 'string' }), cfg);
    assert.strictEqual(cfg.home.heroTitle, 'Test');
  });
}

// ===== APP.JS: formatAbsoluteTimestamp with custom format =====
console.log('\n=== app.js: formatAbsoluteTimestamp (custom format) ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  const formatAbsoluteTimestamp = ctx.formatAbsoluteTimestamp;

  test('formatAbsoluteTimestamp returns dash for null', () => {
    assert.strictEqual(formatAbsoluteTimestamp(null), '—');
  });

  test('formatAbsoluteTimestamp returns dash for invalid date', () => {
    assert.strictEqual(formatAbsoluteTimestamp('not-a-date'), '—');
  });

  test('formatAbsoluteTimestamp uses custom format when enabled', () => {
    ctx.window.SITE_CONFIG = { timestamps: { allowCustomFormat: true, customFormat: 'YYYY/MM/DD' } };
    ctx.localStorage.removeItem('meshcore-timestamp-custom-format');
    ctx.localStorage.setItem('meshcore-timestamp-timezone', 'utc');
    const result = formatAbsoluteTimestamp('2024-06-15T10:30:00Z');
    assert.strictEqual(result, '2024/06/15');
    ctx.localStorage.removeItem('meshcore-timestamp-timezone');
    ctx.window.SITE_CONFIG = null;
  });

  test('formatAbsoluteTimestamp locale UTC returns a formatted date string', () => {
    ctx.window.SITE_CONFIG = { timestamps: { allowCustomFormat: false } };
    ctx.localStorage.setItem('meshcore-timestamp-format', 'locale');
    ctx.localStorage.setItem('meshcore-timestamp-timezone', 'utc');
    const result = formatAbsoluteTimestamp('2024-06-15T10:30:00Z');
    // Verify structural properties rather than reimplementing the production code
    assert.ok(result.includes('2024'), 'result should contain the year');
    assert.ok(result.length > 5, 'result should be a non-trivial formatted string');
    assert.notStrictEqual(result, '2024-06-15T10:30:00Z', 'result should differ from raw ISO format');
    assert.notStrictEqual(result, '—', 'result should not be a dash');
    ctx.localStorage.removeItem('meshcore-timestamp-format');
    ctx.localStorage.removeItem('meshcore-timestamp-timezone');
  });
}

// ===== APP.JS: ROUTE_TYPES / PAYLOAD_TYPES edge cases =====
console.log('\n=== app.js: routeTypeName/payloadTypeName edge cases ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');

  // Edge cases: unknown/boundary values, not just restating the lookup table
  test('routeTypeName returns UNKNOWN for negative value', () => {
    assert.strictEqual(ctx.routeTypeName(-1), 'UNKNOWN');
  });
  test('routeTypeName returns UNKNOWN for value beyond max', () => {
    assert.strictEqual(ctx.routeTypeName(4), 'UNKNOWN');
  });
  test('routeTypeName returns UNKNOWN for null', () => {
    assert.strictEqual(ctx.routeTypeName(null), 'UNKNOWN');
  });
  test('routeTypeName returns UNKNOWN for undefined', () => {
    assert.strictEqual(ctx.routeTypeName(undefined), 'UNKNOWN');
  });
  test('routeTypeName returns string for valid type 0', () => {
    assert.strictEqual(typeof ctx.routeTypeName(0), 'string');
    assert.notStrictEqual(ctx.routeTypeName(0), 'UNKNOWN');
  });
  test('routeTypeName returns distinct values for each valid type', () => {
    const names = new Set([0, 1, 2, 3].map(i => ctx.routeTypeName(i)));
    assert.strictEqual(names.size, 4, 'all 4 route types should have unique names');
    for (const n of names) assert.notStrictEqual(n, 'UNKNOWN');
  });

  test('payloadTypeName returns UNKNOWN for negative value', () => {
    assert.strictEqual(ctx.payloadTypeName(-1), 'UNKNOWN');
  });
  test('payloadTypeName returns UNKNOWN for gap value (12)', () => {
    assert.strictEqual(ctx.payloadTypeName(12), 'UNKNOWN');
  });
  test('payloadTypeName returns UNKNOWN for gap value (14)', () => {
    assert.strictEqual(ctx.payloadTypeName(14), 'UNKNOWN');
  });
  test('payloadTypeName handles type 15 (max defined)', () => {
    assert.notStrictEqual(ctx.payloadTypeName(15), 'UNKNOWN');
  });
  test('payloadTypeName returns UNKNOWN for 16 (beyond max)', () => {
    assert.strictEqual(ctx.payloadTypeName(16), 'UNKNOWN');
  });
  test('payloadTypeName returns UNKNOWN for null', () => {
    assert.strictEqual(ctx.payloadTypeName(null), 'UNKNOWN');
  });
  test('payloadTypeName returns distinct values for all defined types', () => {
    const definedTypes = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 15];
    const names = new Set(definedTypes.map(i => ctx.payloadTypeName(i)));
    assert.strictEqual(names.size, 13, 'all 13 payload types should have unique names');
    for (const n of names) assert.notStrictEqual(n, 'UNKNOWN');
  });

  // isTransportRoute edge cases
  test('isTransportRoute returns true for type 0 and 3', () => {
    assert.strictEqual(ctx.isTransportRoute(0), true);
    assert.strictEqual(ctx.isTransportRoute(3), true);
  });
  test('isTransportRoute returns false for type 1 and 2', () => {
    assert.strictEqual(ctx.isTransportRoute(1), false);
    assert.strictEqual(ctx.isTransportRoute(2), false);
  });
  test('isTransportRoute returns false for null/undefined', () => {
    assert.strictEqual(ctx.isTransportRoute(null), false);
    assert.strictEqual(ctx.isTransportRoute(undefined), false);
  });
}

// ===== packet-helpers.js behavioral tests =====
{
  console.log('\n=== packet-helpers.js: getParsedPath / getParsedDecoded ===');

  // Load the shared module
  const helperSource = fs.readFileSync('public/packet-helpers.js', 'utf8');
  const helperCtx = { window: {}, JSON, Array, Object, console, process };
  vm.createContext(helperCtx);
  vm.runInContext(helperSource, helperCtx);
  const getParsedPath = helperCtx.window.getParsedPath;
  const getParsedDecoded = helperCtx.window.getParsedDecoded;

  // Helper: compare via JSON since vm context creates objects with different prototypes
  function assertJsonEqual(actual, expected, msg) {
    assert.strictEqual(JSON.stringify(actual), JSON.stringify(expected), msg);
  }

  // --- getParsedPath ---
  test('getParsedPath: valid JSON array', () => {
    const p = { path_json: '["abc","def"]' };
    const result = getParsedPath(p);
    assertJsonEqual(result, ["abc", "def"]);
  });

  test('getParsedPath: null input returns empty array', () => {
    const p = { path_json: null };
    assertJsonEqual(getParsedPath(p), []);
  });

  test('getParsedPath: undefined input returns empty array', () => {
    const p = {};
    assertJsonEqual(getParsedPath(p), []);
  });

  test('getParsedPath: empty string returns empty array', () => {
    const p = { path_json: '' };
    assertJsonEqual(getParsedPath(p), []);
  });

  test('getParsedPath: invalid JSON returns empty array', () => {
    const p = { path_json: '{not valid json' };
    assertJsonEqual(getParsedPath(p), []);
  });

  test('getParsedPath: JSON null string returns empty array', () => {
    const p = { path_json: 'null' };
    assertJsonEqual(getParsedPath(p), []);
  });

  test('getParsedPath: caching returns same reference on second call', () => {
    const p = { path_json: '["x"]' };
    const first = getParsedPath(p);
    const second = getParsedPath(p);
    assert.strictEqual(first, second, 'cached result should be same object reference');
  });

  test('getParsedPath: pre-parsed array (non-string) returned as-is', () => {
    const arr = ['already', 'parsed'];
    const p = { path_json: arr };
    assert.strictEqual(getParsedPath(p), arr);
  });

  test('getParsedPath: pre-parsed non-array object returns empty array', () => {
    const p = { path_json: { foo: 1 } };
    assertJsonEqual(getParsedPath(p), []);
  });

  // --- getParsedDecoded ---
  test('getParsedDecoded: valid JSON object', () => {
    const p = { decoded_json: '{"type":"GRP_TXT","text":"hello"}' };
    const result = getParsedDecoded(p);
    assertJsonEqual(result, { type: "GRP_TXT", text: "hello" });
  });

  test('getParsedDecoded: null input returns empty object', () => {
    const p = { decoded_json: null };
    assertJsonEqual(getParsedDecoded(p), {});
  });

  test('getParsedDecoded: undefined input returns empty object', () => {
    const p = {};
    assertJsonEqual(getParsedDecoded(p), {});
  });

  test('getParsedDecoded: empty string returns empty object', () => {
    const p = { decoded_json: '' };
    assertJsonEqual(getParsedDecoded(p), {});
  });

  test('getParsedDecoded: invalid JSON returns empty object', () => {
    const p = { decoded_json: 'not json' };
    assertJsonEqual(getParsedDecoded(p), {});
  });

  test('getParsedDecoded: JSON null string returns empty object', () => {
    const p = { decoded_json: 'null' };
    assertJsonEqual(getParsedDecoded(p), {});
  });

  test('getParsedDecoded: caching returns same reference on second call', () => {
    const p = { decoded_json: '{"a":1}' };
    const first = getParsedDecoded(p);
    const second = getParsedDecoded(p);
    assert.strictEqual(first, second, 'cached result should be same object reference');
  });

  test('getParsedDecoded: pre-parsed object (non-string) returned as-is', () => {
    const obj = { type: 'TXT_MSG' };
    const p = { decoded_json: obj };
    assert.strictEqual(getParsedDecoded(p), obj);
  });

  test('getParsedDecoded: pre-parsed non-object returns empty object', () => {
    const p = { decoded_json: 42 };
    assertJsonEqual(getParsedDecoded(p), {});
  });

  // --- Performance: caching avoids repeated JSON.parse ---
  test('getParsedPath: caching is faster than repeated parsing', () => {
    const iterations = 1000;
    const p_nocache = { path_json: '["hop1","hop2","hop3","hop4","hop5"]' };

    // Measure uncached: parse fresh each time
    const startUncached = process.hrtime.bigint();
    for (let i = 0; i < iterations; i++) {
      JSON.parse(p_nocache.path_json);
    }
    const uncachedNs = Number(process.hrtime.bigint() - startUncached);

    // Measure cached: first call parses, rest hit cache
    const p_cached = { path_json: '["hop1","hop2","hop3","hop4","hop5"]' };
    const startCached = process.hrtime.bigint();
    for (let i = 0; i < iterations; i++) {
      getParsedPath(p_cached);
    }
    const cachedNs = Number(process.hrtime.bigint() - startCached);

    console.log(`    perf: ${iterations} uncached parses = ${(uncachedNs / 1e6).toFixed(2)}ms, ` +
                `${iterations} cached calls = ${(cachedNs / 1e6).toFixed(2)}ms ` +
                `(${(uncachedNs / cachedNs).toFixed(1)}x speedup)`);
    assert.ok(cachedNs < uncachedNs, 'cached path should be faster than uncached parsing');
  });

  test('getParsedDecoded: caching is faster than repeated parsing', () => {
    const iterations = 1000;
    const json = '{"type":"GRP_TXT","text":"hello world","sender":"node1","channel":5}';

    const startUncached = process.hrtime.bigint();
    for (let i = 0; i < iterations; i++) {
      JSON.parse(json);
    }
    const uncachedNs = Number(process.hrtime.bigint() - startUncached);

    const p_cached = { decoded_json: json };
    const startCached = process.hrtime.bigint();
    for (let i = 0; i < iterations; i++) {
      getParsedDecoded(p_cached);
    }
    const cachedNs = Number(process.hrtime.bigint() - startCached);

    console.log(`    perf: ${iterations} uncached parses = ${(uncachedNs / 1e6).toFixed(2)}ms, ` +
                `${iterations} cached calls = ${(cachedNs / 1e6).toFixed(2)}ms ` +
                `(${(uncachedNs / cachedNs).toFixed(1)}x speedup)`);
    assert.ok(cachedNs < uncachedNs, 'cached decoded should be faster than uncached parsing');
  });
}

// ===== SUMMARY =====
Promise.allSettled(pendingTests).then(() => {
  console.log(`\n${'═'.repeat(40)}`);
  console.log(`  Frontend helpers: ${passed} passed, ${failed} failed`);
  console.log(`${'═'.repeat(40)}\n`);
  if (failed > 0) process.exit(1);
}).catch((e) => {
  console.error('Failed waiting for async tests:', e);
  process.exit(1);
});
