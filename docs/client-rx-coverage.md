# Client RX Coverage

Crowdsourced RF coverage from mobile clients: a phone connects over BLE to a MeshCore
*companion* radio, captures which nodes the companion hears (with SNR/RSSI), tags each reception
with the phone's GPS position, and publishes it to MQTT. CoreScope ingests these into
`client_receptions` and renders per-node H3-style hex coverage on the Reach page.

## Companion app — where to get it

The mobile capture side is **[corescope-rx](https://github.com/efiten/corescope-rx)** — an
open-source (GPL-3.0) Android PWA. Operators who enable coverage point their users at it: it connects
over BLE to a MeshCore companion radio, captures directly-heard nodes + the phone's GPS, and publishes
the payload defined below. It's self-hostable and generic — a runtime `config.json` aims it at your
own MQTT broker + CoreScope instance (see its README).

## Enabling coverage (operators)

Coverage is **off by default**. To turn it on:

1. In CoreScope's `config.json`, set `"clientRxCoverage": { "enabled": true }` and restart the server
   and ingestor. This is a **single flag read by both processes** — the ingestor and server each parse
   the same `config.json`, so you set `clientRxCoverage.enabled` once and it gates both the ingest write
   path and the read endpoints. There is no separate per-process flag.
2. **Required: an ACL-capable broker.** Bind `meshcore/client/{PUBLIC_KEY}/packets` so each client may
   publish **only** under its own pubkey (e.g. an EMQX ACL keyed on the connected client's identity).
   This is the trust boundary, not an optimization — see [Trust](#trust). The ingestor already
   subscribes under `meshcore/#`.
3. Optionally set `retention.clientRxDays` to bound the coverage tables (see
   [Storage](#storage--client_receptions-ingestor-owned)).
4. Point your users at [corescope-rx](https://github.com/efiten/corescope-rx) and they start
   contributing. Results show on each node's Reach page (coverage toggle) and the `#/rx-coverage`
   dashboard. **Warn them first that their contribution is world-readable and a per-observer view can
   reconstruct their movements — see [Privacy](#privacy--contributor-location-is-public).**

The rest of this document is the MQTT payload contract the companion app implements.

## Companion BLE source (verified against firmware)

The mobile app's RX data comes from the companion's **`PUSH_CODE_LOG_RX_DATA` (0x88)** BLE frame:
`[0x88][snr×4 int8][rssi int8][raw packet bytes]`. This is emitted for **every** received
packet (promiscuous, incl. overheard flood traffic), not just messages addressed to the device:

- `src/Dispatcher.cpp:198` calls `logRxRaw(getLastSNR(), getLastRSSI(), raw, len)` in `checkRecv()`
  **unconditionally** — NOT behind `#if MESH_PACKET_LOGGING`. So it works on stock firmware.
- `examples/companion_radio/MyMesh.cpp:283` overrides it to write the 0x88 frame whenever the app
  is connected over BLE (`_serial->isConnected()`).

So per received packet the app gets SNR + RSSI + the raw bytes. It decodes the raw packet (standard
MeshCore format) to derive the directly-heard node (`path[last]` or 0-hop advert pubkey) and pairs it
with the phone's GPS. The bare advert push (`PUSH_CODE_ADVERT` 0x80) carries only a pubkey (no SNR/
RSSI/path) and is NOT used — 0x88 already covers adverts (the raw advert is in its payload).

Caveats: 0x88 is only sent while the app is BLE-connected; packets larger than `MAX_FRAME_SIZE` are
skipped; the firmware doc labels 0x88 "can be ignored" (messaging-app view) — for coverage it is the
primary frame. GPS is always the phone's, never the companion's.

## MQTT topic & payload

Topic: `meshcore/client/{PUBLIC_KEY}/packets` — `{PUBLIC_KEY}` is the companion's pubkey. The
broker (EMQX) should ACL-restrict each client to publish only under its own pubkey, which is how
"a connected companion may only inject under the keys that apply" is enforced.

Payload — meshcoretomqtt-compatible packet, plus a `gps` object:

```json
{
  "origin": "<companion name>",
  "origin_id": "<companion pubkey hex>",
  "timestamp": "2026-06-09T12:00:00Z",
  "type": "PACKET",
  "direction": "rx",
  "raw": "<packet hex>",
  "SNR": -7,
  "RSSI": -92,
  "gps": { "lat": 51.05, "lon": 3.72, "acc_m": 8 }
}
```

- The discriminator is the `gps` object. A packet without `gps` is dropped (coverage needs a position).
- `raw` is decoded server-side to derive the directly-heard node and the path; `hash`/`path` fields
  are not required.
- Subscription: the ingestor's default subscription (`meshcore/#`) already covers this topic. Sources
  configured with an explicit topic list must add `meshcore/client/+/packets`.

## Capture HARD RULE — only what was heard directly

The app and ingestor record **only the node the companion physically received**, never upstream
relayers:

- **FLOOD** packet **with a path** (≥1 hop) → record `path[len-1]` (the last forwarder = the
  immediate RF transmitter). Confirmed against firmware `Mesh.cpp` (`routeRecvPacket` appends the
  forwarder's hash to the END of the path) and CoreScope's `neighbor_builder.go:226-228`.
- **DIRECT** packet **with a path** → **NOT attributable, discarded.** Direct forwarders consume the
  next hop from the FRONT (`Mesh.cpp removeSelfFromPath`), so `path[len-1]` is the route's
  destination-side end, NOT the node we heard. Attributing it credits the SNR to the wrong (often
  far-away) node. Only FLOOD routes (0,1) are recorded from a path.
- Packet **with no path** (0 hops) **and** an advert → record the advertiser's full pubkey.
- `direction` must be `rx`. 1-byte (2 hex char) prefixes are excluded (collision-prone, like Reach).
- The RSSI/SNR belong to the directly-received transmission, so they attach to the recorded node.
- The rest of the path is discarded for coverage.

## Storage — `client_receptions` (ingestor-owned)

A roaming companion is a mobile observer with a moving position, so it gets its own table (not
`observations`, which assumes a fixed observer location). Per the #1283 read/write invariant, the
table and all writes live in `cmd/ingestor/`.

```
client_receptions(
  id, rx_pubkey, heard_key, heard_keylen, rssi, snr,
  lat, lon, pos_acc_m, rx_at, ingested_at, src,
  UNIQUE(rx_pubkey, heard_key, rx_at))   -- idempotent re-ingest
```

`heard_keylen` is 32 for a full pubkey (0-hop advert) or 2/3 for a multibyte prefix. `src` is
`advert` or `rxlog`. No hex cell is stored — binning is computed server-side from lat/lon.

Indexes: a composite `(heard_key, heard_keylen, lat, lon)` and a `(lat, lon)` index back the coverage
queries; the per-node query matches a sargable `heard_key IN (pubkey, prefix6, prefix4)` list so the
composite is used instead of a table scan (see the benchmark in `cmd/ingestor`).

Retention: the table grows on every submission, so set `retention.clientRxDays` (ingestor) to delete
rows older than N days (and stale `client_observers`); `0` disables it. Without it the table is
unbounded.

## Diagnostic observations — `client_rx_observations` (ingestor-owned)

The client topic may also carry packets the companion could not attribute to a directly-heard
node — a DIRECT-route packet with a path, for instance (see the capture HARD RULE above). Those
packets are still decodable, and are optionally recorded as a diagnostic RF observation,
independent of whether they produced a coverage row.

**Not literally every decodable packet, though.** A packet still needs a `gps` fix and
`direction: "rx"` to reach the decoder/observation write at all — `handleClientPacket` returns
early (before `DecodePacket` even runs) when `gps` is missing or its `lat`/`lon` don't parse, and
`direction: "tx"` (a companion's own outgoing transmission) is decoded but explicitly excluded
from the observation write, the same as it already was from coverage. A diagnostic table silently
requiring a GPS fix is a bit surprising, so: no `gps` → no observation row either, same constraint
as coverage.

- Written to `client_rx_observations` only, **never** to `client_receptions` — the coverage
  invariant (only directly-heard nodes) is unchanged and unaffected by this feature.
- Gated by its own flag, `"clientRxObservations": { "enabled": true }` — a **top-level** `Config`
  field, not nested inside `clientRxCoverage` in the JSON. It IS gated behind `clientRxCoverage` in
  the *control flow*: `handleClientPacket` (where the observation write lives) is only reached when
  `clientRxCoverage.enabled` is true, so observations require coverage to be enabled even though the
  two keys are siblings on disk:
  ```json
  {
    "clientRxCoverage": { "enabled": true },
    "clientRxObservations": { "enabled": true }
  }
  ```
  Config loading is plain `json.Unmarshal` with no `DisallowUnknownFields`, so nesting
  `clientRxObservations` under `clientRxCoverage` as written above is silently ignored — the key is
  never read, the feature stays off, and nothing logs or errors. An ingestor without
  `clientRxObservations.enabled` simply drops these packets (no table writes, no error).
- Enabling the companion app's `fullRfLog` flag while `clientRxObservations.enabled` is `false` here
  is pure waste: the phone spends mobile data uploading packets this ingestor decodes and discards,
  with no row written and no warning anywhere. `fullRfLog` multiplies normal upload volume — see the
  corescope-rx README.
- The JSON payload shape from the companion app is **unchanged** either way — this is purely an
  ingestor-side decision based on what `raw` decodes to, not a new field the app must send.
- Captures routing detail the coverage path discards: `route_type`, `payload_type`,
  `code1`/`code2` transport codes (route types 0/3 only), `scope_name` (matched against configured
  region keys), `hash_size`, `hop_count`, the full forwarder path (`path_json`), and — for FLOOD
  routes only — the immediate `forwarder`.
- `rx_at` is stored at millisecond precision (unlike `client_receptions.rx_at`), because
  `UNIQUE(rx_pubkey, pkt_hash, rx_at)` deliberately allows multiple rows per `pkt_hash`: each row is
  one forwarder's copy of the same flood, and that multiplicity is the flood-amplification signal
  this table exists to capture. Retention is `retention.clientRxObsDays` (separate from, and
  typically shorter than, `retention.clientRxDays` — this table is diagnostic, not archival).
- `pkt_hash` (`ComputeContentHash`) deliberately excludes both the transport-code bytes and the
  path bytes, so distinctness inside `UNIQUE(rx_pubkey, pkt_hash, rx_at)` rests entirely on `rx_at`.
  On the happy path that's fine — real receive times come from the envelope timestamp at
  millisecond resolution, and same-millisecond collisions aren't physical on a half-duplex LoRa
  radio. But on any fallback path (missing/unparseable/implausible timestamp — see
  `resolveRxTimeCore`), every packet in a buffered upload batch is stamped with the same ingest-time
  `rx_at`, and distinct forwarder copies of one flood inside that batch collapse into a single row
  via `ON CONFLICT DO NOTHING`. Not a correctness bug — the constraint is doing exactly what it's
  told — but it means a buffered/late upload with a bad envelope timestamp under-reports flood
  amplification for that batch. This isn't limited to the server-side fallback path either: the
  companion app stamps `rx_at` at BLE-frame *processing* time, not true RF receive time (`app.js`),
  so two forwarder copies processed in the same millisecond collapse just as effectively even when
  the envelope timestamp itself is fine.
- A 0-hop advert gets `forwarder = NULL` in `client_rx_observations` even though the transmitter is
  known — it's the advert's own pubkey, which the coverage path records separately with
  `src='advert'` (see `client_receptions` above). Don't mistake this `NULL` for "unknown".

## Read API — coverage GeoJSON

`GET /api/nodes/{pubkey}/rx-coverage?bbox={minLat,minLon,maxLat,maxLon}&z={zoom}`

Returns a GeoJSON `FeatureCollection` of hexagons covering where clients heard the node, aggregated
server-side (read-only). Each feature:

```json
{ "type": "Feature",
  "geometry": { "type": "Polygon", "coordinates": [[[lon,lat], ...]] },
  "properties": { "cell": "9:123:-45", "count": 7, "best_snr": -6, "has_sig": true,
                  "nodes": [{ "prefix": "aabbcc", "name": "Alice", "snr": -6, "count": 3 }],
                  "nodes_truncated": false } }
```

- Hex binning is a pure-Go pointy-top grid over Web Mercator (`cmd/server/hexgrid.go`). We do **not**
  use `uber/h3-go` because it is CGO and the project builds with `CGO_ENABLED=0`. Latitude is only
  defined within ±85.05° (Web Mercator limit) and is clamped to that range.
- `z` (Leaflet zoom) selects the hex resolution (zoom-adaptive). Raw points never leave the server
  (privacy: contributors' tracks are not exposed).
- `best_snr` / `has_sig` drive the colour: green→orange by best SNR, grey when no signal metric.
- Features are sorted by `cell` for a deterministic (cacheable) payload.
- **Bounds:** the per-cell `nodes` list is capped (with `nodes_truncated`), and the collection is
  capped at a fixed feature count — when exceeded, the densest cells are kept and the top-level
  `truncated` flag is set. The per-node endpoint also returns `mobile_receptions` and `mobile_clients`
  totals (node-wide, independent of the bbox).

## Frontend

Shown only in the Reach view (`#/nodes/{pubkey}/reach`), as a toggleable hex layer drawn on the
existing Leaflet map (`public/node-reach-coverage.js`), deep-linked via `?coverage=1`. No new
frontend dependencies. Colours come from CSS variables in `public/node-reach.css`
(`--nq-cov-strong|mid|weak|grey`).

## Trust

Identity = the companion pubkey (`rx_pubkey`), taken from the `{PUBLIC_KEY}` topic segment.

**The feature requires an ACL-capable broker.** The reported GPS position is the contributor's own
claim, so the only thing anchoring a reception to a real identity is the broker ACL binding
`meshcore/client/{PUBLIC_KEY}/packets` to the client that holds that key. **Without such an ACL, the
topic — and therefore the GPS and the heard-node attribution — is spoofable:** anyone who can publish
to the broker could inject coverage under any pubkey. Do not enable this feature on an open/no-ACL
broker if you trust the resulting map.

Server/ingestor-side defense-in-depth (these reduce blast radius but do **not** replace the ACL):

- The ingestor rejects any topic pubkey that is not lowercase hex before writing, and never falls back
  to a payload-supplied id (`cmd/ingestor/client_reception.go`, #2/#10).
- A blacklisted operator cannot contribute via the client topic (the blacklist is enforced before the
  coverage write, #1).
- The frontend HTML-escapes the pubkey it renders, so a junk pubkey can't inject markup (#14).
- `/api/nodes/resolve` and coverage tooltips never reveal blacklisted or hidden-prefix node identities
  (#15).

## Privacy — contributor location is public

⚠️ **Enabling coverage publishes contributors' GPS-tagged receptions, and the per-observer view can
reconstruct a contributor's movements.** The hex map is read without authentication. The leaderboard
exposes each companion's pubkey, and clicking one filters the map to that single companion
(`/api/rx-coverage?rx=<pubkey>`); at high zoom over the retention window this is effectively a public
movement trail (home / work / commute) of whoever carries that companion. **A pseudonymous companion
name does not mitigate this** — the *locations themselves* are identifying (overnight clustering = home),
and all of one contributor's points are linked by the pubkey.

This is an accepted tradeoff of the feature, not a bug: fine resolution is what makes the aggregate
coverage map useful, the feature is opt-in and OFF by default, and contributors choose to run the
companion. But the consent must be **informed**:

- **Operators:** tell your users, before they contribute, that their coverage (including a per-observer
  view of their own track) is world-readable for as long as `retention.clientRxDays` keeps it.
- **Contributors:** do not contribute from a device you carry on your person if a public record of where
  you have been is a concern. Use a dedicated/stationary node, or accept that the trail is public.

Operators who want to harden this further can lower `retention.clientRxDays`, run the dashboard behind
their own auth/proxy, or (future hardening) coarsen stored coordinates / apply a k-anonymity threshold
to the per-observer view.

Optional future hardening: have the companion sign a broker-issued token (the firmware exposes
on-device signing) — not required for the MVP, tracked as a follow-up.

## Configurable values (future customizer)

Hardcoded initially, tracked for the customizer per AGENTS.md rule 8: hex resolution per zoom
(`zoomToHexRes`), colour SNR thresholds (`coverageColorVar`), and any `rx_at` max-age validation.
