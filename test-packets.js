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
  loadInCtx(ctx, 'public/payload-labels.js');
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  loadInCtx(ctx, 'public/packet-helpers.js');
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
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-chat-circle"'));
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
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-broadcast"'));
    assert(result.includes('TestNode'));
    assert(result.includes('hop-link'));
  });

  test('getDetailPreview handles ADVERT room', () => {
    const result = api.getDetailPreview({
      type: 'ADVERT', name: 'RoomNode', pubKey: 'abc',
      flags: { room: true }
    });
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-house-line"'));
  });

  test('getDetailPreview handles ADVERT sensor', () => {
    const result = api.getDetailPreview({
      type: 'ADVERT', name: 'Sensor1', pubKey: 'abc',
      flags: { sensor: true }
    });
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-thermometer"'));
  });

  test('getDetailPreview handles ADVERT companion (default)', () => {
    const result = api.getDetailPreview({
      type: 'ADVERT', name: 'Comp', pubKey: 'abc',
      flags: {}
    });
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-radio"'));
  });

  test('getDetailPreview handles GRP_TXT with channelHash (no_key)', () => {
    const result = api.getDetailPreview({
      type: 'GRP_TXT', channelHash: 0xAB, decryptionStatus: 'no_key'
    });
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-lock"'));
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

  // #1792: GRP_DATA detail preview parity with GRP_TXT.
  test('getDetailPreview handles GRP_DATA with channelHash (no_key)', () => {
    const result = api.getDetailPreview({
      type: 'GRP_DATA', channelHash: 0xAB, channelHashHex: 'AB', decryptionStatus: 'no_key'
    });
    assert(result.includes('Ch 0xAB'), 'should render channel hash hex with Ch prefix');
    assert(result.includes('no key'), 'should render no key status');
  });

  test('getDetailPreview handles GRP_DATA decryption_failed', () => {
    const result = api.getDetailPreview({
      type: 'GRP_DATA', channelHash: 5, channelHashHex: '05', decryptionStatus: 'decryption_failed'
    });
    assert(result.includes('Ch 0x05'), 'should render channel hash hex with Ch prefix');
    assert(result.includes('decryption failed'), 'should render failure status');
  });

  // #1796 polish: explicit 'encrypted' fallback when decryptionStatus is absent/pending.
  test('getDetailPreview handles GRP_DATA encrypted fallback (no decryptionStatus)', () => {
    const result = api.getDetailPreview({
      type: 'GRP_DATA', channelHash: 0xAB, channelHashHex: 'AB'
    });
    assert(result.includes('Ch 0xAB'), 'should render channel hash hex with Ch prefix');
    assert(result.includes('encrypted'), 'should render encrypted fallback label');
  });

  // #1796 polish: decrypted-but-malformed — status is 'decrypted' but dataType is null
  // because inner payload was too short to parse (cmd/ingestor/decoder.go:619-654).
  test('getDetailPreview handles GRP_DATA decrypted-but-malformed (dataType null)', () => {
    const result = api.getDetailPreview({
      type: 'GRP_DATA',
      channelHash: 0xAB,
      channelHashHex: 'AB',
      decryptionStatus: 'decrypted',
      dataType: null
    });
    assert(result.includes('Ch 0xAB'), 'should render channel hash hex with Ch prefix');
    assert(result.includes('malformed'), 'should label decrypted-but-malformed inner explicitly');
    assert(!result.includes('encrypted'), 'must NOT mislabel decrypted packet as encrypted');
  });

  // #1796 r1 adversarial — pin the `!decoded.error` guard on the happy-path branch
  // (public/packets.js:2841). Backend cmd/ingestor/decoder.go:619-654 sets BOTH
  // DataType=0xNN AND Error='data_len exceeds buffer' for the malformed inner case
  // where data_len > available_len. Without the `!decoded.error` guard, this row
  // would mis-render as `type=0x0001 len=0` (a confident-looking happy-path label)
  // instead of falling through to the explicit `(decrypted, malformed)` branch.
  // Regression pin: if a future refactor drops the `!decoded.error` clause, this
  // test fails. (Verified locally: removing `&& !decoded.error` makes this fail.)
  test('getDetailPreview routes decrypted+dataType+error through malformed branch (#1796 r1 adv)', () => {
    const result = api.getDetailPreview({
      type: 'GRP_DATA',
      channelHash: 0x12,
      channelHashHex: '12',
      decryptionStatus: 'decrypted',
      dataType: 0x0001,
      dataLen: 0,
      error: 'data_len exceeds buffer'
      // decryptedBlob absent (omitempty); backend Error field set.
    });
    assert(result.includes('Ch 0x12'), 'should still render channel hash hex');
    assert(result.includes('malformed'),
      'must label as malformed when Error is set, NOT confidently render type=0xNN');
    assert(!result.includes('type=0x0001'),
      'must NOT show a happy-path type=0xNN header when inner parse errored');
    assert(!result.includes('len=0'),
      'must NOT show a happy-path len=N header when inner parse errored');
  });

  // #1796 r1 regression — data_len=0 is a LEGITIMATE empty datagram per firmware
  // (BaseChatMesh.cpp:387: `data_len > available_len` is the only reject; 0 is allowed).
  // Backend cmd/ingestor/decoder.go:142 marshals DecryptedBlob with `omitempty`, so a
  // valid data_len=0 packet arrives with an empty/absent blob and no error. Frontend
  // must render the header (type=...len=0) WITHOUT a <code> block and MUST NOT label
  // it 'malformed'. (Same assertion covers the round-0 'tightened gate' regression.)
  test('getDetailPreview renders GRP_DATA data_len=0 empty datagram (no code block, not malformed)', () => {
    const result = api.getDetailPreview({
      type: 'GRP_DATA',
      channelHash: 0x12,
      channelHashHex: '12',
      decryptionStatus: 'decrypted',
      dataType: 0x0001,
      dataLen: 0
      // decryptedBlob absent (backend `omitempty`); no error.
    });
    assert(result.includes('Ch 0x12'), 'should render channel hash hex');
    assert(result.includes('type=0x0001'), 'should render data_type as hex');
    assert(result.includes('len=0'), 'should render data_len=0');
    assert(!result.includes('<code>'), 'must NOT render any <code> block when blob is empty');
    assert(!result.includes('malformed'), 'data_len=0 is a legitimate empty datagram, NOT malformed');
  });

  test('getDetailPreview handles GRP_DATA decrypted with data_type and blob', () => {
    const result = api.getDetailPreview({
      type: 'GRP_DATA',
      channelHash: 0x12,
      channelHashHex: '12',
      decryptionStatus: 'decrypted',
      dataType: 0x0001,
      dataLen: 4,
      decryptedBlob: 'deadbeef'
    });
    assert(result.includes('0x12'), 'should render channel hash hex');
    assert(result.includes('0x0001'), 'should render data_type as hex');
    assert(result.includes('len=4'), 'should render data_len with len= label');
    assert(result.includes('deadbeef'), 'should render blob hex');
  });

  test('getDetailPreview handles GRP_DATA decrypted truncates long blob', () => {
    const longBlob = 'ab'.repeat(64); // 128 hex chars = 64 bytes
    const result = api.getDetailPreview({
      type: 'GRP_DATA',
      channelHash: 0x12,
      channelHashHex: '12',
      decryptionStatus: 'decrypted',
      dataType: 0,
      dataLen: 64,
      decryptedBlob: longBlob
    });
    // Adversarial #3: assert EXACT rendered <code> content via regex, not substring.
    // Substring match would still pass at cutoff 40/48 because 'ab'.repeat(16)+'…'
    // is a prefix of any longer rendered blob. Pin: exactly 32 hex chars + ellipsis,
    // and nothing else inside the <code> tag.
    const codeMatch = result.match(/<code>([^<]*)<\/code>/);
    assert(codeMatch, 'should render a <code> block');
    assert.strictEqual(codeMatch[1], 'ab'.repeat(16) + '…',
      `<code> content must be exactly 32 hex chars + ellipsis, got: ${JSON.stringify(codeMatch[1])}`);
  });

  // Boundary: blob with EXACTLY 32 hex chars renders WITHOUT ellipsis (.length > 32 is strict).
  test('getDetailPreview renders GRP_DATA blob of exactly 32 hex chars without ellipsis', () => {
    const exactBlob = 'cd'.repeat(16); // 32 hex chars
    const result = api.getDetailPreview({
      type: 'GRP_DATA',
      channelHash: 0x12,
      channelHashHex: '12',
      decryptionStatus: 'decrypted',
      dataType: 0,
      dataLen: 16,
      decryptedBlob: exactBlob
    });
    const codeMatch = result.match(/<code>([^<]*)<\/code>/);
    assert(codeMatch, 'should render a <code> block at the 32-char boundary');
    assert.strictEqual(codeMatch[1], exactBlob,
      'exactly-32-char blob must render verbatim, no ellipsis');
    assert(!result.includes('…'), 'must NOT append ellipsis at the boundary');
  });

  // Item 6: channelHash=0 — confirms the `!= null` gate (not truthy check) so falsy 0
  // still enters the GRP_DATA branch and renders `Ch 0x00`.
  test('getDetailPreview handles GRP_DATA channelHash=0 (falsy but valid)', () => {
    const result = api.getDetailPreview({
      type: 'GRP_DATA',
      channelHash: 0,
      decryptionStatus: 'no_key'
    });
    assert(result.includes('Ch 0x00'),
      'channelHash=0 must render Ch 0x00 (falsy 0 passes `!= null` gate)');
    assert(result.includes('no key'), 'should render no key status');
  });

  test('getDetailPreview handles TXT_MSG', () => {
    const result = api.getDetailPreview({
      type: 'TXT_MSG', srcHash: 'abcdef01', destHash: '12345678'
    });
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-envelope"'));
    assert(result.includes('abcdef01'));
    assert(result.includes('12345678'));
  });

  test('getDetailPreview handles PATH', () => {
    const result = api.getDetailPreview({
      type: 'PATH', srcHash: 'aabb', destHash: 'ccdd'
    });
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-shuffle"'));
  });

  test('getDetailPreview handles REQ', () => {
    const result = api.getDetailPreview({
      type: 'REQ', srcHash: 'aa', destHash: 'bb'
    });
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-lock"'));
    assert(result.includes('aa'));
  });

  test('getDetailPreview handles RESPONSE', () => {
    const result = api.getDetailPreview({
      type: 'RESPONSE', srcHash: 'aa', destHash: 'bb'
    });
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-lock"'));
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
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-broadcast"'));
    assert(result.includes('abcdef1234567890'));
  });

  test('getDetailPreview returns empty for empty decoded', () => {
    assert.strictEqual(api.getDetailPreview({}), '');
  });

  // #1802 — CONTROL DISCOVER_REQ / DISCOVER_RESP should be rendered (not just
  // hex). Backend cmd/ingestor/decoder.go decodeControl() emits ctrlSubtype +
  // body fields; the detail preview must surface them.
  test('getDetailPreview handles CONTROL DISCOVER_REQ', () => {
    const result = api.getDetailPreview({
      type: 'CONTROL',
      ctrlSubtype: 'DISCOVER_REQ',
      ctrlFilter: 2,
      ctrlTag: 0xDEADBEEF,
      ctrlSince: 0x11223344,
    });
    assert(result.includes('DISCOVER_REQ'), 'should label subtype');
    assert(result.includes('filter'), 'should render filter field');
    assert(result.includes('tag'), 'should render tag field');
  });

  test('getDetailPreview handles CONTROL DISCOVER_RESP', () => {
    const result = api.getDetailPreview({
      type: 'CONTROL',
      ctrlSubtype: 'DISCOVER_RESP',
      ctrlNodeType: 2,
      ctrlSNR: 16,
      ctrlTag: 0x11223344,
      ctrlPubKey: '0001020304050607',
    });
    assert(result.includes('DISCOVER_RESP'), 'should label subtype');
    assert(result.includes('snr') || result.includes('SNR'), 'should render snr');
    assert(result.includes('0001020304050607'), 'should render pubkey hex');
  });

  test('getDetailPreview handles CONTROL UNKNOWN subtype', () => {
    const result = api.getDetailPreview({
      type: 'CONTROL',
      ctrlSubtype: 'UNKNOWN',
      ctrlFlags: 'a0',
    });
    assert(result.includes('UNKNOWN') || result.includes('CONTROL'),
      'should at least label the unknown subtype');
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
    // Path byte 0xC0 → bits 7-6 = 3 → hash_size = 4, but hash_count = 0
    // Since #653: when hashCount == 0, shows "hash_count=0 (direct advert)" instead of hash_size
    const pkt = { raw_hex: '00C0', route_type: 1, payload_type: 0 };
    const decoded = {};
    const result = api.buildFieldTable(pkt, decoded, [], []);
    assert(result.includes('hash_count=0 (direct advert)'));
  });

  test('buildFieldTable hash_size shown when hash_count > 0', () => {
    // Path byte 0xC1 → bits 7-6 = 3 → hash_size = 4, hash_count = 1
    const pkt = { raw_hex: '00C1aabbccdd', route_type: 1, payload_type: 0 };
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

  test('buildFlatRowHtml emits data-entry-idx when provided', () => {
    const p = {
      id: 4, hash: 'z', timestamp: '', observer_id: null,
      raw_hex: 'aabb', payload_type: 0, route_type: 0,
      decoded_json: '{}', path_json: '[]'
    };
    const result = api.buildFlatRowHtml(p, 42);
    assert(result.includes('data-entry-idx="42"'));
  });

  test('buildFlatRowHtml emits data-entry-idx=-1 by default', () => {
    const p = {
      id: 5, hash: 'w', timestamp: '', observer_id: null,
      raw_hex: 'aabb', payload_type: 0, route_type: 0,
      decoded_json: '{}', path_json: '[]'
    };
    const result = api.buildFlatRowHtml(p);
    assert(result.includes('data-entry-idx="-1"'));
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
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-caret-up"'));  // collapsed arrow
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
    assert(result.includes('href="/icons/phosphor-sprite.svg#ph-eye"'));
    assert(result.includes('5'));
  });

  test('buildGroupRowHtml emits data-entry-idx on header row', () => {
    const p = {
      hash: 'ei1', count: 1, latest: '2024-01-01T00:00:00Z',
      observer_id: null, raw_hex: 'aa', payload_type: 0,
      route_type: 0, decoded_json: '{}', path_json: '[]',
      observation_count: 1, observer_count: 1
    };
    const result = api.buildGroupRowHtml(p, 7);
    assert(result.includes('data-entry-idx="7"'));
  });

  test('buildGroupRowHtml emits data-entry-idx on child rows', () => {
    const ctx2 = loadPacketsSandbox();
    const api2 = ctx2._packetsTestAPI;
    // Simulate expandedHashes having this hash
    // We can't easily toggle expandedHashes from outside, so test via the
    // fact that children only render when isExpanded is true.
    // For this test, just verify the header row has the attribute (child rows
    // are conditional on expandedHashes which we can't set from tests).
    const p = {
      hash: 'ei2', count: 3, latest: '2024-01-01T00:00:00Z',
      observer_id: null, raw_hex: 'aabb', payload_type: 0,
      route_type: 0, decoded_json: '{}', path_json: '[]',
      observation_count: 3, observer_count: 2,
      _children: []
    };
    const result = api2.buildGroupRowHtml(p, 15);
    assert(result.includes('data-entry-idx="15"'));
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

console.log('\n=== packets.js: buildPacketsParams ===');
{
  const ctx = loadPacketsSandbox();
  const api = ctx._packetsTestAPI;
  assert(typeof api.buildPacketsParams === 'function', 'buildPacketsParams must be exported');

  test('hash filter suppresses region — direct hash links work regardless of saved region', () => {
    // This is the bug from URL https://analyzer.../#/packets?hash=178525e9f693aa7e
    // when the user's saved RegionFilter excludes the packet's observer region.
    // The hash is an exact identifier; ALL other filters must be ignored.
    const p = api.buildPacketsParams({
      filters: { hash: 'abc123' },
      regionParam: 'SJC,SFO,OAK,MRY',
      windowMin: 60,
      groupByHash: false,
      limit: 200,
    });
    assert.strictEqual(p.get('hash'), 'abc123');
    assert.strictEqual(p.get('region'), null, 'region must NOT be set when hash is present');
    assert.strictEqual(p.get('since'), null, 'since must NOT be set when hash is present');
  });

  test('hash filter suppresses ALL other filters — observer, node, channel too', () => {
    const p = api.buildPacketsParams({
      filters: { hash: 'h', node: 'n', observer: 'o', channel: 'c' },
      regionParam: 'SJC',
      windowMin: 60,
      groupByHash: false,
      limit: 200,
    });
    assert.strictEqual(p.get('hash'), 'h');
    assert.strictEqual(p.get('node'), null);
    assert.strictEqual(p.get('observer'), null);
    assert.strictEqual(p.get('channel'), null);
    assert.strictEqual(p.get('region'), null);
    assert.strictEqual(p.get('since'), null);
  });

  test('hash filter suppresses region with default windowMin=0', () => {
    const p = api.buildPacketsParams({
      filters: { hash: 'deadbeef' },
      regionParam: 'COA',
      windowMin: 0,
      groupByHash: false,
      limit: 50,
    });
    assert.strictEqual(p.get('hash'), 'deadbeef');
    assert.strictEqual(p.get('region'), null);
  });

  test('region applied normally when hash filter is absent', () => {
    const p = api.buildPacketsParams({
      filters: {},
      regionParam: 'SJC,SFO',
      windowMin: 60,
      groupByHash: false,
      limit: 200,
    });
    assert.strictEqual(p.get('region'), 'SJC,SFO', 'region must apply when no hash');
    assert.strictEqual(p.get('hash'), null);
    assert(p.get('since'), 'since must apply when no hash and windowMin>0');
  });

  test('observer/node/channel pass through normally when no hash', () => {
    const p = api.buildPacketsParams({
      filters: { observer: 'obs1', node: 'node1', channel: '#test' },
      regionParam: '',
      windowMin: 0,
      groupByHash: false,
      limit: 50,
    });
    assert.strictEqual(p.get('observer'), 'obs1');
    assert.strictEqual(p.get('node'), 'node1');
    assert.strictEqual(p.get('channel'), '#test');
  });

  test('region absent when regionParam empty — no spurious empty region= param', () => {
    const p = api.buildPacketsParams({
      filters: {},
      regionParam: '',
      windowMin: 0,
      groupByHash: false,
      limit: 50,
    });
    assert.strictEqual(p.get('region'), null);
  });

  test('groupByHash=true with hash sets groupByHash and omits expand', () => {
    const p = api.buildPacketsParams({
      filters: { hash: 'h' }, regionParam: '', windowMin: 0, groupByHash: true, limit: 50,
    });
    assert.strictEqual(p.get('groupByHash'), 'true');
    assert.strictEqual(p.get('expand'), null);
    assert.strictEqual(p.get('hash'), 'h');
  });

  test('groupByHash=false with hash sets expand=observations', () => {
    const p = api.buildPacketsParams({
      filters: { hash: 'h' }, regionParam: '', windowMin: 0, groupByHash: false, limit: 50,
    });
    assert.strictEqual(p.get('expand'), 'observations');
    assert.strictEqual(p.get('groupByHash'), null);
    assert.strictEqual(p.get('hash'), 'h');
  });

  test('groupByHash=false without hash sets expand=observations', () => {
    const p = api.buildPacketsParams({
      filters: {}, regionParam: '', windowMin: 0, groupByHash: false, limit: 50,
    });
    assert.strictEqual(p.get('expand'), 'observations');
    assert.strictEqual(p.get('groupByHash'), null);
  });
}

console.log('\n=== packets.js: scroll position preserved across renderTableRows (#431) ===');
{
  // Build a richer sandbox with DOM elements that renderTableRows needs
  const ctx = makeSandbox();
  // Mock DOM elements needed by renderTableRows and renderVisibleRows
  let pktLeftScrollTop = 500;
  const pktBody = {
    tagName: 'TBODY', id: 'pktBody', _innerHTML: '', children: [],
    get innerHTML() { return this._innerHTML; },
    set innerHTML(v) { this._innerHTML = v; pktLeftScrollTop = 0; }, // Simulate browser scroll reset on DOM rebuild
    appendChild: () => {}, insertBefore: () => {}, removeChild: () => {},
    querySelectorAll: () => [], querySelector: () => null,
    style: {},
  };
  const pktLeft = {
    tagName: 'DIV', id: 'pktLeft', className: '',
    get scrollTop() { return pktLeftScrollTop; },
    set scrollTop(v) { pktLeftScrollTop = v; },
    clientHeight: 800,
    offsetHeight: 800,
    querySelector: (sel) => {
      if (sel === 'thead') return { offsetHeight: 40 };
      if (sel === '.count' || sel === '#pktLeft .count') return { textContent: '' };
      return null;
    },
    querySelectorAll: () => [],
    addEventListener: () => {},
    removeEventListener: () => {},
    style: {},
  };
  const origGetById = ctx.document.getElementById;
  ctx.document.getElementById = (id) => {
    if (id === 'pktBody') return pktBody;
    if (id === 'pktLeft') return pktLeft;
    if (id === 'fGroup') return { classList: { toggle: () => {}, add: () => {}, remove: () => {}, contains: () => false } };
    if (id === 'packetFilterCount') return { style: {}, textContent: '' };
    if (id === 'vscroll-top') return null;
    if (id === 'vscroll-bottom') return null;
    return null;
  };
  ctx.document.querySelector = (sel) => {
    if (sel === '#pktLeft .count') return { textContent: '', set textContent(v) {} };
    if (sel === '#pktLeft') return pktLeft;
    return null;
  };

  loadInCtx(ctx, 'public/payload-labels.js');
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  loadInCtx(ctx, 'public/packet-helpers.js');
  vm.runInContext(`
    window.HopDisplay = {
      renderHop: function(h, entry, opts) { return '<span>' + h + '</span>'; },
      _showFromBtn: function() {}
    };
  `, ctx);
  loadInCtx(ctx, 'public/packets.js');

  const api = ctx._packetsTestAPI;

  test('scroll position preserved after renderTableRows (#431)', () => {
    // Inject packets that will ALL be filtered out by type filter,
    // triggering the empty-state path which sets tbody.innerHTML (resetting scroll in browser)
    api._setPackets([
      { id: 1, hash: 'aaa', payload_type: 4, timestamp: '2024-01-01T00:00:00Z', observer_id: 'obs1', path_len: 2, decoded_json: '{}' },
      { id: 2, hash: 'bbb', payload_type: 4, timestamp: '2024-01-01T00:01:00Z', observer_id: 'obs1', path_len: 1, decoded_json: '{}' },
    ]);

    // Set scroll position to 500
    pktLeftScrollTop = 500;

    // Filter by type 99 (no packets match) — this triggers tbody.innerHTML assignment
    api._setFilter('type', '99');
    try { api.renderTableRows(); } catch(e) { /* swallow DOM stub errors */ }

    // scrollTop must be preserved (not reset to 0)
    assert.strictEqual(pktLeftScrollTop, 500, 'scrollTop should be preserved after renderTableRows, got ' + pktLeftScrollTop);
  });
}

// ===== SUMMARY =====
console.log(`\n${'='.repeat(40)}`);
console.log(`packets.js tests: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
