/* Unit tests for channel color highlighting (M1) — #271 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

// Build minimal sandbox with localStorage mock
function makeSandbox() {
  const store = {};
  const localStorage = {
    getItem: function(k) { return store[k] !== undefined ? store[k] : null; },
    setItem: function(k, v) { store[k] = String(v); },
    removeItem: function(k) { delete store[k]; },
    clear: function() { for (var k in store) delete store[k]; }
  };
  const ctx = {
    window: {},
    localStorage: localStorage,
    console: console,
    JSON: JSON,
  };
  ctx.window.ChannelColors = undefined;
  vm.createContext(ctx);
  const src = fs.readFileSync(__dirname + '/public/channel-colors.js', 'utf8');
  vm.runInContext(src, ctx);
  return ctx;
}

console.log('\n🎨 Channel Colors — Storage CRUD');

test('getChannelColor returns null for unassigned channel', function() {
  const ctx = makeSandbox();
  assert.strictEqual(ctx.window.ChannelColors.get('#test'), null);
});

test('setChannelColor + getChannelColor round-trip', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#sf', '#ef4444');
  assert.strictEqual(ctx.window.ChannelColors.get('#sf'), '#ef4444');
});

test('setChannelColor overwrites existing color', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#sf', '#ef4444');
  ctx.window.ChannelColors.set('#sf', '#3b82f6');
  assert.strictEqual(ctx.window.ChannelColors.get('#sf'), '#3b82f6');
});

test('removeChannelColor removes assignment', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#test', '#ff0000');
  ctx.window.ChannelColors.remove('#test');
  assert.strictEqual(ctx.window.ChannelColors.get('#test'), null);
});

test('removeChannelColor on non-existent channel is no-op', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.remove('#nonexistent');
  assert.deepStrictEqual(ctx.window.ChannelColors.getAll(), {});
});

test('getAllChannelColors returns all assignments', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#a', '#111111');
  ctx.window.ChannelColors.set('#b', '#222222');
  const all = ctx.window.ChannelColors.getAll();
  assert.strictEqual(JSON.stringify(all), JSON.stringify({ '#a': '#111111', '#b': '#222222' }));
});

test('getAllChannelColors returns empty object when none set', function() {
  const ctx = makeSandbox();
  assert.strictEqual(JSON.stringify(ctx.window.ChannelColors.getAll()), '{}');
});

test('handles corrupt localStorage gracefully', function() {
  const ctx = makeSandbox();
  ctx.localStorage.setItem('live-channel-colors', 'not-json{{{');
  assert.strictEqual(ctx.window.ChannelColors.get('#test'), null);
  assert.strictEqual(JSON.stringify(ctx.window.ChannelColors.getAll()), '{}');
});

test('set with null/empty channel is no-op', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('', '#ff0000');
  ctx.window.ChannelColors.set(null, '#ff0000');
  assert.strictEqual(JSON.stringify(ctx.window.ChannelColors.getAll()), '{}');
});

test('set rejects invalid hex colors', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#ch', 'red');
  ctx.window.ChannelColors.set('#ch', '#xyz');
  ctx.window.ChannelColors.set('#ch', '#12345');
  ctx.window.ChannelColors.set('#ch', '#1234567');
  ctx.window.ChannelColors.set('#ch', 'ff0000');
  assert.strictEqual(ctx.window.ChannelColors.get('#ch'), null);
});

test('set normalizes 3-digit hex to 6-digit', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#ch', '#abc');
  assert.strictEqual(ctx.window.ChannelColors.get('#ch'), '#aabbcc');
});

test('set accepts valid 6-digit hex', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#ch', '#ef4444');
  assert.strictEqual(ctx.window.ChannelColors.get('#ch'), '#ef4444');
});

test('get with null/empty channel returns null', function() {
  const ctx = makeSandbox();
  assert.strictEqual(ctx.window.ChannelColors.get(''), null);
  assert.strictEqual(ctx.window.ChannelColors.get(null), null);
});

console.log('\n🎨 Channel Colors — Row Style Generation');

test('getRowStyle returns empty string for non-GRP_TXT types', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#test', '#ff0000');
  assert.strictEqual(ctx.window.ChannelColors.getRowStyle('ADVERT', '#test'), '');
  assert.strictEqual(ctx.window.ChannelColors.getRowStyle('TXT_MSG', '#test'), '');
  assert.strictEqual(ctx.window.ChannelColors.getRowStyle('ACK', '#test'), '');
});

test('getRowStyle returns empty string for unassigned channel', function() {
  const ctx = makeSandbox();
  assert.strictEqual(ctx.window.ChannelColors.getRowStyle('GRP_TXT', '#unassigned'), '');
});

test('getRowStyle returns empty string for null channel', function() {
  const ctx = makeSandbox();
  assert.strictEqual(ctx.window.ChannelColors.getRowStyle('GRP_TXT', null), '');
});

test('getRowStyle returns border + background for assigned GRP_TXT channel', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#sf', '#ef4444');
  const style = ctx.window.ChannelColors.getRowStyle('GRP_TXT', '#sf');
  assert.ok(style.includes('border-left:4px solid #ef4444'), 'should have left border');
  assert.ok(style.includes('background:#ef44441a'), 'should have 10% opacity background');
});

test('getRowStyle works with CHAN type (alias for GRP_TXT)', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#mesh', '#3b82f6');
  const style = ctx.window.ChannelColors.getRowStyle('CHAN', '#mesh');
  assert.ok(style.includes('border-left:4px solid #3b82f6'), 'should have left border');
  assert.ok(style.includes('background:#3b82f61a'), 'should have background tint');
});

test('getRowStyle returns empty when channel has no assigned color', function() {
  const ctx = makeSandbox();
  ctx.window.ChannelColors.set('#other', '#ff0000');
  assert.strictEqual(ctx.window.ChannelColors.getRowStyle('GRP_TXT', '#nope'), '');
});

// ── M2: Channel Color Picker tests ──

test('channel-color-picker.js loads without error in sandbox', function() {
  const ctx = makeSandbox();
  // Provide minimal DOM stubs for the picker
  const elements = {};
  const createdEls = [];
  ctx.document = {
    createElement: function(tag) {
      var el = {
        tagName: tag.toUpperCase(),
        className: '', style: { cssText: '', display: '' },
        innerHTML: '', textContent: '', title: '',
        children: [],
        _attrs: {},
        _listeners: {},
        setAttribute: function(k, v) { this._attrs[k] = v; },
        getAttribute: function(k) { return this._attrs[k] || null; },
        addEventListener: function(ev, fn, opts) { this._listeners[ev] = fn; },
        removeEventListener: function() {},
        appendChild: function(c) { this.children.push(c); return c; },
        querySelector: function(sel) {
          // Very basic selector matching for test
          if (sel === '.cc-picker-swatches') return { addEventListener: function(){}, appendChild: function(c){} };
          if (sel === '.cc-picker-apply') return { addEventListener: function(){} };
          if (sel === '.cc-picker-clear') return { addEventListener: function(){}, style: {} };
          if (sel === '.cc-picker-close') return { addEventListener: function(){} };
          if (sel === '.cc-picker-title') return { textContent: '' };
          if (sel === '.cc-picker-input') return { value: '#000000' };
          return null;
        },
        querySelectorAll: function() { return []; },
        classList: { toggle: function(){}, remove: function(){}, add: function(){} },
        contains: function() { return false; },
        closest: function() { return null; },
        getBoundingClientRect: function() { return { width: 200, height: 200 }; }
      };
      createdEls.push(el);
      return el;
    },
    getElementById: function() { return null; },
    addEventListener: function() {},
    removeEventListener: function() {},
    body: { appendChild: function(c) {} },
    querySelectorAll: function() { return []; }
  };
  ctx.setTimeout = function(fn) { fn(); };
  ctx.window.innerWidth = 1024;
  ctx.window.innerHeight = 768;
  const pickerSrc = fs.readFileSync(__dirname + '/public/channel-color-picker.js', 'utf8');
  vm.runInContext(pickerSrc, ctx);
  assert.ok(ctx.window.ChannelColorPicker, 'ChannelColorPicker should be exported');
  assert.strictEqual(typeof ctx.window.ChannelColorPicker.install, 'function');
  assert.strictEqual(typeof ctx.window.ChannelColorPicker.show, 'function');
  assert.strictEqual(typeof ctx.window.ChannelColorPicker.hide, 'function');
});

test('ChannelColorPicker.install does not throw when elements missing', function() {
  const ctx = makeSandbox();
  ctx.document = {
    createElement: function() {
      return { className: '', style: {}, innerHTML: '', _attrs: {}, children: [],
        setAttribute: function(){}, getAttribute: function(){ return null; },
        addEventListener: function(){}, appendChild: function(c){ this.children.push(c); return c; },
        querySelector: function(){ return { addEventListener: function(){}, style: {}, textContent: '' }; },
        querySelectorAll: function(){ return []; },
        getBoundingClientRect: function(){ return {width:0,height:0}; },
        contains: function(){ return false; }
      };
    },
    getElementById: function() { return null; },
    addEventListener: function() {},
    removeEventListener: function() {},
    body: { appendChild: function(){} },
    querySelectorAll: function() { return []; }
  };
  ctx.setTimeout = function(fn) { fn(); };
  ctx.window.innerWidth = 1024;
  ctx.window.innerHeight = 768;
  const pickerSrc = fs.readFileSync(__dirname + '/public/channel-color-picker.js', 'utf8');
  vm.runInContext(pickerSrc, ctx);
  // Should not throw when feed/table elements don't exist
  ctx.window.ChannelColorPicker.install();
});

// Summary
console.log(`\n${passed} passed, ${failed} failed\n`);
process.exit(failed ? 1 : 0);
