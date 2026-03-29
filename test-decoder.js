/* Unit tests for decoder.js */
'use strict';
const assert = require('assert');
const { decodePacket, validateAdvert, ROUTE_TYPES, PAYLOAD_TYPES, VALID_ROLES } = require('./decoder');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// === Constants ===
console.log('\n=== Constants ===');
test('ROUTE_TYPES has 4 entries', () => assert.strictEqual(Object.keys(ROUTE_TYPES).length, 4));
test('PAYLOAD_TYPES has 13 entries', () => assert.strictEqual(Object.keys(PAYLOAD_TYPES).length, 13));
test('VALID_ROLES has repeater, companion, room, sensor', () => {
  for (const r of ['repeater', 'companion', 'room', 'sensor']) assert(VALID_ROLES.has(r));
});

// === Header decoding ===
console.log('\n=== Header decoding ===');
test('FLOOD + ADVERT = 0x11', () => {
  const p = decodePacket('1100' + '00'.repeat(101));
  assert.strictEqual(p.header.routeType, 1);
  assert.strictEqual(p.header.routeTypeName, 'FLOOD');
  assert.strictEqual(p.header.payloadType, 4);
  assert.strictEqual(p.header.payloadTypeName, 'ADVERT');
});

test('TRANSPORT_FLOOD = routeType 0', () => {
  // header=0x00 (TRANSPORT_FLOOD + REQ), transportCodes=AABB+CCDD, pathByte=0x00, payload
  const hex = '00' + 'AABB' + 'CCDD' + '00' + '00'.repeat(16);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.routeType, 0);
  assert.strictEqual(p.header.routeTypeName, 'TRANSPORT_FLOOD');
  assert.notStrictEqual(p.transportCodes, null);
  assert.strictEqual(p.transportCodes.code1, 'AABB');
  assert.strictEqual(p.transportCodes.code2, 'CCDD');
});

test('TRANSPORT_DIRECT = routeType 3', () => {
  const hex = '03' + '1122' + '3344' + '00' + '00'.repeat(16);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.routeType, 3);
  assert.strictEqual(p.header.routeTypeName, 'TRANSPORT_DIRECT');
  assert.strictEqual(p.transportCodes.code1, '1122');
});

test('DIRECT = routeType 2, no transport codes', () => {
  const hex = '0200' + '00'.repeat(16);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.routeType, 2);
  assert.strictEqual(p.header.routeTypeName, 'DIRECT');
  assert.strictEqual(p.transportCodes, null);
});

test('payload version extracted', () => {
  // 0xC1 = 11_0000_01 → version=3, payloadType=0, routeType=1
  const hex = 'C100' + '00'.repeat(16);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.payloadVersion, 3);
});

// === Path decoding ===
console.log('\n=== Path decoding ===');
test('hashSize=1, hashCount=3', () => {
  // pathByte = 0x03 → (0>>6)+1=1, 3&0x3F=3
  const hex = '1103' + 'AABBCC' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.path.hashSize, 1);
  assert.strictEqual(p.path.hashCount, 3);
  assert.strictEqual(p.path.hops.length, 3);
  assert.strictEqual(p.path.hops[0], 'AA');
  assert.strictEqual(p.path.hops[1], 'BB');
  assert.strictEqual(p.path.hops[2], 'CC');
});

test('hashSize=2, hashCount=2', () => {
  // pathByte = 0x42 → (1>>0=1)+1=2, 2&0x3F=2
  const hex = '1142' + 'AABB' + 'CCDD' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.path.hashSize, 2);
  assert.strictEqual(p.path.hashCount, 2);
  assert.strictEqual(p.path.hops[0], 'AABB');
  assert.strictEqual(p.path.hops[1], 'CCDD');
});

