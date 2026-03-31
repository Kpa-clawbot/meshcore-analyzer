# Migrating from Node.js to Go Engine

Guide for existing CoreScope users switching from the Node.js Docker image to the Go version.

> **Status (July 2025):** The Go engine is fully functional for production use.
> Go images are **not yet published to Docker Hub** — you build locally from source.

---

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Backup](#backup)
3. [Config Changes](#config-changes)
4. [Switch to Go](#switch-to-go)
5. [DB Compatibility](#db-compatibility)
6. [Verification](#verification)
7. [Rollback to Node.js](#rollback-to-nodejs)
8. [Known Differences](#known-differences)
9. [FAQ](#faq)

---

## Prerequisites

- **Docker** 20.10+ and **Docker Compose** v2 (verify: `docker compose version`)
- An existing CoreScope deployment running the Node.js image
- The repository cloned locally (needed to build the Go image):
  ```bash
  git clone https://github.com/Kpa-clawbot/meshcore-analyzer.git
  cd corescope
  git pull   # get latest
  ```
- Your `config.json` and `caddy-config/Caddyfile` in place (the same ones you use now)

---

## Backup

**Always back up before switching engines.** The Go engine applies the same v3 schema, but once Go writes to your DB, you want a restore point.

### Using manage.sh

```bash
./manage.sh backup
```

This backs up:
- `meshcore.db` (SQLite database)
- `config.json`
- `Caddyfile`
- `theme.json` (if present)

Backups are saved to `./backups/meshcore-<timestamp>/`.

### Manual backup

```bash
mkdir -p backups/pre-go-migration
cp ~/meshcore-data/meshcore.db backups/pre-go-migration/
cp config.json backups/pre-go-migration/
cp caddy-config/Caddyfile backups/pre-go-migration/
```

Adjust paths if your data directory differs (check `PROD_DATA_DIR` in your `.env` or the default `~/meshcore-data`).

---

## Config Changes

The Go engine reads the **same `config.json`** as Node.js. No changes are required for a basic migration. However, there are a few things to be aware of:

### MQTT broker URLs (automatic)

Node.js uses `mqtt://` and `mqtts://` scheme prefixes. The Go MQTT library (paho) uses `tcp://` and `ssl://`. **The Go ingestor normalizes this automatically** — your existing `mqtt://localhost:1883` config works as-is.

### `retention.nodeDays` (compatible)

Both engines support `retention.nodeDays` (default: 7). Stale nodes are moved to the `inactive_nodes` table on the same schedule. No config change needed.

### `packetStore.maxMemoryMB` (Go ignores this — it's Node-only)

The Node.js server has a configurable in-memory packet store limit (`packetStore.maxMemoryMB`). The Go server has its own in-memory store that loads all packets from SQLite on startup — it does not read this config value. This is safe to leave in your config; Go simply ignores it.

### `channelKeys` / `channel-rainbow.json` (compatible)

Both engines load channel encryption keys:
- From `channelKeys` in `config.json` (inline map)
- From `channel-rainbow.json` next to `config.json`
- Go also supports `CHANNEL_KEYS_PATH` env var and `channelKeysPath` config field

No changes needed.

### `cacheTTL` (compatible)

Both engines read `cacheTTL` from config. Go serves the same values via `/api/config/cache`.

### Go-only config fields

| Field | Description | Default |
|-------|-------------|---------|
| `dbPath` | SQLite path (also settable via `DB_PATH` env var) | `data/meshcore.db` |
| `logLevel` | Ingestor log verbosity | (unset) |
| `channelKeysPath` | Path to channel keys file | `channel-rainbow.json` next to config |

These are optional and safe to add without breaking Node.js (Node ignores unknown fields).

---

## Switch to Go

### Option A: Docker Compose (recommended)

The `docker-compose.yml` already has a `staging-go` service for testing. To run Go in production:

#### Step 1: Build the Go image

```bash
docker compose --profile staging-go build staging-go
```

Or build directly:

```bash
docker build -f Dockerfile.go -t corescope-go:latest \
  --build-arg APP_VERSION=$(git describe --tags 2>/dev/null || echo unknown) \
  --build-arg GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) \
  .
```

#### Step 2: Test with staging-go first

Run the Go image on a separate port alongside your Node.js production:

```bash
# Copies your production DB to staging directory
mkdir -p ~/meshcore-staging-data
cp ~/meshcore-data/meshcore.db ~/meshcore-staging-data/
cp config.json ~/meshcore-staging-data/config.json

# Start the Go staging container (port 82 by default)
docker compose --profile staging-go up -d staging-go
```

Verify at `http://your-server:82` — see [Verification](#verification) below.

#### Step 3: Switch production to Go

Once satisfied, update `docker-compose.yml` to use the Go image for prod:

```yaml
services:
  prod:
    image: corescope-go:latest          # was: corescope:latest
    build:
      context: .
      dockerfile: Dockerfile.go        # add this
    # ... everything else stays the same
```

Then rebuild and restart:

```bash
docker compose build prod
docker compose up -d prod
```

### Option B: manage.sh (legacy single-container)

> ⚠️ `manage.sh` does **not** currently support an `--engine` flag. You must manually switch the image.

```bash
# Stop the current container
./manage.sh stop

# Build the Go image
docker build -f Dockerfile.go -t corescope:latest .

# Start (manage.sh uses the corescope:latest image)
./manage.sh start
```

Note: This **replaces** the Node.js image tag. To switch back, you'll need to rebuild from `Dockerfile` (see [Rollback](#rollback-to-nodejs)).

---

## DB Compatibility

### Schema

Both engines use the same **v3 schema**:

| Table | Purpose | Shared? |
|-------|---------|---------|
| `nodes` | Mesh nodes from adverts | ✅ Both read/write |
| `observers` | MQTT feed sources | ✅ Both read/write |
| `inactive_nodes` | Nodes past retention window | ✅ Both read/write |
| `transmissions` | Deduplicated packets | ✅ Both read/write |
| `observations` | Per-observer sightings | ✅ Both read/write |
| `_migrations` | One-time migration tracking | ✅ Both read/write |

### Can Go read a Node.js DB?

**Yes.** The Go ingestor and server open existing v3 databases with no issues. If the database is pre-v3 (no `observations` table), Go creates it automatically using the same v3 schema.

### Can Node.js read a Go-modified DB?

**Yes.** Go writes the same schema and data formats. You can switch back to Node.js and it will read the DB normally.

### SQLite WAL mode

Both engines use WAL (Write-Ahead Logging) mode for concurrent access. The Go image runs two processes (ingestor + server) writing to the same DB file — same as Node.js running a single process.

### Migration on first run

When Go opens a database for the first time:
1. Creates missing tables (`transmissions`, `observations`, `nodes`, `observers`, `inactive_nodes`) with `CREATE TABLE IF NOT EXISTS`
2. Runs the `advert_count_unique_v1` migration if not already done (recalculates advert counts)
3. Does NOT modify existing data

---

## Verification

After starting the Go engine, verify it's working:

### 1. Check the engine field

```bash
curl -s http://localhost/api/health | jq '.engine'
# Expected: "go"

curl -s http://localhost/api/stats | jq '.engine'
# Expected: "go"
```

The Node.js engine does not include an `engine` field (or returns `"node"`). The Go engine always returns `"engine": "go"`.

### 2. Check packet counts

```bash
curl -s http://localhost/api/stats | jq '{totalPackets, totalNodes, totalObservers}'
```

These should match (or be close to) your pre-migration numbers.

### 3. Check MQTT ingestion

```bash
# Watch container logs for MQTT messages
docker logs -f corescope-prod --tail 20

# Or use manage.sh
./manage.sh mqtt-test
```

You should see `MQTT [source] packet:` log lines as new data arrives.

### 4. Check the UI

Open the web UI in your browser. Navigate through:
- **Nodes** — list should be populated
- **Packets** — table should show data
- **Map** — markers should appear
- **Live** — new packets should stream via WebSocket

### 5. Check WebSocket

Open browser DevTools → Network → WS tab. You should see a WebSocket connection to `/` with periodic packet broadcasts.

---

## Rollback to Node.js

If something goes wrong, switching back is straightforward:

### Docker Compose

```yaml
services:
  prod:
    image: corescope:latest    # back to Node.js
    # Remove the build.dockerfile line if you added it
```

```bash
# Rebuild Node.js image if needed
docker build -t corescope:latest .

docker compose up -d --force-recreate prod
```

### manage.sh (legacy)

```bash
./manage.sh stop

# Rebuild Node.js image (overwrites the corescope:latest tag)
docker build -t corescope:latest .

./manage.sh start
```

### Restore from backup (if DB issues)

```bash
./manage.sh restore ./backups/pre-go-migration
```

Or manually:

```bash
docker stop corescope-prod
cp backups/pre-go-migration/meshcore.db ~/meshcore-data/meshcore.db
docker start corescope-prod
```

---

## Known Differences

### Fully supported in Go

| Feature | Notes |
|---------|-------|
| Raw packet ingestion (Format 1) | Cisien/meshcoretomqtt format — full parity |
| Companion bridge channel messages (Format 2) | `meshcore/message/channel/<n>` — full parity |
| Companion bridge direct messages (Format 2b) | `meshcore/message/direct/<id>` — full parity |
| Channel key decryption | AES-CTR decryption of GRP_TXT payloads — implemented |
| WebSocket broadcast | Real-time packet streaming to browsers |
| In-memory packet store | Loads all packets from DB on startup, serves from RAM |
| All API endpoints | Full REST API parity (see `/api/health`, `/api/stats`, etc.) |
| Node retention / aging | Moves stale nodes to `inactive_nodes` per `retention.nodeDays` |

### Not yet supported in Go

| Feature | Impact | Workaround |
|---------|--------|------------|
| Companion bridge advertisements | `meshcore/advertisement` topic not handled by Go ingestor | Users relying on companion bridge adverts must stay on Node.js or wait for Go support |
| Companion bridge `self_info` | `meshcore/self_info` topic not handled | Same as above — minimal impact (only affects local node identity) |
| `packetStore.maxMemoryMB` config | Go doesn't read this setting | Go manages its own memory; no action needed |
| Docker Hub images | Go images not published yet | Build locally with `docker build -f Dockerfile.go` |
| `manage.sh --engine` flag | Can't toggle engines via manage.sh | Manual image swap required (see [Switch to Go](#switch-to-go)) |

### Behavioral differences

| Area | Node.js | Go |
|------|---------|-----|
| `engine` field in `/api/health` | Not present or `"node"` | Always `"go"` |
| MQTT URL scheme | Uses `mqtt://` / `mqtts://` natively | Auto-converts to `tcp://` / `ssl://` (transparent) |
| Process model | Single Node.js process (server + ingestor) | Two binaries: `corescope-ingestor` + `corescope-server` (managed by supervisord) |
| Memory management | Configurable via `packetStore.maxMemoryMB` | Loads all packets; no configurable limit |
| Startup time | Faster (no compilation) | Slightly slower (loads all packets from DB into memory) |

---

## FAQ

### Can I run Go alongside Node.js?

Yes, but **not writing to the same DB simultaneously across containers**. SQLite supports concurrent readers but cross-container writes via mounted volumes can cause locking issues.

The recommended approach is:
1. Run Go on staging (separate DB copy, separate port)
2. Verify it works
3. Stop Node.js, switch production to Go

### Do I need to change my observer configs?

No. Observers publish to MQTT topics — they don't know or care which engine is consuming the data.

### Will my theme.json and customizations carry over?

Yes. The Go server reads `theme.json` from the data directory (same as Node.js). All CSS variable-based theming works identically since the frontend is the same.

### What about the in-memory packet store size?

The Go server loads all packets from the database on startup. For large databases (100K+ packets), this may use more memory than Node.js with a configured limit. Monitor memory usage after switching.

### Is the frontend different?

No. Both engines serve the exact same `public/` directory. The frontend JavaScript is identical.

---

## Migration Gaps (Tracked Issues)

The following gaps have been identified. Check the GitHub issue tracker for current status:

1. **`manage.sh` has no `--engine` flag** — Users must manually swap Docker images to switch between Node.js and Go. An `--engine go|node` flag would simplify this.

2. **Go ingestor missing `meshcore/advertisement` handling** — Companion bridge advertisement messages are not processed by the Go ingestor. Users who receive node advertisements via companion bridge (not raw packets) will miss node upserts.

3. **Go ingestor missing `meshcore/self_info` handling** — The local node identity topic is not processed. Low impact but breaks parity.

4. **No Docker Hub publishing for Go images** — Users must build locally. CI/CD pipeline should publish `corescope-go:latest` alongside the Node.js image.
