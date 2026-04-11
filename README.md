# CoreScope

[![Go Server Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/.badges/go-server-coverage.json)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)
[![Go Ingestor Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/.badges/go-ingestor-coverage.json)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)
[![E2E Tests](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/.badges/e2e-tests.json)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)
[![Frontend Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/.badges/frontend-coverage.json)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)
[![Deploy](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml/badge.svg)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)

> High-performance mesh network analyzer powered by Go. Sub-millisecond packet queries, ~300 MB memory for 56K+ packets, real-time WebSocket broadcast, full channel decryption.

Self-hosted, open-source MeshCore packet analyzer. Collects MeshCore packets via MQTT, decodes them in real time, and presents a full web UI with live packet feed, interactive maps, channel chat, packet tracing, and per-node analytics.

## ⚡ Performance

The Go backend serves all 40+ API endpoints from an in-memory packet store with 5 indexes (hash, txID, obsID, observer, node). SQLite is for persistence only — reads never touch disk.

| Metric | Value |
|--------|-------|
| Packet queries | **< 1 ms** (in-memory) |
| All API endpoints | **< 100 ms** |
| Memory (56K packets) | **~300 MB** (vs 1.3 GB on Node.js) |
| WebSocket broadcast | **Real-time** to all connected browsers |
| Channel decryption | **AES-128-ECB** with rainbow table |

See [PERFORMANCE.md](PERFORMANCE.md) for full benchmarks.

## ✨ Features

### 📡 Live Trace Map
Real-time animated map with packet route visualization, VCR-style playback controls, and a retro LCD clock. Replay the last 24 hours of mesh activity, scrub through the timeline, or watch packets flow live at up to 4× speed.

![Live VCR playback — watch packets flow across the Bay Area mesh](docs/screenshots/MeshVCR.gif)

### 📦 Packet Feed
Filterable real-time packet stream with byte-level breakdown, Excel-like resizable columns, and a detail pane. Toggle "My Nodes" to focus on your mesh.

![Packets view](docs/screenshots/packets1.png)

### 🗺️ Network Overview
At-a-glance mesh stats — node counts, packet volume, observer coverage.

![Network overview](docs/screenshots/mesh-overview.png)

### 📊 Node Analytics
Per-node deep dive with interactive charts: activity timeline, packet type breakdown, SNR distribution, hop count analysis, peer network graph, and hourly heatmap.

![Node analytics](docs/screenshots/node-analytics.png)

### 💬 Channel Chat
Decoded group messages with sender names, @mentions, timestamps — like reading a Discord channel for your mesh.

![Channels](docs/screenshots/channels1.png)

### 📱 Mobile Ready
Full experience on your phone — proper touch controls, iOS safe area support, and a compact VCR bar.

<img src="docs/screenshots/Live-view-iOS.png" alt="Live view on iOS" width="300">

### And More

- **11 Analytics Tabs** — RF, topology, channels, hash stats, distance, route patterns, and more
- **Node Directory** — searchable list with role tabs, detail panel, QR codes, advert timeline
- **Packet Tracing** — follow individual packets across observers with SNR/RSSI timeline
- **Observer Status** — health monitoring, packet counts, uptime, per-observer analytics
- **Hash Collision Matrix** — detect address collisions across the mesh
- **Channel Key Auto-Derivation** — hashtag channels (`#channel`) keys derived via SHA256
- **Multi-Broker MQTT** — connect to multiple brokers with per-source IATA filtering
- **Dark / Light Mode** — auto-detects system preference, map tiles swap too
- **Theme Customizer** — design your theme in-browser, export as `theme.json`
- **Global Search** — search packets, nodes, and channels (Ctrl+K)
- **Shareable URLs** — deep links to packets, channels, and observer detail pages
- **Protobuf API Contract** — typed API definitions in `proto/`
- **Accessible** — ARIA patterns, keyboard navigation, screen reader support

## Quick Start

### Pre-built Image (Recommended)

No build step required — just run:

```bash
docker run -d --name corescope \
  --restart=unless-stopped \
  -p 80:80 -p 1883:1883 \
  -v /your/data:/app/data \
  ghcr.io/kpa-clawbot/corescope:latest
```

Open `http://localhost` — done. No config file needed; CoreScope starts with sensible defaults.

For HTTPS with a custom domain, add `-p 443:443` and mount your Caddyfile:
```bash
docker run -d --name corescope \
  --restart=unless-stopped \
  -p 80:80 -p 443:443 -p 1883:1883 \
  -v /your/data:/app/data \
  -v /your/Caddyfile:/etc/caddy/Caddyfile:ro \
  -v /your/caddy-data:/data/caddy \
  ghcr.io/kpa-clawbot/corescope:latest
```

Disable built-in services with `-e DISABLE_MOSQUITTO=true` or `-e DISABLE_CADDY=true`, or drop a `.env` file in your data volume. See [docs/deployment.md](docs/deployment.md) for the full reference.

### Build from Source

```bash
git clone https://github.com/Kpa-clawbot/CoreScope.git
cd CoreScope
./manage.sh setup
```

The setup wizard walks you through config, domain, HTTPS, build, and run.

```bash
./manage.sh status       # Health check + packet/node counts
./manage.sh logs         # Follow logs
./manage.sh backup       # Backup database
./manage.sh update       # Pull latest + rebuild + restart
./manage.sh mqtt-test    # Check if observer data is flowing
./manage.sh help         # All commands
```

### Configure

Copy `config.example.json` to `config.json` and edit:

