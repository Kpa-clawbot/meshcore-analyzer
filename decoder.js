/**
 * MeshCore Packet Decoder
 * Custom implementation — does NOT use meshcore-decoder library (known path_length bug).
 *
 * Packet layout:
 *   [header(1)] [pathLength(1)] [transportCodes?] [path hops] [payload...]
 *
 * Header byte (LSB first):
 *   bits 1-0: routeType (0=TRANSPORT_FLOOD, 1=FLOOD, 2=DIRECT, 3=TRANSPORT_DIRECT)
 *   bits 5-2: payloadType
 *   bits 7-6: payloadVersion
 *
 * Path length byte:
 *   bits 5-0: hash_count (number of hops, 0-63)
 *   bits 7-6: (value >> 6) + 1 = hash_size (1-4 bytes per hop hash)
 */

'use strict';

// --- Constants ---

const ROUTE_TYPES = {
  0: 'TRANSPORT_FLOOD',
  1: 'FLOOD',
  2: 'DIRECT',
  3: 'TRANSPORT_DIRECT',
};

const PAYLOAD_TYPES = {
  0x00: 'REQ',
  0x01: 'RESPONSE',
  0x02: 'TXT_MSG',
  0x03: 'ACK',
  0x04: 'ADVERT',
  0x05: 'GRP_TXT',
  0x07: 'ANON_REQ',
  0x08: 'PATH',
  0x09: 'TRACE',
};

// Route types that carry transport codes (nextHop + lastHop, 2 bytes each)
const TRANSPORT_ROUTES = new Set([0, 3]); // TRANSPORT_FLOOD, TRANSPORT_DIRECT

// --- Header parsing ---

function decodeHeader(byte) {
  return {
    routeType: byte & 0x03,
    routeTypeName: ROUTE_TYPES[byte & 0x03] || 'UNKNOWN',
    payloadType: (byte >> 2) & 0x0F,
    payloadTypeName: PAYLOAD_TYPES[(byte >> 2) & 0x0F] || 'UNKNOWN',
    payloadVersion: (byte >> 6) & 0x03,
  };
}

// --- Path parsing ---

function decodePath(pathByte, buf, offset) {
  const hashSize = (pathByte >> 6) + 1;   // 1-4 bytes per hash
  const hashCount = pathByte & 0x3F;       // 0-63 hops
  const totalBytes = hashSize * hashCount;
  const hops = [];

  for (let i = 0; i < hashCount; i++) {
    const hopBuf = buf.subarray(offset + i * hashSize, offset + i * hashSize + hashSize);
    hops.push(hopBuf.toString('hex').toUpperCase());
  }

  return {
    hashSize,
    hashCount,
    hops,
    bytesConsumed: totalBytes,
  };
}

// --- Payload decoders ---

/** REQ / RESPONSE / TXT_MSG: dest(6) + src(6) + MAC(4) + encrypted */
function decodeEncryptedPayload(buf) {
  if (buf.length < 16) return { error: 'too short', raw: buf.toString('hex') };
  return {
    destHash: buf.subarray(0, 6).toString('hex'),
    srcHash: buf.subarray(6, 12).toString('hex'),
    mac: buf.subarray(12, 16).toString('hex'),
    encryptedData: buf.subarray(16).toString('hex'),
  };
}

/** ACK: dest(6) + src(6) + extra(6) */
function decodeAck(buf) {
  if (buf.length < 18) return { error: 'too short', raw: buf.toString('hex') };
  return {
    destHash: buf.subarray(0, 6).toString('hex'),
    srcHash: buf.subarray(6, 12).toString('hex'),
    extraHash: buf.subarray(12, 18).toString('hex'),
  };
}

