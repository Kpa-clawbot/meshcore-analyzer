# AGENTS.md — MeshCore Analyzer

Guide for AI agents working on this codebase. Read this before writing any code.

## Architecture

Single Node.js server + static frontend. No build step. No framework. No bundler.

```
server.js          — Express API + MQTT ingestion + WebSocket broadcast
decoder.js         — MeshCore packet parser (header, path, payload, adverts)
packet-store.js    — In-memory packet store + query engine (backed by SQLite)
db.js              — SQLite schema + prepared statements
public/            — Frontend (vanilla JS, one file per page)
  app.js           — SPA router, shared globals, theme loading
  roles.js         — ROLE_COLORS, TYPE_COLORS, health thresholds, shared helpers
  nodes.js         — Nodes list + side pane + full detail page
  map.js           — Leaflet map with markers, legend, filters
  packets.js       — Packets table + detail pane + hex breakdown
  packet-filter.js — Wireshark-style filter engine (standalone, testable)
  customize.js     — Theme customizer panel (self-contained IIFE)
  analytics.js     — Analytics tabs (RF, topology, hash issues, etc.)
  channels.js      — Channel message viewer
  live.js          — Live packet feed + VCR mode
  home.js          — Home/onboarding page
  hop-resolver.js  — Client-side hop prefix → node name resolution
  style.css        — Main styles, CSS variables for theming
  live.css         — Live page styles
  home.css         — Home page styles
  index.html       — SPA shell, script/style tags with cache busters
```

### Data Flow
1. MQTT brokers → server.js ingests packets → decoder.js parses → packet-store.js stores in memory + SQLite
2. WebSocket broadcasts new packets to connected browsers
3. Frontend fetches via REST API, filters/sorts client-side

## Rules — Read These First

### 1. No commit without tests
Every change that touches logic MUST have unit tests. Run `node test-packet-filter.js && node test-aging.js` before pushing. If you add new logic, add tests to the appropriate test file or create a new one. No exceptions.

### 2. No commit without browser validation
After pushing, verify the change works in an actual browser. Use `browser profile=openclaw` against the running instance. Take a screenshot if the change is visual. If you can't validate it, say so — don't claim it works.

### 3. Cache busters — ALWAYS bump them
Every time you change a `.js` or `.css` file in `public/`, bump the cache buster in `index.html`. This has caused 7 separate production regressions. Use:
```bash
NEWV=$(date +%s) && sed -i "s/v=[0-9]*/v=$NEWV/g" public/index.html
```
Do this in the SAME commit as the code change, not as a follow-up.

### 4. Verify API response shape before building UI
Before writing client code that consumes an API endpoint, check what the endpoint ACTUALLY returns. Use `curl` or check the server code. Don't assume fields exist — grouped packets (`groupByHash=true`) have different fields than raw packets. This has caused multiple breakages.

### 5. Plan before implementing
Present a plan with milestones to the human. Wait for sign-off before starting. The plan must include:
- What changes in each milestone
- What tests will be written
- What browser validation will be done
- What config/customizer implications exist (see rule 8)

Do NOT start coding until the human says "go" or "start" or equivalent.

### 6. One commit per logical change
Don't push half-finished work. Don't push "let me try this" experiments. Get it right locally, test it, THEN push ONE commit. The QR overlay took 6 commits because each one was pushed without looking at the result. That's 6x the review burden for one visual change.

### 7. Understand before fixing
When something doesn't work as expected, INVESTIGATE before "fixing." Read the firmware source. Check the actual data. Understand WHY before changing code. The hash_size saga (21 commits) happened because we guessed at behavior instead of reading the MeshCore source.

### 8. Config values belong in the customizer eventually
If a feature introduces configurable values (thresholds, timeouts, display limits), note in the plan that these should be exposed in the customizer in a later milestone. It's OK to hardcode initially, but don't forget — track it in the plan.

### 9. Explicit git add only
Never use `git add -A` or `git add .`. Always list files explicitly: `git add file1.js file2.js`. Review with `git diff --cached --stat` before committing.

### 10. Don't regress performance
The packets page loads 30K+ packets. Don't add per-packet API calls. Don't add O(n²) loops. Client-side filtering is preferred over server-side. If you need data from the server, fetch it once and cache it.

## MeshCore Firmware — Source of Truth

The MeshCore firmware source is cloned at `firmware/` (gitignored — not part of this repo). This is THE authoritative reference for anything related to the protocol, packet format, device behavior, advert structure, flags, hash sizes, route types, or how repeaters/companions/rooms/sensors behave.

**Before implementing any feature that touches protocol behavior:**
1. Check the firmware source in `firmware/src/` and `firmware/docs/`
2. Key files: `Mesh.h` (constants, packet structure), `Packet.cpp` (encoding/decoding), `helpers/AdvertDataHelpers.h` (advert flags/types), `helpers/CommonCLI.cpp` (CLI commands), `docs/packet_format.md`, `docs/payloads.md`
3. If `firmware/` doesn't exist, clone it: `git clone --depth 1 https://github.com/meshcore-dev/MeshCore.git firmware`
4. To update: `cd firmware && git pull`

