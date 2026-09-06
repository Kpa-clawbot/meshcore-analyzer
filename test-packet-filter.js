/* Unit tests for packet filter language */
'use strict';
const vm = require('vm');
const fs = require('fs');

const labelsCode = fs.readFileSync('public/payload-labels.js', 'utf8');
const code = fs.readFileSync('public/packet-filter.js', 'utf8');
const ctx = { window: {}, console };
vm.createContext(ctx);
// PR #1804 r1 item 9: packet-filter.js now hard-fails if
// window.PayloadLabels is missing. Pre-load the canonical module so the
// test exercises the production code path.
vm.runInContext(labelsCode, ctx);
vm.runInContext(code, ctx);
const PF = ctx.window.PacketFilter;

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; }
  catch (e) { console.log(`FAIL: ${name} — ${e.message}`); fail++; }
}
function assert(cond, msg) { if (!cond) throw new Error(msg || 'assertion failed'); }

const pkt = {
  route_type: 1, payload_type: 5, snr: 8.5, rssi: -45,
  hash: 'abc123def456', raw_hex: '110500aabbccdd',
  path_json: '["8A","B5","97"]',
  decoded_json: JSON.stringify({
    name: "ESP1 Gilroy Repeater", lat: 37.005, lon: -121.567,
    pubKey: "f81d265c03c5c1b2", text: "Hello mesh", sender: "KpaPocket",
    flags: { raw: 147, type: 2, repeater: true, room: false, hasLocation: true, hasName: true }
  }),
  observer_name: 'kpabap', observer_id: '2301ACD8E9DCEDE5',
  observation_count: 3, timestamp: new Date().toISOString(),
};

const nullSnrPkt = { ...pkt, snr: null, rssi: null };

// --- Firmware type names ---
test('type == GRP_TXT', () => { assert(PF.compile('type == GRP_TXT').filter(pkt)); });
test('type == grp_txt (case insensitive)', () => { assert(PF.compile('type == grp_txt').filter(pkt)); });
test('type == ADVERT is false', () => { assert(!PF.compile('type == ADVERT').filter(pkt)); });
test('type == TXT_MSG is false', () => { assert(!PF.compile('type == TXT_MSG').filter(pkt)); });
test('type != GRP_TXT is false', () => { assert(!PF.compile('type != GRP_TXT').filter(pkt)); });
test('type != ADVERT is true', () => { assert(PF.compile('type != ADVERT').filter(pkt)); });

// --- Type aliases ---
test('type == channel (alias)', () => { assert(PF.compile('type == channel').filter(pkt)); });
test('type == "Channel Msg" (alias)', () => { assert(PF.compile('type == "Channel Msg"').filter(pkt)); });
test('type == dm is false', () => { assert(!PF.compile('type == dm').filter(pkt)); });
test('type == request is false', () => { assert(!PF.compile('type == request').filter(pkt)); });

// --- Route ---
test('route == FLOOD', () => { assert(PF.compile('route == FLOOD').filter(pkt)); });
test('route == DIRECT is false', () => { assert(!PF.compile('route == DIRECT').filter(pkt)); });

// --- Transport route filters (issue #339) ---
const tFloodPkt   = { ...pkt, route_type: 0 }; // TRANSPORT_FLOOD
const floodPkt    = { ...pkt, route_type: 1 }; // FLOOD
const directPkt   = { ...pkt, route_type: 2 }; // DIRECT
const tDirectPkt  = { ...pkt, route_type: 3 }; // TRANSPORT_DIRECT

