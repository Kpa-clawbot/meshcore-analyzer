#!/usr/bin/env node
'use strict';

// Server route integration tests via supertest
process.env.NODE_ENV = 'test';
process.env.SEED_DB = 'true';  // Seed test data

const request = require('supertest');
const { app, server, wss, pktStore, db, cache } = require('./server');

let passed = 0, failed = 0;

async function t(name, fn) {
  try {
    await fn();
    passed++;
  } catch (e) {
    failed++;
    console.error(`FAIL: ${name} — ${e.message}`);
  }
}

function assert(cond, msg) { if (!cond) throw new Error(msg || 'assertion failed'); }

// Seed additional test data for branch coverage
function seedTestData() {
  const now = new Date().toISOString();
  const yesterday = new Date(Date.now() - 86400000).toISOString();
  
  // Add nodes with various roles and locations
  const nodes = [
    { public_key: 'aabb' + '0'.repeat(60), name: 'TestRepeater1', role: 'repeater', lat: 37.7749, lon: -122.4194, last_seen: now, first_seen: yesterday },
    { public_key: 'ccdd' + '0'.repeat(60), name: 'TestRoom1', role: 'room', lat: 40.7128, lon: -74.0060, last_seen: now, first_seen: yesterday },
    { public_key: 'eeff' + '0'.repeat(60), name: 'TestCompanion1', role: 'companion', lat: 0, lon: 0, last_seen: yesterday, first_seen: yesterday },
    { public_key: '1122' + '0'.repeat(60), name: 'TestSensor1', role: 'sensor', lat: 51.5074, lon: -0.1278, last_seen: now, first_seen: yesterday },
    // Node with same 2-char prefix as TestRepeater1 to test ambiguous resolution
    { public_key: 'aabb' + '1'.repeat(60), name: 'TestRepeater2', role: 'repeater', lat: 34.0522, lon: -118.2437, last_seen: now, first_seen: yesterday },
  ];
  for (const n of nodes) {
    try { db.upsertNode(n); } catch {}
  }

  // Add observer
  try { db.upsertObserver({ id: 'test-obs-1', name: 'TestObs', iata: 'SFO', last_seen: now, first_seen: yesterday }); } catch {}
  try { db.upsertObserver({ id: 'test-obs-2', name: 'TestObs2', iata: 'NYC', last_seen: now, first_seen: yesterday }); } catch {}

  // Add packets with paths and decoded data
  const packets = [
    {
      raw_hex: '11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172',
      timestamp: now, observer_id: 'test-obs-1', snr: 10.5, rssi: -85,
      hash: 'test-hash-001', route_type: 1, payload_type: 4, payload_version: 1,
      path_json: JSON.stringify(['aabb', 'ccdd']),
      decoded_json: JSON.stringify({ type: 'ADVERT', name: 'TestRepeater1', pubKey: 'aabb' + '0'.repeat(60), role: 'repeater', lat: 37.7749, lon: -122.4194, flags: { repeater: true } }),
    },
    {
      raw_hex: '2233445566778899AABBCCDD',
      timestamp: yesterday, observer_id: 'test-obs-1', snr: -5, rssi: -110,
      hash: 'test-hash-002', route_type: 0, payload_type: 5, payload_version: 1,
      path_json: JSON.stringify(['aabb', 'ccdd', 'eeff']),
      decoded_json: JSON.stringify({ type: 'TXT_MSG', text: 'Hello test', channelHash: 'ch01', channel_hash: 'ch01', srcName: 'TestCompanion1' }),
    },
    {
      raw_hex: 'AABBCCDD00112233',
      timestamp: now, observer_id: 'test-obs-2', snr: 8, rssi: -70,
      hash: 'test-hash-003', route_type: 3, payload_type: 4, payload_version: 1,
      path_json: JSON.stringify(['1122', 'aabb']),
      decoded_json: JSON.stringify({ type: 'ADVERT', name: 'TestSensor1', pubKey: '1122' + '0'.repeat(60), role: 'sensor', lat: 51.5074, lon: -0.1278, flags: { sensor: true } }),
    },
    {
      raw_hex: 'FF00FF00FF00FF00',
      timestamp: now, observer_id: 'test-obs-1', snr: 15, rssi: -60,
      hash: 'test-hash-001', route_type: 1, payload_type: 4, payload_version: 1,
      path_json: JSON.stringify(['aabb', 'ccdd']),
      decoded_json: JSON.stringify({ type: 'ADVERT', name: 'TestRepeater1', pubKey: 'aabb' + '0'.repeat(60) }),
    },
    {
      raw_hex: '5566778899AABB00',
      timestamp: now, observer_id: 'test-obs-2', snr: 3, rssi: -90,
      hash: 'test-hash-004', route_type: 0, payload_type: 5, payload_version: 1,
      path_json: JSON.stringify(['eeff', 'aabb', 'ccdd', '1122']),
      decoded_json: JSON.stringify({ type: 'TXT_MSG', text: 'Another msg', channelHash: 'ch02', srcName: 'TestRoom1' }),
    },
  ];

  for (const pkt of packets) {
    try { pktStore.insert(pkt); } catch {}
    try { db.insertTransmission(pkt); } catch {}
  }

  // Seed another packet with CHAN type for channel messages
  const chanPkt = {
    raw_hex: 'AA00BB00CC00DD00',
    timestamp: now, observer_id: 'test-obs-1', observer_name: 'TestObs', snr: 5, rssi: -80,
    hash: 'test-hash-005', route_type: 0, payload_type: 5, payload_version: 1,
    path_json: JSON.stringify(['aabb', 'ccdd']),
    decoded_json: JSON.stringify({ type: 'CHAN', channel: 'ch01', text: 'UserA: Hello world', sender: 'UserA', sender_timestamp: now, SNR: 5 }),
  };
  try { pktStore.insert(chanPkt); } catch {}
  try { db.insertTransmission(chanPkt); } catch {}

  // Another CHAN message for dedup code path coverage
  const chanPkt3 = {
    raw_hex: 'FF00EE00DD00CC00',
    timestamp: now, observer_id: 'test-obs-1', observer_name: 'TestObs', snr: 7, rssi: -75,
    hash: 'test-hash-006', route_type: 1, payload_type: 5, payload_version: 1,
    path_json: JSON.stringify([]),
    decoded_json: JSON.stringify({ type: 'CHAN', channel: 'ch01', text: 'UserB: Test msg', sender: 'UserB' }),
  };
  try { pktStore.insert(chanPkt3); } catch {}
  try { db.insertTransmission(chanPkt3); } catch {}

  // Duplicate of same message from different observer (for dedup/repeats coverage)
  const chanPkt2 = {
    raw_hex: 'AA00BB00CC00DD00',
    timestamp: now, observer_id: 'test-obs-2', observer_name: 'TestObs2', snr: 3, rssi: -90,
    hash: 'test-hash-005', route_type: 0, payload_type: 5, payload_version: 1,
    path_json: JSON.stringify(['aabb', 'ccdd']),
    decoded_json: JSON.stringify({ type: 'CHAN', channel: 'ch01', text: 'UserA: Hello world', sender: 'UserA' }),
  };
  try { pktStore.insert(chanPkt2); } catch {}
  try { db.insertTransmission(chanPkt2); } catch {}

  // Packet with sender_key/recipient_key for peer interaction coverage in db.getNodeAnalytics
  const peerPkt = {
    raw_hex: 'DEADBEEF00112233',
    timestamp: yesterday, observer_id: 'test-obs-1', observer_name: 'TestObs', snr: 8, rssi: -82,
    hash: 'test-hash-007', route_type: 0, payload_type: 2, payload_version: 1,
    path_json: JSON.stringify(['aabb']),
    decoded_json: JSON.stringify({ type: 'TXT_MSG', sender_key: 'aabb' + '0'.repeat(60), sender_name: 'TestRepeater1', recipient_key: 'ccdd' + '0'.repeat(60), recipient_name: 'TestRoom1', text: 'hello' }),
  };
  try { pktStore.insert(peerPkt); } catch {}
  try { db.insertTransmission(peerPkt); } catch {}

  // Clear cache so fresh data is picked up
  cache.clear();
}

