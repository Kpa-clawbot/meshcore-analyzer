// Parallel frontend coverage collector
// Visits every page and exercises key UI interactions across 7 parallel browser contexts.
// Merges coverage JSONs at the end. Target: < 2 minutes.
//
// Skips interactions already covered by E2E tests (test-e2e-playwright.js):
//   - Assertions on page load, data presence, column headers
//   - Compare page (fully covered by E2E)
//   - Audio Lab page (fully covered by E2E)
//   - Map localStorage persistence, resize tests
//   - Analytics sub-tab content assertions
//   - Channels message click, Traces search, Observers health dots
// Focuses on: code paths E2E doesn't touch (customizer presets, filter expressions,
// VCR controls, node detail tabs, packet column toggles, utility functions, etc.)

const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const CLICK_TIMEOUT = 100;   // ms — elements exist immediately or not at all
const NAV_WAIT = 50;         // ms — SPA hash routing is instant

async function run() {
  const browser = await chromium.launch({
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    headless: true
  });

  // Shared helpers factory — each group gets its own page
  function helpers(page) {
    page.setDefaultTimeout(5000);
    const nav = async (hash) => {
      await page.evaluate((h) => { location.hash = h; }, hash);
      await page.waitForTimeout(NAV_WAIT);
    };
    const click = async (sel) => {
      try { await page.click(sel, { timeout: CLICK_TIMEOUT }); } catch {}
    };
    const fill = async (sel, text) => {
      try { await page.fill(sel, text); } catch {}
    };
    const clickAll = async (sel, max = 10) => {
      try {
        const els = await page.$$(sel);
        for (let i = 0; i < Math.min(els.length, max); i++) {
          try { await els[i].click(); } catch {}
        }
      } catch {}
    };
    const cycleSelect = async (sel) => {
      try {
        const opts = await page.$$eval(`${sel} option`, o => o.map(x => x.value));
        for (const v of opts) { try { await page.selectOption(sel, v); } catch {} }
      } catch {}
    };
    const init = async () => {
      await page.goto(BASE, { waitUntil: 'domcontentloaded', timeout: 10000 });
    };
    return { nav, click, fill, clickAll, cycleSelect, init, page };
  }

  // ── Group definitions ──────────────────────────────────────────────

  // Group 1: Home + Customizer
  async function group1() {
    const ctx = await browser.newContext();
    const { nav, click, fill, clickAll, init, page } = helpers(await ctx.newPage());
    await init();
    console.log('  [cov] G1: Home + Customizer');

    // Chooser flow
    await page.evaluate(() => localStorage.clear());
    await nav('#/home');
    await click('#chooseNew');
    await fill('#homeSearch', 'test');
    await clickAll('.suggest-item', 3);
    await clickAll('.suggest-claim', 2);
    await fill('#homeSearch', '');
    await clickAll('.my-node-card', 3);
    await clickAll('[data-action="health"]', 2);
    await clickAll('[data-action="packets"]', 2);
    await click('#toggleLevel');
    await clickAll('.faq-q, .question, [class*="accordion"]', 5);
    await clickAll('.timeline-item', 5);
    await clickAll('.health-claim', 2);
    await clickAll('.card, .health-card', 3);
    await clickAll('.mnc-remove', 2);

    // Experienced mode
    await page.evaluate(() => localStorage.clear());
    await nav('#/home');
    await click('#chooseExp');
    await fill('#homeSearch', 'a');
    await clickAll('.suggest-item', 2);
    await fill('#homeSearch', '');
    await page.evaluate(() => document.body.click());

    // Set myNodes localStorage and revisit home for personalized view
    await page.evaluate(() => {
      localStorage.setItem('myNodes', JSON.stringify([
        '8a91f89957e6d30ca51f36e28790228971c473b755f244f718754cf5ee4a2fd5',
        'aabb111111111111111111111111111111111111111111111111111111111111',
        '1122000000000000000000000000000000000000000000000000000000000000'
      ]));
    });
    await nav('#/home');
    await page.waitForTimeout(200);
    // Interact with personalized home (my-node cards should appear)
    await clickAll('.my-node-card', 5);
    await clickAll('[data-action="health"]', 3);
    await clickAll('[data-action="packets"]', 3);
    await clickAll('.mnc-remove', 1);

    // Customizer — open ALL tabs and toggle ALL sections
    await click('#customizeToggle');
    for (const tab of ['branding', 'theme', 'nodes', 'home', 'export']) {
      try { await page.click(`.cust-tab[data-tab="${tab}"]`, { timeout: CLICK_TIMEOUT }); } catch {}
    }
    // Branding
    try {
      await page.click('.cust-tab[data-tab="branding"]', { timeout: CLICK_TIMEOUT });
      await fill('input[data-key="branding.siteName"]', 'Test');
      await fill('input[data-key="branding.tagline"]', 'Tag');
    } catch {}
    // Theme presets
    try {
      await page.click('.cust-tab[data-tab="theme"]', { timeout: CLICK_TIMEOUT });
      await clickAll('.cust-preset-btn[data-preset]', 20);
      const colorInputs = await page.$$('input[type="color"][data-theme]');
      for (let i = 0; i < Math.min(colorInputs.length, 3); i++) {
        await colorInputs[i].evaluate(el => { el.value = '#ff5500'; el.dispatchEvent(new Event('input', { bubbles: true })); });
      }
    } catch {}
    await clickAll('[data-reset-theme]', 3);
    await clickAll('[data-reset-node]', 3);
    // Nodes tab colors
    try {
      await page.click('.cust-tab[data-tab="nodes"]', { timeout: CLICK_TIMEOUT });
      const nc = await page.$$('input[type="color"][data-node]');
      for (let i = 0; i < Math.min(nc.length, 3); i++) {
        await nc[i].evaluate(el => { el.value = '#00ff00'; el.dispatchEvent(new Event('input', { bubbles: true })); });
      }
      const tc = await page.$$('input[type="color"][data-type-color]');
      for (let i = 0; i < Math.min(tc.length, 3); i++) {
        await tc[i].evaluate(el => { el.value = '#0000ff'; el.dispatchEvent(new Event('input', { bubbles: true })); });
      }
    } catch {}
    // Home tab
    try {
      await page.click('.cust-tab[data-tab="home"]', { timeout: CLICK_TIMEOUT });
      await fill('input[data-key="home.heroTitle"]', 'Hero');
      await clickAll('[data-move-step]', 2);
      await clickAll('[data-rm-step]', 1);
      await clickAll('[data-rm-check]', 1);
      await clickAll('[data-rm-link]', 1);
    } catch {}
    // Export tab
    try {
      await page.click('.cust-tab[data-tab="export"]', { timeout: CLICK_TIMEOUT });
      await clickAll('.cust-panel[data-panel="export"] button', 3);
    } catch {}
    await click('#custResetPreview');
    await click('#custResetUser');
    await click('.cust-close');

    const cov = await page.evaluate(() => window.__coverage__);
    await ctx.close();
    return cov;
  }

  // Group 2: Nodes + Node Detail
  async function group2() {
    const ctx = await browser.newContext();
    const { nav, click, fill, clickAll, cycleSelect, init, page } = helpers(await ctx.newPage());
    await init();
    console.log('  [cov] G2: Nodes');

    await nav('#/nodes');
    // Sort columns
    for (const col of ['name', 'public_key', 'role', 'last_seen', 'advert_count']) {
      try { await page.click(`th[data-sort="${col}"]`, { timeout: CLICK_TIMEOUT }); } catch {}
      try { await page.click(`th[data-sort="${col}"]`, { timeout: CLICK_TIMEOUT }); } catch {}
    }
    // Role tabs
    await clickAll('.node-tab[data-tab]', 20);
    try { await page.click('.node-tab[data-tab="all"]', { timeout: CLICK_TIMEOUT }); } catch {}
    // Status filter
    for (const s of ['active', 'stale', 'all']) {
      try { await page.click(`#nodeStatusFilter .btn[data-status="${s}"]`, { timeout: CLICK_TIMEOUT }); } catch {}
    }
    await cycleSelect('#nodeLastHeard');
    await fill('#nodeSearch', 'test');
    await fill('#nodeSearch', '');
    // Click rows for side pane
    const rows = await page.$$('#nodesBody tr');
    for (let i = 0; i < Math.min(rows.length, 3); i++) {
      try { await rows[i].click(); } catch {}
    }
    await click('a[href*="/nodes/"]');
    await clickAll('.fav-star', 2);
    await click('#nodeBackBtn');

    // Node detail
    try {
      const key = await page.$eval('#nodesBody tr td:nth-child(2)', el => el.textContent.trim());
      if (key) {
        await nav('#/nodes/' + key);
        await clickAll('.tab-btn, [data-tab]', 10);
        await click('#copyUrlBtn');
        await click('#showAllPaths');
        await click('#showAllFullPaths');
        for (const d of ['1', '7', '30', '365']) {
          try { await page.click(`[data-days="${d}"]`, { timeout: CLICK_TIMEOUT }); } catch {}
        }
        await nav('#/nodes/' + key + '?scroll=paths');
      }
    } catch {}

    // Region filter
    await nav('#/nodes');
    await click('#nodesRegionFilter');
    await clickAll('#nodesRegionFilter input[type="checkbox"]', 3);

    // Node analytics page (node-analytics.js)
    await nav('#/nodes/8a91f89957e6d30ca51f36e28790228971c473b755f244f718754cf5ee4a2fd5/analytics');
    await page.waitForTimeout(300);
    // Click day buttons on analytics
    for (const d of ['1', '7', '30']) {
      try { await page.click(`[data-days="${d}"]`, { timeout: CLICK_TIMEOUT }); } catch {}
    }
    // Try another node's analytics
    await nav('#/nodes/aabb111111111111111111111111111111111111111111111111111111111111/analytics');
    await page.waitForTimeout(200);

    // Exercise nodes.js functions via page.evaluate
    await page.evaluate(() => {
      // getStatusInfo
      if (typeof getStatusInfo === 'function') {
        const nodes = [
          { role: 'repeater', last_heard: new Date().toISOString(), last_seen: new Date().toISOString(), public_key: 'abc', name: 'TestNode' },
          { role: 'companion', last_heard: new Date(Date.now() - 999999999).toISOString(), last_seen: null, public_key: 'def', name: 'OldNode' },
          { role: 'room', last_heard: null, last_seen: null, public_key: 'ghi', name: 'NoTime' },
          { role: 'sensor', last_heard: new Date().toISOString(), last_seen: new Date().toISOString(), public_key: 'jkl', name: 'Sensor1' },
        ];
        for (const n of nodes) {
          try { getStatusInfo(n); } catch {}
        }
      }
      // renderNodeBadges
      if (typeof renderNodeBadges === 'function') {
        try { renderNodeBadges({ battery_mv: 3700, temperature_c: 25, hash_size: 4, hash_size_inconsistent: true, role: 'repeater' }, '#ff0000'); } catch {}
        try { renderNodeBadges({ battery_mv: null, temperature_c: null, hash_size: null, hash_size_inconsistent: false, role: 'companion' }, '#00ff00'); } catch {}
      }
      // renderStatusExplanation
      if (typeof renderStatusExplanation === 'function') {
        try { renderStatusExplanation({ role: 'repeater', last_heard: new Date().toISOString(), last_seen: new Date().toISOString() }); } catch {}
        try { renderStatusExplanation({ role: 'companion', last_heard: null, last_seen: null }); } catch {}
      }
      // renderHashInconsistencyWarning
      if (typeof renderHashInconsistencyWarning === 'function') {
        try { renderHashInconsistencyWarning({ hash_size_inconsistent: true, hash_size: 4 }); } catch {}
        try { renderHashInconsistencyWarning({ hash_size_inconsistent: false }); } catch {}
      }
    }).catch(() => {});

    const cov = await page.evaluate(() => window.__coverage__);
    await ctx.close();
    return cov;
  }

  // Group 3: Packets + Packet Detail
  async function group3() {
    const ctx = await browser.newContext();
    const { nav, click, fill, clickAll, cycleSelect, init, page } = helpers(await ctx.newPage());
    await init();
    console.log('  [cov] G3: Packets');

    await nav('#/packets');
    await click('#filterToggleBtn');

    // Filter expressions (exercises packet-filter.js parser)
    for (const expr of ['type == ADVERT', 'snr > 0', 'hops > 1', 'route == FLOOD',
      'snr > 5 && hops > 1', 'type == TXT_MSG || type == GRP_TXT', '!type == ACK',
      'type == ADVERT && (snr > 0 || hops > 1)', '@@@', '']) {
      await fill('#packetFilterInput', expr);
    }

    await cycleSelect('#fTimeWindow');
    await click('#fGroup'); await click('#fGroup');
    await click('#fMyNodes'); await click('#fMyNodes');

    // Observer/type menus
    await click('#observerTrigger');
    await clickAll('#observerMenu input[type="checkbox"]', 5);
    await click('#observerTrigger');
    await click('#typeTrigger');
    await clickAll('#typeMenu input[type="checkbox"]', 5);
    await click('#typeTrigger');

    await fill('#fHash', 'abc123'); await fill('#fHash', '');
    await fill('#fNode', 'test');
    await clickAll('.node-filter-option', 3);
    await fill('#fNode', '');
    await cycleSelect('#fObsSort');

    // Column toggle
    await click('#colToggleBtn');
    await clickAll('#colToggleMenu input[type="checkbox"]', 8);
    await click('#colToggleBtn');

    await click('#hexHashToggle'); await click('#hexHashToggle');
    await click('#pktPauseBtn'); await click('#pktPauseBtn');

    // Click packet rows
    const rows = await page.$$('#pktBody tr');
    for (let i = 0; i < Math.min(rows.length, 3); i++) {
      try { await rows[i].click(); } catch {}
    }

    // Resize handle
    await page.evaluate(() => {
      const h = document.getElementById('pktResizeHandle');
      if (h) {
        h.dispatchEvent(new MouseEvent('mousedown', { clientX: 500, bubbles: true }));
        document.dispatchEvent(new MouseEvent('mousemove', { clientX: 400, bubbles: true }));
        document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
      }
    });

    await page.evaluate(() => document.body.click());
    await nav('#/packets/deadbeef');

    // Navigate to a real packet hash
    await nav('#/packets/b6b839cb61eead4a');
    await page.waitForTimeout(200);

    // Region filter
    await nav('#/packets');
    await click('#packetsRegionFilter');
    await clickAll('#packetsRegionFilter input[type="checkbox"]', 3);

    // Exercise packet detail/hex rendering functions via page.evaluate
    await page.evaluate(() => {
      // renderDecodedPacket if available globally
      if (typeof renderDecodedPacket === 'function') {
        try {
          renderDecodedPacket({
            header: { routeType: 1, payloadType: 5, payloadVersion: 1, payloadTypeName: 'GRP_TXT' },
            payload: { channelHash: 0, text: 'hello', from: 'abc' },
            path: { hops: ['A1B2', 'C3D4'] }
          }, '154000E9221C303193541599CC382A78FDAABFAF858F');
        } catch {}
        try {
          renderDecodedPacket({
            header: { routeType: 0, payloadType: 0, payloadVersion: 1, payloadTypeName: 'ADVERT' },
            payload: { pubKey: 'aabb', name: 'TestNode', role: 'repeater', lat: 37.3, lon: -121.8 },
            path: { hops: [] }
          }, 'FF00AABB');
        } catch {}
      }

      // Exercise obsName if available
      if (typeof obsName === 'function') {
        try { obsName('test-obs-1'); } catch {}
        try { obsName('unknown-obs'); } catch {}
        try { obsName(null); } catch {}
      }

      // Exercise renderPath/renderHop if available
      if (typeof renderPath === 'function') {
        try { renderPath(['A1B2', 'C3D4'], 'test-obs-1'); } catch {}
        try { renderPath([], 'test-obs-1'); } catch {}
      }
      if (typeof renderHop === 'function') {
        try { renderHop('A1B2', 'test-obs-1'); } catch {}
      }
    }).catch(() => {});

    const cov = await page.evaluate(() => window.__coverage__);
    await ctx.close();
    return cov;
  }

  // Group 4: Map
  async function group4() {
    const ctx = await browser.newContext();
    const { nav, click, clickAll, cycleSelect, init, page } = helpers(await ctx.newPage());
    await init();
    console.log('  [cov] G4: Map');

    await nav('#/map');
    await click('#mapControlsToggle');

    // Role checkboxes toggle
    try {
      const cbs = await page.$$('#mcRoleChecks input[type="checkbox"]');
      for (const cb of cbs) { try { await cb.click(); await cb.click(); } catch {} }
    } catch {}

    await click('#mcClusters'); await click('#mcClusters');
    await click('#mcHeatmap'); await click('#mcHeatmap');
    await click('#mcNeighbors'); await click('#mcNeighbors');
    await click('#mcHashLabels'); await click('#mcHashLabels');
    await cycleSelect('#mcLastHeard');

    for (const st of ['active', 'stale', 'all']) {
      try { await page.click(`#mcStatusFilter .btn[data-status="${st}"]`, { timeout: CLICK_TIMEOUT }); } catch {}
    }
    await clickAll('#mcJumps button', 5);
    await clickAll('.leaflet-marker-icon', 3);
    await clickAll('.leaflet-interactive', 2);
    await clickAll('.leaflet-popup-content a', 2);
    await click('.leaflet-control-zoom-in');
    await click('.leaflet-control-zoom-out');

    // Dark mode toggle triggers tile swap
    await click('#darkModeToggle');
    await click('#darkModeToggle');

    const cov = await page.evaluate(() => window.__coverage__);
    await ctx.close();
    return cov;
  }

  // Group 5: Analytics + Channels + Observers
  async function group5() {
    const ctx = await browser.newContext();
    const { nav, click, clickAll, cycleSelect, init, page } = helpers(await ctx.newPage());
    await init();
    console.log('  [cov] G5: Analytics + Channels + Observers');

    await nav('#/analytics');
    const tabs = ['overview', 'rf', 'topology', 'channels', 'hashsizes', 'collisions', 'subpaths', 'nodes', 'distance'];
    for (const t of tabs) {
      try { await page.click(`#analyticsTabs [data-tab="${t}"]`, { timeout: 500 }); } catch {}
    }
    // Topology observer selector
    try {
      await page.click('#analyticsTabs [data-tab="topology"]', { timeout: 500 });
      await clickAll('#obsSelector .tab-btn', 5);
      await click('[data-obs="__all"]');
    } catch {}
    // Collisions navigate rows
    try {
      await page.click('#analyticsTabs [data-tab="collisions"]', { timeout: 500 });
      await clickAll('tr[data-action="navigate"]', 3);
    } catch {}
    // Nodes tab sort
    try {
      await page.click('#analyticsTabs [data-tab="nodes"]', { timeout: 500 });
      await clickAll('.analytics-table th', 8);
    } catch {}
    // Deep-link tabs
    for (const t of tabs) {
      await page.evaluate((tab) => { location.hash = '#/analytics?tab=' + tab; }, t);
      await page.waitForTimeout(NAV_WAIT);
    }

    // Channels
    await nav('#/channels');
    await clickAll('.channel-item, .channel-row, .channel-card', 3);
    await clickAll('table tbody tr', 3);
    try {
      const ch = await page.$eval('table tbody tr td:first-child', el => el.textContent.trim());
      if (ch) await nav('#/channels/' + ch);
    } catch {}

    // Observers
    await nav('#/observers');
    await clickAll('table tbody tr, .observer-card, .observer-row', 3);
    try {
      const link = await page.$('a[href*="/observers/"]');
      if (link) {
        await link.click();
        await cycleSelect('#obsDaysSelect');
      }
    } catch {}
    // Navigate directly to observer detail pages
    await nav('#/observers/test-obs-1');
    await page.waitForTimeout(200);
    await cycleSelect('#obsDaysSelect');
    await nav('#/observers/test-status-obs');
    await page.waitForTimeout(200);

    // Compare page with two real observers
    await nav('#/compare?obsA=test-obs-1&obsB=test-obs-2');
    await page.waitForTimeout(300);
    // Also try selecting observers via the UI
    await nav('#/compare');
    await page.waitForTimeout(200);
    try {
      await page.selectOption('#compareObsA', 'test-obs-1');
      await page.selectOption('#compareObsB', 'test-obs-2');
    } catch {}
    try {
      // Click compare button
      await click('#compareBtn');
      await page.waitForTimeout(300);
    } catch {}

    // Traces page — search for a real packet hash
    await nav('#/traces');
    await page.waitForTimeout(200);
    try {
      await page.fill('#traceHashInput', 'b6b839cb61eead4a');
      await click('#traceSearchBtn');
      await page.waitForTimeout(300);
    } catch {}
    // Try another hash
    try {
      await page.fill('#traceHashInput', 'ed3bcc36ddfd824c');
      await click('#traceSearchBtn');
      await page.waitForTimeout(200);
    } catch {}
    await clickAll('table tbody tr', 3);

    // Exercise channels.js functions
    await page.evaluate(() => {
      // hashCode, getChannelColor, getSenderColor, highlightMentions
      if (typeof hashCode === 'function') {
        try { hashCode('test'); hashCode(''); hashCode('abc123'); } catch {}
      }
      if (typeof getChannelColor === 'function') {
        try { getChannelColor('00'); getChannelColor('ff'); getChannelColor(null); } catch {}
      }
      if (typeof getSenderColor === 'function') {
        try { getSenderColor('Alice'); getSenderColor('Bob'); getSenderColor(''); } catch {}
      }
      if (typeof highlightMentions === 'function') {
        try { highlightMentions('Hello @[Alice]'); highlightMentions('No mentions'); } catch {}
      }
      if (typeof formatSecondsAgo === 'function') {
        try { formatSecondsAgo(30); formatSecondsAgo(3600); formatSecondsAgo(86400); formatSecondsAgo(0); } catch {}
      }
    }).catch(() => {});

    const cov = await page.evaluate(() => window.__coverage__);
    await ctx.close();
    return cov;
  }

  // Group 6: Live + Perf + Traces + App globals
  async function group6() {
    const ctx = await browser.newContext();
    const { nav, click, clickAll, init, page } = helpers(await ctx.newPage());
    await init();
    console.log('  [cov] G6: Live + Perf + Traces + globals');

    // Live
    await nav('#/live');
    await click('#vcrPauseBtn'); await click('#vcrPauseBtn');
    await click('#vcrSpeedBtn'); await click('#vcrSpeedBtn'); await click('#vcrSpeedBtn');
    await click('#vcrMissed');
    await click('#vcrPromptReplay'); await click('#vcrPromptSkip');
    await click('#liveHeatToggle'); await click('#liveHeatToggle');
    await click('#liveGhostToggle'); await click('#liveGhostToggle');
    await click('#liveRealisticToggle'); await click('#liveRealisticToggle');
    await click('#liveFavoritesToggle'); await click('#liveFavoritesToggle');
    await click('#liveMatrixToggle'); await click('#liveMatrixToggle');
    await click('#liveMatrixRainToggle'); await click('#liveMatrixRainToggle');
    await click('#liveAudioToggle');
    await page.evaluate(() => {
      const s = document.getElementById('audioBpmSlider');
      if (s) { s.value = '140'; s.dispatchEvent(new Event('input', { bubbles: true })); }
    }).catch(() => {});
    await click('#liveAudioToggle');
    // VCR timeline click
    await page.evaluate(() => {
      const c = document.getElementById('vcrTimeline');
      if (c) { const r = c.getBoundingClientRect(); c.dispatchEvent(new MouseEvent('click', { clientX: r.left + r.width * 0.5, clientY: r.top + r.height * 0.5, bubbles: true })); }
    }).catch(() => {});
    await page.evaluate(() => window.dispatchEvent(new Event('resize'))).catch(() => {});

    // Exercise VCR and live.js functions via page.evaluate
    await page.evaluate(() => {
      // VCR functions
      if (typeof vcrPause === 'function') { try { vcrPause(); } catch {} }
      if (typeof vcrSpeedCycle === 'function') { try { vcrSpeedCycle(); } catch {} }
      if (typeof vcrReplayFromTs === 'function') {
        try { vcrReplayFromTs(Date.now() - 60000); } catch {}
      }
      if (typeof drawLcdText === 'function') {
        try { drawLcdText('12:34:56', '#00ff00'); } catch {}
        try { drawLcdText('PAUSED', '#ff0000'); } catch {}
        try { drawLcdText('00:00:00', '#ffffff'); } catch {}
      }
      if (typeof vcrResumeLive === 'function') { try { vcrResumeLive(); } catch {} }
      if (typeof vcrUnpause === 'function') { try { vcrUnpause(); } catch {} }
      if (typeof vcrRewind === 'function') { try { vcrRewind(5000); } catch {} }
      if (typeof updateVCRClock === 'function') { try { updateVCRClock(Date.now()); } catch {} }
      if (typeof updateVCRLcd === 'function') { try { updateVCRLcd(); } catch {} }
      if (typeof updateVCRUI === 'function') { try { updateVCRUI(); } catch {} }

      // bufferPacket — simulate WebSocket packet arrival
      if (typeof bufferPacket === 'function') {
        try {
          bufferPacket({
            hash: 'test-ws-hash-001',
            observer_id: 'test-obs-1',
            observer_name: 'TestObs',
            snr: 5,
            rssi: -80,
            timestamp: new Date().toISOString(),
            decoded: {
              header: { routeType: 1, payloadType: 5, payloadTypeName: 'GRP_TXT' },
              payload: { channelHash: 0, text: 'test message' },
              path: { hops: ['AABB'] }
            },
            raw_hex: '154000E9221C303193541599CC382A78',
            path_json: '["AABB"]',
            route_type: 1,
            payload_type: 5
          });
        } catch {}
        try {
          bufferPacket({
            hash: 'test-ws-hash-002',
            observer_id: 'test-obs-2',
            snr: -3,
            rssi: -110,
            timestamp: new Date().toISOString(),
            decoded: {
              header: { routeType: 0, payloadType: 0, payloadTypeName: 'ADVERT' },
              payload: { pubKey: 'aabb111111111111111111111111111111111111111111111111111111111111', name: 'TestRepeater2', role: 'repeater', lat: 34.05, lon: -118.24 },
              path: { hops: [] }
            },
            raw_hex: 'FF00AABB',
            path_json: '[]',
            route_type: 0,
            payload_type: 0
          });
        } catch {}
      }

      // dbPacketToLive
      if (typeof dbPacketToLive === 'function') {
        try {
          dbPacketToLive({
            hash: 'test-hash', observer_id: 'obs1', snr: 5, rssi: -80,
            timestamp: new Date().toISOString(), decoded_json: '{"type":"GRP_TXT"}',
            raw_hex: 'AABB', path_json: '["CC"]', route_type: 1, payload_type: 5
          });
        } catch {}
      }

      // renderPacketTree with synthetic packets
      if (typeof renderPacketTree === 'function') {
        try {
          renderPacketTree([{
            hash: 'synth-001', observer_id: 'test-obs-1', observer_name: 'TestObs',
            snr: 5, rssi: -80, timestamp: new Date().toISOString(),
            decoded: { header: { payloadTypeName: 'GRP_TXT' }, payload: { text: 'synth' }, path: { hops: [] } },
            raw_hex: 'AABBCCDD', path_json: '[]'
          }], false);
        } catch {}
      }
    }).catch(() => {});

    // Traces
    await nav('#/traces');
    await clickAll('table tbody tr', 3);

    // Perf
    await nav('#/perf');
    await click('#perfRefresh');
    await click('#perfReset');

    // App.js globals
    await nav('#/nonexistent-route');
    for (const r of ['home', 'nodes', 'packets', 'map', 'live', 'channels', 'traces', 'observers', 'analytics', 'perf', 'compare']) {
      await page.evaluate((rt) => { location.hash = '#/' + rt; }, r);
      await page.waitForTimeout(NAV_WAIT);
    }
    // Rapid route transitions (exercises destroy/init cycles)
    for (const r of ['nodes', 'packets', 'live', 'map', 'analytics', 'channels', 'observers', 'home']) {
      await page.evaluate((rt) => { location.hash = '#/' + rt; }, r);
      await page.waitForTimeout(20);
    }
    await page.evaluate(() => window.dispatchEvent(new HashChangeEvent('hashchange'))).catch(() => {});
    for (let i = 0; i < 4; i++) await click('#darkModeToggle');
    await page.evaluate(() => window.dispatchEvent(new Event('theme-changed'))).catch(() => {});

    // Hamburger + nav
    await click('#hamburger');
    await clickAll('.nav-links .nav-link', 5);

    // Favorites
    await click('#favToggle');
    await clickAll('.fav-dd-item', 3);
    await page.evaluate(() => document.body.click()).catch(() => {});
    await click('#favToggle');

    // Global search
    await click('#searchToggle');
    try { await page.fill('#searchInput', 'test'); } catch {}
    await clickAll('.search-result-item', 3);
    try { await page.keyboard.press('Escape'); } catch {}
    try {
      await page.keyboard.press('Control+k');
      await page.fill('#searchInput', 'node');
      await page.keyboard.press('Escape');
    } catch {}

    // apiPerf
    await page.evaluate(() => { if (window.apiPerf) window.apiPerf(); }).catch(() => {});

    const cov = await page.evaluate(() => window.__coverage__);
    await ctx.close();
    return cov;
  }

  // Group 7: Utility functions via page.evaluate() — no UI needed
  async function group7() {
    const ctx = await browser.newContext();
    const { init, page } = helpers(await ctx.newPage());
    await init();
    console.log('  [cov] G7: Utility functions');

    await page.evaluate(() => {
      // timeAgo
      if (typeof timeAgo === 'function') {
        timeAgo(null);
        timeAgo(new Date().toISOString());
        timeAgo(new Date(Date.now() - 30000).toISOString());
        timeAgo(new Date(Date.now() - 3600000).toISOString());
        timeAgo(new Date(Date.now() - 86400000 * 2).toISOString());
        timeAgo(new Date(Date.now() - 86400000 * 60).toISOString());
        timeAgo(new Date(Date.now() - 5000).toISOString());
        timeAgo(new Date(Date.now() - 120000).toISOString());
        timeAgo('invalid-date');
        timeAgo('');
      }
      if (typeof truncate === 'function') {
        truncate('hello world', 5); truncate(null, 5); truncate('hi', 10);
        truncate('', 5); truncate('exactly10!', 10);
      }
      if (typeof routeTypeName === 'function') {
        for (let i = 0; i <= 6; i++) routeTypeName(i);
        routeTypeName(99); routeTypeName(-1); routeTypeName(null);
      }
      if (typeof payloadTypeName === 'function') {
        for (let i = 0; i <= 15; i++) payloadTypeName(i);
        payloadTypeName(99); payloadTypeName(null);
      }
      if (typeof payloadTypeColor === 'function') {
        for (let i = 0; i <= 15; i++) payloadTypeColor(i);
        payloadTypeColor(99); payloadTypeColor(null);
      }
      if (typeof invalidateApiCache === 'function') {
        invalidateApiCache(); invalidateApiCache('/test'); invalidateApiCache('/api/nodes');
      }
      if (typeof escapeHtml === 'function') {
        escapeHtml('<script>alert("xss")</script>');
        escapeHtml('normal text');
        escapeHtml(''); escapeHtml(null);
        escapeHtml('a&b<c>d"e');
      }
      if (typeof debouncedOnWS === 'function') {
        try {
          const handler = debouncedOnWS(function(msgs) {}, 100);
          if (handler) handler([{type:'packet',data:{}}]);
        } catch {}
      }

      // roles.js functions
      if (typeof getHealthThresholds === 'function') {
        getHealthThresholds('repeater');
        getHealthThresholds('room');
        getHealthThresholds('companion');
        getHealthThresholds('sensor');
        getHealthThresholds('unknown');
        getHealthThresholds('');
        getHealthThresholds(null);
      }
      if (typeof getNodeStatus === 'function') {
        getNodeStatus('repeater', Date.now());
        getNodeStatus('repeater', Date.now() - 999999999);
        getNodeStatus('companion', Date.now());
        getNodeStatus('companion', Date.now() - 999999999);
        getNodeStatus('room', Date.now());
        getNodeStatus('sensor', Date.now() - 999999999);
        getNodeStatus('unknown', null);
        getNodeStatus('repeater', 0);
      }
      if (typeof getTileUrl === 'function') {
        getTileUrl();
      }
      if (typeof syncBadgeColors === 'function') {
        try { syncBadgeColors(); } catch {}
      }
      if (typeof miniMarkdown === 'function') {
        miniMarkdown('**bold** and *italic* and `code`');
        miniMarkdown('[link](https://example.com)');
        miniMarkdown('- item1\n- item2\n- item3');
        miniMarkdown('');
        miniMarkdown(null);
        miniMarkdown('plain text no formatting');
      }
      if (typeof copyToClipboard === 'function') {
        try { copyToClipboard('test text', function(){}, function(){}); } catch {}
      }

      // PacketFilter — exercise with actual packet data for evaluate paths
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
          'has_text', 'is_encrypted', 'type contains ADV',
          'size > 10', 'size < 100', 'observations > 1',
          'payload_bytes > 5', 'payload.channelHash == 0',
          'payload.text contains hello',
          'observer_id == "test-obs-1"', 'path contains AABB',
          'hash == "b6b839cb61eead4a"',
        ];
        // Compile all
        for (const e of exprs) { try { PF.compile(e); } catch {} }
        // Bad expressions
        for (const e of ['@@@', '== ==', '(((', 'type ==', '', '!!!', 'and and', 'type == &&']) {
          try { PF.compile(e); } catch {}
        }
        // Actually run filters against synthetic packets
        const testPackets = [
          { payload_type: 0, route_type: 0, snr: 5, rssi: -80, hash: 'aabb', raw_hex: 'FF00AABB11223344', path_json: '["A1B2"]', observation_count: 3, observer_id: 'test-obs-1', observer_name: 'TestObs', decoded_json: '{"type":"ADVERT","channelHash":0,"text":"hello"}' },
          { payload_type: 5, route_type: 1, snr: -10, rssi: -120, hash: 'ccdd', raw_hex: 'AABBCCDD', path_json: '[]', observation_count: 1, observer_id: 'test-obs-2', observer_name: '', decoded_json: '{"type":"GRP_TXT","channelHash":0}' },
          { payload_type: 6, route_type: 2, snr: 0, rssi: -90, hash: 'eeff', raw_hex: '', path_json: '["X1","X2","X3"]', observation_count: 2, observer_id: '', observer_name: null, decoded_json: null },
          { payload_type: 3, route_type: 3, snr: 15, rssi: -50, hash: '1122', raw_hex: 'AB', path_json: null, observation_count: 0 },
        ];
        for (const e of exprs) {
          try {
            const compiled = PF.compile(e);
            if (compiled && compiled.filter) {
              for (const pkt of testPackets) { try { compiled.filter(pkt); } catch {} }
            }
          } catch {}
        }
      }
    });

    const cov = await page.evaluate(() => window.__coverage__);
    await ctx.close();
    return cov;
  }

  // ── Run all groups in parallel ─────────────────────────────────────
  console.log('Starting parallel coverage collection (7 groups)...');
  const start = Date.now();

  const results = await Promise.allSettled([
    group1(), group2(), group3(), group4(), group5(), group6(), group7()
  ]);

  const elapsed = ((Date.now() - start) / 1000).toFixed(1);
  console.log(`All groups completed in ${elapsed}s`);

  // ── Merge coverage ─────────────────────────────────────────────────
  const outDir = path.join(__dirname, '..', '.nyc_output');
  if (!fs.existsSync(outDir)) fs.mkdirSync(outDir, { recursive: true });

  let fileIndex = 0;
  let totalFiles = 0;
  for (let i = 0; i < results.length; i++) {
    const r = results[i];
    if (r.status === 'fulfilled' && r.value) {
      const fname = `frontend-coverage-g${i + 1}.json`;
      fs.writeFileSync(path.join(outDir, fname), JSON.stringify(r.value));
      const count = Object.keys(r.value).length;
      totalFiles = Math.max(totalFiles, count);
      fileIndex++;
      console.log(`  Group ${i + 1}: ${count} instrumented files`);
    } else if (r.status === 'rejected') {
      console.log(`  Group ${i + 1}: FAILED — ${r.reason?.message || r.reason}`);
    } else {
      console.log(`  Group ${i + 1}: no coverage (not instrumented?)`);
    }
  }

  if (fileIndex === 0) {
    console.log('WARNING: No __coverage__ found in any group — instrumentation may have failed');
  } else {
    console.log(`Frontend coverage collected: ${fileIndex} groups, ${totalFiles} instrumented files`);
  }

  await browser.close();
}

run().catch(e => { console.error(e); process.exit(1); });
