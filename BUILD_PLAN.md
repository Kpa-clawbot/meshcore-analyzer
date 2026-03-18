# MeshCore Analyzer — Build Plan (v2)

## Target
Open-source clone of `analyzer.letsmesh.net` — self-hosted MeshCore packet analyzer.

## Nav Bar
Packets | Map | Channels | Nodes | Traces | More

## CRITICAL: meshcore-decoder Path Parsing Bug

The `@michaelhart/meshcore-decoder` npm library treats path_length as raw byte count.
Per `Packet.h`, it encodes hash_size (top 2 bits) + hash_count (lower 6 bits):
```
hash_size  = (path_len >> 6) + 1
hash_count = path_len & 63
path_bytes = hash_size * hash_count
```
We wrote our own decoder in `decoder.js`.

## Tech Stack
- **Frontend**: SPA, vanilla HTML/CSS/JS, Leaflet for maps, WebSocket for live updates
- **Backend**: Node.js + Express + better-sqlite3 + ws + mqtt
- **Decoder**: Custom `decoder.js` (from Packet.h spec)
- **Style**: Dark-ish theme, blue accent (matches letsmesh)

## Milestones

### M1: Custom Packet Decoder ✅ (in progress)
- `decoder.js` — decodes all packet types from raw hex
- Correct path_length parsing (hash_size/hash_count encoding)

### M2: SQLite Schema ✅ DONE
- `db.js` — packets, nodes, observers, paths tables
- All CRUD helpers, pagination, filtering

### M3: Server + MQTT + WebSocket + API
- `server.js` — single file combining Express server, MQTT subscriber, WebSocket broadcaster, REST API
- MQTT: subscribe to `meshcore/+/+/packets`, decode, store, broadcast via WS
- POST `/api/packets` for manual injection ("Bring Your Own Packet")
- REST endpoints:
  - `GET /api/packets` — paginated, filterable by type/region/observer/hash, ?groupByHash=true for dedup
  - `GET /api/packets/:id` — single packet with full decode + path + byte breakdown
  - `GET /api/nodes` — filterable by role (repeater/room/companion/sensor), region, lastHeard
  - `GET /api/nodes/:pubkey` — detail + recent adverts
  - `GET /api/channels` — list of known channels with last message + message count
  - `GET /api/channels/:hash/messages` — paginated messages for a channel
  - `GET /api/observers` — observer list with stats
  - `GET /api/stats` — summary counts
  - `GET /api/traces/:hash` — trace a packet across observers
- WebSocket: broadcast new packets + messages to connected clients
- Config via `config.json` (MQTT broker, channel keys, default region)

### M4: SPA Shell + Packets Page
- `public/index.html` — SPA shell with nav bar, client-side routing
- `public/style.css` — dark/blue theme matching letsmesh aesthetic
- `public/app.js` — router, WebSocket client, shared utilities
- **Packets page** (`public/packets.js`):
  - Table: Region, Time, Hash (truncated), Size, Type (color badge), First Observer, Repeats, Path (hop arrows), Details
  - Filters: Observer dropdown, Region, Type, time range
  - "Group by Hash" toggle (collapses duplicates from multiple observers)
  - "Bring Your Own Packet" button → hex input modal
  - Click row → right panel: **Packet Byte Breakdown**
    - Header metadata (observer, radio: freq/SF/BW/CR)
    - Color-coded hex dump (header=red, path=green, payload=yellow)
    - Field-by-field table: offset, field name, value, description
    - For ADVERTs: Public Key, Timestamp, Signature, App Flags, Lat, Lon, Node Name sections
  - Auto-scroll with pause, WebSocket live updates

### M5: Map Page
- `public/map.js` — Leaflet full-screen map
- Node markers: icon/color by role (repeater=blue filled, companion=blue outline, room=grey, sensor=small)
- Controls panel (top-right):
  - Node type checkboxes with counts (Repeaters, Companions, Room Servers, Sensors)
  - "Show clusters" toggle
  - Filters: "MQTT Connected Only", "Show direct neighbors"
  - "Last Heard" dropdown (1h, 6h, 24h, 7d, 30d)
  - Quick Jump buttons (SJC, LAR, etc.) — region IATA codes
- Click marker → popup: Name, Key (truncated), Location, Last Advert time, Observers/regions

### M6: Channels Page (the killer feature)
- `public/channels.js` — chat-style UI
- Left sidebar: channel list
  - Each channel: color badge (2-letter abbreviation), name, last message preview, time
  - Channels: Public (default key), #bot, #test, #emergency, #hamradio, #jokes, #sports, #chat, etc.
  - Region selector dropdown at top
- Right panel: message feed for selected channel
  - Sender avatar (first letter of name, colored circle — consistent color per sender)
  - Sender name
  - Message bubble (dark background)
  - @mentions highlighted in green/accent
  - Below each message: time, "X repeats heard by Y observers [REGION] - LP: Z", "Analyze" link
  - Decryption using known channel keys (public channel key built-in)
  - Auto-scroll, WebSocket live updates

### M7: Nodes Page
- `public/nodes.js` — node directory
- Quick search bar at top
- Count badges: N repeaters, N rooms, N companions, N sensors
- Tabs: Repeaters | Rooms | Companions | Sensors
- Table: Name, Public Key (truncated), Region tags (SJC, SFO, OAK as badges), Last Seen ("Xm ago")
- Sortable columns
- Click row → right panel: **Node Detail**
  - Mini Leaflet map showing node location
  - Name, full public key, regions, first/last seen
  - QR code (encode node public key for sharing)
  - "Copy URL" button
  - **Recent Adverts** timeline: green/blue event dots with timestamps, "heard X times", link to raw packet

