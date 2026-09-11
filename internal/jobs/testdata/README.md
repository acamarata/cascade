# internal/jobs/testdata: provenance

## Art.2 real-counterpart provenance

- **Driver:** `modernc.org/sqlite` (pure-Go SQLite), version `v1.58.0` as
  pinned in the repository root `go.mod`.
- **Method:** every migration and store test in this package opens a real
  database FILE under `t.TempDir()` via `database/sql.Open("sqlite",
  <path>)` (`migration_test.go`'s `openTestDB`) — never an in-memory
  double and never a self-authored schema stand-in. `ApplyJobsSchema`
  (migration.go) runs the real `internal/storage/migrate` DSL against
  that file, and assertions read the result back through
  `sqlite_master` (table presence, `TestJobsMigrationCreatesSevenTables`)
  and `PRAGMA table_info(...)` (column presence, including every W9/W6
  column, `TestJobsMigrationW9W6Columns`) directly against the live
  driver — never a parsed copy of migration.go's own `TableDef` values.
- **Round-trip coverage:** every store test (`store_job_test.go`,
  `store_exec_test.go`, `store_lease_test.go`) writes through `Store`
  and reads back through the same real connection, so the DSL's emitted
  SQLite DDL (column types, PRIMARY KEY composites, `ON CONFLICT`
  upserts) is exercised for real, not mocked.

## fuzz/FuzzJobState/

Seed corpus for `state_test.go`'s `FuzzJobState`, which asserts
`DecodeJobState` never panics and always resolves an unknown or
unparseable value to `JobStateFailed` (06 §5.7), never a zero value.
Seeds cover: every valid state name, the empty string, a case-mismatched
valid name (`PENDING`), an unrecognized string (`bogus-state`), and
trailing/embedded control bytes — go-fuzz-format `string(...)` seed
files (`seed1`–`seed3`), matching this repository's existing convention
(`internal/fleet/governor/testdata/fuzz/FuzzParseProcStat/seed1`).

## Lease model (P1-E29-W6-S59-T2) Art.2 real-counterpart provenance

- **Persistence:** `lease_test.go`/`lease_expiry_test.go`/
  `lease_fence_test.go` all construct their `LeaseManager` over
  `newTestStore` (migration_test.go's real `modernc.org/sqlite` file
  under `t.TempDir()`) -- the same real driver and method the rest of
  this package's tests use, never an in-memory double of the lease
  model's own persistence. `Store.withTx` (lease_query.go) wraps the
  conflict-check-and-grant in one real `*sql.Tx` over that same file,
  and the mutual-exclusion and expiry-race tests
  (`TestLeaseAcquireMutualExclusion`, `TestLeaseExpiryRaceAtExactInstant`)
  run real concurrent goroutines against it under `-race`.
- **Attention queue / event bus / journal:** `lease_events_test.go`
  constructs a real `internal/fleet/supervision.Store`, a real
  `internal/events.Bus`, and a real `internal/fleet/journal.SQLiteStore`,
  all backed by one shared `internal/storage/storetest.NewMemStore()`
  provider.Store -- the same in-memory KV each of those three packages'
  OWN tests already use as their real counterpart. No lease-specific
  test double of any of the three exists anywhere in this package.
- **Time:** every lease test drives elapsed time via
  `internal/runtime.FixedClock.Advance` -- no bare `time.Now`, no
  `time.Sleep` used as a synchronization primitive anywhere in
  lease_test.go/lease_expiry_test.go/lease_fence_test.go.
- **Process liveness:** `lease_fence_test.go` injects a deterministic
  `fakeLivenessProbe` for `Reclaim`'s tests (the pgid-liveness question
  is a boolean seam by design, per this ticket's HOW step 11 -- there is
  no "real" counterpart to a live/dead OS process in a unit test); the
  production `unixLivenessProbe`/`windowsLivenessProbe`
  (lease_fence_unix.go/lease_fence_windows.go) are exercised by
  `go build`/`go vet` on their respective `GOOS` only, per this
  package's existing platform-split convention.

Tool/version/date: `modernc.org/sqlite` v1.58.0 (root go.mod pin),
`github.com/bmatcuk/doublestar/v4` v4.10.0 (MIT license; added by this
ticket per 06-FORGE-SPEC.md §7's standing license-gate authorization),
Go 1.26, recorded 2026-09-11.
