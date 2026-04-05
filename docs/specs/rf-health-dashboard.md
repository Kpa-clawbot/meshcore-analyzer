# Spec: RF Health Dashboard — Observer Radio Metrics

**Status:** Draft v3
**Purpose:** Enable operators to quickly identify RF jammers, deaf receivers, and radio health issues through per-observer time-series charts.

## Prerequisite Gate

**Before building anything, verify that stats messages arrive periodically from observers.**

The ingestor must receive radio stats messages at a predictable interval via MQTT. Confirmed: status messages arrive every ~5 minutes per observer.

**Verification steps (M0):**
1. Connect ≥3 observers to the MQTT bridge
2. Log all incoming stats messages with timestamps for 24h
3. Confirm messages arrive at a regular interval (expected: every few minutes)
4. If stats are NOT periodic, stop — a stats-request mechanism must be added to the MQTT bridge first (separate spec)
5. **Verify `triggerNoiseFloorCalibrate()` firing frequency.** If it fires on every stats cycle, noise floor readings may be artificially consistent (measuring calibration, not environment). If it fires only on boot, the first sample after reboot is unreliable — document which behavior the firmware uses.

Do not proceed to M1 until this gate passes.

## Problem

Operators currently have no visibility into RF environment quality over time. A jammer could be active for hours before anyone notices degraded mesh performance. A deaf receiver silently drops packets with no alert. There's no way to distinguish "the mesh is quiet" from "my observer can't hear anything."

## Solution

A new Analytics tab ("RF Health") showing per-observer time-series charts for noise floor, TX airtime, RX airtime, and receive errors over configurable time windows (1h to 30d, plus custom from/to range). Automated pattern detection (M3+) flags anomalies and suggests diagnoses after operators have used raw charts to provide feedback.

## Data Model

### New table: `observer_metrics`

```sql
CREATE TABLE IF NOT EXISTS observer_metrics (
    observer_id TEXT NOT NULL,
    timestamp TEXT NOT NULL,       -- ISO 8601, rounded to nearest sample interval
    noise_floor REAL,              -- dBm, from radio stats (nullable — may arrive without airtime)
    tx_air_secs INTEGER,           -- cumulative TX seconds since boot (nullable)
    rx_air_secs INTEGER,           -- cumulative RX seconds since boot (nullable)
    packets_sent INTEGER,          -- cumulative packets sent since boot (nullable)
    packets_recv INTEGER,          -- cumulative packets received since boot (nullable)
    recv_errors INTEGER,           -- cumulative CRC/decode failures since boot (nullable)
    battery_mv INTEGER,            -- battery voltage in millivolts (nullable, for field/solar nodes)
    PRIMARY KEY (observer_id, timestamp)
);
```

**Field notes:**

- **`recv_errors`** (CRC failure count) is the strongest single indicator of channel quality. A rising error rate with stable noise floor points to in-band digital interference rather than broadband jamming. This is more diagnostic than packet_count alone.
- **`packets_sent` / `packets_recv`** are tracked separately because the ratio reveals asymmetric link problems (e.g., observer can transmit but not receive, or vice versa). The old `packet_count` field conflated these.
- **`battery_mv`** is nullable and only relevant for field/solar deployments. Low battery causes erratic radio behavior (reduced TX power, missed RX windows) that looks like RF problems but isn't. Charting voltage alongside RF metrics prevents misdiagnosis.
- All cumulative counters (`tx_air_secs`, `rx_air_secs`, `packets_sent`, `packets_recv`, `recv_errors`) reset on reboot — see reboot handling below.

No additional indexes. The composite primary key covers all query patterns (per-observer time-range scans). At 70K rows, a full scan for any fleet-wide time query is fast enough.

### Clock source

**Always use the ingestor's wall clock for timestamps, not observer-reported timestamps.** Observer clocks may be wrong, drifted, or absent (no RTC). Round the ingestor wall clock to the nearest sample interval boundary (e.g., 5-minute marks) for consistent time alignment.

### Noise floor cold start caveat

**The first noise floor sample after a reboot may be unreliable.** The radio's noise floor reading requires settling time and may reflect calibration artifacts rather than the actual RF environment. Mark the first post-reboot sample with a `reboot` flag (see reboot handling) so the frontend can annotate it. Do not use first-post-reboot noise floor samples in baseline/median calculations.

### Sampling strategy

