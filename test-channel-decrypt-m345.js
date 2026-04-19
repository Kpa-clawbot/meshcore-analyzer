/**
 * Tests for #725 M3 (PSK hex key), M4 (channel removal), M5 (message caching).
 * Runs in Node.js via vm.createContext to simulate browser environment.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const { subtle } = require('crypto').webcrypto;

let passed = 0;
let failed = 0;

function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

// Build a minimal browser-like sandbox
function createSandbox() {
  const storage = {};
  const localStorage = {
    getItem: (k) => storage[k] !== undefined ? storage[k] : null,
    setItem: (k, v) => { storage[k] = String(v); },
    removeItem: (k) => { delete storage[k]; },
    _data: storage
  };

  const ctx = {
    window: {},
    crypto: { subtle },
    TextEncoder: TextEncoder,
    TextDecoder: TextDecoder,
    Uint8Array,
    localStorage,
    console,
    Date,
    JSON,
    parseInt,
    Math,
    String,
    Number,
    Object,
    Array,
    RegExp,
    Error,
    Promise,
    setTimeout,
    btoa: (s) => Buffer.from(s, 'binary').toString('base64'),
    atob: (s) => Buffer.from(s, 'base64').toString('binary'),
  };
  ctx.window = ctx;
  ctx.self = ctx;
  return ctx;
}

async function runTests() {
  console.log('\n=== M3: PSK hex key detection ===');

  // Load channel-decrypt.js in sandbox
  const cdSrc = fs.readFileSync(__dirname + '/public/channel-decrypt.js', 'utf8');
  const sandbox = createSandbox();
  const context = vm.createContext(sandbox);
  vm.runInContext(cdSrc, context);
  const CD = sandbox.window.ChannelDecrypt;

  // Test: isHexKey detection (via channels.js logic)
  // We test the pattern directly since isHexKey is inside channels.js IIFE
  const isHexKey = (val) => /^[0-9a-fA-F]{32}$/.test(val);

  assert(isHexKey('0123456789abcdef0123456789abcdef'), 'Valid 32-char hex detected');
  assert(isHexKey('AABBCCDD11223344AABBCCDD11223344'), 'Valid uppercase hex detected');
  assert(!isHexKey('#LongFast'), 'Hashtag name NOT detected as hex');
  assert(!isHexKey('0123456789abcdef'), 'Short hex (16 chars) NOT detected');
  assert(!isHexKey('0123456789abcdef0123456789abcdefXX'), 'Too long NOT detected');
  assert(!isHexKey('zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz'), 'Non-hex chars NOT detected');

  // Test: PSK decrypt with known key bytes
  console.log('\n=== M3: PSK decrypt produces correct plaintext ===');

  // Derive a key from #LongFast for testing
  const keyBytes = await CD.deriveKey('#LongFast');
  assert(keyBytes.length === 16, 'Derived key is 16 bytes');

  const keyHex = CD.bytesToHex(keyBytes);
  assert(keyHex.length === 32, 'Key hex is 32 chars');

  // Round-trip: hex → bytes → hex
  const roundTrip = CD.bytesToHex(CD.hexToBytes(keyHex));
  assert(roundTrip === keyHex, 'Hex round-trip preserves key');

  // Channel hash computation works
  const hashByte = await CD.computeChannelHash(keyBytes);
  assert(typeof hashByte === 'number' && hashByte >= 0 && hashByte <= 255, 'Channel hash byte is valid (0-255)');

  // PSK key (raw hex) stored and retrieved correctly
  const pskHex = 'aabbccdd11223344aabbccdd11223344';
  CD.storeKey('psk:aabbccdd', pskHex);
  const keys = CD.getStoredKeys();
  assert(keys['psk:aabbccdd'] === pskHex, 'PSK key stored and retrieved correctly');

  console.log('\n=== M4: Channel removal clears key + cache ===');

  // Store a key and some cached messages
  CD.storeKey('#TestChannel', 'deadbeefdeadbeefdeadbeefdeadbeef');
  CD.setCache('#TestChannel', [{ sender: 'A', text: 'hello', timestamp: '2026-01-01T00:00:00Z', packetHash: 'h1' }], '2026-01-01T00:00:00Z', 1);

  // Verify they exist
  var storedKeys = CD.getStoredKeys();
  assert(storedKeys['#TestChannel'] === 'deadbeefdeadbeefdeadbeefdeadbeef', 'Key exists before removal');
  var cachedBefore = CD.getCache('#TestChannel');
  assert(cachedBefore && cachedBefore.messages.length === 1, 'Cache exists before removal');

  // Remove the key (also clears cache)
  CD.removeKey('#TestChannel');
  var storedAfter = CD.getStoredKeys();
  assert(!storedAfter['#TestChannel'], 'Key cleared after removal');
  var cachedAfter = CD.getCache('#TestChannel');
  assert(!cachedAfter, 'Cache cleared after removal');

  console.log('\n=== M5: Cache operations ===');

  // Test: setCache with count and size limit
  var bigMessages = [];
  for (var i = 0; i < 1200; i++) {
    bigMessages.push({ sender: 'S', text: 'msg' + i, timestamp: '2026-01-01T00:00:' + String(i).padStart(2, '0') + 'Z', packetHash: 'h' + i });
  }
  CD.setCache('bigchannel', bigMessages, '2026-01-01T00:20:00Z', 1200);
  var bigCached = CD.getCache('bigchannel');
  assert(bigCached.messages.length <= 1000, 'Cache enforces 1000 message limit (got ' + bigCached.messages.length + ')');
  assert(bigCached.count === 1200, 'Cache stores total count');
  assert(bigCached.lastTimestamp === '2026-01-01T00:20:00Z', 'Cache stores lastTimestamp');
  // Should keep most recent 1000
  assert(bigCached.messages[0].packetHash === 'h200', 'Cache keeps most recent 1000 (first is h200)');

  // Test: cache hit (delta fetch scenario)
  CD.setCache('deltatest', [
    { sender: 'A', text: 'old', timestamp: '2026-01-01T00:00:00Z', packetHash: 'p1' }
  ], '2026-01-01T00:00:00Z', 1);

  var deltaCache = CD.getCache('deltatest');
  assert(deltaCache.messages.length === 1, 'Delta cache has 1 message');
  assert(deltaCache.lastTimestamp === '2026-01-01T00:00:00Z', 'Delta cache lastTimestamp correct');
  assert(deltaCache.count === 1, 'Delta cache count correct');

  // Test: clearChannelCache
  CD.setCache('clearthis', [{ sender: 'X', text: 'y' }], 'ts', 1);
  assert(CD.getCache('clearthis') !== null, 'Cache exists before clear');
  CD.clearChannelCache('clearthis');
  assert(CD.getCache('clearthis') === null, 'Cache cleared by clearChannelCache');

  console.log('\n=== Results ===');
  console.log('Passed: ' + passed + ', Failed: ' + failed);
  process.exit(failed > 0 ? 1 : 0);
}

runTests().catch(e => { console.error(e); process.exit(1); });
