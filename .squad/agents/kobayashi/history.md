# Kobayashi — History

## Project Context

CoreScope is a real-time LoRa mesh packet analyzer. Node.js + Express + SQLite backend, vanilla JS SPA frontend with Leaflet maps, WebSocket live feed, MQTT ingestion. Production at v2.6.0, ~18K lines, 85%+ backend test coverage.

User: User

## Learnings

- Session started 2026-03-26. Team formed: Kobayashi (Lead), Hicks (Backend), Newt (Frontend), Bishop (Tester).
- **E2E Playwright performance audit (2026-03-26):** 16 tests, single browser/context/page (good). Key bottlenecks: (1) `waitUntil: 'networkidle'` used ~20 times — catastrophic for SPA with WebSocket + map tiles, (2) ~17s of hardcoded `waitForTimeout` sleeps, (3) redundant `page.goto()` to same routes across tests, (4) CI installs Playwright browser on every run with no caching, (5) coverage collection launches a second full browser session, (6) `sleep 5` server startup instead of health-check polling. Estimated 40-50% total runtime reduction achievable.
- **Issue triage session (2026-03-27):** Triaged 4 open issues, assigned to team:
  - **#131** (Feature: Auto-update nodes tab) → Newt (⚛️). Requires WebSocket real-time updates in nodes.js, similar to existing packets feed.
  - **#130** (Bug: Disappearing nodes on live map) → Newt (⚛️). High severity, multiple Cascadia Mesh community reports. Likely status calculation or map filter bug. Nodes visible in static list but vanishing from live map.
  - **#129** (Feature: Packet comparison between observers) → Newt (⚛️). Feature request from letsmesh analyzer. Side-by-side packet filtering for two repeaters to diagnose repeater issues.
  - **#123** (Feature: Show channel hash on decrypt failure) → Hicks (🔧). Core contributor (lincomatic) request. Decoder needs to track why decrypt failed (no key vs. corruption) and expose channel hash + reason in API response.
- **Massive session — 2026-03-27 (full day):**
  - **#133 root cause (phantom nodes):** `autoLearnHopNodes()` creates stub nodes for unresolved hop prefixes (2-8 hex chars). Cascadia showed 7,308 nodes (6,638 repeaters) when real size ~200-400. With `hash_size=1`, collision rate high → infinite phantom generation.
  - **DB merge decision:** Staging DB (185MB, 50K transmissions, 1.2M observations) is superset. Use as merge base. Transmissions dedup by hash (unique), observations all preserved (unique by observer), nodes/observers latest-wins + sum counts. 6-phase execution plan: pre-flight, backup, merge, deploy, validate, cleanup.
  - **Coordination:** Assigned Hicks phantom cleanup (backend), Newt live page pruning (frontend), Hudson merge execution (DevOps).
  - **Outcome:** All 4 triaged issues fixed (#131, #130, #129, #123), #133 (phantom nodes) fully resolved, #126 (ambiguous hop prefixes) fixed as bonus, database merged successfully (0 data loss, 2 min downtime, 51,723 tx + 1.237M obs), Go rewrite (MQTT ingestor + web server) completed and ready for staging.
  - **Team expanded:** Hudson joined for DevOps work, Ripley joined as Support Engineer.
- **Go staging bug triage (2026-03-28):** Filed 8 issues for Go staging bugs missed during API parity work. All found by actually loading the analytics page in a browser — none caught by endpoint-level parity checks.
  - **#142** (Channels tab: wrong count, all decrypted, undefined fields) → Hicks
  - **#136** (Hash stats tab: empty) → Hicks
  - **#138** (Hash issues: no inconsistencies/collision risks shown) → Hicks
  - **#135** (Topology tab: broken) → Hicks
  - **#134** (Route patterns: broken) → Hicks
  - **#140** (bulk-health API: 12s response time) → Hicks
  - **#137** (Distance tab: broken) → Hicks
  - **#139** (Commit link: bad contrast) → Newt
  - **Post-mortem:** Parity was verified by comparing individual endpoint response shapes in isolation. Nobody loaded the analytics page in a browser and looked at it. The agents tested API responses without browser validation of the full UI — exactly the failure mode AGENTS.md rule #2 exists to prevent.
