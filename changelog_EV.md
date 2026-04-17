# Changelog — EV session

Changes made during this development session, ordered newest first.

---

## Perf page — server-side history (`5fe12b2`)

**Files:** `cmd/server/types.go`, `cmd/server/routes.go`, `cmd/server/main.go`, `cmd/server/coverage_test.go`, `public/perf.js`

- Added `PerfSample` struct (mirrors frontend ring buffer fields: `ts`, `cpuPercent`, `totalSysMB`, `heapAllocMB`, `goroutines`, `packetsInRAM`, `cacheHitRate`, `avgMs`, `dbSizeMB`).
- Added `perfHistory []PerfSample` + `perfHistoryMu sync.Mutex` to `Server` struct.
- Added `collectPerfSample()` and `storePerfSample()` methods on `*Server`.
- Added `GET /api/perf/history` endpoint — returns the ring buffer as `{ "samples": [...] }`.
- Background goroutine in `main.go` fires once immediately on startup, then every 60 s. Holds up to 2 880 samples (48 h at 1-min resolution).
- `perf.js`: `preloadFromServer()` fetches the server history on every page load and merges it into both local ring buffers. Deduplication by timestamp prevents doubles on refresh.
- **Result:** opening the perf page for the first time shows up to 48 h of historical data immediately rather than an empty "collecting…" state.
- Tests: `TestHandlePerfHistoryEmpty`, `TestHandlePerfHistoryAfterSample`.

---

## Perf page — 48 h graph history (`d2016ce`)

**Files:** `public/perf.js`

- Added a second **long-history ring buffer** (`longHistory`, 1-min resolution, 2 880 samples max) alongside the existing short buffer (5 s resolution, 720 samples / 1 h).
- `pushSample()` writes to the short buffer every tick and to the long buffer at most once per minute.
- `getSlice()` routes to the correct buffer based on the selected timeframe.
- Timeframe buttons expanded to `5m / 15m / 30m / 1h / 6h / 12h / 24h / 48h`.
- Both buffers persist separately in `sessionStorage` (`cs-perf-history` / `cs-perf-history-long`); stale entries are pruned on load (>1 h / >48 h respectively).
- Total `sessionStorage` usage: ~130 KB short + ~520 KB long — well within the 5 MB limit.

---

## Perf page — persist graph history across refreshes (`8bd663d`)

**Files:** `public/perf.js`

- Ring buffer now serialised to `sessionStorage['cs-perf-history']` on every `pushSample()` call.
- Restored on module load with entries older than 1 h pruned automatically.
- A page refresh (F5) no longer resets the graphs; closing the tab still clears the buffer.

---

## Perf page — CPU %, server RAM and dataset size (`49aca07`)

**Files:** `cmd/server/cpu_unix.go` *(new)*, `cmd/server/cpu_windows.go` *(new)*, `cmd/server/types.go`, `cmd/server/routes.go`, `public/perf.js`

- `cpu_unix.go` (`//go:build !windows`): `getCPUPercent()` on `*Server` via `syscall.Getrusage`. Tracks user+sys CPU nanoseconds and wall-clock delta between calls; returns the percentage. Explicit `int64` casts handle Darwin's `int32 Usec` vs Linux's `int64`.
- `cpu_windows.go` (`//go:build windows`): stub returning `0` so the binary compiles on Windows.
- `GoRuntimeStats` in `types.go` gains `CpuPercent float64` (`cpuPercent`) and `TotalSysMB float64` (`totalSysMB`, sourced from `runtime.MemStats.Sys`).
- `Server` struct gains `cpuMu sync.Mutex`, `cpuLastWall time.Time`, `cpuLastCPUNs int64`.
- `handlePerf` wires in both new fields.
- `perf.js`: CPU Usage (%) and Server RAM cards added to the top overview row and the Go Runtime detail section, with colour thresholds (≥40 % yellow, ≥80 % red). Both metrics added to the graphs METRICS array and ring buffer.
- Test: `TestHandlePerfCPUFields`.

---

## Perf page — graph toggle + timeframe selector (`46e9806`)

**Files:** `public/perf.js`, `public/style.css`

- Added **Cards / Graphs** toggle button to the perf page header.
- Graph view renders a `Chart.js` line chart per metric in a responsive CSS grid.
- Charts update in-place (`chart.update('none')`) every 5 s — no DOM rebuilds, no flicker.
- In-memory ring buffer (720 samples, 1 h at 5 s intervals) stored in the IIFE closure — survives SPA navigations.
- **Timeframe selector:** `5m / 15m / 30m / 1h` buttons, persisted to `localStorage`.
- View mode (`cards` / `graphs`) persisted to `localStorage`.
- Charts destroyed on switch back to cards view or on page `destroy()`.
- CSS added: `.perf-header`, `.perf-header-controls`, `.perf-view-btn`, `.perf-tf-btn`, `.perf-graphs-grid`, `.perf-graph-card`, `.perf-graph-title`.
- Metrics tracked: Heap Alloc (MB), Server RAM — Heap Sys (MB), Goroutines, Packets in RAM, Cache Hit Rate (%), Avg Response (ms), DB Size (MB).

---

## Home page — Discord button (`11c882e`)

**Files:** `public/home.js`, `public/home.css`

- Added a **💬 Join the Discord** button alongside the donate button.
- Styled with Discord brand colour (`#5865f2`); shares the `.home-donate-actions` flex row.
- Placeholder URL: `https://discord.gg/placeholder` (to be updated when invite is ready).

---

## Home page — donate section + toggle bug fix (`c53e735`)

**Files:** `public/home.js`, `public/home.css`, `public/observers.js`

**Donate section:**
- Added `.home-donate` card before the footer explaining that the Cornmeister.nl analyzer runs on real hardware and that donations help cover server costs.
- **❤️ Support the project** button links to `https://bunq.me/CornmeisterNL`.
- CSS: `.home-donate`, `.home-donate-text`, `.home-donate-actions`, `.home-donate-btn`, `.home-discord-btn`.

**Toggle bug fix (`observers.js`):**
- Help block expand/collapse stopped working after the first auto-refresh (every 30 s).
- Root cause: `content.style.maxHeight === '0px'` becomes unreliable after the DOM is re-rendered — the inline style can normalise to `''`.
- Fix: use `toggle.textContent === '▶'` as the state source instead; the arrow character is always stable.

---

## Observers page — MQTT block margin standardisation (`4676739`)

**Files:** `public/observers.js`

- Standardised all spacing inside the "How to connect your observer" help block to the design system scale: `8 px` (label gap), `12 px` (description gap), `16 px` (section breaks and HR margins).
- Block is collapsed by default (`max-height: 0px`) and expands to `2000px` on click.
