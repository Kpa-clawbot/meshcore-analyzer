#!/usr/bin/env node
'use strict';

const crypto = require('crypto');
const http = require('http');

const API_URL = 'http://localhost:3000/api/packets';

// --- Bay Area mesh network topology ---
const OBSERVERS = [
  { id: 'SJC-Lick-Observatory',  region: 'SJC', lat: 37.3414, lon: -121.6429 },
  { id: 'SJC-Comm-Hill',         region: 'SJC', lat: 37.3375, lon: -121.8377 },
  { id: 'SFO-Twin-Peaks',        region: 'SFO', lat: 37.7544, lon: -122.4477 },
  { id: 'SFO-Bernal-Heights',    region: 'SFO', lat: 37.7426, lon: -122.4157 },
  { id: 'OAK-Grizzly-Peak',      region: 'OAK', lat: 37.8816, lon: -122.2446 },
  { id: 'MTV-Black-Mountain',    region: 'MTV', lat: 37.3209, lon: -122.1485 },
  { id: 'SCZ-UCSC-Tower',        region: 'SCZ', lat: 36.9916, lon: -122.0583 },
];

// Persistent nodes with fixed pubkeys and locations
const NODES = [
  { name: 'SJ-Downtown-RPT',      role: 'repeater',  lat: 37.3382, lon: -121.8863, cluster: 'SJC' },
  { name: 'SJ-Almaden-Solar',     role: 'repeater',  lat: 37.2504, lon: -121.8617, cluster: 'SJC' },
  { name: 'SJ-Japantown-Relay',   role: 'repeater',  lat: 37.3485, lon: -121.8950, cluster: 'SJC' },
  { name: 'MV-Googleplex-Node',   role: 'companion', lat: 37.4220, lon: -122.0841, cluster: 'MTV' },
  { name: 'MV-Shoreline-RPT',     role: 'repeater',  lat: 37.4300, lon: -122.0880, cluster: 'MTV' },
  { name: 'PA-University-Ave',    role: 'companion', lat: 37.4419, lon: -122.1430, cluster: 'MTV' },
  { name: 'SF-TwinPeaks-Solar',   role: 'repeater',  lat: 37.7544, lon: -122.4477, cluster: 'SFO' },
  { name: 'SF-Mission-Room',      role: 'room',      lat: 37.7599, lon: -122.4148, cluster: 'SFO' },
  { name: 'SF-SOMA-Sensor',       role: 'sensor',    lat: 37.7785, lon: -122.3893, cluster: 'SFO' },
  { name: 'SF-Sunset-Relay',      role: 'repeater',  lat: 37.7530, lon: -122.4944, cluster: 'SFO' },
  { name: 'Oak-Hills-Relay',      role: 'repeater',  lat: 37.8324, lon: -122.2390, cluster: 'OAK' },
  { name: 'Oak-Temescal-Node',    role: 'companion', lat: 37.8340, lon: -122.2600, cluster: 'OAK' },
  { name: 'Berkeley-Marina',      role: 'repeater',  lat: 37.8694, lon: -122.3100, cluster: 'OAK' },
  { name: 'Fremont-Hub',          role: 'companion', lat: 37.5485, lon: -121.9886, cluster: 'SJC' },
  { name: 'Sunnyvale-Central',    role: 'companion', lat: 37.3688, lon: -122.0363, cluster: 'MTV' },
  { name: 'Cupertino-Foothills',  role: 'repeater',  lat: 37.3230, lon: -122.0322, cluster: 'MTV' },
  { name: 'RedwoodCity-Harbor',   role: 'companion', lat: 37.5074, lon: -122.2117, cluster: 'MTV' },
  { name: 'Saratoga-Summit-RPT',  role: 'repeater',  lat: 37.2560, lon: -122.0230, cluster: 'SCZ' },
  { name: 'LosGatos-Creek',       role: 'companion', lat: 37.2306, lon: -121.9625, cluster: 'SCZ' },
  { name: 'SC-Boardwalk-Node',    role: 'companion', lat: 36.9641, lon: -122.0178, cluster: 'SCZ' },
  { name: 'HalfMoonBay-Coast',    role: 'repeater',  lat: 37.4636, lon: -122.4286, cluster: 'SFO' },
  { name: 'Pacifica-Fog-RPT',     role: 'repeater',  lat: 37.6138, lon: -122.4869, cluster: 'SFO' },
  { name: 'Napa-Valley-Hilltop',  role: 'repeater',  lat: 38.2975, lon: -122.2869, cluster: 'OAK' },
  { name: 'SanMateo-Bridge-RPT',  role: 'repeater',  lat: 37.5800, lon: -122.2530, cluster: 'MTV' },
  { name: 'Milpitas-Gateway',     role: 'companion', lat: 37.4323, lon: -121.8996, cluster: 'SJC' },
  { name: 'Campbell-Downtown',    role: 'companion', lat: 37.2872, lon: -121.9500, cluster: 'SJC' },
  { name: 'MorganHill-South',     role: 'companion', lat: 37.1305, lon: -121.6544, cluster: 'SJC' },
  { name: 'Gilroy-Garlic-Relay',  role: 'repeater',  lat: 37.0058, lon: -121.5683, cluster: 'SJC' },
  { name: 'DalyCity-Colma-RPT',   role: 'repeater',  lat: 37.6879, lon: -122.4702, cluster: 'SFO' },
  { name: 'Burlingame-RPT',       role: 'repeater',  lat: 37.5841, lon: -122.3660, cluster: 'MTV' },
  { name: 'Hayward-Hills',        role: 'repeater',  lat: 37.6688, lon: -122.0808, cluster: 'OAK' },
  { name: 'Newark-Bridge-Node',   role: 'companion', lat: 37.5316, lon: -122.0402, cluster: 'OAK' },
];

