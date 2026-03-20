# MeshCore Analyzer

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
- **Dark / Light Mode** — auto-detects system preference, instant toggle
- **Global Search** — search packets, nodes, and channels (Ctrl+K)
- **Mobile Responsive** — proper two-row VCR bar, iOS safe area support, touch-friendly
- **Accessible** — ARIA patterns, keyboard navigation, screen reader support, distinct marker shapes

## Quick Start

### Prerequisites

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
  "mqtt": {
    "broker": "mqtt://localhost:1883",
    "topic": "meshcore/+/+/packets"
  },
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
| `mqtt.broker` | MQTT broker URL. Set to `""` to disable MQTT and use API-only mode |
| `mqtt.topic` | MQTT topic pattern for packet ingestion |
| `channelKeys` | Named channel decryption keys (hex). `public` is the default MeshCore public channel |
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
├── config.json          # MQTT, channel keys, regions
├── server.js            # Express + WebSocket + MQTT + REST API
├── decoder.js           # Custom MeshCore packet decoder
├── db.js                # SQLite schema + queries
├── data/
│   └── meshcore.db      # Packet database (auto-created)
├── public/
│   ├── index.html       # SPA shell
│   ├── style.css        # Theme (light/dark)
│   ├── app.js           # Router, WebSocket, utilities
│   ├── packets.js       # Packet feed + byte breakdown
│   ├── map.js           # Leaflet map with route visualization
│   ├── live.js          # Live trace page with VCR playback
│   ├── channels.js      # Channel chat
│   ├── nodes.js         # Node directory + detail views
│   ├── analytics.js     # Global analytics dashboard
│   ├── node-analytics.js # Per-node analytics with charts
│   ├── traces.js        # Packet tracing
│   └── observers.js     # Observer status
└── tools/
    ├── generate-packets.js  # Synthetic packet generator
    ├── e2e-test.js          # End-to-end API tests
    └── frontend-test.js     # Frontend smoke tests
```

## License

MIT