test('hashSize=4 from pathByte 0xC1', () => {
  // 0xC1 = 11_000001 → hashSize=(3)+1=4, hashCount=1
  const hex = '11C1' + 'DEADBEEF' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.path.hashSize, 4);
  assert.strictEqual(p.path.hashCount, 1);
  assert.strictEqual(p.path.hops[0], 'DEADBEEF');
});

test('zero hops', () => {
  const hex = '1100' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.path.hashCount, 0);
  assert.strictEqual(p.path.hops.length, 0);
});

// === Payload types ===
console.log('\n=== ADVERT payload ===');
test('ADVERT with name and location', () => {
  const pkt = decodePacket(
    '11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172'
  );
  assert.strictEqual(pkt.payload.type, 'ADVERT');
  assert.strictEqual(pkt.payload.name, 'Kpa Roof Solar');
  assert(pkt.payload.pubKey.length === 64);
  assert(pkt.payload.timestamp > 0);
  assert(pkt.payload.timestampISO);
  assert(pkt.payload.signature.length === 128);
});

test('ADVERT flags: chat type=1', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  const flags = '01'; // type=1 → chat
  const hex = '1100' + pubKey + ts + sig + flags;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.type, 1);
  assert.strictEqual(p.payload.flags.chat, true);
  assert.strictEqual(p.payload.flags.repeater, false);
});

test('ADVERT flags: repeater type=2', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  const flags = '02';
  const hex = '1100' + pubKey + ts + sig + flags;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.type, 2);
  assert.strictEqual(p.payload.flags.repeater, true);
});

test('ADVERT flags: room type=3', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  const flags = '03';
  const hex = '1100' + pubKey + ts + sig + flags;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.type, 3);
  assert.strictEqual(p.payload.flags.room, true);
});

test('ADVERT flags: sensor type=4', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  const flags = '04';
  const hex = '1100' + pubKey + ts + sig + flags;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.type, 4);
  assert.strictEqual(p.payload.flags.sensor, true);
});

test('ADVERT flags: hasLocation', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  // flags=0x12 → type=2(repeater), hasLocation=true
  const flags = '12';
  const lat = '40420f00'; // 1000000 → 1.0 degrees
  const lon = '80841e00'; // 2000000 → 2.0 degrees
  const hex = '1100' + pubKey + ts + sig + flags + lat + lon;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.hasLocation, true);
  assert.strictEqual(p.payload.lat, 1.0);
  assert.strictEqual(p.payload.lon, 2.0);
});

test('ADVERT flags: hasName', () => {
  const pubKey = 'AB'.repeat(32);
  const ts = '01000000';
  const sig = 'CC'.repeat(64);
  // flags=0x82 → type=2(repeater), hasName=true
  const flags = '82';
  const name = Buffer.from('MyNode').toString('hex');
  const hex = '1100' + pubKey + ts + sig + flags + name;
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.flags.hasName, true);
  assert.strictEqual(p.payload.name, 'MyNode');
});

test('ADVERT too short', () => {
  const hex = '1100' + '00'.repeat(50);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== GRP_TXT payload ===');
test('GRP_TXT basic decode', () => {
  // payloadType=5 → (5<<2)|1 = 0x15
  const hex = '1500' + 'FF' + 'AABB' + 'CCDDEE';
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'GRP_TXT');
  assert.strictEqual(p.payload.channelHash, 0xFF);
  assert.strictEqual(p.payload.mac, 'aabb');
});

test('GRP_TXT too short', () => {
  const hex = '1500' + 'FF' + 'AA';
  const p = decodePacket(hex);
  assert(p.payload.error);
});

test('GRP_TXT has channelHashHex field', () => {
  const hex = '1500' + '1A' + 'AABB' + 'CCDDEE';
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.channelHashHex, '1A');
});

test('GRP_TXT channelHashHex zero-pads single digit', () => {
  const hex = '1500' + '03' + 'AABB' + 'CCDDEE';
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.channelHashHex, '03');
});

test('GRP_TXT decryptionStatus is no_key when no keys provided', () => {
  const hex = '1500' + 'FF' + 'AABB' + 'CCDDEE112233';
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.decryptionStatus, 'no_key');
});

