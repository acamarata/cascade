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

## Worktree manager (P1-E29-W6-S59-T3) Art.2 real-counterpart provenance

- **Git binary:** every worktree test
  (`worktree_test.go`/`worktree_list_test.go`/`worktree_sweep_test.go`/
  `worktree_quarantine_test.go`/`worktree_snapshot_test.go`) drives the
  REAL `git` binary found on `PATH` (`newExecGitRunner`, worktree.go)
  against a throwaway repository created fresh under `t.TempDir()` by
  `newTestGitRepo` (worktree_test.go) -- `git init`, a real commit, then
  real `git worktree add|remove|list|status|write-tree` calls. No
  porcelain output or git error text is ever hand-authored; only the
  injected BINARY PATH is swapped, and only for the two tests that
  specifically exercise a git-binary-failure path
  (`TestWorktreeCreateBrokenGitBinaryIsTypedError`,
  `withGitBinary`).
- **Capture tool/version/date:** `git version 2.51.0` (Homebrew,
  `/opt/homebrew/bin/git`), captured 2026-09-12, via
  `git -C <repo> worktree list --porcelain` (plain and, for the locked
  seed, after `git worktree lock <path> --reason "manual test lock"`).
  NOTE: this shell's bare `git` on `PATH` is transparently rewritten by
  this environment's own `rtk` token-optimizing wrapper and does not
  reproduce raw porcelain output when invoked interactively; every
  capture and every test in this package instead invokes the binary by
  its resolved absolute path (`/opt/homebrew/bin/git`) or through Go's
  `os/exec`, which resolves `PATH` directly and is never subject to that
  interactive-shell rewrite.
- **FuzzWorktreePorcelain seed corpus**
  (`testdata/fuzz/FuzzWorktreePorcelain/`): `seed_real_capture_1` is a
  clean two-worktree real capture (main + one linked worktree on branch
  `job/1`); `seed_real_capture_2` is the same repository after locking
  the linked worktree, capturing a real `locked <reason>` line. Both
  paths are real filesystem paths from the capturing machine
  (`/private/tmp/wtcheck/...`), not synthesized. `worktree_list_test.go`
  additionally seeds inline (`f.Add`) a detached, a `locked`-with-reason,
  and a `prunable`-with-reason block, transcribed verbatim from this same
  capture session, plus three adversarial shapes (empty input, an
  attribute line before any `worktree` header, an unrecognized keyword).
  `FuzzWorktreePorcelain` ran 30s clean (1.76M execs, 138 corpus entries,
  zero crashers) on 2026-09-12.
- **pgid liveness:** `worktree_sweep_test.go` injects the SAME
  `fakeLivenessProbe` lease_fence_test.go already defines (package-level,
  no redeclaration) -- the pgid-liveness question has no real counterpart
  in a unit test, exactly as this file's Lease model section above
  records for `Reclaim`'s own tests.
- **Journal/attention:** `worktree_quarantine_test.go` wires a real
  `internal/fleet/journal.SQLiteStore` and a real
  `internal/fleet/supervision.Store`, both backed by
  `internal/storage/storetest.NewMemStore()` -- the same real-counterpart
  pattern `lease_events_test.go`'s `newTestSink` already establishes in
  this package.

## Acceptance suite (P1-E29-W6-S60-T4) Art.2 provenance and status

`pews-ticket-fixture.yaml` is this acceptance suite's own fixture (a
minimal, structurally valid 17-field PEWS ticket, docs/**-only). There is
no production PEWSContract YAML decoder anywhere in the tree
(`pews_compiler.go`'s own doc comment: decoding is explicitly out of that
compiler's scope), so `acceptance_test.go`'s `loadFixtureContract` is
this suite's own loader over its own fixture shape, not a schema any
production code reads.

Paths 1 (happy path, `acceptance_path1_test.go`) and 3 (contending
lease, `acceptance_path3_test.go`) drive the REAL Planner, LeaseManager,
WorktreeManager (a real `git` binary against a throwaway repo under
`t.TempDir()`), EvidenceLedger (a real `internal/audit.Log` over a real
`storetest.NewMemStore()`), CompletionPolicy and Scheduler -- no
hand-authored executor double, no synthetic gate-set table. Both were
verified RED (a deliberately broken assertion, real failure output) then
GREEN, and pass under `-race`.

Paths 2 (kill -9 mid-DAG via a real daemon process, over its real
socket) and 4 (job.list/lease.list over a real unix socket) are NOT
implemented in this suite. See
`.claude/planning/p1/phase/journals/BLOCKED-P1-E29-W6-S60-T4.md` for the
full accounting: `testkit.SpawnDaemon` does not exist anywhere in the
tree (S-59.T5's own kill9 test already documents this and uses a
self-exec subprocess idiom instead); no daemon startup path resumes jobs
DAG state from the on-disk journal (`cmd/cascade/daemon_unix_jobs_rpc.go`
wires job.*/lease.* RPC handlers over a fresh in-process Store/
LeaseManager but never calls `Scheduler.Resume`); and a real unix-socket
RPC dial requires importing `net`/`net/http`, which
`internal/build/hygiene.go`'s `NoNetworkUnitTestScanFile` gate forbids in
any `_test.go` file lacking the `integration` build tag -- a tag this
ticket's own `checks:` list does not carry on its `-run TestAcceptance`
invocation.
