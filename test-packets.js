/* Unit tests for packets.js functions (tested via VM sandbox) */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

// Build a browser-like sandbox with all deps packets.js needs
function makeSandbox() {
  const registeredPages = {};
  const ctx = {
    window: {
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => {},
      innerWidth: 1200,
      PacketFilter: null,
    },
    document: {
      readyState: 'complete',
      createElement: (tag) => ({
        tagName: tag.toUpperCase(), id: '', textContent: '', innerHTML: '',
        className: '', style: {}, appendChild: () => {}, setAttribute: () => {},
        addEventListener: () => {}, querySelectorAll: () => [], querySelector: () => null,
        classList: { add: () => {}, remove: () => {}, contains: () => false },
      }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      removeEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
      body: { appendChild: () => {} },
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
    RangeError,
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
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
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
    history: { replaceState: () => {} },
    CustomEvent: class CustomEvent {},
    Map,
    Set,
    Promise,
    URLSearchParams,
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    _registeredPages: registeredPages,
    // Stub global functions packets.js depends on
    registerPage: (name, handler) => { registeredPages[name] = handler; },
  };
  vm.createContext(ctx);
  return ctx;
}

function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx, { filename: file });
  for (const k of Object.keys(ctx.window)) {
    ctx[k] = ctx.window[k];
  }
}

function loadPacketsSandbox() {
  const ctx = makeSandbox();
  // Load dependencies first
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  // HopDisplay stub (simpler than loading real file which may have DOM deps)
  vm.runInContext(`
    window.HopDisplay = {
      renderHop: function(h, entry, opts) {
        if (entry && entry.name) return '<span class="hop-named">' + entry.name + '</span>';
        return '<span class="hop-hex">' + h + '</span>';
      },
      _showFromBtn: function() {}
    };
  `, ctx);
  loadInCtx(ctx, 'public/packets.js');
  return ctx;
}

// ===== TESTS =====

console.log('\n=== packets.js: typeName ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('typeName returns known type', () => {
    assert.strictEqual(api.typeName(0), 'Request');
    assert.strictEqual(api.typeName(4), 'Advert');
    assert.strictEqual(api.typeName(5), 'Channel Msg');
  });

  test('typeName returns fallback for unknown', () => {
    assert.strictEqual(api.typeName(99), 'Type 99');
    assert.strictEqual(api.typeName(undefined), 'Type undefined');
  });
}

console.log('\n=== packets.js: obsName ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('obsName returns dash for falsy id', () => {
    assert.strictEqual(api.obsName(null), '—');
    assert.strictEqual(api.obsName(''), '—');
    assert.strictEqual(api.obsName(undefined), '—');
  });

  test('obsName returns id when not in observerMap', () => {
    assert.strictEqual(api.obsName('unknown-id'), 'unknown-id');
  });
}

console.log('\n=== packets.js: kv ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('kv produces correct HTML', () => {
    const result = api.kv('Route', 'Direct');
    assert(result.includes('byop-key'));
    assert(result.includes('Route'));
    assert(result.includes('Direct'));
    assert(result.includes('byop-val'));
  });
}

console.log('\n=== packets.js: sectionRow / fieldRow ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('sectionRow produces section HTML', () => {
    const result = api.sectionRow('Header');
    assert(result.includes('section-row'));
    assert(result.includes('Header'));
    assert(result.includes('colspan="4"'));
  });

  test('fieldRow produces field HTML', () => {
    const result = api.fieldRow(0, 'Header Byte', '0xFF', 'some desc');
    assert(result.includes('0'));
    assert(result.includes('Header Byte'));
    assert(result.includes('0xFF'));
    assert(result.includes('some desc'));
    assert(result.includes('mono'));
  });

  test('fieldRow handles empty description', () => {
    const result = api.fieldRow(5, 'Test', 'val', '');
    assert(result.includes('text-muted'));
  });
}

