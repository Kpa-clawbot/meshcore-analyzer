/* Unit tests for live.js functions (tested via VM sandbox)
 * Part of #344 — live.js coverage
 */
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
        out.then(() => { passed++; console.log(`  ✅ ${name}`); })
        .catch((e) => { failed++; console.log(`  ❌ ${name}: ${e.message}`); })
      );
      return;
    }
    passed++; console.log(`  ✅ ${name}`);
  } catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// --- Browser-like sandbox ---
function makeSandbox() {
  const ctx = {
    window: { addEventListener: () => {}, dispatchEvent: () => {}, devicePixelRatio: 1 },
    document: {
      readyState: 'complete',
      createElement: (tag) => ({
        tagName: tag, id: '', textContent: '', innerHTML: '', style: {},
        classList: { add() {}, remove() {}, contains() { return false; } },
        setAttribute() {}, getAttribute() { return null; },
        addEventListener() {}, focus() {},
        getContext: () => ({
          clearRect() {}, fillRect() {}, beginPath() {}, arc() {}, fill() {},
          scale() {}, fillStyle: '', font: '', fillText() {},
        }),
        offsetWidth: 200, offsetHeight: 40, width: 0, height: 0,
      }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
      createElementNS: () => ({
        tagName: 'svg', id: '', textContent: '', innerHTML: '', style: {},
        setAttribute() {}, getAttribute() { return null; },
      }),
      documentElement: { getAttribute: () => null, setAttribute: () => {} },
      body: { appendChild: () => {}, removeChild: () => {}, contains: () => false },
      hidden: false,
    },
    console,
    Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, Map, Set, Promise, URLSearchParams,
    parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => 0, clearTimeout: () => {},
    setInterval: () => 0, clearInterval: () => {},
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    cancelAnimationFrame: () => {},
    localStorage: (() => {
      const store = {};
      return {
        getItem: k => store[k] !== undefined ? store[k] : null,
        setItem: (k, v) => { store[k] = String(v); },
        removeItem: k => { delete store[k]; },
      };
    })(),
    location: { hash: '', protocol: 'https:', host: 'localhost' },
    CustomEvent: class CustomEvent {},
    addEventListener: () => {},
    dispatchEvent: () => {},
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    matchMedia: () => ({ matches: false, addEventListener: () => {} }),
    navigator: {},
    visualViewport: null,
    MutationObserver: function() { this.observe = () => {}; this.disconnect = () => {}; },
    WebSocket: function() { this.close = () => {}; },
    IATA_COORDS_GEO: {},
  };
  vm.createContext(ctx);
  return ctx;
}

function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx);
  for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
}

function makeLeafletMock() {
  return {
    circleMarker: () => {
      const m = {
        addTo() { return m; }, bindTooltip() { return m; }, on() { return m; },
        setRadius() {}, setStyle() {}, setLatLng() {},
        getLatLng() { return { lat: 0, lng: 0 }; },
        _baseColor: '', _baseSize: 5, _glowMarker: null, remove() {},
      };
      return m;
    },
    polyline: () => { const p = { addTo() { return p; }, setStyle() {}, remove() {} }; return p; },
    polygon: () => { const p = { addTo() { return p; }, remove() {} }; return p; },
    map: () => {
      const m = {
        setView() { return m; }, addLayer() { return m; }, on() { return m; },
        getZoom() { return 11; }, getCenter() { return { lat: 37, lng: -122 }; },
        getBounds() { return { contains: () => true }; }, fitBounds() { return m; },
        invalidateSize() {}, remove() {}, hasLayer() { return false; }, removeLayer() {},
      };
      return m;
    },
    layerGroup: () => {
      const g = {
        addTo() { return g; }, addLayer() {}, removeLayer() {},
        clearLayers() {}, hasLayer() { return true; }, eachLayer() {},
      };
      return g;
    },
    tileLayer: () => ({ addTo() { return this; } }),
    control: { attribution: () => ({ addTo() {} }) },
    DomUtil: { addClass() {}, removeClass() {} },
  };
}

function addLiveGlobals(ctx) {
  ctx.L = makeLeafletMock();
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
  ctx.HopResolver = { init() {}, resolve: () => ({}), ready: () => false };
  ctx.MeshAudio = null;
  ctx.RegionFilter = { init() {}, getSelected: () => null, onRegionChange: () => {} };
}

