/* test-table-sort.js — Unit tests for TableSort utility */
'use strict';

const { JSDOM } = require('jsdom');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ✗ ${name}`);
    console.log(`    ${e.message}`);
  }
}

function createDOM(html) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>${html}</body></html>`, {
    url: 'http://localhost',
    runScripts: 'dangerously'
  });
  // Load TableSort into this DOM
  const script = fs.readFileSync(path.join(__dirname, 'public', 'table-sort.js'), 'utf8');
  const el = dom.window.document.createElement('script');
  el.textContent = script;
  dom.window.document.head.appendChild(el);
  return dom;
}

function makeTable(headers, rows) {
  // headers: [{key, type?, label}], rows: [[value, ...]]
  let html = '<table id="t"><thead><tr>';
  for (const h of headers) {
    html += `<th data-sort-key="${h.key}"${h.type ? ` data-type="${h.type}"` : ''}>${h.label || h.key}</th>`;
  }
  html += '</tr></thead><tbody>';
  for (const row of rows) {
    html += '<tr>';
    for (let i = 0; i < row.length; i++) {
      const val = row[i];
      if (typeof val === 'object' && val !== null) {
        html += `<td data-value="${val.dataValue}">${val.text || ''}</td>`;
      } else {
        html += `<td data-value="${val}">${val}</td>`;
      }
    }
    html += '</tr>';
  }
  html += '</tbody></table>';
  return html;
}

function getColumnValues(dom, colIndex) {
  const rows = dom.window.document.querySelectorAll('tbody tr');
  return Array.from(rows).map(r => r.cells[colIndex].getAttribute('data-value'));
}

console.log('\nTableSort — comparators');

test('text comparator: basic alphabetical', () => {
  const cmp = (() => {
    const dom = createDOM('<div></div>');
    return dom.window.TableSort.comparators.text;
  })();
  assert.ok(cmp('apple', 'banana') < 0);
  assert.ok(cmp('banana', 'apple') > 0);
  assert.strictEqual(cmp('same', 'same'), 0);
});

test('text comparator: null/undefined handling', () => {
  const dom = createDOM('<div></div>');
  const cmp = dom.window.TableSort.comparators.text;
  assert.strictEqual(cmp(null, null), 0);
  assert.strictEqual(cmp(undefined, undefined), 0);
});

test('numeric comparator: basic numbers', () => {
  const dom = createDOM('<div></div>');
  const cmp = dom.window.TableSort.comparators.numeric;
  assert.ok(cmp('1', '2') < 0);
  assert.ok(cmp('10', '2') > 0);
  assert.strictEqual(cmp('5', '5'), 0);
});

test('numeric comparator: NaN sorts last', () => {
  const dom = createDOM('<div></div>');
  const cmp = dom.window.TableSort.comparators.numeric;
  assert.ok(cmp('abc', '5') > 0);  // NaN > number (sorts last)
  assert.ok(cmp('5', 'abc') < 0);
  assert.strictEqual(cmp('abc', 'xyz'), 0); // both NaN
});

test('numeric comparator: negative numbers', () => {
  const dom = createDOM('<div></div>');
  const cmp = dom.window.TableSort.comparators.numeric;
  assert.ok(cmp('-10', '-5') < 0);
  assert.ok(cmp('-5', '-10') > 0);
});

test('date comparator: ISO dates', () => {
  const dom = createDOM('<div></div>');
  const cmp = dom.window.TableSort.comparators.date;
  assert.ok(cmp('2024-01-01T00:00:00Z', '2024-06-01T00:00:00Z') < 0);
  assert.ok(cmp('2024-06-01T00:00:00Z', '2024-01-01T00:00:00Z') > 0);
  assert.strictEqual(cmp('2024-01-01', '2024-01-01'), 0);
});

test('date comparator: invalid dates sort last', () => {
  const dom = createDOM('<div></div>');
  const cmp = dom.window.TableSort.comparators.date;
  assert.ok(cmp('invalid', '2024-01-01') > 0);
  assert.ok(cmp('2024-01-01', 'invalid') < 0);
});

