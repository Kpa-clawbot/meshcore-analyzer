/**
 * MeshCore Packet Decoder
 * Custom implementation — does NOT use meshcore-decoder library (known path_length bug).
 *
 * Packet layout (per firmware docs/packet_format.md):
 *   [header(1)] [transportCodes?(4)] [pathLength(1)] [path hops] [payload...]
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
  0x06: 'GRP_DATA',
  0x07: 'ANON_REQ',
  0x08: 'PATH',
  0x09: 'TRACE',
  0x0A: 'MULTIPART',
  0x0B: 'CONTROL',
  0x0F: 'RAW_CUSTOM',
};

// Route types that carry transport codes (2x uint16_t, 4 bytes total)
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
  const available = buf.length - offset;
  // Cap to what the buffer actually holds — corrupt packets may claim more hops than exist
  const safeCount = Math.min(hashCount, Math.floor(available / hashSize));
  const totalBytes = safeCount * hashSize;
  const hops = [];

  for (let i = 0; i < safeCount; i++) {
    hops.push(buf.subarray(offset + i * hashSize, offset + i * hashSize + hashSize).toString('hex').toUpperCase());
  }

  return {
    hashSize,
    hashCount: safeCount,
    hops,
    bytesConsumed: totalBytes,
    truncated: safeCount < hashCount,
  };
}

// --- Payload decoders ---

/** REQ / RESPONSE / TXT_MSG: dest(1) + src(1) + MAC(2) + encrypted (PAYLOAD_VER_1, per Mesh.cpp) */
function decodeEncryptedPayload(buf) {
  if (buf.length < 4) return { error: 'too short', raw: buf.toString('hex') };
  return {
    destHash: buf.subarray(0, 1).toString('hex'),
    srcHash: buf.subarray(1, 2).toString('hex'),
    mac: buf.subarray(2, 4).toString('hex'),
    encryptedData: buf.subarray(4).toString('hex'),
  };
}

/** ACK: checksum(4) — CRC of message timestamp + text + sender pubkey (per Mesh.cpp createAck) */
function decodeAck(buf) {
  if (buf.length < 4) return { error: 'too short', raw: buf.toString('hex') };
  return {
    ackChecksum: buf.subarray(0, 4).toString('hex'),
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
    const advType = flags & 0x0F; // lower nibble is enum type, not individual bits
    result.flags = {
      raw: flags,
      type: advType,
      chat: advType === 1,
      repeater: advType === 2,
      room: advType === 3,
      sensor: advType === 4,
      hasLocation: !!(flags & 0x10),
      hasFeat1: !!(flags & 0x20),
      hasFeat2: !!(flags & 0x40),
      hasName: !!(flags & 0x80),
    };

    let off = 1;
    if (result.flags.hasLocation && appdata.length >= off + 8) {
      result.lat = appdata.readInt32LE(off) / 1e6;
      result.lon = appdata.readInt32LE(off + 4) / 1e6;
      off += 8;
    }
    if (result.flags.hasFeat1 && appdata.length >= off + 2) {
      result.feat1 = appdata.readUInt16LE(off);
      off += 2;
    }
    if (result.flags.hasFeat2 && appdata.length >= off + 2) {
      result.feat2 = appdata.readUInt16LE(off);
      off += 2;
    }
    if (result.flags.hasName) {
      // Find null terminator to separate name from trailing telemetry bytes
      let nameEnd = appdata.length;
      for (let i = off; i < appdata.length; i++) {
        if (appdata[i] === 0x00) { nameEnd = i; break; }
      }
      let name = appdata.subarray(off, nameEnd).toString('utf8');
      name = name.replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, '');
      result.name = name;
      off = nameEnd;
      // Skip null terminator(s)
      while (off < appdata.length && appdata[off] === 0x00) off++;
    }

    // Telemetry bytes after name: battery_mv(2 LE) + temperature_c(2 LE, signed, /100)
    // Only sensor nodes (advType=4) carry telemetry bytes.
    if (result.flags.sensor && off + 4 <= appdata.length) {
      const batteryMv = appdata.readUInt16LE(off);
      const tempRaw = appdata.readInt16LE(off + 2);
      const tempC = tempRaw / 100.0;
      if (batteryMv > 0 && batteryMv <= 10000) {
        result.battery_mv = batteryMv;
      }
      // Raw int16 / 100 → °C; accept -50°C to 100°C (raw: -5000 to 10000)
      if (tempRaw >= -5000 && tempRaw <= 10000) {
        result.temperature_c = tempC;
      }
    }
  }

  return result;
}

