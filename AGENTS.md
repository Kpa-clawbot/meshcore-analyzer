# AGENTS.md — CoreScope

Guide for AI agents working on this codebase. Read this before writing any code.

## Architecture

Go backend + static frontend. No build step. No framework. No bundler.

**⚠️ The Node.js server (server.js) is DEPRECATED and has been removed. All backend code is in Go.**
**⚠️ DO NOT create or modify any Node.js server files. All backend changes go in `cmd/server/` or `cmd/ingestor/`.**

```
cmd/server/        — Go API server (REST + WebSocket broadcast + static file serving)
  main.go          — Entry point, flags, SPA handler
  routes.go        — All /api/* endpoints
  store.go         — In-memory packet store + analytics + SQLite queries
  config.go        — Configuration loading
  decoder.go       — MeshCore packet decoder
cmd/ingestor/      — Go MQTT ingestor (separate binary, writes to shared SQLite DB)
public/            — Frontend (vanilla JS, one file per page) — ACTIVE, NOT DEPRECATED
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
test-fixtures/     — Real data SQLite fixture from staging (used for E2E tests)
scripts/           — Tooling (coverage collector, fixture capture, frontend instrumentation)
```

### Data Flow
1. MQTT brokers → Go ingestor (`cmd/ingestor/`) ingests packets → decodes → writes to SQLite
2. Go server (`cmd/server/`) polls SQLite for new packets, broadcasts via WebSocket
3. Frontend fetches via REST API (`/api/*`), filters/sorts client-side

### What's Deprecated (DO NOT TOUCH)
The following were part of the old Node.js backend and have been removed:
- `server.js`, `db.js`, `decoder.js`, `server-helpers.js`, `packet-store.js`, `iata-coords.js`
- All `test-server-*.js`, `test-decoder*.js`, `test-db*.js`, `test-regional*.js` files
- If you see references to these in comments or docs, they're stale — ignore them

## Rules — Read These First

### 1. No commit without tests
Every change that touches logic MUST have tests. For Go backend: `cd cmd/server && go test ./...` and `cd cmd/ingestor && go test ./...`. For frontend: `node test-packet-filter.js && node test-aging.js && node test-frontend-helpers.js`. If you add new logic, add tests. No exceptions.

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

### 11. PR descriptions must be clean markdown
When opening a pull request, the description must be **valid, readable markdown**. Use real newlines (not `\n` literals), proper code fences, and correct heading syntax. Write it using `--body-file -` (piped from a heredoc or file), never inline `--body` with escaped characters. If the description renders as garbage, fix it before requesting review. This is the first thing reviewers see.

### 12. Post a follow-up comment when review feedback is addressed
When you push fixes for review comments, post a comment on the PR listing what was changed and the commit hash. Reviewers should not have to dig through commits to find what was fixed. Format: "Review feedback addressed (commit `abc1234`)" followed by a numbered list of what was done.

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

### Test Pipeline
```bash
npm test                    # all backend tests + coverage summary
npm run test:unit           # fast: unit tests only (no server needed)
npm run test:coverage       # all tests + HTML coverage report
npm run test:full-coverage  # backend + instrumented frontend coverage via Playwright
```

### Test Files
```bash
# Backend (deterministic, run before every push)
node test-packet-filter.js        # filter engine
node test-aging.js                # node aging system
node test-regional-filter.js      # regional observer filtering
node test-decoder.js              # packet decoder
node test-decoder-spec.js         # spec-driven + golden fixture tests
node test-server-helpers.js       # extracted server functions
node test-server-routes.js        # API route tests via supertest
node test-packet-store.js         # in-memory packet store
node test-db.js                   # SQLite operations
node test-frontend-helpers.js     # frontend logic (via vm.createContext)
node tools/e2e-test.js            # E2E: temp server + synthetic packets
node tools/frontend-test.js       # frontend smoke: HTML, JS refs, API shapes

# Frontend E2E (requires running server or Playwright)
node test-e2e-playwright.js       # 8 Playwright browser tests (default: localhost:3000)
```

### Rules
**ALL existing tests must pass before pushing.** No exceptions. No "known failures."

**Every new feature must add tests.** Unit tests for logic, Playwright tests for UI changes. Test count only goes up.