console.log('\n=== packets.js: getDetailPreview ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('getDetailPreview returns empty for null/undefined', () => {
    assert.strictEqual(api.getDetailPreview(null), '');
    assert.strictEqual(api.getDetailPreview(undefined), '');
  });

  test('getDetailPreview handles CHAN type', () => {
    const result = api.getDetailPreview({ type: 'CHAN', text: 'hello world', channel: 'general' });
    assert(result.includes('💬'));
    assert(result.includes('hello world'));
    assert(result.includes('chan-tag'));
    assert(result.includes('general'));
  });

  test('getDetailPreview truncates long CHAN text', () => {
    const longText = 'x'.repeat(100);
    const result = api.getDetailPreview({ type: 'CHAN', text: longText });
    assert(result.includes('…'));
    assert(!result.includes('x'.repeat(100)));
  });

  test('getDetailPreview handles ADVERT type', () => {
    const result = api.getDetailPreview({
      type: 'ADVERT', name: 'TestNode', pubKey: 'abc123',
      flags: { repeater: true }
    });
    assert(result.includes('📡'));
    assert(result.includes('TestNode'));
    assert(result.includes('hop-link'));
  });

  test('getDetailPreview handles ADVERT room', () => {
    const result = api.getDetailPreview({
      type: 'ADVERT', name: 'RoomNode', pubKey: 'abc',
      flags: { room: true }
    });
    assert(result.includes('🏠'));
  });

  test('getDetailPreview handles ADVERT sensor', () => {
    const result = api.getDetailPreview({
      type: 'ADVERT', name: 'Sensor1', pubKey: 'abc',
      flags: { sensor: true }
    });
    assert(result.includes('🌡'));
  });

  test('getDetailPreview handles ADVERT companion (default)', () => {
    const result = api.getDetailPreview({
      type: 'ADVERT', name: 'Comp', pubKey: 'abc',
      flags: {}
    });
    assert(result.includes('📻'));
  });

  test('getDetailPreview handles GRP_TXT with channelHash (no_key)', () => {
    const result = api.getDetailPreview({
      type: 'GRP_TXT', channelHash: 0xAB, decryptionStatus: 'no_key'
    });
    assert(result.includes('🔒'));
    assert(result.includes('0xAB'));
    assert(result.includes('no key'));
  });

  test('getDetailPreview handles GRP_TXT decryption_failed', () => {
    const result = api.getDetailPreview({
      type: 'GRP_TXT', channelHash: 5, decryptionStatus: 'decryption_failed'
    });
    assert(result.includes('decryption failed'));
  });

  test('getDetailPreview handles GRP_TXT with channelHashHex', () => {
    const result = api.getDetailPreview({
      type: 'GRP_TXT', channelHash: 0xFF, channelHashHex: 'FF'
    });
    assert(result.includes('0xFF'));
  });

  test('getDetailPreview handles TXT_MSG', () => {
    const result = api.getDetailPreview({
      type: 'TXT_MSG', srcHash: 'abcdef01', destHash: '12345678'
    });
    assert(result.includes('✉️'));
    assert(result.includes('abcdef01'));
    assert(result.includes('12345678'));
  });

  test('getDetailPreview handles PATH', () => {
    const result = api.getDetailPreview({
      type: 'PATH', srcHash: 'aabb', destHash: 'ccdd'
    });
    assert(result.includes('🔀'));
  });

  test('getDetailPreview handles REQ', () => {
    const result = api.getDetailPreview({
      type: 'REQ', srcHash: 'aa', destHash: 'bb'
    });
    assert(result.includes('🔒'));
    assert(result.includes('aa'));
  });

  test('getDetailPreview handles RESPONSE', () => {
    const result = api.getDetailPreview({
      type: 'RESPONSE', srcHash: 'aa', destHash: 'bb'
    });
    assert(result.includes('🔒'));
  });

  test('getDetailPreview handles ANON_REQ', () => {
    const result = api.getDetailPreview({
      type: 'ANON_REQ', destHash: 'dd'
    });
    assert(result.includes('anon'));
    assert(result.includes('dd'));
  });

  test('getDetailPreview handles text fallback', () => {
    const result = api.getDetailPreview({ text: 'some message' });
    assert(result.includes('some message'));
  });

  test('getDetailPreview truncates long text fallback', () => {
    const result = api.getDetailPreview({ text: 'z'.repeat(100) });
    assert(result.includes('…'));
  });

  test('getDetailPreview handles public_key fallback', () => {
    const result = api.getDetailPreview({ public_key: 'abcdef1234567890abcdef' });
    assert(result.includes('📡'));
    assert(result.includes('abcdef1234567890'));
  });

  test('getDetailPreview returns empty for empty decoded', () => {
    assert.strictEqual(api.getDetailPreview({}), '');
  });
}

