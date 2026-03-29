/**
 * Spec-driven tests for MeshCore decoder.
 *
 * Section 1: Spec assertions (from firmware/docs/packet_format.md + payloads.md)
 * Section 2: Golden fixtures (from production data at analyzer.00id.net)
 */

'use strict';

const { decodePacket, validateAdvert, ROUTE_TYPES, PAYLOAD_TYPES } = require('./decoder');

let passed = 0;
let failed = 0;
let noted = 0;

function assert(condition, msg) {
  if (condition) { passed++; }
  else { failed++; console.error(`  FAIL: ${msg}`); }
}

function assertEq(actual, expected, msg) {
  if (actual === expected) { passed++; }
  else { failed++; console.error(`  FAIL: ${msg} — expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`); }
}

function assertDeepEq(actual, expected, msg) {
  const a = JSON.stringify(actual);
  const b = JSON.stringify(expected);
  if (a === b) { passed++; }
  else { failed++; console.error(`  FAIL: ${msg}\n    expected: ${b}\n    got:      ${a}`); }
}

function note(msg) {
  noted++;
  console.log(`  NOTE: ${msg}`);
}

// ═══════════════════════════════════════════════════════════
// Section 1: Spec-based assertions
// ═══════════════════════════════════════════════════════════

console.log('── Spec Tests: Header Parsing ──');

// Header byte: bits 1-0 = routeType, bits 5-2 = payloadType, bits 7-6 = payloadVersion
{
  // 0x11 = 0b00_0100_01 → routeType=1(FLOOD), payloadType=4(ADVERT), version=0
  const p = decodePacket('1100' + '00'.repeat(101)); // min advert = 100 bytes payload
  assertEq(p.header.routeType, 1, 'header: routeType from bits 1-0');
  assertEq(p.header.payloadType, 4, 'header: payloadType from bits 5-2');
  assertEq(p.header.payloadVersion, 0, 'header: payloadVersion from bits 7-6');
  assertEq(p.header.routeTypeName, 'FLOOD', 'header: routeTypeName');
  assertEq(p.header.payloadTypeName, 'ADVERT', 'header: payloadTypeName');
}

// All four route types
{
  const routeNames = { 0: 'TRANSPORT_FLOOD', 1: 'FLOOD', 2: 'DIRECT', 3: 'TRANSPORT_DIRECT' };
  for (const [val, name] of Object.entries(routeNames)) {
    assertEq(ROUTE_TYPES[val], name, `ROUTE_TYPES[${val}] = ${name}`);
  }
}

// All payload types from spec
{
  const specTypes = {
    0x00: 'REQ', 0x01: 'RESPONSE', 0x02: 'TXT_MSG', 0x03: 'ACK',
    0x04: 'ADVERT', 0x05: 'GRP_TXT', 0x07: 'ANON_REQ',
    0x08: 'PATH', 0x09: 'TRACE',
  };
  for (const [val, name] of Object.entries(specTypes)) {
    assertEq(PAYLOAD_TYPES[val], name, `PAYLOAD_TYPES[${val}] = ${name}`);
  }
}

// Spec defines 0x06=GRP_DATA, 0x0A=MULTIPART, 0x0B=CONTROL, 0x0F=RAW_CUSTOM — decoder may not have them
{
  if (!PAYLOAD_TYPES[0x06]) note('Decoder missing PAYLOAD_TYPE 0x06 (GRP_DATA) — spec defines it');
  if (!PAYLOAD_TYPES[0x0A]) note('Decoder missing PAYLOAD_TYPE 0x0A (MULTIPART) — spec defines it');
  if (!PAYLOAD_TYPES[0x0B]) note('Decoder missing PAYLOAD_TYPE 0x0B (CONTROL) — spec defines it');
  if (!PAYLOAD_TYPES[0x0F]) note('Decoder missing PAYLOAD_TYPE 0x0F (RAW_CUSTOM) — spec defines it');
}

console.log('── Spec Tests: Path Byte Parsing ──');

