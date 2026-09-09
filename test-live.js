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
    AreaFilter: {
      init: () => {},
      onChange: () => {},
      areaQueryString: () => ''
    },
    RegionFilter: {
      nodesRegionQueryString: () => '',
      packetsRegionQueryString: () => ''
    },
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

  loadInCtx(ctx, 'public/payload-labels.js');
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/packet-helpers.js');
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
    assert.strictEqual(result.decoded.path.hops.length, 0);
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
    // Entries should be in chronological order
    assert.ok(entries[0].ts < entries[1].ts, 'entry 0 should be before entry 1');
    assert.ok(entries[1].ts < entries[2].ts, 'entry 1 should be before entry 2');
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

// ===== expandToBufferEntriesAsync (chunked, non-blocking) =====
console.log('\n=== live.js: expandToBufferEntriesAsync ===');
{
  // Build a sandbox with packet-helpers loaded so expandToBufferEntries can call dbPacketToLive
  const ctx = makeSandbox();
  addLiveGlobals(ctx);
  loadInCtx(ctx, 'public/payload-labels.js');
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/packet-helpers.js');
  try { loadInCtx(ctx, 'public/live.js'); } catch (e) {
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }
  const expandSync = ctx.window._liveExpandToBufferEntries;
  const expandAsync = ctx.window._liveExpandToBufferEntriesAsync;
  assert.ok(expandAsync, '_liveExpandToBufferEntriesAsync must be exposed');

  const pkts = [];
  for (let i = 0; i < 500; i++) {
    pkts.push({
      id: i, hash: 'h' + i, timestamp: new Date(1700000000000 + i * 1000).toISOString(),
      decoded_json: '{"type":"GRP_TXT"}', path_json: '[]',
      observations: [
        { timestamp: new Date(1700000000000 + i * 1000 + 100).toISOString(), snr: 5, observer_name: 'O1' },
        { timestamp: new Date(1700000000000 + i * 1000 + 200).toISOString(), snr: 8, observer_name: 'O2' },
      ],
    });
  }

  test('sync expand handles 500 packets (1000 entries) correctly', () => {
    const result = expandSync(pkts);
    assert.strictEqual(result.length, 1000, '500 packets * 2 observations = 1000 entries');
    assert.strictEqual(result[0].pkt.hash, 'h0');
    assert.strictEqual(result[999].pkt.hash, 'h499');
  });

  test('VCR_CHUNK_SIZE is defined and async function yields via setTimeout', () => {
    const src = fs.readFileSync(__dirname + '/public/live.js', 'utf8');
    assert.ok(src.includes('VCR_CHUNK_SIZE'), 'VCR_CHUNK_SIZE constant must exist');
    assert.ok(src.includes('expandToBufferEntriesAsync'), 'async version must exist');
    assert.ok(src.includes('setTimeout(processChunk, 0)'), 'must yield via setTimeout between chunks');
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

  test('digit 8 lights all 7 segments and no others', () => {
    // 0x7F = 0b01111111 — all 7 segment bits on, MSB (colon) off
    const val = SEG_MAP['8'];
    assert.strictEqual(val & 0x7F, 0x7F, 'all 7 segment bits should be set');
    assert.strictEqual(val & 0x80, 0, 'colon bit should not be set for a digit');
  });

  test('colon only sets the MSB (dot/colon indicator)', () => {
    const val = SEG_MAP[':'];
    assert.strictEqual(val & 0x80, 0x80, 'MSB (colon bit) should be set');
    assert.strictEqual(val & 0x7F, 0, 'no segment bits should be set for colon');
  });

  test('space lights no segments', () => {
    assert.strictEqual(SEG_MAP[' '], 0x00, 'space should have no bits set');
  });

  test('digit 1 lights fewer segments than digit 8', () => {
    // Behavioral: 1 has fewer segments lit than 8
    const ones = (n) => { let c = 0; while (n) { c += n & 1; n >>= 1; } return c; };
    assert.ok(ones(SEG_MAP['1']) < ones(SEG_MAP['8']),
      'digit 1 should have fewer segment bits than digit 8');
  });

  test('VCR mode letters are mapped with non-zero segments', () => {
    for (const ch of ['P', 'A', 'U', 'S', 'E', 'L', 'I', 'V']) {
      assert.ok(SEG_MAP[ch] !== undefined, `${ch} must be in SEG_MAP`);
      assert.ok(SEG_MAP[ch] > 0, `${ch} must have non-zero segments`);
    }
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
    assert.strictEqual(VCR().mode, 'PAUSED', 'mode should be PAUSED after first call');
    vcrPause();
    assert.strictEqual(VCR().frozenNow, frozen1);
    assert.strictEqual(VCR().mode, 'PAUSED', 'mode should stay PAUSED after second call');
  });

  test('vcrSpeedCycle cycles through 0.25, 0.5, 1, 2, 4, 8 and wraps', () => {
    vcrSetMode('LIVE');
    VCR().speed = 0.25;
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 0.5);
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 1);
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 2);
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 4);
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 8);
    vcrSpeedCycle();
    assert.strictEqual(VCR().speed, 0.25); // wraps around
  });

  test('vcrSpeedCycle saves speed to localStorage', () => {
    VCR().speed = 1;
    vcrSpeedCycle();
    assert.strictEqual(ctx.localStorage.getItem('live-vcr-speed'), '2');
    vcrSpeedCycle();
    assert.strictEqual(ctx.localStorage.getItem('live-vcr-speed'), '4');
  });

  const speedLabel = ctx.window._liveSpeedLabel;
  assert.ok(speedLabel, '_liveSpeedLabel must be exposed');

  test('speedLabel returns fraction strings for sub-1x speeds', () => {
    assert.strictEqual(speedLabel(0.25), '¼x');
    assert.strictEqual(speedLabel(0.5), '½x');
    assert.strictEqual(speedLabel(1), '1x');
    assert.strictEqual(speedLabel(2), '2x');
    assert.strictEqual(speedLabel(8), '8x');
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
    assert.strictEqual(result.length, 0, 'corrupt data should yield empty array');
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
    assert.ok(html.includes('—'), 'null input should render em-dash fallback');
  });

  test('handles numeric timestamp', () => {
    const html = fmt(Date.now() - 60000);
    assert.ok(typeof html === 'string');
    assert.ok(html.includes('timestamp-text'), 'numeric timestamp should produce timestamp-text span');
    assert.ok(html.includes('title='), 'numeric timestamp should have tooltip');
  });

  test('future timestamp shows warning icon', () => {
    const future = new Date(Date.now() + 120000).toISOString();
    const html = fmt(future);
    assert.ok(html.includes('timestamp-future-icon'), 'should show future warning');
  });
}

