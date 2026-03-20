# Changelog

## v2.0.0 (2026-03-20)

85+ commits — analytics, mobile redesign, accessibility, 100+ bug fixes.

### ✨ New Features
- Per-node analytics page (6 charts, stat cards, peer table, time range selector)
- Global analytics — Nodes tab (network status, role breakdown, claimed nodes, leaderboards)
- Live map VCR playback — rewind/replay/scrub 24h at up to 4× speed, retro LCD clock
- Richer node detail — status badge, avg SNR/hops, observer table, QR codes, recent packets
- Claimed (My Mesh) nodes — star your nodes, always sorted to top, auto-sync favorites
- Packets "My Nodes" toggle — filter to only your mesh traffic
- Bulk health API (`GET /api/nodes/bulk-health`)
- Network status API (`GET /api/nodes/network-status`)
- Live theme toggle — dark/light tiles swap instantly via MutationObserver

### 📱 Mobile
- Two-row VCR bar layout (controls+LCD / full-width timeline)
- iOS safe area support (home indicator clearance)
- Feed/legend hidden on mobile — just map + VCR + LCD
- JS-driven viewport height for reliable orientation changes
- Touch-friendly targets, horizontal scroll on tables

### ♿ Accessibility
- ARIA tab patterns, focus management, keyboard navigation
- Distinct SVG marker shapes per node role
- Color-blind safe palettes, screen reader support

### 🐛 Bug Fixes (100+)
- Excel-like column resize — steal proportionally from all right columns
- Panel drag live reflow
- VCR scrub pagination, replay buffer management
- Express route ordering (named before parameterized)
- XSS escaping, WebSocket cleanup, memory leaks
- Dark mode consistency, empty states, SRI hashes
- Stray CSS fragment corrupting live.css
- Geographic prefix disambiguation restored

## v1.0.0 (2026-03-19)

Initial release.
