/**
 * #1281 — Packet detail Location row + 📍map link contrast.
 *
 * Bug:
 *   A) <dt>Location</dt><dd>—</dd> renders unconditionally on every packet,
 *      wasting a row on ~90% of packet types (only ADVERT carries unencrypted
 *      transmitter GPS).
 *   B) The trailing `📍map` link has no class/color → inherits UA-default <a>
 *      blue → unreadable in dark mode.
 *
 * Asserts:
 *   1. Some non-ADVERT packet detail does NOT contain <dt>Location</dt>.
 *   2. Some ADVERT packet detail DOES contain <dt>Location</dt> with coords.
 *   3. The 📍map link uses class="loc-map-link" with a themed link color
 *      (was --accent pre-M5; now --link-color after #1668/PR #1696 AA-contrast
 *      fix). The hard guarantee is: NOT the default UA blue rgb(0,0,238).
 *   4. #1149: node side/full stats omit aggregate SNR, retaining Heard By
 *      readings on desktop and mobile. Node API fixtures keep SNR non-null.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-issue-1281-location-row-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

function normRgb(s) {
  const m = s && s.match(/rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/);
  if (!m) return null;
  return `rgb(${m[1]}, ${m[2]}, ${m[3]})`;
}

async function gotoPackets(page) {
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => {
    localStorage.removeItem('meshcore-groupbyhash');
    localStorage.setItem('meshcore-time-window', '525600');
  });
  await page.reload({ waitUntil: 'load' });
  await page.waitForSelector('table tbody tr[data-hash]', { timeout: 15000 });
}

// Click rows until detail pane's Payload Type matches `wantType` (e.g. "Advert"
// or any non-"Advert"). Returns true on hit, false if exhausted.
async function findPacketDetailByType(page, predicate, maxRows = 40) {
  await page.waitForTimeout(400);
  const rows = await page.$$('table tbody tr[data-hash][data-action]');
  for (let i = 0; i < Math.min(rows.length, maxRows); i++) {
    await rows[i].click({ timeout: 3000 }).catch(() => null);
    await page.waitForTimeout(350);
    const meta = await page.evaluate(() => {
      const dts = document.querySelectorAll('dl.detail-meta dt');
      let typeName = null;
      let hasLocation = false;
      let locationText = '';
      for (const dt of dts) {
        const label = dt.textContent.trim();
        const dd = dt.nextElementSibling;
        if (label === 'Payload Type') typeName = dd ? dd.textContent.trim() : null;
        if (label === 'Location') { hasLocation = true; locationText = dd ? dd.textContent.trim() : ''; }
      }
      return { typeName, hasLocation, locationText };
    });
    if (predicate(meta)) return meta;
  }
  return null;
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium',
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log(`\n=== #1281 Location row + map link contrast E2E against ${BASE} ===`);

  await step('Non-ADVERT packet detail does NOT render <dt>Location</dt>', async () => {
    await gotoPackets(page);
    // Filter to a non-ADVERT type to make the search efficient.
    const meta = await findPacketDetailByType(
      page,
      (m) => m.typeName && m.typeName !== 'Advert',
      40
    );
    assert(meta, 'No non-ADVERT packet found in first 40 rows');
    assert(!meta.hasLocation,
      `Expected NO <dt>Location</dt> for type "${meta.typeName}", but found one with text "${meta.locationText}"`);
  });

  await step('ADVERT packet detail STILL renders <dt>Location</dt> with GPS coords', async () => {
    await gotoPackets(page);
    // Filter UI to ADVERTs to guarantee we find one.
    const fInput = await page.$('#packetFilterInput');
    if (fInput) {
      await fInput.fill('type == ADVERT');
      await page.keyboard.press('Enter');
      await page.waitForTimeout(600);
    }
    const meta = await findPacketDetailByType(
      page,
      (m) => m.typeName === 'Advert' && m.hasLocation,
      40
    );
    assert(meta, 'No ADVERT packet with Location row found in first 40 ADVERT rows');
    assert(/-?\d+\.\d+\s*,\s*-?\d+\.\d+/.test(meta.locationText),
      `ADVERT Location should contain GPS coords, got: "${meta.locationText}"`);
  });

  await step('📍map link uses class="loc-map-link" with themed link color (not UA default blue)', async () => {
    // Reuse the ADVERT detail pane left open from the previous step.
    //
    // History: original #1281 asserted color === var(--accent) (rgb(74,158,255)).
    // M5 axe/WCAG-AA fixes (#1668, PR #1696) rewired link text away from --accent
    // because --accent (#4a9eff) fails AA contrast for body-text links on most
    // surfaces. .loc-map-link now resolves via --link-color (= --palette-blue-600
    // = #2563eb), which passes AA. Test author intent ("links use a token, not
    // raw UA blue") is satisfied by either token; we now track --link-color to
    // match the CSS rule and preserve the "not UA default blue" guard.
    const result = await page.evaluate(() => {
      const link = document.querySelector('dl.detail-meta a.loc-map-link');
      if (!link) return { missing: true };
      const cs = getComputedStyle(link);
      const linkColorRaw = getComputedStyle(document.documentElement).getPropertyValue('--link-color').trim();
      // Resolve --link-color value to its computed rgb() via a probe element.
      const probe = document.createElement('span');
      probe.style.color = `var(--link-color)`;
      document.body.appendChild(probe);
      const linkColorRgb = getComputedStyle(probe).color;
      probe.remove();
      return {
        linkColor: cs.color,
        tokenRgb: linkColorRgb,
        tokenRaw: linkColorRaw,
        href: link.getAttribute('href'),
        text: link.textContent.trim(),
      };
    });
    assert(!result.missing,
      '<a class="loc-map-link"> not found in detail pane — implementation must apply the class');
    const link = normRgb(result.linkColor);
    const token = normRgb(result.tokenRgb);
    console.log(`    link.color=${result.linkColor}  --link-color→${result.tokenRgb} (raw "${result.tokenRaw}")`);
    assert(link === token,
      `📍map link color ${result.linkColor} must equal var(--link-color) (${result.tokenRgb}); ` +
      `default UA blue (rgb(0, 0, 238)) is not acceptable`);
    assert(link !== 'rgb(0, 0, 238)',
      'Link color is UA-default blue — class is missing or CSS rule does not match');
  });

  const pubkey = 'ab'.repeat(32);
  const now = new Date().toISOString();
  const node = {
    public_key: pubkey, name: 'SNR fixture node', role: 'repeater',
    first_seen: now, last_seen: now, advert_count: 1005,
  };
  const health = {
    stats: { totalPackets: 1005, packetsToday: 7, avgSnr: 6.3, avgHops: 2, lastHeard: now },
    observers: [
      { observer_id: 'observer-a', observer_name: 'Observer A', packetCount: 1000, avgSnr: 12.5, avgRssi: -45 },
      { observer_id: 'observer-b', observer_name: 'Observer B', packetCount: 5, avgSnr: -8.5, avgRssi: -110 },
    ],
    recentPackets: [],
  };

  for (const view of [
    { name: 'desktop side panel', width: 1280, full: false },
    { name: 'desktop full detail', width: 1280, full: true },
    { name: 'mobile full detail', width: 390, full: true },
  ]) {
    await step('#1149 ' + view.name + ' retains observer SNR without an aggregate headline', async () => {
      const nodePage = await browser.newPage({ viewport: { width: view.width, height: 900 } });
      try {
        await nodePage.route('**/api/nodes**', async (route) => {
          const pathname = new URL(route.request().url()).pathname;
          const fixtures = {
            '/api/nodes': { nodes: [node], total: 1, counts: { repeaters: 1 } },
            '/api/nodes/clock-skew': [],
            ['/api/nodes/' + pubkey]: { node, recentAdverts: [] },
            ['/api/nodes/' + pubkey + '/health']: health,
            ['/api/nodes/' + pubkey + '/neighbors']: { neighbors: [] },
            ['/api/nodes/' + pubkey + '/paths']: { paths: [] },
            ['/api/nodes/' + pubkey + '/clock-skew']: {},
          };
          if (Object.prototype.hasOwnProperty.call(fixtures, pathname)) {
            await route.fulfill({ json: fixtures[pathname] });
          } else {
            await route.continue();
          }
        });
        await nodePage.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
        await nodePage.locator('#nodesBody tr[data-key="' + pubkey + '"]').click();
        if (view.full && view.width > 640) {
          await nodePage.locator('#nodesRight .node-detail-btn').click();
        }

        const stats = nodePage.locator(view.full ? '#node-stats' : '#nodesRight .detail-meta');
        const readings = nodePage.locator(view.full ? '#node-observers tbody tr' : '#nodesRight .observer-row');
        await readings.first().waitFor({ state: 'visible' });
        assert(await readings.count() === 2, 'Heard By must retain both observers');
        for (const observer of health.observers) {
          const row = readings.filter({ hasText: observer.observer_name });
          const text = (await row.innerText()).replace(/\s+/g, ' ');
          const snr = observer.avgSnr.toFixed(1) + (view.full ? ' dB' : 'dB');
          assert(text.includes(snr), observer.observer_name + ' must retain SNR ' + snr + ', got: ' + text);
        }
        for (const [label, value] of [['Total Packets', '1005'], ['Packets Today', '7'], ['Avg Hops', '2']]) {
          const metric = stats.getByText(label, { exact: true });
          const actual = await metric.evaluate(el => el.nextElementSibling.textContent.trim());
          assert(actual === value, label + ' must remain ' + value + ', got: ' + actual);
        }
        const headlineCount = await stats.getByText('Avg SNR', { exact: true }).count();
        assert(headlineCount === 0,
          'Expected no aggregate Avg SNR headline with stats.avgSnr=6.3, got ' + headlineCount);
        if (process.env.SCREENSHOT_DIR) {
          await stats.scrollIntoViewIfNeeded();
          await nodePage.screenshot({
            path: require('path').join(process.env.SCREENSHOT_DIR, 'issue-1149-' + view.name.replace(/ /g, '-') + '.png'),
            fullPage: true,
          });
        }
      } finally {
        await nodePage.close();
      }
    });
  }

  await browser.close();

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(1); });
