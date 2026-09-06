/**
 * Follow-up UX round 2 to channels (post #1040):
 *
 *   1. Channel header (selected-channel title) must NOT display the raw
 *      "psk:<hex8>" key prefix. Use the user-supplied label when present,
 *      otherwise fall back to "Private Channel".
 *   2. Sidebar share button uses a recognizable label ("📤 Share" or
 *      similar), not the bare ⤴ glyph.
 *   3. ✕ remove button has a red background, white text, proper button
 *      styling — looks like a destructive action.
 */
'use strict';

const fs = require('fs');
const path = require('path');

const chSrc = fs.readFileSync(path.join(__dirname, 'public/channels.js'), 'utf8');
const cssSrc = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

console.log('\n=== Fix 1: header display name for PSK channels ===');
// Behavior test: extract channelDisplayName helper and exercise it.
const vm = require('vm');
function extractFn(src, header) {
  const start = src.indexOf(header);
  if (start < 0) return null;
  let depth = 0, i = src.indexOf('{', start);
  if (i < 0) return null;
  for (let j = i; j < src.length; j++) {
    const c = src[j];
    if (c === '{') depth++;
    else if (c === '}') { depth--; if (depth === 0) return src.substring(start, j + 1); }
  }
  return null;
}
const helperSrc = extractFn(chSrc, 'function channelDisplayName(ch');
assert(helperSrc, 'channelDisplayName helper exists');
if (helperSrc) {
  const sandbox = { formatHashHex: h => h, PRIVATE_CHANNEL_LABEL: 'Private Channel' };
  vm.createContext(sandbox);
  vm.runInContext('const PRIVATE_CHANNEL_LABEL = "Private Channel";\n' + helperSrc, sandbox);
  assert(sandbox.channelDisplayName({ name: 'psk:372a9c93', userLabel: 'My Crew' }) === 'My Crew',
    'psk:* with userLabel returns the userLabel');
  assert(sandbox.channelDisplayName({ name: 'psk:372a9c93' }) === 'Private Channel',
    'psk:* without label returns "Private Channel"');
  assert(sandbox.channelDisplayName({ name: '#meshcore' }) === '#meshcore',
    'non-PSK names pass through unchanged');
  assert(sandbox.channelDisplayName({ hash: 'abc', name: '' }) === 'Channel abc',
    'falls back to "Channel <hash>" when name missing');
  assert(sandbox.channelDisplayName({ hash: 'abc', name: '' }, 'Unknown') === 'Unknown',
    'caller-supplied fallback overrides "Channel <hash>" default');
  assert(sandbox.channelDisplayName({ name: 'psk:abc' }, 'Unknown') === 'Private Channel',
    'fallback does NOT override the psk:* → "Private Channel" rule');
}
// Source-level: header rendering must call channelDisplayName, not raw ch.name.
assert(/channelDisplayName\(ch\)/.test(chSrc),
  'selectChannel header rendering uses channelDisplayName(ch)');

console.log('\n=== Fix 2: share button has recognizable label ===');
assert(!/'⤴'/.test(chSrc) && !/"⤴"/.test(chSrc),
  'bare ⤴ glyph no longer used as the share button content');
// Assert the Phosphor icon and visible Share text are passed together
// into the iconBtn(...) call for ch-share-btn — this catches the
// case where someone removes the icon from the button content but leaves
// "Share" in an aria-label or title.
assert(/iconBtn\(\s*'ch-share-btn'[^)]*#ph-share-network[^)]*<\/svg> Share'/.test(chSrc),
  "share button content includes the Phosphor share icon and visible Share label");

console.log('\n=== Fix 3: ✕ delete button is a visibly red destructive button ===');
const removeRule = (cssSrc.match(/\.ch-remove-btn\s*\{[^}]*\}/) || [''])[0];
assert(/background:\s*var\(--statusRed/.test(removeRule) || /background:\s*#b54a4a/.test(removeRule),
  '.ch-remove-btn has red background (var(--statusRed,...) or #b54a4a)');
assert(/color:\s*white/.test(removeRule) || /color:\s*#fff/.test(removeRule),
  '.ch-remove-btn has white text');
assert(/border-radius:/.test(removeRule),
  '.ch-remove-btn has border-radius (button shape)');
assert(/font-weight:\s*bold|font-weight:\s*700/.test(removeRule),
  '.ch-remove-btn has bold font-weight');

console.log('\n=== Results ===');
console.log('Passed: ' + passed + ', Failed: ' + failed);
process.exit(failed > 0 ? 1 : 0);