// path_length: bits 5-0 = hop count, bits 7-6 = hash_size - 1
{
  // 0x00: 0 hops, 1-byte hashes
  const p0 = decodePacket('0500' + '00'.repeat(10));
  assertEq(p0.path.hashCount, 0, 'path 0x00: hashCount=0');
  assertEq(p0.path.hashSize, 1, 'path 0x00: hashSize=1');
  assertDeepEq(p0.path.hops, [], 'path 0x00: no hops');
}

{
  // 0x05: 5 hops, 1-byte hashes → 5 path bytes
  const p5 = decodePacket('0505' + 'AABBCCDDEE' + '00'.repeat(10));
  assertEq(p5.path.hashCount, 5, 'path 0x05: hashCount=5');
  assertEq(p5.path.hashSize, 1, 'path 0x05: hashSize=1');
  assertEq(p5.path.hops.length, 5, 'path 0x05: 5 hops');
  assertEq(p5.path.hops[0], 'AA', 'path 0x05: first hop');
  assertEq(p5.path.hops[4], 'EE', 'path 0x05: last hop');
}

{
  // 0x45: 5 hops, 2-byte hashes (bits 7-6 = 01) → 10 path bytes
  const p45 = decodePacket('0545' + 'AA11BB22CC33DD44EE55' + '00'.repeat(10));
  assertEq(p45.path.hashCount, 5, 'path 0x45: hashCount=5');
  assertEq(p45.path.hashSize, 2, 'path 0x45: hashSize=2');
  assertEq(p45.path.hops.length, 5, 'path 0x45: 5 hops');
  assertEq(p45.path.hops[0], 'AA11', 'path 0x45: first hop (2-byte)');
}

{
  // 0x8A: 10 hops, 3-byte hashes (bits 7-6 = 10) → 30 path bytes
  const p8a = decodePacket('058A' + 'AA11FF'.repeat(10) + '00'.repeat(10));
  assertEq(p8a.path.hashCount, 10, 'path 0x8A: hashCount=10');
  assertEq(p8a.path.hashSize, 3, 'path 0x8A: hashSize=3');
  assertEq(p8a.path.hops.length, 10, 'path 0x8A: 10 hops');
}

console.log('── Spec Tests: Transport Codes ──');

{
  // Route type 0 (TRANSPORT_FLOOD) and 3 (TRANSPORT_DIRECT) should have 4-byte transport codes
  // Route type 0: header=0x14 = payloadType 5 (GRP_TXT), routeType 0 (TRANSPORT_FLOOD)
  // Format: header(1) + transportCodes(4) + pathByte(1) + payload
  const hex = '14' + 'AABB' + 'CCDD' + '00' + '1A' + '00'.repeat(10); // transport codes + pathByte + GRP_TXT payload
  const p = decodePacket(hex);
  assertEq(p.header.routeType, 0, 'transport: routeType=0 (TRANSPORT_FLOOD)');
  assert(p.transportCodes !== null, 'transport: transportCodes present for TRANSPORT_FLOOD');
  assertEq(p.transportCodes.code1, 'AABB', 'transport: code1');
  assertEq(p.transportCodes.code2, 'CCDD', 'transport: code2');
}

{
  // Route type 1 (FLOOD) should NOT have transport codes
  const p = decodePacket('0500' + '00'.repeat(10));
  assertEq(p.transportCodes, null, 'no transport codes for FLOOD');
}

console.log('── Spec Tests: Advert Payload ──');

