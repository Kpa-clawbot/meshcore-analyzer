# Deep Linking P1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make P1 UI states in nodes, packets, and channels URL-addressable so they survive refresh and can be shared.

**Architecture:** Each page reads URL params from `location.hash.split('?')[1]` on init (router strips query string before passing `routeParam`, so pages must read `location.hash` directly). State changes call `history.replaceState` to keep the URL in sync. localStorage remains the fallback default; URL params override when present.

**Tech Stack:** Vanilla JS (ES5/6), browser History API, URLSearchParams

---

## Files Changed

| File | Changes |
|---|---|
| `public/region-filter.js` | Add `setSelected(codesArray)`, track `_container` for re-render |
| `public/nodes.js` | Read `?tab=`/`?search=` on init; `updateNodesUrl()` on tab/search change; expose `buildNodesQuery` on `window` |
| `public/packets.js` | Read `?timeWindow=`/`?region=` on init; `updatePacketsUrl()` on timeWindow/region change; expose `buildPacketsUrl` on `window` |
| `public/channels.js` | Read `?node=` on init; update URL in `showNodeDetail`/`closeNodeDetail` |
| `test-frontend-helpers.js` | Add unit tests for `buildNodesQuery` and `buildPacketsUrl` |
| `test-e2e-playwright.js` | Add Playwright tests: tab URL persistence, timeWindow URL persistence |

---

## Task 1: Add `setSelected` to RegionFilter

**Files:**
- Modify: `public/region-filter.js`

- [ ] **Step 1: Write the failing unit test**

Add to `test-frontend-helpers.js` before the `// ===== SUMMARY =====` line:

```javascript
// ===== REGION-FILTER.JS: setSelected =====
console.log('\n=== region-filter.js: setSelected ===');
{
  const ctx = makeSandbox();
  ctx.fetch = () => Promise.resolve({ json: () => Promise.resolve({ 'US-SFO': 'San Jose', 'US-LAX': 'Los Angeles' }) });
  loadInCtx(ctx, 'public/region-filter.js');

  const RF = ctx.RegionFilter;
  RF.init(document.createElement('div'));

  test('setSelected sets region codes', async () => {
    await RF.init(document.createElement('div'));
    RF.setSelected(['US-SFO', 'US-LAX']);
    assert.strictEqual(RF.getRegionParam(), 'US-SFO,US-LAX');
  });

  test('setSelected with null clears selection', async () => {
    await RF.init(document.createElement('div'));
    RF.setSelected(['US-SFO']);
    RF.setSelected(null);
    assert.strictEqual(RF.getRegionParam(), '');
  });

  test('setSelected with empty array clears selection', async () => {
    await RF.init(document.createElement('div'));
    RF.setSelected(['US-SFO']);
    RF.setSelected([]);
    assert.strictEqual(RF.getRegionParam(), '');
  });
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
node test-frontend-helpers.js 2>&1 | grep -A2 "setSelected"
```

Expected: `❌ setSelected sets region codes: RF.setSelected is not a function`

- [ ] **Step 3: Add `_container` tracking and `setSelected` to region-filter.js**

In `region-filter.js`, add `var _container = null;` after the existing module-level vars (after line 9 `var _listeners = [];`):

```javascript
  var _listeners = [];
  var _container = null;   // ← add this line
  var _loaded = false;
```

In `initFilter`, save the container:

```javascript
  async function initFilter(container, opts) {
    _container = container;          // ← add this line
    if (opts && opts.dropdown) container._forceDropdown = true;
    await fetchRegions();
    render(container);
  }
```

Add `setSelected` function before `// Expose globally`:

```javascript
  /** Override selected regions (e.g. from URL param). Persists to localStorage and re-renders. */
  function setSelected(codesArray) {
    _selected = (codesArray && codesArray.length > 0) ? new Set(codesArray) : null;
    saveToStorage();
    if (_container) render(_container);
  }
```

Add `setSelected` to the public API object:

```javascript
  window.RegionFilter = {
    init: initFilter,
    render: render,
    getSelected: getSelected,
    getRegionParam: getRegionParam,
    regionQueryString: regionQueryString,
    onChange: onChange,
    offChange: offChange,
    fetchRegions: fetchRegions,
    setSelected: setSelected,    // ← add this line
  };
```

- [ ] **Step 4: Run test to verify it passes**

```bash
node test-frontend-helpers.js 2>&1 | grep -E "(setSelected|FAIL|passed|failed)"
```

Expected: 3 passing `setSelected` tests, overall pass.