```json
{
  "port": 3000,
  "mqtt": {
    "broker": "mqtt://localhost:1883",
    "topic": "meshcore/+/+/packets"
  },
  "mqttSources": [
    {
      "name": "remote-feed",
      "broker": "mqtts://remote-broker:8883",
      "topics": ["meshcore/+/+/packets"],
      "username": "user",
      "password": "pass",
      "iataFilter": ["SJC", "SFO", "OAK"]
    }
  ],
  "channelKeys": {
    "public": "8b3387e9c5cdea6ac9e5edbaa115cd72"
  },
  "defaultRegion": "SJC"
}
```

| Field | Description |
|-------|-------------|
| `port` | HTTP server port (default: 3000) |
| `mqtt.broker` | Local MQTT broker URL (`""` to disable) |
| `mqttSources` | External MQTT broker connections (optional) |
| `channelKeys` | Channel decryption keys (hex). Hashtag channels auto-derived via SHA256 |
| `defaultRegion` | Default IATA region code for the UI |
| `dbPath` | SQLite database path (default: `data/meshcore.db`) |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `PORT` | Override config port |
| `DB_PATH` | Override SQLite database path |

## Architecture

```
                           ┌─────────────────────────────────────────────┐
                           │              Docker Container               │
                           │                                             │
Observer → USB →           │  Mosquitto ──→ Go Ingestor ──→ SQLite DB   │
  meshcoretomqtt → MQTT ──→│                    │                        │
                           │              Go HTTP Server ──→ WebSocket   │
                           │                    │               │        │
                           │              Caddy (HTTPS) ←───────┘        │
                           └────────────────────┼────────────────────────┘
                                                │
                                             Browser
```

**Two-process model:** The Go ingestor handles MQTT ingestion and packet decoding. The Go HTTP server loads all packets into an in-memory store on startup (5 indexes for fast lookups) and serves the REST API + WebSocket broadcast. Both are managed by supervisord inside a single container with Caddy for HTTPS and Mosquitto for local MQTT.

## MQTT Setup

1. **Flash an observer node** with `MESH_PACKET_LOGGING=1` build flag
2. **Connect via USB** to a host running [meshcoretomqtt](https://github.com/Cisien/meshcoretomqtt)
3. **Configure meshcoretomqtt** with your IATA region code and MQTT broker address
4. **Packets appear** on topic `meshcore/{IATA}/{PUBKEY}/packets`

Or POST raw hex packets to `POST /api/packets` for manual injection.

## Project Structure

```
corescope/
├── cmd/
│   ├── server/              # Go HTTP server + WebSocket + REST API
│   │   ├── main.go          # Entry point
│   │   ├── routes.go        # 40+ API endpoint handlers
│   │   ├── store.go         # In-memory packet store (5 indexes)
│   │   ├── db.go            # SQLite persistence layer
│   │   ├── decoder.go       # MeshCore packet decoder
│   │   ├── websocket.go     # WebSocket broadcast
│   │   └── *_test.go        # 327 test functions
│   └── ingestor/            # Go MQTT ingestor
│       ├── main.go          # MQTT subscription + packet processing
│       ├── decoder.go       # Packet decoder (shared logic)
│       ├── db.go            # SQLite write path
│       └── *_test.go        # 53 test functions
├── proto/                   # Protobuf API definitions
├── public/                  # Vanilla JS frontend (no build step)
│   ├── index.html           # SPA shell
│   ├── app.js               # Router, WebSocket, utilities
│   ├── packets.js           # Packet feed + hex breakdown
│   ├── map.js               # Leaflet map + route visualization
│   ├── live.js              # Live trace + VCR playback
│   ├── channels.js          # Channel chat
│   ├── nodes.js             # Node directory + detail views
│   ├── analytics.js         # 11-tab analytics dashboard
│   └── style.css            # CSS variable theming (light/dark)
├── docker/
│   ├── supervisord-go.conf  # Process manager (server + ingestor)
│   ├── mosquitto.conf       # MQTT broker config
│   ├── Caddyfile            # Reverse proxy + HTTPS
│   └── entrypoint-go.sh     # Container entrypoint
├── Dockerfile               # Multi-stage Go build + Alpine runtime
├── config.example.json      # Example configuration
├── test-*.js                # Node.js test suite (frontend + legacy)
└── tools/                   # Generators, E2E tests, utilities
```

## For Developers

### Test Suite

**380 Go tests** covering the backend, plus **150+ Node.js tests** for the frontend and legacy logic, plus **49 Playwright E2E tests** for browser validation.

```bash
# Go backend tests
cd cmd/server && go test ./... -v
cd cmd/ingestor && go test ./... -v

# Node.js frontend + integration tests
npm test

# Playwright E2E (requires running server on localhost:3000)
node test-e2e-playwright.js
```

### Generate Test Data

```bash
node tools/generate-packets.js --api --count 200
```

### Migrating from Node.js

If you're running an existing Node.js deployment, see [docs/go-migration.md](docs/go-migration.md) for a step-by-step guide. The Go engine reads the same SQLite database and `config.json` — no data migration needed.

## Contributing

Contributions welcome. Please read [AGENTS.md](AGENTS.md) for coding conventions, testing requirements, and engineering principles before submitting a PR.

**Live instance:** [analyzer.00id.net](https://analyzer.00id.net) — all API endpoints are public, no auth required.

**API Documentation:** CoreScope auto-generates an OpenAPI 3.0 spec. Browse the interactive Swagger UI at [`/api/docs`](https://analyzer.00id.net/api/docs) or fetch the machine-readable spec at [`/api/spec`](https://analyzer.00id.net/api/spec).

## License

MIT