test('GRP_TXT decryptionStatus is no_key when keys empty', () => {
  const hex = '1500' + 'FF' + 'AABB' + 'CCDDEE112233';
  const p = decodePacket(hex, {});
  assert.strictEqual(p.payload.decryptionStatus, 'no_key');
});

test('GRP_TXT decryptionStatus is decryption_failed with bad keys', () => {
  const hex = '1500' + 'FF' + 'AABB' + 'CCDDEE112233';
  const p = decodePacket(hex, { '#test': 'deadbeefdeadbeefdeadbeefdeadbeef' });
  assert.strictEqual(p.payload.decryptionStatus, 'decryption_failed');
});

test('GRP_TXT decryptionStatus is no_key when encrypted data too short', () => {
  // encryptedData < 10 hex chars (5 bytes) — not enough to attempt decryption
  const hex = '1500' + 'FF' + 'AABB' + 'CCDD';
  const p = decodePacket(hex, { '#test': 'deadbeefdeadbeefdeadbeefdeadbeef' });
  assert.strictEqual(p.payload.decryptionStatus, 'no_key');
});

test('GRP_TXT decryptionStatus is decrypted when key matches', () => {
  // Mock the ChannelCrypto module to simulate successful decryption
  const cryptoPath = require.resolve('@michaelhart/meshcore-decoder/dist/crypto/channel-crypto');
  const originalModule = require.cache[cryptoPath];
  require.cache[cryptoPath] = {
    id: cryptoPath,
    exports: {
      ChannelCrypto: {
        decryptGroupTextMessage: () => ({
          success: true,
          data: { sender: 'TestUser', message: 'Hello world', timestamp: 1700000000, flags: 0 },
        }),
      },
    },
  };
  try {
    const hex = '1500' + 'FF' + 'AABB' + 'CCDDEE112233';
    const p = decodePacket(hex, { '#general': 'aabbccddaabbccddaabbccddaabbccdd' });
    assert.strictEqual(p.payload.decryptionStatus, 'decrypted');
    assert.strictEqual(p.payload.type, 'CHAN');
    assert.strictEqual(p.payload.channelHashHex, 'FF');
    assert.strictEqual(p.payload.channel, '#general');
    assert.strictEqual(p.payload.sender, 'TestUser');
    assert.strictEqual(p.payload.text, 'TestUser: Hello world');
    assert.strictEqual(p.payload.sender_timestamp, 1700000000);
    assert.strictEqual(p.payload.flags, 0);
    assert.strictEqual(p.payload.channelHash, 0xFF);
  } finally {
    if (originalModule) require.cache[cryptoPath] = originalModule;
    else delete require.cache[cryptoPath];
  }
});

test('GRP_TXT decrypted without sender formats text correctly', () => {
  const cryptoPath = require.resolve('@michaelhart/meshcore-decoder/dist/crypto/channel-crypto');
  const originalModule = require.cache[cryptoPath];
  require.cache[cryptoPath] = {
    id: cryptoPath,
    exports: {
      ChannelCrypto: {
        decryptGroupTextMessage: () => ({
          success: true,
          data: { sender: null, message: 'Broadcast msg', timestamp: 1700000001, flags: 1 },
        }),
      },
    },
  };
  try {
    const hex = '1500' + '0A' + 'AABB' + 'CCDDEE112233';
    const p = decodePacket(hex, { '#alerts': 'deadbeefdeadbeefdeadbeefdeadbeef' });
    assert.strictEqual(p.payload.decryptionStatus, 'decrypted');
    assert.strictEqual(p.payload.sender, null);
    assert.strictEqual(p.payload.text, 'Broadcast msg');
    assert.strictEqual(p.payload.channelHashHex, '0A');
  } finally {
    if (originalModule) require.cache[cryptoPath] = originalModule;
    else delete require.cache[cryptoPath];
  }
});