// Advert: pubkey(32) + timestamp(4 LE) + signature(64) + appdata
{
  const pubkey = 'AA'.repeat(32);
  const timestamp = '78563412'; // 0x12345678 LE = 305419896
  const signature = 'BB'.repeat(64);
  // flags: 0x92 = repeater(2) | hasLocation(0x10) | hasName(0x80)
  const flags = '92';
  // lat: 37000000 = 0x02353A80 LE → 80 3A 35 02
  const lat = '40933402';
  // lon: -122100000 = 0xF8B9E260 LE → 60 E2 B9 F8
  const lon = 'E0E6B8F8';
  const name = Buffer.from('TestNode').toString('hex');

  const hex = '1200' + pubkey + timestamp + signature + flags + lat + lon + name;
  const p = decodePacket(hex);

  assertEq(p.payload.type, 'ADVERT', 'advert: payload type');
  assertEq(p.payload.pubKey, pubkey.toLowerCase(), 'advert: 32-byte pubkey');
  assertEq(p.payload.timestamp, 0x12345678, 'advert: uint32 LE timestamp');
  assertEq(p.payload.signature, signature.toLowerCase().repeat(1), 'advert: 64-byte signature');

  // Flags
  assertEq(p.payload.flags.raw, 0x92, 'advert flags: raw byte');
  assertEq(p.payload.flags.type, 2, 'advert flags: type enum = 2 (repeater)');
  assertEq(p.payload.flags.repeater, true, 'advert flags: repeater');
  assertEq(p.payload.flags.room, false, 'advert flags: not room');
  assertEq(p.payload.flags.chat, false, 'advert flags: not chat');
  assertEq(p.payload.flags.sensor, false, 'advert flags: not sensor');
  assertEq(p.payload.flags.hasLocation, true, 'advert flags: hasLocation (bit 4)');
  assertEq(p.payload.flags.hasName, true, 'advert flags: hasName (bit 7)');

  // Location: int32 at 1e6 scale
  assert(Math.abs(p.payload.lat - 37.0) < 0.001, 'advert: lat decoded from int32/1e6');
  assert(Math.abs(p.payload.lon - (-122.1)) < 0.001, 'advert: lon decoded from int32/1e6');

  // Name
  assertEq(p.payload.name, 'TestNode', 'advert: name from remaining appdata');
}

// Advert type enum values per spec
{
  // type 0 = none (companion), 1 = chat/companion, 2 = repeater, 3 = room, 4 = sensor
  const makeAdvert = (flagsByte) => {
    const hex = '1200' + 'AA'.repeat(32) + '00000000' + 'BB'.repeat(64) + flagsByte.toString(16).padStart(2, '0');
    return decodePacket(hex).payload;
  };

  const t1 = makeAdvert(0x01);
  assertEq(t1.flags.type, 1, 'advert type 1 = chat/companion');
  assertEq(t1.flags.chat, true, 'type 1: chat=true');

  const t2 = makeAdvert(0x02);
  assertEq(t2.flags.type, 2, 'advert type 2 = repeater');
  assertEq(t2.flags.repeater, true, 'type 2: repeater=true');

  const t3 = makeAdvert(0x03);
  assertEq(t3.flags.type, 3, 'advert type 3 = room');
  assertEq(t3.flags.room, true, 'type 3: room=true');

  const t4 = makeAdvert(0x04);
  assertEq(t4.flags.type, 4, 'advert type 4 = sensor');
  assertEq(t4.flags.sensor, true, 'type 4: sensor=true');
}

// Advert with no location, no name (flags = 0x02, just repeater)
{
  const hex = '1200' + 'CC'.repeat(32) + '00000000' + 'DD'.repeat(64) + '02';
  const p = decodePacket(hex).payload;
  assertEq(p.flags.hasLocation, false, 'advert no location: hasLocation=false');
  assertEq(p.flags.hasName, false, 'advert no name: hasName=false');
  assertEq(p.lat, undefined, 'advert no location: lat undefined');
  assertEq(p.name, undefined, 'advert no name: name undefined');
}

// Telemetry: sensor node with battery + positive temperature
{
  const pubkey = 'AA'.repeat(32);
  const sig = 'BB'.repeat(64);
  const flags = '84'; // sensor(4) | hasName(0x80)
  const name = Buffer.from('S1').toString('hex') + '00'; // null-terminated
  const battBuf = Buffer.alloc(2); battBuf.writeUInt16LE(3700);
  const tempBuf = Buffer.alloc(2); tempBuf.writeInt16LE(2850); // 28.50°C
  const hex = '1200' + pubkey + '00000000' + sig + flags + name +
    battBuf.toString('hex') + tempBuf.toString('hex');
  const p = decodePacket(hex).payload;
  assertEq(p.battery_mv, 3700, 'telemetry: battery_mv decoded');
  assert(Math.abs(p.temperature_c - 28.50) < 0.01, 'telemetry: temperature_c positive');
}

