/* Unit tests for live.js animation system — verifies rAF migration and concurrency cap */
'use strict';
const fs = require('fs');
const assert = require('assert');

const src = fs.readFileSync('public/live.js', 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

console.log('\n=== Animation interval elimination ===');

test('pulseNode does not use setInterval', () => {
  // Extract pulseNode function body
  const pulseStart = src.indexOf('function pulseNode(');
  const nextFn = src.indexOf('\n  function ', pulseStart + 1);
  const body = src.substring(pulseStart, nextFn);
  assert.ok(!body.includes('setInterval'), 'pulseNode still uses setInterval');
  assert.ok(body.includes('requestAnimationFrame'), 'pulseNode should use requestAnimationFrame');
});

test('drawAnimatedLine does not use setInterval', () => {
  const drawStart = src.indexOf('function drawAnimatedLine(');
  const nextFn = src.indexOf('\n  function ', drawStart + 1);
  const body = src.substring(drawStart, nextFn);
  assert.ok(!body.includes('setInterval'), 'drawAnimatedLine still uses setInterval');
  assert.ok(body.includes('requestAnimationFrame'), 'drawAnimatedLine should use requestAnimationFrame');
});

test('ghost hop pulse does not use setInterval', () => {
  // Ghost pulse is inside animatePath
  const animStart = src.indexOf('function animatePath(');
  const animEnd = src.indexOf('\n  function ', animStart + 1);
  const body = src.substring(animStart, animEnd);
  assert.ok(!body.includes('setInterval'), 'animatePath still uses setInterval');
});

console.log('\n=== Concurrency cap ===');

test('MAX_CONCURRENT_ANIMS is defined', () => {
  assert.ok(src.includes('MAX_CONCURRENT_ANIMS'), 'MAX_CONCURRENT_ANIMS constant not found');
});

test('MAX_CONCURRENT_ANIMS is set to 20', () => {
  const match = src.match(/MAX_CONCURRENT_ANIMS\s*=\s*(\d+)/);
  assert.ok(match, 'Could not parse MAX_CONCURRENT_ANIMS value');
  assert.strictEqual(parseInt(match[1]), 20);
});

test('animatePath checks MAX_CONCURRENT_ANIMS before proceeding', () => {
  const animStart = src.indexOf('function animatePath(');
  // Check that within the first 200 chars of the function, we check the cap
  const snippet = src.substring(animStart, animStart + 300);
  assert.ok(snippet.includes('activeAnims >= MAX_CONCURRENT_ANIMS'), 'animatePath should check activeAnims against cap');
});

console.log('\n=== Safety: no stale setInterval in animation functions ===');

test('no setInterval remains in animation hot path', () => {
  // The only acceptable setIntervals are the UI ones (timeline, clock, prune, rate counter)
  // Count total setInterval occurrences
  const matches = src.match(/setInterval\(/g) || [];
  // Count known OK ones: _timelineRefreshInterval, _lcdClockInterval, _pruneInterval, _rateCounterInterval
  const okPatterns = ['_timelineRefreshInterval', '_lcdClockInterval', '_pruneInterval', '_rateCounterInterval'];
  let okCount = 0;
  for (const p of okPatterns) {
    if (src.includes(p + ' = setInterval') || src.includes(p + '= setInterval')) okCount++;
  }
  // Allow some non-animation setIntervals (the 4 UI ones above)
  assert.ok(matches.length <= okCount + 1, 
    `Found ${matches.length} setInterval calls, expected at most ${okCount + 1} (non-animation). Some animation setIntervals may remain.`);
});

console.log(`\n${passed} passed, ${failed} failed\n`);
process.exit(failed > 0 ? 1 : 0);
