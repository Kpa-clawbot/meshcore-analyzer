# v3.0.0 — The Go Rewrite

MeshCore Analyzer is now powered by Go. The entire backend — MQTT ingestion, packet decoding, API server, WebSocket broadcast — has been rewritten from Node.js to Go. Same features, same UI, same database. Dramatically faster.

This is the biggest change in the project's history. Over 200 commits, 58 issues closed, and a ground-up reimplementation that delivers real, measurable performance gains on every endpoint.

---

## ⚡ Performance

These are real numbers from production with 56K+ packets:

| Endpoint | Node.js | Go |
|----------|---------|-----|
| Packet queries | 30-100ms | **sub-millisecond** (in-memory store) |
| GroupByHash | 437ms (9s before store) | **97ms** |
| Analytics (RF, topology, distance) | 1-8 seconds | **all under 100ms** |
| Node health calculation | 13 seconds | **instant** (precomputed) |
| Server startup (56K packets) | ~9 seconds | **< 1 second** |
| Memory (56K packets) | ~1.3 GB | **~300 MB** |

The Go server loads all packets into an in-memory store at startup and serves queries directly from RAM. Analytics are precomputed at ingest time — no more scanning the full packet table on every request. TTL caches protect expensive aggregations. The result: every page in the UI feels instant.

---

## 🆕 New Features

### Protobuf API Contract
10 `.proto` files define the exact shape of all 40+ API endpoints and WebSocket messages. Golden fixture tests ensure the Go server matches the Node.js response format byte-for-byte. API drift is caught in CI before it reaches production.

### Go Runtime Metrics
The performance page now shows Go-specific runtime stats when connected to a Go backend: goroutine count, heap allocation, GC pause percentiles, and memory breakdown. The engine badge in the stats bar shows **[go]** or **[node]** so you always know which backend you're running.

### Build Identity
Every API response from `/api/stats` and `/api/health` now includes `engine`, `version`, `commit`, and `buildTime` fields. The stats bar in the UI shows the commit hash as a clickable link to the exact source.

### Observer Packet Comparison (#129)
New `#/compare` page lets you compare what different observers saw for the same packet — side-by-side diffs of paths, timestamps, and signal data.

### Auto-Updating Nodes List
The Nodes tab now updates in real-time when ADVERT packets arrive via WebSocket. No more manual refresh to see new nodes.

### Channel Improvements
- Channel hash displayed for undecrypted GRP_TXT messages — you can see *which* channel even without the key
- Sortable channels table with persistent column sort preferences
- Garbage decryption detection — wrong keys no longer produce garbled "decrypted" text
- AES-128-CTR channel decryption natively in Go

### Node Pruning (#202)
Nodes past the retention window are automatically moved to an `inactive_nodes` table instead of polluting the active node list. Pruning runs hourly.

### Correct Advert Counts
Advert counts now reflect unique transmissions, not total observations. A packet seen by 8 observers counts as 1 advert, not 8.

---

## 🐛 Bug Fixes

- **Phantom nodes from hop prefixes** (#133) — `autoLearnHopNodes` no longer creates fake nodes from 1-byte repeater IDs. Active node counts, live page counter, and topology analytics all filtered to real nodes only.
- **Offline nodes on map** (#126) — ambiguous hop prefixes excluded from path-seen tracking. Stale nodes dim on the live map instead of disappearing.
- **Disappearing live map nodes** (#130) — stale nodes are dimmed, not removed, preventing the jarring vanish-and-reappear cycle.
- **packetsLastHour always zero** (#182) — early `break` in observer loop prevented counting; fixed across all observers.
- **Corrupted packet decoder crash** (#183) — bounds check on path hops prevents buffer overrun on malformed packets.
- **Node detail rendering crashes** (#190) — `Number()` casts and `Array.isArray` guards harden against unexpected data shapes.
- **Topology uniqueNodes inflated** — hop prefixes no longer counted as real nodes in analytics.
- **Channels stale messages** (#171) — latest message now sorted by observation timestamp, not first-seen.
- **MQTT puback errors** (#161) — explicit QoS 0 subscription prevents protocol-level flag errors.
- **WebSocket broadcast missing fields** (#162, #172) — nested packet object and timestamp field added to match frontend expectations.

---

## 🏗️ Architecture

The Go backend is two binaries managed by supervisord inside Docker:

- **`corescope-ingestor`** — connects to MQTT brokers, decodes packets, writes to SQLite, maintains the in-memory store
- **`corescope-server`** — HTTP API, WebSocket broadcast, static file serving, analytics computation

Both share the same SQLite database (WAL mode). The frontend is unchanged — same vanilla JS, same `public/` directory, served by the Go HTTP server through Caddy.

### CI Pipeline
The CI pipeline runs two independent tracks:
- **Node.js track**: unit tests, E2E Playwright tests, coverage badges
- **Go track**: `go test` with 92%+ coverage, golden fixture parity tests, proto contract validation

Both must pass before deploy.

---

## 📦 Upgrading

### For Docker Compose users (recommended)

```bash
git pull
docker compose down
docker compose build prod
docker compose up -d prod
```

### For manage.sh users

```bash
git pull
./manage.sh stop
./manage.sh setup
```

The Go engine reads your existing `config.json` with no changes. MQTT URLs (`mqtt://` → `tcp://`) are normalized automatically. Your database is compatible in both directions — Go reads Node.js databases and vice versa.

### Verify the upgrade

```bash
curl -s http://localhost/api/health | grep engine
# "engine": "go"
```

### Rolling back

The Node.js Dockerfile is preserved as `Dockerfile.node`:

```bash
docker build -f Dockerfile.node -t corescope:latest .
docker compose up -d --force-recreate prod
```

See [docs/go-migration.md](docs/go-migration.md) for the full migration guide.

---

## ⚠️ Breaking Changes

**None for end users.** All API endpoints return the same data in the same shape. The frontend works identically on both backends.

The only additions are new fields in `/api/stats` and `/api/health`:
- `engine` — `"go"` or `"node"`
- `version` — semver string
- `commit` — short git hash
- `buildTime` — ISO timestamp

These are additive and do not break existing integrations.

---

## 🙏 Thank You

This release wouldn't exist without the community:

- **efiten** — PR #128 contribution
- **jade-on-mesh** — testing, feedback, and issue reports throughout the Go migration
- **lincomatic** — issue reports and real-world deployment testing
- **LitBomb** — issue reports from production deployments
- **mibzzer15** — issue reports and edge case discovery

And to everyone running CoreScope in the wild — your packet data, bug reports, and feature requests are what drive this project forward. The Go rewrite happened because the community outgrew what Node.js could handle. 56K packets, dozens of observers, sub-second queries. This is your tool. We just rewrote the engine.

---

*Full migration guide: [docs/go-migration.md](docs/go-migration.md)*
*Previous release: [v2.6.0](RELEASE-v2.6.0.md)*
