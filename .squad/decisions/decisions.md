# Squad Decisions Log

---

## Decision: User Directives

### 2026-03-27T04:27 — Docker Compose v2 Plugin Check
**By:** User (via Copilot)  
**Decision:** CI pipeline should check if `docker compose` (v2 plugin) is installed on the self-hosted runner and install it if needed, as part of the deploy job itself.  
**Rationale:** Self-healing CI is preferred over manual VM setup; the VM may not have docker compose v2 installed.

### 2026-03-27T04:39 — Staging DB: Use Old Problematic DB
**By:** User (via Copilot)  
**Decision:** Staging environment's primary purpose is debugging the problematic DB that caused 100% CPU on prod. Use the old DB (`~/meshcore-data-old/` on the VM) for staging. Prod keeps its current (new) DB. Never put the problematic DB on prod.  
**Rationale:** This is the reason the staging environment was built.

### 2026-03-27T06:09 — Plan Go Rewrite (MQTT Separation)
**By:** User (via Copilot)  
**Decision:** Start planning a Go rewrite. First step: separate MQTT ingestion (writes to DB) from the web server (reads from DB + serves API/frontend). Two separate services.  
**Rationale:** Node.js single-thread + V8 heap limitations cause fragility at scale (185MB DB → 2.7GB heap → OOM). Go eliminates heap cap problem and enables real concurrency.

### 2026-03-27T06:31 — NO PII in Git
**By:** User (via Copilot)  
**Decision:** NEVER write real names, usernames, email addresses, or any PII to files committed to git. Use "User" for attribution and "deploy" for SSH/server references. This is a PUBLIC repo.  
**Rationale:** PII was leaked to the public repo and required a full git history rewrite to remove.

### 2026-03-27T02:19 — Production/Infrastructure Touches: Hudson Only
**By:** User (via Copilot)  
**Decision:** Production/infrastructure touches (SSH, DB ops, server restarts, Azure operations) should only be done by Hudson (DevOps). No other agents should touch prod directly.  
**Rationale:** Separation of concerns — dev agents write code, DevOps deploys and manages prod.

### 2026-03-27T03:36 — Staging Environment Architecture
**By:** User (via Copilot)  
**Decision:**
1. No Docker named volumes — always bind mount from `~/meshcore-data` (host location, easy to access)
2. Staging container runs on plaintext port (e.g., port 81, no HTTPS)
3. Use Docker Compose to orchestrate prod + staging containers on the same VM
4. `manage.sh` supports launching prod only OR prod+staging with clear messaging
5. Ports must be configurable via `manage.sh` or environment, with sane defaults

### 2026-03-27T03:43 — Staging Refinements: Shared Data
**By:** User (via Copilot)  
**Decision:**
1. Staging copies prod DB on launch (snapshot into staging data dir when started)
2. Staging connects to SAME MQTT broker as prod (not its own Mosquitto)

**Rationale:** Staging needs real data (prod-like conditions) to be useful for testing.

### 2026-03-27T17:13 — Scribe Auto-Run After Agent Batches
**By:** User (via Copilot)  
**Decision:** Scribe must run after EVERY batch of agent work automatically. No manual triggers. No reminders needed. This is a process guarantee, not a suggestion.  
**Rationale:** Coordinator has been forgetting to spawn Scribe after agent batches complete. This is a process failure. Scribe auto-spawn ends the forgetfulness.

---

## Decision: Technical Fixes

### Issue #126 — Skip Ambiguous Hop Prefixes
**By:** Hicks (Backend Dev)  
**Date:** 2026-03-27  
**Status:** Implemented

When resolving hop prefixes to full node pubkeys, require a **unique match**. If prefix matches 2+ nodes in DB, skip it and cache in `ambiguousHopPrefixes` (negative cache). Prevents hash prefix collisions (e.g., `1CC4` vs `1C82` sharing prefix `1C` under 1-byte hash_size) from attributing packets to wrong nodes.

**Impact:**
- Hopresixes that collide won't update `lastPathSeenMap` for any node (conservative, correct)
- `disambiguateHops()` still does geometric disambiguation for route visualization
- Performance: `LIMIT 2` query efficient; ambiguous results cached

---

### Issue #133 — Phantom Nodes & Active Window
**By:** Hicks (Backend Dev)  
**Date:** 2026-03-27  
**Status:** Implemented

**Part 1: Remove phantom node creation**
- `autoLearnHopNodes()` no longer calls `db.upsertNode()` for unresolved hops
- Added `db.removePhantomNodes()` — deletes nodes where `LENGTH(public_key) <= 16` (real keys are 64 hex chars)
- Called at startup to purge existing phantoms from prior behavior
- Hop-resolver still handles unresolved prefixes gracefully

