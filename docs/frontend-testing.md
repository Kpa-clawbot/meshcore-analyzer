# Frontend tests

`test-all.sh` is the authoritative list of standalone frontend suites. Both
`npm run test:unit` and the CI **Run JS unit tests** step invoke it. `npm test`
and `npm run test:coverage` wrap the same runner with coverage reporting.

Run `npm run test:unit` from a checkout with Node.js, a POSIX shell, Bash, and
Python 3 available. The XSS gate's fixture test uses Bash and Python. On
Windows, use Git Bash and ensure `python3` resolves to Python 3 rather than a
Microsoft Store alias; set `PYTHONUTF8=1` for the gate's Unicode output.
No server or browser is started by this runner.

When adding a root `test-*.js` file:

1. Add its `node test-name.js` command to `test-all.sh` if it runs standalone.
2. Otherwise add it to `scripts/non-unit-tests.json`, with a prerequisite or
   reason explaining why it belongs outside the unit runner. Browser suites
   still need explicit selection in the E2E workflow to become CI gates.
3. Run `node test-test-inventory.js`. The guard rejects unclassified files,
   duplicate assignments, removed files left in a list, undocumented groups,
   and local/CI entry points that bypass the unit runner.

The separate inventory distinguishes plain Node Playwright scripts, suites
requiring the `@playwright/test` runner, and the `jsdom` integration suite.
Those last two dependencies are not currently declared in `package.json`;
classification records that limitation rather than installing them implicitly.
The existing E2E workflow retains its browser installation, local fixture
server, coverage collection, and repeat checks. Only use local servers for
browser tests; inventory membership alone does not mean a suite runs in CI.
