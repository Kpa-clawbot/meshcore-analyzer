'use strict';
// Unit test for #14: the mobile RX coverage leaderboard must HTML-escape the
// pubkey it interpolates into the row markup (data-rx="..." and the truncated
// fallback label), not only the name. A no-ACL broker / pre-validation rows
// could carry a non-hex pubkey, and the rest of the row is built by string
// concatenation, so an unescaped pubkey is an HTML-injection vector.
//
// Like test-coverage-gate.js we slice the real row-building expression out of
// public/rx-coverage.js and evaluate it in a vm sandbox — no hand-copied
// duplicate — so the test tracks the actual source.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const src = fs.readFileSync(path.join(__dirname, 'public', 'rx-coverage.js'), 'utf8');

// Slice from `var nm = o.name ...` through the end of the returned row string.
const startMarker = 'var nm = o.name ? escapeHtml(o.name)';
const endMarker = "}).join('');";
const startIdx = src.indexOf(startMarker);
assert.ok(startIdx >= 0, 'could not locate row-builder start in rx-coverage.js');
const endIdx = src.indexOf(endMarker, startIdx);
assert.ok(endIdx >= 0, 'could not locate row-builder end in rx-coverage.js');
const block = src.slice(startIdx, endIdx);

// Canonical escapeHtml (public/app.js).
function escapeHtml(s) {
  if (s == null) return '';
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function renderRow(o) {
  const sandbox = { o: o, i: 0, selectedRx: '', escapeHtml: escapeHtml };
  vm.createContext(sandbox);
  return vm.runInContext('(function () { ' + block + ' })()', sandbox);
}

// Malicious pubkey that would break out of the data-rx attribute and inject a
// tag if interpolated raw. With escaping, no raw '<', '>' or attribute-closing
// '"' survives.
const evil = '"><img src=x onerror=alert(1)>';

// Case 1: no name → pubkey used as the visible label fallback too.
const row1 = renderRow({ pubkey: evil, name: '', receptions: 1, nodes: 1 });
assert.ok(row1.indexOf('<img') === -1, 'raw <img must not appear in row (label fallback): ' + row1);
assert.ok(row1.indexOf('data-rx="' + evil + '"') === -1, 'raw pubkey must not appear unescaped in data-rx');
assert.ok(row1.indexOf('&lt;img') !== -1 || row1.indexOf('&quot;&gt;') !== -1, 'pubkey should be HTML-escaped: ' + row1);

// Case 2: name present → label is the (escaped) name, but data-rx still carries
// the pubkey and must be escaped.
const row2 = renderRow({ pubkey: evil, name: 'Mob', receptions: 2, nodes: 3 });
assert.ok(row2.indexOf('<img') === -1, 'raw <img must not appear in row (named): ' + row2);

// #a11y: the clickable row must be keyboard-operable (role/tabindex) and expose
// pressed state, so it isn't a mouse-only <div>.
assert.ok(/role="button"/.test(row2), 'row must have role="button"');
assert.ok(/tabindex="0"/.test(row2), 'row must be focusable (tabindex)');
assert.ok(/aria-pressed="(true|false)"/.test(row2), 'row must expose aria-pressed');

console.log('rx-coverage pubkey escaping + row a11y OK');