function makeLiveSandbox({ withAppJs = false } = {}) {
  const ctx = makeSandbox();
  addLiveGlobals(ctx);

  loadInCtx(ctx, 'public/roles.js');
  if (withAppJs) loadInCtx(ctx, 'public/app.js');
  try { loadInCtx(ctx, 'public/live.js'); } catch (e) {
    console.error('live.js load error:', e.message);
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }
  return ctx;
}

// ===== dbPacketToLive =====
console.log('\n=== live.js: dbPacketToLive ===');
{
  const ctx = makeLiveSandbox();
  const dbPacketToLive = ctx.window._liveDbPacketToLive;
  assert.ok(dbPacketToLive, '_liveDbPacketToLive must be exposed');

  test('converts basic DB packet to live format', () => {
    const pkt = {
      id: 42, hash: 'abc123',
      raw_hex: 'deadbeef',
      path_json: '["hop1","hop2"]',
      decoded_json: '{"type":"GRP_TXT","text":"hello"}',
      timestamp: '2024-06-15T12:00:00Z',
      snr: 7.5, rssi: -85, observer_name: 'ObsA',
    };
    const result = dbPacketToLive(pkt);
    assert.strictEqual(result.id, 42);
    assert.strictEqual(result.hash, 'abc123');
    assert.strictEqual(result.raw, 'deadbeef');
    assert.strictEqual(result.snr, 7.5);
    assert.strictEqual(result.rssi, -85);
    assert.strictEqual(result.observer, 'ObsA');
    assert.strictEqual(result.decoded.header.payloadTypeName, 'GRP_TXT');
    assert.strictEqual(result.decoded.payload.text, 'hello');
    assert.deepStrictEqual(result.decoded.path.hops, ['hop1', 'hop2']);
    assert.strictEqual(result._ts, new Date('2024-06-15T12:00:00Z').getTime());
  });

  test('handles null decoded_json', () => {
    const pkt = { id: 1, hash: 'x', decoded_json: null, path_json: null, timestamp: '2024-01-01T00:00:00Z' };
    const result = dbPacketToLive(pkt);
    assert.strictEqual(result.decoded.header.payloadTypeName, 'UNKNOWN');
    assert.deepStrictEqual(result.decoded.path.hops, []);
  });

  test('uses payload_type_name as fallback', () => {
    const pkt = { id: 2, hash: 'y', decoded_json: '{}', path_json: '[]', timestamp: '2024-01-01T00:00:00Z', payload_type_name: 'ADVERT' };
    const result = dbPacketToLive(pkt);
    assert.strictEqual(result.decoded.header.payloadTypeName, 'ADVERT');
  });

  test('uses created_at as timestamp fallback', () => {
    const pkt = { id: 3, hash: 'z', decoded_json: '{}', path_json: '[]', created_at: '2024-03-01T06:00:00Z' };
    const result = dbPacketToLive(pkt);
    assert.strictEqual(result._ts, new Date('2024-03-01T06:00:00Z').getTime());
  });
}