**Do NOT guess at protocol behavior.** The hash_size saga (21 commits) and the advert flags bug (room servers misclassified as repeaters) both happened because we assumed instead of reading the firmware source. The firmware is C++ — read it.

## MeshCore Protocol

**Do not memorize or hardcode protocol details from this file.** Read the firmware source.

- Packet format: `firmware/docs/packet_format.md`
- Payload types & structures: `firmware/docs/payloads.md`
- Advert flags & types: `firmware/src/helpers/AdvertDataHelpers.h`
- Route types & constants: `firmware/src/Mesh.h`
- CLI commands & behavior: `firmware/docs/cli_commands.md`
- FAQ (advert intervals, etc.): `firmware/docs/faq.md`

If you need to know how something works — a flag, a field, a timing, a behavior — **open the file and read it.** Don't rely on comments in our code, don't rely on what someone told you, don't guess. The firmware C++ source is the only thing that matters.

## Frontend Conventions

### Theming
All colors MUST use CSS variables. Never hardcode `#hex` values outside of `:root` definitions. The customizer controls colors via `THEME_CSS_MAP` in customize.js. If you add a new color, add it as a CSS variable and map it in the customizer.

### Shared Helpers (roles.js)
- `getNodeStatus(role, lastSeenMs)` → 'active' | 'stale'
- `getHealthThresholds(role)` → `{ staleMs, degradedMs, silentMs }`
- `ROLE_COLORS`, `ROLE_STYLE`, `TYPE_COLORS` — global color maps

### Shared Helpers (nodes.js)
- `getStatusInfo(n)` → `{ status, statusLabel, explanation, roleColor, ... }`
- `renderNodeBadges(n, roleColor)` → HTML string
- `renderStatusExplanation(n)` → HTML string

### last_heard vs last_seen
- `last_seen` = DB timestamp, only updates on adverts/direct upserts
- `last_heard` = from in-memory packet store, updates on ALL traffic
- Always prefer `n.last_heard || n.last_seen` for display and status calculation

### Packet Filter (packet-filter.js)
Standalone module. No dependencies on app globals (copies what it needs). Testable in Node.js:
```bash
node test-packet-filter.js
```
Uses firmware-standard type names (GRP_TXT, TXT_MSG, REQ) with aliases for convenience.

## Testing

### Unit Tests
```bash
node test-packet-filter.js        # 62 tests — filter engine
node test-aging.js                # 29 tests — node aging system
node test-regional-filter.js      # 22 tests — regional observer filtering
node test-regional-integration.js # integration — hits live API (non-deterministic)
node tools/e2e-test.js            # 44 tests — E2E: spins up temp server, injects packets, validates all APIs
node tools/frontend-test.js       # 66 tests — frontend smoke: HTML, JS refs, API shapes
```

**ALL existing tests must pass before pushing.** Run:
```bash
npm test                    # all tests + coverage summary
npm run test:unit           # fast: unit tests only (no server needed)
npm run test:coverage       # all tests + HTML coverage report in coverage/
```
If any test fails, fix it before pushing. No exceptions. No "known failures."

**Coverage baseline:** 37% statements, 42% branches, 54% functions. Coverage should only go up.

**Every new feature must add tests.** If you add logic, add tests. If you fix a bug, add a regression test. Test count should only go up, never down.

Tests that hit live data can use `https://analyzer.00id.net` — all API endpoints are public, no auth required.

### Browser Validation
After pushing, verify in the browser:
1. Does the page load without errors? (Check console)
2. Does the new feature work as intended?
3. Did you break anything else? (Quick check of adjacent features)
4. Does it work in both light and dark mode?

### What Needs Tests
- Parsers and decoders (packet-filter, decoder)
- Threshold/status calculations (aging, health)
- Data transformations (hash size computation, field resolvers)
- Anything with edge cases (null handling, boundary values)

## Common Pitfalls

| Pitfall | Times it happened | Prevention |
|---------|-------------------|------------|
| Forgot cache busters | 7 | Always bump in same commit |
| Grouped packets missing fields | 3 | curl the actual API first |
| last_seen vs last_heard mismatch | 4 | Always use `last_heard \|\| last_seen` |
| CSS selectors don't match SVG | 2 | Manipulate SVG in JS after generation |
| Feature built on wrong assumption | 5+ | Read source/data before coding |
| Pushed without testing | 5+ | Run tests + browser check every time |

## File Naming
- Tests: `test-{feature}.js` in repo root
- No build step, no transpilation — write ES2020 for server, ES5/6 for frontend (broad browser support)

## What NOT to Do
- **Don't check in private information** — no names, API keys, tokens, passwords, IP addresses, personal data, or any identifying information. This is a PUBLIC repo.
- Don't add npm dependencies without asking
- Don't create a build step
- Don't add framework abstractions (React, Vue, etc.)
- Don't hardcode colors — use CSS variables
- Don't make per-packet server API calls from the frontend
- Don't push without running tests
- Don't start implementing without plan approval
