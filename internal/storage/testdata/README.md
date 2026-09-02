# internal/storage/testdata

No fixture files live here — this ticket (P1-E02-W1-S03-T1) has none to
check in. This README exists to record the Art.2 (12-QUALITY-
CONSTITUTION.md, "real-counterpart") provenance for `domains_test.go` and
`health_test.go`'s database assertions, per the ticket's own instruction
("Art.2 provenance recorded in internal/storage/testdata/README.md").

## Real-counterpart provenance

- **Tool:** `modernc.org/sqlite` (pure-Go, no CGO, the same driver
  `providers/sqlite` ships against).
- **Version:** `v1.58.0`, read from this module's `go.mod` at the time
  this ticket was built (2026-09-02) — see the `require` block's
  `modernc.org/sqlite` line.
- **Date:** 2026-09-02.
- **Method:** every test in `domains_test.go` and `health_test.go` opens a
  real `*sql.DB` via `sql.Open("sqlite", ...)` against a file inside
  `t.TempDir()` (or, for the two explicitly-in-memory cases,
  `sql.Open("sqlite", ":memory:")` — still the real modernc-sqlite engine,
  just without a backing file). No test constructs an in-process fake, a
  mock `*sql.DB`, or a hand-rolled schema double: `Bootstrap`'s claims are
  checked by querying `sqlite_master` directly (`SELECT name FROM
  sqlite_master WHERE type = 'table' ...`), `StorageHealthCheck`'s claims
  by reading `PRAGMA journal_mode`, `applied_migrations`, and the same
  `sqlite_master` table, and the domain-isolation test writes a real row
  through one anchor table and queries every other anchor table by name to
  confirm it is absent.
- **What is and is not exercised:** `TestStorageHealth_FlockProbe_
  HeldByAnotherOpen` additionally drives `providers/sqlite.Open` (the
  real `Driver`, real §D-3 sidecar `flock(2)`) to hold a genuine OS-level
  lock that a separate connection's health check must detect — not a
  simulated lock state. The windows tier-2 flock refusal path
  (`flock_windows.go`) is NOT exercised by any test in this package on
  this (darwin) build host; it is proven separately by `providers/sqlite`'s
  own `GOOS=windows go build` check and `lock_test.go`'s windows-tagged
  assertions, and by this ticket's own `GOOS=windows go build
  ./internal/storage/...` check succeeding.

## Why no fixture files

Bootstrap's anchor tables (`CREATE TABLE IF NOT EXISTS <prefix>_domain_root
(id INTEGER PRIMARY KEY)`) and the `applied_migrations` ledger stamp are
simple enough that a golden DDL fixture (the pattern
`internal/storage/migrate/testdata/golden/` uses for its emitter output)
would add a second source of truth for content this ticket's own tests
already assert directly against a live `sqlite_master` query. A fixture
becomes worth adding once a later ticket gives a domain real per-table
schema beyond the anchor.

## P1-E02-W1-S03-T2 (retention: windowed prune + weekly VACUUM)

- **Tool:** `modernc.org/sqlite` (pure-Go, no CGO — the same driver
  `providers/sqlite` and T1's `domains_test.go`/`health_test.go` ship
  against).
- **Version:** `v1.58.0`, read from this module's `go.mod` `require` block
  at the time this ticket was built (2026-09-02) — unchanged from T1's
  entry above; no dependency bump was needed or made.
- **Date:** 2026-09-02.
- **Method:** every test across `retention_helpers_test.go`,
  `retention_prune_test.go`, `retention_isolation_test.go`, and
  `retention_vacuum_test.go` opens a real `*sql.DB` via
  `sql.Open("sqlite", ...)` against a file inside `t.TempDir()`, in WAL
  mode (`PRAGMA journal_mode=WAL`, mirroring `domains.go`'s `Bootstrap`
  and `providers/sqlite.Open`). No test constructs a mock `*sql.DB`, a
  fake write path, or a hand-rolled schema double:
  - `DomainPruner.Prune`'s boundary/no-op/idempotency/isolation/
    aggregate-error claims are checked by real `INSERT`s into a real
    table followed by real `SELECT COUNT(*)` / row-content queries after
    `Prune` returns — never by inspecting in-memory state.
  - `TestPruneBatchCap`'s "exactly N DELETE round-trips" claim (the
    ticket's own "write-executor call-count instrumentation" acceptance
    criterion) is proven by a `countingExecer` that wraps the real
    `*sql.DB` and counts `ExecContext` calls whose query text starts with
    `DELETE`, then asserts against the real post-delete row count — the
    counting is pure observability over a real engine, never a
    substituted implementation of `deleteOlderThan` itself.
  - `VacuumJob.Run`'s claims are checked by `os.Stat`ing the real on-disk
    `.db` file before and after (`TestVacuumJob`: 10 000 padded rows
    inserted, fully cleared via `Prune`, then a real `VACUUM` — asserting
    `FileSizeAfter < FileSizeBefore` against the literal file, not a
    reported number the code could fabricate independently of disk
    reality) and by a follow-up `PRAGMA wal_checkpoint(PASSIVE)` query
    against the same real connection, asserting `busy=0` and
    `checkpointed==log` — the concrete, queryable proof that `Run`'s own
    `PRAGMA wal_checkpoint(FULL)` calls actually executed and left
    nothing outstanding.
  - Error-path tests (`TestFileSize_MissingPath`,
    `TestMainDatabasePath_ClosedDB`, `TestVacuumJob_Run_ClosedDB`,
    `TestPrune_AggregateError`'s missing-table domain) all exercise real
    OS/driver failures (a genuinely absent file, a genuinely closed
    `*sql.DB`, a genuinely nonexistent table) rather than an injected
    fake error.
- **What is and is not exercised:** no platform-specific code path exists
  in `retention.go`/`retention_vacuum.go` (Art.5 — the package doc says
  so explicitly), so there is nothing for a windows/linux-only fixture to
  cover beyond this ticket's own `GOOS=linux` / `GOOS=windows go build
  ./internal/storage/...` checks, both green.
