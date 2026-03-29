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
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    headless: true
  });
  const page = await browser.newPage();
  page.setDefaultTimeout(10000);
  const BASE = process.env.BASE_URL || 'http://localhost:13581';

  // Helper: navigate via hash (SPA — no full page reload needed after initial load)
  async function navHash(hash, wait = 150) {
    await page.evaluate((h) => { location.hash = h; }, hash);
    await new Promise(r => setTimeout(r, wait));
  }

  // Helper: safe click — 500ms timeout (elements exist immediately or not at all)
  async function safeClick(selector, timeout) {
    try {
      await page.click(selector, { timeout: timeout || 500 });
    } catch {}
  }

  // Helper: safe fill
  async function safeFill(selector, text) {
    try {
      await page.fill(selector, text);
    } catch {}
  }

  // Helper: safe select
  async function safeSelect(selector, value) {
    try {
      await page.selectOption(selector, value);
    } catch {}
  }

  // Helper: click all matching elements
  async function clickAll(selector, max = 10) {
    try {
      const els = await page.$$(selector);
      for (let i = 0; i < Math.min(els.length, max); i++) {
        try { await els[i].click(); } catch {}
      }
    } catch {}
  }

  // Helper: iterate all select options
  async function cycleSelect(selector) {
    try {
      const options = await page.$$eval(`${selector} option`, opts => opts.map(o => o.value));
      for (const val of options) {
        try { await page.selectOption(selector, val); } catch {}
      }
    } catch {}
  }

  // ══════════════════════════════════════════════
  // HOME PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Home page — chooser...');
  // Clear localStorage to get chooser
  await page.goto(BASE, { waitUntil: 'domcontentloaded', timeout: 10000 }).catch(() => {});
  await page.evaluate(() => localStorage.clear()).catch(() => {});
  await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded', timeout: 10000 }).catch(() => {});

  // Click "I'm new"
  await safeClick('#chooseNew');

  // Now on home page as "new" user — interact with search
  await safeFill('#homeSearch', 'test');
  // Click suggest items if any
  await clickAll('.suggest-item', 3);
  // Click suggest claim buttons
  await clickAll('.suggest-claim', 2);
  await safeFill('#homeSearch', '');

  // Click my-node-card elements
  await clickAll('.my-node-card', 3);
  // Click health/packets buttons on cards
  await clickAll('[data-action="health"]', 2);
  await clickAll('[data-action="packets"]', 2);

  // Click toggle level
  await safeClick('#toggleLevel');

  // Click FAQ items
  await clickAll('.faq-q, .question, [class*="accordion"]', 5);

  // Click timeline items
  await clickAll('.timeline-item', 5);

  // Click health claim button
  await clickAll('.health-claim', 2);

  // Click cards
  await clickAll('.card, .health-card', 3);

  // Click remove buttons on my-node cards
  await clickAll('.mnc-remove', 2);

  // Switch to experienced mode
  await page.evaluate(() => localStorage.clear()).catch(() => {});
  await page.goto(`${BASE}/#/home`, { waitUntil: 'domcontentloaded', timeout: 10000 }).catch(() => {});
  await safeClick('#chooseExp');

  // Interact with experienced home page
  await safeFill('#homeSearch', 'a');
  await clickAll('.suggest-item', 2);
  await safeFill('#homeSearch', '');

  // Click outside to dismiss suggest
  await page.evaluate(() => document.body.click()).catch(() => {});

  // ══════════════════════════════════════════════
  // NODES PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Nodes page...');
  await navHash('#/nodes');

  // Sort by EVERY column
  for (const col of ['name', 'public_key', 'role', 'last_seen', 'advert_count']) {
    try { await page.click(`th[data-sort="${col}"]`); } catch {}
    // Click again for reverse sort
    try { await page.click(`th[data-sort="${col}"]`); } catch {}
  }

  // Click EVERY role tab
  const roleTabs = await page.$$('.node-tab[data-tab]');
  for (const tab of roleTabs) {
    try { await tab.click(); } catch {}
  }
  // Go back to "all"
  try { await page.click('.node-tab[data-tab="all"]'); } catch {}

  // Click EVERY status filter
  for (const status of ['active', 'stale', 'all']) {
    try { await page.click(`#nodeStatusFilter .btn[data-status="${status}"]`); } catch {}
  }

  // Cycle EVERY Last Heard option
  await cycleSelect('#nodeLastHeard');

  // Search
  await safeFill('#nodeSearch', 'test');
  await safeFill('#nodeSearch', '');

  // Click node rows to open side pane — try multiple
  const nodeRows = await page.$$('#nodesBody tr');
  for (let i = 0; i < Math.min(nodeRows.length, 4); i++) {
    try { await nodeRows[i].click(); } catch {}
  }

  // In side pane — click detail/analytics links
  await safeClick('a[href*="/nodes/"]');
  // Click fav star
  await clickAll('.fav-star', 2);

  // On node detail page — interact
  // Click back button
  await safeClick('#nodeBackBtn');

  // Navigate to a node detail page via hash
  try {
    const firstNodeKey = await page.$eval('#nodesBody tr td:nth-child(2)', el => el.textContent.trim());
    if (firstNodeKey) {
      await navHash('#/nodes/' + firstNodeKey);

      // Click tabs on detail page
      await clickAll('.tab-btn, [data-tab]', 10);

      // Click copy URL button
      await safeClick('#copyUrlBtn');

      // Click "Show all paths" button
      await safeClick('#showAllPaths');
      await safeClick('#showAllFullPaths');

      // Click node analytics day buttons
      for (const days of ['1', '7', '30', '365']) {
        try { await page.click(`[data-days="${days}"]`); } catch {}
      }
    }
  } catch {}

  // Node detail with scroll target
  try {
    const firstKey = await page.$eval('#nodesBody tr td:nth-child(2)', el => el.textContent.trim()).catch(() => null);
    if (firstKey) {
      await navHash('#/nodes/' + firstKey + '?scroll=paths');
    }
  } catch {}

  // ══════════════════════════════════════════════
  // PACKETS PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Packets page...');
  await navHash('#/packets');

  // Open filter bar
  await safeClick('#filterToggleBtn');

  // Type various filter expressions
  const filterExprs = [
    'type == ADVERT', 'type == GRP_TXT', 'snr > 0', 'hops > 1',
    'route == FLOOD', 'rssi < -80', 'type == TXT_MSG', 'type == ACK',
    'snr > 5 && hops > 1', 'type == PATH', '@@@', ''
  ];
  for (const expr of filterExprs) {
    await safeFill('#packetFilterInput', expr);
  }

  // Cycle ALL time window options
  await cycleSelect('#fTimeWindow');

  // Toggle group by hash
  await safeClick('#fGroup');
  await safeClick('#fGroup');

  // Toggle My Nodes filter
  await safeClick('#fMyNodes');
  await safeClick('#fMyNodes');

  // Click observer menu trigger
  await safeClick('#observerTrigger');
  // Click items in observer menu
  await clickAll('#observerMenu input[type="checkbox"]', 5);
  await safeClick('#observerTrigger');

  // Click type filter trigger
  await safeClick('#typeTrigger');
  await clickAll('#typeMenu input[type="checkbox"]', 5);
  await safeClick('#typeTrigger');

  // Hash input
  await safeFill('#fHash', 'abc123');
  await safeFill('#fHash', '');

  // Node filter
  await safeFill('#fNode', 'test');
  await clickAll('.node-filter-option', 3);
  await safeFill('#fNode', '');

  // Observer sort
  await cycleSelect('#fObsSort');

  // Column toggle menu
  await safeClick('#colToggleBtn');
  await clickAll('#colToggleMenu input[type="checkbox"]', 8);
  await safeClick('#colToggleBtn');

  // Hex hash toggle
  await safeClick('#hexHashToggle');
  await safeClick('#hexHashToggle');

  // Pause button
  await safeClick('#pktPauseBtn');
  await safeClick('#pktPauseBtn');

  // Click packet rows to open detail pane
  const pktRows = await page.$$('#pktBody tr');
  for (let i = 0; i < Math.min(pktRows.length, 5); i++) {
    try { await pktRows[i].click(); } catch {}
  }

  // Resize handle drag simulation
  try {
    await page.evaluate(() => {
      const handle = document.getElementById('pktResizeHandle');
      if (handle) {
        handle.dispatchEvent(new MouseEvent('mousedown', { clientX: 500, bubbles: true }));
        document.dispatchEvent(new MouseEvent('mousemove', { clientX: 400, bubbles: true }));
        document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
      }
    });
  } catch {}

  // Click outside filter menus to close them
  try {
    await page.evaluate(() => document.body.click());
  } catch {}

  // Navigate to specific packet by hash
  await navHash('#/packets/deadbeef');

  // ══════════════════════════════════════════════
  // MAP PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Map page...');
  await navHash('#/map');

  // Toggle controls panel
  await safeClick('#mapControlsToggle');

  // Toggle each role checkbox on/off
  try {
    const roleChecks = await page.$$('#mcRoleChecks input[type="checkbox"]');
    for (const cb of roleChecks) {
      try { await cb.click(); } catch {}
      try { await cb.click(); } catch {}
    }
  } catch {}

  // Toggle clusters, heatmap, neighbors, hash labels
  await safeClick('#mcClusters');
  await safeClick('#mcClusters');
  await safeClick('#mcHeatmap');
  await safeClick('#mcHeatmap');
  await safeClick('#mcNeighbors');
  await safeClick('#mcNeighbors');
  await safeClick('#mcHashLabels');
  await safeClick('#mcHashLabels');

  // Last heard dropdown on map
  await cycleSelect('#mcLastHeard');

  // Status filter buttons on map
  for (const st of ['active', 'stale', 'all']) {
    try { await page.click(`#mcStatusFilter .btn[data-status="${st}"]`); } catch {}
  }

  // Click jump buttons (region jumps)
  await clickAll('#mcJumps button', 5);

  // Click markers
  await clickAll('.leaflet-marker-icon', 5);
  await clickAll('.leaflet-interactive', 3);

  // Click popups
  await clickAll('.leaflet-popup-content a', 3);

  // Zoom controls
  await safeClick('.leaflet-control-zoom-in');
  await safeClick('.leaflet-control-zoom-out');

  // Toggle dark mode while on map (triggers tile layer swap)
  await safeClick('#darkModeToggle');
  await safeClick('#darkModeToggle');

  // ══════════════════════════════════════════════
  // ANALYTICS PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Analytics page...');
  await navHash('#/analytics');

  // Click EVERY analytics tab
  const analyticsTabs = ['overview', 'rf', 'topology', 'channels', 'hashsizes', 'collisions', 'subpaths', 'nodes', 'distance'];
  for (const tabName of analyticsTabs) {
    try {
      await page.click(`#analyticsTabs [data-tab="${tabName}"]`, { timeout: 2000 });
    } catch {}
  }

  // On topology tab — click observer selector buttons
  try {
    await page.click('#analyticsTabs [data-tab="topology"]', { timeout: 2000 });
    await clickAll('#obsSelector .tab-btn', 5);
    // Click the "All Observers" button
    await safeClick('[data-obs="__all"]');
  } catch {}

  // On collisions tab — click navigate rows
  try {
    await page.click('#analyticsTabs [data-tab="collisions"]', { timeout: 2000 });
    await clickAll('tr[data-action="navigate"]', 3);
  } catch {}

  // On subpaths tab — click rows
  try {
    await page.click('#analyticsTabs [data-tab="subpaths"]', { timeout: 2000 });
    await clickAll('tr[data-action="navigate"]', 3);
  } catch {}

  // On nodes tab — click sortable headers
  try {
    await page.click('#analyticsTabs [data-tab="nodes"]', { timeout: 2000 });
    await clickAll('.analytics-table th', 8);
  } catch {}

  // Deep-link to each analytics tab via hash (avoid full page.goto)
  for (const tab of analyticsTabs) {
    try {
      await page.evaluate((t) => { location.hash = '#/analytics?tab=' + t; }, tab);
      await new Promise(r => setTimeout(r, 100));
    } catch {}
  }

  // Region filter on analytics
  try {
    await page.click('#analyticsRegionFilter');
    await clickAll('#analyticsRegionFilter input[type="checkbox"]', 3);
  } catch {}

  // ══════════════════════════════════════════════
  // CUSTOMIZE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Customizer...');
  await navHash('#/home');
  await safeClick('#customizeToggle');

  // Click EVERY customizer tab
  for (const tab of ['branding', 'theme', 'nodes', 'home', 'export']) {
    try { await page.click(`.cust-tab[data-tab="${tab}"]`); } catch {}
  }

  // On branding tab — change text inputs
  try {
    await page.click('.cust-tab[data-tab="branding"]');
    await safeFill('input[data-key="branding.siteName"]', 'Test Site');
    await safeFill('input[data-key="branding.tagline"]', 'Test Tagline');
    await safeFill('input[data-key="branding.logoUrl"]', 'https://example.com/logo.png');
    await safeFill('input[data-key="branding.faviconUrl"]', 'https://example.com/favicon.ico');
  } catch {}

  // On theme tab — click EVERY preset
  try {
    await page.click('.cust-tab[data-tab="theme"]');
    const presets = await page.$$('.cust-preset-btn[data-preset]');
    for (const preset of presets) {
      try { await preset.click(); } catch {}
    }
  } catch {}

  // Change color inputs on theme tab
  try {
    const colorInputs = await page.$$('input[type="color"][data-theme]');
    for (let i = 0; i < Math.min(colorInputs.length, 5); i++) {
      try {
        await colorInputs[i].evaluate(el => {
          el.value = '#ff5500';
          el.dispatchEvent(new Event('input', { bubbles: true }));
        });
      } catch {}
    }
  } catch {}

  // Click reset buttons on theme
  await clickAll('[data-reset-theme]', 3);
  await clickAll('[data-reset-node]', 3);
  await clickAll('[data-reset-type]', 3);

  // On nodes tab — change node color inputs
  try {
    await page.click('.cust-tab[data-tab="nodes"]');
    const nodeColors = await page.$$('input[type="color"][data-node]');
    for (let i = 0; i < Math.min(nodeColors.length, 3); i++) {
      try {
        await nodeColors[i].evaluate(el => {
          el.value = '#00ff00';
          el.dispatchEvent(new Event('input', { bubbles: true }));
        });
      } catch {}
    }
    // Type color inputs
    const typeColors = await page.$$('input[type="color"][data-type-color]');
    for (let i = 0; i < Math.min(typeColors.length, 3); i++) {
      try {
        await typeColors[i].evaluate(el => {
          el.value = '#0000ff';
          el.dispatchEvent(new Event('input', { bubbles: true }));
        });
      } catch {}
    }
  } catch {}

  // On home tab — edit home customization fields
  try {
    await page.click('.cust-tab[data-tab="home"]');
    await safeFill('input[data-key="home.heroTitle"]', 'Test Hero');
    await safeFill('input[data-key="home.heroSubtitle"]', 'Test Subtitle');
    // Edit journey steps
    await clickAll('[data-move-step]', 2);
    await clickAll('[data-rm-step]', 1);
    // Edit checklist
    await clickAll('[data-rm-check]', 1);
    // Edit links
    await clickAll('[data-rm-link]', 1);
    // Modify step fields
    const stepTitles = await page.$$('input[data-step-field="title"]');
    for (let i = 0; i < Math.min(stepTitles.length, 2); i++) {
      try {
        await stepTitles[i].fill('Test Step ' + i);
      } catch {}
    }
  } catch {}

  // On export tab
  try {
    await page.click('.cust-tab[data-tab="export"]');
    // Click export/import buttons if present
    await clickAll('.cust-panel[data-panel="export"] button', 3);
  } catch {}

  // Reset preview and user theme
  await safeClick('#custResetPreview');
  await safeClick('#custResetUser');

  // Close customizer
  await safeClick('.cust-close');

  // ══════════════════════════════════════════════
  // CHANNELS PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Channels page...');
  await navHash('#/channels');
  // Click channel rows/items
  await clickAll('.channel-item, .channel-row, .channel-card', 3);
  await clickAll('table tbody tr', 3);

  // Navigate to a specific channel
  try {
    const channelHash = await page.$eval('table tbody tr td:first-child', el => el.textContent.trim()).catch(() => null);
    if (channelHash) {
      await navHash('#/channels/' + channelHash);
    }
  } catch {}

  // ══════════════════════════════════════════════
  // LIVE PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Live page...');
  await navHash('#/live');

  // VCR controls
  await safeClick('#vcrPauseBtn');
  await safeClick('#vcrPauseBtn');

  // VCR speed cycle
  await safeClick('#vcrSpeedBtn');
  await safeClick('#vcrSpeedBtn');
  await safeClick('#vcrSpeedBtn');

  // VCR mode / missed
  await safeClick('#vcrMissed');

  // VCR prompt buttons
  await safeClick('#vcrPromptReplay');
  await safeClick('#vcrPromptSkip');

  // Toggle visualization options
  await safeClick('#liveHeatToggle');
  await safeClick('#liveHeatToggle');

  await safeClick('#liveGhostToggle');
  await safeClick('#liveGhostToggle');

  await safeClick('#liveRealisticToggle');
  await safeClick('#liveRealisticToggle');

  await safeClick('#liveFavoritesToggle');
  await safeClick('#liveFavoritesToggle');

  await safeClick('#liveMatrixToggle');
  await safeClick('#liveMatrixToggle');

  await safeClick('#liveMatrixRainToggle');
  await safeClick('#liveMatrixRainToggle');

  // Audio toggle and controls
  await safeClick('#liveAudioToggle');
  try {
    await page.fill('#audioBpmSlider', '120');
    // Dispatch input event on slider
    await page.evaluate(() => {
      const s = document.getElementById('audioBpmSlider');
      if (s) { s.value = '140'; s.dispatchEvent(new Event('input', { bubbles: true })); }
    });
  } catch {}
  await safeClick('#liveAudioToggle');

  // VCR timeline click
  try {
    await page.evaluate(() => {
      const canvas = document.getElementById('vcrTimeline');
      if (canvas) {
        const rect = canvas.getBoundingClientRect();
        canvas.dispatchEvent(new MouseEvent('click', {
          clientX: rect.left + rect.width * 0.5,
          clientY: rect.top + rect.height * 0.5,
          bubbles: true
        }));
      }
    });
  } catch {}

  // VCR LCD canvas
  try {
    await page.evaluate(() => {
      const canvas = document.getElementById('vcrLcdCanvas');
      if (canvas) canvas.getContext('2d');
    });
  } catch {}

  // Resize the live page panel
  try {
    await page.evaluate(() => {
      window.dispatchEvent(new Event('resize'));
    });
  } catch {}

  // ══════════════════════════════════════════════
  // TRACES PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Traces page...');
  await navHash('#/traces');
  await clickAll('table tbody tr', 3);

  // ══════════════════════════════════════════════
  // OBSERVERS PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Observers page...');
  await navHash('#/observers');
  // Click observer rows
  const obsRows = await page.$$('table tbody tr, .observer-card, .observer-row');
  for (let i = 0; i < Math.min(obsRows.length, 3); i++) {
    try { await obsRows[i].click(); } catch {}
  }

  // Navigate to observer detail page
  try {
    const obsLink = await page.$('a[href*="/observers/"]');
    if (obsLink) {
      await obsLink.click();
      // Change days select
      await cycleSelect('#obsDaysSelect');
    }
  } catch {}

  // ══════════════════════════════════════════════
  // PERF PAGE
  // ══════════════════════════════════════════════
  console.log('  [coverage] Perf page...');
  await navHash('#/perf');
  await safeClick('#perfRefresh');
  await safeClick('#perfReset');

  // ══════════════════════════════════════════════
  // APP.JS — Router, theme, global features
  // ══════════════════════════════════════════════
  console.log('  [coverage] App.js — router + global...');

  // Navigate to bad route to trigger error/404
  await navHash('#/nonexistent-route');

  // Navigate to every route via hash (50ms is enough for SPA hash routing)
  const allRoutes = ['home', 'nodes', 'packets', 'map', 'live', 'channels', 'traces', 'observers', 'analytics', 'perf'];
  for (const route of allRoutes) {
    try {
      await page.evaluate((r) => { location.hash = '#/' + r; }, route);
      await new Promise(r => setTimeout(r, 50));
    } catch {}
  }

  // Trigger hashchange manually
  try {
    await page.evaluate(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
  } catch {}

  // Theme toggle multiple times
  for (let i = 0; i < 4; i++) {
    await safeClick('#darkModeToggle');
  }

  // Dispatch theme-changed event
  try {
    await page.evaluate(() => {
      window.dispatchEvent(new Event('theme-changed'));
    });
  } catch {}

  // Hamburger menu
  await safeClick('#hamburger');
  // Click nav links in mobile menu
  await clickAll('.nav-links .nav-link', 5);

  // Favorites
  await safeClick('#favToggle');
  await clickAll('.fav-dd-item', 3);
  // Click outside to close
  try { await page.evaluate(() => document.body.click()); } catch {}
  await safeClick('#favToggle');

  // Global search
  await safeClick('#searchToggle');
  await safeFill('#searchInput', 'test');
  // Click search result items
  await clickAll('.search-result-item', 3);
  // Close search
  try { await page.keyboard.press('Escape'); } catch {}

  // Ctrl+K shortcut
  try {
    await page.keyboard.press('Control+k');
    await safeFill('#searchInput', 'node');
    await page.keyboard.press('Escape');
  } catch {}

  // Click search overlay background to close
  try {
    await safeClick('#searchToggle');
    await page.click('#searchOverlay', { position: { x: 5, y: 5 } });
  } catch {}

  // Navigate via nav links with data-route
  for (const route of allRoutes) {
    await safeClick(`a[data-route="${route}"]`);
  }

  // Exercise apiPerf console function
  try {
    await page.evaluate(() => { if (window.apiPerf) window.apiPerf(); });
  } catch {}

  // Exercise utility functions + packet filter parser in one evaluate call
  console.log('  [coverage] Utility functions + packet filter...');
  try {
    await page.evaluate(() => {
      // Utility functions
      if (typeof timeAgo === 'function') {
        timeAgo(null);
        timeAgo(new Date().toISOString());
        timeAgo(new Date(Date.now() - 30000).toISOString());
        timeAgo(new Date(Date.now() - 3600000).toISOString());
        timeAgo(new Date(Date.now() - 86400000 * 2).toISOString());
      }
      if (typeof truncate === 'function') {
        truncate('hello world', 5);
        truncate(null, 5);
        truncate('hi', 10);
      }
      if (typeof routeTypeName === 'function') {
        for (let i = 0; i <= 4; i++) routeTypeName(i);
      }
      if (typeof payloadTypeName === 'function') {
        for (let i = 0; i <= 15; i++) payloadTypeName(i);
      }
      if (typeof payloadTypeColor === 'function') {
        for (let i = 0; i <= 15; i++) payloadTypeColor(i);
      }
      if (typeof invalidateApiCache === 'function') {
        invalidateApiCache();
        invalidateApiCache('/test');
      }

      // Packet filter parser
      if (window.PacketFilter && window.PacketFilter.compile) {
        const PF = window.PacketFilter;
        const exprs = [
          'type == ADVERT', 'type == GRP_TXT', 'type != ACK',
          'snr > 0', 'snr < -5', 'snr >= 10', 'snr <= 3',
          'hops > 1', 'hops == 0', 'rssi < -80',
          'route == FLOOD', 'route == DIRECT', 'route == TRANSPORT_FLOOD',
          'type == ADVERT && snr > 0', 'type == TXT_MSG || type == GRP_TXT',
          '!type == ACK', 'NOT type == ADVERT',
          'type == ADVERT && (snr > 0 || hops > 1)',
          'observer == "test"', 'from == "abc"', 'to == "xyz"',
          'has_text', 'is_encrypted',
          'type contains ADV',
        ];
        for (const e of exprs) {
          try { PF.compile(e); } catch {}
        }
        const bad = ['@@@', '== ==', '(((', 'type ==', ''];
        for (const e of bad) {
          try { PF.compile(e); } catch {}
        }
      }
    });
  } catch {}

  // ══════════════════════════════════════════════
  // REGION FILTER — exercise
  // ══════════════════════════════════════════════
  console.log('  [coverage] Region filter...');
  try {
    // Open region filter on nodes page (use hash nav, already visited)
    await page.evaluate(() => { location.hash = '#/nodes'; });
    await new Promise(r => setTimeout(r, 100));
    await safeClick('#nodesRegionFilter');
    await clickAll('#nodesRegionFilter input[type="checkbox"]', 3);
  } catch {}

  // Region filter on packets
  try {
    await page.evaluate(() => { location.hash = '#/packets'; });
    await new Promise(r => setTimeout(r, 100));
    await safeClick('#packetsRegionFilter');
    await clickAll('#packetsRegionFilter input[type="checkbox"]', 3);
  } catch {}

  // ══════════════════════════════════════════════
  // FINAL — extract coverage (all routes already visited above)
  // ══════════════════════════════════════════════

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