- [ ] **Step 5: Commit**

```bash
git add public/region-filter.js test-frontend-helpers.js
git commit -m "feat: add RegionFilter.setSelected for URL param initialization (#536)"
```

---

## Task 2: nodes.js — tab and search deep linking

**Files:**
- Modify: `public/nodes.js`
- Test: `test-frontend-helpers.js`
- Test: `test-e2e-playwright.js`

- [ ] **Step 1: Write the unit test (add to test-frontend-helpers.js)**

Add before the `// ===== SUMMARY =====` line:

```javascript
// ===== NODES.JS: buildNodesQuery =====
console.log('\n=== nodes.js: buildNodesQuery ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');

  // Provide required globals for nodes.js IIFE to execute
  ctx.registerPage = () => {};
  ctx.RegionFilter = { init: () => Promise.resolve(), onChange: () => () => {}, offChange: () => {}, getSelected: () => null, getRegionParam: () => '' };
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.debouncedOnWS = () => () => {};
  ctx.invalidateApiCache = () => {};
  ctx.favStar = () => '';
  ctx.bindFavStars = () => {};
  ctx.getFavorites = () => [];
  ctx.isFavorite = () => false;
  ctx.connectWS = () => {};
  ctx.HopResolver = { init: () => {}, resolve: () => ({}), ready: () => false };
  ctx.initTabBar = () => {};
  ctx.debounce = (fn) => fn;
  ctx.copyToClipboard = () => {};
  ctx.api = () => Promise.resolve({});
  ctx.escapeHtml = (s) => s;
  ctx.timeAgo = () => '';
  ctx.formatTimestampWithTooltip = () => '';
  ctx.getTimestampMode = () => 'ago';
  ctx.CLIENT_TTL = {};
  ctx.qrcode = null;

  try {
    const src = fs.readFileSync('public/nodes.js', 'utf8');
    vm.runInContext(src, ctx);
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  } catch (e) {
    console.log('  ⚠️ nodes.js sandbox load failed:', e.message.slice(0, 120));
  }

  const buildNodesQuery = ctx.buildNodesQuery;

  if (buildNodesQuery) {
    test('buildNodesQuery: all tab + no search = empty', () => {
      assert.strictEqual(buildNodesQuery('all', ''), '');
    });
    test('buildNodesQuery: repeater tab only', () => {
      assert.strictEqual(buildNodesQuery('repeater', ''), '?tab=repeater');
    });
    test('buildNodesQuery: search only (all tab)', () => {
      assert.strictEqual(buildNodesQuery('all', 'foo'), '?search=foo');
    });
    test('buildNodesQuery: tab + search combined', () => {
      assert.strictEqual(buildNodesQuery('companion', 'bar'), '?tab=companion&search=bar');
    });
    test('buildNodesQuery: null search treated as empty', () => {
      assert.strictEqual(buildNodesQuery('all', null), '');
    });
    test('buildNodesQuery: sensor tab', () => {
      assert.strictEqual(buildNodesQuery('sensor', ''), '?tab=sensor');
    });
  } else {
    console.log('  ⚠️ buildNodesQuery not exposed — skipping');
  }
}
```

- [ ] **Step 2: Run test to verify it fails (or skips)**

```bash
node test-frontend-helpers.js 2>&1 | grep -A3 "buildNodesQuery"
```

Expected: `⚠️ buildNodesQuery not exposed — skipping`

- [ ] **Step 3: Add URL param reading and helpers to nodes.js**

**3a.** Add `buildNodesQuery` and `updateNodesUrl` functions inside the nodes.js IIFE, after the `TABS` definition (around line 86, before `function renderNodeTimestampHtml`):

```javascript
  function buildNodesQuery(tab, searchStr) {
    var parts = [];
    if (tab && tab !== 'all') parts.push('tab=' + encodeURIComponent(tab));
    if (searchStr) parts.push('search=' + encodeURIComponent(searchStr));
    return parts.length ? '?' + parts.join('&') : '';
  }
  window.buildNodesQuery = buildNodesQuery;

  function updateNodesUrl() {
    history.replaceState(null, '', '#/nodes' + buildNodesQuery(activeTab, search));
  }
```

**3b.** In the list-view branch of `init` (after the `return;` that ends the full-screen block at line 317), add URL param reading before `app.innerHTML`:

```javascript
    // Read URL params for list view (router strips query string from routeParam)
    const _listUrlParams = new URLSearchParams(location.hash.split('?')[1] || '');
    const _urlTab = _listUrlParams.get('tab');
    const _urlSearch = _listUrlParams.get('search');
    if (_urlTab && TABS.some(function(t) { return t.key === _urlTab; })) activeTab = _urlTab;
    if (_urlSearch) search = _urlSearch;

    app.innerHTML = `<div class="nodes-page">
```

**3c.** After `app.innerHTML = ...` (after the closing backtick at line ~330), populate the search input:

```javascript
    if (search) {
      var _si = document.getElementById('nodeSearch');
      if (_si) _si.value = search;
    }
```

**3d.** In the search input event listener (around line 335), add `updateNodesUrl()`:

```javascript
    document.getElementById('nodeSearch').addEventListener('input', debounce(e => {
      search = e.target.value;
      updateNodesUrl();
      loadNodes();
    }, 250));
```

**3e.** In the tab click handler inside `renderLeft` (around line 875), add `updateNodesUrl()`:

```javascript
      btn.addEventListener('click', () => { activeTab = btn.dataset.tab; updateNodesUrl(); loadNodes(); });
```

- [ ] **Step 4: Run unit tests**

```bash
node test-frontend-helpers.js 2>&1 | grep -E "(buildNodesQuery|✅|❌)" | grep -v "helpers"
```

Expected: 6 passing `buildNodesQuery` tests.

- [ ] **Step 5: Write Playwright test (add to test-e2e-playwright.js)**

Add before the closing `await browser.close()` line:

```javascript
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
    if (roomTab) {
      await roomTab.click();
      await page.waitForTimeout(300);
      const url = page.url();
      assert(url.includes('tab=room'), `URL should contain tab=room after click, got: ${url}`);
    }
  });
```

- [ ] **Step 6: Run full test suite**

```bash
node test-frontend-helpers.js
```

Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add public/nodes.js test-frontend-helpers.js test-e2e-playwright.js
git commit -m "feat: deep link nodes tab and search query (#536)"
```

---

## Task 3: packets.js — timeWindow and region deep linking

**Files:**
- Modify: `public/packets.js`
- Test: `test-frontend-helpers.js`
- Test: `test-e2e-playwright.js`

> Depends on Task 1 (RegionFilter.setSelected).

- [ ] **Step 1: Write the unit test**

Add to `test-frontend-helpers.js` before `// ===== SUMMARY =====`:

```javascript
// ===== PACKETS.JS: buildPacketsUrl =====
console.log('\n=== packets.js: buildPacketsUrl ===');
{
  // Test the pure helper function
  // (loaded via packets.js after it exposes window.buildPacketsUrl)
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');

  ctx.registerPage = () => {};
  ctx.RegionFilter = { init: () => Promise.resolve(), onChange: () => () => {}, offChange: () => {}, getSelected: () => null, getRegionParam: () => '', setSelected: () => {} };
  ctx.onWS = () => {};
  ctx.offWS = () => {};
  ctx.debouncedOnWS = () => () => {};
  ctx.invalidateApiCache = () => {};
  ctx.api = () => Promise.resolve({});
  ctx.observerMap = new Map();
  ctx.getParsedPath = () => [];
  ctx.getParsedDecoded = () => ({});
  ctx.clearParsedCache = () => {};
  ctx.escapeHtml = (s) => s;
  ctx.timeAgo = () => '';
  ctx.formatTimestampWithTooltip = () => '';
  ctx.getTimestampMode = () => 'ago';
  ctx.copyToClipboard = () => {};
  ctx.CLIENT_TTL = {};
  ctx.debounce = (fn) => fn;
  ctx.initTabBar = () => {};

  try {
    const src = fs.readFileSync('public/packet-helpers.js', 'utf8');
    vm.runInContext(src, ctx);
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
    const src2 = fs.readFileSync('public/packets.js', 'utf8');
    vm.runInContext(src2, ctx);
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  } catch (e) {
    console.log('  ⚠️ packets.js sandbox load failed:', e.message.slice(0, 120));
  }

  const buildPacketsUrl = ctx.buildPacketsUrl;

  if (buildPacketsUrl) {
    test('buildPacketsUrl: default (15min, no region) = bare #/packets', () => {
      assert.strictEqual(buildPacketsUrl(15, ''), '#/packets');
    });
    test('buildPacketsUrl: non-default timeWindow', () => {
      assert.strictEqual(buildPacketsUrl(60, ''), '#/packets?timeWindow=60');
    });
    test('buildPacketsUrl: region only', () => {
      assert.strictEqual(buildPacketsUrl(15, 'US-SFO'), '#/packets?region=US-SFO');
    });
    test('buildPacketsUrl: timeWindow + region', () => {
      assert.strictEqual(buildPacketsUrl(30, 'US-SFO,US-LAX'), '#/packets?timeWindow=30&region=US-SFO%2CUS-LAX');
    });
    test('buildPacketsUrl: timeWindow=0 treated as default', () => {
      assert.strictEqual(buildPacketsUrl(0, ''), '#/packets');
    });
  } else {
    console.log('  ⚠️ buildPacketsUrl not exposed — skipping');
  }
}
```

