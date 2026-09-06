/**
 * E2E (#1329): Map controls panel on mobile must NOT be capped at 200px
 * with internal scroll. Use accordion sections — one expanded at a time —
 * so the visible content always fits without scrolling.
 *
 * Mobile (375x812):
 *  - Open Map controls.
 *  - Panel must have accordion sections (legend acts as toggle, with
 *    aria-expanded attribute).
 *  - Default state: at most one section expanded.
 *  - Panel contents must NOT require internal scroll
 *    (scrollHeight <= clientHeight + 1).
 *  - Clicking a different section's legend collapses the previously-open
 *    section (single-open behavior).
 *
 * Desktop (1280x800):
 *  - Existing layout unchanged: all sections visible by default,
 *    panel position:absolute, modest width.
 *
 * Run: BASE_URL=http://localhost:13581 node test-issue-1329-map-controls-accordion-e2e.js
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

async function run() {
  const launchOpts = { args: ['--no-sandbox'] };
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  const browser = await chromium.launch(launchOpts);

  // === Mobile: 375x812 ===
  const ctx = await browser.newContext({ viewport: { width: 375, height: 812 } });
  const page = await ctx.newPage();

  await page.goto(BASE + '/#/map', { waitUntil: 'load', timeout: 60000 });
  await page.waitForSelector('#leaflet-map', { timeout: 10000 });
  await page.waitForSelector('#mapControls', { state: 'attached', timeout: 10000 });
  await page.waitForTimeout(500);

  // Ensure controls panel is expanded (default is collapsed on mobile).
  await page.evaluate(() => {
    const panel = document.getElementById('mapControls');
    const btn = document.getElementById('mapControlsToggle');
    if (panel && panel.classList.contains('collapsed')) btn && btn.click();
  });
  await page.waitForTimeout(300);

  await step('mobile: at least one accordion section present with aria-expanded', async () => {
    const data = await page.evaluate(() => {
      const panel = document.getElementById('mapControls');
      // Accordion section markers: legend (or button) carrying aria-expanded
      // inside a .mc-section.mc-accordion (or equivalent) descendant.
      const toggles = panel.querySelectorAll('.mc-section [aria-expanded], .mc-accordion-toggle[aria-expanded]');
      const sections = panel.querySelectorAll('.mc-section');
      return {
        toggles: toggles.length,
        sections: sections.length,
        expandedCount: Array.from(toggles).filter(t => t.getAttribute('aria-expanded') === 'true').length,
      };
    });
    assert(data.toggles >= 1,
      'expected ≥1 accordion toggle (aria-expanded), got ' + data.toggles +
      ' (sections=' + data.sections + ')');
  });

  await step('mobile: at most one section expanded by default', async () => {
    const data = await page.evaluate(() => {
      const panel = document.getElementById('mapControls');
      const toggles = panel.querySelectorAll('.mc-section [aria-expanded], .mc-accordion-toggle[aria-expanded]');
      return {
        expandedCount: Array.from(toggles).filter(t => t.getAttribute('aria-expanded') === 'true').length,
        total: toggles.length,
      };
    });
    assert(data.expandedCount <= 1,
      'expected ≤1 section expanded by default, got ' + data.expandedCount + '/' + data.total);
  });

  await step('mobile: panel content does NOT require internal scroll', async () => {
    const data = await page.evaluate(() => {
      const panel = document.getElementById('mapControls');
      return {
        scrollH: panel.scrollHeight,
        clientH: panel.clientHeight,
        overflowY: getComputedStyle(panel).overflowY,
      };
    });
    // The accordion sections should keep content within viewport — when only
    // one section is expanded, panel must not need to scroll internally.
    assert(data.scrollH <= data.clientH + 1,
      'panel must not require internal scroll (scrollH=' + data.scrollH +
      ' clientH=' + data.clientH + ')');
  });

  await step('mobile: clicking a 2nd toggle collapses the first (single-open)', async () => {
    const result = await page.evaluate(() => {
      const panel = document.getElementById('mapControls');
      const toggles = Array.from(panel.querySelectorAll('.mc-section [aria-expanded], .mc-accordion-toggle[aria-expanded]'));
      if (toggles.length < 2) return { skip: true, n: toggles.length };
      // Find one currently closed and one open; if all closed, open first then click second.
      let openIdx = toggles.findIndex(t => t.getAttribute('aria-expanded') === 'true');
      if (openIdx === -1) {
        toggles[0].click();
        openIdx = 0;
      }
      const otherIdx = openIdx === 0 ? 1 : 0;
      toggles[otherIdx].click();
      return {
        skip: false,
        firstNow: toggles[openIdx].getAttribute('aria-expanded'),
        otherNow: toggles[otherIdx].getAttribute('aria-expanded'),
      };
    });
    if (result.skip) {
      throw new Error('need at least 2 accordion toggles to test single-open (got ' + result.n + ')');
    }
    assert(result.otherNow === 'true',
      'second toggle should be open after click, got ' + result.otherNow);
    assert(result.firstNow === 'false',
      'first toggle should auto-close (single-open), got ' + result.firstNow);
  });

  await ctx.close();

  // === Desktop: 1280x800 ===
  const ctx2 = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const p2 = await ctx2.newPage();
  await p2.goto(BASE + '/#/map', { waitUntil: 'load', timeout: 60000 });
  await p2.waitForSelector('#mapControls', { state: 'attached', timeout: 10000 });
  await p2.waitForTimeout(300);

  await step('desktop (1280px): panel position:absolute, all section contents visible', async () => {
    const data = await p2.evaluate(() => {
      const panel = document.getElementById('mapControls');
      const cs = getComputedStyle(panel);
      const rect = panel.getBoundingClientRect();
      // Check that section content (e.g., labels) is visible on desktop.
      // Exclude controls inside a collapsed progressive-disclosure container
      // (inline style="display:none" — the geo-filter and affinity-debug
      // toggles, and #1771's "Important Links" rank-by select, which a
      // checkbox reveals on demand). Those are hidden by an explicit toggle,
      // NOT by the mobile accordion this desktop check guards against: the
      // accordion collapses via the `.mc-collapsed > *:not(legend){display:none}`
      // CSS rule (mobile media query, class-based, no inline style), so a
      // desktop accordion regression is still counted here and caught.
      const allInputs = Array.from(panel.querySelectorAll('input[type=checkbox], select, button'))
        .filter(el => !el.closest('[style*="display:none"], [style*="display: none"]'));
      let visible = 0;
      allInputs.forEach(el => {
        const r = el.getBoundingClientRect();
        if (r.width > 0 && r.height > 0) visible++;
      });
      return {
        position: cs.position,
        width: Math.round(rect.width),
        vw: window.innerWidth,
        visibleControls: visible,
        totalControls: allInputs.length,
      };
    });
    assert(data.position === 'absolute',
      'desktop panel must be position:absolute, got ' + data.position);
    assert(data.width < data.vw * 0.5,
      'desktop panel must be <50% viewport width, got ' + data.width + '/' + data.vw);
    // All (or nearly all) controls should be visible on desktop — accordion
    // collapse must NOT apply at desktop sizes.
    assert(data.visibleControls >= data.totalControls - 2,
      'desktop must show all controls (got ' + data.visibleControls + '/' + data.totalControls + ')');
  });

  await browser.close();

  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed' +
              (failed ? ', ' + failed + ' failed' : ''));
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => { console.error('Fatal:', err); process.exit(1); });
