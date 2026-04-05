# Getting Started

## What is CoreScope?

CoreScope is a web-based analyzer for **MeshCore LoRa mesh networks**. It shows you every node, packet, and signal path in your mesh — in real time.

Use it to monitor node health, debug connectivity, view decrypted channel messages, and understand how your mesh is performing.

## What you need

- A running CoreScope server (Go binary + SQLite database)
- An MQTT broker feeding mesh packets into the CoreScope ingestor
- A modern web browser

## Quick start

### 1. Configure

Copy `config.example.json` to `config.json` and edit it:

```json
{
  "port": 3000,
  "apiKey": "pick-a-secret-key",
  "mqtt": {
    "broker": "mqtt://your-broker:1883",
    "topic": "meshcore/+/+/packets"
  }
}
```

See [Configuration](configuration.md) for all options.

### 2. Run

Start both the ingestor (reads MQTT → writes to SQLite) and the server (serves the UI + API):

```bash
./corescope-ingestor &
./corescope-server
```

### 3. Open the UI

Go to `http://localhost:3000`. You'll see the **Home** page.

- **New to MeshCore?** Choose "I'm new" for setup guides and tips.
- **Already set up?** Choose "I know what I'm doing" to jump straight in.

Search for your node by name or public key, then click **+ Claim** to add it to your personal dashboard.

## What's on each page

| Page | What it does |
|------|-------------|
| [Home](getting-started.md) | Your personal mesh dashboard — claimed nodes, health, stats |
| [Nodes](nodes.md) | Browse all nodes with status, role, and filters |
| [Packets](packets.md) | Inspect every packet — grouped or raw, with hex breakdown |
| [Map](map.md) | See node locations on a live map |
| [Live](live.md) | Watch packets flow in real time with map animations |
| [Analytics](analytics.md) | Deep-dive charts: RF, topology, routes, hash stats |
| [Channels](channels.md) | Read decrypted channel messages |

## Home page features

- **Claim nodes** — search and add nodes to "My Mesh" for at-a-glance status cards
- **Node cards** — show status (🟢 Active / 🟡 Degraded / 🔴 Silent), SNR, hops, packet count, and 24h sparkline
- **Health detail** — click a card to see full health: observers, recent packets, mini map
- **Packet journey** — click a recent packet to see sender → observer flow
- **Network stats** — total transmissions, nodes, observers, and 24h activity