// ===== expandToBufferEntries =====
console.log('\n=== live.js: expandToBufferEntries ===');
{
  const ctx = makeLiveSandbox();
  const expand = ctx.window._liveExpandToBufferEntries;
  assert.ok(expand, '_liveExpandToBufferEntries must be exposed');

  test('single packet without observations returns one entry', () => {
    const pkts = [{
      id: 1, hash: 'h1', timestamp: '2024-06-15T12:00:00Z',
      decoded_json: '{"type":"GRP_TXT"}', path_json: '[]',
    }];
    const entries = expand(pkts);
    assert.strictEqual(entries.length, 1);
    assert.strictEqual(entries[0].pkt.id, 1);
    assert.strictEqual(entries[0].ts, new Date('2024-06-15T12:00:00Z').getTime());
  });

  test('packet with observations expands to one entry per observation', () => {
    const pkts = [{
      id: 10, hash: 'h10', timestamp: '2024-06-15T12:00:00Z',
      decoded_json: '{"type":"ADVERT"}', path_json: '[]', raw_hex: 'ff',
      observations: [
        { timestamp: '2024-06-15T12:00:01Z', snr: 5, observer_name: 'O1' },
        { timestamp: '2024-06-15T12:00:02Z', snr: 8, observer_name: 'O2' },
        { timestamp: '2024-06-15T12:00:03Z', snr: 3, observer_name: 'O3' },
      ],
    }];
    const entries = expand(pkts);
    assert.strictEqual(entries.length, 3);
    assert.strictEqual(entries[0].pkt.observer, 'O1');
    assert.strictEqual(entries[1].pkt.observer, 'O2');
    assert.strictEqual(entries[2].pkt.observer, 'O3');
    // All should share the same hash
    assert.strictEqual(entries[0].pkt.hash, 'h10');
    assert.strictEqual(entries[2].pkt.hash, 'h10');
  });

  test('empty observations array treated as no observations', () => {
    const pkts = [{
      id: 5, hash: 'h5', timestamp: '2024-01-01T00:00:00Z',
      decoded_json: '{}', path_json: '[]', observations: [],
    }];
    const entries = expand(pkts);
    assert.strictEqual(entries.length, 1);
  });

  test('multiple packets expand independently', () => {
    const pkts = [
      { id: 1, hash: 'h1', timestamp: '2024-01-01T00:00:00Z', decoded_json: '{}', path_json: '[]' },
      {
        id: 2, hash: 'h2', timestamp: '2024-01-01T00:00:00Z', decoded_json: '{}', path_json: '[]', raw_hex: 'aa',
        observations: [
          { timestamp: '2024-01-01T00:00:01Z', observer_name: 'X' },
          { timestamp: '2024-01-01T00:00:02Z', observer_name: 'Y' },
        ],
      },
    ];
    const entries = expand(pkts);
    assert.strictEqual(entries.length, 3);
  });
}

// ===== SEG_MAP (7-segment display) =====
console.log('\n=== live.js: SEG_MAP ===');
{
  const ctx = makeLiveSandbox();
  const SEG_MAP = ctx.window._liveSEG_MAP;
  assert.ok(SEG_MAP, '_liveSEG_MAP must be exposed');

  test('all digits 0-9 are mapped', () => {
    for (let i = 0; i <= 9; i++) {
      assert.ok(SEG_MAP[String(i)] !== undefined, `digit ${i} must be in SEG_MAP`);
      assert.ok(SEG_MAP[String(i)] > 0, `digit ${i} must have non-zero segments`);
    }
  });

  test('colon has 0x80 flag', () => {
    assert.strictEqual(SEG_MAP[':'], 0x80);
  });

  test('space has 0x00', () => {
    assert.strictEqual(SEG_MAP[' '], 0x00);
  });

  test('digit 8 has all segments (0x7F)', () => {
    assert.strictEqual(SEG_MAP['8'], 0x7F);
  });

  test('VCR mode letters are mapped', () => {
    assert.ok(SEG_MAP['P'] !== undefined, 'P for PAUSE');
    assert.ok(SEG_MAP['A'] !== undefined, 'A');
    assert.ok(SEG_MAP['U'] !== undefined, 'U');
    assert.ok(SEG_MAP['S'] !== undefined, 'S');
    assert.ok(SEG_MAP['E'] !== undefined, 'E');
    assert.ok(SEG_MAP['L'] !== undefined, 'L');
    assert.ok(SEG_MAP['I'] !== undefined, 'I');
    assert.ok(SEG_MAP['V'] !== undefined, 'V');
  });
}

