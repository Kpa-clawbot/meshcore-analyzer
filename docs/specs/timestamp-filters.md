# Timestamp-Based Packet Filters

**Issue:** #289  
**Status:** Draft  
**Depends on:** #286 (timestamp display config)

## Summary

Extend the existing filter engine (`packet-filter.js`) with a `time` field type supporting absolute ISO timestamps, relative durations, and range expressions. The filter compiles date expressions to epoch milliseconds at parse time so per-packet evaluation is a single numeric comparison — no date parsing in the hot path.

## Syntax

### Absolute (ISO 8601)

```
time > "2024-01-01T00:00:00Z"
time <= "2024-06-15"
time == "2024-03-01"
```

Quoted strings after `time` are parsed as dates. Partial dates (`"2024-01-01"`) are treated as midnight UTC. All absolute values are interpreted as UTC regardless of the user's display preference.

### Relative

```
time > 2h ago
time > 30m ago
time > 7d ago
```

The lexer recognizes `<number><unit> ago` as a relative time literal. Supported units: `s` (seconds), `m` (minutes), `h` (hours), `d` (days). At compile time, the relative offset is resolved to an absolute epoch ms value (`Date.now() - offset`). This means a compiled filter's relative thresholds are frozen at compile time — recompile to refresh.

### Shorthand

```
time.ago < 30m
time.ago < 2h
```

`time.ago` resolves to `Date.now() - packet.timestamp`. The comparison value is a duration literal (`30m`, `2h`, `7d`). This is syntactic sugar and semantically equivalent to the relative form but reads more naturally for "show me recent packets."

### Range

```
time between "2024-01-01" "2024-01-02"
time between 1h ago 30m ago
```

`between` is a ternary operator: `field between <low> <high>`. Compiles to `low <= field && field <= high`. Both bounds are inclusive.

### Combinable with existing filters

```
type == Advert && time > 1h ago
snr > 5 && time between "2024-01-01" "2024-01-02"
(type == GRP_TXT || type == TXT_MSG) && time.ago < 30m
```

## Grammar Extension

### New token types

| Token | Pattern | Example |
|-------|---------|---------|
| `DURATION` | `/^\d+[smhd]$/` | `30m`, `2h`, `7d` |
| `AGO` | keyword `ago` | `ago` |
| `BETWEEN` | keyword `between` | `between` |

### Lexer changes

1. After reading an identifier that matches `\d+[smhd]`, emit `DURATION` token instead of `FIELD`.
2. Recognize `ago` and `between` as keywords (like `and`/`or`).

### Parser changes

In `parseComparison()`:

1. **Relative time:** If field is `time` and value tokens are `DURATION AGO`, compute `Date.now() - durationToMs(duration)` and store as a numeric epoch ms value in the AST node.
2. **Absolute time:** If field is `time` and value is a `STRING`, attempt `new Date(value).getTime()`. If `NaN`, return parse error. Store epoch ms.
3. **`time.ago` shorthand:** If field is `time.ago`, the value is a `DURATION`. Store the duration in ms. At evaluation, compute `now - packet_ts` and compare against the duration.
4. **`between`:** If operator token is `BETWEEN`, consume two values (same type resolution as above). Emit `{ type: 'between', field, low, high }`.

### AST node shapes

```js
// Absolute/relative (pre-resolved to epoch ms)
{ type: 'comparison', field: 'time', op: '>', value: 1704067200000 }

// time.ago (duration in ms)
{ type: 'comparison', field: 'time.ago', op: '<', value: 1800000 }

// between (both bounds as epoch ms)
{ type: 'between', field: 'time', low: 1704067200000, high: 1704153600000 }
```

## Field Resolution

Add to `resolveField()`:

```js
if (field === 'time') return packet.timestamp;  // epoch ms
if (field === 'time.ago') return Date.now() - packet.timestamp;
```

`packet.timestamp` is the packet's capture time in epoch milliseconds. This field already exists in the data model (populated from the DB `created_at` column).

## Time Semantics

