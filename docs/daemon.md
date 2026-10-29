# Daemon startup, IPC surface, and crash recovery

This document covers the daemon's startup sequence, the IPC surface it
serves once started, and its crash-safety recovery scan: what the scan
checks, how it decides an artifact is stale, and what it does when it
cannot decide. Package home: [`internal/runtime`](../internal/runtime)
(`bootstrap.go`, `recovery.go`, `recovery_scan.go`, `lockfile.go`,
`domain_registry.go`) and [`internal/daemon`](../internal/daemon)
(`lifecycle_unix.go`, `lifecycle_unix_serve.go`, `daemon.go`).

## IPC surface

Once the socket is bound, every accepted connection is served for real:
`POST /rpc` (JSON-RPC 2.0, `internal/rpc`) and `GET /events` (a
server-sent-events bridge to the daemon's internal event bus,
`internal/rpc`'s `SSEHandler`) are both mounted on the same
`*http.Server`. A connection accepted while the daemon is mid-drain
(upgrade-in-place or ordinary shutdown) is refused rather than accepted
and silently dropped. `GET /events` binds to a single event-bus namespace,
`"daemon"`, the only namespace anything publishes to today (the
upgrade-in-place engine's `ShutdownRequested` event).

## Startup sequence

`cascade daemon run`'s real entrypoint
(`cmd/cascade/daemon_unix.go`'s `platformDaemonRun`) runs these steps in
order:

1. Resolve paths and load `config.toml`, running the schema-version
   upgrade frame if needed, and resolve `[daemon]` settings (socket path,
   shutdown grace).
2. Construct the log provider.
3. Open the daemon's on-disk store (`cascade.db`, `providers/sqlite`) and
   construct the event bus over it.
4. Run the crash-safety recovery scan (`runtime.Scan`, described below),
   with a real `StoreDomainRegistry` (backed by the same store) wired in
   for the orphaned-advisory-lock step.
5. Derive the daemon's run context from the caller's context and create
   the one subsystem manifest that `status.get` reports. The background
   subsystems (the retention scheduler loop, the fleet metrics and CI
   attention consumers, the memory projection loop, the jobs scheduler,
   the harness session watch and the Telegram bridge poll) start under
   that run context, and the manifest tracks each of their goroutines so
   shutdown can join them. The loops started through the supervisor also
   recover a panic, reported as a failed subsystem whose detail starts
   with `panic:`; a clean return is reported as skipped with detail
   `stopped`.
6. Serve the IPC surface (above) until a termination signal or the
   context is canceled. The upgrade hand-off (see below) answers
   `SIGUSR2` while serving.

## Shutdown order

However serving ends (the context is canceled, a termination signal
arrives, or an upgrade attempt returns), the daemon tears down in this
order:

1. Cancel the run context. Nothing the daemon started waits on a signal
   of its own; the Telegram bridge poll stops on this cancellation too.
2. Join: wait until every supervised goroutine has returned, including
   the bridge drain.
3. Run the background cleanup (stop the config watcher, release the
   retention scheduler's lock).
4. Close the fleet runtime (see [Fleet runtime](#fleet-runtime)).
5. Close the event bus, which stops any delivery goroutine a subscriber
   left running.
6. Close the store, then the log provider.

The join comes before the store close on purpose: a background goroutine
still running when the store closes would read or write a closed
database.

`internal/runtime.Bootstrap` covers the same path/config/log sequence for
callers that do not also need the daemon-specific store, event bus, and
recovery-registry wiring; the daemon's own run path calls `runtime.Scan`
directly rather than through `Bootstrap`, so it keeps using the same
injectable `PathProvider` every other `cascade daemon` verb in that file
uses, rather than `Bootstrap`'s own `Getenv`/`HomeDir`-based path
resolution.

The recovery scan runs before the IPC surface is served. This placement is
deliberate: the scan's own socket probe is the daemon's write-arbitration
contract's socket-probe-first rule, and it has to run before any lock on
the daemon's store or advisory-lock state is taken. A scan that ran after
lock acquisition could never clean up the very lock blocking it, since it
would already be blocked waiting on that lock.

The scan's result (a `*RecoveryEvent`, or `nil` on a clean start) is
published to the daemon's event bus by the scan itself; the caller has
nothing further to do with it.

## Fleet runtime

`composeDaemon` builds one `daemon.FleetRuntime` after the store, the bus,
the run context and the manifest exist and before any registration runs. It
is handed to every registration as `w.Deps.Fleet`; no registration builds its
own. `NewFleetRuntime` runs an ordered list of builder steps and returns the
first failure wrapped with the step name, after closing what the earlier
steps built, so a failed step stops the daemon from starting.

The first step is admission: it reads the `[governor]` table through
`governor.Load`, builds the one `governor.Sampler` and the one
`governor.AdmissionController` over it, and runs the sampler as the
supervised subsystem `governor-sampler` on the run context. A later
ticket adds its own step and file; the dependencies a step needs are already
fields of `FleetRuntimeDeps`.

The sampler goroutine is joined like every other supervised goroutine: the
run context ends it and `Manifest.Wait` returns only after its ticker has
stopped. `FleetRuntime.Close` then stops the sampler (the controller refuses
new and queued admissions with a draining error) and runs the closers the
later steps registered, in reverse. It is idempotent.

`status.get` lists the subsystems `governor-sampler`, `admission-posture` (the
row refresher) and `admission`. The last one's detail is
`posture=<posture>` (see [Daemon admission](governor.md#daemon-admission)).

### Reservation substrate

The second step, `reservation`, opens `cascade.db` once. The handle allows one
connection, and every transaction on it takes the write lock at `BEGIN`
(`_txlock=immediate`) and waits under `_busy_timeout=5000`, so it never fails
with `SQLITE_BUSY_SNAPSHOT` against the store's own writes. The jobs, outbox,
reservation and topology schemas are applied on that handle, and everything
below shares it:

- **One jobs store and one lease authority.** `Jobs()` and `Leases()` are the
  only `jobs.Store` and `jobs.LeaseManager` in the daemon. The lease manager
  asks the controller binding (`<data>/nodes/controller_binding.json`) on every
  call, so a node-role daemon grants, renews and releases no lease, and a
  re-designation takes effect without a restart. Its events go to the daemon
  bus, the fleet journal and the attention queue. A job that reaches a
  terminal state, the completion policy's commit included, releases every
  lease it holds.
- **Fenced worktrees.** `Worktrees()` comes from the `jobs.worktree.sweep`
  subsystem: built with the lease manager's fence, and its orphan sweep runs
  once at build. A create under a lease that is no longer current is refused.
- **One reserver.** `Reserver()` is the one `economics.Reserver`, with an owner
  epoch minted per daemon start (`OwnerEpoch()`). A reservation's lease id is
  `<repo>:<glob>@<epoch>`: the lease and the grant it was made at, so a later
  holder's grant of the same scope is never renewed or released by it.
- **The `job.*`/`lease.*` RPCs** run over this store and lease manager and open
  no database of their own.

Before the socket serves, the `fleet-node-jobs` registration runs the start
sweep. It reads the pidfile and refuses to start (`ErrConcurrentDaemon`) when
it names a running daemon other than this process. Otherwise every held or
parked reservation of a dead epoch is recovered and rolled back with its leases
and worktree released, and every committed one is adopted. The
`reservation-sweep` row of `status.get` carries the counts and the new epoch,
for example `rolled_back=1 adopted=1 expired=0 rollback_errors=0
owner_epoch=daemon-…`. A row the sweep could not retire marks that row failed
without stopping the daemon; a sweep that cannot read the reservation rows at
all refuses the start. The pidfile check runs before the socket's lifetime
lock is taken, so two daemons started at the same instant can both sweep: the
reservation ledger's compare-and-set writes keep each row retired or adopted
once, and the loser exits when it cannot take the socket. A reservation whose
lease was reclaimed (a later epoch) no longer finds its worktree during
recovery; the worktree is left for the orphan sweep of the next daemon start
rather than risk touching a later holder's tree.

The supervised subsystem `fleet-reservation-liveness` then ticks every lease
renew interval (15 minutes): it heartbeats this daemon's reservations and
renews their leases, retires reservations whose heartbeat is older than three
intervals (skipped on a tick whose heartbeat write failed), and, on the
controller, expires overdue leases and reclaims those whose holder process is
gone. An adopted committed row that nothing claims is therefore released after
three intervals. The loop is joined with the other supervised goroutines, and
`Close` closes the handle once, after the join and before the store.

### Capacity

The third step, `capacity`, builds the one `capacity.Fleet` (`Fleet()`) and
the compositor behind `fleet.capacity`. It opens the provider registry
(`<data>/providers.db`), applies the learn schema (SchemaVersion 2) on the
runtime handle and creates the aggregation salt at
`<data>/learn/aggregate_salt` (mode 0600) once. The Fleet's parts are:

- the learn capability scorer, estimate source and scheduler-decision writer
  (the decision is written inside the lease's `Bind` transaction);
- the interim selector, `NewTierPolicySelector` over
  `NewTierPolicy(estimate, NewDefaultProbeAdmission(), clock)`;
- `DomainOf`: a registry lane resolves through the topology credential
  `Reconcile` named after it. An un-reconciled lane, a quarantined credential
  and a domain with no live lane refuse `ErrNoQuotaDomain`;
- placement over the enrolled node records, and the controller id read once
  from the controller binding (its endpoint on a node daemon, `controller`
  otherwise). The runtime holds no tunnel registry yet, so a placement need
  refuses; work without one runs on the controller.

The supervised subsystem `fleet-capacity-refresh` reads the providers, the
node records and the latest admission sample into the compositor every two
seconds on the run context (node slots are re-stamped on a change and every ten seconds). A slot not refreshed for 30 seconds reads
unknown. Each material change publishes `fleet.capacity_changed` on the bus.
The loop is joined with the others; `Close` closes the registry handle after
the join and before the store.

A second supervised subsystem, `fleet-learn-aggregate`, runs
`learn.Aggregator.Aggregate` over the runtime handle every hour with the
salt above; a failed pass is logged and retried on the next tick.
`SetAgentLanes(fn)` registers the source of agent lanes: the Fleet's
snapshot (not the `fleet.capacity` result) gains one available tier-zero
`agent:<driver>` lane per entry.

`fleet.capacity` and the `fleet.capacity` topic on `GET /events` are
owner-only (declared in `internal/fleet/capacity/rpc.go`, mounted by
`wire_fleet_capacity.go`). A principal caller is refused
`KindPermissionDenied`. There is no `fleet.lease` RPC: leasing stays in
process.

## Crash recovery

The recovery scan handles the case where the daemon's previous process
did not exit cleanly: killed with `SIGKILL`, terminated by an OOM killer,
or lost to a power failure. In all of these cases the process cannot run
its own shutdown code, so anything it would normally clean up on exit
(its pidfile, its IPC socket file, any advisory locks it held) is left
behind on disk.

The scan's first action, before touching the pidfile or any lock, is a
socket probe: it attempts to connect to the daemon's IPC socket. If a live
daemon answers, the scan aborts immediately with no cleanup performed.
A live daemon is never disturbed. Only when the probe confirms no live
daemon is listening does the scan proceed to check the pidfile and
advisory locks.

On Windows, this whole scan is skipped: the daemon socket probe is
unix-socket only, and the daemon itself is not supported on Windows. See
[Windows](#windows) below.

### Fan-out cursors

A `conductor.execute` call with `fan_out` of 2 or more writes a cursor, a
request record and one result per finished leg to the daemon store, so a
crash mid-call loses nothing that already ran. The daemon never
re-dispatches such a call on its own. Nobody is waiting for that output,
and a provider call without a caller only costs money.

At startup, before the socket serves, the daemon classifies every fan-out
cursor and dispatches nothing. A cursor with every leg recorded, or one
that is still resumable, is kept for its client. `status.get` reports
subsystem `fanout-resume` as started with detail `pending N`, where N
counts those kept cursors. A cursor that cannot resume (no request record,
or a leg the policy refused) is finalized and its records are deleted.

Recovery is the client's re-attach. `cascade run` prints the request id
to stderr before it sends a fan-out. After a crash, run the same command
again with `--resume <request_id>`: the daemon re-authorizes each stored
leg before returning it, runs only the legs that never finished, and
delivers all of them to that caller. A leg started three times without an
ok outcome is never sent again; the re-attach then ends with an
`unknown_outcome` refusal.

A fan-out nobody re-attaches to is expired by the `fanout-sweep`
subsystem, which runs at startup and then on a fixed interval. Once the
last journal entry of a fan-out is older than the record ttl, the sweep
writes an `expired` marker and deletes its records, still without a
provider call. Both values live in `[daemon]`:

```toml
[daemon]
fanout_record_ttl = "24h"     # default 24h
fanout_sweep_interval = "1h"  # default 1h
```

Each takes a Go duration string or a number of seconds, and must be
positive. A malformed value stops the daemon from starting. With no
daemon store (some test builds) `fanout-resume` is reported as skipped
with `no daemon store`. If the conductor's executor could not be built,
it is skipped with `pending (executor unavailable)`: nothing is
classified or marked at startup, and the daemon keeps serving. The
`fanout-sweep` still runs whenever the claim table and journal were
built, so unattached fan-outs still expire at `fanout_record_ttl`.

## Artifact classes

The scan checks three kinds of leftover state, in this order.

### IPC socket

Staleness is decided by attempting to dial the socket path:

- No file exists at the socket path: nothing to check. This is the
  ordinary clean-startup case, not a stale-socket case.
- The dial succeeds (something answers): a live daemon is running. The
  scan aborts with an error and performs no cleanup at all, not even
  removing files that later turn out to be genuinely stale.
- The dial fails with connection-refused: the socket file exists but
  nothing is listening on it. This is the confirmed-stale case. The
  socket file is removed and a warning is logged.
- The dial fails any other way (permission denied, an unexpected network
  error): this cannot be classified as either live or stale. The scan
  treats this as undecidable and aborts with an error rather than
  guessing.

### Pidfile

The pidfile is read and its content parsed as an integer pid:

- No pidfile exists: nothing to check, ordinary clean state.
- The content is not a valid pid (empty, non-integer, non-positive): the
  scan logs a warning and leaves the pidfile in place. This is reported,
  not silently ignored, but it does not stop the scan.
- The content parses to a pid: the scan checks whether that pid is a live
  process. Only when the check unambiguously confirms the process is dead
  does the scan remove the pidfile and log a warning naming the pid. If
  the process is confirmed alive, or if the liveness check itself could
  not decide, the pidfile is left in place and a warning is logged
  explaining why.

### Orphaned advisory locks

This step depends on an injected registry of advisory-lock records. In
the real daemon startup path this is `runtime.StoreDomainRegistry`
(`internal/runtime/domain_registry.go`): a `provider.Store`-backed
ledger of `lock_id`/`owner_pid` records, sharing the daemon's own
`cascade.db`. A caller that supplies no registry (`nil`) skips the step
outright, reported as such rather than silently pretending to check;
this is still true for any composition root that has no store to build
one from. Where a registry is present:

- Every advisory-lock record is checked against its stored owner pid.
- Only locks whose owner pid is unambiguously confirmed dead are
  released.
- A lock whose owner is alive, or whose liveness could not be decided, is
  left in place.
- If a single lock's release fails, the scan logs a warning and continues
  checking the remaining locks rather than stopping.
- If listing the locks fails outright, the scan logs a warning and skips
  this step entirely. Any pidfile or socket cleanup already performed
  earlier in the scan is unaffected.

## The undecidable case: refuse rather than guess

The scan's liveness check on a pid has three possible outcomes, not two:

- **Dead**: the operating system unambiguously reports no such process.
  This is the only outcome that licenses removing anything.
- **Alive**: either a process genuinely holds that pid, or a process
  holds it but the daemon lacks permission to signal it. Both cases are
  reported as alive, because a permission failure proves a process
  exists even though it cannot prove which one.
- **Undecided**: an unexpected error from the liveness check itself, one
  the check cannot classify as either of the above.

Alive and Undecided are both treated as "do not delete." The scan never
removes a pidfile, a socket, or a lock unless it can prove the owning
process is gone. When it cannot prove that, it leaves the artifact in
place, logs a warning explaining why, and moves on.

The socket probe has its own, stricter version of this same posture: an
undecidable dial failure does not just skip cleanup for the socket, it
aborts the whole scan with an error, so the daemon does not proceed to
load config and start up in an uncertain state. What the operator sees
in this case is the daemon failing to start with an error naming the
socket path and the underlying dial failure. This error carries no
specific taxonomy classification and surfaces as an internal error; the
fix is to investigate why the socket path is not dialable (commonly a
permissions problem on the socket's parent directory) rather than to
retry blindly.

## Pid recycling

A pidfile or a lock record naming a pid that is currently alive is never
touched, even if the scan has reason to believe the recorded pid belongs
to a long-dead process. The operating system's own report of "this pid is
alive right now" is trusted over any other evidence, because treating a
live pid as stale risks two failure modes at once: deleting a lock or
pidfile a running daemon still depends on, and in the worst case
signaling or interfering with an unrelated process that has since reused
the same pid number. The scan has no way to distinguish "the original
daemon, still running" from "an unrelated process that reused this pid"
without additional bookkeeping it does not do, so it treats both alike:
alive means untouched.

## Recovery event

When the scan finds and cleans up at least one stale artifact, it
constructs a recovery event and publishes it to the daemon's event bus
(if one was supplied at startup; publishing is skipped, not faked, when
none was). Its fields:

| Field | Meaning |
|---|---|
| `stale_pid` | `true` if a stale pidfile was found and removed. |
| `stale_socket` | `true` if a stale socket file was found and removed. |
| `stale_locks` | List of advisory-lock IDs that were released as orphaned. |
| `recovered_at` | Timestamp of the scan, from the daemon's injected clock. |

A clean startup, with nothing stale found, produces no event at all. No
event is published, and none is returned to the caller. Absence of an
event is the signal that startup was clean; it is not an omission to
watch for.

The scan is idempotent: running it again immediately afterward, against
the now-clean state it just produced, finds nothing to do and again
produces no event.

## Windows

The daemon is not currently supported on Windows. The recovery scan
detects this platform at its very first step and returns immediately,
logging a warning that the unix-socket probe was skipped for this
platform, without touching a pidfile or any lock. An operator running the
daemon on Windows sees this warning in the log and no further recovery
activity; nothing downstream of the scan on Windows depends on it having
done any cleanup.

## Operator troubleshooting

**Startup fails immediately, before config or logging come up.**
This is either the live-daemon case or the undecidable-socket case, both
from the socket probe step, since that step runs first and aborts on
either outcome.

- A message naming a live daemon already answering the socket means
  another daemon process is already running against this socket path.
  Check for a running `cascade` daemon process before doing anything
  else; do not delete the socket file by hand while a live daemon holds
  it.
- A message describing the socket probe as undecidable, naming the
  socket path and an underlying dial error, means the daemon could not
  tell whether a process is listening or not. Check filesystem
  permissions on the socket path and its parent directory. Do not delete
  the socket file to work around this without first confirming no daemon
  process is actually running; if the probe is undecidable because of a
  transient permission problem, fixing that problem lets the next start
  attempt classify the socket correctly on its own.

**Startup succeeds but a pidfile or lock warning appears in the log.**
Look for warning lines describing a pidfile or lock as present but not
confirmed dead. This means the daemon found leftover state naming a pid
it could not prove was gone, and left it untouched rather than guessing.
If you are confident no such process is running (for example, you have
independently verified the pid does not exist), the leftover file can be
removed by hand once you are certain no daemon is running.

**Telling "another daemon is running" apart from "stale artifacts need
cleaning."** The socket probe answers this directly and is checked first:
if it finds a live listener, the daemon is running, full stop, and no
cleanup runs. If the probe finds no listener, anything left behind
(pidfile, socket file, locks) is by definition not backed by a running
process holding the socket open, and the recovery scan handles it
according to the rules above.

**Log lines to look for**, all emitted at warning level:

```
runtime: removed stale socket file
runtime: pidfile present but unparsable; skipping removal
runtime: pidfile present but owner pid is not confirmed dead; skipping removal
runtime: removed stale pidfile
runtime: recovery: could not list orphaned locks; skipping lock cleanup
runtime: recovery: failed to release orphaned lock
runtime: recovery: released orphaned advisory lock
platform-unsupported: unix socket probe skipped
```

Each of these lines carries structured fields (path, pid, lock ID, or
error) alongside the message; consult those fields for the specifics of
what the scan found.

## Upgrade in place

The daemon hands off to a replaced binary only when it receives
`SIGUSR2` (`daemon.UpgradeSignal`), the signal the self-update sends once
it has installed a new binary. `SIGTERM` and `SIGINT` never relaunch:
`cascade daemon stop`, `restart`, service managers and Ctrl-C always drain
and exit, even after an out-of-band replacement (a package manager, an
install script re-run, a `go build -o` over the installed path). Package
home: [`internal/daemon`](../internal/daemon) (`upgrade.go`,
`upgrade_skew.go`, `upgrade_conntracker.go`).

**Build identity is a content digest.** Before its socket exists the
daemon resolves its own executable (`os.Executable`, then every symlink)
and records the SHA-256 of that file. That digest is captured once and
never re-read. No linker flag or version stamp takes part: a binary cannot
embed its own digest, and a version string never equals one. If the
executable cannot be resolved or read at start, the build hash is the
`dev` sentinel and the hand-off is not wired.

**Skew.** On `SIGUSR2` the daemon resolves the install path again and
hashes the file it names now. Different bytes mean skew. A file rewritten
with identical bytes is not skew. A symlinked install that now points at
a different file is compared by that file's content. If the installed file
cannot be read, the check fails closed: the error is logged, nothing is
drained or executed, and the daemon keeps serving. It never treats an
unreadable file as "unchanged".

With no skew the daemon logs `upgrade requested, binary unchanged` and
keeps serving. With skew it drains and re-execs in place:

1. Stop accepting new IPC connections; a connection accepted after this
   point is refused rather than accepted and silently dropped.
2. Wait, up to the configured `shutdown_grace`, for any in-flight
   connection to finish. Anything still running once grace elapses is
   force-closed.
3. Cancel the run context and join the supervised goroutines, bounded by
   the same `shutdown_grace`. A goroutine still running when grace ends is
   logged and the relaunch goes ahead anyway: a stuck goroutine never
   blocks an upgrade.
4. `exec()` the file it just hashed, with the same arguments and
   environment. The process keeps its PID; no new process is spawned.

**Residuals.** A file replaced between the daemon's exec and its startup
hash is recorded as the running binary; the window is the daemon's own
start and needs a same-user writer. A `SIGUSR2` that arrives before the
daemon installs its signal handler (its first milliseconds) stops it,
because that is Go's default action for the signal; the self-update sends
it only after the daemon reports running, and a missed hand-off fails
closed. On Linux `os.Executable` already returns the resolved file, so a
symlinked install is followed only by its target's content. After a
relaunch the new image's install path is the file that was executed.

If the `exec()` call itself fails (the new binary is missing or not
executable, for example), the daemon has already drained and cannot go
back to serving. It falls through to its normal shutdown path instead
of trying to limp on with a closed listener. The pidfile and socket are
cleaned up exactly as they are on any other clean exit, so `cascade
daemon start` (or the crash-recovery scan) finds a clean, restartable
state rather than a half-alive process.

**Resume leg (allowed-fail, W1).** Before a drain begins, the daemon
writes a best-effort checkpoint of its current write-ahead-log position.
On the next startup this checkpoint is read back and logged if present;
if it is absent or unreadable, startup proceeds as a clean start with no
error. Actually resuming in-flight session state from that checkpoint is
not implemented yet: this leg only records and reports the position.

**Windows.** The daemon does not run on Windows at all (tier-2), so
there is nothing to upgrade in place there; every daemon verb, including
`restart`, refuses with the same typed unsupported-platform error.