// ===== VCR state machine =====
console.log('\n=== live.js: VCR state machine ===');
{
  const ctx = makeLiveSandbox();
  const VCR = ctx.window._liveVCR;
  const vcrSetMode = ctx.window._liveVcrSetMode;
  const vcrPause = ctx.window._liveVcrPause;
  const vcrSpeedCycle = ctx.window._liveVcrSpeedCycle;
  assert.ok(VCR, '_liveVCR must be exposed');

  test('VCR initial mode is LIVE', () => {
    assert.strictEqual(VCR().mode, 'LIVE');
  });

  test('vcrSetMode changes mode', () => {
    vcrSetMode('PAUSED');
    assert.strictEqual(VCR().mode, 'PAUSED');
    assert.ok(VCR().frozenNow != null, 'frozenNow should be set when not LIVE');
  });

  test('vcrSetMode LIVE clears frozenNow', () => {
    vcrSetMode('LIVE');
    assert.strictEqual(VCR().mode, 'LIVE');
    assert.strictEqual(VCR().frozenNow, null);
  });

  test('vcrPause stops replay and sets PAUSED', () => {
    vcrSetMode('LIVE');
    vcrPause();
    assert.strictEqual(VCR().mode, 'PAUSED');
    assert.strictEqual(VCR().missedCount, 0);
  });

  test('vcrPause is idempotent', () => {
    vcrPause();
    const frozen1 = VCR().frozenNow;
    vcrPause();
    assert.strictEqual(VCR().frozenNow, frozen1);
  });

  test('vcrSpeedCycle cycles through 1,2,4,8', () => {
    vcrSetMode('LIVE');
    VCR().speed = 1;
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 2);
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 4);
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 8);
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 1); // wraps around
  });

  const vcrResumeLive = ctx.window._liveVcrResumeLive;
  assert.ok(vcrResumeLive, '_liveVcrResumeLive must be exposed');

  test('vcrResumeLive transitions from PAUSED to LIVE', () => {
    vcrPause();
    assert.strictEqual(VCR().mode, 'PAUSED');
    assert.ok(VCR().frozenNow != null, 'frozenNow should be set when paused');
    vcrResumeLive();
    assert.strictEqual(VCR().mode, 'LIVE');
    assert.strictEqual(VCR().frozenNow, null, 'frozenNow should be cleared');
    assert.strictEqual(VCR().playhead, -1, 'playhead should reset to -1');
    assert.strictEqual(VCR().speed, 1, 'speed should reset to 1');
    assert.strictEqual(VCR().missedCount, 0, 'missedCount should be 0');
  });
}

// ===== getFavoritePubkeys =====
console.log('\n=== live.js: getFavoritePubkeys ===');
{
  const ctx = makeLiveSandbox();
  const getFavPubkeys = ctx.window._liveGetFavoritePubkeys;
  assert.ok(getFavPubkeys, '_liveGetFavoritePubkeys must be exposed');

  test('returns empty array when no favorites stored', () => {
    ctx.localStorage.removeItem('meshcore-favorites');
    ctx.localStorage.removeItem('meshcore-my-nodes');
    const result = getFavPubkeys();
    assert.ok(Array.isArray(result));
    assert.strictEqual(result.length, 0);
  });

  test('reads from meshcore-favorites', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["pk1","pk2"]');
    ctx.localStorage.removeItem('meshcore-my-nodes');
    const result = getFavPubkeys();
    assert.ok(result.includes('pk1'));
    assert.ok(result.includes('pk2'));
  });

  test('reads from meshcore-my-nodes pubkeys', () => {
    ctx.localStorage.removeItem('meshcore-favorites');
    ctx.localStorage.setItem('meshcore-my-nodes', '[{"pubkey":"mynode1"},{"pubkey":"mynode2"}]');
    const result = getFavPubkeys();
    assert.ok(result.includes('mynode1'));
    assert.ok(result.includes('mynode2'));
  });

  test('merges both sources', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["fav1"]');
    ctx.localStorage.setItem('meshcore-my-nodes', '[{"pubkey":"mine1"}]');
    const result = getFavPubkeys();
    assert.ok(result.includes('fav1'));
    assert.ok(result.includes('mine1'));
    assert.strictEqual(result.length, 2);
  });

  test('handles corrupt localStorage gracefully', () => {
    ctx.localStorage.setItem('meshcore-favorites', 'not json');
    ctx.localStorage.setItem('meshcore-my-nodes', '{bad}');
    const result = getFavPubkeys();
    assert.ok(Array.isArray(result));
  });

  test('filters out falsy values', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["pk1",null,"",false,"pk2"]');
    ctx.localStorage.removeItem('meshcore-my-nodes');
    const result = getFavPubkeys();
    assert.ok(!result.includes(null));
    assert.ok(!result.includes(''));
    assert.strictEqual(result.length, 2);
  });
}

