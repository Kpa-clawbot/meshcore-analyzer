/* Unit tests for customizer v2 core functions */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

function makeSandbox() {
  const storage = {};
  const localStorage = {
    _data: storage,
    getItem(k) { return k in storage ? storage[k] : null; },
    setItem(k, v) { storage[k] = String(v); },
    removeItem(k) { delete storage[k]; },
    clear() { for (const k in storage) delete storage[k]; }
  };
  const ctx = {
    window: {
      addEventListener: () => {},
      dispatchEvent: () => {},
      SITE_CONFIG: {},
      _SITE_CONFIG_ORIGINAL_HOME: null,
    },
    document: {
      readyState: 'loading',
      createElement: (tag) => ({
        id: '', textContent: '', innerHTML: '', className: '',
        setAttribute: () => {}, appendChild: () => {},
        style: {}, addEventListener: () => {},
        querySelectorAll: () => [], querySelector: () => null,
      }),
      head: { appendChild: () => {} },
      getElementById: () => null,
      addEventListener: () => {},
      querySelectorAll: () => [],
      querySelector: () => null,
      documentElement: {
        style: { setProperty: () => {}, removeProperty: () => {}, getPropertyValue: () => '' },
        dataset: { theme: 'dark' },
        getAttribute: () => 'dark',
      },
    },
    console,
    localStorage,
    setTimeout: (fn) => fn(),
    clearTimeout: () => {},
    Date, Math, Array, Object, JSON, String, Number, Boolean,
    parseInt, parseFloat, isNaN, Infinity, NaN, undefined,
    MutationObserver: class { observe() {} },
    HashChangeEvent: class {},
    CustomEvent: class CustomEvent { constructor(type, opts) { this.type = type; this.detail = opts && opts.detail; } },
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
  };
  ctx.window.localStorage = localStorage;
  ctx.self = ctx.window;
  return ctx;
}

function loadCustomizer() {
  const ctx = makeSandbox();
  const code = fs.readFileSync('public/customize-v2.js', 'utf8');
  vm.createContext(ctx);
  vm.runInContext(code, ctx, { filename: 'customize-v2.js' });
  return { ctx, api: ctx.window._customizerV2, ls: ctx.localStorage };
}

console.log('\n📋 Customizer V2 — Core Function Tests\n');

// ── readOverrides ──
console.log('readOverrides:');
test('returns {} when key is absent', () => {
  const { api } = loadCustomizer();
  const result = api.readOverrides();
  assert.strictEqual(JSON.stringify(result), '{}');
});

test('returns {} when key contains invalid JSON', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', 'not json{{{');
  assert.strictEqual(JSON.stringify(api.readOverrides()), '{}');
});

test('returns {} when key contains a non-object (string)', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', '"just a string"');
  assert.strictEqual(JSON.stringify(api.readOverrides()), '{}');
});

test('returns {} when key contains an array', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', '[1,2,3]');
  assert.strictEqual(JSON.stringify(api.readOverrides()), '{}');
});

test('returns {} when key contains a number', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', '42');
  assert.strictEqual(JSON.stringify(api.readOverrides()), '{}');
});

test('returns parsed object when valid', () => {
  const { api, ls } = loadCustomizer();
  const data = { theme: { accent: '#ff0000' } };
  ls.setItem('cs-theme-overrides', JSON.stringify(data));
  assert.deepStrictEqual(api.readOverrides(), data);
});

// ── writeOverrides ──
console.log('\nwriteOverrides:');
test('writes serialized JSON to localStorage', () => {
  const { api, ls } = loadCustomizer();
  const data = { theme: { accent: '#ff0000' } };
  api.writeOverrides(data);
  assert.deepStrictEqual(JSON.parse(ls.getItem('cs-theme-overrides')), data);
});

test('removes key when delta is empty {}', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', '{"theme":{}}');
  api.writeOverrides({});
  assert.strictEqual(ls.getItem('cs-theme-overrides'), null);
});

test('round-trips correctly (write → read = identical)', () => {
  const { api } = loadCustomizer();
  const data = { theme: { accent: '#abc', text: '#def' }, nodeColors: { repeater: '#111' } };
  api.writeOverrides(data);
  assert.deepStrictEqual(api.readOverrides(), data);
});

test('strips invalid color values silently', () => {
  const { api, ls } = loadCustomizer();
  api.writeOverrides({ theme: { accent: 'not-a-color' } });
  // Invalid color is stripped by _validateDelta; remaining empty object is stored as '{}'
  const stored = JSON.parse(ls.getItem('cs-theme-overrides'));
  assert.strictEqual(stored.theme, undefined);
});

test('strips out-of-range opacity', () => {
  const { api, ls } = loadCustomizer();
  api.writeOverrides({ heatmapOpacity: 1.5 });
  const stored1 = JSON.parse(ls.getItem('cs-theme-overrides'));
  assert.strictEqual(stored1.heatmapOpacity, undefined);
  api.writeOverrides({ heatmapOpacity: -0.1 });
  const stored2 = JSON.parse(ls.getItem('cs-theme-overrides'));
  assert.strictEqual(stored2.heatmapOpacity, undefined);
});

test('accepts valid opacity', () => {
  const { api, ls } = loadCustomizer();
  api.writeOverrides({ heatmapOpacity: 0.5 });
  const stored = JSON.parse(ls.getItem('cs-theme-overrides'));
  assert.strictEqual(stored.heatmapOpacity, 0.5);
});

// ── computeEffective ──
console.log('\ncomputeEffective:');
test('returns server defaults when overrides is {}', () => {
  const { api } = loadCustomizer();
  const defaults = { theme: { accent: '#aaa', text: '#bbb' }, nodeColors: { repeater: '#ccc' } };
  const result = api.computeEffective(defaults, {});
  assert.deepStrictEqual(result, defaults);
});

test('overrides a single key in a section', () => {
  const { api } = loadCustomizer();
  const defaults = { theme: { accent: '#aaa', text: '#bbb' } };
  const result = api.computeEffective(defaults, { theme: { accent: '#ff0000' } });
  assert.strictEqual(result.theme.accent, '#ff0000');
  assert.strictEqual(result.theme.text, '#bbb');
});

test('overrides multiple keys across sections', () => {
  const { api } = loadCustomizer();
  const defaults = { theme: { accent: '#aaa' }, nodeColors: { repeater: '#bbb' } };
  const result = api.computeEffective(defaults, { theme: { accent: '#111' }, nodeColors: { repeater: '#222' } });
  assert.strictEqual(result.theme.accent, '#111');
  assert.strictEqual(result.nodeColors.repeater, '#222');
});

test('does not mutate either input', () => {
  const { api } = loadCustomizer();
  const defaults = { theme: { accent: '#aaa' } };
  const overrides = { theme: { accent: '#bbb' } };
  const defCopy = JSON.stringify(defaults);
  const ovrCopy = JSON.stringify(overrides);
  api.computeEffective(defaults, overrides);
  assert.strictEqual(JSON.stringify(defaults), defCopy);
  assert.strictEqual(JSON.stringify(overrides), ovrCopy);
});

test('handles missing sections in overrides gracefully', () => {
  const { api } = loadCustomizer();
  const defaults = { theme: { accent: '#aaa' }, nodeColors: { repeater: '#bbb' } };
  const result = api.computeEffective(defaults, { theme: { accent: '#ccc' } });
  assert.strictEqual(result.nodeColors.repeater, '#bbb');
});

test('array values in home are fully replaced, not merged', () => {
  const { api } = loadCustomizer();
  const defaults = { home: { steps: [{ emoji: '1', title: 'a', description: 'b' }], heroTitle: 'X' } };
  const overrides = { home: { steps: [{ emoji: '2', title: 'c', description: 'd' }, { emoji: '3', title: 'e', description: 'f' }] } };
  const result = api.computeEffective(defaults, overrides);
  assert.strictEqual(result.home.steps.length, 2);
  assert.strictEqual(result.home.steps[0].emoji, '2');
  assert.strictEqual(result.home.heroTitle, 'X'); // untouched
});

test('top-level scalars are directly replaced', () => {
  const { api } = loadCustomizer();
  const defaults = { heatmapOpacity: 0.5 };
  const result = api.computeEffective(defaults, { heatmapOpacity: 0.8 });
  assert.strictEqual(result.heatmapOpacity, 0.8);
});

// ── validateShape ──
console.log('\nvalidateShape:');
test('accepts valid delta objects', () => {
  const { api } = loadCustomizer();
  const result = api.validateShape({ theme: { accent: '#fff' }, heatmapOpacity: 0.5 });
  assert.strictEqual(result.valid, true);
});

test('accepts empty object', () => {
  const { api } = loadCustomizer();
  assert.strictEqual(api.validateShape({}).valid, true);
});

test('rejects non-objects (string)', () => {
  const { api } = loadCustomizer();
  assert.strictEqual(api.validateShape('hello').valid, false);
});

test('rejects non-objects (array)', () => {
  const { api } = loadCustomizer();
  assert.strictEqual(api.validateShape([1, 2]).valid, false);
});

test('rejects non-objects (null)', () => {
  const { api } = loadCustomizer();
  assert.strictEqual(api.validateShape(null).valid, false);
});

test('warns on unknown top-level keys', () => {
  const { api } = loadCustomizer();
  const result = api.validateShape({ unknownKey: {} });
  // Unknown keys produce a console.warn but validateShape still returns valid
  assert.strictEqual(result.valid, true);
  assert.strictEqual(result.errors.length, 0);
});

test('validates section types (rejects non-object section)', () => {
  const { api } = loadCustomizer();
  const result = api.validateShape({ theme: 'not an object' });
  assert.strictEqual(result.valid, false);
});

test('accepts valid rgb() color values in theme', () => {
  const { api } = loadCustomizer();
  const result = api.validateShape({ theme: { accent: 'rgb(1,2,3)' } });
  assert.strictEqual(result.valid, true);
});

test('rejects out-of-range opacity values', () => {
  const { api } = loadCustomizer();
  assert.strictEqual(api.validateShape({ heatmapOpacity: 2.0 }).valid, false);
  assert.strictEqual(api.validateShape({ liveHeatmapOpacity: -1 }).valid, false);
});

// ── migrateOldKeys ──
console.log('\nmigrateOldKeys:');
test('migrates all 7 keys correctly', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('meshcore-user-theme', JSON.stringify({ theme: { accent: '#f00' }, branding: { siteName: 'Test' } }));
  ls.setItem('meshcore-timestamp-mode', 'absolute');
  ls.setItem('meshcore-timestamp-timezone', 'utc');
  ls.setItem('meshcore-timestamp-format', 'iso-seconds');
  ls.setItem('meshcore-timestamp-custom-format', 'YYYY-MM-DD');
  ls.setItem('meshcore-heatmap-opacity', '0.7');
  ls.setItem('meshcore-live-heatmap-opacity', '0.3');
  const result = api.migrateOldKeys();
  assert.strictEqual(result.theme.accent, '#f00');
  assert.strictEqual(result.branding.siteName, 'Test');
  assert.strictEqual(result.timestamps.defaultMode, 'absolute');
  assert.strictEqual(result.timestamps.timezone, 'utc');
  assert.strictEqual(result.heatmapOpacity, 0.7);
  assert.strictEqual(result.liveHeatmapOpacity, 0.3);
  // Legacy keys removed
  assert.strictEqual(ls.getItem('meshcore-user-theme'), null);
  assert.strictEqual(ls.getItem('meshcore-timestamp-mode'), null);
  // New key written
  assert.notStrictEqual(ls.getItem('cs-theme-overrides'), null);
});

test('handles partial migration (only some keys)', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('meshcore-timestamp-mode', 'ago');
  const result = api.migrateOldKeys();
  assert.strictEqual(result.timestamps.defaultMode, 'ago');
  assert.strictEqual(ls.getItem('meshcore-timestamp-mode'), null);
});

test('handles invalid JSON in meshcore-user-theme', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('meshcore-user-theme', '{bad json');
  const result = api.migrateOldKeys();
  // Should not crash, returns delta (possibly empty besides what was valid)
  assert(result !== null);
  assert.strictEqual(ls.getItem('meshcore-user-theme'), null);
});

test('skips migration if cs-theme-overrides already exists', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', '{"theme":{}}');
  ls.setItem('meshcore-user-theme', JSON.stringify({ theme: { accent: '#f00' } }));
  const result = api.migrateOldKeys();
  assert.strictEqual(result, null);
  // Legacy key NOT removed (migration skipped entirely)
  assert.notStrictEqual(ls.getItem('meshcore-user-theme'), null);
});

test('returns null when no legacy keys found', () => {
  const { api } = loadCustomizer();
  assert.strictEqual(api.migrateOldKeys(), null);
});

test('drops unknown keys from meshcore-user-theme', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('meshcore-user-theme', JSON.stringify({ theme: { accent: '#f00' }, unknownStuff: 'hi' }));
  const result = api.migrateOldKeys();
  assert.strictEqual(result.theme.accent, '#f00');
  assert.strictEqual(result.unknownStuff, undefined);
});

// ── THEME_CSS_MAP completeness ──
console.log('\nTHEME_CSS_MAP:');
test('includes surface3 mapping', () => {
  const { api } = loadCustomizer();
  assert.strictEqual(api.THEME_CSS_MAP.surface3, '--surface-3');
});

test('includes sectionBg mapping', () => {
  const { api } = loadCustomizer();
  assert.strictEqual(api.THEME_CSS_MAP.sectionBg, '--section-bg');
});

test('matches all keys from old app.js varMap', () => {
  const { api } = loadCustomizer();
  const expectedKeys = [
    'accent', 'accentHover', 'navBg', 'navBg2', 'navText', 'navTextMuted',
    'background', 'text', 'textMuted', 'border',
    'statusGreen', 'statusYellow', 'statusRed',
    'surface1', 'surface2', 'surface3',
    'cardBg', 'contentBg', 'inputBg',
    'rowStripe', 'rowHover', 'detailBg',
    'selectedBg', 'sectionBg',
    'font', 'mono'
  ];
  for (const key of expectedKeys) {
    assert(key in api.THEME_CSS_MAP, `Missing key: ${key}`);
  }
});

// ── _isOverridden tests ──
console.log('\n_isOverridden (value comparison):');

test('returns false when no overrides exist', () => {
  const { api } = loadCustomizer();
  api.init({ theme: { accent: '#aaa' } });
  assert.strictEqual(api.isOverridden('theme', 'accent'), false);
});

test('returns false when override matches server default', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#aaa' } }));
  api.init({ theme: { accent: '#aaa' } });
  assert.strictEqual(api.isOverridden('theme', 'accent'), false);
});

test('returns true when override differs from server default', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#bbb' } }));
  api.init({ theme: { accent: '#aaa' } });
  assert.strictEqual(api.isOverridden('theme', 'accent'), true);
});

test('returns false for key not in overrides', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#bbb' } }));
  api.init({ theme: { accent: '#aaa', border: '#ccc' } });
  assert.strictEqual(api.isOverridden('theme', 'border'), false);
});

test('returns true when server has no default for overridden key', () => {
  const { api, ls } = loadCustomizer();
  ls.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#bbb' } }));
  api.init({});
  assert.strictEqual(api.isOverridden('theme', 'accent'), true);
});

// ── Bug #518 Fixes ──

test('phantom overrides cleaned on init — matching scalars removed', () => {
  const { api, ls } = loadCustomizer();
  const server = { theme: { accent: '#4a9eff', border: '#e2e5ea' }, typeColors: { ADVERT: '#22c55e' } };
  ls.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#4a9eff' }, typeColors: { ADVERT: '#22c55e' } }));
  api.init(server);
  const delta = JSON.parse(ls.getItem('cs-theme-overrides') || '{}');
  assert.ok(!delta.theme, 'phantom theme override should be cleaned');
  assert.ok(!delta.typeColors, 'phantom typeColors override should be cleaned');
});

test('phantom overrides cleaned on init — matching arrays removed', () => {
  const { api, ls } = loadCustomizer();
  const server = { home: { steps: [{ emoji: '📡', title: 'Go', description: 'Do it' }] } };
  ls.setItem('cs-theme-overrides', JSON.stringify({ home: { steps: [{ emoji: '📡', title: 'Go', description: 'Do it' }] } }));
  api.init(server);
  const delta = JSON.parse(ls.getItem('cs-theme-overrides') || '{}');
  assert.ok(!delta.home, 'phantom home array override should be cleaned');
});

test('real overrides preserved after init cleanup', () => {
  const { api, ls } = loadCustomizer();
  const server = { theme: { accent: '#4a9eff' } };
  ls.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#ff0000' } }));
  api.init(server);
  const delta = JSON.parse(ls.getItem('cs-theme-overrides'));
  assert.strictEqual(delta.theme.accent, '#ff0000');
});