test('route == TRANSPORT_FLOOD matches route_type 0', () => {
  assert(PF.compile('route == TRANSPORT_FLOOD').filter(tFloodPkt));
  assert(!PF.compile('route == TRANSPORT_FLOOD').filter(floodPkt));
});
test('route == TRANSPORT_DIRECT matches route_type 3', () => {
  assert(PF.compile('route == TRANSPORT_DIRECT').filter(tDirectPkt));
  assert(!PF.compile('route == TRANSPORT_DIRECT').filter(directPkt));
});
test('route == T_FLOOD alias matches route_type 0', () => {
  assert(PF.compile('route == T_FLOOD').filter(tFloodPkt));
  assert(!PF.compile('route == T_FLOOD').filter(floodPkt));
  assert(!PF.compile('route == T_FLOOD').filter(directPkt));
});
test('route == T_DIRECT alias matches route_type 3', () => {
  assert(PF.compile('route == T_DIRECT').filter(tDirectPkt));
  assert(!PF.compile('route == T_DIRECT').filter(directPkt));
  assert(!PF.compile('route == T_DIRECT').filter(tFloodPkt));
});
test('transport == true matches TRANSPORT_FLOOD and TRANSPORT_DIRECT', () => {
  assert(PF.compile('transport == true').filter(tFloodPkt));
  assert(PF.compile('transport == true').filter(tDirectPkt));
  assert(!PF.compile('transport == true').filter(floodPkt));
  assert(!PF.compile('transport == true').filter(directPkt));
});
test('transport == false matches non-transported FLOOD and DIRECT', () => {
  assert(PF.compile('transport == false').filter(floodPkt));
  assert(PF.compile('transport == false').filter(directPkt));
  assert(!PF.compile('transport == false').filter(tFloodPkt));
  assert(!PF.compile('transport == false').filter(tDirectPkt));
});
test('bare transport (truthy) matches transported packets', () => {
  assert(PF.compile('transport').filter(tFloodPkt));
  assert(PF.compile('transport').filter(tDirectPkt));
  assert(!PF.compile('transport').filter(floodPkt));
  assert(!PF.compile('transport').filter(directPkt));
});

// --- Hash ---
test('hash == abc123def456', () => { assert(PF.compile('hash == abc123def456').filter(pkt)); });
test('hash contains abc', () => { assert(PF.compile('hash contains abc').filter(pkt)); });
test('hash starts_with abc', () => { assert(PF.compile('hash starts_with abc').filter(pkt)); });
test('hash ends_with 456', () => { assert(PF.compile('hash ends_with 456').filter(pkt)); });

// --- Numeric ---
test('snr > 5', () => { assert(PF.compile('snr > 5').filter(pkt)); });
test('snr > 10 is false', () => { assert(!PF.compile('snr > 10').filter(pkt)); });
test('snr >= 8.5', () => { assert(PF.compile('snr >= 8.5').filter(pkt)); });
test('snr < 8.5 is false', () => { assert(!PF.compile('snr < 8.5').filter(pkt)); });
test('rssi < -40', () => { assert(PF.compile('rssi < -40').filter(pkt)); });
test('rssi < -50 is false', () => { assert(!PF.compile('rssi < -50').filter(pkt)); });

// --- Hops ---
test('hops == 3', () => { assert(PF.compile('hops == 3').filter(pkt)); });
test('hops > 2', () => { assert(PF.compile('hops > 2').filter(pkt)); });
test('hops > 3 is false', () => { assert(!PF.compile('hops > 3').filter(pkt)); });

// --- Observer ---
test('observer == kpabap', () => { assert(PF.compile('observer == kpabap').filter(pkt)); });
test('observer contains kpa', () => { assert(PF.compile('observer contains kpa').filter(pkt)); });

// --- Observations ---
test('observations > 1', () => { assert(PF.compile('observations > 1').filter(pkt)); });
test('observations == 3', () => { assert(PF.compile('observations == 3').filter(pkt)); });

// --- Size ---
test('size > 3', () => { assert(PF.compile('size > 3').filter(pkt)); });