// ===== packetInvolvesFavorite =====
console.log('\n=== live.js: packetInvolvesFavorite ===');
{
  const ctx = makeLiveSandbox();
  // Clean localStorage to avoid leakage from prior test sections
  ctx.localStorage.removeItem('meshcore-favorites');
  ctx.localStorage.removeItem('meshcore-my-nodes');
  const involves = ctx.window._livePacketInvolvesFavorite;
  assert.ok(involves, '_livePacketInvolvesFavorite must be exposed');

  test('returns false when no favorites', () => {
    ctx.localStorage.removeItem('meshcore-favorites');
    ctx.localStorage.removeItem('meshcore-my-nodes');
    const pkt = { decoded: { header: {}, payload: { pubKey: 'abc' } } };
    assert.strictEqual(involves(pkt), false);
  });

  test('matches sender pubKey', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["sender123"]');
    const pkt = { decoded: { header: {}, payload: { pubKey: 'sender123' } } };
    assert.strictEqual(involves(pkt), true);
  });

  test('matches hop prefix', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["abcdef1234567890"]');
    const pkt = { decoded: { header: {}, payload: {}, path: { hops: ['abcd'] } } };
    assert.strictEqual(involves(pkt), true);
  });

  test('does not match unrelated hop', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["abcdef1234567890"]');
    const pkt = { decoded: { header: {}, payload: {}, path: { hops: ['ffff'] } } };
    assert.strictEqual(involves(pkt), false);
  });

  test('handles missing decoded fields gracefully', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["xyz"]');
    const pkt = {};
    assert.strictEqual(involves(pkt), false);
  });
}

// ===== isNodeFavorited =====
console.log('\n=== live.js: isNodeFavorited ===');
{
  const ctx = makeLiveSandbox();
  // Clean localStorage to avoid leakage from prior test sections
  ctx.localStorage.removeItem('meshcore-favorites');
  ctx.localStorage.removeItem('meshcore-my-nodes');
  const isFav = ctx.window._liveIsNodeFavorited;
  assert.ok(isFav, '_liveIsNodeFavorited must be exposed');

  test('returns true when pubkey is in favorites', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["pk1","pk2"]');
    assert.strictEqual(isFav('pk1'), true);
  });

  test('returns false when pubkey not in favorites', () => {
    ctx.localStorage.setItem('meshcore-favorites', '["pk1"]');
    assert.strictEqual(isFav('pk99'), false);
  });

  test('returns false with empty favorites', () => {
    ctx.localStorage.removeItem('meshcore-favorites');
    ctx.localStorage.removeItem('meshcore-my-nodes');
    assert.strictEqual(isFav('pk1'), false);
  });
}

// ===== formatLiveTimestampHtml =====
console.log('\n=== live.js: formatLiveTimestampHtml ===');
{
  const ctx = makeLiveSandbox({ withAppJs: true });

  const fmt = ctx.window._liveFormatLiveTimestampHtml;
  assert.ok(fmt, '_liveFormatLiveTimestampHtml must be exposed');

  test('formats a recent ISO timestamp', () => {
    const iso = new Date(Date.now() - 30000).toISOString();
    const html = fmt(iso);
    assert.ok(html.includes('timestamp-text'), 'should contain timestamp-text span');
    assert.ok(html.includes('title='), 'should have tooltip');
  });

  test('handles null input', () => {
    const html = fmt(null);
    assert.ok(typeof html === 'string');
    assert.ok(html.includes('—') || html.includes('timestamp-text'));
  });

  test('handles numeric timestamp', () => {
    const html = fmt(Date.now() - 60000);
    assert.ok(typeof html === 'string');
  });

  test('future timestamp shows warning icon', () => {
    const future = new Date(Date.now() + 120000).toISOString();
    const html = fmt(future);
    assert.ok(html.includes('timestamp-future-icon'), 'should show future warning');
  });
}

