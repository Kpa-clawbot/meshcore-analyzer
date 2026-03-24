# MeshCore Analyzer

[![Tests](https://github.com/Kpa-clawbot/meshcore-analyzer/actions/workflows/deploy.yml/badge.svg)](https://github.com/Kpa-clawbot/meshcore-analyzer/actions/workflows/deploy.yml)

> Self-hosted, open-source MeshCore packet analyzer — a community alternative to the closed-source `analyzer.letsmesh.net`.

Collects MeshCore packets via MQTT, decodes them, and presents a full web UI with live packet feed, node map, channel chat, packet tracing, per-node analytics, and more.

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

### 🔀 Route Patterns
Visualize how packets traverse the mesh — see which repeaters carry the most traffic and identify routing patterns.

![Route patterns](docs/screenshots/route-patterns.png)

### 📊 Node Analytics
Per-node deep dive with 6 interactive charts: activity timeline, packet type breakdown, SNR distribution, hop count analysis, peer network graph, and hourly heatmap.

![Node analytics](docs/screenshots/node-analytics.png)

### 💬 Channel Chat
Decoded group messages with sender names, @mentions, timestamps — like reading a Discord channel for your mesh.

![Channels](docs/screenshots/channels1.png)

### 📱 Mobile Ready
Full experience on your phone — proper touch controls, iOS safe area support, and a compact VCR bar that doesn't fight your thumb.

<img src="docs/screenshots/Live-view-iOS.png" alt="Live view on iOS" width="300">

### And More

- **Node Directory** — searchable list with role tabs, detail panel, QR codes, advert timeline, "Heard By" observer table
- **Packet Tracing** — follow individual packets across observers with SNR/RSSI timeline
- **Observer Status** — health monitoring, packet counts, uptime
- **Hash Collision Matrix** — detect address collisions across the mesh
- **Claimed Nodes** — star your nodes, always sorted to top, visual distinction
- **Dark / Light Mode** — auto-detects system preference, instant toggle, map tiles swap too
- **Multi-Broker MQTT** — connect to multiple MQTT brokers simultaneously with per-source IATA filtering
- **Observer Detail Pages** — click any observer for analytics, charts, status, radio info, recent packets
- **Channel Key Auto-Derivation** — hashtag channels (`#channel`) keys derived automatically via SHA256
- **Global Search** — search packets, nodes, and channels (Ctrl+K)
- **Shareable URLs** — deep links to individual packets, channels, and observer detail pages
- **Mobile Responsive** — proper two-row VCR bar, iOS safe area support, touch-friendly
- **Accessible** — ARIA patterns, keyboard navigation, screen reader support, distinct marker shapes

### ⚡ Performance (v2.1.1)

Two-layer caching architecture: in-memory packet store + TTL response cache. All packet reads served from RAM — SQLite is write-only. Heavy endpoints pre-warmed on startup.

| Endpoint | Before | After | Speedup |
|---|---|---|---|
| Bulk Health | 7,059 ms | 1 ms | **7,059×** |
| Node Analytics | 381 ms | 1 ms | **381×** |
| Topology | 685 ms | 2 ms | **342×** |
| Node Health | 195 ms | 1 ms | **195×** |
| Node Detail | 133 ms | 1 ms | **133×** |

See [PERFORMANCE.md](PERFORMANCE.md) for the full benchmark.

## Quick Start

### Docker (Recommended)

The easiest way to run MeshCore Analyzer. Includes Mosquitto MQTT broker — everything in one container.

```bash
docker build -t meshcore-analyzer .
docker run -d \
  --name meshcore-analyzer \
  -p 80:80 \
  -p 443:443 \
  -p 1883:1883 \
  -v meshcore-data:/app/data \
  -v caddy-certs:/data/caddy \
  meshcore-analyzer
```

Open `http://localhost`. Point your MeshCore gateway's MQTT to `<host-ip>:1883`.

**With a domain (automatic HTTPS):**
```bash
# Create a Caddyfile with your domain
echo 'analyzer.example.com { reverse_proxy localhost:3000 }' > Caddyfile

docker run -d \
  --name meshcore-analyzer \
  -p 80:80 \
  -p 443:443 \
  -p 1883:1883 \
  -v meshcore-data:/app/data \
  -v caddy-certs:/data/caddy \
  -v $(pwd)/Caddyfile:/etc/caddy/Caddyfile \
  meshcore-analyzer
```

Caddy automatically provisions Let's Encrypt TLS certificates.

**Custom config:**
```bash
# Copy and edit the example config
cp config.example.json config.json
# Edit config.json with your channel keys, regions, etc.

docker run -d \
  --name meshcore-analyzer \
  -p 3000:3000 \
  -p 1883:1883 \
  -v meshcore-data:/app/data \
  meshcore-analyzer
```

Config lives in the data volume at `/app/data/config.json` — a default is created on first run. To edit it:
```bash
docker exec -it meshcore-analyzer vi /app/data/config.json
```

Or use a bind mount for the data directory:
```bash
docker run -d \
  --name meshcore-analyzer \
  -p 3000:3000 \
  -p 1883:1883 \
  -v ./data:/app/data \
  meshcore-analyzer
# Now edit ./data/config.json directly on the host
```

**Theme customization:** Put `theme.json` next to `config.json` — wherever your config lives, that's where the theme goes. Use the built-in customizer (Tools → Customize) to design your theme, download the file, and drop it in. Changes are picked up on page refresh — no restart needed. The server logs where it's looking on startup.

### Manual Install

#### Prerequisites

- **Node.js** 18+ (tested with 22.x)
- **MQTT broker** (Mosquitto recommended) — optional, can inject packets via API

### Install

```bash
git clone https://github.com/Kpa-clawbot/meshcore-analyzer.git
cd meshcore-analyzer
npm install
```

### Configure

Edit `config.json`:

```json
{
  "port": 3000,
  "https": {
    "cert": "/path/to/cert.pem",
    "key": "/path/to/key.pem"
  },
  "mqtt": {
    "broker": "mqtt://localhost:1883",
    "topic": "meshcore/+/+/packets"
  },
  "mqttSources": [
    {
      "name": "remote-feed",
      "broker": "mqtts://remote-broker:8883",
      "topics": ["meshcore/+/+/packets", "meshcore/+/+/status"],
      "username": "user",
      "password": "pass",
      "rejectUnauthorized": false,
      "iataFilter": ["SJC", "SFO", "OAK"]
    }
  ],
  "channelKeys": {
    "public": "8b3387e9c5cdea6ac9e5edbaa115cd72"
  },
  "defaultRegion": "SJC",
  "regions": {
    "SJC": "San Jose, US",
    "SFO": "San Francisco, US",
    "OAK": "Oakland, US"
  }
}
```

| Field | Description |
|-------|-------------|
| `port` | HTTP server port (default: 3000) |
| `https.cert` / `https.key` | Optional PEM cert/key paths to enable native HTTPS (falls back to HTTP if omitted or unreadable) |
| `mqtt.broker` | Local MQTT broker URL. Set to `""` to disable |
| `mqtt.topic` | MQTT topic pattern for packet ingestion |
| `mqttSources` | Array of external MQTT broker connections (optional) |
| `mqttSources[].name` | Friendly name for logging |
| `mqttSources[].broker` | Broker URL (`mqtt://` or `mqtts://` for TLS) |
| `mqttSources[].topics` | Array of MQTT topic patterns to subscribe to |
| `mqttSources[].username` / `password` | Broker credentials |
| `mqttSources[].rejectUnauthorized` | Set `false` for self-signed TLS certs |
| `mqttSources[].iataFilter` | Only accept packets from these IATA regions |
| `channelKeys` | Named channel decryption keys (hex). Hashtag channels auto-derived via SHA256 |
| `defaultRegion` | Default IATA region code for the UI |
| `regions` | Map of IATA codes to human-readable region names |

### Run

```bash
node server.js
```

Open `http://localhost:3000` in your browser.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `PORT` | Override config.json port |
| `DB_PATH` | Override SQLite database path (default: `data/meshcore.db`) |

### Generate Test Data

```bash
# Generate and inject 200 packets via API
node tools/generate-packets.js --api --count 200

# Or output as JSON
node tools/generate-packets.js --json --count 50
```

### Run Tests

```bash
# End-to-end test
DB_PATH=/tmp/test-e2e.db PORT=13590 node tools/e2e-test.js

# Frontend smoke test
DB_PATH=/tmp/test-fe.db PORT=13591 node tools/frontend-test.js
```

## MQTT Setup

MeshCore packets flow into the analyzer via MQTT:

1. **Flash an observer node** with `MESH_PACKET_LOGGING=1` build flag
2. **Connect via USB** to a host running [meshcoretomqtt](https://github.com/Cisien/meshcoretomqtt)
3. **Configure meshcoretomqtt** with your IATA region code and MQTT broker address
4. **Packets appear** on topic `meshcore/{IATA}/{PUBKEY}/packets`

Alternatively, POST raw hex packets to `POST /api/packets` for manual injection.

## Architecture

```
Observer Node → USB → meshcoretomqtt → MQTT Broker → Analyzer Server → WebSocket → Browser
                                                    → SQLite DB
                                                    → REST API
```

## Project Structure

```
meshcore-analyzer/
├── Dockerfile           # Single-container build (Node + Mosquitto + Caddy)
├── .dockerignore
├── config.example.json  # Example config (copy to config.json)
├── config.json          # MQTT, channel keys, regions (gitignored)
├── server.js            # Express + WebSocket + MQTT + REST API
├── decoder.js           # Custom MeshCore packet decoder
├── db.js                # SQLite schema + queries
├── packet-store.js      # In-memory packet store (ring buffer, indexed)
├── docker/
│   ├── supervisord.conf # Process manager config
│   ├── mosquitto.conf   # MQTT broker config
│   ├── Caddyfile        # Default Caddy config (localhost)
│   └── entrypoint.sh    # Container entrypoint
├── data/
│   └── meshcore.db      # Packet database (auto-created)
├── public/
│   ├── index.html       # SPA shell
│   ├── style.css        # Theme (light/dark)
│   ├── app.js           # Router, WebSocket, utilities
│   ├── packets.js       # Packet feed + byte breakdown + detail page
│   ├── map.js           # Leaflet map with route visualization
│   ├── live.js          # Live trace page with VCR playback
│   ├── channels.js      # Channel chat
│   ├── nodes.js         # Node directory + detail views
│   ├── analytics.js     # Global analytics dashboard
│   ├── node-analytics.js # Per-node analytics with charts
│   ├── traces.js        # Packet tracing
│   ├── observers.js     # Observer status
│   ├── observer-detail.js # Observer detail with analytics
│   ├── home.js          # Dashboard home page
│   └── perf.js          # Performance monitoring dashboard
└── tools/
    ├── generate-packets.js  # Synthetic packet generator
    ├── e2e-test.js          # End-to-end API tests
    └── frontend-test.js     # Frontend smoke tests
```

## License

MIT
