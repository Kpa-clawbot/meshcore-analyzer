/**
 * #1297 B2 — Coverage E2E for public/channel-color-picker.js
 *
 * Exercises the picker popover by driving its public API
 * (window.ChannelColorPicker.show / .hide) on the /#/channels page and
 * asserting:
 *   - palette renders all 8 swatches
 *   - clicking a swatch writes ChannelColors.set + updates .ch-color-dot
 *   - Escape closes popover
 *   - keyboard ArrowRight cycles focus across swatches
 *   - Clear button removes the assignment (when one exists)
 *   - active-class highlights the currently assigned color
 *
 * Usage: BASE_URL=http://localhost:13581 node test-channel-color-picker-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  \u2713 ' + name); }
  catch (e) { failed++; console.error('  \u2717 ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  page.on('pageerror', (e) => console.error('[pageerror]', e.message));

  console.log('\n=== #1297 B2 channel-color-picker E2E against ' + BASE + ' ===');

  // Bootstrap: load page, clear storage
  await page.goto(BASE + '/#/channels', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#chList', { timeout: 8000 });
  await page.evaluate(() => {
    try { localStorage.removeItem('live-channel-colors'); } catch (e) {}
  });

  await step('window.ChannelColorPicker is loaded with PALETTE', async () => {
    const palette = await page.evaluate(() =>
      window.ChannelColorPicker && window.ChannelColorPicker.PALETTE);
    assert(Array.isArray(palette), 'PALETTE missing');
    assert(palette.length === 8, 'expected 8 colors, got ' + palette.length);
    assert(palette[0] === '#ef4444', 'first color should be #ef4444, got ' + palette[0]);
  });

  await step('show() opens popover with 8 swatches', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#testchan', 100, 100));
    await page.waitForSelector('.cc-picker-popover', { timeout: 3000 });
    const swatchCount = await page.$$eval('.cc-swatch', els => els.length);
    assert(swatchCount === 8, 'expected 8 swatches, got ' + swatchCount);
    const visible = await page.evaluate(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display !== 'none';
    });
    assert(visible, 'popover should be visible after show()');
  });

  await step('Escape closes popover', async () => {
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
  });

  await step('clicking a swatch writes ChannelColors + closes popover', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#myroom', 100, 100));
    await page.waitForSelector('.cc-picker-popover');
    // Click the green swatch (#22c55e)
    await page.click('.cc-swatch[data-color="#22c55e"]');
    // Popover should hide
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
    const stored = await page.evaluate(() =>
      window.ChannelColors && window.ChannelColors.get('#myroom'));
    assert(stored === '#22c55e', 'expected stored color #22c55e, got ' + stored);
    const raw = await page.evaluate(() =>
      localStorage.getItem('live-channel-colors'));
    assert(raw && raw.indexOf('#22c55e') >= 0,
      'expected localStorage live-channel-colors to contain #22c55e, got: ' + raw);
  });

  await step('reopening picker for assigned channel marks active swatch + shows Clear', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#myroom', 100, 100));
    await page.waitForSelector('.cc-picker-popover');
    const activeColor = await page.$eval('.cc-swatch.cc-swatch-active',
      el => el.getAttribute('data-color'));
    assert(activeColor === '#22c55e',
      'expected active swatch #22c55e, got ' + activeColor);
    const clearVisible = await page.evaluate(() => {
      const b = document.querySelector('.cc-picker-clear');
      return b && b.style.display !== 'none';
    });
    assert(clearVisible, 'Clear button should be visible when color is assigned');
  });

  await step('Clear button removes the channel color', async () => {
    await page.click('.cc-picker-clear');
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
    const stored = await page.evaluate(() =>
      window.ChannelColors && window.ChannelColors.get('#myroom'));
    assert(stored == null,
      'expected color cleared, got ' + JSON.stringify(stored));
  });

  await step('Clear button is hidden when no color assigned', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#freshchan', 100, 100));
    await page.waitForSelector('.cc-picker-popover');
    const clearHidden = await page.evaluate(() => {
      const b = document.querySelector('.cc-picker-clear');
      return b && b.style.display === 'none';
    });
    assert(clearHidden, 'Clear button should be hidden when no color assigned');
    await page.keyboard.press('Escape');
  });

  await step('ArrowRight cycles focus across swatches', async () => {
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#navchan', 100, 100));
    await page.waitForSelector('.cc-picker-popover');
    // Wait a tick for setTimeout(0) focus
    await page.waitForFunction(() => {
      const el = document.activeElement;
      return el && el.classList && el.classList.contains('cc-swatch');
    }, { timeout: 2000 });
    const firstColor = await page.evaluate(() =>
      document.activeElement.getAttribute('data-color'));
    await page.keyboard.press('ArrowRight');
    // Wait for focus to actually move rather than reading activeElement on the
    // next tick.
    //
    // #1939 added this wait believing the intermittent "was #ef4444, now
    // #ef4444" failure was a macrotask race in which the handler had not yet
    // run. That was wrong, and #1943 has the measurement: the failing step took
    // 16 ms while this wait has a 3 s budget, so it was resolving, not timing
    // out. Focus DID move and was then taken back by showPopover's deferred
    // focus of the first swatch. The product side is fixed in this PR; the wait
    // stays as ordinary defensiveness against a slow handler, not as a cure.
    try {
      await page.waitForFunction((prev) => {
        const el = document.activeElement;
        if (!el || !el.classList || !el.classList.contains('cc-swatch')) return false;
        const c = el.getAttribute('data-color');
        return !!c && c !== prev;
      }, firstColor, { timeout: 3000 });
    } catch (_) { /* fall through so the assertion reports the actual value */ }
    const nextColor = await page.evaluate(() =>
      document.activeElement.getAttribute('data-color'));
    assert(nextColor && nextColor !== firstColor,
      'ArrowRight should move focus to next swatch (was ' + firstColor + ', now ' + nextColor + ')');
    // Enter to assign + close
    await page.keyboard.press('Enter');
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
    const stored = await page.evaluate(() =>
      window.ChannelColors && window.ChannelColors.get('#navchan'));
    assert(stored === nextColor,
      'Enter should assign focused color (' + nextColor + '), got ' + stored);
  });

  await step('a late focus timer does not snap the selection back (#1943)', async () => {
    // Deterministic reproduction of #1943. showPopover() defers focusing the
    // first swatch with setTimeout(0). Reopening while a swatch still has focus
    // means the test's usual "wait for a focused swatch" is satisfied by the
    // OLD focus, so ArrowRight runs before the new timer lands. The timer then
    // fired into the popover and pulled focus back to the first swatch, and
    // Enter assigned that colour instead of the navigated-to one. Proven with a
    // focus() stack trace: the second focus came from channel-color-picker.js:146.
    await page.evaluate(() => window.ChannelColorPicker.show('#lateA', 100, 100));
    await page.waitForFunction(() => {
      const el = document.activeElement;
      return el && el.classList && el.classList.contains('cc-swatch');
    }, { timeout: 2000 });
    await page.keyboard.press('Escape');
    // Reopen and move immediately, without waiting for the new focus timer.
    await page.evaluate(() => window.ChannelColorPicker.show('#lateB', 100, 100));
    const before = await page.evaluate(() =>
      document.activeElement && document.activeElement.getAttribute
        ? document.activeElement.getAttribute('data-color') : null);
    await page.keyboard.press('ArrowRight');
    const moved = await page.evaluate(() =>
      document.activeElement.getAttribute('data-color'));
    // Give any pending setTimeout(0) more than enough time to land.
    await page.waitForTimeout(150);
    const settled = await page.evaluate(() =>
      document.activeElement.getAttribute('data-color'));
    assert(settled === moved,
      'a late focus timer must not move focus after the user did (was ' + before
      + ', moved to ' + moved + ', settled on ' + settled + ')');
    await page.keyboard.press('Enter');
    await page.waitForFunction(() => {
      const el = document.querySelector('.cc-picker-popover');
      return el && el.style.display === 'none';
    }, { timeout: 3000 });
    const stored = await page.evaluate(() =>
      window.ChannelColors && window.ChannelColors.get('#lateB'));
    assert(stored === moved,
      'Enter must assign the swatch the user navigated to (expected ' + moved
      + ', got ' + stored + ')');
    await page.evaluate(() => window.ChannelColors.remove('#lateB'));
  });

  await step('outside click closes popover', async () => {
    // De-flake history: #1317 (62a81776) tried `mouse.click(700,500)` + a
    // `rect.width > 0` "listener installed" proxy. That proxy is FALSE — it
    // only proves the popover is visible, not that showPopover's
    // `setTimeout(0)` document-level click listener has actually run. Under
    // CI load the macrotask can be deferred past Playwright's polling
    // resolution, so the synthetic click fires BEFORE the listener exists,
    // is dropped, and the popover never hides → 8s default-timeout failure
    // (see run 26574358472 / d24246395 master push).
    //
    // Real fix: (1) install a one-shot probe of our own via
    // `requestAnimationFrame + setTimeout(0)` and `await` it from
    // node-side, guaranteeing showPopover's setTimeout(0) drained;
    // (2) retry the click in a small loop, since even with the probe
    // there's no synchronous handle on Playwright's internal event-loop
    // ordering. Each click is cheap (~ms); the popover hides on the first
    // one that reaches the installed listener.
    await page.evaluate(() =>
      window.ChannelColorPicker.show('#outsidechan', 100, 100));
    await page.waitForSelector('.cc-picker-popover', { state: 'visible', timeout: 5000 });
    // Drain pending macrotasks (showPopover's setTimeout(0) installs the
    // outside-click listener). Wait two animation frames + a setTimeout(0)
    // so the same scheduler tier the listener uses has definitely run.
    await page.evaluate(() => new Promise((r) => {
      requestAnimationFrame(() => requestAnimationFrame(() =>
        setTimeout(r, 0)));
    }));
    // Click outside in a retry loop — if the very first synthetic click
    // still races the listener install, subsequent clicks land cleanly.
    // Popover anchored at (100,100); click at (700,500) is unambiguously
    // outside its bounding rect (popover is ~300×80).
    const closed = await (async () => {
      for (let i = 0; i < 10; i++) {
        await page.mouse.click(700, 500);
        try {
          await page.waitForFunction(() => {
            const el = document.querySelector('.cc-picker-popover');
            return el && el.style.display === 'none';
          }, { timeout: 1000 });
          return true;
        } catch (_) {
          // Re-check listener install by waiting another rAF and retrying.
          await page.evaluate(() => new Promise((r) =>
            requestAnimationFrame(() => setTimeout(r, 0))));
        }
      }
      return false;
    })();
    assert(closed, 'popover did not close after 10 outside-click attempts');
  });

  // Cleanup
  await page.evaluate(() => {
    try { localStorage.removeItem('live-channel-colors'); } catch (e) {}
  });

  console.log('\n=== Results: passed ' + passed + ' failed ' + failed + ' ===');
  await browser.close();
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error('FATAL:', e); process.exit(1); });
