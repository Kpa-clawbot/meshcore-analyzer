/**
 * Repeater Metric Scatter — a new analytics tab plotting each repeater
 * (or room) as a point with two selectable usefulness metrics on the axes.
 *
 * The metrics are NOT computed here: /api/nodes already attaches
 * traffic_share_score, bridge_score and relay_count_1h/24h to repeater/room
 * rows; advert_count comes from the base node row. This tab only plots them.
 *
 * Two layers of coverage:
 *  - structural pins (file-grep) for the tab WIRING that can't run without a
 *    DOM/app (tab button, dispatch, area-filter registration);
 *  - BEHAVIORAL tests that actually execute the pure render pipeline
 *    (REPEATER_METRIC_AXES … renderMetricScatter) against stub globals and
 *    assert on the produced SVG — real behaviour, not "source contains X".
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const src = fs.readFileSync(path.join(__dirname, 'public', 'analytics.js'), 'utf8');

console.log('\n=== tab wiring (structural — not executable without a DOM) ===');
assert(/data-tab="repeater-metrics"[^>]*>\s*Repeater Metrics\s*</.test(src),
  'tab bar has a "Repeater Metrics" button');
assert(/case 'repeater-metrics':\s*await renderRepeaterMetricsTab\(el\)/.test(src),
  'renderTab dispatches repeater-metrics');
assert(/AREA_FILTER_TABS[\s\S]{0,200}'repeater-metrics'/.test(src),
  'repeater-metrics participates in region/area filtering');

// --- Extract and execute the pure render pipeline with stub globals. ---
const start = src.indexOf('const REPEATER_METRIC_AXES');
const end = src.indexOf('async function renderRepeaterMetricsTab');
if (start < 0 || end < 0) {
  console.error('  ✗ could not locate the REPEATER_METRIC_AXES … renderMetricScatter block');
  process.exit(1);
}
const block = src.slice(start, end);
const esc = s => s ? String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;') : '';
const statusYellow = () => '#eab308';
const window = { ROLE_COLORS: { repeater: '#3b82f6', room: '#a855f7' } };
const M = new Function('esc', 'statusYellow', 'window',
  block + '\nreturn { REPEATER_METRIC_AXES, _niceCeil, _axisFmt, _resolveAxis, _toScatterPoints, renderMetricScatter };')(esc, statusYellow, window);

console.log('\n=== axis math (behavioral) ===');
assert(M._niceCeil(0) === 1 && M._niceCeil(0.07) === 0.1 && M._niceCeil(37) === 50,
  '_niceCeil rounds up to 1/2/5×10ⁿ');
assert(M._niceCeil(-1) === 1 && M._niceCeil(NaN) === 1 && M._niceCeil(-0.5) === 1,
  '_niceCeil guards NaN/negative/zero → 1 (the !(v>0) contract)');
assert(M._axisFmt({ score: true }, 0.1234) === '12.3%' && M._axisFmt({ score: false }, 5) === '5',
  '_axisFmt renders scores as % and counts as integers');
assert(M._axisFmt({ score: true }, 0.2) === '20%' && M._axisFmt({ score: true }, 0) === '0%',
  '_axisFmt drops the trailing .0 on whole-percent gridlines');
assert(M.REPEATER_METRIC_AXES.map(a => a.key).join(',') === 'traffic,bridge,relay1h,relay24h,adverts',
  'axis registry exposes the five repeater metrics');
// Unknown/stale stored key (e.g. leftover localStorage) falls back to the
// FIRST axis instead of throwing or returning undefined.
const bogus = M._resolveAxis('bogus', [{ traffic: 0.4 }]);
assert(bogus.key === 'traffic' && bogus.max > 0,
  "_resolveAxis('bogus') falls back to the traffic axis with a real domain");

console.log('\n=== node→point mapping (behavioral — _toScatterPoints) ===');
const favSet = new Set(['FAV0000000000000']);
const mapped = M._toScatterPoints([
  { public_key: 'FAV0000000000000', name: 'Fav Rptr', role: 'repeater', traffic_share_score: 0.5, bridge_score: 0.2, relay_count_1h: 1, relay_count_24h: 2, advert_count: 3 },
  { public_key: 'USEFULNESS000000', name: 'Old Server', role: 'room', usefulness_score: 0.07 },
  { public_key: 'NOSCORES00000000', name: 'Bare', role: 'repeater' },
  { public_key: 'NAMELESS00000000ABCDEF', role: 'repeater' },
  { public_key: 'COMPANION0000000', name: 'Phone', role: 'companion', traffic_share_score: 0.9 },
  { role: 'repeater' },
], favSet);
assert(mapped.length === 5 && !mapped.some(p => p.role === 'companion'),
  'non-repeater/room roles are filtered out');
assert(mapped[0].traffic === 0.5 && mapped[0].fav === true,
  'traffic_share_score is preferred and favorites are flagged');
assert(mapped[0].bridge === 0.2 && mapped[0].relay1h === 1 && mapped[0].relay24h === 2 && mapped[0].adverts === 3,
  'bridge/relay/advert counts map onto their renamed point fields (advert_count → adverts)');
// #1927: no fallback. usefulness_score is the #672 composite
// (0.30*bridge + 0.25*coverage + ...), a different metric from the single
// traffic axis, and substituting it put a composite under a column and an axis
// that both promise share of non-advert traffic. A node carrying only the
// composite now reads as unknown on that axis and is dropped from the plot by
// the plottable filter, which is what #1927 asked for.
assert(mapped[1].traffic === null && mapped[1].fav === false,
  'a node with only usefulness_score has no traffic value; it must not be substituted');
assert(mapped[2].traffic === null && mapped[2].bridge === null && mapped[2].relay1h === null,
  'rows without scores map to null (not 0/undefined) so plots can skip them');
assert(mapped[3].name === 'NAMELESS0000',
  'nameless node falls back to a 12-char pubkey prefix');
assert(mapped[4].name === '?' && mapped[4].pk === undefined,
  'node with neither name nor pubkey falls back to "?"');

console.log('\n=== scatter render (behavioral — executes renderMetricScatter) ===');
const pts = [
  { pk: 'AA', name: 'Rptr Eins', role: 'repeater', fav: true, traffic: 0.42, bridge: 0.1, relay1h: 12, relay24h: 200, adverts: 50 },
  { pk: 'BB', name: 'Raum <Zwei>', role: 'room', fav: false, traffic: 0.05, bridge: 0.8, relay1h: 0, relay24h: 3, adverts: 5 },
  { pk: 'CC', name: 'Rptr Drei', role: 'repeater', fav: false, traffic: null, bridge: 0.3, relay1h: 4, relay24h: 40, adverts: 9 },
];
const xa = M._resolveAxis('traffic', pts), ya = M._resolveAxis('bridge', pts);
const svg = M.renderMetricScatter(pts, xa, ya);
assert(svg.startsWith('<svg') && svg.endsWith('</svg>') && !/NaN/.test(svg),
  'produces a valid <svg> with no NaN coordinates');
assert((svg.match(/href="#\/nodes\//g) || []).length === 2,
  'plots only points with BOTH axis values (CC has null traffic → skipped)');
assert(/href="#\/nodes\/AA\/analytics"/.test(svg),
  'each point links to its per-node analytics');
assert(/fill="#3b82f6"/.test(svg) && /fill="#a855f7"/.test(svg),
  'point fill follows node role colour');
assert(/Raum &lt;Zwei&gt;/.test(svg),
  'point names are HTML-escaped in the tooltip');

console.log('\n=== round-1 fixes (regression guards) ===');
// Favorite ring uses the neutral foreground colour, never statusYellow.
assert(/stroke="var\(--text\)"/.test(svg) && !/stroke="#eab308"/.test(svg),
  'favorite ring uses var(--text), not statusYellow()');
// Tooltip uses ' · ' separators, not newlines (SVG <title> collapses ws).
assert(/Rptr Eins · /.test(svg) && !/Rptr Eins\n/.test(svg),
  "tooltip uses ' · ' separators, not '\\n'");
// Points are out of the keyboard tab order.
assert(/tabindex="-1"/.test(svg), 'points carry tabindex="-1" (keyboard tab order)');
// Unknown role falls back to var(--text-muted), not a hardcoded hex.
const unk = M.renderMetricScatter([{ pk: 'Z', name: 'z', role: 'mystery', fav: false, traffic: 0.5, bridge: 0.5 }], M._resolveAxis('traffic', pts), M._resolveAxis('bridge', pts));
assert(/fill="var\(--text-muted\)"/.test(unk) && !/#6b7280/.test(unk),
  'unknown-role point falls back to var(--text-muted)');

console.log('\n=== round-2 fixes (behavioral regression guards) ===');
// MAJOR: favorites are kept UNCONDITIONALLY through sampling; cap ~2000.
const many = [];
for (let i = 0; i < 2500; i++) many.push({ pk: 'p' + i, name: 'n' + i, role: 'repeater', fav: (i % 500 === 0), traffic: i / 2500, bridge: (i % 7) / 7 });
const favCount = many.filter(p => p.fav).length;
const bigSvg = M.renderMetricScatter(many, M._resolveAxis('traffic', many), M._resolveAxis('bridge', many));
assert(/showing \d+ of 2500 points/.test(bigSvg),
  'sampling >2000 points is disclosed in-plot ("showing N of 2500 points")');
const plotted = (bigSvg.match(/href="#\/nodes\//g) || []).length;
assert(plotted <= 2000 + favCount && plotted > 1000, 'plotted point count respects the ~2000 cap');
// every favorite pubkey must still appear (the legend must not lie)
const allFavsShown = many.filter(p => p.fav).every(p => bigSvg.includes('#/nodes/' + p.pk + '/analytics'));
assert(allFavsShown, 'ALL favorites survive sampling (legend count stays truthful)');
// Round-3 nit: even an all-favorites set beyond the cap must be strided so the
// cap can't be bypassed.
const allFav = [];
for (let i = 0; i < 2500; i++) allFav.push({ pk: 'q' + i, name: 'n' + i, role: 'repeater', fav: true, traffic: i / 2500, bridge: 0.5 });
const allFavSvg = M.renderMetricScatter(allFav, M._resolveAxis('traffic', allFav), M._resolveAxis('bridge', allFav));
const allFavPlotted = (allFavSvg.match(/href="#\/nodes\//g) || []).length;
assert(allFavPlotted <= 2000 && allFavPlotted > 1000,
  '2500 favorites are themselves strided to respect the cap (no bypass)');
assert(!/showing \d+ of \d+ points/.test(svg),
  'no sampling disclosure for a small point set');
// NIT: empty-axis selection shows a message, not a blank plot.
const empties = [{ pk: 'E', name: 'e', role: 'repeater', fav: false, traffic: null, bridge: null }];
const emptySvg = M.renderMetricScatter(empties, M._resolveAxis('traffic', empties), M._resolveAxis('bridge', empties));
assert(/No repeaters have values/.test(emptySvg),
  'empty-axis selection renders an explicit "no values" message');

console.log('\n────────────────────────────────────────');
console.log(`  ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
