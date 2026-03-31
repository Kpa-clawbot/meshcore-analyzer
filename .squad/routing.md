# Work Routing

How to decide who handles what.

## Routing Table

| Work Type | Route To | Examples |
|-----------|----------|----------|
| Architecture, scope, decisions | Kobayashi | Feature planning, trade-offs, scope decisions |
| Code review, PR review | Kobayashi | Review PRs, check quality, approve/reject |
| server.js, API routes, Express | Hicks | Add endpoints, fix API bugs, MQTT config |
| decoder.js, packet parsing | Hicks | Protocol changes, parser bugs, new packet types |
| packet-store.js, db.js, SQLite | Hicks | Storage bugs, query optimization, schema changes |
| server-helpers.js, MQTT, WebSocket | Hicks | Helper functions, real-time data flow |
| Performance optimization | Hicks | Caching, O(n) improvements, response times |
| Docker, deployment, manage.sh | Hicks | Container config, deploy scripts |
| MeshCore protocol/firmware | Hicks | Read firmware source, verify protocol behavior |
| public/*.js (all frontend modules) | Newt | UI features, interactions, SPA routing |
| Leaflet maps, live visualization | Newt | Map markers, VCR playback, animations |
| CSS, theming, customize.js | Newt | Styles, CSS variables, theme customizer |
| packet-filter.js (filter engine) | Newt | Filter syntax, parser, Wireshark-style queries |
| index.html, cache busters | Newt | Script tags, version bumps |
| Unit tests, test-*.js | Bishop | Write/fix tests, coverage improvements |
| Playwright E2E tests | Bishop | Browser tests, UI verification |
| Coverage, CI pipeline | Bishop | Coverage targets, CI config |
| CI/CD pipeline, .github/workflows | Hudson | Pipeline config, step optimization, CI debugging |
| Docker, Dockerfile, docker/ | Hudson | Container config, build optimization |
| manage.sh, deployment scripts | Hudson | Deploy scripts, server management |
| scripts/, coverage tooling | Hudson | Build scripts, coverage collector optimization |
| Azure, VM, infrastructure | Hudson | az CLI, SSH, server provisioning, monitoring |
| Production debugging, DB ops | Hudson | SQLite recovery, WAL issues, process diagnostics |
| User questions, "why does X..." | Ripley | Community support, UI behavior explanations |
| Bug report triage from users | Ripley | Analyze reports, reproduce, route to dev |
| GitHub issue comments (support) | Ripley | Explain behavior, suggest workarounds |
| README, docs/ | Kobayashi | Documentation updates |
| Session logging | Scribe | Automatic — never needs routing |

## Issue Routing

| Label | Action | Who |
|-------|--------|-----|
| `squad` | Triage: analyze issue, assign `squad:{member}` label | Lead |
| `squad:{name}` | Pick up issue and complete the work | Named member |

### How Issue Assignment Works

1. When a GitHub issue gets the `squad` label, the **Lead** triages it — analyzing content, assigning the right `squad:{member}` label, and commenting with triage notes.
2. When a `squad:{member}` label is applied, that member picks up the issue in their next session.
3. Members can reassign by removing their label and adding another member's label.
4. The `squad` label is the "inbox" — untriaged issues waiting for Lead review.

## Rules

1. **Eager by default** — spawn all agents who could usefully start work, including anticipatory downstream work.
2. **Scribe always runs** after substantial work, always as `mode: "background"`. Never blocks.
3. **Quick facts → coordinator answers directly.** Don't spawn an agent for "what port does the server run on?"
4. **When two agents could handle it**, pick the one whose domain is the primary concern.
5. **"Team, ..." → fan-out.** Spawn all relevant agents in parallel as `mode: "background"`.
6. **Anticipate downstream work.** If a feature is being built, spawn the tester to write test cases from requirements simultaneously.
7. **Issue-labeled work** — when a `squad:{member}` label is applied to an issue, route to that member. The Lead handles all `squad` (base label) triage.
