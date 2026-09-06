/* Tests for perf.js Disk I/O + Write Sources + SQLite sections (#1120) */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

function makeSandbox() {
  let capturedHtml = '';
  const pages = {};
  const ctx = {
    window: { addEventListener: () => {}, apiPerf: null },
    document: {
      getElementById: (id) => {
        if (id === 'perfContent') return { set innerHTML(v) { capturedHtml = v; } };
        return null;
      },
      addEventListener: () => {},
    },
    console,
    Date, Math, Array, Object, String, Number, JSON, RegExp, Error, TypeError,
    parseInt, parseFloat, isNaN, isFinite,
    setTimeout: () => {}, clearTimeout: () => {},
    setInterval: () => 0, clearInterval: () => {},
    performance: { now: () => Date.now() },
    Map, Set, Promise,
    registerPage: (name, handler) => { pages[name] = handler; },
    _apiCache: null,
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  };
  ctx.window.document = ctx.document;
  ctx.globalThis = ctx;
  return { ctx, pages, getHtml: () => capturedHtml };
}

function loadPerf() {
  const sb = makeSandbox();
  const code = fs.readFileSync('public/perf.js', 'utf8');
  vm.runInNewContext(code, sb.ctx);
  return sb;
}

function stubFetch(sb, perfData, healthData, ioData, sqliteData, sourcesData) {
  sb.ctx.fetch = (url) => {
    if (url === '/api/perf') return Promise.resolve({ json: () => Promise.resolve(perfData) });
    if (url === '/api/health') return Promise.resolve({ json: () => Promise.resolve(healthData) });
    if (url === '/api/perf/io') return Promise.resolve({ json: () => Promise.resolve(ioData) });
    if (url === '/api/perf/sqlite') return Promise.resolve({ json: () => Promise.resolve(sqliteData) });
    if (url === '/api/perf/write-sources') return Promise.resolve({ json: () => Promise.resolve(sourcesData) });
    return Promise.resolve({ json: () => Promise.resolve({}) });
  };
}

const basePerf = {
  totalRequests: 100, avgMs: 5, uptime: 3600,
  slowQueries: [], endpoints: {}, cache: null, packetStore: null, sqlite: null
};
const goRuntime = {
  goroutines: 17, numGC: 31, pauseTotalMs: 2.1, lastPauseMs: 0.03,
  heapAllocMB: 473, heapSysMB: 1035, heapInuseMB: 663, heapIdleMB: 371, numCPU: 2
};
const goHealth = { engine: 'go', uptimeHuman: '2h', websocket: { clients: 5 } };

const ioData = {
  readBytesPerSec: 1024, writeBytesPerSec: 2048,
  syscallsRead: 10, syscallsWrite: 20
};
const sqliteData = {
  walSizeMB: 12.3, walSize: 12900000, pageCount: 4096, pageSize: 4096,
  cacheSize: 2000, cacheHitRate: 0.987
};
const sourcesData = {
  sources: { tx_inserted: 25, obs_inserted: 1787, backfill_path_json: 0, node_upserts: 329, observer_upserts: 1823, walCommits: 100 },
  sampleAt: '2026-01-01T00:00:00Z'
};

console.log('\n🧪 perf.js — Disk I/O + Write Sources (#1120)\n');

(async () => {
await test('Renders Disk I/O section', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sqliteData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(html.includes('Disk I/O'), 'should show Disk I/O heading');
  assert.ok(/2\.0\s*KB/.test(html), 'should render write rate value (2048 B/s formatted as 2.0 KB/s)');
});

await test('Renders Write Sources section with non-zero rates', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sqliteData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(html.includes('Write Sources'), 'should show Write Sources heading');
  assert.ok(html.includes('tx_inserted'), 'should list tx_inserted source');
  assert.ok(html.includes('obs_inserted'), 'should list obs_inserted source');
});

await test('Renders SQLite section with WAL + cache hit rate', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sqliteData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/WAL/i.test(html), 'should show WAL info');
  assert.ok(/Cache Hit/i.test(html) || /cacheHitRate/i.test(html), 'should show cache hit rate');
});

// === #1120 follow-up: cancelled writes + ingestor row + threshold UX ===

await test('Renders cancelledWriteBytesPerSec for server process', async () => {
  const sb = loadPerf();
  const io = { ...ioData, cancelledWriteBytesPerSec: 4096 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, io, sqliteData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/Cancel(led)?/i.test(html), 'should show a Cancelled write label');
  assert.ok(/4\.0\s*KB/.test(html), 'should render cancelled write rate (4096 B/s → 4.0 KB/s)');
});

await test('Renders ingestor row alongside server row in Disk I/O', async () => {
  const sb = loadPerf();
  const io = {
    ...ioData,
    cancelledWriteBytesPerSec: 0,
    ingestor: {
      readBytesPerSec: 0,
      writeBytesPerSec: 1048576,
      cancelledWriteBytesPerSec: 0,
      syscallsRead: 0,
      syscallsWrite: 0,
    },
  };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, io, sqliteData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/Ingestor/i.test(html), 'should label ingestor row');
  assert.ok(/1\.0\s*MB/.test(html), 'should render ingestor write 1 MB/s');
});