**Part 2: totalNodes now 7-day active window**
- `/api/stats` `totalNodes` returns only nodes seen in last 7 days (was all-time)
- New field `totalNodesAllTime` for historical tracking
- Role counts (repeaters, rooms, companions, sensors) also filtered to 7-day window
- Frontend: no changes needed (same field name, smaller correct number)

**Impact:** Frontend `totalNodes` now reflects active mesh size. Go server should apply same 7-day filter when querying.

---

### Issue #123 — Channel Hash on Undecrypted Messages
**By:** Hicks  
**Status:** Implemented

Fixed test coverage for decrypted status tracking on channel messages.

---

### Issue #130 — Live Map: Dim Stale Nodes, Don't Remove
**By:** Newt (Frontend)  
**Date:** 2026-03-27  
**Status:** Implemented

`pruneStaleNodes()` in `live.js` now distinguishes API-loaded nodes (`_fromAPI`) from WS-only dynamic nodes. API nodes dimmed (reduced opacity) when stale instead of removed. WS-only nodes still pruned to prevent memory leaks.

**Rationale:** Static map shows stale nodes with faded markers; live map was deleting them, causing user-reported disappearing nodes. Parity expected.

**Pattern:** Database-loaded nodes never removed from map during session. Future live map features should respect `_fromAPI` flag.

---

### Issue #131 — Nodes Tab Auto-Update via WebSocket
**By:** Newt (Frontend)  
**Date:** 2026-03-27  
**Status:** Implemented

WS-driven page updates must reset local caches: (1) set local cache to null, (2) call `invalidateApiCache()`, (3) re-fetch. New `loadNodes(refreshOnly)` pattern skips full DOM rebuild, only updates data rows. Preserves scroll, selection, listeners.

**Trap:** Two-layer caching (local variable + API cache) prevents re-fetches. All three reset steps required.

**Pattern:** Other pages doing WS-driven updates should follow same approach.

---

### Issue #129 — Observer Comparison Page
**By:** Newt (Frontend)  
**Date:** 2026-03-27  
**Status:** Implemented

Added `comparePacketSets(hashesA, hashesB)` as standalone pure function exposed on `window` for testability. Computes `{ onlyA, onlyB, both }` via Set operations (O(n)).

**Pattern:** Comparison logic decoupled from UI, reusable. Client-side diff avoids new server endpoint. 24-hour window keeps data size reasonable (~10K packets max).

---

### Issue #132 — Detail Pane Collapse
**By:** Newt (Frontend)  
**Date:** 2026-03-27  
**Status:** Implemented

Detail pane collapse uses CSS class on parent container. Add `detail-collapsed` class to `.split-layout`, which sets `.panel-right` to `display: none`. `.panel-left` with `flex: 1` fills 100% width naturally.

**Pattern:** CSS class toggling on parent cleaner than inline styles, easier to animate, keeps layout logic in CSS.

---

## Decision: Infrastructure & Deployment

### Database Merge — Prod + Staging
**By:** Kobayashi (Lead) / Hudson (DevOps)  
**Date:** 2026-03-27  
**Status:** ✅ Complete

Merged staging DB (185MB, 50K transmissions + 1.2M observations) into prod DB (21MB). Dedup strategy:
- **Transmissions:** `INSERT OR IGNORE` on `hash` (unique key)
- **Observations:** All unique by observer, all preserved
- **Nodes/Observers:** Latest `last_seen` wins, sum counts

**Results:**
- Merged DB: 51,723 transmissions, 1,237,186 observations
- Deployment: Docker Compose managed `meshcore-prod` with bind mounts
- Load time: 8,491ms, Memory: 860MiB RSS (no NODE_OPTIONS needed, RAM fix effective)
- Downtime: ~2 minutes
- Backups: Retained at `/home/deploy/backups/pre-merge-20260327-071425/` until 2026-04-03

---

### Unified Docker Volume Paths
**By:** Hudson (DevOps)  
**Date:** 2026-03-27  
**Status:** Applied

Reconciled `manage.sh` and `docker-compose.yml` Docker volume names:
- Caddy volume: `caddy-data` everywhere (prod); `caddy-data-staging` for staging
- Data directory: Bind mount via `PROD_DATA_DIR` env var, default `~/meshcore-data`
- Config/Caddyfile: Mounted from repo checkout for prod, staging data dir for staging
- Removed deprecated `version` key from docker-compose.yml

