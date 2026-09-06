/**
 * Analytics → Channels section integration with PSK decrypt UX.
 *
 * Bug: the analytics channels list shows nonsense names like "ch185" for
 *      every encrypted channel and ignores the user's locally-decrypted PSK
 *      channels (from ChannelDecrypt.getStoredKeys() + label store).
 *
 * Fix:
 *   1. Replace "chNNN" raw names with "🔒 Encrypted (0xNN)" when the channel
 *      is encrypted and the server only knows its hash byte.
 *   2. For channels matching a locally-stored PSK key, show the user's
 *      label / key-name instead of the hash-byte placeholder.
 *   3. Group rendering: My Channels → Network → Encrypted, each sorted by
 *      message count descending.
 *   4. Add a link from the Channels page to the Analytics page so users can
 *      jump to channel activity stats.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

// ── Set up a tiny browser-ish global so analytics.js loads cleanly ──────────
global.window = global;
global.document = {
  documentElement: {},
  createElement: () => ({ style: {}, addEventListener() {} }),
  addEventListener() {},
  removeEventListener() {},
  querySelector: () => null,
  querySelectorAll: () => [],
  getElementById: () => null,
};
global.localStorage = {
  _s: {},
  getItem(k) { return this._s[k] || null; },
  setItem(k, v) { this._s[k] = String(v); },
  removeItem(k) { delete this._s[k]; },
};
global.getComputedStyle = () => ({ getPropertyValue: () => '' });
global.registerPage = () => {};
global.api = async () => ({});
global.fetch = async () => ({ ok: true, json: async () => ({}) });
global.CLIENT_TTL = {};
global.RegionFilter = { getRegionParam: () => '' };
global.Storage = function () {};
global.timeAgo = () => '';
global.histogram = () => ({ svg: '' });

// Load analytics.js — it self-registers global helpers we test.
const analyticsSrc = fs.readFileSync(
  path.join(__dirname, 'public/analytics.js'),
  'utf8'
);
// Strip top-level `await` / module syntax — analytics.js is plain IIFE so it's
// fine to eval as-is.
// eslint-disable-next-line no-eval
eval(analyticsSrc); // sets window._analyticsDecorateChannels etc.

console.log('\n=== Analytics channels: decorate with PSK keys ===');

const decorate = global._analyticsDecorateChannels;
assert(typeof decorate === 'function',
  '_analyticsDecorateChannels exposed for testing');

// Server response sample — mix of cleartext, rainbow-known encrypted, raw "chNNN".
const sampleChannels = [
  { hash: 17,  name: 'public',           messages: 100, senders: 5,  encrypted: false },
  { hash: 217, name: '#test',            messages: 200, senders: 8,  encrypted: false },
  { hash: 185, name: 'ch185',            messages: 50,  senders: 0,  encrypted: true  },
  { hash: 64,  name: 'ch64',             messages: 300, senders: 0,  encrypted: true  },
  { hash: 30,  name: 'ch30',             messages: 75,  senders: 0,  encrypted: true  },
  { hash: 99,  name: '#earthquake',      messages: 10,  senders: 1,  encrypted: false },
  // Rainbow-table hit on an ENCRYPTED channel: server resolved a real name.
  { hash: 12,  name: 'public-meshcore',  messages: 40,  senders: 2,  encrypted: true  },
  // Encrypted channel with empty name — must not render an empty <strong>.
  { hash: 200, name: '',                 messages: 5,   senders: 0,  encrypted: true  },
];

// User has two PSK keys locally: one matches hash=185 (named "Levski"),
// one matches hash=30 (named "secret-room", with label "Garage").
const myKeyHashToName = { 185: 'Levski', 30: 'secret-room' };
const labels = { 'secret-room': 'Garage' };

const out = decorate(sampleChannels, myKeyHashToName, labels);
assert(Array.isArray(out), 'decorate returns an array');
assert(out.length === sampleChannels.length, 'decorate keeps every channel');

// Find by original hash (and optionally original name) for assertions.
// Decoration preserves c.name as-is and writes the user-facing string to
// c.displayName, so matching on c.name is unambiguous.
function find(hash, name) {
  return out.find(c => c.hash === hash && (name == null || c.name === name));
}

const mine185 = find(185, 'ch185');
assert(mine185 && mine185.displayName === 'Levski',
  'hash 185 + stored key → displayName = "Levski" (not "ch185")');
assert(mine185 && mine185.group === 'mine',
  'hash 185 grouped as "mine"');

const mine30 = find(30, 'ch30');
assert(mine30 && mine30.displayName === 'Garage',
  'hash 30 with stored key + label → displayName = "Garage" (label wins)');
assert(mine30 && mine30.group === 'mine', 'hash 30 grouped as "mine"');

const ch64 = find(64, 'ch64');
assert(ch64 && ch64.displayName === 'Encrypted (0x40)',
  'unknown encrypted ch64 → "Encrypted (0x40)" (no nonsense "ch64")');
assert(ch64 && ch64.group === 'encrypted', 'unknown encrypted grouped as "encrypted"');

const pub = find(17, 'public');
assert(pub && pub.displayName === 'public', 'cleartext public name preserved');
assert(pub && pub.group === 'network', 'cleartext public grouped as "network"');

const test = find(217, '#test');
assert(test && test.group === 'network', 'rainbow-known #test grouped as "network"');

// Rainbow-table hit on an ENCRYPTED channel — actually exercises the
// "encrypted but server has the real name" branch (was previously dead-untested).
const rainbow = find(12, 'public-meshcore');
assert(rainbow && rainbow.encrypted === true,
  'rainbow row preserves encrypted=true');
assert(rainbow && rainbow.displayName === 'public-meshcore',
  'rainbow-decoded encrypted row → displayName = real name');
assert(rainbow && rainbow.group === 'network',
  'rainbow-decoded encrypted row → group = "network"');

// Empty-name encrypted: must NOT leak through with displayName = ''.
const empty = find(200, '');
assert(empty && empty.displayName === 'Encrypted (0xC8)',
  'encrypted with empty name → render as opaque encrypted placeholder');
assert(empty && empty.group === 'encrypted',
  'encrypted with empty name → group = "encrypted"');

// No "chNNN" leaks into displayName for any row.
const leak = out.find(c => /^ch(\d+|\?)$/.test(c.displayName));
assert(!leak, 'no displayName matches the raw chNNN placeholder');

console.log('\n=== Grouped table render: order + sort ===');

const tbody = global._analyticsChannelTbodyHtml(out, 'messages', 'desc', {
  grouped: true,
});
assert(typeof tbody === 'string' && tbody.length > 0,
  'channelTbodyHtml accepts grouped option and returns html');

// Group headers must appear in order: My Channels, Network, Encrypted.
const iMine = tbody.indexOf('My Channels');
const iNet  = tbody.indexOf('Network');
const iEnc  = tbody.indexOf('Encrypted');
assert(iMine >= 0 && iNet > iMine && iEnc > iNet,
  'group headers render in order: My Channels → Network → Encrypted');

// Within "mine" section, hash=30 (75 msgs) > hash=185 (50 msgs).
const i30  = tbody.indexOf('Garage');
const i185 = tbody.indexOf('Levski');
assert(i30 > 0 && i185 > i30,
  'within "My Channels" sort by messages desc (Garage 75 before Levski 50)');

// Within "network" section, #test (200) > public (100) > #earthquake (10).
const iT = tbody.indexOf('#test');
const iP = tbody.indexOf('public');
const iE = tbody.indexOf('#earthquake');
assert(iT > 0 && iP > iT && iE > iP,
  'within "Network" sort by messages desc (#test → public → #earthquake)');

// Within "encrypted" section, ch64 (300 msgs) appears (only one entry).
assert(tbody.indexOf('0x40') > iEnc, 'encrypted section contains 0x40');

// The "Channels page links to Analytics" assertion that used to live here was
// removed: #1367 / PR #1376 ("channels page chat-app redesign — restore prod row
// layout, drop analytics chip") deliberately deleted the `#/analytics` chip from
// public/channels.js. The assertion could not pass again without reinstating a
// feature that was removed on purpose. It survived because nothing runs
// test-all.sh in CI (#1858).

console.log('\n' + (failed ? '✗ ' + failed + ' failed, ' : '') + passed + ' passed');
process.exit(failed ? 1 : 0);