seedTestData();

(async () => {
  console.log('── Server Route Tests ──');

  // --- Config routes ---
  await t('GET /api/config/cache', async () => {
    const r = await request(app).get('/api/config/cache').expect(200);
    assert(r.body && typeof r.body === 'object', 'should return object');
  });

  await t('GET /api/config/client', async () => {
    const r = await request(app).get('/api/config/client').expect(200);
    assert(typeof r.body === 'object', 'should return config');
  });

  await t('GET /api/config/regions', async () => {
    const r = await request(app).get('/api/config/regions').expect(200);
    assert(typeof r.body === 'object', 'should return regions');
  });

  await t('GET /api/config/theme', async () => {
    const r = await request(app).get('/api/config/theme').expect(200);
    assert(typeof r.body === 'object', 'should return theme object');
  });

  await t('GET /api/config/map', async () => {
    const r = await request(app).get('/api/config/map').expect(200);
    assert(typeof r.body === 'object', 'should return map config');
  });

  // --- Health ---
  await t('GET /api/health', async () => {
    const r = await request(app).get('/api/health').expect(200);
    assert(r.body.status, 'should have status');
  });

  // --- Stats ---
  await t('GET /api/stats', async () => {
    const r = await request(app).get('/api/stats').expect(200);
    assert(typeof r.body === 'object', 'should return stats');
  });

  // --- Perf ---
  await t('GET /api/perf', async () => {
    const r = await request(app).get('/api/perf').expect(200);
    assert(typeof r.body === 'object', 'should return perf data');
  });

  await t('POST /api/perf/reset', async () => {
    const r = await request(app).post('/api/perf/reset');
    assert(r.status === 200 || r.status === 403, 'should return 200 or 403');
  });

  // --- Nodes ---
  await t('GET /api/nodes default', async () => {
    const r = await request(app).get('/api/nodes').expect(200);
    assert(Array.isArray(r.body) || r.body.nodes, 'should return nodes');
  });

  await t('GET /api/nodes with limit', async () => {
    await request(app).get('/api/nodes?limit=5').expect(200);
  });

  await t('GET /api/nodes with offset', async () => {
    await request(app).get('/api/nodes?limit=2&offset=1').expect(200);
  });

  await t('GET /api/nodes with role=repeater', async () => {
    await request(app).get('/api/nodes?role=repeater').expect(200);
  });

  await t('GET /api/nodes with role=room', async () => {
    await request(app).get('/api/nodes?role=room').expect(200);
  });

  await t('GET /api/nodes with region=SFO', async () => {
    await request(app).get('/api/nodes?region=SFO').expect(200);
  });

  await t('GET /api/nodes with search', async () => {
    await request(app).get('/api/nodes?search=Test').expect(200);
  });

  await t('GET /api/nodes with lastHeard', async () => {
    await request(app).get('/api/nodes?lastHeard=86400').expect(200);
  });

  await t('GET /api/nodes with sortBy=name', async () => {
    await request(app).get('/api/nodes?sortBy=name').expect(200);
  });

  await t('GET /api/nodes with sortBy=role', async () => {
    await request(app).get('/api/nodes?sortBy=role').expect(200);
  });

  await t('GET /api/nodes with before cursor', async () => {
    await request(app).get('/api/nodes?before=2099-01-01T00:00:00Z').expect(200);
  });

  await t('GET /api/nodes with large limit', async () => {
    await request(app).get('/api/nodes?limit=10000&lastHeard=259200').expect(200);
  });

  await t('GET /api/nodes/search with q', async () => {
    const r = await request(app).get('/api/nodes/search?q=Test').expect(200);
    assert(Array.isArray(r.body) || typeof r.body === 'object', 'should return results');
  });

  await t('GET /api/nodes/search without q', async () => {
    await request(app).get('/api/nodes/search').expect(200);
  });

  await t('GET /api/nodes/bulk-health', async () => {
    const r = await request(app).get('/api/nodes/bulk-health').expect(200);
    assert(typeof r.body === 'object', 'should return bulk health');
  });

  await t('GET /api/nodes/network-status', async () => {
    const r = await request(app).get('/api/nodes/network-status').expect(200);
    assert(typeof r.body === 'object', 'should return network status');
  });

  cache.clear(); // Clear to avoid cache hits for regional queries
  await t('GET /api/nodes/network-status with region', async () => {
    await request(app).get('/api/nodes/network-status?region=SFO').expect(200);
  });

  cache.clear();
  await t('GET /api/nodes/bulk-health with region', async () => {
    await request(app).get('/api/nodes/bulk-health?region=SFO').expect(200);
  });

  // Test with real node pubkey
  const testPubkey = 'aabb' + '0'.repeat(60);
  
  await t('GET /api/nodes/:pubkey — existing', async () => {
    const r = await request(app).get(`/api/nodes/${testPubkey}`);
    assert(r.status === 200 || r.status === 404, 'should find or not find');
  });

  await t('GET /api/nodes/:pubkey — nonexistent', async () => {
    await request(app).get('/api/nodes/' + '0'.repeat(64)).expect(404);
  });

  await t('GET /api/nodes/:pubkey/health — existing', async () => {
    const r = await request(app).get(`/api/nodes/${testPubkey}/health`);
    assert(r.status === 200 || r.status === 404, 'should handle');
  });

  await t('GET /api/nodes/:pubkey/health — nonexistent', async () => {
    const r = await request(app).get('/api/nodes/nonexistent/health');
    assert(r.status === 404 || r.status === 200, 'should handle missing node');
  });

  await t('GET /api/nodes/:pubkey/paths — existing', async () => {
    const r = await request(app).get(`/api/nodes/${testPubkey}/paths`);
    assert(r.status === 200 || r.status === 404, 'should handle');
  });

  await t('GET /api/nodes/:pubkey/paths — nonexistent', async () => {
    const r = await request(app).get('/api/nodes/nonexistent/paths');
    assert(r.status === 404 || r.status === 200, 'should handle missing');
  });

  await t('GET /api/nodes/:pubkey/paths with days param', async () => {
    await request(app).get(`/api/nodes/${testPubkey}/paths?days=7`);
  });

  await t('GET /api/nodes/:pubkey/analytics — existing', async () => {
    const r = await request(app).get(`/api/nodes/${testPubkey}/analytics`);
    assert(r.status === 200 || r.status === 404, 'should handle');
  });

  await t('GET /api/nodes/:pubkey/analytics with days', async () => {
    await request(app).get(`/api/nodes/${testPubkey}/analytics?days=7`);
  });

  await t('GET /api/nodes/:pubkey/analytics — nonexistent', async () => {
    const r = await request(app).get('/api/nodes/nonexistent/analytics');
    assert(r.status === 404 || r.status === 200, 'should handle missing');
  });

  // --- Packets ---
  await t('GET /api/packets default', async () => {
    const r = await request(app).get('/api/packets').expect(200);
    assert(typeof r.body === 'object', 'should return packets');
  });

  await t('GET /api/packets with limit', async () => {
    await request(app).get('/api/packets?limit=5').expect(200);
  });

  await t('GET /api/packets with offset', async () => {
    await request(app).get('/api/packets?limit=5&offset=0').expect(200);
  });

  await t('GET /api/packets with type', async () => {
    await request(app).get('/api/packets?type=ADVERT').expect(200);
  });

  await t('GET /api/packets with route', async () => {
    await request(app).get('/api/packets?route=1').expect(200);
  });

  await t('GET /api/packets with observer', async () => {
    await request(app).get('/api/packets?observer=test-obs-1').expect(200);
  });

  await t('GET /api/packets with region', async () => {
    await request(app).get('/api/packets?region=SFO').expect(200);
  });

  await t('GET /api/packets with hash', async () => {
    await request(app).get('/api/packets?hash=test-hash-001').expect(200);
  });

  await t('GET /api/packets with since/until', async () => {
    await request(app).get('/api/packets?since=2020-01-01T00:00:00Z&until=2099-01-01T00:00:00Z').expect(200);
  });

  await t('GET /api/packets with groupByHash', async () => {
    await request(app).get('/api/packets?groupByHash=true').expect(200);
  });

  await t('GET /api/packets with node filter', async () => {
    await request(app).get(`/api/packets?node=${testPubkey}`).expect(200);
  });

  await t('GET /api/packets with nodes (multi)', async () => {
    await request(app).get(`/api/packets?nodes=${testPubkey},ccdd${'0'.repeat(60)}`).expect(200);
  });

  await t('GET /api/packets with order asc', async () => {
    await request(app).get('/api/packets?order=asc').expect(200);
  });

  await t('GET /api/packets/timestamps without since', async () => {
    await request(app).get('/api/packets/timestamps').expect(400);
  });

  await t('GET /api/packets/timestamps with since', async () => {
    const r = await request(app).get('/api/packets/timestamps?since=2020-01-01T00:00:00Z').expect(200);
    assert(typeof r.body === 'object', 'should return timestamps');
  });

  await t('GET /api/packets/:id — id 1', async () => {
    const r = await request(app).get('/api/packets/1');
    assert(r.status === 200 || r.status === 404, 'should handle');
  });

  await t('GET /api/packets/:id — nonexistent', async () => {
    const r = await request(app).get('/api/packets/999999');
    assert(r.status === 404 || r.status === 200, 'should handle missing packet');
  });

  // --- POST /api/decode ---
  await t('POST /api/decode without hex', async () => {
    await request(app).post('/api/decode').send({}).expect(400);
  });

  await t('POST /api/decode with invalid hex', async () => {
    await request(app).post('/api/decode').send({ hex: 'zzzz' }).expect(400);
  });

  await t('POST /api/decode with valid hex', async () => {
    const r = await request(app).post('/api/decode')
      .send({ hex: '11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172' });
    assert(r.status === 200 || r.status === 400, 'should not crash');
  });

  // --- POST /api/packets ---
  await t('POST /api/packets without hex', async () => {
    await request(app).post('/api/packets').send({}).expect(400);
  });

  await t('POST /api/packets with hex (no api key configured)', async () => {
    const r = await request(app).post('/api/packets')
      .send({ hex: '11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172', observer: 'test-obs-1', region: 'SFO' });
    assert(r.status === 200 || r.status === 400 || r.status === 403, 'should handle');
  });

  await t('POST /api/packets with invalid hex', async () => {
    const r = await request(app).post('/api/packets').send({ hex: 'zzzz' });
    assert(r.status === 400, 'should reject invalid hex');
  });

  // --- Channels (clear cache first to ensure fresh data) ---
  cache.clear();
  await t('GET /api/channels', async () => {
    const r = await request(app).get('/api/channels').expect(200);
    assert(typeof r.body === 'object', 'should return channels');
  });

  await t('GET /api/channels with region', async () => {
    await request(app).get('/api/channels?region=SFO').expect(200);
  });

  await t('GET /api/channels/:hash/messages', async () => {
    const r = await request(app).get('/api/channels/ch01/messages').expect(200);
    assert(typeof r.body === 'object', 'should return messages');
  });

  await t('GET /api/channels/:hash/messages with params', async () => {
    await request(app).get('/api/channels/ch01/messages?limit=5&offset=0').expect(200);
  });

  await t('GET /api/channels/:hash/messages with region', async () => {
    await request(app).get('/api/channels/ch01/messages?region=SFO').expect(200);
  });

  await t('GET /api/channels/:hash/messages nonexistent', async () => {
    await request(app).get('/api/channels/nonexistent/messages').expect(200);
  });

  // --- Observers ---
  await t('GET /api/observers', async () => {
    const r = await request(app).get('/api/observers').expect(200);
    assert(typeof r.body === 'object', 'should return observers');
  });

  await t('GET /api/observers/:id — existing', async () => {
    const r = await request(app).get('/api/observers/test-obs-1');
    assert(r.status === 200 || r.status === 404, 'should handle');
  });

  await t('GET /api/observers/:id — nonexistent', async () => {
    const r = await request(app).get('/api/observers/nonexistent');
    assert(r.status === 404 || r.status === 200, 'should handle missing observer');
  });

  await t('GET /api/observers/:id/analytics — existing', async () => {
    const r = await request(app).get('/api/observers/test-obs-1/analytics');
    assert(r.status === 200 || r.status === 404, 'should handle');
  });

  await t('GET /api/observers/:id/analytics — nonexistent', async () => {
    const r = await request(app).get('/api/observers/nonexistent/analytics');
    assert(r.status === 404 || r.status === 200, 'should handle');
  });

  // --- Traces ---
  await t('GET /api/traces/:hash — existing', async () => {
    const r = await request(app).get('/api/traces/test-hash-001');
    assert(r.status === 200 || r.status === 404, 'should handle');
  });

  await t('GET /api/traces/:hash — nonexistent', async () => {
    const r = await request(app).get('/api/traces/nonexistent');
    assert(r.status === 200 || r.status === 404, 'should handle trace lookup');
  });

  // --- Analytics (clear cache before regional tests) ---
  cache.clear();
  await t('GET /api/analytics/rf', async () => {
    const r = await request(app).get('/api/analytics/rf').expect(200);
    assert(typeof r.body === 'object', 'should return RF analytics');
  });

  await t('GET /api/analytics/rf with region', async () => {
    await request(app).get('/api/analytics/rf?region=SFO').expect(200);
  });

  await t('GET /api/analytics/rf with region NYC', async () => {
    await request(app).get('/api/analytics/rf?region=NYC').expect(200);
  });

  await t('GET /api/analytics/topology', async () => {
    const r = await request(app).get('/api/analytics/topology').expect(200);
    assert(typeof r.body === 'object', 'should return topology');
  });

  await t('GET /api/analytics/topology with region', async () => {
    await request(app).get('/api/analytics/topology?region=SFO').expect(200);
  });

  await t('GET /api/analytics/channels', async () => {
    const r = await request(app).get('/api/analytics/channels').expect(200);
    assert(typeof r.body === 'object', 'should return channel analytics');
  });

  await t('GET /api/analytics/channels with region', async () => {
    await request(app).get('/api/analytics/channels?region=SFO').expect(200);
  });

  await t('GET /api/analytics/hash-sizes', async () => {
    const r = await request(app).get('/api/analytics/hash-sizes').expect(200);
    assert(typeof r.body === 'object', 'should return hash sizes');
  });

  await t('GET /api/analytics/hash-sizes with region', async () => {
    await request(app).get('/api/analytics/hash-sizes?region=SFO').expect(200);
  });

  await t('GET /api/analytics/subpaths', async () => {
    const r = await request(app).get('/api/analytics/subpaths').expect(200);
    assert(typeof r.body === 'object', 'should return subpaths');
  });

  await t('GET /api/analytics/subpaths with params', async () => {
    await request(app).get('/api/analytics/subpaths?minLen=2&maxLen=3&limit=10').expect(200);
  });

  await t('GET /api/analytics/subpaths with region', async () => {
    await request(app).get('/api/analytics/subpaths?region=SFO').expect(200);
  });

  await t('GET /api/analytics/subpath-detail with hops', async () => {
    const r = await request(app).get('/api/analytics/subpath-detail?hops=aabb,ccdd');
    assert(r.status === 200 || r.status === 400, 'should handle');
  });

  await t('GET /api/analytics/subpath-detail without hops', async () => {
    const r = await request(app).get('/api/analytics/subpath-detail');
    assert(r.status === 200 || r.status === 400, 'should handle missing hops');
  });

  await t('GET /api/analytics/distance', async () => {
    const r = await request(app).get('/api/analytics/distance').expect(200);
    assert(typeof r.body === 'object', 'should return distance analytics');
  });

  await t('GET /api/analytics/distance with region', async () => {
    await request(app).get('/api/analytics/distance?region=SFO').expect(200);
  });

  // --- Resolve hops ---
  await t('GET /api/resolve-hops with hops', async () => {
    const r = await request(app).get('/api/resolve-hops?hops=aabb,ccdd').expect(200);
    assert(typeof r.body === 'object', 'should return resolved hops');
  });

  await t('GET /api/resolve-hops without hops', async () => {
    await request(app).get('/api/resolve-hops').expect(200);
  });

  await t('GET /api/resolve-hops with region and observer', async () => {
    await request(app).get('/api/resolve-hops?hops=aabb,ccdd&region=SFO&observer=test-obs-1').expect(200);
  });

  await t('GET /api/resolve-hops with prefixes (legacy)', async () => {
    await request(app).get('/api/resolve-hops?prefixes=aabb,ccdd').expect(200);
  });

  await t('GET /api/resolve-hops ambiguous prefix', async () => {
    // 'aabb' matches both TestRepeater1 and TestRepeater2
    const r = await request(app).get('/api/resolve-hops?hops=aabb,ccdd,1122&region=SFO&observer=test-obs-1').expect(200);
    assert(typeof r.body === 'object', 'should resolve hops');
  });

  await t('GET /api/resolve-hops with packet context', async () => {
    await request(app).get('/api/resolve-hops?hops=aabb,eeff,ccdd&region=SFO&observer=test-obs-1&packetHash=test-hash-001').expect(200);
  });

  // --- IATA coords ---
  await t('GET /api/iata-coords', async () => {
    const r = await request(app).get('/api/iata-coords').expect(200);
    assert(r.body && 'coords' in r.body, 'should have coords key');
  });

  // --- Audio lab ---
  await t('GET /api/audio-lab/buckets', async () => {
    const r = await request(app).get('/api/audio-lab/buckets').expect(200);
    assert(r.body && 'buckets' in r.body, 'should have buckets key');
  });

  // --- SPA fallback ---
  await t('GET /nodes SPA fallback', async () => {
    const r = await request(app).get('/nodes');
    assert([200, 304, 404].includes(r.status), 'should not crash');
  });

  // --- Cache behavior: hit same endpoint twice ---
  await t('Cache hit: /api/nodes/bulk-health twice', async () => {
    await request(app).get('/api/nodes/bulk-health').expect(200);
    await request(app).get('/api/nodes/bulk-health').expect(200);
  });

  await t('Cache hit: /api/analytics/rf twice', async () => {
    await request(app).get('/api/analytics/rf').expect(200);
    await request(app).get('/api/analytics/rf').expect(200);
  });

  await t('Cache hit: /api/analytics/topology twice', async () => {
    await request(app).get('/api/analytics/topology').expect(200);
    await request(app).get('/api/analytics/topology').expect(200);
  });

  // ── WebSocket tests ──
  await t('WebSocket connection', async () => {
    const WebSocket = require('ws');
    await new Promise((resolve) => {
      if (server.address()) return resolve();
      server.listen(0, resolve);
    });
    const port = server.address().port;
    const ws = new WebSocket(`ws://127.0.0.1:${port}`);
    await new Promise((resolve, reject) => {
      ws.on('open', resolve);
      ws.on('error', reject);
      setTimeout(() => reject(new Error('WS timeout')), 3000);
    });
    assert(ws.readyState === WebSocket.OPEN, 'WS should be open');
    ws.close();
    await new Promise(r => setTimeout(r, 100));
  });

  // ── Additional query parameter branches ──
  await t('GET /api/nodes?sortBy=lastSeen', async () => {
    await request(app).get('/api/nodes?sortBy=lastSeen').expect(200);
  });
  await t('GET /api/nodes multi-filter', async () => {
    await request(app).get('/api/nodes?role=repeater&region=SFO&lastHeard=86400&search=Test').expect(200);
  });
  await t('GET /api/packets?type=4', async () => {
    await request(app).get('/api/packets?type=4').expect(200);
  });
  await t('GET /api/packets multi-filter', async () => {
    await request(app).get('/api/packets?type=5&route=1&observer=test-obs-1').expect(200);
  });
  await t('GET /api/packets?node=TestRepeater1', async () => {
    await request(app).get('/api/packets?node=TestRepeater1').expect(200);
  });
  await t('GET /api/packets?groupByHash=true&type=4&observer=test-obs-1', async () => {
    await request(app).get('/api/packets?groupByHash=true&type=4&observer=test-obs-1').expect(200);
  });
  await t('GET /api/packets?groupByHash=true&region=SFO', async () => {
    await request(app).get('/api/packets?groupByHash=true&region=SFO').expect(200);
  });
  await t('GET /api/nodes?sortBy=name&search=nonexistent', async () => {
    const r = await request(app).get('/api/nodes?sortBy=name&search=nonexistent').expect(200);
    assert(r.body.nodes.length === 0, 'no nodes');
  });
  await t('GET /api/nodes?before=2000-01-01', async () => {
    const r = await request(app).get('/api/nodes?before=2000-01-01T00:00:00Z').expect(200);
    assert(r.body.nodes.length === 0, 'no nodes');
  });

  // ── Node health/analytics/paths for nodes WITH packets ──
  const testRepeaterKey = 'aabb' + '0'.repeat(60);

  await t('GET /api/nodes/:pubkey/health with packets', async () => {
    cache.clear();
    const r = await request(app).get(`/api/nodes/${testRepeaterKey}/health`).expect(200);
    assert(r.body.node && r.body.stats, 'should have node+stats');
  });
  await t('GET /api/nodes/:pubkey/analytics?days=30', async () => {
    cache.clear();
    const r = await request(app).get(`/api/nodes/${testRepeaterKey}/analytics?days=30`).expect(200);
    assert(r.body.computedStats, 'should have computedStats');
  });
  await t('GET /api/nodes/:pubkey/analytics?days=1', async () => {
    cache.clear();
    await request(app).get(`/api/nodes/${testRepeaterKey}/analytics?days=1`).expect(200);
  });
  await t('GET /api/nodes/:pubkey/paths with packets', async () => {
    cache.clear();
    const r = await request(app).get(`/api/nodes/${testRepeaterKey}/paths`).expect(200);
    assert(r.body.paths !== undefined, 'should have paths');
  });
  await t('GET /api/nodes/:pubkey existing', async () => {
    await request(app).get(`/api/nodes/${testRepeaterKey}`).expect(200);
  });
  await t('GET /api/nodes/:pubkey 404', async () => {
    await request(app).get('/api/nodes/' + '0'.repeat(63) + '1').expect(404);
  });

  // ── Observer analytics ──
  await t('GET /api/observers/test-obs-1/analytics', async () => {
    cache.clear();
    const r = await request(app).get('/api/observers/test-obs-1/analytics').expect(200);
    assert(r.body.timeline !== undefined, 'should have timeline');
  });
  await t('GET /api/observers/test-obs-1/analytics?days=1', async () => {
    cache.clear();
    await request(app).get('/api/observers/test-obs-1/analytics?days=1').expect(200);
  });

  // ── Traces ──
  await t('GET /api/traces/:hash existing', async () => {
    const r = await request(app).get('/api/traces/test-hash-001').expect(200);
    assert(r.body.traces, 'should have traces');
  });

  // ── Resolve hops ──
  await t('GET /api/resolve-hops?hops=aabb', async () => {
    await request(app).get('/api/resolve-hops?hops=aabb').expect(200);
  });
  await t('GET /api/resolve-hops?hops=ffff', async () => {
    await request(app).get('/api/resolve-hops?hops=ffff').expect(200);
  });

  // ── Analytics with nonexistent region ──
  for (const ep of ['rf', 'topology', 'channels', 'distance', 'hash-sizes']) {
    await t(`GET /api/analytics/${ep}?region=ZZZZ`, async () => {
      cache.clear();
      await request(app).get(`/api/analytics/${ep}?region=ZZZZ`).expect(200);
    });
  }

  // ── Subpath endpoints ──
  await t('GET /api/analytics/subpaths?minLen=2&maxLen=2', async () => {
    cache.clear();
    await request(app).get('/api/analytics/subpaths?minLen=2&maxLen=2&limit=5').expect(200);
  });
  await t('GET /api/analytics/subpath-detail?hops=aabb,ccdd,eeff', async () => {
    cache.clear();
    await request(app).get('/api/analytics/subpath-detail?hops=aabb,ccdd,eeff').expect(200);
  });

  // ── POST /api/packets with observer+region ──
  await t('POST /api/packets with observer+region', async () => {
    const r = await request(app).post('/api/packets')
      .send({ hex: '11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172', observer: 'test-api-obs', region: 'LAX', snr: 12, rssi: -75 });
    assert([200, 400, 403].includes(r.status), 'should handle');
  });

  // ── POST /api/decode with whitespace ──
  await t('POST /api/decode whitespace hex', async () => {
    const r = await request(app).post('/api/decode').send({ hex: ' 1145 1000 D818 206D ' });
    assert([200, 400].includes(r.status), 'should handle');
  });

  // ── Direct db function calls ──
  await t('db.searchNodes', () => {
    assert(Array.isArray(db.searchNodes('Test', 10)), 'should return array');
    assert(db.searchNodes('zzzznonexistent', 5).length === 0, 'empty for no match');
  });
  await t('db.getNodeHealth existing', () => {
    const h = db.getNodeHealth(testRepeaterKey);
    assert(h && h.node && h.stats, 'should have node+stats');
  });
  await t('db.getNodeHealth nonexistent', () => {
    assert(db.getNodeHealth('nonexistent') === null, 'null for missing');
  });
  await t('db.getNodeAnalytics existing', () => {
    const a = db.getNodeAnalytics(testRepeaterKey, 7);
    assert(a && a.computedStats, 'should have computedStats');
  });
  await t('db.getNodeAnalytics nonexistent', () => {
    assert(db.getNodeAnalytics('nonexistent', 7) === null, 'null for missing');
  });
  await t('db.getNodeAnalytics for named node', () => {
    const a = db.getNodeAnalytics('ccdd' + '0'.repeat(60), 30);
    assert(a !== null, 'should return analytics');
  });
  await t('db.getNodeHealth for named node', () => {
    assert(db.getNodeHealth('ccdd' + '0'.repeat(60)) !== null, 'should have health');
  });
  await t('db.updateObserverStatus', () => {
    db.updateObserverStatus({ id: 'test-status-obs', name: 'StatusObs', iata: 'LAX', model: 'test', firmware: '1.0', client_version: '2.0', radio: '915,125,7,5', battery_mv: 3700, uptime_secs: 86400, noise_floor: -120 });
  });

  // ── Packet store direct methods ──
  await t('pktStore.getById missing', () => { assert(pktStore.getById(999999) === null); });
  await t('pktStore.getSiblings missing', () => { assert(pktStore.getSiblings('nonexistent').length === 0); });
  await t('pktStore.getTimestamps', () => { assert(Array.isArray(pktStore.getTimestamps('2000-01-01T00:00:00Z'))); });
  await t('pktStore.all', () => { assert(Array.isArray(pktStore.all())); });
  await t('pktStore.filter', () => { assert(Array.isArray(pktStore.filter(p => p.payload_type === 4))); });
  await t('pktStore.getStats', () => { const s = pktStore.getStats(); assert(s.inMemory !== undefined && s.indexes); });
  await t('pktStore.queryGrouped', () => { assert(pktStore.queryGrouped({ limit: 5, type: 4 }).packets !== undefined); });
  await t('pktStore.queryGrouped region+since', () => { assert(pktStore.queryGrouped({ limit: 5, region: 'SFO', since: '2000-01-01' }).packets !== undefined); });
  await t('pktStore.countForNode existing', () => { assert(pktStore.countForNode(testRepeaterKey).transmissions !== undefined); });
  await t('pktStore.countForNode missing', () => { assert(pktStore.countForNode('nonexistent').transmissions === 0); });
  await t('pktStore.findPacketsForNode', () => { assert(pktStore.findPacketsForNode(testRepeaterKey).packets !== undefined); });
  await t('pktStore._transmissionsForObserver with fromTx', () => {
    assert(Array.isArray(pktStore._transmissionsForObserver('test-obs-1', pktStore.all())));
  });

  // ── Cache SWR (stale-while-revalidate) ──
  await t('Cache stale-while-revalidate', async () => {
    // 50ms TTL, grace period = 100ms
    cache.set('swr-test', { data: 42 }, 50);
    await new Promise(r => setTimeout(r, 60)); // Wait past expiry but within grace
    const stale = cache.get('swr-test');
    assert(stale && stale.data === 42, 'should return stale value within grace');
  });
  await t('Cache fully expired (past grace)', async () => {
    cache.set('swr-expired', { data: 99 }, 10);
    await new Promise(r => setTimeout(r, 30)); // Past 2× TTL
    const expired = cache.get('swr-expired');
    assert(expired === undefined, 'should return undefined past grace');
  });
  await t('Cache isStale', async () => {
    cache.set('stale-test', { data: 1 }, 50);
    await new Promise(r => setTimeout(r, 60));
    assert(cache.isStale('stale-test') === true, 'should be stale');
  });
  await t('Cache recompute', () => {
    let ran = false;
    cache.recompute('recompute-test', () => { ran = true; });
    assert(ran === true, 'recompute fn should run');
  });
  await t('Cache debouncedInvalidateAll', async () => {
    cache.debouncedInvalidateAll();
    await new Promise(r => setTimeout(r, 200));
  });

  // ── Cache operations ──
  await t('Cache set/get/invalidate', () => {
    cache.set('test-key', { data: 1 }, 60000);
    assert(cache.get('test-key').data === 1);
    cache.invalidate('test-key');
  });
  await t('Cache invalidate+refetch', async () => {
    cache.clear();
    await request(app).get('/api/stats').expect(200);
    await request(app).get('/api/stats').expect(200);
  });

  // ── Channel messages fresh (dedup coverage) ──
  await t('GET /api/channels/ch01/messages fresh', async () => {
    cache.clear();
    const r = await request(app).get('/api/channels/ch01/messages').expect(200);
    assert(r.body.messages !== undefined, 'should have messages');
  });
  await t('GET /api/channels/ch01/messages?limit=1&offset=0', async () => {
    cache.clear();
    await request(app).get('/api/channels/ch01/messages?limit=1&offset=0').expect(200);
  });

  // ── Multi-filter packet queries ──
  await t('GET /api/packets?type=4&node=TestRepeater1', async () => {
    await request(app).get('/api/packets?type=4&node=TestRepeater1').expect(200);
  });
  await t('GET /api/packets all filters', async () => {
    await request(app).get('/api/packets?type=4&route=1&since=2000-01-01T00:00:00Z&until=2099-01-01T00:00:00Z&observer=test-obs-1&hash=test-hash-001').expect(200);
  });
  await t('GET /api/packets?type=5&region=SFO&node=TestRepeater1', async () => {
    await request(app).get('/api/packets?type=5&region=SFO&node=TestRepeater1&since=2000-01-01T00:00:00Z').expect(200);
  });

  // ── Perf/nocache bypass ──
  await t('GET /api/stats?nocache=1', async () => {
    await request(app).get('/api/stats?nocache=1').expect(200);
  });
  await t('GET /api/nodes?nocache=1', async () => {
    await request(app).get('/api/nodes?nocache=1').expect(200);
  });

  // ── More route branch coverage ──
  await t('GET /api/packets/:id by hash', async () => {
    await request(app).get('/api/packets/testhash00000001').expect(404); // 16 hex chars
  });
  await t('GET /api/packets/:id by string non-hash', async () => {
    await request(app).get('/api/packets/not-a-hash-or-number').expect(404);
  });

  // ── SPA fallback paths ──
  for (const path of ['/map', '/packets', '/analytics', '/live']) {
    await t(`GET ${path} SPA fallback`, async () => {
      const r = await request(app).get(path);
      assert([200, 304, 404].includes(r.status));
    });
  }

  // ── Network status ──
  await t('GET /api/nodes/network-status?region=ZZZZ', async () => {
    cache.clear();
    await request(app).get('/api/nodes/network-status?region=ZZZZ').expect(200);
  });

  // ── Bulk health variants ──
  await t('GET /api/nodes/bulk-health?limit=2', async () => {
    cache.clear();
    await request(app).get('/api/nodes/bulk-health?limit=2').expect(200);
  });
  await t('GET /api/nodes/bulk-health?region=NYC', async () => {
    cache.clear();
    await request(app).get('/api/nodes/bulk-health?region=NYC').expect(200);
  });

  // ── Decoder: various payload types ──
  await t('POST /api/decode REQ', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '0000' + 'AA'.repeat(20) });
    assert(r.status === 200 && r.body.decoded.payload.type === 'REQ');
  });
  await t('POST /api/decode RESPONSE', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '0400' + 'AA'.repeat(20) });
    assert(r.status === 200 && r.body.decoded.payload.type === 'RESPONSE');
  });
  await t('POST /api/decode TXT_MSG', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '0800' + 'AA'.repeat(20) });
    assert(r.status === 200 && r.body.decoded.payload.type === 'TXT_MSG');
  });
  await t('POST /api/decode ACK', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '0C00' + 'BB'.repeat(6) });
    assert(r.status === 200 && r.body.decoded.payload.type === 'ACK');
  });
  await t('POST /api/decode GRP_TXT', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '1500' + 'CC'.repeat(10) });
    assert(r.status === 200 && r.body.decoded.payload.type === 'GRP_TXT');
  });
  await t('POST /api/decode ANON_REQ', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '1D00' + 'DD'.repeat(40) });
    assert(r.status === 200 && r.body.decoded.payload.type === 'ANON_REQ');
  });
  await t('POST /api/decode PATH', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '2100' + 'EE'.repeat(10) });
    assert(r.status === 200 && r.body.decoded.payload.type === 'PATH');
  });
  await t('POST /api/decode TRACE', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '2500' + 'FF'.repeat(12) });
    assert(r.status === 200 && r.body.decoded.payload.type === 'TRACE');
  });
  await t('POST /api/decode UNKNOWN type', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '3C00' + '00'.repeat(10) });
    assert(r.status === 200 && r.body.decoded.payload.type === 'UNKNOWN');
  });
  await t('POST /api/decode TRANSPORT_FLOOD', async () => {
    const r = await request(app).post('/api/decode').send({ hex: '1200AABBCCDD' + '00'.repeat(101) });
    assert([200, 400].includes(r.status));
  });
  await t('POST /api/decode minimal ADVERT', async () => {
    await request(app).post('/api/decode').send({ hex: '1100' + '00'.repeat(100) }).expect(200);
  });
  await t('POST /api/decode too-short', async () => {
    await request(app).post('/api/decode').send({ hex: 'AA' }).expect(400);
  });
  // Short payloads triggering error branches
  await t('POST /api/decode GRP_TXT too short', async () => {
    await request(app).post('/api/decode').send({ hex: '1500AABB' }).expect(200);
  });
  await t('POST /api/decode ADVERT too short', async () => {
    await request(app).post('/api/decode').send({ hex: '1100' + 'AA'.repeat(10) }).expect(200);
  });
  await t('POST /api/decode TRACE too short', async () => {
    await request(app).post('/api/decode').send({ hex: '2500' + 'FF'.repeat(5) }).expect(200);
  });
  await t('POST /api/decode PATH too short', async () => {
    await request(app).post('/api/decode').send({ hex: '2100' + 'EE'.repeat(2) }).expect(200);
  });
  await t('POST /api/decode ANON_REQ too short', async () => {
    await request(app).post('/api/decode').send({ hex: '1D00' + 'DD'.repeat(10) }).expect(200);
  });

  // ── server-helpers: disambiguateHops direct tests ──
  await t('disambiguateHops ambiguous multi-candidate', () => {
    const helpers = require('./server-helpers');
    const allNodes = [
      { public_key: 'aabb' + '0'.repeat(60), name: 'Node1', lat: 37.7, lon: -122.4 },
      { public_key: 'aabb' + '1'.repeat(60), name: 'Node2', lat: 34.0, lon: -118.2 },
      { public_key: 'ccdd' + '0'.repeat(60), name: 'Node3', lat: 40.7, lon: -74.0 },
    ];
    const resolved = helpers.disambiguateHops(['aabb', 'ccdd'], allNodes);
    assert(resolved.length === 2 && resolved[0].name);
  });
  await t('disambiguateHops backward pass', () => {
    const helpers = require('./server-helpers');
    // Ambiguous first hop, known second → backward pass resolves first
    const allNodes = [
      { public_key: 'aa' + '3'.repeat(62), name: 'ANode1', lat: 37.7, lon: -122.4 },
      { public_key: 'aa' + '4'.repeat(62), name: 'ANode2', lat: 34.0, lon: -118.2 },
      { public_key: 'bb' + '3'.repeat(62), name: 'BNode', lat: 40.7, lon: -74.0 },
    ];
    const resolved = helpers.disambiguateHops(['aa', 'bb'], allNodes);
    assert(resolved.length === 2);
  });
  await t('disambiguateHops distance unreliable', () => {
    const helpers = require('./server-helpers');
    const allNodes = [
      { public_key: 'aa' + '5'.repeat(62), name: 'Near1', lat: 37.7, lon: -122.4 },
      { public_key: 'bb' + '5'.repeat(62), name: 'FarAway', lat: -33.8, lon: 151.2 },
      { public_key: 'cc' + '5'.repeat(62), name: 'Near2', lat: 37.8, lon: -122.3 },
    ];
    const resolved = helpers.disambiguateHops(['aa', 'bb', 'cc'], allNodes, 0.5);
    assert(resolved[1].unreliable === true, 'middle node should be unreliable');
  });
  await t('disambiguateHops unknown prefix', () => {
    const helpers = require('./server-helpers');
    const allNodes = [{ public_key: 'aabb' + '0'.repeat(60), name: 'Node1', lat: 37.7, lon: -122.4 }];
    const resolved = helpers.disambiguateHops(['ffff', 'aabb'], allNodes);
    assert(resolved[0].known === false);
  });
  await t('disambiguateHops single known match', () => {
    const helpers = require('./server-helpers');
    const allNodes = [{ public_key: 'ccdd' + '6'.repeat(60), name: 'UniqueNode', lat: 40.7, lon: -74.0 }];
    const resolved = helpers.disambiguateHops(['ccdd'], allNodes);
    assert(resolved[0].known === true && resolved[0].name === 'UniqueNode');
  });
  await t('disambiguateHops no-coord node', () => {
    const helpers = require('./server-helpers');
    const allNodes = [{ public_key: 'aabb' + '7'.repeat(60), name: 'NoCoord', lat: 0, lon: 0 }];
    const resolved = helpers.disambiguateHops(['aabb'], allNodes);
    assert(resolved.length === 1);
  });

  // ── isHashSizeFlipFlop ──
  await t('isHashSizeFlipFlop true', () => {
    const h = require('./server-helpers');
    assert(h.isHashSizeFlipFlop([1, 2, 1, 2], new Set([1, 2])) === true);
  });
  await t('isHashSizeFlipFlop false stable', () => {
    const h = require('./server-helpers');
    assert(h.isHashSizeFlipFlop([1, 1, 1], new Set([1])) === false);
  });
  await t('isHashSizeFlipFlop false short/null', () => {
    const h = require('./server-helpers');
    assert(h.isHashSizeFlipFlop([1, 2], new Set([1, 2])) === false);
    assert(h.isHashSizeFlipFlop(null, null) === false);
  });

  // ── Summary ──
  console.log(`\n═══ Server Route Tests: ${passed} passed, ${failed} failed ═══`);
  if (failed > 0) process.exit(1);
  process.exit(0);
})();
