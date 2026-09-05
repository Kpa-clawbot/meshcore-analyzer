/**
 * E2E regression for #1206 review must-fix (kent-beck #2):
 *   ResizeObserver leak in initVCRHeightTracker().
 *
 * SPA navigates to /#/live, then bounces /#/nodes ↔ /#/live ≥ 3 times.
 * Each /#/live mount re-runs initVCRHeightTracker(); without the cleanup
 * tear-down (or with a future regression that orphans cleanup) each visit
 * would accumulate another ResizeObserver against #vcrBar.
 *
 * We can't read live ResizeObserver instances directly — wrap the
 * constructor + .disconnect() via addInitScript so we can count
 * outstanding (constructed but not disconnected) observers and assert it
 * does NOT grow with each /live mount.
 *
 * Also exercises node-map disposal with a paused browser clock: navigation,
 * pane closure and replacement must cancel the old map's delayed resize.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1206-resize-observer-leak-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function gotoHash(page, hash) {
  await page.evaluate((h) => { window.location.hash = h; }, hash);
  if (hash === '#/live') await waitForVCRTracker(page);
  else await page.locator('#nodesLeft[data-loaded="true"]').waitFor();
}

async function waitForVCRTracker(page) {
  // The tracker publishes this only after live's async node loading finishes.
  await page.locator('.live-page[style*="--vcr-bar-height"]').waitFor({ state: 'attached' });
}

// Synthetic nodes keep map lifecycle coverage independent of fixture locations.
const mapNodes = [
  { public_key: 'a1'.repeat(32), name: 'Map fixture A', lat: 0, lon: 0 },
  { public_key: 'b2'.repeat(32), name: 'Map fixture B', lat: 1, lon: 1 },
  { public_key: 'c3'.repeat(32), name: 'No location fixture', lat: null, lon: null },
].map((node) => ({ ...node, role: 'repeater', advert_count: 1 }));

async function withNodeMaps(browser, fn, allowedErrors = []) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  try {
    await page.route('**/api/nodes**', (route) => {
      const path = new URL(route.request().url()).pathname;
      const node = mapNodes.find((n) => path === '/api/nodes/' + n.public_key);
      const body = path === '/api/nodes'
        ? { nodes: mapNodes, total: mapNodes.length, counts: { repeater: mapNodes.length } }
        : node ? { node, recentAdverts: [] }
        : path === '/api/nodes/clock-skew' ? []
        : /\/health$/.test(path) ? { stats: {}, observers: [], recentPackets: [] }
        : /\/neighbors$/.test(path) ? { neighbors: [] }
        : /\/paths$/.test(path) ? { paths: [] } : {};
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    });
    await page.clock.install({ time: new Date('2026-01-01T00:00:00Z') });
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.locator('#nodesBody tr[data-key="' + mapNodes[0].public_key + '"]').waitFor();
    await page.clock.pauseAt(new Date('2026-01-01T00:02:00Z'));
    // Observe real Leaflet instances through public lifecycle APIs. No app hooks.
    await page.evaluate(() => {
      window.__nodeMaps = [];
      L.Map.addInitHook(function() {
        const container = this.getContainer();
        if (container.id !== 'nodeMap' && container.id !== 'nodeFullMap') return;
        const record = { removed: false, resizes: [] };
        window.__nodeMaps.push(record);
        this.on('unload', () => { record.removed = true; });
        const invalidateSize = this.invalidateSize;
        this.invalidateSize = function(...args) {
          record.resizes.push({ removed: record.removed, connected: container.isConnected });
          return invalidateSize.apply(this, args);
        };
      });
    });
    const open = async (view, node) => {
      if (view === 'side') {
        await page.locator('#nodesBody tr[data-key="' + node.public_key + '"]').click();
      } else {
        await page.evaluate((key) => { location.hash = '#/nodes/' + key; }, node.public_key);
      }
      const root = view === 'side' ? '#nodesRight' : '#nodeFullBody';
      await page.locator(root + ' .node-detail-name').filter({ hasText: node.name }).waitFor();
      if (node.lat != null) await page.locator(root + ' .leaflet-map-pane').waitFor({ state: 'attached' });
    };
    await fn(page, open);
    const unexpectedErrors = errors.filter((error) => !allowedErrors.includes(error));
    assert(unexpectedErrors.length === 0, 'unexpected pageerror: ' + unexpectedErrors.join('; '));
  } finally {
    await ctx.close();
  }
}

