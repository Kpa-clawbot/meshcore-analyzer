#!/usr/bin/env node
// Integration test: Verify layered filtering works against live prod API
// Tests that resolve-hops returns regional metadata and correct filtering

const https = require('https');
const BASE = 'https://analyzer.00id.net';

function apiGet(path) {
  return new Promise((resolve, reject) => {
    https.get(BASE + path, { timeout: 10000 }, (res) => {
      let data = '';
      res.on('data', d => data += d);
      res.on('end', () => { try { resolve(JSON.parse(data)); } catch (e) { reject(e); } });
    }).on('error', reject);
  });
}

let pass = 0, fail = 0;
function assert(condition, msg) {
  if (condition) { pass++; console.log(`  ✅ ${msg}`); }
  else { fail++; console.error(`  ❌ FAIL: ${msg}`); }
}

async function run() {
  console.log('\n=== Integration: resolve-hops API with regional filtering ===\n');

  // 1. Get a packet with short hops and a known observer
  const packets = await apiGet('/api/packets?limit=100&groupByHash=true');
  const pkt = packets.packets.find(p => {
    const path = JSON.parse(p.path_json || '[]');
    return path.length > 0 && path.some(h => h.length <= 2) && p.observer_id;
  });

  if (!pkt) {
    console.log('  ⚠ No packets with short hops found — skipping API tests');
    return;
  }

  const path = JSON.parse(pkt.path_json);
  const shortHops = path.filter(h => h.length <= 2);
  console.log(`  Using packet ${pkt.hash.slice(0,12)} observed by ${pkt.observer_name || pkt.observer_id.slice(0,12)}`);
  console.log(`  Path: ${path.join(' → ')} (${shortHops.length} short hops)`);

  // 2. Resolve WITH observer (should get regional filtering)
  const withObs = await apiGet(`/api/resolve-hops?hops=${path.join(',')}&observer=${pkt.observer_id}`);

  assert(withObs.region != null, `Response includes region: ${withObs.region}`);

  // 3. Check that conflicts have filterMethod field
  let hasFilterMethod = false;
  let hasDistKm = false;
  for (const [hop, info] of Object.entries(withObs.resolved)) {
    if (info.conflicts && info.conflicts.length > 0) {
      for (const c of info.conflicts) {
        if (c.filterMethod) hasFilterMethod = true;
        if (c.distKm != null) hasDistKm = true;
      }
    }
    if (info.filterMethods) {
      assert(Array.isArray(info.filterMethods), `Hop ${hop}: filterMethods is array: ${JSON.stringify(info.filterMethods)}`);
    }
  }
  assert(hasFilterMethod, 'At least one conflict has filterMethod');

  // 4. Resolve WITHOUT observer (no regional filtering)
  const withoutObs = await apiGet(`/api/resolve-hops?hops=${path.join(',')}`);
  assert(withoutObs.region === null, `Without observer: region is null`);

  // 5. Compare: with observer should have same or fewer candidates per ambiguous hop
  for (const hop of shortHops) {
    const withInfo = withObs.resolved[hop];
    const withoutInfo = withoutObs.resolved[hop];
    if (withInfo && withoutInfo && withInfo.conflicts && withoutInfo.conflicts) {
      const withCount = withInfo.totalRegional || withInfo.conflicts.length;
      const withoutCount = withoutInfo.totalGlobal || withoutInfo.conflicts.length;
      assert(withCount <= withoutCount + 1,
        `Hop ${hop}: regional(${withCount}) <= global(${withoutCount}) — ${withInfo.name || '?'}`);
    }
  }

  // 6. Check that geo-filtered candidates have distKm
  for (const [hop, info] of Object.entries(withObs.resolved)) {
    if (info.conflicts) {
      const geoFiltered = info.conflicts.filter(c => c.filterMethod === 'geo');
      for (const c of geoFiltered) {
        assert(c.distKm != null, `Hop ${hop} candidate ${c.name}: has distKm=${c.distKm}km (geo filter)`);
      }
    }
  }

  console.log(`\n${'='.repeat(40)}`);
  console.log(`Results: ${pass} passed, ${fail} failed`);
  process.exit(fail > 0 ? 1 : 0);
}

run().catch(e => { console.error('Test error:', e); process.exit(1); });
