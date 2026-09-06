#!/usr/bin/env node
/**
 * #1758 — Neighbor Graph re-renders on filter change (canvas un-hide guard).
 *
 * The Analytics → Neighbor Graph renderer skips the force simulation when the
 * DISPLAYED node set exceeds 1000 nodes: it hides #ngCanvas and inserts a
 * stable-id #ngSkipMsg notice. PR #1758 made startGraphRenderer() re-entrant so
 * that applyNGFilters() re-evaluates that guard against the *filtered* set — so
 * narrowing the role filters below the limit must remove #ngSkipMsg and un-hide
 * #ngCanvas again, and widening them back must restore the skip state.
 *
 * This is the red-then-green guard for #1758: it FAILS before the lifecycle fix
 * (the renderer keyed the guard off the full fetched graph and never un-hid the
 * canvas once the skip message was shown, so filtering down did nothing) and
 * PASSES after it.
 *
 * Fixture: ~1200 "companion" + ~200 "repeater" nodes, every node edge-bearing.
 * With the default filters (observer unchecked, companion + repeater checked)
 * the displayed set is >1000 → skip. Unchecking "companion" drops to ~200
 * repeater nodes < 1000 → render. Re-checking restores the skip state.
 *
 * Usage:
 *   BASE_URL=http://localhost:13581 \
 *   CHROMIUM_PATH=/path/to/chromium \
 *   node test-issue-1758-ng-filter-rerenders-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

function fail(msg) {
  console.error('  ✗ ' + msg);
  process.exit(1);
}

// Build a synthetic neighbor-graph fixture: every node carries at least one
// edge so it survives the "edge-bearing nodes only" filter in applyNGFilters().
function buildFixture() {
  const nodes = [];
  const edges = [];
  function pk(prefix, i) {
    // 64-hex pubkey, deterministic and unique per (prefix,i).
    return (prefix + i.toString(16)).padEnd(64, '0').slice(0, 64);
  }
  const COMPANIONS = 1200;
  const REPEATERS = 200;
  // Companion ring: each companion edged to the next (wraps), so all are
  // edge-bearing among themselves and survive when "companion" is checked.
  for (let i = 0; i < COMPANIONS; i++) {
    nodes.push({ pubkey: pk('c', i), role: 'companion', neighbor_count: 2 });
  }
  for (let i = 0; i < COMPANIONS; i++) {
    const a = pk('c', i);
    const b = pk('c', (i + 1) % COMPANIONS);
    edges.push({ source: a, target: b, score: 0.9, ambiguous: false });
  }
  // Repeater ring: independent, so unchecking "companion" leaves ~200 nodes.
  for (let i = 0; i < REPEATERS; i++) {
    nodes.push({ pubkey: pk('r', i), role: 'repeater', neighbor_count: 2 });
  }
  for (let i = 0; i < REPEATERS; i++) {
    const a = pk('r', i);
    const b = pk('r', (i + 1) % REPEATERS);
    edges.push({ source: a, target: b, score: 0.9, ambiguous: false });
  }
  return { nodes, edges, stats: {} };
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1758 neighbor-graph filter re-render guard against ${BASE} ===`);

  const fixture = buildFixture();

  // Intercept the neighbor-graph API and return the synthetic oversized graph.
  await page.route('**/api/analytics/neighbor-graph*', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(fixture),
    });
  });

  // Deep-link straight to the Neighbor Graph tab instead of clicking it.
  // loadAnalytics() awaits a Promise.all() of the OTHER analytics endpoints and
  // only THEN calls renderTab(_currentTab). A tab click that lands before that
  // resolves makes renderNeighborGraphTab() run twice: the second pass (fired by
  // loadAnalytics when its fetches finally settle — slow during server warmup)
  // rebuilds the role checkboxes (all re-checked) and a fresh _ngState from the
  // full graph, flipping the >1000-node skip guard back on mid-test and racing
  // our filter changes. Deep-linking renders the tab exactly once, as part of
  // loadAnalytics itself, so #ngSkipMsg appears only when the page is quiescent
  // and no late re-render can clobber the filter state we assert on.
  await page.goto(BASE + '/#/analytics?tab=neighbor-graph', { waitUntil: 'domcontentloaded' });
  // The renderer is async (awaits the API). Wait for the role checkboxes —
  // they exist once the tab markup is painted.
  await page.waitForSelector('#ngRoleChecks input[data-role="companion"]', { timeout: 15000 });
  // Wait for the first guard evaluation to settle: with the oversized default
  // set, #ngSkipMsg should appear.
  await page.waitForSelector('#ngSkipMsg', { timeout: 15000 });

  // --- Assertion 1: initial oversized state hides the canvas, shows skip msg.
  const initial = await page.evaluate(() => {
    const skip = document.getElementById('ngSkipMsg');
    const canvas = document.getElementById('ngCanvas');
    return {
      hasSkip: !!skip,
      canvasDisplay: canvas ? getComputedStyle(canvas).display : '(no canvas)',
    };
  });
  if (!initial.hasSkip) fail('initial: expected #ngSkipMsg to exist (>1000 displayed nodes)');
  if (initial.canvasDisplay !== 'none') {
    fail(`initial: expected #ngCanvas display 'none', got '${initial.canvasDisplay}'`);
  }
  console.log('  ✓ initial: #ngSkipMsg present and #ngCanvas hidden');

  // --- Filter down: uncheck "companion" → ~200 repeater nodes < 1000.
  await page.evaluate(() => {
    const cb = document.querySelector('#ngRoleChecks input[data-role="companion"]');
    cb.checked = false;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
  });
  // Guard re-evaluates synchronously inside applyNGFilters(); wait for the
  // skip message to be removed.
  await page.waitForFunction(() => !document.getElementById('ngSkipMsg'), { timeout: 15000 });

  // --- Assertion 2: filtered-down state removes skip msg, un-hides canvas.
  const filtered = await page.evaluate(() => {
    const skip = document.getElementById('ngSkipMsg');
    const canvas = document.getElementById('ngCanvas');
    return {
      hasSkip: !!skip,
      canvasDisplay: canvas ? getComputedStyle(canvas).display : '(no canvas)',
    };
  });
  if (filtered.hasSkip) {
    fail('after unchecking companion: expected #ngSkipMsg to be gone (<1000 nodes)');
  }
  if (filtered.canvasDisplay === 'none') {
    fail('after unchecking companion: expected #ngCanvas to be visible (display !== none)');
  }
  console.log('  ✓ filtered down: #ngSkipMsg gone and #ngCanvas visible');

  // --- Filter back up: re-check "companion" → >1000 again → skip restored.
  await page.evaluate(() => {
    const cb = document.querySelector('#ngRoleChecks input[data-role="companion"]');
    cb.checked = true;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await page.waitForSelector('#ngSkipMsg', { timeout: 15000 });

  // --- Assertion 3: re-widened state restores skip msg and hides canvas.
  const restored = await page.evaluate(() => {
    const skip = document.getElementById('ngSkipMsg');
    const canvas = document.getElementById('ngCanvas');
    return {
      hasSkip: !!skip,
      canvasDisplay: canvas ? getComputedStyle(canvas).display : '(no canvas)',
    };
  });
  if (!restored.hasSkip) {
    fail('after re-checking companion: expected #ngSkipMsg to return (>1000 nodes)');
  }
  if (restored.canvasDisplay !== 'none') {
    fail(`after re-checking companion: expected #ngCanvas display 'none', got '${restored.canvasDisplay}'`);
  }
  console.log('  ✓ re-widened: #ngSkipMsg restored and #ngCanvas hidden');

  // --- Skip message must report filtered-of-total, not just the filtered count.
  const restoredSkipText = await page.evaluate(
    () => (document.getElementById('ngSkipMsg') || {}).textContent || '');
  if (!/\bof\b/.test(restoredSkipText) || !/nodes/.test(restoredSkipText)) {
    fail(`skip message should report "N of M nodes", got: ${JSON.stringify(restoredSkipText)}`);
  }
  console.log('  ✓ skip message reports filtered-of-total node count');

  // --- Filter down AGAIN: the small→oversized→small lifecycle must keep working
  // (the re-entrancy epoch guard + bind-once interaction listeners survive
  // repeated re-renders, not just one down→up cycle).
  await page.evaluate(() => {
    const cb = document.querySelector('#ngRoleChecks input[data-role="companion"]');
    cb.checked = false;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await page.waitForFunction(() => !document.getElementById('ngSkipMsg'), { timeout: 15000 });
  const reFiltered = await page.evaluate(() => {
    const canvas = document.getElementById('ngCanvas');
    return {
      hasSkip: !!document.getElementById('ngSkipMsg'),
      canvasDisplay: canvas ? getComputedStyle(canvas).display : '(no canvas)',
    };
  });
  if (reFiltered.hasSkip) fail('second filter-down: expected #ngSkipMsg gone again');
  if (reFiltered.canvasDisplay === 'none') fail('second filter-down: expected #ngCanvas visible again');
  console.log('  ✓ second filter-down: render restored across repeated cycles');

  // --- #1925: a theme-refresh must not discard the applied filter.
  // Every page load fires one 'theme-refresh' about 300ms after
  // /api/config/theme resolves. It used to call renderTab(), which replaced
  // el.innerHTML (resetting the role checkboxes to their defaults) and
  // rebuilt _ngState from the full graph, putting #ngSkipMsg back. On an
  // idle machine the test finished ~90ms before that landed; under CI load
  // it landed mid-test, which is both failure modes of #1925.
  // Dispatching the event directly makes that deterministic, so this asserts
  // the cause rather than waiting for the race to show up.
  await page.evaluate(() => window.dispatchEvent(new CustomEvent('theme-refresh')));
  await page.waitForTimeout(500);
  const afterTheme = await page.evaluate(() => {
    const canvas = document.getElementById('ngCanvas');
    const cb = document.querySelector('#ngRoleChecks input[data-role="companion"]');
    return {
      hasSkip: !!document.getElementById('ngSkipMsg'),
      canvasDisplay: canvas ? getComputedStyle(canvas).display : '(no canvas)',
      companionChecked: cb ? cb.checked : '(no checkbox)',
    };
  });
  if (afterTheme.companionChecked !== false) {
    fail('theme-refresh reset the role filter (companion re-checked): '
      + JSON.stringify(afterTheme));
  }
  if (afterTheme.hasSkip) fail('theme-refresh brought #ngSkipMsg back: ' + JSON.stringify(afterTheme));
  if (afterTheme.canvasDisplay === 'none') fail('theme-refresh hid #ngCanvas again');
  console.log('  ✓ theme-refresh preserves the applied filter (#1925)');

  await browser.close();

  console.log('\nPASS: #1758 neighbor-graph filter re-render lifecycle holds');
  process.exit(0);
})().catch((e) => { console.error(e); process.exit(1); });
