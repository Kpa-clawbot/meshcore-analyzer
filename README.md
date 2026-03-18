# MeshCore Analyzer

Self-hosted, open-source MeshCore packet analyzer — a community alternative to the closed-source `analyzer.letsmesh.net`.

Collects MeshCore packets via MQTT, decodes them, and presents a full web UI with live packet feed, node map, channel chat, packet tracing, and more.

## Features

- **Live Packet Feed** — real-time WebSocket updates, filterable by type/region/observer
- **Interactive Map** — Leaflet map with node markers by role, clustering, last-heard filters
- **Channel Chat** — decoded group messages with sender names, @mentions, timestamps
- **Node Directory** — searchable node list with role tabs, detail panel, advert timeline
- **Packet Tracing** — follow packets across observers with SNR/RSSI timeline
- **Observer Status** — health monitoring, packet counts, uptime
- **Dark Mode** — toggle with sun/moon icon, persisted in localStorage
- **Global Search** — search packets, nodes, and channels (Ctrl+K)

## Quick Start

### Prerequisites

- **Node.js** 18+ (tested with 22.x)
- **MQTT broker** (Mosquitto recommended) — optional, can inject packets via API

### Install

```bash
git clone https://github.com/youruser/meshcore-analyzer.git
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

To populate the analyzer with synthetic packets for testing/demo:

```bash
# Generate and inject 200 packets via API
node tools/generate-packets.js --api --count 200

# Or output as JSON
node tools/generate-packets.js --json --count 50
```

### Run Tests

```bash
# End-to-end test (starts server, injects packets, validates all APIs)
DB_PATH=/tmp/test-e2e.db PORT=13590 node tools/e2e-test.js

# Frontend smoke test (validates pages load and render correctly)
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
│   ├── map.js           # Leaflet map
│   ├── channels.js      # Channel chat
│   ├── nodes.js         # Node directory
│   ├── traces.js        # Packet tracing
│   └── observers.js     # Observer status
└── tools/
    ├── generate-packets.js  # Synthetic packet generator
    ├── e2e-test.js          # End-to-end API tests
    └── frontend-test.js     # Frontend smoke tests
```

## License

MIT
