# Packets

The Packets page shows every transmission captured by your mesh observers.

[Screenshot: packets table with grouped view]

## Grouped vs ungrouped view

By default, packets are **grouped by hash**. Each row represents one unique transmission, with a count of how many observers heard it.

Click **Ungroup** to see every individual observation as its own row.

Click the **▶** arrow on a grouped row to expand it and see all observations of that packet.

## What each row shows

- **Time** — when the packet was received
- **From** — sender node name or hash prefix
- **Type** — packet type (Advert, Channel Msg, Direct Msg, ACK, Request, Response, Trace, Path)
- **Observer** — which observer captured the packet
- **SNR** — signal-to-noise ratio in dB
- **RSSI** — received signal strength
- **Hops** — how many relay hops the packet took

## Filters

### Observer filter

Select a specific observer to see only packets it captured. Saved across sessions.

### Type filter

Filter by packet type (e.g., show only Adverts or Channel Messages).

### Time window

Choose how far back to look: 15 minutes, 1 hour, 6 hours, 24 hours, etc. On mobile, the window is capped at 3 hours for performance.

### Wireshark-style filter bar

Type filter expressions for advanced filtering:

```
type:advert snr>5 hops<3
from:MyNode observer:SJC
```

See the filter bar's help tooltip for all supported fields and operators.

## Packet detail

Click any row to open the **detail pane** on the right showing:

- Full packet metadata (hash, type, size, timestamp)
- Decoded payload fields
- Hop path with resolved node names
- All observers that heard this packet, sorted by SNR

### Hex breakdown

The detail pane includes a hex dump of the raw packet bytes with field boundaries highlighted.

## Observation sorting

When viewing a grouped packet's observations, they're sorted by SNR (best signal first). This helps you see which observer had the clearest reception.

## Display options

- **Hex hashes** — toggle to show packet hashes in hex format
- **Panel resize** — drag the detail pane border to resize it
- **Keyboard shortcuts** — press `Esc` to close the detail pane

## Tips

- Grouped view is best for understanding what's happening on the mesh
- Ungrouped view is best for debugging signal paths and comparing observers
- The time window filter is your best friend for managing large datasets
- Packet hashes in the URL are deep-linkable — share a link to a specific packet
