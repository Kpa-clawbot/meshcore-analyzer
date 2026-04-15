# Repeater Liveness — Design Spec

**Issue:** Kpa-clawbot/CoreScope#662
**Date:** 2026-04-15
**Status:** Approved

---

## Problem

CoreScope conflates two distinct repeater states into "active":

| State | Adverts | In paths | Meaning |
|---|---|---|---|
| Relaying | Recent | Yes | Up and forwarding traffic |
| Alive but idle | Recent | No | Up, but nothing routed through it |
| Down | None | None | Offline |

An operator cannot tell whether a repeater is healthy and carrying traffic, or healthy but not actually relaying anything.

---

## Scope

M1 (backend relay metrics) + M2 (frontend three-state indicator). M3 (repeater dashboard) is out of scope for this implementation.

---

## Architecture

### Data Structure (Backend)

Add a parallel index to `PacketStore` in `store.go`:

```go
relayTimes map[string][]int64  // lowercase pubkey → sorted []int64 unix-millis
```

- Indexed by **full pubkeys only** (from `tx.ResolvedPath`) — not raw hop prefixes, avoiding hash-collision noise
- Maintained under the existing `s.mu` RWMutex — no new lock needed
- Lives parallel to `byPathHop`, sharing the same add/remove call sites

### Index Maintenance

**On add** — called from `addTxToPathHopIndex`:
- For each non-nil entry in `tx.ResolvedPath`
- Parse `tx.FirstSeen` → unix millis
- Binary-search insert into sorted slice

**On remove** — called from `removeTxFromPathHopIndex`:
- For each non-nil entry in `tx.ResolvedPath`
- Remove the millis value from the sorted slice

**On build** — called from `buildPathHopIndex`:
- Populate `relayTimes` in the same pass

### Query (O(log n))

```go
now := time.Now().UnixMilli()
times := s.relayTimes[lowerPK]
count1h  := len(times) - sort.Search(len(times), func(i int) bool { return times[i] >= now-3600000 })
count24h := len(times) - sort.Search(len(times), func(i int) bool { return times[i] >= now-86400000 })
lastRelayed := times[len(times)-1]  // free — last element of sorted slice
```

---

## API

No new endpoints. Relay metrics are added to existing health responses.

### `GET /api/nodes/bulk-health` and `GET /api/nodes/{pubkey}/health`

Added to the `stats` sub-object, **repeater nodes only** (fields absent for other roles):

```json
{
  "stats": {
    "lastHeard": "2026-04-15T10:00:00Z",
    "packetsToday": 12,
    "relay_count_1h": 3,
    "relay_count_24h": 47,
    "last_relayed": "2026-04-15T09:58:00Z"
  }
}
```

`last_relayed` is an RFC3339 string (consistent with existing timestamp fields). Omitted when `relayTimes[pk]` is empty.

---

## Frontend

### `roles.js` — extend `getNodeStatus`

```js
// Signature change:
// Before: getNodeStatus(role, lastSeenMs) → 'active' | 'stale'
// After:  getNodeStatus(role, lastSeenMs, relayCount24h) → 'relaying' | 'active' | 'stale'
```

Logic:
- Non-repeaters: `relayCount24h` ignored, returns `'active'` or `'stale'` as before — **no behaviour change**
- Repeaters:
  - Stale threshold exceeded → `'stale'`
  - Within threshold + `relayCount24h > 0` → `'relaying'`
  - Within threshold + `relayCount24h == 0` → `'active'` (alive but idle)

### `nodes.js` — `getStatusInfo` and render

- Extract `relay_count_24h`, `relay_count_1h`, `last_relayed` from `n.stats`
- Pass `relay_count_24h` into `getNodeStatus`

**Status labels (repeaters):**

| Status | Label | Explanation |
|---|---|---|
| `'relaying'` | `🟢 Relaying` | `"Relayed N packets in last 24h, last X ago"` |
| `'active'` | `🟡 Idle` | `"Alive but no relay traffic in 24h"` |
| `'stale'` | `⚪ Stale` | existing text |

**Non-repeaters:** labels unchanged (`🟢 Active` / `⚪ Stale`).

**Node detail pane:** relay stats row added to the stats table for repeaters:
- `Relay (1h)` / `Relay (24h)` / `Last Relayed`
- Row hidden for non-repeater roles

**Status filter buttons:** `'relaying'` maps to the `active` bucket — filter UI unchanged.

---

## Testing

### Backend (Go) — new test cases in `store_test.go` or dedicated file

- `addTxToRelayTimeIndex`: insert packets with known timestamps → verify sorted order
- Count at 1h/24h boundaries: packets straddling the window edge → correct counts
- `removeFromRelayTimeIndex`: add then remove → slice returns to original state
- `GetBulkHealth` relay fields: repeater with relay activity → fields present; companion → fields absent
- Eviction: add packets, evict oldest → relay_count_24h drops correctly
- `last_relayed`: equals timestamp of most recently relayed packet
- Empty `relayTimes`: no panic, fields omitted from response
- Node with no pubkeys in `ResolvedPath`: `relayTimes` unchanged (raw hops ignored)

### Frontend (Node.js) — `test-repeater-liveness.js`

- `getNodeStatus('repeater', recentMs, 5)` → `'relaying'`
- `getNodeStatus('repeater', recentMs, 0)` → `'active'`
- `getNodeStatus('repeater', staleMs, 5)` → `'stale'`
- `getNodeStatus('companion', recentMs, 0)` → `'active'` (no three-state for non-repeaters)
- `getNodeStatus('companion', recentMs, 99)` → `'active'` (relay count ignored for non-repeaters)
- Status label: `'relaying'` → `🟢 Relaying`
- Status label: `'active'` on repeater → `🟡 Idle`

---

## Limitations

1. **Observer coverage gaps**: if no observer hears traffic through a repeater, relay activity won't be recorded even if the repeater is relaying. Inherent to passive observation.
2. **Low-traffic networks**: zero relay activity ≠ broken. The "Idle" label must be clearly worded.
3. **Hash collisions**: mitigated by indexing full pubkeys only (resolved path), not raw hop prefixes.
4. **Memory**: `relayTimes` adds one `int64` per relay event per node. Bounded by store packet limit — acceptable.
