/**
 * E2E (#1056 AC #4): Row-detail slide-over panel at narrow widths.
 *
 * At viewports <=1023, clicking a row in the Packets, Nodes, or Observers
 * tables must open the row's detail in a slide-over panel
 * (`.slide-over-panel`) with a backdrop (`.slide-over-backdrop`), instead of
 * pushing layout to a separate page. The panel must close via the X button,
 * a backdrop click, and the Escape key.
 *
 * Wide viewports (>=1280) MUST NOT trigger the slide-over — the existing
 * right-side detail panel behavior is preserved.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-slideover-1056-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
// #1168 Munger #4: replaces the prior `console.warn('⚠️ DEFERRED ...')`
// soft-warn pattern. Skipped tests show up in CI output as `↷ SKIP`
// (visible) instead of being silently swallowed inside an assertion that
// quietly returned. Body of `fn` is preserved verbatim with HARD asserts;
// the gate is the skip wrapper, not a softened assertion. Restore by
// flipping the call from `step.skip(...)` back to `step(...)` once the
// referenced issue is fixed.
let skipped = 0;
step.skip = function (name, reason, fn) {
  skipped++;
  // Touch fn so linters don't flag it as unused; never invoke.
  void fn;
  console.log('  ↷ SKIP ' + name + ' (' + reason + ')');
};
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// The packets page defaults to a 15-minute time window (DEFAULT_TIME_WINDOW in
// public/packets.js). CI freshens the fixture once at the start of the e2e job,
// and this file runs twice: inside the main suite, and again as the #1616
// flake-gate AFTER every other E2E — roughly 20 minutes later. By then every
// fixture packet is older than 15 minutes, the packets table renders zero data
// rows, and `packets@800: page renders + first row exists` times out after 30s.
// The two tests that need a row to click then fail with it, so one expired
// window reports as three failures. Whether it fires depends on how long the
// preceding suite took, which is why it looked like flake and hit unrelated PRs.
//
// Pin an explicit window so this file tests the slide-over rather than the
// clock. The value must be > 0: packets.js only reads the param under
// `_urlTimeWindow > 0`, so `timeWindow=0` does NOT disable the filter, it
// silently leaves the 15-minute default in place.
const PACKETS_QUERY = '?timeWindow=1440';

const PAGES = [
  { hash: '#/packets' + PACKETS_QUERY, tableSel: '#pktTable', rowSel: '#pktTable tbody tr[data-hash]',           name: 'packets'   },
  { hash: '#/nodes',     tableSel: '#nodesTable',  rowSel: '#nodesTable tbody tr[data-value]',                  name: 'nodes'     },
  { hash: '#/observers', tableSel: '#obsTable',    rowSel: '#obsTable tbody tr[data-action="navigate"]',        name: 'observers' },
];

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1056 AC#4 slide-over E2E against ${BASE} ===`);

  // ---- Narrow viewport: slide-over MUST appear ----
  for (const p of PAGES) {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));

    const tag = `${p.name}@800`;

    await step(`${tag}: page renders + first row exists`, async () => {
      await page.goto(BASE + '/' + p.hash, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector(p.tableSel, { timeout: 8000 });
      // Wait for at least one real data row (per the page-specific rowSel).
      // Using p.rowSel here — not a bare `tbody tr` — avoids the
      // virtual-scroll spacer race on the packets page (#1662).
      await page.waitForFunction((rowSel) => {
        return document.querySelector(rowSel) !== null;
      }, p.rowSel, { timeout: 30000 });
    });

    await step(`${tag}: clicking row opens slide-over with backdrop`, async () => {
      // Click the first body row — prefer one with a data-action attribute
      // (packets) or any row otherwise.
      const diag = await page.evaluate(({sel, rowSel}) => {
        const t = document.querySelector(sel);
        if (!t) return { ok: false, why: 'no table' };
        // Use the page-specific rowSel so we never match the virtual-scroll
        // spacer (#1662). Don't fall through to bare 'tbody tr'.
        const candidates = Array.from(t.querySelectorAll(rowSel));
        const row = candidates.find(r => r.hasAttribute('data-action'))
                || candidates.find(r => r.hasAttribute('data-value'))
                || candidates[0];
        if (!row) return { ok: false, why: 'no row', rowCount: candidates.length };
        // Click a real cell (avoid empty/loading rows)
        const td = row.querySelector('td:not(:empty)') || row;
        // Dispatch a real bubbling click event so delegated tbody handlers fire.
        const ev = new MouseEvent('click', { bubbles: true, cancelable: true, view: window });
        td.dispatchEvent(ev);
        return {
          ok: true,
          rowCount: candidates.length,
          rowAction: row.getAttribute('data-action') || null,
          rowValue: row.getAttribute('data-value') || null,
          hasSlideOver: typeof window.SlideOver !== 'undefined',
          shouldUse: !!(window.SlideOver && window.SlideOver.shouldUse && window.SlideOver.shouldUse()),
          innerW: window.innerWidth,
        };
      }, { sel: p.tableSel, rowSel: p.rowSel });
      if (!diag.ok) throw new Error('click setup failed: ' + JSON.stringify(diag));
      // Wait up to 15s for the slide-over to appear (packets does async fetches).
      try {
        await page.waitForFunction(() => {
          const panel = document.querySelector('.slide-over-panel');
          return panel && !panel.hidden;
        }, null, { timeout: 15000 });
      } catch (_) { /* fall through to assertion below for clearer message */ }
      const info = await page.evaluate(() => {
        function isShown(el) {
          if (!el) return false;
          if (el.hidden) return false;
          const r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0;
        }
        const panel = document.querySelector('.slide-over-panel');
        const back  = document.querySelector('.slide-over-backdrop');
        const closeBtn = panel && panel.querySelector('.slide-over-close');
        return {
          panelPresent: !!panel,
          panelVisible: isShown(panel),
          backdropPresent: !!back,
          backdropVisible: isShown(back),
          hasCloseBtn: !!closeBtn,
        };
      });
      assert(info.panelPresent, 'slide-over panel not in DOM (diag: ' + JSON.stringify(diag) + ')');
      assert(info.panelVisible, 'slide-over panel not visible');
      assert(info.backdropPresent, 'slide-over backdrop not in DOM');
      assert(info.backdropVisible, 'slide-over backdrop not visible');
      assert(info.hasCloseBtn, 'slide-over panel missing .slide-over-close X button');
    });

    await step(`${tag}: panel anchored to right edge + a11y attrs + body scroll lock`, async () => {
      // The slideInRight keyframe applies a transient translateX(20px) → 0
      // over ~200ms. Wait comfortably past it before measuring layout.
      await page.waitForTimeout(600);
      const a = await page.evaluate(() => {
        const panel = document.querySelector('.slide-over-panel');
        const back  = document.querySelector('.slide-over-backdrop');
        const x = panel && panel.querySelector('.slide-over-close');
        const cs = panel && getComputedStyle(panel);
        const xr = x && x.getBoundingClientRect();
        const pr = panel && panel.getBoundingClientRect();
        return {
          // Layout-level anchor check: panel's right edge MUST coincide
          // with the viewport's right edge in the rendered layout.
          // (#1168 non-blocker: previous `cssRight === '0px'` re-asserted
          // a value declared in style.css and proved nothing about
          // rendering — strengthened to a real layout assertion.)
          panelRight: pr ? pr.right : null,
          viewportWidth: window.innerWidth,
          cssPosition: cs && cs.position,
          role: panel && panel.getAttribute('role'),
          ariaModal: panel && panel.getAttribute('aria-modal'),
          // #1168 review must-fix #4: panel must use aria-labelledby pointing
          // at the actual <h3 id="slideOverTitle"> so screen readers announce
          // the meaningful title, not a generic static "Detail" string.
          ariaLabelledBy: panel && panel.getAttribute('aria-labelledby'),
          ariaLabel: panel && panel.getAttribute('aria-label'),
          titleId: panel && panel.querySelector('.slide-over-title')
            ? panel.querySelector('.slide-over-title').id : null,
          backdropAriaHidden: back && back.getAttribute('aria-hidden'),
          xAriaLabel: x && x.getAttribute('aria-label'),
          xWidth: xr ? xr.width : 0,
          xHeight: xr ? xr.height : 0,
          // #1168 Munger #3: scroll-lock is now class-based + ref-counted.
          // Assert via getComputedStyle (effective behavior) plus the class
          // marker, not the inline style attribute (which is no longer set).
          bodyOverflow: getComputedStyle(document.body).overflow,
          bodyHasLockClass: document.body.classList.contains('scroll-locked'),
        };
      });
      assert(a.cssPosition === 'fixed', 'slide-over panel not position:fixed (got ' + a.cssPosition + ')');
      // Layout assertion (replaces the prior `cssRight === '0px'` tautology).
      // The panel's rendered right edge must equal the viewport width — i.e.
      // it is actually painted flush to the right edge in the live layout,
      // not merely declared so in CSS. Allow ±2px subpixel rounding.
      assert(a.panelRight !== null && Math.abs(a.panelRight - a.viewportWidth) <= 2,
        'slide-over panel right edge not flush to viewport (panelRight=' + a.panelRight + ', vw=' + a.viewportWidth + ')');
      assert(a.role === 'dialog', 'slide-over role!=dialog (got ' + a.role + ')');
      assert(a.ariaModal === 'true', 'slide-over aria-modal!=true (got ' + a.ariaModal + ')');
      // #1168 must-fix #4: aria-labelledby (pointing to the title h3) wins
      // over a static aria-label so SRs announce the actual packet/node name.
      assert(a.ariaLabelledBy === 'slideOverTitle',
        'slide-over panel must use aria-labelledby="slideOverTitle" (got ' + a.ariaLabelledBy + ')');
      assert(a.titleId === 'slideOverTitle',
        'slide-over title must keep id="slideOverTitle" (got ' + a.titleId + ')');
      assert(!a.ariaLabel,
        'slide-over panel must NOT carry a static aria-label that shadows the title (got ' + a.ariaLabel + ')');
      assert(a.backdropAriaHidden === 'true', 'backdrop aria-hidden!=true (got ' + a.backdropAriaHidden + ')');
      assert(a.xAriaLabel && a.xAriaLabel.length > 0, 'X button missing aria-label');
      assert(a.xWidth >= 44 && a.xHeight >= 44, 'X tap target <44px (' + a.xWidth + 'x' + a.xHeight + ')');
      assert(a.bodyHasLockClass, 'body missing scroll-locked class while open');
      assert(a.bodyOverflow === 'hidden', 'body scroll not locked while open (overflow=' + a.bodyOverflow + ')');
    });

    await step(`${tag}: Escape closes slide-over`, async () => {
      await page.keyboard.press('Escape');
      await page.waitForTimeout(200);
      const info = await page.evaluate(() => {
        function isShown(el) {
          if (!el) return false;
          if (el.hidden) return false;
          const r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0;
        }
        const panel = document.querySelector('.slide-over-panel');
        const back  = document.querySelector('.slide-over-backdrop');
        return { panelGone: !isShown(panel), backGone: !isShown(back),
                 bodyOverflow: getComputedStyle(document.body).overflow,
                 bodyHasLockClass: document.body.classList.contains('scroll-locked') };
      });
      assert(info.panelGone, 'slide-over panel still visible after Escape');
      assert(info.backGone, 'slide-over backdrop still visible after Escape');
      assert(!info.bodyHasLockClass, 'scroll-locked class not removed after Escape');
      assert(info.bodyOverflow !== 'hidden', 'body scroll lock not released after Escape (overflow=' + info.bodyOverflow + ')');
    });

    await step(`${tag}: backdrop click closes slide-over`, async () => {
      await page.evaluate((sel) => {
        const t = document.querySelector(sel);
        if (!t) return;
        const rows = Array.from(t.querySelectorAll('tbody tr'));
        const row = rows.find(r => r.hasAttribute('data-action'))
                || rows.find(r => r.hasAttribute('data-value'))
                || rows.find(r => r.children.length > 0);
        if (!row) return;
        const td = row.querySelector('td:not(:empty)') || row;
        td.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window }));
      }, p.tableSel);
      try {
        await page.waitForFunction(() => {
          const panel = document.querySelector('.slide-over-panel');
          return panel && !panel.hidden;
        }, null, { timeout: 5000 });
      } catch (_) {}
      // Click the backdrop directly.
      await page.evaluate(() => {
        const b = document.querySelector('.slide-over-backdrop');
        if (b) b.click();
      });
      await page.waitForTimeout(200);
      const gone = await page.evaluate(() => {
        const panel = document.querySelector('.slide-over-panel');
        if (!panel || panel.hidden) return true;
        const r = panel.getBoundingClientRect();
        return r.width === 0 || r.height === 0;
      });
      assert(gone, 'slide-over still visible after backdrop click');
    });

    await step(`${tag}: X button closes slide-over`, async () => {
      await page.evaluate((sel) => {
        const t = document.querySelector(sel);
        if (!t) return;
        const rows = Array.from(t.querySelectorAll('tbody tr'));
        const row = rows.find(r => r.hasAttribute('data-action'))
                || rows.find(r => r.hasAttribute('data-value'))
                || rows.find(r => r.children.length > 0);
        if (!row) return;
        const td = row.querySelector('td:not(:empty)') || row;
        td.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window }));
      }, p.tableSel);
      try {
        await page.waitForFunction(() => {
          const panel = document.querySelector('.slide-over-panel');
          return panel && !panel.hidden;
        }, null, { timeout: 5000 });
      } catch (_) {}
      await page.evaluate(() => {
        const x = document.querySelector('.slide-over-panel .slide-over-close');
        if (x) x.click();
      });
      await page.waitForTimeout(200);
      const gone = await page.evaluate(() => {
        const panel = document.querySelector('.slide-over-panel');
        if (!panel || panel.hidden) return true;
        const r = panel.getBoundingClientRect();
        return r.width === 0 || r.height === 0;
      });
      assert(gone, 'slide-over still visible after X click');
    });

    await ctx.close();
  }

  // ============================================================
  // #1168 review must-fix #1: Focus trap — Tab/Shift-Tab cycle
  // inside the panel. Behavior implemented in commit 76ec12c
  // ("SlideOver a11y polish — focus trap"); this block adds the
  // missing assertion so a future refactor that breaks the trap
  // goes red.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);

    await step('focus-trap@800 nodes: Shift+Tab from first focusable wraps to last', async () => {
      await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#nodesTable tbody tr[data-value]', { timeout: 8000 });
      await page.evaluate(() => {
        const r = document.querySelector('#nodesTable tbody tr[data-value]');
        r.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window }));
      });
      await page.waitForFunction(() => {
        const p = document.querySelector('.slide-over-panel');
        return p && !p.hidden;
      }, null, { timeout: 8000 });
      // Focus the close (X) button, which is also the first focusable in tab
      // order inside the panel; Shift+Tab MUST wrap focus to the last
      // focusable element inside the panel — NOT escape to <body>.
      await page.evaluate(() => {
        const x = document.querySelector('.slide-over-panel .slide-over-close');
        x.focus();
      });
      const firstFocused = await page.evaluate(() => {
        return document.activeElement && document.activeElement.classList.contains('slide-over-close');
      });
      assert(firstFocused, 'precondition: X button should be focused');
      await page.keyboard.press('Shift+Tab');
      const wrapped = await page.evaluate(() => {
        const p = document.querySelector('.slide-over-panel');
        if (!p) return { ok: false, why: 'panel gone' };
        if (!p.contains(document.activeElement)) {
          return { ok: false, why: 'focus escaped panel', activeTag: document.activeElement && document.activeElement.tagName };
        }
        const focusables = Array.from(p.querySelectorAll(
          'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
        ));
        const last = focusables[focusables.length - 1];
        return { ok: document.activeElement === last, focusableCount: focusables.length };
      });
      assert(wrapped.ok, 'Shift+Tab should wrap to last focusable in panel: ' + JSON.stringify(wrapped));
    });

    await step('focus-trap@800 nodes: Tab from last focusable wraps back to first', async () => {
      const setup = await page.evaluate(() => {
        const p = document.querySelector('.slide-over-panel');
        if (!p) return { ok: false };
        const focusables = Array.from(p.querySelectorAll(
          'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
        ));
        if (!focusables.length) return { ok: false, focusableCount: 0 };
        const last = focusables[focusables.length - 1];
        last.focus();
        return { ok: document.activeElement === last, focusableCount: focusables.length };
      });
      assert(setup.ok, 'precondition: last focusable should focus: ' + JSON.stringify(setup));
      await page.keyboard.press('Tab');
      const wrapped = await page.evaluate(() => {
        const p = document.querySelector('.slide-over-panel');
        if (!p || !p.contains(document.activeElement)) return { ok: false };
        const focusables = Array.from(p.querySelectorAll(
          'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
        ));
        const first = focusables[0];
        return { ok: document.activeElement === first };
      });
      assert(wrapped.ok, 'Tab from last focusable should wrap back to first');
    });

    await ctx.close();
  }

  // ============================================================
  // #1168 review must-fix #2: Focus restore — closing the panel
  // (Escape and X) returns focus to the row that opened it.
  // Behavior implemented in commit 76ec12c; assertion added now.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#nodesTable tbody tr[data-value]', { timeout: 8000 });

    async function openPanelFromRow() {
      // Capture the row's data-value (stable across re-renders) and focus it.
      // We can't use a synthetic id because renderRows() rebuilds the tbody
      // on close — by then any injected id is gone.
      const rowKey = await page.evaluate(() => {
        const r = document.querySelector('#nodesTable tbody tr[data-value]');
        if (!r) return null;
        if (!r.hasAttribute('tabindex')) r.setAttribute('tabindex', '0');
        r.focus();
        // Click via dispatch so delegated handlers fire.
        r.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window }));
        return r.getAttribute('data-value');
      });
      assert(rowKey, 'no nodes row found for focus-restore test');
      await page.waitForFunction(() => {
        const p = document.querySelector('.slide-over-panel');
        return p && !p.hidden;
      }, null, { timeout: 8000 });
      return rowKey;
    }

    await step('focus-restore@800: Escape returns focus to originating row', async () => {
      const rowKey = await openPanelFromRow();
      await page.keyboard.press('Escape');
      // Wait for renderRows() + post-rAF focus restore to settle.
      await page.waitForFunction((key) => {
        const esc = (window.CSS && CSS.escape) ? CSS.escape(key) : key;
        const row = document.querySelector('#nodesTable tbody tr[data-value="' + esc + '"]');
        return !!row && document.activeElement === row;
      }, rowKey, { timeout: 2000 }).catch(() => {});
      const r = await page.evaluate((key) => {
        const esc = (window.CSS && CSS.escape) ? CSS.escape(key) : key;
        const row = document.querySelector('#nodesTable tbody tr[data-value="' + esc + '"]');
        return {
          rowExists: !!row,
          isActive: !!row && document.activeElement === row,
          activeTag: document.activeElement && document.activeElement.tagName,
          activeAttrs: document.activeElement && {
            id: document.activeElement.id,
            cls: document.activeElement.className,
            dv: document.activeElement.getAttribute && document.activeElement.getAttribute('data-value'),
          },
        };
      }, rowKey);
      assert(r.rowExists, 'originating row (data-value=' + rowKey + ') vanished from DOM after re-render');
      assert(r.isActive, 'focus did NOT restore to originating row after Escape: ' + JSON.stringify(r));
    });

    await step('focus-restore@800: re-renders while open still restore to new row instance', async () => {
      const rowKey = await openPanelFromRow();
      
      // Force a re-render of the table so the original DOM node is detached
      await page.evaluate(() => {
        if (typeof window.renderRows === 'function') {
           window.renderRows();
        }
      });

      await page.keyboard.press('Escape');
      // Wait for renderRows() + post-rAF focus restore to settle.
      await page.waitForFunction((key) => {
        const esc = (window.CSS && CSS.escape) ? CSS.escape(key) : key;
        const row = document.querySelector('#nodesTable tbody tr[data-value="' + esc + '"]');
        return !!row && document.activeElement === row;
      }, rowKey, { timeout: 2000 }).catch(() => {});

      const r = await page.evaluate((key) => {
        const esc = (window.CSS && CSS.escape) ? CSS.escape(key) : key;
        const row = document.querySelector('#nodesTable tbody tr[data-value="' + esc + '"]');
        return {
          rowExists: !!row,
          isActive: !!row && document.activeElement === row,
        };
      }, rowKey);
      assert(r.rowExists, 'originating row vanished from DOM after manual re-render');
      assert(r.isActive, 'focus did NOT restore to NEW row instance after Escape: ' + JSON.stringify(r));
    });

    // ------------------------------------------------------------------
    // #1616: hard-asserted (architectural fix in SlideOver.close — detach
    // panel on close, MutationObserver fallback for late-attaching rows).
    // Supersedes the #1172 soften-and-track approach.
    // ------------------------------------------------------------------
    await step('focus-restore@800: X-button click returns focus to originating row', async () => {
      const rowKey = await openPanelFromRow();
      await page.evaluate(() => {
        const x = document.querySelector('.slide-over-panel .slide-over-close');
        x.click();
      });
      await page.waitForTimeout(300);
      const r = await page.evaluate((key) => {
        const esc = (window.CSS && CSS.escape) ? CSS.escape(key) : key;
        const row = document.querySelector('#nodesTable tbody tr[data-value="' + esc + '"]');
        return {
          rowExists: !!row,
          isActive: !!row && document.activeElement === row,
        };
      }, rowKey);
      assert(r.rowExists, 'originating row vanished from DOM');
      assert(r.isActive, 'focus did NOT restore to originating row after X click: ' + JSON.stringify(r));
    });

    // ------------------------------------------------------------------
    // #1616 followup: SYNTHETIC deterministic repro for the Escape-path
    // focus-restore race. The natural test above flakes ~50% of the time
    // in Chromium-headless because the nodes module's onClose calls
    // `renderRows()` synchronously and the originating <tr> instance is
    // replaced — but the exact micro-timing between focus(), the panel
    // detach, and the synchronous innerHTML swap is racy: under some
    // schedules Chromium drops activeElement to <body> before the
    // replacement <tr> is laid out.
    //
    // This test forces the race deterministically by driving SlideOver
    // directly with an onClose that defers the row swap to a microtask
    // (Promise.resolve().then(...)). Resolver runs synchronously at
    // close-time and returns the OLD (still-attached) tr — but by the
    // time the microtask fires, the OLD tr is detached and Chromium
    // re-derives activeElement = <body>. The MutationObserver fallback
    // SHOULD pick up the new row attachment and re-land focus.
    // ------------------------------------------------------------------
    await step('focus-restore@800: Escape with async-cb re-render (synthetic)', async () => {
      // Build an isolated test surface: a tbody with a single row and a
      // resolver/closer pair driven through SlideOver.open() directly.
      // We rebuild the row from scratch in a microtask after close() to
      // exactly mirror the production async-render race.
      await page.evaluate(() => {
        // Clean any prior fixture so re-runs are deterministic.
        const old = document.getElementById('synth-1616');
        if (old) old.remove();
        const wrap = document.createElement('div');
        wrap.id = 'synth-1616';
        wrap.innerHTML = '<table><tbody id="synth-tbody">'
          + '<tr data-value="row-A" tabindex="0"><td>A</td></tr>'
          + '</tbody></table>';
        document.body.appendChild(wrap);
      });
      // Focus the row, open SlideOver with a microtask-delayed onClose.
      await page.evaluate(() => {
        const tbody = document.getElementById('synth-tbody');
        const row = tbody.querySelector('tr[data-value="row-A"]');
        row.focus();
        window.SlideOver.open({
          title: 'Synthetic',
          restoreFocus: function () {
            return document.querySelector('#synth-tbody tr[data-value="row-A"]');
          },
          onClose: function () {
            // Defer the row swap to a microtask so it lands AFTER
            // close()'s synchronous focus + detach. This is the precise
            // race: resolver() returned the OLD tr (still attached at
            // close() time); the microtask then replaces it.
            Promise.resolve().then(function () {
              tbody.innerHTML = '<tr data-value="row-A" tabindex="0"><td>A-new</td></tr>';
            });
          }
        });
      });
      // Slide-over is open. Press Escape.
      await page.keyboard.press('Escape');
      // Wait for the new row to mount AND focus to land on it.
      await page.waitForFunction(() => {
        const row = document.querySelector('#synth-tbody tr[data-value="row-A"]');
        return !!row && document.activeElement === row;
      }, null, { timeout: 1500 }).catch(() => {});
      const r = await page.evaluate(() => {
        const row = document.querySelector('#synth-tbody tr[data-value="row-A"]');
        return {
          rowExists: !!row,
          isActive: !!row && document.activeElement === row,
          rowText: row && row.textContent,
          activeTag: document.activeElement && document.activeElement.tagName,
        };
      });
      assert(r.rowExists, 'synthetic row vanished after async cb');
      assert(r.rowText === 'A-new', 'synthetic row was not re-rendered by microtask (got: ' + r.rowText + ')');
      assert(r.isActive, 'focus did NOT restore to NEW synthetic row after async cb: ' + JSON.stringify(r));
    });

    await ctx.close();
  }

  // ============================================================
  // #1168 review must-fix #3: Open-2nd-row race — opening row B
  // while row A's panel is open must (a) keep exactly one
  // backdrop, (b) reflect row B's content, and (c) fire row A's
  // onClose proxy exactly once. SlideOver.open() handles this in
  // commit 7498083 via `if (isOpen()) close();`.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#nodesTable tbody tr[data-value]', { timeout: 8000 });
    await page.waitForFunction(() => {
      return document.querySelectorAll('#nodesTable tbody tr[data-value]').length >= 2;
    }, null, { timeout: 8000 });

    await step('race@800 nodes: open row A, then row B → single backdrop, row A onClose fired exactly once', async () => {
      // Drive open() directly via the SlideOver public API so we can install
      // an onClose proxy and observe call count without relying on a
      // particular page wiring.
      const result = await page.evaluate(() => {
        if (!window.SlideOver) return { ok: false, why: 'no SlideOver' };
        let aCloseCalls = 0;
        const aContent = window.SlideOver.open({
          title: 'Row A',
          onClose: function () { aCloseCalls++; },
        });
        if (!aContent) return { ok: false, why: 'open A returned no content' };
        aContent.innerHTML = '<p>A body</p>';
        // Snapshot state mid-A.
        const backdropsAfterA = document.querySelectorAll('.slide-over-backdrop').length;
        const panelsAfterA = document.querySelectorAll('.slide-over-panel').length;
        // Now open B without closing A — this should trigger A's onClose
        // proxy exactly once and replace the panel content.
        const bContent = window.SlideOver.open({ title: 'Row B' });
        bContent.innerHTML = '<p>B body</p>';
        return {
          ok: true,
          backdropsAfterA,
          panelsAfterA,
          backdropsAfterB: document.querySelectorAll('.slide-over-backdrop').length,
          panelsAfterB: document.querySelectorAll('.slide-over-panel').length,
          titleNow: document.querySelector('.slide-over-title').textContent,
          bodyNow: document.querySelector('.slide-over-content').textContent,
          aCloseCalls,
          bodyOverflow: getComputedStyle(document.body).overflow,
          bodyHasLockClass: document.body.classList.contains('scroll-locked'),
        };
      });
      assert(result.ok, 'race precondition failed: ' + JSON.stringify(result));
      assert(result.backdropsAfterB === 1, 'expected exactly one backdrop after open(B), got ' + result.backdropsAfterB);
      assert(result.panelsAfterB === 1, 'expected exactly one panel after open(B), got ' + result.panelsAfterB);
      assert(result.titleNow === 'Row B', 'title should reflect row B, got: ' + result.titleNow);
      assert(result.bodyNow.indexOf('B body') !== -1, 'content should reflect row B, got: ' + result.bodyNow);
      assert(result.aCloseCalls === 1, 'row A onClose should fire exactly once, got ' + result.aCloseCalls);
      assert(result.bodyHasLockClass, 'scroll-locked class must remain after open(B) (single ref-count, not released-and-re-locked)');
      assert(result.bodyOverflow === 'hidden', 'body scroll lock must remain (single lock, not double-restored): ' + result.bodyOverflow);
      // Cleanup
      await page.evaluate(() => window.SlideOver.close());
    });

    await ctx.close();
  }

  // ============================================================
  // #1168 review must-fix #4: Resize crossing breakpoint cleans
  // up. Open at 800w (slide-over branch), then resize to 1440w
  // (>1023 BP). Debounced resize listener (commit 76ec12c) must
  // close the panel, hide the backdrop, release the body
  // scroll-lock, AND restore focus.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#nodesTable tbody tr[data-value]', { timeout: 8000 });

    await step('resize@800→1440 nodes: cleanup releases panel, backdrop, scroll-lock, focus', async () => {
      const rowKey = await page.evaluate(() => {
        const r = document.querySelector('#nodesTable tbody tr[data-value]');
        if (!r) return null;
        r.focus();
        r.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window }));
        return r.getAttribute('data-value');
      });
      assert(rowKey, 'no nodes row for resize test');
      await page.waitForFunction(() => {
        const p = document.querySelector('.slide-over-panel');
        return p && !p.hidden;
      }, null, { timeout: 8000 });
      // Cross the breakpoint upwards.
      await page.setViewportSize({ width: 1440, height: 900 });
      // Resize listener is debounced ~120ms; give it a comfortable window.
      await page.waitForTimeout(500);
      const after = await page.evaluate((key) => {
        function isShown(el) {
          if (!el) return false;
          if (el.hidden) return false;
          const r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0;
        }
        const esc = (window.CSS && CSS.escape) ? CSS.escape(key) : key;
        const row = document.querySelector('#nodesTable tbody tr[data-value="' + esc + '"]');
        return {
          panelGone: !isShown(document.querySelector('.slide-over-panel')),
          backdropGone: !isShown(document.querySelector('.slide-over-backdrop')),
          bodyOverflow: getComputedStyle(document.body).overflow,
          bodyHasLockClass: document.body.classList.contains('scroll-locked'),
          rowExists: !!row,
          focusRestored: !!row && document.activeElement === row,
          activeTag: document.activeElement && document.activeElement.tagName,
        };
      }, rowKey);
      assert(after.panelGone, 'panel still shown after viewport crossed BP: ' + JSON.stringify(after));
      assert(after.backdropGone, 'backdrop still shown after viewport crossed BP');
      assert(!after.bodyHasLockClass, 'scroll-locked class still present after viewport crossed BP');
      assert(after.bodyOverflow !== 'hidden', 'body scroll-lock not released after viewport crossed BP (overflow=' + after.bodyOverflow + ')');
      // #1616: focus-restore portion is now hard-asserted (architectural
      // fix in SlideOver.close — panel detach + MutationObserver fallback).
      assert(after.rowExists, 'originating row vanished after viewport-crossing close');
      assert(after.focusRestored, 'focus did NOT restore to originating row after viewport-crossing close: ' + JSON.stringify(after));
    });

    // #1616: the formerly-skipped duplicate focus-only test has been merged
    // into the cleanup assertion above (single open/resize cycle). The
    // separate test was unreachable anyway — by the time it ran the viewport
    // was already 1440, so SlideOver.shouldUse() returned false and the
    // panel never opened.

    await ctx.close();
  }

  // ---- Wide viewport: slide-over MUST NOT appear (regression guard) ----
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);

    await step('wide@1440 packets: row click does NOT open slide-over', async () => {
      await page.goto(BASE + '/#/packets' + PACKETS_QUERY, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#pktTable', { timeout: 8000 });
      await page.waitForFunction(() => document.querySelectorAll('#pktTable tbody tr').length > 0, null, { timeout: 8000 });
      await page.evaluate(() => {
        const r = document.querySelector('#pktTable tbody tr');
        if (r) r.click();
      });
      await page.waitForTimeout(300);
      const slideOverShown = await page.evaluate(() => {
        const p = document.querySelector('.slide-over-panel');
        if (!p || p.hidden) return false;
        const r = p.getBoundingClientRect();
        return r.width > 0 && r.height > 0;
      });
      assert(!slideOverShown, 'slide-over should NOT appear at 1440px width');
    });

    await ctx.close();
  }

  await browser.close();

  console.log(`\n=== #1056 AC#4 slide-over E2E: ${passed} passed, ${failed} failed, ${skipped} skipped ===`);
  process.exit(failed ? 1 : 0);
})();
