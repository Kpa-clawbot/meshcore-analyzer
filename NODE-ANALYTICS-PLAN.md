# Node Analytics Page — Implementation Plan

## Overview
A dedicated per-node analytics page (`#/nodes/:pubkey/analytics`) showing charts, breakdowns, and computed stats. Linked from node sidebar and full-screen detail views.

## Route & Navigation
- **Hash route:** `#/nodes/:pubkey/analytics`
- **Entry points:**
  - Sidebar detail: "📊 Analytics" button next to "📋 Copy URL"
  - Full-screen detail: same button placement
  - Direct URL (shareable)
- **Back navigation:** "← Back to node" link returns to `#/nodes/:pubkey`

## API Endpoint

### `GET /api/nodes/:pubkey/analytics?days=7`

Returns all data needed for the page in a single request. Server computes aggregations in SQLite for efficiency.

```json
{
  "node": { "public_key": "...", "name": "...", "role": "..." },
  "timeRange": { "from": "ISO", "to": "ISO", "days": 7 },
  "activityTimeline": [
    { "bucket": "2026-03-19T10:00:00Z", "count": 5 }
  ],
  "snrTrend": [
    { "timestamp": "ISO", "snr": 11.5, "rssi": -44, "observer_id": "...", "observer_name": "..." }
  ],
  "packetTypeBreakdown": [
    { "payload_type": 4, "label": "Advert", "count": 120 },
    { "payload_type": 5, "label": "Channel Msg", "count": 45 }
  ],
  "observerCoverage": [
    { "observer_id": "...", "observer_name": "...", "packetCount": 200, "avgSnr": 8.5, "avgRssi": -60, "firstSeen": "ISO", "lastSeen": "ISO" }
  ],
  "hopDistribution": [
    { "hops": 1, "count": 150 },
    { "hops": 2, "count": 30 }
  ],
  "peerInteractions": [
    { "peer_key": "...", "peer_name": "...", "messageCount": 15, "lastContact": "ISO" }
  ],
  "computedStats": {
    "availabilityPct": 92.5,
    "longestSilenceMs": 14400000,
    "longestSilenceStart": "ISO",
    "signalGrade": "B+",
    "snrMean": 8.2,
    "snrStdDev": 3.1,
    "relayPct": 22.5,
    "totalPackets": 450,
    "uniqueObservers": 3,
    "uniquePeers": 8,
    "avgPacketsPerDay": 64.3
  },
  "uptimeHeatmap": [
    { "dayOfWeek": 0, "hour": 14, "count": 12 }
  ]
}
```

### Server Implementation (`server.js`)

Add route handler at `/api/nodes/:pubkey/analytics`. All queries use the same LIKE-based matching as existing `getNodeHealth()`. Key queries:

1. **activityTimeline** — `SELECT strftime('%Y-%m-%dT%H:00:00Z', timestamp) as bucket, COUNT(*) as count FROM packets WHERE ... AND timestamp > ? GROUP BY bucket ORDER BY bucket`
2. **snrTrend** — `SELECT timestamp, snr, rssi, observer_id, observer_name FROM packets WHERE ... AND snr IS NOT NULL ORDER BY timestamp` (raw points, chart.js handles rendering)
3. **packetTypeBreakdown** — `SELECT payload_type, COUNT(*) as count FROM packets WHERE ... GROUP BY payload_type`
4. **observerCoverage** — `SELECT observer_id, observer_name, COUNT(*), AVG(snr), AVG(rssi), MIN(timestamp), MAX(timestamp) FROM packets WHERE ... GROUP BY observer_id ORDER BY COUNT(*) DESC`
5. **hopDistribution** — Parse `path_json` in JS, count hop lengths
6. **peerInteractions** — Parse `decoded_json`, extract sender/recipient pubkeys and names, aggregate
7. **uptimeHeatmap** — `SELECT strftime('%w', timestamp) as dow, strftime('%H', timestamp) as hour, COUNT(*) FROM packets WHERE ... GROUP BY dow, hour`
8. **computedStats** — Derived from above data:
   - `availabilityPct`: count distinct hours with packets / total hours in range × 100
   - `longestSilenceMs`: iterate timestamps, find max gap
   - `signalGrade`: A (snr>15, stddev<2), B (snr>8), C (snr>3), D (snr<=3)
   - `relayPct`: packets with hop count > 1 / total with path data × 100

