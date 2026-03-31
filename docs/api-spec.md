# CoreScope — API Contract Specification

> **Authoritative contract.** Both the Node.js and Go backends MUST conform to this spec.
> The frontend relies on these exact shapes. Breaking changes require a spec update first.

**Version:** 1.0.0
**Last updated:** 2025-07-17

---

## Table of Contents

- [Conventions](#conventions)
- [GET /api/stats](#get-apistats)
- [GET /api/health](#get-apihealth)
- [GET /api/perf](#get-apiperf)
- [POST /api/perf/reset](#post-apiperfreset)
- [GET /api/nodes](#get-apinodes)
- [GET /api/nodes/search](#get-apinodessearch)
- [GET /api/nodes/bulk-health](#get-apinodesbulk-health)
- [GET /api/nodes/network-status](#get-apinodesnetwork-status)
- [GET /api/nodes/:pubkey](#get-apinodespubkey)
- [GET /api/nodes/:pubkey/health](#get-apinodespubkeyhealth)
- [GET /api/nodes/:pubkey/paths](#get-apinodespubkeypaths)
- [GET /api/nodes/:pubkey/analytics](#get-apinodespubkeyanalytics)
- [GET /api/packets](#get-apipackets)
- [GET /api/packets/timestamps](#get-apipacketstimestamps)
- [GET /api/packets/:id](#get-apipacketsid)
- [POST /api/packets](#post-apipackets)
- [POST /api/decode](#post-apidecode)
- [GET /api/observers](#get-apiobservers)
- [GET /api/observers/:id](#get-apiobserversid)
- [GET /api/observers/:id/analytics](#get-apiobserversidanalytics)
- [GET /api/channels](#get-apichannels)
- [GET /api/channels/:hash/messages](#get-apichannelshashmessages)
- [GET /api/analytics/rf](#get-apianalyticsrf)
- [GET /api/analytics/topology](#get-apianalyticstopology)
- [GET /api/analytics/channels](#get-apianalyticschannels)
- [GET /api/analytics/distance](#get-apianalyticsdistance)
- [GET /api/analytics/hash-sizes](#get-apianalyticshash-sizes)
- [GET /api/analytics/subpaths](#get-apianalyticssubpaths)
- [GET /api/analytics/subpath-detail](#get-apianalyticssubpath-detail)
- [GET /api/resolve-hops](#get-apiresolve-hops)
- [GET /api/traces/:hash](#get-apitraceshash)
- [GET /api/config/theme](#get-apiconfigtheme)
- [GET /api/config/regions](#get-apiconfigregions)
- [GET /api/config/client](#get-apiconfigclient)
- [GET /api/config/cache](#get-apiconfigcache)
- [GET /api/config/map](#get-apiconfigmap)
- [GET /api/iata-coords](#get-apiiata-coords)
- [GET /api/audio-lab/buckets](#get-apiaudio-labbuckets)
- [WebSocket Messages](#websocket-messages)

---

## Conventions

### Types

| Notation        | Meaning                                              |
|-----------------|------------------------------------------------------|
| `string`        | JSON string                                          |
| `number`        | JSON number (integer or float)                       |
| `boolean`       | `true` / `false`                                     |
| `string (ISO)`  | ISO 8601 timestamp, e.g. `"2025-07-17T04:23:01.000Z"` |
| `string (hex)`  | Hex-encoded bytes, uppercase, e.g. `"4F01A3..."`     |
| `number \| null`| May be `null` when data is unavailable               |
| `[T]`           | JSON array of type `T`; always `[]` when empty, never `null` |
| `object`        | Nested JSON object (shape defined inline)            |

### Null Rules

- Fields marked `| null` may be absent or `null`.
- Array fields MUST be `[]` when empty, NEVER `null`.
- String fields that are "unknown" SHOULD be `null`, not `""`.

### Pagination

Paginated endpoints accept `limit` (default 50) and `offset` (default 0) as query params.
They return `total` (the unfiltered/filtered count before pagination).

### Error Responses

```json
{ "error": "string" }
```

- `400` — Bad request (missing/invalid params)
- `404` — Resource not found

---

## GET /api/stats

Server-wide statistics. Lightweight, cached 10s.

### Response `200`

```jsonc
{
  "totalPackets":        number,       // observation count (legacy name)
  "totalTransmissions":  number | null, // unique transmission count
  "totalObservations":   number,       // total observation records
  "totalNodes":          number,       // active nodes (last 7 days)
  "totalNodesAllTime":   number,       // all nodes ever seen
  "totalObservers":      number,       // observer device count
  "packetsLastHour":     number,       // observations in last hour
  "engine":              "node",       // backend engine identifier
  "version":             string,       // package.json version, e.g. "2.6.0"
  "commit":              string,       // git short SHA or "unknown"
  "counts": {
    "repeaters":         number,       // active repeaters (last 7 days)
    "rooms":             number,
    "companions":        number,
    "sensors":           number
  }
}
```

---

## GET /api/health

Server health and telemetry. Used by monitoring.

### Response `200`

```jsonc
{
  "status":    "ok",
  "engine":    "node",
  "version":   string,
  "commit":    string,
  "uptime":    number,          // seconds
  "uptimeHuman": string,       // e.g. "4h 32m"
  "memory": {
    "rss":       number,       // MB
    "heapUsed":  number,       // MB
    "heapTotal": number,       // MB
    "external":  number        // MB
  },
  "eventLoop": {
    "currentLagMs": number,
    "maxLagMs":     number,
    "p50Ms":        number,
    "p95Ms":        number,
    "p99Ms":        number
  },
  "cache": {
    "entries":    number,
    "hits":       number,
    "misses":     number,
    "staleHits":  number,
    "recomputes": number,
    "hitRate":    number        // percentage (0–100)
  },
  "websocket": {
    "clients":   number        // connected WS clients
  },
  "packetStore": {
    "packets":      number,    // loaded transmissions
    "estimatedMB":  number
  },
  "perf": {
    "totalRequests": number,
    "avgMs":         number,
    "slowQueries":   number,
    "recentSlow": [            // last 5
      {
        "path":   string,
        "ms":     number,
        "time":   string,      // ISO timestamp
        "status": number       // HTTP status
      }
    ]
  }
}
```

---

## GET /api/perf

Detailed performance metrics per endpoint.

### Response `200`

```jsonc
{
  "uptime":        number,          // seconds since perf stats reset
  "totalRequests": number,
  "avgMs":         number,
  "endpoints": {
    "/api/packets": {               // keyed by route path
      "count":  number,
      "avgMs":  number,
      "p50Ms":  number,
      "p95Ms":  number,
      "maxMs":  number
    }
    // ... more endpoints
  },
  "slowQueries": [                  // last 20 queries > 100ms
    {
      "path":   string,
      "ms":     number,
      "time":   string,             // ISO timestamp
      "status": number
    }
  ],
  "cache": {
    "size":       number,
    "hits":       number,
    "misses":     number,
    "staleHits":  number,
    "recomputes": number,
    "hitRate":    number             // percentage (0–100)
  },
  "packetStore": {                  // from PacketStore.getStats()
    "totalLoaded":       number,
    "totalObservations": number,
    "evicted":           number,
    "inserts":           number,
    "queries":           number,
    "inMemory":          number,
    "sqliteOnly":        boolean,
    "maxPackets":        number,
    "estimatedMB":       number,
    "maxMB":             number,
    "indexes": {
      "byHash":            number,
      "byObserver":        number,
      "byNode":            number,
      "advertByObserver":  number
    }
  },
  "sqlite": {
    "dbSizeMB":    number,
    "walSizeMB":   number,
    "freelistMB":  number,
    "walPages":    { "total": number, "checkpointed": number, "busy": number } | null,
    "rows": {
      "transmissions": number,
      "observations":  number,
      "nodes":         number,
      "observers":     number
    }
  },
  "goRuntime": {                    // Go server only
    "heapMB":       number,         // heap allocation in MB
    "sysMB":        number,         // total system memory in MB
    "numGoroutine": number,         // active goroutines
    "numGC":        number,         // completed GC cycles
    "gcPauseMs":    number          // last GC pause in ms
  }
}
```

---

## POST /api/perf/reset

Resets performance counters. Requires API key.

### Headers

- `X-API-Key: <key>` (required if `config.apiKey` is set)

### Response `200`

```json
{ "ok": true }
```

---

## GET /api/nodes

Paginated node list with filtering.

### Query Parameters

| Param      | Type   | Default      | Description                                        |
|------------|--------|--------------|----------------------------------------------------|
| `limit`    | number | `50`         | Page size                                          |
| `offset`   | number | `0`          | Pagination offset                                  |
| `role`     | string | —            | Filter by role: `repeater`, `room`, `companion`, `sensor` |
| `region`   | string | —            | Comma-separated IATA codes for regional filtering  |
| `lastHeard`| string | —            | Recency filter: `1h`, `6h`, `24h`, `7d`, `30d`    |
| `sortBy`   | string | `lastSeen`   | Sort key: `name`, `lastSeen`, `packetCount`        |
| `search`   | string | —            | Substring match on `name`                          |
| `before`   | string | —            | ISO timestamp; only nodes with `first_seen <= before` |

### Response `200`

```jsonc
{
  "nodes": [
    {
      "public_key":    string,           // 64-char hex public key
      "name":          string | null,
      "role":          string,           // "repeater" | "room" | "companion" | "sensor"
      "lat":           number | null,
      "lon":           number | null,
      "last_seen":     string (ISO),
      "first_seen":    string (ISO),
      "advert_count":  number,
      "hash_size":     number | null,    // latest hash size (1–3 bytes)
      "hash_size_inconsistent": boolean, // true if flip-flopping
      "hash_sizes_seen": [number] | undefined, // present only if >1 unique size seen
      "last_heard":    string (ISO) | undefined // from in-memory packets or path relay
    }
  ],
  "total":  number,                      // total matching count (before pagination)
  "counts": {
    "repeaters":  number,                // global counts (not filtered by current query)
    "rooms":      number,
    "companions": number,
    "sensors":    number
  }
}
```

**Notes:**
- `hash_sizes_seen` is only present when more than one hash size has been observed.
- `last_heard` is only present when in-memory data provides a more recent timestamp than `last_seen`.

---

## GET /api/nodes/search

Quick node search for autocomplete/typeahead.

### Query Parameters

| Param | Type   | Required | Description                          |
|-------|--------|----------|--------------------------------------|
| `q`   | string | yes      | Search term (name substring or pubkey prefix) |

### Response `200`

```jsonc
{
  "nodes": [
    {
      "public_key":   string,
      "name":         string | null,
      "role":         string,
      "lat":          number | null,
      "lon":          number | null,
      "last_seen":    string (ISO),
      "first_seen":   string (ISO),
      "advert_count": number
    }
  ]
}
```

Returns `{ "nodes": [] }` when `q` is empty.

---

## GET /api/nodes/bulk-health

Bulk health summary for all nodes. Used by analytics dashboard.

### Query Parameters

| Param    | Type   | Default | Description                                     |
|----------|--------|---------|-------------------------------------------------|
| `limit`  | number | `50`    | Max nodes (capped at 200)                       |
| `region` | string | —       | Comma-separated IATA codes for regional filtering |

### Response `200`

Returns a JSON array (not wrapped in an object):

```jsonc
[
  {
    "public_key": string,
    "name":       string | null,
    "role":       string,
    "lat":        number | null,
    "lon":        number | null,
    "stats": {
      "totalTransmissions": number,
      "totalObservations":  number,
      "totalPackets":       number,   // same as totalTransmissions (backward compat)
      "packetsToday":       number,
      "avgSnr":             number | null,
      "lastHeard":          string (ISO) | null
    },
    "observers": [
      {
        "observer_id":   string,
        "observer_name": string | null,
        "avgSnr":        number | null,
        "avgRssi":       number | null,
        "packetCount":   number
      }
    ]
  }
]
```

**Note:** This is a bare array, not `{ nodes: [...] }`.

---

## GET /api/nodes/network-status

Aggregate network health status counts.

### Query Parameters

| Param    | Type   | Default | Description                         |
|----------|--------|---------|-------------------------------------|
| `region` | string | —       | Comma-separated IATA codes          |

### Response `200`

```jsonc
{
  "total":      number,
  "active":     number,    // within degradedMs threshold
  "degraded":   number,    // between degradedMs and silentMs
  "silent":     number,    // beyond silentMs
  "roleCounts": {
    "repeater":  number,
    "room":      number,
    "companion": number,
    "sensor":    number
    // may include "unknown" if role is missing
  }
}
```

---

## GET /api/nodes/:pubkey

Node detail page data.

### Path Parameters

| Param    | Type   | Description          |
|----------|--------|----------------------|
| `pubkey` | string | Node public key (hex)|

### Response `200`

```jsonc
{
  "node": {
    "public_key":    string,
    "name":          string | null,
    "role":          string,
    "lat":           number | null,
    "lon":           number | null,
    "last_seen":     string (ISO),
    "first_seen":    string (ISO),
    "advert_count":  number,
    "hash_size":     number | null,
    "hash_size_inconsistent": boolean,
    "hash_sizes_seen": [number] | undefined
  },
  "recentAdverts": [Packet]   // last 20 packets for this node, newest first
}
```

Where `Packet` is a transmission object (see [Packet Object](#packet-object)).

### Response `404`

```json
{ "error": "Not found" }
```

---

## GET /api/nodes/:pubkey/health

Detailed health information for a single node.

### Response `200`

```jsonc
{
  "node": {                          // full node row
    "public_key":   string,
    "name":         string | null,
    "role":         string,
    "lat":          number | null,
    "lon":          number | null,
    "last_seen":    string (ISO),
    "first_seen":   string (ISO),
    "advert_count": number
  },
  "observers": [
    {
      "observer_id":   string,
      "observer_name": string | null,
      "packetCount":   number,
      "avgSnr":        number | null,
      "avgRssi":       number | null,
      "iata":          string | null
    }
  ],
  "stats": {
    "totalTransmissions": number,
    "totalObservations":  number,
    "totalPackets":       number,    // same as totalTransmissions (backward compat)
    "packetsToday":       number,
    "avgSnr":             number | null,
    "avgHops":            number,    // rounded integer
    "lastHeard":          string (ISO) | null
  },
  "recentPackets": [                 // last 20 packets, observations stripped
    {
      // Packet fields (see Packet Object) minus `observations`
      "observation_count": number    // added for display
    }
  ]
}
```

### Response `404`

```json
{ "error": "Not found" }
```

---

## GET /api/nodes/:pubkey/paths

Path analysis for a node — all paths containing this node's prefix.

### Response `200`

```jsonc
{
  "node": {
    "public_key": string,
    "name":       string | null,
    "lat":        number | null,
    "lon":        number | null
  },
  "paths": [
    {
      "hops": [
        {
          "prefix": string,        // raw hex hop prefix
          "name":   string,        // resolved node name
          "pubkey": string | null,
          "lat":    number | null,
          "lon":    number | null
        }
      ],
      "count":      number,        // times this path was seen
      "lastSeen":   string (ISO) | null,
      "sampleHash": string         // hash of a sample packet using this path
    }
  ],
  "totalPaths":         number,    // unique path signatures
  "totalTransmissions": number     // total transmissions with this node in path
}
```

### Response `404`

```json
{ "error": "Not found" }
```

---

## GET /api/nodes/:pubkey/analytics

Per-node analytics over a time range.

### Query Parameters

| Param  | Type   | Default | Description              |
|--------|--------|---------|--------------------------|
| `days` | number | `7`     | Lookback window (1–365)  |

### Response `200`

```jsonc
{
  "node": {                          // full node row (same shape as nodes table)
    "public_key": string, "name": string | null, "role": string,
    "lat": number | null, "lon": number | null,
    "last_seen": string (ISO), "first_seen": string (ISO), "advert_count": number
  },
  "timeRange": {
    "from": string (ISO),
    "to":   string (ISO),
    "days": number
  },
  "activityTimeline": [
    { "bucket": string (ISO),  "count": number }   // hourly buckets
  ],
  "snrTrend": [
    {
      "timestamp":     string (ISO),
      "snr":           number,
      "rssi":          number | null,
      "observer_id":   string | null,
      "observer_name": string | null
    }
  ],
  "packetTypeBreakdown": [
    { "payload_type": number, "count": number }
  ],
  "observerCoverage": [
    {
      "observer_id":   string,
      "observer_name": string | null,
      "packetCount":   number,
      "avgSnr":        number | null,
      "avgRssi":       number | null,
      "firstSeen":     string (ISO),
      "lastSeen":      string (ISO)
    }
  ],
  "hopDistribution": [
    { "hops": string, "count": number }    // "0", "1", "2", "3", "4+"
  ],
  "peerInteractions": [
    {
      "peer_key":    string,
      "peer_name":   string,
      "messageCount": number,
      "lastContact": string (ISO)
    }
  ],
  "uptimeHeatmap": [
    { "dayOfWeek": number, "hour": number, "count": number }  // 0=Sun, 0–23
  ],
  "computedStats": {
    "availabilityPct":    number,     // 0–100
    "longestSilenceMs":   number,
    "longestSilenceStart": string (ISO) | null,
    "signalGrade":        string,     // "A", "A-", "B+", "B", "C", "D"
    "snrMean":            number,
    "snrStdDev":          number,
    "relayPct":           number,     // % of packets with >1 hop
    "totalPackets":       number,
    "uniqueObservers":    number,
    "uniquePeers":        number,
    "avgPacketsPerDay":   number
  }
}
```

### Response `404`

```json
{ "error": "Not found" }
```

---

## GET /api/packets

Paginated packet (transmission) list with filtering.

### Query Parameters

| Param        | Type   | Default | Description                                        |
|--------------|--------|---------|----------------------------------------------------|
| `limit`      | number | `50`    | Page size                                          |
| `offset`     | number | `0`     | Pagination offset                                  |
| `type`       | string | —       | Filter by payload type (number or name)            |
| `route`      | string | —       | Filter by route type                               |
| `region`     | string | —       | Filter by region (IATA code substring)             |
| `observer`   | string | —       | Filter by observer ID                              |
| `hash`       | string | —       | Filter by packet hash                              |
| `since`      | string | —       | ISO timestamp lower bound                          |
| `until`      | string | —       | ISO timestamp upper bound                          |
| `node`       | string | —       | Filter by node pubkey                              |
| `nodes`      | string | —       | Comma-separated pubkeys (multi-node filter)        |
| `order`      | string | `DESC`  | Sort direction: `asc` or `desc`                    |
| `groupByHash`| string | —       | Set to `"true"` for grouped response               |
| `expand`     | string | —       | Set to `"observations"` to include observation arrays |

### Response `200` (default)

```jsonc
{
  "packets": [Packet],    // see Packet Object below (observations stripped unless expand=observations)
  "total":   number,
  "limit":   number,
  "offset":  number
}
```

### Response `200` (groupByHash=true)

```jsonc
{
  "packets": [
    {
      "hash":              string,
      "first_seen":        string (ISO),
      "count":             number,       // observation count
      "observer_count":    number,       // unique observers
      "latest":            string (ISO),
      "observer_id":       string | null,
      "observer_name":     string | null,
      "path_json":         string | null,
      "payload_type":      number,
      "route_type":        number,
      "raw_hex":           string (hex),
      "decoded_json":      string | null,
      "observation_count": number,
      "snr":               number | null,
      "rssi":              number | null
    }
  ],
  "total": number
}
```

### Response `200` (nodes=... multi-node)

```jsonc
{
  "packets": [Packet],
  "total":   number,
  "limit":   number,
  "offset":  number
}
```

---

## GET /api/packets/timestamps

Lightweight endpoint returning only timestamps for timeline sparklines.

### Query Parameters

| Param   | Type   | Required | Description                       |
|---------|--------|----------|-----------------------------------|
| `since` | string | yes      | ISO timestamp lower bound         |

### Response `200`

Returns a JSON array of timestamps (strings or numbers):

```jsonc
["2025-07-17T00:00:01.000Z", "2025-07-17T00:00:02.000Z", ...]
```

### Response `400`

```json
{ "error": "since required" }
```

---

## GET /api/packets/:id

Single packet detail with byte breakdown and observations.

### Path Parameters

| Param | Type   | Description                                              |
|-------|--------|----------------------------------------------------------|
| `id`  | string | Packet ID (numeric) or 16-char hex hash                  |

### Response `200`

```jsonc
{
  "packet": Packet,                  // full packet/transmission object
  "path":   [string],                // parsed path hops (from packet.paths or [])
  "breakdown": {                     // byte-level packet structure
    "ranges": [
      {
        "start":  number,            // byte offset
        "end":    number,
        "label":  string,
        "hex":    string,
        "value":  string | number | null
      }
    ]
  } | null,
  "observation_count": number,
  "observations": [
    {
      "id":              number,
      "transmission_id": number,
      "hash":            string,
      "observer_id":     string | null,
      "observer_name":   string | null,
      "direction":       string | null,
      "snr":             number | null,
      "rssi":            number | null,
      "score":           number | null,
      "path_json":       string | null,
      "timestamp":       string (ISO),
      "raw_hex":         string (hex),
      "payload_type":    number,
      "decoded_json":    string | null,
      "route_type":      number
    }
  ]
}
```

### Response `404`

```json
{ "error": "Not found" }
```

---

## POST /api/packets

Ingest a raw packet. Requires API key.

### Headers

- `X-API-Key: <key>` (required if `config.apiKey` is set)

### Request Body

```jsonc
{
  "hex":      string,        // required — raw hex-encoded packet
  "observer": string | null, // observer ID
  "snr":      number | null,
  "rssi":     number | null,
  "region":   string | null, // IATA code
  "hash":     string | null  // pre-computed content hash
}
```

### Response `200`

```jsonc
{
  "id":      number,         // packet/observation ID
  "decoded": {               // full decode result
    "header":  DecodedHeader,
    "path":    DecodedPath,
    "payload": object
  }
}
```

### Response `400`

```json
{ "error": "hex is required" }
```

---

## POST /api/decode

Decode a raw packet without storing it.

### Request Body

```jsonc
{
  "hex": string              // required — raw hex-encoded packet
}
```

### Response `200`

```jsonc
{
  "decoded": {
    "header":  DecodedHeader,
    "path":    DecodedPath,
    "payload": object
  }
}
```

### Response `400`

```json
{ "error": "hex is required" }
```

---

## GET /api/observers

List all observers with packet counts.

### Response `200`

```jsonc
{
  "observers": [
    {
      "id":              string,
      "name":            string | null,
      "iata":            string | null,      // region code
      "last_seen":       string (ISO),
      "first_seen":      string (ISO),
      "packet_count":    number,
      "model":           string | null,      // hardware model
      "firmware":        string | null,
      "client_version":  string | null,
      "radio":           string | null,
      "battery_mv":      number | null,      // millivolts
      "uptime_secs":     number | null,
      "noise_floor":     number | null,      // dBm
      "packetsLastHour": number,             // computed, not from DB
      "lat":             number | null,      // from matched node
      "lon":             number | null,      // from matched node
      "nodeRole":        string | null       // from matched node
    }
  ],
  "server_time": string (ISO)                // server's current time
}
```

---

## GET /api/observers/:id

Single observer detail.

### Response `200`

```jsonc
{
  "id":              string,
  "name":            string | null,
  "iata":            string | null,
  "last_seen":       string (ISO),
  "first_seen":      string (ISO),
  "packet_count":    number,
  "model":           string | null,
  "firmware":        string | null,
  "client_version":  string | null,
  "radio":           string | null,
  "battery_mv":      number | null,
  "uptime_secs":     number | null,
  "noise_floor":     number | null,
  "packetsLastHour": number
}
```

### Response `404`

```json
{ "error": "Observer not found" }
```

---

## GET /api/observers/:id/analytics

Per-observer analytics.

### Query Parameters

| Param  | Type   | Default | Description              |
|--------|--------|---------|--------------------------|
| `days` | number | `7`     | Lookback window          |

### Response `200`

```jsonc
{
  "timeline": [
    { "label": string, "count": number }    // bucketed by hours/days
  ],
  "packetTypes": {
    "4": number,                             // keyed by payload_type number
    "5": number
  },
  "nodesTimeline": [
    { "label": string, "count": number }    // unique nodes per time bucket
  ],
  "snrDistribution": [
    { "range": string, "count": number }    // e.g. "6 to 8"
  ],
  "recentPackets": [Packet]                 // last 20 enriched observations
}
```

---

## GET /api/channels

List decoded channels with message counts.

### Query Parameters

| Param    | Type   | Default | Description                         |
|----------|--------|---------|-------------------------------------|
| `region` | string | —       | Comma-separated IATA codes          |

### Response `200`

```jsonc
{
  "channels": [
    {
      "hash":         string,        // channel name (used as key)
      "name":         string,        // decoded channel name
      "lastMessage":  string | null, // text of most recent message
      "lastSender":   string | null, // sender of most recent message
      "messageCount": number,
      "lastActivity": string (ISO)
    }
  ]
}
```

---

## GET /api/channels/:hash/messages

Messages for a specific channel.

### Path Parameters

| Param  | Type   | Description                 |
|--------|--------|-----------------------------|
| `hash` | string | Channel name (from /api/channels) |

### Query Parameters

| Param    | Type   | Default | Description     |
|----------|--------|---------|-----------------|
| `limit`  | number | `100`   | Page size       |
| `offset` | number | `0`     | Pagination offset (from end) |

### Response `200`

```jsonc
{
  "messages": [
    {
      "sender":           string,
      "text":             string,
      "timestamp":        string (ISO),
      "sender_timestamp": number | null,    // device timestamp (unreliable)
      "packetId":         number,
      "packetHash":       string,
      "repeats":          number,           // dedup count
      "observers":        [string],         // observer names
      "hops":             number,
      "snr":              number | null
    }
  ],
  "total": number                           // total deduplicated messages
}
```

---

## GET /api/analytics/rf

RF signal analytics.

### Query Parameters

| Param    | Type   | Default | Description                         |
|----------|--------|---------|-------------------------------------|
| `region` | string | —       | Comma-separated IATA codes          |

### Response `200`

```jsonc
{
  "totalPackets":       number,      // observations with SNR data
  "totalAllPackets":    number,      // all regional observations
  "totalTransmissions": number,      // unique transmission hashes
  "snr": {
    "min":    number,
    "max":    number,
    "avg":    number,
    "median": number,
    "stddev": number
  },
  "rssi": {
    "min":    number,
    "max":    number,
    "avg":    number,
    "median": number,
    "stddev": number
  },
  "snrValues":  Histogram,           // pre-computed histogram (20 bins)
  "rssiValues": Histogram,           // pre-computed histogram (20 bins)
  "packetSizes": Histogram,          // pre-computed histogram (25 bins)
  "minPacketSize": number,           // bytes
  "maxPacketSize": number,
  "avgPacketSize": number,
  "packetsPerHour": [
    { "hour": string, "count": number }   // "2025-07-17T04"
  ],
  "payloadTypes": [
    { "type": number, "name": string, "count": number }
  ],
  "snrByType": [
    { "name": string, "count": number, "avg": number, "min": number, "max": number }
  ],
  "signalOverTime": [
    { "hour": string, "count": number, "avgSnr": number }
  ],
  "scatterData": [
    { "snr": number, "rssi": number }    // max 500 points
  ],
  "timeSpanHours": number
}
```

### Histogram Shape

```jsonc
{
  "bins": [
    { "x": number, "w": number, "count": number }
  ],
  "min": number,
  "max": number
}
```

---

## GET /api/analytics/topology

Network topology analytics.

### Query Parameters

| Param    | Type   | Default | Description                         |
|----------|--------|---------|-------------------------------------|
| `region` | string | —       | Comma-separated IATA codes          |

### Response `200`

```jsonc
{
  "uniqueNodes": number,
  "avgHops":     number,
  "medianHops":  number,
  "maxHops":     number,
  "hopDistribution": [
    { "hops": number, "count": number }      // capped at 25
  ],
  "topRepeaters": [
    {
      "hop":    string,         // raw hex prefix
      "count":  number,
      "name":   string | null,  // resolved name
      "pubkey": string | null
    }
  ],
  "topPairs": [
    {
      "hopA":    string,
      "hopB":    string,
      "count":   number,
      "nameA":   string | null,
      "nameB":   string | null,
      "pubkeyA": string | null,
      "pubkeyB": string | null
    }
  ],
  "hopsVsSnr": [
    { "hops": number, "count": number, "avgSnr": number }
  ],
  "observers": [
    { "id": string, "name": string }
  ],
  "perObserverReach": {
    "<observer_id>": {
      "observer_name": string,
      "rings": [
        {
          "hops": number,
          "nodes": [
            {
              "hop":       string,
              "name":      string | null,
              "pubkey":    string | null,
              "count":     number,
              "distRange": string | null   // e.g. "1-3" or null if constant
            }
          ]
        }
      ]
    }
  },
  "multiObsNodes": [
    {
      "hop":    string,
      "name":   string | null,
      "pubkey": string | null,
      "observers": [
        {
          "observer_id":   string,
          "observer_name": string,
          "minDist":       number,
          "count":         number
        }
      ]
    }
  ],
  "bestPathList": [
    {
      "hop":           string,
      "name":          string | null,
      "pubkey":        string | null,
      "minDist":       number,
      "observer_id":   string,
      "observer_name": string
    }
  ]
}
```

---

## GET /api/analytics/channels

Channel analytics.

### Query Parameters

| Param    | Type   | Default | Description                         |
|----------|--------|---------|-------------------------------------|
| `region` | string | —       | Comma-separated IATA codes          |

### Response `200`

```jsonc
{
  "activeChannels": number,
  "decryptable":    number,
  "channels": [
    {
      "hash":       string,
      "name":       string,
      "messages":   number,
      "senders":    number,        // unique sender count
      "lastActivity": string (ISO),
      "encrypted":  boolean
    }
  ],
  "topSenders": [
    { "name": string, "count": number }
  ],
  "channelTimeline": [
    { "hour": string, "channel": string, "count": number }
  ],
  "msgLengths": [number]            // raw array of message character lengths
}
```

---

## GET /api/analytics/distance

Hop distance analytics.

### Query Parameters

| Param    | Type   | Default | Description                         |
|----------|--------|---------|-------------------------------------|
| `region` | string | —       | Comma-separated IATA codes          |

### Response `200`

```jsonc
{
  "summary": {
    "totalHops":  number,
    "totalPaths": number,
    "avgDist":    number,      // km, 2 decimal places
    "maxDist":    number       // km
  },
  "topHops": [
    {
      "fromName": string,
      "fromPk":   string,
      "toName":   string,
      "toPk":     string,
      "dist":     number,      // km
      "type":     string,      // "R↔R" | "C↔R" | "C↔C"
      "snr":      number | null,
      "hash":     string,
      "timestamp": string (ISO)
    }
  ],
  "topPaths": [
    {
      "hash":      string,
      "totalDist": number,     // km
      "hopCount":  number,
      "timestamp": string (ISO),
      "hops": [
        {
          "fromName": string,
          "fromPk":   string,
          "toName":   string,
          "toPk":     string,
          "dist":     number
        }
      ]
    }
  ],
  "catStats": {
    "R↔R": { "count": number, "avg": number, "median": number, "min": number, "max": number },
    "C↔R": { "count": number, "avg": number, "median": number, "min": number, "max": number },
    "C↔C": { "count": number, "avg": number, "median": number, "min": number, "max": number }
  },
  "distHistogram": Histogram | [],   // empty array if no data
  "distOverTime": [
    { "hour": string, "avg": number, "count": number }
  ]
}
```

---

## GET /api/analytics/hash-sizes

Hash size analysis across the network.

### Query Parameters

| Param    | Type   | Default | Description                         |
|----------|--------|---------|-------------------------------------|
| `region` | string | —       | Comma-separated IATA codes          |

### Response `200`

```jsonc
{
  "total": number,              // packets analyzed
  "distribution": {
    "1": number,                // 1-byte hash count
    "2": number,                // 2-byte hash count
    "3": number                 // 3-byte hash count
  },
  "hourly": [
    { "hour": string, "1": number, "2": number, "3": number }
  ],
  "topHops": [
    {
      "hex":    string,         // raw hop hex
      "size":   number,         // bytes (ceil(hex.length/2))
      "count":  number,
      "name":   string | null,
      "pubkey": string | null
    }
  ],
  "multiByteNodes": [
    {
      "name":     string,
      "hashSize": number,
      "packets":  number,
      "lastSeen": string (ISO),
      "pubkey":   string | null
    }
  ]
}
```

---

## GET /api/analytics/subpaths

Subpath frequency analysis.

### Query Parameters

| Param    | Type   | Default | Description                            |
|----------|--------|---------|----------------------------------------|
| `minLen` | number | `2`     | Minimum subpath length (≥2)            |
| `maxLen` | number | `8`     | Maximum subpath length                 |
| `limit`  | number | `100`   | Max results                            |
| `region` | string | —       | Comma-separated IATA codes             |

### Response `200`

```jsonc
{
  "subpaths": [
    {
      "path":    string,        // "Node A → Node B → Node C"
      "rawHops": [string],      // ["aa", "bb", "cc"]
      "count":   number,
      "hops":    number,        // length of subpath
      "pct":     number         // percentage of totalPaths (0–100)
    }
  ],
  "totalPaths": number
}
```

---

## GET /api/analytics/subpath-detail

Detailed stats for a specific subpath.

### Query Parameters

| Param  | Type   | Required | Description                         |
|--------|--------|----------|-------------------------------------|
| `hops` | string | yes      | Comma-separated raw hex hop prefixes |

### Response `200`

```jsonc
{
  "hops":  [string],                     // input hops echoed back
  "nodes": [
    {
      "hop":    string,
      "name":   string,
      "lat":    number | null,
      "lon":    number | null,
      "pubkey": string | null
    }
  ],
  "totalMatches": number,
  "firstSeen":    string (ISO) | null,
  "lastSeen":     string (ISO) | null,
  "signal": {
    "avgSnr":  number | null,
    "avgRssi": number | null,
    "samples": number
  },
  "hourDistribution": [number],         // 24-element array (index = UTC hour)
  "parentPaths": [
    { "path": string, "count": number }
  ],
  "observers": [
    { "name": string, "count": number }
  ]
}
```

---

## GET /api/resolve-hops

Resolve path hop hex prefixes to node names with regional disambiguation.

### Query Parameters

| Param       | Type   | Required | Description                              |
|-------------|--------|----------|------------------------------------------|
| `hops`      | string | yes      | Comma-separated hex hop prefixes         |
| `observer`  | string | no       | Observer ID for regional context         |
| `originLat` | number | no       | Origin latitude for distance-based disambiguation |
| `originLon` | number | no       | Origin longitude                         |

### Response `200`

```jsonc
{
  "resolved": {
    "<hop>": {
      "name":         string | null,
      "pubkey":       string | null,
      "ambiguous":    boolean | undefined,   // true if multiple candidates
      "unreliable":   boolean | undefined,   // true if failed sanity check
      "candidates":   [Candidate],
      "conflicts":    [Candidate],
      "globalFallback": boolean | undefined,
      "filterMethod": string | undefined,    // "geo" | "observer"
      "hopBytes":     number | undefined,    // for ambiguous entries
      "totalGlobal":  number | undefined,
      "totalRegional": number | undefined,
      "filterMethods": [string] | undefined
    }
  },
  "region": string | null
}
```

**Candidate shape:**

```jsonc
{
  "name":         string,
  "pubkey":       string,
  "lat":          number | null,
  "lon":          number | null,
  "regional":     boolean,
  "filterMethod": string,
  "distKm":       number | null
}
```

---

## GET /api/traces/:hash

All observations of a specific packet hash, sorted chronologically.

### Path Parameters

| Param  | Type   | Description    |
|--------|--------|----------------|
| `hash` | string | Packet hash    |

### Response `200`

```jsonc
{
  "traces": [
    {
      "observer":      string | null,   // observer_id
      "observer_name": string | null,
      "time":          string (ISO),
      "snr":           number | null,
      "rssi":          number | null,
      "path_json":     string | null
    }
  ]
}
```

---

## GET /api/config/theme

Theme and branding configuration (merged from config.json + theme.json).

### Response `200`

```jsonc
{
  "branding": {
    "siteName": string,          // default: "CoreScope"
    "tagline":  string           // default: "Real-time MeshCore LoRa mesh network analyzer"
    // ... additional branding keys from config/theme files
  },
  "theme": {
    "accent":      string,       // hex color, default "#4a9eff"
    "accentHover": string,
    "navBg":       string,
    "navBg2":      string
    // ... additional theme CSS values
  },
  "themeDark": {
    // dark mode overrides (may be empty object)
  },
  "nodeColors": {
    "repeater":  string,         // hex color
    "companion": string,
    "room":      string,
    "sensor":    string,
    "observer":  string
  },
  "typeColors": {
    // payload type → hex color overrides
  },
  "home": object | null          // home page customization
}
```

---

## GET /api/config/regions

Available regions (IATA codes) merged from config + DB.

### Response `200`

```jsonc
{
  "<iata_code>": string          // code → display name
  // e.g. "SFO": "San Francisco", "LAX": "Los Angeles"
}
```

Returns a flat key-value object.

---

## GET /api/config/client

Client-side configuration values.

### Response `200`

```jsonc
{
  "roles":              object | null,
  "healthThresholds":   object | null,
  "tiles":              object | null,
  "snrThresholds":      object | null,
  "distThresholds":     object | null,
  "maxHopDist":         number | null,
  "limits":             object | null,
  "perfSlowMs":         number | null,
  "wsReconnectMs":      number | null,
  "cacheInvalidateMs":  number | null,
  "externalUrls":       object | null,
  "propagationBufferMs": number          // default: 5000
}
```

---

## GET /api/config/cache

Cache TTL configuration (raw values in seconds).

### Response `200`

Returns the raw `cacheTTL` object from `config.json`, or `{}` if not set:

```jsonc
{
  "stats":                number | undefined,    // seconds
  "nodeDetail":           number | undefined,
  "nodeHealth":           number | undefined,
  "nodeList":             number | undefined,
  "bulkHealth":           number | undefined,
  "networkStatus":        number | undefined,
  "observers":            number | undefined,
  "channels":             number | undefined,
  "channelMessages":      number | undefined,
  "analyticsRF":          number | undefined,
  "analyticsTopology":    number | undefined,
  "analyticsChannels":    number | undefined,
  "analyticsHashSizes":   number | undefined,
  "analyticsSubpaths":    number | undefined,
  "analyticsSubpathDetail": number | undefined,
  "nodeAnalytics":        number | undefined,
  "nodeSearch":           number | undefined,
  "invalidationDebounce": number | undefined
}
```

---

## GET /api/config/map

Map default center and zoom.

### Response `200`

```jsonc
{
  "center": [number, number],      // [lat, lon], default [37.45, -122.0]
  "zoom":   number                 // default 9
}
```

---

## GET /api/iata-coords

IATA airport/region coordinates for client-side regional filtering.

### Response `200`

```jsonc
{
  "coords": {
    "<iata_code>": {
      "lat": number,
      "lon": number,
      "radiusKm": number
    }
  }
}
```

---

## GET /api/audio-lab/buckets

Representative packets bucketed by payload type for audio lab.

### Response `200`

```jsonc
{
  "buckets": {
    "<type_name>": [
      {
        "hash":              string,
        "raw_hex":           string (hex),
        "decoded_json":      string | null,
        "observation_count": number,
        "payload_type":      number,
        "path_json":         string | null,
        "observer_id":       string | null,
        "timestamp":         string (ISO)
      }
    ]
  }
}
```

---

## WebSocket Messages

### Connection

Connect to `ws://<host>` (or `wss://<host>` for HTTPS). No authentication.
The server broadcasts messages to all connected clients.

### Message Wrapper

All WebSocket messages use this envelope:

```jsonc
{
  "type": string,     // "packet" or "message"
  "data": object      // payload (shape depends on type)
}
```

### Message Type: `"packet"`

Broadcast on every new packet ingestion.

```jsonc
{
  "type": "packet",
  "data": {
    "id":                number,           // observation or transmission ID
    "raw":               string (hex) | null,
    "decoded": {
      "header": {
        "routeType":       number,
        "payloadType":     number,
        "payloadVersion":  number,
        "payloadTypeName": string          // "ADVERT", "GRP_TXT", "TXT_MSG", etc.
      },
      "path": {
        "hops":            [string]        // hex hop prefixes
      },
      "payload":           object          // decoded payload (varies by type)
    },
    "snr":               number | null,
    "rssi":              number | null,
    "hash":              string | null,
    "observer":          string | null,    // observer_id
    "observer_name":     string | null,
    "path_json":         string | null,    // JSON-stringified hops array
    "packet":            Packet | undefined, // full packet object (when available)
    "observation_count": number | undefined
  }
}
```

**Notes:**
- `data.decoded` is always present with at least `header.payloadTypeName`.
- `data.packet` is included for raw packet ingestion (Format 1 / MQTT), may be absent for companion bridge messages.
- `data.path_json` is the JSON-stringified version of `data.decoded.path.hops`.

#### Fields consumed by frontend pages:

| Field                     | live.js | packets.js | app.js | channels.js |
|---------------------------|---------|------------|--------|-------------|
| `data.id`                 | ✓       | ✓          |        |             |
| `data.hash`               | ✓       | ✓          |        |             |
| `data.raw`                | ✓       |            |        |             |
| `data.decoded.header.payloadTypeName` | ✓ | ✓   |        |             |
| `data.decoded.payload`    | ✓       | ✓          |        |             |
| `data.decoded.path.hops`  | ✓       |            |        |             |
| `data.snr`                | ✓       |            |        |             |
| `data.rssi`               | ✓       |            |        |             |
| `data.observer`           | ✓       |            |        |             |
| `data.observer_name`      | ✓       |            |        |             |
| `data.packet`             |         | ✓          |        |             |
| `data.observation_count`  |         | ✓          |        |             |
| `data.path_json`          | ✓       |            |        |             |
| (any)                     |         |            | ✓ (*)  |             |

(*) `app.js` passes all messages to registered `wsListeners` and uses them only for cache invalidation.

### Message Type: `"message"`

Broadcast for GRP_TXT (channel message) packets only. Same `data` shape as `"packet"` type.
`channels.js` listens for this type to update the channel message feed in real time.

```jsonc
{
  "type": "message",
  "data": {
    // identical shape to "packet" data
  }
}
```

---

## Shared Object Shapes

### Packet Object

A transmission/packet as stored in memory and returned by most endpoints:

```jsonc
{
  "id":                number,              // transmission ID
  "raw_hex":           string (hex) | null,
  "hash":              string,              // content hash (dedup key)
  "first_seen":        string (ISO),        // when first observed
  "timestamp":         string (ISO),        // display timestamp (= first_seen)
  "route_type":        number,              // 0=DIRECT, 1=FLOOD, 2=reserved, 3=TRANSPORT
  "payload_type":      number,              // 0=REQ, 1=RESPONSE, 2=TXT_MSG, 3=ACK, 4=ADVERT, 5=GRP_TXT, 7=ANON_REQ, 8=PATH, 9=TRACE, 11=CONTROL
  "payload_version":   number | null,
  "decoded_json":      string | null,       // JSON-stringified decoded payload
  "observation_count": number,
  "observer_id":       string | null,       // from "best" observation
  "observer_name":     string | null,
  "snr":               number | null,
  "rssi":              number | null,
  "path_json":         string | null,       // JSON-stringified hop array
  "direction":         string | null,
  "score":             number | null,
  "observations":      [Observation] | undefined  // stripped by default on list endpoints
}
```

### Observation Object

A single observation of a transmission by an observer:

```jsonc
{
  "id":              number,
  "transmission_id": number,
  "hash":            string,
  "observer_id":     string | null,
  "observer_name":   string | null,
  "direction":       string | null,
  "snr":             number | null,
  "rssi":            number | null,
  "score":           number | null,
  "path_json":       string | null,
  "timestamp":       string (ISO) | number,  // ISO string or unix epoch
  // Enriched fields (from parent transmission):
  "raw_hex":         string (hex) | null,
  "payload_type":    number,
  "decoded_json":    string | null,
  "route_type":      number
}
```

### DecodedHeader

```jsonc
{
  "routeType":       number,
  "payloadType":     number,
  "payloadVersion":  number,
  "payloadTypeName": string    // human-readable name
}
```

### DecodedPath

```jsonc
{
  "hops":      [string],       // hex hop prefixes, e.g. ["a1b2", "c3d4"]
  "hashSize":  number,         // bytes per hop hash (1–3)
  "hashCount": number          // number of hops in path field
}
```

---

## Payload Type Reference

| Value | Name       | Description                      |
|-------|------------|----------------------------------|
| 0     | `REQ`      | Request                          |
| 1     | `RESPONSE` | Response                         |
| 2     | `TXT_MSG`  | Direct text message              |
| 3     | `ACK`      | Acknowledgement                  |
| 4     | `ADVERT`   | Node advertisement               |
| 5     | `GRP_TXT`  | Group/channel text message       |
| 7     | `ANON_REQ` | Anonymous request                |
| 8     | `PATH`     | Path / traceroute                |
| 9     | `TRACE`    | Trace response                   |
| 11    | `CONTROL`  | Control message                  |

## Route Type Reference

| Value | Name        | Description                          |
|-------|-------------|--------------------------------------|
| 0     | `DIRECT`    | Direct (with transport codes)        |
| 1     | `FLOOD`     | Flood/broadcast                      |
| 2     | (reserved)  |                                      |
| 3     | `TRANSPORT` | Transport (with transport codes)     |