/** ADVERT: pubkey(32) + timestamp(4 LE) + signature(64) + appdata */
function decodeAdvert(buf) {
  if (buf.length < 100) return { error: 'too short for advert', raw: buf.toString('hex') };
  const pubKey = buf.subarray(0, 32).toString('hex');
  const timestamp = buf.readUInt32LE(32);
  const signature = buf.subarray(36, 100).toString('hex');
  const appdata = buf.subarray(100);

  const result = { pubKey, timestamp, timestampISO: new Date(timestamp * 1000).toISOString(), signature };

  if (appdata.length > 0) {
    const flags = appdata[0];
    result.flags = {
      raw: flags,
      chat: !!(flags & 0x01),
      repeater: !!(flags & 0x02),
      room: !!(flags & 0x04),
      sensor: !!(flags & 0x08),
      hasLocation: !!(flags & 0x10),
      hasName: !!(flags & 0x80),
    };

    let off = 1;
    if (result.flags.hasLocation && appdata.length >= off + 8) {
      result.lat = appdata.readInt32LE(off) / 1e6;
      result.lon = appdata.readInt32LE(off + 4) / 1e6;
      off += 8;
    }
    if (result.flags.hasName) {
      result.name = appdata.subarray(off).toString('utf8');
    }
  }

  return result;
}

/** GRP_TXT: channel_hash(1) + MAC(2) + encrypted */
function decodeGrpTxt(buf, channelKeys) {
  if (buf.length < 3) return { error: 'too short', raw: buf.toString('hex') };
  const channelHash = buf[0];
  const mac = buf.subarray(1, 3).toString('hex');
  const encryptedData = buf.subarray(3).toString('hex');

  // Try decryption with known channel keys
  if (channelKeys && encryptedData.length >= 10) {
    try {
      const { ChannelCrypto } = require('@michaelhart/meshcore-decoder/dist/crypto/channel-crypto');
      for (const [name, key] of Object.entries(channelKeys)) {
        const result = ChannelCrypto.decryptGroupTextMessage(encryptedData, mac, key);
        if (result.success && result.data) {
          return {
            type: 'CHAN',
            channel: name,
            channelHash,
            sender: result.data.sender || null,
            text: result.data.sender && result.data.message
              ? `${result.data.sender}: ${result.data.message}`
              : result.data.message || '',
            sender_timestamp: result.data.timestamp,
            flags: result.data.flags,
          };
        }
      }
    } catch (e) { /* decryption failed, fall through */ }
  }

  return { type: 'GRP_TXT', channelHash, mac, encryptedData };
}

/** ANON_REQ: dest(6) + ephemeral_pubkey(32) + MAC(4) + encrypted */
function decodeAnonReq(buf) {
  if (buf.length < 42) return { error: 'too short', raw: buf.toString('hex') };
  return {
    destHash: buf.subarray(0, 6).toString('hex'),
    ephemeralPubKey: buf.subarray(6, 38).toString('hex'),
    mac: buf.subarray(38, 42).toString('hex'),
    encryptedData: buf.subarray(42).toString('hex'),
  };
}

/** PATH: dest(6) + src(6) + MAC(4) + path_data */
function decodePath_payload(buf) {
  if (buf.length < 16) return { error: 'too short', raw: buf.toString('hex') };
  return {
    destHash: buf.subarray(0, 6).toString('hex'),
    srcHash: buf.subarray(6, 12).toString('hex'),
    mac: buf.subarray(12, 16).toString('hex'),
    pathData: buf.subarray(16).toString('hex'),
  };
}

/** TRACE: flags(1) + tag(4) + dest(6) + src(1) */
function decodeTrace(buf) {
  if (buf.length < 12) return { error: 'too short', raw: buf.toString('hex') };
  return {
    flags: buf[0],
    tag: buf.readUInt32LE(1),
    destHash: buf.subarray(5, 11).toString('hex'),
    srcHash: buf.subarray(11, 12).toString('hex'),
  };
}

// Dispatcher
function decodePayload(type, buf, channelKeys) {
  switch (type) {
    case 0x00: return { type: 'REQ', ...decodeEncryptedPayload(buf) };
    case 0x01: return { type: 'RESPONSE', ...decodeEncryptedPayload(buf) };
    case 0x02: return { type: 'TXT_MSG', ...decodeEncryptedPayload(buf) };
    case 0x03: return { type: 'ACK', ...decodeAck(buf) };
    case 0x04: return { type: 'ADVERT', ...decodeAdvert(buf) };
    case 0x05: return { type: 'GRP_TXT', ...decodeGrpTxt(buf, channelKeys) };
    case 0x07: return { type: 'ANON_REQ', ...decodeAnonReq(buf) };
    case 0x08: return { type: 'PATH', ...decodePath_payload(buf) };
    case 0x09: return { type: 'TRACE', ...decodeTrace(buf) };
    default:   return { type: 'UNKNOWN', raw: buf.toString('hex') };
  }
}

