# providers/sqlite

The modernc-sqlite (pure Go, no CGO) concrete `provider.Store` driver — WAL
mode, a single write-connection executor with a per-domain fairness queue,
a domain-ownership registry, and §D-3 storage-side arbitration (exclusive
flock + daemon-owns-store refusal). Implements P1-E02-W1-S02-T2.

## WAL configuration

`Open` opens two `*sql.DB` handles against the same `.db` file via
[modernc.org/sqlite](https://gitlab.com/cznic/sqlite) (registered as the
`"sqlite"` `database/sql` driver, pure Go — no CGO, satisfying
02-TARGET-STRUCTURE's "CGO avoided in core" mandate):

- a pooled **read** handle (multiple connections — WAL allows concurrent
  readers without blocking writers or each other);
- a **write** handle capped at `SetMaxOpenConns(1)`, exclusively driving
  the `WriteExecutor`.

Both connections open with `_journal_mode=WAL`, `_busy_timeout=5000` (ms —
mattn-compatible DSN shorthand keys modernc-sqlite recognizes, applied
before any other PRAGMA per the driver's own documented ordering), and
`_foreign_keys=1`. WAL is what lets the read pool observe committed writes
without contending with the write connection's open transaction.

## Write executor and fairness

Every `Put`, `Delete`, and `Tx` call is submitted as one job to a
`WriteExecutor`, fairness-tagged by domain (the `namespace` argument for
`Put`/`Delete`; see "Tx fairness" below for `Tx`). A single drainer
goroutine pops jobs in round-robin order across domains with pending work,
so a burst of writes to one domain cannot starve another domain's queued
write — this is 06-FORGE-SPEC.md §2's "single write-connection executor
with per-domain fairness queue," and the 04-PEWS-PLAN-W1-W3.md Epic B note
that SQLite's true invariant is "one writer per FILE": the fairness queue
is an app-level *scheduling* discipline on top of that physical fact, not
a claim that two writers ever touch the file concurrently.

### Tx fairness (design decision, contract silent)

`provider.Store.Tx(ctx, fn)` carries no namespace argument — a transaction
closure may read/write several namespaces before returning. Since the
fairness key must be known when the job is enqueued, not after `fn` runs,
every `Tx` job is tagged under one reserved fairness slot (`"\x00tx"`,
distinct from any real namespace by construction) rather than one derived
from `fn`'s eventual writes. All `Tx` jobs and all `Put`/`Delete` jobs
still funnel through the same single write connection either way — this
only affects scheduling order among concurrent callers, never
correctness.

## Domain registry

`DomainRegistry` (`Driver.OwnDomain`) is a coarser-grained exclusivity
mechanism than the write executor's per-op fairness queue: it lets a
caller claim a whole domain for a multi-step span of work (e.g. a
read-modify-write sequence spanning several `Store` calls), which per-op
serialization alone does not prevent two callers from interleaving. A
second `OwnDomain` of an already-held domain returns `ErrDomainOwned`.

The ticket's task text describes this as a "goroutine-keyed map." Go
exposes no stable, supported goroutine-ID API, so ownership here is
tracked by an explicit acquire/release token (the `release func()`
`OwnDomain` returns) instead of a literal goroutine ID — the same
exclusivity guarantee, expressed the idiomatic Go way.

## §D-3 arbitration and daemonless refusal

`Open`'s sequence, in order:

1. **Socket-probe-first.** If a `SocketProbe` was supplied via
   `WithSocketProbe`, `Open` calls it before touching the filesystem lock
   at all. A `true` result short-circuits `Open` with `ErrDaemonOwnsStore`
   — no flock attempt, no partial state to unwind. A nil probe (the
   default) skips this step entirely, which is correct for tests and for
   any caller that already knows it is the sole process (e.g. every
   `storetest`-driven test here, one process per `t.TempDir()` database).

   The real probe — actually checking whether a daemon's RPC listener is
   live — lives in `internal/rpc`/`internal/daemon`, which
   `providers/sqlite` may never import (Art.10.2: providers/** imports
   pkg/** only). `SocketProbe` is the injection seam; the composition root
   (`cmd/` or `internal/daemon`, which may import both) wires the real
   check in.

2. **Exclusive flock**, on a **sidecar `<path>.lock` file** — never the
   `.db` file itself. modernc-sqlite already manages its own OS-level
   locking on the database file as part of normal SQLite operation;
   layering a second, independent app-level flock directly onto that same
   file risks confusing SQLite's own lock state machine (spurious
   `SQLITE_BUSY`, or the reverse — an app-level lock that doesn't actually
   observe SQLite's in-process contention). A dedicated sidecar file gives
   §D-3's "never two writers" invariant a lock that is *only* about
   process-level ownership, orthogonal to SQLite's own file locking.

   - `flock_darwin.go`: `syscall.Flock(fd, LOCK_EX|LOCK_NB)`.
   - `flock_linux.go`: `golang.org/x/sys/unix.Flock(fd, LOCK_EX|LOCK_NB)`.
   - `flock_windows.go`: **tier-2 scope, not implemented.** Real Windows
     advisory locking (`LockFileEx`) is out of this ticket
     (04-PEWS-PLAN-W1-W3.md's T4 daemonless-Windows note). Rather than
     silently proceeding without the "never two writers" guarantee, `Open`
     on Windows always fails closed with a `cascade.KindUnsupported`
     refusal — verified by `TestOpen_ExclusiveLockRefusesSecondOpener`'s
     `runtime.GOOS == "windows"` branch, and by `GOOS=windows go build`
     (not `go test` — cross-compiled test binaries can't execute on this
     darwin host) succeeding.

   A held lock surfaces as `cascade.KindConflict`, traceable via
   `errors.Is(err, syscall.EWOULDBLOCK)` on darwin/linux — proven by
   `TestOpen_ExclusiveLockRefusesSecondOpener`'s non-windows branch, which
   opens the same path twice and asserts the second `Open` fails.

## Bench spike (Art.12)

`BenchmarkStoreWriteConcurrent` is this ticket's Art.12 risk spike for
"pure-Go SQLite under write load." Verdict and raw numbers:
`.claude/planning/p1/phase/journals/P1-E02-W1-S02-T2-bench-spike-findings.md`.

## Schema

One table, `kv(namespace, key, value)`, primary key `(namespace, key)`,
`WITHOUT ROWID`. Every `provider.Store` method is namespace-scoped by an
explicit column rather than a separate table per namespace, matching
`pkg/provider/store.go`'s own namespace-scoping design decision.

## Migration boot path

`Open` accepts an optional `WithMigrator` option (`driver.go`), mirroring
`WithSocketProbe`'s injection-seam pattern exactly and for the same
reason: `providers/sqlite` may import `pkg/**` only, never `internal/**`
(Art.10.2, `depguard`'s `plugins-providers-boundary` rule), so this
package never imports `internal/storage/migrate` directly. When a
`Migrator` is supplied, `openLocked` calls it against the write
connection immediately after the base `kv` schema is created and BEFORE
`Open` returns the `Driver` — so `internal/storage/migrate.Apply`'s
ledger bootstrap, downgrade check, §D-18 pre-migration snapshot, and any
caller-defined `MigrationSet` all complete before any caller can read
from or write to the store. A nil `Migrator` (the default) skips
migration entirely.

The real migrator is a thin adapter the composition root (`cmd/` or
`internal/daemon`, both of which may import `internal/storage/migrate`)
builds around `migrate.Apply`:

```go
sqlite.WithMigrator(func(ctx context.Context, db *sql.DB) error {
    return migrate.Apply(ctx, migrate.ApplyConfig{
        DB:        db,
        Dialect:   migrate.SQLiteEmitter{},
        Clock:     runtime.NewSystemClock(),
        DBPath:    path,                              // enables the §D-18 snapshot
        BackupDir: filepath.Join(profileDir, "backups"),
    }, myMigrationSet)
})
```

`migration_wiring_test.go` proves this wiring end-to-end against a real
database: after `Open` returns, the `applied_migrations` ledger table and
every table the supplied `MigrationSet` defines already exist, and a
failing `Migrator` aborts `Open` with the migrator's error rather than
returning a partially-migrated `Driver`.

### Adding a new `MigrationStep`

A schema change is a NEW `migrate.MigrationStep` (a new `TableDef` or
`IndexDef`) appended to the caller's `MigrationSet`, with `SchemaVersion`
incremented — `internal/storage/migrate`'s DSL is forward-only and
CREATE-only (`CREATE TABLE`/`CREATE INDEX ... IF NOT EXISTS`, no `ALTER`;
see `internal/storage/migrate/dsl.go`'s `StepKind` doc comment for why).
Adding a column to an existing table means defining its NEW shape as a
new table via a new step, the standard forward-only SQLite migration
pattern, which is portable to Postgres by construction.

### Pre-migration snapshot location

Before any step that actually executes new DDL, `migrate.Apply` WAL-
checkpoints the database and copies it to
`<BackupDir>/pre-migrate-v<on-disk-schema-version>.db` (§D-18), so an
operator always has a point-in-time rollback target from immediately
before a migration ran. See `internal/storage/migrate/ledger.go`'s
`Apply` doc comment for the exact idempotency and crash-safety guarantees
this snapshot does and does not provide.
