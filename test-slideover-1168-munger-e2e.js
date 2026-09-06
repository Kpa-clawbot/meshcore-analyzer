/**
 * E2E (#1168 Munger review): SlideOver hardening.
 *
 * Three regressions surfaced by Munger persona on PR #1168:
 *
 *   (1) rAF-clobber: close()'s deferred focus-restore captured `target`
 *       in closure scope. If close() ran, then a NEW open() ran on the
 *       SAME microtask before rAF fired, the old rAF would `target.focus()`
 *       AFTER the new panel opened, stealing focus back to row A's row.
 *       Fix: openSeq counter + rAF guard — only restore if no newer open
 *       has happened since close-time.
 *
 *   (2) hashchange: `location.hash` changing (e.g. user navigates
 *       /#/packets → /#/nodes via the URL bar or a hash anchor) did NOT
 *       close the open slide-over. Panel + backdrop + scroll-lock
 *       leaked across pages. Fix: window.addEventListener('hashchange',
 *       () => isOpen() && close()).
 *
 *   (3) Scroll-lock corruption: capturing literal body.style.overflow at
 *       open-time and restoring on close means two cooperating modal
 *       surfaces (e.g. SlideOver + ChannelColorPicker) running concurrently
 *       can leave overflow in a wrong state (last-writer-wins). Fix:
 *       reference-counted `body.scroll-locked` class — multiple lockers
 *       each add/remove independently; class only removed when count==0.
 *
 * Usage: BASE_URL=http://localhost:13581 node test-slideover-1168-munger-e2e.js
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

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  console.log(`\n=== #1168 Munger SlideOver hardening E2E against ${BASE} ===`);

  // ============================================================
  // Item 1: rAF-clobber — close A then immediately open B; the
  // pending rAF from close(A) must NOT focus row A's row after
  // panel B is open. Panel B (or its first focusable) wins.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#nodesTable tbody tr[data-value]', { timeout: 8000 });

    await step('rAF-clobber: close-then-open does NOT steal focus back to row A', async () => {
      const result = await page.evaluate(async () => {
        if (!window.SlideOver) return { ok: false, why: 'no SlideOver' };
        // Synthesize a "row A" that close() would otherwise focus on rAF.
        const fakeRowA = document.createElement('button');
        fakeRowA.id = '__munger_rowA';
        fakeRowA.textContent = 'rowA';
        document.body.appendChild(fakeRowA);
        fakeRowA.focus();

        // Open A — capturing fakeRowA as prevFocus.
        const aContent = window.SlideOver.open({ title: 'A' });
        aContent.innerHTML = '<button id="__a_btn">a</button>';

        // Close A. close() schedules an rAF that would focus fakeRowA
        // when it fires. Without the openSeq guard this rAF will
        // run AFTER the next open() and steal focus.
        window.SlideOver.close();

        // IMMEDIATELY open B before the rAF fires.
        const bContent = window.SlideOver.open({ title: 'B' });
        bContent.innerHTML = '<button id="__b_btn">b</button>';
        // Move focus into B explicitly (caller convention — first focusable).
        document.querySelector('#__b_btn').focus();

        // Wait two animation frames so the stale rAF would have fired.
        await new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(res)));

        const active = document.activeElement;
        const result = {
          ok: true,
          panelOpen: window.SlideOver.isOpen(),
          activeId: active && active.id,
          activeTag: active && active.tagName,
          // Critical: focus is NOT on the closed-A's originating fake row.
          stoleBackToA: active === fakeRowA,
        };
        // Cleanup
        window.SlideOver.close();
        fakeRowA.remove();
        return result;
      });
      assert(result.ok, 'precondition: ' + JSON.stringify(result));
      assert(result.panelOpen, 'panel B should remain open after stale rAF window');
      assert(!result.stoleBackToA,
        'Stale rAF from close(A) clobbered focus back to row A after open(B). ' +
        'activeId=' + result.activeId + ', tag=' + result.activeTag);
    });

    await ctx.close();
  }

  // ============================================================
  // Item 2: hashchange cleanup. Open slide-over on /#/packets,
  // navigate to /#/nodes via location.hash, assert panel hidden,
  // backdrop hidden, scroll-lock released.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    // #1923 follow-up: pin an explicit window so this step tests the munger
    // slide-over rather than the clock, the same way #1924 pinned
    // test-slideover-1056-e2e.js. The packets page defaults to
    // `since = now - 15 min` client-side, and the freshened fixture's newest
    // rows age out of that window ~15 minutes after freshen-fixture.sh runs.
    // Must be > 0: packets.js reads the param only under `_urlTimeWindow > 0`,
    // so `timeWindow=0` silently keeps the default.
    await page.goto(BASE + '/#/packets?timeWindow=1440', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#pktTable tbody tr[data-action]', { timeout: 8000 });

    await step('hashchange: navigating away closes slide-over + releases scroll-lock', async () => {
      // Open slide-over via SlideOver API directly (page-agnostic).
      await page.evaluate(() => {
        const c = window.SlideOver.open({ title: 'X' });
        c.innerHTML = '<p>x</p>';
      });
      const before = await page.evaluate(() => ({
        open: window.SlideOver.isOpen(),
        bodyHasLockClass: document.body.classList.contains('scroll-locked'),
        bodyOverflowComputed: getComputedStyle(document.body).overflow,
      }));
      assert(before.open, 'precondition: panel should be open');

      // Trigger a hashchange.
      await page.evaluate(() => { location.hash = '#/nodes'; });
      // Allow hashchange listeners to flush.
      await page.waitForTimeout(150);

      const after = await page.evaluate(() => {
        const p = document.querySelector('.slide-over-panel');
        const b = document.querySelector('.slide-over-backdrop');
        function shown(el) {
          if (!el) return false;
          if (el.hidden) return false;
          const r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0;
        }
        return {
          isOpen: window.SlideOver.isOpen(),
          panelShown: shown(p),
          backdropShown: shown(b),
          bodyHasLockClass: document.body.classList.contains('scroll-locked'),
          bodyOverflowComputed: getComputedStyle(document.body).overflow,
        };
      });
      assert(!after.isOpen, 'SlideOver.isOpen() must be false after hashchange: ' + JSON.stringify(after));
      assert(!after.panelShown, 'panel must be hidden after hashchange: ' + JSON.stringify(after));
      assert(!after.backdropShown, 'backdrop must be hidden after hashchange: ' + JSON.stringify(after));
      assert(!after.bodyHasLockClass, 'body must NOT carry scroll-locked class after hashchange: ' + JSON.stringify(after));
    });

    await ctx.close();
  }

  // ============================================================
  // Item 3: Scroll-lock ref-count via class. Two simulated
  // lockers can independently add/remove without corruption.
  // The fix uses `body.scroll-locked` class (CSS supplies
  // overflow:hidden) — class-add is idempotent; removal only
  // happens when the last locker releases. We assert this with
  // a direct simulation independent of which surface owns it.
  // ============================================================
  {
    const ctx = await browser.newContext({ viewport: { width: 800, height: 800 } });
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    page.on('pageerror', (e) => console.error('[pageerror]', e.message));
    await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#nodesTable tbody tr[data-value]', { timeout: 8000 });

    await step('scroll-lock: ref-counted class survives interleaved lockers without corruption', async () => {
      const result = await page.evaluate(() => {
        if (!window.__scrollLock) {
          return { ok: false, why: 'window.__scrollLock helper not exposed (item 3 not implemented)' };
        }
        // Simulate locker A acquiring → CSS class present + overflow hidden.
        const tokenA = window.__scrollLock.acquire();
        const afterA = {
          cls: document.body.classList.contains('scroll-locked'),
          ovf: getComputedStyle(document.body).overflow,
        };
        // Simulate locker B acquiring → still locked.
        const tokenB = window.__scrollLock.acquire();
        const afterB = {
          cls: document.body.classList.contains('scroll-locked'),
          ovf: getComputedStyle(document.body).overflow,
        };
        // Locker A releases first → still locked because B holds it.
        window.__scrollLock.release(tokenA);
        const afterAreleased = {
          cls: document.body.classList.contains('scroll-locked'),
          ovf: getComputedStyle(document.body).overflow,
        };
        // Locker B releases → unlocked.
        window.__scrollLock.release(tokenB);
        const afterBreleased = {
          cls: document.body.classList.contains('scroll-locked'),
          ovf: getComputedStyle(document.body).overflow,
        };
        return { ok: true, afterA, afterB, afterAreleased, afterBreleased };
      });
      assert(result.ok, 'precondition: ' + JSON.stringify(result));
      assert(result.afterA.cls, 'after A acquire: scroll-locked class missing');
      assert(result.afterA.ovf === 'hidden', 'after A acquire: overflow not hidden (got ' + result.afterA.ovf + ')');
      assert(result.afterB.cls, 'after B acquire: scroll-locked class missing');
      assert(result.afterAreleased.cls, 'after A release: scroll-locked must STAY (B still holds): ' + JSON.stringify(result.afterAreleased));
      assert(result.afterAreleased.ovf === 'hidden', 'after A release: overflow must stay hidden (B still holds)');
      assert(!result.afterBreleased.cls, 'after B release: scroll-locked class must be gone: ' + JSON.stringify(result.afterBreleased));
      assert(result.afterBreleased.ovf !== 'hidden', 'after B release: overflow must NOT be hidden (got ' + result.afterBreleased.ovf + ')');
    });

    await ctx.close();
  }

  await browser.close();

  console.log(`\n=== #1168 Munger SlideOver hardening: ${passed} passed, ${failed} failed ===`);
  process.exit(failed ? 1 : 0);
})();