Add a helper function `getNodeAnalytics(pubkey, days)` in `db.js` to keep it organized.

## Frontend

### New File: `public/node-analytics.js`

IIFE pattern matching existing pages. Registers with the router for `#/nodes/:pubkey/analytics`.

### Layout

```
┌─────────────────────────────────────────────────┐
│ ← Back to SomeNodeName                          │
│                                                 │
│ ┌─────────────┐ ┌─────────────┐ ┌────────────┐ │
│ │ Availability│ │ Signal Grade│ │ Packets/Day│ │
│ │    92.5%    │ │     B+      │ │    64.3    │ │
│ └─────────────┘ └─────────────┘ └────────────┘ │
│ ┌─────────────┐ ┌─────────────┐ ┌────────────┐ │
│ │ Observers   │ │ Relay %     │ │ Longest    │ │
│ │      3      │ │   22.5%     │ │ Silence 4h │ │
│ └─────────────┘ └─────────────┘ └────────────┘ │
│                                                 │
│ ┌─────────────────────────────────────────────┐ │
│ │ Activity Timeline (bar chart, hourly)       │ │
│ └─────────────────────────────────────────────┘ │
│                                                 │
│ ┌──────────────────────┐ ┌────────────────────┐ │
│ │ SNR Trend (line)     │ │ Packet Types (pie) │ │
│ └──────────────────────┘ └────────────────────┘ │
│                                                 │
│ ┌──────────────────────┐ ┌────────────────────┐ │
│ │ Observer Coverage    │ │ Hop Distribution   │ │
│ │ (horizontal bar)     │ │ (bar chart)        │ │
│ └──────────────────────┘ └────────────────────┘ │
│                                                 │
│ ┌─────────────────────────────────────────────┐ │
│ │ Uptime Heatmap (7×24 grid, GitHub-style)    │ │
│ └─────────────────────────────────────────────┘ │
│                                                 │
│ ┌─────────────────────────────────────────────┐ │
│ │ Peer Interactions (ranked list)             │ │
│ └─────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────┘
```

### Time Range Selector
- Buttons: 24h | 7d | 30d | All
- Default: 7d
- Reloads data via API when changed

### Chart Library
- **Chart.js v4** from CDN (unpkg): `https://unpkg.com/chart.js@4/dist/chart.umd.min.js`
- Add `<script>` tag in `index.html` (with cache buster)
- Chart.js is ~70KB gzipped, handles all chart types needed

### Chart Specifications

1. **Activity Timeline** (bar chart, full width)
   - X: time buckets (hourly for ≤3d, daily for >3d)
   - Y: packet count
   - Color: role color with 50% opacity
   - Tooltip: exact count + timestamp

2. **SNR Trend** (line chart, half width)
   - One line per observer (different colors)
   - X: timestamp, Y: SNR (dB)
   - Include a horizontal reference line at 0 dB
   - Legend shows observer names

3. **Packet Type Breakdown** (doughnut chart, half width)
   - Segments: Advert, Channel Msg, DM, ACK, Request, Response, etc.
   - Colors: match existing PAYLOAD badge colors
   - Center text: total count

4. **Observer Coverage** (horizontal bar chart, half width)
   - Bars: one per observer, length = packet count
   - Color intensity mapped to avg SNR (brighter = better signal)
   - Labels: observer name + avg SNR

5. **Hop Distribution** (bar chart, half width)
   - X: hop count (1, 2, 3, 4+)
   - Y: packet count
   - Simple, clean

6. **Uptime Heatmap** (custom canvas/div grid, full width)
   - 7 rows (Sun–Sat) × 24 columns (hours)
   - Cell color intensity = packet count for that slot
   - Tooltip: "Monday 14:00 — 12 packets"
   - Use CSS grid with inline background colors (no chart.js needed)

7. **Peer Interactions** (table/list, full width)
   - Ranked by message count
   - Columns: peer name, messages, last contact
   - Peer name links to their node detail page

### Stat Cards
- Use CSS grid, 3 columns on desktop, 2 on tablet, 1 on mobile
- Each card: label (small, muted), value (large, bold), optional trend arrow
- Signal grade uses color coding: A=green, B=blue, C=yellow, D=red

