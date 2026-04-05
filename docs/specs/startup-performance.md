# Startup Performance: Serve HTTP Within 2 Minutes on Any Database Size

## Problem

CoreScope takes 30–45 minutes to start on large databases (325K transmissions, 7.3M observations, 1.4GB SQLite). The HTTP server is completely unavailable during this time. Operators cannot restart without 30+ minutes of downtime.

### Where time goes (7.3M observation benchmark)

| Phase | Time | Blocking? |
|---|---|---|
| `Load()` — read SQLite → memory | ~90s | Yes |
| Build subpath index | ~20s | Yes |
| Build distance index | ~15s | Yes |
| Build path-hop index | <1s | Yes |
| Load neighbor edges from SQLite | <1s | Yes |
| **Backfill `resolved_path` for NULL observations** | **20–30+ min** | **Yes — the killer** |
| Re-pick best observations | ~10s | Yes |

The backfill calls `resolvePathForObs` for every observation with `resolved_path IS NULL`, then writes results back to SQLite and updates in-memory state. On first run (or after schema migration), this means resolving all 7.3M observations.

### Root cause

`backfillResolvedPaths()` in `neighbor_persist.go` runs synchronously in `main()` before `httpServer.ListenAndServe()`. It:
1. Collects all observations with `ResolvedPath == nil` under a read lock
2. Resolves paths (CPU-bound, ~millions of calls to `resolvePathForObs`)
3. Writes results to SQLite in a single transaction
4. Updates in-memory state under a write lock

Steps 2–4 block the main goroutine for 20–30 minutes.

## Solution: Async Chunked Backfill

### Design

Move `backfillResolvedPaths` out of the startup critical path. Start the HTTP server immediately after loading data and building indexes. Run backfill in a background goroutine with chunked processing that yields between batches.

### Startup sequence (new)

```
1. OpenDB, verify tables                    (~1s)
2. store.Load()                             (~90s)
3. ensureNeighborEdgesTable                  (<1s)
4. ensureResolvedPathColumn                  (<1s)
5. Load/build neighbor graph                 (<1s)
6. Build subpath/distance/path-hop indexes   (~35s)
7. pickBestObservation (with whatever        (~10s)
   resolved_path data exists)
8. *** START HTTP SERVER ***                 — serving at ~2min mark
9. Background: backfillResolvedPaths         (20-30 min, non-blocking)
   → chunked, yields between batches
   → updates in-memory + SQLite incrementally
   → re-picks best obs for affected txs
```

Total time to first HTTP response: **~2 minutes** regardless of database size.

### Implementation details

#### 1. Background backfill goroutine

```go
// In main(), after starting HTTP server:
go func() {
    backfillResolvedPathsAsync(store, dbPath, 5000, 100*time.Millisecond)
}()
```

The async backfill processes observations in chunks of N (e.g., 5,000):

```go
func backfillResolvedPathsAsync(store *PacketStore, dbPath string, chunkSize int, yieldDuration time.Duration) {
    for {
        n := backfillResolvedPathsChunk(store, dbPath, chunkSize)
        if n == 0 {
            break // done
        }
        log.Printf("[store] backfilled resolved_path for %d observations (async)", n)
        time.Sleep(yieldDuration) // yield to HTTP handlers
    }
    log.Printf("[store] async resolved_path backfill complete")
}
```

Each chunk:
1. Takes a read lock, collects up to `chunkSize` pending observations, releases lock
2. Resolves paths (no lock held — `resolvePathForObs` only reads immutable data)
3. Opens a separate RW SQLite connection, writes results in a transaction
4. Takes a write lock, updates in-memory `obs.ResolvedPath` and re-picks best obs for affected transmissions, releases lock
5. Sleeps briefly to yield CPU/lock time to HTTP handlers

#### 2. Readiness flag and API degraded-mode header

Add a boolean to `PacketStore`:

```go
type PacketStore struct {
    // ...
    backfillComplete atomic.Bool
}
```

API responses include a header during backfill:

```
X-CoreScope-Status: backfilling
X-CoreScope-Backfill-Remaining: 4523000
```

After backfill completes:
```
X-CoreScope-Status: ready
```

The frontend can read this header and show a subtle banner: *"Resolving hop paths… some paths may show abbreviated pubkeys."*

#### 3. Index rebuilds

The subpath, distance, and path-hop indexes are built during startup from whatever data exists. During backfill, newly resolved paths need to update these indexes incrementally.

Options (in order of preference):

**Option A: Defer index updates to end of backfill.** Indexes work fine with unresolved paths — they just produce slightly less precise results. After backfill completes, rebuild indexes once. Simple, correct, low risk.

**Option B: Incremental index updates per chunk.** After each chunk, update affected index entries. More complex, better real-time accuracy. Only worth it if index accuracy during backfill matters for production use.

**Recommendation: Option A.** The indexes are usable with unresolved paths. A single rebuild at the end (~35s) is cheap compared to the backfill duration. The API works throughout — results just improve after backfill finishes.

