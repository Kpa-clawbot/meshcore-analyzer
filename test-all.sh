#!/bin/sh
# Run all tests with coverage
set -e

echo "═══════════════════════════════════════"
echo "  MeshCore Analyzer — Test Suite"
echo "═══════════════════════════════════════"
echo ""

# Unit tests (deterministic, fast)
echo "── Unit Tests ──"
node test-packet-filter.js
node test-aging.js
node test-regional-filter.js

# Integration tests (spin up temp servers)
echo ""
echo "── Integration Tests ──"
node tools/e2e-test.js
node tools/frontend-test.js

echo ""
echo "═══════════════════════════════════════"
echo "  All tests passed"
echo "═══════════════════════════════════════"
