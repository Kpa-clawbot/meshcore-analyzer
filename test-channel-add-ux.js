/**
 * Tests for #759 — Add channel UX: button, hint, status feedback.
 * Validates the HTML structure rendered by channels.js init.
 */
'use strict';

const fs = require('fs');

let passed = 0;
let failed = 0;

function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function assertIncludes(html, substr, msg) {
  assert(html.includes(substr), msg);
}

// Read the channels.js source to extract the HTML template
const src = fs.readFileSync(__dirname + '/public/channels.js', 'utf8');

// Extract the sidebar HTML from the template literal
const htmlMatch = src.match(/app\.innerHTML\s*=\s*`([\s\S]*?)`;/);
const html = htmlMatch ? htmlMatch[1] : '';

console.log('Test: Add channel UX (#759)');

// 1. Button renders in the form
assertIncludes(html, 'class="ch-add-btn"', 'Add button has ch-add-btn class');
assertIncludes(html, 'type="submit"', 'Button is type=submit');
assertIncludes(html, '>+</button>', 'Button shows + text');

// 2. Form has proper structure
assertIncludes(html, 'class="ch-add-form"', 'Form has ch-add-form class');
assertIncludes(html, 'class="ch-add-row"', 'Row wrapper present');
assertIncludes(html, 'class="ch-add-label"', 'Label present');

// 3. Hint text present
assertIncludes(html, 'class="ch-add-hint"', 'Hint div present');
assertIncludes(html, 'Hashtag name or 32-char hex key', 'Hint text correct');

// 4. Status div present
assertIncludes(html, 'id="chAddStatus"', 'Status div has correct id');
assertIncludes(html, 'class="ch-add-status"', 'Status div has correct class');
assertIncludes(html, 'style="display:none"', 'Status div hidden by default');

// 5. showAddStatus function exists in source
assert(src.includes('function showAddStatus('), 'showAddStatus function defined');
assert(src.includes("'success'"), 'Success status type referenced');
assert(src.includes("'error'"), 'Error status type referenced');

// 6. CSS classes exist
const css = fs.readFileSync(__dirname + '/public/style.css', 'utf8');
assert(css.includes('.ch-add-form'), 'CSS: .ch-add-form defined');
assert(css.includes('.ch-add-btn'), 'CSS: .ch-add-btn defined');
assert(css.includes('.ch-add-hint'), 'CSS: .ch-add-hint defined');
assert(css.includes('.ch-add-status'), 'CSS: .ch-add-status defined');
assert(css.includes('.ch-add-row'), 'CSS: .ch-add-row defined');
assert(css.includes('.ch-add-label'), 'CSS: .ch-add-label defined');

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed > 0 ? 1 : 0);
