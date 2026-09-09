package dbschema

// WriterDSN builds the DSN every writer must open the database with.
//
// It lives here because this package is the one thing cmd/ingestor and
// cmd/migrate both import, and they are the only two writers. Keeping the
// string in one place is not tidiness: cmd/migrate previously opened with a
// bare path, which under github.com/mattn/go-sqlite3 silently means
// synchronous=NORMAL, so the durability the ingestor DSN carefully pins was
// lost the moment anyone ran the migrate CLI against the same database. Two
// copies of a DSN is how that happens.
//
// Every parameter is deliberate:
//
//   - _journal_mode=WAL — cmd/server reads concurrently and needs WAL.
//   - _synchronous=FULL — SQLite's own default, and what modernc.org/sqlite
//     left in place. mattn defaults this connection to NORMAL and executes the
//     pragma unconditionally, so it must be stated to be kept. NORMAL can lose
//     recent transactions on power loss.
//   - _auto_vacuum=incremental — cmd/ingestor/maintenance.go drives
//     incremental_vacuum. Only takes effect on a database created with it.
//   - _foreign_keys=on — the schema relies on FK enforcement.
//   - _busy_timeout=5000 — writers serialise behind the reader.
//   - _cache_size=-2000 — 2000 KiB of page cache per connection. This is
//     SQLite's own default, pinned so it cannot drift: the page cache is a C
//     allocation and therefore sits outside GOMEMLIMIT.
func WriterDSN(path string) string {
	return path +
		"?_journal_mode=WAL" +
		"&_synchronous=FULL" +
		"&_auto_vacuum=incremental" +
		"&_foreign_keys=on" +
		"&_busy_timeout=5000" +
		"&_cache_size=-2000"
}
