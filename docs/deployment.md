# CoreScope Deployment Guide

Comprehensive guide to deploying and operating CoreScope. For a quick start, see [DEPLOY.md](../DEPLOY.md).

## Table of Contents

- [System Requirements](#system-requirements)
- [Docker Deployment](#docker-deployment)
- [Configuration Reference](#configuration-reference)
- [MQTT Setup](#mqtt-setup)
- [TLS / HTTPS](#tls--https)
- [Monitoring & Health Checks](#monitoring--health-checks)
- [Backup & Restore](#backup--restore)
- [Troubleshooting](#troubleshooting)

---

## System Requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| RAM | 256 MB | 512 MB+ |
| Disk | 500 MB (image + DB) | 2 GB+ for long-term data |
| CPU | 1 core | 2+ cores |
| Architecture | `linux/amd64`, `linux/arm64` | — |
| Docker | 20.10+ | Latest stable |

CoreScope runs well on Raspberry Pi 4/5 (ARM64). The Go server uses ~300 MB RAM for 56K+ packets.

---

## Docker Deployment

### Quick Start (one command)

```bash
docker run -d --name corescope \
  -p 80:80 \
  -v corescope-data:/app/data \
  ghcr.io/kpa-clawbot/corescope:latest
```

Open `http://localhost` — you'll see an empty dashboard ready to receive packets.

No `config.json` is required. The server starts with sensible defaults:
- HTTP on port 3000 (Caddy proxies port 80 → 3000 internally)
- Internal Mosquitto MQTT broker on port 1883
- Ingestor connects to `mqtt://localhost:1883` automatically
- SQLite database at `/app/data/meshcore.db`

### Full `docker run` Reference (recommended)

The bare `docker run` command is the primary deployment method. One image, documented parameters — run it however you want.

```bash
docker run -d --name corescope \
  --restart=unless-stopped \
  -p 80:80 -p 443:443 -p 1883:1883 \
  -e DISABLE_MOSQUITTO=false \
  -e DISABLE_CADDY=false \
  -v /your/data:/app/data \
  -v /your/Caddyfile:/etc/caddy/Caddyfile:ro \
  -v /your/caddy-data:/data/caddy \
  ghcr.io/kpa-clawbot/corescope:latest
```

#### Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `-p 80:80` | Yes | HTTP web UI |
| `-p 443:443` | No | HTTPS (only if using built-in Caddy with a domain) |
| `-p 1883:1883` | No | MQTT broker (expose if external gateways connect directly) |
| `-v /your/data:/app/data` | Yes | Persistent data: SQLite DB, config.json, theme.json |
| `-v /your/Caddyfile:/etc/caddy/Caddyfile:ro` | No | Custom Caddyfile for HTTPS |
| `-v /your/caddy-data:/data/caddy` | No | Caddy TLS certificate storage |
| `-e DISABLE_MOSQUITTO=true` | No | Skip the internal Mosquitto broker (use your own) |
| `-e DISABLE_CADDY=true` | No | Skip the built-in Caddy reverse proxy |
| `-e MQTT_BROKER=mqtt://host:1883` | No | Override MQTT broker URL |

#### `/app/data/.env` convenience file

Instead of passing `-e` flags, you can drop a `.env` file in your data volume:

```bash
# /your/data/.env
DISABLE_MOSQUITTO=true
DISABLE_CADDY=true
MQTT_BROKER=mqtt://my-broker:1883
```

The entrypoint sources this file before starting services. This works with any launch method (`docker run`, compose, or manage.sh).

### Docker Compose (legacy alternative)

Docker Compose files are maintained for backward compatibility but are no longer the recommended approach.

```bash
curl -sL https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/docker-compose.example.yml \
  -o docker-compose.yml
docker compose up -d
```

#### Compose environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_PORT` | `80` | Host port for the web UI |
| `DATA_DIR` | `./data` | Host path for persistent data |
| `DISABLE_MOSQUITTO` | `false` | Set `true` to use an external MQTT broker |
| `DISABLE_CADDY` | `false` | Set `true` to skip the built-in Caddy proxy |

### manage.sh (legacy alternative)

The `manage.sh` wrapper script provides a setup wizard and convenience commands. It uses Docker Compose internally. See [DEPLOY.md](../DEPLOY.md) for usage. New deployments should prefer bare `docker run`.

### Image tags

| Tag | Use case |
|-----|----------|
| `v3.4.1` | Pinned release — recommended for production |
| `v3.4` | Latest patch in the v3.4.x series |
| `v3` | Latest minor+patch in v3.x |
| `latest` | Latest release tag |
| `edge` | Built from master on every push — unstable |

### Updating

```bash
docker compose pull
docker compose up -d
```

For `docker run` users:

```bash
docker pull ghcr.io/kpa-clawbot/corescope:latest
docker stop corescope && docker rm corescope
docker run -d --name corescope ... # same flags as before
```

Data is preserved in the volume — updates are non-destructive.

---

## Configuration Reference

CoreScope uses a layered configuration system (highest priority wins):

1. **Environment variables** — `MQTT_BROKER`, `DB_PATH`, etc.
2. **`/app/data/config.json`** — full config file (volume-mounted)
3. **Built-in defaults** — work out of the box with no config

### Environment variable overrides

| Variable | Default | Description |
|----------|---------|-------------|
| `MQTT_BROKER` | `mqtt://localhost:1883` | MQTT broker URL (overrides config file) |
| `MQTT_TOPIC` | `meshcore/#` | MQTT topic subscription pattern |
| `DB_PATH` | `data/meshcore.db` | SQLite database path |
| `DISABLE_MOSQUITTO` | `false` | Skip the internal Mosquitto broker |
| `DISABLE_CADDY` | `false` | Skip the built-in Caddy reverse proxy |

### config.json

For advanced configuration, create a `config.json` and mount it at `/app/data/config.json`:

```bash
docker run -d --name corescope \
  -p 80:80 \
  -v corescope-data:/app/data \
  -v ./config.json:/app/data/config.json:ro \
  ghcr.io/kpa-clawbot/corescope:latest
```

See `config.example.json` in the repository for all available options including:
- MQTT sources (multiple brokers)
- Channel encryption keys
- Branding and theming
- Health thresholds
- Region filters
- Retention policies
- Geo-filtering

---

## MQTT Setup

CoreScope receives MeshCore packets via MQTT. The container ships with an internal Mosquitto broker — no setup needed for basic use.

### Internal broker (default)

The built-in Mosquitto broker listens on port 1883 inside the container. Point your MeshCore gateways at it:

```bash
# Expose MQTT port for external gateways
docker run -d --name corescope \
  -p 80:80 -p 1883:1883 \
  -v corescope-data:/app/data \
  ghcr.io/kpa-clawbot/corescope:latest
```

### External broker

To use your own MQTT broker (Mosquitto, EMQX, HiveMQ, etc.):

1. Disable the internal broker:
   ```bash
   -e DISABLE_MOSQUITTO=true
   ```

2. Point the ingestor at your broker:
   ```bash
   -e MQTT_BROKER=mqtt://your-broker:1883
   ```

   Or via `config.json`:
   ```json
   {
     "mqttSources": [
       {
         "name": "my-broker",
         "broker": "mqtt://your-broker:1883",
         "username": "user",
         "password": "pass",
         "topics": ["meshcore/#"]
       }
     ]
   }
   ```

### Multiple brokers

CoreScope can connect to multiple MQTT brokers simultaneously:

```json
{
  "mqttSources": [
    {
      "name": "local",
      "broker": "mqtt://localhost:1883",
      "topics": ["meshcore/#"]
    },
    {
      "name": "remote",
      "broker": "mqtts://remote-broker:8883",
      "username": "reader",
      "password": "secret",
      "topics": ["meshcore/+/+/packets"]
    }
  ]
}
```

### MQTT topic format

MeshCore gateways typically publish to `meshcore/<gateway>/<region>/packets`. The default subscription `meshcore/#` catches all of them.

---

## TLS / HTTPS

### Option 1: External reverse proxy (recommended)

Run CoreScope behind nginx, Traefik, or Cloudflare Tunnel for TLS termination:

```nginx
# nginx example
server {
    listen 443 ssl;
    server_name corescope.example.com;

    ssl_certificate /etc/ssl/certs/corescope.pem;
    ssl_certificate_key /etc/ssl/private/corescope.key;

    location / {
        proxy_pass http://localhost:80;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }
}
```

The `Upgrade` and `Connection` headers are required for WebSocket support.

### Option 2: Built-in Caddy (auto-TLS)

The container includes Caddy for automatic Let's Encrypt certificates:

1. Create a Caddyfile:
   ```
   corescope.example.com {
     reverse_proxy localhost:3000
   }
   ```

2. Mount it and expose TLS ports:
   ```bash
   docker run -d --name corescope \
     -p 80:80 -p 443:443 \
     -v corescope-data:/app/data \
     -v caddy-certs:/data/caddy \
     -v ./Caddyfile:/etc/caddy/Caddyfile:ro \
     ghcr.io/kpa-clawbot/corescope:latest
   ```

Caddy handles certificate issuance and renewal automatically.

---

## API Documentation

CoreScope auto-generates an OpenAPI 3.0 specification from its route definitions. The spec is always in sync with the running server — no manual maintenance required.

### Endpoints

| URL | Description |
|-----|-------------|
| `/api/spec` | OpenAPI 3.0 JSON schema — machine-readable API definition |
| `/api/docs` | Interactive Swagger UI — browse and test all 40+ endpoints |

### Usage

**Browse the API interactively:**
```
http://your-instance/api/docs
```

**Fetch the spec programmatically:**
```bash
curl http://your-instance/api/spec | jq .
```

**For bot/integration developers:** The spec includes all request parameters, response schemas, and example values. Import it into Postman, Insomnia, or any OpenAPI-compatible tool.

### Public instance
The live instance at [analyzer.00id.net](https://analyzer.00id.net) has all API endpoints publicly accessible:
- Spec: [analyzer.00id.net/api/spec](https://analyzer.00id.net/api/spec)
- Docs: [analyzer.00id.net/api/docs](https://analyzer.00id.net/api/docs)

---

## Monitoring & Health Checks

### Docker health check

The container includes a built-in health check that hits `/api/stats`:

```bash
docker inspect --format='{{.State.Health.Status}}' corescope
```

Docker reports `healthy` or `unhealthy` automatically. The check runs every 30 seconds.

### Manual health check

```bash
curl -f http://localhost/api/stats
```

Returns JSON with packet counts, node counts, and version info:

```json
{
  "totalPackets": 56234,
  "totalNodes": 142,
  "totalObservers": 12,
  "packetsLastHour": 830,
  "packetsLast24h": 19644,
  "engine": "go",
  "version": "v3.4.1"
}
```

### Log monitoring

```bash
# All logs
docker compose logs -f

# Server only
docker compose logs -f | grep '\[server\]'

# Ingestor only
docker compose logs -f | grep '\[ingestor\]'
```

### Resource monitoring

```bash
docker stats corescope
```

---

## Backup & Restore

### Backup

All persistent data lives in `/app/data`. The critical file is the SQLite database:

```bash
# Copy from the Docker volume
docker cp corescope:/app/data/meshcore.db ./backup-$(date +%Y%m%d).db

# Or if using a bind mount
cp ./data/meshcore.db ./backup-$(date +%Y%m%d).db
```

Optional files to back up:
- `config.json` — custom configuration
- `theme.json` — custom theme/branding

### Restore

```bash
# Stop the container
docker stop corescope

# Replace the database
docker cp ./backup.db corescope:/app/data/meshcore.db

# Restart
docker start corescope
```

### Automated backups

```bash
# cron: daily backup at 3 AM, keep 7 days
0 3 * * * docker cp corescope:/app/data/meshcore.db /backups/corescope-$(date +\%Y\%m\%d).db && find /backups -name "corescope-*.db" -mtime +7 -delete
```

---

## Troubleshooting

### Container starts but dashboard is empty

This is normal on first start with no MQTT sources configured. The dashboard shows data once packets arrive via MQTT. Either:
- Point a MeshCore gateway at the container's MQTT broker (port 1883)
- Configure an external MQTT source in `config.json`

### "no MQTT connections established" in logs

The ingestor couldn't connect to any MQTT broker. Check:
1. Is the internal Mosquitto running? (`DISABLE_MOSQUITTO` should be `false`)
2. Is the external broker reachable? Test with `mosquitto_sub -h broker -t meshcore/#`
3. Are credentials correct in `config.json`?

### WebSocket disconnects / real-time updates stop

If behind a reverse proxy, ensure WebSocket upgrade headers are forwarded:
```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
```

Also check proxy timeouts — set them to at least 300s for long-lived WebSocket connections.

### High memory usage

The in-memory packet store grows with retained packets. Configure retention limits in `config.json`:

```json
{
  "packetStore": {
    "retentionHours": 24,
    "maxMemoryMB": 512
  },
  "retention": {
    "nodeDays": 7,
    "packetDays": 30
  }
}
```

### Database locked errors

SQLite doesn't support concurrent writers well. Ensure only one CoreScope instance accesses the database file. If running multiple containers, each needs its own database.

### Container unhealthy

Check logs: `docker compose logs --tail 50`. Common causes:
- Port 3000 already in use inside the container
- Database file permissions (must be writable by the container user)
- Corrupted database — restore from backup

### ARM / Raspberry Pi issues

- Use `linux/arm64` images (Pi 4 and 5). Pi 3 (armv7) is not supported.
- First pull may be slow — the multi-arch manifest selects the right image automatically.
- If memory is tight, set `packetStore.maxMemoryMB` to limit RAM usage.
