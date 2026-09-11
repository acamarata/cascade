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