**Coverage targets:** Backend 85%+, Frontend 42%+ (both should only go up). CI reports both and updates badges automatically.

### When writing a new feature
1. Write the feature code
2. Write unit tests for the logic
3. Write/update Playwright tests if it's a UI change
4. Run `npm test` — all tests must pass
5. Run `node test-e2e-playwright.js` against a local server — E2E must pass
6. THEN push to master

### Testing infrastructure
- **Backend coverage**: c8 tracks server-side code in-process
- **Frontend coverage**: Istanbul instruments `public/*.js` → Playwright exercises them → `window.__coverage__` extracted → nyc reports. Instrumented files are generated fresh each CI run, never checked in.
- **CI pipeline**: backend tests + coverage → instrument frontend → start local server → Playwright E2E + coverage collection → badges update → deploy (only if all pass)
- **Playwright tests default to localhost:3000** — NEVER run against prod. CI sets `BASE_URL=http://localhost:13581`. Running locally: start your server, then `node test-e2e-playwright.js`
- **ARM machines**: Basic Playwright tests work with system chromium (`CHROMIUM_PATH=/usr/bin/chromium-browser`). Heavy coverage collection scripts may crash — use CI for those.

Tests that need live mesh data can use `https://analyzer.00id.net` — all API endpoints are public, no auth required.

### What Needs Tests
- Parsers and decoders (packet-filter, decoder)
- Threshold/status calculations (aging, health)
- Data transformations (hash size computation, field resolvers)
- Anything with edge cases (null handling, boundary values)
- UI interactions that exercise frontend code branches

## Engineering Principles

These aren't optional. Every change must follow these principles.

### DRY — Don't Repeat Yourself
If the same logic exists in two places, it MUST be extracted into a shared function. We had **5 separate implementations** of hash prefix disambiguation across the codebase — that's a maintenance nightmare and a bug factory. One implementation, imported everywhere.

**Before writing new code, search the codebase for existing implementations.** `grep -rn 'functionName\|pattern' public/ server.js` takes 2 seconds and prevents duplication.

### SOLID Principles
- **Single Responsibility**: Each function does ONE thing. A 200-line function that fetches, transforms, renders, and caches is wrong. Split it.
- **Open/Closed**: Add behavior by extending, not modifying. Use callbacks, options objects, or configuration — not `if (caller === 'live')` branches inside shared code.
- **Dependency Injection**: Functions should accept their dependencies as parameters, not reach into globals. `resolveHops(hops, nodeList)` — not `resolveHops(hops)` where it secretly reads `window.allNodes`. This makes functions testable in isolation.
- **Interface Segregation**: Don't force callers to depend on things they don't need. If a function returns 20 fields but the caller uses 3, consider a simpler return shape or let the caller pick.

### Code Reuse
- **Shared helpers go in shared files.** Frontend: `roles.js`, `hop-resolver.js`. Backend: `server-helpers.js`, `decoder.js`.
- **Don't copy-paste between files.** If `live.js` needs the same algorithm as `packets.js`, import it from a shared module. If the shared module doesn't exist yet, create one.
- **Parameterize, don't duplicate.** If two callers need slightly different behavior, add a parameter — don't fork the function.

### Testability
- **Write functions that are easy to test.** Pure functions (input → output, no side effects) are ideal. If a function reads from the DOM, the DB, and localStorage, it's untestable without mocking everything.
- **Dependency injection enables testing.** Pass the node list, the map reference, the API function as parameters. Tests can substitute fakes.
- **Test the real code, not copies.** Don't paste a function into a test file and test the copy. Import/require the actual module. If the module isn't importable (IIFE, browser-only), refactor it so it is — or use `vm.createContext` like `test-frontend-helpers.js` does.
- **Every bug fix gets a regression test.** If it broke once, it'll break again. The test proves it stays fixed.

### Type Safety (without TypeScript)
- **Cast at the boundary.** Data from the DB, API, or localStorage may be strings when you expect numbers. Cast early: `Number(val)`, `parseInt(val)`, `String(val)`. Don't let type mismatches propagate deep into logic where they cause cryptic `.toFixed is not a function` errors.
- **Null-check before method calls.** `val != null ? Number(val).toFixed(1) : '—'` — not `val.toFixed(1)`.