// Generate stable pubkeys per node
NODES.forEach(n => { n.pubKey = crypto.createHash('sha256').update(n.name).digest('hex').slice(0, 64); });

const CHANNEL_HASHES = [
  { hash: 0xC3, name: 'public' },
  { hash: 0x7A, name: '#bayarea' },
  { hash: 0x3F, name: '#meshdev' },
  { hash: 0x91, name: '#offtopic' },
];

const CHAT_MESSAGES = {
  '#bayarea': [
    'Good morning from SJ!', 'Coverage test from Twin Peaks', 'Can confirm — signal reaches across the bay now',
    'New repeater going up on Mt Hamilton next week', 'Anyone want to do a range test Saturday?',
    'Fog is killing my signal today', 'Just hit 12 hops from Gilroy to Napa!',
    'Battery swap done on the solar repeater', 'Who maintains the Berkeley Marina node?',
    'Link budget looking great after the antenna upgrade', 'Need help with antenna alignment on Oak Hills',
    'Beautiful propagation conditions today', 'Testing new firmware build', 'Signal check from Half Moon Bay',
  ],
  '#meshdev': [
    'PR merged for the new path length encoding', 'Anyone tested the latest companion app build?',
    'Found a bug in flood routing — packets loop when path > 6 hops',
    'New ADVERT format adds 8 bytes for altitude data', 'Working on trace route visualization',
    'The decoder library needs a fix for v1 headers', 'Who broke the MQTT bridge? 😅',
    'Benchmarking shows ~200ms latency across 8 hops', 'Memory leak in the repeater firmware — investigating',
    'Released v0.4.2 with power management fixes',
  ],
  '#offtopic': [
    'Anyone catch the Warriors game?', 'Best tacos in the mission — El Farolito, fight me',
    'Weather station says 72°F, feels like 90 on the roof', 'My cat unplugged the repeater again',
    'Happy Friday everyone!', 'Who else is going to the maker faire?',
    'Just got a new solar panel, 100W for $40', 'Coffee recommendations near downtown SJ?',
  ],
  'public': [
    'Hello mesh!', 'Testing testing', 'CQ CQ CQ', 'Anyone there?', 'GM from Bay Area mesh',
    'New node online', 'Running range test', '73s everyone', 'First packet!',
    'Checking in from the coast', 'Mesh is alive!', 'Good copy on all channels',
  ],
};

// --- Helpers ---
function rand(min, max) { return Math.random() * (max - min) + min; }
function randInt(min, max) { return Math.floor(rand(min, max + 1)); }
function pick(arr) { return arr[randInt(0, arr.length - 1)]; }
function gaussRand(mean, std) {
  const u1 = Math.random(), u2 = Math.random();
  return mean + std * Math.sqrt(-2 * Math.log(u1)) * Math.cos(2 * Math.PI * u2);
}

