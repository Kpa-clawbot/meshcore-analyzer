# Configuration

CoreScope is configured via `config.json` in the server's working directory. Copy `config.example.json` to get started.

## Core settings

| Field | Default | Description |
|-------|---------|-------------|
| `port` | `3000` | HTTP server port |
| `apiKey` | — | Secret key for admin API endpoints (POST/PUT routes) |
| `dbPath` | — | Path to SQLite database file (optional, defaults to `meshcore.db`) |

## MQTT

```json
"mqtt": {
  "broker": "mqtt://localhost:1883",
  "topic": "meshcore/+/+/packets"
}
```

The ingestor connects to this MQTT broker and subscribes to the topic pattern.

### Multiple MQTT sources

Use `mqttSources` for multiple brokers:

```json
"mqttSources": [
  {
    "name": "local",
    "broker": "mqtt://localhost:1883",
    "topics": ["meshcore/#"]
  },
  {
    "name": "remote",
    "broker": "mqtts://mqtt.example.com:8883",
    "username": "user",
    "password": "pass",
    "topics": ["meshcore/SJC/#"]
  }
]
```

## Branding

| Field | Description |
|-------|-------------|
| `branding.siteName` | Site title shown in the nav bar |
| `branding.tagline` | Subtitle on the home page |
| `branding.logoUrl` | URL to a custom logo image |
| `branding.faviconUrl` | URL to a custom favicon |

## Theme

Colors used throughout the UI. All values are hex color codes.

| Field | Description |
|-------|-------------|
| `theme.accent` | Primary accent color (links, buttons) |
| `theme.navBg` | Navigation bar background |
| `theme.navBg2` | Secondary nav background |
| `theme.statusGreen` | Healthy status color |
| `theme.statusYellow` | Degraded status color |
| `theme.statusRed` | Silent/error status color |

See [Customization](customization.md) for the full list — the theme customizer exposes every color.

## Node colors

Default marker colors by role:

```json
"nodeColors": {
  "repeater": "#dc2626",
  "companion": "#2563eb",
  "room": "#16a34a",
  "sensor": "#d97706",
  "observer": "#8b5cf6"
}
```

## Health thresholds

How long (in hours) before a node is marked degraded or silent:

| Field | Default | Description |
|-------|---------|-------------|
| `healthThresholds.infraDegradedHours` | `24` | Repeaters/rooms → degraded after this many hours |
| `healthThresholds.infraSilentHours` | `72` | Repeaters/rooms → silent after this many hours |
| `healthThresholds.nodeDegradedHours` | `1` | Companions/others → degraded |
| `healthThresholds.nodeSilentHours` | `24` | Companions/others → silent |

## Retention

| Field | Default | Description |
|-------|---------|-------------|
| `retention.nodeDays` | `7` | Nodes not seen in N days move to inactive |
| `retention.packetDays` | `30` | Packets older than N days are deleted daily |

## Channel decryption

| Field | Description |
|-------|-------------|
| `channelKeys` | Object of `"label": "hex-key"` pairs for decrypting channel messages |
| `hashChannels` | Array of channel names (e.g., `"#LongFast"`) to match by hash |

See [Channels](channels.md) for details.

## Map defaults

```json
"mapDefaults": {
  "center": [37.45, -122.0],
  "zoom": 9
}
```

Initial map center and zoom level.

## Regions

```json
"regions": {
  "SJC": "San Jose, US",
  "SFO": "San Francisco, US"
}
```

Named regions for the region filter dropdown. The `defaultRegion` field sets which region is selected by default.

## Cache TTL

All values in seconds. Controls how long the server caches API responses:

```json
"cacheTTL": {
  "stats": 10,
  "nodeList": 90,
  "nodeDetail": 300,
  "analyticsRF": 1800
}
```

Lower values = fresher data but more server load.

## Packet store

| Field | Default | Description |
|-------|---------|-------------|
| `packetStore.maxMemoryMB` | `1024` | Maximum RAM for in-memory packet store |
| `packetStore.estimatedPacketBytes` | `450` | Estimated bytes per packet (for memory budgeting) |

## Timestamps

| Field | Default | Description |
|-------|---------|-------------|
| `timestamps.defaultMode` | `"ago"` | Display mode: `"ago"` (relative) or `"absolute"` |
| `timestamps.timezone` | `"local"` | `"local"` or `"utc"` |
| `timestamps.formatPreset` | `"iso"` | Date format preset |

## Live map

| Field | Default | Description |
|-------|---------|-------------|
| `liveMap.propagationBufferMs` | `5000` | How long to buffer observations before animating |

## HTTPS

```json
"https": {
  "cert": "/path/to/cert.pem",
  "key": "/path/to/key.pem"
}
```

Provide cert and key paths to enable HTTPS.

## Home page

The `home` section customizes the onboarding experience. See `config.example.json` for the full structure including `steps`, `checklist`, and `footerLinks`.
