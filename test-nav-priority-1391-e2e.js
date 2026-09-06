#!/usr/bin/env node
/* Issue #1391 — Priority+ nav regression: active-route pill overflows at ~1080px.
 * Issue #1396 — /#/channels extension: entire inline strip EMPTY at ~1024px on /#/channels,
 *               More dropdown showing only one item (root cause: #1400 min-height overflow).
 *
 * Symptom (#1391): at viewport ~1080-1200px on a non-high-priority active route
 * (e.g. /#/perf, /#/audio-lab), the active-route pill is shoved into the
 * More dropdown instead of staying visible inline. Operator screenshot at
 * ~1080px on /#/perf showed the navbar with only the "Perf" pill visible
 * (or, in the inverse failure mode, NO inline pill at all, with More
 * containing only the orphaned active route).
 *
 * Symptom (#1396): at viewport ~1024px on /#/channels, the entire .nav-links
 * strip was visually empty (no high-priority links, no active pill, nothing)
 * and the More dropdown contained only "Tools". Root cause was issue #1400:
 * min-height:48px on .nav-link inflated the strip beyond the 52px top-nav
 * height; Firefox flex-centered it to a negative y, clipping it above the
 * viewport (overflow:hidden). Fixed by PR #1401. This test locks that contract.
 *
 * Acceptance (from issue #1391):
 *   - Active-route pill MUST always be visible inline (never overflowed
 *     to More) at any viewport ≥768px.
 *   - If active route is NOT a high-priority link (e.g. /#/perf), the
 *     high-priority links MUST still be inline ≥768px.
 *   - Every link in overflow MUST be reachable via the More dropdown
 *     (the existing #1311/#1139 contract — don't regress).
 *
 * Acceptance (from issue #1396 / #1400):
 *   - .nav-links must never render at a negative top offset (y >= 0).
 *   - In the ≤1100px force-collapse band on /#/channels, More must contain
 *     exactly the 5 non-active non-high routes; channels stays inline.
 *
 * Mutation guard: removing the "pin active inline" rule in applyNavPriority
 * must make this test fail (active link gets overflowed at 1080px on /#/perf).
 * Mutation guard (#1400): re-adding min-height:48px to .nav-link globally
 * must make the navLinksTop assertion fail with a large negative value.
 */
'use strict';

const assert = require('node:assert');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const HIGH_PRIORITY_HREFS = ['#/home', '#/packets', '#/map', '#/live', '#/nodes'];

// Routes whose link is NOT data-priority="high" (verified via
// `grep data-priority public/index.html`). These exercise the
// "active pill is non-high" branch where the bug surfaces.
// #1396: extended to include /#/channels — operator screenshot at ~1024px
// showed the entire inline strip EMPTY and More containing only "Tools".
const NON_HIGH_ROUTES = ['#/perf', '#/audio-lab', '#/analytics', '#/observers', '#/channels'];

// Operator screenshot was ~1080px. Cover the narrow-desktop CSS branch
// (≤1100) AND the measurement-loop branch (>1100) — bug reproduces in
// both, and the #1311 fix only addressed >1100.
const WIDTHS = [1024, 1080, 1100, 1101, 1200, 1300];
const HEIGHT = 800;