// Telemetry: sensor node with 0°C must still emit temperature_c
{
  const pubkey = 'CC'.repeat(32);
  const sig = 'DD'.repeat(64);
  const flags = '84'; // sensor(4) | hasName(0x80)
  const name = Buffer.from('S2').toString('hex') + '00';
  const battBuf = Buffer.alloc(2); battBuf.writeUInt16LE(3600);
  const tempBuf = Buffer.alloc(2); // 0°C
  const hex = '1200' + pubkey + '00000000' + sig + flags + name +
    battBuf.toString('hex') + tempBuf.toString('hex');
  const p = decodePacket(hex).payload;
  assert(p.temperature_c === 0, 'telemetry: 0°C is valid and emitted');
}

// Telemetry: non-sensor node with trailing bytes must NOT decode telemetry
{
  const pubkey = 'EE'.repeat(32);
  const sig = 'FF'.repeat(64);
  const flags = '82'; // repeater(2) | hasName(0x80)
  const name = Buffer.from('R1').toString('hex') + '00';
  const extraBytes = 'B40ED403'; // battery-like and temp-like bytes
  const hex = '1200' + pubkey + '00000000' + sig + flags + name + extraBytes;
  const p = decodePacket(hex).payload;
  assertEq(p.battery_mv, undefined, 'telemetry: non-sensor node: battery_mv must be undefined');
  assertEq(p.temperature_c, undefined, 'telemetry: non-sensor node: temperature_c must be undefined');
}

console.log('── Spec Tests: Encrypted Payload Format ──');

// Spec says v1 encrypted payloads: dest(1)+src(1)+MAC(2)+cipher — decoder matches this.
{
  const hex = '0100' + 'AA' + 'BB' + 'CCDD' + '00'.repeat(10);
  const p = decodePacket(hex);
  assertEq(p.payload.destHash, 'aa', 'encrypted payload: dest is 1 byte');
  assertEq(p.payload.srcHash, 'bb', 'encrypted payload: src is 1 byte');
  assertEq(p.payload.mac, 'ccdd', 'encrypted payload: MAC is 2 bytes');
}

console.log('── Spec Tests: validateAdvert ──');

{
  const good = { pubKey: 'aa'.repeat(32), flags: { repeater: true, room: false, sensor: false } };
  assertEq(validateAdvert(good).valid, true, 'validateAdvert: good advert');

  assertEq(validateAdvert(null).valid, false, 'validateAdvert: null');
  assertEq(validateAdvert({ error: 'bad' }).valid, false, 'validateAdvert: error advert');
  assertEq(validateAdvert({ pubKey: 'aa' }).valid, false, 'validateAdvert: short pubkey');
  assertEq(validateAdvert({ pubKey: '00'.repeat(32) }).valid, false, 'validateAdvert: all-zero pubkey');

  const badLat = { pubKey: 'aa'.repeat(32), lat: 999 };
  assertEq(validateAdvert(badLat).valid, false, 'validateAdvert: invalid lat');

  const badLon = { pubKey: 'aa'.repeat(32), lon: -999 };
  assertEq(validateAdvert(badLon).valid, false, 'validateAdvert: invalid lon');

  const badName = { pubKey: 'aa'.repeat(32), name: 'test\x00name' };
  assertEq(validateAdvert(badName).valid, false, 'validateAdvert: control chars in name');

  const longName = { pubKey: 'aa'.repeat(32), name: 'x'.repeat(65) };
  assertEq(validateAdvert(longName).valid, false, 'validateAdvert: name too long');
}

// ═══════════════════════════════════════════════════════════
// Section 2: Golden fixtures (from production)
// ═══════════════════════════════════════════════════════════

console.log('── Golden Tests: Production Packets ──');

