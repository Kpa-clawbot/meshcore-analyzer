/**
 * #1446 — CB preset is an end-user opt-in, NOT the canonical color source.
 *
 * Cascade (top wins):
 *   user per-role override  >  active CB preset  >  server config.nodeColors  >  built-in :root defaults
 *
 * 5 acceptance scenarios from the issue. Source-grep + vm-sandbox assertions
 * because the cascade is enforced by a mix of JS (cb-presets.js setting
 * body[data-cb-preset]), CSS (body[data-cb-preset="X"] selectors), and inline
 * style writes (setRoleColorOverride + applyCSS).
 *
 * If these fail, reverting the fix on customize-v2.js / cb-presets.js / roles.js
 * brings them back red.
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

const ROOT = __dirname;
const cv2Src     = fs.readFileSync(path.join(ROOT, 'public', 'customize-v2.js'), 'utf8');
const rolesSrc   = fs.readFileSync(path.join(ROOT, 'public', 'roles.js'), 'utf8');
const presetsSrc = fs.readFileSync(path.join(ROOT, 'public', 'cb-presets.js'), 'utf8');
const styleSrc   = fs.readFileSync(path.join(ROOT, 'public', 'style.css'), 'utf8');

function makeSandbox(localStorageMap) {
  localStorageMap = localStorageMap || {};
  function makeStyle() {
    return {
      _vars: {},          // map var → value
      _imp: {},           // map var → priority ('important' or '')
      setProperty(k, v, prio) {
        this._vars[k] = String(v);
        this._imp[k] = prio || '';
      },
      getPropertyValue(k) { return this._vars[k] || ''; },
      getPropertyPriority(k) { return this._imp[k] || ''; },
      removeProperty(k) { delete this._vars[k]; delete this._imp[k]; }
    };
  }
  const root = { style: makeStyle(), getAttribute() { return null; }, setAttribute() {} };
  const body = {
    _attrs: {},
    setAttribute(k, v) { this._attrs[k] = v; },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k) ? this._attrs[k] : null; },
    removeAttribute(k) { delete this._attrs[k]; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k); },
    dataset: {},
    style: makeStyle()
  };
  const sandbox = {
    window: null,
    _listeners: {},
    localStorage: {
      _m: Object.assign({}, localStorageMap),
      getItem(k) { return Object.prototype.hasOwnProperty.call(this._m, k) ? this._m[k] : null; },
      setItem(k, v) { this._m[k] = String(v); },
      removeItem(k) { delete this._m[k]; }
    },
    document: {
      documentElement: root,
      body: body,
      readyState: 'complete',
      getElementById() { return null; },
      querySelector() { return null; },
      querySelectorAll() { return []; },
      createElement() { return { style: {}, setAttribute() {}, appendChild() {} }; },
      head: { appendChild() {} },
      addEventListener() {}
    },
    console: console,
    setTimeout: setTimeout, clearTimeout: clearTimeout,
    addEventListener(type, fn) {
      if (!this._listeners[type]) this._listeners[type] = [];
      this._listeners[type].push(fn);
    },
    dispatchEvent(ev) {
      var arr = this._listeners[ev && ev.type] || [];
      arr.forEach(function (fn) { try { fn(ev); } catch (e) {} });
      return true;
    },
    fetch: function () { return Promise.resolve({ json: function () { return Promise.resolve({}); } }); },
    matchMedia: function () { return { matches: false, addEventListener() {} }; },
    CustomEvent: function (t, o) { this.type = t; this.detail = o && o.detail; },
    Event: function (t) { this.type = t; },
    Proxy: Proxy,
    getComputedStyle: function () {
      // Pretend body[data-cb-preset]=X CSS rule applies if attr is set: but
      // the JS apply also writes the var to root, so just return root's view.
      return {
        getPropertyValue: function (k) { return root.style._vars[k] || ''; }
      };
    }
  };
  sandbox.window = sandbox;
  return { sandbox, root, body };
}

// ─── SCENARIO 1: Default load with NO localStorage preset → data-cb-preset must NOT be set ───
console.log('\n=== #1446 Scenario 1: cold boot with empty localStorage → no preset active ===');
{
  const env = makeSandbox({});
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  // After cb-presets.js auto-init: with no stored preset, body must NOT carry
  // data-cb-preset (or it must be "none"), so the body[data-cb-preset="X"]
  // CSS rules in style.css do not apply.
  const attr = env.body.getAttribute('data-cb-preset');
  assert(attr === null || attr === 'none' || attr === '',
    'cold boot: body[data-cb-preset] not forced to "default" (got: ' + JSON.stringify(attr) + ')');
  // And cb-presets must NOT have stomped --mc-role-repeater on root with the
  // Wong default, because that would lock out server config.
  const repAtBoot = env.root.style.getPropertyValue('--mc-role-repeater');
  assert(!repAtBoot,
    'cold boot: cb-presets did NOT auto-write --mc-role-repeater (got: ' + JSON.stringify(repAtBoot) + ')');
}

// ─── SCENARIO 2: Server config nodeColors land on --mc-role-X when no preset is active ───
console.log('\n=== #1446 Scenario 2: server config.nodeColors → --mc-role-X (no preset active) ===');
{
  const env = makeSandbox({});
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  vm.runInContext(cv2Src, env.sandbox);
  env.sandbox.window._customizerV2.init({ nodeColors: { repeater: '#aaaaaa', companion: '#56B4E9' } });
  const rep = env.root.style.getPropertyValue('--mc-role-repeater').toLowerCase();
  assert(rep === '#aaaaaa',
    'server-only nodeColors with NO active preset writes --mc-role-repeater = #aaaaaa (got: ' + JSON.stringify(rep) + ')');
}

// ─── SCENARIO 3: User per-role override beats active preset (writes to body inline w/ !important) ───
console.log('\n=== #1446 Scenario 3: user override > active CB preset (body inline !important) ===');
{
  const env = makeSandbox({});
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  env.sandbox.window.MeshCorePresets.applyPreset('deut');
  env.sandbox.window.setRoleColorOverride('repeater', '#ff00ff');
  const bodyVal = env.body.style.getPropertyValue('--mc-role-repeater').toLowerCase();
  const bodyPrio = env.body.style.getPropertyPriority('--mc-role-repeater');
  assert(bodyVal === '#ff00ff',
    'setRoleColorOverride writes --mc-role-repeater on body.style (got: ' + JSON.stringify(bodyVal) + ')');
  assert(bodyPrio === 'important',
    'setRoleColorOverride writes with !important so it wins against body[data-cb-preset] cascade (got: ' + JSON.stringify(bodyPrio) + ')');
}

// ─── SCENARIO 4: Clear per-role override → reverts to active preset ───
console.log('\n=== #1446 Scenario 4: clear override → reverts to active CB preset ===');
{
  const env = makeSandbox({});
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  env.sandbox.window.MeshCorePresets.applyPreset('deut');
  env.sandbox.window.setRoleColorOverride('repeater', '#ff00ff');
  env.sandbox.window.setRoleColorOverride('repeater', null);
  const bodyVal = env.body.style.getPropertyValue('--mc-role-repeater').toLowerCase();
  // After clear: body inline should be restored to the snapshot (deut #FE6100) OR removed.
  // The snapshot logic in roles.js restores the prior body inline value (which
  // was '' before override since applyPreset writes only to root).
  // Either way, after clear, the body inline override must NOT shadow the preset.
  // Effective truth: root still carries #fe6100 from applyPreset.
  const rootVal = env.root.style.getPropertyValue('--mc-role-repeater').toLowerCase();
  assert(rootVal === '#fe6100',
    'after clearing override, --mc-role-repeater on root still shows preset deut #FE6100 (got: ' + JSON.stringify(rootVal) + ')');
  assert(bodyVal !== '#ff00ff',
    'after clearing override, body inline no longer carries the user pick (got: ' + JSON.stringify(bodyVal) + ')');
}

// ─── SCENARIO 5: Clear preset → reverts to server config / built-in default ───
console.log('\n=== #1446 Scenario 5: clear preset → reverts to server config / default ===');
{
  const env = makeSandbox({ 'meshcore-cb-preset': 'deut' });
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  vm.runInContext(cv2Src, env.sandbox);
  env.sandbox.window._customizerV2.init({ nodeColors: { repeater: '#aaaaaa' } });
  // The customizer removes inline role colors when a preset is active;
  // its body selector in the real stylesheet supplies the selected color.
  const repWithPreset = env.root.style.getPropertyValue('--mc-role-repeater').toLowerCase();
  const presetRule = styleSrc.match(/body\[data-cb-preset="deut"\]\s*\{([^}]*)\}/);
  assert(repWithPreset === '' && env.body.getAttribute('data-cb-preset') === 'deut' &&
    presetRule && /--mc-role-repeater:\s*#fe6100\s*;/i.test(presetRule[1]),
    'precondition: deut selects the CSS repeater color #FE6100 without a root override');
  // Now clear the preset.
  const hasClear = typeof env.sandbox.window.MeshCorePresets.clearPreset === 'function';
  assert(hasClear, 'MeshCorePresets.clearPreset() exists');
  if (hasClear) env.sandbox.window.MeshCorePresets.clearPreset();
  else { env.body.removeAttribute('data-cb-preset'); /* fallback to keep test going */ }
  const attr = env.body.getAttribute('data-cb-preset');
  assert(attr === null || attr === 'none' || attr === '',
    'after clearPreset, body[data-cb-preset] is unset/none (got: ' + JSON.stringify(attr) + ')');
  const repAfter = env.root.style.getPropertyValue('--mc-role-repeater').toLowerCase();
  assert(repAfter === '#aaaaaa',
    'after clearPreset, --mc-role-repeater reverts to server config #aaaaaa (got: ' + JSON.stringify(repAfter) + ')');
}