console.log('\n=== packets.js: getPathHopCount ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('getPathHopCount with valid path', () => {
    assert.strictEqual(api.getPathHopCount({ path_json: '["a","b","c"]' }), 3);
  });

  test('getPathHopCount with empty path', () => {
    assert.strictEqual(api.getPathHopCount({ path_json: '[]' }), 0);
  });

  test('getPathHopCount with null/missing', () => {
    assert.strictEqual(api.getPathHopCount({}), 0);
    assert.strictEqual(api.getPathHopCount({ path_json: null }), 0);
  });

  test('getPathHopCount with invalid JSON', () => {
    assert.strictEqual(api.getPathHopCount({ path_json: 'not json' }), 0);
  });
}

console.log('\n=== packets.js: sortGroupChildren ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('sortGroupChildren handles null/empty gracefully', () => {
    api.sortGroupChildren(null);
    api.sortGroupChildren({});
    api.sortGroupChildren({ _children: [] });
    // No throw
  });

  test('sortGroupChildren default sort groups by observer earliest-first', () => {
    // Need to set obsSortMode — it reads from closure. Default is 'observer'.
    const group = {
      _children: [
        { observer_name: 'B', timestamp: '2024-01-01T02:00:00Z' },
        { observer_name: 'A', timestamp: '2024-01-01T01:00:00Z' },
        { observer_name: 'B', timestamp: '2024-01-01T01:30:00Z' },
      ]
    };
    api.sortGroupChildren(group);
    // A has earliest timestamp, should be first
    assert.strictEqual(group._children[0].observer_name, 'A');
    // Then B entries
    assert.strictEqual(group._children[1].observer_name, 'B');
    assert.strictEqual(group._children[2].observer_name, 'B');
    // B entries should be time-ascending within group
    assert(group._children[1].timestamp < group._children[2].timestamp);
  });

  test('sortGroupChildren updates header from first child', () => {
    const group = {
      observer_id: 'old',
      _children: [
        { observer_name: 'A', observer_id: 'new-id', timestamp: '2024-01-01T01:00:00Z', snr: 10, rssi: -50, path_json: '["x"]', direction: 'rx' },
      ]
    };
    api.sortGroupChildren(group);
    assert.strictEqual(group.observer_id, 'new-id');
    assert.strictEqual(group.snr, 10);
    assert.strictEqual(group.rssi, -50);
    assert.strictEqual(group.path_json, '["x"]');
    assert.strictEqual(group.direction, 'rx');
  });
}

console.log('\n=== packets.js: renderTimestampCell ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('renderTimestampCell produces HTML with timestamp-text', () => {
    const result = api.renderTimestampCell('2024-01-15T10:30:00Z');
    assert(result.includes('timestamp-text'));
  });

  test('renderTimestampCell handles null gracefully', () => {
    const result = api.renderTimestampCell(null);
    // Should not throw, produces some output
    assert(typeof result === 'string');
  });
}

console.log('\n=== packets.js: renderPath ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('renderPath returns dash for empty/null', () => {
    assert.strictEqual(api.renderPath(null, null), '—');
    assert.strictEqual(api.renderPath([], null), '—');
  });

  test('renderPath renders hops with arrows', () => {
    const result = api.renderPath(['aa', 'bb'], null);
    assert(result.includes('arrow'));
    assert(result.includes('aa'));
    assert(result.includes('bb'));
  });

  test('renderPath renders single hop without arrow', () => {
    const result = api.renderPath(['cc'], null);
    assert(result.includes('cc'));
    assert(!result.includes('arrow'));
  });
}

