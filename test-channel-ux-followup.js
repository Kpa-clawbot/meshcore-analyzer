/**
 * Follow-up UX fixes to #1037 channel modal/sidebar redesign:
 *
 *   1. ✕ remove button must hit a 44×44px touch target (WCAG 2.5.5).
 *   2. Channel rows must NOT display "0 messages" — when no messages
 *      have been decrypted yet, omit the count entirely.
 *   3. Modal footer wording: keys removed via ✕ button, not by
 *      clearing browser data.
 *   4. Each user-added (PSK) row must expose a Share affordance that
 *      re-opens the QR/key for that channel without re-generating it.
 *   5. "(your key)" preview suffix on user-added rows is noise; drop it.
 *      Likewise no key hex in the default row rendering.
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

console.log('\n=== Fix 1: ✕ touch target ≥ 44×44px (on shared .ch-icon-btn base) ===');
const iconBtnRule = (cssSrc.match(/\.ch-icon-btn\s*\{[^}]*\}/) || [''])[0];
assert(/min-width:\s*44px/.test(iconBtnRule),
  '.ch-icon-btn declares min-width: 44px');
assert(/min-height:\s*44px/.test(iconBtnRule),
  '.ch-icon-btn declares min-height: 44px');

console.log('\n=== Fix 2: no "0 messages" in default row ===');
// renderChannelRow must not emit a literal "0 messages" preview when
// messageCount is missing/zero. Look for the offending fallback pattern.
assert(!/\$\{ch\.messageCount\s*\|\|\s*0\}\s*messages/.test(chSrc),
  'preview no longer falls back to "${ch.messageCount || 0} messages"');
assert(!/\$\{ch\.messageCount\s*\|\|\s*0\}\s*packets/.test(chSrc),
  'encrypted preview no longer falls back to "${ch.messageCount || 0} packets"');

console.log('\n=== Fix 3: privacy footer wording ===');
assert(!/Clear browser data to remove stored keys/.test(chSrc),
  'old "Clear browser data to remove stored keys" copy is gone');
assert(/Use the close button to remove individual channels/.test(chSrc),
  'copy names the close button for individual key removal');

console.log('\n=== Fix 4: Share/reshare affordance on user-added rows ===');
// Source-level: data attribute and helper exist. Behavior-level checks
// against rendered output are below in the renderChannelRow section.
assert(/data-share-channel/.test(chSrc),
  'channels.js wires the data-share-channel hook somewhere in render');
// Click handler must wire the share button to ChannelQR.generate (or a
// QR-display fallback). The handler lives in the chListEl click delegation.
assert(/data-share-channel/.test(chSrc) && /ChannelQR/.test(chSrc),
  'share handler references ChannelQR for QR rendering');
// Modal must have a target container for the reshare QR output.
// (#1087 polish) The share UX moved to a DEDICATED modal —
// `#chShareModal` with `#chShareQr` — so the dead `chShareSection` /
// `chShareOutput` markup was removed from the Add Channel modal.
assert(/id="chShareModal"/.test(chSrc) && /id="chShareQr"/.test(chSrc),
  'dedicated share modal exposes a QR output container');

console.log('\n=== Fix 5: "(your key)" suffix removed from preview ===');
assert(!/\(your key\)/.test(chSrc),
  'user-added preview no longer says "(your key)"');

console.log('\n=== Fix 6: browser-local warning is obvious ===');
// A visible callout in the modal — separate from the small privacy footer.
assert(/class="ch-modal-callout"/.test(chSrc),
  'modal has a dedicated .ch-modal-callout for the locality warning');
assert(/THIS browser only/.test(chSrc),
  'callout uses emphatic copy: "Channels are saved to THIS browser only"');
assert(/won't appear on other devices or browsers|won.t appear on other devices/.test(chSrc),
  'callout warns that channels won\u2019t appear on other devices/browsers');

// Sidebar "My Channels" section header gets a locality marker.
assert(/My Channels[\s\S]{0,200}\(this browser\)|🖥️[\s\S]{0,200}My Channels|My Channels[\s\S]{0,200}🖥️/.test(chSrc),
  'My Channels section header reinforces locality (🖥️ or "(this browser)")');

// Remove confirm prompt explicitly mentions "this browser".
assert(/permanently remove the key from this browser/.test(chSrc),
  'remove confirm says key is permanently removed from this browser');

console.log('\n=== Fix 7: default channel reference is #meshcore, not #LongFast ===');
// Channels UI must not reference Meshtastic's LongFast as the example
// channel — meshcore network's analogous default is #meshcore.
assert(!/LongFast/.test(chSrc),
  'public/channels.js has no "LongFast" references');
assert(/#meshcore/.test(chSrc),
  'public/channels.js uses #meshcore as the example/placeholder');

console.log('\n=== Behavior: renderChannelRow output structure ===');
// Extract renderChannelRow and exercise it against synthetic ch records
// to assert behavior (not just source substring presence).
const vm = require('vm');
// Locate renderChannelRow source by walking braces from the function header.
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
const renderRowSrc = extractFn(chSrc, 'function renderChannelRow(ch)');
assert(renderRowSrc, 'extracted renderChannelRow source for behavior testing');
if (renderRowSrc) {
  // Stub the helpers renderChannelRow depends on, evaluate it in a sandbox.
  const sandbox = {
    escapeHtml: s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({
      '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'
    }[c])),
    truncate: (s, n) => (s && s.length > n ? s.substring(0, n) + '…' : s || ''),
    formatSecondsAgo: () => '5m',
    formatHashHex: h => h,
    getChannelColor: () => '#fff',
    selectedHash: null,
    customColors: {},
    window: {},
    renderChannelRow: null,
  };
  // #1041 follow-up: renderChannelRow now delegates to channelDisplayName.
  // Eval the REAL helper (and its module-local PRIVATE_CHANNEL_LABEL)
  // into the sandbox so this test stays in sync with production behavior
  // automatically — no hand-rolled duplicate of the psk:* rule.
  const helperSrc = extractFn(chSrc, 'function channelDisplayName(ch');
  assert(helperSrc, 'extracted channelDisplayName source for behavior sandbox');
  vm.createContext(sandbox);
  vm.runInContext('const PRIVATE_CHANNEL_LABEL = "Private Channel";\n' + helperSrc, sandbox);
  vm.runInContext(renderRowSrc, sandbox);
  const userRow = sandbox.renderChannelRow({
    hash: 'user:Crew',
    name: 'Crew',
    userAdded: true,
    encrypted: true,
    messageCount: 0,
    lastActivityMs: Date.now(),
  });
  assert(/data-share-channel="user:Crew"/.test(userRow),
    'renderChannelRow emits a share button for user-added channels');
  assert(/aria-haspopup="dialog"/.test(userRow),
    'share button announces it opens a dialog (aria-haspopup="dialog")');
  assert(/data-remove-channel="user:Crew"/.test(userRow),
    'renderChannelRow emits a remove button for user-added channels');
  assert(!/0 messages/.test(userRow) && !/your key/.test(userRow),
    'user-added preview omits "0 messages" and "your key" when no activity');
  // Non-user-added encrypted row should NOT carry share/remove.
  const encRow = sandbox.renderChannelRow({
    hash: 'abc123', name: 'Net', userAdded: false, encrypted: true,
    messageCount: 0, lastActivityMs: 0,
  });
  assert(!/data-share-channel/.test(encRow),
    'encrypted (non-user) rows do NOT expose a share button');
  assert(!/0 packets/.test(encRow),
    'encrypted preview omits "0 packets" when count is zero');
}

console.log('\n=== Behavior: share output lives in a dedicated, labeled modal ===');
// (#1087 polish) The share UX is no longer a section nested inside the
// Add Channel modal — it is a dedicated dialog (`#chShareModal`) with
// its own labeled title (`aria-labelledby="chShareModalTitle"`).
assert(/id="chShareModal"[\s\S]{0,400}aria-labelledby="chShareModalTitle"/.test(chSrc),
  'dedicated share modal is labeled (chShareModal / chShareModalTitle)');
assert(/role="dialog"[\s\S]{0,200}aria-modal="true"/.test(chSrc),
  'share modal advertises role="dialog" + aria-modal="true"');

console.log('\n=== A11y: locality marker font-size ≥ 11px ===');
const localityRule = (cssSrc.match(/\.ch-section-locality\s*\{[^}]*\}/) || [''])[0];
const sizeMatch = localityRule.match(/font-size:\s*(\d+)px/);
assert(sizeMatch && parseInt(sizeMatch[1], 10) >= 11,
  '.ch-section-locality font-size is ≥ 11px (got: ' + (sizeMatch ? sizeMatch[1] : 'none') + ')');

console.log('\n=== Share handler: no native alert(), uses inline output ===');
// Walk the share-handler region and verify it doesn't drop to alert().
const shareHandlerMatch = chSrc.match(/data-share-channel[\s\S]{0,2000}?return;\n      \}/);
assert(shareHandlerMatch && !/alert\(/.test(shareHandlerMatch[0]),
  'share handler does not use native alert() for missing-key error');

console.log('\n=== Results ===');
console.log('Passed: ' + passed + ', Failed: ' + failed);
process.exit(failed > 0 ? 1 : 0);
