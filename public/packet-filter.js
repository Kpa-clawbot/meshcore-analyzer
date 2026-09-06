/* packet-filter.js — Wireshark-style filter language for MeshCore packets
 * Standalone IIFE exposing window.PacketFilter = { parse, evaluate, compile }
 */
(function() {
  'use strict';

  // Canonical payload-label module is the single source of truth (#1799).
  //
  // #1799 PR #1804 r1 item 9 (adv6): inline fallbacks dropped. In the
  // browser, payload-labels.js is loaded synchronously before this
  // script in index.html — a missing module is a packaging bug that
  // should crash loud, not silently mask itself as stale labels.
  var _PL;
  if (typeof window !== 'undefined') {
    if (!window.PayloadLabels) {
      throw new Error('packet-filter.js: window.PayloadLabels missing — payload-labels.js failed to load');
    }
    _PL = window.PayloadLabels;
  } else {
    _PL = require('./payload-labels.js');
  }
  // Prefer the .api namespace introduced in PR #1804 r1 item 8; legacy
  // root-level properties remain as a fall-through for older callers.
  var _src = _PL.api || _PL;
  var FW_PAYLOAD_TYPES = _src.FW_PAYLOAD_TYPES;
  var TYPE_ALIASES     = _src.TYPE_ALIASES;
  var ROUTE_TYPES = { 0: 'TRANSPORT_FLOOD', 1: 'FLOOD', 2: 'DIRECT', 3: 'TRANSPORT_DIRECT' };
  // Aliases: shorthand → canonical route name (issue #339)
  var ROUTE_ALIASES = { 't_flood': 'TRANSPORT_FLOOD', 't_direct': 'TRANSPORT_DIRECT' };
  // Transport route_type values: TRANSPORT_FLOOD (0) and TRANSPORT_DIRECT (3).
  // Mirrors isTransportRoute() in cmd/server/decoder.go.
  function isTransportRouteType(rt) { return rt === 0 || rt === 3; }

  // Use window globals if available (they may have more types)
  function getRT() { return window.ROUTE_TYPES || ROUTE_TYPES; }

  // ── Lexer ──────────────────────────────────────────────────────────────────
  var TK = {
    FIELD: 'FIELD', OP: 'OP', STRING: 'STRING', NUMBER: 'NUMBER', BOOL: 'BOOL',
    DURATION: 'DURATION',
    AND: 'AND', OR: 'OR', NOT: 'NOT', LPAREN: 'LPAREN', RPAREN: 'RPAREN', COMMA: 'COMMA'
  };

  var OP_WORDS = { contains: true, starts_with: true, ends_with: true, after: true, before: true, between: true, in: true };

  // Duration unit → seconds. Used for `age < 1h`-style filters.
  var DURATION_UNITS = { s: 1, m: 60, h: 3600, d: 86400, w: 604800 };

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
      if (input[i] === ',') { tokens.push({ type: TK.COMMA, value: ',' }); i++; continue; }
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
        var numStr = input.slice(start, i);
        // Duration suffix: 1h, 15m, 7d, 30s, 2w. Rejects bare letters/multi-letter units.
        if (i < len && /[a-zA-Z]/.test(input[i])) {
          var unitStart = i;
          while (i < len && /[a-zA-Z]/.test(input[i])) i++;
          var unit = input.slice(unitStart, i);
          if (!DURATION_UNITS[unit]) {
            // Issue #1800: when the user typed a bare hex token (e.g.
            // `path 2f0b001247a047ca`), the number/duration tokenizer eats
            // only the leading "2f". Extend the scan to capture any further
            // [0-9a-fA-F] chars; if the combined slice is pure hex of
            // length >= 4, emit a targeted error rather than the cryptic
            // duration-unit message.
            var probe = i;
            while (probe < len && /[0-9a-fA-F]/.test(input[probe])) probe++;
            var fullSlice = input.slice(start, probe);
            if (/^[0-9a-fA-F]+$/.test(fullSlice) && fullSlice.length >= 4) {
              return { tokens: null, error: "Hex value must be quoted: try 'field == \"<hex>\"' or use the starts_with/contains operator" };
            }
            return { tokens: null, error: "Invalid duration unit '" + unit + "' at position " + unitStart + " (expected s/m/h/d/w)" };
          }
          tokens.push({ type: TK.DURATION, value: parseFloat(numStr) * DURATION_UNITS[unit], raw: numStr + unit });
          continue;
        }
        tokens.push({ type: TK.NUMBER, value: parseFloat(numStr) });
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

      // `between` takes two values: `field between <a> <b>`
      if (op === 'between') {
        var lo = parseValue(field, op);
        var hi = parseValue(field, op);
        validateTimeValue(field, op, lo);
        validateTimeValue(field, op, hi);
        return { type: 'comparison', field: field, op: op, value: lo, value2: hi };
      }

      // `in` takes a parenthesized list of values: `field in (a, b, c)`
      if (op === 'in') {
        if (!peek() || peek().type !== TK.LPAREN) {
          throw new Error("Expected '(' after 'in'");
        }
        advance(); // consume '('
        var values = [];
        if (!peek() || peek().type === TK.RPAREN) {
          throw new Error("Empty value list for 'in'");
        }
        values.push(parseValue(field, op));
        while (peek() && peek().type === TK.COMMA) {
          advance(); // consume ','
          values.push(parseValue(field, op));
        }
        if (!peek() || peek().type !== TK.RPAREN) {
          throw new Error("Expected ')' or ',' in 'in' list");
        }
        advance(); // consume ')'
        return { type: 'comparison', field: field, op: op, values: values };
      }

      var value = parseValue(field, op);
      if (op === 'after' || op === 'before') validateTimeValue(field, op, value);
      return { type: 'comparison', field: field, op: op, value: value };
    }

    // Validates that a value supplied to a temporal op parses as a date.
    function validateTimeValue(field, op, v) {
      if (typeof v !== 'string') return; // numeric epochs are accepted as-is
      var ms = Date.parse(v);
      if (isNaN(ms)) {
        throw new Error("Invalid datetime '" + v + "' for '" + field + ' ' + op + "'");
      }
    }

    function parseValue(field, op) {
      var valTok = peek();
      if (!valTok) throw new Error("Expected value after '" + field + ' ' + op + "'");
      if (valTok.type === TK.STRING) { return advance().value; }
      if (valTok.type === TK.NUMBER) { return advance().value; }
      if (valTok.type === TK.BOOL) { return advance().value; }
      if (valTok.type === TK.DURATION) { return { __duration: true, seconds: advance().value }; }
      if (valTok.type === TK.FIELD) {
        // Bare word as string value (e.g., ADVERT, FLOOD)
        return advance().value;
      }
      throw new Error("Expected value after '" + field + ' ' + op + "'");
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
    if (field === 'transport') return isTransportRouteType(packet.route_type);
    if (field === 'hash') return packet.hash || '';
    if (field === 'raw') return packet.raw_hex || '';
    if (field === 'size') return packet.raw_hex ? packet.raw_hex.length / 2 : 0;
    if (field === 'snr') return packet.snr;
    if (field === 'rssi') return packet.rssi;
    if (field === 'hops') {
      try { return JSON.parse(packet.path_json || '[]').length; } catch(e) { return 0; }
    }
    // Transport region scope. null (not transport-scoped) and "" (transport-scoped,
    // region unmatched) both collapse to "" — neither has a scope to match on.
    if (field === 'scope') return packet.scope_name || '';
    if (field === 'observer') return packet.observer_name || '';
    if (field === 'observer_id') return packet.observer_id || '';
    if (field === 'observer_iata' || field === 'iata') return packet.observer_iata || '';
    if (field === 'observations') return packet.observation_count || 0;
    if (field === 'time' || field === 'timestamp') {
      // Returns ms-since-epoch or null. Falls back to first_seen when timestamp absent
      // (group rows from /api/packets?groupByHash=true expose first_seen instead).
      var ts = packet.timestamp || packet.first_seen || packet.latest;
      if (!ts) return null;
      var ms = typeof ts === 'number' ? ts : Date.parse(ts);
      return isNaN(ms) ? null : ms;
    }
    if (field === 'age') {
      // Age in seconds since the packet timestamp (NOW - ts).
      var ts2 = packet.timestamp || packet.first_seen || packet.latest;
      if (!ts2) return null;
      var ms2 = typeof ts2 === 'number' ? ts2 : Date.parse(ts2);
      if (isNaN(ms2)) return null;
      return Math.max(0, (Date.now() - ms2) / 1000);
    }
    if (field === 'path' || field === 'path_prefixes') {
      try { return JSON.parse(packet.path_json || '[]').join(' → '); } catch(e) { return ''; }
    }
    // Issue #1800: routed_through matches against resolved_path full pubkeys.
    // resolved_path may arrive as a JSON string OR as an already-parsed array
    // (depends on which API surface produced the packet). Handles both.
    if (field === 'routed_through') {
      var rp = packet.resolved_path;
      if (rp == null) return '';
      var arr;
      if (typeof rp === 'string') {
        try { arr = JSON.parse(rp); } catch (e) { return ''; }
      } else if (Array.isArray(rp)) {
        arr = rp;
      } else {
        return '';
      }
      if (!Array.isArray(arr)) return '';
      return arr.map(function(h) { return String(h || '').toLowerCase(); }).join(' ');
    }
    if (field === 'payload_bytes') {
      return packet.raw_hex ? Math.max(0, packet.raw_hex.length / 2 - 2) : 0;
    }
    if (field === 'payload_hex') {
      return packet.raw_hex ? packet.raw_hex.slice(4) : '';
    }
    // TransportCodes fields surfaced from decoded_json.transportCodes
    // (firmware/src/Packet.h:46, parsed at cmd/server/decoder.go:492-498).
    if (field === 'code1' || field === 'code2') {
      try {
        var dc = typeof packet.decoded_json === 'string' ? JSON.parse(packet.decoded_json) : packet.decoded_json;
        if (!dc || !dc.transportCodes) return null;
        var v = field === 'code1' ? dc.transportCodes.code1 : dc.transportCodes.code2;
        return v ? String(v).toUpperCase() : null;
      } catch (e) { return null; }
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
  function parseDateValue(v) {
    if (v == null) return null;
    if (typeof v === 'number') return v;
    if (typeof v === 'string') {
      var ms = Date.parse(v);
      return isNaN(ms) ? null : ms;
    }
    return null;
  }

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

        // `in` operator: membership in a list of values (case-insensitive for strings)
        if (op === 'in') {
          var list = ast.values || [];
          var lhs = String(fieldVal).toLowerCase();
          for (var iv = 0; iv < list.length; iv++) {
            if (String(list[iv]).toLowerCase() === lhs) return true;
          }
          return false;
        }

        // Temporal ops: after / before / between operate on epoch-ms.
        if (op === 'after' || op === 'before' || op === 'between') {
          var lhsMs = typeof fieldVal === 'number' ? fieldVal : Date.parse(fieldVal);
          if (isNaN(lhsMs)) return false;
          var rhs1 = parseDateValue(target);
          if (rhs1 == null) return false;
          if (op === 'after') return lhsMs > rhs1;
          if (op === 'before') return lhsMs < rhs1;
          var rhs2 = parseDateValue(ast.value2);
          if (rhs2 == null) return false;
          var lo = Math.min(rhs1, rhs2), hi = Math.max(rhs1, rhs2);
          return lhsMs >= lo && lhsMs <= hi;
        }

        // Numeric operators
        if (op === '>' || op === '<' || op === '>=' || op === '<=') {
          var a = typeof fieldVal === 'number' ? fieldVal : parseFloat(fieldVal);
          // Duration values are pre-converted to seconds at lex time
          var b = (target && typeof target === 'object' && target.__duration)
            ? target.seconds
            : (typeof target === 'number' ? target : parseFloat(target));
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
          if (ast.field === 'route' && typeof target === 'string') {
            var rAlias = ROUTE_ALIASES[String(target).toLowerCase()];
            if (rAlias) resolvedTarget = rAlias;
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

  // ── Metadata for autocomplete + in-UI documentation (#966) ────────────────
  var FIELDS = [
    { name: 'type',          desc: 'Packet payload type (ADVERT, GRP_TXT, TXT_MSG, ACK, …)' },
    { name: 'route',         desc: 'Route type (FLOOD, DIRECT, TRANSPORT_FLOOD, TRANSPORT_DIRECT)' },
    { name: 'transport',     desc: 'true if route is TRANSPORT_FLOOD or TRANSPORT_DIRECT' },
    { name: 'hash',          desc: 'Packet hash (hex)' },
    { name: 'raw',           desc: 'Full raw hex of the packet' },
    { name: 'size',          desc: 'Total packet size in bytes' },
    { name: 'snr',           desc: 'Signal-to-noise ratio (dB)' },
    { name: 'rssi',          desc: 'Received signal strength (dBm)' },
    { name: 'hops',          desc: 'Number of hops in the path' },
    { name: 'scope',         desc: 'Transport region scope (e.g. #be). Empty for non-transport routes and unmatched regions.' },
    { name: 'observer',      desc: 'Observer station name' },
    { name: 'observer_id',   desc: 'Observer pubkey/id' },
    { name: 'observer_iata', desc: 'Observer IATA region code (e.g. SJC, SFO)' },
    { name: 'iata',          desc: 'Alias of observer_iata' },
    { name: 'observations',  desc: 'Number of observations of this packet' },
    { name: 'path',          desc: 'Hop path as 1-byte prefixes joined (e.g. a3→7f). For pubkey search use routed_through.' },
    { name: 'path_prefixes', desc: 'Alias of path (1-byte hop prefixes joined with arrows)' },
    { name: 'routed_through', desc: 'Match packets routed through a node by pubkey (full or prefix)' },
    { name: 'payload_bytes', desc: 'Payload size in bytes (size - 2 header bytes)' },
    { name: 'payload_hex',   desc: 'Payload bytes as hex (raw without header)' },
    { name: 'time',          desc: 'Packet timestamp (epoch ms)' },
    { name: 'age',           desc: 'Seconds since the packet was observed (use with durations: age < 1h)' },
    { name: 'payload.name',         desc: 'Decoded payload: node name (adverts)' },
    { name: 'payload.lat',          desc: 'Decoded payload: latitude' },
    { name: 'payload.lon',          desc: 'Decoded payload: longitude' },
    { name: 'payload.text',         desc: 'Decoded payload: message text (channel/DM)' },
    { name: 'payload.channel',      desc: 'Decoded payload: channel name' },
    { name: 'payload.channelHash',  desc: 'Decoded payload: channel hash' },
    { name: 'payload.sender',       desc: 'Decoded payload: sender name' },
    { name: 'payload.flags.repeater',    desc: 'Decoded payload: advert flag (repeater role)' },
    { name: 'payload.flags.room',        desc: 'Decoded payload: advert flag (room server)' },
    { name: 'payload.flags.hasLocation', desc: 'Decoded payload: advert has location' },
    { name: 'payload.destHash',     desc: 'Decoded payload: destination hash byte (1B hex, REQ/RESPONSE/TXT_MSG/PATH/ANON_REQ)' },
    { name: 'payload.srcHash',      desc: 'Decoded payload: source hash byte (1B hex, REQ/RESPONSE/TXT_MSG/PATH/ANON_REQ)' },
    { name: 'code1',                desc: 'Transport route Code1 (hex, e.g. AABB) — present on TRANSPORT_FLOOD/DIRECT' },
    { name: 'code2',                desc: 'Transport route Code2 (hex, e.g. CCDD) — present on TRANSPORT_FLOOD/DIRECT' },
  ];

  var OPERATORS = [
    { op: '==',          desc: 'Equal (case-insensitive for strings, alias-aware for type/route)', example: 'type == ADVERT' },
    { op: '!=',          desc: 'Not equal',                                                         example: 'type != ACK' },
    { op: '>',           desc: 'Greater than (numeric)',                                            example: 'snr > 5' },
    { op: '<',           desc: 'Less than (numeric)',                                               example: 'rssi < -90' },
    { op: '>=',          desc: 'Greater or equal',                                                  example: 'hops >= 2' },
    { op: '<=',          desc: 'Less or equal',                                                     example: 'size <= 100' },
    { op: 'contains',    desc: 'Substring match (case-insensitive)',                                example: 'payload.name contains "Gilroy"' },
    { op: 'starts_with', desc: 'String prefix match',                                               example: 'hash starts_with "8a91"' },
    { op: 'ends_with',   desc: 'String suffix match',                                               example: 'hash ends_with "ff"' },
    { op: 'after',       desc: 'Datetime after (ISO or epoch)',                                     example: 'time after "2025-01-01"' },
    { op: 'before',      desc: 'Datetime before',                                                   example: 'time before "2025-12-31"' },
    { op: 'between',     desc: 'Datetime between two values',                                       example: 'time between "2025-01-01" "2025-02-01"' },
    { op: 'in',          desc: 'Value in a list (case-insensitive for strings)',                   example: 'iata in ("SJC","SFO")' },
  ];

  // Canonical type names (firmware payload types)
  var TYPE_VALUES = ['REQ', 'RESPONSE', 'TXT_MSG', 'ACK', 'ADVERT', 'GRP_TXT', 'GRP_DATA', 'ANON_REQ', 'PATH', 'TRACE', 'MULTIPART', 'CONTROL', 'RAW_CUSTOM'];
  var ROUTE_VALUES = ['TRANSPORT_FLOOD', 'FLOOD', 'DIRECT', 'TRANSPORT_DIRECT'];

  // suggest(input, cursor, opts?) → { suggestions: [{value, kind, desc?}], replaceStart, replaceEnd }
  // Token-aware autocomplete:
  //   - Empty / partial-word at cursor → field names
  //   - Right after `field` → operators
  //   - Right after `type ==` → TYPE_VALUES (filtered by partial)
  //   - Right after `route ==` → ROUTE_VALUES
  //   - Partial `payload.<x>` → payload.* fields (incl. dynamic opts.payloadKeys)
  function suggest(input, cursor, opts) {
    opts = opts || {};
    input = input || '';
    if (cursor == null) cursor = input.length;
    var before = input.slice(0, cursor);

    // Determine the current word being typed (the replaceable span).
    // Treat alphanumerics, '_', and '.' as word chars (so "payload.na" is one word).
    var i = cursor;
    while (i > 0 && /[A-Za-z0-9_.]/.test(input.charAt(i - 1))) i--;
    var replaceStart = i;
    var replaceEnd = cursor;
    while (replaceEnd < input.length && /[A-Za-z0-9_.]/.test(input.charAt(replaceEnd))) replaceEnd++;
    var partial = input.slice(replaceStart, cursor);

    // Look at preceding non-space tokens (very small recogniser)
    var preceding = before.slice(0, replaceStart).replace(/\s+$/, '');
    var lastTokMatch = preceding.match(/(==|!=|>=|<=|>|<|contains|starts_with|ends_with|after|before|between|&&|\|\||\(|!)$/);
    var lastTok = lastTokMatch ? lastTokMatch[1] : null;
    // The token before lastTok (the field, if any)
    var fieldBefore = null;
    if (lastTok) {
      var beforeOp = preceding.slice(0, preceding.length - lastTok.length).replace(/\s+$/, '');
      var fm = beforeOp.match(/([A-Za-z_][A-Za-z0-9_.]*)$/);
      if (fm) fieldBefore = fm[1];
    }

    function makePrefixSuggestions(items, kind) {
      var p = partial.toLowerCase();
      var out = [];
      for (var k = 0; k < items.length; k++) {
        var it = items[k];
        var val = typeof it === 'string' ? it : it.value;
        if (!p || val.toLowerCase().indexOf(p) === 0) {
          out.push({ value: val, kind: kind, desc: typeof it === 'string' ? '' : (it.desc || '') });
        }
      }
      return out;
    }

    // Case A: just typed `field ==` (or other comparison op) → value suggestions
    if (lastTok && fieldBefore) {
      if (fieldBefore === 'type' && (lastTok === '==' || lastTok === '!=')) {
        return { suggestions: makePrefixSuggestions(TYPE_VALUES, 'value'), replaceStart: replaceStart, replaceEnd: replaceEnd };
      }
      if (fieldBefore === 'route' && (lastTok === '==' || lastTok === '!=')) {
        return { suggestions: makePrefixSuggestions(ROUTE_VALUES, 'value'), replaceStart: replaceStart, replaceEnd: replaceEnd };
      }
    }

    // Case B: a field is just typed (no operator yet) → operator suggestions
    // Detect: preceding ends with a known field-like identifier and there's no partial word at cursor
    if (!partial && preceding.length) {
      var afterField = preceding.match(/([A-Za-z_][A-Za-z0-9_.]*)$/);
      if (afterField && !lastTok) {
        var ops = OPERATORS.map(function(o) { return { value: o.op, kind: 'op', desc: o.desc }; });
        return { suggestions: ops, replaceStart: replaceStart, replaceEnd: replaceEnd };
      }
    }

    // Case C: default → field name suggestions (incl. dynamic payload.* keys)
    var fieldItems = FIELDS.map(function(f) { return { value: f.name, desc: f.desc }; });
    if (Array.isArray(opts.payloadKeys)) {
      var have = {};
      for (var z = 0; z < fieldItems.length; z++) have[fieldItems[z].value] = true;
      for (var y = 0; y < opts.payloadKeys.length; y++) {
        var pkey = 'payload.' + opts.payloadKeys[y];
        if (!have[pkey]) fieldItems.push({ value: pkey, desc: 'Decoded payload field (dynamic)' });
      }
    }
    return { suggestions: makePrefixSuggestions(fieldItems, 'field'), replaceStart: replaceStart, replaceEnd: replaceEnd };
  }

  var _exports = {
    parse: parse, evaluate: evaluate, compile: compile,
    FIELDS: FIELDS, OPERATORS: OPERATORS,
    TYPE_VALUES: TYPE_VALUES, ROUTE_VALUES: ROUTE_VALUES,
    suggest: suggest,
  };
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

    // Observer IATA (#1188)
    c = compile('observer_iata == "SJC"');
    assert(c.filter({ observer_iata: 'SJC' }), 'observer_iata ==');
    assert(!c.filter({ observer_iata: 'SFO' }), 'observer_iata != mismatch');
    c = compile('iata == "SJC"');
    assert(c.filter({ observer_iata: 'SJC' }), 'iata alias');
    c = compile('iata in ("SJC","SFO")');
    assert(c.filter({ observer_iata: 'SFO' }), 'iata in (...)');
    assert(!c.filter({ observer_iata: 'LAX' }), 'iata in (...) mismatch');
    c = compile('observer_iata contains "S"');
    assert(c.filter({ observer_iata: 'SJC' }), 'observer_iata contains');

    console.log('\nAll tests passed!');
    module.exports = { parse: parse, evaluate: evaluate, compile: compile };
  }
})();
