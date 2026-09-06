# Client RF Environment Samples

Crowdsourced RF environment data from mobile clients: the same phone that samples
[client RX coverage](client-rx-coverage.md) also reads its attached MeshCore companion radio's own
counters — noise floor, RX/TX airtime, CRC errors, packet totals — along the GPS track, and publishes
them on a separate topic. Unlike a fixed observer, which measures one point forever, a roaming
companion builds a noise-floor map, a channel-utilisation map and a CRC-error-rate map as it moves.

## Enabling RF samples (operators)

Off by default. To turn it on:

1. In CoreScope's `config.json`, set `"clientRfSamples": { "enabled": true }` and restart the
   ingestor. Independent of `clientRxCoverage` — a deployment can run one, both, or neither.
2. **Required: an ACL-capable broker**, same as coverage. Bind `meshcore/client/{PUBLIC_KEY}/rf` so
   each client may publish only under its own pubkey. The ingestor already subscribes under
   `meshcore/#`.
3. Optionally set `retention.clientRfDays` to bound the table.

## MQTT topic & payload

Topic: `meshcore/client/{PUBLIC_KEY}/rf` — `{PUBLIC_KEY}` is the companion's pubkey.

```json
{
  "type": "RF_SAMPLE",
  "timestamp": "2026-08-17T10:00:00.000Z",
  "gps": { "lat": 51.2, "lon": 4.4, "acc_m": 8.0 },
  "stationary": false,
  "uptime_secs": 84213,
  "battery_mv": 3950,
  "queue_len": 0,
  "errors": 0,
  "noise_floor": -119,
  "last_rssi": -92,
  "last_snr": -7.0,
  "tx_air_secs": 120,
  "rx_air_secs": 20877,
  "recv": 4310,
  "sent": 12,
  "flood_rx": 3980,
  "direct_rx": 330,
  "flood_tx": 10,
  "direct_tx": 2,
  "recv_errors": 5
}
```

- The discriminator is the `gps` object, same as coverage: a sample without `gps` (or with an
  out-of-range `lat`/`lon`) is dropped.
