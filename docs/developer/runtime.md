# Runtime metrics (`internal/runtime`)

Status: seeded from Wave 1 (P1-E03-W1-S05-T4, internal counters/gauges +
snapshot API). Ticket contract:
`.claude/planning/p1/phase/epics/E-C/waves/W-1/sprints/S-05/tickets/T-4.yaml`.

This is **internal operational instrumentation** — counts and gauges a
subsystem can register and read for its own diagnostics and for the
periodic snapshot event. It is explicitly **not** the TELEMETRY egress
system (that is a separate, opt-in, anonymized surface enumerated in
H/S-16.T1's inventory). Nothing in this package sends data anywhere by
itself; see "Where metrics go today" below for the honest current state.

## Types (`internal/runtime/metrics.go`)

- **`Counter`** — a monotonically increasing `int64`, backed by
  `sync/atomic.Int64`. `Inc()` and `Add(n)` increment; `Value()` reads.
  There is no decrement method by design.
- **`Gauge`** — a settable `int64`, also atomic. `Set(v)` replaces,
  `Add(delta)` adjusts (delta may be negative), `Value()` reads. Unlike
  `Counter` it may go negative and may decrease.
- **`MetricSample`** — one point-in-time read: `{Name string, Labels
  map[string]string, Value int64, Ts time.Time}`. `Ts` is always stamped
  from an injected `Clock`, never a bare `time.Now()`.
- **`MetricsSnapshotEvent`** — the event-bus payload: `{Samples
  []MetricSample, Ts time.Time}`.
- **`Registry`** — a thread-safe, named collection of metrics. Construct
  with `NewRegistry()`.
  - `RegisterCounter(name, labels) *Counter` / `RegisterGauge(name,
    labels) *Gauge` — **panic** on a duplicate name. This is deliberate:
    a duplicate registration is a programming error, meant to be caught
    the first time the process boots with the conflicting registration
    code paths both compiled in, not handled as a runtime condition.
  - `Get(name) (Metric, bool)` — lookup by name.
  - `Snapshot(ts time.Time) []MetricSample` — one `RLock`-held pass over
    every registered metric.

### Snapshot consistency, precisely

`Snapshot` is **structurally atomic**: no metric can be added, removed, or
renamed mid-call, because registration takes the registry's write lock for
its whole duration and `Snapshot` holds the read lock for its whole
duration too. It is **not** a cross-metric transaction: each sample's
`Value` is an independent atomic load, and `Counter`/`Gauge` deliberately
never take the registry's lock to mutate (that is the whole point of a
lock-free counter — writers never block on a snapshot in progress). Two
samples from the same `Snapshot` call may therefore reflect writes from
different instants relative to each other under concurrent load. Treat
ratios between two samples in one snapshot as approximate when writers are
active, and exact only when they are quiescent during the call.

### Cardinality

The registry places no numeric cap on the number of distinct metric
names — the ticket contract specifies none, and R-14.107 forbids inventing
one. In practice it is bounded by its access pattern rather than a limit
check: registration is a startup-time, composition-root call (per
Art.10.2 — no subsystem imports this package by `init`), driven by a fixed
set of names each subsystem compiles in, never by untrusted runtime input.
A caller that registered names sourced from unbounded external input would
be misusing the API outside its documented contract.

## Periodic emitter (`internal/runtime/metrics_emitter.go`)

`RunPeriodicEmitter(ctx, PeriodicEmitterOptions, onError)` runs a
tick-snapshot-publish loop until `ctx` is cancelled:

- **`Ticker`** — a small interface (`C() <-chan struct{}`, `Stop()`)
  abstracting the tick source so tests never sleep (R-14.136).
  `NewSystemTicker(d)` is the production implementation, backed by a real
  `time.Ticker`; its own `time.Time` tick payload is discarded — only the
  injected `Clock`'s `Now()` ever stamps a timestamp that leaves this
  package.