**Consequence:** `./manage.sh start` and `docker compose up prod` now produce identical mounts. Anyone with data in old `caddy-data-prod` volume will need Caddy to re-provision TLS certs automatically.

---

### Staging DB Setup & Production Data Locations
**By:** Hudson (DevOps)  
**Date:** 2026-03-27  
**Status:** Implemented

**Production Data Locations:**
- **Prod DB:** Docker volume `meshcore-data` → `/var/lib/docker/volumes/meshcore-data/_data/meshcore.db` (21MB, fresh)
- **Prod config:** `/home/deploy/meshcore-analyzer/config.json` (bind mount, read-only)
- **Caddyfile:** `/home/deploy/meshcore-analyzer/caddy-config/Caddyfile` (bind mount, read-only)
- **Old (broken) DB:** `~/meshcore-data-old/meshcore.db` (185MB, DO NOT DELETE)
- **Staging data:** `~/meshcore-staging-data/` (copy of broken DB + config)

**Rules:**
- DO NOT delete `~/meshcore-data-old/` — backup of problematic DB
- DO NOT modify staging DB before staging container ready
- Only Hudson touches prod infrastructure

---

## Decision: Go Rewrite — API & Storage

### Go MQTT Ingestor (cmd/ingestor/)
**By:** Hicks (Backend Dev)  
**Date:** 2026-03-27  
**Status:** Implemented, 25 tests passing

Standalone Go MQTT ingestor service. Separate process from Node.js web server that handles MQTT packet ingestion + writes to shared SQLite DB.

**Architecture:**
- Single binary, no CGO (uses `modernc.org/sqlite` pure Go)
- Reads same `config.json` (mqttSources array)
- Shares SQLite DB with Node.js (WAL mode for concurrent access)
- Format 1 (raw packet) MQTT only — companion bridge stays in Node.js
- No HTTP/WebSocket — web layer stays in Node.js

**Ported from decoder.js:**
- Packet header/path/payloads, advert with flags/lat/lon/name
- computeContentHash (SHA-256, path-independent)
- db.js v3 schema (transmissions, observations, nodes, observers)
- MQTT connection logic (multi-broker, reconnect, IATA filter)

**Not Ported:** Companion bridge format, channel key decryption, WebSocket broadcast, in-memory packet store.

---

### Go Web Server (cmd/server/)
**By:** Hicks (Backend Dev)  
**Date:** 2026-03-27  
**Status:** Implemented, 42 tests passing, `go vet` clean

Standalone Go web server replacing Node.js server's READ side (REST API + WebSocket). Two-component rewrite: ingestor (MQTT writes), server (REST/WS reads).

**Architecture Decisions:**
1. **Direct SQLite queries** — No in-memory packet store; all reads via `packets_v` view (v3 schema)
2. **Per-module go.mod** — Each `cmd/*` directory has own `go.mod`
3. **gorilla/mux for routing** — Handles 35+ parameterized routes cleanly
4. **SQLite polling for WebSocket** — Polls for new transmission IDs every 1s (decouples from MQTT)
5. **Analytics stubs** — Topology, distance, hash-sizes, subpath return valid structural responses (empty data). RF/channels implemented via SQL.
6. **Response shape compatibility** — All endpoints return JSON matching Node.js exactly (frontend works unchanged)

**Files:**
- `cmd/server/main.go` — Entry, HTTP, graceful shutdown
- `cmd/server/db.go` — SQLite read queries
- `cmd/server/routes.go` — 35+ REST API handlers
- `cmd/server/websocket.go` — Hub + SQLite poller
- `cmd/server/README.md` — Build/run docs

**Future Work:** Full analytics via SQL, TTL response cache, shared `internal/db/` package, TLS, region-aware filtering.

---

### Go API Parity: Transmission-Centric Queries
**By:** Hicks (Backend Dev)  
**Date:** 2026-03-27  
**Status:** Implemented, all 42+ tests pass

Go server rewrote packet list queries from VIEW-based (slow, wrong shape) to **transmission-centric** with correlated subqueries. Schema version detection (`isV3` flag) handles both v2 and v3 schemas.

**Performance Fix:** `/api/packets?groupByHash=true` — 8s → <100ms (query `transmissions` table 52K rows instead of `packets_v` 1.2M observations).