- `uptime_secs` is required. It is the only reboot/wrap detector the delta query has — see
  [Deltas](#deltas--reboot-and-wrap-handling) below. A sample missing it is dropped and logged, the
  same as a missing GPS fix, rather than silently defaulting to 0.
- `stationary` is optional, defaults to `false` when absent.
- Every counter besides `uptime_secs` is optional. Absent stays SQL `NULL`, never `0` — see
  [recv_errors](#recv_errors-absent-vs-zero) below.
- `errors` is not a counter despite sitting among them here: per the firmware stats frame it is an
  error-flags bitmask, so it is stored as reported but deliberately excluded from `ClientRfDeltas`.
- Subscription: the ingestor's default subscription (`meshcore/#`) already covers this topic. Sources
  configured with an explicit topic list must add `meshcore/client/+/rf`.

## Trust

Identity = the companion pubkey, taken from the `{PUBLIC_KEY}` topic segment — never from a
payload-supplied `origin_id`, which would defeat the ACL trust model. The ingestor rejects any topic
pubkey that is not lowercase hex before writing (same `clientPubkeyRe` used for coverage), and the
observer blacklist is enforced before any write, so a blacklisted operator cannot contribute RF
samples any more than it can contribute coverage.

## `recv_errors`: absent vs. zero

`recv_errors` counts CRC errors. Firmware predating the counter cannot report it at all, and that
case must stay `NULL` — storing `0` would read as "a perfectly clean channel," the opposite of "we
don't know." The app is expected to omit the field entirely (not send a fabricated `0`) on firmware
that doesn't support it, and the handler never collapses an absent optional field to a zero.

## Storage — `client_rf_samples` (ingestor-owned)

```
client_rf_samples(
  id, rx_pubkey, sampled_at, ingested_at, lat, lon, pos_acc_m, stationary,
  uptime_secs, battery_mv, queue_len, errors, noise_floor, last_rssi, last_snr,
  tx_air_secs, rx_air_secs, recv, sent, flood_rx, direct_rx, flood_tx, direct_tx,
  recv_errors,
  UNIQUE(rx_pubkey, sampled_at))   -- idempotent re-ingest
```

Every counter is stored as an absolute cumulative value, exactly as the radio reports it — the
handler does no arithmetic. `errors` is the exception: per the firmware stats frame it is an
error-flags bitmask, not a counter, so it is deliberately excluded from `ClientRfDeltas` — a future
consumer must not wire it into a rate calculation. `sampled_at` is stored at **millisecond** precision (the
`rxTimeMillisLayout` format shared with `client_rx_observations.rx_at`), not the second-resolution
RFC3339 used by `client_receptions.rx_at`. This matters for two reasons: samples can arrive faster
than once per second along a moving track, and Task 6's retention prune compares the cutoff
lexicographically against these stored strings — a second-resolution cutoff against
millisecond-resolution values would delete rows it should keep.

Retention: `retention.clientRfDays` bounds the table by `sampled_at`; `0` disables it (Task 6).

## Deltas — reboot and wrap handling

`Store.ClientRfDeltas(rxPubkey, from, to)` derives consecutive-sample deltas (RX/TX airtime, recv,
recv_errors, wall-clock milliseconds) for one radio over a time range, via a `LAG(...) OVER (ORDER BY
sampled_at)` window query. Every counter in the table is cumulative, so a delta is only meaningful
between two samples from the *same uninterrupted uptime*:

- A pair is skipped whenever `uptime_secs` does not strictly increase between consecutive samples.
  That is a reboot (uptime reset near 0) or a counter wrap, and subtracting across it would produce a
  large negative or a bogus spike. `uptime_secs` has whole-second granularity, so sampling faster than
  1 Hz means roughly every other pair gets skipped this way too (equal, not decreased, `uptime_secs`)
  — that is expected, not a bug: strict-increase is deliberately correct for reboot detection, but it
  means `ClientRfDeltas` effectively assumes callers sample at ≥1 s intervals if they want every
  interval represented.
- The absolute values always stay in `client_rf_samples`; only the delta view drops the
  cross-reboot pair. No row is ever deleted or modified because of this check.
- Each `*Delta` field on `ClientRfDelta` (`RxAirDelta`, `TxAirDelta`, `RecvDelta`, `RecvErrDelta`) is a
  `*int64`, **nil** when either endpoint of the pair is NULL — i.e. the underlying counter is
  unsupported on that firmware. A delta of `0` there would be indistinguishable from a measured zero,
  which for `RecvErrDelta` in particular would render as "clean channel" on a radio that simply can't
  count CRC errors. Consumers must treat nil as unknown, not zero.
- When both endpoints of a pair *are* present, the per-metric delta additionally guards against
  `cur < prev` (covers a wrapped individual counter even when `uptime_secs` itself looks monotonic) by
  clamping to `0` rather than going negative.
- `from`/`to` are compared **lexicographically** against millisecond-precision `sampled_at` strings, so
  callers must pass bounds in the same `rxTimeMillisLayout` format
  (`2006-01-02T15:04:05.000Z07:00`). A bound like `10:00:00Z` would lexicographically exclude a sample
  stored as `10:00:00.500Z` (`.` sorts before `Z`), silently narrowing the range.
- A phone clock running more than 14h ahead has its `timestamp` clamped to ingest time by
  `resolveRxTimeCore` (same clamp used across the client-RX paths), which compresses `WallMillis` for a
  buffered batch uploaded in one burst — several samples with distinct on-device timestamps can collapse
  onto ingest-time values seconds apart. `sampled_at` can never be zero-spaced (the UNIQUE constraint
  drops an exact collision), so this is a wrong-rate hazard, not a divide-by-zero: a consumer computing
  a rate from `*Delta / WallMillis` must sanity-bound `WallMillis` before dividing.

## Configurable values (future customizer)

`retention.clientRfDays` is the only tunable so far; no color/threshold customization applies to this
feature yet (no map rendering built on it in this task).