- **Interval:** Every 5 minutes (configurable via config.json `metrics.sampleIntervalSec`, default 300)
- **Source:** MQTT stats messages (`STATS_TYPE_RADIO`)
- **Insertion:** `INSERT OR REPLACE INTO observer_metrics (observer_id, timestamp, ...) VALUES (?, ?, ...)` with timestamp rounded to the nearest interval boundary. No need to track last-insert time per observer — rounding + `INSERT OR REPLACE` is idempotent and naturally deduplicates.
- **Storage:** ~10K rows/day for 35 observers. At configurable retention. Negligible.
- **Retention:** Configurable, configurable, default 30 days. Prune with a single `DELETE FROM observer_metrics WHERE timestamp < datetime('now', '-N days')` on startup and every 24h. Consider `PRAGMA auto_vacuum = INCREMENTAL` for embedded devices.

### Gap detection

If the time between two consecutive samples for an observer exceeds 2× the sample interval (e.g., >10 minutes for a 5-min interval), insert null values in the response to indicate a gap. This prevents charts from drawing misleading interpolation lines across outages.

### Reboot handling

Cumulative counters (`tx_air_secs`, `rx_air_secs`, `packets_sent`, `packets_recv`, `recv_errors`) reset on device reboot. Detect counter resets (current value < previous value) and:
1. Skip the delta computation for that interval (do not produce a negative value)
2. Log a reboot event for the observer with the timestamp
3. Use the current sample as the new baseline for subsequent deltas
4. **Include reboot timestamps in the API response** so the frontend can render them as annotations directly on the chart (see frontend design)
5. **Flag the first post-reboot noise floor sample** as potentially unreliable (cold start — see above)

### Delta computation (server-side)

Cumulative counters are converted to per-interval rates server-side. **Deltas are computed server-side, not in the frontend.** The API returns percentage/rate values directly. This keeps firmware implementation details (cumulative counters, reboot semantics) out of the UI layer, reduces payload size, and centralizes reboot-handling logic.

### Graceful degradation

Not all observers may report all metrics. If fields are absent:
- Store `NULL` for missing columns
- The API returns `null` for unavailable fields
- The frontend shows only the charts for which data exists — missing charts are hidden, not broken
- Status detection uses only available metrics
- `battery_mv` is expected to be absent on mains-powered observers — this is normal, not an error

Partial data is always better than no data. Never error or crash on missing optional fields.

### Required ingestor changes

1. Parse `tx_air_secs`, `rx_air_secs`, `packets_sent`, `packets_recv`, `recv_errors`, and `battery_mv` from MQTT stats messages (same pattern as existing `noise_floor`)
2. On each stats message, round ingestor wall clock to nearest interval, `INSERT OR REPLACE` into `observer_metrics`
3. Handle missing fields gracefully (insert NULLs for absent metrics)
4. Detect counter resets and record reboot events
5. Add new columns to `observers` table for current/latest values

### API endpoints

```
GET /api/observers/{id}/metrics?since=2026-04-04T00:00:00Z&until=2026-04-05T00:00:00Z&resolution=5m
```

**`resolution` query parameter** controls downsampling:
- `5m` (default) — raw samples
- `1h` — hourly aggregates (`GROUP BY strftime('%Y-%m-%dT%H:00:00', timestamp)` with MIN/MAX/AVG)
- `1d` — daily aggregates

Use `1h` resolution for 7d views to avoid shipping 2,016 points per observer. Essential for the fleet comparison view (35 observers × 2,016 = 70K points at raw resolution → 35 × 168 = 5,880 points at 1h resolution).

Returns:
```json
{
  "observer_id": "1F445B...",
  "observer_name": "GY889 Repeater",
  "reboots": ["2026-04-04T03:15:00Z", "2026-04-04T18:22:00Z"],
  "metrics": [
    {
      "timestamp": "2026-04-04T00:00:00Z",
      "noise_floor": -112.5,
      "tx_airtime_pct": 2.1,
      "rx_airtime_pct": 8.3,
      "packets_sent": 42,
      "packets_recv": 342,
      "recv_errors": 3,
      "recv_error_rate": 0.87,
      "battery_mv": 3720,
      "is_reboot_sample": false
    }
  ]
}
```