**Field Parity:**
- `totalNodes` now 7-day active window (was all-time)
- Added `totalNodesAllTime` field
- Role counts use 7-day filter (matches Node.js line 880-886)
- `/api/nodes` counts use no time filter; `/api/stats` uses 7-day (separate methods avoid conflation)
- `/api/packets/:id` now parses `path_json`, returns actual hop array
- `/api/observers` — packetsLastHour, lat, lon, nodeRole computed from SQL
- `/api/nodes/bulk-health` — Per-node stats computed (was returning zeros)
- `/api/packets` — Multi-node filter support (`nodes` query param, comma-separated pubkeys)

---

### Go In-Memory Packet Store (cmd/server/store.go)
**By:** Hicks (Backend Dev)  
**Date:** 2026-03-26  
**Status:** Implemented

Port of `packet-store.js` with streaming load, 5 indexes, lean observation structs (only observation-specific fields). `QueryPackets` handles type, route, observer, hash, since, until, region, node. `IngestNewFromDB()` streams new transmissions from DB into memory.

**Trade-offs:**
- Memory: ~450 bytes/tx + ~100 bytes/obs (52K tx + 1.2M obs ≈ ~143MB)
- Startup: One-time load adds few seconds (acceptable)
- DB still used for: analytics, node/observer queries, role counts, region resolution

---

### Observation RAM Optimization
**By:** Hicks (Backend Dev)  
**Date:** 2026-03-27  
**Status:** Implemented

Observation objects in in-memory packet store now store only `transmission_id` reference instead of copying `hash`, `raw_hex`, `decoded_json`, `payload_type`, `route_type` from parent. API boundary methods (`getById`, `getSiblings`, `enrichObservations`) hydrate on demand. Load uses `.iterate()` instead of `.all()` to avoid materializing full JOIN.

**Impact:** Eliminates ~1.17M redundant string copies, avoids 1.17M-row array during startup. 2.7GB RAM → acceptable levels with 185MB database.

**Code Pattern:** Any code reading observation objects from `tx.observations` directly must use `pktStore.enrichObservations()` if it needs transmission fields. Internal iteration over observations for observer_id, snr, rssi, path_json works unchanged.

---

## Decision: E2E Playwright Performance Improvements

**Author:** Kobayashi (Lead)  
**Date:** 2026-03-26  
**Status:** Proposed — awaiting user sign-off before implementation

Playwright E2E tests (16 tests in `test-e2e-playwright.js`) are slow in CI. Analysis identified ~40-50% potential runtime reduction.

### Recommendations (prioritized)

#### HIGH impact (30%+ improvement)

1. **Replace `waitUntil: 'networkidle'` with `'domcontentloaded'` + targeted waits** — used ~20 times; `networkidle` worst-case for SPAs with persistent WebSocket + Leaflet tile loading. Each navigation pays 500ms+ penalty.

2. **Eliminate redundant navigations** — group tests by route; navigate once, run all assertions for that route.

3. **Cache Playwright browser install in CI** — `npx playwright install chromium --with-deps` runs every frontend push. Self-hosted runner should retain browser between runs.

#### MEDIUM impact (10-30%)

4. **Replace hardcoded `waitForTimeout` with event-driven waits** — ~17s scattered. Replace with `waitForSelector`, `waitForFunction`, or `page.waitForResponse`.

5. **Merge coverage collection into E2E run** — `collect-frontend-coverage.js` launches second browser. Extract `window.__coverage__` at E2E end instead.

6. **Replace `sleep 5` server startup with health-check polling** — Start tests as soon as `/api/stats` responsive (~1-2s savings).

#### LOW impact (<10%)

7. **Block unnecessary resources for non-visual tests** — use `page.route()` to abort map tiles, fonts.

8. **Reduce default timeout 15s → 10s** — sufficient for local CI.

### Implementation notes

- Items 1-2 are test-file-only (Bishop/Newt scope)
- Items 3, 5-6 are CI pipeline (Hicks scope)
- No architectural changes; all incremental
- All assertions remain identical — only wait strategies change

---

### 2026-03-27T20:56:00Z — Protobuf API Contract (Merged)
**By:** Kpa-clawbot (via Copilot)  
**Decision:** 
1. All frontend/backend interfaces get protobuf definitions as single source of truth
2. Go generates structs with JSON tags from protos; Node stays unchanged — protos derived from Node's current JSON shapes
3. Proto definitions MUST use inheritance and composition (no repeating field definitions)
4. Data flow: SQLite → proto struct → JSON; JSON blobs from DB deserialize against proto structs for validation
5. CI pipeline's proto fixture capture runs against prod (stable reference), not staging

**Rationale:** Eliminates parity bugs between Node and Go. Compiler-enforced contract. Prod is known-good baseline.
