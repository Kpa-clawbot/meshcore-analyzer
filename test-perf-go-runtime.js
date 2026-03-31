/* Tests for perf.js Go runtime vs Node event loop rendering (fixes #153) */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Minimal sandbox to run perf.js in a browser-like context
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

// Load perf.js into sandbox
function loadPerf() {
  const sb = makeSandbox();
  const code = fs.readFileSync('public/perf.js', 'utf8');
  vm.runInNewContext(code, sb.ctx);
  return sb;
}

// Stub fetch to return controlled data
function stubFetch(sb, perfData, healthData) {
  sb.ctx.fetch = (url) => {
    if (url === '/api/perf') return Promise.resolve({ json: () => Promise.resolve(perfData) });
    if (url === '/api/health') return Promise.resolve({ json: () => Promise.resolve(healthData) });
    return Promise.resolve({ json: () => Promise.resolve({}) });
  };
}

const basePerf = {
  totalRequests: 100, avgMs: 5, uptime: 3600,
  slowQueries: [], endpoints: {}, cache: null, packetStore: null, sqlite: null
};

const nodeHealth = {
  engine: 'node',
  uptimeHuman: '1h',
  memory: { heapUsed: 100, heapTotal: 200, rss: 250 },
  eventLoop: { p95Ms: 10, maxLagMs: 20, currentLagMs: 1 },
  websocket: { clients: 3 }
};

const goRuntime = {
  goroutines: 17, numGC: 31, pauseTotalMs: 2.1, lastPauseMs: 0.03,
  heapAllocMB: 473, heapSysMB: 1035, heapInuseMB: 663, heapIdleMB: 371, numCPU: 2
};

const goHealth = {
  engine: 'go',
  uptimeHuman: '2h',
  websocket: { clients: 5 }
};

console.log('\n🧪 perf.js — Go Runtime vs Node Event Loop\n');

// --- Node engine tests ---

test('Node engine shows Event Loop labels', async () => {
  const sb = loadPerf();
  stubFetch(sb, basePerf, nodeHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  // Wait for async refresh
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('Event Loop p95'), 'should show Event Loop p95');
  assert.ok(html.includes('EL Max Lag'), 'should show EL Max Lag');
  assert.ok(html.includes('EL Current'), 'should show EL Current');
  assert.ok(html.includes('System Health'), 'should show System Health heading');
});

test('Node engine does NOT show Go Runtime heading', async () => {
  const sb = loadPerf();
  stubFetch(sb, basePerf, nodeHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(!html.includes('Go Runtime'), 'should not show Go Runtime');
  assert.ok(!html.includes('Goroutines'), 'should not show Goroutines');
});

test('Node engine shows memory stats', async () => {
  const sb = loadPerf();
  stubFetch(sb, basePerf, nodeHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('Heap Used'), 'should show Heap Used');
  assert.ok(html.includes('RSS'), 'should show RSS');
});

// --- Go engine tests ---

test('Go engine shows Go Runtime heading', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('Go Runtime'), 'should show Go Runtime heading');
});

test('Go engine shows all goRuntime fields', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('Goroutines'), 'should show Goroutines');
  assert.ok(html.includes('GC Collections'), 'should show GC Collections');
  assert.ok(html.includes('GC Pause Total'), 'should show GC Pause Total');
  assert.ok(html.includes('Last GC Pause'), 'should show Last GC Pause');
  assert.ok(html.includes('Heap Alloc'), 'should show Heap Alloc');
  assert.ok(html.includes('Heap Sys'), 'should show Heap Sys');
  assert.ok(html.includes('Heap Inuse'), 'should show Heap Inuse');
  assert.ok(html.includes('Heap Idle'), 'should show Heap Idle');
  assert.ok(html.includes('CPUs'), 'should show CPUs');
});

test('Go engine shows goRuntime values', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('17'), 'goroutines value');
  assert.ok(html.includes('31'), 'numGC value');
  assert.ok(html.includes('2.1ms'), 'pauseTotalMs value');
  assert.ok(html.includes('0.03ms'), 'lastPauseMs value');
  assert.ok(html.includes('473MB'), 'heapAllocMB value');
  assert.ok(html.includes('1035MB'), 'heapSysMB value');
  assert.ok(html.includes('663MB'), 'heapInuseMB value');
  assert.ok(html.includes('371MB'), 'heapIdleMB value');
});

test('Go engine does NOT show Event Loop labels', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(!html.includes('Event Loop'), 'should not show Event Loop');
  assert.ok(!html.includes('EL Max Lag'), 'should not show EL Max Lag');
  assert.ok(!html.includes('EL Current'), 'should not show EL Current');
});

test('Go engine still shows WS Clients', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('WS Clients'), 'should show WS Clients');
  assert.ok(html.includes('>5<'), 'should show 5 WS clients');
});

// --- GC color threshold tests ---

test('Go GC pause green when lastPauseMs <= 1', async () => {
  const sb = loadPerf();
  const gr = { ...goRuntime, lastPauseMs: 0.5 };
  stubFetch(sb, { ...basePerf, goRuntime: gr }, goHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('var(--status-green)'), 'should use green for low GC pause');
});

test('Go GC pause yellow when lastPauseMs > 1 and <= 5', async () => {
  const sb = loadPerf();
  const gr = { ...goRuntime, lastPauseMs: 3 };
  stubFetch(sb, { ...basePerf, goRuntime: gr }, goHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('var(--status-yellow)'), 'should use yellow for moderate GC pause');
});

test('Go GC pause red when lastPauseMs > 5', async () => {
  const sb = loadPerf();
  const gr = { ...goRuntime, lastPauseMs: 10 };
  stubFetch(sb, { ...basePerf, goRuntime: gr }, goHealth);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('var(--status-red)'), 'should use red for high GC pause');
});

// --- Fallback: engine=go but no goRuntime falls back to Node UI ---

test('engine=go but missing goRuntime falls back to Node UI', async () => {
  const sb = loadPerf();
  const goHealthWithMemory = {
    ...goHealth,
    memory: { heapUsed: 50, heapTotal: 100, rss: 80 },
    eventLoop: { p95Ms: 5, maxLagMs: 10, currentLagMs: 1 }
  };
  stubFetch(sb, basePerf, goHealthWithMemory);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('Event Loop p95'), 'should fall back to Event Loop');
  assert.ok(!html.includes('Go Runtime'), 'should not show Go Runtime');
});

// --- Missing engine field ---

test('Missing engine field shows Node UI', async () => {
  const sb = loadPerf();
  const healthNoEngine = {
    uptimeHuman: '1h',
    memory: { heapUsed: 100, heapTotal: 200, rss: 250 },
    eventLoop: { p95Ms: 10, maxLagMs: 20, currentLagMs: 1 },
    websocket: { clients: 2 }
  };
  stubFetch(sb, basePerf, healthNoEngine);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  assert.ok(html.includes('Event Loop p95'), 'should show Event Loop');
  assert.ok(!html.includes('Go Runtime'), 'should not show Go Runtime');
});

console.log(`\n${passed} passed, ${failed} failed\n`);
process.exit(failed ? 1 : 0);