Notes:
- `tx_airtime_pct` and `rx_airtime_pct` are server-computed deltas as percentages. Null if airtime data unavailable.
- `recv_error_rate` = `recv_errors / (packets_recv + recv_errors)` as a percentage. Null if either field unavailable.
- `packets_sent` and `packets_recv` are per-interval deltas (not cumulative). Null if unavailable.
- `reboots` array contains timestamps of detected reboots within the queried window, for chart annotation.
- `is_reboot_sample` flags first-post-reboot samples where noise floor may be unreliable.
- `battery_mv` is null for mains-powered observers.

```
GET /api/observers/metrics/summary?window=24h
```

**Fleet summary is cached incrementally.** Maintain a rolling summary struct in memory, updated on each new sample insert (35 observers × 1 sample/5min = 7 inserts/min — trivially cheap). The endpoint reads from the cached struct, not from SQLite queries on every request.

Returns:
```json
{
  "observers": [
    {
      "observer_id": "1F445B...",
      "observer_name": "GY889 Repeater",
      "current_noise_floor": -112.5,
      "avg_noise_floor_24h": -114.2,
      "max_noise_floor_24h": -95.0,
      "tx_airtime_pct_24h": 2.1,
      "rx_airtime_pct_24h": 8.3,
      "recv_error_rate_24h": 0.87,
      "battery_mv": 3720,
      "status": "normal"
    }
  ]
}
```

## Frontend Design

### Design Principles

The dashboard exists for one purpose: **let an operator glance at it at 3 AM and know immediately if something is wrong.** Every design decision follows from this. Decoration that doesn't serve comprehension is removed. Data that can be shown is shown — not hidden behind clicks or hovers.

Key rules (per Tufte):
- **Maximize data-ink ratio.** Every pixel must encode data or directly support reading it. Remove anything that doesn't.
- **No chartjunk.** No gradient fills, no 3D effects, no decorative borders, no ornamental chrome.
- **Labels on the data, not in legends.** Direct-label lines, annotate anomalies at the point they occur. The viewer should never look away from the data to understand it.
- **Show data variation, not design variation.** All observer charts use identical scales, formats, and typography. If two charts look different, it's because the data is different.
- **Respect the viewer's intelligence.** Dense, information-rich displays are fine. Oversimplified displays waste screen space and the operator's time.

### Page structure: small multiples grid

```
Analytics → RF Health tab
├── Time range: [1h] [3h] [6h] [12h] [24h] [3d] [7d] [30d] [Custom ▾]
│   ├── Presets: click to quick-set
│   └── Custom: two datetime inputs (from/to) with calendar picker
│       └── URL hash reflects selected range for deep linking
│
├── Small Multiples Grid (ALL observers, one cell per observer)
│   │
│   │  Each cell contains:
│   │  ┌─────────────────────────────────────────┐
│   │  │ GY889 Repeater          -112.5 dBm  3.7V│  ← name, current NF, battery (if field node)
│   │  │ ┈┈┈╲┈┈┈┈┈┈╱┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈│  ← noise floor sparkline (24h)
│   │  │ err: 0.8%  TX: 2.1%  RX: 8.3%          │  ← key rates, inline text
│   │  │ ▲reboot 03:15                           │  ← reboot annotation (if any)
│   │  └─────────────────────────────────────────┘
│   │
│   │  Sorted by: worst status first, then highest noise floor
│   │  Grid: 3–4 columns on desktop, 2 on tablet, 1 on phone
│   │  Click any cell → expand to full detail below
│   │
│   └── Entire grid is visible at once — no pagination, no "show more"
│       (35 observers × ~60px per cell = ~700px — fits on one screen)
│
├── Expanded Detail (shown below grid when a cell is clicked)
│   │
│   │  Three time-aligned charts, stacked vertically, sharing X-axis:
│   │
│   │  1. Noise Floor (dBm)
│   │     - SVG line chart, Y-axis inverted (higher dBm = worse = higher on chart)
│   │     - Thin reference lines at -100 dBm and -85 dBm, directly labeled
│   │       (e.g., "−100 warning" / "−85 critical") — no color bands
│   │     - Gaps (nulls) break the line — no interpolation across outages
│   │     - Reboot markers: vertical hairline at each reboot timestamp,
│   │       labeled "reboot" directly on the chart
│   │     - First-post-reboot sample marked with open circle (unreliable cold start)
│   │     - Direct labels on notable points (min, max, anomalies)
│   │
│   │  2. Airtime (%) — hidden if no airtime data
│   │     - Two separate SVG lines (NOT stacked area — stacked areas
│   │       make it impossible to read the lower series accurately)
│   │     - TX line and RX line, directly labeled at their endpoints
│   │       ("TX 2.1%" / "RX 8.3%") — no legend box
│   │     - Same X-axis as noise floor chart above
│   │     - Gaps shown as breaks
│   │
│   │  3. Channel Quality
│   │     - Receive error rate (%) as a line
│   │     - Packets recv as a light step-line for context
│   │     - Directly labeled — no legend
│   │     - High error rate + low packet count = dead channel
│   │     - High error rate + high packet count = interference
│   │
│   │  4. Battery Voltage (shown only if battery_mv is non-null)
│   │     - Simple line chart, mV scale
│   │     - Directly labeled with current value
│   │     - Useful for correlating RF anomalies with low-battery behavior
│   │
│   │  All four charts share the same X-axis and time range.
│   │  Reboot markers appear as vertical hairlines across ALL charts
│   │  (same event, visible in all contexts — no hunting).
│   │
│   └── Current values shown as text below charts:
│       NF: −112.5 dBm | TX: 2.1% | RX: 8.3% | Err: 0.87% | Batt: 3.72V
│       24h: avg −114.2 | max −95.0 | 3 reboots
│
└── Fleet Comparison (M4)
    └── Small multiples of noise floor, one per observer, identical Y-scale
    └── NOT an overlay chart — overlays become unreadable past 5 lines
    └── Use 1h resolution for 7d views
```