// ─── SCENARIO 6: existing localStorage preset still applies (backward compat) ───
console.log('\n=== #1446 Scenario 6: backward compat — existing localStorage preset still applies ===');
{
  const env = makeSandbox({ 'meshcore-cb-preset': 'deut' });
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  // cb-presets auto-init must have applied deut from storage.
  const attr = env.body.getAttribute('data-cb-preset');
  assert(attr === 'deut', 'stored "deut" preset auto-applies on boot (got: ' + JSON.stringify(attr) + ')');
  const rep = env.root.style.getPropertyValue('--mc-role-repeater').toLowerCase();
  assert(rep === '#fe6100', 'stored preset writes --mc-role-repeater = #FE6100 (got: ' + JSON.stringify(rep) + ')');
}

// ─── Source-grep: customizer UI puts per-role pickers FIRST, then preset selector ───
console.log('\n=== #1446 Scenario 7: customizer UI re-org — node-color pickers come BEFORE preset selector ===');
{
  // Find _renderNodes() return string: it concatenates _renderColorblindPresetSelector() + node rows + ...
  // After fix: node rows come first, preset selector is in a labelled-secondary block.
  const renderNodesIdx = cv2Src.indexOf('function _renderNodes()');
  const after = cv2Src.slice(renderNodesIdx, renderNodesIdx + 4000);
  const presetIdx  = after.indexOf('_renderColorblindPresetSelector');
  const rolesIdx   = after.indexOf("Node Role Colors");
  assert(presetIdx > rolesIdx && rolesIdx > 0,
    'Node Role Colors section appears BEFORE Colorblind Preset block in _renderNodes (rolesIdx=' + rolesIdx + ', presetIdx=' + presetIdx + ')');
  // Preset section labelled as optional.
  assert(/Optional[^<]*colorblind|colorblind[^<]*\(optional\)/i.test(cv2Src),
    'preset section labelled "Optional" / "(optional)" in customizer UI');
}