test('isOverridden handles array comparison via JSON.stringify', () => {
  const { api, ls } = loadCustomizer();
  const server = { home: { steps: [{ emoji: '📡', title: 'Go', description: 'Do' }] } };
  ls.setItem('cs-theme-overrides', JSON.stringify({ home: { steps: [{ emoji: '📡', title: 'Go', description: 'Do' }] } }));
  api.init(server);
  assert.strictEqual(api.isOverridden('home', 'steps'), false, 'matching array should not be overridden');
});

test('isOverridden returns true for differing arrays', () => {
  const { api, ls } = loadCustomizer();
  const server = { home: { steps: [{ emoji: '📡', title: 'Go', description: 'Do' }] } };
  ls.setItem('cs-theme-overrides', JSON.stringify({ home: { steps: [{ emoji: '🚀', title: 'New', description: 'Changed' }] } }));
  api.init(server);
  assert.strictEqual(api.isOverridden('home', 'steps'), true, 'differing array should be overridden');
});

test('setOverride prunes value matching server default', () => {
  const { api, ls } = loadCustomizer();
  const server = { theme: { accent: '#4a9eff' } };
  api.init(server);
  api.setOverride('theme', 'accent', '#4a9eff');
  // debounce fires synchronously in sandbox
  const delta = JSON.parse(ls.getItem('cs-theme-overrides') || '{}');
  assert.ok(!delta.theme || !delta.theme.accent, 'matching value should be pruned after setOverride');
});

// ── Fix #2: _cleanPhantomOverrides when server has no section ──

test('phantom overrides cleaned when server has NO home section', () => {
  const { api, ls } = loadCustomizer();
  // Server has theme but NO home — the common deployment case
  const server = { theme: { accent: '#4a9eff' } };
  ls.setItem('cs-theme-overrides', JSON.stringify({ home: { checklist: [], steps: [] } }));
  api.init(server);
  const delta = JSON.parse(ls.getItem('cs-theme-overrides') || '{}');
  assert.ok(!delta.home, 'phantom home override should be removed when server has no home section');
});

test('phantom overrides cleaned when server section is undefined — empty arrays removed', () => {
  const { api, ls } = loadCustomizer();
  const server = { theme: { accent: '#4a9eff' }, nodeColors: { repeater: '#dc2626' } };
  // timestamps has actual values (not phantom), home has empty arrays (phantom)
  ls.setItem('cs-theme-overrides', JSON.stringify({
    timestamps: { defaultMode: 'ago', timezone: 'local' },
    home: { checklist: [], steps: [] }
  }));
  api.init(server);
  const delta = JSON.parse(ls.getItem('cs-theme-overrides') || '{}');
  assert.ok(!delta.home, 'phantom home with empty arrays should be removed');
  // timestamps has non-empty values — preserved even without server section
  assert.ok(delta.timestamps, 'timestamps with actual values should be preserved');
  assert.strictEqual(delta.timestamps.defaultMode, 'ago');
});

// ── Fix #4: setOverride with value matching server default is NOT stored ──

test('setOverride with value matching server default is not stored', () => {
  const { api, ls } = loadCustomizer();
  const server = { theme: { accent: '#4a9eff', border: '#e2e5ea' } };
  api.init(server);
  // Set override to same value as server default
  api.setOverride('theme', 'accent', '#4a9eff');
  const delta = JSON.parse(ls.getItem('cs-theme-overrides') || '{}');
  assert.ok(!delta.theme || !delta.theme.accent, 'value matching server default should not be stored');
});

test('existing user overrides are NOT pruned by setOverride on other keys', () => {
  const { api, ls } = loadCustomizer();
  const server = { theme: { accent: '#4a9eff', border: '#e2e5ea' } };
  // User previously chose a custom accent (different from server default)
  ls.setItem('cs-theme-overrides', JSON.stringify({ theme: { accent: '#ff0000' } }));
  api.init(server);
  // Now user changes border — accent should be preserved
  api.setOverride('theme', 'border', '#00ff00');
  const delta = JSON.parse(ls.getItem('cs-theme-overrides') || '{}');
  assert.strictEqual(delta.theme.accent, '#ff0000', 'pre-existing custom override should be preserved');
  assert.strictEqual(delta.theme.border, '#00ff00', 'new non-matching override should be stored');
});

// ── Summary ──
console.log(`\n${passed + failed} tests: ${passed} passed, ${failed} failed\n`);
process.exit(failed > 0 ? 1 : 0);