test('GRP_TXT decrypted tries multiple keys, first match wins', () => {
  const cryptoPath = require.resolve('@michaelhart/meshcore-decoder/dist/crypto/channel-crypto');
  const originalModule = require.cache[cryptoPath];
  let callCount = 0;
  require.cache[cryptoPath] = {
    id: cryptoPath,
    exports: {
      ChannelCrypto: {
        decryptGroupTextMessage: (ciphertext, mac, key) => {
          callCount++;
          if (key === 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb') {
            return { success: true, data: { sender: 'Bob', message: 'Found it', timestamp: 0, flags: 0 } };
          }
          return { success: false };
        },
      },
    },
  };
  try {
    const hex = '1500' + 'FF' + 'AABB' + 'CCDDEE112233';
    const p = decodePacket(hex, {
      '#wrong': 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
      '#right': 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    });
    assert.strictEqual(p.payload.decryptionStatus, 'decrypted');
    assert.strictEqual(p.payload.channel, '#right');
    assert.strictEqual(p.payload.sender, 'Bob');
    assert.strictEqual(callCount, 2);
  } finally {
    if (originalModule) require.cache[cryptoPath] = originalModule;
    else delete require.cache[cryptoPath];
  }
});

console.log('\n=== TXT_MSG payload ===');
test('TXT_MSG decode', () => {
  // payloadType=2 → (2<<2)|1 = 0x09
  const hex = '0900' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'TXT_MSG');
  assert(p.payload.destHash);
  assert(p.payload.srcHash);
  assert(p.payload.mac);
});

console.log('\n=== ACK payload ===');
test('ACK decode', () => {
  // payloadType=3 → (3<<2)|1 = 0x0D
  const hex = '0D00' + '00'.repeat(18);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'ACK');
  assert(p.payload.ackChecksum);
});

test('ACK too short', () => {
  const hex = '0D00' + '00'.repeat(3);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== REQ payload ===');
test('REQ decode', () => {
  // payloadType=0 → (0<<2)|1 = 0x01
  const hex = '0100' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'REQ');
});

console.log('\n=== RESPONSE payload ===');
test('RESPONSE decode', () => {
  // payloadType=1 → (1<<2)|1 = 0x05
  const hex = '0500' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'RESPONSE');
});

console.log('\n=== ANON_REQ payload ===');
test('ANON_REQ decode', () => {
  // payloadType=7 → (7<<2)|1 = 0x1D
  const hex = '1D00' + '00'.repeat(50);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'ANON_REQ');
  assert(p.payload.destHash);
  assert(p.payload.ephemeralPubKey);
  assert(p.payload.mac);
});