// ===== resolveHopPositions =====
console.log('\n=== live.js: resolveHopPositions ===');
{
  const ctx = makeLiveSandbox();
  const resolve = ctx.window._liveResolveHopPositions;
  const nodeData = ctx.window._liveNodeData();
  const nodeMarkers = ctx.window._liveNodeMarkers();
  assert.ok(resolve, '_liveResolveHopPositions must be exposed');

  test('returns empty array for empty hops', () => {
    const result = resolve([], {});
    assert.deepStrictEqual(result, []);
  });

  test('returns sender position when payload has pubKey + coords', () => {
    const payload = { pubKey: 'sender1', name: 'Sender', lat: 37.5, lon: -122.0 };
    // No nodes in nodeData, so hops won't resolve
    const result = resolve([], payload);
    // With empty hops, the function still adds the sender as an anchor point.
    assert.ok(Array.isArray(result), 'should return an array');
    assert.strictEqual(result.length, 1, 'sender coords should produce one anchor position');
    assert.strictEqual(result[0].pos[0], 37.5, 'anchor should use sender lat');
    assert.strictEqual(result[0].pos[1], -122.0, 'anchor should use sender lon');
    assert.strictEqual(result[0].name, 'Sender', 'anchor should use sender name');
    assert.strictEqual(result[0].known, true, 'sender with coords should be marked as known');
  });

  test('resolves known node from nodeData', () => {
    // Add a node to nodeData
    nodeData['nodeA_pubkey'] = { public_key: 'nodeA_pubkey', name: 'NodeA', lat: 37.3, lon: -122.0 };
    nodeData['nodeB_pubkey'] = { public_key: 'nodeB_pubkey', name: 'NodeB', lat: 38.0, lon: -121.0 };
    // Need HopResolver to resolve the hop prefix — set on both ctx and window
    const mockResolver = {
      init() {},
      ready() { return true; },
      resolve(hops) {
        const map = {};
        for (const h of hops) {
          if (h === 'nodeA') map[h] = { name: 'NodeA', pubkey: 'nodeA_pubkey' };
          else if (h === 'nodeB') map[h] = { name: 'NodeB', pubkey: 'nodeB_pubkey' };
          else map[h] = { name: null, pubkey: null };
        }
        return map;
      },
    };
    ctx.HopResolver = mockResolver;
    ctx.window.HopResolver = mockResolver;
    // Need at least 2 known nodes for ghost mode to not filter down
    const result = resolve(['nodeA', 'nodeB'], {});
    assert.ok(result.length >= 2, `expected >= 2 positions, got ${result.length}`);
    const foundA = result.find(r => r.key === 'nodeA_pubkey');
    assert.ok(foundA, 'should resolve nodeA to nodeA_pubkey');
    assert.strictEqual(foundA.pos[0], 37.3);
    assert.strictEqual(foundA.pos[1], -122.0);
    assert.strictEqual(foundA.known, true);
    delete nodeData['nodeA_pubkey'];
    delete nodeData['nodeB_pubkey'];
  });

  test('ghost hops get interpolated positions between known nodes', () => {
    // Set up: two known nodes, one unknown hop between them
    nodeData['n1'] = { public_key: 'n1', name: 'N1', lat: 37.0, lon: -122.0 };
    nodeData['n2'] = { public_key: 'n2', name: 'N2', lat: 38.0, lon: -121.0 };
    const mockResolver = {
      init() {},
      ready() { return true; },
      resolve(hops) {
        const map = {};
        for (const h of hops) {
          if (h === 'h1') map[h] = { name: 'N1', pubkey: 'n1' };
          else if (h === 'h3') map[h] = { name: 'N2', pubkey: 'n2' };
          else map[h] = { name: null, pubkey: null };
        }
        return map;
      },
    };
    ctx.HopResolver = mockResolver;
    ctx.window.HopResolver = mockResolver;
    const result = resolve(['h1', 'h2', 'h3'], {});
    assert.ok(result.length >= 2, `should have at least 2 positions, got ${result.length}`);
    // Check that the ghost hop got an interpolated position
    const ghost = result.find(r => r.ghost);
    if (ghost) {
      assert.ok(ghost.pos[0] > 37.0 && ghost.pos[0] < 38.0, 'ghost lat should be interpolated');
      assert.ok(ghost.pos[1] > -122.0 && ghost.pos[1] < -121.0, 'ghost lon should be interpolated');
    }
    delete nodeData['n1'];
    delete nodeData['n2'];
  });
}

