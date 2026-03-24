// After Playwright tests, this script:
// 1. Connects to the running test server
// 2. Exercises frontend interactions to maximize code coverage
// 3. Extracts window.__coverage__ from the browser
// 4. Writes it to .nyc_output/ for merging

const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

async function collectCoverage() {
  const browser = await chromium.launch({
    executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium',
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    headless: true
  });
  const page = await browser.newPage();
  page.setDefaultTimeout(10000);
  const BASE = process.env.BASE_URL || 'http://localhost:13581';

  // Helper: safe click
  async function safeClick(selector, timeout) {
    try {
      await page.click(selector, { timeout: timeout || 3000 });
      await page.waitForTimeout(400);
    } catch {}
  }

  // Helper: safe fill
  async function safeFill(selector, text) {
    try {
      await page.fill(selector, text);
      await page.waitForTimeout(400);
    } catch {}
  }

  // Helper: safe select
  async function safeSelect(selector, value) {
    try {
      await page.selectOption(selector, value);
      await page.waitForTimeout(400);
    } catch {}
  }

  // ── HOME PAGE ──
  console.log('  [coverage] Home page...');
  await page.goto(`${BASE}/#/home`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1500);
  // Click onboarding buttons
  await safeClick('#chooseNew');
  await page.waitForTimeout(800);
  // Click FAQ items if present
  const faqItems = await page.$$('.faq-q, .question, [class*="accordion"]').catch(() => []);
  for (let i = 0; i < Math.min(faqItems.length, 3); i++) {
    try { await faqItems[i].click(); await page.waitForTimeout(300); } catch {}
  }
  // Click cards
  const cards = await page.$$('.card, .health-card, [class*="card"]').catch(() => []);
  for (let i = 0; i < Math.min(cards.length, 3); i++) {
    try { await cards[i].click(); await page.waitForTimeout(300); } catch {}
  }
  // Toggle level
  await safeClick('#toggleLevel');
  await page.waitForTimeout(500);
  // Go back to home and choose experienced
  await page.goto(`${BASE}/#/home`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1000);
  await safeClick('#chooseExp');
  await page.waitForTimeout(1000);
  // Click journey timeline items
  const timelineItems = await page.$$('.timeline-item, [class*="journey"]').catch(() => []);
  for (let i = 0; i < Math.min(timelineItems.length, 5); i++) {
    try { await timelineItems[i].click(); await page.waitForTimeout(300); } catch {}
  }

  // ── NODES PAGE ──
  console.log('  [coverage] Nodes page...');
  await page.goto(`${BASE}/#/nodes`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(2000);

  // Click column headers to sort
  const headers = await page.$$('th');
  for (let i = 0; i < Math.min(headers.length, 6); i++) {
    try { await headers[i].click(); await page.waitForTimeout(300); } catch {}
  }
  // Click first header again for reverse sort
  if (headers.length > 0) try { await headers[0].click(); await page.waitForTimeout(300); } catch {}

  // Click role tabs using data-tab attribute
  const roleTabs = await page.$$('.node-tab[data-tab]');
  for (const tab of roleTabs) {
    try { await tab.click(); await page.waitForTimeout(600); } catch {}
  }

  // Status filter buttons
  const statusBtns = await page.$$('#nodeStatusFilter .btn, [data-status]');
  for (const btn of statusBtns) {
    try { await btn.click(); await page.waitForTimeout(400); } catch {}
  }

  // Use search box
  await safeFill('#nodeSearch', 'test');
  await page.waitForTimeout(500);
  await safeFill('#nodeSearch', '');
  await page.waitForTimeout(300);

  // Use dropdowns (Last Heard, etc.)
  const selects = await page.$$('select');
  for (const sel of selects) {
    try {
      const options = await sel.$$eval('option', opts => opts.map(o => o.value));
      if (options.length > 1) {
        await sel.selectOption(options[1]);
        await page.waitForTimeout(400);
        if (options.length > 2) {
          await sel.selectOption(options[2]);
          await page.waitForTimeout(400);
        }
        await sel.selectOption(options[0]);
        await page.waitForTimeout(300);
      }
    } catch {}
  }

  // Click node rows to open side pane
  const nodeRows = await page.$$('table tbody tr');
  for (let i = 0; i < Math.min(nodeRows.length, 3); i++) {
    try { await nodeRows[i].click(); await page.waitForTimeout(600); } catch {}
  }

  // Click Details link in side pane
  await safeClick('a[href*="node/"]', 2000);
  await page.waitForTimeout(1500);

  // If on node detail page, interact with it
  try {
    // Click tabs on detail page if any
    const detailTabs = await page.$$('.tab-btn, [data-tab]');
    for (const tab of detailTabs) {
      try { await tab.click(); await page.waitForTimeout(400); } catch {}
    }
  } catch {}

  // Go back to nodes
  await page.goto(`${BASE}/#/nodes`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1500);

  // ── PACKETS PAGE ──
  console.log('  [coverage] Packets page...');
  await page.goto(`${BASE}/#/packets`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(2000);

  // Type filter expressions
  const filterInput = await page.$('#packetFilterInput');
  if (filterInput) {
    const filters = ['type == ADVERT', 'type == GRP_TXT', 'hops > 1', 'type == GRP_TXT && hops > 1', 'rssi < -80', 'snr > 5', 'type == TXT_MSG', ''];
    for (const f of filters) {
      try {
        await filterInput.fill(f);
        await page.waitForTimeout(600);
      } catch {}
    }
  }

  // Click Group by Hash button
  await safeClick('#fGroup');
  await page.waitForTimeout(800);

  // Click packet rows (group headers)
  const packetRows = await page.$$('table tbody tr');
  for (let i = 0; i < Math.min(packetRows.length, 5); i++) {
    try { await packetRows[i].click(); await page.waitForTimeout(500); } catch {}
  }

  // Toggle group off
  await safeClick('#fGroup');
  await page.waitForTimeout(800);

  // Change time window dropdown
  const pktSelects = await page.$$('select');
  for (const sel of pktSelects) {
    try {
      const options = await sel.$$eval('option', opts => opts.map(o => ({ value: o.value, text: o.textContent })));
      const isTimeSelect = options.some(o => o.text.match(/hour|min|day|all|time/i));
      if (isTimeSelect) {
        for (let i = 0; i < Math.min(options.length, 4); i++) {
          await sel.selectOption(options[i].value);
          await page.waitForTimeout(500);
        }
      }
    } catch {}
  }

  // ── MAP PAGE ──
  console.log('  [coverage] Map page...');
  await page.goto(`${BASE}/#/map`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(3000);

  // Click markers
  const markers = await page.$$('.leaflet-marker-icon, .leaflet-interactive');
  for (let i = 0; i < Math.min(markers.length, 3); i++) {
    try { await markers[i].click(); await page.waitForTimeout(500); } catch {}
  }

  // Toggle filter checkboxes in legend
  const checkboxes = await page.$$('input[type="checkbox"]');
  for (const cb of checkboxes) {
    try {
      await cb.click(); await page.waitForTimeout(300);
      await cb.click(); await page.waitForTimeout(300);
    } catch {}
  }

  // Toggle dark mode while on map
  await safeClick('#darkModeToggle');
  await page.waitForTimeout(800);
  await safeClick('#darkModeToggle');
  await page.waitForTimeout(500);

  // ── ANALYTICS PAGE ──
  console.log('  [coverage] Analytics page...');
  await page.goto(`${BASE}/#/analytics`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(3000); // wait for data to load

  // Click ALL analytics tabs using specific selector
  const tabNames = ['overview', 'rf', 'topology', 'channels', 'hashsizes', 'collisions', 'subpaths', 'nodes', 'distance'];
  for (const tabName of tabNames) {
    try {
      await page.click(`#analyticsTabs [data-tab="${tabName}"]`, { timeout: 2000 });
      await page.waitForTimeout(1200); // give renderTab time
    } catch {}
  }

  // Also test deep-link tabs
  for (const tab of ['collisions', 'rf', 'distance', 'topology', 'nodes', 'subpaths']) {
    await page.goto(`${BASE}/#/analytics?tab=${tab}`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
    await page.waitForTimeout(2000);
  }

  // ── CUSTOMIZE ──
  console.log('  [coverage] Customizer...');
  await page.goto(BASE, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(500);
  await safeClick('#customizeToggle');
  await page.waitForTimeout(1000);

  // Click each customizer tab using specific selector
  const custTabs = await page.$$('.cust-tab[data-tab]');
  for (const tab of custTabs) {
    try { await tab.click(); await page.waitForTimeout(500); } catch {}
  }

  // Click preset theme buttons
  const presets = await page.$$('.cust-preset-btn[data-preset]');
  for (let i = 0; i < Math.min(presets.length, 4); i++) {
    try { await presets[i].click(); await page.waitForTimeout(400); } catch {}
  }

  // Change a color input
  const colorInputs = await page.$$('input[type="color"]');
  for (let i = 0; i < Math.min(colorInputs.length, 3); i++) {
    try {
      await colorInputs[i].evaluate(el => { el.value = '#ff0000'; el.dispatchEvent(new Event('input', {bubbles:true})); });
      await page.waitForTimeout(300);
    } catch {}
  }

  // Reset preview
  await safeClick('#custResetPreview');
  await page.waitForTimeout(400);

  // Reset user theme
  await safeClick('#custResetUser');
  await page.waitForTimeout(400);

  // Close customizer
  await safeClick('#customizeToggle');

  // ── CHANNELS PAGE ──
  console.log('  [coverage] Channels page...');
  await page.goto(`${BASE}/#/channels`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(2000);
  // Click any channel items
  const channelItems = await page.$$('.channel-item, .channel-row, .channel-card, tr td');
  if (channelItems.length > 0) {
    try { await channelItems[0].click(); await page.waitForTimeout(600); } catch {}
  }

  // ── LIVE PAGE ──
  console.log('  [coverage] Live page...');
  await page.goto(`${BASE}/#/live`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(3000);
  // Click live controls if any
  const liveButtons = await page.$$('button');
  for (const btn of liveButtons) {
    const text = await btn.textContent().catch(() => '');
    if (text.match(/pause|Pause|resume|Resume|clear|Clear|vcr|VCR/i)) {
      try { await btn.click(); await page.waitForTimeout(400); } catch {}
    }
  }

  // ── TRACES PAGE ──
  console.log('  [coverage] Traces page...');
  await page.goto(`${BASE}/#/traces`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(2000);

  // ── OBSERVERS PAGE ──
  console.log('  [coverage] Observers page...');
  await page.goto(`${BASE}/#/observers`, { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(2000);
  // Click observer rows
  const obsRows = await page.$$('table tbody tr, .observer-card, .observer-row');
  for (let i = 0; i < Math.min(obsRows.length, 2); i++) {
    try { await obsRows[i].click(); await page.waitForTimeout(500); } catch {}
  }

  // ── GLOBAL SEARCH ──
  console.log('  [coverage] Global search...');
  await safeClick('#searchToggle');
  await page.waitForTimeout(500);
  await safeFill('#searchInput', 'test');
  await page.waitForTimeout(800);
  await safeFill('#searchInput', '');
  await page.keyboard.press('Escape').catch(() => {});
  await page.waitForTimeout(300);

  // ── FAVORITES ──
  await safeClick('#favToggle');
  await page.waitForTimeout(400);
  await safeClick('#favToggle');
  await page.waitForTimeout(300);

  // ── DARK MODE TOGGLE ──
  await safeClick('#darkModeToggle');
  await page.waitForTimeout(500);

  // ── KEYBOARD SHORTCUT (Ctrl+K for search) ──
  try {
    await page.keyboard.press('Control+k');
    await page.waitForTimeout(500);
    await page.keyboard.press('Escape');
    await page.waitForTimeout(300);
  } catch {}

  // ── Navigate via nav links to exercise router ──
  console.log('  [coverage] Nav link navigation...');
  const routes = ['home', 'packets', 'map', 'live', 'channels', 'nodes', 'traces', 'observers', 'analytics', 'perf'];
  for (const route of routes) {
    await safeClick(`a[data-route="${route}"]`);
    await page.waitForTimeout(1000);
  }

  // Extract coverage
  const coverage = await page.evaluate(() => window.__coverage__);
  await browser.close();

  if (coverage) {
    const outDir = path.join(__dirname, '..', '.nyc_output');
    if (!fs.existsSync(outDir)) fs.mkdirSync(outDir, { recursive: true });
    fs.writeFileSync(path.join(outDir, 'frontend-coverage.json'), JSON.stringify(coverage));
    console.log('Frontend coverage collected: ' + Object.keys(coverage).length + ' files');
  } else {
    console.log('WARNING: No __coverage__ object found — instrumentation may have failed');
  }
}

collectCoverage().catch(e => { console.error(e); process.exit(1); });
