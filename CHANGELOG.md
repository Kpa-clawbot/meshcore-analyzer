# Changelog

## [2.4.1] — 2026-03-22

Hotfix release for regressions introduced in v2.4.0.

### Fixed
- Packet ingestion broken: `insert()` returned undefined after legacy table removal, causing all MQTT packets to fail silently
- Live packet updates not working: pause button `addEventListener` on null element crashed `init()`, preventing WS handler registration
- Pause button not toggling: event delegation was on `app` variable not in IIFE scope; moved to `document`
- WS broadcast had null packet data when observation was deduped (2nd+ observer of same packet)
- Multi-select filter menu close handler crashed on null `observerFilterWrap`/`typeFilterWrap` elements
- Live map animation cleanup crashed with null `animLayer`/`pathsLayer` after navigating away (setInterval kept firing)

## [2.4.0] — 2026-03-22

UI polish, client-side filtering, time window selector, DB cleanup, and bug fixes.

### Added
- Observation-level deeplinks (`#/packets/HASH?obs=OBSERVER_ID`)
- Observation detail pane (click any child row for its specific data)
- Observation sort: Observer / Path ↑↓ / Time ↑↓ with persistent preference
- Ungrouped mode flattens all observations into individual rows
- Sort help tooltip (ⓘ) explaining each mode
- Distance/Range analytics tab with haversine calculations
- View on Map buttons for distance leaderboard entries
- Realistic packet propagation mode on live map
- Packet propagation time in detail pane
- Replay sends all observations with realistic animation
- Paths-through section on node detail (desktop + mobile)
- Regional filters on all tabs (shared RegionFilter component)
- Favorites filter on live map (packet-level, not node markers)
- Configurable map defaults via `config.json`
- Hash prefix labels on map with spiral deconfliction + callout lines
- Channel rainbow table (pre-computed keys for common names)
- Zero-API live channel updates via WebSocket
- Channel message dedup by packet hash
- Channel name tags (blue pill) in packet detail column
- Shareable channel URLs (`#/channels/HASH`)
- API key required for POST endpoints
- HTTPS support (lincomatic PR #105)
- Graceful shutdown (lincomatic PR #109)
- Filter bar: logical grouping, consistent 34px height, help tooltips
- Multi-select Observer and Type filters (checkbox dropdowns, OR logic)
- Hex Paths toggle: show raw hex hash prefixes vs resolved node names
- Time window selector (15min/30min/1h/3h/6h/12h/24h/All) replaces fixed packet count limit
- Pause/resume button (⏸/▶) for live WebSocket updates with buffered packet count
- localStorage persistence for all filter/view preferences

### Changed
- Channel keys: plain `String(channelHash)`, `hashChannels` for auto-derived SHA256
- Node region filtering uses ADVERT-based index (accurate local presence vs mesh-wide routing)
- Header row reflects first sorted observation's data
- Max hop distance filter: 1000km → 300km (LoRa record ~250km)
- Route view labels use deconflicted divIcons
- Channels page hides encrypted messages, shows only decrypted
- Dark mode: active filter buttons retain accent styling
- Region dropdown: `IATA - Friendly Name` format, proper sizing
- Observer/Type filters are pure client-side (no API calls on filter change)
- Packet loading: time-window based (`since`) instead of fixed count limit
- Header row shows matching observer when observer filter is active

### Removed
- Legacy `packets` and `paths` database tables (auto-migrated on startup)
- Redundant server-side type/observer filtering (client filters in-memory)

### Fixed
- Header row showed longest path instead of first observer's path
- Observer/path mismatch when earlier observation arrives later
- Auto-seeding fake data on empty DB (now requires `--seed` flag)
- Channel "10h ago" bug (used stale `first_seen` instead of current time)
- Stale UI: wrong ID type for packet lookup after insert
- ADVERT timestamp validation rejecting valid nodes
- Channels page API spam on every WS update
- Duplicate observations in expanded view
- Analytics RF 500 error (stack overflow with 193K observations)
- Region filter SQL using non-existent column
- Channel hash: decimal→hex, keyed by decrypted name
- Corrupted repeater entries (ADVERT validation at ingestion)
- Hash_size: uses newest ADVERT, precomputed at startup
- Tab backgrounding: skip animations, resume cleanly
- Feed panel position (obscured by VCR bar)
- Hop disambiguation anchored from sender origin
- Packet hash case normalization for deeplinks
- Critical: packet ingestion broken after legacy table removal (`insert()` returned undefined)
- Sort help tooltip rendering (CSS pseudo-elements don't support newlines)

### Performance
- `/api/analytics/distance`: 3s → 630ms
- `/api/analytics/topology`: 289ms → 193ms
- `/api/observers`: 3s → 130ms
- `/api/nodes`: 50ms → 2ms (precomputed hash_size)
- Event loop max: 3.2s → 903ms (startup only)
- Pre-warm yields event loop via `setImmediate`
- Client-side hop resolution
- SQLite manual PASSIVE checkpointing
- Single API call for packet expand (was 3)

## [2.3.0] - 2026-03-20

### Added
- **Packet Deduplication**: Normalized storage with `transmissions` and `observations` tables — packets seen by multiple observers are stored once with linked observation records
- **Observation count badges**: Packets page shows 👁 badge indicating how many observers saw each transmission
- **`?expand=observations`**: API query param to include full observation details on packet responses
- **`totalTransmissions` / `totalObservations`**: Health and analytics APIs return both deduped and raw counts
- **Migration script**: `scripts/migrate-dedup.js` for converting existing packet data to normalized schema
- **Live map deeplinks**: Node detail panel links to full node detail, observer detail, and filtered packets
- **CI validation**: `setup-node` added to deploy workflow for JS syntax checking

### Changed
- In-memory packet store restructured around transmissions (primary) with observation indexes
- Packets API returns unique transmissions by default (was returning inflated observation rows)
- Home page shows "Transmissions" instead of "Packets" for network stats
- Analytics overview uses transmission counts for throughput metrics
- Node health stats include `totalTransmissions` alongside legacy `totalPackets`
- WebSocket broadcasts include `observation_count`

### Fixed
- Packet expand showing only the collapsed row instead of individual observations
- Live page "Heard By" showing "undefined pkts" (wrong field name)
- Recent packets deeplink using query param instead of route path
- Migration script handling concurrent dual-write during live deployment

### Performance
- **8.19× dedup ratio on production** (117K observations → 14K transmissions)
- RAM usage reduced proportionally — store loads transmissions, not inflated observations
