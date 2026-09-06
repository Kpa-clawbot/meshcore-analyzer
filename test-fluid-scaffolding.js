/* Tests for fluid CSS scaffolding (issue #1054).
 * Ensures `public/style.css` declares fluid spacing/type/container tokens
 * via clamp() and that base selectors consume them instead of hardcoded px.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const css = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');
const rules = css.replace(/\/\*[\s\S]*?\*\//g, '');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// --- Helpers ---------------------------------------------------------------

// Base tokens can be split across multiple :root blocks. Theme overrides use
// different selectors; combine the base declarations in stylesheet order.
function rootBlock() {
  const blocks = Array.from(rules.matchAll(/^:root\s*\{([^}]*)\}/gm), m => m[1]);
  if (!blocks.length) throw new Error(':root block not found in style.css');
  return blocks.join('\n');
}

// Find the value of a custom property declared in :root.
function rootVar(name) {
  const re = new RegExp(`${name}\\s*:\\s*([^;]+);`, 'g');
  const values = Array.from(rootBlock().matchAll(re), m => m[1].trim());
  return values.length ? values[values.length - 1] : null;
}

function assertClamp(name) {
  const v = rootVar(name);
  assert.ok(v, `expected :root to declare ${name}`);
  assert.ok(/clamp\s*\(/.test(v), `${name} should use clamp(); got: ${v}`);
}

// --- Fluid spacing tokens --------------------------------------------------

const SPACE_TOKENS = ['--space-xs', '--space-sm', '--space-md',
                      '--space-lg', '--space-xl', '--space-2xl'];

SPACE_TOKENS.forEach(t => {
  test(`spacing token ${t} declared with clamp()`, () => assertClamp(t));
});

// --- Fluid type scale ------------------------------------------------------

const TYPE_TOKENS = ['--fs-sm', '--fs-md', '--fs-lg', '--fs-xl', '--fs-2xl'];

TYPE_TOKENS.forEach(t => {
  test(`type token ${t} declared with clamp()`, () => assertClamp(t));
});

// --- Container tokens ------------------------------------------------------

test('container token --content-max declared', () => {
  const v = rootVar('--content-max');
  assert.ok(v, 'expected --content-max in :root');
  assert.ok(/min\s*\(|clamp\s*\(/.test(v),
    `--content-max should use min()/clamp(); got: ${v}`);
});

test('container token --gutter declared with clamp()', () => assertClamp('--gutter'));

// --- Base selectors must consume fluid tokens ------------------------------

test('html/body rule references fluid font-size token', () => {
  // Look at the html, body { ... } rule (first one).
  const m = css.match(/html\s*,\s*body\s*\{([^}]*)\}/);
  assert.ok(m, 'html, body rule not found');
  const block = m[1];
  assert.ok(/font-size\s*:\s*var\(--fs-/.test(block),
    `html/body should set font-size via var(--fs-*); block was: ${block.trim()}`);
});

// --- Section markers -------------------------------------------------------

test('style.css contains FLUID SCAFFOLDING section marker', () => {
  assert.ok(/FLUID SCAFFOLDING/i.test(css),
    'expected a "FLUID SCAFFOLDING" section marker comment');
});

// --- Summary ---------------------------------------------------------------

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed ? 1 : 0);
