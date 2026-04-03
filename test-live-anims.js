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
if (failed > 0) process.exit(1);

/* === Null-guard coverage for rAF callbacks === */
const src2 = fs.readFileSync('public/live.js', 'utf8');
let p2 = 0, f2 = 0;
function test2(name, fn) {
  try { fn(); p2++; console.log(`  ✅ ${name}`); }
  catch (e) { f2++; console.log(`  ❌ ${name}: ${e.message}`); }
}

console.log('\n=== Null guards on rAF animation callbacks ===');

test2('animatePath tick() has null guard', () => {
  // tick is inside animatePath, after "function tick(now)"
  const tickStart = src2.indexOf('function tick(now)');
  const tickBody = src2.substring(tickStart, tickStart + 200);
  assert.ok(tickBody.includes('!animLayer || !pathsLayer'), 'tick() missing animLayer/pathsLayer null guard');
});

test2('animatePath fadeOut() has null guard', () => {
  const fadeOutStart = src2.indexOf('function fadeOut(now)');
  const fadeOutBody = src2.substring(fadeOutStart, fadeOutStart + 200);
  assert.ok(fadeOutBody.includes('!animLayer || !pathsLayer'), 'fadeOut() missing animLayer/pathsLayer null guard');
});

test2('drawAnimatedLine animateLine() has null guard', () => {
  const lineStart = src2.indexOf('function animateLine(now)');
  const lineBody = src2.substring(lineStart, lineStart + 200);
  assert.ok(lineBody.includes('!animLayer || !pathsLayer'), 'animateLine() missing animLayer/pathsLayer null guard');
});

test2('drawAnimatedLine animateFade() has null guard', () => {
  const fadeStart = src2.indexOf('function animateFade(now)');
  const fadeBody = src2.substring(fadeStart, fadeStart + 200);
  assert.ok(fadeBody.includes('!pathsLayer'), 'animateFade() missing pathsLayer null guard');
});

test2('pulseNode animatePulse() has null guard', () => {
  const pulseStart = src2.indexOf('function animatePulse(now)');
  const pulseBody = src2.substring(pulseStart, pulseStart + 200);
  assert.ok(pulseBody.includes('!animLayer'), 'animatePulse() missing animLayer null guard');
});

test2('ghostPulse has null guard', () => {
  const ghostStart = src2.indexOf('function ghostPulse(now)');
  const ghostBody = src2.substring(ghostStart, ghostStart + 200);
  assert.ok(ghostBody.includes('!animLayer'), 'ghostPulse() missing animLayer null guard');
});

console.log(`\n${p2} passed, ${f2} failed\n`);
if (f2 > 0) process.exit(1);
