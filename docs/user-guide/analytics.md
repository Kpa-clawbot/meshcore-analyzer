# Analytics

The Analytics page provides deep-dive charts and tables about your mesh network. Select a tab to explore different aspects.

[Screenshot: analytics page with tab bar]

## Overview

Summary dashboard with key network metrics at a glance. Quick sparklines and counts across all data dimensions.

## RF / Signal

Radio frequency analysis:

- **SNR distribution** — histogram of signal-to-noise ratios across all packets
- **RSSI distribution** — histogram of received signal strength
- **SNR by observer** — which observers are getting the best signals
- **Signal trends** — how signal quality changes over time

Use this to identify weak links or noisy observers.

## Topology

Network structure analysis:

- **Hop count distribution** — how many relay hops packets typically take
- **Top relay nodes** — which repeaters handle the most traffic
- **Node connectivity** — how well-connected each node is

## Channels

Channel message statistics:

- **Messages per channel** — which channels are most active
- **Channel activity over time** — traffic trends by channel
- **Top senders** — most active nodes per channel

## Hash Stats

Mesh hash size analysis:

- **Hash size distribution** — how many bytes nodes use for addressing
- **Hash sizes by role** — do repeaters use different hash sizes than companions?

## Hash Issues

Potential hash collision detection:

- **Collision pairs** — nodes whose short hash prefixes overlap
- **Risk assessment** — how likely collisions are at current hash sizes

Hash collisions can cause packet misrouting. If you see collisions here, consider increasing hash sizes on affected nodes.

## Route Patterns (Subpaths)

Common routing paths through the mesh:

- **Frequent subpaths** — which relay chains appear most often
- **Path reliability** — how consistently each path is used
- **Path detail** — click a subpath to see every packet that used it

## Nodes

Per-node analytics with sortable metrics across the fleet.

## Distance

Estimated distances between nodes based on GPS coordinates, correlated with signal quality.

## Neighbor Graph

Interactive visualization of which nodes can directly hear each other. Shows the mesh topology as a network graph.

## RF Health

Per-observer signal health over time. Identifies observers with degrading reception.

## Prefix Tool

Test hash prefix lengths to see how many collisions different sizes would produce. Useful for deciding on hash_size settings.

## Region filter

All analytics tabs respect the **region filter** at the top. Select a region to scope the data to observers in that area.

## Deep linking

Each tab is deep-linkable. Share a URL like `#/analytics?tab=collisions` to point someone directly at hash issues.
