/**
 * #1146 — "Paths Through This Node" path-link contrast E2E.
 *
 * Bug: Path entries inside the node-detail "Paths Through This Node"
 * section are rendered as <div> blocks, not a <table>. The existing
 * `.node-detail-section .data-table td a { color: var(--accent) }`
 * rule (style.css:1231) doesn't apply, so the path-hop <a> elements
 * fall back to UA-default `rgb(0,0,238)` blue. On dark theme, that
 * blue against `var(--card-bg)` (#1a1a2e) computes to ~3.0:1 — a
 * WCAG AA failure (4.5:1 required for body text).
 *
 * This test loads a node detail page, mocks the /paths API to return
 * a deterministic chain with at least one named hop, switches to dark
 * theme, then asserts the computed link colour vs. its background
 * yields a contrast ratio ≥ 4.5:1.
 *
 * #1153 also checks that the selected node has a visible enclosing marker
 * in an 18-hop chain, without marking a different node with the same prefix.
 * Covers the desktop side/full views and mobile full view in both themes.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1146-path-link-contrast-e2e.js
 * Optional SCREENSHOT_DIR saves PNG evidence; use an ignored output directory.
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname),
  'Browser regression tests must use a local server');
const SCREENSHOT_DIR = process.env.SCREENSHOT_DIR;
if (SCREENSHOT_DIR) require('fs').mkdirSync(SCREENSHOT_DIR, { recursive: true });

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// WCAG 2.1 relative luminance + contrast ratio.
function srgbToLin(c) {
  c = c / 255;
  return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
}
function lum(rgb) {
  return 0.2126 * srgbToLin(rgb[0]) + 0.7152 * srgbToLin(rgb[1]) + 0.0722 * srgbToLin(rgb[2]);
}
function contrast(fg, bg) {
  const L1 = lum(fg), L2 = lum(bg);
  const hi = Math.max(L1, L2), lo = Math.min(L1, L2);
  return (hi + 0.05) / (lo + 0.05);
}
function parseRgb(s) {
  // Accept "rgb(r, g, b)" or "rgba(r, g, b, a)".
  const m = s.match(/rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/);
  if (!m) throw new Error('Cannot parse colour: ' + s);
  return [parseInt(m[1], 10), parseInt(m[2], 10), parseInt(m[3], 10)];
}
// Walk up parent chain to find the first non-transparent backgroundColor.
async function effectiveBgFor(page, selector) {
  return await page.evaluate((sel) => {
    let el = document.querySelector(sel);
    if (!el) return null;
    while (el) {
      const cs = getComputedStyle(el);
      const bg = cs.backgroundColor;
      const m = bg && bg.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)(?:,\s*([\d.]+))?\)/);
      if (m) {
        const a = m[4] !== undefined ? parseFloat(m[4]) : 1;
        if (a > 0.01) return bg;
      }
      el = el.parentElement;
    }
    // Fallback: html background.
    return getComputedStyle(document.documentElement).backgroundColor || 'rgb(255,255,255)';
  }, selector);
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

  console.log(`\n=== #1146 path-link contrast E2E against ${BASE} ===`);

  const hopPubkey = 'a1'.repeat(32);
  const targetName = 'Selected <node> & relay';
  let selectedPubkey;
  let targetHopKey;
  let siblingKey;

  // Mock paths API for ANY node so test is deterministic.
  await page.route('**/api/nodes/*/paths*', (route) => {
    selectedPubkey = decodeURIComponent(new URL(route.request().url()).pathname.split('/')[3]);
    // Full public-key identity must survive hex casing; a shared prefix alone
    // must never qualify. Neither fixture node requires resolver changes.
    targetHopKey = selectedPubkey === selectedPubkey.toUpperCase()
      ? selectedPubkey.toLowerCase() : selectedPubkey.toUpperCase();
    siblingKey = targetHopKey.slice(0, -1) + (targetHopKey.endsWith('0') ? '1' : '0');
    const hops = Array.from({ length: 18 }, (_, i) => ({
      pubkey: hopPubkey.slice(0, -2) + i.toString(16).padStart(2, '0'),
      prefix: 'a1', name: 'Relay ' + (i + 1),
    }));
    hops[8] = { pubkey: targetHopKey, prefix: targetHopKey.slice(0, 2), name: targetName, unreliable: true, ambiguous: true };
    hops[9] = {
      pubkey: siblingKey, prefix: targetHopKey.slice(0, 2), name: 'Same-prefix sibling', ambiguous: true,
      conflicts: [
        { pubkey: targetHopKey, name: targetName, regional: true },
        { pubkey: siblingKey, name: 'Same-prefix sibling', regional: true },
      ],
    };
    hops[10] = { prefix: targetHopKey.slice(0, 2), name: 'Unresolved prefix' };
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        totalPaths: 1,
        totalTransmissions: 5,
        paths: [{
          hops,
          count: 5,
          lastSeen: new Date().toISOString(),
          sampleHash: 'deadbeef00',
        }],
      }),
    });
  });

  await step('Load nodes page and force dark theme', async () => {
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#nodesBody tr[data-key]', { timeout: 15000 });
    if (await page.locator('html').getAttribute('data-theme') !== 'dark') {
      await page.locator('#darkModeToggle').click();
    }
  });

  await step('Open side panel for first node and wait for paths', async () => {
    await page.click('#nodesBody tr[data-key]');
    await page.waitForSelector('#pathsContent', { timeout: 10000 });
    await page.waitForFunction(
      () => {
        const el = document.getElementById('pathsContent');
        return el && el.querySelector('a[href^="#/nodes/"]');
      },
      null, { timeout: 15000 }
    );
  });

  await step('Path link contrast (#pathsContent a) ≥ 4.5:1 in dark mode', async () => {
    // Use page.evaluate (single CDP call) so querySelector and getComputedStyle
    // are atomic — page.$eval splits them across two calls, leaving a window
    // where a concurrent re-render can detach the element before getComputedStyle
    // runs, causing Chromium to return '' for color.
    const linkColor = await page.evaluate(() => {
      const el = document.querySelector('#pathsContent a[href^="#/nodes/"]');
      return el ? getComputedStyle(el).color : '';
    });
    const bgColor = await effectiveBgFor(page, '#pathsContent a[href^="#/nodes/"]');
    const fg = parseRgb(linkColor);
    const bg = parseRgb(bgColor);
    const ratio = contrast(fg, bg);
    console.log(`    link=${linkColor}  bg=${bgColor}  ratio=${ratio.toFixed(2)}:1`);
    assert(ratio >= 4.5,
      `Expected contrast ≥ 4.5:1 (WCAG AA), got ${ratio.toFixed(2)}:1 ` +
      `(link ${linkColor} on ${bgColor}). The path-link <a> elements are not ` +
      `covered by the .data-table td a rule and inherit UA blue.`);
  });

  async function assertCurrentHop(selector, hasHopDisplay) {
    await page.waitForFunction((sel) => document.querySelector(sel)?.textContent.includes('Relay 18'), selector);
    const targetSelector = selector + ' a[href="#/nodes/' + targetHopKey + '"]';
    const result = await page.evaluate(({ selector, targetHopKey, siblingKey }) => {
      const container = document.querySelector(selector);
      const chain = container.firstElementChild.firstElementChild;
      const target = chain.querySelector('a[href="#/nodes/' + targetHopKey + '"]');
      const sibling = chain.querySelector('a[href="#/nodes/' + siblingKey + '"]');
      target.scrollIntoView({ block: 'center' });
      const style = getComputedStyle(target);
      const rect = target.getBoundingClientRect();
      const chainRect = chain.getBoundingClientRect();
      const outline = parseFloat(style.outlineWidth) >= 1 && style.outlineStyle === 'solid';
      const border = ['Top', 'Right', 'Bottom', 'Left'].every(side =>
        parseFloat(style['border' + side + 'Width']) >= 1 && style['border' + side + 'Style'] === 'solid');
      return {
        marked: Array.from(chain.querySelectorAll('.hop-current')).map(el => el.getAttribute('href')),
        ring: outline || border, ringColor: outline ? style.outlineColor : style.borderTopColor,
        outline, dashedBottom: style.borderBottomStyle === 'dashed' && parseFloat(style.borderBottomWidth) >= 1,
        textColor: style.color, padding: parseFloat(style.paddingLeft),
        text: target.textContent, injectedElements: target.childElementCount,
        siblingMarked: sibling.classList.contains('hop-current'),
        fits: rect.left >= chainRect.left - 1 && rect.right <= chainRect.right + 1,
        fragments: target.getClientRects().length,
        warning: !!chain.querySelector('.hop-unreliable-btn'),
        conflict: !!chain.querySelector('.hop-conflict-btn'),
        siblingAmbiguous: sibling.classList.contains('hop-ambiguous'),
        hopCount: chain.children.length - chain.querySelectorAll('button').length,
      };
    }, { selector, targetHopKey, siblingKey });
    assert(result.hopCount === 18, 'Expected all 18 hops in the rendered chain');
    assert(result.ring, 'Selected hop must have a visible enclosing border or outline, beyond bold/color');
    assert(result.marked.length === 1 && result.marked[0] === '#/nodes/' + targetHopKey,
      'Exactly the selected full public key must be marked, including mixed-case hex');
    assert(!result.siblingMarked, 'Same-prefix sibling must not be marked as this node');
    assert(result.padding > 0 && result.fits && result.fragments === 1,
      'Selected hop marker must enclose the name and fit within the wrapping chain');
    assert(result.text === targetName && result.injectedElements === 0, 'Selected hop name must stay escaped');
    const bg = parseRgb(await effectiveBgFor(page, targetSelector));
    assert(contrast(parseRgb(result.ringColor), bg) >= 3, 'Selected hop ring must contrast with the active theme');
    assert(contrast(parseRgb(result.textColor), bg) >= 4.5, 'Selected hop text must remain readable');
    if (hasHopDisplay) {
      assert(result.warning && result.conflict && result.siblingAmbiguous,
        'Shared hop rendering must preserve unreliable and ambiguous indicators');
      assert(result.outline && result.dashedBottom,
        'The selected ambiguous hop must keep its dashed bottom border alongside the enclosing outline');
    }
    if (SCREENSHOT_DIR) {
      const theme = await page.locator('html').getAttribute('data-theme');
      const filename = `${selector.slice(1)}-${page.viewportSize().width}-${theme}${hasHopDisplay ? '' : '-plain'}.png`;
      await page.locator(selector).screenshot({ path: require('path').join(SCREENSHOT_DIR, filename) });
    }
  }

  await step('#1153 desktop side panel: selected hop stands out in an 18-hop dark chain',
    () => assertCurrentHop('#pathsContent', false));

  for (const theme of ['light', 'dark']) {
    if (await page.locator('html').getAttribute('data-theme') !== theme) {
      await page.locator('#darkModeToggle').click();
    }
    await step('#1153 desktop side panel: ' + theme + ' theme', async () => {
      await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
      assert(await page.locator('html').getAttribute('data-theme') === theme, 'Expected the selected theme');
      await page.locator('#nodesBody tr[data-key]').first().click();
      await assertCurrentHop('#pathsContent', false);
    });
    await step('#1153 desktop full view: ' + theme + ' theme', async () => {
      await page.locator('#nodesRight .node-detail-btn').click();
      await assertCurrentHop('#fullPathsContent', true);
    });
  }

  for (const theme of ['light', 'dark']) {
    // The theme toggle lives in the desktop header; exercise the same theme
    // at mobile width without depending on the unrelated navigation drawer.
    await page.setViewportSize({ width: 1280, height: 900 });
    if (await page.locator('html').getAttribute('data-theme') !== theme) {
      await page.locator('#darkModeToggle').click();
    }
    await page.setViewportSize({ width: 390, height: 844 });
    await step('#1153 mobile row click opens a readable full chain: ' + theme + ' theme', async () => {
      await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
      assert(await page.locator('html').getAttribute('data-theme') === theme, 'Expected the selected theme');
      await page.locator('#nodesBody tr[data-key]').first().click();
      await assertCurrentHop('#fullPathsContent', true);
    });
  }

  await step('#1153 full-view fallback also marks only the selected node', async () => {
    await page.route('**/hop-display.js*', route => route.fulfill({ contentType: 'text/javascript', body: '' }));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await assertCurrentHop('#fullPathsContent', false);
    assert(await page.evaluate(() => !window.HopDisplay), 'Expected the real full-view fallback');
  });

  await browser.close();

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });
