'use strict';
const vm = require('vm');
const fs = require('fs');

// Minimal browser environment
const ctx = {
  window: {},
  console,
  document: {
    readyState: 'complete',
    documentElement: { getAttribute: () => null },
    getElementById: () => null,
    createElement: () => ({ textContent: '' }),
    head: { appendChild: () => {} }
  },
  navigator: {},
  fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  Date,
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync('public/roles.js', 'utf8'), ctx);
// Mirror browser semantics: in a real browser window === globalThis, so bare
// HEALTH_THRESHOLDS resolves the same as window.HEALTH_THRESHOLDS. Expose it here.
ctx.HEALTH_THRESHOLDS = ctx.window.HEALTH_THRESHOLDS;

// Run tests inside the VM context to preserve closures
const testCode = `
let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  ok:', name); }
  catch (e) { fail++; console.log('FAIL:', name, '—', e.message); }
}
function assert(cond, msg) { if (!cond) throw new Error(msg || 'assertion failed'); }

const now = Date.now();
const recentMs = now - 1000;                                     // 1 second ago — always active
const staleMs  = now - (window.HEALTH_THRESHOLDS.infraSilentMs + 1);   // just past silent threshold

// --- Repeater three-state ---
test('repeater + recent + relay > 0 → relaying',
  () => assert(window.getNodeStatus('repeater', recentMs, 5) === 'relaying'));

test('repeater + recent + relay == 0 → active (idle)',
  () => assert(window.getNodeStatus('repeater', recentMs, 0) === 'active'));

test('repeater + stale + relay > 0 → stale (stale beats relay)',
  () => assert(window.getNodeStatus('repeater', staleMs, 99) === 'stale'));

test('repeater + stale + relay == 0 → stale',
  () => assert(window.getNodeStatus('repeater', staleMs, 0) === 'stale'));

// --- Non-repeater roles unaffected ---
test('companion + recent + relay 0 → active',
  () => assert(window.getNodeStatus('companion', recentMs, 0) === 'active'));

test('companion + recent + relay > 0 → active (relay ignored)',
  () => assert(window.getNodeStatus('companion', recentMs, 99) === 'active'));

test('room + recent + relay 0 → active',
  () => assert(window.getNodeStatus('room', recentMs, 0) === 'active'));

test('sensor + recent + relay 0 → active',
  () => assert(window.getNodeStatus('sensor', recentMs, 0) === 'active'));

// --- Backward compatibility: omitting third arg ---
test('getNodeStatus(repeater, recent) with no relay arg → active (not relaying)',
  () => assert(window.getNodeStatus('repeater', recentMs) === 'active'));

test('getNodeStatus(companion, recent) with no relay arg → active',
  () => assert(window.getNodeStatus('companion', recentMs) === 'active'));

window.testResults = { pass, fail };
`;

vm.runInContext(testCode, ctx);

const { pass, fail } = ctx.window.testResults;
console.log(`\n${pass} passed, ${fail} failed`);
if (fail > 0) process.exit(1);
