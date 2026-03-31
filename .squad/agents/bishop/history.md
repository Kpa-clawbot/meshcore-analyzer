# Bishop — History

## Project Context

CoreScope has 14 test files, 4,290 lines of test code. Backend coverage 85%+, frontend 42%+. Tests use Node.js native runner, Playwright for E2E, c8/nyc for coverage, supertest for API routes. vm.createContext pattern used for testing frontend helpers in Node.js.

User: User

## Learnings

- Session started 2026-03-26. Team formed: Kobayashi (Lead), Hicks (Backend), Newt (Frontend), Bishop (Tester).
- E2E run 2026-03-26: 12/16 passed, 4 failed. Results:
  - ✅ Home page loads
  - ✅ Nodes page loads with data
  - ❌ Map page loads with markers — No markers found (empty DB, no geo data)
  - ✅ Packets page loads with filter
  - ✅ Node detail loads
  - ✅ Theme customizer opens
  - ✅ Dark mode toggle
  - ✅ Analytics page loads
  - ✅ Map heat checkbox persists in localStorage
  - ✅ Map heat checkbox is clickable
  - ✅ Live heat disabled when ghosts mode active
  - ✅ Live heat checkbox persists in localStorage
  - ✅ Heatmap opacity persists in localStorage
  - ❌ Live heatmap opacity persists — browser closed before test ran (bug: browser.close() on line 274 is before tests 14-16)
  - ❌ Customizer has separate map/live opacity sliders — same browser-closed bug
  - ❌ Map re-renders on resize — same browser-closed bug
- BUG FOUND: test-e2e-playwright.js line 274 calls `await browser.close()` before tests 14, 15, 16 execute. Those 3 tests will always fail. The `browser.close()` must be moved after all tests.
- The "Map page loads with markers" failure is expected with an empty local DB — no nodes with coordinates exist to render markers.
- FIX APPLIED 2026-03-26: Moved `browser.close()` from between test 13 and test 14 to after test 16 (just before the summary). Tests 14 ("Live heatmap opacity persists") and 15 ("Customizer has separate map/live opacity sliders") now pass. Test 16 ("Map re-renders on resize") now runs but fails due to empty DB (no markers to count) — same root cause as test 3. Result: 14/16 pass, 2 fail (both map-marker tests, expected with empty DB).
- TESTS ADDED 2026-03-26: Issue #127 (copyToClipboard) — 8 unit tests in test-frontend-helpers.js using vm.createContext + DOM/clipboard mocks. Tests cover: fallback path (execCommand success/fail/throw), clipboard API path, null/undefined input, textarea lifecycle, no-callback usage. Pattern: `makeClipboardSandbox(opts)` helper builds sandbox with configurable navigator.clipboard and document.execCommand mocks. Total frontend helper tests: 47→55.
- TESTS ADDED 2026-03-26: Issue #125 (packet detail dismiss) — 1 E2E test in test-e2e-playwright.js. Tests: click row → pane opens (empty class removed) → click ✕ → pane closes (empty class restored). Skips gracefully when DB has no packets. Inserted before analytics group, before browser.close().
- E2E SPEED OPTIMIZATION 2026-03-26: Rewrote test-e2e-playwright.js for performance per Kobayashi's audit. Changes:
  - Replaced ALL 19 `waitUntil: 'networkidle'` → `'domcontentloaded'` + targeted `waitForSelector`/`waitForFunction`. networkidle stalls ~500ms+ per navigation due to persistent WebSocket + Leaflet tiles.
  - Eliminated 11 of 12 `waitForTimeout` sleeps → event-driven waits (waitForSelector, waitForFunction). Only 1 remains: 500ms for packet filter debounce (was 1500ms).
  - Reordered tests into page groups to eliminate 7 redundant navigations (page.goto 14→7): Home(1,6,7), Nodes(2,5), Map(3,9,10,13,16), Packets(4), Analytics(8), Live(11,12), NoNav(14,15).
  - Reduced default timeout from 15s to 10s.
  - All 17 test names and assertions preserved unchanged.
  - Verified: 17/17 tests pass against local server with generated test data.
