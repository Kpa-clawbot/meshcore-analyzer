# v3.1.0 — Now It's CoreScope

MeshCore Analyzer has a new name: **CoreScope**. Same mesh analysis you rely on, sharper identity, and a boatload of fixes and performance wins since v3.0.0.

48 commits, 30+ issues closed. Here's what changed.

---

## 🏷️ Renamed to CoreScope

The project is now **CoreScope** — frontend, backend, Docker images, manage.sh, docs, CI — everything has been updated. The URL, the API, the database, and your config all stay the same. Just a better name for the tool the community built.

---

## ⚡ Performance

| What | Before | After |
|------|--------|-------|
| Subpath analytics | 900 ms | **5 ms** (precomputed at ingest) |
| Distance analytics | 1.2 s | **15 ms** (precomputed at ingest) |
| Packet ingest (prepend) | O(n) slice copy | **O(1) append** |
| Go runtime stats | GC stop-the-world on every call | **cached ReadMemStats** |
| All analytics endpoints | computed per-request | **TTL-cached** |

The in-memory store now precomputes subpaths and distance data as packets arrive, eliminating expensive full-table scans on the analytics endpoints. The O(n) slice prepend on every ingest — the single hottest line in the server — is gone. `ReadMemStats` calls are cached to prevent GC pause spikes under load.

---

## 🆕 New Features

### Telemetry Decode
Sensor nodes now report **battery voltage** and **temperature** parsed from advert payloads. Telemetry is gated on the sensor flag — only real sensors emit data, and 0°C is no longer falsely reported. Safe migration with `PRAGMA` column checks.

### Channel Decryption for Custom Channels
The `hashChannels` config now works in the Go ingestor. Key derivation has been ported from Node.js with full AES-128-ECB support and garbage text detection — wrong keys silently fail instead of producing garbled output.

### Node Pruning
Stale nodes are automatically moved to an `inactive_nodes` table after the configurable retention window. Pruning runs hourly. Your active node list stays clean. (#202)

### Duplicate Node Name Badges
Nodes with the same display name but different public keys are flagged with a badge so you can spot collisions instantly.

### Sortable Channels Table
Channel columns are now sortable with click-to-sort headers. Sort preferences persist in `localStorage` across sessions. (#167)

### Go Runtime Metrics
The performance page exposes goroutine count, heap allocation, GC pause percentiles, and memory breakdown when connected to a Go backend.

---

## 🐛 Bug Fixes

- **Channel decryption regression** (#176) — full AES-128-ECB in Go, garbage text detection, hashChannels key derivation ported correctly (#218)
- **Packets page not live-updating** (#172) — WebSocket broadcast now includes the nested packet object and timestamp fields the frontend expects; multiple fixes across broadcast and render paths
- **Node detail page crashes** (#190) — `Number()` casts and `Array.isArray` guards prevent rendering errors on unexpected data shapes
- **Observation count staleness** (#174) — trace page and packet detail now show correct observation counts
- **Phantom node cleanup** (#133) — `autoLearnHopNodes` no longer creates fake nodes from 1-byte repeater IDs
- **Advert count inflation** (#200) — counts unique transmissions, not total observations (8 observers × 1 advert = 1, not 8)
- **SQLite BUSY contention** (#214) — `MaxOpenConns(1)` + `MaxIdleConns(1)` serializes writes; load-tested under concurrent ingest
- **Decoder bounds check** (#183) — corrupt/malformed packets no longer crash the decoder with buffer overruns
- **noise_floor / battery_mv type mismatches** — consistent `float64` scanning handles SQLite REAL values correctly
- **packetsLastHour always zero** (#182) — early `break` in observer loop prevented counting
- **Channels stale messages** (#171) — latest message sorted by observation timestamp, not first-seen
- **pprof port conflict** — non-fatal bind with separate ports prevents Go server crash on startup

---

## ♿ Accessibility & 📱 Mobile

### WCAG AA Compliance (10 fixes)
- Search results keyboard-accessible with `tabindex`, `role`, and arrow-key navigation (#208)
- 40+ table headers given `scope` attributes (#211)
- 9 Chart.js canvases given accessible names (#210)
- Form inputs in customizer/filters paired with labels (#212)

### Mobile Responsive
- **Live page**: bottom-sheet panel instead of full-screen overlay (#203)
- **Perf page**: responsive layout with stacked cards (#204)
- **Nodes table**: column hiding at narrow viewports (#205)
- **Analytics/Compare**: horizontal scroll wrappers (#206)
- **VCR bar**: 44px minimum touch targets (#207)

---

## 🏗️ Infrastructure

### manage.sh Refactored (#230)
`manage.sh` is now a thin wrapper around `docker compose` — no custom container management, no divergent logic. It reads `.env` for data paths, matching how `docker-compose.yml` works. One source of truth.

### .env Support
Data directory, ports, and image tags are configured via `.env`. Both `docker compose` and `manage.sh` read the same file.

### Branch Protection & CI on PRs
- Branch protection enabled on `master` — CI must pass, PRs required
- CI now triggers on `pull_request`, not just `push` — catch failures before merge (#199)

### Protobuf API Contract
10 `.proto` files, 33 golden fixtures, CI validation on every push. API shape drift is caught automatically.

### pprof Profiling
Controlled by `ENABLE_PPROF` env var. When enabled, exposes Go profiling endpoints on separate ports — zero overhead when off.

### Test Coverage
- Go backend: **92%+** coverage
- **49 Playwright E2E tests**
- Both tracks gate deploy in CI

---

## 📦 Upgrading

```bash
git pull
./manage.sh stop
./manage.sh setup
```

That's it. Your existing `config.json` and database work as-is. The rename is cosmetic — no schema changes, no API changes, no config changes.

### Verify

```bash
curl -s http://localhost/api/health | grep engine
# "engine": "go"
```

---

## ⚠️ Breaking Changes

**None.** All API endpoints, WebSocket messages, and config options are backwards-compatible. The rename affects branding only — Docker image names, page titles, and documentation.

---

## 🙏 Thank You

- **efiten** — PR #222 performance fix (O(n) slice prepend elimination)
- **jade-on-mesh**, **lincomatic**, **LitBomb**, **mibzzer15** — ongoing testing, feedback, and issue reports

And to everyone running CoreScope on their mesh networks — your real-world data drives every fix and feature in this release. 48 commits since v3.0.0, and every one of them came from something the community found, reported, or requested.

---

*Previous release: [v3.0.0](RELEASE-v3.0.0.md)*