test('dBm comparator: strips suffix', () => {
  const dom = createDOM('<div></div>');
  const cmp = dom.window.TableSort.comparators.dbm;
  assert.ok(cmp('-120 dBm', '-80 dBm') < 0);
  assert.ok(cmp('-80 dBm', '-120 dBm') > 0);
  assert.strictEqual(cmp('-95 dBm', '-95 dBm'), 0);
});

test('dBm comparator: works without suffix', () => {
  const dom = createDOM('<div></div>');
  const cmp = dom.window.TableSort.comparators.dbm;
  assert.ok(cmp('-120', '-80') < 0);
});

console.log('\nTableSort — DOM sorting');

test('sort ascending by text column', () => {
  const html = makeTable(
    [{key: 'name'}],
    [['Charlie'], ['Alice'], ['Bob']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  const inst = dom.window.TableSort.init(table, { defaultColumn: 'name', defaultDirection: 'asc' });
  const vals = getColumnValues(dom, 0);
  assert.deepStrictEqual(vals, ['Alice', 'Bob', 'Charlie']);
});

test('sort descending by numeric column', () => {
  const html = makeTable(
    [{key: 'val', type: 'numeric'}],
    [['3'], ['1'], ['2']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  dom.window.TableSort.init(table, { defaultColumn: 'val', defaultDirection: 'desc' });
  const vals = getColumnValues(dom, 0);
  assert.deepStrictEqual(vals, ['3', '2', '1']);
});

test('click toggles direction', () => {
  const html = makeTable(
    [{key: 'name'}],
    [['B'], ['A'], ['C']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  const inst = dom.window.TableSort.init(table, { defaultColumn: 'name', defaultDirection: 'asc' });

  // Initially ascending
  assert.deepStrictEqual(getColumnValues(dom, 0), ['A', 'B', 'C']);

  // Click same header → descending
  const th = dom.window.document.querySelector('th[data-sort-key="name"]');
  th.click();
  assert.deepStrictEqual(getColumnValues(dom, 0), ['C', 'B', 'A']);

  // Click again → ascending
  th.click();
  assert.deepStrictEqual(getColumnValues(dom, 0), ['A', 'B', 'C']);
});

console.log('\nTableSort — aria-sort attributes');

test('aria-sort set correctly on active column', () => {
  const html = makeTable(
    [{key: 'a'}, {key: 'b'}],
    [['1', 'x'], ['2', 'y']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  dom.window.TableSort.init(table, { defaultColumn: 'a', defaultDirection: 'asc' });

  const thA = dom.window.document.querySelector('th[data-sort-key="a"]');
  const thB = dom.window.document.querySelector('th[data-sort-key="b"]');
  assert.strictEqual(thA.getAttribute('aria-sort'), 'ascending');
  assert.strictEqual(thB.getAttribute('aria-sort'), 'none');
});

test('aria-sort updates on direction change', () => {
  const html = makeTable(
    [{key: 'a'}],
    [['1'], ['2']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  dom.window.TableSort.init(table, { defaultColumn: 'a', defaultDirection: 'asc' });

  const th = dom.window.document.querySelector('th[data-sort-key="a"]');
  assert.strictEqual(th.getAttribute('aria-sort'), 'ascending');

  th.click(); // toggle to desc
  assert.strictEqual(th.getAttribute('aria-sort'), 'descending');
});

test('aria-sort updates when switching columns', () => {
  const html = makeTable(
    [{key: 'a'}, {key: 'b'}],
    [['1', 'x'], ['2', 'y']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  dom.window.TableSort.init(table, { defaultColumn: 'a', defaultDirection: 'asc' });

  const thB = dom.window.document.querySelector('th[data-sort-key="b"]');
  thB.click(); // switch to column b

  const thA = dom.window.document.querySelector('th[data-sort-key="a"]');
  assert.strictEqual(thA.getAttribute('aria-sort'), 'none');
  assert.strictEqual(thB.getAttribute('aria-sort'), 'ascending');
});

console.log('\nTableSort — visual indicator');

test('sort arrow shows on active column', () => {
  const html = makeTable(
    [{key: 'a'}],
    [['1'], ['2']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  dom.window.TableSort.init(table, { defaultColumn: 'a', defaultDirection: 'asc' });

  const arrow = dom.window.document.querySelector('.sort-arrow');
  assert.ok(arrow, 'sort arrow should exist');
  assert.ok(arrow.textContent.includes('▲'), 'ascending should show ▲');
});

test('sort arrow changes on direction toggle', () => {
  const html = makeTable(
    [{key: 'a'}],
    [['1'], ['2']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  dom.window.TableSort.init(table, { defaultColumn: 'a', defaultDirection: 'asc' });

  const th = dom.window.document.querySelector('th[data-sort-key="a"]');
  th.click(); // desc
  const arrow = dom.window.document.querySelector('.sort-arrow');
  assert.ok(arrow.textContent.includes('▼'), 'descending should show ▼');
});

console.log('\nTableSort — onSort callback');

test('onSort fires with column and direction', () => {
  const html = makeTable(
    [{key: 'a'}, {key: 'b'}],
    [['1', 'x'], ['2', 'y']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  let called = null;
  dom.window.TableSort.init(table, {
    domReorder: false,
    onSort: function(col, dir) { called = { col, dir }; }
  });

  const th = dom.window.document.querySelector('th[data-sort-key="a"]');
  th.click();
  assert.ok(called, 'onSort should fire');
  assert.strictEqual(called.col, 'a');
  assert.strictEqual(called.dir, 'asc');
});

console.log('\nTableSort — domReorder: false');

test('domReorder: false skips DOM sorting', () => {
  const html = makeTable(
    [{key: 'name'}],
    [['C'], ['A'], ['B']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  dom.window.TableSort.init(table, { defaultColumn: 'name', defaultDirection: 'asc', domReorder: false });

  // DOM order should NOT change
  const vals = getColumnValues(dom, 0);
  assert.deepStrictEqual(vals, ['C', 'A', 'B']);
});

console.log('\nTableSort — destroy');

test('destroy removes event handlers and cleans up', () => {
  const html = makeTable(
    [{key: 'a'}],
    [['2'], ['1']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  const inst = dom.window.TableSort.init(table, { defaultColumn: 'a', defaultDirection: 'asc' });

  inst.destroy();

  const th = dom.window.document.querySelector('th[data-sort-key="a"]');
  assert.strictEqual(th.getAttribute('aria-sort'), null, 'aria-sort should be removed');
  assert.ok(!th.classList.contains('sort-active'), 'sort-active should be removed');
  assert.strictEqual(th.querySelector('.sort-arrow'), null, 'arrow should be removed');
});

console.log('\nTableSort — custom comparators');

test('custom comparator overrides built-in', () => {
  const html = makeTable(
    [{key: 'val', type: 'numeric'}],
    [['3'], ['1'], ['2']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  // Custom: reverse numeric
  dom.window.TableSort.init(table, {
    defaultColumn: 'val', defaultDirection: 'asc',
    comparators: { val: function(a, b) { return Number(b) - Number(a); } }
  });
  const vals = getColumnValues(dom, 0);
  assert.deepStrictEqual(vals, ['3', '2', '1']); // reversed
});

console.log('\nTableSort — date sort with data-type="date"');

test('date column sorts correctly', () => {
  const html = makeTable(
    [{key: 'ts', type: 'date'}],
    [['2024-06-15T10:00:00Z'], ['2024-01-01T00:00:00Z'], ['2024-12-25T23:59:59Z']]
  );
  const dom = createDOM(html);
  const table = dom.window.document.getElementById('t');
  dom.window.TableSort.init(table, { defaultColumn: 'ts', defaultDirection: 'asc' });
  const vals = getColumnValues(dom, 0);
  assert.deepStrictEqual(vals, ['2024-01-01T00:00:00Z', '2024-06-15T10:00:00Z', '2024-12-25T23:59:59Z']);
});

// Summary
console.log(`\n${passed + failed} tests, ${passed} passed, ${failed} failed\n`);
process.exit(failed > 0 ? 1 : 0);