// --- Payload dot notation ---
test('payload.name contains "Gilroy"', () => { assert(PF.compile('payload.name contains "Gilroy"').filter(pkt)); });
test('payload.name contains "Oakland" is false', () => { assert(!PF.compile('payload.name contains "Oakland"').filter(pkt)); });
test('payload.name starts_with "ESP1"', () => { assert(PF.compile('payload.name starts_with "ESP1"').filter(pkt)); });
test('payload.lat > 37', () => { assert(PF.compile('payload.lat > 37').filter(pkt)); });
test('payload.lat > 38 is false', () => { assert(!PF.compile('payload.lat > 38').filter(pkt)); });
test('payload.lon < -121', () => { assert(PF.compile('payload.lon < -121').filter(pkt)); });
test('payload.pubKey starts_with "f81d"', () => { assert(PF.compile('payload.pubKey starts_with "f81d"').filter(pkt)); });
test('payload.text contains "Hello"', () => { assert(PF.compile('payload.text contains "Hello"').filter(pkt)); });
test('payload.sender == "KpaPocket"', () => { assert(PF.compile('payload.sender == "KpaPocket"').filter(pkt)); });
test('payload.flags.hasLocation (truthy)', () => { assert(PF.compile('payload.flags.hasLocation').filter(pkt)); });
test('payload.flags.room is false (truthy)', () => { assert(!PF.compile('payload.flags.room').filter(pkt)); });
test('payload.flags.raw == 147', () => { assert(PF.compile('payload.flags.raw == 147').filter(pkt)); });
test('payload_hex contains "aabb"', () => { assert(PF.compile('payload_hex contains "aabb"').filter(pkt)); });

// --- Logic ---
test('type == GRP_TXT && snr > 5', () => { assert(PF.compile('type == GRP_TXT && snr > 5').filter(pkt)); });
test('type == GRP_TXT && snr > 10 is false', () => { assert(!PF.compile('type == GRP_TXT && snr > 10').filter(pkt)); });
test('type == ADVERT || snr > 5', () => { assert(PF.compile('type == ADVERT || snr > 5').filter(pkt)); });
test('type == ADVERT || snr > 10 is false', () => { assert(!PF.compile('type == ADVERT || snr > 10').filter(pkt)); });
test('!(type == ADVERT)', () => { assert(PF.compile('!(type == ADVERT)').filter(pkt)); });
test('!(type == GRP_TXT) is false', () => { assert(!PF.compile('!(type == GRP_TXT)').filter(pkt)); });

// --- Parentheses ---
test('(type == ADVERT || type == GRP_TXT) && snr > 5', () => {
  assert(PF.compile('(type == ADVERT || type == GRP_TXT) && snr > 5').filter(pkt));
});
test('(type == ADVERT) && snr > 5 is false', () => {
  assert(!PF.compile('(type == ADVERT) && snr > 5').filter(pkt));
});

// --- Complex ---
test('type == GRP_TXT && snr > 5 && hops > 2', () => {
  assert(PF.compile('type == GRP_TXT && snr > 5 && hops > 2').filter(pkt));
});
test('!(type == ACK) && !(type == PATH)', () => {
  assert(PF.compile('!(type == ACK) && !(type == PATH)').filter(pkt));
});
test('payload.lat >= 37 && payload.lat <= 38 && payload.lon >= -122 && payload.lon <= -121', () => {
  assert(PF.compile('payload.lat >= 37 && payload.lat <= 38 && payload.lon >= -122 && payload.lon <= -121').filter(pkt));
});

// --- Edge cases: null fields ---
test('snr > 5 with null snr → false', () => { assert(!PF.compile('snr > 5').filter(nullSnrPkt)); });
test('rssi < -50 with null rssi → false', () => { assert(!PF.compile('rssi < -50').filter(nullSnrPkt)); });
test('payload.nonexistent == "x" → false', () => { assert(!PF.compile('payload.nonexistent == "x"').filter(pkt)); });
test('payload.flags.nonexistent (truthy) → false', () => { assert(!PF.compile('payload.flags.nonexistent').filter(pkt)); });

