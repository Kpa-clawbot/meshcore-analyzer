/**
 * Issue #1646 — Polish follow-ups for the #1644/#1645 observer-comparison
 * redesign. Behavioral CSS+markup assertions only (Node-only, no Playwright).
 *
 * The aesthetic items (font weights, vertical centering, removing decorative
 * bars) are verified visually via screenshots in the PR — this file gates
 * the few items that ARE behaviorally testable so a regression future-proofs
 * the polish.
 *
 *   1) `input[type=checkbox]` has a GLOBAL `accent-color: var(--accent)`
 *      rule (not only the per-page `.col-compare-select` rule that misses
 *      the rest of the surface). Both light + dark must theme. AND no
 *      later override drops accent-color back to a non-token value.
 *   2) BOTH dark-theme blocks (auto via prefers-color-scheme + manual via
 *      [data-theme="dark"]) declare `color-scheme: dark` so UA-native
 *      widgets render dark.
 *   3) `.compare-vs` font-size is smaller than `.compare-select` font-size.
 *   4) `.compare-strip-mid-pct` uses var(--fs-xl) and is the largest text
 *      in the mid cell.
 *   5) `.compare-strip-mid-count` is strictly smaller than
 *      `.compare-strip-mid-pct` (token-rank comparison, not "not --fs-xl").
 *   6) `.compare-asym-line` and `.compare-type-summary` explicitly declare
 *      `border-left: none` AND no `border:` shorthand resolves to a left
 *      edge (Kent Beck: "absence of declaration" is too permissive).
 *   7) compare.js drives the collapse via the actual call
 *      `wrap.classList.toggle('is-collapsed', ready)` — grepping comments
 *      for the literal string is a tautology.
 *   8) The legacy Compare button has been removed from the DOM in compare.js.
 */
'use strict';
const fs = require('fs');
const path = require('path');

const CSS = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const COMPARE_JS = fs.readFileSync(path.join(__dirname, 'public/compare.js'), 'utf8');

