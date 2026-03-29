#!/usr/bin/env node
'use strict';

/**
 * Seed synthetic test data into a running CoreScope server.
 * Usage: node tools/seed-test-data.js [baseUrl]
 * Default: http://localhost:13581
 */

const crypto = require('crypto');

const BASE = process.argv[2] || process.env.BASE_URL || 'http://localhost:13581';

const OBSERVERS = [
  { id: 'E2E-SJC-1', iata: 'SJC' },
  { id: 'E2E-SFO-2', iata: 'SFO' },
  { id: 'E2E-OAK-3', iata: 'OAK' },
];

const NODE_NAMES = [
  'TestNode Alpha', 'TestNode Beta', 'TestNode Gamma', 'TestNode Delta',
  'TestNode Epsilon', 'TestNode Zeta', 'TestNode Eta', 'TestNode Theta',
];

function rand(a, b) { return Math.random() * (b - a) + a; }
function randInt(a, b) { return Math.floor(rand(a, b + 1)); }
function pick(a) { return a[randInt(0, a.length - 1)]; }
function randomBytes(n) { return crypto.randomBytes(n); }
function pubkeyFor(name) { return crypto.createHash('sha256').update(name).digest(); }

function encodeHeader(routeType, payloadType, ver = 0) {
  return (routeType & 0x03) | ((payloadType & 0x0F) << 2) | ((ver & 0x03) << 6);
}

function buildPath(hopCount, hashSize = 2) {
  const pathByte = ((hashSize - 1) << 6) | (hopCount & 0x3F);
  const hops = crypto.randomBytes(hashSize * hopCount);
  return { pathByte, hops };
}

function buildAdvert(name, role) {
  const pubKey = pubkeyFor(name);
  const ts = Buffer.alloc(4); ts.writeUInt32LE(Math.floor(Date.now() / 1000));
  const sig = randomBytes(64);
  let flags = 0x80 | 0x10; // hasName + hasLocation
  if (role === 'repeater') flags |= 0x02;
  else if (role === 'room') flags |= 0x04;
  else if (role === 'sensor') flags |= 0x08;
  else flags |= 0x01;
  const nameBuf = Buffer.from(name, 'utf8');
  const appdata = Buffer.alloc(9 + nameBuf.length);
  appdata[0] = flags;
  appdata.writeInt32LE(Math.round(37.34 * 1e6), 1);
  appdata.writeInt32LE(Math.round(-121.89 * 1e6), 5);
  nameBuf.copy(appdata, 9);
  const payload = Buffer.concat([pubKey, ts, sig, appdata]);
  const header = encodeHeader(1, 0x04, 0); // FLOOD + ADVERT
  const { pathByte, hops } = buildPath(randInt(0, 3));
  return Buffer.concat([Buffer.from([header, pathByte]), hops, payload]);
}

function buildGrpTxt(channelHash = 0) {
  const mac = randomBytes(2);
  const enc = randomBytes(randInt(10, 40));
  const payload = Buffer.concat([Buffer.from([channelHash]), mac, enc]);
  const header = encodeHeader(1, 0x05, 0); // FLOOD + GRP_TXT
  const { pathByte, hops } = buildPath(randInt(0, 3));
  return Buffer.concat([Buffer.from([header, pathByte]), hops, payload]);
}

/**
 * Build a properly encrypted GRP_TXT packet that decrypts to a CHAN message.
 * Uses #LongFast channel key from channel-rainbow.json.
 */
function buildEncryptedGrpTxt(sender, message) {
  try {
    const CryptoJS = require('crypto-js');
    const { ChannelCrypto } = require('@michaelhart/meshcore-decoder/dist/crypto/channel-crypto');

    const channelKey = '2cc3d22840e086105ad73443da2cacb8'; // #LongFast
    const text = `${sender}: ${message}`;
    const buf = Buffer.alloc(5 + text.length + 1);
    buf.writeUInt32LE(Math.floor(Date.now() / 1000), 0);
    buf[4] = 0;
    buf.write(text + '\0', 5, 'utf8');

    const padded = Buffer.alloc(Math.ceil(buf.length / 16) * 16);
    buf.copy(padded);

    const keyWords = CryptoJS.enc.Hex.parse(channelKey);
    const plaintextWords = CryptoJS.enc.Hex.parse(padded.toString('hex'));
    const encrypted = CryptoJS.AES.encrypt(plaintextWords, keyWords, {
      mode: CryptoJS.mode.ECB, padding: CryptoJS.pad.NoPadding
    });
    const cipherHex = encrypted.ciphertext.toString(CryptoJS.enc.Hex);

    const channelSecret = Buffer.alloc(32);
    Buffer.from(channelKey, 'hex').copy(channelSecret);
    const mac = CryptoJS.HmacSHA256(
      CryptoJS.enc.Hex.parse(cipherHex),
      CryptoJS.enc.Hex.parse(channelSecret.toString('hex'))
    );
    const macHex = mac.toString(CryptoJS.enc.Hex).substring(0, 4);

    const chHash = ChannelCrypto.calculateChannelHash('#LongFast');
    const grpPayload = Buffer.from(
      chHash.toString(16).padStart(2, '0') + macHex + cipherHex, 'hex'
    );

    const header = encodeHeader(1, 0x05, 0);
    const { pathByte, hops } = buildPath(randInt(0, 2));
    return Buffer.concat([Buffer.from([header, pathByte]), hops, grpPayload]);
  } catch (e) {
    // Fallback to unencrypted if crypto libs unavailable
    return buildGrpTxt(0);
  }
}

