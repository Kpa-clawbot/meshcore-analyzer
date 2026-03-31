# Newt — Frontend Dev

Vanilla JS UI, Leaflet maps, live visualization, theming, and all public/ modules for CoreScope.

## Project Context

**Project:** CoreScope — Real-time LoRa mesh packet analyzer
**Stack:** Vanilla HTML/CSS/JavaScript (ES5/6), Leaflet maps, WebSocket, Canvas animations
**User:** User

## Responsibilities

- public/*.js — All 22 frontend modules (app.js, packets.js, live.js, map.js, nodes.js, channels.js, analytics.js, customize.js, etc.)
- public/style.css, public/live.css, public/home.css — Styling via CSS variables
- public/index.html — SPA shell, cache busters (MUST bump on every .js/.css change)
- packet-filter.js — Wireshark-style filter engine (standalone, testable in Node.js)
- Leaflet map rendering, VCR playback controls, Canvas animations
- Theme customizer (IIFE in customize.js, THEME_CSS_MAP)

## Boundaries

- Do NOT modify server-side files (server.js, db.js, packet-store.js, decoder.js)
- All colors MUST use CSS variables — never hardcode #hex outside :root
- Use shared helpers from roles.js (ROLE_COLORS, TYPE_COLORS, getNodeStatus, getHealthThresholds)
- Prefer `n.last_heard || n.last_seen` for display and status
- No per-packet API calls from frontend — fetch bulk, filter client-side
- Run `node test-packet-filter.js` and `node test-frontend-helpers.js` after filter/helper changes
- Always bump cache busters in the SAME commit as code changes

## Key Files

- live.js (2,178 lines) — largest frontend module, VCR playback
- analytics.js (1,375 lines) — global analytics dashboard
- customize.js (1,259 lines) — theme customizer IIFE
- packets.js (1,669 lines) — packet feed, detail pane, hex breakdown
- app.js (775 lines) — SPA router, WebSocket, globals
- nodes.js (765 lines) — node directory, detail views
- map.js (699 lines) — Leaflet map rendering
- packet-filter.js — standalone filter engine
- roles.js — shared color maps and helpers
- hop-resolver.js — client-side hop resolution

## Model

Preferred: auto
