# Scope Stats Page — Design Spec

**Issue**: Kpa-clawbot/CoreScope#899  
**Date**: 2026-04-23  
**Branch target**: `master`

---

## Overview

Add a dedicated **Scopes** page showing scope/region statistics for MeshCore transport-route packets. Scope filtering in MeshCore uses `TRANSPORT_FLOOD` (route_type 0) and `TRANSPORT_DIRECT` (route_type 3) packets that carry two 16-bit transport codes. Code1 ≠ `0000` means the packet is region-scoped.

Feature 3 from the issue (default scope per client via advert) is **not implemented** — the advert format has no scope field in the current firmware.

---

## How Scopes Work (Firmware)

Transport code derivation (authoritative source: `meshcore-dev/MeshCore`):

```
key  = SHA256("#regionname")[:16]          // TransportKeyStore::getAutoKeyFor
Code1 = HMAC-SHA256(key, type || payload)  // TransportKey::calcTransportCode, 2-byte output
```

Code1 is a **per-message** HMAC — the same region produces a different Code1 for every message. Identifying a region from Code1 requires knowing the region name in advance and recomputing the HMAC.

`Code1 = 0000` is the "no scope" sentinel (also `FFFF` is reserved). Packets with route_type 1 or 2 (plain FLOOD/DIRECT) carry no transport codes.

---

## Config

Add `hashRegions` to the ingestor `Config` struct in `cmd/ingestor/config.go`, mirroring `hashChannels`:

```json
"hashRegions": ["#belgium", "#eu", "#brussels"]
```

Normalization (same rules as `hashChannels`):
- Trim whitespace
- Prepend `#` if missing
- Skip empty entries

---

## Ingestor Changes

### Key derivation (`loadRegionKeys`)

```go
func loadRegionKeys(cfg *Config) map[string][]byte {
    // key = first 16 bytes of SHA256("#regionname")
}
```

Returns `map[string][]byte` (region name → 16-byte HMAC key). Called once at startup, stored on the `Store`.

### Decoder: expose raw payload bytes

Add `PayloadRaw []byte` to `DecodedPacket` in `cmd/ingestor/decoder.go`. Populated from the raw `buf` slice at the payload offset — zero-copy slice, no allocation. This is the **encrypted** payload bytes, matching what the firmware feeds into `calcTransportCode`.

### At-ingest region matching

In `BuildPacketData`:
- Skip if `route_type` not in `{0, 3}` → `scope_name` stays `nil`
- If `Code1 == "0000"` → `scope_name = nil` (unscoped transport, no scope involvement)
- If `Code1 != "0000"` → try each region key:
  ```
  HMAC-SHA256(key, payloadType_byte || PayloadRaw)  → first 2 bytes as uint16
  ```
  First match → `scope_name = "#regionname"`. No match → `scope_name = ""` (unknown scope).

Add `ScopeName *string` to `PacketData`.

### MQTT-sourced packets (DM / CHAN paths in main.go)

These are injected directly without going through `BuildPacketData`. They use `route_type = 1` (FLOOD), so they are never transport-route packets. No scope matching needed for these paths.

---

## Database

### Migration

```sql
ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL;
CREATE INDEX idx_tx_scope_name ON transmissions(scope_name) WHERE scope_name IS NOT NULL;
```

### Column semantics

| Value | Meaning |
|-------|---------|
| `NULL` | Either: non-transport-route packet (route_type 1/2), or transport-route with Code1=0000 |
| `""` (empty string) | Transport-route, Code1 ≠ 0000, but no configured region matched |
| `"#belgium"` | Matched named region |

The API stats queries resolve the NULL ambiguity by always filtering `route_type IN (0, 3)` first:
- `unscoped` count = `route_type IN (0,3) AND scope_name IS NULL`
- `scoped` count = `route_type IN (0,3) AND scope_name IS NOT NULL`

### Backfill

On migration, re-decode `raw_hex` for all rows where `route_type IN (0, 3)` and `scope_name IS NULL`. Run the same HMAC matching logic. Rows with `Code1 = 0000` remain `NULL`.

The backfill runs in the existing migration framework in `cmd/ingestor/db.go`. If no regions are configured, backfill is skipped.

---

## API

### `GET /api/scope-stats`

**Query param**: `window` — one of `1h`, `24h` (default), `7d`

**Time-series bucket sizes**:
| Window | Bucket |
|--------|--------|
| `1h`   | 5 min  |
| `24h`  | 1 hour |
| `7d`   | 6 hours|

**Response**:
```json
{
  "window": "24h",
  "summary": {
    "transportTotal": 1240,
    "scoped": 890,
    "unscoped": 350,
    "unknownScope": 42
  },
  "byRegion": [
    { "name": "#belgium", "count": 612 },
    { "name": "#eu",      "count": 236 }
  ],
  "timeSeries": [
    { "t": "2026-04-23T10:00:00Z", "scoped": 45, "unscoped": 18 },
    { "t": "2026-04-23T11:00:00Z", "scoped": 51, "unscoped": 22 }
  ]
}
```

- `transportTotal` = `scoped + unscoped` (transport-route packets only)
- `scoped` = Code1 ≠ 0000 (named + unknown)
- `unscoped` = transport-route with Code1 = 0000
- `unknownScope` = scoped but no region name matched (subset of `scoped`)
- `byRegion` sorted by count descending, excludes unknown
- `timeSeries` covers the full window at the bucket granularity

Route: `GET /api/scope-stats` registered in `cmd/server/routes.go`.  
No auth required (same as other read endpoints).  
TTL cache: 30 seconds (heavier query than `/api/stats`).

---

## Frontend

### Placement

Add a **"Scopes" tab** to the existing Analytics page (`public/analytics.js`) — no new nav item, no new JS file. Tab button added after the existing "Clock Health" tab in the `analyticsTabs` div.

Deep-link: `#/analytics?tab=scopes`

### Tab content — three sections

**1. Summary cards** (reuse existing Analytics card CSS)  
- Transport total, Scoped, Unscoped, Unknown scope  
- Each card shows count + percentage of transport total

**2. Per-region table**  
Columns: Region, Messages, % of Scoped  
Sorted by count descending. Last row: "Unknown scope" (italic) if `unknownScope > 0`.  
Shows a "No regions configured — add `hashRegions` to your config" hint if `byRegion` is empty and `unknownScope = 0`.

**3. Time-series chart**  
- Window selector: `1h / 24h / 7d` (default 24h)  
- Two lines: **Scoped** (blue) and **Unscoped** (grey)  
- Uses the same inline SVG chart helpers already present in `analytics.js` (no external lib)

---

## Testing

- Unit tests for `loadRegionKeys`: normalization, key bytes match firmware SHA256 derivation
- Unit tests for HMAC matching: known Code1 value computed from firmware logic, verified against Go implementation
- Integration test: ingest a synthetic transport-route packet with a known region, assert `scope_name` column is set correctly
- API test: `GET /api/scope-stats` returns correct summary counts against fixture DB

---

## Out of Scope

- Feature 3 (default scope per client via advert) — firmware has no advert scope field
- Drill-down from region row to filtered packet list (deferred)
- Private regions (`$`-prefixed) — use secret keys not publicly derivable