await test('WAL >100 MB fires ⚠️ flag', async () => {
  const sb = loadPerf();
  const sql = { ...sqliteData, walSizeMB: 150, walSize: 150 * 1048576 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  // The warning appears in the WAL Size card; assert proximity by extracting
  // the WAL Size card's text content.
  const walSection = html.match(/150\.0MB\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/);
  assert.ok(walSection, 'expected ⚠️ next to 150MB WAL value, html=' + html.slice(html.indexOf('WAL Size') - 200, html.indexOf('WAL Size') + 200));
});

await test('WAL <100 MB does NOT fire ⚠️ flag', async () => {
  const sb = loadPerf();
  const sql = { ...sqliteData, walSizeMB: 12.3 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  const walIdx = html.indexOf('WAL Size');
  const slice = html.slice(Math.max(0, walIdx - 200), walIdx);
  assert.ok(!/12\.3MB\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(slice), 'expected NO warning icon next to 12.3MB WAL value');
});

await test('Cache hit <90% fires ⚠️ flag', async () => {
  const sb = loadPerf();
  const sql = { ...sqliteData, cacheHitRate: 0.85 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/85\.0%\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(html), 'expected warning icon next to 85.0% cache hit value');
});

await test('Cache hit ≥90% does NOT fire ⚠️ flag', async () => {
  const sb = loadPerf();
  const sql = { ...sqliteData, cacheHitRate: 0.987 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(!/98\.7%\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(html), 'expected NO warning icon next to 98.7% cache hit value');
});

// === #1167 must-fix #7: threshold boundary cases ===

await test('WAL exactly 100 MB does NOT fire ⚠️ (boundary, strict >)', async () => {
  const sb = loadPerf();
  const sql = { ...sqliteData, walSizeMB: 100 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  const walIdx = html.indexOf('WAL Size');
  const slice = html.slice(Math.max(0, walIdx - 200), walIdx);
  assert.ok(!/100\.0MB\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(slice), 'expected NO warning icon at exactly 100 MB WAL (boundary), slice=' + slice);
});

await test('WAL infinitesimally over 100 MB DOES fire ⚠️', async () => {
  const sb = loadPerf();
  const sql = { ...sqliteData, walSizeMB: 100.01 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/100\.0MB\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(html), 'expected warning icon next to 100.0MB WAL value (just over threshold)');
});

await test('Cache hit exactly 90% does NOT fire ⚠️ (boundary, strict <)', async () => {
  const sb = loadPerf();
  const sql = { ...sqliteData, cacheHitRate: 0.90 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(!/90\.0%\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(html), 'expected NO warning icon at exactly 90.0% cache hit (boundary)');
});

await test('Cache hit infinitesimally below 90% DOES fire ⚠️', async () => {
  const sb = loadPerf();
  const sql = { ...sqliteData, cacheHitRate: 0.8999 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/90\.0%\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(html), 'expected warning icon next to 90.0% cache hit value (just under threshold)');
});

await test('Backfill anomaly: rate >10× its stable rolling baseline shows a warning', async () => {
  // The current detector compares a source with its own rolling baseline.
  // Prime a 60-second baseline at 1/s, then jump to 1000/s for the next tick.
  const sb = loadPerf();
  const samples = [
    { sources: { tx_inserted: 100, backfill_path_json: 0 }, sampleAt: '2026-01-01T00:00:00Z' },
    { sources: { tx_inserted: 400, backfill_path_json: 60 }, sampleAt: '2026-01-01T00:01:00Z' },
    { sources: { tx_inserted: 405, backfill_path_json: 1060 }, sampleAt: '2026-01-01T00:01:01Z' },
  ];
  for (const sample of samples) {
    stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sqliteData, sample);
    await sb.pages.perf.init({ set innerHTML(v) {} });
    await new Promise(r => setTimeout(r, 50));
  }
  const html = sb.getHtml();
  const idx = html.indexOf('backfill_path_json');
  assert.ok(idx >= 0, 'backfill_path_json row missing');
  const row = html.slice(idx, html.indexOf('</tr>', idx));
  assert.ok(row.includes('1000.00') && row.includes('1.00'), 'renders current and baseline rates');
  assert.ok(row.includes('#ph-warning"'), 'expected warning on backfill row when rate ratio >10×, row=' + row);
});

await test('Backfill anomaly: insufficient history suppresses the warning', async () => {
  // Two snapshots cannot provide the required stable historical baseline,
  // even if the current backfill rate is much larger than the tx rate.
  const sb = loadPerf();
  const t0 = '2026-01-01T00:00:00Z';
  const t1 = '2026-01-01T00:00:01Z';
  const phase1 = { sources: { tx_inserted: 5, backfill_path_json: 0 }, sampleAt: t0 };
  const phase2 = { sources: { tx_inserted: 6, backfill_path_json: 1000 }, sampleAt: t1 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sqliteData, phase1);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sqliteData, phase2);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  const idx = html.indexOf('backfill_path_json');
  assert.ok(idx >= 0, 'backfill_path_json row missing');
  const row = html.slice(idx, html.indexOf('</tr>', idx));
  assert.ok(!row.includes('#ph-warning"'), 'expected NO warning without a stable baseline, row=' + row);
});

console.log(`\n${passed} passed, ${failed} failed\n`);
process.exit(failed ? 1 : 0);
})();
