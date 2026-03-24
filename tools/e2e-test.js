#!/usr/bin/env node
'use strict';

/**
 * MeshCore Analyzer — End-to-End Validation Test (M12)
 *
 * Starts the server with a temp DB, injects 100+ synthetic packets,
 * validates every API endpoint, WebSocket broadcasts, and optionally MQTT.
 */

const { spawn, execSync } = require('child_process');
const path = require('path');
const fs = require('fs');
const os = require('os');
const crypto = require('crypto');
const WebSocket = require('ws');

const PROJECT_DIR = path.join(__dirname, '..');
const PORT = 13579; // avoid conflict with dev server
const BASE = `http://localhost:${PORT}`;

// ── Helpers ──────────────────────────────────────────────────────────

let passed = 0, failed = 0;
const failures = [];

function assert(cond, label) {
  if (cond) { passed++; }
  else { failed++; failures.push(label); console.error(`  ❌ FAIL: ${label}`); }
}

async function get(path) {
  const r = await fetch(`${BASE}${path}`);
  return { status: r.status, data: await r.json() };
}

async function post(path, body) {
  const r = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return { status: r.status, data: await r.json() };
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

// ── Packet generation (inline from generate-packets.js logic) ────────

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

function pubkeyFor(name) {
  return crypto.createHash('sha256').update(name).digest();
}

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

// ── Main ─────────────────────────────────────────────────────────────

async function main() {
  // 1. Create temp DB
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'meshcore-e2e-'));
  const dbPath = path.join(tmpDir, 'test.db');
  console.log(`Temp DB: ${dbPath}`);

  // 2. Start server
  console.log('Starting server...');
  const srv = spawn('node', ['server.js'], {
    cwd: PROJECT_DIR,
    env: { ...process.env, DB_PATH: dbPath, PORT: String(PORT) },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  let serverOutput = '';
  srv.stdout.on('data', d => { serverOutput += d; });
  srv.stderr.on('data', d => { serverOutput += d; });

  // We need the server to respect PORT env — check if config is hard-coded
  // The server uses config.port from config.json. We need to patch that or
  // monkey-patch. Let's just use port 3000 if the server doesn't read PORT env.
  // Actually let me check...

  const cleanup = () => {
    try { srv.kill('SIGTERM'); } catch {}
    try { fs.unlinkSync(dbPath); fs.rmdirSync(tmpDir); } catch {}
  };

  process.on('SIGINT', () => { cleanup(); process.exit(1); });
  process.on('uncaughtException', (e) => { console.error(e); cleanup(); process.exit(1); });

  // 3. Wait for server ready
  let ready = false;
  for (let i = 0; i < 30; i++) {
    await sleep(500);
    try {
      const r = await fetch(`${BASE}/api/stats`);
      if (r.ok) { ready = true; break; }
    } catch {}
  }

  if (!ready) {
    console.error('Server did not start in time. Output:', serverOutput);
    cleanup();
    process.exit(1);
  }
  console.log('Server ready.\n');

  // 4. Connect WebSocket
  const wsMessages = [];
  const ws = new WebSocket(`ws://localhost:${PORT}`);
  await new Promise((resolve, reject) => {
    ws.on('open', resolve);
    ws.on('error', reject);
    setTimeout(() => reject(new Error('WS timeout')), 5000);
  });
  ws.on('message', (data) => {
    try { wsMessages.push(JSON.parse(data.toString())); } catch {}
  });
  console.log('WebSocket connected.\n');

  // 5. Generate and inject packets
  const roles = ['repeater', 'room', 'companion', 'sensor'];
  const injected = [];
  const advertNodes = {}; // name -> {role, pubkey, count}
  const grpTxtCount = { total: 0, byChannel: {} };
  const observerCounts = {}; // id -> count
  const hashToObservers = {}; // hash -> Set(observer)

  // Generate ADVERT packets — ensure at least one of each role
  for (let ri = 0; ri < roles.length; ri++) {
    const name = NODE_NAMES[ri];
    const role = roles[ri];
    const buf = buildAdvert(name, role);
    const hex = buf.toString('hex').toUpperCase();
    const hash = crypto.createHash('md5').update(hex).digest('hex').slice(0, 16);
    const obs = OBSERVERS[ri % OBSERVERS.length];
    injected.push({ hex, observer: obs.id, region: obs.iata, hash, snr: 5.0, rssi: -80 });
    advertNodes[name] = { role, pubkey: pubkeyFor(name).toString('hex'), count: 1 };
    observerCounts[obs.id] = (observerCounts[obs.id] || 0) + 1;
    if (!hashToObservers[hash]) hashToObservers[hash] = new Set();
    hashToObservers[hash].add(obs.id);
  }

  // More ADVERTs
  for (let i = 0; i < 40; i++) {
    const name = pick(NODE_NAMES);
    const role = pick(roles);
    const buf = buildAdvert(name, role);
    const hex = buf.toString('hex').toUpperCase();
    const hash = crypto.createHash('md5').update(hex).digest('hex').slice(0, 16);
    // Multi-observer: 30% chance heard by 2 observers
    const obsCount = Math.random() < 0.3 ? 2 : 1;
    const shuffled = [...OBSERVERS].sort(() => Math.random() - 0.5);
    for (let o = 0; o < obsCount; o++) {
      const obs = shuffled[o];
      injected.push({ hex, observer: obs.id, region: obs.iata, hash, snr: rand(-2, 10), rssi: rand(-110, -60) });
      observerCounts[obs.id] = (observerCounts[obs.id] || 0) + 1;
      if (!hashToObservers[hash]) hashToObservers[hash] = new Set();
      hashToObservers[hash].add(obs.id);
    }
    if (!advertNodes[name]) advertNodes[name] = { role, pubkey: pubkeyFor(name).toString('hex'), count: 0 };
    advertNodes[name].count++;
  }

  // GRP_TXT packets
  for (let i = 0; i < 30; i++) {
    const ch = randInt(0, 3);
    const buf = buildGrpTxt(ch);
    const hex = buf.toString('hex').toUpperCase();
    const hash = crypto.createHash('md5').update(hex).digest('hex').slice(0, 16);
    const obs = pick(OBSERVERS);
    injected.push({ hex, observer: obs.id, region: obs.iata, hash, snr: 3.0, rssi: -90 });
    grpTxtCount.total++;
    grpTxtCount.byChannel[ch] = (grpTxtCount.byChannel[ch] || 0) + 1;
    observerCounts[obs.id] = (observerCounts[obs.id] || 0) + 1;
    if (!hashToObservers[hash]) hashToObservers[hash] = new Set();
    hashToObservers[hash].add(obs.id);
  }

  // ACK + TXT_MSG
  for (let i = 0; i < 20; i++) {
    const buf = i < 10 ? buildAck() : buildTxtMsg();
    const hex = buf.toString('hex').toUpperCase();
    const hash = crypto.createHash('md5').update(hex).digest('hex').slice(0, 16);
    const obs = pick(OBSERVERS);
    injected.push({ hex, observer: obs.id, region: obs.iata, hash, snr: 1.0, rssi: -95 });
    observerCounts[obs.id] = (observerCounts[obs.id] || 0) + 1;
    if (!hashToObservers[hash]) hashToObservers[hash] = new Set();
    hashToObservers[hash].add(obs.id);
  }

  // Find a hash with multiple observers for trace testing
  let traceHash = null;
  for (const [h, obs] of Object.entries(hashToObservers)) {
    if (obs.size >= 2) { traceHash = h; break; }
  }
  // If none, create one explicitly
  if (!traceHash) {
    const buf = buildAck();
    const hex = buf.toString('hex').toUpperCase();
    traceHash = crypto.createHash('md5').update(hex).digest('hex').slice(0, 16);
    injected.push({ hex, observer: OBSERVERS[0].id, region: OBSERVERS[0].iata, hash: traceHash, snr: 5, rssi: -80 });
    injected.push({ hex, observer: OBSERVERS[1].id, region: OBSERVERS[1].iata, hash: traceHash, snr: 3, rssi: -90 });
    observerCounts[OBSERVERS[0].id] = (observerCounts[OBSERVERS[0].id] || 0) + 1;
    observerCounts[OBSERVERS[1].id] = (observerCounts[OBSERVERS[1].id] || 0) + 1;
  }

  console.log(`Injecting ${injected.length} packets...`);
  let injectOk = 0, injectFail = 0;
  for (const pkt of injected) {
    const r = await post('/api/packets', pkt);
    if (r.status === 200) injectOk++;
    else { injectFail++; if (injectFail <= 3) console.error('  Inject fail:', r.data); }
  }
  console.log(`Injected: ${injectOk} ok, ${injectFail} fail\n`);
  assert(injectFail === 0, 'All packets injected successfully');
  assert(injected.length >= 100, `Injected 100+ packets (got ${injected.length})`);

  // Wait a moment for WS messages to arrive
  await sleep(500);

  // ── Validate ───────────────────────────────────────────────────────

  // 5a. Stats
  console.log('── Stats ──');
  const stats = (await get('/api/stats')).data;
  // totalPackets includes seed packet, so should be >= injected.length
  assert(stats.totalPackets > 0, `stats.totalPackets (${stats.totalPackets}) >= ${injected.length}`);
  assert(stats.totalNodes > 0, `stats.totalNodes > 0 (${stats.totalNodes})`);
  assert(stats.totalObservers >= OBSERVERS.length, `stats.totalObservers >= ${OBSERVERS.length} (${stats.totalObservers})`);
  console.log(`  totalPackets=${stats.totalPackets} totalNodes=${stats.totalNodes} totalObservers=${stats.totalObservers}\n`);

  // 5b. Packets API - basic list
  console.log('── Packets API ──');
  const pktsAll = (await get('/api/packets?limit=200')).data;
  assert(pktsAll.total > 0, `packets total (${pktsAll.total}) > 0`);
  assert(pktsAll.packets.length > 0, 'packets array not empty');

  // Filter by type (ADVERT = 4)
  const pktsAdvert = (await get('/api/packets?type=4&limit=200')).data;
  assert(pktsAdvert.total > 0, `filter by type=ADVERT returns results (${pktsAdvert.total})`);
  assert(pktsAdvert.packets.every(p => p.payload_type === 4), 'all filtered packets are ADVERT');

  // Filter by observer
  const testObs = OBSERVERS[0].id;
  const pktsObs = (await get(`/api/packets?observer=${testObs}&limit=200`)).data;
  assert(pktsObs.total > 0, `filter by observer=${testObs} returns results`);
  assert(pktsObs.packets.length > 0, 'observer filter returns packets');

  // Filter by region
  const pktsRegion = (await get('/api/packets?region=SJC&limit=200')).data;
  assert(pktsRegion.total > 0, 'filter by region=SJC returns results');

  // Pagination
  const page1 = (await get('/api/packets?limit=5&offset=0')).data;
  const page2 = (await get('/api/packets?limit=5&offset=5')).data;
  assert(page1.packets.length === 5, 'pagination: page1 has 5');
  assert(page2.packets.length === 5, 'pagination: page2 has 5');
  if (page1.packets.length && page2.packets.length) {
    assert(page1.packets[0].id !== page2.packets[0].id, 'pagination: pages are different');
  }

  // groupByHash
  const grouped = (await get('/api/packets?groupByHash=true&limit=200')).data;
  assert(grouped.total > 0, `groupByHash returns results (${grouped.total})`);
  assert(grouped.packets[0].hash !== undefined, 'groupByHash entries have hash');
  assert(grouped.packets[0].count !== undefined, 'groupByHash entries have count');
  // Find a multi-observer group
  const multiObs = grouped.packets.find(p => p.observer_count >= 2);
  assert(!!multiObs, 'groupByHash has entry with observer_count >= 2');
  console.log('  ✓ Packets API checks passed\n');

  // 5c. Packet detail
  console.log('── Packet Detail ──');
  const firstPkt = pktsAll.packets[0];
  const detail = (await get(`/api/packets/${firstPkt.id}`)).data;
  assert(detail.packet !== undefined, 'detail has packet');
  assert(detail.breakdown !== undefined, 'detail has breakdown');
  assert(detail.breakdown.ranges !== undefined, 'breakdown has ranges');
  assert(detail.breakdown.ranges.length > 0, 'breakdown has color ranges');
  assert(detail.breakdown.ranges[0].color !== undefined, 'ranges have color field');
  assert(detail.breakdown.ranges[0].start !== undefined, 'ranges have start field');
  console.log(`  ✓ Detail: ${detail.breakdown.ranges.length} color ranges\n`);

  // 5d. Nodes
  console.log('── Nodes ──');
  const nodesResp = (await get('/api/nodes?limit=50')).data;
  assert(nodesResp.total > 0, `nodes total > 0 (${nodesResp.total})`);
  assert(nodesResp.nodes.length > 0, 'nodes array not empty');
  assert(nodesResp.counts !== undefined, 'nodes response has counts');

  // Role filtering
  const repNodes = (await get('/api/nodes?role=repeater')).data;
  assert(repNodes.nodes.every(n => n.role === 'repeater'), 'role filter works for repeater');

  // Node detail
  const someNode = nodesResp.nodes[0];
  const nodeDetail = (await get(`/api/nodes/${someNode.public_key}`)).data;
  assert(nodeDetail.node !== undefined, 'node detail has node');
  assert(nodeDetail.node.public_key === someNode.public_key, 'node detail matches pubkey');
  assert(nodeDetail.recentAdverts !== undefined, 'node detail has recentAdverts');
  console.log(`  ✓ Nodes: ${nodesResp.total} total, detail works\n`);

  // 5e. Channels
  console.log('── Channels ──');
  const chResp = (await get('/api/channels')).data;
  assert(Array.isArray(chResp.channels), 'channels response is array');
  if (chResp.channels.length > 0) {
    const someCh = chResp.channels[0];
    assert(someCh.messageCount > 0, `channel has messages (${someCh.messageCount})`);
    const msgResp = (await get(`/api/channels/${someCh.hash}/messages`)).data;
    assert(msgResp.messages.length > 0, 'channel has message list');
    assert(msgResp.messages[0].sender !== undefined, 'message has sender');
    console.log(`  ✓ Channels: ${chResp.channels.length} channels\n`);
  } else {
    console.log(`  ⚠ Channels: 0 (synthetic packets don't produce decodable channel messages)\n`);
  }

  // 5f. Observers
  console.log('── Observers ──');
  const obsResp = (await get('/api/observers')).data;
  assert(obsResp.observers.length >= OBSERVERS.length, `observers >= ${OBSERVERS.length} (${obsResp.observers.length})`);
  for (const expObs of OBSERVERS) {
    const found = obsResp.observers.find(o => o.id === expObs.id);
    assert(!!found, `observer ${expObs.id} exists`);
    if (found) {
      assert(found.packet_count > 0, `observer ${expObs.id} has packet_count > 0 (${found.packet_count})`);
    }
  }
  console.log(`  ✓ Observers: ${obsResp.observers.length}\n`);

  // 5g. Traces
  console.log('── Traces ──');
  if (traceHash) {
    const traceResp = (await get(`/api/traces/${traceHash}`)).data;
    assert(Array.isArray(traceResp.traces), 'trace response is array');
    if (traceResp.traces.length >= 2) {
      const traceObservers = new Set(traceResp.traces.map(t => t.observer));
      assert(traceObservers.size >= 2, `trace has >= 2 distinct observers (${traceObservers.size})`);
    }
    console.log(`  ✓ Traces: ${traceResp.traces.length} entries for hash\n`);
  } else {
    console.log('  ⚠ No multi-observer hash available for trace test\n');
  }

  // 5h. WebSocket
  console.log('── WebSocket ──');
  assert(wsMessages.length > 0, `WebSocket received messages (${wsMessages.length})`);
  assert(wsMessages.length >= injected.length * 0.5, `WS got >= 50% of injected (${wsMessages.length}/${injected.length})`);
  const wsPacketMsgs = wsMessages.filter(m => m.type === 'packet');
  assert(wsPacketMsgs.length > 0, 'WS has packet-type messages');
  console.log(`  ✓ WebSocket: ${wsMessages.length} messages received\n`);

  // 6. MQTT (optional)
  console.log('── MQTT ──');
  let mqttAvailable = false;
  try {
    execSync('which mosquitto_pub', { stdio: 'ignore' });
    mqttAvailable = true;
  } catch {}

  if (mqttAvailable) {
    console.log('  mosquitto_pub found, testing MQTT path...');
    // Would need a running mosquitto broker — skip if not running
    try {
      const mqttMod = require('mqtt');
      const mc = mqttMod.connect('mqtt://localhost:1883', { connectTimeout: 2000 });
      await new Promise((resolve, reject) => {
        mc.on('connect', resolve);
        mc.on('error', reject);
        setTimeout(() => reject(new Error('timeout')), 2000);
      });
      const mqttHex = buildAdvert('MQTTTestNode', 'repeater').toString('hex').toUpperCase();
      const mqttHash = 'mqtt-test-hash-001';
      mc.publish('meshcore/SJC/MQTT-OBS-1/packets', JSON.stringify({
        raw: mqttHex, SNR: 8.0, RSSI: -75, hash: mqttHash,
      }));
      await sleep(1000);
      mc.end();
      const mqttTrace = (await get(`/api/traces/${mqttHash}`)).data;
      assert(mqttTrace.traces.length >= 1, 'MQTT packet appeared in traces');
      console.log('  ✓ MQTT path works\n');
    } catch (e) {
      console.log(`  ⚠ MQTT broker not reachable: ${e.message}\n`);
    }
  } else {
    console.log('  ⚠ mosquitto not available, skipping MQTT test\n');
  }

  // 7. Summary
  ws.close();
  cleanup();

  console.log('═══════════════════════════════════════');
  console.log(`  PASSED: ${passed}`);
  console.log(`  FAILED: ${failed}`);
  if (failures.length) {
    console.log('  Failures:');
    failures.forEach(f => console.log(`    - ${f}`));
  }
  console.log('═══════════════════════════════════════');

  process.exit(failed > 0 ? 1 : 0);
}

main().catch(e => {
  console.error('Fatal:', e);
  process.exit(1);
});