/**
 * Check if text contains non-printable characters (binary garbage).
 * Returns true if more than 2 non-printable chars found (excluding \n, \t).
 */
function hasNonPrintableChars(text) {
  if (!text) return false;
  let count = 0;
  for (let i = 0; i < text.length; i++) {
    const code = text.charCodeAt(i);
    if (code < 0x20 && code !== 0x0A && code !== 0x09) count++;
    else if (code === 0xFFFD) count++;  // Unicode replacement char (invalid UTF-8)
    if (count > 2) return true;
  }
  return false;
}

/** GRP_TXT: channel_hash(1) + MAC(2) + encrypted */
function decodeGrpTxt(buf, channelKeys) {
  if (buf.length < 3) return { error: 'too short', raw: buf.toString('hex') };
  const channelHash = buf[0];
  const channelHashHex = channelHash.toString(16).padStart(2, '0').toUpperCase();
  const mac = buf.subarray(1, 3).toString('hex');
  const encryptedData = buf.subarray(3).toString('hex');

  const hasKeys = channelKeys && Object.keys(channelKeys).length > 0;

  // Try decryption with known channel keys
  if (hasKeys && encryptedData.length >= 10) {
    try {
      const { ChannelCrypto } = require('@michaelhart/meshcore-decoder/dist/crypto/channel-crypto');
      for (const [name, key] of Object.entries(channelKeys)) {
        const result = ChannelCrypto.decryptGroupTextMessage(encryptedData, mac, key);
        if (result.success && result.data) {
          const text = result.data.sender && result.data.message
            ? `${result.data.sender}: ${result.data.message}`
            : result.data.message || '';
          // Validate decrypted text is printable UTF-8 (not binary garbage)
          if (hasNonPrintableChars(text)) {
            return {
              type: 'GRP_TXT', channelHash, channelHashHex, channel: name,
              decryptionStatus: 'decryption_failed', text: null, mac, encryptedData,
            };
          }
          return {
            type: 'CHAN',
            channel: name,
            channelHash,
            channelHashHex,
            decryptionStatus: 'decrypted',
            sender: result.data.sender || null,
            text,
            sender_timestamp: result.data.timestamp,
            flags: result.data.flags,
          };
        }
      }
    } catch (e) { /* decryption failed, fall through */ }

    return { type: 'GRP_TXT', channelHash, channelHashHex, decryptionStatus: 'decryption_failed', mac, encryptedData };
  }

  return { type: 'GRP_TXT', channelHash, channelHashHex, decryptionStatus: 'no_key', mac, encryptedData };
}

/** ANON_REQ: dest(1) + ephemeral_pubkey(32) + MAC(2) + encrypted */
function decodeAnonReq(buf) {
  if (buf.length < 35) return { error: 'too short', raw: buf.toString('hex') };
  return {
    destHash: buf.subarray(0, 1).toString('hex'),
    ephemeralPubKey: buf.subarray(1, 33).toString('hex'),
    mac: buf.subarray(33, 35).toString('hex'),
    encryptedData: buf.subarray(35).toString('hex'),
  };
}

/** PATH: dest(1) + src(1) + MAC(2) + path_data */
function decodePath_payload(buf) {
  if (buf.length < 4) return { error: 'too short', raw: buf.toString('hex') };
  return {
    destHash: buf.subarray(0, 1).toString('hex'),
    srcHash: buf.subarray(1, 2).toString('hex'),
    mac: buf.subarray(2, 4).toString('hex'),
    pathData: buf.subarray(4).toString('hex'),
  };
}

