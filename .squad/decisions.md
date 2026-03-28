# Squad Decisions

## Active Decisions

### 2026-03-28T05:30:00Z: User directive — soft-delete nodes
**By:** User (via Copilot)
**What:** Don't delete stale nodes from DB — mark them as inactive instead. Add an `active` boolean column (or use last_seen threshold). All node queries (API, stats, analytics) should exclude inactive nodes. Historical data preserved but not shown on the site.
**Why:** User request — keep historical data, just don't pollute the UI with stale nodes.

### 2026-03-27T22:00:00Z: Proto fixture capture in CI
**By:** Copilot (User directive)
**What:** Proto fixture capture in CI should run against prod (the stable reference), not staging. Staging may have broken code. Prod is known-good.
**Why:** Ensures fixture definitions are derived from a stable, known-good API contract. Prevents broken staging deployments from breaking proto contracts.

### 2026-03-27T20:56:00Z: Architecture decision — Protobuf API contract
**By:** Copilot (Architecture decision)
**What:** All frontend/backend interfaces get protobuf definitions as the single source of truth. Go generates structs with JSON tags from protos. Node stays unchanged — protos are derived FROM Node's current JSON shapes. Proto definitions MUST use inheritance and composition (no repeating field definitions). Data flow: SQLite → proto struct → JSON. JSON blobs from DB deserialize against proto structs for validation.
**Why:** Eliminates the endless parity bugs between Node and Go. Compiler-enforced contract instead of agent-verified field matching. DRY — shared message types composed, not duplicated.

### 2026-03-27T02:18:00Z: Infrastructure connection details
**By:** Squad Coordinator (capturing session discoveries)
**What:** Production VM connection details established this session:
- **VM Name:** meshcore-vm
- **Resource Group:** MESHCORE-WEST-RG
- **Region:** westus2
- **Size:** Standard_D2as_v5 (Linux)
- **Public IP:** (see VM_HOST env var)
- **SSH User:** deploy
- **SSH Command:** `ssh deploy@$VM_HOST`
- **Azure CLI:** v2.84.0 (upgraded from 2.11.1 this session — stale .pyc files cleared)
- **CI Runner:** self-hosted on this same VM ("meshcore-vm")
- **App path:** TBD (Hudson investigating via SSH)
- **DB path:** TBD (Hudson investigating via SSH)
**Why:** Team needs a single reference for prod access. Hudson, Hicks, and any future agent doing prod debugging needs these details.

### 2026-03-27T00:06:00Z: User directive — auto-close issues with commit messages
**By:** User (via Copilot)
**What:** Always use "Fixes #N" or "Closes #N" in commit messages so GitHub auto-closes issues on push. Don't just reference issue numbers in description text.
**Why:** User request — captured for team memory. Previous commit listed issues but didn't trigger auto-close.

### 2026-03-26T19:10:00Z: User directive — test data isolation
**By:** User (via Copilot)
**What:** Seeded test data for E2E tests must be isolated — never pollute production or deployed containers. Use a separate test-only DB or inject via test harness. Seed before tests, tear down after. No seed scripts in Docker image.
**Why:** User request — captured for team memory.

### 2026-03-26T19:00:00Z: Shared clipboard helper in roles.js
**Author:** Newt  
**What:** Added `window.copyToClipboard(text, onSuccess, onFail)` to `roles.js` as the single clipboard implementation for all frontend modules.
**Rationale:** Three separate files had their own clipboard logic (nodes.js, packets.js, customize.js) — one had no fallback, one used `prompt()`, one had a proper fallback. DRY principle: one implementation, used everywhere. The helper tries `navigator.clipboard.writeText()` first, falls back to hidden textarea + `document.execCommand('copy')` for Firefox and older browsers.
**Impact:** Any future copy-to-clipboard needs should use `window.copyToClipboard()` instead of calling the Clipboard API directly.

## Governance

- All meaningful changes require team consensus
- Document architectural decisions here
- Keep history focused on work, decisions focused on direction
