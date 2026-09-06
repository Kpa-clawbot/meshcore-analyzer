/**
 * test-a11y-axe-1668.js — Milestones 5 + 6 of #1668
 *
 * axe-core CI gate. Loads every major CoreScope route in dark + light theme,
 * injects axe-core, runs the configured ruleset, and asserts zero
 * violations (modulo `tests/a11y-allowlist.yaml`).
 *
 * Scope:
 *   - M5: color-contrast on desktop dark+light at 1200x900.
 *   - M6: expanded ruleset (image-alt, label, aria-required-attr,
 *     aria-valid-attr, aria-valid-attr-value, landmark-one-main, region,
 *     button-name, link-name, document-title, html-has-lang, duplicate-id)
 *     applied across BOTH viewports, PLUS color-contrast at 375x812 mobile.
 *
 *   Themes:    dark + light
 *   Viewports: desktop 1200x900, mobile 375x812 (M6 adds mobile)
 *
 * Allowlist (`tests/a11y-allowlist.yaml`):
 *   Operator-flagged false-positives. Each entry MUST cite an issue # AND
 *   an expires_at date. Expired entries are refused (warning logged, full
 *   failure). Missing fields => refused.
 *
 * Usage:
 *   BASE_URL=http://localhost:13581 node test-a11y-axe-1668.js
 *
 * Env:
 *   BASE_URL          required (server to test against)
 *   CHROMIUM_PATH     optional (else playwright's bundled chromium)
 *   AXE_ROUTES_ONLY   optional comma list of routes to limit (debug)
 *   AXE_SCREENSHOT_DIR  where to write screenshots on failure (default /tmp/axe-1668)
 */

'use strict';

const fs = require('fs');
const path = require('path');
// Lazy-require playwright + @axe-core/playwright inside main() so the
// parser helpers below are unit-testable on hosts without those modules
// (e.g. CI lint passes, or the sanity self-test below).

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const ROUTES_FILTER = (process.env.AXE_ROUTES_ONLY || '').split(',').filter(Boolean);
const SHOT_DIR = process.env.AXE_SCREENSHOT_DIR || '/tmp/axe-1668';
const ALLOWLIST_PATH = path.join(__dirname, 'tests', 'a11y-allowlist.yaml');

// Routes: M1 audit baseline (already proven coverage).
// Hash routes — CoreScope is a SPA, server returns the same shell for any path.
//
// HARD INVARIANT: every entry below MUST resolve to a `registerPage()` page
// in `public/*.js` (or — for `/analytics?tab=X` — to a real `case 'X':` arm
// of the tab dispatch in `public/analytics.js`). The selftest enforces this
// via REGISTERED_PAGES / REGISTERED_ANALYTICS_TABS reciprocity so a removed
// route forces a build break instead of the gate silently skipping coverage.
const ROUTES = [
  '/',                              // SPA default → packets
  '/packets',
  '/nodes',
  '/channels',
  '/live',
  '/map',
  '/observers',
  '/compare',
  '/analytics?tab=overview',
  '/analytics?tab=rf',
  '/analytics?tab=topology',
  '/analytics?tab=channels',
  '/analytics?tab=hashsizes',
  '/analytics?tab=collisions',
  '/analytics?tab=roles',
  // #1706: remaining analytics tabs — every `data-tab=` button in
  // public/analytics.js must be gated. test-a11y-axe-routes-coverage.js
  // enforces this; do not remove entries without dropping the tab too.
  '/analytics?tab=subpaths',
  '/analytics?tab=nodes',
  '/analytics?tab=distance',
  '/analytics?tab=neighbor-graph',
  '/analytics?tab=rf-health',
  '/analytics?tab=clock-health',
  '/analytics?tab=scopes',
  '/analytics?tab=prefix-tool',
  '/analytics?tab=my-repeaters',
  '/analytics?tab=repeater-metrics',
  '/audio-lab',
];

