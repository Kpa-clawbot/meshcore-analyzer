# Changelog

## v2.0.0 (2026-03-20)

Major release — 83 commits covering accessibility, mobile responsive redesign, live page overhaul, node analytics, and 100+ bug fixes.

### ✨ New Features

- **Per-Node Analytics page** — 6 charts (activity timeline, packet types, SNR distribution, hop count, peer network, hourly heatmap), stat cards, peer table, time range selector
- **Global Analytics — Nodes tab** — network status overview, role breakdown pie chart, claimed nodes table, leaderboards (activity, signal, observers, recent)
- **Richer Node Detail** — status badge, avg SNR/hops, packets today/total, "Heard By" observer table, QR code in sidebar + full-screen view
- **Claimed (My Mesh) nodes** — always sort to top of nodes list, visual distinction (blue tint, accent border, ★ badge), auto-sync claimed→favorites
- **Packets "My Nodes" toggle** — ★ button filters to only claimed/favorited node packets
- **Live map theme toggle** — dark/light CartoDB tiles swap instantly via MutationObserver (no refresh needed)
- **Bulk health API** — `GET /api/nodes/bulk-health?limit=N` replaces 50 individual health requests
- **Network status API** — `GET /api/nodes/network-status` computes status server-side across ALL nodes
- **VCR replay pagination** — fetches next 10k packets when buffer exhausted instead of jumping to live
- **Multi-slot save system** — unlimited named slots, export/import with SHA-256 checksum

### 🗺️ Map & Visualization

- **Accessible map markers** — distinct SVG shapes per role (diamond/circle/square/triangle) + high-contrast colors
- **Geographic prefix disambiguation** restored for route overlay
- **Hash matrix improvements** — bigger font, progressive color scheme, free cells show hex prefix, collision risk sorted closest-first
- **Scatter plot** color-blind accessible

### 📱 Mobile Responsive

- **Live page mobile redesign** — feed + legend hidden on mobile, LCD clock preserved
- **Mobile VCR bar** — proper two-row layout (controls+scope+LCD / full-width timeline), no horizontal scrolling
- **Rotation fix** — JS-driven height via `window.innerHeight` + `visualViewport` resize listener with staggered invalidation
- **`100dvh` fallback** on `#app` and `.live-page` for proper viewport height
- **Packets page** — horizontal scroll on tables, filter bar wrapping, touch-friendly targets
- **Analytics** — single-column grid on mobile, reduced padding
- **Nodes** — count pills wrap, compact layout
- **Feed detail card** — bottom sheet on mobile with slide-up animation

### ♿ Accessibility (WCAG)

- ARIA tab pattern, form labels, focus management
- SVG alt text, color-blind safe palettes
- Keyboard-accessible table rows, feed items, sender list
- Node panel focus trap, combobox ARIA on filters
- `aria-live` regions on data tables and feeds
- Screen-reader-only text for icon-only buttons
- VCR timeline + LCD ARIA labels

### 🐛 Bug Fixes

- Fixed 100+ issues across all pages (see closed GitHub issues #1–#101)
- Excel-like column resize — drag steals proportionally from ALL right columns, min 50px
- Panel drag live reflow — left panel explicitly sized during drag
- VCR scrub fetches ASC from scrub point (prevents jumping forward)
- Removed dead code: `svgLine()`, `.vcr-clock`, duplicate `escapeHtml`/`debounce`
- XSS fix: escape decoded text/name in innerHTML
- WebSocket debounce helper, cleaned up window globals
- Race conditions in analytics async loading
- Express route ordering: named routes before `:pubkey` wildcards
- Stray CSS fragment removed that was corrupting live.css
- Dark mode: section-row background uses CSS variable
- SRI integrity hashes on Leaflet CDN scripts
- Empty/error states on all data tables

### 🏗️ Infrastructure

- Cache busters on all JS/CSS files
- Feed resize handle (drag to resize feed panel width)
- Nav auto-hide on live page with pin button
- Legend toggle button for mobile
- `totalPackets` added to health API

---

## v1.0.0 (2026-03-19)

Initial release.
