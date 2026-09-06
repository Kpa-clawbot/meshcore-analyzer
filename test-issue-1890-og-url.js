#!/usr/bin/env node
/* Issue #1890 — index.html must not hardcode one instance's URL in its
 * Open Graph tags.
 *
 * `<meta property="og:url" content="https://analyzer.00id.net">` shipped to
 * every self-hosted CoreScope. Facebook, Messenger and other OG consumers treat
 * og:url as the canonical destination, so clicking a shared preview from ANY
 * instance navigated to that one host instead of the instance the link came
 * from. With no og:url present, consumers fall back to the URL they crawled,
 * which is correct for every deployment without any configuration.
 *
 * og:image is deliberately NOT covered: it points at the project's own asset on
 * raw.githubusercontent.com, which is a shared project resource, not a
 * redirect target.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const INDEX = path.resolve(__dirname, 'public', 'index.html');
const html = fs.readFileSync(INDEX, 'utf8');

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

console.log('\n=== #1890: index.html carries no instance-specific canonical URL ===');

test('no og:url meta tag pinning a single instance', () => {
  const m = html.match(/<meta[^>]*property=["']og:url["'][^>]*>/i);
  assert.ok(!m, `og:url must not be hardcoded; found: ${m && m[0]}`);
});

test('no rel="canonical" pinning a single instance', () => {
  const m = html.match(/<link[^>]*rel=["']canonical["'][^>]*>/i);
  assert.ok(!m, `canonical link must not be hardcoded; found: ${m && m[0]}`);
});

test('the analyzer.00id.net host appears nowhere in index.html', () => {
  assert.ok(!/00id\.net/i.test(html), 'index.html still references 00id.net');
});

test('og:title and og:description are still present', () => {
  // The fix removes one tag, not the embed. Guard against over-deletion.
  assert.ok(/property=["']og:title["']/i.test(html), 'og:title missing');
  assert.ok(/property=["']og:description["']/i.test(html), 'og:description missing');
  assert.ok(/property=["']og:image["']/i.test(html), 'og:image missing');
});

console.log(`\n  #1890 og:url: ${passed} passed, ${failed} failed\n`);
process.exit(failed ? 1 : 0);