// Source-of-truth for ROUTES reciprocity. Keep these in sync with the
// `registerPage(...)` calls under `public/` and the `case 'X':` arms in
// `public/analytics.js`. The selftest greps the source to confirm.
const REGISTERED_PAGES = [
  'home', 'packets', 'packet-detail', 'nodes', 'node-analytics', 'node-reach',
  'channels', 'live', 'map', 'observers', 'observer-detail', 'compare',
  'analytics', 'audio-lab', 'perf', 'traces', 'path-inspector', 'tools-landing',
];
const REGISTERED_ANALYTICS_TABS = [
  'overview', 'rf', 'topology', 'channels', 'hashsizes', 'collisions',
  'subpaths', 'nodes', 'distance', 'neighbor-graph', 'rf-health',
  'clock-health', 'roles', 'prefix-tool', 'scopes', 'my-repeaters',
  'repeater-metrics',
];

const THEMES = ['dark', 'light'];

// M6: ruleset per viewport. Both viewports share the expanded ruleset;
// color-contrast also runs on both (M5 baseline desktop + M6 mobile gate).
// All rules in these arrays MUST be 0 violations against the CI fixture
// (no allowlist seeding — same hard policy as M5).
const RULES_DESKTOP = [
  'color-contrast',
  'image-alt',
  'label',
  'aria-required-attr',
  'aria-valid-attr',
  'aria-valid-attr-value',
  'landmark-one-main',
  'region',
  'button-name',
  'link-name',
  'document-title',
  'html-has-lang',
  'duplicate-id',
];
const RULES_MOBILE = RULES_DESKTOP.slice(); // identical at M6; split arrays let
                                            // a future PR diverge cleanly.
const VIEWPORTS = [
  { name: 'desktop', w: 1200, h: 900, rules: RULES_DESKTOP },
  { name: 'mobile',  w: 375,  h: 812, rules: RULES_MOBILE  },
];

