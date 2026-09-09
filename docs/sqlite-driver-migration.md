# SQLite driver: modernc.org/sqlite → github.com/mattn/go-sqlite3

The database driver changed from `modernc.org/sqlite v1.34.5` (pure Go, SQLite
3.46.0) to `github.com/mattn/go-sqlite3 v1.14.52` (cgo, bundled SQLite 3.53.4).

This is the record of why, what it measured, and the five behavioural
differences that had to be handled — several of which fail silently if you get
them wrong, so read this before touching the DSNs or the build.

## Why

corescope is read-heavy: the server chunk-loads a graph at startup and fans out
neighbor/topology/analytics queries per request. modernc's pure-Go SQLite is a
transpilation of the C amalgamation and pays for it on exactly those paths.

## What it measured

Head-to-head on the same 120k-transmission / 240k-observation database, running
corescope's own hot-path SQL under both drivers (Apple M4, `-count=5`, medians):

| Workload | modernc | mattn | Change |
|---|---:|---:|---|
| Chunk load (the `cmd/server/chunked_load.go` v3 join, 20k transmissions) | 449 ms | 196 ms | **2.3× faster** |
| Aggregate scan (240k-row join + `GROUP BY`, stands in for analytics) | 276 ms | 137 ms | **2.0× faster** |
| 1500 prepared-statement lookups (`prepareStatements` round trips) | 512 ms | 403 ms | **1.3× faster** |

Allocation counts drop with it: 1.12M vs 1.64M allocs (−32%) and 21 MB vs 30 MB
(−30%) on the chunk load, 20.3k vs 27.3k on the lookups.

## The cost: the build is cgo now

`CGO_ENABLED=0` still *builds*, which is the trap: mattn links a stub, and the
binary dies on its first query with `go-sqlite3 requires cgo to work. This is a
stub`. A green build proves nothing here. `GOOS=linux go build` from a Mac, on
the other hand, genuinely cannot cross-compile any more — cgo needs a C compiler
that can target the other platform. That compiler is
[`zig`](https://ziglang.org/download/):

```bash
make build        # host
make crossbuild   # static linux/amd64 + linux/arm64 via `zig cc -target …-linux-musl`
```

Targeting musl and linking with `-extldflags "-static -Wl,-s"` keeps the output a
single self-contained binary, so the `alpine:3.20` runtime image no longer
depends on the base image's libc at all. `-Wl,-s` matters: Go's own `-s -w` does
not reach the musl objects zig links in, and without it the server binary is
19.8 MB instead of 12.1 MB.

The `Dockerfile` builder stage installs a checksum-pinned zig and does the same
thing, still on a single `$BUILDPLATFORM` builder with no QEMU for compilation.
Build-cache mounts are not optional there: compiling the SQLite amalgamation
twice from cold takes over half an hour.

## The five behavioural differences

### 1. Statement preparation is eager

modernc's `newStmt` only stored the SQL and compiled lazily on first use; mattn
calls `sqlite3_prepare_v2` inside `Prepare`. SQL referencing a missing table or
column now fails at **open** time.

This is the migration's largest single effect: 59 server tests failed on it,
purely from fixtures with partial schemas. `OpenDB` keeps failing loudly (that is
the #1901 behaviour we want in production, and `cmd/server/main.go` also gates on
`dbschema.AssertReady`); the fixtures instead declare what they are prepared
against, via `ensurePreparable` in `cmd/server/preparable_schema_test.go`. If you
add a prepared statement referencing something new,
`TestEnsurePreparableMatchesPrepareStatements` tells you to extend that helper.

It also surfaced nine `nodes(pubkey …)` declarations across seven files, when
production has only ever had `public_key`. Lazy compilation had hidden the
mismatch.

### 2. It exposed a real bug in the observation UPSERT

`stmtInsertObservation` resolves `ON CONFLICT(transmission_id, observer_idx,
COALESCE(path_json, ''))` against the unique expression index
`idx_observations_dedup` — which `cmd/ingestor/db.go` only ever created inside
the branch that creates the `observations` table for the first time. Databases
whose table predates that branch never had one, so the UPSERT had no conflict
target. modernc failed on the first insert; mattn fails at `OpenStore`. Same bug,
found earlier.

`internal/dbschema` now creates it unconditionally. Because the index is what was
supposed to prevent duplicates, a database that never had it can already hold
rows violating it — the repo's own `test-fixtures/e2e-fixture.db` held one — so
duplicates are collapsed first. Refusing would not have been safer: without the
index the ingestor cannot prepare its UPSERT, so it cannot start at all.

The collapse has to *replay* the UPSERT, and getting that subtly wrong is easy.
`DO UPDATE SET snr = COALESCE(excluded.snr, snr)` means the **incoming** value
wins when it is non-NULL, so down a group in id order the survivor ends up with
the **last** non-NULL value, not the first. It also names exactly five columns —
`snr`, `rssi`, `score`, `raw_hex`, `resolved_path` — so every other column keeps
the surviving row's own value; merging those too would invent history the
ingestor would never have written. An earlier version of this change took the
first non-NULL value and merged every column, which silently discarded newer
readings.

Merge, delete and index creation all share one transaction. Split apart, a
writer inserting a duplicate in the gap makes the index creation fail while the
deletions stay committed — rows destroyed and no index to show for it.

Note `COALESCE(path_json, '')` makes a NULL path and an empty-string path the
same key, but `NULL` and `'[]'` different keys. See
`internal/dbschema/dedup_index_test.go`, where the last-wins and atomicity
properties each have a test.

### 3. `synchronous` silently dropped from FULL to NORMAL

mattn defaults `synchronousMode` to `NORMAL` and runs `PRAGMA synchronous =
NORMAL` unconditionally, where SQLite's own compile default (what modernc left in
place) is FULL. In WAL mode that changes durability under power loss.