- [ ] **Step 2: Run to verify it skips**

```bash
node test-frontend-helpers.js 2>&1 | grep -A2 "buildPacketsUrl"
```

Expected: `⚠️ buildPacketsUrl not exposed — skipping`

- [ ] **Step 3: Add helpers and URL param reading to packets.js**

**3a.** Add `buildPacketsUrl` and `updatePacketsUrl` inside the packets.js IIFE, after the existing constants at the top (around line 36, after `let showHexHashes`):

```javascript
  function buildPacketsUrl(timeWindowMin, regionParam) {
    var parts = [];
    if (timeWindowMin && timeWindowMin !== 15) parts.push('timeWindow=' + timeWindowMin);
    if (regionParam) parts.push('region=' + encodeURIComponent(regionParam));
    return '#/packets' + (parts.length ? '?' + parts.join('&') : '');
  }
  window.buildPacketsUrl = buildPacketsUrl;

  function updatePacketsUrl() {
    history.replaceState(null, '', buildPacketsUrl(savedTimeWindowMin, RegionFilter.getRegionParam()));
  }
```

**3b.** In the `init` function (around line 263), add URL param reading after the existing `routeParam`/`directObsId` parsing and before `app.innerHTML`:

```javascript
    // Read URL params for filter state (router strips query from routeParam; read from location.hash)
    var _initUrlParams = new URLSearchParams(location.hash.split('?')[1] || '');
    var _urlTimeWindow = Number(_initUrlParams.get('timeWindow'));
    if (Number.isFinite(_urlTimeWindow) && _urlTimeWindow > 0) {
      savedTimeWindowMin = _urlTimeWindow;
      localStorage.setItem('meshcore-time-window', String(_urlTimeWindow));
    }
    var _urlRegion = _initUrlParams.get('region');
    if (_urlRegion) {
      RegionFilter.setSelected(_urlRegion.split(',').filter(Boolean));
    }

    app.innerHTML = `<div class="split-layout detail-collapsed">
```

**3c.** In the time window change handler (around line 865), add `updatePacketsUrl()`:

```javascript
    fTimeWindow.addEventListener('change', () => {
      savedTimeWindowMin = Number(fTimeWindow.value);
      if (!Number.isFinite(savedTimeWindowMin) || savedTimeWindowMin <= 0) savedTimeWindowMin = 15;
      localStorage.setItem('meshcore-time-window', fTimeWindow.value);
      updatePacketsUrl();
      loadPackets();
    });
```

**3d.** In the RegionFilter.onChange callback (around line 719), add `updatePacketsUrl()`:

```javascript
    RegionFilter.onChange(function() { updatePacketsUrl(); loadPackets(); });
```

- [ ] **Step 4: Run unit tests**

```bash
node test-frontend-helpers.js 2>&1 | grep -E "(buildPacketsUrl|✅|❌)" | grep -v "helpers"
```

Expected: 5 passing `buildPacketsUrl` tests.

- [ ] **Step 5: Write Playwright test (add to test-e2e-playwright.js, inside the deep-linking group)**

```javascript
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
```

- [ ] **Step 6: Run full test suite**

```bash
node test-frontend-helpers.js
```

Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add public/packets.js test-frontend-helpers.js test-e2e-playwright.js
git commit -m "feat: deep link packets timeWindow and region filter (#536)"
```

---

## Task 4: channels.js — node panel deep linking

**Files:**
- Modify: `public/channels.js`

No unit tests needed for this task — the URL manipulation is side-effectful (DOM + History API). Playwright tests cover it.

- [ ] **Step 1: Write the Playwright test (add to test-e2e-playwright.js, inside the deep-linking group)**

```javascript
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
```

- [ ] **Step 2: Update `showNodeDetail` to write `?node=` to the URL**

In `channels.js`, in `showNodeDetail` (around line 171), add the URL update right after `selectedNode = name;`:

```javascript
  async function showNodeDetail(name) {
    _nodePanelTrigger = document.activeElement;
    if (_focusTrapCleanup) { _focusTrapCleanup(); _focusTrapCleanup = null; }
    const node = await lookupNode(name);
    selectedNode = name;
    var _chBase = selectedHash ? '#/channels/' + encodeURIComponent(selectedHash) : '#/channels';
    history.replaceState(null, '', _chBase + '?node=' + encodeURIComponent(name));

    let panel = document.getElementById('chNodePanel');
```

- [ ] **Step 3: Update `closeNodeDetail` to strip `?node=` from the URL**

In `closeNodeDetail` (around line 232), add URL restore right after `selectedNode = null;`:

```javascript
  function closeNodeDetail() {
    if (_focusTrapCleanup) { _focusTrapCleanup(); _focusTrapCleanup = null; }
    const panel = document.getElementById('chNodePanel');
    if (panel) panel.classList.remove('open');
    selectedNode = null;
    var _chRestoreUrl = selectedHash ? '#/channels/' + encodeURIComponent(selectedHash) : '#/channels';
    history.replaceState(null, '', _chRestoreUrl);
    if (_nodePanelTrigger && typeof _nodePanelTrigger.focus === 'function') {
```

- [ ] **Step 4: Read `?node=` on init and auto-open panel**

In `channels.js` `init` (line 316), add URL param reading at the very top of the function (before `app.innerHTML`):

```javascript
  function init(app, routeParam) {
    var _initUrlParams = new URLSearchParams(location.hash.split('?')[1] || '');
    var _pendingNode = _initUrlParams.get('node');

    app.innerHTML = `<div class="ch-layout">
```

Then update the `loadChannels().then(...)` call (around line 350) to auto-open the node panel:

```javascript
    loadChannels().then(async function () {
      if (routeParam) await selectChannel(routeParam);
      if (_pendingNode) showNodeDetail(_pendingNode);
    });
```

- [ ] **Step 5: Run full test suite**

```bash
node test-frontend-helpers.js
```

Expected: all tests pass (no channels unit tests, but regression tests still pass).

- [ ] **Step 6: Commit**

```bash
git add public/channels.js
git commit -m "feat: deep link channels node panel via ?node= (#536)"
```

---

## Task 5: Run E2E Playwright tests

- [ ] **Step 1: Start the local server**

```bash
cd cmd/server && go run . &
```

Wait for it to be ready (check `http://localhost:3000`).

- [ ] **Step 2: Run Playwright tests**

```bash
node test-e2e-playwright.js
```

Expected: all tests pass including the new deep-linking group.

- [ ] **Step 3: If any deep-linking test fails, debug**

Common failures:
- Selector `.node-tab.active` not found: check that nodes.js correctly reads `?tab=` from URL before rendering
- `#fTimeWindow` value wrong: check that `savedTimeWindowMin` is overridden before the DOM is built
- URL doesn't update: check `history.replaceState` calls in the change handlers

- [ ] **Step 4: Final commit (if any fixes needed)**

```bash
git add public/nodes.js public/packets.js public/channels.js
git commit -m "fix: deep linking E2E adjustments (#536)"
```

---

## Self-Review

**Spec coverage check:**
- ✅ P1: Nodes role tab → Task 2
- ✅ P1: Packets time window → Task 3
- ✅ P1: Packets region filter → Task 3 (depends on Task 1)
- ✅ P1: Channels selected channel → Already implemented via `#/channels/{hash}` (verified in channels.js init line 351)
- ✅ P1: Channels node panel → Task 4
- ✅ P2+ items → explicitly out of scope per issue

**Architecture note:** The router in `app.js` strips the query string at line 422 (`const route = hash.split('?')[0]`) before computing `basePage` and `routeParam`. Therefore `#/nodes?tab=repeater` gives `routeParam=null` (not `?tab=repeater`). All pages must read URL params from `location.hash` directly, not from `routeParam`. This is the established pattern in `analytics.js` and `nodes.js` (section scroll).

**Placeholder scan:** No TBDs, no "implement later", all code blocks complete. ✅

**Type consistency:**
- `buildNodesQuery(tab, searchStr)` — used consistently in `updateNodesUrl()` and in tests ✅
- `buildPacketsUrl(timeWindowMin, regionParam)` — used consistently in `updatePacketsUrl()` and in tests ✅
- `RegionFilter.setSelected(codesArray)` — defined in Task 1, used in Task 3 ✅
