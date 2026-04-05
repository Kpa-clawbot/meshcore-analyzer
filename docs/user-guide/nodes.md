# Nodes

The Nodes page lists every node your mesh has seen — repeaters, companions, rooms, and sensors.

[Screenshot: nodes list with status indicators]

## What you see

Each row shows:

- **Name** — the node's advertised name (or public key if unnamed)
- **Role** — Repeater, Companion, Room, or Sensor
- **Status** — color-coded health indicator
- **Last seen** — when the node was last heard
- **Advert count** — how many advertisements this node has sent

## Status indicators

| Indicator | Meaning |
|-----------|---------|
| 🟢 Active | Heard recently (within threshold for its role) |
| 🟡 Degraded | Not heard for a while but not yet silent |
| 🔴 Silent | Not heard for an extended period |

Thresholds differ by role. Infrastructure nodes (repeaters, rooms) have longer grace periods than companions. See [Configuration](configuration.md) for `healthThresholds`.

## Filtering

### Role tabs

Click **All**, **Repeaters**, **Rooms**, **Companions**, or **Sensors** to filter by role.

### Search

Type in the search box to filter by name or public key. The filter applies instantly.

### Status filter

Filter to show only active, degraded, or silent nodes.

### Last heard filter

Filter nodes by how recently they were heard (e.g., last hour, last 24h).

## Sorting

Click any column header to sort. Click again to reverse the order. Your sort preference is saved across sessions.

## Node detail

Click a node row to open the **detail pane** on the right. It shows:

- Full public key
- Role and status explanation
- Location (if known)
- Recent packets involving this node
- Neighbor nodes
- Signal statistics

Click the node name in the detail pane to open the **full node page** with complete history, analytics, and health data.

## Favorites

Nodes you've claimed on the Home page appear as favorites. You can also star nodes directly from the Nodes page.

## Tips

- Use the search box for quick lookups — it matches partial names and keys
- Sort by "Last seen" descending to find the most active nodes
- The status explanation tells you exactly why a node is marked degraded or silent