### Performance Awareness
- **No per-item API calls.** Fetch bulk data once, filter/transform client-side.
- **No O(n²) in hot paths.** The packets page has 30K+ rows. A nested loop over all packets × all nodes = 20 billion operations. Use Maps/Sets for lookups.
- **Cache expensive computations.** If you compute the same thing on every render, cache it and invalidate on data change.

## XP (Extreme Programming) Practices

### Test-First Development
Write the test BEFORE the code. Not after. Not "I'll add tests later." The test defines the expected behavior, then you write the minimum code to make it pass.

**Flow:** Red (write failing test) → Green (make it pass) → Refactor (clean up).

This prevents shipping bugs like `.toFixed on a string` — if the test existed first with string inputs, the bug could never have been introduced. Every bug fix starts by writing a test that reproduces the bug, THEN fixing it.

### YAGNI — You Aren't Gonna Need It
Don't build for hypothetical future requirements. Build the simplest thing that solves the current problem. The 5 separate disambiguation implementations happened because each page rolled its own "just in case" version instead of importing the one that already existed.

If you're writing code that handles a case nobody asked for: stop. Delete it. Add it when there's a real need.

### Refactor Mercilessly
When you touch a file and see duplication, dead code, unclear names, or structural mess — clean it up in the same commit. Don't leave it for "later." Later never comes. Tech debt compounds.

**The Boy Scout Rule:** Leave every file cleaner than you found it.

### Simple Design
The simplest solution that works is the correct one. Complexity is a bug. Before building something, ask:
1. Does this already exist somewhere in the codebase?
2. Can I solve this with an existing function + a parameter?
3. Am I over-engineering for a case that doesn't exist yet?

If the answer to any of these is yes, simplify.

### Pair Programming (Human + AI Model)
For this project, pair programming means: **subagent writes the code → parent agent reviews and tests locally → THEN pushes to master.** The subagent is the "driver," the parent is the "navigator."

**What this means in practice:**
- Subagent output is NEVER pushed directly without review
- Parent agent runs the tests, checks the diff, verifies the behavior
- If the subagent's work is wrong, parent fixes it before pushing — not after
- "The subagent said it works" is not verification. Running the tests is.

### Continuous Integration as a Gate
CI must pass before code is considered shipped. But CI is the LAST line of defense, not the first. The process is:
1. Test locally (unit + E2E)
2. Review the diff
3. Push
4. CI confirms

If CI catches something you missed locally, that's a process failure — figure out why your local testing didn't catch it and fix the gap.

### 10-Minute Build
Everything must be testable locally in under 10 minutes. If local tests are broken, flaky, or crashing — that's a P0 blocker. Fix the test infrastructure before shipping features. Broken tests = no tests = shipping blind.

### Collective Code Ownership
No file is "someone else's problem." Every file follows the same patterns, uses the same shared modules, meets the same quality bar. `live.js` doesn't get to be a special snowflake with its own reimplementation of everything. If it drifts from the shared patterns, bring it back in line.

### Small Releases
One logical change per commit. Each commit is deployable. Each commit has its tests. Don't bundle "fix A + feature B + cleanup C" into one push — if B breaks, you can't revert without losing A and C.

## Common Pitfalls

| Pitfall | Times it happened | Prevention |
|---------|-------------------|------------|
| Forgot cache busters | 7 | Always bump in same commit |
| Grouped packets missing fields | 3 | curl the actual API first |
| last_seen vs last_heard mismatch | 4 | Always use `last_heard \|\| last_seen` |
| CSS selectors don't match SVG | 2 | Manipulate SVG in JS after generation |
| Feature built on wrong assumption | 5+ | Read source/data before coding |
| Pushed without testing | 5+ | Run tests + browser check every time |
| Tests defaulting to prod | 2 | Always default to localhost, never prod |
| Gave up testing locally | 2 | Basic tests work on ARM — only heavy coverage scripts crash |
| Copy-pasted functions for "coverage" | 1 | Test the real code, not copies in a helper file |
| Subagent timed out mid-work | 4 | Give clear scope, don't try to run slow pipelines locally |

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