- COVERAGE PIPELINE TIMING (measured locally, Windows):
  - Phase 1: Istanbul instrumentation (22 JS files) — **3.7s**
  - Phase 2: Server startup (COVERAGE=1) — **~2s** (ready after pre-warm)
  - Phase 3: Playwright E2E (test-e2e-playwright.js, 17 tests) — **3.7s**
  - Phase 4: Coverage collector (collect-frontend-coverage.js) — **746s (12.4 min)** ← THE BOTTLENECK
  - Phase 5: nyc report generation — **1.8s**
  - TOTAL: ~757s (~12.6 min locally). CI reports ~13 min (matches).
  - ROOT CAUSE: collect-frontend-coverage.js is a 978-line script that launches a SECOND Playwright browser and exhaustively clicks every UI element on every page to maximize code coverage. It contains:
    - 169 explicit `waitForTimeout()` calls totaling 104.1s (1.74 min) of hard sleep
    - 21 `waitUntil: 'networkidle'` navigations (each adds ~2-15s depending on page load + WebSocket/tile activity)
    - Visits 12 pages: Home, Nodes, Packets, Map, Analytics, Customizer, Channels, Live, Traces, Observers, Perf, plus global router/theme exercises
    - Heaviest sections by sleep: Packets (13s), Analytics (13.8s), Nodes (11.6s), Live (11.7s), App.js router (10.4s)
    - The networkidle waits are the real killer — they stall ~500ms-15s EACH waiting for WebSocket + Leaflet tiles to settle
  - Note: test-e2e-interactions.js (called in combined-coverage.sh) does not exist — it fails silently via `|| true`
  - OPTIMIZATION OPPORTUNITIES: Replace networkidle→domcontentloaded (same fix as E2E tests), replace waitForTimeout with event-driven waits, reduce/batch page navigations, parallelize independent page exercises
