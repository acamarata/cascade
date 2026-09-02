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
