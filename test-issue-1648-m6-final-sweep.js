#!/usr/bin/env node
/* Issue #1648 — M6: emoji → Phosphor migration final sweep + lint gate.
 *
 * This is the headline M6 deliverable: a permanent regression-prevention
 * gate that fails CI if any new emoji codepoint lands in the source tree
 * outside an explicit allowlist (`tests/emoji-allowlist.txt`).
 *
 * Scans:
 *   public/**.{js,html,css}
 *   cmd/(server|ingestor|decrypt)/*.go
 *
 * Detects codepoints in:
 *   U+1F300–U+1FAFF  (Misc-Symbols-and-Pictographs, Supplemental, etc.)
 *   U+2600–U+27BF    (Misc-Symbols + Dingbats)
 *   U+2300–U+23FF    (Misc-Technical — ⌚⌛⌨⌖⌃)
 *   U+25A0–U+25FF    (Geometric Shapes — ●○■□▲▼◆◇)
 *   U+2B00–U+2BFF    (Misc-Symbols-Arrows — ⬆⬇⬢)
 *   U+2190–U+21FF    (Arrows — ←→↑↓↗↘ etc; many are text, see allowlist)
 *
 * Allowlist forms (see header of tests/emoji-allowlist.txt):
 *   path/glob (matches any line in file)
 *   path:line
 *   path:line:U+XXXX
 *   /regex/  (matches lines whose CONTENT matches the regex, in any file)
 *
 * Anti-tautology: tested by `test-issue-1648-m6-lint-self.js`, which
 * feeds a known-bad fixture and asserts this lint script flags it.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.resolve(__dirname);

// Exposed for unit tests.
const EMOJI_RANGES = [
  [0x1F300, 0x1FAFF],
  [0x2600,  0x27BF],
  [0x2300,  0x23FF],
  [0x25A0,  0x25FF],
  [0x2B00,  0x2BFF],
  [0x2190,  0x21FF],
];

function isEmojiCodepoint(cp) {
  for (var i = 0; i < EMOJI_RANGES.length; i++) {
    if (cp >= EMOJI_RANGES[i][0] && cp <= EMOJI_RANGES[i][1]) return true;
  }
  return false;
}

function findEmojiInLine(line) {
  var hits = [];
  for (var i = 0; i < line.length; i++) {
    var cp = line.codePointAt(i);
    if (isEmojiCodepoint(cp)) {
      hits.push({ index: i, codepoint: cp });
    }
    if (cp > 0xFFFF) i++; // surrogate pair
  }
  return hits;
}

function loadAllowlist(filePath) {
  if (!fs.existsSync(filePath)) {
    return { pathLine: new Set(), pathLineCp: new Set(), pathGlobs: [], regexes: [] };
  }
  var txt = fs.readFileSync(filePath, 'utf8');
  var pathLine = new Set();         // "file:line"
  var pathLineCp = new Set();       // "file:line:U+XXXX"
  var pathGlobs = [];               // string globs (no `:` after path)
  var regexes = [];                 // RegExp objects
  txt.split('\n').forEach(function (raw) {
    var line = raw.split('#')[0].trim();
    if (!line) return;
    if (line.length >= 2 && line[0] === '/' && line[line.length - 1] === '/') {
      try { regexes.push(new RegExp(line.slice(1, -1), 'u')); } catch (e) {}
      return;
    }
    var parts = line.split(':');
    if (parts.length === 3) pathLineCp.add(line);
    else if (parts.length === 2) pathLine.add(line);
    else pathGlobs.push(line);
  });
  return { pathLine: pathLine, pathLineCp: pathLineCp, pathGlobs: pathGlobs, regexes: regexes };
}

function matchesGlob(rel, glob) {
  // Minimal glob: '*' matches any chars except '/', '**' matches anything.
  // Also a bare path string matches as substring/prefix.
  if (glob === rel) return true;
  if (!glob.includes('*')) return rel === glob;
  var re = '^' + glob
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*\*/g, '___DBLSTAR___')
    .replace(/\*/g, '[^/]*')
    .replace(/___DBLSTAR___/g, '.*') + '$';
  return new RegExp(re).test(rel);
}