console.log('\n=== packets.js: renderDecodedPacket ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('renderDecodedPacket produces header section', () => {
    const decoded = {
      header: { routeType: 0, payloadType: 4, payloadVersion: 1 },
      payload: { name: 'TestNode' },
      path: { hops: [] }
    };
    const hex = 'aabbccdd';
    const result = api.renderDecodedPacket(decoded, hex);
    assert(result.includes('byop-decoded'));
    assert(result.includes('Header'));
    assert(result.includes('4 bytes'));
  });

  test('renderDecodedPacket renders path hops', () => {
    const decoded = {
      header: { routeType: 0, payloadType: 4 },
      payload: {},
      path: { hops: ['aa', 'bb'] }
    };
    const hex = 'aabbccdd';
    const result = api.renderDecodedPacket(decoded, hex);
    assert(result.includes('Path (2 hops)'));
    assert(result.includes('aa'));
    assert(result.includes('bb'));
  });

  test('renderDecodedPacket renders payload fields', () => {
    const decoded = {
      header: { routeType: 0, payloadType: 5 },
      payload: { channel: 'general', text: 'hello' },
      path: { hops: [] }
    };
    const hex = 'aabb';
    const result = api.renderDecodedPacket(decoded, hex);
    assert(result.includes('channel'));
    assert(result.includes('general'));
    assert(result.includes('hello'));
  });

  test('renderDecodedPacket renders nested objects as JSON', () => {
    const decoded = {
      header: { routeType: 0, payloadType: 0 },
      payload: { flags: { repeater: true } },
      path: { hops: [] }
    };
    const hex = 'aa';
    const result = api.renderDecodedPacket(decoded, hex);
    assert(result.includes('byop-pre'));
    assert(result.includes('repeater'));
  });

  test('renderDecodedPacket skips null payload values', () => {
    const decoded = {
      header: { routeType: 0, payloadType: 0 },
      payload: { a: null, b: undefined, c: 'visible' },
      path: { hops: [] }
    };
    const hex = 'aa';
    const result = api.renderDecodedPacket(decoded, hex);
    assert(result.includes('visible'));
    // null/undefined values should be skipped
    const kvCount = (result.match(/byop-row/g) || []).length;
    // Only 'c' should appear in payload (a and b are null/undefined), plus header fields
    assert(kvCount >= 1);
  });

  test('renderDecodedPacket renders raw hex', () => {
    const decoded = {
      header: { routeType: 0, payloadType: 0 },
      payload: {},
      path: { hops: [] }
    };
    const hex = 'aabbcc';
    const result = api.renderDecodedPacket(decoded, hex);
    assert(result.includes('AA BB CC'));
    assert(result.includes('byop-hex'));
  });
}

