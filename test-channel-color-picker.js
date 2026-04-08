/**
 * Tests for channel color picker fix (#674)
 *
 * Verifies:
 * 1. _ccChannel is set correctly for GRP_TXT packets (flat decoded structure)
 * 2. _ccChannel is NOT set for non-GRP_TXT packets
 * 3. Channel color picker palette is 8 colors
 * 4. getRowStyle uses border-left only (no background tint)
 */

'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');

let passed = 0;
let failed = 0;

function assert(condition, msg) {
  if (condition) {
    passed++;
    console.log(`  ✓ ${msg}`);
  } else {
    failed++;
    console.error(`  ✗ ${msg}`);
  }
}

// --- Test 1: _ccChannel extraction logic (simulates live.js behavior) ---
console.log('\n=== _ccChannel assignment from flat decoded structure ===');

// Simulate the fixed logic from live.js — uses payload.channel (name string),
// NOT payload.channelHash (numeric byte). Channel colors are keyed by channel
// name (e.g. "public", "#test") matching the channels API hash field.
function extractCcChannel(typeName, pkt) {
  var _ccPayload = (pkt.decoded || {}).payload || {};
  if (typeName === 'GRP_TXT' || typeName === 'CHAN') {
    return _ccPayload.channel || null;
  }
  return undefined; // not set
}

// CHAN with channel name (normal case — ingestor-decrypted WS broadcast)
var chanPkt = {
  decoded: {
    header: { payloadTypeName: 'CHAN' },
    payload: { type: 'CHAN', channel: '#test', channelHash: 217, text: 'hello' }
  }
};
assert(extractCcChannel('CHAN', chanPkt) === '#test', 'CHAN with channel="#test" → _ccChannel="#test"');

// CHAN with "public" channel
var publicPkt = {
  decoded: {
    header: { payloadTypeName: 'CHAN' },
    payload: { type: 'CHAN', channel: 'public', text: 'hi' }
  }
};
assert(extractCcChannel('CHAN', publicPkt) === 'public', 'CHAN with channel="public" → _ccChannel="public"');

// GRP_TXT without channel (encrypted, no decryption)
var encryptedPkt = {
  decoded: {
    header: { payloadTypeName: 'GRP_TXT' },
    payload: { type: 'GRP_TXT', channelHash: 5, mac: 'ab12', encryptedData: 'ff' }
  }
};
assert(extractCcChannel('GRP_TXT', encryptedPkt) === null, 'GRP_TXT without channel field → null');

// Non-GRP_TXT packet — should not set _ccChannel
var advertPkt = {
  decoded: {
    header: { payloadTypeName: 'ADVERT' },
    payload: { type: 'ADVERT', name: 'Node1' }
  }
};
assert(extractCcChannel('ADVERT', advertPkt) === undefined, 'ADVERT → _ccChannel not set');

// Empty decoded
var emptyPkt = { decoded: {} };
assert(extractCcChannel('GRP_TXT', emptyPkt) === null, 'GRP_TXT with empty payload → null');

// --- Test 2: _getChannelStyle fix (simulates fixed logic) ---
console.log('\n=== _getChannelStyle with flat structure ===');

function simulateGetChannelStyle(pkt, channelColors) {
  var d = pkt.decoded || {};
  var h = d.header || {};
  var p = d.payload || {};
  var ch = p.channel || null;
  var typeName = h.payloadTypeName || '';
  if (typeName !== 'GRP_TXT' && typeName !== 'CHAN') return '';
  if (!ch) return '';
  var color = channelColors[ch] || null;
  if (!color) return '';
  return 'border-left:3px solid ' + color + ';';
}

var colors = { '#test': '#ef4444' };
assert(
  simulateGetChannelStyle(chanPkt, colors) === 'border-left:3px solid #ef4444;',
  'getChannelStyle returns border-left for assigned color'
);
assert(
  simulateGetChannelStyle(chanPkt, {}) === '',
  'getChannelStyle returns empty for unassigned channel'
);
assert(
  simulateGetChannelStyle(advertPkt, colors) === '',
  'getChannelStyle returns empty for non-GRP_TXT'
);

// --- Test 3: channel-colors.js getRowStyle uses border-left only ---
console.log('\n=== channel-colors.js getRowStyle ===');

const ccSource = fs.readFileSync(path.join(__dirname, 'public', 'channel-colors.js'), 'utf8');
const ccCtx = {
  window: {},
  localStorage: {
    _data: {},
    getItem(k) { return this._data[k] || null; },
    setItem(k, v) { this._data[k] = v; }
  }
};
vm.createContext(ccCtx);
vm.runInContext(ccSource, ccCtx);

// Set a color
ccCtx.window.ChannelColors.set('5', '#3b82f6');
var style = ccCtx.window.ChannelColors.getRowStyle('GRP_TXT', '5');
assert(style === 'border-left:3px solid #3b82f6;', 'getRowStyle returns border-left:3px (no background tint)');
assert(!style.includes('background'), 'getRowStyle has no background property');

var noStyle = ccCtx.window.ChannelColors.getRowStyle('GRP_TXT', '99');
assert(noStyle === '', 'getRowStyle returns empty for unassigned channel');

var advertStyle = ccCtx.window.ChannelColors.getRowStyle('ADVERT', '5');
assert(advertStyle === '', 'getRowStyle returns empty for non-GRP_TXT type');

// --- Test 4: channel-color-picker.js palette ---
console.log('\n=== channel-color-picker.js palette ===');

const pickerSource = fs.readFileSync(path.join(__dirname, 'public', 'channel-color-picker.js'), 'utf8');
const pickerCtx = {
  window: { ChannelColors: ccCtx.window.ChannelColors, matchMedia: () => ({ matches: false }) },
  document: {
    createElement: () => ({
      className: '', style: {}, innerHTML: '',
      setAttribute: () => {},
      querySelector: () => ({ textContent: '', style: {}, addEventListener: () => {} }),
      querySelectorAll: () => [],
      appendChild: () => {},
      addEventListener: () => {}
    }),
    body: { appendChild: () => {}, style: {} },
    addEventListener: () => {},
    removeEventListener: () => {},
    activeElement: null
  },
  setTimeout: (fn) => fn(),
  Array: Array
};
vm.createContext(pickerCtx);
vm.runInContext(pickerSource, pickerCtx);

assert(pickerCtx.window.ChannelColorPicker != null, 'ChannelColorPicker exported');
assert(Array.isArray(pickerCtx.window.ChannelColorPicker.PALETTE), 'PALETTE is exported');
assert(pickerCtx.window.ChannelColorPicker.PALETTE.length === 8, 'PALETTE has exactly 8 colors');

// Verify no teal/rose in palette
var palette = pickerCtx.window.ChannelColorPicker.PALETTE;
assert(!palette.includes('#14b8a6'), 'No teal in palette');
assert(!palette.includes('#f43f5e'), 'No rose in palette');

// --- Summary ---
console.log(`\n${passed + failed} tests: ${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);