test('ANON_REQ too short', () => {
  const hex = '1D00' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== PATH payload ===');
test('PATH decode', () => {
  // payloadType=8 → (8<<2)|1 = 0x21
  const hex = '2100' + '00'.repeat(20);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'PATH');
  assert(p.payload.destHash);
  assert(p.payload.srcHash);
});

test('PATH too short', () => {
  const hex = '2100' + '00'.repeat(1);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== TRACE payload ===');
test('TRACE decode', () => {
  // payloadType=9 → (9<<2)|1 = 0x25
  const hex = '2500' + '00'.repeat(12);
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'TRACE');
  assert(p.payload.tag !== undefined);
  assert(p.payload.authCode !== undefined);
  assert.strictEqual(p.payload.flags, 0);
});

test('TRACE too short', () => {
  const hex = '2500' + '00'.repeat(5);
  const p = decodePacket(hex);
  assert(p.payload.error);
});

console.log('\n=== UNKNOWN payload ===');
test('Unknown payload type', () => {
  // payloadType=6 → (6<<2)|1 = 0x19
  const hex = '1900' + 'DEADBEEF';
  const p = decodePacket(hex);
  assert.strictEqual(p.payload.type, 'UNKNOWN');
  assert(p.payload.raw);
});

// === Edge cases ===
console.log('\n=== Edge cases ===');
test('Packet too short throws', () => {
  assert.throws(() => decodePacket('FF'), /too short/);
});

test('Packet with spaces in hex', () => {
  const hex = '11 00 ' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.header.payloadTypeName, 'ADVERT');
});

test('Transport route too short throws', () => {
  assert.throws(() => decodePacket('0000'), /too short for transport/);
});

test('Corrupt packet #183 — TRANSPORT_DIRECT with correct field order', () => {
  const hex = 'BBAD6797EC8751D500BF95A1A776EF580E665BCBF6A0BBE03B5E730707C53489B8C728FD3FB902397197E1263CEC21E52465362243685DBBAD6797EC8751C90A75D9FD8213155D';
  const p = decodePacket(hex);
  assert.strictEqual(p.header.routeType, 3, 'routeType should be TRANSPORT_DIRECT');
  assert.strictEqual(p.header.payloadTypeName, 'UNKNOWN');
  // transport codes are bytes 1-4, pathByte=0x87 at byte 5
  assert.strictEqual(p.transportCodes.code1, 'AD67');
  assert.strictEqual(p.transportCodes.code2, '97EC');
  // pathByte 0x87: hashSize=3, hashCount=7
  assert.strictEqual(p.path.hashSize, 3);
  assert.strictEqual(p.path.hashCount, 7);
  assert.strictEqual(p.path.hops.length, 7);
  // No empty strings in hops
  assert(p.path.hops.every(h => h.length > 0), 'no empty hops');
});

test('path.truncated is false for normal packets', () => {
  const hex = '1100' + '00'.repeat(101);
  const p = decodePacket(hex);
  assert.strictEqual(p.path.truncated, false);
});

test('path overflow with hashSize=2', () => {
  // FLOOD + REQ, pathByte=0x45 → hashSize=2, hashCount=5, needs 10 bytes of path
  // Only provide 7 bytes after pathByte → fits 3 full 2-byte hops
  const hex = '0145' + 'AABBCCDDEEFF77';
  const p = decodePacket(hex);
  assert.strictEqual(p.path.hashCount, 3);
  assert.strictEqual(p.path.truncated, true);
  assert.strictEqual(p.path.hops.length, 3);
  assert.strictEqual(p.path.hops[0], 'AABB');
  assert.strictEqual(p.path.hops[1], 'CCDD');
  assert.strictEqual(p.path.hops[2], 'EEFF');
});

// === Real packets from API ===
console.log('\n=== Real packets ===');
test('Real GRP_TXT packet', () => {
  const p = decodePacket('150115D96CFF1FC90E7917B91729B76C1B509AE7789BBBD87D5AC3837E6C1487B47B0958AED8C7A6');
  assert.strictEqual(p.header.payloadTypeName, 'GRP_TXT');
  assert.strictEqual(p.header.routeTypeName, 'FLOOD');
  assert.strictEqual(p.path.hashCount, 1);
});

test('Real ADVERT packet FLOOD with 3 hops', () => {
  const p = decodePacket('11036CEF52206D763E1EACFD52FBAD4EF926887D0694C42A618AAF480A67C41120D3785950EFE0C1');
  assert.strictEqual(p.header.payloadTypeName, 'ADVERT');
  assert.strictEqual(p.header.routeTypeName, 'FLOOD');
  assert.strictEqual(p.path.hashCount, 3);
  assert.strictEqual(p.path.hashSize, 1);
  // Payload is too short for full ADVERT but decoder handles it
  assert.strictEqual(p.payload.type, 'ADVERT');
});

test('Real DIRECT TXT_MSG packet', () => {
  // 0x0A = DIRECT(2) + TXT_MSG(2)
  const p = decodePacket('0A403220AD034C0394C2C449810E3D86399C53AEE7FE355BA67002FFC3627B1175A257A181AE');
  assert.strictEqual(p.header.payloadTypeName, 'TXT_MSG');
  assert.strictEqual(p.header.routeTypeName, 'DIRECT');
});

// === validateAdvert ===
console.log('\n=== validateAdvert ===');
test('valid advert', () => {
  const a = { pubKey: 'AB'.repeat(16), flags: { repeater: true, room: false, sensor: false } };
  assert.deepStrictEqual(validateAdvert(a), { valid: true });
});

test('null advert', () => {
  assert.strictEqual(validateAdvert(null).valid, false);
});

test('advert with error', () => {
  assert.strictEqual(validateAdvert({ error: 'bad' }).valid, false);
});

test('pubkey too short', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AABB' }).valid, false);
});