// Time spread: packets over last 7 days, clustered towards recent
function randomTimestamp() {
  const now = Date.now();
  const sevenDays = 7 * 24 * 60 * 60 * 1000;
  // Exponential distribution — more packets recently
  const age = sevenDays * Math.pow(Math.random(), 2);
  return new Date(now - age).toISOString();
}

// Nearby observers see the same packet (geographic clustering)
function getObserversForCluster(cluster) {
  const clusterObs = {
    SJC: ['SJC-Lick-Observatory', 'SJC-Comm-Hill'],
    SFO: ['SFO-Twin-Peaks', 'SFO-Bernal-Heights'],
    OAK: ['OAK-Grizzly-Peak'],
    MTV: ['MTV-Black-Mountain'],
    SCZ: ['SCZ-UCSC-Tower'],
  };
  // Always include local, sometimes include neighbors
  const local = clusterObs[cluster] || [pick(OBSERVERS).id];
  const result = [...local];
  // 30% chance a nearby cluster also hears it
  if (Math.random() < 0.3) {
    const neighbors = { SJC: 'MTV', MTV: 'SJC', SFO: 'OAK', OAK: 'SFO', SCZ: 'SJC' };
    const neighbor = neighbors[cluster];
    if (neighbor && clusterObs[neighbor]) result.push(pick(clusterObs[neighbor]));
  }
  return result;
}

// --- Packet Builders ---

function buildHeader(payloadType, routeType = 1, version = 0) {
  return (version << 6) | (payloadType << 2) | routeType;
}

function buildPath(maxHops = 8) {
  const hashSize = Math.random() < 0.7 ? 2 : 1;
  const hashCount = randInt(0, maxHops);
  const pathByte = ((hashSize - 1) << 6) | hashCount;
  const pathData = crypto.randomBytes(hashSize * hashCount);
  return { pathByte, pathData, hashCount };
}

function buildAdvert(node) {
  const pubKey = Buffer.from(node.pubKey, 'hex');
  const timestamp = Math.floor(Date.now() / 1000) - randInt(0, 3600);
  const tsBuf = Buffer.alloc(4);
  tsBuf.writeUInt32LE(timestamp);
  const signature = crypto.randomBytes(64);

  let flags = 0x80 | 0x10; // hasName + hasLocation
  if (node.role === 'repeater') flags |= 0x02;
  if (node.role === 'room') flags |= 0x04;
  if (node.role === 'sensor') flags |= 0x08;
  if (Math.random() < 0.6) flags |= 0x01; // chat

  // Slight GPS jitter
  const lat = node.lat + gaussRand(0, 0.0002);
  const lon = node.lon + gaussRand(0, 0.0002);
  const latBuf = Buffer.alloc(4); latBuf.writeInt32LE(Math.round(lat * 1e6));
  const lonBuf = Buffer.alloc(4); lonBuf.writeInt32LE(Math.round(lon * 1e6));

  return { payload: Buffer.concat([pubKey, tsBuf, signature, Buffer.from([flags]), latBuf, lonBuf, Buffer.from(node.name, 'utf8')]), cluster: node.cluster };
}

function buildGrpTxt(senderNode) {
  const ch = pick(CHANNEL_HASHES);
  const mac = crypto.randomBytes(2);
  const msgs = CHAT_MESSAGES[ch.name] || CHAT_MESSAGES['public'];
  const sender = senderNode ? senderNode.name : pick(NODES).name;
  const msg = `${sender}: ${pick(msgs)}`;
  return { payload: Buffer.concat([Buffer.from([ch.hash]), mac, Buffer.from(msg, 'utf8')]), cluster: senderNode?.cluster || pick(NODES).cluster };
}

function buildAck() {
  return { payload: crypto.randomBytes(18), cluster: pick(NODES).cluster };
}

function buildTxtMsg(senderNode) {
  const msg = pick(CHAT_MESSAGES['public']);
  return { payload: Buffer.concat([crypto.randomBytes(16), Buffer.from(msg, 'utf8')]), cluster: senderNode?.cluster || pick(NODES).cluster };
}