// ===== bufferPacket and VCR buffer management =====
console.log('\n=== live.js: bufferPacket / VCR buffer ===');
{
  const ctx = makeLiveSandbox();
  const bufferPacket = ctx.window._liveBufferPacket;
  const VCR = ctx.window._liveVCR;
  assert.ok(bufferPacket, '_liveBufferPacket must be exposed');

  test('bufferPacket adds entry to VCR buffer', () => {
    const initialLen = VCR().buffer.length;
    const pkt = { hash: 'test1', decoded: { header: { payloadTypeName: 'GRP_TXT' }, payload: {} } };
    bufferPacket(pkt);
    assert.strictEqual(VCR().buffer.length, initialLen + 1);
    const last = VCR().buffer[VCR().buffer.length - 1];
    assert.strictEqual(last.pkt.hash, 'test1');
    assert.ok(last.ts > 0);
  });

  test('bufferPacket sets _ts on packet', () => {
    const pkt = { hash: 'test2', decoded: { header: {}, payload: {} } };
    const before = Date.now();
    bufferPacket(pkt);
    assert.ok(pkt._ts >= before);
  });

  test('VCR buffer caps at ~2000 entries', () => {
    // Fill buffer past 2000
    VCR().buffer.length = 0;
    for (let i = 0; i < 2100; i++) {
      VCR().buffer.push({ ts: Date.now(), pkt: { hash: 'fill' + i } });
    }
    // Next bufferPacket should trigger trim
    const pkt = { hash: 'overflow', decoded: { header: {}, payload: {} } };
    bufferPacket(pkt);
    assert.ok(VCR().buffer.length <= 2001, `buffer should be capped, got ${VCR().buffer.length}`);
  });

  test('bufferPacket increments missedCount when PAUSED', () => {
    ctx.window._liveVcrSetMode('PAUSED');
    VCR().missedCount = 0;
    const pkt = { hash: 'missed1', decoded: { header: {}, payload: {} } };
    bufferPacket(pkt);
    assert.strictEqual(VCR().missedCount, 1);
    bufferPacket({ hash: 'missed2', decoded: { header: {}, payload: {} } });
    assert.strictEqual(VCR().missedCount, 2);
    ctx.window._liveVcrSetMode('LIVE');
  });

  test('bufferPacket handles malformed packet without decoded field', () => {
    const before = VCR().buffer.length;
    // Packet with no decoded field at all — should not throw
    bufferPacket({ hash: 'malformed1' });
    assert.ok(VCR().buffer.length >= before, 'buffer should not shrink');
  });

  test('bufferPacket handles packet with null decoded', () => {
    const before = VCR().buffer.length;
    bufferPacket({ hash: 'malformed2', decoded: null });
    assert.ok(VCR().buffer.length >= before, 'buffer should not shrink');
  });
}

// ===== VCR frozenNow behavior =====
console.log('\n=== live.js: VCR frozenNow ===');
{
  const ctx = makeLiveSandbox();
  const VCR = ctx.window._liveVCR;
  const setMode = ctx.window._liveVcrSetMode;

  test('frozenNow is set on first non-LIVE mode', () => {
    setMode('LIVE');
    assert.strictEqual(VCR().frozenNow, null);
    setMode('PAUSED');
    const t1 = VCR().frozenNow;
    assert.ok(t1 > 0);
    // Should NOT change on subsequent non-LIVE mode changes
    setMode('REPLAY');
    assert.strictEqual(VCR().frozenNow, t1, 'frozenNow should not change if already set');
  });

  test('frozenNow cleared on LIVE', () => {
    setMode('PAUSED');
    assert.ok(VCR().frozenNow != null);
    setMode('LIVE');
    assert.strictEqual(VCR().frozenNow, null);
  });
}

