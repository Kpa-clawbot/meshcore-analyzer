# Deployment Simplification Spec

**Status:** Draft  
**Author:** Kpa-clawbot  
**Date:** 2026-04-05  

## Current State

CoreScope deployment today requires:

1. **Clone the repo** and build from source (`docker compose build`)
2. **Create a config.json** — the example is 100+ lines with MQTT credentials, channel keys, theme colors, regions, cache TTLs, health thresholds, branding, and more. An operator must understand all of this before seeing a single packet.
3. **Set up a Caddyfile** for TLS (separate `caddy-config/` directory, bind-mounted)
4. **Understand the supervisord architecture** — the container runs 4 processes (mosquitto, ingestor, server, caddy) via supervisord. This is opaque to operators.
5. **No pre-built images** — there's no image on Docker Hub or GHCR. Every operator must `git clone` + `docker compose build`.
6. **Updates require rebuilding** — `git pull && docker compose build && docker compose up -d`. No `docker compose pull`.
7. **manage.sh is 100+ lines** of bash wrapping `docker compose` with state files, confirmations, and color output. It's helpful for the maintainer but intimidating for new operators.

### What works well

- **Dockerfile is solid** — multi-stage Go build, Alpine runtime, small image
- **Health checks exist** — `wget -qO- http://localhost:3000/api/stats`
- **Environment variable overrides** — ports and data dirs are configurable via `.env`
- **Data persistence** — bind mounts for DB (`~/meshcore-data`), named volume for Caddy certs
- **DISABLE_MOSQUITTO flag** — can use external MQTT broker
- **Graceful shutdown** — `stop_grace_period: 30s`, SIGTERM handling

### What's painful

| Pain Point | Impact |
|---|---|
| Must build from source | Blocks anyone without Go/Docker buildx knowledge |
| 100-line config.json required | Operator doesn't know what's optional vs required |
| No sensible defaults for MQTT | Can't connect to public mesh without credentials |
| No pre-built multi-arch images | ARM users (Raspberry Pi) must cross-compile |
| No one-line deploy | Minimum 4 steps: clone, configure, build, start |
| Updates = rebuild | Slow, error-prone, requires git |

## Goal

An operator who has never seen the codebase should be able to run CoreScope with:

```bash
docker run -d -p 80:80 -v corescope-data:/app/data ghcr.io/kpa-clawbot/corescope:v3.4.1
```

And see live MeshCore packets from the public mesh within 60 seconds.

## Pre-built Images

Publish to **GHCR** (`ghcr.io/kpa-clawbot/corescope`) on every release tag.

- **Tags:**
  - `vX.Y.Z` (e.g., `v3.4.1`) — specific release, pinned, recommended for production
  - `vX.Y` (e.g., `v3.4`) — latest patch in a minor series, auto-updates patches only
  - `vX` (e.g., `v3`) — latest minor+patch in a major series
  - `latest` — latest release tag (NOT latest commit). Only moves on tagged releases, never on random master commits. Still, production deployments should pin to `vX.Y.Z`
  - `edge` — built from master on every push. Unstable, for testing only. Clearly labeled as such
- **Architectures:** `linux/amd64`, `linux/arm64` (Raspberry Pi 4/5)
- **Build trigger:** GitHub Actions on `v*` tag push
- **CI workflow:** New job `publish` after existing `deploy`, uses `docker/build-push-action` with QEMU for multi-arch

```yaml
# .github/workflows/publish.yml (simplified)
on:
  push:
    tags: ['v*']
jobs:
  publish:
    runs-on: ubuntu-latest
    permissions:
      packages: write
    steps:
      - uses: actions/checkout@v5
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/build-push-action@v6
        with:
          push: true
          platforms: linux/amd64,linux/arm64
          tags: |
            ghcr.io/kpa-clawbot/corescope:v3.4.1
            ghcr.io/kpa-clawbot/corescope:${{ github.ref_name }}
          build-args: |
            APP_VERSION=${{ github.ref_name }}
            GIT_COMMIT=${{ github.sha }}
            BUILD_TIME=${{ github.event.head_commit.timestamp }}
```

## Configuration

### Hierarchy (highest priority wins)