async function main() {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (process.env.CHROMIUM_REQUIRE === '1') {
      console.error(`test-nav-priority-1391-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-nav-priority-1391-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  for (const w of WIDTHS) {
    for (const route of NON_HIGH_ROUTES) {
      await page.setViewportSize({ width: w, height: HEIGHT });
      await page.goto(`${BASE}/${route}`, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('.top-nav .nav-links');
      await page.evaluate(() => document.fonts && document.fonts.ready ? document.fonts.ready : null);
      // Settle layout (two consecutive frames identical for nav-right).
      await page.waitForFunction(() => {
        const el = document.querySelector('.top-nav .nav-right');
        if (!el) return false;
        const r1 = el.getBoundingClientRect();
        return new Promise((resolve) => {
          requestAnimationFrame(() => requestAnimationFrame(() => {
            const r2 = el.getBoundingClientRect();
            resolve(r1.right === r2.right && r1.left === r2.left);
          }));
        });
      }, null, { timeout: 5000 });
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));

      const data = await page.evaluate((route) => {
        const links = Array.from(document.querySelectorAll('.nav-links .nav-link'));
        let activeHref = null;
        let activeOverflowed = false;
        let activeWidth = 0;
        const visibleHighPri = [];
        const overflowedHighPri = [];
        for (const a of links) {
          const href = a.getAttribute('href');
          const isActive = a.classList.contains('active');
          const isOverflow = a.classList.contains('is-overflow');
          const w = a.getBoundingClientRect().width;
          if (isActive) {
            activeHref = href;
            activeOverflowed = isOverflow;
            activeWidth = w;
          }
          if (a.dataset.priority === 'high') {
            if (isOverflow || w === 0) overflowedHighPri.push({ href, isOverflow, w });
            else visibleHighPri.push(href);
          }
        }
        // Open More dropdown and capture its items (clones live in
        // .nav-more-menu, the originals stay in .nav-links).
        const moreBtn = document.getElementById('navMoreBtn');
        const moreWrap = document.querySelector('.nav-more-wrap');
        const moreMenu = document.getElementById('navMoreMenu');
        const moreVisible = moreWrap && !moreWrap.classList.contains('is-hidden');
        const moreItems = moreMenu
          ? Array.from(moreMenu.querySelectorAll('.nav-link')).map(a => a.getAttribute('href'))
          : [];
        // Every inline-overflowed link must appear in the More dropdown
        // (otherwise it's unreachable).
        const overflowedHrefs = links
          .filter(a => a.classList.contains('is-overflow'))
          .map(a => a.getAttribute('href'));
        const missingFromMore = overflowedHrefs.filter(h => !moreItems.includes(h));
        // #1396 / #1400: capture the nav strip's vertical position to
        // detect the min-height overflow bug (strip rendered at negative y,
        // clipped invisible by top-nav overflow:hidden).
        const navLinksRect = document.querySelector('.top-nav .nav-links')?.getBoundingClientRect();
        return {
          activeHref, activeOverflowed, activeWidth,
          visibleHighPri, overflowedHighPri,
          moreVisible, moreItems, overflowedHrefs, missingFromMore,
          navLinksTop: navLinksRect ? navLinksRect.top : null,
        };
      }, route);

      const tag = `${w}px @ ${route}`;
      const expectedActive = route;

      try {
        // (1) Active pill is correctly identified and present inline.
        assert.strictEqual(
          data.activeHref, expectedActive,
          `${tag}: expected active=${expectedActive}, got ${data.activeHref}`
        );
        assert.strictEqual(
          data.activeOverflowed, false,
          `${tag}: active-route pill ${expectedActive} MUST NOT be in overflow ` +
          `(was overflowed=${data.activeOverflowed}, width=${data.activeWidth})`
        );
        assert.ok(
          data.activeWidth > 0,
          `${tag}: active-route pill ${expectedActive} must have non-zero width inline ` +
          `(got width=${data.activeWidth})`
        );

        // (2) All high-priority links must be inline (regression guard for #1311).
        assert.deepStrictEqual(
          [...data.visibleHighPri].sort(),
          [...HIGH_PRIORITY_HREFS].sort(),
          `${tag}: expected all 5 high-pri inline, got [${data.visibleHighPri.join(', ')}] ` +
          `overflowed=[${data.overflowedHighPri.map(o => o.href).join(', ')}]`
        );

        // (3) Every overflowed link is reachable via the More dropdown
        //     (no orphaned overflow links).
        assert.deepStrictEqual(
          data.missingFromMore, [],
          `${tag}: overflowed links missing from More dropdown: [${data.missingFromMore.join(', ')}] ` +
          `(more=[${data.moreItems.join(', ')}])`
        );

        // (4) #1396 / #1400: .nav-links must not be clipped above the viewport.
        // The original bug had the strip at y ≈ -57, invisible behind top-nav
        // overflow:hidden. Allow 0.5px sub-pixel rounding tolerance.
        assert.ok(
          data.navLinksTop !== null && data.navLinksTop > -1,
          `${tag}: .nav-links is clipped above viewport (top=${data.navLinksTop}); ` +
          `root cause was min-height:48px overflowing the 52px top-nav (#1400)`
        );

        // (5) #1396: in the ≤1100px force-collapse band, More must contain
        // EXACTLY the non-active non-high routes so the channels link (when
        // active) stays inline and is not orphaned in the dropdown.
        if (w <= 1100) {
          // Every nav-link in index.html WITHOUT data-priority="high". Adding a
          // nav entry means updating this list too, or the exactly-equals
          // assertion below fails on the new route. That is the fourth place a
          // nav link has to be registered, after index.html, bottom-nav.js
          // MORE_ROUTES and nav-drawer.js ROUTES.
          const ALL_NON_HIGH = ['#/channels', '#/tools', '#/observers', '#/analytics', '#/perf', '#/audio-lab', '#/scope-audit'];
          const expectedMore = ALL_NON_HIGH.filter(h => h !== expectedActive).sort();
          assert.deepStrictEqual(
            [...data.moreItems].sort(),
            expectedMore,
            `${tag}: More must contain exactly [${expectedMore.join(', ')}], ` +
            `got [${data.moreItems.join(', ')}]`
          );
        }

        passes++;
        console.log(`  ✅ ${tag}: active inline + ${data.visibleHighPri.length}/5 high-pri inline + ` +
                    `More has ${data.moreItems.length} item(s) + strip top=${data.navLinksTop?.toFixed(1)}`);
      } catch (e) {
        failures++;
        console.log(`  ❌ ${tag}: ${e.message}`);
      }
    }
  }

  await browser.close();
  const total = WIDTHS.length * NON_HIGH_ROUTES.length;
  console.log(`\ntest-nav-priority-1391-e2e.js: ${failures === 0 ? 'OK' : 'FAIL'} — ${passes}/${total} passed`);
  process.exit(failures === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error('test-nav-priority-1391-e2e.js: fatal', err);
  process.exit(1);
});