- REGRESSION TESTS ADDED 2026-03-27: Memory optimization (observation deduplication). 8 new tests in test-packet-store.js under "=== Observation deduplication (transmission_id refs) ===" section. Tests verify: (1) observations don't duplicate raw_hex/decoded_json, (2) transmission fields accessible via store.byTxId.get(obs.transmission_id), (3) query() and all() still return transmission fields for backward compat, (4) multiple observations share one transmission_id, (5) getSiblings works after dedup, (6) queryGrouped returns transmission fields, (7) memory estimate reflects dedup savings. 4 tests fail pre-fix (expected — Hicks hasn't applied changes yet), 4 pass (backward compat). Pattern: use hasOwnProperty() to distinguish own vs inherited/absent fields.
- REVIEW 2026-03-27: Hicks RAM fix (observation dedup). REJECTED. Tests pass (42 packet-store + 204 route), but 5 server.js consumers access `.hash`, `.raw_hex`, `.decoded_json`, `.payload_type` on lean observations from `byObserver.get()` or `tx.observations` without enrichment. Broken endpoints: (1) `/api/nodes/bulk-health` line 1141 `o.hash` undefined, (2) `/api/nodes/network-status` line 1220 `o.hash` undefined, (3) `/api/analytics/signal` lines 1298+1306 `p.hash`/`p.raw_hex` undefined, (4) `/api/observers/:id/analytics` lines 2320+2329+2361 `p.payload_type`/`p.decoded_json` undefined + lean objects sent to client as recentPackets, (5) `/api/analytics/subpaths` line 2711 `o.hash` undefined. All are regional filtering or analytics code paths that use `byObserver` directly. Fix: either enrich at these call sites or store `hash` on observations (it's small). The enrichment pattern works for `getById()`, `getSiblings()`, and `/api/packets/:id` but was not applied to the 5 other consumers. Route tests pass because they don't assert on these specific field values in analytics responses.
- BATCH REVIEW 2026-03-27: Reviewed 6 issue fixes pushed without sign-off. Full suite: 971 tests, 0 failures across 11 test files. Cache busters uniform (v=1774625000). Verdicts:
  - #133 (phantom nodes): ✅ APPROVED. 12 assertions on removePhantomNodes, real db.js code, edge cases (idempotency, real node preserved, stats filtering).
  - #123 (channel hash): ⚠️ APPROVED WITH NOTES. 6 new decoder tests cover channelHashHex (zero-padding) and decryptionStatus (no_key ×3, decryption_failed). Missing: `decrypted` status untested (needs valid crypto key), frontend rendering of "Ch 0xXX (no key)" untested.
  - #126 (offline node on map): ✅ APPROVED. 3 regression tests: ambiguous prefix→null, unique prefix→resolves, dead node stays dead. Caching verified. Excellent quality.
  - #130 (disappearing nodes): ✅ APPROVED. 8 pruneStaleNodes tests cover dim/restore/remove for API vs WS nodes. Real live.js via vm.createContext.
  - #131 (auto-updating nodes): ⚠️ APPROVED WITH NOTES. 8 solid isAdvertMessage tests (real code). BUT 5 WS handler tests are source-string-match checks (`src.includes('loadNodes(true)')`) — these verify code exists but not that it works at runtime. No runtime test for debounce batching behavior.
  - #129 (observer comparison): ✅ APPROVED. 11 comprehensive tests for comparePacketSets — all edge cases, performance (10K hashes <500ms), mathematical invariant. Real compare.js via vm.createContext.
  - NOTES FOR IMPROVEMENT: (1) #131 debounce behavior should get a runtime test via vm.createContext, not string checks. (2) #123 could benefit from a `decrypted` status test if crypto mocking is feasible. Neither is blocking.
- TEST GAP FIX 2026-03-27: Closed both noted gaps from batch review:
  - #123 (channel hash decryption `decrypted` status): 3 new tests in test-decoder.js. Used require.cache mocking to swap ChannelCrypto module with mock that returns `{success:true, data:{...}}`. Tests cover: (1) decrypted status with sender+message (text formatted as "Sender: message"), (2) decrypted without sender (text is just message), (3) multiple keys tried, first match wins (verifies iteration order + call count). All verify channelHashHex, type='CHAN', channel name, sender, timestamp, flags. require.cache is restored in finally block.
  - #131 (WS handler runtime tests): Rewrote 5 `src.includes()` string-match tests to use vm.createContext with runtime execution. Created `makeNodesWsSandbox()` helper that provides controllable setTimeout (timer queue), mock DOM, tracked api/invalidateApiCache calls, and real `debouncedOnWS` logic. Tests run actual nodes.js init() and verify: (1) ADVERT triggers refresh with 5s debounce, (2) non-ADVERT doesn't trigger refresh, (3) debounce collapses 3 ADVERTs into 1 API call, (4) _allNodes cache reset forces re-fetch, (5) scroll/selection preserved (panel innerHTML + scrollTop untouched by WS handler). Total: 87 frontend helper tests (same count — 5 replaced, not added), 61 decoder tests (+3).
  - Technique learned: require.cache mocking is effective for testing code paths that depend on external modules (like ChannelCrypto). Store original, replace exports, restore in finally. Controllable setTimeout (capturing callbacks in array, firing manually) enables testing debounce logic without real timers.

- **Massive session 2026-03-27 (FULL DAY):** Reviewed and approved all 6 fixes, closed 2 test gaps, validated E2E:
  - **Batch PR review:** #123 (channel hash), #126 (ambiguous prefixes), #130 (live map), #131 (WS auto-update), #129 (observer comparison) — 2 gaps identified, resolved.
  - **Gap 1 closed:** #123 decrypted status mocked via require.cache (ChannelCrypto module swap). 3 new decoder tests.
  - **Gap 2 closed:** #131 WS debounce runtime tests via vm.createContext. 5 source-match tests replaced with actual execution tests. Controllable setTimeout technique verified.
  - **Test counts:** 109 db tests (+14 phantom), 204 route tests (+5 WS), 90 frontend tests (+3 pane), 61 decoder tests (+3 channel), 25 Go ingestor tests, 42 Go server tests.
  - **E2E validation:** 16 Playwright tests passing, all routes functional with merged 1.237M observation DB. Browser smoke tests verified. Coverage 85%+ backend, 42%+ frontend.