function buildAck() {
  const payload = randomBytes(18);
  const header = encodeHeader(2, 0x03, 0);
  const { pathByte, hops } = buildPath(randInt(0, 2));
  return Buffer.concat([Buffer.from([header, pathByte]), hops, payload]);
}

function buildTxtMsg() {
  const payload = Buffer.concat([randomBytes(6), randomBytes(6), randomBytes(4), randomBytes(20)]);
  const header = encodeHeader(2, 0x02, 0);
  const { pathByte, hops } = buildPath(randInt(0, 2));
  return Buffer.concat([Buffer.from([header, pathByte]), hops, payload]);
}

function computeContentHash(hex) {
  return crypto.createHash('sha256').update(hex.toUpperCase()).digest('hex').substring(0, 16);
}

async function post(path, body) {
  const r = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return { status: r.status, data: await r.json() };
}

async function main() {
  console.log(`Seeding test data into ${BASE}...`);

  const packets = [];

  // 1. ADVERTs for each node (creates nodes with location for map)
  const roles = ['repeater', 'repeater', 'room', 'companion', 'repeater', 'companion', 'sensor', 'repeater'];
  for (let i = 0; i < NODE_NAMES.length; i++) {
    const obs = pick(OBSERVERS);
    const hex = buildAdvert(NODE_NAMES[i], roles[i]).toString('hex').toUpperCase();
    const hash = computeContentHash(hex);
    packets.push({ hex, observer: obs.id, region: obs.iata, hash, snr: 5.0, rssi: -80 });
    // Send same advert from multiple observers for compare page
    for (const otherObs of OBSERVERS) {
      if (otherObs.id !== obs.id) {
        packets.push({ hex, observer: otherObs.id, region: otherObs.iata, hash, snr: rand(-2, 10), rssi: rand(-110, -60) });
      }
    }
  }

  // 2. Encrypted GRP_TXT packets (creates channel messages for channels page)
  const chatMessages = [
    ['Alice', 'Hello everyone!'], ['Bob', 'Hey Alice!'], ['Charlie', 'Good morning'],
    ['Alice', 'How is the mesh today?'], ['Bob', 'Looking great, 8 nodes online'],
    ['Charlie', 'I just set up a new repeater'], ['Alice', 'Nice! Where is it?'],
    ['Bob', 'Signal looks strong from here'], ['Charlie', 'On top of the hill'],
    ['Alice', 'Perfect location!'],
  ];
  for (const [sender, message] of chatMessages) {
    const obs = pick(OBSERVERS);
    const hex = buildEncryptedGrpTxt(sender, message).toString('hex').toUpperCase();
    const hash = computeContentHash(hex);
    packets.push({ hex, observer: obs.id, region: obs.iata, hash, snr: rand(-2, 10), rssi: rand(-110, -60) });
  }

  // 3. Unencrypted GRP_TXT packets (won't create channel entries but add packet variety)
  for (let i = 0; i < 10; i++) {
    const obs = pick(OBSERVERS);
    const hex = buildGrpTxt(randInt(0, 3)).toString('hex').toUpperCase();
    const hash = computeContentHash(hex);
    packets.push({ hex, observer: obs.id, region: obs.iata, hash, snr: rand(-2, 10), rssi: rand(-110, -60) });
  }

  // 3. ACK packets
  for (let i = 0; i < 15; i++) {
    const obs = pick(OBSERVERS);
    const hex = buildAck().toString('hex').toUpperCase();
    const hash = computeContentHash(hex);
    packets.push({ hex, observer: obs.id, region: obs.iata, hash, snr: rand(-2, 10), rssi: rand(-110, -60) });
  }

  // 4. TXT_MSG packets
  for (let i = 0; i < 15; i++) {
    const obs = pick(OBSERVERS);
    const hex = buildTxtMsg().toString('hex').toUpperCase();
    const hash = computeContentHash(hex);
    packets.push({ hex, observer: obs.id, region: obs.iata, hash, snr: rand(-2, 10), rssi: rand(-110, -60) });
  }

  // 5. Extra packets with shared hashes (for trace/compare)
  for (let t = 0; t < 5; t++) {
    const hex = buildGrpTxt(0).toString('hex').toUpperCase();
    const traceHash = computeContentHash(hex);
    for (const obs of OBSERVERS) {
      packets.push({ hex, observer: obs.id, region: obs.iata, hash: traceHash, snr: 5, rssi: -80 });
    }
  }

  console.log(`Injecting ${packets.length} packets...`);
  let ok = 0, fail = 0;
  for (const pkt of packets) {
    const r = await post('/api/packets', pkt);
    if (r.status === 200) ok++;
    else { fail++; if (fail <= 3) console.error('  Inject fail:', r.data); }
  }
  console.log(`Done: ${ok} ok, ${fail} fail`);

  if (fail > 0) {
    process.exit(1);
  }
}

main().catch(err => { console.error(err); process.exit(1); });
