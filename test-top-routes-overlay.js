/**
 * "Important Links" map overlay (issue #672 / D) — a public, B-weighted
 * top-routes layer in public/map.js. It joins the loaded nodes (coords +
 * the #672 usefulness/bridge/redundancy/traffic scores from /api/nodes) with
 * the public neighbor-graph edges, ranks edges by a chosen axis, and draws the
 * top-N weighted polylines so geographic chokepoints stand out.
 *
 * Two layers of coverage:
 *  - structural pins (file-grep) for the DOM wiring that needs Leaflet/DOM
 *    (toggle, rank-by select, slider, load/clear/render handlers);
 *  - BEHAVIORAL tests that execute the pure ranking core computeTopRouteEdges
 *    against fixtures and assert on importance, ordering, top-N and skips.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const src = fs.readFileSync(path.join(__dirname, 'public', 'map.js'), 'utf8');

console.log('\n=== overlay wiring (structural — needs Leaflet/DOM) ===');
assert(/id="mcTopRoutes"[^>]*>\s*<svg/.test(src) || /<input type="checkbox" id="mcTopRoutes">/.test(src),
  'controls template has the #mcTopRoutes toggle');
['usefulness', 'bridge', 'redundancy', 'traffic', 'affinity'].forEach(ax => {
  assert(new RegExp('value="' + ax + '"').test(src), 'rank-by select offers "' + ax + '"');
});
assert(/id="mcTopRoutesN"/.test(src) && /type="range"/.test(src), 'top-N slider present');
assert(/function loadTopRoutes[\s\S]{0,400}\/analytics\/neighbor-graph/.test(src),
  'loadTopRoutes fetches the public neighbor-graph endpoint');
assert(/checked\) loadTopRoutes\(\); else clearTopRoutes\(\)/.test(src),
  'toggle wires loadTopRoutes/clearTopRoutes');
assert(/renderTopRoutes\(\)/.test(src) && /topRoutesLayer = L\.layerGroup\(\)/.test(src),
  'renderTopRoutes builds a dedicated layer group');

// --- extract & execute the pure ranking core ---
const start = src.indexOf('const TOP_ROUTES_AXES');
const end = src.indexOf('function clearTopRoutes');
if (start < 0 || end < 0) { console.error('  ✗ could not locate the ranking core'); process.exit(1); }
const block = src.slice(start, end);
// Guard the indexOf-based slice: fail loudly (not silently) if the function is
// renamed out of the extracted block.
assert(block.includes('function computeTopRouteEdges'), 'extracted block contains computeTopRouteEdges');
const M = new Function(block + '\nreturn { TOP_ROUTES_AXES, computeTopRouteEdges };')();

console.log('\n=== ranking core (behavioral) ===');
const nodes = [
  { public_key: 'AA', lat: 50.0, lon: 7.0, usefulness_score: 0.9, bridge_score: 0.1 },
  { public_key: 'BB', lat: 50.1, lon: 7.1, usefulness_score: 0.8, bridge_score: 0.9 },
  { public_key: 'CC', lat: 50.2, lon: 7.2, usefulness_score: 0.1, bridge_score: 0.1 },
  { public_key: 'DD', lat: null, lon: null, usefulness_score: 0.9 },                  // no GPS
  { public_key: 'FF', lat: 50.4, lon: 7.4, usefulness_score: 0 },                     // zero score
  { public_key: 'GG', lat: 50.5, lon: 7.5, usefulness_score: 0 },                     // zero score
];
const edges = [
  { source: 'AA', target: 'BB', score: 0.5 },
  { source: 'AA', target: 'CC', score: 0.8 },
  { source: 'BB', target: 'CC', score: 0.3 },
  { source: 'AA', target: 'DD', score: 0.9 }, // DD has no GPS → skipped
  { source: 'FF', target: 'GG', score: 0.6 }, // both zero usefulness → skipped on usefulness axis
];

const u = M.computeTopRouteEdges(edges, nodes, 'usefulness', 50);
const key = t => t.a + '-' + t.b;
assert(key(u[0]) === 'aa-bb' && Math.abs(u[0].importance - 0.425) < 1e-9,
  'usefulness: top link is AA↔BB, importance = edge.score × mean(endpoint scores)');
assert(u.length === 3, 'usefulness: GPS-less (AA-DD) and zero-score (FF-GG) edges dropped → 3 remain');
assert(!u.some(t => t.a === 'aa' && t.b === 'dd'), 'edge with a GPS-less endpoint is skipped');
assert(!u.some(t => t.a === 'ff' || t.b === 'gg'), 'zero-importance edge skipped on a score axis');
assert(u.every((t, i) => i === 0 || u[i - 1].importance >= t.importance), 'edges sorted by importance desc');

console.log('\n=== axis choice changes the ranking ===');
const b = M.computeTopRouteEdges(edges, nodes, 'bridge', 50);
// usefulness order: AA-BB, AA-CC, BB-CC. bridge order: AA-BB, BB-CC, AA-CC (swap).
assert(key(b[1]) === 'bb-cc' && key(u[1]) === 'aa-cc',
  'switching axis (usefulness→bridge) reorders the 2nd-ranked link');

console.log('\n=== affinity-only axis + top-N ===');
const aff = M.computeTopRouteEdges(edges, nodes, 'affinity', 50);
assert(key(aff[0]) === 'aa-cc' && Math.abs(aff[0].importance - 0.8) < 1e-9,
  'affinity axis: importance is the raw edge affinity (top = AA↔CC at 0.8)');
assert(aff.some(t => t.a === 'ff' && t.b === 'gg'),
  'zero-score nodes still link on the affinity axis (no endpoint weighting)');
const capped = M.computeTopRouteEdges(edges, nodes, 'usefulness', 2);
assert(capped.length === 2 && key(capped[0]) === 'aa-bb',
  'top-N caps the result to N highest-importance links');

console.log('\n────────────────────────────────────────');
console.log(`  ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