// ---- tiny YAML loader (flow `[]` or block list of `key: value` maps) -------
//
// Stays dependency-free — we only need to parse our own narrow schema.
// Supports:
//   - empty list ([])
//   - block list of inline `- key: value` items continued with `  key: value` lines
//   - top-level scalar keys (e.g. `strict_unused: true`) before/after the list
//   - quoted strings ('...' or "...")
//   - integers, booleans, and YYYY-MM-DD dates
//
// Return shape:
//   - legacy: an Array of entry objects (back-compat for callers expecting
//     parseAllowlistYaml(...) to be array-iterable)
//   - new:    when top-level keys are present, returns
//     Array.prototype-extended array PLUS .topLevel and .entries properties.
function parseAllowlistYaml(src) {
  // strip BOM, comments, normalize line endings
  const lines = src.replace(/^\uFEFF/, '').split(/\r?\n/)
    .map(l => l.replace(/(^|\s)#.*$/, '').replace(/\s+$/, ''))
    .filter(l => l.trim().length > 0);

  if (lines.length === 0) {
    const out = [];
    out.topLevel = {};
    out.entries = out;
    return out;
  }
  if (lines.length === 1 && lines[0].trim() === '[]') {
    const out = [];
    out.topLevel = {};
    out.entries = out;
    return out;
  }

  const entries = [];
  const topLevel = {};
  let current = null;
  for (const raw of lines) {
    const m = raw.match(/^(\s*)(-?)\s*([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$/);
    if (!m) {
      throw new Error(`a11y-allowlist.yaml: cannot parse line: ${raw}`);
    }
    const [, indent, dash, key, valRaw] = m;
    if (dash === '-') {
      if (current) entries.push(current);
      current = {};
    }
    if (!current && dash !== '-') {
      // Top-level key (e.g. `strict_unused: true`) — before any list item.
      if (indent.length === 0) {
        topLevel[key] = coerce(valRaw.trim());
        continue;
      }
      throw new Error(`a11y-allowlist.yaml: indented key "${key}" outside list item`);
    }
    if (!current) throw new Error(`a11y-allowlist.yaml: key "${key}" outside list item`);
    current[key] = coerce(valRaw.trim());
  }
  if (current) entries.push(current);
  entries.topLevel = topLevel;
  entries.entries = entries;
  return entries;
}

function coerce(v) {
  if (v === '' || v === '~' || v === 'null') return null;
  if (v === 'true') return true;
  if (v === 'false') return false;
  if (/^-?\d+$/.test(v)) return parseInt(v, 10);
  if (/^'.*'$/.test(v) || /^".*"$/.test(v)) return v.slice(1, -1);
  return v;
}

// #1706 finding 4: top-level YAML config (e.g. `strict_unused: true`) parsed
// alongside entries. Captured from the most-recent loadAllowlist() / parse call
// so callers can read it via getAllowlistConfig() without re-parsing.
let _lastTopLevel = {};

function loadAllowlist() {
  if (!fs.existsSync(ALLOWLIST_PATH)) {
    _lastTopLevel = {};
    return [];
  }
  const raw = fs.readFileSync(ALLOWLIST_PATH, 'utf8');
  // parseAllowlistYaml THROWS on malformed input (don't silently swallow → []).
  // A swallowed parse error would let a typo-mangled allowlist suppress nothing
  // and ship a green gate that no longer reflects operator intent. Loud failure.
  const parsed = parseAllowlistYaml(raw);
  _lastTopLevel = parsed.topLevel || {};
  const today = new Date().toISOString().slice(0, 10);
  return filterAllowlist(parsed.entries || parsed, today);
}

// #1706 finding 4: read parsed top-level config. Env override
// A11Y_STRICT_UNUSED=1 promotes stale-entry WARN → FAIL regardless of YAML.
function getAllowlistConfig() {
  const cfg = { strict_unused: false, ..._lastTopLevel };
  if (process.env.A11Y_STRICT_UNUSED === '1') cfg.strict_unused = true;
  return cfg;
}

// Pure function: filter a parsed allowlist against `today` (YYYY-MM-DD string).
// THROWS on:
//   - any entry missing a required field (route/rule/issue/expires_at AND
//     either `selector` or `selector_pattern` + `count_max`)
//   - any entry whose expires_at <= today (today inclusive — boundary fails)
//   - any selector_pattern that fails to compile as a regex (precompiled here
//     onto the entry for runtime hot-path match)
// Returning a soft-filtered subset would let stale suppressions persist; this
// matches PR policy that expired entries are refused as a HARD failure.
function filterAllowlist(entries, today) {
  if (!Array.isArray(entries)) throw new Error('filterAllowlist: entries must be an array');
  if (typeof today !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(today)) {
    throw new Error(`filterAllowlist: today must be YYYY-MM-DD, got ${today}`);
  }
  const valid = [];
  for (const e of entries) {
    if (!e || !e.route || !e.rule || !e.issue || !e.expires_at) {
      throw new Error(`a11y-allowlist.yaml: REFUSED (missing required field): ${JSON.stringify(e)}`);
    }
    // #1706 finding 1: pattern form requires `count_max` so unbounded growth
    // re-trips the gate. Exactly one of selector / selector_pattern.
    const hasSelector = typeof e.selector === 'string' && e.selector.length > 0;
    const hasPattern  = typeof e.selector_pattern === 'string' && e.selector_pattern.length > 0;
    if (!hasSelector && !hasPattern) {
      throw new Error(`a11y-allowlist.yaml: REFUSED (need selector OR selector_pattern): ${JSON.stringify(e)}`);
    }
    if (hasSelector && hasPattern) {
      throw new Error(`a11y-allowlist.yaml: REFUSED (selector and selector_pattern are mutually exclusive): ${JSON.stringify(e)}`);
    }
    if (hasPattern) {
      if (typeof e.count_max !== 'number' || e.count_max <= 0) {
        throw new Error(`a11y-allowlist.yaml: REFUSED (selector_pattern requires count_max > 0): ${JSON.stringify(e)}`);
      }
      try {
        e._patternRe = new RegExp(e.selector_pattern);
      } catch (re) {
        throw new Error(`a11y-allowlist.yaml: REFUSED (selector_pattern invalid regex /${e.selector_pattern}/: ${re.message})`);
      }
    }
    // #1706 finding 6: expires_at hard-gates CI. boundary == today is REFUSED.
    // (Pre-existing in the loader, but now explicitly cited so future edits
    // know it's load-bearing for the M4 "sed forward" pressure mitigation.)
    if (String(e.expires_at) <= today) {
      throw new Error(`a11y-allowlist.yaml: REFUSED (expired ${e.expires_at} <= today ${today}, issue #${e.issue}): ${e.route} ${e.selector || e.selector_pattern}`);
    }
    // #1706 finding 4: zero out the per-run match counter (mutated by
    // violationAllowed during the CI sweep, read by reportStaleAllowlist).
    e._matchCount = 0;
    valid.push(e);
  }
  return valid;
}

// #1706 finding 1+4: match a violation node against the allowlist.
// Returns { allowed, overflow } — when an entry uses selector_pattern and
// its match count exceeds count_max, overflow=true so the caller can FAIL
// the gate (this is the "unbounded growth re-trips CI" guarantee).
function matchViolation(route, rule, node, allowlist) {
  const targets = (node.target || []).flat ? node.target.flat() : [].concat(...(node.target || []));
  for (const entry of allowlist) {
    if (entry.route !== route) continue;
    if (entry.rule !== rule) continue;
    for (const t of targets) {
      if (typeof t !== 'string') continue;
      let hit = false;
      if (entry.selector && t === entry.selector) hit = true;
      else if (entry._patternRe && entry._patternRe.test(t)) hit = true;
      if (hit) {
        entry._matchCount = (entry._matchCount || 0) + 1;
        const overflow = !!(entry.count_max && entry._matchCount > entry.count_max);
        return { allowed: true, overflow, entry };
      }
    }
  }
  return { allowed: false, overflow: false, entry: null };
}

// Back-compat thin wrapper — older selftest blocks and external readers
// (e.g. ad-hoc CLI debugging) expect the boolean signature.
function violationAllowed(route, rule, node, allowlist) {
  return matchViolation(route, rule, node, allowlist).allowed;
}

// #1706 finding 4: emit a STALE ALLOWLIST report listing entries that
// were never matched during a CI run. WARN by default; if cfg.strict_unused
// is true, return { fail: true } so the caller can hard-fail the gate.
function reportStaleAllowlist(allowlist, cfg) {
  const stale = allowlist.filter(e => !e._matchCount);
  if (stale.length === 0) return { stale: [], fail: false };
  const verdict = cfg && cfg.strict_unused ? 'FAIL' : 'WARN';
  console.log(`\n[${verdict}] STALE ALLOWLIST: ${stale.length} entr${stale.length === 1 ? 'y' : 'ies'} matched nothing this run`);
  for (const e of stale) {
    const desc = e.selector_pattern
      ? `pattern=${e.selector_pattern} count_max=${e.count_max}`
      : `selector=${e.selector}`;
    console.log(`  - route=${e.route} rule=${e.rule} ${desc} issue=#${e.issue} expires=${e.expires_at}`);
  }
  return { stale, fail: !!(cfg && cfg.strict_unused) };
}

// ---------------------------------------------------------------------------

async function setTheme(page, theme) {
  // Seed localStorage BEFORE the SPA boots so the theme is correct on first paint.
  await page.addInitScript((t) => {
    try {
      localStorage.setItem('meshcore-theme', t);
      // Live page collapses controls by default; keep them visible
      // (matches test-e2e-playwright.js convention).
      localStorage.setItem('live-controls-expanded', 'true');
      // Default time window wide enough to render content.
      localStorage.setItem('meshcore-time-window', '525600');
    } catch (_) { /* ignore */ }
    // Set the attribute pre-paint to avoid a transient mismatch.
    try { document.documentElement.setAttribute('data-theme', t); } catch (_) {}
  }, theme);
}

// #1706 finding 2: extract the expected analytics tab from a route string,
// or null for non-analytics routes. Pure helper so selftest can exercise it
// without a browser.
function analyticsTabOf(route) {
  if (!route.startsWith('/analytics')) return null;
  const q = route.split('?')[1];
  if (!q) return null;
  const m = q.match(/(?:^|&)tab=([^&]+)/);
  return m ? decodeURIComponent(m[1]) : null;
}

// #1706 finding 2: assert the SPA actually rendered the requested route.
// Before this guard, a failed mount (component 404, JS error, hash typo)
// produced an empty container and axe returned 0 violations → false-clean
// CI green. We now throw a loud error so a non-mounted tab fails the route
// instead of silently passing.
async function assertRouteMounted(page, route) {
  const expectedTab = analyticsTabOf(route);
  const mounted = await page.evaluate((expectedTab) => {
    const hash = window.location.hash || '';
    const container = document.querySelector('#appRoot, main, #app, [role="main"]')
      || document.body;
    const hasContent = !!container && (container.children.length > 0
      || (container.innerText || '').trim().length > 0);
    let tabActive = true;
    if (expectedTab) {
      const btn = document.querySelector(`.tab-btn[data-tab="${expectedTab}"]`);
      tabActive = !!btn && btn.classList.contains('active');
    }
    return { hash, hasContent, tabActive,
             containerChildren: container ? container.children.length : 0 };
  }, expectedTab);

  // Hash must reference the requested route (allow trailing query/hash diffs
  // that the SPA may append, e.g. &section=...).
  const hashHasRoute = mounted.hash.includes(route.split('?')[0]);
  if (!hashHasRoute) {
    throw new Error(`tab=${route} did not mount; URL hash "${mounted.hash}" missing "${route.split('?')[0]}" — axe scan would have been false-clean`);
  }
  if (!mounted.hasContent) {
    throw new Error(`tab=${route} did not mount; main container empty (children=${mounted.containerChildren}) — axe scan would have been false-clean`);
  }
  if (expectedTab && !mounted.tabActive) {
    throw new Error(`tab=${route} did not mount; analytics tab "${expectedTab}" did not become active — axe scan would have been false-clean`);
  }
}

async function runRoute(page, route, theme, rules, AxeBuilder) {
  const url = `${BASE}/#${route}`;
  await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 30000 });
  // Give the SPA a moment to render. We deliberately do NOT
  // wait for network idle because /live + /map keep sockets open.
  await page.waitForTimeout(1500);

  // Quick sanity: confirm body is visible and theme attr matches
  const themeAttr = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
  if (themeAttr !== theme) {
    // Try toggling explicitly if the SPA reset it (shouldn't happen, but be safe)
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme);
    await page.waitForTimeout(200);
  }

  // #1706 finding 2: mount assertion BEFORE axe to prevent false-clean.
  await assertRouteMounted(page, route);

  const axe = new AxeBuilder({ page }).withRules(rules);
  const result = await axe.analyze();
  return result;
}

async function main() {
  const { chromium } = require('playwright');
  const { AxeBuilder } = require('@axe-core/playwright');
  if (!fs.existsSync(SHOT_DIR)) fs.mkdirSync(SHOT_DIR, { recursive: true });
  const allowlist = loadAllowlist();
  const cfg = getAllowlistConfig();
  console.log(`a11y-axe-1668: BASE=${BASE} allowlist=${allowlist.length} entries strict_unused=${cfg.strict_unused}`);

  const routesToRun = ROUTES_FILTER.length ? ROUTES.filter(r => ROUTES_FILTER.includes(r)) : ROUTES;
  console.log(`a11y-axe-1668: routes=${routesToRun.length} themes=${THEMES.length} viewports=${VIEWPORTS.length} cells=${routesToRun.length * THEMES.length * VIEWPORTS.length}`);

  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  const summary = []; // { vp, route, theme, raw, suppressed, net }
  let totalNet = 0;
  // Per-viewport tallies for the summary footer.
  const vpTotals = {};
  for (const vp of VIEWPORTS) vpTotals[vp.name] = { raw: 0, suppressed: 0, net: 0 };

  try {
    for (const vp of VIEWPORTS) {
      console.log(`\n--- viewport ${vp.name} ${vp.w}x${vp.h} rules=${vp.rules.length} ---`);
      for (const theme of THEMES) {
        // One context per (viewport, theme) — keeps init-script localStorage stable.
        const context = await browser.newContext({ viewport: { width: vp.w, height: vp.h } });
        await context.addInitScript((t) => {
          try {
            localStorage.setItem('meshcore-theme', t);
            localStorage.setItem('live-controls-expanded', 'true');
            localStorage.setItem('meshcore-time-window', '525600');
            document.documentElement.setAttribute('data-theme', t);
          } catch (_) {}
        }, theme);

        for (const route of routesToRun) {
          const page = await context.newPage();
          let raw = 0, suppressed = 0, net = 0;
          const violationsDetail = [];
          try {
            const result = await runRoute(page, route, theme, vp.rules, AxeBuilder);
            for (const v of result.violations) {
              if (!vp.rules.includes(v.id)) continue; // narrow safeguard
              for (const node of v.nodes) {
                raw++;
                const m = matchViolation(route, v.id, node, allowlist);
                if (m.allowed && !m.overflow) {
                  suppressed++;
                } else if (m.allowed && m.overflow) {
                  // #1706 finding 1: count_max breached → unbounded growth.
                  // Fail the gate so the allowlist re-trips CI for a human review.
                  net++;
                  violationsDetail.push({
                    rule: v.id,
                    selector: node.target,
                    overflow: `count_max=${m.entry.count_max} exceeded (matched=${m.entry._matchCount}) for pattern=${m.entry.selector_pattern} issue=#${m.entry.issue}`,
                  });
                } else {
                  net++;
                  violationsDetail.push({
                    rule: v.id,
                    selector: node.target,
                    html: node.html && node.html.slice(0, 200),
                    message: node.failureSummary,
                  });
                }
              }
            }
          } catch (err) {
            // Probe errors should NOT silently pass — treat as a hard failure
            // so route regressions (server 500, hash route 404, JS crash) surface.
            net = 1;
            violationsDetail.push({ probeError: err.message });
          }

          const cell = { vp: vp.name, route, theme, raw, suppressed, net };
          summary.push(cell);
          totalNet += net;
          vpTotals[vp.name].raw += raw;
          vpTotals[vp.name].suppressed += suppressed;
          vpTotals[vp.name].net += net;
          const verdict = net === 0 ? '✅' : '❌';
          console.log(`  ${verdict} ${vp.name.padEnd(7)} ${theme.padEnd(5)} ${route.padEnd(34)} raw=${raw} suppressed=${suppressed} net=${net}`);
          if (net > 0) {
            for (const d of violationsDetail) {
              console.log(`     - ${JSON.stringify(d).slice(0, 500)}`);
            }
            const safe = `${vp.name}_${theme}_${route.replace(/[^a-z0-9]+/gi, '_')}`;
            const shot = path.join(SHOT_DIR, `${safe}.png`);
            try { await page.screenshot({ path: shot, fullPage: false }); } catch (_) {}
          }
          await page.close();
        }
        await context.close();
      }
    }
  } finally {
    await browser.close();
  }

  console.log('');
  console.log(`a11y-axe-1668: SUMMARY net=${totalNet} cells=${summary.length}`);
  for (const vp of VIEWPORTS) {
    const t = vpTotals[vp.name];
    console.log(`  viewport ${vp.name}: raw=${t.raw} suppressed=${t.suppressed} net=${t.net} (${vp.rules.length} rules)`);
  }
  for (const c of summary) {
    if (c.net > 0) {
      console.log(`  FAIL ${c.vp} ${c.theme} ${c.route} net=${c.net}`);
    }
  }
  // #1706 finding 4: stale-allowlist audit (WARN by default, FAIL when
  // strict_unused=true). Caller (CI) can flip the env flag during cleanup
  // sweeps without re-editing the YAML.
  const staleReport = reportStaleAllowlist(allowlist, cfg);
  if (totalNet > 0) {
    console.error(`\nFAIL: ${totalNet} a11y violation(s) above allowlist`);
    process.exit(1);
  }
  if (staleReport.fail) {
    console.error(`\nFAIL: ${staleReport.stale.length} stale allowlist entr${staleReport.stale.length === 1 ? 'y' : 'ies'} (strict_unused=true)`);
    process.exit(1);
  }
  console.log(`\nPASS: zero violations across ${summary.length} cells (${VIEWPORTS.length} viewports × ${THEMES.length} themes × ${routesToRun.length} routes)`);
}

if (require.main === module) {
  main().catch((err) => {
    console.error('a11y-axe-1668 fatal:', err && err.stack || err);
    process.exit(2);
  });
}

// Allow consumers (e.g. a CI unit-test step) to import the parser helpers
// without launching a browser.
module.exports = {
  parseAllowlistYaml,
  loadAllowlist,
  getAllowlistConfig,
  filterAllowlist,
  violationAllowed,
  matchViolation,
  reportStaleAllowlist,
  analyticsTabOf,
  ROUTES,
  THEMES,
  VIEWPORTS,
  RULES_DESKTOP,
  RULES_MOBILE,
  REGISTERED_PAGES,
  REGISTERED_ANALYTICS_TABS,
};