const goldenFixtures = [
  {
    "raw_hex": "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976",
    "payload_type": 2,
    "route_type": 2,
    "decoded": "{\"type\":\"TXT_MSG\",\"destHash\":\"d6\",\"srcHash\":\"9f\",\"mac\":\"d7a5\",\"encryptedData\":\"a7475db07337749ae61fa53a4788e976\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "0A009FD605771EE2EB0CDC46D100232B455947E3C2D4B9DD0B8880EACA99A3C5F7EF63183D6D",
    "payload_type": 2,
    "route_type": 2,
    "decoded": "{\"type\":\"TXT_MSG\",\"destHash\":\"9f\",\"srcHash\":\"d6\",\"mac\":\"0577\",\"encryptedData\":\"1ee2eb0cdc46d100232b455947e3c2d4b9dd0b8880eaca99a3c5f7ef63183d6d\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52",
    "payload_type": 4,
    "route_type": 2,
    "decoded": "{\"type\":\"ADVERT\",\"pubKey\":\"46d62de27d4c5194d7821fc5a34a45565dcc2537b300b9ab6275255cefb65d84\",\"timestamp\":1774314764,\"timestampISO\":\"2026-03-24T01:12:44.000Z\",\"signature\":\"c94c9aed39e8bcb6cb6eb0335497a198b33a1a610cd3b03d8dcfc160900e5244280323ee0b44cacab8f02b5b38b91cfa18bd067b0b5e63e94cfc85f758a8530b\",\"flags\":{\"raw\":146,\"type\":2,\"chat\":false,\"repeater\":true,\"room\":false,\"sensor\":false,\"hasLocation\":true,\"hasName\":true},\"lat\":37,\"lon\":-122.1,\"name\":\"MRR2-R\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "120073CFF971E1CB5754A742C152B2D2E0EB108A19B246D663ED8898A72C4A5AD86EA6768E66694B025EDF6939D5C44CFF719C5D5520E5F06B20680A83AD9C2C61C3227BBB977A85EE462F3553445FECF8EDD05C234ECE217272E503F14D6DF2B1B9B133890C923CDF3002F8FDC1F85045414BF09F8CB3",
    "payload_type": 4,
    "route_type": 2,
    "decoded": "{\"type\":\"ADVERT\",\"pubKey\":\"73cff971e1cb5754a742c152b2d2e0eb108a19b246d663ed8898a72c4a5ad86e\",\"timestamp\":1720612518,\"timestampISO\":\"2024-07-10T11:55:18.000Z\",\"signature\":\"694b025edf6939d5c44cff719c5d5520e5f06b20680a83ad9c2c61c3227bbb977a85ee462f3553445fecf8edd05c234ece217272e503f14d6df2b1b9b133890c\",\"flags\":{\"raw\":146,\"type\":2,\"chat\":false,\"repeater\":true,\"room\":false,\"sensor\":false,\"hasLocation\":true,\"hasName\":true},\"lat\":36.757308,\"lon\":-121.504264,\"name\":\"PEAK🌳\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "06001f33e1bef15f5596b394adf03a77d46b89afa2e3",
    "payload_type": 1,
    "route_type": 2,
    "decoded": "{\"type\":\"RESPONSE\",\"destHash\":\"1f\",\"srcHash\":\"33\",\"mac\":\"e1be\",\"encryptedData\":\"f15f5596b394adf03a77d46b89afa2e3\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "0200331fe52805e05cf6f4bae6a094ac258d57baf045",
    "payload_type": 0,
    "route_type": 2,
    "decoded": "{\"type\":\"REQ\",\"destHash\":\"33\",\"srcHash\":\"1f\",\"mac\":\"e528\",\"encryptedData\":\"05e05cf6f4bae6a094ac258d57baf045\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "15001ABC314305D3CCC94EB3F398D3054B4E95899229027B027E450FD68B4FA4E0A0126AC1",
    "payload_type": 5,
    "route_type": 1,
    "decoded": "{\"type\":\"GRP_TXT\",\"channelHash\":26,\"mac\":\"bc31\",\"encryptedData\":\"4305d3ccc94eb3f398d3054b4e95899229027b027e450fd68b4fa4e0a0126ac1\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "010673a210206cb51e42fee24c4847a99208b9fc1d7ab36c42b10748",
    "payload_type": 0,
    "route_type": 1,
    "decoded": "{\"type\":\"REQ\",\"destHash\":\"1e\",\"srcHash\":\"42\",\"mac\":\"fee2\",\"encryptedData\":\"4c4847a99208b9fc1d7ab36c42b10748\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 6,
      "hops": [
        "73",
        "A2",
        "10",
        "20",
        "6C",
        "B5"
      ]
    }
  },
  {
    "raw_hex": "0101731E42FEE24C4847A99208293810E4A3E335640D8E",
    "payload_type": 0,
    "route_type": 1,
    "decoded": "{\"type\":\"REQ\",\"destHash\":\"1e\",\"srcHash\":\"42\",\"mac\":\"fee2\",\"encryptedData\":\"4c4847a99208293810e4a3e335640d8e\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 1,
      "hops": [
        "73"
      ]
    }
  },
  {
    "raw_hex": "0106FB10844070101E42BA859D1D939362F79D3F3865333629FF92E9",
    "payload_type": 0,
    "route_type": 1,
    "decoded": "{\"type\":\"REQ\",\"destHash\":\"1e\",\"srcHash\":\"42\",\"mac\":\"ba85\",\"encryptedData\":\"9d1d939362f79d3f3865333629ff92e9\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 6,
      "hops": [
        "FB",
        "10",
        "84",
        "40",
        "70",
        "10"
      ]
    }
  },
  {
    "raw_hex": "0102FB101E42BA859D1D939362F79D3F3865333629FF92D9",
    "payload_type": 0,
    "route_type": 1,
    "decoded": "{\"type\":\"REQ\",\"destHash\":\"1e\",\"srcHash\":\"42\",\"mac\":\"ba85\",\"encryptedData\":\"9d1d939362f79d3f3865333629ff92d9\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 2,
      "hops": [
        "FB",
        "10"
      ]
    }
  },
  {
    "raw_hex": "22009FD65B38857C5A7F6F0F28E999CF2632C03ACCCC",
    "payload_type": 8,
    "route_type": 2,
    "decoded": "{\"type\":\"PATH\",\"destHash\":\"9f\",\"srcHash\":\"d6\",\"mac\":\"5b38\",\"pathData\":\"857c5a7f6f0f28e999cf2632c03acccc\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "0506701085AD8573D69F96FA7DD3B1AC3702794035442D9CDAD436D4",
    "payload_type": 1,
    "route_type": 1,
    "decoded": "{\"type\":\"RESPONSE\",\"destHash\":\"d6\",\"srcHash\":\"9f\",\"mac\":\"96fa\",\"encryptedData\":\"7dd3b1ac3702794035442d9cdad436d4\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 6,
      "hops": [
        "70",
        "10",
        "85",
        "AD",
        "85",
        "73"
      ]
    }
  },
  {
    "raw_hex": "0500D69F96FA7DD3B1AC3702794035442D9CDAD43654",
    "payload_type": 1,
    "route_type": 1,
    "decoded": "{\"type\":\"RESPONSE\",\"destHash\":\"d6\",\"srcHash\":\"9f\",\"mac\":\"96fa\",\"encryptedData\":\"7dd3b1ac3702794035442d9cdad43654\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "1E009FD6DFC543C53E826A2B789B072FF9CBE922E57EA093E5643A0CA813E79F42EE9108F855B72A3E0B599C9AC80D3A211E7C7BA2",
    "payload_type": 7,
    "route_type": 2,
    "decoded": "{\"type\":\"ANON_REQ\",\"destHash\":\"9f\",\"ephemeralPubKey\":\"d6dfc543c53e826a2b789b072ff9cbe922e57ea093e5643a0ca813e79f42ee91\",\"mac\":\"08f8\",\"encryptedData\":\"55b72a3e0b599c9ac80d3a211e7c7ba2\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "110146B7F1C45F2ED5888335F79E27085D0DE871A7C8ECB1EF5313435EBD0825BACDC181E3C1695556F51A89C9895E2114D1FECA91B58F82CBBBC1DD2B868ADDC0F7EB8C310D0887C2A2283D6F7D01A5E97B6C2F6A4CC899F27AFA513CC6B295E34ADC84A1F1019240933402E0E6B8F84D6574726F2D52",
    "payload_type": 4,
    "route_type": 1,
    "decoded": "{\"type\":\"ADVERT\",\"pubKey\":\"b7f1c45f2ed5888335f79e27085d0de871a7c8ecb1ef5313435ebd0825bacdc1\",\"timestamp\":1774314369,\"timestampISO\":\"2026-03-24T01:06:09.000Z\",\"signature\":\"5556f51a89c9895e2114d1feca91b58f82cbbbc1dd2b868addc0f7eb8c310d0887c2a2283d6f7d01a5e97b6c2f6a4cc899f27afa513cc6b295e34adc84a1f101\",\"flags\":{\"raw\":146,\"type\":2,\"chat\":false,\"repeater\":true,\"room\":false,\"sensor\":false,\"hasLocation\":true,\"hasName\":true},\"lat\":37,\"lon\":-122.1,\"name\":\"Metro-R\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 1,
      "hops": [
        "46"
      ]
    }
  },
  {
    "raw_hex": "15001A901C5D927D90572BAF6135D226F91D180AD4F7B90DF20F82EEEA920312D9CCFD9C3F8CA9EFBEB1C37DFA31265F73483BD0640EC94E247902F617B2C320BFA332F50441AD234D8324A48ABAA9A16EB15BD50F2D67029F2424E0836010A635EB45B5DFDB4CDC080C09FC849040AB4B82769E0F",
    "payload_type": 5,
    "route_type": 1,
    "decoded": "{\"type\":\"GRP_TXT\",\"channelHash\":26,\"mac\":\"901c\",\"encryptedData\":\"5d927d90572baf6135d226f91d180ad4f7b90df20f82eeea920312d9ccfd9c3f8ca9efbeb1c37dfa31265f73483bd0640ec94e247902f617b2c320bfa332f50441ad234d8324a48abaa9a16eb15bd50f2d67029f2424e0836010a635eb45b5dfdb4cdc080c09fc849040ab4b82769e0f\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "0A00D69F0E65C6CCDEBE8391ED093D3C76E2D064F525",
    "payload_type": 2,
    "route_type": 2,
    "decoded": "{\"type\":\"TXT_MSG\",\"destHash\":\"d6\",\"srcHash\":\"9f\",\"mac\":\"0e65\",\"encryptedData\":\"c6ccdebe8391ed093d3c76e2d064f525\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "0A00D69F940E0BA255095E9540EE6E23895DA80AAC60",
    "payload_type": 2,
    "route_type": 2,
    "decoded": "{\"type\":\"TXT_MSG\",\"destHash\":\"d6\",\"srcHash\":\"9f\",\"mac\":\"940e\",\"encryptedData\":\"0ba255095e9540ee6e23895da80aac60\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  },
  {
    "raw_hex": "06001f5d5acf699ea80c7ca1a9349b8af9a1b47d4a1a",
    "payload_type": 1,
    "route_type": 2,
    "decoded": "{\"type\":\"RESPONSE\",\"destHash\":\"1f\",\"srcHash\":\"5d\",\"mac\":\"5acf\",\"encryptedData\":\"699ea80c7ca1a9349b8af9a1b47d4a1a\"}",
    "path": {
      "hashSize": 1,
      "hashCount": 0,
      "hops": []
    }
  }
];

