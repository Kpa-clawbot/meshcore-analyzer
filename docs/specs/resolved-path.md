# Spec: Server-side hop resolution at ingest — `resolved_path`

**Status:** Final  
**Issue:** [#555](https://github.com/Kpa-clawbot/CoreScope/issues/555)  
**Related:** [#482](https://github.com/Kpa-clawbot/CoreScope/issues/482), [#528](https://github.com/Kpa-clawbot/CoreScope/issues/528)

## Problem

Any place where 1, 2, or 3-byte prefixes must be resolved to actual full repeater public keys and friendly names should use affinity data first, geo data as fallback. Across frontend, backend, whatever. Efficiently — no 7-second waits, no recomputation, aggressive caching.

Currently, hop paths are stored as short uppercase hex prefixes in `path_json` (e.g. `["D6", "E3", "59"]`). Resolution to full pubkeys happens **client-side** via `HopResolver` (`public/hop-resolver.js`), which:

- Is slow — each page/component re-resolves independently
- Is inconsistent — different components may resolve the same prefix differently
- Cannot leverage the server's neighbor affinity graph, which has far richer context for disambiguation
- Causes redundant `/api/resolve-hops` calls from every client

## Solution

Resolve hop prefixes to full pubkeys **once at ingest time** on the server, using `resolveWithContext()` with 4-tier priority (affinity → geo → GPS → first match) and a **persisted neighbor graph**. Store the result as a new `resolved_path` column on observations alongside `path_json`.

## Design decisions (locked)

1. **`path_json` stays unchanged** — raw firmware prefixes, uppercase hex. Ground truth.
2. **`resolved_path` is a column on observations** — full 64-char lowercase hex pubkeys, `null` for unresolved.
3. **Resolved at ingest** using `resolveWithContext(hop, context, graph)` — 4-tier priority: affinity → geo → GPS → first match.
4. **`null` = unresolved** — ambiguous prefixes store `null`. Frontend falls back to prefix display.
5. **Both fields coexist** — not interchangeable. Different consumers use different fields.

## Persisted neighbor graph

### SQLite table: `neighbor_edges`

Thin and normalized. Stores ONLY the relationship. SNR, observer names, GPS, roles — all join from existing tables when needed. No duplication.

```sql
CREATE TABLE IF NOT EXISTS neighbor_edges (
    node_a TEXT NOT NULL,
    node_b TEXT NOT NULL,
    count INTEGER DEFAULT 1,
    last_seen TEXT,
    PRIMARY KEY (node_a, node_b)
);
```

### Edge extraction rules (ADVERT vs non-ADVERT)

At ingest, for each packet:

- **ADVERT packets** (payload_type 4): originator pubkey is known from `decoded_json.pubKey`. Extract edge: `originator ↔ path[0]` (the first hop is a direct neighbor of the originator).
- **ALL packets**: observer pubkey is known. Extract edge: `observer ↔ path[last]` (the last hop is a direct neighbor of the observer).
- **Non-ADVERT packets**: originator is unknown (encrypted). ONLY extract `observer ↔ path[last]`.
- Each packet produces **1 or 2 edge upserts** depending on type.

Edge upsert: `INSERT OR REPLACE INTO neighbor_edges` with `count = count + 1` and `last_seen = now`.

### Cold startup and backfill

On startup:

1. **Load `neighbor_edges` from SQLite** → build in-memory graph.
2. **If table empty (first run):** `BuildFromStore(packets)` — scan all existing packets, extract edges per the rules above, INSERT into `neighbor_edges`.
3. **Load observations from SQLite.**
4. **For observations without `resolved_path`:** resolve using the graph, UPDATE `resolved_path` in SQLite.
5. **Ready to serve.**

On subsequent runs, step 2 is skipped (table already populated). Step 4 only processes observations with NULL `resolved_path` (new or previously unresolved).

## Data model

### Where does `resolved_path` live?

**On observations**, as a column:

```sql
ALTER TABLE observations ADD COLUMN resolved_path TEXT;
```

Rationale: Each observer sees the packet from a different vantage point. The same 2-char prefix may resolve to different full pubkeys depending on which observer's neighborhood is considered. The observer's own pubkey provides critical context for `resolveWithContext` (tier 2: neighbor affinity). Storing on observations preserves this per-observer resolution.

`resolved_path` is written in the same INSERT that creates the observation — one write, no double-write problem.

### Field shape

```
resolved_path TEXT  -- JSON array: ["aabb...64chars", null, "ccdd...64chars"]
```

- Same length as the `path_json` array
- Each element is either a 64-char lowercase hex pubkey string, or `null`
- Stored as a JSON text column (same approach as `path_json`)
- Uses `omitempty` — absent from JSON when not set

## Every path resolution uses the graph — no exceptions

All existing `pm.resolve()` call sites MUST be migrated to `resolveWithContext` with the persisted graph. No "we'll get to it later."

### Call sites to migrate (exhaustive)

Found via `grep -n "pm.resolve" cmd/server/store.go`:

| Line | Function | Current | After |
|------|----------|---------|-------|
| 1192 | `IngestNewFromDB()` | `pm.resolve(hop)` | `resolveWithContext(hop, ctx, graph)` — resolve at ingest, store as `resolved_path` |
| 1876 | `buildDistanceIndex()` | `pm.resolve(hop)` | Read `resolved_path` from observation — already resolved at ingest |
| 3537 | `computeAnalyticsTopology()` | `pm.resolve(hop)` | Read `resolved_path` from observation |
| 5528 | `computeAnalyticsSubpaths()` | `pm.resolve(hop)` | Read `resolved_path` from observation |
| 5665 | `GetSubpathDetail()` | `pm.resolve(hop)` | `resolveWithContext(hop, ctx, graph)` — ad-hoc resolution for user-provided hops |
| 5744 | `GetSubpathDetail()` | `pm.resolve(h)` | `resolveWithContext(h, ctx, graph)` — same function, second usage |

**After migration:** `pm.resolve()` (naive prefix-only lookup) is dead code. Remove it. All resolution goes through `resolveWithContext` which uses the persisted neighbor graph for affinity-based disambiguation.

## Ingest pipeline changes

### Where resolution happens

In `PacketStore.IngestNewFromDB()` in `cmd/server/store.go`. Resolution is added **during** the observation INSERT — same write, same transaction.

Resolution flow per observation:
1. Parse `path_json` into hop prefixes
2. Build context pubkeys from the observation (observer pubkey, source/dest from decoded packet)
3. Call `resolveWithContext(hop, contextPubkeys, neighborGraph)` for each hop
4. Store result as `resolved_path` column on the observation (same INSERT)
5. Upsert neighbor edges into `neighbor_edges` table (incremental update)

### Performance

`resolveWithContext` does:
- Prefix map lookup (map access, O(1))
- Optional neighbor graph check (small map lookups)
- No DB queries, no network calls

Per-hop cost: ~1–5μs. A typical packet has 0–5 hops. At 100 packets/second ingest rate, this adds <0.5ms total overhead per second. **Negligible.**

## All consumers use `resolved_path`

| Consumer | Before | After |
|---|---|---|
| Packets detail path names | Client HopResolver (naive) | Read `resolved_path` |
| Map Show Route | Client HopResolver (naive) | Read `resolved_path` |
| Live map animated paths | Client HopResolver (naive) | Read `resolved_path` |
| Node detail paths | Client HopResolver (naive) | Read `resolved_path` |
| Analytics topology | Server `pm.resolve()` (naive) | Read `resolved_path` from observations |
| Analytics subpaths | Server `pm.resolve()` (naive) | Read `resolved_path` from observations |
| Analytics hop distances | Server `pm.resolve()` (naive) | Read `resolved_path` from observations |
| Subpath detail | Server `pm.resolve()` (naive) | `resolveWithContext` with graph |
| Show Neighbors | Server neighbors API | Already correct |
| `/api/resolve-hops` | Server `resolveWithContext` | Already correct |
| Hex breakdown display | `path_json` raw | Unchanged — shows raw bytes |

## WebSocket broadcast

Include `resolved_path` in broadcast messages. Resolution happens before broadcast assembly — negligible latency impact. The WS broadcast already includes `path_json`; `resolved_path` is added alongside it.

## API changes

### Endpoints that return `resolved_path`

All endpoints that currently return `path_json` also return `resolved_path`:

- `GET /api/packets` — transmission-level (use best observation's `resolved_path`)
- `GET /api/packets/:hash` — per-observation detail
- `GET /api/packets/:hash/observations` — each observation includes its own `resolved_path`
- WebSocket broadcast messages — per-observation

### `/api/resolve-hops`

**Kept.** Useful for ad-hoc resolution of arbitrary prefixes (debug tools, clients resolving prefixes not associated with a packet). Not deprecated.

## Pubkey case convention

- **DB/API:** lowercase
- **`path_json` display prefixes:** uppercase (raw firmware)
- **`resolved_path`:** lowercase full pubkeys
- **Comparison code:** normalizes to lowercase

## Backward compatibility

- Old observations without `resolved_path`: resolved during cold startup backfill (step 4). If still `null` after backfill, frontend falls back to client-side HopResolver.
- `resolved_path` field uses `omitempty` — absent from JSON when not set.

### Fallback pattern (frontend)

```javascript
function getResolvedHops(packet) {
  if (packet.resolved_path) return packet.resolved_path;
  // Fall back to client-side resolution for old packets
  return resolveHopsClientSide(packet.path_json);
}
```

## Implementation milestones

### M1: Persist graph to SQLite + load on startup + incremental updates at ingest
- Create `neighbor_edges` table in SQLite (schema above)
- On first run: `BuildFromStore(packets)` — scan all packets, extract edges per ADVERT/non-ADVERT rules, INSERT into table
- On subsequent runs: load from SQLite → build in-memory graph (instant startup)
- Upsert edges incrementally during packet ingest
- Graph lives on `PacketStore`, not `Server`
- Tests: graph persistence, load, incremental update, ADVERT vs non-ADVERT edge extraction

### M2: Add `resolved_path` column to observations + resolve at ingest
- `ALTER TABLE observations ADD COLUMN resolved_path TEXT`
- Add `ResolvedPath []*string` to `Observation` struct
- Resolve during `IngestNewFromDB` — same INSERT, one write
- Cold startup backfill: resolve observations with NULL `resolved_path`, UPDATE in SQLite
- Migrate ALL 6 `pm.resolve()` call sites to `resolveWithContext` or read from `resolved_path`
- Remove dead `pm.resolve()` code
- Tests: unit test resolution at ingest, verify stored values, verify all call sites use graph

### M3: Update all API responses to include `resolved_path`
- Include `resolved_path` in all packet/observation API responses
- Include in WebSocket broadcast messages
- Tests: verify API response shape, WS broadcast shape

### M4: Update frontend consumers to prefer `resolved_path`
- Update `packets.js`, `map.js`, `live.js`, `analytics.js`, `nodes.js`
- Add fallback to `path_json` + `HopResolver` for old packets
- `hop-resolver.js` becomes fallback only
- Tests: Playwright tests for path display