// ─── SCENARIO 9 (#1446 followup): customize-v2 setOverride path must also write
//     !important on body.style when a preset is active — same gotcha as
//     setRoleColorOverride (roles.js), different code path. The legacy
//     customize.js color picker routes through setRoleColorOverride; the
//     customize-v2.js color picker routes through setOverride → _runPipeline
//     → applyCSS. Both code paths must produce the same observable. ───
console.log('\n=== #1446 Scenario 9: customize-v2 applyCSS writes user override on body with !important when preset active ===');
{
  const env = makeSandbox({ 'meshcore-cb-preset': 'deut' });
  vm.createContext(env.sandbox);
  vm.runInContext(rolesSrc, env.sandbox);
  vm.runInContext(presetsSrc, env.sandbox);
  vm.runInContext(cv2Src, env.sandbox);
  // Init with server config + simulate a user override stored already.
  env.sandbox.localStorage.setItem('cs-theme-overrides', JSON.stringify({ nodeColors: { repeater: '#ff00ff' } }));
  env.sandbox.window._customizerV2.init({ nodeColors: { repeater: '#aaaaaa' } });
  // Body has data-cb-preset=deut (auto-init), user override = #ff00ff.
  // After applyCSS: body.style[--mc-role-repeater] must be #ff00ff with !important
  // so it survives the body[data-cb-preset="deut"] CSS rule cascade.
  const val = env.body.style.getPropertyValue('--mc-role-repeater').toLowerCase();
  const prio = env.body.style.getPropertyPriority('--mc-role-repeater');
  assert(val === '#ff00ff',
    'applyCSS writes user override on body.style (got: ' + JSON.stringify(val) + ')');
  assert(prio === 'important',
    'applyCSS writes user override on body.style with !important (got: ' + JSON.stringify(prio) + ')');
}

// ─── Source-grep: style.css must NOT define a body[data-cb-preset="default"] rule
//     that locks --mc-role-X to Wong — Wong is the :root default already, and the
//     "default" preset selection is the same as "no preset". ───
console.log('\n=== #1446 Scenario 8: style.css does not redundantly clamp Wong via body[data-cb-preset="default"] ===');
{
  // Either the body[data-cb-preset="default"] block is removed, OR the rule body
  // does not set --mc-role-* (only resets text). Either way, when default is
  // active the rule must not write --mc-role-repeater = #D55E00 (which would
  // mask server config). The safest implementation: drop the "default" block.
  const re = /body\[data-cb-preset="default"\][^{]*\{([^}]*)\}/;
  const m = styleSrc.match(re);
  if (!m) {
    assert(true, 'no body[data-cb-preset="default"] block in style.css (preferred)');
  } else {
    const blockBody = m[1];
    assert(!/--mc-role-(repeater|companion|room|sensor|observer)\s*:/.test(blockBody),
      'body[data-cb-preset="default"] block does not redefine --mc-role-{role} (would mask server config)');
  }
}

console.log('\n──────────────────────────');
console.log('passed: ' + passed + ', failed: ' + failed);
process.exit(failed === 0 ? 0 : 1);