#### 4. SQLite contention

The backfill opens a separate RW connection for writes. The main server uses a read-only connection for polling. SQLite WAL mode (already in use) allows concurrent readers and one writer. Contention risk is minimal:

- Write transactions are small (5,000 UPDATEs per chunk, batched in a single tx)
- Read queries from HTTP handlers are unaffected by WAL writes
- The 100ms yield between chunks prevents sustained write pressure

#### 5. Lock contention

The write lock is held only during the in-memory update phase of each chunk (~5,000 pointer assignments + re-picks). This takes microseconds. HTTP handlers acquire read locks for API responses — they will not be blocked for any perceptible duration.

#### 6. Frontend handling

The `hop-resolver.js` module already handles unresolved (prefix) hops gracefully — it shows abbreviated pubkeys. No frontend changes are required for correctness.

Optional enhancement: read the `X-CoreScope-Status` header and show a transient info banner during backfill. This is cosmetic and can be done in a follow-up.

### What about first-run specifically?

On first run with a pre-existing database (e.g., migrating from a version without `resolved_path`), ALL 7.3M observations need backfill. The async approach handles this identically — it just takes longer in the background while HTTP is already serving.

On subsequent restarts, `resolved_path` is already persisted in SQLite and loaded by `store.Load()`. The backfill loop finds zero pending observations and exits immediately.

### What about new observations during backfill?

The poller ingests new packets continuously. New observations written by the ingestor already have `resolved_path` set at ingest time (this is already implemented). The backfill only processes observations with `ResolvedPath == nil`, so there's no conflict with new data.

## Alternatives considered

### Lazy resolution (resolve on API access)

Resolve `resolved_path` only when an observation is accessed via API, cache the result.

**Rejected because:**
- Adds latency to every API call that touches unresolved observations
- Cache invalidation complexity (when does a cached resolution become stale?)
- Doesn't help with index accuracy — indexes still need full data
- The backfill is a one-time cost; lazy resolution makes it a recurring cost

### Progressive loading (recent data first)

Load only the last 24h into memory, start serving, load historical data in background.

**Rejected because:**
- Significantly more complex — all store operations need "is this data loaded yet?" checks
- Memory implications: need to track which time ranges are loaded
- Historical queries return wrong results during loading (not just degraded — wrong)
- The actual bottleneck is backfill, not `Load()`. Even loading all 7.3M observations takes only ~90s.

### Chunked blocking backfill (yield to HTTP between chunks, but keep in main startup)

Process N observations per tick with `runtime.Gosched()` between chunks, but still in `main()` before `ListenAndServe`.

**Rejected because:**
- HTTP still isn't available until all chunks complete
- Adds complexity without solving the core problem

## Carmack Review (Performance)

**The approach is sound.** Moving a 20–30 minute blocking operation to a background goroutine is the right call. Some notes:

1. **Chunk size tuning.** 5,000 is a reasonable starting point. Monitor: if write lock contention shows up in pprof (unlikely with microsecond hold times), reduce chunk size. If backfill is too slow, increase it or reduce yield time.

2. **Memory is not a concern.** The observations are already fully loaded in memory by `Load()`. The backfill only mutates the `ResolvedPath` field on existing objects — no additional memory allocation beyond temporary slices for the chunk.

3. **No hidden costs in `resolvePathForObs`.** It reads `nodePM` (a `PrefixMatcher`, immutable after startup) and `graph` (neighbor graph, immutable after startup). No locks needed during resolution. This is embarrassingly parallelizable if needed, but single-goroutine processing with chunking is sufficient.

4. **The index rebuild at the end is O(n) and takes ~35s.** This is a one-time cost after the first backfill. Not worth optimizing further unless the profile shows otherwise.

5. **Risk: `pickBestObservation` during backfill.** API responses may flip their "best" observation as resolved paths become available. This is cosmetically noisy but functionally correct. Document this as expected behavior.

6. **Future optimization if needed:** The backfill loop could be parallelized across multiple goroutines (partition observations by transmission hash). The resolution step is CPU-bound and read-only. This would reduce backfill wall time from 30 min to ~5 min on 8 cores. Not needed for MVP — the goal is HTTP availability, not backfill speed.

## Implementation plan

1. **Refactor `backfillResolvedPaths` into chunked async version** — new function `backfillResolvedPathsAsync` that processes in chunks and yields
2. **Move backfill call in `main.go` to after `ListenAndServe`** — wrap in goroutine
3. **Add `backfillComplete` atomic flag to `PacketStore`** — set after backfill finishes
4. **Add `X-CoreScope-Status` response header** — middleware reads the flag
5. **Rebuild indexes after backfill completes** — single call to rebuild subpath/distance/path-hop
6. **Tests:** unit test for chunked backfill (mock store with N unresolved obs, verify chunks process correctly)
7. **Frontend (follow-up):** optional banner during backfill state

Estimated effort: 1–2 hours for steps 1–5, plus tests.
