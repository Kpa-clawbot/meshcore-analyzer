#!/usr/bin/env node
'use strict';

/**
 * Benchmark suite for meshcore-analyzer.
 * Launches two server instances — one with in-memory store, one with pure SQLite —
 * and compares performance side by side.
 *
 * Usage: node benchmark.js [--runs 5] [--json]
 */

const http = require('http');
const { spawn } = require('child_process');
const path = require('path');

const args = process.argv.slice(2);
const RUNS = Number(args.find((a, i) => args[i - 1] === '--runs') || 5);
const JSON_OUT = args.includes('--json');

const PORT_MEM = 13001;  // In-memory store
const PORT_SQL = 13002;  // SQLite-only

const ENDPOINTS = [
  { name: 'Stats', path: '/api/stats' },
  { name: 'Packets (50)', path: '/api/packets?limit=50' },
  { name: 'Packets (100)', path: '/api/packets?limit=100' },
  { name: 'Packets grouped', path: '/api/packets?limit=100&groupByHash=true' },
  { name: 'Packets filtered', path: '/api/packets?limit=50&type=5' },
  { name: 'Packets timestamps', path: '/api/packets/timestamps?since=2020-01-01' },
  { name: 'Nodes list', path: '/api/nodes?limit=50' },
  { name: 'Node detail', path: '/api/nodes/__FIRST_NODE__' },
  { name: 'Node health', path: '/api/nodes/__FIRST_NODE__/health' },
  { name: 'Bulk health', path: '/api/nodes/bulk-health?limit=50' },
  { name: 'Network status', path: '/api/nodes/network-status' },
  { name: 'Observers', path: '/api/observers' },
  { name: 'Channels', path: '/api/channels' },
  { name: 'RF Analytics', path: '/api/analytics/rf' },
  { name: 'Topology', path: '/api/analytics/topology' },
  { name: 'Channel Analytics', path: '/api/analytics/channels' },
  { name: 'Hash Sizes', path: '/api/analytics/hash-sizes' },
  { name: 'Subpaths 2-hop', path: '/api/analytics/subpaths?minLen=2&maxLen=2&limit=50' },
  { name: 'Subpaths 3-hop', path: '/api/analytics/subpaths?minLen=3&maxLen=3&limit=30' },
  { name: 'Subpaths 4-hop', path: '/api/analytics/subpaths?minLen=4&maxLen=4&limit=20' },
  { name: 'Subpaths 5-8 hop', path: '/api/analytics/subpaths?minLen=5&maxLen=8&limit=15' },
];

function fetch(url) {
  return new Promise((resolve, reject) => {
    const t0 = process.hrtime.bigint();
    const req = http.get(url, (res) => {
      let body = '';
      res.on('data', c => body += c);
      res.on('end', () => {
        const ms = Number(process.hrtime.bigint() - t0) / 1e6;
        resolve({ ms, bytes: Buffer.byteLength(body), status: res.statusCode, body });
      });
    });
    req.on('error', reject);
    req.setTimeout(60000, () => { req.destroy(); reject(new Error('timeout')); });
  });
}

function median(arr) { const s = [...arr].sort((a,b)=>a-b); return s[Math.floor(s.length/2)]; }
function p95(arr) { const s = [...arr].sort((a,b)=>a-b); return s[Math.floor(s.length*0.95)]; }
function avg(arr) { return arr.reduce((a,b)=>a+b,0)/arr.length; }
function fmt(ms) { return ms >= 1000 ? (ms/1000).toFixed(1)+'s' : ms.toFixed(1)+'ms'; }
function fmtSize(b) { return b >= 1048576 ? (b/1048576).toFixed(1)+'MB' : b >= 1024 ? (b/1024).toFixed(0)+'KB' : b+'B'; }

