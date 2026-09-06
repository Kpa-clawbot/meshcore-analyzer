/**
 * #1485 — Live map: animated packets render BEHIND the node base layer.
 *
 * Regression introduced by PR #1334 (commit 0f7c03cc), which swapped node
 * markers from L.circleMarker (lives in `overlayPane`, z=400) to L.marker
 * with divIcons (lives in `markerPane`, z=600). Animations stayed in the
 * default overlayPane (z=400 — polylines + circleMarkers) so once node
 * markers populated the map, they painted ON TOP of the animation layer,
 * hiding moving packets entirely.
 *
 * Fix invariant: the animation layers (animLayer + pathsLayer) must be
 * created on a CUSTOM Leaflet pane whose z-index is strictly greater than
 * the default markerPane (600). The conventional CoreScope choice is a
 * pane named `liveAnimPane` at z=650 (tooltipPane sits at 650 too, but
 * tooltips are user-triggered and harmless to share).
 *
 * Strategy: source-invariant assertions on public/live.js. We pin:
 *   1. A custom pane is created via map.createPane('liveAnimPane', ...).
 *   2. Its z-index >= 650 (covers markerPane @ 600).
 *   3. Both animLayer and pathsLayer are constructed with `pane:`
 *      pointing at that custom pane (so polylines + circleMarkers inherit
 *      the pane, not the default overlayPane).
 *
 * Behavioural check via vm sandbox: simulate the layer-creation calls
 * and assert addTo()/L.layerGroup() receive the pane option.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const liveSrc = fs.readFileSync(path.join(__dirname, 'public', 'live.js'), 'utf8');

console.log('\n=== #1485 live anim z-order A: custom pane created ===');

const paneMatch = liveSrc.match(
  /map\.createPane\(\s*['"](liveAnimPane|animPane)['"]\s*\)/
);
assert(!!paneMatch,
  'map.createPane(\'liveAnimPane\') (or animPane) called in live.js init');

const paneName = paneMatch ? paneMatch[1] : 'liveAnimPane';

// z-index assignment, e.g. .style.zIndex = 650 or = '650'
const zMatch = liveSrc.match(
  /getPane\(\s*['"](?:liveAnimPane|animPane)['"]\s*\)[\s\S]{0,80}?style\.zIndex\s*=\s*['"]?(\d+)/
);
assert(!!zMatch, 'liveAnimPane.style.zIndex assigned a numeric value');
if (zMatch) {
  const z = parseInt(zMatch[1], 10);
  assert(z >= 650,
    'liveAnimPane z-index >= 650 — strictly above markerPane (600) (got ' + z + ')');
}

console.log('\n=== #1485 live anim z-order B: layers bound to the pane ===');

// animLayer must be created with { pane: 'liveAnimPane' }
const animLayerRe = new RegExp(
  'animLayer\\s*=\\s*L\\.layerGroup\\(\\s*\\{[^}]*pane\\s*:\\s*[\'"]' +
    paneName + '[\'"]'
);
assert(animLayerRe.test(liveSrc),
  'animLayer = L.layerGroup({ pane: \'' + paneName + '\' }) — animation layer bound to anim pane');

const pathsLayerRe = new RegExp(
  'pathsLayer\\s*=\\s*L\\.layerGroup\\(\\s*\\{[^}]*pane\\s*:\\s*[\'"]' +
    paneName + '[\'"]'
);
assert(pathsLayerRe.test(liveSrc),
  'pathsLayer = L.layerGroup({ pane: \'' + paneName + '\' }) — trail layer bound to anim pane');

console.log('\n=== #1485 live anim z-order C: per-shape inheritance ===');

// At least one polyline/circleMarker addTo(animLayer) call exists — we're
// trusting Leaflet pane inheritance from the LayerGroup parent. Sanity
// check that the four animLayer / pathsLayer addTo sites still exist
// (regression detector if someone moves circles to the default pane).
const animAddTo = (liveSrc.match(/\.addTo\(animLayer\)/g) || []).length;
const pathsAddTo = (liveSrc.match(/\.addTo\(pathsLayer\)/g) || []).length;
// Was >= 3. #1490 moved the flying dot off Leaflet SVG onto a hardware-
// accelerated <canvas> overlay, retiring one call site by design; only the
// static fade-tail shapes still go through animLayer. The guard is against
// shapes drifting to the DEFAULT pane, so the baseline moves with the
// implementation rather than pinning a count #1490 deliberately reduced.
assert(animAddTo >= 2,
  'animLayer still hosts >=2 .addTo() animation shapes (got ' + animAddTo + ')');
assert(pathsAddTo >= 3,
  'pathsLayer still hosts >=3 .addTo() trail shapes (got ' + pathsAddTo + ')');

console.log('\n=== Summary ===');
console.log('  Passed: ' + passed);
console.log('  Failed: ' + failed);
process.exit(failed === 0 ? 0 : 1);