/** TRACE: tag(4) + authCode(4) + flags(1) + pathData (per Mesh.cpp onRecvPacket TRACE) */
function decodeTrace(buf) {
  if (buf.length < 9) return { error: 'too short', raw: buf.toString('hex') };
  return {
    tag: buf.readUInt32LE(0),
    authCode: buf.subarray(4, 8).toString('hex'),
    flags: buf[8],
    pathData: buf.subarray(9).toString('hex'),
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
  let offset = 1;

  // Transport codes for TRANSPORT_FLOOD / TRANSPORT_DIRECT — BEFORE path_length per spec
  let transportCodes = null;
  if (TRANSPORT_ROUTES.has(header.routeType)) {
    if (buf.length < offset + 4) throw new Error('Packet too short for transport codes');
    transportCodes = {
      code1: buf.subarray(offset, offset + 2).toString('hex').toUpperCase(),
      code2: buf.subarray(offset + 2, offset + 4).toString('hex').toUpperCase(),
    };
    offset += 4;
  }

  // Path length byte — AFTER transport codes per spec
  const pathByte = buf[offset++];

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
      truncated: path.truncated,
    },
    payload,
    raw: hex.toUpperCase(),
  };
}

// --- ADVERT validation ---

const VALID_ROLES = new Set(['repeater', 'companion', 'room', 'sensor']);

/**
 * Validate decoded ADVERT data before upserting into the DB.
 * Returns { valid: true } or { valid: false, reason: string }.
 */
function validateAdvert(advert) {
  if (!advert || advert.error) return { valid: false, reason: advert?.error || 'null advert' };

  // pubkey must be at least 16 hex chars (8 bytes) and not all zeros
  const pk = advert.pubKey || '';
  if (pk.length < 16) return { valid: false, reason: `pubkey too short (${pk.length} hex chars)` };
  if (/^0+$/.test(pk)) return { valid: false, reason: 'pubkey is all zeros' };

  // lat/lon must be in valid ranges if present
  if (advert.lat != null) {
    if (!Number.isFinite(advert.lat) || advert.lat < -90 || advert.lat > 90) {
      return { valid: false, reason: `invalid lat: ${advert.lat}` };
    }
  }
  if (advert.lon != null) {
    if (!Number.isFinite(advert.lon) || advert.lon < -180 || advert.lon > 180) {
      return { valid: false, reason: `invalid lon: ${advert.lon}` };
    }
  }

  // name must not contain control chars (except space) or be garbage
  if (advert.name != null) {
    // eslint-disable-next-line no-control-regex
    if (/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/.test(advert.name)) {
      return { valid: false, reason: 'name contains control characters' };
    }
    // Reject names that are mostly non-printable or suspiciously long
    if (advert.name.length > 64) {
      return { valid: false, reason: `name too long (${advert.name.length} chars)` };
    }
  }

  // role derivation check — flags byte should produce a known role
  if (advert.flags) {
    const role = advert.flags.repeater ? 'repeater' : advert.flags.room ? 'room' : advert.flags.sensor ? 'sensor' : 'companion';
    if (!VALID_ROLES.has(role)) return { valid: false, reason: `unknown role: ${role}` };
  }

  // timestamp: decoded but not currently used for node storage — skip validation

  return { valid: true };
}

module.exports = { decodePacket, validateAdvert, hasNonPrintableChars, ROUTE_TYPES, PAYLOAD_TYPES, VALID_ROLES };

// --- Tests ---
if (require.main === module) {
  console.log('=== Test 1: ADVERT, FLOOD, 5 hops (2-byte hashes), "Kpa Roof Solar" ===');
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
  assert(pkt1.payload.name === 'Kpa Roof Solar', 'name should be "Kpa Roof Solar"');
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