- **Filter expressions:** Always UTC. `"2024-01-01"` means `2024-01-01T00:00:00Z`.
- **Display:** Follows the user's timestamp config from #286 (UTC/local/relative).
- **Relative times:** Computed against `Date.now()` at compile time. The compiled filter is a snapshot — if the filter stays active for hours, relative thresholds drift. This is acceptable; filters are typically short-lived or recompiled on interaction.

**No timezone specifiers in the filter syntax.** UTC only. This avoids ambiguity and parsing complexity. Users who think in local time can use the relative syntax (`time > 2h ago`) which is timezone-agnostic.

## Performance

### Compile-time work (once)

- Parse date strings → epoch ms via `new Date().getTime()` (~1μs per date)
- Parse duration strings → ms via multiplication (~0ns, trivial arithmetic)
- Relative `ago` → `Date.now() - offset` (~0ns)

### Per-packet evaluation (hot path)

- `time` comparison: one numeric read + one numeric compare. Same cost as `snr > 5`.
- `time.ago`: one subtraction + one compare. Two arithmetic ops. **Important:** cache `Date.now()` once per filter pass (e.g., in a closure variable set before iterating packets), not per-packet. 30K `Date.now()` calls are ~1ms but it's a pointless syscall tax.
- `between`: two numeric compares.

**No `Date` objects created per packet. No string parsing per packet. No regex per packet.**

At 30K packets, the time filter adds ~0.1ms total to filter evaluation — dominated by the existing field resolution and AST walk overhead. No measurable regression.

### Implementation note: `between` as sugar

`between` should compile to `{ type: 'and', left: { type: 'comparison', field, op: '>=', value: low }, right: { type: 'comparison', field, op: '<=', value: high } }` — reusing existing comparison evaluation. No new AST node type, no new evaluator branch. The parser desugars it; the evaluator never sees `between`.

### Implementation note: `time.ago` and `Date.now()` caching

The `compile()` function should return a filter that accepts an optional `now` parameter:

```js
var compiled = compile('time.ago < 30m');
var now = Date.now();
packets.filter(function(p) { return compiled.filter(p, now); });
```

If `now` is not passed, `Date.now()` is called once on the first invocation and reused for the entire filter pass. This avoids 30K syscalls and ensures consistent evaluation within a single pass.

## Carmack Review Notes

Reviewed with a performance-first lens (30K+ packets, real-time updates):

1. **✅ No allocations in hot path.** All date parsing happens at compile time. Per-packet evaluation is pure numeric comparison — same cost as existing `snr > 5` filters.

2. **⚠️ `Date.now()` per-packet for `time.ago`.** Fixed above — cache once per filter pass via optional `now` parameter or closure. Without this, 30K packets × `Date.now()` = ~1ms wasted on a monotonic clock syscall that returns the same value.

3. **✅ `between` as sugar, not a new node type.** Desugar in the parser to reuse existing `and` + `comparison` nodes. Zero new code paths in the evaluator = zero new bugs in the evaluator.

4. **✅ Parser complexity is bounded.** Three new token types, one new keyword. The parser remains LL(1) — no backtracking, no ambiguity. `DURATION AGO` is a clear two-token lookahead only when field is `time`.

5. **✅ Memory impact negligible.** Compiled time filters add one or two floats to the AST. At 16 bytes per node, even complex expressions with multiple time clauses are <100 bytes.

6. **⚠️ Compiled filter staleness for relative times.** Spec acknowledges this. Acceptable for a web UI where filters are recompiled on user interaction. If filters persist across long WebSocket sessions, consider recompiling on a timer (every 60s). This is a future concern, not a blocker.

7. **✅ No regex in hot path.** Duration parsing uses a simple char check on the last character + `parseInt`. Cheaper than any regex.

A compiled time filter adds one or two 64-bit float values to the AST. Negligible — roughly 16 bytes per time comparison node.

## URL Integration

Time filters appear in the URL hash query string like any other filter:

```
#/packets?filter=time%20%3E%201h%20ago
#/packets?filter=type%20%3D%3D%20Advert%20%26%26%20time%20%3E%20%222024-01-01%22
```

