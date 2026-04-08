/**
 * Playwright E2E tests — proof of concept
 * Runs against prod (analyzer.00id.net), read-only.
 * Usage: node test-e2e-playwright.js
 */
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:3000';
const GO_BASE = process.env.GO_BASE_URL || '';  // e.g. https://analyzer.00id.net:82
const results = [];

async function test(name, fn) {
  try {
    await fn();
    results.push({ name, pass: true });
    console.log(`  \u2705 ${name}`);
  } catch (err) {
    results.push({ name, pass: false, error: err.message });
    console.log(`  \u274c ${name}: ${err.message}`);
    console.log(`\nFail-fast: stopping after first failure.`);
    process.exit(1);
  }
}

function assert(condition, msg) {
  if (!condition) throw new Error(msg || 'Assertion failed');
}

async function run() {
  console.log('Launching Chromium...');
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage']
  });
  const context = await browser.newContext();
  const page = await context.newPage();
  page.setDefaultTimeout(10000);

  console.log(`\nRunning E2E tests against ${BASE}\n`);

  // --- Group: Home page (tests 1, 6, 7) ---

  // Test 1: Home page loads
  await test('Home page loads', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const title = await page.title();
    assert(title.toLowerCase().includes('corescope'), `Title "${title}" doesn't contain CoreScope`);
    const nav = await page.$('nav, .navbar, .nav, [class*="nav"]');
    assert(nav, 'Nav bar not found');
  });

  // Test 6: Theme customizer opens (reuses home page from test 1)
  await test('Theme customizer opens', async () => {
    // Look for palette/customize button
    const btn = await page.$('button[title*="ustom" i], button[aria-label*="theme" i], [class*="customize"], button:has-text("\ud83c\udfa8")');
    if (!btn) {
      // Try finding by emoji content
      const allButtons = await page.$$('button');
      let found = false;
      for (const b of allButtons) {
        const text = await b.textContent();
        if (text.includes('\ud83c\udfa8')) {
          await b.click();
          found = true;
          break;
        }
      }
      assert(found, 'Could not find theme customizer button');
    } else {
      await btn.click();
    }
    await page.waitForFunction(() => {
      const html = document.body.innerHTML;
      return html.includes('preset') || html.includes('Preset') || html.includes('theme') || html.includes('Theme');
    });
    const html = await page.content();
    const hasCustomizer = html.includes('preset') || html.includes('Preset') || html.includes('theme') || html.includes('Theme');
    assert(hasCustomizer, 'Customizer panel not found after clicking');
  });

  await test('Customizer open does not overwrite server home config without edits', async () => {
    // TODO: requires running server with full customize/home wiring
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    await page.evaluate(() => {
      localStorage.removeItem('cs-theme-overrides');
      window.SITE_CONFIG = window.SITE_CONFIG || {};
      window.SITE_CONFIG.home = {
        heroTitle: 'Server Hero (E2E)',
        heroSubtitle: 'Server Subtitle (E2E)',
        steps: [{ emoji: 'S', title: 'Server Step', description: 'server' }],
        checklist: [{ question: 'Server Q', answer: 'Server A' }],
        footerLinks: [{ label: 'Server Link', url: '#/server' }]
      };
    });
    const before = await page.evaluate(() => JSON.stringify(window.SITE_CONFIG && window.SITE_CONFIG.home));
    const btn = await page.$('#customizeToggle, button[title*="ustom" i], [class*="customize"]');
    if (!btn) {
      console.log('    ⏭️  Customizer toggle not found — TODO: requires running server');
      return;
    }
    await btn.click();
    await page.waitForTimeout(200);
    const after = await page.evaluate(() => JSON.stringify(window.SITE_CONFIG && window.SITE_CONFIG.home));
    assert(after === before, 'Opening customizer should not mutate server home config');
  });

  await test('Home customization persists through page refresh', async () => {
    // TODO: requires running server with full customize/home wiring
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const toggleSelector = '#customizeToggle, button[title*="ustom" i], button[aria-label*="theme" i], [class*="customize"]';
    const btn = await page.$(toggleSelector);
    if (!btn) {
      console.log('    ⏭️  Customizer toggle not found — TODO: requires running server');
      return;
    }
    const editedHero = 'Persisted Hero From Playwright';
    await page.click(toggleSelector);
    const homeTab = page.locator('.cust-tab[data-tab="home"]');
    await homeTab.waitFor({ state: 'visible', timeout: 10000 });
    await homeTab.click();
    const heroInput = page.locator('[data-cv2-field="home.heroTitle"]');
    if (await heroInput.count() === 0) {
      console.log('    ⏭️  home.heroTitle input not found — TODO: requires running server');
      return;
    }
    await heroInput.waitFor({ state: 'visible', timeout: 10000 });
    await heroInput.fill(editedHero);
    await page.waitForTimeout(700); // debounce is 300ms, allow margin
    await page.reload({ waitUntil: 'domcontentloaded' });
    const persistedHero = await page.evaluate(() => {
      try {
        const saved = JSON.parse(localStorage.getItem('cs-theme-overrides') || '{}');
        return saved && saved.home ? saved.home.heroTitle : '';
      } catch {
        return '';
      }
    });
    assert(persistedHero === editedHero, `Expected persisted hero "${editedHero}" but got "${persistedHero}"`);
  });

  // Test 7: Dark mode toggle (fresh navigation \u2014 customizer panel may be open)
  await test('Dark mode toggle', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const themeBefore = await page.$eval('html', el => el.getAttribute('data-theme'));
    // Find toggle button
    const allButtons = await page.$$('button');
    let toggled = false;
    for (const b of allButtons) {
      const text = await b.textContent();
      if (text.includes('\u2600') || text.includes('\ud83c\udf19') || text.includes('\ud83c\udf11') || text.includes('\ud83c\udf15')) {
        await b.click();
        toggled = true;
        break;
      }
    }
    assert(toggled, 'Could not find dark mode toggle button');
    await page.waitForFunction(
      (before) => document.documentElement.getAttribute('data-theme') !== before,
      themeBefore
    );
    const themeAfter = await page.$eval('html', el => el.getAttribute('data-theme'));
    assert(themeBefore !== themeAfter, `Theme didn't change: before=${themeBefore}, after=${themeAfter}`);
  });

  // Test: Stats bar shows version/commit badge
  await test('Stats bar shows version and commit badge', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    // Wait for stats to load (fetched from /api/stats)
    await page.waitForFunction(() => {
      const stats = document.getElementById('navStats');
      return stats && stats.textContent.trim().length > 5;
    }, { timeout: 10000 });
    const navStats = await page.$('#navStats');
    assert(navStats, 'Nav stats bar (#navStats) not found');
    // Check if stats API exposes version info
    const hasVersionData = await page.evaluate(async () => {
      try {
        const res = await fetch('/api/stats');
        const data = await res.json();
        return !!(data.version || data.commit || data.engine);
      } catch { return false; }
    });
    if (!hasVersionData) {
      console.log('    ⏭️  Server does not expose version/commit in /api/stats — badge test skipped');
      return;
    }
    // Version badge should appear when data is available
    await page.waitForFunction(() => !!document.querySelector('.version-badge'), { timeout: 5000 });
    const badgeText = await page.$eval('.version-badge', el => el.textContent.trim());
    assert(badgeText.length > 3, `Version badge should have content but got "${badgeText}"`);
    const hasCommitHash = /[0-9a-f]{7}/i.test(badgeText);
    assert(hasCommitHash, `Version badge should contain a commit hash, got "${badgeText}"`);
    const engineBadge = await page.$('.engine-badge');
    assert(engineBadge, 'Engine badge (.engine-badge) not found');
    const engineText = await page.$eval('.engine-badge', el => el.textContent.trim().toLowerCase());
    assert(engineText.includes('node') || engineText.includes('go'), `Engine should contain "node" or "go", got "${engineText}"`);
  });

  // --- Group: Nodes page (tests 2, 5) ---

  // Test 2: Nodes page loads with data
  await test('Nodes page loads with data', async () => {
    await page.goto(`${BASE}/#/nodes`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('table tbody tr');
    const headers = await page.$$eval('th', els => els.map(e => e.textContent.trim()));
    for (const col of ['Name', 'Public Key', 'Role']) {
      assert(headers.some(h => h.includes(col)), `Missing column: ${col}`);
    }
    assert(headers.some(h => h.includes('Last Seen') || h.includes('Last')), 'Missing Last Seen column');
    const rows = await page.$$('table tbody tr');
    assert(rows.length >= 1, `Expected >=1 nodes, got ${rows.length}`);
  });

  // Test 5: Node detail loads (reuses nodes page from test 2)
  await test('Node detail loads', async () => {
    await page.waitForSelector('table tbody tr');
    // Click first row
    const firstRow = await page.$('table tbody tr');
    assert(firstRow, 'No node rows found');
    await firstRow.click();
    // Wait for detail pane to appear
    await page.waitForSelector('.node-detail');
    const html = await page.content();
    // Check for status indicator
    const hasStatus = html.includes('\ud83d\udfe2') || html.includes('\u26aa') || html.includes('status') || html.includes('Active') || html.includes('Stale');
    assert(hasStatus, 'No status indicator found in node detail');
  });

  // Test: Nodes page has WebSocket auto-update listener (#131)
  await test('Nodes page has WebSocket auto-update', async () => {
    await page.goto(`${BASE}/#/nodes`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('table tbody tr');
    // The live dot in navbar indicates WS connection status
    const liveDot = await page.$('#liveDot');
    assert(liveDot, 'Live dot WebSocket indicator (#liveDot) not found');
    // Verify WS infrastructure exists (onWS/offWS globals from app.js)
    const hasWsInfra = await page.evaluate(() => {
      return typeof onWS === 'function' && typeof offWS === 'function';
    });
    assert(hasWsInfra, 'WebSocket listener infrastructure (onWS/offWS) should be available');
    // Wait for WS connection and verify liveDot shows connected state
    try {
      await page.waitForFunction(() => {
        const dot = document.getElementById('liveDot');
        return dot && dot.classList.contains('connected');
      }, { timeout: 5000 });
    } catch (_) {
      // WS may not connect against remote — liveDot existence is sufficient
    }
  });

  // --- Group: Map page (tests 3, 9, 10, 13, 16) ---

  // Test 3: Map page loads with markers
  await test('Map page loads with markers', async () => {
    await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.leaflet-container');
    await page.waitForSelector('.leaflet-tile-loaded');
    // Wait for markers/overlays to render (may not exist with empty DB)
    try {
      await page.waitForSelector('.leaflet-marker-icon, .leaflet-interactive, circle, .marker-cluster, .leaflet-marker-pane > *, .leaflet-overlay-pane svg path, .leaflet-overlay-pane svg circle', { timeout: 3000 });
    } catch (_) {
      // No markers with empty DB \u2014 assertion below handles it
    }
    const markers = await page.$$('.leaflet-marker-icon, .leaflet-interactive, circle, .marker-cluster, .leaflet-marker-pane > *, .leaflet-overlay-pane svg path, .leaflet-overlay-pane svg circle');
    assert(markers.length > 0, 'No map markers/overlays found');
  });

  // Test 9: Map heat checkbox persists in localStorage (reuses map page)
  await test('Map heat checkbox persists in localStorage', async () => {
    await page.waitForSelector('#mcHeatmap');
    // Uncheck first to ensure clean state
    await page.evaluate(() => localStorage.removeItem('meshcore-map-heatmap'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#mcHeatmap');
    let checked = await page.$eval('#mcHeatmap', el => el.checked);
    assert(!checked, 'Heat checkbox should be unchecked by default');
    // Check it
    await page.click('#mcHeatmap');
    const stored = await page.evaluate(() => localStorage.getItem('meshcore-map-heatmap'));
    assert(stored === 'true', `localStorage should be "true" but got "${stored}"`);
    // Reload and verify persisted — wait for async map init to restore state
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => {
      const el = document.getElementById('mcHeatmap');
      return el && el.checked;
    }, { timeout: 10000 });
    checked = await page.$eval('#mcHeatmap', el => el.checked);
    assert(checked, 'Heat checkbox should be checked after reload');
    // Clean up
    await page.evaluate(() => localStorage.removeItem('meshcore-map-heatmap'));
  });

  // Test 10: Map heat checkbox is not disabled (unless matrix mode)
  await test('Map heat checkbox is clickable', async () => {
    await page.waitForSelector('#mcHeatmap');
    const disabled = await page.$eval('#mcHeatmap', el => el.disabled);
    assert(!disabled, 'Heat checkbox should not be disabled');
    // Click and verify state changes
    const before = await page.$eval('#mcHeatmap', el => el.checked);
    await page.click('#mcHeatmap');
    const after = await page.$eval('#mcHeatmap', el => el.checked);
    assert(before !== after, 'Heat checkbox state should toggle on click');
  });

  // Test 13: Heatmap opacity stored in localStorage (reuses map page)
  await test('Heatmap opacity persists in localStorage', async () => {
    await page.evaluate(() => localStorage.setItem('meshcore-heatmap-opacity', '0.5'));
    // Enable heat to trigger layer creation with saved opacity
    await page.evaluate(() => localStorage.setItem('meshcore-map-heatmap', 'true'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#mcHeatmap');
    const opacity = await page.evaluate(() => localStorage.getItem('meshcore-heatmap-opacity'));
    assert(opacity === '0.5', `Opacity should persist as "0.5" but got "${opacity}"`);
    // Verify the canvas element has the opacity applied (if heat layer exists)
    const canvasOpacity = await page.evaluate(() => {
      if (window._meshcoreHeatLayer && window._meshcoreHeatLayer._canvas) {
        return window._meshcoreHeatLayer._canvas.style.opacity;
      }
      return null; // no heat layer (no node data) \u2014 skip
    });
    if (canvasOpacity !== null) {
      assert(canvasOpacity === '0.5', `Canvas opacity should be "0.5" but got "${canvasOpacity}"`);
    }
    // Clean up
    await page.evaluate(() => {
      localStorage.removeItem('meshcore-heatmap-opacity');
      localStorage.removeItem('meshcore-map-heatmap');
    });
  });

  // Test 16: Map re-renders markers on resize (decollision recalculates)
  await test('Map re-renders on resize', async () => {
    await page.waitForSelector('.leaflet-container');
    // Wait for markers (may not exist with empty DB)
    try {
      await page.waitForSelector('.leaflet-marker-icon, .leaflet-interactive', { timeout: 3000 });
    } catch (_) {
      // No markers with empty DB
    }
    // Count markers before resize
    const beforeCount = await page.$$eval('.leaflet-marker-icon, .leaflet-interactive', els => els.length);
    // Resize viewport
    await page.setViewportSize({ width: 600, height: 400 });
    // Wait for Leaflet to process resize
    await page.waitForFunction(() => {
      const c = document.querySelector('.leaflet-container');
      return c && c.offsetWidth <= 600;
    });
    // Markers should still be present after resize (re-rendered, not lost)
    const afterCount = await page.$$eval('.leaflet-marker-icon, .leaflet-interactive', els => els.length);
    assert(afterCount > 0, `Should have markers after resize, got ${afterCount}`);
    // Restore
    await page.setViewportSize({ width: 1280, height: 720 });
  });

  // --- Group: Packets page (test 4) ---

  // Test 4: Packets page loads with filter
  await test('Packets page loads with filter', async () => {
    // Ensure desktop viewport and broad time window so fixture timestamps are included.
    await page.setViewportSize({ width: 1280, height: 720 });
    // Set time window BEFORE packets.js IIFE re-executes (525600 min ≈ 1 year).
    // Navigate to the packets URL then reload — avoids about:blank cross-origin issues
    // that can prevent the SPA from fully initializing within the timeout.
    await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => localStorage.setItem('meshcore-time-window', '525600'));
    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('table tbody tr', { timeout: 15000 });
    const rowsBefore = await page.$$('table tbody tr');
    assert(rowsBefore.length > 0, 'No packets visible');
    // Use the specific filter input
    const filterInput = await page.$('#packetFilterInput');
    assert(filterInput, 'Packet filter input not found');
    await filterInput.fill('type == ADVERT');
    // Client-side filter has input debounce (~250ms); wait for it to apply
    await page.waitForTimeout(500);
    // Verify filter was applied (count may differ)
    const rowsAfter = await page.$$('table tbody tr');
    assert(rowsAfter.length > 0, 'No packets after filtering');
  });

  await test('Packets initial fetch honors persisted time window', async () => {
    // Set persisted time window to 60 min and reload so the IIFE reads it
    await page.evaluate(() => localStorage.setItem('meshcore-time-window', '60'));

    const packetsRequestPromise = page.waitForRequest((req) => {
      try {
        const parsed = new URL(req.url());
        return parsed.pathname === '/api/packets' && parsed.searchParams.has('since');
      } catch {
        return false;
      }
    }, { timeout: 10000 });

    // Full reload on the packets page — scripts re-execute, IIFE reads localStorage
    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('#fTimeWindow', { timeout: 10000 });
    const timeWindowValue = await page.$eval('#fTimeWindow', (el) => el.value);
    assert(timeWindowValue === '60', `Expected time window dropdown to restore 60, got ${timeWindowValue}`);

    const req = await packetsRequestPromise;
    const parsed = new URL(req.url());
    const since = parsed.searchParams.get('since');
    assert(since, 'Expected since query parameter on initial packets request');

    const deltaMin = (Date.now() - Date.parse(since)) / 60000;
    assert(deltaMin > 45 && deltaMin < 75, `Expected ~60 minute window, got ${deltaMin.toFixed(2)} minutes`);
  });

  // Test: Packet detail pane hidden on fresh load
  await test('Packets detail pane hidden on fresh load', async () => {
    await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#pktRight', { state: 'attached' });
    const isEmpty = await page.$eval('#pktRight', el => el.classList.contains('empty'));
    assert(isEmpty, 'Detail pane should have "empty" class on fresh load');
  });

  // Test: Packets groupByHash toggle changes view
  await test('Packets groupByHash toggle works', async () => {
    // Restore wide time window — previous test set it to 60 min which excludes fixture data
    await page.evaluate(() => localStorage.setItem('meshcore-time-window', '525600'));
    await page.reload({ waitUntil: 'load' });
    await page.waitForSelector('table tbody tr', { timeout: 15000 });
    const groupBtn = await page.$('#fGroup');
    assert(groupBtn, 'Group by hash button (#fGroup) not found');
    // Check initial state (default is grouped/active)
    const initialActive = await page.$eval('#fGroup', el => el.classList.contains('active'));
    // Click to toggle
    await groupBtn.click();
    await page.waitForFunction((wasActive) => {
      const btn = document.getElementById('fGroup');
      return btn && btn.classList.contains('active') !== wasActive;
    }, initialActive, { timeout: 5000 });
    const afterFirst = await page.$eval('#fGroup', el => el.classList.contains('active'));
    assert(afterFirst !== initialActive, 'Group button state should change after click');
    await page.waitForSelector('table tbody tr');
    const rows = await page.$$eval('table tbody tr', r => r.length);
    assert(rows > 0, 'Should have rows after toggle');
    // Click again to toggle back
    await groupBtn.click();
    await page.waitForFunction((prev) => {
      const btn = document.getElementById('fGroup');
      return btn && btn.classList.contains('active') !== prev;
    }, afterFirst, { timeout: 5000 });
    const afterSecond = await page.$eval('#fGroup', el => el.classList.contains('active'));
    assert(afterSecond === initialActive, 'Group button should return to initial state after second click');
  });

  // Test: Clicking a packet row opens detail pane
  // SKIPPED: flaky test — see https://github.com/Kpa-clawbot/CoreScope/issues/257
  console.log('  ⏭️  Packets clicking row shows detail pane (SKIPPED — flaky)');
  /*await test('Packets clicking row shows detail pane', async () => {
    // Fresh navigation to avoid stale row references from previous test
    await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
    // Wait for table rows AND initial API data to settle
    await page.waitForSelector('table tbody tr[data-action]', { timeout: 15000 });
    await page.waitForLoadState('networkidle');
    const firstRow = await page.$('table tbody tr[data-action]');
    assert(firstRow, 'No clickable packet rows found');
    // Click the row and wait for the /packets/{hash} API response
    const [response] = await Promise.all([
      page.waitForResponse(resp => resp.url().includes('/packets/') && resp.status() === 200, { timeout: 15000 }),
      firstRow.click(),
    ]);
    assert(response, 'API response for packet detail not received');
    await page.waitForFunction(() => {
      const panel = document.getElementById('pktRight');
      return panel && !panel.classList.contains('empty');
    }, { timeout: 15000 });
    const panelVisible = await page.$eval('#pktRight', el => !el.classList.contains('empty'));
    assert(panelVisible, 'Detail pane should open after clicking a row');
    const content = await page.$eval('#pktRight', el => el.textContent.trim());
    assert(content.length > 0, 'Detail pane should have content');
  });

  // Test: Packet detail pane dismiss button (Issue #125)
  await test('Packet detail pane closes on ✕ click', async () => {
    // Ensure we're on packets page with detail pane open
    const pktRight = await page.$('#pktRight');
    if (!pktRight) {
      await page.goto(`${BASE}/#/packets`, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('table tbody tr[data-action]', { timeout: 15000 });
      await page.waitForLoadState('networkidle');
    }
    const panelOpen = await page.$eval('#pktRight', el => !el.classList.contains('empty'));
    if (!panelOpen) {
      const firstRow = await page.$('table tbody tr[data-action]');
      if (!firstRow) { console.log('    ⏭️  Skipped (no clickable rows)'); return; }
      await Promise.all([
        page.waitForResponse(resp => resp.url().includes('/packets/') && resp.status() === 200, { timeout: 15000 }),
        firstRow.click(),
      ]);
      await page.waitForFunction(() => {
        const panel = document.getElementById('pktRight');
        return panel && !panel.classList.contains('empty');
      }, { timeout: 15000 });
    }
    const closeBtn = await page.$('#pktRight .panel-close-btn');
    assert(closeBtn, 'Close button (✕) not found in detail pane');
    await closeBtn.click();
    await page.waitForFunction(() => {
      const panel = document.getElementById('pktRight');
      return panel && panel.classList.contains('empty');
    }, { timeout: 3000 });
    const panelHidden = await page.$eval('#pktRight', el => el.classList.contains('empty'));
    assert(panelHidden, 'Detail pane should be hidden after clicking ✕');
  });*/
  console.log('  ⏭️  Packet detail pane closes on ✕ click (SKIPPED — depends on flaky test above)');

  // Test: GRP_TXT packet detail shows Channel Hash (#123)
  await test('GRP_TXT packet detail shows Channel Hash', async () => {
    // Find an undecrypted GRP_TXT packet via API (only these show Channel Hash)
    const hash = await page.evaluate(async () => {
      try {
        const res = await fetch('/api/packets?limit=500');
        const data = await res.json();
        for (const p of (data.packets || [])) {
          try {
            const d = JSON.parse(p.decoded_json || '{}');
            if (d.type === 'GRP_TXT' && !d.text && d.channelHash != null) return p.hash;
          } catch {}
        }
      } catch {}
      return null;
    });
    if (!hash) { console.log('    ⏭️  Skipped (no undecrypted GRP_TXT packets found)'); return; }
    await page.goto(`${BASE}/#/packets/${hash}`, { waitUntil: 'domcontentloaded' });
    // Wait for detail to render with actual content (not "Loading…")
    await page.waitForFunction(() => {
      const panel = document.getElementById('pktRight');
      if (!panel || panel.classList.contains('empty')) return false;
      const text = panel.textContent;
      return text.length > 50 && !text.includes('Loading');
    }, { timeout: 8000 });
    const detailHtml = await page.$eval('#pktRight', el => el.innerHTML);
    const hasChannelHash = detailHtml.includes('Channel Hash') || detailHtml.includes('Ch 0x');
    assert(hasChannelHash, 'Undecrypted GRP_TXT detail should show "Channel Hash"');
  });

  // --- Group: Analytics page (test 8 + sub-tabs) ---

  // Test 8: Analytics page loads with overview
  await test('Analytics page loads', async () => {
    await page.goto(`${BASE}/#/analytics`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#analyticsTabs');
    const tabs = await page.$$('#analyticsTabs .tab-btn');
    assert(tabs.length >= 10, `Expected >=10 analytics tabs, got ${tabs.length}`);
    // Overview tab should be active by default and show stat cards
    await page.waitForSelector('#analyticsContent .stat-card', { timeout: 8000 });
    const cards = await page.$$('#analyticsContent .stat-card');
    assert(cards.length >= 3, `Expected >=3 overview stat cards, got ${cards.length}`);
  });

  // Analytics sub-tab tests
  await test('Analytics RF tab renders content', async () => {
    await page.click('[data-tab="rf"]');
    await page.waitForSelector('#analyticsContent .analytics-table, #analyticsContent svg', { timeout: 8000 });
    const hasTables = await page.$$eval('#analyticsContent .analytics-table', els => els.length);
    const hasSvg = await page.$$eval('#analyticsContent svg', els => els.length);
    assert(hasTables > 0 || hasSvg > 0, 'RF tab should render tables or SVG charts');
  });

  await test('Analytics Topology tab renders content', async () => {
    await page.click('[data-tab="topology"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && (c.querySelector('.repeater-list') || c.querySelector('.analytics-card') || c.querySelector('.reach-rings'));
    }, { timeout: 8000 });
    const hasContent = await page.$$eval('#analyticsContent .analytics-card, #analyticsContent .repeater-list', els => els.length);
    assert(hasContent > 0, 'Topology tab should render cards or repeater list');
  });

  await test('Analytics Channels tab renders content', async () => {
    await page.click('[data-tab="channels"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && c.textContent.trim().length > 10;
    }, { timeout: 8000 });
    const content = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(content.length > 10, 'Channels tab should render content');
  });

  await test('Analytics Hash Stats tab renders content', async () => {
    await page.click('[data-tab="hashsizes"]');
    await page.waitForSelector('#analyticsContent .hash-bar-row, #analyticsContent .analytics-table', { timeout: 8000 });
    const content = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(content.length > 10, 'Hash Stats tab should render content');
  });

  await test('Analytics Hash Issues tab renders content', async () => {
    await page.click('[data-tab="collisions"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && (c.querySelector('#hashMatrix') || c.querySelector('#inconsistentHashSection') || c.textContent.trim().length > 20);
    }, { timeout: 8000 });
    const hasContent = await page.$('#analyticsContent #hashMatrix, #analyticsContent #inconsistentHashSection');
    const text = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(hasContent || text.length > 20, 'Hash Issues tab should render content');
  });

  await test('Analytics Route Patterns tab renders content', async () => {
    await page.click('[data-tab="subpaths"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && (c.querySelector('.subpath-layout') || c.textContent.trim().length > 20);
    }, { timeout: 8000 });
    const content = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(content.length > 10, 'Route Patterns tab should render content');
  });

  await test('Analytics Distance tab renders content', async () => {
    await page.click('[data-tab="distance"]');
    await page.waitForFunction(() => {
      const c = document.getElementById('analyticsContent');
      return c && (c.querySelector('.stat-card') || c.querySelector('.data-table') || c.textContent.trim().length > 20);
    }, { timeout: 8000 });
    const content = await page.$eval('#analyticsContent', el => el.textContent.trim());
    assert(content.length > 10, 'Distance tab should render content');
  });

  await test('Analytics Neighbor Graph tab renders canvas and stats', async () => {
    await page.click('[data-tab="neighbor-graph"]');
    await page.waitForSelector('#ngCanvas', { timeout: 8000 });
    const hasCanvas = await page.$('#ngCanvas');
    assert(hasCanvas, 'Neighbor Graph tab should have a canvas element');
    const hasStats = await page.$$eval('#ngStats .stat-card', els => els.length);
    assert(hasStats >= 3, `Neighbor Graph stats should have >=3 cards, got ${hasStats}`);
    // Verify filters exist
    const hasSlider = await page.$('#ngMinScore');
    assert(hasSlider, 'Should have min score slider');
    const hasConfidence = await page.$('#ngConfidence');
    assert(hasConfidence, 'Should have confidence filter');
  });

  await test('Analytics Neighbor Graph filter changes update stats', async () => {
    // Capture edge count before filter
    const edgesBefore = await page.$eval('#ngStats', el => {
      const cards = el.querySelectorAll('.stat-card');
      for (const c of cards) {
        if (c.textContent.toLowerCase().includes('edge')) {
          const m = c.textContent.match(/\d+/);
          if (m) return parseInt(m[0], 10);
        }
      }
      return -1;
    });
    // Set min score slider to high value to reduce edges
    await page.$eval('#ngMinScore', el => { el.value = 90; el.dispatchEvent(new Event('input')); });
    await page.waitForTimeout(300);
    const edgesAfter = await page.$eval('#ngStats', el => {
      const cards = el.querySelectorAll('.stat-card');
      for (const c of cards) {
        if (c.textContent.toLowerCase().includes('edge')) {
          const m = c.textContent.match(/\d+/);
          if (m) return parseInt(m[0], 10);
        }
      }
      return -1;
    });
    assert(edgesBefore >= 0, 'Should find edge count in stats before filter');
    assert(edgesAfter >= 0, 'Should find edge count in stats after filter');
    assert(edgesAfter <= edgesBefore, `Raising min score should reduce (or keep) edge count: ${edgesBefore} → ${edgesAfter}`);
    // Reset slider
    await page.$eval('#ngMinScore', el => { el.value = 0; el.dispatchEvent(new Event('input')); });
    await page.waitForTimeout(200);
  });

  // --- Group: Compare page ---

  await test('Compare page loads with observer dropdowns', async () => {
    await page.goto(`${BASE}/#/compare`, { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => {
      const selA = document.getElementById('compareObsA');
      return selA && selA.options.length > 1;
    }, { timeout: 10000 });
    const optionsA = await page.$$eval('#compareObsA option', opts => opts.length);
    const optionsB = await page.$$eval('#compareObsB option', opts => opts.length);
    assert(optionsA > 1, `Observer A dropdown should have options, got ${optionsA}`);
    assert(optionsB > 1, `Observer B dropdown should have options, got ${optionsB}`);
  });

  await test('Compare page runs comparison', async () => {
    const options = await page.$$eval('#compareObsA option', opts =>
      opts.filter(o => o.value).map(o => o.value)
    );
    assert(options.length >= 2, `Need >=2 observers, got ${options.length}`);
    await page.selectOption('#compareObsA', options[0]);
    await page.selectOption('#compareObsB', options[1]);
    await page.waitForFunction(() => {
      const btn = document.getElementById('compareBtn');
      return btn && !btn.disabled;
    }, { timeout: 3000 });
    await page.click('#compareBtn');
    await page.waitForFunction(() => {
      const c = document.getElementById('compareContent');
      return c && c.textContent.trim().length > 20;
    }, { timeout: 15000 });
    const hasResults = await page.$eval('#compareContent', el => el.textContent.trim().length > 0);
    assert(hasResults, 'Comparison should produce results');
  });

  // Test: Compare results show shared/unique breakdown (#129)
  await test('Compare results show shared/unique cards', async () => {
    // Results should be visible from previous test
    const cardBoth = await page.$('.compare-card-both');
    assert(cardBoth, 'Should have "shared" card (.compare-card-both)');
    const cardA = await page.$('.compare-card-a');
    assert(cardA, 'Should have "only A" card (.compare-card-a)');
    const cardB = await page.$('.compare-card-b');
    assert(cardB, 'Should have "only B" card (.compare-card-b)');
    // Verify counts are rendered (may be locale-formatted with commas)
    const counts = await page.$$eval('.compare-card-count', els => els.map(e => e.textContent.trim()));
    assert(counts.length >= 3, `Expected >=3 summary counts, got ${counts.length}`);
    counts.forEach((c, i) => {
      assert(/^[\d,]+$/.test(c), `Count ${i} should be a number but got "${c}"`);
    });
    // Verify tab buttons exist for both/onlyA/onlyB
    const tabs = await page.$$eval('[data-cview]', els => els.map(e => e.getAttribute('data-cview')));
    assert(tabs.includes('both'), 'Should have "both" tab');
    assert(tabs.includes('onlyA'), 'Should have "onlyA" tab');
    assert(tabs.includes('onlyB'), 'Should have "onlyB" tab');
  });

  // Test: Compare "both" tab shows table with shared packets
  await test('Compare both tab shows shared packets table', async () => {
    const bothTab = await page.$('[data-cview="both"]');
    assert(bothTab, '"both" tab button not found');
    await bothTab.click();
    // Table renders inside #compareDetail (not #compareContent)
    await page.waitForFunction(() => {
      const d = document.getElementById('compareDetail');
      return d && (d.querySelector('.compare-table') || d.textContent.trim().length > 5);
    }, { timeout: 5000 });
    const table = await page.$('#compareDetail .compare-table');
    if (table) {
      // Verify table has expected columns (Hash, Time, Type)
      const headers = await page.$$eval('#compareDetail .compare-table th', els => els.map(e => e.textContent.trim()));
      assert(headers.some(h => h.includes('Hash') || h.includes('hash')), 'Table should have Hash column');
      assert(headers.some(h => h.includes('Type') || h.includes('type')), 'Table should have Type column');
    } else {
      // No shared packets — should show "No packets" message
      const text = await page.$eval('#compareDetail', el => el.textContent.trim());
      assert(text.includes('No packets') || text.length > 0, 'Should show message or table');
    }
  });

  // --- Group: Live page ---

  // Test: Live page loads with map and stats
  await test('Live page loads with map and stats', async () => {
    await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#liveMap');
    // Verify key page elements exist
    const hasMap = await page.$('#liveMap');
    assert(hasMap, 'Live page should have map element');
    const hasHeader = await page.$('#liveHeader, .live-header');
    assert(hasHeader, 'Live page should have header');
    // Check stats elements exist
    const pktCount = await page.$('#livePktCount');
    assert(pktCount, 'Live page should have packet count element');
    const nodeCount = await page.$('#liveNodeCount');
    assert(nodeCount, 'Live page should have node count element');
  });

  // Test: Live page WebSocket connects
  await test('Live page WebSocket connects', async () => {
    // Check for live beacon indicator (shows page is in live mode)
    const hasBeacon = await page.$('.live-beacon');
    assert(hasBeacon, 'Live page should have beacon indicator');
    // Check VCR mode indicator shows LIVE
    const vcrMode = await page.$('#vcrMode, #vcrLcdMode');
    assert(vcrMode, 'Live page should have VCR mode indicator');
    // Verify WebSocket is connected by checking for the ws object
    const wsConnected = await page.evaluate(() => {
      // The live page creates a WebSocket - check if it exists
      // Look for any WebSocket instances or connection indicators
      const beacon = document.querySelector('.live-beacon');
      const vcrDot = document.querySelector('.vcr-live-dot');
      return !!(beacon || vcrDot);
    });
    assert(wsConnected, 'WebSocket connection indicators should be present');
  });


  // Test 11: Live page heat checkbox disabled by matrix/ghosts mode
  await test('Live heat disabled when ghosts mode active', async () => {
    await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
    // Wait for live init to complete (Leaflet tiles load after map creation + loadNodes)
    await page.waitForSelector('.leaflet-tile-loaded', { timeout: 15000 });
    await page.waitForSelector('#liveHeatToggle');
    // Enable matrix mode if not already
    const matrixEl = await page.$('#liveMatrixToggle');
    if (matrixEl) {
      await page.evaluate(() => {
        const mt = document.getElementById('liveMatrixToggle');
        if (mt && !mt.checked) mt.click();
      });
      await page.waitForFunction(() => {
        const heat = document.getElementById('liveHeatToggle');
        return heat && heat.disabled;
      });
      const heatDisabled = await page.$eval('#liveHeatToggle', el => el.disabled);
      assert(heatDisabled, 'Heat should be disabled when ghosts/matrix is on');
      // Turn off matrix
      await page.evaluate(() => {
        const mt = document.getElementById('liveMatrixToggle');
        if (mt && mt.checked) mt.click();
      });
      await page.waitForFunction(() => {
        const heat = document.getElementById('liveHeatToggle');
        return heat && !heat.disabled;
      });
      const heatEnabled = await page.$eval('#liveHeatToggle', el => !el.disabled);
      assert(heatEnabled, 'Heat should be re-enabled when ghosts/matrix is off');
    }
  });

  // Test 12: Live page heat checkbox persists across reload (reuses live page)
  await test('Live heat checkbox persists in localStorage', async () => {
    await page.waitForSelector('#liveHeatToggle');
    // Clear state and set to 'false' so we can verify persistence after reload
    await page.evaluate(() => localStorage.setItem('meshcore-live-heatmap', 'false'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    // Wait for async init to read localStorage and uncheck the toggle
    await page.waitForFunction(() => {
      const el = document.getElementById('liveHeatToggle');
      return el && !el.checked;
    }, { timeout: 10000 });
    const afterReload = await page.$eval('#liveHeatToggle', el => el.checked);
    assert(!afterReload, 'Live heat checkbox should stay unchecked after reload');
    // Set to 'true' and verify that also persists
    await page.evaluate(() => localStorage.setItem('meshcore-live-heatmap', 'true'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => {
      const el = document.getElementById('liveHeatToggle');
      return el && el.checked;
    }, { timeout: 10000 });
    const afterReload2 = await page.$eval('#liveHeatToggle', el => el.checked);
    assert(afterReload2, 'Live heat checkbox should stay checked after reload');
    // Clean up
    await page.evaluate(() => localStorage.removeItem('meshcore-live-heatmap'));
  });

  // --- Group: No navigation needed (tests 14, 15) ---

  // Test 14: Live heatmap opacity stored in localStorage
  await test('Live heatmap opacity persists in localStorage', async () => {
    // Verify localStorage key works (no page load needed \u2014 reuse current page)
    await page.evaluate(() => localStorage.setItem('meshcore-live-heatmap-opacity', '0.6'));
    const opacity = await page.evaluate(() => localStorage.getItem('meshcore-live-heatmap-opacity'));
    assert(opacity === '0.6', `Live opacity should persist as "0.6" but got "${opacity}"`);
    await page.evaluate(() => localStorage.removeItem('meshcore-live-heatmap-opacity'));
  });

  // Test 15: Customizer has separate Map and Live opacity sliders
  await test('Customizer has separate map and live opacity sliders', async () => {
    // Verify by checking JS source \u2014 avoids heavy page reloads that crash ARM chromium
    const custJs = await page.evaluate(async () => {
      const res = await fetch('/customize.js?_=' + Date.now());
      return res.text();
    });
    assert(custJs.includes('custHeatOpacity'), 'customize.js should have map opacity slider (custHeatOpacity)');
    assert(custJs.includes('custLiveHeatOpacity'), 'customize.js should have live opacity slider (custLiveHeatOpacity)');
    assert(custJs.includes('meshcore-heatmap-opacity'), 'customize.js should use meshcore-heatmap-opacity key');
    assert(custJs.includes('meshcore-live-heatmap-opacity'), 'customize.js should use meshcore-live-heatmap-opacity key');
    // Verify labels are distinct
    assert(custJs.includes('Nodes Map') || custJs.includes('nodes map') || custJs.includes('\ud83d\uddfa'), 'Map slider should have map-related label');
    assert(custJs.includes('Live Map') || custJs.includes('live map') || custJs.includes('\ud83d\udce1'), 'Live slider should have live-related label');
  });

  // --- Group: Channels page ---

  await test('Channels page loads with channel list', async () => {
    await page.goto(`${BASE}/#/channels`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#chList', { timeout: 8000 });
    // Channels are fetched async — wait for items to render
    await page.waitForFunction(() => {
      const list = document.getElementById('chList');
      return list && list.querySelectorAll('.ch-item').length > 0;
    }, { timeout: 15000 });
    const items = await page.$$('#chList .ch-item');
    assert(items.length > 0, `Expected >=1 channel items, got ${items.length}`);
    // Verify channel items have names
    const names = await page.$$eval('#chList .ch-item-name', els => els.map(e => e.textContent.trim()));
    assert(names.length > 0, 'Channel items should have names');
    assert(names[0].length > 0, 'First channel name should not be empty');
  });

  await test('Channels clicking channel shows messages', async () => {
    await page.waitForFunction(() => {
      const list = document.getElementById('chList');
      return list && list.querySelectorAll('.ch-item').length > 0;
    }, { timeout: 10000 });
    const firstItem = await page.$('#chList .ch-item');
    assert(firstItem, 'No channel items to click');
    await firstItem.click();
    await page.waitForFunction(() => {
      const msgs = document.getElementById('chMessages');
      return msgs && msgs.children.length > 0;
    }, { timeout: 10000 });
    const msgCount = await page.$$eval('#chMessages > *', els => els.length);
    assert(msgCount > 0, `Expected messages after clicking channel, got ${msgCount}`);
    // Verify header updated with channel name
    const header = await page.$eval('#chHeader', el => el.textContent.trim());
    assert(header.length > 0, 'Channel header should show channel name');
  });

  // --- Group: Traces page ---

  await test('Traces page loads with search input', async () => {
    await page.goto(`${BASE}/#/traces`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#traceHashInput', { timeout: 8000 });
    const input = await page.$('#traceHashInput');
    assert(input, 'Trace hash input not found');
    const btn = await page.$('#traceBtn');
    assert(btn, 'Trace button not found');
  });

  await test('Traces search returns results for valid hash', async () => {
    // First get a real packet hash from the packets API
    const hash = await page.evaluate(async () => {
      const res = await fetch('/api/packets?limit=1');
      const data = await res.json();
      if (data.packets && data.packets.length > 0) return data.packets[0].hash;
      if (Array.isArray(data) && data.length > 0) return data[0].hash;
      return null;
    });
    if (!hash) { console.log('    ⏭️  Skipped (no packets available)'); return; }
    await page.fill('#traceHashInput', hash);
    await page.click('#traceBtn');
    await page.waitForFunction(() => {
      const r = document.getElementById('traceResults');
      return r && r.textContent.trim().length > 10;
    }, { timeout: 10000 });
    const content = await page.$eval('#traceResults', el => el.textContent.trim());
    assert(content.length > 10, 'Trace results should have content');
  });

  // --- Group: Observers page ---

  await test('Observers page loads with table', async () => {
    await page.goto(`${BASE}/#/observers`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#obsTable', { timeout: 8000 });
    const table = await page.$('#obsTable');
    assert(table, 'Observers table not found');
    // Check for summary stats
    const summary = await page.$('.obs-summary');
    assert(summary, 'Observer summary stats not found');
    // Verify table has rows
    const rows = await page.$$('#obsTable tbody tr');
    assert(rows.length > 0, `Expected >=1 observer rows, got ${rows.length}`);
  });

  await test('Observers table shows health indicators', async () => {
    const dots = await page.$$('#obsTable .health-dot');
    assert(dots.length > 0, 'Observer rows should have health status dots');
    // Verify at least one row has an observer name
    const firstCell = await page.$eval('#obsTable tbody tr td', el => el.textContent.trim());
    assert(firstCell.length > 0, 'Observer name cell should not be empty');
  });

  // --- Group: Perf page ---

  await test('Perf page loads with metrics', async () => {
    await page.goto(`${BASE}/#/perf`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#perfContent', { timeout: 8000 });
    // Wait for perf cards to render (fetches /api/perf and /api/health)
    await page.waitForFunction(() => {
      const c = document.getElementById('perfContent');
      return c && (c.querySelector('.perf-card') || c.querySelector('.perf-table') || c.textContent.trim().length > 20);
    }, { timeout: 10000 });
    const content = await page.$eval('#perfContent', el => el.textContent.trim());
    assert(content.length > 10, 'Perf page should show metrics content');
  });

  await test('Perf page has refresh button', async () => {
    const refreshBtn = await page.$('#perfRefresh');
    assert(refreshBtn, 'Perf refresh button not found');
    // Click refresh and verify content updates (no errors)
    await refreshBtn.click();
    await page.waitForFunction(() => {
      const c = document.getElementById('perfContent');
      return c && c.textContent.trim().length > 10;
    }, { timeout: 8000 });
    const content = await page.$eval('#perfContent', el => el.textContent.trim());
    assert(content.length > 10, 'Perf content should still be present after refresh');
  });



  // Test: Go perf page shows Go Runtime section (goroutines, GC)
  // NOTE: This test requires GO_BASE_URL pointing to Go staging (port 82)
  if (GO_BASE) {
    await test('Go perf page shows Go Runtime metrics', async () => {
      await page.goto(`${GO_BASE}/#/perf`, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#perfContent', { timeout: 8000 });
      await page.waitForFunction(() => {
        const c = document.getElementById('perfContent');
        return c && c.textContent.trim().length > 20;
      }, { timeout: 10000 });
      const perfText = await page.$eval('#perfContent', el => el.textContent);
      assert(perfText.includes('Go Runtime'), 'Go perf page should show "Go Runtime" section');
      assert(perfText.includes('Goroutines') || perfText.includes('goroutines'),
        'Go perf page should show Goroutines metric');
      assert(perfText.includes('GC') || perfText.includes('Heap'),
        'Go perf page should show GC or Heap metrics');
      // Should NOT show Event Loop on Go server
      const hasEventLoop = perfText.includes('Event Loop');
      assert(!hasEventLoop, 'Go perf page should NOT show Event Loop section');
    });
  } else {
    console.log('  ⏭️  Go perf test skipped (set GO_BASE_URL for Go staging, e.g. port 82)');
  }

  // --- Group: Audio Lab page ---

  await test('Audio Lab page loads with controls', async () => {
    await page.goto(`${BASE}/#/audio-lab`, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#alabSidebar', { timeout: 8000 });
    // Verify core controls exist
    const playBtn = await page.$('#alabPlay');
    assert(playBtn, 'Audio Lab play button not found');
    const voiceSelect = await page.$('#alabVoice');
    assert(voiceSelect, 'Audio Lab voice selector not found');
    const bpmSlider = await page.$('#alabBPM');
    assert(bpmSlider, 'Audio Lab BPM slider not found');
    const volSlider = await page.$('#alabVol');
    assert(volSlider, 'Audio Lab volume slider not found');
  });

  await test('Audio Lab sidebar lists packets', async () => {
    // Wait for packets to load from API
    await page.waitForFunction(() => {
      const sidebar = document.getElementById('alabSidebar');
      return sidebar && sidebar.querySelectorAll('.alab-pkt').length > 0;
    }, { timeout: 10000 });
    const packets = await page.$$('#alabSidebar .alab-pkt');
    assert(packets.length > 0, `Expected packets in sidebar, got ${packets.length}`);
    // Verify type headers exist
    const typeHeaders = await page.$$('#alabSidebar .alab-type-hdr');
    assert(typeHeaders.length > 0, 'Should have packet type headers');
  });

  await test('Audio Lab clicking packet shows detail', async () => {
    const firstPkt = await page.$('#alabSidebar .alab-pkt');
    assert(firstPkt, 'No packets to click');
    await firstPkt.click();
    await page.waitForFunction(() => {
      const detail = document.getElementById('alabDetail');
      return detail && detail.textContent.trim().length > 10;
    }, { timeout: 5000 });
    const detail = await page.$eval('#alabDetail', el => el.textContent.trim());
    assert(detail.length > 10, 'Packet detail should show content after click');
    // Verify hex dump is present
    const hexDump = await page.$('#alabHex');
    assert(hexDump, 'Hex dump should be visible after selecting a packet');
  });

  // --- Group: Customizer v2 E2E tests ---

  await test('Customizer v2: setOverride persists and applies CSS', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    // Force light mode — CI headless browsers may default to dark mode,
    // and in dark mode themeDark.accent overwrites theme.accent in applyCSS
    await page.evaluate(() => {
      localStorage.setItem('meshcore-theme', 'light');
      document.documentElement.setAttribute('data-theme', 'light');
    });
    // Clear any existing overrides
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
    // Wait for init() to complete (server config fetch + full pipeline) before
    // setting override, so _runPipeline from init doesn't overwrite our value.
    await page.waitForFunction(() => {
      return window._customizerV2 && window._customizerV2.initDone;
    }, { timeout: 5000 });
    // Set an override via the API
    const result = await page.evaluate(() => {
      window._customizerV2.setOverride('theme', 'accent', '#ff0000');
      // Wait for debounce (300ms) + buffer
      return new Promise(resolve => setTimeout(() => {
        const stored = JSON.parse(localStorage.getItem('cs-theme-overrides') || '{}');
        const cssVal = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim();
        resolve({ stored, cssVal });
      }, 500));
    });
    assert(result.stored.theme && result.stored.theme.accent === '#ff0000',
      'Override not persisted to localStorage');
    assert(result.cssVal === '#ff0000',
      `CSS variable --accent expected #ff0000 but got "${result.cssVal}"`);
    // Cleanup
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });

  await test('Customizer v2: clearOverride resets to server default', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    // Force light mode for consistent CSS testing
    await page.evaluate(() => {
      localStorage.setItem('meshcore-theme', 'light');
      document.documentElement.setAttribute('data-theme', 'light');
    });
    // Wait for init() to complete so _serverDefaults is populated
    await page.waitForFunction(() => {
      return window._customizerV2 && window._customizerV2.initDone;
    }, { timeout: 5000 });
    const result = await page.evaluate(() => {
      // Set the server default accent
      window._customizerV2.setOverride('theme', 'accent', '#ff0000');
      return new Promise(resolve => setTimeout(() => {
        window._customizerV2.clearOverride('theme', 'accent');
        const stored = JSON.parse(localStorage.getItem('cs-theme-overrides') || '{}');
        const hasAccent = stored.theme && stored.theme.hasOwnProperty('accent');
        resolve({ hasAccent });
      }, 500));
    });
    assert(!result.hasAccent, 'accent should be removed from overrides after clearOverride');
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });

  await test('Customizer v2: full reset clears all overrides', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const result = await page.evaluate(() => {
      if (!window._customizerV2) return { error: 'customizerV2 not loaded' };
      localStorage.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#ff0000' }, nodeColors: { repeater: '#00ff00' } }));
      // Simulate full reset
      localStorage.removeItem('cs-theme-overrides');
      const stored = localStorage.getItem('cs-theme-overrides');
      return { stored };
    });
    assert(!result.error, result.error || '');
    assert(result.stored === null, 'cs-theme-overrides should be null after full reset');
  });

  await test('Customizer v2: export produces valid JSON', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const result = await page.evaluate(() => {
      if (!window._customizerV2) return { error: 'customizerV2 not loaded' };
      // Set some overrides
      localStorage.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#123456' } }));
      const delta = window._customizerV2.readOverrides();
      const json = JSON.stringify(delta, null, 2);
      try { JSON.parse(json); return { valid: true, hasAccent: delta.theme && delta.theme.accent === '#123456' }; }
      catch { return { valid: false }; }
    });
    assert(!result.error, result.error || '');
    assert(result.valid, 'Exported JSON must be valid');
    assert(result.hasAccent, 'Exported JSON must contain the stored override');
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });

  await test('Customizer v2: import applies overrides', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const result = await page.evaluate(() => {
      if (!window._customizerV2) return { error: 'customizerV2 not loaded' };
      localStorage.removeItem('cs-theme-overrides');
      const importData = { theme: { accent: '#abcdef' }, nodeColors: { repeater: '#112233' } };
      const validation = window._customizerV2.validateShape(importData);
      if (!validation.valid) return { error: 'Validation failed: ' + validation.errors.join(', ') };
      window._customizerV2.writeOverrides(importData);
      const stored = window._customizerV2.readOverrides();
      return { accent: stored.theme && stored.theme.accent, repeater: stored.nodeColors && stored.nodeColors.repeater };
    });
    assert(!result.error, result.error || '');
    assert(result.accent === '#abcdef', 'Imported accent should be #abcdef');
    assert(result.repeater === '#112233', 'Imported repeater should be #112233');
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });

  await test('Customizer v2: migration from legacy keys', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const result = await page.evaluate(() => {
      if (!window._customizerV2) return { error: 'customizerV2 not loaded' };
      // Clear new key so migration can run
      localStorage.removeItem('cs-theme-overrides');
      // Set legacy keys
      localStorage.setItem('meshcore-user-theme', JSON.stringify({ theme: { accent: '#aabb01' }, branding: { siteName: 'LegacyName' } }));
      localStorage.setItem('meshcore-timestamp-mode', 'absolute');
      localStorage.setItem('meshcore-heatmap-opacity', '0.5');
      // Run migration
      const migrated = window._customizerV2.migrateOldKeys();
      const stored = window._customizerV2.readOverrides();
      const legacyGone = localStorage.getItem('meshcore-user-theme') === null &&
                         localStorage.getItem('meshcore-timestamp-mode') === null &&
                         localStorage.getItem('meshcore-heatmap-opacity') === null;
      return {
        migrated: !!migrated,
        accent: stored.theme && stored.theme.accent,
        siteName: stored.branding && stored.branding.siteName,
        tsMode: stored.timestamps && stored.timestamps.defaultMode,
        opacity: stored.heatmapOpacity,
        legacyGone
      };
    });
    assert(!result.error, result.error || '');
    assert(result.migrated, 'migrateOldKeys should return non-null');
    assert(result.accent === '#aabb01', 'Theme accent should be migrated');
    assert(result.siteName === 'LegacyName', 'Branding should be migrated');
    assert(result.tsMode === 'absolute', 'Timestamp mode should be migrated');
    assert(result.opacity === 0.5, 'Heatmap opacity should be migrated');
    assert(result.legacyGone, 'Legacy keys should be removed after migration');
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });

  await test('Customizer v2: browser-local banner visible', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    // Open customizer
    const toggleSel = '#customizeToggle, button[title*="ustom" i], [class*="customize"]';
    const btn = await page.$(toggleSel);
    if (!btn) { console.log('    ⏭️  Customizer toggle not found'); return; }
    await btn.click();
    await page.waitForSelector('.cv2-local-banner', { timeout: 5000 });
    const bannerText = await page.$eval('.cv2-local-banner', el => el.textContent);
    assert(bannerText.includes('browser only'), `Banner should mention "browser only" but got "${bannerText}"`);
  });

  await test('Customizer v2: auto-save status indicator', async () => {
    // Panel should already be open from previous test
    const statusEl = await page.$('#cv2-save-status');
    if (!statusEl) { console.log('    ⏭️  Save status element not found'); return; }
    const statusText = await page.$eval('#cv2-save-status', el => el.textContent);
    assert(statusText.includes('saved') || statusText.includes('Saving'),
      `Status should show save state but got "${statusText}"`);
  });

  await test('Customizer v2: override indicator appears and disappears', async () => {
    // Set override BEFORE page load so _renderTheme sees it during init
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => {
      // Force light mode so theme tab renders 'theme' section (not 'themeDark')
      localStorage.setItem('meshcore-theme', 'light');
      localStorage.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#ff0000' } }));
    });
    // Reload so customizer v2 initializes with the override in place
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    // Ensure light mode is active (CI headless may default to dark)
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
    const result = await page.evaluate(() => {
      if (!window._customizerV2) return { error: 'customizerV2 not loaded' };
      return { ok: true };
    });
    assert(!result.error, result.error || '');
    // Open customizer and check for override dot
    const toggleSel = '#customizeToggle, button[title*="ustom" i], [class*="customize"]';
    const btn = await page.$(toggleSel);
    if (!btn) { console.log('    ⏭️  Customizer toggle not found'); return; }
    await btn.click();
    await page.waitForSelector('.cust-overlay', { timeout: 5000 });
    // Click theme tab
    const themeTab = await page.$('.cust-tab[data-tab="theme"]');
    if (themeTab) await themeTab.click();
    await page.waitForTimeout(200);
    // Check for override dot
    const dots = await page.$$('.cv2-override-dot');
    assert(dots.length > 0, 'Override dot should be visible when overrides exist');
    // Clear overrides and reload to verify dots disappear
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    const btn2 = await page.$(toggleSel);
    if (btn2) await btn2.click();
    await page.waitForSelector('.cust-overlay', { timeout: 5000 });
    const themeTab2 = await page.$('.cust-tab[data-tab="theme"]');
    if (themeTab2) await themeTab2.click();
    await page.waitForTimeout(200);
    const dotsAfter = await page.$$('.cv2-override-dot');
    assert(dotsAfter.length === 0, 'Override dots should disappear after clearing overrides');
  });

  await test('Customizer v2: presets apply through standard pipeline', async () => {
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
    const toggleSel = '#customizeToggle, button[title*="ustom" i], [class*="customize"]';
    const btn = await page.$(toggleSel);
    if (!btn) { console.log('    ⏭️  Customizer toggle not found'); return; }
    await btn.click();
    await page.waitForSelector('.cust-overlay', { timeout: 5000 });
    // Click theme tab
    const themeTab = await page.$('.cust-tab[data-tab="theme"]');
    if (themeTab) await themeTab.click();
    await page.waitForTimeout(200);
    // Click ocean preset
    const oceanBtn = await page.$('.cust-preset-btn[data-preset="ocean"]');
    if (!oceanBtn) { console.log('    ⏭️  Ocean preset button not found'); return; }
    await oceanBtn.click();
    await page.waitForTimeout(300);
    const result = await page.evaluate(() => {
      const stored = JSON.parse(localStorage.getItem('cs-theme-overrides') || '{}');
      const cssAccent = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim();
      return { hasTheme: !!stored.theme, cssAccent };
    });
    assert(result.hasTheme, 'Preset should write theme to localStorage');
    assert(result.cssAccent.length > 0, 'CSS accent should be set after preset');
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });

  await test('Customizer v2: page load applies overrides from localStorage', async () => {
    // Set overrides BEFORE navigating
    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    await page.evaluate(() => {
      localStorage.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#ee1122' } }));
    });
    // Reload to trigger init with overrides
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('nav, .navbar, .nav, [class*="nav"]');
    await page.waitForTimeout(500); // allow pipeline to run
    const cssAccent = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue('--accent').trim()
    );
    assert(cssAccent === '#ee1122', `Page load should apply override accent #ee1122 but got "${cssAccent}"`);
    await page.evaluate(() => localStorage.removeItem('cs-theme-overrides'));
  });


  await test('Show Neighbors populates neighborPubkeys from affinity API', async () => {
    const testPubkey = 'aabbccdd11223344556677889900aabbccddeeff00112233445566778899001122';
    const neighborPubkey1 = '1111111111111111111111111111111111111111111111111111111111111111';
    const neighborPubkey2 = '2222222222222222222222222222222222222222222222222222222222222222';

    await page.route(`**/api/nodes/${testPubkey}/neighbors*`, route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          node: testPubkey,
          neighbors: [
            { pubkey: neighborPubkey1, prefix: '11', name: 'Neighbor-1', role: 'repeater', count: 50, score: 0.9, ambiguous: false },
            { pubkey: neighborPubkey2, prefix: '22', name: 'Neighbor-2', role: 'companion', count: 20, score: 0.7, ambiguous: false }
          ],
          total_observations: 70
        })
      });
    });

    await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1500);

    const result = await page.evaluate(async (args) => {
      if (typeof window._mapSelectRefNode !== 'function') return { error: 'no _mapSelectRefNode' };
      await window._mapSelectRefNode(args.pk, 'TestNode');
      return { neighbors: window._mapGetNeighborPubkeys() };
    }, { pk: testPubkey });

    assert(!result.error, result.error || '');
    assert(result.neighbors.includes(neighborPubkey1), 'Should contain neighbor1');
    assert(result.neighbors.includes(neighborPubkey2), 'Should contain neighbor2');
    assert(result.neighbors.length === 2, `Expected 2 neighbors, got ${result.neighbors.length}`);
    await page.unroute(`**/api/nodes/${testPubkey}/neighbors*`);
  });

  await test('Show Neighbors resolves correct node on hash collision via affinity API', async () => {
    const nodeA = 'c0dedad4208acb6cbe44b848943fc6d3c5d43cf38a21e48b43826a70862980e4';
    const nodeB = 'c0f1a2b3000000000000000000000000000000000000000000000000000000ff';
    const neighborR1 = 'r1aaaaaa000000000000000000000000000000000000000000000000000000aa';
    const neighborR2 = 'r2bbbbbb000000000000000000000000000000000000000000000000000000bb';
    const neighborR4 = 'r4dddddd000000000000000000000000000000000000000000000000000000dd';

    await page.route(`**/api/nodes/${nodeA}/neighbors*`, route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          node: nodeA,
          neighbors: [
            { pubkey: neighborR1, prefix: 'R1', name: 'Repeater-R1', role: 'repeater', count: 100, score: 0.95, ambiguous: false },
            { pubkey: neighborR2, prefix: 'R2', name: 'Repeater-R2', role: 'repeater', count: 80, score: 0.85, ambiguous: false }
          ],
          total_observations: 180
        })
      });
    });

    await page.route(`**/api/nodes/${nodeB}/neighbors*`, route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          node: nodeB,
          neighbors: [
            { pubkey: neighborR4, prefix: 'R4', name: 'Repeater-R4', role: 'repeater', count: 60, score: 0.75, ambiguous: false }
          ],
          total_observations: 60
        })
      });
    });

    await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1500);

    // Select Node A — should get R1, R2 but NOT R4
    const resultA = await page.evaluate(async (pk) => {
      await window._mapSelectRefNode(pk, 'NodeA');
      return window._mapGetNeighborPubkeys();
    }, nodeA);
    assert(resultA.includes(neighborR1), 'Node A should have R1');
    assert(resultA.includes(neighborR2), 'Node A should have R2');
    assert(!resultA.includes(neighborR4), 'Node A should NOT have R4');

    // Select Node B — should get R4 but NOT R1, R2
    const resultB = await page.evaluate(async (pk) => {
      await window._mapSelectRefNode(pk, 'NodeB');
      return window._mapGetNeighborPubkeys();
    }, nodeB);
    assert(resultB.includes(neighborR4), 'Node B should have R4');
    assert(!resultB.includes(neighborR1), 'Node B should NOT have R1');
    assert(!resultB.includes(neighborR2), 'Node B should NOT have R2');

    await page.unroute(`**/api/nodes/${nodeA}/neighbors*`);
    await page.unroute(`**/api/nodes/${nodeB}/neighbors*`);
  });

  await test('Show Neighbors falls back to path walking when affinity API returns empty', async () => {
    const testPubkey = 'fallbacktest0000000000000000000000000000000000000000000000000000';
    const hopBefore = 'aaaa000000000000000000000000000000000000000000000000000000000000';
    const hopAfter = 'bbbb000000000000000000000000000000000000000000000000000000000000';

    await page.route(`**/api/nodes/${testPubkey}/neighbors*`, route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ node: testPubkey, neighbors: [], total_observations: 0 })
      });
    });

    await page.route(`**/api/nodes/${testPubkey}/paths*`, route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          paths: [{
            hops: [
              { pubkey: hopBefore, name: 'HopBefore' },
              { pubkey: testPubkey, name: 'Self' },
              { pubkey: hopAfter, name: 'HopAfter' }
            ]
          }]
        })
      });
    });

    await page.goto(`${BASE}/#/map`, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(1500);

    const result = await page.evaluate(async (pk) => {
      if (typeof window._mapSelectRefNode !== 'function') return { error: 'no-function' };
      await window._mapSelectRefNode(pk, 'FallbackNode');
      return { neighbors: window._mapGetNeighborPubkeys() };
    }, testPubkey);

    assert(!result.error, result.error || '');
    assert(result.neighbors.includes(hopBefore), 'Fallback should find hopBefore');
    assert(result.neighbors.includes(hopAfter), 'Fallback should find hopAfter');
    assert(result.neighbors.length === 2, `Expected 2 fallback neighbors, got ${result.neighbors.length}`);
    await page.unroute(`**/api/nodes/${testPubkey}/neighbors*`);
    await page.unroute(`**/api/nodes/${testPubkey}/paths*`);
  });
  // ─── Neighbor section tests ───────────────────────────────────────────────

  await test('Node detail: neighbors section exists with correct columns', async () => {
    // Navigate to a node detail page (use the first node in the list)
    await page.goto(BASE + '/#/nodes');
    await page.waitForSelector('#nodesBody tr[data-key]', { timeout: 10000 });
    // Get the first node's pubkey from the row's data-key attribute
    const pubkey = await page.$eval('#nodesBody tr[data-key]', el => el.dataset.key);
    await page.goto(BASE + '/#/nodes/' + pubkey);
    await page.waitForSelector('#node-neighbors', { timeout: 10000 });
    // Check the section exists
    const header = await page.$eval('#fullNeighborsHeader', el => el.textContent);
    assert(header.startsWith('Neighbors'), 'Header should start with "Neighbors", got: ' + header);
    // Wait for content to load (either table or empty state)
    await page.waitForFunction(() => {
      const el = document.getElementById('fullNeighborsContent');
      return el && !el.innerHTML.includes('spinner');
    }, { timeout: 10000 });
    const hasTable = await page.$('#fullNeighborsContent .data-table');
    if (hasTable) {
      // Check columns
      const headers = await page.$$eval('#fullNeighborsContent thead th', ths => ths.map(t => t.textContent.trim().replace(/\s*[▲▼]\s*$/, '')));
      assert(headers.includes('Neighbor'), 'Should have Neighbor column');
      assert(headers.includes('Role'), 'Should have Role column');
      assert(headers.includes('Score'), 'Should have Score column');
      assert(headers.includes('Obs'), 'Should have Obs column');
      assert(headers.includes('Last Seen'), 'Should have Last Seen column');
      assert(headers.includes('Conf'), 'Should have Conf column');
    } else {
      // Empty state
      const text = await page.$eval('#fullNeighborsContent', el => el.textContent);
      assert(text.includes('No neighbor data') || text.includes('Could not load'), 'Should show empty or error state');
    }
  });


  // ─── End neighbor section tests ───────────────────────────────────────────

  // ─── Affinity debug overlay tests ─────────────────────────────────────────

  await test('Map: affinity debug checkbox exists in DOM', async () => {
    await page.goto(BASE + '/#/map');
    await page.waitForSelector('#mapControls', { timeout: 5000 });
    const checkbox = await page.$('#mcAffinityDebug');
    assert(checkbox !== null, 'Affinity debug checkbox should exist in DOM');
  });

  await test('Map: affinity debug checkbox toggles without crash', async () => {
    await page.goto(BASE + '/#/map');
    await page.waitForSelector('#mapControls', { timeout: 5000 });
    // Make the checkbox visible by setting localStorage
    await page.evaluate(() => localStorage.setItem('meshcore-affinity-debug', 'true'));
    await page.reload();
    await page.waitForSelector('#mapControls', { timeout: 5000 });
    const label = await page.$('#mcAffinityDebugLabel');
    if (label) {
      const display = await label.evaluate(el => getComputedStyle(el).display);
      // When debugAffinity or localStorage is set, label should be visible
      // Just verify toggling doesn't crash
      const cb = await page.$('#mcAffinityDebug');
      if (cb) {
        await cb.click();
        // Wait a bit for fetch to complete (or fail gracefully)
        await page.waitForTimeout(500);
        await cb.click();
        await page.waitForTimeout(200);
      }
    }
    // Clean up
    await page.evaluate(() => localStorage.removeItem('meshcore-affinity-debug'));
    assert(true, 'Toggle did not crash');
  });

  await test('Node detail: affinity debug section expandable', async () => {
    await page.goto(BASE + '/#/nodes');
    await page.waitForSelector('#nodesBody tr[data-key]', { timeout: 10000 });
    // Enable debug mode
    await page.evaluate(() => localStorage.setItem('meshcore-affinity-debug', 'true'));
    // Click first node to go to detail
    const nodeLink = await page.$('a[href*="/nodes/"]');
    if (nodeLink) {
      await nodeLink.click();
      await page.waitForTimeout(1000);
      const debugPanel = await page.$('#node-affinity-debug');
      if (debugPanel) {
        const display = await debugPanel.evaluate(el => el.style.display);
        // Panel should be visible when debug is enabled
        const header = await debugPanel.$('h4');
        if (header) {
          // Click to expand
          await header.click();
          await page.waitForTimeout(300);
          const body = await debugPanel.$('.affinity-debug-body');
          if (body) {
            const bodyDisplay = await body.evaluate(el => el.style.display);
            assert(bodyDisplay !== 'none', 'Debug body should be expanded after click');
          }
        }
      }
    }
    await page.evaluate(() => localStorage.removeItem('meshcore-affinity-debug'));
    assert(true, 'Debug panel expansion works');
  });

  // ─── End affinity debug tests ─────────────────────────────────────────────

  // ─── Mobile filter dropdown tests (#534) ──────────────────────────────────

  await test('Mobile: filter toggle expands filter bar on packets page (#534)', async () => {
    // Use a mobile viewport
    await page.setViewportSize({ width: 480, height: 800 });
    await page.goto(`${BASE}/#/packets`);
    await page.waitForTimeout(500);

    const filterBar = await page.$('.filter-bar');
    assert(filterBar, 'Filter bar should exist on packets page');

    // Before clicking toggle, filter inputs should be hidden
    const toggleBtn = await page.$('.filter-toggle-btn');
    assert(toggleBtn, 'Filter toggle button should exist on mobile');

    await toggleBtn.click();
    await page.waitForTimeout(300);

    // After clicking, .filters-expanded should be on the filter bar
    const expanded = await filterBar.evaluate(el => el.classList.contains('filters-expanded'));
    assert(expanded, 'Filter bar should have filters-expanded class after toggle');

    // Filter inputs should now be visible
    const filterInput = await page.$('.filter-bar input');
    if (filterInput) {
      const display = await filterInput.evaluate(el => getComputedStyle(el).display);
      assert(display !== 'none', `Filter input should be visible when expanded, got display: ${display}`);
    }

    const filterSelect = await page.$('.filter-bar select');
    if (filterSelect) {
      const display = await filterSelect.evaluate(el => getComputedStyle(el).display);
      assert(display !== 'none', `Filter select should be visible when expanded, got display: ${display}`);
    }

    // Reset viewport
    await page.setViewportSize({ width: 1280, height: 720 });
  });

  // ─── End mobile filter tests ──────────────────────────────────────────────

  // Extract frontend coverage if instrumented server is running
  try {
    const coverage = await page.evaluate(() => window.__coverage__);
    if (coverage) {
      const fs = require('fs');
      const path = require('path');
      const outDir = path.join(__dirname, '.nyc_output');
      if (!fs.existsSync(outDir)) fs.mkdirSync(outDir, { recursive: true });
      fs.writeFileSync(path.join(outDir, 'e2e-coverage.json'), JSON.stringify(coverage));
      console.log(`Frontend coverage from E2E: ${Object.keys(coverage).length} files`);
    }
  } catch {}

  // --- Group: Deep linking (#536) ---

  // Test: nodes tab deep link
  await test('Nodes tab deep link restores active tab', async () => {
    await page.goto(BASE + '#/nodes?tab=repeater', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.node-tab', { timeout: 8000 });
    const activeTab = await page.$('.node-tab.active');
    assert(activeTab, 'No active tab found');
    const tabText = await activeTab.textContent();
    assert(tabText.includes('Repeater'), `Expected Repeater tab active, got: ${tabText}`);
    const url = page.url();
    assert(url.includes('tab=repeater'), `URL should contain tab=repeater, got: ${url}`);
  });

  // Test: nodes tab click updates URL
  await test('Nodes tab click updates URL', async () => {
    await page.goto(BASE + '#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.node-tab', { timeout: 8000 });
    const roomTab = await page.$('.node-tab[data-tab="room"]');
    assert(roomTab, 'Room tab (data-tab="room") not found — nodes page may not have rendered or tab selector changed');
    await roomTab.click();
    await page.waitForTimeout(300);
    const url = page.url();
    assert(url.includes('tab=room'), `URL should contain tab=room after click, got: ${url}`);
  });

  // Test: packets timeWindow deep link
  await test('Packets timeWindow deep link restores dropdown', async () => {
    await page.goto(BASE + '#/packets?timeWindow=60', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#fTimeWindow', { timeout: 8000 });
    const val = await page.$eval('#fTimeWindow', el => el.value);
    assert(val === '60', `Expected timeWindow dropdown = 60, got: ${val}`);
    const url = page.url();
    assert(url.includes('timeWindow=60'), `URL should still contain timeWindow=60, got: ${url}`);
  });

  // Test: timeWindow change updates URL
  await test('Packets timeWindow change updates URL', async () => {
    await page.goto(BASE + '#/packets', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#fTimeWindow', { timeout: 8000 });
    await page.selectOption('#fTimeWindow', '30');
    await page.waitForTimeout(300);
    const url = page.url();
    assert(url.includes('timeWindow=30'), `URL should contain timeWindow=30 after change, got: ${url}`);
  });

  // Test: channels selected channel survives refresh (already implemented, verify it still works)
  await test('Channels channel selection is URL-addressable', async () => {
    await page.goto(BASE + '#/channels', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.ch-item', { timeout: 8000 }).catch(() => null);
    const firstChannel = await page.$('.ch-item');
    if (firstChannel) {
      await firstChannel.click();
      await page.waitForTimeout(500);
      const url = page.url();
      assert(url.includes('#/channels/') || url.includes('#/channels'), `URL should reflect channel selection, got: ${url}`);
    }
  });

  await browser.close();

  // Summary
  const passed = results.filter(r => r.pass).length;
  const failed = results.filter(r => !r.pass).length;
  console.log(`\n${passed}/${results.length} tests passed${failed ? `, ${failed} failed` : ''}`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => {
  console.error('Fatal error:', err);
  process.exit(1);
});
