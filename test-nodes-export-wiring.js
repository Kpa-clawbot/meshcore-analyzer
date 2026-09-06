/**
 * Wiring tests for the Nodes JSON export: index.html loads nodes-export.js,
 * and nodes.js renders an Export JSON button in the topbar that hands the
 * currently filtered node list + active area key to NodesExport.download().
 *
 * Pure source-string assertions (no browser); behavior lives in
 * test-nodes-export.js, DOM behavior in test-nodes-export-e2e.js.
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0;
let failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const html = fs.readFileSync(path.join(__dirname, 'public/index.html'), 'utf8');
const src = fs.readFileSync(path.join(__dirname, 'public/nodes.js'), 'utf8');

console.log('\n=== nodes-export.js is loaded by the SPA shell ===');
assert(/<script src="nodes-export\.js\?v=__BUST__"/.test(html),
  'index.html loads nodes-export.js with the __BUST__ cache buster');
const exportTagIdx = html.indexOf('src="nodes-export.js');
const nodesTagIdx = html.indexOf('src="nodes.js');
assert(exportTagIdx > 0 && nodesTagIdx > 0 && exportTagIdx < nodesTagIdx,
  'nodes-export.js is loaded before nodes.js');

console.log('\n=== Nodes topbar renders the export button ===');
assert(/id="nodesExportBtn"/.test(src), 'nodes.js renders an element with id nodesExportBtn');
const topbarIdx = src.indexOf('nodes-topbar');
const topbarBlock = src.substring(topbarIdx, src.indexOf('</div>', src.indexOf('nodesAreaFilter')));
assert(topbarIdx > 0 && /nodesExportBtn/.test(topbarBlock),
  'the export button sits in the nodes topbar');

console.log('\n=== Click handler exports the visible list for the active area ===');
const handlerIdx = src.indexOf("getElementById('nodesExportBtn')");
assert(handlerIdx > 0, 'found the nodesExportBtn handler block');
const handlerBlock = src.substring(handlerIdx, handlerIdx + 600);
assert(/NodesExport\.download\s*\(/.test(handlerBlock),
  'handler calls NodesExport.download(...)');
assert(/NodesExport\.download\(\s*nodes\s*,/.test(handlerBlock),
  'handler passes the filtered `nodes` array (WYSIWYG), not _allNodes');
assert(/AreaFilter\.getSelected\(\)/.test(handlerBlock),
  'handler passes the active area key from AreaFilter.getSelected()');

console.log('\n=== Empty list disables the button ===');
assert(/nodesExportBtn[\s\S]{0,400}?\.disabled\s*=/.test(src) ||
       /exportBtn\.disabled\s*=/.test(src),
  'the export button is disabled when there is nothing to export');

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed ? 1 : 0);
