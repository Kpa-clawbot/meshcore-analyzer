# New User Experience — Build Spec

## Overview
A new "home" page for the MeshCore Analyzer that serves as a **diagnostic companion** for new mesh users. The existing pages (packets, map, channels, nodes, observers, traces) remain untouched — this is a new front door.

## Architecture
- New page registered as `home` in the SPA router (hash `#/` or `#/home`)
- New file: `public/home.js` — the page module
- New file: `public/home.css` — styles (imported via `<link>` in index.html)
- New server endpoints in `server.js` for node lookup
- Default route changes from `#/packets` to `#/home`

## Page Structure

### Section 1: Hero Search
- Big centered search box with placeholder: "Search by node name or public key..."
- Subtitle: "Check if your packets are reaching the mesh"
- Auto-suggest dropdown as user types (search nodes API)
- Below search: quick stats bar — "X nodes active · Y packets today · Z observers online"
- Clean, minimal. No nav clutter competing for attention.

### Section 2: Node Health Card (shown after search)
Appears below search with a smooth transition. Contains:

**Status Banner:**
- 🟢 HEALTHY: "Heard by N observers in the last hour"
- 🟡 DEGRADED: "Last heard 3 hours ago by 1 observer" 
- 🔴 SILENT: "Not heard in 24+ hours"
- Status logic: green (<1h), yellow (1-24h), red (>24h)

**Mini Map:**
- Small Leaflet map showing the node's position + observer positions that heard it
- Lines from node to observers, thickness = signal strength
- Only if node has lat/lon

**Key Metrics (card grid):**
- Last heard: relative time
- Observers: count + names
- Avg SNR: with plain-English quality label (Excellent >10, Good 0-10, Marginal -5-0, Poor <-5)
- Hop count: typical path length
- Packets today: count

**Recent Activity Timeline:**
- Last 10 packets from this node, shown as a simple timeline
- Each entry: time, type (ADVERT/GRP_TXT/etc), observers that heard it, hop count
- Click any packet → links to `#/packets?id=X` (existing detail view)

### Section 3: Packet Journey (expandable, one click)
When user clicks a specific packet from the timeline:
- Visual representation: Node → Hop 1 → Hop 2 → Observer
- Each step shows SNR, timing
- Simple horizontal flow diagram using CSS (no canvas/SVG needed for v1)
- Educational tooltips on hover:
  - "ADVERT: This tells the network your node exists"
  - "3 hops: Your packet traveled through 3 repeaters"
  - "SNR -2 dB: Marginal signal — a better antenna would help"

### Section 4: Quick Setup Checklist
Always visible at bottom, collapsible accordion:

**"Not seeing results? Check these:"**
1. **Correct preset?** — Show recommended presets by region (US: 910.525 MHz, SF7, BW62.5, CR5)
2. **Sent a flood advert?** — "In the MeshCore app, tap the signal icon → Flood Advert. This announces your node to the network."
3. **See 'Heard N repeats'?** — "After sending a message, the app shows how many repeaters forwarded it. 0 repeats = no one heard you."
4. **Repeaters near you?** — Link to `#/map` filtered to repeaters
5. **Right frequency?** — "If you're on old settings (SF11, BW125), you can't talk to nodes on the new recommended preset"

### Section 5: "Ready for more?" footer
Links to the full dashboard pages:
- 🗺️ Network Map — See all nodes and coverage
- 💬 Channels — Read mesh chat messages  
- 📦 Packet Inspector — Deep dive into raw packets
- 📡 Observers — See who's listening

## Server API Additions

### GET /api/nodes/search?q=<query>
- Search nodes by name (partial match) or public_key (prefix match)
- Returns top 10 matches: `{ nodes: [{ public_key, name, role, lat, lon, last_seen }] }`

### GET /api/nodes/:pubkey/health
- Returns health summary for a specific node:
```json
{
  "node": { "public_key": "...", "name": "...", "role": "...", "lat": ..., "lon": ..., "last_seen": "..." },
  "status": "healthy|degraded|silent",
  "observers": [{ "id": "...", "lastHeard": "...", "avgSnr": ..., "avgRssi": ..., "packetCount": ... }],
  "stats": {
    "packetsToday": 42,
    "avgSnr": 3.2,
    "avgHops": 2.1,
    "lastHeard": "2026-03-17T14:30:00Z"
  },
  "recentPackets": [
    { "id": 123, "timestamp": "...", "payload_type": 4, "hash": "...", "observer_id": "...", "snr": 3.1, "rssi": -82, "hopCount": 2 }
  ]
}
```

## DB Queries Needed (add to db.js)

### searchNodes(query, limit=10)
```sql
SELECT * FROM nodes WHERE name LIKE ? OR public_key LIKE ? ORDER BY last_seen DESC LIMIT ?
```

### getNodeHealth(pubkey)
Multiple queries:
1. Get node by pubkey
2. Get packets where decoded_json contains this pubkey (for ADVERTs) or observer patterns
3. Aggregate observer stats
4. Recent packets list

Note: Since we don't have a `source_node` column on packets, for ADVERTs we can match by decoded_json containing the pubkey. For other packet types this is harder — we may need to add a `source_pubkey` column in a future iteration. For v1, focus on ADVERT packets as the primary "was I heard?" signal, since that's exactly what new users send first.

## Styling Guidelines
- Use existing CSS variables (--bg, --text, --accent, --border, --surface-0/1/2/3, etc.)
- Dark mode must work (use var() everywhere, no hardcoded colors)
- Mobile responsive (works at 640px)
- Match existing design language but cleaner/more spacious for the landing page
- Status colors: green=#22c55e, yellow=#eab308, red=#ef4444

## File Locations
All files in `/root/.openclaw/workspace/meshcore-analyzer/`
- `public/home.js` — new
- `public/home.css` — new  
- `public/index.html` — add `<link>` for home.css, add `<script>` for home.js
- `server.js` — add 2 new endpoints
- `db.js` — add 2 new query methods
- `public/app.js` — change default route from packets to home
