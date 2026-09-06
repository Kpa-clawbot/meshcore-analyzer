#!/bin/sh
# Run all tests with coverage
set -e

echo "═══════════════════════════════════════"
echo "  CoreScope — Test Suite"
echo "═══════════════════════════════════════"
echo ""

# Unit tests (deterministic, fast)
echo "── Unit Tests ──"
node test-packet-filter.js
node test-packet-filter-ux.js
node test-aging.js
node test-issue-1065-gesture-hints-gates.js
node test-frontend-helpers.js
node test-fetch-all-nodes-pagination.js
node test-my-repeaters-dashboard.js
node test-repeater-metric-scatter.js
node test-top-routes-overlay.js
node test-url-state.js
node test-perf-go-runtime.js
node test-channel-psk-ux.js
node test-channel-sidebar-layout.js
node test-channel-fluid-layout.js
node test-channel-modal-ux.js
node test-channel-decrypt-insecure-context.js
node test-channel-qr.js
node test-channel-qr-wiring.js
node test-channel-issue-1087.js
node test-issue-1409-no-encrypted-flood.js
node test-analytics-channels-integration.js
node test-observers-headings.js
node test-issue-1789-observer-firmware-cols.js
node test-issue-1648-m1-emoji-scan.js
node test-issue-1648-m2-emoji-scan.js
node test-issue-1648-m3-emoji-scan.js
node test-issue-1648-m6-final-sweep.js
node test-issue-1648-m6-lint-self.js
node test-issue-1890-og-url.js
node test-traces.js
node test-live-multibyte-filter.js
node test-issue-1956-release-routing.js

# #1418 — route-view v2 (Tufte) coverage
node test-issue-1418-raw-hex-extraction.js
node test-issue-1418-edge-weights.js
node test-issue-1418-cb-preset-ramp.js
node test-issue-1418-spider-fan.js
node test-issue-1418-deeplink-hops-channels.js
node test-issue-1418-polish-review.js
node test-issue-1420-tile-providers.js
node test-issue-1614-tile-url-function.js
node test-issue-1438-marker-css-vars.js
node test-issue-1438-customizer-mcrole.js
node test-issue-1446-cb-preset-cascade.js
node test-issue-1380-cb-sim-overlay.js
node test-issue-1380-cb-reset-button.js
node test-issue-1450-logo-aspect.js
node test-issue-1454-channels-toggle.js
node test-issue-1456-score-labels.js

# #1461 mobile UX overhaul + #1470 node-detail tile helper (#1468 covered by E2E)
node test-issue-1461-mobile-page-actions.js
node test-issue-1470-node-tile-helper.js
node test-issue-1485-live-anim-z.js
node test-issue-1532-live-fullscreen.js
node test-naive-banner-tone.js
node test-issue-1473-reserved-prefixes.js
node test-issue-1473-prefix-generator.js
node test-issue-1770-mobile-row-clamp.js
node test-issue-1849-trace-hashbytes.js
node test-nodes-export.js
node test-nodes-export-wiring.js

echo ""
echo "═══════════════════════════════════════"
echo "  All tests passed"
echo "═══════════════════════════════════════"
