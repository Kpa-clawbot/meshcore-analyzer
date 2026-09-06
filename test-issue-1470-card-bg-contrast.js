/* test-issue-1470-card-bg-contrast.js — unit tests for the dark-mode card-bg
 * contrast fix shipped in #1517.
 *
 * Verifies two independent things:
 *
 *   1. CSS: both dark-mode variable blocks in public/style.css set
 *      --card-bg to var(--surface-2), not var(--surface-1).
 *
 *   2. JS fallback chain: all three theme-apply paths
 *      (customize.js / app.js / customize-v2.js) resolve --card-bg as
 *      cardBg || surface2 || surface1 so that saved themes without an
 *      explicit cardBg inherit surface-2 (new default) rather than
 *      reverting to surface-1 (old low-contrast value).
 *
 * Mutation guards: reverting either hunk makes the corresponding test fail.
 */
'use strict';
const fs   = require('fs');
const path = require('path');
const vm   = require('vm');
const assert = require('assert');

const ROOT = __dirname;
function read(rel) { return fs.readFileSync(path.join(ROOT, rel), 'utf8'); }

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// ─── 1. CSS variable assertions ──────────────────────────────────────────────

console.log('\n── #1470 CSS: --card-bg dark-mode variable ──');

test('@media dark block sets --card-bg to var(--surface-2)', () => {
  const css = read('public/style.css');
  // Find the @media (prefers-color-scheme: dark) block
  const mediaIdx = css.indexOf('@media (prefers-color-scheme: dark)');
  assert.ok(mediaIdx !== -1, '@media dark block not found');
  const mediaBlock = css.slice(mediaIdx, mediaIdx + 2000);
  const m = mediaBlock.match(/--card-bg\s*:\s*([^;]+);/);
  assert.ok(m, '--card-bg not found in @media dark block');
  assert.strictEqual(m[1].trim(), 'var(--surface-2)',
    `Expected var(--surface-2), got "${m[1].trim()}"`);
});

test('[data-theme="dark"] block sets --card-bg to var(--surface-2)', () => {
  const css = read('public/style.css');
  const rules = css.replace(/\/\*[\s\S]*?\*\//g, '');
  const blocks = Array.from(rules.matchAll(/\[data-theme="dark"\]\s*\{([^}]*)\}/g), m => m[1]);
  assert.ok(blocks.length, '[data-theme="dark"] block not found');
  const m = blocks.join('\n').match(/--card-bg\s*:\s*([^;]+);/);
  assert.ok(m, '--card-bg not found in [data-theme="dark"] block');
  assert.strictEqual(m[1].trim(), 'var(--surface-2)',
    `Expected var(--surface-2), got "${m[1].trim()}"`);
});

test('light-mode block keeps --card-bg as var(--surface-1)', () => {
  const css = read('public/style.css');
  // Light mode block ends before the @media dark block
  const mediaIdx = css.indexOf('@media (prefers-color-scheme: dark)');
  const lightBlock = css.slice(0, mediaIdx);
  const m = lightBlock.match(/--card-bg\s*:\s*([^;]+);/);
  assert.ok(m, '--card-bg not found in light-mode :root block');
  assert.strictEqual(m[1].trim(), 'var(--surface-1)',
    `Light mode should stay var(--surface-1), got "${m[1].trim()}"`);
});

// ─── 2. JS fallback chain: behavioural ───────────────────────────────────────

console.log('\n── #1470 JS: --card-bg fallback chain (cardBg || surface2 || surface1) ──');

// Minimal mock for document.documentElement.style
function makeRoot() {
  const props = {};
  return {
    props,
    setProperty(name, val) { props[name] = val; },
    getPropertyValue(name) { return props[name] || ''; },
  };
}

// Run the three-file fallback logic in isolation and collect the applied --card-bg.
// Each file uses slightly different variable names; we normalise to (surface1, surface2, cardBg).
function runCardBgFallback(surface1, surface2, cardBg) {
  const root = makeRoot();
  // Reproduce the shared pattern:  cardBg || surface2 || surface1
  const t = { surface1, surface2, cardBg };
  if (t.surface1) root.setProperty('--card-bg', t.cardBg || t.surface2 || t.surface1);
  return root.props['--card-bg'];
}

test('surface2 wins when cardBg is absent (the new default case)', () => {
  const result = runCardBgFallback('#1a1a2e', '#232340', undefined);
  assert.strictEqual(result, '#232340', `Expected #232340 (surface-2), got "${result}"`);
});

test('explicit cardBg always wins over surface2', () => {
  const result = runCardBgFallback('#1a1a2e', '#232340', '#ff0000');
  assert.strictEqual(result, '#ff0000', `Expected explicit cardBg, got "${result}"`);
});

test('surface1 is used only when both cardBg and surface2 are absent (legacy compat)', () => {
  const result = runCardBgFallback('#1a1a2e', undefined, undefined);
  assert.strictEqual(result, '#1a1a2e', `Expected surface1 fallback, got "${result}"`);
});

test('surface1 absent — no --card-bg override applied', () => {
  const result = runCardBgFallback(undefined, '#232340', undefined);
  assert.strictEqual(result, undefined, 'No card-bg should be set when surface1 absent');
});

// ─── 3. Source mutation guards ────────────────────────────────────────────────

console.log('\n── #1470 source: mutation guards for all three JS files ──');

test('customize.js uses cardBg || surface2 || surface1 fallback', () => {
  const src = read('public/customize.js');
  assert.ok(
    /t\.cardBg\s*\|\|\s*t\.surface2\s*\|\|\s*t\.surface1/.test(src),
    'customize.js: expected "t.cardBg || t.surface2 || t.surface1" not found'
  );
});

test('app.js uses cardBg || surface2 || surface1 fallback', () => {
  const src = read('public/app.js');
  assert.ok(
    /themeData\.cardBg\s*\|\|\s*themeData\.surface2\s*\|\|\s*themeData\.surface1/.test(src),
    'app.js: expected "themeData.cardBg || themeData.surface2 || themeData.surface1" not found'
  );
});

test('customize-v2.js uses cardBg || surface2 || surface1 fallback (both call sites)', () => {
  const src = read('public/customize-v2.js');
  const matches = (src.match(/themeSection\.cardBg\s*\|\|\s*themeSection\.surface2\s*\|\|\s*themeSection\.surface1/g) || []).length;
  assert.strictEqual(matches, 2,
    `customize-v2.js: expected 2 occurrences of the fallback chain, found ${matches}`);
});

// ─── summary ─────────────────────────────────────────────────────────────────

console.log(`\n#1470 card-bg contrast: ${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