// --- Error handling ---
test('empty filter → no error', () => {
  const c = PF.compile('');
  assert(c.error === null, 'should have no error');
});
test('invalid syntax → error message', () => {
  const c = PF.compile('== broken');
  assert(c.error !== null, 'should have error');
});
test('@@@ garbage → error', () => {
  const c = PF.compile('@@@ garbage');
  assert(c.error !== null, 'should have error');
});
test('unclosed quote → error', () => {
  const c = PF.compile('type == "hello');
  assert(c.error !== null, 'should have error');
});

// --- Transport region scope filter field ---
// scope_name is null for non-transport routes, "" when transport-scoped but the
// region did not match a configured key, and "#be" on a match.
const bePkt = { ...pkt, scope_name: '#be' };
const unknownScopePkt = { ...pkt, scope_name: '' };
const noScopePkt = { ...pkt, scope_name: null };

test('scope == "#be" matches a packet scoped to #be', () => {
  assert(PF.compile('scope == "#be"').filter(bePkt));
});
test('scope == "#be" is case-insensitive', () => {
  assert(PF.compile('scope == "#BE"').filter(bePkt));
});
test('scope == "#nl" does not match a #be packet', () => {
  assert(!PF.compile('scope == "#nl"').filter(bePkt));
});
test('scope == "" matches transport-scoped with an unmatched region', () => {
  assert(PF.compile('scope == ""').filter(unknownScopePkt));
  assert(!PF.compile('scope == ""').filter(bePkt));
});
test('scope == "" also matches a non-transport packet (both render as no scope)', () => {
  assert(PF.compile('scope == ""').filter(noScopePkt));
});
test('scope contains "be" matches #be', () => {
  assert(PF.compile('scope contains "be"').filter(bePkt));
});
test('scope listed in FIELDS suggestions', () => {
  const names = PF.FIELDS.map(f => f.name);
  assert(names.indexOf('scope') !== -1, 'scope in FIELDS');
});

// --- Observer IATA filter field (#1188) ---
const iataPkt = { ...pkt, observer_iata: 'SJC' };
const sfoPkt  = { ...pkt, observer_iata: 'SFO' };
const noIataPkt = { ...pkt, observer_iata: null };

test('observer_iata == "SJC" matches', () => {
  assert(PF.compile('observer_iata == "SJC"').filter(iataPkt));
});
test('observer_iata == "SJC" case-insensitive', () => {
  assert(PF.compile('observer_iata == "sjc"').filter(iataPkt));
});
test('observer_iata == "SFO" does not match SJC packet', () => {
  assert(!PF.compile('observer_iata == "SFO"').filter(iataPkt));
});
test('iata alias works like observer_iata', () => {
  assert(PF.compile('iata == "SJC"').filter(iataPkt));
  assert(!PF.compile('iata == "LAX"').filter(iataPkt));
});
test('observer_iata in ("SJC","SFO") matches both', () => {
  assert(PF.compile('observer_iata in ("SJC","SFO")').filter(iataPkt));
  assert(PF.compile('observer_iata in ("SJC","SFO")').filter(sfoPkt));
});
test('iata in ("LAX","OAK") does not match SJC', () => {
  assert(!PF.compile('iata in ("LAX","OAK")').filter(iataPkt));
});
test('observer_iata contains "S"', () => {
  assert(PF.compile('observer_iata contains "S"').filter(iataPkt));
  assert(!PF.compile('observer_iata contains "Z"').filter(iataPkt));
});
test('missing observer_iata → no match (not parse error)', () => {
  const c = PF.compile('observer_iata == "SJC"');
  assert(c.error === null, 'should parse with no error');
  assert(!c.filter(noIataPkt), 'should not match when iata absent');
});
test('combined: type == ADVERT && iata == "SJC"', () => {
  const advIataPkt = { ...iataPkt, payload_type: 4 };
  assert(PF.compile('type == ADVERT && iata == "SJC"').filter(advIataPkt));
});
test('observer_iata and iata appear in suggest field list', () => {
  const names = PF.FIELDS.map(f => f.name);
  assert(names.indexOf('observer_iata') !== -1, 'observer_iata in FIELDS');
  assert(names.indexOf('iata') !== -1, 'iata in FIELDS');
});