// ===== Source-level checks for live.js safety guards =====
// NOTE: These src.includes() checks are intentionally brittle — they verify that specific
// safety guards exist in the source code TODAY. They will break on whitespace/rename refactors,
// which is an acceptable tradeoff: a failing test forces the developer to verify the guard
// still exists in its new form. For critical guards (animation limits, null checks), prefer
// behavioral tests where feasible (see bufferPacket and VCR sections above).
console.log('\n=== live.js: source-level safety checks ===');
{
  const src = fs.readFileSync('public/live.js', 'utf8');

  test('renderPacketTree null-checks packets array', () => {
    assert.ok(src.includes('if (!packets || !packets.length) return;'),
      'renderPacketTree must guard null/empty packets');
  });

  test('animatePath guards MAX_CONCURRENT_ANIMS', () => {
    assert.ok(src.includes('if (activeAnims >= MAX_CONCURRENT_ANIMS) return;'),
      'animatePath must respect concurrent animation limit');
  });

  test('animatePath guards null animLayer/pathsLayer', () => {
    assert.ok(src.includes('if (!animLayer || !pathsLayer) return;'),
      'animatePath must guard null layers');
  });

  test('pulseNode guards null animLayer/nodesLayer', () => {
    assert.ok(src.includes('if (!animLayer || !nodesLayer) return;'),
      'pulseNode must guard null layers');
  });

  test('nextHop guards null animLayer', () => {
    assert.ok(src.includes('if (!animLayer) return;'),
      'nextHop must guard null animLayer before drawing');
  });

  test('VCR buffer trim adjusts playhead', () => {
    assert.ok(src.includes('VCR.playhead = Math.max(0, VCR.playhead - trimCount)'),
      'buffer trim must adjust playhead to prevent stale indices');
  });

  test('tab hidden skips animations', () => {
    assert.ok(src.includes('if (_tabHidden)'),
      'bufferPacket should skip animation when tab is hidden');
  });

  test('visibility change clears propagation buffer', () => {
    assert.ok(src.includes('propagationBuffer.clear()'),
      'tab restore should clear propagation buffer');
  });

  test('connectWS has reconnect on close', () => {
    assert.ok(src.includes('ws.onclose = () => setTimeout(connectWS, WS_RECONNECT_MS)'),
      'WebSocket should auto-reconnect on close');
  });

  test('addNodeMarker avoids duplicates', () => {
    assert.ok(src.includes('if (nodeMarkers[n.public_key]) return nodeMarkers[n.public_key]'),
      'addNodeMarker should return existing marker if already exists');
  });

  test('matrix mode saves toggle to localStorage', () => {
    assert.ok(src.includes("localStorage.setItem('live-matrix-mode'"),
      'matrix toggle should persist to localStorage');
  });

  test('matrix rain saves toggle to localStorage', () => {
    assert.ok(src.includes("localStorage.setItem('live-matrix-rain'"),
      'matrix rain toggle should persist to localStorage');
  });

  test('realistic propagation saves toggle to localStorage', () => {
    assert.ok(src.includes("localStorage.setItem('live-realistic-propagation'"),
      'realistic propagation toggle should persist to localStorage');
  });

  test('favorites filter saves toggle to localStorage', () => {
    assert.ok(src.includes("localStorage.setItem('live-favorites-only'"),
      'favorites filter toggle should persist to localStorage');
  });

  test('ghost hops saves toggle to localStorage', () => {
    assert.ok(src.includes("localStorage.setItem('live-ghost-hops'"),
      'ghost hops toggle should persist to localStorage');
  });

  test('clearNodeMarkers resets HopResolver', () => {
    assert.ok(src.includes('if (window.HopResolver) HopResolver.init([])'),
      'clearNodeMarkers should reset HopResolver');
  });

  test('rescaleMarkers reads zoom from map', () => {
    assert.ok(src.includes('const zoom = map.getZoom()'),
      'rescaleMarkers should read current zoom level');
  });

  test('startReplay pre-aggregates by hash', () => {
    assert.ok(src.includes('const hashGroups = new Map()'),
      'startReplay should group buffer entries by hash');
  });

  test('orientation change retries resize with delays', () => {
    assert.ok(src.includes('[50, 200, 500, 1000, 2000].forEach'),
      'orientation change handler should retry resize at multiple intervals');
  });

  test('VCR rewind deduplicates buffer entries by ID', () => {
    assert.ok(src.includes('const existingIds = new Set(VCR.buffer.map(b => b.pkt.id)'),
      'vcrRewind should dedup by packet ID');
  });
}

// ===== SUMMARY =====
Promise.allSettled(pendingTests).then(() => {
  console.log(`\n${'═'.repeat(40)}`);
  console.log(`  live.js tests: ${passed} passed, ${failed} failed`);
  console.log(`${'═'.repeat(40)}\n`);
  if (failed > 0) process.exit(1);
}).catch((e) => {
  console.error('Failed waiting for async tests:', e);
  process.exit(1);
});