async function advanceMapClock(page, ms) {
  // Playwright also reports exceptions from fake-timer callbacks via runFor.
  const error = await page.clock.runFor(ms).then(() => null, (error) => error);
  assert(!error, 'timer callback must not throw: ' + (error && error.message));
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log('\n=== #1206 ResizeObserver leak E2E against ' + BASE + ' ===');

  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });

  // Install ResizeObserver wrapper BEFORE any page script runs.
  await ctx.addInitScript(() => {
    var RealRO = window.ResizeObserver;
    if (typeof RealRO !== 'function') {
      window.__roOutstanding = 0;
      window.__roConstructed = 0;
      return;
    }
    window.__roConstructed = 0;
    window.__roOutstanding = 0;
    function WrappedRO(cb) {
      var inst = new RealRO(cb);
      window.__roConstructed++;
      window.__roOutstanding++;
      var realDisconnect = inst.disconnect.bind(inst);
      var disconnected = false;
      inst.disconnect = function() {
        if (!disconnected) {
          disconnected = true;
          window.__roOutstanding--;
        }
        return realDisconnect();
      };
      return inst;
    }
    WrappedRO.prototype = RealRO.prototype;
    window.ResizeObserver = WrappedRO;
  });

  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  await step('initial /#/live mount constructs at most 1 VCR ResizeObserver', async () => {
    await page.goto(BASE + '/#/live', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#vcrBar', { timeout: 8000 });
    await waitForVCRTracker(page);
    // Baseline snapshot — record outstanding right after first /live mount.
    const snap = await page.evaluate(() => ({
      outstanding: window.__roOutstanding,
      constructed: window.__roConstructed,
    }));
    assert(typeof snap.outstanding === 'number',
      'ResizeObserver wrapper not installed (snap=' + JSON.stringify(snap) + ')');
    // Stash the first-mount baseline on window for the next step.
    await page.evaluate((b) => { window.__roBaseline = b; }, snap);
  });

  await step('3 SPA round-trips /live<->/nodes do NOT grow outstanding observer count', async () => {
    for (let i = 0; i < 3; i++) {
      await gotoHash(page, '#/nodes');
      await gotoHash(page, '#/live');
      await page.waitForSelector('#vcrBar', { timeout: 8000 });
    }
    const after = await page.evaluate(() => ({
      outstanding: window.__roOutstanding,
      constructed: window.__roConstructed,
      baseline: window.__roBaseline,
    }));
    // The VCR tracker MUST clean its observer on destroy(). After N
    // remounts the outstanding count for VCR-tracking observers must not
    // exceed the baseline.  We can't isolate which observers are
    // ours, so we use the delta: 4 mounts * leak-of-1 = 3 extra
    // outstanding observers, which is the failure mode this test gates.
    var delta = after.outstanding - after.baseline.outstanding;
    assert(delta <= 0,
      'ResizeObserver leak: outstanding count grew by ' + delta +
      ' across 3 SPA round-trips (baseline=' + after.baseline.outstanding +
      ', after=' + after.outstanding + ', constructed=' + after.constructed +
      '). Expected delta <= 0.');
  });

  await ctx.close();

  for (const view of ['side', 'full']) {
    await step(view + ' node map: navigation cancels the pending resize', () => withNodeMaps(browser, async (page, open) => {
      await open(view, mapNodes[0]);
      await page.evaluate(() => { location.hash = '#/live'; });
      await page.locator('#vcrBar').waitFor();
      await advanceMapClock(page, 100);
      const maps = await page.evaluate(() => window.__nodeMaps);
      assert(maps.length === 1 && maps[0].removed, 'navigation must remove the node map');
      assert(maps[0].resizes.length === 0, 'removed map must not be resized');
    }));

    await step(view + ' node map: replacement only resizes at its own deadline', () => withNodeMaps(browser, async (page, open) => {
      await open(view, mapNodes[0]);
      await advanceMapClock(page, 50);
      await open(view, mapNodes[1]);
      await advanceMapClock(page, 50); // First map's deadline; replacement is only 50ms old.
      let maps = await page.evaluate(() => window.__nodeMaps);
      assert(maps.length === 2 && maps[0].removed && !maps[1].removed, 'replacement must own the surviving map');
      assert(maps[0].resizes.length === 0 && maps[1].resizes.length === 0, 'old timer must not resize either map');
      await advanceMapClock(page, 49);
      maps = await page.evaluate(() => window.__nodeMaps);
      assert(maps[1].resizes.length === 0, 'replacement must wait its full 100ms');
      await advanceMapClock(page, 1);
      maps = await page.evaluate(() => window.__nodeMaps);
      assert(maps[1].resizes.length === 1 && !maps[1].resizes[0].removed && maps[1].resizes[0].connected,
        'surviving map must resize once while connected');
    }));
  }

  for (const close of ['button', 'Escape']) {
    await step('side node map: ' + close + ' disposes the map before resize', () => withNodeMaps(browser, async (page, open) => {
      await open('side', mapNodes[0]);
      if (close === 'button') await page.locator('#nodesRight .panel-close-btn').click();
      else await page.keyboard.press('Escape');
      await page.locator('#nodesRight.empty').waitFor();
      await advanceMapClock(page, 100);
      const maps = await page.evaluate(() => window.__nodeMaps);
      assert(maps.length === 1 && maps[0].removed, 'closing the pane must remove its map');
      assert(maps[0].resizes.length === 0, 'closed pane map must not be resized');
    }));
  }

  await step('side node map: no-location replacement disposes the pending map', () => withNodeMaps(browser, async (page, open) => {
    await open('side', mapNodes[0]);
    await open('side', mapNodes[2]);
    await advanceMapClock(page, 100);
    const maps = await page.evaluate(() => window.__nodeMaps);
    assert(maps.length === 1 && maps[0].removed, 'no-location replacement must remove the previous map');
    assert(maps[0].resizes.length === 0, 'no-location replacement must cancel the old resize');
    assert(await page.locator('#nodeMap').count() === 0, 'no-location node must not create a map');
  }));

  for (const scenario of [
    { view: 'full', fails: true },
    { view: 'full', fails: false },
    { view: 'side', fails: true },
    { view: 'side', fails: false },
  ]) {
    const expectedError = scenario.fails ? 'API 500: /nodes/' + mapNodes[0].public_key : null;
    await step(scenario.view + ' node map: stale ' + (scenario.fails ? 'failure' : 'no-location response') + ' preserves the replacement',
      () => withNodeMaps(browser, async (page, open) => {
        const requestUrl = '**/api/nodes/' + mapNodes[0].public_key;
        let releaseResponse;
        const holdResponse = new Promise((resolve) => { releaseResponse = resolve; });
        await page.route(requestUrl, async (route) => {
          await holdResponse;
          await route.fulfill({ status: scenario.fails ? 500 : 200, contentType: 'application/json', body: JSON.stringify({
            node: { ...mapNodes[0], lat: null, lon: null }, recentAdverts: [],
          }) });
        });
        const requested = page.waitForRequest(requestUrl);
        if (scenario.view === 'full') {
          await page.evaluate((key) => { location.hash = '#/nodes/' + key; }, mapNodes[0].public_key);
        } else {
          await page.locator('#nodesBody tr[data-key="' + mapNodes[0].public_key + '"]').click();
        }
        await requested;
        // Join the existing in-flight API promise so assertions run after the
        // client has decoded/rejected the held response, not merely sent it.
        const requestCompleted = page.evaluate((key) => api('/nodes/' + key).then(() => null, (error) => error.message), mapNodes[0].public_key);
        await open(scenario.view, mapNodes[1]);
        await advanceMapClock(page, 50);
        // api() may report its rejected finally() derivative as a pageerror.
        // Only this deliberately injected API failure is allowed by the helper.
        releaseResponse();
        let requestTimeout;
        const result = await Promise.race([
          requestCompleted,
          new Promise((_, reject) => {
            requestTimeout = setTimeout(() => reject(new Error('held response was not handled')), 8000);
          }),
        ]).finally(() => clearTimeout(requestTimeout));
        assert(result === expectedError, 'held response must complete with the intended result');
        let maps = await page.evaluate(() => window.__nodeMaps);
        assert(maps.length === 1 && !maps[0].removed, 'stale response must not remove the replacement map');
        assert(maps[0].resizes.length === 0, 'replacement must wait for its own resize deadline');
        await advanceMapClock(page, 50);
        maps = await page.evaluate(() => window.__nodeMaps);
        assert(maps[0].resizes.length === 1 && maps[0].resizes[0].connected && !maps[0].resizes[0].removed,
          'replacement must remain connected and receive its own resize');
      }, expectedError ? [expectedError] : []));
  }

  await browser.close();
  console.log('\nSPA observer and node-map lifecycle: ' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