- **`EventBus`** — `Publish(ctx, namespace, kind, source string, payload
  []byte) error`. This is a deliberately decoupled interface, not a
  literal match of `internal/events.Bus.Publish` (which takes a typed
  `events.EventKind`, not `string`): `internal/events` already imports
  `internal/runtime` (for `Clock`), so `internal/runtime` importing
  `internal/events` back would be a cycle. The composition root
  (`cmd/cascade`, outside this ticket's scope) is expected to supply a
  thin adapter over the real `*events.Bus`.
- On each tick: `Registry.Snapshot(clock.Now())` →
  `MetricsSnapshotEvent{Samples, Ts}` → JSON-encoded (the default
  `Encode`, overridable) → `EventBus.Publish(ctx, "metrics",
  "metrics.snapshot", "runtime.metrics", payload)`.
- A `Publish` or encode error is reported to `onError` (if non-nil) and
  the loop continues — a transient bus outage does not stop metrics
  collection.
- Default interval: `DefaultMetricsInterval` = 60s, used when
  `PeriodicEmitterOptions`/`BootstrapOptions.MetricsInterval` is zero, per
  the ticket contract's "configurable interval (default 60s)".
- Delivery through the real event bus is **at-least-once**, and ordering
  is guaranteed only **within one namespace** — the same contract
  `internal/events.Bus` documents for every publisher.

## Wiring (`internal/runtime/bootstrap.go`)

`Bootstrap` always creates a `Registry` (`Runtime.Metrics`), regardless of
whether an event bus is available — subsystems may always register
counters and gauges against it. The periodic emitter goroutine is started
**only** when `BootstrapOptions.MetricsBus` is non-nil.

### Where metrics go today, honestly

As of this ticket, no composition-root caller constructs a real
`*events.Bus` and passes an adapter for it through
`BootstrapOptions.MetricsBus` — that wiring is `cmd/cascade`'s to do in a
later ticket, once a daemon-lifetime `EventBus` adapter exists. Until
then, `Runtime.Metrics` is fully usable for in-process registration and
`Snapshot()` reads, but the periodic emitter never runs in production and
nothing is published anywhere. This is stated plainly per Art.1: the
emitter code path is real and tested (`metrics_emitter_test.go`,
`bootstrap_metrics_test.go`), but it has no live sink yet.

## Lockfile parsing (`internal/runtime/lockfile.go`)

`ParsePidfile(b []byte) (int, error)` turns pidfile bytes into a pid. The
accepted format is a single integer, optionally padded with leading or
trailing whitespace (including a trailing newline): the parser trims
surrounding whitespace with `strings.TrimSpace` before parsing, so
`"1234\n"` and `"1234"` parse identically.

Everything else is rejected with a plain error, never a panic:

- Empty content, or content that is only whitespace, after trimming.
- Content that does not parse as a base-10 integer.
- An integer that is zero or negative. No real OS pid is zero or
  negative, so a pidfile holding one is corrupt, not simply "no process."

**A parse failure is not staleness.** `ParsePidfile` only reports whether
it could read a pid out of the bytes it was given. It has no opinion on
whether a process with that pid is alive, and a caller must never treat
"failed to parse" as equivalent to "confirmed dead." The caller that
consumes this distinction is `scanPidfile` in `recovery_scan.go`: on a
parse error it logs a warning and leaves the pidfile in place, exactly as
it does when the pid parses but the owning process turns out to be alive
or undecided. Removal only ever follows a successful parse plus an
unambiguous dead-process result from `ProcessAlive`.

`FuzzParsePidfile` (`lockfile_test.go`) is the invariant check for this
function: seeded from `internal/testdata/fuzz/parsepidfile/seed1` plus a
handful of adversarial and non-UTF8 byte sequences, it asserts that
`ParsePidfile` never panics on any input, and that every pid it returns
without an error is strictly positive. It does not assert anything about
which inputs are accepted or rejected beyond that invariant.

## Process liveness (`ProcessAlive`)

`ProcessAlive(pid int) (ProcessLiveness, error)` answers one question:
does the operating system currently have a live process at `pid`. The
result type is a three-valued `ProcessLiveness`, not a boolean:

```go
type ProcessLiveness int

const (
	ProcessLivenessUndecided ProcessLiveness = iota
	ProcessLivenessAlive
	ProcessLivenessDead
)
```

On darwin and linux, `ProcessAlive` sends signal 0 to `pid` (POSIX's
standard existence probe: the kernel checks whether the process exists
and whether this process may signal it, without ever delivering an actual
signal), and maps the result:

| `kill(pid, 0)` result | `ProcessLiveness` | Why |
|---|---|---|
| `ESRCH` ("no such process") | `Dead` | The kernel has no live process at this pid. This is the only result that licenses removing anything. |
| `EPERM` ("operation not permitted") | `Alive` | A process exists at this pid, this caller just cannot prove which one. |
| `nil` (signal delivered) | `Alive` | The pid belongs to a process this caller can signal. |
| any other error | `Undecided` | An unexpected syscall failure the check cannot classify. |

This is the single most important contract in this package: **a
permission error counts as alive, not as undecided and not as dead.**
`EPERM` means the kernel found a live process slot to run its permission
check against; it is proof a process exists, even though it cannot prove
whose process it is. Collapsing this three-valued result down to a
boolean ("did the call succeed") reintroduces exactly the bug this type
exists to prevent: a starting daemon that only checks for a clean `nil`
result will read `EPERM` as "not alive" and go on to remove a pidfile,
socket, or lock a live daemon still owns.

The caller-side rule, enforced everywhere in `recovery_scan.go`, is
symmetric and absolute: only `ProcessLivenessDead` licenses deleting
anything. `Alive` and `Undecided` are both treated as "do not delete,"
with no code path that distinguishes them for the deletion decision.
The distinction exists only so log lines can say which one occurred.

On Windows, `ProcessAlive` (`lockfile_windows.go`) always returns
`ProcessLivenessUndecided` with a static error. There is no POSIX
`kill(pid, 0)` equivalent wired in, and the recovery scan's own
Windows short-circuit (below) means this function is never actually
called there in production; the stub exists only so the package compiles
under `GOOS=windows`.

If you add a new caller of `ProcessAlive` anywhere in this package or a
consumer of it, treat `Alive` and `Undecided` identically: never act on a
pid unless you have `ProcessLivenessDead` in hand.

## `DomainRegistry` and `OrphanedLocks`

`DomainRegistry` (`recovery.go`) is the interface the crash-recovery scan
uses for its orphaned-advisory-lock step:

```go
type DomainRegistry interface {
	OrphanedLocks(ctx context.Context) ([]OrphanedLock, error)
	Release(ctx context.Context, lockID string) error
}

type OrphanedLock struct {
	LockID   string
	OwnerPID int
}
```

An implementer's job is narrow: `OrphanedLocks` returns every
advisory-lock record currently on file, each carrying the pid that was
recorded as its owner at acquisition time. It is not expected to have
already filtered these by liveness. The scan does that filtering itself,
calling `ProcessAlive` on each `OwnerPID` and calling `Release` only for
locks whose owner comes back `ProcessLivenessDead`. Keeping the
liveness decision in the scan, and out of every `DomainRegistry`
implementation, keeps the "never delete on ambiguity" rule in exactly
one place instead of needing to be re-proven in each implementer.

**A nil registry is a valid configuration, not a bug.** `RecoveryOptions.Registry`
may be nil, and when it is, the orphaned-lock step is skipped outright.
This is reported honestly: the scan does not fabricate a result or log
success for a step it did not run, it simply omits any `StaleLocks`
entries and moves on. If you are implementing `DomainRegistry` for a real
backing store, do not assume the step always runs. It runs only when
something wires a non-nil value into `RecoveryOptions.Registry`.

**Error handling is per-lock, not all-or-nothing.** If `Release` fails
for one lock, the scan logs a warning naming that lock and its owner pid,
and continues on to the remaining locks in the list. One failing release
never aborts the others. Separately, if `OrphanedLocks` itself returns an
error, the whole lock-scanning step is skipped (logged, not silently
dropped), but this does not affect the pidfile or socket cleanup that
already ran earlier in the same scan.

## `RecoveryRegistry` injection

A production caller wires a `DomainRegistry` implementation through
`BootstrapOptions.RecoveryRegistry`:

```go
type BootstrapOptions struct {
	// ...
	RecoveryRegistry DomainRegistry
}
```

`Bootstrap` passes this straight through to `Scan`'s `RecoveryOptions.Registry`
field. There is no default implementation and no fallback logic beyond
"nil means skipped" as described above.

**Note for whoever wires the real daemon startup path:** as of this
writing, no production caller constructs a real `BootstrapOptions` value
with a non-nil `RecoveryRegistry`. The tests that exercise this field
(`bootstrap_test.go` and the `recovery.go`/`recovery_scan.go` unit tests)
supply fakes; nothing outside test code sets it. This means orphaned-lock
cleanup currently never runs against a real advisory-lock store in
production, no matter how many locks a crashed process left behind. If
you are the one building the daemon's real startup composition root,
you must construct a real `DomainRegistry` over the actual advisory-lock
store and set `BootstrapOptions.RecoveryRegistry` to it. Leaving it nil
will compile, will pass every existing test, and will silently mean
orphaned locks are never cleaned up after a crash.

## Windows support (tier 2)

The daemon is not supported on Windows. This package still has to build
there, because CI runs a `GOOS=windows GOARCH=amd64 go build` check
against it, and other parts of the tree may import it on any platform.
What that means concretely:

- `lockfile.go` (pidfile parsing, pidfile/socket removal helpers) has no
  platform build tag and compiles and behaves identically on every
  platform, Windows included.
- `lockfile_unix.go` (`ProcessAlive` backed by `kill(pid, 0)`) is
  constrained to `//go:build darwin || linux` and never compiles on
  Windows. `lockfile_windows.go` supplies the same function signature,
  always returning `ProcessLivenessUndecided` with a static error, so the
  package as a whole always compiles under `GOOS=windows`.
- `Scan` itself checks `runtime.GOOS == "windows"` as its very first
  step and returns immediately with a logged warning, before touching a
  pidfile, a socket, or a lock. The Windows stub of `ProcessAlive` is
  therefore unreachable in the actual recovery flow on that platform; it
  exists purely to satisfy the build, not to be exercised at runtime.

If you add runtime code that touches process liveness, pidfiles, or
sockets, keep the cross-compile green: run (or expect CI to run)
`GOOS=windows GOARCH=amd64 go build ./internal/runtime/...` against your
change before it lands. Anything that only makes sense on a POSIX
target belongs behind a `darwin || linux` build tag with a Windows
counterpart file, not behind an unconditional import that breaks the
Windows build.