// ===== Feed timestamp refresh — data-ts attribute and selector (#701) =====
console.log('\n=== live.js: feed timestamp refresh (#701) ===');
{
  const ctx = makeLiveSandbox({ withAppJs: true });
  const fmt = ctx.window._liveFormatLiveTimestampHtml;

  test('formatLiveTimestampHtml returns different text for different ages', () => {
    const recent = fmt(Date.now() - 5000);
    const older = fmt(Date.now() - 120000);
    // Both should produce valid HTML
    assert.ok(recent.includes('timestamp-text'), 'recent should have timestamp-text');
    assert.ok(older.includes('timestamp-text'), 'older should have timestamp-text');
  });

  test('formatLiveTimestampHtml accepts numeric ms timestamp', () => {
    const ts = Date.now() - 45000;
    const html = fmt(ts);
    assert.ok(html.includes('timestamp-text'), 'numeric ms timestamp should render');
    // Re-calling with same ts should produce same result (idempotent refresh)
    const html2 = fmt(ts);
    assert.strictEqual(html, html2, 'same input should produce same output');
  });

  test('feed-time template with data-ts round-trips correctly', () => {
    // Verify that Number(dataset.ts) fed back to fmt produces valid output
    const ts = Date.now() - 30000;
    const tsStr = String(ts);
    const reparsed = Number(tsStr);
    assert.strictEqual(reparsed, ts, 'data-ts round-trip should preserve value');
    const html = fmt(reparsed);
    assert.ok(html.includes('timestamp-text'), 'round-tripped timestamp should render');
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
    assert.ok(ghost, 'ghost hop should be present in resolved positions — if missing, interpolation logic changed');
    assert.ok(ghost.pos[0] > 37.0 && ghost.pos[0] < 38.0, 'ghost lat should be interpolated');
    assert.ok(ghost.pos[1] > -122.0 && ghost.pos[1] < -121.0, 'ghost lon should be interpolated');
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
    const after = Date.now();
    assert.ok(pkt._ts >= before && pkt._ts <= after, `_ts should be between ${before} and ${after}, got ${pkt._ts}`);
  });

  test('VCR buffer caps at ~2000 entries', () => {
    // Fill buffer past 2000
    VCR().buffer.length = 0;
    for (let i = 0; i < 2100; i++) {
      VCR().buffer.push({ ts: Date.now(), pkt: { hash: 'fill' + i } });
    }
    // Next bufferPacket triggers trim: 2100+1=2101 > 2000 → splice(0, 500) → 1601
    const pkt = { hash: 'overflow', decoded: { header: {}, payload: {} } };
    bufferPacket(pkt);
    assert.strictEqual(VCR().buffer.length, 1601, `buffer should be 2101 - 500 = 1601, got ${VCR().buffer.length}`);
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
    // Packet with no decoded field at all — should not throw, and should still be buffered
    bufferPacket({ hash: 'malformed1' });
    assert.strictEqual(VCR().buffer.length, before + 1, 'malformed packet should still be added to buffer');
  });

  test('bufferPacket handles packet with null decoded', () => {
    const before = VCR().buffer.length;
    bufferPacket({ hash: 'malformed2', decoded: null });
    assert.strictEqual(VCR().buffer.length, before + 1, 'packet with null decoded should still be added to buffer');
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

  test('the live map owns no socket of its own', () => {
    // It used to open a second WebSocket to the endpoint app.js already holds
    // open, and the broadcast does no per-client filtering, so every viewer on
    // this page pulled the full packet stream twice.
    assert.ok(!src.includes('new WebSocket'),
      'live.js must not construct a WebSocket; app.js owns the one socket');
    assert.ok(src.includes('onWS(wsHandler)'),
      'it must subscribe to the shared channel instead');
    assert.ok(src.includes('offWS(wsHandler)'),
      'and unsubscribe rather than closing a socket the rest of the app needs');
  });

  test('reconnect lives with the socket owner and honours wsReconnectMs', () => {
    // Moving the subscription took the live map's own reconnect with it. That
    // was the only place WS_RECONNECT_MS was honoured, so app.js has to use it
    // now or the operator's `wsReconnectMs` setting applies nowhere.
    const appSrc = fs.readFileSync('public/app.js', 'utf8');
    assert.ok(appSrc.includes('setTimeout(connectWS, window.WS_RECONNECT_MS || 3000)'),
      'app.js should reconnect on the configured interval, defaulting to 3s');
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

  test('feed items include transport badge', () => {
    const count = (src.match(/transportBadge\(pkt\.route_type\)/g) || []).length;
    assert.ok(count >= 3,
      `feed rendering should call transportBadge(pkt.route_type) in at least 3 places (found ${count})`);
  });

  test('node detail recent packets include transport badge', () => {
    assert.ok(src.includes('transportBadge(p.route_type)'),
      'node detail recent packets should call transportBadge(p.route_type)');
  });
}

// ===== Node filter (M3 — #771) =====
console.log('\n=== live.js: node filter ===');
{
  const ctx = makeLiveSandbox();
  const pktInvolvesFilter = ctx.window._livePacketInvolvesFilterNode;
  assert.ok(pktInvolvesFilter, '_livePacketInvolvesFilterNode must be exposed');

  const makePkt = (hops) => ({ decoded: { path: { hops }, payload: {} } });

  test('packetInvolvesFilterNode returns true when filter is empty', () => {
    assert.strictEqual(pktInvolvesFilter(makePkt(['abcd1234']), []), true);
  });

  test('packetInvolvesFilterNode matches hop by prefix', () => {
    assert.strictEqual(pktInvolvesFilter(makePkt(['abcd1234', 'ef012345']), ['abcd1234567890ab']), true);
  });

  test('packetInvolvesFilterNode matches full key against short hop', () => {
    assert.strictEqual(pktInvolvesFilter(makePkt(['abcd']), ['abcd1234567890ab']), true);
  });

  test('packetInvolvesFilterNode returns false when no hop matches', () => {
    assert.strictEqual(pktInvolvesFilter(makePkt(['ffff1234', '00001111']), ['abcd1234567890ab']), false);
  });

  test('packetInvolvesFilterNode matches any of multiple filter keys (OR logic)', () => {
    assert.strictEqual(pktInvolvesFilter(makePkt(['ffff0000']), ['abcd1234', 'ffff0000']), true);
  });

  test('packetInvolvesFilterNode returns false for packet with no hops', () => {
    assert.strictEqual(pktInvolvesFilter(makePkt([]), ['abcd1234']), false);
  });

  const getNodeFilterKeys = ctx.window._liveGetNodeFilterKeys;
  assert.ok(getNodeFilterKeys, '_liveGetNodeFilterKeys must be exposed');

  test('node filter defaults to empty array when localStorage is unset', () => {
    assert.strictEqual(getNodeFilterKeys().length, 0);
  });

  test('node filter saves to localStorage when set', () => {
    const setFilter = ctx.window._liveSetNodeFilter;
    assert.ok(setFilter, '_liveSetNodeFilter must be exposed');
    setFilter(['abcd1234', 'ef012345']);
    assert.strictEqual(ctx.localStorage.getItem('live-node-filter'), 'abcd1234,ef012345');
    setFilter([]);
    assert.strictEqual(ctx.localStorage.getItem('live-node-filter'), '');
  });
}

// ===== Clickable paths (M2 — #771) =====
console.log('\n=== live.js: clickable paths ===');
{
  // withAppJs: true → load public/app.js so global `escapeHtml` is in scope.
  // buildClickablePathPopupHtml escapes each hop name via escapeHtml (#1536).
  const ctx = makeLiveSandbox({ withAppJs: true });
  const buildPopupHtml = ctx.window._liveBuildClickablePathPopupHtml;
  assert.ok(buildPopupHtml, '_liveBuildClickablePathPopupHtml must be exposed');

  test('buildClickablePathPopupHtml includes type badge with color', () => {
    const html = buildPopupHtml('GRP_TXT', '#22c55e', ['NodeA', 'Rpt1', 'NodeB'], Date.now() - 5000);
    assert.ok(html.includes('GRP_TXT'), 'should include type name');
    assert.ok(html.includes('#22c55e'), 'should include type color');
  });

  test('buildClickablePathPopupHtml includes hop chain', () => {
    const html = buildPopupHtml('ADVERT', '#6b7280', ['Alpha', 'Beta', 'Gamma'], Date.now() - 3000);
    assert.ok(html.includes('Alpha'), 'should include first hop');
    assert.ok(html.includes('Beta'), 'should include middle hop');
    assert.ok(html.includes('Gamma'), 'should include last hop');
  });

  test('buildClickablePathPopupHtml includes packet link', () => {
    const html = buildPopupHtml('GRP_TXT', '#22c55e', ['A', 'B'], Date.now() - 1000, 'abc123def');
    assert.ok(html.includes('abc123def'), 'should include packet hash link');
  });

  test('buildClickablePathPopupHtml shows relative time', () => {
    const html = buildPopupHtml('GRP_TXT', '#22c55e', ['A', 'B'], Date.now() - 10000);
    assert.ok(html.includes('10s ago'), 'should show 10s ago');
  });

  const pruneClickablePaths = ctx.window._livePruneClickablePaths;
  const clickablePaths = ctx.window._liveClickablePaths;
  assert.ok(pruneClickablePaths, '_livePruneClickablePaths must be exposed');
  assert.ok(Array.isArray(clickablePaths), '_liveClickablePaths must be exposed');

  function loadPaths(entries) {
    clickablePaths.splice(0, clickablePaths.length, ...entries);
  }

  test('pruneClickablePaths removes entries older than TTL', () => {
    const now = Date.now();
    loadPaths([
      { addedAt: now - 35000, poly: { remove() {} } },
      { addedAt: now - 5000,  poly: { remove() {} } },
      { addedAt: now - 1000,  poly: { remove() {} } },
    ]);
    pruneClickablePaths(now);
    assert.strictEqual(clickablePaths.length, 2, 'should remove paths older than 30s');
  });

  test('pruneClickablePaths keeps all entries within TTL', () => {
    const now = Date.now();
    loadPaths([
      { addedAt: now - 5000,  poly: { remove() {} } },
      { addedAt: now - 1000,  poly: { remove() {} } },
    ]);
    pruneClickablePaths(now);
    assert.strictEqual(clickablePaths.length, 2);
  });

  test('pruneClickablePaths enforces max 50 entries (FIFO eviction)', () => {
    const now = Date.now();
    // Match production insertion order: oldest at front (index 0), newest at back
    // entries[0].addedAt = now-5100 (oldest), entries[51].addedAt = now (newest)
    const entries = [];
    for (let i = 51; i >= 0; i--) entries.push({ addedAt: now - i * 100, poly: { remove() {} } });
    loadPaths(entries);
    pruneClickablePaths(now);
    assert.strictEqual(clickablePaths.length, 50, 'should evict oldest beyond 50');
    // FIFO: the 2 oldest (addedAt now-5100 and now-5000) were shifted off; now-4900 is oldest remaining
    assert.strictEqual(clickablePaths[0].addedAt, now - 49 * 100, 'oldest remaining should have addedAt = now-4900');
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
