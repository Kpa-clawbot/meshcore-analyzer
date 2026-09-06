// E2E for the Nodes JSON export button (#/nodes → "Export JSON").
// Defaults to localhost:3000 — NEVER point at prod (AGENTS.md). CI sets BASE_URL.
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

const REF_KEYS = ['type', 'name', 'custom_name', 'public_key', 'flags', 'latitude',
  'longitude', 'last_advert', 'last_modified', 'out_path_list'];

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ acceptDownloads: true });

  await page.goto(BASE + '/#/nodes');
  await page.waitForSelector('#nodesLeft[data-loaded="true"]', { timeout: 30000 });

  const btn = await page.$('#nodesExportBtn');
  if (!btn) throw new Error('#nodesExportBtn is missing from the nodes topbar');

  const label = (await btn.textContent()).trim();
  if (await btn.isDisabled()) {
    // No exportable node in this dataset (all lack a name or a position).
    if (label !== 'Export JSON') {
      throw new Error('disabled button should carry no count, got "' + label + '"');
    }
    console.log('nodes-export E2E SKIP (no exportable node in dataset)');
    await browser.close();
    return;
  }

  const m = label.match(/^Export JSON \((\d+)\)$/);
  if (!m) throw new Error('enabled button must show the contact count, got "' + label + '"');
  const expectedCount = Number(m[1]);

  const [download] = await Promise.all([
    page.waitForEvent('download', { timeout: 15000 }),
    btn.click(),
  ]);

  const name = download.suggestedFilename();
  if (!/^corescope_nodes_[A-Za-z0-9_-]+_\d{4}-\d{2}-\d{2}-\d{6}\.json$/.test(name)) {
    throw new Error('unexpected download filename: ' + name);
  }

  const stream = await download.createReadStream();
  let raw = '';
  for await (const chunk of stream) raw += chunk;
  const payload = JSON.parse(raw);

  if (!Array.isArray(payload.contacts)) throw new Error('export must have a contacts array');
  if (payload.contacts.length !== expectedCount) {
    throw new Error('button said ' + expectedCount + ' contacts, file has ' + payload.contacts.length);
  }
  if (Object.keys(payload).length !== 1) {
    throw new Error('export must contain only the contacts key, got ' + Object.keys(payload).join(','));
  }

  payload.contacts.forEach(function (c, i) {
    const keys = Object.keys(c).join(',');
    if (keys !== REF_KEYS.join(',')) {
      throw new Error('contact ' + i + ' key mismatch: ' + keys);
    }
    if (typeof c.public_key !== 'string' || c.public_key.length < 64) {
      throw new Error('contact ' + i + ' has a truncated public_key');
    }
    if (typeof c.latitude !== 'string' || typeof c.longitude !== 'string') {
      throw new Error('contact ' + i + ' coordinates must be strings');
    }
    if (c.latitude === '0' && c.longitude === '0') {
      throw new Error('contact ' + i + ' is at null island and should have been skipped');
    }
    if (typeof c.last_advert !== 'number' || typeof c.type !== 'number') {
      throw new Error('contact ' + i + ' last_advert/type must be numbers');
    }
  });

  // The export is WYSIWYG: narrowing the table with the search box must narrow
  // the export too.
  const first = payload.contacts[0].name;
  await page.fill('#nodeSearch', first);
  await page.waitForFunction(function (want) {
    var b = document.getElementById('nodesExportBtn');
    return b && b.textContent.trim() !== want;
  }, label, { timeout: 15000 });
  const narrowed = (await page.textContent('#nodesExportBtn')).trim();
  const nm = narrowed.match(/^Export JSON \((\d+)\)$/);
  if (!nm || Number(nm[1]) >= expectedCount) {
    throw new Error('search should shrink the export set, got "' + narrowed + '" vs ' + expectedCount);
  }

  console.log('nodes-export E2E OK (' + expectedCount + ' contacts, ' + name + ')');
  await browser.close();
})();
