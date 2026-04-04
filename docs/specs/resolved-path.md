# Spec: Server-side hop resolution at ingest — `resolved_path`

**Status:** Draft  
**Related:** [Neighbor affinity spec](./customizer-rework.md), [#482](https://github.com/Kpa-clawbot/CoreScope/issues/482), [#528](https://github.com/Kpa-clawbot/CoreScope/issues/528)

## Problem

Hop paths are stored as short uppercase hex prefixes in `path_json` (e.g. `["D6", "E3", "59"]`). Resolution to full pubkeys currently happens **client-side** via `HopResolver` (`public/hop-resolver.js`), which:

- Is slow — each page/component re-resolves independently
- Is inconsistent — different components may resolve the same prefix differently
- Cannot leverage the server's neighbor affinity graph, which has far richer context for disambiguation
- Causes redundant `/api/resolve-hops` calls from every client

## Solution

Resolve hop prefixes to full pubkeys **once at ingest time** on the server, using the existing `prefixMap.resolveWithContext()` 4-tier priority system and the `NeighborGraph`. Store the result as a new `resolved_path` field alongside `path_json`.

## Design decisions (locked)

1. **`path_json` stays unchanged** — raw firmware prefixes, uppercase hex. Ground truth.
2. **`resolved_path` is NEW** — array of full 64-char lowercase hex pubkeys (or `null` per hop for unresolved).
3. **Lowercase pubkeys everywhere** — `resolved_path` uses lowercase. All comparison code normalizes to lowercase. `path_json` uppercase stays as-is.
4. **Resolve once at ingest** — no re-resolution. As affinity improves, new packets benefit automatically.
5. **`null` = unresolved** — ambiguous prefixes store `null`. Frontend falls back to prefix display.
6. **Both fields coexist** — not interchangeable. Different consumers use different fields.

## Data model

### Where does `resolved_path` live?

**On observations**, not transmissions.

Rationale: Each observer sees the packet from a different vantage point. The same 2-char prefix may resolve to different full pubkeys depending on which observer's neighborhood is considered. The observer's own pubkey provides critical context for `resolveWithContext` (tier 2: neighbor affinity). Storing on observations preserves this per-observer resolution.

`path_json` already lives on observations in the DB schema. `resolved_path` follows the same pattern.

### Field shape

```
resolved_path TEXT  -- JSON array: ["aabb...64chars", null, "ccdd...64chars"]
```

- Same length as the `path_json` array
- Each element is either a 64-char lowercase hex pubkey string, or `null`
- Stored as a JSON text column (same approach as `path_json`)

## Ingest pipeline changes

### Where resolution happens

In `PacketStore.IngestNewFromDB()` and `PacketStore.IngestNewObservations()` in `cmd/server/store.go`. These methods already read observations from SQLite and build in-memory structures. The resolution step is added **after** the observation is read from DB and **before** it's stored in memory and broadcast.

Resolution flow per observation:
1. Parse `path_json` into hop prefixes
2. Build context pubkeys from the observation (observer pubkey, source/dest from decoded packet)
3. Call `prefixMap.resolveWithContext(hop, contextPubkeys, neighborGraph)` for each hop
4. Store result as `resolved_path` on the in-memory observation struct

### Accessing the affinity graph

The `PacketStore` already holds a `*NeighborGraph` (rebuilt periodically). The `prefixMap` is already built from the node list via `buildPrefixMap()`. Both are available during ingest — no new dependencies needed.

### Performance

`resolveWithContext` does:
- Prefix map lookup (map access, O(1))
- Optional neighbor graph check (small map lookups)
- No DB queries, no network calls

Per-hop cost: ~1–5μs. A typical packet has 0–5 hops. At 100 packets/second ingest rate, this adds <0.5ms total overhead per second. **Negligible.**

## Storage

### Schema change

Add `resolved_path` column to the `observations` table:

```sql
ALTER TABLE observations ADD COLUMN resolved_path TEXT;
```

Applied as a migration in `cmd/ingestor/db.go` (same pattern as existing migrations). Existing rows will have `NULL` for `resolved_path`.

### Who writes `resolved_path`?

**The Go server** (`cmd/server/`), not the ingestor. The ingestor writes raw packets to SQLite without resolution (it doesn't have access to the full node list or neighbor graph). The server resolves during its `IngestNewFromDB`/`IngestNewObservations` polling cycle and writes `resolved_path` back to SQLite via an UPDATE.

Alternative (simpler): Store `resolved_path` **only in memory** on the server's `Observation` struct, never in SQLite. Compute it on read. This avoids schema changes and write-back complexity. The tradeoff: resolution must re-run on every server restart (during the initial DB load). At ~50K observations and ~5μs/hop, this is ~1 second — acceptable.

**Recommendation: In-memory only for v1.** Persist to SQLite in a follow-up if needed.

### WebSocket broadcast

Yes — include `resolved_path` in broadcast messages. The WS broadcast already includes `path_json`; add `resolved_path` alongside it. This is the primary delivery mechanism for live data.

## API changes

### Endpoints that return `resolved_path`

All endpoints that currently return `path_json` should also return `resolved_path`:

- `GET /api/packets` — transmission-level (use best observation's `resolved_path`)
- `GET /api/packets/:hash` — per-observation detail
- `GET /api/packets/:hash/observations` — each observation includes its own `resolved_path`
- `GET /api/observations` — if this endpoint exists
- WebSocket broadcast messages — per-observation

### `/api/resolve-hops`

Keep for now (backward compatibility), but document as **deprecated for most use cases**. Clients should prefer `resolved_path` from packet data. The endpoint remains useful for:
- Ad-hoc resolution of arbitrary prefixes (debug tools)
- Clients that need to resolve prefixes not associated with a packet

## Frontend changes

### Consumers to update

Every component that currently uses `path_json` + client-side `HopResolver` switches to `resolved_path` when available, falling back to `path_json` + `HopResolver` for old packets.

| File | Usage | Change |
|------|-------|--------|
| `packets.js` | Packet detail pane path display | Use `resolved_path` for node names, `path_json` for hex display |
| `map.js` | Route lines between nodes | Use `resolved_path` for coordinates lookup |
| `live.js` | Live packet animation, path rendering | Use `resolved_path` for map lines and labels |
| `analytics.js` | Topology analysis, path statistics | Use `resolved_path` for node-level aggregation |
| `nodes.js` | Node detail path history | Use `resolved_path` for linked node names |
| `hop-resolver.js` | Client-side resolution | Becomes fallback only; still needed for old packets |
| `packet-helpers.js` | Path formatting helpers | Add `resolved_path`-aware helpers |
| `observer-detail.js` | Observer-specific path view | Use observation-level `resolved_path` |
| `traces.js` | Trace visualization | Use `resolved_path` for node identification |
| `audio-lab.js` | Audio path sonification | Use `resolved_path` if path-aware |

### Fallback pattern

```javascript
function getResolvedHops(packet) {
  if (packet.resolved_path) return packet.resolved_path;
  // Fall back to client-side resolution for old packets
  return resolveHopsClientSide(packet.path_json);
}
```

## Migration

### Existing packets

Existing packets/observations won't have `resolved_path`. The frontend gracefully falls back to `path_json` + client-side `HopResolver` (same as today).

### Optional: batch backfill

A one-time background task can resolve existing observations on server startup:
1. Load all observations with `path_json IS NOT NULL AND resolved_path IS NULL`
2. Resolve each using current prefix map + neighbor graph
3. Store results (in-memory or SQLite)

This is low priority — new packets get resolution automatically, and old packets work fine with the fallback.

## Pubkey case standardization

### Standard: lowercase

All pubkeys in `resolved_path` are **lowercase** 64-char hex. This is the canonical form for pubkey comparison throughout the codebase.

### What stays uppercase

- `path_json` prefixes — raw firmware data, always uppercase. Never normalize these.
- Any protocol-level hex display (packet detail, hex breakdown) — show as-is.

### Audit needed

Search for uppercase pubkey comparisons and normalize:
```bash
grep -rn '\.toUpperCase\(\)' public/ cmd/   # Find uppercase conversions
grep -rn 'strings\.ToUpper' cmd/            # Go-side uppercase
```

All pubkey comparisons should use `strings.ToLower()` (Go) or `.toLowerCase()` (JS). Add this as a linting rule or documented convention.

## Implementation milestones

### M1: Server-side resolution (in-memory)
- Add `ResolvedPath []interface{}` to `Observation` struct in `store.go`
- Resolve during `IngestNewFromDB`/`IngestNewObservations`
- Include in WS broadcast and API responses
- Tests: unit test resolution at ingest, verify broadcast shape

### M2: Frontend consumption
- Update `packets.js`, `map.js`, `live.js` to prefer `resolved_path`
- Add fallback to `path_json` + `HopResolver`
- Tests: Playwright tests for path display

### M3: Remaining consumers + cleanup
- Update `analytics.js`, `nodes.js`, `traces.js`, `observer-detail.js`
- Deprecate `/api/resolve-hops` in docs
- Pubkey case audit and normalization
- Tests: full coverage of all path consumers

### M4 (optional): SQLite persistence
- Schema migration to add `resolved_path` column
- Write-back during ingest
- Batch backfill of existing observations
- Survives server restarts without re-resolution
