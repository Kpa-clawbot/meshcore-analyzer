#!/bin/sh
# Run server-side tests with c8, then frontend coverage with nyc
set -e

# 1. Server-side coverage (existing)
npx c8 --reporter=json --reports-dir=.nyc_output node tools/e2e-test.js

# 2. Instrument frontend
sh scripts/instrument-frontend.sh

# 3. Start instrumented server
COVERAGE=1 PORT=13581 node server.js &
SERVER_PID=$!
sleep 5

# 4. Run Playwright tests (exercises frontend code)
BASE_URL=http://localhost:13581 node test-e2e-playwright.js || true
BASE_URL=http://localhost:13581 node test-e2e-interactions.js || true

# 5. Collect browser coverage
BASE_URL=http://localhost:13581 node scripts/collect-frontend-coverage.js

# 6. Kill server
kill $SERVER_PID 2>/dev/null || true

# 7. Generate combined report
npx nyc report --reporter=text-summary --reporter=text
