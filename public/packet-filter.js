/* packet-filter.js — Wireshark-style filter language for MeshCore packets
 * Standalone IIFE exposing window.PacketFilter = { parse, evaluate, compile }
 */
(function() {
  'use strict';

  // Local copies of type maps (also available as window globals from app.js)
  // Standard firmware payload type names (canonical)
  var FW_PAYLOAD_TYPES = { 0: 'REQ', 1: 'RESPONSE', 2: 'TXT_MSG', 3: 'ACK', 4: 'ADVERT', 5: 'GRP_TXT', 7: 'ANON_REQ', 8: 'PATH', 9: 'TRACE', 11: 'CONTROL' };
  // Aliases: display names → firmware names (for user convenience)
  var TYPE_ALIASES = { 'request': 'REQ', 'response': 'RESPONSE', 'direct msg': 'TXT_MSG', 'dm': 'TXT_MSG', 'ack': 'ACK', 'advert': 'ADVERT', 'channel msg': 'GRP_TXT', 'channel': 'GRP_TXT', 'anon req': 'ANON_REQ', 'path': 'PATH', 'trace': 'TRACE', 'control': 'CONTROL' };
  var ROUTE_TYPES = { 0: 'TRANSPORT_FLOOD', 1: 'FLOOD', 2: 'DIRECT', 3: 'TRANSPORT_DIRECT' };

  // Use window globals if available (they may have more types)
  function getRT() { return window.ROUTE_TYPES || ROUTE_TYPES; }

  // ── Lexer ──────────────────────────────────────────────────────────────────
  var TK = {
    FIELD: 'FIELD', OP: 'OP', STRING: 'STRING', NUMBER: 'NUMBER', BOOL: 'BOOL',
    AND: 'AND', OR: 'OR', NOT: 'NOT', LPAREN: 'LPAREN', RPAREN: 'RPAREN'
  };

  var OP_WORDS = { contains: true, starts_with: true, ends_with: true };

  function lex(input) {
    var tokens = [], i = 0, len = input.length;
    while (i < len) {
      // skip whitespace
      if (input[i] === ' ' || input[i] === '\t') { i++; continue; }
      // two-char operators
      var two = input.slice(i, i + 2);
      if (two === '&&') { tokens.push({ type: TK.AND, value: '&&' }); i += 2; continue; }
      if (two === '||') { tokens.push({ type: TK.OR, value: '||' }); i += 2; continue; }
      if (two === '==' || two === '!=' || two === '>=' || two === '<=') {
        tokens.push({ type: TK.OP, value: two }); i += 2; continue;
      }
      // single char
      if (input[i] === '>' || input[i] === '<') {
        tokens.push({ type: TK.OP, value: input[i] }); i++; continue;
      }
      if (input[i] === '!') { tokens.push({ type: TK.NOT, value: '!' }); i++; continue; }
      if (input[i] === '(') { tokens.push({ type: TK.LPAREN }); i++; continue; }
      if (input[i] === ')') { tokens.push({ type: TK.RPAREN }); i++; continue; }
      // quoted string
      if (input[i] === '"') {
        var j = i + 1;
        while (j < len && input[j] !== '"') {
          if (input[j] === '\\') j++;
          j++;
        }
        if (j >= len) return { tokens: null, error: 'Unterminated string starting at position ' + i };
        tokens.push({ type: TK.STRING, value: input.slice(i + 1, j) });
        i = j + 1; continue;
      }
      // number (including negative: only if previous token is OP, AND, OR, NOT, LPAREN, or start)
      if (/[0-9]/.test(input[i]) || (input[i] === '-' && i + 1 < len && /[0-9]/.test(input[i + 1]) &&
          (tokens.length === 0 || tokens[tokens.length - 1].type === TK.OP ||
           tokens[tokens.length - 1].type === TK.AND || tokens[tokens.length - 1].type === TK.OR ||
           tokens[tokens.length - 1].type === TK.NOT || tokens[tokens.length - 1].type === TK.LPAREN))) {
        var start = i;
        if (input[i] === '-') i++;
        while (i < len && /[0-9]/.test(input[i])) i++;
        if (i < len && input[i] === '.') { i++; while (i < len && /[0-9]/.test(input[i])) i++; }
        tokens.push({ type: TK.NUMBER, value: parseFloat(input.slice(start, i)) });
        continue;
      }
      // identifier / keyword / bare value
      if (/[a-zA-Z_]/.test(input[i])) {
        var s = i;
        while (i < len && /[a-zA-Z0-9_.]/.test(input[i])) i++;
        var word = input.slice(s, i);
        if (word === 'true' || word === 'false') {
          tokens.push({ type: TK.BOOL, value: word === 'true' });
        } else if (OP_WORDS[word]) {
          tokens.push({ type: TK.OP, value: word });
        } else {
          // Could be a field or a bare string value — context decides in parser
          tokens.push({ type: TK.FIELD, value: word });
        }
        continue;
      }
      return { tokens: null, error: "Unexpected character '" + input[i] + "' at position " + i };
    }
    return { tokens: tokens, error: null };
  }

  // ── Parser ─────────────────────────────────────────────────────────────────
  function parse(expression) {
    if (!expression || !expression.trim()) return { ast: null, error: null };
    var lexResult = lex(expression);
    if (lexResult.error) return { ast: null, error: lexResult.error };
    var tokens = lexResult.tokens, pos = 0;

    function peek() { return pos < tokens.length ? tokens[pos] : null; }
    function advance() { return tokens[pos++]; }

    function parseOr() {
      var left = parseAnd();
      while (peek() && peek().type === TK.OR) {
        advance();
        var right = parseAnd();
        left = { type: 'or', left: left, right: right };
      }
      return left;
    }

    function parseAnd() {
      var left = parseNot();
      while (peek() && peek().type === TK.AND) {
        advance();
        var right = parseNot();
        left = { type: 'and', left: left, right: right };
      }
      return left;
    }

    function parseNot() {
      if (peek() && peek().type === TK.NOT) {
        advance();
        return { type: 'not', expr: parseNot() };
      }
      if (peek() && peek().type === TK.LPAREN) {
        advance();
        var expr = parseOr();
        if (!peek() || peek().type !== TK.RPAREN) {
          throw new Error('Expected closing parenthesis');
        }
        advance();
        return expr;
      }
      return parseComparison();
    }

    function parseComparison() {
      var t = peek();
      if (!t) throw new Error('Unexpected end of expression');
      if (t.type !== TK.FIELD) throw new Error("Expected field name, got '" + (t.value || t.type) + "'");
      var field = advance().value;

      // Check if next token is an operator
      var next = peek();
      if (!next || next.type === TK.AND || next.type === TK.OR || next.type === TK.RPAREN) {
        // Bare field — truthy check
        return { type: 'truthy', field: field };
      }

      if (next.type !== TK.OP) {
        throw new Error("Expected operator after '" + field + "', got '" + (next.value || next.type) + "'");
      }
      var op = advance().value;

      // Parse value
      var valTok = peek();
      if (!valTok) throw new Error("Expected value after '" + field + ' ' + op + "'");
      var value;
      if (valTok.type === TK.STRING) { value = advance().value; }
      else if (valTok.type === TK.NUMBER) { value = advance().value; }
      else if (valTok.type === TK.BOOL) { value = advance().value; }
      else if (valTok.type === TK.FIELD) {
        // Bare word as string value (e.g., ADVERT, FLOOD)
        value = advance().value;
      }
      else { throw new Error("Expected value after '" + field + ' ' + op + "'"); }

      return { type: 'comparison', field: field, op: op, value: value };
    }

    try {
      var ast = parseOr();
      if (pos < tokens.length) {
        throw new Error("Unexpected '" + (tokens[pos].value || tokens[pos].type) + "' at end of expression");
      }
      return { ast: ast, error: null };
    } catch (e) {
      return { ast: null, error: e.message };
    }
  }

  // ── Field Resolver ─────────────────────────────────────────────────────────
  function resolveField(packet, field) {
    if (field === 'type') return FW_PAYLOAD_TYPES[packet.payload_type] || '';
    if (field === 'route') return getRT()[packet.route_type] || '';
    if (field === 'hash') return packet.hash || '';
    if (field === 'raw') return packet.raw_hex || '';
    if (field === 'size') return packet.raw_hex ? packet.raw_hex.length / 2 : 0;
    if (field === 'snr') return packet.snr;
    if (field === 'rssi') return packet.rssi;
    if (field === 'hops') {
      try { return JSON.parse(packet.path_json || '[]').length; } catch(e) { return 0; }
    }
    if (field === 'observer') return packet.observer_name || '';
    if (field === 'observer_id') return packet.observer_id || '';
    if (field === 'observations') return packet.observation_count || 0;
    if (field === 'path') {
      try { return JSON.parse(packet.path_json || '[]').join(' → '); } catch(e) { return ''; }
    }
    if (field === 'payload_bytes') {
      return packet.raw_hex ? Math.max(0, packet.raw_hex.length / 2 - 2) : 0;
    }
    if (field === 'payload_hex') {
      return packet.raw_hex ? packet.raw_hex.slice(4) : '';
    }
    // Decoded payload fields (dot notation)
    if (field.startsWith('payload.')) {
      try {
        var decoded = typeof packet.decoded_json === 'string' ? JSON.parse(packet.decoded_json) : packet.decoded_json;
        if (decoded == null) return null;
        var parts = field.slice(8).split('.');
        var val = decoded;
        for (var k = 0; k < parts.length; k++) {
          if (val == null) return null;
          val = val[parts[k]];
        }
        return val;
      } catch(e) { return null; }
    }
    return null;
  }

  // ── Evaluator ──────────────────────────────────────────────────────────────
  function evaluate(ast, packet) {
    if (!ast) return true;
    switch (ast.type) {
      case 'and': return evaluate(ast.left, packet) && evaluate(ast.right, packet);
      case 'or': return evaluate(ast.left, packet) || evaluate(ast.right, packet);
      case 'not': return !evaluate(ast.expr, packet);
      case 'truthy': {
        var v = resolveField(packet, ast.field);
        return !!v;
      }
      case 'comparison': {
        var fieldVal = resolveField(packet, ast.field);
        var target = ast.value;
        var op = ast.op;

        if (fieldVal == null || fieldVal === undefined) return false;

        // Numeric operators
        if (op === '>' || op === '<' || op === '>=' || op === '<=') {
          var a = typeof fieldVal === 'number' ? fieldVal : parseFloat(fieldVal);
          var b = typeof target === 'number' ? target : parseFloat(target);
          if (isNaN(a) || isNaN(b)) return false;
          if (op === '>') return a > b;
          if (op === '<') return a < b;
          if (op === '>=') return a >= b;
          return a <= b;
        }

        // Equality
        if (op === '==' || op === '!=') {
          var eq;
          // Resolve type aliases (e.g., "Channel Msg" → "GRP_TXT")
          var resolvedTarget = target;
          if (ast.field === 'type' && typeof target === 'string') {
            var alias = TYPE_ALIASES[String(target).toLowerCase()];
            if (alias) resolvedTarget = alias;
          }
          if (typeof fieldVal === 'number' && typeof resolvedTarget === 'number') {
            eq = fieldVal === resolvedTarget;
          } else if (typeof fieldVal === 'boolean' || typeof resolvedTarget === 'boolean') {
            eq = fieldVal === resolvedTarget;
          } else {
            eq = String(fieldVal).toLowerCase() === String(resolvedTarget).toLowerCase();
          }
          return op === '==' ? eq : !eq;
        }

        // String operators
        var sv = String(fieldVal).toLowerCase();
        var tv = String(target).toLowerCase();
        if (op === 'contains') return sv.indexOf(tv) !== -1;
        if (op === 'starts_with') return sv.indexOf(tv) === 0;
        if (op === 'ends_with') return sv.slice(-tv.length) === tv;

        return false;
      }
      default: return false;
    }
  }

  // ── Compile ────────────────────────────────────────────────────────────────
  function compile(expression) {
    var result = parse(expression);
    if (result.error) {
      return { filter: function() { return true; }, error: result.error };
    }
    if (!result.ast) {
      return { filter: function() { return true; }, error: null };
    }
    var ast = result.ast;
    return {
      filter: function(packet) { return evaluate(ast, packet); },
      error: null
    };
  }

  var _exports = { parse: parse, evaluate: evaluate, compile: compile };
  if (typeof window !== 'undefined') window.PacketFilter = _exports;

  // ── Self-tests (Node.js only) ─────────────────────────────────────────────
  if (typeof module !== 'undefined' && module.exports) {
    var assert = function(cond, msg) {
      if (!cond) throw new Error('FAIL: ' + (msg || ''));
      process.stdout.write('.');
    };

    // Mock window for tests
    if (typeof window === 'undefined') {
      global.window = { PacketFilter: { parse: parse, evaluate: evaluate, compile: compile } };
    }

    var c;

    // Basic comparison — type == Advert (payload_type 4)
    c = compile('type == Advert');
    assert(!c.error, 'no error');
    assert(c.filter({ payload_type: 4 }), 'type == Advert');
    assert(!c.filter({ payload_type: 1 }), 'type != Advert');

    // Case insensitive
    c = compile('type == advert');
    assert(c.filter({ payload_type: 4 }), 'case insensitive');

    // Numeric
    c = compile('snr > 5');
    assert(c.filter({ snr: 8 }), 'snr > 5 pass');
    assert(!c.filter({ snr: 3 }), 'snr > 5 fail');

    // Negative number
    c = compile('snr < -5');
    assert(c.filter({ snr: -10 }), 'snr < -5');
    assert(!c.filter({ snr: 0 }), 'snr not < -5');

    // Contains
    c = compile('payload.name contains "Gilroy"');
    assert(c.filter({ decoded_json: '{"name":"ESP1 Gilroy Repeater"}' }), 'contains');
    assert(!c.filter({ decoded_json: '{"name":"SFO Node"}' }), 'not contains');

    // AND/OR
    c = compile('type == Advert && snr > 5');
    assert(c.filter({ payload_type: 4, snr: 8 }), 'AND pass');
    assert(!c.filter({ payload_type: 4, snr: 2 }), 'AND fail');

    c = compile('snr > 100 || rssi > -50');
    assert(c.filter({ snr: 1, rssi: -30 }), 'OR pass');
    assert(!c.filter({ snr: 1, rssi: -200 }), 'OR fail');

    // Bare field truthy
    c = compile('payload.flags.hasLocation');
    assert(c.filter({ decoded_json: '{"flags":{"hasLocation":true}}' }), 'truthy true');
    assert(!c.filter({ decoded_json: '{"flags":{"hasLocation":false}}' }), 'truthy false');

    // NOT
    c = compile('!type == Advert');
    assert(!c.filter({ payload_type: 4 }), 'NOT advert');
    assert(c.filter({ payload_type: 1 }), 'NOT non-advert');

    // Hops
    c = compile('hops > 2');
    assert(c.filter({ path_json: '["a","b","c"]' }), 'hops > 2');
    assert(!c.filter({ path_json: '["a"]' }), 'hops not > 2');

    // starts_with
    c = compile('hash starts_with "8a91"');
    assert(c.filter({ hash: '8a91bf33' }), 'starts_with');
    assert(!c.filter({ hash: 'deadbeef' }), 'not starts_with');

    // Parentheses
    c = compile('(type == Advert || type == ACK) && snr > 0');
    assert(c.filter({ payload_type: 4, snr: 5 }), 'parens');
    assert(!c.filter({ payload_type: 4, snr: -1 }), 'parens fail');

    // Error handling
    c = compile('invalid @@@ garbage');
    assert(c.error !== null, 'error on bad input');

    // Null field values
    c = compile('snr > 5');
    assert(!c.filter({}), 'null field');

    // Size
    c = compile('size > 10');
    assert(c.filter({ raw_hex: 'aabbccddee112233445566778899001122' }), 'size');

    // Observer
    c = compile('observer == "kpabap"');
    assert(c.filter({ observer_name: 'kpabap' }), 'observer');

    console.log('\nAll tests passed!');
    module.exports = { parse: parse, evaluate: evaluate, compile: compile };
  }
})();