### Why small multiples, not expandable accordion

An accordion (expand/collapse per observer) forces the operator to click through each observer sequentially. At 3 AM with 35 observers, that's unacceptable. The small multiples grid shows ALL observers simultaneously — the eye does the comparison, not the mouse. Anomalies pop out visually because they break the pattern of the grid. This is Tufte's core insight: **small multiples leverage the viewer's ability to detect pattern breaks across a consistent visual template.**

### Why no color bands on charts

Color bands (green/yellow/red zones) are decorative — they add ink that doesn't encode data. They also pre-judge what's "good" and "bad," which varies by deployment environment. Instead, use **thin reference lines with direct text labels** at the warning and critical thresholds. The reference lines take up negligible ink, the labels are informational, and the operator's eye naturally compares the data line against them.

### Why not stacked area for airtime

Stacked area charts are a common source of graphical dishonesty. The bottom series (TX) reads correctly against the X-axis, but the top series (RX) reads against the TX boundary — making it impossible to accurately judge RX values without mental subtraction. Two separate lines, directly labeled, are always more honest and more readable.

### Color usage

Color encodes data category, never decoration:
- **Noise floor line:** single muted color (the line IS the data — it doesn't need to be loud)
- **TX / RX lines:** two distinct colors, directly labeled at endpoints (no legend needed)
- **Error rate:** a third distinct color
- **Reboot markers:** gray hairlines (de-emphasized — context, not data)
- **Status text in grid cells:** text color only (not background fill) — red text for critical, amber for warning, default for normal
- No background color fills on cards. No colored borders. No badge backgrounds. Color on text only where it carries meaning.

### Labels and annotations

- **Reference lines** at threshold values, labeled directly ("−100 dBm warning")
- **Reboot events** as vertical hairlines across all charts, labeled "reboot" at the top
- **Cold-start samples** marked with open circles and a subtle "?" annotation
- **Current values** as inline text on the sparkline cells and below detail charts
- **No separate legends.** Lines are labeled at their endpoints or directly on the chart.
- **Hover** shows exact timestamp + value — this is the only interactive element, and it reveals precision, not hidden data

### Data density

- The small multiples grid fits 35 observers in ~700px vertical space (one screen on desktop)
- Each cell is information-dense: name + current value + sparkline + rates + reboot count — all visible without clicking
- Detail charts are stacked vertically sharing the X-axis, eliminating redundant time labels
- No wasted whitespace between chart panels — they are a single visual unit

### Information hierarchy (3 AM glance test)

1. **Grid scan (2 seconds):** Are all sparklines flat and similar? Yes → everything's fine. One cell has a spike or red text → that's the problem.
2. **Cell read (3 seconds):** Which observer, what's the current NF, what's the error rate? All visible without clicking.
3. **Detail dive (10 seconds):** Click the cell, see time-series context, see if it correlates with reboots, check battery, check airtime.

An operator never needs to click anything to know if the fleet is healthy. Clicking only provides temporal detail for diagnosis.

### Mobile considerations

- Grid collapses to 1 column on phone (each cell is full-width, still showing sparkline + values)
- Detail charts fill the viewport width, Y-axis labels move above the chart to save horizontal space
- Touch targets: the entire grid cell is tappable (not a small icon)
- Time range selector uses segmented control (large touch targets) for presets, not a dropdown
- Custom range picker: two datetime inputs with calendar popup, positioned below the presets
- Selected range (preset or custom) persists in URL hash: `&range=24h` or `&from=2026-04-04T14:00:00Z&to=2026-04-04T16:00:00Z`

### Chart rendering

**Use SVG, not Canvas.** The existing analytics.js uses SVG for all charts (sparklines, bar charts, histograms). Canvas is only used for the force-directed neighbor graph. Follow the existing SVG patterns — reuse `sparkSvg()` for fleet overview sparklines.

2,016 SVG polyline points per chart is fine. For the fleet comparison view (M4), use hourly downsampling (168 points per observer) to avoid layout jank on mobile.

### Deep linking

```
#/analytics?tab=rf-health
#/analytics?tab=rf-health&observer=1F445B...&range=24h
```

## Pattern Detection (M3+)

**Pattern detection is deferred until after operators have used raw charts (M1–M2) and provided feedback on what patterns actually matter.** Do not implement automated diagnosis until real-world usage informs the rules.

### Planned automated diagnosis

The server computes a `status` field per observer based on the last N samples:

| Pattern | Status | Indicator |
|---|---|---|
| NF stable, RX/TX normal, low error rate | `normal` | (no indicator — absence of alarm is the signal) |
| NF spike + RX drop (broadband interference) | `jammer_suspected` | Red text: "Jammer?" |
| NF normal, RX near zero, fleet active (≥5 observers) | `deaf` | Red text: "Deaf receiver" |
| High `recv_errors` rate + stable NF | `digital_interference` | Amber text: "CRC errors high" |
| TX approaching duty cycle warning | `tx_overload` | Amber text: "TX overload" |
| No samples in >15 min | `offline` | Gray text: "Offline" |
| NF gradually increasing over hours | `interference_trend` | Amber text: "Rising interference" |
| Battery voltage below threshold | `low_battery` | Amber text: "Low battery" |

**Jammer detection logic:** A jammer raises the noise floor AND causes RX to drop (the receiver can't hear legitimate signals over the interference). NF spike + RX spike would indicate a legitimate busy channel, not a jammer. The key signal is: NF goes up, RX goes down.

**Digital interference detection (new):** High `recv_errors` with a stable noise floor indicates in-band digital interference (another protocol sharing the frequency, or a malfunctioning node transmitting garbage). This is distinct from broadband jamming, which raises the noise floor. `recv_errors` is the strongest single signal for this.

**Deaf detection:** Requires a minimum fleet size of ≥5 active observers to establish a meaningful fleet median. With fewer observers, skip deaf detection — the sample size is too small for comparison.

### Status priority

When multiple status conditions apply simultaneously, use this priority order (highest first):
1. `offline` — no data trumps everything
2. `jammer_suspected` — active threat
3. `deaf` — hardware failure
4. `digital_interference` — channel quality issue
5. `tx_overload` — regulatory concern
6. `low_battery` — power issue causing RF symptoms
7. `interference_trend` — gradual degradation
8. `normal` — default

### Baseline computation

- **Baseline noise floor:** rolling median of last 24h, **excluding first-post-reboot samples** (cold start unreliable). Computed once on new sample arrival, cached — not recomputed per request.
- **Spike detection:** current sample exceeds an absolute threshold (configurable) AND exceeds baseline + spike delta. Both conditions must be met — a delta-only threshold could false-positive in environments where the absolute NF is already benign (e.g., -115 dBm + 15 dBm = -100 dBm, which is fine).
- **"Others active" check for deaf detection:** compare this observer's RX packet count against the fleet median. If this observer is <10% of fleet median AND fleet has ≥5 active observers, flag as potentially deaf.
- **Error rate baseline:** rolling average of `recv_error_rate` over 24h. Spike above 2× baseline triggers `digital_interference` status.

### Alert thresholds (configurable)

```json
{
  "rfHealth": {
    "noiseFloorWarning": -100,
    "noiseFloorCritical": -85,
    "spikeThresholdDb": 15,
    "txDutyCycleWarning": 8,
    "deafThresholdPct": 10,
    "deafMinFleetSize": 5,
    "offlineTimeoutSec": 900,
    "sampleIntervalSec": 300,
    "retentionDays": 30,
    "errorRateWarning": 5,
    "lowBatteryMv": 3300
  }
}
```

Note: No hardcoded duty cycle limit line on charts. Duty cycle regulations vary by jurisdiction (e.g., 1% in EU 868MHz, 10% in some US ISM bands). The warning threshold is configurable but no "regulatory limit" line is drawn on charts.

## Implementation Milestones

### M0: Prerequisite — Verify stats message frequency ✅ PASSED
- **Confirmed 2026-04-05:** Live MQTT capture on staging shows status messages arriving every ~5 minutes per observer
- **Fields confirmed present:** `noise_floor`, `tx_air_secs`, `rx_air_secs`, `recv_errors`, `battery_mv`, `uptime_secs`
- **Fields NOT yet parsed by ingestor:** `tx_air_secs`, `rx_air_secs`, `recv_errors` (noise_floor and battery_mv already parsed)
- **Ingestor timestamps:** Use ingestor wall clock, not observer timestamps (confirmed in design)
- **Verified:** `triggerNoiseFloorCalibrate()` fires every 2 seconds (`NOISE_FLOOR_CALIB_INTERVAL = 2000ms` in `Dispatcher.cpp`). Continuous calibration with 64 RSSI samples per cycle. Noise floor data is always fresh.
- **Gate: PASSED.** Proceed to M1.

### M1: Store metrics + small multiples grid (MVP)
- Create `observer_metrics` table with all columns (migration)
- Ingestor: parse all available fields from stats, `INSERT OR REPLACE` with rounded timestamps
- Handle missing fields gracefully (store NULLs)
- Detect counter resets and record reboot events
- Add `/api/observers/{id}/metrics` endpoint (all available fields)
- Add `/api/observers/metrics/summary` endpoint (cached incrementally)
- Add "RF Health" tab to Analytics
- **Small multiples grid** with sparklines and inline values for all observers
- Per-observer detail view: noise floor line chart with reference lines (not color bands), reboot markers as vertical hairlines, cold-start sample annotation
- Time range selector (1h/3h/6h/12h/24h/3d/7d/30d + custom range picker)
- Deep linking
- Retention pruning
- Tests: sampling, insertion idempotency, retention, API responses, gap handling, reboot detection

### M2: Airtime + channel quality charts
- Server-side delta computation for all cumulative counters with reboot handling and gap detection
- Add `resolution` query param for downsampling (1h, 1d)
- Airtime charts: two separate lines (TX/RX), directly labeled — not stacked area
- Channel quality chart: recv_error_rate line + packets_recv step-line
- Battery voltage chart (shown only when data exists)
- All charts time-aligned, sharing X-axis, reboot markers spanning all charts
- Tests: delta computation, reboot handling, counter reset, gap insertion, downsampling, error rate calculation

#### M2 feedback improvements (post-M2)
- **Auto-scale airtime Y-axis**: clamp to min/max of actual data values (20% headroom, min 1%) instead of fixed 0-100%, matching noise floor chart behavior. Increases data-ink ratio for low-activity nodes.
- **Hover tooltips on all chart data points**: invisible SVG circles with `<title>` elements on every data point across all 4 charts (noise floor, airtime, error rate, battery). Shows exact value + UTC timestamp on hover. Detail-on-demand without cluttering the chart.

### M3: Pattern detection
- Implement after operators have used raw charts (M1–M2) and provided feedback
- Jammer detection (NF spike + RX drop)
- Digital interference detection (high recv_errors + stable NF)
- Deaf receiver detection (with ≥5 fleet minimum)
- Low battery detection
- Interference trend detection
- Status text indicators with priority ordering (no emoji badges — text only)
- Baseline computation (rolling median excluding cold-start samples, cached)
- Configurable alert thresholds
- Tests: each pattern, edge cases, status priority

### M4: Fleet comparison + advanced views
- Fleet comparison as **small multiples** (one noise floor chart per observer, identical Y-scale) — not overlay
- Sort/filter fleet by status, noise floor, error rate
- Optional: per-observer historical baseline trend
- Use 1h resolution for 7d views

### M5: Metrics export — Prometheus / Grafana / external systems
- **Prometheus endpoint:** `GET /metrics` exposing observer radio metrics in Prometheus exposition format
  - Gauges per observer: `corescope_observer_noise_floor_dbm{observer="...",name="..."}`, `corescope_observer_tx_air_secs_total`, `corescope_observer_rx_air_secs_total`, `corescope_observer_recv_errors_total`, `corescope_observer_battery_mv`, `corescope_observer_uptime_secs`
  - Fleet-level: `corescope_observers_total`, `corescope_observers_online`
  - Packet counters: `corescope_packets_total`, `corescope_observations_total`
  - Standard `process_*` and `go_*` runtime metrics via `promhttp` handler
- **Configurable:** Enable/disable via `config.json` (`metrics.prometheusEnabled: true`, `metrics.prometheusPath: "/metrics"`)
- **Auth:** Optional bearer token or basic auth on the metrics endpoint (prevents public scraping)
- **Labels:** Each observer metric labeled with `observer` (pubkey), `name` (friendly name), `region`
- **Why Prometheus format:** Industry standard, compatible with Grafana, Datadog, Victoria Metrics, Mimir, and any OpenMetrics consumer. Operators who already run monitoring stacks can integrate CoreScope without any custom work.
- **Implementation:** Use Go `prometheus/client_golang` library. Register collectors that read from the in-memory `PacketStore` and `observer_metrics` table. No additional polling — just expose current state on each scrape.
- **Grafana dashboard template:** Ship a JSON dashboard template (`docs/grafana-dashboard.json`) that operators can import for instant RF health visualization in Grafana. Pre-configured panels matching the built-in RF Health tab.
- **OpenTelemetry (future):** If demand exists, add OTLP export alongside Prometheus. Not in M5 scope.

## Design Decisions

1. **Per-observer, not per-device.** Even if two observers share hardware, their RF environments may differ (different antennas, channels). observer_id is already the natural key.
2. **Poll-on-tab-switch, not WebSocket push.** Data changes every 5 minutes. Users check this tab when investigating issues, not for live monitoring. WebSocket push adds complexity for no UX benefit.
3. **SVG charts.** Matches existing analytics.js patterns. Canvas only if fleet comparison proves too slow with SVG.
4. **Server-side deltas.** Keeps firmware details out of the frontend. Single point for reboot/gap handling logic.
5. **Incremental fleet summary cache.** 7 inserts/min is trivially cheap to process. No need to query SQLite on every summary request.
6. **No standalone timestamp index.** The composite PK handles all query patterns. A standalone index wastes write amplification.
7. **Ingestor wall clock for timestamps.** Observer clocks are unreliable. Consistent time source prevents alignment issues.
8. **Small multiples over accordion/cards.** Enables instant visual fleet comparison without clicking. Anomalies break the visual pattern of the grid. (Tufte: "Small multiples are the best design solution for a wide range of problems in data presentation.")
9. **Reference lines, not color bands.** Color bands add non-data ink and pre-judge thresholds. Reference lines are minimal and informational.
10. **Two lines, not stacked area for airtime.** Stacked areas make the upper series unreadable. Two lines with direct labels are always more honest.
11. **Text status indicators, not emoji badges.** Emoji badges are decorative chrome. Plain text with semantic color (red/amber/default) is higher data-ink ratio and more accessible.
12. **Reboot markers as cross-chart annotations.** Reboots affect all metrics simultaneously. Showing them as vertical hairlines across all charts prevents the operator from having to correlate events across separate views.
13. **Separate packets_sent/packets_recv.** The ratio reveals asymmetric link problems invisible in a combined count.
14. **recv_errors as a first-class metric.** CRC failures are the strongest channel quality signal — more diagnostic than noise floor alone for in-band interference.
15. **Exclude cold-start samples from baseline.** First-post-reboot noise floor readings may reflect calibration artifacts, not the RF environment. Including them would bias the baseline.

## Open Questions

1. **Multiple observers on same channel:** If two observers share a channel, their noise floors should correlate. Could be useful for validation but doesn't change the data model.
2. **EMA vs median for baseline:** Exponential moving average is cheaper (no sort) and smoother than median. Consider for M3 implementation — but median is more robust against outliers. Decision deferred to M3.
3. **`triggerNoiseFloorCalibrate()` frequency:** Must be verified in M0. If it fires on every stats cycle, noise floor readings may be artificially smoothed. If only on boot, cold-start caveat applies. This affects how much weight to give noise floor vs. recv_errors for interference detection.
4. **Battery voltage thresholds:** 3.3V is a reasonable default for LiPo cells, but varies by chemistry and regulator. May need per-observer configuration.
