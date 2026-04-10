/* test-drag-manager.js — Unit tests for DragManager (#608 M1) */
'use strict';

const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

// Minimal DOM shim
function makePanel(id) {
  const listeners = {};
  const style = {};
  const dataset = {};
  const classList = {
    _set: new Set(),
    add(c) { this._set.add(c); },
    remove(c) { this._set.delete(c); },
    contains(c) { return this._set.has(c); }
  };
  let attrs = {};
  const header = {
    _listeners: {},
    addEventListener(ev, fn) {
      if (!this._listeners[ev]) this._listeners[ev] = [];
      this._listeners[ev].push(fn);
    },
    setPointerCapture() {},
    releasePointerCapture() {},
    _fire(ev, data) {
      (this._listeners[ev] || []).forEach(fn => fn(data));
    }
  };
  return {
    id: id,
    style: style,
    dataset: dataset,
    classList: classList,
    querySelector(sel) {
      if (sel === '.panel-header') return header;
      return null;
    },
    getAttribute(k) { return attrs[k] || null; },
    setAttribute(k, v) { attrs[k] = v; },
    removeAttribute(k) { delete attrs[k]; },
    getBoundingClientRect() {
      return {
        left: parseFloat(style.left) || 0,
        top: parseFloat(style.top) || 0,
        right: (parseFloat(style.left) || 0) + 300,
        bottom: (parseFloat(style.top) || 0) + 200,
        width: 300,
        height: 200
      };
    },
    _header: header
  };
}

// Mock globals
const storage = {};
const mockWindow = {
  innerWidth: 1920,
  innerHeight: 1080,
  DragManager: null,
  matchMedia() { return { matches: true, addEventListener() {} }; },
  addEventListener() {}
};
const mockDocument = {
  addEventListener(ev, fn) {
    if (!mockDocument._listeners) mockDocument._listeners = {};
    if (!mockDocument._listeners[ev]) mockDocument._listeners[ev] = [];
    mockDocument._listeners[ev].push(fn);
  },
  removeEventListener(ev, fn) {
    if (mockDocument._listeners && mockDocument._listeners[ev]) {
      mockDocument._listeners[ev] = mockDocument._listeners[ev].filter(f => f !== fn);
    }
  },
  querySelectorAll() { return []; }
};
const mockLocalStorage = {
  _data: {},
  getItem(k) { return this._data[k] || null; },
  setItem(k, v) { this._data[k] = v; },
  removeItem(k) { delete this._data[k]; },
  clear() { this._data = {}; }
};

// Load DragManager
const src = fs.readFileSync(path.join(__dirname, 'public', 'drag-manager.js'), 'utf8');
const ctx = vm.createContext({
  window: mockWindow,
  document: mockDocument,
  localStorage: mockLocalStorage,
  Math: Math,
  JSON: JSON,
  console: console,
  setTimeout: setTimeout,
  clearTimeout: clearTimeout,
  parseFloat: parseFloat
});
vm.runInContext(src, ctx);
const DragManager = ctx.window.DragManager;

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    mockLocalStorage.clear();
    fn();
    passed++;
    console.log('  ✓ ' + name);
  } catch (e) {
    failed++;
    console.log('  ✗ ' + name + ': ' + e.message);
  }
}

console.log('DragManager tests:');

test('constructor initializes IDLE state', () => {
  const dm = new DragManager();
  assert.strictEqual(dm.state, 'IDLE');
  assert.strictEqual(dm.enabled, true);
});

test('register adds panel', () => {
  const dm = new DragManager();
  const panel = makePanel('testPanel');
  dm.register(panel);
  assert.strictEqual(dm._panels.length, 1);
});

test('register ignores null panel', () => {
  const dm = new DragManager();
  dm.register(null);
  assert.strictEqual(dm._panels.length, 0);
});

test('pointerdown transitions to PENDING', () => {
  const dm = new DragManager();
  const panel = makePanel('p1');
  dm.register(panel);
  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  assert.strictEqual(dm.state, 'PENDING');
  assert.strictEqual(dm.activePanel, panel);
});

test('pointerdown ignores non-left button', () => {
  const dm = new DragManager();
  const panel = makePanel('p1');
  dm.register(panel);
  panel._header._fire('pointerdown', {
    button: 2, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  assert.strictEqual(dm.state, 'IDLE');
});

test('pointerdown ignores button clicks', () => {
  const dm = new DragManager();
  const panel = makePanel('p1');
  dm.register(panel);
  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest(sel) { return sel === 'button' ? {} : null; } }
  });
  assert.strictEqual(dm.state, 'IDLE');
});

test('pointermove within dead zone stays PENDING', () => {
  const dm = new DragManager();
  const panel = makePanel('p1');
  dm.register(panel);
  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  panel._header._fire('pointermove', { clientX: 103, clientY: 102 });
  assert.strictEqual(dm.state, 'PENDING');
  assert.ok(!panel.classList.contains('is-dragging'));
});

test('pointermove beyond dead zone transitions to DRAGGING', () => {
  const dm = new DragManager();
  const panel = makePanel('p1');
  panel.setAttribute('data-position', 'bl');
  dm.register(panel);
  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  panel._header._fire('pointermove', { clientX: 110, clientY: 110 });
  assert.strictEqual(dm.state, 'DRAGGING');
  assert.ok(panel.classList.contains('is-dragging'));
  assert.strictEqual(panel.getAttribute('data-position'), null); // removed
  assert.strictEqual(panel.dataset.dragged, 'true');
});

test('pointerup after drag finalizes position', () => {
  const dm = new DragManager();
  const panel = makePanel('p1');
  panel.setAttribute('data-position', 'bl');
  ctx.window.innerWidth = 1920;
  ctx.window.innerHeight = 1080;
  dm.register(panel);

  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  panel._header._fire('pointermove', { clientX: 200, clientY: 300 });
  panel._header._fire('pointerup', { pointerId: 1 });

  assert.strictEqual(dm.state, 'IDLE');
  assert.ok(!panel.classList.contains('is-dragging'));
  // Should have persisted
  const saved = JSON.parse(mockLocalStorage.getItem('panel-drag-p1'));
  assert.ok(saved.xPct >= 0);
  assert.ok(saved.yPct >= 0);
});

test('pointerup from PENDING (click) does not finalize', () => {
  const dm = new DragManager();
  const panel = makePanel('p1');
  dm.register(panel);
  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  panel._header._fire('pointerup', { pointerId: 1 });
  assert.strictEqual(dm.state, 'IDLE');
  assert.strictEqual(mockLocalStorage.getItem('panel-drag-p1'), null);
});

test('disable prevents drag', () => {
  const dm = new DragManager();
  dm.disable();
  const panel = makePanel('p1');
  dm.register(panel);
  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  assert.strictEqual(dm.state, 'IDLE');
});

test('snap-to-edge works within threshold', () => {
  const dm = new DragManager();
  const panel = makePanel('p1');
  panel.setAttribute('data-position', 'tl');
  ctx.window.innerWidth = 1920;
  ctx.window.innerHeight = 1080;
  dm.register(panel);

  // Simulate drag to near top-left edge
  panel._header._fire('pointerdown', {
    button: 0, clientX: 500, clientY: 500,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  panel._header._fire('pointermove', { clientX: 510, clientY: 510 }); // trigger DRAGGING

  // Panel is now detached; set its position near edge
  panel.style.left = '5px';
  panel.style.top = '10px';

  panel._header._fire('pointerup', { pointerId: 1 });

  // Should have snapped to margin (12px)
  assert.strictEqual(panel.style.left, '12px');
  assert.strictEqual(panel.style.top, '12px');
});

test('restorePositions applies saved viewport percentages', () => {
  const dm = new DragManager();
  const panel = makePanel('p2');
  panel.setAttribute('data-position', 'br');
  ctx.window.innerWidth = 1000;
  ctx.window.innerHeight = 800;
  dm.register(panel);

  mockLocalStorage.setItem('panel-drag-p2', JSON.stringify({ xPct: 0.5, yPct: 0.25 }));
  dm.restorePositions();

  assert.strictEqual(panel.style.left, '500px');
  assert.strictEqual(panel.style.top, '200px');
  assert.strictEqual(panel.dataset.dragged, 'true');
  assert.strictEqual(panel.getAttribute('data-position'), null);
});

test('handleResize clamps panels inside viewport', () => {
  const dm = new DragManager();
  const panel = makePanel('p3');
  dm.register(panel);

  // Simulate a dragged panel that's now off-screen
  panel.dataset.dragged = 'true';
  panel.style.left = '1800px';
  panel.style.top = '900px';
  ctx.window.innerWidth = 1000;
  ctx.window.innerHeight = 600;

  // Need querySelectorAll to return this panel
  const origQSA = mockDocument.querySelectorAll;
  mockDocument.querySelectorAll = function (sel) {
    if (sel === '.live-overlay[data-dragged="true"]') return [panel];
    return [];
  };

  dm.handleResize();
  mockDocument.querySelectorAll = origQSA;

  // Should be clamped
  const left = parseFloat(panel.style.left);
  const top = parseFloat(panel.style.top);
  assert.ok(left + 300 <= 1000, 'left clamped: ' + left);
  assert.ok(top + 200 <= 600, 'top clamped: ' + top);
});

test('Escape during drag reverts to corner position', () => {
  const dm = new DragManager();
  const panel = makePanel('esc1');
  panel.setAttribute('data-position', 'bl');
  dm.register(panel);

  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  panel._header._fire('pointermove', { clientX: 120, clientY: 120 }); // trigger DRAGGING
  assert.strictEqual(dm.state, 'DRAGGING');

  // Simulate Escape
  dm._handleKeyDown({ key: 'Escape' });

  assert.strictEqual(dm.state, 'IDLE');
  assert.ok(!panel.classList.contains('is-dragging'));
  assert.strictEqual(panel.dataset.dragged, undefined);
  assert.strictEqual(panel.style.transform, '');
});

test('Escape during drag reverts to saved position', () => {
  const dm = new DragManager();
  const panel = makePanel('esc2');
  ctx.window.innerWidth = 1000;
  ctx.window.innerHeight = 800;
  dm.register(panel);

  // Pre-save a dragged position
  mockLocalStorage.setItem('panel-drag-esc2', JSON.stringify({ xPct: 0.3, yPct: 0.4 }));
  dm.restorePositions();
  assert.strictEqual(panel.dataset.dragged, 'true');

  panel._header._fire('pointerdown', {
    button: 0, clientX: 400, clientY: 400,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  panel._header._fire('pointermove', { clientX: 500, clientY: 500 });
  assert.strictEqual(dm.state, 'DRAGGING');

  dm._handleKeyDown({ key: 'Escape' });

  assert.strictEqual(dm.state, 'IDLE');
  assert.strictEqual(panel.style.transform, 'none');
});

test('pointercancel during drag finalizes position', () => {
  const dm = new DragManager();
  const panel = makePanel('pc1');
  panel.setAttribute('data-position', 'tl');
  ctx.window.innerWidth = 1920;
  ctx.window.innerHeight = 1080;
  dm.register(panel);

  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  panel._header._fire('pointermove', { clientX: 200, clientY: 200 });
  assert.strictEqual(dm.state, 'DRAGGING');

  panel._header._fire('pointercancel', {});
  assert.strictEqual(dm.state, 'IDLE');
  assert.ok(!panel.classList.contains('is-dragging'));
});

test('z-index increments on drag', () => {
  const dm = new DragManager();
  const p1 = makePanel('z1');
  const p2 = makePanel('z2');
  dm.register(p1);
  dm.register(p2);

  // Drag p1
  p1._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  p1._header._fire('pointermove', { clientX: 110, clientY: 110 });
  const z1 = parseInt(p1.style.zIndex);
  p1._header._fire('pointerup', { pointerId: 1 });

  // Drag p2
  p2._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  p2._header._fire('pointermove', { clientX: 110, clientY: 110 });
  const z2 = parseInt(p2.style.zIndex);
  p2._header._fire('pointerup', { pointerId: 1 });

  assert.ok(z2 > z1, 'z2 (' + z2 + ') should be greater than z1 (' + z1 + ')');
  assert.ok(z1 >= 1001, 'z1 should be >= 1001');
});

test('disable mid-drag resets state', () => {
  const dm = new DragManager();
  const panel = makePanel('dis1');
  dm.register(panel);

  panel._header._fire('pointerdown', {
    button: 0, clientX: 100, clientY: 100,
    preventDefault() {},
    target: { closest() { return null; } }
  });
  panel._header._fire('pointermove', { clientX: 120, clientY: 120 });
  assert.strictEqual(dm.state, 'DRAGGING');

  dm.disable();
  assert.strictEqual(dm.state, 'IDLE');
  assert.ok(!panel.classList.contains('is-dragging'));
  assert.strictEqual(dm.enabled, false);
});

console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed > 0) process.exit(1);
