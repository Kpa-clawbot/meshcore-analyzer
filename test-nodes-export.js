'use strict';
// Unit test for nodes-export.js — the MeshCore companion "contacts" JSON export
// of the visible node list. Loads the browser IIFE in a vm sandbox (pattern from
// test-node-reach-coverage.js) and exercises the pure mapping helpers.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(__dirname, 'public', 'nodes-export.js'), 'utf8');
const sandbox = { window: {}, document: {} };
vm.createContext(sandbox);
vm.runInContext(code, sandbox);

const { buildContacts, filename } = sandbox.window.NodesExport;

// ─── Field mapping ───────────────────────────────────────────────────────────
// The companion app reads a fixed key set; emitting extra/renamed keys makes the
// import silently drop contacts, so both the keys AND their order are asserted.
const REF_KEYS = ['type', 'name', 'custom_name', 'public_key', 'flags', 'latitude',
  'longitude', 'last_advert', 'last_modified', 'out_path_list'];

const PK = 'efef7943505052b47f1809488ea4b4d3942d4ed72d2b1953b90a9f5e62a65fb5';
const repeater = {
  public_key: PK, name: 'BE-BRE-ON8AR🔋', role: 'repeater',
  lat: 51.137798, lon: 5.590199, last_seen: '2026-05-14T09:25:43Z',
};

const out = buildContacts([repeater]);
assert.deepStrictEqual(Object.keys(out), ['contacts'], 'top level is a single contacts array');
assert.strictEqual(out.contacts.length, 1);
const c = out.contacts[0];
assert.deepStrictEqual(Object.keys(c), REF_KEYS, 'contact keys must match the companion format, in order');
assert.strictEqual(c.type, 2, 'repeater → type 2');
assert.strictEqual(c.name, 'BE-BRE-ON8AR🔋', 'name passes through unchanged, emoji included');
assert.strictEqual(c.custom_name, null);
assert.strictEqual(c.public_key, PK);
assert.strictEqual(c.flags, 0);
assert.strictEqual(c.latitude, '51.137798', 'latitude is a string');
assert.strictEqual(c.longitude, '5.590199', 'longitude is a string');
assert.strictEqual(c.last_advert, 1778750743, 'last_seen → unix seconds');
assert.strictEqual(c.last_modified, 1778750743, 'last_modified mirrors last_advert');
assert.strictEqual(c.out_path_list, null);

// ─── Role → type ─────────────────────────────────────────────────────────────
function typeOf(role) {
  return buildContacts([Object.assign({}, repeater, { role: role })]).contacts[0].type;
}
assert.strictEqual(typeOf('repeater'), 2, 'repeater → 2');
assert.strictEqual(typeOf('companion'), 1, 'companion → 1');
assert.strictEqual(typeOf('room'), 3, 'room → 3');
assert.strictEqual(typeOf('sensor'), 4, 'sensor → 4');
assert.strictEqual(typeOf('Repeater'), 2, 'role match is case-insensitive');
assert.strictEqual(typeOf('observer'), 1, 'unknown role → 1');
assert.strictEqual(typeOf(null), 1, 'missing role → 1');

// ─── Skip rules ──────────────────────────────────────────────────────────────
function kept(patch) {
  return buildContacts([Object.assign({}, repeater, patch)]).contacts.length;
}
assert.strictEqual(kept({ name: null }), 0, 'no name → skipped');
assert.strictEqual(kept({ name: '   ' }), 0, 'blank name → skipped');
assert.strictEqual(kept({ public_key: 'efef7943' }), 0, 'short pubkey → skipped');
assert.strictEqual(kept({ lat: null }), 0, 'missing lat → skipped');
assert.strictEqual(kept({ lon: undefined }), 0, 'missing lon → skipped');
assert.strictEqual(kept({ lat: 0, lon: 0 }), 0, 'null island → skipped');
assert.strictEqual(kept({ lat: '0', lon: '0' }), 0, 'null island as strings → skipped');
assert.strictEqual(kept({ lat: 0, lon: 5.59 }), 1, 'lat 0 with a real lon is a valid position');
assert.strictEqual(kept({ lat: 'n/a' }), 0, 'non-numeric lat → skipped');
assert.strictEqual(kept({ last_seen: null }), 1, 'a node without last_seen is still exported');
assert.strictEqual(
  buildContacts([Object.assign({}, repeater, { last_seen: null })]).contacts[0].last_advert, 0,
  'missing last_seen → last_advert 0');
assert.strictEqual(buildContacts([]).contacts.length, 0, 'empty input → empty contacts');
assert.strictEqual(buildContacts(null).contacts.length, 0, 'null input → empty contacts');

// Order is preserved: the export is WYSIWYG w.r.t. the table's current sort.
const two = buildContacts([
  Object.assign({}, repeater, { name: 'first' }),
  Object.assign({}, repeater, { name: 'second', public_key: PK.replace(/^efef/, 'aaaa') }),
]);
// Joined, not deepStrictEqual: arrays built inside the vm sandbox have a
// different Array prototype and would fail the realm check.
assert.strictEqual(two.contacts.map(function (x) { return x.name; }).join(','), 'first,second',
  'input order is preserved');

// ─── Filename ────────────────────────────────────────────────────────────────
const d = new Date(2026, 7, 12, 16, 5, 17); // local time, 2026-08-12 16:05:17
assert.strictEqual(filename('BE-LIM', d), 'corescope_nodes_BE-LIM_2026-08-12-160517.json');
assert.strictEqual(filename(null, d), 'corescope_nodes_all_2026-08-12-160517.json',
  'no area selected → "all"');
assert.strictEqual(filename('', d), 'corescope_nodes_all_2026-08-12-160517.json');
assert.strictEqual(filename('NL / Zuid', d), 'corescope_nodes_NL_Zuid_2026-08-12-160517.json',
  'unsafe filename chars are collapsed to underscores');

console.log('nodes-export mapping, skip rules and filename OK');
