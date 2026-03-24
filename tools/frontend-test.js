#!/usr/bin/env node
'use strict';

/**
 * MeshCore Analyzer — Frontend Smoke Tests (M13)
 *
 * Starts the server with a temp DB, injects synthetic packets,
 * then validates HTML pages, JS syntax, and API data shapes.
 */

const { spawn } = require('child_process');
const path = require('path');
const fs = require('fs');
const os = require('os');
const crypto = require('crypto');

const PROJECT_DIR = path.join(__dirname, '..');
const PORT = 13580;
const BASE = `http://localhost:${PORT}`;

// ── Helpers ──────────────────────────────────────────────────────────

let passed = 0, failed = 0;
const failures = [];

function assert(cond, label) {
  if (cond) { passed++; }
  else { failed++; failures.push(label); console.error(`  ❌ FAIL: ${label}`); }
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function get(urlPath) {
  const r = await fetch(`${BASE}${urlPath}`);
  return { status: r.status, data: await r.json() };
}

async function getHtml(urlPath) {
  const r = await fetch(`${BASE}${urlPath}`);
  return { status: r.status, text: await r.text() };
}

async function post(urlPath, body) {
  const r = await fetch(`${BASE}${urlPath}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return { status: r.status, data: await r.json() };
}

// ── Packet builders (from e2e-test.js) ───────────────────────────────

function rand(a, b) { return Math.random() * (b - a) + a; }
function randInt(a, b) { return Math.floor(rand(a, b + 1)); }
function pick(a) { return a[randInt(0, a.length - 1)]; }

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
  const sig = crypto.randomBytes(64);
  let flags = 0x80 | 0x10;
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
  const header = encodeHeader(1, 0x04, 0);
  const { pathByte, hops } = buildPath(randInt(0, 3));
  return Buffer.concat([Buffer.from([header, pathByte]), hops, payload]);
}

function buildGrpTxt(channelHash = 0) {
  const mac = crypto.randomBytes(2);
  const enc = crypto.randomBytes(randInt(10, 40));
  const payload = Buffer.concat([Buffer.from([channelHash]), mac, enc]);
  const header = encodeHeader(1, 0x05, 0);
  const { pathByte, hops } = buildPath(randInt(0, 3));
  return Buffer.concat([Buffer.from([header, pathByte]), hops, payload]);
}

function buildAck() {
  const payload = crypto.randomBytes(18);
  const header = encodeHeader(2, 0x03, 0);
  const { pathByte, hops } = buildPath(randInt(0, 2));
  return Buffer.concat([Buffer.from([header, pathByte]), hops, payload]);
}

// ── Main ─────────────────────────────────────────────────────────────

const OBSERVERS = [
  { id: 'FE-SJC-1', iata: 'SJC' },
  { id: 'FE-SFO-2', iata: 'SFO' },
];

const NODE_NAMES = ['FENode Alpha', 'FENode Beta', 'FENode Gamma', 'FENode Delta'];

async function main() {
  // 1. Temp DB
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'meshcore-fe-'));
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

  const cleanup = () => {
    try { srv.kill('SIGTERM'); } catch {}
    try { fs.unlinkSync(dbPath); fs.rmdirSync(tmpDir); } catch {}
  };

  process.on('SIGINT', () => { cleanup(); process.exit(1); });

  // 3. Wait for ready
  let ready = false;
  for (let i = 0; i < 30; i++) {
    await sleep(500);
    try {
      const r = await fetch(`${BASE}/api/stats`);
      if (r.ok) { ready = true; break; }
    } catch {}
  }
  if (!ready) {
    console.error('Server did not start. Output:', serverOutput);
    cleanup();
    process.exit(1);
  }
  console.log('Server ready.\n');

  // 4. Inject test data
  const injected = [];
  const roles = ['repeater', 'room', 'companion', 'sensor'];
  for (let i = 0; i < NODE_NAMES.length; i++) {
    const buf = buildAdvert(NODE_NAMES[i], roles[i]);
    const hex = buf.toString('hex').toUpperCase();
    const hash = crypto.createHash('md5').update(hex).digest('hex').slice(0, 16);
    const obs = OBSERVERS[i % OBSERVERS.length];
    injected.push({ hex, observer: obs.id, region: obs.iata, hash, snr: 5.0, rssi: -80 });
  }
  for (let i = 0; i < 20; i++) {
    const buf = buildGrpTxt(i % 3);
    const hex = buf.toString('hex').toUpperCase();
    const hash = crypto.createHash('md5').update(hex).digest('hex').slice(0, 16);
    const obs = pick(OBSERVERS);
    injected.push({ hex, observer: obs.id, region: obs.iata, hash, snr: 3.0, rssi: -90 });
  }
  for (let i = 0; i < 10; i++) {
    const buf = buildAck();
    const hex = buf.toString('hex').toUpperCase();
    const hash = crypto.createHash('md5').update(hex).digest('hex').slice(0, 16);
    const obs = pick(OBSERVERS);
    injected.push({ hex, observer: obs.id, region: obs.iata, hash, snr: 1.0, rssi: -95 });
  }

  console.log(`Injecting ${injected.length} packets...`);
  let injectFail = 0;
  for (const pkt of injected) {
    const r = await post('/api/packets', pkt);
    if (r.status !== 200) injectFail++;
  }
  assert(injectFail === 0, `All ${injected.length} packets injected`);
  console.log(`Injected: ${injected.length - injectFail} ok, ${injectFail} fail\n`);

  // ── HTML & Nav Tests ───────────────────────────────────────────────
  console.log('── HTML & Navigation ──');
  const { status: htmlStatus, text: html } = await getHtml('/');
  assert(htmlStatus === 200, 'index.html returns 200');
  assert(html.includes('<nav'), 'index.html contains <nav>');

  const expectedLinks = ['#/packets', '#/map', '#/channels', '#/nodes', '#/traces', '#/observers'];
  for (const link of expectedLinks) {
    assert(html.includes(`href="${link}"`), `nav contains link to ${link}`);
  }

  // ── JS File References ─────────────────────────────────────────────
  console.log('\n── JS File References ──');
  const jsFiles = ['app.js', 'packets.js', 'map.js', 'channels.js', 'nodes.js', 'traces.js', 'observers.js'];
  for (const jsFile of jsFiles) {
    assert(html.includes(`src="${jsFile}`) || html.includes(`src="${jsFile}?`), `index.html references ${jsFile}`);
  }

  // ── JS Syntax Validation ───────────────────────────────────────────
  console.log('\n── JS Syntax Validation ──');
  for (const jsFile of jsFiles) {
    const jsPath = path.join(PROJECT_DIR, 'public', jsFile);
    try {
      const source = fs.readFileSync(jsPath, 'utf8');
      // Use the vm module's Script to check for syntax errors
      new (require('vm')).Script(source, { filename: jsFile });
      assert(true, `${jsFile} has valid syntax`);
    } catch (e) {
      assert(false, `${jsFile} syntax error: ${e.message}`);
    }
  }

  // ── JS Files Fetchable from Server ─────────────────────────────────
  console.log('\n── JS Files Served ──');
  for (const jsFile of jsFiles) {
    const resp = await getHtml(`/${jsFile}`);
    assert(resp.status === 200, `${jsFile} served with 200`);
    assert(resp.text.length > 0, `${jsFile} is non-empty`);
  }

  // ── API Data Shape Validation ──────────────────────────────────────
  console.log('\n── API: /api/stats ──');
  const stats = (await get('/api/stats')).data;
  assert(typeof stats.totalPackets === 'number', 'stats.totalPackets is number');
  assert(typeof stats.totalNodes === 'number', 'stats.totalNodes is number');
  assert(typeof stats.totalObservers === 'number', 'stats.totalObservers is number');
  assert(stats.totalPackets > 0, `stats.totalPackets > 0 (${stats.totalPackets})`);

  console.log('\n── API: /api/packets (packets page) ──');
  const pkts = (await get('/api/packets?limit=10')).data;
  assert(typeof pkts.total === 'number', 'packets response has total');
  assert(Array.isArray(pkts.packets), 'packets response has packets array');
  assert(pkts.packets.length > 0, 'packets array non-empty');
  const pkt0 = pkts.packets[0];
  assert(pkt0.id !== undefined, 'packet has id');
  assert(pkt0.raw_hex !== undefined, 'packet has raw_hex');
  assert(pkt0.payload_type !== undefined, 'packet has payload_type');
  assert(pkt0.observer_id !== undefined, 'packet has observer_id');

  // Packet detail (byte breakdown)
  const detail = (await get(`/api/packets/${pkt0.id}`)).data;
  assert(detail.packet !== undefined, 'packet detail has packet');
  assert(detail.breakdown !== undefined, 'packet detail has breakdown');
  assert(Array.isArray(detail.breakdown.ranges), 'breakdown has ranges array');

  console.log('\n── API: /api/packets?groupByHash (map page) ──');
  const grouped = (await get('/api/packets?groupByHash=true&limit=10')).data;
  assert(typeof grouped.total === 'number', 'groupByHash has total');
  assert(Array.isArray(grouped.packets), 'groupByHash has packets array');

  console.log('\n── API: /api/channels (channels page) ──');
  const ch = (await get('/api/channels')).data;
  assert(Array.isArray(ch.channels), 'channels response has channels array');
  if (ch.channels.length > 0) {
    assert(ch.channels[0].hash !== undefined, 'channel has hash');
    assert(ch.channels[0].messageCount !== undefined, 'channel has messageCount');
    const chMsgs = (await get(`/api/channels/${ch.channels[0].hash}/messages`)).data;
    assert(Array.isArray(chMsgs.messages), 'channel messages is array');
  } else {
    console.log('  ⚠ No channels (synthetic packets are not decodable channel messages)');
  }

  console.log('\n── API: /api/nodes (nodes page) ──');
  const nodes = (await get('/api/nodes?limit=10')).data;
  assert(typeof nodes.total === 'number', 'nodes has total');
  assert(Array.isArray(nodes.nodes), 'nodes has nodes array');
  assert(nodes.nodes.length > 0, 'nodes non-empty');
  const n0 = nodes.nodes[0];
  assert(n0.public_key !== undefined, 'node has public_key');
  assert(n0.name !== undefined, 'node has name');

  // Node detail
  const nd = (await get(`/api/nodes/${n0.public_key}`)).data;
  assert(nd.node !== undefined, 'node detail has node');
  assert(nd.recentAdverts !== undefined, 'node detail has recentAdverts');

  console.log('\n── API: /api/observers (observers page) ──');
  const obs = (await get('/api/observers')).data;
  assert(Array.isArray(obs.observers), 'observers is array');
  assert(obs.observers.length > 0, 'observers non-empty');
  assert(obs.observers[0].id !== undefined, 'observer has id');
  assert(obs.observers[0].packet_count !== undefined, 'observer has packet_count');

  console.log('\n── API: /api/traces (traces page) ──');
  // Use a known hash from injected packets
  const knownHash = crypto.createHash('md5').update(injected[0].hex).digest('hex').slice(0, 16);
  const traces = (await get(`/api/traces/${knownHash}`)).data;
  assert(Array.isArray(traces.traces), 'traces is array');

  // ── Summary ────────────────────────────────────────────────────────
  cleanup();

  console.log('\n═══════════════════════════════════════');
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