The filter text is URL-encoded and round-trips through `encodeURIComponent`/`decodeURIComponent`. No special handling needed — the existing filter-in-URL mechanism (#286 or current) works unchanged.

For convenience, a future milestone could add dedicated `timeFrom`/`timeTo` query params that inject into the filter, but this is not required for the initial implementation.

## Wireshark Compatibility

| Wireshark syntax | CoreScope equivalent | Notes |
|------------------|---------------------|-------|
| `frame.time >= "2024-01-01"` | `time >= "2024-01-01"` | We use `time` instead of `frame.time` for brevity. Could alias `frame.time` → `time` later. |
| `frame.time_relative < 60` | `time.ago < 60s` | Wireshark uses seconds float; we use duration literals |
| `frame.time_delta` | Not supported | Inter-packet delta is a different feature |

We intentionally diverge from Wireshark where their syntax is verbose or requires pcap-specific concepts. CoreScope's filter language prioritizes brevity and readability for a web UI. A `frame.time` alias for `time` can be added trivially in the field resolver if users request it.

## Milestones

### M1: Core time filtering (parser + evaluator)
- Add `DURATION`, `AGO`, `BETWEEN` tokens to lexer
- Extend parser for `time` field special handling
- Add `time` and `time.ago` to `resolveField()`
- Implement `between` AST node evaluation
- Unit tests: absolute, relative, ago, between, combined with existing filters, edge cases (bad dates, invalid units)
- **Test:** filter 30K packets by time in <50ms (assert in test)

### M2: UI integration
- Filter bar autocomplete hints for time syntax
- Help tooltip / cheat sheet update with time examples
- Verify URL round-trip with time filters
- Playwright E2E test: enter time filter, verify packet list updates

### M3: Polish
- `frame.time` alias
- Error messages for common mistakes ("did you mean `time > 1h ago`?")
- Consider dedicated time range picker UI widget (out of scope for this spec)

## Testing

### Unit tests (add to `test-packet-filter.js`)

```js
// Absolute time
c = compile('time > "2024-01-01"');
assert(c.filter({ timestamp: new Date('2024-06-01').getTime() }), 'after 2024-01-01');
assert(!c.filter({ timestamp: new Date('2023-06-01').getTime() }), 'before 2024-01-01');

// Relative time
c = compile('time > 1h ago');
assert(c.filter({ timestamp: Date.now() - 30 * 60000 }), '30m ago passes 1h filter');
assert(!c.filter({ timestamp: Date.now() - 2 * 3600000 }), '2h ago fails 1h filter');

// time.ago shorthand
c = compile('time.ago < 30m');
assert(c.filter({ timestamp: Date.now() - 10 * 60000 }), '10m ago < 30m');
assert(!c.filter({ timestamp: Date.now() - 60 * 60000 }), '60m ago not < 30m');

// between
c = compile('time between "2024-01-01" "2024-01-02"');
assert(c.filter({ timestamp: new Date('2024-01-01T12:00:00Z').getTime() }), 'in range');
assert(!c.filter({ timestamp: new Date('2024-01-03').getTime() }), 'out of range');

// Combined
c = compile('type == Advert && time > 1h ago');
assert(c.filter({ payload_type: 4, timestamp: Date.now() - 1000 }), 'combined pass');
assert(!c.filter({ payload_type: 4, timestamp: Date.now() - 7200000 }), 'combined fail time');
assert(!c.filter({ payload_type: 1, timestamp: Date.now() - 1000 }), 'combined fail type');

// Error cases
c = compile('time > "not-a-date"');
assert(c.error, 'invalid date string');

c = compile('time > 5x ago');
assert(c.error, 'invalid duration unit');

// Performance
var start = Date.now();
c = compile('time > 1h ago && type == Advert');
var packets = [];
for (var i = 0; i < 30000; i++) {
  packets.push({ payload_type: i % 5, timestamp: Date.now() - i * 1000 });
}
packets.forEach(function(p) { c.filter(p); });
assert(Date.now() - start < 50, 'filter 30K packets in <50ms');
```

### Playwright tests

- Enter `time > 1h ago` in filter bar → verify packet count decreases
- Enter invalid time filter → verify error message appears
- Reload page with time filter in URL → verify filter is applied