// Token-rank used by font-size comparisons. Comments-only mirror of the
// scale; if --fs-xl ever moves, only the relative order matters and that
// is what we assert.
const TOKEN_RANK = {
  'var(--fs-xs)': 1, 'var(--fs-sm)': 2, 'var(--fs-md)': 3,
  'var(--fs-lg)': 4, 'var(--fs-xl)': 5,
};
function fsRank(decl) {
  if (!decl) return null;
  const v = decl.trim();
  if (TOKEN_RANK[v] != null) return TOKEN_RANK[v];
  const num = v.match(/^(\d+(?:\.\d+)?)px$/);
  if (num) return parseFloat(num[1]) / 4; // crude px-to-rank fallback
  return null;
}

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  \u2705 ' + name); }
  catch (e) { failed++; console.error('  \u274c ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

function ruleBlock(css, selectorRegex) {
  // returns the {...} block body for the first matching selector list
  const re = new RegExp('(?:^|[\\s,}])(' + selectorRegex.source + ')[^{}]*\\{([^}]*)\\}', 'm');
  const m = css.match(re);
  return m ? m[2] : null;
}
function fsOf(block) {
  if (!block) return null;
  const m = block.match(/font-size\s*:\s*([^;]+);/);
  return m ? m[1].trim() : null;
}

console.log('\n#1646 compare-polish — behavioral assertions\n');

// ── 1) global checkbox accent-color ───────────────────────────────────
test('global input[type=checkbox] accent-color rule uses var(--accent) and no later rule overrides it to a non-token value', () => {
  const re = /(?:^|})\s*input\[type=["']?checkbox["']?\][^{]*\{[^}]*accent-color\s*:\s*var\(--accent\)/m;
  assert(re.test(CSS),
    'missing top-level `input[type=checkbox] { accent-color: var(--accent); }`');
  // Find every accent-color decl on a checkbox selector (single-line scan)
  // and assert each uses var(--accent ...) — no white/colored hardcodes.
  const ruleRe = /input\[type=["']?checkbox["']?\][^{]*\{([^}]*)\}/g;
  let m;
  while ((m = ruleRe.exec(CSS))) {
    const decl = m[1].match(/accent-color\s*:\s*([^;]+);/);
    if (!decl) continue;
    const val = decl[1].trim();
    assert(/^var\(--/.test(val),
      `checkbox accent-color override must use a CSS var, got "${val}"`);
  }
});

// ── 2) color-scheme on BOTH dark theme blocks ─────────────────────────
test('both dark-theme rules (prefers-color-scheme + [data-theme="dark"]) declare color-scheme: dark', () => {
  const auto = /@media[^{]*prefers-color-scheme:\s*dark[^{]*\{\s*[^{]*\{[^}]*color-scheme\s*:\s*dark/m;
  const manual = /\[data-theme=["']dark["']\][^{]*\{[^}]*color-scheme\s*:\s*dark/m;
  assert(auto.test(CSS), 'missing color-scheme: dark inside @media(prefers-color-scheme: dark)');
  assert(manual.test(CSS), 'missing color-scheme: dark inside [data-theme="dark"] block');
});

// ── 3) .compare-vs smaller than .compare-select ───────────────────────
test('.compare-vs font-size < .compare-select font-size', () => {
  const vsBlock = ruleBlock(CSS, /\.compare-vs/);
  const selBlock = ruleBlock(CSS, /\.compare-select(?![a-zA-Z-])/);
  assert(vsBlock, '.compare-vs block missing');
  assert(selBlock, '.compare-select block missing');
  function px(block) {
    const v = fsOf(block);
    if (!v) return null;
    const tokenMap = {
      'var(--fs-xs)': 11, 'var(--fs-sm)': 12, 'var(--fs-md)': 14,
      'var(--fs-lg)': 15, 'var(--fs-xl)': 18,
    };
    if (tokenMap[v] != null) return tokenMap[v];
    const num = v.match(/^(\d+(?:\.\d+)?)px$/);
    return num ? parseFloat(num[1]) : null;
  }
  const vsSize = px(vsBlock);
  const selSize = px(selBlock);
  assert(vsSize != null && selSize != null, 'could not parse font-size');
  assert(vsSize < selSize, `.compare-vs (${vsSize}) must be smaller than .compare-select (${selSize})`);
});

// ── 4) middle column hierarchy: pct > count > label ───────────────────
test('.compare-strip-mid-pct exists and is var(--fs-xl)', () => {
  const pctBlock = ruleBlock(CSS, /\.compare-strip-mid-pct/);
  assert(pctBlock, '.compare-strip-mid-pct rule missing (needed for inverted hierarchy)');
  assert(/font-size\s*:\s*var\(--fs-xl\)/.test(pctBlock),
    '.compare-strip-mid-pct must be var(--fs-xl) (the largest)');
});

test('.compare-strip-mid-count is strictly smaller than .compare-strip-mid-pct (token-rank)', () => {
  const countBlock = ruleBlock(CSS, /\.compare-strip-mid-count(?!-)/);
  const pctBlock = ruleBlock(CSS, /\.compare-strip-mid-pct(?!-)/);
  assert(countBlock, '.compare-strip-mid-count rule missing');
  assert(pctBlock, '.compare-strip-mid-pct rule missing');
  const cRank = fsRank(fsOf(countBlock));
  const pRank = fsRank(fsOf(pctBlock));
  assert(cRank != null && pRank != null, `could not parse font-sizes (count=${fsOf(countBlock)}, pct=${fsOf(pctBlock)})`);
  assert(cRank < pRank,
    `.compare-strip-mid-count rank (${cRank}) must be < .compare-strip-mid-pct rank (${pRank})`);
});

// ── 5) Compare CTA dead — button removed from DOM ─────────────────────
test('legacy #compareBtn is no longer emitted by compare.js (auto-run replaces it)', () => {
  // Behavioral: the markup string for an explicit Compare button must
  // be gone from the rendered HTML. A render path that conditionally
  // hides a still-present button would let an enabled-but-hidden state
  // exist; deleting the markup makes that impossible.
  const re = /id=["']compareBtn["']/;
  assert(!re.test(COMPARE_JS),
    'compare.js still renders #compareBtn — must be removed (auto-run on selection change)');
  // And no .compare-btn-ghost / .compare-btn class lingers.
  assert(!/compare-btn-ghost/.test(COMPARE_JS),
    'dead .compare-btn-ghost class still emitted');
});

// ── 6) decorative asym-line border-left explicitly removed ────────────
test('.compare-asym-line declares border-left: none AFTER any border-shorthand (cascade-safe)', () => {
  const block = ruleBlock(CSS, /\.compare-asym-line(?!-)/);
  assert(block, '.compare-asym-line rule missing');
  // Tightened (Kent Beck must-fix #3): require explicit border-left: none,
  // not just absence of a border-left: declaration. If a border: shorthand
  // exists, border-left: none must come AFTER it so the cascade kills the
  // left edge.
  const blIdx = block.search(/border-left\s*:\s*none\b/);
  assert(blIdx >= 0,
    '.compare-asym-line must explicitly declare `border-left: none` (a future `border:` shorthand could re-add the bar)');
  const shortIdx = block.search(/(?:^|[;{\s])border\s*:\s*(?!none\b)[^;]*\b\d/);
  if (shortIdx >= 0) {
    assert(blIdx > shortIdx,
      '.compare-asym-line `border-left: none` must come AFTER `border:` shorthand or shorthand wins');
  }
});

// ── 7) decorative type-summary border-left explicitly removed ─────────
test('.compare-type-summary declares border-left: none AFTER any border-shorthand (cascade-safe)', () => {
  const block = ruleBlock(CSS, /\.compare-type-summary(?!-)/);
  assert(block, '.compare-type-summary rule missing');
  const blIdx = block.search(/border-left\s*:\s*none\b/);
  assert(blIdx >= 0,
    '.compare-type-summary must explicitly declare `border-left: none`');
  const shortIdx = block.search(/(?:^|[;{\s])border\s*:\s*(?!none\b)[^;]*\b\d/);
  if (shortIdx >= 0) {
    assert(blIdx > shortIdx,
      '.compare-type-summary `border-left: none` must come AFTER `border:` shorthand');
  }
});

// ── 8) controls collapse — assert the actual DOM call, not comment text ──
test('compare.js makes the actual classList.toggle("is-collapsed", ready) call AND CSS keys on it', () => {
  // Tightened (Kent Beck must-fix #1, Adversarial #6, #8): assert the
  // behavioral call, not just a string match against comments. The PR
  // also dropped the redundant data-collapsed setAttribute path, so any
  // re-introduction is a regression.
  const callRe = /classList\.toggle\(\s*['"]is-collapsed['"]\s*,/;
  assert(callRe.test(COMPARE_JS),
    'compare.js must call classList.toggle("is-collapsed", <bool>) on #compareControls');
  assert(!/setAttribute\(\s*['"]data-collapsed['"]/.test(COMPARE_JS),
    'compare.js must NOT also setAttribute("data-collapsed", ...) — pick one source of truth (the class)');
  // CSS rule keying on the class must exist (without a stale [data-collapsed] selector).
  assert(/\.compare-controls\.is-collapsed/.test(CSS),
    'style.css must define a rule on .compare-controls.is-collapsed');
});

// ════════════════════════════════════════════════════════════════════
// #1646 — Mobile follow-ups (Tufte review).
// At ≤768px the page sits above a fixed bottom-nav (56px + safe-area)
// reserved via --bottom-nav-reserve. The headline diff bar has segments
// that go invisible at narrow widths when their share is ~2%, and the
// asymmetric-reach sentences wrap mid-phrase. These are behavioral
// guards so the polish doesn't quietly regress.
// ════════════════════════════════════════════════════════════════════

// helper: pull the body of the FIRST @media (max-width: <=768px) block
function mobileBlock(css) {
  const re = /@media[^{]*\(max-width:\s*(640|768)px\)\s*\{([\s\S]*?)\n\}\s*\n/g;
  let combined = '';
  let m;
  while ((m = re.exec(css))) combined += m[2] + '\n';
  return combined;
}
const MOBILE_CSS = mobileBlock(CSS);

// ── 9) compare-page reserves room for the bottom-nav at mobile ────────
test('mobile .compare-page reserves padding-bottom for the bottom-nav', () => {
  // Either the rule lives inside a mobile @media block, OR an
  // unconditional rule references var(--bottom-nav-reserve).
  const inMobile = /\.compare-page[^{]*\{[^}]*padding-bottom[^;]*--bottom-nav-reserve/m.test(MOBILE_CSS);
  const unconditional = /\.compare-page[^{]*\{[^}]*padding-bottom[^;]*--bottom-nav-reserve/m.test(CSS);
  assert(inMobile || unconditional,
    '.compare-page must add padding-bottom tied to var(--bottom-nav-reserve) so the last row is not eaten by the bottom-nav');
});

// ── 10) diff-bar segments stay visible at narrow widths ───────────────
test('.compare-bar-seg has a min-width so non-zero segments stay visible', () => {
  // We accept either a global min-width on .compare-bar-seg, or a
  // mobile-scoped one. Visibility is what matters.
  const reAny = /\.compare-bar-seg[^{}]*\{[^}]*min-width\s*:/m;
  assert(reAny.test(CSS),
    '.compare-bar-seg must declare min-width so a 2% segment is still readable on mobile');
  // #1646 round-2 review: the floor must be a *presence* floor only,
  // not a magnitude-flattening floor. 6px collapses 1% and 5% slices
  // to identical width, lying about magnitude. Cap at 2px — visible
  // pip without falsely equating sizes.
  const segBlockRe = /\.compare-bar-seg[^{}]*\{[^}]*min-width\s*:\s*(\d+)px/m;
  const m = CSS.match(segBlockRe);
  assert(m, '.compare-bar-seg min-width must be expressed in px so we can bound it');
  const px = parseInt(m[1], 10);
  assert(px <= 2,
    `.compare-bar-seg min-width must be <= 2px (presence floor only, not magnitude-equivalent); got ${px}px`);
});

// ── 11) asym sentence reflows cleanly on mobile ───────────────────────
test('mobile .compare-asym-line uses text-wrap balance/pretty (or overflow-wrap) to avoid mid-phrase breaks', () => {
  // Look inside any mobile @media block for the rule. We accept
  // text-wrap: balance | pretty, or word-break/overflow-wrap as
  // alternative reflow strategies.
  const ok = /\.compare-asym-line[^{}]*\{[^}]*(text-wrap\s*:\s*(balance|pretty)|overflow-wrap\s*:\s*anywhere|word-break\s*:\s*break-word)/m.test(MOBILE_CSS);
  assert(ok,
    'mobile .compare-asym-line needs a wrap rule (text-wrap: balance/pretty or overflow-wrap) so the sentence does not break mid-phrase');
});

// ── 12) tabs row stays usable on narrow widths ────────────────────────
test('mobile .compare-tabs .tab-btn shrinks/wraps so all four tabs fit', () => {
  // We need EITHER tabs to allow wrap (flex-wrap: wrap on .compare-tabs
  // — already present at desktop) AND a per-button rule that lets the
  // long "Only <name> (NNN)" labels truncate or shrink.
  // Accept: (a) a mobile rule on .tab-btn with min-width:0 + text-overflow,
  //         (b) a flex: 1 1 0 / flex-shrink, or
  //         (c) overflow-wrap on the tab.
  const ok = /\.compare-tabs[^{}]*\.tab-btn[^{}]*\{[^}]*(min-width\s*:\s*0|flex\s*:\s*1|flex-shrink|overflow-wrap)/m.test(MOBILE_CSS) ||
             /\.compare-tabs\s+\.tab-btn[^{}]*\{[^}]*(min-width\s*:\s*0|flex\s*:\s*1|flex-shrink|overflow-wrap)/m.test(MOBILE_CSS);
  assert(ok,
    'mobile .compare-tabs .tab-btn must declare min-width:0 / flex / overflow-wrap so all four tab labels fit at 375px');
});

// ── 13) compare.js auto-run guards are consistent (#1646 round-2) ─────
// Round-2 review found two run-comparison call sites with *different*
// guards: loadObservers used `selA && selB` (no inequality check) while
// onChange used `selA && selB && selA !== selB`. URL-prepopulated
// ?a=X&b=X (same observer in both slots) would launch a comparison via
// the loadObservers path and not the change path. Both call sites must
// gate on the same predicate.
test('compare.js — both runComparison() guards include selA !== selB', () => {
  // Find every line that calls runComparison() and check the guard
  // immediately preceding it on the same line is identical.
  // Only inspect guards that reference the selA/selB / ready predicate
  // (not the route-filter re-run path which conditions on comparisonResult).
  const guardLines = COMPARE_JS
    .split('\n')
    .filter(l => /\brunComparison\s*\(/.test(l) && /\bif\s*\(/.test(l))
    .filter(l => /selA|selB|[Rr]eady\b|canCompare\b/.test(l));
  assert(guardLines.length >= 2,
    `expected at least 2 selA/selB-guarded runComparison() call sites, found ${guardLines.length}`);
  // Every such line must check selA !== selB (or the equivalent
  // ready predicate factored into a helper). The forbidden pattern is
  // a guard that ONLY checks `selA && selB` without the inequality.
  guardLines.forEach((line, i) => {
    const trimmed = line.trim();
    const hasInequality = /selA\s*!==\s*selB|selB\s*!==\s*selA|[Rr]eady\b|canCompare\b/.test(trimmed);
    assert(hasInequality,
      `runComparison() guard #${i + 1} missing selA !== selB / ready predicate: ${trimmed}`);
  });
});


process.exit(failed === 0 ? 0 : 1);