test('pubkey all zeros', () => {
  assert.strictEqual(validateAdvert({ pubKey: '0'.repeat(64) }).valid, false);
});

test('invalid lat', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), lat: 200 }).valid, false);
});

test('invalid lon', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), lon: -200 }).valid, false);
});

test('name with control chars', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), name: 'test\x00bad' }).valid, false);
});

test('name too long', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), name: 'A'.repeat(65) }).valid, false);
});

test('valid name', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), name: 'My Node' }).valid, true);
});

test('valid lat/lon', () => {
  const r = validateAdvert({ pubKey: 'AB'.repeat(16), lat: 37.3, lon: -121.9 });
  assert.strictEqual(r.valid, true);
});

test('NaN lat invalid', () => {
  assert.strictEqual(validateAdvert({ pubKey: 'AB'.repeat(16), lat: NaN }).valid, false);
});

// --- GRP_TXT garbage detection (fixes #197) ---

test('GRP_TXT decrypted garbage text marked as decryption_failed', () => {
  const cryptoPath = require.resolve('@michaelhart/meshcore-decoder/dist/crypto/channel-crypto');
  const originalModule = require.cache[cryptoPath];
  require.cache[cryptoPath] = {
    id: cryptoPath,
    exports: {
      ChannelCrypto: {
        decryptGroupTextMessage: () => ({
          success: true,
          data: { sender: 'Node', message: '\x01\x02\x03\x80\x81', timestamp: 1700000000, flags: 0 },
        }),
      },
    },
  };
  try {
    const hex = '1500' + 'FF' + 'AABB' + 'CCDDEE112233';
    const p = decodePacket(hex, { '#general': 'aabbccddaabbccddaabbccddaabbccdd' });
    assert.strictEqual(p.payload.decryptionStatus, 'decryption_failed');
    assert.strictEqual(p.payload.text, null);
    assert.strictEqual(p.payload.channelHashHex, 'FF');
    assert.strictEqual(p.payload.channel, '#general');
  } finally {
    if (originalModule) require.cache[cryptoPath] = originalModule;
    else delete require.cache[cryptoPath];
  }
});

test('GRP_TXT valid text still marked as decrypted', () => {
  const cryptoPath = require.resolve('@michaelhart/meshcore-decoder/dist/crypto/channel-crypto');
  const originalModule = require.cache[cryptoPath];
  require.cache[cryptoPath] = {
    id: cryptoPath,
    exports: {
      ChannelCrypto: {
        decryptGroupTextMessage: () => ({
          success: true,
          data: { sender: 'Alice', message: 'Hello\nworld', timestamp: 1700000000, flags: 0 },
        }),
      },
    },
  };
  try {
    const hex = '1500' + 'FF' + 'AABB' + 'CCDDEE112233';
    const p = decodePacket(hex, { '#general': 'aabbccddaabbccddaabbccddaabbccdd' });
    assert.strictEqual(p.payload.decryptionStatus, 'decrypted');
    assert.strictEqual(p.payload.text, 'Alice: Hello\nworld');
  } finally {
    if (originalModule) require.cache[cryptoPath] = originalModule;
    else delete require.cache[cryptoPath];
  }
});

// === Summary ===
console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