// --- Issue #1774: payload.destHash / payload.srcHash filterable + in suggestions ---
const reqPkt = {
  route_type: 1, payload_type: 0, // REQ
  hash: 'deadbeef', raw_hex: '0000',
  decoded_json: JSON.stringify({ destHash: '2f', srcHash: '0b' }),
};
test('#1774: payload.destHash filters REQ by destination hash byte', () => {
  assert(PF.compile('payload.destHash == "2f"').filter(reqPkt));
  assert(!PF.compile('payload.destHash == "ff"').filter(reqPkt));
});
test('#1774: payload.srcHash filters REQ by source hash byte', () => {
  assert(PF.compile('payload.srcHash == "0b"').filter(reqPkt));
  assert(!PF.compile('payload.srcHash == "ff"').filter(reqPkt));
});
test('#1774: payload.destHash listed in FIELDS suggestions', () => {
  const names = PF.FIELDS.map(f => f.name);
  assert(names.indexOf('payload.destHash') !== -1, 'payload.destHash missing from FIELDS');
});
test('#1774: payload.srcHash listed in FIELDS suggestions', () => {
  const names = PF.FIELDS.map(f => f.name);
  assert(names.indexOf('payload.srcHash') !== -1, 'payload.srcHash missing from FIELDS');
});

// --- Issue #1800: routed_through field + path desc + hex lexer error ---
const routedPkt = {
  ...pkt,
  resolved_path: '["2f0b001247a047cabcdef0123456789a","aabbccddeeff00112233445566778899"]',
};
const routedArrPkt = {
  ...pkt,
  resolved_path: ['2f0b001247a047cabcdef0123456789a', 'aabbccddeeff00112233445566778899'],
};
test('#1800: routed_through starts_with prefix matches', () => {
  assert(PF.compile('routed_through starts_with "2f0b00"').filter(routedPkt));
});
test('#1800: routed_through starts_with prefix matches (array form)', () => {
  assert(PF.compile('routed_through starts_with "2f0b00"').filter(routedArrPkt));
});
test('#1800: routed_through == full pubkey matches', () => {
  assert(PF.compile('routed_through contains "2f0b001247a047cabcdef0123456789a"').filter(routedPkt));
});
test('#1800: routed_through contains "2f0b" matches', () => {
  assert(PF.compile('routed_through contains "2f0b"').filter(routedPkt));
});
test('#1800: routed_through does not match unrelated prefix', () => {
  assert(!PF.compile('routed_through starts_with "deadbe"').filter(routedPkt));
});
test('#1800: bare hex value after path → "Hex value must be quoted" error', () => {
  const c = PF.compile('path 2f0b001247a047ca');
  assert(c.error !== null, 'should have parse/lex error');
  assert(c.error.indexOf('Hex value must be quoted') !== -1,
    'error must mention Hex value must be quoted, got: ' + c.error);
});
test('#1800 regression: path contains "a3" still matches existing prefix list', () => {
  const prefixPkt = { ...pkt, path_json: '["a3","7f"]' };
  assert(PF.compile('path contains "a3"').filter(prefixPkt));
});
test('#1800: routed_through listed in FIELDS suggestions', () => {
  const names = PF.FIELDS.map(f => f.name);
  assert(names.indexOf('routed_through') !== -1, 'routed_through missing from FIELDS');
});
test('#1800: path_prefixes alias resolves like path', () => {
  const prefixPkt = { ...pkt, path_json: '["a3","7f"]' };
  assert(PF.compile('path_prefixes contains "a3"').filter(prefixPkt));
});

console.log(`\n=== Results: ${pass} passed, ${fail} failed ===`);
process.exit(fail > 0 ? 1 : 0);