function isAllowed(rel, lineNo, codepoint, lineContent, allow) {
  var cpKey = 'U+' + codepoint.toString(16).toUpperCase().padStart(4, '0');
  if (allow.pathLineCp.has(rel + ':' + lineNo + ':' + cpKey)) return true;
  if (allow.pathLine.has(rel + ':' + lineNo)) return true;
  for (var i = 0; i < allow.pathGlobs.length; i++) {
    if (matchesGlob(rel, allow.pathGlobs[i])) return true;
  }
  for (var j = 0; j < allow.regexes.length; j++) {
    if (allow.regexes[j].test(lineContent)) return true;
  }
  return false;
}

function walkFiles(root, exts, ignore) {
  var out = [];
  (function recurse(dir) {
    var entries;
    try { entries = fs.readdirSync(dir, { withFileTypes: true }); } catch (e) { return; }
    entries.forEach(function (ent) {
      var full = path.join(dir, ent.name);
      // Normalise to forward slashes: path.relative yields "public\file.js" on
      // Windows, while both the ignore list here and every path in
      // tests/emoji-allowlist.txt are written with '/'. Without this the
      // allowlist matches nothing off Linux and the sweep reports every
      // annotated line as a violation.
      var rel = path.relative(ROOT, full).split(path.sep).join('/');
      if (ignore.some(function (s) { return rel.indexOf(s) === 0 || rel.indexOf('/' + s) >= 0 || rel.indexOf(s + '/') >= 0; })) return;
      if (ent.isDirectory()) { recurse(full); return; }
      if (!exts.some(function (e) { return ent.name.endsWith(e); })) return;
      // Skip test files and SVG icons by default.
      if (ent.name.startsWith('test-')) return;
      if (ent.name.endsWith('_test.go')) return;
      out.push(rel);
    });
  })(root);
  return out;
}

// --- Public lint API (used by both this test and the self-test fixture) ---
function lintFiles(files, allow) {
  var violations = [];
  files.forEach(function (rel) {
    var abs = path.join(ROOT, rel);
    var txt;
    try { txt = fs.readFileSync(abs, 'utf8'); } catch (e) { return; }
    var lines = txt.split('\n');
    for (var i = 0; i < lines.length; i++) {
      var hits = findEmojiInLine(lines[i]);
      for (var k = 0; k < hits.length; k++) {
        if (!isAllowed(rel, i + 1, hits[k].codepoint, lines[i], allow)) {
          violations.push({
            file: rel,
            line: i + 1,
            codepoint: 'U+' + hits[k].codepoint.toString(16).toUpperCase().padStart(4, '0'),
            content: lines[i].trim().slice(0, 160),
          });
        }
      }
    }
  });
  return violations;
}

function runLint() {
  var allow = loadAllowlist(path.join(ROOT, 'tests', 'emoji-allowlist.txt'));
  var publicFiles = walkFiles(
    path.join(ROOT, 'public'),
    ['.js', '.html', '.css'],
    ['icons', 'instrumented', 'node_modules']
  );
  var cmdFiles = walkFiles(
    path.join(ROOT, 'cmd'),
    ['.go'],
    []
  );
  var allFiles = publicFiles.concat(cmdFiles);
  return lintFiles(allFiles, allow);
}

// Exported surface for the self-test.
module.exports = {
  EMOJI_RANGES: EMOJI_RANGES,
  isEmojiCodepoint: isEmojiCodepoint,
  findEmojiInLine: findEmojiInLine,
  loadAllowlist: loadAllowlist,
  lintFiles: lintFiles,
  runLint: runLint,
};

if (require.main === module) {
  console.log('═══ Issue #1648 M6: emoji → Phosphor final lint gate ═══');
  var violations = runLint();
  if (violations.length) {
    console.error('\n✗ ' + violations.length + ' emoji-as-icon violation(s):\n');
    violations.slice(0, 50).forEach(function (v) {
      console.error('  ' + v.file + ':' + v.line + ' [' + v.codepoint + '] ' + v.content);
    });
    if (violations.length > 50) console.error('  ... and ' + (violations.length - 50) + ' more');
    console.error('\nIf a hit is intentional text content (not iconography),');
    console.error('add it to tests/emoji-allowlist.txt with a `# why` comment.');
    console.error('See the header of that file for entry formats.\n');
    assert.fail('emoji lint gate: ' + violations.length + ' violations');
  }
  console.log('✓ lint gate: 0 violations across public/** and cmd/**');
  console.log('✓ allowlist: tests/emoji-allowlist.txt');
}
