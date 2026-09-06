/**
 * Playwright E2E — Scope column on the Packets tab.
 *
 * transmissions.scope_name has three states and the column must render each one
 * distinguishably: em dash (not transport-scoped), muted "unknown"
 * (transport-scoped, region unmatched) and the region name on a match.
 *
 * Usage: node test-packets-scope-column.js
 *        BASE_URL=https://staging.on8ar.eu node test-packets-scope-column.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const results = [];

async function test(name, fn) {
  try {
    await fn();
    results.push({ name, pass: true });
    console.log(`  ✅ ${name}`);
  } catch (err) {
    results.push({ name, pass: false, error: err.message });
    console.log(`  ❌ ${name}: ${err.message}`);
  }
}

function assert(condition, msg) {
  if (!condition) throw new Error(msg || 'Assertion failed');
}

async function gotoPackets(page) {
  await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => {
    localStorage.removeItem('packets-visible-cols');
    localStorage.removeItem('packets-known-cols');
  });
  await page.reload({ waitUntil: 'networkidle' });
  await page.waitForSelector('#pktTable tbody tr:not([id^=vscroll])', { timeout: 30000 });
}

(async () => {
  console.log(`\nPackets Scope column — ${BASE}\n`);
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });

  await gotoPackets(page);

  await test('Scope header sits between Type and Observer', async () => {
    const headers = await page.$$eval('#pktTable thead th', ths =>
      ths.map(th => th.textContent.trim()));
    const iType = headers.indexOf('Type');
    const iScope = headers.indexOf('Scope');
    const iObserver = headers.indexOf('Observer');
    assert(iScope !== -1, 'Scope header missing, got: ' + headers.join('|'));
    assert(iType < iScope && iScope < iObserver,
      `expected Type < Scope < Observer, got ${iType} < ${iScope} < ${iObserver}`);
  });

  await test('Scope column is visible by default', async () => {
    const hidden = await page.$eval('#pktTable', t => t.classList.contains('hide-col-scope'));
    assert(!hidden, 'table carries hide-col-scope on a fresh visit');
    const cellCount = await page.$$eval('#pktTable tbody td.col-scope', tds => tds.length);
    assert(cellCount > 0, 'no td.col-scope rendered');
  });

  await test('every row renders exactly one scope cell', async () => {
    const { rows, cells } = await page.evaluate(() => {
      const trs = Array.from(document.querySelectorAll('#pktTable tbody tr'))
        .filter(tr => !tr.id.startsWith('vscroll') && tr.querySelector('td.col-type'));
      return {
        rows: trs.length,
        cells: trs.filter(tr => tr.querySelectorAll('td.col-scope').length === 1).length,
      };
    });
    assert(rows > 0, 'no packet rows found');
    assert(rows === cells, `${rows} rows but ${cells} have exactly one scope cell`);
  });

  await test('non-transport rows render an em dash', async () => {
    const found = await page.evaluate(() => {
      for (const tr of document.querySelectorAll('#pktTable tbody tr')) {
        const type = tr.querySelector('td.col-type');
        const scope = tr.querySelector('td.col-scope');
        if (!type || !scope) continue;
        // No T badge → FLOOD or DIRECT → no transport scope possible.
        if (!type.querySelector('.badge-transport')) return scope.textContent.trim();
      }
      return null;
    });
    assert(found !== null, 'no non-transport row on screen to check');
    assert(found === '—', `expected em dash, got "${found}"`);
  });

  await test('sorting by Scope pins the empties last', async () => {
    await page.click('#pktTable thead th.col-scope');
    await page.waitForTimeout(600);
    const values = await page.$$eval('#pktTable tbody td.col-scope', tds =>
      tds.map(td => td.textContent.trim()));
    const lastScoped = values.reduce((acc, v, i) => (v !== '—' ? i : acc), -1);
    const firstEmpty = values.indexOf('—');
    if (lastScoped === -1 || firstEmpty === -1) {
      console.log('    (only one scope state on screen, ordering not exercised)');
      return;
    }
    assert(firstEmpty > lastScoped,
      `em dashes must follow every scoped row; first dash at ${firstEmpty}, last scoped at ${lastScoped}`);
  });

  await test('Columns menu can hide and restore the Scope column', async () => {
    // A checkbox click bubbles to the document handler that closes the menu, so
    // each toggle needs its own open.
    const toggleScope = async () => {
      await page.click('#colToggleBtn');
      await page.waitForTimeout(200);
      const box = await page.$('#colToggleMenu input[data-col="scope"]');
      assert(box, 'no Scope checkbox in the Columns menu');
      const wasChecked = await box.isChecked();
      await box.click();
      await page.waitForTimeout(300);
      return wasChecked;
    };

    assert(await toggleScope(), 'Scope checkbox should start checked');
    assert(await page.$eval('#pktTable', t => t.classList.contains('hide-col-scope')),
      'unchecking should add hide-col-scope');
    assert(!(await toggleScope()), 'Scope checkbox should now be unchecked');
    assert(!(await page.$eval('#pktTable', t => t.classList.contains('hide-col-scope'))),
      're-checking should remove hide-col-scope');
  });

  await test('a column added after the visitor saved prefs arrives visible', async () => {
    // Simulate a returning visitor whose stored prefs predate the Scope column.
    await page.evaluate(() => {
      localStorage.setItem('packets-visible-cols',
        JSON.stringify(['time', 'hash', 'size', 'type', 'observer', 'path', 'rpt', 'details']));
      localStorage.removeItem('packets-known-cols');
    });
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('#pktTable tbody tr:not([id^=vscroll])', { timeout: 30000 });
    assert(!(await page.$eval('#pktTable', t => t.classList.contains('hide-col-scope'))),
      'Scope should be shown for prefs saved before the column existed');
    // Region was explicitly absent from those prefs AND predates the column, so
    // the backfill must not resurrect it — only genuinely new keys get defaulted.
    assert(await page.$eval('#pktTable', t => t.classList.contains('hide-col-region')),
      'backfill must not re-enable a column the visitor had hidden');
  });

  await browser.close();

  const failed = results.filter(r => !r.pass);
  console.log(`\n=== ${results.length - failed.length} passed, ${failed.length} failed ===`);
  process.exit(failed.length ? 1 : 0);
})();