The writer DSN pins `_synchronous=FULL`, and lives in one place —
`dbschema.WriterDSN` — because there are two writers. `cmd/migrate` originally
kept a bare path and so silently wrote at NORMAL, which is exactly what a second
copy of a DSN buys you. `TestOpenStorePragmas` reads every pragma back
**through the store's own connection**, and `TestWriterDSNPragmas` covers the DSN
itself; a separate `sqlite3` session or the startup log line would prove
nothing.

### 4. The DSN dialect is different, and each driver ignores the other's

modernc understood only `_pragma=name(value)`; mattn understands only
`_`-prefixed parameters. Neither errors on the other's form, so a driver-only
rename would have dropped every pragma on the floor in silence. All five
`_pragma=` DSNs were rewritten (one production, four test seeds).

Also removed: `_journal_mode=WAL` on the server's read handle. modernc had been
ignoring it all along; mattn honours it, and setting `journal_mode` on a
read-only connection is a write. Dropping `_busy_timeout` with it costs nothing —
mattn's default is already 5000 ms, so the read handle finally *gets* the busy
timeout it had silently lacked.

### 5. `mode=ro` still works — but not for the reason you would guess

mattn always passes `SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE` to
`sqlite3_open_v2`, and the bundled amalgamation has `SQLITE_USE_URI=0`. What
makes `file:…?mode=ro` work anyway is that mattn's C wrapper `_sqlite3_open_v2`
ORs `SQLITE_OPEN_URI` into the flags itself. So the read-only invariant from
#1283/#1289 holds with no build flags — but it depends on the `file:` prefix
being present. `TestOpenDBRefusesMissingDatabase` fails if any of that stops
holding.

`cmd/decrypt` had been building its DSN *without* the `file:` prefix, so both
drivers stripped the query string and its `mode=ro` had never applied — a missing
path was created read-write. Fixed in passing; it was never a migration
regression.

## Memory

`runtime/debug.SetMemoryLimit` (GOMEMLIMIT) covers what the Go runtime manages —
heap, stacks, runtime structures — and nothing else, so it is not an RSS ceiling
now that SQLite allocates in C.

What is bounded is the page cache specifically: both DSNs pin
`_cache_size=-2000`, i.e. ~2 MiB per connection, so ~8 MiB across the server's
`SetMaxOpenConns(4)` and ~2 MiB in the ingestor, comfortably inside the 1.5×
headroom `applyMemoryLimit` derives. That caps the page cache, not everything
SQLite allocates — statement and schema memory sit outside it — so revisit
against measured RSS if the connection count or `_cache_size` grows, or if a
workload starts holding many prepared statements.

There is no cgo-bytes metric because Go exposes no counter for one. Do not read
`processRSSMB - goSysMB` as the C share either: `goSysMB` is reserved address
space rather than resident memory, so the subtraction mixes two different
quantities. It is a smell test, not a measurement.

## Things that did not change

No modernc-specific API was in use — no `RegisterFunction`, no `*sqlite.Conn`, no
`modernc.org/sqlite/lib` error constants, no `sql.Register`. No `time.Time` is
ever bound as a query argument (every retention cutoff is pre-formatted), so
driver time handling is not in play. Both drivers convert declared
`DATE`/`DATETIME`/`TIMESTAMP` columns to `time.Time`, so `/api/dropped-packets`
keeps emitting `dropped_at` as RFC3339 exactly as before.