1. **Environment variables** — `CORESCOPE_MQTT_BROKER`, `CORESCOPE_PORT`, etc.
2. **`/app/data/config.json`** — full config file (volume-mounted)
3. **Built-in defaults** — work out of the box

### Environment variables for common settings

| Variable | Default | Description |
|---|---|---|
| `CORESCOPE_MQTT_BROKER` | `mqtt://localhost:1883` | Primary MQTT broker URL |
| `CORESCOPE_MQTT_TOPIC` | `meshcore/+/+/packets` | MQTT topic pattern |
| `CORESCOPE_PORT` | `3000` | HTTP server port (internal) |
| `CORESCOPE_DB_PATH` | `/app/data/meshcore.db` | SQLite database path |
| `CORESCOPE_SITE_NAME` | `CoreScope` | Branding site name |
| `CORESCOPE_DEFAULT_REGION` | (none) | Default region filter |
| `DISABLE_MOSQUITTO` | `false` | Skip internal MQTT broker |
| `DISABLE_CADDY` | `false` | Skip internal Caddy (when behind reverse proxy) |

### Built-in defaults that work out of the box

The Go server and ingestor already have reasonable defaults compiled in. The only missing piece is **a default public MQTT source** so a fresh instance can see packets immediately. Options:

- **Option A:** Ship with the internal Mosquitto broker only (no external sources). Operator sees an empty dashboard and must configure MQTT. Safe but unhelpful.
- **Option B:** Ship with a public read-only MQTT source pre-configured (e.g., `mqtt.meshtastic.org` or equivalent if one exists for MeshCore). Operator sees live data immediately. Better UX.

**Recommendation:** Option A as default (safe), with a documented one-liner to add a public source. The config.example.json already shows how to add `mqttSources`.

## Compose Profiles

A single `docker-compose.yml` with profiles:

```yaml
services:
  corescope:
    image: ghcr.io/kpa-clawbot/corescope:v3.4.1
    profiles: ["", "standard", "full"]  # runs in all profiles
    ports:
      - "${HTTP_PORT:-80}:80"
    volumes:
      - ${DATA_DIR:-./data}:/app/data
    environment:
      - DISABLE_MOSQUITTO=${DISABLE_MOSQUITTO:-false}
      - DISABLE_CADDY=${DISABLE_CADDY:-false}
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:3000/api/stats"]
      interval: 30s
      timeout: 5s
      retries: 3
    restart: unless-stopped
```

**Note:** Since the container already bundles mosquitto + caddy + server + ingestor via supervisord, "profiles" are really just env var toggles:

| Profile | DISABLE_MOSQUITTO | DISABLE_CADDY | Use case |
|---|---|---|---|
| **minimal** | `true` | `true` | External MQTT + external reverse proxy |
| **standard** (default) | `false` | `true` | Internal MQTT, no TLS (behind nginx/traefik) |
| **full** | `false` | `false` | Everything including Caddy auto-TLS |

This avoids splitting into separate compose services. The monolithic container is actually fine for this use case — it's a single-purpose appliance.

## One-Line Deploy

### Simplest (Docker run, no TLS)

```bash
docker run -d --name corescope \
  -p 80:80 \
  -v corescope-data:/app/data \
  -e DISABLE_CADDY=true \
  ghcr.io/kpa-clawbot/corescope:v3.4.1
```

### With Docker Compose

```bash
curl -sL https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/docker-compose.simple.yml -o docker-compose.yml
docker compose up -d
```

Where `docker-compose.simple.yml` is a minimal 15-line file shipped in the repo.

## Update Path

```bash
docker compose pull
docker compose up -d
```

Or for `docker run` users:

```bash
docker pull ghcr.io/kpa-clawbot/corescope:v3.4.1
docker stop corescope && docker rm corescope
docker run -d --name corescope ... # same args as before
```

No rebuild. No git pull. No source code needed.

## Data Persistence

| Path | Content | Mount |
|---|---|---|
| `/app/data/meshcore.db` | SQLite database (all packets, nodes) | Required volume |
| `/app/data/config.json` | Custom configuration (optional) | Same volume |
| `/app/data/theme.json` | Custom theme (optional) | Same volume |
| `/data/caddy` | TLS certificates (Caddy-managed) | Named volume (automatic) |

