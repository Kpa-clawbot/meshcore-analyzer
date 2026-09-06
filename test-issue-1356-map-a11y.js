/**
 * #1356 — WCAG 2.2 AA accessibility for map cluster bubbles, role pills,
 * and multi-byte hash labels.
 *
 * Locked design = Tufte's structural framing (drop color as primary signal,
 * use shape / glyph / border-style as carriers) WITH the audit's "Minimal
 * patch to Tufte's proposal to reach AA" applied.
 *
 * Design sources:
 *   - https://github.com/Kpa-clawbot/CoreScope/issues/1356#issuecomment-4535244400
 *   - https://github.com/Kpa-clawbot/CoreScope/issues/1356#issuecomment-4535849354
 *
 * Pure-string assertions (mirrors test-issue-1293-marker-shapes.js pattern)
 * so this runs in the JS-unit-tests CI step without a browser.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const mapSrc   = fs.readFileSync(path.join(__dirname, 'public', 'map.js'),   'utf8');
const cssSrc   = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');

// ── Load map.js in a DOM-less sandbox so the label assertions can call the
// real makeRepeaterLabelIcon and look at what it renders.
//
// The V3 label checks used to be source greps. One of them bounded the distance
// between two identifiers in map.js to 200 characters, which made it fail on any
// statement inserted between them even when the rendering was unchanged -- and,
// worse, let a comment that happened to mention both identifiers satisfy it, so
// it could report green while the ordering was in fact wrong. A grep cannot tell
// code from prose about code.
//
// Same sandbox shape as test-map-clustering.js; no browser, so this still runs
// in the JS-unit-tests CI step.
function makeLeafletShim() {
  const chain = () => { const o = { addTo: () => o, on: () => o, setStyle: () => o, remove: () => o,
                                    bindTooltip: () => o, setLatLng: () => o, extend: () => o }; return o; };
  const control = () => chain();
  control.layers = () => chain();
  return {
    divIcon: (opts) => opts,               // the icon under test: keep the options verbatim
    point: (x, y) => ({ x: x, y: y }),
    circleMarker: chain, polyline: chain, polygon: chain, marker: chain,
    tileLayer: chain, layerGroup: chain, featureGroup: chain, control: control,
    latLngBounds: chain, latLng: (a, b) => ({ lat: a, lng: b }),
    map: () => Object.assign(chain(), { setView: () => chain(), getZoom: () => 11,
      getCenter: () => ({ lat: 0, lng: 0 }), getBounds: () => ({ contains: () => true }),
      fitBounds: () => chain(), invalidateSize: () => {}, hasLayer: () => false,
      addLayer: () => chain(), removeLayer: () => {} }),
    markerClusterGroup: chain, MarkerClusterGroup: function () {},
    DomUtil: { create: () => ({}) }, Icon: { Default: { prototype: {} } },
  };
}

function loadMapInternals() {
  const vm = require('vm');
  const ctx = {
    window: {},
    document: {
      addEventListener() {}, getElementById() { return null; },
      querySelector() { return null; }, querySelectorAll() { return []; },
      createElement() { return { id: '', textContent: '', innerHTML: '', style: {}, dataset: {},
        appendChild() {}, addEventListener() {}, setAttribute() {},
        classList: { add() {}, remove() {}, toggle() {} } }; },
      head: { appendChild() {} }, body: { appendChild() {} },
      documentElement: { getAttribute: () => null, dataset: {} },
    },
    console, Date, Math, Array, Object, String, Number, JSON, RegExp, Error, TypeError,
    parseInt, parseFloat, isFinite, isNaN, Map, Set, Promise, URLSearchParams,
    setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
    registerPage: () => {}, esc: (x) => x, onWS: () => {}, offWS: () => {},
    localStorage: (() => { const st = {}; return { getItem: k => (k in st ? st[k] : null),
      setItem: (k, v) => { st[k] = String(v); }, removeItem: k => { delete st[k]; } }; })(),
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
    addEventListener() {}, dispatchEvent() {},
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    matchMedia: () => ({ matches: false, addEventListener() {} }),
    navigator: {}, location: { hash: '', protocol: 'https:', host: 'localhost' },
    L: makeLeafletShim(),
  };
  ctx.window.L = ctx.L;
  vm.createContext(ctx);
  for (const f of ['public/roles.js', 'public/map.js']) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, f), 'utf8'), ctx, { filename: f });
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  }
  return ctx.window.__meshcoreMapInternals;
}

// Deliberately NOT wrapped in try/catch that warns and continues: a sandbox that
// fails to load must fail the suite, not quietly drop every assertion below it.
const mapInternals = loadMapInternals();
const NODE = { public_key: '3e7a1b9c'.repeat(8), hash_size: 2 };
const THIN = '\u2009';

console.log('\n=== #1356 V1: cluster bubble — neutral fill, border-style ramp, ARIA ===');

// V1.a — CSS must define a neutral cluster fill constant (not the bucket color).
assert(/--mc-cluster-fill\s*:/.test(cssSrc),
  'style.css declares --mc-cluster-fill CSS variable');

// V1.b — Per-bucket background MUST NOT be the old --info/--warning/--accent system colors.
// (Those system vars are reserved per AGENTS.md / issue scope.)
const clusterBlock = cssSrc.match(/\.mc-cluster\.mc-sm[\s\S]{0,400}\.mc-cluster\.mc-lg[^}]*\}/);
assert(clusterBlock && !/var\(--info|var\(--warning|var\(--accent/.test(clusterBlock[0]),
  'cluster sm/md/lg no longer use --info / --warning / --accent for fill');

// V1.c — Border-style ramp (solid → heavier → double) is the redundant carrier.
assert(/\.mc-cluster\.mc-lg[^}]*double/.test(cssSrc),
  'cluster lg uses "double" border-style as a non-color carrier');

// V1.d — Audit override: border color must be #666 (NOT white) plus a dark halo via box-shadow.
assert(/--mc-cluster-border\s*:\s*#666/i.test(cssSrc),
  '--mc-cluster-border is #666 (audit fix for SC 1.4.11 vs Carto-light)');
assert(/\.mc-cluster[^{]*\{[\s\S]*?box-shadow[^;]*rgba\(0\s*,\s*0\s*,\s*0/i.test(cssSrc),
  '.mc-cluster has a dark halo box-shadow (audit fix for border visibility)');

// V1.e — ARIA on the cluster div (rendered in makeClusterIcon).
assert(/role=["']img["']/.test(mapSrc) && /aria-label[^=]*=[^>]*nodes/.test(mapSrc),
  'makeClusterIcon emits role="img" + aria-label summarising count + role breakdown');
assert(/' nodes — '/.test(mapSrc) || /\d+ nodes — /.test(mapSrc) ||
       /total\s*\+\s*' nodes — '/.test(mapSrc),
  'cluster aria-label matches /\\d+ nodes — / pattern (summary + breakdown)');

console.log('\n=== #1356 V2: role pills — letter primary, Wong palette, dark text ===');

// V2.a — A ROLE_LETTERS map is defined for the 5 roles.
assert(/ROLE_LETTERS\s*=\s*\{[\s\S]*?repeater[\s\S]*?['"]R['"][\s\S]*?companion[\s\S]*?['"]C['"][\s\S]*?room[\s\S]*?['"]M['"][\s\S]*?sensor[\s\S]*?['"]S['"][\s\S]*?observer[\s\S]*?['"]O['"]/.test(mapSrc),
  'map.js defines ROLE_LETTERS with R/C/M/S/O for the five roles');

// V2.b — makeClusterIcon emits the letter (not just a count) inside the pill.
const pillEmitRe = /<span class="mc-pill[^>]*>[^<]*' \+\s*ROLE_LETTERS\[/;
assert(pillEmitRe.test(mapSrc) || /ROLE_LETTERS\[role\][\s\S]{0,200}mc-pill/.test(mapSrc) ||
       /mc-pill[\s\S]{0,200}ROLE_LETTERS\[role\]/.test(mapSrc),
  'pill HTML embeds ROLE_LETTERS[role] as the primary content');

// V2.c — Dark text on ALL five Wong-default pills (audit override of Tufte's
//   per-pill switch). #1407 generalized this to a per-role text-color CSS var
//   (--mc-role-X-text) so darker presets (achromat / trit) can pair white text
//   with darker bgs and still meet WCAG 1.4.3 AA. The Wong DEFAULT still uses
//   #1a1a1a — encoded as the fallback in `var(--mc-pill-text, #1a1a1a)` AND
//   on each `var(--mc-role-X-text, #1a1a1a)`, so any regression that drops the
//   per-role vars still renders dark text on Wong (no theming illusion).
assert(/\.mc-pill\b[^{]*\{[^}]*color\s*:\s*var\(\s*--mc-(?:pill|role-[a-z]+)-text\s*,\s*#1a1a1a\s*\)/i.test(cssSrc),
  '.mc-pill CSS rule sets color: var(--mc-...-text, #1a1a1a) — #1407 generalized #1356\'s authoritative dark default');
assert(/class="mc-pill[^"]*"[^>]*style="[^"]*color:(?:\s*#1a1a1a|'\s*\+\s*fg\b|\s*var\(--mc-role-[a-z]+-text)/i.test(mapSrc),
  '.mc-pill render-site emits inline color (#1a1a1a, "+ fg +", or var(--mc-role-X-text, #1a1a1a)) — defense-in-depth for divIcon (#1407)');

// V2.d — font-size ≥ 10px (audit bumped from 9px).
const pillFontMatch = cssSrc.match(/\.mc-pill\b[^{]*\{[^}]*font[^;]*;/);
assert(pillFontMatch && /1[0-9]px|0\.625rem|0\.6875rem|0\.75rem/.test(pillFontMatch[0]),
  '.mc-pill font-size is ≥ 10px (audit fix for SC 1.4.3 / 1.4.4)');

// V2.e — Wong palette declared as --mc-role-* constants.
['repeater','companion','room','sensor','observer'].forEach(function(r){
  assert(new RegExp('--mc-role-' + r + '\\s*:').test(cssSrc),
    '--mc-role-' + r + ' CSS variable declared');
});

// V2.f — per-pill aria-label "<N> <role>s".
assert(/aria-label="'\s*\+\s*n\s*\+\s*' '\s*\+\s*role/.test(mapSrc) ||
       /aria-label=("|')[\s\S]{0,80}\+\s*n\s*\+[\s\S]{0,80}\+\s*role/.test(mapSrc),
  'pill HTML emits aria-label with count + role');

// V2.g — DO NOT touch --info / --warning / --accent (out of scope hard rule).
const mcRoleBlock = cssSrc.match(/--mc-role-[\s\S]{0,1500}/);
assert(mcRoleBlock && !/--info\s*:|--warning\s*:|--accent\s*:/.test(mcRoleBlock[0]),
  'role pill constants are --mc-* namespaced (do not redefine --info/--warning/--accent)');

console.log('\n=== #1356 V3: multi-byte hash labels — glyph + neutral fill + colored border-left ===');

// V3.a — MB_GLYPHS map for ✓ / ? / ✗.
assert(/MB_GLYPHS\s*=\s*\{[\s\S]*?confirmed[\s\S]*?['"\\]u2713|MB_GLYPHS\s*=\s*\{[\s\S]*?confirmed[\s\S]*?['"]\u2713['"]/.test(mapSrc) ||
       /MB_GLYPHS\s*=\s*\{[\s\S]*?confirmed[\s\S]*?['"]✓['"]/.test(mapSrc),
  'map.js defines MB_GLYPHS with ✓ for confirmed');
assert(/MB_GLYPHS[\s\S]*?suspected[\s\S]*?['"]\?['"]/.test(mapSrc),
  'MB_GLYPHS.suspected === "?"');
assert(/MB_GLYPHS[\s\S]*?unknown[\s\S]*?['"\\]u2717|MB_GLYPHS[\s\S]*?unknown[\s\S]*?['"]✗['"]/.test(mapSrc),
  'MB_GLYPHS.unknown === ✗ (u2717)');

// V3.b — Neutral fill constant for multi-byte label.
assert(/--mc-mb-fill\s*:/.test(cssSrc),
  '--mc-mb-fill CSS variable declared (neutral fill, not status color)');

// V3.c — High-luminance accent set (audit override of Tol "vibrant").
//   Confirmed #56F0A0 / suspected #FFD966 / unknown #FF8888.
assert(/--mc-mb-confirmed\s*:\s*#56F0A0/i.test(cssSrc),
  '--mc-mb-confirmed is #56F0A0 (audit high-luminance set, not #117733)');
assert(/--mc-mb-suspected\s*:\s*#FFD966/i.test(cssSrc),
  '--mc-mb-suspected is #FFD966');
assert(/--mc-mb-unknown\s*:\s*#FF8888/i.test(cssSrc),
  '--mc-mb-unknown is #FF8888');

// V3.d — 3px colored left border in style.
assert(/border-left\s*:\s*3px solid/.test(cssSrc),
  '.mc-mb-label has 3px solid border-left (colored accent stripe)');

// V3.e/f/g are asserted on what makeRepeaterLabelIcon RENDERS, not on where its
// identifiers sit in the source. See loadMapInternals() above for why.
assert(mapInternals && typeof mapInternals.makeRepeaterLabelIcon === 'function',
  'map.js exposes makeRepeaterLabelIcon on window.__meshcoreMapInternals');

const labelConfirmed = mapInternals.makeRepeaterLabelIcon(NODE, false, false, 'confirmed').html;
const labelUnknown   = mapInternals.makeRepeaterLabelIcon(NODE, false, false, 'unknown').html;
const labelNoStatus  = mapInternals.makeRepeaterLabelIcon(NODE, false, false, null).html;

// V3.e — the glyph is PREPENDED to the hash: glyph, thin space, hash, in that
// order and with nothing between them.
assert(labelConfirmed.includes('>\u2713' + THIN + '3E7A<'),
  'makeRepeaterLabelIcon prepends the confirmed glyph to the hash text');
assert(labelUnknown.includes('>\u2717' + THIN + '3E7A<'),
  'makeRepeaterLabelIcon prepends the unknown glyph to the hash text');
assert(labelNoStatus.includes('>3E7A<') && !labelNoStatus.includes(THIN),
  'with no multi-byte status the label is the bare hash, no glyph and no thin space');

// V3.f — aria-label "multi-byte <status>, hash <ID>", and the plain form without.
assert(labelConfirmed.includes('aria-label="multi-byte confirmed, hash 3E7A"'),
  'makeRepeaterLabelIcon emits aria-label "multi-byte <status>, hash <ID>"');
assert(labelNoStatus.includes('aria-label="repeater hash 3E7A"'),
  'without a status the aria-label is "repeater hash <ID>"');

// V3.g — the visible glyph+hash span is aria-hidden so AT reads the label only
// (not "check mark 3 E"). Asserted on the emitted markup: the glyph must sit
// inside a span carrying aria-hidden.
assert(/<span aria-hidden="true">\u2713\u20093E7A<\/span>/.test(labelConfirmed),
  'visible glyph+hash span is aria-hidden="true" (AT reads aria-label only)');

// V3.h — repeater label MUST use the neutral fill via var(--mc-mb-fill); MUST
//   NOT paint background per-status (that would re-enable the pre-#1356
//   color-only signal). Affirmative check on the neutral-fill rule AND
//   negative check on the per-status bgColor pattern (round-1 adversarial #5:
//   the prior `!removal || affirmative` form short-circuited to a tautology).
assert(/\.mc-mb-label\b[^{]*\{[^}]*background\s*:\s*var\(--mc-mb-fill\)/.test(cssSrc),
  '.mc-mb-label background uses var(--mc-mb-fill) — neutral fill, not status color');
assert(!/bgColor\s*=\s*colorOverride\s*\|\|\s*s\.color/.test(mapSrc),
  'old per-status bgColor pattern is gone (no per-status background painting)');

console.log('\n=== #1356 Round-1 coverage adds: dual-marker star, null mbStatus, forced-colors ===');

// COV-1 — Observer-also-repeater dual marker: the ★ star glyph inside
//   makeRepeaterLabelIcon's obsIndicator branch MUST carry aria-hidden="true",
//   otherwise the AT announcement is polluted with "black star" / "star" on
//   top of the meaningful aria-label. Round-1 (Kent + adversarial) flagged.
//   Match either the legacy `★` glyph form OR the Phosphor SVG form
//   (issue #1648 M2 swap) — both rendered inside an aria-hidden="true" span.
assert(/isAlsoObserver[\s\S]{0,40}\?\s*['"][^'"]*<span\s+aria-hidden="true"[^>]*>[^<]*(?:★|<svg[^>]*class="ph-icon"[^>]*>[\s\S]{0,200}?#ph-star)/.test(mapSrc),
  'observer-also-repeater star span carries aria-hidden="true" (no AT pollution)');

// COV-2 — makeRepeaterLabelIcon with no multi_byte_status field must NOT emit
//   an aria-label containing "multi-byte undefined" (the obvious bug if the
//   null-fallback branch is dropped). Verify the source has the explicit
//   `mbStatus || null` + truthy-check structure that prevents this.
assert(/var\s+status\s*=\s*mbStatus\s*\|\|\s*null\s*;/.test(mapSrc),
  'makeRepeaterLabelIcon normalises missing mbStatus to null (not "undefined")');
assert(/ariaStatus\s*=\s*status\s*\?\s*\(\s*['"]multi-byte\s/.test(mapSrc),
  'ariaStatus uses ternary on truthy `status` — null falls through to "repeater hash <ID>" branch');
// Negative regression: no template/concat that would ever produce "multi-byte undefined".
assert(!/['"]multi-byte\s*['"]\s*\+\s*mbStatus(?![^,]*\?)/.test(mapSrc),
  'no unconditional concat of "multi-byte " + mbStatus (would emit "multi-byte undefined" on null)');

// COV-3 — @media (forced-colors: active) block MUST exist in style.css AND
//   MUST NOT contain `forced-color-adjust: none` anywhere within its body
//   (audit explicitly warned against `none`; degrades High Contrast Mode).
const fcMatch = cssSrc.match(/@media\s*\(\s*forced-colors\s*:\s*active\s*\)\s*\{[\s\S]*?\n\}/);
assert(fcMatch, '@media (forced-colors: active) block present in style.css');
assert(fcMatch && !/forced-color-adjust\s*:\s*none/i.test(fcMatch[0]),
  '@media (forced-colors: active) block does NOT use forced-color-adjust: none (audit regression guard)');


console.log('\n=== #1356 Hard rules: --info / --warning / --accent untouched ===');

// Sanity: ensure new --mc-* constants don't redefine the reserved system vars.
// (--info and --warning are only used via var(..., fallback) — they may not be declared
//  at all; --accent IS declared.)
const newConstantsBlock = (cssSrc.match(/\/\*[^*]*#1356[\s\S]*?\*\/[\s\S]*?(?=\/\*|$)/) || ['', ''])[0];
assert(!/--info\s*:|--warning\s*:|--accent\s*:/.test(newConstantsBlock),
  '#1356 CSS block does not redefine --info / --warning / --accent');
assert(/--accent\s*:/.test(cssSrc), '--accent CSS variable still defined');

console.log('\n=== Summary ===');
console.log(`  Passed: ${passed}`);
console.log(`  Failed: ${failed}`);
if (failed > 0) { console.error('\n#1356 FAIL'); process.exit(1); }
console.log('\n#1356 PASS');