// One special case: the advert with 1 hop from prod had raw_hex starting with "110146"
// but the API reported path ["46"]. Let me re-check — header 0x11 = routeType 1, payloadType 4.
// pathByte 0x01 = 1 hop, 1-byte hash. Next byte is 0x46 = the hop. Correct.
// However, the raw_hex I captured from the API was "110146B7F1..." but the actual prod JSON showed path ["46"].
// I need to use the correct raw_hex. Let me fix fixture 15 (Metro-R advert).

for (let i = 0; i < goldenFixtures.length; i++) {
  const fix = goldenFixtures[i];
  const expected = typeof fix.decoded === "string" ? JSON.parse(fix.decoded) : fix.decoded;
  const label = `golden[${i}] ${expected.type}`;

  try {
    const result = decodePacket(fix.raw_hex);

    // Verify header matches expected route/payload type
    assertEq(result.header.routeType, fix.route_type, `${label}: routeType`);
    assertEq(result.header.payloadType, fix.payload_type, `${label}: payloadType`);

    // Verify path hops
    assertDeepEq(result.path.hops, (fix.path.hops || fix.path), `${label}: path hops`);

    // Verify payload matches prod decoded output
    // Compare key fields rather than full deep equality (to handle minor serialization diffs)
    
    assertEq(result.payload.type, expected.type, `${label}: payload type`);

    if (expected.type === 'ADVERT') {
      assertEq(result.payload.pubKey, expected.pubKey, `${label}: pubKey`);
      assertEq(result.payload.timestamp, expected.timestamp, `${label}: timestamp`);
      assertEq(result.payload.signature, expected.signature, `${label}: signature`);
      if (expected.flags) {
        assertEq(result.payload.flags.raw, expected.flags.raw, `${label}: flags.raw`);
        assertEq(result.payload.flags.type, expected.flags.type, `${label}: flags.type`);
        assertEq(result.payload.flags.hasLocation, expected.flags.hasLocation, `${label}: hasLocation`);
        assertEq(result.payload.flags.hasName, expected.flags.hasName, `${label}: hasName`);
      }
      if (expected.lat != null) assert(Math.abs(result.payload.lat - expected.lat) < 0.001, `${label}: lat`);
      if (expected.lon != null) assert(Math.abs(result.payload.lon - expected.lon) < 0.001, `${label}: lon`);
      if (expected.name) assertEq(result.payload.name, expected.name, `${label}: name`);

      // Spec checks on advert structure
      assert(result.payload.pubKey.length === 64, `${label}: pubKey is 32 bytes (64 hex chars)`);
      assert(result.payload.signature.length === 128, `${label}: signature is 64 bytes (128 hex chars)`);
    } else if (expected.type === 'GRP_TXT' || expected.type === 'CHAN') {
      assertEq(result.payload.channelHash, expected.channelHash, `${label}: channelHash`);
      // If decoded as CHAN (with channel key), check sender/text; otherwise check mac/encrypted
      if (expected.type === 'GRP_TXT') {
        assertEq(result.payload.mac, expected.mac, `${label}: mac`);
        assertEq(result.payload.encryptedData, expected.encryptedData, `${label}: encryptedData`);
      }
    } else if (expected.type === 'ANON_REQ') {
      assertEq(result.payload.destHash, expected.destHash, `${label}: destHash`);
      assertEq(result.payload.ephemeralPubKey, expected.ephemeralPubKey, `${label}: ephemeralPubKey`);
      assertEq(result.payload.mac, expected.mac, `${label}: mac`);
    } else {
      // Encrypted payload types: REQ, RESPONSE, TXT_MSG, PATH
      assertEq(result.payload.destHash, expected.destHash, `${label}: destHash`);
      assertEq(result.payload.srcHash, expected.srcHash, `${label}: srcHash`);
      assertEq(result.payload.mac, expected.mac, `${label}: mac`);
      if (expected.encryptedData) assertEq(result.payload.encryptedData, expected.encryptedData, `${label}: encryptedData`);
      if (expected.pathData) assertEq(result.payload.pathData, expected.pathData, `${label}: pathData`);
    }
  } catch (e) {
    failed++;
    console.error(`  FAIL: ${label} — threw: ${e.message}`);
  }
}

// ═══════════════════════════════════════════════════════════
// Summary
// ═══════════════════════════════════════════════════════════

console.log('');
console.log(`═══ Results: ${passed} passed, ${failed} failed, ${noted} notes ═══`);
if (failed > 0) process.exit(1);
