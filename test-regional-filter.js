#!/usr/bin/env node
// Test: Regional hop resolution filtering
// Validates that resolve-hops correctly filters candidates by geography and observer region

const { IATA_COORDS, haversineKm, nodeNearRegion } = require('./iata-coords');

let pass = 0, fail = 0;

function assert(condition, msg) {
  if (condition) { pass++; console.log(`  ✅ ${msg}`); }
  else { fail++; console.error(`  ❌ FAIL: ${msg}`); }
}

// === 1. Haversine distance tests ===
console.log('\n=== Haversine Distance ===');

const sjcToSea = haversineKm(37.3626, -121.9290, 47.4502, -122.3088);
assert(sjcToSea > 1100 && sjcToSea < 1150, `SJC→SEA = ${Math.round(sjcToSea)}km (expect ~1125km)`);

const sjcToOak = haversineKm(37.3626, -121.9290, 37.7213, -122.2208);
assert(sjcToOak > 40 && sjcToOak < 55, `SJC→OAK = ${Math.round(sjcToOak)}km (expect ~48km)`);

const sjcToSjc = haversineKm(37.3626, -121.9290, 37.3626, -121.9290);
assert(sjcToSjc === 0, `SJC→SJC = ${sjcToSjc}km (expect 0)`);

const sjcToEug = haversineKm(37.3626, -121.9290, 44.1246, -123.2119);
assert(sjcToEug > 750 && sjcToEug < 780, `SJC→EUG = ${Math.round(sjcToEug)}km (expect ~762km)`);

// === 2. nodeNearRegion tests ===
console.log('\n=== Node Near Region ===');

// Node in San Jose, check against SJC region
const sjNode = nodeNearRegion(37.35, -121.95, 'SJC');
assert(sjNode && sjNode.near, `San Jose node near SJC: ${sjNode.distKm}km`);

// Node in Seattle, check against SJC region — should NOT be near
const seaNode = nodeNearRegion(47.45, -122.30, 'SJC');
assert(seaNode && !seaNode.near, `Seattle node NOT near SJC: ${seaNode.distKm}km`);

// Node in Seattle, check against SEA region — should be near
const seaNodeSea = nodeNearRegion(47.45, -122.30, 'SEA');
assert(seaNodeSea && seaNodeSea.near, `Seattle node near SEA: ${seaNodeSea.distKm}km`);

// Node in Eugene, check against EUG — should be near
const eugNode = nodeNearRegion(44.05, -123.10, 'EUG');
assert(eugNode && eugNode.near, `Eugene node near EUG: ${eugNode.distKm}km`);

// Eugene node should NOT be near SJC (~762km)
const eugNodeSjc = nodeNearRegion(44.05, -123.10, 'SJC');
assert(eugNodeSjc && !eugNodeSjc.near, `Eugene node NOT near SJC: ${eugNodeSjc.distKm}km`);

// Node with no location — returns null
const noLoc = nodeNearRegion(null, null, 'SJC');
assert(noLoc === null, 'Null lat/lon returns null');

// Node at 0,0 — returns null
const zeroLoc = nodeNearRegion(0, 0, 'SJC');
assert(zeroLoc === null, 'Zero lat/lon returns null');

// Unknown IATA — returns null
const unkIata = nodeNearRegion(37.35, -121.95, 'ZZZ');
assert(unkIata === null, 'Unknown IATA returns null');

// === 3. Edge cases: nodes just inside/outside 300km radius ===
console.log('\n=== Boundary Tests (300km radius) ===');

// Sacramento is ~145km from SJC — inside
const smfNode = nodeNearRegion(38.58, -121.49, 'SJC');
assert(smfNode && smfNode.near, `Sacramento near SJC: ${smfNode.distKm}km (expect ~145)`);

// Fresno is ~235km from SJC — inside
const fatNode = nodeNearRegion(36.74, -119.79, 'SJC');
assert(fatNode && fatNode.near, `Fresno near SJC: ${fatNode.distKm}km (expect ~235)`);

// Redding is ~400km from SJC — outside
const rddNode = nodeNearRegion(40.59, -122.39, 'SJC');
assert(rddNode && !rddNode.near, `Redding NOT near SJC: ${rddNode.distKm}km (expect ~400)`);

// === 4. Simulate the core issue: 1-byte hop with cross-regional collision ===
console.log('\n=== Cross-Regional Collision Simulation ===');

// Two nodes with pubkeys starting with "D6": one in SJC area, one in SEA area
const candidates = [
  { name: 'Redwood Mt. Tam', pubkey: 'D6...sjc', lat: 37.92, lon: -122.60 },  // Marin County, CA
  { name: 'VE7RSC North Repeater', pubkey: 'D6...sea', lat: 49.28, lon: -123.12 }, // Vancouver, BC
  { name: 'KK7RXY Lynden', pubkey: 'D6...bel', lat: 48.94, lon: -122.47 }, // Bellingham, WA
];

// Packet observed in SJC region
const packetIata = 'SJC';
const geoFiltered = candidates.filter(c => {
  const check = nodeNearRegion(c.lat, c.lon, packetIata);
  return check && check.near;
});
assert(geoFiltered.length === 1, `Geo filter SJC: ${geoFiltered.length} candidates (expect 1)`);
assert(geoFiltered[0].name === 'Redwood Mt. Tam', `Winner: ${geoFiltered[0].name} (expect Redwood Mt. Tam)`);

// Packet observed in SEA region
const seaFiltered = candidates.filter(c => {
  const check = nodeNearRegion(c.lat, c.lon, 'SEA');
  return check && check.near;
});
assert(seaFiltered.length === 2, `Geo filter SEA: ${seaFiltered.length} candidates (expect 2 — Vancouver + Bellingham)`);

// Packet observed in EUG region — Eugene is ~300km from SEA nodes
const eugFiltered = candidates.filter(c => {
  const check = nodeNearRegion(c.lat, c.lon, 'EUG');
  return check && check.near;
});
assert(eugFiltered.length === 0, `Geo filter EUG: ${eugFiltered.length} candidates (expect 0 — all too far)`);

// === 5. Layered fallback logic ===
console.log('\n=== Layered Fallback ===');

const nodeWithGps = { lat: 37.92, lon: -122.60 }; // has GPS
const nodeNoGps = { lat: null, lon: null }; // no GPS
const observerSawNode = true; // observer-based filter says yes

// Layer 1: GPS check
const gpsCheck = nodeNearRegion(nodeWithGps.lat, nodeWithGps.lon, 'SJC');
assert(gpsCheck && gpsCheck.near, 'Layer 1 (GPS): node with GPS near SJC');

// Layer 2: No GPS, fall back to observer
const gpsCheckNoLoc = nodeNearRegion(nodeNoGps.lat, nodeNoGps.lon, 'SJC');
assert(gpsCheckNoLoc === null, 'Layer 2: no GPS returns null → use observer-based fallback');

// Bridged WA node with GPS — should be REJECTED by SJC even though observer saw it
const bridgedWaNode = { lat: 47.45, lon: -122.30 }; // Seattle
const bridgedCheck = nodeNearRegion(bridgedWaNode.lat, bridgedWaNode.lon, 'SJC');
assert(bridgedCheck && !bridgedCheck.near, `Bridge test: WA node rejected by SJC geo filter (${bridgedCheck.distKm}km)`);

// === Summary ===
console.log(`\n${'='.repeat(40)}`);
console.log(`Results: ${pass} passed, ${fail} failed`);
process.exit(fail > 0 ? 1 : 0);