function buildTrace() {
  const flags = randInt(0, 3);
  const tag = Buffer.alloc(4); tag.writeUInt32LE(randInt(1, 100000));
  const dest = crypto.randomBytes(6);
  const src = crypto.randomBytes(1);
  const snrPath = Buffer.alloc(randInt(0, 6));
  for (let i = 0; i < snrPath.length; i++) snrPath[i] = randInt(0, 255);
  return { payload: Buffer.concat([Buffer.from([flags]), tag, dest, src, snrPath]), cluster: pick(NODES).cluster };
}

// --- Generate full packet hex ---

function generatePacket() {
  const r = Math.random();
  let payloadType, built, routeType;
  const node = pick(NODES);

  if (r < 0.50) {
    payloadType = 0x04; // ADVERT
    built = buildAdvert(node);
    routeType = 1;
  } else if (r < 0.78) {
    payloadType = 0x05; // GRP_TXT
    built = buildGrpTxt(node);
    routeType = 1;
  } else if (r < 0.88) {
    payloadType = 0x03; // ACK
    built = buildAck();
    routeType = 1;
  } else if (r < 0.93) {
    payloadType = 0x02; // TXT_MSG
    built = buildTxtMsg(node);
    routeType = Math.random() < 0.4 ? 2 : 1;
  } else if (r < 0.97) {
    payloadType = 0x09; // TRACE
    built = buildTrace();
    routeType = 1;
  } else {
    payloadType = pick([0x00, 0x01, 0x08]); // REQ/RESPONSE/PATH
    built = { payload: Buffer.concat([crypto.randomBytes(16), crypto.randomBytes(randInt(4, 20))]), cluster: pick(NODES).cluster };
    routeType = 1;
  }

  const { pathByte, pathData, hashCount } = buildPath(payloadType === 0x04 ? 4 : 8);
  const headerByte = buildHeader(payloadType, routeType);

  let transportCodes = Buffer.alloc(0);
  if (routeType === 0 || routeType === 3) {
    transportCodes = crypto.randomBytes(4);
  }

  const hex = Buffer.concat([Buffer.from([headerByte, pathByte]), transportCodes, pathData, built.payload]).toString('hex').toUpperCase();
  const hash = crypto.createHash('sha256').update(hex).digest('hex').slice(0, 16);

  return { hex, hash, cluster: built.cluster, payloadType };
}

// --- API posting ---

function postPacket(body) {
  return new Promise((resolve, reject) => {
    const data = JSON.stringify(body);
    const url = new URL(API_URL);
    const req = http.request({
      hostname: url.hostname, port: url.port, path: url.pathname, method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data) },
    }, (res) => {
      let body = '';
      res.on('data', c => body += c);
      res.on('end', () => {
        if (res.statusCode >= 400) reject(new Error(`HTTP ${res.statusCode}: ${body}`));
        else resolve(JSON.parse(body));
      });
    });
    req.on('error', reject);
    req.write(data);
    req.end();
  });
}

// --- Main ---

async function main() {
  const count = parseInt(process.argv[2] || '500');
  console.log(`Generating ${count} packets with realistic Bay Area topology...`);
  console.log(`  ${NODES.length} nodes, ${OBSERVERS.length} observers, ${CHANNEL_HASHES.length} channels`);

  let posted = 0, errors = 0;

  for (let i = 0; i < count; i++) {
    const pkt = generatePacket();
    const observers = getObserversForCluster(pkt.cluster);

    for (const obsId of observers) {
      const obs = OBSERVERS.find(o => o.id === obsId);
      try {
        await postPacket({
          hex: pkt.hex,
          hash: pkt.hash,
          observer: obsId,
          snr: Math.round(gaussRand(2, 4) * 10) / 10,
          rssi: Math.round(gaussRand(-85, 12)),
          region: obs?.region || 'UNK',
        });
        posted++;
      } catch (e) {
        errors++;
        if (errors < 5) console.error(`  Error: ${e.message}`);
      }
    }

    if ((i + 1) % 100 === 0) console.log(`  ${i + 1}/${count} packets generated (${posted} observations posted)`);
  }

  console.log(`\nDone! ${count} packets → ${posted} observations posted (${errors} errors)`);
}

main().catch(e => { console.error(e); process.exit(1); });