// --- Main decoder ---

function decodePacket(hexString, channelKeys) {
  const hex = hexString.replace(/\s+/g, '');
  const buf = Buffer.from(hex, 'hex');

  if (buf.length < 2) throw new Error('Packet too short (need at least header + pathLength)');

  const header = decodeHeader(buf[0]);
  const pathByte = buf[1];
  let offset = 2;

  // Transport codes for TRANSPORT_FLOOD / TRANSPORT_DIRECT
  let transportCodes = null;
  if (TRANSPORT_ROUTES.has(header.routeType)) {
    if (buf.length < offset + 4) throw new Error('Packet too short for transport codes');
    transportCodes = {
      nextHop: buf.subarray(offset, offset + 2).toString('hex').toUpperCase(),
      lastHop: buf.subarray(offset + 2, offset + 4).toString('hex').toUpperCase(),
    };
    offset += 4;
  }

  // Path
  const path = decodePath(pathByte, buf, offset);
  offset += path.bytesConsumed;

  // Payload (rest of buffer)
  const payloadBuf = buf.subarray(offset);
  const payload = decodePayload(header.payloadType, payloadBuf, channelKeys);

  return {
    header: {
      routeType: header.routeType,
      routeTypeName: header.routeTypeName,
      payloadType: header.payloadType,
      payloadTypeName: header.payloadTypeName,
      payloadVersion: header.payloadVersion,
    },
    transportCodes,
    path: {
      hashSize: path.hashSize,
      hashCount: path.hashCount,
      hops: path.hops,
    },
    payload,
    raw: hex.toUpperCase(),
  };
}

module.exports = { decodePacket, ROUTE_TYPES, PAYLOAD_TYPES };

// --- Tests ---
if (require.main === module) {
  console.log('=== Test 1: ADVERT, FLOOD, 5 hops (2-byte hashes), "Test Repeater" ===');
  const pkt1 = decodePacket(
    '11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172'
  );
  console.log(JSON.stringify(pkt1, null, 2));
  console.log();

  // Assertions
  const assert = (cond, msg) => { if (!cond) throw new Error('ASSERT FAILED: ' + msg); };
  assert(pkt1.header.routeTypeName === 'FLOOD', 'route should be FLOOD');
  assert(pkt1.header.payloadTypeName === 'ADVERT', 'payload should be ADVERT');
  assert(pkt1.path.hashSize === 2, 'hashSize should be 2');
  assert(pkt1.path.hashCount === 5, 'hashCount should be 5');
  assert(pkt1.path.hops[0] === '1000', 'first hop should be 1000');
  assert(pkt1.path.hops[1] === 'D818', 'second hop should be D818');
  assert(pkt1.transportCodes === null, 'FLOOD has no transport codes');
  assert(pkt1.payload.name === 'Test Repeater', 'name should be "Test Repeater"');
  console.log('✅ Test 1 passed\n');

  console.log('=== Test 2: ADVERT, FLOOD, 0 hops (zero-path) ===');
  // Build a minimal advert: header=0x11 (FLOOD+ADVERT), pathLen=0x00 (1-byte hashes, 0 hops)
  // Then a minimal advert payload: 32-byte pubkey + 4-byte ts + 64-byte sig + flags(1)
  const fakePubKey = '00'.repeat(32);
  const fakeTs = '78563412'; // LE = 0x12345678
  const fakeSig = 'AA'.repeat(64);
  const flags = '00'; // no location, no name
  const pkt2hex = '1100' + fakePubKey + fakeTs + fakeSig + flags;
  const pkt2 = decodePacket(pkt2hex);
  console.log(JSON.stringify(pkt2, null, 2));
  console.log();

  assert(pkt2.header.routeTypeName === 'FLOOD', 'route should be FLOOD');
  assert(pkt2.header.payloadTypeName === 'ADVERT', 'payload should be ADVERT');
  assert(pkt2.path.hashSize === 1, 'hashSize should be 1');
  assert(pkt2.path.hashCount === 0, 'hashCount should be 0');
  assert(pkt2.path.hops.length === 0, 'no hops');
  assert(pkt2.payload.timestamp === 0x12345678, 'timestamp');
  console.log('✅ Test 2 passed\n');

  console.log('All tests passed ✅');
}