**Backup:** `cp ~/corescope-data/meshcore.db ~/backup/` — it's just a SQLite file.

**Migration:** Existing `~/meshcore-data` directories work unchanged. Just point the volume at the same path.

## TLS/HTTPS

### Option 1: Caddy auto-TLS (built-in)

The container ships Caddy. To enable auto-TLS:

1. Mount a custom Caddyfile:
   ```bash
   docker run -d \
     -p 80:80 -p 443:443 \
     -v corescope-data:/app/data \
     -v caddy-certs:/data/caddy \
     -v ./Caddyfile:/etc/caddy/Caddyfile:ro \
     ghcr.io/kpa-clawbot/corescope:v3.4.1
   ```

2. Caddyfile:
   ```
   your-domain.com {
     reverse_proxy localhost:3000
   }
   ```

### Option 2: External reverse proxy (recommended for production)

Run with `DISABLE_CADDY=true` and put nginx/traefik/cloudflare in front. This is the standard approach and what most operators already have.

## Health Checks

Already implemented. The container health check hits `/api/stats`:

```bash
# From outside the container
curl -f http://localhost/api/stats

# Response includes packet counts, node counts, uptime
```

Docker will mark the container as `healthy`/`unhealthy` automatically.

## Monitoring

**Future (M5 from RF health spec):** Expose a `/metrics` Prometheus endpoint with:

- `corescope_packets_total` — total packets ingested
- `corescope_nodes_active` — currently active nodes
- `corescope_mqtt_connected` — MQTT connection status
- `corescope_ingestor_lag_seconds` — time since last packet

This is not required for the deployment simplification work but should be designed alongside it.

## Migration from Current Setup

For existing operators using `manage.sh` + build-from-source:

1. **Keep your data directory** — the bind mount path is the same
2. **Keep your config.json** — it goes in the data directory as before
3. **Replace `docker compose build`** with `docker compose pull`
4. **Update docker-compose.yml** — change `build:` to `image: ghcr.io/kpa-clawbot/corescope:v3.4.1`
5. **manage.sh continues to work** — it wraps `docker compose` and will work with pre-built images

**Breaking changes:** None expected. The container interface (ports, volumes, env vars) stays the same.

## Milestones

### M1: Pre-built images (1-2 days)
- [ ] Create `.github/workflows/publish.yml` for multi-arch builds
- [ ] Push a test `v0.x.0` tag and verify image on GHCR
- [ ] Update README with `docker run` quickstart
- [ ] Create `docker-compose.simple.yml` (minimal compose file using pre-built image)

### M2: Environment variable configuration (1 day)
- [ ] Add env var parsing to Go server `config.go` (overlay on config.json)
- [ ] Add env var parsing to Go ingestor
- [ ] Add `DISABLE_CADDY` support to `entrypoint-go.sh`
- [ ] Document all env vars in README

### M3: Sensible defaults (0.5 day)
- [ ] Ensure server starts with zero config (no config.json required)
- [ ] Verify ingestor connects to localhost MQTT by default
- [ ] Test: `docker run` with no config produces a working (empty) dashboard

### M4: Documentation + migration guide (0.5 day)
- [ ] Write operator-facing deployment docs in `docs/deployment.md`
- [ ] Migration guide for existing users
- [ ] One-page quickstart

**Total estimate:** 3-4 days of work.

## Torvalds Review

> "Is this over-engineered?"

The spec is intentionally simple. Key decisions:

1. **No Kubernetes manifests, Helm charts, or Terraform.** Just Docker.
2. **No config management system.** Env vars + optional JSON file.
3. **Keep the monolithic container.** Splitting into 4 separate services (server, ingestor, mosquitto, caddy) would be "proper" microservices but is worse for operators who just want one thing to run. The supervisord approach is fine for an appliance.
4. **No custom CLI tool.** `docker compose` is the interface.
5. **Profiles are just env vars**, not separate compose files or services.

The simplest version is literally just M1: publish the existing image to GHCR. Everything else is polish. An operator can already `docker run` the image — they just can't `docker pull` it because it's not published anywhere.