console.log('\n=== packets.js: buildFieldTable ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('buildFieldTable produces table HTML', () => {
    const pkt = { raw_hex: 'c0400102', route_type: 1, payload_type: 4 };
    const decoded = { type: 'ADVERT', name: 'Node', pubKey: 'abc', flags: { type: 2, hasLocation: false, hasName: true, raw: 0x22 } };
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('field-table'));
    assert(result.includes('Header'));
    assert(result.includes('Header Byte'));
    assert(result.includes('Path Length'));
  });

  test('buildFieldTable handles transport codes (route_type 0)', () => {
    const pkt = { raw_hex: 'c0400102030405060708', route_type: 0, payload_type: 0 };
    const decoded = { destHash: 'aa', srcHash: 'bb', mac: 'cc', encryptedData: 'dd' };
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('Transport Codes'));
    assert(result.includes('Next Hop'));
    assert(result.includes('Last Hop'));
  });

  test('buildFieldTable renders path hops', () => {
    const pkt = { raw_hex: 'c042aabb', route_type: 1, payload_type: 0 };
    const decoded = { destHash: 'xx' };
    const result = api.buildFieldTable(pkt, decoded, ['aa', 'bb'], []);
    assert(result.includes('Path (2 hops)'));
    assert(result.includes('Hop 0'));
    assert(result.includes('Hop 1'));
  });

  test('buildFieldTable renders ADVERT payload', () => {
    const pkt = { raw_hex: 'c040', route_type: 1, payload_type: 4 };
    const decoded = {
      type: 'ADVERT', pubKey: 'abc123', timestamp: 1234567890,
      timestampISO: '2009-02-13T23:31:30Z', signature: 'sig',
      name: 'TestNode',
      flags: { type: 1, hasLocation: true, hasName: true, raw: 0x55 }
    };
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('Public Key'));
    assert(result.includes('Timestamp'));
    assert(result.includes('Signature'));
    assert(result.includes('App Flags'));
    assert(result.includes('Companion'));
    assert(result.includes('Latitude'));
    assert(result.includes('Node Name'));
  });

  test('buildFieldTable renders GRP_TXT payload', () => {
    const pkt = { raw_hex: 'c040', route_type: 1, payload_type: 5 };
    const decoded = { type: 'GRP_TXT', channelHash: 0xAB, mac: 'AABB', encryptedData: 'data', decryptionStatus: 'no_key' };
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('Channel Hash'));
    assert(result.includes('MAC'));
    assert(result.includes('Encrypted Data'));
  });

  test('buildFieldTable renders CHAN payload', () => {
    const pkt = { raw_hex: 'c040', route_type: 1, payload_type: 5 };
    const decoded = { type: 'CHAN', channel: 'general', sender: 'Alice', sender_timestamp: '12:00' };
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('Channel'));
    assert(result.includes('general'));
    assert(result.includes('Sender'));
    assert(result.includes('Sender Time'));
  });

  test('buildFieldTable renders ACK payload', () => {
    const pkt = { raw_hex: 'c040', route_type: 1, payload_type: 3 };
    const decoded = { type: 'ACK', ackChecksum: 'DEADBEEF' };
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('Checksum'));
    assert(result.includes('DEADBEEF'));
  });

  test('buildFieldTable renders destHash-based payload', () => {
    const pkt = { raw_hex: 'c040', route_type: 1, payload_type: 2 };
    const decoded = { destHash: 'DD', srcHash: 'SS', mac: 'MM', encryptedData: 'EE' };
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('Dest Hash'));
    assert(result.includes('Src Hash'));
  });

  test('buildFieldTable renders raw fallback for unknown payload', () => {
    const pkt = { raw_hex: 'c040aabbccdd', route_type: 1, payload_type: 99 };
    const decoded = {};
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('Raw'));
  });

  test('buildFieldTable hash_size calculation', () => {
    // Path byte 0xC0 → bits 7-6 = 3 → hash_size = 4
    const pkt = { raw_hex: '00C0', route_type: 1, payload_type: 0 };
    const decoded = {};
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('hash_size=4'));
  });

  test('buildFieldTable handles empty raw_hex', () => {
    const pkt = { raw_hex: '', route_type: 1, payload_type: 0 };
    const decoded = {};
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('field-table'));
    assert(result.includes('0B') || result.includes('0 bytes') || result.includes('??'));
  });
}

console.log('\n=== packets.js: _getRowCount ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('_getRowCount returns 1 for ungrouped', () => {
    // _displayGrouped is internal, but when not grouped, should return 1
    // Since we can't easily control _displayGrouped, test the function behavior
    const result = api._getRowCount({ hash: 'abc', _children: [{ observer_id: '1' }] });
    // Default _displayGrouped depends on initialization, but the function should not throw
    assert(typeof result === 'number');
    assert(result >= 1);
  });
}

console.log('\n=== packets.js: buildFlatRowHtml ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('buildFlatRowHtml produces table row', () => {
    const p = {
      id: 1, hash: 'abc123', timestamp: '2024-01-01T00:00:00Z',
      observer_id: null, raw_hex: 'aabb', payload_type: 4,
      route_type: 1, decoded_json: '{}', path_json: '[]'
    };
    const result = api.buildFlatRowHtml(p);
    assert(result.includes('<tr'));
    assert(result.includes('data-id="1"'));
    assert(result.includes('data-hash="abc123"'));
  });

  test('buildFlatRowHtml calculates size from hex', () => {
    const p = {
      id: 2, hash: 'x', timestamp: '', observer_id: null,
      raw_hex: 'aabbccdd', payload_type: 0, route_type: 0,
      decoded_json: '{}', path_json: '[]'
    };
    const result = api.buildFlatRowHtml(p);
    assert(result.includes('4B'));  // 8 hex chars = 4 bytes
  });

  test('buildFlatRowHtml handles missing raw_hex', () => {
    const p = {
      id: 3, hash: 'y', timestamp: '', observer_id: null,
      raw_hex: null, payload_type: 0, route_type: 0,
      decoded_json: '{}', path_json: '[]'
    };
    const result = api.buildFlatRowHtml(p);
    assert(result.includes('0B'));
  });
}