### CSS (add to `style.css`)
```css
.analytics-stats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; margin-bottom: 24px; }
.analytics-stat-card { background: var(--card-bg); border: 1px solid var(--border); border-radius: 8px; padding: 16px; text-align: center; }
.analytics-stat-label { font-size: 11px; text-transform: uppercase; letter-spacing: .5px; color: var(--text-muted); margin-bottom: 4px; }
.analytics-stat-value { font-size: 28px; font-weight: 700; }
.analytics-charts { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-bottom: 24px; }
.analytics-chart-card { background: var(--card-bg); border: 1px solid var(--border); border-radius: 8px; padding: 16px; }
.analytics-chart-card.full { grid-column: 1 / -1; }
.analytics-chart-card h4 { font-size: 12px; text-transform: uppercase; letter-spacing: .5px; color: var(--text-muted); margin-bottom: 12px; }
.analytics-heatmap { display: grid; grid-template-columns: 40px repeat(24, 1fr); gap: 2px; }
.analytics-heatmap-cell { aspect-ratio: 1; border-radius: 2px; }
.analytics-heatmap-label { font-size: 10px; color: var(--text-muted); display: flex; align-items: center; }
.analytics-time-range { display: flex; gap: 8px; margin-bottom: 16px; }
.analytics-time-range button { padding: 4px 12px; border-radius: 4px; border: 1px solid var(--border); background: var(--card-bg); color: var(--text); cursor: pointer; font-size: 12px; }
.analytics-time-range button.active { background: var(--accent); color: white; border-color: var(--accent); }
@media (max-width: 768px) { .analytics-stats { grid-template-columns: repeat(2, 1fr); } .analytics-charts { grid-template-columns: 1fr; } }
@media (max-width: 480px) { .analytics-stats { grid-template-columns: 1fr; } }
```

### Dark Mode
All colors use CSS variables. Chart.js text/grid colors should reference `--text-muted` and `--border`. Set via:
```js
Chart.defaults.color = getComputedStyle(document.documentElement).getPropertyValue('--text-muted').trim();
Chart.defaults.borderColor = getComputedStyle(document.documentElement).getPropertyValue('--border').trim();
```

## Files to Modify

1. **`db.js`** — Add `getNodeAnalytics(pubkey, days)` function
2. **`server.js`** — Add `GET /api/nodes/:pubkey/analytics` route
3. **`public/node-analytics.js`** — New file, full page implementation
4. **`public/style.css`** — Add analytics CSS classes
5. **`public/index.html`** — Add Chart.js CDN script + `node-analytics.js` script tag (with cache buster)
6. **`public/app.js`** — Add route for `#/nodes/:pubkey/analytics` in the router
7. **`public/nodes.js`** — Add "📊 Analytics" button to sidebar and full-screen detail views

## Constraints — DO NOT TOUCH

These files/behaviors have been manually tuned. Do not modify unless explicitly part of the plan:

1. **`public/map.js`** — Map markers, disambiguation logic, route drawing. OFF LIMITS.
2. **`public/packets.js`** — Panel resize, VCR replay logic. OFF LIMITS.
3. **`public/app.js` `makeColumnsResizable()`** (line ~463) — Column resize steals proportionally from all right columns with 50px minimum. Do not change.
4. **Existing node detail rendering in `nodes.js`** — Only ADD the analytics button. Do not reorganize, rename, or restructure existing sections.
5. **Cache busters** — When modifying `index.html`, bump cache busters on ALL changed files using `?v=TIMESTAMP`.
6. **`escapeHtml` and `timeAgo`** — Globals defined in `app.js`. Do not redefine them anywhere.
7. **Router in `app.js`** — Follow existing pattern exactly when adding the analytics route.

## Implementation Order

1. Add CSS to `style.css`
2. Add Chart.js to `index.html`
3. Add `getNodeAnalytics()` to `db.js`
4. Add API route to `server.js`
5. Create `node-analytics.js`
6. Register route in `app.js`
7. Add analytics button to `nodes.js` (sidebar + full-screen)
8. Add `node-analytics.js` script tag to `index.html` with cache buster
9. Bump all modified file cache busters
10. Test: `node -c` on all JS files, verify no syntax errors

## Testing

After implementation:
- Navigate to any node → click Analytics → page loads with charts
- Switch time ranges → data reloads
- Dark mode → charts readable
- Mobile → responsive layout
- Direct URL → page loads correctly
- Back button → returns to node detail
