# Live

The Live page shows packets flowing through your mesh in real time, with animated map visualizations.

[Screenshot: live page with map animations and packet feed]

## Real-time feed

Packets appear as they arrive via WebSocket. Each entry shows:

- Packet type icon and color
- Sender name
- Observer that captured it
- SNR and hop count
- Timestamp

The feed scrolls automatically. New packets appear at the top.

## Map animations

When a packet arrives, the Live map animates the signal path:

- A pulse appears at the sender's location
- Lines animate from sender to each observer that heard the packet
- Observer markers flash briefly on reception

### Realistic propagation

Enable **Realistic Propagation** in the controls to buffer observations of the same packet and animate them simultaneously — showing how a single transmission ripples through the mesh.

### Ghost hops

When enabled, intermediate relay hops are shown as faded markers even if they don't have known locations. Disable to show only nodes with GPS coordinates.

## VCR mode

The Live page has a built-in VCR (video cassette recorder) for packet replay.

| Button | Action |
|--------|--------|
| ⏸ Pause | Freeze the feed. New packets are buffered but not displayed. |
| ▶ Play | Resume live feed or start replay. |
| ⏪ Rewind | Step backward through packet history. |
| ⏩ Fast-forward | Replay at 2×, 4×, or 8× speed. |

While paused, a badge shows how many packets arrived that you haven't seen yet.

## Timeline

The timeline bar at the bottom shows packet activity over the selected time scope (default: 1 hour). Click anywhere on the timeline to jump to that point in time.

## Packet type legend

Each packet type has a color and icon:

| Type | Icon | Color |
|------|------|-------|
| Advert | 📡 | Green |
| Channel Msg | 💬 | Blue |
| Direct Msg | ✉️ | Amber |
| ACK | ✓ | Gray |
| Request | ❓ | Purple |
| Response | 📨 | Cyan |
| Trace | 🔍 | Pink |
| Path | 🛤️ | Teal |

## Controls

- **Favorites only** — show only packets from your claimed nodes
- **Matrix mode** — visual effect overlay (just for fun)

## Tips

- Use VCR pause when you spot something interesting — then step through packet by packet
- Realistic propagation mode is best for understanding multi-path reception
- The timeline sparkline shows traffic patterns — useful for spotting quiet periods or bursts