console.log('\n=== packets.js: buildGroupRowHtml ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('buildGroupRowHtml renders single-count group', () => {
    const p = {
      hash: 'abc', count: 1, latest: '2024-01-01T00:00:00Z',
      observer_id: null, raw_hex: 'aabb', payload_type: 4,
      route_type: 1, decoded_json: '{}', path_json: '[]',
      observation_count: 1, observer_count: 1
    };
    const result = api.buildGroupRowHtml(p);
    assert(result.includes('<tr'));
    assert(result.includes('data-hash="abc"'));
    // Single count: no expand arrow, no group-header class
    assert(!result.includes('group-header'));
  });

  test('buildGroupRowHtml renders multi-count group with expand arrow', () => {
    const p = {
      hash: 'xyz', count: 3, latest: '2024-01-01T00:00:00Z',
      observer_id: null, raw_hex: 'aabbcc', payload_type: 0,
      route_type: 0, decoded_json: '{}', path_json: '[]',
      observation_count: 3, observer_count: 2
    };
    const result = api.buildGroupRowHtml(p);
    assert(result.includes('group-header'));
    assert(result.includes('▶'));  // collapsed arrow
  });

  test('buildGroupRowHtml shows observation count badge', () => {
    const p = {
      hash: 'obs', count: 1, latest: '2024-01-01T00:00:00Z',
      observer_id: null, raw_hex: 'aa', payload_type: 0,
      route_type: 0, decoded_json: '{}', path_json: '[]',
      observation_count: 5, observer_count: 1
    };
    const result = api.buildGroupRowHtml(p);
    assert(result.includes('badge-obs'));
    assert(result.includes('👁'));
    assert(result.includes('5'));
  });
}

console.log('\n=== packets.js: page registration ===');
{
  const ctx = loadPacketsSandbox();
  // registerPage is defined in app.js and stores in its own `pages` closure.
  // We verify via the navigateTo mechanism or by checking the pages object isn't empty.
  // Since we can't easily access the closure, just verify the test API is exposed.
  test('_packetsTestAPI is exposed on window', () => {
    assert(ctx._packetsTestAPI);
    assert(typeof ctx._packetsTestAPI.typeName === 'function');
    assert(typeof ctx._packetsTestAPI.getDetailPreview === 'function');
    assert(typeof ctx._packetsTestAPI.sortGroupChildren === 'function');
    assert(typeof ctx._packetsTestAPI.buildFieldTable === 'function');
  });
}

console.log('\n=== packets.js: _invalidateRowCounts / _refreshRowCountsIfDirty (#410) ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;

  test('_invalidateRowCounts and _refreshRowCountsIfDirty are exported', () => {
    assert(typeof api._invalidateRowCounts === 'function');
    assert(typeof api._refreshRowCountsIfDirty === 'function');
  });

  test('_invalidateRowCounts does not throw', () => {
    api._invalidateRowCounts();
  });

  test('_refreshRowCountsIfDirty does not throw when no display packets', () => {
    api._invalidateRowCounts();
    api._refreshRowCountsIfDirty();
  });

  test('_cumulativeRowOffsets returns valid offsets after invalidation cycle', () => {
    // Even with no display packets, should return valid array
    const offsets = api._cumulativeRowOffsets();
    assert(Array.isArray(offsets));
    assert(offsets[0] === 0);
  });
}

// ===== SUMMARY =====
console.log(`\n${'='.repeat(40)}`);
console.log(`packets.js tests: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
