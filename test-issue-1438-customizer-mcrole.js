/**
 * #1438 FINAL — customizer-v2 applyCSS must write --mc-role-{role}
 * alongside the legacy --node-{role} WHEN the value comes from
 * userOverrides (not from server defaults).
 *
 * The earlier closing chain (#1439 marker SVG migration, #1441 body.style
 * tweaks) did NOT extend the per-role write loop. Result:
 *
 *   - Operator opens customizer, picks a custom color per-role. The pick
 *     goes through setRoleColorOverride() (roles.js) which DOES write
 *     --mc-role-X correctly → marker SVGs recolor live. ✅
 *   - Operator reloads the page. customize-v2.js applyCSS replays from
 *     localStorage userOverrides.nodeColors via the
 *     `for (var role in nc) { root.setProperty('--node-' + role, ...) }`
 *     loop. setRoleColorOverride is NOT replayed. Result: --mc-role-X
 *     falls back to preset defaults; marker SVGs revert to preset
 *     colors even though localStorage still holds the user pick. ❌
 *
 * Fix: extend the loop in customize-v2.js applyCSS to write
 * --mc-role-{role} when the role is present in userOverrides.nodeColors.
 * Do NOT write --mc-role-* for keys that only exist in server config
 * (that would re-introduce the #1412 ROLE_COLORS / preset-propagation
 * regression — server-config writes must stay legacy-vars-only).
 *
 * This test runs the extracted block in a vm sandbox so reverting the
 * fix in customize-v2.js breaks it.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const cv2Src     = fs.readFileSync(path.join(__dirname, 'public', 'customize-v2.js'), 'utf8');
const rolesSrc   = fs.readFileSync(path.join(__dirname, 'public', 'roles.js'), 'utf8');
const presetsSrc = fs.readFileSync(path.join(__dirname, 'public', 'cb-presets.js'), 'utf8');
const styleSrc   = fs.readFileSync(path.join(__dirname, 'public', 'style.css'), 'utf8');

// ─── Extract the nodeColors-processing block from customize-v2.js. ───
function extractBlock(src, anchor) {
  const idx = src.indexOf(anchor);
  if (idx === -1) throw new Error('anchor not found: ' + anchor);
  const start = src.indexOf('{', idx);
  if (start === -1) throw new Error('open brace not found after anchor');
  let depth = 0, end = -1;
  for (let i = start; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (depth === 0) { end = i; break; } }
  }
  if (end === -1) throw new Error('matching close brace not found');
  return src.slice(idx, end + 1);
}
const blockA = extractBlock(cv2Src, 'var nc = effectiveConfig.nodeColors;');

function makeSandbox() {
  const root = {
    style: {
      _vars: {},
      setProperty(k, v) { this._vars[k] = String(v); },
      getPropertyValue(k) { return this._vars[k] || ''; },
      removeProperty(k) { delete this._vars[k]; }
    },
    getAttribute() { return null; },
    setAttribute() {}
  };
  const body = {
    _attrs: {},
    setAttribute(k, v) { this._attrs[k] = v; },
    getAttribute(k) { return this._attrs[k] || null; },
    removeAttribute(k) { delete this._attrs[k]; },
    dataset: {},
    style: {
      _vars: {},
      setProperty(k, v) { this._vars[k] = String(v); },
      getPropertyValue(k) { return this._vars[k] || ''; },
      removeProperty(k) { delete this._vars[k]; }
    }
  };
  const sandbox = {
    window: null,
    document: {
      documentElement: root,
      body: body,
      readyState: 'complete',
      getElementById() { return null; },
      createElement() { return { style: {}, setAttribute() {}, appendChild() {} }; },
      head: { appendChild() {} },
      addEventListener() {},
    },
    console: console,
    setTimeout: setTimeout,
    clearTimeout: clearTimeout,
    addEventListener() {},
    dispatchEvent() { return true; },
    fetch: function () { return { then: function () { return { then: function () { return { catch: function () {} }; }, catch: function () {} }; } }; },
    matchMedia: function () { return { matches: false }; },
    CustomEvent: function (type, opts) { this.type = type; this.detail = opts && opts.detail; },
    Event: function (type) { this.type = type; },
    getComputedStyle: function () {
      return { getPropertyValue: function (k) { return (root.style._vars[k] || ''); } };
    }
  };
  sandbox.window = sandbox;
  return { sandbox, root, body };
}

console.log('\n=== #1438 FINAL A: source invariant — --mc-role-{role} write present ===');
assert(/setProperty\(\s*['"]--mc-role-['"]\s*\+\s*role/.test(blockA),
  'loop body writes --mc-role-{role}');
assert(/setProperty\(\s*['"]--node-['"]\s*\+\s*role/.test(blockA),
  'loop body still writes legacy --node-{role}');

console.log('\n=== #1438 FINAL B: user override → --mc-role-{role} reflects user hex on reload ===');
{
  const env = makeSandbox();
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  env.sandbox.window.MeshCorePresets.applyPreset('deut');

  // Precondition: preset wrote --mc-role-repeater to IBM orange.
  assert(env.root.style.getPropertyValue('--mc-role-repeater').toLowerCase() === '#fe6100',
    'precondition: applyPreset("deut") wrote --mc-role-repeater = #FE6100');

  // Simulate reload: customize-v2 applyCSS replays from localStorage.
  // effectiveConfig.nodeColors merges server + user; userOverrides is the
  // user-only slice.
  const setup =
    'var root = document.documentElement.style;\n' +
    'var userOverrides = { nodeColors: { repeater: "#ff00ff" } };\n' +
    'var effectiveConfig = { nodeColors: { repeater: "#ff00ff", companion: "#56B4E9" } };\n' +
    blockA + '\n';
  vm.runInContext(setup, env.sandbox);

  // Operator's pick MUST be in --mc-role-repeater now.
  const got = env.root.style.getPropertyValue('--mc-role-repeater').toLowerCase();
  assert(got === '#ff00ff',
    'after applyCSS replay, --mc-role-repeater === user pick #ff00ff (got ' + got + ')');

  // And --node-repeater for legacy compat.
  assert(env.root.style.getPropertyValue('--node-repeater').toLowerCase() === '#ff00ff',
    '--node-repeater legacy var also written (got ' + env.root.style.getPropertyValue('--node-repeater') + ')');
}

console.log('\n=== #1438 FINAL C: server-only key does NOT clobber --mc-role-* (preserves #1412) ===');
{
  const env = makeSandbox();
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  env.sandbox.window.MeshCorePresets.applyPreset('deut');

  // companion has NO user override; server config has its own legacy hex.
  const setup =
    'var root = document.documentElement.style;\n' +
    'var userOverrides = { nodeColors: { repeater: "#ff00ff" } };\n' +
    'var effectiveConfig = { nodeColors: { repeater: "#ff00ff", companion: "#2563eb" } };\n' +
    blockA + '\n';
  vm.runInContext(setup, env.sandbox);

  // applyCSS clears inline preset values for server-only keys. The active
  // body selector supplies the color; a vm does not load that stylesheet.
  const got = env.root.style.getPropertyValue('--mc-role-companion').toLowerCase();
  assert(got === '' && env.body.style.getPropertyValue('--mc-role-companion') === '',
    'server-only companion leaves no inline role color overriding the preset');
  const presetRule = styleSrc.match(/body\[data-cb-preset="deut"\]\s*\{([^}]*)\}/);
  assert(env.body.getAttribute('data-cb-preset') === 'deut' && presetRule &&
    /--mc-role-companion:\s*#648fff\s*;/i.test(presetRule[1]),
    'the selected body preset supplies companion #648FFF from style.css');

  // --node-companion CAN take the server value (legacy compat is fine here).
  assert(env.root.style.getPropertyValue('--node-companion').toLowerCase() === '#2563eb',
    '--node-companion legacy var takes server value (got ' + env.root.style.getPropertyValue('--node-companion') + ')');
}

console.log('\n──────────────────────────');
console.log('passed: ' + passed + ', failed: ' + failed);
process.exit(failed === 0 ? 0 : 1);