### M8: Traces Page
- `public/traces.js` — packet propagation tracing
- Input: packet hash
- Shows: which observers saw this packet, in what order, with what SNR/RSSI
- Timeline visualization of packet propagation across the mesh
- Path visualization (node hashes with arrows)

### M9: Observer Status Page (under More dropdown)
- `public/observers.js`
- Table: Name, IATA region, last seen, packet count, uptime
- Health indicators (green/yellow/red)
- Packets/hour sparkline or bar

### M10: Polish
- Dark mode toggle (sun/moon icon in nav)
- Search (magnifying glass in nav)
- Login button (placeholder for future auth)
- Config file support
- README with setup instructions
- "Forum" link in nav (external link placeholder)

### M11: Synthetic Packet Generator (`tools/generate-packets.js`)
- Generates realistic synthetic MeshCore packets of ALL types:
  - ADVERTs: random node names, locations scattered across Bay Area, varied roles (repeater/room/companion/sensor), realistic pubkeys, varied hop counts (0-8)
  - GRP_TXT: messages on multiple channels (public, #bot, #test, #emergency, #chat), realistic sender names, @mentions, varied message content
  - TXT_MSG: direct messages between random node pairs
  - ACK: acknowledgments for recent messages
  - REQ/RESPONSE: request-response pairs
  - TRACE: trace packets with SNR values in path
  - PATH: path announcements
- Multiple synthetic observers (SJC-OBS-1, SFO-OBS-2, OAK-OBS-3) each "hearing" packets with realistic SNR/RSSI values (-120 to -60 dBm, -20 to +10 dB SNR)
- Same packet heard by multiple observers (with different SNR/RSSI) to test dedup/groupByHash
- Packets arrive with realistic timing (not all at once — stagger over simulated time window)
- Output modes:
  - `--mqtt` — publish to local MQTT broker (requires mosquitto running)
  - `--api` — POST to `http://localhost:3000/api/packets`
  - `--json` — dump to stdout/file for manual import
- Configurable: `--count 500 --duration 60` (500 packets over 60 seconds)
- Default: 200 packets, mix of ~60% ADVERTs, 25% GRP_TXT, 10% ACK, 5% other

### M12: End-to-End Validation (`tools/e2e-test.js`)
- Automated test script that:
  1. Starts the server (spawns `node server.js` as child process)
  2. Waits for server ready (poll `/api/stats`)
  3. Connects a WebSocket client to track live updates
  4. Sends 100+ synthetic packets via POST `/api/packets`
  5. Validates:
     - **Ingestion**: `/api/stats` shows correct total counts
     - **Packets API**: `/api/packets` returns all packets, filtering by type/region/observer works, pagination works, groupByHash correctly deduplicates
     - **Packet detail**: `/api/packets/:id` returns correct byte breakdown with color ranges, decoded payload matches expected values
     - **Nodes**: ADVERTs created nodes in `/api/nodes`, role filtering works, node detail shows recent adverts
     - **Channels**: GRP_TXT packets appear in `/api/channels`, messages are listed with correct sender/text
     - **Observers**: all synthetic observers appear with correct packet counts
     - **Traces**: same-hash packets from different observers appear in `/api/traces/:hash`
     - **WebSocket**: received real-time broadcasts for each injected packet
  6. Tests MQTT path (if mosquitto available): publish raw MQTT messages, verify they flow through to DB + WebSocket
  7. Prints pass/fail summary with details on failures
  8. Kills the server process
  9. Exit code 0 = all pass, 1 = failures

### M13: Frontend Smoke Tests (`tools/frontend-test.js`)
- Uses the server + synthetic data from M12
- Fetches each page's HTML and verifies:
  - index.html loads, contains nav with all links
  - Packets page: table renders, filters exist, byte breakdown panel works
  - Map page: Leaflet container exists, node markers present
  - Channels page: channel sidebar renders, messages load
  - Nodes page: tabs exist, node detail panel works
  - Traces page: input exists, results render
  - Observers page: table renders with data
- Validates no JavaScript errors in page scripts (basic syntax check)
- Validates API calls each page makes return expected data shapes

## Dependency Chain
```
M1 + M2 (done) → M3 (done) → M4 (SPA+packets) + M5 (map) + M6 (channels) + M7 (nodes) in parallel → M8 (traces) + M9 (observers) → M11 (packet generator) → M12 (e2e test) → M13 (frontend smoke) → M10 (polish)
```

## Sub-Agent Execution
- Each milestone = one sub-agent
- M3 is next (depends on M1+M2)
- After M3: spawn M4, M5, M6, M7 in parallel
- Cron job checks every 30min, tests milestones, spawns next

## File Structure
```
meshcore-analyzer/
├── package.json
├── config.json         (MQTT broker, channel keys, regions)
├── server.js           (Express + WS + MQTT + API — all in one)
├── decoder.js          (custom packet decoder)
├── db.js               (SQLite schema + queries)
├── data/
│   └── meshcore.db
└── public/
    ├── index.html      (SPA shell + nav)
    ├── style.css       (dark/blue theme)
    ├── app.js          (router, WS client, utils)
    ├── packets.js      (packets page + byte breakdown)
    ├── map.js          (Leaflet map page)
    ├── channels.js     (chat-style channels page)
    ├── nodes.js        (node directory + detail panel)
    ├── traces.js       (packet trace page)
    └── observers.js    (observer status page)
```

## Test Data
ADVERT packet (Test Repeater, 5 hops, 2-byte hashes):
```
11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172
```

Default public channel key: `8b3387e9c5cdea6ac9e5edbaa115cd72`
