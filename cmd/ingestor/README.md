# MeshCore MQTT Ingestor (Go)

Standalone MQTT ingestion service for CoreScope. Connects to MQTT brokers, decodes raw MeshCore packets, and writes to the same SQLite database used by the Node.js web server.

This is the first step of a larger Go rewrite — separating MQTT ingestion from the web server.

## Architecture

```
MQTT Broker(s)  →  Go Ingestor  →  SQLite DB  ←  Node.js Web Server
                    (this binary)     (shared)
```

- **Single static binary** — no runtime dependencies, no CGO
- **SQLite** via `modernc.org/sqlite` (pure Go)
- **MQTT** via `github.com/eclipse/paho.mqtt.golang`
- Runs **alongside** the Node.js server — they share the DB file
- Does NOT serve HTTP/WebSocket — that stays in Node.js

## Build

Requires Go 1.22+.

```bash
cd cmd/ingestor
go build -o corescope-ingestor .
```

Cross-compile for Linux (e.g., for the production VM):

```bash
GOOS=linux GOARCH=amd64 go build -o corescope-ingestor .
```

## Run

```bash
./corescope-ingestor -config /path/to/config.json
```

The config file uses the same format as the Node.js `config.json`. The ingestor reads the `mqttSources` array (or legacy `mqtt` object) and `dbPath` fields.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DB_PATH` | SQLite database path | `data/meshcore.db` |
| `MQTT_BROKER` | Single MQTT broker URL (overrides config) | — |
| `MQTT_TOPIC` | MQTT topic (used with `MQTT_BROKER`) | `meshcore/#` |

### Minimal Config

```json
{
  "dbPath": "data/meshcore.db",
  "mqttSources": [
    {
      "name": "local",
      "broker": "mqtt://localhost:1883",
      "topics": ["meshcore/#"]
    }
  ]
}
```

### Full Config (same as Node.js)

The ingestor reads these fields from the existing `config.json`:

- `mqttSources[]` — array of MQTT broker connections
  - `name` — display name for logging
  - `broker` — MQTT URL (`mqtt://`, `mqtts://`)
  - `username` / `password` — auth credentials
  - `topics` — array of topic patterns to subscribe
  - `iataFilter` — optional regional filter
- `mqtt` — legacy single-broker config (auto-converted to `mqttSources`)
- `dbPath` — SQLite DB path (default: `data/meshcore.db`)

## Test

```bash
cd cmd/ingestor
go test -v ./...
```

## What It Does

1. Connects to configured MQTT brokers with auto-reconnect
2. Subscribes to mesh packet topics (e.g., `meshcore/+/+/packets`)
3. Receives raw hex packets via JSON messages (`{ "raw": "...", "SNR": ..., "RSSI": ... }`)
4. Decodes MeshCore packet headers, paths, and payloads (ported from `decoder.js`)
5. Computes content hashes (path-independent, SHA-256-based)
6. Writes to SQLite: `transmissions` + `observations` tables
7. Upserts `nodes` from decoded ADVERT packets (with validation)
8. Upserts `observers` from MQTT topic metadata

## Schema Compatibility

The Go ingestor creates the same v3 schema as the Node.js server:

- `transmissions` — deduplicated by content hash
- `observations` — per-observer sightings with `observer_idx` (rowid reference)
- `nodes` — mesh nodes discovered from adverts
- `observers` — MQTT feed sources

Both processes can write to the same DB concurrently (SQLite WAL mode).

## What's Not Ported (Yet)

- Companion bridge format (Format 2 — `meshcore/advertisement`, channel messages, etc.)
- Channel key decryption (GRP_TXT encrypted payload decryption)
- WebSocket broadcast to browsers
- In-memory packet store
- Cache invalidation

These stay in the Node.js server for now.

## Files

```
cmd/ingestor/
  main.go          — entry point, MQTT connect, message handler
  decoder.go       — MeshCore packet decoder (ported from decoder.js)
  decoder_test.go  — decoder tests (25 tests, golden fixtures)
  db.go            — SQLite writer (schema-compatible with db.js)
  db_test.go       — DB tests (schema validation, insert/upsert, E2E)
  config.go        — config struct + loader
  util.go          — shared utilities
  go.mod / go.sum  — Go module definition
```