function launchServer(port, env = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn('node', ['server.js'], {
      cwd: __dirname,
      env: { ...process.env, PORT: String(port), ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let started = false;
    const timeout = setTimeout(() => { if (!started) { child.kill(); reject(new Error('Server start timeout')); } }, 30000);

    child.stdout.on('data', (d) => {
      if (!started && (d.toString().includes('listening') || d.toString().includes('running'))) {
        started = true; clearTimeout(timeout); resolve(child);
      }
    });
    child.stderr.on('data', (d) => {
      if (!started && (d.toString().includes('listening') || d.toString().includes('running'))) {
        started = true; clearTimeout(timeout); resolve(child);
      }
    });
    child.on('exit', (code) => { if (!started) { clearTimeout(timeout); reject(new Error(`Server exited with ${code}`)); } });

    // Fallback: wait longer (SQLite-only mode pre-warms subpaths ~6s)
    setTimeout(() => {
      if (!started) {
        started = true; clearTimeout(timeout);
        resolve(child);
      }
    }, 15000);
  });
}

async function waitForServer(port, maxMs = 20000) {
  const t0 = Date.now();
  while (Date.now() - t0 < maxMs) {
    try {
      const r = await fetch(`http://127.0.0.1:${port}/api/stats`);
      if (r.status === 200) return true;
    } catch {}
    await new Promise(r => setTimeout(r, 500));
  }
  throw new Error(`Server on port ${port} didn't start`);
}

async function benchmarkEndpoints(port, endpoints, nocache = false) {
  const results = [];
  for (const ep of endpoints) {
    const suffix = nocache ? (ep.path.includes('?') ? '&nocache=1' : '?nocache=1') : '';
    const url = `http://127.0.0.1:${port}${ep.path}${suffix}`;

    // Warm-up
    try { await fetch(url); } catch {}

    const times = [];
    let bytes = 0;
    let failed = false;

    for (let i = 0; i < RUNS; i++) {
      try {
        const r = await fetch(url);
        if (r.status !== 200) { failed = true; break; }
        times.push(r.ms);
        bytes = r.bytes;
      } catch { failed = true; break; }
    }

    if (failed || !times.length) {
      results.push({ name: ep.name, failed: true });
    } else {
      results.push({
        name: ep.name,
        avg: Math.round(avg(times) * 10) / 10,
        p50: Math.round(median(times) * 10) / 10,
        p95: Math.round(p95(times) * 10) / 10,
        bytes
      });
    }
  }
  return results;
}

async function run() {
  console.log(`\nMeshCore Analyzer Benchmark — ${RUNS} runs per endpoint`);
  console.log('Launching servers...\n');

  // Launch both servers
  let memServer, sqlServer;
  try {
    console.log('  Starting in-memory server (port ' + PORT_MEM + ')...');
    memServer = await launchServer(PORT_MEM, {});
    await waitForServer(PORT_MEM);
    console.log('  ✅ In-memory server ready');

    console.log('  Starting SQLite-only server (port ' + PORT_SQL + ')...');
    sqlServer = await launchServer(PORT_SQL, { NO_MEMORY_STORE: '1' });
    await waitForServer(PORT_SQL);
    console.log('  ✅ SQLite-only server ready\n');
  } catch (e) {
    console.error('Failed to start servers:', e.message);
    if (memServer) memServer.kill();
    if (sqlServer) sqlServer.kill();
    process.exit(1);
  }

  // Get first node pubkey
  let firstNode = '';
  try {
    const r = await fetch(`http://127.0.0.1:${PORT_MEM}/api/nodes?limit=1`);
    const data = JSON.parse(r.body);
    firstNode = data.nodes?.[0]?.public_key || '';
  } catch {}

  const endpoints = ENDPOINTS.map(e => ({
    ...e,
    path: e.path.replace('__FIRST_NODE__', firstNode),
  }));

  // Get packet count
  try {
    const r = await fetch(`http://127.0.0.1:${PORT_MEM}/api/stats`);
    const stats = JSON.parse(r.body);
    console.log(`Dataset: ${(stats.totalPackets || '?').toLocaleString()} packets\n`);
  } catch {}

  // Run benchmarks
  console.log('Benchmarking in-memory store (nocache for true compute cost)...');
  const memResults = await benchmarkEndpoints(PORT_MEM, endpoints, true);

  console.log('Benchmarking SQLite-only (nocache)...');
  const sqlResults = await benchmarkEndpoints(PORT_SQL, endpoints, true);

  // Also test cached in-memory for the full picture
  console.log('Benchmarking in-memory store (cached)...');
  const memCachedResults = await benchmarkEndpoints(PORT_MEM, endpoints, false);

  // Kill servers
  memServer.kill();
  sqlServer.kill();

  if (JSON_OUT) {
    console.log(JSON.stringify({ memoryNocache: memResults, sqliteNocache: sqlResults, memoryCached: memCachedResults }, null, 2));
    return;
  }

  // Print results
  const W = 94;
  console.log(`\n${'═'.repeat(W)}`);
  console.log('  🏁 BENCHMARK RESULTS: SQLite vs In-Memory Store');
  console.log(`${'═'.repeat(W)}`);
  console.log(`${'Endpoint'.padEnd(24)} ${'SQLite'.padStart(9)} ${'Memory'.padStart(9)} ${'Cached'.padStart(9)} ${'Speedup'.padStart(9)} ${'Size (SQL)'.padStart(10)} ${'Size (Mem)'.padStart(10)}`);
  console.log(`${'─'.repeat(24)} ${'─'.repeat(9)} ${'─'.repeat(9)} ${'─'.repeat(9)} ${'─'.repeat(9)} ${'─'.repeat(10)} ${'─'.repeat(10)}`);

  for (let i = 0; i < endpoints.length; i++) {
    const sql = sqlResults[i];
    const mem = memResults[i];
    const cached = memCachedResults[i];
    if (!sql || sql.failed || !mem || mem.failed) {
      console.log(`${endpoints[i].name.padEnd(24)} ${'FAILED'.padStart(9)}`);
      continue;
    }

    const speedup = sql.avg > 0 && mem.avg > 0 ? Math.round(sql.avg / mem.avg) + '×' : '—';
    const cachedStr = cached && !cached.failed ? fmt(cached.avg) : '—';

    console.log(
      `${sql.name.padEnd(24)} ${fmt(sql.avg).padStart(9)} ${fmt(mem.avg).padStart(9)} ${cachedStr.padStart(9)} ${speedup.padStart(9)} ${fmtSize(sql.bytes).padStart(10)} ${fmtSize(mem.bytes).padStart(10)}`
    );
  }

  // Summary
  const sqlTotal = sqlResults.filter(r => !r.failed).reduce((s, r) => s + r.avg, 0);
  const memTotal = memResults.filter(r => !r.failed).reduce((s, r) => s + r.avg, 0);
  console.log(`${'─'.repeat(24)} ${'─'.repeat(9)} ${'─'.repeat(9)} ${'─'.repeat(9)} ${'─'.repeat(9)}`);
  console.log(`${'TOTAL'.padEnd(24)} ${fmt(sqlTotal).padStart(9)} ${fmt(memTotal).padStart(9)} ${''.padStart(9)} ${(Math.round(sqlTotal/memTotal)+'×').padStart(9)}`);
  console.log(`\n${'═'.repeat(W)}\n`);
}

run().catch(e => { console.error(e); process.exit(1); });
